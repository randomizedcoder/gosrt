package main

import (
	"testing"
	"time"
)

func TestStabilityGate_IsCritical(t *testing.T) {
	config := DefaultStabilityConfig()
	gate := &StabilityGate{config: config}

	tests := []struct {
		name     string
		metrics  StabilityMetrics
		expected bool
	}{
		{
			name: "all_normal",
			metrics: StabilityMetrics{
				GapRate:         0.001,
				NAKRate:         0.001,
				ConnectionAlive: true,
			},
			expected: false,
		},
		{
			name: "critical_gap_rate",
			metrics: StabilityMetrics{
				GapRate:         0.10, // > CriticalGapRate (0.05)
				NAKRate:         0.001,
				ConnectionAlive: true,
			},
			expected: true,
		},
		{
			name: "critical_nak_rate",
			metrics: StabilityMetrics{
				GapRate:         0.001,
				NAKRate:         0.15, // > CriticalNAKRate (0.10)
				ConnectionAlive: true,
			},
			expected: true,
		},
		{
			name: "connection_dead",
			metrics: StabilityMetrics{
				GapRate:         0.001,
				NAKRate:         0.001,
				ConnectionAlive: false,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gate.isCritical(tt.metrics)
			if got != tt.expected {
				t.Errorf("isCritical() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestStabilityGate_EvaluateSamples_Stable(t *testing.T) {
	config := DefaultStabilityConfig()
	gate := &StabilityGate{config: config}

	samples := []StabilityMetrics{
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.98},
		{GapRate: 0.002, NAKRate: 0.002, RTTMs: 12, ThroughputTE: 0.97},
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 11, ThroughputTE: 0.98},
		{GapRate: 0.002, NAKRate: 0.002, RTTMs: 10, ThroughputTE: 0.99},
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 11, ThroughputTE: 0.97},
	}

	verdict, reason := gate.evaluateSamples(samples)

	if verdict != VerdictStable {
		t.Errorf("verdict = %v, want %v (reason: %s)", verdict, VerdictStable, reason)
	}
}

func TestStabilityGate_EvaluateSamples_Unstable_HighGap(t *testing.T) {
	config := DefaultStabilityConfig()
	gate := &StabilityGate{config: config}

	samples := []StabilityMetrics{
		{GapRate: 0.02, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.98},
		{GapRate: 0.03, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.98},
		{GapRate: 0.02, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.98},
	}

	verdict, reason := gate.evaluateSamples(samples)

	if verdict != VerdictUnstable {
		t.Errorf("verdict = %v, want %v (reason: %s)", verdict, VerdictUnstable, reason)
	}
}

func TestStabilityGate_EvaluateSamples_Unstable_LowThroughput(t *testing.T) {
	config := DefaultStabilityConfig()
	gate := &StabilityGate{config: config}

	samples := []StabilityMetrics{
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.90},
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.88},
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.91},
	}

	verdict, reason := gate.evaluateSamples(samples)

	if verdict != VerdictUnstable {
		t.Errorf("verdict = %v, want %v (reason: %s)", verdict, VerdictUnstable, reason)
	}
}

func TestStabilityGate_EvaluateSamples_NoSamples(t *testing.T) {
	config := DefaultStabilityConfig()
	gate := &StabilityGate{config: config}

	verdict, _ := gate.evaluateSamples(nil)

	if verdict != VerdictUnstable {
		t.Errorf("verdict = %v, want %v for empty samples", verdict, VerdictUnstable)
	}
}

func TestStabilityGate_Aggregate(t *testing.T) {
	gate := &StabilityGate{}

	samples := []StabilityMetrics{
		{GapRate: 0.01, NAKRate: 0.02, RTTMs: 10, ThroughputTE: 0.95, TargetBitrate: 100000000},
		{GapRate: 0.02, NAKRate: 0.03, RTTMs: 12, ThroughputTE: 0.96, TargetBitrate: 100000000},
		{GapRate: 0.01, NAKRate: 0.02, RTTMs: 11, ThroughputTE: 0.97, TargetBitrate: 100000000},
	}

	m := gate.aggregate(samples)

	// Check averages
	expectedGap := (0.01 + 0.02 + 0.01) / 3
	if m.GapRate != expectedGap {
		t.Errorf("GapRate = %f, want %f", m.GapRate, expectedGap)
	}

	expectedNAK := (0.02 + 0.03 + 0.02) / 3
	if m.NAKRate != expectedNAK {
		t.Errorf("NAKRate = %f, want %f", m.NAKRate, expectedNAK)
	}

	expectedRTT := (10.0 + 12.0 + 11.0) / 3
	if m.RTTMs != expectedRTT {
		t.Errorf("RTTMs = %f, want %f", m.RTTMs, expectedRTT)
	}

	// TargetBitrate should come from last sample
	if m.TargetBitrate != 100000000 {
		t.Errorf("TargetBitrate = %d, want 100000000", m.TargetBitrate)
	}
}

func TestStabilityGate_Aggregate_Empty(t *testing.T) {
	gate := &StabilityGate{}

	m := gate.aggregate(nil)

	if m.GapRate != 0 || m.NAKRate != 0 || m.RTTMs != 0 {
		t.Error("empty aggregate should return zero values")
	}
}

