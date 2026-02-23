# Configurable Timer Intervals Design

**Date**: 2026-01-17
**Status**: Draft
**Parent Doc**: [performance_testing_implementation_log.md](performance_testing_implementation_log.md)

## Problem Statement

At high throughput (350+ Mb/s), the SRT implementation experiences **EventLoop Starvation** - the sender cannot generate packets fast enough to meet the target bitrate. Analysis identified that the timer intervals are aggressive:

- **ACK Timer**: 10ms → 100 ACKs/second → 100 scans/second
- **NAK Timer**: 20ms → 50 NAKs/second → 50 gap scans/second
- **Tick Interval**: 10ms → 100 ticks/second → 100 delivery cycles/second
- **Drop Timer**: 100ms (hardcoded) → 10 drop cycles/second

**Hypothesis**: Increasing timer intervals to 100ms/200ms or 500ms/1000ms would:
1. Reduce CPU overhead per unit time
2. ACKs jump forward more → less scanning per ACK
3. Less aggressive NAKing → fewer retransmissions
4. More packets delivered per Tick cycle

## Timer Inventory

### 1. Receiver Timers

| Timer | Default | Config Field | Location | Description |
|-------|---------|--------------|----------|-------------|
| Periodic ACK | 10ms | `PeriodicAckIntervalMs` | `congestion/live/receive/ack.go:23`, `event_loop.go:90` | Full ACK for RTT measurement |
| Periodic NAK | 20ms | `PeriodicNakIntervalMs` | `congestion/live/receive/nak.go:83`, `event_loop.go:101` | Gap detection and retransmit requests |
| Rate Calculation | 1s | `EventLoopRateInterval` | `congestion/live/receive/event_loop.go:111` | Statistics calculation |

### 2. Sender Timers

| Timer | Default | Config Field | Location | Description |
|-------|---------|--------------|----------|-------------|
| Drop Ticker | 100ms | **NOT CONFIGURABLE** | `congestion/live/send/eventloop.go:68` | Old packet cleanup |

### 3. Connection Timers

| Timer | Default | Config Field | Location | Description |
|-------|---------|--------------|----------|-------------|
| Tick Interval | 10ms | `TickIntervalMs` | `connection.go:429` | TSBPD delivery tick |
| io_uring Wait | 10ms | **HARDCODED** | `connection_linux.go:142` | CQE wait timeout |

### 4. Related Timers (Already Configurable)

| Timer | Default | Config Field | Description |
|-------|---------|--------------|-------------|
| FastNAK Threshold | N/A | `FastNakThresholdMs` | Quick NAK after silence |
| NAK Consolidation | N/A | `NakConsolidationBudgetMs` | NAK batching window |
| Handshake Timeout | 1.5s | `HandshakeTimeout` | Connection setup timeout |

## Code Analysis

### Receiver: `congestion/live/receive/event_loop.go`

```go:66:112:congestion/live/receive/event_loop.go
// ACK interval from config (microseconds -> time.Duration)
ackInterval := time.Duration(r.periodicACKInterval) * time.Microsecond
if ackInterval <= 0 {
    ackInterval = 10 * time.Millisecond // Default: 10ms  ← CONFIGURABLE
}

// NAK interval from config (microseconds -> time.Duration)
nakInterval := time.Duration(r.periodicNAKInterval) * time.Microsecond
if nakInterval <= 0 {
    nakInterval = 20 * time.Millisecond // Default: 20ms  ← CONFIGURABLE
}
// ...
fullACKTicker := time.NewTicker(ackInterval)
// ...
nakTicker := time.NewTicker(nakInterval)
// ...
rateTicker := time.NewTicker(rateInterval)
```

### Sender: `congestion/live/send/eventloop.go`

```go:67:70:congestion/live/send/eventloop.go
// Drop ticker (periodic old packet cleanup)
dropInterval := 100 * time.Millisecond // Check every 100ms  ← HARDCODED!
dropTicker := time.NewTicker(dropInterval)
defer dropTicker.Stop()
```

### Connection: `connection.go`

```go:428:429:connection.go
// TSBPD delivery tick interval - configurable via TickIntervalMs (default: 10ms)
c.tick = time.Duration(c.config.TickIntervalMs) * time.Millisecond
```

## Design

### 1. New Config Fields

Add to `config.go`:

```go
// Timer interval configuration
type Config struct {
    // ... existing fields ...

    // === Timer Intervals (all in milliseconds) ===

    // TickIntervalMs is the TSBPD delivery tick interval (default: 10ms)
    // Lower = lower latency but higher CPU. Higher = higher latency but lower CPU.
    // ALREADY EXISTS - keep as is
    TickIntervalMs uint64

    // PeriodicAckIntervalMs is the periodic ACK timer interval (default: 10ms)
    // Controls how often Full ACKs are sent for RTT measurement.
    // ALREADY EXISTS - keep as is
    PeriodicAckIntervalMs uint64

    // PeriodicNakIntervalMs is the periodic NAK timer interval (default: 20ms)
    // Controls how often gap detection runs.
    // ALREADY EXISTS - keep as is
    PeriodicNakIntervalMs uint64

    // NEW: SendDropIntervalMs is the sender drop ticker interval (default: 100ms)
    // Controls how often the sender checks for and drops too-old packets.
    // Higher values reduce CPU but may delay dropping stale packets.
    SendDropIntervalMs uint64

    // NEW: EventLoopRateIntervalMs is the rate calculation interval (default: 1000ms)
    // Controls how often throughput/rate statistics are calculated.
    EventLoopRateIntervalMs uint64
}
```

**Default Values** (in `DefaultConfig()`):

```go
// Timer interval defaults
TickIntervalMs:           10,   // 10ms TSBPD tick
PeriodicAckIntervalMs:    10,   // 10ms periodic ACK
PeriodicNakIntervalMs:    20,   // 20ms periodic NAK
SendDropIntervalMs:       100,  // 100ms drop check (NEW)
EventLoopRateIntervalMs:  1000, // 1s rate calculation (NEW)
```

### 2. New CLI Flags

Add to `contrib/common/flags.go`:

