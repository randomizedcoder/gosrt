# Table-Driven Test Design for gosrt

## Overview

This document describes the table-driven testing approach used in `congestion/live` and analyzes its applicability across the test suite.

---

## 🔑 Critical Insight: Code Parameters vs Test Infrastructure

### The Problem

When analyzing table-driven test structs for combinatorial coverage, we initially treated **all fields** as requiring corner case testing. This led to explosion:

```
14 fields × 5 values each = 78,000+ combinations
```

### The Solution

Not all test struct fields are equal. They fall into three categories:

| Category | What It Is | Needs Corner Coverage? |
|----------|------------|------------------------|
| **Code Parameter** | Maps to actual production code variable | ✅ YES |
| **Test Infrastructure** | Controls test execution, not in production | ❌ NO |
| **Expectation** | Asserts results, derived from params | ❌ NO |

### Example: `LossRecoveryTestCase` (Verified by test-audit tool)

```go
type LossRecoveryTestCase struct {
    // 🎯 CODE PARAMETERS (3) - map to production code, need corner coverage
    StartSeq         uint32   // → ReceiveConfig.InitialSequenceNumber
    TsbpdDelayUs     uint64   // → ReceiveConfig.TsbpdDelay
    NakRecentPct     float64  // → ReceiveConfig.NakRecentPercent

    // ⚙️ TEST INFRASTRUCTURE (10) - control test execution, skip combinatorial
    Name             string       // Test identifier
    TotalPackets     int          // Test scale control
    DropPattern      DropPattern  // Test scenario generator
    NakCycles        int          // Number of NAK iterations
    DeliveryCycles   int          // Number of delivery rounds
    NakTickUs        int          // NAK tick interval
    DeliveryTickUs   int          // Delivery tick interval
    PacketSpreadUs   int          // Packet timing
    DoRetransmit     bool         // Enable retransmit simulation
    ExpectFullRecovery bool       // Test mode flag

    // 📊 EXPECTATIONS (3) - derived from params, don't vary
    MinDeliveryPct   float64      // Minimum delivery percentage
    MinNakPct        float64      // Minimum NAK percentage
    MaxOverNakFactor float64      // Maximum over-NAK factor
}
```

**Verified Classification** (via `make audit-classify FILE=congestion/live/loss_recovery_table_test.go`):

| Category | Count | Fields |
|----------|-------|--------|
| 🎯 CODE_PARAM | 3 | `StartSeq`, `TsbpdDelayUs`, `NakRecentPct` |
| ⚙️ TEST_INFRA | 10 | `Name`, `TotalPackets`, `DropPattern`, cycles, ticks, etc. |
| 📊 EXPECTATION | 3 | `MinDeliveryPct`, `MinNakPct`, `MaxOverNakFactor` |

**Corner Values Needed**:
```
StartSeq:     [0, 2147483547 (MAX-100), 2147483647 (MAX)]  → 3 values
TsbpdDelayUs: [10000 (10ms), 120000 (120ms), 500000 (500ms)] → 3 values
NakRecentPct: [0.05, 0.10, 0.25]                          → 3 values
```

**Combinations**: 3 × 3 × 3 = **27 tests** (not 78,000!)

### How to Identify Code Parameters

Use AST analysis of **production code** (not test code):

```bash
# Extract parameters from production code
make audit-code-params DIR=congestion/live

# Cross-reference test fields against production
make audit-classify FILE=congestion/live/loss_recovery_table_test.go
```

**Key production files to analyze**:
- `receive.go` → `receiver` struct fields
- `send.go` → `sender` struct fields
- `connection.go` → config structs

---

## 🔗 Critical Insight #2: Derived Parameters

### The Discovery

While adding corner tests for extreme CODE_PARAM values (e.g., `TsbpdDelayUs = 10_000` for 10ms), we discovered that **some TEST_INFRA parameters must change based on CODE_PARAM values**.

For example, with `TsbpdDelayUs = 10ms`:
- Default `NakIntervalUs = 20ms` → **NAK can never fire in time!**
- Default `AckIntervalUs = 10ms` → **ACK interval equals TSBPD!**

This breaks the test, not because the system is broken, but because the test infrastructure values are incompatible with the CODE_PARAM being tested.

### The Fourth Category: DERIVED Parameters

| Category | What It Is | Needs Corner Coverage? | How Set? |
|----------|------------|------------------------|----------|
| **Code Parameter** | Maps to production code | ✅ YES | Explicitly varied |
| **Derived Parameter** | Computed from CODE_PARAMs | ❌ NO | Auto-calculated |
| **Test Infrastructure** | Independent test control | ❌ NO | Fixed or defaults |
| **Expectation** | Asserts results | ❌ NO | Derived from scenario |

### Derived Parameter Relationships

