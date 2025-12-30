# Large File Refactoring Plan

## Executive Summary

This document outlines a plan to refactor large `.go` files into smaller, more manageable subpackages, following the successful pattern established with `congestion/live/receive/`.

---

## Phase 1A Complete: connection.go Split (2025-12-30)

### Summary

Successfully split `connection.go` (2437 lines) into 9 smaller files within the main `srt` package.

| File | Lines | Functions |
|------|-------|-----------|
| `connection.go` | 600 | Core struct, interface, newSRTConn |
| `connection_rtt.go` | 66 | RTT calculation (4 funcs) |
| `connection_io.go` | 263 | Read/Write/push/pop/deliver |
| `connection_handlers.go` | 455 | ACK/NAK/ACKACK/keepalive handlers |
| `connection_handshake.go` | 276 | HS request/response |
| `connection_keymgmt.go` | 197 | KM request/response |
| `connection_send.go` | 252 | sendNAK/sendACK/sendShutdown |
| `connection_lifecycle.go` | 203 | Close/watchPeerIdleTimeout |
| `connection_stats.go` | 231 | Stats/printCloseStatistics |
| **Total** | **2543** | **63 functions** |

**Result:** +4.3% lines (overhead from file headers/imports), **0 functions lost**

### Lessons Learned

#### 1. Subpackage vs Same-Package Split

**Problem:** Initial attempt to create `connection/` subpackage failed due to circular imports:
- `srtConn` depends on `Config` (defined in `config.go`)
- `Conn` interface must remain in public API
- Handlers reference main package types

**Solution:** Split into multiple files within `package srt` instead of creating subpackage.

**Recommendation for future phases:**
- Config, Dial, Listen have similar dependencies on main package types
- **Prefer same-package split** (Phase 1A approach) over subpackages
- Reserve subpackages for truly isolated code (like `receive/`)

#### 2. Complete Function Extraction

**Problem:** Simplified Stats() function during extraction, breaking TestStats.

**Root cause:** Tried to "clean up" code while extracting instead of pure move.

**Lesson:** **Extract code exactly as-is first**, then refactor in separate commits:
```
Step 1: Move code unchanged (verify tests pass)
Step 2: Refactor moved code (verify tests pass)
```

#### 3. Verification After Each Step

**What worked well:**
- `go build ./...` after each file creation
- `go test .` after completing extraction
- Function count comparison (63 before = 63 after)

**Add to checklist:**
- `git status` to track new files
- `diff <(original funcs) <(new funcs)` to verify no loss

#### 4. Documentation Accuracy

**Problem:** Miscalculated line counts (claimed 13% reduction, was actually 4.3% increase).

**Lesson:** Always verify numbers with actual commands before documenting:
```bash
# Before extraction
git show HEAD:connection.go | wc -l

# After extraction
wc -l connection*.go | grep -v test
```

---

## Design Decision: Metrics Package

**Decision:** Keep `metrics/` package separate; do NOT move metrics during subpackage refactoring.

**Rationale:**
1. **Lower risk** - One refactoring concern at a time
2. **Easier rollback** - If something breaks, you know which change caused it
3. **Metrics are cross-cutting** - Used by listener, dialer, server, not just connection
4. **Current design works** - `metrics/` is well-tested (5,462 lines) and functional
5. **Can revisit later** - Once connection/ is stable, can evaluate if metrics should move

**Implementation:**
- `connection/` will import `metrics.ConnectionMetrics` from `metrics/`
- No changes to metrics package during Phase 1-4
- Separate evaluation after all subpackages complete

---

## Baseline Test Status (Before Refactoring)

**Date:** 2025-12-30
**Status:** ✅ ALL TESTS PASSING

| Package | Tests | Status |
|---------|-------|--------|
| `github.com/randomizedcoder/gosrt` | 85 | ✅ PASS |
| `github.com/randomizedcoder/gosrt/metrics` | 4 | ✅ PASS |
| `github.com/randomizedcoder/gosrt/contrib/integration_testing` | 4 | ✅ PASS |

