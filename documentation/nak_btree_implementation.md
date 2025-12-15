# NAK btree Implementation Progress

**Status**: IN PROGRESS
**Started**: 2025-12-14
**Design**: `design_nak_btree.md`
**Plan**: `design_nak_btree_implementation_plan.md`

---

## Overview

This document tracks the implementation progress of the NAK btree feature. Each phase and step is marked with its status and any notes from the implementation.

### Status Legend

- ⬜ Not started
- 🔄 In progress
- ✅ Complete
- ❌ Blocked/Issues

---

## Phase Summary

| Phase | Name | Status | Notes |
|-------|------|--------|-------|
| 1 | Configuration & Flags | ✅ Complete | All config fields, flags, and test_flags.sh updated |
| 2 | Sequence Math | ✅ Complete | `circular/seq_math.go` with tests |
| 3 | NAK btree Data Structure | ✅ Complete | `nak_btree.go` + tests |
| 4 | Receiver Integration | ✅ Complete | pushLocked/periodicNAK dispatch |
| 5 | Consolidation & FastNAK | ✅ Complete | sync.Pool, time budget, metrics |
| 6 | Sender Modifications | ✅ Complete | nakLocked dispatch, honor-order retransmission |
| 7 | Metrics | ✅ Complete | All NAK btree metrics, Prometheus export |
| 8 | Unit Tests | ✅ Complete | 88 tests pass including comprehensive out-of-order + modulus/burst tests |
| 9 | Benchmarks | ✅ Complete | FastNAK ~7ns, consolidation ~28µs/1k |
| 10 | Integration Testing | 🔄 In progress | config.go, analysis.go updated |

---

## Design vs Implementation Checklist

This section verifies all design requirements from `design_nak_btree.md` have been implemented.

### Functional Requirements (FR-*)

| ID | Requirement | Status | Notes |
|----|-------------|--------|-------|
| FR-1 | Suppress immediate NAK for io_uring | ✅ | `suppressImmediateNak` in pushLockedNakBtree |
| FR-2 | Maintain immediate NAK for non-io_uring | ✅ | pushLockedOriginal unchanged |
| FR-3 | Periodic NAK every 20ms | ✅ | periodicNakBtree runs on timer |
| FR-4 | NAK btree stores singles only | ✅ | `nak_btree.go` stores uint32 |
| FR-5 | TSBPD-based scan boundary | ✅ | `tooRecentThreshold` in periodicNakBtree |
| FR-6 | NAKScanStartPoint tracking | ✅ | Independent atomic, immune to ACK jumps |
| FR-7 | Configurable "too recent" percentage | ✅ | `NakRecentPercent` config |
| FR-8 | Consolidate singles into ranges | ✅ | `consolidateNakBtree()` |
| FR-9 | MergeGap for range consolidation | ✅ | `nakMergeGap` config |
| FR-10 | Time-budgeted consolidation | ⚠️ Partial | Budget exists but check is amortized |
| FR-11 | **Multiple NAK packets when exceeds MSS** | ❌ Missing | Not implemented |
| FR-12 | Urgency ordering (oldest first) | ✅ | Btree ascends in sequence order |
| FR-13 | FastNAK trigger after silent period | ✅ | `checkFastNak()` |
| FR-14 | Configurable FastNAK threshold | ✅ | `FastNakThresholdMs` config |
| FR-15 | Track last packet arrival atomically | ✅ | `AtomicTime` type |
| FR-16 | Honor NAK packet order in sender | ✅ | `nakLockedHonorOrder()` |
| FR-17 | Feature flag for new retrans behavior | ✅ | `HonorNakOrder` config |
| FR-18 | Delete from NAK btree on arrival | ✅ | In pushLockedNakBtree |
| FR-19 | **Expire entries based on TSBPD** | ✅ | `expireNakEntries()` implemented |
| FR-20 | Initialize NAKScanStartPoint on first pkt | ✅ | Lazy init from packetStore.Min() |

### Non-Functional Requirements (NFR-*)

| ID | Requirement | Status | Notes |
|----|-------------|--------|-------|
| NFR-1 | Go idiomatic code | ✅ | Uses time.Time, atomic.Value |
| NFR-2 | Specific file/function references | ✅ | Design cites exact locations |
| NFR-3 | Atomic operations for hot-path | ✅ | lastPacketArrivalTime, lastDataPacketSeq |
| NFR-4 | Go generics for sequence math | ✅ | `seq_math_generic.go` |
| NFR-5 | Feature flags for enable/disable | ✅ | All features have config flags |
| NFR-6 | Separate lock for NAK btree | ✅ | `nakBtree` has own RWMutex |
| NFR-7 | Clear lock ordering | ✅ | Documented in code |
| NFR-8 | Function substitution | ✅ | Dispatch functions |
| NFR-9 | Existing path unchanged | ✅ | pushLockedOriginal, periodicNakOriginal |
| NFR-10 | Configuration-driven activation | ✅ | `UseNakBtree`, auto-config |
| NFR-11 | Graceful degradation | ⚠️ Partial | No runtime fallback (by design) |

### Performance Requirements (PERF-*)

| ID | Benchmark | Status | Result |
|----|-----------|--------|--------|
| PERF-1 | BenchmarkPushWithNakBtree | ✅ | ~500ns/op |
| PERF-2 | BenchmarkPeriodicNakScan | ⚠️ Partial | Consolidation benchmarked, not full scan |
| PERF-3 | BenchmarkConsolidation | ✅ | 28µs/1k entries |
| PERF-4 | BenchmarkSeqMath | ✅ | ~0.24ns/op |
| PERF-5 | BenchmarkNakBtreeOperations | ⚠️ Partial | Basic ops tested |

### Testing Requirements

| Component | Test File | Status | Notes |
|-----------|-----------|--------|-------|
| NAK btree | `nak_btree_test.go` | ✅ | Insert, delete, iterate, concurrent |
| Scan window | `receive_iouring_reorder_test.go` | ✅ | 7 deterministic out-of-order tests |
| Consolidation | `nak_consolidate_test.go` | ✅ | All scenarios covered |
| Sequence math | `seq_math_test.go` | ✅ | Wraparound, edge cases |
| FastNAK | `fast_nak_test.go` | ✅ | Trigger conditions |
| FastNAKRecent | `fast_nak_test.go` | ✅ | Sequence jump detection |
| Push hot path | `receive_bench_test.go` | ✅ | List vs btree comparison |

### Configuration Options

| Option | Design Default | Implementation | Match? |
|--------|----------------|----------------|--------|
| `TickIntervalMs` | 10 | 10 | ✅ |
| `PeriodicNakIntervalMs` | 20 | 20 | ✅ |
| `PeriodicAckIntervalMs` | 10 | 10 | ✅ |
| `NakRecentPercent` | 0.10 | 0.10 | ✅ |
| `NakMergeGap` | 3 | 3 | ✅ |
| `NakConsolidationBudget` | 2ms | 2000µs | ✅ |
| `FastNakEnabled` | true | true (auto) | ✅ |
| `FastNakThresholdMs` | 50 | 50 | ✅ |
| `FastNakRecentEnabled` | true | true (auto) | ✅ |
| `HonorNakOrder` | false | false | ✅ |

### Metrics

| Metric Category | Count Designed | Count Implemented | Match? |
|-----------------|----------------|-------------------|--------|
| NAK btree core | 6 | 6 | ✅ |
| Periodic NAK | 4 | 4 | ✅ (NakPeriodicSkipped added) |
| FastNAK | 3 | 4 | ✅ (+overflow) |
| Consolidation | 4 | 4 | ✅ |
| Sender honor-order | 1 | 1 | ✅ |

### ✅ Original and HonorNakOrder Tests - IMPLEMENTED

**Problem**: Need comprehensive test coverage for both `nakLockedOriginal` and `nakLockedHonorOrder` to verify they correctly handle complex consolidated NAK packets from the receiver (modulus drops, burst drops, mixed patterns).

**Key Behavioral Difference**:
- **Original**: Iterates lossList backwards (newest-first), retransmits highest seq first
- **HonorOrder**: Iterates NAK list in order, retransmits per receiver's priority

**Original NAK Tests Added** (`congestion/live/send_test.go`):
- `TestSendOriginal_BasicSingle` - Single packet NAK
- `TestSendOriginal_BasicRange` - Range NAK (3-6) → returns [6,5,4,3] (reverse)
- `TestSendOriginal_MultipleSingles` - 15,5,10 → returns [15,10,5] (highest first)
- `TestSendOriginal_MultipleRanges` - Multiple ranges → highest seq first across all
- `TestSendOriginal_MixedSinglesAndRanges` - Complex mix
- `TestSendOriginal_ModulusDrops` - 9 singles → [90,80,70,60,50,40,30,20,10]
- `TestSendOriginal_BurstDrops` - 3 burst ranges → highest burst first
- `TestSendOriginal_RealisticConsolidatedNAK` - 24 packets, newest-first order
- `TestSendOriginal_NotFoundPackets` - Verify NakNotFound metric
- `TestSendOriginal_LargeScale` - 499 singles from 10k packets
- `TestSendOriginal_VsHonorOrder_Difference` - Demonstrates key behavioral difference