#### From `TsbpdDelayUs` (TSBPD window - the primary timing parameter):

| Derived Parameter | Relationship | Rationale |
|-------------------|--------------|-----------|
| `AckIntervalUs` | `TsbpdDelayUs / 10` | ACK must fire multiple times within TSBPD |
| `NakIntervalUs` | `TsbpdDelayUs / 5` | NAK must fire before packet expires |
| `NakTickUs` | `TsbpdDelayUs / 50` | Fine-grained timing within window |
| `DeliveryTickUs` | `TsbpdDelayUs / 25` | Allow multiple delivery checks |
| `PacketSpreadUs` | `TsbpdDelayUs / 100` | Fit packets within TSBPD window |

**Example derivations**:
```
TsbpdDelayUs = 500_000 (500ms, default)
├── AckIntervalUs   = 50_000  (50ms)
├── NakIntervalUs   = 100_000 (100ms)
├── NakTickUs       = 10_000  (10ms)
├── DeliveryTickUs  = 20_000  (20ms)
└── PacketSpreadUs  = 5_000   (5ms)

TsbpdDelayUs = 10_000 (10ms, aggressive)
├── AckIntervalUs   = 1_000   (1ms)
├── NakIntervalUs   = 2_000   (2ms)
├── NakTickUs       = 200     (200us)
├── DeliveryTickUs  = 400     (400us)
└── PacketSpreadUs  = 100     (100us)
```

#### From `TotalPackets` (stream size):

| Derived Parameter | Relationship | Rationale |
|-------------------|--------------|-----------|
| `NakCycles` | `max(60, TotalPackets / 2)` | More packets need more cycles |
| `DeliveryCycles` | `max(20, TotalPackets / 5)` | Allow all packets to be delivered |

#### From `DropPattern` (loss pattern):

| Pattern | Effect on Derived Parameters |
|---------|------------------------------|
| Heavy loss (DropEveryN{N: 5}) | Increase `NakCycles` by 3-5x |
| Burst loss (DropBurst) | May need longer NAK window |
| Tail loss (DropNearTail) | Needs trailing packets |

#### From `NakRecentPct` (NAK recent window):

| Derived Parameter | Relationship | Rationale |
|-------------------|--------------|-----------|
| `NakCycles` | Higher `NakRecentPct` → More cycles | Larger recent window delays NAKs |

### Complete Dependency Graph

```
CODE_PARAMs (explicitly varied for corner coverage)
    │
    ├── TsbpdDelayUs
    │       │
    │       ├──► AckIntervalUs     = TsbpdDelayUs / 10
    │       ├──► NakIntervalUs     = TsbpdDelayUs / 5
    │       ├──► NakTickUs         = TsbpdDelayUs / 50
    │       ├──► DeliveryTickUs    = TsbpdDelayUs / 25
    │       └──► PacketSpreadUs    = TsbpdDelayUs / 100
    │
    ├── NakRecentPct
    │       │
    │       └──► NakCycles adjustment (+ if higher)
    │
    └── StartSeq
            │
            └──► (no derived params, but affects wraparound behavior)

TEST_INFRA (set once, independent)
    │
    ├── TotalPackets
    │       │
    │       ├──► NakCycles         = max(60, TotalPackets / 2)
    │       └──► DeliveryCycles    = max(20, TotalPackets / 5)
    │
    ├── DropPattern
    │       │
    │       └──► NakCycles multiplier (heavy loss = 3-5x)
    │
    └── Name, DoRetransmit, etc. (no derivations)

EXPECTATIONS (computed from scenario)
    │
    ├── ExpectFullRecovery  ← DoRetransmit && not extreme timing
    ├── MinDeliveryPct      ← 100% if full recovery, else from drop count
    └── MinNakPct           ← based on drop pattern visibility
```

### Implementation Strategy

Instead of manually setting derived parameters in each test case, implement a **smart defaults function**:

```go
// applyDerivedDefaults computes DERIVED parameters from CODE_PARAMs
func (tc *LossRecoveryTestCase) applyDerivedDefaults() {
    // If TSBPD is set but intervals aren't, derive them
    if tc.TsbpdDelayUs > 0 {
        if tc.AckIntervalUs == 0 {
            tc.AckIntervalUs = tc.TsbpdDelayUs / 10
        }
        if tc.NakIntervalUs == 0 {
            tc.NakIntervalUs = tc.TsbpdDelayUs / 5
        }
        if tc.NakTickUs == 0 {
            tc.NakTickUs = int(tc.TsbpdDelayUs / 50)
        }
        // ... etc
    }

    // Adjust cycles based on TotalPackets and DropPattern
    if tc.NakCycles == 0 {
        tc.NakCycles = max(60, tc.TotalPackets / 2)
        if isHeavyLoss(tc.DropPattern) {
            tc.NakCycles *= 3
        }
    }
}
```

