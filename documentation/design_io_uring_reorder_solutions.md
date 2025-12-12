# Design: Handling Out-of-Order Packet Delivery with io_uring

**Status**: DRAFT - Discussion Document (Updated with feedback)
**Date**: 2025-12-11
**Related**: `parallel_defect1_highperf_excessive_gaps.md`

## Design Decisions (Agreed)

| Decision | Status |
|----------|--------|
| btree mandatory for io_uring recv | ✅ Agreed |
| Sliding window approach | ✅ Agreed |
| Periodic NAK only (no immediate NAK) | ✅ Agreed |
| Reuse existing btree for tracking | ✅ Agreed |
| Range merging with acceptable duplicates | ✅ Agreed |
| Priority ordering (most urgent first) | ✅ To design |
| TCP SACK approach | ❌ Rejected (breaks compatibility) |

## Problem Statement

When using `io_uring` for the receive path, packets are delivered to the application layer **out of order**, even though they arrived at the kernel network stack in order.

### Root Cause (Confirmed)

With 512 outstanding `recvmsg` requests in the io_uring submission queue, completions arrive in arbitrary order based on:
- Kernel scheduling across CPU cores
- io_uring internal batching
- Memory/buffer availability

### Evidence

Debug logging shows clear out-of-order patterns:
```
seq=194811147
seq=194811150    ← gap (148, 149 missing)
seq=194811152    ← gap (151 missing)
seq=194811146    ← OUT OF ORDER (went back 6)
seq=194811148    ← filling gap
seq=194811149    ← filling gap
...
seq=194811181
seq=194811122    ← MAJOR OUT OF ORDER (went back 59!)
```

### Impact

On a **clean network** with 0% packet loss:
- Current implementation detects **2,476 false gaps**
- Generates **718 unnecessary NAKs**
- Creates **2,500 unnecessary retransmissions**
- Results in **2,500 "already_acked" drops**

This wastes bandwidth and CPU cycles on retransmitting packets that were never lost.

---

## Current SRT NAK Architecture

### Immediate NAK on Gap Detection

The current receiver logic (in `congestion/live/receive.go`) triggers NAKs immediately:

```go
// Simplified current logic
func (r *liveRecv) handlePacket(pkt) {
    seq := pkt.Header().PacketSequenceNumber

    if seq > r.maxSeenSequence + 1 {
        // GAP DETECTED!
        gapStart := r.maxSeenSequence + 1
        gapEnd := seq - 1
        r.sendNAK(gapStart, gapEnd)  // Immediate NAK
    }

    r.maxSeenSequence = max(r.maxSeenSequence, seq)
    r.storePacket(pkt)
}
```

### Periodic NAK (Timer-Based)

In addition to immediate NAKs, SRT has periodic NAK retransmission every 20ms.

**Source**: `congestion/live/receive.go:462-511` - `periodicNAKLocked()`

**Key insight**: There is NO separate `missingPackets` structure! Instead, gaps are detected by iterating through the packet store (btree/list) and comparing expected vs actual sequence numbers:

```go
// congestion/live/receive.go:462-511 - ACTUAL implementation
func (r *receiver) periodicNAKLocked(now uint64) []circular.Number {
    if now-r.lastPeriodicNAK < r.periodicNAKInterval {
        return nil  // Not time yet (< 20ms since last)
    }

    list := []circular.Number{}
    ackSequenceNumber := r.lastACKSequenceNumber

    // Iterate through ALL packets in the store
    r.packetStore.Iterate(func(p packet.Packet) bool {
        h := p.Header()

        // Skip packets we already ACK'd
        if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
            return true // Continue
        }

        // GAP DETECTED: expected ackSequenceNumber+1, got something higher
        if !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
            nackSequenceNumber := ackSequenceNumber.Inc()  // First missing

            // Add range [first_missing, last_missing] to NAK list
            list = append(list, nackSequenceNumber)           // start
            list = append(list, h.PacketSequenceNumber.Dec()) // end
        }

        ackSequenceNumber = h.PacketSequenceNumber
        return true // Continue
    })

    r.lastPeriodicNAK = now
    return list  // Format: [start1, end1, start2, end2, ...]
}
```

**How gap detection works**:

```
Packet store contains: [100, 101, 105, 106, 110]
lastACKSequenceNumber = 99

Iteration:
  Packet 100: expected 100 (99+1), got 100 ✓ no gap
  Packet 101: expected 101 (100+1), got 101 ✓ no gap
  Packet 105: expected 102 (101+1), got 105 ✗ GAP!
              → Add [102, 104] to NAK list
  Packet 106: expected 106 (105+1), got 106 ✓ no gap
  Packet 110: expected 107 (106+1), got 110 ✗ GAP!
              → Add [107, 109] to NAK list

Result NAK list: [102, 104, 107, 109]
  = Range 102-104 (3 packets missing)
  = Range 107-109 (3 packets missing)
```

**Important**: This scans the ENTIRE packet store every 20ms. For large buffers (~1400 packets at 5Mbps with 3s latency), this is significant overhead - which is why our NAK btree proposal reduces this to scanning only a window.

### NAK Packet Format (RFC)

From SRT RFC Section 3.2.5:

```
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|0|                 Lost packet sequence number                 |  ← Single packet
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|1|         Range of lost packets from sequence number          |  ← Range start
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|0|                    Up to sequence number                    |  ← Range end
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

**Key insight**: A single NAK packet can contain:
- Multiple single sequence numbers (4 bytes each)
- Multiple ranges (8 bytes each: start + end)
- Mixed singles and ranges

Maximum entries per NAK = `(MSS - header_size) / 4` ≈ 320 singles or 160 ranges

---

## Solution Options

### Option 1: Small Sorting Buffer (Pre-SRT Reorder)

**Concept**: Add a small buffer between io_uring and SRT that reorders packets before delivering them to the SRT receive path.

```
io_uring completions → [Reorder Buffer] → SRT receiver (in-order)
```

**Implementation**:
```go
type ReorderBuffer struct {
    buffer    map[uint32]*packet.Packet  // seq → packet
    nextSeq   uint32                      // next expected sequence
    maxDelay  int                         // max packets to buffer (e.g., 100)
}

func (rb *ReorderBuffer) Insert(pkt *packet.Packet) []*packet.Packet {
    seq := pkt.Header().PacketSequenceNumber
    rb.buffer[seq] = pkt

    // Flush in-order packets
    var ready []*packet.Packet
    for {
        if p, ok := rb.buffer[rb.nextSeq]; ok {
            ready = append(ready, p)
            delete(rb.buffer, rb.nextSeq)
            rb.nextSeq++
        } else {
            break
        }
    }

    // Force flush if buffer too full (oldest packet is "too late")
    if len(rb.buffer) > rb.maxDelay {
        // Advance nextSeq to force delivery
        rb.nextSeq = rb.findMinSeq()
        // Recursive flush
        return append(ready, rb.Flush()...)
    }

    return ready
}
```

**Pros**:
- Transparent to SRT — no changes to NAK logic
- Simple to implement and understand
- Can use existing btree implementation

**Cons**:
- Adds latency (waits for reordering)
- Extra memory for buffer
- Doesn't leverage SRT's existing receive buffer
- Packets may still be delivered with gaps if reorder depth exceeds buffer

**Questions**:
1. What buffer size is sufficient? (Debug shows 60+ packet reorder depth)
2. How to handle timeout for truly lost packets?
3. Does this duplicate work already done by SRT's receive buffer?

---

### Option 2: Sliding Window NAK Delay (goTrackRTP-Inspired)

**Concept**: Delay NAK generation until packets have truly "fallen off" a sliding window, rather than NAKing immediately on gap detection.

```
                ← Behind Window (bw) →     Max()
                ┌───────────────────────────┬─────┐
     ...────────│   Acceptable Late         │ 500 │────...
                └───────────────────────────┴─────┘
                      ↑
              Only NAK packets that
              fall off this edge
```

**Implementation Approach**:

```go
type SlidingWindowReceiver struct {
    maxSeen       uint32           // Highest sequence number seen
    behindWindow  uint32           // How far back is acceptable (e.g., 100)
    receivedSeqs  *btree.BTreeG    // Tracks which sequences we've received
}

func (sw *SlidingWindowReceiver) handlePacket(pkt) {
    seq := pkt.Header().PacketSequenceNumber

    // Update max if this is a new high
    if seq > sw.maxSeen {
        oldMax := sw.maxSeen
        sw.maxSeen = seq

        // Check if any packets have fallen off the behind window
        cutoff := sw.maxSeen - sw.behindWindow
        lostPackets := sw.findMissingBelow(cutoff)
        if len(lostPackets) > 0 {
            sw.sendNAK(lostPackets)  // Only NAK truly late packets
        }
    }

    // Mark this sequence as received
    sw.receivedSeqs.ReplaceOrInsert(seq)

    // Cleanup: remove sequences that are too old
    sw.receivedSeqs.DeleteMin() // when below cutoff
}
```

**Pros**:
- No extra memory for reordering (uses existing btree)
- Integrates with SRT's existing buffer
- Reduces unnecessary NAKs by 95%+
- Proven concept (goTrackRTP)

**Cons**:
- Requires btree (can mandate btree when io_uring recv enabled)
- Changes NAK timing semantics
- Need to tune behind window size
- Interaction with periodic NAK needs design

**Questions**:
1. How does this interact with TSBPD (Time Stamp Based Packet Delivery)?
2. What happens during connection startup (initial sequence)?
3. Should window be packet-count or time-based?
4. How to handle sequence number wraparound?

---

### Option 3: Periodic-Only NAK (Eliminate Immediate NAK)

**Concept**: Remove immediate NAK entirely when io_uring is enabled. Rely only on periodic NAK timer to request missing packets.

**Implementation**:

```go
func (r *liveRecv) handlePacket(pkt) {
    seq := pkt.Header().PacketSequenceNumber
    r.storePacket(pkt)  // btree stores in order automatically
    r.maxSeenSequence = max(r.maxSeenSequence, seq)
    // NO immediate NAK
}

