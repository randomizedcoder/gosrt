# Performance Testing Implementation Plan

> **Status**: Pre-Implementation Review
> **Parent Documents**:
> - [Client-Seeker Design](client_seeker_design.md)
> - [Performance Test Orchestrator Design](performance_test_orchestrator_design.md)
> - [Performance Maximization](performance_maximization_500mbps.md)

---

## Overview

This document provides a detailed, step-by-step implementation plan for the performance
testing system. Each phase includes:

- **Files to create** with estimated line counts
- **Functions to implement** in order
- **Compilation checkpoints** (✓ Build)
- **Test verification** (✓ Test)
- **Milestones** to validate progress
- **Definition of Done** with output artifacts and failure examples

---

## ⚠️ Timing & Control Contract (READ FIRST)

This section defines **all timing parameters, ownership rules, and guarantees**. Every
component MUST use the centralized `TimingModel` - no raw durations scattered through code.

### The TimingModel (Single Source of Truth)

```go
// contrib/performance/timing.go (~100 lines)
// EVERY time constant lives here. No exceptions.

type TimingModel struct {
    // ═══════════════════════════════════════════════════════════════════════
    // PRIMARY PARAMETERS (set by configuration)
    // ═══════════════════════════════════════════════════════════════════════

    // Seeker Control
    HeartbeatInterval   time.Duration  // How often orchestrator sends heartbeat (default: 1s)
    WatchdogTimeout     time.Duration  // Seeker stops if no heartbeat (default: 5s)

    // Ramping
    RampDuration        time.Duration  // Total time to ramp to new bitrate (default: 2s)
    RampUpdateInterval  time.Duration  // Frequency of set_bitrate during ramp (default: 100ms)

    // Stability Evaluation
    SampleInterval      time.Duration  // Prometheus scrape frequency (default: 500ms)
    WarmUpDuration      time.Duration  // Ignore metrics after bitrate change (default: 2s)
    StabilityWindow     time.Duration  // Duration to evaluate stability (default: 5s)

    // EOF Detection
    FastPollInterval    time.Duration  // Seeker status poll frequency (default: 50ms)

    // Search Bounds
    Precision           int64          // Search stops when high-low < precision (default: 5Mbps)
    SearchTimeout       time.Duration  // Max total search time (default: 10min)

    // ═══════════════════════════════════════════════════════════════════════
    // DERIVED PARAMETERS (computed, not configurable)
    // ═══════════════════════════════════════════════════════════════════════

    MinProbeDuration    time.Duration  // = WarmUpDuration + StabilityWindow
    RequiredSamples     int            // = StabilityWindow / SampleInterval
    RampSteps           int            // = RampDuration / RampUpdateInterval
    ProofDuration       time.Duration  // = 2 * StabilityWindow (extended verification)
}

// Compute derived values and validate contracts
func NewTimingModel(cfg TimingConfig) (*TimingModel, error) {
    tm := &TimingModel{
        HeartbeatInterval:  cfg.HeartbeatInterval,
        WatchdogTimeout:    cfg.WatchdogTimeout,
        RampDuration:       cfg.RampDuration,
        RampUpdateInterval: cfg.RampUpdateInterval,
        SampleInterval:     cfg.SampleInterval,
        WarmUpDuration:     cfg.WarmUpDuration,
        StabilityWindow:    cfg.StabilityWindow,
        FastPollInterval:   cfg.FastPollInterval,
        Precision:          cfg.Precision,
        SearchTimeout:      cfg.SearchTimeout,
    }

    // Compute derived
    tm.MinProbeDuration = tm.WarmUpDuration + tm.StabilityWindow
    tm.RequiredSamples = int(tm.StabilityWindow / tm.SampleInterval)
    tm.RampSteps = int(tm.RampDuration / tm.RampUpdateInterval)
    tm.ProofDuration = 2 * tm.StabilityWindow

    // Validate contracts
    if err := tm.ValidateContracts(); err != nil {
        return nil, err
    }

    return tm, nil
}
```

### Contract Invariants (Enforced at Startup)

```go
// ValidateContracts returns error if any timing contract is violated.
// Called ONCE in main() - fail fast on misconfiguration.
func (tm *TimingModel) ValidateContracts() error {
    var errs []string

    // INVARIANT 1: WarmUp > 2 × RampUpdateInterval
    // Why: Ensures warm-up window fully covers ramp jitter
    if tm.WarmUpDuration <= 2*tm.RampUpdateInterval {
        errs = append(errs, fmt.Sprintf(
            "CONTRACT VIOLATION: WarmUp(%v) must be > 2×RampUpdateInterval(%v)",
            tm.WarmUpDuration, tm.RampUpdateInterval))
    }

    // INVARIANT 2: StabilityWindow > 3 × SampleInterval
    // Why: Need at least 3 samples for meaningful stability evaluation
    if tm.StabilityWindow <= 3*tm.SampleInterval {
        errs = append(errs, fmt.Sprintf(
            "CONTRACT VIOLATION: StabilityWindow(%v) must be > 3×SampleInterval(%v)",
            tm.StabilityWindow, tm.SampleInterval))
    }

    // INVARIANT 3: HeartbeatInterval < WatchdogTimeout/2
    // Why: Must send at least 2 heartbeats before timeout triggers
    if tm.HeartbeatInterval >= tm.WatchdogTimeout/2 {
        errs = append(errs, fmt.Sprintf(
            "CONTRACT VIOLATION: HeartbeatInterval(%v) must be < WatchdogTimeout/2(%v)",
            tm.HeartbeatInterval, tm.WatchdogTimeout/2))
    }

    // INVARIANT 4: RampDuration > WarmUpDuration
    // Why: Ramp must complete before warm-up ends (warm-up ignores ramp noise)
    // UPDATE: Actually WarmUp should START after ramp completes (see probe lifecycle)

    // INVARIANT 5: FastPollInterval < SampleInterval
    // Why: EOF detection must be faster than metrics collection
    if tm.FastPollInterval >= tm.SampleInterval {
        errs = append(errs, fmt.Sprintf(
            "CONTRACT VIOLATION: FastPollInterval(%v) must be < SampleInterval(%v)",
            tm.FastPollInterval, tm.SampleInterval))
    }

    // INVARIANT 6: RequiredSamples >= 3
    // Why: Need minimum samples for statistical validity
    if tm.RequiredSamples < 3 {
        errs = append(errs, fmt.Sprintf(
            "CONTRACT VIOLATION: RequiredSamples(%d) must be >= 3",
            tm.RequiredSamples))
    }

    if len(errs) > 0 {
        return fmt.Errorf("timing contract violations:\n  %s", strings.Join(errs, "\n  "))
    }

    return nil
}
```

### Default Timing Values

| Parameter | Default | Rationale |
|-----------|---------|-----------|
| `HeartbeatInterval` | 1s | Balance between overhead and responsiveness |
| `WatchdogTimeout` | 5s | Allow GC pauses, network blips |
| `RampDuration` | 2s | SRT congestion control needs time to adapt |
| `RampUpdateInterval` | 100ms | 20 steps in ramp (smooth transition) |
| `SampleInterval` | 500ms | Prometheus convention, sufficient resolution |
| `WarmUpDuration` | 2s | Must exceed 2×RampUpdateInterval (200ms) ✓ |
| `StabilityWindow` | 5s | 10 samples at 500ms = solid statistical basis |
| `FastPollInterval` | 50ms | 10× faster than Prometheus for EOF detection |
| `Precision` | 5 Mb/s | Sufficient for 4K ProRes target |
| `SearchTimeout` | 10min | Worst case: ~40 probes at 15s each |

**Derived Values** (with defaults):
- `MinProbeDuration` = 2s + 5s = **7s**
- `RequiredSamples` = 5s / 500ms = **10**
- `RampSteps` = 2s / 100ms = **20**
- `ProofDuration` = 2 × 5s = **10s**

---

### Probe Lifecycle (Explicit Sequence)

**The SearchLoop owns the probe lifecycle. The Gate ONLY evaluates stability.**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           PROBE LIFECYCLE                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. RAMP PHASE (SearchLoop owns)                                            │
│     ├─ seeker.SetBitrate(step1)  ─┐                                         │
│     ├─ sleep(RampUpdateInterval)  │  × RampSteps                            │
│     ├─ seeker.SetBitrate(step2)  ─┤  (default: 20 × 100ms = 2s)            │
│     ├─ ...                        │                                         │
│     └─ seeker.SetBitrate(target) ─┘                                         │
│                                                                              │
│  2. PROBE START (SearchLoop records timestamp)                              │
│     └─ probeStart := time.Now()  ← THIS is when the probe officially starts │
│                                                                              │
│  3. STABILITY EVALUATION (Gate owns)                                        │
│     └─ result := gate.Probe(ctx, probeStart, targetBitrate)                 │
│         ├─ Warm-up: ignore metrics for WarmUpDuration after probeStart      │
│         ├─ Evaluate: collect samples for StabilityWindow                    │
│         └─ Return: Stable/Unstable/Critical verdict                         │
│                                                                              │
│  4. BOUND UPDATE (SearchLoop owns)                                          │
│     └─ Update low/high based on Gate's verdict                              │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Key Rule**: `probeStart` is set AFTER ramp completes. The Gate's warm-up starts from
`probeStart`, not from when ramping began. This eliminates the "when did the probe start?"
ambiguity.

---

### Probe Timeline Diagram (Visual Ownership)

```
Time ──────────────────────────────────────────────────────────────────────────────▶

│◄──────── RampDuration (2s) ────────▶│◄─ WarmUp (2s) ─▶│◄── StabilityWindow (5s) ──▶│
│                                     │                 │                            │
│  SearchLoop: ramp bitrate           │     Gate: ignore samples                     │
│  ├─ SetBitrate(step1)               │     (noise from ramp settling)               │
│  ├─ SetBitrate(step2)               │                 │     Gate: evaluate samples │
│  ├─ ...                             │                 │     ├─ Sample 1            │
│  └─ SetBitrate(target)              │                 │     ├─ Sample 2            │
│                                     │                 │     ├─ ...                 │
│                                     │                 │     └─ Sample N            │
│                                     │                 │                            │
│                                probeStart             │                         verdict
│                                     │                 │                            │
├─────────────────────────────────────┼─────────────────┼────────────────────────────┤
│                                     │                 │                            │
│         SearchLoop owns             │           Gate owns (all timing)             │
│         (ramping only)              │           (warm-up + evaluation)             │
│                                     │                 │                            │
└─────────────────────────────────────┴─────────────────┴────────────────────────────┘

                                      │◄──────────── MinProbeDuration (7s) ──────────▶│
```

**Ownership Rules** (explicit):

| Phase | Owner | Timing Source | Actions |
|-------|-------|---------------|---------|
| Ramp | SearchLoop | `timing.RampDuration`, `timing.RampUpdateInterval` | SetBitrate calls |
| Warm-up | Gate | `timing.WarmUpDuration` | Ignore samples, poll for EOF |
| Evaluation | Gate | `timing.StabilityWindow`, `timing.SampleInterval` | Collect & evaluate |
| Verdict | Gate | — | Return `ProbeResult` |
| Bound Update | SearchLoop | — | Update `low`/`high` based on verdict |

**Only Gate decides stability using these timings. SearchLoop only orchestrates.**

---

### Ramp Step Calculation (Linear in bps)

**Clarification**: Ramp steps are **linear in bits-per-second**, not percentage.

```go
// Ramp from 'from' to 'to' in RampSteps linear increments
func (s *SearchLoop) rampToTarget(ctx context.Context, from, to int64) error {
    stepSize := (to - from) / int64(s.timing.RampSteps)  // LINEAR in bps

    current := from
    for i := 0; i < s.timing.RampSteps; i++ {
        current += stepSize  // Each step adds same number of bps
        s.seeker.SetBitrate(ctx, current)
        time.Sleep(s.timing.RampUpdateInterval)
    }

    return s.seeker.SetBitrate(ctx, to)  // Ensure exact target
}
```

**Example**: Ramp from 100 Mb/s to 200 Mb/s in 20 steps:
- Step size = (200M - 100M) / 20 = 5 Mb/s per step
- Steps: 105M, 110M, 115M, ..., 195M, 200M

**Why Linear (not Percentage)**: At low bitrates, percentage steps would be too small
to overcome SRT's congestion control hysteresis. Linear ensures consistent step size.

---

### Artifact Guarantees on Failure

**On ANY failure (invariant violation, timeout, EOF, crash), you ALWAYS get:**

```go
// FailureArtifacts is included in every SearchResult, even on failure
type FailureArtifacts struct {
    // Always present
    ConfigSnapshot     SearchConfig       `json:"config_snapshot"`      // Exact config used
    TimingSnapshot     TimingModel        `json:"timing_snapshot"`      // Timing parameters
    HypothesisSnapshot HypothesisModel    `json:"hypothesis_thresholds"` // Threshold values
    TerminationReason  TerminationReason  `json:"termination_reason"`   // Why it ended

    // Probe history (always present, may be partial)
    Probes             []ProbeRecord      `json:"probes"`               // All completed probes
    LastNProbes        int                `json:"last_n_probes"`        // How many included

    // Metrics (present if any samples collected)
    LastSampleWindow   []StabilityMetrics `json:"last_sample_window,omitempty"`
    FinalMetrics       *StabilityMetrics  `json:"final_metrics,omitempty"`

    // Diagnostics (present if profiling enabled)
    ProfilePaths       []string           `json:"profile_paths,omitempty"` // cpu.pprof, heap.pprof, etc.

    // Invariant violation (present if that's why we failed)
    Violation          *InvariantViolation `json:"violation,omitempty"`
}

// SearchResult ALWAYS includes artifacts
type SearchResult struct {
    Status      SearchStatus      `json:"status"`
    Ceiling     int64             `json:"ceiling"`
    Proven      bool              `json:"proven"`
    ProofData   CeilingProofData  `json:"proof_data,omitempty"`
    Metrics     StabilityMetrics  `json:"metrics,omitempty"`

    // ALWAYS present, even on failure
    Artifacts   FailureArtifacts  `json:"artifacts"`

    // Human-readable failure reason (if failed)
    FailReason  string            `json:"fail_reason,omitempty"`
}
```

**This eliminates "it failed and I don't know why":**

| Failure Type | You Get |
|--------------|---------|
| Invariant Violation | Config + all probes + violation struct with exact bounds |
| Timeout | Config + all probes + last metrics sample |
| EOF | Config + probes + last metrics + profile paths |
| User Cancel | Config + probes completed so far |
| Critical | Config + probes + metrics + profiles |

**JSON Output Example** (on failure):