### Benefits

1. **Test correctness**: Derived params are always compatible with CODE_PARAMs
2. **Simpler test cases**: Only specify CODE_PARAMs, derive the rest
3. **Corner coverage**: Extreme CODE_PARAM values automatically get appropriate timing
4. **Maintainability**: Change derivation logic in one place

### Updated Field Classification

| Field | Category | Corner Coverage | Derivation |
|-------|----------|-----------------|------------|
| `StartSeq` | CODE_PARAM | ✅ 0, MAX-100, MAX | - |
| `TsbpdDelayUs` | CODE_PARAM | ✅ 10ms, 120ms, 500ms | - |
| `NakRecentPct` | CODE_PARAM | ✅ 5%, 10%, 25% | - |
| `AckIntervalUs` | **DERIVED** | ❌ | = TsbpdDelayUs / 10 |
| `NakIntervalUs` | **DERIVED** | ❌ | = TsbpdDelayUs / 5 |
| `NakTickUs` | **DERIVED** | ❌ | = TsbpdDelayUs / 50 |
| `DeliveryTickUs` | **DERIVED** | ❌ | = TsbpdDelayUs / 25 |
| `PacketSpreadUs` | **DERIVED** | ❌ | = TsbpdDelayUs / 100 |
| `NakCycles` | **DERIVED** | ❌ | = f(TotalPackets, DropPattern) |
| `DeliveryCycles` | **DERIVED** | ❌ | = f(TotalPackets) |
| `TotalPackets` | TEST_INFRA | ❌ | - |
| `DropPattern` | TEST_INFRA | ❌ | - |
| `Name` | TEST_INFRA | ❌ | - |
| `DoRetransmit` | TEST_INFRA | ❌ | - |
| `MinDeliveryPct` | EXPECTATION | ❌ | - |
| `MinNakPct` | EXPECTATION | ❌ | - |

---

## 🔴 Critical Insight #3: Negative Tests for Derived Parameters

### The Principle

To **prove** that derived parameters are correct, we must also have tests that **intentionally use wrong derivations** and verify they fail. This validates that:

1. Our tests are actually sensitive to the parameters
2. The derivation formulas are necessary (not just nice-to-have)
3. The failure modes are what we expect

### Negative Test Cases for Derived Parameters

| Test Name | Intentional Misconfiguration | Expected Failure |
|-----------|------------------------------|------------------|
| `Negative_NakInterval_TooLarge` | `NakIntervalUs = 20_000` with `TsbpdDelayUs = 10_000` | NAKs never fire → 0% NAK rate |
| `Negative_AckInterval_EqualsTSBPD` | `AckIntervalUs = TsbpdDelayUs` | ACKs too slow → degraded recovery |
| `Negative_PacketSpread_ExceedsTSBPD` | `PacketSpreadUs = TsbpdDelayUs * 2` | Packets expire before delivery |
| `Negative_NakCycles_TooFew` | `NakCycles = 1` with heavy loss | Insufficient retransmit cycles |
| `Negative_DeliveryCycles_TooFew` | `DeliveryCycles = 1` | Not all packets delivered |

### Example Negative Test Structure

```go
// These tests EXPECT to fail - they validate that our derivation formulas are necessary
var NegativeDerivationTests = []LossRecoveryTestCase{
    {
        Name:               "Negative_NakInterval_TooLarge",
        TotalPackets:       50,
        StartSeq:           1,
        TsbpdDelayUs:       10_000,    // 10ms TSBPD
        NakIntervalUs:      20_000,    // 20ms NAK interval - WRONG! > TSBPD
        // Intentionally NOT using derived defaults
        DropPattern:        DropSpecific{Indices: []int{10, 20, 30, 40}},
        DoRetransmit:       true,

        // EXPECT FAILURE: NAKs cannot fire before packets expire
        ExpectFullRecovery: false,
        MinDeliveryPct:     90,        // Expect ~92% (4 lost, not recovered)
        MinNakPct:          0,         // Expect 0% NAKs (interval too large)
    },
    {
        Name:               "Negative_NakCycles_TooFew",
        TotalPackets:       200,
        StartSeq:           1,
        DropPattern:        DropEveryN{N: 5},  // Heavy 20% loss
        NakCycles:          5,         // WAY too few for 40 dropped packets
        DoRetransmit:       true,

        // EXPECT FAILURE: Not enough cycles to recover all losses
        ExpectFullRecovery: false,
        MinDeliveryPct:     50,        // Significant loss expected
    },
}

func TestLossRecovery_NegativeDerivations(t *testing.T) {
    for _, tc := range NegativeDerivationTests {
        tc := tc
        t.Run(tc.Name, func(t *testing.T) {
            t.Parallel()
            // Run test - these are EXPECTED to show degraded performance
            // The test passes if the system behaves as expected with bad config
            runLossRecoveryTableTest(t, tc)
        })
    }
}
```

