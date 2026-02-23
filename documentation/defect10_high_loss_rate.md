# Defect 10: High Loss Rate Despite Large SRT Buffers

**Status**: ✅ RESOLVED - Analysis Flaw Fixed
**Priority**: Medium (was High)
**Discovered**: 2024-12-10
**Root Cause Identified**: 2024-12-10
**Resolution**: 2024-12-10 (Fixed recovery rate calculation in `analysis.go`)
**Related Documents**:
- `integration_testing_with_network_impairment_defects.md` - Parent tracking document
- `integration_testing_design.md` - Test framework design
- `metrics_analysis_design.md` - Metrics analysis methodology

---

## Summary

**TL;DR**: The "low recovery rate" was a **metrics interpretation error**, not an SRT bug.

- **`Skips: 0`** confirms ALL packets eventually arrived (true 100% recovery)
- The "drops" (`already_acked` + `duplicate`) are **duplicate retransmits**, not unrecovered packets
- The recovery calculation was counting duplicate arrivals as failures

---

## Problem Statement (Original)

With only **2% netem packet loss** configured and **3-second SRT latency buffers**, the test shows unexpectedly poor recovery:

| Metric | Expected | Actual | Issue |
|--------|----------|--------|-------|
| Gap detection rate | ~2% | 10.72% | **5.4x higher** |
| Recovery rate | 95%+ | 68.9% | **26% lower** |
| NAK delivery rate | 98%+ | ~60% | **~38% lower** |
| Packets unrecovered | <10 | 127 | **12x higher** |

**Original Question**: Why is SRT unable to recover from 2% loss when it has 3-second buffers?

**Answer**: SRT IS recovering! The metrics were being misinterpreted.

---

## Test Configuration

```
Test: Network-Loss2pct-1Mbps-HighPerf
Bitrate: 1 Mbps (~61 packets/second)
SRT Latency: 3000ms (both rcvlatency and peerlatency)
netem Loss: 2% bidirectional (applied to both veth pairs)
io_uring: Enabled (send and receive)
Packet Reordering: btree algorithm
TLPKTDROP: Enabled
```

At 61 pkt/s with 3-second buffers:
- Buffer capacity: ~183 packets
- Expected loss: ~1.2 packets/second (2% of 61)
- Expected gaps needing NAK: ~36 per 30-second test
- Actual gaps detected: 408

---

## Observed Behavior

### Final Statistics from Test Run

**Connection 1 (Client-Generator → Server):**
```
Sender: sent=3806, retrans=142 (3.73%)
Receiver: recv=3729, gaps=196 (5.15%), drops=65, recovery=66.8%
NAKs: sent=124, recv=123
```

**Connection 2 (Server → Subscriber):**
```
Sender: sent=4189, retrans=159 (3.80%)
Receiver: recv=4092, gaps=212 (5.06%), drops=62, recovery=70.8%
NAKs: sent=137, recv=135
```

**Combined:**
```
netem configured: 2.0% bidirectional loss
Original packets: 3806
Total gaps: 408 (10.72% combined rate)
Total retransmissions: 301 (7.91% of original)
Unrecovered (SRT drops): 127
Combined recovery rate: 68.9%
```

### Mid-Test Verbose Output Pattern

Consistent pattern observed every 2-second interval:
```
NAK request gap: requested 16 pkts, sent 9 retrans (unfulfilled: 7)
```

This shows NAKs requesting more packets than are actually retransmitted.

---

## Hypotheses

### Hypothesis A: Cascading Loss Effect (Bidirectional Loss Amplification)

**Theory**: With 2% loss applied bidirectionally, control packets (NAKs, ACKs) are also subject to loss, creating a cascade:

1. Data packet lost (2% chance)
2. NAK sent requesting retransmission (2% chance NAK is also lost)
3. If NAK delivered, retransmission sent (2% chance retrans is also lost)
4. If retrans lost, another NAK needed (cycle repeats)

**Expected vs Actual:**

For simple 2% bidirectional loss:
- P(data delivered) = 0.98
- P(NAK delivered) = 0.98
- P(retrans delivered) = 0.98
- P(full recovery) = 0.98 × 0.98 = 96%
- P(unrecoverable) ≈ 0.02 × 0.04 ≈ 0.08%

But we're seeing **31%** unrecovered (127/408), not 0.08%.

**Likelihood**: Medium - Explains some amplification but not the 5.4x gap rate.

### Hypothesis B: Double-Hop Loss Multiplication

**Theory**: Each packet traverses TWO network hops (CG→Server→Client), each with 2% loss:

- Hop 1 (CG→Server): 2% loss
- Hop 2 (Server→Client): Additional 2% loss
- Effective loss: ~4% (not 2%)

But each hop also has its own NAK/retrans cycle, and losses can compound.

**Evidence**: The two connections show similar gap rates (5.15% and 5.06%), suggesting each hop contributes independently.

**Likelihood**: Medium - Explains some, but 5.4x still unexplained.

### Hypothesis C: NAK Delivery Rate Issue

**Theory**: The "60% NAK delivery rate" metric suggests something is wrong with NAK processing.

From test output:
```
NAK detail: 34 pkts (singles) + 202 pkts (ranges) = 236 total
Delivery: 60.2%
```

If only 60% of NAK requests are being "delivered" (processed/fulfilled), then 40% of loss events are never addressed.

**Questions to investigate:**
1. What exactly does "NAK delivery rate" measure in `analysis.go`?
2. Are NAKs being sent but lost by the network?
3. Are NAKs being received but not processed correctly?
4. Is this a metric calculation issue rather than a real problem?

**Likelihood**: High - This is the smoking gun. Need to understand what this metric really measures.

### Hypothesis D: TLPKTDROP Timing Issue

**Theory**: Packets are arriving "too late" and being dropped by TLPKTDROP before retransmission can complete.

**Evidence against this:**
- Drops show `too_late: 0` consistently
- 3-second buffer should provide ample time
- RTT is only ~11ms

**Likelihood**: Low - The "too_late" counter would show this.

### Hypothesis E: Range NAK Over-Requesting

**Theory**: When gaps are detected, range NAKs request packets that have already arrived (just out of order), leading to:
1. Inflated gap counts
2. Duplicate retransmissions
3. Wasted bandwidth

**Evidence**:
- "Over-NAK Confirmation: 5 dupes vs 5 over-requested pkts (100% match)"
- This shows duplicates ARE occurring and matching over-requested count

But this shouldn't cause unrecovered losses - just wasted bandwidth.

**Likelihood**: Low for explaining unrecovered losses, but explains inflated metrics.

### Hypothesis F: netem Configuration Issue

**Theory**: The `tc netem loss 2%` command with our configuration might behave differently than expected.

**Possible issues:**
1. Loss applied to BOTH ingress and egress (effectively 4%)?
2. `limit 50000` causing additional drops?
3. Loss pattern (uniform vs burst) affecting recovery?

**Likelihood**: Medium - Need to verify actual netem behavior.

### Hypothesis G: Missing LossMaxTTL Implementation

**Theory**: Without `LossMaxTTL`, every out-of-order packet triggers an immediate NAK. This could:
1. Flood the network with NAKs
2. Cause NAK congestion
3. Lead to some NAKs being dropped or ignored

**Evidence**: High range NAK ratio (84-86% are ranges) suggests aggressive NAKing.

**Likelihood**: Medium - Could explain NAK delivery issues.

---

## Action Plan

### Phase 1: Understand the "NAK Delivery Rate" Metric

