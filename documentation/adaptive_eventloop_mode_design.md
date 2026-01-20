# Adaptive EventLoop Mode Design

## Table of Contents

- [Problem Statement](#problem-statement)
- [Test Results (Confirmed)](#test-results-confirmed)
- [Design Goals](#design-goals)
- [Existing Infrastructure](#existing-infrastructure)
- [Mode Definitions](#mode-definitions)
- [Chosen Strategy: Start Yield, Relax to Sleep When Idle](#chosen-strategy-start-yield-relax-to-sleep-when-idle)
- [Alternative Strategies Considered](#alternative-strategies-considered)
- [Detailed Design](#detailed-design)
  - [Mode State Machine](#mode-state-machine)
  - [Implementation](#implementation)
  - [Integration Points](#integration-points)
  - [Configuration (Optional Override)](#configuration-optional-override)
- [Risk Analysis: What Could Go Wrong?](#risk-analysis-what-could-go-wrong)
- [Comprehensive Testing Plan](#comprehensive-testing-plan)
  - [Unit Tests](#unit-tests-adaptive_backoff_testgo)
  - [Table-Driven Tests](#table-driven-tests)
  - [Race Tests](#race-tests-adaptive_backoff_race_testgo)
  - [Benchmark Tests](#benchmark-tests-adaptive_backoff_bench_testgo)
  - [Integration Tests](#integration-tests)
  - [Existing Test Impact Analysis](#existing-test-impact-analysis)
  - [New Makefile Targets](#new-makefile-targets)
- [Implementation Phases (TDD Approach)](#implementation-phases-tdd-approach)
  - [Phase 1: Core Types & Tests First](#phase-1-core-types--tests-first-tdd)
  - [Phase 2: Sender EventLoop Integration](#phase-2-sender-eventloop-integration)
  - [Phase 3: Benchmarks & Validation](#phase-3-benchmarks--validation)
  - [Phase 4: Receiver Integration](#phase-4-receiver-integration)
  - [Phase 5: Metrics & Observability](#phase-5-metrics--observability)
  - [Phase 6: Configuration & CLI](#phase-6-configuration--cli)
  - [Phase 7: Integration Testing](#phase-7-integration-testing)
  - [Phase 8: Documentation](#phase-8-documentation)
- [Success Criteria Checklist](#success-criteria-checklist)
- [Expected Results](#expected-results)
- [Open Questions](#open-questions)
- [References](#references)

---

## Problem Statement

The SRT library needs to efficiently handle a wide range of throughput:
- **Low throughput** (<20 Mb/s): CPU efficiency matters, sleeping is fine
- **Medium throughput** (20-200 Mb/s): Balance between CPU and latency
- **High throughput** (>200 Mb/s): Latency/throughput matters, sleeping is the bottleneck

The current fixed `time.Sleep()` approach caps iteration rate at ~945/sec due to OS scheduler granularity, limiting throughput to ~375 Mb/s even when CPU isn't maxed.

## Test Results (Confirmed)

```
NoWait:       109,526,570 iterations/sec
Yield:          6,219,705 iterations/sec (+6581x vs Sleep!)
Spin:              98,374 iterations/sec
Sleep:                945 iterations/sec  ← Current bottleneck
```

## Design Goals

1. **Adaptive**: Automatically detect throughput and switch modes
2. **Conservative start**: Begin CPU-friendly, escalate when needed
3. **No configuration required**: Works out of the box for any use case
4. **Graceful degradation**: Falls back to lower modes when idle
5. **Leverage existing metrics**: Use the rate calculations already in EventLoop

## Existing Infrastructure

The library already calculates rates in the EventLoop (see `sender_lockfree_architecture.md`):

```go
// From eventloop.go - rate calculation already exists
if time.Since(lastRateCalc) >= s.eventLoopRateInterval {
    // Calculate packets/sec, bytes/sec
    m.SendEventLoopPacketsPerSec.Store(pps)
    m.SendEventLoopBytesPerSec.Store(bps)
}
```

We can leverage these existing metrics for mode switching.

## Mode Definitions

| Mode | Mechanism | CPU Impact | Latency | Use When |
|------|-----------|------------|---------|----------|
| **Sleep** | `time.Sleep(duration)` | Minimal | ~1ms+ | <20 Mb/s |
| **Yield** | `runtime.Gosched()` | Low-Medium | ~1-10µs | 20-200 Mb/s |
| **Spin** | Busy loop + occasional yield | High | <1µs | >200 Mb/s |

## Chosen Strategy: Start Yield, Relax to Sleep When Idle

**Decision**: We will implement a two-mode adaptive system (Yield + Sleep) that:
1. **Starts in Yield mode** - Ready for any throughput immediately
2. **Relaxes to Sleep when idle** - Saves CPU when no traffic for 1 second
3. **Wakes immediately on activity** - Any packet triggers return to Yield

### Why This Strategy?

| Requirement | How Strategy Addresses It |
|-------------|--------------------------|
| Support <20 Mb/s efficiently | Sleep mode when idle saves CPU |
| Support >300 Mb/s | Yield mode provides 6.2M iter/sec (144x headroom) |
| No configuration needed | Auto-detects based on activity |
| Quick burst response | Starts in Yield, ready immediately |
| Graceful degradation | Falls back to Sleep when idle |

### Key Design Decisions

1. **Two modes only (Yield + Sleep)**: SPIN mode adds complexity and high CPU usage. Yield's 6.2M iter/sec provides 144x headroom over 500 Mb/s - SPIN is unnecessary.

2. **Start in Yield**: User is connecting for a reason - they have data! Starting conservative (Sleep) would delay first packets.

3. **1 second idle threshold**: Long enough to avoid thrashing, short enough to save CPU quickly when stream ends.

4. **Immediate wake on activity**: Any packet triggers instant return to Yield - no delay for burst traffic.

### State Machine

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│    ┌────────────────────┐         any activity              │
│    │      YIELD         │◄──────────────────────────────────┤
│    │   (default start)  │                                   │
│    │   6.2M iter/sec    │                                   │
│    └────────┬───────────┘                                   │
│             │                                               │
│    idle 1s  │ (no packets for 1 second)                     │
│             ▼                                               │
│    ┌────────────────────┐                                   │
│    │      SLEEP         │───────────────────────────────────┘
│    │   (CPU friendly)   │
│    │   945 iter/sec     │
│    └────────────────────┘
│
└─────────────────────────────────────────────────────────────┘
```

---

## Alternative Strategies Considered

<details>
<summary>Click to expand alternative strategies that were evaluated but not chosen</summary>

### Strategy A: Start Conservative, Escalate on Demand
- Start in Sleep, escalate to Yield when busy
- **Rejected**: Delays high-throughput response, first packets suffer

### Strategy C: Rate-Proportional Yielding
- Dynamically adjust yield count based on rate
- **Rejected**: More complex, may oscillate at thresholds

### Strategy D: Hybrid with Warmup
- Yield for 5 seconds, then decide
- **Rejected**: 5-second delay, doesn't adapt to variable traffic

### SPIN Mode
- Busy loop for maximum throughput
- **Rejected**: Yield (6.2M iter/sec) already provides 144x headroom over 500 Mb/s requirement. SPIN adds high CPU usage for no benefit.

</details>

---

## Detailed Design

### Type Definitions

```go
// EventLoopMode represents the current backoff strategy
type EventLoopMode int

const (
    EventLoopModeSleep EventLoopMode = iota  // CPU-friendly, ~945 iter/sec
    EventLoopModeYield                        // High-throughput, ~6.2M iter/sec
)

func (m EventLoopMode) String() string {
    switch m {
    case EventLoopModeSleep:
        return "Sleep"
    case EventLoopModeYield:
        return "Yield"
    default:
        return "Unknown"
    }
}
```

### Core Struct

```go
// adaptiveBackoff manages automatic switching between Sleep and Yield modes
// based on connection activity. Thread-safe via atomic operations.
type adaptiveBackoff struct {
    mode           atomic.Int32  // Current EventLoopMode
    lastActivityNs atomic.Int64  // UnixNano of last packet activity

    // Configuration
    idleThresholdNs int64  // Time without activity before switching to Sleep (default: 1s)
}

// newAdaptiveBackoff creates a new adaptive backoff starting in Yield mode
func newAdaptiveBackoff() *adaptiveBackoff {
    ab := &adaptiveBackoff{
        idleThresholdNs: int64(time.Second), // 1 second idle threshold
    }
    ab.mode.Store(int32(EventLoopModeYield))      // Start in Yield mode
    ab.lastActivityNs.Store(time.Now().UnixNano()) // Initialize activity time
    return ab
}
```

### Wait Implementation

```go
// Wait performs the appropriate wait based on current mode and activity.
// Called once per EventLoop iteration.
//
// Parameters:
//   - hadActivity: true if any packets were processed this iteration
//
// Behavior:
//   - YIELD mode: runtime.Gosched(), check for idle→Sleep transition
//   - SLEEP mode: time.Sleep(), immediate wake on any activity
func (ab *adaptiveBackoff) Wait(hadActivity bool) {
    now := time.Now().UnixNano()

    // Update activity timestamp if we did work
    if hadActivity {
        ab.lastActivityNs.Store(now)
    }

    mode := EventLoopMode(ab.mode.Load())

    switch mode {
    case EventLoopModeYield:
        // Check if we should transition to Sleep (idle for 1s)
        lastActivity := ab.lastActivityNs.Load()
        if now - lastActivity > ab.idleThresholdNs {
            ab.mode.Store(int32(EventLoopModeSleep))
            // Metric: mode switch Yield→Sleep
        }
        runtime.Gosched()

    case EventLoopModeSleep:
        // Any activity immediately wakes us to Yield
        if hadActivity {
            ab.mode.Store(int32(EventLoopModeYield))
            runtime.Gosched()  // Don't sleep, we have work!
            // Metric: mode switch Sleep→Yield
            return
        }
        time.Sleep(100 * time.Microsecond)
    }
}

// Mode returns the current EventLoopMode (for metrics/debugging)
func (ab *adaptiveBackoff) Mode() EventLoopMode {
    return EventLoopMode(ab.mode.Load())
}
```

### Integration Points

#### 1. Sender EventLoop (`congestion/live/send/eventloop.go`)

**Current code (lines 159-164):**
```go
if sleepResult.Duration > 0 {
    time.Sleep(sleepResult.Duration)
    if sleepResult.Duration >= s.backoffMinSleep {
        m.SendEventLoopIdleBackoffs.Add(1)
    }
}
```

**New code:**
```go
// Replace sleep with adaptive backoff
hadActivity := delivered > 0 || controlDrained > 0
s.adaptiveBackoff.Wait(hadActivity)
```

#### 2. Sender struct (`congestion/live/send/sender.go`)

```go
type sender struct {
    // ... existing fields ...

    // Adaptive backoff for EventLoop mode (replaces fixed sleep)
    adaptiveBackoff *adaptiveBackoff
}
```

#### 3. Receiver EventLoop (`congestion/live/receive/eventloop.go`)

Similar integration - replace sleep calls with `adaptiveBackoff.Wait()`.

#### 4. Metrics (for observability)

```go
// New metrics in metrics/metrics.go
EventLoopMode         atomic.Int32   // Current mode (0=Sleep, 1=Yield)
EventLoopModeSwitches atomic.Uint64  // Total mode switches
```

### Configuration (Optional Override)

For testing and special cases, allow manual mode override:

```go
// In config.go
type Config struct {
    // ... existing fields ...

    // EventLoopBackoffMode overrides auto-detection
    // Values: "auto" (default), "sleep", "yield"
    EventLoopBackoffMode string

    // EventLoopIdleThreshold is time without activity before Sleep mode
    // Default: 1s
    EventLoopIdleThreshold time.Duration
}
```

CLI flags:
```bash
-eventloopmode auto|sleep|yield  # Override mode (default: auto)
-eventloopidlethreshold 1s       # Idle time before Sleep (default: 1s)
```

## Risk Analysis: What Could Go Wrong?

### Risk 1: Race Conditions in Mode Switching
**Severity**: High
**Description**: Multiple goroutines accessing mode state concurrently could cause data races.
**Mitigation**:
- Use `atomic.Int32` for mode storage
- Use `atomic.Int64` for timestamps
- No locks needed - pure atomic operations
**Testing**: Run with `-race` flag, add dedicated race tests

### Risk 2: Mode Thrashing at Thresholds
**Severity**: Medium
**Description**: Traffic oscillating around threshold could cause rapid mode switching, adding overhead.
**Mitigation**:
- Hysteresis: Different thresholds for up vs down transitions
- Minimum time between switches (e.g., 500ms)
- Rate averaging over window (not instant)
**Testing**: Table-driven tests with oscillating rates

### Risk 3: Goroutine Starvation in Yield Mode
**Severity**: Medium
**Description**: Tight Yield loop could starve other goroutines on same thread.
**Mitigation**:
- `runtime.Gosched()` explicitly yields to scheduler
- Go runtime handles this well in practice
- Monitor with goroutine profile
**Testing**: Run with GOMAXPROCS=1 to stress-test

### Risk 4: Performance Regression at Low Throughput
**Severity**: Low
**Description**: Extra mode-checking logic could slow down low-rate connections.
**Mitigation**:
- Mode check is single atomic load (nanoseconds)
- Only evaluates transition when activity changes
- Benchmark before/after
**Testing**: Benchmark at 1 Mb/s, 10 Mb/s, compare to baseline

### Risk 5: Metrics Inconsistencies
**Severity**: Low
**Description**: Rate metrics used for mode decisions could be stale or inconsistent.
**Mitigation**:
- Use existing rate calculation interval (1s default)
- Mode decisions are soft - no correctness impact
- Metrics are best-effort for optimization
**Testing**: Unit test with mock metrics

### Risk 6: Existing Tests Breaking
**Severity**: Medium
**Description**: Tests that assume fixed timing behavior may fail.
**Mitigation**:
- Add `EventLoopMode` config override for tests
- Tests can force specific mode
- Backward compatible defaults
**Testing**: Run full test suite before/after

### Risk 7: Benchmark Instability
**Severity**: Medium
**Description**: Benchmarks may produce inconsistent results due to mode switching mid-benchmark.
**Mitigation**:
- Benchmarks should use fixed mode (not auto)
- Add separate benchmarks for each mode
- Document expected variance
**Testing**: Run benchmarks multiple times, check variance

### Risk 8: CPU Usage Spike on Connection Start
**Severity**: Low
**Description**: Starting in Yield mode uses more CPU than Sleep for brief connections.
**Mitigation**:
- Yield is still very efficient (<1% CPU typically)
- Quick fallback to Sleep when idle (1s)
- Trade-off: better UX for real connections
**Testing**: Measure CPU for connect/disconnect cycles

## Comprehensive Testing Plan

### Unit Tests (`adaptive_backoff_test.go`)

```go
// ═══════════════════════════════════════════════════════════════
// Test: Initial State
// ═══════════════════════════════════════════════════════════════

func TestAdaptiveBackoff_StartsInYieldMode(t *testing.T)
// Verify: newAdaptiveBackoff() returns backoff in Yield mode

func TestAdaptiveBackoff_ModeString(t *testing.T)
// Verify: String() returns "Sleep" or "Yield"

// ═══════════════════════════════════════════════════════════════
// Test: Mode Transitions
// ═══════════════════════════════════════════════════════════════

func TestAdaptiveBackoff_YieldToSleep_AfterIdleThreshold(t *testing.T)
// Verify: Transitions to Sleep after 1s of hadActivity=false

func TestAdaptiveBackoff_SleepToYield_OnAnyActivity(t *testing.T)
// Verify: Immediately transitions to Yield when hadActivity=true

func TestAdaptiveBackoff_YieldStaysYield_WithContinuousActivity(t *testing.T)
// Verify: Stays in Yield when hadActivity=true repeatedly

func TestAdaptiveBackoff_SleepStaysSleep_WhenIdle(t *testing.T)
// Verify: Stays in Sleep when hadActivity=false

// ═══════════════════════════════════════════════════════════════
// Test: Edge Cases
// ═══════════════════════════════════════════════════════════════

func TestAdaptiveBackoff_NoThrashing_WithIntermittentActivity(t *testing.T)
// Verify: Brief activity gaps don't cause Sleep transition

func TestAdaptiveBackoff_ConfigurableIdleThreshold(t *testing.T)
// Verify: Can set custom idle threshold (e.g., 500ms or 2s)

// ═══════════════════════════════════════════════════════════════
// Test: Thread Safety
// ═══════════════════════════════════════════════════════════════

func TestAdaptiveBackoff_ConcurrentWait(t *testing.T)
// Verify: Multiple goroutines calling Wait() is safe

func TestAdaptiveBackoff_ConcurrentModeRead(t *testing.T)
// Verify: Mode() can be called while Wait() is running
```

### Table-Driven Tests

```go
// activityEvent represents one iteration of the EventLoop
type activityEvent struct {
    timeOffsetMs int64  // Milliseconds since test start
    hadActivity  bool   // Were packets processed this iteration?
}

func TestAdaptiveBackoff_ModeTransitions(t *testing.T) {
    tests := []struct {
        name           string
        initialMode    EventLoopMode
        events         []activityEvent
        expectedMode   EventLoopMode
        expectSwitch   bool
    }{
        {
            name:        "Yield stays Yield with continuous activity",
            initialMode: EventLoopModeYield,
            events: []activityEvent{
                {0, true},
                {100, true},
                {200, true},
                {500, true},
            },
            expectedMode: EventLoopModeYield,
            expectSwitch: false,
        },
        {
            name:        "Yield to Sleep after 1s idle",
            initialMode: EventLoopModeYield,
            events: []activityEvent{
                {0, true},      // Activity at start
                {500, false},   // Idle at 500ms
                {1000, false},  // Idle at 1s
                {1100, false},  // Idle at 1.1s → should switch
            },
            expectedMode: EventLoopModeSleep,
            expectSwitch: true,
        },
        {
            name:        "Sleep to Yield on single packet",
            initialMode: EventLoopModeSleep,
            events: []activityEvent{
                {0, false},
                {100, true},  // Single packet → immediate wake
            },
            expectedMode: EventLoopModeYield,
            expectSwitch: true,
        },
        {
            name:        "No thrashing with intermittent activity",
            initialMode: EventLoopModeYield,
            events: []activityEvent{
                {0, true},
                {200, false},   // Brief gap
                {400, true},    // Activity resumes
                {600, false},   // Brief gap
                {800, true},    // Activity resumes
                // Should NOT switch - gaps < 1s
            },
            expectedMode: EventLoopModeYield,
            expectSwitch: false,
        },
        {
            name:        "Activity resets idle timer",
            initialMode: EventLoopModeYield,
            events: []activityEvent{
                {0, true},
                {800, false},   // 800ms idle
                {900, true},    // Activity! Resets timer
                {1800, false},  // 900ms since last activity
                {1900, false},  // Still only 1s since activity
                {2000, false},  // NOW 1.1s idle → switch
            },
            expectedMode: EventLoopModeSleep,
            expectSwitch: true,
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            ab := newAdaptiveBackoff()
            ab.mode.Store(int32(tc.initialMode))

            var switched bool
            startTime := time.Now()

            for _, event := range tc.events {
                // Simulate time passing
                eventTime := startTime.Add(time.Duration(event.timeOffsetMs) * time.Millisecond)
                ab.lastActivityNs.Store(eventTime.UnixNano()) // For testing

                oldMode := ab.Mode()
                ab.Wait(event.hadActivity)
                if ab.Mode() != oldMode {
                    switched = true
                }
            }

            require.Equal(t, tc.expectedMode, ab.Mode())
            require.Equal(t, tc.expectSwitch, switched)
        })
    }
}

func TestAdaptiveBackoff_ActivityScenarios(t *testing.T) {
    // Each bool represents 100ms: true=activity, false=idle
    tests := []struct {
        name        string
        activity    []bool  // Activity pattern (1 value per 100ms)
        finalMode   EventLoopMode
        modeChanges int
    }{
        {
            name:        "Continuous activity stays Yield",
            activity:    repeat(true, 50),  // 5s of continuous activity
            finalMode:   EventLoopModeYield,
            modeChanges: 0,
        },
        {
            name:        "Continuous idle transitions to Sleep",
            activity:    repeat(false, 15),  // 1.5s idle → should switch at ~1s
            finalMode:   EventLoopModeSleep,
            modeChanges: 1,
        },
        {
            name:        "Burst then idle",
            activity:    append(repeat(true, 10), repeat(false, 15)...),
            finalMode:   EventLoopModeSleep,
            modeChanges: 1,  // Yield→Sleep after 1s idle
        },
        {
            name:        "Intermittent activity stays Yield",
            activity:    []bool{true, true, false, false, true, true, false, false, true, true},
            finalMode:   EventLoopModeYield,
            modeChanges: 0,  // Gaps < 1s, no switch
        },
        {
            name:        "Sleep wakes immediately on activity",
            activity:    append(repeat(false, 15), true),  // Idle→Sleep, then activity
            finalMode:   EventLoopModeYield,
            modeChanges: 2,  // Yield→Sleep→Yield
        },
    }
    // Implementation: iterate through activity, call Wait(), check mode
}
```

### Race Tests (`adaptive_backoff_race_test.go`)

```go
func TestAdaptiveBackoff_Race_ConcurrentWait(t *testing.T) {
    ab := newAdaptiveBackoff()

    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 1000; j++ {
                ab.Wait(rand.Intn(2) == 0)  // Random activity
            }
        }()
    }
    wg.Wait()
    // Test passes if no race detected
}

func TestAdaptiveBackoff_Race_ModeReadDuringWait(t *testing.T) {
    ab := newAdaptiveBackoff()

    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
    defer cancel()

    // Writers (calling Wait)
    for i := 0; i < 5; i++ {
        go func() {
            for {
                select {
                case <-ctx.Done():
                    return
                default:
                    ab.Wait(rand.Intn(2) == 0)
                }
            }
        }()
    }

    // Readers (checking Mode)
    for i := 0; i < 5; i++ {
        go func() {
            for {
                select {
                case <-ctx.Done():
                    return
                default:
                    _ = ab.Mode()
                }
            }
        }()
    }

    <-ctx.Done()
}
```

### Benchmark Tests (`adaptive_backoff_bench_test.go`)

```go
// Compare overhead of mode checking
func BenchmarkAdaptiveBackoff_ModeCheck(b *testing.B) {
    ab := newAdaptiveBackoff()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = ab.Mode()
    }
}

// Compare Wait() performance in Sleep mode
func BenchmarkAdaptiveBackoff_Wait_Sleep(b *testing.B) {
    ab := newAdaptiveBackoff()
    ab.mode.Store(int32(EventLoopModeSleep))
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        ab.Wait(false)  // No activity → stays in Sleep
    }
}

// Compare Wait() performance in Yield mode
func BenchmarkAdaptiveBackoff_Wait_Yield(b *testing.B) {
    ab := newAdaptiveBackoff()
    ab.mode.Store(int32(EventLoopModeYield))
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        ab.Wait(true)  // Activity → stays in Yield
    }
}

// Overhead benchmark - measure cost of mode checking vs direct Gosched
func BenchmarkAdaptiveBackoff_Overhead(b *testing.B) {
    ab := newAdaptiveBackoff()
    ab.mode.Store(int32(EventLoopModeYield))  // Force Yield mode

    b.Run("AdaptiveBackoff_Wait", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            ab.Wait(true)
        }
    })
    b.Run("Direct_Gosched", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            runtime.Gosched()
        }
    })
    // Expected: AdaptiveBackoff should be <5% overhead vs direct Gosched
}

// Regression benchmark - compare total EventLoop throughput
func BenchmarkEventLoop_WithAdaptiveBackoff(b *testing.B) {
    // Setup real sender with adaptive backoff
    // Measure iterations/sec
    // Compare to BenchmarkEventLoopThroughput baseline
    // Target: No regression at high throughput
}
```

### Integration Tests

```go
func TestEventLoop_AdaptiveBackoff_LowThroughput(t *testing.T) {
    // Create sender, send at 10 Mb/s for 5s
    // Verify: Mode settles to Sleep
    // Verify: CPU usage < 5%
}

func TestEventLoop_AdaptiveBackoff_HighThroughput(t *testing.T) {
    // Create sender, send at 300 Mb/s for 5s
    // Verify: Mode stays Yield
    // Verify: Throughput achieved
}

func TestEventLoop_AdaptiveBackoff_BurstTraffic(t *testing.T) {
    // Create sender
    // Send burst (500 Mb/s for 100ms)
    // Idle for 2s
    // Send another burst
    // Verify: Mode transitions correctly
    // Verify: Both bursts succeed
}
```

### Existing Test Impact Analysis

| Test Category | Impact | Action |
|--------------|--------|--------|
| `eventloop_test.go` | Medium | Add mode override for deterministic behavior |
| `sender_race_test.go` | High | Add new race tests for adaptive backoff |
| `sender_control_ring_overflow_test.go` | Low | Should work unchanged |
| `backoff_benchmark_test.go` | Extend | Add adaptive mode benchmarks |
| Integration tests | Medium | May need longer timeouts for mode settling |

### New Makefile Targets

```makefile
## test-adaptive-backoff: Run adaptive backoff unit tests
.PHONY: test-adaptive-backoff
test-adaptive-backoff:
	go test -v -run TestAdaptiveBackoff ./congestion/live/send/

## test-adaptive-backoff-race: Run adaptive backoff race tests
.PHONY: test-adaptive-backoff-race
test-adaptive-backoff-race:
	go test -v -race -run TestAdaptiveBackoff ./congestion/live/send/

## bench-adaptive-backoff: Benchmark adaptive backoff modes
.PHONY: bench-adaptive-backoff
bench-adaptive-backoff:
	go test -bench=BenchmarkAdaptiveBackoff -benchtime=2s ./congestion/live/send/

## test-adaptive-backoff-all: Run all adaptive backoff tests
.PHONY: test-adaptive-backoff-all
test-adaptive-backoff-all: test-adaptive-backoff test-adaptive-backoff-race bench-adaptive-backoff
```

## Implementation Phases (TDD Approach)

### Phase 1: Core Types & Tests First (TDD)

**Step 1.1**: Create `adaptive_backoff.go` with types only
```go
// congestion/live/send/adaptive_backoff.go
type EventLoopMode int
const (
    EventLoopModeSleep EventLoopMode = iota  // CPU-friendly, ~945 iter/sec
    EventLoopModeYield                        // High-throughput, ~6.2M iter/sec
)
type adaptiveBackoff struct {
    mode           atomic.Int32
    lastActivityNs atomic.Int64
    idleThresholdNs int64
}
func newAdaptiveBackoff() *adaptiveBackoff
func (ab *adaptiveBackoff) Mode() EventLoopMode
func (ab *adaptiveBackoff) Wait(hadActivity bool)  // Simple API: just activity flag
```

**Step 1.2**: Create comprehensive unit tests
```bash
# Tests should FAIL initially (no implementation)
go test -v -run TestAdaptiveBackoff ./congestion/live/send/
```

**Step 1.3**: Implement until tests pass
```bash
go test -v -run TestAdaptiveBackoff ./congestion/live/send/
# All PASS
```

**Step 1.4**: Race tests
```bash
go test -v -race -run TestAdaptiveBackoff ./congestion/live/send/
# All PASS, no races
```

**Checkpoint**: `make test-adaptive-backoff-all` passes

### Phase 2: Sender EventLoop Integration

**Step 2.1**: Add config field
```go
// config.go
EventLoopMode EventLoopMode  // Override auto-detection (for testing)
```

**Step 2.2**: Add to sender struct
```go
// congestion/live/send/sender.go
adaptiveBackoff *adaptiveBackoff
```

**Step 2.3**: Modify eventloop.go
```go
// Replace lines 159-164:
// OLD:
//   if sleepResult.Duration > 0 {
//       time.Sleep(sleepResult.Duration)
//   }
//
// NEW:
hadActivity := delivered > 0 || controlDrained > 0
s.adaptiveBackoff.Wait(hadActivity)
```

**Step 2.4**: Run existing tests
```bash
go test -v ./congestion/live/send/
# All existing tests should still pass
```

**Step 2.5**: Run race tests
```bash
go test -v -race ./congestion/live/send/
# No new races introduced
```

**Checkpoint**: All sender tests pass, benchmarks show improvement

### Phase 3: Benchmarks & Validation

**Step 3.1**: Benchmark comparison
```bash
# Before (baseline)
go test -bench=BenchmarkEventLoopThroughput ./congestion/live/send/ | tee baseline.txt

# After (with adaptive backoff)
go test -bench=BenchmarkEventLoopThroughput ./congestion/live/send/ | tee adaptive.txt

# Compare
benchstat baseline.txt adaptive.txt
```

**Step 3.2**: CPU usage comparison at low rate
```bash
# Run 10 Mb/s test, measure CPU
make test-performance INITIAL=10M MAX=20M
# Verify CPU < 5%
```

**Step 3.3**: High throughput test
```bash
# Run 350 Mb/s test
make test-performance INITIAL=350M MAX=500M
# Verify: Throughput > 400 Mb/s (was 375 Mb/s)
```

**Checkpoint**: Benchmarks show improvement, no regression at low rates

### Phase 4: Receiver Integration

**Step 4.1**: Add adaptive backoff to receiver
```go
// congestion/live/receive/receiver.go
adaptiveBackoff *adaptiveBackoff
```

**Step 4.2**: Run receiver tests
```bash
go test -v ./congestion/live/receive/
go test -v -race ./congestion/live/receive/
```

**Checkpoint**: Receiver tests pass

### Phase 5: Metrics & Observability

**Step 5.1**: Add metrics
```go
// metrics/metrics.go
EventLoopMode         atomic.Int32   // Current mode (0=Sleep, 1=Yield)
EventLoopModeSwitches atomic.Uint64  // Total mode switches
```

**Step 5.2**: Update audit
```bash
make audit-metrics
# Verify new metrics documented
```

**Step 5.3**: Add to Prometheus export
```go
// In handler.go or similar
metrics.WriteGauge(w, "eventloop_mode", m.EventLoopMode.Load())
metrics.WriteCounter(w, "eventloop_mode_switches_total", m.EventLoopModeSwitches.Load())
```

**Checkpoint**: `make audit-metrics` passes

### Phase 6: Configuration & CLI

**Step 6.1**: Add CLI flags
```go
// contrib/common/flags.go
EventLoopMode = flag.String("eventloopmode", "auto",
    "EventLoop backoff mode: auto (default), sleep, yield")
```

**Step 6.2**: Add to config validation
```go
// config_validate.go
if c.EventLoopMode != "" && c.EventLoopMode != "auto" &&
   c.EventLoopMode != "sleep" && c.EventLoopMode != "yield" {
    return fmt.Errorf("invalid eventloopmode: %s", c.EventLoopMode)
}
```

**Step 6.3**: Test flag parsing
```bash
make test-flags
# All PASS
```

**Checkpoint**: `make test-flags` passes

### Phase 7: Integration Testing

**Step 7.1**: Run full test suite
```bash
make test
# All existing tests pass
```

**Step 7.2**: Run race tests
```bash
make race
# No races
```

**Step 7.3**: Run isolation tests
```bash
sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4
# Verify improved throughput
```

**Step 7.4**: Run performance tool
```bash
make test-performance INITIAL=350M MAX=550M
# Target: Find ceiling > 450 Mb/s
```

**Checkpoint**: All tests pass, throughput ceiling increased

### Phase 8: Documentation

**Step 8.1**: Update cli_args.md
**Step 8.2**: Update performance docs
**Step 8.3**: Add tuning guide

## Success Criteria Checklist

- [ ] All existing tests pass (`make test`)
- [ ] No new race conditions (`make race`)
- [ ] Low-rate CPU usage < 5% at 10 Mb/s
- [ ] High-rate throughput > 450 Mb/s (was 375 Mb/s)
- [ ] Benchmark shows improvement at high rates
- [ ] Benchmark shows no regression at low rates
- [ ] Mode transitions work correctly (unit tests)
- [ ] Metrics exported to Prometheus
- [ ] CLI flags functional
- [ ] Documentation updated

## Expected Results

| Scenario | Current | With Adaptive |
|----------|---------|---------------|
| Idle connection | ~1% CPU | ~1% CPU (Sleep mode) |
| 50 Mb/s stream | ~5% CPU | ~5% CPU (Yield mode) |
| 300 Mb/s stream | ~30% CPU, 375 Mb/s ceiling | ~40% CPU, 400+ Mb/s |
| 500 Mb/s stream | FAILS (ceiling) | ~60% CPU, 500 Mb/s |

## Design Decisions (Resolved)

| Question | Decision | Rationale |
|----------|----------|-----------|
| **SPIN mode?** | ❌ Not needed | Yield (6.2M iter/sec) provides 144x headroom over 500 Mb/s (43K pkt/sec) |
| **Per-connection or global?** | Per-connection | Cleaner, already structured this way in sender/receiver |
| **Startup mode?** | Yield | User is connecting for a reason - ready for immediate throughput |
| **Idle threshold?** | 1 second | Long enough to avoid thrashing, short enough to save CPU |

## Future Considerations

1. **If Yield is still too slow**: Could add SPIN mode later, but 144x headroom suggests this is unlikely
2. **Configurable idle threshold**: Default 1s works for most cases, but could expose via config
3. **Metrics-based tuning**: Could auto-tune idle threshold based on observed traffic patterns

## References

- [Sender Lockfree Architecture](sender_lockfree_architecture.md)
- [Completely Lockfree Receiver](completely_lockfree_receiver.md)
- [Adaptive Backoff Design](adaptive_backoff_design.md) - Hypothesis confirmation
- [Performance Testing Implementation Log](performance_testing_implementation_log.md)
