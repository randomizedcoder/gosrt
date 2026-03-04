package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// StabilityGate implements the Gate interface.
// It is the "single binary oracle" for stability - SearchLoop MUST NOT interpret metrics.
type StabilityGate struct {
	config    StabilityConfig
	timing    TimingModel
	collector *MetricsCollector
	seeker    *SeekerControl
	profiler  *DiagnosticProfiler

	// High-resolution monitoring (Snapshot-on-EOF)
	fastPollInterval time.Duration // 50ms for seeker status
	slowPollInterval time.Duration // 500ms for Prometheus metrics

	// EOF detection state
	mu                 sync.Mutex
	lastAliveTime      time.Time
	connectionWasAlive bool
}

// NewStabilityGate creates a new stability gate.
func NewStabilityGate(
	config StabilityConfig,
	timing TimingModel,
	collector *MetricsCollector,
	seeker *SeekerControl,
	profiler *DiagnosticProfiler,
) *StabilityGate {
	return &StabilityGate{
		config:           config,
		timing:           timing,
		collector:        collector,
		seeker:           seeker,
		profiler:         profiler,
		fastPollInterval: timing.FastPollInterval,
		slowPollInterval: timing.SampleInterval,
	}
}

// Probe implements Gate interface.
// probeStart is when the probe officially started (after ramp completed).
// The Gate handles warm-up and stability window internally.
func (g *StabilityGate) Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult {
	// Track connection state
	g.mu.Lock()
	g.connectionWasAlive = true
	g.lastAliveTime = time.Now()
	g.mu.Unlock()

	// 1. Warm-up phase with fast polling for early EOF detection
	warmUpStart := time.Now()
	if earlyFailure := g.warmUpWithFastPoll(ctx, bitrate, probeStart); earlyFailure != nil {
		earlyFailure.Duration = time.Since(warmUpStart)
		return *earlyFailure
	}

	// 2. Stability evaluation with dual-speed polling
	evalStart := time.Now()
	samples := make([]StabilityMetrics, 0, g.timing.RequiredSamples)

	// Two tickers: fast for EOF detection, slow for metrics
	fastTicker := time.NewTicker(g.fastPollInterval)
	slowTicker := time.NewTicker(g.slowPollInterval)
	defer fastTicker.Stop()
	defer slowTicker.Stop()

	deadline := time.Now().Add(g.config.StabilityWindow)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ProbeResult{Verdict: VerdictTimeout, Reason: "context canceled"}

		case <-fastTicker.C:
			// FAST PATH: Check connection alive (50ms)
			status, err := g.seeker.Status(ctx)
			if err != nil || !status.ConnectionAlive {
				// === SNAPSHOT-ON-EOF: Capture immediately! ===
				m, collectErr := g.collector.Collect(ctx)
				if collectErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to collect metrics at EOF: %v\n", collectErr)
				}
				diag := g.captureAtFailureImmediate(bitrate, m, probeStart)

				return ProbeResult{
					Verdict:   VerdictEOF,
					Metrics:   m,
					Samples:   len(samples),
					Duration:  time.Since(evalStart),
					Reason:    "connection died during stability evaluation",
					Artifacts: diag,
				}
			}
			g.mu.Lock()
			g.lastAliveTime = time.Now()
			g.mu.Unlock()

		case <-slowTicker.C:
			// SLOW PATH: Full metrics collection (500ms)
			m, err := g.collector.Collect(ctx)
			if err != nil {
				continue // Skip this sample on error
			}
			samples = append(samples, m)

			// Check critical thresholds
			if g.isCritical(m) {
				diag := g.captureAtFailureImmediate(bitrate, m, probeStart)
				return ProbeResult{
					Verdict:   VerdictCritical,
					Metrics:   m,
					Samples:   len(samples),
					Duration:  time.Since(evalStart),
					Reason:    fmt.Sprintf("critical threshold exceeded: gap=%.3f%%, nak=%.3f%%", m.GapRate*100, m.NAKRate*100),
					Artifacts: diag,
				}
			}
		}
	}

	// 3. Evaluate all samples
	if len(samples) < 3 {
		return ProbeResult{
			Verdict:  VerdictUnstable,
			Samples:  len(samples),
			Duration: time.Since(evalStart),
			Reason:   fmt.Sprintf("insufficient samples: got %d, need >= 3", len(samples)),
		}
	}

	verdict, reason := g.evaluateSamples(samples)
	aggregated := g.aggregate(samples)

	return ProbeResult{
		Verdict:  verdict,
		Metrics:  aggregated,
		Samples:  len(samples),
		Duration: time.Since(evalStart),
		Reason:   reason,
	}
}

