package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// RefillMode controls how the token bucket handles sub-millisecond precision.
type RefillMode int

const (
	// RefillSleep uses standard time.Sleep (good for intervals >1ms)
	RefillSleep RefillMode = iota
	// RefillHybrid uses sleep for bulk + spin for final precision (default)
	RefillHybrid
	// RefillSpin uses pure spinning (highest precision, highest CPU)
	RefillSpin
)

// TokenBucket implements a high-precision rate limiter suitable for 500 Mb/s traffic.
//
// At 500 Mb/s with 1456-byte packets, packets arrive every ~23 microseconds.
// Standard time.Sleep() has 1-15ms OS scheduler granularity, causing micro-bursts.
// This implementation uses hybrid sleep+spin for sub-millisecond precision.
//
// Key features:
// - Atomic operations for lock-free hot path (Consume)
// - Sub-byte accumulator for precision at high rates
// - Configurable refill mode (sleep/hybrid/spin)
// - Dynamic rate changes via SetRate()
type TokenBucket struct {
	// Atomic fields for lock-free hot path
	tokens atomic.Int64
	rate   atomic.Int64 // bytes per second

	// Configuration (immutable after construction)
	maxTokens int64
	mode      RefillMode

	// Refill state (protected by mutex)
	mu          sync.Mutex
	lastRefill  time.Time
	accumulator float64 // Sub-byte accumulator for precision

	// Statistics (atomic) - for bottleneck detection
	totalWaitNs  atomic.Int64
	waitCount    atomic.Int64
	spinTimeNs   atomic.Int64
	blockedCount atomic.Int64 // Times ConsumeOrWait had to wait
	lastJitterNs atomic.Int64 // Last measured inter-packet jitter
	maxJitterNs  atomic.Int64 // Maximum jitter observed
}

// TokenBucketStats holds statistics for monitoring.
type TokenBucketStats struct {
	CurrentRate     int64   // bytes/sec
	CurrentTokens   int64   // available tokens
	AvgWaitNs       int64   // average wait time per consume
	MaxJitterNs     int64   // maximum observed jitter
	SpinTimePercent float64 // % of time spent spinning
}

// TokenBucketDetailedStats holds detailed statistics for bottleneck detection.
// See: client_seeker_instrumentation_design.md
type TokenBucketDetailedStats struct {
	// Rate and capacity
	RateBps         int64  // Current rate in bits per second
	TokensAvailable int64  // Current tokens available
	TokensMax       int64  // Maximum token capacity
	Mode            string // "sleep", "hybrid", or "spin"

	// Timing metrics for bottleneck detection
	TotalWaitNs  int64 // Total time blocked waiting for tokens
	SpinTimeNs   int64 // Time spent in spin-wait loops
	WaitCount    int64 // Number of ConsumeOrWait calls
	BlockedCount int64 // Times consume had to wait (tokens were insufficient)

	// Derived metrics
	AvgWaitNs int64 // Average wait time per consume
}

// NewTokenBucket creates a high-precision rate limiter.
//
// Parameters:
//   - bitsPerSecond: Target rate in bits per second
//   - mode: RefillMode controlling precision vs CPU tradeoff
//
// The bucket capacity (maxTokens) is set to allow small bursts while
// preventing large accumulations that could cause network congestion.
func NewTokenBucket(bitsPerSecond int64, mode RefillMode) *TokenBucket {
	bytesPerSecond := bitsPerSecond / 8

	// Allow 10ms worth of burst (prevents micro-starvation)
	maxTokens := bytesPerSecond / 100
	if maxTokens < 1456 {
		maxTokens = 1456 // At least one packet
	}
	if maxTokens > 1456*100 {
		maxTokens = 1456 * 100 // Cap at 100 packets (~146KB)
	}

	tb := &TokenBucket{
		maxTokens:  maxTokens,
		mode:       mode,
		lastRefill: time.Now(),
	}
	tb.rate.Store(bytesPerSecond)
	tb.tokens.Store(maxTokens) // Start full

	return tb
}

// SetRate atomically updates the target rate.
// Called by BitrateManager when orchestrator changes bitrate.
func (tb *TokenBucket) SetRate(bitsPerSecond int64) {
	bytesPerSecond := bitsPerSecond / 8
	tb.rate.Store(bytesPerSecond)
}

// Rate returns the current rate in bits per second.
func (tb *TokenBucket) Rate() int64 {
	return tb.rate.Load() * 8
}