### Test Matrix: Positive vs Negative

| Scenario | Derived Params | Expected Result | Purpose |
|----------|----------------|-----------------|---------|
| `Corner_TSBPD_10ms` | ✅ Correct | 100% recovery | Prove system works |
| `Negative_NakInterval_TooLarge` | ❌ Wrong | <100% recovery | Prove derivation needed |
| `Corner_TSBPD_500ms` | ✅ Correct | 100% recovery | Prove system works |
| `Negative_NakCycles_TooFew` | ❌ Wrong | <100% recovery | Prove derivation needed |

### Why This Matters

Without negative tests, we might:
- Have derivation formulas that are overly conservative
- Not realize when a parameter is actually critical
- Miss that tests are insensitive to certain parameters

**The negative tests prove that our positive tests are meaningful.**

### Implementation Checklist

- [ ] Add `NegativeDerivationTests` table to `loss_recovery_table_test.go`
- [ ] Test each derived parameter independently
- [ ] Verify each negative test fails in the expected way
- [ ] Document why each negative test exists

---

### Review Process for Existing Table-Driven Tests

⚠️ **All existing table-driven tests must be reviewed** using the unified `test-audit` tool:

| File | Status | CODE_PARAMs | Corners Needed |
|------|--------|-------------|----------------|
| `loss_recovery_table_test.go` | ✅ Analyzed | 3 (StartSeq, TsbpdDelayUs, NakRecentPct) | 27 |
| `nak_consolidate_table_test.go` | 🔍 Pending | TBD | TBD |
| `send_table_test.go` | 🔍 Pending | TBD | TBD |
| `fast_nak_table_test.go` | 🔍 Pending | TBD | TBD |
| `core_scan_table_test.go` | 🔍 Pending | TBD | TBD |
| `receive_drop_table_test.go` | 🔍 Pending | TBD | TBD |

**Review commands**:
```bash
# Step 1: Classify fields (identify CODE_PARAMs)
make audit-classify FILE=congestion/live/<file>.go

# Step 2: Check corner coverage
make audit-coverage FILE=congestion/live/<file>.go

# Step 3: Full audit of all test files
make audit
```

**Review checklist for each file**:
1. ✅ Run `make audit-classify FILE=<path>`
2. ✅ Identify CODE_PARAM fields (map to production code)
3. ✅ Verify corner values: 0, MAX-100, MAX for sequences; 10ms, 120ms, 500ms for delays
4. ✅ Add missing corner tests (typically ~27-50 combinations)
5. ✅ Re-run `make audit-coverage` to verify 100%

### Tool Consolidation

Two tools are being consolidated into a unified `tools/test-audit/`:

```
Current:                          → Unified:
├── test-table-audit/               tools/test-audit/
│   └── main.go                     ├── analysis/      (test patterns)
└── test-combinatorial-gen/         ├── ast/           (struct + prod params)
    ├── main.go                     ├── coverage/      (corner checking)
    ├── coverage_check.go           └── output/        (reports)
    ├── smart_gen.go
    └── code_params.go
```

See `table_driven_test_design_implementation.md` for detailed tool consolidation design.

---

## Table-Driven Pattern

### Core Concept

Instead of writing individual test functions with duplicated setup/teardown code:

```go
func TestFeature_CaseA(t *testing.T) {
    // 50 lines of setup
    // test logic
    // assertions
}

func TestFeature_CaseB(t *testing.T) {
    // Same 50 lines of setup (copy-pasted)
    // different test logic
    // assertions
}
```

Use a single runner with parameterized test cases:

```go
type TestCase struct {
    Name           string
    Config         ConfigParams
    Input          InputParams
    Expected       ExpectedOutput
}

var testCases = []TestCase{
    {Name: "CaseA", Config: ..., Input: ..., Expected: ...},
    {Name: "CaseB", Config: ..., Input: ..., Expected: ...},
}

func TestFeature_Table(t *testing.T) {
    for _, tc := range testCases {
        t.Run(tc.Name, func(t *testing.T) {
            runTest(t, tc)
        })
    }
}
```

### Benefits

1. **Reduced Code Duplication**: Single test runner handles all setup/teardown
2. **Easy to Add Cases**: Adding a test = adding a struct to a slice
3. **Consistent Testing**: All cases use identical methodology
4. **Self-Documenting**: Test parameters clearly show what's being tested
5. **Easier Maintenance**: Fix bugs in one place, affects all tests

### When Table-Driven Works Well

