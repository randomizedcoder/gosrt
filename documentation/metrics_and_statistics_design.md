# Metrics and Statistics Design

## Overview

This document provides a comprehensive design for a unified metrics and statistics system for the GoSRT library. It reviews the current implementation, identifies gaps, and proposes a holistic approach to metrics collection with Prometheus integration and lock timing measurements.

## Table of Contents

1. [Current Statistics Implementation Review](#current-statistics-implementation-review)
2. [Locking Strategy Analysis](#locking-strategy-analysis)
3. [Packet Processing Paths Analysis](#packet-processing-paths-analysis)
4. [Metrics Design Principles](#metrics-design-principles)
5. [Unified Metrics Architecture](#unified-metrics-architecture)
6. [Prometheus Integration](#prometheus-integration)
7. [Lock Timing Metrics](#lock-timing-metrics)
8. [Detailed Migration Guide: `connStats` to Atomic Counters](#detailed-migration-guide-connstats-to-atomic-counters)
9. [Implementation Plan](#implementation-plan)

---

## Current Statistics Implementation Review

### Current Statistics Structure

**Location**: `connection.go:136-154` (`connStats` struct)

```go
type connStats struct {
    headerSize        uint64
    pktSentACK        uint64
    pktRecvACK        uint64
    pktSentACKACK     uint64
    pktRecvACKACK     uint64
    pktSentNAK        uint64
    pktRecvNAK        uint64
    pktRetransFromNAK uint64
    pktSentKM         uint64
    pktRecvKM         uint64
    pktRecvUndecrypt  uint64
    byteRecvUndecrypt uint64
    pktRecvInvalid    uint64
    pktSentKeepalive  uint64
    pktRecvKeepalive  uint64
    pktSentShutdown   uint64
    pktRecvShutdown   uint64
    mbpsLinkCapacity  float64
}
```

### Current Locking Strategy

**Location**: `connection.go:232`

```go
statisticsLock sync.RWMutex
```

**Current Usage Pattern**:
- **Write operations**: `Lock()` / `Unlock()` for increments
- **Read operations**: `RLock()` / `RUnlock()` in `Stats()` method
- **Frequency**: High (every packet send/receive increments counters)

**Example Increment Pattern**:
```go
c.statisticsLock.Lock()
c.statistics.pktRecvACK++
c.statisticsLock.Unlock()
```

**Example Read Pattern**:
```go
c.statisticsLock.RLock()
defer c.statisticsLock.RUnlock()
// Read all statistics
```

### Current Statistics Sources

1. **Connection-level statistics** (`connStats`):
   - Control packet counts (ACK, NAK, ACKACK, KM, Keepalive, Shutdown)
   - Error counts (undecrypt, invalid)
   - Extended statistics (retrans from NAK)

2. **Congestion control statistics** (`congestion/live/receive.go`, `congestion/live/send.go`):
   - Packet/byte counts (sent, received, unique, retrans, loss, drop, belated)
   - Buffer statistics (packets, bytes, flight size)
   - Rate calculations (packets/sec, bytes/sec, loss rate)
   - Link capacity estimates

3. **Statistics aggregation** (`connection.go:Stats()`):
   - Combines connection-level and congestion control statistics
   - Calculates instantaneous rates
   - Formats for external consumption

### Current Limitations

1. **Inconsistent locking**: Mix of `sync.RWMutex` (connection) and `sync.Mutex` (congestion control)
2. **No visibility into packet drops**: Many drop points have no counters
3. **No lock timing**: No measurement of lock contention or hold times
4. **No Prometheus integration**: Statistics only available via `Stats()` method
5. **Missing negative cases**: Only positive cases counted (e.g., `pktRecvACK` but no `pktRecvACKDropped`)
6. **No per-path visibility**: Can't distinguish io_uring vs ReadFrom() paths

---

## Locking Strategy Analysis

### Current Locking Patterns

#### 1. Connection Statistics (`statisticsLock sync.RWMutex`)

**Write Operations** (High Frequency):
- Increment counters on every packet send/receive
- Pattern: `Lock()` → increment → `Unlock()`
- **Contention**: High (every packet operation)

**Read Operations** (Low Frequency):
- Periodic statistics collection (every 10s)
- Pattern: `RLock()` → read all → `RUnlock()`
- **Contention**: Low (infrequent reads)

**Analysis**:
- ✅ **Good**: RWMutex allows concurrent reads
- ⚠️ **Issue**: Write operations are frequent and require exclusive lock
- ⚠️ **Issue**: No measurement of lock contention

#### 2. Congestion Control (`receiver.lock sync.RWMutex`, `sender.lock sync.Mutex`)

**Receiver** (`sync.RWMutex`):
- Read operations: `periodicACK()`, `periodicNAK()` (read locks)
- Write operations: `Push()`, `Tick()` (write locks)
- **Contention**: Moderate (read-heavy, write-occasional)

**Sender** (`sync.Mutex`):
- All operations require exclusive lock
- **Contention**: Low (single-threaded per connection)

**Analysis**:
- ✅ **Good**: Receiver uses RWMutex for read-heavy workload
- ⚠️ **Issue**: No measurement of lock hold times

#### 3. Per-Connection Packet Processing (`handlePacketMutex sync.Mutex`)

**Usage**:
- Serializes `handlePacket()` calls per connection
- **Contention**: Low (per-connection, fast operations)
- **Critical**: If this lock is held indefinitely, connection stops processing

**Analysis**:
- ✅ **Good**: Per-connection mutex minimizes contention
- ⚠️ **Issue**: No visibility into lock hold times or stuck locks

### Locking Strategy Recommendations

1. **Atomic Counters for High-Frequency Operations**:
   - Use `atomic.Uint64` for packet counters (ACK, NAK, etc.)
   - Eliminates lock contention for increments
   - Maintains `sync.RWMutex` for complex reads (if needed)

2. **Lock Timing for Critical Mutexes**:
   - Measure hold times for `handlePacketMutex`
   - Measure contention for `receiver.lock` and `sender.lock`
   - Track max hold times for debugging

3. **Hybrid Approach**:
   - Atomic counters for simple increments
   - Mutex for complex state reads (if needed)
   - Lock timing for all critical mutexes

---

## Packet Processing Paths Analysis

### Receive Path (Input)

#### Path 1: io_uring Receive Path (Primary)

**Flow**:
1. `listen_linux.go:recvCompletionHandler()` - io_uring completion handler
2. `listen_linux.go:processRecvCompletion()` - Process completion
   - **Drop Point**: Receive error (`cqe.Res < 0`)
   - **Drop Point**: Empty datagram (`bytesReceived == 0`)
   - **Drop Point**: RSA extraction failure (`addr == nil`)
   - **Drop Point**: Packet deserialization failure (`err != nil`)
   - **Drop Point**: Unknown socket ID (`!ok`)
   - **Drop Point**: Nil connection (`conn == nil`)
   - **Drop Point**: Wrong peer address (`AllowPeerIpChange=false` and mismatch)
   - **Drop Point**: Backlog full (handshake packets)
3. `connection.go:handlePacketDirect()` - Direct call with mutex
4. `connection.go:handlePacket()` - Process packet
   - **Drop Point**: Nil packet (early return)
   - **Drop Point**: Unknown control type (logged, but no counter)
   - **Drop Point**: FEC filter packet (`MessageNumber == 0`)
5. `connection.go:recv.Push()` - Congestion control
   - **Drop Point**: Too old packet
   - **Drop Point**: Already acknowledged
   - **Drop Point**: Duplicate packet

**Current Counters**:
- ✅ `pktRecvACK`, `pktRecvNAK`, `pktRecvACKACK`, `pktRecvKM`, `pktRecvKeepalive`, `pktRecvShutdown`
- ✅ `pktRecvUndecrypt`, `pktRecvInvalid`
- ✅ Congestion control: `PktRecv`, `PktRecvDrop`, `PktRecvLoss`, `PktRecvRetrans`, `PktRecvBelated`

**Missing Counters**:
- ❌ `pktRecvIoUringError` (receive errors from io_uring)
- ❌ `pktRecvEmpty` (empty datagrams)
- ❌ `pktRecvParseError` (deserialization failures)
- ❌ `pktRecvUnknownSocketId` (unknown destination)
- ❌ `pktRecvNilConnection` (connection is nil)
- ❌ `pktRecvWrongPeer` (peer address mismatch)
- ❌ `pktRecvHandshakeBacklogFull` (backlog full)
- ❌ `pktRecvUnknownControlType` (unknown control packet type)
- ❌ `pktRecvFECFilter` (FEC filter packets dropped)

#### Path 2: ReadFrom() Fallback Path

**Flow**:
1. `listen.go:reader()` - ReadFrom() goroutine
2. `listen.go:reader()` - Route to connection
   - **Drop Point**: Unknown socket ID
   - **Drop Point**: Nil connection
   - **Drop Point**: Wrong peer address
3. `connection.go:push()` - Queue to networkQueue
   - **Drop Point**: Network queue full
4. `connection.go:networkQueueReader()` - Process from queue
5. `connection.go:handlePacket()` - Process packet (same as Path 1)

**Current Counters**:
- ✅ Same as Path 1 (congestion control counters)

**Missing Counters**:
- ❌ `pktRecvReadFromError` (ReadFrom() errors)
- ❌ `pktRecvNetworkQueueFull` (queue full drops)
- ❌ Same missing counters as Path 1

### Send Path (Output)

#### Path 1: io_uring Send Path (Primary)

**Flow**:
1. `connection.go:sendACK()`, `sendNAK()`, etc. - Create control packet
2. `connection.go:pop()` - Prepare packet for send
3. `connection_linux.go:sendIoUring()` - Submit to io_uring
   - **Drop Point**: Ring full (can't submit)
   - **Drop Point**: Marshalling failure
   - **Drop Point**: Submit failure
4. `connection_linux.go:sendCompletionHandler()` - Process completion
   - **Drop Point**: Send error (`cqe.Res < 0`)

**Current Counters**:
- ✅ `pktSentACK`, `pktSentNAK`, `pktSentACKACK`, `pktSentKM`, `pktSentKeepalive`, `pktSentShutdown`
- ✅ Congestion control: `PktSent`, `PktRetrans`, `PktSendDrop`, `PktSendLoss`

**Missing Counters**:
- ❌ `pktSentIoUringRingFull` (ring full, can't submit)
- ❌ `pktSentIoUringMarshalError` (marshalling failure)
- ❌ `pktSentIoUringSubmitError` (submit failure)
- ❌ `pktSentIoUringError` (send error from completion)

#### Path 2: WriteTo() Fallback Path

**Flow**:
1. `connection.go:sendACK()`, `sendNAK()`, etc. - Create control packet
2. `connection.go:pop()` - Prepare packet for send
3. `listen.go:send()` or `dial.go:send()` - WriteTo() fallback
   - **Drop Point**: Marshalling failure
   - **Drop Point**: WriteTo() error

**Current Counters**:
- ✅ Same as Path 1

**Missing Counters**:
- ❌ `pktSentWriteToMarshalError` (marshalling failure)
- ❌ `pktSentWriteToError` (WriteTo() error)

### Summary: Missing Counters

**Receive Path**:
- Error conditions: io_uring errors, parse errors, empty datagrams
- Routing failures: unknown socket ID, nil connection, wrong peer, backlog full
- Filtering: FEC filter packets, unknown control types

**Send Path**:
- Error conditions: io_uring errors, marshalling errors, submit errors
- Resource exhaustion: ring full, queue full

**Both Paths**:
- Path identification: io_uring vs fallback
- Lock timing: hold times, contention

---

## Metrics Design Principles

### 1. Positive and Negative Cases

**Principle**: For every positive case (packet received), track corresponding negative cases (packet dropped).

**Examples**:
- `pktRecvACK` + `pktRecvACKDropped` = total ACK packets seen
- `pktSentACK` + `pktSentACKDropped` = total ACK packets attempted
- `pktRecvData` + `pktRecvDataDropped` = total data packets seen

**Benefits**:
- Complete visibility into packet flow
- Can calculate drop rates
- Identifies where packets are being lost

### 2. Path Identification

**Principle**: Distinguish between io_uring and fallback paths.

**Examples**:
- `pktRecvIoUring` vs `pktRecvReadFrom`
- `pktSentIoUring` vs `pktSentWriteTo`

**Benefits**:
- Understand which path is being used
- Performance comparison between paths
- Debugging path-specific issues

### 3. Error Classification

**Principle**: Classify errors by type and location.

**Examples**:
- `pktRecvError` (generic) → `pktRecvErrorIoUring`, `pktRecvErrorParse`, `pktRecvErrorRoute`
- `pktSentError` (generic) → `pktSentErrorIoUring`, `pktSentErrorMarshal`, `pktSentErrorSubmit`

**Benefits**:
- Identify specific failure modes
- Prioritize fixes based on error frequency
- Track error trends over time

### 4. Consistent Naming

**Principle**: Use consistent naming conventions.

**Format**: `{direction}{type}{action}[{qualifier}]`

- **Direction**: `pktRecv`, `pktSent`, `byteRecv`, `byteSent`
- **Type**: `Data`, `ACK`, `NAK`, `ACKACK`, `KM`, `Keepalive`, `Shutdown`, `Handshake`
- **Action**: `Success`, `Dropped`, `Error`, `Retrans`
- **Qualifier**: `IoUring`, `ReadFrom`, `WriteTo`, `ParseError`, `RingFull`, etc.

**Examples**:
- `pktRecvACKSuccess` (ACK received and processed)
- `pktRecvACKDropped` (ACK received but dropped)
- `pktRecvACKError` (ACK received but error processing)
- `pktSentACKSuccess` (ACK sent successfully)
- `pktSentACKDropped` (ACK attempted but dropped)
- `pktSentACKError` (ACK attempted but error)

### 5. Atomic Operations

**Principle**: Use atomic operations for high-frequency counters.

**Benefits**:
- No lock contention
- Better performance
- Thread-safe by design

**Trade-offs**:
- Slightly more complex code
- No atomic operations for complex types (use mutex for those)

---

## Unified Metrics Architecture

### Design Decision: Atomic Counters + Custom /metrics Handler

**Rationale**:
- **Avoid double collection**: Use atomic counters as the single source of truth
- **No external dependencies**: Avoid prometheus client library overhead
- **High performance**: Direct reads from atomic variables, custom string building
- **Deep integration**: Works with existing statistics system (can migrate gradually)

### Metrics Structure

```go
// metrics.go

// ConnectionMetrics holds all metrics for a single connection
// All counters use atomic.Uint64 for lock-free, high-performance increments
type ConnectionMetrics struct {
    // Packet counters (atomic for performance)
    PktRecvDataSuccess      atomic.Uint64
    PktRecvDataDropped      atomic.Uint64
    PktRecvDataError        atomic.Uint64
    PktSentDataSuccess      atomic.Uint64
    PktSentDataDropped      atomic.Uint64
    PktSentDataError        atomic.Uint64

    // Control packet counters
    PktRecvACKSuccess       atomic.Uint64
    PktRecvACKDropped       atomic.Uint64
    PktRecvACKError         atomic.Uint64
    PktSentACKSuccess       atomic.Uint64
    PktSentACKDropped       atomic.Uint64
    PktSentACKError         atomic.Uint64

    // ... (similar for NAK, ACKACK, KM, Keepalive, Shutdown, Handshake)

    // Path-specific counters
    PktRecvIoUring          atomic.Uint64
    PktRecvReadFrom         atomic.Uint64
    PktSentIoUring          atomic.Uint64
    PktSentWriteTo          atomic.Uint64

    // Error counters (detailed)
    PktRecvErrorIoUring     atomic.Uint64
    PktRecvErrorParse       atomic.Uint64
    PktRecvErrorRoute       atomic.Uint64
    PktRecvErrorEmpty       atomic.Uint64
    PktSentErrorIoUring     atomic.Uint64
    PktSentErrorMarshal     atomic.Uint64
    PktSentErrorSubmit      atomic.Uint64

    // Routing failure counters
    PktRecvUnknownSocketId  atomic.Uint64
    PktRecvNilConnection    atomic.Uint64
    PktRecvWrongPeer        atomic.Uint64
    PktRecvBacklogFull      atomic.Uint64

    // Resource exhaustion counters
    PktSentRingFull         atomic.Uint64
    PktRecvQueueFull        atomic.Uint64

    // Lock timing (see Lock Timing Metrics section)
    HandlePacketLockTiming  *LockTimingMetrics
    ReceiverLockTiming      *LockTimingMetrics
    SenderLockTiming        *LockTimingMetrics

    // Byte counters (for completeness)
    ByteRecvDataSuccess     atomic.Uint64
    ByteRecvDataDropped     atomic.Uint64
    ByteSentDataSuccess      atomic.Uint64
    ByteSentDataDropped      atomic.Uint64
    // ... (similar pattern for all packet types)
}
```

### Metrics Collection Pattern

**Increment Pattern** (Atomic):
```go
// Success case
metrics.PktRecvACKSuccess.Add(1)

// Drop case
metrics.PktRecvACKDropped.Add(1)

// Error case
metrics.PktRecvACKError.Add(1)
```

**Path Identification**:
```go
// In processRecvCompletion (io_uring path)
metrics.PktRecvIoUring.Add(1)
metrics.PktRecvACKSuccess.Add(1)

// In reader() (ReadFrom path)
metrics.PktRecvReadFrom.Add(1)
metrics.PktRecvACKSuccess.Add(1)
```

**Error Classification**:
```go
// In processRecvCompletion
if err := packet.NewPacketFromData(...); err != nil {
    metrics.PktRecvErrorParse.Add(1)
    return
}

// In sendIoUring
if ringFull {
    metrics.PktSentRingFull.Add(1)
    metrics.PktSentACKDropped.Add(1)
    return
}
```

### Migration Strategy

**Phase 1: Add New Metrics (Parallel)**
- Add `ConnectionMetrics` struct alongside existing `connStats`
- Increment both old and new counters during transition
- No breaking changes

**Phase 2: Update Statistics Method**
- Update `Stats()` to read from new metrics
- Maintain backward compatibility with existing `Statistics` struct

**Phase 3: Remove Old Counters**
- Remove `connStats` struct
- Remove `statisticsLock` (if all counters are atomic)
- Clean up old increment code

---

## Detailed Migration Guide: `connStats` to Atomic Counters

This section documents all locations in the codebase that need to be updated to migrate from `connStats` (with `statisticsLock`) to atomic counters in `ConnectionMetrics`.

### Current `connStats` Structure

**Location**: `connection.go:136-155`

```go
type connStats struct {
    headerSize        uint64
    pktSentACK        uint64
    pktRecvACK        uint64
    pktSentACKACK     uint64
    pktRecvACKACK     uint64
    pktSentNAK        uint64
    pktRecvNAK        uint64
    pktRetransFromNAK uint64
    pktSentKM         uint64
    pktRecvKM         uint64
    pktRecvUndecrypt  uint64
    byteRecvUndecrypt uint64
    pktRecvInvalid    uint64
    pktSentKeepalive  uint64
    pktRecvKeepalive  uint64
    pktSentShutdown   uint64
    pktRecvShutdown   uint64
    mbpsLinkCapacity  float64
}
```

**Current Usage**: `srtConn` struct contains:
- `statistics connStats` (line 231)
- `statisticsLock sync.RWMutex` (line 232)

### Migration Mapping: `connStats` → `ConnectionMetrics`

| `connStats` Field | `ConnectionMetrics` Field | Type | Notes |
|------------------|---------------------------|------|-------|
| `pktSentACK` | `PktSentACKSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvACK` | `PktRecvACKSuccess` | `atomic.Uint64` | Direct mapping |
| `pktSentACKACK` | `PktSentACKACKSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvACKACK` | `PktRecvACKACKSuccess` | `atomic.Uint64` | Direct mapping |
| `pktSentNAK` | `PktSentNAKSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvNAK` | `PktRecvNAKSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRetransFromNAK` | `PktRetransFromNAK` | `atomic.Uint64` | Direct mapping |
| `pktSentKM` | `PktSentKMSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvKM` | `PktRecvKMSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvUndecrypt` | `PktRecvUndecrypt` | `atomic.Uint64` | Direct mapping |
| `byteRecvUndecrypt` | `ByteRecvUndecrypt` | `atomic.Uint64` | Direct mapping |
| `pktRecvInvalid` | `PktRecvInvalid` | `atomic.Uint64` | Direct mapping |
| `pktSentKeepalive` | `PktSentKeepaliveSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvKeepalive` | `PktRecvKeepaliveSuccess` | `atomic.Uint64` | Direct mapping |
| `pktSentShutdown` | `PktSentShutdownSuccess` | `atomic.Uint64` | Direct mapping |
| `pktRecvShutdown` | `PktRecvShutdownSuccess` | `atomic.Uint64` | Direct mapping |
| `mbpsLinkCapacity` | `MbpsLinkCapacity` | `atomic.Uint64` | Store as uint64 (Mbps * 1000), convert on read |
| `headerSize` | `HeaderSize` | `atomic.Uint64` | Direct mapping |

### Locations Requiring Updates

#### 1. Statistics Increments (Remove Locks, Use Atomic)

**File**: `connection.go`

**1.1. Undecrypt Packet Handling** (Lines 869-878)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvUndecrypt++
c.statistics.byteRecvUndecrypt += p.Len()
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvUndecrypt.Add(1)
c.metrics.ByteRecvUndecrypt.Add(uint64(p.Len()))
```

**1.2. Keepalive Packets** (Lines 891-894)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvKeepalive++
c.statistics.pktSentKeepalive++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvKeepaliveSuccess.Add(1)
c.metrics.PktSentKeepaliveSuccess.Add(1)
```

**1.3. Shutdown Packet Received** (Lines 908-910)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvShutdown++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvShutdownSuccess.Add(1)
```

**1.4. ACK Packet Received** (Lines 923-925)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvACK++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvACKSuccess.Add(1)
```

**1.5. Invalid ACK Packet** (Lines 930-932)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvInvalid++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvInvalid.Add(1)
```

**1.6. Link Capacity Update** (Lines 946-948)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.mbpsLinkCapacity = float64(cif.EstimatedLinkCapacity) * MAX_PAYLOAD_SIZE * 8 / 1024 / 1024
c.statisticsLock.Unlock()

// AFTER:
// Store as uint64 (Mbps * 1000) for atomic operations
mbps := uint64(float64(cif.EstimatedLinkCapacity) * MAX_PAYLOAD_SIZE * 8 / 1024 / 1024 * 1000)
c.metrics.MbpsLinkCapacity.Store(mbps)
```

**1.7. NAK Packet Received** (Lines 958-960)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvNAK++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvNAKSuccess.Add(1)
```

**1.8. Invalid NAK Packet** (Lines 965-967)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvInvalid++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvInvalid.Add(1)
```

**1.9. Retransmissions from NAK** (Lines 977-979)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRetransFromNAK += retransCount
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRetransFromNAK.Add(retransCount)
```

**1.10. ACKACK Packet Received** (Lines 1007-1009)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvACKACK++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvACKACKSuccess.Add(1)
```

**1.11. Invalid ACKACK Packet** (Lines 1020-1022)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvInvalid++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvInvalid.Add(1)
```

**1.12. Invalid Handshake Packet** (Lines 1052-1054, 1164-1166, 1276-1278, 1310-1312, 1321-1323, 1355-1357)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvInvalid++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvInvalid.Add(1)
```

**1.13. KM Packet Received** (Lines 1269-1271, 1348-1350)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktRecvKM++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktRecvKMSuccess.Add(1)
```

**1.14. KM Packet Sent** (Lines 1337-1339, 1602-1604)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktSentKM++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktSentKMSuccess.Add(1)
```

**1.15. Shutdown Packet Sent** (Lines 1418-1420)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktSentShutdown++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktSentShutdownSuccess.Add(1)
```

**1.16. NAK Packet Sent** (Lines 1443-1445)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktSentNAK++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktSentNAKSuccess.Add(1)
```

**1.17. ACK Packet Sent** (Lines 1494-1496)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktSentACK++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktSentACKSuccess.Add(1)
```

**1.18. ACKACK Packet Sent** (Lines 1514-1516)
```go
// BEFORE:
c.statisticsLock.Lock()
c.statistics.pktSentACKACK++
c.statisticsLock.Unlock()

// AFTER:
c.metrics.PktSentACKACKSuccess.Add(1)
```

**1.19. Header Size Initialization** (Lines 427-431)
```go
// BEFORE:
c.statistics.headerSize = 8 + 16 // 8 bytes UDP + 16 bytes SRT
if c.localAddr.(*net.UDPAddr).IP.To4() != nil {
    c.statistics.headerSize += 20 // 20 bytes IPv4 header
} else {
    c.statistics.headerSize += 40 // 40 bytes IPv6 header
}

// AFTER:
headerSize := uint64(8 + 16) // 8 bytes UDP + 16 bytes SRT
if c.localAddr.(*net.UDPAddr).IP.To4() != nil {
    headerSize += 20 // 20 bytes IPv4 header
} else {
    headerSize += 40 // 40 bytes IPv6 header
}
c.metrics.HeaderSize.Store(headerSize)
```

#### 2. Statistics Reading (Update to Atomic Loads)

**File**: `connection.go`

**2.1. `Stats()` Method** (Lines 1798-1917)
```go
// BEFORE:
c.statisticsLock.RLock()
defer c.statisticsLock.RUnlock()

s.Accumulated = StatisticsAccumulated{
    PktSentACK:        c.statistics.pktSentACK,
    PktRecvACK:        c.statistics.pktRecvACK,
    PktSentNAK:        c.statistics.pktSentNAK,
    PktRecvNAK:        c.statistics.pktRecvNAK,
    PktSentKM:         c.statistics.pktSentKM,
    PktRecvKM:         c.statistics.pktRecvKM,
    PktRecvUndecrypt:  c.statistics.pktRecvUndecrypt,
    ByteSent:          send.Byte + (send.Pkt * c.statistics.headerSize),
    ByteRecv:          recv.Byte + (recv.Pkt * c.statistics.headerSize),
    // ... etc
}

// AFTER (no lock needed):
s.Accumulated = StatisticsAccumulated{
    PktSentACK:        c.metrics.PktSentACKSuccess.Load(),
    PktRecvACK:        c.metrics.PktRecvACKSuccess.Load(),
    PktSentNAK:        c.metrics.PktSentNAKSuccess.Load(),
    PktRecvNAK:        c.metrics.PktRecvNAKSuccess.Load(),
    PktSentKM:         c.metrics.PktSentKMSuccess.Load(),
    PktRecvKM:         c.metrics.PktRecvKMSuccess.Load(),
    PktRecvUndecrypt:  c.metrics.PktRecvUndecrypt.Load(),
    ByteSent:          send.Byte + (send.Pkt * c.metrics.HeaderSize.Load()),
    ByteRecv:          recv.Byte + (recv.Pkt * c.metrics.HeaderSize.Load()),
    // ... etc
}

// For mbpsLinkCapacity (line 1912):
// BEFORE:
s.Instantaneous.MbpsLinkCapacity = c.statistics.mbpsLinkCapacity

// AFTER:
mbps := float64(c.metrics.MbpsLinkCapacity.Load()) / 1000.0
s.Instantaneous.MbpsLinkCapacity = mbps
```

**2.2. `GetExtendedStatistics()` Method** (Lines 993-1000)
```go
// BEFORE:
c.statisticsLock.RLock()
defer c.statisticsLock.RUnlock()
return &ExtendedStatistics{
    PktSentACKACK:     c.statistics.pktSentACKACK,
    PktRecvACKACK:     c.statistics.pktRecvACKACK,
    PktRetransFromNAK: c.statistics.pktRetransFromNAK,
}

// AFTER (no lock needed):
return &ExtendedStatistics{
    PktSentACKACK:     c.metrics.PktSentACKACKSuccess.Load(),
    PktRecvACKACK:     c.metrics.PktRecvACKACKSuccess.Load(),
    PktRetransFromNAK: c.metrics.PktRetransFromNAK.Load(),
}
```

**2.3. `printCloseStatistics()` Method** (Lines 1724-1792)
```go
// This method calls c.Stats(stats), which will automatically use atomic counters
// No changes needed - it will benefit from the atomic reads in Stats()
```

#### 3. Other Files Using Statistics

**3.1. `dial.go` - `Stats()` Call** (Line 835)
```go
// No changes needed - calls conn.Stats() which will use atomic counters
dl.conn.Stats(s)
```

**3.2. `contrib/common/statistics.go` - `PrintConnectionStatistics()`** (Line 72)
```go
// No changes needed - calls conn.Stats() which will use atomic counters
conn.Stats(stats)
```

**3.3. Test Files**
- `connection_test.go` (Lines 520, 549): No changes needed - calls `conn.Stats()`
- `congestion/live/receive_test.go`: Uses `recv.Stats()`, not connection stats

### Summary of Changes

**Total Locations to Update**: ~35 locations

1. **Statistics Increments**: ~25 locations (remove locks, use atomic `.Add()`)
2. **Statistics Reads**: 2 methods (`Stats()`, `GetExtendedStatistics()`)
3. **Header Size Initialization**: 1 location
4. **Link Capacity Update**: 1 location

**Benefits After Migration**:
- ✅ **No lock contention**: All statistics operations are lock-free
- ✅ **Better performance**: Atomic operations are faster than mutex locks
- ✅ **Simpler code**: No need to manage `statisticsLock`
- ✅ **Thread-safe**: Atomic operations are inherently thread-safe

**Breaking Changes**: None - all changes are internal to `srtConn` struct

---

## Prometheus Integration

### Design Decision: Custom /metrics Handler (No Prometheus Client Library)

**Rationale**:
- **No double collection**: Atomic counters are the single source of truth
- **No external dependencies**: Avoid prometheus client library
- **High performance**: Direct reads from atomic variables, efficient string building
- **Simple and lightweight**: Custom handler that formats Prometheus text format

### Metric Naming Convention

**Format**: `gosrt_{component}_{metric}_{unit}`

- **Component**: `connection`, `listener`, `dialer`
- **Metric**: `packets_received`, `packets_dropped`, `lock_hold_seconds`
- **Unit**: `total`, `bytes`, `seconds`

**Examples**:
- `gosrt_connection_packets_received_total{socket_id="0x12345678",type="ack",path="iouring"}`
- `gosrt_connection_packets_dropped_total{socket_id="0x12345678",type="ack",reason="parse_error"}`
- `gosrt_connection_lock_hold_seconds_avg{socket_id="0x12345678",lock="handle_packet"}`
- `gosrt_connection_lock_hold_seconds_max{socket_id="0x12345678",lock="handle_packet"}`

### Custom /metrics Handler Implementation

```go
// metrics/handler.go

package metrics

import (
    "fmt"
    "net/http"
    "sync"
    "strings"
)

// MetricsRegistry holds all connection metrics
type MetricsRegistry struct {
    connections map[uint32]*ConnectionMetrics
    mu          sync.RWMutex
}

var globalRegistry = &MetricsRegistry{
    connections: make(map[uint32]*ConnectionMetrics),
}

// RegisterConnection registers a connection's metrics
func RegisterConnection(socketId uint32, metrics *ConnectionMetrics) {
    globalRegistry.mu.Lock()
    defer globalRegistry.mu.Unlock()
    globalRegistry.connections[socketId] = metrics
}

// UnregisterConnection removes a connection's metrics
func UnregisterConnection(socketId uint32) {
    globalRegistry.mu.Lock()
    defer globalRegistry.mu.Unlock()
    delete(globalRegistry.connections, socketId)
}

// metricsBuilderPool is a sync.Pool for strings.Builder objects to reduce allocations
var metricsBuilderPool = sync.Pool{
    New: func() interface{} {
        b := &strings.Builder{}
        b.Grow(64 * 1024) // Pre-allocate 64KB buffer
        return b
    },
}

// MetricsHandler returns an HTTP handler that serves Prometheus-formatted metrics
func MetricsHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain; version=0.0.4")

        // Get strings.Builder from pool
        b := metricsBuilderPool.Get().(*strings.Builder)
        defer func() {
            // Reset and return to pool
            b.Reset()
            // Keep the grown capacity (don't shrink)
            metricsBuilderPool.Put(b)
        }()

        globalRegistry.mu.RLock()
        connections := make([]*ConnectionMetrics, 0, len(globalRegistry.connections))
        socketIds := make([]uint32, 0, len(globalRegistry.connections))
        for socketId, metrics := range globalRegistry.connections {
            connections = append(connections, metrics)
            socketIds = append(socketIds, socketId)
        }
        globalRegistry.mu.RUnlock()

        // Write metrics for each connection
        for i, metrics := range connections {
            socketId := socketIds[i]
            socketIdStr := fmt.Sprintf("0x%08x", socketId)

            // Packet counters
            writeCounterValue(b, "gosrt_connection_packets_received_total",
                metrics.PktRecvACKSuccess.Load(),
                "socket_id", socketIdStr, "type", "ack", "path", "iouring")

            writeCounterValue(b, "gosrt_connection_packets_dropped_total",
                metrics.PktRecvACKDropped.Load(),
                "socket_id", socketIdStr, "type", "ack", "reason", "parse_error")

            // ... (similar for all metrics)

            // Lock timing (average and max)
            if metrics.HandlePacketLockTiming != nil {
                holdAvg, holdMax, waitAvg, waitMax := metrics.HandlePacketLockTiming.GetStats()
                writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
                    holdAvg, "socket_id", socketIdStr, "lock", "handle_packet")
                writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
                    holdMax, "socket_id", socketIdStr, "lock", "handle_packet")
                writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
                    waitAvg, "socket_id", socketIdStr, "lock", "handle_packet")
                writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
                    waitMax, "socket_id", socketIdStr, "lock", "handle_packet")
            }
        }

        w.Write([]byte(b.String()))
    })
}

// Note: writeCounter is not used - we use writeCounterValue instead
// This function is kept for reference but should be removed

// Helper function with proper signature
func writeCounterValue(b *strings.Builder, name string, value uint64, labels ...string) {
    b.WriteString(name)
    if len(labels) > 0 {
        b.WriteByte('{')
        for i := 0; i < len(labels); i += 2 {
            if i > 0 {
                b.WriteByte(',')
            }
            fmt.Fprintf(b, "%s=\"%s\"", labels[i], labels[i+1])
        }
        b.WriteByte('}')
    }
    fmt.Fprintf(b, " %d\n", value)
}

func writeGauge(b *strings.Builder, name string, value float64, labels ...string) {
    b.WriteString(name)
    if len(labels) > 0 {
        b.WriteByte('{')
        for i := 0; i < len(labels); i += 2 {
            if i > 0 {
                b.WriteByte(',')
            }
            fmt.Fprintf(b, "%s=\"%s\"", labels[i], labels[i+1])
        }
        b.WriteByte('}')
    }
    fmt.Fprintf(b, " %.9f\n", value)
}
```

**Performance Optimizations**:
- **`sync.Pool` for `strings.Builder`**: Reuse builders to avoid allocations
  - Pre-allocated 64KB buffer in pool
  - Reset and return to pool after use
  - Keeps grown capacity (doesn't shrink)
- Single read lock to snapshot all connections
- Direct atomic reads (no locks)
- Efficient string formatting
- Minimal allocations (only for connection snapshot, not builder)

**Memory Management**:
- `strings.Builder` is pooled and reused across requests
- Buffer capacity is preserved (not shrunk) for better performance
- Only allocations are for the connection snapshot slice (small, infrequent)

### HTTP Server Integration

**Location**: `server.go` or new `metrics/server.go`

```go
// Add to Server struct
type Server struct {
    // ... existing fields ...
    metricsServer *http.Server  // Optional metrics HTTP server
}

// StartMetricsServer starts an HTTP server for Prometheus metrics
func (s *Server) StartMetricsServer(addr string) error {
    if addr == "" {
        return nil // Metrics disabled
    }

    mux := http.NewServeMux()
    mux.Handle("/metrics", metrics.MetricsHandler())

    s.metricsServer = &http.Server{
        Addr:    addr,
        Handler: mux,
    }

    go func() {
        if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            // Log error (if logger available)
        }
    }()

    return nil
}
```

**Connection Registration**:
```go
// In newSRTConn() or connection initialization
func newSRTConn(...) *srtConn {
    c := &srtConn{
        // ... existing fields ...
        metrics: &ConnectionMetrics{
            HandlePacketLockTiming: &LockTimingMetrics{},
            ReceiverLockTiming:     &LockTimingMetrics{},
            SenderLockTiming:       &LockTimingMetrics{},
        },
    }

    // Register with metrics registry
    metrics.RegisterConnection(c.socketId, c.metrics)

    return c
}

// In close()
func (c *srtConn) close() {
    // ... existing close logic ...

    // Unregister from metrics registry
    metrics.UnregisterConnection(c.socketId)
}
```

### Configuration

**Location**: `config.go`

```go
type Config struct {
    // ... existing fields ...

    // Metrics configuration
    MetricsEnabled    bool   // Enable metrics collection
    MetricsListenAddr string // HTTP address for /metrics endpoint (e.g., ":9090")
}
```

---

## Lock Timing Metrics

### Design Decision: Simple Array-Based Latency Tracking

**Rationale**:
- **Avoid histogram overhead**: Prometheus histograms are expensive (bucket calculations)
- **Simple and fast**: Array of recent measurements with atomic index
- **Sufficient visibility**: Average and max provide good insight without complexity
- **Low overhead**: Minimal allocations, atomic operations only

### Lock Timing Structure

```go
// metrics/lock_timing.go

const (
    LockTimingSamples = 10 // Number of recent samples to keep
)

// LockTimingMetrics tracks lock hold and wait times
// Uses lock-free ring buffer with atomic values for maximum performance
type LockTimingMetrics struct {
    // Recent hold time samples (nanoseconds) - each slot is atomic for lock-free reads
    holdTimeSamples [LockTimingSamples]atomic.Int64
    holdTimeIndex   atomic.Uint64  // Global write counter for circular buffer

    // Recent wait time samples (nanoseconds) - each slot is atomic for lock-free reads
    waitTimeSamples [LockTimingSamples]atomic.Int64
    waitTimeIndex   atomic.Uint64  // Global write counter for circular buffer

    // Max values (nanoseconds)
    maxHoldTime atomic.Int64
    maxWaitTime atomic.Int64

    // Note: We don't need a separate totalAcquisitions counter.
    // holdTimeIndex and waitTimeIndex already track the total number of acquisitions
    // (they wrap around, but for rate calculations we can use the current value)
}

// RecordHoldTime records a lock hold time measurement
func (ltm *LockTimingMetrics) RecordHoldTime(duration time.Duration) {
    ns := duration.Nanoseconds()

    // Update max hold time (lock-free CAS loop)
    for {
        current := ltm.maxHoldTime.Load()
        if ns <= current {
            break
        }
        if ltm.maxHoldTime.CompareAndSwap(current, ns) {
            break
        }
    }

    // Store in circular buffer (lock-free)
    i := ltm.holdTimeIndex.Add(1) // Returns new value
    slot := i % LockTimingSamples
    ltm.holdTimeSamples[slot].Store(ns)
}

// RecordWaitTime records a lock wait time measurement
func (ltm *LockTimingMetrics) RecordWaitTime(duration time.Duration) {
    ns := duration.Nanoseconds()

    // Update max wait time (lock-free CAS loop)
    for {
        current := ltm.maxWaitTime.Load()
        if ns <= current {
            break
        }
        if ltm.maxWaitTime.CompareAndSwap(current, ns) {
            break
        }
    }

    // Store in circular buffer (lock-free)
    i := ltm.waitTimeIndex.Add(1) // Returns new value
    slot := i % LockTimingSamples
    ltm.waitTimeSamples[slot].Store(ns)
}

// GetTotalAcquisitions returns the total number of lock acquisitions
// Uses holdTimeIndex as a proxy (saves an atomic operation)
func (ltm *LockTimingMetrics) GetTotalAcquisitions() uint64 {
    // Use holdTimeIndex as it's incremented on every unlock
    // This is close enough for rate calculations
    return ltm.holdTimeIndex.Load()
}

// GetStats returns average and max hold/wait times
// All reads are lock-free (atomic operations only)
//
// Performance note: For 10 values, a simple loop is optimal.
// The Go compiler may auto-vectorize this, and the overhead of
// SIMD setup (if we used assembly/cgo) would likely exceed the benefit.
func (ltm *LockTimingMetrics) GetStats() (holdAvg, holdMax, waitAvg, waitMax float64) {
    // Snapshot hold time samples (lock-free atomic reads)
    // Simple loop - compiler may auto-vectorize for 10 values
    var holdSum int64
    holdCount := 0
    for i := 0; i < LockTimingSamples; i++ {
        if sample := ltm.holdTimeSamples[i].Load(); sample > 0 {
            holdSum += sample
            holdCount++
        }
    }
    if holdCount > 0 {
        holdAvg = float64(holdSum) / float64(holdCount) / 1e9 // Convert to seconds
    }
    holdMax = float64(ltm.maxHoldTime.Load()) / 1e9 // Convert to seconds

    // Snapshot wait time samples (lock-free atomic reads)
    // Simple loop - compiler may auto-vectorize for 10 values
    var waitSum int64
    waitCount := 0
    for i := 0; i < LockTimingSamples; i++ {
        if sample := ltm.waitTimeSamples[i].Load(); sample > 0 {
            waitSum += sample
            waitCount++
        }
    }
    if waitCount > 0 {
        waitAvg = float64(waitSum) / float64(waitCount) / 1e9 // Convert to seconds
    }
    waitMax = float64(ltm.maxWaitTime.Load()) / 1e9 // Convert to seconds

    return holdAvg, holdMax, waitAvg, waitMax
}

// SnapshotHoldTimes returns a snapshot of all hold time samples (for debugging)
func (ltm *LockTimingMetrics) SnapshotHoldTimes() [LockTimingSamples]int64 {
    var out [LockTimingSamples]int64
    for i := 0; i < LockTimingSamples; i++ {
        out[i] = ltm.holdTimeSamples[i].Load()
    }
    return out
}

// SnapshotWaitTimes returns a snapshot of all wait time samples (for debugging)
func (ltm *LockTimingMetrics) SnapshotWaitTimes() [LockTimingSamples]int64 {
    var out [LockTimingSamples]int64
    for i := 0; i < LockTimingSamples; i++ {
        out[i] = ltm.waitTimeSamples[i].Load()
    }
    return out
}

### Lock Timing Implementation Pattern

**Recommended: Defer-Based Measurement with Helper**

```go
// metrics/helpers.go

// WithLockTiming executes a function while measuring lock hold and wait times
func WithLockTiming(metrics *LockTimingMetrics, mutex sync.Locker, fn func()) {
    // Measure wait time
    waitStart := time.Now()
    mutex.Lock()
    waitDuration := time.Since(waitStart)

    if waitDuration > 0 {
        metrics.RecordWaitTime(waitDuration)
    }
    // Note: RecordHoldTime will increment holdTimeIndex, which serves as acquisition counter

    // Measure hold time
    defer func() {
        holdDuration := time.Since(waitStart) // Total time from lock acquisition
        metrics.RecordHoldTime(holdDuration) // This increments holdTimeIndex
        mutex.Unlock()
    }()

    fn()
}

// Usage in handlePacketDirect
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    WithLockTiming(c.metrics.HandlePacketLockTiming, &c.handlePacketMutex, func() {
        c.handlePacket(p)
    })
}
```

**Alternative: Direct Implementation (If Helper Adds Overhead)**

```go
// In handlePacketDirect
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    waitStart := time.Now()
    c.handlePacketMutex.Lock()
    waitDuration := time.Since(waitStart)

    if c.metrics.HandlePacketLockTiming != nil {
        if waitDuration > 0 {
            c.metrics.HandlePacketLockTiming.RecordWaitTime(waitDuration)
        }
        // Note: RecordHoldTime will increment holdTimeIndex, which serves as acquisition counter
    }

    defer func() {
        holdDuration := time.Since(waitStart)
        if c.metrics.HandlePacketLockTiming != nil {
            c.metrics.HandlePacketLockTiming.RecordHoldTime(holdDuration) // This increments holdTimeIndex
        }
        c.handlePacketMutex.Unlock()
    }()

    c.handlePacket(p)
}
```

**Recommendation**: Use **direct implementation** for hot paths (like `handlePacketDirect`), helper function for less critical paths.

### Lock Timing Helper

```go
// metrics/helpers.go

// WithLockTiming executes a function while measuring lock hold time
func WithLockTiming(socketId uint32, lockName string, mutex sync.Locker, fn func()) {
    lockStart := time.Now()
    mutex.Lock()
    defer func() {
        holdDuration := time.Since(lockStart)
        UpdateLockHoldTime(socketId, lockName, holdDuration)
        mutex.Unlock()
    }()
    fn()
}

// Usage
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    WithLockTiming(c.socketId, "handle_packet", &c.handlePacketMutex, func() {
        c.handlePacket(p)
    })
}
```

### Lock Timing Metrics Exposed

**Prometheus Metrics** (via custom handler):
- `gosrt_connection_lock_hold_seconds_avg{socket_id="0x12345678",lock="handle_packet"}` - Average hold time (from last 10 samples)
- `gosrt_connection_lock_hold_seconds_max{socket_id="0x12345678",lock="handle_packet"}` - Maximum hold time (all-time)
- `gosrt_connection_lock_wait_seconds_avg{socket_id="0x12345678",lock="handle_packet"}` - Average wait time (from last 10 samples)
- `gosrt_connection_lock_wait_seconds_max{socket_id="0x12345678",lock="handle_packet"}` - Maximum wait time (all-time)
- `gosrt_connection_lock_acquisitions_total{socket_id="0x12345678",lock="handle_packet"}` - Total lock acquisitions

**Implementation in /metrics Handler**:
```go
// In MetricsHandler() (b is from pool, will be reset after use)
if metrics.HandlePacketLockTiming != nil {
    holdAvg, holdMax, waitAvg, waitMax := metrics.HandlePacketLockTiming.GetStats()

    writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
        holdAvg, "socket_id", socketIdStr, "lock", "handle_packet")
    writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
        holdMax, "socket_id", socketIdStr, "lock", "handle_packet")
    writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
        waitAvg, "socket_id", socketIdStr, "lock", "handle_packet")
    writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
        waitMax, "socket_id", socketIdStr, "lock", "handle_packet")

    writeCounterValue(b, "gosrt_connection_lock_acquisitions_total",
        metrics.HandlePacketLockTiming.GetTotalAcquisitions(),
        "socket_id", socketIdStr, "lock", "handle_packet")
}
```

**Use Cases**:
- Detect stuck locks (max hold time > threshold, e.g., > 1 second)
- Identify lock contention (high wait times)
- Performance optimization (identify slow operations)
- Monitor lock health over time (average trends)

**Trade-offs**:
- ✅ **Simple**: No complex histogram calculations
- ✅ **Fast**: Minimal overhead (atomic operations, lock-free reads/writes)
- ✅ **Lock-free**: No mutexes needed, each slot is atomic
- ✅ **Race-free**: Concurrent writes and reads are safe
- ✅ **Efficient**: No separate acquisition counter (uses index counter)
- ✅ **Sufficient**: Average + max provide good visibility
- ⚠️ **Limited history**: Only last 10 samples (but sufficient for monitoring)
- ⚠️ **No percentiles**: Can't calculate 95th/99th percentile (but max is often more useful)
- ⚠️ **Non-atomic snapshot**: Snapshot reads individual values, but this is acceptable for metrics (not critical data)

**SIMD Optimization Analysis**:
- **For 10 int64 values**: Simple loop is optimal
  - Go compiler may auto-vectorize the sum loop (SSE/AVX)
  - SIMD setup overhead (assembly/cgo) would likely exceed benefit
  - Modern CPUs are very fast at simple additions (~1 cycle per add)
  - 10 additions ≈ 10-20 CPU cycles (negligible)
- **If we needed 100+ values**: SIMD would be worth considering
  - Could use `golang.org/x/simd` or inline assembly
  - AVX-512 can sum 8 int64 values per instruction
  - But for metrics collection (not hot path), simple loop is fine
- **Current approach**: Let the compiler optimize, measure if needed
  - Profile `GetStats()` if it becomes a bottleneck
  - Unlikely to be an issue (called once per /metrics scrape, ~every 15s)

---

## Implementation Plan

### Phase 1: Metrics Infrastructure (Foundation)

**Tasks**:
1. Create `metrics/` package
2. Define `ConnectionMetrics` struct with atomic counters
3. Implement Prometheus metric definitions
4. Implement Prometheus HTTP handler
5. Add configuration options (`MetricsEnabled`, `MetricsListenAddr`)

**Files**:
- `metrics/metrics.go` - Core metrics structures
- `metrics/prometheus.go` - Prometheus integration
- `metrics/helpers.go` - Helper functions
- `config.go` - Add metrics configuration

**Estimated Effort**: 4-6 hours

### Phase 2: Lock Timing (Critical for Debugging)

**Tasks**:
1. Implement lock timing helpers
2. Add lock timing to `handlePacketMutex`
3. Add lock timing to `receiver.lock` and `sender.lock`
4. Expose lock timing metrics to Prometheus

**Files**:
- `metrics/lock_timing.go` - Lock timing implementation
- `connection.go` - Update `handlePacketDirect()` to use lock timing
- `congestion/live/receive.go` - Add lock timing to receiver
- `congestion/live/send.go` - Add lock timing to sender

**Estimated Effort**: 3-4 hours

### Phase 3: Receive Path Metrics (Complete Visibility)

**Tasks**:
1. Add counters for all receive path drop points
2. Add path identification (io_uring vs ReadFrom)
3. Add error classification
4. Update `processRecvCompletion()` to increment metrics
5. Update `reader()` to increment metrics

**Files**:
- `listen_linux.go` - Add metrics to io_uring path
- `listen.go` - Add metrics to ReadFrom path
- `connection.go` - Add metrics to `handlePacket()`

**Estimated Effort**: 4-5 hours

### Phase 4: Send Path Metrics (Complete Visibility)

**Tasks**:
1. Add counters for all send path drop points
2. Add path identification (io_uring vs WriteTo)
3. Add error classification
4. Update `sendIoUring()` to increment metrics
5. Update `send()` fallback to increment metrics

**Files**:
- `connection_linux.go` - Add metrics to io_uring send path
- `listen.go`, `dial.go` - Add metrics to WriteTo fallback

**Estimated Effort**: 3-4 hours

### Phase 5: Statistics Migration (Backward Compatibility)

**Tasks**:
1. Update `Stats()` method to read from new metrics
2. Maintain backward compatibility with existing `Statistics` struct
3. Add migration path documentation

**Files**:
- `connection.go` - Update `Stats()` method
- `statistics.go` - Update if needed

**Estimated Effort**: 2-3 hours

### Phase 6: Testing and Validation

**Tasks**:
1. Unit tests for metrics collection
2. Integration tests for Prometheus endpoint
3. Performance tests (ensure metrics don't impact performance)
4. Documentation updates

**Estimated Effort**: 4-6 hours

### Total Estimated Effort: 20-28 hours

---

## Example Prometheus Queries

### Packet Drop Rate
```promql
rate(gosrt_connection_packets_dropped_total[5m]) /
rate(gosrt_connection_packets_received_total[5m]) * 100
```

### Lock Contention (average wait time)
```promql
gosrt_connection_lock_wait_seconds_avg
```

### Stuck Lock Detection (max hold time > 1 second)
```promql
gosrt_connection_lock_hold_seconds_max > 1
```

### Path Usage (io_uring vs fallback)
```promql
rate(gosrt_connection_packets_received_total{path="iouring"}[5m]) /
rate(gosrt_connection_packets_received_total[5m]) * 100
```

### Lock Acquisition Rate
```promql
rate(gosrt_connection_lock_acquisitions_total[5m])
```

---

## Benefits of This Design

1. **Complete Visibility**: Every packet path has positive and negative counters
2. **Performance**: Atomic counters eliminate lock contention
3. **No Double Collection**: Single source of truth (atomic counters), no duplicate metrics
4. **No External Dependencies**: Custom /metrics handler, no prometheus client library
5. **High Performance**: Direct atomic reads, efficient string building
6. **Simple Lock Timing**: Array-based tracking (average + max) instead of expensive histograms
7. **Debugging**: Lock timing helps identify stuck locks
8. **Monitoring**: Prometheus-compatible format enables real-time monitoring
9. **Consistency**: Unified approach across all metrics
10. **Extensibility**: Easy to add new metrics as needed
11. **Backward Compatible**: Can migrate gradually from existing statistics system

---

## Design Decisions Summary

### 1. Atomic Counters (Not Prometheus Client Library)
- ✅ **Single source of truth**: Atomic counters are the only metrics storage
- ✅ **No double collection**: Custom /metrics handler reads directly from atomic variables
- ✅ **No external dependencies**: No prometheus client library required
- ✅ **High performance**: Direct reads, efficient string building

### 2. Simple Lock Timing (Not Histograms)
- ✅ **Array-based tracking**: Last 10 samples with atomic index
- ✅ **Average + Max**: Sufficient visibility without histogram overhead
- ✅ **Low overhead**: Minimal allocations, atomic operations only
- ✅ **Fast calculations**: Simple average, no bucket management

### 3. Custom /metrics Handler
- ✅ **Prometheus-compatible format**: Standard text format, works with Prometheus
- ✅ **Efficient**: strings.Builder with pre-allocated buffer
- ✅ **Direct reads**: No intermediate data structures
- ✅ **Simple**: Easy to understand and maintain

### Open Questions

1. **Metrics Cardinality**: Should we expose per-connection metrics or aggregate?
   - **Recommendation**: Per-connection (allows detailed debugging), with optional aggregation in Prometheus queries

2. **Metrics Retention**: How long to keep metrics in memory?
   - **Recommendation**: Keep for connection lifetime, unregister on close

3. **Performance Impact**: Will atomic operations impact performance?
   - **Recommendation**: Atomic operations are very fast (typically single CPU instruction), minimal overhead

4. **Lock Timing Overhead**: Will lock timing measurement add overhead?
   - **Recommendation**: Minimal (time.Now() is ~20-50ns), but can be made optional via config flag

5. **Lock Timing Sample Size**: Is 10 samples sufficient?
   - **Recommendation**: Start with 10, can be made configurable if needed. 10 samples provide good rolling average without memory overhead.

---

## Next Steps

1. **Review and approve design**
2. **Implement Phase 1** (Metrics Infrastructure)
3. **Implement Phase 2** (Lock Timing) - **Critical for current debugging**
4. **Iterate on Phases 3-6** based on priorities

