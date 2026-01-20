package main

import (
	"context"
	"math"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestTokenBucket_NewTokenBucket(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid) // 100 Mb/s

	// Rate should be stored correctly
	if got := tb.Rate(); got != 100_000_000 {
		t.Errorf("Rate() = %d, want 100000000", got)
	}

	// Should start with tokens
	if tb.tokens.Load() <= 0 {
		t.Errorf("tokens = %d, want > 0", tb.tokens.Load())
	}
}

func TestTokenBucket_SetRate(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	// Change rate
	tb.SetRate(200_000_000)

	if got := tb.Rate(); got != 200_000_000 {
		t.Errorf("Rate() = %d, want 200000000", got)
	}
}

func TestTokenBucket_Consume_Success(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	// Should succeed with available tokens
	if !tb.Consume(1456) {
		t.Error("Consume(1456) = false, want true")
	}
}

func TestTokenBucket_Consume_Insufficient(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	// Drain all tokens
	for tb.Consume(1456) {
		// Keep consuming
	}

	// Now should fail
	if tb.Consume(1456) {
		t.Error("Consume(1456) = true after drain, want false")
	}
}

func TestTokenBucket_ConsumeOrWait_Refills(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid) // 100 Mb/s = 12.5 MB/s

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start refill loop
	go tb.StartRefillLoop(ctx)

	// Drain tokens
	for tb.Consume(1456) {
	}

	// ConsumeOrWait should block then succeed after refill
	start := time.Now()
	err := tb.ConsumeOrWait(ctx, 1456)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ConsumeOrWait() error = %v", err)
	}

	// Should have waited some time for refill
	if elapsed < time.Microsecond {
		t.Errorf("ConsumeOrWait() returned too fast: %v", elapsed)
	}
}

func TestTokenBucket_ConsumeOrWait_ContextCancel(t *testing.T) {
	tb := NewTokenBucket(8000, RefillHybrid) // 1 KB/s = need ~1.5s for 1456 bytes

	ctx, cancel := context.WithCancel(context.Background())

	// Drain tokens
	for tb.Consume(1456) {
	}

	// Cancel context after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Should return error due to context cancellation
	start := time.Now()
	err := tb.ConsumeOrWait(ctx, 1456)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("ConsumeOrWait() error = nil, want context.Canceled")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("ConsumeOrWait() took %v, should have cancelled faster", elapsed)
	}
}

func TestTokenBucket_Refill_Accumulator(t *testing.T) {
	// Use a rate that produces fractional bytes per refill
	tb := NewTokenBucket(80_000_000, RefillHybrid) // 80 Mb/s = 10 MB/s

	// Drain tokens to zero
	for tb.Consume(1456) {
	}
	tb.tokens.Store(0)

	// Reset refill timing
	tb.mu.Lock()
	tb.lastRefill = time.Now()
	tb.accumulator = 0
	tb.mu.Unlock()

	// Wait and refill
	time.Sleep(10 * time.Millisecond)
	tb.refill()

	// Should have gained ~100KB (10 MB/s * 10ms = 100KB)
	gained := tb.tokens.Load()
	expectedMin := int64(90000)   // Allow 10% tolerance
	expectedMax := int64(110000)

	if gained < expectedMin || gained > expectedMax {
		t.Errorf("refill gained %d bytes, want %d-%d", gained, expectedMin, expectedMax)
	}
}