// Consume attempts to consume tokens without blocking.
// Returns true if successful, false if insufficient tokens.
//
// This is the hot path - uses lock-free atomic CAS loop.
func (tb *TokenBucket) Consume(bytes int64) bool {
	for {
		current := tb.tokens.Load()
		if current < bytes {
			return false
		}
		if tb.tokens.CompareAndSwap(current, current-bytes) {
			return true
		}
		// CAS failed, retry
	}
}

// ConsumeOrWait blocks until tokens are available, then consumes them.
// Uses appropriate wait strategy based on RefillMode and wait duration.
//
// Returns error if context is canceled or rate is zero.
func (tb *TokenBucket) ConsumeOrWait(ctx context.Context, bytes int64) error {
	// Check rate first to fail fast
	if tb.rate.Load() == 0 {
		return fmt.Errorf("rate is zero")
	}

	startWait := time.Now()
	wasBlocked := false

	for {
		// Try to consume immediately
		if tb.Consume(bytes) {
			// Record wait time
			waitNs := time.Since(startWait).Nanoseconds()
			tb.totalWaitNs.Add(waitNs)
			tb.waitCount.Add(1)
			if wasBlocked {
				tb.blockedCount.Add(1)
			}
			return nil
		}

		// Mark that we had to wait (for bottleneck detection)
		wasBlocked = true

		// Calculate how long to wait for tokens
		rate := tb.rate.Load()
		if rate == 0 {
			return fmt.Errorf("rate is zero")
		}

		// Tokens needed = bytes - current tokens
		current := tb.tokens.Load()
		needed := bytes - current
		if needed <= 0 {
			continue // Race: tokens appeared, retry consume
		}

		// Wait time = needed_bytes / rate * 1e9 (nanoseconds)
		waitNs := int64(float64(needed) / float64(rate) * 1e9)

		// Check context before waiting
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Wait using appropriate strategy
		switch tb.mode {
		case RefillSpin:
			tb.spinWait(ctx, time.Duration(waitNs))
		case RefillHybrid:
			tb.hybridWait(ctx, time.Duration(waitNs))
		default: // RefillSleep
			tb.sleepWait(ctx, time.Duration(waitNs))
		}

		// Refill tokens after wait
		tb.refill()
	}
}

// sleepWait uses standard time.Sleep (lowest CPU, lowest precision).
func (tb *TokenBucket) sleepWait(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		return
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}

// hybridWait uses sleep for bulk wait, spin for final precision.
// Achieves sub-millisecond accuracy while minimizing CPU usage.
func (tb *TokenBucket) hybridWait(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		return
	}

	// For very short waits (<100µs), spin only
	if duration < 100*time.Microsecond {
		tb.spinWait(ctx, duration)
		return
	}

	// For medium waits (100µs - 1ms), sleep most, spin last 50µs
	if duration < time.Millisecond {
		sleepDuration := duration - 50*time.Microsecond
		tb.contextAwareSleep(ctx, sleepDuration)
		tb.spinWait(ctx, 50*time.Microsecond)
		return
	}

	// For longer waits (>1ms), sleep most, spin last 100µs
	sleepDuration := duration - 100*time.Microsecond
	tb.contextAwareSleep(ctx, sleepDuration)
	tb.spinWait(ctx, 100*time.Microsecond)
}

// contextAwareSleep sleeps but can be interrupted by context cancellation.
func (tb *TokenBucket) contextAwareSleep(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		return
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}

// spinWait uses active spinning for maximum precision.
// Yields to scheduler periodically to prevent CPU monopolization.
func (tb *TokenBucket) spinWait(ctx context.Context, duration time.Duration) {
	if duration <= 0 {
		return
	}

	start := time.Now()
	spinStart := start
	spins := 0

	for time.Since(start) < duration {
		spins++

		// Check context every 1000 spins (~10-50µs)
		if spins%1000 == 0 {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}

		// Yield to scheduler every 100 spins to prevent monopolization
		if spins%100 == 0 {
			runtime.Gosched()
		}
	}

	// Record spin time for statistics
	spinNs := time.Since(spinStart).Nanoseconds()
	tb.spinTimeNs.Add(spinNs)
}