- Tests follow a common pattern (setup → action → assert)
- Multiple test cases with similar structure but different parameters
- Configuration variations (different timeouts, sizes, thresholds)
- Input variations (different packet patterns, sequences)
- Edge cases that only differ in specific values

### When Table-Driven May Not Work

- Tests with fundamentally different flows
- Tests requiring unique mocking strategies
- Tests with complex state machines
- Benchmark tests (separate framework)
- Race detection tests (special runtime flags)

---

## Loss Recovery Table-Driven Implementation

### Test Case Structure

```go
type LossRecoveryTestCase struct {
    Name         string
    TotalPackets int
    StartSeq     uint32       // For wraparound tests
    TsbpdDelayUs uint64       // TSBPD delay
    NakRecentPct float64      // NAK recent percent
    DropPattern  DropPattern  // Interface for drop logic

    // Timing configuration
    NakCycles       int // Number of NAK/retransmit cycles
    DeliveryCycles  int // Number of delivery cycles
    NakTickUs       int // Microseconds per NAK tick
    DeliveryTickUs  int // Microseconds per delivery tick
    PacketSpreadUs  int // Microseconds between packets

    // Behavior
    DoRetransmit bool // Whether to send retransmits

    // Expected outcomes
    ExpectFullRecovery bool
    MinDeliveryPct     float64
    MinNakPct          float64
    MaxOverNakFactor   float64
}
```

### Drop Pattern Interface

Abstracts packet loss scenarios:

```go
type DropPattern interface {
    ShouldDrop(i int, totalPackets int) bool
    Description() string
}
```

Implementations:
- `DropEveryN` - Periodic loss (every Nth packet)
- `DropBurst` - Contiguous range
- `DropHead` - First N packets
- `DropNearTail` - Near end with trailing data
- `DropMultipleBursts` - Multiple burst ranges
- `DropClustered` - Small groups
- `DropSpecific` - Explicit indices

### Key Timing Insights

1. **Trailing Packets**: Tests using periodic loss need extra packets after the last dropped one to trigger NAK detection
2. **Packet Spread**: Tighter spread = more packets fit in NAK window
3. **Retransmit Processing**: Extra tick after retransmit ensures processing before TSBPD advances
4. **NAK Window Calculation**: `nakStartTime = firstTsbpd - (tsbpdDelay * (1 - nakRecentPercent))`

---

## Test File Analysis

### Completed: Loss Recovery (76% reduction)

| Metric | Before | After |
|--------|--------|-------|
| loss_recovery_test.go | 2,676 lines | Deleted |
| loss_recovery_table_test.go | N/A | 641 lines |
| **Net Savings** | | **2,034 lines (76%)** |
| Test Cases | 15 functions | 15 table entries |
| Coverage | Same scenarios | Same scenarios |

### Summary Matrix - Remaining Files

| File | Lines | Funcs | Table-Driven? | Shared Patterns | Effort | Priority |
|------|-------|-------|---------------|-----------------|--------|----------|
| nak_btree_scan_stream_test.go | 1922 | ~30 | ✅ High | Stream profiles, loss | Medium | High |
| eventloop_test.go | 1976 | ~35 | ⚠️ Partial | Time config, assertions | High | Medium |
| nak_consolidate_test.go | 1447 | 36 | ✅ High | NAK ranges, gaps | Low | **High** |
| nak_large_merge_ack_test.go | 1275 | ~20 | ✅ High | Stream profiles | Medium | High |
| send_test.go | 1104 | ~25 | ⚠️ Medium | Packet configs | Medium | Medium |
| receive_race_test.go | 1006 | ~15 | ❌ Skip | Special runtime | - | Skip |
| stream_test_helpers_test.go | 960 | N/A | N/A | Infrastructure | - | Skip |
| receive_iouring_reorder_test.go | 941 | ~15 | ⚠️ Medium | Reorder patterns | Medium | Medium |
| receive_basic_test.go | 833 | ~20 | ⚠️ Medium | Basic configs | Medium | Medium |
| tsbpd_advancement_test.go | 738 | ~8 | ⚠️ Medium | Time, gaps | Medium | Medium |
| receive_bench_test.go | 723 | ~10 | ❌ Skip | Benchmark framework | - | Skip |
| fast_nak_test.go | 663 | 23 | ✅ High | Loss patterns | Low | **High** |
| core_scan_test.go | 614 | ~10 | ⚠️ Medium | Scan configs | Medium | Medium |
| metrics_test.go | 610 | ~15 | ⚠️ Medium | Metric assertions | Low | Low |
| packet_store_test.go | 579 | ~12 | ⚠️ Medium | Store operations | Medium | Low |
| receive_ring_test.go | 472 | ~10 | ⚠️ Medium | Ring configs | Medium | Low |
| nak_btree_test.go | 386 | 9 | ✅ High | Range operations | Low | Medium |
| too_recent_threshold_test.go | 236 | 1 | ✅ Done | Already table-driven | - | Done |
| receive_config_test.go | 211 | ~8 | ⚠️ Low | Config variations | Low | Low |
| stream_matrix_test.go | 87 | 4 | ✅ Done | Uses matrix framework | - | Done |
| loss_recovery_table_test.go | 641 | 1 | ✅ Done | Table-driven | - | **Done** |

