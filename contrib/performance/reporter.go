package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ReportMode defines the output format.
type ReportMode int

const (
	ReportTerminal ReportMode = iota
	ReportJSON
	ReportQuiet
)

// ProgressReporter handles progress output and final results.
// Implements the Reporter interface.
type ProgressReporter struct {
	mode            ReportMode
	startTime       time.Time
	probes          []ProbeRecord
	hypothesisModel HypothesisModel
	hypotheses      []HypothesisEvidence
	verbose         bool
}

// HypothesisEvidence tracks evidence for a bottleneck hypothesis.
type HypothesisEvidence struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Triggered  bool     `json:"triggered"`
	Confidence string   `json:"confidence"` // "HIGH", "MEDIUM", "LOW"
	Evidence   []string `json:"evidence"`
	Suggestion string   `json:"suggestion"`
}

// NewProgressReporter creates a new progress reporter.
func NewProgressReporter(mode ReportMode) *ProgressReporter {
	return &ProgressReporter{
		mode:            mode,
		startTime:       time.Now(),
		hypothesisModel: DefaultHypothesisModel(),
		hypotheses:      make([]HypothesisEvidence, 0),
	}
}

// SetVerbose enables verbose output.
func (r *ProgressReporter) SetVerbose(v bool) {
	r.verbose = v
}

// SetHypothesisModel sets custom hypothesis thresholds.
func (r *ProgressReporter) SetHypothesisModel(m HypothesisModel) {
	r.hypothesisModel = m
}

// ProbeStart logs the start of a probe.
func (r *ProgressReporter) ProbeStart(number int, bitrate int64, low, high int64) {
	if r.mode == ReportQuiet {
		return
	}
	if r.verbose {
		fmt.Printf("[Probe %d] Starting: target=%s, bounds=[%s, %s)\n",
			number, FormatBitrate(bitrate), FormatBitrate(low), FormatBitrate(high))
	}
}

// ProbeEnd logs the end of a probe and collects hypothesis evidence.
func (r *ProgressReporter) ProbeEnd(number int, bitrate int64, result ProbeResult) {
	// Store probe record
	record := ProbeRecord{
		Number:        number,
		TargetBitrate: bitrate,
		Stable:        result.Verdict == VerdictStable,
		Critical:      result.Verdict == VerdictCritical || result.Verdict == VerdictEOF,
		Duration:      result.Duration,
		Metrics:       &result.Metrics,
	}
	r.probes = append(r.probes, record)

	// Collect evidence for hypothesis validation (always, regardless of mode)
	if !record.Stable {
		r.collectHypothesisEvidence(bitrate, result)
	}

	// Early return for quiet mode (no logging)
	if r.mode == ReportQuiet {
		return
	}

	// Log result
	icon := "✓"
	if !record.Stable {
		icon = "✗"
		if record.Critical {
			icon = "💥"
		}
	}

	if r.verbose {
		fmt.Printf("[Probe %d] %s %s: %s (%v)\n",
			number, icon, FormatBitrate(bitrate), result.Verdict, result.Duration.Round(time.Millisecond))
	} else if r.mode == ReportTerminal {
		fmt.Printf("  %s %s: %s\n", icon, FormatBitrate(bitrate), result.Verdict)
	}
}

// FinalReport outputs the final results.
func (r *ProgressReporter) FinalReport(result SearchResult) {
	switch r.mode {
	case ReportTerminal:
		r.printTerminalReport(result)
	case ReportJSON:
		r.printJSONReport(result)
	case ReportQuiet:
		fmt.Printf("%d\n", result.Ceiling)
	}
}