**Connection-specific tests verified:**
- `TestConnectionMetricsACKFlow` ✅
- `TestConnectionMetricsNAKRetransmit` ✅
- `TestConnectionMetricsControlPackets` ✅
- `TestConnectionMetricsPrometheusMatch` ✅
- `TestConnection_WriteAfterClose` ✅
- `TestConnection_ReadAfterClose` ✅
- `TestConnectionLifecycle*` (14 table-driven tests) ✅
- `TestServer_ConcurrentConnections` ✅

**Implementation Tracking:** See `connection_subpackage_implementation.md`

---

**Goals:**
- Files < 600 lines each (matching receive/ convention)
- Single responsibility per file
- Clear subpackage boundaries
- Safe, incremental migration
- No regressions (tests pass after each step)

---

## 1. Current State Analysis

### Files by Size (Lines of Code)

| File | Lines | Bytes | Priority | Status |
|------|-------|-------|----------|--------|
| `connection.go` | ~~2,437~~ → 600 | 82K → 21K | ~~CRITICAL~~ | ✅ SPLIT into 9 files |
| `config.go` | ~~1,209~~ → 525 | 37K → 17K | ~~HIGH~~ | ✅ SPLIT into 3 files |
| `dial.go` | ~~1,043~~ → 350 | 27K → 11K | ~~MEDIUM~~ | ✅ SPLIT into 3 files |
| `listen_linux.go` | 954 | 30K | LOW | Platform-specific, keep as-is |
| `listen.go` | ~~708~~ → 346 | 23K → 11K | ~~MEDIUM~~ | ✅ SPLIT into 4 files |
| `dial_linux.go` | 614 | 17K | LOW | Near target |
| `connection_linux.go` | 598 | 19K | LOW | Near target |
| `conn_request.go` | 551 | 18K | LOW | Near target |

### Complexity Analysis

```
connection.go responsibilities:
├── RTT calculation (rtt struct, methods)
├── Connection lifecycle (new, close, shutdown)
├── Packet I/O (Read, Write, push, pop)
├── Control handlers:
│   ├── handleACK, sendACK
│   ├── handleNAK, sendNAK
│   ├── handleACKACK, sendACKACK
│   ├── handleKeepAlive, sendProactiveKeepalive
│   └── handleShutdown, sendShutdown
├── Handshake (handleHSRequest, handleHSResponse, sendHSRequest)
├── Key management (handleKMRequest, handleKMResponse, sendKMRequest)
├── Statistics (GetExtendedStatistics, printCloseStatistics)
└── Ticker and goroutines
```

---

## 2. Proposed Subpackage Structure

### Phase 1: connection.go Split ✅ COMPLETE

**Status:** COMPLETE (2025-12-30)

**Approach Changed:** Same-package split instead of subpackage (avoids circular imports).

**Final Structure:**
```
./ (package srt)
├── connection.go          # 600 lines - Core struct, interface, newSRTConn
├── connection_rtt.go      # 66 lines - RTT calculation
├── connection_io.go       # 263 lines - Read/Write/push/pop/deliver
├── connection_handlers.go # 455 lines - ACK/NAK/ACKACK/keepalive handlers
├── connection_handshake.go # 276 lines - HS request/response
├── connection_keymgmt.go  # 197 lines - KM request/response
├── connection_send.go     # 252 lines - sendNAK/sendACK/sendShutdown
├── connection_lifecycle.go # 203 lines - Close/watchPeerIdleTimeout
└── connection_stats.go    # 231 lines - Stats/printCloseStatistics
```

**Results:**
- Original: 2437 lines, 63 functions
- After: 2543 lines (+4.3%), 63 functions (unchanged)
- All 85 tests passing

**See:** `connection_subpackage_implementation.md` for detailed progress

### Phase 2: config.go Split ✅ COMPLETE

**Status:** COMPLETE (2025-12-30)

