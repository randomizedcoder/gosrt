# Receive.go Comprehensive Testing Strategy

> **Related Documents:**
> - [Fork Comprehensive Plan](fork_comprehensive_plan.md) - Parent planning document, high-risk area identification
> - [Design NAK Btree](design_nak_btree.md) - NAK btree implementation details
> - [Contiguous Point TSBPD Advancement](contiguous_point_tsbpd_advancement_design.md) - TSBPD scanning logic
> - [Table-Driven Test Design](table_driven_test_design.md) - Test methodology

---

## 1. Executive Summary

`receive.go` is the **most critical file** in the gosrt codebase:
- **2758 lines** of code
- **408 branches** (if/switch/case/for statements)
- **42+ functions**
- **30+ configuration parameters**
- **73.6% coverage** (current), with critical paths at <60%

This document proposes a strategy to:
1. **Split `receive.go`** into logical, testable units
2. **Create comprehensive table-driven tests** with AST/reflection-based generation
3. **Achieve 90%+ coverage** on critical paths

---

## 2. Current State Analysis

### 2.1 Function Inventory (42 functions)

| Category | Functions | Lines | Coverage Range |
|----------|-----------|-------|----------------|
| **Initialization** | `NewReceiver`, `parseRetryStrategy` | ~310 | 28-94% |
| **Push Path** | `Push`, `pushWithLock`, `pushLocked`, `pushLockedNakBtree`, `pushLockedOriginal`, `pushToRing` | ~300 | 57-86% |
| **Scanning** | `contiguousScan`, `contiguousScanWithTime`, `gapScan`, `tooRecentThreshold` | ~200 | 94-96% |
| **ACK Handling** | `periodicACK`, `periodicACKLocked`, `periodicACKWriteLocked` | ~290 | 0-98% |
| **NAK Handling** | `periodicNAK`, `periodicNakOriginal`, `periodicNakOriginalLocked`, `periodicNakBtree`, `periodicNakBtreeLocked`, `expireNakEntries` | ~500 | 0-85% |
| **Ring Buffer** | `drainPacketRing`, `drainAllFromRing`, `drainRingByDelta` | ~200 | 0-82% |
| **Tick/EventLoop** | `Tick`, `EventLoop`, `processOnePacket` | ~350 | 84-92% |
| **Delivery** | `deliverReadyPackets*`, `updateRateStats` | ~150 | 0-95% |
| **Utility** | `Stats`, `PacketRate`, `Flush`, `String`, `releasePacketFully` | ~100 | 0-100% |

### 2.2 Configuration Complexity

`ReceiveConfig` has **30+ parameters** across 7 feature groups:

```
┌────────────────────────────────────────────────────────────────┐
│                     ReceiveConfig (30+ params)                 │
├────────────────────────────────────────────────────────────────┤
│ CORE (5)           │ NAK_BTREE (6)      │ FAST_NAK (3)        │
│ • InitialSeqNum    │ • UseNakBtree      │ • FastNakEnabled    │
│ • PeriodicACKInt   │ • SuppressImmedNak │ • FastNakThresholdUs│
│ • PeriodicNAKInt   │ • TsbpdDelay       │ • FastNakRecentEnabled│
│ • OnSendACK/NAK    │ • NakRecentPercent │                     │
│ • OnDeliver        │ • NakMergeGap      │                     │
│                    │ • NakConsolBudget  │                     │
├────────────────────┼────────────────────┼─────────────────────┤
│ RING_BUFFER (7)    │ EVENT_LOOP (5)     │ TIME_BASE (2)       │
│ • UsePacketRing    │ • UseEventLoop     │ • TsbpdTimeBase     │
│ • PacketRingSize   │ • EventLoopRateInt │ • StartTime         │
│ • PacketRingShards │ • BackoffColdStart │                     │
│ • PacketRingMax*   │ • BackoffMin/Max   │ LIGHT_ACK (1)       │
│ • RetryStrategy    │                    │ • LightACKDifference│
└────────────────────┴────────────────────┴─────────────────────┘
```

### 2.3 Critical Low-Coverage Functions

| Function | Coverage | Risk Level | Reason |
|----------|----------|------------|--------|
| `parseRetryStrategy` | 28.6% | HIGH | Config parsing, affects ring behavior |
| `periodicNakOriginal` | 50.0% | HIGH | Legacy NAK path, still used |
| `getSleepDuration` | 54.5% | MEDIUM | Timing-critical for EventLoop |
| `deliverReadyPacketsLocked` | 55.6% | HIGH | Packet ordering/delivery |
| `pushWithLock` | 57.1% | HIGH | Main receive path |
| `drainPacketRing` | 68.8% | MEDIUM | Ring buffer handling |
| `periodicNakBtree` | 85.7% | MEDIUM | Modern NAK path |

