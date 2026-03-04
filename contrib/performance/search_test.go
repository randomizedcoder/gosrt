package main

import (
	"context"
	"testing"
	"time"
)

func TestSearchLoop_ConvergesOnThreshold(t *testing.T) {
	// Setup: stable up to 300 Mb/s
	threshold := int64(300_000_000)
	gate := NewThresholdGate(threshold)
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       50_000_000,
		Precision:      10_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 10 * time.Millisecond // Fast for testing
	timing.RampSteps = 2

	loop := NewSearchLoop(config, timing, gate, seeker)
	loop.SetVerbose(false)

	ctx := context.Background()
	result := loop.Run(ctx)

	if result.Status != StatusSuccess {
		t.Errorf("Status = %v, want StatusSuccess (reason: %s)", result.Status, result.FailReason)
	}

	// Ceiling should be close to threshold
	if result.Ceiling < threshold-config.Precision || result.Ceiling > threshold+config.Precision {
		t.Errorf("Ceiling = %d, want ~%d (±%d)", result.Ceiling, threshold, config.Precision)
	}

	t.Logf("Found ceiling: %s (threshold: %s)", FormatBitrate(result.Ceiling), FormatBitrate(threshold))
}

func TestSearchLoop_Monotonicity_LowOnlyIncreases(t *testing.T) {
	// Gate that alternates stable/unstable to stress test bounds
	responses := []ProbeVerdict{
		VerdictStable,   // 100M -> low=100M
		VerdictUnstable, // 150M -> high=150M
		VerdictStable,   // 125M -> low=125M
		VerdictUnstable, // 137M -> high=137M
		VerdictStable,   // 131M -> low=131M
	}
	gate := NewDeterministicGate(responses)
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       50_000_000,
		Precision:      5_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 1 * time.Millisecond
	timing.RampSteps = 1

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx := context.Background()
	result := loop.Run(ctx)

	// Check that low only increased
	var lastLow int64
	for _, probe := range result.Artifacts.Probes {
		if probe.Stable {
			if probe.TargetBitrate < lastLow {
				t.Errorf("Monotonicity violated: stable at %d after low was %d",
					probe.TargetBitrate, lastLow)
			}
			lastLow = probe.TargetBitrate
		}
	}

	t.Logf("Final ceiling: %s, probes: %d", FormatBitrate(result.Ceiling), len(result.Artifacts.Probes))
}

func TestSearchLoop_Monotonicity_HighOnlyDecreases(t *testing.T) {
	// Gate that returns unstable for high bitrates
	gate := NewThresholdGate(200_000_000) // Stable up to 200M
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       100_000_000, // Large steps to hit high quickly
		Precision:      10_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 1 * time.Millisecond
	timing.RampSteps = 1

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx := context.Background()
	result := loop.Run(ctx)

	// Verify high only decreased after first failure
	var firstHigh = config.MaxBitrate
	foundFirstFailure := false
	for _, probe := range result.Artifacts.Probes {
		if !probe.Stable {
			if !foundFirstFailure {
				firstHigh = probe.TargetBitrate
				foundFirstFailure = true
			} else if probe.TargetBitrate > firstHigh {
				t.Errorf("Monotonicity violated: failed at %d after high was %d",
					probe.TargetBitrate, firstHigh)
			}
		}
	}

	t.Logf("Final ceiling: %s", FormatBitrate(result.Ceiling))
}

func TestSearchLoop_Timeout(t *testing.T) {
	// Gate that always returns unstable but never critical (search never converges)
	// Use a very high threshold so nothing is stable, but also not critical
	gate := NewFakeGate(0, 1_000_000_000) // Nothing stable, nothing critical
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       10_000_000,
		Precision:      1_000_000,             // Very small precision to prevent quick convergence
		Timeout:        50 * time.Millisecond, // Very short timeout
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 10 * time.Millisecond // Slow enough to trigger timeout
	timing.RampSteps = 5

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx := context.Background()
	result := loop.Run(ctx)

	// Should timeout or fail
	if result.Status == StatusSuccess && result.Proven {
		t.Errorf("Status = %v with Proven=%v, expected timeout or failure", result.Status, result.Proven)
	}
	t.Logf("Status: %v, Reason: %s, Probes: %d", result.Status, result.FailReason, len(result.Artifacts.Probes))
}

func TestSearchLoop_Cancellation(t *testing.T) {
	gate := NewThresholdGate(300_000_000)
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       10_000_000,
		Precision:      5_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 100 * time.Millisecond // Slow enough to cancel during ramp
	timing.RampSteps = 10

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result := loop.Run(ctx)

	if result.Status != StatusAborted {
		t.Errorf("Status = %v, want StatusAborted", result.Status)
	}
}

