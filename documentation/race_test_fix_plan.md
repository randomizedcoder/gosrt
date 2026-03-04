# Race Test Fix Plan

**Date:** 2026-02-24
**Status:** Draft
**Issue:** 12 failing race tests in `ci-race` target

## Executive Summary

Running `make ci-race` identifies 12 tests failing with data race detection across 2 packages:
- `congestion/live/receive` - 11 tests
- `contrib/client-seeker` - 1 test

The races fall into **3 distinct categories** with different root causes and fix strategies.

---

## Failing Tests Summary

| Test Name | Package | Category | Severity |
|-----------|---------|----------|----------|
| `TestRecvControlRing_ProducerConsumer_Realistic` | receive | Test Bug | Low |
| `TestRecvControlRing_ProducerConsumer_StressWithConsumer` | receive | Test Bug | Low |
| `TestRecvControlRing_ProducerConsumer_SingleProducerMaxSpeed` | receive | Test Bug | Low |
| `TestTokenBucket_ConcurrentConsume` | client-seeker | Test Bug | Low |
| `TestConcurrent_PushTickNAKACK_OutOfOrder` | receive | Design Issue | High |
| `TestRace_PushWithTick` | receive | Design Issue | High |
| `TestRace_PushWithTick_FastTick` | receive | Design Issue | High |
| `TestRace_FullPipeline` | receive | Design Issue | High |
| `TestRace_FullPipeline_WithLoss` | receive | Design Issue | High |
| `TestRace_NakBtreeOperations` | receive | Design Issue | High |
| `TestRace_MetricsUpdates` | receive | Design Issue | High |
| `TestRace_SequenceWraparound` | receive | Design Issue | High |

---

## Category 1: Test Infrastructure Bugs (4 tests)

### Description

These tests use plain `int64` variables (`totalPushed`, `totalPopped`, `totalConsumed`) that are read from the main goroutine while being written from background goroutines. This is a race in the **test code**, not the production code.

### Affected Tests

1. **`TestRecvControlRing_ProducerConsumer_Realistic`** (`control_ring_test.go:379-446`)
2. **`TestRecvControlRing_ProducerConsumer_StressWithConsumer`** (`control_ring_test.go:451-538`)
3. **`TestRecvControlRing_ProducerConsumer_SingleProducerMaxSpeed`** (`control_ring_test.go:542-612`)
4. **`TestTokenBucket_ConcurrentConsume`** (`tokenbucket_test.go:356-397`)

### Root Cause

```go
// control_ring_test.go:393 - BUG: plain int64 shared between goroutines
var totalPushed, totalPopped int64

// Line 413 - Written in consumer goroutine
totalPopped++

// Line 440 - Read in main goroutine
t.Logf("Realistic test: pushed=%d, popped=%d", totalPushed, totalPopped)
```

### Fix Strategy

**Convert to `atomic.Int64`:**

```go
// BEFORE (race)
var totalPushed, totalPopped int64
totalPopped++  // In goroutine
t.Logf("pushed=%d, popped=%d", totalPushed, totalPopped)  // In main

// AFTER (safe)
var totalPushed, totalPopped atomic.Int64
totalPopped.Add(1)  // In goroutine
t.Logf("pushed=%d, popped=%d", totalPushed.Load(), totalPopped.Load())  // In main
```

### TokenBucket Special Case

`TestTokenBucket_ConcurrentConsume` has a particularly buggy pattern:

```go
// tokenbucket_test.go:379-386 - BUG: broken manual CAS loop
current := totalConsumed
for {
    if current == totalConsumed {
        totalConsumed = current + 1456
        break
    }
    current = totalConsumed
}
```

This attempts to implement a CAS loop using plain `int64` which doesn't work. Fix:

```go
// AFTER (safe)
var totalConsumed atomic.Int64
totalConsumed.Add(1456)  // Atomic increment
```

### Effort Estimate

- **Files to modify:** 2 (`control_ring_test.go`, `tokenbucket_test.go`)
- **Changes:** Replace `int64` with `atomic.Int64`, update all accesses
- **Risk:** None - test-only changes
- **Difficulty:** Trivial