func TestProbeVerdict_String(t *testing.T) {
	tests := []struct {
		verdict  ProbeVerdict
		expected string
	}{
		{VerdictStable, "stable"},
		{VerdictUnstable, "unstable"},
		{VerdictCritical, "critical"},
		{VerdictEOF, "eof"},
		{VerdictTimeout, "timeout"},
	}

	for _, tt := range tests {
		if string(tt.verdict) != tt.expected {
			t.Errorf("verdict %v != %s", tt.verdict, tt.expected)
		}
	}
}

func TestNewStabilityGate(t *testing.T) {
	config := DefaultStabilityConfig()
	timing := DefaultTimingModel()

	gate := NewStabilityGate(config, timing, nil, nil, nil)

	if gate.fastPollInterval != timing.FastPollInterval {
		t.Errorf("fastPollInterval = %v, want %v", gate.fastPollInterval, timing.FastPollInterval)
	}
	if gate.slowPollInterval != timing.SampleInterval {
		t.Errorf("slowPollInterval = %v, want %v", gate.slowPollInterval, timing.SampleInterval)
	}
}

func TestStabilityGate_WithConfig(t *testing.T) {
	config := DefaultStabilityConfig()
	timing := DefaultTimingModel()

	gate := NewStabilityGate(config, timing, nil, nil, nil)

	// Create modified config
	newConfig := config
	newConfig.MaxGapRate = 0.05

	newGate := gate.WithConfig(newConfig)

	// Original should be unchanged
	if gate.config.MaxGapRate == 0.05 {
		t.Error("original gate should not be modified")
	}

	// New gate should have new config
	if sg, ok := newGate.(*StabilityGate); ok {
		if sg.config.MaxGapRate != 0.05 {
			t.Errorf("new gate MaxGapRate = %f, want 0.05", sg.config.MaxGapRate)
		}
	}
}

func TestStabilityGate_EvaluateSamples_MajorityUnstable(t *testing.T) {
	config := DefaultStabilityConfig()
	gate := &StabilityGate{config: config}

	// 4 out of 5 samples are unstable (80% > 30% threshold)
	samples := []StabilityMetrics{
		{GapRate: 0.05, NAKRate: 0.05, RTTMs: 150, ThroughputTE: 0.80},  // unstable
		{GapRate: 0.05, NAKRate: 0.05, RTTMs: 150, ThroughputTE: 0.80},  // unstable
		{GapRate: 0.001, NAKRate: 0.001, RTTMs: 10, ThroughputTE: 0.98}, // stable
		{GapRate: 0.05, NAKRate: 0.05, RTTMs: 150, ThroughputTE: 0.80},  // unstable
		{GapRate: 0.05, NAKRate: 0.05, RTTMs: 150, ThroughputTE: 0.80},  // unstable
	}

	verdict, reason := gate.evaluateSamples(samples)

	if verdict != VerdictUnstable {
		t.Errorf("verdict = %v, want %v (reason: %s)", verdict, VerdictUnstable, reason)
	}
}

func TestDiagnosticCapture_GetProfilePaths(t *testing.T) {
	capture := &DiagnosticCapture{
		CPUProfilePath:       "/tmp/cpu.pprof",
		HeapProfilePath:      "/tmp/heap.pprof",
		GoroutineProfilePath: "/tmp/goroutine.pprof",
	}

	paths := capture.GetProfilePaths()

	if len(paths) != 3 {
		t.Errorf("expected 3 paths, got %d", len(paths))
	}
}

func TestDiagnosticCapture_GetProfilePaths_Empty(t *testing.T) {
	capture := &DiagnosticCapture{}

	paths := capture.GetProfilePaths()

	if len(paths) != 0 {
		t.Errorf("expected 0 paths for empty capture, got %d", len(paths))
	}
}

func TestDefaultHypothesisModel(t *testing.T) {
	h := DefaultHypothesisModel()

	if h.H1NAKRateThreshold != 0.02 {
		t.Errorf("H1NAKRateThreshold = %f, want 0.02", h.H1NAKRateThreshold)
	}
	if h.H2TEThreshold != 0.95 {
		t.Errorf("H2TEThreshold = %f, want 0.95", h.H2TEThreshold)
	}
	if h.H3GapRateThreshold != 0.01 {
		t.Errorf("H3GapRateThreshold = %f, want 0.01", h.H3GapRateThreshold)
	}
	if h.H5RTTVarianceThreshold != 20.0 {
		t.Errorf("H5RTTVarianceThreshold = %f, want 20.0", h.H5RTTVarianceThreshold)
	}
}

func TestProbeResult_Fields(t *testing.T) {
	result := ProbeResult{
		Verdict:  VerdictStable,
		Samples:  10,
		Duration: 5 * time.Second,
		Reason:   "test reason",
	}

	if result.Verdict != VerdictStable {
		t.Errorf("Verdict = %v, want %v", result.Verdict, VerdictStable)
	}
	if result.Samples != 10 {
		t.Errorf("Samples = %d, want 10", result.Samples)
	}
	if result.Duration != 5*time.Second {
		t.Errorf("Duration = %v, want 5s", result.Duration)
	}
	if result.Reason != "test reason" {
		t.Errorf("Reason = %q, want %q", result.Reason, "test reason")
	}
}