// refill adds tokens based on elapsed time since last refill.
// Uses sub-byte accumulator for precision at high rates.
func (tb *TokenBucket) refill() {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)
	tb.lastRefill = now

	rate := tb.rate.Load()
	if rate == 0 {
		return
	}

	// Calculate tokens to add (with sub-byte precision)
	addBytes := float64(rate) * elapsed.Seconds()
	tb.accumulator += addBytes

	// Only add whole bytes to token count
	wholeBytes := int64(tb.accumulator)
	tb.accumulator -= float64(wholeBytes)

	if wholeBytes <= 0 {
		return
	}

	// Add tokens, capped at maxTokens
	for {
		current := tb.tokens.Load()
		newTokens := current + wholeBytes
		if newTokens > tb.maxTokens {
			newTokens = tb.maxTokens
		}
		if tb.tokens.CompareAndSwap(current, newTokens) {
			return
		}
	}
}

// StartRefillLoop starts a background goroutine that periodically refills tokens.
// This ensures tokens accumulate even when no Consume calls are made.
//
// The refill frequency depends on the mode:
// - Sleep: every 1ms
// - Hybrid: every 100µs
// - Spin: every 50µs
func (tb *TokenBucket) StartRefillLoop(ctx context.Context) {
	var interval time.Duration
	switch tb.mode {
	case RefillSpin:
		interval = 50 * time.Microsecond
	case RefillHybrid:
		interval = 100 * time.Microsecond
	default:
		interval = time.Millisecond
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tb.refill()
			}
		}
	}()
}

// Stats returns current statistics.
func (tb *TokenBucket) Stats() TokenBucketStats {
	waitCount := tb.waitCount.Load()
	var avgWaitNs int64
	if waitCount > 0 {
		avgWaitNs = tb.totalWaitNs.Load() / waitCount
	}

	// Calculate spin time percentage (rough approximation)
	spinNs := tb.spinTimeNs.Load()
	totalWaitNs := tb.totalWaitNs.Load()
	var spinPercent float64
	if totalWaitNs > 0 {
		spinPercent = float64(spinNs) / float64(totalWaitNs) * 100
	}

	return TokenBucketStats{
		CurrentRate:     tb.rate.Load() * 8, // Convert to bits
		CurrentTokens:   tb.tokens.Load(),
		AvgWaitNs:       avgWaitNs,
		MaxJitterNs:     tb.maxJitterNs.Load(),
		SpinTimePercent: spinPercent,
	}
}

// DetailedStats returns detailed statistics for bottleneck detection.
// See: client_seeker_instrumentation_design.md
func (tb *TokenBucket) DetailedStats() TokenBucketDetailedStats {
	waitCount := tb.waitCount.Load()
	var avgWaitNs int64
	if waitCount > 0 {
		avgWaitNs = tb.totalWaitNs.Load() / waitCount
	}

	// Convert mode to string
	var modeStr string
	switch tb.mode {
	case RefillSleep:
		modeStr = "sleep"
	case RefillHybrid:
		modeStr = "hybrid"
	case RefillSpin:
		modeStr = "spin"
	default:
		modeStr = "unknown"
	}

	return TokenBucketDetailedStats{
		RateBps:         tb.rate.Load() * 8,
		TokensAvailable: tb.tokens.Load(),
		TokensMax:       tb.maxTokens,
		Mode:            modeStr,
		TotalWaitNs:     tb.totalWaitNs.Load(),
		SpinTimeNs:      tb.spinTimeNs.Load(),
		WaitCount:       waitCount,
		BlockedCount:    tb.blockedCount.Load(),
		AvgWaitNs:       avgWaitNs,
	}
}

// ResetStats resets all statistics counters.
// Useful for getting clean measurements during testing.
func (tb *TokenBucket) ResetStats() {
	tb.totalWaitNs.Store(0)
	tb.waitCount.Store(0)
	tb.spinTimeNs.Store(0)
	tb.blockedCount.Store(0)
	tb.lastJitterNs.Store(0)
	tb.maxJitterNs.Store(0)
}

// RecordJitter records inter-packet timing jitter for monitoring.
// Called by the data generator after each packet send.
func (tb *TokenBucket) RecordJitter(jitterNs int64) {
	tb.lastJitterNs.Store(jitterNs)

	// Update max jitter (atomic CAS loop)
	for {
		current := tb.maxJitterNs.Load()
		if jitterNs <= current {
			return
		}
		if tb.maxJitterNs.CompareAndSwap(current, jitterNs) {
			return
		}
	}
}
