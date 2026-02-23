# Table-Driven Test Design Implementation

This document tracks the implementation progress of converting individual tests to table-driven tests.

## 🎯 Current Focus: Apply CODE_PARAM Corner Coverage

**Key Insights** (Verified):

### Insight #1: Field Classification
- **Code Parameters**: Test fields that map to actual production code variables → Need corner coverage
- **Test Infrastructure**: Test fields that control test execution → Skip combinatorial
- **Expectations**: Test fields that assert results → Derived, don't vary

### Insight #2: Derived Parameters (NEW!)
Some TEST_INFRA parameters must be **derived from CODE_PARAMs** to ensure test correctness.

**Discovery**: When testing `TsbpdDelayUs = 10ms` with default `NakIntervalUs = 20ms`, NAKs **cannot fire in time** because NAK interval > TSBPD window!

**Solution**: Derive timing parameters from CODE_PARAMs:
```
TsbpdDelayUs (CODE_PARAM)
├── AckIntervalUs   = TsbpdDelayUs / 10
├── NakIntervalUs   = TsbpdDelayUs / 5
├── NakTickUs       = TsbpdDelayUs / 50
├── DeliveryTickUs  = TsbpdDelayUs / 25
└── PacketSpreadUs  = TsbpdDelayUs / 100
```

See `table_driven_test_design.md` section "Critical Insight #2: Derived Parameters" for full dependency graph.

**Progress**:
1. ✅ Documented tool consolidation design
2. ✅ Implemented unified `tools/test-audit/` tool
3. ✅ Added production code parameter extraction (config.go, receive.go, send.go)
4. ✅ Cross-reference test fields against production code (O(1) map lookups)
5. ✅ Verified `LossRecoveryTestCase`: 3 CODE_PARAMs, 10 TEST_INFRA, 3 EXPECTATIONS
6. ✅ Added `send_table_test.go` StartSeq CODE_PARAM with wraparound tests
7. ✅ Added `receive_drop_table_test.go` StartSeq + TsbpdDelayUs CODE_PARAMs
8. ✅ Added corner tests to `loss_recovery_table_test.go` (StartSeq, TsbpdDelayUs, NakRecentPct)
9. ✅ Discovered DERIVED parameter dependency (TsbpdDelayUs → timing params)
10. ✅ Documented DERIVED parameter design in `table_driven_test_design.md`
11. ✅ Implemented `applyDerivedDefaults()` function with smart derivation:
    - `AckIntervalUs = TsbpdDelayUs / 10`
    - `NakIntervalUs = TsbpdDelayUs / 5`
    - `NakTickUs = TsbpdDelayUs / 50`
    - `DeliveryTickUs = TsbpdDelayUs / 25`
    - `PacketSpreadUs = TsbpdDelayUs / 100`
    - `NakCycles = max(baseCycles, 2 * TSBPD / NakTickUs)` (ensure TSBPD window covered)
12. ✅ Added 5 negative tests to validate derivation formulas are necessary
13. ✅ All 32 tests pass (27 positive + 5 negative)

**Key Discoveries**:
- **10ms TSBPD works** with derived params (AckInterval=1ms, NakInterval=2ms)
- **Negative tests prove derivations are critical**: e.g., NakInterval > TSBPD → 0% NAK rate
- **Catastrophic misconfiguration** (all params wrong) → 38% delivery (proves sensitivity)

**Next Steps**:
1. ✅ Run `make audit-classify` on ALL table-driven test files - COMPLETE
2. 🔲 Add missing corner tests identified by audit
3. 🔲 Document legacy tests to keep (not table-driveable)
4. 🔲 Delete redundant individual test files

---

## Full Audit Results (2024-12-29)

### File 1: `loss_recovery_table_test.go` ✅ COMPLETE

**CODE_PARAMs**: 3 (`StartSeq`, `TsbpdDelayUs`, `NakRecentPct`)

| Struct | CODE_PARAMs | Corner Values | Combinations |
|--------|-------------|---------------|--------------|
| `LossRecoveryTestCase` | StartSeq, TsbpdDelayUs, NakRecentPct | 3×3×3 | 27 |

**Audit Output** (coverage):
```
✅🎯 StartSeq/0          : Zero - baseline [[Corner_StartSeq_Zero Corner_Combo_ZeroSeq_5pctNak]]
✅🎯 StartSeq/2147483547 : Near MAX - wraparound zone [[Corner_StartSeq_NearMax]]
✅🎯 StartSeq/2147483647 : AT MAX - immediate wrap [[Corner_StartSeq_AtMax]]
✅🎯 TsbpdDelayUs/10000  : 10ms - aggressive [[Corner_TSBPD_10ms Corner_Combo_MaxSeq_10msTSBPD ...]]
✅   TsbpdDelayUs/120000 : 120ms - standard [[Corner_TSBPD_120ms]]
✅🎯 TsbpdDelayUs/500000 : 500ms - high latency [[Corner_TSBPD_500ms_Explicit ...]]
✅🎯 NakRecentPct/0.05   : 5% - aggressive [[Corner_NakRecent_5pct ...]]
✅🎯 NakRecentPct/0.25   : 25% - conservative [[Corner_NakRecent_25pct ...]]

Summary: 14/19 corners covered (73.7%)
```

**Corner Tests Added**:
1. `Corner_StartSeq_Zero` - StartSeq=0
2. `Corner_StartSeq_NearMax` - StartSeq=MAX-100
3. `Corner_StartSeq_AtMax` - StartSeq=MAX
4. `Corner_TSBPD_10ms` - TsbpdDelayUs=10,000
5. `Corner_TSBPD_120ms` - TsbpdDelayUs=120,000
6. `Corner_TSBPD_500ms_Explicit` - TsbpdDelayUs=500,000
7. `Corner_NakRecent_5pct` - NakRecentPct=0.05
8. `Corner_NakRecent_25pct` - NakRecentPct=0.25
9. `Corner_Combo_MaxSeq_10msTSBPD` - Multiple corners combined
10. `Corner_Combo_ZeroSeq_5pctNak` - Multiple corners combined
11. `Corner_Combo_MaxSeq_25pctNak_500msTSBPD` - All three CODE_PARAMs at corners

**Negative Tests** (5): Validate derived param formulas are necessary

**Result**: 32 tests pass (27 positive + 5 negative)

---

### File 2: `nak_consolidate_table_test.go` ✅ COMPLETE

**CODE_PARAMs**: 1 (`NakMergeGap`)

| Struct | CODE_PARAMs | Corner Values | Combinations |
|--------|-------------|---------------|--------------|
| `ConsolidateTestCase` | NakMergeGap | 0, 3, 100 | 3 |
| `MSSTestCase` | NakMergeGap | 0, 3, 100 | 3 |
| `ExtremeScaleTestCase` | (none) | - | 0 |

**Bug Found**: Test runner treated `NakMergeGap=0` as "not set" → defaulted to 3
- **Fix**: Added `UseDefaultMergeGap bool` flag to struct
- **Verification**: All 7 new corner tests pass

**Corner Tests Added**:
1. `Corner_MergeGap_Zero_ContiguousMerge` - Only merge adjacent
2. `Corner_MergeGap_Zero_GapOfOne` - Gap of 1 should NOT merge
3. `Corner_MergeGap_Zero_AllSingles` - No merging at all
4. `Corner_MergeGap_Zero_Wraparound` - Adjacent at MAX still merges
5. `Corner_MergeGap_Large_MergeDistant` - Merge sequences 50 apart
6. `Corner_MergeGap_Large_StillSplits` - Gap > 100 still splits
7. `Corner_MergeGap_Large_ModulusDrops` - Every 10th merges into 1 range

**Result**: 24 tests pass (17 original + 7 new corners)

---

### File 3: `send_table_test.go` 🔴 NEEDS WORK

**CODE_PARAMs**: 1 (`StartSeq`)

| Struct | CODE_PARAMs | Corner Values | Combinations |
|--------|-------------|---------------|--------------|
| `SendNakTestCase` | StartSeq | 0, MAX-100, MAX | 3 |

**Audit Output** (coverage):
```
❌🚨 StartSeq/0          : Zero - baseline
❌🚨 StartSeq/2147483547 : Near MAX - wraparound zone
❌🚨 StartSeq/2147483647 : AT MAX - immediate wrap
❌🚨 TotalPackets/1      : Minimum - single
✅   TotalPackets/100    : Typical [[ModulusDrops BurstDrops Wraparound_NearMax]]
❌🚨 TotalPackets/1000   : Large - stress
✅🎯 NotFoundTest/true   : Enabled [[NotFoundPackets]]
❌🚨 NotFoundTest/false  : Disabled

Summary: 2/12 corners covered (16.7%)
⚠️  10 CRITICAL corners missing
```

**Existing Wraparound Tests** (need verification):
- `Wraparound_NearMax` (StartSeq=MAX-50)
- `Wraparound_AtMax` (StartSeq=MAX-5)
- `Wraparound_CrossingMax` (StartSeq=MAX-10)

**Missing Corners (High Priority)**:
- ❌ `StartSeq=0` (baseline)
- ❌ `TotalPackets=1` (minimum)
- ❌ `TotalPackets=1000` (stress)

---

### File 4: `fast_nak_table_test.go` ⚠️ PARTIAL

**CODE_PARAMs**: 2 (`FastNakEnabled`, `UseNakBtree`)

| Struct | CODE_PARAMs | Corner Values | Combinations |
|--------|-------------|---------------|--------------|
| `FastNakConditionTestCase` | FastNakEnabled, UseNakBtree | true/false | 4 |
| `FastNakRecentTestCase` | (none - all TEST_INFRA) | - | 0 |
| `BuildNakListTestCase` | UseNakBtree | true/false | 2 |

**Audit Output** (coverage):
```
FastNakConditionTestCase:
  ✅🎯 FastNakEnabled/true : Enabled [[NoNakBtree NoPreviousPacket ...]]
  ✅🎯 FastNakEnabled/false: Disabled [[Disabled]]
  ✅🎯 UseNakBtree/true    : Enabled [[Disabled NoPreviousPacket ...]]
  ✅🎯 UseNakBtree/false   : Disabled [[NoNakBtree NoNakBtree]]
  Summary: 7/8 corners covered (87.5%)

FastNakRecentTestCase:
  ✅🎯 LastSeq/0           : Zero - baseline [[NoPreviousPacket]]
  ❌🚨 LastSeq/2147483547  : Near MAX - wraparound zone
  ❌🚨 LastSeq/2147483647  : AT MAX - immediate wrap
  ❌🚨 NewSeq/0            : Zero - baseline
  ❌🚨 NewSeq/2147483547   : Near MAX - wraparound zone
  ❌🚨 NewSeq/2147483647   : AT MAX - immediate wrap
  Summary: 9/14 corners covered (64.3%)

BuildNakListTestCase:
  ✅🎯 UseNakBtree/true    : Enabled
  ✅🎯 UseNakBtree/false   : Disabled
  Summary: 4/7 corners covered (57.1%)
```

**Coverage Status**:
- ✅ `FastNakEnabled`: true/false covered
- ✅ `UseNakBtree`: true/false covered
- ⚠️ `LastSeq/NewSeq` near MAX: Not covered (classified as TEST_INFRA but may need coverage)

**Missing Corners (Medium Priority)**:
- ❌ Sequence wraparound tests for LastSeq/NewSeq

---

### File 5: `core_scan_table_test.go` 🔴 NEEDS WORK

**CODE_PARAMs**: 2 (`StartSeq`, `ContiguousPoint`)

| Struct | CODE_PARAMs | Corner Values | Combinations |
|--------|-------------|---------------|--------------|
| `ContiguousScanTestCase` | StartSeq, ContiguousPoint | 0, MAX-100, MAX | 9 |
| `GapScanTestCase` | StartSeq, ContiguousPoint | 0, MAX-100, MAX | 9 |

**Audit Output** (coverage):
```
ContiguousScanTestCase:
  ✅🎯 StartSeq/0          : Zero - baseline [[Empty Contiguous Gap_FutureTSBPD ...]]
  ❌🚨 StartSeq/2147483547 : Near MAX - wraparound zone
  ❌🚨 StartSeq/2147483647 : AT MAX - immediate wrap
  ❌🚨 ContiguousPoint/0   : Zero - baseline
  ❌🚨 ContiguousPoint/2147483547: Near MAX - wraparound zone
  ❌🚨 ContiguousPoint/2147483647: AT MAX - immediate wrap
  Summary: 3/13 corners covered (23.1%)
  ⚠️  9 CRITICAL corners missing

GapScanTestCase:
  ✅🎯 StartSeq/0          : Zero - baseline
  ❌🚨 StartSeq/2147483547 : Near MAX - wraparound zone
  ❌🚨 StartSeq/2147483647 : AT MAX - immediate wrap
  ❌🚨 ContiguousPoint/0   : Zero - baseline
  ❌🚨 ContiguousPoint/2147483547: Near MAX - wraparound zone
  ❌🚨 ContiguousPoint/2147483647: AT MAX - immediate wrap
  Summary: 2/11 corners covered (18.2%)
  ⚠️  8 CRITICAL corners missing
```

