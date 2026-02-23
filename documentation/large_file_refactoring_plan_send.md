# Large File Refactoring Plan: send.go → send/

**Created:** 2026-01-01
**Status:** ✅ COMPLETE
**Related:** `large_file_refactoring_plan.md` (reference for methodology)

---

## 1. Executive Summary

This document outlines a plan to refactor `congestion/live/send.go` (584 lines, 19KB) into a `congestion/live/send/` subpackage, following the successful pattern established with `congestion/live/receive/`.

**Goals:**
- Files < 400 lines each (matching receive/ convention)
- Single responsibility per file
- Clear subpackage boundary
- Safe, incremental migration
- No regressions (tests pass after each step)

---

## 2. Current State Analysis

### 2.1 File Statistics

| Metric | Value |
|--------|-------|
| File | `congestion/live/send.go` |
| Lines | 584 |
| Bytes | 19,083 |
| Functions | 18 |

### 2.2 Function Inventory

```
send.go responsibilities:
├── Configuration
│   ├── SendConfig struct (lines 16-29)
│   └── sender struct (lines 32-57)
├── Lifecycle
│   ├── NewSender() (lines 60-94)
│   └── Flush() (lines 151-157)
│   └── SetDropThreshold() (lines 578-583)
├── Statistics
│   └── Stats() (lines 96-149)
├── Packet Push
│   ├── Push() (lines 159-169)
│   └── pushLocked() (lines 171-215)
├── Tick/Timing
│   ├── Tick() (lines 217-246)
│   ├── tickDeliverPackets() (lines 248-280)
│   ├── tickDropOldPackets() (lines 282-314)
│   └── tickUpdateRateStats() (lines 316-353)
├── ACK Handling
│   ├── ACK() (lines 355-365)
│   └── ackLocked() (lines 367-403)
└── NAK Handling
    ├── NAK() (lines 406-421)
    ├── nakLocked() (lines 424-429)
    ├── isNakBeforeACK() (lines 436-438)
    ├── checkNakBeforeACK() (lines 443-450)
    ├── nakLockedOriginal() (lines 457-509)
    └── nakLockedHonorOrder() (lines 515-576)
```

### 2.3 Dependencies

```
send.go imports:
├── container/list       # For packetList, lossList
├── math                 # For Float64bits
├── sync                 # For RWMutex
├── time                 # For duration calculations
├── github.com/randomizedcoder/gosrt/circular    # Sequence numbers
├── github.com/randomizedcoder/gosrt/congestion  # Sender interface
├── github.com/randomizedcoder/gosrt/metrics     # Metrics
└── github.com/randomizedcoder/gosrt/packet      # Packet types
```

### 2.4 Interface Implementation

```go
// congestion/sender.go
type Sender interface {
    Stats() SendStats
    Flush()
    Push(p packet.Packet)
    Tick(now uint64)
    ACK(sequenceNumber circular.Number)
    NAK(sequenceNumbers []circular.Number) uint64
    SetDropThreshold(threshold uint64)
}
```

---

## 3. Comparison with receive/ Pattern

### 3.1 receive/ Structure (Reference)

| File | Lines | Responsibility |
|------|-------|----------------|
| `receiver.go` | 459 | Core struct, NewReceiver, interface |
| `ack.go` | 346 | ACK generation and handling |
| `nak.go` | 556 | NAK generation |
| `nak_btree.go` | 145 | NAK btree data structure |
| `nak_consolidate.go` | 143 | NAK consolidation logic |
| `fast_nak.go` | 195 | Fast NAK optimization |
| `push.go` | 276 | Push packet handling |
| `scan.go` | 342 | Contiguous scan logic |
| `tick.go` | 497 | Tick/timing functions |
| `ring.go` | 353 | Ring buffer |
| `packet_store.go` | 174 | Store interface |
| `packet_store_btree.go` | 216 | Btree store impl |

### 3.2 Proposed send/ Structure

| File | Est. Lines | Responsibility |
|------|------------|----------------|
| `sender.go` | ~120 | Core struct, NewSender, interface impl |
| `stats.go` | ~60 | Stats() function |
| `push.go` | ~70 | Push(), pushLocked() |
| `tick.go` | ~120 | Tick(), tickDeliver*, tickDrop*, tickUpdate* |
| `ack.go` | ~60 | ACK(), ackLocked() |
| `nak.go` | ~180 | NAK(), nakLocked*, isNakBeforeACK, checkNakBeforeACK |

**Total: ~610 lines** (slight overhead from file headers/imports, ~4% increase)

---

## 4. Detailed Migration Plan

### 4.1 Key Principles (from large_file_refactoring_plan.md)

