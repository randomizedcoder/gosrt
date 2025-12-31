# Metrics Refactoring Analysis

**Date:** 2025-12-30
**Status:** ANALYSIS / DOCUMENTATION ONLY

## Current Architecture Overview

### Connection Structure (`connection.go:96-175`)

The `srtConn` struct contains rich connection metadata:

```go
type srtConn struct {
    socketId     uint32       // Unique connection ID (line 107)
    peerSocketId uint32       // Peer's socket ID (line 108)
    localAddr    net.Addr     // Local IP:port (line 100)
    remoteAddr   net.Addr     // Remote IP:port (line 101)
    config       Config       // Contains StreamId (line 110)
    metrics      *metrics.ConnectionMetrics  // Prometheus metrics (line 175)
    // ...
}
```

**Key methods:**
- `StreamId() string` (`connection.go:538`) - Returns `c.config.StreamId`
- `SocketId() uint32` - Returns `c.socketId`

### Metrics System (`metrics/`)

**Files:**
| File | Purpose | Lines |
|------|---------|-------|
| `metrics/metrics.go` | `ConnectionMetrics` struct with ~100 atomic counters | ~600 |
| `metrics/registry.go` | Global connection registry | ~99 |
| `metrics/handler.go` | Prometheus HTTP handler, writes metrics | ~1013 |
| `metrics/helpers.go` | Helper functions for incrementing metrics | ~330 |

**Current Registry (`metrics/registry.go:19-28`):**
```go
type MetricsRegistry struct {
    connections   map[uint32]*ConnectionMetrics  // socketId -> metrics
    instanceNames map[uint32]string              // socketId -> instance name
    mu            sync.RWMutex
}
```

**Current Registration (`connection.go:254-278`):**
```go
func createConnectionMetrics(localAddr net.Addr, socketId uint32, instanceName string) *metrics.ConnectionMetrics {
    m := &metrics.ConnectionMetrics{
        HandlePacketLockTiming: metrics.NewLockTimingMetrics(),
        // ...
    }
    metrics.RegisterConnection(socketId, m, instanceName)
    return m
}
```

### Current Prometheus Labels (`metrics/handler.go:42-46`)
```go
socketIdStr := fmt.Sprintf("0x%08x", socketId)
instanceName := instanceNames[socketId]

// Example metric output:
// gosrt_connection_packets_sent_total{socket_id="0xabc123",instance="baseline-server",direction="send"} 12345
```

**Missing labels that would enable connection identification:**
- `remote_addr` (available in `srtConn.remoteAddr`)
- `stream_id` (available via `srtConn.StreamId()`)
- `peer_type` (derivable from stream_id: "publish:" vs "subscribe:")
- `peer_socket_id` (available in `srtConn.peerSocketId`)

---

## Approach 1: Extend Registry with Connection Metadata

### Concept

Add additional metadata to the registry alongside `ConnectionMetrics`, enabling richer Prometheus labels without restructuring the metrics system.

### Required Changes

#### 1.1 Extend Registry Structure (`metrics/registry.go`)

```go
// NEW: ConnectionInfo holds metadata for a registered connection
type ConnectionInfo struct {
    Metrics      *ConnectionMetrics
    InstanceName string
    RemoteAddr   string    // NEW: e.g., "10.1.1.2:45678"
    StreamId     string    // NEW: e.g., "publish:/test-stream-baseline"
    PeerType     string    // NEW: "publisher" or "subscriber" (derived from StreamId)
    PeerSocketId uint32    // NEW: peer's socket ID
}

type MetricsRegistry struct {
    connections map[uint32]*ConnectionInfo  // Changed from *ConnectionMetrics
    mu          sync.RWMutex
}
```

**Files to modify:**
- `metrics/registry.go:19-28` - Change struct
- `metrics/registry.go:40-51` - Update `RegisterConnection()` signature
- `metrics/registry.go:77-98` - Update `GetConnections()` return type

#### 1.2 Update Registration Call Sites

**File: `connection.go:254-278`**
```go
func createConnectionMetrics(localAddr net.Addr, socketId uint32, instanceName string,
    remoteAddr net.Addr, streamId string, peerSocketId uint32) *metrics.ConnectionMetrics {

    m := &metrics.ConnectionMetrics{...}

    // Derive peer type from stream ID
    peerType := "unknown"
    if strings.HasPrefix(streamId, "publish:") {
        peerType = "publisher"
    } else if strings.HasPrefix(streamId, "subscribe:") {
        peerType = "subscriber"
    }

    metrics.RegisterConnection(socketId, m, instanceName,
        remoteAddr.String(), streamId, peerType, peerSocketId)
    return m
}
```

**Call sites to update:**
- `conn_request.go:489` - Listener accepting connection
- `dial_handshake.go:252` - Dialer establishing connection

#### 1.3 Update Prometheus Handler (`metrics/handler.go:33-46`)

