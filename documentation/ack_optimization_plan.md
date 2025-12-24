# ACK Optimization Implementation Plan

**Status**: READY FOR IMPLEMENTATION
**Date**: 2025-12-24
**Related Documents**:
- [`ack_optimization.md`](./ack_optimization.md) - Design exploration and benchmarks
- [`lockless_phase4_implementation.md`](./lockless_phase4_implementation.md) - RTT increase root cause
- [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md) - Test matrix structure

---

## 1. Problem Statement

### RTT Increased from ~0.08ms to ~10ms in EventLoop Mode

When EventLoop mode was introduced (Phase 4), ACKs are only sent when the `ackTicker` fires (every 10ms). This results in:

- **RTT increased** from ~0.08ms (legacy) to ~10ms (EventLoop)
- **Sender feedback delayed** - congestion control responds slowly
- **Inaccurate RTT measurement** - RTT reflects ticker interval, not network latency

**Root cause**: In EventLoop mode, ACKs are batched on a timer instead of being sent when sequential packets arrive.

**See**: [`lockless_phase4_implementation.md` Section "Root Cause Identified: ACK Timing Difference"](./lockless_phase4_implementation.md#root-cause-identified-ack-timing-difference)

---

## 2. Solution Overview

**Core idea**: Remove the `ackTicker` and run ACK scanning after every packet delivery. The scan is already optimized with the High Water Mark to only check new packets (~250-400ns per call).

**Key changes**:
1. Rename `periodicACKNoLock()` → `ackScanNoLock()` (better describes what it does)
2. Remove `ackTicker.C` case from EventLoop
3. Call `ackScanNoLock()` after `deliverReadyPacketsNoLock()` in the `default:` case
4. Add `ACKModulus` config to reduce ACK packet frequency (default: 10)

---

## 3. Current Implementation

### 3.1 EventLoop Structure

**File**: `congestion/live/receive.go` lines 1784-1882

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // ... setup tickers ...

    ackTicker := time.NewTicker(ackInterval)   // ← PROBLEM: 10ms timer
    defer ackTicker.Stop()

    // ... other tickers ...

    for {
        select {
        case <-ctx.Done():
            return

        case <-ackTicker.C:                     // ← ACK only fires every 10ms
            r.drainRingByDelta()
            now := uint64(time.Now().UnixMicro())
            if ok, seq, lite := r.periodicACK(now); ok {
                r.sendACK(seq, lite)
            }

        case <-nakTicker.C:
            // NAK processing...

        case <-rateTicker.C:
            // Rate stats...

        default:
            now := uint64(time.Now().UnixMicro())
            processed := r.processOnePacket()
            delivered := r.deliverReadyPacketsNoLock(now)
            // ... adaptive backoff ...
        }
    }
}
```

### 3.2 periodicACK Function

**File**: `congestion/live/receive.go` lines 758-922

The `periodicACK()` function scans the packet btree to find contiguous sequence numbers. With the **ACK Scan High Water Mark** optimization, it only scans NEW packets since the last call.

**Cost per call** (from `ack_optimization.md` benchmarks at 100Mb/s):
- Timer-based: ~5-10µs (scans ~89 packets)
- Continuous: ~250-400ns (scans 1 packet)

---

## 4. Implementation Plan

### 4.1 Phase A: Create `ackScanNoLock()` Function

**File**: `congestion/live/receive.go`

Create a new function `ackScanNoLock()` for EventLoop mode. The key differences from `periodicACK()`:
- **NO early exit check** (interval/lite threshold) - we always scan
- **NO locks** - EventLoop is single consumer of btree
- Simplified write phase (direct field updates)

#### 4.1.1 Existing `periodicACK()` Function (lines 758-965)

The existing function has these major sections:

```go
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 1: EARLY EXIT CHECK (lines 762-773) ← REMOVED in ackScanNoLock
    // ═══════════════════════════════════════════════════════════════════════
    r.lock.RLock()

    needLiteACK := false
    lightACKCount := r.metrics.RecvLightACKCounter.Load()
    if now-r.lastPeriodicACK < r.periodicACKInterval {
        if lightACKCount >= 64 {
            needLiteACK = true
        } else {
            r.lock.RUnlock()
            return // Early return - no ACK needed
        }
    }

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 2: GET MIN PACKET (lines 775-804) ← KEEP (for msBuf calculation)
    // ═══════════════════════════════════════════════════════════════════════
    minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
    ackSequenceNumber := r.lastACKSequenceNumber

    minPkt := r.packetStore.Min()
    if minPkt == nil {
        // Empty btree - send keepalive ACK with last known sequence
        r.lock.RUnlock()
        r.lock.Lock()
        defer r.lock.Unlock()
        return r.periodicACKWriteLocked(...)
    }
    minH := minPkt.Header()
    minPktTsbpdTime = minH.PktTsbpdTime
    maxPktTsbpdTime = minH.PktTsbpdTime
    minPktSeq := minH.PacketSequenceNumber

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 3: DETERMINE SCAN START POINT (lines 806-833) ← KEEP
    // High Water Mark optimization - only scan NEW packets
    // ═══════════════════════════════════════════════════════════════════════
    scanStartPoint := r.ackScanHighWaterMark

    // Handle edge cases:
    // 1. Not initialized → start from lastACKSequenceNumber
    // 2. Behind lastACK → start from lastACKSequenceNumber
    // 3. Behind minPkt (packets expired) → start from minPkt
    // 4. Valid → use high water mark
    if scanStartPoint.Val() == 0 || scanStartPoint.Lt(ackSequenceNumber) {
        scanStartPoint = ackSequenceNumber
    }
    if minPktSeq.Gt(scanStartPoint) {
        scanStartPoint = minPktSeq.Dec()
    }

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 4: ITERATE AND SCAN (lines 835-891) ← KEEP
    // Find contiguous sequence numbers, stop at first gap
    // ═══════════════════════════════════════════════════════════════════════
    var totalSkippedPkts uint64
    var lastContiguousSeq circular.Number
    firstPacketChecked := false

    r.packetStore.IterateFrom(scanStartPoint, func(p packet.Packet) bool {
        h := p.Header()

        // Skip packets at or before scan start
        if h.PacketSequenceNumber.Lte(scanStartPoint) {
            return true
        }

        // Check gap at first packet
        if !firstPacketChecked {
            firstPacketChecked = true
            if h.PktTsbpdTime > now && !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
                return false // Gap detected
            }
        }

        // Skip expired packets (count as lost)
        if h.PktTsbpdTime <= now {
            skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
            if skippedCount > 1 {
                totalSkippedPkts += skippedCount - 1
            }
            ackSequenceNumber = h.PacketSequenceNumber
            lastContiguousSeq = ackSequenceNumber
            return true
        }

        // Check if next in sequence
        if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
            ackSequenceNumber = h.PacketSequenceNumber
            lastContiguousSeq = ackSequenceNumber
            maxPktTsbpdTime = h.PktTsbpdTime
            return true
        }

        return false // Gap found
    })

    newHighWaterMark := lastContiguousSeq
    r.lock.RUnlock()

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 5: UPDATE METRICS (lines 899-906) ← KEEP
    // ═══════════════════════════════════════════════════════════════════════
    m := r.metrics
    if m != nil && totalSkippedPkts > 0 {
        avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
        m.CongestionRecvPktSkippedTSBPD.Add(totalSkippedPkts)
        m.CongestionRecvByteSkippedTSBPD.Add(totalSkippedPkts * avgPayloadSize)
    }

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 6: WRITE PHASE (lines 908-965) ← SIMPLIFY (no locks)
    // ═══════════════════════════════════════════════════════════════════════
    r.lock.Lock()
    defer r.lock.Unlock()
    return r.periodicACKWriteLocked(now, needLiteACK, ackSequenceNumber,
                                    minPktTsbpdTime, maxPktTsbpdTime, newHighWaterMark)
}
```

#### 4.1.2 New `ackScanNoLock()` Function (Proposed)

```go
// ackScanNoLock scans the packet btree to find the highest contiguous
// sequence number that can be acknowledged.
//
// Key differences from periodicACK():
// - NO early exit check (interval/lite threshold) - we always scan
// - NO locks - EventLoop is single consumer of btree
// - Always returns ok=true if there are packets to ACK
//
// Uses ACK Scan High Water Mark optimization: only scans NEW packets
// since last call (~96.7% reduction in iterations at steady state).
//
// Performance: ~250-400ns per call at 100Mb/s (scanning 1 packet).
//
// REQUIRES: Caller is single consumer of btree (EventLoop mode).
func (r *receiver) ackScanNoLock(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
    // ═══════════════════════════════════════════════════════════════════════
    // NO EARLY EXIT CHECK - We always scan when called
    // The caller (EventLoop) decides when to call us
    // ═══════════════════════════════════════════════════════════════════════

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 2: GET MIN PACKET (unchanged)
    // ═══════════════════════════════════════════════════════════════════════
    minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
    ackSequenceNumber := r.lastACKSequenceNumber

    minPkt := r.packetStore.Min()
    if minPkt == nil {
        // Empty btree - nothing to ACK
        // Unlike periodicACK, we don't send keepalive here (caller handles that)
        return false, circular.Number{}, false
    }
    minH := minPkt.Header()
    minPktTsbpdTime = minH.PktTsbpdTime
    maxPktTsbpdTime = minH.PktTsbpdTime
    minPktSeq := minH.PacketSequenceNumber

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 3: DETERMINE SCAN START POINT (unchanged)
    // ═══════════════════════════════════════════════════════════════════════
    scanStartPoint := r.ackScanHighWaterMark

    if scanStartPoint.Val() == 0 || scanStartPoint.Lt(ackSequenceNumber) {
        scanStartPoint = ackSequenceNumber
    }
    if minPktSeq.Gt(scanStartPoint) {
        scanStartPoint = minPktSeq.Dec()
    }

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 4: ITERATE AND SCAN (unchanged)
    // ═══════════════════════════════════════════════════════════════════════
    var totalSkippedPkts uint64
    var lastContiguousSeq circular.Number
    firstPacketChecked := false

    r.packetStore.IterateFrom(scanStartPoint, func(p packet.Packet) bool {
        h := p.Header()

        if h.PacketSequenceNumber.Lte(scanStartPoint) {
            return true
        }

        if !firstPacketChecked {
            firstPacketChecked = true
            if h.PktTsbpdTime > now && !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
                return false
            }
        }

        if h.PktTsbpdTime <= now {
            skippedCount := uint64(h.PacketSequenceNumber.Distance(ackSequenceNumber))
            if skippedCount > 1 {
                totalSkippedPkts += skippedCount - 1
            }
            ackSequenceNumber = h.PacketSequenceNumber
            lastContiguousSeq = ackSequenceNumber
            return true
        }

        if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
            ackSequenceNumber = h.PacketSequenceNumber
            lastContiguousSeq = ackSequenceNumber
            maxPktTsbpdTime = h.PktTsbpdTime
            return true
        }

        return false
    })

    newHighWaterMark := lastContiguousSeq

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 5: UPDATE METRICS (unchanged)
    // ═══════════════════════════════════════════════════════════════════════
    m := r.metrics
    if m != nil && totalSkippedPkts > 0 {
        avgPayloadSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
        m.CongestionRecvPktSkippedTSBPD.Add(totalSkippedPkts)
        m.CongestionRecvByteSkippedTSBPD.Add(totalSkippedPkts * avgPayloadSize)
    }

    // ═══════════════════════════════════════════════════════════════════════
    // SECTION 6: WRITE PHASE (SIMPLIFIED - no locks, no interval check)
    // ═══════════════════════════════════════════════════════════════════════

    // Check if we made progress
    if ackSequenceNumber.Equals(r.lastACKSequenceNumber) {
        // No new contiguous packets found
        return false, circular.Number{}, false
    }

    // Track that ackScan ran (for health monitoring)
    if m != nil {
        m.CongestionRecvPeriodicACKRuns.Add(1)
    }

    // Update state (NO LOCK - EventLoop is single consumer)
    ok = true
    sequenceNumber = ackSequenceNumber.Inc()

    r.lastACKSequenceNumber = ackSequenceNumber

    if newHighWaterMark.Val() > 0 && newHighWaterMark.Gt(r.ackScanHighWaterMark) {
        r.ackScanHighWaterMark = newHighWaterMark
    }

    // Don't update lastPeriodicACK - no interval check in this version
    // Don't reset RecvLightACKCounter - not used in continuous mode

    // Calculate buffer time for congestion control
    msBuf := (maxPktTsbpdTime - minPktTsbpdTime) / 1_000
    m.CongestionRecvMsBuf.Store(msBuf)

    // lite=false always (caller can decide to send lite ACK based on modulus)
    return ok, sequenceNumber, false
}
```

#### 4.1.3 Summary of Changes

| Aspect | `periodicACK()` | `ackScanNoLock()` |
|--------|-----------------|-------------------|
| **Early exit check** | Yes (interval + lite threshold) | **NO** |
| **RLock/RUnlock** | Yes | **NO** |
| **WLock/WUnlock** | Yes | **NO** |
| **Interval tracking** | Updates `lastPeriodicACK` | **NO** |
| **Lite ACK counter** | Resets `RecvLightACKCounter` | **NO** |
| **Empty btree** | Sends keepalive ACK | Returns `ok=false` |
| **No progress** | Returns based on interval | Returns `ok=false` |

The `ackScanNoLock()` function is simpler because:
1. The caller decides when to scan (no interval check needed)
2. The caller decides when to send ACK (no lite threshold needed)
3. No lock contention possible (single consumer pattern)

### 4.2 Phase B: Add ACKModulus Configuration

**File**: `config.go` (after line 381, EventLoop section)

```go
// ACKModulus controls how often ACK packets are sent in EventLoop mode.
// When > 0, ACK packets are sent every ACKModulus consecutive in-sequence packets.
// Example: ACKModulus=10 means send ACK after 10, 20, 30... consecutive packets.
// Set to 1 for legacy behavior (ACK every sequential packet).
// Set to 0 to disable continuous ACK (use timer-based ACK only - not recommended).
// Default: 10 (reduces ACK packets by ~10x while maintaining low RTT)
ACKModulus uint64
```

**Default value** (after line 490):
```go
ACKModulus: 10, // Send ACK every 10 consecutive packets
```

**Validation** (after line 1135):
```go
if c.UseEventLoop && c.ACKModulus == 0 {
    // Warning: ACKModulus=0 means no continuous ACK, only timer-based
    // This defeats the purpose of the optimization
}
```

### 4.3 Phase C: Add CLI Flag

**File**: `contrib/common/flags.go` (after line 104)

```go
ACKModulus = flag.Int("ackmodulus", -1, "ACK every N consecutive packets in EventLoop mode (default: 10, 1=every packet, requires -useeventloop)")
```

**Flag application** (after line 377):
```go
if FlagSet["ackmodulus"] && *ACKModulus >= 0 {
    config.ACKModulus = uint64(*ACKModulus)
}
```

### 4.4 Phase D: Modify EventLoop

**File**: `congestion/live/receive.go` lines 1784-1882

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // ... existing setup ...

    // ACK modulus for reducing ACK packet frequency
    ackModulus := r.ackModulus
    if ackModulus == 0 {
        ackModulus = 10 // Default if not set
    }
    consecutiveInSeq := uint64(0)

    // REMOVED: ackTicker - no longer needed
    // ackTicker := time.NewTicker(ackInterval)
    // defer ackTicker.Stop()

    // Keep NAK and rate tickers
    nakTicker := time.NewTicker(nakInterval)
    defer nakTicker.Stop()
    rateTicker := time.NewTicker(rateInterval)
    defer rateTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return

        // REMOVED: case <-ackTicker.C:

        case <-nakTicker.C:
            r.drainRingByDelta()
            now := uint64(time.Now().UnixMicro())
            if list := r.periodicNAK(now); len(list) != 0 {
                metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
                r.sendNAK(list)
            }
            if r.useNakBtree && r.nakBtree != nil {
                r.expireNakEntries()
            }

        case <-rateTicker.C:
            now := uint64(time.Now().UnixMicro())
            r.updateRateStats(now)

        default:
            now := uint64(time.Now().UnixMicro())
            processed := r.processOnePacket()
            delivered := r.deliverReadyPacketsNoLock(now)

            // NEW: Run ACK scan after every delivery
            // The btree was just updated - scan from high water mark
            if processed || delivered > 0 {
                if ok, seq, lite := r.ackScanNoLock(now); ok {
                    // Track consecutive in-sequence packets
                    if seq.Gt(r.lastACKSequenceNumber) {
                        consecutiveInSeq++
                    }

                    // Send ACK based on modulus (or always for lite ACK)
                    if consecutiveInSeq % ackModulus == 0 || lite {
                        r.sendACK(seq, lite)
                    }
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

### 4.5 Phase E: Run Flag Tests

```bash
cd /home/das/Downloads/srt/gosrt
./contrib/common/test_flags.sh
```

Add test case for new flag:
```bash
# ACKModulus flag test
run_test "ACKModulus default" \
    "-useeventloop -usepacketring" \
    "ACKModulus.*10" \
    "$SERVER_BIN"

