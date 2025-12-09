# Graceful Quiesce Design for Accurate Metrics Collection

**Status**: 📋 Design Complete, Ready for Implementation
**Created**: 2024-12-09
**Related Documents**:
- `integration_testing_design.md` (needs update after implementation)
- `integration_testing_with_network_impairment_defects.md` (Defects 3, 4)

## Problem Statement

### The Core Issue

During integration tests, we send OS signals (SIGINT) to stop each component independently. Each process shuts down at its own pace, leading to **counter discrepancies**:

```
Timeline of Current Shutdown:
─────────────────────────────────────────────────────────────────────────────
Time:     T0          T1          T2          T3          T4
          │           │           │           │           │
Client:   │ SIGINT ───┼─ closing ─┼─ CLOSED   │           │
          │           │           │           │           │
Server:   │           │ SIGINT ───┼─ closing ─┼─ CLOSED   │
          │           │           │           │           │
ClntGen:  │           │           │ SIGINT ───┼─ closing ─┼─ CLOSED
          │           │           │           │           │
─────────────────────────────────────────────────────────────────────────────
```

**What happens during this unsynchronized shutdown:**

1. **Client receives SIGINT first** → Closes SRT connection
2. **Server still thinks client is connected** → Continues processing, maybe sends data
3. **Client-generator still sending** → Server receives data, sends to dead client connection
4. **Packets sent after peer closed** → Counted by sender but never by receiver

This creates **permanent counter mismatches** that are not bugs in SRT, but artifacts of our test methodology.

### Evidence from Test Runs

```
Client-generator sent:  21,426 packets
Server received:        20,509 packets (from client-gen)
Server sent:            18,614 packets (to client)
Client received:        18,614 packets
```

The difference (21,426 - 20,509 = 917 packets) includes:
- Packets lost to netem (expected)
- Packets in flight during shutdown (artifact)
- Packets sent after receiver closed (artifact)

We cannot distinguish "real network loss" from "shutdown timing artifacts."

---

## Proposed Solution: Quiesce Before Shutdown

### Core Insight

Instead of abruptly shutting down, we **quiesce** the data flow first:

1. **Stop the data source** (client-generator stops generating)
2. **Wait for pipeline to drain** (all in-flight data delivered)
3. **Wait for ACK/NAK to settle** (retransmissions complete)
4. **Collect metrics** (counters are now stable)
5. **Then shutdown** (actual process termination)

### What Happens in SRT When Sender Pauses?

In SRT live mode, when the sender stops pushing data:

| Phase | Duration | What Happens |
|-------|----------|--------------|
| **1. Data drain** | ~latency (TsbPdDelay) | Receiver's buffer empties, delivers remaining packets |
| **2. ACK completion** | ~2-3 RTTs | Final ACKs sent for last data |
| **3. NAK resolution** | ~latency | Any pending retransmissions complete |
| **4. Quiescent state** | Indefinite | Only keepalives flow, counters stable |

```
Quiesce Timeline:
─────────────────────────────────────────────────────────────────────────────
                    │ PAUSE SIGNAL          │ STABLE        │ SHUTDOWN
                    │                       │               │
Data flow:     ═════╪══════ draining ══════╪═══════════════╪═══════════════
                    │                       │               │
ACK/NAK:       ═════╪═══ settling ═════════╪═══════════════╪═══════════════
                    │                       │               │
Keepalive:     ═════╪═══════════════════════╪═══════════════╪═══════════════
                    │                       │               │
Counters:      ═════╪═══ still changing ═══╪═══ STABLE ════╪═══════════════
                    │                       │               │
                    │<──── Drain Time ─────>│<── Collect ──>│
                    │      (3-5 seconds)    │   Metrics     │
─────────────────────────────────────────────────────────────────────────────
```

### Why This Works

1. **No new data** → No new sequence numbers → No new loss detection
2. **Pending ACKs complete** → All sent data acknowledged
3. **Pending NAKs resolve** → Retransmissions either succeed or timeout
4. **Keepalives maintain connection** → Metrics remain available
5. **Counters stabilize** → Final values are accurate

---

## Implementation Design

### Signal Handling

We introduce a new signal for "pause data generation":

| Signal | Current Behavior | New Behavior |
|--------|-----------------|--------------|
| `SIGINT` | Immediate shutdown | Immediate shutdown (unchanged) |
| `SIGTERM` | Immediate shutdown | Immediate shutdown (unchanged) |
| `SIGUSR1` | Not handled | **PAUSE data generation** (new) |

### Client-Generator Changes

```go
// Current behavior
func main() {
    // ...
    for {
        select {
        case <-ctx.Done():
            return  // Exit immediately on SIGINT
        default:
            conn.Write(generatePacket())  // Continuously send
        }
    }
}

// New behavior with pause support
var paused atomic.Bool

func main() {
    // Handle SIGUSR1 for pause
    pauseChan := make(chan os.Signal, 1)
    signal.Notify(pauseChan, syscall.SIGUSR1)
    go func() {
        <-pauseChan
        log.Println("PAUSE signal received - stopping data generation")
        paused.Store(true)
    }()

    for {
        select {
        case <-ctx.Done():
            return  // Exit on SIGINT
        default:
            if !paused.Load() {
                conn.Write(generatePacket())  // Only send if not paused
            } else {
                time.Sleep(100 * time.Millisecond)  // Idle when paused
            }
        }
    }
}
```

### Test Orchestrator Changes