**Status**: 🔍 In Progress

#### Two Types of NAK Counters

We have **two separate counter sets** that are being compared:

| Counter Type | Purpose | Receiver (sends NAKs) | Sender (receives NAKs) |
|--------------|---------|----------------------|------------------------|
| **NAK Packets** | SRT control packets sent/recv | `PktSentNAKSuccess` | `PktRecvNAKSuccess` |
| **NAK Entries** | Packets requested within NAKs | `CongestionRecvNAKPktsTotal` | `CongestionSendNAKPktsRecv` |

#### NAK Delivery Rate Calculation

From `analysis.go:computeRates()`:

```go
if c.NAKPktsRequested > 0 {
    c.NAKDeliveryRate = float64(c.NAKPktsReceived) / float64(c.NAKPktsRequested)
}
```

Maps to Prometheus metrics:
- `NAKPktsRequested` ← `gosrt_connection_nak_packets_requested_total{direction="sent"}` ← **`CongestionRecvNAKPktsTotal`**
- `NAKPktsReceived` ← `gosrt_connection_nak_packets_requested_total{direction="recv"}` ← **`CongestionSendNAKPktsRecv`**

#### Observed Discrepancy in Verbose Output

From the test run:
```
Sender (CG):
  Control: +3 NAKs recv          ← NAK PACKETS (PktRecvNAKSuccess)
  NAK detail: +3 singles, +0 ranges, requesting 3 pkts  ← NAK ENTRIES

Receiver (Server):
  Control: +3 NAKs sent          ← NAK PACKETS (PktSentNAKSuccess) ✓ MATCHES!
  NAK detail: +1 singles, +4 ranges, requesting 5 pkts  ← NAK ENTRIES ✗ DIFFERS!
```

**Key Finding**:
- NAK **PACKET** count matches: 3 sent = 3 received ✓
- NAK **ENTRY** count differs: 5 pkts requested ≠ 3 pkts received ✗

This suggests the NAK packets ARE being delivered, but **the entry counting is inconsistent**.

#### Hypothesis: Different Counting Locations

Possible causes for the discrepancy:

1. **Send-side counts entries BEFORE serialization**
2. **Receive-side counts entries AFTER parsing**
3. **Range NAK encoding counted differently** (entries vs packets)

#### Code Review Action Items

| File | Function | Counter | Review Focus |
|------|----------|---------|--------------|
| `congestion/live/receive.go` | NAK generation | `CongestionRecvNAKSingle/Range/PktsTotal` | Where incremented? |
| `congestion/live/send.go` | NAK handling | `CongestionSendNAKSingleRecv/RangeRecv/PktsRecv` | Where incremented? |
| `packet/nak.go` | NAK serialization | N/A | How entries encoded? |

---

## Phase 1 Review Results

**Status**: 🔍 Completed - BUG FOUND

### Bug: Immediate NAK Always Counted as Range

**Location**: `congestion/live/receive.go:311-312`

```go
// Current code (BUGGY):
m.CongestionRecvNAKRange.Add(gapSize)    // ← ALWAYS adds to Range!
m.CongestionRecvNAKPktsTotal.Add(gapSize)
```

**The Problem**:

The comment on line 309 says "Immediate NAK is always a range (gapSize > 1)" - but this is **WRONG**.

Consider: `maxSeenSequence = 5`, `currentPacket = 7` (packet 6 missing):
- NAK sends: `start = 6`, `end = 6`
- This is a **SINGLE** NAK entry (start == end)
- But code adds to `NAKRange`, not `NAKSingle`!

**Comparison to Periodic NAK (correct)**:

```go
// Periodic NAK path (receive.go:503-511) - CORRECT:
if start.Equals(end) {
    m.CongestionRecvNAKSingle.Add(1)      // ← Single entry
    m.CongestionRecvNAKPktsTotal.Add(1)
} else {
    rangeSize := uint64(end.Distance(start)) + 1
    m.CongestionRecvNAKRange.Add(rangeSize) // ← Range entry
    m.CongestionRecvNAKPktsTotal.Add(rangeSize)
}
```

**Comparison to Sender NAK Receive (correct)**:

```go
// Sender NAK receive path (send.go:413-418) - CORRECT:
if start.Equals(end) {
    m.CongestionSendNAKSingleRecv.Add(1)
} else {
    m.CongestionSendNAKRangeRecv.Add(lossCount)
}
```

### Impact Analysis

This bug explains the discrepancy in verbose output:

| Metric | Server (sends NAKs) | CG (receives NAKs) | Issue |
|--------|---------------------|-------------------|-------|
| Singles | 1 (undercounted) | 3 | Server miscounts singles as ranges |
| Ranges | 4 (overcounted) | 0 | Includes packets that should be singles |
| Total | 5 | 3 | Metrics mismatch |

The **NAK Delivery Rate** calculation is therefore flawed:
- `NAKDeliveryRate = NAKPktsReceived / NAKPktsRequested`
- But `NAKPktsRequested` is **overcounted** by the buggy immediate NAK path!

### Proposed Fix: Shared Helper Function

Instead of fixing the bug in isolation, create a **shared helper function** that both send and receive paths use. This guarantees 100% consistency.

#### Design: `metrics/nak_counter.go`

```go
// NAKCounterType specifies which counter set to increment
type NAKCounterType int

const (
    NAKCounterSend NAKCounterType = iota  // Receiver generates NAKs (CongestionRecvNAK*)
    NAKCounterRecv                         // Sender receives NAKs (CongestionSendNAK*Recv)
)

// CountNAKEntries iterates through a NAK loss list and increments the appropriate
// single/range/total counters. This function is used by BOTH:
//   - Receiver when generating NAKs (CongestionRecvNAK*)
//   - Sender when receiving NAKs (CongestionSendNAK*Recv)
//
// Using the same function ensures counters are 100% consistent between endpoints.
func CountNAKEntries(m *ConnectionMetrics, list []circular.Number, counterType NAKCounterType) {
    for i := 0; i < len(list); i += 2 {
        start, end := list[i], list[i+1]
        if start.Equals(end) {
            // Single packet NAK entry
            if counterType == NAKCounterSend {
                m.CongestionRecvNAKSingle.Add(1)
            } else {
                m.CongestionSendNAKSingleRecv.Add(1)
            }
            totalPkts += 1
        } else {
            // Range NAK entry
            rangeSize := uint64(end.Distance(start)) + 1
            if counterType == NAKCounterSend {
                m.CongestionRecvNAKRange.Add(rangeSize)
            } else {
                m.CongestionSendNAKRangeRecv.Add(rangeSize)
            }
            totalPkts += rangeSize
        }
    }
    // Update total counter...
}
```

#### Test Strategy

1. **Unit test for `CountNAKEntries()`**: Verify single/range/mixed lists
2. **Invariant test**: Verify `Single + Range == Total`
3. **Alignment test**: Generate NAK on receiver, receive on sender, verify counters match

### Additional Question: Is gapSize Correct?

Need to verify the `gapSize` calculation:

```go
gapSize := uint64(pkt.Header().PacketSequenceNumber.Distance(r.maxSeenSequenceNumber))
```

For `maxSeen = 5`, `current = 7`:
- `Distance(5, 7) = ?` (need to check implementation)
- Expected missing packets: 1 (just packet 6)

---

### Implementation Plan (Phase 1.1)

