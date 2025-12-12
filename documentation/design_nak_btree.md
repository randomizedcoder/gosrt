# Design: NAK btree - Efficient Gap Detection for io_uring

**Status**: DESIGN
**Date**: 2025-12-11
**Related**:
- `design_io_uring_reorder_solutions.md` (exploration document with additional options considered)
- `parallel_defect1_highperf_excessive_gaps.md` (root cause analysis)

---

## Table of Contents

1. [Problem Statement and Motivation](#1-problem-statement-and-motivation)
2. [Current goSRT Implementation](#2-current-gosrt-implementation)
3. [Design Requirements](#3-design-requirements)
4. [New Design: NAK btree](#4-new-design-nak-btree)
5. [Metrics and Visibility](#5-metrics-and-visibility)
6. [Error Handling](#6-error-handling)
7. [Comprehensive Testing](#7-comprehensive-testing)
8. [Comprehensive Benchmarking](#8-comprehensive-benchmarking)
9. [Integration: Parallel Isolation Tests](#9-integration-parallel-isolation-tests)
10. [Integration: Parallel Comparison Tests](#10-integration-parallel-comparison-tests)
11. [Appendix](#appendix)

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

## 4. New Design: NAK btree

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

    // NAK btree fields
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
// periodicNakBtree implements TSBPD-based scanning.
// File: congestion/live/receive.go
func (r *receiver) periodicNakBtree(now uint64) []circular.Number {
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

**Design Note**: `nakScanStartPoint` is initialized lazily in `periodicNakBtree()` using `packetStore.Min()`, rather than via `CompareAndSwap` on every packet in `Push()`. This avoids an atomic operation on the hot path.

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
// pushLocked - modified for NAK btree (hot path optimized)
func (r *receiver) pushLocked(pkt packet.Packet) {
    // ... existing validation ...

    // NAK btree operations (only when enabled)
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

#### 4.3.5 Interaction: Packet btree Expiry → NAK btree Cleanup

**Question**: How are NAK btree entries removed when packets are released from the packet btree?

**Two removal paths for NAK btree entries**:

| Path | When | How |
|------|------|-----|
| **Packet arrival** | Packet arrives (Push) | `nakBtree.Delete(seq)` in Push path |
| **TSBPD expiry** | Packet released (Tick) | `expireNakEntries()` in periodicNakBtree |

**Timing interaction**:

```
┌─────────────────────────────────────────────────────────────────┐
│                    TSBPD EXPIRY TIMELINE                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  T=0      T=10ms    T=20ms    T=30ms    T=40ms                 │
│   │         │         │         │         │                     │
│   ├─ Tick ──┼─ Tick ──┼─ Tick ──┼─ Tick ──┤  Packet btree      │
│   │         │         │         │         │  releases via TSBPD │
│   │         │         │         │         │                     │
│   ├─────────┼─ NAK ───┼─────────┼─ NAK ───┤  periodicNakBtree  │
│   │         │         │         │         │  cleans NAK btree   │
│                                                                 │
│  At T=10ms: Tick releases packets up to seq=X                  │
│  At T=20ms: periodicNak calls expireNakEntries()               │
│             → DeleteBefore(Min()) removes NAK entries < X       │
│                                                                 │
│  Max delay: ~20ms between packet release and NAK cleanup       │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

**Is the ~20ms delay a problem?**

No, and **we never send NAKs for expired entries** because:
1. `expireNakEntries()` runs **first** in `periodicNakBtree()` (before consolidation)
2. By the time we consolidate and send NAK, expired entries are already removed
3. Self-correcting: Entry is cleaned up before NAK generation

**Alternative considered (not implemented)**:
- Hook NAK btree cleanup into Tick() when packets are released
- Rejected: Adds complexity to Tick hot path, unnecessary (expiry-first solves it)

**Testing requirement**: Verify NAK btree entries are properly cleaned up when packet btree advances (test in Section 7)

#### 4.3.6 NAK btree Entry Contents (Minimal by Design)

**What's stored in each btree**:

| Btree | Entry Type | Size | Contents |
|-------|------------|------|----------|
| **Packet btree** | `packet.Packet` | ~1400 bytes | Header + payload data |
| **NAK btree** | `uint32` | **4 bytes** | Sequence number only |

**Why NAK btree is so lightweight**:

1. **No TSBPD time needed**: Expiry uses sequence number comparison
   ```go
   cutoff := packetStore.Min().Header().PacketSequenceNumber.Val()
   nakBtree.DeleteBefore(cutoff)  // Compare sequences, not timestamps
   ```

2. **No packet data needed**: We only need to know *which* sequences are missing

3. **Sequence implies TSBPD**: Missing sequence X has TSBPD roughly equal to packet X's TSBPD (which we can look up in packet btree if needed)

**Memory comparison** (at 100 Mbps, 3s buffer, 5% loss):

| Btree | Entries | Entry Size | Total Memory |
|-------|---------|------------|--------------|
| Packet btree | ~28,500 | ~1400 bytes | ~40 MB |
| NAK btree | ~1,425 | 4 bytes | **~6 KB** |

**Conclusion**: NAK btree memory overhead is negligible (~0.015% of packet btree).

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

    if list := r.periodicNakBtree(now); len(list) != 0 {
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
           - Runs periodicNakBtree()
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
| `periodicNakBtree()` scan | Read lock | Write lock (Insert) |
| `periodicNakBtree()` consolidate | None | Read lock |
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
| `congestion/live/receive.go` | Add NAK btree fields, `periodicNakBtree()`, FastNAK, dispatch |
| `congestion/live/send.go` | Honor NAK order dispatch, `nakLockedHonorOrder()` |
| `config.go` | Add operator-visible configuration options |
| `connection.go` | Pass config to receiver/sender, auto-set internal options |

#### 4.8.3 Function Dispatch

To maintain backward compatibility, use function dispatch based on configuration:

```go
// congestion/live/receive.go

// periodicNAK is the entry point - dispatches to Original or NAKBtree.
// Counter is incremented here so both implementations are tracked.
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    // Increment counter regardless of which implementation runs
    // This allows monitoring that periodic NAK is firing
    if r.metrics != nil {
        r.metrics.NakPeriodicCalls.Add(1)
    }

    if r.useNakBtree {
        return r.periodicNakBtree(now)    // NAKBtree implementation
    }
    return r.periodicNakOriginal(now)     // Original implementation
}

// periodicNakOriginal is the existing implementation (renamed from periodicNAKLocked)
func (r *receiver) periodicNakOriginal(now uint64) []circular.Number {
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

The NAK btree implementation requires comprehensive metrics for:
1. **Confirming correct operation** - New code paths work as expected
2. **Debugging issues** - Identify problems during development and production
3. **Performance monitoring** - Track overhead of new features
4. **Comparison testing** - Compare Original vs NAKBtree behavior in parallel tests

See also: `metrics_analysis_design.md` for existing metrics architecture.

### 5.1 New Counters

All new counters follow the existing pattern in `metrics/metrics.go`:
- Atomic counters (`atomic.Uint64`)
- Prometheus export via `metrics/handler.go`
- Direction labels where applicable (`direction="sent"` or `direction="recv"`)

#### 5.1.1 NAK btree Counters

**File**: `metrics/metrics.go`

```go
// NAK btree counters (receiver side)
NakBtreeInserts       atomic.Uint64  // Sequences added to NAK btree
NakBtreeDeletes       atomic.Uint64  // Sequences removed (packet arrived)
NakBtreeExpired       atomic.Uint64  // Sequences removed (TSBPD expired)
NakBtreeSize          atomic.Uint64  // Current size (gauge, updated each periodic NAK)
NakBtreeScanPackets   atomic.Uint64  // Packets scanned in periodicNakBtree()
NakBtreeScanGaps      atomic.Uint64  // Gaps found during scan
```

| Counter | Type | Description | Usage |
|---------|------|-------------|-------|
| `NakBtreeInserts` | Counter | Missing sequences added to btree | Gap detection working |
| `NakBtreeDeletes` | Counter | Sequences deleted (packet arrived) | Reordered packets recovered |
| `NakBtreeExpired` | Counter | Sequences expired (too late) | Unrecoverable losses |
| `NakBtreeSize` | Gauge | Current btree size | Memory/backlog monitoring |
| `NakBtreeScanPackets` | Counter | Packets examined in scan | Scan efficiency |
| `NakBtreeScanGaps` | Counter | Gaps discovered during scan | True gap detection rate |

#### 5.1.1a Packet Store Counters (Alignment)

**Current state**: The existing packet store (`packetStore` interface in `congestion/live/packet_store.go`) has **no metrics**. Both `listPacketStore` and `btreePacketStore` implementations perform operations without counter tracking.

**Requirement**: Align packet store metrics with NAK btree metrics for observability consistency.

```go
// Packet store counters (receiver side) - NEW
PktStoreInserts       atomic.Uint64  // Packets inserted into packet store
PktStoreDuplicates    atomic.Uint64  // Duplicate packets rejected
PktStoreRemovals      atomic.Uint64  // Packets removed (delivered or dropped)
PktStoreSize          atomic.Uint64  // Current size (gauge)
```

| Counter | Type | Description | Usage |
|---------|------|-------------|-------|
| `PktStoreInserts` | Counter | Packets added to packet store | Ingress rate |
| `PktStoreDuplicates` | Counter | Duplicate packets rejected | Retransmission success indicator |
| `PktStoreRemovals` | Counter | Packets removed | Egress rate |
| `PktStoreSize` | Gauge | Current packet store size | Buffer utilization |

**Note**: This aligns both btrees (packet and NAK) with similar observability. The packet store counters are a separate work item but should be implemented alongside the NAK btree feature for consistency.

#### 5.1.2 Periodic NAK Counters

```go
// Periodic NAK execution counters
NakPeriodicCalls      atomic.Uint64  // Times periodicNAK() was called
NakPeriodicOriginalRuns atomic.Uint64  // Times periodicNakOriginal() executed
NakPeriodicBtreeRuns    atomic.Uint64  // Times periodicNakBtree() executed
NakPeriodicSkipped    atomic.Uint64  // Times skipped (interval not elapsed)
```

| Counter | Type | Description | Usage |
|---------|------|-------------|-------|
| `NakPeriodicCalls` | Counter | Total calls to periodicNAK | Timer health |
| `NakPeriodicOriginalRuns` | Counter | Original implementation runs | Compare Original/NAKBtree |
| `NakPeriodicBtreeRuns` | Counter | NAKBtree implementation runs | Confirm NAKBtree active |
| `NakPeriodicSkipped` | Counter | Skipped (too soon) | Unexpected if high |

#### 5.1.3 FastNAK Counters

```go
// FastNAK optimization counters
NakFastTriggers       atomic.Uint64  // FastNAK triggers (silent period exceeded)
NakFastRecentInserts  atomic.Uint64  // Sequences added via FastNAKRecent
NakFastRecentSkipped  atomic.Uint64  // FastNAKRecent skipped (gap too small)
```

| Counter | Type | Description | Usage |
|---------|------|-------------|-------|
| `NakFastTriggers` | Counter | FastNAK activations | Outage recovery events |
| `NakFastRecentInserts` | Counter | Sequences from FastNAKRecent | Recent gap handling |
| `NakFastRecentSkipped` | Counter | Gap below threshold | False positive avoidance |

#### 5.1.4 Consolidation Counters

```go
// Consolidation counters
NakConsolidationRuns    atomic.Uint64  // Times consolidation ran
NakConsolidationTimeout atomic.Uint64  // Times consolidation hit time budget
NakConsolidationEntries atomic.Uint64  // NAKEntry items produced
NakConsolidationMerged  atomic.Uint64  // Sequences merged (duplicates accepted)
```

| Counter | Type | Description | Usage |
|---------|------|-------------|-------|
| `NakConsolidationRuns` | Counter | Consolidation executions | Per-NAK cycle |
| `NakConsolidationTimeout` | Counter | Hit 2ms budget | Performance concern if high |
| `NakConsolidationEntries` | Counter | NAKEntry items created | Consolidation efficiency |
| `NakConsolidationMerged` | Counter | Sequences merged | Duplicate trade-off tracking |

#### 5.1.5 Sender Honor-Order Counters

```go
// Sender NAK order counters
NakHonorOrderRetrans    atomic.Uint64  // Packets retransmitted with honor-order
NakOriginalOrderRetrans atomic.Uint64  // Packets retransmitted with original order
```

### 5.2 Positive Path Metrics

These metrics confirm the system is working correctly. Non-zero values are expected during normal operation.

| Metric | Expected Behavior | Concern If |
|--------|-------------------|------------|
| `NakPeriodicBtreeRuns` | Increasing ~50/sec | Zero (NAKBtree not running) |
| `NakBtreeInserts` | Proportional to packet loss | Zero with loss (not detecting gaps) |
| `NakBtreeDeletes` | Close to Inserts | Much lower (packets not recovering) |
| `NakConsolidationRuns` | Equal to BtreeRuns | Lower (consolidation failing) |
| `NakFastTriggers` | Matches outage events | Zero during outages |

#### 5.2.1 Integration Test Analysis

**Requirement**: Update `contrib/integration_testing/analysis.go` to analyze the new NAK btree metrics at the end of each integration test.

The analysis should:
1. Verify positive signals (counters increasing as expected)
2. Check for unexpected error conditions
3. Compare NAKBtree vs Original behavior in parallel tests
4. Validate FastNAK triggers during Starlink outage simulations

**Implementation Notes**:
- This can be planned in detail during the integration test phase
- Should follow the existing analysis patterns in `metrics_analysis_design.md`
- New analysis functions needed:
  - `analyzeNakBtreeHealth()` - Verify NAK btree is operational
  - `analyzeNakBtreeRecovery()` - Verify inserts ≈ deletes (reordering recovery)
  - `analyzeNakBtreeExpiry()` - Verify low expiry rate (good recovery)
  - `analyzeFastNakTriggers()` - Verify triggers during outages

**Health Check Query** (Prometheus):

```promql
# Verify NAK btree is active and healthy
sum(rate(gosrt_nak_btree_inserts_total[1m])) > 0
  and
sum(rate(gosrt_nak_btree_deletes_total[1m])) > 0
  and
sum(rate(gosrt_nak_periodic_btree_runs_total[1m])) > 40  # ~50/sec expected
```

### 5.3 Negative Path Metrics

These metrics indicate problems. Non-zero values require investigation.

| Metric | Meaning | Action |
|--------|---------|--------|
| `NakConsolidationTimeout` | Consolidation taking too long | Increase budget or optimize |
| `NakBtreeExpired` (high) | Many unrecoverable packets | Check network, increase buffer |
| `NakFastRecentSkipped` (high) | FastNAKRecent triggering falsely | Tune threshold or disable |

**Alert Conditions**:

```promql
# Alert: Consolidation timeout rate > 1%
sum(rate(gosrt_nak_consolidation_timeout_total[5m]))
  /
sum(rate(gosrt_nak_consolidation_runs_total[5m])) > 0.01

# Alert: High expiry rate (unrecoverable loss)
sum(rate(gosrt_nak_btree_expired_total[5m]))
  /
sum(rate(gosrt_nak_btree_inserts_total[5m])) > 0.1
```

### 5.4 Prometheus Export

**File**: `metrics/handler.go` (additions)

```go
// NAK btree metrics
writeCounterIfNonZero(b, "gosrt_nak_btree_inserts_total",
    metrics.NakBtreeInserts.Load(),
    "socket_id", socketIdStr)
writeCounterIfNonZero(b, "gosrt_nak_btree_deletes_total",
    metrics.NakBtreeDeletes.Load(),
    "socket_id", socketIdStr)
writeCounterIfNonZero(b, "gosrt_nak_btree_expired_total",
    metrics.NakBtreeExpired.Load(),
    "socket_id", socketIdStr)
writeGauge(b, "gosrt_nak_btree_size",
    float64(metrics.NakBtreeSize.Load()),
    "socket_id", socketIdStr)

// Periodic NAK metrics
writeCounterIfNonZero(b, "gosrt_nak_periodic_calls_total",
    metrics.NakPeriodicCalls.Load(),
    "socket_id", socketIdStr)
writeCounterIfNonZero(b, "gosrt_nak_periodic_btree_runs_total",
    metrics.NakPeriodicBtreeRuns.Load(),
    "socket_id", socketIdStr)

// FastNAK metrics
writeCounterIfNonZero(b, "gosrt_nak_fast_triggers_total",
    metrics.NakFastTriggers.Load(),
    "socket_id", socketIdStr)
writeCounterIfNonZero(b, "gosrt_nak_fast_recent_inserts_total",
    metrics.NakFastRecentInserts.Load(),
    "socket_id", socketIdStr)

// Consolidation metrics
writeCounterIfNonZero(b, "gosrt_nak_consolidation_runs_total",
    metrics.NakConsolidationRuns.Load(),
    "socket_id", socketIdStr)
writeCounterIfNonZero(b, "gosrt_nak_consolidation_timeout_total",
    metrics.NakConsolidationTimeout.Load(),
    "socket_id", socketIdStr)
```

#### 5.4.1 Implementation Testing Requirements

When implementing the Prometheus export, the following must be completed:

1. **Unit Tests**: Add tests to `metrics/handler_test.go`:
   - Test each new counter is exported with correct Prometheus name
   - Test counter values are correctly retrieved from metrics struct
   - Test gauge values (like `NakBtreeSize`) are exported correctly
   - Follow existing patterns like `TestPrometheusNAKDetailMetrics`

2. **Metrics Audit**: Run and pass the metrics audit tool:
   ```bash
   go run tools/metrics-audit/main.go
   ```
   The audit verifies:
   - All atomic counters in `metrics/metrics.go` are exported to Prometheus
   - Counter names follow naming conventions
   - No orphaned counters (defined but not exported)
   - No missing counters (exported but not defined)
   - No double increments.  Each metric only incremented in a single place.

3. **Documentation**: Update `documentation/metrics_and_statistics_design.md` with new counter definitions.

### 5.5 Integration Test Validation

The parallel comparison tests should validate these metrics:

| Test | Expected |
|------|----------|
| Clean network, io_uring | `NakBtreeInserts ≈ NakBtreeDeletes` (reordering, not loss) |
| Clean network, io_uring | `NakBtreeExpired = 0` (no true loss) |
| Starlink simulation | `NakFastTriggers > 0` (outage recovery) |
| 1% loss test | `NakBtreeExpired / NakBtreeInserts < 0.05` (good recovery) |

### 5.6 Log Topics for Debugging

**Implementation status**: These log topics are **not implemented initially**. The detailed metrics (Section 5.1) provide sufficient observability for normal operation and most debugging scenarios.

**When to implement**: If additional debugging is required beyond what metrics provide (e.g., tracing individual packet sequences through the NAK btree), these log topics are the recommended approach:

| Topic | Content | When to Enable |
|-------|---------|----------------|
| `nak:btree:insert` | Sequence inserted to btree | Debugging gap detection |
| `nak:btree:delete` | Sequence deleted (arrived) | Debugging recovery |
| `nak:btree:expire` | Sequence expired | Debugging unrecoverable loss |
| `nak:scan:start` | Scan start (startSeq, threshold) | Debugging scan window |
| `nak:scan:gap` | Gap found (expected, actual) | Debugging gap detection |
| `nak:consolidate` | Consolidation result (entries, merged) | Debugging NAK building |
| `nak:fast:trigger` | FastNAK triggered | Debugging outage recovery |
| `nak:fast:recent` | FastNAKRecent gap detected | Debugging sequence jump |

**Example** (if implemented):
```bash
./server -logtopics "nak:btree:insert:nak:scan:gap"
```

**Note**: These topics would generate high-volume output at scale (one log per packet/sequence). Use only for targeted debugging, not production monitoring.

---

## 6. Error Handling and Risk Analysis

This section identifies where bugs are likely to occur, complex interactions requiring extra attention, and strategies to minimize implementation risk.

### 6.1 High-Risk Areas (Bug-Prone Code)

#### 6.1.1 Sequence Number Wraparound

**Risk Level**: 🔴 HIGH

**What can go wrong**:
- Incorrect comparison when sequence wraps from `0x7FFFFFFF` to `0`
- Gap detection produces wrong results near wraparound boundary
- NAK btree ordering breaks (sequences appear out of order)
- Consolidation merges wrong ranges

**Why it's risky**:
- Wraparound happens rarely (~14 days at 20Mbps), so bugs may not appear in short tests
- Off-by-one errors in the half-max comparison logic
- Easy to forget wraparound when writing new comparison code

**Mitigation**:
1. Use `SeqLess()` and `SeqDiff()` generics **everywhere** - never use raw `<` or `>`
2. Port test cases from goTrackRTP (known working implementation)
3. Create wraparound-specific unit tests with sequences at `0x7FFFFFFE`, `0x7FFFFFFF`, `0`, `1`
4. Add fuzz testing for sequence comparisons

**Code review checklist**:
- [ ] All sequence comparisons use `SeqLess()` family
- [ ] Gap calculation uses `SeqDiff()` not subtraction
- [ ] Consolidation range extension handles wraparound

#### 6.1.2 Lock Ordering and Deadlocks

**Risk Level**: 🔴 HIGH

**What can go wrong**:
- Deadlock between packet btree lock and NAK btree lock
- Deadlock between receiver lock and metrics updates
- Race condition if locks acquired in different order by different goroutines

**Complex interactions**:
```
Goroutine 1 (packet arrival):     Goroutine 2 (periodic NAK):
  Lock(receiver.lock)               Lock(nakBtree.mu)
  ...                               ...
  Lock(nakBtree.mu) ← WAIT          Lock(receiver.lock) ← DEADLOCK!
```

**Mitigation**:
1. **Document and enforce lock ordering**: `nakBtree.mu` → `receiver.lock` (never reverse)
2. **Minimize lock scope**: Don't hold locks during external calls
3. **Prefer separate operations**: Push() only touches NAK btree briefly (Delete)
4. **Add `-race` testing**: Run all tests with Go race detector

**Code review checklist**:
- [ ] Lock ordering documented in code comments
- [ ] No nested locks where possible
- [ ] All tests pass with `-race` flag

#### 6.1.3 NAKScanStartPoint Initialization Race

**Risk Level**: 🟡 MEDIUM (likely mitigated by design)

**What could go wrong**:
- First packet arrives simultaneously with first periodicNakBtree() call
- `nakScanStartPoint` initialized to wrong value
- Scan starts from middle of packet stream, missing early gaps

**Scenario** (theoretical):
```
T=0:    First packet seq=100 arrives
        Push() starts processing
T=0.1:  periodicNakBtree() fires (20ms timer first tick)
        Calls packetStore.Min() → returns packet 100
        Sets nakScanStartPoint = 100
T=0.2:  Packet seq=50 arrives (delayed, out of order)
        Push() processes it
        Gap 51-99 never scanned!
```

**Why this may already be mitigated**:

The design in Section 4.3.1 uses lazy initialization via `packetStore.Min()`:
- Initialization happens only in `periodicNakBtree()`, not in `Push()`
- `packetStore.Min()` returns the oldest packet currently in the btree
- Packets that arrive out-of-order go into the btree sorted by sequence
- The btree maintains correct sequence order regardless of arrival order

The remaining edge case is: packets with seq < Min() that arrive *after* `Min()` is called but *before* the scan reaches them. However:
- These packets are already in the btree (sorted correctly)
- Next periodic NAK scan will start from where we left off
- Worst case: first scan misses some early gaps (recovered in next cycle)

**Testing requirement** (important):
1. Test with concurrent first-packet and first-NAK scenarios
2. Test with severely out-of-order initial packets
3. Verify no gaps are permanently missed (recovered within 2-3 NAK cycles)
4. Consider fuzz testing with random arrival order at connection start

#### 6.1.4 TSBPD Threshold Boundary

**Risk Level**: 🟡 MEDIUM

**What can go wrong**:
- Off-by-one at "too recent" threshold boundary
- Packets exactly at threshold included/excluded incorrectly
- Inconsistent behavior when threshold changes during scan

**What's tricky**:
- Threshold is `now + (tsbpdDelay × nakRecentPercent)`
- Comparison is `packet.PktTsbpdTime > threshold`
- Edge: packet with `PktTsbpdTime == threshold` - include or exclude?

**Mitigation**:
1. Document boundary decision clearly (use `>` not `>=`)
2. Unit tests for exact boundary values
3. Consider small margin to avoid boundary issues

#### 6.1.5 sync.Pool Slice Corruption

**Risk Level**: 🟡 MEDIUM

**What can go wrong**:
- Returning slice to pool while still in use
- Slice content corrupted by concurrent user
- Memory leak if pool entries not returned

**Scenario**:
```go
entries := *entriesPtr
defer func() {
    *entriesPtr = entries[:0]
    nakEntryPool.Put(entriesPtr)  // Return to pool
}()
return r.entriesToNakList(entries)  // entries still being read!
```

**Why our design is safe**:
- `entriesToNakList()` copies data to fresh slice
- `entries` is fully consumed before defer runs
- Return value is independent of pooled slice

**Mitigation**:
1. Never return pooled slice directly to caller
2. Document pool lifetime in code comments
3. Add tests that detect use-after-pool-return (with race detector)

#### 6.1.6 Consolidation Time Budget Exceeded

**Risk Level**: 🟢 LOW

**What can go wrong**:
- Very large NAK btree causes consolidation to always timeout
- Partial consolidation produces incomplete NAK list
- Starvation: always timing out, never completing

**Mitigation**:
1. Metric tracks timeout frequency
2. Accept partial results (most urgent entries are first anyway)
3. Alert if timeout rate exceeds threshold
4. Consider adaptive budget based on btree size

### 6.2 Complex Interactions

#### 6.2.1 FastNAK vs Periodic NAK Race

**Interaction**:
```
T=0:    Silent period exceeds threshold
T=50ms: Packet arrives, triggers FastNAK
        FastNAK calls periodicNakBtree()
T=60ms: Periodic timer fires, also calls periodicNakBtree()
        → Double NAK for same gaps?
```

**Protection in design**:
```go
if r.timeSinceLastNak() < r.periodicNAKInterval {
    return  // Don't trigger if we just sent a NAK
}
```

**Testing requirement**: Verify no duplicate NAKs within interval.

#### 6.2.2 FastNAKRecent Sequence Jump Detection

**Interaction**: io_uring reordering may cause `lastDataPacketSeq` to not be the actual highest sequence.

**Scenario**:
```
Actual sequence: 100, 101, 102, 103 (all arrive)
io_uring order:  102, 100, 103, 101

lastDataPacketSeq progression: 102, 102, 103, 103
  (only updates if higher, but doesn't handle reordering correctly)
```

**Risk**: After outage, gap detection may be inaccurate.

**Mitigation**:
1. Use minimum gap threshold (10 packets) to avoid false positives
2. Accept that FastNAKRecent may add some extra sequences (harmless)
3. Feature flag allows disabling if problematic

#### 6.2.3 Packet Arrival During Scan

**Interaction**: While scanning packet btree, new packets arrive.

**What happens**:
- Packet btree: Read lock during scan
- New packet: Write lock for insert
- Result: Scan may see partially-updated btree state

**Protection**: btree iteration is snapshot-consistent (Google btree behavior).

**Testing requirement**: Stress test with concurrent inserts during iteration.

### 6.3 Defensive Measures

#### 6.3.1 Runtime Failure Scenarios

**Question**: How could the NAK btree feature fail at runtime?

**Answer**: With proper testing, it shouldn't. The NAK btree uses:
- Standard Go data structures (Google btree library, well-tested)
- Atomic operations (built into Go runtime)
- No external dependencies, network calls, or file I/O

**Realistic "failure" scenarios are actually bugs**:
- Nil pointer dereference → bug in implementation, caught by testing
- Deadlock → bug in lock ordering, caught by race detector
- Wrong results → bug in logic, caught by unit/integration tests

**Conclusion**: We do NOT implement panic recovery or runtime fallback. Instead:
1. Rely on thorough testing (Section 7) to catch bugs before deployment
2. Use feature flag to enable/disable at startup (not runtime switching)
3. If a bug is found in production, fix it and redeploy

**Why no runtime fallback**:
- Adds complexity
- Masks bugs (silent fallback hides problems)
- If NAK btree has a bug, the original implementation may also have issues in the same scenario

#### 6.3.2 NAK btree Size Limits

**What could cause large NAK btree?**

1. **Extreme packet loss** - Many missing sequences to track
2. **Coding bug** - Entries not being removed correctly (mitigated by testing)

**Calculating expected size**:

| Data Rate | Packet Size | Packets/sec | 3s Buffer | 20% Loss | NAK btree |
|-----------|-------------|-------------|-----------|----------|-----------|
| 50 Mbps | 1316 bytes | ~4,750 | ~14,250 | ~2,850 | ~2,850 |
| 100 Mbps | 1316 bytes | ~9,500 | ~28,500 | ~5,700 | ~5,700 |

**Analysis**:
- At 100 Mbps with 20% loss (extreme), NAK btree ≈ 5,700 entries
- NAK btree is roughly `lossRate × packetBuffer` in size
- 100,000 limit = ~17× worst-case scenario (very generous)

**Recommended approach**:

1. **Primary defense**: Prometheus alerting when `NakBtreeSize` exceeds threshold
   ```promql
   # Alert: NAK btree size > 10,000 (investigate)
   gosrt_nak_btree_size > 10000

   # Alert: NAK btree size > 50,000 (critical)
   gosrt_nak_btree_size > 50000
   ```

2. **Safety valve** (optional): Hard limit to prevent runaway memory
   ```go
   const maxNakBtreeSize = 50000  // ~50K - well above normal operation

   func (nb *nakBtree) Insert(seq uint32) bool {
       if nb.tree.Len() >= maxNakBtreeSize {
           // Log warning - this shouldn't happen in normal operation
           // Btree full - trim oldest entries
           nb.trimOldest(1000)
       }
       nb.tree.ReplaceOrInsert(seq)
       return true
   }
   ```

**Note**: If we hit the hard limit in production, it indicates either:
- Network conditions far worse than expected (investigate network)
- A bug in expiry/deletion logic (investigate code)

#### 6.3.2a Packet btree Size Limits

**Similar risk applies to packet btree**: The packet btree (holding received packets awaiting TSBPD delivery) can also grow unbounded if packets aren't being released.

**Calculating expected packet btree size**:

| Data Rate | TSBPD Buffer | Expected Packets |
|-----------|--------------|------------------|
| 50 Mbps | 3s | ~14,250 |
| 100 Mbps | 3s | ~28,500 |
| 100 Mbps | 5s | ~47,500 |
| 100 Mbps | 10s | ~95,000 |

**Key difference from NAK btree**:
- Packet btree size is primarily determined by `dataRate × tsbpdDelay`
- NAK btree size is determined by `dataRate × tsbpdDelay × lossRate`
- Packet btree is typically 5-100× larger than NAK btree

**What could cause excessive packet btree growth?**:
1. **Bug in TSBPD release** - Packets not being delivered to application
2. **Slow consumer** - Application not reading fast enough
3. **Bug in packet removal** - Packets not being removed after delivery

**Recommended approach**:

1. **Primary defense**: Prometheus alerting
   ```promql
   # Alert: Packet btree larger than expected for configured buffer
   # At 100 Mbps, 3s buffer: expect ~30K packets
   gosrt_pkt_store_size > 100000

   # Alert: Packet btree growing (not stable)
   delta(gosrt_pkt_store_size[1m]) > 1000
   ```

2. **Safety valve** (in existing code or new):
   ```go
   const maxPacketStoreSize = 200000  // ~200K packets

   // Note: This is much larger than NAK btree limit
   // 200K ≈ 100 Mbps × 21 seconds of buffer
   ```

**Note**: The packet btree size limit may already be implicitly enforced by memory pressure or existing SRT buffer management. Verify current implementation before adding explicit limits.

#### 6.3.3 Integration Test Validation: Btree Size Ratios

**Key insight**: The ratio of NAK btree size to packet btree size should approximate the packet loss rate.

```
NAK btree size / Packet btree size ≈ Loss Rate
```

**Integration test validation** (add to `analysis.go`):

For network impairment tests with known loss rate:

```go
func analyzeNakBtreeRatio(metrics TestMetrics, expectedLossRate float64) AnalysisResult {
    nakSize := metrics.NakBtreeSize
    pktSize := metrics.PktStoreSize

    if pktSize == 0 {
        return fail("No packets in store")
    }

    actualRatio := float64(nakSize) / float64(pktSize)
    tolerance := 0.5  // Allow 50% variance

    minExpected := expectedLossRate * (1 - tolerance)
    maxExpected := expectedLossRate * (1 + tolerance)

    if actualRatio < minExpected || actualRatio > maxExpected {
        return fail(fmt.Sprintf(
            "NAK/Packet ratio %.2f outside expected range [%.2f, %.2f] for %.0f%% loss",
            actualRatio, minExpected, maxExpected, expectedLossRate*100))
    }

    return pass()
}
```

**Expected ratios by test scenario**:

| Test | Expected Loss | Expected Ratio | Alert If |
|------|---------------|----------------|----------|
| Clean network | 0% | ~0 | > 0.01 |
| 1% loss | 1% | ~0.01 | > 0.05 |
| 5% loss | 5% | ~0.05 | > 0.15 |
| Starlink sim | Variable | ~0.02-0.10 | > 0.20 |

**Prometheus alert**:

```promql
# Alert: NAK btree ratio unexpectedly high (possible bug or extreme loss)
(gosrt_nak_btree_size / gosrt_pkt_store_size) > 0.30

# Alert: NAK btree ratio high for clean network (should be ~0)
(gosrt_nak_btree_size / gosrt_pkt_store_size) > 0.01
  unless on(instance) (network_impairment_active == 1)
```

**Note**: This ratio check requires adding `PktStoreSize` metric to the packet store (see Section 5.1.1a for packet store counter alignment).

#### 6.3.3 Consolidation Timeout Handling

Return partial results rather than nothing:

```go
// Current design already handles this:
// - Iterates oldest-first (most urgent)
// - Returns whatever was consolidated before timeout
// - Remaining entries will be included in next cycle
```

### 6.4 Implementation Risk Mitigation

#### 6.4.1 Incremental Implementation Order

Implement in order of dependency, testing each step:

| Phase | Component | Risk | Testing Gate |
|-------|-----------|------|--------------|
| 1 | `seq_math.go` | 🔴 HIGH | All wraparound tests pass |
| 2 | `nakBtree` struct | 🟡 MED | Insert/Delete/Iterate tests pass |
| 3 | `consolidateNakBtree()` | 🟡 MED | Consolidation tests pass |
| 4 | `periodicNakBtree()` | 🟡 MED | Scan tests pass |
| 5 | FastNAK | 🟢 LOW | Trigger tests pass |
| 6 | Integration | 🟡 MED | Isolation tests show 0 false gaps |

**Gate rule**: Do not proceed to next phase until current phase tests pass.

#### 6.4.2 Feature Flag Rollout

```go
// Start disabled
UseNakBtree = false

// Enable only when:
// 1. All unit tests pass
// 2. Isolation tests show improvement
// 3. Parallel comparison tests confirm parity
```

#### 6.4.3 Code Review Focus Areas

| File | Focus Area | Reviewer Should Check |
|------|------------|----------------------|
| `seq_math.go` | Wraparound correctness | Comparison logic, edge cases |
| `nak_btree.go` | Lock safety | Deadlock potential, race conditions |
| `nak_consolidate.go` | Pool usage | Lifetime, corruption potential |
| `receive.go` changes | Lock ordering | No nested locks, correct order |
| `fast_nak.go` | Race conditions | Concurrent access to atomics |

#### 6.4.4 Pre-Merge Checklist

Before merging NAK btree feature:

- [ ] All unit tests pass (`go test ./...`)
- [ ] All tests pass with race detector (`go test -race ./...`)
- [ ] Benchmarks show acceptable overhead (`go test -bench ./...`)
- [ ] Isolation test `Isolation-Server-IoUringRecv` shows 0 gaps on clean network
- [ ] Parallel comparison shows NAKBtree ≤ Original in gap detection
- [ ] Metrics audit passes (`go run tools/metrics-audit/main.go`)
- [ ] No new linter warnings
- [ ] Code review completed with focus areas addressed

---

## 7. Comprehensive Testing

This section consolidates all testing requirements into a comprehensive test plan. Testing is the primary defense against bugs (see Section 6.3.1).

### 7.1 Test Categories Overview

| Category | Tool | Purpose | When to Run |
|----------|------|---------|-------------|
| **Unit Tests** | `go test ./...` | Verify individual functions | Every commit |
| **Race Detection** | `go test -race ./...` | Detect data races | Every commit |
| **Benchmarks** | `go test -bench ./...` | Measure performance | Before/after changes |
| **Integration Tests** | `make test-isolation` | Verify io_uring behavior | Feature complete |
| **Comparison Tests** | `make test-parallel` | Compare Original vs NAKBtree | Feature complete |

### 7.2 Unit Tests by File

#### 7.2.1 New Test Files

| Test File | Tests For | Key Test Cases |
|-----------|-----------|----------------|
| `circular/seq_math_test.go` | Sequence wraparound | Normal comparison, wraparound at max, edge at half-max |
| `congestion/live/nak_btree_test.go` | NAK btree operations | Insert, Delete, DeleteBefore, Iterate, Len, concurrent access |
| `congestion/live/nak_consolidate_test.go` | Range consolidation | Contiguous, gaps, MergeGap, timeout, pool reuse |
| `congestion/live/fast_nak_test.go` | FastNAK logic | Trigger threshold, recent detection, sequence jump |
| `congestion/live/receive_benchmark_test.go` | Push performance | With/without NAKBtree overhead |

#### 7.2.2 seq_math_test.go

```go
func TestSeqLess(t *testing.T) {
    tests := []struct {
        name     string
        s1, s2   uint32
        expected bool
    }{
        // Normal cases
        {"0 < 1", 0, 1, true},
        {"1 < 0", 1, 0, false},
        {"100 < 101", 100, 101, true},

        // Wraparound cases (CRITICAL)
        {"0 vs MaxSeq", 0, 0x7FFFFFFF, false},       // 0 is "ahead" of max
        {"MaxSeq vs 0", 0x7FFFFFFF, 0, true},        // max is "behind" 0
        {"1 vs MaxSeq", 1, 0x7FFFFFFF, false},
        {"MaxSeq vs 1", 0x7FFFFFFF, 1, true},
        {"MaxSeq-1 vs 0", 0x7FFFFFFE, 0, true},

        // Half-max boundary
        {"0 vs HalfMax", 0, 0x3FFFFFFF, true},
        {"HalfMax vs 0", 0x3FFFFFFF, 0, false},

        // Same value
        {"same 0", 0, 0, false},
        {"same 1000", 1000, 1000, false},
    }
    // ... run tests
}

func TestSeqDiff(t *testing.T) {
    // Similar structure with wraparound cases
}

func FuzzSeqLess(f *testing.F) {
    // Fuzz testing for edge cases
    f.Add(uint32(0), uint32(1))
    f.Add(uint32(0x7FFFFFFF), uint32(0))
    f.Fuzz(func(t *testing.T, s1, s2 uint32) {
        // Verify transitivity and consistency
    })
}
```

#### 7.2.3 Concurrent Access Tests (receive_concurrent_test.go)

**Note**: Basic btree operations (Insert, Delete, Iterate) are well-tested by Google's btree library. The real risk is in our concurrent access patterns between packet btree and NAK btree.

**Focus areas**:
1. Packet arrival (Push) concurrent with periodicNakBtree()
2. Lock ordering between packet btree and NAK btree
3. Atomic operations on shared state
4. Performance under concurrent load

```go
// Test: Concurrent packet arrival and NAK btree deletion
func TestConcurrent_Push_NakBtreeDelete(t *testing.T) {
    // Scenario: Packets arriving while periodicNakBtree is running
    // Risk: Lock ordering, race on NAK btree

    recv := createTestReceiver(withNakBtree)
    var wg sync.WaitGroup

    // Goroutine 1: Rapid packet arrival (Push)
    // - Inserts into packet btree
    // - Deletes from NAK btree
    wg.Add(1)
    go func() {
        defer wg.Done()
        for i := uint32(0); i < 10000; i++ {
            pkt := createPacket(i)
            recv.Push(pkt)
        }
    }()

    // Goroutine 2: Periodic NAK timer
    // - Scans packet btree (read)
    // - Inserts into NAK btree (write)
    // - Consolidates NAK btree (read)
    wg.Add(1)
    go func() {
        defer wg.Done()
        for i := 0; i < 500; i++ {  // 500 × 20ms = 10s simulated
            recv.periodicNakBtree(now())
            time.Sleep(time.Millisecond)  // Simulate timer interval
        }
    }()

    wg.Wait()
    // Verify: No deadlock, no panic, data consistent
}

// Test: Lock ordering verification
func TestConcurrent_LockOrdering(t *testing.T) {
    // Ensure we never acquire locks in wrong order
    // Expected order: nakBtree.mu → receiver.lock

    recv := createTestReceiver(withNakBtree)

    // Hammer with concurrent operations that touch both locks
    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(2)

        // Push path: packet btree write, NAK btree delete
        go func() {
            defer wg.Done()
            for j := uint32(0); j < 1000; j++ {
                recv.Push(createPacket(j))
            }
        }()

        // NAK path: packet btree read, NAK btree write
        go func() {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                recv.periodicNakBtree(now())
            }
        }()
    }

    // If this completes without hanging, lock ordering is correct
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Success
    case <-time.After(30 * time.Second):
        t.Fatal("Deadlock detected - lock ordering issue")
    }
}

// Test: Atomic state consistency
func TestConcurrent_AtomicState(t *testing.T) {
    // Verify atomic variables stay consistent under concurrent access
    // - lastPacketArrivalTime
    // - lastDataPacketSeq
    // - nakScanStartPoint

    recv := createTestReceiver(withNakBtree)
    var wg sync.WaitGroup

    // Writers: Push updates atomics
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := uint32(0); j < 1000; j++ {
                recv.Push(createPacket(j))
            }
        }()
    }

    // Readers: FastNAK checks atomics
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 1000; j++ {
                recv.checkFastNak(time.Now())
            }
        }()
    }

    wg.Wait()
    // No race detector failures = success
}

// Test: High-frequency concurrent operations
func TestConcurrent_HighFrequency(t *testing.T) {
    // Simulate realistic high-throughput scenario
    // 100 Mbps ≈ 9500 pps, periodic NAK every 20ms

    recv := createTestReceiver(withNakBtree)
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    var pushCount, nakCount atomic.Uint64
    var wg sync.WaitGroup

    // Packet arrival at ~10K pps
    wg.Add(1)
    go func() {
        defer wg.Done()
        seq := uint32(0)
        ticker := time.NewTicker(100 * time.Microsecond)  // 10K/sec
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                recv.Push(createPacket(seq))
                seq++
                pushCount.Add(1)
            }
        }
    }()

    // Periodic NAK at 50 Hz
    wg.Add(1)
    go func() {
        defer wg.Done()
        ticker := time.NewTicker(20 * time.Millisecond)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                recv.periodicNakBtree(now())
                nakCount.Add(1)
            }
        }
    }()

    wg.Wait()

    t.Logf("Processed %d packets, %d NAK cycles", pushCount.Load(), nakCount.Load())
    // Verify: ~50K packets, ~250 NAK cycles in 5 seconds
}
```

**Run with race detector**: `go test -race -run TestConcurrent ./congestion/live/`

#### 7.2.4 nak_consolidate_test.go

```go
func TestConsolidateNakBtree_Empty(t *testing.T) {
    // Empty btree returns empty list
}

func TestConsolidateNakBtree_SingleEntry(t *testing.T) {
    // Single entry returns single NAK
}

func TestConsolidateNakBtree_Contiguous(t *testing.T) {
    // [100,101,102] → Range(100,102)
}

func TestConsolidateNakBtree_WithGaps(t *testing.T) {
    // [100,101,105,106] with MergeGap=2 → Range(100,101), Range(105,106)
}

func TestConsolidateNakBtree_MergeGap(t *testing.T) {
    // [100,101,104,105] with MergeGap=3 → Range(100,105)
    // Verify duplicates accepted
}

func TestConsolidateNakBtree_Timeout(t *testing.T) {
    // Large btree, short budget → partial result
    // Verify oldest entries included first
}

func TestConsolidateNakBtree_PoolReuse(t *testing.T) {
    // Call multiple times, verify no memory growth
    // Verify pool entries reused
}

func TestConsolidateNakBtree_Wraparound(t *testing.T) {
    // Entries spanning MaxSeq → 0
}
```

#### 7.2.5 fast_nak_test.go

```go
func TestCheckFastNak_NoTrigger(t *testing.T) {
    // Silent period < threshold → no trigger
}

func TestCheckFastNak_Trigger(t *testing.T) {
    // Silent period >= threshold → trigger
}

func TestCheckFastNak_RecentNAK(t *testing.T) {
    // Silent period exceeded but NAK sent recently → no trigger
}

func TestFastNakRecent_SequenceJump(t *testing.T) {
    // Large sequence jump after silence → entries added
}

func TestFastNakRecent_SmallJump(t *testing.T) {
    // Small sequence jump (< threshold) → no entries added
}

func TestFastNakRecent_Disabled(t *testing.T) {
    // FastNakRecentEnabled=false → no entries added
}
```

### 7.3 Race Detection Testing

**Command**: `go test -race ./...`

**Critical sections to test**:

| File | Function | Concurrent Access |
|------|----------|-------------------|
| `nak_btree.go` | All methods | Insert/Delete/Iterate from different goroutines |
| `receive.go` | `Push()` + `periodicNakBtree()` | Packet arrival + NAK timer |
| `receive.go` | `lastPacketArrivalTime` | Atomic store/load |
| `receive.go` | `nakScanStartPoint` | Atomic store/load |

**Test pattern**:

```go
func TestNakBtree_Race(t *testing.T) {
    nb := newNakBtree(32)
    var wg sync.WaitGroup

    // Writer goroutine
    wg.Add(1)
    go func() {
        defer wg.Done()
        for i := uint32(0); i < 10000; i++ {
            nb.Insert(i)
        }
    }()

    // Deleter goroutine
    wg.Add(1)
    go func() {
        defer wg.Done()
        for i := uint32(0); i < 10000; i++ {
            nb.Delete(i)
        }
    }()

    // Iterator goroutine
    wg.Add(1)
    go func() {
        defer wg.Done()
        for i := 0; i < 100; i++ {
            nb.Iterate(func(seq uint32) bool { return true })
        }
    }()

    wg.Wait()
}
```

### 7.4 Corner Cases (from Section 6.1)

| Risk Area | Test Case | Test File |
|-----------|-----------|-----------|
| **Sequence Wraparound** | Sequences at 0x7FFFFFFE, 0x7FFFFFFF, 0, 1 | `seq_math_test.go` |
| **Scan Init Race** | First packet + first periodicNAK concurrent | `receive_test.go` |
| **TSBPD Boundary** | Packet exactly at threshold | `receive_test.go` |
| **Pool Corruption** | Verify data copied before pool return | `nak_consolidate_test.go` |
| **Consolidation Timeout** | Very large btree with short budget | `nak_consolidate_test.go` |
| **FastNAK Race** | FastNAK + periodicNAK within interval | `fast_nak_test.go` |

### 7.5 Integration Tests

Integration tests validate the complete system behavior. See referenced documents for full details.

#### 7.5.1 Parallel Isolation Tests

**Reference**: `parallel_isolation_test_plan.md`

**Purpose**: Isolate which component causes excessive gap detection with io_uring.

**Key test for NAK btree**:

| Test | Config | Expected with NAKBtree |
|------|--------|------------------------|
| `Isolation-Server-IoUringRecv` | Server uses io_uring recv | 0 false gaps on clean network |

**Command**: `make test-isolation CONFIG=Isolation-Server-IoUringRecv`

**Success criteria**:
- `packets_lost_total` (gaps detected) = 0 on clean network
- `NakBtreeInserts` ≈ `NakBtreeDeletes` (reordering, not loss)
- `NakBtreeExpired` = 0

#### 7.5.2 Parallel Comparison Tests

**Reference**: `parallel_comparison_test_design.md`

**Purpose**: Compare Original vs NAKBtree behavior under identical conditions.

**Key comparisons**:

| Metric | Original | NAKBtree | Pass Criteria |
|--------|----------|----------|---------------|
| False gaps (clean network) | Baseline | Must be ≤ | NAKBtree <= Original |
| True gaps (1% loss) | Baseline | Must be ≈ | Within 10% |
| Retransmissions | Baseline | Must be ≤ | NAKBtree <= Original |

**Command**: `make test-parallel CONFIG=Parallel-NAKBtree-Comparison`

#### 7.5.3 Network Impairment Tests

**Reference**: `integration_testing_design.md`

**Scenarios**:

| Scenario | Network Config | NAKBtree Behavior |
|----------|----------------|-------------------|
| Clean network | 0% loss, 0ms latency | 0 gaps detected |
| 1% loss | 1% netem loss | Gaps detected, ~99% recovered |
| 5% loss | 5% netem loss | Gaps detected, ~95% recovered |
| Starlink sim | Variable outages | FastNAK triggers, good recovery |

#### 7.5.4 Integration Test Validation (analysis.go)

As noted in Section 5.2.1, update `analysis.go` with:

```go
// NAK btree specific analysis functions
func analyzeNakBtreeHealth(metrics TestMetrics) AnalysisResult
func analyzeNakBtreeRecovery(metrics TestMetrics) AnalysisResult
func analyzeNakBtreeExpiry(metrics TestMetrics) AnalysisResult
func analyzeFastNakTriggers(metrics TestMetrics) AnalysisResult
func analyzeNakBtreeRatio(metrics TestMetrics, expectedLoss float64) AnalysisResult
```

### 7.6 Stress Tests

#### 7.6.1 High Packet Rate

```go
func TestNakBtree_HighRate(t *testing.T) {
    // Simulate 100 Mbps packet rate
    // Verify no performance degradation
    // Verify btree size stays bounded
}
```

#### 7.6.2 Long Duration

```go
func TestNakBtree_LongDuration(t *testing.T) {
    // Run for 10 minutes
    // Verify no memory growth
    // Verify metrics stay consistent
}
```

#### 7.6.3 High Loss Rate

```go
func TestNakBtree_HighLoss(t *testing.T) {
    // Simulate 20% packet loss
    // Verify btree size limit enforced
    // Verify oldest entries processed first
}
```

### 7.7 Test Execution Order

Follow the phased approach from Section 6.4.1:

| Phase | Tests | Gate |
|-------|-------|------|
| 1 | `seq_math_test.go` | All wraparound tests pass |
| 2 | `nak_btree_test.go` | All btree operations pass |
| 3 | `nak_consolidate_test.go` | Consolidation tests pass |
| 4 | `fast_nak_test.go` | FastNAK tests pass |
| 5 | Race detection | `go test -race` passes |
| 6 | Benchmarks | Performance acceptable |
| 7 | Isolation tests | 0 false gaps on clean network |
| 8 | Comparison tests | NAKBtree ≤ Original |

### 7.8 CI/CD Integration

```yaml
# .github/workflows/nak_btree.yml
name: NAK btree Tests

on: [push, pull_request]

jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Unit Tests
        run: go test ./...
      - name: Race Detection
        run: go test -race ./...
      - name: Benchmarks
        run: go test -bench ./... -benchmem
      - name: Metrics Audit
        run: go run tools/metrics-audit/main.go

  integration-tests:
    runs-on: ubuntu-latest
    needs: unit-tests
    steps:
      - name: Isolation Test
        run: sudo make test-isolation CONFIG=Isolation-Server-IoUringRecv
      # Note: Full integration tests may require dedicated environment
```

---

## 8. Comprehensive Benchmarking

Benchmarking validates that NAK btree doesn't introduce unacceptable overhead. Focus on:
1. Hot path operations (every packet)
2. Periodic operations (every 20ms)
3. Scalability (performance vs btree size)

**Command**: `go test -bench=. -benchmem ./congestion/live/`

### 8.1 Critical Path Benchmarks

#### 8.1.1 Push Path (Every Packet)

The `Push()` function is the hottest path - called for every received packet.

```go
// receive_benchmark_test.go

func BenchmarkPush_WithoutNakBtree(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: false)
    pkt := createBenchPacket()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        pkt.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
        recv.Push(pkt)
    }
}

func BenchmarkPush_WithNakBtree(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)
    pkt := createBenchPacket()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        pkt.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
        recv.Push(pkt)
    }
}

func BenchmarkPush_WithNakBtree_WithFastNak(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true, fastNakEnabled: true)
    pkt := createBenchPacket()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        pkt.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
        recv.Push(pkt)
    }
}
```

**Expected results**:

| Benchmark | Target | Concern If |
|-----------|--------|------------|
| `Push_WithoutNakBtree` | Baseline | N/A |
| `Push_WithNakBtree` | < 10% overhead | > 20% overhead |
| `Push_WithNakBtree_WithFastNak` | < 15% overhead | > 25% overhead |

#### 8.1.2 TSBPD Tick + NAK btree Cleanup Interaction

The packet btree releases packets via TSBPD (Tick), and NAK btree cleanup happens during periodicNakBtree. Benchmark the combined overhead.

```go
func BenchmarkTick_WithNakBtreeCleanup(b *testing.B) {
    // Simulate realistic scenario: packets flowing, periodic expiry
    recv := createBenchReceiver(useNakBtree: true)

    // Pre-populate: 1000 packets in packet btree, 50 in NAK btree (5% loss)
    for i := uint32(0); i < 1000; i++ {
        if i % 20 == 0 {
            recv.nakBtree.Insert(i)  // Missing
        } else {
            recv.packetStore.Insert(createPacket(i))
        }
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Simulate Tick releasing oldest packets
        recv.packetStore.RemoveAll(func(p packet.Packet) bool {
            return p.Header().PacketSequenceNumber.Val() < uint32(i % 100)
        }, nil)

        // NAK btree cleanup (happens in periodicNakBtree)
        recv.expireNakEntries(now())
    }
}

func BenchmarkCombined_Push_Tick_PeriodicNak(b *testing.B) {
    // Most realistic: interleaved Push, Tick, and periodicNak
    recv := createBenchReceiver(useNakBtree: true)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        seq := uint32(i)

        // Push (every iteration)
        recv.Push(createPacket(seq))

        // Tick (every 10 iterations, simulating 10ms)
        if i % 10 == 0 {
            recv.packetStore.RemoveAll(func(p packet.Packet) bool {
                return p.Header().PktTsbpdTime < uint64(i)
            }, nil)
        }

        // periodicNak (every 20 iterations, simulating 20ms)
        if i % 20 == 0 {
            recv.periodicNakBtree(now())
        }
    }
}
```

**Note**: Basic NAK btree Delete operations are already tested by Google's btree library benchmarks. We focus on our integration patterns.

### 8.2 Periodic Operation Benchmarks

#### 8.2.1 Consolidation Scalability

**Key benchmark**: How does consolidation time scale with NAK btree size?

```go
func BenchmarkConsolidate_10entries(b *testing.B) {
    benchmarkConsolidate(b, 10)
}

func BenchmarkConsolidate_100entries(b *testing.B) {
    benchmarkConsolidate(b, 100)
}

func BenchmarkConsolidate_500entries(b *testing.B) {
    benchmarkConsolidate(b, 500)
}

func BenchmarkConsolidate_1000entries(b *testing.B) {
    benchmarkConsolidate(b, 1000)
}

func BenchmarkConsolidate_5000entries(b *testing.B) {
    benchmarkConsolidate(b, 5000)
}

func benchmarkConsolidate(b *testing.B, size int) {
    recv := createBenchReceiver(useNakBtree: true)

    // Populate NAK btree with 'size' entries
    // Simulate gaps: every 3rd sequence missing
    for i := 0; i < size; i++ {
        recv.nakBtree.Insert(uint32(i * 3))
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.consolidateNakBtree()
    }
}
```

**Expected scaling**:

| Entries | Expected Time | Concern If |
|---------|---------------|------------|
| 10 | < 1µs | > 5µs |
| 100 | < 10µs | > 50µs |
| 500 | < 50µs | > 200µs |
| 1000 | < 100µs | > 500µs |
| 5000 | < 500µs | > 2ms (hitting timeout) |

**Critical check**: Performance should scale roughly O(n), not O(n²). If we see exponential growth, there's a problem.

#### 8.2.2 Consolidation with MergeGap Variations

```go
func BenchmarkConsolidate_MergeGap0(b *testing.B) {
    benchmarkConsolidateWithMergeGap(b, 500, 0)
}

func BenchmarkConsolidate_MergeGap3(b *testing.B) {
    benchmarkConsolidateWithMergeGap(b, 500, 3)
}

func BenchmarkConsolidate_MergeGap10(b *testing.B) {
    benchmarkConsolidateWithMergeGap(b, 500, 10)
}

func benchmarkConsolidateWithMergeGap(b *testing.B, size int, mergeGap uint32) {
    recv := createBenchReceiver(useNakBtree: true, nakMergeGap: mergeGap)

    // Populate with scattered entries
    for i := 0; i < size; i++ {
        recv.nakBtree.Insert(uint32(i * 5))  // Gap of 4 between each
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.consolidateNakBtree()
    }
}
```

#### 8.2.3 Periodic NAK Full Cycle

```go
func BenchmarkPeriodicNakBtree_Empty(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)
    // Populate packet btree but no gaps
    for i := uint32(0); i < 1000; i++ {
        recv.packetStore.Insert(createPacket(i))
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.periodicNakBtree(now())
    }
}

func BenchmarkPeriodicNakBtree_WithGaps(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)
    // Populate with 5% gaps
    for i := uint32(0); i < 1000; i++ {
        if i % 20 != 0 {  // Skip every 20th = 5% gaps
            recv.packetStore.Insert(createPacket(i))
        }
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.periodicNakBtree(now())
    }
}
```

#### 8.2.5 Concurrent Stress Benchmark (Push + Tick + PeriodicNAK)

**Critical benchmark**: Tests realistic concurrent operation with all three paths running simultaneously in separate goroutines. Detects races, deadlocks, and poor interactions.

```go
func BenchmarkConcurrent_FullSystem(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)

    // Control channels
    ctx, cancel := context.WithCancel(context.Background())
    var wg sync.WaitGroup

    // Metrics
    var pushCount, tickCount, nakCount atomic.Uint64

    // Goroutine 1: High-rate packet arrival (Push)
    // Simulates ~10K pps (100 Mbps)
    wg.Add(1)
    go func() {
        defer wg.Done()
        seq := uint32(0)
        for {
            select {
            case <-ctx.Done():
                return
            default:
                recv.Push(createPacket(seq))
                seq++
                pushCount.Add(1)
            }
        }
    }()

    // Goroutine 2: TSBPD Tick (packet btree expiry)
    // Runs every ~10ms
    wg.Add(1)
    go func() {
        defer wg.Done()
        ticker := time.NewTicker(10 * time.Millisecond)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                // Simulate Tick releasing oldest packets
                released := recv.packetStore.RemoveAll(
                    func(p packet.Packet) bool {
                        return p.Header().PktTsbpdTime < uint64(time.Now().UnixMicro())
                    },
                    nil,
                )
                _ = released
                tickCount.Add(1)
            }
        }
    }()

    // Goroutine 3: Periodic NAK (gap detection + NAK btree cleanup)
    // Runs every ~20ms
    wg.Add(1)
    go func() {
        defer wg.Done()
        ticker := time.NewTicker(20 * time.Millisecond)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                recv.periodicNakBtree(uint64(time.Now().UnixMicro()))
                nakCount.Add(1)
            }
        }
    }()

    // Run for duration of benchmark
    b.ResetTimer()
    time.Sleep(time.Duration(b.N) * time.Microsecond)
    cancel()

    // Wait with timeout (detect deadlock)
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Success
    case <-time.After(5 * time.Second):
        b.Fatal("Deadlock detected - goroutines did not exit")
    }

    b.ReportMetric(float64(pushCount.Load()), "pushes")
    b.ReportMetric(float64(tickCount.Load()), "ticks")
    b.ReportMetric(float64(nakCount.Load()), "naks")
}

func BenchmarkConcurrent_HighContention(b *testing.B) {
    // Extreme case: Maximum contention on both btrees
    recv := createBenchReceiver(useNakBtree: true)

    ctx, cancel := context.WithCancel(context.Background())
    var wg sync.WaitGroup

    // 4 Push goroutines (high contention on packet btree + NAK btree delete)
    for i := 0; i < 4; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            seq := uint32(id * 1000000)
            for {
                select {
                case <-ctx.Done():
                    return
                default:
                    recv.Push(createPacket(seq))
                    seq++
                }
            }
        }(i)
    }

    // 2 Tick goroutines (contention on packet btree removal)
    for i := 0; i < 2; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for {
                select {
                case <-ctx.Done():
                    return
                default:
                    recv.packetStore.RemoveAll(
                        func(p packet.Packet) bool { return false },
                        nil,
                    )
                    time.Sleep(time.Millisecond)
                }
            }
        }()
    }

    // 2 PeriodicNAK goroutines (contention on NAK btree)
    for i := 0; i < 2; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for {
                select {
                case <-ctx.Done():
                    return
                default:
                    recv.periodicNakBtree(uint64(time.Now().UnixMicro()))
                    time.Sleep(5 * time.Millisecond)
                }
            }
        }()
    }

    b.ResetTimer()
    time.Sleep(time.Duration(b.N) * time.Microsecond)
    cancel()

    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Success - no deadlock
    case <-time.After(5 * time.Second):
        b.Fatal("Deadlock detected under high contention")
    }
}
```

**Run with race detector**: `go test -race -bench=BenchmarkConcurrent -timeout=60s ./congestion/live/`

**What these benchmarks detect**:

| Issue | How Detected |
|-------|--------------|
| **Deadlock** | Timeout waiting for goroutines |
| **Race condition** | `-race` flag failures |
| **Lock contention** | Low throughput in metrics |
| **Performance cliff** | Sudden drop in pushes/sec |

### 8.3 Comparison: Original vs NAKBtree

#### 8.3.1 A/B Benchmark Suite

```go
func BenchmarkPeriodicNak_Original(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: false)
    setupRealisticBuffer(recv, 1000, 0.05)  // 1000 packets, 5% loss

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.periodicNakOriginal(now())
    }
}

func BenchmarkPeriodicNak_NAKBtree(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)
    setupRealisticBuffer(recv, 1000, 0.05)  // 1000 packets, 5% loss

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.periodicNakBtree(now())
    }
}
```

**Acceptance criteria**:

| Metric | Original | NAKBtree | Pass Criteria |
|--------|----------|----------|---------------|
| `Push` latency | Baseline | Measured | < 20% overhead |
| `periodicNAK` latency | Baseline | Measured | < 50% overhead (acceptable for better accuracy) |
| Memory allocations | Baseline | Measured | < 2x allocations |

### 8.4 Memory Profiling

#### 8.4.1 Allocation Benchmarks

```go
func BenchmarkPush_Allocations(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)
    pkt := createBenchPacket()

    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        pkt.Header().PacketSequenceNumber = circular.New(uint32(i), packet.MAX_SEQUENCENUMBER)
        recv.Push(pkt)
    }
}

func BenchmarkConsolidate_Allocations(b *testing.B) {
    recv := createBenchReceiver(useNakBtree: true)
    for i := 0; i < 100; i++ {
        recv.nakBtree.Insert(uint32(i * 3))
    }

    b.ReportAllocs()
    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        recv.consolidateNakBtree()
    }
}
```

**Target**: Verify sync.Pool reduces allocations after warmup.

#### 8.4.2 Long-Running Memory Check

```go
func TestMemoryStability(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping long memory test")
    }

    recv := createTestReceiver(useNakBtree: true)

    var memBefore, memAfter runtime.MemStats
    runtime.GC()
    runtime.ReadMemStats(&memBefore)

    // Run for 60 seconds
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()

    seq := uint32(0)
    ticker := time.NewTicker(100 * time.Microsecond)
    nakTicker := time.NewTicker(20 * time.Millisecond)

    for {
        select {
        case <-ctx.Done():
            goto done
        case <-ticker.C:
            recv.Push(createPacket(seq))
            seq++
        case <-nakTicker.C:
            recv.periodicNakBtree(now())
        }
    }
done:

    runtime.GC()
    runtime.ReadMemStats(&memAfter)

    heapGrowth := memAfter.HeapAlloc - memBefore.HeapAlloc
    if heapGrowth > 10*1024*1024 {  // 10 MB
        t.Errorf("Excessive heap growth: %d bytes", heapGrowth)
    }
}
```

### 8.5 Benchmark Execution

#### 8.5.1 Running Benchmarks

```bash
# All benchmarks
go test -bench=. -benchmem ./congestion/live/

# Specific benchmark
go test -bench=BenchmarkConsolidate -benchmem ./congestion/live/

# Compare old vs new
go test -bench=BenchmarkPeriodicNak -benchmem ./congestion/live/

# CPU profile
go test -bench=BenchmarkPush -cpuprofile=cpu.prof ./congestion/live/
go tool pprof cpu.prof

# Memory profile
go test -bench=BenchmarkConsolidate -memprofile=mem.prof ./congestion/live/
go tool pprof mem.prof
```

#### 8.5.2 Benchmark Comparison Tool

```bash
# Install benchstat
go install golang.org/x/perf/cmd/benchstat@latest

# Run baseline (without NAKBtree)
go test -bench=. -count=10 ./congestion/live/ > old.txt

# Run with NAKBtree
go test -bench=. -count=10 ./congestion/live/ > new.txt

# Compare
benchstat old.txt new.txt
```

### 8.6 Performance Regression Gates

| Benchmark | Baseline | Max Acceptable | Action If Exceeded |
|-----------|----------|----------------|-------------------|
| `Push_WithNakBtree` | *Establish after impl* | +20% vs Original | Optimize delete path |
| `Consolidate_1000entries` | *Establish after impl* | < 500µs | Increase timeout budget |
| `PeriodicNak_NAKBtree` | *Establish after impl* | +50% vs Original | Acceptable if accuracy improved |
| `Push_Allocations` | *Establish after impl* | 0 allocs/op | Check pool usage |

**Process**: Run benchmarks before and after NAK btree implementation to establish baselines.

**CI Integration**: Add benchmark comparison to PR checks; flag regressions > 20%.

---

## 9. Integration: Parallel Isolation Tests

The parallel isolation tests are critical for validating that NAK btree solves the io_uring out-of-order problem without introducing new issues.

**Reference**: See `parallel_isolation_test_plan.md` for full test framework design.

### 9.1 Background: Why Isolation Tests Matter

The root cause investigation (`parallel_defect1_highperf_excessive_gaps.md`) identified:
- **Test 5 (Server-IoUringRecv)** showed excessive false gap detection on clean network
- io_uring receive delivers packets out-of-order
- Original NAK logic sends immediate NAKs on gaps, causing false positives

**NAK btree solution**: Suppress immediate NAKs, use periodic NAK with TSBPD-based scan window.

### 9.2 Updated Test Matrix for NAK btree

Extend the existing isolation test matrix with NAK btree configurations:

| Test # | Name | Config | Expected Result |
|--------|------|--------|-----------------|
| 5 | Server-IoUringRecv (Original) | io_uring recv, no NAK btree | ❌ False gaps (baseline problem) |
| **5a** | Server-IoUringRecv-NAKBtree | io_uring recv, **with NAK btree** | ✅ 0 false gaps |
| 7 | Server-IoUringRecv-FullHighPerf | io_uring recv + btree + NAK btree | ✅ 0 false gaps |

### 9.3 New Test Configurations

Add to `test_configs.go`:

```go
// Test 5a: Server-IoUringRecv with NAK btree fix
{
    Name:        "Isolation-Server-IoUringRecv-NAKBtree",
    Description: "Test NAK btree fix for io_uring recv out-of-order",
    TestDuration: 30 * time.Second,
    StatsPeriod:  10 * time.Second,

    ControlCG: BaseControlConfig(),
    ControlServer: BaseControlConfig(),

    TestCG: BaseControlConfig(),
    TestServer: BaseControlConfig().
        WithIoUringRecv().
        WithNakBtree(),  // NEW: Enable NAK btree

    NetworkPath: "clean",  // 0% loss, 0ms latency
},

// Test 7: Full HighPerf with NAK btree
{
    Name:        "Isolation-Server-FullHighPerf-NAKBtree",
    Description: "Full HighPerf config (io_uring + btree + NAK btree)",
    TestDuration: 30 * time.Second,
    StatsPeriod:  10 * time.Second,

    ControlCG: BaseControlConfig(),
    ControlServer: BaseControlConfig(),

    TestCG: BaseControlConfig().
        WithIoUringSend().
        WithIoUringRecv().
        WithBtree(),
    TestServer: BaseControlConfig().
        WithIoUringSend().
        WithIoUringRecv().
        WithBtree().
        WithNakBtree(),  // NEW: Enable NAK btree

    NetworkPath: "clean",
},
```

### 9.4 Configuration Helper for NAK btree

Add to `config.go`:

```go
// WithNakBtree enables NAK btree for io_uring receive path
func (c SRTConfig) WithNakBtree() SRTConfig {
    c.UseNakBtree = true
    c.SuppressImmediateNak = true
    c.FastNakEnabled = true
    c.FastNakRecentEnabled = true
    return c
}

// ToCliFlags - add NAK btree flags
func (c SRTConfig) ToCliFlags() []string {
    var flags []string
    // ... existing flags ...

    if c.UseNakBtree {
        flags = append(flags, "-usenakbtree")
    }
    if c.FastNakEnabled {
        flags = append(flags, "-fastnakEnabled")
    }
    // Note: SuppressImmediateNak is auto-set when UseNakBtree=true

    return flags
}
```

### 9.5 Success Criteria

#### 9.5.1 Test 5a: Server-IoUringRecv-NAKBtree

| Metric | Control | Test (NAKBtree) | Pass Criteria |
|--------|---------|-----------------|---------------|
| `packets_lost_total` | ~0 | **0** | Test = 0 (no false gaps) |
| `NakBtreeInserts` | N/A | > 0 | NAK btree is active |
| `NakBtreeDeletes` | N/A | ≈ Inserts | Reordering recovered |
| `NakBtreeExpired` | N/A | 0 | No true loss on clean network |
| `NakPeriodicBtreeRuns` | N/A | ~1500 (30s × 50/s) | Timer running |

#### 9.5.2 Test 7: Full HighPerf with NAKBtree

| Metric | Control | Test (FullHighPerf) | Pass Criteria |
|--------|---------|---------------------|---------------|
| `packets_lost_total` | ~0 | **0** | Test = 0 |
| `packets_sent_total` | X | ≈ X | Similar throughput |
| `retransmissions_total` | ~0 | ~0 | No unnecessary retrans |

### 9.6 Running Isolation Tests

```bash
# Run specific NAK btree isolation test
make test-isolation CONFIG=Isolation-Server-IoUringRecv-NAKBtree

# Run all isolation tests
make test-isolation-all

# View results
cat /tmp/isolation_test_results/*.log
```

### 9.7 Expected Outcome

**Before NAK btree** (Test 5):
```
Control: packets_lost_total = 0
Test:    packets_lost_total = 2476  ← FALSE GAPS
```

**After NAK btree** (Test 5a):
```
Control: packets_lost_total = 0
Test:    packets_lost_total = 0     ← FIXED!
         NakBtreeInserts = 5000     (reordering detected)
         NakBtreeDeletes = 5000     (reordering recovered)
         NakBtreeExpired = 0        (no true loss)
```

### 9.8 Integration with analysis.go

Update `analysis.go` to validate NAK btree metrics:

```go
func analyzeIsolationTest_NAKBtree(metrics TestMetrics) AnalysisResult {
    result := AnalysisResult{Passed: false}

    // Must have 0 false gaps on clean network
    if metrics.Test.PacketsLost > 0 {
        result.Violations = append(result.Violations,
            fmt.Sprintf("False gaps detected: %d (expected 0)", metrics.Test.PacketsLost))
        return result
    }

    // NAK btree must be active
    if metrics.Test.NakBtreeInserts == 0 {
        result.Warnings = append(result.Warnings,
            "NakBtreeInserts = 0 (is NAK btree enabled?)")
    }

    // Inserts ≈ Deletes (reordering recovery)
    if metrics.Test.NakBtreeInserts > 0 {
        ratio := float64(metrics.Test.NakBtreeDeletes) / float64(metrics.Test.NakBtreeInserts)
        if ratio < 0.95 {
            result.Violations = append(result.Violations,
                fmt.Sprintf("Low recovery ratio: %.2f (expected > 0.95)", ratio))
            return result
        }
    }

    // No expired entries on clean network
    if metrics.Test.NakBtreeExpired > 0 {
        result.Violations = append(result.Violations,
            fmt.Sprintf("Expired entries on clean network: %d (expected 0)",
                metrics.Test.NakBtreeExpired))
        return result
    }

    result.Passed = true
    return result
}
```

---

## 10. Integration: Parallel Comparison Tests

The parallel comparison tests run two complete SRT pipelines side-by-side under identical network conditions (including Starlink impairment patterns). This enables direct comparison of Baseline vs HighPerf configurations.

**Reference**: See `parallel_comparison_test_design.md` for full test framework design.

### 10.1 NAK btree in Parallel Comparisons

With NAK btree, the HighPerf pipeline should now perform **as well as or better than** Baseline in all metrics, solving the original excessive gap problem.

#### 10.1.1 HighPerf Configuration with NAK btree

Update `test_configs.go` HighPerf configuration:

```go
HighPerfSRTConfig := SRTConfig{
    ConnectionTimeout:      3000 * time.Millisecond,
    PeerIdleTimeout:        30000 * time.Millisecond,
    Latency:                3000 * time.Millisecond,
    RecvLatency:            3000 * time.Millisecond,
    PeerLatency:            3000 * time.Millisecond,
    IoUringEnabled:         true,    // io_uring for SRT send
    IoUringRecvEnabled:     true,    // io_uring for SRT recv
    PacketReorderAlgorithm: "btree", // B-tree packet store
    BTreeDegree:            32,
    TLPktDrop:              true,

    // NEW: NAK btree (required for io_uring)
    UseNakBtree:            true,
    SuppressImmediateNak:   true,   // Auto-set by UseNakBtree
    FastNakEnabled:         true,
    FastNakRecentEnabled:   true,
}
```

### 10.2 Updated Metrics Comparison

#### 10.2.1 SRT-Level Metrics

| Metric | Baseline | HighPerf (with NAK btree) | Success Criteria |
|--------|----------|---------------------------|------------------|
| `packets_lost_total` | X | ≈ X | HighPerf ≤ Baseline |
| `recovery_rate` | 100% | 100% | Both = 100% |
| `retransmissions_total` | X | ≤ X | HighPerf ≤ Baseline |
| `drops_too_late` | X | ≤ X | HighPerf ≤ Baseline (key goal) |
| `tsbpd_skips` | 0 | 0 | Both = 0 |

#### 10.2.2 New NAK btree Metrics (HighPerf Only)

| Metric | Expected Value | What It Tells Us |
|--------|----------------|------------------|
| `NakBtreeInserts` | > 0 | NAK btree is active |
| `NakBtreeDeletes` | High | Reordering recovery working |
| `NakBtreeExpired` | ≈ packet loss | True losses → NAKs sent |
| `NakPeriodicBtreeRuns` | ~45K (90s × 50/s) | Timer running |
| `FastNakTriggers` | > 0 | FastNAK responding to Starlink outages |
| `NakConsolidationMerged` | > 0 | Range consolidation reducing NAK overhead |

### 10.3 Expected Comparison Output

**Before NAK btree** (original problem):
```
=== Parallel Comparison: Starlink-20Mbps ===

                          Baseline      HighPerf      Diff
  Packets Sent:           114,000       114,000       =
  Packets Received:       113,850       110,200       -3.2% ❌ WORSE
  Gaps Detected:          9,425         12,901        +37% ❌ FALSE GAPS
  Retransmissions:        21,500        34,200        +59% ❌ EXCESSIVE
  Recovery Rate:          100.0%        97.2%         -2.8% ❌ WORSE
  Drops (too_late):       12            156           +1200% ❌ WORSE

=== Summary ===
  ❌ HighPerf WORSE than Baseline - excessive gap detection
```

**After NAK btree** (expected):
```
=== Parallel Comparison: Starlink-20Mbps ===

                          Baseline      HighPerf      Diff
  Packets Sent:           114,000       114,000       =
  Packets Received:       113,850       113,920       +0.06% ✓
  Gaps Detected:          9,425         9,425         = ✓
  Retransmissions:        21,500        21,200        -1.4% ✓
  Recovery Rate:          100.0%        100.0%        = ✓
  Drops (too_late):       12            3             -75% ✓

=== NAK btree Metrics (HighPerf Only) ===
  NakBtreeInserts:        12,350        (reordering detected)
  NakBtreeDeletes:        11,900        (reordering recovered)
  NakBtreeExpired:        450           (true losses)
  FastNakTriggers:        6             (Starlink outages)
  NakConsolidationMerged: 3,200         (ranges created)

=== Summary ===
  ✓ HighPerf matches Baseline in recovery rate (100%)
  ✓ HighPerf shows 75% fewer late drops
  ✓ NAK btree successfully handling io_uring reordering
```

### 10.4 Test Configurations

Add parallel test configurations that include NAK btree:

```go
var ParallelTestConfigs = []ParallelTestConfig{
    // Existing configs updated with NAK btree in HighPerf
    {
        Name:        "Parallel-Starlink-5Mbps",
        Description: "Starlink pattern at 5 Mbps, Baseline vs HighPerf+NAKBtree",
        Duration:    90 * time.Second,
        Bitrate:     5_000_000,
        Pattern:     "starlink",

        Baseline: BaselineSRTConfig,
        HighPerf: HighPerfSRTConfig,  // Now includes UseNakBtree: true
    },
    {
        Name:        "Parallel-Starlink-20Mbps",
        Description: "Starlink pattern at 20 Mbps, Baseline vs HighPerf+NAKBtree",
        Duration:    90 * time.Second,
        Bitrate:     20_000_000,
        Pattern:     "starlink",

        Baseline: BaselineSRTConfig,
        HighPerf: HighPerfSRTConfig,
    },
    // New: Clean network to verify no false gaps
    {
        Name:        "Parallel-Clean-50Mbps",
        Description: "Clean network at 50 Mbps, verify no false gaps",
        Duration:    60 * time.Second,
        Bitrate:     50_000_000,
        Pattern:     "clean",  // 0% loss, 0ms latency

        Baseline: BaselineSRTConfig,
        HighPerf: HighPerfSRTConfig,
    },
}
```

### 10.5 Analysis.go Updates

Update `parallel_analysis.go` to validate NAK btree metrics:

```go
func analyzeParallelTest_NAKBtree(baseline, highperf PipelineMetrics) ComparisonResult {
    result := ComparisonResult{Passed: false}

    // Primary: HighPerf gaps ≤ Baseline gaps
    if highperf.GapsDetected > baseline.GapsDetected * 1.05 {
        result.Violations = append(result.Violations,
            fmt.Sprintf("HighPerf gaps (%d) > Baseline gaps (%d) by %.1f%%",
                highperf.GapsDetected, baseline.GapsDetected,
                (float64(highperf.GapsDetected)/float64(baseline.GapsDetected)-1)*100))
        return result
    }

    // Primary: Recovery rate
    if highperf.RecoveryRate < 99.0 {
        result.Violations = append(result.Violations,
            fmt.Sprintf("HighPerf recovery rate %.1f%% < 99%%", highperf.RecoveryRate))
        return result
    }

    // NAK btree must be active
    if highperf.NakBtreeInserts == 0 {
        result.Warnings = append(result.Warnings,
            "NakBtreeInserts = 0 - is NAK btree enabled?")
    }

    // FastNAK should trigger during Starlink outages
    if highperf.Pattern == "starlink" && highperf.FastNakTriggers == 0 {
        result.Warnings = append(result.Warnings,
            "FastNakTriggers = 0 during Starlink test - expected > 0")
    }

    // Btree size ratio check
    if highperf.PktStoreSize > 0 && highperf.NakBtreeSize > 0 {
        ratio := float64(highperf.NakBtreeSize) / float64(highperf.PktStoreSize)
        // Ratio should roughly match packet loss rate
        expectedRatio := baseline.LossRate / 100.0
        if ratio > expectedRatio * 2 {
            result.Warnings = append(result.Warnings,
                fmt.Sprintf("NAK/Pkt btree ratio %.2f > expected %.2f (2x loss rate)",
                    ratio, expectedRatio))
        }
    }

    result.Passed = true
    return result
}
```

### 10.6 Running Parallel Comparison Tests

```bash
# Run Starlink comparison with NAK btree
sudo make test-parallel CONFIG=Parallel-Starlink-20Mbps

# Run clean network comparison (verify no false gaps)
sudo make test-parallel CONFIG=Parallel-Clean-50Mbps

# Run all parallel tests
sudo make test-parallel-all

# Run with profiling to compare performance
sudo make test-parallel-profile CONFIG=Parallel-Starlink-20Mbps PROFILES=cpu,heap
```

### 10.7 Key Validation Points

| Validation | What We're Checking | Pass Criteria |
|------------|---------------------|---------------|
| **No false gaps** | io_uring reordering handled | HighPerf gaps ≤ Baseline |
| **Recovery maintained** | NAK btree sends correct NAKs | Recovery rate ≥ 99% |
| **FastNAK works** | Starlink outages trigger FastNAK | FastNakTriggers > 0 |
| **Consolidation works** | Ranges created to reduce overhead | NakConsolidationMerged > 0 |
| **Memory bounded** | Btrees don't grow unbounded | Size < limits |
| **Performance** | HighPerf still performs well | CPU/Memory ≤ Baseline |

### 10.8 Profiling with NAK btree

The profiling mode helps identify any performance regressions from NAK btree:

```bash
# CPU profile - look for NAK btree hot spots
go tool pprof -top -diff_base=profiles/server-baseline.pprof \
    profiles/server-highperf.pprof | head -20

# Expected: NAK btree functions should NOT dominate
# - consolidateNakBtree() < 1% CPU
# - nakBtree.Insert/Delete < 0.5% CPU
```

Functions to watch in profiles:

| Function | Concern If | Action |
|----------|------------|--------|
| `consolidateNakBtree` | > 2% CPU | Optimize iteration |
| `nakBtree.Insert` | > 1% CPU | Check btree degree |
| `periodicNakBtree` | > 3% CPU | Check scan window size |
| `FastNakCheck` | > 0.5% CPU | Optimize time checks |

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

