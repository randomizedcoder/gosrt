# Table-Driven Test Coverage Review

This document tracks the corner case coverage review for all table-driven test files.

## Review Process

For each table-driven test file:
1. Identify the test case struct(s)
2. Define critical corner cases for each field
3. Run coverage checker (or manual review)
4. Document gaps and actions

---

## 1. loss_recovery_table_test.go

**Struct**: `LossRecoveryTestCase`

### Corner Cases Defined

| Field | Corner Values | Critical? |
|-------|--------------|-----------|
| StartSeq | 0, 1, mid, MAX-100, MAX-50, MAX | Yes |
| TotalPackets | 1, 50, 100, 1000, 10000 | Yes |
| TsbpdDelayUs | 10ms, 50ms, 120ms, 500ms, 1s | Yes |
| NakRecentPct | 0.01, 0.05, 0.10, 0.25, 0.50 | Yes |
| DoRetransmit | true, false | Yes |
| DropPattern | All 7 patterns | Yes |

### Coverage Status

**Run**: `test-combinatorial-gen -coverage congestion/live/loss_recovery_table_test.go`

| Field | Covered | Missing Critical | Status |
|-------|---------|------------------|--------|
| StartSeq | 2/6 | 0, MAX-100, MAX | ❌ GAPS |
| TotalPackets | 4/5 | 1, 1000, 10000 | ❌ GAPS |
| TsbpdDelayUs | 1/5 | 10ms, 50ms, 500ms, 1s | ❌ GAPS |
| NakRecentPct | 1/5 | 0.01, 0.05, 0.25, 0.50 | ❌ GAPS |
| DoRetransmit | 2/2 | None | ✅ |
| DropPattern | 7/7 | None | ✅ |

**Action Required**: Add ~10 tests for missing critical corners

---

## 2. core_scan_table_test.go

**Structs**: `ContiguousScanTestCase`, `GapScanTestCase`

### Corner Cases Defined

| Field | Corner Values | Critical? |
|-------|--------------|-----------|
| StartSeq | 0, mid, MAX-2, MAX | Yes (wraparound) |
| ContiguousPoint | ISN-1, 0, typical, MAX | Yes |
| PacketSeqs | empty, single, contiguous, gap, wraparound | Yes |
| TsbpdTime | past, present, future | Yes |
| MockTime | before TSBPD, at TSBPD, after TSBPD | Yes |

### Coverage Status (Manual Review)

| Scenario | ContiguousScan | GapScan | Status |
|----------|---------------|---------|--------|
| Empty btree | ✅ | N/A | ✅ |
| Contiguous packets | ✅ | ✅ | ✅ |
| Gap in middle | ✅ | ✅ | ✅ |
| No progress (gap at start) | ✅ | N/A | ✅ |
| Wraparound (MAX→0) | ✅ | ✅ | ✅ |
| Wraparound with gap | ✅ | ✅ | ✅ |
| Stale contiguousPoint | ✅ | ✅ | ✅ |
| Stale + wraparound | ✅ | ✅ | ✅ |
| Small gap (no stale) | ✅ | N/A | ✅ |
| Exact threshold | ✅ | N/A | ✅ |

**Status**: ✅ GOOD COVERAGE

---

## 3. nak_consolidate_table_test.go

**Struct**: `ConsolidateTestCase` (and others)

### Corner Cases Defined

| Field | Corner Values | Critical? |
|-------|--------------|-----------|
| NakMergeGap | 0, 1, 3, 10, 100 | Yes |
| Pattern type | Explicit, Modulus, Burst, Contiguous | Yes |
| Sequence count | 0, 1, small, large, very large | Yes |
| Out-of-order | in-order, reversed, random | Yes |

### Coverage Status (Manual Review)