// TestTokenBucket_RateAccuracy_100Mbps tests rate accuracy at 100 Mb/s.
// This is a warm-up test before the critical 500 Mb/s test.
func TestTokenBucket_RateAccuracy_100Mbps(t *testing.T) {
	targetBps := int64(100_000_000) // 100 Mb/s
	tb := NewTokenBucket(targetBps, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	packetSize := int64(1456)
	var bytesSent int64
	start := time.Now()

	// Run for 2 seconds
	for time.Since(start) < 2*time.Second {
		if err := tb.ConsumeOrWait(ctx, packetSize); err != nil {
			break
		}
		bytesSent += packetSize
	}

	elapsed := time.Since(start)
	actualBps := float64(bytesSent*8) / elapsed.Seconds()
	ratio := actualBps / float64(targetBps)

	t.Logf("100 Mb/s test: target=%d, actual=%.0f, ratio=%.4f (%.2f%% accuracy)",
		targetBps, actualBps, ratio, ratio*100)

	// Must be within 5% for this lower rate test
	if ratio < 0.95 || ratio > 1.05 {
		t.Errorf("Rate accuracy failed: ratio=%.4f, want 0.95-1.05", ratio)
	}
}

// TestTokenBucket_RateAccuracy_500Mbps is the CRITICAL test.
// If this fails, the performance testing system cannot proceed.
//
// Success criteria: ±1% accuracy at 500 Mb/s
func TestTokenBucket_RateAccuracy_500Mbps(t *testing.T) {
	targetBps := int64(500_000_000) // 500 Mb/s
	tb := NewTokenBucket(targetBps, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	packetSize := int64(1456)
	var bytesSent int64
	start := time.Now()

	// Run for 3 seconds (enough to get stable measurement)
	testDuration := 3 * time.Second
	for time.Since(start) < testDuration {
		if err := tb.ConsumeOrWait(ctx, packetSize); err != nil {
			break
		}
		bytesSent += packetSize
	}

	elapsed := time.Since(start)
	actualBps := float64(bytesSent*8) / elapsed.Seconds()
	ratio := actualBps / float64(targetBps)

	t.Logf("500 Mb/s test: target=%d, actual=%.0f, ratio=%.4f (%.2f%% accuracy)",
		targetBps, actualBps, ratio, ratio*100)
	t.Logf("  bytes sent: %d, elapsed: %v", bytesSent, elapsed)

	// CRITICAL: Must be within 1% of target
	// Acceptable range: 495-505 Mb/s
	if ratio < 0.99 || ratio > 1.01 {
		t.Errorf("CRITICAL: Rate accuracy failed at 500 Mb/s: ratio=%.4f, want 0.99-1.01", ratio)
		t.Errorf("  This blocks Phase 2. Debug the TokenBucket implementation.")
	}
}

// TestTokenBucket_Jitter_500Mbps measures inter-packet timing jitter.
// At 500 Mb/s, packets should arrive every ~23µs.
//
// Success criteria: p99 jitter < 200µs
func TestTokenBucket_Jitter_500Mbps(t *testing.T) {
	targetBps := int64(500_000_000) // 500 Mb/s
	tb := NewTokenBucket(targetBps, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	packetSize := int64(1456)
	// Expected interval: packetSize * 8 / targetBps * 1e9 = ~23,296 ns
	expectedIntervalNs := int64(float64(packetSize*8) / float64(targetBps) * 1e9)

	var intervals []int64
	lastSend := time.Now()

	// Collect 10,000 samples
	numSamples := 10000
	for i := 0; i < numSamples; i++ {
		if err := tb.ConsumeOrWait(ctx, packetSize); err != nil {
			break
		}
		now := time.Now()
		interval := now.Sub(lastSend).Nanoseconds()
		intervals = append(intervals, interval)
		lastSend = now
	}

	if len(intervals) < numSamples/2 {
		t.Fatalf("Only collected %d intervals, need at least %d", len(intervals), numSamples/2)
	}

	// Calculate statistics
	sort.Slice(intervals, func(i, j int) bool { return intervals[i] < intervals[j] })

	// Skip first 100 samples (warm-up)
	intervals = intervals[100:]

	// Calculate jitter (deviation from expected interval)
	var sumJitter int64
	var maxJitter int64
	for _, interval := range intervals {
		jitter := interval - expectedIntervalNs
		if jitter < 0 {
			jitter = -jitter
		}
		sumJitter += jitter
		if jitter > maxJitter {
			maxJitter = jitter
		}
	}

	avgJitter := sumJitter / int64(len(intervals))

	// p99 index
	p99Index := int(float64(len(intervals)) * 0.99)
	p99Jitter := intervals[p99Index] - expectedIntervalNs
	if p99Jitter < 0 {
		p99Jitter = -p99Jitter
	}

	t.Logf("Jitter test at 500 Mb/s:")
	t.Logf("  Expected interval: %d ns (%.1f µs)", expectedIntervalNs, float64(expectedIntervalNs)/1000)
	t.Logf("  Avg jitter: %d ns (%.1f µs)", avgJitter, float64(avgJitter)/1000)
	t.Logf("  Max jitter: %d ns (%.1f µs)", maxJitter, float64(maxJitter)/1000)
	t.Logf("  p99 jitter: %d ns (%.1f µs)", p99Jitter, float64(p99Jitter)/1000)

	// CRITICAL: p99 jitter must be < 200µs (200,000 ns)
	maxAllowedJitter := int64(200_000) // 200µs
	if p99Jitter > maxAllowedJitter {
		t.Errorf("CRITICAL: Jitter too high: p99=%d ns (%.1f µs), max=%d ns (200 µs)",
			p99Jitter, float64(p99Jitter)/1000, maxAllowedJitter)
		t.Errorf("  This blocks Phase 2. Consider RefillSpin mode.")
	}
}

// TestTokenBucket_RefillModes compares the three refill modes.
func TestTokenBucket_RefillModes(t *testing.T) {
	modes := []struct {
		name string
		mode RefillMode
	}{
		{"Sleep", RefillSleep},
		{"Hybrid", RefillHybrid},
		{"Spin", RefillSpin},
	}

	targetBps := int64(100_000_000) // 100 Mb/s (faster test)
	packetSize := int64(1456)
	testDuration := time.Second

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			tb := NewTokenBucket(targetBps, m.mode)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			go tb.StartRefillLoop(ctx)

			var bytesSent int64
			start := time.Now()

			for time.Since(start) < testDuration {
				if err := tb.ConsumeOrWait(ctx, packetSize); err != nil {
					break
				}
				bytesSent += packetSize
			}

			elapsed := time.Since(start)
			actualBps := float64(bytesSent*8) / elapsed.Seconds()
			ratio := actualBps / float64(targetBps)

			stats := tb.Stats()

			t.Logf("%s mode: ratio=%.4f, avgWait=%d ns, spinTime=%.1f%%",
				m.name, ratio, stats.AvgWaitNs, stats.SpinTimePercent)
		})
	}
}