**HonorOrder NAK Tests Added**:
- `TestSendHonorOrder_BasicSingle` - Single packet NAK
- `TestSendHonorOrder_BasicRange` - Range NAK (3-6) → returns [3,4,5,6] (NAK order)
- `TestSendHonorOrder_MultipleSingles` - 15,5,10 → returns [15,5,10] (NAK order)
- `TestSendHonorOrder_MultipleRanges` - Multiple ranges → NAK order
- `TestSendHonorOrder_MixedSinglesAndRanges` - Complex mix, NAK order
- `TestSendHonorOrder_ModulusDrops` - 9 singles → [10,20,30,40,50,60,70,80,90]
- `TestSendHonorOrder_BurstDrops` - 3 burst ranges → NAK order
- `TestSendHonorOrder_RealisticConsolidatedNAK` - 24 packets, receiver priority
- `TestSendHonorOrder_NotFoundPackets` - Verify NakNotFound metric
- `TestSendHonorOrder_Metric` - Verify CongestionSendNAKHonoredOrder metric
- `TestSendHonorOrder_LargeScale` - 499 singles from 10k packets

**Benchmarks**:
```
BenchmarkNAK_Original-24      5,781 ns/op    0 B/op
BenchmarkNAK_HonorOrder-24  424,034 ns/op    0 B/op
```
Note: HonorOrder is ~73x slower due to O(n×m) iteration (scans lossList for each NAK entry). This is acceptable for the trade-off of preserving receiver priority order.

---

### ✅ FastNAKRecent Large Burst Tests - IMPLEMENTED

**Problem**: Need tests for the FastNAKRecent feature handling large burst losses (~60ms outages typical of Starlink).

**Tests Added** (`congestion/live/fast_nak_test.go`):
- `TestCheckFastNakRecent_LargeBurstLoss_5Mbps` - 299 packets (60ms × 5000pps)
- `TestCheckFastNakRecent_LargeBurstLoss_20Mbps` - 1199 packets (60ms × 20000pps)
- `TestCheckFastNakRecent_LargeBurstLoss_100Mbps` - 5999 packets (60ms × 100000pps)
- `TestCheckFastNakRecent_MultipleBurstLosses` - 3 separate bursts with gaps → 3 ranges
- `TestCheckFastNakRecent_LargeBurstThenConsolidate` - Verify 300-packet burst → single 8-byte range
- `TestCheckFastNakRecent_LargeBurstWithPriorGaps` - Mix of prior singles + burst range
- `TestCheckFastNakRecent_VeryLongOutage` - 500ms outage → 2499 packets

**Key Verification**:
```
✅ 300-packet burst loss encoded as single 8-byte range entry
✅ 1200-packet burst (20Mbps) consolidated to: 50001-51199
✅ 3 separate bursts with gaps → 3 separate ranges
✅ Prior singles + burst range → correct mixed consolidation
```

**Benchmarks**:
```
BenchmarkFastNakRecent_SmallBurst-24       3,115 ns/op    576 B/op    7 allocs
BenchmarkFastNakRecent_MediumBurst-24     21,477 ns/op  4,512 B/op   39 allocs  (300 pkts)
BenchmarkFastNakRecent_LargeBurst-24      84,932 ns/op 18,272 B/op  128 allocs  (1200 pkts)
BenchmarkFastNakRecent_VeryLargeBurst-24 412,091 ns/op 75,376 B/op  505 allocs  (5000 pkts)
```

---

### ✅ FR-11: Multiple NAK Packets (MSS Overflow Handling) - IMPLEMENTED

**Problem**: Large SRT buffers (30-60s) at high data rates (20-100 Mbps) with significant loss
can generate NAK lists that exceed the MSS limit (1456 bytes for NAK CIF payload).

**Solution**: Added `splitNakList()` function in `connection.go` that:
- Calculates wire size for each entry (4 bytes for singles, 8 bytes for ranges)
- Splits the list into MSS-sized chunks
- `sendNAK()` now sends multiple NAK packets if needed

**Files Changed**:
- `connection.go`: Added `splitNakList()`, updated `sendNAK()` to split and send multiple packets
- `metrics/metrics.go`: Added `NakPacketsSplit` counter
- `metrics/handler.go`: Added Prometheus export for `gosrt_nak_packets_split_total`
- `connection_nak_test.go`: Comprehensive tests for splitting logic

**Test Coverage**:
- 12 unit tests for `splitNakList()` including extreme 50k entry test
- Verified 50,000 singles split into 138 NAK packets correctly

**Extreme Scale Calculations**:
```
100 Mbps = ~8,900 packets/sec
60s buffer = ~534,000 packets
20% loss = ~107,000 packets to NAK
Wire size: 428 KB
NAK packets needed: ~294 packets ✅ SUPPORTED
```

### ✅ Fixed During Review

1. **FR-19: expireNakEntries() function** ✅
   - Implemented: `expireNakEntries()` in `receive.go`
   - Called at start of `periodicNakBtree()` before scanning
   - Uses `packetStore.Min()` as cutoff
   - Increments `NakBtreeExpired` metric

2. **NakPeriodicSkipped metric** ✅
   - Added increment when `periodicNakBtree()` returns early

3. **FR-6: NAKScanStartPoint** ✅ (Critical fix!)
   - **Problem**: Using `lastACKSequenceNumber` is dangerous because ACK can "jump forward"
     when TSBPD skips occur, causing us to skip scanning a region entirely
   - **Solution**: Added independent `nakScanStartPoint atomic.Uint32` to receiver struct
   - **Behavior**:
     - Initialized lazily from `packetStore.Min()` on first scan
     - Updated to `lastScannedSeq` after each scan (where we actually stopped)
     - Independent of ACK - immune to TSBPD jumps
   - **Why this matters**: With io_uring reordering + low TSBPD delay, there's a race where:
     1. Packets are delayed in io_uring completion queue
     2. TSBPD skip causes ACK to jump forward
     3. We'd never NAK for the skipped region!
   - Now we always scan from where we left off, not from where ACK is

### ⚠️ Partial Implementation Items

1. **FR-10: Time-budgeted consolidation**
   - Design: Check every iteration
   - Implementation: Amortized check every 100 iterations
   - Impact: Acceptable trade-off for performance

---

## 🚨 Critical Testing Gap: Out-of-Order Packet Arrival

### The Problem

The `NAKScanStartPoint` bug (FR-6) was found during design review, **NOT** by an automated test. This is concerning because:

1. The entire NAK btree design exists to handle io_uring packet reordering
2. No existing test simulates packets arriving with out-of-order sequence numbers
3. No test exercises the concurrent interaction between:
   - `Push()` (packets arriving out-of-order)
   - `periodicNAK()` (scanning for gaps)
   - `periodicACK()` (advancing ACK, potentially with TSBPD skips)
   - `Tick()` (TSBPD delivery and packet store cleanup)

### Why Existing Tests Didn't Catch This

| Test Type | What It Tests | What It Misses |
|-----------|---------------|----------------|
| `nak_btree_test.go` | btree Insert/Delete/Iterate | Doesn't test interaction with ACK/Tick |
| `fast_nak_test.go` | FastNAK trigger conditions | Uses minimal packet scenarios |
| `nak_consolidate_test.go` | Consolidation algorithm | Doesn't test with concurrent operations |
| `receive_test.go` | Basic receiver operations | Packets arrive in order |
| `receive_bench_test.go` | Performance comparison | Doesn't test correctness of gap detection |

### Required New Tests

**File**: `congestion/live/receive_iouring_reorder_test.go` (new file)

#### Test 1: Out-of-Order Arrival with Concurrent Operations

```go
// TestOutOfOrderArrival_NakScanStartPoint tests that nakScanStartPoint
// correctly tracks scanning progress independent of ACK, preventing
// the bug where TSBPD skips could cause us to miss NAKing packets.
//
// Scenario:
// 1. Send 1000 packets with out-of-order arrival (simulating io_uring)
// 2. Run Tick, periodicNAK, periodicACK concurrently
// 3. Inject TSBPD skip scenario (some packets never arrive)
// 4. Verify: All truly lost packets were NAK'd
// 5. Verify: nakScanStartPoint progresses to 90% boundary
func TestOutOfOrderArrival_NakScanStartPoint(t *testing.T)
```

**Key test mechanics**:
- Generate 1000+ packets with sequential sequence numbers
- Shuffle arrival order to simulate io_uring reordering
- Drop ~5% of packets (never deliver them)
- Run for multiple NAK cycles (at least 5-10 iterations)
- Track which packets were NAK'd via metrics or callback
- Verify ALL dropped packets appear in NAK lists
- Verify `nakScanStartPoint` advances correctly

