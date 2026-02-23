# NAK btree Implementation Plan

**Status**: IMPLEMENTATION PLAN
**Date**: 2025-12-14
**Design Reference**: `design_nak_btree.md`

---

## Important Notes

### No Backward Compatibility Required

**This implementation does NOT need to maintain backward compatibility during the implementation process.**

- It is acceptable for the system to be in a **non-functional state** until all phases are complete
- For example: config fields and CLI flags can be added in Phase 1 even though they won't be used until Phase 4
- Tests may fail during intermediate phases - this is expected
- The goal is a clean, phased implementation that is easy to follow and review

### Implementation Approach

- Each phase builds on previous phases
- Each step within a phase should be a separate commit for easy review
- Run `go build ./...` after each step to verify compilation
- Run `go test ./...` only after completing a full phase (intermediate failures are expected)

---

## High-Level Phases Overview

| Phase | Name | Description | Dependencies |
|-------|------|-------------|--------------|
| **1** | Configuration & Flags | Add all new config options and CLI flags | None |
| **2** | Sequence Math | Add generic sequence number math with wraparound | None |
| **3** | NAK btree Data Structure | Create the NAK btree with basic operations | Phase 2 |
| **4** | Receiver Integration | Wire NAK btree into receiver, add dispatch | Phase 1, 3 |
| **5** | Consolidation & FastNAK | Add consolidation algorithm and FastNAK | Phase 4 |
| **6** | Sender Modifications | Add honor-order retransmission dispatch | Phase 1 |
| **7** | Metrics | Add all new metrics and Prometheus export | Phase 4, 5, 6 |
| **8** | Unit Tests | Add comprehensive unit tests | Phase 1-7 |
| **9** | Benchmarks | Add performance benchmarks | Phase 1-7 |
| **10** | Integration Testing | Update integration tests for NAK btree validation | Phase 1-9 |

```
Phase 1 ──────────────────────────────────┐
(Config)                                  │
                                          ▼
Phase 2 ────────────┐              Phase 4 ────────────────┐
(Seq Math)          │              (Receiver Integration)  │
        │           │                      │               │
        ▼           │                      ▼               │
Phase 3 ────────────┘              Phase 5 ────────────────┤
(NAK btree)                        (Consolidation/FastNAK) │
                                           │               │
                                           ▼               │
                                   Phase 6 ◄───────────────┘
                                   (Sender)
                                           │
                                           ▼
                                   Phase 7
                                   (Metrics)
                                           │
                                           ▼
                                   Phase 8
                                   (Unit Tests)
                                           │
                                           ▼
                                   Phase 9
                                   (Benchmarks)
                                           │
                                           ▼
                                   Phase 10
                                   (Integration)
```

---

## Phase 1: Configuration & Flags

**Goal**: Add all new configuration options and CLI flags. These won't be used until later phases, but adding them first establishes the API.

### Step 1.1: Add Config Fields to `config.go`

**File**: `config.go`
**Location**: After line ~180 (after existing io_uring config fields)

Add new fields to `Config` struct:

```go
// NAK btree configuration (for io_uring receive path)
// Timer intervals (replaces hardcoded 10ms/20ms)
TickIntervalMs        uint64 // Default: 10 (TSBPD delivery tick interval in ms)
PeriodicNakIntervalMs uint64 // Default: 20 (Periodic NAK timer interval in ms)
PeriodicAckIntervalMs uint64 // Default: 10 (Periodic ACK timer interval in ms)

// NAK btree behavior
UseNakBtree            bool    // Enable NAK btree (auto-set when IoUringRecvEnabled=true)
SuppressImmediateNak   bool    // Suppress immediate NAK (auto-set when IoUringRecvEnabled=true)
NakRecentPercent       float64 // Default: 0.10 (percentage of tsbpdDelay for "too recent")
NakMergeGap            uint32  // Default: 3 (max sequence gap to merge in consolidation)
NakConsolidationBudget uint64  // Default: 2000 (consolidation time budget in µs)

// FastNAK configuration
FastNakEnabled       bool   // Default: true (enable FastNAK optimization)
FastNakThresholdMs   uint64 // Default: 50 (silent period to trigger FastNAK in ms)
FastNakRecentEnabled bool   // Default: true (add recent gap on FastNAK trigger)

// Sender configuration
HonorNakOrder bool // Default: false (retransmit in NAK packet order)
```

