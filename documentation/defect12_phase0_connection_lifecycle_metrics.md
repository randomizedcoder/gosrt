# Phase 0: Connection Lifecycle Metrics

**Goal**: Add metrics to track connection establishment and closure, enabling detection of connection replacements during Starlink tests.

**Status**: 🟢 Implementation Complete
**Related**: `defect12_starlink_negative_metrics.md`

---

## Implementation Progress

| Step | Status | Notes |
|------|--------|-------|
| 1. Add counters to `listener_metrics.go` | ✅ Done | 7 new counters added |
| 2. Update `registry.go` | ✅ Done | `CloseReason` enum, counter increments |
| 3. Export in `handler.go` | ✅ Done | New metrics exported |
| 4. Update `connection.go` | ✅ Done | 20 call sites updated with reasons |
| 5. Add unit tests | ✅ Done | 3 new test functions |
| 6. Run metrics-audit | ✅ Done | All metrics aligned |
| 7. Update `analysis.go` | ✅ Done | `AnalyzeConnectionLifecycle()` added |
| 8. Add configurable expectations | ✅ Done | `ExpectedServerConnections`, etc. in `TestConfig` |

---

## Current State

### Existing Metrics

| Location | What it tracks |
|----------|----------------|
| `ListenerMetrics` | Listener-level events (handshake errors, lookup failures) |
| `ConnectionMetrics` | Per-connection stats (packets, ACKs, NAKs, etc.) |
| `RegisterConnection()` | Called when connection established (no counter) |

### Missing

- No counter for connections established
- No counter for connections closed
- No counter for close reasons (graceful vs timeout vs error)
- No way to detect connection replacement during tests

---

## Proposed Metrics

### 1. Listener-Level Counters (in `ListenerMetrics`)

```go
// === Connection Lifecycle Counters ===
// Track connection establishment and closure for debugging and testing.

// ConnectionsEstablished increments when a new SRT connection is fully
// established (after successful handshake). This is incremented in
// RegisterConnection() when metrics are registered.
ConnectionsEstablished atomic.Uint64

// ConnectionsClosed increments when a connection is closed.
// Tracked per close reason to identify issues (e.g., unexpected timeouts).
ConnectionsClosedGraceful      atomic.Uint64 // Normal shutdown (SIGINT, Close() called)
ConnectionsClosedPeerIdle      atomic.Uint64 // Peer idle timeout expired
ConnectionsClosedContextCancel atomic.Uint64 // Parent context cancelled
ConnectionsClosedError         atomic.Uint64 // Error during operation
```

### 2. Prometheus Export Format

```
# HELP gosrt_connections_established_total Total connections established
# TYPE gosrt_connections_established_total counter
gosrt_connections_established_total 5

# HELP gosrt_connections_closed_total Total connections closed
# TYPE gosrt_connections_closed_total counter
gosrt_connections_closed_total{reason="graceful"} 3
gosrt_connections_closed_total{reason="peer_idle_timeout"} 1
gosrt_connections_closed_total{reason="context_cancelled"} 0
gosrt_connections_closed_total{reason="error"} 1

# HELP gosrt_connections_active Current number of active connections
# TYPE gosrt_connections_active gauge
gosrt_connections_active 1
```

### 3. Active Connections Gauge

Already available via `len(globalRegistry.connections)`, just need to export it.

---

## Implementation Plan

### Step 1: Add Counters to `listener_metrics.go`

```go
// === Connection Lifecycle Counters ===
// Track connection establishment and closure for debugging and testing.

// ConnectionsActive is a gauge (can increase/decrease) tracking current
// active connections. Uses Int64 to support Add(-1) for decrements.
ConnectionsActive atomic.Int64

// ConnectionsEstablished increments when RegisterConnection() is called.
ConnectionsEstablished atomic.Uint64

// ConnectionsClosed tracks connections closed by reason.
ConnectionsClosedGraceful      atomic.Uint64
ConnectionsClosedPeerIdle      atomic.Uint64
ConnectionsClosedContextCancel atomic.Uint64
ConnectionsClosedError         atomic.Uint64
```

