package main

import (
	"fmt"
	"strings"
	"time"
)

// TimingModel is the single source of truth for all timing parameters.
// It owns every time constant and derived constant, ensuring consistency
// across SearchLoop, Gate, and Seeker.
//
// This struct is validated ONCE in main() via ValidateContracts().
// All components receive a TimingModel (not raw durations) to prevent
// timing mismatches.
type TimingModel struct {
	// === Orchestrator → Seeker Communication ===
	HeartbeatInterval  time.Duration // How often to send heartbeats (default: 1s)
	WatchdogTimeout    time.Duration // Seeker's watchdog timeout (default: 5s)
	RampDuration       time.Duration // Total time to ramp to target (default: 2s)
	RampUpdateInterval time.Duration // Interval between SetBitrate calls during ramp (default: 100ms)

	// === Metrics Collection ===
	SampleInterval   time.Duration // Prometheus scrape interval (default: 500ms)
	FastPollInterval time.Duration // Status socket poll for EOF detection (default: 50ms)

	// === Stability Evaluation ===
	WarmUpDuration  time.Duration // Ignore metrics after bitrate change (default: 2s)
	StabilityWindow time.Duration // Window for stability evaluation (default: 5s)

	// === Search Control ===
	Precision     int64         // Minimum bitrate step (default: 5 Mb/s)
	SearchTimeout time.Duration // Maximum total search time (default: 10min)

	// === Derived Values (computed from above) ===
	MinProbeDuration time.Duration // WarmUpDuration + StabilityWindow
	RequiredSamples  int           // StabilityWindow / SampleInterval
	RampSteps        int           // RampDuration / RampUpdateInterval
	ProofDuration    time.Duration // 2 × StabilityWindow (for ceiling proof)
}

// DefaultTimingModel returns sensible defaults for performance testing.
func DefaultTimingModel() TimingModel {
	tm := TimingModel{
		// Orchestrator → Seeker
		HeartbeatInterval:  1 * time.Second,
		WatchdogTimeout:    5 * time.Second,
		RampDuration:       2 * time.Second,
		RampUpdateInterval: 100 * time.Millisecond,

		// Metrics
		SampleInterval:   500 * time.Millisecond,
		FastPollInterval: 50 * time.Millisecond,

		// Stability
		WarmUpDuration:  2 * time.Second,
		StabilityWindow: 5 * time.Second,

		// Search
		Precision:     5_000_000, // 5 Mb/s
		SearchTimeout: 10 * time.Minute,
	}

	// Compute derived values
	tm.computeDerived()

	return tm
}

// computeDerived calculates derived values from primary parameters.
func (tm *TimingModel) computeDerived() {
	tm.MinProbeDuration = tm.WarmUpDuration + tm.StabilityWindow
	tm.RequiredSamples = int(tm.StabilityWindow / tm.SampleInterval)
	tm.RampSteps = int(tm.RampDuration / tm.RampUpdateInterval)
	tm.ProofDuration = 2 * tm.StabilityWindow
}

// ValidateContracts returns error if any timing contract is violated.
// Called ONCE in main() - fail fast on misconfiguration.
func (tm *TimingModel) ValidateContracts() error {
	var errs []string

	// INVARIANT 1: WarmUp > 2 × RampUpdateInterval
	// Why: Ensures warm-up window fully covers ramp jitter
	if tm.WarmUpDuration <= 2*tm.RampUpdateInterval {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [WARMUP_TOO_SHORT]: WarmUp(%v) must be > 2×RampUpdateInterval(%v)",
			tm.WarmUpDuration, tm.RampUpdateInterval))
	}

	// INVARIANT 2: StabilityWindow > 3 × SampleInterval
	// Why: Need at least 3 samples for meaningful stability evaluation
	if tm.StabilityWindow <= 3*tm.SampleInterval {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [STABILITY_TOO_SHORT]: StabilityWindow(%v) must be > 3×SampleInterval(%v)",
			tm.StabilityWindow, tm.SampleInterval))
	}

	// INVARIANT 3: HeartbeatInterval < WatchdogTimeout/2
	// Why: Must send at least 2 heartbeats before timeout triggers
	if tm.HeartbeatInterval >= tm.WatchdogTimeout/2 {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [HEARTBEAT_TOO_SLOW]: HeartbeatInterval(%v) must be < WatchdogTimeout/2(%v)",
			tm.HeartbeatInterval, tm.WatchdogTimeout/2))
	}

	// INVARIANT 4: FastPollInterval < SampleInterval
	// Why: EOF detection must be faster than metrics collection
	if tm.FastPollInterval >= tm.SampleInterval {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [FAST_POLL_TOO_SLOW]: FastPollInterval(%v) must be < SampleInterval(%v)",
			tm.FastPollInterval, tm.SampleInterval))
	}

	// INVARIANT 5: RequiredSamples >= 3
	// Why: Need minimum samples for statistical validity
	if tm.RequiredSamples < 3 {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [TOO_FEW_SAMPLES]: RequiredSamples(%d) must be >= 3",
			tm.RequiredSamples))
	}

	// INVARIANT 6: Precision > 0
	// Why: Must have positive step size
	if tm.Precision <= 0 {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [INVALID_PRECISION]: Precision(%d) must be > 0",
			tm.Precision))
	}

	// INVARIANT 7: SearchTimeout > MinProbeDuration
	// Why: Must have time for at least one probe
	if tm.SearchTimeout <= tm.MinProbeDuration {
		errs = append(errs, fmt.Sprintf(
			"CONTRACT VIOLATION [TIMEOUT_TOO_SHORT]: SearchTimeout(%v) must be > MinProbeDuration(%v)",
			tm.SearchTimeout, tm.MinProbeDuration))
	}

	if len(errs) > 0 {
		return fmt.Errorf("timing contract violations:\n  %s", strings.Join(errs, "\n  "))
	}

	return nil
}

// String returns a human-readable summary of the timing model.
func (tm *TimingModel) String() string {
	return fmt.Sprintf(`TimingModel:
  Heartbeat: %v (watchdog: %v)
  Ramp: %v (%d steps @ %v)
  Sample: %v (fast poll: %v)
  WarmUp: %v, Stability: %v
  MinProbe: %v, RequiredSamples: %d
  Precision: %d bps, Timeout: %v`,
		tm.HeartbeatInterval, tm.WatchdogTimeout,
		tm.RampDuration, tm.RampSteps, tm.RampUpdateInterval,
		tm.SampleInterval, tm.FastPollInterval,
		tm.WarmUpDuration, tm.StabilityWindow,
		tm.MinProbeDuration, tm.RequiredSamples,
		tm.Precision, tm.SearchTimeout)
}

// Clone returns a deep copy of the TimingModel.
func (tm *TimingModel) Clone() TimingModel {
	clone := *tm
	return clone
}

// WithWarmUp returns a new TimingModel with modified warm-up duration.
// Useful for extended proof phase.
func (tm *TimingModel) WithWarmUp(d time.Duration) TimingModel {
	clone := tm.Clone()
	clone.WarmUpDuration = d
	clone.computeDerived()
	return clone
}

// WithStabilityWindow returns a new TimingModel with modified stability window.
// Useful for extended proof phase.
func (tm *TimingModel) WithStabilityWindow(d time.Duration) TimingModel {
	clone := tm.Clone()
	clone.StabilityWindow = d
	clone.computeDerived()
	return clone
}
