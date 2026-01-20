# Adaptive Backoff Design for High-Throughput SRT

## Problem Statement

**Observation**: When running performance tests at 350+ Mb/s, CPU utilization is NOT maxed out, yet throughput is capped at ~375 Mb/s.

**Root Cause Hypothesis**: The backoff algorithms in the lock-free rings and gosrt code use `time.Sleep()` which has significant overhead:

1. **OS Scheduler Granularity**: Linux scheduler typically has 1-15ms granularity
2. **Syscall Overhead**: Each `time.Sleep()` involves:
   - Futex syscall to kernel
   - Context switch away from goroutine
   - Timer setup in kernel
   - Interrupt when timer fires
   - Context switch back to goroutine
3. **Timing Mismatch**: At 500 Mb/s with 1456-byte packets:
   - Packets arrive every **23 microseconds**
   - Minimum sleep granularity is **~1000 microseconds** (1ms)
   - **43x mismatch** between packet rate and sleep granularity

This explains why CPU isn't maxed but throughput is limited: goroutines are sleeping when they should be processing packets.

## Current Backoff Mechanisms

### 1. Lock-Free Ring Buffer (`internal/lockfree/ring.go`)

```go
// Current implementation uses sleep-based backoff
const (
    defaultBackoffMinSleep = 100 * time.Microsecond
    defaultBackoffMaxSleep = 1 * time.Millisecond
)

func (s *Shard) writeWithBackoff(pkt packet.Packet, ...) error {
    // After N immediate retries, falls back to sleeping
    for attempt := 0; attempt < maxAttempts; attempt++ {
        if s.write(pkt) {
            return nil
        }
        // PROBLEM: This sleep dominates at high throughput
        time.Sleep(backoffDuration)
        backoffDuration *= 2 // Exponential backoff
    }
}
```

### 2. Receiver EventLoop (`congestion/live/receiver_eventloop.go`)

```go
// Backoff when no packets available
func (r *receiver) runEventLoopIteration() {
    if noPackets {
        time.Sleep(r.config.BackoffMinSleep)  // Sleeping here!
    }
}
```

### 3. Sender EventLoop (`congestion/live/sender_eventloop.go`)

```go
// Similar backoff pattern
func (s *sender) runSendEventLoopIteration() {
    if nothingToSend {
        time.Sleep(s.sendEventLoopBackoffMinSleep)  // Sleeping here!
    }
}
```

## Proposed Solution: Adaptive Spin/Sleep Mode

### Concept

Instead of always sleeping, detect when throughput is high and switch to **spin-wait** or **yield** mode:

| Mode | Mechanism | CPU Usage | Latency | Best For |
|------|-----------|-----------|---------|----------|
| **Sleep** | `time.Sleep()` | Low | High (1-15ms) | <100 Mb/s |
| **Yield** | `runtime.Gosched()` | Medium | Medium (~10-100µs) | 100-300 Mb/s |
| **Spin** | Busy loop | High | Lowest (<1µs) | >300 Mb/s |

### Auto-Detection Strategy

The system should automatically detect when to switch modes based on:

1. **Packet Rate**: Track packets/second, switch modes at thresholds
2. **Backoff Frequency**: If backing off too often, mode is wrong
3. **Ring Utilization**: If ring is frequently full/empty, adjust mode

```go
type BackoffMode int

const (
    BackoffModeSleep BackoffMode = iota  // Low throughput, save CPU
    BackoffModeYield                      // Medium throughput
    BackoffModeSpin                       // High throughput, minimize latency
    BackoffModeAuto                       // Auto-detect (default)
)

// Thresholds for auto-detection
const (
    HighThroughputThreshold = 200_000_000  // 200 Mb/s
    UltraHighThroughputThreshold = 350_000_000  // 350 Mb/s
)
```

## Implementation Options

### Option 1: Configurable Mode per Ring

**Pros:**
- Simple to implement
- User has explicit control
- Easy to test different modes

**Cons:**
- Requires manual tuning
- Doesn't adapt to changing conditions

```go
type RingConfig struct {
    // Existing fields...

    // New: Backoff mode selection
    BackoffMode BackoffMode  // Sleep, Yield, Spin, or Auto
}

// Usage
ring := lockfree.NewRing(lockfree.RingConfig{
    Size:        16384,
    Shards:      8,
    BackoffMode: lockfree.BackoffModeSpin,  // For high throughput
})
```

### Option 2: Throughput-Based Auto-Switch

**Pros:**
- Adapts automatically
- Works for varying throughput
- No manual tuning needed

**Cons:**
- More complex implementation
- Potential for mode-switching overhead
- Hysteresis needed to prevent thrashing