---

## Detailed Analysis by File

### High Priority - Clear Candidates

#### 1. `nak_btree_scan_stream_test.go` (1922 lines)

**Current Structure**: Multiple `TestNakBtree_*` functions with realistic stream simulations.

**Common Patterns**:
- Stream creation with specific loss patterns
- Mock time management
- NAK/ACK verification
- Sequence wraparound handling

**Proposed Table Structure**:
```go
type NakBtreeStreamTestCase struct {
    Name            string
    TotalPackets    int
    PacketInterval  time.Duration
    LossPattern     DropPattern      // Reuse from loss_recovery
    ReorderPercent  float64
    TsbpdDelay      uint64

    // NAK expectations
    ExpectedNakCount    int
    ExpectedGapMerges   int
    MaxNakLatency       time.Duration
}
```

**Effort**: Medium - Many tests follow similar patterns
**Benefit**: High - 1922 lines could reduce to ~500

#### 2. `nak_consolidate_test.go` (1447 lines)

**Current Structure**: Tests NAK range consolidation with various gap patterns.

**Common Patterns**:
- Gap definitions (start, end)
- NAK generation
- Consolidation verification

**Proposed Table Structure**:
```go
type NakConsolidateTestCase struct {
    Name     string
    Gaps     []Gap  // {Start, End} pairs
    Expected []NakRange

    // Configuration
    MaxNakSize    int
    ConsolidateMs int
}
```

**Effort**: Low - Very repetitive patterns
**Benefit**: High - Could reduce to ~300 lines

#### 3. `nak_large_merge_ack_test.go` (1275 lines)

**Current Structure**: Large-scale stream tests with ACK merging.

**Common Patterns**:
- Large packet counts
- ACK sequence tracking
- Merge gap verification

**Proposed Table Structure**:
```go
type LargeMergeTestCase struct {
    Name          string
    StreamSize    int
    LossPattern   DropPattern
    AckInterval   time.Duration

    ExpectedAcks  Range  // min/max
    ExpectedMerge int
}
```

**Effort**: Medium
**Benefit**: High

#### 4. `fast_nak_test.go` (663 lines)

**Current Structure**: Tests fast NAK detection timing.

**Common Patterns**:
- Packet timing
- NAK trigger conditions
- Timing thresholds

**Proposed Table Structure**:
```go
type FastNakTestCase struct {
    Name           string
    PacketArrival  []int64  // Arrival times in microseconds
    MissingSeq     []uint32
    ExpectFastNak  bool
    MaxNakDelay    int64
}
```

**Effort**: Low - Clear patterns
**Benefit**: Medium

#### 5. `nak_btree_test.go` (386 lines)

**Current Structure**: Tests NAK btree data structure operations.

**Common Patterns**:
- Insert/delete operations
- Range queries
- Iterator behavior

**Proposed Table Structure**:
```go
type NakBtreeOpTestCase struct {
    Name       string
    Operations []BtreeOp  // {Insert|Delete|Query, seq, expected}
    FinalState []uint32   // Expected sequences in tree
}
```

**Effort**: Low
**Benefit**: Medium

### Medium Priority - Partial Candidates

#### 6. `eventloop_test.go` (1976 lines)

**Analysis**: Mixed - some tests are highly specialized (io_uring simulation), others could be table-driven.

**Tableable Tests**:
- Basic EventLoop functionality tests
- Time base tests
- NAK generation tests

**Not Tableable**:
- io_uring simulation tests (unique setup)
- Race-related EventLoop tests

**Effort**: High - Would need to keep some individual tests
**Benefit**: Medium

#### 7. `receive_basic_test.go` (833 lines)

**Analysis**: Many basic receiver operation tests.

**Common Patterns**:
- Packet creation
- Push operations
- State verification

**Proposed Table Structure**:
```go
type ReceiverOpTestCase struct {
    Name       string
    StartSeq   uint32
    Operations []RecvOp  // {Push|Tick|Flush, packet/time}
    Expected   RecvState // contiguousPoint, delivered, etc.
}
```

**Effort**: Medium
**Benefit**: Medium

#### 8. `tsbpd_advancement_test.go` (738 lines)

**Analysis**: Could share infrastructure with loss_recovery table tests.