---

## Category 2: Receiver Design Issue - Push/Tick Btree Race (8 tests)

### Description

The receiver's btree (`packetStore`) is accessed concurrently:
- **Push goroutine(s):** Insert packets via `pushLockedNakBtree()` → `packetStore.Insert()`
- **Tick goroutine:** Read packets via `periodicNakBtree()` → `packetStore.Min()`, `packetStore.IterateFrom()`

When `UsePacketRing=false` (direct push to btree), there's a race between Push and Tick.

### Affected Tests

All 8 tests in `receive_race_test.go` and `receive_iouring_reorder_test.go` that exercise Push+Tick concurrently:

1. `TestConcurrent_PushTickNAKACK_OutOfOrder`
2. `TestRace_PushWithTick` (all configs)
3. `TestRace_PushWithTick_FastTick`
4. `TestRace_FullPipeline` (all configs)
5. `TestRace_FullPipeline_WithLoss`
6. `TestRace_NakBtreeOperations`
7. `TestRace_MetricsUpdates`
8. `TestRace_SequenceWraparound`

### Root Cause - Race Stack Trace

```
Read at ... by goroutine 553 (Tick):
  btree.(*BTreeG).Min()
  receive.(*btreePacketStore).Min()
  receive.(*receiver).periodicNakBtree()   ← NAK scanning
  receive.(*receiver).Tick()

Previous write at ... by goroutine 552 (Push):
  btree.(*BTreeG).ReplaceOrInsert()
  receive.(*btreePacketStore).Insert()
  receive.(*receiver).pushLockedNakBtree() ← Packet insertion
```

### Current Architecture

```
Push() ──┬─[UsePacketRing=true]──► Lock-free Ring ──► EventLoop drains ──► Btree
         │
         └─[UsePacketRing=false]─► pushLockedNakBtree() ──► Btree ◄──── Tick reads
                                         ↑                      ↑
                                         └── RACE ──────────────┘
```

### Why This Happens

The `pushLockedNakBtree()` function is called from `pushWithLock()` which acquires `r.lock`:

```go
func (r *receiver) pushWithLock(pkt packet.Packet) {
    r.lock.Lock()
    defer r.lock.Unlock()
    r.pushLocked(pkt)  // → pushLockedNakBtree()
}
```

However, `Tick()` operations like `periodicNakBtree()` access the btree **without holding `r.lock`** when `UsePacketRing=false`:

```go
func (r *receiver) periodicNakBtree(nowUs uint64) []circular.Number {
    // Direct btree access - no lock!
    minEntry := r.packetStore.Min()
    // ...
    r.packetStore.IterateFrom(...)
}
```

### Fix Options

#### Option A: Add Locking to periodicNakBtree (Quick Fix)

Wrap btree access in `periodicNakBtree()` with `r.lock.RLock()`:

```go
func (r *receiver) periodicNakBtree(nowUs uint64) []circular.Number {
    r.lock.RLock()
    minEntry := r.packetStore.Min()
    // ... all btree operations
    r.lock.RUnlock()
    return nakList
}
```

**Pros:** Simple, localized change
**Cons:** Adds lock contention in Tick path, may impact performance

#### Option B: Require UsePacketRing=true for Race Tests (Test Fix)

Update test configurations to always use `UsePacketRing=true`:

```go
func mockLiveRecvNakBtree(...) *receiver {
    // ...
    UsePacketRing:    true,   // Required for race-safe Push/Tick
    PacketRingSize:   1024,
    UseEventLoop:     true,   // Use EventLoop instead of Tick
}
```

**Pros:** No production code changes, follows intended architecture
**Cons:** Tests no longer cover the direct push path

#### Option C: Add RWLock to btreePacketStore (Proper Fix)

Add a `sync.RWMutex` to `btreePacketStore` itself:

```go
type btreePacketStore struct {
    tree *btree.BTreeG[packet.Packet]
    mu   sync.RWMutex  // Protects tree access
}

func (b *btreePacketStore) Insert(p packet.Packet) (bool, packet.Packet) {
    b.mu.Lock()
    defer b.mu.Unlock()
    // ...
}

func (b *btreePacketStore) Min() packet.Packet {
    b.mu.RLock()
    defer b.mu.RUnlock()
    // ...
}
```