run_test "ACKModulus=1" \
    "-useeventloop -usepacketring -ackmodulus 1" \
    "ACKModulus.*1" \
    "$SERVER_BIN"

run_test "ACKModulus=50" \
    "-useeventloop -usepacketring -ackmodulus 50" \
    "ACKModulus.*50" \
    "$SERVER_BIN"
```

---

## 5. Testing Plan

### 5.1 Unit Tests

**File**: `congestion/live/receive_test.go`

Add tests for:
- `ackScanNoLock()` correctly advances ACK sequence
- ACKModulus=1 sends ACK every packet
- ACKModulus=10 sends ACK every 10th packet
- ACKModulus behavior with out-of-order packets (counter resets)

### 5.2 Integration Test Matrix Update

**File**: `contrib/integration_testing/configs.go`

Add `ACKModulus` as a new dimension for EventLoop tests:

| ACKModulus | Behavior | Test Abbrev |
|------------|----------|-------------|
| 1 | ACK every packet (legacy-like) | `Ack1` |
| 10 | ACK every 10th packet (default) | `Ack10` |
| 50 | ACK every 50th packet (aggressive) | `Ack50` |

**New config variants** (EventLoop only):

| Config Name | Features |
|-------------|----------|
| `FullEL_Ack1` | Full + EventLoop + ACKModulus=1 |
| `FullEL_Ack10` | Full + EventLoop + ACKModulus=10 |
| `FullEL_Ack50` | Full + EventLoop + ACKModulus=50 |

### 5.3 Test Commands

**Isolation tests** (single server/client, no network impairment):
```bash
make test-isolation-starlink-5Mbps-EL-Ack1
make test-isolation-starlink-5Mbps-EL-Ack10
make test-isolation-starlink-5Mbps-EL-Ack50
```

**Parallel comparison tests** (Base vs EventLoop variants):
```bash
make test-parallel-starlink-5Mbps-Base-vs-FullEL_Ack1
make test-parallel-starlink-5Mbps-Base-vs-FullEL_Ack10
make test-parallel-starlink-5Mbps-Base-vs-FullEL_Ack50
```

### 5.4 Success Criteria

| Metric | Current (EventLoop) | Target (ACKModulus=10) | Target (ACKModulus=1) |
|--------|---------------------|------------------------|----------------------|
| RTT (MsRTT) | ~10ms | <2ms | <1ms |
| ACK packets/sec | ~100 | ~890 | ~8,900 |
| CPU overhead | ~0.05% | ~0.25% | ~0.35% |
| Packet loss | baseline | ≤ baseline | ≤ baseline |
| Retransmissions | baseline | ≤ baseline | ≤ baseline |

**Key validation**: RTT should decrease from ~10ms to <2ms with ACKModulus=10, and <1ms with ACKModulus=1.

---

## 6. Code Locations Summary

| File | Line | Change |
|------|------|--------|
| `congestion/live/receive.go` | 758-922 | Rename `periodicACK` → `ackScan`, create `ackScanNoLock` |
| `congestion/live/receive.go` | 1784-1882 | Modify `EventLoop()`: remove `ackTicker`, add continuous ACK |
| `config.go` | ~381 | Add `ACKModulus` field |
| `config.go` | ~490 | Add `ACKModulus` default value (10) |
| `config.go` | ~1135 | Add `ACKModulus` validation |
| `contrib/common/flags.go` | ~104 | Add `-ackmodulus` flag |
| `contrib/common/flags.go` | ~377 | Apply `ackmodulus` flag to config |
| `contrib/common/test_flags.sh` | EOF | Add ACKModulus test cases |
| `contrib/integration_testing/configs.go` | TBD | Add `FullEL_Ack1/10/50` configs |

---

## 7. Rollout Plan

### 7.1 Development (This PR)

1. Implement Phase A-D code changes
2. Run `test_flags.sh` to validate flag parsing
3. Run unit tests
4. Run isolation tests with ACKModulus=1,10,50

### 7.2 Validation

1. Run parallel comparison tests
2. Verify RTT reduction in test results
3. Check for regressions in packet loss / retransmissions
4. Document results in `lockless_phase5_implementation.md`

### 7.3 Default Behavior

- `ACKModulus=10` as default (good balance of RTT vs overhead)
- Users can set `ACKModulus=1` for minimum RTT (legacy-like behavior)
- Users can set `ACKModulus=50+` for minimum overhead (higher latency environments)

---

## 8. Appendix: ACKModulus Trade-offs

From `ack_optimization.md` benchmarks at 100 Mb/s:

| ACKModulus | ACK Latency | ACKs/sec | CPU Overhead |
|------------|-------------|----------|--------------|
| Timer (10ms) | 5ms avg | ~100 | ~0.05% |
| 50 | ~5.6ms | ~180 | ~0.25% |
| 10 | ~1.1ms | ~890 | ~0.25% |
| 1 | <0.1ms | ~8,900 | ~0.35% |

**Recommendation**: Start with `ACKModulus=10` (default). This provides:
- ~10x RTT reduction (10ms → ~1ms)
- ~10x more ACKs than timer-based
- Only ~5x more CPU than timer-based (~0.25% vs ~0.05%)

