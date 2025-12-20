# Lock Analysis Tools - Implementation Progress

This document tracks the implementation progress of the lock analysis tools as designed in [`metrics_lock_analysis_design.md`](./metrics_lock_analysis_design.md).

## Overview

Two complementary static analysis tools for understanding lock usage:

1. **`metrics-lock-analyzer`** - Finds metrics that are incremented UNDER lock but could be moved OUTSIDE lock
2. **`lock-requirements-analyzer`** - Finds operations that MUST be protected by locks (btree operations, rate counters)

**Tool Locations**:
- `tools/metrics-lock-analyzer/main.go`
- `tools/lock-requirements-analyzer/main.go`

---

## Tools Summary

### Tool 1: metrics-lock-analyzer

**Purpose**: Identify atomic metric operations that are unnecessarily inside lock scopes.

**Latest Run Results** (2025-12-17):
```
=== GoSRT Metrics Lock Analyzer ===
  Scanned 78 .go files
  Found 331 lock operations
  Found 255 metric operations (.Add/.Store)
  Found 184 lock scopes
  Found 25 functions called under lock
  Found 44 metrics under lock (17%)
```

### Tool 2: lock-requirements-analyzer

**Purpose**: Identify operations on protected resources (btrees, rate counters) and verify they're within lock scopes.

**Latest Run Results** (2025-12-17, after bug fix):
```
=== GoSRT Lock Requirements Analyzer ===
  Scanned 78 files
  Found 93 protected operations
  Found 36 functions under lock
  Protected operations in lock: 43
  Potentially unprotected: 50
```

**Summary by Resource** (after bug fix):

| Resource | Protected | Unprotected | Needs Review |
|----------|-----------|-------------|--------------|
| Packet Store (btree) | 11 | 4 | 0 |
| NAK Btree | 5 | 1 | 3 |
| Rate Counters | 9 | 36 | 0 |
| Running Averages | 5 | 2 | 0 |
| Sequence Numbers | 6 | 3 | 0 |
| Loss List (sender) | 4 | 4 | 0 |

---

## Analysis of "Unprotected" Findings

### Category 1: False Positives - Constructors (SAFE)

Operations in constructor functions are safe - no concurrency exists yet:

| File | Function | Operations | Status |
|------|----------|------------|--------|
| `receive.go` | `NewReceiver` | `r.nakBtree`, `r.rate.*` | ✅ SAFE - initialization |
| `send.go` | `NewSender` | `s.rate.*` | ✅ SAFE - initialization |

### Category 2: False Positives - Test/Fake Code (SAFE)

Mock implementations don't need thread safety:

| File | Function | Status |
|------|----------|--------|
| `fake.go` | All functions | ✅ SAFE - fake implementation |
| `contrib/*` | Various | ✅ SAFE - test utilities |

### Category 3: BUG FIXED - `*Locked` Functions Now Detected ✅

**Issue (FIXED)**: Functions with `Locked` in their name (not just suffix) are now recognized.

**Root Cause**: Was using `HasSuffix("Locked")` but function names like `pushLockedNakBtree` have "Locked" in the middle.

**Fix**: Changed to `Contains("Locked")` in both analyzers.

| File | Function | Status |
|------|----------|--------|
| `receive.go` | `pushLockedNakBtree` | ✅ Now detected |
| `receive.go` | `pushLockedOriginal` | ✅ Now detected |
| `send.go` | `tickDeliverPackets` etc. | ✅ Now detected |

### Category 4: True Findings - Need Review

Operations that may actually be unprotected:

| File | Function | Operation | Status |
|------|----------|-----------|--------|
| `receive.go` | `Flush` | `packetStore.Clear()` | ⚠️ REVIEW - called without lock? |
| `receive.go` | `periodicACK` | `packetStore.Min()`, `IterateFrom()` | ⚠️ Uses RLock - likely OK |
| `receive.go` | `periodicNakBtree` | `packetStore.IterateFrom()` | ⚠️ Uses RLock - likely OK |
| `fast_nak.go` | `checkFastNakRecent` | `nakBtree.Insert()` | ⚠️ REVIEW - potential race |
| `send.go` | `Flush` | `lossList.Init()` | ⚠️ REVIEW - called without lock? |

### Category 5: NAK Btree Internal Lock (NEEDS REVIEW)

NAK btree has its own internal mutex - some operations may be safe without external lock:

| Operation | Has Internal Lock | External Lock Needed? |
|-----------|-------------------|----------------------|
| `InsertBatch` | ✅ Yes | Probably not |
| `DeleteBefore` | ✅ Yes | Probably not |
| `Len` | ✅ Yes | Probably not |
| `Delete` | ✅ Yes | ⚠️ Need to verify atomicity with packet store |

---

## Implementation Phases

### Phase 1: Core Scanner ✅ Complete