| Step | Description | Status |
|------|-------------|--------|
| 1.1a | Check `circular.Number.Distance()` semantics | ✅ Complete |
| 1.1b | Create `metrics/nak_counter.go` with `CountNAKEntries()` | ✅ Complete |
| 1.1c | Add unit tests for `CountNAKEntries()` | ✅ Complete |
| 1.1d | Update `receive.go` immediate NAK to use helper | ✅ Complete |
| 1.1e | Update `receive.go` periodic NAK to use helper | ✅ Complete |
| 1.1f | Update `send.go` NAK receive to use helper | ✅ Complete |
| 1.1g | Add alignment integration test | ✅ Complete (`TestCountNAKEntries_ConsistencyBetweenSendAndRecv`) |
| 1.1h | Run existing tests to verify no regression | ✅ Complete (all tests pass) |
| 1.3 | Re-run network impairment tests | 🔴 Not Started |

### Files Changed

| File | Change |
|------|--------|
| `metrics/metrics.go` | Added `NewConnectionMetrics()` constructor |
| `metrics/nak_counter.go` | **NEW** - Shared NAK counting helper |
| `metrics/nak_counter_test.go` | **NEW** - Unit tests for helper |
| `congestion/live/receive.go` | Use shared helper for immediate & periodic NAK |
| `congestion/live/send.go` | Use shared helper for received NAKs |
| `congestion/live/metrics_test.go` | Updated test to expect correct counts |

### Bug Fix Summary

**Old behavior** (buggy):
- Immediate NAK always counted as Range (even for single packets)
- Loss counter used `Distance(pkt, maxSeen)` which was 1 too high
- NAK single/range/total counters didn't match between sender and receiver

**New behavior** (fixed):
- Shared `CountNAKEntries()` function used by both send and receive paths
- Correctly identifies single vs range NAK entries
- Loss counter now matches actual missing packet count
- 100% consistency guaranteed between endpoints

---

### Phase 2: Create Simplified "Single Connection" Test

#### Goal
Eliminate the relay (Connection 2) to isolate Connection 1 behavior.

#### New Test Configuration: `Network-Loss2pct-1Mbps-SingleHop`

```go
{
    Name:        "Network-Loss2pct-1Mbps-SingleHop",
    Description: "2% loss - single connection only (no subscriber)",
    Mode:        TestModeNetwork,
    Impairment: NetworkImpairment{
        LossRate:       0.02,
        LatencyProfile: "none",
    },
    Bitrate:         1_000_000,
    TestDuration:    30 * time.Second,
    ConnectionWait:  3 * time.Second,
    MetricsEnabled:  true,
    CollectInterval: 2 * time.Second,
    VerboseMetrics:  true,
    SingleConnection: true,  // NEW FLAG: Don't start subscriber
    SharedSRT: &SRTConfig{
        RecvLatency:            3000 * time.Millisecond,
        PeerLatency:            3000 * time.Millisecond,
        TLPktDrop:              true,
        PacketReorderAlgorithm: "btree",
        IoUringEnabled:         true,
        IoUringRecvEnabled:     true,
    },
}
```

**What this tests**:
- Only CG → Server connection
- No relay to subscriber
- Simpler analysis (one connection instead of two)
- Confirms if issue is in single connection or only in relay scenario

---

### Phase 3: Network Topology for Simplified Test

#### Current Full Topology (3 Components)

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                   Host System                                            │
│                                                                                          │
│  ┌──────────────┐                                               ┌──────────────┐        │
│  │ ns_publisher │                                               │ns_subscriber │        │
│  │  (CG)        │                                               │  (Client)    │        │
│  │ 10.1.1.2     │                                               │ 10.1.2.2     │        │
│  └──────┬───────┘                                               └──────┬───────┘        │
│         │                                                              │                 │
│         │ veth                                                   veth  │                 │
│         ▼                                                              ▼                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                           ns_router_a (Client Router)                         │       │
│  │                                                                               │       │
│  │  eth_pub (10.1.1.1)                                    eth_sub (10.1.2.1)    │       │
│  │                                                                               │       │
│  │  link0_a ◄───────── netem loss 2% ─────────► link0_b (to Router B)          │       │
│  │                     + delay (profile)                                         │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth pair (link0)                              │
│                                        │                                                 │
│                                        ▼                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                          ns_router_b (Server Router)                          │       │
│  │                                                                               │       │
│  │  link0_b ◄───────── netem loss 2% ─────────► link0_a (to Router A)          │       │
│  │                     + delay (profile)                                         │       │
│  │                                                                               │       │
│  │  eth_srv (10.2.1.1)                                                          │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth                                            │
│                                        ▼                                                 │
│                               ┌──────────────┐                                          │
│                               │  ns_server   │                                          │
│                               │  (Server)    │                                          │
│                               │  10.2.1.2    │                                          │
│                               └──────────────┘                                          │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

#### Where netem Loss is Applied (Current Setup)

**FROM `lib.sh:set_netem_loss()`:**

```bash
# Router A side (Publisher/Subscriber → Server direction)
ip netns exec ns_router_a tc qdisc change dev link0_a \
    root netem delay ${delay}ms loss ${loss}% limit 50000

# Router B side (Server → Publisher/Subscriber direction)
ip netns exec ns_router_b tc qdisc change dev link0_b \
    root netem delay ${delay}ms loss ${loss}% limit 50000
```

**Visual: Packet Path with Loss Points**

```
                          Connection 1 (CG ↔ Server)

CG (Publisher)                                              Server
    │                                                          │
    │ DATA packet ──────────────────────────────────────────►  │
    │              ▲                        ▲                  │
    │              │                        │                  │
    │          link0_a                  link0_b               │
    │          2% loss                  (no loss on           │
    │          (Router A)               this direction)       │
    │                                                          │
    │ ◄──────────────────────────────────────────── NAK packet │
    │              ▲                        ▲                  │
    │              │                        │                  │
    │          link0_a                  link0_b               │
    │          (no loss on              2% loss               │
    │          this direction)          (Router B)            │
    │                                                          │
    │ RETRANS ────────────────────────────────────────────►   │
    │              ▲                                          │
    │              │                                          │
    │          link0_a                                        │
    │          2% loss                                        │
    │          (same as DATA)                                 │
```

**Key Observation**:
- DATA packets from CG get 2% loss at Router A's `link0_a`
- NAK packets from Server get 2% loss at Router B's `link0_b`
- RETRANS packets from CG get ANOTHER 2% loss at Router A's `link0_a`

**This is correct bidirectional loss!** But it means:
- 2% of original data lost → NAK sent
- 2% of NAKs lost → sender never knows to retransmit
- 2% of retransmissions lost → need another NAK cycle

---

### Phase 4: Review netem Configuration in Detail

#### Current netem Commands

```bash
# Applied in lib.sh:set_netem_loss()

# On Router A (client-side) - affects: CG→Server DATA, Server→CG NAKs
tc qdisc change dev link0_a root netem delay 0ms loss 2% limit 50000

# On Router B (server-side) - affects: Server→CG RETRANS, CG→Server NAKs
tc qdisc change dev link0_b root netem delay 0ms loss 2% limit 50000
```

**Questions to Verify**:

1. **Is loss applied to BOTH ingress and egress?**
   - netem `tc qdisc` on a device applies to **egress only**
   - Ingress filtering requires `tc filter` with `ingress` qdisc
   - **Expectation**: Loss is only applied to packets LEAVING the interface

2. **Interface direction verification**:
   - `link0_a` is Router A's interface TO Router B
   - Packets from CG→Server traverse: CG → eth_pub → routing → link0_a (EGRESS, 2% loss) → link0_b
   - Packets from Server→CG traverse: Server → eth_srv → routing → link0_b (EGRESS, 2% loss) → link0_a

   **This looks correct!** Each direction gets 2% loss on egress.