**Final Structure:**
```
./ (package srt)
├── config.go              # 525 lines - Constants, Config struct, defaultConfig, DefaultConfig()
├── config_marshal.go      # 419 lines - MarshalURL, UnmarshalURL, MarshalQuery, UnmarshalQuery
└── config_validate.go     # 278 lines - Validate(), ApplyAutoConfiguration()
```

**Results:**
- Original: 1209 lines, 7 functions
- After: 1222 lines (+1.1%), 7 functions (unchanged)
- All 85 tests passing

**Note:** Did not need `config_options.go` - struct + defaults are better kept together in `config.go`.

**See:** `config_refactoring_implementation.md` for detailed progress

### Phase 3: dial.go Split ✅ COMPLETE

**Status:** COMPLETE (2025-12-30)

**Final Structure:**
```
./ (package srt)
├── dial.go              # 350 lines - dialer struct, Dial(), checkConnection()
├── dial_handshake.go    # 359 lines - handleHandshake(), sendInduction(), sendShutdown()
└── dial_io.go           # 360 lines - reader, send, getters, Close, Read/Write, stats
```

**Results:**
- Original: 1043 lines, 26 functions
- After: 1069 lines (+2.5%), 26 functions (unchanged)
- All 85 tests passing

**See:** `dial_refactoring_implementation.md` for detailed progress

---

### Phase 3 (OLD - replaced by above):
**Target:** Split dial logic into multiple files within main package.

**Recommended Approach:** Same-package split based on Phase 1 learnings.

```
./ (package srt)
├── dial.go              # ~400 lines - Dial(), dialer struct
├── dial_handshake.go    # ~350 lines - handleHandshake, sendInduction
├── dial_io.go           # ~150 lines - Read, Write methods
└── dial_linux.go        # ~600 lines - Platform-specific (keep as-is)
```

**Migration Strategy:**
1. Extract handshake logic to `dial_handshake.go`
2. Run `go build ./...` + `go test .`
3. Extract I/O methods to `dial_io.go`
4. Run `go build ./...` + `go test .`
5. Verify function count unchanged

### Phase 4: listen.go Split ✅ COMPLETE

**Status:** COMPLETE (2025-12-30)

**Final Structure:**
```
./ (package srt)
├── listen.go              # 346 lines - Types, interfaces, Listen(), listener struct
├── listen_accept.go       # 55 lines  - Accept2(), Accept()
├── listen_lifecycle.go    # 135 lines - markDone, error, handleShutdown, getConnections, isShutdown, Close, Addr
└── listen_io.go           # 196 lines - reader, send, sendWithMetrics, sendBrokenLookup, log
```

**Results:**
- Original: 708 lines, 16 functions
- After: 732 lines (+3.4%), 16 functions (unchanged)
- All 85 tests passing

**See:** `listen_refactoring_implementation.md` for detailed progress

---

### Phase 4 (OLD - replaced by above):
**Target:** Split listener logic into multiple files within main package.

**Recommended Approach:** Same-package split based on Phase 1 learnings.

```
./ (package srt)
├── listen.go            # ~400 lines - Listen(), listener struct
├── listen_accept.go     # ~200 lines - Accept, Accept2
├── listen_io.go         # ~100 lines - send, reader
└── listen_linux.go      # ~950 lines - Platform-specific (keep as-is)
```

**Migration Strategy:**
1. Extract accept logic to `listen_accept.go`
2. Run `go build ./...` + `go test .`
3. Extract I/O methods to `listen_io.go`
4. Run `go build ./...` + `go test .`
5. Verify function count unchanged

---

## 3. Detailed Migration Plan (Updated Based on Phase 1 Learnings)

### Key Principles (from connection.go experience)

1. **Same-package split preferred** - Avoids circular import issues
2. **Extract code unchanged** - Don't "clean up" during extraction
3. **Verify after each step** - Build + test after every file
4. **Count functions** - Ensure no functions lost

### Standard Extraction Procedure

For each file extraction:

```bash
# Step 1: Count functions before
grep -E "^func " original.go | wc -l

# Step 2: Create new file with extracted code (unchanged!)
# - Copy function(s) exactly as-is
# - Add package declaration
# - Add necessary imports

# Step 3: Remove from original file

# Step 4: Verify
go build ./...
go test .

# Step 5: Count functions after (should match)
grep -E "^func " *.go | wc -l
```

### Verification Checklist (Per Extraction)

- [ ] `go build ./...` passes
- [ ] `go test .` passes (full package)
- [ ] Function count unchanged
- [ ] No "simplified" or "cleaned up" code (pure move only)

### Phase 1 Example (Completed)

```bash
# Before
$ git show HEAD:connection.go | grep -E "^func " | wc -l
63

# After (across all connection_*.go files)
$ cat connection*.go | grep -E "^func " | wc -l
63
```

### Import Handling

When extracting, imports follow the code:
- Copy ALL imports from original file to new file
- After extraction, remove unused imports from both files
- Use `goimports` or IDE to clean up automatically

---

## 4. Verification Checklist (Updated from Phase 1 Learnings)

### After EACH File Extraction:

- [ ] `go build ./...` passes
- [ ] `go test .` passes (package tests)
- [ ] Function count unchanged: `grep -E "^func " *.go | wc -l`
- [ ] No code modified during extraction (pure move)
- [ ] `git status` shows expected new file

### After Phase Complete:

- [ ] `go test ./...` passes (all packages)
- [ ] `go vet ./...` passes
- [ ] Function diff clean: `diff <(original funcs sorted) <(new funcs sorted)`
- [ ] Line count reasonable: original ± 10% (overhead from file headers)
- [ ] Coverage unchanged: `go test -cover .`

### After ALL Phases Complete:

- [ ] `make test` passes
- [ ] `make test-race` passes
- [ ] Integration tests pass: `make test-parallel`
- [ ] Benchmarks show no regression
- [ ] Documentation updated

---

## 5. Risk Mitigation

### Circular Import Prevention

```
connection/ imports:
├── packet/         ✓ (data types)
├── circular/       ✓ (sequence numbers)
├── metrics/        ✓ (metrics)
├── crypto/         ✓ (encryption)
└── congestion/     ✓ (send/receive)

dial/ imports:
├── connection/     ✓ (creates connections)
└── packet/         ✓ (data types)

listen/ imports:
├── connection/     ✓ (creates connections)
└── packet/         ✓ (data types)
```

### Backward Compatibility

Option A: Type aliases in root package
```go
// srt.go
type Conn = connection.Conn
```

Option B: Direct migration (recommended)
- Update all imports at once
- No compatibility shims needed

---

## 6. Timeline Estimate (Updated)

| Phase | Files | Lines | Est. Time | Status |
|-------|-------|-------|-----------|--------|
| Phase 1: connection.go | 9 | 2,437 → 2,543 | ~2 hours | ✅ COMPLETE |
| Phase 2: config.go | 3 | 1,209 → 1,222 | ~30 min | ✅ COMPLETE |
| Phase 3: dial.go | 3 | 1,043 → 1,069 | ~30 min | ✅ COMPLETE |
| Phase 4: listen.go | 4 | 708 → 732 | ~20 min | ✅ COMPLETE |

**All phases complete!**

**Lessons from Phase 1 + 2:**
- Actual time was much faster than estimates
- Same-package split is very efficient
- Build/test after each step catches issues early
- Clean extractions (no code changes) = no bugs

---

## 7. Success Metrics

### Phase 1 (connection.go) ✅ Complete
- [x] No file > 600 lines (largest: connection.go at 600 lines)
- [x] Each file has single responsibility
- [x] Test coverage maintained (85 tests passing)
- [x] No circular imports
- [x] Documentation updated

### Phase 2 (config.go) ✅ Complete
- [x] No file > 600 lines (largest: config.go at 525 lines)
- [x] Each file has single responsibility
- [x] Test coverage maintained (85 tests passing)
- [x] Function count unchanged (7 before = 7 after)
- [x] Documentation updated