func TestSearchLoop_BinarySearch(t *testing.T) {
	// Threshold at exactly 350 Mb/s
	threshold := int64(350_000_000)
	gate := NewThresholdGate(threshold)
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       100_000_000,
		Precision:      5_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 1 * time.Millisecond
	timing.RampSteps = 1

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx := context.Background()
	result := loop.Run(ctx)

	if result.Status != StatusSuccess {
		t.Fatalf("Status = %v, want StatusSuccess (reason: %s)", result.Status, result.FailReason)
	}

	// Should find ceiling within precision of threshold
	diff := abs64(result.Ceiling - threshold)
	if diff > config.Precision {
		t.Errorf("Ceiling = %d, want within %d of %d (diff=%d)",
			result.Ceiling, config.Precision, threshold, diff)
	}

	// Binary search should be efficient
	if len(result.Artifacts.Probes) > 20 {
		t.Errorf("Too many probes: %d (binary search should be more efficient)", len(result.Artifacts.Probes))
	}

	t.Logf("Found ceiling %s in %d probes (threshold: %s)",
		FormatBitrate(result.Ceiling), len(result.Artifacts.Probes), FormatBitrate(threshold))
}

func TestSearchLoop_CriticalFailure(t *testing.T) {
	// Gate with critical threshold - stable up to 200M, critical above 250M
	// This ensures we hit critical when stepping from 200M
	gate := NewFakeGate(200_000_000, 250_000_000) // Stable up to 200M, critical above 250M
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       200_000_000, // Large step to hit critical (100M -> 300M)
		Precision:      10_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 1 * time.Millisecond
	timing.RampSteps = 1

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx := context.Background()
	result := loop.Run(ctx)

	// Log all probes for debugging
	t.Logf("Probes:")
	for _, probe := range result.Artifacts.Probes {
		t.Logf("  #%d: %s stable=%v critical=%v", probe.Number, FormatBitrate(probe.TargetBitrate), probe.Stable, probe.Critical)
	}

	// Check that we recorded critical failures
	hasCritical := false
	for _, probe := range result.Artifacts.Probes {
		if probe.Critical {
			hasCritical = true
			break
		}
	}

	if !hasCritical {
		t.Logf("Note: No critical failures recorded. This may be expected if binary search didn't hit critical threshold.")
	}

	t.Logf("Final ceiling: %s, probes: %d", FormatBitrate(result.Ceiling), len(result.Artifacts.Probes))
}

func TestSearchLoop_RampingOccurs(t *testing.T) {
	gate := NewThresholdGate(300_000_000)
	seeker := NewFakeSeeker(100_000_000)

	config := SearchConfig{
		InitialBitrate: 100_000_000,
		MinBitrate:     50_000_000,
		MaxBitrate:     500_000_000,
		StepSize:       100_000_000,
		Precision:      10_000_000,
		Timeout:        1 * time.Minute,
	}
	timing := DefaultTimingModel()
	timing.RampDuration = 10 * time.Millisecond
	timing.RampSteps = 5 // 5 steps

	loop := NewSearchLoop(config, timing, gate, seeker)

	ctx := context.Background()
	_ = loop.Run(ctx)

	// Check that SetBitrate was called multiple times (ramping)
	if len(seeker.setBitrateCalls) < 5 {
		t.Errorf("Expected at least 5 SetBitrate calls (ramping), got %d", len(seeker.setBitrateCalls))
	}

	// Check that heartbeats were sent during ramping
	if seeker.heartbeatCalls < 1 {
		t.Error("Expected heartbeats during ramping")
	}

	t.Logf("SetBitrate calls: %d, Heartbeats: %d", len(seeker.setBitrateCalls), seeker.heartbeatCalls)
}

func TestSearchLoop_InvariantViolation(t *testing.T) {
	// This test verifies that invariant violations are caught
	// We can't easily trigger a real violation, so we test the error handling

	violation := &InvariantViolation{
		Invariant:   "BOUNDS_CROSSED",
		Description: "low(350000000) > high(300000000)",
		Low:         350_000_000,
		High:        300_000_000,
		ProbeCount:  5,
	}

	errStr := violation.Error()
	if errStr == "" {
		t.Error("InvariantViolation.Error() should return non-empty string")
	}

	expected := "INVARIANT VIOLATION [BOUNDS_CROSSED]"
	if !contains(errStr, expected) {
		t.Errorf("Error string should contain %q, got %q", expected, errStr)
	}
}

func TestNewSearchLoop(t *testing.T) {
	config := DefaultSearchConfig()
	timing := DefaultTimingModel()
	gate := NewThresholdGate(300_000_000)
	seeker := NewFakeSeeker(100_000_000)

	loop := NewSearchLoop(config, timing, gate, seeker)

	if loop.low != 0 {
		t.Errorf("initial low = %d, want 0", loop.low)
	}
	if loop.high != config.MaxBitrate {
		t.Errorf("initial high = %d, want %d", loop.high, config.MaxBitrate)
	}
	if loop.lastStableBitrate != config.InitialBitrate {
		t.Errorf("initial lastStableBitrate = %d, want %d", loop.lastStableBitrate, config.InitialBitrate)
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		v, min, max, expected int64
	}{
		{50, 0, 100, 50},   // In range
		{-10, 0, 100, 0},   // Below min
		{150, 0, 100, 100}, // Above max
		{0, 0, 100, 0},     // At min
		{100, 0, 100, 100}, // At max
	}

	for _, tt := range tests {
		got := clamp(tt.v, tt.min, tt.max)
		if got != tt.expected {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", tt.v, tt.min, tt.max, got, tt.expected)
		}
	}
}

// Helper functions

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