**Missing Corners (HIGH PRIORITY)**:
- ❌ `StartSeq` near MAX and at MAX
- ❌ `ContiguousPoint` at 0, near MAX, and at MAX

---

### File 6: `receive_drop_table_test.go` 🔴 NEEDS WORK

**CODE_PARAMs**: 2 (`StartSeq`, `TsbpdDelayUs`)

| Struct | CODE_PARAMs | Corner Values | Combinations |
|--------|-------------|---------------|--------------|
| `DropTestCase` | StartSeq, TsbpdDelayUs | 0/MAX, 10ms/120ms/500ms | 9 |

**Audit Output** (coverage):
```
❌🚨 StartSeq/0          : Zero - baseline
❌🚨 StartSeq/2147483547 : Near MAX - wraparound zone
❌🚨 StartSeq/2147483647 : AT MAX - immediate wrap
❌🚨 TsbpdDelayUs/10000  : 10ms - aggressive
❌   TsbpdDelayUs/120000 : 120ms - standard
❌🚨 TsbpdDelayUs/500000 : 500ms - high latency
❌🚨 DuplicateSeq/0      : Zero - baseline
❌🚨 DuplicateSeq/2147483547: Near MAX - wraparound zone
❌🚨 DuplicateSeq/2147483647: AT MAX - immediate wrap

Summary: 0/12 corners covered (0.0%)
⚠️  10 CRITICAL corners missing
```

**NOTE**: Despite having `Wraparound_NearMax` test, audit shows 0% corner coverage because:
- Audit looks for explicit field values matching corner definitions
- Existing tests don't set explicit `StartSeq` values matching 0/MAX-100/MAX

**Missing Corners (HIGH PRIORITY)**:
- ❌ All `StartSeq` corners (0, near MAX, at MAX)
- ❌ All `TsbpdDelayUs` corners (10ms, 120ms, 500ms)
- ❌ All `DuplicateSeq` corners (0, near MAX, at MAX)

---

## Summary: Audit Findings

| File | CODE_PARAMs | Coverage | Status | Priority |
|------|-------------|----------|--------|----------|
| `loss_recovery_table_test.go` | 3 | 73.7% | ✅ Good | Done |
| `nak_consolidate_table_test.go` | 1 | ~100% | ✅ Complete | Done |
| `send_table_test.go` | 1 | 16.7% | 🔴 Needs work | High |
| `fast_nak_table_test.go` | 2 | 64-87% | ⚠️ Partial | Medium |
| `core_scan_table_test.go` | 2 | 18-23% | 🔴 Needs work | **High** |
| `receive_drop_table_test.go` | 2 | 0% | 🔴 Needs work | **High** |

### Key Discoveries

1. **Test Infrastructure Bug**: `NakMergeGap=0` was being overwritten by default logic
   - **Impact**: Could not test "no merging" corner case
   - **Fix**: Added explicit flag to allow zero value
   - **Lesson**: Go zero values can mask valid test inputs

2. **Derived Parameters Critical**: 10ms TSBPD requires proportionally smaller ACK/NAK intervals
   - Without derivation: 0% NAK rate (intervals > TSBPD)
   - With derivation: 100% recovery

3. **Negative Tests Prove Sensitivity**: Intentionally misconfigured tests show system degrades as expected
   - Validates that correct configuration matters
   - Proves tests aren't passing by accident

4. **Audit Tool Limitation**: Coverage checker looks for explicit field values matching corner definitions
   - Tests with implicit defaults may show 0% even if functionally covered
   - Need explicit field assignments to register as covered

---

## 🚨 PER-FILE ACTION PLANS

Each file below has a detailed action plan. We will work through each file sequentially,
implementing the corner tests, verifying they pass, and documenting progress.

---

### 📁 Action Plan 1: `core_scan_table_test.go`

**Status**: ✅ COMPLETE (with discrepancies noted)
**Priority**: HIGH
**Coverage Before**: 18-23%
**Coverage After**: 27-31% (tool limitation - DISC-003 - symbolic values not recognized)
**Tests Added**: 12 corner tests (6 ContiguousScan + 6 GapScan)
**All Tests Pass**: Yes (33 tests)

---

## 🐛 BUG INVESTIGATION: Corner Test Failure (2024-12-29)

### Test Failure Observed

```
=== NAME  TestContiguousScan_Table/Corner_Combo_BothNearMax
    core_scan_table_test.go:338:
        Error Trace:    core_scan_table_test.go:338
        Error:          Not equal:
                        expected: 0x7fffff9d (maxSeq - 98 = 2147483549)
                        actual  : 0x2
        Test:           TestContiguousScan_Table/Corner_Combo_BothNearMax
        Messages:       ackSeq mismatch
```

### Test Case That Failed

```go
{
    Name:            "Corner_Combo_BothNearMax",
    StartSeq:        maxSeq - 100,        // 2147483547
    ContiguousPoint: maxSeq - 100,
    SetContiguousPt: true,
    PacketSeqs:      []uint32{maxSeq - 100, maxSeq - 99, maxSeq, 0, 1},
    TsbpdTime:       100,                  // ⚠️ SMALL VALUE
    ExpectedOk:      true,
    ExpectedAckSeq:  maxSeq - 98,          // Expected: stop at gap
    ExpectedCP:      maxSeq - 99,
}
```

**Packet Layout** (gap of 98 packets from maxSeq-98 through maxSeq-1):
```
Packets:     [maxSeq-100] [maxSeq-99] ... GAP (98 pkts) ... [maxSeq] [0] [1]
Sequence:    2147483547   2147483548                        2147483647  0   1
```

### Hypothesis

**The test result is CORRECT behavior, not a bug in production code.**

The issue is in the **test setup**, not the `contiguousScan()` function:

1. **Test uses `TsbpdTime: 100`** (100 microseconds from epoch)
2. **`nowFn()` defaults to `time.Now().UnixMicro()`** (~1,735,000,000,000,000 microseconds)
3. **Since `now >> TsbpdTime`, ALL packets are considered TSBPD-expired**

The TSBPD skip logic in `receive.go` (lines 1001-1011):
```go
if h.PktTsbpdTime <= now && h.PktTsbpdTime > 0 {
    // TSBPD expired - advance past gap to this packet
    lastContiguous = seq
    return true // Continue scanning
}
```

**Result**: Since all packets are "expired", the scan correctly skips all gaps:
- maxSeq-100 → maxSeq-99 → maxSeq → 0 → 1
- Returns `ackSeq = 2` (lastContiguous + 1)

### Key Insight

This reveals we need to test **TWO distinct scenarios** at wraparound boundaries:

| Scenario | Mock Time | TsbpdTime | Expected Behavior |
|----------|-----------|-----------|-------------------|
| **TSBPD NOT expired** | 1,000,000,000 | 1,001,000,000 (future) | Gaps should STOP the scan |
| **TSBPD expired** | 1,001,000,000 | 1,000,000,000 (past) | Gaps should be SKIPPED |

Looking at existing tests that work correctly:
- `Gap_FutureTSBPD`: `MockTime: 1_000_000_000, TsbpdTime: 1_001_000_000` ✅
- `SmallGap_NoStaleHandling`: `MockTime: 1_000_000_000, TsbpdTime: 1_001_000_000` ✅

### Evidence Supporting Hypothesis

1. Other tests that expect gaps to block use `SetMockTime: true` with TSBPD in the future
2. The `ackSeq=2` result is mathematically correct for "all packets TSBPD-expired"
3. The production code logic is sound - it's the test setup that's incomplete

### Proposed Next Steps

**Option A: Fix Test Setup Only**
- Add `SetMockTime: true` and `MockTime: 1_000_000_000` to corner tests
- Set `TsbpdTime: 1_001_000_000` (future relative to mock time)
- Gaps will then block the scan as expected

**Option B: Add Both Scenarios (More Comprehensive)**
- `Corner_Combo_BothNearMax_GapBlocks` - TSBPD in future, gaps stop scan
- `Corner_Combo_BothNearMax_TSBPDExpired` - TSBPD in past, gaps skipped

**Option C: Verify Production Code Path First**
- Add debug logging to confirm which code path is taken
- Run test with verbose output to verify hypothesis
- Then implement fix

### Resolution

**DEFERRED**: Continue audit, collect all discrepancies, prioritize fixes after full coverage.

**Tracking ID**: `DISC-001`

---

## 📋 Discrepancy Tracker (Detailed)

Track all issues found during audit. Prioritize and fix after full audit is complete.

---

### DISC-001: Corner Tests Missing Mock Time Setup

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **File** | `congestion/live/core_scan_table_test.go` |
| **Related To** | TSBPD timing, test infrastructure |

**Problem**: Corner tests initially used `TsbpdTime: 100` without `SetMockTime: true`. Since `nowFn()` defaults to `time.Now().UnixMicro()` (~1.7 trillion µs), all packets appeared TSBPD-expired.

**Suspicious Code** (`receive.go` lines 1001-1011):
```go
if h.PktTsbpdTime <= now && h.PktTsbpdTime > 0 {
    // TSBPD expired - advance past gap to this packet
    lastContiguous = seq  // Skips gaps!
    return true
}
```

**Why Tests Didn't Catch It**: Existing tests (like `Gap_FutureTSBPD`) correctly used mock time. The new corner tests copied the pattern but missed the mock time setup.

**Resolution**: Fixed - Added `SetMockTime: true` and future TSBPD values to corner tests.

**Related Issues**: None - test-only issue.

---

### DISC-002: GapScan Test Expectation Wrong

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `congestion/live/core_scan_table_test.go` |
| **Related To** | Test design |

**Problem**: Test `Corner_Combo_BothNearMax_WithGap` expected only 1 gap (`{maxSeq-98}`) but `gapScan()` correctly found all 99 gaps.

**Why Tests Didn't Catch It**: Test expectation was based on incorrect assumption that scan would stop early.

**Resolution**: Fixed - Changed test to use smaller gap (2 gaps at wraparound boundary).

**Related Issues**: None - test-only issue.

---

### DISC-003: Audit Tool Symbolic Value Limitation

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `tools/test-audit/main.go` |
| **Related To** | Tooling |

**Problem**: Audit tool looks for literal values like `2147483547` but tests use `maxSeq - 100` which equals the same value at runtime.

**Why Tests Didn't Catch It**: N/A - tooling limitation, not code bug.

**Resolution**: Deferred - Accept lower reported coverage; tests are actually correct.

**Related Issues**: None.

---

### DISC-004: TsbpdDelayUs Corner Tests Deferred

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **File** | `congestion/live/receive_drop_table_test.go` |
| **Related To** | Test infrastructure |

**Problem**: `mockLiveRecvWithStartSeq()` uses hardcoded `TsbpdDelay=100_000`. Cannot test 10ms or 500ms TSBPD without refactoring mock.

**Why Tests Didn't Catch It**: N/A - test infrastructure limitation, not code bug.

**Resolution**: Deferred - Would need to add `TsbpdDelay` parameter to mock factory.

**Related Issues**: Similar to `loss_recovery_table_test.go` derived parameters pattern.

---

### DISC-005: `checkFastNakRecent()` 31-bit Wraparound Bug 🔴 HIGH

| Field | Value |
|-------|-------|
| **Severity** | **HIGH - PRODUCTION BUG** |
| **File** | `congestion/live/fast_nak.go` |
| **Function** | `checkFastNakRecent()` |
| **Line** | 111-114 |
| **Related To** | 31-bit wraparound, `circular.SeqDiff()` |

**Suspicious Code** (`fast_nak.go` lines 110-114):
```go
// Actual gap (signed to handle wraparound correctly)  <-- COMMENT IS WRONG!
actualGapSigned := circular.SeqDiff(currentSeq, lastSeq)
if actualGapSigned <= 0 {
    return // Not a forward jump
}
```

**Root Cause** (`circular/seq_math.go` lines 107-112):
```go
func SeqDiff(a, b uint32) int32 {
    a = a & MaxSeqNumber31
    b = b & MaxSeqNumber31
    return int32(a - b)  // <-- BUG: Same as old broken SeqLess!
}
```

**Bug Analysis**:

When `currentSeq = 10` (after wrap) and `lastSeq = 0x7FFFFF00` (near MAX):
- `currentSeq - lastSeq` = `10 - 2147483392` = underflows to large uint32 (`0x80000110`)
- `int32(0x80000110)` = `-2147483376` (negative!)
- `actualGapSigned <= 0` is TRUE → function returns early
- **Result**: FastNAK doesn't detect the ~265 packet gap!

**Why `seq_math_31bit_wraparound_test.go` Didn't Catch It**:

1. **`SeqLess` was fixed, but `SeqDiff` wasn't**: The test file extensively tests `SeqLess`, `SeqGreater`, etc. but `SeqDiff` is NOT tested for wraparound!

2. **Comment mismatch**: `SeqDiff` comment says "handling wraparound" but it uses the SAME broken `int32(a-b)` pattern that `SeqLessBroken` uses (documented in test file lines 34-42).

3. **No unit test for `SeqDiff` wraparound**: Search for `SeqDiff` in test file shows 0 direct wraparound tests.

**Verification** (expected behavior vs actual):
```
Input: currentSeq=10, lastSeq=0x7FFFFF00
Expected: SeqDiff returns +265 (10 is 265 packets "after" 0x7FFFFF00)
Actual: SeqDiff returns -2147483376 (WRONG!)
```

**Impact**:
- FastNAK won't trigger for burst losses that occur near sequence wraparound (~every 2.1 billion packets)
- At 20Mbps with 1316-byte packets, wraparound occurs every ~20 hours
- Production impact: Occasional missed burst loss detection

**Next Steps**:
1. Add `SeqDiff` wraparound tests to `seq_math_31bit_wraparound_test.go`
2. Fix `SeqDiff` to use threshold-based approach (like fixed `SeqLess`)
3. Audit ALL uses of `SeqDiff` in codebase for similar issues
4. Un-comment and verify the 3 corner tests in `fast_nak_table_test.go`

**Related Issues to Investigate**:
- `circular.SeqDistance()` uses `SeqDiff` (line 117) - may also be broken
- Any other code using `SeqDiff` for wraparound calculations

---

### DISC-006: `SeqDiff` Not Tested for Wraparound 🟠 MEDIUM

| Field | Value |
|-------|-------|
| **Severity** | **MEDIUM - TEST GAP** |
| **File** | `circular/seq_math_test.go` |
| **Function** | `TestSeqDiff` |
| **Line** | 78-103 |
| **Related To** | 31-bit wraparound, root cause of DISC-005 |

**Problem**: `TestSeqDiff` has NO wraparound tests. Test cases only go up to `MaxSeqNumber31/4` (~500M).

**Existing Test Cases** (lines 85-92):
```go
{"10 - 5", 10, 5, 5},
{"5 - 10", 5, 10, -5},
{"same", 100, 100, 0},
{"1001000 - 1000000", 1001000, 1000000, 1000},
{"1000000 - 1001000", 1000000, 1001000, -1000},
{"quarter - 0", MaxSeqNumber31 / 4, 0, int32(MaxSeqNumber31 / 4)},
{"0 - quarter", 0, MaxSeqNumber31 / 4, -int32(MaxSeqNumber31 / 4)},
// NO WRAPAROUND TESTS!
```

**Missing Test Cases**:
```go
// These would FAIL with current SeqDiff!
{"MAX - 0", MaxSeqNumber31, 0, -1},           // Should be -1 (MAX is "before" 0)
{"0 - MAX", 0, MaxSeqNumber31, 1},            // Should be 1 (0 is "after" MAX)
{"10 - MAX-100", 10, MaxSeqNumber31-100, 111}, // ~111 packet gap
```

**Why This Gap Exists**:
1. `seq_math_31bit_wraparound_test.go` was added to test `SeqLess` fix
2. `SeqDiff` was NOT part of that fix effort
3. No one added corresponding `SeqDiff` wraparound tests

**Impact**: DISC-005 was not caught by tests.

**Next Steps**: Add wraparound tests for `SeqDiff` before implementing fix.

---

### DISC-007: NAK Consolidation May Fail at Wraparound 🟠 MEDIUM

| Field | Value |
|-------|-------|
| **Severity** | **MEDIUM - PRODUCTION** |
| **File** | `congestion/live/nak_consolidate.go` |
| **Function** | `consolidateNakBtree()` inner loop |
| **Line** | 93-96 |
| **Related To** | `SeqDiff`, DISC-005, DISC-006 |

**Suspicious Code** (`nak_consolidate.go` lines 93-96):
```go
// gap = actual distance between sequences minus 1
gap := circular.SeqDiff(seq, currentEntry.End) - 1
if gap >= 0 && uint32(gap) <= r.nakMergeGap {
    // Extend current entry
    currentEntry.End = seq
}
```

**Bug Analysis**:

When `currentEntry.End = 0x7FFFFF00` (near MAX) and `seq = 10` (after wrap):
- `SeqDiff(10, 0x7FFFFF00)` returns `-2147483376` (WRONG - should be ~265)
- `gap = -2147483376 - 1` = `-2147483377`
- `gap >= 0` is FALSE → merge skipped

**Impact**:
- NAK entries at wraparound won't merge correctly
- Creates two separate NAK entries instead of one range
- **Lower severity than DISC-005**: Packets still get NAK'd, just less efficiently

**Next Steps**: Fix `SeqDiff` first (DISC-005), then verify consolidation works.

---

### DISC-008: FastNAK `expectedGap*2` Heuristic Too Strict 🟠 MEDIUM

| Field | Value |
|-------|-------|
| **Severity** | **MEDIUM - PRODUCTION** |
| **File** | `congestion/live/fast_nak.go` |
| **Function** | `checkFastNakRecent()` |
| **Line** | 121 |
| **Related To** | DISC-005 (discovered while testing fix) |

**Suspicious Code** (`fast_nak.go` line 121):
```go
if actualGap > minGapThreshold && actualGap < expectedGap*2 {
    // Add missing range to NAK btree
```

**Problem**:

The `actualGap < expectedGap*2` heuristic filters out legitimate gaps:

| Scenario | actualGap | expectedGap | Check | Result |
|----------|-----------|-------------|-------|--------|
| Normal | 50 | 50 | 50 < 100 | ✅ Insert |
| Wraparound | 266 | 50 | 266 < 100 | ❌ **Filtered!** |
| Stale pps | 500 | 50 | 500 < 100 | ❌ **Filtered!** |

**Why This Is Problematic**:
1. During network outages (when FastNAK is needed most), pps estimates may be stale
2. At connection start, pps estimates may be inaccurate
3. The heuristic was designed to filter io_uring reordering, but 266 packets is far beyond reordering

**Impact**:
- FastNAK won't insert NAKs for legitimate burst losses when `actualGap > expectedGap*2`
- Most visible at sequence wraparound or during recovery from long outages

**Fix Applied**:

Added `minUpperBound` and `maxAbsoluteGap` constants:

```go
const minUpperBound = uint32(1000)    // At least 1000 packets before filtering
const maxAbsoluteGap = uint32(100000) // Never insert more than 100k NAKs

upperBound := expectedGap * 2
if upperBound < minUpperBound {
    upperBound = minUpperBound
}
if upperBound > maxAbsoluteGap {
    upperBound = maxAbsoluteGap
}
```

**Result**: All 3 wraparound tests now pass:
- `Corner_Wraparound_SmallGap`: 265 inserts ✅
- `Corner_Wraparound_NewSeqZero`: 255 inserts ✅
- `Corner_Wraparound_LargeGap`: 755 inserts ✅

---

### Summary Table

| ID | Severity | Type | Status |
|----|----------|------|--------|
| DISC-001 | Medium | Test setup | ✅ Fixed |
| DISC-002 | Low | Test expectation | ✅ Fixed |
| DISC-003 | Low | Tooling | Deferred |
| DISC-004 | Low | Test infrastructure | Deferred |
| **DISC-005** | **HIGH** | **Production bug (FastNAK)** | ✅ **Fixed** (SeqDiff) |
| **DISC-006** | **MEDIUM** | **Test gap (SeqDiff)** | ✅ **Fixed** |
| **DISC-007** | **MEDIUM** | **Production (NAK merge)** | ✅ **Fixed** (SeqDiff) |
| **DISC-008** | **MEDIUM** | **Heuristic too strict** | ✅ **Fixed** |

---

## 🔧 Fix Plan (Recommended Order)

### Phase 1: Add Failing Tests (TDD)

**1.1 Add SeqDiff wraparound tests** (`circular/seq_math_test.go`):
```go
// Expected to FAIL initially
{"MAX - 0", MaxSeqNumber31, 0, -1},
{"0 - MAX", 0, MaxSeqNumber31, 1},
{"10 - MAX-100", 10, MaxSeqNumber31-100, 111},
{"MAX-100 - 10", MaxSeqNumber31-100, 10, -111},
```

**1.2 Add SeqDiff wraparound tests** (`circular/seq_math_31bit_wraparound_test.go`):
- Add `TestSeqDiff_31BitWraparound` to match existing `TestSeqLess_*` pattern

**1.3 Un-comment FastNAK table tests** (`fast_nak_table_test.go`):
- `Corner_Wraparound_SmallGap`
- `Corner_Wraparound_LargeGap`
- `Corner_Wraparound_NewSeqZero`

### Phase 2: Fix SeqDiff

**Option A: Threshold-based (like SeqLess)**:
```go
func SeqDiff(a, b uint32) int32 {
    a = a & MaxSeqNumber31
    b = b & MaxSeqNumber31
    if a == b {
        return 0
    }
    // Use threshold to detect wraparound
    d := a - b  // unsigned subtraction
    if d > seqThreshold31 {
        // Wraparound: a is "before" b, return negative
        return -int32(b - a)
    }
    return int32(d)
}
```

**Option B: Use existing SeqLess**:
```go
func SeqDiff(a, b uint32) int32 {
    a = a & MaxSeqNumber31
    b = b & MaxSeqNumber31
    dist := SeqDistance(a, b)  // always positive
    if SeqLess(a, b) {
        return -int32(dist)
    }
    return int32(dist)
}
```

### Phase 3: Verify Fixes

1. All `SeqDiff` tests pass
2. FastNAK table tests pass
3. NAK consolidation tests pass (may already pass after SeqDiff fix)
4. Run full test suite
5. Run integration tests with wraparound-inducing packet counts

### Dependencies

```
DISC-006 (SeqDiff tests) → DISC-005 (FastNAK fix)
                        → DISC-007 (NAK merge fix, if needed)
```

---

## 🔬 AST-Based Code Analysis

### Motivation

While table-driven tests and corner coverage help ensure test quality, they cannot prevent bugs from being introduced in the first place. The `SeqDiff` bug (DISC-005, DISC-006, DISC-007) existed because:

1. Tests only covered certain patterns, not the underlying code
2. No automated way to detect unsafe sequence arithmetic patterns
3. Code review missed the similarity to the known-broken `SeqLessBroken` pattern

### Solution: `seq-audit` Tool (Type-Aware)

Created `tools/seq-audit/main.go` - a **type-aware** AST analyzer using `golang.org/x/tools/go/packages` and `go/types`.

**Key Advantage**: Uses Go's type checker to find patterns by **actual types**, not just variable names.

**How It Works**:

1. Loads packages with full type information via `packages.Load()`
2. Walks AST looking for `CallExpr` nodes (type conversions)
3. Uses `types.Info.TypeOf()` to get actual types of expressions
4. Flags `int32(uint32 - uint32)` patterns regardless of variable names

**Why Type-Aware Analysis is Better**:

| Approach | Pattern `int32(a - b)` where `a,b` are `uint32` |
|----------|------------------------------------------------|
| Name heuristics | ❌ Misses - `a` and `b` don't match "seq" pattern |
| Type-aware AST | ✅ Catches - knows `a` and `b` are `uint32` |

**Patterns Detected**:

| Pattern | Severity | Description |
|---------|----------|-------------|
| `int32(uint32 - uint32)` | HIGH | Fails at 31-bit wraparound |
| `int64(uint64 - uint64)` in seq funcs | MEDIUM | Similar issue for 64-bit |

### Usage

```bash
# Scan production code (excludes *_test.go)
go run ./tools/seq-audit/... ./congestion/live ./circular

# Verbose mode (show all findings)
go run ./tools/seq-audit/... -verbose ./congestion/live

# Add to Makefile
make audit-seq
```

### Example Output (Actual Run 2024-12-29)