**Status**: ✅ Complete

**Deliverables**:
- [x] `tools/metrics-lock-analyzer/main.go` - Finds metrics under lock
- [x] `tools/lock-requirements-analyzer/main.go` - Finds protected operations
- [x] AST parsing for lock operations
- [x] AST parsing for metric operations
- [x] AST parsing for protected resource operations
- [x] Basic lock scope correlation
- [x] Report generation

---

### Phase 2: Call Flow Analysis ✅ Mostly Complete

**Status**: ✅ Mostly Complete

**Fixed Issues**:
1. ✅ `*Locked*` functions now detected (changed `HasSuffix` to `Contains`)
2. ✅ Functions called FROM `*Locked` functions now propagated
3. ⚠️ `periodicACK` uses RLock - analyzer doesn't distinguish read vs write locks (low priority)

**Tasks**:
- [x] Build call graph from AST
- [x] Track functions called under lock
- [x] Propagate lock context transitively
- [x] **FIXED**: Improve `*Locked` function detection (use `Contains` not `HasSuffix`)
- [x] **FIXED**: Track functions called from `*Locked` without `Locked` suffix
- [ ] **LOW**: Distinguish RLock vs Lock protection (nice to have)

---

### Phase 3: Transformation Generator

**Status**: ⏳ Pending

**Tasks**:
- [ ] Implement dependency analysis for metric arguments
- [ ] Generate stack variable proposals
- [ ] Generate code transformation suggestions

**Dependencies**: Phase 2 bugs fixed

---

### Phase 4: Report Generator

**Status**: ⏳ Pending

**Tasks**:
- [ ] Markdown report with full details
- [ ] JSON output for tooling integration
- [ ] Filter out false positives (constructors, test code)

---

### Phase 5: Integration

**Status**: ⏳ Pending

**Tasks**:
- [ ] Add to Makefile (`make analyze-locks`)
- [ ] Add CI check (optional - warning only)

---

## Progress Log

### 2025-12-17

**Phase 1 Complete, Phase 2 Bug Fixed**

- Created `tools/metrics-lock-analyzer/main.go`
- Created `tools/lock-requirements-analyzer/main.go`
- Implemented core AST scanning infrastructure
- Implemented lock scope detection
- Implemented protected resource detection
- Ran initial analysis on codebase
- **Identified bug** in `*Locked` function detection
- **Fixed bug**: Changed `HasSuffix("Locked")` to `Contains("Locked")` in both tools

**Results Before Fix**: 27 protected, 66 unprotected
**Results After Fix**: 43 protected, 50 unprotected (+16 detected)

**Remaining "unprotected" (50 total)**:
- ~15 in constructors (safe - initialization before concurrency)
- ~20 in test/fake code (safe - mock implementations)
- ~4 packet store ops in `periodicACK`/`periodicNakBtree` (uses RLock - likely OK)
- ~4 in `Flush` functions (need review)
- ~1 in `fast_nak.go` (need review)
- ~6 other misc (need review)

---

## Usage

```bash
# Run metrics-lock-analyzer
go run tools/metrics-lock-analyzer/main.go

# Run lock-requirements-analyzer
go run tools/lock-requirements-analyzer/main.go

# Analyze specific files
go run tools/lock-requirements-analyzer/main.go congestion/live/receive.go
```

---

## Protected Resources Configuration

The `lock-requirements-analyzer` tracks these resources:

```go
// Packet Store (btree)
Fields: [packetStore]
Methods: [Insert Has Min Clear Iterate IterateFrom RemoveAll Remove]
Why: Packet reordering buffer - concurrent access causes data races

// NAK Btree
Fields: [nakBtree]
Methods: [Insert InsertBatch Delete DeleteBefore Iterate Len]
Why: Missing sequence tracking - concurrent modification causes corruption

// Rate Counters
Fields: [rate]
Why: Rate statistics - non-atomic updates cause data races (TODO: migrate to atomics)

// Running Averages
Fields: [avgPayloadSize avgLinkCapacity]
Why: EMA calculations - non-atomic read-modify-write causes races (TODO: migrate to atomics)

// Sequence Numbers
Fields: [lastACKSequenceNumber lastDeliveredSequenceNumber maxSeenSequenceNumber ackScanHighWaterMark]
Why: Protocol state - must be consistent with packet store operations

// Loss List (sender)
Fields: [lossList]
Methods: [Push Pop Front Init Len]
Why: Sender loss tracking - concurrent access causes corruption
```

---

## Next Steps

1. **Fix Phase 2 bugs** - Improve `*Locked` function detection
2. **Add false positive filtering** - Skip constructors, test code
3. **Verify true findings** - Manual review of the ~11 potentially real issues
4. **Document rate counter migration plan** - 40 rate counter operations need atomics
