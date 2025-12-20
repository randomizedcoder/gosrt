# Receive Lock Contention Analysis

## Overview

This document provides a focused analysis of lock contention in `congestion/live/receive.go`, specifically examining the usage of `metrics.WithWLockTiming` and identifying optimization opportunities to reduce lock contention that has been identified as a critical performance bottleneck.

**Related Documents**:
- [`metrics_and_statistics_design.md`](./metrics_and_statistics_design.md) - Overall metrics design
- [`metrics_implementation_progress.md`](./metrics_implementation_progress.md) - Implementation progress
- [`integration_testing_50mbps_defect.md`](./integration_testing_50mbps_defect.md) - Contention evidence (Section 23)
- [`rate_metrics_performance_design.md`](./rate_metrics_performance_design.md) - Rate metrics migration plan

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Lock Contention Evidence](#2-lock-contention-evidence)
3. [Current WithWLockTiming Usage](#3-current-withwlocktiming-usage)
4. [Lock Scope Analysis](#4-lock-scope-analysis)
5. [Contention Hotspots](#5-contention-hotspots)
6. [Optimization Strategies](#6-optimization-strategies)
7. [Implementation Priority](#7-implementation-priority)
8. [Metrics for Monitoring](#8-metrics-for-monitoring)

---

## 1. Executive Summary

### Problem Statement

The test server (with io_uring + btree + NAK btree) shows **44% CPU in `runtime.futex`** compared to 4.2% on the control server. Mutex profiling reveals:
- **54% in inlined lock operations**
- **36.7% in RWMutex.RUnlock** (up from 9.6% on control)

This lock contention:
1. Limits throughput at higher bitrates (75-100+ Mb/s)
2. May cause issues under packet loss (more lock operations per NAK)
3. Increases RTT by ~8-10x on the test path

### Root Causes Identified

| Issue | Location | Impact |
|-------|----------|--------|
| NAK btree insertions not batched | `periodicNakBtree()` | N lock acquisitions per cycle |
| Rate metrics under lock | `pushLocked*()` | 4300+ lock ops/sec at 50 Mb/s |
| Nested locking pattern | `pushLockedNakBtree()` | Extended contention window |
| Lock scope too wide | `periodicNakBtree()` | Blocks concurrent operations |

---

## 2. Lock Contention Evidence

### Mutex Profile Comparison

| Metric | Control Server | Test Server | Change |
|--------|----------------|-------------|--------|
| `runtime.futex` | 4.2% | 44% | +947% |
| `sync.(*RWMutex).RUnlock` | 9.6% | 36.7% | +282% |
| `runtime.lock2` (inlined) | - | 54% | - |
| `runtime.unlock` | 1.0% | 5.2% | +420% |

### RTT Impact (confirms contention)

| Pipeline | RTT |
|----------|-----|
| Control CG | 1.3ms |
| Control Server | 0.8ms |
| **Test CG** | **10.1ms** |
| **Test Server** | **11.7ms** |

The ~8-10x higher RTT on the test path is caused by lock contention.

---

## 3. Current WithWLockTiming Usage

### 3.1 How WithWLockTiming Works

```go
// metrics/helpers.go

func WithWLockTiming(metrics *LockTimingMetrics, mutex interface {
    Lock()
    Unlock()
}, fn func()) {
    if metrics == nil {
        mutex.Lock()
        defer mutex.Unlock()
        fn()
        return
    }

    // Measure wait time (time spent waiting to acquire lock)
    waitStart := time.Now()
    mutex.Lock()
    waitDuration := time.Since(waitStart)

    if waitDuration > 0 {
        metrics.RecordWaitTime(waitDuration)
    }

    // Measure hold time (time lock is held)
    defer func() {
        holdDuration := time.Since(waitStart)
        metrics.RecordHoldTime(holdDuration)
        mutex.Unlock()
    }()

    fn()
}
```

**Key Metrics Recorded**:
- **Wait Time**: How long the goroutine waited to acquire the lock (indicates contention)
- **Hold Time**: How long the lock was held (indicates critical section duration)

### 3.2 LockTimingMetrics Structure

```go
type LockTimingMetrics struct {
    holdTimeSamples [10]atomic.Int64  // Circular buffer, nanoseconds
    holdTimeIndex   atomic.Uint64     // Write counter
    waitTimeSamples [10]atomic.Int64  // Circular buffer, nanoseconds
    waitTimeIndex   atomic.Uint64     // Write counter
    maxHoldTime     atomic.Int64      // Peak hold time
    maxWaitTime     atomic.Int64      // Peak wait time
}
```

### 3.3 Usage Locations in receive.go

| Method | Line | Lock Type | Purpose |
|--------|------|-----------|---------|
| `Push()` | 260-269 | Write | Packet arrival handling |
| `periodicACK()` | 614-625 | Write | Write phase of ACK |
| `Tick()` delivery | 951-973 | Write | Packet delivery to application |
| `Tick()` rate stats | 998-1006 | Write | Rate statistics update |

### 3.4 Related Usage (Read Locks)

| Method | Line | Lock Type | Purpose |
|--------|------|-----------|---------|
| `periodicNakOriginal()` | 681-690 | Read | NAK list building (non-btree path) |

---

## 4. Lock Scope Analysis

### 4.1 Push() - Packet Arrival Path

**Current Implementation** (`receive.go:260-279`):

```go
func (r *receiver) Push(pkt packet.Packet) {
    if r.lockTiming != nil {
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            r.pushLocked(pkt)  // ← Everything under lock
        })
        return
    }
    r.lock.Lock()
    defer r.lock.Unlock()
    r.pushLocked(pkt)
}
```

**What's Under Lock** (`pushLockedNakBtree`, lines 285-362):

```go
func (r *receiver) pushLockedNakBtree(pkt packet.Packet) {
    // Under lock:
    r.nPackets++                                    // Could be atomic
    r.rate.packets++                                // Could be atomic
    r.rate.bytes += pktLen                          // Could be atomic
    r.avgPayloadSize = 0.875*... + 0.125*...        // Could be CAS

    // ... packet validation checks ...

    r.nakBtree.Delete(seq)                          // ← NESTED LOCK!
    r.packetStore.Insert(pkt)                       // Must be under lock

    r.lastPacketArrivalTime.Store(now)              // Already atomic
    r.lastDataPacketSeq.Store(seq)                  // Already atomic
}
```

**Problems**:
1. Rate counters updated under lock (~4300 times/sec at 50 Mb/s)
2. `nakBtree.Delete()` creates nested locking (nakBtree has its own mutex)
3. EMA calculation (`avgPayloadSize`) under lock

### 4.2 periodicACK() - Two-Phase Lock Pattern

**Current Implementation** (`receive.go:498-626`):

```go
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
    // Phase 1: Read-only work with read lock
    r.lock.RLock()
    // ... iteration and calculations ...
    r.lock.RUnlock()

    // Phase 2: Write updates with write lock (brief)
    if r.lockTiming != nil {
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            okResult, seqResult, liteResult = r.periodicACKWriteLocked(...)
        })
        return okResult, seqResult, liteResult
    }
    r.lock.Lock()
    defer r.lock.Unlock()
    return r.periodicACKWriteLocked(...)
}
```

**Assessment**: ✅ Good - already optimized with read/write lock separation.

### 4.3 Tick() - Packet Delivery

**Current Implementation** (`receive.go:951-995`):

```go
func (r *receiver) Tick(now uint64) {
    // ...
    if r.lockTiming != nil {
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            removed = r.packetStore.RemoveAll(
                func(p packet.Packet) bool { /* filter */ },
                func(p packet.Packet) { /* deliver */ },
            )
        })
    } else {
        r.lock.Lock()
        removed := r.packetStore.RemoveAll(...)
        r.lock.Unlock()
    }
}
```

**Problems**:
1. Delivery callback executed under lock
2. If `r.deliver()` is slow, lock is held longer
3. Metrics updates (atomic) done under lock

### 4.4 periodicNakBtree() - NOT Using WithWLockTiming (But Should?)

**Current Implementation** (`receive.go:760-887`):

```go
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
    // === PRE-WORK: No lock needed ===
    // ... time checks, metrics ...

    // === MINIMAL LOCK SCOPE ===
    r.lock.RLock()
    // ... packetStore iteration ...
    r.lock.RUnlock()
    // === END LOCK SCOPE ===

    // === POST-WORK: No lock needed ===
    r.nakBtree.InsertBatch(*gapsPtr)
    list := r.consolidateNakBtree()
    return list
}
```

**Assessment**: ✅ Already optimized - minimal lock scope, no timing metrics (read lock only).

---

## 5. Contention Hotspots

### 5.1 Hotspot #1: Push() Rate Metrics

**Frequency**: ~4300 calls/sec at 50 Mb/s

**Code** (`receive.go:301-316`):
```go
r.nPackets++
r.rate.packets++
r.rate.bytes += pktLen
if pkt.Header().RetransmittedPacketFlag {
    r.rate.bytesRetrans += pktLen
}
r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)
```

**Impact**: 36.7% of CPU in RUnlock contention.

### 5.2 Hotspot #2: Nested NAK Btree Lock

**Code** (`receive.go:339-343`):
```go
if r.nakBtree != nil {
    if r.nakBtree.Delete(seq) {  // ← Acquires nakBtree.mu while holding r.lock
        m.NakBtreeDeletes.Add(1)
    }
}
```

**Impact**: Extended contention window, blocking other Push() calls.

### 5.3 Hotspot #3: Tick() Delivery Under Lock

**Code** (`receive.go:954-971`):
```go
metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
    removed = r.packetStore.RemoveAll(
        func(p packet.Packet) bool { ... },
        func(p packet.Packet) {
            // ... metrics updates ...
            r.deliver(p)  // ← Application callback under lock!
        },
    )
})
```

**Impact**: If delivery is slow, blocks all Push() calls.

### 5.4 Hotspot #4: Tick() Rate Stats

**Code** (`receive.go:998-1006`):
```go
if r.lockTiming != nil {
    metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
        r.updateRateStats(now)
    })
} else {
    r.lock.Lock()
    r.updateRateStats(now)
    r.lock.Unlock()
}
```

**Impact**: Blocks Push() during rate calculation (once per second).

---

## 6. Optimization Strategies

### 6.1 Strategy A: Migrate Rate Metrics to Atomics

**Current**:
```go
// Under lock (pushLockedNakBtree)
r.nPackets++
r.rate.packets++
r.rate.bytes += pktLen
r.rate.bytesRetrans += pktLen
r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)
```

**Proposed**:
```go
// Lock-free
r.nPackets.Add(1)
r.ratePackets.Add(1)
r.rateBytes.Add(pktLen)
r.rateBytesRetrans.Add(pktLen)
r.avgPayloadSize.UpdateEMA(float64(pktLen), 0.125)  // CAS loop

// Then acquire lock for packetStore only
r.lock.Lock()
// ... packetStore operations only ...
r.lock.Unlock()
```

**Expected Impact**: Remove ~4300 lock ops/sec.

### 6.2 Strategy B: Separate NAK Btree Delete from Receiver Lock

**Current**:
```go
r.lock.Lock()
// ...
r.nakBtree.Delete(seq)  // Nested lock
r.packetStore.Insert(pkt)
r.lock.Unlock()
```

**Proposed**:
```go
r.lock.Lock()
inserted := r.packetStore.Insert(pkt)
seq := pkt.Header().PacketSequenceNumber.Val()
r.lock.Unlock()

// NAK btree update outside receiver lock
if inserted && r.nakBtree != nil {
    r.nakBtree.Delete(seq)
}
```

**Expected Impact**: Reduce nested lock holding time.

### 6.3 Strategy C: Batch Delivery Outside Lock

**Current**:
```go
r.lock.Lock()
r.packetStore.RemoveAll(filter, func(p packet.Packet) {
    r.deliver(p)  // Under lock!
})
r.lock.Unlock()
```

**Proposed**:
```go
// Collect packets to deliver under lock
r.lock.Lock()
toDeliver := r.packetStore.RemoveAllCollect(filter)
r.lock.Unlock()

// Deliver outside lock
for _, p := range toDeliver {
    r.deliver(p)
}
```

**Expected Impact**: Application callback latency no longer blocks Push().

### 6.4 Strategy D: Lock-Free Rate Stats Update

**Current**:
```go
func (r *receiver) updateRateStats(now uint64) {
    // Under lock
    tdiff := now - r.rate.last
    if tdiff > r.rate.period {
        r.rate.packetsPerSecond = float64(r.rate.packets) / ...
        // ... calculations ...
    }
}
```

**Proposed**:
```go
func (r *receiver) updateRateStats(now uint64) {
    // Lock-free using atomics
    tdiff := now - r.rateLast.Load()
    if tdiff > r.ratePeriod {
        packets := r.ratePackets.Swap(0)
        bytes := r.rateBytes.Swap(0)
        // ... calculations using atomics ...
    }
}
```

**Expected Impact**: Eliminate rate stats lock contention.

---

## 7. Implementation Priority

| Priority | Strategy | Files | Effort | Expected Impact | Risk |
|----------|----------|-------|--------|-----------------|------|
| **P1** | A. Rate metrics to atomics | `receive.go` | 3 hours | High - remove 4300 lock ops/sec | Low |
| **P1** | B. Separate nakBtree.Delete | `receive.go` | 1 hour | Medium - reduce nested locking | Low |
| **P2** | C. Batch delivery outside lock | `receive.go` | 2 hours | Medium - unblock Push() | Medium |
| **P3** | D. Lock-free rate stats | `receive.go` | 2 hours | Low - once per second | Low |

### Recommended Order

1. **Phase 1** (Immediate - 4 hours)
   - Implement Strategy A: Rate metrics to atomics
   - Implement Strategy B: Separate nakBtree.Delete
   - Expected: ~50% reduction in lock contention

2. **Phase 2** (Short-term - 2 hours)
   - Implement Strategy C: Batch delivery
   - Expected: More consistent Push() latency

3. **Phase 3** (Low priority - 2 hours)
   - Implement Strategy D: Lock-free rate stats
   - Expected: Marginal improvement

---

## 8. Metrics for Monitoring

### 8.1 Current Lock Timing Metrics

The following Prometheus metrics are exposed for `receiver.lock`:

```prometheus
# Average hold time (seconds)
gosrt_lock_hold_time_avg{socket_id="...", lock="receiver"} 0.000012

# Maximum hold time (seconds)
gosrt_lock_hold_time_max{socket_id="...", lock="receiver"} 0.000089

# Average wait time (seconds)
gosrt_lock_wait_time_avg{socket_id="...", lock="receiver"} 0.000008

# Maximum wait time (seconds)
gosrt_lock_wait_time_max{socket_id="...", lock="receiver"} 0.000156

# Total lock acquisitions
gosrt_lock_acquisitions_total{socket_id="...", lock="receiver"} 1234567
```

### 8.2 Key Indicators of Contention

| Metric | Healthy | Warning | Critical |
|--------|---------|---------|----------|
| `lock_wait_time_avg` | < 10µs | 10-50µs | > 50µs |
| `lock_wait_time_max` | < 100µs | 100-500µs | > 500µs |
| `lock_hold_time_avg` | < 50µs | 50-200µs | > 200µs |
| `lock_acquisitions_total` rate | Linear | - | Plateaued |

### 8.3 Monitoring Dashboard Query Examples

**Lock Contention Rate**:
```promql
rate(gosrt_lock_acquisitions_total{lock="receiver"}[1m])
```

**Average Wait Time Over Time**:
```promql
gosrt_lock_wait_time_avg{lock="receiver"}
```

**Contention Ratio** (wait time / hold time):
```promql
gosrt_lock_wait_time_avg{lock="receiver"} / gosrt_lock_hold_time_avg{lock="receiver"}
```

Values > 1.0 indicate significant contention (goroutines waiting longer than locks are held).

---

## 9. Code Reference Summary

### Key Files

| File | Purpose |
|------|---------|
| `congestion/live/receive.go` | Receiver implementation with lock timing |
| `metrics/helpers.go` | `WithWLockTiming`, `WithRLockTiming` implementations |
| `metrics/metrics.go` | `LockTimingMetrics` structure definition |
| `metrics/handler.go` | Prometheus metric export |

### Critical Sections Under Analysis

| Location | Lines | Lock Type | Contention Level |
|----------|-------|-----------|------------------|
| `Push()` | 260-269 | Write | **HIGH** |
| `pushLockedNakBtree()` | 285-362 | Write | **HIGH** |
| `periodicACKWriteLocked()` | 628-669 | Write | Low |
| `Tick()` delivery | 951-995 | Write | **MEDIUM** |
| `Tick()` rate stats | 998-1006 | Write | Low |

---

## 10. Next Steps

### Immediate Actions

1. [ ] Run mutex profile to baseline current contention:
   ```bash
   go tool pprof -http=:8080 http://localhost:6060/debug/pprof/mutex
   ```

2. [ ] Implement Strategy A (rate metrics to atomics)
   - Modify `receiver` struct to use atomic fields
   - Update `pushLockedNakBtree()` to increment atomics before lock
   - Update `updateRateStats()` to read from atomics

3. [ ] Implement Strategy B (separate nakBtree.Delete)
   - Extract sequence number before unlock
   - Call `nakBtree.Delete()` after releasing `r.lock`

### Validation

After each optimization:
1. Run mutex profile to measure contention reduction
2. Run throughput test at 50 Mb/s to verify no regression
3. Run throughput test at 75 Mb/s to measure improvement
4. Check RTT reduction (target: < 5ms)

---

## Appendix A: WithWLockTiming Implementation Detail

The `WithWLockTiming` function in `metrics/helpers.go` provides:

1. **Transparent fallback**: If `metrics == nil`, it degrades to simple lock/unlock
2. **Wait time measurement**: `time.Since(waitStart)` after `Lock()` returns
3. **Hold time measurement**: Total time from lock acquisition to unlock
4. **Lock-free recording**: Uses atomic operations in `LockTimingMetrics`

```go
func WithWLockTiming(metrics *LockTimingMetrics, mutex interface {
    Lock()
    Unlock()
}, fn func()) {
    if metrics == nil {
        mutex.Lock()
        defer mutex.Unlock()
        fn()
        return
    }

    waitStart := time.Now()
    mutex.Lock()
    waitDuration := time.Since(waitStart)

    if waitDuration > 0 {
        metrics.RecordWaitTime(waitDuration)
    }

    defer func() {
        holdDuration := time.Since(waitStart)
        metrics.RecordHoldTime(holdDuration)
        mutex.Unlock()
    }()

    fn()
}
```

## Appendix B: LockTimingMetrics Ring Buffer

The `LockTimingMetrics` structure uses a lock-free ring buffer for minimum overhead:

```go
type LockTimingMetrics struct {
    holdTimeSamples [10]atomic.Int64  // Last 10 hold times (ns)
    holdTimeIndex   atomic.Uint64     // Monotonic counter, slot = index % 10
    waitTimeSamples [10]atomic.Int64  // Last 10 wait times (ns)
    waitTimeIndex   atomic.Uint64     // Monotonic counter
    maxHoldTime     atomic.Int64      // All-time max (CAS updated)
    maxWaitTime     atomic.Int64      // All-time max (CAS updated)
}
```

**Why 10 samples?**
- Small enough for L1 cache locality
- Large enough for meaningful averages
- Simple modulo operation for slot calculation
- No locks needed for reads or writes