---

## 3. Splitting Strategy Options

### 3.1 Option A: Split by Feature/Phase

Split based on the implementation phases documented in the codebase:

```
receive.go (2758 lines)
    │
    ├── receive_core.go (~400 lines)
    │   • receiver struct definition
    │   • NewReceiver
    │   • Stats, Flush, utility methods
    │
    ├── receive_push.go (~400 lines)
    │   • Push, pushWithLock, pushLocked
    │   • pushLockedNakBtree, pushLockedOriginal
    │   • pushToRing
    │
    ├── receive_scan.go (~350 lines)
    │   • contiguousScan, contiguousScanWithTime
    │   • gapScan
    │   • tooRecentThreshold, CalcTooRecentThreshold
    │
    ├── receive_ack.go (~350 lines)
    │   • periodicACK, periodicACKLocked
    │   • periodicACKWriteLocked
    │
    ├── receive_nak.go (~550 lines)
    │   • periodicNAK, periodicNakOriginal
    │   • periodicNakBtree, periodicNakBtreeLocked
    │   • expireNakEntries
    │
    ├── receive_ring.go (~250 lines)
    │   • drainPacketRing, drainAllFromRing
    │   • drainRingByDelta
    │   • adaptiveBackoff
    │
    └── receive_eventloop.go (~400 lines)
        • Tick, EventLoop
        • processOnePacket
        • deliverReadyPackets*
        • updateRateStats
```

**Pros:**
- Clear separation by functionality
- Each file ~300-550 lines (manageable)
- Matches existing test file organization

**Cons:**
- Some cross-cutting concerns (e.g., locking)
- May need shared helpers file

### 3.2 Option B: Split by Data Flow

Split based on packet flow through the receiver:

```
receive.go
    │
    ├── receive_ingest.go (~500 lines)
    │   • Push path (all push* functions)
    │   • Ring buffer insertion
    │
    ├── receive_process.go (~700 lines)
    │   • Tick/EventLoop
    │   • drainPacketRing
    │   • processOnePacket
    │
    ├── receive_control.go (~700 lines)
    │   • ACK generation
    │   • NAK generation
    │   • Scanning functions
    │
    └── receive_deliver.go (~400 lines)
        • deliverReadyPackets*
        • Stats, metrics
        • Utility
```

**Pros:**
- Follows packet lifecycle
- Natural for integration testing

**Cons:**
- Files still large (700 lines)
- ACK/NAK logic mixed with scanning

### 3.3 Option C: Split by Locking Domain

Split based on lock ownership:

```
receive.go
    │
    ├── receive_lockfree.go (~600 lines)
    │   • Ring buffer operations
    │   • Atomic operations
    │   • Lock-free push path
    │
    ├── receive_locked.go (~1000 lines)
    │   • All *Locked functions
    │   • Btree operations
    │   • Delivery
    │
    └── receive_public.go (~500 lines)
        • Public API (Push, Tick, EventLoop)
        • Stats, Flush
        • Configuration
```

**Pros:**
- Clear concurrency boundaries
- Race condition testing easier

**Cons:**
- Doesn't match functional domains
- receive_locked.go still large

### 3.4 Option D: Hybrid (Same Package)

Combine feature-based split with extracted shared utilities, all in `congestion/live/`:

```
congestion/live/
    │
    ├── receive.go (~500 lines) - KEEP
    │   • receiver struct
    │   • NewReceiver
    │   • Public API: Push, Tick, EventLoop
    │   • Stats, Flush, utility
    │
    ├── receive_push.go (~350 lines) - NEW
    ├── receive_scan.go (~300 lines) - NEW
    ├── receive_ack.go (~300 lines) - NEW
    ├── receive_nak.go (~500 lines) - NEW
    ├── receive_ring.go (~300 lines) - NEW
    └── receive_delivery.go (~350 lines) - NEW
```

**Pros:**
- Balanced file sizes (~300-500 lines each)
- No import changes needed
- Minimal refactoring

**Cons:**
- All functions share `live` package namespace (pollution)
- No encapsulation of internal helpers
- Test files also in same package

---

### 3.5 Option E: New Subpackage (Recommended) ⭐

Create a dedicated `receive` subpackage with clean separation:

```
congestion/live/
    │
    ├── receive.go              - DELETE (move to subpackage)
    │
    └── receive/                - NEW SUBPACKAGE
        │
        ├── receiver.go (~400 lines) - CORE
        │   • Receiver struct definition
        │   • Config struct
        │   • New() constructor
        │   • Public API: Push, Tick, EventLoop
        │
        ├── push.go (~350 lines)
        │   • pushWithLock, pushLocked
        │   • pushLockedNakBtree
        │   • pushLockedOriginal
        │   • pushToRing
        │
        ├── scan.go (~300 lines)
        │   • contiguousScan, contiguousScanWithTime
        │   • gapScan
        │   • CalcTooRecentThreshold
        │
        ├── ack.go (~300 lines)
        │   • periodicACK (all variants)
        │   • Light ACK logic
        │
        ├── nak.go (~500 lines)
        │   • periodicNAK (all variants)
        │   • NAK btree operations
        │   • expireNakEntries
        │
        ├── ring.go (~300 lines)
        │   • Ring buffer drain functions
        │   • adaptiveBackoff (internal)
        │   • parseRetryStrategy (internal)
        │
        ├── delivery.go (~350 lines)
        │   • deliverReadyPackets (all variants)
        │   • processOnePacket
        │   • updateRateStats
        │
        └── internal.go (~100 lines)
            • Shared internal helpers
            • Lock wrappers
            • Time utilities
```

**Package Structure:**

```go
// congestion/live/receive/receiver.go
package receive

type Config struct {
    InitialSequenceNumber  circular.Number
    PeriodicACKInterval    uint64
    // ... all config fields
}

type Receiver struct {
    config          Config
    contiguousPoint atomic.Uint32
    // ... all receiver fields
}

func New(cfg Config) *Receiver { ... }

func (r *Receiver) Push(pkt packet.Packet) { ... }
func (r *Receiver) Tick(now uint64) { ... }
func (r *Receiver) EventLoop(ctx context.Context) { ... }
```

**Migration Impact Analysis:**

**Production code** - only **2 files** need updating:

| File | Change |
|------|--------|
| `connection.go` | `live.NewReceiver(live.ReceiveConfig{...})` → `receive.New(receive.Config{...})` |
| `congestion/live/fake.go` | `ReceiveConfig` → `receive.Config` |

**Test files** - 16 files in `congestion/live/` reference `ReceiveConfig`:
```
core_scan_table_test.go      receive_config_test.go     receive_ring_test.go
receive_drop_table_test.go   tsbpd_advancement_test.go  receive_bench_test.go
receive_iouring_reorder_test.go  receive_race_test.go   metrics_test.go
stream_test_helpers_test.go  receive_basic_test.go      eventloop_test.go
loss_recovery_table_test.go  nak_btree_scan_stream_test.go  nak_large_merge_ack_test.go
core_scan_test.go
```

**Test file strategy options:**
1. **Move to subpackage** - Tests move to `receive/` (access internal functions)
2. **Stay in `live/`** - Import `receive` package (only test public API)

**Recommended:** Move receive-related tests to `receive/` subpackage for:
- Access to internal functions for thorough testing
- Clean separation of concerns
- Natural co-location with source files

**sed commands for migration:**
```bash
# Update connection.go import
sed -i 's|"github.com/randomizedcoder/gosrt/congestion/live"|"github.com/randomizedcoder/gosrt/congestion/live"\n\t"github.com/randomizedcoder/gosrt/congestion/live/receive"|' connection.go

# Update connection.go usage
sed -i 's/live\.NewReceiver(live\.ReceiveConfig/receive.New(receive.Config/g' connection.go

# Update fake.go
sed -i 's/ReceiveConfig/receive.Config/g' congestion/live/fake.go
```

**Pros:**
- ✅ **Clean namespace**: `receive.Receiver`, `receive.Config`
- ✅ **Encapsulation**: Internal helpers not exported
- ✅ **Testable**: Each file gets `*_test.go` in same package
- ✅ **Go idiom**: Standard subpackage pattern
- ✅ **Natural test organization**: `receive/nak_test.go`, `receive/push_test.go`
- ✅ **Internal functions**: Can use lowercase for truly internal helpers
- ✅ **Minimal impact**: Only 2 files need import updates!
- ✅ **No facade complexity**: Direct usage, no indirection

**Cons:**
- ⚠️ Breaking change (but this is a fork, so acceptable)
- ⚠️ Test files need updating too

**Import Changes Required:**

```go
// Before
import "github.com/randomizedcoder/gosrt/congestion/live"
recv := live.NewReceiver(live.ReceiveConfig{...})

// After
import "github.com/randomizedcoder/gosrt/congestion/live/receive"
recv := receive.New(receive.Config{...})
```

---