```go
func MetricsHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // ...
        connections := GetConnections()  // Returns map[uint32]*ConnectionInfo

        for socketId, info := range connections {
            metrics := info.Metrics
            socketIdStr := fmt.Sprintf("0x%08x", socketId)

            // All labels now available:
            // socket_id, instance, remote_addr, stream_id, peer_type, peer_socket_id

            writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
                metrics.PktSentDataSuccess.Load(),
                "socket_id", socketIdStr,
                "instance", info.InstanceName,
                "peer_type", info.PeerType,          // NEW
                "stream_id", info.StreamId,          // NEW
                "remote_addr", info.RemoteAddr,      // NEW
                "direction", "send")
        }
    })
}
```

### Complexity Assessment

| Task | Files | Est. Changes | Effort |
|------|-------|--------------|--------|
| Extend `MetricsRegistry` | `registry.go` | ~50 lines | 1 hour |
| Update `RegisterConnection()` | `registry.go` | ~20 lines | 30 min |
| Update `createConnectionMetrics()` | `connection.go` | ~30 lines | 1 hour |
| Update call sites | `conn_request.go`, `dial_handshake.go` | ~20 lines | 1 hour |
| Update `handler.go` | `handler.go` | ~100 lines | 2 hours |
| Update tests | `*_test.go` | ~50 lines | 1 hour |
| **Total** | | | **~6-7 hours** |

### Pros
- Minimal structural change
- Backward compatible (existing label values unchanged)
- New labels are additive
- Easy to roll back

### Cons
- Adds complexity to registry
- Need to pass more parameters through call chain
- Doesn't address fundamental separation of per-connection vs global metrics

---

## Approach 2: Move Per-Connection Metrics to `srtConn`

### Concept

Move the `ConnectionMetrics` struct from `metrics/` package into `srtConn` as an embedded field, keeping only global/system-wide metrics in `metrics/metrics.go`.

### Current State

```
metrics/
├── metrics.go          <- ConnectionMetrics struct (~100 atomic counters)
├── registry.go         <- Global registry of connections
├── handler.go          <- Prometheus handler
├── helpers.go          <- Increment helpers
└── listener_metrics.go <- Listener-level metrics

connection.go
├── srtConn struct
│   └── metrics *metrics.ConnectionMetrics  <- Reference to external struct
```

### Proposed State

```
connection.go
├── srtConn struct
│   ├── // All connection-specific fields...
│   ├── connectionMetrics embedded struct  <- MOVED HERE
│   │   ├── PktRecvDataSuccess atomic.Uint64
│   │   ├── PktSentDataSuccess atomic.Uint64
│   │   └── // ~100 more counters...
│   └── // Already has: socketId, remoteAddr, config.StreamId, etc.

metrics/
├── metrics.go          <- SIMPLIFIED: Only global/listener metrics
├── registry.go         <- Changed: registry holds *srtConn pointers
├── handler.go          <- Changed: accesses metrics via srtConn
├── helpers.go          <- May need interface changes
└── listener_metrics.go <- Unchanged
```

### Required Changes

#### 2.1 Move `ConnectionMetrics` to `connection.go`

**File: `connection.go` (add after line 175)**
```go
type srtConn struct {
    // ... existing fields ...

    // Embedded connection metrics (moved from metrics/metrics.go)
    // All counters use atomic.Uint64 for lock-free, high-performance increments
    connMetrics struct {
        PktRecvSuccess        atomic.Uint64
        PktRecvNil            atomic.Uint64
        PktRecvControlUnknown atomic.Uint64
        // ... ~100 more counters ...
    }
}
```

**Impact:**
- `metrics/metrics.go:14-200` - Remove `ConnectionMetrics` struct
- `connection.go` - Add ~200 lines for embedded struct
- All files using `*metrics.ConnectionMetrics` - Change to interface or direct access

#### 2.2 Create Interface for Metrics Access

**File: `metrics/interface.go` (NEW)**
```go
package metrics

// ConnectionMetricsProvider allows access to connection metrics.
// Implemented by srtConn.
type ConnectionMetricsProvider interface {
    GetSocketId() uint32
    GetRemoteAddr() string
    GetStreamId() string
    GetInstanceName() string

    // Metrics accessors
    GetPktRecvSuccess() uint64
    GetPktSentDataSuccess() uint64
    // ... accessors for all metrics ...
}
```

#### 2.3 Update Registry to Hold Connection References

**File: `metrics/registry.go`**
```go
type MetricsRegistry struct {
    connections map[uint32]ConnectionMetricsProvider  // Interface instead of struct
    mu          sync.RWMutex
}

func RegisterConnection(socketId uint32, conn ConnectionMetricsProvider) {
    globalRegistry.mu.Lock()
    globalRegistry.connections[socketId] = conn
    globalRegistry.mu.Unlock()
}
```

#### 2.4 Update Handler to Use Interface

**File: `metrics/handler.go:33-46`**
```go
func MetricsHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        connections := GetConnections()  // Returns map[uint32]ConnectionMetricsProvider

        for socketId, conn := range connections {
            socketIdStr := fmt.Sprintf("0x%08x", socketId)

            // Rich labels directly from connection
            writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
                conn.GetPktSentDataSuccess(),
                "socket_id", socketIdStr,
                "instance", conn.GetInstanceName(),
                "peer_type", derivePeerType(conn.GetStreamId()),
                "stream_id", conn.GetStreamId(),
                "remote_addr", conn.GetRemoteAddr(),
                "direction", "send")
        }
    })
}
```