**Common Patterns**:
- Gap creation
- Time advancement
- ContiguousPoint verification

**Could Potentially Merge**: With loss_recovery table tests using different assertion modes.

**Effort**: Medium
**Benefit**: Medium

#### 9. `receive_iouring_reorder_test.go` (941 lines)

**Analysis**: Tests io_uring packet reordering scenarios.

**Common Patterns**:
- Reorder patterns
- Delivery order verification

**Proposed Table Structure**:
```go
type ReorderTestCase struct {
    Name           string
    ArrivalOrder   []int  // Indices in arrival order
    ExpectedDeliver []int // Indices in expected delivery order
    ReorderWindow  int
}
```

**Effort**: Medium
**Benefit**: Medium

### Low Priority / Skip

#### 10. `receive_race_test.go` (1006 lines)
**Skip Reason**: Race tests require special runtime (`-race` flag) and unique goroutine patterns.

#### 11. `receive_bench_test.go` (723 lines)
**Skip Reason**: Uses Go's benchmark framework which has its own pattern.

#### 12. `stream_test_helpers_test.go` (960 lines)
**Skip Reason**: This IS the shared infrastructure. Could be enhanced but not table-driven.

#### 13. `send_test.go` (1104 lines)
**Analysis**: Sender tests have different patterns than receiver tests.
**Recommendation**: Analyze separately for sender-specific table structure.

---

## Shared Infrastructure Proposal

### Common Test Case Fields

Many tests share these concepts:

```go
// Base test configuration - could be embedded in all test cases
type BaseTestConfig struct {
    TotalPackets   int
    StartSeq       uint32
    TsbpdDelayUs   uint64
    NakRecentPct   float64
    PacketSpreadUs int
}

// Timing configuration - shared across NAK/recovery/advancement tests
type TimingConfig struct {
    NakCycles      int
    DeliveryCycles int
    NakTickUs      int
    DeliveryTickUs int
}

// Common assertions
type Assertions struct {
    MinDelivered   int
    MaxDelivered   int
    MinNaks        int
    MaxNaks        int
    MinAcks        int
    MaxAcks        int
}
```

### Unified DropPattern Interface

The `DropPattern` interface from loss_recovery can be reused across:
- `loss_recovery_table_test.go` ✅
- `nak_btree_scan_stream_test.go`
- `nak_large_merge_ack_test.go`
- `fast_nak_test.go`
- `tsbpd_advancement_test.go`

### Unified Stream Generator

```go
type StreamGenerator struct {
    Config      BaseTestConfig
    DropPattern DropPattern
    Reorder     ReorderPattern
}

func (g *StreamGenerator) Generate() (delivered []packet.Packet, dropped []packet.Packet)
```

---

## Implementation Roadmap

### AST-Assisted Conversion Tool

A tool has been created to analyze test files and assist with safe conversion:

```bash
# Run full analysis
make audit-tests

# Get table structure suggestions
make audit-tests ARGS="-suggest"

# Analyze specific file
make audit-tests ARGS="-file=congestion/live/send_test.go"

# Get detailed function analysis
make audit-tests ARGS="-details"

# Verify coverage after conversion
make audit-tests ARGS="-verify"
```

The tool (`tools/test-table-audit/main.go`) provides:
- **Pattern Detection**: Identifies common setup, assertion, and timing patterns
- **Score Calculation**: Ranks files by table-driven conversion potential
- **Name Grouping**: Groups tests by prefix to suggest table structures
- **Struct Suggestions**: Proposes test case struct fields based on patterns
- **Coverage Verification**: Ensures table tests cover all original test cases

### Safe Conversion Process

For each file being converted:

1. **Analyze**: `make audit-tests ARGS="-file=path/to/test.go -details"`
2. **Document**: Record all test functions and their purposes
3. **Create**: Build table structure based on suggestions
4. **Implement**: Convert tests one group at a time
5. **Verify**: Run both old and new tests to ensure equivalence
6. **Delete**: Remove old tests only after verification passes

### Phase 1: ✅ Complete
- `loss_recovery_test.go` → `loss_recovery_table_test.go`
- **Result**: 2,676 → 641 lines (76% reduction)

### Phase 2: High-Impact Consolidation (Recommended Priority)

Based on AST analysis (from `make audit-tests`):

| File | Tests | Lines | Score | Est. Savings |
|------|-------|-------|-------|--------------|
| `nak_consolidate_test.go` | 36 | 1,448 | HIGH | ~1,000 lines |
| `send_test.go` | 27 | 1,105 | 95/100 | ~734 lines |
| `fast_nak_test.go` | 23 | 664 | 70/100 | ~325 lines |
| `core_scan_test.go` | 21 | 615 | 80/100 | ~344 lines |