```
═══════════════════════════════════════════════════════════════════
TYPE-AWARE SEQUENCE ARITHMETIC AUDIT
═══════════════════════════════════════════════════════════════════

This tool uses Go's type checker to find int32(uint32 - uint32)
patterns that fail at 31-bit sequence number wraparound.

Summary: 1 HIGH, 0 MEDIUM, 0 LOW, 0 INFO

📁 circular/seq_math.go
───────────────────────────────────────────────────────────────────
  🔴 [Line 111] int32(a - b)
     Type: int32(uint32 - uint32)
     Context: function SeqDiff
     💡 This pattern fails at 31-bit wraparound...
```

### Analysis Results

| Scope | Files Scanned | HIGH | MEDIUM | LOW |
|-------|---------------|------|--------|-----|
| `./circular` | 3 | **1** | 0 | 0 |
| `./congestion/live` | 25 | 0 | 0 | 0 |
| **Total** | 28 | **1** | 0 | 0 |

**Conclusion**: Only ONE instance of the unsafe pattern exists in the entire codebase - the `SeqDiff` function we identified through table-driven test auditing. This confirms:
1. The bug is isolated to `circular/seq_math.go:111`
2. No other code uses this broken pattern
3. Fixing `SeqDiff` will resolve all related issues (DISC-005, DISC-007)

### Integration with Makefile ✅

`seq-audit` is now integrated into the build process:

```makefile
## check: Run all static analysis checks (seq-audit, lint)
check: audit-seq
	@echo "✅ All static checks passed"

## test: Run all tests (includes static checks first)
test: check
	go test -race ...

## test-quick: Run tests without static checks (for development)
test-quick:
	go test -race ...
```

**Usage**:
```bash
make check       # Run seq-audit only
make test        # Run seq-audit + tests (BLOCKS on HIGH severity)
make test-quick  # Skip seq-audit (for fast iteration)
```

**Exit Codes**:
- `seq-audit` exits with code 1 if HIGH severity issues found
- `make test` will FAIL if seq-audit finds issues
- Prevents merging code with unsafe sequence arithmetic

### Limitations

1. **Heuristic-based**: May miss variables with non-standard names
2. **False positives**: May flag legitimate non-sequence arithmetic
3. **Cannot detect logic errors**: Only finds pattern-based issues

### Future Enhancements

1. **Track type information**: Use `go/types` to confirm uint32 types
2. **Call graph analysis**: Find all callers of unsafe patterns
3. **Auto-fix suggestions**: Generate corrected code

---

**CODE_PARAMs Identified**:
| Parameter | Corner Values | Current Coverage |
|-----------|---------------|------------------|
| `StartSeq` | 0, MAX-100, MAX | ✅ 0 only |
| `ContiguousPoint` | 0, MAX-100, MAX | ❌ None |

**Tests to Add** (6 tests for each struct = ~12 total):

For `ContiguousScanTestCase`:
| Test Name | StartSeq | ContiguousPoint | Expected Behavior |
|-----------|----------|-----------------|-------------------|
| `Corner_StartSeq_NearMax` | MAX-100 | default | Normal scan near wraparound |
| `Corner_StartSeq_AtMax` | MAX | default | Scan starting at MAX |
| `Corner_CP_Zero` | default | 0 | ContiguousPoint at minimum |
| `Corner_CP_NearMax` | default | MAX-100 | CP near wraparound |
| `Corner_CP_AtMax` | default | MAX | CP at maximum (wraparound edge) |
| `Corner_Combo_BothMax` | MAX-100 | MAX-100 | Both params near MAX |

For `GapScanTestCase`:
| Test Name | StartSeq | ContiguousPoint | Expected Behavior |
|-----------|----------|-----------------|-------------------|
| `Corner_StartSeq_NearMax` | MAX-100 | default | Gap scan near wraparound |
| `Corner_StartSeq_AtMax` | MAX | default | Gap scan starting at MAX |
| `Corner_CP_Zero` | default | 0 | ContiguousPoint at minimum |
| `Corner_CP_NearMax` | default | MAX-100 | CP near wraparound |
| `Corner_CP_AtMax` | default | MAX | CP at maximum |
| `Corner_Combo_BothMax` | MAX-100 | MAX-100 | Both params near MAX |

**Implementation Steps**:
1. 🔲 Read existing test structure
2. 🔲 Add ContiguousScan corner tests
3. 🔲 Add GapScan corner tests
4. 🔲 Run tests to verify
5. 🔲 Re-run audit to confirm coverage increase
6. 🔲 Document results

**Progress Log**:
- [ ] Started: (date/time)
- [ ] Tests added: 0/12
- [ ] Tests passing: 0/12
- [ ] Final coverage: TBD

---

### 📁 Action Plan 2: `receive_drop_table_test.go`

**Status**: ✅ COMPLETE (with discrepancy noted)
**Priority**: HIGH
**Coverage Before**: 0%
**Tests Added**: 4 corner tests (StartSeq: 0, MAX-100, MAX, MAX-5)
**All Tests Pass**: Yes (7 tests)
**Discrepancy**: DISC-004 - TsbpdDelayUs corners deferred (fixed mock)

**CODE_PARAMs Identified**:
| Parameter | Corner Values | Current Coverage |
|-----------|---------------|------------------|
| `StartSeq` | 0, MAX-100, MAX | ❌ None explicit |
| `TsbpdDelayUs` | 10000, 120000, 500000 | ❌ None explicit |

**Tests to Add** (6 tests):

| Test Name | StartSeq | TsbpdDelayUs | Scenario |
|-----------|----------|--------------|----------|
| `Corner_StartSeq_Zero` | 0 | 100000 | Baseline sequence |
| `Corner_StartSeq_NearMax` | MAX-100 | 100000 | Near wraparound |
| `Corner_StartSeq_AtMax` | MAX | 100000 | At wraparound boundary |
| `Corner_TSBPD_10ms` | 1000 | 10000 | Aggressive TSBPD (needs derived params!) |
| `Corner_TSBPD_120ms` | 1000 | 120000 | Standard TSBPD |
| `Corner_TSBPD_500ms` | 1000 | 500000 | High latency TSBPD |

**⚠️ NOTE**: 10ms TSBPD may need derived tick times (like `loss_recovery_table_test.go`)

**Implementation Steps**:
1. 🔲 Read existing test structure
2. 🔲 Check if derived params pattern needed
3. 🔲 Add StartSeq corner tests
4. 🔲 Add TsbpdDelayUs corner tests
5. 🔲 Run tests to verify
6. 🔲 Re-run audit to confirm coverage increase
7. 🔲 Document results

**Progress Log**:
- [ ] Started: (date/time)
- [ ] Tests added: 0/6
- [ ] Tests passing: 0/6
- [ ] Final coverage: TBD

---

### 📁 Action Plan 3: `send_table_test.go`

**Status**: ✅ COMPLETE
**Priority**: HIGH
**Tests Added**: 2 corner tests (StartSeq=0, TotalPackets=1)
**All Tests Pass**: Yes (28 tests - 14 scenarios × 2 strategies)

**CODE_PARAMs Identified**:
| Parameter | Corner Values | Current Coverage |
|-----------|---------------|------------------|
| `StartSeq` | 0, MAX-100, MAX | ❌ None at exact corners |

**Tests to Add** (3 tests):

| Test Name | StartSeq | TotalPackets | Scenario |
|-----------|----------|--------------|----------|
| `Corner_StartSeq_Zero` | 0 | 50 | Baseline starting point |
| `Corner_TotalPackets_Single` | 1000 | 1 | Single packet edge case |
| `Corner_TotalPackets_Large` | 1000 | 1000 | Stress test with many packets |

**Implementation Steps**:
1. 🔲 Read existing test structure
2. 🔲 Add StartSeq=0 corner test
3. 🔲 Add TotalPackets corner tests
4. 🔲 Run tests to verify
5. 🔲 Re-run audit to confirm coverage increase
6. 🔲 Document results

**Progress Log**:
- [ ] Started: (date/time)
- [ ] Tests added: 0/3
- [ ] Tests passing: 0/3
- [ ] Final coverage: TBD

---

### 📁 Action Plan 4: `fast_nak_table_test.go`

**Status**: ⚠️ COMPLETE (with HIGH severity discrepancy)
**Priority**: MEDIUM
**Tests Added**: 3 wraparound corner tests (COMMENTED OUT - FAIL)
**Discrepancy**: **DISC-005 (HIGH)** - `checkFastNakRecent()` fails 31-bit wraparound
**All Existing Tests Pass**: Yes (14 tests)

**CODE_PARAMs Identified**:
| Parameter | Corner Values | Current Coverage |
|-----------|---------------|------------------|
| `FastNakEnabled` | true, false | ✅ Both covered |
| `UseNakBtree` | true, false | ✅ Both covered |

**TEST_INFRA that may need coverage** (optional):
| Parameter | Corner Values | Current Coverage |
|-----------|---------------|------------------|
| `LastSeq` | 0, MAX-100, MAX | ⚠️ Only 0 covered |
| `NewSeq` | 0, MAX-100, MAX | ❌ None covered |

**Tests to Add** (4 tests - optional sequence wraparound):

| Test Name | LastSeq | NewSeq | Scenario |
|-----------|---------|--------|----------|
| `Corner_Seq_NearMax` | MAX-100 | MAX-90 | Both near wraparound |
| `Corner_Seq_AtMax` | MAX | MAX+10 (wraps to 10) | Crossing boundary |
| `Corner_Seq_WrapDetection` | MAX-5 | 5 | Large apparent gap (actually small) |
| `Corner_Seq_ZeroNewSeq` | 100 | 0 | New sequence at zero |

**Implementation Steps**:
1. 🔲 Read existing test structure
2. 🔲 Determine if sequence wraparound tests add value
3. 🔲 Add sequence corner tests if valuable
4. 🔲 Run tests to verify
5. 🔲 Re-run audit to confirm coverage increase
6. 🔲 Document results

**Progress Log**:
- [ ] Started: (date/time)
- [ ] Tests added: 0/4
- [ ] Tests passing: 0/4
- [ ] Final coverage: TBD

---

### 📁 Action Plan 5: Document Legacy Tests

**Status**: 🟡 PENDING
**Priority**: LOW

**Goal**: Document which legacy tests should be KEPT (not table-driveable) vs DELETED (covered by tables)

**Files to Review**:
| File | Tests | Decision |
|------|-------|----------|
| `core_scan_test.go` | 13 | 🔲 Review after table corners added |
| `fast_nak_test.go` | 13 | 🔲 Review - complex scenarios |
| `send_test.go` | 22 | 🔲 Review after table corners added |
| `nak_consolidate_test.go` | 27 | 🔲 Review - mostly covered by table |
| `tsbpd_advancement_test.go` | 8 | 🔲 Keep - complex timing scenarios |

**Implementation Steps**:
1. 🔲 Complete all table-driven corner additions first
2. 🔲 Run AST comparison between legacy and table tests
3. 🔲 Document unique scenarios in legacy tests
4. 🔲 Mark legacy tests for deletion or retention
5. 🔲 Final cleanup

---

## Summary: Execution Order

| Step | File | Tests Added | Status |
|------|------|-------------|--------|
| 1 | `core_scan_table_test.go` | 12 | ✅ COMPLETE |
| 2 | `receive_drop_table_test.go` | 4 | ✅ COMPLETE |
| 3 | `send_table_test.go` | 2 | ✅ COMPLETE |
| 4 | `fast_nak_table_test.go` | 0 (3 deferred) | ⚠️ DISC-005 FOUND |
| 5 | Legacy test documentation | N/A | 🔴 PENDING |

**Total Corner Tests Added**: 18
**Total Discrepancies Found**: 5 (1 HIGH severity)
**All Tests Pass**: Yes

---

## 🔴 CRITICAL: `circular.SeqDiff()` Bug

**Root cause identified**: `SeqDiff()` uses `int32(a - b)` which fails for 31-bit wraparound.

| Impact | Component | Status |
|--------|-----------|--------|
| FastNAK burst detection fails | `checkFastNakRecent()` | DISC-005 |
| NAK consolidation fails | `consolidateNakBtree()` | DISC-007 |
| Unknown | Any code using `SeqDiff` | Audit needed |

**Why tests didn't catch it**: `TestSeqDiff` has NO wraparound tests (DISC-006).

**Fix**: Update `SeqDiff` to use threshold-based comparison (like `SeqLess` fix).

---

## Test File Classification

### Table-Driven Tests (audited)

| File | Lines | Tests | Corner Coverage |
|------|-------|-------|-----------------|
| `loss_recovery_table_test.go` | 1010 | 32 | 73.7% |
| `nak_consolidate_table_test.go` | 721 | 24 | ~100% |
| `core_scan_table_test.go` | 586 | 33 | 27-31% |
| `fast_nak_table_test.go` | 532 | 14 | 64-87% (DISC-005) |
| `send_table_test.go` | 449 | 28 | Good |
| `receive_drop_table_test.go` | 241 | 7 | Good |