1. **Same-package preferred for tightly coupled code** - receive/ works as subpackage because it's self-contained
2. **Extract code unchanged** - Don't "clean up" during extraction
3. **Verify after each step** - Build + test after every file
4. **Count functions** - Ensure no functions lost

### 4.2 Decision: Subpackage vs Same-Package

**Recommendation: Subpackage (`send/`)**

**Rationale:**
- `receive/` already exists as a subpackage - consistency
- `sender` struct has minimal external dependencies
- `Sender` interface is clean boundary
- Test files (`send_test.go`, `send_table_test.go`) are already large (33KB combined)
- Future: potential for send-specific optimizations (like receive's btree/ring)

**Interface Contract:**
```go
// congestion/live/send/sender.go
package send

type SendConfig struct { ... }  // Keep SendConfig name for clarity when multiple configs exist

func NewSender(config SendConfig) congestion.Sender { ... }
```

### 4.3 Migration Steps

#### Phase 1: Create Directory and Move Core (sender.go)

```bash
mkdir -p congestion/live/send

# Create sender.go with:
# - package send
# - Config struct (rename SendConfig → Config)
# - sender struct
# - NewSender()
# - Flush()
# - SetDropThreshold()
```

**Verification:**
```bash
go build ./congestion/live/send/...
```

#### Phase 2: Extract stats.go

Move `Stats()` function to `send/stats.go`.

**Verification:**
```bash
go build ./congestion/live/send/...
go test ./congestion/live/...
```

#### Phase 3: Extract push.go

Move `Push()` and `pushLocked()` to `send/push.go`.

**Verification:**
```bash
go build ./congestion/live/send/...
go test ./congestion/live/...
```

#### Phase 4: Extract tick.go

Move tick functions to `send/tick.go`:
- `Tick()`
- `tickDeliverPackets()`
- `tickDropOldPackets()`
- `tickUpdateRateStats()`

**Verification:**
```bash
go build ./congestion/live/send/...
go test ./congestion/live/...
```

#### Phase 5: Extract ack.go

Move ACK functions to `send/ack.go`:
- `ACK()`
- `ackLocked()`

**Verification:**
```bash
go build ./congestion/live/send/...
go test ./congestion/live/...
```

#### Phase 6: Extract nak.go

Move NAK functions to `send/nak.go`:
- `NAK()`
- `nakLocked()`
- `isNakBeforeACK()`
- `checkNakBeforeACK()`
- `nakLockedOriginal()`
- `nakLockedHonorOrder()`

**Verification:**
```bash
go build ./congestion/live/send/...
go test ./congestion/live/...
```

#### Phase 7: Update Imports and Delete Original

1. Update all imports from `congestion/live` → `congestion/live/send`
2. Update `SendConfig` → `send.Config` references
3. Delete original `send.go`
4. Move test files to `send/` directory

**Full Verification:**
```bash
go build ./...
go test ./...
make test-race
```

---

## 5. Test File Migration

### 5.1 Current Test Files

| File | Lines | Purpose |
|------|-------|---------|
| `send_test.go` | 18,686 | Unit tests |
| `send_table_test.go` | 14,865 | Table-driven NAK tests |
| **Total** | **33,551** | |

### 5.2 Proposed Test Structure

| File | Purpose |
|------|---------|
| `send/sender_test.go` | Core tests (NewSender, Flush, SetDropThreshold) |
| `send/stats_test.go` | Stats tests |
| `send/push_test.go` | Push tests |
| `send/tick_test.go` | Tick tests |
| `send/ack_test.go` | ACK tests |
| `send/nak_test.go` | NAK tests (incl. isNakBeforeACK, checkNakBeforeACK) |
| `send/nak_table_test.go` | Table-driven NAK strategy tests |

---

## 6. Import Updates Required

### 6.1 Files That Import send.go

```bash
# Find all files importing congestion/live that use sender
grep -r "live.NewSender\|live.SendConfig" --include="*.go" .
```

Expected files:
- `connection.go` - creates sender
- `connection_handlers.go` - uses sender
- Potentially others

### 6.2 Update Pattern

**Before:**
```go
import "github.com/randomizedcoder/gosrt/congestion/live"

sender := live.NewSender(live.SendConfig{...})
```

**After:**
```go
import "github.com/randomizedcoder/gosrt/congestion/live/send"

sender := send.NewSender(send.SendConfig{...})
```

---

## 7. Verification Checklist

### After EACH File Extraction:

- [ ] `go build ./congestion/live/send/...` passes
- [ ] `go test ./congestion/live/...` passes
- [ ] Function count unchanged: `grep -E "^func " send/*.go | wc -l`
- [ ] No code modified during extraction (pure move)

### After Phase Complete:

- [ ] `go test ./...` passes (all packages)
- [ ] `go vet ./...` passes
- [ ] Function diff clean
- [ ] Line count reasonable: original ± 10%
- [ ] Coverage unchanged

### After ALL Phases Complete:

- [ ] `make test` passes
- [ ] `make test-race` passes
- [ ] Integration tests pass: `make test-parallel`
- [ ] Benchmarks show no regression
- [ ] Documentation updated

---

## 8. Risk Assessment

### 8.1 Low Risk

- **Circular imports unlikely** - send/ has same dependencies as receive/
- **Interface stability** - `Sender` interface is well-defined
- **Test coverage good** - 33K lines of tests

### 8.2 Medium Risk

- **Import updates** - Need to update all callers of `live.NewSender`

### 8.3 Mitigation

```go
// Option: Type alias in congestion/live for backward compatibility
// congestion/live/send_compat.go
package live

import "github.com/randomizedcoder/gosrt/congestion/live/send"

// Deprecated: Use send.SendConfig directly
type SendConfig = send.SendConfig

// Deprecated: Use send.NewSender directly
func NewSender(config SendConfig) congestion.Sender {
    return send.NewSender(config)
}
```

**Note:** Keeping `SendConfig` name (not renaming to `Config`) for clarity when multiple config types exist in the codebase.

---

## 9. Timeline Estimate

| Phase | Description | Est. Time |
|-------|-------------|-----------|
| 1 | Create send/ + sender.go | 15 min |
| 2 | Extract stats.go | 10 min |
| 3 | Extract push.go | 10 min |
| 4 | Extract tick.go | 15 min |
| 5 | Extract ack.go | 10 min |
| 6 | Extract nak.go | 15 min |
| 7 | Update imports + cleanup | 20 min |
| 8 | Move tests | 30 min |
| **Total** | | **~2 hours** |

---

## 10. Success Metrics

- [ ] No file > 400 lines (matching receive/ convention)
- [ ] Each file has single responsibility
- [ ] Test coverage maintained
- [ ] No circular imports
- [ ] All existing tests pass
- [ ] Benchmarks show no regression
- [ ] Documentation updated

---

## 11. Implementation Status

| Phase | Status | Notes |
|-------|--------|-------|
| Planning | ✅ Complete | This document |
| Phase 1-6 | ✅ Complete | All source files created |
| Phase 7 | ✅ Complete | Imports updated, original deleted |
| Phase 8 | ✅ Complete | Tests moved and passing |

### Final Results

**Source Files Created:**

| File | Lines | Responsibility |
|------|-------|----------------|
| `sender.go` | 109 | Core struct, NewSender, Flush, SetDropThreshold |
| `stats.go` | 61 | Stats() |
| `push.go` | 64 | Push(), pushLocked() |
| `tick.go` | 147 | Tick(), tickDeliver*, tickDrop*, tickUpdate* |
| `ack.go` | 59 | ACK(), ackLocked() |
| `nak.go` | 180 | NAK handling, isNakBeforeACK, checkNakBeforeACK |
| **Total** | **620** | +6.2% overhead from file headers |

**Test Files Moved:**
- `sender_test.go` (was `send_test.go`)
- `nak_table_test.go` (was `send_table_test.go`)

**Files Updated:**
- `connection.go` - import changed to `send.NewSender(send.SendConfig{...})`
- `congestion/live/metrics_test.go` - import and usage updated

**Verification:**
- ✅ `go build ./...` passes
- ✅ `go test ./...` passes (all packages)
- ✅ Function count: 18 functions preserved

---

## 12. Appendix: Function Line Count

| Function | Lines | Target File |
|----------|-------|-------------|
| `SendConfig` struct | 14 | sender.go |
| `sender` struct | 26 | sender.go |
| `NewSender()` | 35 | sender.go |
| `Flush()` | 7 | sender.go |
| `SetDropThreshold()` | 6 | sender.go |
| `Stats()` | 54 | stats.go |
| `Push()` | 11 | push.go |
| `pushLocked()` | 45 | push.go |
| `Tick()` | 30 | tick.go |
| `tickDeliverPackets()` | 33 | tick.go |
| `tickDropOldPackets()` | 33 | tick.go |
| `tickUpdateRateStats()` | 38 | tick.go |
| `ACK()` | 11 | ack.go |
| `ackLocked()` | 37 | ack.go |
| `NAK()` | 16 | nak.go |
| `nakLocked()` | 6 | nak.go |
| `isNakBeforeACK()` | 8 | nak.go |
| `checkNakBeforeACK()` | 8 | nak.go |
| `nakLockedOriginal()` | 53 | nak.go |
| `nakLockedHonorOrder()` | 62 | nak.go |
| **Total** | **584** | |

---

## 13. References

- `large_file_refactoring_plan.md` - Methodology and lessons learned
- `congestion/live/receive/` - Reference subpackage structure
- `receive_testing_strategy.md` - Testing approach for subpackages