3. **Verify with `tc -s qdisc show`**:
   ```bash
   ip netns exec ns_router_a_<id> tc -s qdisc show dev link0_a
   ip netns exec ns_router_b_<id> tc -s qdisc show dev link0_b
   ```

   This will show actual packet counts and drops - compare to SRT's gap count.

---

### Phase 5: Mathematical Analysis

#### Expected vs Observed with Correct Bidirectional Loss

**Single Packet Recovery Probability:**

For a lost packet to be recovered:
1. NAK must be delivered: P(NAK delivered) = 0.98
2. Retransmission must be delivered: P(retrans delivered) = 0.98
3. Combined first-attempt recovery: 0.98 × 0.98 = **96.04%**

For remaining 3.96% needing second attempt:
4. Second NAK delivered: 0.98
5. Second retrans delivered: 0.98
6. Second-attempt recovery: 0.0396 × 0.9604 = **3.80%**

**Total expected recovery: 96.04% + 3.80% = 99.84%**

But we're seeing **68.9%** recovery. This is a **31% gap** from expected.

#### Possible Explanations for the Gap

| Factor | Impact | Likelihood |
|--------|--------|------------|
| NAK loss (2%) prevents first retry | ~2% unrecovered | Confirmed |
| Retrans loss (2%) requires second NAK | ~4% need 2nd attempt | Confirmed |
| TLPKTDROP before 2nd attempt | Unknown | Need to check |
| NAK processing delay | Could miss window | Medium |
| Metric counting issues | Could inflate gaps | Medium |

---

### Phase 6: Specific Experiments

#### Experiment 1: Single Connection Test

Create and run `Network-Loss2pct-1Mbps-SingleHop`:
- Verify gap rate with only one connection
- Compare to expected ~4-5% cascade rate (not 10.7%)

#### Experiment 2: Unidirectional Loss

Apply loss only on DATA direction (Router A → Router B), not on control:
```bash
# Only apply loss on Router A's link0_a (CG→Server direction)
ip netns exec ns_router_a tc qdisc change dev link0_a root netem loss 2% limit 50000

# NO loss on Router B's link0_b (Server→CG direction)
ip netns exec ns_router_b tc qdisc change dev link0_b root netem limit 50000
```

Expected: Recovery rate should improve dramatically (NAKs always delivered).

#### Experiment 3: Verify netem Drop Counts

During test, capture:
```bash
# Before test
ip netns exec ns_router_a tc -s qdisc show dev link0_a > /tmp/netem_before.txt
ip netns exec ns_router_b tc -s qdisc show dev link0_b >> /tmp/netem_before.txt

# After test
ip netns exec ns_router_a tc -s qdisc show dev link0_a > /tmp/netem_after.txt
ip netns exec ns_router_b tc -s qdisc show dev link0_b >> /tmp/netem_after.txt

# Compare dropped packet counts
```

#### Experiment 4: Lower Loss Rate

Test with 0.5% and 1% loss to see if amplification factor is consistent:
- If 0.5% → 2.7% gaps (5.4x), same amplification
- If 0.5% → 1% gaps (2x), amplification scales differently

---

## Questions Needing Answers (Updated)

1. **Why is gap rate 5.4x higher than netem loss rate?**
   - Hypothesis: Bidirectional loss causes cascade (confirmed in analysis above)
   - Need to verify with unidirectional loss experiment

2. **What does "60% NAK delivery rate" actually mean?** ✓ ANSWERED
   - `NAKPktsReceived / NAKPktsRequested`
   - Compares receiver's sent NAKs vs sender's received NAKs
   - 60% means 40% of NAK requests never reached the sender

3. **Are NAKs being correctly counted on both sides?**
   - After Defect 8 fix, counters should be balanced
   - Need to verify timing of metric collection doesn't create mismatch

4. **What are the actual netem drop counts?**
   - `tc -s qdisc show` will reveal actual packet drops
   - Compare to SRT's gap count

5. **Does the two-connection relay amplify issues?**
   - Single-connection test will isolate this

---

## Data to Collect

1. **netem statistics**: `tc -s qdisc show` before/after test on both routers
2. **Single-connection gap rate**: Compare to two-connection rate
3. **Unidirectional loss recovery**: Compare to bidirectional
4. **NAK packet counts**: Verify sender received = receiver sent
5. **Time between gap detection and NAK send**: Check for delays

---

## Related Observations

### NAK Balance Is Now Correct (Defect 8 Fixed)

The verbose output now shows:
```
✓ NAK pkts balanced: 10 sent = 10 recv
✓ Retrans balanced: 10 sent = 10 recv
```

So the NAK counting bugs from Defect 8 are fixed. The issue is higher-level.

### Graceful Shutdown Works (Defect 9 Fixed)

Components now shut down cleanly with timing:
```
Graceful shutdown complete after 0ms
```

### Pattern in Mid-Test Deltas

Every 2-second collection shows similar pattern:
- Gaps detected slightly exceed retransmissions
- Small number of unfulfilled NAK requests (3-11 per interval)
- These unfulfilled requests accumulate over time

---

## Implementation Status

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Understand NAK Delivery Rate metric | ✅ BUG FOUND |
| 1.1 | Create shared `CountNAKEntries()` helper | ✅ IMPLEMENTED |
| 1.2 | Verify Distance() function semantics | ✅ VERIFIED |
| 1.3 | Re-run tests to verify NAK fix | ✅ VERIFIED (99% delivery) |
| 2 | **Metrics Gap Analysis** | ✅ COMPLETE |
| 2.1 | Add 4 new counters to `metrics.go` | ✅ IMPLEMENTED |
| 2.2 | Implement TSBPD skip counting | ✅ IMPLEMENTED |
| 2.3 | Implement tick counters for ACK/NAK routines | ✅ IMPLEMENTED |
| 2.4 | Export to Prometheus | ✅ IMPLEMENTED |
| 2.5 | Update `analysis.go` (recovery + health check) | ✅ IMPLEMENTED |
| 2.6 | Add unit tests | ✅ IMPLEMENTED (3 tests) |
| 2.7 | Run metrics-audit to verify single increments | ✅ VERIFIED |
| 2.8 | Re-run network impairment tests | 🔴 Not Started |

**Phase 1 COMPLETE**: NAK accounting bugs fixed:
- Created shared `CountNAKEntries()` helper (100% consistency)
- NAK Delivery Rate: 99.1% (was 60% before fix) ✅
- NAK Fulfillment Rate: 100% ✅

**Phase 2 COMPLETE**: Metrics gap analysis:
- Reviewed all metrics design documents
- Identified missing counter: `CongestionRecvPktSkippedTSBPD`
- Documented complete packet flow audit
- Created 7-step implementation plan

**Phase 2 COMPLETE**: TSBPD skip counter implemented and tested:
- Counter correctly tracks packets that NEVER arrive (verified in unit tests)
- In real-world test: `skips=0` because all packets eventually arrive (3s buffer is enough)
- The 36 "drops" are packets that ARRIVED but were discarded (`already_acked`, `duplicate`)
- Recovery rate issue is a **timing** problem, not a loss problem

**NEW FINDING**: ACK is advancing past gaps BEFORE retransmits arrive:
- Retransmit arrives → but ACK already passed → `already_acked` drop
- This causes low "recovery" even though packets DO arrive