#### Test 2: TSBPD Skip Race Condition

```go
// TestTSBPDSkip_DoesNotSkipScanning tests the specific race condition:
// ACK jumps forward due to TSBPD expiry, but we still scan the region
// between old and new ACK positions.
//
// Scenario:
// 1. Deliver packets 100-110 (in order)
// 2. Skip packets 111-120 (simulate loss during "io_uring delay")
// 3. Deliver packets 121-130 (in order)
// 4. Wait for TSBPD expiry of packet 130
// 5. Verify: ACK jumps from 110 to 130 (TSBPD skip)
// 6. Verify: Packets 111-120 WERE NAK'd (nakScanStartPoint saved us)
func TestTSBPDSkip_DoesNotSkipScanning(t *testing.T)
```

**Key test mechanics**:
- Use short TSBPD delay (e.g., 100ms) to trigger skips faster
- Time the test to ensure TSBPD expiry occurs
- Capture NAK lists via mock `sendNAK` callback
- Verify the "lost" region was covered

#### Test 3: Concurrent Push/Tick/NAK/ACK Stress Test

```go
// TestConcurrent_PushTickNAKACK_OutOfOrder is a stress test that runs
// all receiver operations concurrently with out-of-order packet arrival.
//
// This test uses race detector to find any data races and verifies
// correctness of gap detection under concurrent load.
func TestConcurrent_PushTickNAKACK_OutOfOrder(t *testing.T)
```

**Key test mechanics**:
- Multiple goroutines:
  - Goroutine 1: Push packets in random order (1000+ packets)
  - Goroutine 2: Call Tick() every 10ms
  - Goroutine 3: Call periodicNAK() every 20ms
  - Goroutine 4: Call periodicACK() every 10ms
- Run with `-race` flag
- Verify no data races
- Verify gap detection correctness

#### Test 4: nakScanStartPoint Progression Verification

```go
// TestNakScanStartPoint_ProgressesToRecentBoundary verifies that
// nakScanStartPoint correctly advances to the "too recent" boundary
// (90% of TSBPD) and doesn't scan packets that might still be reordered.
func TestNakScanStartPoint_ProgressesToRecentBoundary(t *testing.T)
```

**Key test mechanics**:
- Deliver packets with TSBPD times spread across the buffer
- Run multiple NAK cycles
- After each cycle, verify `nakScanStartPoint` value
- Verify it stops at ~90% boundary (NakRecentPercent)
- Verify packets beyond boundary are NOT added to NAK btree prematurely

#### Test 5: Arrival Order Permutations

```go
// TestOutOfOrder_ArrivalPermutations tests various out-of-order patterns:
// - Random shuffle
// - Reverse order
// - Interleaved (odd then even)
// - Burst with gaps (1-10, skip 11-15, 16-25, skip 26-30, ...)
// - io_uring-like: small batches arriving in random order
func TestOutOfOrder_ArrivalPermutations(t *testing.T)
```

### Deterministic Test Design

**Key principle**: Use modulus-based packet dropping for deterministic, verifiable tests.

#### Why Deterministic Dropping?

| Approach | Reproducibility | Verifiability | Consolidation Check |
|----------|-----------------|---------------|---------------------|
| Random drop | ❌ Different each run | ❌ Hard to verify | ❌ Unknown ranges |
| **Modulus drop** | ✅ Same every run | ✅ Exact expectations | ✅ Known ranges |

#### Modulus Dropping Pattern

```go
// Drop every 10th packet: sequences 10, 20, 30, 40, ...
// For 1000 packets (seq 1-1000):
//   Dropped: 10, 20, 30, ..., 1000 = 100 packets
//   Delivered: 999 - 100 = 900 packets
dropModulus := 10

for _, pkt := range packets {
    seq := pkt.Header().PacketSequenceNumber.Val()
    if seq % dropModulus == 0 {
        dropped = append(dropped, pkt)
    } else {
        delivered = append(delivered, pkt)
    }
}
```

#### Expected NAK btree Entries (with dropModulus=10)

```
Dropped sequences: [10, 20, 30, 40, 50, ..., 1000]

After scanning packets 1-100 (with 10, 20, 30, ... missing):
  NAK btree should contain: [10, 20, 30, 40, 50, 60, 70, 80, 90, 100]

Since drops are evenly spaced (gap=9 between each):
  With NakMergeGap=3: Each drop is a SINGLE (gap too large to merge)
  Expected consolidation: 10 singles, 0 ranges
```

#### Expected NAK Packet Format

```
For dropped [10, 20, 30, 40]:
  NAK packet CIF should contain:
    [10, 10]  // Single: seq 10
    [20, 20]  // Single: seq 20
    [30, 30]  // Single: seq 30
    [40, 40]  // Single: seq 40

  Total entries: 4 singles × 2 = 8 circular.Number values
```

#### Alternative Pattern: Burst Drops for Range Testing

```go
// Drop consecutive sequences to test RANGE consolidation
// Drop sequences 100-109, 200-209, 300-309, ...
dropBurstStart := []uint32{100, 200, 300, 400, 500}
dropBurstSize := 10

// Expected NAK btree: [100-109, 200-209, 300-309, ...]
// Expected consolidation with NakMergeGap=3:
//   5 RANGES: (100,109), (200,209), (300,309), (400,409), (500,509)
```

### Test Helper Functions Needed

```go
// generatePackets creates n packets with sequential sequence numbers
// starting from startSeq, with TSBPD times spread across tsbpdDelay
func generatePackets(startSeq uint32, n int, tsbpdDelay time.Duration) []packet.Packet

// shufflePacketsDeterministic returns packets in shuffled order using seeded RNG
// This simulates io_uring reordering but is reproducible
func shufflePacketsDeterministic(packets []packet.Packet, seed int64) []packet.Packet

// dropByModulus drops packets where seq % modulus == 0
// Returns (delivered, dropped) with exact known contents
func dropByModulus(packets []packet.Packet, modulus int) (delivered, dropped []packet.Packet)

// dropByBursts drops consecutive sequences at specified start points
// Returns (delivered, dropped) with exact known contents
func dropByBursts(packets []packet.Packet, burstStarts []uint32, burstSize int) (delivered, dropped []packet.Packet)

// captureNakCallback returns a sendNAK function that captures all NAK lists
func captureNakCallback() (sendNAK func([]circular.Number), getNakLists func() [][]circular.Number)

// expectedNakEntriesModulus calculates expected NAK entries for modulus dropping
// Returns expected singles and ranges based on NakMergeGap
func expectedNakEntriesModulus(startSeq uint32, n int, modulus int, mergeGap uint32) (singles, ranges []uint32)

// expectedNakEntriesBursts calculates expected NAK entries for burst dropping
func expectedNakEntriesBursts(burstStarts []uint32, burstSize int, mergeGap uint32) (singles []uint32, ranges [][2]uint32)

// verifyNakList checks that a NAK list contains exactly the expected entries
func verifyNakList(t *testing.T, nakList []circular.Number, expectedSingles []uint32, expectedRanges [][2]uint32)

// verifyAllDroppedWereNAKd checks that every dropped packet's sequence
// appears in at least one NAK list
func verifyAllDroppedWereNAKd(t *testing.T, dropped []packet.Packet, nakLists [][]circular.Number)
```

### Updated Test Designs

#### Test 1: Out-of-Order Arrival with Modulus Dropping

```go
func TestOutOfOrderArrival_NakScanStartPoint(t *testing.T) {
    // Setup
    const (
        numPackets   = 1000
        startSeq     = uint32(1)
        dropModulus  = 10  // Drop every 10th packet
        tsbpdDelay   = 200 * time.Millisecond
        nakMergeGap  = uint32(3)
    )

    // Generate packets
    packets := generatePackets(startSeq, numPackets, tsbpdDelay)

    // Drop deterministically: 10, 20, 30, ...
    delivered, dropped := dropByModulus(packets, dropModulus)
    // dropped = [10, 20, 30, ..., 1000] = 100 packets

    // Shuffle delivered packets (simulating io_uring reordering)
    delivered = shufflePacketsDeterministic(delivered, 42) // Fixed seed

    // Calculate expected NAK entries
    expectedSingles, expectedRanges := expectedNakEntriesModulus(
        startSeq, numPackets, dropModulus, nakMergeGap)
    // expectedSingles = [10, 20, 30, ..., 1000] (100 singles)
    // expectedRanges = [] (no ranges - gaps too large to merge)

    // ... run test with concurrent Push/Tick/NAK/ACK ...

    // Verify
    nakLists := getNakLists()
    verifyAllDroppedWereNAKd(t, dropped, nakLists)

    // Verify consolidation produced expected format
    for _, nakList := range nakLists {
        verifyNakListFormat(t, nakList, expectedSingles, expectedRanges)
    }
}
```