```go
type AdaptiveBackoff struct {
    mode        atomic.Int32
    packetCount atomic.Uint64
    lastCheck   atomic.Int64

    // Thresholds
    highThreshold  int64  // Packets/sec to switch to Yield
    ultraThreshold int64  // Packets/sec to switch to Spin
}

func (ab *AdaptiveBackoff) Wait() {
    // Periodically recalculate mode based on throughput
    ab.maybeUpdateMode()

    switch BackoffMode(ab.mode.Load()) {
    case BackoffModeSleep:
        time.Sleep(100 * time.Microsecond)
    case BackoffModeYield:
        runtime.Gosched()
    case BackoffModeSpin:
        // Tight loop with occasional yield
        for i := 0; i < 100; i++ {
            // Spin
        }
        runtime.Gosched()  // Yield every 100 spins
    }
}

func (ab *AdaptiveBackoff) maybeUpdateMode() {
    now := time.Now().UnixNano()
    last := ab.lastCheck.Load()

    // Check every 100ms
    if now-last < 100_000_000 {
        return
    }

    if !ab.lastCheck.CompareAndSwap(last, now) {
        return  // Another goroutine is updating
    }

    // Calculate packets/sec
    count := ab.packetCount.Swap(0)
    elapsed := float64(now - last) / 1e9
    pps := float64(count) / elapsed

    // Update mode based on throughput
    var newMode BackoffMode
    switch {
    case pps > float64(ab.ultraThreshold):
        newMode = BackoffModeSpin
    case pps > float64(ab.highThreshold):
        newMode = BackoffModeYield
    default:
        newMode = BackoffModeSleep
    }

    ab.mode.Store(int32(newMode))
}
```

### Option 3: Hybrid Spin-Sleep with Timeout

**Pros:**
- Best of both worlds
- Naturally adapts to load
- No explicit thresholds needed

**Cons:**
- Most complex implementation
- CPU usage unpredictable
- Spin duration tuning needed

```go
func (ab *AdaptiveBackoff) HybridWait(spinDuration time.Duration) {
    // Phase 1: Spin for a short period (optimistic)
    deadline := time.Now().Add(spinDuration)
    for time.Now().Before(deadline) {
        if ab.hasWork() {
            return  // Work appeared during spin
        }
        runtime.Gosched()  // Yield between spins
    }

    // Phase 2: Sleep if still no work (pessimistic)
    time.Sleep(100 * time.Microsecond)
}
```

### Option 4: Bitrate-Aware Configuration

**Pros:**
- Explicit control based on target throughput
- Easy to integrate with SRT config
- Predictable behavior

**Cons:**
- Requires user to know expected throughput
- Doesn't adapt if throughput varies

```go
// In gosrt config
type Config struct {
    // Existing fields...

    // New: Expected throughput hint
    ExpectedThroughputBps int64  // 0 = auto-detect
}

// Internal logic
func (c *Config) getBackoffMode() BackoffMode {
    switch {
    case c.ExpectedThroughputBps >= 350_000_000:
        return BackoffModeSpin
    case c.ExpectedThroughputBps >= 100_000_000:
        return BackoffModeYield
    default:
        return BackoffModeSleep
    }
}
```

## Recommended Approach: Option 2 + Option 4

Combine auto-detection with user hint:

1. **User provides throughput hint** (optional): If user knows expected throughput, use appropriate mode from start
2. **Auto-detect if no hint**: Monitor actual throughput and switch modes dynamically
3. **Hysteresis**: Only switch modes after sustained threshold breach (avoid thrashing)

```go
type BackoffConfig struct {
    Mode               BackoffMode  // Explicit mode or Auto
    ExpectedBps        int64        // Throughput hint (0 = unknown)
    HighThresholdBps   int64        // Switch to Yield above this
    UltraThresholdBps  int64        // Switch to Spin above this
    HysteresisMs       int64        // Time before mode switch
}

func DefaultBackoffConfig() BackoffConfig {
    return BackoffConfig{
        Mode:              BackoffModeAuto,
        ExpectedBps:       0,
        HighThresholdBps:  100_000_000,   // 100 Mb/s
        UltraThresholdBps: 300_000_000,   // 300 Mb/s
        HysteresisMs:      500,            // 500ms before switch
    }
}
```

## Specific Code Locations (Current Implementation)

### Sender EventLoop Sleep (Primary Target)

The key location is `congestion/live/send/eventloop.go` lines 159-164:

```go
// Current implementation - ALWAYS sleeps when Duration > 0
if sleepResult.Duration > 0 {
    time.Sleep(sleepResult.Duration)  // <-- THIS IS THE BOTTLENECK
    if sleepResult.Duration >= s.backoffMinSleep {
        m.SendEventLoopIdleBackoffs.Add(1)
    }
}
```

