package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// SearchLoop implements the outer loop of the performance search.
// It manages probe lifecycle, bounds updates, and enforces monotonic invariants.
//
// Key principle: SearchLoop NEVER interprets metrics. It only:
// 1. Manages the probe lifecycle (ramp → record probeStart → call Gate)
// 2. Updates bounds based on Gate's verdict
// 3. Enforces monotonic invariants
type SearchLoop struct {
	config  SearchConfig
	timing  TimingModel
	gate    Gate
	seeker  Seeker
	verbose bool

	// Bounds (monotonic invariants)
	low  int64 // Last PROVEN stable bitrate
	high int64 // Last PROVEN unstable bitrate

	// History
	probes    []ProbeRecord
	startTime time.Time

	// Last stable point (for ramping from known-good state)
	lastStableBitrate int64

	// Failure tracking for ceiling proof
	consecutiveFailures int
	lastFailedBitrate   int64

	// Status reporting
	statusInterval time.Duration
	currentBitrate int64  // Current bitrate being tested
	currentPhase   string // Current phase (ramping, probing, etc.)
	stopStatusChan chan struct{}

	// CPU monitoring (optional)
	cpuMonitor *CPUMonitor
}

// NewSearchLoop creates a new search loop.
func NewSearchLoop(config SearchConfig, timing TimingModel, gate Gate, seeker Seeker) *SearchLoop {
	return &SearchLoop{
		config:            config,
		timing:            timing,
		gate:              gate,
		seeker:            seeker,
		low:               0,
		high:              config.MaxBitrate,
		lastStableBitrate: config.InitialBitrate,
		stopStatusChan:    make(chan struct{}),
	}
}

// SetVerbose enables verbose output.
func (s *SearchLoop) SetVerbose(v bool) {
	s.verbose = v
}

// SetStatusInterval sets the interval for status updates (0 disables).
func (s *SearchLoop) SetStatusInterval(interval time.Duration) {
	s.statusInterval = interval
}

// SetCPUMonitor sets the CPU monitor for status output.
func (s *SearchLoop) SetCPUMonitor(m *CPUMonitor) {
	s.cpuMonitor = m
}

// startStatusReporter starts a background goroutine that prints status updates.
func (s *SearchLoop) startStatusReporter(ctx context.Context) {
	if s.statusInterval <= 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(s.statusInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopStatusChan:
				return
			case <-ticker.C:
				elapsed := time.Since(s.startTime).Round(time.Second)
				phase := s.currentPhase
				if phase == "" {
					phase = "initializing"
				}

				// Base status line
				status := fmt.Sprintf("\r[%v] %s @ %s | bounds: [%s, %s] | probes: %d",
					elapsed,
					phase,
					FormatBitrate(s.currentBitrate),
					FormatBitrate(s.low),
					FormatBitrate(s.high),
					len(s.probes))

				// Add CPU info if available
				if s.cpuMonitor != nil {
					cpuStatus := s.cpuMonitor.FormatStatus()
					status += " | " + cpuStatus
				}

				fmt.Printf("%s   ", status)
			}
		}
	}()
}

// stopStatusReporter stops the status reporter goroutine.
func (s *SearchLoop) stopStatusReporter() {
	if s.statusInterval > 0 {
		close(s.stopStatusChan)
		fmt.Println() // New line after status updates
	}
}