## 4. Recommended Approach: Option E (New Subpackage) ⭐

### 4.1 Rationale

1. **Go Idiom**: Subpackages are the standard way to organize complex modules
2. **Encapsulation**: Internal helpers (`adaptiveBackoff`, `parseRetryStrategy`) stay internal
3. **Testability**: Each file gets its own `*_test.go` with package-level access
4. **Namespace**: Clean API - `receive.New()`, `receive.Config{}` vs `live.NewReceiver()`
5. **Backward Compatible**: Facade in `live/receive.go` preserves existing imports
6. **Future Proof**: Can add more internal helpers without API pollution

### 4.2 Subpackage File Mapping

| Phase | New File | Priority | Functions | Current Coverage |
|-------|----------|----------|-----------|------------------|
| 0 | `receive/receiver.go` | SETUP | Receiver struct, New(), Config | - |
| 1 | `receive/nak.go` | CRITICAL | periodicNak*, expire* | 0-85% |
| 2 | `receive/push.go` | CRITICAL | push* | 57-86% |
| 3 | `receive/ring.go` | HIGH | drain*, backoff, parse* | 0-82% |
| 4 | `receive/scan.go` | MEDIUM | *Scan, tooRecent* | 94-96% |
| 5 | `receive/ack.go` | MEDIUM | periodicACK* | 0-98% |
| 6 | `receive/delivery.go` | HIGH | deliver*, process*, update* | 0-95% |
| 7 | DELETE `live/receive.go` | CLEANUP | Remove old file | - |

### 4.3 Test File Organization

Each source file gets a corresponding test file in the same package:

```
congestion/live/receive/
    ├── receiver.go           → receiver_test.go
    ├── push.go               → push_test.go
    ├── push_table_test.go    (table-driven)
    ├── scan.go               → scan_test.go
    ├── scan_table_test.go    (table-driven)
    ├── ack.go                → ack_test.go
    ├── ack_table_test.go     (table-driven)
    ├── nak.go                → nak_test.go
    ├── nak_table_test.go     (table-driven)
    ├── ring.go               → ring_test.go
    ├── ring_table_test.go    (table-driven)
    ├── delivery.go           → delivery_test.go
    └── delivery_table_test.go (table-driven)
```

**Benefits of package-level tests:**
- Access to internal (lowercase) functions for thorough testing
- No need for `_test` package suffix workarounds
- Test helpers can be shared within package

### 4.4 Dependency Analysis

```
┌─────────────────────────────────────────────────────────────────┐
│                     congestion/live/                            │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │  receive.go (FACADE)                                       │ │
│  │  • type Receiver = receive.Receiver                        │ │
│  │  • var NewReceiver = receive.New                           │ │
│  └─────────────────────────┬──────────────────────────────────┘ │
│                            │                                    │
│                            ▼                                    │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │               congestion/live/receive/                     │ │
│  │  ┌──────────────────────────────────────────────────────┐  │ │
│  │  │  receiver.go (CORE)                                  │  │ │
│  │  │  • type Receiver struct { ... }                      │  │ │
│  │  │  • type Config struct { ... }                        │  │ │
│  │  │  • func New(Config) *Receiver                        │  │ │
│  │  │  • func (r *Receiver) Push/Tick/EventLoop            │  │ │
│  │  └───────────────────────┬──────────────────────────────┘  │ │
│  │                          │                                  │ │
│  │      ┌───────────────────┼───────────────────┐              │ │
│  │      │                   │                   │              │ │
│  │      ▼                   ▼                   ▼              │ │
│  │ ┌─────────┐       ┌──────────┐       ┌──────────┐          │ │
│  │ │ push.go │──────▶│ scan.go  │──────▶│ ack.go   │          │ │
│  │ └─────────┘       └──────────┘       └──────────┘          │ │
│  │      │                   │                   │              │ │
│  │      │                   ▼                   │              │ │
│  │      │            ┌──────────┐               │              │ │
│  │      └───────────▶│ nak.go   │◀──────────────┘              │ │
│  │                   └──────────┘                              │ │
│  │                         │                                   │ │
│  │      ┌──────────────────┼──────────────────┐                │ │
│  │      │                  │                  │                │ │
│  │      ▼                  ▼                  ▼                │ │
│  │ ┌─────────┐      ┌───────────┐      ┌───────────┐          │ │
│  │ │ ring.go │─────▶│delivery.go│      │internal.go│          │ │
│  │ └─────────┘      └───────────┘      └───────────┘          │ │
│  └─────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

### 4.5 Internal vs Exported Functions

With a subpackage, we can properly encapsulate:

| Function | Current | New | Reason |
|----------|---------|-----|--------|
| `NewReceiver` | Exported | `receive.New` | Go idiom |
| `Push` | Exported | `Receiver.Push` | Public API |
| `Tick` | Exported | `Receiver.Tick` | Public API |
| `EventLoop` | Exported | `Receiver.EventLoop` | Public API |
| `pushWithLock` | lowercase | lowercase | Internal |
| `pushLockedNakBtree` | lowercase | lowercase | Internal |
| `adaptiveBackoff` | lowercase | lowercase | Internal |
| `parseRetryStrategy` | lowercase | lowercase | Internal |
| `CalcTooRecentThreshold` | Exported | `CalcTooRecentThreshold` | Utility (keep exported) |

---

## 5. Table-Driven Test Strategy

### 5.1 Test Generation Approach

```
┌─────────────────────────────────────────────────────────────────┐
│                    TEST GENERATION PIPELINE                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. AST Analysis           2. Reflection            3. Output   │
│  ┌─────────────┐           ┌─────────────┐         ┌─────────┐ │
│  │ Parse Go    │           │ Extract     │         │ Generate│ │
│  │ source      │──────────▶│ struct      │────────▶│ test    │ │
│  │ files       │           │ field types │         │ tables  │ │
│  └─────────────┘           └─────────────┘         └─────────┘ │
│         │                         │                      │      │
│         ▼                         ▼                      ▼      │
│  • Function signatures     • Corner values        • TestCase   │
│  • Config parameters       • Type bounds          • structs    │
│  • Branch conditions       • Enum values          • Runner     │
│                                                   • functions  │
└─────────────────────────────────────────────────────────────────┘
```

### 5.2 Test Table Structure per File

Each extracted file gets a corresponding table-driven test file:

```go
// receive_nak_table_test.go