// WithConfig returns a Gate with modified configuration.
func (g *StabilityGate) WithConfig(config StabilityConfig) Gate {
	return &StabilityGate{
		config:           config,
		timing:           g.timing,
		collector:        g.collector,
		seeker:           g.seeker,
		profiler:         g.profiler,
		fastPollInterval: g.fastPollInterval,
		slowPollInterval: g.slowPollInterval,
	}
}

// warmUpWithFastPoll monitors connection during warm-up period.
func (g *StabilityGate) warmUpWithFastPoll(ctx context.Context, bitrate int64, probeStart time.Time) *ProbeResult {
	fastTicker := time.NewTicker(g.fastPollInterval)
	defer fastTicker.Stop()

	deadline := time.Now().Add(g.config.WarmUpDuration)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return &ProbeResult{Verdict: VerdictTimeout, Reason: "context canceled during warm-up"}

		case <-fastTicker.C:
			status, err := g.seeker.Status(ctx)
			if err != nil || !status.ConnectionAlive {
				m, collectErr := g.collector.Collect(ctx)
				if collectErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to collect metrics at warm-up EOF: %v\n", collectErr)
				}
				diag := g.captureAtFailureImmediate(bitrate, m, probeStart)
				return &ProbeResult{
					Verdict:   VerdictEOF,
					Metrics:   m,
					Reason:    "connection died during warm-up",
					Artifacts: diag,
				}
			}
		}
	}

	return nil // Warm-up completed successfully
}

// captureAtFailureImmediate captures profiles immediately on failure.
func (g *StabilityGate) captureAtFailureImmediate(bitrate int64, m StabilityMetrics, probeStart time.Time) *DiagnosticCapture {
	if g.profiler == nil || !g.profiler.IsEnabled() {
		return nil
	}

	// CRITICAL: We're racing against process termination
	// The child processes may exit within 100-500ms of EOF

	capture := g.profiler.CaptureAtFailure(bitrate, m)
	if capture != nil {
		// Log hypothesis analysis immediately
		g.logHypothesisAnalysis(capture, m, time.Since(probeStart))
	}

	return capture
}