```go
// Timer interval configuration flags (already partially exist)
TickIntervalMs          = flag.Uint64("tickintervalms", 0, "TSBPD delivery tick interval in ms (default: 10)")
PeriodicNakIntervalMs   = flag.Uint64("periodicnakintervalms", 0, "Periodic NAK timer interval in ms (default: 20)")
PeriodicAckIntervalMs   = flag.Uint64("periodicackintervalms", 0, "Periodic ACK timer interval in ms (default: 10)")

// NEW FLAGS
SendDropIntervalMs      = flag.Uint64("senddropintervalms", 0, "Sender drop ticker interval in ms (default: 100)")
EventLoopRateIntervalMs = flag.Uint64("eventlooprateintervalms", 0, "Rate calculation interval in ms (default: 1000)")
```

### 3. Code Changes

#### 3.1 Sender EventLoop (`congestion/live/send/eventloop.go`)

**Before:**
```go
dropInterval := 100 * time.Millisecond // Hardcoded
```

**After:**
```go
// Use configured drop interval (microseconds -> time.Duration)
dropInterval := time.Duration(s.sendDropIntervalUs) * time.Microsecond
if dropInterval <= 0 {
    dropInterval = 100 * time.Millisecond // Default: 100ms
}
dropTicker := time.NewTicker(dropInterval)
```

#### 3.2 Sender Config (`congestion/live/send/sender.go`)

Add to `SendConfig`:
```go
type SendConfig struct {
    // ... existing fields ...

    // SendDropIntervalUs is the drop ticker interval in microseconds
    SendDropIntervalUs uint64
}
```

#### 3.3 Connection (`connection.go`)

Pass new config to sender:
```go
send.NewSender(send.SendConfig{
    // ... existing fields ...
    SendDropIntervalUs: c.config.SendDropIntervalMs * 1000,
})
```

#### 3.4 Receiver EventLoop (`congestion/live/receive/event_loop.go`)

Already uses `r.periodicACKInterval` and `r.periodicNAKInterval` from config.
Need to add rate interval from config:

**Before:**
```go
rateInterval := r.eventLoopRateInterval
if rateInterval <= 0 {
    rateInterval = 1 * time.Second
}
```

**After:**
```go
rateInterval := time.Duration(r.eventLoopRateIntervalUs) * time.Microsecond
if rateInterval <= 0 {
    rateInterval = 1 * time.Second
}
```

### 4. Comprehensive Validation Rules

#### 4.1 Validation Principles