```json
{
  "status": "failed",
  "fail_reason": "INVARIANT VIOLATION [BOUNDS_CROSSED]: low(350000000) >= high(350000000)",
  "ceiling": 0,
  "proven": false,
  "artifacts": {
    "config_snapshot": {
      "initial_bitrate": 100000000,
      "max_bitrate": 1000000000,
      "precision": 5000000,
      "step_size": 20000000
    },
    "timing_snapshot": {
      "warm_up_duration": "2s",
      "stability_window": "5s",
      "ramp_duration": "2s"
    },
    "hypothesis_thresholds": {
      "h1_nak_rate_threshold": 0.02,
      "h2_te_threshold": 0.95
    },
    "termination_reason": "critical",
    "probes": [
      {"number": 1, "target_bitrate": 100000000, "stable": true},
      {"number": 2, "target_bitrate": 200000000, "stable": true},
      {"number": 3, "target_bitrate": 300000000, "stable": true},
      {"number": 4, "target_bitrate": 400000000, "stable": false, "critical": true}
    ],
    "last_sample_window": [
      {"gap_rate": 0.001, "nak_rate": 0.025, "rtt": 15.2, "te": 0.92}
    ],
    "violation": {
      "invariant": "BOUNDS_CROSSED",
      "description": "low(350000000) >= high(350000000)",
      "low": 350000000,
      "high": 350000000,
      "probe_count": 15
    }
  }
}
```

---

### Component Responsibilities (No Overlap)

| Component | Owns | Does NOT Do |
|-----------|------|-------------|
| **SearchLoop** | Probe lifecycle, ramp cadence, bound updates | Interpret metrics |
| **Gate** | Stability verdict, EOF detection, profile trigger | Ramping, bound management |
| **Seeker** | Data generation, rate limiting | Rate decisions |
| **Reporter** | Output formatting, hypothesis flagging | Stability evaluation |

**The Golden Rule**:

> **SearchLoop NEVER interprets metrics. It only asks Gate for a verdict.**

This prevents the bug where SearchLoop checks throughput while Gate checks NAKs, leading
to contradictory "stable by Gate, unstable by SearchLoop" states.

---

### Cross-Component Interfaces (With Fakes)

Every cross-component boundary is defined as an interface. This enables:
- **Unit tests**: Run with `FakeSeeker` + `FakeMetrics` + `FakeGate` (deterministic)
- **Integration tests**: Swap in real implementations
- **Property tests**: Hammer SearchLoop with 1000s of simulated scenarios

```go
// contrib/performance/interfaces.go (~60 lines)

// Seeker controls the data generator (client-seeker process)
type Seeker interface {
    SetBitrate(ctx context.Context, bps int64) error
    Status(ctx context.Context) (SeekerStatus, error)
    Heartbeat(ctx context.Context) error
    Stop(ctx context.Context) error
}

// MetricsSource provides stability metrics (Prometheus scraping)
type MetricsSource interface {
    Sample(ctx context.Context) (StabilityMetrics, error)
}

// Gate provides stability verdicts (the inner loop oracle)
type Gate interface {
    Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult
    WithConfig(config StabilityConfig) Gate  // For extended proof phase
}

// Profiler captures diagnostic data on failure
type Profiler interface {
    CaptureAtFailure(bitrate int64, metrics StabilityMetrics) *DiagnosticCapture
}
```

**Fake Implementations** (for testing):

```go
// contrib/performance/fakes_test.go (~150 lines)

// FakeSeeker with deterministic behavior
type FakeSeeker struct {
    currentBitrate int64
    alive          bool
    failAfter      int      // Simulate EOF after N SetBitrate calls
    callCount      int
}

func (f *FakeSeeker) SetBitrate(ctx context.Context, bps int64) error {
    f.callCount++
    if f.failAfter > 0 && f.callCount >= f.failAfter {
        f.alive = false
    }
    f.currentBitrate = bps
    return nil
}

func (f *FakeSeeker) Status(ctx context.Context) (SeekerStatus, error) {
    return SeekerStatus{
        CurrentBitrate:  f.currentBitrate,
        ConnectionAlive: f.alive,
    }, nil
}

// FakeGate with configurable ceiling
type FakeGate struct {
    trueCeiling     int64    // Stable below this, unstable above
    flukeFailRate   float64  // Probability of random failure (0.0-1.0)
    callCount       int
    probeLog        []int64  // Record all probed bitrates
}

func (f *FakeGate) Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult {
    f.callCount++
    f.probeLog = append(f.probeLog, bitrate)

    // Deterministic stability based on ceiling
    stable := bitrate <= f.trueCeiling

    // Optional fluke failures for robustness testing
    if stable && f.flukeFailRate > 0 && rand.Float64() < f.flukeFailRate {
        stable = false
    }

    return ProbeResult{
        Stable:   stable,
        Critical: !stable && bitrate > f.trueCeiling*1.2, // Critical if way over
    }
}

// FakeMetrics returns configurable stability metrics
type FakeMetrics struct {
    metrics StabilityMetrics
}

func (f *FakeMetrics) Sample(ctx context.Context) (StabilityMetrics, error) {
    return f.metrics, nil
}
```

**Usage in Tests**:

```go
func TestSearchLoop_ConvergesToCeiling(t *testing.T) {
    fakeGate := &FakeGate{trueCeiling: 350_000_000}

    s := NewSearchLoop(config, fakeGate)
    result := s.Run(context.Background())

    assert.Equal(t, StatusConverged, result.Status)
    assert.InDelta(t, 350_000_000, result.Ceiling, float64(config.Precision))
}

func TestSearchLoop_HandlesFlukeFails(t *testing.T) {
    fakeGate := &FakeGate{trueCeiling: 350_000_000, flukeFailRate: 0.1}

    s := NewSearchLoop(config, fakeGate)
    result := s.Run(context.Background())

    // Should still converge despite 10% fluke failures
    assert.Equal(t, StatusConverged, result.Status)
    assert.True(t, result.Proven)  // Two-failure bracket
}
```

---

### Readiness Barrier (Explicit Criteria)

Process startup uses a single barrier with explicit pass/fail criteria:

```go
// ReadinessCriteria defines what "ready" means
type ReadinessCriteria struct {
    ServerProcessRunning  bool
    SeekerProcessRunning  bool
    ServerUDSResponding   bool   // Can scrape /metrics from server
    SeekerUDSResponding   bool   // Can scrape /metrics from seeker
    ControlUDSConnected   bool   // Can connect to seeker control socket
}

// WaitReady blocks until ALL criteria pass or timeout
// Returns error describing WHICH criteria failed
func (pm *ProcessManager) WaitReady(ctx context.Context, timeout time.Duration) error {
    deadline := time.Now().Add(timeout)

    for time.Now().Before(deadline) {
        criteria := pm.checkCriteria()

        if criteria.AllPassing() {
            return nil
        }

        time.Sleep(100 * time.Millisecond)
    }

    // Return detailed failure
    criteria := pm.checkCriteria()
    return fmt.Errorf("readiness timeout: %s", criteria.FailureDescription())
}

// FailureDescription returns human-readable failure explanation
func (c ReadinessCriteria) FailureDescription() string {
    var failures []string
    if !c.ServerProcessRunning { failures = append(failures, "server process not running") }
    if !c.SeekerProcessRunning { failures = append(failures, "seeker process not running") }
    if !c.ServerUDSResponding { failures = append(failures, "cannot scrape server /metrics") }
    if !c.SeekerUDSResponding { failures = append(failures, "cannot scrape seeker /metrics") }
    if !c.ControlUDSConnected { failures = append(failures, "cannot connect to control socket") }
    return strings.Join(failures, "; ")
}
```

---

### "Ceiling Proven" Definition (Two-Failure Bracket)

A ceiling is **proven** when:

1. `low` is stable for ≥ `MinProbeDuration`
2. `high = low + Precision` has **failed twice** with a reset-to-low in between

```go
// CeilingProofData tracks proof state (consistent naming across SearchResult)
type CeilingProofData struct {
    ProvenBitrate     int64     `json:"proven_bitrate"`       // The "low" that passed
    FailedBitrate     int64     `json:"failed_bitrate"`       // The "high" that failed
    FailureCount      int       `json:"failure_count"`        // Must be >= 2 for "proven"
    ResetBetweenFails bool      `json:"reset_between_fails"`  // Must reset to low between failures
    ProofDuration     time.Duration `json:"proof_duration"`   // How long ceiling was held
}

func (s *SearchLoop) isCeilingProven() bool {
    return s.proofData.FailureCount >= 2 &&
           s.proofData.ResetBetweenFails &&
           s.proofData.FailedBitrate <= s.proofData.ProvenBitrate + s.timing.Precision
}

// In SearchResult (aligned terminology):
// - Proven bool          → matches CeilingProofData
// - ProofData            → CeilingProofData struct
// - Artifacts.Violation  → InvariantViolation if failed
```

This prevents "one fluke failure" from being treated as the ceiling.

---

### Termination Reason (EOF Detection Contract)

**What counts as termination?** A first-class enum with explicit semantics:

```go
// TerminationReason captures why a probe ended
type TerminationReason int

const (
    TerminationNone       TerminationReason = iota  // Probe completed normally
    TerminationEOF                                   // SRT connection closed gracefully
    TerminationWatchdog                              // Seeker watchdog triggered
    TerminationTimeout                               // StabilityWindow exceeded
    TerminationUserCancel                            // Context cancelled
    TerminationCritical                              // Critical threshold exceeded
    TerminationProcessDied                           // Child process exited unexpectedly
)

func (r TerminationReason) String() string {
    return [...]string{
        "none", "eof", "watchdog", "timeout",
        "user_cancel", "critical", "process_died",
    }[r]
}

func (r TerminationReason) IsFatal() bool {
    return r == TerminationProcessDied || r == TerminationWatchdog
}
```

**EOF Detection Ordering** (explicit sequence):

```
1. DETECT EOF
   └─ Gate's fast-poll (50ms) sees ConnectionAlive: false
   └─ OR: Seeker status socket returns error
   └─ OR: MetricsSource returns "connection refused"

2. CAPTURE PROFILES (if enabled)
   └─ MUST happen BEFORE stopping processes
   └─ Race: child may exit within 100-500ms of EOF
   └─ Profiler.CaptureAtFailure() saves cpu.pprof, heap.pprof

3. STOP ALL PROCESSES
   └─ Send SIGTERM to server and seeker
   └─ Wait up to 5s for graceful shutdown
   └─ Send SIGKILL if still running

4. MARK PROBE RESULT
   └─ ProbeResult.Stable = false
   └─ ProbeResult.Termination = TerminationEOF
   └─ ProbeResult.Critical = true (EOF is always critical)
   └─ ProbeResult.Diagnostics = captured profiles
```

**EOF vs Process Died**:
- `TerminationEOF`: Connection closed but processes still running (SRT-level failure)
- `TerminationProcessDied`: OS process exited unexpectedly (crash, OOM, etc.)

Both are critical, but `ProcessDied` may indicate infrastructure issues vs protocol limits.

---

### TokenBucket Precision Targets (Measurable)

**Success Criteria** (not opinions):

| Metric | Target | How Measured |
|--------|--------|--------------|
| Rate accuracy | ±1% at 500 Mb/s | `TestTokenBucket_RateAccuracy_500Mbps` |
| p99 inter-send jitter | < 200µs at 500 Mb/s | `TestTokenBucket_Jitter_500Mbps` |
| CPU ceiling (spin mode) | < 1 core at 100% | `top` during test, fail if > 100% |

**Exported Metrics** (for runtime monitoring):
```go
type TokenBucketStats struct {
    CurrentRate       int64   // bytes/sec
    AvgWaitNs         int64   // average wait time
    P99WaitNs         int64   // p99 wait time (jitter measure)
    SpinTimePercent   float64 // % time spent spinning (CPU indicator)
}
```

**Implementation Choice Flow**:
```
TestTokenBucket_RateAccuracy_500Mbps
         │
         ▼
    PASS with RefillHybrid? ──YES──▶ Use Hybrid (default)
         │
         NO
         ▼
    PASS with RefillSpin? ──YES──▶ Use Spin (document CPU cost)
         │
         NO
         ▼
    FAIL: Hardware cannot sustain target rate
```

---

### SearchLoop Monotonic Invariants (Structured Failure)

**Key Design**: `checkInvariants()` returns an error instead of panicking. This preserves
"catch bugs immediately" while allowing graceful shutdown with diagnostic artifacts.

```go
// InvariantViolation captures invariant failure with full context
type InvariantViolation struct {
    Invariant    string          // Which invariant failed
    Description  string          // Human-readable explanation
    Low          int64           // Bound at time of failure
    High         int64           // Bound at time of failure
    PrevLow      int64           // Previous low
    PrevHigh     int64           // Previous high
    CurrentProbe int64           // Probe that triggered violation
    ProbeCount   int             // How many probes executed
}

func (v InvariantViolation) Error() string {
    return fmt.Sprintf("INVARIANT VIOLATION [%s]: %s (low=%d, high=%d, probe=%d)",
        v.Invariant, v.Description, v.Low, v.High, v.CurrentProbe)
}

// SearchLoop invariants - checked after every probe
// Returns error instead of panicking for graceful failure + diagnostics
func (s *SearchLoop) checkInvariants() error {
    // INVARIANT 1: low < high (bounds never cross)
    if s.low >= s.high {
        return InvariantViolation{
            Invariant:   "BOUNDS_CROSSED",
            Description: fmt.Sprintf("low(%d) >= high(%d)", s.low, s.high),
            Low: s.low, High: s.high, PrevLow: s.prevLow, PrevHigh: s.prevHigh,
            CurrentProbe: s.currentProbe, ProbeCount: s.probeCount,
        }
    }

    // INVARIANT 2: low only increases
    if s.low < s.prevLow {
        return InvariantViolation{
            Invariant:   "LOW_DECREASED",
            Description: fmt.Sprintf("low decreased (%d -> %d)", s.prevLow, s.low),
            Low: s.low, High: s.high, PrevLow: s.prevLow, PrevHigh: s.prevHigh,
            CurrentProbe: s.currentProbe, ProbeCount: s.probeCount,
        }
    }

    // INVARIANT 3: high only decreases
    if s.high > s.prevHigh {
        return InvariantViolation{
            Invariant:   "HIGH_INCREASED",
            Description: fmt.Sprintf("high increased (%d -> %d)", s.prevHigh, s.high),
            Low: s.low, High: s.high, PrevLow: s.prevLow, PrevHigh: s.prevHigh,
            CurrentProbe: s.currentProbe, ProbeCount: s.probeCount,
        }
    }

    // INVARIANT 4: current probe within bounds (after first explore)
    if s.probeCount > 1 && (s.currentProbe <= s.low || s.currentProbe >= s.high) {
        return InvariantViolation{
            Invariant:   "PROBE_OUT_OF_BOUNDS",
            Description: fmt.Sprintf("probe(%d) outside bounds [%d, %d)",
                s.currentProbe, s.low, s.high),
            Low: s.low, High: s.high, PrevLow: s.prevLow, PrevHigh: s.prevHigh,
            CurrentProbe: s.currentProbe, ProbeCount: s.probeCount,
        }
    }

    // Update prev for next check
    s.prevLow = s.low
    s.prevHigh = s.high
    return nil
}

// In SearchLoop.Run(): handle invariant violation gracefully
if err := s.checkInvariants(); err != nil {
    return SearchResult{
        Status:      StatusFailed,
        FailReason:  err.Error(),
        Probes:      s.probes,           // Include all probe records
        ConfigSnapshot: s.config,        // Include config for debugging
        Violation:   err.(InvariantViolation), // Structured failure data
    }
}
```

