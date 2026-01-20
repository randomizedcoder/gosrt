package main

import (
	"fmt"
	"time"
)

// BottleneckType identifies where the performance bottleneck is located.
// See: client_seeker_instrumentation_design.md
type BottleneckType int

const (
	// BottleneckNone indicates no bottleneck detected (system is healthy)
	BottleneckNone BottleneckType = iota
	// BottleneckTool indicates the client-seeker tool itself is the bottleneck
	// (e.g., TokenBucket spinning, CPU overhead)
	BottleneckTool
	// BottleneckLibrary indicates the SRT library is the bottleneck
	// (e.g., Write() blocking, EventLoop starvation)
	BottleneckLibrary
	// BottleneckUnknown indicates insufficient data to determine bottleneck
	BottleneckUnknown
)

func (b BottleneckType) String() string {
	switch b {
	case BottleneckNone:
		return "NONE"
	case BottleneckTool:
		return "TOOL-LIMITED"
	case BottleneckLibrary:
		return "LIBRARY-LIMITED"
	default:
		return "UNKNOWN"
	}
}

// BottleneckDetector analyzes metrics to determine where the performance
// bottleneck is located. This is critical for distinguishing between
// tool-generated limits and actual SRT library limits.
//
// See: client_seeker_instrumentation_design.md Section 10.5
type BottleneckDetector struct {
	// Thresholds (configurable)
	EfficiencyThreshold    float64 // Below this = bottleneck (default: 0.95)
	ToolOverheadThreshold  float64 // Above this = tool bottleneck (default: 0.30)
	WriteBlockedThreshold  float64 // Above this = library bottleneck (default: 0.10)
	TokenStarvationThreshold float64 // Below this = tool bottleneck (default: 0.10)
}

// NewBottleneckDetector creates a detector with default thresholds.
func NewBottleneckDetector() *BottleneckDetector {
	return &BottleneckDetector{
		EfficiencyThreshold:     0.95, // 95% efficiency
		ToolOverheadThreshold:   0.30, // 30% time in tool overhead
		WriteBlockedThreshold:   0.10, // 10% writes blocked
		TokenStarvationThreshold: 0.10, // 10% token utilization
	}
}

// BottleneckAnalysis contains the results of bottleneck detection.
type BottleneckAnalysis struct {
	Type        BottleneckType
	Confidence  float64 // 0.0 - 1.0
	Reason      string
	Suggestions []string

	// Raw metrics used for analysis
	Efficiency       float64 // ActualBps / TargetBps
	ToolOverhead     float64 // (WaitTime + SpinTime) / Elapsed
	WriteBlockedRate float64 // WriteBlocked / WriteTotal
	TokenUtilization float64 // TokensAvailable / TokensMax
}

// BottleneckMetrics contains all metrics needed for bottleneck detection.
type BottleneckMetrics struct {
	// From Generator
	Efficiency float64 // ActualBps / TargetBps

	// From TokenBucket
	TotalWaitNs  int64
	SpinTimeNs   int64
	BlockedCount int64
	TokensAvailable int64
	TokensMax    int64
	Mode         string

	// From Publisher
	WriteTimeNs       int64
	WriteCount        uint64
	WriteBlockedCount uint64

	// Timing context
	ElapsedNs int64
}