func (r *liveRecv) periodicNAKTick() {
    // Calculate what should have arrived by now
    cutoffSeq := r.calculateExpectedMinSeq()

    // Find gaps in btree
    missing := r.findMissingSequences(cutoffSeq, r.maxSeenSequence)

    if len(missing) > 0 {
        r.sendPackedNAK(missing)  // Single NAK with list
    }
}
```

**Pros**:
- Simplest change — just disable immediate NAK
- Natural batching of loss reports
- Works with existing periodic timer

**Cons**:
- Delays NAK by up to one timer period
- May increase latency for actual packet loss
- Less responsive than immediate NAK for real loss

**Questions**:
1. What's the current periodic NAK interval?
2. Is the delay acceptable for latency-sensitive applications?
3. Should the timer interval be adaptive?

---

### Option 4: Packed NAK with btree Traversal

**Concept**: Keep immediate NAK but make it smarter — traverse btree to find ALL missing packets and pack them into a single NAK.

**Current Behavior**:
```
Gap detected: seq 100 missing → NAK(100)
Gap detected: seq 101 missing → NAK(101)
Gap detected: seq 102 missing → NAK(102)
# 3 separate NAK packets
```

**Proposed Behavior**:
```
Periodic tick → traverse btree → find gaps 100-102, 150, 200-210
# Single NAK packet: [Range(100,102), Single(150), Range(200,210)]
```

**Implementation**:

```go
func (r *liveRecv) sendPackedNAK() {
    var entries []NAKEntry

    // Traverse btree to find gaps
    expectedSeq := r.oldestInBuffer
    r.packetStore.Ascend(func(pkt *packet.Packet) bool {
        seq := pkt.Seq()

        if seq > expectedSeq {
            // Gap from expectedSeq to seq-1
            if seq == expectedSeq + 1 {
                entries = append(entries, NAKSingle(expectedSeq))
            } else {
                entries = append(entries, NAKRange(expectedSeq, seq-1))
            }
        }
        expectedSeq = seq + 1

        // Stop if NAK packet is full
        return len(entries) < MaxNAKEntries
    })

    if len(entries) > 0 {
        r.sendNAKPacket(entries)
    }
}
```

**NAK Packet Efficiency**:
| Loss Pattern | Singles Approach | Packed Approach |
|--------------|------------------|-----------------|
| 10 consecutive | 10 packets (40B each) | 1 packet (8B range) |
| 50 scattered | 50 packets | 1 packet (~200B) |
| 100 mixed | 100 packets | 1 packet (~400B) |

**Pros**:
- Dramatically reduces NAK packet count
- Efficient use of NAK range encoding
- Works with any packet store (list or btree)
- Leverages existing SRT NAK format

**Cons**:
- btree traversal cost per tick
- Doesn't solve immediate NAK problem
- Still need to decide when to NAK

---

### Option 5: TCP-Inspired Selective ACK (SACK)

**Concept**: Instead of NAKing missing packets, ACK ranges of received packets. Sender infers gaps.

```
Receiver sends: ACK(100-150, 155-200, 210-300)
Sender infers: Missing 151-154, 201-209
```

**Pros**:
- Proven in TCP
- Receiver doesn't need to track what's missing
- More robust to ACK loss

**Cons**:
- Major protocol change
- Not compatible with standard SRT
- Requires sender-side changes

**Verdict**: Too invasive for current scope. Note for future consideration.

---

### Option 6: io_uring Ordering Enforcement

**Concept**: Use io_uring features to enforce completion ordering.

**Approaches**:
1. **IOSQE_IO_LINK**: Chain operations so they complete in order
2. **Single outstanding read**: Only one recvmsg at a time
3. **io_uring_register_ring_fd with ordering**: Kernel-enforced order

**Research Needed**:
```c
// IOSQE_IO_LINK chains operations
sqe->flags |= IOSQE_IO_LINK;  // This op must complete before next
```

**Questions**:
1. Does IOSQE_IO_LINK work for recvmsg? (Probably not — designed for file I/O)
2. What's the performance impact of single outstanding read?
3. Are there newer io_uring ordering features?

**Pros**:
- Solves problem at source
- No changes to SRT logic

**Cons**:
- May not be possible for UDP recvmsg
- Loses parallelism benefit of io_uring
- Kernel version dependent

---

## Recommendation Matrix

| Option | Complexity | Performance Impact | Protocol Change | Recommended |
|--------|------------|-------------------|-----------------|-------------|
| 1: Sorting Buffer | Low | Medium (latency) | None | Maybe |
| 2: Sliding Window NAK | Medium | Low | Behavioral | **Yes** |
| 3: Periodic-Only NAK | Low | Medium (latency) | Behavioral | Maybe |
| 4: Packed NAK | Medium | Low | None | **Yes** |
| 5: TCP SACK | High | Low | Major | No (future) |
| 6: io_uring Ordering | Unknown | High (perf loss) | None | Research |

---

---

## Refined Design v2: Dual btree Architecture (NAK btree)

### Motivation

The original design scans the entire primary packet btree every 20ms to find gaps. This is O(n) where n = packets in buffer. With a 3-second buffer at 5 Mbps, that's ~1,400 packets to scan 50 times per second.

**New idea**: Use a separate **NAK btree** that only contains missing sequence numbers. This dramatically reduces scanning overhead.

### Architecture

```
                          ← Primary Packet btree (received packets) →
                                                                                       Max()
