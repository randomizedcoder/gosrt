# Rate Metrics Performance Design

**Status**: DRAFT - Future Work
**Date**: 2025-12-14
**Related**:
- `metrics_and_statistics_design.md`
- `metrics_implementation_progress.md`
- `nak_btree_implementation.md`

## Overview

This document addresses rate calculation fields in `receive.go` and `send.go` that were not migrated to atomics during the metrics overhaul. These fields are updated on the hot path (every packet) and currently require lock protection, potentially causing contention.

## Problem Statement

During the NAK btree implementation review, we identified that **20 rate-related fields** across receiver and sender are still using lock-based protection, while other metrics have been successfully migrated to atomics.

### Evidence from Metrics Implementation

From `metrics_implementation_progress.md`:
- ✅ Phase 1-6 completed: Atomic counters for `CongestionRecvPkt`, `CongestionRecvByte`, etc.
- ❌ **Gap**: The `rate.*` struct fields and running averages were not addressed

### Current Lock Contention Points

```go
// In Push() - holds write lock for entire packet processing
r.lock.Lock()
defer r.lock.Unlock()
// ... all rate updates happen here under lock ...
r.rate.packets++
r.rate.bytes += pktLen
r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)
```

## Fields Requiring Migration

### Receiver (`congestion/live/receive.go`)

| Field | Type | Updated In | Hot Path? | Frequency |
|-------|------|------------|-----------|-----------|
| `rate.packets` | uint64 | `pushLocked*()` | ✅ Yes | Every packet |
| `rate.bytes` | uint64 | `pushLocked*()` | ✅ Yes | Every packet |
| `rate.bytesRetrans` | uint64 | `pushLocked*()` | ✅ Yes | Retrans packets |
| `nPackets` | uint | `pushLocked*()` | ✅ Yes | Every packet |
| `avgPayloadSize` | float64 | `pushLocked*()` | ✅ Yes | Every packet |
| `avgLinkCapacity` | float64 | `pushLockedOriginal()` | ✅ Yes | Every 16th packet |
| `rate.last` | uint64 | `updateRateStats()` | No | Once per second |
| `rate.packetsPerSecond` | float64 | `updateRateStats()` | No | Once per second |
| `rate.bytesPerSecond` | float64 | `updateRateStats()` | No | Once per second |
| `rate.pktRetransRate` | float64 | `updateRateStats()` | No | Once per second |
| `rate.period` | uint64 | `NewReceiver()` | No | Never (config) |

**Total: 11 fields** (6 on hot path)

### Sender (`congestion/live/send.go`)

| Field | Type | Updated In | Hot Path? | Frequency |
|-------|------|------------|-----------|-----------|
| `rate.bytes` | uint64 | `Push()` | ✅ Yes | Every packet |
| `rate.bytesSent` | uint64 | `Tick()`, `NAK()` | ✅ Yes | Every sent packet |
| `rate.bytesRetrans` | uint64 | `NAK()` | ✅ Yes | Retrans packets |
| `avgPayloadSize` | float64 | `Tick()`, `NAK()` | ✅ Yes | Every sent packet |
| `rate.last` | uint64 | `tickUpdateRateStats()` | No | Once per second |
| `rate.estimatedInputBW` | float64 | `tickUpdateRateStats()` | No | Once per second |
| `rate.estimatedSentBW` | float64 | `tickUpdateRateStats()` | No | Once per second |
| `rate.pktRetransRate` | float64 | `tickUpdateRateStats()` | No | Once per second |
| `rate.period` | uint64 | `NewSender()` | No | Never (config) |

**Total: 9 fields** (4 on hot path)

**Combined: 20 fields** (10 on hot path)

## Proposed Migration

### Category 1: Easy - Counter Increments (uint64)

These can directly use `atomic.Uint64`:

```go
// Before
r.rate.packets++
r.rate.bytes += pktLen

// After
r.ratePackets.Add(1)
r.rateBytes.Add(pktLen)
```

**Fields**:
- `rate.packets` → `ratePackets atomic.Uint64`
- `rate.bytes` → `rateBytes atomic.Uint64`
- `rate.bytesRetrans` → `rateBytesRetrans atomic.Uint64`
- `rate.bytesSent` → `rateBytesSent atomic.Uint64`
- `nPackets` → `nPackets atomic.Uint64`
- `rate.last` → `rateLast atomic.Uint64`

### Category 2: Medium - Computed Float64 Values

These are written once per second, read occasionally. Can use `atomic.Value`:

```go
// Before
r.rate.bytesPerSecond = float64(r.rate.bytes) / (float64(tdiff) / 1e6)

// After
r.rateBytesPerSecond.Store(float64(r.rateBytes.Load()) / (float64(tdiff) / 1e6))

// Reading
bps := r.rateBytesPerSecond.Load().(float64)
```

**Fields**:
- `rate.packetsPerSecond` → `ratePacketsPerSecond atomic.Value`
- `rate.bytesPerSecond` → `rateBytesPerSecond atomic.Value`
- `rate.pktRetransRate` → `ratePktRetransRate atomic.Value`
- `rate.estimatedInputBW` → `rateEstimatedInputBW atomic.Value`
- `rate.estimatedSentBW` → `rateEstimatedSentBW atomic.Value`

**Note**: `atomic.Value` allocates on `Store()`, but these are updated once per second, not per packet.

### Category 3: Hard - Running Averages (float64 read-modify-write)