**Problem**: At 500 Mb/s (23µs packet interval), any `time.Sleep()` creates massive delays:
- Minimum sleep is 100µs (config default)
- OS scheduler adds 1-15ms
- Result: EventLoop processes packets in bursts instead of continuously

### Receiver EventLoop

Similar pattern exists in the receiver - needs investigation.

### Lock-Free Ring Shards

The ring shards use retry/backoff when full - check `vendor/github.com/randomizedcoder/circular/` or internal implementations.

## Changes Required

### 1. Add BackoffMode Enum

**File**: `config.go`

```go
// BackoffMode controls how EventLoops wait during idle periods
type BackoffMode int

const (
    BackoffModeSleep BackoffMode = iota  // Default: time.Sleep() - lowest CPU
    BackoffModeYield                      // runtime.Gosched() - medium CPU
    BackoffModeSpin                       // Busy loop - highest CPU, lowest latency
    BackoffModeAuto                       // Auto-detect based on throughput
)
```

### 2. Add Config Fields

**File**: `config.go` (add to Config struct)

```go
// BackoffMode controls EventLoop idle waiting strategy
// Default: BackoffModeAuto
BackoffMode BackoffMode

// ExpectedThroughputBps is a hint for auto-mode selection
// If 0, auto-detection monitors actual throughput
ExpectedThroughputBps int64
```

### 3. Update Sender EventLoop

**File**: `congestion/live/send/eventloop.go`

Replace the sleep logic with adaptive waiting:

```go
// New implementation with mode selection
if sleepResult.Duration > 0 {
    switch s.backoffMode {
    case BackoffModeSleep:
        time.Sleep(sleepResult.Duration)
    case BackoffModeYield:
        // Yield CPU but don't block - allows other goroutines
        for i := 0; i < int(sleepResult.Duration.Microseconds()/10); i++ {
            runtime.Gosched()
        }
    case BackoffModeSpin:
        // Busy-wait with periodic yields (prevents starvation)
        deadline := time.Now().Add(sleepResult.Duration)
        for time.Now().Before(deadline) {
            // Check for new work every N iterations
            if i%100 == 0 {
                runtime.Gosched()
            }
        }
    case BackoffModeAuto:
        s.adaptiveWait(sleepResult.Duration)
    }
}

func (s *sender) adaptiveWait(duration time.Duration) {
    // Check current throughput and select mode dynamically
    pps := s.metrics.SendDataSent.Load() / max(1, s.uptimeSeconds())

    switch {
    case pps > 30000: // ~350 Mb/s (30K * 1456 * 8)
        // Spin mode for ultra-high throughput
        deadline := time.Now().Add(duration)
        for time.Now().Before(deadline) {
            if i%100 == 0 { runtime.Gosched() }
        }
    case pps > 8000: // ~100 Mb/s
        // Yield mode for high throughput
        for i := 0; i < int(duration.Microseconds()/10); i++ {
            runtime.Gosched()
        }
    default:
        // Sleep mode for normal throughput
        time.Sleep(duration)
    }
}
```

### 4. Add CLI Flags

**File**: `contrib/common/flags.go`

```go
BackoffMode = flag.String("backoffmode", "auto",
    "Backoff mode: sleep (low CPU), yield (medium), spin (high CPU), auto (detect)")
ExpectedThroughputBps = flag.Int64("expectedthroughput", 0,
    "Expected throughput in bps for backoff mode selection (0 = auto-detect)")
```

### 5. Receiver EventLoop (Similar Changes)

Apply the same pattern to `congestion/live/receive/eventloop.go`.

### 3. New CLI Flags

```bash
-backoffmode string      # "sleep", "yield", "spin", "auto" (default: auto)
-expectedthroughput int  # Expected throughput hint in bps (default: 0 = auto)
```

## Performance Expectations

| Mode | CPU @ Idle | CPU @ 350 Mb/s | Expected Throughput |
|------|------------|----------------|---------------------|
| Sleep (current) | ~1% | ~30% | 375 Mb/s |
| Yield | ~5% | ~60% | 450 Mb/s |
| Spin | ~20% | ~95% | 500+ Mb/s |

## Testing Plan

1. **Baseline**: Current behavior (Sleep mode)
2. **Manual Spin**: Force Spin mode, measure max throughput
3. **Manual Yield**: Force Yield mode, find sweet spot
4. **Auto-Detection**: Verify mode switching works correctly
5. **Regression**: Ensure low-throughput scenarios still work

## Implementation Phases

### Phase 1: Manual Mode Selection (Quick Win)
**Goal**: Prove that removing sleep increases throughput