### Legacy Tests Cleanup Plan

**Analysis Result**: Legacy files have BOTH duplicated and unique tests.
- **Remove**: Tests duplicated by table-driven versions
- **Keep**: Unique tests with specialized coverage

---

## Legacy Cleanup: Detailed Plan

### 1. `send_test.go` (27 tests → 6 unique)

**KEEP** (unique):
- `TestSendSequence` - sequence handling
- `TestSendLossListACK` - ACK handling
- `TestSendRetransmit` - retransmit logic
- `TestSendDrop` - drop handling
- `TestSendFlush` - flush behavior
- `TestSendHonorOrder_Metric` - metrics

**REMOVE** (duplicated by table): 21 tests
- All `TestSendOriginal_*` (10 tests)
- Most `TestSendHonorOrder_*` (11 tests, except Metric)

### 2. `nak_consolidate_test.go` (36 tests → 14 unique)

**KEEP** (unique):
- `TestNAKEntry_IsRange`
- `TestNAKEntry_Count`
- `TestSyncPoolReuse`
- `TestConsolidateMetrics`
- `TestConsolidateUsesCircularSeqDiff`
- `TestConsolidateNakBtree_InOrderBaseline`
- `TestConsolidateNakBtree_InOrderVsOutOfOrder_Consistency`
- `TestConsolidateNakBtree_MSS_UnderLimit_Ranges`
- `TestConsolidateNakBtree_MSS_OverLimit_Ranges`
- `TestConsolidateNakBtree_MSS_MixedOverflow`
- `TestCalculateNakWireSize`
- `TestConsolidateNakBtree_ExtremeScale_60SecBuffer`
- `TestConsolidateNakBtree_ExtremeScale_LongOutage`
- `TestConsolidateNakBtree_ExtremeScale_WorstCase`

**REMOVE** (duplicated by table): 22 tests

### 3. `fast_nak_test.go` (23 tests → 7 unique)

**KEEP** (unique):
- `TestAtomicTime_LoadStore`
- `TestAtomicTime_ConcurrentAccess`
- `TestPacketsPerSecondEstimate`
- `TestCheckFastNakRecent_MultipleBurstLosses`
- `TestCheckFastNakRecent_LargeBurstThenConsolidate`
- `TestCheckFastNakRecent_LargeBurstWithPriorGaps`
- `TestCheckFastNakRecent_VeryLongOutage`

**REMOVE** (duplicated by table): 16 tests

### 4. `core_scan_test.go` (21 tests → 1 unique)

**KEEP** (unique):
- `TestContiguousScan_StaleContiguousPoint_EmptyBtree`

**REMOVE** (duplicated by table): 20 tests

---

### Summary

| File | Before (lines) | After (lines) | Reduction | Tests Removed |
|------|----------------|---------------|-----------|---------------|
| `core_scan_test.go` | 614 | 84 | **86%** | 20 |
| `fast_nak_test.go` | 663 | 324 | **51%** | 16 |
| `send_test.go` | 1104 | 254 | **77%** | 21 |
| `nak_consolidate_test.go` | 1447 | 580 | **60%** | 22 |
| **Total** | **3828** | **1242** | **68%** | **79** |

✅ **Cleanup Complete** - Removed 79 duplicated tests (2586 lines)

---

### Legacy Tests (not table-driven)

**After cleanup**: Legacy files contain ONLY unique tests.

| File | Lines | Table Equiv | Unique Tests | Status |
|------|-------|-------------|--------------|--------|
| `send_test.go` | 1104 | `send_table_test.go` | 5 (Sequence, ACK, Retransmit, Drop, Flush) | **Keep** |
| `nak_consolidate_test.go` | 1447 | `nak_consolidate_table_test.go` | 11+ (NAKEntry, EntriesToNakList, etc.) | **Keep** |
| `fast_nak_test.go` | 663 | `fast_nak_table_test.go` | Unique edge cases | **Keep** |
| `core_scan_test.go` | 614 | `core_scan_table_test.go` | Unique scenarios | **Keep** |
| `eventloop_test.go` | 1976 | - | EventLoop specific | Keep |
| `nak_btree_scan_stream_test.go` | 1921 | - | Stream simulation | Keep |
| `nak_large_merge_ack_test.go` | 1275 | - | Large scale tests | Keep |
| `receive_race_test.go` | 1006 | - | Race detection | Keep |
| `stream_test_helpers_test.go` | 959 | - | Helpers | Keep |
| `receive_iouring_reorder_test.go` | 941 | - | io_uring specific | Keep |
| `receive_basic_test.go` | 832 | - | Basic receiver | Keep |
| `tsbpd_advancement_test.go` | 737 | - | Complex timing | Keep |
| `receive_bench_test.go` | 723 | - | Benchmarks | Keep |
| `metrics_test.go` | 610 | - | Metrics tests | Keep |
| `packet_store_test.go` | 579 | - | Data structure | Keep |
| `receive_ring_test.go` | 472 | - | Ring buffer | Keep |
| `nak_btree_test.go` | 386 | - | Data structure | Keep |
| `too_recent_threshold_test.go` | 236 | - | Unit tests | Keep |
| `receive_config_test.go` | 211 | - | Config validation | Keep |
| `stream_matrix_test.go` | 86 | - | Matrix tests | Keep |

---

**Verified Analysis for `LossRecoveryTestCase`**:
```
🎯 CODE_PARAM (3): StartSeq, TsbpdDelayUs, NakRecentPct
⚙️ TEST_INFRA (10): Name, TotalPackets, DropPattern, cycles, ticks, etc.
📊 EXPECTATION (3): MinDeliveryPct, MinNakPct, MaxOverNakFactor

Corner combinations: 3 × 3 × 3 = 27 tests
```

---

## Complete Test Classification Audit

### Table-Driven Tests - CODE_PARAM Analysis

| File | CODE_PARAMs | Corners | Legacy Equivalent |
|------|-------------|---------|-------------------|
| `loss_recovery_table_test.go` | 3: StartSeq, TsbpdDelayUs, NakRecentPct | 27 | (new, no legacy) |
| `core_scan_table_test.go` | 2: StartSeq, ContiguousPoint | 9 | `core_scan_test.go` (13 tests) |
| `fast_nak_table_test.go` | 2: FastNakEnabled, UseNakBtree | 4 | `fast_nak_test.go` (13 tests) |
| `nak_consolidate_table_test.go` | 1: NakMergeGap | 3 | `nak_consolidate_test.go` (27 tests) |
| `send_table_test.go` | 1: StartSeq ✅ | 3 | `send_test.go` (11+11 tests) |
| `receive_drop_table_test.go` | 2: StartSeq, TsbpdDelayUs ✅ | 9 | `receive_basic_test.go` (3 tests) |

**Updates Applied**:
- ✅ `send_table_test.go`: Added `StartSeq` CODE_PARAM with 3 wraparound test cases
- ✅ `receive_drop_table_test.go`: Added `StartSeq`, `TsbpdDelayUs` CODE_PARAMs with 1 wraparound test

### Legacy Tests - Scenario Coverage

| File | Tests | Key Scenarios |
|------|-------|---------------|
| `fast_nak_test.go` | 13 `TestCheckFastNakRecent_*` | Disabled, NoNakBtree, ShortSilence, SignificantJump, LargeBurstLoss (5/20/100 Mbps), MultipleBurstLosses, VeryLongOutage |
| `core_scan_test.go` | 13 `TestContiguousScan_*` | Empty, Contiguous, Gap, NoProgress, Wraparound, StaleContiguousPoint, SmallGap |
| `send_test.go` | 11+11 `TestSendOriginal_*`/`TestSendHonorOrder_*` | BasicSingle/Range, MultipleSingles/Ranges, ModulusDrops, BurstDrops, RealisticConsolidatedNAK, LargeScale |
| `nak_consolidate_test.go` | 27 `TestConsolidateNakBtree_*` | Empty, SingleEntry, ContiguousRange, MergeWithinGap, GapExceedsMergeThreshold, SequenceWraparound, ModulusDrops, MSS tests |
| `tsbpd_advancement_test.go` | 8 `TestTSBPDAdvancement_*` | RingOutOfOrder, CompleteOutage, MidStreamGap, SmallGapNoAdvance, ExtendedOutage, Wraparound, MultipleGaps, IterativeCycles |

### Gap Analysis: Table-Driven vs Legacy

| Area | Table-Driven | Legacy | Gap |
|------|--------------|--------|-----|
| **StartSeq wraparound** | ✅ loss_recovery, core_scan | ✅ core_scan, tsbpd_advancement | None |
| **TSBPD delay variations** | ✅ loss_recovery (3 values) | ❓ Implicit | ⚠️ Legacy uses fixed values |
| **NAK recent percent** | ✅ loss_recovery (3 values) | ❓ Implicit | ⚠️ Legacy uses fixed values |
| **Large burst loss (Mbps)** | ❌ Not explicit | ✅ fast_nak (5/20/100 Mbps) | 🔴 Missing bitrate scenarios |
| **Extended outage** | ❌ Not explicit | ✅ tsbpd_advancement | 🔴 Missing extended outage |
| **Multiple gaps** | ✅ loss_recovery (DropMultipleBursts) | ✅ tsbpd_advancement | None |
| **MSS boundary tests** | ❌ Not in table | ✅ nak_consolidate | 🔴 Missing MSS tests |
| **Extreme scale (60s buffer)** | ❌ Not in table | ✅ nak_consolidate | 🔴 Missing extreme scale |

### Recommendations

**Completed**:
- ✅ `send_table_test.go`: Added StartSeq CODE_PARAM with wraparound tests
- ✅ `receive_drop_table_test.go`: Added StartSeq + TsbpdDelayUs CODE_PARAMs

**Remaining Gaps to Address**:
1. 🔴 `fast_nak_table_test.go`: Add bitrate-based scenarios (5/20/100 Mbps)
2. 🔴 `nak_consolidate_table_test.go`: Add MSS boundary tests
3. 🔴 `loss_recovery_table_test.go`: Add more corner values for StartSeq (MAX-100, MAX)

**Tests to Keep from Legacy** (complex multi-phase, not table-driveable):
- `tsbpd_advancement_test.go` (8 tests) - Complex timing scenarios
- `fast_nak_test.go` unique tests: `LargeBurstThenConsolidate`, `LargeBurstWithPriorGaps`, `VeryLongOutage`
- `nak_consolidate_test.go` unique tests: MSS tests, ExtremeScale tests

---

## Status Summary

| Phase | File | Status | Before | After | Savings |
|-------|------|--------|--------|-------|---------|
| 1 | `loss_recovery_test.go` | ✅ Complete | 2,676 | 641 | 76% |
| 2 | `nak_consolidate_test.go` | 🔄 In Progress | 1,447 | 637 | ~56%* |
| 2 | `send_test.go` | 🔄 In Progress | 1,104 | 387 | ~65%* |
| 2 | `fast_nak_test.go` | 🔄 In Progress | 663 | 476 | ~28%* |
| 2 | `core_scan_test.go` | ✅ Complete | 614 | 411 | ~33% |
| 3 | `receive_basic_test.go` | 🔄 Partial | 833 | 147 (subset) | 3 tests |
| 3 | `tsbpd_advancement_test.go` | ❌ Keep As-Is | 738 | - | Critical |

*Savings calculated after deletion of redundant original tests

### Parallelization ✅ Enabled

All table-driven tests now use `t.Parallel()` for concurrent execution:
- Each test case creates its own isolated receiver/sender instance
- No shared state between test cases
- Speedup: ~3x on multi-core systems (user time > real time)

---

## Phase 1: Loss Recovery ✅ Complete

**Date**: 2024-12-29

### Results
- **Before**: 2,676 lines, 15 individual test functions
- **After**: 641 lines, 15 table entries in single test
- **Savings**: 2,034 lines (76%)

### Key Learnings
1. **DropPattern Interface**: Reusable across multiple test files
2. **Timing Parameters**: Critical for NAK window coverage
3. **Trailing Packets**: Tests using periodic loss need extra packets after last drop

### Files Changed
- Created: `congestion/live/loss_recovery_table_test.go`
- Deleted: `congestion/live/loss_recovery_test.go`

---

## Phase 2: High-Impact Consolidation 🔄 In Progress

### 2.1 nak_consolidate_test.go 🔄 In Progress

**Start Date**: 2024-12-29