**Unit Test**: Expects `StatusFailed` with specific reason (not panic):

```go
func TestSearchLoop_Monotonicity(t *testing.T) {
    // Simulate: stable at 100, 200, 300; unstable at 400, 350; stable at 320
    results := []struct {
        bitrate int64
        stable  bool
    }{
        {100_000_000, true},   // low=100, high=max
        {200_000_000, true},   // low=200, high=max
        {300_000_000, true},   // low=300, high=max
        {400_000_000, false},  // low=300, high=400
        {350_000_000, false},  // low=300, high=350
        {320_000_000, true},   // low=320, high=350
        {335_000_000, false},  // low=320, high=335
    }

    s := NewSearchLoop(config, fakeGate)
    for _, r := range results {
        s.applyResult(r.bitrate, r.stable)
        err := s.checkInvariants()
        require.NoError(t, err, "invariant should hold")
    }

    // Verify final bounds
    assert.Equal(t, int64(320_000_000), s.low)
    assert.Equal(t, int64(335_000_000), s.high)
}

func TestSearchLoop_InvariantViolation_ReturnsError(t *testing.T) {
    // Force a bounds-crossing violation
    s := NewSearchLoop(config, fakeGate)
    s.low = 400_000_000
    s.high = 300_000_000  // Invalid: low > high

    err := s.checkInvariants()
    require.Error(t, err)

    violation, ok := err.(InvariantViolation)
    require.True(t, ok)
    assert.Equal(t, "BOUNDS_CROSSED", violation.Invariant)
}
```

---

### SearchLoop Simulation Test Suite (Property-Style)

**Purpose**: Eliminate algorithmic bugs by running 1000s of random scenarios with a
deterministic fake gate. This catches "stuck probing same bitrate", "bounds cross",
and "never converges" issues faster than any integration test.

```go
// contrib/performance/search_property_test.go (~200 lines)

func TestSearchLoop_PropertyBased(t *testing.T) {
    rand.Seed(time.Now().UnixNano())

    for i := 0; i < 1000; i++ {
        t.Run(fmt.Sprintf("scenario_%d", i), func(t *testing.T) {
            // Random ceiling between 100 Mb/s and 800 Mb/s
            trueCeiling := int64(100_000_000 + rand.Intn(700_000_000))

            // Optional fluke failures (0-20%)
            flukeRate := rand.Float64() * 0.2

            fakeGate := &FakeGate{
                trueCeiling:   trueCeiling,
                flukeFailRate: flukeRate,
            }

            config := SearchConfig{
                InitialBitrate: 100_000_000,
                MaxBitrate:     1_000_000_000,
                Precision:      5_000_000,
                StepSize:       20_000_000,
                Timeout:        60 * time.Second,  // Fast timeout for tests
            }

            s := NewSearchLoop(config, fakeGate)
            result := s.Run(context.Background())

            // PROPERTY 1: Invariants always hold
            // (checkInvariants returns error, not panic)
            assert.NotEqual(t, StatusFailed, result.Status,
                "invariant violation at ceiling=%d, flukeRate=%.2f: %s",
                trueCeiling, flukeRate, result.FailReason)

            // PROPERTY 2: Algorithm converges within timeout
            assert.NotEqual(t, StatusTimeout, result.Status,
                "timeout at ceiling=%d after %d probes",
                trueCeiling, len(result.Probes))

            // PROPERTY 3: If converged, ceiling is within precision of true ceiling
            if result.Status == StatusConverged {
                diff := abs(result.Ceiling - trueCeiling)
                assert.LessOrEqual(t, diff, config.Precision+config.StepSize,
                    "ceiling %d too far from true ceiling %d",
                    result.Ceiling, trueCeiling)
            }

            // PROPERTY 4: If proven, two-failure bracket holds
            if result.Proven {
                assert.GreaterOrEqual(t, result.ProofData.FailureCount, 2,
                    "proven but only %d failures", result.ProofData.FailureCount)
                assert.True(t, result.ProofData.ResetBetweenFails,
                    "proven but no reset between failures")
            }

            // PROPERTY 5: No duplicate consecutive probes (stuck detection)
            for j := 1; j < len(result.Probes); j++ {
                assert.NotEqual(t, result.Probes[j-1].TargetBitrate, result.Probes[j].TargetBitrate,
                    "stuck probing same bitrate %d twice in a row",
                    result.Probes[j].TargetBitrate)
            }
        })
    }
}

func TestSearchLoop_ConvergenceSpeed(t *testing.T) {
    // Verify O(log n) convergence behavior
    ceilings := []int64{100_000_000, 300_000_000, 500_000_000, 700_000_000}

    for _, ceiling := range ceilings {
        t.Run(fmt.Sprintf("ceiling_%dM", ceiling/1_000_000), func(t *testing.T) {
            fakeGate := &FakeGate{trueCeiling: ceiling}

            config := SearchConfig{
                InitialBitrate: 50_000_000,
                MaxBitrate:     1_000_000_000,
                Precision:      5_000_000,
                StepSize:       20_000_000,
            }

            s := NewSearchLoop(config, fakeGate)
            result := s.Run(context.Background())

            // Should converge in reasonable number of probes
            // Binary search: log2(1000/5) ≈ 8 probes, plus some for AIMD exploration
            maxExpectedProbes := 30
            assert.LessOrEqual(t, len(result.Probes), maxExpectedProbes,
                "took %d probes, expected <%d for ceiling=%d",
                len(result.Probes), maxExpectedProbes, ceiling)
        })
    }
}

func TestSearchLoop_EdgeCases(t *testing.T) {
    t.Run("ceiling_at_minimum", func(t *testing.T) {
        fakeGate := &FakeGate{trueCeiling: 50_000_000}  // Below initial
        config := SearchConfig{InitialBitrate: 100_000_000, MinBitrate: 10_000_000}

        s := NewSearchLoop(config, fakeGate)
        result := s.Run(context.Background())

        assert.Equal(t, StatusConverged, result.Status)
        assert.LessOrEqual(t, result.Ceiling, int64(50_000_000))
    })

    t.Run("ceiling_at_maximum", func(t *testing.T) {
        fakeGate := &FakeGate{trueCeiling: 2_000_000_000}  // Above max
        config := SearchConfig{MaxBitrate: 1_000_000_000}

        s := NewSearchLoop(config, fakeGate)
        result := s.Run(context.Background())

        assert.Equal(t, StatusConverged, result.Status)
        assert.Equal(t, int64(1_000_000_000), result.Ceiling)  // Clamped to max
    })

    t.Run("always_unstable", func(t *testing.T) {
        fakeGate := &FakeGate{trueCeiling: 0}  // Nothing is stable

        s := NewSearchLoop(config, fakeGate)
        result := s.Run(context.Background())

        // Should converge to minimum, not loop forever
        assert.NotEqual(t, StatusTimeout, result.Status)
    })
}

func abs(x int64) int64 {
    if x < 0 { return -x }
    return x
}
```

---

## File Structure Summary

```
contrib/
├── client-seeker/                 # ~920 lines total
│   ├── main.go                    # ~120 lines
│   ├── protocol.go                # ~80 lines
│   ├── control.go                 # ~150 lines
│   ├── tokenbucket.go             # ~180 lines (high-precision)
│   ├── bitrate.go                 # ~60 lines
│   ├── generator.go               # ~100 lines
│   ├── publisher.go               # ~80 lines
│   ├── watchdog.go                # ~90 lines
│   └── metrics.go                 # ~60 lines
│
├── performance/                   # ~1700 lines total
│   ├── main.go                    # ~100 lines
│   ├── timing.go                  # ~120 lines ← TimingModel (single source of truth)
│   ├── config.go                  # ~200 lines
│   ├── interfaces.go              # ~60 lines  ← NEW: Seeker, MetricsSource, Gate interfaces
│   ├── readiness.go               # ~80 lines  ← ReadinessCriteria barrier
│   ├── termination.go             # ~50 lines  ← NEW: TerminationReason enum
│   ├── artifacts.go               # ~80 lines  ← NEW: FailureArtifacts struct
│   ├── process.go                 # ~180 lines
│   ├── metrics.go                 # ~120 lines
│   ├── gate.go                    # ~350 lines (enhanced with EOF detection)
│   ├── search.go                  # ~320 lines (with ramping + structured invariants)
│   ├── proof.go                   # ~100 lines (two-failure bracket)
│   ├── hypothesis.go              # ~80 lines  ← NEW: HypothesisModel + thresholds
│   ├── profiler.go                # ~100 lines
│   ├── reporter.go                # ~280 lines (with hypothesis validation)
│   └── replay.go                  # ~100 lines
│
├── performance/fakes_test.go      # ~200 lines ← NEW: FakeSeeker, FakeGate, FakeMetrics
├── performance/property_test.go   # ~250 lines ← NEW: 1000-scenario simulation tests
│
└── common/                        # Existing (minimal changes)
    └── flags.go                   # Add ~20 lines for new flags
```

---

## Phase 1: Client-Seeker Foundation

**Goal**: Basic client-seeker that can send data at a fixed bitrate

**Duration**: Day 1-2

### Definition of Done (Phase 1)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `contrib/client-seeker/client-seeker` binary compiles |
| **Test Passes** | `go test -v -run TestTokenBucket` all green |
| **500 Mb/s Precision** | `TestTokenBucket_RateAccuracy_500Mbps` passes with ±1% |
| **Jitter Target** | `TestTokenBucket_Jitter_500Mbps` p99 < 200µs |
| **What Failure Looks Like** | `rate accuracy: got 478Mbps, want 500Mbps±1%` or `jitter p99=450µs > 200µs threshold` |
| **On Failure, You Get** | Test output with rate ratio, jitter histogram, CPU usage |

**BLOCKER**: If 500 Mb/s precision test fails, DO NOT proceed to Phase 2. Debug the TokenBucket first.

**Exact Thresholds** (for CI/automation):
- **Rate accuracy**: Target 500 Mb/s ± 1% → acceptable range: **495-505 Mb/s**
- **p99 jitter**: < **200 microseconds** (0.2ms)
- **CPU ceiling**: Spin mode must not exceed **1 core at 100%** for >5 seconds

---

### Step 1.1: Protocol Types

**File**: `contrib/client-seeker/protocol.go` (~80 lines)

```go
// Lines 1-20: Package and imports
package main

import (
    "encoding/json"
    "fmt"
)

// Lines 22-35: Request types
type ControlRequest struct {
    Command string `json:"command"`           // "set_bitrate", "get_status", "heartbeat", "stop"
    Bitrate int64  `json:"bitrate,omitempty"` // For set_bitrate
}

// Lines 37-50: Response types
type ControlResponse struct {
    Status         string `json:"status"`                    // "ok" or "error"
    Error          string `json:"error,omitempty"`           // Error message if status="error"
    CurrentBitrate int64  `json:"current_bitrate,omitempty"` // Current sending rate
    PacketsSent    uint64 `json:"packets_sent,omitempty"`    // Total packets
    BytesSent      uint64 `json:"bytes_sent,omitempty"`      // Total bytes
    ConnectionAlive bool  `json:"connection_alive,omitempty"` // SRT connection status
    UptimeSeconds  float64 `json:"uptime_seconds,omitempty"` // Time since start
}

// Lines 52-65: Parse request
func ParseRequest(data []byte) (*ControlRequest, error)

// Lines 67-80: Format response
func (r *ControlResponse) Marshal() ([]byte, error)
```

**Checkpoint 1.1**: ✓ Build
```bash
cd contrib/client-seeker && go build ./...
```

---

### Step 1.2: Token Bucket Rate Limiter (High-Precision)

**Critical for 500 Mb/s**: At this rate, 1456-byte packets arrive every ~23 microseconds.
Standard `time.Sleep()` has 1-15ms OS scheduler granularity - this causes **micro-bursts**
that overflow hardware buffers and trigger false NAKs.

**Solution**: Hybrid refill with active spinning for sub-millisecond precision.

**File**: `contrib/client-seeker/tokenbucket.go` (~180 lines)

```go
// Lines 1-20: Package and imports
package main

import (
    "context"
    "runtime"
    "sync"
    "sync/atomic"
    "time"
)

// Lines 22-45: TokenBucket struct (high-precision)
type TokenBucket struct {
    // Atomic for lock-free hot path
    tokens     atomic.Int64
    rate       atomic.Int64   // bytes per second

    // Configuration
    maxTokens  int64          // Bucket capacity (allows small bursts)

    // High-precision refill state
    mu           sync.Mutex
    lastRefill   time.Time
    accumulator  float64       // Sub-byte accumulator for precision

    // Precision tuning
    refillMode   RefillMode
    spinBudgetNs int64         // Max time to spin before yielding
}

type RefillMode int
const (
    RefillSleep  RefillMode = iota  // Standard sleep (>1ms intervals)
    RefillHybrid                     // Sleep + spin for sub-ms precision
    RefillSpin                       // Pure spin (highest precision, highest CPU)
)

// Lines 47-70: Constructor with precision options
func NewTokenBucket(bitsPerSecond int64, mode RefillMode) *TokenBucket

// Lines 72-90: SetRate (atomic, called by BitrateManager)
func (tb *TokenBucket) SetRate(bitsPerSecond int64)

// Lines 92-115: Consume (lock-free hot path using CAS)
func (tb *TokenBucket) Consume(bytes int64) bool

// Lines 117-160: ConsumeOrWait (blocking with precision control)
// Uses hybrid sleep+spin for sub-millisecond waits
func (tb *TokenBucket) ConsumeOrWait(ctx context.Context, bytes int64) error

// Lines 162-200: refillPrecise (high-frequency, accumulator-based)
// Called every 100µs in hybrid mode for smooth packet release
func (tb *TokenBucket) refillPrecise()

// Lines 202-240: StartRefillLoop (background goroutine)
// Hybrid mode: sleeps for 100µs, spins for remaining precision
func (tb *TokenBucket) StartRefillLoop(ctx context.Context)

// Lines 242-260: spinWait (active spinning for sub-ms waits)
// Yields to scheduler periodically to avoid CPU monopolization
func (tb *TokenBucket) spinWait(duration time.Duration)

// Lines 262-270: Stats (for metrics + jitter measurement)
func (tb *TokenBucket) Stats() (tokens, rate int64, jitterNs int64)
```