// printTerminalReport outputs a formatted terminal report.
func (r *ProgressReporter) printTerminalReport(result SearchResult) {
	fmt.Println()
	fmt.Println("╔═══════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                           FINAL RESULTS                                   ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")

	// Main result
	ceilingMbps := float64(result.Ceiling) / 1_000_000
	provenStr := ""
	if result.Proven {
		provenStr = " (PROVEN) ✓"
	} else {
		provenStr = " (unverified)"
	}
	fmt.Printf("║  Maximum Sustainable Throughput: %.2f Mb/s%s\n", ceilingMbps, provenStr)
	fmt.Println("║                                                                           ║")

	// Search statistics
	fmt.Println("║  Search Statistics:                                                       ║")
	fmt.Printf("║    Status:         %s\n", result.Status)
	fmt.Printf("║    Total Probes:   %d\n", len(r.probes))
	fmt.Printf("║    Total Time:     %v\n", time.Since(r.startTime).Round(time.Second))

	if result.Artifacts.Probes != nil && len(result.Artifacts.Probes) > 0 {
		lastProbe := result.Artifacts.Probes[len(result.Artifacts.Probes)-1]
		fmt.Printf("║    Final Bounds:   [%s, %s)\n",
			FormatBitrate(lastProbe.TargetBitrate), FormatBitrate(result.Ceiling))
	}

	// Hypothesis analysis
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("║                      BOTTLENECK HYPOTHESIS ANALYSIS                       ║")
	fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")

	triggeredCount := 0
	for _, h := range r.hypotheses {
		if h.Triggered {
			triggeredCount++
			icon := "🔴"
			if h.Confidence == "MEDIUM" {
				icon = "🟡"
			} else if h.Confidence == "LOW" {
				icon = "🟠"
			}

			fmt.Printf("║  %s HYPOTHESIS %d: %s\n", icon, h.ID, h.Name)
			fmt.Printf("║     Confidence: %s\n", h.Confidence)
			for _, e := range h.Evidence {
				// Truncate long evidence lines
				if len(e) > 60 {
					e = e[:57] + "..."
				}
				fmt.Printf("║     • %s\n", e)
			}
			fmt.Printf("║     → %s\n", h.Suggestion)
			fmt.Println("║                                                                           ║")
		}
	}

	if triggeredCount == 0 {
		fmt.Println("║  ✓ No specific bottleneck identified                                      ║")
		fmt.Println("║    System may be at hardware/network limit                                ║")
	}

	// Failure reason if applicable
	if result.FailReason != "" {
		fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
		fmt.Printf("║  Failure Reason: %s\n", truncate(result.FailReason, 55))
	}

	fmt.Println("╚═══════════════════════════════════════════════════════════════════════════╝")
}

// JSONReport is the structure for JSON output.
type JSONReport struct {
	Ceiling         int64                `json:"ceiling_bps"`
	CeilingMbps     float64              `json:"ceiling_mbps"`
	Proven          bool                 `json:"proven"`
	Status          string               `json:"status"`
	TotalProbes     int                  `json:"total_probes"`
	TotalTime       string               `json:"total_time"`
	FailReason      string               `json:"fail_reason,omitempty"`
	Probes          []ProbeRecord        `json:"probes"`
	Hypotheses      []HypothesisEvidence `json:"hypotheses"`
	HypothesisModel HypothesisModel      `json:"hypothesis_model"`
	Artifacts       *FailureArtifacts    `json:"artifacts,omitempty"`
}

