# Receiver Lock Optimization Plan

## Problem Analysis

### Current Lock Usage Issues

1. **`periodicACK()` uses write lock for read-only iteration** ⚠️ **CRITICAL**
   - Uses `Lock()` (write lock) for entire function
   - Iteration is read-only (only reads from `packetStore`)
   - Blocks all `Push()` operations during iteration
   - Only needs write lock for final field updates (4 fields)

2. **`periodicNAK()` correctly uses read lock** ✅
   - Uses `RLock()` (read lock) - good!
   - But iteration can still take time, holding read lock

3. **`Tick()` has multiple lock acquisitions** ⚠️
   - Calls `periodicACK()` (locks)
   - Calls `periodicNAK()` (locks)
   - Calls `RemoveAll()` (locks)
   - Updates rate (locks)
   - Multiple lock/unlock cycles

## Optimization Strategy

### Optimization #1: Split `periodicACK()` Read/Write Locks ⭐ **HIGH IMPACT**

**Current (Bad)**:
```go
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
    r.lock.Lock()  // ← Write lock for entire function
    defer r.lock.Unlock()

    // ... read-only iteration ...
    r.packetStore.Iterate(...)

    // ... update fields ...
    r.lastACKSequenceNumber = ackSequenceNumber
    r.lastPeriodicACK = now
    r.nPackets = 0
    r.statistics.MsBuf = ...
}
```

**Problem**: Write lock blocks all `Push()` operations during iteration.

**Optimized (Good)**:
```go
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
    // Phase 1: Read-only work with read lock
    r.lock.RLock()

    // Early return check (read-only)
    if now-r.lastPeriodicACK < r.periodicACKInterval {
        if r.nPackets >= 64 {
            lite = true
            // Need to continue, but can't update nPackets yet
        } else {
            r.lock.RUnlock()
            return
        }
    }

    // Read-only iteration
    minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
    ackSequenceNumber := r.lastACKSequenceNumber

    minPkt := r.packetStore.Min()
    if minPkt != nil {
        minH := minPkt.Header()
        minPktTsbpdTime = minH.PktTsbpdTime
        maxPktTsbpdTime = minH.PktTsbpdTime
    }

    r.packetStore.Iterate(func(p packet.Packet) bool {
        h := p.Header()
        if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
            return true
        }
        if h.PktTsbpdTime <= now {
            ackSequenceNumber = h.PacketSequenceNumber
            return true
        }
        if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
            ackSequenceNumber = h.PacketSequenceNumber
            maxPktTsbpdTime = h.PktTsbpdTime
            return true
        }
        return false
    })

    // Release read lock before acquiring write lock
    r.lock.RUnlock()

    // Phase 2: Write updates with write lock (brief)
    r.lock.Lock()
    defer r.lock.Unlock()

    // Re-check conditions (may have changed)
    if now-r.lastPeriodicACK < r.periodicACKInterval {
        if !lite {
            return // Early return, no update needed
        }
        // lite ACK needed, continue
    }

    // Update fields (write lock held)
    ok = true
    sequenceNumber = ackSequenceNumber.Inc()
    r.lastACKSequenceNumber = ackSequenceNumber
    r.lastPeriodicACK = now
    r.nPackets = 0
    r.statistics.MsBuf = (maxPktTsbpdTime - minPktTsbpdTime) / 1_000

    return
}
```

**Benefits**:
- Read lock during iteration allows concurrent `Push()` operations
- Write lock only held briefly for field updates
- **Massive reduction in contention**

**Expected Impact**:
- Eliminate blocking of `Push()` during `periodicACK()` iteration
- Reduce `RUnlock()` contention significantly
- **Estimated 5-7% reduction in total blocking time**

### Optimization #2: Minimize Lock Hold Time in `Tick()`

**Current**: Multiple separate lock acquisitions

**Optimized**: Combine related operations where possible

```go
func (r *receiver) Tick(now uint64) {
    // ... periodicACK and periodicNAK (already optimized) ...

    // Combine RemoveAll and rate update into single lock if possible
    r.lock.Lock()

    // RemoveAll (modifies store)
    removed := r.packetStore.RemoveAll(...)

    // Rate update (modifies rate fields)
    tdiff := now - r.rate.last
    if tdiff > r.rate.period {
        // ... rate calculations ...
    }

    r.lock.Unlock()
}
```

**Benefits**:
- Fewer lock acquisitions
- Reduced lock overhead

**Expected Impact**: Small but measurable improvement

### Optimization #3: Consider Lock-Free Statistics Updates

**Current**: Statistics updates require write lock

**Option**: Use atomic operations for read-only statistics

**Analysis**:
- Statistics are read infrequently (mostly for monitoring)
- Updates are frequent (every packet, every tick)
- Atomic operations might be faster for simple counters

**Recommendation**: Defer to Phase 2 - measure impact of Optimization #1 first.

## Implementation Plan

### Phase 1: Fix `periodicACK()` Lock Usage (CRITICAL)

1. **Refactor `periodicACK()` to use read lock for iteration**
   - Use `RLock()` for read-only iteration
   - Use `Lock()` only for final field updates
   - Handle early return correctly

2. **Test**: Verify ACK generation still works correctly

3. **Profile**: Measure reduction in blocking time

**Expected Impact**: **5-7% reduction in total blocking time**

### Phase 2: Optimize `Tick()` Lock Usage

1. **Combine lock acquisitions in `Tick()`**
   - Merge `RemoveAll()` and rate update locks
   - Minimize lock/unlock cycles

2. **Profile**: Measure additional improvement

**Expected Impact**: **1-2% additional reduction**

### Phase 3: Consider Further Optimizations (If Needed)

1. **Lock-free statistics** (if still showing contention)
2. **Batch operations** (collect work, release lock, perform work)
3. **Lock-free read paths** (complex, only if needed)

## Risk Assessment

**Optimization #1 (periodicACK split locks)**:
- **Risk**: Low - well-understood pattern
- **Complexity**: Medium - need to handle early returns correctly
- **Testing**: Need to verify ACK generation correctness

**Optimization #2 (Tick lock combining)**:
- **Risk**: Very Low - straightforward optimization
- **Complexity**: Low - simple refactoring
- **Testing**: Standard testing should catch issues

## Expected Overall Impact

**Conservative Estimate**:
- Optimization #1: 5-7% reduction in blocking time
- Optimization #2: 1-2% additional reduction
- **Total: 6-9% reduction in blocking time**

**Best Case**:
- Optimization #1: 7-10% reduction
- Optimization #2: 2-3% additional reduction
- **Total: 9-13% reduction in blocking time**

This should significantly reduce the 8.98% blocking time in `RUnlock()` operations.