### Step 1.2: Add Default Values in `DefaultConfig()`

**File**: `config.go`
**Function**: `DefaultConfig()` (around line ~400)

Add defaults:

```go
// Timer intervals
TickIntervalMs:        10,
PeriodicNakIntervalMs: 20,
PeriodicAckIntervalMs: 10,

// NAK btree defaults (UseNakBtree and SuppressImmediateNak are auto-set)
NakRecentPercent:       0.10,
NakMergeGap:            3,
NakConsolidationBudget: 2000,

// FastNAK defaults
FastNakEnabled:       true,
FastNakThresholdMs:   50,
FastNakRecentEnabled: true,

// Sender defaults
HonorNakOrder: false,
```

### Step 1.3: Add CLI Flags to `contrib/common/flags.go`

**File**: `contrib/common/flags.go`
**Location**: After line ~71 (after io_uring recv flags)

Add new flags:

```go
// Timer interval flags
TickIntervalMs = flag.Uint64("tickintervalms", 0,
    "TSBPD delivery tick interval in milliseconds (default: 10)")
PeriodicNakIntervalMs = flag.Uint64("periodicnakintervalms", 0,
    "Periodic NAK timer interval in milliseconds (default: 20)")
PeriodicAckIntervalMs = flag.Uint64("periodicackintervalms", 0,
    "Periodic ACK timer interval in milliseconds (default: 10)")

// NAK btree flags
UseNakBtree = flag.Bool("usenakbtree", false,
    "Enable NAK btree for io_uring receive path (auto-enabled with -iouringrecvenabled)")
NakRecentPercent = flag.Float64("nakrecentpercent", 0,
    "Percentage of TSBPD delay for 'too recent' threshold (default: 0.10)")
NakMergeGap = flag.Uint64("nakmergegap", 0,
    "Max sequence gap to merge in NAK consolidation (default: 3)")
NakConsolidationBudgetMs = flag.Uint64("nakconsolidationbudgetms", 0,
    "Max time for NAK consolidation in milliseconds (default: 2)")

// FastNAK flags
FastNakEnabled = flag.Bool("fastnakEnabled", false,
    "Enable FastNAK optimization (default: true when NAK btree enabled)")
FastNakThresholdMs = flag.Uint64("fastnakthresholdms", 0,
    "Silent period to trigger FastNAK in milliseconds (default: 50)")
FastNakRecentEnabled = flag.Bool("fastnakrecentenabled", false,
    "Add recent gap immediately on FastNAK trigger (default: true)")

// Sender flags
HonorNakOrder = flag.Bool("honornakorder", false,
    "Retransmit packets in NAK packet order (oldest first)")
```

### Step 1.4: Add Flag Application in `ApplyFlagsToConfig()`

**File**: `contrib/common/flags.go`
**Function**: `ApplyFlagsToConfig()` (after line ~268)

Add:

```go
// Timer interval flags
if FlagSet["tickintervalms"] {
    config.TickIntervalMs = *TickIntervalMs
}
if FlagSet["periodicnakintervalms"] {
    config.PeriodicNakIntervalMs = *PeriodicNakIntervalMs
}
if FlagSet["periodicackintervalms"] {
    config.PeriodicAckIntervalMs = *PeriodicAckIntervalMs
}

// NAK btree flags
if FlagSet["usenakbtree"] {
    config.UseNakBtree = *UseNakBtree
}
if FlagSet["nakrecentpercent"] {
    config.NakRecentPercent = *NakRecentPercent
}
if FlagSet["nakmergegap"] {
    config.NakMergeGap = uint32(*NakMergeGap)
}
if FlagSet["nakconsolidationbudgetms"] {
    config.NakConsolidationBudget = *NakConsolidationBudgetMs * 1000 // Convert to µs
}

// FastNAK flags
if FlagSet["fastnakEnabled"] {
    config.FastNakEnabled = *FastNakEnabled
}
if FlagSet["fastnakthresholdms"] {
    config.FastNakThresholdMs = *FastNakThresholdMs
}
if FlagSet["fastnakrecentenabled"] {
    config.FastNakRecentEnabled = *FastNakRecentEnabled
}

// Sender flags
if FlagSet["honornakorder"] {
    config.HonorNakOrder = *HonorNakOrder
}
```

### Step 1.5: Add Auto-Configuration Logic

**File**: `config.go`
**Function**: Add new function `ApplyAutoConfiguration()` after `DefaultConfig()`

```go
// ApplyAutoConfiguration sets internal configuration based on other settings.
// Call this after user configuration is applied but before connection creation.
func (c *Config) ApplyAutoConfiguration() {
    // When io_uring recv is enabled, NAK btree and suppress immediate NAK are required
    if c.IoUringRecvEnabled {
        c.UseNakBtree = true
        c.SuppressImmediateNak = true

        // Default FastNAK to enabled if not explicitly set
        // (We can't tell if user set FastNakEnabled=false explicitly, so always set)
        if !c.FastNakEnabled {
            c.FastNakEnabled = true
        }
        if !c.FastNakRecentEnabled {
            c.FastNakRecentEnabled = true
        }
    }
}
```

### Step 1.6: Update `contrib/common/test_flags.sh`

**File**: `contrib/common/test_flags.sh`
**Location**: Add new test section after existing flag tests

```bash
# Timer interval flags
test_flag "-tickintervalms" "5" "tick interval 5ms"
test_flag "-tickintervalms" "20" "tick interval 20ms"
test_flag "-periodicnakintervalms" "10" "NAK interval 10ms"
test_flag "-periodicnakintervalms" "40" "NAK interval 40ms"
test_flag "-periodicackintervalms" "5" "ACK interval 5ms"

# NAK btree flags
test_flag "-usenakbtree" "" "NAK btree enabled"
test_flag "-nakrecentpercent" "0.15" "NAK recent percent"
test_flag "-nakmergegap" "5" "NAK merge gap"
test_flag "-nakconsolidationbudgetms" "3" "consolidation budget"

# FastNAK flags
test_flag "-fastnakEnabled" "" "FastNAK enabled"
test_flag "-fastnakthresholdms" "100" "FastNAK threshold"
test_flag "-fastnakrecentenabled" "" "FastNAK recent enabled"

# Sender flags
test_flag "-honornakorder" "" "honor NAK order"
```

### Step 1.7: Verify Phase 1 Completion

```bash
# Should compile without errors
go build ./...

# Flags should be recognized (even if they don't do anything yet)
./contrib/server/server -h 2>&1 | grep -i "tickintervalms\|usenakbtree\|fastnakEnabled"
```

---

## Phase 2: Sequence Math

**Goal**: Add generic sequence number math functions that handle 31-bit SRT wraparound.

### Step 2.1: Create `circular/seq_math.go`

**File**: `circular/seq_math.go` (NEW FILE)

