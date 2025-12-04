# Metrics and Statistics Design

## Overview

This document provides a comprehensive design for a unified metrics and statistics system for the GoSRT library. It reviews the current implementation, identifies gaps, and proposes a holistic approach to metrics collection with Prometheus integration and lock timing measurements.

**Important**: For detailed definitions of packet loss vs. packet drop counters, see [Packet Loss vs. Packet Drop Definitions](./packet_loss_drop_definitions.md).

## Table of Contents

1. [Current Statistics Implementation Review](#current-statistics-implementation-review)
2. [Locking Strategy Analysis](#locking-strategy-analysis)
3. [Packet Processing Paths Analysis](#packet-processing-paths-analysis)
4. [Metrics Design Principles](#metrics-design-principles)
5. [Unified Metrics Architecture](#unified-metrics-architecture)
6. [Prometheus Integration](#prometheus-integration)
7. [Lock Timing Metrics](#lock-timing-metrics)
8. [Go Runtime Metrics](#go-runtime-metrics)
9. [Detailed Migration Guide: `connStats` to Atomic Counters](#detailed-migration-guide-connstats-to-atomic-counters)
10. [Implementation Plan](#implementation-plan)

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
- Update `Stats()` to read from new atomic metrics
- Fully migrate to atomic-based system

**Phase 3: Remove Old Counters**
- Remove `connStats` struct completely
- Remove `statisticsLock` (all counters are atomic)
- Clean up all old increment code

---

## Go Runtime Metrics

### Overview

Go runtime metrics provide visibility into the Go process itself, including memory usage, garbage collection statistics, goroutine counts, and other runtime information. These metrics are essential for understanding the health and performance of the GoSRT library in production.

**Rationale**: Similar to `promauto`, we want to expose standard Go runtime metrics to enable:
- Memory leak detection
- GC pressure monitoring
- Goroutine leak detection
- Overall process health monitoring

### Metrics to Include

Based on what `prometheus/client_golang` exposes via `promauto`, we should include:

#### 1. Memory Metrics

| Metric Name | Description | Source |
|------------|-------------|--------|
| `go_memstats_alloc_bytes` | Bytes allocated and still in use | `runtime.MemStats.Alloc` |
| `go_memstats_alloc_bytes_total` | Total bytes allocated (cumulative) | `runtime.MemStats.TotalAlloc` |
| `go_memstats_sys_bytes` | Bytes obtained from system | `runtime.MemStats.Sys` |
| `go_memstats_lookups_total` | Number of pointer lookups | `runtime.MemStats.Lookups` |
| `go_memstats_mallocs_total` | Total number of mallocs | `runtime.MemStats.Mallocs` |
| `go_memstats_frees_total` | Total number of frees | `runtime.MemStats.Frees` |
| `go_memstats_heap_alloc_bytes` | Bytes allocated for heap objects | `runtime.MemStats.HeapAlloc` |
| `go_memstats_heap_sys_bytes` | Bytes obtained from system for heap | `runtime.MemStats.HeapSys` |
| `go_memstats_heap_idle_bytes` | Bytes in idle spans | `runtime.MemStats.HeapIdle` |
| `go_memstats_heap_inuse_bytes` | Bytes in in-use spans | `runtime.MemStats.HeapInuse` |
| `go_memstats_heap_released_bytes` | Bytes released to the OS | `runtime.MemStats.HeapReleased` |
| `go_memstats_heap_objects` | Number of allocated heap objects | `runtime.MemStats.HeapObjects` |
| `go_memstats_stack_inuse_bytes` | Bytes used for stack spans | `runtime.MemStats.StackInuse` |
| `go_memstats_stack_sys_bytes` | Bytes obtained from system for stack | `runtime.MemStats.StackSys` |
| `go_memstats_mspan_inuse_bytes` | Bytes used for mspan structures | `runtime.MemStats.MSpanInuse` |
| `go_memstats_mspan_sys_bytes` | Bytes obtained from system for mspan | `runtime.MemStats.MSpanSys` |
| `go_memstats_mcache_inuse_bytes` | Bytes used for mcache structures | `runtime.MemStats.MCacheInuse` |
| `go_memstats_mcache_sys_bytes` | Bytes obtained from system for mcache | `runtime.MemStats.MCacheSys` |
| `go_memstats_buck_hash_sys_bytes` | Bytes used by the profiling bucket hash table | `runtime.MemStats.BuckHashSys` |
| `go_memstats_gc_sys_bytes` | Bytes used for GC metadata | `runtime.MemStats.GCSys` |
| `go_memstats_other_sys_bytes` | Bytes used for other system allocations | `runtime.MemStats.OtherSys` |
| `go_memstats_next_gc_bytes` | Target heap size for next GC | `runtime.MemStats.NextGC` |
| `go_memstats_last_gc_time_seconds` | Last GC time (relative to program start) | `runtime.MemStats.LastGC` (convert to seconds) |

#### 2. Garbage Collection Metrics

| Metric Name | Description | Source |
|------------|-------------|--------|
| `go_memstats_gc_cpu_fraction` | Fraction of CPU time used by GC | `runtime.MemStats.GCCPUFraction` |
| `go_memstats_gc_count` | Number of GC cycles | `runtime.MemStats.NumGC` |
| `go_memstats_gc_duration_seconds` | Summary of GC pause durations | `runtime.MemStats.PauseTotalNs` (convert to seconds) |

#### 3. Goroutine Metrics

| Metric Name | Description | Source |
|------------|-------------|--------|
| `go_goroutines` | Number of goroutines | `runtime.NumGoroutine()` |

#### 4. CPU Metrics

| Metric Name | Description | Source |
|------------|-------------|--------|
| `go_cpu_count` | Number of logical CPUs | `runtime.NumCPU()` |

**Note**: Thread count is not directly available in Go's `runtime` package. If needed, it could be obtained via platform-specific methods (e.g., parsing `/proc/self/status` on Linux), but this adds complexity and is typically not needed.

### Implementation

**Location**: `metrics/runtime.go`

```go
package metrics

import (
    "fmt"
    "runtime"
    "strings"
    "time"
)

// Track program start time for GC timestamp conversion
var programStartTime = time.Now()

// writeRuntimeMetrics writes Go runtime metrics to the strings.Builder
// These metrics are compatible with prometheus/client_golang's promauto metrics
func writeRuntimeMetrics(b *strings.Builder) {
    var m runtime.MemStats
    runtime.ReadMemStats(&m)

    // Memory metrics
    writeGauge(b, "go_memstats_alloc_bytes", float64(m.Alloc))
    writeGauge(b, "go_memstats_alloc_bytes_total", float64(m.TotalAlloc))
    writeGauge(b, "go_memstats_sys_bytes", float64(m.Sys))
    writeGauge(b, "go_memstats_lookups_total", float64(m.Lookups))
    writeGauge(b, "go_memstats_mallocs_total", float64(m.Mallocs))
    writeGauge(b, "go_memstats_frees_total", float64(m.Frees))

    // Heap metrics
    writeGauge(b, "go_memstats_heap_alloc_bytes", float64(m.HeapAlloc))
    writeGauge(b, "go_memstats_heap_sys_bytes", float64(m.HeapSys))
    writeGauge(b, "go_memstats_heap_idle_bytes", float64(m.HeapIdle))
    writeGauge(b, "go_memstats_heap_inuse_bytes", float64(m.HeapInuse))
    writeGauge(b, "go_memstats_heap_released_bytes", float64(m.HeapReleased))
    writeGauge(b, "go_memstats_heap_objects", float64(m.HeapObjects))

    // Stack metrics
    writeGauge(b, "go_memstats_stack_inuse_bytes", float64(m.StackInuse))
    writeGauge(b, "go_memstats_stack_sys_bytes", float64(m.StackSys))

    // MSpan metrics
    writeGauge(b, "go_memstats_mspan_inuse_bytes", float64(m.MSpanInuse))
    writeGauge(b, "go_memstats_mspan_sys_bytes", float64(m.MSpanSys))

    // MCache metrics
    writeGauge(b, "go_memstats_mcache_inuse_bytes", float64(m.MCacheInuse))
    writeGauge(b, "go_memstats_mcache_sys_bytes", float64(m.MCacheSys))

    // Other memory metrics
    writeGauge(b, "go_memstats_buck_hash_sys_bytes", float64(m.BuckHashSys))
    writeGauge(b, "go_memstats_gc_sys_bytes", float64(m.GCSys))
    writeGauge(b, "go_memstats_other_sys_bytes", float64(m.OtherSys))
    writeGauge(b, "go_memstats_next_gc_bytes", float64(m.NextGC))

    // GC metrics
    if m.LastGC != 0 {
        // LastGC is in nanoseconds since program start
        // Convert to seconds since program start (relative time)
        lastGCRelative := float64(m.LastGC) / 1e9
        writeGauge(b, "go_memstats_last_gc_time_seconds", lastGCRelative)
    }
    writeGauge(b, "go_memstats_gc_cpu_fraction", m.GCCPUFraction)
    writeGauge(b, "go_memstats_gc_count", float64(m.NumGC))

    // GC pause duration (total)
    writeGauge(b, "go_memstats_gc_duration_seconds", float64(m.PauseTotalNs)/1e9)

    // Goroutines
    writeGauge(b, "go_goroutines", float64(runtime.NumGoroutine()))

    // CPU count
    writeGauge(b, "go_cpu_count", float64(runtime.NumCPU()))
}
```

### Integration into MetricsHandler

**Update `MetricsHandler()` to include runtime metrics**:

```go
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

        // Write Go runtime metrics first (standard metrics, compatible with prometheus/client_golang)
        writeRuntimeMetrics(b)

        // Write application-specific metrics
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

            // ... (connection-specific metrics)
        }

        w.Write([]byte(b.String()))
    })
}
```

### Performance Considerations

**Cost of `runtime.ReadMemStats()`**:
- **Stop-the-world operation**: `runtime.ReadMemStats()` stops all goroutines briefly (< 1ms typically)
- **Frequency**: Called once per `/metrics` scrape (typically every 15-60 seconds)
- **Impact**: Minimal for typical scraping intervals, but should be documented
- **Alternative**: Could cache metrics with a TTL (e.g., 1 second) to reduce calls if needed

**Caching Option** (if profiling shows it's needed):
```go
var (
    runtimeMetricsCache struct {
        data      string
        timestamp time.Time
        mu        sync.RWMutex
    }
    runtimeMetricsCacheTTL = 1 * time.Second
)

func getCachedRuntimeMetrics() string {
    runtimeMetricsCache.mu.RLock()
    if time.Since(runtimeMetricsCache.timestamp) < runtimeMetricsCacheTTL {
        data := runtimeMetricsCache.data
        runtimeMetricsCache.mu.RUnlock()
        return data
    }
    runtimeMetricsCache.mu.RUnlock()

    // Cache miss - generate new metrics
    var b strings.Builder
    b.Grow(4 * 1024) // Smaller buffer for runtime metrics
    writeRuntimeMetrics(&b)
    data := b.String()

    runtimeMetricsCache.mu.Lock()
    runtimeMetricsCache.data = data
    runtimeMetricsCache.timestamp = time.Now()
    runtimeMetricsCache.mu.Unlock()

    return data
}
```

**Recommendation**: Start without caching. The stop-the-world pause is typically < 1ms and only occurs during metrics scraping (infrequent). Add caching only if profiling shows it's a bottleneck.

### Example Prometheus Queries

**Memory Usage**:
```promql
go_memstats_heap_alloc_bytes / 1024 / 1024  # MB
```

**GC Pressure**:
```promql
rate(go_memstats_gc_count[5m])  # GCs per second
go_memstats_gc_cpu_fraction  # Fraction of CPU used by GC
```

**Goroutine Leak Detection**:
```promql
go_goroutines  # Alert if > threshold
rate(go_goroutines[5m])  # Alert if increasing rapidly
```

**Memory Leak Detection**:
```promql
rate(go_memstats_heap_alloc_bytes[5m])  # Alert if continuously increasing
```

### Benefits

1. **Standard Metrics**: Compatible with standard Prometheus Go metrics (same names as `prometheus/client_golang`)
2. **Process Health**: Monitor memory, Gphase 3 C, and goroutine health
3. **Debugging**: Identify memory leaks, GC pressure, goroutine leaks
4. **No External Dependencies**: Uses only Go standard library (`runtime` package)
5. **Low Overhead**: Called only during metrics scraping (infrequent)
6. **Familiar**: Same metric names as `promauto`, making it easy for operators familiar with Prometheus Go metrics

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

## Detailed Migration Guide: Congestion Control Statistics to Atomic Counters

This section documents the migration of congestion control statistics from lock-protected structs to atomic counters in `ConnectionMetrics`. This migration will eliminate lock contention in the congestion control layer and make all statistics reads lock-free.

### Current Congestion Control Statistics Structure

**Location**: `congestion/live/receive.go:51`, `congestion/live/send.go:42`

**Receiver Statistics** (`congestion.ReceiveStats`):
```go
type ReceiveStats struct {
    Pkt  uint64
    Byte uint64
    PktUnique  uint64
    ByteUnique uint64
    PktLoss  uint64
    ByteLoss uint64
    PktRetrans  uint64
    ByteRetrans uint64
    PktBelated  uint64
    ByteBelated uint64
    PktDrop  uint64
    ByteDrop uint64
    // instantaneous
    PktBuf  uint64
    ByteBuf uint64
    MsBuf   uint64
    BytePayload uint64
    MbpsEstimatedRecvBandwidth float64
    MbpsEstimatedLinkCapacity  float64
    PktLossRate float64
}
```

**Sender Statistics** (`congestion.SendStats`):
```go
type SendStats struct {
    Pkt  uint64
    Byte uint64
    PktUnique  uint64
    ByteUnique uint64
    PktLoss  uint64
    ByteLoss uint64
    PktRetrans  uint64
    ByteRetrans uint64
    UsSndDuration uint64 // microseconds
    PktDrop  uint64
    ByteDrop uint64
    // instantaneous
    PktBuf  uint64
    ByteBuf uint64
    MsBuf   uint64
    PktFlightSize uint64
    UsPktSndPeriod float64 // microseconds
    BytePayload    uint64
    MbpsEstimatedInputBandwidth float64
    MbpsEstimatedSentBandwidth  float64
    PktLossRate float64
}
```

**Current Locking Strategy**:
- **Receiver**: `sync.RWMutex` (`r.lock`) protects `r.statistics` field
- **Sender**: `sync.RWMutex` (`s.lock`) protects `s.statistics` field
- **Write operations**: All increments happen while holding write lock (`Lock()`)
- **Read operations**: `Stats()` method acquires read lock (`RLock()`) or write lock (`Lock()`)
- **Frequency**: Very high (every packet push, tick, ACK, NAK)

**Current Usage Pattern**:
```go
// Write (in pushLocked, tickDeliverPackets, etc.)
r.lock.Lock()
r.statistics.Pkt++
r.statistics.Byte += pktLen
r.lock.Unlock()

// Read (in Stats())
r.lock.Lock()
defer r.lock.Unlock()
return r.statistics  // Returns struct copy
```

### Migration Mapping: Congestion Control → `ConnectionMetrics`

**Design Decision**: Add congestion control statistics to `ConnectionMetrics` struct as atomic counters. The congestion control layer will increment these atomic counters directly, eliminating the need for locks during statistics updates.

| `ReceiveStats` Field | `ConnectionMetrics` Field | Type | Notes |
|---------------------|---------------------------|------|-------|
| `Pkt` | `CongestionRecvPkt` | `atomic.Uint64` | Total packets received |
| `Byte` | `CongestionRecvByte` | `atomic.Uint64` | Total bytes received |
| `PktUnique` | `CongestionRecvPktUnique` | `atomic.Uint64` | Unique packets received |
| `ByteUnique` | `CongestionRecvByteUnique` | `atomic.Uint64` | Unique bytes received |
| `PktLoss` | `CongestionRecvPktLoss` | `atomic.Uint64` | Packets lost (gaps detected by receiver, before sending NAK) |
| `ByteLoss` | `CongestionRecvByteLoss` | `atomic.Uint64` | Bytes lost (gaps detected by receiver, before sending NAK) |
| `PktRetrans` | `CongestionRecvPktRetrans` | `atomic.Uint64` | Retransmitted packets received |
| `ByteRetrans` | `CongestionRecvByteRetrans` | `atomic.Uint64` | Retransmitted bytes received |
| `PktBelated` | `CongestionRecvPktBelated` | `atomic.Uint64` | Belated packets (too old) |
| `ByteBelated` | `CongestionRecvByteBelated` | `atomic.Uint64` | Belated bytes |
| `PktDrop` | `CongestionRecvPktDrop` | `atomic.Uint64` | Packets dropped locally (too old, duplicate, already ACK'd, etc.) |
| `ByteDrop` | `CongestionRecvByteDrop` | `atomic.Uint64` | Bytes dropped locally (too old, duplicate, already ACK'd, etc.) |
| `PktBuf` | `CongestionRecvPktBuf` | `atomic.Uint64` | Instantaneous: packets in buffer |
| `ByteBuf` | `CongestionRecvByteBuf` | `atomic.Uint64` | Instantaneous: bytes in buffer |
| `MsBuf` | `CongestionRecvMsBuf` | `atomic.Uint64` | Instantaneous: buffer time (milliseconds) |
| `BytePayload` | `CongestionRecvBytePayload` | `atomic.Uint64` | Average payload size |
| `MbpsEstimatedRecvBandwidth` | `CongestionRecvMbpsBandwidth` | `atomic.Uint64` | Store as uint64 (Mbps * 1000) |
| `MbpsEstimatedLinkCapacity` | `CongestionRecvMbpsLinkCapacity` | `atomic.Uint64` | Store as uint64 (Mbps * 1000) |
| `PktLossRate` | `CongestionRecvPktLossRate` | `atomic.Uint64` | Store as uint64 (percentage * 100) |

| `SendStats` Field | `ConnectionMetrics` Field | Type | Notes |
|------------------|---------------------------|------|-------|
| `Pkt` | `CongestionSendPkt` | `atomic.Uint64` | Total packets sent |
| `Byte` | `CongestionSendByte` | `atomic.Uint64` | Total bytes sent |
| `PktUnique` | `CongestionSendPktUnique` | `atomic.Uint64` | Unique packets sent |
| `ByteUnique` | `CongestionSendByteUnique` | `atomic.Uint64` | Unique bytes sent |
| `PktLoss` | `CongestionSendPktLoss` | `atomic.Uint64` | Packets lost (reported via NAK from receiver) |
| `ByteLoss` | `CongestionSendByteLoss` | `atomic.Uint64` | Bytes lost (reported via NAK from receiver) |
| `PktRetrans` | `CongestionSendPktRetrans` | `atomic.Uint64` | Packets retransmitted |
| `ByteRetrans` | `CongestionSendPktRetrans` | `atomic.Uint64` | Bytes retransmitted |
| `UsSndDuration` | `CongestionSendUsSndDuration` | `atomic.Uint64` | Send duration (microseconds) |
| `PktDrop` | `CongestionSendPktDrop` | `atomic.Uint64` | Packets dropped locally (too old, serialization errors, io_uring failures, etc.) |
| `ByteDrop` | `CongestionSendByteDrop` | `atomic.Uint64` | Bytes dropped locally (too old, serialization errors, io_uring failures, etc.) |
| `PktBuf` | `CongestionSendPktBuf` | `atomic.Uint64` | Instantaneous: packets in buffer |
| `ByteBuf` | `CongestionSendByteBuf` | `atomic.Uint64` | Instantaneous: bytes in buffer |
| `MsBuf` | `CongestionSendMsBuf` | `atomic.Uint64` | Instantaneous: buffer time (milliseconds) |
| `PktFlightSize` | `CongestionSendPktFlightSize` | `atomic.Uint64` | Instantaneous: packets in flight |
| `UsPktSndPeriod` | `CongestionSendUsPktSndPeriod` | `atomic.Uint64` | Store as uint64 (microseconds) |
| `BytePayload` | `CongestionSendBytePayload` | `atomic.Uint64` | Average payload size |
| `MbpsEstimatedInputBandwidth` | `CongestionSendMbpsInputBandwidth` | `atomic.Uint64` | Store as uint64 (Mbps * 1000) |
| `MbpsEstimatedSentBandwidth` | `CongestionSendMbpsSentBandwidth` | `atomic.Uint64` | Store as uint64 (Mbps * 1000) |
| `PktLossRate` | `CongestionSendPktLossRate` | `atomic.Uint64` | Store as uint64 (percentage * 100) |

**Note**: Instantaneous values (`PktBuf`, `ByteBuf`, `MsBuf`, `PktFlightSize`) are calculated on-the-fly in `Stats()` and don't need atomic storage. However, we can still track them atomically for consistency and to enable lock-free reads.

### Locations Requiring Updates

#### 1. Add Congestion Control Fields to `ConnectionMetrics`

**File**: `metrics/metrics.go`

**Add new fields to `ConnectionMetrics` struct**:
```go
// Congestion control - Receiver statistics
CongestionRecvPkt              atomic.Uint64
CongestionRecvByte             atomic.Uint64
CongestionRecvPktUnique        atomic.Uint64
CongestionRecvByteUnique        atomic.Uint64
CongestionRecvPktLoss           atomic.Uint64
CongestionRecvByteLoss          atomic.Uint64
CongestionRecvPktRetrans        atomic.Uint64
CongestionRecvByteRetrans       atomic.Uint64
CongestionRecvPktBelated        atomic.Uint64
CongestionRecvByteBelated       atomic.Uint64
CongestionRecvPktDrop           atomic.Uint64
CongestionRecvByteDrop          atomic.Uint64
CongestionRecvPktBuf            atomic.Uint64
CongestionRecvByteBuf           atomic.Uint64
CongestionRecvMsBuf            atomic.Uint64
CongestionRecvBytePayload      atomic.Uint64
CongestionRecvMbpsBandwidth     atomic.Uint64 // Mbps * 1000
CongestionRecvMbpsLinkCapacity  atomic.Uint64 // Mbps * 1000
CongestionRecvPktLossRate       atomic.Uint64 // Percentage * 100

// Congestion control - Sender statistics
CongestionSendPkt               atomic.Uint64
CongestionSendByte              atomic.Uint64
CongestionSendPktUnique         atomic.Uint64
CongestionSendByteUnique        atomic.Uint64
CongestionSendPktLoss           atomic.Uint64
CongestionSendByteLoss          atomic.Uint64
CongestionSendPktRetrans        atomic.Uint64
CongestionSendByteRetrans       atomic.Uint64
CongestionSendUsSndDuration     atomic.Uint64
CongestionSendPktDrop           atomic.Uint64
CongestionSendByteDrop          atomic.Uint64
CongestionSendPktBuf            atomic.Uint64
CongestionSendByteBuf           atomic.Uint64
CongestionSendMsBuf             atomic.Uint64
CongestionSendPktFlightSize     atomic.Uint64
CongestionSendUsPktSndPeriod    atomic.Uint64
CongestionSendBytePayload       atomic.Uint64
CongestionSendMbpsInputBandwidth atomic.Uint64 // Mbps * 1000
CongestionSendMbpsSentBandwidth  atomic.Uint64 // Mbps * 1000
CongestionSendPktLossRate        atomic.Uint64 // Percentage * 100
```

#### 2. Pass `ConnectionMetrics` to Congestion Control

**File**: `connection.go` (in `newSRTConn()`)

**Update receiver and sender initialization**:
```go
// BEFORE:
c.recv = live.NewReceiver(live.ReceiveConfig{
    // ... config ...
})

// AFTER:
c.recv = live.NewReceiver(live.ReceiveConfig{
    // ... config ...
    ConnectionMetrics: c.metrics, // Pass metrics for atomic updates
})

// Similar for sender
c.snd = live.NewSender(live.SendConfig{
    // ... config ...
    ConnectionMetrics: c.metrics, // Pass metrics for atomic updates
})
```

**Update `ReceiveConfig` and `SendConfig` structs**:
```go
// congestion/live/receive.go
type ReceiveConfig struct {
    // ... existing fields ...
    ConnectionMetrics *metrics.ConnectionMetrics // For atomic statistics updates
}

// congestion/live/send.go
type SendConfig struct {
    // ... existing fields ...
    ConnectionMetrics *metrics.ConnectionMetrics // For atomic statistics updates
}
```

#### 3. Receiver Statistics Updates

**File**: `congestion/live/receive.go`

**3.1. `pushLocked()` Method** (Lines 164-280)

**Replace all `r.statistics.*` increments with atomic operations**:
```go
// BEFORE:
r.statistics.Pkt++
r.statistics.Byte += pktLen

// AFTER:
if r.metrics != nil {
    r.metrics.CongestionRecvPkt.Add(1)
    r.metrics.CongestionRecvByte.Add(uint64(pktLen))
}
// Old statistics struct removed - fully migrated to atomic counters
```

**All locations in `pushLocked()`**:
- Line 198: `r.statistics.Pkt++` → `r.metrics.CongestionRecvPkt.Add(1)`
- Line 199: `r.statistics.Byte += pktLen` → `r.metrics.CongestionRecvByte.Add(uint64(pktLen))`
- Line 203: `r.statistics.PktRetrans++` → `r.metrics.CongestionRecvPktRetrans.Add(1)`
- Line 204: `r.statistics.ByteRetrans += pktLen` → `r.metrics.CongestionRecvByteRetrans.Add(uint64(pktLen))`
- Line 214: `r.statistics.PktBelated++` → `r.metrics.CongestionRecvPktBelated.Add(1)`
- Line 215: `r.statistics.ByteBelated += pktLen` → `r.metrics.CongestionRecvByteBelated.Add(uint64(pktLen))`
- Line 217: `r.statistics.PktDrop++` → `r.metrics.CongestionRecvPktDrop.Add(1)`
- Line 218: `r.statistics.ByteDrop += pktLen` → `r.metrics.CongestionRecvByteDrop.Add(uint64(pktLen))`
- Line 225: `r.statistics.PktDrop++` → `r.metrics.CongestionRecvPktDrop.Add(1)`
- Line 226: `r.statistics.ByteDrop += pktLen` → `r.metrics.CongestionRecvByteDrop.Add(uint64(pktLen))`
- Line 238: `r.statistics.PktDrop++` → `r.metrics.CongestionRecvPktDrop.Add(1)`
- Line 239: `r.statistics.ByteDrop += pktLen` → `r.metrics.CongestionRecvByteDrop.Add(uint64(pktLen))`
- Line 246: `r.statistics.PktBuf++` → `r.metrics.CongestionRecvPktBuf.Add(1)`
- Line 247: `r.statistics.PktUnique++` → `r.metrics.CongestionRecvPktUnique.Add(1)`
- Line 249: `r.statistics.ByteBuf += pktLen` → `r.metrics.CongestionRecvByteBuf.Add(uint64(pktLen))`
- Line 250: `r.statistics.ByteUnique += pktLen` → `r.metrics.CongestionRecvByteUnique.Add(uint64(pktLen))`
- Line 253: `r.statistics.PktDrop++` → `r.metrics.CongestionRecvPktDrop.Add(1)`
- Line 254: `r.statistics.ByteDrop += pktLen` → `r.metrics.CongestionRecvByteDrop.Add(uint64(pktLen))`
- Line 267: `r.statistics.PktLoss += len` → `r.metrics.CongestionRecvPktLoss.Add(len)`
- Line 268: `r.statistics.ByteLoss += len * uint64(r.avgPayloadSize)` → `r.metrics.CongestionRecvByteLoss.Add(len * uint64(r.avgPayloadSize))`
- Line 273: `r.statistics.PktBuf++` → `r.metrics.CongestionRecvPktBuf.Add(1)`
- Line 274: `r.statistics.PktUnique++` → `r.metrics.CongestionRecvPktUnique.Add(1)`
- Line 276: `r.statistics.ByteBuf += pktLen` → `r.metrics.CongestionRecvByteBuf.Add(uint64(pktLen))`
- Line 277: `r.statistics.ByteUnique += pktLen` → `r.metrics.CongestionRecvByteUnique.Add(uint64(pktLen))`

**3.2. `Tick()` Method** (Lines 436-501)

**Update buffer decrements**:
- Line 456: `r.statistics.PktBuf--` → `r.metrics.CongestionRecvPktBuf.Add(^uint64(0))` (decrement via add of max)
- Line 457: `r.statistics.ByteBuf -= p.Len()` → `r.metrics.CongestionRecvByteBuf.Add(^uint64(p.Len() - 1))` (decrement)
- Line 477: `r.statistics.PktBuf--` → `r.metrics.CongestionRecvPktBuf.Add(^uint64(0))`
- Line 478: `r.statistics.ByteBuf -= p.Len()` → `r.metrics.CongestionRecvByteBuf.Add(^uint64(p.Len() - 1))`

**Note**: For decrements, we can use a helper function or subtract directly. However, atomic doesn't have `Sub()` for `Uint64`, so we use `Add(^uint64(0))` for decrement by 1, or `Add(^uint64(n-1))` for decrement by n. Alternatively, we can store the current value, subtract, and use `CompareAndSwap`, but that's more complex. The simplest approach is to use signed atomic operations or track increments/decrements separately.

**Better approach**: Use `atomic.AddUint64` with two's complement for subtraction:
```go
// Decrement by 1
r.metrics.CongestionRecvPktBuf.Add(^uint64(0))  // Add max uint64 = subtract 1

// Decrement by n
r.metrics.CongestionRecvByteBuf.Add(^uint64(p.Len() - 1))  // Add complement
```

**3.3. `Stats()` Method** (Lines 122-132)

**Update to read from atomic counters and calculate instantaneous values**:
```go
// BEFORE:
func (r *receiver) Stats() congestion.ReceiveStats {
    r.lock.Lock()
    defer r.lock.Unlock()

    r.statistics.BytePayload = uint64(r.avgPayloadSize)
    r.statistics.MbpsEstimatedRecvBandwidth = r.rate.bytesPerSecond * 8 / 1024 / 1024
    r.statistics.MbpsEstimatedLinkCapacity = r.avgLinkCapacity * packet.MAX_PAYLOAD_SIZE * 8 / 1024 / 1024
    r.statistics.PktLossRate = r.rate.pktLossRate

    return r.statistics
}

// AFTER:
func (r *receiver) Stats() congestion.ReceiveStats {
    // Read lock only for rate calculations (not for statistics)
    r.lock.RLock()
    bytePayload := uint64(r.avgPayloadSize)
    mbpsBandwidth := r.rate.bytesPerSecond * 8 / 1024 / 1024
    mbpsLinkCapacity := r.avgLinkCapacity * packet.MAX_PAYLOAD_SIZE * 8 / 1024 / 1024
    pktLossRate := r.rate.pktLossRate
    r.lock.RUnlock()

    // Update atomic counters for instantaneous/calculated values
    if r.metrics != nil {
        r.metrics.CongestionRecvBytePayload.Store(bytePayload)
        r.metrics.CongestionRecvMbpsBandwidth.Store(uint64(mbpsBandwidth * 1000))
        r.metrics.CongestionRecvMbpsLinkCapacity.Store(uint64(mbpsLinkCapacity * 1000))
        r.metrics.CongestionRecvPktLossRate.Store(uint64(pktLossRate * 100))
    }

    // Build return struct from atomic counters (lock-free reads)
    return congestion.ReceiveStats{
        Pkt:                      r.metrics.CongestionRecvPkt.Load(),
        Byte:                     r.metrics.CongestionRecvByte.Load(),
        PktUnique:                r.metrics.CongestionRecvPktUnique.Load(),
        ByteUnique:               r.metrics.CongestionRecvByteUnique.Load(),
        PktLoss:                  r.metrics.CongestionRecvPktLoss.Load(),
        ByteLoss:                 r.metrics.CongestionRecvByteLoss.Load(),
        PktRetrans:               r.metrics.CongestionRecvPktRetrans.Load(),
        ByteRetrans:              r.metrics.CongestionRecvByteRetrans.Load(),
        PktBelated:               r.metrics.CongestionRecvPktBelated.Load(),
        ByteBelated:              r.metrics.CongestionRecvByteBelated.Load(),
        PktDrop:                  r.metrics.CongestionRecvPktDrop.Load(),
        ByteDrop:                 r.metrics.CongestionRecvByteDrop.Load(),
        PktBuf:                   r.metrics.CongestionRecvPktBuf.Load(),
        ByteBuf:                  r.metrics.CongestionRecvByteBuf.Load(),
        MsBuf:                    r.metrics.CongestionRecvMsBuf.Load(),
        BytePayload:              bytePayload,
        MbpsEstimatedRecvBandwidth: mbpsBandwidth,
        MbpsEstimatedLinkCapacity:  mbpsLinkCapacity,
        PktLossRate:              pktLossRate,
    }
}
```

**3.4. `periodicACK()` Method** (Line 379)

**Update `MsBuf` calculation**:
```go
// BEFORE:
r.statistics.MsBuf = (maxPktTsbpdTime - minPktTsbpdTime) / 1_000

// AFTER:
msBuf := (maxPktTsbpdTime - minPktTsbpdTime) / 1_000
if r.metrics != nil {
    r.metrics.CongestionRecvMsBuf.Store(msBuf)
}
// Old statistics struct removed - fully migrated to atomic counters
```

#### 4. Sender Statistics Updates

**File**: `congestion/live/send.go`

**4.1. `pushLocked()` Method** (Lines 136-175)

**Replace statistics increments**:
- Line 151: `s.statistics.PktBuf++` → `s.metrics.CongestionSendPktBuf.Add(1)`
- Line 152: `s.statistics.ByteBuf += pktLen` → `s.metrics.CongestionSendByteBuf.Add(uint64(pktLen))`
- Line 174: `s.statistics.PktFlightSize = uint64(s.packetList.Len())` → `s.metrics.CongestionSendPktFlightSize.Store(uint64(s.packetList.Len()))`

**4.2. `tickDeliverPackets()` Method** (Lines 208-239)

**Replace statistics increments**:
- Line 213: `s.statistics.Pkt++` → `s.metrics.CongestionSendPkt.Add(1)`
- Line 214: `s.statistics.PktUnique++` → `s.metrics.CongestionSendPktUnique.Add(1)`
- Line 218: `s.statistics.Byte += pktLen` → `s.metrics.CongestionSendByte.Add(uint64(pktLen))`
- Line 219: `s.statistics.ByteUnique += pktLen` → `s.metrics.CongestionSendByteUnique.Add(uint64(pktLen))`
- Line 221: `s.statistics.UsSndDuration += uint64(s.pktSndPeriod)` → `s.metrics.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))`

**4.3. `tickDropOldPackets()` Method** (Lines 241-269)

**Replace statistics increments**:
- Line 248: `s.statistics.PktDrop++` → `s.metrics.CongestionSendPktDrop.Add(1)`
- **Note**: `PktLoss` should NOT be incremented here - this is a local drop (too old), not a loss. Loss is only incremented when NAK is received (see `nakLocked()`).
- Line 250: `s.statistics.ByteDrop += p.Len()` → `s.metrics.CongestionSendByteDrop.Add(uint64(p.Len()))`
- **Note**: `ByteLoss` should NOT be incremented here - this is a local drop, not a loss.
- Line 261: `s.statistics.PktBuf--` → `s.metrics.CongestionSendPktBuf.Add(^uint64(0))`
- Line 262: `s.statistics.ByteBuf -= p.Len()` → `s.metrics.CongestionSendByteBuf.Add(^uint64(p.Len() - 1))`

**4.4. `ackLocked()` Method** (Lines 303-329)

**Replace buffer decrements**:
- Line 319: `s.statistics.PktBuf--` → `s.metrics.CongestionSendPktBuf.Add(^uint64(0))`
- Line 320: `s.statistics.ByteBuf -= p.Len()` → `s.metrics.CongestionSendByteBuf.Add(^uint64(p.Len() - 1))`

**4.5. `nakLocked()` Method** (Lines 349-388)

**Replace statistics increments**:
- **First, count ALL packets in NAK list (all reported losses)**:
  - For each range in `sequenceNumbers` (pairs of [start, end]), count packets: `lossCount = end.Distance(start) + 1`
  - Increment `CongestionSendPktLoss` by total loss count (all packets in NAK)
  - Increment `CongestionSendByteLoss` by estimated bytes (lossCount * avgPayloadSize)
- **Then, retransmit packets we can find**:
  - Line 357: `s.statistics.PktRetrans++` → `s.metrics.CongestionSendPktRetrans.Add(1)` (for each retransmitted packet)
  - Line 358: `s.statistics.Pkt++` → `s.metrics.CongestionSendPkt.Add(1)` (for each retransmitted packet)
  - Line 361: `s.statistics.ByteRetrans += p.Len()` → `s.metrics.CongestionSendByteRetrans.Add(uint64(p.Len()))` (for each retransmitted packet)
  - Line 362: `s.statistics.Byte += p.Len()` → `s.metrics.CongestionSendByte.Add(uint64(p.Len()))` (for each retransmitted packet)
- **Note**: `PktLoss` is incremented for ALL packets in NAK (reported losses), not just retransmitted ones. Some packets may not be retransmitted if they're no longer in the buffer.

**4.6. `Stats()` Method** (Lines 93-112)

**Update to read from atomic counters**:
```go
// BEFORE:
func (s *sender) Stats() congestion.SendStats {
    s.lock.Lock()
    defer s.lock.Unlock()

    s.statistics.UsPktSndPeriod = s.pktSndPeriod
    s.statistics.BytePayload = uint64(s.avgPayloadSize)
    s.statistics.MsBuf = 0
    // ... calculate MsBuf ...
    s.statistics.MbpsEstimatedInputBandwidth = s.rate.estimatedInputBW * 8 / 1024 / 1024
    s.statistics.MbpsEstimatedSentBandwidth = s.rate.estimatedSentBW * 8 / 1024 / 1024
    s.statistics.PktLossRate = s.rate.pktLossRate

    return s.statistics
}

// AFTER:
func (s *sender) Stats() congestion.SendStats {
    // Read lock only for rate calculations
    s.lock.RLock()
    usPktSndPeriod := s.pktSndPeriod
    bytePayload := uint64(s.avgPayloadSize)
    msBuf := uint64(0)
    max := s.lossList.Back()
    min := s.lossList.Front()
    if max != nil && min != nil {
        msBuf = (max.Value.(packet.Packet).Header().PktTsbpdTime - min.Value.(packet.Packet).Header().PktTsbpdTime) / 1_000
    }
    mbpsInputBW := s.rate.estimatedInputBW * 8 / 1024 / 1024
    mbpsSentBW := s.rate.estimatedSentBW * 8 / 1024 / 1024
    pktLossRate := s.rate.pktLossRate
    s.lock.RUnlock()

    // Update atomic counters for instantaneous/calculated values
    if s.metrics != nil {
        s.metrics.CongestionSendUsPktSndPeriod.Store(uint64(usPktSndPeriod))
        s.metrics.CongestionSendBytePayload.Store(bytePayload)
        s.metrics.CongestionSendMsBuf.Store(msBuf)
        s.metrics.CongestionSendMbpsInputBandwidth.Store(uint64(mbpsInputBW * 1000))
        s.metrics.CongestionSendMbpsSentBandwidth.Store(uint64(mbpsSentBW * 1000))
        s.metrics.CongestionSendPktLossRate.Store(uint64(pktLossRate * 100))
    }

    // Build return struct from atomic counters (lock-free reads)
    return congestion.SendStats{
        Pkt:                        s.metrics.CongestionSendPkt.Load(),
        Byte:                       s.metrics.CongestionSendByte.Load(),
        PktUnique:                  s.metrics.CongestionSendPktUnique.Load(),
        ByteUnique:                 s.metrics.CongestionSendByteUnique.Load(),
        PktLoss:                    s.metrics.CongestionSendPktLoss.Load(),
        ByteLoss:                   s.metrics.CongestionSendByteLoss.Load(),
        PktRetrans:                 s.metrics.CongestionSendPktRetrans.Load(),
        ByteRetrans:                s.metrics.CongestionSendByteRetrans.Load(),
        UsSndDuration:              s.metrics.CongestionSendUsSndDuration.Load(),
        PktDrop:                    s.metrics.CongestionSendPktDrop.Load(),
        ByteDrop:                   s.metrics.CongestionSendByteDrop.Load(),
        PktBuf:                     s.metrics.CongestionSendPktBuf.Load(),
        ByteBuf:                    s.metrics.CongestionSendByteBuf.Load(),
        MsBuf:                      msBuf,
        PktFlightSize:              s.metrics.CongestionSendPktFlightSize.Load(),
        UsPktSndPeriod:             usPktSndPeriod,
        BytePayload:                bytePayload,
        MbpsEstimatedInputBandwidth: mbpsInputBW,
        MbpsEstimatedSentBandwidth:  mbpsSentBW,
        PktLossRate:                pktLossRate,
    }
}
```

#### 5. Update `connection.go:Stats()` to Use Atomic Counters

**File**: `connection.go` (Lines 1823-1953)

**Replace congestion control statistics reads**:
```go
// BEFORE:
send := c.snd.Stats()  // Acquires lock internally
recv := c.recv.Stats() // Acquires lock internally

s.Accumulated = StatisticsAccumulated{
    PktSent:           send.Pkt,
    PktRecv:           recv.Pkt,
    // ... etc
}

// AFTER:
// Read directly from atomic counters (lock-free)
s.Accumulated = StatisticsAccumulated{
    PktSent:           c.metrics.CongestionSendPkt.Load(),
    PktRecv:           c.metrics.CongestionRecvPkt.Load(),
    PktSentUnique:     c.metrics.CongestionSendPktUnique.Load(),
    PktRecvUnique:     c.metrics.CongestionRecvPktUnique.Load(),
    PktSendLoss:       c.metrics.CongestionSendPktLoss.Load(),
    PktRecvLoss:       c.metrics.CongestionRecvPktLoss.Load(),
    PktRetrans:        c.metrics.CongestionSendPktRetrans.Load(),
    PktRecvRetrans:    c.metrics.CongestionRecvPktRetrans.Load(),
    UsSndDuration:     c.metrics.CongestionSendUsSndDuration.Load(),
    PktSendDrop:       c.metrics.CongestionSendPktDrop.Load(),
    PktRecvDrop:       c.metrics.CongestionRecvPktDrop.Load(),
    ByteSent:          c.metrics.CongestionSendByte.Load() + (c.metrics.CongestionSendPkt.Load() * headerSize),
    ByteRecv:          c.metrics.CongestionRecvByte.Load() + (c.metrics.CongestionRecvPkt.Load() * headerSize),
    // ... etc
}
```

**Note**: We still need to call `c.snd.Stats()` and `c.recv.Stats()` to update instantaneous values (like `MsBuf`, `MbpsEstimatedBandwidth`, etc.), but we can read the cumulative counters directly from atomic variables.

**Alternative approach**: Keep calling `Stats()` but make it lock-free by reading from atomic counters. The `Stats()` methods will still need locks for rate calculations, but the statistics reads will be lock-free.

### Migration Strategy

**Phase 1: Add Atomic Counters (Parallel)**
- Add congestion control fields to `ConnectionMetrics`
- Pass `ConnectionMetrics` to receiver and sender
- Increment both old and new counters during transition
- No breaking changes

**Phase 2: Update Statistics Reads**
- Update `Stats()` methods to read from atomic counters (lock-free)
- Update `connection.go:Stats()` to use atomic counters directly
- Fully migrate to atomic-based system

**Phase 3: Remove Old Statistics Structs**
- Remove `r.statistics` and `s.statistics` fields completely
- Remove all locks from `Stats()` methods (fully lock-free)
- Clean up all old increment code

**Phase 4: Optimize Lock Usage**
- Reduce lock scope in `Stats()` methods (only for rate calculations)
- Consider removing locks entirely if rate calculations can be made lock-free

### Benefits After Migration

- ✅ **No lock contention**: All statistics operations are lock-free
- ✅ **Better performance**: Atomic operations are faster than mutex locks
- ✅ **Simpler code**: No need to manage locks for statistics
- ✅ **Thread-safe**: Atomic operations are inherently thread-safe
- ✅ **Consistent**: All statistics use the same atomic counter pattern
- ✅ **Lock-free reads**: `Stats()` calls don't require locks (except for rate calculations)

### Challenges and Solutions

**Challenge 1: Decrement Operations**
- **Problem**: `atomic.Uint64` doesn't have `Sub()` method
- **Solution**: Use two's complement arithmetic: `Add(^uint64(0))` for decrement by 1, `Add(^uint64(n-1))` for decrement by n
- **Alternative 1**: Use signed `atomic.Int64` for values that can decrease (but requires type change)
- **Alternative 2**: Add helper functions in `metrics/helpers.go` for clarity:
  ```go
  // DecrementUint64 decrements an atomic uint64 by 1
  func DecrementUint64(addr *atomic.Uint64) {
      addr.Add(^uint64(0))  // Add max uint64 = subtract 1
  }

  // SubtractUint64 subtracts n from an atomic uint64
  func SubtractUint64(addr *atomic.Uint64, n uint64) {
      addr.Add(^uint64(n - 1))  // Add complement
  }
  ```
- **Recommendation**: Use helper functions for clarity and maintainability

**Challenge 2: Instantaneous Values**
- **Problem**: `PktBuf`, `ByteBuf`, `MsBuf` are calculated on-the-fly
- **Solution**: Update atomic counters when these values change (increment/decrement), and read them atomically in `Stats()`

**Challenge 3: Rate Calculations Still Need Locks**
- **Problem**: `MbpsEstimatedBandwidth`, `PktLossRate` require reading `r.rate` struct
- **Solution**: Keep read locks for rate calculations only, make statistics reads lock-free

### Summary of Changes

**Total Locations to Update**: ~60 locations

1. **Add fields to `ConnectionMetrics`**: ~40 new atomic counter fields
2. **Receiver statistics increments**: ~20 locations in `pushLocked()`, `Tick()`, `periodicACK()`
3. **Sender statistics increments**: ~15 locations in `pushLocked()`, `tickDeliverPackets()`, `tickDropOldPackets()`, `ackLocked()`, `nakLocked()`
4. **Statistics reads**: 2 methods (`receiver.Stats()`, `sender.Stats()`) - now fully lock-free
5. **Connection statistics aggregation**: 1 method (`connection.go:Stats()`) - now fully lock-free
6. **Remove old statistics structs**: Remove `r.statistics` and `s.statistics` fields completely

**Performance Improvement**: All statistics operations are now lock-free, eliminating contention in the hot path

### Granular Drop Counters Design (Option B: Full Granularity)

**Design Decision**: Implement granular drop counters for ALL drop points, with separate counters for DATA vs control packets. This provides complete visibility into which specific drop types are occurring, while maintaining aggregate counters for SRT RFC compliance.

**Key Principles**:
1. **Granular Counters**: One counter per drop reason (for debugging)
2. **Aggregate Counter**: Sum of all granular counters (for SRT RFC compliance)
3. **DATA vs Control**: Separate counters for DATA and control packets (control drops are more serious)
4. **Helper Functions**: Use helper functions to ensure granular + aggregate stay in sync

#### Granular Drop Counter Structure

**Congestion Control - Receiver (DATA packets only)**:
- `CongestionRecvDataDropTooOld` - Belated, past play time
- `CongestionRecvDataDropAlreadyAcked` - Already acknowledged
- `CongestionRecvDataDropDuplicate` - Duplicate (already in store)
- `CongestionRecvDataDropStoreInsertFailed` - Store insert failed

**Congestion Control - Sender (DATA packets only)**:
- `CongestionSendDataDropTooOld` - Exceed drop threshold

**Connection-Level - Receive (DATA and Control packets)**:
- `PktRecvDataErrorParse` - DATA packet parse errors
- `PktRecvControlErrorParse` - Control packet parse errors
- `PktRecvDataErrorIoUring` - DATA packet io_uring errors
- `PktRecvControlErrorIoUring` - Control packet io_uring errors
- `PktRecvDataErrorEmpty` - DATA packet empty datagrams
- `PktRecvControlErrorEmpty` - Control packet empty datagrams
- `PktRecvDataErrorRoute` - DATA packet routing failures
- `PktRecvControlErrorRoute` - Control packet routing failures

**Connection-Level - Send (DATA and Control packets)**:
- `PktSentDataErrorMarshal` - DATA packet marshal errors
- `PktSentControlErrorMarshal` - Control packet marshal errors
- `PktSentDataRingFull` - DATA packet ring full
- `PktSentControlRingFull` - Control packet ring full
- `PktSentDataErrorSubmit` - DATA packet submit errors
- `PktSentControlErrorSubmit` - Control packet submit errors
- `PktSentDataErrorIoUring` - DATA packet io_uring completion errors
- `PktSentControlErrorIoUring` - Control packet io_uring completion errors

#### Aggregate Counter Calculation

The aggregate counters (`CongestionRecvPktDrop`, `CongestionSendPktDrop`) will be calculated from granular counters in `Stats()` methods:

```go
// Receiver aggregate
PktRecvDrop = CongestionRecvDataDropTooOld +
              CongestionRecvDataDropAlreadyAcked +
              CongestionRecvDataDropDuplicate +
              CongestionRecvDataDropStoreInsertFailed

// Sender aggregate (DATA packets only)
PktSendDrop = CongestionSendDataDropTooOld +
              PktSentDataErrorMarshal +
              PktSentDataRingFull +
              PktSentDataErrorSubmit +
              PktSentDataErrorIoUring
```

**Note**: Control packet drops are tracked separately and are NOT included in the aggregate `PktSendDrop` / `PktRecvDrop` counters, as these are SRT RFC counters for DATA packets only.

#### Helper Functions

Helper functions will be created to increment both granular and aggregate counters atomically:

```go
// metrics/helpers.go

// IncrementRecvDataDrop increments both granular and aggregate drop counters for receiver
func IncrementRecvDataDrop(m *ConnectionMetrics, reason string, pktLen uint64) {
    if m == nil {
        return
    }

    // Increment granular counter
    switch reason {
    case "too_old":
        m.CongestionRecvDataDropTooOld.Add(1)
    case "already_acked":
        m.CongestionRecvDataDropAlreadyAcked.Add(1)
    case "duplicate":
        m.CongestionRecvDataDropDuplicate.Add(1)
    case "store_insert_failed":
        m.CongestionRecvDataDropStoreInsertFailed.Add(1)
    }

    // Always increment aggregate
    m.CongestionRecvPktDrop.Add(1)
    m.CongestionRecvByteDrop.Add(pktLen)
}

// IncrementSendDataDrop increments both granular and aggregate drop counters for sender
func IncrementSendDataDrop(m *ConnectionMetrics, reason string, pktLen uint64) {
    if m == nil {
        return
    }

    // Increment granular counter
    switch reason {
    case "too_old":
        m.CongestionSendDataDropTooOld.Add(1)
    }

    // Always increment aggregate
    m.CongestionSendPktDrop.Add(1)
    m.CongestionSendByteDrop.Add(pktLen)
}

// IncrementSendErrorDrop increments granular error counters and aggregate for DATA packets
func IncrementSendErrorDrop(m *ConnectionMetrics, p packet.Packet, reason string, pktLen uint64) {
    if m == nil {
        return
    }

    isData := p != nil && !p.Header().IsControlPacket

    // Increment granular counter based on packet type
    switch reason {
    case "marshal":
        if isData {
            m.PktSentDataErrorMarshal.Add(1)
            m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
            m.CongestionSendByteDrop.Add(pktLen)
        } else {
            m.PktSentControlErrorMarshal.Add(1)
        }
    case "ring_full":
        if isData {
            m.PktSentDataRingFull.Add(1)
            m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
            m.CongestionSendByteDrop.Add(pktLen)
        } else {
            m.PktSentControlRingFull.Add(1)
        }
    case "submit":
        if isData {
            m.PktSentDataErrorSubmit.Add(1)
            m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
            m.CongestionSendByteDrop.Add(pktLen)
        } else {
            m.PktSentControlErrorSubmit.Add(1)
        }
    case "iouring":
        if isData {
            m.PktSentDataErrorIoUring.Add(1)
            m.CongestionSendPktDrop.Add(1) // Aggregate for DATA only
            m.CongestionSendByteDrop.Add(pktLen)
        } else {
            m.PktSentControlErrorIoUring.Add(1)
        }
    }
}
```

#### Implementation Locations

**Congestion Control - Receiver** (`congestion/live/receive.go`):
- Line 283: Too old → `IncrementRecvDataDrop(m, "too_old", pktLen)`
- Line 298: Already ACK'd → `IncrementRecvDataDrop(m, "already_acked", pktLen)`
- Line 315: Duplicate → `IncrementRecvDataDrop(m, "duplicate", pktLen)`
- Line 341: Store insert failed → `IncrementRecvDataDrop(m, "store_insert_failed", pktLen)`

**Congestion Control - Sender** (`congestion/live/send.go`):
- Line 329: Too old → `IncrementSendDataDrop(m, "too_old", pktLen)`

**Connection-Level - Send** (`connection_linux.go`, `connection.go`, `listen.go`, `dial.go`):
- Marshal errors → `IncrementSendErrorDrop(m, p, "marshal", pktLen)`
- Ring full → `IncrementSendErrorDrop(m, p, "ring_full", pktLen)`
- Submit errors → `IncrementSendErrorDrop(m, p, "submit", pktLen)`
- io_uring errors → `IncrementSendErrorDrop(m, p, "iouring", pktLen)`

**Connection-Level - Receive** (`listen_linux.go`, `dial_linux.go`, `listen.go`, `dial.go`):
- Parse errors → Check packet type, increment `PktRecvDataErrorParse` or `PktRecvControlErrorParse`
- io_uring errors → Check packet type, increment `PktRecvDataErrorIoUring` or `PktRecvControlErrorIoUring`
- Empty datagrams → Check packet type, increment `PktRecvDataErrorEmpty` or `PktRecvControlErrorEmpty`
- Routing failures → Check packet type, increment `PktRecvDataErrorRoute` or `PktRecvControlErrorRoute`

**Note**: For receive path, we may not have a packet object when errors occur (parse failures, empty datagrams). In these cases, we'll need to track the error type separately and attempt to classify based on context (e.g., handshake packets are control, data packets are DATA).

#### Stats() Method Updates

**Receiver Stats()** (`congestion/live/receive.go`):
```go
PktDrop: CongestionRecvDataDropTooOld.Load() +
         CongestionRecvDataDropAlreadyAcked.Load() +
         CongestionRecvDataDropDuplicate.Load() +
         CongestionRecvDataDropStoreInsertFailed.Load()
```

**Sender Stats()** (`congestion/live/send.go`):
```go
PktDrop: CongestionSendDataDropTooOld.Load()
```

**Connection Stats()** (`connection.go`):
```go
PktSendDrop: CongestionSendDataDropTooOld.Load() +
             PktSentDataErrorMarshal.Load() +
             PktSentDataRingFull.Load() +
             PktSentDataErrorSubmit.Load() +
             PktSentDataErrorIoUring.Load()

PktRecvDrop: CongestionRecvDataDropTooOld.Load() +
             CongestionRecvDataDropAlreadyAcked.Load() +
             CongestionRecvDataDropDuplicate.Load() +
             CongestionRecvDataDropStoreInsertFailed.Load()
```

#### Prometheus Metrics

Granular counters will be exposed in Prometheus with labels:

```
gosrt_connection_congestion_recv_data_drop_total{reason="too_old",socket_id="..."} 10
gosrt_connection_congestion_recv_data_drop_total{reason="already_acked",socket_id="..."} 5
gosrt_connection_congestion_recv_data_drop_total{reason="duplicate",socket_id="..."} 2
gosrt_connection_congestion_recv_data_drop_total{reason="store_insert_failed",socket_id="..."} 1
gosrt_connection_congestion_send_data_drop_total{reason="too_old",socket_id="..."} 3
gosrt_connection_send_data_drop_total{reason="marshal",socket_id="..."} 0
gosrt_connection_send_data_drop_total{reason="ring_full",socket_id="..."} 0
gosrt_connection_send_data_drop_total{reason="submit",socket_id="..."} 0
gosrt_connection_send_control_drop_total{reason="marshal",socket_id="..."} 0
gosrt_connection_send_control_drop_total{reason="ring_full",socket_id="..."} 0
```

---

### Additional Error and Drop Counters for Congestion Control

After reviewing the congestion control code paths, we identified several error conditions and failure cases that are not currently tracked. These should be added to provide complete visibility into congestion control behavior.

#### Missing Counters in Receiver

**1. Nil Packet Handling** (`receive.go:165`)
- **Current**: `if pkt == nil { return }` - silently ignored
- **Proposed**: `CongestionRecvPktNil` - count nil packets received
- **Rationale**: Indicates upstream issues (packet parsing failures, memory issues)

**2. Packet Store Insertion Failures** (`receive.go:244`)
- **Current**: `Insert()` returns `false` for duplicates, tracked as generic `PktDrop`
- **Proposed**: `CongestionRecvPktStoreInsertFailed` - count insertion failures
- **Rationale**: Distinguish duplicate packets from other drop reasons
- **Note**: Currently `Insert()` only fails for duplicates (after `Has()` check), but this could change if we add capacity limits

**3. Delivery Failures** (`receive.go:463, 484`)
- **Current**: `r.deliver(p)` called, but no tracking if delivery fails
- **Proposed**: `CongestionRecvDeliveryFailed` - count delivery callback failures
- **Rationale**: `OnDeliver` callback could fail (e.g., application buffer full, write error)
- **Implementation**: Wrap `OnDeliver` call in try-catch or check return value if callback is modified to return error

**4. Packet Store Capacity** (Future)
- **Current**: No capacity limit on packet store
- **Proposed**: `CongestionRecvPktStoreFull` - count packets dropped due to store capacity
- **Rationale**: If we add capacity limits in the future, track when store is full

#### Missing Counters in Sender

**1. Delivery Failures** (`send.go:228, 372`)
- **Current**: `s.deliver(p)` called, but no tracking if delivery fails
- **Proposed**: `CongestionSendDeliveryFailed` - count delivery callback failures
- **Rationale**: `OnDeliver` callback could fail (e.g., network write error, buffer full)
- **Implementation**: Wrap `OnDeliver` call in try-catch or check return value if callback is modified to return error

**2. NAK Requests for Missing Packets** (`send.go:349-388`)
- **Current**: `nakLocked()` only retransmits packets in `lossList`
- **Proposed**: `CongestionSendNAKNotFound` - count NAK requests for packets not in lossList
- **Rationale**: Indicates packets were already ACK'd and removed, or never sent
- **Implementation**: Track when NAK sequence number is not found in lossList

**3. Buffer Capacity** (Future)
- **Current**: No capacity limit on `packetList` or `lossList`
- **Proposed**: `CongestionSendBufferFull` - count packets dropped due to buffer capacity
- **Rationale**: If we add capacity limits in the future, track when buffer is full

#### Additional Counters to Add

**Receiver**:
```go
CongestionRecvPktNil              atomic.Uint64  // Nil packets received
CongestionRecvPktStoreInsertFailed atomic.Uint64 // Packet store insertion failures
CongestionRecvDeliveryFailed      atomic.Uint64  // Delivery callback failures
CongestionRecvPktStoreFull       atomic.Uint64  // Packets dropped due to store capacity (future)
```

**Sender**:
```go
CongestionSendDeliveryFailed      atomic.Uint64  // Delivery callback failures
CongestionSendNAKNotFound         atomic.Uint64  // NAK requests for packets not in lossList
CongestionSendBufferFull          atomic.Uint64  // Packets dropped due to buffer capacity (future)
```

### Latency Measurements for Congestion Control

Currently, the design includes lock timing metrics, but we should also track packet processing latencies to understand end-to-end performance. These measurements help identify bottlenecks and optimize the congestion control layer.

#### Proposed Latency Metrics

**1. Packet Processing Latency (Receiver)**
- **Metric**: `CongestionRecvProcessingLatencyNs` (nanoseconds)
- **Definition**: Time from `Push()` call to packet delivery (via `OnDeliver`)
- **Measurement**: Record timestamp in `Push()`, calculate latency in `Tick()` when delivering
- **Use Case**: Identify slow packet processing, buffer delays
- **Implementation**: Use `LockTimingMetrics` pattern with ring buffer for recent samples

**2. Buffer Latency (Receiver)**
- **Metric**: `CongestionRecvBufferLatencyNs` (nanoseconds)
- **Definition**: Time packets spend in buffer (from insertion to delivery)
- **Measurement**: `PktTsbpdTime - now` when packet is inserted, actual latency when delivered
- **Use Case**: Monitor buffer delays, identify late packets
- **Implementation**: Track per-packet or aggregate statistics

**3. Retransmission Latency (Sender)**
- **Metric**: `CongestionSendRetransLatencyNs` (nanoseconds)
- **Definition**: Time from NAK receipt to packet retransmission
- **Measurement**: Record timestamp when NAK received, calculate when packet retransmitted
- **Use Case**: Monitor retransmission performance, identify slow retransmissions
- **Implementation**: Track in `nakLocked()` method

**4. NAK Response Time (Receiver)**
- **Metric**: `CongestionRecvNAKResponseTimeNs` (nanoseconds)
- **Definition**: Time from detecting gap to sending NAK
- **Measurement**: Record timestamp when gap detected, calculate when NAK sent
- **Use Case**: Monitor NAK generation performance
- **Implementation**: Track in `pushLocked()` when gap detected, in `periodicNAK()` when NAK sent

**5. ACK Generation Latency (Receiver)**
- **Metric**: `CongestionRecvACKLatencyNs` (nanoseconds)
- **Definition**: Time from packet receipt to ACK generation
- **Measurement**: Record timestamp in `Push()`, calculate when ACK sent
- **Use Case**: Monitor ACK generation performance
- **Implementation**: Track in `periodicACK()` method

#### Latency Metrics Structure

**Add to `ConnectionMetrics`**:
```go
// Congestion control latency metrics (using LockTimingMetrics pattern)
CongestionRecvProcessingLatency *LockTimingMetrics  // Push to delivery
CongestionRecvBufferLatency     *LockTimingMetrics  // Buffer time
CongestionSendRetransLatency   *LockTimingMetrics  // NAK to retransmission
CongestionRecvNAKResponseTime  *LockTimingMetrics  // Gap detection to NAK
CongestionRecvACKLatency       *LockTimingMetrics  // Packet receipt to ACK
```

**Implementation Pattern**:
- Use same `LockTimingMetrics` structure (ring buffer with atomic values)
- Record timestamps at key points, calculate latency when events occur
- Store average and max values for each latency type
- Expose via Prometheus as `gosrt_connection_congestion_*_latency_seconds_avg` and `_max`

**Example Implementation**:
```go
// In receiver.pushLocked()
pushTime := time.Now()
// ... process packet ...

// In receiver.Tick() when delivering
if r.metrics != nil && r.metrics.CongestionRecvProcessingLatency != nil {
    latency := time.Since(pushTime)
    r.metrics.CongestionRecvProcessingLatency.RecordHoldTime(latency)
}
```

**Note**: For per-packet latency tracking, we may need to store timestamps with packets or use a different approach (e.g., aggregate statistics rather than per-packet).

### Prometheus Metrics for Congestion Control

The congestion control statistics should be exposed in Prometheus alongside connection-level metrics. This provides complete visibility into packet flow, drops, losses, and latencies.

#### Metric Naming Convention

**Format**: `gosrt_connection_congestion_{direction}_{metric}_{unit}`

- **Direction**: `recv`, `send`
- **Metric**: `packets_total`, `bytes_total`, `packets_dropped_total`, `packets_lost_total`, `packets_retrans_total`, `packets_belated_total`, `buffer_packets`, `buffer_bytes`, `buffer_time_seconds`, `latency_seconds`
- **Unit**: `total`, `bytes`, `seconds`

#### Example Prometheus Metrics

**Receiver Statistics**:
```
gosrt_connection_congestion_recv_packets_total{socket_id="0x12345678"} 1000000
gosrt_connection_congestion_recv_bytes_total{socket_id="0x12345678"} 1456000000
gosrt_connection_congestion_recv_packets_dropped_total{socket_id="0x12345678"} 18
gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="too_old"} 10
gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="already_acked"} 5
gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="duplicate"} 2
gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="store_insert_failed"} 1
gosrt_connection_congestion_recv_packets_lost_total{socket_id="0x12345678"} 100
gosrt_connection_congestion_recv_packets_retrans_total{socket_id="0x12345678"} 50
gosrt_connection_congestion_recv_packets_belated_total{socket_id="0x12345678"} 10
gosrt_connection_congestion_recv_buffer_packets{socket_id="0x12345678"} 100
gosrt_connection_congestion_recv_buffer_bytes{socket_id="0x12345678"} 145600
gosrt_connection_congestion_recv_buffer_time_seconds{socket_id="0x12345678"} 0.1
gosrt_connection_congestion_recv_processing_latency_seconds_avg{socket_id="0x12345678"} 0.001
gosrt_connection_congestion_recv_processing_latency_seconds_max{socket_id="0x12345678"} 0.01
gosrt_connection_congestion_recv_buffer_latency_seconds_avg{socket_id="0x12345678"} 0.05
gosrt_connection_congestion_recv_buffer_latency_seconds_max{socket_id="0x12345678"} 0.2
```

**Sender Statistics**:
```
gosrt_connection_congestion_send_packets_total{socket_id="0x12345678"} 1000000
gosrt_connection_congestion_send_bytes_total{socket_id="0x12345678"} 1456000000
gosrt_connection_congestion_send_packets_dropped_total{socket_id="0x12345678"} 8
gosrt_connection_congestion_send_data_drop_total{socket_id="0x12345678",reason="too_old"} 3
gosrt_connection_send_data_drop_total{socket_id="0x12345678",reason="marshal"} 2
gosrt_connection_send_data_drop_total{socket_id="0x12345678",reason="ring_full"} 1
gosrt_connection_send_data_drop_total{socket_id="0x12345678",reason="submit"} 1
gosrt_connection_send_data_drop_total{socket_id="0x12345678",reason="iouring"} 1
gosrt_connection_send_control_drop_total{socket_id="0x12345678",reason="marshal"} 0
gosrt_connection_send_control_drop_total{socket_id="0x12345678",reason="ring_full"} 0
gosrt_connection_send_control_drop_total{socket_id="0x12345678",reason="submit"} 0
gosrt_connection_send_control_drop_total{socket_id="0x12345678",reason="iouring"} 0
gosrt_connection_congestion_send_packets_lost_total{socket_id="0x12345678"} 100
gosrt_connection_congestion_send_packets_retrans_total{socket_id="0x12345678"} 50
gosrt_connection_congestion_send_buffer_packets{socket_id="0x12345678"} 200
gosrt_connection_congestion_send_buffer_bytes{socket_id="0x12345678"} 291200
gosrt_connection_congestion_send_retrans_latency_seconds_avg{socket_id="0x12345678"} 0.0005
gosrt_connection_congestion_send_retrans_latency_seconds_max{socket_id="0x12345678"} 0.005
```

**Error Counters**:
```
gosrt_connection_congestion_recv_packets_nil_total{socket_id="0x12345678"} 0
gosrt_connection_congestion_recv_store_insert_failed_total{socket_id="0x12345678"} 2
gosrt_connection_congestion_recv_delivery_failed_total{socket_id="0x12345678"} 0
gosrt_connection_congestion_send_delivery_failed_total{socket_id="0x12345678"} 0
gosrt_connection_congestion_send_nak_not_found_total{socket_id="0x12345678"} 0
```

#### Implementation in Metrics Handler

**Update `metrics/handler.go`** to include congestion control metrics:

```go
// In MetricsHandler(), after connection-level metrics
// Write congestion control receiver metrics
writeCounterValue(b, "gosrt_connection_congestion_recv_packets_total",
    metrics.CongestionRecvPkt.Load(),
    "socket_id", socketIdStr)
writeCounterValue(b, "gosrt_connection_congestion_recv_bytes_total",
    metrics.CongestionRecvByte.Load(),
    "socket_id", socketIdStr)
writeCounterValue(b, "gosrt_connection_congestion_recv_packets_dropped_total",
    metrics.CongestionRecvPktDrop.Load(),
    "socket_id", socketIdStr, "reason", "total")
writeCounterValue(b, "gosrt_connection_congestion_recv_packets_lost_total",
    metrics.CongestionRecvPktLoss.Load(),
    "socket_id", socketIdStr)
// ... (similar for all receiver metrics)

// Write congestion control sender metrics
writeCounterValue(b, "gosrt_connection_congestion_send_packets_total",
    metrics.CongestionSendPkt.Load(),
    "socket_id", socketIdStr)
// ... (similar for all sender metrics)

// Write latency metrics
if metrics.CongestionRecvProcessingLatency != nil {
    holdAvg, holdMax, _, _ := metrics.CongestionRecvProcessingLatency.GetStats()
    writeGauge(b, "gosrt_connection_congestion_recv_processing_latency_seconds_avg",
        holdAvg, "socket_id", socketIdStr)
    writeGauge(b, "gosrt_connection_congestion_recv_processing_latency_seconds_max",
        holdMax, "socket_id", socketIdStr)
}
// ... (similar for all latency metrics)
```

#### Example Prometheus Queries

**Packet Drop Rate**:
```promql
rate(gosrt_connection_congestion_recv_packets_dropped_total[5m]) /
rate(gosrt_connection_congestion_recv_packets_total[5m]) * 100
```

**Retransmission Rate**:
```promql
rate(gosrt_connection_congestion_recv_packets_retrans_total[5m]) /
rate(gosrt_connection_congestion_recv_packets_total[5m]) * 100
```

**Average Buffer Latency**:
```promql
gosrt_connection_congestion_recv_buffer_latency_seconds_avg
```

**Processing Latency Trend**:
```promql
rate(gosrt_connection_congestion_recv_processing_latency_seconds_avg[5m])
```

**Error Rate**:
```promql
rate(gosrt_connection_congestion_recv_delivery_failed_total[5m]) +
rate(gosrt_connection_congestion_recv_store_insert_failed_total[5m])
```

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

### Phase 5: Statistics Migration (Full Migration to Atomic Counters)

**Tasks**:
1. Update `Stats()` method to read from atomic metrics (lock-free)
2. Remove old `connStats` struct and `statisticsLock`
3. Clean up all old increment code
4. Verify all statistics are now lock-free

**Files**:
- `connection.go` - Update `Stats()` method, remove old structs
- Remove all references to `c.statistics` and `c.statisticsLock`

**Estimated Effort**: 2-3 hours

**Note**: This phase completes the full migration to atomic counters, eliminating all lock contention in statistics collection.

### Phase 6: Congestion Control Statistics Migration (Lock-Free Statistics)

**Tasks**:
1. Add congestion control fields to `ConnectionMetrics` struct
2. Pass `ConnectionMetrics` to receiver and sender via config
3. Replace all `r.statistics.*` and `s.statistics.*` increments with atomic operations
4. Update `Stats()` methods to read from atomic counters (lock-free)
5. Update `connection.go:Stats()` to use atomic counters directly
6. Remove old `statistics` structs and associated locks completely

**Files**:
- `metrics/metrics.go` - Add congestion control fields
- `congestion/live/receive.go` - Replace statistics increments, update `Stats()`
- `congestion/live/send.go` - Replace statistics increments, update `Stats()`
- `connection.go` - Pass metrics to receiver/sender, update `Stats()` to use atomic counters

**Estimated Effort**: 6-8 hours

**Note**: This phase eliminates lock contention in the congestion control layer, which is critical for high-performance packet processing.

### Phase 7: Granular Drop Counters (Option B: Full Granularity)

**Goal**: Implement granular drop counters for all drop points, with separate counters for DATA vs control packets, while maintaining aggregate counters for SRT RFC compliance.

**Tasks**:
1. Add granular drop counter fields to `ConnectionMetrics` struct
   - Congestion control: 5 counters (receiver: 4, sender: 1)
   - Connection-level receive: 8 counters (DATA + control for 4 error types)
   - Connection-level send: 8 counters (DATA + control for 4 error types)
   - Total: 21 new atomic counters
2. Create helper functions for incrementing granular + aggregate counters
   - `IncrementRecvDataDrop()` - Congestion control receiver drops
   - `IncrementSendDataDrop()` - Congestion control sender drops
   - `IncrementSendErrorDrop()` - Connection-level send errors (DATA vs control)
   - `IncrementRecvErrorDrop()` - Connection-level receive errors (DATA vs control)
3. Update congestion control drop points
   - `congestion/live/receive.go`: 4 locations (too old, already ACK'd, duplicate, store insert failed)
   - `congestion/live/send.go`: 1 location (too old)
4. Update connection-level error handlers
   - `connection_linux.go`: Marshal, ring full, submit, io_uring errors
   - `connection.go`: Marshal errors (fallback path)
   - `listen.go`, `dial.go`: Marshal, write errors (fallback path)
   - `listen_linux.go`, `dial_linux.go`: Parse, io_uring, empty, route errors
5. Update `Stats()` methods to calculate aggregates from granular counters
   - `congestion/live/receive.go:Stats()`
   - `congestion/live/send.go:Stats()`
   - `connection.go:Stats()`
6. Update Prometheus metrics handler to expose granular counters with labels
   - Add labels for drop reasons
   - Separate metrics for DATA vs control packets
7. Update `IncrementSendMetrics` and `IncrementRecvMetrics` to use granular counters
   - Check packet type (DATA vs control)
   - Increment appropriate granular counter
   - Increment aggregate for DATA packets only

**Files to Modify**:
- `metrics/metrics.go` - Add 21 new atomic counter fields
- `metrics/helpers.go` - Add helper functions for granular drops
- `metrics/packet_classifier.go` - Update to use granular counters
- `congestion/live/receive.go` - Update 4 drop points
- `congestion/live/send.go` - Update 1 drop point
- `connection_linux.go` - Update error handlers
- `connection.go` - Update error handlers and Stats()
- `listen.go`, `dial.go` - Update error handlers
- `listen_linux.go`, `dial_linux.go` - Update error handlers
- `metrics/handler.go` - Expose granular counters in Prometheus

**Estimated Effort**: 6-8 hours

**Note**: This phase provides complete visibility into drop reasons, enabling precise debugging. The helper function approach ensures granular and aggregate counters stay in sync.

### Phase 8: Testing and Validation

**Tasks**:
1. Unit tests for metrics collection
2. Integration tests for Prometheus endpoint
3. Performance tests (ensure metrics don't impact performance)
4. Documentation updates

**Estimated Effort**: 4-6 hours

### Total Estimated Effort: 32-44 hours

**Note**: Phase 6 (Congestion Control Statistics Migration) is critical for eliminating lock contention in the hot path (packet processing). It should be prioritized after Phase 5. Phase 7 (Granular Drop Counters) provides enhanced debugging capabilities and should be implemented after Phase 6.

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
11. **Complete Migration**: Fully lock-free statistics system, no legacy code

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

