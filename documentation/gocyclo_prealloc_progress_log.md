# GoSRT Cyclomatic Complexity & Preallocation Progress Log

**Created:** 2026-02-28
**Plan Reference:** See full plan at `/home/das/.claude/plans/mossy-leaping-liskov.md`

---

## Overview

| Category | Initial Count | Current Count | Target |
|----------|---------------|---------------|--------|
| gocyclo (>30) | 23 | 23 | 0 |
| prealloc | 17 | 17 | 0 |

---

## Phase 1: Core Library Functions

### 1.1 periodicNakBtree (Complexity: 51)
**File:** `congestion/live/receive/nak.go:145`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Extract `shouldRunPeriodicNak()` | ⬜ | | Rate limiting phase |
| Extract `calculateNakScanParams()` | ⬜ | | Scan parameters |
| Extract `detectGaps()` | ⬜ | | Btree scanning |
| Extract `consolidateGapsToNakEntries()` | ⬜ | | NAK consolidation |
| Write phase unit tests | ⬜ | | Table-driven |
| Write phase benchmarks | ⬜ | | Per-phase timing |
| Verify complexity reduced | ⬜ | | Target: <15 each |

### 1.2 Config.Validate (Complexity: 80)
**File:** `config_validate.go:32`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create `config_validators.go` | ⬜ | | Individual validators |
| Extract `validateTransmissionType()` | ⬜ | | |
| Extract `validateConnectionTimeout()` | ⬜ | | |
| Extract `validateMSS()` | ⬜ | | |
| Extract `validateIoUringConfig()` | ⬜ | | Group io_uring checks |
| Extract remaining validators | ⬜ | | ~20 validators total |
| Write table-driven tests | ⬜ | | Per-validator |
| Verify complexity reduced | ⬜ | | Target: <10 main |

### 1.3 Dial (Complexity: 42)
**File:** `dial.go:101`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create `dial_phases.go` | ⬜ | | Internal phases |
| Extract `setupUDPConnection()` | ⬜ | | |
| Extract `initializeReceiver()` | ⬜ | | io_uring init |
| Extract `startReadGoroutine()` | ⬜ | | |
| Extract `performHandshake()` | ⬜ | | |
| Write phase unit tests | ⬜ | | |
| Verify complexity reduced | ⬜ | | Target: <15 main |

---

## Phase 2: CLI Infrastructure

### 2.1 ApplyFlagsToConfig (Complexity: 116)
**File:** `contrib/common/flags.go:344`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create `flags_applicators.go` | ⬜ | | Table-driven |
| Define `FlagApplicator` struct | ⬜ | | |
| Create applicator table (~80 entries) | ⬜ | | Organized by category |
| Refactor `ApplyFlagsToConfig()` | ⬜ | | Loop over table |
| Write unit tests | ⬜ | | Per-flag tests |
| Write benchmarks | ⬜ | | All/none/few flags |
| Run `./contrib/common/test_flags.sh` | ⬜ | | **80+ integration tests** |
| Verify complexity reduced | ⬜ | | Target: <10 |

### 2.2 ValidateFlagDependencies (Complexity: 37)
**File:** `contrib/common/flags.go:785`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create dependency table | ⬜ | | Table-driven |
| Refactor validation logic | ⬜ | | Loop over dependencies |
| Run `./contrib/common/test_flags.sh` | ⬜ | | Regression check |
| Verify complexity reduced | ⬜ | | Target: <15 |

### 2.3 UnmarshalQuery (Complexity: 74)
**File:** `config_marshal.go:30`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create unmarshal handler table | ⬜ | | Per-field handlers |
| Refactor to table-driven | ⬜ | | |
| Write table-driven tests | ⬜ | | Round-trip tests |
| Verify complexity reduced | ⬜ | | Target: <15 |

### 2.4 MarshalQuery (Complexity: 37)
**File:** `config_marshal.go:272`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create marshal handler table | ⬜ | | Per-field handlers |
| Refactor to table-driven | ⬜ | | |
| Write table-driven tests | ⬜ | | Round-trip tests |
| Verify complexity reduced | ⬜ | | Target: <10 |

---

## Phase 3: Handshake & Packet Handling

### 3.1 newConnRequest (Complexity: 47)
**File:** `conn_request.go:73`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Create `conn_request_handlers.go` | ⬜ | | Handler functions |
| Extract `handleInduction()` | ⬜ | | |
| Extract `handleConclusionV4()` | ⬜ | | |
| Extract `handleConclusionV5()` | ⬜ | | |
| Create handler dispatch map | ⬜ | | |
| Write handler unit tests | ⬜ | | Per-handler |
| Verify complexity reduced | ⬜ | | Target: <15 main |

### 3.2 handleHandshake (Complexity: 36)
**File:** `dial_handshake.go:13`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Extract handshake phases | ⬜ | | |
| Write phase tests | ⬜ | | |
| Verify complexity reduced | ⬜ | | Target: <15 |

### 3.3 CIFHandshake.Unmarshal (Complexity: 37)
**File:** `packet/packet.go:800`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Extract field unmarshalers | ⬜ | | |
| Add benchmarks | ⬜ | | |
| Verify complexity reduced | ⬜ | | Target: <15 |