| Step | File | Change |
|------|------|--------|
| 1.1 | `config.go` | Add `BackoffMode` type and constants |
| 1.2 | `config.go` | Add `BackoffMode` field to `Config` struct |
| 1.3 | `config_validate.go` | Add validation for `BackoffMode` |
| 1.4 | `contrib/common/flags.go` | Add `-backoffmode` flag |
| 1.5 | `connection.go` | Pass `BackoffMode` to sender/receiver config |
| 1.6 | `congestion/live/send/sender.go` | Add `backoffMode` field |
| 1.7 | `congestion/live/send/eventloop.go` | Implement mode-based waiting |

**Verification**:
```bash
# Test with spin mode
./contrib/performance/performance -initial 350000000 -backoffmode spin ...

# Compare CPU and throughput
```

### Phase 2: Receiver EventLoop
**Goal**: Apply same changes to receiver

| Step | File | Change |
|------|------|--------|
| 2.1 | `congestion/live/receive/receiver.go` | Add `backoffMode` field |
| 2.2 | `congestion/live/receive/eventloop.go` | Implement mode-based waiting |

### Phase 3: Auto-Detection
**Goal**: Automatically select optimal mode

| Step | File | Change |
|------|------|--------|
| 3.1 | `congestion/live/send/eventloop.go` | Add `adaptiveWait()` function |
| 3.2 | `congestion/live/send/eventloop.go` | Track packets/sec for mode selection |
| 3.3 | Add hysteresis to prevent mode thrashing |

### Phase 4: Testing & Tuning
**Goal**: Validate improvements

| Test | Expected Result |
|------|-----------------|
| 300 Mb/s Sleep mode | ~375 Mb/s ceiling |
| 300 Mb/s Spin mode | 450+ Mb/s |
| 500 Mb/s Spin mode | Target: 500 Mb/s |
| Low throughput regression | No change in CPU/latency |

### Phase 5: Documentation
- Update `cli_args.md` with new flags
- Update performance testing docs
- Add recommended configurations for different scenarios

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Spin mode burns CPU | High CPU usage even when idle | Hysteresis + quick fallback to sleep |
| Mode thrashing | Unstable behavior | Longer hysteresis period |
| Spin breaks on single-core | No progress if one goroutine spins | Always yield periodically |
| Harder to debug | Timing-dependent bugs | Comprehensive logging in debug mode |

## Success Criteria

1. **Primary**: Achieve 450+ Mb/s with acceptable CPU usage
2. **Secondary**: No regression in low-throughput scenarios
3. **Tertiary**: Auto-detection correctly identifies appropriate mode

## ✅ HYPOTHESIS CONFIRMED (2026-01-17)

Unit test `TestBackoffHypothesis` confirms sleep is the bottleneck:

```
go test -v -run TestBackoffHypothesis ./congestion/live/send/
```

### Results

| Mode | Iterations/sec | vs Sleep_100µs |
|------|----------------|----------------|
| **NoWait** | 109,526,570 | **+11,590,752%** |
| **Yield (Gosched)** | 6,219,705 | **+658,112%** |
| **Spin** | 98,374 | +10,311% |
| **Sleep 10µs** | 974 | +3% |
| **Sleep 100µs** | 945 | baseline |
| **Sleep 1ms** | 945 | same |

### Key Finding

**`time.Sleep()` caps at ~945 iterations/sec regardless of requested duration!**

The OS scheduler has a minimum granularity (~1ms), so even `Sleep(10µs)` becomes effectively `Sleep(~1ms)`.

### Recommendation

**Switch to `runtime.Gosched()` (Yield mode)** for high throughput:
- **6,581x faster** than Sleep
- Still cooperative (yields to other goroutines)
- At 6.2M iterations/sec, easily supports 500+ Mb/s
- CPU usage will increase but throughput ceiling removed

### Implementation Priority

1. **Quick win**: Replace `time.Sleep()` with `runtime.Gosched()` in EventLoop
2. **Phase 2**: Add `-backoffmode` flag to make it configurable
3. **Phase 3**: Auto-detection based on throughput

## Metrics to Monitor

During testing, watch these prometheus metrics:

| Metric | Meaning | Good @ 500 Mb/s |
|--------|---------|-----------------|
| `send_eventloop_idle_backoffs_total` | Times EventLoop slept | Low |
| `send_eventloop_iterations_total` | Loop iterations | High |
| `send_eventloop_sleep_total_us` | Total sleep time | Low |
| `send_eventloop_tsbpd_sleeps_total` | TSBPD-guided sleeps | Expected |
| `send_data_sent_total` | Packets sent | Matches target rate |

## References

- [Go runtime.Gosched()](https://pkg.go.dev/runtime#Gosched)
- [Linux scheduler granularity](https://lwn.net/Articles/549580/)
- [Lock-free ring buffer design](../internal/lockfree/README.md)
- [Performance testing implementation log](performance_testing_implementation_log.md)