**Key Implementation Details**:

```go
// ConsumeOrWait with hybrid precision
func (tb *TokenBucket) ConsumeOrWait(ctx context.Context, bytes int64) error {
    for {
        if tb.Consume(bytes) {
            return nil
        }

        // Calculate wait time
        rate := tb.rate.Load()
        if rate == 0 {
            return fmt.Errorf("rate is zero")
        }

        waitNs := int64(float64(bytes) / float64(rate) * 1e9)

        // CRITICAL: Use appropriate wait strategy based on duration
        if waitNs < 100_000 {  // < 100µs: spin only (OS sleep is too coarse)
            tb.spinWait(time.Duration(waitNs))
        } else if waitNs < 1_000_000 {  // 100µs - 1ms: hybrid
            // Sleep for most of it, spin for the last 50µs
            sleepNs := waitNs - 50_000
            time.Sleep(time.Duration(sleepNs))
            tb.spinWait(50 * time.Microsecond)
        } else {  // > 1ms: standard sleep is fine
            time.Sleep(time.Duration(waitNs))
        }

        tb.refillPrecise()
    }
}

// spinWait - active spinning with scheduler yields
func (tb *TokenBucket) spinWait(duration time.Duration) {
    start := time.Now()
    spins := 0
    for time.Since(start) < duration {
        spins++
        if spins%100 == 0 {
            runtime.Gosched()  // Yield to prevent CPU monopolization
        }
    }
}

// refillPrecise - accumulator-based for sub-byte precision
func (tb *TokenBucket) refillPrecise() {
    tb.mu.Lock()
    defer tb.mu.Unlock()

    now := time.Now()
    elapsed := now.Sub(tb.lastRefill)
    tb.lastRefill = now

    rate := tb.rate.Load()

    // Use accumulator for sub-byte precision
    addBytes := float64(rate) * elapsed.Seconds()
    tb.accumulator += addBytes

    // Only add whole bytes to token count
    wholeBytes := int64(tb.accumulator)
    tb.accumulator -= float64(wholeBytes)

    current := tb.tokens.Load()
    newTokens := current + wholeBytes
    if newTokens > tb.maxTokens {
        newTokens = tb.maxTokens
    }
    tb.tokens.Store(newTokens)
}
```

**Test File**: `contrib/client-seeker/tokenbucket_test.go` (~200 lines)

```go
func TestTokenBucket_SetRate(t *testing.T)              // Lines 10-25
func TestTokenBucket_Consume_Success(t *testing.T)      // Lines 27-40
func TestTokenBucket_Consume_Insufficient(t *testing.T) // Lines 42-55
func TestTokenBucket_Refill_Accumulator(t *testing.T)   // Lines 57-80
func TestTokenBucket_RateAccuracy_100Mbps(t *testing.T) // Lines 82-110
func TestTokenBucket_RateAccuracy_500Mbps(t *testing.T) // Lines 112-145 (CRITICAL)
func TestTokenBucket_Jitter_500Mbps(t *testing.T)       // Lines 147-180 (burst detection)
func BenchmarkTokenBucket_Consume(b *testing.B)         // Lines 182-195
```

**Strategic Verification**: The "1% Precision at 500 Mb/s" Test

```go
func TestTokenBucket_RateAccuracy_500Mbps(t *testing.T) {
    // CRITICAL: If this fails, the whole system will fail
    targetBps := int64(500_000_000)  // 500 Mb/s
    tb := NewTokenBucket(targetBps, RefillHybrid)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    go tb.StartRefillLoop(ctx)

    packetSize := int64(1456)
    var bytesSent int64
    start := time.Now()

    for time.Since(start) < 3*time.Second {
        if err := tb.ConsumeOrWait(ctx, packetSize); err != nil {
            break
        }
        bytesSent += packetSize
    }

    elapsed := time.Since(start)
    actualBps := float64(bytesSent*8) / elapsed.Seconds()
    ratio := actualBps / float64(targetBps)

    // MUST be within 1% of target
    if ratio < 0.99 || ratio > 1.01 {
        t.Errorf("Rate accuracy failed: target=%d, actual=%.0f, ratio=%.3f",
            targetBps, actualBps, ratio)
    }

    t.Logf("500 Mb/s test: ratio=%.4f (%.2f%% accuracy)", ratio, ratio*100)
}

func TestTokenBucket_Jitter_500Mbps(t *testing.T) {
    // Measure inter-packet timing variance
    targetBps := int64(500_000_000)
    tb := NewTokenBucket(targetBps, RefillHybrid)

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    go tb.StartRefillLoop(ctx)

    packetSize := int64(1456)
    expectedIntervalNs := int64(float64(packetSize*8) / float64(targetBps) * 1e9)  // ~23µs

    var intervals []int64
    lastSend := time.Now()

    for i := 0; i < 10000; i++ {
        tb.ConsumeOrWait(ctx, packetSize)
        now := time.Now()
        intervals = append(intervals, now.Sub(lastSend).Nanoseconds())
        lastSend = now
    }

    // Calculate jitter (variance from expected)
    var sumVariance int64
    for _, interval := range intervals {
        diff := interval - expectedIntervalNs
        if diff < 0 {
            diff = -diff
        }
        sumVariance += diff
    }
    avgJitter := sumVariance / int64(len(intervals))

    // Jitter should be < 50% of packet interval
    maxJitter := expectedIntervalNs / 2
    if avgJitter > maxJitter {
        t.Errorf("Jitter too high: avg=%dns, max=%dns, expected_interval=%dns",
            avgJitter, maxJitter, expectedIntervalNs)
    }

    t.Logf("Jitter test: avg=%dns (%.1f%% of interval)",
        avgJitter, float64(avgJitter)/float64(expectedIntervalNs)*100)
}
```

**Checkpoint 1.2**: ✓ Build + ✓ Test + ✓ **500 Mb/s Precision Test**
```bash
cd contrib/client-seeker && go build ./...
cd contrib/client-seeker && go test -v -run TestTokenBucket
cd contrib/client-seeker && go test -v -run TestTokenBucket_RateAccuracy_500Mbps
cd contrib/client-seeker && go test -v -run TestTokenBucket_Jitter_500Mbps
```

**If 500 Mb/s precision test fails**: The implementation cannot proceed. Debug:
1. Profile the refill loop with `go tool pprof`
2. Check if `runtime.Gosched()` is yielding too aggressively
3. Consider `RefillSpin` mode (higher CPU, higher precision)

---

### Step 1.3: Bitrate Manager

**File**: `contrib/client-seeker/bitrate.go` (~60 lines)

```go
// Lines 1-10: Package and imports
package main

import "sync/atomic"

// Lines 12-22: BitrateManager struct
type BitrateManager struct {
    bucket     *TokenBucket
    current    atomic.Int64  // Current bitrate in bps
    minBitrate int64
    maxBitrate int64
}

// Lines 24-35: Constructor
func NewBitrateManager(initialBps, minBps, maxBps int64) *BitrateManager

// Lines 37-50: Set (with bounds clamping)
func (bm *BitrateManager) Set(bps int64) error

// Lines 52-55: Get
func (bm *BitrateManager) Get() int64

// Lines 57-60: Bucket accessor
func (bm *BitrateManager) Bucket() *TokenBucket
```

**Checkpoint 1.3**: ✓ Build
```bash
cd contrib/client-seeker && go build ./...
```

---

### Step 1.4: Data Generator (Simplified)

**File**: `contrib/client-seeker/generator.go` (~100 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "context"
    "crypto/rand"
    "sync/atomic"
)

// Lines 17-30: Generator struct
type DataGenerator struct {
    bucket      *TokenBucket
    packetSize  int
    pattern     string  // "random", "sequential", "zeros"

    // Stats
    packetsSent atomic.Uint64
    bytesSent   atomic.Uint64

    // Output
    outputChan  chan []byte
}

// Lines 32-45: Constructor
func NewDataGenerator(bucket *TokenBucket, packetSize int, pattern string) *DataGenerator

// Lines 47-80: Run (main generation loop)
func (g *DataGenerator) Run(ctx context.Context) error

// Lines 82-95: fillPacket (pattern generation)
func (g *DataGenerator) fillPacket(packet []byte)

// Lines 97-100: Stats
func (g *DataGenerator) Stats() (packets, bytes uint64)
```

**Checkpoint 1.4**: ✓ Build
```bash
cd contrib/client-seeker && go build ./...
```

---

### Step 1.5: Main Entry Point (Minimal)

**File**: `contrib/client-seeker/main.go` (Initial ~60 lines, will grow)

```go
// Lines 1-20: Package, imports, flags
package main

import (
    "context"
    "flag"
    "fmt"
    "os"
    "os/signal"
)

var (
    flagTo       = flag.String("to", "", "SRT URL to publish to")
    flagInitial  = flag.String("initial", "100M", "Initial bitrate")
    flagControl  = flag.String("control", "/tmp/client_seeker.sock", "Control socket path")
    flagPattern  = flag.String("pattern", "random", "Data pattern")
)

// Lines 22-60: main()
func main() {
    flag.Parse()

    // Parse initial bitrate
    bitrate, err := parseBitrate(*flagInitial)
    if err != nil {
        fmt.Fprintf(os.Stderr, "invalid bitrate: %v\n", err)
        os.Exit(1)
    }

    // Create components
    bm := NewBitrateManager(bitrate, 1_000_000, 1_000_000_000)
    gen := NewDataGenerator(bm.Bucket(), 1456, *flagPattern)

    // Setup context with signal handling
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    // Start token bucket refill
    go bm.Bucket().StartRefillLoop(ctx)

    // Start generator (for now, just prints stats)
    go gen.Run(ctx)

    // Wait for interrupt
    <-ctx.Done()
    fmt.Println("Shutting down...")
}
```

**Milestone 1**: ✓ Build + ✓ Run (no SRT yet)
```bash
cd contrib/client-seeker && go build -o client-seeker
./client-seeker -initial 100M
# Should run and print stats, exit on Ctrl+C
```

---

## Phase 2: Client-Seeker Control Socket

**Goal**: Control socket accepts commands and changes bitrate

**Duration**: Day 3-4

### Definition of Done (Phase 2)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `./client-seeker -control /tmp/seeker.sock` starts and listens |
| **Test Passes** | `go test -v -run TestControlServer` all green |
| **Watchdog Works** | `TestWatchdog_Timeout` triggers after 5s without heartbeat |
| **What Failure Looks Like** | `watchdog: no heartbeat for 5.1s, expected <5s timeout` |

---

### Step 2.1: Control Server

**File**: `contrib/client-seeker/control.go` (~150 lines)

```go
// Lines 1-20: Package and imports
package main

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "net"
    "os"
    "sync"
    "time"
)

// Lines 22-40: ControlServer struct
type ControlServer struct {
    socketPath   string
    listener     net.Listener
    bm           *BitrateManager
    gen          *DataGenerator
    startTime    time.Time

    // Heartbeat tracking
    mu             sync.Mutex
    lastHeartbeat  time.Time
}

// Lines 42-55: Constructor
func NewControlServer(socketPath string, bm *BitrateManager, gen *DataGenerator) *ControlServer

// Lines 57-85: Start (accept loop)
func (cs *ControlServer) Start(ctx context.Context) error

// Lines 87-120: handleConnection
func (cs *ControlServer) handleConnection(ctx context.Context, conn net.Conn)

// Lines 122-145: handleCommand
func (cs *ControlServer) handleCommand(req *ControlRequest) *ControlResponse

// Lines 147-150: LastHeartbeat
func (cs *ControlServer) LastHeartbeat() time.Time
```

**Test File**: `contrib/client-seeker/control_test.go` (~80 lines)

```go
func TestControlServer_SetBitrate(t *testing.T)    // Lines 10-35
func TestControlServer_GetStatus(t *testing.T)     // Lines 37-55
func TestControlServer_Heartbeat(t *testing.T)     // Lines 57-75
func TestControlServer_InvalidCommand(t *testing.T) // Lines 77-90
```

**Checkpoint 2.1**: ✓ Build + ✓ Test
```bash
cd contrib/client-seeker && go build ./...
cd contrib/client-seeker && go test -v -run TestControlServer
```

---

### Step 2.2: Watchdog (Tiered Soft-Landing)

**File**: `contrib/client-seeker/watchdog.go` (~90 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "context"
    "log"
    "time"
)

// Lines 17-25: WatchdogConfig
type WatchdogConfig struct {
    Enabled      bool
    Timeout      time.Duration
    SafeBitrate  int64
    StopTimeout  time.Duration
}

// Lines 27-35: WatchdogState enum
type WatchdogState int

const (
    WatchdogNormal WatchdogState = iota
    WatchdogWarning
    WatchdogCritical
)

// Lines 37-50: Watchdog struct
type Watchdog struct {
    config    WatchdogConfig
    control   *ControlServer
    bm        *BitrateManager
    stopFunc  context.CancelFunc
}

// Lines 52-60: Constructor
func NewWatchdog(config WatchdogConfig, cs *ControlServer, bm *BitrateManager, stop context.CancelFunc) *Watchdog

// Lines 62-90: Run (state machine loop)
func (w *Watchdog) Run(ctx context.Context)
```

**Test File**: `contrib/client-seeker/watchdog_test.go` (~60 lines)

```go
func TestWatchdog_NormalOperation(t *testing.T)      // Lines 10-25
func TestWatchdog_SoftLanding(t *testing.T)          // Lines 27-45
func TestWatchdog_Recovery(t *testing.T)             // Lines 47-65
func TestWatchdog_CriticalStop(t *testing.T)         // Lines 67-85
```

**Checkpoint 2.2**: ✓ Build + ✓ Test
```bash
cd contrib/client-seeker && go build ./...
cd contrib/client-seeker && go test -v -run TestWatchdog
```

---

### Step 2.3: Update Main with Control + Watchdog

**File**: `contrib/client-seeker/main.go` (Update to ~120 lines)