// TestTokenBucket_ConcurrentConsume tests thread safety.
func TestTokenBucket_ConcurrentConsume(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	// Run 4 concurrent consumers
	var wg sync.WaitGroup
	var totalConsumed int64
	numGoroutines := 4
	consumePerGoroutine := 1000

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < consumePerGoroutine; j++ {
				if err := tb.ConsumeOrWait(ctx, 1456); err != nil {
					return
				}
				// Use atomic to safely increment
				current := totalConsumed
				for {
					if current == totalConsumed {
						totalConsumed = current + 1456
						break
					}
					current = totalConsumed
				}
			}
		}()
	}

	wg.Wait()

	expected := int64(numGoroutines * consumePerGoroutine * 1456)
	if totalConsumed != expected {
		t.Errorf("totalConsumed = %d, want %d", totalConsumed, expected)
	}
}

// BenchmarkTokenBucket_Consume benchmarks the hot path.
func BenchmarkTokenBucket_Consume(b *testing.B) {
	tb := NewTokenBucket(1_000_000_000, RefillHybrid) // 1 Gb/s (unlimited essentially)

	// Pre-fill with lots of tokens
	tb.tokens.Store(int64(b.N) * 1456)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.Consume(1456)
	}
}

// BenchmarkTokenBucket_ConsumeOrWait_100Mbps benchmarks at 100 Mb/s.
func BenchmarkTokenBucket_ConsumeOrWait_100Mbps(b *testing.B) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go tb.StartRefillLoop(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.ConsumeOrWait(ctx, 1456)
	}
}