**Root Cause Identified (Phase 1.4)**: Two issues found!

**Issue 1: Missing "TSBPD Skip" Counter**
- When a packet never arrives (lost + all retransmissions lost), the receiver skips over it at TSBPD time
- This is NOT counted as a "drop" - the current drop counters only track packets that ARRIVED but were discarded
- Location: `receive.go:363-365` - ACK sequence advances past missing packets, no counter incremented
- The verbose output shows `DROPS: +5 (too_late: 0, buf_full: 0, dupes: 0)` - mismatch indicates uncounted drops

**Issue 2: Recovery Rate Calculation Flawed**
- `analysis.go:179` calculates: `recoveryRate = 1 - (TotalPacketsDropped / TotalGapsDetected)`
- But `TotalPacketsDropped` only counts packets that ARRIVED then were discarded
- It does NOT count packets that NEVER arrived (the real unrecovered gaps)
- This explains the 45% "recovery rate" - it's measuring the wrong thing!

**Actual Recovery Analysis (from test output)**:
- Total retransmissions: 245
- Total gaps detected: 175
- Retrans/gap ratio: 1.4

If periodic NAK were working correctly with 150 retry cycles:
- Expected retransmissions for 45% unrecovered: ~79 gaps × 150 retries = 11,850
- Actual: only 245 total retransmissions
- **Conclusion**: Periodic NAK is NOT re-requesting unrecovered packets multiple times!

**Hypothesis**: The periodic NAK stops re-requesting once ACK sequence advances past the gap (TSBPD skip)

---

## Phase 2: Metrics Gap Analysis

### 2.1 Document Review Summary

Reviewed the following documents to understand the gap:

| Document | Relevant Content | Gap Found? |
|----------|------------------|------------|
| `metrics_and_statistics_design.md` | Section 1269: "`PktDrop` = Packets dropped locally (too old, duplicate, already ACK'd, etc.)" | ❌ No mention of TSBPD skip |
| `packet_loss_drop_definitions.md` | "PktRecvDrop = packets not delivered to upstream application" | ❌ Only covers packets that ARRIVE |
| `metrics_implementation_progress.md` | Phase 3: Receive Path - lists all drop scenarios | ❌ Missing TSBPD skip |
| `metrics_and_statistics_audit.md` | Checklist for Loss vs Drop counters | ❌ Missing TSBPD skip |
| `tools/metrics-audit/main.go` | Checks defined vs used vs exported | N/A - can't detect SHOULD-BE metrics |

### 2.2 The Missing Counter

**Counter Name**: `CongestionRecvPktSkippedTSBPD` (or similar)

**Definition**: Packets that **never arrived** and were skipped when the ACK sequence advanced past them due to TSBPD timeout.

**Current Behavior** (line 363-365 in `receive.go`):
```go
// If there are packets that should have been delivered by now, move forward.
if h.PktTsbpdTime <= now {
    ackSequenceNumber = h.PacketSequenceNumber  // SKIP missing packets silently!
    return true // Continue
}
```

**What Happens**:
1. Packet 10 is received, TSBPD time = T+3000ms
2. Packet 11 is NEVER received (lost + all retransmissions lost)
3. Packet 12 is received, TSBPD time = T+3001ms
4. At time T+3000ms, receiver checks packet 12's TSBPD time
5. Since packet 12's time has passed, ACK advances from 10 to 12
6. Packet 11 is now "skipped" - **NO COUNTER INCREMENTED**

### 2.3 Design Gap in `packet_loss_drop_definitions.md`

The document defines `PktRecvDrop` as:
> "The total number of dropped by the SRT receiver and, as a result, **not delivered to the upstream application** DATA packets"

The key phrase "not delivered" should include:
1. ✅ Packets that arrived too old (tracked: `CongestionRecvDataDropTooOld`)
2. ✅ Packets already ACK'd (tracked: `CongestionRecvDataDropAlreadyAcked`)
3. ✅ Duplicate packets (tracked: `CongestionRecvDataDropDuplicate`)
4. ❌ **Packets that NEVER arrived** (NOT tracked!)

### 2.4 Complete Packet Flow Audit

| Stage | Scenario | Counter | Status |
|-------|----------|---------|--------|
| **Receive (Push)** | Packet received successfully | `CongestionRecvPkt` | ✅ Tracked |
| **Receive (Push)** | Gap detected, NAK sent | `CongestionRecvPktLoss` | ✅ Tracked |
| **Receive (Push)** | Packet too old (belated) | `CongestionRecvDataDropTooOld` | ✅ Tracked |
| **Receive (Push)** | Packet already ACK'd | `CongestionRecvDataDropAlreadyAcked` | ✅ Tracked |
| **Receive (Push)** | Duplicate packet | `CongestionRecvDataDropDuplicate` | ✅ Tracked |
| **Receive (Push)** | Store insert failed | `CongestionRecvDataDropStoreInsertFailed` | ✅ Tracked |
| **Receive (Tick)** | Packet delivered | `r.deliver(p)` called | ✅ (no counter, but delivered) |
| **Receive (periodicACK)** | ACK advances past gap | **NONE** | ❌ **MISSING** |

### 2.5 Proposed Counter Addition

**New Counters to Add**:

| Counter | Location | Description |
|---------|----------|-------------|
| `CongestionRecvPktSkippedTSBPD` | `receive.go:periodicACK()` | Packets skipped because TSBPD expired before they arrived |
| `CongestionRecvByteSkippedTSBPD` | `receive.go:periodicACK()` | Bytes skipped (estimated) |
| `CongestionRecvPeriodicACKRuns` | `receive.go:periodicACKWriteLocked()` | Times periodicACK actually ran (should be ~100/sec) |
| `CongestionRecvPeriodicNAKRuns` | `receive.go:periodicNAKLocked()` | Times periodicNAK actually ran (should be ~50/sec) |

**Rationale for Tick Counters**:
- Timer-based routines are critical for SRT's reliability
- `periodicACK` runs every 10ms (100 times/sec) → enables ACK-based flow control
- `periodicNAK` runs every 20ms (50 times/sec) → enables NAK-based retransmission
- If either stops running, SRT can't recover from losses
- Linear growth expected: `ticks ≈ test_duration_seconds × frequency`
- Any deviation from linear indicates a problem (timer stuck, lock contention, etc.)

**Timer Configuration** (from `connection.go`):
```go
c.tick = 10 * time.Millisecond              // Tick() called every 10ms
PeriodicACKInterval: 10_000,                // 10ms between ACKs (in µs)
PeriodicNAKInterval: 20_000,                // 20ms between NAKs (in µs)
```

**Implementation Location 1**: `receive.go:periodicACKWriteLocked()` (line ~397)

```go
func (r *receiver) periodicACKWriteLocked(...) (ok bool, sequenceNumber circular.Number, lite bool) {
    m := r.metrics

    // Track that periodicACK actually ran (not just returned early)
    if m != nil {
        m.CongestionRecvPeriodicACKRuns.Add(1)
    }

    // ... existing logic ...

    // If there are packets that should have been delivered by now, move forward.
    if h.PktTsbpdTime <= now {
        // Count skipped packets: gap from current ACK to this packet's sequence
        skippedCount := h.PacketSequenceNumber.Distance(ackSequenceNumber)
        if skippedCount > 1 {
            // Packets between ackSequenceNumber and h.PacketSequenceNumber-1 are skipped
            m.CongestionRecvPktSkippedTSBPD.Add(uint64(skippedCount - 1))
            m.CongestionRecvByteSkippedTSBPD.Add(uint64(skippedCount-1) * uint64(r.avgPayloadSize))
        }
        ackSequenceNumber = h.PacketSequenceNumber
        return true // Continue
    }
}
```