type NakTestCase struct {
    Name string

    // CODE_PARAMS (from ReceiveConfig)
    UseNakBtree        bool
    TsbpdDelay         uint64
    NakRecentPercent   float64
    NakMergeGap        uint32
    FastNakEnabled     bool

    // TEST_INFRA
    PacketSequences    []uint32   // Packets to inject
    DropPattern        *DropPattern
    TickTimes          int
    MockTime           uint64

    // EXPECTATIONS
    ExpectedNakCount   int
    ExpectedNakSeqs    []uint32
    ExpectNakMerge     bool
}

var nakTests = []NakTestCase{
    // Corner cases for UseNakBtree
    {"NAK_Btree_Enabled", true, ...},
    {"NAK_Btree_Disabled", false, ...},

    // Corner cases for TsbpdDelay
    {"TSBPD_10ms", ..., 10_000, ...},
    {"TSBPD_500ms", ..., 500_000, ...},
    {"TSBPD_Max", ..., math.MaxUint64-1, ...},

    // Corner cases for NakRecentPercent
    {"Recent_5pct", ..., 0.05, ...},
    {"Recent_25pct", ..., 0.25, ...},
    {"Recent_Zero", ..., 0.0, ...},

    // Combination corners
    {"Combo_Btree_10ms_5pct", true, 10_000, 0.05, ...},
    ...
}
```

### 5.3 AST-Based Parameter Extraction

Tool to extract test parameters from `ReceiveConfig`:

```go
// tools/receive-test-gen/main.go (proposed)

func extractConfigParams() []ConfigParam {
    // Parse receive.go
    fset := token.NewFileSet()
    node, _ := parser.ParseFile(fset, "receive.go", nil, parser.ParseComments)

    // Find ReceiveConfig struct
    for _, decl := range node.Decls {
        if genDecl, ok := decl.(*ast.GenDecl); ok {
            for _, spec := range genDecl.Specs {
                if typeSpec, ok := spec.(*ast.TypeSpec); ok {
                    if typeSpec.Name.Name == "ReceiveConfig" {
                        // Extract fields
                        return extractFields(typeSpec.Type.(*ast.StructType))
                    }
                }
            }
        }
    }
}