Key test groups identified:
- `TestConsolidateNakBtree_*` (27 tests) - perfect table candidate
- `TestSendOriginal_*` (11 tests) + `TestSendHonorOrder_*` (11 tests)
- `TestCheckFastNakRecent_*` (13 tests)
- `TestContiguousScan_*` (13 tests) + `TestGapScan_*` (8 tests)

### Phase 3: Stream/Receiver Tests

| File | Tests | Lines | Score | Est. Savings |
|------|-------|-------|-------|--------------|
| `receive_basic_test.go` | 13 | 834 | 115/100 | ~583 lines |
| `tsbpd_advancement_test.go` | 8 | 739 | 100/100 | ~517 lines |
| `receive_ring_test.go` | 13 | 473 | 105/100 | ~331 lines |
| `metrics_test.go` | 12 | 611 | 110/100 | ~427 lines |

### Phase 4: Complex Files (Lower Priority)

| File | Tests | Lines | Notes |
|------|-------|-------|-------|
| `eventloop_test.go` | 20 | 1,977 | Mixed - some io_uring tests unique |
| `nak_btree_scan_stream_test.go` | 16 | 1,923 | Can reuse DropPattern |
| `receive_iouring_reorder_test.go` | 8 | 942 | Reorder patterns unique |

### Files to Skip

- `receive_race_test.go` - Special `-race` runtime
- `receive_bench_test.go` - Go benchmark framework
- `stream_test_helpers_test.go` - Shared infrastructure

### Expected Results (Validated by Loss Recovery Conversion)

| Metric | Before | After | Validated |
|--------|--------|-------|-----------|
| Loss recovery tests | 2,676 lines | 641 lines | ✅ **76% reduction** |
| Total test lines (proj) | ~17,000 | ~8,500 | Estimated ~50% |
| Test files | 21 | 15-18 | TBD |
| Shared infrastructure | Minimal | Comprehensive | ✅ DropPattern interface |
| Time to add new test case | 30-50 lines | 5-10 lines (struct) | ✅ |

### Top Candidates for Immediate Conversion

Based on function count and pattern repetition:

1. **nak_consolidate_test.go** (1,447 lines, 36 functions)
   - Many `TestConsolidateNakBtree_ModulusDrops_*` variants
   - Many `TestConsolidateNakBtree_MSS_*` variants
   - Expected reduction: **~70% (~1,000 lines saved)**

2. **fast_nak_test.go** (663 lines, 23 functions)
   - `TestCheckFastNak_*` variants
   - `TestCheckFastNakRecent_*` variants
   - Expected reduction: **~60% (~400 lines saved)**

3. **nak_btree_scan_stream_test.go** (1,922 lines, ~30 functions)
   - `TestNakBtree_RealisticStream_*` variants
   - Can reuse `DropPattern` from loss_recovery
   - Expected reduction: **~60% (~1,150 lines saved)**

**Projected Total Savings**: ~4,500-5,000 lines from top 4 conversions

---

## Conclusion

The table-driven approach is highly applicable to gosrt's test suite, particularly for:
- NAK-related tests (5 files, ~5,000 lines)
- Stream simulation tests (3 files, ~4,000 lines)
- Receiver operation tests (3 files, ~2,500 lines)

Conservative estimate: **50%+ code reduction** while maintaining or improving coverage.

---

## ⚠️ IMPORTANT: Review Checklist

Before considering the table-driven conversion complete:

- [x] **Unified tool implemented** (`tools/test-audit/`) ✅
- [x] **Production code analysis** - extract CODE_PARAMs from `config.go`, `receive.go`, `send.go` ✅
- [ ] **All table-driven tests reviewed** using `make audit-classify`
  - [x] `loss_recovery_table_test.go` - 3 CODE_PARAMs, 27 combinations
  - [ ] `nak_consolidate_table_test.go`
  - [ ] `send_table_test.go`
  - [ ] `fast_nak_table_test.go`
  - [ ] `core_scan_table_test.go`
  - [ ] `receive_drop_table_test.go`
- [ ] **Corner coverage verified** for CODE_PARAM fields only
- [ ] **Missing corners added** (especially 31-bit wraparound: StartSeq=MAX-100, MAX)
- [ ] **Intentional skips documented** (if any corner is skipped, explain why)
- [ ] **Delete redundant individual test files**

**Tool Commands**:
```bash
make audit                    # Full audit of all test files
make audit-classify FILE=...  # Classify fields as CODE_PARAM/TEST_INFRA/EXPECTATION
make audit-coverage FILE=...  # Check corner case coverage
make audit-suggest FILE=...   # Get table structure suggestions for conversion
```

**Key Reference**: `table_driven_test_design_implementation.md` contains:
- Tool consolidation design
- Field classification algorithm
- Per-file review status