### Step 2: Increment in `registry.go`

```go
func RegisterConnection(socketId uint32, metrics *ConnectionMetrics) {
    globalRegistry.mu.Lock()
    globalRegistry.connections[socketId] = metrics
    globalRegistry.mu.Unlock()  // Unlock immediately after map operation

    // Atomic operations are lock-free - no need to hold mutex
    globalListenerMetrics.ConnectionsActive.Add(1)
    globalListenerMetrics.ConnectionsEstablished.Add(1)
}

func UnregisterConnection(socketId uint32) {
    globalRegistry.mu.Lock()
    delete(globalRegistry.connections, socketId)
    globalRegistry.mu.Unlock()  // Unlock immediately after map operation

    // Atomic operation is lock-free
    globalListenerMetrics.ConnectionsActive.Add(-1)
}
```

### Step 3: Add Close Reason Tracking in `connection.go`

Define close reason enum:

```go
// CloseReason indicates why a connection was closed.
type CloseReason int

const (
    CloseReasonGraceful CloseReason = iota
    CloseReasonPeerIdleTimeout
    CloseReasonContextCancelled
    CloseReasonError
)
```

Update close path to track reason:

```go
// In monitorPeerIdleTimeout(), when timeout fires:
case <-c.peerIdleTimeout.C:
    if currentCount == initialCount {
        c.closeReason = CloseReasonPeerIdleTimeout  // NEW
        c.cancel()
        return
    }

// In close(), after cleanup:
func (c *srtConn) close() {
    // ... existing cleanup ...

    // NEW: Increment close counter based on reason
    lm := metrics.GetListenerMetrics()
    switch c.closeReason {
    case CloseReasonGraceful:
        lm.ConnectionsClosedGraceful.Add(1)
    case CloseReasonPeerIdleTimeout:
        lm.ConnectionsClosedPeerIdle.Add(1)
    case CloseReasonContextCancelled:
        lm.ConnectionsClosedContextCancel.Add(1)
    default:
        lm.ConnectionsClosedError.Add(1)
    }

    // Unregister from metrics registry
    metrics.UnregisterConnection(c.socketId)
}
```

### Step 4: Export in `handler.go`

```go
func writeListenerMetrics(b *strings.Builder) {
    lm := GetListenerMetrics()

    // ... existing counters ...

    // Connection lifecycle counters (lock-free atomic reads)
    writeCounter(b, "gosrt_connections_established_total",
        lm.ConnectionsEstablished.Load())

    writeCounterIfNonZero(b, "gosrt_connections_closed_total",
        lm.ConnectionsClosedGraceful.Load(),
        "reason", "graceful")
    writeCounterIfNonZero(b, "gosrt_connections_closed_total",
        lm.ConnectionsClosedPeerIdle.Load(),
        "reason", "peer_idle_timeout")
    writeCounterIfNonZero(b, "gosrt_connections_closed_total",
        lm.ConnectionsClosedContextCancel.Load(),
        "reason", "context_cancelled")
    writeCounterIfNonZero(b, "gosrt_connections_closed_total",
        lm.ConnectionsClosedError.Load(),
        "reason", "error")

    // Active connections gauge (lock-free atomic read)
    writeGauge(b, "gosrt_connections_active", float64(lm.ConnectionsActive.Load()))
}
```

### Step 5: Update `analysis.go` to Detect Connection Replacement