### Phase 3 (dial.go) ✅ Complete
- [x] No file > 400 lines (largest: dial_io.go at 360 lines)
- [x] Each file has single responsibility
- [x] Test coverage maintained (85 tests passing)
- [x] Function count unchanged (26 before = 26 after)
- [x] Documentation updated

### Phase 4 (listen.go) ✅ Complete
- [x] No file > 400 lines (largest: listen.go at 346 lines)
- [x] Each file has single responsibility
- [x] Test coverage maintained (85 tests passing)
- [x] Function count unchanged (16 before = 16 after)
- [x] Documentation updated

### All Primary Phases Complete! 🎉
- [x] connection.go: 2,437 → 581 lines (9 files)
- [x] config.go: 1,209 → 525 lines (3 files)
- [x] dial.go: 1,043 → 350 lines (3 files)
- [x] listen.go: 708 → 346 lines (4 files)
- [x] All 85 tests passing
- [x] Benchmarks: no regression

---

## 8. References

- [receive/ Subpackage Migration](receive_testing_strategy.md) - Successful example
- [Go Code Organization](https://go.dev/doc/modules/layout) - Best practices
- [Effective Go](https://go.dev/doc/effective_go) - Style guide

---

## 9. Appendix: Function Inventory

### connection.go Functions by Category (After Split)

| File | Functions | Lines |
|------|-----------|-------|
| `connection_rtt.go` | `Recalculate`, `RTT`, `RTTVar`, `NAKInterval` | 66 |
| `connection_io.go` | `ReadPacket`, `Read`, `WritePacket`, `Write`, `push`, `pop`, `getTimestamp`, `getTimestampForPacket`, `networkQueueReader`, `writeQueueReader`, `deliver` | 263 |
| `connection_handlers.go` | `handlePacketDirect`, `initializeControlHandlers`, `handleUserPacket`, `handlePacket`, `handleKeepAlive`, `sendProactiveKeepalive`, `getKeepaliveInterval`, `handleShutdown`, `handleACK`, `handleNAK`, `ExtendedStatistics`, `GetExtendedStatistics`, `handleACKACK`, `recalculateRTT`, `getNextACKNumber` | 455 |
| `connection_handshake.go` | `handleHSRequest`, `handleHSResponse`, `sendHSRequests`, `sendHSRequest` | 276 |
| `connection_keymgmt.go` | `handleKMRequest`, `handleKMResponse`, `sendKMRequests`, `sendKMRequest` | 197 |
| `connection_send.go` | `sendShutdown`, `splitNakList`, `sendNAK`, `sendACK`, `sendACKACK` | 252 |
| `connection_lifecycle.go` | `Close`, `GetPeerIdleTimeoutRemaining`, `resetPeerIdleTimeout`, `getTotalReceivedPackets`, `watchPeerIdleTimeout`, `close`, `log` | 203 |
| `connection_stats.go` | `Stats`, `SetDeadline`, `SetReadDeadline`, `SetWriteDeadline`, `printCloseStatistics` | 231 |
| `connection.go` | Core struct, interface, `newSRTConn`, getters | 600 |

**Total: 63 functions across 9 files (verified with diff)**

### Issues Encountered During Phase 1

1. **Stats() Oversimplification Bug**
   - Symptom: TestStats failed with wrong ByteRecv value
   - Cause: Simplified Stats() during extraction instead of copying exactly
   - Fix: Restored full original implementation
   - Lesson: **Never modify code during extraction**

2. **Documentation Math Error**
   - Symptom: Claimed 13% line reduction when it was 4.3% increase
   - Cause: Miscounted lines, didn't verify with actual commands
   - Fix: Updated documentation with actual `wc -l` output
   - Lesson: **Always verify numbers with commands**

3. **Unused Import Cleanup**
   - Symptom: Build failed with "imported and not used"
   - Cause: After moving functions, some imports no longer needed
   - Fix: Removed unused imports from connection.go
   - Lesson: **Check imports after each extraction**

