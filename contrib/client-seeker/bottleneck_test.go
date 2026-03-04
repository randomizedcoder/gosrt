package main

import (
	"testing"
)

func TestBottleneckDetector_NoBottleneck(t *testing.T) {
	// When efficiency is high, no bottleneck should be detected
	detector := NewBottleneckDetector()

	metrics := BottleneckMetrics{
		Efficiency:        0.98, // 98% efficiency - healthy
		TotalWaitNs:       1000000,
		SpinTimeNs:        500000,
		TokensAvailable:   50000,
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 5,
		ElapsedNs:         100000000, // 100ms
	}

	result := detector.Analyze(metrics)

	if result.Type != BottleneckNone {
		t.Errorf("Type = %v, want BottleneckNone", result.Type)
	}
	if result.Confidence < 0.8 {
		t.Errorf("Confidence = %.2f, want >= 0.8", result.Confidence)
	}
}

func TestBottleneckDetector_ToolBottleneck_HighOverhead(t *testing.T) {
	// When tool overhead is high (spinning), tool bottleneck should be detected
	detector := NewBottleneckDetector()

	metrics := BottleneckMetrics{
		Efficiency:        0.70,     // 70% efficiency - bottleneck exists
		TotalWaitNs:       40000000, // 40ms wait
		SpinTimeNs:        30000000, // 30ms spin
		TokensAvailable:   50000,
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 5,
		ElapsedNs:         100000000, // 100ms
		Mode:              "hybrid",
	}

	result := detector.Analyze(metrics)

	if result.Type != BottleneckTool {
		t.Errorf("Type = %v, want BottleneckTool", result.Type)
	}
	if result.ToolOverhead < 0.30 {
		t.Errorf("ToolOverhead = %.2f, want >= 0.30", result.ToolOverhead)
	}
	if len(result.Suggestions) == 0 {
		t.Error("Expected suggestions for tool bottleneck")
	}
	t.Logf("Reason: %s", result.Reason)
	t.Logf("Suggestions: %v", result.Suggestions)
}

func TestBottleneckDetector_LibraryBottleneck_WriteBlocked(t *testing.T) {
	// When writes are frequently blocked, library bottleneck should be detected
	detector := NewBottleneckDetector()

	metrics := BottleneckMetrics{
		Efficiency:        0.70,    // 70% efficiency - bottleneck exists
		TotalWaitNs:       5000000, // 5ms wait (low overhead)
		SpinTimeNs:        1000000, // 1ms spin
		TokensAvailable:   50000,
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 200,       // 20% blocked - high!
		ElapsedNs:         100000000, // 100ms
		Mode:              "sleep",
	}

	result := detector.Analyze(metrics)

	if result.Type != BottleneckLibrary {
		t.Errorf("Type = %v, want BottleneckLibrary", result.Type)
	}
	if result.WriteBlockedRate < 0.10 {
		t.Errorf("WriteBlockedRate = %.2f, want >= 0.10", result.WriteBlockedRate)
	}
	if len(result.Suggestions) == 0 {
		t.Error("Expected suggestions for library bottleneck")
	}
	t.Logf("Reason: %s", result.Reason)
	t.Logf("Suggestions: %v", result.Suggestions)
}

func TestBottleneckDetector_ToolBottleneck_TokenStarvation(t *testing.T) {
	// When tokens are nearly depleted, tool bottleneck (starvation) should be detected
	detector := NewBottleneckDetector()

	metrics := BottleneckMetrics{
		Efficiency:        0.70,    // 70% efficiency - bottleneck exists
		TotalWaitNs:       5000000, // 5ms wait (low overhead)
		SpinTimeNs:        1000000, // 1ms spin
		TokensAvailable:   5000,    // Only 5% available!
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 5,         // Low blocked rate
		ElapsedNs:         100000000, // 100ms
		Mode:              "sleep",
	}

	result := detector.Analyze(metrics)

	if result.Type != BottleneckTool {
		t.Errorf("Type = %v, want BottleneckTool (starvation)", result.Type)
	}
	if result.TokenUtilization > 0.10 {
		t.Errorf("TokenUtilization = %.2f, want < 0.10", result.TokenUtilization)
	}
	t.Logf("Reason: %s", result.Reason)
}

func TestBottleneckDetector_Unknown(t *testing.T) {
	// When efficiency is low but no clear indicator, unknown should be returned
	detector := NewBottleneckDetector()

	metrics := BottleneckMetrics{
		Efficiency:        0.70,    // 70% efficiency - bottleneck exists
		TotalWaitNs:       5000000, // 5ms wait (low overhead)
		SpinTimeNs:        1000000, // 1ms spin
		TokensAvailable:   50000,   // 50% available (not starving)
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 5,         // Low blocked rate
		ElapsedNs:         100000000, // 100ms
		Mode:              "sleep",
	}

	result := detector.Analyze(metrics)

	if result.Type != BottleneckUnknown {
		t.Errorf("Type = %v, want BottleneckUnknown", result.Type)
	}
	if len(result.Suggestions) == 0 {
		t.Error("Expected suggestions for unknown bottleneck")
	}
	t.Logf("Reason: %s", result.Reason)
}