**Implementation Location 2**: `receive.go:periodicNAKLocked()` (line ~446)

```go
func (r *receiver) periodicNAKLocked(now uint64) []circular.Number {
    if now-r.lastPeriodicNAK < r.periodicNAKInterval {
        return nil  // Early return - don't count this
    }

    m := r.metrics
    if m != nil {
        m.CongestionRecvPeriodicNAKRuns.Add(1)  // Track actual NAK runs
    }

    // ... existing logic ...
}
```

### 2.6 Impact on Existing Metrics

**Updates Required**:

| File | Change |
|------|--------|
| `metrics/metrics.go` | Add 4 new counters (2 TSBPD skip + 2 tick counters) |
| `congestion/live/receive.go` | Increment counters in `periodicACKWriteLocked()` and `periodicNAKLocked()` |
| `metrics/handler.go` | Export all 4 new counters to Prometheus |
| `contrib/integration_testing/analysis.go` | Use TSBPD skip counter in recovery rate, use tick counters for health check |

**New Counters Summary**:
```go
// TSBPD Skip counters - track packets that NEVER arrived
CongestionRecvPktSkippedTSBPD    atomic.Uint64  // Packets skipped at TSBPD time
CongestionRecvByteSkippedTSBPD   atomic.Uint64  // Bytes skipped (estimated)

// Tick counters - track timer routine execution (for health monitoring)
CongestionRecvPeriodicACKRuns    atomic.Uint64  // Times periodicACK ran (~100/sec)
CongestionRecvPeriodicNAKRuns    atomic.Uint64  // Times periodicNAK ran (~50/sec)
```

**Recovery Rate Recalculation**:

Currently (flawed):
```go
recoveryRate = 1 - (TotalPacketsDropped / TotalGapsDetected)
```

With new counter:
```go
// Actual unrecovered = TSBPD skips (packets that never arrived)
unrecovered := TotalPacketsSkippedTSBPD
// Recovery rate = gaps that were filled / gaps detected
recoveryRate = 1 - (unrecovered / TotalGapsDetected)
```

### 2.7 Relationship to Other Counters

**Key Invariants**:

```
// Total gaps detected (NAK sent)
CongestionRecvPktLoss = initial gaps detected

// Packets that arrived late (after TSBPD)
CongestionRecvDataDropTooOld = packets that ARRIVED too late

// Packets that NEVER arrived (skipped at TSBPD)
CongestionRecvPktSkippedTSBPD = packets that NEVER arrived

// True undelivered count
TrueUndelivered = CongestionRecvDataDropTooOld + CongestionRecvPktSkippedTSBPD

// Recovery rate
RecoveryRate = 1 - (TrueUndelivered / CongestionRecvPktLoss)

// Timer health check (expected values for 30-second test)
CongestionRecvPeriodicACKRuns ≈ 30 × 100 = 3000  // 100/sec
CongestionRecvPeriodicNAKRuns ≈ 30 × 50  = 1500  // 50/sec

// If tick count is significantly lower than expected, indicates:
// - Lock contention (blocking timer execution)
// - Timer not running (bug)
// - System overload (can't keep up with 10ms ticks)
```

### 2.8 Why This Was Missed

1. **Focus on "packets seen"**: All existing drop counters track packets that **arrived** but were dropped
2. **No "negative space" tracking**: We track what we see, not what we don't see
3. **periodicACK not audited for drops**: The audit focused on `pushLocked()` and `Tick()`, not `periodicACK()`
4. **Semantic gap in documentation**: "PktDrop" was defined as "packets not delivered" but implemented as "packets received but discarded"

---

## Phase 2 Implementation Plan

| Step | Description | Status |
|------|-------------|--------|
| 2.1 | Add 4 counters to `metrics/metrics.go` (2 skip + 2 tick) | ✅ Complete |
| 2.2 | Implement TSBPD skip counting in `periodicACK()` | ✅ Complete |
| 2.3 | Implement tick counting in `periodicACKWriteLocked()` and `periodicNAKLocked()` | ✅ Complete |
| 2.4 | Export 4 counters in `metrics/handler.go` | ✅ Complete |
| 2.5 | Update `analysis.go`: recovery rate using skip counter, health check using tick counters | ✅ Complete |
| 2.6 | Add unit tests for new counters | ✅ Complete |
| 2.7 | Run metrics-audit to verify single increments | ✅ Complete |
| 2.8 | Re-run network impairment tests | ✅ Complete |

---

## Phase 2.8 Test Results Analysis

### Test Configuration
```
Network-Loss2pct-1Mbps-HighPerf
- 2% bidirectional packet loss
- 3000ms TSBPD latency buffer
- 1 Mbps bitrate
- btree + io_uring enabled
```

### Key Findings

**TSBPD Skip Counter Shows 0**:
```
unrecov=36 (drops=36, skips=0)
```

**All Drops Are From Packets That ARRIVED**:
```
⚠ DROPS: +3 (too_late: 0, buf_full: 0, dupes: 2)
```
Note: `too_late: 0` means no packets arrived after TSBPD expired!

### Why skips=0?

With a 3-second latency buffer and only 2% loss, the probability of a packet NEVER arriving is extremely low:

```
P(never_recovered) = P(loss)^N_retries
N_retries = (TSBPD_buffer / NAK_interval) = (3000ms / 20ms) = 150 retries
P(never_recovered) = 0.02^150 ≈ 0
```

**Conclusion**: Every missing packet eventually arrives (just sometimes too late).

### What Are The 36 Drops?

The drops are from `CongestionRecvDataDropAlreadyAcked` and `CongestionRecvDataDropDuplicate`:

| Drop Type | Meaning |
|-----------|---------|
| `already_acked` | Retransmit arrived AFTER ACK advanced past it |
| `duplicate` | Same packet received twice (usually from aggressive NAK retries) |
| `too_old` | Packet arrived after TSBPD expired (0 in our test) |

**Scenario**:
1. Packet 11 is lost, gap detected, NAK sent
2. Retransmit of 11 is also lost
3. Meanwhile, time passes, packet 12's TSBPD expires
4. ACK advances past 11 to deliver 12 (TSBPD skip would happen here IF 11 never arrives)
5. Retransmit of 11 finally arrives
6. But ACK already passed 11 → `already_acked` drop

### TSBPD Skip Counter IS Working

The unit test `TestTSBPDSkipCounter` passes - the counter correctly identifies gaps that are never filled:

```go
// Packets 0-4 and 7-9 in store (5-6 missing)
// Tick at time past TSBPD
// Result: CongestionRecvPktSkippedTSBPD = 2 ✓
```

### Why Low Recovery Rate (62.9%)?

With drops=36 and gaps=97:
```
Recovery = 1 - (36/97) = 62.9%
```

But these 36 "unrecovered" packets DID eventually arrive - they just arrived TOO LATE.

**Root Cause**: The ACK is advancing past gaps (due to TSBPD expiry on subsequent packets), causing retransmits to arrive after their sequence was ACK'd.

This is a **timing issue**, not a packet loss issue:
- The retransmit system works (NAK delivery ~99%, fulfillment ~100%)
- But sometimes the retransmit arrives a few ms after the ACK advances

### Updated Test Results (with granular drop visibility)

After adding `already_acked` to the output, we now have full visibility:

```
Connection1 (Publisher → Server):
  Drops: 37 (too_late=0, already_ack=17, dupes=20), Skips: 0
Connection2 (Server → Subscriber):
  Drops: 48 (too_late=0, already_ack=22, dupes=26), Skips: 0
```

| Drop Type | Count | % of Total | Meaning |
|-----------|-------|------------|---------|
| `already_ack` | 39 | 46% | Retransmits arrived AFTER ACK advanced |
| `duplicate` | 46 | 54% | Same packet received twice (over-NAKing) |
| `too_late` | 0 | 0% | No packets arrived after TSBPD expired |
| `Skips` | 0 | 0% | All packets eventually arrived |

**Key Finding**: The drops are NOT from TSBPD expiry. They're from:
1. **Timing (46%)**: Retransmits arriving after ACK has already advanced
2. **Over-NAKing (54%)**: Same packet requested multiple times

---

## Phase 3: ACK Advancement Investigation

### 3.1 Expected NAK/Retransmit Behavior

**Immediate NAK Path**:
1. Receiver detects gap (e.g., receives packet 10, then packet 12 → gap at 11)
2. Receiver sends immediate NAK for packet 11
3. Sender receives NAK, retransmits packet 11
4. Receiver gets retransmit, inserts into packet store (btree/list)
5. ACK advances past 11 when it's delivered

**Periodic NAK Path (every 20ms)**:
1. Periodic NAK runs, scans packet store for gaps
2. Sends NAK for any remaining gaps as ranges (e.g., [5,7] for packets 5,6,7)
3. Sender receives NAK range, retransmits all packets in range
4. Some may already have arrived (→ duplicates), some may fill gaps

**Expected Outcome with 3-second buffer**:
- Immediate NAK: ~10ms RTT for retransmit to arrive
- Periodic NAK: ~20ms between retries
- 3000ms buffer / 20ms = **150 retry opportunities**
- With 2% loss per retry: P(never recovered) = 0.02^150 ≈ 0

So ALL packets should eventually arrive. The question is: **why is ACK advancing before they do?**

### 3.2 The Problem: ACK Advancing Past Gaps

The `already_acked` drops (46%) indicate:
1. Packet was missing (gap detected)
2. NAK was sent
3. **ACK advanced past the gap BEFORE retransmit arrived**
4. Retransmit arrived → dropped because ACK already passed it

**Question**: What causes ACK to advance past a gap?

Looking at `receive.go:periodicACK()`:
```go
// If there are packets that should have been delivered by now, move forward.
if h.PktTsbpdTime <= now {
    ackSequenceNumber = h.PacketSequenceNumber
    return true // Continue - ACK advances past the gap!
}
```

ACK advances when **ANY packet in the store** has TSBPD time ≤ now. This includes packets AFTER the gap!

**Scenario**:
1. Receive packets 10, 12, 13 (gap at 11)
2. Packet 12's TSBPD time = T+3000ms
3. At time T+3001ms, periodicACK runs
4. Packet 12's TSBPD time has passed → ACK advances to 12, **skipping 11**
5. Retransmit of 11 arrives at T+3010ms → **dropped as `already_acked`**

### 3.3 Why Are Retransmits Taking So Long?

With 10ms RTT, retransmits should arrive within ~20ms of NAK. But TSBPD is 3000ms.

**Possible causes**:
1. **NAK is being lost** - But NAK delivery is 97%+, so most arrive
2. **Retransmit is being lost** - But we see `already_acked`, meaning it DID arrive
3. **TSBPD time is too aggressive** - Is packet 12's TSBPD time based on packet 11's send time?
4. **Reordering in netem** - Are packets arriving out of order, triggering early TSBPD?

### 3.4 Hypothesis: TSBPD Time Calculation Issue

Each packet has a `PktTsbpdTime` calculated from its send timestamp + latency.

If packet 11 is lost and packet 12 arrives, does packet 12's TSBPD time account for the gap?

**Expected**: Packet 12's TSBPD = (packet 12's send time) + latency
**Issue**: If packet 11 was supposed to be delivered first, shouldn't we wait for it?

The SRT spec says: "ACK advances when all packets up to that point are either received or their TSBPD time has passed."

**Question**: Is our implementation correctly waiting for missing packets?

### 3.5 Understanding the `already_acked` Drops

After tracing through the code, here's what's happening:

**Code Flow in `pushLocked()` (receive.go lines 255-291):**

```go
// Check 1: Too old (delivered to application already)
if pkt.Seq.Lte(lastDeliveredSequenceNumber) → drop as "too_old"

// Check 2: Already ACK'd (ACK has advanced past this sequence)
if pkt.Seq.Lt(lastACKSequenceNumber) → drop as "already_acked"  // ← Our issue!

// Check 3: In order (expected packet)
if pkt.Seq.Equals(maxSeenSequenceNumber.Inc()) → accept, update maxSeen

// Check 4: Out of order (potential gap filler)
if pkt.Seq.Lte(maxSeenSequenceNumber):
    if packetStore.Has(pkt.Seq) → drop as "duplicate"
    else → Insert into store (fills gap!)
```

**The Scenario Causing `already_acked`:**

1. **T=0ms**: Packets 10, 12 arrive (11 lost by netem)
2. **T=0ms**: Gap detected, **immediate NAK** sent for packet 11
3. **T=10ms**: First retransmit arrives, inserted into packetStore at position 11
4. **T=10ms**: periodicACK runs, sees no gap, **ACK advances** 10→11→12...
5. **T=20ms**: **Periodic NAK** runs, but gap is already filled (no NAK sent)
   - OR -
6. **T=5ms**: Periodic NAK ran BEFORE retransmit arrived, sent another NAK for 11
7. **T=25ms**: SECOND retransmit arrives (from periodic NAK)
8. **T=25ms**: Check: `11 < lastACKSequenceNumber(12)` → **drop as `already_acked`!**

**Key Insight**: The `already_acked` drops are **DUPLICATE retransmits** arriving after the gap was already filled by an earlier retransmit.

### 3.6 Why `duplicate` vs `already_acked`?

| Drop Type | When It Happens | Meaning |
|-----------|-----------------|---------|
| `duplicate` | Packet still in packetStore | Two identical packets arrived while buffered |
| `already_acked` | ACK has advanced past sequence | Retransmit arrived AFTER gap was filled and ACK advanced |

The distinction:
- Packets remain in `packetStore` until ACK advances and they're delivered
- Once delivered, `packetStore.Has(seq)` returns false
- Late arrivals then hit the `already_acked` check instead of `duplicate`

### 3.7 The Real Question: Why 48% Recovery?

The NAK/retransmit mechanism IS working:
- ✅ Gaps detected → NAKs sent (97%+ delivery)
- ✅ NAKs received → Retransmits sent (100% fulfillment)
- ✅ Retransmits arriving → Gaps being filled
- ✅ `Skips: 0` means ALL packets eventually arrived

**But** we're counting drops as "unrecovered"!

**The Real Issue**: Our `recovery` calculation is wrong!

```go
recoveryRate = 1 - (drops / gaps)
```

But `drops` includes:
- `already_acked` = duplicate retransmits (NOT unrecovered packets!)
- `duplicate` = over-NAKing (NOT unrecovered packets!)

**Both drop types represent packets that DID arrive** (as retransmits), just after another copy already arrived!

### 3.8 Corrected Analysis

