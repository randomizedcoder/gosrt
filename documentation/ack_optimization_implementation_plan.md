# ACK Optimization Implementation Plan

**Status**: READY FOR IMPLEMENTATION
**Date**: 2025-12-24
**Background**: See [`ack_optimization_plan.md`](./ack_optimization_plan.md) for detailed analysis

---

## Table of Contents

- [Overview](#overview)
- [Phase Summary](#phase-summary)
- [Function Name Changes](#function-name-changes)
- [Function Signature Changes](#function-signature-changes)
- [Variable Changes](#variable-changes)
- [Phase 1: Add LightACKDifference Config](#phase-1-add-lightackdifference-config)
- [Phase 2: Add Light/Full ACK Metrics](#phase-2-add-lightfull-ack-metrics)
- [Phase 3: Remove RecvLightACKCounter from Hot Path](#phase-3-remove-recvlightackcounter-from-hot-path)
- [Phase 4: Add Receiver Fields for Light ACK Tracking](#phase-4-add-receiver-fields-for-light-ack-tracking)
- [Phase 5: Update periodicACK to Use Difference Check](#phase-5-update-periodicack-to-use-difference-check)
- [Phase 6: Add contiguousPoint Atomic](#phase-6-add-contiguouspoint-atomic)
- [Phase 7: Create Core Scan Functions](#phase-7-create-core-scan-functions)
- [Phase 8: Create Locked Wrappers](#phase-8-create-locked-wrappers)
- [Phase 9: Create deliverReadyPackets Core Function](#phase-9-create-deliverreadypackets-core-function)
- [Phase 10: Update Tick() Function](#phase-10-update-tick-function)
- [Phase 11: Update EventLoop](#phase-11-update-eventloop)
- [Phase 12: Integration Testing Analysis Updates](#phase-12-integration-testing-analysis-updates)
- [Phase 13: RemoveAll Optimization](#phase-13-removeall-optimization)
- [Phase 14: Cleanup](#phase-14-cleanup)
- [Summary: Files Changed](#summary-files-changed)
- [Success Criteria](#success-criteria)

---

## Phase Summary

| Phase | Goal | Key Files |
|-------|------|-----------|
| **1** | Add `LightACKDifference` config | `config.go`, `flags.go` |
| **2** | Add Light/Full ACK metrics | `metrics/metrics.go`, `connection.go` |
| **3** | Remove `RecvLightACKCounter` from hot path | `receive.go`, `fake.go` |
| **4** | Add receiver fields for Light ACK tracking | `receive.go` |
| **5** | Update `periodicACK` to use difference check | `receive.go`, `fake.go` |
| **6** | Add `contiguousPoint` atomic | `receive.go` |
| **7** | Create core scan functions (`contiguousScan`, `gapScan`) | `receive.go` |
| **8** | Create locked wrappers (`periodicACKLocked`, etc.) | `receive.go` |
| **9** | Create `deliverReadyPackets` core function | `receive.go` |
| **10** | Update `Tick()` to use new functions, reorder ops | `receive.go` |
| **11** | Update EventLoop with continuous ACK scanning | `receive.go` |
| **12** | Integration testing analysis for ACK metrics | `analysis.go`, `analysis_test.go` |
| **13** | Optimize `RemoveAll()` with `DeleteMin` | `packet_store_btree.go` |
| **14** | Cleanup deprecated code | Various |

---

## Function Name Changes

| Before | After | Notes |
|--------|-------|-------|
| `periodicACK()` | `contiguousScan()` | Core function (no lock) |
| `periodicACK()` | `periodicACKLocked()` | Locked wrapper, calls `contiguousScan()` |
| `periodicNAK()` / `periodicNakBtree()` | `gapScan()` | Core function (no lock) |
| `periodicNakBtree()` | `periodicNakBtreeLocked()` | Locked wrapper, calls `gapScan()` |
| `deliverReadyPacketsNoLock()` | `deliverReadyPackets()` | Core function (no lock) |
| (new) | `deliverReadyPacketsLocked()` | Locked wrapper, calls `deliverReadyPackets()` |
| `RemoveAll()` | `RemoveAllSlow()` | Original implementation (for comparison) |
| (new) | `RemoveAll()` | Optimized with `DeleteMin` |

**Naming Convention**:
- **Core functions** (no lock): `contiguousScan()`, `gapScan()`, `deliverReadyPackets()`
- **Locked wrappers**: `periodicACKLocked()`, `periodicNakBtreeLocked()`, `deliverReadyPacketsLocked()`
- No `*NoLock` suffix needed - core functions ARE the no-lock versions

---

## Function Signature Changes

| Function | Before | After | Notes |
|----------|--------|-------|-------|
| `periodicACK()` | `(now uint64) (ok bool, seq circular.Number, lite bool)` | — | Renamed to `periodicACKLocked()` |
| `periodicACKLocked()` | — | `() (ok bool, seq circular.Number, lite bool)` | No `now` param needed |
| `contiguousScan()` | — | `() (ok bool, ackSeq uint32)` | New function, returns `uint32` |
| `periodicNakBtree()` | `(now uint64) []circular.Number` | — | Renamed to `periodicNakBtreeLocked()` |
| `periodicNakBtreeLocked()` | — | `() []circular.Number` | No `now` param needed |
| `gapScan()` | — | `() []uint32` | New function, gets `now` internally |
| `deliverReadyPacketsNoLock()` | `(now uint64) int` | — | Renamed to `deliverReadyPackets()` |
| `deliverReadyPackets()` | — | `() int` | Gets `now` internally via `time.Now()` |
| `deliverReadyPacketsLocked()` | — | `() int` | New wrapper, calls `deliverReadyPackets()` |

**Key Changes**:

1. **`now` parameter removed** - Functions that need current time get it internally:
   ```go
   // Before: caller passes now
   func (r *receiver) deliverReadyPacketsNoLock(now uint64) int { ... }

   // After: function gets now internally
   func (r *receiver) deliverReadyPackets() int {
       now := uint64(time.Now().UnixMicro())
       // ...
   }
   ```

2. **Return type changes** - Core functions return `uint32` internally, wrappers convert to `circular.Number`:
   ```go
   // Core function - returns uint32 for efficiency
   func (r *receiver) contiguousScan() (ok bool, ackSeq uint32)

   // Locked wrapper - converts to circular.Number for API compatibility
   func (r *receiver) periodicACKLocked() (ok bool, seq circular.Number, lite bool) {
       ok, ackSeq := r.contiguousScan()
       return ok, circular.New(ackSeq, packet.MAX_SEQUENCENUMBER), false
   }
   ```

---

## Variable Changes

### Removed Variables

| Variable | File | Function(s) | Replaced By | Removed In |
|----------|------|-------------|-------------|------------|
| `RecvLightACKCounter` | `metrics/metrics.go` | All `Push*()` functions | `lastLightACKSeq` + difference check | **Phase 3** |
| `ackScanHighWaterMark` | `congestion/live/receive.go` | `periodicACK()`, `periodicACKWriteLocked()` | `contiguousPoint` | **Phase 14** |
| `nakScanStartPoint` | `congestion/live/receive.go` | `periodicNakBtree()` | `contiguousPoint` | **Phase 14** |

### New Variables

| Variable | Type | File | Purpose |
|----------|------|------|---------|
| `contiguousPoint` | `atomic.Uint32` | `congestion/live/receive.go` | Unified scan starting point for ACK and NAK |
| `lastLightACKSeq` | `uint32` | `congestion/live/receive.go` | Sequence when last Light ACK was sent |
| `lightACKDifference` | `uint32` | `congestion/live/receive.go` | Threshold for sending Light ACK (from config) |
| `LightACKDifference` | `uint32` | `config.go` | Config option (default: 64, max: 5000) |
| `forceFullACKMultiplier` | constant `4` | `congestion/live/receive.go` | When diff >= lightACKDifference × 4, send Full ACK instead of Light |
| `nowFn` | `func() uint64` | `congestion/live/receive.go` | Injectable time provider for testability (defaults to `time.Now().UnixMicro()`) |

### New Metrics

| Metric | Type | File | Purpose |
|--------|------|------|---------|
| `PktSentACKLiteSuccess` | `atomic.Uint64` | `metrics/metrics.go` | Count of Light ACKs sent |
| `PktSentACKFullSuccess` | `atomic.Uint64` | `metrics/metrics.go` | Count of Full ACKs sent |
| `PktRecvACKLiteSuccess` | `atomic.Uint64` | `metrics/metrics.go` | Count of Light ACKs received |
| `PktRecvACKFullSuccess` | `atomic.Uint64` | `metrics/metrics.go` | Count of Full ACKs received |

### Variable Location Summary

| File | Variables Added | Variables Removed |
|------|-----------------|-------------------|
| `config.go` | `LightACKDifference` | — |
| `congestion/live/receive.go` | `contiguousPoint`, `lastLightACKSeq`, `lightACKDifference`, `nowFn` | `ackScanHighWaterMark`, `nakScanStartPoint` |
| `metrics/metrics.go` | `PktSentACKLiteSuccess`, `PktSentACKFullSuccess`, `PktRecvACKLiteSuccess`, `PktRecvACKFullSuccess` | `RecvLightACKCounter` |

### Variable Lifecycle by Phase

**Important**: Old and new variables coexist during the transition. Old variables are only removed after new code is fully tested.

| Phase | Variables Added | Variables Removed | Notes |
|-------|-----------------|-------------------|-------|
| **3** | — | `RecvLightACKCounter` | Removed from hot path first |
| **4** | `lastLightACKSeq`, `lightACKDifference` | — | New Light ACK tracking |
| **6** | `contiguousPoint` | — | New unified scan point |
| **9** | `nowFn` | — | Injectable time provider for testability |
| **10-11** | — | — | Old variables still exist but unused |
| **14** | — | `ackScanHighWaterMark`, `nakScanStartPoint` | Final cleanup after all tests pass |

---

## Overview

**Goal**: Reduce RTT from ~10ms to sub-millisecond in EventLoop mode by running ACK scanning continuously instead of on a 10ms timer.

**Key Changes**:
1. Add `LightACKDifference` config (default: 64, per RFC recommendation)
2. Add 4 new metrics to distinguish Light ACK vs Full ACK
3. Remove `RecvLightACKCounter` from hot path (saves ~35,600 atomic ops/sec at 100Mb/s)
4. Unify scan tracking with `contiguousPoint`
5. Rename functions for clarity: `contiguousScan()`, `gapScan()`, `deliverReadyPackets()`
6. Rename functions with locks for clarity: Wrap core function with Lock versions (Create Locked Wrappers)
7. **Force Full ACK on massive jumps**: When `contiguousPoint` advances by ≥4× `LightACKDifference`
   (e.g., after gap recovery), send Full ACK instead of Light ACK to update sender's congestion
   window and RTT immediately
8. Optimize `removeAll()`

---

## Phase 1: Add LightACKDifference Config

**Goal**: Make Light ACK frequency configurable.

### 1.1 Implementation

| File | Change |
|------|--------|
| `config.go` | Add `LightACKDifference uint32` with default 64 |
| `contrib/common/flags.go` | Add `-lightackdifference` flag |

**config.go** (after line ~381):
```go
// LightACKDifference controls how often Light ACK packets are sent.
// A Light ACK is sent when the contiguous sequence has advanced by
// at least this many packets since the last Light ACK.
// RFC recommends 64, but higher values reduce overhead at high bitrates.
// Default: 64 (RFC recommendation)
// Suggested for high bitrate (200Mb/s+): 256
LightACKDifference uint32
```

**Default** (in `DefaultConfig()`):
```go
LightACKDifference: 64,
```

**Validation** (in `Validate()` or config initialization):
```go
if c.LightACKDifference == 0 {
    c.LightACKDifference = 64  // Default
}
if c.LightACKDifference > 5000 {
    return fmt.Errorf("LightACKDifference must be <= 5000, got %d", c.LightACKDifference)
}
```

**Rationale for bounds**:
- Minimum: 1 (ACK every packet - not recommended, high overhead)
- Default: 64 (RFC recommendation)
- Maximum: 5000 (at 100Mb/s ~8900 pkt/s, would mean ~1.8 Light ACKs/sec - too infrequent)

**contrib/common/flags.go**:
```go
LightACKDifference = flag.Int("lightackdifference", -1,
    "Send Light ACK after N contiguous packets progress (default: 64)")

// In ApplyFlags():
if FlagSet["lightackdifference"] && *LightACKDifference > 0 {
    config.LightACKDifference = uint32(*LightACKDifference)
}
```

### 1.2 Tests

```bash
# Run flag tests
./contrib/common/test_flags.sh
```

Add to `test_flags.sh`:
```bash
run_test "LightACKDifference default" \
    "-useeventloop -usepacketring" \
    "LightACKDifference.*64" \
    "$SERVER_BIN"

run_test "LightACKDifference=256" \
    "-useeventloop -usepacketring -lightackdifference 256" \
    "LightACKDifference.*256" \
    "$SERVER_BIN"
```

### 1.3 Verify

- [ ] `go build ./...` passes
- [ ] `./contrib/common/test_flags.sh` passes
- [ ] Config prints `LightACKDifference` value

---

## Phase 2: Add Light/Full ACK Metrics

**Goal**: Distinguish Light ACK from Full ACK in Prometheus metrics.

### 2.1 Implementation

| File | Line(s) | Change |
|------|---------|--------|
| `metrics/metrics.go` | ~34-45 | Add 4 new counters |
| `connection.go` | ~1724 | Increment in `sendACK()` |
| `connection.go` | ~1113 | Increment in `handleACK()` |
| `metrics/handler.go` | ~80-91 | Export to Prometheus |

**metrics/metrics.go** (after `PktSentACKSuccess`):
```go
PktSentACKLiteSuccess atomic.Uint64  // Light ACKs sent
PktSentACKFullSuccess atomic.Uint64  // Full ACKs sent
PktRecvACKLiteSuccess atomic.Uint64  // Light ACKs received
PktRecvACKFullSuccess atomic.Uint64  // Full ACKs received
```

**connection.go** `sendACK()` (line ~1724):
```go
if lite {
    cif.IsLite = true
    p.Header().TypeSpecific = 0
    c.metrics.PktSentACKLiteSuccess.Add(1)  // ADD
} else {
    // ... existing Full ACK setup ...
    c.metrics.PktSentACKFullSuccess.Add(1)  // ADD
}
```

**connection.go** `handleACK()` (line ~1113):
```go
if cif.IsLite {
    c.metrics.PktRecvACKLiteSuccess.Add(1)  // ADD
} else if !cif.IsSmall {
    c.metrics.PktRecvACKFullSuccess.Add(1)  // ADD
    // ... existing Full ACK handling ...
}
```

**metrics/handler.go** (add to `getMetricsSnapshot()`):
```go
"pkt_sent_ack_lite_success": metrics.PktSentACKLiteSuccess.Load(),
"pkt_sent_ack_full_success": metrics.PktSentACKFullSuccess.Load(),
"pkt_recv_ack_lite_success": metrics.PktRecvACKLiteSuccess.Load(),
"pkt_recv_ack_full_success": metrics.PktRecvACKFullSuccess.Load(),
```

### 2.2 Tests

| File | Change |
|------|--------|
| `metrics/handler_test.go` | Add tests for 4 new counters |
| `metrics/packet_classifier_test.go` | Verify existing ACK tests still pass |

### 2.3 Run Metrics Audit

```bash
# Verify no double incrementing and all metrics exported to Prometheus
make audit-metrics
```

This runs `tools/metrics-audit/main.go` which checks:
- All metrics in `metrics/metrics.go` are exported in `handler.go`
- No metrics are incremented multiple times for the same event

### 2.4 Verify

- [ ] `go test ./metrics/...` passes
- [ ] `make audit-metrics` passes
- [ ] New metrics appear in `/metrics` endpoint
- [ ] Prometheus can scrape new metrics

---

## Phase 3: Remove RecvLightACKCounter from Hot Path

**Goal**: Eliminate per-packet atomic increment overhead.

### 3.1 Implementation

| File | Line(s) | Current | Change |
|------|---------|---------|--------|
| `congestion/live/receive.go` | 509 | `m.RecvLightACKCounter.Add(1)` | **DELETE** |
| `congestion/live/receive.go` | 553 | `m.RecvLightACKCounter.Add(1)` | **DELETE** |
| `congestion/live/receive.go` | 660 | `m.RecvLightACKCounter.Add(1)` | **DELETE** |
| `congestion/live/fake.go` | 127 | `m.RecvLightACKCounter.Add(1)` | **DELETE** |
| `metrics/metrics.go` | 326 | `RecvLightACKCounter atomic.Uint64` | **DELETE** |

### 3.2 Tests

| File | Line | Change |
|------|------|--------|
| `congestion/live/receive_ring_test.go` | 91 | Remove `RecvLightACKCounter` assertion |
| `metrics/handler_test.go` | 196 | Remove from exclusion list |

### 3.3 Verify

- [ ] `go test ./congestion/live/...` passes
- [ ] `go test ./metrics/...` passes
- [ ] No references to `RecvLightACKCounter` remain (except comments)

---

## Phase 4: Add Receiver Fields for Light ACK Tracking

**Goal**: Add fields to track Light ACK state using difference-based approach.

### 4.1 Implementation

**congestion/live/receive.go** - Add to `receiver` struct:
```go
type receiver struct {
    // ... existing fields ...

    // Light ACK tracking (Phase 4)
    lightACKDifference uint32  // Threshold for sending Light ACK (default: 64)
    lastLightACKSeq    uint32  // Sequence when last Light ACK was sent
}
```

**ReceiveConfig** - Add field:
```go
type ReceiveConfig struct {
    // ... existing fields ...
    LightACKDifference uint32  // Light ACK threshold (default: 64)
}
```

**NewReceiver()** - Initialize:
```go
lightACKDifference := recvConfig.LightACKDifference
if lightACKDifference == 0 {
    lightACKDifference = 64
}

return &receiver{
    // ...
    lightACKDifference: lightACKDifference,
    lastLightACKSeq:    recvConfig.InitialSequenceNumber.Val(),
}
```

**connection.go** - Pass config:
```go
c.recv = live.NewReceiver(live.ReceiveConfig{
    // ... existing fields ...
    LightACKDifference: c.config.LightACKDifference,
})
```

### 4.2 Tests

- [ ] Verify receiver initializes with correct `lightACKDifference`
- [ ] Verify `lastLightACKSeq` initialized from ISN

### 4.3 Verify

- [ ] `go build ./...` passes
- [ ] `go test ./congestion/live/...` passes

---

## Phase 5: Update periodicACK to Use Difference Check

**Goal**: Replace counter-based Light ACK triggering with difference-based.

### 5.1 Implementation

**congestion/live/receive.go** `periodicACK()` (lines 767-769):

**Before**:
```go
lightACKCount := r.metrics.RecvLightACKCounter.Load()
if now-r.lastPeriodicACK < r.periodicACKInterval {
    if lightACKCount >= 64 {
        needLiteACK = true
    }
}
```

**After**:
```go
if now-r.lastPeriodicACK < r.periodicACKInterval {
    // Check if we've advanced enough for a Light ACK
    diff := circular.SeqSub(r.lastACKSequenceNumber.Val(), r.lastLightACKSeq)
    if diff >= r.lightACKDifference {
        needLiteACK = true
    }
}
```

**Also update** `periodicACKWriteLocked()` (line ~961):

**Before**:
```go
r.metrics.RecvLightACKCounter.Store(0)
```

**After**:
```go
if lite {
    r.lastLightACKSeq = ackSequenceNumber.Val()
}
```

**Update fake.go similarly** (lines 159-176).

### 5.2 Tests

| File | Test |
|------|------|
| `congestion/live/receive_test.go` | Verify Light ACK sent after 64 packets |
| `congestion/live/receive_test.go` | Verify Light ACK NOT sent before 64 packets |

### 5.3 Verify

- [ ] `go test ./congestion/live/... -run TestPeriodicACK` passes
- [ ] Light ACKs sent at expected rate

---

## Phase 6: Add contiguousPoint Atomic

**Goal**: Unify ACK and NAK scan tracking with single atomic.

### 6.1 Implementation

**congestion/live/receive.go** - Add to struct:
```go
type receiver struct {
    // ... existing fields ...
    contiguousPoint atomic.Uint32  // Last contiguous sequence (shared by ACK/NAK scan)
}
```

**Initialize** in `NewReceiver()` (after line ~393, similar to `nakScanStartPoint`):
```go
// Initialize contiguousPoint from InitialSequenceNumber (known from SRT handshake).
// The InitialSequenceNumber is negotiated during the SRT connection setup and
// represents the first sequence number the sender will transmit.
// This is CRITICAL for wraparound handling:
// - If we used btree.Min() instead, we'd get the CIRCULAR minimum (e.g., 0 or 1)
// - But for a stream starting at MAX-2, logical order is: MAX-2, MAX-1, MAX, 0, 1, 2, ...
// - Btree circular order is: 0, 1, 2, ..., MAX-2, MAX-1, MAX
// - Starting from btree.Min() would give wrong gap detection across wraparound
r.contiguousPoint.Store(recvConfig.InitialSequenceNumber.Val())
```

### 6.2 Tests

- [ ] Verify `contiguousPoint` initialized correctly
- [ ] Verify atomic operations work correctly

---

## Phase 7: Create Core Scan Functions

**Goal**: Create `contiguousScan()` and `gapScan()` core functions.

### 7.1 Implementation

**Create** `contiguousScan()`:
```go
// contiguousScan scans packet btree for contiguous sequences.
// Updates contiguousPoint atomically when progress is made.
// Returns: ok=true if ACK should be sent, ackSeq=sequence to ACK
func (r *receiver) contiguousScan() (ok bool, ackSeq uint32) {
    // ... implementation per ack_optimization_plan.md section 3.5.4 ...
}
```

**Create** `gapScan()`:
```go
// gapScan scans packet btree for gaps (missing sequences).
// Updates contiguousPoint atomically when contiguous packets found.
// Returns: list of missing sequence numbers to NAK
func (r *receiver) gapScan() []uint32 {
    // ... implementation per ack_optimization_plan.md section 3.5.4 ...
}
```

### 7.1.1 CRITICAL: 31-bit Wraparound Safety Rule

> **⚠️ GLOBAL RULE**: All ordering comparisons on `contiguousPoint` and sequence numbers
> **MUST** use the `circular` package functions. Never use raw `>`, `<`, `>=`, `<=`, or
> subtraction for sequence number comparisons.

**Problem**: SRT sequence numbers are 31-bit values (0 to 2³¹−1). Simple arithmetic comparisons
fail when the sequence wraps from 2³¹−1 (2147483647) to 0.

**Example of the bug**:
```go
// WRONG: Will fail at wraparound
startSeq := uint32(2147483645)  // Near max
pktSeq := uint32(2)             // After wrap
if pktSeq > startSeq {          // FALSE! 2 > 2147483645 is false
    // This branch never taken, but logically pkt 2 IS after 2147483645
}

// WRONG: Subtraction overflows
diff := pktSeq - startSeq       // Underflows to huge positive number
```

**Required Functions** (from `circular/seq_math.go`):

| Function | Use For |
|----------|---------|
| `circular.SeqLess(a, b)` | `a < b` with wraparound |
| `circular.SeqLessOrEqual(a, b)` | `a <= b` with wraparound |
| `circular.SeqGreater(a, b)` | `a > b` with wraparound |
| `circular.SeqGreaterOrEqual(a, b)` | `a >= b` with wraparound |
| `circular.SeqAdd(a, n)` | `a + n` with wraparound |
| `circular.SeqSub(a, b)` | `a - b` with wraparound (returns signed distance) |

**Correct Implementation**:
```go
// CORRECT: Use circular package for all comparisons
startSeq := r.contiguousPoint.Load()

r.packetStore.IterateFrom(startSeq, func(p packet.Packet) bool {
    pktSeq := p.Header().PacketSequenceNumber.Val()

    // CORRECT: Use SeqLessOrEqual for "already processed" check
    if circular.SeqLessOrEqual(pktSeq, startSeq) {
        return true  // Skip already-processed packets
    }

    // CORRECT: Use SeqAdd for "next expected" calculation
    expectedNext := circular.SeqAdd(startSeq, 1)
    if pktSeq == expectedNext {
        // Contiguous! Advance the scan point
        startSeq = pktSeq
        return true  // Continue
    }

    // Gap found
    return false
})

// CORRECT: Update with new position
r.contiguousPoint.Store(startSeq)
```

**See also**: `circular/seq_math_31bit_wraparound_test.go` for test cases documenting this bug.

### 7.2 Tests

| Test | Purpose |
|------|---------|
| `TestContiguousScan_Empty` | Empty btree returns ok=false |
| `TestContiguousScan_Contiguous` | Contiguous packets advance point |
| `TestContiguousScan_Gap` | Gap stops scan |
| `TestContiguousScan_Wraparound` | 31-bit wraparound handled correctly |
| `TestGapScan_NoGaps` | No gaps returns empty list |
| `TestGapScan_SingleGap` | Single gap detected |
| `TestGapScan_AdvancesPoint` | Contiguous packets before gap advance point |
| `TestGapScan_Wraparound` | 31-bit wraparound handled correctly |

**Wraparound Test Cases** (critical for both functions):
```go
// Test sequence: 2147483645, 2147483646, 2147483647, 0, 1, 2
// (MAX-2, MAX-1, MAX, 0, 1, 2) - crosses the 31-bit boundary

func TestContiguousScan_Wraparound(t *testing.T) {
    // Start at MAX-2 (2147483645)
    // Insert packets: MAX-2, MAX-1, MAX, 0, 1, 2 (all contiguous)
    // Verify contiguousScan correctly identifies all 6 as contiguous
    // and advances contiguousPoint to 2
}

func TestGapScan_Wraparound(t *testing.T) {
    // Start at MAX-2 (2147483645)
    // Insert packets: MAX-2, MAX-1, MAX, 2, 3 (gap at 0, 1)
    // Verify gapScan correctly detects gap [0, 1] across boundary
}
```

### 7.3 Verify

- [ ] All scan tests pass
- [ ] `TestContiguousScan_Wraparound` passes (CRITICAL)
- [ ] `TestGapScan_Wraparound` passes (CRITICAL)
- [ ] No raw `>`, `<`, `>=`, `<=` comparisons on sequence numbers in new code

---

## Phase 8: Create Locked Wrappers

**Goal**: Create `periodicACKLocked()` and `periodicNakBtreeLocked()` wrappers.

### 8.1 Implementation

**congestion/live/receive.go** - Create wrapper functions:

```go
// periodicACKLocked wraps contiguousScan with write lock (for Tick()-based mode)
func (r *receiver) periodicACKLocked() (ok bool, seq circular.Number, lite bool) {
    r.lock.RLock()
    ok, ackSeq := r.contiguousScan()
    r.lock.RUnlock()
    // ... convert to circular.Number, apply Light ACK logic, return ...
}

// periodicNakBtreeLocked wraps gapScan with write lock (for Tick()-based mode)
func (r *receiver) periodicNakBtreeLocked() []circular.Number {
    r.lock.RLock()
    gaps := r.gapScan()
    r.lock.RUnlock()
    // ... convert []uint32 to []circular.Number and return ...
}
```

**Update callers** in `congestion/live/receive.go`:

| Caller | Line(s) | Current Call | New Call |
|--------|---------|--------------|----------|
| `Tick()` | ~1713 | `r.periodicACK(now)` | `r.periodicACKLocked()` |
| `Tick()` | ~1743 | `r.periodicNakBtree(now)` | `r.periodicNakBtreeLocked()` |

**Note**: The old `periodicACK()` and `periodicNakBtree()` functions are NOT deleted in this phase.
They remain for reference and testing. They are removed in Phase 14 (Cleanup).

### 8.2 Tests

**File**: `congestion/live/receive_test.go`

```bash
# Run ACK tests to verify wrappers work correctly
go test ./congestion/live/... -run TestRecvACK -v
go test ./congestion/live/... -run TestRecvPeriodicACKLite -v
go test ./congestion/live/... -run TestACK_Wraparound -v

# Run NAK tests to verify wrappers work correctly
go test ./congestion/live/... -run TestRecvNAK -v
go test ./congestion/live/... -run TestRecvPeriodicNAK -v
go test ./congestion/live/... -run TestNakBtree -v
```

### 8.3 Verify

- [ ] Existing `TestRecvACK` passes with wrapped function
- [ ] Existing `TestRecvPeriodicACKLite` passes
- [ ] Existing `TestACK_Wraparound_*` tests pass (critical for 31-bit safety)
- [ ] Existing `TestRecvNAK` passes with wrapped function
- [ ] Existing `TestRecvPeriodicNAK` passes
- [ ] All `TestNakBtree_*` tests pass

---

## Phase 9: Create deliverReadyPackets Core Function

**Goal**: Refactor delivery to have core function + locked wrapper, with testable time provider.

### 9.1 Implementation

#### 9.1.1 Add Time Provider for Testability

**Problem**: If `deliverReadyPackets()` calls `time.Now()` internally, unit tests for TSBPD
delivery become non-deterministic and require `time.Sleep()` calls.

**Solution**: Add an injectable `nowFn` field that defaults to `time.Now().UnixMicro()` but
can be replaced in tests.

**congestion/live/receive.go** - Add to `receiver` struct:
```go
type receiver struct {
    // ... existing fields ...

    // Time provider for testability (defaults to time.Now().UnixMicro)
    // In tests, this can be replaced with a mock to enable deterministic TSBPD testing.
    nowFn func() uint64
}
```

**Initialize** in `NewReceiver()`:
```go
r := &receiver{
    // ... existing fields ...
    nowFn: func() uint64 { return uint64(time.Now().UnixMicro()) },
}
```

**Test helper** (add to `receive_test.go` or create `receive_test_helpers.go`):
```go
// setNowFn replaces the time provider for deterministic testing.
// Returns the original function for restoration.
func (r *receiver) setNowFn(fn func() uint64) func() uint64 {
    old := r.nowFn
    r.nowFn = fn
    return old
}

// Example usage in tests:
func TestDeliverReadyPackets_TSBPD(t *testing.T) {
    r := createTestReceiver()

    // Mock time: start at T=1000000 (1 second in microseconds)
    mockTime := uint64(1_000_000)
    r.setNowFn(func() uint64 { return mockTime })

    // Insert packet with TSBPD time = 1,500,000 (1.5 seconds)
    insertPacketWithTSBPD(r, 1_500_000)

    // Time is 1.0s, packet ready at 1.5s - should NOT deliver
    delivered := r.deliverReadyPackets()
    require.Equal(t, 0, delivered, "Packet not ready yet")

    // Advance mock time to 1.6s - should deliver
    mockTime = 1_600_000
    delivered = r.deliverReadyPackets()
    require.Equal(t, 1, delivered, "Packet should be delivered")
}
```

#### 9.1.2 Update deliverReadyPackets to Use nowFn

**congestion/live/receive.go** - Rename `deliverReadyPacketsNoLock` → `deliverReadyPackets`:

**Create** `deliverReadyPackets()` (core, no lock, uses `nowFn`):
```go
// deliverReadyPackets delivers TSBPD-ready packets to the application.
// This is the core function - no locking, gets current time from nowFn.
func (r *receiver) deliverReadyPackets() int {
    now := r.nowFn()  // Use injectable time provider
    // ... existing deliverReadyPacketsNoLock implementation ...
}
```

**Create** `deliverReadyPacketsLocked()` (wrapper):
```go
// deliverReadyPacketsLocked wraps deliverReadyPackets with write lock (for Tick()-based mode)
func (r *receiver) deliverReadyPacketsLocked() int {
    if r.lockTiming != nil {
        var delivered int
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            delivered = r.deliverReadyPackets()
        })
        return delivered
    }
    r.lock.Lock()
    delivered := r.deliverReadyPackets()
    r.lock.Unlock()
    return delivered
}
```

**Update callers**:

| Caller | Location | Current Call | New Call |
|--------|----------|--------------|----------|
| `Tick()` | `receive.go` ~1705 | inline delivery code | `r.deliverReadyPacketsLocked()` |
| `EventLoop()` | `receive.go` ~1862 | `r.deliverReadyPacketsNoLock(now)` | `r.deliverReadyPackets()` |

### 9.2 Tests

**Test Files**:
- `congestion/live/receive_test.go` - Core receiver tests
- `congestion/live/receive_stream_test.go` - Stream-based integration tests

```bash
# Run delivery-related tests
go test ./congestion/live/... -run TestRecvTSBPD -v
go test ./congestion/live/... -run TestRecvDropTooLate -v
go test ./congestion/live/... -run TestRecvFlush -v

# Run sequence ordering tests (delivery depends on this)
go test ./congestion/live/... -run TestRecvSequence -v

# Run NAK btree tests that include delivery
go test ./congestion/live/... -run TestNakBtree_RealisticStream -v

# Full receive test suite
go test ./congestion/live/... -v
```

### 9.3 Verify

- [ ] `go test ./congestion/live/... -run TestRecvTSBPD` passes
- [ ] `go test ./congestion/live/... -run TestRecvDropTooLate` passes
- [ ] `go test ./congestion/live/...` passes (full suite)
- [ ] Delivery still happens at correct TSBPD times
- [ ] `nowFn` defaults to `time.Now().UnixMicro()` in production
- [ ] Tests can inject mock time via `setNowFn()` for deterministic TSBPD testing
- [ ] No `time.Sleep()` needed in new TSBPD unit tests

---

## Phase 10: Update Tick() Function

**Goal**: Refactor `Tick()` to use new functions and reorder operations.

### 10.1 Implementation

**congestion/live/receive.go** `Tick()`:

**Before**:
```go
func (r *receiver) Tick(now uint64) {
    // 1. Drain ring
    // 2. periodicACK
    // 3. periodicNAK
    // 4. expireNakEntries
    // 5. Delivery (inline, duplicated)
}
```

**After**:
```go
func (r *receiver) Tick(now uint64) {
    if r.usePacketRing {
        r.drainRingByDelta()
    }

    // REORDERED: Deliver first to shrink btree
    r.deliverReadyPacketsLocked()

    // ACK and NAK on smaller btree
    if ok, seq, lite := r.periodicACKLocked(); ok {
        r.sendACK(seq, lite)
    }

    if list := r.periodicNakBtreeLocked(); len(list) != 0 {
        metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
        r.sendNAK(list)
    }

    if r.useNakBtree && r.nakBtree != nil {
        r.expireNakEntries()
    }
}
```

### 10.2 Tests

- [ ] `go test ./congestion/live/... -run TestTick` passes
- [ ] Delivery happens before ACK/NAK

---

## Phase 11: Update EventLoop

**Goal**: Run continuous ACK scanning in EventLoop with difference-based Light ACK.

### 11.1 Implementation

**congestion/live/receive.go** `EventLoop()`:

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // ... existing setup ...

    for {
        select {
        case <-ctx.Done():
            return

        // REMOVED: case <-ackTicker.C:  (no longer needed)

        case <-nakTicker.C:
            // ... NAK handling ...

        case <-rateTicker.C:
            // ... rate stats ...

        default:
            // REORDERED: Deliver first
            delivered := r.deliverReadyPackets()
            processed := r.processOnePacket()

            // Continuous ACK scan
            ok, newContiguous := r.contiguousScan()
            if ok {
                // Check if we've advanced enough to send an ACK
                diff := circular.SeqSub(newContiguous, r.lastLightACKSeq)
                if diff >= r.lightACKDifference {
                    // Determine ACK type: Light vs Full (Force Full on massive jump)
                    //
                    // Rationale: If contiguousPoint jumps by a large amount (e.g., 500 packets
                    // when a large gap is filled), sending just a Light ACK loses valuable info.
                    // A Full ACK is more valuable here because it:
                    //   1. Updates the sender's congestion window immediately
                    //   2. Provides fresh RTT information after recovery
                    //   3. Triggers ACKACK for accurate RTT measurement
                    //
                    // Threshold: 4x the LightACKDifference (e.g., 256 packets if diff=64)
                    forceFullACK := diff >= (r.lightACKDifference * 4)
                    lite := !forceFullACK

                    r.sendACK(circular.New(newContiguous+1, packet.MAX_SEQUENCENUMBER), lite)
                    r.lastLightACKSeq = newContiguous
                }
            }

            // Adaptive backoff when idle
            if !processed && delivered == 0 {
                time.Sleep(backoff.getSleepDuration())
            } else {
                backoff.recordActivity()
            }
        }
    }
}
```

### 11.2 Tests

**Unit Tests** - `congestion/live/receive_test.go`:

```bash
# Run all ACK-related tests
go test ./congestion/live/... -run TestRecvACK -v
go test ./congestion/live/... -run TestRecvPeriodicACKLite -v
go test ./congestion/live/... -run TestACK_Wraparound -v

# Run NAK tests (gapScan)
go test ./congestion/live/... -run TestNakBtree -v

# Full receiver test suite
go test ./congestion/live/... -v
```

**Force Full ACK Tests** (new):

Test that massive jumps in `contiguousPoint` trigger Full ACK instead of Light ACK:

```go
func TestEventLoop_ForceFullACK_MassiveJump(t *testing.T) {
    // Setup: LightACKDifference = 64
    // Scenario: contiguousPoint jumps from 100 to 400 (diff = 300)
    // Since 300 >= 64 * 4 (256), should send Full ACK, not Light ACK

    var sentACKs []struct{ seq uint32; lite bool }
    // ... setup receiver with mock sendACK that records calls ...

    // Inject packets 100-400 all at once (simulating gap recovery)
    // Run EventLoop iteration

    require.Len(t, sentACKs, 1)
    require.False(t, sentACKs[0].lite, "Expected Full ACK for massive jump")
}

func TestEventLoop_LightACK_NormalProgress(t *testing.T) {
    // Setup: LightACKDifference = 64
    // Scenario: contiguousPoint advances from 100 to 164 (diff = 64)
    // Since 64 < 64 * 4 (256), should send Light ACK

    // ... similar setup ...

    require.True(t, sentACKs[0].lite, "Expected Light ACK for normal progress")
}
```

**Stream Tests** - `congestion/live/receive_stream_test.go`:

These tests exercise the full receiver pipeline with realistic packet streams:

```bash
# Tier 1: Core functionality tests (~5 tests, ~30s)
go test ./congestion/live/... -run TestStream_Tier1 -v

# Tier 2: Extended coverage (~10 tests, ~60s)
go test ./congestion/live/... -run TestStream_Tier2 -v

# Tier 3: Comprehensive stress tests (~20 tests, ~120s)
go test ./congestion/live/... -run TestStream_Tier3 -v

# All stream tests
go test ./congestion/live/... -run TestStream -v
```

**Race Detection Tests** - `congestion/live/receive_race_test.go`:

Critical for validating lock-free EventLoop with continuous ACK scanning:

```bash
# Full race test with race detector
go test ./congestion/live/... -run TestRace -race -v

# Specific race tests
go test ./congestion/live/... -run TestRace_FullPipeline -race -v
go test ./congestion/live/... -run TestRace_PushWithTick -race -v
```

**Integration Tests** (from `integration_testing_matrix_design.md`):

See [Integration Testing Matrix Design](./integration_testing_matrix_design.md) for full test matrix.

```bash
# Isolation tests (clean network, compare Base vs Full config)
cd contrib/integration_testing
go run . isolation --config="Iso-Clean-20M-5s-R0-Base-vs-Full"

# Parallel tests (Starlink impairment, compare Base vs Full)
go run . parallel --config="Parallel-Starlink-20M-5s-R60-Base-vs-Full"

# Tier 1 matrix tests (~30 tests, ~45 min)
make test-matrix-tier1

# RTT sweep tests (critical for validating RTT improvement)
go run . parallel --config="Parallel-Starlink-20M-5s-R10-Base-vs-Full"
go run . parallel --config="Parallel-Starlink-20M-5s-R60-Base-vs-Full"
go run . parallel --config="Parallel-Starlink-20M-5s-R130-Base-vs-Full"
go run . parallel --config="Parallel-Starlink-20M-5s-R300-Base-vs-Full"
```

**LightACKDifference Variations**:

Test with different `LightACKDifference` values to validate the optimization:

```bash
# Default (64 packets)
go run . parallel --config="..." --lightackdifference=64

# Legacy behavior (ACK every packet) - for comparison
go run . parallel --config="..." --lightackdifference=1

# High bitrate optimization (256 packets)
go run . parallel --config="..." --lightackdifference=256
```

### 11.3 Verify

- [ ] RTT reduced from ~10ms to sub-millisecond in EventLoop mode
- [ ] Light ACKs sent at expected rate (~packets / LightACKDifference)
- [ ] Full ACKs still sent every 10ms (via timer)
- [ ] **Force Full ACK**: Massive jumps (≥4x LightACKDifference) trigger Full ACK
- [ ] `go test ./congestion/live/... -race` passes (no race conditions)
- [ ] `TestStream_Tier1` passes
- [ ] `TestRace_FullPipeline` passes with race detector
- [ ] `TestEventLoop_ForceFullACK_MassiveJump` passes
- [ ] Isolation tests show no regression vs Baseline
- [ ] Parallel tests show improvement vs Baseline under impairment

---

## Phase 12: Integration Testing Analysis Updates

**Goal**: Update integration testing to validate new Light/Full ACK metrics.

See [`integration_testing_design.md`](./integration_testing_design.md) for analysis framework details.

### 12.1 Add New Metrics to DerivedMetrics

**File**: `contrib/integration_testing/analysis.go`

Add to `DerivedMetrics` struct (around line 240):
```go
// Light/Full ACK counters (Phase 5 optimization)
// Expected: LightACKsSent ≈ (packets_sent / LightACKDifference)
// Expected: FullACKsSent ≈ (test_duration_sec * 100)  // 10ms interval = 100/sec
ACKLiteSent int64  // gosrt_pkt_sent_ack_lite_success_total
ACKFullSent int64  // gosrt_pkt_sent_ack_full_success_total
ACKLiteRecv int64  // gosrt_pkt_recv_ack_lite_success_total
ACKFullRecv int64  // gosrt_pkt_recv_ack_full_success_total
```

Add to `ComputeDerivedMetrics()` function:
```go
// Light/Full ACK counters
dm.ACKLiteSent = int64(getSumByPrefix(last, "gosrt_pkt_sent_ack_lite_success_total") -
    getSumByPrefix(first, "gosrt_pkt_sent_ack_lite_success_total"))
dm.ACKFullSent = int64(getSumByPrefix(last, "gosrt_pkt_sent_ack_full_success_total") -
    getSumByPrefix(first, "gosrt_pkt_sent_ack_full_success_total"))
dm.ACKLiteRecv = int64(getSumByPrefix(last, "gosrt_pkt_recv_ack_lite_success_total") -
    getSumByPrefix(first, "gosrt_pkt_recv_ack_lite_success_total"))
dm.ACKFullRecv = int64(getSumByPrefix(last, "gosrt_pkt_recv_ack_full_success_total") -
    getSumByPrefix(first, "gosrt_pkt_recv_ack_full_success_total"))
```

### 12.2 Add Validation Functions

**File**: `contrib/integration_testing/analysis.go`

```go
// ValidateACKRates checks that Light and Full ACK rates are within expected bounds
func ValidateACKRates(dm DerivedMetrics, testDurationSec float64,
                      lightACKDifference int64, packetsSent int64) ACKRateValidation {
    result := ACKRateValidation{Passed: false}  // Fail-safe default

    // Expected Full ACK rate: ~100/sec (10ms interval)
    expectedFullACKs := int64(testDurationSec * 100)
    fullACKTolerance := int64(testDurationSec * 20)  // ±20/sec tolerance

    if dm.ACKFullSent < expectedFullACKs-fullACKTolerance ||
       dm.ACKFullSent > expectedFullACKs+fullACKTolerance {
        result.FullACKError = fmt.Sprintf("Full ACKs sent %d, expected %d±%d",
            dm.ACKFullSent, expectedFullACKs, fullACKTolerance)
        return result
    }

    // Expected Light ACK rate: packets_sent / LightACKDifference
    if lightACKDifference > 0 {
        expectedLightACKs := packetsSent / lightACKDifference
        lightACKTolerance := expectedLightACKs / 10  // ±10% tolerance

        if dm.ACKLiteSent < expectedLightACKs-lightACKTolerance ||
           dm.ACKLiteSent > expectedLightACKs+lightACKTolerance {
            result.LightACKError = fmt.Sprintf("Light ACKs sent %d, expected %d±%d",
                dm.ACKLiteSent, expectedLightACKs, lightACKTolerance)
            return result
        }
    }

    result.Passed = true
    return result
}

type ACKRateValidation struct {
    Passed        bool
    LightACKError string
    FullACKError  string
}
```

### 12.3 Add to TestResultSummary

**File**: `contrib/integration_testing/analysis.go`

Add to `TestResultSummary` struct:
```go
// ACK counters
ACKLiteSent int64
ACKFullSent int64
ACKLiteRecv int64
ACKFullRecv int64
```

### 12.4 Update Tests

**File**: `contrib/integration_testing/analysis_test.go`

```go
func TestValidateACKRates(t *testing.T) {
    // Test with default LightACKDifference=64
    dm := DerivedMetrics{
        ACKLiteSent: 1400,  // ~90000 packets / 64 = 1406
        ACKFullSent: 1000,  // 10 sec * 100/sec = 1000
    }

    result := ValidateACKRates(dm, 10.0, 64, 90000)
    require.True(t, result.Passed, "ACK rates should be valid")
}

func TestValidateACKRates_TooFewFullACKs(t *testing.T) {
    dm := DerivedMetrics{
        ACKLiteSent: 1400,
        ACKFullSent: 500,  // Too few - expected ~1000
    }

    result := ValidateACKRates(dm, 10.0, 64, 90000)
    require.False(t, result.Passed, "Should fail with too few Full ACKs")
    require.Contains(t, result.FullACKError, "expected")
}

func TestValidateACKRates_TooFewLightACKs(t *testing.T) {
    dm := DerivedMetrics{
        ACKLiteSent: 100,  // Too few - expected ~1400
        ACKFullSent: 1000,
    }

    result := ValidateACKRates(dm, 10.0, 64, 90000)
    require.False(t, result.Passed, "Should fail with too few Light ACKs")
    require.Contains(t, result.LightACKError, "expected")
}
```

### 12.5 Verify

```bash
# Run integration test analysis tests
go test ./contrib/integration_testing/... -run TestValidateACKRates

# Run full integration test and check ACK metrics
make integration-test-50mbps
# Check output for ACK rate validation
```

- [ ] `ValidateACKRates` function implemented
- [ ] New metrics appear in test output summary
- [ ] Integration tests validate expected ACK rates
- [ ] `make audit-metrics` passes

---

## Phase 13: RemoveAll Optimization

**Goal**: Optimize `btreePacketStore.RemoveAll()` to use `DeleteMin`.

### 12.1 Implementation

**congestion/live/packet_store_btree.go**:

```go
// RemoveAllSlow - Original implementation (for comparison)
func (s *btreePacketStore) RemoveAllSlow(...) int { ... }

// RemoveAll - Optimized using DeleteMin
func (s *btreePacketStore) RemoveAll(predicate func(pkt packet.Packet) bool,
                                      deliverFunc func(pkt packet.Packet)) int {
    removed := 0
    for {
        min := s.tree.Min()
        if min == nil || !predicate(min.packet) {
            break
        }
        deliverFunc(min.packet)
        s.tree.DeleteMin()
        removed++
    }
    return removed
}
```

### 12.2 Benchmarks

```go
func BenchmarkRemoveAllSlow(b *testing.B) { ... }
func BenchmarkRemoveAll(b *testing.B) { ... }
```

### 12.3 Verify

- [ ] Benchmark shows improvement
- [ ] All delivery tests pass

---

## Phase 14: Cleanup - Remove Deprecated Variables and Code

**Goal**: Remove deprecated variables, functions, and update documentation.

### 13.1 Variables to Remove

| Variable | File | Line(s) | Replaced By | Verify Not Referenced |
|----------|------|---------|-------------|----------------------|
| `ackScanHighWaterMark` | `congestion/live/receive.go` | struct ~line 185 | `contiguousPoint` | `grep -r "ackScanHighWaterMark"` |
| `nakScanStartPoint` | `congestion/live/receive.go` | struct ~line 200 | `contiguousPoint` | `grep -r "nakScanStartPoint"` |

### 13.2 Functions to Remove (if not already renamed)

| Function | File | Status |
|----------|------|--------|
| `periodicACK()` | `congestion/live/receive.go` | Renamed to `periodicACKLocked()` in Phase 8 |
| `periodicNakBtree()` | `congestion/live/receive.go` | Renamed to `periodicNakBtreeLocked()` in Phase 8 |
| `deliverReadyPacketsNoLock()` | `congestion/live/receive.go` | Renamed to `deliverReadyPackets()` in Phase 9 |

### 13.3 Implementation Steps

```bash
# Step 1: Verify no references to old variables
grep -rn "ackScanHighWaterMark" congestion/live/
grep -rn "nakScanStartPoint" congestion/live/

# Step 2: Remove from struct (congestion/live/receive.go)
# DELETE: ackScanHighWaterMark circular.Number
# DELETE: nakScanStartPoint atomic.Uint32

# Step 3: Remove from NewReceiver() initialization
# DELETE: any initialization of ackScanHighWaterMark
# DELETE: any initialization of nakScanStartPoint

# Step 4: Update comments referencing old names
grep -rn "periodicACK\|periodicNAK\|ackScanHighWaterMark\|nakScanStartPoint" --include="*.go" .
```

### 13.4 Tests

```bash
# Verify nothing references removed variables
go build ./...
go test ./congestion/live/...
go test ./...
```

### 13.5 Verify

- [ ] `grep -r "ackScanHighWaterMark"` returns no results (except documentation)
- [ ] `grep -r "nakScanStartPoint"` returns no results (except documentation)
- [ ] `go build ./...` passes
- [ ] All tests pass
- [ ] No dead code remains

---

## Summary: Files Changed

| File | Phases |
|------|--------|
| `config.go` | 1 |
| `contrib/common/flags.go` | 1 |
| `contrib/common/test_flags.sh` | 1 |
| `metrics/metrics.go` | 2, 3 |
| `metrics/handler.go` | 2 |
| `metrics/handler_test.go` | 2, 3 |
| `connection.go` | 2, 4 |
| `congestion/live/receive.go` | 3, 4, 5, 6, 7, 8, 9, 10, 11, 14 |
| `congestion/live/fake.go` | 3, 5 |
| `congestion/live/receive_ring_test.go` | 3 |
| `congestion/live/receive_test.go` | 5, 7, 8, 10 |
| `contrib/integration_testing/analysis.go` | 12 |
| `contrib/integration_testing/analysis_test.go` | 12 |
| `congestion/live/packet_store_btree.go` | 13 |

---

## Success Criteria

1. **RTT**: Reduced from ~10ms to < 1ms in EventLoop mode
2. **Light ACKs**: Sent every 64 packets (configurable)
3. **Full ACKs**: Still sent every 10ms for RTT measurement
4. **Metrics**: New Prometheus metrics show Light/Full ACK rates
5. **Performance**: ~35,600 fewer atomic ops/sec at 100Mb/s
6. **Tests**: All existing tests pass, new tests added