| Scenario | Covered? | Status |
|----------|----------|--------|
| Empty btree | ✅ | ✅ |
| Single entry | ✅ | ✅ |
| Contiguous range | ✅ | ✅ |
| Merge within gap | ✅ | ✅ |
| Gap exceeds threshold | ✅ | ✅ |
| Mixed singles/ranges | ✅ | ✅ |
| Sequence wraparound | ✅ | ✅ |
| Modulus drops | ✅ (3 tests) | ✅ |
| Burst drops | ✅ | ✅ |
| Large scale | ✅ (2 tests) | ✅ |
| Out-of-order insertion | ✅ | ✅ |
| MSS limits | Partial | ⚠️ |

**Status**: ⚠️ MSS tests need review (some in original file only)

---

## 4. send_table_test.go

**Struct**: `SendNakTestCase`

### Corner Cases Defined

| Field | Corner Values | Critical? |
|-------|--------------|-----------|
| TotalPackets | 10, 100, 1000 | Yes |
| NakRanges | single, range, multiple, mixed | Yes |
| NotFoundTest | true, false | Yes |
| Strategy | Original, HonorOrder | Yes |

### Coverage Status (Manual Review)

| Scenario | Original | HonorOrder | Status |
|----------|----------|------------|--------|
| Basic single | ✅ | ✅ | ✅ |
| Basic range | ✅ | ✅ | ✅ |
| Multiple singles | ✅ | ✅ | ✅ |
| Multiple ranges | ✅ | ✅ | ✅ |
| Mixed | ✅ | ✅ | ✅ |
| Not found | ✅ | ✅ | ✅ |
| Modulus drops | ✅ | ✅ | ✅ |
| Burst drops | ✅ | ✅ | ✅ |
| Large scale | ✅ | ✅ | ✅ |
| Strategy difference | ✅ | ✅ | ✅ |

**Status**: ✅ GOOD COVERAGE

---

## 5. fast_nak_table_test.go

**Structs**: `FastNakConditionTestCase`, `FastNakRecentTestCase`, `BuildNakListTestCase`

### Corner Cases Defined

| Field | Corner Values | Critical? |
|-------|--------------|-----------|
| UseNakBtree | true, false | Yes |
| UseFastNak | true, false | Yes |
| Silence duration | short, threshold, long | Yes |
| Sequence jump | small, significant | Yes |
| Burst size | small, medium, large | Yes |

### Coverage Status (Manual Review)

| Scenario | Covered? | Status |
|----------|----------|--------|
| Disabled | ✅ | ✅ |
| No NAK btree | ✅ | ✅ |
| No previous packet | ✅ | ✅ |
| Short silence | ✅ | ✅ |
| No sequence jump | ✅ | ✅ |
| Significant jump | ✅ | ✅ |
| Large burst (5/20/100 Mbps) | ✅ | ✅ |
| Multiple bursts | ✅ | ✅ |
| Build NAK list | ✅ | ✅ |

**Status**: ✅ GOOD COVERAGE

---

## 6. receive_drop_table_test.go

**Struct**: `DropTestCase`

### Corner Cases Defined

| Field | Corner Values | Critical? |
|-------|--------------|-----------|
| Drop reason | TooLate, AlreadyACK, AlreadyRecv | Yes |
| Tick timing | before/after ACK period | Yes |
| Duplicate sequence | before/after contiguousPoint | Yes |

### Coverage Status

| Scenario | Covered? | Status |
|----------|----------|--------|
| Too late | ✅ | ✅ |
| Already ACKed | ✅ | ✅ |
| Already received (no ACK) | ✅ | ✅ |

**Status**: ✅ GOOD COVERAGE (3/3 scenarios)

---

## Summary

| File | Status | Action |
|------|--------|--------|
| loss_recovery_table_test.go | ❌ 14 critical gaps | Add ~10 tests |
| core_scan_table_test.go | ✅ Good | None |
| nak_consolidate_table_test.go | ⚠️ MSS partial | Review MSS tests |
| send_table_test.go | ✅ Good | None |
| fast_nak_table_test.go | ✅ Good | None |
| receive_drop_table_test.go | ✅ Good | None |

## Priority Actions

1. **HIGH**: Add missing corner cases to `loss_recovery_table_test.go`
2. **MEDIUM**: Review MSS coverage in `nak_consolidate_table_test.go`
3. **LOW**: Consider adding extreme scale tests where applicable