These require atomic read-modify-write for float64:

```go
// Current code
r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)
```

**Options**:

#### Option A: CAS Loop with math.Float64bits

```go
func (r *receiver) updateAvgPayloadSize(pktLen uint64) {
    for {
        oldBits := r.avgPayloadSizeBits.Load()
        old := math.Float64frombits(oldBits)
        new := 0.875*old + 0.125*float64(pktLen)
        newBits := math.Float64bits(new)
        if r.avgPayloadSizeBits.CompareAndSwap(oldBits, newBits) {
            return
        }
    }
}
```

**Pros**: Lock-free, correct
**Cons**: CAS loop can spin under contention

#### Option B: Accept Slight Races

Running averages are approximate by nature. Small races may be acceptable:

```go
// Potentially racy but running average tolerates errors
old := math.Float64frombits(r.avgPayloadSizeBits.Load())
new := 0.875*old + 0.125*float64(pktLen)
r.avgPayloadSizeBits.Store(math.Float64bits(new))
```

**Pros**: Simple, no spinning
**Cons**: Theoretically racy (torn reads on some architectures)

#### Option C: Fine-Grained Lock

Keep lock but make it specific to running averages:

```go
type receiver struct {
    avgLock sync.Mutex  // Only protects avgPayloadSize and avgLinkCapacity
    avgPayloadSize float64
    avgLinkCapacity float64
}
```

**Pros**: Correct, simple
**Cons**: Still has lock contention (but much smaller critical section)

**Recommendation**: Option A (CAS loop) for correctness, or Option C if CAS contention is a problem.

**Fields**:
- `avgPayloadSize` (receiver and sender)
- `avgLinkCapacity` (receiver only)

## Proposed Struct Changes

### Receiver

```go
type receiver struct {
    // ... existing fields ...

    // Rate counters (atomics - hot path)
    ratePackets      atomic.Uint64
    rateBytes        atomic.Uint64
    rateBytesRetrans atomic.Uint64
    rateLast         atomic.Uint64
    nPackets         atomic.Uint64

    // Rate computed values (atomic.Value for float64)
    ratePacketsPerSecond atomic.Value // float64
    rateBytesPerSecond   atomic.Value // float64
    ratePktRetransRate   atomic.Value // float64

    // Running averages (atomic uint64 with Float64bits)
    avgPayloadSizeBits  atomic.Uint64 // Use math.Float64bits/Float64frombits
    avgLinkCapacityBits atomic.Uint64

    // Config (immutable)
    ratePeriod uint64
}
```

### Sender

```go
type sender struct {
    // ... existing fields ...

    // Rate counters (atomics - hot path)
    rateBytes        atomic.Uint64
    rateBytesSent    atomic.Uint64
    rateBytesRetrans atomic.Uint64
    rateLast         atomic.Uint64

    // Rate computed values (atomic.Value for float64)
    rateEstimatedInputBW atomic.Value // float64
    rateEstimatedSentBW  atomic.Value // float64
    ratePktRetransRate   atomic.Value // float64

    // Running averages
    avgPayloadSizeBits atomic.Uint64

    // Config (immutable)
    ratePeriod uint64
}
```

## Implementation Plan

### Phase 1: Counter Migration (Low Risk)

1. Replace `rate.packets`, `rate.bytes`, etc. with atomic.Uint64
2. Update all increment sites
3. Update all read sites (updateRateStats, Stats)
4. Run race detector tests

**Estimated effort**: 2 hours

### Phase 2: Computed Value Migration (Low Risk)

1. Replace computed float64 fields with atomic.Value
2. Update write sites (updateRateStats, tickUpdateRateStats)
3. Update read sites (Stats, PacketRate)
4. Run race detector tests

**Estimated effort**: 1 hour

### Phase 3: Running Average Migration (Medium Risk)

1. Implement CAS-based update for avgPayloadSize
2. Implement CAS-based update for avgLinkCapacity
3. Add helper functions for float64 atomic operations
4. Comprehensive testing

**Estimated effort**: 2 hours

### Phase 4: Lock Removal and Cleanup

1. Remove rate-related code from lock-protected sections
2. Potentially remove or reduce lock scope in Push()
3. Performance benchmarking
4. Update documentation

**Estimated effort**: 2 hours

**Total estimated effort**: 7-8 hours

## Testing Requirements

### Unit Tests

- `receive_rate_atomic_test.go`: Test atomic counter correctness
- `send_rate_atomic_test.go`: Test atomic counter correctness

### Race Detection

```bash
go test -race ./congestion/live/... -count=10
```

### Benchmarks

```bash
# Before and after comparison
go test -bench=BenchmarkPush -benchmem ./congestion/live/...
go test -bench=BenchmarkTick -benchmem ./congestion/live/...
```

### Integration Tests

- Verify rate calculations in `Stats()` output match expected values
- Verify no lock contention in parallel test scenarios

## Metrics to Add

Consider adding visibility into rate calculation performance:

```go
// In metrics.go
RateUpdateCount     atomic.Uint64 // Times updateRateStats ran
RateCASRetries      atomic.Uint64 // CAS retries for running averages (if using Option A)
```

## Dependencies

- NAK btree implementation should be completed first
- This work is independent of NAK btree but may overlap in `receive.go` changes

## References

- `metrics_and_statistics_design.md` - Original metrics design
- `metrics_implementation_progress.md` - Current migration status
- `nak_btree_implementation.md` - Where this issue was discovered