func TestBottleneckType_String(t *testing.T) {
	tests := []struct {
		typ  BottleneckType
		want string
	}{
		{BottleneckNone, "NONE"},
		{BottleneckTool, "TOOL-LIMITED"},
		{BottleneckLibrary, "LIBRARY-LIMITED"},
		{BottleneckUnknown, "UNKNOWN"},
	}

	for _, tt := range tests {
		if got := tt.typ.String(); got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.typ, got, tt.want)
		}
	}
}

func TestBottleneckDetector_CustomThresholds(t *testing.T) {
	// Test that custom thresholds work
	detector := &BottleneckDetector{
		EfficiencyThreshold:      0.90, // More lenient
		ToolOverheadThreshold:    0.50, // More lenient
		WriteBlockedThreshold:    0.20, // More lenient
		TokenStarvationThreshold: 0.05, // More strict
	}

	// With 92% efficiency, should be "no bottleneck" with 0.90 threshold
	metrics := BottleneckMetrics{
		Efficiency:        0.92,
		TotalWaitNs:       1000000,
		SpinTimeNs:        500000,
		TokensAvailable:   50000,
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 5,
		ElapsedNs:         100000000,
	}

	result := detector.Analyze(metrics)

	if result.Type != BottleneckNone {
		t.Errorf("Type = %v, want BottleneckNone with custom threshold", result.Type)
	}
}

func TestBottleneckAnalysis_DerivedMetrics(t *testing.T) {
	// Test that derived metrics are calculated correctly
	detector := NewBottleneckDetector()

	metrics := BottleneckMetrics{
		Efficiency:        0.98,
		TotalWaitNs:       30000000, // 30ms
		SpinTimeNs:        20000000, // 20ms
		TokensAvailable:   25000,
		TokensMax:         100000,
		WriteCount:        1000,
		WriteBlockedCount: 100,
		ElapsedNs:         100000000, // 100ms
	}

	result := detector.Analyze(metrics)

	// ToolOverhead = (30 + 20) / 100 = 0.50
	expectedOverhead := 0.50
	if result.ToolOverhead < expectedOverhead-0.01 || result.ToolOverhead > expectedOverhead+0.01 {
		t.Errorf("ToolOverhead = %.2f, want %.2f", result.ToolOverhead, expectedOverhead)
	}

	// WriteBlockedRate = 100 / 1000 = 0.10
	expectedBlockedRate := 0.10
	if result.WriteBlockedRate < expectedBlockedRate-0.01 || result.WriteBlockedRate > expectedBlockedRate+0.01 {
		t.Errorf("WriteBlockedRate = %.2f, want %.2f", result.WriteBlockedRate, expectedBlockedRate)
	}

	// TokenUtilization = 25000 / 100000 = 0.25
	expectedUtilization := 0.25
	if result.TokenUtilization < expectedUtilization-0.01 || result.TokenUtilization > expectedUtilization+0.01 {
		t.Errorf("TokenUtilization = %.2f, want %.2f", result.TokenUtilization, expectedUtilization)
	}
}

// Table-driven test for decision tree
func TestBottleneckDetector_DecisionTree(t *testing.T) {
	detector := NewBottleneckDetector()

	tests := []struct {
		name     string
		metrics  BottleneckMetrics
		wantType BottleneckType
	}{
		{
			name: "healthy_system",
			metrics: BottleneckMetrics{
				Efficiency:        0.98,
				TotalWaitNs:       1000000,
				SpinTimeNs:        500000,
				TokensAvailable:   50000,
				TokensMax:         100000,
				WriteCount:        1000,
				WriteBlockedCount: 5,
				ElapsedNs:         100000000,
			},
			wantType: BottleneckNone,
		},
		{
			name: "tool_overhead_high",
			metrics: BottleneckMetrics{
				Efficiency:        0.70,
				TotalWaitNs:       40000000,
				SpinTimeNs:        30000000,
				TokensAvailable:   50000,
				TokensMax:         100000,
				WriteCount:        1000,
				WriteBlockedCount: 5,
				ElapsedNs:         100000000,
				Mode:              "hybrid",
			},
			wantType: BottleneckTool,
		},
		{
			name: "write_blocked_high",
			metrics: BottleneckMetrics{
				Efficiency:        0.70,
				TotalWaitNs:       5000000,
				SpinTimeNs:        1000000,
				TokensAvailable:   50000,
				TokensMax:         100000,
				WriteCount:        1000,
				WriteBlockedCount: 200,
				ElapsedNs:         100000000,
			},
			wantType: BottleneckLibrary,
		},
		{
			name: "token_starvation",
			metrics: BottleneckMetrics{
				Efficiency:        0.70,
				TotalWaitNs:       5000000,
				SpinTimeNs:        1000000,
				TokensAvailable:   5000,
				TokensMax:         100000,
				WriteCount:        1000,
				WriteBlockedCount: 5,
				ElapsedNs:         100000000,
			},
			wantType: BottleneckTool,
		},
		{
			name: "unknown_bottleneck",
			metrics: BottleneckMetrics{
				Efficiency:        0.70,
				TotalWaitNs:       5000000,
				SpinTimeNs:        1000000,
				TokensAvailable:   50000,
				TokensMax:         100000,
				WriteCount:        1000,
				WriteBlockedCount: 5,
				ElapsedNs:         100000000,
			},
			wantType: BottleneckUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.Analyze(tt.metrics)
			if result.Type != tt.wantType {
				t.Errorf("Analyze() Type = %v, want %v\nReason: %s",
					result.Type, tt.wantType, result.Reason)
			}
		})
	}
}