```go
package circular

// MaxSeqNumber is the maximum 31-bit SRT sequence number.
const MaxSeqNumber = 0x7FFFFFFF // 2^31 - 1

// SeqLess returns true if a < b, handling wraparound.
// Uses signed comparison: if (a - b) is negative (high bit set), a < b.
func SeqLess(a, b uint32) bool {
    diff := int32(a - b)
    return diff < 0
}

// SeqGreater returns true if a > b, handling wraparound.
func SeqGreater(a, b uint32) bool {
    diff := int32(a - b)
    return diff > 0
}

// SeqLessOrEqual returns true if a <= b, handling wraparound.
func SeqLessOrEqual(a, b uint32) bool {
    return !SeqGreater(a, b)
}

// SeqDiff returns (a - b) as a signed value, handling wraparound.
// Positive if a > b, negative if a < b.
func SeqDiff(a, b uint32) int32 {
    return int32(a - b)
}
```

### Step 2.2: Create `circular/seq_math_test.go`

**File**: `circular/seq_math_test.go` (NEW FILE)

See design document Section 4.7.3 for test cases. Port tests from goTrackRTP.

### Step 2.3: Verify Phase 2 Completion

```bash
go test ./circular/... -v
```

---

## Phase 3: NAK btree Data Structure

**Goal**: Create the NAK btree that stores missing sequence numbers.

### Step 3.1: Create `congestion/live/nak_btree.go`

**File**: `congestion/live/nak_btree.go` (NEW FILE)

```go
package live

import (
    "sync"

    "github.com/datarhei/gosrt/circular"
    "github.com/google/btree"
)

// nakBtree stores missing sequence numbers for NAK generation.
// Stores singles only (not ranges) for simplicity of delete operations.
// Uses a separate lock from the packet btree.
type nakBtree struct {
    tree *btree.BTreeG[uint32]
    mu   sync.RWMutex
}

// newNakBtree creates a new NAK btree.
// Degree of 32 is a good default (same as packet btree).
func newNakBtree(degree int) *nakBtree {
    return &nakBtree{
        tree: btree.NewG[uint32](degree, func(a, b uint32) bool {
            return circular.SeqLess(a, b)
        }),
    }
}

// Insert adds a missing sequence number.
func (nb *nakBtree) Insert(seq uint32) {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    nb.tree.ReplaceOrInsert(seq)
}

// Delete removes a sequence number (packet arrived or expired).
func (nb *nakBtree) Delete(seq uint32) bool {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    _, found := nb.tree.Delete(seq)
    return found
}

// DeleteBefore removes all sequences before cutoff (expired).
// Returns count of deleted entries.
func (nb *nakBtree) DeleteBefore(cutoff uint32) int {
    nb.mu.Lock()
    defer nb.mu.Unlock()

    var toDelete []uint32
    nb.tree.Ascend(func(seq uint32) bool {
        if circular.SeqLess(seq, cutoff) {
            toDelete = append(toDelete, seq)
            return true
        }
        return false // Stop when we reach cutoff
    })

    for _, seq := range toDelete {
        nb.tree.Delete(seq)
    }
    return len(toDelete)
}

// Len returns the number of entries.
func (nb *nakBtree) Len() int {
    nb.mu.RLock()
    defer nb.mu.RUnlock()
    return nb.tree.Len()
}

// Iterate traverses in ascending order (oldest first = most urgent).
func (nb *nakBtree) Iterate(fn func(seq uint32) bool) {
    nb.mu.RLock()
    defer nb.mu.RUnlock()
    nb.tree.Ascend(fn)
}

// Min returns the minimum sequence number, or 0 if empty.
func (nb *nakBtree) Min() (uint32, bool) {
    nb.mu.RLock()
    defer nb.mu.RUnlock()
    if nb.tree.Len() == 0 {
        return 0, false
    }
    min, _ := nb.tree.Min()
    return min, true
}
```

### Step 3.2: Verify Phase 3 Completion

```bash
go build ./congestion/live/...
```

---

## Phase 4: Receiver Integration

**Goal**: Wire NAK btree into receiver, add function dispatch, update connection initialization.

### Step 4.1: Update `ReceiveConfig` Struct

**File**: `congestion/live/receive.go`
**Location**: Line ~16-27 (`ReceiveConfig` struct)

Add new fields:

```go
type ReceiveConfig struct {
    // ... existing fields ...

    // NAK btree configuration
    UseNakBtree            bool
    SuppressImmediateNak   bool
    TsbpdDelay             uint64  // Microseconds, for scan window calculation
    NakRecentPercent       float64
    NakMergeGap            uint32
    NakConsolidationBudget uint64  // Microseconds

    // FastNAK configuration
    FastNakEnabled       bool
    FastNakThresholdUs   uint64 // Microseconds
    FastNakRecentEnabled bool
}
```

### Step 4.2: Update `receiver` Struct

**File**: `congestion/live/receive.go`
**Location**: Line ~30-70 (`receiver` struct)

Add new fields after existing fields:

```go
type receiver struct {
    // ... existing fields (lines 31-69) ...

    // NAK btree fields (new)
    useNakBtree          bool
    suppressImmediateNak bool
    nakBtree             *nakBtree
    nakBtreeLock         sync.RWMutex // Separate lock for NAK btree
    nakScanStartPoint    atomic.Uint32

    // Scan window configuration
    tsbpdDelay       uint64  // Microseconds
    nakRecentPercent float64
    nakMergeGap      uint32
    nakConsolidationBudget time.Duration

    // FastNAK fields
    fastNakEnabled         bool
    fastNakThreshold       time.Duration
    fastNakRecentEnabled   bool
    lastPacketArrivalTime  atomic.Int64 // UnixMicro
    lastDataPacketSeq      atomic.Uint32
}
```

### Step 4.3: Update `NewReceiver()` Function

**File**: `congestion/live/receive.go`
**Function**: `NewReceiver()` (line ~73)

Add initialization:

```go
func NewReceiver(config ReceiveConfig) congestion.Receiver {
    // ... existing code ...

    r := &receiver{
        // ... existing initializations ...

        // NAK btree initialization
        useNakBtree:          config.UseNakBtree,
        suppressImmediateNak: config.SuppressImmediateNak,
        tsbpdDelay:           config.TsbpdDelay,
        nakRecentPercent:     config.NakRecentPercent,
        nakMergeGap:          config.NakMergeGap,
        nakConsolidationBudget: time.Duration(config.NakConsolidationBudget) * time.Microsecond,

        // FastNAK initialization
        fastNakEnabled:       config.FastNakEnabled,
        fastNakThreshold:     time.Duration(config.FastNakThresholdUs) * time.Microsecond,
        fastNakRecentEnabled: config.FastNakRecentEnabled,
    }

    // Create NAK btree if enabled
    if r.useNakBtree {
        r.nakBtree = newNakBtree(32) // Same degree as packet btree
    }

    // ... rest of existing code ...
}
```

### Step 4.4: Add Function Dispatch for `periodicNAK`

**File**: `congestion/live/receive.go`
**Location**: Before existing `periodicNAKLocked()` function (line ~462)

```go
// periodicNAK is the entry point - dispatches to Original or NAKBtree.
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    if r.metrics != nil {
        r.metrics.NakPeriodicCalls.Add(1)
    }

    if r.useNakBtree {
        return r.periodicNakBtree(now)
    }
    return r.periodicNakOriginal(now)
}
```

### Step 4.5: Rename Existing `periodicNAKLocked()` to `periodicNakOriginal()`

**File**: `congestion/live/receive.go`
**Location**: Line ~462

Rename function from `periodicNAKLocked` to `periodicNakOriginal`.

### Step 4.6: Add `periodicNakBtree()` Function

**File**: `congestion/live/receive.go`
**Location**: After `periodicNakOriginal()`

See design document Section 4.3.1 for full implementation.

### Step 4.7: Update `Push()` to Handle NAK btree

**File**: `congestion/live/receive.go`
**Function**: `Push()` (around line ~200-300)

Add delete from NAK btree when packet arrives:

```go
func (r *receiver) Push(pkt packet.Packet) {
    // ... existing code ...

    // If NAK btree is enabled, delete this sequence (packet arrived)
    if r.useNakBtree && r.nakBtree != nil {
        seq := pkt.Header().PacketSequenceNumber.Val()
        if r.nakBtree.Delete(seq) {
            if r.metrics != nil {
                r.metrics.NakBtreeDeletes.Add(1)
            }
        }
    }

    // ... existing gap detection code ...

    // Modify immediate NAK behavior
    if !r.suppressImmediateNak {
        // Original immediate NAK behavior
        nakList := []circular.Number{
            r.maxSeenSequenceNumber.Inc(),
            pkt.Header().PacketSequenceNumber.Dec(),
        }
        r.sendNAK(nakList)
    }
    // If suppressImmediateNak is true, gaps will be handled by periodic NAK

    // ... rest of existing code ...
}
```

### Step 4.8: Update `connection.go` to Pass Config to Receiver

**File**: `connection.go`
**Location**: Line ~386-397 (NewReceiver call)

Update to pass new config:

```go
c.recv = live.NewReceiver(live.ReceiveConfig{
    InitialSequenceNumber:  c.initialPacketSequenceNumber,
    PeriodicACKInterval:    getAckInterval(c.config),
    PeriodicNAKInterval:    getNakInterval(c.config),
    OnSendACK:              c.sendACK,
    OnSendNAK:              c.sendNAK,
    OnDeliver:              c.deliver,
    PacketReorderAlgorithm: c.config.PacketReorderAlgorithm,
    BTreeDegree:            c.config.BTreeDegree,
    LockTimingMetrics:      c.metrics.ReceiverLockTiming,
    ConnectionMetrics:      c.metrics,

    // NAK btree config
    UseNakBtree:            c.config.UseNakBtree,
    SuppressImmediateNak:   c.config.SuppressImmediateNak,
    TsbpdDelay:             uint64(c.tsbpdDelay),
    NakRecentPercent:       c.config.NakRecentPercent,
    NakMergeGap:            c.config.NakMergeGap,
    NakConsolidationBudget: c.config.NakConsolidationBudget,

    // FastNAK config
    FastNakEnabled:       c.config.FastNakEnabled,
    FastNakThresholdUs:   c.config.FastNakThresholdMs * 1000,
    FastNakRecentEnabled: c.config.FastNakRecentEnabled,
})
```

### Step 4.9: Add Helper Functions for Timer Intervals

**File**: `connection.go`
**Location**: Before line ~386

```go
func getAckInterval(config Config) uint64 {
    if config.PeriodicAckIntervalMs > 0 {
        return config.PeriodicAckIntervalMs * 1000 // Convert to µs
    }
    return 10_000 // Default 10ms
}

func getNakInterval(config Config) uint64 {
    if config.PeriodicNakIntervalMs > 0 {
        return config.PeriodicNakIntervalMs * 1000 // Convert to µs
    }
    return 20_000 // Default 20ms
}

func getTickInterval(config Config) time.Duration {
    if config.TickIntervalMs > 0 {
        return time.Duration(config.TickIntervalMs) * time.Millisecond
    }
    return 10 * time.Millisecond // Default 10ms
}
```

### Step 4.10: Update Tick Interval Usage

**File**: `connection.go`
**Location**: Line ~382

Change:
```go
c.tick = 10 * time.Millisecond
```

To:
```go
c.tick = getTickInterval(c.config)
```

### Step 4.11: Verify Phase 4 Completion

```bash
go build ./...
```

---

## Phase 5: Consolidation & FastNAK

**Goal**: Add NAK consolidation algorithm and FastNAK optimization.

### Step 5.1: Create `congestion/live/nak_consolidate.go`

**File**: `congestion/live/nak_consolidate.go` (NEW FILE)

See design document Section 4.4 for full implementation including:
- `NAKEntry` struct
- `nakEntryPool` sync.Pool
- `consolidateNakBtree()` function
- `entriesToNakList()` function