```go
// Current shutdown sequence
func shutdown() {
    sendSignal(client, SIGINT)      // 1. Stop client
    wait(1 * time.Second)
    sendSignal(clientGen, SIGINT)   // 2. Stop client-generator
    wait(1 * time.Second)
    sendSignal(server, SIGINT)      // 3. Stop server
    collectMetrics()                 // 4. Collect (but connections are gone!)
}

// New quiesce-then-shutdown sequence
func shutdown() {
    // Phase 1: Quiesce - stop data flow
    sendSignal(clientGen, SIGUSR1)  // 1. Pause data generation

    // Phase 2: Drain - wait for pipeline to empty
    drainTime := config.Latency + 2*time.Second  // TsbPdDelay + safety margin
    wait(drainTime)

    // Phase 3: Collect - metrics are now stable
    collectMetrics()                 // 2. Collect while connections are UP

    // Phase 4: Shutdown - actual termination
    sendSignal(client, SIGINT)      // 3. Stop client
    sendSignal(clientGen, SIGINT)   // 4. Stop client-generator
    sendSignal(server, SIGINT)      // 5. Stop server
}
```

### Dynamic Stabilization Detection (Preferred)

Instead of waiting fixed drain times, **poll metrics until they stabilize**.

**Key Insight**: Network RTTs are measured in milliseconds (60-120ms typical). After pausing data generation, all in-flight packets, ACKs, and NAKs should settle within a few RTTs - often **under 1 second**.

**Stabilization Metrics to Monitor**:

| Metric | Component | Indicates |
|--------|-----------|-----------|
| `PktSentDataSuccess` | Client-Generator | Data transmission stopped |
| `PktRecvDataSuccess` | Server, Client | All data received |
| `PktSentACKSuccess` | Server, Client | ACKs complete |
| `PktRecvACKSuccess` | Client-Generator, Server | ACKs received |
| `PktSentNAKSuccess` | Server | NAKs complete |
| `PktRecvNAKSuccess` | Client-Generator | NAKs received |
| `CongestionSendPktRetrans` | Client-Generator | Retransmissions complete |

**Algorithm**:

```
1. SIGUSR1 → Client-Generator (pause data)
2. Wait 100ms (initial settling)
3. Poll all components every 100ms
4. If key metrics unchanged for 2 consecutive polls → STABLE
5. Maximum timeout: 5 seconds (safety net)
```

**Expected Performance**:

| Scenario | Fixed Wait | Dynamic Wait |
|----------|------------|--------------|
| 120ms latency, no loss | 3 seconds | ~300-500ms |
| 500ms latency, no loss | 4 seconds | ~800ms-1s |
| 3000ms latency, 5% loss | 8 seconds | ~2-3s (retrans needed) |

**Test Suite Impact**:
- 10 tests × 3s average savings = **30 seconds faster**
- Encourages frequent test runs

### Fallback: Fixed Drain Times

If dynamic detection fails or times out, fall back to fixed times:

| Latency Config | TsbPdDelay | Fixed Drain Time |
|----------------|------------|------------------|
| 120ms | 120ms | 2 seconds |
| 500ms | 500ms | 3 seconds |
| 1000ms | 1000ms | 4 seconds |
| 3000ms | 3000ms | 6 seconds |

Formula: `fixedDrainTime = TsbPdDelay + 2 seconds`

---

## Visual Flow Diagram

### Current Flow (Problematic)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CURRENT TEST FLOW                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  TEST RUNNING                           SHUTDOWN                            │
│  ───────────                            ────────                            │
│                                                                             │
│  ┌──────────┐   data    ┌──────────┐   data    ┌──────────┐                │
│  │ Client   │◄─────────│  Server  │◄─────────│ Client   │                │
│  │ Generator│   ACK/NAK │          │   ACK/NAK │          │                │
│  └──────────┘──────────►└──────────┘──────────►└──────────┘                │
│       │                      │                      │                       │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ Collect "pre-shutdown" metrics                                       │   │
│  │ (DATA STILL FLOWING! Counters changing!)                             │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│     SIGINT                 SIGINT                 SIGINT                    │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│    CLOSED                  CLOSED                 CLOSED                    │
│  (counters               (counters              (counters                   │
│   unregistered)           unregistered)          unregistered)              │
│       │                      │                      │                       │
│       └──────────────────────┼──────────────────────┘                       │
│                              ▼                                              │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ Collect "final" metrics                                              │   │
│  │ (CONNECTIONS CLOSED! Metrics may be gone or stale!)                  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  PROBLEM: Counters are either still changing or already unregistered!      │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### New Flow with Quiesce (Solution)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           NEW TEST FLOW WITH QUIESCE                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  TEST RUNNING                  QUIESCE                     SHUTDOWN         │
│  ───────────                   ───────                     ────────         │
│                                                                             │
│  ┌──────────┐   data    ┌──────────┐   data    ┌──────────┐                │
│  │ Client   │◄─────────│  Server  │◄─────────│ Client   │                │
│  │ Generator│   ACK/NAK │          │   ACK/NAK │          │                │
│  └──────────┘──────────►└──────────┘──────────►└──────────┘                │
│       │                      │                      │                       │
│       ▼                      │                      │                       │
│   SIGUSR1                    │                      │                       │
│   (PAUSE)                    │                      │                       │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│  ┌──────────┐ no data   ┌──────────┐ draining  ┌──────────┐                │
│  │ PAUSED   │──────────►│  Server  │──────────►│  Client  │                │
│  │(idle)    │  ACK only │  (buffer │   data    │          │                │
│  └──────────┘◄──────────┤  draining)│          │          │                │
│                         └──────────┘          └──────────┘                │
│       │                      │                      │                       │
│       │       Wait drain time (TsbPdDelay + 2s)     │                       │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ Collect "quiesced" metrics                                           │   │
│  │ (CONNECTIONS UP! Counters STABLE! No data flowing!)                  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│     SIGINT                 SIGINT                 SIGINT                    │
│       │                      │                      │                       │
│       ▼                      ▼                      ▼                       │
│    CLOSED                  CLOSED                 CLOSED                    │
│                                                                             │
│  SOLUTION: Metrics collected while stable, before any shutdown!            │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Expected Improvements