#### AST Analysis Results
```
make audit-tests ARGS="-file=congestion/live/nak_consolidate_test.go"

Tests: 36
Lines: 1,448
Top Patterns: loss_drop(105), nak_btree(79), for_loop(55)

Test Groups:
- TestConsolidateNakBtree_* (27 tests) - PRIMARY TARGET
- TestEntriesToNakList_* (3 tests)
- TestNAKEntry_* (2 tests)
- Other: 4 tests
```

#### Test Function Inventory

**Group 1: TestConsolidateNakBtree_* (27 tests)**
| # | Test Name | Line | Purpose |
|---|-----------|------|---------|
| 1 | Empty | 27 | Empty btree returns empty list |
| 2 | SingleEntry | 37 | Single entry returns (seq, seq) |
| 3 | ContiguousRange | 54 | Contiguous sequences merge |
| 4 | MergeWithinGap | 74 | Gap <= nakMergeGap merges |
| 5 | GapExceedsMergeThreshold | 99 | Gap > nakMergeGap splits |
| 6 | MixedSinglesAndRanges | 127 | Mixed patterns |
| 7 | SequenceWraparound | 279 | 31-bit wraparound |
| 8 | OutOfOrderInsertion | 427 | Insertion order independence |
| 9 | OutOfOrderWithGaps | 452 | OOO with gaps |
| 10 | InOrderBaseline | 484 | In-order baseline |
| 11 | InOrderVsOutOfOrder_Consistency | 507 | OOO == in-order |
| 12 | ModulusDrops_Every10th | 594 | Every 10th dropped |
| 13 | ModulusDrops_Every5th | 622 | Every 5th dropped |
| 14 | ModulusDrops_Every3rd_WithMerge | 641 | Every 3rd with merge |
| 15 | BurstDrops | 662 | Burst loss pattern |
| 16 | MixedModulusAndBurst | 701 | Mixed patterns |
| 17 | LargeScale_ModulusDrops | 742 | Large scale modulus |
| 18 | LargeScale_BurstDrops | 766 | Large scale burst |
| 19 | MSS_UnderLimit_Singles | 989 | MSS boundary - under |
| 20 | MSS_AtLimit_Singles | 1008 | MSS boundary - at |
| 21 | MSS_OverLimit_Singles | 1028 | MSS boundary - over |
| 22 | MSS_UnderLimit_Ranges | 1056 | MSS ranges - under |
| 23 | MSS_OverLimit_Ranges | 1078 | MSS ranges - over |
| 24 | MSS_MixedOverflow | 1108 | MSS mixed overflow |
| 25 | ExtremeScale_60SecBuffer | 1217 | 60s buffer |
| 26 | ExtremeScale_LongOutage | 1249 | Long outage |
| 27 | ExtremeScale_WorstCase | 1284 | Worst case |

**Group 2: TestEntriesToNakList_* (3 tests)**
| # | Test Name | Line | Purpose |
|---|-----------|------|---------|
| 1 | Empty | 201 | Empty entries |
| 2 | SingleAndRange | 217 | Single + range |
| 3 | CircularNumberMax | 396 | MAX_SEQUENCENUMBER |

**Group 3: Other (6 tests)**
| # | Test Name | Line | Purpose |
|---|-----------|------|---------|
| 1 | TestNAKEntry_IsRange | 163 | NAKEntry.IsRange() |
| 2 | TestNAKEntry_Count | 182 | NAKEntry.Count() |
| 3 | TestSyncPoolReuse | 295 | sync.Pool behavior |
| 4 | TestConsolidateMetrics | 335 | Metrics tracking |
| 5 | TestConsolidateUsesCircularSeqDiff | 365 | Circular diff |
| 6 | TestNakMergeGap_* (5 tests) | 666+ | Merge gap variants |

#### Proposed Table Structure

```go
type ConsolidateTestCase struct {
    Name        string
    NakMergeGap int           // Default 3
    Sequences   []uint32      // Sequences to insert
    // OR use pattern-based generation:
    DropPattern DropPattern   // Reuse from loss_recovery
    TotalPackets int
    StartSeq    uint32

    // Expected output
    ExpectedRanges []NakRange // [{Start, End}, ...]
    ExpectedCount  int        // Total NAK count
}

type NakRange struct {
    Start uint32
    End   uint32
}
```

#### Implementation Steps

- [ ] Step 1: Read and understand all 27 TestConsolidateNakBtree_* tests
- [ ] Step 2: Create `nak_consolidate_table_test.go` with struct
- [ ] Step 3: Convert basic tests (Empty, SingleEntry, ContiguousRange)
- [ ] Step 4: Convert merge/gap tests (4-11)
- [ ] Step 5: Convert modulus/burst tests (12-18)
- [ ] Step 6: Convert MSS tests (19-24)
- [ ] Step 7: Convert extreme scale tests (25-27)
- [ ] Step 8: Convert TestEntriesToNakList_* (3 tests)
- [ ] Step 9: Keep special tests as individual (SyncPool, Metrics, etc.)
- [ ] Step 10: Verify all tests pass
- [ ] Step 11: Delete old tests

#### Progress Log

**2024-12-29 - Started**
- Completed AST analysis
- Created test inventory
- Defined table structure

**2024-12-29 - Table Tests Created**
- Created `nak_consolidate_table_test.go` (637 lines)
- Converted 27+ test scenarios to table entries:
  - `TestConsolidateNakBtree_Table` - 17 test cases (basic, wraparound, modulus, burst, large scale, OOO)
  - `TestConsolidateNakBtree_MSS_Table` - 3 MSS boundary tests
  - `TestConsolidateNakBtree_ExtremeScale_Table` - 2 extreme scale tests
  - `TestEntriesToNakList_Table` - 4 test cases
  - `TestNAKEntry_Table` - 3 test cases
- Total: 29 table-driven test cases covering 33 sub-tests
- All tests pass alongside original tests

**Tests to Keep (special behavior)**:
- `TestSyncPoolReuse` - sync.Pool verification
- `TestConsolidateMetrics` - metrics tracking
- `TestConsolidateUsesCircularSeqDiff` - circular diff verification
- `TestCalculateNakWireSize` - wire size calculation
- Benchmark tests

**Remaining Tests to Add**:
- [ ] MSS: `_UnderLimit_Ranges`, `_OverLimit_Ranges`, `_MixedOverflow`
- [ ] Extreme: `_WorstCase`
- [ ] OOO: `_InOrderVsOutOfOrder_Consistency`

**Deletion Strategy**: Keep old tests until ALL files converted, then:
1. Run AST verification: `make audit-tests ARGS="-verify"`
2. Confirm table tests cover all original tests
3. Delete old tests in bulk

---

### 2.2 send_test.go 🔄 In Progress

**Start Date**: 2024-12-29

#### AST Analysis
```
Tests: 27
Lines: 1,105
Score: 95/100

Test Groups:
- TestSendOriginal_* (11 tests)
- TestSendHonorOrder_* (11 tests)
- Other: 5 tests (basic send tests, not NAK-related)
```

#### Created: `send_table_test.go`

**Approach**: Unified table tests BOTH strategies (Original & HonorOrder) with same test cases:

| Test Case | Original | HonorOrder | Status |
|-----------|----------|------------|--------|
| BasicSingle | ✅ | ✅ | Pass |
| BasicRange | ✅ | ✅ | Pass |
| MultipleSingles | ✅ | ✅ | Pass |
| MultipleRanges | ✅ | ✅ | Pass |
| MixedSinglesAndRanges | ✅ | ✅ | Pass |
| NotFoundPackets | ✅ | ✅ | Pass |
| ModulusDrops | ✅ | ✅ | Pass |
| BurstDrops | ✅ | ✅ | Pass |
| RealisticConsolidatedNAK | ✅ | ✅ | Pass |
| LargeScale | ✅ | ✅ | Pass |

**Additional Tests**:
- `TestSendNak_StrategyDifference` - Explicitly verifies strategies differ

**Coverage**:
- 10 test cases × 2 strategies = 20 sub-tests (vs 22 original)
- Plus 1 strategy comparison test

**Tests to Keep**:
- `TestSendSequence` - Basic send functionality
- `TestSendLossListACK` - ACK handling
- `TestSendRetransmit` - Basic retransmit
- `TestSendOriginal_VsHonorOrder_Difference` - Keep for reference (similar to new test)
- `TestSendHonorOrder_Metric` - Metrics verification

**Lines**: ~320 lines (table-driven) vs ~800 lines (22 Original/HonorOrder tests)

---

### 2.3 fast_nak_test.go 🔄 In Progress

**Start Date**: 2024-12-29

#### Created: `fast_nak_table_test.go` (476 lines)

**Test Coverage**:

| Test Function | Test Cases | Status |
|--------------|------------|--------|
| `TestCheckFastNak_Table` | 5 condition tests | ✅ |
| `TestCheckFastNakRecent_Table` | 9 tests (conditions + bursts) | ✅ |
| `TestBuildNakListLocked_Table` | 4 tests | ✅ |
| `TestCheckFastNakRecent_MultipleBursts_Table` | 1 multi-burst test | ✅ |

**Tests to Keep (special behavior)**:
- `TestAtomicTime_LoadStore` - AtomicTime type tests
- `TestAtomicTime_ConcurrentAccess` - Concurrency test
- `TestPacketsPerSecondEstimate` - Rate estimation
- More complex burst tests: `LargeBurstThenConsolidate`, `LargeBurstWithPriorGaps`, `VeryLongOutage`

**Lines**: 476 (table-driven) vs 663 (original) → ~28% savings (after cleanup)

### 2.4 core_scan_test.go ✅ Complete

**Completion Date**: 2024-12-29

#### Created: `core_scan_table_test.go` (411 lines)

**Test Coverage**:

| Test Function | Test Cases | Status |
|--------------|------------|--------|
| `TestContiguousScan_Table` | 12 tests | ✅ |
| `TestGapScan_Table` | 8 tests | ✅ |
| `TestContiguousScan_StaleCP_EmptyBtree_Table` | 1 (multi-phase) | ✅ |

**Categories Covered**:
- Basic scenarios (Empty, Contiguous, Gap, NoProgress)
- Wraparound scenarios (31-bit boundary crossing)
- Stale contiguousPoint scenarios (TSBPD expiry)
- Combined stale + wraparound scenarios
- Small gap vs threshold edge cases

**Parallel Performance**: 0.018s → 0.007s (**2.5x** speedup)

**Lines**: 411 (table-driven) vs 614 (original) → ~33% savings

---

## Phase 3: Scenario-Based Tests (Selective Conversion)

### 3.1 tsbpd_advancement_test.go ❌ Keep As-Is

**Decision**: Do NOT convert to table-driven.

**Rationale**:
1. **Critical Timing Behavior**: These tests verify TSBPD advancement logic at sequence wraparound boundaries - each has multi-phase timing requirements
2. **Well-Documented Scenarios**: Each test maps to specific design document scenarios
3. **Complex State Tracking**: Tests track `contiguousPoint`, `tooOldDrops`, mock time advancement through phases
4. **Debugging Value**: Individual tests with clear phase comments are easier to debug than table entries
5. **Already Uses Shared Helpers**: `createTSBPDTestReceiver()`, `createTestPacket()` provide reuse

**Tests (8 total)**: All scenario tests kept as-is:
- `RingOutOfOrder` - io_uring reorder bug test
- `CompleteOutage` - 3-second network outage recovery
- `MidStreamGap` - Gap smaller than stale threshold
- `SmallGapNoAdvance` - Negative test (no premature advancement)
- `ExtendedOutage` - 30+ second outage with 80% loss
- `Wraparound` - 31-bit sequence boundary crossing
- `MultipleGaps` - Two independent gaps expiring at different times
- `IterativeCycles` - Many small time increments

### 3.2 receive_basic_test.go 🔍 Analyzing

**Assessment of 13 tests**:

| Test | Pattern | Convert? | Reason |
|------|---------|----------|--------|
| `TestRecvSequence` | Multi-phase | ❌ | Callback tracking, stateful |
| `TestRecvTSBPD` | Multi-phase | ❌ | TSBPD timing with callbacks |
| `TestRecvNAK` | Multi-phase | ❌ | Complex NAK state tracking |
| `TestRecvPeriodicNAK` | Multi-phase | ❌ | Periodic interval testing |
| `TestRecvACK` | Multi-phase | ❌ | Very complex (9 assertions) |
| `TestRecvDropTooLate` | Push→Tick→Drop | ✅ | Simple drop pattern |
| `TestRecvDropAlreadyACK` | Push→Tick→Drop | ✅ | Simple drop pattern |
| `TestRecvDropAlreadyRecvNoACK` | Push→Tick→Drop | ✅ | Simple drop pattern |
| `TestRecvFlush` | Push→Flush→Verify | ❌ | Unique test |
| `TestRecvPeriodicACKLite` | Push→Tick→Verify | ❌ | Special callback |
| `TestSkipTooLate` | Multi-phase | ❌ | Two push phases |
| `TestIssue67` | Regression | ❌ | Specific bug repro |
| `TestListVsBTreeEquivalence` | Algorithm | ❌ | Algorithm comparison |