### Step 5.2: Create `congestion/live/fast_nak.go`

**File**: `congestion/live/fast_nak.go` (NEW FILE)

See design document Section 4.5 for full implementation including:
- `checkFastNak()` function
- `triggerFastNak()` function
- `FastNAKRecent` sequence jump detection

### Step 5.3: Update `Push()` for FastNAK Tracking

**File**: `congestion/live/receive.go`
**Function**: `Push()`

Add at start of function:

```go
func (r *receiver) Push(pkt packet.Packet) {
    now := time.Now()

    // Track for FastNAK
    if r.fastNakEnabled && r.useNakBtree {
        r.lastPacketArrivalTime.Store(now.UnixMicro())

        h := pkt.Header()
        if !h.IsControlPacket {
            // Track sequence for FastNAKRecent
            seq := h.PacketSequenceNumber.Val()
            r.lastDataPacketSeq.Store(seq)
        }
    }

    // ... rest of existing code ...
}
```

### Step 5.4: Integrate FastNAK Check

**File**: `congestion/live/receive.go`
**Function**: `Push()` - after packet processing

```go
// Check for FastNAK trigger
if r.fastNakEnabled && r.useNakBtree {
    r.checkFastNak()
}
```

### Step 5.5: Verify Phase 5 Completion

```bash
go build ./...
```

---

## Phase 6: Sender Modifications

**Goal**: Add honor-order retransmission dispatch.

### Step 6.1: Update `SendConfig` Struct

**File**: `congestion/live/send.go`
**Location**: `SendConfig` struct

Add:
```go
HonorNakOrder bool // Retransmit in NAK packet order
```

### Step 6.2: Update `sender` Struct

**File**: `congestion/live/send.go`
**Location**: `sender` struct

Add:
```go
honorNakOrder bool
```

### Step 6.3: Update `NewSender()` Function

**File**: `congestion/live/send.go`
**Function**: `NewSender()`

Add initialization:
```go
honorNakOrder: config.HonorNakOrder,
```

### Step 6.4: Add Function Dispatch for NAK Processing

**File**: `congestion/live/send.go`
**Location**: Before existing `nakLocked()` function

Add dispatch function:
```go
// nakLocked - dispatches to original or honor-order implementation.
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
    if s.honorNakOrder {
        return s.nakLockedHonorOrder(sequenceNumbers)
    }
    return s.nakLockedOriginal(sequenceNumbers)
}
```

### Step 6.5: Rename Existing Function and Add Honor-Order Version

**File**: `congestion/live/send.go`

Rename existing `nakLocked()` to `nakLockedOriginal()`.

Add `nakLockedHonorOrder()` as per design document Section 4.8.4.

### Step 6.6: Update `connection.go` to Pass Config to Sender

**File**: `connection.go`
**Location**: Line ~407-417 (NewSender call)

Add:
```go
HonorNakOrder: c.config.HonorNakOrder,
```

### Step 6.7: Verify Phase 6 Completion

```bash
go build ./...
```

---

## Phase 7: Metrics

**Goal**: Add all new metrics and Prometheus export.

### Step 7.1: Add Metrics to `metrics/metrics.go`

**File**: `metrics/metrics.go`
**Location**: In `ConnectionMetrics` struct

Add new atomic counters as per design document Section 5.1.

### Step 7.2: Update `metrics/handler.go`

**File**: `metrics/handler.go`

Add Prometheus export for new metrics as per design document Section 5.4.

### Step 7.3: Update Metric Increment Points

**Files**: `congestion/live/receive.go`, `congestion/live/send.go`

Add metric increments at appropriate points:
- NAK btree Insert/Delete
- Periodic NAK runs
- FastNAK triggers
- Consolidation runs/timeouts
- etc.

### Step 7.4: Verify Phase 7 Completion

```bash
go build ./...
go test ./metrics/... -v
```

---

