# NAK Btree Lockless Refactoring

> **Document Purpose:** Track refactoring of `nak_btree.go` to follow the lockless design pattern.
> **Parent Document:** `gosrt_lockless_design.md`
> **Status:** ✅ COMPLETE

---

## Overview

Refactored `congestion/live/receive/nak_btree.go` to follow the lockless design pattern:
- **Lock-free core functions** for event loop (single-threaded)
- **Locking wrappers** for tick mode / concurrent access
- **Function dispatch** configured at receiver initialization based on `UsePacketRing`

This aligns with the pattern established in `gosrt_lockless_design.md`.

---

## Functions Refactored

| Function | Lock-Free | Locking Wrapper | Status |
|----------|-----------|-----------------|--------|
| `Insert` | ✅ | `InsertLocking` | ✅ Complete |
| `InsertBatch` | ✅ | `InsertBatchLocking` | ✅ Complete |
| `Len` | ✅ | `LenLocking` | ✅ Complete |
| `Iterate` | ✅ | `IterateLocking` | ✅ Complete |
| `IterateAndUpdate` | ✅ | `IterateAndUpdateLocking` | ✅ Complete |
| `IterateDescending` | ✅ | `IterateDescendingLocking` | ✅ Complete |
| `Min` | ✅ | `MinLocking` | ✅ Complete |
| `Max` | ✅ | `MaxLocking` | ✅ Complete |
| `Has` | ✅ | `HasLocking` | ✅ Complete |
| `Clear` | ✅ | `ClearLocking` | ✅ Complete |
| `Delete` | ✅ | `DeleteLocking` | ✅ (Already done) |
| `DeleteBefore` | ✅ | `DeleteBeforeLocking` | ✅ (Already done) |

---

## Function Dispatch Implementation

### receiver struct fields
```go
// NAK btree function dispatch (configured once based on usePacketRing)
// Event loop mode (usePacketRing=true): lock-free versions for single-threaded access
// Tick mode (usePacketRing=false): locking versions for concurrent Push/Tick safety
nakInsert           func(seq uint32)
nakInsertBatch      func(seqs []uint32) int
nakDelete           func(seq uint32) bool
nakDeleteBefore     func(cutoff uint32) int
nakLen              func() int
nakIterateAndUpdate func(fn func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool))
```

### setupNakDispatch()
```go
// setupNakDispatch configures NAK btree function dispatch based on execution mode.
// Called once at receiver initialization for zero runtime overhead.
//
// In event loop mode (usePacketRing=true):
//   - After draining ring, Tick has exclusive access to nakBtree
//   - Use lock-free versions for maximum performance
//
// In tick mode (usePacketRing=false):
//   - Push and Tick can run concurrently
//   - Use locking versions for thread safety
func (r *receiver) setupNakDispatch(usePacketRing bool) {
    if usePacketRing {
        // Event loop mode: lock-free
        r.nakInsert = r.nakBtree.Insert
        r.nakInsertBatch = r.nakBtree.InsertBatch
        // ... etc
    } else {
        // Tick mode: locking
        r.nakInsert = r.nakBtree.InsertLocking
        r.nakInsertBatch = r.nakBtree.InsertBatchLocking
        // ... etc
    }
}
```

---

## Implementation Steps - Completed

### Step 1: Refactor `nak_btree.go` Functions ✅
- [x] All functions now have lock-free core + `*Locking` wrapper

### Step 2: Add Function Dispatch to receiver ✅
- [x] Added function pointer fields to `receiver` struct
- [x] Added `setupNakDispatch()` method
- [x] Called from `New()` after nakBtree creation

### Step 3: Update Production Call Sites ✅
- [x] `nak.go` - Uses dispatch functions
- [x] `fast_nak.go` - Uses dispatch functions
- [x] `nak_consolidate.go` - Uses dispatch functions
- [x] `ring.go` - Uses dispatch functions
- [x] `tick.go` - Uses dispatch functions

### Step 4: Update Test Call Sites ✅
- [x] All test files updated to use `*Locking` versions
- [x] Test helper functions call `setupNakDispatch(false)`

### Step 5: Final Verification ✅
- [x] `go build ./congestion/live/receive/...` - PASS
- [x] `go test ./congestion/live/receive/...` - PASS (55.979s)
- [x] Race tests pass

---

## Key Design Decisions

### 1. Function Dispatch vs. Direct Calls
Instead of checking `usePacketRing` at every call site, we configure function pointers once at startup. This provides:
- **Zero runtime overhead** - just a function pointer call
- **Config-driven** - automatically selects correct version
- **Clean separation** - no conditionals in hot paths

### 2. Nil Safety
Added nil checks for dispatch functions to handle tests that create receivers directly without calling `setupNakDispatch()`:
- `fast_nak.go:136` - checks `r.nakInsert != nil`
- `fast_nak.go:189` - checks `r.nakLen == nil`
- `nak_consolidate.go:48` - checks `r.nakLen == nil`
- `nak_consolidate.go:90` - checks `r.nakIterateAndUpdate == nil`

### 3. Test Updates
All test helper functions that create receivers with nakBtree now call `setupNakDispatch(false)` to use locking versions (safe for concurrent test execution).

---

## Performance Impact

| Mode | Lock Operations | Overhead |
|------|-----------------|----------|
| Event Loop (`usePacketRing=true`) | None | Zero lock overhead |
| Tick Mode (`usePacketRing=false`) | Per-call | Normal mutex overhead |

The function dispatch adds only a function pointer call (typically 1-2ns) compared to a direct call.

---

## Files Modified

| File | Changes |
|------|---------|
| `nak_btree.go` | All functions split into lock-free + locking |
| `receiver.go` | Added dispatch fields + `setupNakDispatch()` |
| `nak.go` | Uses dispatch functions |
| `fast_nak.go` | Uses dispatch functions + nil checks |
| `nak_consolidate.go` | Uses dispatch functions + nil checks |
| `ring.go` | Uses dispatch functions |
| `tick.go` | Uses dispatch functions |
| `nak_btree_test.go` | Concurrent tests use `*Locking` |
| `nak_consolidate_test.go` | All tests call `setupNakDispatch()` |
| `nak_consolidate_table_test.go` | All tests call `setupNakDispatch()` |
| `fast_nak_test.go` | Helper calls `setupNakDispatch()` |
| `fast_nak_table_test.go` | All tests call `setupNakDispatch()` |
| `hotpath_bench_test.go` | Benchmarks both lock-free and locking |
| `receive_iouring_reorder_test.go` | Uses `LenLocking()` |
| `nak_periodic_table_test.go` | Uses `InsertLocking()` |

---

## Progress Log

### Session: January 2026

**Completed:**
1. Refactored all `nak_btree.go` functions to lockless pattern
2. Added function dispatch to receiver struct
3. Updated all production call sites
4. Updated all test files
5. Verified with `go test ./congestion/live/receive/...` - PASS