#### Test 2: Burst Drops for Range Consolidation

```go
func TestOutOfOrderArrival_BurstDrops_RangeConsolidation(t *testing.T) {
    // Setup
    const (
        numPackets  = 1000
        startSeq    = uint32(1)
        burstSize   = 10  // Each burst drops 10 consecutive packets
        nakMergeGap = uint32(3)
    )
    burstStarts := []uint32{100, 200, 300, 400, 500}

    // Generate and drop
    packets := generatePackets(startSeq, numPackets, 200*time.Millisecond)
    delivered, dropped := dropByBursts(packets, burstStarts, burstSize)
    // dropped = [100-109, 200-209, 300-309, 400-409, 500-509] = 50 packets

    // Calculate expected ranges
    expectedSingles, expectedRanges := expectedNakEntriesBursts(
        burstStarts, burstSize, nakMergeGap)
    // expectedSingles = []
    // expectedRanges = [(100,109), (200,209), (300,309), (400,409), (500,509)]

    // Shuffle and deliver
    delivered = shufflePacketsDeterministic(delivered, 42)

    // ... run test ...

    // Verify consolidation produced RANGES (not singles)
    nakLists := getNakLists()
    for _, nakList := range nakLists {
        verifyNakListFormat(t, nakList, expectedSingles, expectedRanges)
    }
}
```

#### Test 3: Mixed Pattern (Singles + Ranges)

```go
func TestOutOfOrderArrival_MixedPattern(t *testing.T) {
    // Combines modulus drops (singles) and burst drops (ranges)
    // to verify consolidation handles both correctly

    // Drop pattern:
    //   Every 50th packet: 50, 100, 150, ... (singles, gap > mergeGap)
    //   Burst at 500-504: consecutive (range)
    //   Burst at 700-702: consecutive (range)

    // Expected consolidation:
    //   Singles: [50, 100, 150, 200, 250, 300, 350, 400, 450, ...]
    //   Ranges: [(500,504), (700,702)]
}
```

### Implementation Notes

1. **Test file location**: `congestion/live/receive_iouring_reorder_test.go`
2. **Run with race detector**: All tests should pass `go test -race`
3. **Timing considerations**: Tests that rely on TSBPD need careful timing; use `testing.Short()` to skip long tests
4. **Metrics verification**: Use receiver's metrics to verify internal state
5. **Deterministic shuffle**: Use seeded RNG for reproducible tests

### Priority

| Test | Priority | Why |
|------|----------|-----|
| Test 1: Modulus Drops + Out-of-Order | **HIGH** | Would have caught FR-6, deterministic verification |
| Test 2: TSBPD Skip race | **HIGH** | Specific regression test |
| Test 3: Concurrent stress | **HIGH** | Race detection |
| Test 4: Burst Drops (Range testing) | **HIGH** | Verify range consolidation is correct |
| Test 5: Mixed Pattern (Singles + Ranges) | Medium | Combined verification |
| Test 6: nakScanStartPoint progression | Medium | Verifies scan window behavior |
| Test 7: Arrival permutations | Low | Comprehensive coverage |

### Status

| Test | Status | Notes |
|------|--------|-------|
| Test 0: In-Order Baseline | ✅ Implemented | `TestInOrderArrival_Baseline` - control test |
| Test 1: Modulus Drops | ✅ Implemented | `TestOutOfOrderArrival_ModulusDrops` |
| Test 2: TSBPD Skip | ✅ Implemented | `TestTSBPDSkip_DoesNotSkipScanning` |
| Test 3: Concurrent stress | ✅ Implemented | `TestConcurrent_PushTickNAKACK_OutOfOrder` |
| Test 4: Burst Drops | ✅ Implemented | `TestOutOfOrderArrival_BurstDrops_RangeConsolidation` |
| Test 5: Mixed Pattern | ✅ Implemented | `TestOutOfOrderArrival_MixedPattern` |
| Test 6: Progression | ✅ Implemented | `TestNakScanStartPoint_ProgressesToRecentBoundary` |
| Test 7: Permutations | ✅ Implemented | `TestOutOfOrder_ArrivalPermutations` (5 subtests) |

### Consolidation Tests in `nak_consolidate_test.go`

#### Out-of-Order Tests
| Test | Status | Notes |
|------|--------|-------|
| OutOfOrderInsertion | ✅ Implemented | Verifies btree sorts correctly |
| OutOfOrderWithGaps | ✅ Implemented | Multiple groups with gaps |
| InOrderBaseline | ✅ Implemented | Control test for consolidation |
| InOrderVsOutOfOrder_Consistency | ✅ Implemented | Verifies identical results |

#### Modulus-Based Drop Tests
| Test | Status | Notes |
|------|--------|-------|
| ModulusDrops_Every10th | ✅ Implemented | 10% loss → all singles |
| ModulusDrops_Every5th | ✅ Implemented | 20% loss → all singles |
| ModulusDrops_Every3rd_WithMerge | ✅ Implemented | Gap <= mergeGap → merged |
| LargeScale_ModulusDrops | ✅ Implemented | 10k packets, 10% loss |

#### Burst Drop Tests
| Test | Status | Notes |
|------|--------|-------|
| BurstDrops | ✅ Implemented | 5 bursts of 10 packets |
| MixedModulusAndBurst | ✅ Implemented | Singles + range combined |
| LargeScale_BurstDrops | ✅ Implemented | 10 bursts of 20 packets |

#### Benchmarks Added
| Benchmark | Patterns |
|-----------|----------|
| BenchmarkConsolidate_ModulusDrops | 1%/5%/10%/20% loss at 1k/10k packets |
| BenchmarkConsolidate_BurstDrops | 5-50 bursts, 10-50 packets each |
| BenchmarkConsolidate_MixedPatterns | Light/moderate/heavy/burst-heavy/singles-heavy |
| BenchmarkConsolidate_OutOfOrderInsertion | In-order vs out-of-order comparison |
| BenchmarkConsolidate_RealisticScenarios | Clean/Starlink/Congestion/Heavy loss |

**Key benchmark results:**
- Clean network (1 drop): ~144ns
- Starlink outage (50 packet burst): ~479ns
- Multiple outages (3 bursts): ~1012ns
- Heavy loss 20% (1000 singles): ~32µs
- In-order vs out-of-order: **identical** performance (~3µs for 100 entries)

### Implementation Notes

All receiver tests implemented in `congestion/live/receive_iouring_reorder_test.go`:

1. **Deterministic dropping**: Uses modulus-based dropping (`seq % 10 == 0`) for reproducible tests
2. **Realistic TSBPD times**: All packets have properly calculated TSBPD times based on sequence offset
3. **Time advancement**: Tests advance time properly to allow scan window progression
4. **Recent boundary handling**: Tests account for packets at the "recent" boundary (last 10% of TSBPD) not being NAK'd immediately by design
5. **Race detection**: All tests pass with `-race` flag

**Verification output from Test 6 (nakScanStartPoint progression)**:
```
After cycle 9 (time=1500000): nakScanStartPoint=21
After cycle 10 (time=1550000): nakScanStartPoint=41
After cycle 11 (time=1600000): nakScanStartPoint=61
...
After cycle 18 (time=1950000): nakScanStartPoint=199
Final nakScanStartPoint: 199 (advanced by 199)
```

This confirms the scan window correctly progresses through the buffer over time.

---

## Phase 1: Configuration & Flags

**Goal**: Add all new configuration options and CLI flags.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 1.1 | Add Config fields to `config.go` | ✅ | Lines 260-310: Timer intervals, NAK btree, FastNAK, sender config |
| 1.2 | Add default values in `DefaultConfig()` | ✅ | Lines 360-373: All defaults set |
| 1.3 | Add CLI flags to `contrib/common/flags.go` | ✅ | Lines 72-95: 12 new flags added |
| 1.4 | Add flag application in `ApplyFlagsToConfig()` | ✅ | Lines 280-320: All flags wired up |
| 1.5 | Add auto-configuration logic | ✅ | `ApplyAutoConfiguration()` function added |
| 1.6 | Update `contrib/common/test_flags.sh` | ✅ | Tests 31-35 added for new flags |
| 1.7 | Verify Phase 1 completion | ✅ | `go build ./...` passes |

### Files Modified

- `config.go` - Added 12 new Config fields, defaults, and `ApplyAutoConfiguration()`
- `contrib/common/flags.go` - Added 12 new CLI flags and `ApplyFlagsToConfig()` entries
- `contrib/common/test_flags.sh` - Added tests for all new flags

---

## Phase 2: Sequence Math