```go
// Add flags (~lines 15-20)
var (
    // ... existing flags ...
    flagWatchdog        = flag.Bool("watchdog", true, "Enable watchdog")
    flagWatchdogTimeout = flag.Duration("watchdog-timeout", 5*time.Second, "Watchdog timeout")
    flagWatchdogSafe    = flag.String("watchdog-safe-bitrate", "10M", "Safe bitrate on watchdog")
)

// Update main() to:
// 1. Create ControlServer
// 2. Create Watchdog
// 3. Start all components as goroutines
// 4. Wait for shutdown
```

**Milestone 2**: ✓ Build + ✓ Manual Test
```bash
cd contrib/client-seeker && go build -o client-seeker
./client-seeker -initial 100M -control /tmp/test.sock &

# In another terminal:
echo '{"command":"get_status"}' | nc -U /tmp/test.sock
echo '{"command":"set_bitrate","bitrate":200000000}' | nc -U /tmp/test.sock
echo '{"command":"heartbeat"}' | nc -U /tmp/test.sock

# Should respond with JSON
```

---

## Phase 3: Client-Seeker SRT Integration

**Goal**: Actually send data over SRT connection

**Duration**: Day 5-6

### Definition of Done (Phase 3)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `client-seeker` connects to running SRT server |
| **Manual Test** | Start server → start seeker → verify packets sent |
| **Metrics Work** | `curl -s http://localhost:9091/metrics` shows `bytes_sent` |
| **What Failure Looks Like** | `srt dial: connection refused` or `0 bytes_sent after 5s` |

---

### Step 3.1: Publisher (SRT Connection)

**File**: `contrib/client-seeker/publisher.go` (~80 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "context"
    "fmt"
    "sync/atomic"

    srt "github.com/datarhei/gosrt"
)

// Lines 17-30: Publisher struct
type Publisher struct {
    url           string
    conn          *srt.Conn
    config        srt.Config
    reconnect     bool

    // Stats
    connectionAlive atomic.Bool
    nakCount        atomic.Uint64
}

// Lines 32-50: Constructor
func NewPublisher(url string, config srt.Config, reconnect bool) *Publisher

// Lines 52-70: Connect
func (p *Publisher) Connect(ctx context.Context) error

// Lines 72-90: Run (read from generator, write to SRT)
func (p *Publisher) Run(ctx context.Context, gen *DataGenerator) error

// Lines 92-100: Stats
func (p *Publisher) Stats() (alive bool, naks uint64)
```

**Checkpoint 3.1**: ✓ Build
```bash
cd contrib/client-seeker && go build ./...
```

---

### Step 3.2: Metrics Export

**File**: `contrib/client-seeker/metrics.go` (~60 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "fmt"
    "net"
    "net/http"
)

// Lines 17-30: MetricsServer struct
type MetricsServer struct {
    socketPath string
    bm         *BitrateManager
    gen        *DataGenerator
    pub        *Publisher
}

// Lines 32-45: Constructor
func NewMetricsServer(socketPath string, bm *BitrateManager, gen *DataGenerator, pub *Publisher) *MetricsServer

// Lines 47-70: Start (HTTP server on UDS)
func (ms *MetricsServer) Start(ctx context.Context) error

// Lines 72-90: metricsHandler (Prometheus format)
func (ms *MetricsServer) metricsHandler(w http.ResponseWriter, r *http.Request)
```

**Checkpoint 3.2**: ✓ Build
```bash
cd contrib/client-seeker && go build ./...
```

---

### Step 3.3: Complete Main Integration

**File**: `contrib/client-seeker/main.go` (Final ~150 lines)

Update to include:
- SRT config flag parsing (reuse from contrib/common)
- Publisher creation and connection
- Full component wiring
- Graceful shutdown

**Milestone 3**: ✓ Build + ✓ Integration Test
```bash
# Build
cd contrib/client-seeker && go build -o client-seeker

# Start a test server (in another terminal)
cd contrib/server && ./server -addr 127.0.0.1:6000

# Run client-seeker
./client-seeker \
    -to srt://127.0.0.1:6000/test \
    -initial 100M \
    -control /tmp/seeker.sock \
    -promuds /tmp/seeker_metrics.sock

# Verify:
# 1. Connection established (server shows client)
# 2. Control socket works: echo '{"command":"get_status"}' | nc -U /tmp/seeker.sock
# 3. Metrics available: curl --unix-socket /tmp/seeker_metrics.sock http://localhost/metrics
```

---

## Phase 4: Performance Orchestrator Foundation

**Goal**: Basic orchestrator that can start/stop processes and validate timing contracts

**Duration**: Day 7-8

### Definition of Done (Phase 4)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `contrib/performance/performance` binary compiles |
| **Config Parses** | `./performance -config fc=102400,step=10M` parses without error |
| **TimingModel Valid** | `TestTimingModel_ValidateContracts` all green |
| **Readiness Barrier** | `TestProcessManager_WaitReady` shows detailed failures |
| **What Failure Looks Like** | `CONTRACT VIOLATION: WarmUp(1s) must be > 2×RampUpdateInterval(1s)` |

---

### Step 4.1: Configuration Parser

**File**: `contrib/performance/config.go` (~200 lines)

```go
// Lines 1-20: Package and imports
package main

import (
    "fmt"
    "strconv"
    "strings"
    "time"
)

// Lines 22-50: SearchConfig
type SearchConfig struct {
    InitialBitrate  int64
    MinBitrate      int64
    MaxBitrate      int64
    StepSize        int64
    DecreasePercent float64
    CriticalPercent float64
    Precision       int64
    Timeout         time.Duration
}

// Lines 52-80: StabilityConfig
type StabilityConfig struct {
    WarmUpDuration   time.Duration
    StabilityWindow  time.Duration
    SampleInterval   time.Duration
    MinSamples       int
    MaxGapRate       float64
    MaxNAKRate       float64
    MaxRTTMs         float64
    MinThroughput    float64
    CriticalGapRate  float64
    CriticalNAKRate  float64
}

// Lines 82-100: SRTConfig (subset for performance testing)
type SRTConfig struct {
    FC              uint32
    RecvBuf         uint32
    SendBuf         uint32
    Latency         time.Duration
    RecvRings       int
    PacketRingSize  int
    // ... etc
}

// Lines 102-130: Parsers
func parseBitrate(s string) (int64, error)    // "200M" -> 200_000_000
func parseBytes(s string) (uint32, error)     // "128M" -> 134_217_728
func parseDuration(s string) (time.Duration, error)  // "5s" -> 5*time.Second

// Lines 132-180: ParseArgs
func ParseArgs(args map[string]string) (*SearchConfig, *StabilityConfig, *SRTConfig, error)

// Lines 182-200: Defaults
var DefaultSearchConfig = SearchConfig{...}
var DefaultStabilityConfig = StabilityConfig{...}
```

**Test File**: `contrib/performance/config_test.go` (~100 lines)

```go
func TestParseBitrate(t *testing.T)     // Lines 10-30
func TestParseBytes(t *testing.T)       // Lines 32-50
func TestParseDuration(t *testing.T)    // Lines 52-70
func TestParseArgs_Defaults(t *testing.T)    // Lines 72-90
func TestParseArgs_Custom(t *testing.T)      // Lines 92-115
```

**Checkpoint 4.1**: ✓ Build + ✓ Test
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestParse
```

---

### Step 4.2: Contracts (Timing Invariants)

**File**: `contrib/performance/contracts.go` (~80 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "fmt"
    "time"
)

// Lines 17-35: Contract structs
type OrchestratorContract struct {
    RampUpdateInterval time.Duration
    HeartbeatInterval  time.Duration
    MinProbeDuration   time.Duration
}

type SeekerContract struct {
    MaxApplyLatency  time.Duration
    MetricsStaleness time.Duration
    WatchdogTimeout  time.Duration
}

// Lines 37-55: ValidateContracts
func ValidateContracts(oc OrchestratorContract, sc SeekerContract, stab StabilityConfig) error

// Lines 57-80: Contract validation checks
// INVARIANT 1: WarmUp > 2 * RampUpdateInterval
// INVARIANT 2: StabilityWindow > 3 * SampleInterval
// INVARIANT 3: HeartbeatInterval < WatchdogTimeout / 2
// INVARIANT 4: MinProbeDuration = WarmUp + StabilityWindow
```

**Test File**: `contrib/performance/contracts_test.go` (~50 lines)

```go
func TestContracts_Valid(t *testing.T)            // Lines 10-25
func TestContracts_InvalidWarmUp(t *testing.T)    // Lines 27-40
func TestContracts_InvalidStability(t *testing.T) // Lines 42-55
```

**Checkpoint 4.2**: ✓ Build + ✓ Test
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestContracts
```

---

### Step 4.3: Process Manager

**File**: `contrib/performance/process.go` (~180 lines)

```go
// Lines 1-20: Package and imports
package main

import (
    "context"
    "fmt"
    "net"
    "os/exec"
    "time"
)

// Lines 22-45: ProcessManager struct
type ProcessManager struct {
    serverCmd       *exec.Cmd
    seekerCmd       *exec.Cmd

    serverPromUDS   string
    seekerPromUDS   string
    seekerControlUDS string

    serverAddr      string
    config          *SRTConfig
}

// Lines 47-65: Constructor
func NewProcessManager(serverAddr string, config *SRTConfig) *ProcessManager

// Lines 67-100: StartServer
func (pm *ProcessManager) StartServer(ctx context.Context) error

// Lines 102-135: StartSeeker
func (pm *ProcessManager) StartSeeker(ctx context.Context, initialBitrate int64) error

// Lines 137-160: WaitReady (readiness gate)
func (pm *ProcessManager) WaitReady(ctx context.Context) error

// Lines 162-175: probePrometheus
func (pm *ProcessManager) probePrometheus(ctx context.Context, socketPath string) error

// Lines 177-190: Stop
func (pm *ProcessManager) Stop()
```

**Checkpoint 4.3**: ✓ Build
```bash
cd contrib/performance && go build ./...
```

---

### Step 4.4: Main Entry Point (Minimal)

**File**: `contrib/performance/main.go` (Initial ~100 lines)

```go
// Lines 1-30: Package, imports, flags
package main

import (
    "context"
    "flag"
    "fmt"
    "os"
    "os/signal"
)

// Lines 32-70: main() - minimal version
func main() {
    // Parse args
    args := parseCliArgs()

    searchConfig, stabConfig, srtConfig, err := ParseArgs(args)
    if err != nil {
        fmt.Fprintf(os.Stderr, "config error: %v\n", err)
        os.Exit(1)
    }

    // Validate contracts
    if err := ValidateContracts(...); err != nil {
        fmt.Fprintf(os.Stderr, "contract violation: %v\n", err)
        os.Exit(1)
    }

    // Create process manager
    pm := NewProcessManager("127.0.0.1:6000", srtConfig)

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    // Start components
    if err := pm.StartServer(ctx); err != nil {
        fmt.Fprintf(os.Stderr, "server start failed: %v\n", err)
        os.Exit(1)
    }
    defer pm.Stop()

    fmt.Println("Server started, press Ctrl+C to stop")
    <-ctx.Done()
}
```

**Milestone 4**: ✓ Build + ✓ Manual Test
```bash
cd contrib/performance && go build -o performance
./performance INITIAL=100M
# Should start server, wait for Ctrl+C
```

---

## Phase 5: Metrics Collection & Stability Gate

**Goal**: Collect metrics and implement the inner loop (the stability oracle)

**Duration**: Day 9-10

### Definition of Done (Phase 5)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `gate.go` compiles with all methods |
| **Gate Tests** | `go test -v -run TestGate` all green |
| **EOF Detection** | `TestGate_Probe_EOF_FastDetection` detects within 100ms |
| **Profile Capture** | `TestGate_Probe_EOF_ProfileCapture` saves cpu.pprof |
| **What Failure Looks Like** | `EOF detected after 2.1s, expected <100ms after connection death` |
| **On Failure, You Get** | ProbeResult with Termination=EOF, last metrics, profile paths |

**CRITICAL**: Gate is the "single binary oracle" - SearchLoop MUST NOT interpret metrics.
All stability logic lives here.

**Gate implements the `Gate` interface** for testability:
```go
type Gate interface {
    Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult
}
```

---

### Step 5.1: Metrics Collector

**File**: `contrib/performance/metrics.go` (~120 lines)

```go
// Lines 1-20: Package and imports
package main

import (
    "bufio"
    "fmt"
    "net"
    "net/http"
    "strconv"
    "strings"
)

// Lines 22-45: StabilityMetrics struct
type StabilityMetrics struct {
    GapRate         float64
    NAKRate         float64
    RetransRate     float64
    RecoveryRate    float64
    RTT             float64
    RTTVariance     float64
    ActualBitrate   float64
    TargetBitrate   float64
    ThroughputRatio float64
    ConnectionAlive bool
    ErrorCount      int
}

// Lines 47-65: MetricsCollector struct
type MetricsCollector struct {
    serverPromUDS  string
    seekerPromUDS  string
    httpClient     *http.Client
}

// Lines 67-80: Constructor
func NewMetricsCollector(serverUDS, seekerUDS string) *MetricsCollector

// Lines 82-100: Collect (scrape both endpoints)
func (mc *MetricsCollector) Collect() StabilityMetrics

// Lines 102-120: parsePrometheus (extract metrics from text)
func (mc *MetricsCollector) parsePrometheus(body string) map[string]float64
```

**Test File**: `contrib/performance/metrics_test.go` (~60 lines)

```go
func TestMetricsCollector_ParsePrometheus(t *testing.T)  // Lines 10-40
func TestMetricsCollector_Aggregate(t *testing.T)        // Lines 42-65
```

**Checkpoint 5.1**: ✓ Build + ✓ Test
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestMetrics
```

---

### Step 5.2: Seeker Control Client

**File**: `contrib/performance/seeker.go` (~80 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "encoding/json"
    "net"
    "time"
)

// Lines 17-30: SeekerControl struct
type SeekerControl struct {
    socketPath string
    conn       net.Conn
}

// Lines 32-45: Constructor + Connect
func NewSeekerControl(socketPath string) *SeekerControl
func (sc *SeekerControl) Connect() error

// Lines 47-60: SetBitrate
func (sc *SeekerControl) SetBitrate(bps int64) error

// Lines 62-75: Heartbeat
func (sc *SeekerControl) Heartbeat() error

