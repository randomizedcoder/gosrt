package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestReporter_ProbeTracking(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// Simulate some probes
	r.ProbeStart(1, 100_000_000, 0, 500_000_000)
	r.ProbeEnd(1, 100_000_000, ProbeResult{
		Verdict:  VerdictStable,
		Duration: 5 * time.Second,
	})

	r.ProbeStart(2, 200_000_000, 100_000_000, 500_000_000)
	r.ProbeEnd(2, 200_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 3 * time.Second,
	})

	if len(r.probes) != 2 {
		t.Errorf("Expected 2 probes, got %d", len(r.probes))
	}

	if !r.probes[0].Stable {
		t.Error("Probe 1 should be stable")
	}
	if r.probes[1].Stable {
		t.Error("Probe 2 should be unstable")
	}
}

func TestReporter_HypothesisCollection(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// Simulate a probe with high NAK rate (Hypothesis 1)
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 2 * time.Second,
		Metrics: StabilityMetrics{
			NAKRate: 0.05, // 5% > 2% threshold
		},
	})

	if len(r.hypotheses) != 1 {
		t.Fatalf("Expected 1 hypothesis, got %d", len(r.hypotheses))
	}

	if r.hypotheses[0].ID != 1 {
		t.Errorf("Expected hypothesis 1, got %d", r.hypotheses[0].ID)
	}
	if r.hypotheses[0].Confidence != "HIGH" {
		t.Errorf("Expected HIGH confidence, got %s", r.hypotheses[0].Confidence)
	}
}

func TestReporter_Hypothesis2_EventLoopStarvation(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// Low throughput efficiency without packet loss
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 5 * time.Second,
		Metrics: StabilityMetrics{
			ThroughputTE: 0.85,   // 85% < 95% threshold
			GapRate:      0.0001, // Very low gap rate
		},
	})

	found := false
	for _, h := range r.hypotheses {
		if h.ID == 2 && h.Name == "Sender EventLoop Starvation" {
			found = true
			if h.Confidence != "HIGH" {
				t.Errorf("Expected HIGH confidence, got %s", h.Confidence)
			}
		}
	}

	if !found {
		t.Error("Expected hypothesis 2 (EventLoop Starvation) to be triggered")
	}
}

func TestReporter_Hypothesis3_BtreeLag(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// High gap rate
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 5 * time.Second,
		Metrics: StabilityMetrics{
			GapRate: 0.05, // 5% > 1% threshold
		},
	})

	found := false
	for _, h := range r.hypotheses {
		if h.ID == 3 && h.Name == "Btree Iteration Lag" {
			found = true
		}
	}

	if !found {
		t.Error("Expected hypothesis 3 (Btree Lag) to be triggered")
	}
}

func TestReporter_Hypothesis5_GCPressure(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// High RTT variance
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 5 * time.Second,
		Metrics: StabilityMetrics{
			RTTVarianceMs: 25.0, // 25ms > 20ms threshold
		},
	})

	found := false
	for _, h := range r.hypotheses {
		if h.ID == 5 && h.Name == "GC/Memory Pressure" {
			found = true
		}
	}

	if !found {
		t.Error("Expected hypothesis 5 (GC Pressure) to be triggered")
	}
}

func TestReporter_NoHypothesisForStableProbe(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// Stable probe should not trigger hypotheses
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictStable,
		Duration: 5 * time.Second,
		Metrics: StabilityMetrics{
			NAKRate: 0.05, // Would trigger H1 if unstable
		},
	})

	if len(r.hypotheses) != 0 {
		t.Errorf("Expected 0 hypotheses for stable probe, got %d", len(r.hypotheses))
	}
}

func TestReporter_JSONOutput(t *testing.T) {
	r := NewProgressReporter(ReportJSON)

	r.ProbeEnd(1, 300_000_000, ProbeResult{
		Verdict:  VerdictStable,
		Duration: 5 * time.Second,
	})

	result := SearchResult{
		Status:  StatusSuccess,
		Ceiling: 300_000_000,
		Proven:  true,
	}

	// Capture stdout
	old := os.Stdout
	r2, w, _ := os.Pipe()
	os.Stdout = w

	r.FinalReport(result)

	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close pipe writer: %v", err)
	}
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r2); err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}
	output := buf.String()

	// Parse JSON
	var report JSONReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
	}

	if report.Ceiling != 300_000_000 {
		t.Errorf("Ceiling = %d, want 300000000", report.Ceiling)
	}
	if !report.Proven {
		t.Error("Proven should be true")
	}
	if report.Status != "success" {
		t.Errorf("Status = %s, want success", report.Status)
	}
}