### Counter Alignment After Quiesce

With quiesce, we expect:

| Counter Relationship | Before Quiesce | After Quiesce |
|----------------------|----------------|---------------|
| ClientGen.PktSent - Server.PktRecv | ~1000 (artifacts) | ~0 (only real loss) |
| Server.PktSent - Client.PktRecv | ~500 (artifacts) | ~0 |
| Total retransmissions | Accurate | Accurate |
| Unrecovered packets | Inflated | Accurate |

### Clean Network Tests

For tests without packet loss:
```
ClientGen.PktSent == Server.PktRecv  (exactly)
Server.PktSent == Client.PktRecv     (exactly)
```

### Network Impairment Tests

For tests with packet loss:
```
ClientGen.PktSent - Server.PktRecv == ActualLoss  (from netem)
Server.PktSent - Client.PktRecv == ActualLoss     (from netem on return path if any)
```

---

## Verification Plan

### Step 1: Implement SIGUSR1 Handler

Add pause support to client-generator only (it's the data source).

### Step 2: Update Test Orchestrator

Modify shutdown sequence to:
1. SIGUSR1 → client-generator
2. Wait drain time
3. Collect metrics
4. SIGINT → all components

### Step 3: Run Clean Network Test

Verify:
- `ClientGen.PktSent == Server.PktRecv` (within 1-2 packets)
- `Server.PktSent == Client.PktRecv` (within 1-2 packets)

### Step 4: Run Loss Test

Verify:
- Loss count matches netem configuration
- Retransmission count aligns with loss
- Recovery rate calculation is accurate

---

## Alternative Approaches Considered

### A: Close SRT Connection Gracefully (Rejected)

**Idea**: Send SHUTDOWN control packet before SIGINT.

**Problem**: Once connection closes, `metrics.ConnectionMetrics` is unregistered. We lose access to the counters.

### B: Longer Wait After SIGINT (Current Approach - Insufficient)

**Idea**: Wait longer between SIGINTs.

**Problem**: Doesn't solve the fundamental issue. One end is still running while the other has closed.

### C: Coordinated Shutdown via Out-of-Band Channel (Complex)

**Idea**: Add IPC between components to coordinate shutdown.

**Problem**: Too complex. Requires new communication channel.

### D: Quiesce Data Source (Proposed - Selected)

**Idea**: Stop data generation, let pipeline drain, then collect metrics.

**Pros**:
- Simple to implement (one signal handler)
- Connections remain up (metrics available)
- Counters stabilize naturally
- Works with existing infrastructure

---

## Implementation Checklist

- [ ] Add SIGUSR1 handler to `contrib/client-generator/main.go`
- [ ] Add `paused` atomic flag
- [ ] Modify data generation loop to check pause flag
- [ ] Update `test_network_mode.go` shutdown sequence
- [ ] Add quiesce phase before metrics collection
- [ ] Calculate drain time from config.Latency
- [ ] Test with clean network (verify counter alignment)
- [ ] Test with 2% loss (verify loss count accuracy)
- [ ] Test with 5% loss (verify Defect 3 is resolved)
- [ ] Update documentation

---

## Impact on ALL Tests

### Clean Network Tests (TestModeClean)

**Current State**: These tests appear to work but have hidden inaccuracies.

**Current Shutdown Sequence** (from `test_graceful_shutdown.go`):
```go
// Step 1: SIGINT to Client-Generator
// Step 2: Brief wait (500ms)
// Step 3: SIGINT to Client
// Step 4: SIGINT to Server
```

**Problem**: Even without network loss, the 500ms wait is insufficient:
- Client-generator may have packets in flight
- Server may have packets in its buffer
- ACKs may be in transit

**Expected Improvement After Quiesce**:
```
BEFORE: ClientGen.PktSent - Server.PktRecv = ~50-200 packets (shutdown artifacts)
AFTER:  ClientGen.PktSent - Server.PktRecv = 0 packets (exact match)
```

This enables **Pipeline Balance Verification** for clean network tests:
- `ClientGen.PktSent == Server.PktRecv` (verified)
- `Server.PktSent == Client.PktRecv` (verified)

### Network Impairment Tests (TestModeNetwork)

**Current State**: Statistical validation fails due to shutdown artifacts mixed with real loss.

**Current Shutdown Sequence** (from `test_network_mode.go`):
```go
// Clear loss
// SIGINT to Client
// Wait 5s
// SIGINT to Client-Generator
// Wait 8s
// SIGINT to Server
```

**Problem**: Even after clearing loss, packets in flight during shutdown create artifacts.

**Expected Improvement After Quiesce**:
- Loss counts will reflect **only netem-induced loss**, not shutdown artifacts
- Recovery rate will be accurate
- "Lost: 0" bug (Defect 4) should be resolved

---

## Files Requiring Updates

### 0. SRT Core: Proactive Keepalive (Prerequisite)

**File**: `connection.go`

| Change | Description |
|--------|-------------|
| Add `sendProactiveKeepalive()` | New method to send keepalive packet |
| Modify `watchPeerIdleTimeout()` | Add keepalive ticker at 75% of PeerIdleTimeout |
| Add keepalive tracking | Track last keepalive time to avoid flooding |

This ensures connections stay alive during quiesce period, even with short `PeerIdleTimeout`.

### 1. Client-Generator Binary (Core Change)

**File**: `contrib/client-generator/main.go`

| Change | Description |
|--------|-------------|
| Add SIGUSR1 handler | New signal handler for pause |
| Add `paused atomic.Bool` | Thread-safe pause flag |
| Modify data loop | Check pause flag before sending |
| Add log message | "PAUSE signal received - stopping data generation" |

### 2. Clean Network Test Orchestrator

**File**: `contrib/integration_testing/test_graceful_shutdown.go`

**Current Shutdown** (lines ~447-545):
```go
// Step 1: SIGINT to Client-Generator
// Step 2: Brief wait (500ms)
// Step 3: SIGINT to Client
// Step 4: SIGINT to Server
```

**New Shutdown**:
```go
// Phase 1: QUIESCE
// Step 1: SIGUSR1 to Client-Generator (pause data)
// Step 2: Wait drain time (TsbPdDelay + 2s)
// Step 3: Collect metrics (connections still up!)

// Phase 2: SHUTDOWN
// Step 4: SIGINT to Client
// Step 5: SIGINT to Client-Generator
// Step 6: SIGINT to Server
```

### 3. Network Impairment Test Orchestrator

**File**: `contrib/integration_testing/test_network_mode.go`

**Current Shutdown** (lines ~200-299):
```go
// Clear loss
// SIGINT to Client
// Wait 5s
// SIGINT to Client-Generator
// Wait 8s
// SIGINT to Server
// Collect final metrics (connections already closed!)
```

**New Shutdown**:
```go
// Phase 1: QUIESCE
// Step 1: Clear loss (as before)
// Step 2: SIGUSR1 to Client-Generator (pause data)
// Step 3: Wait drain time (TsbPdDelay + 2s)
// Step 4: Collect metrics (connections still up!)

// Phase 2: SHUTDOWN
// Step 5: SIGINT to Client
// Step 6: SIGINT to Client-Generator
// Step 7: SIGINT to Server
```

### 4. Metrics Stabilization Helper

**File**: `metrics/stabilization.go` (NEW)

This new file provides a clean API for detecting when metrics have stabilized.

```go
package metrics

import (
    "context"
    "time"
)

// StabilizationConfig configures the stabilization detection.
type StabilizationConfig struct {
    // PollInterval is how often to check metrics (default: 100ms)
    PollInterval time.Duration

    // StableCount is how many consecutive unchanged polls required (default: 2)
    StableCount int

    // MaxWait is the maximum time to wait before giving up (default: 5s)
    MaxWait time.Duration
}

// DefaultStabilizationConfig returns sensible defaults.
func DefaultStabilizationConfig() StabilizationConfig {
    return StabilizationConfig{
        PollInterval: 100 * time.Millisecond,
        StableCount:  2,
        MaxWait:      5 * time.Second,
    }
}

// StabilizationMetrics holds the key counters we monitor for stabilization.
type StabilizationMetrics struct {
    PktSentDataSuccess uint64
    PktRecvDataSuccess uint64
    PktSentACKSuccess  uint64
    PktRecvACKSuccess  uint64
    PktSentNAKSuccess  uint64
    PktRecvNAKSuccess  uint64
    PktRetransFromNAK  uint64
}

// GetStabilizationMetrics extracts stabilization-relevant metrics from a ConnectionMetrics.
func GetStabilizationMetrics(m *ConnectionMetrics) StabilizationMetrics {
    return StabilizationMetrics{
        PktSentDataSuccess: m.PktSentDataSuccess.Load(),
        PktRecvDataSuccess: m.PktRecvDataSuccess.Load(),
        PktSentACKSuccess:  m.PktSentACKSuccess.Load(),
        PktRecvACKSuccess:  m.PktRecvACKSuccess.Load(),
        PktSentNAKSuccess:  m.PktSentNAKSuccess.Load(),
        PktRecvNAKSuccess:  m.PktRecvNAKSuccess.Load(),
        PktRetransFromNAK:  m.PktRetransFromNAK.Load(),
    }
}

// Equal returns true if two StabilizationMetrics are identical.
func (s StabilizationMetrics) Equal(other StabilizationMetrics) bool {
    return s.PktSentDataSuccess == other.PktSentDataSuccess &&
           s.PktRecvDataSuccess == other.PktRecvDataSuccess &&
           s.PktSentACKSuccess == other.PktSentACKSuccess &&
           s.PktRecvACKSuccess == other.PktRecvACKSuccess &&
           s.PktSentNAKSuccess == other.PktSentNAKSuccess &&
           s.PktRecvNAKSuccess == other.PktRecvNAKSuccess &&
           s.PktRetransFromNAK == other.PktRetransFromNAK
}

// MetricsGetter is a function that returns current stabilization metrics.
// Used to abstract over different ways of getting metrics (local vs HTTP).
type MetricsGetter func() (StabilizationMetrics, error)

// WaitForStabilization waits until metrics stop changing.
// Returns the final metrics and elapsed time.
// Returns error if context cancelled or max wait exceeded.
func WaitForStabilization(ctx context.Context, cfg StabilizationConfig, getters ...MetricsGetter) (time.Duration, error) {
    if cfg.PollInterval == 0 {
        cfg = DefaultStabilizationConfig()
    }

    start := time.Now()
    deadline := start.Add(cfg.MaxWait)

    ticker := time.NewTicker(cfg.PollInterval)
    defer ticker.Stop()

    // Get initial snapshot
    var prevSnapshots []StabilizationMetrics
    for _, getter := range getters {
        m, err := getter()
        if err != nil {
            return 0, err
        }
        prevSnapshots = append(prevSnapshots, m)
    }

    stableCount := 0

    for {
        select {
        case <-ctx.Done():
            return time.Since(start), ctx.Err()

        case <-ticker.C:
            if time.Now().After(deadline) {
                return time.Since(start), fmt.Errorf("stabilization timeout after %v", cfg.MaxWait)
            }

            // Get current snapshots
            allStable := true
            for i, getter := range getters {
                current, err := getter()
                if err != nil {
                    return time.Since(start), err
                }

                if !current.Equal(prevSnapshots[i]) {
                    allStable = false
                    prevSnapshots[i] = current
                }
            }

            if allStable {
                stableCount++
                if stableCount >= cfg.StableCount {
                    return time.Since(start), nil // SUCCESS!
                }
            } else {
                stableCount = 0
            }
        }
    }
}
```

### 5. Integration Test Metrics Client Update

**File**: `contrib/integration_testing/metrics_collector.go`

Add method to get stabilization metrics from Prometheus endpoints:

```go
// GetStabilizationMetrics fetches metrics from UDS endpoint and extracts stabilization metrics.
func (c *MetricsClient) GetStabilizationMetrics(udsPath string) (metrics.StabilizationMetrics, error) {
    snapshot, err := c.FetchUDS(udsPath)
    if err != nil {
        return metrics.StabilizationMetrics{}, err
    }

    return metrics.StabilizationMetrics{
        PktSentDataSuccess: getCounterValue(snapshot, "gosrt_connection_packets_sent_total", "type", "data"),
        PktRecvDataSuccess: getCounterValue(snapshot, "gosrt_connection_packets_received_total", "type", "data"),
        PktSentACKSuccess:  getCounterValue(snapshot, "gosrt_connection_packets_sent_total", "type", "ack"),
        PktRecvACKSuccess:  getCounterValue(snapshot, "gosrt_connection_packets_received_total", "type", "ack"),
        PktSentNAKSuccess:  getCounterValue(snapshot, "gosrt_connection_packets_sent_total", "type", "nak"),
        PktRecvNAKSuccess:  getCounterValue(snapshot, "gosrt_connection_packets_received_total", "type", "nak"),
        PktRetransFromNAK:  getCounterValue(snapshot, "gosrt_connection_retransmissions_from_nak_total"),
    }, nil
}
```

### 6. Test Configuration

**File**: `contrib/integration_testing/config.go`

| Change | Description |
|--------|-------------|
| Remove fixed `QuiesceDrainTime` | Use dynamic stabilization instead |
| Add `StabilizationConfig` | Optional override for stabilization parameters |

### 5. Documentation Updates

**Files to update after implementation**:

| File | Section to Update |
|------|-------------------|
| `documentation/integration_testing_design.md` | Phase 1 shutdown sequence, add Quiesce description |
| `documentation/integration_testing_with_network_impairment_defects.md` | Mark Defects 3, 4 as resolved |
| `documentation/test_1.1_detailed_design.md` | Update shutdown sequence diagram |

#### integration_testing_design.md Updates

Add a new section "### Quiesce Before Shutdown" that explains:

```markdown
### Quiesce Before Shutdown

All integration tests use a "quiesce before shutdown" pattern to ensure accurate metrics:

1. **Pause Data Source**: Send SIGUSR1 to client-generator to stop data generation
2. **Wait for Drain**: Wait TsbPdDelay + 2 seconds for pipeline to empty
3. **Collect Metrics**: Gather metrics while connections are still up
4. **Shutdown**: Send SIGINT to all components

This ensures:
- Counters are stable (not changing during collection)
- Connections are still registered (metrics accessible)
- No shutdown timing artifacts in counter values
```

Update the "Phase 1: Clean Network Tests" section to show:
- Old shutdown order: SIGINT → SIGINT → SIGINT → Collect
- New shutdown order: SIGUSR1 → Wait → Collect → SIGINT → SIGINT → SIGINT

Update the "Phase 3: Network Impairment Tests" section with same pattern.

---

## Implementation Checklist

### Phase 0: Proactive Keepalive (SRT Core)

- [ ] Add `sendProactiveKeepalive()` method to `connection.go`
- [ ] Add keepalive ticker to `watchPeerIdleTimeout()` at 75% of `PeerIdleTimeout`
- [ ] Track last keepalive time to avoid sending too frequently
- [ ] Add logging for proactive keepalive sends
- [ ] Test: Verify connection stays alive when idle for > PeerIdleTimeout

### Phase 1: Client-Generator Changes

- [ ] Add `syscall.SIGUSR1` import
- [ ] Add `paused atomic.Bool` package-level variable
- [ ] Add SIGUSR1 signal handler goroutine
- [ ] Add pause check in data generation loop
- [ ] Add log message on pause
- [ ] Test manually: `kill -SIGUSR1 <pid>` pauses data

### Phase 1.5: Metrics Stabilization Helper

- [ ] Create `metrics/stabilization.go`
- [ ] Implement `StabilizationConfig` struct
- [ ] Implement `StabilizationMetrics` struct with `Equal()` method
- [ ] Implement `GetStabilizationMetrics()` for local `ConnectionMetrics`
- [ ] Implement `WaitForStabilization()` with polling loop
- [ ] Add unit tests for stabilization logic

### Phase 2: Integration Test Metrics Client

- [ ] Add `GetStabilizationMetrics()` to `metrics_collector.go`
- [ ] Parse Prometheus output to extract stabilization counters
- [ ] Create `MetricsGetter` functions for each component (server, client-gen, client)

### Phase 3: Test Orchestrator Changes

- [ ] Add `syscall.SIGUSR1` to `test_graceful_shutdown.go`
- [ ] Add quiesce phase with dynamic stabilization:
  - [ ] Send SIGUSR1 to client-generator
  - [ ] Call `WaitForStabilization()` with all component getters
  - [ ] Collect metrics after stabilization
- [ ] Move metrics collection to after stabilization, before shutdown
- [ ] Update `test_network_mode.go` with same pattern
- [ ] Add helper function `quiesceAndWaitForStabilization()`

### Phase 3: Verification

- [ ] Run clean network test: `make test-integration`
- [ ] Verify `ClientGen.PktSent == Server.PktRecv`
- [ ] Run 2% loss test: `sudo make test-network CONFIG=Network-Loss2pct-5Mbps`
- [ ] Verify loss count matches netem configuration
- [ ] Run 5% loss test: `sudo make test-network CONFIG=Network-Loss5pct-5Mbps`
- [ ] Verify Defect 3 (recovery rate) is resolved
- [ ] Verify Defect 4 ("Lost: 0") is resolved

### Phase 4: Documentation

- [ ] Update `integration_testing_design.md` with Quiesce description
- [ ] Update Defects 3, 4 status to resolved
- [ ] Add Quiesce to test flow diagrams

---

## Detailed Code Changes

### Client-Generator Signal Handler

```go
// In contrib/client-generator/main.go

import (
    "sync/atomic"
    "syscall"
)

// Package-level pause flag
var paused atomic.Bool

func main() {
    // ... existing setup ...

    // Setup SIGUSR1 handler for quiesce
    pauseChan := make(chan os.Signal, 1)
    signal.Notify(pauseChan, syscall.SIGUSR1)
    go func() {
        <-pauseChan
        log.Println("PAUSE signal received - stopping data generation")
        paused.Store(true)
    }()

    // ... existing code ...

    // In the data generation loop:
    for {
        select {
        case <-ctx.Done():
            return
        default:
            if paused.Load() {
                // Paused - just sleep briefly to reduce CPU
                time.Sleep(100 * time.Millisecond)
                continue
            }
            // Normal data sending
            conn.Write(generatePacket())
        }
    }
}
```

### Test Orchestrator Quiesce Helper

```go
// In contrib/integration_testing/test_graceful_shutdown.go or common

import "syscall"

// quiesceDataSource sends SIGUSR1 to pause data generation and waits for drain
func quiesceDataSource(cmd *exec.Cmd, drainTime time.Duration) error {
    if cmd == nil || cmd.Process == nil {
        return fmt.Errorf("process not running")
    }

    // Send SIGUSR1 to pause data generation
    fmt.Println("Sending SIGUSR1 to client-generator (pausing data)...")
    if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
        return fmt.Errorf("failed to send SIGUSR1: %w", err)
    }

    // Wait for pipeline to drain
    fmt.Printf("Waiting %v for pipeline to drain...\n", drainTime)
    time.Sleep(drainTime)

    return nil
}

// calculateDrainTime computes the drain time based on latency config
func calculateDrainTime(config *SRTConfig) time.Duration {
    if config == nil {
        return 3 * time.Second // Default
    }

    // Drain time = TsbPdDelay + safety margin
    latency := config.Latency
    if latency == 0 {
        latency = 120 * time.Millisecond // Default SRT latency
    }

    drainTime := latency + 2*time.Second

    // Cap at reasonable maximum
    if drainTime > 10*time.Second {
        drainTime = 10 * time.Second
    }

    return drainTime
}
```

### Updated Shutdown Sequence

```go
// In runTestWithMetrics() - after test duration completes:

// ========== PHASE 1: QUIESCE WITH DYNAMIC STABILIZATION ==========

fmt.Println("\nQuiescing data flow...")

// Step 1: Pause data generation
fmt.Println("Sending SIGUSR1 to client-generator (pause data)...")
if err := clientGenCmd.Process.Signal(syscall.SIGUSR1); err != nil {
    return false, testMetrics, startTime, time.Now()
}

// Step 2: Wait for metrics to stabilize (dynamic - usually < 1 second)
fmt.Println("Waiting for metrics to stabilize...")
stabilizationCfg := metrics.DefaultStabilizationConfig()

// Create getters for all components
getters := []metrics.MetricsGetter{
    func() (metrics.StabilizationMetrics, error) {
        return metricsClient.GetStabilizationMetrics(serverUDSPath)
    },
    func() (metrics.StabilizationMetrics, error) {
        return metricsClient.GetStabilizationMetrics(clientGenUDSPath)
    },
    func() (metrics.StabilizationMetrics, error) {
        return metricsClient.GetStabilizationMetrics(clientUDSPath)
    },
}

elapsed, err := metrics.WaitForStabilization(ctx, stabilizationCfg, getters...)
if err != nil {
    fmt.Fprintf(os.Stderr, "Warning: stabilization wait failed: %v (continuing anyway)\n", err)
} else {
    fmt.Printf("✓ Metrics stabilized in %v\n", elapsed)
}

// Step 3: Collect metrics while connections are still up and stable
fmt.Println("Collecting quiesced metrics...")
testMetrics.CollectAllMetrics("quiesced")

// ========== PHASE 2: SHUTDOWN ==========

fmt.Println("\nInitiating shutdown sequence...")

// Stop client first
fmt.Println("Sending SIGINT to client (subscriber)...")
clientCmd.Process.Signal(os.Interrupt)
// ... wait for exit ...

// Stop client-generator
fmt.Println("Sending SIGINT to client-generator (publisher)...")
clientGenCmd.Process.Signal(os.Interrupt)
// ... wait for exit ...

// Stop server last
fmt.Println("Sending SIGINT to server...")
serverCmd.Process.Signal(os.Interrupt)
// ... wait for exit ...
```

### Expected Test Output

```
Quiescing data flow...
Sending SIGUSR1 to client-generator (pause data)...
Waiting for metrics to stabilize...
✓ Metrics stabilized in 247ms

Collecting quiesced metrics...
Initiating shutdown sequence...
```

Compare to current fixed wait:
```
Waiting 3 seconds for pipeline to drain...  ← SLOW!
```

---

## Conclusion

The quiesce-before-shutdown approach addresses the fundamental timing issue in our test methodology. By stopping data generation and allowing the pipeline to drain before collecting metrics, we ensure:

1. **All sent packets are either delivered or definitively lost**
2. **All ACKs and NAKs have completed**
3. **Counters reflect the true state of the transfer**
4. **Connections remain up for metric collection**

This fix improves **both** clean network and network impairment tests:
- **Clean network**: Enables exact pipeline balance verification
- **Network impairment**: Provides accurate loss/recovery metrics

### Summary of Changes

| Component | File | Scope |
|-----------|------|-------|
| **SRT Core** | `connection.go` | ~40 lines (proactive keepalive in watchPeerIdleTimeout) |
| **SRT Core** | `config.go` | ~5 lines (KeepaliveInterval config) |
| **Metrics** | `metrics/stabilization.go` | ~100 lines (NEW - stabilization detection) |
| Client-Generator | `main.go` | ~20 lines (signal handler + pause check) |
| Integration Tests | `metrics_collector.go` | ~30 lines (GetStabilizationMetrics) |
| Test Orchestrator (Clean) | `test_graceful_shutdown.go` | ~40 lines (quiesce + stabilization) |
| Test Orchestrator (Network) | `test_network_mode.go` | ~40 lines (quiesce + stabilization) |
| Documentation | Multiple | Update flow diagrams |

**Total estimated effort**: 4-5 hours implementation + 1 hour testing

### Implementation Order

1. **Phase 0: Proactive Keepalive** - Ensures connections survive quiesce period
2. **Phase 1: Client-Generator SIGUSR1** - Enables pausing data generation
3. **Phase 1.5: Stabilization Helper** - Fast detection of when metrics settle
4. **Phase 2: Integration Test Metrics Client** - Bridge stabilization to test framework
5. **Phase 3: Test Orchestrators** - Adds quiesce + stabilization to shutdown
6. **Phase 4: Verification** - Run all tests to confirm improvements
7. **Phase 5: Documentation** - Update design docs

### Performance Impact

| Test Type | Current Wait | With Stabilization |
|-----------|--------------|-------------------|
| Clean network, 120ms | 3 seconds | ~300ms |
| Clean network, 3s latency | 8 seconds | ~1-2 seconds |
| 5% loss, 3s latency | 8 seconds | ~2-3 seconds |

**Estimated savings per test run**: 3-6 seconds average
**For 10 tests**: 30-60 seconds faster

---

## Appendix: Defects This Should Resolve

### Defect 3: 5% Loss Test Fails Recovery Rate Threshold

**Current Symptom**: Recovery rate 94.92% (threshold 95%)

**Root Cause Hypothesis**: The 0.08% shortfall may be shutdown timing artifacts (packets "lost" during shutdown, not to netem).

**Expected After Quiesce**: Recovery rate should reflect only netem-induced loss. If SRT ARQ is working correctly (which JSON stats show it is), the true recovery rate should be ~96-97%.

### Defect 4: Analysis Reports "Lost: 0" Despite Actual Loss

**Current Symptom**: Analysis shows "Lost: 0" but JSON shows `pkt_recv_loss: 1702`

**Root Cause Hypothesis**: The metrics are being collected at the wrong time:
1. Either while counters are still changing (pre-shutdown)
2. Or after connections close (counters unregistered)

**Expected After Quiesce**: With metrics collected during stable quiesced state:
- `CongestionRecvPktLoss` will have its final, stable value
- Analysis will correctly read 1702 (or similar) losses
- Statistical validation will work correctly

### Relationship to Other Defects

| Defect | Status | Quiesce Impact |
|--------|--------|----------------|
| Defect 1 (0% loss reported) | ✅ Fixed | N/A - was Prometheus export issue |
| Defect 2 (NAK counters) | ✅ Fixed | N/A - was counter increment issue |
| Defect 3 (Recovery rate) | 🔄 May be resolved | Removes shutdown artifacts from calculation |
| Defect 4 ("Lost: 0") | 🔄 May be resolved | Ensures stable metrics at collection time |

---

## Appendix: SRT Behavior During Quiesce

### What Packets Flow During Quiesce?

After SIGUSR1 (data generation paused):

| Time | Sender (Client-Gen) | Server | Receiver (Client) |
|------|---------------------|--------|-------------------|
| T+0 | Stops pushing data | Has buffered data | Has buffered data |
| T+100ms | Idle | Sending remaining data | Receiving, sending ACKs |
| T+500ms | Receives final ACKs | Buffer draining | Buffer draining |
| T+1s | Fully quiesced | Nearly empty | Delivering to output |
| T+TsbPdDelay | All ACKs complete | All data sent | All data delivered |

### ⚠️ Potential Issue: PeerIdleTimeout During Quiesce

**Problem**: If the quiesce drain time exceeds `PeerIdleTimeout`, the connection will be closed due to idle timeout.

**Current Behavior**:
- `watchPeerIdleTimeout()` monitors `getTotalReceivedPackets()`
- If no packets received for `PeerIdleTimeout`, connection closes
- Keepalives are only sent **reactively** (in response to received keepalive)

**Example Risk**:
```
PeerIdleTimeout: 5 seconds
Drain Time:      8 seconds (3s latency + 5s margin)
Result:          Connection closes at T+5s during drain!
```

### Solution: Proactive Keepalive

Add proactive keepalive sending when idle time reaches a threshold.

**Design**:
1. Add config option: `KeepaliveInterval` (time.Duration, 0 = disabled)
2. Alternative: Use percentage of `PeerIdleTimeout` (e.g., 75%)
3. When idle exceeds threshold, send keepalive proactively
4. Peer responds with keepalive, resetting both timers

**Implementation Approach** (Go-idiomatic, minimal impact):

```go
// In config.go - add to Config struct:
type Config struct {
    // ...existing fields...

    // KeepaliveInterval is the interval for sending proactive keepalive packets.
    // If zero, proactive keepalives are disabled (only reactive keepalives are sent).
    // Recommended: Set to PeerIdleTimeout * 3/4 for connections that may go idle.
    // SRTO_KEEPALIVE (proposed extension)
    KeepaliveInterval time.Duration
}

// In DefaultConfig:
var DefaultConfig = Config{
    // ...existing...
    KeepaliveInterval: 0, // Disabled by default (backward compatible)
}
```

```go
// In connection.go - modify watchPeerIdleTimeout:
func (c *srtConn) watchPeerIdleTimeout() {
    initialCount := c.getTotalReceivedPackets()

    // Existing ticker for timeout checking
    tickerInterval := c.config.PeerIdleTimeout / 2
    if c.config.PeerIdleTimeout > 6*time.Second {
        tickerInterval = c.config.PeerIdleTimeout / 4
    }
    ticker := time.NewTicker(tickerInterval)
    defer ticker.Stop()

    // NEW: Keepalive ticker (if enabled)
    var keepaliveTicker *time.Ticker
    var keepaliveChan <-chan time.Time
    if c.config.KeepaliveInterval > 0 {
        keepaliveTicker = time.NewTicker(c.config.KeepaliveInterval)
        keepaliveChan = keepaliveTicker.C
        defer keepaliveTicker.Stop()
    }

    for {
        select {
        case <-c.peerIdleTimeout.C:
            // ... existing timeout handling ...

        case <-ticker.C:
            // ... existing periodic check ...

        case <-keepaliveChan:
            // NEW: Proactive keepalive
            currentCount := c.getTotalReceivedPackets()
            if currentCount == initialCount {
                // No packets received recently - send keepalive to keep connection alive
                c.sendProactiveKeepalive()
            }

        case <-c.ctx.Done():
            return
        }

        // ... existing counter check logic ...
    }
}

// NEW: Send a proactive keepalive packet
func (c *srtConn) sendProactiveKeepalive() {
    p := packet.NewPacket(c.remoteAddr)
    p.Header().IsControlPacket = true
    p.Header().ControlType = packet.CTRLTYPE_KEEPALIVE
    p.Header().TypeSpecific = 0
    p.Header().Timestamp = c.getTimestamp()
    p.Header().DestinationSocketId = c.peerSocketId

    c.log("control:send:keepalive:proactive", func() string {
        return "sending proactive keepalive to maintain connection"
    })

    c.pop(p)
}
```

**Automatic Threshold (Opt-Out)**

Derive keepalive interval automatically at 75% of PeerIdleTimeout:

```go
// In config.go - add to Config struct:
type Config struct {
    // ...existing fields...

    // KeepaliveInterval is the interval for sending proactive keepalive packets.
    // Default: 75% of PeerIdleTimeout (auto-calculated if zero)
    // Set to a negative value to disable proactive keepalives.
    KeepaliveInterval time.Duration
}

// In DefaultConfig:
var DefaultConfig = Config{
    // ...existing...
    KeepaliveInterval: 0, // 0 = auto-calculate from PeerIdleTimeout
}
```

```go
// In connection.go - calculate effective interval:
func (c *srtConn) getKeepaliveInterval() time.Duration {
    if c.config.KeepaliveInterval < 0 {
        return 0 // Disabled explicitly
    }
    if c.config.KeepaliveInterval > 0 {
        return c.config.KeepaliveInterval // User-specified
    }
    // Auto-calculate: 75% of PeerIdleTimeout
    return c.config.PeerIdleTimeout * 3 / 4
}
```

This is **opt-out** - proactive keepalives are enabled by default, making GoSRT more reliable out of the box. Users can disable with `-keepalive -1` if needed.

### Recommended Approach

**For the quiesce feature, use the automatic threshold approach**:

1. No new config required (minimal change)
2. Connections automatically stay alive when idle
3. Backward compatible (behavior is additive, not breaking)
4. Works for both test scenarios and production use

### Benefits Beyond Testing

This proactive keepalive feature is valuable for production use cases too:

| Scenario | Without Proactive Keepalive | With Proactive Keepalive |
|----------|---------------------------|--------------------------|
| **Intermittent streams** | Connection drops during gaps | Connection stays alive |
| **Low-bitrate audio** | May timeout between packets | Stays connected |
| **Pause/resume streams** | Must reconnect after pause | Seamless resume |
| **NAT traversal** | NAT mapping expires | Keepalives refresh NAT |

This makes the feature a general improvement to GoSRT, not just a test infrastructure fix.

**Implementation in `watchPeerIdleTimeout()`**:

```go
func (c *srtConn) watchPeerIdleTimeout() {
    initialCount := c.getTotalReceivedPackets()

    // Timeout check interval (existing logic)
    tickerInterval := c.config.PeerIdleTimeout / 2
    if c.config.PeerIdleTimeout > 6*time.Second {
        tickerInterval = c.config.PeerIdleTimeout / 4
    }
    ticker := time.NewTicker(tickerInterval)
    defer ticker.Stop()

    // Keepalive interval: 75% of PeerIdleTimeout (auto-calculated)
    keepaliveInterval := c.config.PeerIdleTimeout * 3 / 4
    keepaliveTicker := time.NewTicker(keepaliveInterval)
    defer keepaliveTicker.Stop()

    lastKeepaliveSent := time.Now()

    for {
        select {
        case <-c.peerIdleTimeout.C:
            // Existing timeout handling...

        case <-ticker.C:
            // Existing periodic check...

        case <-keepaliveTicker.C:
            // Proactive keepalive: only send if no recent activity
            currentCount := c.getTotalReceivedPackets()
            if currentCount == initialCount && time.Since(lastKeepaliveSent) >= keepaliveInterval {
                c.sendProactiveKeepalive()
                lastKeepaliveSent = time.Now()
            }

        case <-c.ctx.Done():
            return
        }

        // ... existing counter check logic ...
    }
}
```

### Why Not Just Wait Longer With SIGINT?

Waiting longer after SIGINT doesn't help because:
1. **Connection closes immediately on SIGINT** → Metrics unregistered
2. **Other end sees peer disconnect** → Triggers its own shutdown
3. **Race condition** → No amount of waiting fixes the fundamental issue

Quiesce keeps connections **alive but idle**, which is the key difference.