// Run executes the search algorithm.
func (s *SearchLoop) Run(ctx context.Context) SearchResult {
	s.startTime = time.Now()
	current := s.config.InitialBitrate
	probeNumber := 0

	// Start status reporter
	s.currentBitrate = current
	s.currentPhase = "starting"
	s.startStatusReporter(ctx)
	defer s.stopStatusReporter()

	s.log("Starting search: initial=%s, max=%s, precision=%s",
		FormatBitrate(current), FormatBitrate(s.config.MaxBitrate), FormatBitrate(s.config.Precision))

	for {
		probeNumber++
		s.currentBitrate = current

		// Check termination conditions
		if err := s.checkInvariants(); err != nil {
			return s.failWithViolation(err, probeNumber)
		}

		if s.high-s.low <= s.config.Precision {
			s.log("Converged: low=%s, high=%s, precision=%s",
				FormatBitrate(s.low), FormatBitrate(s.high), FormatBitrate(s.config.Precision))
			return s.proveCeiling(ctx, probeNumber)
		}

		if time.Since(s.startTime) > s.config.Timeout {
			s.log("Timeout after %v", time.Since(s.startTime))
			return SearchResult{
				Status:     StatusFailed,
				Ceiling:    s.low,
				Proven:     false,
				FailReason: fmt.Sprintf("timeout after %v", time.Since(s.startTime)),
				Artifacts:  s.buildArtifacts(TerminationTimeout),
			}
		}

		// === MULTI-STAGE PROBE ===
		s.log("Probe %d: target=%s, low=%s, high=%s",
			probeNumber, FormatBitrate(current), FormatBitrate(s.low), FormatBitrate(s.high))

		// Phase A: Ramp from last stable to target (if different)
		rampedFrom := s.lastStableBitrate
		if current != s.lastStableBitrate {
			s.currentPhase = "ramping"
			s.log("  Ramping from %s to %s over %v",
				FormatBitrate(s.lastStableBitrate), FormatBitrate(current), s.timing.RampDuration)
			if err := s.rampToTarget(ctx, s.lastStableBitrate, current); err != nil {
				if ctx.Err() != nil {
					return SearchResult{
						Status:     StatusAborted,
						Ceiling:    s.low,
						FailReason: "canceled during ramp",
						Artifacts:  s.buildArtifacts(TerminationCancelled),
					}
				}
				return SearchResult{
					Status:     StatusFailed,
					Ceiling:    s.low,
					FailReason: fmt.Sprintf("ramp failed: %v", err),
					Artifacts:  s.buildArtifacts(TerminationError),
				}
			}
		}

		// Phase B: Record probe start time AFTER ramp completes
		probeStart := time.Now()

		// Phase C: Run stability gate probe
		s.currentPhase = "probing"
		result := s.gate.Probe(ctx, probeStart, current)

		// Record probe
		record := ProbeRecord{
			Number:        probeNumber,
			TargetBitrate: current,
			Stable:        result.Verdict == VerdictStable,
			Critical:      result.Verdict == VerdictCritical || result.Verdict == VerdictEOF,
			Duration:      result.Duration,
			Metrics:       &result.Metrics,
		}
		s.probes = append(s.probes, record)

		s.log("  Result: %s (%s)", result.Verdict, result.Reason)

		// Update phase based on result
		if result.Verdict == VerdictStable {
			s.currentPhase = "stable"
		} else {
			s.currentPhase = "unstable"
		}

		// Handle cancellation
		if result.Verdict == VerdictTimeout {
			return SearchResult{
				Status:     StatusAborted,
				Ceiling:    s.low,
				FailReason: "canceled",
				Artifacts:  s.buildArtifacts(TerminationCancelled),
			}
		}

		// Update bounds based on result
		if result.Verdict == VerdictStable {
			// INVARIANT: low only increases
			if current > s.low {
				s.low = current
				s.log("  Updated low=%s", FormatBitrate(s.low))
			}
			s.lastStableBitrate = current
			s.consecutiveFailures = 0

			// Additive increase or binary search
			current = s.nextProbeUp(current)
		} else {
			// INVARIANT: high only decreases
			if current < s.high {
				s.high = current
				s.log("  Updated high=%s", FormatBitrate(s.high))
			}

			// Track consecutive failures for ceiling proof
			if current == s.lastFailedBitrate {
				s.consecutiveFailures++
			} else {
				s.consecutiveFailures = 1
				s.lastFailedBitrate = current
			}

			// Multiplicative decrease
			isCritical := result.Verdict == VerdictCritical || result.Verdict == VerdictEOF
			current = s.nextProbeDown(s.lastStableBitrate, isCritical)
		}

		// Clamp to valid range
		current = clamp(current, s.config.MinBitrate, s.config.MaxBitrate)

		// Also clamp to search bounds
		if current < s.low {
			current = s.low + s.config.Precision
		}
		if current > s.high {
			current = s.high - s.config.Precision
		}

		// Ensure we don't get stuck
		if current <= s.low || current >= s.high {
			// Binary search midpoint
			current = (s.low + s.high) / 2
		}

		_ = rampedFrom // Used for logging/recording
	}
}

// rampToTarget gradually transitions bitrate from `from` to `to`.
func (s *SearchLoop) rampToTarget(ctx context.Context, from, to int64) error {
	if from == to {
		return nil
	}

	steps := s.timing.RampSteps
	stepDuration := s.timing.RampDuration / time.Duration(steps)
	stepSize := (to - from) / int64(steps)

	current := from
	for i := 0; i < steps; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		current += stepSize
		if err := s.seeker.SetBitrate(ctx, current); err != nil {
			return fmt.Errorf("set bitrate to %s: %w", FormatBitrate(current), err)
		}

		// Send heartbeat during ramp
		if err := s.seeker.Heartbeat(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: heartbeat during ramp failed: %v\n", err)
		}

		time.Sleep(stepDuration)
	}

	// Ensure we hit exact target
	return s.seeker.SetBitrate(ctx, to)
}

// nextProbeUp determines the next bitrate to try after a stable probe.
func (s *SearchLoop) nextProbeUp(current int64) int64 {
	// If we have an upper bound from a failure, binary search
	if s.high < s.config.MaxBitrate {
		return (s.low + s.high) / 2
	}

	// Otherwise, additive increase
	return current + s.config.StepSize
}