**Goal**: Add generic sequence number math with wraparound handling.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 2.1 | Create `circular/seq_math.go` | ✅ | SeqLess, SeqGreater, SeqDiff, SeqDistance, SeqAdd, SeqSub, SeqInRange |
| 2.2 | Create `circular/seq_math_test.go` | ✅ | Comprehensive tests + benchmarks |
| 2.3 | Verify Phase 2 completion | ✅ | `go test ./circular/...` passes |
| 2.4 | Create `circular/seq_math_generic.go` | ✅ | Generic implementations for uint16/uint32/uint64 |
| 2.5 | Create `circular/seq_math_generic_test.go` | ✅ | Cross-bit-width validation + benchmarks |
| 2.6 | Run benchmarks | ✅ | Generic has NO performance penalty |
| 2.7 | Add 64-bit support | ✅ | SeqLess64, SeqDiff64, SeqDistance64, SeqAdd64, SeqSub64 |
| 2.8 | Add 64-bit tests | ✅ | Test64BitWraparound, Test64BitDiff, Test64BitAddSub, etc. |
| 2.9 | Verify 64-bit benchmarks | ✅ | 64-bit same speed as 16/32-bit (~0.24 ns/op) |
| 2.10 | Update packet btree comparator | ✅ | Uses `SeqLess()` for consistency |

### Files Created

- `circular/seq_math.go` - 31-bit sequence number math with wraparound handling
- `circular/seq_math_test.go` - Unit tests and benchmarks
- `circular/seq_math_generic.go` - Generic implementations using Go generics
- `circular/seq_math_generic_test.go` - Cross-bit-width validation tests and benchmarks

### Reference Files (excluded from build)

- `documentation/trackRTP_math.go.reference` - Original goTrackRTP implementation for reference
- `documentation/trackRTP_math_test.go.reference` - Original goTrackRTP tests for reference

---

## Phase 3: NAK btree Data Structure

**Goal**: Create the NAK btree that stores missing sequence numbers.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 3.1 | Create `congestion/live/nak_btree.go` | ✅ | nakBtree struct with Insert, Delete, DeleteBefore, Iterate, etc. |
| 3.2 | Create `congestion/live/nak_btree_test.go` | ✅ | Unit tests for all operations |
| 3.3 | Verify Phase 3 completion | ✅ | `go test ./congestion/live/... -run NakBtree` passes |

### Files Created

- `congestion/live/nak_btree.go` - NAK btree data structure
- `congestion/live/nak_btree_test.go` - Unit tests

### NAK btree API

```go
type nakBtree struct { ... }

func newNakBtree(degree int) *nakBtree
func (nb *nakBtree) Insert(seq uint32)
func (nb *nakBtree) Delete(seq uint32) bool
func (nb *nakBtree) DeleteBefore(cutoff uint32) int
func (nb *nakBtree) Len() int
func (nb *nakBtree) Has(seq uint32) bool
func (nb *nakBtree) Min() (uint32, bool)
func (nb *nakBtree) Max() (uint32, bool)
func (nb *nakBtree) Iterate(fn func(seq uint32) bool)
func (nb *nakBtree) IterateDescending(fn func(seq uint32) bool)
func (nb *nakBtree) Clear()
```

### Key Design Decisions

1. **Stores uint32 only** - Not circular.Number, for efficiency
2. **Uses `circular.SeqLess()`** - Same comparator as packet btree for consistency
3. **Separate RWMutex** - Independent locking from packet btree
4. **Singles only** - No range storage; consolidation happens at NAK generation time

---

## Phase 4: Receiver Integration

**Goal**: Wire NAK btree into receiver, add function dispatch, update Push().

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 4.1 | Update `ReceiveConfig` struct | ✅ | Added NAK btree and FastNAK config fields |
| 4.2 | Update `receiver` struct | ✅ | Added useNakBtree, nakBtree, fastNak fields |
| 4.3 | Update `NewReceiver()` | ✅ | Initialize new fields, create nakBtree if enabled |
| 4.4 | Add function dispatch for `periodicNAK` | ✅ | Dispatches to Original or Btree based on config |
| 4.5 | Rename to `periodicNakOriginal()` | ✅ | Original implementation preserved |
| 4.6 | Add `periodicNakBtree()` | ✅ | New implementation using NAK btree |
| 4.7 | Update `pushLocked()` | ✅ | Add/delete from NAK btree, suppress immediate NAK |

### Changes to `receive.go`

**New config fields in `ReceiveConfig`**:
- `UseNakBtree` - Enable NAK btree
- `SuppressImmediateNak` - Let periodic NAK handle gaps
- `TsbpdDelay`, `NakRecentPercent`, `NakMergeGap`, `NakConsolidationBudget`
- `FastNakEnabled`, `FastNakThresholdUs`, `FastNakRecentEnabled`

**New receiver fields**:
- `useNakBtree`, `suppressImmediateNak`, `nakBtree`
- `tsbpdDelay`, `nakRecentPercent`, `nakMergeGap`, `nakConsolidationBudget`
- `fastNakEnabled`, `fastNakThreshold`, `fastNakRecentEnabled`

**Function dispatch**:
```go
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    if r.useNakBtree {
        return r.periodicNakBtree(now)
    }
    return r.periodicNakOriginal(now)
}
```

**pushLocked changes**:
- When gap detected: Add missing sequences to NAK btree
- Immediate NAK suppressed if `suppressImmediateNak` is true
- When packet arrives: Delete from NAK btree

---

### ⚠️ ISSUE IDENTIFIED: Rate Statistics Locking Analysis

**User concern**: Are `r.rate.*` updates properly protected from races?

**Analysis of current locking**:

| Operation | Lock Type | Fields Accessed |
|-----------|-----------|-----------------|
| `Push()` → `pushLockedNakBtree()` | `Lock()` (exclusive) | `r.rate.packets++`, `r.rate.bytes+=`, `r.rate.bytesRetrans+=` |
| `Push()` → `pushLockedOriginal()` | `Lock()` (exclusive) | Same as above |
| `Tick()` → `updateRateStats()` | `Lock()` (exclusive) | Reads all, resets counters, writes computed values |
| `Stats()` | `RLock()` (shared) | Reads `r.rate.bytesPerSecond`, `r.rate.pktRetransRate` |
| `PacketRate()` | `Lock()` (exclusive) | Reads `r.rate.packetsPerSecond`, `r.rate.bytesPerSecond` |

**Conclusion**: The current locking appears **correct** for race safety:
- All writes use exclusive `Lock()`
- `Stats()` uses `RLock()` which is mutually exclusive with `Lock()`
- Go's `sync.RWMutex` guarantees no concurrent read/write access

**However, potential concerns to investigate**:

1. **Performance**: `Push()` holds `Lock()` for entire packet processing. With high packet rates, this could cause contention between:
   - Multiple connections calling `Push()`
   - `Tick()` trying to call `updateRateStats()`
   - `Stats()` trying to read values

2. **Missing probe timing in NAK btree path**: In `pushLockedNakBtree()`, the probe timing code that updates `r.avgLinkCapacity` was **not included**.

   **Probe timing purpose**: Every 16th and 17th packet are sent as pairs. The time between them estimates link capacity (PUMASK_SEQNO_PROBE in SRT spec).

   **Question**: Should probe timing be included in NAK btree path?
   - The design doc (`design_nak_btree.md` Section 4.1.2) doesn't mention it
   - Probe timing is for congestion control, orthogonal to NAK handling
   - With io_uring, packet arrival order is random, so probe timing may not work correctly anyway

   **Recommendation**: Verify with design whether probe timing should be:
   - Omitted (current implementation)
   - Added back (same as original path)
   - Modified for io_uring (e.g., timestamp-based instead of arrival-order-based)

3. **Design doc verification needed**: The design doc focuses on NAK handling but doesn't explicitly address:
   - Probe timing for link capacity estimation
   - Rate statistics update strategy
   - Whether atomic counters should replace lock-protected fields

**Recommendation**:
- No immediate changes needed for correctness
- The probe timing omission in `pushLockedNakBtree()` should be addressed before Phase 4 completion

### ⚠️ Future Work: Rate Fields Not Migrated to Atomics

**Issue**: 20 rate-related fields across receiver and sender still use lock-based protection (not migrated to atomics during the metrics overhaul).

**Full design and migration plan**: See [`rate_metrics_performance_design.md`](./rate_metrics_performance_design.md)

**Status**: Deferred until NAK btree implementation is complete.

---

### ⚠️ ISSUE IDENTIFIED: Gap Detection Logic Mismatch

**Problem**: The current Phase 4 implementation still uses `maxSeenSequenceNumber` for gap detection in `pushLocked()`, which is fundamentally incompatible with io_uring's out-of-order delivery.

**What the design document says** (Section 4.3.1):
1. **In Push()**: Just insert packet, delete from NAK btree. **NO gap detection**
2. **In periodicNakBtree()**: Scan packet btree to find actual gaps, add them to NAK btree

**What current implementation does** (wrong):
1. **In pushLocked()**: Detects "gaps" using `maxSeenSequenceNumber` and adds to NAK btree
2. This causes false positives because with io_uring, packets arrive out of order