func TestReporter_SaveLoadProbes(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	r.ProbeEnd(1, 100_000_000, ProbeResult{Verdict: VerdictStable, Duration: 5 * time.Second})
	r.ProbeEnd(2, 200_000_000, ProbeResult{Verdict: VerdictUnstable, Duration: 3 * time.Second})

	// Save to temp file
	tmpFile, err := os.CreateTemp("", "probes-*.json")
	if err != nil {
		t.Fatal(err)
	}
	tmpFileName := tmpFile.Name()
	t.Cleanup(func() {
		if removeErr := os.Remove(tmpFileName); removeErr != nil && !os.IsNotExist(removeErr) {
			t.Logf("Warning: failed to remove temp file: %v", removeErr)
		}
	})
	if closeErr := tmpFile.Close(); closeErr != nil {
		t.Fatalf("Failed to close temp file: %v", closeErr)
	}

	if saveErr := r.SaveProbes(tmpFile.Name()); saveErr != nil {
		t.Fatalf("SaveProbes failed: %v", saveErr)
	}

	// Load and verify
	loaded, err := LoadProbes(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadProbes failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Errorf("Expected 2 probes, got %d", len(loaded))
	}
	if loaded[0].TargetBitrate != 100_000_000 {
		t.Errorf("Probe 1 bitrate = %d, want 100000000", loaded[0].TargetBitrate)
	}
}

func TestSearchStatus_String(t *testing.T) {
	tests := []struct {
		status SearchStatus
		want   string
	}{
		{StatusSuccess, "success"},
		{StatusFailed, "failed"},
		{StatusAborted, "aborted"},
		{SearchStatus("custom"), "custom"},
	}

	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestParseReportMode(t *testing.T) {
	tests := []struct {
		input string
		want  ReportMode
	}{
		{"json", ReportJSON},
		{"JSON", ReportJSON},
		{"quiet", ReportQuiet},
		{"QUIET", ReportQuiet},
		{"terminal", ReportTerminal},
		{"", ReportTerminal},
		{"unknown", ReportTerminal},
	}

	for _, tt := range tests {
		got := ParseReportMode(tt.input)
		if got != tt.want {
			t.Errorf("ParseReportMode(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long string", 10, "this is..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}

	for _, tt := range tests {
		got := truncate(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

func TestReporter_MultipleHypothesesSameProbe(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// Probe that triggers multiple hypotheses
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 2 * time.Second,
		Metrics: StabilityMetrics{
			NAKRate:       0.05, // H1
			ThroughputTE:  0.85, // H2
			GapRate:       0.02, // H3
			RTTVarianceMs: 25.0, // H5
		},
	})

	// Should have multiple hypotheses
	if len(r.hypotheses) < 3 {
		t.Errorf("Expected at least 3 hypotheses, got %d", len(r.hypotheses))
	}

	// Check that each is unique
	ids := make(map[int]bool)
	for _, h := range r.hypotheses {
		if ids[h.ID] {
			t.Errorf("Duplicate hypothesis ID: %d", h.ID)
		}
		ids[h.ID] = true
	}
}

func TestReporter_HypothesisConfidenceUpgrade(t *testing.T) {
	r := NewProgressReporter(ReportQuiet)

	// First probe triggers H1 with MEDIUM confidence (if we had such a case)
	// For now, just verify that duplicate hypotheses don't get added
	r.ProbeEnd(1, 400_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 2 * time.Second,
		Metrics: StabilityMetrics{
			NAKRate: 0.05,
		},
	})

	r.ProbeEnd(2, 350_000_000, ProbeResult{
		Verdict:  VerdictUnstable,
		Duration: 1 * time.Second,
		Metrics: StabilityMetrics{
			NAKRate: 0.10, // Even higher NAK rate
		},
	})

	// Should still have only one H1
	h1Count := 0
	for _, h := range r.hypotheses {
		if h.ID == 1 {
			h1Count++
		}
	}

	if h1Count != 1 {
		t.Errorf("Expected 1 H1 hypothesis, got %d", h1Count)
	}
}
