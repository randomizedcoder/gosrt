# Design: NAK btree v2 - Efficient Gap Detection for io_uring

**Status**: DESIGN
**Date**: 2025-12-11
**Related**:
- `design_io_uring_reorder_solutions.md` (exploration document with additional options considered)
- `parallel_defect1_highperf_excessive_gaps.md` (root cause analysis)

---

## Table of Contents

1. [Problem Statement and Motivation](#1-problem-statement-and-motivation)
2. [Current goSRT Implementation](#2-current-gosrt-implementation)
3. [Design Requirements](#3-design-requirements) *(HOLD)*
4. [New Design: NAK btree v2](#4-new-design-nak-btree-v2) *(HOLD)*
5. [Metrics and Visibility](#5-metrics-and-visibility) *(HOLD)*
6. [Error Handling](#6-error-handling) *(HOLD)*
7. [Comprehensive Testing](#7-comprehensive-testing) *(HOLD)*
8. [Comprehensive Benchmarking](#8-comprehensive-benchmarking) *(HOLD)*
9. [Integration: Parallel Isolation Tests](#9-integration-parallel-isolation-tests) *(HOLD)*
10. [Integration: Parallel Comparison Tests](#10-integration-parallel-comparison-tests) *(HOLD)*

---

## 1. Problem Statement and Motivation

### 1.1 The Problem

When using `io_uring` for the receive path, packets are delivered to the application layer **out of order**, even though they arrived at the kernel network stack in order.

**Root cause confirmed** via isolation testing (see `parallel_defect1_highperf_excessive_gaps.md`):

| Test | Variable Changed | Gaps on Clean Network |
|------|------------------|----------------------|
| Control | None | 0 |
| Server-IoUringSend | io_uring send | 0 |
| **Server-IoUringRecv** | **io_uring recv** | **2,476** |
| Server-Btree | btree storage | 0 |

The issue is **exclusively** in the server's io_uring receive path.

### 1.2 Why Out-of-Order Occurs

With 512 outstanding `recvmsg` requests in the io_uring submission queue, completions arrive in arbitrary order based on:
- Kernel scheduling across CPU cores
- io_uring internal batching
- Memory/buffer availability

**Debug logging confirmed**:
```
seq=194811147 reqID=77
seq=194811150 reqID=3776    ← GAP (148, 149 missing)
seq=194811152 reqID=3787    ← GAP (151 missing)
seq=194811146 reqID=4140    ← OUT OF ORDER (went back 6)
seq=194811148 reqID=4313    ← filling gap
...
seq=194811181 reqID=4206
seq=194811122 reqID=4224    ← MAJOR OUT OF ORDER (went back 59!)
```

### 1.3 Impact of Current Implementation

The current goSRT implementation sends **immediate NAKs** when gaps are detected:

```go
// congestion/live/receive.go:295-301 (current behavior)
// Too far ahead, there are some missing sequence numbers, immediate NAK report.
// TODO: Implement SRTO_LOSSMAXTTL to delay NAK for reordered packets.
nakList := []circular.Number{
    r.maxSeenSequenceNumber.Inc(),
    pkt.Header().PacketSequenceNumber.Dec(),
}
r.sendNAK(nakList)
```

**Note**: The codebase already has a TODO acknowledging this issue! `SRTO_LOSSMAXTTL` is the standard SRT option for tolerating reordering.

**Consequences on a clean network (0% packet loss)**:
- 2,476 false gap detections
- 718 unnecessary NAKs sent
- 2,500 unnecessary retransmissions
- Wasted bandwidth and CPU cycles

### 1.4 Motivation for New Design

We need a NAK mechanism that:

1. **Tolerates io_uring reordering** - Don't NAK immediately; packets may arrive shortly
2. **Efficiently detects true gaps** - Without scanning the entire packet buffer every 20ms
3. **Prioritizes urgent packets** - NAK oldest (most urgent) missing packets first
4. **Scales with data rate** - Works efficiently at 5, 10, 20+ Mbps
5. **Maintains compatibility** - Existing non-io_uring path continues to work

---

## 2. Current goSRT Implementation

This section describes how goSRT handles packet buffering, TSBPD, and NAK/retransmission today.

### 2.1 Packet Flow Overview

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              PACKET RECEIVE FLOW                                    │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                     │
│  NETWORK                                                                            │
│     │                                                                               │
│     ▼                                                                               │
│  ┌─────────────────────────────────────────────────────────────────────────────┐    │
│  │ listen.go / dial.go                                                         │    │
│  │   receivePacket() - reads UDP packets from socket                           │    │
│  │   OR                                                                        │    │
│  │ listen_linux.go / dial_linux.go                                             │    │
│  │   recvCompletionHandler() - io_uring receive path                           │    │
│  └─────────────────────────────────────────────────────────────────────────────┘    │
│     │                                                                               │
│     ▼                                                                               │
│  ┌─────────────────────────────────────────────────────────────────────────────┐    │
│  │ connection.go:handlePacket()                                                │    │
│  │   - Calculates PktTsbpdTime (line 927)                                      │    │
│  │   - Decrypts if needed                                                      │    │
│  │   - Calls c.recv.Push(p)                                                    │    │
│  └─────────────────────────────────────────────────────────────────────────────┘    │
│     │                                                                               │
│     ▼                                                                               │
│  ┌─────────────────────────────────────────────────────────────────────────────┐    │
│  │ congestion/live/receive.go:Push() → pushLocked()                            │    │
│  │   - Updates avgPayloadSize (line 253)                                       │    │
│  │   - Detects gaps → sends IMMEDIATE NAK (lines 295-301)                      │    │
│  │   - Stores packet in packetStore (btree or list)                            │    │
│  └─────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                     │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 TSBPD: Timestamp-Based Packet Delivery

TSBPD is the mechanism that controls when packets are released to the application.

#### 2.2.1 How PktTsbpdTime is Calculated

**Source**: `connection.go:927` in `handlePacket()`

```go
header.PktTsbpdTime = c.tsbpdTimeBase + tsbpdTimeBaseOffset + uint64(header.Timestamp) + c.tsbpdDelay + c.tsbpdDrift
```

| Component | Source | Description |
|-----------|--------|-------------|
| `tsbpdTimeBase` | Handshake | Synchronized time base between sender/receiver |
| `tsbpdTimeBaseOffset` | Wrap handling | Adjusts for 32-bit timestamp wraparound |
| `header.Timestamp` | Packet header | Sender's timestamp when packet was created |
| `tsbpdDelay` | Config (`RecvLatency`) | Latency buffer (e.g., 3000ms = 3,000,000 µs) |
| `tsbpdDrift` | Drift tracker | Corrects for clock drift between sender/receiver |

**Example calculation**:
```
tsbpdTimeBase       = 1,000,000,000 µs  (connection start time)
tsbpdTimeBaseOffset = 0                  (no wrap yet)
header.Timestamp    = 5,000,000 µs       (5 seconds into stream)
tsbpdDelay          = 3,000,000 µs       (3 second latency buffer)
tsbpdDrift          = 100 µs             (small clock correction)

PktTsbpdTime = 1,000,000,000 + 0 + 5,000,000 + 3,000,000 + 100
             = 1,008,000,100 µs

→ This packet should be delivered 8 seconds after connection start
```

#### 2.2.2 Timestamp Wraparound Handling

**Source**: `connection.go:906-925`

The packet's `Timestamp` field is 32-bit (max ~71 minutes). goSRT detects and handles wraparound:

```go
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

### 2.3 Packet Storage

#### 2.3.1 Packet Store Implementations

goSRT supports two packet store implementations:

| Implementation | File | Use Case |
|----------------|------|----------|
| Linked List | `packet/store_list.go` | Default, simple |
| btree | `packet/store_btree.go` | Higher performance, auto-sorting |

**Selection**: `congestion/live/receive.go:74-85` in `NewReceiver()`

```go
if config.PacketReorderAlgorithm == "btree" {
    degree := config.BTreeDegree
    if degree <= 0 {
        degree = 32 // Default btree degree
    }
    store = NewBTreePacketStore(degree)
} else {
    store = NewListPacketStore()
}
```

#### 2.3.2 Storing Packets

**Source**: `congestion/live/receive.go:320` in `pushLocked()`

```go
r.packetStore.Insert(pkt)
```

Both implementations store packets and allow iteration in sequence order.

**Key insight**: The btree implementation automatically maintains sequence order, which we can leverage for efficient gap detection.

### 2.4 Packet Release (TSBPD Delivery)

**Source**: `congestion/live/receive.go:534-549` in `Tick()`

Packets are released when their TSBPD time has arrived AND they've been acknowledged:

```go
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

**Important**:
- SRT buffer is **TIME-BASED**, not packet-count limited
- `RecvLatency = 3000ms` means packets wait up to 3 seconds
- Buffer can hold ANY number of packets
- Packets are released based on TSBPD timestamp, not count

### 2.5 Current NAK Implementation

goSRT has two NAK mechanisms:

#### 2.5.1 Immediate NAK (on gap detection)

**Source**: `congestion/live/receive.go:295-301` in `pushLocked()`

```go
// Too far ahead, there are some missing sequence numbers, immediate NAK report.
// TODO: Implement SRTO_LOSSMAXTTL to delay NAK for reordered packets.
nakList := []circular.Number{
    r.maxSeenSequenceNumber.Inc(),
    pkt.Header().PacketSequenceNumber.Dec(),
}
r.sendNAK(nakList)
```

**Trigger**: When a packet arrives with sequence > expected (maxSeen + 1)

**Problem with io_uring**: Packets arrive out of order, causing false gap detection.

#### 2.5.2 Periodic NAK (every 20ms)

**Source**: `congestion/live/receive.go:462-511` in `periodicNAKLocked()`

```go
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

**Performance concern**: This scans the ENTIRE packet store every 20ms. For large buffers (~1400 packets at 5Mbps with 3s latency), this is significant overhead.

**Buffer size in packets** (assuming `avgPayloadSize = 1456 bytes`):

| Data Rate | 200ms | 500ms | 1s | 3s | 5s | 10s |
|-----------|-------|-------|-----|------|-------|--------|
| **1 Mbps** | 17 | 43 | 86 | 257 | 429 | 858 |
| **5 Mbps** | 86 | 215 | 429 | 1,287 | 2,145 | 4,290 |
| **10 Mbps** | 172 | 429 | 858 | 2,574 | 4,290 | 8,580 |
| **20 Mbps** | 343 | 858 | 1,716 | 5,148 | 8,580 | 17,160 |

*Formula: `packets = (data_rate_bps / 8 / avgPayloadSize) × buffer_seconds`*

**Scan overhead at 20ms interval** (50 scans/second):
- 5 Mbps, 3s buffer: 1,287 packets × 50 = **64,350 packet iterations/second**
- 20 Mbps, 5s buffer: 8,580 packets × 50 = **429,000 packet iterations/second**

This highlights why the NAK btree approach (scanning only a window) is important for high-throughput scenarios.

#### 2.5.3 NAK Packet Format (RFC)

**Source**: SRT RFC Section 3.2.5

```
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|0|                 Lost packet sequence number                 |  ← Single (4 bytes)
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|1|         Range of lost packets from sequence number          |  ← Range start (4 bytes)
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|0|                    Up to sequence number                    |  ← Range end (4 bytes)
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

- **Single**: First bit = 0, followed by sequence number (4 bytes total)
- **Range**: First bit = 1 for start, then first bit = 0 for end (8 bytes total)
- **Multiple entries** can be packed in one NAK packet

### 2.6 Sender Retransmission

When the sender receives a NAK, it retransmits the requested packets.

**Source**: `congestion/live/send.go:395-447` in `nakLocked()`

```go
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
    // Count packets requested
    totalLossCount := metrics.CountNAKEntries(m, sequenceNumbers, metrics.NAKCounterRecv)

    // Retransmit packets that we can find in our buffer
    retransCount := uint64(0)
    for e := s.lossList.Back(); e != nil; e = e.Prev() {
        p := e.Value.(packet.Packet)

        // Check if this packet is in any of the NAK ranges
        for i := 0; i < len(sequenceNumbers); i += 2 {
            if p.Header().PacketSequenceNumber.Gte(sequenceNumbers[i]) &&
               p.Header().PacketSequenceNumber.Lte(sequenceNumbers[i+1]) {
                // Retransmit
                p.Header().RetransmittedPacketFlag = true
                s.deliver(p)
                retransCount++
            }
        }
    }

    return retransCount
}
```

**Key observations**:
- Sender iterates `lossList` from Back to Front (newest to oldest)
- Checks each packet against all NAK ranges
- Sets `RetransmittedPacketFlag = true` on retransmitted packets
- Tracks packets that couldn't be retransmitted (already dropped from buffer)

**Note on order**: Currently retransmits newest first (iterates `lossList` from Back to Front).

**New design options**:

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **Sender oldest-first** | Sender changes iteration to Front→Back | Simple sender change | Sender decides priority |
| **Honor NAK order** | Sender processes NAK entries in received order | Receiver controls priority; NAK btree naturally orders by urgency | Requires sender to iterate NAK entries, not lossList |

**Recommendation**: Honor NAK order. This allows the receiver to control retransmission priority:
- Receiver's NAK btree is traversed oldest-first (most urgent)
- NAK packet contains entries in urgency order
- Sender retransmits in NAK packet order
- More precise control: receiver knows its buffer state better than sender

### 2.7 Rate Statistics

goSRT tracks packet rates for congestion control and statistics.

**Source**: `congestion/live/receive.go`

| Metric | Updated | Location |
|--------|---------|----------|
| `avgPayloadSize` | Every packet | `pushLocked()` line 253 |
| `bytesPerSecond` | Every ~1 second | `updateRateStats()` lines 589-607 |

```go
// Per-packet update (line 253)
r.avgPayloadSize = 0.875*r.avgPayloadSize + 0.125*float64(pktLen)

// Per-second update (lines 592-594)
if tdiff > r.rate.period {  // period = 1 second
    r.rate.bytesPerSecond = float64(r.rate.bytes) / (float64(tdiff) / 1000 / 1000)
    // ... reset counters ...
}
```

### 2.8 Summary of Current Design Issues

| Issue | Impact | Root Cause |
|-------|--------|------------|
| **Immediate NAK on gap** | False positives with io_uring | Doesn't tolerate reordering |
| **Full buffer scan every 20ms** | O(n) where n = buffer size | No incremental tracking |
| **No reorder tolerance** | Unnecessary retransmissions | Missing SRTO_LOSSMAXTTL |
| **Newest-first retransmission** | Suboptimal recovery | Oldest packets most urgent |

---

## 3. Design Requirements

### 3.1 Functional Requirements

#### 3.1.1 Core NAK Behavior

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-1 | **Suppress immediate NAK** for io_uring receive path | Prevents false gap detection from out-of-order delivery |
| FR-2 | **Maintain immediate NAK** for non-io_uring path | Backward compatibility with existing behavior |
| FR-3 | **Periodic NAK every 20ms** continues to operate | Ensures gaps are eventually detected and NAK'd |
| FR-4 | **NAK btree** stores missing sequence numbers (singles only) | Efficient tracking without range-splitting complexity |

#### 3.1.2 Scan Window

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-5 | **TSBPD-based scan boundary** | Stop scanning at packets whose TSBPD is within "too recent" threshold |
| FR-6 | **NAKScanStartPoint tracking** | Never skip scanning a packet; always scan forward from last position |
| FR-7 | **Configurable "too recent" percentage** | Default 10% of tsbpdDelay; operator can tune aggressiveness |

#### 3.1.3 NAK Packet Generation

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-8 | **Consolidate singles into ranges** | Reduce NAK packet size; efficient use of NAK format |
| FR-9 | **MergeGap for range consolidation** | Accept small duplicate retransmissions for fewer NAK entries |
| FR-10 | **Time-budgeted consolidation** | Limit consolidation time (2ms) to avoid blocking |
| FR-11 | **Multiple NAK packets** when entries exceed MSS | Handle large gap scenarios gracefully |
| FR-12 | **Urgency ordering** (oldest first) | Most urgent packets appear first in NAK packet |

#### 3.1.4 FastNAK Optimization

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-13 | **FastNAK trigger** after silent period | Don't wait for 20ms timer after outage ends |
| FR-14 | **Configurable FastNAK threshold** | Default 50ms; tune for network characteristics |
| FR-15 | **Track last packet arrival time** atomically | Enable FastNAK check without locking |

#### 3.1.5 Sender Retransmission

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-16 | **Honor NAK packet order** for retransmission | Receiver controls priority; sender retransmits in NAK order |
| FR-17 | **Feature flag** for new retransmission behavior | Allow rollback if issues discovered |

#### 3.1.6 NAK btree Lifecycle

| ID | Requirement | Rationale |
|----|-------------|-----------|
| FR-18 | **Delete from NAK btree** when packet arrives | Stop NAKing for packets that have arrived |
| FR-19 | **Expire entries** based on RTT consideration | Stop NAKing packets too late for retransmission to help |
| FR-20 | **Initialize on first packet** | Set NAKScanStartPoint when connection starts |

### 3.2 Non-Functional Requirements

#### 3.2.1 Code Quality

| ID | Requirement | Details |
|----|-------------|---------|
| NFR-1 | **Go idiomatic code** | Use `time.Time` for timestamps, standard library patterns |
| NFR-2 | **Specific file/function references** | All design references must cite exact file:line |
| NFR-3 | **Atomic operations** for hot-path variables | `lastPacketArrivalTime`, `NAKScanStartPoint` |
| NFR-4 | **Go generics** for sequence math | `SeqLess[T]`, `SeqDiff[T]` for wraparound handling |

#### 3.2.2 Maintainability

| ID | Requirement | Details |
|----|-------------|---------|
| NFR-5 | **Feature flags** for enable/disable | Easy rollback without code changes |
| NFR-6 | **Separate lock for NAK btree** | Don't block packet processing during NAK generation |
| NFR-7 | **Clear lock ordering** | Document and enforce to prevent deadlocks |
| NFR-8 | **Function substitution** for performance paths | Allow swapping implementations via config |

#### 3.2.3 Backward Compatibility

| ID | Requirement | Details |
|----|-------------|---------|
| NFR-9 | **Existing non-io_uring path unchanged** | Default behavior identical to current |
| NFR-10 | **Configuration-driven activation** | New behavior only when explicitly enabled |
| NFR-11 | **Graceful degradation** | If NAK btree fails, fall back to existing behavior |

### 3.3 Performance Requirements

#### 3.3.1 Critical Paths

| Path | Current | Target | Measurement |
|------|---------|--------|-------------|
| **Packet arrival (Push)** | O(log n) insert | O(log n) insert + O(log m) NAK delete | Benchmark: ns/op |
| **Periodic NAK scan** | O(n) full buffer | O(window) partial scan | Benchmark: ns/op at various buffer sizes |
| **NAK consolidation** | N/A (new) | ≤ 2ms budget | Benchmark: time to consolidate |
| **FastNAK check** | N/A (new) | O(1) atomic read | Benchmark: ns/op |

#### 3.3.2 Benchmarking Requirements

| ID | Benchmark | Purpose |
|----|-----------|---------|
| PERF-1 | `BenchmarkPushWithNakBtree` | Measure packet arrival overhead |
| PERF-2 | `BenchmarkPeriodicNakScan` | Compare window scan vs full scan |
| PERF-3 | `BenchmarkConsolidation` | Measure range consolidation time |
| PERF-4 | `BenchmarkSeqMath` | Verify sequence wraparound performance |
| PERF-5 | `BenchmarkNakBtreeOperations` | Insert/delete/iterate performance |

#### 3.3.3 Scalability Targets

| Scenario | Buffer Size | Scan Overhead Target |
|----------|-------------|---------------------|
| 5 Mbps, 3s latency | ~1,287 packets | < 100µs per periodic NAK |
| 20 Mbps, 5s latency | ~8,580 packets | < 500µs per periodic NAK |
| 20 Mbps, 10s latency | ~17,160 packets | < 1ms per periodic NAK |

### 3.4 Testing Requirements

#### 3.4.1 Unit Test Coverage

| Component | Test File | Key Test Cases |
|-----------|-----------|----------------|
| NAK btree | `nak_btree_test.go` | Insert, delete, iterate, empty, large |
| Scan window | `scan_window_test.go` | TSBPD threshold, NAKScanStartPoint lazy init |
| Consolidation | `consolidation_test.go` | Ranges, singles, merge gap, time budget, pool reuse |
| Sequence math | `seq_math_test.go` | Wraparound, edge cases (from goTrackRTP) |
| FastNAK | `fast_nak_test.go` | Trigger conditions, threshold |
| FastNAKRecent | `fast_nak_recent_test.go` | Sequence jump detection, gap insertion |
| Push hot path | `receive_benchmark_test.go` | `BenchmarkPushLockedWithNakBtree`, `BenchmarkPushLockedWithoutNakBtree` |

#### 3.4.2 Corner Cases (Bug-Prone Areas)

| Area | Corner Case | Why Bug-Prone |
|------|-------------|---------------|
| **Sequence wraparound** | Max sequence → 0 transition | 31-bit arithmetic edge |
| **Empty NAK btree** | No missing packets | Nil/empty checks |
| **First packet** | NAKScanStartPoint initialization | Race with periodic NAK |
| **Window boundaries** | Exactly at TSBPD threshold | Off-by-one errors |
| **MSS overflow** | Exactly at packet boundary | Off-by-one in packing |
| **Consolidation timeout** | Budget expires mid-consolidation | Partial results handling |
| **Lock ordering** | Concurrent Push and periodicNAK | Deadlock potential |

#### 3.4.3 Integration Test Requirements

| Test | Purpose | Related Doc |
|------|---------|-------------|
| `Isolation-Server-IoUringRecv` | Verify 0 false gaps on clean network | `parallel_isolation_test_plan.md` |
| Full parallel comparison | Compare old vs new under real conditions | `parallel_comparison_test_design.md` |
| Starlink simulation | Test FastNAK under 60ms outages | Network impairment tests |

### 3.5 Configuration Requirements

#### 3.5.1 Operator-Visible Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `NakRecentPercent` | float64 | 0.10 | Percentage of tsbpdDelay for "too recent" threshold |
| `NakMergeGap` | uint32 | 3 | Max sequence gap to merge in consolidation (packets) |
| `NakConsolidationBudgetMs` | uint64 | 2 | Max time for consolidation (ms) |
| `FastNakEnabled` | bool | true | Enable FastNAK optimization (can disable if issues) |
| `FastNakThresholdMs` | uint64 | 50 | Silent period to trigger FastNAK (ms) |
| `FastNakRecentEnabled` | bool | true | Add recent gap immediately on FastNAK trigger |
| `HonorNakOrder` | bool | false | Sender retransmits in NAK packet order |

#### 3.5.2 Internal Configuration (Auto-Set)

These options are not exposed to operators; they are automatically configured:

| Option | Type | Description | Auto-set Rule |
|--------|------|-------------|---------------|
| `UseNakBtree` | bool | Enable NAK btree | `true` when `IoUringRecvEnabled=true` |
| `SuppressImmediateNak` | bool | Suppress immediate NAK | `true` when `IoUringRecvEnabled=true` |

**Rationale**: These internal options exist for code clarity (readable conditionals) but should not be exposed to operators because:
- `UseNakBtree` must be true for io_uring to function correctly
- `SuppressImmediateNak` must be true to prevent false gap detection

#### 3.5.3 Auto-Configuration

When `IoUringRecvEnabled = true`:
- `UseNakBtree` → true (internal, required)
- `SuppressImmediateNak` → true (internal, required)
- `FastNakEnabled` → true (operator can override to false)
- `FastNakRecentEnabled` → true (operator can override to false)

### 3.6 Documentation Requirements

| ID | Requirement |
|----|-------------|
| DOC-1 | Update `cli_args.md` with new flags |
| DOC-2 | Add inline code comments for complex algorithms |
| DOC-3 | Document lock ordering in code comments |
| DOC-4 | Update metrics documentation with new counters |

---

## 4. New Design: NAK btree v2

### 4.1 Architecture Overview

#### 4.1.1 Two Independent Structures

The design introduces a **NAK btree** that operates independently from the existing **packet btree**:

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              DUAL BTREE ARCHITECTURE                                 │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                      │
│  ┌─────────────────────────────────────────────────────────────────────────────┐    │
│  │                     PACKET BTREE (existing)                                  │    │
│  │  - Stores received packets                                                   │    │
│  │  - Ordered by sequence number                                                │    │
│  │  - Each packet has PktTsbpdTime                                              │    │
│  │  - Releases packets when TSBPD time arrives                                  │    │
│  │                                                                              │    │
│  │  [pkt:100] → [pkt:101] → [pkt:105] → [pkt:106] → ... → [pkt:500]            │    │
│  └─────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                      │
│  ┌─────────────────────────────────────────────────────────────────────────────┐    │
│  │                        NAK BTREE (new)                                       │    │
│  │  - Stores missing sequence numbers (singles only)                            │    │
│  │  - Ordered by sequence number                                                │    │
│  │  - Much smaller than packet btree                                            │    │
│  │  - Entries removed when packets arrive or expire                             │    │
│  │                                                                              │    │
│  │  [seq:102] → [seq:103] → [seq:104] → [seq:107] → [seq:108]                  │    │
│  └─────────────────────────────────────────────────────────────────────────────┘    │
│                                                                                      │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

#### 4.1.2 Data Flow

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                                PACKET ARRIVAL                                        │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                      │
│  1. Packet arrives with seq=105                                                     │
│     │                                                                                │
│     ├──► FastNAK check (if silent period exceeded, trigger NAK)                     │
│     │                                                                                │
│     ├──► Update lastPacketArrivalTime (atomic)                                      │
│     │                                                                                │
│     ├──► Delete seq=105 from NAK btree (if present)                                 │
│     │    └── O(log m) where m = NAK btree size                                      │
│     │                                                                                │
│     ├──► Insert packet into packet btree                                            │
│     │    └── O(log n) where n = packet btree size                                   │
│     │                                                                                │
│     └──► NO immediate NAK (suppressed for io_uring)                                 │
│                                                                                      │
└─────────────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              PERIODIC NAK (every 20ms)                               │
├─────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                      │
│  1. Calculate TSBPD threshold                                                        │
│     └── tooRecentThreshold = now + (tsbpdDelay × nakRecentPercent)                  │
│                                                                                      │
│  2. Scan packet btree from NAKScanStartPoint                                        │
│     │                                                                                │
│     ├── For each packet, check for gaps in sequence                                 │
│     ├── Add missing sequences to NAK btree                                          │
│     ├── Stop when packet.PktTsbpdTime > tooRecentThreshold                          │
│     └── Update NAKScanStartPoint to last scanned sequence                           │
│                                                                                      │
│  3. Expire old entries from NAK btree                                               │
│     └── Remove entries whose TSBPD time has arrived (RTT optimization later)        │
│                                                                                      │
│  4. Consolidate NAK btree into ranges (time-budgeted)                               │
│     │                                                                                │
│     ├── Traverse NAK btree (oldest first for urgency)                               │
│     ├── Merge adjacent sequences (respecting mergeGap)                              │
│     └── Stop if consolidation budget exceeded                                       │
│                                                                                      │
│  5. Build and send NAK packet(s)                                                    │
│     │                                                                                │
│     ├── Pack entries respecting MSS limit                                           │
│     ├── Generate multiple packets if needed                                         │
│     └── Entries in urgency order (oldest first)                                     │
│                                                                                      │
└─────────────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 NAK btree Structure

#### 4.2.1 Design Decision: Store Singles Only

The NAK btree stores **individual sequence numbers**, not ranges:

| Approach | Insert | Delete on Arrival | Complexity |
|----------|--------|-------------------|------------|
| **Singles (chosen)** | O(log m) | O(log m) simple delete | Low |
| Ranges | Complex merge check | Complex split (50-60 → 50-54, 56-60) | High |

**Rationale**: When a packet arrives, deleting a single sequence is trivial. With ranges, we'd need to split ranges (e.g., if range 50-60 exists and packet 55 arrives, split to 50-54 and 56-60).

#### 4.2.2 Data Structure

**File**: `congestion/live/nak_btree.go` (new file)

```go
package live

import (
    "sync"
    "github.com/google/btree"
)

// nakBtree stores missing sequence numbers for NAK generation.
// Stores singles only (not ranges) for simplicity of delete operations.
type nakBtree struct {
    tree *btree.BTreeG[uint32]
    mu   sync.RWMutex  // Separate lock from packet btree
}

// newNakBtree creates a new NAK btree with specified degree.
func newNakBtree(degree int) *nakBtree {
    return &nakBtree{
        tree: btree.NewG[uint32](degree, seqLess),
    }
}

// seqLess compares sequence numbers handling wraparound.
// Uses the generic SeqLess function from seq_math.go.
func seqLess(a, b uint32) bool {
    return SeqLess(a, b)
}

// Insert adds a missing sequence number to the btree.
func (nb *nakBtree) Insert(seq uint32) {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    nb.tree.ReplaceOrInsert(seq)
}

// Delete removes a sequence number (packet arrived).
func (nb *nakBtree) Delete(seq uint32) {
    nb.mu.Lock()
    defer nb.mu.Unlock()
    nb.tree.Delete(seq)
}

// DeleteBefore removes all sequences below cutoff (expired).
func (nb *nakBtree) DeleteBefore(cutoff uint32) int {
    nb.mu.Lock()
    defer nb.mu.Unlock()

    deleted := 0
    nb.tree.Ascend(func(seq uint32) bool {
        if SeqLess(seq, cutoff) {
            nb.tree.Delete(seq)
            deleted++
            return true
        }
        return false  // Stop when we reach cutoff
    })
    return deleted
}

// Len returns the number of missing sequences.
func (nb *nakBtree) Len() int {
    nb.mu.RLock()
    defer nb.mu.RUnlock()
    return nb.tree.Len()
}

// Iterate traverses the btree in sequence order.
// Callback returns false to stop iteration.
func (nb *nakBtree) Iterate(fn func(seq uint32) bool) {
    nb.mu.RLock()
    defer nb.mu.RUnlock()
    nb.tree.Ascend(func(seq uint32) bool {
        return fn(seq)
    })
}

// IterateDescending traverses in reverse order (for urgency ordering).
func (nb *nakBtree) IterateDescending(fn func(seq uint32) bool) {
    nb.mu.RLock()
    defer nb.mu.RUnlock()
    nb.tree.Descend(func(seq uint32) bool {
        return fn(seq)
    })
}
```

#### 4.2.3 Integration with Receiver

**File**: `congestion/live/receive.go` (modifications)

```go
type receiver struct {
    // ... existing fields ...

    // NAK btree v2 fields
    nakBtree                    *nakBtree
    nakScanStartPoint           atomic.Uint32    // Sequence to start scanning from
    lastPacketArrivalTime       atomic.Value     // time.Time for FastNAK
    lastDataPacketSeq           atomic.Uint32    // For FastNAKRecent feature

    // Configuration (auto-set when IoUringRecvEnabled=true)
    useNakBtree                 bool             // Auto: true with io_uring recv
    suppressImmediateNak        bool             // Auto: true with io_uring recv

    // Configuration (operator-adjustable)
    nakRecentPercent            float64          // Default: 0.10
    nakMergeGap                 uint32           // Default: 3 (max sequence gap to merge)
    nakConsolidationBudget      time.Duration    // Default: 2ms
    fastNakEnabled              bool             // Default: true with io_uring
    fastNakThreshold            time.Duration    // Default: 50ms
    fastNakRecentEnabled        bool             // Default: true - add recent gap on FastNAK
}

// Note: nakEntryPool is a package-level sync.Pool (not per-receiver)
// because the pool benefits from cross-connection sharing.
```

**Configuration Visibility**:

| Config Option | User Visible | Default | Description |
|---------------|--------------|---------|-------------|
| `UseNakBtree` | No (internal) | auto | `true` when `IoUringRecvEnabled=true` |
| `SuppressImmediateNak` | No (internal) | auto | `true` when `IoUringRecvEnabled=true` |
| `NakRecentPercent` | **Yes** | 0.10 | TSBPD threshold for "too recent" |
| `NakMergeGap` | **Yes** | 3 | Max sequence gap to merge |
| `NakConsolidationBudgetMs` | **Yes** | 2 | Max consolidation time (ms) |
| `FastNakEnabled` | **Yes** | true | Enable FastNAK optimization |
| `FastNakRecentEnabled` | **Yes** | true | Add recent gap on FastNAK |

### 4.3 TSBPD-Based Scan Window

#### 4.3.1 Algorithm

The scan window is determined by TSBPD timestamps, not packet counts:

```go
// periodicNakV2 implements TSBPD-based scanning.
// File: congestion/live/receive.go
func (r *receiver) periodicNakV2(now uint64) []circular.Number {
    if now-r.lastPeriodicNAK < r.periodicNAKInterval {
        return nil
    }

    // Step 1: Calculate "too recent" threshold
    // Packets with TSBPD beyond this are too new to NAK
    tooRecentThreshold := now + uint64(float64(r.tsbpdDelay)*r.nakRecentPercent)

    // Step 2: Get starting point - lazy initialization from packet btree
    // More efficient than CompareAndSwap on every packet in Push()
    startSeq := r.nakScanStartPoint.Load()
    if startSeq == 0 {
        // Initialize from oldest packet in btree
        minPkt := r.packetStore.Min()
        if minPkt == nil {
            return nil  // No packets yet
        }
        startSeq = minPkt.Header().PacketSequenceNumber.Val()
        r.nakScanStartPoint.Store(startSeq)
    }

    // Step 3: Scan packet btree from startSeq
    var lastScannedSeq uint32
    expectedSeq := startSeq

    r.packetStore.AscendGreaterOrEqual(startSeq, func(pkt packet.Packet) bool {
        h := pkt.Header()

        // Stop if this packet is "too recent"
        if h.PktTsbpdTime > tooRecentThreshold {
            return false  // Stop iteration
        }

        actualSeq := h.PacketSequenceNumber.Val()

        // Detect gaps: expected vs actual
        for expectedSeq < actualSeq {
            // Missing sequence - add to NAK btree
            r.nakBtree.Insert(expectedSeq)
            expectedSeq++
        }

        expectedSeq = actualSeq + 1
        lastScannedSeq = actualSeq
        return true  // Continue
    })

    // Step 4: Update scan start point for next iteration
    if lastScannedSeq > 0 {
        r.nakScanStartPoint.Store(lastScannedSeq)
    }

    // Step 5: Expire old entries (based on TSBPD time)
    // Note: RTT-based earlier expiry is a future optimization
    r.expireNakEntries(now)

    // Step 6: Consolidate and build NAK list
    nakList := r.consolidateNakBtree()

    r.lastPeriodicNAK = now
    return nakList
}
```

**Design Note**: `nakScanStartPoint` is initialized lazily in `periodicNakV2()` using `packetStore.Min()`, rather than via `CompareAndSwap` on every packet in `Push()`. This avoids an atomic operation on the hot path.

#### 4.3.2 Scan Window Visualization

```
                        TSBPD Timeline
    ─────────────────────────────────────────────────────────────►

    │◄──────────── tsbpdDelay (e.g., 3000ms) ────────────────────►│
    │                                                              │
    │  NAKScanStartPoint            tooRecentThreshold    now+tsbpdDelay
    │        │                              │                  │
    │        ▼                              ▼                  ▼
    ├────────┬──────────────────────────────┬──────────────────┤
    │ SCANNED│       SCAN THIS RANGE        │   TOO RECENT     │
    │ BEFORE │                              │   (don't NAK)    │
    ├────────┴──────────────────────────────┴──────────────────┤
              │                              │
              │◄─── 90% of tsbpdDelay ──────►│◄── 10% ──►│

    Example with tsbpdDelay = 3000ms, nakRecentPercent = 0.10:
    - tooRecentThreshold = now + 300ms
    - Scan packets with TSBPD < now + 2700ms
    - Don't NAK packets arriving in last 300ms (might be OOO)
```

#### 4.3.3 Hot Path Optimization in Push

**File**: `congestion/live/receive.go`

The `pushLocked` function is on the data hot path. Variables and operations are moved inside conditionals to minimize overhead when features are disabled:

```go
// pushLocked - modified for NAK btree v2 (hot path optimized)
func (r *receiver) pushLocked(pkt packet.Packet) {
    // ... existing validation ...

    // NAK btree v2 operations (only when enabled)
    if r.useNakBtree {
        seq := pkt.Header().PacketSequenceNumber.Val()

        // Delete from NAK btree (packet arrived!) (FR-18)
        r.nakBtree.Delete(seq)

        // Track for FastNAKRecent feature
        if r.fastNakRecentEnabled {
            r.lastDataPacketSeq.Store(seq)
        }
    }

    // FastNAK check (only when enabled)
    if r.fastNakEnabled {
        now := time.Now()
        r.checkFastNak(now)
        r.lastPacketArrivalTime.Store(now)
    }

    // Suppress immediate NAK for io_uring path (FR-1)
    if !r.suppressImmediateNak {
        // ... existing immediate NAK logic ...
    }

    // ... rest of existing pushLocked ...
}
```

**Hot Path Design Notes**:

| Feature Disabled | Avoided Operations |
|------------------|-------------------|
| `useNakBtree=false` | `pkt.Header().PacketSequenceNumber.Val()`, `nakBtree.Delete()` |
| `fastNakEnabled=false` | `time.Now()`, `checkFastNak()`, `lastPacketArrivalTime.Store()` |
| `fastNakRecentEnabled=false` | `lastDataPacketSeq.Store()` |

**Benchmark Requirement**: Add `BenchmarkPushLockedWithNakBtree` and `BenchmarkPushLockedWithoutNakBtree` to measure overhead.

#### 4.3.4 NAK btree Entry Expiry

Entries in the NAK btree expire when it's too late for retransmission to help:

```go
// expireNakEntries removes entries whose TSBPD time has passed.
// Initially uses TSBPD-based expiry; RTT-based earlier expiry is a future optimization.
func (r *receiver) expireNakEntries(now uint64) {
    // Find the oldest packet in the packet btree
    minPkt := r.packetStore.Min()
    if minPkt == nil {
        return
    }

    // Any NAK entry older than the oldest packet's sequence is expired
    // (the packet btree has already released those packets via TSBPD)
    cutoff := minPkt.Header().PacketSequenceNumber.Val()

    expired := r.nakBtree.DeleteBefore(cutoff)
    if expired > 0 && r.metrics != nil {
        r.metrics.NakEntriesExpired.Add(uint64(expired))
    }
}
```

**Design Notes**:

| Phase | Expiry Strategy | Rationale |
|-------|-----------------|-----------|
| **Phase 1 (initial)** | TSBPD-based | Simple: expire when packet btree releases |
| **Phase 2 (future)** | RTT-aware | Earlier expiry: `TSBPD - smoothedRTT - margin` |

**Why TSBPD-based is sufficient initially**:
- Packet btree releases packets when TSBPD arrives
- NAK entries for those sequences are no longer useful
- RTT optimization would expire entries slightly earlier (saves a few retransmission attempts)
- Can add RTT optimization later without changing the interface

### 4.4 Consolidation Algorithm

#### 4.4.1 Purpose

Convert individual sequences in NAK btree to efficient ranges for NAK packet:

```
NAK btree (missing sequences): [100, 101, 102, 106, 107, 108, 115, 116]

Note: 103, 104, 105 are NOT in btree (packets arrived)
      109-114 are NOT in btree (packets arrived)

With NakMergeGap = 3 (default):
  Processing seq 100: start Range(100, 100)
  Processing seq 101: gap = 101-100-1 = 0 ≤ 3 → extend to Range(100, 101)
  Processing seq 102: gap = 102-101-1 = 0 ≤ 3 → extend to Range(100, 102)
  Processing seq 106: gap = 106-102-1 = 3 ≤ 3 → extend to Range(100, 106)*
  Processing seq 107: gap = 107-106-1 = 0 ≤ 3 → extend to Range(100, 107)
  Processing seq 108: gap = 108-107-1 = 0 ≤ 3 → extend to Range(100, 108)
  Processing seq 115: gap = 115-108-1 = 6 > 3 → emit Range(100,108), start Range(115,115)
  Processing seq 116: gap = 116-115-1 = 0 ≤ 3 → extend to Range(115, 116)

Result: [Range(100,108), Range(115,116)]
  = 2 ranges instead of 8 singles

* Range(100,106) includes 103,104,105 which already arrived.
  These will be retransmitted as duplicates (acceptable trade-off).
```

#### 4.4.2 Implementation

**File**: `congestion/live/nak_consolidate.go` (new file)

```go
package live

import (
    "sync"
    "time"
    "github.com/datarhei/gosrt/circular"
)

// NAKEntry represents either a single sequence or a range.
type NAKEntry struct {
    Start uint32
    End   uint32  // Same as Start for singles
}

func (e NAKEntry) IsRange() bool {
    return e.End > e.Start
}

// Pool for intermediate []NAKEntry slices.
// These grow to "right size" for the workload over time.
// Note: Only pool intermediate results, not return values.
var nakEntryPool = sync.Pool{
    New: func() interface{} {
        s := make([]NAKEntry, 0, 64)  // Initial capacity
        return &s
    },
}

// consolidateNakBtree converts NAK btree singles into ranges.
// Traverses in ascending order (oldest first = most urgent).
// Respects time budget to avoid blocking.
// Uses sync.Pool for intermediate []NAKEntry slice.
func (r *receiver) consolidateNakBtree() []circular.Number {
    deadline := time.Now().Add(r.nakConsolidationBudget)

    // Get pooled slice for intermediate entries
    // Already len=0 (from New or previous defer reset)
    entriesPtr := nakEntryPool.Get().(*[]NAKEntry)
    entries := *entriesPtr

    // Defer: return entries to pool AFTER entriesToNakList() consumes it
    // Reset to len=0 before returning so next Get() is ready to use
    defer func() {
        // Update pointer in case slice grew (append may reallocate)
        *entriesPtr = entries[:0]  // Reset length for next use
        nakEntryPool.Put(entriesPtr)
    }()

    var currentEntry *NAKEntry
    iterCount := 0

    r.nakBtree.Iterate(func(seq uint32) bool {
        // Check time budget every 100 iterations
        // (time.Now() per iteration is expensive)
        iterCount++
        if iterCount%100 == 0 {
            if time.Now().After(deadline) {
                return false  // Stop - time's up
            }
        }

        if currentEntry == nil {
            // Start new entry
            currentEntry = &NAKEntry{Start: seq, End: seq}
            return true
        }

        // Check if contiguous or within mergeGap
        gap := seq - currentEntry.End - 1
        if gap <= r.nakMergeGap {
            // Extend current entry
            currentEntry.End = seq
        } else {
            // Gap too large - emit current, start new
            entries = append(entries, *currentEntry)
            currentEntry = &NAKEntry{Start: seq, End: seq}
        }

        return true
    })

    // Emit final entry
    if currentEntry != nil {
        entries = append(entries, *currentEntry)
    }

    // Convert to NAK list format
    // entries is consumed here, then returned to pool by defer
    return r.entriesToNakList(entries)
}

// entriesToNakList converts NAKEntry slice to circular.Number pairs.
// Returns a fresh slice (not pooled) because caller needs ownership.
func (r *receiver) entriesToNakList(entries []NAKEntry) []circular.Number {
    // Allocate with known capacity - caller owns this slice
    list := make([]circular.Number, 0, len(entries)*2)

    for _, entry := range entries {
        list = append(list,
            circular.New(entry.Start, packet.MAX_SEQUENCENUMBER),
            circular.New(entry.End, packet.MAX_SEQUENCENUMBER),
        )
    }

    return list
}
```

#### 4.4.3 Deadline Check Analysis

**Option 1: `time.Now()` per iteration** (original design)
```go
if time.Now().After(deadline) {
    return false
}
```
- Cost: ~30-50ns per `time.Now()` call
- At 1000 entries: 30-50µs overhead
- Simple and accurate

**Option 2: `select` with `time.After`**
```go
select {
case <-time.After(budget):
    // timeout
default:
    // continue
}
```
- Cost: Timer allocation, channel operations
- More expensive per check than `time.Now()`
- Better for blocking waits, not iteration checks

**Option 3: Amortized check (chosen)**
```go
iterCount++
if iterCount%100 == 0 {
    if time.Now().After(deadline) { ... }
}
```
- Cost: ~0.3-0.5ns per iteration (counter increment + modulo)
- `time.Now()` called every 100 iterations: 0.3-0.5µs overhead per 100
- Best of both: low overhead, still time-bounded

**Recommendation**: Option 3 (amortized) is most efficient for iteration patterns.

#### 4.4.4 sync.Pool Design

**What to pool**: Only intermediate results that are fully consumed before returning.

| Slice | Pooled? | Reason |
|-------|---------|--------|
| `entries []NAKEntry` | **Yes** | Intermediate; consumed by `entriesToNakList()` before function returns |
| `list []circular.Number` | **No** | Return value; caller needs ownership to send NAK packet |

**How defer works with pooling**:

```
consolidateNakBtree() called
    │
    ├── nakEntryPool.Get()         → entries acquired
    ├── defer registered           → will run after return
    ├── entries populated          → NAKEntry values added
    ├── entriesToNakList(entries)  → entries READ (consumed)
    │       └── returns list       → fresh allocation
    ├── defer executes             → entries returned to pool
    └── return list                → caller receives list
```

**Pool warmup behavior**:

| Call # | Without Pool | With Pool (after warmup) |
|--------|--------------|-------------------------|
| 1 | Alloc entries, alloc list | Alloc entries, alloc list |
| 2 | Alloc entries, alloc list | **Reuse entries**, alloc list |
| 3+ | Alloc entries, alloc list | **Reuse entries** (right-sized), alloc list |

The pool saves one allocation per call (the `[]NAKEntry` slice), while `[]circular.Number` is always fresh because caller owns it.

**Benchmark Requirement**: Create both pooled and non-pooled versions, benchmark to confirm improvement:
- `BenchmarkConsolidateNakBtreeWithPool`
- `BenchmarkConsolidateNakBtreeWithoutPool`

#### 4.4.3 MergeGap Behavior

`NakMergeGap` is an **integer** representing the maximum sequence number gap to bridge when consolidating ranges. This is simpler than time-based calculation and matches the consolidation code directly.

| NakMergeGap | Effect | Trade-off |
|-------------|--------|-----------|
| 0 | No merging, exact ranges only | More NAK entries, no duplicate retransmissions |
| 1-3 (default: 3) | Merge small gaps | Fewer entries, few duplicates |
| 5+ | Aggressive merging | Fewest entries, more duplicate retransmissions |

**Example with NakMergeGap = 3**:

```
Missing sequences in NAK btree: [100, 101, 102, 106, 107, 115]
(Note: 103-105 arrived, 108-114 arrived)

Consolidation:
  100→101→102: contiguous (gap=0) → Range(100, 102)
  102→106: gap = 3 ≤ NakMergeGap → extend to Range(100, 106)*
  106→107: gap = 0 → extend to Range(100, 107)
  107→115: gap = 7 > NakMergeGap → emit Range(100,107), start Single(115)

Result: [Range(100,107), Single(115)]

* Sequences 103,104,105 will be retransmitted as duplicates (trade-off)
```

**Design decision**: Integer-based gap is preferred over time-based because:
1. Simpler - no packet rate calculation needed
2. Predictable - same behavior regardless of bitrate
3. Direct - matches the consolidation algorithm's sequence comparison

### 4.5 FastNAK Optimization

#### 4.5.1 Purpose

After a network outage (e.g., Starlink 60ms reconvergence), don't wait for the 20ms periodic timer. Trigger NAK immediately when packets resume.

#### 4.5.2 Implementation

**File**: `congestion/live/receive.go`

```go
// checkFastNak triggers immediate NAK if silent period exceeded.
// Also detects sequence jump for FastNAKRecent feature.
func (r *receiver) checkFastNak(now time.Time) {
    lastArrival, ok := r.lastPacketArrivalTime.Load().(time.Time)
    if !ok || lastArrival.IsZero() {
        return  // No previous packet
    }

    silentPeriod := now.Sub(lastArrival)
    if silentPeriod < r.fastNakThreshold {
        return  // Not long enough silence
    }

    // Don't trigger if we just sent a NAK
    if r.timeSinceLastNak() < r.periodicNAKInterval {
        return
    }

    // Trigger FastNAK
    r.triggerFastNak()
}

func (r *receiver) triggerFastNak() {
    now := uint64(time.Now().UnixMicro())

    if list := r.periodicNakV2(now); len(list) != 0 {
        metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
        r.sendNAK(list)
    }

    // Track metric
    if r.metrics != nil {
        r.metrics.NakFastTriggers.Add(1)
    }
}
```

#### 4.5.3 FastNAKRecent Feature

When the first packet arrives after a silent period, the sequence number likely "jumped forward" by the outage duration × packet rate. FastNAKRecent detects this jump and immediately adds the missing range to the NAK btree.

**File**: `congestion/live/receive.go`

```go
// checkFastNakRecent detects sequence jump after outage.
// Called from pushLocked when first packet arrives after silence.
func (r *receiver) checkFastNakRecent(currentSeq uint32, now time.Time) {
    if !r.fastNakRecentEnabled {
        return
    }

    lastArrival, ok := r.lastPacketArrivalTime.Load().(time.Time)
    if !ok || lastArrival.IsZero() {
        return
    }

    silentPeriod := now.Sub(lastArrival)
    if silentPeriod < r.fastNakThreshold {
        return  // Not an outage
    }

    lastSeq := r.lastDataPacketSeq.Load()
    if lastSeq == 0 {
        return
    }

    // Calculate expected gap based on outage duration
    // At 5 Mbps with 1316 byte packets: ~475 pps
    // 60ms outage = ~28 packets missed
    expectedGap := uint32(silentPeriod.Seconds() * float64(r.packetsPerSecond()))

    // Actual gap
    actualGap := SeqDiff(currentSeq, lastSeq)

    // If actual gap is significant (and matches expected), add to NAK btree
    // Use threshold to avoid false positives from io_uring reordering
    minGapThreshold := uint32(10)  // At least 10 packets gap
    if actualGap > minGapThreshold && actualGap < expectedGap*2 {
        // Add missing range to NAK btree (as singles)
        for seq := lastSeq + 1; SeqLess(seq, currentSeq); seq++ {
            r.nakBtree.Insert(seq)
        }

        if r.metrics != nil {
            r.metrics.NakFastRecentInserts.Add(uint64(actualGap - 1))
        }
    }
}
```

**Configuration**:
- `FastNakRecentEnabled`: Operator can disable if io_uring reordering causes false positives
- Default: `true` (test in Starlink integration tests)

**Caveat**: With io_uring receive reordering, `lastDataPacketSeq` may not be exactly the highest sequence seen. This is acceptable because:
1. We use a minimum gap threshold
2. Adding a few extra sequences to NAK btree is harmless (they'll be deleted when packets arrive)
3. The feature can be disabled if problematic

#### 4.5.4 Timing Diagram

```
Timeline during Starlink outage (with FastNAKRecent):

0ms   : Last packet received (seq=1000)
        lastPacketArrivalTime = T0
        lastDataPacketSeq = 1000

20ms  : Periodic NAK fires (nothing new to NAK)

40ms  : Periodic NAK fires (still waiting)

60ms  : Outage ends, first packet arrives (seq=1028)
        silentPeriod = 60ms > threshold (50ms)

        1. FastNAKRecent detects jump:
           - lastSeq=1000, currentSeq=1028
           - Gap of 27 packets matches expected (~28 for 60ms)
           - Immediately adds seq 1001-1027 to NAK btree

        2. FastNAK triggers:
           - Runs periodicNakV2()
           - NAK includes 1001-1027 immediately!

        → NAK sent at 60ms with recent loss included

Without FastNAKRecent: Wait for next periodic NAK scan to find gap
With FastNAKRecent: Gap is NAK'd immediately (saves 20ms + scan time)
```

### 4.6 Lock Design

#### 4.6.1 Problem

The Google btree is NOT thread-safe. Currently, `receiver.lock` protects the packet btree. If we reuse this lock for NAK btree, periodic NAK blocks packet processing.

#### 4.6.2 Solution: Separate Locks

```go
type receiver struct {
    // Packet btree lock (existing)
    lock        sync.RWMutex
    packetStore PacketStore

    // NAK btree lock (new, separate)
    // Lock is internal to nakBtree struct
    nakBtree    *nakBtree
}
```

**Lock ordering** (to prevent deadlock):
1. Never hold both locks simultaneously if possible
2. If both needed: acquire `nakBtree.mu` BEFORE `receiver.lock`

#### 4.6.3 Lock Usage by Operation

| Operation | packet btree lock | NAK btree lock |
|-----------|-------------------|----------------|
| `Push()` packet | Write lock (existing) | Write lock (Delete) |
| `periodicNakV2()` scan | Read lock | Write lock (Insert) |
| `periodicNakV2()` consolidate | None | Read lock |
| `Tick()` delivery | Write lock | None |

**Key optimization**: Push() only briefly holds NAK btree lock for Delete(). Periodic NAK doesn't block Push() during consolidation.

### 4.7 Sequence Number Wraparound

#### 4.7.1 SRT Sequence Numbers

- 31-bit sequences: 0 to 2^31-1 (2,147,483,647)
- At 20 Mbps: wraps every ~14.5 days
- Must handle wraparound in comparisons and arithmetic

#### 4.7.2 Generic Sequence Math

**File**: `circular/seq_math.go` (new file)

```go
package circular

// MaxSeqNumber is the maximum SRT sequence number (31-bit).
const MaxSeqNumber uint32 = 0x7FFFFFFF

// SeqLess returns true if s1 < s2, handling wraparound.
// Based on goTrackRTP algorithm.
func SeqLess[T ~uint32](s1, s2 T) bool {
    const halfMax = MaxSeqNumber / 2
    diff := int64(s1) - int64(s2)
    diff += int64(halfMax) + 1
    diff &= int64(MaxSeqNumber)
    return diff > 0 && diff <= int64(halfMax)
}

// SeqDiff returns |s1 - s2| handling wraparound.
func SeqDiff[T ~uint32](s1, s2 T) T {
    if s1 == s2 {
        return 0
    }
    var abs T
    if s1 < s2 {
        abs = T(s2 - s1)
    } else {
        abs = T(s1 - s2)
    }
    if uint32(abs) > MaxSeqNumber/2 {
        return T(MaxSeqNumber - uint32(abs) + 1)
    }
    return abs
}

// SeqGreater returns true if s1 > s2, handling wraparound.
func SeqGreater[T ~uint32](s1, s2 T) bool {
    return SeqLess(s2, s1)
}

// SeqLessOrEqual returns true if s1 <= s2, handling wraparound.
func SeqLessOrEqual[T ~uint32](s1, s2 T) bool {
    return s1 == s2 || SeqLess(s1, s2)
}
```

#### 4.7.3 Test Cases (from goTrackRTP)

```go
// circular/seq_math_test.go
func TestSeqLess(t *testing.T) {
    tests := []struct {
        s1, s2 uint32
        want   bool
    }{
        // Obvious cases
        {0, 1, true},
        {1, 0, false},
        {100, 101, true},

        // Wraparound cases
        {0, MaxSeqNumber, false},      // 0 is "ahead" of max
        {MaxSeqNumber, 0, true},       // max is "behind" 0
        {1, MaxSeqNumber, false},
        {MaxSeqNumber, 1, true},

        // Edge at half-max
        {0, MaxSeqNumber / 2, true},
        {MaxSeqNumber / 2, 0, false},

        // Same
        {0, 0, false},
        {1000, 1000, false},
    }
    // ... test implementation ...
}
```

### 4.8 Implementation Files

#### 4.8.1 New Files

| File | Purpose |
|------|---------|
| `congestion/live/nak_btree.go` | NAK btree structure and operations |
| `congestion/live/nak_btree_test.go` | Unit tests for NAK btree |
| `congestion/live/nak_consolidate.go` | Consolidation algorithm with sync.Pool |
| `congestion/live/nak_consolidate_test.go` | Consolidation tests (with/without pool) |
| `congestion/live/fast_nak.go` | FastNAK and FastNAKRecent implementation |
| `congestion/live/fast_nak_test.go` | FastNAK trigger and sequence jump tests |
| `congestion/live/receive_benchmark_test.go` | Push hot path benchmarks |
| `circular/seq_math.go` | Generic sequence math functions |
| `circular/seq_math_test.go` | Sequence math tests (from goTrackRTP) |

#### 4.8.2 Modified Files

| File | Modifications |
|------|---------------|
| `congestion/live/receive.go` | Add NAK btree fields, `periodicNakV2()`, FastNAK dispatch |
| `congestion/live/send.go` | Honor NAK order dispatch, `nakLockedHonorOrder()` |
| `config.go` | Add operator-visible configuration options |
| `connection.go` | Pass config to receiver/sender, auto-set internal options |

#### 4.8.3 Function Dispatch

To maintain backward compatibility, use function dispatch based on configuration:

```go
// congestion/live/receive.go

// periodicNAK is the entry point - dispatches to v1 or v2.
// Counter is incremented here so both implementations are tracked.
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    // Increment counter regardless of which implementation runs
    // This allows monitoring that periodic NAK is firing
    if r.metrics != nil {
        r.metrics.NakPeriodicCalls.Add(1)
    }

    if r.useNakBtree {
        return r.periodicNakV2(now)  // New implementation
    }
    return r.periodicNakV1(now)      // Existing implementation (renamed)
}

// Rename existing periodicNAKLocked to periodicNakV1
func (r *receiver) periodicNakV1(now uint64) []circular.Number {
    // ... existing implementation unchanged ...
}
```

**Note**: The counter `NakPeriodicCalls` is incremented in the wrapper function, ensuring it tracks calls regardless of which implementation is active. This allows operators to verify the periodic NAK timer is firing as expected.

#### 4.8.4 Sender Modifications

**File**: `congestion/live/send.go`

Uses same function dispatch pattern as receiver:

```go
// nakLocked - dispatches to original or honor-order implementation.
func (s *sender) nakLocked(sequenceNumbers []circular.Number) uint64 {
    // Track metric
    if s.metrics != nil {
        s.metrics.NakReceivedCalls.Add(1)
    }

    if s.honorNakOrder {
        return s.nakLockedHonorOrder(sequenceNumbers)
    }
    return s.nakLockedOriginal(sequenceNumbers)  // Existing behavior
}

// nakLockedOriginal is the existing implementation (renamed).
func (s *sender) nakLockedOriginal(sequenceNumbers []circular.Number) uint64 {
    // ... existing implementation unchanged ...
}

// nakLockedHonorOrder retransmits in NAK packet order.
// NAK entries are already in urgency order from receiver.
func (s *sender) nakLockedHonorOrder(sequenceNumbers []circular.Number) uint64 {
    retransCount := uint64(0)

    // Process each NAK entry in order (receiver's urgency order)
    for i := 0; i < len(sequenceNumbers); i += 2 {
        start := sequenceNumbers[i]
        end := sequenceNumbers[i+1]

        // Find packets in this range and retransmit
        for e := s.lossList.Front(); e != nil; e = e.Next() {
            p := e.Value.(packet.Packet)
            seq := p.Header().PacketSequenceNumber

            if seq.Gte(start) && seq.Lte(end) {
                p.Header().RetransmittedPacketFlag = true
                s.deliver(p)
                retransCount++
            }
        }
    }

    return retransCount
}
```

**Configuration**:
```go
type sender struct {
    // ... existing fields ...

    // NAK order configuration
    honorNakOrder bool  // Default: false, set via config
}
```

The `honorNakOrder` option allows receiver-controlled retransmission priority. When enabled, packets are retransmitted in the order specified in the NAK packet (oldest/most-urgent first).

---

## 5. Metrics and Visibility

*HOLD - To be completed in future iteration*

See also: `metrics_analysis_design.md`

### 5.1 New Counters

### 5.2 Positive Path Metrics

### 5.3 Negative Path Metrics

### 5.4 Debugging Support

---

## 6. Error Handling

*HOLD - To be completed in future iteration*

### 6.1 Error Scenarios

### 6.2 Recovery Strategies

### 6.3 Logging

---

## 7. Comprehensive Testing

*HOLD - To be completed in future iteration*

### 7.1 Unit Tests by Function

### 7.2 Corner Cases

### 7.3 Integration Tests

### 7.4 Stress Tests

---

## 8. Comprehensive Benchmarking

*HOLD - To be completed in future iteration*

### 8.1 Critical Path Benchmarks

### 8.2 Comparison: Old vs New

### 8.3 Memory Profiling

---

## 9. Integration: Parallel Isolation Tests

*HOLD - To be completed in future iteration*

See: `parallel_isolation_test_plan.md`

---

## 10. Integration: Parallel Comparison Tests

*HOLD - To be completed in future iteration*

See: `parallel_comparison_test_design.md`

---

## Appendix

### A. Reference Documents

- `design_io_uring_reorder_solutions.md` - Exploration of design options
- `parallel_defect1_highperf_excessive_gaps.md` - Root cause analysis
- `goTrackRTP.README.md` - Inspiration for sliding window approach
- `trackRTP_math.go` / `trackRTP_math_test.go` - Sequence wraparound handling

### B. SRT RFC References

- Section 3.2.5: NAK packet format
- Section 4.5.1.1: TSBPD Time Base Calculation
- Appendix A: NAK encoding (singles and ranges)