// nextProbeDown determines the next bitrate after an unstable probe.
func (s *SearchLoop) nextProbeDown(lastStable int64, wasCritical bool) int64 {
	// Use multiplicative decrease from last stable
	factor := s.config.DecreasePercent
	if wasCritical {
		factor = 0.5 // More aggressive backoff for critical failures
	}

	// Calculate new target
	decrease := int64(float64(s.high-lastStable) * factor)
	newTarget := s.high - decrease

	// Ensure we're above last stable
	if newTarget <= lastStable {
		newTarget = lastStable + s.config.Precision
	}

	return newTarget
}

// checkInvariants verifies search loop invariants.
func (s *SearchLoop) checkInvariants() error {
	if s.low > s.high {
		return &InvariantViolation{
			Invariant:   "BOUNDS_CROSSED",
			Description: fmt.Sprintf("low(%d) > high(%d)", s.low, s.high),
			Low:         s.low,
			High:        s.high,
			ProbeCount:  len(s.probes),
		}
	}
	if s.low < 0 {
		return &InvariantViolation{
			Invariant:   "NEGATIVE_LOW",
			Description: fmt.Sprintf("low(%d) < 0", s.low),
			Low:         s.low,
			High:        s.high,
			ProbeCount:  len(s.probes),
		}
	}
	if s.high > s.config.MaxBitrate {
		return &InvariantViolation{
			Invariant:   "HIGH_EXCEEDS_MAX",
			Description: fmt.Sprintf("high(%d) > max(%d)", s.high, s.config.MaxBitrate),
			Low:         s.low,
			High:        s.high,
			ProbeCount:  len(s.probes),
		}
	}
	return nil
}

// proveCeiling runs extended stability tests to prove the ceiling.
func (s *SearchLoop) proveCeiling(ctx context.Context, probeNumber int) SearchResult {
	s.log("Proving ceiling at %s", FormatBitrate(s.low))

	// Run extended stability test at the ceiling
	probeStart := time.Now()
	result := s.gate.Probe(ctx, probeStart, s.low)

	if result.Verdict != VerdictStable {
		// Ceiling not proven, back off
		s.log("Ceiling proof failed: %s", result.Reason)
		return SearchResult{
			Status:     StatusSuccess,
			Ceiling:    s.low - s.config.Precision,
			Proven:     false,
			Metrics:    &result.Metrics,
			FailReason: "ceiling proof failed",
			Artifacts:  s.buildArtifacts(TerminationSuccess),
		}
	}

	// Ceiling proven
	s.log("Ceiling proven: %s", FormatBitrate(s.low))
	return SearchResult{
		Status:  StatusSuccess,
		Ceiling: s.low,
		Proven:  true,
		ProofData: &CeilingProofData{
			StableRuns:    1,
			ProofDuration: result.Duration,
			FinalMetrics:  result.Metrics,
		},
		Metrics:   &result.Metrics,
		Artifacts: s.buildArtifacts(TerminationSuccess),
	}
}

// failWithViolation creates a failure result from an invariant violation.
func (s *SearchLoop) failWithViolation(err error, probeNumber int) SearchResult {
	violation, ok := err.(*InvariantViolation)
	if !ok {
		violation = &InvariantViolation{
			Invariant:   "UNKNOWN",
			Description: err.Error(),
			ProbeCount:  probeNumber,
		}
	}

	artifacts := s.buildArtifacts(TerminationInvariant)
	artifacts.Violation = violation

	return SearchResult{
		Status:     StatusFailed,
		Ceiling:    s.low,
		Proven:     false,
		FailReason: fmt.Sprintf("INVARIANT VIOLATION [%s]: %s", violation.Invariant, violation.Description),
		Artifacts:  artifacts,
	}
}

// buildArtifacts creates failure artifacts for the result.
func (s *SearchLoop) buildArtifacts(reason TerminationReason) FailureArtifacts {
	return FailureArtifacts{
		ConfigSnapshot:     Config{Search: s.config},
		TimingSnapshot:     s.timing,
		HypothesisSnapshot: DefaultHypothesisModel(),
		TerminationReason:  reason,
		Probes:             s.probes,
		LastNProbes:        len(s.probes),
	}
}

// log outputs verbose information.
func (s *SearchLoop) log(format string, args ...interface{}) {
	if s.verbose {
		fmt.Printf("[SearchLoop] "+format+"\n", args...)
	}
}

// clamp restricts a value to a range.
func clamp(v, minVal, maxVal int64) int64 {
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

// Error implements the error interface for InvariantViolation.
func (iv *InvariantViolation) Error() string {
	return fmt.Sprintf("INVARIANT VIOLATION [%s]: %s", iv.Invariant, iv.Description)
}