## Phase 8: Unit Tests

**Goal**: Add comprehensive unit tests.

### Step 8.1: Create Test Files

Create the following new test files:
- `circular/seq_math_test.go` (Phase 2)
- `congestion/live/nak_btree_test.go`
- `congestion/live/nak_consolidate_test.go`
- `congestion/live/fast_nak_test.go`
- `congestion/live/receive_concurrent_test.go`

### Step 8.2: Add Tests to Existing Files

Update:
- `congestion/live/receive_test.go` - NAK btree integration tests
- `config_test.go` - New config option tests

### Step 8.3: Verify Phase 8 Completion

```bash
go test ./... -v
go test ./... -race
```

---

## Phase 9: Benchmarks

**Goal**: Add performance benchmarks.

### Step 9.1: Create Benchmark Files

- `congestion/live/receive_bench_test.go` - Update with NAK btree benchmarks
- `congestion/live/nak_consolidate_bench_test.go` - Consolidation benchmarks

### Step 9.2: Run Benchmarks

```bash
go test -bench=. ./congestion/live/... -benchmem
```

---

## Phase 10: Integration Testing

**Goal**: Update integration tests for NAK btree validation.

### Step 10.1: Update Test Configurations

**File**: `contrib/integration_testing/test_configs.go`

Add NAK btree isolation tests as per design document Section 9.

### Step 10.2: Update `config.go` Integration

**File**: `contrib/integration_testing/config.go`

Add `WithNakBtree()` helper method.

### Step 10.3: Update `analysis.go`

**File**: `contrib/integration_testing/analysis.go`

Add NAK btree metric validation.

### Step 10.4: Run Integration Tests

```bash
make test-isolation CONFIG=Isolation-Server-IoUringRecv-NAKBtree
make test-parallel CONFIG=Parallel-Starlink-20Mbps
```

---

## Verification Checklist

### After Each Phase

- [ ] `go build ./...` passes
- [ ] `go vet ./...` passes
- [ ] No new linter warnings

### After Phase 8 (Tests)

- [ ] `go test ./...` passes
- [ ] `go test ./... -race` passes
- [ ] All new functions have test coverage

### After Phase 10 (Integration)

- [ ] Isolation test 5a shows 0 false gaps
- [ ] Parallel comparison shows HighPerf ≤ Baseline gaps
- [ ] Metrics are exported correctly
- [ ] `test_flags.sh` passes

---

## Commit Strategy

Each step should be a separate commit with a clear message:

```
Phase 1.1: Add NAK btree config fields to config.go
Phase 1.2: Add NAK btree default values in DefaultConfig()
Phase 1.3: Add NAK btree CLI flags to flags.go
...
Phase 4.5: Rename periodicNAKLocked to periodicNakOriginal
Phase 4.6: Add periodicNakBtree implementation
...
```

This allows easy review, bisection if bugs are found, and rollback of specific steps if needed.

---

## Estimated Effort

| Phase | Estimated Time | Complexity |
|-------|----------------|------------|
| 1. Config & Flags | 2-3 hours | Low |
| 2. Sequence Math | 1-2 hours | Low |
| 3. NAK btree Structure | 2-3 hours | Medium |
| 4. Receiver Integration | 4-6 hours | High |
| 5. Consolidation & FastNAK | 3-4 hours | Medium |
| 6. Sender Modifications | 2-3 hours | Low |
| 7. Metrics | 2-3 hours | Low |
| 8. Unit Tests | 4-6 hours | Medium |
| 9. Benchmarks | 2-3 hours | Low |
| 10. Integration Testing | 3-4 hours | Medium |

**Total**: ~25-40 hours

---

## Related Documents

- `design_nak_btree.md` - Full design specification
- `parallel_defect1_highperf_excessive_gaps.md` - Root cause analysis
- `metrics_analysis_design.md` - Metrics architecture
- `integration_testing_design.md` - Integration test framework