| Metric | Value | Meaning |
|--------|-------|---------|
| Gaps | 165 | Unique packets that were lost |
| Retransmits | 239 | Total retransmit attempts |
| already_acked | 39 | Retransmits arriving after gap filled |
| duplicate | 46 | Retransmits arriving while buffered |
| **TRUE unrecovered** | 0 (Skips) | Packets that NEVER arrived |
| **TRUE recovery** | 100% | All gaps eventually filled! |

The 48% "recovery" rate is **misleading** because it's counting duplicate arrivals as failures!

### 3.9 Why So Many Duplicate Retransmits?

With bidirectional 2% loss:
1. P(original packet lost) = 2%
2. P(NAK lost) = 2%
3. P(retransmit lost) = 2%

When a retransmit is lost:
- Periodic NAK (every 20ms) requests again
- Multiple retransmits in flight
- Eventually one arrives → gap filled → ACK advances
- Later retransmits → dropped as `already_acked`

**This is expected behavior** with aggressive NAKing! The drops are "wasted" retransmits, not unrecovered packets.

### 3.10 Resolution: Fixed Recovery Rate Calculation

**Changes made to `analysis.go`**:

1. **TRUE recovery rate now uses ONLY TSBPD skips**:
   ```go
   // Old (WRONG):
   RecoveryRate = 1 - (Drops / Gaps)  // Counted redundant arrivals as losses!

   // New (CORRECT):
   RecoveryRate = 1 - (TSBPD_Skips / Gaps)  // Only true losses
   ```

2. **Clarified drop categories**:
   - **TRUE losses**: `Skips` (TSBPD) - packets that NEVER arrived
   - **Redundant arrivals**: `Drops` (too_late, already_acked, duplicate) - packets that arrived, but were discarded as duplicates

3. **Updated output format**:
   ```
   True losses (TSBPD skips): 0
   Redundant copies discarded: 85 (late arrivals)
   Combined recovery rate: 100.0%
   ```

4. **Updated validation check**:
   - Violation message now correctly identifies TSBPD skips as the true loss metric

**Changes made to `statistics.go` (real-time display)**:

1. **Renamed "loss" to "gaps"** in throughput display:
   ```
   // Old (MISLEADING):
   [SUB] 14:41:47.23 | 0.5k ok / 22 loss / 35 retx ~= 96.1%

   // New (CORRECT):
   [SUB] 14:41:47.23 | 0.5k ok / 22 gaps / 35 retx ~= 96.1% success
   ```

2. **Updated comments** to clarify that "gaps" are sequence gaps (triggers NAK/retrans), NOT true losses

**Expected behavior after fix**:
- `Skips: 0` → 100% recovery rate (all gaps eventually filled)
- The drops are not losses - they're duplicate retransmits that arrived after the gap was already filled
- Real-time display now correctly shows "gaps" instead of "loss"

### 3.11 Verified Results

After fix, the test shows:
```
Connection1 (Publisher → Server):
  Receiver: recv=3721, gaps=112 (2.94%), true_loss=0, recovery=100.0%
    Drops: 57 (too_late=0, already_ack=33, dupes=24), Skips: 0

Connection2 (Server → Subscriber):
  Receiver: recv=4090, gaps=108 (2.59%), true_loss=0, recovery=100.0%
    Drops: 60 (too_late=0, already_ack=26, dupes=34), Skips: 0

Combined Statistics:
  True losses (TSBPD skips): 0
  Redundant copies discarded: 117 (late arrivals)
  Combined recovery rate: 100.0%
```

**Key insight**: SRT IS working correctly! All gaps are being recovered. The "drops" are just duplicate retransmits - a sign of aggressive (but successful) NAKing.

---

## Changelog

| Date | Change | Author |
|------|--------|--------|
| 2024-12-10 | Created defect document with 7 hypotheses | - |
| 2024-12-10 | Added detailed analysis of NAK delivery rate calculation | - |
| 2024-12-10 | Added simplified single-connection test design | - |
| 2024-12-10 | Drew network topology showing netem placement | - |
| 2024-12-10 | Reviewed netem qdisc configuration (egress-only confirmed) | - |
| 2024-12-10 | Added mathematical analysis of expected vs observed recovery | - |
| 2024-12-10 | Defined 4 specific experiments to run | - |
| 2024-12-10 | **PHASE 1 COMPLETE**: Found bug in `receive.go:311` - immediate NAK always counted as Range | - |
| 2024-12-10 | Documented bug impact: NAKPktsRequested is overcounted, causing low NAK Delivery Rate | - |
| 2024-12-10 | Proposed fix with proper single/range detection | - |
| 2024-12-10 | **PHASE 1.1 IMPLEMENTED**: Created shared `CountNAKEntries()` helper | - |
| 2024-12-10 | Added `metrics/nak_counter.go` with 10 unit tests | - |
| 2024-12-10 | Updated `receive.go` immediate & periodic NAK paths to use helper | - |
| 2024-12-10 | Updated `send.go` NAK receive path to use helper | - |
| 2024-12-10 | Fixed `CongestionRecvPktLoss` to match actual missing packets | - |
| 2024-12-10 | All tests pass including updated `TestReceiverLossCounter` | - |
| 2024-12-10 | **PHASE 1.3 VERIFIED**: NAK accounting fix confirmed (99.1% delivery, 100% fulfillment) | - |
| 2024-12-10 | Identified remaining issue: 45% recovery despite correct NAK accounting | - |
| 2024-12-10 | **PHASE 2: METRICS GAP ANALYSIS** | - |
| 2024-12-10 | Reviewed `metrics_and_statistics_design.md`, `packet_loss_drop_definitions.md` | - |
| 2024-12-10 | Reviewed `metrics_implementation_progress.md`, `metrics_and_statistics_audit.md` | - |
| 2024-12-10 | Ran `tools/metrics-audit/main.go` - all counters aligned | - |
| 2024-12-10 | **FOUND MISSING COUNTER**: `CongestionRecvPktSkippedTSBPD` for packets never arrived | - |
| 2024-12-10 | Documented design gap: "PktDrop" only tracks packets that ARRIVED | - |
| 2024-12-10 | Complete packet flow audit shows TSBPD skip is the only missing scenario | - |
| 2024-12-10 | Added tick counters: `PeriodicACKRuns` (~100/sec) and `PeriodicNAKRuns` (~50/sec) | - |
| 2024-12-10 | Updated implementation plan for Phase 2 (8 steps, 4 counters total) | - |
| 2024-12-10 | **PHASE 2 IMPLEMENTATION COMPLETE** | - |
| 2024-12-10 | Added 4 counters to `metrics/metrics.go` | - |
| 2024-12-10 | Implemented TSBPD skip counting in `congestion/live/receive.go:periodicACK()` | - |
| 2024-12-10 | Implemented tick counters in `periodicACKWriteLocked()` and `periodicNAKLocked()` | - |
| 2024-12-10 | Exported 4 counters in `metrics/handler.go` | - |
| 2024-12-10 | Updated `analysis.go` with TSBPD skip counter and timer health | - |
| 2024-12-10 | Added 3 unit tests for new counters (all pass) | - |
| 2024-12-10 | Metrics audit verified: 135 fields fully aligned, 4 new counters have single increment | - |
| 2024-12-10 | **PHASE 2.8 TEST RUN**: `skips=0` observed, all 36 drops are from `already_acked`/`duplicate` | - |
| 2024-12-10 | Finding: With 3s buffer + 2% loss, packets ALWAYS arrive eventually (no true TSBPD skips) | - |
| 2024-12-10 | The "drops" are retransmits that arrived AFTER ACK advanced, not never-arrived packets | - |