**Goal**: Protect operators from typos and misconfigurations that would cause:
- 100% CPU usage (intervals too small)
- Unresponsive system (intervals too large)
- Protocol dysfunction (contradictory relationships)
- Excessive packet loss (timers don't coordinate)

#### 4.2 Timer Relationships Diagram

```
                    ┌─────────────────────────────────────────────────────────┐
                    │                 TIMER RELATIONSHIPS                      │
                    └─────────────────────────────────────────────────────────┘

   FAST ◄──────────────────────────────────────────────────────────────► SLOW

   1ms    5ms    10ms    20ms    50ms    100ms    500ms    1000ms    5000ms
    │      │      │       │       │        │        │         │         │
    │      │      ├───────┴───────┼────────┴────────┼─────────┴─────────┤
    │      │      │  ACK Timer    │                 │                   │
    │      │      │  (10-1000ms)  │                 │                   │
    │      │      │               │                 │                   │
    │      │      │      ┌────────┴─────────────────┤                   │
    │      │      │      │    NAK Timer             │                   │
    │      │      │      │    (20-2000ms)           │                   │
    │      │      │      │    MUST BE >= ACK        │                   │
    │      │      │               │                 │                   │
    │      │      ├───────────────┴─────────────────┤                   │
    │      │      │      Tick Interval              │                   │
    │      │      │      (10-1000ms)                │                   │
    │      │      │                                 │                   │
    │      │                      ┌─────────────────┴───────────────────┤
    │      │                      │      Drop Interval                  │
    │      │                      │      (50-5000ms)                    │
    │      │                      │      SHOULD BE >= NAK               │
    │      │                      │                                     │
    └──────┴──────────────────────┴─────────────────────────────────────┘

   CONSTRAINT: ACK <= NAK <= Drop
   REASON: RTT → NAK suppression → Drop decision chain
```

#### 4.3 Constraint Rules

| Rule | Constraint | Reason | Error if Violated |
|------|------------|--------|-------------------|
| R1 | `ACK >= 1ms` | Prevent CPU spin (1000 ACKs/sec max) | "ACK interval too small" |
| R2 | `ACK <= 1000ms` | Ensure timely RTT updates | "ACK interval too large" |
| R3 | `NAK >= 1ms` | Prevent CPU spin | "NAK interval too small" |
| R4 | `NAK <= 2000ms` | Ensure timely loss recovery | "NAK interval too large" |
| R5 | `NAK >= ACK` | NAK needs RTT from ACK | "NAK must be >= ACK" |
| R6 | `Tick >= 1ms` | Prevent CPU spin | "Tick interval too small" |
| R7 | `Tick <= 1000ms` | Ensure timely TSBPD delivery | "Tick interval too large" |
| R8 | `Drop >= 50ms` | Prevent excessive drop checks | "Drop interval too small" |
| R9 | `Drop <= 5000ms` | Ensure stale packet cleanup | "Drop interval too large" |
| R10 | `Drop >= NAK` | Allow NAK/retransmit before drop | "Drop must be >= NAK" |
| R11 | `Rate >= 100ms` | Reasonable statistics interval | "Rate interval too small" |
| R12 | `Rate <= 10000ms` | Timely rate updates | "Rate interval too large" |
| R13 | `NAK <= 10 × ACK` | Keep NAK reasonably coupled to ACK | "NAK too much larger than ACK" |
| R14 | `Tick <= 2 × ACK` (Tick mode) | Tick must fire often enough for ACK | "Tick too slow for ACK interval" |

#### 4.4 TSBPD-Related Constraints

The timer intervals must make sense relative to the TSBPD delay (latency setting):

| Rule | Constraint | Reason |
|------|------------|--------|
| R15 | `ACK < Latency/2` | Multiple ACKs before TSBPD expiry |
| R16 | `NAK < Latency/2` | Multiple NAK cycles before TSBPD expiry |
| R17 | `Tick < Latency/4` | Multiple delivery opportunities |

**Example**: With `Latency=3000ms` (3 seconds):
- ACK should be < 1500ms ✓ (max is 1000ms anyway)
- NAK should be < 1500ms ✓ (max is 2000ms, but latency/2 check)
- Tick should be < 750ms ✓

#### 4.5 Validation Implementation

```go
// config_validate.go

// TimerValidationError provides detailed error information
type TimerValidationError struct {
    Rule        string // Rule ID (e.g., "R5")
    Field       string // Field name
    Value       uint64 // Actual value
    Constraint  string // What was expected
    Suggestion  string // How to fix
}

func (e TimerValidationError) Error() string {
    return fmt.Sprintf("[%s] %s=%dms violates constraint: %s. Suggestion: %s",
        e.Rule, e.Field, e.Value, e.Constraint, e.Suggestion)
}

// validateTimerIntervals performs comprehensive timer validation
func (c Config) validateTimerIntervals() error {
    var errors []error

    // Helper to get effective value (0 means use default)
    ack := c.PeriodicAckIntervalMs
    if ack == 0 { ack = 10 }

    nak := c.PeriodicNakIntervalMs
    if nak == 0 { nak = 20 }

    tick := c.TickIntervalMs
    if tick == 0 { tick = 10 }

    drop := c.SendDropIntervalMs
    if drop == 0 { drop = 100 }

    rate := c.EventLoopRateIntervalMs
    if rate == 0 { rate = 1000 }

    latency := uint64(c.Latency.Milliseconds())
    if latency == 0 { latency = 3000 } // Default 3s

    // ═══════════════════════════════════════════════════════════════════════
    // ABSOLUTE BOUNDS - Prevent CPU spin and unresponsive behavior
    // ═══════════════════════════════════════════════════════════════════════

    // R1: ACK minimum
    if c.PeriodicAckIntervalMs > 0 && c.PeriodicAckIntervalMs < 1 {
        errors = append(errors, TimerValidationError{
            Rule:       "R1",
            Field:      "PeriodicAckIntervalMs",
            Value:      c.PeriodicAckIntervalMs,
            Constraint: "must be >= 1ms",
            Suggestion: "Use at least 1ms, recommend 10-100ms",
        })
    }

    // R2: ACK maximum
    if c.PeriodicAckIntervalMs > 1000 {
        errors = append(errors, TimerValidationError{
            Rule:       "R2",
            Field:      "PeriodicAckIntervalMs",
            Value:      c.PeriodicAckIntervalMs,
            Constraint: "must be <= 1000ms",
            Suggestion: "RTT accuracy degrades with intervals > 1s",
        })
    }

    // R3: NAK minimum
    if c.PeriodicNakIntervalMs > 0 && c.PeriodicNakIntervalMs < 1 {
        errors = append(errors, TimerValidationError{
            Rule:       "R3",
            Field:      "PeriodicNakIntervalMs",
            Value:      c.PeriodicNakIntervalMs,
            Constraint: "must be >= 1ms",
            Suggestion: "Use at least 1ms, recommend 20-200ms",
        })
    }

    // R4: NAK maximum
    if c.PeriodicNakIntervalMs > 2000 {
        errors = append(errors, TimerValidationError{
            Rule:       "R4",
            Field:      "PeriodicNakIntervalMs",
            Value:      c.PeriodicNakIntervalMs,
            Constraint: "must be <= 2000ms",
            Suggestion: "Loss recovery too slow with intervals > 2s",
        })
    }

    // R6: Tick minimum
    if c.TickIntervalMs > 0 && c.TickIntervalMs < 1 {
        errors = append(errors, TimerValidationError{
            Rule:       "R6",
            Field:      "TickIntervalMs",
            Value:      c.TickIntervalMs,
            Constraint: "must be >= 1ms",
            Suggestion: "Use at least 1ms, recommend 10-100ms",
        })
    }

    // R7: Tick maximum
    if c.TickIntervalMs > 1000 {
        errors = append(errors, TimerValidationError{
            Rule:       "R7",
            Field:      "TickIntervalMs",
            Value:      c.TickIntervalMs,
            Constraint: "must be <= 1000ms",
            Suggestion: "TSBPD delivery too slow with intervals > 1s",
        })
    }

    // R8: Drop minimum
    if c.SendDropIntervalMs > 0 && c.SendDropIntervalMs < 50 {
        errors = append(errors, TimerValidationError{
            Rule:       "R8",
            Field:      "SendDropIntervalMs",
            Value:      c.SendDropIntervalMs,
            Constraint: "must be >= 50ms",
            Suggestion: "Drop checks too frequent wastes CPU",
        })
    }

    // R9: Drop maximum
    if c.SendDropIntervalMs > 5000 {
        errors = append(errors, TimerValidationError{
            Rule:       "R9",
            Field:      "SendDropIntervalMs",
            Value:      c.SendDropIntervalMs,
            Constraint: "must be <= 5000ms",
            Suggestion: "Stale packets may accumulate with intervals > 5s",
        })
    }

    // R11: Rate minimum
    if c.EventLoopRateIntervalMs > 0 && c.EventLoopRateIntervalMs < 100 {
        errors = append(errors, TimerValidationError{
            Rule:       "R11",
            Field:      "EventLoopRateIntervalMs",
            Value:      c.EventLoopRateIntervalMs,
            Constraint: "must be >= 100ms",
            Suggestion: "Rate calculation too frequent wastes CPU",
        })
    }

    // R12: Rate maximum
    if c.EventLoopRateIntervalMs > 10000 {
        errors = append(errors, TimerValidationError{
            Rule:       "R12",
            Field:      "EventLoopRateIntervalMs",
            Value:      c.EventLoopRateIntervalMs,
            Constraint: "must be <= 10000ms",
            Suggestion: "Rate updates too infrequent for monitoring",
        })
    }

    // ═══════════════════════════════════════════════════════════════════════
    // RELATIONSHIP CONSTRAINTS - Ensure protocol coherence
    // ═══════════════════════════════════════════════════════════════════════

    // R5: NAK >= ACK (NAK relies on RTT from ACK)
    if nak < ack {
        errors = append(errors, TimerValidationError{
            Rule:       "R5",
            Field:      "PeriodicNakIntervalMs",
            Value:      nak,
            Constraint: fmt.Sprintf("must be >= PeriodicAckIntervalMs (%d)", ack),
            Suggestion: "NAK uses RTT from ACK; NAK should be slower or equal",
        })
    }

    // R10: Drop >= NAK (allow NAK/retransmit cycle before dropping)
    if drop < nak {
        errors = append(errors, TimerValidationError{
            Rule:       "R10",
            Field:      "SendDropIntervalMs",
            Value:      drop,
            Constraint: fmt.Sprintf("must be >= PeriodicNakIntervalMs (%d)", nak),
            Suggestion: "Drop check should be after NAK has a chance to recover",
        })
    }

    // R13: NAK <= 10 × ACK (keep NAK reasonably coupled)
    if nak > ack*10 {
        errors = append(errors, TimerValidationError{
            Rule:       "R13",
            Field:      "PeriodicNakIntervalMs",
            Value:      nak,
            Constraint: fmt.Sprintf("should be <= 10 × PeriodicAckIntervalMs (%d)", ack*10),
            Suggestion: "NAK too decoupled from ACK; may cause stale RTT issues",
        })
    }

    // R14: In Tick mode, Tick <= 2 × ACK (Tick must fire often enough)
    // Note: In EventLoop mode, ACK has its own ticker, so this doesn't apply
    if !c.UseEventLoop && tick > ack*2 {
        errors = append(errors, TimerValidationError{
            Rule:       "R14",
            Field:      "TickIntervalMs",
            Value:      tick,
            Constraint: fmt.Sprintf("should be <= 2 × PeriodicAckIntervalMs (%d) in Tick mode", ack*2),
            Suggestion: "Tick drives ACK in Tick mode; Tick too slow will delay ACKs",
        })
    }

    // ═══════════════════════════════════════════════════════════════════════
    // TSBPD-RELATED CONSTRAINTS - Ensure timers fit within latency window
    // ═══════════════════════════════════════════════════════════════════════

    // R15: ACK < Latency/2 (multiple ACKs before expiry)
    if ack > latency/2 {
        errors = append(errors, TimerValidationError{
            Rule:       "R15",
            Field:      "PeriodicAckIntervalMs",
            Value:      ack,
            Constraint: fmt.Sprintf("should be < Latency/2 (%d)", latency/2),
            Suggestion: fmt.Sprintf("With Latency=%dms, ACK interval too large for RTT updates", latency),
        })
    }

    // R16: NAK < Latency/2 (multiple NAK cycles before expiry)
    if nak > latency/2 {
        errors = append(errors, TimerValidationError{
            Rule:       "R16",
            Field:      "PeriodicNakIntervalMs",
            Value:      nak,
            Constraint: fmt.Sprintf("should be < Latency/2 (%d)", latency/2),
            Suggestion: fmt.Sprintf("With Latency=%dms, NAK interval too large for recovery", latency),
        })
    }

    // R17: Tick < Latency/4 (multiple delivery opportunities)
    if tick > latency/4 {
        errors = append(errors, TimerValidationError{
            Rule:       "R17",
            Field:      "TickIntervalMs",
            Value:      tick,
            Constraint: fmt.Sprintf("should be < Latency/4 (%d)", latency/4),
            Suggestion: fmt.Sprintf("With Latency=%dms, Tick too slow for smooth delivery", latency),
        })
    }

    // ═══════════════════════════════════════════════════════════════════════
    // SANITY CHECKS - Catch obvious typos
    // ═══════════════════════════════════════════════════════════════════════

    // Typo detection: value looks like microseconds instead of milliseconds
    if c.PeriodicAckIntervalMs > 0 && c.PeriodicAckIntervalMs >= 10000 {
        errors = append(errors, TimerValidationError{
            Rule:       "TYPO",
            Field:      "PeriodicAckIntervalMs",
            Value:      c.PeriodicAckIntervalMs,
            Constraint: "value seems too large (did you mean microseconds?)",
            Suggestion: fmt.Sprintf("Did you mean %dms instead of %dms?",
                c.PeriodicAckIntervalMs/1000, c.PeriodicAckIntervalMs),
        })
    }

    if c.PeriodicNakIntervalMs > 0 && c.PeriodicNakIntervalMs >= 20000 {
        errors = append(errors, TimerValidationError{
            Rule:       "TYPO",
            Field:      "PeriodicNakIntervalMs",
            Value:      c.PeriodicNakIntervalMs,
            Constraint: "value seems too large (did you mean microseconds?)",
            Suggestion: fmt.Sprintf("Did you mean %dms instead of %dms?",
                c.PeriodicNakIntervalMs/1000, c.PeriodicNakIntervalMs),
        })
    }

    if len(errors) > 0 {
        // Return first error (or could combine them)
        return errors[0]
    }

    return nil
}

// validateTimerIntervalsWithWarnings returns warnings for non-fatal issues
func (c Config) validateTimerIntervalsWithWarnings() []string {
    var warnings []string

    ack := c.PeriodicAckIntervalMs
    if ack == 0 { ack = 10 }

    nak := c.PeriodicNakIntervalMs
    if nak == 0 { nak = 20 }

    tick := c.TickIntervalMs
    if tick == 0 { tick = 10 }

    // Warning: High latency configuration
    if ack > 100 || nak > 200 || tick > 100 {
        warnings = append(warnings, fmt.Sprintf(
            "⚠️  High timer intervals (ACK=%dms, NAK=%dms, Tick=%dms) may increase loss recovery latency",
            ack, nak, tick))
    }

    // Warning: Very aggressive configuration
    if ack < 5 || tick < 5 {
        warnings = append(warnings, fmt.Sprintf(
            "⚠️  Aggressive timer intervals (ACK=%dms, Tick=%dms) may cause high CPU usage",
            ack, tick))
    }

    // Warning: Non-standard ratio
    if nak > 0 && ack > 0 && nak != ack*2 {
        warnings = append(warnings, fmt.Sprintf(
            "ℹ️  NAK/ACK ratio is %.1fx (standard is 2x)", float64(nak)/float64(ack)))
    }

    return warnings
}
```

#### 4.6 Example Validation Output

**Good Configuration:**
```
✓ Timer intervals valid
  ACK: 100ms, NAK: 200ms, Tick: 100ms, Drop: 500ms
  Relationships: ACK ≤ NAK ≤ Drop ✓
  TSBPD (3000ms): All intervals < Latency/2 ✓
```

**Bad Configuration (typo):**
```
✗ [TYPO] PeriodicAckIntervalMs=10000ms violates constraint: value seems too large
  Suggestion: Did you mean 10ms instead of 10000ms?
```

**Bad Configuration (relationship):**
```
✗ [R5] PeriodicNakIntervalMs=50ms violates constraint: must be >= PeriodicAckIntervalMs (100)
  Suggestion: NAK uses RTT from ACK; NAK should be slower or equal
```

**Bad Configuration (TSBPD):**
```
✗ [R15] PeriodicAckIntervalMs=2000ms violates constraint: should be < Latency/2 (1500)
  Suggestion: With Latency=3000ms, ACK interval too large for RTT updates
```

## Testing Plan

### 1. Comprehensive Table-Driven Validation Tests (`config_timer_table_test.go`)

```go
package srt

import (
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

// ============================================================================
// Timer Interval Validation Table Tests
// Tests all validation rules R1-R17 plus typo detection
// ============================================================================

type TimerValidationTestCase struct {
    Name                    string
    PeriodicAckIntervalMs   uint64
    PeriodicNakIntervalMs   uint64
    TickIntervalMs          uint64
    SendDropIntervalMs      uint64
    EventLoopRateIntervalMs uint64
    Latency                 time.Duration
    UseEventLoop            bool
    ExpectError             bool
    ExpectRule              string // Expected rule violation (e.g., "R5")
    ExpectField             string // Expected field name
}

var timerValidationTestCases = []TimerValidationTestCase{
    // ═══════════════════════════════════════════════════════════════════════
    // VALID CONFIGURATIONS
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:        "default_values",
        // All zeros = use defaults
        ExpectError: false,
    },
    {
        Name:                  "explicit_defaults",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 20,
        TickIntervalMs:        10,
        SendDropIntervalMs:    100,
        Latency:               3000 * time.Millisecond,
        ExpectError:           false,
    },
    {
        Name:                  "high_throughput_preset",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 200,
        TickIntervalMs:        100,
        SendDropIntervalMs:    500,
        Latency:               5000 * time.Millisecond,
        ExpectError:           false,
    },
    {
        Name:                  "ultra_low_overhead_preset",
        PeriodicAckIntervalMs: 500,
        PeriodicNakIntervalMs: 1000,
        TickIntervalMs:        500,
        SendDropIntervalMs:    2000,
        Latency:               5000 * time.Millisecond,
        ExpectError:           false,
    },
    {
        Name:                  "ack_equals_nak_valid",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 100, // Equal is valid
        TickIntervalMs:        100,
        SendDropIntervalMs:    100,
        Latency:               3000 * time.Millisecond,
        ExpectError:           false,
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R1: ACK >= 1ms
    // ═══════════════════════════════════════════════════════════════════════
    // Note: Can't test < 1ms easily since uint64 can't be fraction
    // Testing boundary: 0 means default, 1 is minimum valid explicit value

    // ═══════════════════════════════════════════════════════════════════════
    // R2: ACK <= 1000ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R2_ack_too_large",
        PeriodicAckIntervalMs: 1001,
        PeriodicNakIntervalMs: 2000,
        TickIntervalMs:        10,
        Latency:               5000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R2",
        ExpectField:           "PeriodicAckIntervalMs",
    },
    {
        Name:                  "R2_ack_at_boundary",
        PeriodicAckIntervalMs: 1000, // Exactly at limit
        PeriodicNakIntervalMs: 2000,
        TickIntervalMs:        10,
        Latency:               5000 * time.Millisecond,
        ExpectError:           false,
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R4: NAK <= 2000ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R4_nak_too_large",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 2001,
        TickIntervalMs:        10,
        Latency:               5000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R4",
        ExpectField:           "PeriodicNakIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R5: NAK >= ACK (critical relationship)
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R5_nak_less_than_ack",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 50, // Violation!
        TickIntervalMs:        10,
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R5",
        ExpectField:           "PeriodicNakIntervalMs",
    },
    {
        Name:                  "R5_nak_much_less_than_ack",
        PeriodicAckIntervalMs: 500,
        PeriodicNakIntervalMs: 10, // Severe violation
        TickIntervalMs:        10,
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R5",
        ExpectField:           "PeriodicNakIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R7: Tick <= 1000ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R7_tick_too_large",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 20,
        TickIntervalMs:        1001,
        Latency:               5000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R7",
        ExpectField:           "TickIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R8: Drop >= 50ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R8_drop_too_small",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 20,
        TickIntervalMs:        10,
        SendDropIntervalMs:    10, // Too small
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R8",
        ExpectField:           "SendDropIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R9: Drop <= 5000ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R9_drop_too_large",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 20,
        TickIntervalMs:        10,
        SendDropIntervalMs:    5001,
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R9",
        ExpectField:           "SendDropIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R10: Drop >= NAK
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R10_drop_less_than_nak",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 200,
        TickIntervalMs:        10,
        SendDropIntervalMs:    100, // Less than NAK!
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R10",
        ExpectField:           "SendDropIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R11: Rate >= 100ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                    "R11_rate_too_small",
        PeriodicAckIntervalMs:   10,
        PeriodicNakIntervalMs:   20,
        TickIntervalMs:          10,
        EventLoopRateIntervalMs: 50,
        Latency:                 3000 * time.Millisecond,
        ExpectError:             true,
        ExpectRule:              "R11",
        ExpectField:             "EventLoopRateIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R12: Rate <= 10000ms
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                    "R12_rate_too_large",
        PeriodicAckIntervalMs:   10,
        PeriodicNakIntervalMs:   20,
        TickIntervalMs:          10,
        EventLoopRateIntervalMs: 10001,
        Latency:                 3000 * time.Millisecond,
        ExpectError:             true,
        ExpectRule:              "R12",
        ExpectField:             "EventLoopRateIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R13: NAK <= 10 × ACK
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R13_nak_too_decoupled",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 200, // 20x ACK, exceeds 10x limit
        TickIntervalMs:        10,
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R13",
        ExpectField:           "PeriodicNakIntervalMs",
    },
    {
        Name:                  "R13_nak_at_10x_boundary",
        PeriodicAckIntervalMs: 10,
        PeriodicNakIntervalMs: 100, // Exactly 10x
        TickIntervalMs:        10,
        Latency:               3000 * time.Millisecond,
        ExpectError:           false, // Equal is OK
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R14: In Tick mode, Tick <= 2 × ACK
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R14_tick_too_slow_for_ack_tick_mode",
        PeriodicAckIntervalMs: 50,
        PeriodicNakIntervalMs: 100,
        TickIntervalMs:        200, // 4x ACK in Tick mode
        UseEventLoop:          false,
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R14",
        ExpectField:           "TickIntervalMs",
    },
    {
        Name:                  "R14_tick_slow_but_eventloop_mode",
        PeriodicAckIntervalMs: 50,
        PeriodicNakIntervalMs: 100,
        TickIntervalMs:        200, // Would fail in Tick mode
        UseEventLoop:          true, // But OK in EventLoop mode
        Latency:               3000 * time.Millisecond,
        ExpectError:           false,
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R15: ACK < Latency/2
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R15_ack_too_large_for_latency",
        PeriodicAckIntervalMs: 1000,
        PeriodicNakIntervalMs: 1000,
        TickIntervalMs:        100,
        Latency:               1500 * time.Millisecond, // Latency/2 = 750ms < 1000ms
        ExpectError:           true,
        ExpectRule:            "R15",
        ExpectField:           "PeriodicAckIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R16: NAK < Latency/2
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R16_nak_too_large_for_latency",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 1000,
        TickIntervalMs:        100,
        SendDropIntervalMs:    1000,
        Latency:               1500 * time.Millisecond, // Latency/2 = 750ms < 1000ms
        ExpectError:           true,
        ExpectRule:            "R16",
        ExpectField:           "PeriodicNakIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // R17: Tick < Latency/4
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "R17_tick_too_large_for_latency",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 200,
        TickIntervalMs:        500,
        SendDropIntervalMs:    500,
        Latency:               1500 * time.Millisecond, // Latency/4 = 375ms < 500ms
        ExpectError:           true,
        ExpectRule:            "R17",
        ExpectField:           "TickIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // TYPO DETECTION
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "typo_ack_microseconds",
        PeriodicAckIntervalMs: 10000, // Looks like 10ms in µs
        PeriodicNakIntervalMs: 20000,
        TickIntervalMs:        10,
        Latency:               30000 * time.Millisecond, // Have to increase latency too
        ExpectError:           true,
        ExpectRule:            "TYPO",
        ExpectField:           "PeriodicAckIntervalMs",
    },
    {
        Name:                  "typo_nak_microseconds",
        PeriodicAckIntervalMs: 100,
        PeriodicNakIntervalMs: 20000, // Looks like 20ms in µs
        TickIntervalMs:        10,
        Latency:               50000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "TYPO",
        ExpectField:           "PeriodicNakIntervalMs",
    },

    // ═══════════════════════════════════════════════════════════════════════
    // EDGE CASES - Multiple violations
    // ═══════════════════════════════════════════════════════════════════════
    {
        Name:                  "multiple_violations_reports_first",
        PeriodicAckIntervalMs: 2000, // R2 violation
        PeriodicNakIntervalMs: 50,   // R5 violation
        TickIntervalMs:        2000, // R7 violation
        Latency:               3000 * time.Millisecond,
        ExpectError:           true,
        ExpectRule:            "R2", // First rule checked
    },
}

func TestTimerIntervalValidation(t *testing.T) {
    for _, tc := range timerValidationTestCases {
        t.Run(tc.Name, func(t *testing.T) {
            cfg := DefaultConfig()

            // Apply test values (0 means use default)
            if tc.PeriodicAckIntervalMs != 0 {
                cfg.PeriodicAckIntervalMs = tc.PeriodicAckIntervalMs
            }
            if tc.PeriodicNakIntervalMs != 0 {
                cfg.PeriodicNakIntervalMs = tc.PeriodicNakIntervalMs
            }
            if tc.TickIntervalMs != 0 {
                cfg.TickIntervalMs = tc.TickIntervalMs
            }
            if tc.SendDropIntervalMs != 0 {
                cfg.SendDropIntervalMs = tc.SendDropIntervalMs
            }
            if tc.EventLoopRateIntervalMs != 0 {
                cfg.EventLoopRateIntervalMs = tc.EventLoopRateIntervalMs
            }
            if tc.Latency != 0 {
                cfg.Latency = tc.Latency
            }
            cfg.UseEventLoop = tc.UseEventLoop

            err := cfg.validateTimerIntervals()

            if tc.ExpectError {
                require.Error(t, err, "Expected validation error for %s", tc.Name)

                // Check it's the right error type
                if terr, ok := err.(TimerValidationError); ok {
                    if tc.ExpectRule != "" {
                        require.Equal(t, tc.ExpectRule, terr.Rule,
                            "Expected rule %s but got %s", tc.ExpectRule, terr.Rule)
                    }
                    if tc.ExpectField != "" {
                        require.Equal(t, tc.ExpectField, terr.Field,
                            "Expected field %s but got %s", tc.ExpectField, terr.Field)
                    }
                }
            } else {
                require.NoError(t, err, "Unexpected validation error for %s: %v", tc.Name, err)
            }
        })
    }
}

// TestTimerIntervalValidationPresets tests known-good presets
func TestTimerIntervalValidationPresets(t *testing.T) {
    presets := []struct {
        name   string
        ack    uint64
        nak    uint64
        tick   uint64
        drop   uint64
        rate   uint64
        latency time.Duration
    }{
        {"default", 10, 20, 10, 100, 1000, 3000 * time.Millisecond},
        {"moderate", 50, 100, 50, 200, 1000, 3000 * time.Millisecond},
        {"high_throughput", 100, 200, 100, 500, 1000, 5000 * time.Millisecond},
        {"ultra_low_overhead", 500, 1000, 500, 2000, 5000, 5000 * time.Millisecond},
        {"extreme_latency", 1000, 2000, 1000, 5000, 10000, 10000 * time.Millisecond},
    }

    for _, p := range presets {
        t.Run(p.name, func(t *testing.T) {
            cfg := DefaultConfig()
            cfg.PeriodicAckIntervalMs = p.ack
            cfg.PeriodicNakIntervalMs = p.nak
            cfg.TickIntervalMs = p.tick
            cfg.SendDropIntervalMs = p.drop
            cfg.EventLoopRateIntervalMs = p.rate
            cfg.Latency = p.latency

            err := cfg.validateTimerIntervals()
            require.NoError(t, err, "Preset %s should be valid", p.name)
        })
    }
}
```

### 2. Table-Driven Timer Tests (`congestion/live/receive/timer_interval_table_test.go`)

```go
type TimerIntervalTestCase struct {
    Name              string
    AckIntervalMs     uint64
    NakIntervalMs     uint64
    TickIntervalMs    uint64
    TestDurationMs    uint64
    ExpectedACKCount  int // Approximate
    ExpectedNAKCount  int // Approximate
    ExpectedTickCount int // Approximate
}

var timerIntervalTestCases = []TimerIntervalTestCase{
    {
        Name:              "default_10ms_20ms",
        AckIntervalMs:     10,
        NakIntervalMs:     20,
        TickIntervalMs:    10,
        TestDurationMs:    1000,
        ExpectedACKCount:  100, // 1000/10 = 100
        ExpectedNAKCount:  50,  // 1000/20 = 50
        ExpectedTickCount: 100,
    },
    {
        Name:              "high_throughput_100ms_200ms",
        AckIntervalMs:     100,
        NakIntervalMs:     200,
        TickIntervalMs:    100,
        TestDurationMs:    1000,
        ExpectedACKCount:  10, // 1000/100 = 10
        ExpectedNAKCount:  5,  // 1000/200 = 5
        ExpectedTickCount: 10,
    },
    {
        Name:              "ultra_low_overhead_500ms_1000ms",
        AckIntervalMs:     500,
        NakIntervalMs:     1000,
        TickIntervalMs:    500,
        TestDurationMs:    5000,
        ExpectedACKCount:  10, // 5000/500 = 10
        ExpectedNAKCount:  5,  // 5000/1000 = 5
        ExpectedTickCount: 10,
    },
}
```

### 3. EventLoop Timer Tests (`congestion/live/send/eventloop_timer_test.go`)

```go
func TestEventLoop_DropTickerInterval(t *testing.T) {
    tests := []struct {
        name             string
        dropIntervalMs   uint64
        testDurationMs   uint64
        expectedDropRuns int
    }{
        {"default_100ms", 100, 500, 5},
        {"slow_500ms", 500, 2000, 4},
        {"fast_50ms", 50, 500, 10},
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            m := &metrics.ConnectionMetrics{}
            s := NewSender(SendConfig{
                // ... other config ...
                SendDropIntervalUs: tc.dropIntervalMs * 1000,
            }).(*sender)

            ctx, cancel := context.WithTimeout(context.Background(),
                time.Duration(tc.testDurationMs)*time.Millisecond)
            defer cancel()

            s.EventLoop(ctx, nil)

            dropFires := m.SendEventLoopDropFires.Load()
            // Allow ±1 for timing jitter
            require.InDelta(t, tc.expectedDropRuns, int(dropFires), 1)
        })
    }
}
```

### 4. Integration Test via `make test-flags`

Update `Makefile`:

```makefile
test-flags:
    @echo "Testing timer interval flags..."
    ./contrib/server/server -tickintervalms 100 -periodicackintervalms 100 \
        -periodicnakintervalms 200 -senddropintervalms 500 -h
    @echo "Timer interval flags validated"
```

## Implementation Plan

### Phase 1: Add New Config Fields (Low Risk)

1. Add `SendDropIntervalMs` to `config.go`
2. Add `EventLoopRateIntervalMs` to `config.go`
3. Update `DefaultConfig()` with default values
4. Run `make test` - should pass (no behavior change yet)

### Phase 2: Add CLI Flags (Low Risk)

1. Add `senddropintervalms` to `contrib/common/flags.go`
2. Add `eventlooprateintervalms` to `contrib/common/flags.go`
3. Add flag parsing in `ApplyConfigFromFlags()`
4. Run `make test-flags` to validate

### Phase 3: Sender EventLoop Changes (Medium Risk)

1. Add `SendDropIntervalUs` to `SendConfig`
2. Update `NewSender()` to store the config value
3. Update `EventLoop()` to use configurable drop interval
4. Add unit tests for drop ticker interval
5. Run `go test -race ./congestion/live/send/...`

### Phase 4: Receiver EventLoop Changes (Low Risk)

1. Add `EventLoopRateIntervalUs` to receiver `Config`
2. Update `New()` to store the config value
3. Update `EventLoop()` to use configurable rate interval
4. Add unit tests
5. Run `go test -race ./congestion/live/receive/...`

### Phase 5: Validation (Low Risk)

1. Add `validateTimerIntervals()` to `config_validate.go`
2. Call from main `Validate()` function
3. Add table-driven validation tests
4. Run `make test`

### Phase 6: Performance Testing

1. Run performance tests with default intervals (baseline)
2. Run with 100ms/200ms intervals
3. Run with 500ms/1000ms intervals
4. Document throughput vs latency tradeoffs

## CPU Savings Estimation

### Workload Analysis at 300 Mb/s

**Packet Rate Calculation:**
```
Bitrate:        300,000,000 bits/sec
Packet Size:    1,456 bytes (typical SRT payload)
Bytes/sec:      300,000,000 / 8 = 37,500,000 bytes/sec
Packets/sec:    37,500,000 / 1,456 ≈ 25,755 packets/sec
```

### Timer Fire Rates

| Timer | Default Interval | Fires/sec | High-Throughput (100ms) | Fires/sec |
|-------|-----------------|-----------|-------------------------|-----------|
| ACK | 10ms | 100 | 100ms | 10 |
| NAK | 20ms | 50 | 200ms | 5 |
| Tick | 10ms | 100 | 100ms | 10 |
| Drop | 100ms | 10 | 500ms | 2 |
| **Total** | | **260** | | **27** |

**Reduction: 260 → 27 timer fires/sec = 90% fewer timer fires**

### Work Per Timer Fire

Each timer fire involves significant work:

| Timer | Work Per Fire | CPU Impact |
|-------|---------------|------------|
| **ACK** | Scan packet store, calculate contiguous point, update RTT | O(n) where n = packets since last ACK |
| **NAK** | Scan for gaps, build NAK list, send NAK packet | O(n) where n = gap candidates |
| **Tick** | Iterate btree, check TSBPD times, deliver ready packets | O(log n) × delivered |
| **Drop** | Iterate btree, check drop threshold, remove stale | O(log n) × dropped |

### Estimated CPU Savings at 300 Mb/s

**Default (10ms/20ms/10ms):**
```
ACK scans:     100/sec × ~257 packets/scan = 25,700 packet-checks/sec
NAK scans:      50/sec × ~514 packets/scan = 25,700 packet-checks/sec
Tick iterations: 100/sec × ~257 deliveries = 25,700 btree-ops/sec
Drop checks:    10/sec × ~2575 packets    = 25,750 checks/sec
─────────────────────────────────────────────────────────────────
Total overhead: ~103,000 operations/sec
```

**High-Throughput (100ms/200ms/100ms):**
```
ACK scans:      10/sec × ~2575 packets/scan = 25,750 packet-checks/sec
NAK scans:       5/sec × ~5150 packets/scan = 25,750 packet-checks/sec
Tick iterations: 10/sec × ~2575 deliveries  = 25,750 btree-ops/sec
Drop checks:     2/sec × ~12875 packets     = 25,750 checks/sec
─────────────────────────────────────────────────────────────────
Total overhead: ~103,000 operations/sec (same!)
```

**Key Insight**: Total operations/sec stays the same, but:

### Where the Real Savings Come From

1. **Function Call Overhead**
   - Each timer fire: goroutine context switch, function call, defer cleanup
   - Default: 260 function calls/sec
   - High-Throughput: 27 function calls/sec
   - **Savings: ~90% fewer function calls**

2. **Lock Acquisition**
   - Each Tick acquires mutex
   - Default: 100 lock acquisitions/sec
   - High-Throughput: 10 lock acquisitions/sec
   - **Savings: 90% fewer lock acquisitions**

3. **Ticker Channel Operations**
   - Each ticker fire involves channel receive
   - Default: 260 channel ops/sec
   - High-Throughput: 27 channel ops/sec
   - **Savings: 90% fewer channel operations**

4. **Btree Iterator Initialization**
   - Each scan starts a new iterator
   - Larger batches = fewer iterator setups
   - **Savings: 90% fewer iterator creations**

5. **ACK Packet Generation**
   - Each Full ACK generates and sends a control packet
   - Default: 100 ACK packets/sec
   - High-Throughput: 10 ACK packets/sec
   - **Savings: 90% fewer ACK packets**

### Projected Throughput Impact

| Config | Timer Overhead | Estimated Ceiling | Improvement |
|--------|---------------|-------------------|-------------|
| Default (10ms/20ms) | 260 fires/sec | 353 Mb/s | Baseline |
| Moderate (50ms/100ms) | 50 fires/sec | ~400 Mb/s | +13% |
| High-Throughput (100ms/200ms) | 27 fires/sec | ~450 Mb/s | +27% |
| Ultra-Low (500ms/1000ms) | 7 fires/sec | ~500 Mb/s | +41% |

### Trade-off Analysis

| Interval | ACK Latency | NAK Latency | Loss Recovery | RTT Accuracy |
|----------|-------------|-------------|---------------|--------------|
| 10ms/20ms | 10ms | 20ms | ~40ms | Excellent |
| 50ms/100ms | 50ms | 100ms | ~200ms | Good |
| 100ms/200ms | 100ms | 200ms | ~400ms | Acceptable |
| 500ms/1000ms | 500ms | 1000ms | ~2s | Poor |

**Recommendation for 500 Mb/s Target:**
- Use **100ms/200ms** for good balance
- With 5000ms TSBPD latency, 400ms loss recovery is acceptable
- Beyond 500ms intervals, RTT accuracy suffers significantly

### Validation: Will Larger Intervals Cause Packet Loss?

**Analysis for 100ms ACK, 200ms NAK, 5000ms Latency:**

```
TSBPD Window:        5000ms (packets have 5s to arrive)
NAK Cycle Time:      200ms (gap detected)
Retransmit RTT:      ~10ms (on loopback) or ~100ms (WAN)
Recovery Attempts:   5000ms / (200ms + 100ms) ≈ 16 attempts

Conclusion: Even with 200ms NAK intervals, there are 16 opportunities
            to recover a lost packet before TSBPD expiry.
            Risk: LOW for 5000ms latency, MODERATE for 3000ms latency.
```

## Expected Results

| Config Preset | ACK/NAK/Tick | Timer Fires/sec | Expected Ceiling |
|---------------|--------------|-----------------|------------------|
| Default | 10ms/20ms/10ms | 260 | 353 Mb/s (current) |
| Moderate | 50ms/100ms/50ms | 50 | ~400 Mb/s |
| High Throughput | 100ms/200ms/100ms | 27 | ~450 Mb/s |
| Ultra Low Overhead | 500ms/1000ms/500ms | 7 | ~500 Mb/s (target!) |

**Trade-offs:**
- **Higher intervals** = Lower CPU overhead, but higher latency for loss recovery
- **Lower intervals** = Lower latency, but higher CPU overhead at high throughput

**Recommendation:** Start with **100ms/200ms/100ms** and measure impact

## Files to Modify

| File | Change |
|------|--------|
| `config.go` | Add `SendDropIntervalMs`, `EventLoopRateIntervalMs` |
| `config_validate.go` | Add timer interval validation |
| `contrib/common/flags.go` | Add new CLI flags |
| `congestion/live/send/sender.go` | Add `SendDropIntervalUs` to SendConfig |
| `congestion/live/send/eventloop.go` | Use configurable drop interval |
| `congestion/live/receive/receiver.go` | Add `EventLoopRateIntervalUs` |
| `congestion/live/receive/event_loop.go` | Use configurable rate interval |
| `connection.go` | Pass new config to sender |

## Test Files to Add/Modify

| File | Change |
|------|--------|
| `config_test.go` | Add timer interval validation tests |
| `config_table_test.go` | Add timer interval table tests |
| `congestion/live/send/eventloop_timer_test.go` | NEW: Drop ticker tests |
| `congestion/live/receive/timer_interval_table_test.go` | NEW: Timer interval table tests |

## Success Criteria

1. ✅ All existing tests pass
2. ✅ New timer interval flags work correctly
3. ✅ Validation catches invalid configurations
4. ✅ Performance testing shows measurable improvement with higher intervals
5. ✅ No regressions at default settings

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Too-high intervals cause packet loss | Validation limits max to 5000ms |
| NAK < ACK causes RTT issues | Validation ensures NAK >= ACK |
| Breaking change | Default values match current behavior |
| Performance regression | Test with defaults first |

## Next Steps

1. [ ] Review this design
2. [ ] Implement Phase 1-2 (config + flags)
3. [ ] Run `make test` and `make test-flags`
4. [ ] Implement Phase 3-4 (code changes)
5. [ ] Run full test suite
6. [ ] Performance testing with new intervals
7. [ ] Update performance log with results