// logHypothesisAnalysis outputs automated bottleneck detection.
func (g *StabilityGate) logHypothesisAnalysis(capture *DiagnosticCapture, m StabilityMetrics, timeToFailure time.Duration) {
	te := m.ThroughputTE

	fmt.Fprintf(os.Stderr, "\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(os.Stderr, "║  FAILURE ANALYSIS at %d Mb/s (after %.1fs)                   \n",
		capture.TriggerBitrate/1_000_000, timeToFailure.Seconds())
	fmt.Fprintf(os.Stderr, "╠══════════════════════════════════════════════════════════════╣\n")
	fmt.Fprintf(os.Stderr, "║  Throughput Efficiency: %.1f%%                                \n", te*100)
	fmt.Fprintf(os.Stderr, "║  Gap Rate: %.3f%%   NAK Rate: %.3f%%   RTT: %.2fms           \n",
		m.GapRate*100, m.NAKRate*100, m.RTTMs)

	// ═══════════════════════════════════════════════════════════════════
	// NEW: Bottleneck Detection from client-seeker instrumentation
	// ═══════════════════════════════════════════════════════════════════
	if m.BottleneckType != "" {
		fmt.Fprintf(os.Stderr, "╠══════════════════════════════════════════════════════════════╣\n")
		fmt.Fprintf(os.Stderr, "║  BOTTLENECK DETECTION: %s\n", m.BottleneckType)
		fmt.Fprintf(os.Stderr, "║  Reason: %s\n", m.BottleneckReason)
		fmt.Fprintf(os.Stderr, "║  Generator Efficiency: %.1f%%\n", m.GeneratorEfficiency*100)
		fmt.Fprintf(os.Stderr, "║  TokenBucket: wait=%.3fs spin=%.3fs blocked=%d mode=%d\n",
			m.TokenBucketWaitSec, m.TokenBucketSpinSec, m.TokenBucketBlocked, m.TokenBucketMode)
		fmt.Fprintf(os.Stderr, "║  SRT Write: time=%.3fs blocked=%d errors=%d\n",
			m.SRTWriteSec, m.SRTWriteBlocked, m.SRTWriteErrors)

		// Specific recommendations based on bottleneck type
		switch m.BottleneckType {
		case "TOOL-LIMITED":
			fmt.Fprintf(os.Stderr, "╠══════════════════════════════════════════════════════════════╣\n")
			fmt.Fprintf(os.Stderr, "║  ⚠️  TOOL BOTTLENECK - client-seeker is the limit            \n")
			fmt.Fprintf(os.Stderr, "║  → Switch TokenBucket from RefillHybrid to RefillSleep      \n")
			fmt.Fprintf(os.Stderr, "║  → The SRT library may be capable of higher throughput      \n")
		case "LIBRARY-LIMITED":
			fmt.Fprintf(os.Stderr, "╠══════════════════════════════════════════════════════════════╣\n")
			fmt.Fprintf(os.Stderr, "║  🔴 LIBRARY BOTTLENECK - SRT is the limit                    \n")
			fmt.Fprintf(os.Stderr, "║  → Profile server CPU, check EventLoop starvation           \n")
			fmt.Fprintf(os.Stderr, "║  → Check control ring overflow, increase ring sizes         \n")
		}
	}

	fmt.Fprintf(os.Stderr, "╠══════════════════════════════════════════════════════════════╣\n")

	// Automated hypothesis flagging (original logic)
	hypothesis := DefaultHypothesisModel()

	switch {
	case te < hypothesis.H2TEThreshold && m.GapRate < hypothesis.H3GapRateThreshold:
		fmt.Fprintf(os.Stderr, "║  🔴 HYPOTHESIS 2: EventLoop Starvation                       \n")
		fmt.Fprintf(os.Stderr, "║     Low throughput without packet loss                       \n")
		fmt.Fprintf(os.Stderr, "║     → Check cpu.pprof for deliverReadyPacketsEventLoop()    \n")
	case m.NAKRate > hypothesis.H1NAKRateThreshold && timeToFailure < 5*time.Second:
		fmt.Fprintf(os.Stderr, "║  🔴 HYPOTHESIS 1: Flow Control Window Exhaustion             \n")
		fmt.Fprintf(os.Stderr, "║     NAK spike immediately before EOF                         \n")
		fmt.Fprintf(os.Stderr, "║     → Increase FC, check ACK processing latency             \n")
	case m.RTTVarianceMs > hypothesis.H5RTTVarianceThreshold:
		fmt.Fprintf(os.Stderr, "║  🔴 HYPOTHESIS 5: GC/Memory Pressure                         \n")
		fmt.Fprintf(os.Stderr, "║     High RTT variance indicates scheduling jitter           \n")
		fmt.Fprintf(os.Stderr, "║     → Check heap.pprof, run with GODEBUG=gctrace=1          \n")
	case te < hypothesis.H2TEThreshold && m.GapRate > hypothesis.H3GapRateThreshold:
		fmt.Fprintf(os.Stderr, "║  🔴 HYPOTHESIS 4: io_uring Backpressure                      \n")
		fmt.Fprintf(os.Stderr, "║     Low throughput WITH packet loss                          \n")
		fmt.Fprintf(os.Stderr, "║     → Check IoUringRecvRingSize, completion queue depth     \n")
	default:
		fmt.Fprintf(os.Stderr, "║  🟡 UNKNOWN: Review profiles for additional clues           \n")
	}

	fmt.Fprintf(os.Stderr, "╠══════════════════════════════════════════════════════════════╣\n")
	if g.profiler != nil {
		fmt.Fprintf(os.Stderr, "║  Profiles saved to: %s\n", g.profiler.outputDir)
	}
	fmt.Fprintf(os.Stderr, "╚══════════════════════════════════════════════════════════════╝\n\n")
}

