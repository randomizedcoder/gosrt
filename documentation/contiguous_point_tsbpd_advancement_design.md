# ContiguousPoint TSBPD-Based Advancement Design

## Problem Statement

The current design in `ack_optimization_plan.md` Section 3.1-3.2 describes `contiguousPoint` as the unified scan starting point that advances when contiguous packets arrive. However, the design **does not address** what happens when packets are permanently lost and never arrive.

### The Core Issue

`contiguousPoint` can get **stuck forever** if:
1. The packet at `contiguousPoint + 1` never arrives
2. NAK retransmission requests are also lost
3. The sender gives up or the connection has gaps

In this state:
- `contiguousScan` can never advance (waiting for a packet that won't come)
- ACKs stop progressing
- Delivery stops (waiting for contiguous sequence)
- The receiver is effectively frozen

### Real-World Scenarios (Unit Test Cases)

Based on `integration_testing_with_network_impairment_defects.md`, these scenarios WILL occur.

**These scenarios form the basis for unit tests**:
- First, write tests that demonstrate the BROKEN behavior (contiguousPoint stuck)
- Then, implement the fix
- Finally, verify tests now PASS (contiguousPoint advances correctly)

#### Scenario 1: Complete Network Outage

**Test Name**: `TestTSBPDAdvancement_CompleteOutage`

```
Timeline:
t=0      : Packets 1-100 received, contiguousPoint=100
t=0-3s   : Network outage, NO packets arrive
t=3s     : Network recovers, packets 200+ start arriving

State at t=3s:
- contiguousPoint = 100 (stuck!)
- Packets 101-199 will NEVER arrive
- TSBPD buffer for packets 101+ has EXPIRED
- New packets 200+ are arriving but can't be processed
```

**Test Assertions (Before Fix - Expected FAIL)**:
- `contiguousPoint` remains at 100 (stuck)
- Packets 200+ are rejected as "too_old"
- `CongestionRecvPktSkippedTSBPD` = 0 (metric doesn't exist yet)

**Test Assertions (After Fix - Expected PASS)**:
- `contiguousPoint` advances to 199 (btree.Min()-1)
- Packets 200+ are accepted and processed
- `CongestionRecvPktSkippedTSBPD` = 99 (packets 101-199 skipped)

#### Scenario 2: Large Mid-Stream Gap

**Test Name**: `TestTSBPDAdvancement_MidStreamGap`

```
Timeline:
t=0      : Packets 1-100 received, contiguousPoint=100
t=0.1s   : Packets 101-150 lost in transit
t=0.2s   : Packets 151-200 arrive (out of order, in btree)
t=0.5s   : NAK sent for 101-150
t=1.0s   : Retransmissions also lost
t=1.5s   : NAK resent
t=3.0s   : TSBPD expired for packets 101-150

State at t=3s:
- contiguousPoint = 100 (stuck!)
- Packets 101-150 TSBPD has expired - they're unrecoverable
- Packets 151-200 are in btree but can't be delivered
- New packets continue arriving
```

**Test Assertions (Before Fix - Expected FAIL)**:
- `contiguousPoint` remains at 100 (stuck)
- Packets 151-200 in btree but not delivered
- ACK stuck at 100

**Test Assertions (After Fix - Expected PASS)**:
- `contiguousPoint` advances to 150 when packet 151's TSBPD expires
- Packets 151-200 become deliverable
- `CongestionRecvPktSkippedTSBPD` = 50 (packets 101-150 skipped)

#### Scenario 3: Tens of Seconds Gap

**Test Name**: `TestTSBPDAdvancement_ExtendedOutage`

```
Timeline:
t=0       : Packets 1-1000 received, contiguousPoint=1000
t=0-30s   : Severe network issues, packet loss 80%+
t=30s     : Network stabilizes

State at t=30s:
- contiguousPoint could be anywhere from 1000 to current
- Thousands of packets may have expired TSBPD
- System must recover gracefully
```

**Test Assertions (After Fix - Expected PASS)**:
- `contiguousPoint` advances multiple times as gaps expire
- System recovers to normal operation
- Total skipped packets tracked in metrics

#### Scenario 4: Ring Out-of-Order (Current Bug)

**Test Name**: `TestTSBPDAdvancement_RingOutOfOrder`

This is the specific bug we're currently experiencing:

```
Timeline:
t=0      : io_uring receives packets 1-10
t=0.001s : Ring round-robin reads packet 4 first (shard 0)
t=0.002s : Packet 4 inserted into btree
t=0.003s : contiguousScan finds gap at 1-3, no advancement
t=0.004s : Stale gap handling INCORRECTLY jumps contiguousPoint
t=0.005s : Packets 1-3 read from ring, rejected as "too_old"
```

**Test Assertions (Before Fix - Expected FAIL)**:
- Packets 1-3 dropped as "too_old"
- `CongestionRecvDataDropTooOld` > 0

**Test Assertions (After Fix - Expected PASS)**:
- All packets 1-10 delivered (no drops)
- `CongestionRecvDataDropTooOld` = 0
- `CongestionRecvPktSkippedTSBPD` = 0 (no TSBPD expiry in this test)

### Test File Location

**File**: `congestion/live/receive_stream_test.go`

Add tests to the existing test file, which already has the matrix-based testing framework including:
- `ReceiverConfig` definitions
- `StreamProfile` definitions
- `LossPattern` and `OutOfOrderPattern` types
- `createMatrixReceiver()` helper
- `generateMatrixStream()` helper

The new TSBPD advancement tests will be added as a separate section after the existing tier tests.

```go
// ============================================================================
// TSBPD ADVANCEMENT TESTS
// ============================================================================
// These tests verify contiguousPoint advancement when packets are permanently
// lost or significantly delayed beyond their TSBPD deadline.
// See documentation/contiguous_point_tsbpd_advancement_design.md

func TestTSBPDAdvancement_CompleteOutage(t *testing.T) { ... }
func TestTSBPDAdvancement_MidStreamGap(t *testing.T) { ... }
func TestTSBPDAdvancement_ExtendedOutage(t *testing.T) { ... }
func TestTSBPDAdvancement_RingOutOfOrder(t *testing.T) { ... }
```

### Test Helper Requirements

The TSBPD tests need precise time control, which may require:
1. Using the `nowFn` injectable time function in the receiver
2. Creating packets with specific `PktTsbpdTime` values
3. Calling `Tick()` at controlled times to trigger TSBPD expiry

The existing `generateMatrixStream()` already sets `PktTsbpdTime` correctly:
```go
p.Header().PktTsbpdTime = startTimeUs + uint64(i)*packetIntervalUs + profile.TsbpdDelayUs
```

---

## Design Requirements

### R1: TSBPD-Based ContiguousPoint Advancement

When a packet at `contiguousPoint + 1` has its TSBPD time expire WITHOUT arriving:
- The packet is considered **permanently lost**
- `contiguousPoint` MUST advance past it
- This is called "TSBPD-based advancement" or "virtual delivery"

### R2: No False Advancement

`contiguousPoint` should NOT advance prematurely:
- Must wait until TSBPD actually expires (not just because btree.Min() is far ahead)
- Packets might still arrive via retransmission up until TSBPD expiry

### R3: Metrics for Unrecoverable Loss

Track packets that were "virtually delivered" (skipped due to TSBPD expiry):
- `CongestionRecvPktSkippedTSBPD` - count of packets skipped
- `CongestionRecvByteSkippedTSBPD` - estimated bytes skipped

### R4: ACK Must Reflect Reality

ACK sequence number should reflect what was actually received OR what was skipped:
- If packets 101-150 were skipped, ACK should indicate 150 (or higher)
- Sender needs to know these packets don't need retransmission

### R5: Delivery Must Continue

After TSBPD-based advancement:
- Packets beyond the gap should be deliverable
- System should return to normal operation

---

## Proposed Design

### Key Insight: TSBPD Time is the Authority

The TSBPD (Time Stamped Based Packet Delivery) time is the definitive deadline:
- If `now > packet.expectedTSBPDTime` and packet hasn't arrived → it's lost
- We know the expected TSBPD time for `contiguousPoint + 1` even if the packet hasn't arrived
- Expected TSBPD = `contiguousPoint.tsbpdTime + interPacketInterval`

### TSBPD Calculation for Missing Packets

For a missing packet at sequence `seq`:
```
expectedTSBPD(seq) = lastKnownTSBPD + (seq - lastKnownSeq) * avgInterPacketInterval
```

Or more simply, if we have the packet immediately after the gap:
```
// If packet 151 is in btree and packet 101-150 are missing:
// Packet 151's TSBPD gives us a reference point
// Packet 101 should have arrived ~50 packets earlier
// If packet 151's TSBPD has passed, packets 101-150 are definitely expired
```

### Proposed Algorithm: TSBPD-Aware ContiguousScan

```go
func (r *receiver) contiguousScanWithTSBPDRecovery(now uint64) (ok bool, ackSeq uint32, skipped uint64) {
    lastContiguous := r.contiguousPoint.Load()
    expectedNext := circular.SeqAdd(lastContiguous, 1)

    minPkt := r.packetStore.Min()
    if minPkt == nil {
        return false, 0, 0 // Empty btree
    }

    minSeq := minPkt.Header().PacketSequenceNumber.Val()
    minTSBPD := minPkt.Header().PktTsbpdTime

    // Case 1: Next expected packet is in btree (normal case)
    if minSeq == expectedNext {
        // Normal contiguous scan - advance as usual
        return r.normalContiguousScan(now, lastContiguous)
    }

    // Case 2: Gap exists between contiguousPoint and btree.Min
    // Check if the missing packets' TSBPD has expired

    // The missing packets (expectedNext to minSeq-1) should have arrived
    // BEFORE minPkt. If minPkt's TSBPD has passed, the missing packets
    // are definitely unrecoverable.

    if now > minTSBPD {
        // minPkt's TSBPD has passed, meaning ALL packets before it
        // (including the missing ones) are past their TSBPD deadline.
        //
        // Advance contiguousPoint to just before minSeq (skip the gap)
        skippedCount := circular.SeqSub(minSeq, expectedNext)

        // Update contiguousPoint atomically
        newContiguous := circular.SeqSub(minSeq, 1)
        r.contiguousPoint.Store(newContiguous)

        // Log the TSBPD-based advancement
        r.log("receiver:tsbpd:skip", func() string {
            return fmt.Sprintf("TSBPD advancement: skipped %d packets (%d to %d), now=%d, minTSBPD=%d",
                skippedCount, expectedNext, newContiguous, now, minTSBPD)
        })

        // Track metrics
        if r.metrics != nil {
            r.metrics.CongestionRecvPktSkippedTSBPD.Add(uint64(skippedCount))
            // Estimate bytes based on average packet size
            avgSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
            r.metrics.CongestionRecvByteSkippedTSBPD.Add(uint64(skippedCount) * avgSize)
        }

        // Now continue with normal scan from the new contiguousPoint
        return r.normalContiguousScan(now, newContiguous)
    }

    // Case 3: Gap exists but TSBPD hasn't expired yet
    // The missing packets might still arrive via retransmission
    // Do NOT advance - wait for NAK/retransmission or TSBPD expiry
    return false, 0, 0
}
```

### Key Differences from Current "Stale Gap" Handling

| Aspect | Current (staleGapThreshold) | Proposed (TSBPD-aware) |
|--------|----------------------------|------------------------|
| **Trigger** | Gap size >= 64 packets | Gap AND minPkt.TSBPD expired |
| **Assumption** | "Packets were delivered" | "Packets are unrecoverable" |
| **Timing** | Immediate (too aggressive) | Waits for TSBPD deadline |
| **Correctness** | Wrong (packets in ring) | Correct (uses TSBPD time) |
| **Design alignment** | Not in design | Follows TSBPD principles |

---

## Edge Cases

### Edge Case 1: Very Long Outage

If outage lasts longer than btree retention:
- Btree might be empty
- `contiguousPoint` is way behind current time
- Need to handle empty btree case

**Solution**: If btree is empty and no packets for > TSBPD interval:
```go
if minPkt == nil && (now - r.lastPacketReceivedTime) > r.tsbpdDelay {
    // No packets for a long time
    // When packets resume, first packet will trigger advancement
    return false, 0, 0
}
```

### Edge Case 2: Packet Reordering vs Loss

How to distinguish:
- **Reordering**: Packet will arrive soon (within RTT)
- **Loss**: Packet won't arrive, TSBPD will expire

**Solution**: Use TSBPD as the definitive deadline:
- If TSBPD hasn't expired → might be reordering, wait
- If TSBPD has expired → definitely lost, advance

### Edge Case 3: Clock Skew

If sender and receiver clocks are skewed:
- TSBPD calculations might be off
- Could cause premature or delayed advancement

**Solution**: TSBPD already accounts for latency estimation and RTT.
The `tsbpdDelay` config value provides buffer for clock differences.

### Edge Case 4: Wraparound

Sequence numbers wrap at 31 bits. The circular arithmetic functions
handle this, but TSBPD-based advancement must also handle it correctly.

**Solution**: Use `circular.SeqSub()` and `circular.SeqLess()` which
handle wraparound.

---

## Integration with Existing Design

### Where This Fits in ack_optimization_plan.md

This design extends Section 3.1 "Unified Scan Starting Point":

**Current design says**:
> "contiguousScan runs every iteration... Advances contiguousPoint forward on contiguous arrivals"

**Extended design adds**:
> "contiguousScan also advances contiguousPoint when packets are TSBPD-expired (unrecoverable loss)"

### Visualization Update

```
                        Packet Btree (sorted by sequence number)
    ═══════════════════════════════════════════════════════════════════════════►
                                                                    sequence →

    InitialSequenceNumber                                              maxSeen
           │                                                              │
           ▼                                                              ▼
    ┌──────┬────────────────────────────────────────────────────────────────┐
    │ ISN  │  1   2   3   ·   ·   ·   7   8   ·   ·   11  12  13  ·   15    │
    └──────┴────────────────────────────────────────────────────────────────┘
                      │   └─────────┘                │
                      │    TSBPD    │                │
                 contiguousPoint   EXPIRED       btree.Min()
                      (=3)         (4,5,6)          (=7)
                                   ↓
                              SKIP THESE
                                   ↓
                         contiguousPoint → 6

    ════════════════════════════════════════════════════════════════════════════

    NEW REGION: TSBPD-EXPIRED GAP

    │◄─── CONTIGUOUS ─────►│◄─ TSBPD EXPIRED ─►│◄─── IN BTREE ───────►│
    │   (already ACKed)    │   (unrecoverable)  │  (waiting for cont.) │
    │                      │                    │                      │
    │  seq 1-3 delivered   │  seq 4-6 LOST     │  seq 7-8, 11-15      │
    │                      │  skip these!      │  can't deliver yet   │

    ════════════════════════════════════════════════════════════════════════════

    TSBPD-BASED ADVANCEMENT:

    1. contiguousPoint = 3, expecting packet 4
    2. btree.Min() = 7 (packets 4,5,6 missing)
    3. Check: now > packet7.TSBPD?
       - If YES: packets 4,5,6 are DEFINITELY expired (they should have arrived
         before packet 7, and packet 7's deadline has passed)
       - If NO: wait, packets 4,5,6 might still arrive via retransmission
    4. If YES: advance contiguousPoint to 6 (skip 4,5,6)
    5. Now normal scan can continue from 6 → 7, 8, ...
```

---

## Metrics

### Reference Design

See `documentation/metrics_and_statistics_design.md` for the metrics implementation pattern.

### New Metrics Required

| Metric Field | Prometheus Name | Description |
|--------------|-----------------|-------------|
| `CongestionRecvPktSkippedTSBPD` | `gosrt_connection_congestion_recv_pkt_skipped_tsbpd` | Packets skipped due to TSBPD expiry |
| `CongestionRecvByteSkippedTSBPD` | `gosrt_connection_congestion_recv_byte_skipped_tsbpd` | Bytes skipped (estimated) |
| `ContiguousPointTSBPDAdvancements` | `gosrt_connection_contiguous_point_tsbpd_advancements` | Count of TSBPD-based advancements |
| `ContiguousPointTSBPDSkippedTotal` | `gosrt_connection_contiguous_point_tsbpd_skipped_total` | Total packets skipped across all advancements |

### Implementation Checklist

| Step | File | Description |
|------|------|-------------|
| M.1 | `metrics/metrics.go` | Add `atomic.Uint64` fields for new metrics |
| M.2 | `metrics/handler.go` | Add Prometheus export calls (`writeCounterIfNonZero`) |
| M.3 | `metrics/handler_test.go` | Add unit tests for new metric exports |
| M.4 | `congestion/live/receive.go` | Increment metrics at TSBPD advancement |
| M.5 | `contrib/integration_testing/analysis.go` | Add metrics to post-test analysis output |
| M.6 | `contrib/integration_testing/test_isolation_mode.go` | Add rows to results table |
| M.7 | Run `make audit-metrics` | Verify no unused/unexported metrics |

### Verification Command

```bash
make audit-metrics
# Runs: go run tools/metrics-audit/main.go
# Should report: ✅ Fully Aligned for new metrics
```

### Existing Metrics to Preserve

- `CongestionRecvPktBelated` - packets that arrived after their TSBPD expired
- `CongestionRecvPktDrop` - total dropped packets (includes TSBPD skipped)
- `CongestionRecvDataDropTooOld` - current "too_old" drops (will change semantics)

---

## Implementation Plan

### Phase 1: Design Review ✅ COMPLETE

1. ✅ Review this document
2. ✅ Identify edge cases (see Edge Cases section)
3. ✅ Decide on approach (see Design Decisions section)

---

### Phase 2: Write Unit Tests FIRST (Test-Driven Development)

**Goal**: Create tests based on Real-World Scenarios that:
1. Demonstrate the BROKEN behavior before fix
2. Verify the CORRECT behavior after fix

**File**: `congestion/live/receive_stream_test.go` (existing test file)

Add tests to the existing matrix testing framework, leveraging:
- `createMatrixReceiver()` for receiver setup
- `generateMatrixStream()` for packet generation
- Existing `StreamProfile` and `ReceiverConfig` definitions

| Step | Test Name | Description |
|------|-----------|-------------|
| 2.1 | `TestTSBPDAdvancement_RingOutOfOrder` | Current bug: ring out-of-order causes "too_old" drops |
| 2.2 | `TestTSBPDAdvancement_CompleteOutage` | 3s network outage, contiguousPoint must advance |
| 2.3 | `TestTSBPDAdvancement_MidStreamGap` | Mid-stream gap, TSBPD expiry triggers advancement |
| 2.4 | `TestTSBPDAdvancement_ExtendedOutage` | 30s outage with recovery |

**Test Workflow**:
```bash
# Step 1: Write tests
# Step 2: Run tests - expect FAIL (broken behavior)
go test -v -run TestTSBPDAdvancement ./congestion/live/

# Step 3: Implement fix (Phases 3-5)
# Step 4: Run tests again - expect PASS
go test -v -run TestTSBPDAdvancement ./congestion/live/
```

---

### Phase 3: Add New Metrics

**Goal**: Add metrics for TSBPD-based advancement tracking.

| Step | File | Change |
|------|------|--------|
| 3.1 | `metrics/metrics.go` | Add `CongestionRecvPktSkippedTSBPD`, `CongestionRecvByteSkippedTSBPD`, `ContiguousPointTSBPDAdvancements` |
| 3.2 | `metrics/handler.go` | Add Prometheus exports with `writeCounterIfNonZero` |
| 3.3 | `metrics/handler_test.go` | Add unit tests for new exports |
| 3.4 | `contrib/integration_testing/analysis.go` | Add to post-test analysis |
| 3.5 | `contrib/integration_testing/test_isolation_mode.go` | Add rows to output table |

**Verification**:
```bash
make audit-metrics
# Should report: ✅ Fully Aligned for new metrics
```

---

### Phase 4: Replace `lastDeliveredSequenceNumber` with `contiguousPoint`

**Goal**: Simplify by using `contiguousPoint` as the unified boundary.

| Step | File | Line | Change |
|------|------|------|--------|
| 4.1 | `receive.go` | 191 | Remove `lastDeliveredSequenceNumber` field |
| 4.2 | `receive.go` | 296 | Remove initialization |
| 4.3 | `receive.go` | 1957 | Change check to `contiguousPoint` |
| 4.4 | `receive.go` | 2084 | Change check to `contiguousPoint` |
| 4.5 | `receive.go` | 2484 | Change check to `contiguousPoint` |
| 4.6 | `receive.go` | 2568 | Remove update in delivery |
| 4.7 | Various | - | Update any other references |

**Code Change**:
```go
// BEFORE:
if seq.Lte(r.lastDeliveredSequenceNumber) {

// AFTER:
if seq.Val() <= r.contiguousPoint.Load() {
```

---

### Phase 5: Replace Stale Gap Handling with TSBPD-Aware Advancement

**Goal**: Use TSBPD time instead of arbitrary gap threshold.

| Step | File | Line | Change |
|------|------|------|--------|
| 5.1 | `receive.go` | 853-877 | Replace stale gap in `contiguousScanWithTime` |
| 5.2 | `receive.go` | 979-998 | Replace stale gap in `gapScan` |
| 5.3 | `receive.go` | 1638-1649 | Replace stale gap in `periodicNakBtreeLocked` |

**Code Change**:
```go
// BEFORE:
if gapSize >= staleGapThreshold {
    lastContiguous = minSeq - 1
}

// AFTER:
if gapSize > 0 && now > minPkt.Header().PktTsbpdTime {
    // TSBPD expired - packets are unrecoverable
    skippedCount := gapSize
    lastContiguous = circular.SeqSub(minSeq, 1)

    // Track metrics
    if r.metrics != nil {
        r.metrics.CongestionRecvPktSkippedTSBPD.Add(uint64(skippedCount))
        r.metrics.ContiguousPointTSBPDAdvancements.Add(1)
        // Estimate bytes
        avgSize := uint64(math.Float64frombits(r.avgPayloadSizeBits.Load()))
        r.metrics.CongestionRecvByteSkippedTSBPD.Add(uint64(skippedCount) * avgSize)
    }

    // Log the advancement
    r.log("receiver:tsbpd:skip", func() string {
        return fmt.Sprintf("TSBPD advancement: skipped %d packets, new contiguousPoint=%d",
            skippedCount, lastContiguous)
    })
}
```

---

### Phase 6: Review Full ACK Ticker Else Branch

**Goal**: Verify else branch (lines 2354-2368) works correctly with new logic.

With TSBPD-aware advancement:
- If gap exists and TSBPD expired → contiguousPoint advances → ACK sent
- If gap exists and TSBPD not expired → wait (correct behavior)
- Else branch still needed for periodic RTT measurement

**Decision**: Keep else branch for now, but verify it works correctly.

---

### Phase 7: Run Unit Tests to Verify Fix

```bash
# All TSBPD advancement tests should now PASS
go test -v -run TestTSBPDAdvancement ./congestion/live/

# Run all receive tests
go test -v ./congestion/live/ -short
```

---

### Phase 8: Integration Testing

```bash
# Run isolation test with io_uring
sudo PRINT_PROM=true make test-isolation CONFIG=Isolation-5M-FullEventLoop
```

**Verify**:
1. ✅ No "too_old" drops from ring timing issues
2. ✅ TSBPD skips tracked in new metrics
3. ✅ RTT back to control levels
4. ✅ `make audit-metrics` passes

---

### Phase 9: Review Sender-Side TSBPD Handling (TODO - Future Work)

**Goal**: Ensure sender can also advance when packets TSBPD-expire.

**File**: `congestion/live/send.go`

The sender tracks packets waiting for ACK. If those packets' TSBPD expires:
- Sender should stop waiting for ACK
- Sender's "unACKed" tracking should advance
- May need similar `btree.Min()-1` pattern

**TODO**: Create separate design document for sender-side TSBPD advancement.

---

## Design Decisions (Reviewed 2025-12-28)

### Decision 1: TSBPD Time is the Authority ✅

**Question**: Is TSBPD time the right authority for "packet is unrecoverable"?

**Answer**: YES. TSBPD time is the definitive deadline. If `now > minPkt.TSBPD` and
earlier packets haven't arrived, they are unrecoverable.

### Decision 2: Skip Entire Gap to btree.Min()-1 ✅

**Question**: Should we skip one packet at a time or the entire gap?

**Answer**: Skip entire gap to `btree.Min()-1`. This is efficient and matches
the logical model - if btree.Min()'s TSBPD has passed, ALL earlier packets are expired.

### Decision 3: Sender Also Needs TSBPD-Based Advancement ⚠️ TODO

**Question**: How does this interact with the sender's congestion control?

**Answer**: The sender also needs a similar mechanism. When sender-side packets
have their TSBPD expire, the sender should:
- Stop waiting for ACKs for those packets
- Advance its "sent but not ACKed" tracking
- Possibly use `btree.Min()-1` pattern as well

**TODO**: Review sender-side congestion control (`congestion/live/send.go`) and
implement similar TSBPD-based advancement for sender's ACK tracking.

### Decision 4: ACK Reflects Advanced ContiguousPoint ✅

**Question**: What about the Full ACK ticker else branch?

**Answer**: With TSBPD-aware advancement, the Full ACK can be based on the
just-advanced `contiguousPoint`. The ACK message tells sender "next packet I
expect is X", which correctly reflects the skipped gap.

The else branch in Full ACK ticker (line 2354-2368) may still be needed for
cases where no TSBPD advancement occurred but we still need to send periodic
Full ACKs for RTT measurement.

### Decision 5: Use `contiguousPoint` Instead of `lastDeliveredSequenceNumber` ✅

**Question**: Should `lastDeliveredSequenceNumber` be updated when skipping?

**Answer**: **Replace `lastDeliveredSequenceNumber` with `contiguousPoint`**.

The design uses `contiguousPoint` as the unified boundary:
- Everything ≤ `contiguousPoint` is "handled" (delivered or skipped)
- New packets with seq ≤ `contiguousPoint` should be rejected

This simplifies the code:
- Remove `lastDeliveredSequenceNumber` field
- Use `contiguousPoint` for the "too old" check
- Fewer variables to track and synchronize

### Decision 6: `contiguousPoint` Semantic Change ✅

**IMPORTANT**: This is a critical semantic change from `lastDeliveredSequenceNumber`:

| Aspect | `lastDeliveredSequenceNumber` (old) | `contiguousPoint` (new) |
|--------|-------------------------------------|-------------------------|
| **When it advances** | Only during delivery (TSBPD passed) | During `contiguousScan` for all contiguous packets |
| **Meaning** | "Last packet delivered to application" | "Last contiguous packet received" |
| **TSBPD dependency** | Only advances when TSBPD <= now | Advances regardless of TSBPD |
| **Update location** | `deliverReadyPacketsWithTime()` | `contiguousScanWithTime()` |

**Example showing the difference**:
```
Packets in btree: 0-9 (contiguous)
Packets 0-4: tsbpdTime 1-5 (TSBPD-ready at now=10)
Packets 5-9: tsbpdTime 16-20 (NOT TSBPD-ready at now=10)

OLD behavior (lastDeliveredSequenceNumber):
  - Only packets 0-4 are delivered (TSBPD passed)
  - lastDeliveredSequenceNumber = 4
  - Pushing duplicate of packet 6 would NOT be dropped (6 > 4)

NEW behavior (contiguousPoint):
  - Packets 0-9 are contiguous in btree
  - contiguousScan advances contiguousPoint to 9
  - contiguousPoint = 9
  - Pushing duplicate of packet 6 IS dropped (6 <= 9)
```

**Why this is correct**: `contiguousPoint` represents "I have received all packets up to here", not "I have delivered all packets up to here". If packets 0-9 are contiguous in the btree, a duplicate of packet 6 is definitely already received, so it should be dropped. The TSBPD check only controls when packets are delivered to the application, not whether we've received them.

### Decision 7: NAK Btree Cleanup on TSBPD Advancement ✅

**Note**: During implementation, a critical bug was discovered and fixed.

### Decision 8: `tooRecentThreshold` Formula Consistency ✅

**BUG DISCOVERED**: The `tooRecentThreshold` formula was inverted in `periodicNakBtree()`.

**Incorrect formula** (in `periodicNakBtree`):
```go
tooRecentThreshold = now + uint64(float64(r.tsbpdDelay)*r.nakRecentPercent)
// = now + tsbpdDelay * 0.10 (with nakRecentPercent = 0.10)
```

**Correct formula** (in `gapScan`, now applied everywhere):
```go
tooRecentThreshold = now + uint64(float64(r.tsbpdDelay)*(1.0-r.nakRecentPercent))
// = now + tsbpdDelay * 0.90 (with nakRecentPercent = 0.10)
```

**Impact of bug**: With the inverted formula, the NAK scan window was only 10% of TSBPD (300ms at 3s TSBPD) instead of the intended 90% (2.7s). This meant packets were considered "too recent" for 90% of the TSBPD delay, severely limiting NAK effectiveness.

**Fix applied**:
1. Created `CalcTooRecentThreshold()` function with documented formula derivation
2. Created `receiver.tooRecentThreshold(now)` method for consistent usage
3. Updated both `gapScan()` and `periodicNakBtree()` to use the method
4. Added comprehensive unit tests in `too_recent_threshold_test.go`

**Formula derivation**:
- A packet with `PktTsbpdTime = T` arrived at `(T - tsbpdDelay)`
- We wait `nakRecentPercent` (10%) of TSBPD after arrival before NAKing
- So we NAK when: `now >= (T - tsbpdDelay) + tsbpdDelay * nakRecentPercent`
- Rearranging: `T <= now + tsbpdDelay * (1.0 - nakRecentPercent)`
- Packets with `T > threshold` are "too recent"

**Question**: When `contiguousPoint` advances due to TSBPD expiry, do we need to remove NAK entries for the skipped packets?

**Answer**: The NAK btree cleanup already happens correctly via two mechanisms:

1. **`gapScan()` TSBPD-aware logic** (receive.go lines 1017-1028):
   - When `now > minTsbpdTime`, `gapScan()` advances local `lastContiguous` to `btree.Min()-1`
   - This prevents adding new NAKs for TSBPD-expired packets
   - Comment: "TSBPD expired - packets in the gap are unrecoverable, don't NAK them"

2. **`expireNakEntries()` cleanup** (receive.go lines 1946-1949):
   - Uses `cutoff = packetStore.Min()` as the expiry boundary
   - Calls `nakBtree.DeleteBefore(cutoff)` to remove old entries
   - When gap packets are skipped, `packetStore.Min()` = first packet after gap
   - All NAK entries for sequences < `packetStore.Min()` are removed

**Sequence of operations in Tick()**:
```
1. periodicNAK() runs → gapScan() avoids NAKing TSBPD-expired packets
2. periodicACK() runs → contiguousScan() advances contiguousPoint past expired gap
3. expireNakEntries() runs → removes NAK entries for sequences < packetStore.Min()
```

**No additional cleanup needed** - the existing design handles this correctly.

### Decision 9: `Tick()` Timing and `periodicACKInterval` ✅

**IMPORTANT**: The `Tick()` function has timing requirements that affect testing:

1. **Full ACK interval check**: In `periodicACKLocked()` (line 1108):
   ```go
   if now-r.lastPeriodicACK < r.periodicACKInterval {
       // Not time for Full ACK - check Light ACK instead
   }
   ```

2. **Test implication**: If you call `Tick(now)` twice with the same `now` value:
   - First call: Full ACK runs, updates `lastPeriodicACK = now`
   - Second call: `now - lastPeriodicACK = 0 < periodicACKInterval` → blocked!
   - Result: `lastACKSequenceNumber` doesn't update

3. **Fix for tests**: Use increasing `now` values in successive `Tick()` calls:
   ```go
   recv.Tick(10)  // now=10, first Full ACK
   // ... push more packets ...
   recv.Tick(20)  // now=20, second Full ACK (20-10 >= periodicACKInterval)
   ```

4. **Why this wasn't obvious**: The old tests using `lastDeliveredSequenceNumber` didn't need multiple `Tick()` calls because delivery happened independently via TSBPD expiry. With `contiguousPoint`, advancement only happens during `Tick()` → `contiguousScan()`.

**Implementation locations to update** (from `iouring_waitcqetimeout_implementation.md`):
1. `drainPacketRing()` line 1957
2. `drainRingByDelta()` line 2084
3. `processOnePacket()` line 2484

```go
// BEFORE:
if seq.Lte(r.lastDeliveredSequenceNumber) {

// AFTER:
if seq.Val() <= r.contiguousPoint.Load() {
```

---

## References

- `ack_optimization_plan.md` - Original scan design
- `integration_testing_with_network_impairment_defects.md` - Network loss scenarios
- `gosrt_lockless_design.md` - Overall lockless architecture
- SRT RFC Section on TSBPD: https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-4.5