// printJSONReport outputs JSON format.
func (r *ProgressReporter) printJSONReport(result SearchResult) {
	report := JSONReport{
		Ceiling:         result.Ceiling,
		CeilingMbps:     float64(result.Ceiling) / 1_000_000,
		Proven:          result.Proven,
		Status:          result.Status.String(),
		TotalProbes:     len(r.probes),
		TotalTime:       time.Since(r.startTime).Round(time.Second).String(),
		FailReason:      result.FailReason,
		Probes:          r.probes,
		Hypotheses:      r.hypotheses,
		HypothesisModel: r.hypothesisModel,
	}

	if result.Status == StatusFailed {
		report.Artifacts = &result.Artifacts
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
}

// collectHypothesisEvidence analyzes probe results for bottleneck evidence.
func (r *ProgressReporter) collectHypothesisEvidence(bitrate int64, result ProbeResult) {
	m := result.Metrics
	h := r.hypothesisModel

	// Calculate throughput efficiency
	te := m.ThroughputTE
	if te == 0 && m.ActualBitrate > 0 && bitrate > 0 {
		te = float64(m.ActualBitrate) / float64(bitrate)
	}

	// Hypothesis 1: Flow Control Window Exhaustion
	// High NAK rate suggests FC exhaustion
	if m.NAKRate > h.H1NAKRateThreshold {
		r.addEvidence(1, "Flow Control Window Exhaustion", "HIGH",
			[]string{
				fmt.Sprintf("NAK rate %.2f%% > %.2f%% threshold", m.NAKRate*100, h.H1NAKRateThreshold*100),
			},
			"Increase FC to 204800+, check ACK processing latency")
	}

	// Hypothesis 2: EventLoop Starvation
	// Low throughput efficiency without packet loss suggests sender can't keep up
	if te > 0 && te < h.H2TEThreshold && m.GapRate < 0.001 {
		r.addEvidence(2, "Sender EventLoop Starvation", "HIGH",
			[]string{
				fmt.Sprintf("Throughput efficiency %.1f%% < %.1f%% threshold", te*100, h.H2TEThreshold*100),
				fmt.Sprintf("Gap rate %.3f%% (no network loss)", m.GapRate*100),
			},
			"Profile deliverReadyPacketsEventLoop(), reduce BackoffMaxSleep")
	}

	// Hypothesis 3: Btree Iteration Lag
	// High gap rate suggests btree can't keep up
	if m.GapRate > h.H3GapRateThreshold {
		r.addEvidence(3, "Btree Iteration Lag", "MEDIUM",
			[]string{
				fmt.Sprintf("Gap rate %.3f%% > %.3f%% threshold", m.GapRate*100, h.H3GapRateThreshold*100),
			},
			"Profile btree iteration, consider ring buffer")
	}

	// Hypothesis 4: Ring Buffer Contention
	// (Detected via retry metrics if available)
	// For now, flag if we have low TE with high gap rate
	if te > 0 && te < h.H2TEThreshold && m.GapRate > h.H3GapRateThreshold {
		r.addEvidence(4, "Ring Buffer Contention", "MEDIUM",
			[]string{
				fmt.Sprintf("Throughput efficiency %.1f%% < %.1f%% threshold", te*100, h.H2TEThreshold*100),
				fmt.Sprintf("Gap rate %.3f%% > %.3f%% (with packet loss)", m.GapRate*100, h.H3GapRateThreshold*100),
			},
			"Increase ring buffer size, reduce shards")
	}

	// Hypothesis 5: GC/Memory Pressure
	// High RTT variance suggests GC pauses
	if m.RTTVarianceMs > h.H5RTTVarianceThreshold {
		r.addEvidence(5, "GC/Memory Pressure", "MEDIUM",
			[]string{
				fmt.Sprintf("RTT variance %.1fms > %.1fms threshold", m.RTTVarianceMs, h.H5RTTVarianceThreshold),
			},
			"Run with GODEBUG=gctrace=1, check heap.pprof")
	}

	// Hypothesis 6: Metrics Lock Contention
	// EOF without clear cause
	if result.Verdict == VerdictEOF && te > 0.95 && m.GapRate < 0.001 && m.RTTMs < 10.0 {
		r.addEvidence(6, "Possible Metrics/Lock Contention", "LOW",
			[]string{
				"No obvious bottleneck from metrics",
				"EOF without clear cause",
			},
			"Profile mutex.pprof, check for per-packet atomic contention")
	}
}

// addEvidence adds or updates hypothesis evidence.
func (r *ProgressReporter) addEvidence(id int, name, confidence string, evidence []string, suggestion string) {
	// Check if hypothesis already recorded
	for i, h := range r.hypotheses {
		if h.ID == id {
			// Update if higher confidence
			if confidence == "HIGH" || (confidence == "MEDIUM" && h.Confidence != "HIGH") {
				r.hypotheses[i].Confidence = confidence
				r.hypotheses[i].Evidence = append(r.hypotheses[i].Evidence, evidence...)
			}
			return
		}
	}

	r.hypotheses = append(r.hypotheses, HypothesisEvidence{
		ID:         id,
		Name:       name,
		Triggered:  true,
		Confidence: confidence,
		Evidence:   evidence,
		Suggestion: suggestion,
	})
}

// SaveProbes saves probe history for replay.
func (r *ProgressReporter) SaveProbes(path string) error {
	data, err := json.MarshalIndent(r.probes, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal probes: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadProbes loads probe history for replay.
func LoadProbes(path string) ([]ProbeRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read probes: %w", err)
	}
	var probes []ProbeRecord
	if err := json.Unmarshal(data, &probes); err != nil {
		return nil, fmt.Errorf("unmarshal probes: %w", err)
	}
	return probes, nil
}

// Helper functions

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// String returns a string representation of SearchStatus.
func (s SearchStatus) String() string {
	return string(s)
}

// ParseReportMode parses a report mode string.
func ParseReportMode(s string) ReportMode {
	switch strings.ToLower(s) {
	case "json":
		return ReportJSON
	case "quiet":
		return ReportQuiet
	default:
		return ReportTerminal
	}
}