### 3.4 CIFHandshake.Marshal (Complexity: 33)
**File:** `packet/packet.go:955`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Extract field marshalers | ⬜ | | |
| Add benchmarks | ⬜ | | |
| Verify complexity reduced | ⬜ | | Target: <15 |

### 3.5 NewSender (Complexity: 31)
**File:** `congestion/live/send/sender.go:244`
**Status:** ⬜ Not Started

| Task | Status | Date | Notes |
|------|--------|------|-------|
| Extract initialization phases | ⬜ | | |
| Write phase tests | ⬜ | | |
| Verify complexity reduced | ⬜ | | Target: <15 |

---

## Phase 4: CLI Entry Points (Lower Priority)

### 4.1 run (client) (Complexity: 70)
**File:** `contrib/client/main.go:45`
**Status:** ⬜ Not Started

### 4.2 run (server) (Complexity: 35)
**File:** `contrib/server/main.go:74`
**Status:** ⬜ Not Started

### 4.3 run (client-generator) (Complexity: 56)
**File:** `contrib/client-generator/main.go:46`
**Status:** ⬜ Not Started

---

## Phase 5: Integration Testing Tools (Lowest Priority)

These are test infrastructure, not production code. Lower priority but documented for completeness.

| Function | File | Complexity | Status |
|----------|------|------------|--------|
| `(*SRTConfig).ToCliFlags` | contrib/integration_testing/config.go:507 | 68 | ⬜ |
| `runParallelModeTest` | contrib/integration_testing/test_parallel_mode.go:34 | 62 | ⬜ |
| `PrintAnalysisResult` | contrib/integration_testing/analysis.go:1522 | 61 | ⬜ |
| `main` (graceful_shutdown) | contrib/integration_testing/test_graceful_shutdown.go:15 | 57 | ⬜ |
| `runIsolationModeTest` | contrib/integration_testing/test_isolation_mode.go:20 | 53 | ⬜ |
| `runNetworkModeTest` | contrib/integration_testing/test_network_mode.go:16 | 46 | ⬜ |
| `runTestWithMetrics` | contrib/integration_testing/test_graceful_shutdown.go:552 | 44 | ⬜ |
| `main` (metrics-audit) | tools/metrics-audit/main.go:81 | 44 | ⬜ |

---

## Phase 6: Preallocation Fixes

### Performance-Critical (Production Code)

| Location | Variable | Status | Date |
|----------|----------|--------|------|
| `contrib/integration_testing/config.go:1857` | `flags` | ⬜ | |
| `contrib/performance/process.go:76` | `args` | ⬜ | |
| `contrib/performance/process.go:135` | `args` | ⬜ | |
| `tools/metrics-audit/main.go:626` | `analyses` | ⬜ | |
| `tools/metrics-audit/main.go:746` | `names` | ⬜ | |
| `tools/lock-requirements-analyzer/main.go:694` | `keys` | ⬜ | |
| `tools/metrics-lock-analyzer/main.go:711` | `keys` | ⬜ | |
| `tools/metrics-lock-analyzer/main.go:774` | `fileStatsList` | ⬜ | |
| `tools/test-audit/main.go:701` | `sorted` | ⬜ | |
| `tools/test-audit/main.go:902` | `result` | ⬜ | |
| `tools/test-audit/main.go:1083` | `vals` | ⬜ | |

### Test Files (Lower Priority)

| Location | Variable | Status | Date |
|----------|----------|--------|------|
| `congestion/live/receive/receive_basic_test.go:123` | `seqNAK` | ⬜ | |
| `congestion/live/receive/receive_basic_test.go:175` | `seqNAK` | ⬜ | |
| `congestion/live/receive/receive_basic_test.go:233` | `seqNAK` | ⬜ | |
| `contrib/integration_testing/test_matrix.go:149` | `tests` | ⬜ | |
| `contrib/integration_testing/test_matrix.go:588` | `tests` | ⬜ | |
| `contrib/integration_testing/test_parallel_mode.go:552` | `allRecommendations` | ⬜ | |

---

## Verification Checklist

Run after each phase completion:

```bash
# 1. Complexity check
nix develop --command golangci-lint run --config=.golangci-comprehensive.yml 2>&1 | grep gocyclo | wc -l

# 2. Prealloc check
nix develop --command golangci-lint run --config=.golangci-comprehensive.yml 2>&1 | grep prealloc | wc -l

# 3. Unit tests
make test-quick

# 4. Race detection
make test-race

# 5. Flag validation (Phase 2 only)
./contrib/common/test_flags.sh

# 6. Benchmarks (compare before/after)
go test -bench=. -benchmem ./path/to/package/...
```

---

## Change Log

| Date | Phase | Change | Complexity Δ |
|------|-------|--------|--------------|
| 2026-02-28 | - | Initial plan created | - |
| | | | |

---

## Notes

- **Legend:** ⬜ Not Started | 🔄 In Progress | ✅ Complete | ❌ Blocked
- Update this log after each work session
- Run verification checklist before marking any phase complete
- For Phase 2 (flags), always run `./contrib/common/test_flags.sh` as final validation