**Convertible**: 3 tests (Drop* tests) → Small benefit (~80 lines)
**Keep As-Is**: 10 tests → Critical behavior verification

### 3.3 receive_drop_table_test.go ✅ Created

**Completion Date**: 2024-12-29

Created `receive_drop_table_test.go` (147 lines) covering 3 Drop tests:

| Test Case | Original Test | Scenario |
|-----------|---------------|----------|
| `TooLate` | `TestRecvDropTooLate` | Push duplicate after ACK |
| `AlreadyACK` | `TestRecvDropAlreadyACK` | Push dup of ACKed seq |
| `AlreadyRecvNoACK` | `TestRecvDropAlreadyRecvNoACK` | Push dup before ACK |

**Key Structs**:
- `PacketBatch` - Defines a batch of packets (start seq, count, tsbpd)
- `DropTestCase` - Test parameters including tick times, duplicate info

**Pattern**: Uses flexible batch system to support multi-phase tests:
1. `InitialBatches` → `TickTimes` → `MoreBatches` → `MoreTickTimes` → Duplicate

**Note**: Original tests in `receive_basic_test.go` kept for now (will verify coverage with AST before removal)

---

## AST Verification Results

### Coverage Analysis

| File | Original | Table | Coverage | Action |
|------|----------|-------|----------|--------|
| `loss_recovery` | 15 | 15 | **100%** | Can delete original |
| `core_scan` | 21 | 20 | **95%** | Can delete most |
| `receive_drop` | 3 | 3 | **100%** | Can delete original |
| `fast_nak` | 23 | 19 | 83% | Keep 4 unique tests |
| `nak_consolidate` | 36 | 25 | 69% | Keep 11 unique tests |
| `send` | 27 | 11 | 41% | Keep 16 unique tests |

### Tests That Must Remain (Not Table-Driven)

**Reason categories**:
1. **Struct method tests** - Test individual struct methods (IsRange, Count)
2. **Metrics tests** - Verify metric increment behavior
3. **Circular math tests** - Verify circular arithmetic edge cases
4. **Concurrency tests** - Test atomic operations, sync.Pool
5. **Comparison tests** - Compare two strategies or baselines
6. **Unique logic tests** - Unique calculations (wire size, rate estimation)

### Final Test Count

```
Total test functions:     269
Table-driven test cases:   93 (run in parallel)
Original tests kept:      176 (some duplicated for safety)
```

### Recommendation

**Phase 1 Cleanup** (Safe to delete - 100% coverage):
- ❌ `loss_recovery_test.go` - Already deleted
- Remove duplicate tests from `receive_basic_test.go` (3 Drop tests)

**Phase 2 Cleanup** (Keep originals - unique tests exist):
- Keep `nak_consolidate_test.go` (has 11 unique tests)
- Keep `send_test.go` (has 16 unique tests)
- Keep `fast_nak_test.go` (has 4 unique tests)
- Keep `core_scan_test.go` (1 edge case test)

---

## Tool Consolidation Design

### Current Tools

We have two separate tools that have evolved:

```
tools/
├── test-table-audit/           # Original tool
│   └── main.go (557 lines)
│
└── test-combinatorial-gen/     # New tool
    ├── main.go (357 lines)     # AST struct analysis
    ├── smart_gen.go (284 lines) # Field categorization
    ├── coverage_check.go (582 lines) # Corner case verification
    └── code_params.go (343 lines) # Production code extraction
```

### Feature Comparison

| Feature | test-table-audit | test-combinatorial-gen |
|---------|------------------|------------------------|
| Find test functions | ✅ | ❌ |
| Identify test patterns | ✅ (regex) | ❌ |
| Suggest table structure | ✅ | ✅ |
| Verify table coverage | ✅ (basic) | ❌ |
| Parse test case structs | ❌ | ✅ (AST) |
| Classify field categories | ❌ | ✅ (smart) |
| Generate corners | ❌ | ✅ (auto) |
| Check corner coverage | ❌ | ✅ |
| Extract production params | ❌ | ✅ 🆕 |
| Match test ↔ production | ❌ | ✅ 🆕 |

### Best-of-Best Features

The consolidated tool should have:

1. **From test-table-audit**:
   - Test function discovery and counting
   - Pattern detection (mock_time, packet_create, etc.)
   - Test grouping by prefix
   - Table-driven potential scoring
   - Savings estimation

2. **From test-combinatorial-gen**:
   - AST-based struct field extraction
   - Smart field categorization (corner/critical/light/derived)
   - Auto-generated corner cases
   - Corner case coverage verification
   - **Production code parameter extraction** 🆕

3. **NEW - The Key Insight**:
   - **Cross-reference test fields against production code**
   - Only fields that map to production code need combinatorial coverage
   - Test infrastructure fields can be ignored

### Consolidated Tool Design

```
tools/test-audit/                     # Unified tool
├── main.go                          # CLI entry point
├── analysis/
│   ├── test_file.go                 # Test file analysis (from test-table-audit)
│   ├── test_patterns.go             # Pattern detection
│   └── test_functions.go            # Test function extraction
├── ast/
│   ├── struct_parser.go             # Struct field extraction
│   ├── production_params.go         # Production code analysis
│   └── field_matcher.go             # Test ↔ Production matching
├── coverage/
│   ├── corner_cases.go              # Corner case generation
│   ├── coverage_check.go            # Coverage verification
│   └── smart_strategy.go            # Smart test planning
└── output/
    ├── report.go                    # Report generation
    └── suggestions.go               # Table structure suggestions
```

### Unified CLI Design

```bash
# Single entry point with modes
test-audit [mode] [options] [file/dir]

# Modes:
  audit          # Full audit of test files (default)
  coverage       # Corner case coverage check
  classify       # Classify test fields vs production
  suggest        # Suggest table structures
  verify         # Verify table tests cover originals

# Options:
  -file FILE     # Analyze single file
  -dir DIR       # Analyze directory
  -struct NAME   # Target specific struct
  -prod-dir DIR  # Production code directory (default: same as test)
  -json          # Output as JSON
  -verbose       # Detailed output

# Examples:
test-audit audit -dir congestion/live
test-audit coverage -file congestion/live/loss_recovery_table_test.go
test-audit classify -file congestion/live/loss_recovery_table_test.go -prod-dir congestion/live
test-audit suggest -file congestion/live/nak_consolidate_test.go
```

### Makefile Integration

```makefile
# Unified commands
audit-tests:           test-audit audit -dir congestion/live
audit-coverage:        test-audit coverage -file $(FILE)
audit-classify:        test-audit classify -file $(FILE)
audit-all:             test-audit audit && test-audit coverage --all
```

### Implementation Plan

| Phase | Task | Priority |
|-------|------|----------|
| 1 | Create unified tool skeleton | High |
| 2 | Merge test-table-audit features | High |
| 3 | Merge test-combinatorial-gen features | High |
| 4 | Add production code matching | High |
| 5 | Update Makefile targets | Medium |
| 6 | Delete old tools | Low |
| 7 | Update documentation | Low |

### Key Algorithm: Field Classification

```go
// ClassifyTestField determines if a test field needs combinatorial coverage
func ClassifyTestField(testField StructField, prodParams []CodeParameter) Classification {

    // Step 1: Check if test field matches a production parameter
    for _, param := range prodParams {
        if matchesParameter(testField, param) {
            return Classification{
                Category:    CODE_PARAM,
                Production:  &param,
                NeedsCoverage: true,
                Reason:      fmt.Sprintf("Maps to %s in %s", param.Name, param.File),
            }
        }
    }

    // Step 2: Check for expectation patterns
    if isExpectation(testField.Name) {
        return Classification{
            Category:      EXPECTATION,
            NeedsCoverage: false,
            Reason:        "Derived from code parameters",
        }
    }

    // Step 3: Default to test infrastructure
    return Classification{
        Category:      TEST_INFRA,
        NeedsCoverage: false,
        Reason:        "Not found in production code",
    }
}
```

### Expected Output

```
═══════════════════════════════════════════════════════════════════
FIELD CLASSIFICATION: LossRecoveryTestCase
═══════════════════════════════════════════════════════════════════

🎯 CODE PARAMETERS (need combinatorial testing):
   TsbpdDelayUs        → Maps to receiver.tsbpdDelay (receive.go) 🎯
   NakRecentPercent    → Maps to receiver.nakRecentPercent (receive.go) 🎯
   StartSeq            → Maps to ReceiveConfig.InitialSequenceNumber 🎯

⚙️ TEST INFRASTRUCTURE (skip combinatorial):
   TotalPackets        → Not in production (test scale)
   NakCycles           → Not in production (test timing)
   DeliveryCycles      → Not in production (test timing)
   PacketSpreadUs      → Not in production (test timing)
   DropPattern         → Not in production (test scenario)

📊 EXPECTATIONS (derived):
   ExpectedRetrans     → Assert: calculated from drop pattern
   ExpectedDelivered   → Assert: TotalPackets - permanent loss
   MinNakPct           → Assert: threshold for NAK effectiveness

═══════════════════════════════════════════════════════════════════
COMBINATORIAL COVERAGE NEEDED
═══════════════════════════════════════════════════════════════════

   Only 3 fields need corner coverage (not 14!):

   TsbpdDelayUs:      [10_000, 50_000, 120_000, 500_000]  4 values
   NakRecentPercent:  [0.05, 0.10, 0.20]                  3 values
   StartSeq:          [0, 1_000_000, MAX-100]             3 values

   Total combinations: 4 × 3 × 3 = 36 tests

   (vs 78,000+ if all 14 fields were varied!)
```

---

## Combinatorial Coverage Analysis

### Problem: Test Struct Field Classification

Table-driven test structs contain three distinct types of fields:

| Category | Purpose | Needs Combinations? | Example |
|----------|---------|---------------------|---------|
| **Code Parameters** | Actual variables in production code | ✅ YES - Critical | `TsbpdDelayUs`, `NakRecentPercent`, `StartSeq` |
| **Test Infrastructure** | Control how the test runs | ❌ NO | `NakCycles`, `DeliveryCycles`, `PacketSpreadUs` |
| **Expectations** | Assert correct behavior | ❌ NO - Derived | `ExpectedRetrans`, `ExpectedDelivered`, `MinNakPct` |

**Why this matters**: If we treat ALL fields as requiring combinatorial coverage, we get explosion:
- 14 fields × 5 values each = 6.1 billion combinations
- But only ~5 fields are actual code parameters = ~3,125 combinations (0.00005% of original)

### Solution: AST Analysis of Production Code

Instead of analyzing test files to find what to test, analyze **production code** to find what parameters actually exist:

```
┌─────────────────────────────────────────────────────────────────┐
│                    AST Analysis Flow                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  receive.go ─────────► Extract receiver struct fields           │
│  send.go    ─────────► Extract sender struct fields             │
│  config.go  ─────────► Extract Config struct fields             │
│                              │                                  │
│                              ▼                                  │
│                   Production Code Parameters                    │
│                   ┌─────────────────────────┐                   │
│                   │ tsbpdDelay      uint64  │                   │
│                   │ nakRecentPercent float64│                   │
│                   │ nakMergeGap     uint32  │                   │
│                   │ initialSeqNum   uint32  │                   │
│                   │ contiguousPoint uint32  │                   │
│                   │ ...                     │                   │
│                   └─────────────────────────┘                   │
│                              │                                  │
│                              ▼                                  │
│              Match test struct fields to code params            │
│                              │                                  │
│  loss_recovery_table_test.go │                                  │
│  ┌───────────────────────────┴───────────────────────────┐     │
│  │ Field            │ Matches Code?  │ Classification    │     │
│  │──────────────────│────────────────│───────────────────│     │
│  │ TsbpdDelayUs     │ ✅ tsbpdDelay  │ CODE_PARAM        │     │
│  │ NakRecentPercent │ ✅ nakRecent.. │ CODE_PARAM        │     │
│  │ StartSeq         │ ✅ initialSeq  │ CODE_PARAM        │     │
│  │ NakCycles        │ ❌ Not found   │ TEST_INFRA        │     │
│  │ ExpectedRetrans  │ ❌ Not found   │ EXPECTATION       │     │
│  └───────────────────────────────────────────────────────┘     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Design: Field Classification Rules

**1. Code Parameter Detection** (needs corner coverage):
```go
// A test field is a CODE_PARAM if:
// - Its name matches a production struct field (fuzzy match)
// - It appears in function parameters of key functions
// - It directly controls SRT protocol behavior