#### 2.5 Update Helper Functions

**File: `metrics/helpers.go`**
Current:
```go
func IncrementRecvMetrics(m *ConnectionMetrics, p packet.Packet, ...) {
    m.PktRecvSuccess.Add(1)
}
```

Change to interface-based or move to `srtConn` methods:
```go
// Option A: Interface-based
func IncrementRecvMetrics(m ConnectionMetricsProvider, p packet.Packet, ...) {
    // Need setter interface too
}

// Option B: Move to srtConn methods
func (c *srtConn) IncrementRecvMetrics(p packet.Packet, isDataPacket bool, success bool, dropReason DropReason) {
    c.connMetrics.PktRecvSuccess.Add(1)
    // ...
}
```

### Circular Import Consideration

**Problem:** Moving metrics into `connection.go` might create circular imports:
- `metrics/handler.go` needs to access `srtConn`
- `srtConn` is in main `gosrt` package
- `metrics` package cannot import `gosrt` package

**Solutions:**

1. **Interface approach** (recommended): Define interface in `metrics/`, implement in `gosrt`
2. **Separate package**: Create `gosrt/internal/connmetrics/` package
3. **Callback approach**: Handler receives metrics via callback/function

### Complexity Assessment

| Task | Files | Est. Changes | Effort |
|------|-------|--------------|--------|
| Create interface in `metrics/` | `interface.go` | ~150 lines | 2 hours |
| Move struct to `connection.go` | `connection.go` | ~200 lines | 2 hours |
| Update registry | `registry.go` | ~50 lines | 1 hour |
| Update handler | `handler.go` | ~150 lines | 3 hours |
| Update helpers | `helpers.go` | ~150 lines | 2 hours |
| Update all call sites | `*.go` | ~100 lines | 3 hours |
| Update all tests | `*_test.go` | ~200 lines | 3 hours |
| Handle circular imports | Multiple | ~50 lines | 2 hours |
| **Total** | | | **~18-20 hours** |

### Pros
- Cleaner architecture: metrics live with the data they measure
- Natural access to all connection metadata (no registry lookup)
- Single source of truth for connection state
- Better encapsulation
- Easier to reason about connection lifecycle

### Cons
- Significant refactoring effort (~3x Approach 1)
- Risk of introducing bugs during migration
- Circular import complexity
- Interface overhead (method calls vs direct field access)
- Changes many files across the codebase
- Harder to roll back

---

## Comparison Summary

| Criteria | Approach 1: Extend Registry | Approach 2: Move to srtConn |
|----------|----------------------------|----------------------------|
| **Effort** | ~6-7 hours | ~18-20 hours |
| **Risk** | Low | Medium-High |
| **Code Changes** | ~270 lines | ~1050 lines |
| **Files Affected** | ~8 files | ~20+ files |
| **Backward Compat** | Yes (additive) | No (breaking) |
| **Architectural Cleanliness** | Medium | High |
| **Future Maintainability** | Incremental improvement | Significant improvement |
| **Rollback Difficulty** | Easy | Hard |

---

## Recommendation

### For Immediate Problem (Connection Mapping)

**Use Approach 1: Extend Registry with Connection Metadata**

Rationale:
- Solves the immediate problem (identifying server socket types)
- Low risk, can be completed in 1-2 days
- Fully backward compatible
- Can be enhanced later if needed

### For Long-Term Architecture

**Consider Approach 2 as a future refactoring project**

Rationale:
- Current metrics architecture works but has grown organically
- Moving metrics to `srtConn` would be cleaner long-term
- Should be done as a dedicated refactoring sprint, not mixed with feature work
- Would benefit from comprehensive test coverage first

---

## File Reference Summary

### Files to Review

| File | Lines | Key Content |
|------|-------|-------------|
| `connection.go` | 96-175 | `srtConn` struct definition |
| `connection.go` | 228-252 | `srtConnConfig` struct |
| `connection.go` | 254-278 | `createConnectionMetrics()` function |
| `connection.go` | 538-540 | `StreamId()` method |
| `metrics/metrics.go` | 14-200 | `ConnectionMetrics` struct |
| `metrics/registry.go` | 19-28 | `MetricsRegistry` struct |
| `metrics/registry.go` | 40-51 | `RegisterConnection()` function |
| `metrics/registry.go` | 77-98 | `GetConnections()` function |
| `metrics/handler.go` | 13-50 | `MetricsHandler()` - label generation |
| `metrics/helpers.go` | 1-100 | `IncrementRecvMetrics()`, `IncrementSendMetrics()` |
| `conn_request.go` | 489-512 | Listener connection creation |
| `dial_handshake.go` | 252-276 | Dialer connection creation |

### Test Files to Update

| File | Purpose |
|------|---------|
| `metrics/handler_test.go` | Prometheus output format tests |
| `metrics/registry_test.go` | Registry behavior tests |
| `metrics/stabilization_test.go` | Stabilization endpoint tests |
| `connection_test.go` | Connection lifecycle tests |