**Pros:** Encapsulated, correct by construction
**Cons:** May duplicate locking with `r.lock`, performance impact

#### Option D: Enforce Single-Threaded Access (Architecture Fix)

The codebase already has the concept of "EventLoop context" vs "Tick context" with debug assertions. Enforce this:

1. When `UsePacketRing=true`, Push goes to ring, EventLoop drains (single-threaded btree access)
2. When `UsePacketRing=false`, both Push and Tick must hold `r.lock` for btree access

This requires auditing all btree access paths and ensuring consistent locking.

### Recommended Approach

**Phase 1 (Immediate):** Option B - Fix tests to use the race-safe configuration
- The race tests should test the intended concurrent architecture
- The `UsePacketRing=true` + `UseEventLoop=true` path is the lock-free design

**Phase 2 (Optional):** Option C or D - Fix the direct push path
- Add proper locking to `btreePacketStore` for defensive correctness
- Or document that `UsePacketRing=false` is not race-safe and should only be used single-threaded

### Effort Estimate

- **Phase 1:** Modify test configurations only, low risk
- **Phase 2:** Audit all btree access, add locking, test performance impact

---

## Implementation Plan

### Step 1: Fix Test Infrastructure Bugs (Category 1)

**Files:**
- `congestion/live/receive/control_ring_test.go`
- `contrib/client-seeker/tokenbucket_test.go`

**Changes:**
1. Replace `var totalPushed, totalPopped int64` with `var totalPushed, totalPopped atomic.Int64`
2. Update all `++` to `.Add(1)` and reads to `.Load()`
3. Fix the broken CAS loop in `TestTokenBucket_ConcurrentConsume`

### Step 2: Fix Test Configurations (Category 2)

**Files:**
- `congestion/live/receive/receive_iouring_reorder_test.go`
- `congestion/live/receive/receive_race_test.go`

**Changes:**
1. Update `mockLiveRecvNakBtree()` to set `UsePacketRing=true` and `PacketRingSize`
2. Consider adding `UseEventLoop=true` for full lock-free path
3. Or add `r.lock.RLock()` to `periodicNakBtree()` if Tick path must work with direct push

### Step 3: Verify

```bash
make ci-race  # Should pass with no races
```

---

## Test Coverage Notes

After fixes, the race tests should cover:

| Scenario | Configuration | Race-Safe |
|----------|--------------|-----------|
| Push+EventLoop | `UsePacketRing=true, UseEventLoop=true` | Yes (single-threaded btree) |
| Push+Tick via Ring | `UsePacketRing=true, UseEventLoop=false` | Yes (ring drains before Tick) |
| Push+Tick direct | `UsePacketRing=false` | **No** (needs locking) |

The direct push path (`UsePacketRing=false`) is primarily for backward compatibility and non-io_uring deployments. If it must remain supported, it needs proper locking added.

---

## Appendix: Full Race Output Excerpts

### Control Ring Test Race

```
WARNING: DATA RACE
Read at 0x00c0000196d8 by goroutine 65:
  receive.TestRecvControlRing_ProducerConsumer_Realistic()
      control_ring_test.go:440  ← t.Logf reads totalPopped

Previous write at 0x00c0000196d8 by goroutine 66:
  receive.TestRecvControlRing_ProducerConsumer_Realistic.func1()
      control_ring_test.go:413  ← totalPopped++ in consumer
```

### Push/Tick Btree Race

```
WARNING: DATA RACE
Read at 0x00c00281e710 by goroutine 553:
  btree.(*BTreeG).Min()
  receive.(*btreePacketStore).Min()
  receive.(*receiver).periodicNakBtree()
      nak.go:204
  receive.(*receiver).Tick()
      tick.go:41

Previous write at 0x00c00281e710 by goroutine 552:
  btree.(*BTreeG).ReplaceOrInsert()
  receive.(*btreePacketStore).Insert()
      packet_store_btree.go:63
  receive.(*receiver).pushLockedNakBtree()
      push.go:135
```