func generateCornerCases(params []ConfigParam) []TestCase {
    var cases []TestCase
    for _, p := range params {
        switch p.Type {
        case "bool":
            cases = append(cases,
                TestCase{Name: p.Name + "_True", Values: map[string]any{p.Name: true}},
                TestCase{Name: p.Name + "_False", Values: map[string]any{p.Name: false}},
            )
        case "uint64":
            cases = append(cases,
                TestCase{Name: p.Name + "_Zero", Values: map[string]any{p.Name: uint64(0)}},
                TestCase{Name: p.Name + "_Max", Values: map[string]any{p.Name: uint64(math.MaxUint64-1)}},
                TestCase{Name: p.Name + "_Typical", Values: map[string]any{p.Name: uint64(100_000)}},
            )
        case "float64":
            cases = append(cases,
                TestCase{Name: p.Name + "_Zero", Values: map[string]any{p.Name: 0.0}},
                TestCase{Name: p.Name + "_One", Values: map[string]any{p.Name: 1.0}},
                TestCase{Name: p.Name + "_Mid", Values: map[string]any{p.Name: 0.5}},
            )
        // ... more types
        }
    }
    return cases
}
```

### 5.4 Reflection-Based Runtime Verification

```go
// receive_test_helpers.go

func verifyConfigCoverage(t *testing.T, testCases []NakTestCase) {
    configType := reflect.TypeOf(ReceiveConfig{})
    testedFields := make(map[string]bool)

    // Track which fields are varied across test cases
    for _, tc := range testCases {
        tcVal := reflect.ValueOf(tc)
        for i := 0; i < tcVal.NumField(); i++ {
            field := tcVal.Type().Field(i)
            if isCodeParam(field.Name) {
                testedFields[field.Name] = true
            }
        }
    }

    // Report untested config params
    for i := 0; i < configType.NumField(); i++ {
        field := configType.Field(i)
        if !testedFields[field.Name] {
            t.Logf("⚠️ Config param %s not varied in tests", field.Name)
        }
    }
}
```

---

## 6. Test Coverage Targets

### 6.1 Per-File Targets

| File | Current | Target | Priority |
|------|---------|--------|----------|
| `receive_nak.go` | ~50% | 95% | CRITICAL |
| `receive_push.go` | ~70% | 95% | CRITICAL |
| `receive_ring.go` | ~50% | 90% | HIGH |
| `receive_delivery.go` | ~55% | 90% | HIGH |
| `receive_scan.go` | ~95% | 98% | MEDIUM |
| `receive_ack.go` | ~60% | 95% | MEDIUM |
| `receive.go` (main) | ~85% | 95% | MEDIUM |

### 6.2 Test Categories per File

For each extracted file, create:

1. **Unit Tests** - Test functions in isolation with mocks
2. **Integration Tests** - Test interaction between components
3. **Corner Case Tests** - Boundary values, wraparound, etc.
4. **Error Path Tests** - Invalid inputs, timeouts, etc.
5. **Concurrency Tests** - Race detection, lock contention

---

## 7. Implementation Plan

### Phase 1: Preparation (No Code Split Yet)
1. ✅ Document current state (this document)
2. ⬜ Create test-gen tool for ReceiveConfig analysis
3. ⬜ Generate corner case matrix from config params
4. ⬜ Identify all existing receive tests
5. ⬜ Map existing tests to new file structure

### Phase 2: Create Subpackage & Move Code ✅ COMPLETED 2024-12-29
1. ✅ Create `congestion/live/receive/` directory
2. ✅ Move `receive.go` → `receive/receiver.go` (renamed types: `ReceiveConfig`→`Config`, `NewReceiver`→`New`)
3. ✅ Move dependent files: `fast_nak.go`, `nak_btree.go`, `nak_consolidate.go`, `packet_store*.go`
4. ✅ Update imports in `connection.go` and `fake.go`
5. ✅ Move 24 test files to `receive/` package
6. ✅ Keep `metrics_test.go` in `live/` (tests both sender+receiver)
7. ✅ Build passes
8. ✅ All tests pass (full suite: 40s main + 56s receive)

**Files in new subpackage:**
```
congestion/live/receive/
├── receiver.go           # Core struct, New(), Config
├── fast_nak.go           # FastNAK detection
├── nak_btree.go          # NAK btree data structure
├── nak_consolidate.go    # NAK consolidation
├── packet_store.go       # Packet store interface
├── packet_store_btree.go # BTree implementation
└── 24 *_test.go files
```

### Phase 3: Split into Domain Files ✅ COMPLETED 2024-12-30
1. ✅ Extract NAK functions → `receive/nak.go` (556 lines)
2. ✅ Extract Push functions → `receive/push.go` (284 lines)
3. ✅ Extract Scan functions → `receive/scan.go` (342 lines)
4. ✅ Extract ACK functions → `receive/ack.go` (347 lines)
5. ✅ Extract Ring functions → `receive/ring.go` (387 lines)
6. ✅ Extract Delivery/Tick functions → `receive/tick.go` (510 lines)
7. ✅ Keep core in `receive/receiver.go` (416 lines)
8. ✅ Build passes
9. ✅ All tests pass (full suite)

**Final Domain File Structure:**
```
congestion/live/receive/
├── receiver.go           # 416 lines - Core struct, New(), Config, Stats()
├── ack.go               # 347 lines - periodicACK*, ACK generation
├── nak.go               # 556 lines - periodicNAK*, expireNakEntries
├── push.go              # 284 lines - Push(), pushLocked*, pushToRing
├── scan.go              # 342 lines - contiguousScan*, gapScan, tooRecentThreshold
├── ring.go              # 387 lines - drainPacketRing*, adaptiveBackoff
├── tick.go              # 510 lines - Tick(), EventLoop(), deliver*
├── fast_nak.go          # 195 lines - FastNAK detection
├── nak_btree.go         # 145 lines - NAK btree data structure
├── nak_consolidate.go   # 143 lines - NAK consolidation
├── packet_store.go      # 171 lines - Packet store interface
├── packet_store_btree.go# 174 lines - BTree implementation
└── testing.go           # Testing helpers (moved from live/)
Total: ~3,670 lines (split from original 2,758 + imports overhead)
```

### Phase 4: Add Table-Driven Tests (IN PROGRESS - 2024-12-30)
1. ✅ Created `receive/nak_periodic_table_test.go` - Tests for NAK generation
   - `periodicNakBtreeLocked`: 0% → **100%** ✅
   - `triggerFastNak`: 0% → **100%** ✅
   - Tests: interval checks, gap detection, wraparound, consolidation
   - 8 table-driven + 5 standalone edge case tests
2. ✅ Created `receive/ack_periodic_table_test.go` - Tests for ACK generation
   - `periodicACK`: 0% → **82.7%** ✅
   - `periodicACKWriteLocked`: 0% → **80%** ✅
   - Tests: interval checks, light ACK, high water mark, TSBPD skip, wraparound
   - 6 table-driven + 3 standalone bug-hunting tests
3. ✅ Created `receive/utility_test.go` - Tests for utility functions
   - `PacketRate`: 0% → **100%** ✅
   - `UseEventLoop`: 0% → **100%** ✅
   - `SetNAKInterval`: 0% → **100%** ✅
   - `String`: 0% → **100%** ✅
   - `deliverReadyPacketsNoLock`: 0% → **100%** ✅
   - `parseRetryStrategy`: 28.6% → **100%** ✅
4. ⬜ Create `receive/push_table_test.go` (next)
5. ⬜ Create `receive/ring_table_test.go`
6. ⬜ Create `receive/tick_table_test.go` (delivery functions)

**Coverage Improvement (Phase 4 so far):**
- Package coverage: 77.9% → **86.7%** (+8.8%)

### Phase 4b: Hot Path Benchmarks ✅ COMPLETED 2024-12-30

Created `receive/hotpath_bench_test.go` - Performance benchmarks for critical paths:

**Key Design Decision**: Ring-based benchmarks use concurrent drain goroutine (realistic io_uring + EventLoop scenario) instead of oversized rings.

| Benchmark | Time/op | Allocs | Notes |
|-----------|---------|--------|-------|
| PushToRing | 813 ns | 4 | Ring with concurrent drain |
| PushWithLock | 1224 ns | 5 | Lock-based push |
| PushParallel | 717 ns | 1 | Multi-producer ring push |
| DrainPacketRing/100 | 36 µs | 219 | Drain 100 packets |
| DrainRingByDelta | 2 ns | 0 | Delta-based drain |
| ContiguousScan/1000 | 265 ns | 4 | Scan 1000 packets |
| EventLoopIteration | 283 ns | 7 | Single EventLoop tick |
| PacketStoreInsert | 760 ns | 3 | BTree insert |
| CircularSeqArithmetic | 0.26 ns | 0 | 31-bit math |
| NakBtreeOperations | 112-304 ns | 0 | NAK btree ops |

**Key Findings:**
- Ring push is **~35% faster** than lock-based (813 ns vs 1224 ns)
- Parallel ring push scales well (1 alloc/op vs 5 for lock)
- CircularSeq arithmetic is extremely fast (sub-ns)
- EventLoop iteration overhead is minimal (283 ns)

### Phase 4c: Optimized BTree Insert ✅ COMPLETED 2024-12-30

**Problem**: Original `Insert` used `Has()` + `ReplaceOrInsert()` = 2 btree traversals per insert.

**Solution**: Single-traversal `Insert` using `ReplaceOrInsert()` directly:
- If returns `(nil, false)` → new item inserted (common case: 1 traversal)
- If returns `(old, true)` → duplicate detected, restore old item (rare case: 2 traversals)

**Benchmark Results:**
| Scenario | DuplicateCheck (old) | Insert (new) | Improvement |
|----------|---------------------|--------------|-------------|
| Unique packets (99%) | 790 ns | 636 ns | **20% faster** |
| Duplicate packets | 472 ns | 582 ns | 23% slower (acceptable) |
| Mixed 1% duplicates | 796 ns | 633 ns | **20% faster** |

**Memory**: Identical (3 allocs/op for both approaches)

**Code Changes:**
- `Insert()` → renamed to `InsertDuplicateCheck()` (legacy, for benchmarking)
- `InsertOptimized()` → renamed to `Insert()` (production, 20% faster)
- New return signature: `(inserted bool, duplicatePacket packet.Packet)`

**Audit of all btree `ReplaceOrInsert` usages:**
| File | Function | Pattern | Status |
|------|----------|---------|--------|
| `nak_btree.go` | `Insert(seq)` | `ReplaceOrInsert()` directly | ✅ Already optimal |
| `nak_btree.go` | `InsertBatch(seqs)` | `ReplaceOrInsert()` + check `replaced` | ✅ Already optimal |
| `ack_btree.go` | `Insert(entry)` | `ReplaceOrInsert()` directly | ✅ Already optimal |
| `packet_store_btree.go` | `Insert(pkt)` | Was `Has()` + `ReplaceOrInsert()` | ✅ **Fixed** |

Only `packet_store_btree.go` had the suboptimal pattern. NAK/ACK btrees were already optimized.

### Phase 4d: insertAndUpdateMetrics Helper ✅ COMPLETED 2024-12-30

**Problem**: Duplicated Insert + metrics update pattern across 5+ locations (tick.go, ring.go x2, push.go x3).

**Solution**: Consolidated helper function `insertAndUpdateMetrics(p, pktLen, isRetransmit, updateDrainMetric)`:
- Calls optimized `Insert()`
- Updates all relevant metrics (PktBuf, PktUnique, ByteBuf, ByteUnique, etc.)
- Handles retransmit metrics when `isRetransmit=true`
- Handles drain metrics when `updateDrainMetric=true`
- Releases duplicate packets via `releasePacketFully()`

**Benefits:**
- Reduced code duplication (~50 lines removed)
- Single test covers all metric updates
- Type-safe (`pktLen` is `uint64`, no casts needed)
- Easier to maintain and extend

### Phase 5: Cleanup and Verification
1. ✅ Old `congestion/live/receive.go` deleted (code moved to subpackage)
2. ⬜ Run full test suite including race detector
3. ⬜ Verify no regressions in integration tests
4. ⬜ Update any remaining references

### Phase 6: Coverage Enforcement
1. ⬜ Add coverage thresholds per file to CI
2. ⬜ Generate coverage reports per subpackage file
3. ⬜ Track progress toward 90%+ targets

---

## 8. Success Criteria

- [x] New `congestion/live/receive/` subpackage created ✅
- [x] 12 files in subpackage, each <600 lines ✅
- [x] Old `congestion/live/receive.go` code moved to subpackage ✅
- [x] `connection.go` and `fake.go` updated to use `receive.New()` ✅
- [x] `nak.go` has table tests (`nak_periodic_table_test.go`) ✅
- [x] `ack.go` has table tests (`ack_periodic_table_test.go`) ✅
- [ ] `push.go` has table tests
- [ ] `ring.go` has table tests
- [ ] `tick.go` has table tests
- [ ] Test-gen tool can auto-generate corner case skeletons
- [ ] Overall receive coverage: 73.6% → **86.5% (in progress)** → 90%+
- [ ] All <50% coverage functions reach 90%+
- [x] Zero regressions in existing tests ✅
- [ ] Race detector passes on all new tests

---

## 9. Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Split breaks functionality | HIGH | Run tests after each extraction step |
| Circular imports | MEDIUM | Receiver struct in receiver.go, methods reference it |
| Import path changes | LOW | Only 2 files need updating (`connection.go`, `fake.go`) |
| Test-gen tool complexity | LOW | Start simple, iterate |
| Coverage gaming (trivial tests) | MEDIUM | Review test quality, not just % |
| Package boundary overhead | LOW | Go inlines small functions, negligible perf impact |
| IDE/tooling confusion | LOW | Standard Go subpackage pattern, well supported |

---

## 10. References

- [Fork Comprehensive Plan](fork_comprehensive_plan.md) - Overall testing strategy
- [Table-Driven Test Design](table_driven_test_design.md) - Test methodology
- [Loss Recovery Table Test](../congestion/live/loss_recovery_table_test.go) - Example implementation