// Analyze examines the metrics and determines the bottleneck type.
//
// Decision tree (from design doc):
// 1. If Efficiency >= 0.95 → No bottleneck
// 2. If ToolOverhead > 0.30 → Tool bottleneck
// 3. If WriteBlockedRate > 0.10 → Library bottleneck
// 4. If TokenUtilization < 0.10 → Tool bottleneck (starving)
// 5. Otherwise → Unknown
func (d *BottleneckDetector) Analyze(m BottleneckMetrics) BottleneckAnalysis {
	result := BottleneckAnalysis{
		Type:       BottleneckUnknown,
		Confidence: 0.0,
		Efficiency: m.Efficiency,
	}

	// Calculate derived metrics
	if m.ElapsedNs > 0 {
		result.ToolOverhead = float64(m.TotalWaitNs+m.SpinTimeNs) / float64(m.ElapsedNs)
	}
	if m.WriteCount > 0 {
		result.WriteBlockedRate = float64(m.WriteBlockedCount) / float64(m.WriteCount)
	}
	if m.TokensMax > 0 {
		result.TokenUtilization = float64(m.TokensAvailable) / float64(m.TokensMax)
	}

	// Decision tree
	// Step 1: Check if system is healthy
	if m.Efficiency >= d.EfficiencyThreshold {
		result.Type = BottleneckNone
		result.Confidence = 0.9
		result.Reason = fmt.Sprintf("Efficiency %.1f%% >= %.1f%% threshold",
			m.Efficiency*100, d.EfficiencyThreshold*100)
		return result
	}

	// Step 2: Check for tool overhead (spinning, waiting)
	if result.ToolOverhead > d.ToolOverheadThreshold {
		result.Type = BottleneckTool
		result.Confidence = 0.8
		result.Reason = fmt.Sprintf("Tool overhead %.1f%% > %.1f%% threshold (TokenBucket mode: %s)",
			result.ToolOverhead*100, d.ToolOverheadThreshold*100, m.Mode)
		result.Suggestions = []string{
			"Consider switching TokenBucket from RefillHybrid to RefillSleep",
			"Reduce spin time by using sleep-based rate limiting",
			"Profile client-seeker CPU usage",
		}
		return result
	}

	// Step 3: Check for library blocking (Write() taking too long)
	if result.WriteBlockedRate > d.WriteBlockedThreshold {
		result.Type = BottleneckLibrary
		result.Confidence = 0.8
		result.Reason = fmt.Sprintf("Write blocked rate %.1f%% > %.1f%% threshold",
			result.WriteBlockedRate*100, d.WriteBlockedThreshold*100)
		result.Suggestions = []string{
			"Check SRT EventLoop starvation",
			"Increase send buffer sizes",
			"Profile server CPU usage",
			"Check for control ring overflow",
		}
		return result
	}

	// Step 4: Check for token starvation (tool can't keep up)
	if result.TokenUtilization < d.TokenStarvationThreshold {
		result.Type = BottleneckTool
		result.Confidence = 0.7
		result.Reason = fmt.Sprintf("Token utilization %.1f%% < %.1f%% threshold (starving)",
			result.TokenUtilization*100, d.TokenStarvationThreshold*100)
		result.Suggestions = []string{
			"TokenBucket is not refilling fast enough",
			"Check refill loop frequency",
			"Consider RefillSleep mode for better CPU efficiency",
		}
		return result
	}

	// Step 5: Unknown - need more data
	result.Type = BottleneckUnknown
	result.Confidence = 0.3
	result.Reason = fmt.Sprintf("Efficiency %.1f%% but no clear bottleneck indicator",
		m.Efficiency*100)
	result.Suggestions = []string{
		"Collect more samples",
		"Check for network issues",
		"Profile both client-seeker and server",
	}
	return result
}

// CollectMetrics gathers metrics from all components for analysis.
func CollectMetrics(
	gen *DataGenerator,
	bucket *TokenBucket,
	pub *Publisher,
	elapsed time.Duration,
) BottleneckMetrics {
	genStats := gen.DetailedStats()
	tbStats := bucket.DetailedStats()
	pubStats := pub.DetailedStats()

	return BottleneckMetrics{
		Efficiency:        genStats.Efficiency,
		TotalWaitNs:       tbStats.TotalWaitNs,
		SpinTimeNs:        tbStats.SpinTimeNs,
		BlockedCount:      tbStats.BlockedCount,
		TokensAvailable:   tbStats.TokensAvailable,
		TokensMax:         tbStats.TokensMax,
		Mode:              tbStats.Mode,
		WriteTimeNs:       pubStats.WriteTimeNs,
		WriteCount:        pubStats.WriteCount,
		WriteBlockedCount: pubStats.WriteBlockedCount,
		ElapsedNs:         elapsed.Nanoseconds(),
	}
}