// Lines 77-90: GetStatus
func (sc *SeekerControl) GetStatus() (*ControlResponse, error)
```

**Checkpoint 5.2**: ✓ Build
```bash
cd contrib/performance && go build ./...
```

---

### Step 5.3: Profiler (Diagnostic Capture)

**File**: `contrib/performance/profiler.go` (~100 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "fmt"
    "os"
    "path/filepath"
    "runtime/pprof"
    "time"
)

// Lines 17-35: DiagnosticProfiler struct
type DiagnosticProfiler struct {
    outputDir     string
    captureOnCrit bool
    profiles      []string  // "cpu", "heap", "goroutine", "allocs"
}

// Lines 37-50: DiagnosticCapture struct
type DiagnosticCapture struct {
    Timestamp    time.Time
    Bitrate      int64
    ProfilePaths map[string]string
    Metrics      StabilityMetrics
    TEMetric     float64
}

// Lines 52-65: Constructor
func NewDiagnosticProfiler(outputDir string, profiles []string) *DiagnosticProfiler

// Lines 67-95: CaptureAtFailure
func (dp *DiagnosticProfiler) CaptureAtFailure(bitrate int64, m StabilityMetrics) *DiagnosticCapture

// Lines 97-115: logTEAnalysis (hypothesis validation)
func (dp *DiagnosticProfiler) logTEAnalysis(capture *DiagnosticCapture)
```

**Checkpoint 5.3**: ✓ Build
```bash
cd contrib/performance && go build ./...
```

---

### Step 5.4: Stability Gate (Inner Loop) with Snapshot-on-EOF

**Critical Improvement**: The 400 Mb/s failure shows "graceful EOF" after 3-4 seconds.
Standard 500ms Prometheus scrape is too slow - by the time we detect failure, the process
may have already terminated and we lose the diagnostic window.

**Solution**:
1. **Fast-poll seeker status** at 50ms (10x faster than Prometheus scrape)
2. **Immediate profile capture** the moment `ConnectionAlive: false` is detected
3. **Race-condition-safe** profiling before child process terminates

**File**: `contrib/performance/gate.go` (~320 lines)

```go
// Lines 1-25: Package and imports
package main

import (
    "context"
    "fmt"
    "sync"
    "time"
)

// Lines 27-55: StabilityGate struct (enhanced)
type StabilityGate struct {
    config          StabilityConfig
    collector       *MetricsCollector
    seeker          *SeekerControl
    profiler        *DiagnosticProfiler

    // High-resolution monitoring (Snapshot-on-EOF)
    fastPollInterval time.Duration  // 50ms for seeker status
    slowPollInterval time.Duration  // 500ms for Prometheus metrics

    // EOF detection state
    mu                sync.Mutex
    lastAliveTime     time.Time
    connectionWasAlive bool
}

// Lines 57-80: ProbeResult struct (enhanced)
type ProbeResult struct {
    Stable      bool
    Critical    bool
    Cancelled   bool
    EOFDetected bool              // NEW: Specific flag for connection death
    Metrics     StabilityMetrics
    Samples     []StabilityMetrics
    Diagnostics *DiagnosticCapture

    // Timing info for debugging
    WarmUpDuration    time.Duration
    EvaluationDuration time.Duration
    TimeToFailure     time.Duration  // NEW: How long until EOF (if any)
}

// Lines 82-100: Constructor
func NewStabilityGate(config StabilityConfig, collector *MetricsCollector,
                      seeker *SeekerControl, profiler *DiagnosticProfiler) *StabilityGate {
    return &StabilityGate{
        config:           config,
        collector:        collector,
        seeker:           seeker,
        profiler:         profiler,
        fastPollInterval: 50 * time.Millisecond,   // Fast for EOF detection
        slowPollInterval: 500 * time.Millisecond,  // Slow for metrics
    }
}

// Lines 102-200: Probe (main method with dual-speed polling)
// NOTE: probeStart is provided by SearchLoop AFTER ramp completes.
// This eliminates "when did the probe start?" ambiguity.
func (g *StabilityGate) Probe(ctx context.Context, probeStart time.Time, bitrate int64) ProbeResult {
    // probeStart is when SearchLoop finished ramping and called us

    // 1. Set bitrate
    if err := g.seeker.SetBitrate(bitrate); err != nil {
        return ProbeResult{Cancelled: true}
    }

    // Track connection state
    g.mu.Lock()
    g.connectionWasAlive = true
    g.lastAliveTime = time.Now()
    g.mu.Unlock()

    // 2. Warm-up with fast polling for early EOF detection
    warmUpStart := time.Now()
    if earlyFailure := g.warmUpWithFastPoll(ctx, bitrate); earlyFailure != nil {
        earlyFailure.WarmUpDuration = time.Since(warmUpStart)
        earlyFailure.TimeToFailure = time.Since(probeStart)
        return *earlyFailure
    }
    warmUpDuration := time.Since(warmUpStart)

    // 3. Dual-speed polling during stability window
    evalStart := time.Now()
    samples := make([]StabilityMetrics, 0, g.config.MinSamples)

    // Two tickers: fast for EOF detection, slow for metrics
    fastTicker := time.NewTicker(g.fastPollInterval)
    slowTicker := time.NewTicker(g.slowPollInterval)
    defer fastTicker.Stop()
    defer slowTicker.Stop()

    deadline := time.Now().Add(g.config.StabilityWindow)

    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return ProbeResult{Cancelled: true}

        case <-fastTicker.C:
            // FAST PATH: Check connection alive (50ms)
            status, err := g.seeker.GetStatus()
            if err != nil || !status.ConnectionAlive {
                // === SNAPSHOT-ON-EOF: Capture immediately! ===
                m := g.collector.Collect()  // Get last metrics
                diag := g.captureAtFailureImmediate(bitrate, m, probeStart)

                return ProbeResult{
                    Stable:             false,
                    Critical:           true,
                    EOFDetected:        true,
                    Metrics:            m,
                    Samples:            samples,
                    Diagnostics:        diag,
                    WarmUpDuration:     warmUpDuration,
                    EvaluationDuration: time.Since(evalStart),
                    TimeToFailure:      time.Since(probeStart),
                }
            }
            g.mu.Lock()
            g.lastAliveTime = time.Now()
            g.mu.Unlock()

        case <-slowTicker.C:
            // SLOW PATH: Full metrics collection (500ms)
            m := g.collector.Collect()
            samples = append(samples, m)

            // Check critical thresholds
            if g.isCritical(m) {
                diag := g.captureAtFailureImmediate(bitrate, m, probeStart)
                return ProbeResult{
                    Stable:             false,
                    Critical:           true,
                    Metrics:            m,
                    Samples:            samples,
                    Diagnostics:        diag,
                    WarmUpDuration:     warmUpDuration,
                    EvaluationDuration: time.Since(evalStart),
                }
            }
        }
    }

    // 4. Evaluate all samples
    stable := g.evaluateSamples(samples)

    return ProbeResult{
        Stable:             stable,
        Metrics:            g.aggregate(samples),
        Samples:            samples,
        WarmUpDuration:     warmUpDuration,
        EvaluationDuration: time.Since(evalStart),
    }
}

// Lines 202-250: warmUpWithFastPoll (50ms polling during warm-up)
func (g *StabilityGate) warmUpWithFastPoll(ctx context.Context, bitrate int64) *ProbeResult {
    fastTicker := time.NewTicker(g.fastPollInterval)
    defer fastTicker.Stop()

    deadline := time.Now().Add(g.config.WarmUpDuration)
    probeStart := time.Now()

    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return &ProbeResult{Cancelled: true}
        case <-fastTicker.C:
            status, err := g.seeker.GetStatus()
            if err != nil || !status.ConnectionAlive {
                m := g.collector.Collect()
                diag := g.captureAtFailureImmediate(bitrate, m, probeStart)
                return &ProbeResult{
                    Stable:      false,
                    Critical:    true,
                    EOFDetected: true,
                    Metrics:     m,
                    Diagnostics: diag,
                }
            }
        }
    }

    return nil  // Warm-up completed successfully
}

// Lines 252-310: captureAtFailureImmediate (RACE THE PROCESS TERMINATION)
func (g *StabilityGate) captureAtFailureImmediate(bitrate int64, m StabilityMetrics, probeStart time.Time) *DiagnosticCapture {
    if g.profiler == nil || !g.profiler.captureOnCrit {
        return nil
    }

    // CRITICAL: We're racing against process termination
    // The child processes may exit within 100-500ms of EOF

    capture := g.profiler.CaptureAtFailure(bitrate, m)
    if capture != nil {
        capture.TimeToFailure = time.Since(probeStart)

        // Log hypothesis analysis immediately
        g.logHypothesisAnalysis(capture)
    }

    return capture
}

// Lines 312-350: logHypothesisAnalysis (automated bottleneck detection)
func (g *StabilityGate) logHypothesisAnalysis(capture *DiagnosticCapture) {
    te := capture.TEMetric
    m := capture.Metrics

    fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
    fmt.Printf("║  FAILURE ANALYSIS at %d Mb/s (after %.1fs)                   \n",
        capture.Bitrate/1_000_000, capture.TimeToFailure.Seconds())
    fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")
    fmt.Printf("║  Throughput Efficiency: %.1f%%                                \n", te*100)
    fmt.Printf("║  Gap Rate: %.3f%%   NAK Rate: %.3f%%   RTT: %.2fms           \n",
        m.GapRate*100, m.NAKRate*100, m.RTT)
    fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")

    // Automated hypothesis flagging
    if te < 0.95 && m.GapRate < 0.001 {
        fmt.Printf("║  🔴 HYPOTHESIS 2: EventLoop Starvation                       \n")
        fmt.Printf("║     Low throughput without packet loss                       \n")
        fmt.Printf("║     → Check cpu.pprof for deliverReadyPacketsEventLoop()    \n")
    } else if m.NAKRate > 0.02 && capture.TimeToFailure < 5*time.Second {
        fmt.Printf("║  🔴 HYPOTHESIS 1: Flow Control Window Exhaustion             \n")
        fmt.Printf("║     NAK spike immediately before EOF                         \n")
        fmt.Printf("║     → Increase FC, check ACK processing latency             \n")
    } else if m.RTTVariance > 10.0 {
        fmt.Printf("║  🔴 HYPOTHESIS 5: GC/Memory Pressure                         \n")
        fmt.Printf("║     High RTT variance indicates scheduling jitter           \n")
        fmt.Printf("║     → Check heap.pprof, run with GODEBUG=gctrace=1          \n")
    } else if te < 0.95 && m.GapRate > 0.001 {
        fmt.Printf("║  🔴 HYPOTHESIS 4: io_uring Backpressure                      \n")
        fmt.Printf("║     Low throughput WITH packet loss                          \n")
        fmt.Printf("║     → Check IoUringRecvRingSize, completion queue depth     \n")
    } else {
        fmt.Printf("║  🟡 UNKNOWN: Review profiles for additional clues           \n")
    }

    fmt.Printf("╠══════════════════════════════════════════════════════════════╣\n")
    fmt.Printf("║  Profiles saved to: %s\n", g.profiler.outputDir)
    fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n\n")
}

// Lines 352-365: isCritical
func (g *StabilityGate) isCritical(m StabilityMetrics) bool

// Lines 367-385: evaluateSamples
func (g *StabilityGate) evaluateSamples(samples []StabilityMetrics) bool

// Lines 387-405: aggregate
func (g *StabilityGate) aggregate(samples []StabilityMetrics) StabilityMetrics

// Lines 407-420: WithConfig (for proof phase extended window)
func (g *StabilityGate) WithConfig(config StabilityConfig) *StabilityGate
```

**Test File**: `contrib/performance/gate_test.go` (~200 lines)

```go
// Mock collector for testing
type mockCollector struct {
    samples []StabilityMetrics
    idx     int
}

// Mock seeker that can simulate EOF
type mockSeeker struct {
    bitrate       int64
    eofAfterCalls int
    callCount     int
}

func TestGate_Probe_Stable(t *testing.T)                  // Lines 25-55
func TestGate_Probe_Unstable(t *testing.T)                // Lines 57-85
func TestGate_Probe_Critical_EarlyExit(t *testing.T)      // Lines 87-115
func TestGate_Probe_WarmUp_IgnoresEarly(t *testing.T)     // Lines 117-145
func TestGate_Probe_EOF_FastDetection(t *testing.T)       // Lines 147-180 (NEW)
func TestGate_Probe_EOF_ProfileCapture(t *testing.T)      // Lines 182-215 (NEW)
func TestGate_EvaluateSamples(t *testing.T)               // Lines 217-240
func TestGate_HypothesisAnalysis(t *testing.T)            // Lines 242-275 (NEW)
```

**Milestone 5**: ✓ Build + ✓ Test + ✓ **EOF Detection Test**
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestGate
cd contrib/performance && go test -v -run TestGate_Probe_EOF
```

---

## Phase 6: Search Loop (Outer Loop)

**Goal**: Implement the search algorithm with AIMD and monotonic bounds

**Duration**: Day 11-12

### Definition of Done (Phase 6)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `search.go` and `proof.go` compile |
| **Monotonicity** | `TestSearchLoop_Monotonicity` passes (no invariant violations) |
| **Ramping** | `TestSearchLoop_Ramp` verifies 2s linear ramp before probe |
| **Ceiling Proof** | `TestSearchLoop_CeilingProven` requires two-failure bracket |
| **Property Tests** | `TestSearchLoop_PropertyBased` runs 1000 scenarios |
| **What Failure Looks Like** | `INVARIANT VIOLATION [BOUNDS_CROSSED]: low(350M) >= high(350M)` |
| **On Failure, You Get** | SearchResult with Status=Failed, Artifacts (config, probes, violation) |

**Key Principle**: SearchLoop NEVER interprets metrics. It only:
1. Manages the probe lifecycle (ramp → record probeStart → call Gate)
2. Updates bounds based on Gate's verdict
3. Enforces monotonic invariants (returns error, not panic)

**SearchLoop uses interfaces** for testability:
```go
s := NewSearchLoop(config, timing, gate)  // gate is Gate interface
// Unit tests use FakeGate, integration tests use real StabilityGate
```

---

### Step 6.1: Search Loop with Multi-Stage Ramping

**Critical Improvement**: Moving directly from `lastStableBitrate` to `probeBitrate` can
cause immediate instability due to SRT's congestion control reacting to sudden traffic
spikes. This creates false negatives (bitrate appears unstable when it's actually the
*transition* that's unstable).

**Solution**: Implement a warm-up ramp within each probe:
1. **Phase A**: Gradually ramp from last stable → target over 2 seconds
2. **Phase B**: Only then start the 5-second stability evaluation

**File**: `contrib/performance/search.go` (~280 lines)

```go
// Lines 1-25: Package and imports
package main