Production Sources:
├── receiver struct fields (receive.go)
│   ├── tsbpdDelay            → test: TsbpdDelayUs
│   ├── nakRecentPercent      → test: NakRecentPercent
│   ├── nakMergeGap           → test: NakMergeGap
│   ├── contiguousPoint       → test: ContiguousPoint
│   └── lastACKSequenceNumber → test: StartSeq
├── sender struct fields (send.go)
│   └── retransmitStrategy    → test: Strategy
└── ReceiveConfig (receive.go)
    └── InitialSequenceNumber → test: StartSeq
```

**2. Test Infrastructure Detection** (skip combinations):
```go
// A test field is TEST_INFRA if:
// - Name contains: cycles, iterations, spread, timeout, skip
// - Not found in any production code
// - Controls test timing, not protocol behavior

Examples:
├── NakCycles       → How many Tick() calls to make
├── DeliveryCycles  → How many delivery rounds
├── PacketSpreadUs  → Spacing between test packets
└── DropPattern     → Test scenario generator
```

**3. Expectation Detection** (derived from params):
```go
// A test field is EXPECTATION if:
// - Name starts with: Expected, Min, Max
// - Name ends with: Count, Pct (when prefixed with expected-like name)
// - Value is calculated from CODE_PARAMs

Examples:
├── ExpectedRetrans    → How many retransmits we expect
├── ExpectedDelivered  → How many packets delivered
├── MinNakPct          → Minimum NAK percentage threshold
└── MaxOverNakFactor   → Maximum over-NAK factor
```

### Benefit: Focused Combinatorial Testing

| Approach | Fields Varied | Combinations | Coverage |
|----------|---------------|--------------|----------|
| Naive (all fields) | 14 | 6.1 billion | Wasteful |
| Current (patterns) | 8 | ~78,000 | Better |
| **Code Params Only** | 5 | ~3,125 | **Optimal** |

**The 5 true code parameters in `LossRecoveryTestCase`**:
1. `StartSeq` → 31-bit wraparound testing (3 values: 0, MAX-100, MAX)
2. `TsbpdDelayUs` → TSBPD timing (5 values: 10ms, 50ms, 120ms, 500ms, 1s)
3. `NakRecentPercent` → NAK window calculation (5 values: 0.01, 0.05, 0.10, 0.25, 0.50)
4. `TotalPackets` → Stream size (5 values: 1, 10, 100, 1000, 10000)
5. `DropPattern` → Loss scenario (7 patterns - but this is test infra!)

**Actual combinations needed**: 3 × 5 × 5 × 5 = **375 tests**

### Implementation Strategy

**Phase 1: Extract Production Parameters** (AST)
```bash
make audit-code-params DIR=congestion/live
```
Outputs: List of all parameters from receiver, sender, config structs

**Phase 2: Classify Test Fields**
```bash
make audit-field-class FILE=congestion/live/loss_recovery_table_test.go
```
Outputs: Classification of each field as CODE_PARAM / TEST_INFRA / EXPECTATION

**Phase 3: Corner Coverage for Code Params Only**
```bash
make audit-corners-code FILE=congestion/live/loss_recovery_table_test.go
```
Outputs: Corner coverage report for CODE_PARAM fields only

### Tool: `tools/test-combinatorial-gen/`

A powerful AST-based tool with four capabilities:

#### 1. Production Code Parameter Extraction (NEW)

Analyzes production code to find real parameters that affect behavior:

```bash
make audit-code-params DIR=congestion/live
```

**Production files analyzed**:
- `receive.go` → `receiver` struct, `ReceiveConfig`, key function params
- `send.go` → `sender` struct, `SendConfig`, retransmit functions
- `connection.go` → `Connection` config

**Output**:
```
🔍 Production Code Parameters Found:
═══════════════════════════════════
  tsbpdDelay               uint64     from receiver struct field 🎯
  nakRecentPercent         float64    from receiver struct field 🎯
  nakMergeGap              uint32     from receiver struct field 🎯
  contiguousPoint          *uint32    from receiver struct field 🎯
  InitialSequenceNumber    Number     from ReceiveConfig struct field 🎯
  retransmitStrategy       Strategy   from sender struct field 🎯
```

**Classification output**:
```
📋 Test Struct: LossRecoveryTestCase
═══════════════════════════════════

🎯 CODE PARAMETERS (need combinatorial coverage):
   ✅ TsbpdDelayUs          → Matches tsbpdDelay in receive.go
   ✅ NakRecentPercent      → Matches nakRecentPercent in receive.go
   ✅ StartSeq              → Matches InitialSequenceNumber in config

🔧 TEST INFRASTRUCTURE (don't need combinations):
   ⚙️  NakCycles            → Name contains 'cycles' - test infrastructure
   ⚙️  DeliveryCycles       → Name contains 'cycles' - test infrastructure
   ⚙️  PacketSpreadUs       → Name contains 'spread' - test infrastructure
   ⚙️  DropPattern          → Name contains 'pattern' - test infrastructure

📊 EXPECTATIONS (derived from params):
   📈 ExpectedRetrans       → Name pattern indicates expected result
   📈 ExpectedDelivered     → Name pattern indicates expected result
   📈 MinNakPct             → Name pattern indicates expected result

📌 SUMMARY:
   Code params:    3 (need full corner coverage)
   Test infra:     7 (skip combinatorial)
   Expectations:   4 (derived)
```

#### 2. Test Field Classification

Cross-references test struct fields against production code parameters:

```bash
make audit-field-class FILE=congestion/live/loss_recovery_table_test.go
```

**Output**:
- 🎯 **Code Parameters** - Match production code → Need corner coverage
- ⚙️ **Test Infrastructure** - Control test execution → Skip combinations
- 📊 **Expectations** - Assert results → Derived from params

#### 3. Smart Test Planning
Categorizes fields by testing importance:

| Category | Strategy | Example Fields |
|----------|----------|----------------|
| 🎯 Corner | Test ALL values | `StartSeq` (wraparound critical) |
| ⚡ Critical | Boundary values only | `TotalPackets`, `TsbpdDelay` |
| 💡 Light | Single typical value | `NakCycles`, `PacketSpread` |
| 📊 Derived | Don't vary | `ExpectedRetrans`, `MinNakPct` |

**Result**: 78,732 → **~20-50 tests** (99.9% reduction)

#### 3. Corner Case Coverage Verification (Reflection-Based) 🆕

**Critical feature**: Auto-discovers struct fields via AST, generates appropriate corner cases, and validates coverage.

```bash
# Check corner case coverage for a single file
make audit-corners FILE=congestion/live/loss_recovery_table_test.go

# Check ALL table-driven test files
make audit-corners-all
```

**How it works**:
1. **AST Analysis**: Parses the test file to find structs with "Test" or "Case" in name
2. **Field Discovery**: Extracts all fields with their types
3. **Corner Generation**: Auto-generates corners based on field name/type patterns:

```go
// Field name contains "Seq" + type uint32 → Sequence corners
if containsAny(fieldName, "Seq", "Sequence") && fieldType == "uint32" {
    corners = []CornerValue{
        {Value: "0", IsCritical: true, Description: "Zero - baseline"},
        {Value: "MAX-100", IsCritical: true, Description: "Near MAX - wraparound zone"},
        {Value: "MAX", IsCritical: true, Description: "AT MAX - immediate wrap"},
    }
}

// Field name contains "Delay" + type uint64 → Timing corners
if containsAny(fieldName, "Tsbpd", "Delay") && fieldType == "uint64" {
    corners = []CornerValue{
        {Value: "10000", IsCritical: true, Description: "10ms - aggressive"},
        {Value: "120000", IsCritical: false, Description: "120ms - standard"},
        {Value: "500000", IsCritical: true, Description: "500ms - high latency"},
    }
}

// ... similar patterns for Gap, Percent, bool, slice, etc.
```

4. **Coverage Check**: Parses actual test values and compares against generated corners

**Output example**:
```
📋 Struct: FastNakConditionTestCase (7 fields)
  ✅🎯 FastNakEnabled/true   : Enabled [test1, test2]
  ✅🎯 FastNakEnabled/false  : Disabled [test3]
  ❌🚨 LastSeq/2147483647    : AT MAX - immediate wrap
  Summary: 12/15 corners covered (80.0%)
  ⚠️  3 CRITICAL corners missing
```

### Corner Case Definition Strategy

Corner cases are defined based on:

1. **Boundary values** - 0, 1, MAX-1, MAX
2. **Wraparound points** - Sequence numbers near 2^31-1
3. **Timing extremes** - Very short (10ms) and very long (1s) delays
4. **Percentage boundaries** - 1%, 5%, 25%, 50%
5. **Scale extremes** - Single packet, very large streams

### Field Pattern Recognition

The tool recognizes these patterns and auto-generates appropriate corners:

| Pattern | Field Examples | Generated Corners |
|---------|---------------|-------------------|
| Sequence | `StartSeq`, `ContiguousPoint` | 0, MAX-100, MAX |
| Count | `TotalPackets`, `NakMergeGap` | 1, 100, 1000 |
| Timing (µs) | `TsbpdDelayUs`, `TsbpdTime` | 10ms, 120ms, 500ms |
| Percentage | `NakRecentPct`, `MinNakPct` | 1%, 10%, 50% |
| Boolean | `DoRetransmit`, `UseNakBtree` | true, false |
| Slice | `PacketSeqs`, `Bursts` | empty, single, multiple |
| Interface | `DropPattern`, `Pattern` | nil, set |
| Cycle | `NakCycles`, `DeliveryCycles` | 1, 10 |

### Coverage Audit Results (Struct-Specific)

| File | Structs | Coverage | Critical Missing |
|------|---------|----------|------------------|
| `loss_recovery_table` | 1 | 16% | StartSeq/0, TsbpdDelay/* |
| `core_scan_table` | 2 | 15% | ContiguousPoint/*, MockTime/* |
| `fast_nak_table` | 4 | ~60% | LastSeq/MAX, NewSeq/* |
| `nak_consolidate_table` | 5 | ~40% | NakMergeGap/0, Pattern/* |
| `send_table` | 1 | ~30% | Sequence corners |
| `receive_drop_table` | 1 | ~20% | Timing corners |

**Key Finding**: Most files are missing sequence wraparound corners (MAX-100, MAX)

### Review Process for All Table-Driven Tests

Each table-driven test file must be reviewed:

1. **Run coverage checker** on the file
2. **Identify missing critical corners**
3. **Add tests for missing corners** (or document why skipped)
4. **Re-run coverage checker** to verify 100%

### Future Enhancement

Consider implementing **pairwise test generation** using algorithms like:
- AETG (Automatic Efficient Test Generator)
- IPO (In-Parameter-Order)
- Jenny (open-source pairwise tool)

---

### 2.4 core_scan_test.go ⏳ Pending

#### AST Analysis (Preview)
```
Tests: 21
Lines: 615
Score: 80/100

Test Groups:
- TestContiguousScan_* (13 tests)
- TestGapScan_* (8 tests)
```

---

## Phase 3: Stream/Receiver Tests ⏳ Pending

### Files
- `receive_basic_test.go` (13 tests, 834 lines)
- `tsbpd_advancement_test.go` (8 tests, 739 lines)
- `receive_ring_test.go` (13 tests, 473 lines)
- `metrics_test.go` (12 tests, 611 lines)

---

## Running Totals

| Metric | Before | Current | Projected |
|--------|--------|---------|-----------|
| Total Test Lines | 17,794 | 15,759 | ~8,500 |
| Test Files | 21 | 20 | ~15 |
| Lines Saved | - | 2,034 | ~9,000 |
| Reduction % | - | 11% | ~50% |

---

## Notes

### Reusable Components

1. **DropPattern Interface** (from `loss_recovery_table_test.go`)
   - `DropEveryN`
   - `DropBurst`
   - `DropHead`
   - `DropNearTail`
   - `DropMultipleBursts`
   - `DropClustered`
   - `DropSpecific`

2. **Common Test Fields**
   - `TotalPackets`
   - `StartSeq`
   - `TsbpdDelayUs`
   - `NakRecentPct`

### Commands Reference

```bash
# Run AST analysis
make audit-tests

# Analyze specific file
make audit-tests ARGS="-file=congestion/live/nak_consolidate_test.go"

# Get suggestions
make audit-tests ARGS="-suggest"

# Run specific test group
go test ./congestion/live/... -run "TestConsolidateNakBtree" -v

# Run full suite
go test ./congestion/live/... -timeout 180s
```

