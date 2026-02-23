package main

import (
	"strings"
	"testing"
	"time"
)

func TestTimingModel_DefaultsValid(t *testing.T) {
	tm := DefaultTimingModel()

	if err := tm.ValidateContracts(); err != nil {
		t.Errorf("Default timing model should be valid: %v", err)
	}
}

func TestTimingModel_DerivedValues(t *testing.T) {
	tm := DefaultTimingModel()

	// MinProbeDuration = WarmUp + StabilityWindow
	expected := tm.WarmUpDuration + tm.StabilityWindow
	if tm.MinProbeDuration != expected {
		t.Errorf("MinProbeDuration = %v, want %v", tm.MinProbeDuration, expected)
	}

	// RequiredSamples = StabilityWindow / SampleInterval
	expectedSamples := int(tm.StabilityWindow / tm.SampleInterval)
	if tm.RequiredSamples != expectedSamples {
		t.Errorf("RequiredSamples = %d, want %d", tm.RequiredSamples, expectedSamples)
	}

	// RampSteps = RampDuration / RampUpdateInterval
	expectedSteps := int(tm.RampDuration / tm.RampUpdateInterval)
	if tm.RampSteps != expectedSteps {
		t.Errorf("RampSteps = %d, want %d", tm.RampSteps, expectedSteps)
	}
}

func TestTimingModel_InvalidWarmUp(t *testing.T) {
	tm := DefaultTimingModel()
	// Violate: WarmUp > 2 × RampUpdateInterval
	tm.WarmUpDuration = 100 * time.Millisecond // Too short
	tm.RampUpdateInterval = 100 * time.Millisecond

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected contract violation for short warm-up")
	}
	if !strings.Contains(err.Error(), "WARMUP_TOO_SHORT") {
		t.Errorf("Expected WARMUP_TOO_SHORT violation, got: %v", err)
	}
}

func TestTimingModel_InvalidStabilityWindow(t *testing.T) {
	tm := DefaultTimingModel()
	// Violate: StabilityWindow > 3 × SampleInterval
	tm.StabilityWindow = 1 * time.Second
	tm.SampleInterval = 500 * time.Millisecond // Only 2 samples

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected contract violation for short stability window")
	}
	if !strings.Contains(err.Error(), "STABILITY_TOO_SHORT") {
		t.Errorf("Expected STABILITY_TOO_SHORT violation, got: %v", err)
	}
}

func TestTimingModel_InvalidHeartbeat(t *testing.T) {
	tm := DefaultTimingModel()
	// Violate: HeartbeatInterval < WatchdogTimeout/2
	tm.HeartbeatInterval = 3 * time.Second
	tm.WatchdogTimeout = 5 * time.Second // Heartbeat >= Watchdog/2

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected contract violation for slow heartbeat")
	}
	if !strings.Contains(err.Error(), "HEARTBEAT_TOO_SLOW") {
		t.Errorf("Expected HEARTBEAT_TOO_SLOW violation, got: %v", err)
	}
}

func TestTimingModel_InvalidFastPoll(t *testing.T) {
	tm := DefaultTimingModel()
	// Violate: FastPollInterval < SampleInterval
	tm.FastPollInterval = 600 * time.Millisecond
	tm.SampleInterval = 500 * time.Millisecond

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected contract violation for slow fast poll")
	}
	if !strings.Contains(err.Error(), "FAST_POLL_TOO_SLOW") {
		t.Errorf("Expected FAST_POLL_TOO_SLOW violation, got: %v", err)
	}
}

func TestTimingModel_InvalidPrecision(t *testing.T) {
	tm := DefaultTimingModel()
	tm.Precision = 0

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected contract violation for zero precision")
	}
	if !strings.Contains(err.Error(), "INVALID_PRECISION") {
		t.Errorf("Expected INVALID_PRECISION violation, got: %v", err)
	}
}

func TestTimingModel_InvalidTimeout(t *testing.T) {
	tm := DefaultTimingModel()
	// Violate: SearchTimeout > MinProbeDuration
	tm.SearchTimeout = 5 * time.Second
	tm.WarmUpDuration = 3 * time.Second
	tm.StabilityWindow = 3 * time.Second
	tm.computeDerived() // MinProbeDuration = 6s > SearchTimeout

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected contract violation for short timeout")
	}
	if !strings.Contains(err.Error(), "TIMEOUT_TOO_SHORT") {
		t.Errorf("Expected TIMEOUT_TOO_SHORT violation, got: %v", err)
	}
}

func TestTimingModel_MultipleViolations(t *testing.T) {
	tm := TimingModel{
		HeartbeatInterval:  3 * time.Second,
		WatchdogTimeout:    5 * time.Second, // Violation 1
		RampDuration:       2 * time.Second,
		RampUpdateInterval: 100 * time.Millisecond,
		SampleInterval:     500 * time.Millisecond,
		FastPollInterval:   600 * time.Millisecond, // Violation 2
		WarmUpDuration:     100 * time.Millisecond, // Violation 3
		StabilityWindow:    5 * time.Second,
		Precision:          5_000_000,
		SearchTimeout:      10 * time.Minute,
	}
	tm.computeDerived()

	err := tm.ValidateContracts()
	if err == nil {
		t.Error("Expected multiple contract violations")
	}

	// Should report all violations
	errStr := err.Error()
	violations := 0
	if strings.Contains(errStr, "HEARTBEAT_TOO_SLOW") {
		violations++
	}
	if strings.Contains(errStr, "FAST_POLL_TOO_SLOW") {
		violations++
	}
	if strings.Contains(errStr, "WARMUP_TOO_SHORT") {
		violations++
	}

	if violations < 3 {
		t.Errorf("Expected at least 3 violations, got %d: %v", violations, err)
	}
}

func TestTimingModel_Clone(t *testing.T) {
	tm := DefaultTimingModel()
	clone := tm.Clone()

	// Modify clone
	clone.WarmUpDuration = 10 * time.Second

	// Original should be unchanged
	if tm.WarmUpDuration == clone.WarmUpDuration {
		t.Error("Clone should be independent of original")
	}
}

func TestTimingModel_WithWarmUp(t *testing.T) {
	tm := DefaultTimingModel()
	originalWarmUp := tm.WarmUpDuration

	modified := tm.WithWarmUp(10 * time.Second)

	// Original unchanged
	if tm.WarmUpDuration != originalWarmUp {
		t.Error("Original should be unchanged")
	}

	// Modified has new value
	if modified.WarmUpDuration != 10*time.Second {
		t.Errorf("Modified WarmUpDuration = %v, want 10s", modified.WarmUpDuration)
	}

	// Derived values updated
	expectedMinProbe := 10*time.Second + modified.StabilityWindow
	if modified.MinProbeDuration != expectedMinProbe {
		t.Errorf("MinProbeDuration not updated: got %v, want %v", modified.MinProbeDuration, expectedMinProbe)
	}
}

func TestTimingModel_String(t *testing.T) {
	tm := DefaultTimingModel()
	s := tm.String()

	// Should contain key information
	if !strings.Contains(s, "Heartbeat") {
		t.Error("String should contain Heartbeat")
	}
	if !strings.Contains(s, "WarmUp") {
		t.Error("String should contain WarmUp")
	}
	if !strings.Contains(s, "Precision") {
		t.Error("String should contain Precision")
	}
}