// isCritical checks if metrics exceed critical thresholds.
func (g *StabilityGate) isCritical(m StabilityMetrics) bool {
	if m.GapRate > g.config.CriticalGapRate {
		return true
	}
	if m.NAKRate > g.config.CriticalNAKRate {
		return true
	}
	if !m.ConnectionAlive {
		return true
	}
	return false
}

// evaluateSamples determines stability from collected samples.
func (g *StabilityGate) evaluateSamples(samples []StabilityMetrics) (ProbeVerdict, string) {
	if len(samples) == 0 {
		return VerdictUnstable, "no samples collected"
	}

	// Calculate averages
	var totalGap, totalNAK, totalRTT float64
	var totalTE float64
	unstableCount := 0

	for i := range samples {
		s := &samples[i]
		totalGap += s.GapRate
		totalNAK += s.NAKRate
		totalRTT += s.RTTMs
		totalTE += s.ThroughputTE

		// Count unstable samples
		if s.GapRate > g.config.MaxGapRate ||
			s.NAKRate > g.config.MaxNAKRate ||
			s.RTTMs > g.config.MaxRTTMs ||
			s.ThroughputTE < g.config.MinThroughput {
			unstableCount++
		}
	}

	n := float64(len(samples))
	avgGap := totalGap / n
	avgNAK := totalNAK / n
	avgRTT := totalRTT / n
	avgTE := totalTE / n

	// Stability criteria: majority of samples must be stable
	unstableRatio := float64(unstableCount) / n
	if unstableRatio > 0.3 { // More than 30% unstable
		return VerdictUnstable, fmt.Sprintf("%.0f%% of samples unstable (gap=%.3f%%, nak=%.3f%%, rtt=%.1fms, te=%.1f%%)",
			unstableRatio*100, avgGap*100, avgNAK*100, avgRTT, avgTE*100)
	}

	// Check average thresholds
	if avgGap > g.config.MaxGapRate {
		return VerdictUnstable, fmt.Sprintf("avg gap rate %.3f%% > %.3f%%", avgGap*100, g.config.MaxGapRate*100)
	}
	if avgNAK > g.config.MaxNAKRate {
		return VerdictUnstable, fmt.Sprintf("avg NAK rate %.3f%% > %.3f%%", avgNAK*100, g.config.MaxNAKRate*100)
	}
	if avgRTT > g.config.MaxRTTMs {
		return VerdictUnstable, fmt.Sprintf("avg RTT %.1fms > %.1fms", avgRTT, g.config.MaxRTTMs)
	}
	if avgTE < g.config.MinThroughput {
		return VerdictUnstable, fmt.Sprintf("avg throughput %.1f%% < %.1f%%", avgTE*100, g.config.MinThroughput*100)
	}

	return VerdictStable, fmt.Sprintf("stable: gap=%.3f%%, nak=%.3f%%, rtt=%.1fms, te=%.1f%%",
		avgGap*100, avgNAK*100, avgRTT, avgTE*100)
}

// aggregate combines multiple samples into a single metrics struct.
func (g *StabilityGate) aggregate(samples []StabilityMetrics) StabilityMetrics {
	if len(samples) == 0 {
		return StabilityMetrics{}
	}

	var m StabilityMetrics
	var totalGap, totalNAK, totalRTT, totalRTTVar, totalTE float64

	for i := range samples {
		s := &samples[i]
		totalGap += s.GapRate
		totalNAK += s.NAKRate
		totalRTT += s.RTTMs
		totalRTTVar += s.RTTVarianceMs
		totalTE += s.ThroughputTE
	}

	n := float64(len(samples))
	m.GapRate = totalGap / n
	m.NAKRate = totalNAK / n
	m.RTTMs = totalRTT / n
	m.RTTVarianceMs = totalRTTVar / n
	m.ThroughputTE = totalTE / n

	// Use last sample for point-in-time values
	last := samples[len(samples)-1]
	m.TargetBitrate = last.TargetBitrate
	m.ActualBitrate = last.ActualBitrate
	m.PacketsSent = last.PacketsSent
	m.BytesSent = last.BytesSent
	m.ConnectionAlive = last.ConnectionAlive
	m.Timestamp = last.Timestamp

	return m
}