```go
// In ComputeDerivedMetrics or a new function:
func DetectConnectionLifecycleIssues(ts *TestMetricsTimeSeries) []string {
    issues := []string{}

    for _, component := range []MetricsTimeSeries{ts.Server, ts.ClientGenerator, ts.Client} {
        if len(component.Snapshots) < 2 {
            continue
        }

        first := component.Snapshots[0]
        last := component.Snapshots[len(component.Snapshots)-1]

        // Check for connection replacements
        establishedDelta := getMetric(last, "gosrt_connections_established_total") -
                           getMetric(first, "gosrt_connections_established_total")
        closedDelta := getMetric(last, "gosrt_connections_closed_total") -
                      getMetric(first, "gosrt_connections_closed_total")

        if establishedDelta > 1 {
            issues = append(issues, fmt.Sprintf(
                "%s: %d connections established during test (possible reconnects)",
                component.Component, int(establishedDelta)))
        }

        // Check for unexpected close reasons
        peerIdleDelta := getMetricWithLabel(last, "gosrt_connections_closed_total", "reason=\"peer_idle_timeout\"") -
                        getMetricWithLabel(first, "gosrt_connections_closed_total", "reason=\"peer_idle_timeout\"")
        if peerIdleDelta > 0 {
            issues = append(issues, fmt.Sprintf(
                "%s: %d connections closed due to peer idle timeout",
                component.Component, int(peerIdleDelta)))
        }
    }

    return issues
}
```

---

## Integration Test Expectations

### Clean Network Tests (no impairment)

```
gosrt_connections_established_total = 2  (one per endpoint pair)
gosrt_connections_closed_total{reason="graceful"} = 2
gosrt_connections_closed_total{reason="peer_idle_timeout"} = 0
gosrt_connections_active = 0  (at end of test)
```

### Starlink Test (with proper PeerIdleTimeout)

If `PeerIdleTimeout` is set high enough (e.g., 30s), connections should survive 60ms outages:

```
gosrt_connections_established_total = 2
gosrt_connections_closed_total{reason="graceful"} = 2
gosrt_connections_closed_total{reason="peer_idle_timeout"} = 0
```

### Starlink Test (with low PeerIdleTimeout)

If `PeerIdleTimeout` is too low, we'd see connection replacements:

```
gosrt_connections_established_total = 8  (multiple reconnects!)
gosrt_connections_closed_total{reason="peer_idle_timeout"} = 6
```

---

## Validation Steps

1. Run `tools/metrics-audit/main.go` to verify:
   - All counters are defined in `listener_metrics.go`
   - All counters are incremented exactly once
   - All counters are exported in `handler.go`

2. Add unit tests in `listener_metrics_test.go`

3. Run integration tests and verify:
   - Clean network: established = closed = 2 per test
   - Starlink: No unexpected closes (with proper `PeerIdleTimeout`)

---

## Files to Modify

| File | Changes |
|------|---------|
| `metrics/listener_metrics.go` | Add 5 new counters |
| `metrics/registry.go` | Add `UnregisterConnection()`, increment established counter |
| `metrics/handler.go` | Export new counters + active gauge |
| `connection.go` | Add `closeReason`, increment close counters |
| `contrib/integration_testing/analysis.go` | Add lifecycle issue detection |
| `tools/metrics-audit/main.go` | Update to check new counters |

---

## Starlink Test Fix

After implementing Phase 0, if we see `peer_idle_timeout` closes during Starlink:

**Root Cause**: `LargeBuffersSRTConfig` has default `PeerIdleTimeout` (2 seconds), which may be too short.

**Fix**: Create `StarlinkSRTConfig` with explicit 30-second `PeerIdleTimeout`:

```go
StarlinkSRTConfig = SRTConfig{
    ConnectionTimeout: 3000 * time.Millisecond,
    PeerIdleTimeout:   30000 * time.Millisecond,  // 30s - survive 60ms outages
    Latency:           3000 * time.Millisecond,
    RecvLatency:       3000 * time.Millisecond,
    PeerLatency:       3000 * time.Millisecond,
    TLPktDrop:         true,
}
```

Update test config:
```go
{
    Name:        "Network-Starlink-5Mbps",
    SharedSRT:   &StarlinkSRTConfig,  // Was &LargeBuffersSRTConfig
    // ...
}
```

