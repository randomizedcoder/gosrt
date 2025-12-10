# Defect 10: High Loss Rate Despite Large SRT Buffers

**Status**: 🔴 Under Investigation
**Priority**: High
**Discovered**: 2024-12-10
**Related Documents**:
- `integration_testing_with_network_impairment_defects.md` - Parent tracking document
- `integration_testing_design.md` - Test framework design
- `metrics_analysis_design.md` - Metrics analysis methodology

---

## Problem Statement

With only **2% netem packet loss** configured and **3-second SRT latency buffers**, the test shows unexpectedly poor recovery:

| Metric | Expected | Actual | Issue |
|--------|----------|--------|-------|
| Gap detection rate | ~2% | 10.72% | **5.4x higher** |
| Recovery rate | 95%+ | 68.9% | **26% lower** |
| NAK delivery rate | 98%+ | ~60% | **~38% lower** |
| Packets unrecovered | <10 | 127 | **12x higher** |

**Key Question**: Why is SRT unable to recover from 2% loss when it has 3-second buffers?

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
| 1.3 | Re-run tests to verify fix | 🔴 Not Started |
| 2 | Design Single Connection test | 📝 Documented |
| 3 | Draw network topology with netem | 📝 Documented |
| 4 | Review netem configuration | 📝 Documented |
| 5 | Mathematical analysis | 📝 Documented |
| 6 | Define experiments | 📝 Documented |

**BUG FIXED (Phase 1.1)**: Immediate NAK path in `receive.go` now uses shared `CountNAKEntries()` helper.

**ADDITIONAL BUG FIXED (Phase 1.1)**: Loss counter (`CongestionRecvPktLoss`) was counting 1 extra packet due to using `Distance(pkt, maxSeen)` instead of the actual NAK list.

**Phase 1.3 COMPLETE**: NAK accounting fix verified:
- NAK Delivery Rate: 99.1% (was 60% before fix) ✅
- NAK Fulfillment Rate: 100% ✅

**NEW ISSUE DISCOVERED**: Despite NAKs working correctly, recovery rate is still 45%!

**Root Cause Identified (Phase 1.4)**: Missing "TSBPD Skip" counter!
- When a packet never arrives (lost + all retransmissions lost), the receiver skips over it at TSBPD time
- This is NOT counted as a "drop" - the current drop counters only track packets that ARRIVED but were discarded
- The verbose output shows `DROPS: +5 (too_late: 0, buf_full: 0, dupes: 0)` - the 5 drops are not categorized!

**Next Step**: Investigate why packets are not being recovered despite 150+ potential NAK cycles

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

