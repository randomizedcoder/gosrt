# NAK Btree Expiry Optimization - RTT-Aware Early Expiry

> **Document Purpose:** Design document for implementing RTT-aware early expiry of NAK btree entries.
> **Implementation Plan:** `nak_btree_expiry_optimization_implementation_plan.md` (step-by-step guide)
> **Parent Documents:**
> - `design_nak_btree.md` (Section 4.3.4: NAK btree Entry Expiry, FR-19)
> - `rto_suppression_implementation.md` (Phase 1: RTO Calculation Infrastructure)
> **Status:** DESIGN
> **Date:** 2026-01-10

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
   - [1.1 Observed Behavior](#11-observed-behavior)
   - [1.2 The Problem](#12-the-problem)
   - [1.3 Why This Is Wasteful](#13-why-this-is-wasteful)
2. [Root Cause Analysis](#2-root-cause-analysis)
   - [2.1 Current Expiry Behavior](#21-current-expiry-behavior)
   - [2.2 The Timing Problem](#22-the-timing-problem)
   - [2.3 Why This Happens More with EventLoop](#23-why-this-happens-more-with-eventloop)
3. [Design Requirements](#3-design-requirements)
4. [Proposed Solution](#4-proposed-solution)
   - [4.1 Core Principle](#41-core-principle)
   - [4.2 Time-Based NAK Entry Expiry](#42-time-based-nak-entry-expiry)
   - [4.3 Reusing RTO Infrastructure](#43-reusing-rto-infrastructure)
   - [4.4 TSBPD Time Source](#44-tsbpd-time-source)
   - [4.5 TSBPD Time Estimation for NAK Entries](#45-tsbpd-time-estimation-for-nak-entries)
   - [4.6 EWMA Warm-up Strategy](#46-ewma-warm-up-strategy)
5. [Configuration](#5-configuration)
   - [5.1 NAK Btree Expiry Margin](#51-nak-btree-expiry-margin)
   - [5.2 Configuration Options](#52-configuration-options)
   - [5.3 EWMA Warm-up Threshold](#53-ewma-warm-up-threshold)
   - [5.4 Operational Guidance](#54-operational-guidance)
   - [5.5 Metrics for Tuning](#55-metrics-for-tuning)
   - [5.6 Comparison with ExtraRTTMargin](#56-comparison-with-extrarttmargin)
6. [Implementation Plan](#6-implementation-plan)
   - [6.1 Files to Modify](#61-files-to-modify)
   - [6.2 Step-by-Step Implementation](#62-step-by-step-implementation)
7. [Corner Cases and Failure Modes](#7-corner-cases-and-failure-modes)
   - [7.1 What Can Go Wrong](#71-what-can-go-wrong)
   - [7.2 Corner Case Matrix](#72-corner-case-matrix)
   - [7.3 Table-Driven Test Design](#73-table-driven-test-design)
8. [Testing Strategy](#8-testing-strategy)
   - [8.1 Unit Tests](#81-unit-tests)
   - [8.2 Integration Tests](#82-integration-tests)
   - [8.3 Benchmark Tests](#83-benchmark-tests)
9. [Performance Considerations](#9-performance-considerations)
   - [9.1 Memory Impact](#91-memory-impact)
   - [9.2 CPU Impact](#92-cpu-impact)
10. [Metrics and Observability](#10-metrics-and-observability)
11. [Implementation Safety Measures](#11-implementation-safety-measures)
   - [11.1 TSBPD Monotonicity Guard](#111-tsbpd-monotonicity-guard)
   - [11.2 Configuration Validation](#112-configuration-validation)
   - [11.3 Named Constants for Testability](#113-named-constants-for-testability)
   - [11.4 Optimized Batch TSBPD Calculation](#114-optimized-batch-tsbpd-calculation)
12. [Rollout Plan](#12-rollout-plan)

---

## 1. Problem Statement

### 1.1 Observed Behavior

In `Parallel-Clean-20M-FullEL-vs-FullSendEL` tests, the sender EventLoop (HighPerf) shows elevated NAKs compared to the baseline:

| Metric | Baseline | HighPerf (SendEventLoop) |
|--------|----------|--------------------------|
| NAKs received by sender | 0 | 1,722 |
| `internal_total [nak_not_found]` | 0 | 1,794 |
| Retransmissions sent | 0 | 1 |
| Gaps at end | 0 | 0 |
| Recovery | 100% | 100% |

### 1.2 The Problem

The `nak_not_found: 1794` metric indicates that when the sender EventLoop receives NAKs and tries to retransmit, the packets are **already gone from the sender's btree** (ACKed and removed).

This happens because:
1. Receiver detects a "gap" and adds entry to NAK btree
2. Receiver sends NAK to sender
3. **Meanwhile**, the original packet arrives (was just delayed, not lost)
4. Receiver sends ACK for the packet
5. ACK reaches sender, sender removes packet from btree
6. NAK reaches sender, sender tries to retransmit → **packet not found**

### 1.3 Why This Is Wasteful

Even though the test passes (100% recovery), these "phantom NAKs" cause:
- **Unnecessary NAK generation** - wasted receiver CPU
- **Unnecessary NAK transmission** - wasted bandwidth
- **Unnecessary NAK processing** - wasted sender CPU
- **`nak_not_found` counter increments** - confusing metrics

---

## 2. Root Cause Analysis

### 2.1 Current NAK Btree Expiry Logic

**File:** `congestion/live/receive/nak.go:509-544`

```go
// expireNakEntries removes entries from the NAK btree that are too old to be useful.
// An entry is expired if its sequence is less than the oldest packet in the packet btree.
func (r *receiver) expireNakEntries() int {
    // Find the oldest packet in the packet btree (brief lock)
    r.lock.RLock()
    minPkt := r.packetStore.Min()
    var cutoff uint32
    if minPkt != nil {
        cutoff = minPkt.Header().PacketSequenceNumber.Val()
    }
    r.lock.RUnlock()

    if minPkt == nil {
        return 0
    }

    // Any NAK entry older than the oldest packet's sequence is expired
    expired := r.nakDeleteBefore(cutoff)
    // ...
}
```

**Current behavior:** NAK entries expire when the packet btree releases packets (at TSBPD time).

**TODO:** expireNakEntries we need to review if we can make this lock free using a control packets lock free ring, like the lockless_sender_design.md.

### 2.2 The Timing Problem

```
Timeline showing why NAK entries should expire EARLIER than TSBPD:

                           TSBPD Time
                               │
    ─────────────────────────────────────────────────────────────────►
                               │
    │←───────── RTT ──────────►│
    │      (e.g., 1-2ms)       │
    │                          │
    NAK Entry      NAK useless │ Packet released
    Expires HERE → (too late)  │ from btree
    (RTT before TSBPD)         │
                               │
    If NAK sent at this point:
    - NAK travel time: RTT/2
    - Sender retransmit time: ~0
    - Retransmit travel time: RTT/2
    - Total: ~RTT

    If packet's TSBPD is in < RTT, retransmit CAN'T arrive in time!
```

### 2.3 Relationship to Existing FR-19

From `design_nak_btree.md` Section 3.1.6:

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-19 | **Expire entries** based on RTT consideration | Stop NAKing packets too late for retransmission to help |

This requirement was documented but not fully implemented. The current implementation uses TSBPD-based expiry (Phase 1), but the RTT-aware expiry (Phase 2) was marked as "future optimization."

### 2.4 Relationship to RTO Suppression

From `rto_suppression_implementation.md`:

The RTO calculation infrastructure is **already implemented and tested**:
- `connection_rtt.go` provides `RTOUs()` method
- `congestion.RTTProvider` interface wires RTT to receiver
- Receiver's `consolidateNakBtree()` already uses `r.rtt.RTOUs()` for NAK suppression

The same infrastructure can be reused for early NAK expiry.

---

## 3. Design Requirements

### 3.1 Functional Requirements

| ID | Requirement | Rationale |
|----|-------------|-----------|
| EXP-1 | Expire NAK entries when `now + RTT > TSBPD_release_time` | No point NAKing if retransmit can't arrive in time |
| EXP-2 | Use existing RTO calculation infrastructure | Avoid duplication, leverage tested code |
| EXP-3 | Fallback to TSBPD-based expiry if RTT unavailable | Graceful degradation for early connection state |
| EXP-4 | Add metric for RTT-based early expirations | Observability into optimization effectiveness |

### 3.2 Performance Requirements

| ID | Requirement | Rationale |
|----|-------------|-----------|
| PERF-1 | Single RTT atomic load per expiry cycle | Minimize atomic operations |
| PERF-2 | No additional locks | Expiry already has brief RLock for packetStore.Min() |
| PERF-3 | Same O(n) complexity as current implementation | Don't regress performance |

### 3.3 Non-Requirements

| ID | Non-Requirement | Rationale |
|----|-----------------|-----------|
| NR-1 | Exact RTT matching | Using RTOUs (RTT + RTTVar) provides safety margin |
| NR-2 | Per-packet RTT tracking | Connection-level RTT is sufficient |

---

## 4. Proposed Solution

### 4.1 Design Decision: Time-Based Expiry

We select **time-based expiry** because:
1. **Accuracy** - Directly compares TSBPD time with RTT, no approximations
2. **Consistency** - Other btree scans (packet delivery, NAK generation) are already time-based
3. **Correctness** - Works correctly regardless of bitrate or packet size variations
4. **Existing Pattern** - `NakEntryWithTime` struct already exists, we're adding one more time field

### 4.2 Data Structure Change

**Current `NakEntryWithTime` struct:**

```go
// congestion/live/receive/nak_btree.go:15-19
type NakEntryWithTime struct {
    Seq           uint32 // Missing sequence number
    LastNakedAtUs uint64 // When we last sent NAK for this seq (microseconds)
    NakCount      uint32 // Number of times NAK'd
}
```

**Updated struct with TSBPD time:**

```go
type NakEntryWithTime struct {
    Seq           uint32 // Missing sequence number
    TsbpdTimeUs   uint64 // TSBPD release time for this sequence (microseconds)
    LastNakedAtUs uint64 // When we last sent NAK for this seq (microseconds)
    NakCount      uint32 // Number of times NAK'd
}
```

**Memory impact:** +8 bytes per NAK entry (uint64 for TsbpdTimeUs)
- At typical NAK btree sizes (10-100 entries), this is 80-800 bytes additional
- Acceptable tradeoff for correctness

### 4.3 Algorithm

```
Time-Based RTT-Aware Expiry Algorithm:

Input:
  - nowUs: current time in microseconds
  - rtoUs: RTT + RTTVar (from r.rtt.RTOUs())
  - nakBtree: btree of NakEntryWithTime, ordered by sequence

Output:
  - expired count
  - nakBtree with expired entries removed

Algorithm:
  1. Calculate expiry threshold:
     expiryThresholdUs = nowUs + rtoUs

  2. Iterate NAK btree (ascending order):
     for each entry in nakBtree:
       if entry.TsbpdTimeUs < expiryThresholdUs:
         // Too late to NAK - retransmit can't arrive in time
         delete entry from nakBtree
         increment expired count
       else:
         // Entry still valid - retransmit could arrive in time
         // Note: btree is ordered by sequence which correlates with TSBPD
         // So once we find a valid entry, all subsequent are also valid
         break

  3. Return expired count

Invariant:
  For any NAK entry, if now + RTT > TSBPD_time, the NAK is useless
  because: NAK travel (RTT/2) + retransmit travel (RTT/2) > time until TSBPD
```

### 4.4 Visualization

```
Timeline showing time-based expiry decision:

                    nowUs              nowUs + rtoUs         TSBPD
                      │                     │                  │
    ──────────────────┼─────────────────────┼──────────────────┼────────►
                      │                     │                  │
                      │◄───── rtoUs ───────►│                  │
                      │     (RTT + var)     │                  │
                      │                     │                  │
    Entry A:          │          │          │                  │
    TsbpdTimeUs=T1    │          ▼          │                  │
                      │   [EXPIRED: T1 < nowUs + rtoUs]        │
                      │                     │                  │
    Entry B:          │                     │         │        │
    TsbpdTimeUs=T2    │                     │         ▼        │
                      │                     │   [KEEP: T2 >= nowUs + rtoUs]
                      │                     │                  │
                      │                     │                  │
    Decision:                                                  │
    - If TsbpdTimeUs < (nowUs + rtoUs): EXPIRE                │
    - If TsbpdTimeUs >= (nowUs + rtoUs): KEEP                 │
```

### 4.5 TSBPD Time Estimation for NAK Entries

When a gap is detected and a NAK entry is created, we need to estimate the TSBPD time
for the **missing** packets. We have the surrounding packets' `PktTsbpdTime` but not
the missing packets themselves.

**Context from existing design** (see `design_nak_btree.md` FR-19):
> FR-19: **Expire entries** based on RTT consideration - Stop NAKing packets too late for retransmission to help

#### 4.5.1 The Estimation Challenge

**Gap Detection Scenario:**
```
Received packets: [seq=100, TSBPD=T_A] ... GAP ... [seq=105, TSBPD=T_B]
Missing packets:  seq=101, 102, 103, 104
Question: What TSBPD should we assign to seq=101-104?
```

**Key constraint:** TSBPD times increase monotonically with sequence numbers:
- `TSBPD_100 < TSBPD_101 < TSBPD_102 < TSBPD_103 < TSBPD_104 < TSBPD_105`

#### 4.5.2 Estimation Options Comparison

| Option | Description | Implementation |
|--------|-------------|----------------|
| **A: Linear Interpolation** | Calculate each missing seq's TSBPD by linear interpolation between boundary packets | `TSBPD_seq = T_A + (seq - A) * (T_B - T_A) / (B - A)` |
| **B: Use Earlier Packet (Conservative)** | Use `T_A` (packet before gap) for all entries in gap | `TSBPD_gap = T_A` |
| **C: Use Later Packet** | Use `T_B` (packet after gap) for all entries in gap | `TSBPD_gap = T_B` |
| **D: Inter-Packet Interval EWMA** | Track average interval, extrapolate from reference | `TSBPD_seq = T_ref + (seq - ref) * avgInterval` |

#### 4.5.3 Pros/Cons Analysis

| Option | Pros | Cons |
|--------|------|------|
| **A: Linear Interpolation** | Most accurate; error bounded by gap size; works for any gap size | Requires both boundary packets; slightly more complex math |
| **B: Earlier Packet** | Simple; single reference packet needed; conservative (expires early) | **May expire NAKs too early for large gaps**, missing retransmit opportunity |
| **C: Later Packet** | Simple; single reference packet | Wrong direction! Expires NAKs too late, causes more phantom NAKs |
| **D: Inter-Packet EWMA** | Works with single reference; adapts to bitrate changes | Requires tracking state; EWMA may lag during rate changes |

#### 4.5.4 Packet Loss Scenario Analysis

| Scenario | Gap Size | @20Mbps (~1700pkt/s) | Option A Error | Option B Error | Impact |
|----------|----------|----------------------|----------------|----------------|--------|
| **Single packet loss** | 1 | ~0.6ms duration | ~0ms | ~0.6ms early | Minimal |
| **Small burst (random)** | 5 | ~3ms duration | ~0ms | ~3ms early | Minimal |
| **Medium burst (congestion)** | 20 | ~12ms duration | ~0ms | ~12ms early | Low |
| **Large burst (Starlink outage)** | 100 | ~60ms duration | ~0ms | ~60ms early | **Significant** |
| **Massive outage** | 500 | ~300ms duration | ~0ms | ~300ms early | **Critical** |

#### 4.5.4.1 Large/Extended Outage Scenarios (1s, 10s, 5min)

For extended network outages, **SRT's existing mechanisms handle cleanup** - our NAK btree
expiry optimization works *within* these bounds, not against them.

| Outage Duration | Packets Missed @20Mbps | What Actually Happens |
|-----------------|------------------------|----------------------|
| **1 second** | ~1,700 | Most packets' TSBPD already expired; only tail recoverable |
| **10 seconds** | ~17,000 | All packets' TSBPD expired; connection may timeout |
| **5 minutes** | ~510,000 | Connection terminated by SRT keepalive timeout (~5s) |

**Key SRT Timing Mechanisms:**

1. **TSBPD Delay** (default 120ms): Packets must be delivered by TSBPD time
   - After 1s outage: first 880ms of packets have TSBPD in the past → unrecoverable
   - Only last ~120ms worth of packets (~200 at 20Mbps) could theoretically be recovered

2. **Sender Drop Threshold** (~1.25×TSBPD + SendDropDelay ≈ 1s):
   - Sender drops packets from its btree when they're too old
   - After 1s outage: sender has already dropped most packets
   - NAKing them would result in `nak_not_found` anyway

3. **SRT Keepalive Timeout** (default ~5s):
   - No data for 5s → connection terminated
   - 10s or 5min outages → connection is dead/reconnecting

4. **Receiver TSBPD Scan** (`gapScan` threshold):
   - Only scans packets within TSBPD window (not too recent)
   - Packets with TSBPD in the past are delivered/dropped, removing them from consideration

**Timeline: 1-Second Outage at 20Mbps (TSBPD=120ms)**

```
T=0ms:      Last packet received (seq=1000, TSBPD=T₀+120ms)
T=0-1000ms: OUTAGE - no packets arrive

What happens during outage:
  T=120ms:  Packet 1000's TSBPD arrives → delivered from receiver btree
  T=121ms:  NAK btree entry for seq=1001 expires (TSBPD < now+RTT)
  ...
  T=880ms:  All packets up to seq≈1500 have expired TSBPD

T=1000ms:   First packet after outage (seq=2700, TSBPD=T₁+120ms)
            Gap detected: seq 1001-2699 (~1700 packets)

Analysis of missing packets:
  - seq 1001-1500 (~850 pkts): TSBPD in past, unrecoverable, sender dropped
  - seq 1501-2550 (~1050 pkts): TSBPD in past, unrecoverable, sender dropped
  - seq 2551-2699 (~150 pkts): TSBPD still valid, MAY be recoverable

Result: Only ~150 packets (last 90ms worth) worth NAKing
```

**How Our Optimization Handles This:**

1. **RTT-aware expiry** prevents useless NAKs:
   - When gap detected, we estimate TSBPD for each missing seq
   - Immediately expire entries where `TSBPD < now + RTT`
   - Only ~150 entries (the recoverable tail) remain in NAK btree

2. **No NAK btree explosion**:
   - We DON'T insert 1700 entries only to immediately expire 1550 of them
   - Insert only entries with valid TSBPD (optimization opportunity)

3. **Linear interpolation accuracy**:
   - For the recoverable tail (~150 packets), accurate TSBPD estimation ensures
     we NAK at the right time, maximizing recovery chance

**Optimization: Pre-filter Before Insertion**

For large gaps, we can pre-filter to avoid inserting already-expired entries:

```go
// In gap detection, before inserting NAK entries:
nowUs := r.nowFn()
rtoUs := r.getRTOUs()
expiryThreshold := nowUs + rtoUs

// Only insert entries that have a chance of being useful
for seq := lowerSeq + 1; seq < upperSeq; seq++ {
    tsbpd := estimateTsbpdForSeq(seq, lowerSeq, lowerTsbpd, upperSeq, upperTsbpd)

    // Skip if TSBPD already expired (no point NAKing)
    if tsbpd < expiryThreshold {
        m.NakBtreeSkippedExpired.Add(1)  // Metric for visibility
        continue
    }

    r.nakBtree.InsertWithTsbpd(seq, tsbpd)
}
```

**New Metric for Extended Outages:**

```go
NakBtreeSkippedExpired atomic.Uint64 // Entries not inserted (already expired at detection time)
```

This metric helps operators identify when large outages occurred and how many
packets were beyond recovery.

**Starlink Scenario Analysis** (see `integration_testing_with_network_impairment_defects.md`):
- Starlink has periodic 60ms reconvergence events causing burst losses
- At 20Mbps: 60ms ≈ 100 packets lost
- FastNAK feature (`design_nak_btree.md` FR-13/14) triggers immediately when packets resume
- **Risk with Option B:** All 100 NAK entries get TSBPD ~60ms earlier than actual
  - With RTT=10ms, entries expire ~50ms before they should
  - We might miss the window to NAK packets that could still arrive in time!

#### 4.5.5 Option B Risk Analysis (Too-Early Expiry)

```
Timeline (Starlink 60ms outage at 20Mbps):

T=0ms:    Last packet received (seq=1000, TSBPD=T₀)
T=0-60ms: Outage - no packets arrive
T=60ms:   First packet after outage (seq=1100, TSBPD=T₁)
          Gap detected: seq 1001-1099 (99 packets)

With Option B (use earlier TSBPD=T₀):
  - NAK entry for seq=1001: TSBPD=T₀ (correct: ~T₀+0.6ms)
  - NAK entry for seq=1050: TSBPD=T₀ (correct: ~T₀+30ms)  ← 30ms error!
  - NAK entry for seq=1099: TSBPD=T₀ (correct: ~T₀+59ms)  ← 59ms error!

Expiry check at T=65ms (RTT=10ms, threshold=now+RTT=75ms):
  - Option B: All entries show TSBPD=T₀ ≈ 0ms < 75ms → ALL EXPIRED!
  - Option A: Entry 1099 shows TSBPD≈59ms < 75ms → only oldest expired

Problem: With Option B, we expire NAKs for packets that could still be retransmitted!
Seq=1099's actual TSBPD is ~59ms, we still have 16ms to NAK and receive retransmit.
```

#### 4.5.6 Recommendation: Option A (Linear Interpolation)

For large gap scenarios (FastNAK/Starlink), **Option A provides the best accuracy**:

```go
// estimateTsbpdForSeq calculates TSBPD for a missing sequence using linear interpolation
// between two boundary packets.
//
// Parameters:
//   - missingSeq: The missing sequence number needing TSBPD estimation
//   - lowerSeq: Sequence of packet before the gap (has known TSBPD)
//   - lowerTsbpd: TSBPD time of the lower boundary packet
//   - upperSeq: Sequence of packet after the gap (has known TSBPD)
//   - upperTsbpd: TSBPD time of the upper boundary packet
//
// Returns: Estimated TSBPD for missingSeq
func estimateTsbpdForSeq(missingSeq, lowerSeq uint32, lowerTsbpd uint64, upperSeq uint32, upperTsbpd uint64) uint64 {
    // Handle edge case: if boundaries are same (shouldn't happen)
    if upperSeq == lowerSeq {
        return lowerTsbpd
    }

    // Linear interpolation:
    // TSBPD_missing = lower + (missing - lower) * (upper - lower_tsbpd) / (upper_seq - lower_seq)
    seqRange := uint64(circular.SeqSub(upperSeq, lowerSeq))
    tsbpdRange := upperTsbpd - lowerTsbpd
    seqOffset := uint64(circular.SeqSub(missingSeq, lowerSeq))

    return lowerTsbpd + (seqOffset * tsbpdRange / seqRange)
}
```

**Fallback for edge cases** (no lower boundary packet):
- Use Option D (inter-packet EWMA) when only one boundary packet available
- Track `avgInterPacketIntervalUs` as backup

#### 4.5.7 Implementation Consideration: Gap Scan Context

During `gapScan()`, we iterate through the packet btree and have access to:
1. The packet **before** the gap (lower boundary) - has `PktTsbpdTime`
2. The packet **after** the gap (upper boundary) - has `PktTsbpdTime`

This makes Option A (linear interpolation) natural to implement:

```go
// In gapScan(), when gap detected between prevPkt and currentPkt:
lowerSeq := prevPkt.Header().PacketSequenceNumber.Val()
lowerTsbpd := prevPkt.Header().PktTsbpdTime
upperSeq := currentPkt.Header().PacketSequenceNumber.Val()
upperTsbpd := currentPkt.Header().PktTsbpdTime

// For each missing sequence in the gap:
for seq := lowerSeq + 1; seq < upperSeq; seq++ {
    tsbpd := estimateTsbpdForSeq(seq, lowerSeq, lowerTsbpd, upperSeq, upperTsbpd)
    r.nakBtree.InsertWithTsbpd(seq, tsbpd)
}
```

### 4.6 EWMA Warm-up Strategy

#### 4.6.1 The Cold Start Problem

On connection startup, we face a bootstrapping challenge:

| Metric | Initial Value | Warm Value | Problem |
|--------|---------------|------------|---------|
| `avgInterPacketIntervalUs` | 0 | ~500-1000µs | No data yet |
| RTT/RTO | 0 | ~10-50ms | Not measured |
| Packet count | 0 | 1000s | Need samples for EWMA |

**Consequences of cold start estimation:**
- Inter-packet interval defaults to `InterPacketIntervalDefaultUs` (1ms)
- This may be 2-10x off from actual rate
- Early gaps get inaccurate TSBPD estimates
- Could expire NAKs too early (miss recovery) or too late (phantom NAKs)

#### 4.6.2 Warm-up Threshold Design

We define a configurable warm-up threshold based on packet count to determine when estimates are reliable:

```go
// In config.go - see Section 5.3 for full configuration details

// EWMAWarmupThreshold is the minimum number of packets needed before
// inter-packet interval EWMA is considered "warm" (reliable).
//
// Rationale:
// - EWMA with α=0.125 reaches ~95% of true value after ~24 samples
// - Default of 32 provides safety margin for variance
// - At 1000 pps, this is only 32ms of data
// - At 100 pps (low bitrate), this is 320ms
//
// During warm-up, we use conservative fallback behavior.
//
// Configurable via:
//   config.EWMAWarmupThreshold (default: 32)
//   CLI flag: -ewmawarmupthreshold
```

**Why configurable:**
- High-rate streams (>10Mbps): May want lower threshold (16-24) for faster warm-up
- Low-rate streams (<1Mbps): May want higher threshold (48-64) for more accuracy
- Testing: Set to 0 to disable warm-up behavior entirely

**EWMA Convergence Analysis:**

With weights α=0.125 (new) and β=0.875 (old):
```
After N samples, influence of initial value = β^N
  N=8:   0.875^8  = 0.34 (34% initial influence)
  N=16:  0.875^16 = 0.12 (12% initial influence)
  N=24:  0.875^24 = 0.04 (4% initial influence)
  N=32:  0.875^32 = 0.01 (1% initial influence) ← Threshold
```

#### 4.6.3 Warm-up Behavior Options

**Option W1: Conservative Fallback (Recommended)**

During warm-up, use tsbpdDelay as a worst-case estimate:
```go
func (r *receiver) estimateTsbpdFallbackWithWarmup(missingSeq, refSeq uint32, refTsbpd uint64) uint64 {
    // Check if EWMA is warm
    if r.interPacketSampleCount.Load() < EWMAWarmupThreshold {
        // Cold: use conservative estimate
        // Assume 1 packet per TSBPD delay (very conservative)
        // This means we'll NAK slightly more than necessary initially
        return refTsbpd + r.tsbpdDelay
    }

    // Warm: use EWMA-based estimate
    intervalUs := r.avgInterPacketIntervalUs.Load()
    if intervalUs == 0 {
        intervalUs = InterPacketIntervalDefaultUs
    }

    seqDiff := int64(circular.SeqSub(missingSeq, refSeq))
    return uint64(int64(refTsbpd) + seqDiff*int64(intervalUs))
}
```

**Option W2: Skip Time-Based Expiry**

During warm-up, don't use time-based expiry at all:
```go
func (r *receiver) calculateExpiryThreshold(nowUs uint64) uint64 {
    // During warm-up, skip time-based expiry entirely
    // This means some phantom NAKs during initial 32 packets
    if r.interPacketSampleCount.Load() < EWMAWarmupThreshold {
        return 0 // Signal to use sequence-based fallback
    }

    rtoUs := r.getRTOUs()
    if rtoUs == 0 {
        return 0
    }

    return nowUs + uint64(float64(rtoUs)*(1.0+r.nakExpiryMargin))
}
```

**Option W3: Wider Margin During Warm-up**

Use a more conservative margin until warm:
```go
func (r *receiver) getEffectiveNakExpiryMargin() float64 {
    if r.interPacketSampleCount.Load() < EWMAWarmupThreshold {
        // Cold: use 2x margin (very conservative)
        return r.nakExpiryMargin * 2.0
    }
    return r.nakExpiryMargin
}
```

#### 4.6.4 Option Comparison

| Option | During Warm-up | Pros | Cons |
|--------|----------------|------|------|
| **W1: Conservative Fallback** | Use tsbpdDelay for estimates | Simple, predictable | May over-NAK for high-rate streams |
| **W2: Skip Time-Based** | Use sequence-based expiry | No estimation risk | Phantom NAKs during warm-up |
| **W3: Wider Margin** | 2x nakExpiryMargin | Graduated approach | Still uses potentially wrong EWMA |

**Recommendation: W1 (Conservative Fallback)**

Rationale:
1. Linear interpolation (Option A) doesn't depend on EWMA - uses boundary packets directly
2. EWMA fallback is only used for edge cases (gap at start, single packet)
3. During warm-up, these edge cases are rare (we need packets to detect gaps!)
4. When edge cases do occur, conservative estimate is safer than wrong estimate

#### 4.6.5 Sample Count Tracking

Add atomic counter to track warm-up progress:

```go
// In receiver struct:
interPacketSampleCount atomic.Uint32 // Count of valid inter-packet samples

// In updateInterPacketInterval():
if newInterval, valid := updateInterPacketInterval(nowUs, lastArrivalUs, oldInterval); valid {
    r.avgInterPacketIntervalUs.Store(newInterval)
    // Increment sample count (saturate at max to avoid overflow)
    count := r.interPacketSampleCount.Load()
    if count < math.MaxUint32 {
        r.interPacketSampleCount.Add(1)
    }
}
```

#### 4.6.6 Warm-up Timeline Example

```
Connection startup at T=0:

T=0ms:     First packet arrives
           interPacketSampleCount = 0 (cold)

T=1ms:     Second packet arrives
           intervalUs = 1000 (actual)
           interPacketSampleCount = 1 (cold)

T=32ms:    33rd packet arrives (at ~1000 pps)
           avgInterPacketIntervalUs ≈ 1000µs (converged)
           interPacketSampleCount = 32 (WARM!)

T=35ms:    Gap detected (packets 34-36 missing)
           Linear interpolation uses boundary packets ← accurate
           Fallback would use warm EWMA ← also accurate

           Time-based expiry now fully enabled
```

**Low-bitrate scenario (100 pps):**
```
T=0ms:     First packet
T=320ms:   33rd packet (warm-up complete)
           During 320ms: sequence-based expiry or conservative estimate
           After: time-based expiry with accurate EWMA
```

#### 4.6.7 Interaction with Other Warm-up Periods

The receiver already has other warm-up considerations:
- **RTT warm-up**: RTT/RTO not available until first ACK roundtrip
- **Congestion control warm-up**: Link capacity estimation needs samples

Our EWMA warm-up aligns with these:
- RTT typically available within 10-100ms (first ACK)
- EWMA warm within 32 packets (~32-320ms depending on rate)
- Both usually warm by the time meaningful gaps occur

**Key insight:** The first ~32 packets rarely have gaps (connection just started,
buffers empty, no congestion). By the time gaps are likely, estimation is warm.

#### 4.6.8 Metrics for Warm-up Visibility

Add metric to track warm-up state:

```go
// Gauge: 1 if warm, 0 if cold
NakTsbpdEstimatorWarm atomic.Uint64 // Set to 1 when interPacketSampleCount >= threshold
```

Or counter for how often we used cold fallback:
```go
NakTsbpdEstColdFallback atomic.Uint64 // Times we used conservative estimate during warm-up
```

---

## 5. Configuration

### 5.1 NAK Btree Expiry Margin

The NAK btree expiry optimization operates in the **opposite direction** from NAK/retransmit suppression:

| Feature | Direction | Goal | Risk if Too Aggressive |
|---------|-----------|------|------------------------|
| **NAK Suppression** (rto_suppression) | Later expiry | Avoid redundant NAKs | May delay recovery |
| **Retransmit Suppression** | Later expiry | Avoid redundant retransmits | May delay recovery |
| **NAK Btree Expiry** (this design) | **Earlier expiry** | Avoid phantom NAKs | **May lose recovery opportunity** |

**Key Insight:** We should err on the side of keeping NAK entries **longer** rather than expiring them too early.
A few extra phantom NAKs are acceptable; losing recovery opportunity for urgent packets is not.

### 5.2 Configuration Options

Following the pattern established in `rto_suppression_implementation.md` (ExtraRTTMargin), we add
operator-configurable margin for NAK btree expiry using percentage-based adjustment.

**Formula:**
```
expiryThreshold = now + (RTO * (1 + NakExpiryMargin))
```

| nakExpiryMargin | Formula | At RTO=15ms | Effect |
|--------|---------|-------------|--------|
| 0.0 | `now + RTO * 1.0` | +15ms | Baseline (most aggressive) |
| 0.05 | `now + RTO * 1.05` | +15.75ms | 5% more conservative |
| **0.10** | `now + RTO * 1.10` | **+16.5ms** | **10% more conservative (default)** |
| 0.25 | `now + RTO * 1.25` | +18.75ms | 25% more conservative |
| 0.50 | `now + RTO * 1.50` | +22.5ms | 50% more conservative |
| 1.0 | `now + RTO * 2.0` | +30ms | 100% more conservative |

#### 5.2.1 Config Struct Addition

**File:** `config.go` (in Config struct, after ExtraRTTMargin around line 358)

```go
    // --- NAK Btree Expiry Configuration ---

    // NakExpiryMargin adds extra margin when expiring NAK btree entries.
    // Specified as a percentage (0.1 = 10% extra margin).
    //
    // Formula: expiryThreshold = now + (RTO * (1 + NakExpiryMargin))
    //
    // Higher values = more conservative (keep NAK entries longer, favor recovery).
    // Lower values = more aggressive (expire entries earlier, reduce phantom NAKs).
    //
    // Values:
    //   0.0:  Baseline - expire at exactly now + RTO
    //   0.05: 5% margin - slightly conservative
    //   0.10: 10% margin (default) - moderately conservative
    //   0.25: 25% margin - more conservative
    //   0.50: 50% margin - very conservative (high-jitter networks)
    //   1.0:  100% margin - doubles the RTO buffer
    //
    // Default: 0.10 (10% - prefer potential repair over phantom NAK reduction)
    NakExpiryMargin float64
```

#### 5.2.2 Default Value

**File:** `config.go` (in DefaultConfig(), around line 625)

```go
    // NAK btree expiry defaults
    NakExpiryMargin: 0.10, // 10% margin - slightly conservative, favors recovery
```

#### 5.2.3 CLI Flag

**File:** `contrib/common/flags.go` (after ExtraRTTMargin flag)

```go
var NakExpiryMargin = flag.Float64("nakexpirymargin", 0.10,
    "NAK btree expiry margin as percentage (0.1 = 10%). "+
        "Formula: expiryThreshold = now + (RTO * (1 + nakExpiryMargin)). "+
        "Higher values keep NAK entries longer, favoring recovery over phantom NAK reduction.")
```

**File:** `contrib/common/flags.go` (in ApplyFlagsToConfig())

```go
    if FlagSet["nakexpirymargin"] {
        config.NakExpiryMargin = *NakExpiryMargin
    }
```

#### 5.2.4 Expiry Threshold Calculation

**File:** `congestion/live/receive/nak.go` (in expireNakEntries())

```go
// calculateExpiryThreshold computes the TSBPD threshold for NAK entry expiry.
// Entries with TSBPD < threshold are expired (no time for retransmit to arrive).
//
// Formula: threshold = now + (RTO * (1 + nakExpiryMargin))
//
// This follows the same percentage-based pattern as ExtraRTTMargin in
// rto_suppression_implementation.md for consistency.
//
// Examples at RTO=15ms:
//   nakExpiryMargin=0.00: threshold = now + 15.0ms (baseline)
//   nakExpiryMargin=0.05: threshold = now + 15.75ms (5% conservative)
//   nakExpiryMargin=0.10: threshold = now + 16.5ms (10% conservative, default)
//   nakExpiryMargin=0.25: threshold = now + 18.75ms (25% conservative)
//   nakExpiryMargin=0.50: threshold = now + 22.5ms (50% conservative)
func (r *receiver) calculateExpiryThreshold(nowUs uint64) uint64 {
    rtoUs := r.getRTOUs()
    if rtoUs == 0 {
        return 0 // RTT not yet available - use fallback
    }

    // Apply percentage-based nakExpiryMargin: RTO * (1 + nakExpiryMargin)
    adjustedRtoUs := uint64(float64(rtoUs) * (1.0 + r.nakExpiryMargin))

    return nowUs + adjustedRtoUs
}
```

### 5.3 EWMA Warm-up Threshold

Following the same configuration pattern as `NakExpiryMargin`, the EWMA warm-up threshold is configurable
to handle different stream characteristics.

**Why configurable:**
- High-rate streams (>10Mbps): May want lower threshold (16-24) for faster warm-up
- Low-rate streams (<1Mbps): May want higher threshold (48-64) for more accuracy
- Testing: Set to 0 to disable warm-up behavior entirely

#### 5.3.1 Config Struct Addition

**File:** `config.go` (in Config struct, after NakExpiryMargin)

```go
    // EWMAWarmupThreshold is the minimum number of packets needed before
    // inter-packet interval EWMA is considered "warm" (reliable).
    //
    // Rationale:
    // - EWMA with α=0.125 reaches ~95% of true value after ~24 samples
    // - Default of 32 provides safety margin for variance
    // - At 1000 pps, this is only 32ms of data
    // - At 100 pps (low bitrate), this is 320ms
    //
    // Values:
    //   0:  Disable warm-up check (always use EWMA, even if cold)
    //   16: Fast warm-up (high-rate streams, less accuracy)
    //   32: Default (balanced)
    //   64: Slow warm-up (low-rate streams, more accuracy)
    //
    // During warm-up (sampleCount < threshold), we use conservative
    // fallback estimation. See Section 4.6.3 (Option W1).
    //
    // Default: 32
    EWMAWarmupThreshold uint32
```

#### 5.3.2 Default Value

**File:** `config.go` (in DefaultConfig(), after NakExpiryMargin)

```go
    NakExpiryMargin:     0.10, // 10% margin - slightly conservative, favors recovery
    EWMAWarmupThreshold: 32,   // 32 samples before EWMA considered warm
```

#### 5.3.3 CLI Flag

**File:** `contrib/common/flags.go` (after NakExpiryMargin flag)

```go
var EWMAWarmupThreshold = flag.Uint("ewmawarmupthreshold", 32,
    "Minimum packets before inter-packet EWMA is considered warm (reliable). "+
        "Set to 0 to disable warm-up check. "+
        "Higher values improve accuracy but delay time-based expiry. "+
        "Default: 32 (balanced for most streams)")
```

**File:** `contrib/common/flags.go` (in ApplyFlagsToConfig())

```go
    if FlagSet["ewmawarmupthreshold"] {
        config.EWMAWarmupThreshold = uint32(*EWMAWarmupThreshold)
    }
```

#### 5.3.4 Warm-up Check Implementation

**File:** `congestion/live/receive/nak.go`

```go
// isEWMAWarm returns true if enough inter-packet samples have been collected
// for the EWMA to be considered reliable.
func (r *receiver) isEWMAWarm() bool {
    // Threshold of 0 means warm-up check is disabled
    if r.ewmaWarmupThreshold == 0 {
        return true
    }
    return r.interPacketSampleCount.Load() >= r.ewmaWarmupThreshold
}
```

#### 5.3.5 Configuration Guidance

| Stream Type | Recommended Threshold | Warm-up Time (approx) | Rationale |
|-------------|----------------------|------------------------|-----------|
| **High-rate (>10Mbps)** | 16-24 | ~16-24ms | Fast warm-up acceptable, many samples quickly |
| **Medium-rate (1-10Mbps)** | 32 (default) | ~32-100ms | Balanced accuracy vs warm-up time |
| **Low-rate (<1Mbps)** | 48-64 | ~200-640ms | Need more samples for stable estimate |
| **Testing/Debugging** | 0 | Immediate | Disable warm-up for testing |

### 5.4 Operational Guidance

| Scenario | Recommended nakExpiryMargin | Recommended EWMAWarmupThreshold | Rationale |
|----------|-------------------|-------------------------------|-----------|
| **Default / Unknown** | 0.10 (10%) | 32 | Balanced defaults |
| **Stable low-latency network** | 0.0 - 0.05 | 32 | Can be more aggressive on margin |
| **High-rate (>10Mbps)** | 0.10 | 16-24 | Fast warm-up, many samples |
| **Low-rate (<1Mbps)** | 0.10 - 0.25 | 48-64 | Need more samples, more conservative |
| **High-jitter (Starlink)** | 0.25 - 0.50 | 32 | More margin for RTT variance |
| **Debugging phantom NAKs** | 0.0 | 32 | Temporary - observe nak_not_found |
| **Debugging lost recovery** | 0.50 - 1.0 | 32 | Temporary - observe packet loss |
| **Testing** | 0.10 | 0 | Disable warm-up for predictable tests |

### 5.5 Metrics for Tuning

| Metric | High Value Indicates | Tuning Action |
|--------|---------------------|---------------|
| `NakBtreeExpiredEarly` | Normal operation | Expected |
| `NakBtreeSkippedExpired` | Large outages | Expected during outages |
| `nak_not_found` (sender) | Phantom NAKs | Consider decreasing nakExpiryMargin |
| `CongestionRecvPktLoss` | Actual packet loss | Consider increasing nakExpiryMargin |
| `NakTsbpdEstColdFallback` | Warm-up fallbacks | Normal at startup; persistent = increase EWMAWarmupThreshold |
| `NakTsbpdEstimatorWarm` | 0=cold, 1=warm | Gauge - should be 1 after initial packets |

### 5.6 Comparison with ExtraRTTMargin

Both margin configs use the same percentage-based formula pattern for consistency:

| Config | Formula | Default | Purpose |
|--------|---------|---------|---------|
| `ExtraRTTMargin` | `RTO = (RTT + RTTVar) * (1 + extraRTTMargin)` | 0.10 | NAK/retransmit suppression |
| `NakExpiryMargin` | `threshold = now + (RTO * (1 + nakExpiryMargin))` | 0.10 | NAK btree entry expiry |
| `EWMAWarmupThreshold` | `sampleCount >= threshold` | 32 | Inter-packet EWMA reliability |

Both margin configs default to 10% extra margin for consistency across the codebase.

---

## 6. Implementation Plan

### 6.1 Files to Modify

> **Note:** Section numbers updated. Configuration is now Section 5.

| File | Lines | Changes |
|------|-------|---------|
| `config.go` | ~30 | Add `NakExpiryMargin` type and constants (Section 5.2.1) |
| `config.go` | ~358 | Add `NakExpiryMargin` to Config struct (Section 5.2.2) |
| `config.go` | ~625 | Add default value `NakExpiryMargin: 1.0` (Section 5.2.3) |
| `contrib/common/flags.go` | ~140 | Add `-nakexpirymargin` CLI flag (Section 5.2.4) |
| `congestion/live/receive/nak_btree.go` | 15-19 | Add `TsbpdTimeUs` to `NakEntryWithTime` |
| `congestion/live/receive/nak_btree.go` | NEW | Add `InsertWithTsbpd()` / `InsertWithTsbpdLocking()` |
| `congestion/live/receive/nak_btree.go` | NEW | Add `InsertBatchWithTsbpd()` / `InsertBatchWithTsbpdLocking()` |
| `congestion/live/receive/nak_btree.go` | NEW | Add `DeleteBeforeTsbpd()` / `DeleteBeforeTsbpdLocking()` / `DeleteBeforeTsbpdSlow()` |
| `congestion/live/receive/nak.go` | NEW | Add `estimateTsbpdForSeq()` - linear interpolation between boundary packets |
| `congestion/live/receive/nak.go` | NEW | Add `estimateTsbpdFallback()` - inter-packet EWMA for edge cases |
| `congestion/live/receive/nak.go` | NEW | Add `calculateExpiryThreshold()` with margin (Section 5.2.5) |
| `congestion/live/receive/nak.go` | gapScan | Use linear interpolation + pre-filter expired entries (Section 4.5.4.1) |
| `congestion/live/receive/nak.go` | 509-544 | Modify `expireNakEntries()` to use time-based expiry with margin |
| `congestion/live/receive/receiver.go` | struct | Add `avgInterPacketIntervalUs`, `lastPacketArrivalUs`, `nakExpiryMargin` |
| `congestion/live/receive/push.go` | pushLocked | Track inter-packet interval EWMA (fallback) |
| `metrics/metrics.go` | ~700 | Add `NakBtreeExpiredEarly`, `NakBtreeSkippedExpired` counters |
| `metrics/handler.go` | ~1100 | Export new metrics |

**Key design decisions:**

1. **Option A: Linear Interpolation** (Section 4.5.6) for accurate per-sequence TSBPD estimation
2. **Option D: Inter-Packet EWMA** as fallback when only one boundary packet available
3. **Pre-filter optimization** (Section 4.5.4.1): Skip inserting already-expired entries for large outages
4. **Configurable margin** (Section 5): `NakExpiryMargin` allows tuning aggressiveness
5. **New metrics**: `NakBtreeSkippedExpired`, `NakBtreeExpiredEarly` for visibility

### 6.2 Step-by-Step Implementation

#### Step 6.2.1: Update NakEntryWithTime Struct

**File:** `congestion/live/receive/nak_btree.go:15-19`

```go
// NakEntryWithTime stores a missing sequence number with timing information.
// Used in NAK btree to track:
// - When the packet should be delivered (TsbpdTimeUs) - for RTT-aware expiry
// - When we last sent NAK (LastNakedAtUs) - for NAK suppression
// - How many times NAK'd (NakCount) - for metrics
type NakEntryWithTime struct {
    Seq           uint32 // Missing sequence number
    TsbpdTimeUs   uint64 // TSBPD release time for this sequence (microseconds)
    LastNakedAtUs uint64 // When we last sent NAK for this seq (microseconds)
    NakCount      uint32 // Number of times NAK'd
}
```

#### Step 6.2.2: Update Insert Methods

**File:** `congestion/live/receive/nak_btree.go`

```go
// InsertWithTsbpd adds a missing sequence number with its TSBPD time.
// This is the lock-free version for use in single-threaded contexts (event loop).
func (nb *nakBtree) InsertWithTsbpd(seq uint32, tsbpdTimeUs uint64) {
    entry := NakEntryWithTime{
        Seq:           seq,
        TsbpdTimeUs:   tsbpdTimeUs,
        LastNakedAtUs: 0,
        NakCount:      0,
    }
    nb.tree.ReplaceOrInsert(entry)
}

// InsertWithTsbpdLocking adds a missing sequence with TSBPD time, with lock protection.
func (nb *nakBtree) InsertWithTsbpdLocking(seq uint32, tsbpdTimeUs uint64) {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    nb.InsertWithTsbpd(seq, tsbpdTimeUs)
}

// InsertBatchWithTsbpd adds multiple missing sequences with their TSBPD times.
// seqsWithTsbpd is a slice of (seq, tsbpdTimeUs) pairs.
// Returns the count of newly inserted sequences.
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use InsertBatchWithTsbpdLocking().
func (nb *nakBtree) InsertBatchWithTsbpd(seqs []uint32, tsbpdTimeUs []uint64) int {
    if len(seqs) == 0 || len(seqs) != len(tsbpdTimeUs) {
        return 0
    }

    count := 0
    for i, seq := range seqs {
        entry := NakEntryWithTime{
            Seq:           seq,
            TsbpdTimeUs:   tsbpdTimeUs[i],
            LastNakedAtUs: 0,
            NakCount:      0,
        }
        if _, replaced := nb.tree.ReplaceOrInsert(entry); !replaced {
            count++
        }
    }
    return count
}

// InsertBatchWithTsbpdLocking adds multiple missing sequences with TSBPD times, with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) InsertBatchWithTsbpdLocking(seqs []uint32, tsbpdTimeUs []uint64) int {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    return nb.InsertBatchWithTsbpd(seqs, tsbpdTimeUs)
}
```

#### Step 6.2.3: Add Time-Based Expiry Method (Optimized)

**File:** `congestion/live/receive/nak_btree.go`

The optimized implementation uses `DeleteMin()` instead of collecting items in a slice.
This follows the same pattern as `packet_store_btree.go:RemoveAll()`.

**Performance comparison:**
- **DeleteBeforeTsbpdSlow** (collect + delete): O(n) allocation + O(n * log N) deletes
- **DeleteBeforeTsbpd** (DeleteMin loop): O(n * log N) deletes, **zero allocation**

**Key invariant:** TSBPD times are monotonically increasing with sequence numbers because:
1. Sequences are ordered by `circular.SeqLess` (ascending)
2. TSBPD = tsbpdTimeBase + timestamp + tsbpdDelay
3. Timestamps increase monotonically with sequence at the sender
4. Therefore: `seq1 < seq2` implies `tsbpd1 <= tsbpd2`

This invariant allows us to stop at the first non-expired entry.

```go
// DeleteBeforeTsbpd removes all entries whose TsbpdTimeUs is before the threshold.
// Uses DeleteMin() for O(log n) per delete (no lookup needed, zero allocation).
// This is the optimized implementation - see DeleteBeforeTsbpdSlow for comparison.
//
// An entry is expired if: entry.TsbpdTimeUs < expiryThresholdUs
// This means retransmit can't arrive before TSBPD time.
//
// Key invariant: TSBPD times are monotonically increasing with sequence numbers.
// This allows us to stop at the first non-expired entry (sorted order).
//
// Performance: For n entries to expire from btree of size N:
//   - DeleteBeforeTsbpd (optimized):  O(n * log N) - DeleteMin is O(log N), zero allocs
//   - DeleteBeforeTsbpdSlow:          O(n) alloc + O(n * log N) collect + O(n * log N) delete
//
// This is the lock-free version for use in single-threaded contexts (event loop).
// For concurrent access, use DeleteBeforeTsbpdLocking().
func (nb *nakBtree) DeleteBeforeTsbpd(expiryThresholdUs uint64) int {
    deleted := 0

    for {
        // Get the minimum element (oldest sequence = earliest TSBPD)
        minItem, found := nb.tree.Min()
        if !found {
            break // Tree is empty
        }

        // Check if it should be expired (TSBPD < threshold)
        // Due to TSBPD monotonicity invariant, once we find a non-expired entry,
        // all subsequent entries (higher sequences) are also non-expired.
        if minItem.TsbpdTimeUs >= expiryThresholdUs {
            break // Stop at first non-expired
        }

        // Delete the minimum (O(log n), no lookup needed)
        nb.tree.DeleteMin()
        deleted++
    }

    return deleted
}

// DeleteBeforeTsbpdLocking removes entries before threshold with lock protection.
// Use this version when called from tick() paths or other concurrent contexts.
func (nb *nakBtree) DeleteBeforeTsbpdLocking(expiryThresholdUs uint64) int {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    return nb.DeleteBeforeTsbpd(expiryThresholdUs)
}

// DeleteBeforeTsbpdSlow is the unoptimized implementation for benchmarking comparison.
// It collects items to remove in a slice, then deletes them in a second pass.
// This requires allocation of the toDelete slice and two traversals.
func (nb *nakBtree) DeleteBeforeTsbpdSlow(expiryThresholdUs uint64) int {
    var toDelete []NakEntryWithTime

    // First pass: collect items to delete
    nb.tree.Ascend(func(entry NakEntryWithTime) bool {
        if entry.TsbpdTimeUs < expiryThresholdUs {
            toDelete = append(toDelete, entry)
            return true
        }
        return false // Stop at first non-expired
    })

    // Second pass: delete collected items
    for _, entry := range toDelete {
        nb.tree.Delete(entry)
    }
    return len(toDelete)
}
```

#### Step 6.2.4: Modify expireNakEntries() for Time-Based Expiry

**File:** `congestion/live/receive/nak.go:509-544`

```go
// expireNakEntries removes entries from the NAK btree that are too old to be useful.
// Uses time-based expiry (FR-19 optimization):
//   - An entry is expired if: now + RTT > entry.TsbpdTimeUs
//   - This means even if we send NAK now, retransmit can't arrive before TSBPD
//
// The expiry threshold is: nowUs + rtoUs
// where rtoUs = RTT + RTTVar (accounts for network variance)
func (r *receiver) expireNakEntries() int {
    if r.nakBtree == nil {
        if r.metrics != nil {
            r.metrics.NakBtreeNilWhenEnabled.Add(1)
        }
        return 0
    }

    // Get current time
    nowUs := r.nowFn()

    // Calculate expiry threshold: now + RTT
    // Use RTOUs (RTT + RTTVar) for safety margin
    expiryThresholdUs := nowUs
    rtoApplied := false

    if r.rtt != nil {
        rtoUs := r.rtt.RTOUs()
        if rtoUs > 0 {
            expiryThresholdUs = nowUs + rtoUs
            rtoApplied = true
        }
    }

    // Fall back to sequence-based expiry if RTT not available
    // This maintains backward compatibility with Phase 1 behavior
    if !rtoApplied {
        // Use DeleteBefore with oldest packet's sequence
        r.lock.RLock()
        minPkt := r.packetStore.Min()
        r.lock.RUnlock()

        if minPkt == nil {
            return 0
        }

        cutoff := minPkt.Header().PacketSequenceNumber.Val()
        expired := r.nakDeleteBefore(cutoff)
        if expired > 0 && r.metrics != nil {
            r.metrics.NakBtreeExpired.Add(uint64(expired))
        }
        return expired
    }

    // Time-based expiry: delete entries where TSBPD < expiryThreshold
    expired := r.nakDeleteBeforeTsbpd(expiryThresholdUs)

    if expired > 0 && r.metrics != nil {
        r.metrics.NakBtreeExpiredEarly.Add(uint64(expired))
    }

    return expired
}
```

#### Step 6.2.6: Add Metrics

**File:** `metrics/metrics.go`

```go
// In ConnectionMetrics struct:

// NAK btree expiry metrics (Section 4.5)
NakBtreeExpired        atomic.Uint64 // Entries expired at TSBPD (sequence-based fallback)
NakBtreeExpiredEarly   atomic.Uint64 // Entries expired RTT before TSBPD (time-based)
NakBtreeSkippedExpired atomic.Uint64 // Entries not inserted - already expired at gap detection (Section 4.5.4.1)
```

**File:** `metrics/handler.go`

```go
// NAK btree expiry metrics
writeCounterIfNonZero(b, "gosrt_nak_btree_expired_early_total",
    metrics.NakBtreeExpiredEarly.Load(),
    "socket_id", socketIdStr, "instance", instanceName)

writeCounterIfNonZero(b, "gosrt_nak_btree_skipped_expired_total",
    metrics.NakBtreeSkippedExpired.Load(),
    "socket_id", socketIdStr, "instance", instanceName)
```

**Metric Semantics:**

| Metric | When Incremented | What It Tells You |
|--------|------------------|-------------------|
| `NakBtreeExpired` | Entry expired by sequence-based fallback | Fallback path active (RTT unavailable) |
| `NakBtreeExpiredEarly` | Entry expired by time-based (TSBPD < now+RTT) | Normal RTT-aware expiry working |
| `NakBtreeSkippedExpired` | Entry not inserted (TSBPD already expired) | Large outage detected, pre-filtered |

**Operational Insight:**
- High `NakBtreeSkippedExpired` → Extended outages occurring, packets unrecoverable
- `NakBtreeExpiredEarly` should dominate in normal operation
- `NakBtreeExpired` should be near zero when RTT tracking is working

**File:** `metrics/handler_test.go`

Add tests for the new metrics.

**File:** `Makefile`
Run `make audit-metrics` to verify metrics are added correctly.

#### Step 6.2.7: Add Linear Interpolation Function

**File:** `congestion/live/receive/nak.go`

```go
// estimateTsbpdForSeq calculates TSBPD for a missing sequence using linear interpolation.
// This is critical for accurate expiry timing, especially during large gap scenarios
// (e.g., Starlink 60ms outages causing 100+ packet gaps).
//
// See design doc Section 4.5.6 for analysis of why linear interpolation is preferred
// over simpler approaches (using single boundary packet's TSBPD).
//
// Parameters:
//   - missingSeq: The missing sequence number needing TSBPD estimation
//   - lowerSeq: Sequence of packet before the gap (has known TSBPD)
//   - lowerTsbpd: PktTsbpdTime of the lower boundary packet
//   - upperSeq: Sequence of packet after the gap (has known TSBPD)
//   - upperTsbpd: PktTsbpdTime of the upper boundary packet
//
// Returns: Estimated TSBPD for missingSeq (microseconds)
func estimateTsbpdForSeq(missingSeq, lowerSeq uint32, lowerTsbpd uint64, upperSeq uint32, upperTsbpd uint64) uint64 {
    // Handle edge cases
    if upperSeq == lowerSeq || upperTsbpd <= lowerTsbpd {
        return lowerTsbpd
    }

    // Linear interpolation using circular sequence arithmetic:
    // TSBPD_missing = lowerTsbpd + (missingSeq - lowerSeq) * (upperTsbpd - lowerTsbpd) / (upperSeq - lowerSeq)
    seqRange := uint64(circular.SeqSub(upperSeq, lowerSeq))
    tsbpdRange := upperTsbpd - lowerTsbpd
    seqOffset := uint64(circular.SeqSub(missingSeq, lowerSeq))

    // Avoid division by zero (should not happen given edge case check above)
    if seqRange == 0 {
        return lowerTsbpd
    }

    return lowerTsbpd + (seqOffset * tsbpdRange / seqRange)
}
```

#### Step 6.2.8: Update Gap Scan to Use Linear Interpolation

**File:** `congestion/live/receive/nak.go` (in `gapScan()`)

During gap scanning, we have access to both boundary packets:

```go
// In gapScan(), when iterating through packet btree:
var prevPkt packet.Packet
var prevSeq uint32
var prevTsbpd uint64

r.packetStore.IterateFrom(startSeq, func(pkt packet.Packet) bool {
    currentSeq := pkt.Header().PacketSequenceNumber.Val()
    currentTsbpd := pkt.Header().PktTsbpdTime

    if prevPkt != nil {
        expectedSeq := circular.SeqAdd(prevSeq, 1)
        if currentSeq != expectedSeq {
            // Gap detected between prevSeq and currentSeq
            // Insert all missing sequences with interpolated TSBPD
            for seq := expectedSeq; circular.SeqLess(seq, currentSeq); seq = circular.SeqAdd(seq, 1) {
                tsbpd := estimateTsbpdForSeq(seq, prevSeq, prevTsbpd, currentSeq, currentTsbpd)
                gaps = append(gaps, seq)
                gapTsbpds = append(gapTsbpds, tsbpd)
            }
        }
    }

    prevPkt = pkt
    prevSeq = currentSeq
    prevTsbpd = currentTsbpd
    return true // continue iteration
})

// Batch insert with TSBPD times
r.nakInsertBatchWithTsbpd(gaps, gapTsbpds)
```

#### Step 6.2.9: Integrate Inter-Packet Interval Tracking with Existing Rate Code

The receiver already tracks arrival time for FastNAK. We can leverage this existing
infrastructure to also track inter-packet interval.

##### Existing Code Analysis

**File:** `congestion/live/receive/receiver.go:100-103`

```go
// FastNAK tracking (atomic for lock-free access)
lastPacketArrivalTime AtomicTime    // Time of last packet arrival  ← ALREADY EXISTS
lastNakTime           AtomicTime    // Time of last NAK sent
lastDataPacketSeq     atomic.Uint32 // Last data packet sequence (for FastNAKRecent)
```

**File:** `congestion/live/receive/receiver.go:74-77`

```go
// Running averages (atomic uint64 with Float64bits/Float64frombits)
// Using atomic operations for lock-free access per gosrt_lockless_design.md Section 8.3
avgPayloadSizeBits  atomic.Uint64 // float64 via math.Float64bits/Float64frombits  ← PATTERN TO FOLLOW
avgLinkCapacityBits atomic.Uint64 // float64 via math.Float64bits/Float64frombits
```

**File:** `congestion/live/receive/push.go:136-139` (existing tracking)

```go
// Update FastNAK tracking (after packet is accepted)
r.lastPacketArrivalTime.Store(now)  // ← We can calculate interval from this!
r.lastDataPacketSeq.Store(seq)
```

##### Proposed Changes

**File:** `congestion/live/receive/receiver.go` (add to struct, around line 78)

```go
// Running averages (atomic uint64 with Float64bits/Float64frombits)
// Using atomic operations for lock-free access per gosrt_lockless_design.md Section 8.3
avgPayloadSizeBits       atomic.Uint64 // float64 via math.Float64bits/Float64frombits
avgLinkCapacityBits      atomic.Uint64 // float64 via math.Float64bits/Float64frombits
avgInterPacketIntervalUs atomic.Uint64 // EWMA inter-packet arrival interval (microseconds) ← NEW

// ... existing FastNAK fields ...
lastPacketArrivalTime AtomicTime    // Time of last packet arrival (unchanged)
lastPacketArrivalUs   atomic.Uint64 // Last arrival in microseconds for interval calc ← NEW
```

**File:** `congestion/live/receive/push.go:136-139` (update existing tracking)

```go
// Update FastNAK tracking and inter-packet interval (after packet is accepted)
// Both use the same arrival timestamp for consistency
r.lastPacketArrivalTime.Store(now)  // For FastNAK (time.Time)
r.lastDataPacketSeq.Store(seq)

// Calculate inter-packet interval for TSBPD estimation fallback
// Uses same EWMA formula as avgPayloadSize (0.875/0.125 smoothing)
nowUs := uint64(now.UnixMicro())
lastArrivalUs := r.lastPacketArrivalUs.Swap(nowUs)
if lastArrivalUs > 0 && nowUs > lastArrivalUs {
    intervalUs := nowUs - lastArrivalUs
    // Clamp to reasonable range (10µs to 100ms) to avoid outliers from pauses
    if intervalUs >= 10 && intervalUs <= 100_000 {
        oldInterval := r.avgInterPacketIntervalUs.Load()
        if oldInterval == 0 {
            r.avgInterPacketIntervalUs.Store(intervalUs)
        } else {
            // EWMA: same formula as avgPayloadSize in push.go:100-102
            newInterval := uint64(float64(oldInterval)*0.875 + float64(intervalUs)*0.125)
            r.avgInterPacketIntervalUs.Store(newInterval)
        }
    }
}
```

##### Comparison: Existing Rate EWMA Patterns

| Field | Formula | Where Calculated | Purpose |
|-------|---------|------------------|---------|
| `avgPayloadSizeBits` | `0.875*old + 0.125*pktLen` | `push.go:100-102` | Rate calculation (bytes/pkt) |
| `avgLinkCapacityBits` | `0.875*old + 0.125*(1e6/diff)` | `push.go:171-173` | Link capacity (probe pairs) |
| `avgInterPacketIntervalUs` | `0.875*old + 0.125*interval` | `push.go` (proposed) | TSBPD estimation fallback |

All use the same EWMA smoothing factor (α=0.125) for consistency.

##### Memory Impact

| Addition | Size | Notes |
|----------|------|-------|
| `avgInterPacketIntervalUs` | +8 bytes | Atomic uint64 |
| `lastPacketArrivalUs` | +8 bytes | Atomic uint64 for interval calculation |
| **Total per receiver** | **+16 bytes** | Negligible |

##### Fallback Estimation Function

```go
// estimateTsbpdFallback uses inter-packet interval when linear interpolation not possible.
// This handles edge cases where we don't have both boundary packets:
//   - Gap at start of packet buffer (no lower boundary)
//   - Single packet in buffer
//
// Returns estimated TSBPD for missingSeq based on reference packet.
func (r *receiver) estimateTsbpdFallback(missingSeq uint32, refSeq uint32, refTsbpd uint64) uint64 {
    intervalUs := r.avgInterPacketIntervalUs.Load()
    if intervalUs == 0 {
        // Default: 1ms per packet (~1000 pkt/s, conservative for 5Mbps+)
        intervalUs = 1000
    }

    // Calculate signed sequence difference for forward/backward estimation
    seqDiff := int64(circular.SeqSub(missingSeq, refSeq))

    // Estimate TSBPD: ref + (seqDiff * interval)
    // Negative seqDiff means missing is before ref (earlier TSBPD)
    // Positive seqDiff means missing is after ref (later TSBPD)
    return uint64(int64(refTsbpd) + seqDiff*int64(intervalUs))
}
```

##### Expected Values at Different Bitrates

| Bitrate | Payload | Packets/sec | Inter-Packet Interval |
|---------|---------|-------------|----------------------|
| 5 Mbps | 1316 bytes | ~475 pkt/s | ~2100 µs |
| 10 Mbps | 1316 bytes | ~950 pkt/s | ~1050 µs |
| 20 Mbps | 1316 bytes | ~1900 pkt/s | ~525 µs |
| 50 Mbps | 1316 bytes | ~4750 pkt/s | ~210 µs |

---

## 7. Corner Cases and Failure Modes

### 7.1 What Can Go Wrong

The primary risk of this optimization is **expiring NAK entries too early**, causing:
1. Lost recovery opportunities for packets that could have been repaired
2. Increased actual packet loss (CongestionRecvPktLoss)
3. Application-level data corruption or quality degradation

**Philosophy:** We favor potential repair over phantom NAK reduction.
A few extra useless NAKs are acceptable; missing a recovery window is not.

### 7.2 Corner Case Matrix

| # | Corner Case | Risk Level | Mitigation | Test Coverage |
|---|-------------|------------|------------|---------------|
| 1 | **RTT spike during gap** | HIGH | Use nakExpiryMargin | `TestExpiryRttSpike` |
| 2 | **RTT not yet measured** | MEDIUM | Fall back to sequence-based expiry | `TestExpiryNoRtt` |
| 3 | **Zero RTT from sender** | LOW | Check for zero, use fallback | `TestExpiryZeroRtt` |
| 4 | **Clock skew** | LOW | Use monotonic clock | `TestExpiryClockSkew` |
| 5 | **Sequence wraparound** | MEDIUM | Use circular.SeqLess | `TestExpirySeqWrap` |
| 6 | **TSBPD estimation error** | HIGH | Linear interpolation + nakExpiryMargin | `TestExpiryTsbpdError` |
| 7 | **Large gap (Starlink)** | HIGH | Pre-filter + accurate estimation | `TestExpiryLargeGap` |
| 8 | **Extended outage (>1s)** | LOW | Pre-filter, SRT timeout handles | `TestExpiryExtendedOutage` |
| 9 | **Rapid ACK after NAK** | LOW | DeleteMin handles | `TestExpiryRapidAck` |
| 10 | **Empty NAK btree** | LOW | Check for empty | `TestExpiryEmptyBtree` |
| 11 | **Single entry in btree** | LOW | Min() handles | `TestExpirySingleEntry` |
| 12 | **Concurrent modification** | MEDIUM | Use locking versions | `TestExpiryConcurrent` |

### 7.3 High-Risk Scenarios Analysis

#### 7.3.1 RTT Spike During Gap (Corner Case #1)

```
Timeline:
T=0ms:   Gap detected, RTT=10ms, RTO=15ms
         NAK entry created with TSBPD=T₀+120ms
         Expiry threshold = now + RTO*(1+nakExpiryMargin) = 0 + 15*1.1 = 16.5ms
T=5ms:   Network congestion starts, RTT spikes to 50ms
T=10ms:  expireNakEntries() called, threshold = 10 + 15*2 = 40ms
         Entry TSBPD=120ms > 40ms → NOT expired (correct!)
T=30ms:  If we used old threshold without nakExpiryMargin:
         threshold = 30 + 15 = 45ms (would be close to expiring)
         With nakExpiryMargin=0.10: threshold = 30 + 16.5ms = 46.5ms (safely kept)
```

**Mitigation:** The `NakExpiryMargin` provides buffer for RTT variance.

#### 7.3.2 TSBPD Estimation Error (Corner Case #6)

```
Gap: seq 1000-1099 (100 packets)
Linear interpolation between seq=999 (TSBPD=T₀) and seq=1100 (TSBPD=T₁)

Worst case error: packets sent at variable rate during gap
  - Estimated TSBPD for seq 1050: T₀ + 50*(T₁-T₀)/100
  - Actual TSBPD could be ±10% different due to rate variation

Mitigation:
  1. Use linear interpolation (bounds error to gap duration)
  2. Apply nakExpiryMargin (absorbs estimation error)
  3. For Starlink bursts, error is bounded by burst duration (~60ms)
```

### 7.4 Table-Driven Test Design

**File:** `congestion/live/receive/nak_expiry_test.go`

```go
func TestDeleteBeforeTsbpd_CornerCases(t *testing.T) {
    tests := []struct {
        name              string
        entries           []NakEntryWithTime
        nowUs             uint64
        rtoUs             uint64
        nakExpiryMargin   float64  // Config: NakExpiryMargin (not to be confused with ExtraRTTMargin)
        wantExpired       int
        wantRemaining     []uint32
        wantMetric        string // Which metric should increment
    }{
        // === Basic Cases ===
        {
            name: "expire_none_all_future",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 10_000_000}, // TSBPD in 10s
                {Seq: 101, TsbpdTimeUs: 10_001_000},
            },
            nowUs:         1_000_000, // now = 1s
            rtoUs:         15_000,    // RTO = 15ms
            nakExpiryMargin:        0.10,      // threshold = 1s + 15ms*1.1 = 1.0165s
            wantExpired:   0,
            wantRemaining: []uint32{100, 101},
        },
        {
            name: "expire_all_past",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 100_000}, // TSBPD = 100ms
                {Seq: 101, TsbpdTimeUs: 200_000}, // TSBPD = 200ms
            },
            nowUs:         1_000_000, // now = 1s
            rtoUs:         15_000,    // threshold = 1s + 16.5ms = 1.0165s
            nakExpiryMargin:        0.10,
            wantExpired:   2,
            wantRemaining: []uint32{},
        },
        {
            name: "expire_partial",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 500_000},   // TSBPD = 500ms → EXPIRED
                {Seq: 101, TsbpdTimeUs: 1_050_000}, // TSBPD = 1.05s → KEPT
                {Seq: 102, TsbpdTimeUs: 2_000_000}, // TSBPD = 2s → KEPT
            },
            nowUs:         1_000_000, // now = 1s
            rtoUs:         15_000,    // threshold = 1s + 16.5ms = 1.0165s
            nakExpiryMargin:        0.10,
            wantExpired:   1,
            wantRemaining: []uint32{101, 102},
        },

        // === Corner Case #1: RTT Spike ===
        {
            name: "rtt_spike_margin_protects",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_017_000}, // TSBPD = 1.017s
            },
            nowUs:         1_000_000, // now = 1s
            rtoUs:         15_000,    // Original RTO
            nakExpiryMargin:        0.10,      // threshold = 1s + 16.5ms = 1.0165s
            wantExpired:   0,         // nakExpiryMargin saved us! (1.017s > 1.0165s)
            wantRemaining: []uint32{100},
        },
        {
            name: "rtt_spike_no_margin_would_expire",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_016_000}, // TSBPD = 1.016s
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin: 0.0,       // nakExpiryMargin=0: threshold = 1s + 15ms = 1.015s
            wantExpired:   0,         // Still kept (1.016s > 1.015s)
            wantRemaining: []uint32{100},
        },

        // === Corner Case #2: No RTT Yet ===
        {
            name: "no_rtt_fallback_to_sequence",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 500_000},
            },
            nowUs:         1_000_000,
            rtoUs:         0,          // RTT not measured yet!
            nakExpiryMargin:        0.10,
            wantExpired:   0,          // Should NOT expire with time-based
            wantRemaining: []uint32{100},
            wantMetric:    "NakBtreeExpired", // Falls back to sequence-based
        },

        // === Corner Case #5: Sequence Wraparound ===
        {
            name: "sequence_wraparound",
            entries: []NakEntryWithTime{
                {Seq: 0xFFFFFFFE, TsbpdTimeUs: 500_000},  // Near max
                {Seq: 0xFFFFFFFF, TsbpdTimeUs: 500_500},  // Max
                {Seq: 0,          TsbpdTimeUs: 501_000},  // Wrapped to 0
                {Seq: 1,          TsbpdTimeUs: 501_500},  // After wrap
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.10,      // threshold = 1s + 16.5ms = 1.0165s
            wantExpired:   4,         // All expired (TSBPD < 1.0165s)
            wantRemaining: []uint32{},
        },

        // === Corner Case #7: Large Gap (Starlink) ===
        {
            name: "starlink_100_packet_gap",
            entries: func() []NakEntryWithTime {
                // Simulate 100-packet gap from 60ms outage
                // At 20Mbps: ~600µs per packet
                entries := make([]NakEntryWithTime, 100)
                baseTsbpd := uint64(1_000_000) // 1s
                for i := 0; i < 100; i++ {
                    entries[i] = NakEntryWithTime{
                        Seq:         uint32(1000 + i),
                        TsbpdTimeUs: baseTsbpd + uint64(i*600), // ~600µs apart
                    }
                }
                // TSBPD range: 1.0s (seq 1000) to 1.0594s (seq 1099)
                return entries
            }(),
            nowUs:         1_050_000, // 1.05s
            rtoUs:         15_000,
            nakExpiryMargin:        0.10,      // threshold = 1.05s + 16.5ms = 1.0665s
            wantExpired:   100,       // All entries (max TSBPD 1.0594s < 1.0665s)
            wantRemaining: []uint32{},
        },

        // === Corner Case #10: Empty Btree ===
        {
            name: "empty_btree",
            entries:       []NakEntryWithTime{},
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.10,
            wantExpired:   0,
            wantRemaining: []uint32{},
        },

        // === Corner Case #11: Single Entry ===
        {
            name: "single_entry_kept",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 2_000_000}, // Far future
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.10,
            wantExpired:   0,
            wantRemaining: []uint32{100},
        },
        {
            name: "single_entry_expired",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 500_000}, // Past
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.10,
            wantExpired:   1,
            wantRemaining: []uint32{},
        },

        // === NakExpiryMargin Sensitivity Tests ===
        {
            name: "margin_0_aggressive",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_016_000}, // TSBPD = 1.016s
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.0,       // threshold = 1s + 15ms*1.0 = 1.015s
            wantExpired:   0,         // 1.016s > 1.015s → kept
            wantRemaining: []uint32{100},
        },
        {
            name: "margin_5pct",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_016_000}, // TSBPD = 1.016s
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.05,      // threshold = 1s + 15ms*1.05 = 1.01575s
            wantExpired:   0,         // 1.016s > 1.01575s → kept
            wantRemaining: []uint32{100},
        },
        {
            name: "margin_10pct_default",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_016_000}, // TSBPD = 1.016s
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.10,      // threshold = 1s + 15ms*1.1 = 1.0165s
            wantExpired:   0,         // 1.016s < 1.0165s → EXPIRED with 10% nakExpiryMargin
            wantRemaining: []uint32{},
        },
        {
            name: "margin_50pct_conservative",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_020_000}, // TSBPD = 1.02s
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        0.50,      // threshold = 1s + 15ms*1.5 = 1.0225s
            wantExpired:   0,         // 1.02s < 1.0225s → EXPIRED even with 50% nakExpiryMargin
            wantRemaining: []uint32{},
        },
        {
            name: "margin_100pct_very_conservative",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1_025_000}, // TSBPD = 1.025s
            },
            nowUs:         1_000_000,
            rtoUs:         15_000,
            nakExpiryMargin:        1.0,       // threshold = 1s + 15ms*2.0 = 1.030s
            wantExpired:   0,         // 1.025s < 1.030s → EXPIRED
            wantRemaining: []uint32{},
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            // Setup
            nb := newNakBtree(32)
            for _, entry := range tc.entries {
                nb.InsertWithTsbpd(entry.Seq, entry.TsbpdTimeUs)
            }

            // Calculate expiry threshold: now + (RTO * (1 + nakExpiryMargin))
            expiryThreshold := tc.nowUs + uint64(float64(tc.rtoUs)*(1.0+tc.nakExpiryMargin))

            // Execute
            var expired int
            if tc.rtoUs == 0 {
                // Fallback path - would use sequence-based expiry
                // For this test, just verify we don't expire
                expired = 0
            } else {
                expired = nb.DeleteBeforeTsbpd(expiryThreshold)
            }

            // Verify expired count
            require.Equal(t, tc.wantExpired, expired, "expired count mismatch")

            // Verify remaining entries
            var remaining []uint32
            nb.tree.Ascend(func(entry NakEntryWithTime) bool {
                remaining = append(remaining, entry.Seq)
                return true
            })
            require.Equal(t, tc.wantRemaining, remaining, "remaining entries mismatch")
        })
    }
}
```

### 7.5 TSBPD Estimation Tests

**File:** `congestion/live/receive/nak_tsbpd_estimation_test.go`

```go
func TestEstimateTsbpdForSeq_CornerCases(t *testing.T) {
    tests := []struct {
        name        string
        missingSeq  uint32
        lowerSeq    uint32
        lowerTsbpd  uint64
        upperSeq    uint32
        upperTsbpd  uint64
        wantTsbpd   uint64
        wantError   bool
    }{
        // Basic interpolation
        {
            name:       "midpoint",
            missingSeq: 105,
            lowerSeq:   100, lowerTsbpd: 1_000_000, // 1s
            upperSeq:   110, upperTsbpd: 1_010_000, // 1.01s
            wantTsbpd:  1_005_000,                  // Exactly midpoint
        },
        {
            name:       "quarter_point",
            missingSeq: 102,
            lowerSeq:   100, lowerTsbpd: 1_000_000,
            upperSeq:   108, upperTsbpd: 1_008_000,
            wantTsbpd:  1_002_000, // 2/8 = 0.25
        },

        // Edge cases
        {
            name:       "same_as_lower",
            missingSeq: 100,
            lowerSeq:   100, lowerTsbpd: 1_000_000,
            upperSeq:   110, upperTsbpd: 1_010_000,
            wantTsbpd:  1_000_000, // Same as lower
        },
        {
            name:       "one_before_upper",
            missingSeq: 109,
            lowerSeq:   100, lowerTsbpd: 1_000_000,
            upperSeq:   110, upperTsbpd: 1_010_000,
            wantTsbpd:  1_009_000, // 9/10 of range
        },

        // Sequence wraparound
        {
            name:       "wraparound_gap",
            missingSeq: 0,          // After wraparound
            lowerSeq:   0xFFFFFFFE, lowerTsbpd: 1_000_000,
            upperSeq:   2,          upperTsbpd: 1_004_000, // 4-seq gap
            wantTsbpd:  1_002_000,  // 2/4 through gap
        },

        // Large gap (Starlink scenario)
        {
            name:       "large_gap_100_packets",
            missingSeq: 1050,       // Middle of gap
            lowerSeq:   1000, lowerTsbpd: 1_000_000,
            upperSeq:   1100, upperTsbpd: 1_060_000, // 60ms gap
            wantTsbpd:  1_030_000,  // 50% through = 30ms
        },

        // Degenerate cases
        {
            name:       "same_boundaries",
            missingSeq: 100,
            lowerSeq:   100, lowerTsbpd: 1_000_000,
            upperSeq:   100, upperTsbpd: 1_000_000, // Same seq (shouldn't happen)
            wantTsbpd:  1_000_000, // Falls back to lower
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)
            require.Equal(t, tc.wantTsbpd, result)
        })
    }
}
```

### 7.6 Integration Test Scenarios

| Test | What It Verifies | Expected Outcome |
|------|------------------|------------------|
| `TestExpiry_NormalOperation` | Basic expiry with nakExpiryMargin | `NakBtreeExpiredEarly` increases |
| `TestExpiry_StarlinkBurst` | 60ms gap handling | Pre-filter works, accurate estimation |
| `TestExpiry_ExtendedOutage` | 1s+ outage | Most entries pre-filtered |
| `TestExpiry_RttSpike` | RTT doubles during gap | nakExpiryMargin protects entries |
| `TestExpiry_NoRttFallback` | RTT unavailable | Falls back to sequence-based |
| `TestExpiry_HighMargin` | nakExpiryMargin=1.0 | Fewer premature expirations |
| `TestExpiry_LowMargin` | nakExpiryMargin=0.0 | More expirations, check for over-expiry |

---

## 8. Testing Strategy

### 8.1 Unit Tests

**File:** `congestion/live/receive/nak_btree_test.go`

```go
func TestNakEntryWithTime_TsbpdField(t *testing.T) {
    // Verify struct layout and field access
    entry := NakEntryWithTime{
        Seq:           100,
        TsbpdTimeUs:   5000000, // 5 seconds
        LastNakedAtUs: 1000000,
        NakCount:      2,
    }

    require.Equal(t, uint32(100), entry.Seq)
    require.Equal(t, uint64(5000000), entry.TsbpdTimeUs)
    require.Equal(t, uint64(1000000), entry.LastNakedAtUs)
    require.Equal(t, uint32(2), entry.NakCount)
}

func TestDeleteBeforeTsbpd(t *testing.T) {
    tests := []struct {
        name              string
        entries           []NakEntryWithTime
        expiryThresholdUs uint64
        wantExpired       int
        wantRemaining     []uint32 // Remaining sequences
    }{
        {
            name: "expire none - all entries in future",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 10000000},
                {Seq: 101, TsbpdTimeUs: 10001000},
                {Seq: 102, TsbpdTimeUs: 10002000},
            },
            expiryThresholdUs: 5000000, // 5s threshold, all entries at 10s
            wantExpired:       0,
            wantRemaining:     []uint32{100, 101, 102},
        },
        {
            name: "expire all - all entries in past",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1000000},
                {Seq: 101, TsbpdTimeUs: 2000000},
                {Seq: 102, TsbpdTimeUs: 3000000},
            },
            expiryThresholdUs: 10000000, // 10s threshold, all entries < 10s
            wantExpired:       3,
            wantRemaining:     []uint32{},
        },
        {
            name: "expire some - mixed",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 5000000},  // Will expire (5s < 7s threshold)
                {Seq: 101, TsbpdTimeUs: 6000000},  // Will expire (6s < 7s threshold)
                {Seq: 102, TsbpdTimeUs: 8000000},  // Will keep (8s >= 7s threshold)
                {Seq: 103, TsbpdTimeUs: 9000000},  // Will keep (9s >= 7s threshold)
            },
            expiryThresholdUs: 7000000, // 7s threshold
            wantExpired:       2,
            wantRemaining:     []uint32{102, 103},
        },
        {
            name: "edge case - exact threshold",
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 5000000}, // Will expire (5s < 5s+1 threshold)
                {Seq: 101, TsbpdTimeUs: 5000001}, // Will keep (5.000001s >= 5.000001s threshold)
            },
            expiryThresholdUs: 5000001, // Exact boundary
            wantExpired:       1,
            wantRemaining:     []uint32{101},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            nb := newNakBtree(32)
            for _, e := range tt.entries {
                nb.InsertWithTsbpd(e.Seq, e.TsbpdTimeUs)
            }

            expired := nb.DeleteBeforeTsbpd(tt.expiryThresholdUs)
            require.Equal(t, tt.wantExpired, expired)
            require.Equal(t, len(tt.wantRemaining), nb.Len())

            // Verify remaining sequences
            var remaining []uint32
            nb.Iterate(func(e NakEntryWithTime) bool {
                remaining = append(remaining, e.Seq)
                return true
            })
            require.Equal(t, tt.wantRemaining, remaining)
        })
    }
}
```

**File:** `congestion/live/receive/nak_test.go`

```go
func TestExpireNakEntries_TimeBased(t *testing.T) {
    tests := []struct {
        name          string
        nowUs         uint64
        rtoUs         uint64
        entries       []NakEntryWithTime
        wantExpired   int
        wantRemaining int
    }{
        {
            name:  "no RTT - fallback to sequence-based",
            nowUs: 1000000,
            rtoUs: 0, // RTT not available
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 500000},
                {Seq: 101, TsbpdTimeUs: 600000},
            },
            // Falls back to sequence-based expiry
            wantExpired:   0, // Depends on minPkt in packet store
            wantRemaining: 2,
        },
        {
            name:  "with RTT 2ms - expire entries within RTT window",
            nowUs: 1000000, // now = 1s
            rtoUs: 2000,    // RTT = 2ms
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1001000}, // TSBPD 1.001s < now+RTT 1.002s → EXPIRE
                {Seq: 101, TsbpdTimeUs: 1001500}, // TSBPD 1.0015s < now+RTT 1.002s → EXPIRE
                {Seq: 102, TsbpdTimeUs: 1003000}, // TSBPD 1.003s >= now+RTT 1.002s → KEEP
            },
            wantExpired:   2,
            wantRemaining: 1,
        },
        {
            name:  "high RTT satellite link - 600ms",
            nowUs: 1000000,   // now = 1s
            rtoUs: 600000,    // RTT = 600ms (satellite)
            entries: []NakEntryWithTime{
                {Seq: 100, TsbpdTimeUs: 1500000}, // TSBPD 1.5s < now+RTT 1.6s → EXPIRE
                {Seq: 101, TsbpdTimeUs: 1700000}, // TSBPD 1.7s >= now+RTT 1.6s → KEEP
            },
            wantExpired:   1,
            wantRemaining: 1,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup receiver with mock RTT provider and NAK btree
            // ... test implementation ...
        })
    }
}

func TestEstimateTsbpdForSeq(t *testing.T) {
    // Test TSBPD estimation based on reference packet
    tests := []struct {
        name               string
        targetSeq          uint32
        referenceSeq       uint32
        referenceTsbpd     uint64
        avgInterPacketUs   uint64
        expectedTsbpd      uint64
    }{
        {
            name:             "target before reference",
            targetSeq:        98,
            referenceSeq:     100,
            referenceTsbpd:   5000000,
            avgInterPacketUs: 1000, // 1ms between packets
            expectedTsbpd:    4998000, // 5s - 2*1ms
        },
        {
            name:             "target after reference",
            targetSeq:        102,
            referenceSeq:     100,
            referenceTsbpd:   5000000,
            avgInterPacketUs: 1000,
            expectedTsbpd:    5002000, // 5s + 2*1ms
        },
        {
            name:             "same sequence",
            targetSeq:        100,
            referenceSeq:     100,
            referenceTsbpd:   5000000,
            avgInterPacketUs: 1000,
            expectedTsbpd:    5000000,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // ... test implementation ...
        })
    }
}
```

### 8.2 Integration Tests

**Test:** `Parallel-Clean-20M-FullEL-vs-FullSendEL` should show:
- Reduced `nak_not_found` (from ~1794 to near 0)
- New `nak_btree_expired_early` metric visible
- Same 100% recovery rate

**Validation criteria:**
```
Before optimization:
  internal_total [nak_not_found]: ~1794
  nak_btree_expired_early: N/A

After optimization:
  internal_total [nak_not_found]: < 10 (ideally 0)
  nak_btree_expired_early: > 0 (shows optimization is active)
```

### 8.3 Benchmark Tests

**File:** `congestion/live/receive/nak_btree_benchmark_test.go`

Comprehensive benchmarks comparing optimized vs slow implementations,
following the pattern established in `packet_store_btree_test.go`.

```go
// =============================================================================
// DELETE BENCHMARKS - Compare optimized vs slow implementations
// =============================================================================

// BenchmarkDeleteBeforeTsbpd_Optimized tests the DeleteMin() loop approach
func BenchmarkDeleteBeforeTsbpd_Optimized(b *testing.B) {
    sizes := []int{10, 50, 100, 500, 1000}
    expirePercents := []int{25, 50, 75, 100} // Percentage of entries to expire

    for _, size := range sizes {
        for _, pct := range expirePercents {
            name := fmt.Sprintf("size=%d/expire=%d%%", size, pct)
            b.Run(name, func(b *testing.B) {
                // Pre-calculate threshold to expire pct% of entries
                expireCount := size * pct / 100
                expiryThreshold := uint64(1000000 + expireCount*1000)

                b.ResetTimer()
                for i := 0; i < b.N; i++ {
                    b.StopTimer()
                    nb := newNakBtree(32)
                    for j := 0; j < size; j++ {
                        nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
                    }
                    b.StartTimer()

                    nb.DeleteBeforeTsbpd(expiryThreshold)
                }
            })
        }
    }
}

// BenchmarkDeleteBeforeTsbpd_Slow tests the collect-then-delete approach
func BenchmarkDeleteBeforeTsbpd_Slow(b *testing.B) {
    sizes := []int{10, 50, 100, 500, 1000}
    expirePercents := []int{25, 50, 75, 100}

    for _, size := range sizes {
        for _, pct := range expirePercents {
            name := fmt.Sprintf("size=%d/expire=%d%%", size, pct)
            b.Run(name, func(b *testing.B) {
                expireCount := size * pct / 100
                expiryThreshold := uint64(1000000 + expireCount*1000)

                b.ResetTimer()
                for i := 0; i < b.N; i++ {
                    b.StopTimer()
                    nb := newNakBtree(32)
                    for j := 0; j < size; j++ {
                        nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
                    }
                    b.StartTimer()

                    nb.DeleteBeforeTsbpdSlow(expiryThreshold)
                }
            })
        }
    }
}

// BenchmarkDeleteBeforeTsbpd_Comparison side-by-side for direct comparison
func BenchmarkDeleteBeforeTsbpd_Comparison(b *testing.B) {
    const size = 100
    const expireCount = 50
    expiryThreshold := uint64(1000000 + expireCount*1000)

    b.Run("Optimized_DeleteMin", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            b.StopTimer()
            nb := newNakBtree(32)
            for j := 0; j < size; j++ {
                nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
            }
            b.StartTimer()

            nb.DeleteBeforeTsbpd(expiryThreshold)
        }
    })

    b.Run("Slow_CollectDelete", func(b *testing.B) {
        for i := 0; i < b.N; i++ {
            b.StopTimer()
            nb := newNakBtree(32)
            for j := 0; j < size; j++ {
                nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
            }
            b.StartTimer()

            nb.DeleteBeforeTsbpdSlow(expiryThreshold)
        }
    })
}

// =============================================================================
// INSERT BENCHMARKS - Compare with/without TSBPD field
// =============================================================================

func BenchmarkInsert_Comparison(b *testing.B) {
    b.Run("Insert_NoTsbpd", func(b *testing.B) {
        nb := newNakBtree(32)
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            nb.Insert(uint32(i))
        }
    })

    b.Run("InsertWithTsbpd", func(b *testing.B) {
        nb := newNakBtree(32)
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            nb.InsertWithTsbpd(uint32(i), uint64(i*1000))
        }
    })
}

func BenchmarkInsertBatch_Comparison(b *testing.B) {
    sizes := []int{10, 50, 100}

    for _, size := range sizes {
        // Prepare test data
        seqs := make([]uint32, size)
        tsbpds := make([]uint64, size)
        for i := 0; i < size; i++ {
            seqs[i] = uint32(i)
            tsbpds[i] = uint64(1000000 + i*1000)
        }

        b.Run(fmt.Sprintf("InsertBatch_NoTsbpd/size=%d", size), func(b *testing.B) {
            for i := 0; i < b.N; i++ {
                nb := newNakBtree(32)
                nb.InsertBatch(seqs)
            }
        })

        b.Run(fmt.Sprintf("InsertBatchWithTsbpd/size=%d", size), func(b *testing.B) {
            for i := 0; i < b.N; i++ {
                nb := newNakBtree(32)
                nb.InsertBatchWithTsbpd(seqs, tsbpds)
            }
        })
    }
}

// =============================================================================
// MEMORY ALLOCATION BENCHMARKS
// =============================================================================

func BenchmarkDeleteBeforeTsbpd_Allocs(b *testing.B) {
    const size = 100
    const expireCount = 50
    expiryThreshold := uint64(1000000 + expireCount*1000)

    b.Run("Optimized_ZeroAllocs", func(b *testing.B) {
        b.ReportAllocs()
        for i := 0; i < b.N; i++ {
            b.StopTimer()
            nb := newNakBtree(32)
            for j := 0; j < size; j++ {
                nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
            }
            b.StartTimer()

            nb.DeleteBeforeTsbpd(expiryThreshold)
        }
    })

    b.Run("Slow_WithAllocs", func(b *testing.B) {
        b.ReportAllocs()
        for i := 0; i < b.N; i++ {
            b.StopTimer()
            nb := newNakBtree(32)
            for j := 0; j < size; j++ {
                nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
            }
            b.StartTimer()

            nb.DeleteBeforeTsbpdSlow(expiryThreshold)
        }
    })
}

// =============================================================================
// REALISTIC SCENARIO BENCHMARKS
// =============================================================================

// BenchmarkExpiryCycle simulates a realistic NAK expiry cycle
// Typical scenario: 20-50 NAK entries, expire 5-10 every 20ms
func BenchmarkExpiryCycle_Realistic(b *testing.B) {
    scenarios := []struct {
        name        string
        btreeSize   int
        expireCount int
    }{
        {"light_loss", 20, 5},
        {"moderate_loss", 50, 15},
        {"heavy_loss", 100, 30},
        {"burst_loss", 200, 100},
    }

    for _, sc := range scenarios {
        b.Run(sc.name, func(b *testing.B) {
            expiryThreshold := uint64(1000000 + sc.expireCount*1000)

            for i := 0; i < b.N; i++ {
                b.StopTimer()
                nb := newNakBtree(32)
                for j := 0; j < sc.btreeSize; j++ {
                    nb.InsertWithTsbpd(uint32(j), uint64(1000000+j*1000))
                }
                b.StartTimer()

                nb.DeleteBeforeTsbpd(expiryThreshold)
            }
        })
    }
}
```

**Expected benchmark results:**

| Scenario | Optimized (DeleteMin) | Slow (Collect+Delete) | Speedup |
|----------|----------------------|----------------------|---------|
| 100 entries, expire 50 | ~5-10µs, 0 allocs | ~10-20µs, 1+ allocs | ~2x |
| 500 entries, expire 250 | ~30-50µs, 0 allocs | ~60-100µs, 1+ allocs | ~2x |
| 1000 entries, expire 500 | ~70-100µs, 0 allocs | ~150-200µs, 1+ allocs | ~2x |

The optimization is particularly important because:
1. `expireNakEntries()` is called every 20ms (NAK tick interval)
2. Zero allocations avoids GC pressure during streaming
3. The DeleteMin loop has better cache locality than two-pass collection

---

## 9. Performance Considerations

### 9.1 Atomic Operations

| Operation | Frequency | Notes |
|-----------|-----------|-------|
| `r.rtt.RTOUs()` | Once per expiry cycle | Single atomic load |
| `r.nowFn()` | Once per expiry cycle | Time function call |
| TSBPD comparison | Once per entry | Simple integer comparison |

**Overhead:** Negligible - one atomic load plus O(n) integer comparisons per 20ms NAK cycle.

### 9.2 Memory Impact

| Item | Size | Notes |
|------|------|-------|
| `TsbpdTimeUs` field | +8 bytes per entry | Added to `NakEntryWithTime` |
| `avgInterPacketIntervalUs` | +8 bytes per receiver | Atomic uint64 |
| `lastPacketArrivalUs` | +8 bytes per receiver | Atomic uint64 |

**Total per connection:** ~16 bytes + 8 bytes per NAK entry

At typical NAK btree sizes (10-100 entries during loss events), this is 80-800 bytes additional memory per connection. This is an acceptable tradeoff for accurate time-based expiry.

### 9.3 Lock Contention

**Improved from current implementation:**
- No RLock needed for `packetStore.Min()` in the time-based path
- NAK btree has its own lock (same as before)
- RTT provider is lock-free (atomic reads)

### 9.4 CPU Impact

| Operation | Current | After |
|-----------|---------|-------|
| Gap insertion | O(log n) | O(log n) + TSBPD calculation |
| Expiry | O(n) sequence compare | O(n) time compare |
| Total per 20ms cycle | ~same | ~same |

The TSBPD calculation during insertion uses:
- One atomic load (`avgInterPacketIntervalUs`)
- Simple arithmetic (multiplication, addition)
- Cost: ~10 nanoseconds per insertion

---

## 10. Metrics and Observability

### 10.1 New Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_nak_btree_expired_early_total` | Counter | NAK entries expired due to RTT-based early expiry |

### 10.2 Existing Metrics (Already Implemented)

| Metric | Description |
|--------|-------------|
| `gosrt_nak_btree_expired_total` | NAK entries expired (sequence-based fallback) |
| `gosrt_internal_total{type="nak_not_found"}` | NAKs where sender couldn't find packet |

### 10.3 Expected Metric Changes After Implementation

| Metric | Before | After | Interpretation |
|--------|--------|-------|----------------|
| `nak_not_found` | ~1794 | ~0-10 | Success: NAKs sent for packets that will arrive in time |
| `nak_btree_expired_early` | N/A | >0 | New: entries expired due to RTT optimization |
| `nak_btree_expired` | ~same | ~reduced | Fallback path (RTT unavailable) |
| `nak_tsbpd_est_boundary` | N/A | >0 | Entries using linear interpolation (preferred) |
| `nak_tsbpd_est_ewma` | N/A | ~0-10 | Entries using EWMA fallback (edge cases) |

---

## 11. Implementation Safety Measures

> **Implementation Details:** See `nak_btree_expiry_optimization_implementation_plan.md` for
> complete step-by-step guide with Go file/function/line references.

### 11.1 TSBPD Monotonicity Guard

The design relies on TSBPD times increasing monotonically with sequence numbers.
However, edge cases could violate this:
- Sender-side clock jumps
- Upstream TSBPD calculation bugs
- Integer overflow in arithmetic

**Guard implementation in `estimateTsbpdForSeq()`:**
```go
	// Final monotonicity guard: ensure we never return less than lowerTsbpd
	// This protects against any arithmetic edge cases (overflow, etc.)
	if estimated < lowerTsbpd {
		return lowerTsbpd
	}
```

**Why this matters:** If estimated TSBPD is in the past, the packet would be
immediately expired and never NAK'd, losing a recovery opportunity.

### 11.2 Configuration Validation

The `NakExpiryMargin` configuration must be validated to prevent catastrophic misconfiguration:

```go
	// A value < -1.0 would result in an expiry threshold in the past,
	// effectively disabling all NAKs and causing 100% packet loss.
	if config.NakExpiryMargin < -1.0 {
		log.Printf("WARNING: NakExpiryMargin %.2f is invalid (< -1.0), resetting to default 0.10",
			config.NakExpiryMargin)
		config.NakExpiryMargin = 0.10
	}
```

| Value | Effect |
|-------|--------|
| `0.10` (default) | Conservative: `threshold = now + RTO * 1.1` |
| `0.0` | Baseline: `threshold = now + RTO` |
| `-0.5` | Aggressive: `threshold = now + RTO * 0.5` |
| `-1.0` | Maximum: `threshold = now` (expire everything not immediate) |
| `< -1.0` | **INVALID**: threshold in past, causes 100% loss |

### 11.3 Named Constants for Testability

Inter-packet tracking uses named constants for clarity and testability:

```go
const (
	InterPacketIntervalMinUs     = 10       // 10µs minimum (filters measurement errors)
	InterPacketIntervalMaxUs     = 100_000  // 100ms maximum (filters pauses)
	InterPacketIntervalDefaultUs = 1000     // 1ms default (~1000 pkt/s)
	InterPacketEWMAOld           = 0.875    // EWMA weight for old value
	InterPacketEWMANew           = 0.125    // EWMA weight for new value
)
```

The `updateInterPacketInterval()` function is extracted for unit testing without
full receiver setup.

### 11.4 Optimized Batch TSBPD Calculation

The batch TSBPD calculation is optimized to avoid per-entry division:

**Naive approach:** O(N) divisions for N gaps
**Optimized approach:** O(B) divisions + O(N) additions for B boundaries

For a 100-packet gap with 1 boundary:
- Naive: 100 divisions ≈ 3000+ CPU cycles
- Optimized: 1 division + 100 additions ≈ 300 CPU cycles (~10x faster)

---

## 12. Rollout Plan

### 12.1 Phase 1: Data Structure Changes

1. Add `TsbpdTimeUs` field to `NakEntryWithTime` struct
2. Add `InsertWithTsbpd()` and `InsertBatchWithTsbpd()` methods
3. Add `DeleteBeforeTsbpd()` and `DeleteBeforeTsbpdLocking()` methods
4. Add unit tests for new btree methods

### 12.2 Phase 2: TSBPD Estimation

1. Add `avgInterPacketIntervalUs` and `lastPacketArrivalUs` to receiver
2. Implement `estimateTsbpdForSeq()` function
3. Update `pushLocked()` to track inter-packet interval
4. Add unit tests for TSBPD estimation

### 12.3 Phase 3: Expiry Logic

1. Modify `expireNakEntries()` to use time-based expiry
2. Update gap insertion call sites to use `InsertWithTsbpd()`
3. Add `NakBtreeExpiredEarly` metric
4. Add benchmark tests

### 12.4 Phase 4: Integration Testing

1. Run `Parallel-Clean-20M-FullEL-vs-FullSendEL`
2. Validate `nak_not_found` reduced from ~1794 to ~0
3. Verify `nak_btree_expired_early` metric is being incremented
4. Verify 100% recovery rate maintained

### 12.5 Phase 5: Documentation

1. Update `design_nak_btree.md` to mark FR-19 as IMPLEMENTED
2. Update `send_eventloop_intermittent_failure_bug.md` with resolution
3. Close related defect tracking

---

## Appendix A: Related Code References

### A.1 Current Expiry Implementation

```
congestion/live/receive/nak.go:509-544       expireNakEntries()
congestion/live/receive/tick.go:70           expireNakEntries() called in Tick()
congestion/live/receive/tick.go:267          expireNakEntries() called in EventLoop
```

### A.2 RTO Infrastructure (Reusable)

```
connection_rtt.go:1-110                      RTT struct with RTOUs()
connection_rtt.go:21                         rtoUs atomic.Uint64
connection_rtt.go:73-75                      RTOUs calculation and storage
congestion/interface.go                      RTTProvider interface
congestion/live/receive/receiver.go:57-59    rtt field in receiver
congestion/live/receive/nak_consolidate.go:67-69  Example of using r.rtt.RTOUs()
```

### A.3 NAK Btree Operations

```
congestion/live/receive/nak_btree.go:15-19   NakEntryWithTime struct (to be modified)
congestion/live/receive/nak_btree.go:39-55   Insert() and InsertLocking()
congestion/live/receive/nak_btree.go:57-83   InsertBatch() and InsertBatchLocking()
congestion/live/receive/nak_btree.go:102-128 DeleteBefore() and DeleteBeforeLocking()
```

### A.4 TSBPD Time on Packets

```
packet/packet.go:248                         PktTsbpdTime field in Header
connection.go                                TSBPD calculation: tsbpdTimeBase + timestamp + tsbpdDelay
```

---

## Appendix B: Design Decision Rationale

### B.1 Why Time-Based Instead of Sequence-Based?

| Approach | Pros | Cons |
|----------|------|------|
| **Sequence-based** | Simple, no struct changes, lower memory | Approximation, assumes steady bitrate |
| **Time-based** | Accurate, works at any bitrate | Requires adding TSBPD to struct, more memory |

**Decision: Time-Based** because:
1. **Accuracy** - Directly compares TSBPD time with RTT, no approximations
2. **Consistency** - Other btree scans (packet delivery, NAK generation) are already time-based
3. **Correctness** - Works correctly regardless of bitrate or packet size variations
4. **Acceptable overhead** - 8 bytes per NAK entry is minimal at typical btree sizes

### B.2 Why Use RTOUs Instead of RTT?

RTOUs = RTT + RTTVar provides:
1. Safety margin for RTT variance
2. Consistent with NAK suppression in `consolidateNakBtree()`
3. Already pre-calculated (no computation needed)

### B.3 Why Track Inter-Packet Interval?

For accurate TSBPD estimation of missing sequences:
1. **EWMA smoothing** - Handles burst traffic gracefully
2. **Atomic operations** - No locks on hot path
3. **Reasonable default** - Falls back to 1ms if unknown

### B.4 Backward Compatibility

If RTT is not available (early connection state), the implementation falls back to sequence-based expiry using `packetStore.Min()`. This ensures:
1. No regression in existing behavior
2. Graceful degradation
3. Same correctness guarantees as Phase 1 implementation

