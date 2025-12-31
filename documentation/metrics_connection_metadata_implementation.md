# Metrics Connection Metadata Implementation

**Date:** 2025-12-30
**Status:** IN PROGRESS
**Parent:** `metrics_refactoring_analysis.md` (Approach 1)

## Objective

Add connection metadata (remoteAddr, streamId, peerType) to the metrics registry, enabling richer Prometheus labels for connection identification.

## Implementation Phases

| Phase | Task | Status |
|-------|------|--------|
| 1 | Create `ConnectionInfo` struct in registry | ⏳ |
| 2 | Update `RegisterConnection()` signature | ⏳ |
| 3 | Update `GetConnections()` return type | ⏳ |
| 4 | Update `createConnectionMetrics()` in connection.go | ⏳ |
| 5 | Update call sites (conn_request.go, dial_handshake.go) | ⏳ |
| 6 | Add new labels to handler.go | ⏳ |
| 7 | Update tests | ⏳ |

---

## Phase 1: Create ConnectionInfo Struct

**File:** `metrics/registry.go`

```go
// ConnectionInfo holds metadata for a registered connection.
// This enables richer Prometheus labels for connection identification.
type ConnectionInfo struct {
    Metrics      *ConnectionMetrics
    InstanceName string
    RemoteAddr   string // Remote IP:port (e.g., "10.1.1.2:45678")
    StreamId     string // Stream ID (e.g., "publish:/test-stream")
    PeerType     string // "publisher", "subscriber", or "unknown"
}
```

---

## Phase 2: Update RegisterConnection Signature

**File:** `metrics/registry.go`

Change from:
```go
func RegisterConnection(socketId uint32, metrics *ConnectionMetrics, instanceName string)
```

To:
```go
func RegisterConnection(socketId uint32, info *ConnectionInfo)
```

---

## Phase 3: Update GetConnections Return Type

**File:** `metrics/registry.go`

Change from:
```go
func GetConnections() (map[uint32]*ConnectionMetrics, []uint32, map[uint32]string)
```

To:
```go
func GetConnections() map[uint32]*ConnectionInfo
```

---

## Phase 4: Update createConnectionMetrics

**File:** `connection.go`

Add parameters for metadata and create `ConnectionInfo`:
```go
func createConnectionMetrics(localAddr net.Addr, socketId uint32, instanceName string,
    remoteAddr net.Addr, streamId string) *metrics.ConnectionMetrics {

    m := &metrics.ConnectionMetrics{...}

    peerType := derivePeerType(streamId)

    info := &metrics.ConnectionInfo{
        Metrics:      m,
        InstanceName: instanceName,
        RemoteAddr:   remoteAddr.String(),
        StreamId:     streamId,
        PeerType:     peerType,
    }

    metrics.RegisterConnection(socketId, info)
    return m
}

func derivePeerType(streamId string) string {
    if strings.HasPrefix(streamId, "publish:") {
        return "publisher"
    } else if strings.HasPrefix(streamId, "subscribe:") {
        return "subscriber"
    }
    return "unknown"
}
```

---

## Phase 5: Update Call Sites

**Files:** `conn_request.go`, `dial_handshake.go`

Pass additional parameters to `createConnectionMetrics()`.

---

## Phase 6: Add Labels to Handler

**File:** `metrics/handler.go`

Add `peer_type`, `stream_id`, and optionally `remote_addr` labels to all connection metrics.

---

## Phase 7: Update Tests

**Files:** `metrics/*_test.go`

Update all test cases that call `RegisterConnection()`.

---

## Progress Log

| Time | Action | Result |
|------|--------|--------|
| - | Starting Phase 1 | - |

