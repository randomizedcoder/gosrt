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

*HOLD - To be completed in future iteration*

### 3.1 Functional Requirements

- Suppress immediate NAK for io_uring receive path
- Efficient gap detection using NAK btree
- TSBPD-based scan window
- Priority ordering (oldest first)
- Multiple NAK packet support

### 3.2 Non-Functional Requirements

- Go idiomatic code
- Specific file and function references
- Comprehensive testing with corner cases
- Performance benchmarking for critical paths
- Feature flag for enable/disable
- Consider Go generics where appropriate

---

## 4. New Design: NAK btree v2

*HOLD - To be completed in future iteration*

### 4.1 Architecture Overview

### 4.2 NAK btree Structure

### 4.3 TSBPD-Based Scan Window

### 4.4 Consolidation Algorithm

### 4.5 FastNAK Optimization

### 4.6 Lock Design

### 4.7 Sequence Number Wraparound

### 4.8 Implementation Files

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

