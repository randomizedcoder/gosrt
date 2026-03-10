package main

import (
	"context"
	"sync/atomic"
	"time"
)

// DataGenerator generates test data at a rate controlled by the TokenBucket.
// It produces packets that can be sent over SRT.
//
// Unlike the common.DataGenerator which uses its own rate limiting,
// this generator uses the BitrateManager's TokenBucket for rate control,
// allowing dynamic bitrate changes from the Orchestrator.
type DataGenerator struct {
	bucket     *TokenBucket
	packetSize int
	payload    []byte

	// Stats (atomic for thread safety)
	packetsSent atomic.Uint64
	bytesSent   atomic.Uint64
	startTime   time.Time

	// Sliding window for instantaneous rate calculation
	// This provides accurate efficiency measurement even after bitrate changes
	windowBytes    atomic.Uint64 // Bytes sent in current window
	windowStartNs  atomic.Int64  // Window start time (UnixNano)
	windowDuration time.Duration // Window size (default: 1 second)
}

// DataGeneratorDetailedStats holds detailed statistics for bottleneck detection.
// See: client_seeker_instrumentation_design.md
type DataGeneratorDetailedStats struct {
	// Counts
	PacketsSent uint64 // Total packets generated
	BytesSent   uint64 // Total bytes generated

	// Rates
	TargetBps int64   // Target bitrate from TokenBucket
	ActualBps float64 // Measured actual bitrate

	// Efficiency - key metric for bottleneck detection
	// < 0.95 indicates something is limiting throughput
	Efficiency float64 // ActualBps / TargetBps

	// Timing
	ElapsedMs int64 // Milliseconds since start/reset
}

// NewDataGenerator creates a data generator that uses the provided TokenBucket.
//
// Parameters:
//   - bucket: TokenBucket for rate limiting (from BitrateManager)
//   - packetSize: Size of each packet (default: 1456 for SRT)
func NewDataGenerator(bucket *TokenBucket, packetSize int) *DataGenerator {
	if packetSize <= 0 {
		packetSize = 1456 // SRT max payload size
	}

	// Pre-fill payload with test pattern (never changes)
	payload := make([]byte, packetSize)
	pattern := []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ!@#$%^&*()")
	for i := range payload {
		payload[i] = pattern[i%len(pattern)]
	}

	gen := &DataGenerator{
		bucket:         bucket,
		packetSize:     packetSize,
		payload:        payload,
		startTime:      time.Now(),
		windowDuration: 1 * time.Second, // 1 second sliding window
	}
	gen.windowStartNs.Store(time.Now().UnixNano())
	return gen
}

// Generate produces a single packet, waiting for rate limiting.
// Returns the packet data or error if context is canceled.
//
// This is the main generation loop entry point - call this repeatedly
// to generate packets at the configured rate.
func (g *DataGenerator) Generate(ctx context.Context) ([]byte, error) {
	// Wait for tokens (rate limiting)
	if err := g.bucket.ConsumeOrWait(ctx, int64(g.packetSize)); err != nil {
		return nil, err
	}

	// Update stats
	g.packetsSent.Add(1)
	g.bytesSent.Add(uint64(g.packetSize))

	// Update sliding window stats
	g.windowBytes.Add(uint64(g.packetSize))

	// Return pre-filled payload (no allocation in hot path)
	return g.payload, nil
}

// Stats returns current generation statistics.
func (g *DataGenerator) Stats() (packets, bytes uint64) {
	return g.packetsSent.Load(), g.bytesSent.Load()
}

// PacketSize returns the configured packet size.
func (g *DataGenerator) PacketSize() int {
	return g.packetSize
}

// ActualBitrate returns the measured bitrate using the sliding window.
// This provides an accurate instantaneous rate even after bitrate changes.
func (g *DataGenerator) ActualBitrate() float64 {
	return g.instantaneousBitrate()
}

// instantaneousBitrate calculates the bitrate over the sliding window.
// If the window hasn't been active long enough, falls back to since-start calculation.
func (g *DataGenerator) instantaneousBitrate() float64 {
	now := time.Now().UnixNano()
	windowStart := g.windowStartNs.Load()
	windowBytes := g.windowBytes.Load()
	windowDur := time.Duration(now - windowStart)

	// If window is too short, use full history
	if windowDur < 100*time.Millisecond {
		bytes := g.bytesSent.Load()
		duration := time.Since(g.startTime)
		if duration.Seconds() > 0 {
			return float64(bytes*8) / duration.Seconds()
		}
		return 0
	}

	// Check if we need to roll the window
	if windowDur >= g.windowDuration {
		// Roll the window - reset counter and timestamp
		g.windowBytes.Store(0)
		g.windowStartNs.Store(now)
		// Return rate from the just-completed window
		return float64(windowBytes*8) / windowDur.Seconds()
	}

	// Return rate from current partial window
	return float64(windowBytes*8) / windowDur.Seconds()
}

// Reset clears the statistics and restarts the timer.
func (g *DataGenerator) Reset() {
	g.packetsSent.Store(0)
	g.bytesSent.Store(0)
	g.startTime = time.Now()
	// Also reset sliding window
	g.windowBytes.Store(0)
	g.windowStartNs.Store(time.Now().UnixNano())
}

// DetailedStats returns detailed statistics for bottleneck detection.
// Uses sliding window for instantaneous rate calculation.
// See: client_seeker_instrumentation_design.md
func (g *DataGenerator) DetailedStats() DataGeneratorDetailedStats {
	packets := g.packetsSent.Load()
	bytes := g.bytesSent.Load()
	elapsed := time.Since(g.startTime)
	elapsedMs := elapsed.Milliseconds()

	// Get target rate from bucket
	targetBps := g.bucket.Rate()

	// Calculate actual bitrate using sliding window (instantaneous rate)
	// This provides accurate efficiency even after bitrate changes
	actualBps := g.instantaneousBitrate()

	// Calculate efficiency
	var efficiency float64
	if targetBps > 0 {
		efficiency = actualBps / float64(targetBps)
	}

	return DataGeneratorDetailedStats{
		PacketsSent: packets,
		BytesSent:   bytes,
		TargetBps:   targetBps,
		ActualBps:   actualBps,
		Efficiency:  efficiency,
		ElapsedMs:   elapsedMs,
	}
}

// Efficiency returns the current efficiency ratio (actual/target).
// This is the key metric for bottleneck detection.
// < 0.95 indicates something is limiting throughput.
func (g *DataGenerator) Efficiency() float64 {
	stats := g.DetailedStats()
	return stats.Efficiency
}