┌────────────────────────────┬─────────────────────────────┬───────────────────────────┬──────┐
│  VERY LATE                 │  LATE (scan window)         │  ACCEPTABLE LATE          │  600 │
│  seq 400-450               │  seq 451-550                │  seq 551-599              │      │
│  (already NAK'd, waiting)  │  (scan here for NEW gaps)   │  (just arrived OOO)       │      │
└────────────────────────────┴─────────────────────────────┴───────────────────────────┴──────┘
                             ↑                             ↑
                        scanStart                      scanEnd
                    (Max - behindWindow)            (Max - acceptableWindow)

                ← NAK btree (missing sequence numbers only) →
┌────────────────────────────────────────────────────────────┬──────┐
│  Missing sequences: 402, 410, 455-458, 470, 512            │      │
│  (much smaller than primary btree!)                        │      │
└────────────────────────────────────────────────────────────┴──────┘
         ↑                                                   ↑
    Drop sequences                                     Add new gaps
    that are too late                                  from scan window
```

### How It Works

#### Design Decision: Store Singles, Consolidate at NAK Time

The NAK btree stores **individual sequence numbers only** (not ranges). This simplifies operations:

| Operation | Singles (chosen) | Ranges (rejected) |
|-----------|------------------|-------------------|
| Insert gap | O(log n) insert each seq | Complex: check for adjacent ranges |
| Packet arrives | O(log n) delete | Complex: split range (50-60 → 50-54, 56-60) |
| Consolidation | Done at NAK generation | Already done, but splits are expensive |

**Example of range splitting complexity**:
```
Initial range: 50-60 (NAK'd as a single range)
Packet 55 arrives → must split to: 50-54, 56-60
Packet 56 arrives → must update to: 50-54, 57-60
Packet 52 arrives → must split 50-54 to: 50-51, 53-54
```
This is complex and expensive. **Storing singles avoids this entirely.**

#### On Packet Arrival (Push)

```go
func (r *receiver) Push(pkt) {
    seq := pkt.Header().PacketSequenceNumber

    // 1. Store in primary btree
    r.packetStore.Insert(pkt)

    // 2. Remove from NAK btree (packet arrived, no longer missing)
    //    O(log n) - simple delete, no range splitting!
    r.nakBtree.Delete(seq)

    // 3. Update maxSeen
    if seq > r.maxSeen {
        r.maxSeen = seq
    }

    // NO immediate NAK - periodic NAK will handle gaps
}
```

#### On Periodic NAK Tick (every 20ms)

```go
func (r *receiver) periodicNAK(now uint64) {
    // Step 1: Define scan window
    scanStart := r.maxSeen - r.behindWindow      // e.g., 500-100 = 400
    scanEnd := r.maxSeen - r.acceptableWindow    // e.g., 500-50 = 450

    // Step 2: Scan ONLY the window range of primary btree for NEW gaps
    expectedSeq := scanStart
    r.packetStore.AscendRange(scanStart, scanEnd, func(pkt) bool {
        actualSeq := pkt.Seq()
        for expectedSeq < actualSeq {
            // Gap found - add single sequence to NAK btree
            r.nakBtree.ReplaceOrInsert(expectedSeq)
            expectedSeq++
        }
        expectedSeq = actualSeq + 1
        return true
    })

    // Step 3: Remove sequences too old (fell off behind window)
    cutoff := r.maxSeen - r.behindWindow
    r.nakBtree.DeleteBefore(cutoff)

    // Step 4: Consolidate singles into ranges (done at NAK time, not storage time)
    nakList := r.consolidateAndBuildNAK()

    // Step 5: Send (most urgent first)
    if len(nakList) > 0 {
        r.sendNAK(nakList)
    }
}
```

### Range Consolidation Algorithm

Since we store singles, we must consolidate into ranges at NAK generation time. This is where burst losses (Starlink ~60ms outages = 3 NAK periods) become efficient ranges.

#### Consolidation with Time Budget

```go
func (r *receiver) consolidateAndBuildNAK() []circular.Number {
    deadline := time.Now().Add(r.consolidationBudget) // e.g., 1ms max

    var result []circular.Number
    var rangeStart, rangeEnd uint32
    inRange := false

    // NAK btree stores singles in ascending order
    // We want DESCENDING order for urgency (oldest = most urgent first)
    // Use DescendLessOrEqual to iterate from highest to lowest
    r.nakBtree.DescendLessOrEqual(r.maxSeen, func(seq uint32) bool {
        // Check time budget
        if time.Now().After(deadline) {
            // Time's up! Emit current range and stop consolidating
            // Remaining singles will be sent as-is next period
            return false
        }

        if !inRange {
            rangeStart = seq
            rangeEnd = seq
            inRange = true
            return true
        }

        // Check if contiguous or within merge gap
        if rangeStart - seq <= r.mergeGap + 1 {
            // Extend range (accept duplicates for small gaps)
            rangeStart = seq
        } else {
            // Gap too large - emit current range, start new
            result = append(result,
                circular.New(rangeEnd, packet.MAX_SEQUENCENUMBER),
                circular.New(rangeStart, packet.MAX_SEQUENCENUMBER))
            rangeStart = seq
            rangeEnd = seq
        }
        return true
    })

    // Emit final range
    if inRange {
        result = append(result,
            circular.New(rangeEnd, packet.MAX_SEQUENCENUMBER),
            circular.New(rangeStart, packet.MAX_SEQUENCENUMBER))
    }

    return result
}
```

**Key points**:
- Traverse in **descending** order (most urgent/oldest first)
- Result pairs are `[end, start]` so oldest sequences appear first in NAK packet
- Time budget prevents consolidation from blocking packet processing
- If time runs out, remaining singles are sent as-is (suboptimal but safe)

#### Merge Gap Handling

With `mergeGap = 3`:
```
Singles in NAK btree: [50, 51, 52, 55, 56, 57, 58, 62, 63]

Consolidation (descending):
  63 → start new range [63, 63]
  62 → extend to [62, 63]
  58 → gap=3, within mergeGap → extend to [58, 63] (59,60,61 will be duplicates)
  57 → contiguous → [57, 63]
  56 → contiguous → [56, 63]
  55 → contiguous → [55, 63]
  52 → gap=2, within mergeGap → [52, 63] (53,54 will be duplicates)
  51 → contiguous → [51, 63]
  50 → contiguous → [50, 63]

Result: Single range [50, 63] (vs 9 singles)
```

---

### MergeGap Explained

**What is MergeGap?**

When consolidating singles from the NAK btree into ranges, `MergeGap` defines how many **received packets** we'll allow between missing sequences before breaking into separate ranges.

**Why MergeGap exists**:

Without MergeGap, if we have missing sequences `[5, 6, 8, 9, 10]`, we'd send:
- Range 5-6 (missing, contiguous)
- Range 8-10 (missing, contiguous)

That's 2 NAK entries (2 ranges = 16 bytes). But if `MergeGap=2`, we notice that packet 7 (which arrived) is only a gap of 1 between the two ranges, so we merge:
- Range 5-10 (request all, even though 7 was received)

Now only 1 NAK entry (1 range = 8 bytes). Packet 7 will be retransmitted unnecessarily, but:
1. It's already in our buffer (we'll drop it as duplicate)
2. NAK packet is smaller (1 range vs 2 ranges)

**Better example showing the benefit**:

Missing sequences: `[100, 101, 103, 105, 106, 107]`
(Packets 102 and 104 arrived)

Without MergeGap:
- Range 100-101
- Single 103
- Range 105-107

That's 3 entries (8 + 4 + 8 = 20 bytes).

With MergeGap=2:
- Range 100-107 (merge across gaps of 1 packet each)

That's 1 entry (8 bytes). Packets 102 and 104 will be retransmitted as duplicates.

**MergeGap trade-off**:

| MergeGap | NAK Entries | Duplicate Retransmissions | Use When |
|----------|-------------|---------------------------|----------|
| 0 | Many (precise) | None | Bandwidth-critical |
| 1-3 | Moderate | Few | Balanced (default) |
| 5+ | Few | More | High loss, want simpler NAKs |

**Time-based MergeGap**:

`MergeGapMs = 5ms` means at 5 Mbps (~0.43 pkts/ms) = ~2 packets merged.
At 20 Mbps (~1.7 pkts/ms) = ~8 packets merged.

This keeps merge behavior consistent across data rates.

**Note on "duplicates"**: These aren't duplicates in the NAK btree (btree naturally deduplicates). Rather, they're packets we already have that will be retransmitted because we requested a range. We handle them as normal duplicate drops.

### Comparison: Original vs NAK btree

| Aspect | Original (scan primary) | NAK btree |
|--------|------------------------|-----------|
| **Scan size per tick** | All packets in buffer (~1400) | Only scan window (~100) |
| **Gap detection** | Re-detect every tick | Detect once, store in NAK btree |
| **NAK list building** | Traverse all, find gaps | Traverse NAK btree (only missing) |
| **Packet arrival** | O(1) insert | O(log n) insert + O(log m) delete from NAK |
| **Memory** | Primary btree only | Primary + NAK btree (small) |
| **Duplicate NAK prevention** | Not tracked | Natural: delete from NAK btree on arrival |

---

### Lock Design for NAK btree

#### Problem

The google btree implementation is NOT thread-safe. Currently, the primary packet btree is protected by `receiver.lock`. The periodic NAK holds this lock during btree traversal, which blocks packet processing.

#### Options

| Option | Pros | Cons |
|--------|------|------|
| **Reuse existing lock** | Simpler, no deadlock risk | PeriodicNAK blocks Push() |
| **Separate NAK btree lock** | Push() not blocked by NAK gen | More complex, potential deadlock |
| **RWMutex for NAK btree** | Readers don't block each other | Still blocks on writes |

#### Recommendation: Separate Lock with Clear Ordering

Use a separate `sync.RWMutex` for the NAK btree with defined lock ordering to prevent deadlock:

```go
type receiver struct {
    // Primary packet store (existing)
    lock        sync.RWMutex
    packetStore PacketStore

    // NAK btree (new) - separate lock
    nakLock     sync.RWMutex
    nakBtree    *nakBtree
}

// Lock ordering: ALWAYS acquire nakLock BEFORE lock if both needed
// (In practice, they're rarely needed together)
```

**Push() path** (hot path):
```go
func (r *receiver) pushLocked(pkt) {
    // 1. Delete from NAK btree (separate lock, brief)
    r.nakLock.Lock()
    r.nakBtree.Delete(seq)
    r.nakLock.Unlock()

    // 2. Insert to packet store (existing lock already held)
    r.packetStore.Insert(pkt)
}
```

**PeriodicNAK path** (background):
```go
func (r *receiver) periodicNAKLocked(now uint64) {
    // 1. Read from packet store to find gaps (read lock on primary)
    r.lock.RLock()
    gaps := r.scanWindowForGaps()
    r.lock.RUnlock()

    // 2. Update NAK btree (separate lock)
    r.nakLock.Lock()
    for _, seq := range gaps {
        r.nakBtree.Insert(seq)
    }
    r.nakBtree.DeleteBefore(cutoff)
    result := r.consolidateAndBuildNAK()
    r.nakLock.Unlock()

    return result
}
```

**Benefits**:
- Push() only briefly holds nakLock for Delete()
- PeriodicNAK doesn't block Push() during consolidation
- Clear lock ordering prevents deadlock

---

### NAK btree Expiration: RTT Consideration

**Problem**: When should we stop NAKing for a missing packet?

The packet btree releases packets at TSBPD time. But NAK btree entries should expire **earlier** because:
1. NAK takes time to reach the sender (RTT/2)
2. Retransmitted packet takes time to return (RTT/2)
3. If we NAK too late, retransmission arrives after TSBPD deadline = useless

**Formula**:
```
NAK expiration = TSBPD_deadline - smoothed_RTT - safety_margin
```

**Implementation**:
```go
func (r *receiver) nakExpirationCutoff() uint32 {
    // Get smoothed RTT from connection (already tracked in goSRT)
    rttPackets := r.rttToPackets(r.smoothedRTT)
    safetyMargin := r.windowCalc.packetsPerMs * 20  // 20ms margin

    // Expire NAK entries earlier than packet btree
    return r.maxSeen.Val() - r.windowCalc.BehindWindow() + rttPackets + uint32(safetyMargin)
}

func (r *receiver) rttToPackets(rtt time.Duration) uint32 {
    rttMs := float64(rtt.Milliseconds())
    return uint32(rttMs * r.windowCalc.packetsPerMs)
}
```

**Example**:
```
RTT = 100ms, rate = 5 Mbps (~0.43 pkt/ms)
rttPackets = 100 * 0.43 = 43 packets
safetyMargin = 20 * 0.43 = 9 packets

NAK expiration = maxSeen - behindWindow + 43 + 9 = maxSeen - behindWindow + 52

So NAKs expire 52 packets (~120ms) before TSBPD would release them.
This gives time for NAK round trip + retransmission.
```

**"VERY LATE" and "LATE" categories**:
- **VERY LATE**: Between NAK expiration cutoff and TSBPD release → Keep NAKing (last chance!)
- **LATE**: In normal scan window → Normal NAK priority
- After NAK expiration: Stop NAKing, packet won't arrive in time anyway

---

### Pros

1. **Reduced scanning overhead**
   - Only scan a small window of primary btree, not the entire buffer
   - Scan window = `behindWindow - acceptableWindow` (~50-100 packets)
   - Compared to full buffer scan of ~1400 packets

2. **Efficient NAK generation**
   - NAK btree only contains missing sequences
   - At 5 Mbps with 1% loss, ~14 entries (vs 1400 packets in primary)
   - Fast traversal to build NAK packet

3. **Natural duplicate NAK prevention**
   - When packet arrives, remove from NAK btree
   - Won't NAK for same sequence again
   - Current design may re-NAK on every periodic tick

4. **Clean expiration**
   - When sequences fall off behind window, delete from NAK btree
   - `DeleteBefore(cutoff)` is efficient btree operation

5. **Incremental gap detection**
   - Only detect NEW gaps in the scan window
   - Already-known gaps are in NAK btree
   - Reduces redundant work

### Cons

1. **Additional memory**
   - Second btree structure
   - But NAK btree is much smaller (only missing sequences)
   - Memory: ~100 entries × 8 bytes = ~800 bytes (negligible)

2. **Complexity**
   - Two btrees to maintain
   - Need to keep them synchronized
   - More code paths to test

3. **Per-packet overhead**
   - Each arriving packet does O(log m) delete from NAK btree
   - But this replaces the need to re-scan for that gap
   - Net win if m << n (missing << total packets)

4. **Window boundary edge cases**
   - Need to handle packets arriving at window boundaries
   - Sequence wraparound affects window calculations

### When to Use

| Scenario | Recommendation |
|----------|----------------|
| Low loss rate (<1%) | NAK btree wins (small btree, fast NAK gen) |
| High loss rate (>10%) | Similar performance (many gaps either way) |
| High packet rate | NAK btree wins (reduced scan overhead) |
| Low packet rate | Either approach works (small buffers) |

### Configuration Parameters (Complete)

```go
type NAKConfig struct {
    // Window sizes (time-based, converted to packets dynamically)
    BehindWindowMs      uint64  // How far back to track (default: 200ms)
    AcceptableWindowMs  uint64  // Recent OOO tolerance (default: 100ms)

    // Consolidation
    MergeGapMs          uint64  // Max gap to merge (default: 5ms)
    ConsolidationBudget time.Duration  // Max time for consolidation (default: 1ms)

    // FastNAK
    FastNAKEnabled      bool    // Enable FastNAK optimization (default: true)
    FastNAKThresholdMs  uint64  // Silent period trigger (default: 50ms)

    // Sorting
    SortByUrgency       bool    // Oldest first in NAK packet (default: true)
}
```

**Window sizing** (dynamically calculated from rate):
- `BehindWindowMs = 200ms`: Packets older than this are "very late"
- `AcceptableWindowMs = 100ms`: Recent packets still arriving OOO from io_uring
- `ScanWindow = 100ms`: Only scan this range per tick

**At 5 Mbps** (~429 pkts/sec):
- BehindWindow ≈ 86 packets
- AcceptableWindow ≈ 43 packets
- ScanWindow ≈ 43 packets per tick

**At 20 Mbps** (~1716 pkts/sec):
- BehindWindow ≈ 344 packets
- AcceptableWindow ≈ 172 packets
- ScanWindow ≈ 172 packets per tick

---

### FastNAK Optimization

#### Problem

After a Starlink reconnection event (~60ms outage, 4 times per minute), we may have built up many missing sequences in the NAK btree. If the periodic NAK timer just ran before packets resume, we'd wait up to 20ms before sending the NAK. This delays recovery.

#### Solution: FastNAK Trigger

When packets resume after a gap, trigger NAK immediately instead of waiting for the periodic timer.

**Two approaches considered**:

| Approach | Pros | Cons |
|----------|------|------|
| **Track max() at last NAK** | Precise packet-count trigger | More complex state |
| **Track time since last packet** | Simpler, time-based | May trigger on normal jitter |

**Recommendation**: Time-based approach is simpler and sufficient.

#### Go-Idiomatic Design: Use `time.Time`

**Design Decision**: Use `time.Time` instead of `uint64` for timestamps.

| Aspect | `uint64` (microseconds) | `time.Time` (Go idiomatic) |
|--------|-------------------------|---------------------------|
| Readability | Less clear | More readable, self-documenting |
| Arithmetic | Manual: `now - last > threshold*1000` | Natural: `time.Since(last) > threshold` |
| Precision | Microseconds | Nanoseconds (more precise) |
| Standard | Custom | Go standard library |

```go
type receiver struct {
    // ... existing fields ...
    lastPacketArrivalTime atomic.Value  // stores time.Time (atomic for lock-free access)
    lastNAKTime           atomic.Value  // stores time.Time
    fastNAKThreshold      time.Duration // default: 50ms
    fastNAKEnabled        bool
}
```

#### Atomic Updates: Where to Update `lastPacketArrivalTime`

**Option A**: Update in `receiver.Push(pkt)`
- Pro: Clear, single location
- Con: Requires lock to be held

**Option B**: Update atomically in `metrics.IncrementRecvMetrics()` when `m.PktRecvSuccess.Add(1)`
- Pro: Already in hot path, uses atomics (consistent with gosrt design)
- Con: Couples metrics with NAK logic

**Recommendation**: Option B - Update atomically alongside success counter

```go
// In metrics/packet_classifier.go or receiver
func IncrementRecvMetrics(m *Metrics, pkt packet.Packet, success bool, ...) {
    if success {
        m.PktRecvSuccess.Add(1)
        // Atomically update last packet arrival time
        m.LastPacketArrivalTime.Store(time.Now())  // atomic.Value
    }
}
```

**FastNAK Check**:
```go
func (r *receiver) checkFastNAK() bool {
    if !r.nakConfig.FastNAKEnabled {
        return false
    }

    lastArrival, ok := r.metrics.LastPacketArrivalTime.Load().(time.Time)
    if !ok || lastArrival.IsZero() {
        return false
    }

    return time.Since(lastArrival) > r.nakConfig.FastNAKThreshold
}
```

#### FastNAK Timing

```
Timeline during Starlink outage:

0ms   : Last packet received, lastPacketArrivalTime set
20ms  : Periodic NAK fires (nothing to NAK yet)
40ms  : Periodic NAK fires (still waiting)
60ms  : Outage ends, first packet arrives
        → time.Since(lastArrival) = 60ms > threshold (50ms)
        → FastNAK triggers immediately!

Without FastNAK: Next NAK at 80ms (periodic timer)
With FastNAK: NAK at 60ms (saves up to 20ms!)
```

#### FastNAK and Consolidation Caching

**Future Optimization**: Since no packets arrive during an outage, the consolidation result from the last periodic NAK is still valid. We could cache it:

```go
type receiver struct {
    // ... existing ...
    cachedNAKList         []circular.Number
    cachedNAKListValid    bool
    cachedNAKListMaxSeen  circular.Number
}

func (r *receiver) invalidateNAKCache() {
    r.cachedNAKListValid = false
}

// Called on packet arrival
func (r *receiver) pushLocked(pkt) {
    r.invalidateNAKCache()  // New packet invalidates cache
    // ...
}

// In periodicNAK, check cache first
func (r *receiver) periodicNAKLocked(now uint64) []circular.Number {
    if r.cachedNAKListValid && r.maxSeen == r.cachedNAKListMaxSeen {
        return r.cachedNAKList  // Reuse cached result!
    }
    // ... full consolidation ...
    r.cachedNAKList = result
    r.cachedNAKListValid = true
    r.cachedNAKListMaxSeen = r.maxSeen
    return result
}
```

This is a future optimization - not required for initial implementation.

---

### Dynamic Window Sizing

#### Problem

Window sizes (behindWindow, acceptableWindow) are in packet counts, but optimal sizes depend on data rate:
- At 5 Mbps: 100 packets ≈ 210ms
- At 20 Mbps: 100 packets ≈ 52ms

We want time-equivalent windows regardless of data rate.

#### Existing SRT Buffer vs Our Proposed NAK Window

**IMPORTANT**: There are TWO separate concepts:

1. **SRT Receive Buffer (TSBPD)** - EXISTING, time-based, controls packet DELIVERY
2. **NAK Window (behindWindow)** - OUR PROPOSAL, for gap DETECTION only

These are independent systems!

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                        SRT RECEIVE BUFFER (TSBPD - Time Based)                       │
│                                                                                      │
│  Packets stored in btree/list, released when: now >= packet.PktTsbpdTime            │
│  Buffer size = RecvLatency (e.g., 3 seconds) - NOT packet count limited!            │
│  Packets are NEVER dropped due to "buffer full" - only when TSBPD time passes       │
│                                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────────────┐   │
│  │ seq=100 │ seq=101 │ seq=103 │ seq=105 │ ... │ seq=500 │ seq=501 │ seq=502 │   │   │
│  │ (old)   │         │         │         │     │         │         │ (new)   │   │   │
│  └──────────────────────────────────────────────────────────────────────────────┘   │
│       ↑                                                                     ↑        │
│  TSBPD releases                                                       Packets       │
│  when time arrives                                                    arriving      │
└─────────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────────┐
│                      NAK WINDOW (OUR PROPOSAL - Packet Count Based)                  │
│                                                                                      │
│  Only for deciding WHICH GAPS TO NAK - does NOT affect packet storage!              │
│                                                                                      │
│          ← behindWindow (e.g., 100 packets) →                      maxSeen          │
│  ┌────────────────────────────────────────────────────────────────────┬─────┐       │
│  │  Scan this range for gaps to NAK                                   │ 502 │       │
│  │  (packets 402-502)                                                 │     │       │
│  └────────────────────────────────────────────────────────────────────┴─────┘       │
│                                                                                      │
│  This is just: "How far back from maxSeen do we look for missing sequences?"        │
│  It does NOT drop packets. It does NOT affect the SRT buffer.                        │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

#### How Existing SRT Buffer Works

**Source Files**:
| File | Function | Purpose |
|------|----------|---------|
| `connection.go:927` | `handlePacket()` | Calculates and sets `PktTsbpdTime` for each packet |
| `congestion/live/receive.go:320` | `pushLocked()` | Stores packets in btree/list |
| `congestion/live/receive.go:534` | `Tick()` | Releases packets when TSBPD time arrives |

##### How PktTsbpdTime Gets Populated

When a packet arrives, `connection.go:handlePacket()` calculates its delivery time:

```go
// connection.go:927 in handlePacket()
header.PktTsbpdTime = c.tsbpdTimeBase + tsbpdTimeBaseOffset + uint64(header.Timestamp) + c.tsbpdDelay + c.tsbpdDrift
```

**Components**:

| Component | Source | Description |
|-----------|--------|-------------|
| `tsbpdTimeBase` | Handshake | Synchronized time base between sender/receiver |
| `tsbpdTimeBaseOffset` | Wrap handling | Adjusts for 32-bit timestamp wraparound |
| `header.Timestamp` | Packet | Sender's timestamp when packet was created |
| `tsbpdDelay` | Config | `RecvLatency` (e.g., 3000ms = 3,000,000 µs) |
| `tsbpdDrift` | Drift tracker | Corrects for clock drift between sender/receiver |

**Example calculation**:
```
tsbpdTimeBase       = 1000000000  (connection start time in µs)
tsbpdTimeBaseOffset = 0          (no wrap yet)
header.Timestamp    = 5000000    (5 seconds into stream)
tsbpdDelay          = 3000000    (3 second latency buffer)
tsbpdDrift          = 100        (small clock correction)

PktTsbpdTime = 1000000000 + 0 + 5000000 + 3000000 + 100
             = 1008000100 µs

This packet should be delivered at time 1008000100 µs (8 seconds after connection start)
```

##### Timestamp Wraparound Handling

The packet's `Timestamp` field is 32-bit (max ~71 minutes). `handlePacket()` detects wraparound:

```go
// connection.go:906-925 in handlePacket()
// 4.5.1.1.  TSBPD Time Base Calculation
if !c.tsbpdWrapPeriod {
    if header.Timestamp > packet.MAX_TIMESTAMP-(30*1000000) {
        c.tsbpdWrapPeriod = true  // Approaching wrap
    }
} else {
    if header.Timestamp >= (30*1000000) && header.Timestamp <= (60*1000000) {
        c.tsbpdWrapPeriod = false
        c.tsbpdTimeBaseOffset += uint64(packet.MAX_TIMESTAMP) + 1  // Add full wrap
    }
}
```

##### Packet Storage and Release

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                         PACKET FLOW THROUGH SRT BUFFER                               │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                      │
│  1. PACKET ARRIVES                                                                   │
│     └── connection.go:handlePacket()                                                 │
│         └── Calculate: PktTsbpdTime = tsbpdTimeBase + Timestamp + tsbpdDelay + ...  │
│         └── Call: c.recv.Push(p)                                                     │
│                                                                                      │
│  2. PACKET STORED                                                                    │
│     └── congestion/live/receive.go:pushLocked()                                      │
│         └── r.packetStore.Insert(pkt)  // btree or linked list                      │
│         └── Packet now in buffer, waiting for TSBPD time                            │
│                                                                                      │
│  3. TICK (every ~10ms)                                                               │
│     └── congestion/live/receive.go:Tick()                                            │
│         └── For each packet in store:                                                │
│             └── if PktTsbpdTime <= now AND seq <= lastACKSequence:                  │
│                 └── r.deliver(p)  // Send to application                             │
│                 └── Remove from packetStore                                          │
│                                                                                      │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

**Packet Release (TSBPD)**:
```go
// congestion/live/receive.go:534-549 in Tick()
removed := r.packetStore.RemoveAll(
    func(p packet.Packet) bool {
        h := p.Header()
        // Release when: ACK'd AND TSBPD time has passed
        return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
    },
    func(p packet.Packet) {
        r.deliver(p)  // Deliver to application
    },
)
```

**Key insight**: SRT buffer is TIME-BASED, not packet-count limited!
- `RecvLatency = 3000ms` means packets wait up to 3 seconds
- Buffer can hold ANY number of packets
- Packets are released based on timestamp, not count
- `PktTsbpdTime` is calculated ONCE when packet arrives, then used for release decision

#### How Rate Statistics Are Calculated

**Source Files**:
| File | Function | Purpose |
|------|----------|---------|
| `congestion/live/receive.go:98` | `NewReceiver()` | Initializes `avgPayloadSize = 1456` |
| `congestion/live/receive.go:118` | `NewReceiver()` | Sets `rate.period = 1 second` |
| `congestion/live/receive.go:253` | `pushLocked()` | Updates `avgPayloadSize` per packet |
| `congestion/live/receive.go:589` | `updateRateStats()` | Calculates `bytesPerSecond` |

**Why 1 second update frequency?**:
```go
// congestion/live/receive.go:118 in NewReceiver()
r.rate.period = uint64(time.Second.Microseconds())  // 1,000,000 µs = 1 second

// congestion/live/receive.go:592 in updateRateStats()
if tdiff > r.rate.period {  // Only update when > 1 second elapsed
    r.rate.bytesPerSecond = float64(r.rate.bytes) / (float64(tdiff) / 1000 / 1000)
    // ... reset counters, update r.rate.last ...
}
```

The `rate.period` is hardcoded to 1 second. `updateRateStats()` is called every tick (~10ms), but only recalculates when 1 second has passed since last calculation.

#### Our Proposed NAK Window: Does NOT Drop Packets!

**Critical clarification**: `behindWindow` does NOT affect the SRT buffer at all!

```go
// Our proposal - NAK gap detection only:
behindWindow = 100  // Look at last 100 packets from maxSeen

// This means:
// - Scan packets from (maxSeen - 100) to maxSeen for gaps
// - NAK for missing sequences in that range
// - Packets OUTSIDE this range are still in the SRT buffer!
// - They just don't get NAK'd (assumed too late for retransmission to help)
```

**What happens if bitrate decreases?**

```
Before: maxSeen=500, bytesPerSecond=10Mbps, behindWindow=172 packets
        NAK scanning range: [328, 500]

After:  maxSeen=550, bytesPerSecond=5Mbps, behindWindow=86 packets
        NAK scanning range: [464, 550]

Packets 328-463 are:
  - Still in the SRT buffer? YES! (until TSBPD releases them)
  - Still delivered to app? YES! (when TSBPD time arrives)
  - Still getting NAK'd? NO - we stopped scanning them
  - Dropped? NO!
```

**Why stop scanning them?**
- If a packet is that old and still missing, retransmission probably won't arrive in time
- TSBPD will release/skip it soon anyway
- Better to focus NAK resources on more recent gaps

#### Interaction: NAK Window vs SRT Buffer

```
┌────────────────────────────────────────────────────────────────────────────────────┐
│                              TIMELINE OF A PACKET                                   │
├────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                     │
│  1. Packet 100 SENT by sender at T=0                                               │
│                                                                                     │
│  2. Packet 100 MISSING (detected as gap when 101+ arrive)                          │
│     → Added to NAK btree                                                           │
│     → NAK sent to request retransmission                                           │
│                                                                                     │
│  3. Time passes... maxSeen advances to 200                                         │
│     behindWindow=100, so scanning [100, 200]                                       │
│     → Packet 100 still in NAK btree, still getting NAK'd                           │
│                                                                                     │
│  4. maxSeen advances to 250                                                         │
│     behindWindow=100, so scanning [150, 250]                                       │
│     → Packet 100 falls off NAK window                                              │
│     → Removed from NAK btree (no more NAKs for it)                                 │
│     → But packet 100's SLOT is still in SRT buffer! (waiting for TSBPD)            │
│                                                                                     │
│  5. TSBPD time arrives for packet 100's slot                                       │
│     → SRT checks: do we have packet 100? NO                                        │
│     → Gap skipped, next packet delivered                                           │
│     → Application sees gap (or handles it)                                         │
│                                                                                     │
└────────────────────────────────────────────────────────────────────────────────────┘
```

**Summary**:
- **SRT Buffer (TSBPD)**: Holds packets, releases by TIME, never drops due to count
- **NAK Window**: Defines scan range for gap detection, does NOT affect storage
- **behindWindow shrinking**: Only affects which gaps we NAK, not packet storage

#### Window Sizing Strategy

Two approaches are considered:

1. **Rate-based windows** (original proposal) - Calculate packet counts from bitrate
2. **TSBPD-based scanning** (alternative proposal) - Use packet TSBPD timestamps directly

---

### Alternative: TSBPD-Based Scan Window

#### Insight

Since the packet btree stores packets in sequence order, and each packet has a `PktTsbpdTime`, we can use TSBPD directly to determine the scan boundary instead of calculating packet counts from bitrate.

#### Algorithm Overview

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              PACKET BTREE (sequence order)                          │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                     │
│  NAKScanStartPoint                                            Stop scanning here    │
│        ↓                                                              ↓             │
│  ┌─────────────────────────────────────────────────────────────┬─────────────────┐  │
│  │ seq=100    seq=101    seq=105    seq=106    ...    seq=500  │  seq=501  502   │  │
│  │ TSBPD:     TSBPD:     TSBPD:     TSBPD:            TSBPD:   │  TSBPD:   ...   │  │
│  │ T+0ms      T+2ms      T+10ms     T+12ms            T+2700ms │  T+2702ms       │  │
│  │                                                             │                 │  │
│  │ ← ─ ─ ─ ─ ─ ─ SCAN THIS RANGE FOR GAPS ─ ─ ─ ─ ─ ─ ─ ─ ─ →  │← TOO RECENT   → │  │
│  │              (TSBPD < now + 90% of tsbpdDelay)              │ (within 10%)    │  │
│  └─────────────────────────────────────────────────────────────┴─────────────────┘  │
│                                                                                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

#### Lifecycle Walkthrough

**1. Connection Establishment (before first periodicNAK)**

```go
// When first packet arrives
func (r *receiver) pushLocked(pkt) {
    seq := pkt.Header().PacketSequenceNumber

    // Initialize NAKScanStartPoint with first packet's sequence
    if r.nakScanStartPoint.Load() == 0 {
        r.nakScanStartPoint.Store(seq.Val())
    }

    r.packetStore.Insert(pkt)
}
```

- Packet btree is empty, NAK btree is empty
- First packet arrives, store its sequence as `NAKScanStartPoint`
- More packets arrive (hopefully sequential, but may have gaps)

**2. First periodicNAK Fires**

```go
func (r *receiver) periodicNAKLocked(now uint64) []circular.Number {
    // Calculate the TSBPD threshold for "too recent" packets
    // Packets with TSBPD > threshold are too new to NAK (might still arrive OOO)
    tooRecentThreshold := now + uint64(float64(r.tsbpdDelay) * r.nakRecentPercent)
    // e.g., now + (3000ms * 0.10) = now + 300ms

    startSeq := r.nakScanStartPoint.Load()
    var lastScannedSeq uint32

    r.packetStore.AscendGreaterOrEqual(startSeq, func(pkt) bool {
        h := pkt.Header()

        // Stop if this packet is "too recent"
        if h.PktTsbpdTime > tooRecentThreshold {
            return false  // Stop iteration
        }

        // Gap detection logic (same as before)
        // ...

        lastScannedSeq = h.PacketSequenceNumber.Val()
        return true  // Continue
    })

    // Update NAKScanStartPoint for next iteration
    if lastScannedSeq > 0 {
        r.nakScanStartPoint.Store(lastScannedSeq)
    }

    return nakList
}
```

**3. Subsequent periodicNAK Calls**

- Start from `NAKScanStartPoint` (where we left off)
- Scan forward, checking TSBPD on each packet
- Stop when packet's TSBPD is within "too recent" window
- Update `NAKScanStartPoint` to last scanned sequence
- Guaranteed: never skip scanning any packet

#### Configuration

```go
type NAKConfig struct {
    // TSBPD-based scanning
    NAKRecentPercent float64  // Percentage of tsbpdDelay considered "too recent"
                              // Default: 0.10 (10%)
                              // Range: 0.05 (aggressive) to 0.30 (relaxed)
}
```

**Examples**:

| tsbpdDelay | NAKRecentPercent | "Too Recent" Window | Meaning |
|------------|------------------|---------------------|---------|
| 3000ms | 10% | 300ms | Don't NAK packets arriving in last 300ms |
| 3000ms | 5% | 150ms | More aggressive: NAK sooner |
| 3000ms | 20% | 600ms | More relaxed: wait longer before NAK |
| 200ms | 10% | 20ms | Short latency: 20ms window |

#### Analysis

**Pros**:

| Advantage | Explanation |
|-----------|-------------|
| **Directly tied to TSBPD** | Uses the same timing mechanism as packet delivery |
| **Self-adjusting** | Automatically adapts to configured latency |
| **No rate calculations** | Doesn't need `avgPayloadSize` or `bytesPerSecond` |
| **No rate update lag** | No 1-second delay waiting for rate stats |
| **Guaranteed coverage** | Always scan from `NAKScanStartPoint` forward - never miss a packet |
| **Simple configuration** | Just one percentage to tune |
| **Handles variable bitrate** | Works regardless of packet rate fluctuations |

**Cons**:

| Disadvantage | Explanation |
|--------------|-------------|
| **TSBPD check per packet** | Must read `PktTsbpdTime` for each packet during iteration |
| **Fixed percentage** | 10% of 3s = 300ms, but 10% of 200ms = 20ms (very different!) |
| **Doesn't account for RTT** | "Too recent" window doesn't consider network round-trip time |
| **First packet edge case** | Need to handle initialization of `NAKScanStartPoint` |
| **Still scans packet btree** | Iterates through packets (though avoids recalculating rates) |

**Comparison with Rate-Based Windows**:

| Aspect | Rate-Based (Original) | TSBPD-Based (Alternative) |
|--------|----------------------|---------------------------|
| Window calculation | `packetsPerMs * windowMs` | `TSBPD < now + threshold` |
| Depends on | `bytesPerSecond`, `avgPayloadSize` | `PktTsbpdTime`, `tsbpdDelay` |
| Update frequency | Every 1 second (rate stats) | Every packet (TSBPD is per-packet) |
| Handles rate changes | 1s lag, needs shrink safety | Instant, inherent in TSBPD |
| Configuration | `BehindWindowMs`, `AcceptableWindowMs` | `NAKRecentPercent` |
| Complexity | Higher (rate calculation, window safety) | Lower (just percentage) |

#### Recommendation

**Include TSBPD-based scanning as the PRIMARY approach** for these reasons:

1. **Simpler**: One configuration parameter vs multiple window sizes
2. **More accurate**: Uses actual packet timing, not estimated rates
3. **No lag**: Doesn't wait for rate statistics to update
4. **Guaranteed coverage**: `NAKScanStartPoint` ensures no packets are ever skipped

**However, consider hybrid approach**:
- Use TSBPD-based "too recent" threshold for the scan STOP condition
- Use `NAKScanStartPoint` for the scan START condition
- This gives us the benefits of both:
  - Never miss scanning a packet (NAKScanStartPoint)
  - Don't NAK packets that might still arrive (TSBPD threshold)

#### Implementation Notes

```go
type receiver struct {
    // ... existing fields ...

    // NAK scanning state
    nakScanStartPoint atomic.Uint32    // Sequence number to start scanning from
    nakRecentPercent  float64          // Config: percentage of tsbpdDelay (default: 0.10)
    tsbpdDelay        uint64           // Cached from connection config (microseconds)
}

// Called on first packet
func (r *receiver) initNAKScanStartPoint(seq circular.Number) {
    // CompareAndSwap ensures we only set it once
    r.nakScanStartPoint.CompareAndSwap(0, seq.Val())
}

// In periodicNAKLocked
func (r *receiver) calcTooRecentThreshold(now uint64) uint64 {
    return now + uint64(float64(r.tsbpdDelay) * r.nakRecentPercent)
}
```

---

### Original Proposal: Rate-Based Windows

**On startup** (before rate is known):
- Use fixed conservative defaults based on configured latency
- Assume worst case: high packet rate

**After rate stabilizes** (~2-3 seconds):
- Calculate packet rate from `avgPayloadSize` and `rate.bytesPerSecond`
- Adjust windows to maintain consistent time-based behavior

```go
type WindowCalculator struct {
    latencyMs        uint64  // Configured SRT latency (e.g., 3000ms)
    avgPayloadSize   float64 // From receiver (smoothed)
    bytesPerSecond   float64 // From receiver rate stats

    // Computed values
    packetsPerMs     float64

    // Target windows in milliseconds
    behindWindowMs      uint64  // e.g., 200ms (packets we're willing to wait for)
    acceptableWindowMs  uint64  // e.g., 100ms (recent OOO tolerance)

    // Previous values for shrinking safety
    prevBehindWindow    uint32
    prevAcceptableWindow uint32
}

func (wc *WindowCalculator) Update(avgPayloadSize, bytesPerSecond float64) {
    if bytesPerSecond <= 0 || avgPayloadSize <= 0 {
        return // Keep defaults
    }

    wc.avgPayloadSize = avgPayloadSize
    wc.bytesPerSecond = bytesPerSecond
    wc.packetsPerMs = bytesPerSecond / avgPayloadSize / 1000.0
}

func (wc *WindowCalculator) BehindWindow() uint32 {
    if wc.packetsPerMs <= 0 {
        return 100 // Default: 100 packets
    }
    return uint32(float64(wc.behindWindowMs) * wc.packetsPerMs)
}

func (wc *WindowCalculator) AcceptableWindow() uint32 {
    if wc.packetsPerMs <= 0 {
        return 50 // Default: 50 packets
    }
    return uint32(float64(wc.acceptableWindowMs) * wc.packetsPerMs)
}
```

#### Window Shrinking Safety

**Problem**: If windows shrink (due to rate decrease), we might skip scanning part of the packet btree, leaving gaps undetected forever.

**Example**:
```
Tick N:   behindWindow=100, acceptableWindow=50, scanRange=[400,450]
          (Rate drops)
Tick N+1: behindWindow=80, acceptableWindow=40, scanRange=[420,460]
          → Range [400,420] was NEVER scanned!
```

**Solution**: When windows shrink, scan the LARGER (previous) range first, then update:

```go
func (r *receiver) periodicNAKLocked(now uint64) []circular.Number {
    // Calculate new windows
    newBehindWindow := r.windowCalc.BehindWindow()
    newAcceptableWindow := r.windowCalc.AcceptableWindow()

    // Use MAX of current and previous to ensure no gaps missed
    effectiveBehind := max(newBehindWindow, r.windowCalc.prevBehindWindow)
    effectiveAcceptable := min(newAcceptableWindow, r.windowCalc.prevAcceptableWindow)

    scanStart := r.maxSeen.Val() - effectiveBehind
    scanEnd := r.maxSeen.Val() - effectiveAcceptable

    // Scan with safe range
    r.scanWindowForGaps(scanStart, scanEnd)

    // Update previous values for next tick
    r.windowCalc.prevBehindWindow = newBehindWindow
    r.windowCalc.prevAcceptableWindow = newAcceptableWindow

    // ... rest of NAK logic
}
```

**Test requirement**: Add `_test.go` cases for:
- Window shrinking scenario (ensure all gaps detected)
- Window growing scenario
- Rate fluctuation scenario

#### Example Window Calculations

| Data Rate | avgPayloadSize | pkts/sec | pkts/ms | 200ms window | 100ms window |
|-----------|---------------|----------|---------|--------------|--------------|
| 5 Mbps | 1456 | 429 | 0.43 | 86 pkts | 43 pkts |
| 10 Mbps | 1456 | 858 | 0.86 | 172 pkts | 86 pkts |
| 20 Mbps | 1456 | 1716 | 1.72 | 344 pkts | 172 pkts |

#### Window Parameters Summary

| Parameter | Time-Based | Purpose |
|-----------|------------|---------|
| `behindWindowMs` | 200ms | Max time to wait for late packets before they're "too late" |
| `acceptableWindowMs` | 100ms | Recent OOO window (io_uring reordering tolerance) |
| `scanWindow` | 100ms | `behindWindowMs - acceptableWindowMs` (scan range per tick) |
| `fastNAKThresholdMs` | 50ms | Trigger FastNAK after this silence |
| `mergeGapMs` | 5ms | Max gap to merge (~2-4 packets at 5 Mbps) |

---

### Design Decision: v1 (Simple) vs v2 (NAK btree)

| Approach | Complexity | Performance | When to Use |
|----------|------------|-------------|-------------|
| **v1: Suppress immediate NAK only** | Low | Good | Quick fix, validate approach |
| **v2: NAK btree** | Medium | Better | Production, high throughput |

**Recommendation**: Start with v1 to validate the concept works, then upgrade to v2 for production.

### NAK btree Benefits Summary

```
BEFORE (scan entire primary btree every 20ms):
┌─────────────────────────────────────────────────────────────────────────┐
│  Scan 1400 packets → find 14 gaps → build NAK → repeat next tick        │
│  Redundant: re-discovers same gaps every tick                            │
└─────────────────────────────────────────────────────────────────────────┘

AFTER (NAK btree with windowed scan):
┌─────────────────────────────────────────────────────────────────────────┐
│  Scan 50 packets (window) → add NEW gaps to NAK btree                   │
│  Traverse NAK btree (14 entries) → build NAK                            │
│  Remove arrived packets from NAK btree                                  │
│  Efficient: only detect gaps once, track in small btree                 │
└─────────────────────────────────────────────────────────────────────────┘
```

---

---

### Library Consideration: golang-set and Generics

**Question**: Should we use a library like [golang-set](https://github.com/deckarep/golang-set) for range operations?

**Analysis**:

| Aspect | Custom btree | golang-set |
|--------|--------------|------------|
| Range operations | Manual iteration | Built-in set operations |
| Memory | btree nodes | Map-based |
| Generics | Can use | Uses generics |
| Dependencies | Google btree (already used) | New dependency |
| Performance | O(log n) operations | O(1) set membership |

**Recommendation**: Keep using Google btree for consistency with existing codebase, but use **generics for sequence math**:

```go
// Generic sequence operations (inspired by goTrackRTP)
// Handles wraparound for SRT's 31-bit sequence numbers

const MaxSeqNumber uint32 = 0x7FFFFFFF  // 2^31 - 1

// SeqLess returns true if s1 < s2, handling wraparound
func SeqLess[T ~uint32](s1, s2 T) bool {
    diff := int64(s1) - int64(s2)
    diff += int64(MaxSeqNumber/2) + 1
    diff &= int64(MaxSeqNumber)
    return diff > 0 && diff <= int64(MaxSeqNumber/2)
}

// SeqDiff returns |s1 - s2| handling wraparound
func SeqDiff[T ~uint32](s1, s2 T) T {
    if s1 == s2 {
        return 0
    }
    var abs T
    if s1 < s2 {
        abs = s2 - s1
    } else {
        abs = s1 - s2
    }
    if abs > MaxSeqNumber/2 {
        return MaxSeqNumber - abs + 1
    }
    return abs
}
```

**File**: `circular/seq_math.go` (new file with comprehensive tests)

---

### Sequence Number Wraparound

**SRT Sequence Numbers**: 31-bit (0 to 2^31-1 = 2,147,483,647)

**At 20 Mbps** (~1716 pkts/sec): Wraps every ~14.5 days (not a concern)

**But we must handle it correctly** in:
1. Window calculations (`maxSeen - behindWindow`)
2. btree comparisons
3. Gap detection

**Reference**: goTrackRTP's `trackRTP_math.go` (now in documentation/) provides tested implementations for uint16. We adapt for uint32:

```go
// From goTrackRTP, adapted for uint32
func isLessBranchless(s1, s2 uint32) bool {
    const halfMax = MaxSeqNumber / 2
    diff := int64(s1) - int64(s2)
    diff += int64(halfMax) + 1
    diff &= int64(MaxSeqNumber)
    return diff > 0 && diff <= int64(halfMax)
}
```

**Test Requirements** (from goTrackRTP pattern):
- Obvious cases: 0 < 1, 100 < 101
- Wraparound: 0 > MaxSeqNumber (0 is "ahead" of max)
- Edge cases: MaxSeqNumber/2 boundary

---

### Multiple NAK Packets

**Problem**: What if consolidated ranges exceed MSS?

**NAK Entry Sizes**:
- Single: 4 bytes (|0|seq|)
- Range: 8 bytes (|1|start| + |0|end|)

**MSS**: ~1500 bytes, Header: ~16 bytes
**Max payload**: ~1484 bytes
**Max entries**: ~370 singles or ~185 ranges

**Usually sufficient**, but during major outages (>1 second), we might exceed.

**Solution**: Generate multiple NAK packets:

```go
func (r *receiver) buildNAKPackets(entries []NAKEntry) [][]circular.Number {
    const headerSize = 16
    const singleSize = 4
    const rangeSize = 8
    maxPayload := r.mss - headerSize

    var packets [][]circular.Number
    var current []circular.Number
    currentSize := 0

    for _, entry := range entries {
        entrySize := singleSize
        if entry.IsRange {
            entrySize = rangeSize
        }

        if currentSize + entrySize > maxPayload {
            // Current packet full, start new one
            packets = append(packets, current)
            current = nil
            currentSize = 0
        }

        if entry.IsRange {
            current = append(current, entry.Start, entry.End)
        } else {
            current = append(current, entry.Start, entry.Start)  // Single: start==end
        }
        currentSize += entrySize
    }

    if len(current) > 0 {
        packets = append(packets, current)
    }

    return packets
}
```

**Test Requirements**:
- Single packet (normal case)
- Exactly at MSS limit
- 2 packets needed
- 5 packets needed (stress test)
- Off-by-one at boundaries

---

### Sender Retransmission Order: Oldest First

**Current behavior**: Sender iterates `lossList.Back()` → `Front()` (newest first)

**Desired behavior**: Oldest first (most urgent, closest to TSBPD deadline)

**Why oldest first is better**:
- Oldest packets have least time before TSBPD release
- If we retransmit oldest first, highest chance of recovery
- NAK packet already orders by urgency (oldest first), sender should honor it

**Implementation**: New function behind feature flag

```go
type SenderConfig struct {
    // ... existing ...
    RetransmitOldestFirst bool  // Feature flag (default: false initially)
}

// New function that retransmits in NAK order (oldest first)
func (s *sender) nakLockedOldestFirst(sequenceNumbers []circular.Number) uint64 {
    // ... metrics counting (same as nakLocked) ...

    retransCount := uint64(0)

    // Iterate NAK entries in order (already oldest first from receiver)
    for i := 0; i < len(sequenceNumbers); i += 2 {
        start := sequenceNumbers[i]
        end := sequenceNumbers[i+1]

        // Find and retransmit packets in this range
        for e := s.lossList.Front(); e != nil; e = e.Next() {  // Front = oldest
            p := e.Value.(packet.Packet)
            seq := p.Header().PacketSequenceNumber

            if seq.Gte(start) && seq.Lte(end) {
                // Retransmit
                s.deliver(p)
                retransCount++
            }

            if seq.Gt(end) {
                break  // Past this range, move to next NAK entry
            }
        }
    }

    return retransCount
}
```

**Test Requirements**:
- Verify oldest packets retransmitted first
- Compare recovery time vs newest-first
- Ensure no packets missed

---

### Complete Feature Summary

| Feature | Description | Config Flag | Default |
|---------|-------------|-------------|---------|
| **NAK btree** | Separate btree for missing sequences | `UseNakBtree` | true (when io_uring) |
| **Singles storage** | Store individual seqs, not ranges | (design choice) | N/A |
| **Separate NAK lock** | Separate RWMutex for NAK btree | (design choice) | N/A |
| **Windowed scan** | Only scan a range, not entire buffer | `BehindWindowMs`, `AcceptableWindowMs` | 200ms, 100ms |
| **Window shrink safety** | Scan larger range when windows shrink | (automatic) | N/A |
| **RTT-based expiration** | Expire NAKs earlier accounting for RTT | (automatic) | N/A |
| **Range consolidation** | Merge singles into ranges at NAK time | `MergeGapMs` | 5ms |
| **Time-budgeted consolidation** | Limit consolidation time | `ConsolidationBudget` | 2ms |
| **Urgency ordering** | Oldest (most urgent) first in NAK | `SortByUrgency` | true |
| **FastNAK** | Immediate NAK after long silence | `FastNAKEnabled`, `FastNAKThreshold` | true, 50ms |
| **Dynamic windows** | Adjust packet windows based on rate | (automatic) | N/A |
| **Suppress immediate NAK** | No immediate NAK for io_uring | `SuppressImmediateNAK` | true (when io_uring) |
| **Multiple NAK packets** | Handle overflow when entries > MSS | (automatic) | N/A |
| **Sequence wraparound** | Generic math for 31-bit sequences | (design choice) | N/A |
| **Sender oldest-first** | Retransmit oldest packets first | `RetransmitOldestFirst` | false (feature flag) |
| **Go idiomatic time** | Use `time.Time` for timestamps | (design choice) | N/A |
| **Atomic arrival time** | Update in `IncrementRecvMetrics` | (design choice) | N/A |

### Complete Algorithm Flow

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                           PACKET ARRIVAL (Push)                                      │
├─────────────────────────────────────────────────────────────────────────────────────┤
│  1. FastNAK check: if (now - lastPacketArrival > 50ms) → triggerFastNAK()           │
│  2. Update lastPacketArrival = now                                                  │
│  3. Insert into primary packet btree                                                 │
│  4. Delete from NAK btree (packet arrived, no longer missing)                       │
│  5. Update maxSeen if needed                                                         │
│  6. NO immediate NAK (suppressed for io_uring)                                      │
└─────────────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼ every 20ms
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                           PERIODIC NAK                                               │
├─────────────────────────────────────────────────────────────────────────────────────┤
│  1. Calculate windows (dynamic based on rate):                                       │
│     - behindWindow = packetsPerMs * BehindWindowMs                                  │
│     - acceptableWindow = packetsPerMs * AcceptableWindowMs                          │
│     - scanStart = maxSeen - behindWindow                                            │
│     - scanEnd = maxSeen - acceptableWindow                                          │
│                                                                                      │
│  2. Scan primary btree [scanStart, scanEnd] for gaps:                               │
│     - For each gap, insert sequence into NAK btree                                  │
│                                                                                      │
│  3. Remove expired sequences from NAK btree:                                        │
│     - DeleteBefore(maxSeen - behindWindow)                                          │
│                                                                                      │
│  4. Consolidate NAK btree into ranges (time-budgeted):                              │
│     - Traverse DESCENDING (oldest first)                                            │
│     - Merge adjacent sequences (accepting duplicates within mergeGap)               │
│     - Stop if consolidation budget exceeded                                         │
│                                                                                      │
│  5. Build and send NAK packet:                                                      │
│     - Ranges in urgency order (oldest first)                                        │
│     - Respect MSS limit                                                             │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

---

## ~~Refined Design v1: Periodic NAK with Urgency-Based Ordering~~

(Superseded by v2 above)

---

## Original Refined Design: Periodic NAK with Urgency-Based Ordering

### Conceptual Model

The existing btree already stores received packets. Missing packets have an **urgency gradient** based on their distance from TSBPD release:

```
                        ← Behind Window (buffer size) →                                    Max()
┌────────────────────────────┬────────────────────────────┬────────────────────────────────┬──────┐
│  VERY URGENT               │  MODERATELY URGENT         │  ACCEPTABLE LATE               │  600 │
│  Close to TSBPD release    │  Some time remaining       │  Just arrived out of order     │      │
│  seq 500-520               │  seq 521-560               │  seq 561-599                   │      │
└────────────────────────────┴────────────────────────────┴────────────────────────────────┴──────┘
        ↑                                                                                    ↑
   TSBPD will release                                                          Packets arriving
   these packets soon!                                                         with small gaps
   NAK these FIRST
```

### Key Insight: Reuse Existing btree

We don't need a separate tracking structure! The existing `btreePacketStore` already holds received packets in sequence order. To find missing packets:

1. **Traverse btree** from oldest to newest
2. **Detect gaps** where expected sequence != actual sequence
3. **Merge adjacent ranges** (accepting potential duplicates)
4. **Order by urgency** (oldest/most urgent first)

### Periodic NAK Algorithm

```go
func (r *liveRecv) generatePeriodicNAK() {
    // Step 1: Traverse btree to find missing sequences
    missing := r.findMissingSequences()

    // Step 2: Merge into ranges (with acceptable duplicates)
    ranges := r.mergeIntoRanges(missing)

    // Step 3: Sort by urgency (oldest first - already in order from btree!)
    // No additional sort needed - btree traversal is naturally oldest→newest

    // Step 4: Pack into NAK packet (respecting MSS limit)
    nakPacket := r.packNAK(ranges)

    // Step 5: Send
    r.sendNAK(nakPacket)
}
```

### Step-by-Step Details

#### Step 1: Find Missing Sequences

Traverse btree and detect gaps:

```go
func (r *liveRecv) findMissingSequences() []uint32 {
    var missing []uint32
    expectedSeq := r.oldestBufferedSeq

    r.packetStore.Ascend(func(pkt *packet.Packet) bool {
        actualSeq := pkt.Header().PacketSequenceNumber.Val()

        // Gap detected
        for expectedSeq < actualSeq {
            missing = append(missing, expectedSeq)
            expectedSeq++
        }
        expectedSeq = actualSeq + 1
        return true // continue iteration
    })

    return missing
}
```

#### Step 2: Merge into Ranges

Convert individual missing sequences into efficient ranges. **Accept duplicates** when gaps are small:

```go
type NAKEntry struct {
    IsRange bool
    Start   uint32
    End     uint32  // Only used if IsRange
}

func (r *liveRecv) mergeIntoRanges(missing []uint32) []NAKEntry {
    if len(missing) == 0 {
        return nil
    }

    var entries []NAKEntry
    rangeStart := missing[0]
    rangeEnd := missing[0]

    for i := 1; i < len(missing); i++ {
        seq := missing[i]

        // Gap in missing sequences
        if seq > rangeEnd + 1 {
            // Should we merge across the gap?
            gapSize := seq - rangeEnd - 1

            if gapSize <= maxMergeGap {  // e.g., 3 packets
                // Merge: accept duplicates for small gaps
                // Example: missing 5,6,8,9,10 → range 5-10 (7 is duplicate)
                rangeEnd = seq
            } else {
                // Gap too large - emit current range, start new
                entries = append(entries, makeEntry(rangeStart, rangeEnd))
                rangeStart = seq
                rangeEnd = seq
            }
        } else {
            // Contiguous
            rangeEnd = seq
        }
    }

    // Emit final range
    entries = append(entries, makeEntry(rangeStart, rangeEnd))

    return entries
}

func makeEntry(start, end uint32) NAKEntry {
    if start == end {
        return NAKEntry{IsRange: false, Start: start}
    }
    return NAKEntry{IsRange: true, Start: start, End: end}
}
```

**Example**:
```
Missing: 5, 6, 8, 9, 10, 15, 16, 17
With maxMergeGap = 3:
  - 5, 6, [gap=1], 8, 9, 10 → merge to range 5-10 (packet 7 is duplicate - OK)
  - [gap=4], 15, 16, 17 → too large, new range 15-17

Result: [Range(5,10), Range(15,17)]
Instead of: [Range(5,6), Single(8), Range(9,10), Range(15,17)]
```

#### Step 3: Urgency Ordering

**Natural ordering from btree traversal!**

Since we traverse the btree from oldest to newest, missing sequences are naturally ordered by urgency:
- First missing = oldest = most urgent (closest to TSBPD release)
- Last missing = newest = least urgent (just arrived out of order)

**No additional sorting needed at NAK generator!**

#### Step 4: Pack into NAK Packet

Respect MSS limit when packing:

```go
const (
    NAKHeaderSize = 16  // SRT control packet header
    NAKSingleSize = 4   // Single sequence entry
    NAKRangeSize  = 8   // Range entry (start + end)
)

func (r *liveRecv) packNAK(entries []NAKEntry) *packet.Packet {
    maxPayload := r.mss - NAKHeaderSize
    var payload []byte

    for _, entry := range entries {
        entrySize := NAKSingleSize
        if entry.IsRange {
            entrySize = NAKRangeSize
        }

        if len(payload) + entrySize > maxPayload {
            break  // NAK packet full
        }

        if entry.IsRange {
            // |1| start | + |0| end |
            payload = append(payload, encodeRangeStart(entry.Start)...)
            payload = append(payload, encodeRangeEnd(entry.End)...)
        } else {
            // |0| seq |
            payload = append(payload, encodeSingle(entry.Start)...)
        }
    }

    return packet.NewNAKPacket(payload)
}
```

### Where Should Priority Sorting Happen?

**Question**: Should the NAK generator or the retransmitter sort by priority?

| Location | Pros | Cons |
|----------|------|------|
| **NAK Generator (Receiver)** | Sender retransmits in order received; simpler sender | Receiver already traversing btree in order |
| **Retransmitter (Sender)** | Sender can apply smarter scheduling; knows send buffer state | Adds complexity to sender; may reorder NAK entries |

**Analysis**:

The NAK packet ordering already reflects urgency (oldest first from btree traversal). The sender has two choices:

1. **Honor NAK order**: Retransmit packets in the order they appear in NAK
   - Simple
   - Respects receiver's urgency assessment

2. **Re-sort at sender**: Reorder based on sender's view
   - Could consider congestion window, RTT, etc.
   - More complex

**Recommendation**: Start with **Option 1** (sender honors NAK order). This is simpler and the receiver's urgency assessment is valid. We can add sender-side sorting later if needed.

### Key Finding: SRTO_LOSSMAXTTL TODO Already Exists! 🎯

The problem we're solving was **already anticipated** in the codebase!

From `congestion/live/receive.go` line 295-296:
```go
// Too far ahead, there are some missing sequence numbers, immediate NAK report.
// TODO: Implement SRTO_LOSSMAXTTL to delay NAK for reordered packets.
```

**What is SRTO_LOSSMAXTTL?**
- Standard SRT option to tolerate packet reordering
- Defines how many packets can arrive out of order before declaring loss
- Our sliding window approach is essentially implementing this!

### ✅ Sender Already Handles Multi-Entry NAK

Verified in `congestion/live/send.go::nakLocked()`:

```go
// NAK format: [start1, end1, start2, end2, ...]
for i := 0; i < len(sequenceNumbers); i += 2 {
    if p.Header().PacketSequenceNumber.Gte(sequenceNumbers[i]) &&
       p.Header().PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {
        // retransmit packet
        s.deliver(p)
    }
}
```

**Confirmed**:
- ✅ Handles multiple `[start, end]` pairs
- ✅ Singles work (start == end)
- ✅ Ranges work (start < end)

**Note on retransmission order**:
- Currently iterates `lossList.Back()` to `Front()` (newest first)
- For urgency-based ordering, we might want to reverse this
- Or rely on NAK order (entries already oldest-first from btree traversal)

### ✅ Periodic NAK Already Does btree Traversal

From `congestion/live/receive.go::periodicNAKLocked()`:

```go
r.packetStore.Iterate(func(p packet.Packet) bool {
    h := p.Header()
    if !h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
        // Gap found!
        list = append(list, ackSequenceNumber.Inc())      // start
        list = append(list, h.PacketSequenceNumber.Dec()) // end
    }
    ackSequenceNumber = h.PacketSequenceNumber
    return true
})
```

**This is exactly what we need!** The periodic NAK already:
- Traverses the btree in sequence order
- Finds gaps (oldest first = most urgent first)
- Builds `[start, end]` pairs

**What's missing**:
- Range merging (to reduce NAK entries)
- Suppression of immediate NAK for io_uring path

---

## ~~Proposed Combined Approach~~ (Superseded)

~~Combine **Option 2 (Sliding Window)** with **Option 4 (Packed NAK)**:~~

See "Refined Design" section above.

---

## Updated Proposed Combined Approach

Combine **Option 2 (Sliding Window)** with **Option 4 (Packed NAK)**:

### Phase 1: Immediate Changes

1. **Mandate btree when io_uring recv is enabled**
   - btree already sorts packets
   - Provides efficient gap detection via traversal

2. **Suppress immediate NAK for small gaps**
   ```go
   if gapSize <= reorderTolerance {
       // Don't NAK immediately — packet likely in-flight in io_uring
       return
   }
   ```

3. **Packed periodic NAK**
   - Traverse btree to find all gaps
   - Encode as singles and ranges
   - Send single NAK packet

### Phase 2: Full Sliding Window

1. **Implement behind window tracking**
   - Track received sequences in btree
   - Define behind window size (100 packets default)
   - Only NAK when packets fall off window

2. **Add configuration options**
   ```go
   type SRTConfig struct {
       // ... existing fields ...
       NAKDelayPackets   int  // Behind window size (default: 100)
       NAKImmediateThreshold int  // Gap size that triggers immediate NAK (default: 50)
   }
   ```

3. **Metrics for tuning**
   - `reorder_depth_max` — Maximum observed reorder depth
   - `reorder_depth_avg` — Average reorder depth
   - `nak_suppressed_total` — NAKs not sent due to sliding window

---

## Open Questions

### Resolved ✅

| Question | Decision |
|----------|----------|
| btree required for io_uring? | ✅ Yes - mandatory |
| Separate tracking structure? | ✅ No - reuse existing btree |
| Immediate NAK for io_uring? | ✅ No - periodic only |
| TCP SACK approach? | ❌ Rejected - breaks compat |
| Where to sort by urgency? | ✅ NAK generator (natural btree order) |
| Accept duplicate retransmissions? | ✅ Yes - for small gaps |

### Answered from Code Review ✅

#### NAK Timing (CONFIRMED)

1. **Periodic NAK interval = 20ms** ✅
   - From `receive.go`: `r.periodicNAKInterval` checked against 20,000 µs
   - Comment: "expected ~50/sec (20ms interval)"

2. **Immediate NAK exists AND has a TODO!** 🎯
   - Line 295-296 in `receive.go`:
     ```go
     // Too far ahead, there are some missing sequence numbers, immediate NAK report.
     // TODO: Implement SRTO_LOSSMAXTTL to delay NAK for reordered packets.
     ```
   - The problem was anticipated! `SRTO_LOSSMAXTTL` is an SRT option for this exact issue.

#### Sender-Side (CONFIRMED)

3. **Sender already handles multi-entry NAK** ✅
   - `nakLocked()` in `send.go` iterates pairs:
     ```go
     for i := 0; i < len(sequenceNumbers); i += 2 {
         if p.Header().PacketSequenceNumber.Gte(sequenceNumbers[i]) &&
            p.Header().PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {
             // retransmit
         }
     }
     ```
   - NAK format is already `[start1, end1, start2, end2, ...]`
   - Both singles (start==end) and ranges work correctly

4. **Sender iterates lossList from Back() to Front()**
   - Newest to oldest order for retransmission
   - May want to reverse if we want oldest (most urgent) first

#### Periodic NAK (CONFIRMED)

5. **Periodic NAK already does btree traversal** ✅
   - `periodicNAKLocked()` iterates packet store to find gaps
   - Already builds list of `[start, end]` pairs
   - This is exactly what we need!

### Resolved Questions ✅

| Question | Decision |
|----------|----------|
| MergeGap value? | Time-based: `MergeGapMs = 5ms` (adapts to rate) |
| NAK packet overflow? | Generate multiple NAK packets (section added) |
| Sender retransmit order? | Oldest first, behind `RetransmitOldestFirst` flag |
| uint64 vs time.Time? | Use `time.Time` for Go idiomaticity |
| Where to update arrival time? | Atomically in `IncrementRecvMetrics` |
| Separate lock for NAK btree? | Yes, separate `sync.RWMutex` |
| Window shrinking safety? | Scan MAX(current, previous) range |
| Sequence wraparound? | Generic `SeqLess`, `SeqDiff` functions (from goTrackRTP) |
| NAK expiration timing? | Account for RTT (expire earlier than TSBPD) |
| Library for ranges? | Stay with Google btree, add generic seq math |
| Consolidation budget? | 2ms (reasonable, needs benchmarking) |
| Implement v1 or v2? | v2 directly (full NAK btree) |

### Still Open

1. **Connection startup**: First few packets - how to initialize NAK btree?
   - Proposal: Don't scan until we've received at least `acceptableWindow` packets

2. **RTT source**: Where to get smoothed RTT in receiver?
   - Need to check existing goSRT RTT tracking

3. **Consolidation caching**: Worth implementing for FastNAK?
   - Proposal: Future optimization, not initial implementation

4. **Testing infrastructure**: Need test harness for simulating io_uring reordering
   - Could inject artificial reordering for unit tests

---

## Implementation Checklist

### Phase 0: Investigation ✅ COMPLETE

- [x] **Review current NAK implementation**
  - [x] Immediate NAK at line 295-301 in `receive.go`
  - [x] Periodic NAK interval = 20ms (20,000 µs)
  - [x] `sendNAK()` called from both immediate and periodic paths

- [x] **Verify sender handles multi-entry NAK**
  - [x] `nakLocked()` in `send.go` iterates pairs correctly
  - [x] Both singles and ranges work
  - [x] Retransmission iterates Back→Front (newest first)

- [x] **Review btree traversal capabilities**
  - [x] `packetStore.Iterate()` already used in `periodicNAKLocked()`
  - [x] Iterates in sequence order
  - [x] Gap detection already implemented!

---

### Implementation Approach: NAK btree (v2) - Complete

#### Phase 1: NAK btree Structure

- [ ] **Create NAK btree type** (stores singles only, not ranges)
  ```go
  // In congestion/live/nak_btree.go (new file)
  type nakBtree struct {
      tree *btree.BTreeG[uint32]  // Missing sequence numbers (singles)
  }

  func newNakBtree() *nakBtree {
      return &nakBtree{
          tree: btree.NewG[uint32](32, func(a, b uint32) bool { return a < b }),
      }
  }

  func (nb *nakBtree) Insert(seq uint32)           { nb.tree.ReplaceOrInsert(seq) }
  func (nb *nakBtree) Delete(seq uint32)           { nb.tree.Delete(seq) }
  func (nb *nakBtree) DeleteBefore(cutoff uint32)  { /* iterate and delete */ }
  func (nb *nakBtree) Len() int                    { return nb.tree.Len() }
  ```

- [ ] **Add to receiver struct**
  ```go
  type receiver struct {
      // ... existing fields ...

      // NAK btree (v2)
      nakBtree              *nakBtree
      windowCalc            *WindowCalculator

      // FastNAK
      lastPacketArrivalTime uint64
      lastNAKTime           uint64

      // Config
      nakConfig             NAKConfig
  }
  ```

#### Phase 2: Window Calculator

- [ ] **Implement dynamic window sizing**
  ```go
  type WindowCalculator struct {
      latencyMs           uint64
      behindWindowMs      uint64   // default: 200ms
      acceptableWindowMs  uint64   // default: 100ms
      avgPayloadSize      float64  // from receiver
      bytesPerSecond      float64  // from receiver rate stats
      packetsPerMs        float64  // computed
  }

  func (wc *WindowCalculator) Update(avgPayloadSize, bytesPerSecond float64)
  func (wc *WindowCalculator) BehindWindow() uint32
  func (wc *WindowCalculator) AcceptableWindow() uint32
  func (wc *WindowCalculator) ScanWindow() uint32
  ```

#### Phase 3: FastNAK

- [ ] **Implement FastNAK trigger**
  ```go
  func (r *receiver) checkFastNAK(now uint64) bool {
      if !r.nakConfig.FastNAKEnabled {
          return false
      }
      if r.lastPacketArrivalTime == 0 {
          return false
      }
      silentPeriod := now - r.lastPacketArrivalTime
      if silentPeriod > r.nakConfig.FastNAKThresholdMs * 1000 {
          return true
      }
      return false
  }
  ```

- [ ] **Integrate into Push()**
  ```go
  func (r *receiver) Push(pkt) {
      now := getCurrentTimeMicros()

      // FastNAK check BEFORE updating arrival time
      if r.checkFastNAK(now) {
          r.triggerFastNAK(now)
      }

      r.lastPacketArrivalTime = now
      // ... rest of push logic ...
  }
  ```

#### Phase 4: Range Consolidation

- [ ] **Implement time-budgeted consolidation**
  ```go
  func (r *receiver) consolidateAndBuildNAK() []circular.Number {
      deadline := time.Now().Add(r.nakConfig.ConsolidationBudget)
      var result []circular.Number

      // Traverse DESCENDING for urgency ordering (oldest first in NAK)
      r.nakBtree.tree.DescendLessOrEqual(r.maxSeen, func(seq uint32) bool {
          if time.Now().After(deadline) {
              return false  // Time's up!
          }
          // Merge logic...
          return true
      })

      return result
  }
  ```

- [ ] **Merge gap handling**
  ```go
  // Convert mergeGapMs to packets using window calculator
  mergeGapPackets := uint32(float64(r.nakConfig.MergeGapMs) * r.windowCalc.packetsPerMs)
  ```

#### Phase 5: Core Logic Changes

- [ ] **Modify Push() - full implementation**
  ```go
  func (r *receiver) pushLocked(pkt packet.Packet) {
      now := getCurrentTimeMicros()
      seq := pkt.Header().PacketSequenceNumber.Val()

      // 1. FastNAK check
      if r.checkFastNAK(now) {
          r.triggerFastNAK(now)
      }
      r.lastPacketArrivalTime = now

      // 2. Remove from NAK btree (packet arrived!)
      r.nakBtree.Delete(seq)

      // 3. Normal packet processing (existing code)
      // ... avgPayloadSize, metrics, etc. ...

      // 4. Update maxSeen
      if pkt.Header().PacketSequenceNumber.Gt(r.maxSeenSequenceNumber) {
          r.maxSeenSequenceNumber = pkt.Header().PacketSequenceNumber
      }

      // 5. NO immediate NAK (suppressed for io_uring path)
      // (remove existing immediate NAK code)

      // 6. Store packet
      r.packetStore.Insert(pkt)
  }
  ```

- [ ] **Modify periodicNAKLocked() - full implementation**
  ```go
  func (r *receiver) periodicNAKLocked(now uint64) []circular.Number {
      // Update window calculator with latest rate
      r.windowCalc.Update(r.avgPayloadSize, r.rate.bytesPerSecond)

      // 1. Calculate scan window
      behindWindow := r.windowCalc.BehindWindow()
      acceptableWindow := r.windowCalc.AcceptableWindow()
      scanStart := r.maxSeen - behindWindow
      scanEnd := r.maxSeen - acceptableWindow

      // 2. Scan window for NEW gaps
      r.scanWindowForGaps(scanStart, scanEnd)

      // 3. Remove expired sequences
      r.nakBtree.DeleteBefore(scanStart)

      // 4. Consolidate and build NAK list
      list := r.consolidateAndBuildNAK()

      r.lastPeriodicNAK = now
      r.lastNAKTime = now

      return list
  }
  ```

#### Phase 6: Configuration

- [ ] **Complete NAKConfig struct**
  ```go
  type NAKConfig struct {
      // Enable/disable
      UseNakBtree           bool          // Auto-enabled for io_uring
      SuppressImmediateNAK  bool          // Auto-enabled for io_uring

      // Windows (time-based)
      BehindWindowMs        uint64        // default: 200
      AcceptableWindowMs    uint64        // default: 100

      // Consolidation
      MergeGapMs            uint64        // default: 5
      ConsolidationBudget   time.Duration // default: 1ms
      SortByUrgency         bool          // default: true

      // FastNAK
      FastNAKEnabled        bool          // default: true
      FastNAKThresholdMs    uint64        // default: 50
  }
  ```

- [ ] **Auto-configuration for io_uring**
  ```go
  func (cfg *NAKConfig) ConfigureForIoUring() {
      cfg.UseNakBtree = true
      cfg.SuppressImmediateNAK = true
      cfg.FastNAKEnabled = true
  }
  ```

#### Phase 7: Metrics

- [ ] **Add comprehensive metrics**
  - `nak_btree_size` — Current size of NAK btree
  - `nak_btree_inserts_total` — Gaps added to NAK btree
  - `nak_btree_deletes_arrival_total` — Removed because packet arrived
  - `nak_btree_deletes_expired_total` — Removed because fell off window
  - `nak_ranges_merged_total` — Ranges merged (accepted duplicates)
  - `nak_duplicates_accepted_total` — Duplicate retransmissions expected
  - `nak_fast_triggers_total` — FastNAK triggered
  - `nak_consolidation_timeouts_total` — Consolidation exceeded budget
  - `nak_window_packets` — Current window size in packets

#### Phase 8: Testing (Comprehensive)

- [ ] **NAK btree unit tests** (`nak_btree_test.go`)
  - [ ] Insert/delete single sequences
  - [ ] DeleteBefore() with various cutoffs
  - [ ] Empty btree edge cases
  - [ ] Large btree (1000+ entries)

- [ ] **Window calculator tests** (`window_calculator_test.go`)
  - [ ] Various data rates (1, 5, 10, 20 Mbps)
  - [ ] Window shrinking safety
  - [ ] Window growing
  - [ ] Rate fluctuation
  - [ ] Startup defaults (before rate known)

- [ ] **Range consolidation tests** (`consolidation_test.go`)
  - [ ] Contiguous sequences → single range
  - [ ] Non-contiguous → multiple ranges + singles
  - [ ] MergeGap behavior (merge small gaps)
  - [ ] Time budget cutoff (verify stops on deadline)
  - [ ] Empty input
  - [ ] Single sequence
  - [ ] All singles (no ranges)
  - [ ] Benchmark: consolidation time

- [ ] **Sequence math tests** (`seq_math_test.go`)
  - [ ] SeqLess: obvious cases (0 < 1, 100 < 101)
  - [ ] SeqLess: wraparound (0 > MaxSeq is false)
  - [ ] SeqLess: edge at MaxSeq/2 boundary
  - [ ] SeqDiff: simple differences
  - [ ] SeqDiff: wraparound (|0 - MaxSeq| = 1)
  - [ ] Benchmark: branchless vs branching

- [ ] **Multiple NAK packets tests** (`nak_packet_test.go`)
  - [ ] Single packet (fits in MSS)
  - [ ] Exactly at MSS boundary
  - [ ] 2 packets needed
  - [ ] 5 packets needed (stress)
  - [ ] Off-by-one at boundaries
  - [ ] Mix of singles and ranges
  - [ ] Deserialize what we serialize (round-trip)

- [ ] **FastNAK tests** (`fast_nak_test.go`)
  - [ ] Trigger after threshold exceeded
  - [ ] No trigger if recent NAK sent
  - [ ] No trigger if disabled
  - [ ] Correct time.Time usage

- [ ] **Lock tests** (`nak_lock_test.go`)
  - [ ] Concurrent Push() and periodicNAK()
  - [ ] No deadlock under load
  - [ ] Lock ordering respected

- [ ] **Sender retransmission tests** (`sender_retrans_test.go`)
  - [ ] Oldest-first ordering
  - [ ] Compare with newest-first
  - [ ] Feature flag respected

- [ ] **Integration tests**
  - [ ] Re-run `Isolation-Server-IoUringRecv` — expect 0 false gaps
  - [ ] Re-run full parallel test
  - [ ] Simulate Starlink outage (60ms gap)
  - [ ] Verify FastNAK triggers correctly
  - [ ] Verify no regression on non-io_uring path
  - [ ] Performance comparison: old vs NAK btree

- [ ] **Stress tests**
  - [ ] High packet rate (20+ Mbps)
  - [ ] High loss rate (10%+)
  - [ ] Consolidation budget under pressure
  - [ ] Long-running (check for memory leaks)

- [ ] **Benchmarks** (`nak_bench_test.go`)
  - [ ] NAK btree insert/delete
  - [ ] Window scanning
  - [ ] Range consolidation
  - [ ] Sequence math operations
  - [ ] Full periodicNAK cycle

---

### Alternative: Minimal Implementation (v1)

If NAK btree seems too complex initially, here's a simpler first step:

- [ ] **Just suppress immediate NAK**
  ```go
  if !r.config.SuppressImmediateNAK {
      r.sendNAK(nakList)
  }
  ```

- [ ] **Keep existing periodicNAK**
  - Still scans full primary btree
  - Less optimal but simpler

- [ ] **Upgrade to NAK btree later**
  - Once basic fix is validated
  - Performance optimization

---

### Implementation Notes

**What we DON'T need to change**:
- Periodic NAK timer (already 20ms)
- NAK packet format (already `[start, end]` pairs)
- Sender NAK handling (already handles multi-entry)
- Primary packet btree

**What we DO need to add (NAK btree approach)**:
- NAK btree structure and operations
- Windowed scanning of primary btree
- Integration in Push() and periodicNAK()
- Configuration and metrics

---

## Appendix: goTrackRTP Concepts

From `documentation/goTrackRTP.README.md`:

### Window Definitions

| Term | Description |
|------|-------------|
| Max() | Highest sequence seen (reference point) |
| Behind Window (bw) | Packets behind Max() that are acceptable |
| Ahead Window (aw) | Packets ahead of Max() (gaps) |
| Behind Buffer (bb) | Safety zone to prevent erroneous resets |
| Ahead Buffer (ab) | Safety zone for large jumps |

### Key Insight

goTrackRTP doesn't NAK at all — it just **tracks** what's missing. For goSRT, we adapt this by **delaying** NAK until packets truly fall off the behind window.

### Example Configuration

For 5 Mbps video (~475 packets/second):
- Behind Window: 100 packets (~210ms)
- Ahead Window: 50 packets (~105ms)
- Safety Buffer: 500 packets (~1s)

This allows reordering up to 100 packets while still detecting true loss.

---

## References

1. SRT RFC Draft: https://datatracker.ietf.org/doc/html/draft-sharabayko-srt
2. goTrackRTP: https://github.com/randomizedcoder/goTrackRTP
3. io_uring documentation: https://kernel.dk/io_uring.pdf
4. TCP SACK RFC 2018: https://tools.ietf.org/html/rfc2018

