package main

import (
	"context"
	"testing"
	"time"
)

func TestDataGenerator_Basic(t *testing.T) {
	bucket := NewTokenBucket(100_000_000, RefillSleep) // 100 Mb/s
	gen := NewDataGenerator(bucket, 1456)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go bucket.StartRefillLoop(ctx)

	// Generate a packet
	data, err := gen.Generate(ctx)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if len(data) != 1456 {
		t.Errorf("len(data) = %d, want 1456", len(data))
	}

	packets, bytes := gen.Stats()
	if packets != 1 {
		t.Errorf("packets = %d, want 1", packets)
	}
	if bytes != 1456 {
		t.Errorf("bytes = %d, want 1456", bytes)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// INSTRUMENTATION TESTS - For bottleneck detection
// See: client_seeker_instrumentation_design.md
// ═══════════════════════════════════════════════════════════════════════════

func TestDataGenerator_EfficiencyMetric(t *testing.T) {
	// Test that efficiency (actual/target) is correctly calculated
	bucket := NewTokenBucket(100_000_000, RefillSleep) // 100 Mb/s
	gen := NewDataGenerator(bucket, 1456)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go bucket.StartRefillLoop(ctx)

	// Generate packets for a bit
	for i := 0; i < 100; i++ {
		if _, err := gen.Generate(ctx); err != nil {
			break
		}
	}

	stats := gen.DetailedStats()

	// Efficiency should be calculable
	if stats.TargetBps <= 0 {
		t.Errorf("TargetBps = %d, want > 0", stats.TargetBps)
	}

	// Efficiency should be between 0 and some reasonable upper bound
	// (can be > 1 if we're faster than target due to burst)
	if stats.Efficiency < 0 {
		t.Errorf("Efficiency = %.2f, want >= 0", stats.Efficiency)
	}

	t.Logf("Generator stats:")
	t.Logf("  Target: %d bps", stats.TargetBps)
	t.Logf("  Actual: %.0f bps", stats.ActualBps)
	t.Logf("  Efficiency: %.2f%%", stats.Efficiency*100)
}

func TestDataGenerator_ActualBpsMetric(t *testing.T) {
	// Test that actual bitrate is measured correctly
	bucket := NewTokenBucket(80_000_000, RefillSleep) // 80 Mb/s = 10 MB/s
	gen := NewDataGenerator(bucket, 1456)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go bucket.StartRefillLoop(ctx)

	// Drain initial tokens to get steady-state behavior
	for bucket.Consume(1456) {
	}

	// Reset stats after draining
	gen.Reset()

	// Generate packets for a meaningful duration
	for i := 0; i < 200; i++ {
		if _, err := gen.Generate(ctx); err != nil {
			break
		}
	}

	stats := gen.DetailedStats()

	// Actual bitrate should be positive
	if stats.ActualBps <= 0 {
		t.Errorf("ActualBps = %.0f, want > 0", stats.ActualBps)
	}

	t.Logf("Actual bitrate test:")
	t.Logf("  Target: %d bps", stats.TargetBps)
	t.Logf("  Actual: %.0f bps", stats.ActualBps)
	t.Logf("  Efficiency: %.2f%%", stats.Efficiency*100)

	// After draining, efficiency should be reasonable (0.5 - 1.5)
	// Note: RefillSleep mode has lower precision so we allow wider range
	if stats.Efficiency < 0.3 || stats.Efficiency > 2.0 {
		t.Errorf("Efficiency = %.2f, want 0.3-2.0 range", stats.Efficiency)
	}
}

func TestDataGenerator_PacketCountMetric(t *testing.T) {
	bucket := NewTokenBucket(100_000_000, RefillSleep)
	gen := NewDataGenerator(bucket, 1456)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go bucket.StartRefillLoop(ctx)

	// Generate exactly 10 packets
	for i := 0; i < 10; i++ {
		if _, err := gen.Generate(ctx); err != nil {
			t.Fatalf("Generate() error = %v at packet %d", err, i)
		}
	}

	stats := gen.DetailedStats()
	if stats.PacketsSent != 10 {
		t.Errorf("PacketsSent = %d, want 10", stats.PacketsSent)
	}
	if stats.BytesSent != 10*1456 {
		t.Errorf("BytesSent = %d, want %d", stats.BytesSent, 10*1456)
	}
}

func TestDataGenerator_ElapsedTimeMetric(t *testing.T) {
	bucket := NewTokenBucket(100_000_000, RefillSleep)
	gen := NewDataGenerator(bucket, 1456)

	// Check elapsed time is tracked
	time.Sleep(50 * time.Millisecond)

	stats := gen.DetailedStats()
	if stats.ElapsedMs < 50 {
		t.Errorf("ElapsedMs = %d, want >= 50", stats.ElapsedMs)
	}
}

func TestDataGenerator_Reset(t *testing.T) {
	bucket := NewTokenBucket(100_000_000, RefillSleep)
	gen := NewDataGenerator(bucket, 1456)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go bucket.StartRefillLoop(ctx)

	// Generate some packets
	for i := 0; i < 5; i++ {
		if _, err := gen.Generate(ctx); err != nil {
			// Context may cancel during generation, which is expected
			break
		}
	}

	// Reset
	gen.Reset()

	stats := gen.DetailedStats()
	if stats.PacketsSent != 0 {
		t.Errorf("PacketsSent after reset = %d, want 0", stats.PacketsSent)
	}
	if stats.BytesSent != 0 {
		t.Errorf("BytesSent after reset = %d, want 0", stats.BytesSent)
	}
}