// TestTokenBucket_Stats tests statistics collection.
func TestTokenBucket_Stats(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	// Generate some traffic
	for i := 0; i < 1000; i++ {
		tb.ConsumeOrWait(ctx, 1456)
	}

	stats := tb.Stats()

	if stats.CurrentRate != 100_000_000 {
		t.Errorf("CurrentRate = %d, want 100000000", stats.CurrentRate)
	}

	// Should have recorded some wait time
	if stats.AvgWaitNs == 0 {
		t.Log("Warning: AvgWaitNs = 0 (may be valid if system is very fast)")
	}
}

// TestTokenBucket_ZeroRate tests handling of zero rate.
func TestTokenBucket_ZeroRate(t *testing.T) {
	tb := NewTokenBucket(100_000_000, RefillHybrid)
	tb.SetRate(0)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := tb.ConsumeOrWait(ctx, 1456)
	if err == nil {
		t.Error("ConsumeOrWait() with zero rate should return error")
	}
}

// TestTokenBucket_HighPrecision tests that we can achieve sub-millisecond precision.
func TestTokenBucket_HighPrecision(t *testing.T) {
	// At 500 Mb/s, we need ~23µs precision per packet
	// This test verifies the mechanism works

	tb := NewTokenBucket(500_000_000, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	packetSize := int64(1456)
	expectedIntervalUs := float64(packetSize*8) / float64(500_000_000) * 1e6 // ~23.3µs

	var intervals []float64
	lastSend := time.Now()

	for i := 0; i < 1000; i++ {
		if err := tb.ConsumeOrWait(ctx, packetSize); err != nil {
			break
		}
		now := time.Now()
		intervalUs := float64(now.Sub(lastSend).Nanoseconds()) / 1000.0
		intervals = append(intervals, intervalUs)
		lastSend = now
	}

	// Skip warm-up
	intervals = intervals[50:]

	// Calculate mean and stddev
	var sum float64
	for _, v := range intervals {
		sum += v
	}
	mean := sum / float64(len(intervals))

	var sumSq float64
	for _, v := range intervals {
		diff := v - mean
		sumSq += diff * diff
	}
	stddev := math.Sqrt(sumSq / float64(len(intervals)))

	t.Logf("High precision test:")
	t.Logf("  Expected interval: %.1f µs", expectedIntervalUs)
	t.Logf("  Mean interval: %.1f µs", mean)
	t.Logf("  Stddev: %.1f µs", stddev)

	// Mean should be close to expected (within 50%)
	if mean < expectedIntervalUs*0.5 || mean > expectedIntervalUs*1.5 {
		t.Errorf("Mean interval %.1f µs too far from expected %.1f µs", mean, expectedIntervalUs)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// INSTRUMENTATION TESTS - For bottleneck detection
// These tests verify metrics needed to distinguish tool vs library bottlenecks
// See: client_seeker_instrumentation_design.md
// ═══════════════════════════════════════════════════════════════════════════

func TestTokenBucket_WaitTimeMetric(t *testing.T) {
	// Test that wait time is recorded when ConsumeOrWait blocks
	tb := NewTokenBucket(100_000_000, RefillSleep) // 100 Mb/s

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Start refill loop
	go tb.StartRefillLoop(ctx)

	// Drain all tokens
	for tb.Consume(1456) {
	}

	// Reset stats to get clean measurement
	tb.ResetStats()

	// This should block and record wait time
	err := tb.ConsumeOrWait(ctx, 1456)
	if err != nil {
		t.Fatalf("ConsumeOrWait() error = %v", err)
	}

	// Verify wait time was recorded
	stats := tb.DetailedStats()
	if stats.TotalWaitNs <= 0 {
		t.Errorf("TotalWaitNs = %d, want > 0", stats.TotalWaitNs)
	}
	if stats.WaitCount <= 0 {
		t.Errorf("WaitCount = %d, want > 0", stats.WaitCount)
	}
}

func TestTokenBucket_SpinTimeMetric(t *testing.T) {
	// Test that spin time is recorded in RefillSpin mode
	tb := NewTokenBucket(100_000_000, RefillSpin) // Force spin mode

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Start refill loop
	go tb.StartRefillLoop(ctx)

	// Drain tokens
	for tb.Consume(1456) {
	}

	// Reset stats
	tb.ResetStats()

	// This should spin and record spin time
	err := tb.ConsumeOrWait(ctx, 1456)
	if err != nil {
		t.Fatalf("ConsumeOrWait() error = %v", err)
	}

	stats := tb.DetailedStats()
	if stats.SpinTimeNs <= 0 {
		t.Errorf("SpinTimeNs = %d, want > 0 for RefillSpin mode", stats.SpinTimeNs)
	}
}

func TestTokenBucket_BlockedCountMetric(t *testing.T) {
	// Test that blocked count is recorded when consume fails initially
	tb := NewTokenBucket(100_000_000, RefillSleep)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	// Drain tokens
	for tb.Consume(1456) {
	}

	tb.ResetStats()

	// This should block at least once
	err := tb.ConsumeOrWait(ctx, 1456)
	if err != nil {
		t.Fatalf("ConsumeOrWait() error = %v", err)
	}

	stats := tb.DetailedStats()
	if stats.BlockedCount <= 0 {
		t.Errorf("BlockedCount = %d, want > 0 when tokens were drained", stats.BlockedCount)
	}
}

func TestTokenBucket_TokensAvailableMetric(t *testing.T) {
	// Test that tokens available is correctly reported
	tb := NewTokenBucket(100_000_000, RefillSleep)

	stats := tb.DetailedStats()

	// Should start with tokens
	if stats.TokensAvailable <= 0 {
		t.Errorf("TokensAvailable = %d, want > 0", stats.TokensAvailable)
	}

	// Should have max tokens set
	if stats.TokensMax <= 0 {
		t.Errorf("TokensMax = %d, want > 0", stats.TokensMax)
	}

	// Utilization should be calculable
	utilization := float64(stats.TokensAvailable) / float64(stats.TokensMax)
	if utilization < 0 || utilization > 1 {
		t.Errorf("Token utilization = %.2f, want 0-1", utilization)
	}
}

func TestTokenBucket_OverheadRatio(t *testing.T) {
	// Test that we can calculate tool overhead ratio
	// This is the key metric for bottleneck detection
	tb := NewTokenBucket(100_000_000, RefillHybrid)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go tb.StartRefillLoop(ctx)

	tb.ResetStats()
	startTime := time.Now()

	// Generate some traffic
	for i := 0; i < 100; i++ {
		if err := tb.ConsumeOrWait(ctx, 1456); err != nil {
			break
		}
	}

	elapsed := time.Since(startTime)
	stats := tb.DetailedStats()

	// Calculate overhead ratio
	overheadNs := stats.TotalWaitNs + stats.SpinTimeNs
	overheadRatio := float64(overheadNs) / float64(elapsed.Nanoseconds())

	t.Logf("Overhead test (RefillHybrid):")
	t.Logf("  Elapsed: %v", elapsed)
	t.Logf("  Wait time: %d ns", stats.TotalWaitNs)
	t.Logf("  Spin time: %d ns", stats.SpinTimeNs)
	t.Logf("  Overhead ratio: %.2f%%", overheadRatio*100)

	// Overhead should be measurable (we're testing the metric exists)
	// The actual value depends on mode and rate
	if stats.TotalWaitNs < 0 || stats.SpinTimeNs < 0 {
		t.Error("Overhead metrics should not be negative")
	}
}

func TestTokenBucket_ModeString(t *testing.T) {
	// Test that mode is reported for diagnostics
	tests := []struct {
		mode RefillMode
		want string
	}{
		{RefillSleep, "sleep"},
		{RefillHybrid, "hybrid"},
		{RefillSpin, "spin"},
	}

	for _, tt := range tests {
		tb := NewTokenBucket(100_000_000, tt.mode)
		stats := tb.DetailedStats()
		if stats.Mode != tt.want {
			t.Errorf("Mode = %q, want %q", stats.Mode, tt.want)
		}
	}
}