import (
    "context"
    "fmt"
    "time"
)

// Lines 27-50: SearchResult struct (enhanced)
type SearchResult struct {
    Status       SearchStatus
    Ceiling      int64
    Proven       bool
    Metrics      StabilityMetrics
    Probes       []ProbeRecord

    // Search statistics
    TotalProbes  int
    TotalTime    time.Duration
    LastStable   int64           // Last known stable bitrate
    FirstUnstable int64          // First bitrate that failed
}

type SearchStatus int
const (
    StatusConverged SearchStatus = iota
    StatusTimeout
    StatusCancelled
    StatusFailed
)

// Lines 52-85: SearchLoop struct (enhanced)
type SearchLoop struct {
    config    SearchConfig
    gate      *StabilityGate
    seeker    *SeekerControl   // Direct seeker control for ramping
    reporter  *Reporter

    // Bounds (monotonic invariants)
    low       int64   // Last PROVEN stable bitrate
    high      int64   // Last PROVEN unstable bitrate

    // Ramping configuration
    rampDuration  time.Duration  // 2 seconds default
    rampSteps     int            // Number of steps in ramp (20 = 100ms each)

    // History
    probes    []ProbeRecord
    startTime time.Time

    // Last stable point (for ramping from known-good state)
    lastStableBitrate int64
}

// Lines 87-105: ProbeRecord (for replay)
type ProbeRecord struct {
    Number        int
    TargetBitrate int64
    RampedFrom    int64           // NEW: What bitrate we ramped from
    Low           int64
    High          int64
    Result        ProbeResult
    RampDuration  time.Duration   // NEW: Actual ramp time
}

// Lines 107-125: Constructor
func NewSearchLoop(config SearchConfig, gate *StabilityGate,
                   seeker *SeekerControl, reporter *Reporter) *SearchLoop {
    return &SearchLoop{
        config:       config,
        gate:         gate,
        seeker:       seeker,
        reporter:     reporter,
        low:          0,
        high:         config.MaxBitrate,
        rampDuration: 2 * time.Second,
        rampSteps:    20,  // 100ms per step
        lastStableBitrate: config.InitialBitrate,
    }
}

// Lines 127-220: Run (main search algorithm with ramping)
func (s *SearchLoop) Run(ctx context.Context) SearchResult {
    s.startTime = time.Now()
    current := s.config.InitialBitrate
    probeNumber := 0

    for {
        probeNumber++

        // Check termination conditions
        if s.high - s.low <= s.config.Precision {
            return s.proveCeiling(ctx)
        }

        if time.Since(s.startTime) > s.config.Timeout {
            return SearchResult{
                Status:      StatusTimeout,
                Ceiling:     s.low,
                TotalProbes: probeNumber - 1,
                TotalTime:   time.Since(s.startTime),
                LastStable:  s.lastStableBitrate,
            }
        }

        // === MULTI-STAGE PROBE ===
        s.reporter.ProbeStart(probeNumber, current, s.low, s.high)

        // Phase A: Ramp from last stable to target (if different)
        rampedFrom := s.lastStableBitrate
        if current != s.lastStableBitrate {
            if err := s.rampToTarget(ctx, s.lastStableBitrate, current); err != nil {
                return SearchResult{Status: StatusCancelled}
            }
        }

        // Phase B: Record probe start time AFTER ramp completes
        // This is the "truth source" - Gate's warm-up starts from HERE
        probeStart := time.Now()

        // Phase C: Run stability gate probe at target bitrate
        // Gate owns warm-up, stability evaluation, EOF detection
        // SearchLoop does NOT interpret any metrics - only the verdict
        result := s.gate.Probe(ctx, probeStart, current)

        // Record probe
        record := ProbeRecord{
            Number:        probeNumber,
            TargetBitrate: current,
            RampedFrom:    rampedFrom,
            Low:           s.low,
            High:          s.high,
            Result:        result,
        }
        s.probes = append(s.probes, record)

        s.reporter.ProbeEnd(probeNumber, current, result)

        if result.Cancelled {
            return SearchResult{Status: StatusCancelled}
        }

        // Update bounds based on result
        if result.Stable {
            // INVARIANT: low only increases
            s.low = max(s.low, current)
            s.lastStableBitrate = current  // Update last known good

            // Additive increase
            current = s.nextProbeUp(current)
        } else {
            // INVARIANT: high only decreases
            s.high = min(s.high, current)

            // Multiplicative decrease from LAST STABLE (not current)
            // This prevents cascading failures
            current = s.nextProbeDown(s.lastStableBitrate, result.Critical)
        }

        // Clamp and check invariants
        current = clamp(current, s.config.MinBitrate, s.config.MaxBitrate)
        s.checkInvariants()
    }
}

// Lines 222-265: rampToTarget (gradual bitrate transition)
func (s *SearchLoop) rampToTarget(ctx context.Context, from, to int64) error {
    if from == to {
        return nil
    }

    stepDuration := s.rampDuration / time.Duration(s.rampSteps)
    stepSize := (to - from) / int64(s.rampSteps)

    current := from
    for i := 0; i < s.rampSteps; i++ {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        current += stepSize
        if err := s.seeker.SetBitrate(current); err != nil {
            return err
        }

        // Send heartbeat during ramp
        s.seeker.Heartbeat()

        time.Sleep(stepDuration)
    }

    // Ensure we hit exact target
    return s.seeker.SetBitrate(to)
}

// Lines 267-290: nextProbeUp (AIMD additive increase)
func (s *SearchLoop) nextProbeUp(current int64) int64 {
    // If we have an upper bound, binary search
    if s.high < s.config.MaxBitrate {
        return (s.low + s.high) / 2
    }

    // Otherwise, additive increase
    return current + s.config.StepSize
}

// Lines 292-315: nextProbeDown (AIMD multiplicative decrease)
func (s *SearchLoop) nextProbeDown(lastStable int64, wasCritical bool) int64 {
    // Decrease from LAST STABLE, not from failed bitrate
    // This prevents spiral-down when we overshoot significantly

    factor := s.config.DecreasePercent
    if wasCritical {
        factor = s.config.CriticalPercent
    }

    // Calculate new target as percentage above last stable
    // e.g., if lastStable=300, decrease=0.25, try 300 * 1.125 = 337.5
    // (This is gentler than dropping below last stable)
    multiplier := 1.0 + (1.0-factor)/2
    newTarget := int64(float64(lastStable) * multiplier)

    // But never exceed what just failed
    return min(newTarget, s.high-s.config.StepSize)
}

// Lines 317-340: checkInvariants
func (s *SearchLoop) checkInvariants() {
    if s.low > s.high {
        panic(fmt.Sprintf("invariant violation: low(%d) > high(%d)", s.low, s.high))
    }
    if s.low < 0 {
        panic(fmt.Sprintf("invariant violation: low(%d) < 0", s.low))
    }
    if s.high > s.config.MaxBitrate {
        panic(fmt.Sprintf("invariant violation: high(%d) > max(%d)", s.high, s.config.MaxBitrate))
    }
}

// Lines 342-350: helper
func clamp(v, min, max int64) int64 {
    if v < min { return min }
    if v > max { return max }
    return v
}
```

**Test File**: `contrib/performance/search_test.go` (~150 lines)

```go
// Mock gate for testing
type mockGate struct {
    stableBelowBitrate int64
}

func TestSearch_Monotonicity_LowOnlyIncreases(t *testing.T)   // Lines 20-45
func TestSearch_Monotonicity_HighOnlyDecreases(t *testing.T)  // Lines 47-70
func TestSearch_AIMD_AdditiveIncrease(t *testing.T)           // Lines 72-95
func TestSearch_AIMD_MultiplicativeDecrease(t *testing.T)     // Lines 97-120
func TestSearch_BinarySearch_WhenBoundsKnown(t *testing.T)    // Lines 122-150
func TestSearch_Convergence(t *testing.T)                     // Lines 152-180
```

**Checkpoint 6.1**: ✓ Build + ✓ Test
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestSearch
```

---

### Step 6.2: Proof Phase

**File**: `contrib/performance/proof.go` (~80 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "context"
)

// Lines 17-35: proveCeiling
func (s *SearchLoop) proveCeiling(ctx context.Context) SearchResult

// Lines 37-60: jitterTest (for 4K ProRes burn-in)
func (s *SearchLoop) jitterTest(ctx context.Context, ceiling int64) bool

// Lines 62-80: Integration into SearchLoop.Run()
// (Update Run() to call proveCeiling when bounds converge)
```

**Test File**: `contrib/performance/proof_test.go` (~60 lines)

```go
func TestProof_ExtendedWindow(t *testing.T)       // Lines 10-35
func TestProof_FailureRestartsSearch(t *testing.T) // Lines 37-60
func TestProof_JitterTest(t *testing.T)           // Lines 62-85
```

**Checkpoint 6.2**: ✓ Build + ✓ Test
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestProof
```

---

## Phase 7: Reporter & Replay

**Goal**: Output results, validate hypotheses, and support replay mode for debugging

**Duration**: Day 13-14

### Definition of Done (Phase 7)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | Final report prints to terminal with hypothesis analysis |
| **JSON Output** | `-output json` produces valid JSON with all fields |
| **Hypothesis Detection** | `TestReporter_HypothesisValidation` flags correct bottleneck |
| **Replay Works** | `TestReplay_DeterministicResults` produces same output |
| **What Failure Looks Like** | `hypothesis 2 flagged but TE=0.98 (should be <0.95)` |

---

### Step 7.1: Reporter with Automated Hypothesis Validation

**Critical Improvement**: The final report should do more than output a ceiling - it should
validate the six hypotheses from `performance_maximization_500mbps.md` to help diagnose
*why* a particular ceiling was reached.

**File**: `contrib/performance/reporter.go` (~250 lines)

```go
// Lines 1-25: Package and imports
package main

import (
    "encoding/json"
    "fmt"
    "os"
    "strings"
    "time"
)

// Lines 27-45: ReportMode enum
type ReportMode int
const (
    ReportTerminal ReportMode = iota
    ReportJSON
    ReportQuiet
)

// Lines 47-75: Reporter struct (enhanced)
type Reporter struct {
    mode      ReportMode
    startTime time.Time
    probes    []ProbeRecord

    // Hypothesis tracking
    hypotheses []HypothesisEvidence
}

// Lines 77-95: HypothesisEvidence
type HypothesisEvidence struct {
    ID          int       // 1-6 from performance_maximization_500mbps.md
    Name        string    // e.g., "Flow Control Window Exhaustion"
    Triggered   bool      // Was this hypothesis flagged?
    Confidence  string    // "HIGH", "MEDIUM", "LOW"
    Evidence    []string  // Supporting observations
    Suggestion  string    // What to try next
}

// Lines 97-115: Constructor
func NewReporter(mode ReportMode) *Reporter

// Lines 117-140: ProbeStart
func (r *Reporter) ProbeStart(number int, bitrate int64, low, high int64)

// Lines 142-175: ProbeEnd (collect hypothesis evidence)
func (r *Reporter) ProbeEnd(number int, bitrate int64, result ProbeResult) {
    // ... existing logging ...

    // Collect evidence for hypothesis validation
    if !result.Stable {
        r.collectHypothesisEvidence(bitrate, result)
    }
}

// Lines 177-250: FinalReport (with hypothesis validation)
func (r *Reporter) FinalReport(result SearchResult) {
    switch r.mode {
    case ReportTerminal:
        r.printTerminalReport(result)
    case ReportJSON:
        r.printJSONReport(result)
    case ReportQuiet:
        fmt.Printf("%d\n", result.Ceiling)
    }
}

// Lines 252-350: printTerminalReport (enhanced)
func (r *Reporter) printTerminalReport(result SearchResult) {
    fmt.Println()
    fmt.Println("╔═══════════════════════════════════════════════════════════════════════════╗")
    fmt.Println("║                           FINAL RESULTS                                    ║")
    fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
    fmt.Printf("║  Maximum Sustainable Throughput: %d Mb/s", result.Ceiling/1_000_000)
    if result.Proven {
        fmt.Println(" (PROVEN) ✓")
    } else {
        fmt.Println(" (unverified)")
    }
    fmt.Println("║                                                                            ║")
    fmt.Printf("║  Search Statistics:                                                        \n")
    fmt.Printf("║    Total Probes:     %d                                                   \n", result.TotalProbes)
    fmt.Printf("║    Total Time:       %v                                                   \n", result.TotalTime.Round(time.Second))
    fmt.Printf("║    Final Bounds:     [%d, %d) Mb/s                                        \n",
        result.LastStable/1_000_000, result.FirstUnstable/1_000_000)

    // === HYPOTHESIS VALIDATION SECTION ===
    fmt.Println("╠═══════════════════════════════════════════════════════════════════════════╣")
    fmt.Println("║                      BOTTLENECK HYPOTHESIS ANALYSIS                        ║")
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
                fmt.Printf("║     • %s\n", e)
            }
            fmt.Printf("║     → %s\n", h.Suggestion)
            fmt.Println("║                                                                            ║")
        }
    }

    if triggeredCount == 0 {
        fmt.Println("║  ✓ No specific bottleneck identified                                       ║")
        fmt.Println("║    System may be at hardware/network limit                                 ║")
    }

    fmt.Println("╚═══════════════════════════════════════════════════════════════════════════╝")
}

// Lines 352-380: HypothesisModel (configurable thresholds)
// All thresholds are configurable and included in JSON report for interpretability
type HypothesisModel struct {
    // Hypothesis 1: Flow Control Window Exhaustion
    H1_NAKRateThreshold      float64       `json:"h1_nak_rate_threshold"`       // Default: 0.02 (2%)
    H1_TimeToFailureThreshold time.Duration `json:"h1_time_to_failure_threshold"` // Default: 5s

    // Hypothesis 2: EventLoop Starvation
    H2_TEThreshold           float64       `json:"h2_te_threshold"`             // Default: 0.95
    H2_GapRateThreshold      float64       `json:"h2_gap_rate_threshold"`       // Default: 0.001 (0.1%)

    // Hypothesis 3: ACK Processing Latency
    H3_RTTThreshold          float64       `json:"h3_rtt_threshold_ms"`         // Default: 20.0ms

    // Hypothesis 4: io_uring Backpressure
    H4_TEThreshold           float64       `json:"h4_te_threshold"`             // Default: 0.95
    H4_GapRateThreshold      float64       `json:"h4_gap_rate_threshold"`       // Default: 0.001

    // Hypothesis 5: GC/Memory Pressure
    H5_RTTVarianceThreshold  float64       `json:"h5_rtt_variance_threshold_ms"` // Default: 10.0ms
}

func DefaultHypothesisModel() HypothesisModel {
    return HypothesisModel{
        H1_NAKRateThreshold:       0.02,
        H1_TimeToFailureThreshold: 5 * time.Second,
        H2_TEThreshold:            0.95,
        H2_GapRateThreshold:       0.001,
        H3_RTTThreshold:           20.0,
        H4_TEThreshold:            0.95,
        H4_GapRateThreshold:       0.001,
        H5_RTTVarianceThreshold:   10.0,
    }
}

// Lines 382-450: collectHypothesisEvidence (using HypothesisModel)
func (r *Reporter) collectHypothesisEvidence(bitrate int64, result ProbeResult) {
    m := result.Metrics
    te := m.ThroughputRatio
    h := r.hypothesisModel  // Use configurable thresholds

    // Hypothesis 1: Flow Control Window Exhaustion
    if m.NAKRate > h.H1_NAKRateThreshold && result.TimeToFailure < h.H1_TimeToFailureThreshold {
        r.addEvidence(1, "Flow Control Window Exhaustion", "HIGH",
            []string{
                fmt.Sprintf("NAK rate %.2f%% > %.2f%% threshold", m.NAKRate*100, h.H1_NAKRateThreshold*100),
                fmt.Sprintf("Failed within %.1fs < %.1fs threshold",
                    result.TimeToFailure.Seconds(), h.H1_TimeToFailureThreshold.Seconds()),
            },
            "Increase FC to 204800+, check ACK processing latency")
    }

    // Hypothesis 2: EventLoop Starvation
    if te < h.H2_TEThreshold && m.GapRate < h.H2_GapRateThreshold {
        r.addEvidence(2, "Sender EventLoop Starvation", "HIGH",
            []string{
                fmt.Sprintf("Throughput efficiency %.1f%% < %.1f%% threshold", te*100, h.H2_TEThreshold*100),
                fmt.Sprintf("Gap rate %.3f%% < %.3f%% threshold (no network loss)",
                    m.GapRate*100, h.H2_GapRateThreshold*100),
            },
            "Profile deliverReadyPacketsEventLoop(), reduce BackoffMaxSleep")
    }

    // Hypothesis 3: ACK Processing Latency
    if m.RTT > h.H3_RTTThreshold {
        r.addEvidence(3, "ACK Processing Latency", "MEDIUM",
            []string{
                fmt.Sprintf("RTT %.1fms > %.1fms threshold", m.RTT, h.H3_RTTThreshold),
            },
            "Profile ackBtree(), increase PeriodicAckIntervalMs")
    }

    // Hypothesis 4: io_uring Backpressure
    if te < h.H4_TEThreshold && m.GapRate > h.H4_GapRateThreshold {
        r.addEvidence(4, "io_uring Completion Backpressure", "MEDIUM",
            []string{
                fmt.Sprintf("Throughput efficiency %.1f%% < %.1f%% threshold", te*100, h.H4_TEThreshold*100),
                fmt.Sprintf("Gap rate %.3f%% > %.3f%% threshold (with packet loss)",
                    m.GapRate*100, h.H4_GapRateThreshold*100),
            },
            "Increase IoUringRecvRingSize to 32768+, reduce ring count")
    }

    // Hypothesis 5: GC/Memory Pressure
    if m.RTTVariance > h.H5_RTTVarianceThreshold {
        r.addEvidence(5, "GC/Memory Pressure", "MEDIUM",
            []string{
                fmt.Sprintf("RTT variance %.1fms > %.1fms threshold", m.RTTVariance, h.H5_RTTVarianceThreshold),
            },
            "Run with GODEBUG=gctrace=1, check heap.pprof")
    }

    // Hypothesis 6: Metrics Lock Contention
    // (Harder to detect automatically, flag if other hypotheses don't explain failure)
    if te > 0.95 && m.GapRate < 0.001 && m.RTT < 10.0 && result.EOFDetected {
        r.addEvidence(6, "Possible Metrics/Lock Contention", "LOW",
            []string{
                "No obvious bottleneck from metrics",
                "EOF without clear cause",
            },
            "Profile mutex.pprof, check for per-packet atomic contention")
    }
}

// Lines 422-440: addEvidence
func (r *Reporter) addEvidence(id int, name, confidence string, evidence []string, suggestion string) {
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

// Lines 442-480: printJSONReport (includes hypotheses)
func (r *Reporter) printJSONReport(result SearchResult)

// Lines 482-500: SaveProbes (for replay)
func (r *Reporter) SaveProbes(path string) error
```