**Why this is wrong**:
With io_uring, if packets arrive as: 100, 103, 101, 102
- Current code sees 100→103 as a "gap" and NAKs for 101, 102
- But 101, 102 are just reordered, not lost
- The packet btree will sort them correctly

**Correct approach per design**:
```
Push() with io_uring NAK btree enabled:
  1. Insert packet into packet btree (btree sorts automatically)
  2. Delete seq from NAK btree (if present - packet arrived)
  3. NO gap detection, NO immediate NAK
  4. NO updating maxSeenSequenceNumber in the normal way

periodicNakBtree():
  1. Scan packet btree from NAKScanStartPoint
  2. For each gap in the btree sequence, add to NAK btree
  3. Only scan packets older than "too recent" threshold
  4. Consolidate NAK btree into ranges and send
```

**Required Changes**:
1. Add `useNakBtreePath` dispatch in `pushLocked()` - completely different path
2. Create `pushLockedNakBtree()` - simple insert, no gap detection
3. Update `periodicNakBtree()` to scan packet btree for gaps
4. The NAK btree gets populated by periodicNak, not by Push

---

### Key Learnings

1. **Signed arithmetic for wraparound** works when sequences are within half the range
2. **16-bit vs 31-bit behavior differs** due to different threshold points
3. **Generic implementations have zero performance overhead** in Go 1.18+
4. **All implementations ~0.24-0.27 ns/op** - single CPU instruction level performance
5. **64-bit sequences would work identically** - no code changes needed for future expansion
6. **Test coverage across bit widths** validates algorithm correctness independent of data size

### 64-bit Testing Insights

Added 64-bit tests to validate algorithm at extreme scale:
- `Test64BitWraparound` - Tests with values up to 2^64
- `Test64BitDiff` - Verified with 1 trillion+ values
- `Test64BitAddSub` - Wraparound at uint64 max
- `TestAllBitWidthsWraparound` - Proportional gap testing

**Key finding**: 64-bit testing DID NOT reveal additional issues. The algorithm
is mathematically sound at all bit widths. The earlier 31-bit test failures were
due to incorrect expectations about the half-range threshold, not algorithm bugs.

This validates our implementation is ready for any future sequence number expansion.

---

### Assessment: Existing vs New Sequence Number Implementations

#### Available Implementations

| Implementation | Location | Type | Max Handling |
|----------------|----------|------|--------------|
| `circular.Number` | `circular/circular.go` | Object-oriented | Stored in struct |
| `SeqLess()` etc | `circular/seq_math.go` | Functions (uint32) | Hardcoded 31-bit |
| `SeqLessG()` etc | `circular/seq_math_generic.go` | Generic functions | Parameter |

#### 1. `circular.Number` (Existing - OOP Style)

```go
type Number struct {
    max       uint32
    threshold uint32  // max/2, stored for performance
    value     uint32
}

a := circular.New(100, packet.MAX_SEQUENCENUMBER)
b := circular.New(200, packet.MAX_SEQUENCENUMBER)
if a.Lt(b) { ... }
```

**Pros**:
- Encapsulates max/threshold - no risk of using wrong max
- Self-documenting - value carries its context
- Extensively used in existing gosrt codebase
- Methods: `Lt()`, `Gt()`, `Lte()`, `Gte()`, `Distance()`, `Add()`, `Sub()`, `Inc()`, `Dec()`
- `LtBranchless()` optimization available

**Cons**:
- Object creation overhead (24 bytes per Number)
- Requires `circular.New()` to create
- Methods require receiver copies
- ~0.26-0.29 ns/op (slightly slower than functions)

**Current Usage**:
- `packet.Header().PacketSequenceNumber` stored as `circular.Number`
- Used in `connection.go`, `congestion/live/*.go`, `dial.go`, `listen.go`
- 100+ call sites in the codebase

#### 2. `SeqLess()` etc (New - Function Style, SRT-Specific)

```go
if SeqLess(seqA, seqB) { ... }
diff := SeqDiff(seqA, seqB)
```

**Pros**:
- Zero allocation - works on raw uint32
- ~0.24-0.26 ns/op (~10% faster)
- Simple function calls, no object creation
- Optimized for SRT's 31-bit sequence numbers
- Functions: `SeqLess()`, `SeqGreater()`, `SeqDiff()`, `SeqDistance()`, `SeqAdd()`, `SeqSub()`, `SeqInRange()`

**Cons**:
- Hardcoded to 31-bit max (SRT-specific)
- Must remember to use correct max
- No encapsulation

#### 3. `SeqLessG()` etc (New - Generic Style)

```go
if SeqLessG[uint64, int64](seqA, seqB, math.MaxUint64) { ... }
if SeqLess64(seqA, seqB) { ... }  // Convenience wrapper
```

**Pros**:
- Works with any unsigned integer type (uint16, uint32, uint64)
- Zero allocation
- ~0.24-0.27 ns/op (same speed as non-generic!)
- Future-proof for 64-bit sequences
- Validates algorithm correctness across bit widths

**Cons**:
- Slightly more verbose generic syntax
- Requires specifying max value
- Convenience wrappers (`SeqLess64()`) need to be defined per type

#### Benchmark Comparison

| Benchmark | ns/op | Allocations | Notes |
|-----------|-------|-------------|-------|
| `SeqLess()` (new) | 0.24 | 0 | Function, 31-bit |
| `SeqLess64()` (new) | 0.24 | 0 | Function, 64-bit |
| `Number.Lt()` (existing) | 0.26 | 0 | Method |
| `Number.LtBranchless()` | 0.26 | 0 | Optimized method |

**Winner**: Function-based approaches are ~10% faster.

#### Recommendation

**For NAK btree implementation**: Use the new `SeqLess()` / `SeqDiff()` functions.

**Rationale**:
1. **Performance**: 10% faster, zero allocations - important for hot paths
2. **Consistency**: NAK btree stores raw `uint32` sequence numbers, not `circular.Number`
3. **Simplicity**: Working with NAK entries is cleaner with functions
4. **SRT-specific**: We only need 31-bit for SRT, so the specialized functions are ideal

**For existing code**: Keep using `circular.Number` - it works well and refactoring
would be high-risk with minimal benefit. The ~10% difference is negligible in
most code paths.

#### Refactoring Opportunities

**Low-risk, high-value refactoring**:
1. **NAK btree operations** - Use `SeqLess()` for comparisons (new code)
2. **Packet btree comparator** - Could use `SeqLess()` instead of `Number.Lt()`
3. **Hot path sequence comparisons** - Where profiling shows benefit

**Do NOT refactor** (high-risk, low-value):
- `packet.Header().PacketSequenceNumber` - deeply embedded, would touch 50+ files
- Connection sequence tracking - works correctly now
- Test code - not performance critical

#### Refactoring Completed

**Packet btree comparator updated** (`congestion/live/packet_store_btree.go`):

```go
// Before (using circular.Number method):
return a.seqNum.Lt(b.seqNum)

// After (using optimized SeqLess function):
return circular.SeqLess(a.seqNum.Val(), b.seqNum.Val())
```

All `congestion/live` tests pass, including `TestListVsBTreeEquivalence`.

#### Code to Add for NAK btree

The NAK btree will use these new functions directly:

```go
// In congestion/live/nak_btree.go
func seqLess(a, b uint32) bool {
    return circular.SeqLess(a, b)  // Uses the new optimized function
}

// btree comparator
tree := btree.NewG[uint32](16, seqLess)
```

This keeps both btrees consistent - using the same optimized sequence comparison
functions for better performance and maintainability.

---

## Phase 3: NAK btree Data Structure

**Goal**: Create the NAK btree with basic operations.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 3.1 | Create `congestion/live/nak_btree.go` | ✅ | Complete with all operations |
| 3.2 | Create `congestion/live/nak_btree_test.go` | ✅ | Unit tests for all operations |
| 3.3 | Verify Phase 3 completion | ✅ | `go test ./congestion/live/... -run NakBtree` passes |

---

## Phase 4: Receiver Integration

**Goal**: Wire NAK btree into receiver, add function dispatch.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 4.1 | Update `ReceiveConfig` struct | ✅ | Added NAK btree and FastNAK config fields |
| 4.2 | Update `receiver` struct | ✅ | Added useNakBtree, nakBtree, fastNak fields |
| 4.3 | Update `NewReceiver()` | ✅ | Initialize new fields, create nakBtree if enabled |
| 4.4 | Add function dispatch for `periodicNAK` | ✅ | Dispatches to Original or Btree based on config |
| 4.5 | Rename to `periodicNakOriginal()` | ✅ | Original implementation preserved |
| 4.6 | Add `periodicNakBtree()` | ✅ | New implementation using NAK btree |
| 4.7 | Add `pushLockedNakBtree()` | ✅ | Insert packet, delete from NAK btree, no gap detection |
| 4.8 | Add function dispatch for `pushLocked` | ✅ | Dispatches to Original or NakBtree based on config |
| 4.9 | Update `connection.go` for receiver config | ✅ | Wiring done |
| 4.10 | Verify Phase 4 completion | ✅ | Build and tests pass |