**Example Final Report Output**:

```
╔═══════════════════════════════════════════════════════════════════════════╗
║                           FINAL RESULTS                                    ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  Maximum Sustainable Throughput: 355 Mb/s (PROVEN) ✓
║                                                                            ║
║  Search Statistics:
║    Total Probes:     19
║    Total Time:       2m 35s
║    Final Bounds:     [355, 360) Mb/s
╠═══════════════════════════════════════════════════════════════════════════╣
║                      BOTTLENECK HYPOTHESIS ANALYSIS                        ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  🔴 HYPOTHESIS 2: Sender EventLoop Starvation
║     Confidence: HIGH
║     • Throughput efficiency 91.2% < 95%
║     • Gap rate 0.001% ≈ 0 (no network loss)
║     → Profile deliverReadyPacketsEventLoop(), reduce BackoffMaxSleep
║                                                                            ║
║  🟡 HYPOTHESIS 3: ACK Processing Latency
║     Confidence: MEDIUM
║     • RTT 22.5ms > 20ms
║     → Profile ackBtree(), increase PeriodicAckIntervalMs
║                                                                            ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

**Checkpoint 7.1**: ✓ Build
```bash
cd contrib/performance && go build ./...
```

---

### Step 7.2: Replay Mode

**File**: `contrib/performance/replay.go` (~100 lines)

```go
// Lines 1-15: Package and imports
package main

import (
    "context"
    "encoding/json"
    "os"
)

// Lines 17-30: ReplayGate struct (mock gate for replay)
type ReplayGate struct {
    probeResults map[int64]ProbeResult  // bitrate -> result
}

// Lines 32-45: Constructor
func NewReplayGate(probePath string) (*ReplayGate, error)

// Lines 47-65: Probe (return recorded result)
func (rg *ReplayGate) Probe(ctx context.Context, bitrate int64) ProbeResult

// Lines 67-85: LoadProbes
func LoadProbes(path string) ([]ProbeRecord, error)

// Lines 87-100: interpolate (for bitrates not in recording)
func (rg *ReplayGate) interpolate(bitrate int64) ProbeResult
```

**Test File**: `contrib/performance/replay_test.go` (~60 lines)

```go
func TestReplay_DeterministicResults(t *testing.T)  // Lines 10-35
func TestReplay_EdgeCases(t *testing.T)             // Lines 37-60
```

**Checkpoint 7.2**: ✓ Build + ✓ Test
```bash
cd contrib/performance && go build ./...
cd contrib/performance && go test -v -run TestReplay
```

---

### Step 7.3: Complete Main Integration

**File**: `contrib/performance/main.go` (Final ~150 lines)

Update to include:
- All component wiring
- Replay mode flag
- Report mode flag
- Full test execution

**Milestone 7**: ✓ Build + ✓ End-to-End Test
```bash
cd contrib/performance && go build -o performance

# Full test (requires server and client-seeker binaries)
./performance INITIAL=100M MAX=200M STEP=20M

# Expected output:
# - Starts server and seeker
# - Runs probes
# - Reports ceiling
# - Exits cleanly
```

---

## Phase 8: Makefile Integration & Final Polish

**Goal**: Integration with build system and CI-ready targets

**Duration**: Day 15

### Definition of Done (Phase 8)

| Criteria | Verification |
|----------|--------------|
| **Output Artifact** | `make test-performance` runs full search |
| **No Sudo** | Entire test runs without root privileges |
| **CI Integration** | `make test-performance-quick` completes in <5min |
| **Baseline Compare** | `make test-performance BASELINE=baseline.json` reports delta |
| **What Failure Looks Like** | `REGRESSION: ceiling dropped 15% (350M → 297M)` |

---

### Step 8.1: Makefile Updates

**File**: `Makefile` (Add ~30 lines)

```makefile
# Lines to add after existing targets

# Build performance tools
.PHONY: build-performance
build-performance:
	cd contrib/client-seeker && go build -o client-seeker
	cd contrib/performance && go build -o performance

# Run performance test (no sudo required!)
.PHONY: test-performance
test-performance: build-performance
	./contrib/performance/performance $(PERF_ARGS)

# Run with replay
.PHONY: test-performance-replay
test-performance-replay:
	./contrib/performance/performance --replay $(REPLAY_FILE) $(PERF_ARGS)
```

**Milestone 8**: ✓ Build + ✓ Full Integration Test
```bash
# From project root
make build-performance
make test-performance INITIAL=100M MAX=200M STEP=20M
```

---

## Summary: Verification Points

### Critical Path Checkpoints

| Phase | Checkpoint | Command | **MUST PASS** |
|-------|------------|---------|---------------|
| 1.2 | TokenBucket 500Mbps precision | `go test -v -run TestTokenBucket_RateAccuracy_500Mbps` | ⚠️ **BLOCKER** |
| 1.2 | TokenBucket jitter test | `go test -v -run TestTokenBucket_Jitter_500Mbps` | ⚠️ **BLOCKER** |
| 2.1 | ControlServer tests | `go test -v -run TestControlServer` | |
| 2.2 | Watchdog tests | `go test -v -run TestWatchdog` | |
| 3 | Manual integration | Start server + seeker, verify connection | |
| 4.1 | Config parser tests | `go test -v -run TestParse` | |
| 4.2 | Contracts tests | `go test -v -run TestContracts` | |
| 5.1 | Metrics tests | `go test -v -run TestMetrics` | |
| 5.4 | Gate EOF detection | `go test -v -run TestGate_Probe_EOF` | ⚠️ **IMPORTANT** |
| 5.4 | Gate hypothesis analysis | `go test -v -run TestGate_HypothesisAnalysis` | |
| 6.1 | Search AIMD tests | `go test -v -run TestSearch` | |
| 6.1 | Search ramping tests | `go test -v -run TestSearch_Ramp` | |
| 6.2 | Proof tests | `go test -v -run TestProof` | |
| 7.2 | Replay tests | `go test -v -run TestReplay` | |
| 8 | Full integration | `make test-performance INITIAL=100M` | |

### Strategic Verification: "Can We Even Reach 500 Mb/s?"

Before proceeding past Phase 1, verify that the generator can sustain the target rate:

```bash
# This MUST pass with <1% error and <50% jitter
cd contrib/client-seeker && go test -v -run TestTokenBucket_RateAccuracy_500Mbps
cd contrib/client-seeker && go test -v -run TestTokenBucket_Jitter_500Mbps

# If either fails, the SRT protocol will fail regardless of tuning
# Debug with: go test -v -run TestTokenBucket -cpuprofile cpu.prof
#             go tool pprof -http=:8080 cpu.prof
```

---

## Estimated Line Counts (Updated)

| Component | Files | Lines | Change |
|-----------|-------|-------|--------|
| client-seeker | 9 | ~920 | +60 (high-precision TokenBucket) |
| client-seeker tests | 4 | ~440 | +100 (500Mbps precision tests) |
| performance | 11 | ~1,550 | +210 (enhanced gate, reporter) |
| performance tests | 7 | ~730 | +100 (EOF, hypothesis tests) |
| Makefile additions | 1 | ~30 | |
| **Total** | **32** | **~3,670** | +470 lines |

---

## Risk Mitigation

### Phase 1 Risks (Most Critical)

| Risk | Symptom | Mitigation |
|------|---------|------------|
| TokenBucket too bursty | 500Mbps jitter test fails | Switch to `RefillSpin` mode, profile hot path |
| OS scheduler granularity | Sub-ms sleeps inaccurate | Use hybrid spin+sleep, `runtime.LockOSThread()` |
| GC during refill loop | Periodic jitter spikes | Reduce allocations, increase `GOGC` |

### Phase 5 Risks

| Risk | Symptom | Mitigation |
|------|---------|------------|
| EOF not detected fast enough | Profiles capture post-termination | Reduce fastPollInterval to 25ms |
| Profile capture races process exit | Empty profiles | Add process keep-alive during capture |
| Hypothesis misidentified | Wrong bottleneck flagged | Add more evidence requirements |

### Phase 6 Risks

| Risk | Symptom | Mitigation |
|------|---------|------------|
| Ramp causes instability | Every probe fails during ramp | Increase rampDuration to 3s |
| AIMD oscillates near ceiling | Bounds don't converge | Reduce DecreasePercent to 0.15 |
| Binary search stuck | Same bitrate probed repeatedly | Add deadlock detection |

### If Integration Fails

1. **Process spawning** - Check binary paths, permissions, use `exec.LookPath()`
2. **UDS sockets** - Verify socket cleanup with `defer os.Remove()`, check permissions
3. **Prometheus scraping** - Add timeout, check endpoint format, log raw response

---

## Implementation Order Rationale

The phases are ordered to **fail fast** on fundamental issues:

1. **Phase 1-2 (Client-Seeker)**: If we can't generate smooth 500 Mb/s traffic, nothing else matters
2. **Phase 3 (SRT Integration)**: Verify SRT connection works before building orchestrator
3. **Phase 4 (Orchestrator Foundation)**: Config, TimingModel, readiness barrier
4. **Phase 5 (Gate - THE CRITICAL ORACLE)**: Make Gate rock-solid with replay/mock tests
5. **Phase 6 (Search Loop)**: Once Gate is solid, SearchLoop is just plumbing + bounds
6. **Phase 7-8 (Reporting + Polish)**: Polish and CI integration

### Re-Ordering Note (Reduced Risk)

**Gate tests should be comprehensive BEFORE SearchLoop complexity.**

Once the StabilityGate is rock-solid and fully tested with mocks/replay, the SearchLoop
becomes much simpler - it's just:
- Ramp management
- Calling Gate and getting a verdict
- Updating monotonic bounds

The SearchLoop test suite outlined in Step 6.1 relies heavily on mocked Gate responses.
This dependency is intentional: it lets us test SearchLoop logic in isolation.

**Do NOT skip Phase 1.2 precision tests** - they are the foundation.

---

## Ready for Implementation?

Please review this plan and let me know:

1. **Scope** - Is this the right level of detail?
2. **Order** - Should any phases be reordered?
3. **Tests** - Are the test cases sufficient?
4. **Timeline** - Is the pacing reasonable (~15 days for ~3,700 lines)?

Once approved, we start with **Phase 1, Step 1.1: Protocol Types**.