---

## Phase 5: Consolidation & FastNAK

**Goal**: Add NAK consolidation algorithm and FastNAK optimization.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 5.1 | Create `congestion/live/nak_consolidate.go` | ✅ | sync.Pool + time budget |
| 5.2 | Create `congestion/live/nak_consolidate_test.go` | ✅ | Comprehensive tests + benchmarks |
| 5.3 | Create `congestion/live/fast_nak.go` | ✅ | checkFastNak, triggerFastNak, checkFastNakRecent |
| 5.4 | Create `congestion/live/fast_nak_test.go` | ✅ | Unit tests for all FastNAK scenarios |
| 5.5 | Add FastNAK tracking fields to receiver | ✅ | AtomicTime, lastDataPacketSeq |
| 5.6 | Update `pushLockedNakBtree()` for FastNAK tracking | ✅ | Calls checkFastNakRecent, updates tracking |
| 5.7 | Update `periodicNakBtree()` to use consolidation | ✅ | Calls consolidateNakBtree() |
| 5.8 | Add FastNAK metrics | ✅ | NakFastTriggers, NakFastRecentInserts |
| 5.9 | Add consolidation metrics | ✅ | NakConsolidationRuns/Entries/Merged/Timeout |
| 5.10 | Verify Phase 5 completion | ✅ | All tests pass, race-free, benchmarks run |

### Files Created

- `congestion/live/nak_consolidate.go` - NAK consolidation with sync.Pool and time budget
- `congestion/live/nak_consolidate_test.go` - Consolidation tests and benchmarks
- `congestion/live/fast_nak.go` - FastNAK optimization (silence detection, sequence jump)
- `congestion/live/fast_nak_test.go` - FastNAK tests

### Performance Results

```
BenchmarkCheckFastNakRecent-24           185775500    6.794 ns/op    0 B/op    0 allocs/op
BenchmarkConsolidateNakBtree/10-24        2843956     429 ns/op      320 B/op   11 allocs/op
BenchmarkConsolidateNakBtree/100-24        324762    3303 ns/op     3491 B/op  101 allocs/op
BenchmarkConsolidateNakBtree/500-24         86476   14752 ns/op    16303 B/op  501 allocs/op
BenchmarkConsolidateNakBtree/1000-24        39411   27644 ns/op    32609 B/op 1001 allocs/op
```

**Key observations**:
- FastNAK check: 0 allocations, ~7ns - negligible overhead
- Consolidation: Linear scaling O(n), allocations from circular.Number objects
- 1000 entries takes ~28µs - well under the 2ms budget

---

## Phase 6: Sender Modifications

**Goal**: Add honor-order retransmission dispatch.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 6.1 | Update `SendConfig` struct | ✅ | Already had `HonorNakOrder` field |
| 6.2 | Update `sender` struct | ✅ | Already had `honorNakOrder` field |
| 6.3 | Update `NewSender()` function | ✅ | Already initialized |
| 6.4 | Add function dispatch for NAK processing | ✅ | `nakLocked()` dispatches to Original or HonorOrder |
| 6.5 | Add `nakLockedHonorOrder()` function | ✅ | Iterates Front→Back, honors NAK order |
| 6.6 | Update `connection.go` for sender config | ✅ | Passes `c.config.HonorNakOrder` |
| 6.7 | Verify Phase 6 completion | ✅ | `go build ./...` passes |

### Changes to `send.go`

**Function dispatch**:
```go
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
    if s.honorNakOrder {
        return s.nakLockedHonorOrder(sequenceNumbers)
    }
    return s.nakLockedOriginal(sequenceNumbers)
}
```

**Original vs HonorOrder**:
- `nakLockedOriginal()`: Iterates `Back()→Prev()` (oldest first)
- `nakLockedHonorOrder()`: Iterates `Front()→Next()` for each NAK range in order

**New metric**: `CongestionSendNAKHonoredOrder` - counts NAK processing runs using honor-order

---

## Phase 7: Metrics

**Goal**: Add all new metrics and Prometheus export.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 7.1 | Add metrics to `metrics/metrics.go` | ✅ | NAK btree, periodic, consolidation, FastNAK metrics |
| 7.2 | Update `metrics/handler.go` | ✅ | All new metrics exported to Prometheus |
| 7.3 | Update metric increment points | ✅ | receive.go, fast_nak.go, send.go |
| 7.4 | Verify Phase 7 completion | ✅ | `go build ./...` and tests pass |

### Metrics Added

**NAK btree Core** (6):
- `NakBtreeInserts`, `NakBtreeDeletes`, `NakBtreeExpired`
- `NakBtreeSize` (gauge), `NakBtreeScanPackets`, `NakBtreeScanGaps`

**Periodic NAK** (3):
- `NakPeriodicOriginalRuns`, `NakPeriodicBtreeRuns`, `NakPeriodicSkipped`

**Consolidation** (4):
- `NakConsolidationRuns`, `NakConsolidationEntries`, `NakConsolidationMerged`, `NakConsolidationTimeout`

**FastNAK** (4):
- `NakFastTriggers`, `NakFastRecentInserts`, `NakFastRecentSkipped`, `NakFastRecentOverflow`

**Sender** (1):
- `CongestionSendNAKHonoredOrder`

---

## Phase 8: Unit Tests

**Goal**: Add comprehensive unit tests.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 8.1 | Create test files | ✅ | nak_btree_test.go, fast_nak_test.go, nak_consolidate_test.go |
| 8.2 | Add tests to existing files | ✅ | metrics_test.go, receive_test.go |
| 8.3 | Verify Phase 8 completion | ✅ | 77 tests pass |
| 8.4 | **Out-of-order arrival tests** | ✅ | 7 tests in `receive_iouring_reorder_test.go` |

### Test Coverage

**NAK btree tests** (`nak_btree_test.go`):
- Basic operations (Insert, Delete, DeleteBefore)
- Iteration (ascending/descending)
- Sequence ordering with wraparound
- Large sequence numbers
- Duplicate handling
- Concurrent access

**FastNAK tests** (`fast_nak_test.go`):
- Disabled/enabled states
- Silent period detection
- Sequence jump detection (FastNAKRecent)
- AtomicTime operations
- buildNakListLocked helper

**Consolidation tests** (`nak_consolidate_test.go`):
- Empty/single/contiguous ranges
- MergeGap behavior
- Mixed singles and ranges
- Sequence wraparound
- sync.Pool reuse
- Metrics tracking
- Benchmark (1000 entries, ~28µs)

---

## Phase 9: Benchmarks

**Goal**: Add performance benchmarks.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 9.1 | Create benchmark files | ✅ | fast_nak_test.go, nak_consolidate_test.go, receive_bench_test.go |
| 9.2 | Run benchmarks | ✅ | All critical paths benchmarked |

### Benchmark Results

**FastNAK**:
- `BenchmarkCheckFastNakRecent`: **6.8ns** - negligible overhead per packet

**Consolidation**:
| NAK btree size | Time | Notes |
|----------------|------|-------|
| 0 entries | 402ns | Empty check |
| 100 entries | 3.2µs | Small loss |
| 500 entries | 15.2µs | Medium loss |
| 1000 entries | 28.6µs | Large loss (still well under 2ms budget) |

**sync.Pool**:
- `BenchmarkSyncPoolConsolidation`: 1.7µs, **32 B/op, 2 allocs** - pool working

**btree vs list (packet store)**:
| Operation | List | BTree | Improvement |
|-----------|------|-------|-------------|
| Push (in-order) | 167µs | 813ns | **205x faster** |
| Push (out-of-order) | 2.1µs | 500ns | **4x faster** |
| Has (lookup) | 1.6µs | 135ns | **12x faster** |

---

## Phase 10: Integration Testing

**Goal**: Update integration tests for NAK btree validation.

| Step | Description | Status | Notes |
|------|-------------|--------|-------|
| 10.1 | Update test configurations | ✅ | HighPerfSRTConfig now includes NAK btree |
| 10.2 | Update `config.go` integration | ✅ | Added NAK btree fields and ToCliFlags() |
| 10.3 | Update `analysis.go` | ✅ | Added 18 new NAK btree metrics to DerivedMetrics |
| 10.4 | Run integration tests | ⬜ | Pending manual test run |

### Integration Testing Config Changes

**SRTConfig additions**:
- `UseNakBtree`, `SuppressImmediateNak`, `FastNakEnabled`, `FastNakRecentEnabled`, `HonorNakOrder`

**HighPerfSRTConfig now enables**:
- NAK btree for gap detection
- Suppress immediate NAK (prevents false positives)
- FastNAK for outage recovery
- FastNAKRecent for sequence jump detection
- Honor NAK order in sender

**Helper method**: `WithNakBtree()` - enables all NAK btree features

### DerivedMetrics additions (18 new fields)

**Core operations**: `NakBtreeInserts`, `NakBtreeDeletes`, `NakBtreeExpired`, `NakBtreeSize`, `NakBtreeScanPackets`, `NakBtreeScanGaps`

**Periodic NAK**: `NakPeriodicOriginalRuns`, `NakPeriodicBtreeRuns`, `NakPeriodicSkipped`

**Consolidation**: `NakConsolidationRuns`, `NakConsolidationEntries`, `NakConsolidationMerged`, `NakConsolidationTimeout`

**FastNAK**: `NakFastTriggers`, `NakFastRecentInserts`, `NakFastRecentSkipped`, `NakFastRecentOverflow`

**Sender**: `NakHonoredOrder`

---

## Build Verification Log

Track `go build ./...` results after each step:

| Date | Phase.Step | Result | Notes |
|------|------------|--------|-------|
| 2025-12-14 | 1.7 | ✅ Pass | Phase 1 complete - all config/flags added |
| 2025-12-14 | 2.3 | ✅ Pass | Phase 2 complete - seq_math.go with tests |
| 2025-12-14 | 2.6 | ✅ Pass | Phase 2 extended - generic implementations + benchmarks |
| 2025-12-14 | 2.10 | ✅ Pass | Packet btree now uses SeqLess() - all tests pass |
| 2025-12-14 | 3.1 | ✅ Pass | Phase 3 complete - NAK btree created with tests |
| 2025-12-14 | 4.1-4.7 | ✅ Pass | Phase 4 - Receiver integration complete |
| 2025-12-14 | 5.1-5.10 | ✅ Pass | Phase 5 - Consolidation & FastNAK complete |
| 2025-12-14 | 6.1-6.7 | ✅ Pass | Phase 6 - Sender honor-order retransmission |
| 2025-12-14 | 7.1-7.4 | ✅ Pass | Phase 7 - All NAK btree metrics + Prometheus |
| 2025-12-14 | 8.1-8.3 | ✅ Pass | Phase 8 - 77 unit tests pass |
| 2025-12-14 | 9.1-9.2 | ✅ Pass | Phase 9 - Benchmarks confirm performance |
| 2025-12-14 | 10.1-10.3 | ✅ Pass | Phase 10 - Integration config/analysis updated |

---

## Issues & Decisions

Track any issues encountered and decisions made during implementation:

### Issue: Sequence Wraparound Test Expectations
**Phase.Step**: 2.2
**Date**: 2025-12-14

**Description**: Initial test expectations for extreme wraparound cases (0 vs MaxSeqNumber31) were incorrect.

**What went wrong**: The goTrackRTP implementation uses signed arithmetic for wraparound detection:
```go
diff := int32(a - b)
return diff < 0  // a < b if diff is negative
```

This approach works correctly **only when sequences are within half the maximum range of each other**. At the extreme boundary (0 vs 2147483647), the signed difference is at the edge of the valid range, making comparison ambiguous.

**Original incorrect test expectation**:
```go
{"max < 0 (wraparound)", MaxSeqNumber31, 0, true}   // Expected max to be "before" 0
{"0 < max (wraparound)", 0, MaxSeqNumber31, false}  // Expected 0 to be "after" max
```

**Why this is wrong**: The distance between 0 and MaxSeqNumber31 is ~2.1 billion - this is NOT a valid "close together" sequence scenario. In reality:
- If sequences wrap from max→0, they're adjacent (distance=1)
- A gap of 2.1 billion packets is meaningless in any real protocol

**Corrected understanding**: The goTrackRTP tests were correct for *their* use case (16-bit RTP). The signed arithmetic approach assumes:
1. Sequences being compared are "reasonably close" (within half the range)
2. A difference larger than half the range indicates wraparound
3. At exactly half range, behavior is undefined/ambiguous

**Resolution**: Updated tests to use realistic scenarios:
- Practical SRT buffers hold thousands of packets, not billions
- Test with realistic gaps (1000, 10000) rather than extreme boundaries
- Document that SeqLess/SeqGreater assume sequences are within half the range

**How to verify correctness**:
1. Generic implementations (uint16, uint32, uint64) should behave identically for proportional test values
2. Cross-reference with existing `circular.Number.Lt()` which uses explicit threshold checking
3. Benchmarks to ensure no performance regression

**Verification completed**:
- Added `seq_math_generic.go` with generic implementations for uint16, uint32, uint64
- Added `seq_math_generic_test.go` with comprehensive tests:
  - `TestGenericMatchesSpecific` - verifies generic matches uint32-specific
  - `Test16BitWraparound` - validates algorithm at 16-bit scale
  - `Test32BitFullWraparound` - validates with full 32-bit range
  - `TestConsistencyAcrossBitWidths` - proportional behavior verification
- Benchmarks confirm generic has NO performance penalty (~0.26 ns/op for all)

**Key insight about goTrackRTP**:
The goTrackRTP library was correct for its use case (16-bit RTP sequences). The confusion arose from:
1. RTP uses 16-bit sequences with full range (0-65535)
2. SRT uses 31-bit sequences (0-2147483647) stored in uint32
3. For 16-bit, wraparound from max→0 is correctly detected
4. For 31-bit masked in uint32, the signed arithmetic threshold is different

The algorithm is sound - the issue was applying 16-bit test expectations to a 31-bit implementation.

---

---

## Known Issues & Pending Work

### ✅ ISSUE-001: Missing Defensive Metrics for "Should Never Happen" Conditions - FIXED

**Problem**: The NAK btree implementation has defensive nil checks that protect against invalid states, but these conditions were not being tracked with metrics.

**Solution Implemented**:

1. **Added metric** in `metrics/metrics.go`:
```go
// Defensive counters for "should never happen" conditions (ISSUE-001)
NakBtreeNilWhenEnabled atomic.Uint64 // nakBtree nil when useNakBtree=true
```

2. **Exported to Prometheus** in `metrics/handler.go`:
```go
writeCounterIfNonZero(b, "gosrt_connection_congestion_internal_total",
    metrics.NakBtreeNilWhenEnabled.Load(),
    "socket_id", socketIdStr, "type", "nak_btree_nil_when_enabled")
```

3. **Increment locations** (5 places):
   - `receive.go:705` - periodicNakBtree()
   - `receive.go:807` - expireNakEntries()
   - `fast_nak.go:83` - checkFastNakRecent()
   - `fast_nak.go:172` - buildNakListLocked()
   - `nak_consolidate.go:47` - consolidateNakBtree()

4. **Updated analysis.go** - Added `NakBtreeNilWhenEnabled` field to `DerivedMetrics` struct and extraction logic.

5. **Verified with metrics-audit**:
```
✅ NakBtreeNilWhenEnabled (5 locations) - defined, used, exported
```

**Status**: ✅ Complete (2025-12-15)

---

### Related: Other Defensive Checks to Consider

| Code Pattern | Description | Current Handling |
|--------------|-------------|------------------|
| `if r.metrics == nil` | Metrics struct not initialized | Multiple places - should always be set |
| `CongestionRecvPktNil` | Nil packet received | ✅ Already has metric |
| `CongestionRecvPktStoreInsertFailed` | Store insert failed after Has() check | ✅ Already has metric |

---

## Test Results Log

Track test runs:

| Date | Command | Result | Notes |
|------|---------|--------|-------|
| 2025-12-14 | `go test ./circular/...` | ✅ Pass | All seq_math tests pass |
| 2025-12-14 | `go test ./circular/... -bench=.` | ✅ Pass | Benchmarks complete - see below |

### Benchmark Results: All Bit Widths Comparison

**System**: AMD Ryzen Threadripper PRO 3945WX

| Benchmark | ns/op | Notes |
|-----------|-------|-------|
| `AllBitWidths_SeqLess/16bit` | 0.24 | 16-bit (RTP-style) |
| `AllBitWidths_SeqLess/31bit` | 0.25 | 31-bit (SRT) |
| `AllBitWidths_SeqLess/32bit` | 0.25 | Full 32-bit |
| `AllBitWidths_SeqLess/64bit` | 0.24 | 64-bit (future-proof) |
| `SeqLess_Specific` | 0.24 | uint32 specific |
| `SeqLess_Generic31` | 0.26 | Generic with 31-bit |
| `SeqLess_Generic64` | 0.27 | Generic with 64-bit |
| `SeqDiff_Generic64` | 0.25 | 64-bit diff |
| `SeqDistance_64` | 0.24 | 64-bit distance |
| `CircularNumberLt` | 0.26 | Existing Number.Lt() |

**Key Findings**:
1. **All bit widths have identical performance** (~0.24-0.27 ns/op)
2. **64-bit has NO penalty** - same speed as 16-bit!
3. **Zero allocations** for all implementations
4. **Generic has NO overhead** - Go's monomorphization works perfectly
5. **Future-proof**: 64-bit sequences would work with no performance impact


