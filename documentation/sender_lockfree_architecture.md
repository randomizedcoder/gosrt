# Sender Lock-Free Architecture Design

## Table of Contents

- [Related Documents](#related-documents)
- [1. Executive Summary](#1-executive-summary)
  - [1.1 Problem Statement](#11-problem-statement)
  - [1.2 Goals](#12-goals)
- [2. Current Architecture Overview](#2-current-architecture-overview)
  - [2.1 Sender Responsibilities](#21-sender-responsibilities)
  - [2.2 Current Data Flow Diagram](#22-current-data-flow-diagram)
- [3. Sequence Number Assignment](#3-sequence-number-assignment)
  - [3.1 Current Implementation](#31-current-implementation)
  - [3.2 Thread Safety Analysis](#32-thread-safety-analysis)
  - [3.3 31-Bit Sequence Numbers (SRT Protocol Requirement)](#33-31-bit-sequence-numbers-srt-protocol-requirement)
    - [3.3.1 SRT Protocol Specification](#331-srt-protocol-specification)
    - [3.3.2 Wraparound Behavior](#332-wraparound-behavior)
    - [3.3.3 Circular Number Implementation](#333-circular-number-implementation)
    - [3.3.4 Atomic 31-Bit Sequence Number Design](#334-atomic-31-bit-sequence-number-design)
    - [3.3.5 Known Bugs (Historical)](#335-known-bugs-historical)
  - [3.4 Current Safety Guarantee](#34-current-safety-guarantee)
- [4. Control Packet Processing](#4-control-packet-processing)
  - [4.1 ACK Processing Flow](#41-ack-processing-flow)
  - [4.2 NAK Processing Flow (Retransmission)](#42-nak-processing-flow-retransmission)
  - [4.3 KEEPALIVE and Packet Routing Overview](#43-keepalive-and-packet-routing-overview)
    - [4.3.1 Packet Receive and Classification](#431-packet-receive-and-classification)
    - [4.3.2 Why Handlers Are Non-Blocking](#432-why-handlers-are-non-blocking)
    - [4.3.3 KEEPALIVE Specific Flow](#433-keepalive-specific-flow)
- [5. Tick Path Deep Dive](#5-tick-path-deep-dive)
  - [5.1 Tick Function Flow](#51-tick-function-flow)
  - [5.2 Tick Timing Diagram](#52-tick-timing-diagram)
- [6. EventLoop Path (Current Implementation)](#6-eventloop-path-current-implementation)
  - [6.1 EventLoop Flow](#61-eventloop-flow)
  - [6.2 EventLoop Timing (Continuous)](#62-eventloop-timing-continuous)
- [7. Proposed Lock-Free Architecture](#7-proposed-lock-free-architecture)
  - [7.1 Design Goals](#71-design-goals)
  - [7.2 Key Insight: TransmitCount-Based First Send](#72-key-insight-transmitcount-based-first-send-simpler-approach)
  - [7.3 Proposed Architecture Diagram](#73-proposed-architecture-diagram)
  - [7.4 Comparison: Current vs Proposed](#74-comparison-current-vs-proposed)
  - [7.5 Key Changes Required](#75-key-changes-required)
  - [7.6 Atomic 31-Bit Sequence Number Design](#76-atomic-31-bit-sequence-number-design)
  - [7.7 Buffer Pool Management (Simplified with TransmitCount)](#77-buffer-pool-management-simplified-with-transmitcount)
    - [7.7.1 Single Buffer Lifetime Flow](#771-single-buffer-lifetime-flow)
    - [7.7.2 Buffer Pool Summary (TransmitCount Approach)](#772-buffer-pool-summary-transmitcount-approach)
    - [7.7.3 io_uring Consideration](#773-io_uring-consideration)
  - [7.8 TransmitCount-Based Write() Implementation](#78-transmitcount-based-write-implementation)
  - [7.9 EventLoop First-Send and Retransmit Logic](#79-eventloop-first-send-and-retransmit-logic)
    - [7.9.1 Control Packet Priority Pattern](#791-control-packet-priority-pattern)
    - [7.9.2 Why This Pattern Matters](#792-why-this-pattern-matters)
    - [7.9.3 Unified TSBPD + TransmitCount Logic](#793-unified-tsbpd--transmitcount-logic)
    - [7.9.4 NAK Retransmission (Separate Path)](#794-nak-retransmission-separate-path)
- [8. Implementation Phases](#8-implementation-phases)
  - [Phase 1: Rename RetransmitCount to TransmitCount](#phase-1-rename-retransmitcount-to-transmitcount)
  - [Phase 2: Atomic 31-Bit Sequence Number](#phase-2-atomic-31-bit-sequence-number)
  - [Phase 3: Extend deliverReadyPacketsEventLoop() with TransmitCount](#phase-3-extend-deliverreadypacketseventloop-with-transmitcount)
  - [Phase 4: Update NAK Handler for TransmitCount](#phase-4-update-nak-handler-for-transmitcount)
  - [Phase 5: Eliminate writeQueueReader](#phase-5-eliminate-writequeuereader)
  - [Phase 6: Full Integration and Metrics](#phase-6-full-integration-and-metrics)
- [9. Risk Analysis](#9-risk-analysis)
- [10. Metrics for Monitoring](#10-metrics-for-monitoring)
- [11. Next Steps](#11-next-steps)

---

## Related Documents

- **Defect Document:** [completely_lockfree_receiver_debugging.md](completely_lockfree_receiver_debugging.md#18-defect-writequeuereader-not-started-when-iouringenabled)
- **Sender Implementation Plan:** [lockless_sender_implementation_plan.md](lockless_sender_implementation_plan.md)
- **Sender Design:** [lockless_sender_design.md](lockless_sender_design.md)
- **SRT Protocol Specification:** [draft-sharabayko-srt-01.txt](draft-sharabayko-srt-01.txt) Section 3.1
- **Sequence Number Wraparound Tests:** `circular/seq_math_31bit_wraparound_test.go`

---

## 1. Executive Summary

This document analyzes the current sender architecture, identifies performance bottlenecks (particularly the `writeQueue` Go channel), and proposes a completely lock-free design for io_uring mode.

### 1.1 Problem Statement

The current sender path uses a Go channel (`writeQueue`) between `Write()` and the sender:
- Adds ~100-500ns latency per packet
- Requires a dedicated goroutine (`writeQueueReader`)
- Creates contention under high throughput

### 1.2 Goals

1. Eliminate Go channels from the hot path
2. Eliminate locks from the hot path
3. Maintain thread safety for sequence number assignment
4. Support both new packet transmission and NAK-triggered retransmission

---

## 2. Current Architecture Overview

### 2.1 Sender Responsibilities

The sender must handle multiple packet flows:

| Flow | Source | Action | Priority |
|------|--------|--------|----------|
| **New Data** | Application `Write()` | Assign sequence number, buffer, deliver when TSBPD ready | Normal |
| **ACK** | Network (from receiver) | Remove ACK'd packets from buffer, update RTT | High |
| **NAK** | Network (from receiver) | Retransmit requested packets | High |
| **KEEPALIVE** | Network | Update peer alive timestamp | Low |
| **Drop Old** | Timer | Remove packets exceeding drop threshold | Background |

### 2.2 Current Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        APPLICATION WRITE PATH                                │
│                                                                             │
│  ┌──────────┐    ┌─────────────┐    ┌──────────────────┐    ┌────────────┐ │
│  │ App      │───►│ writeQueue  │───►│ writeQueueReader │───►│ snd.Push() │ │
│  │ Write()  │    │ (channel)   │    │ (goroutine)      │    │            │ │
│  └──────────┘    └─────────────┘    └──────────────────┘    └─────┬──────┘ │
│       │                                                           │        │
│       │ PktTsbpdTime = c.getTimestamp()                          │        │
│       │ (relative time since connection start)                    │        │
│                                                                   ▼        │
│                                                          ┌────────────────┐│
│                                                          │ Assign SeqNum  ││
│                                                          │ nextSeqNum++   ││
│                                                          └───────┬────────┘│
│                                                                  │         │
│                              ┌───────────────────────────────────┼─────────┤
│                              │                                   │         │
│                              ▼                                   ▼         │
│                    ┌─────────────────┐              ┌─────────────────────┐│
│                    │ useSendRing=true│              │ useSendRing=false   ││
│                    │ pushRing()      │              │ pushBtree/List()    ││
│                    │ (lock-free)     │              │ (with lock)         ││
│                    └────────┬────────┘              └──────────┬──────────┘│
│                             │                                  │           │
│                             ▼                                  │           │
│                    ┌─────────────────┐                         │           │
│                    │ packetRing      │                         │           │
│                    │ (ShardedRing)   │                         │           │
│                    └────────┬────────┘                         │           │
│                             │                                  │           │
└─────────────────────────────┼──────────────────────────────────┼───────────┘
                              │                                  │
                              ▼                                  ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        SENDER TICK / EVENTLOOP                              │
│                                                                             │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │                     Tick(now) / EventLoop                             │  │
│  │                                                                       │  │
│  │  1. drainRingToBtree()     ─────► Move packets from ring to btree    │  │
│  │  2. processControlRing()   ─────► Process ACK/NAK from control ring  │  │
│  │  3. tickDeliverPacketsBtree(now)  ─────► Deliver ready packets       │  │
│  │  4. tickDropOldPackets(now)       ─────► Drop expired packets        │  │
│  │                                                                       │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│                              │                                              │
│                              ▼                                              │
│                    ┌─────────────────┐                                      │
│                    │ packetBtree     │                                      │
│                    │ (sorted by seq) │                                      │
│                    └────────┬────────┘                                      │
│                             │                                               │
│                             │ if PktTsbpdTime <= now                        │
│                             ▼                                               │
│                    ┌─────────────────┐                                      │
│                    │ s.deliver(p)    │                                      │
│                    │ (callback)      │                                      │
│                    └────────┬────────┘                                      │
│                             │                                               │
└─────────────────────────────┼───────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        NETWORK SEND PATH                                     │
│                                                                             │
│                    ┌─────────────────┐                                      │
│                    │ c.pop(p)        │                                      │
│                    │ (connection)    │                                      │
│                    └────────┬────────┘                                      │
│                             │                                               │
│                             │ Add destination address, encrypt              │
│                             ▼                                               │
│                    ┌─────────────────┐       ┌─────────────────┐            │
│                    │ io_uring submit │  OR   │ conn.WriteTo()  │            │
│                    │ (if enabled)    │       │ (blocking)      │            │
│                    └─────────────────┘       └─────────────────┘            │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Sequence Number Assignment

### 3.1 Current Implementation

Sequence numbers are assigned in `Push()` before the packet enters the ring or btree:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    SEQUENCE NUMBER ASSIGNMENT                               │
│                                                                             │
│  Location: congestion/live/send/push.go                                     │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  func (s *sender) pushRing(p packet.Packet) {                         │  │
│  │      // 1. Read current sequence number (NOT ATOMIC)                  │  │
│  │      currentSeq := s.nextSequenceNumber                               │  │
│  │                                                                       │  │
│  │      // 2. Assign to packet header                                    │  │
│  │      p.Header().PacketSequenceNumber = currentSeq                     │  │
│  │      p.Header().PacketPositionFlag = packet.SinglePacket              │  │
│  │      p.Header().OrderFlag = false                                     │  │
│  │      p.Header().MessageNumber = 1                                     │  │
│  │                                                                       │  │
│  │      // 3. Set timestamp                                              │  │
│  │      p.Header().Timestamp = uint32(PktTsbpdTime & MAX_TIMESTAMP)      │  │
│  │                                                                       │  │
│  │      // 4. Push to ring (lock-free, may fail if full)                 │  │
│  │      if !s.packetRing.Push(p) {                                       │  │
│  │          m.SendRingDropped.Add(1)                                     │  │
│  │          p.Decommission()                                             │  │
│  │          return  // Don't increment sequence on failure               │  │
│  │      }                                                                │  │
│  │                                                                       │  │
│  │      // 5. Increment sequence ONLY after successful push              │  │
│  │      s.nextSequenceNumber = s.nextSequenceNumber.Inc()                │  │
│  │      m.SendRingPushed.Add(1)                                          │  │
│  │  }                                                                    │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│  CRITICAL: nextSequenceNumber is circular.Number (NOT atomic)               │
│            This code is NOT thread-safe for concurrent Push() calls         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Thread Safety Analysis

| Component | Type | Thread-Safe? | Notes |
|-----------|------|--------------|-------|
| `nextSequenceNumber` | `circular.Number` (uint32 wrapper) | ❌ NO | Read-modify-write is not atomic |
| `packetRing.Push()` | `ShardedRing` | ✅ YES | Lock-free CAS operations |
| `packetBtree.Insert()` | `btree.BTreeG` | ❌ NO | Requires external locking |
| `s.lock` | `sync.RWMutex` | ✅ YES | Used to protect btree in Tick mode |

### 3.3 31-Bit Sequence Numbers (SRT Protocol Requirement)

**CRITICAL:** SRT sequence numbers are **31 bits**, not 32 bits.

#### 3.3.1 SRT Protocol Specification

From `draft-sharabayko-srt-01.txt` Section 3.1 "Data Packets":

```
    0                   1                   2                   3
    0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
   +-+-+-+-+-+-+-+-+-+-+-+-+- SRT Header +-+-+-+-+-+-+-+-+-+-+-+-+-+
   |0|                    Packet Sequence Number                   |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

   Packet Sequence Number: 31 bits. The sequential number of the data packet.
```

The **first bit (bit 0) is always 0** for data packets. Control packets have bit 0 = 1.

#### 3.3.2 Wraparound Behavior

| Property | Value |
|----------|-------|
| Bit width | 31 bits |
| Max value | `packet.MAX_SEQUENCENUMBER` = `0x7FFFFFFF` (2,147,483,647) |
| Wraparound | After `0x7FFFFFFF` → `0x00000000` |
| Comparison | Requires special "circular" comparison |
| Existing constant | `packet/packet.go`: `MAX_SEQUENCENUMBER` |

#### 3.3.3 Circular Number Implementation

The `circular` package handles 31-bit wraparound correctly:

```go
// packet/packet.go - EXISTING constant
const MAX_SEQUENCENUMBER uint32 = 0b01111111_11111111_11111111_11111111  // 0x7FFFFFFF

// Comparison must handle wraparound:
// - If difference < 2^30, use normal comparison
// - If difference >= 2^30, numbers wrapped around

// Example: Is 0x7FFFFFF0 < 0x00000010?
// Normal: 0x7FFFFFF0 (2147483632) > 0x00000010 (16)  WRONG!
// Circular: 0x00000010 is 32 ahead of 0x7FFFFFF0     CORRECT!
```

Tests for this behavior are in `circular/seq_math_31bit_wraparound_test.go`.

#### 3.3.4 Atomic 31-Bit Sequence Number Design

For thread-safe atomic operations, we must:

1. **Use 32-bit atomic** - Go's `atomic.Uint32` is 32 bits
2. **Mask to 31 bits** - After increment, AND with `packet.MAX_SEQUENCENUMBER`
3. **Handle wraparound** - The mask naturally wraps `0x80000000` → `0x00000000`

```go
// EXISTING constant in packet/packet.go:
const MAX_SEQUENCENUMBER uint32 = 0b01111111_11111111_11111111_11111111  // 0x7FFFFFFF

// CORRECT: Atomic 31-bit sequence number
type sender struct {
    nextSeqAtomic atomic.Uint32  // Internal counter (32-bit)
}

func (s *sender) nextSequenceNumber31() uint32 {
    // Atomically increment and mask to 31 bits
    raw := s.nextSeqAtomic.Add(1) - 1      // Get previous value
    return raw & packet.MAX_SEQUENCENUMBER  // Mask to 31 bits (reuse existing constant)
}

// Initialize with ISN from handshake
func (s *sender) initSequenceNumber(isn uint32) {
    // ISN is already 31-bit, store directly
    s.nextSeqAtomic.Store(isn & packet.MAX_SEQUENCENUMBER)
}
```

#### 3.3.5 Known Bugs (Historical)

Previous bugs related to 31-bit wraparound:
- **Comparison bug**: Using `seq1 < seq2` instead of `circular.Lt(seq1, seq2)`
- **Increment bug**: Using `seq + 1` instead of `(seq + 1) & packet.MAX_SEQUENCENUMBER`
- **btree ordering**: Must use circular comparison for btree ordering

**Reference:** `circular/seq_math_31bit_wraparound_test.go` has comprehensive tests.

### 3.4 Current Safety Guarantee

The `writeQueueReader` goroutine ensures single-threaded access to `Push()`:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    SINGLE-WRITER GUARANTEE                                   │
│                                                                             │
│  App Goroutine 1 ─────┐                                                     │
│                       │    writeQueue     writeQueueReader                  │
│  App Goroutine 2 ─────┼───► (channel) ───► (single goroutine) ───► Push()  │
│                       │                                                     │
│  App Goroutine N ─────┘                                                     │
│                                                                             │
│  Multiple writers to channel ─► Single reader calls Push()                  │
│                                  (serialized access to nextSequenceNumber)  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 4. Control Packet Processing

### 4.1 ACK Processing Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        ACK RECEIVE PATH                                      │
│                                                                             │
│  ┌──────────────┐                                                           │
│  │ Network      │                                                           │
│  │ (Full ACK)   │                                                           │
│  └──────┬───────┘                                                           │
│         │                                                                   │
│         ▼                                                                   │
│  ┌──────────────────┐                                                       │
│  │ handlePacket()   │  connection_handlers.go                               │
│  │ dispatch by type │                                                       │
│  └────────┬─────────┘                                                       │
│           │                                                                 │
│           ▼                                                                 │
│  ┌──────────────────┐                                                       │
│  │ handleACK()      │  Extracts ACK sequence number                         │
│  └────────┬─────────┘                                                       │
│           │                                                                 │
│           │ if useSendControlRing:                                          │
│           │                                                                 │
│           ├───────────────────────────────┐                                 │
│           ▼                               ▼                                 │
│  ┌──────────────────┐           ┌──────────────────┐                        │
│  │ controlRing.Push │           │ snd.ACK() direct │  (fallback)            │
│  │ (lock-free)      │           │ (with lock)      │                        │
│  └────────┬─────────┘           └──────────────────┘                        │
│           │                                                                 │
│           │ Tick/EventLoop drains ring:                                     │
│           ▼                                                                 │
│  ┌──────────────────┐                                                       │
│  │ processControl   │                                                       │
│  │ Ring()           │                                                       │
│  └────────┬─────────┘                                                       │
│           │                                                                 │
│           ▼                                                                 │
│  ┌──────────────────┐                                                       │
│  │ ackBtree()       │  connection_send.go                                   │
│  │                  │                                                       │
│  │ 1. Update last   │                                                       │
│  │    ACKed seq     │                                                       │
│  │                  │                                                       │
│  │ 2. Remove all    │                                                       │
│  │    packets with  │                                                       │
│  │    seq < ACKseq  │                                                       │
│  │    from btree    │                                                       │
│  │                  │                                                       │
│  │ 3. Decommission  │                                                       │
│  │    removed pkts  │                                                       │
│  └──────────────────┘                                                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 NAK Processing Flow (Retransmission)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        NAK RECEIVE PATH (RETRANSMISSION)                    │
│                                                                             │
│  ┌──────────────┐                                                           │
│  │ Network      │  NAK contains list of missing sequence numbers            │
│  │ (NAK packet) │  Format: [seq1, seq2] or [start, end] for ranges          │
│  └──────┬───────┘                                                           │
│         │                                                                   │
│         ▼                                                                   │
│  ┌──────────────────┐                                                       │
│  │ handleNAK()      │  connection_handlers.go                               │
│  │ Parse NAK list   │                                                       │
│  └────────┬─────────┘                                                       │
│           │                                                                 │
│           │ if useSendControlRing:                                          │
│           │                                                                 │
│           ├───────────────────────────────┐                                 │
│           ▼                               ▼                                 │
│  ┌──────────────────┐           ┌──────────────────┐                        │
│  │ controlRing.Push │           │ snd.NAK() direct │  (fallback)            │
│  │ NAK with seqs    │           │ (with lock)      │                        │
│  └────────┬─────────┘           └──────────────────┘                        │
│           │                                                                 │
│           │ Tick/EventLoop drains ring:                                     │
│           ▼                                                                 │
│  ┌──────────────────┐                                                       │
│  │ processControl   │                                                       │
│  │ Ring()           │                                                       │
│  └────────┬─────────┘                                                       │
│           │                                                                 │
│           ▼                                                                 │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ nakBtree(sequenceNumbers []circular.Number)                           │  │
│  │                                                                       │  │
│  │   for each seq in sequenceNumbers:                                    │  │
│  │       │                                                               │  │
│  │       ▼                                                               │  │
│  │   ┌─────────────────┐                                                 │  │
│  │   │ packetBtree.Get │  Look up packet by sequence number              │  │
│  │   │ (seq)           │                                                 │  │
│  │   └────────┬────────┘                                                 │  │
│  │            │                                                          │  │
│  │            │ if found:                                                │  │
│  │            ▼                                                          │  │
│  │   ┌─────────────────┐                                                 │  │
│  │   │ Clone packet    │  Can't modify original (might be in flight)     │  │
│  │   │ for retransmit  │                                                 │  │
│  │   └────────┬────────┘                                                 │  │
│  │            │                                                          │  │
│  │            ▼                                                          │  │
│  │   ┌─────────────────┐                                                 │  │
│  │   │ s.deliver(pkt)  │  Send retransmission immediately                │  │
│  │   │ (callback)      │  (doesn't wait for TSBPD)                       │  │
│  │   └────────┬────────┘                                                 │  │
│  │            │                                                          │  │
│  │            │ Increment retransmit metrics                             │  │
│  │            ▼                                                          │  │
│  │   ┌─────────────────┐                                                 │  │
│  │   │ m.Retrans.Add() │                                                 │  │
│  │   └─────────────────┘                                                 │  │
│  │                                                                       │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
│  IMPORTANT: Retransmissions are sent IMMEDIATELY, not buffered              │
│             They bypass TSBPD timing (already past due)                     │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4.3 KEEPALIVE and Packet Routing Overview

This section details how packets arrive from the network and get routed to handlers.

#### 4.3.1 Packet Receive and Classification

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    PACKET RECEIVE AND CLASSIFICATION                         │
│                                                                             │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                         NETWORK LAYER                                   │ │
│  │                                                                         │ │
│  │  io_uring completion / recvfrom() returns packet bytes                  │ │
│  │                                                                         │ │
│  └─────────────────────────────┬───────────────────────────────────────────┘ │
│                                │                                            │
│                                ▼                                            │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  handlePacketDirect(p)   connection_handlers.go                         │ │
│  │                                                                         │ │
│  │  1. Parse packet header (first 16 bytes of SRT header)                  │ │
│  │  2. Check bit 0 of first byte:                                          │ │
│  │     - Bit 0 = 0 → DATA packet (sequence number in bits 1-31)            │ │
│  │     - Bit 0 = 1 → CONTROL packet (type in bits 1-15)                    │ │
│  │                                                                         │ │
│  └─────────────────────────────┬───────────────────────────────────────────┘ │
│                                │                                            │
│           ┌────────────────────┴────────────────────┐                       │
│           │                                         │                       │
│           ▼                                         ▼                       │
│  ┌─────────────────────┐                   ┌─────────────────────┐          │
│  │ DATA PACKET         │                   │ CONTROL PACKET      │          │
│  │ p.IsControlPacket   │                   │ p.IsControlPacket   │          │
│  │ = false             │                   │ = true              │          │
│  │                     │                   │                     │          │
│  │ → recv.Push(p)      │                   │ → dispatch by       │          │
│  │   (to receiver)     │                   │   ControlType       │          │
│  └─────────────────────┘                   └──────────┬──────────┘          │
│                                                       │                     │
└───────────────────────────────────────────────────────┼─────────────────────┘
                                                        │
                                                        ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    CONTROL PACKET DISPATCH                                  │
│                                                                             │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  controlPacketHandlers[p.ControlType](p)                               │ │
│  │                                                                        │ │
│  │  Map of handler functions (connection_handlers.go):                    │ │
│  │                                                                        │ │
│  │  CTRLTYPE_ACK      → handleACK()       → sender (RTT, remove pkts)     │ │
│  │  CTRLTYPE_NAK      → handleNAK()       → sender (retransmit)           │ │
│  │  CTRLTYPE_ACKACK   → dispatchACKACK()  → receiver (RTT calculation)    │ │
│  │  CTRLTYPE_KEEPALIVE→ dispatchKEEPALIVE() → connection (reset timer)    │ │
│  │  CTRLTYPE_SHUTDOWN → handleShutdown()  → connection (close)            │ │
│  │  CTRLTYPE_DROPREQ  → handleDropReq()   → receiver (drop range)         │ │
│  │                                                                        │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 4.3.2 Why Handlers Are Non-Blocking

**Question:** What prevents `handleKEEPALIVE()` from blocking?

**Answer:** All control packet handlers are designed to be non-blocking:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    NON-BLOCKING HANDLER DESIGN                              │
│                                                                             │
│  The io_uring/recvfrom completion handler runs in a single goroutine:       │
│                                                                             │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  for completion := range io_uring_cq {                                 │ │
│  │      p := parsePacket(completion.buffer)                               │ │
│  │      conn.handlePacketDirect(p)  // MUST NOT BLOCK                     │ │
│  │  }                                                                     │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  If any handler blocks, ALL packet processing stops!                        │
│                                                                             │
│  ═════════════════════════════════════════════════════════════════════════  │
│                                                                             │
│  Handler Analysis:                                                          │
│                                                                             │
│  ┌─────────────────┬────────────────────────────────────────────────────┐   │
│  │ Handler         │ Why Non-Blocking                                   │   │
│  ├─────────────────┼────────────────────────────────────────────────────┤   │
│  │ handleKEEPALIVE │ Just updates atomic timestamp:                     │   │
│  │                 │   c.peerLastActive.Store(time.Now().UnixNano())    │   │
│  │                 │ No locks, no channels, no I/O                      │   │
│  ├─────────────────┼────────────────────────────────────────────────────┤   │
│  │ handleACK       │ With control ring: push to lock-free ring (CAS)    │   │
│  │                 │ Without: acquire lock, update btree                │   │
│  │                 │ Lock contention is brief (btree ops are O(log n))  │   │
│  ├─────────────────┼────────────────────────────────────────────────────┤   │
│  │ handleNAK       │ With control ring: push to lock-free ring          │   │
│  │                 │ Without: acquire lock, retransmit immediately      │   │
│  │                 │ Retransmit uses io_uring submit (non-blocking)     │   │
│  ├─────────────────┼────────────────────────────────────────────────────┤   │
│  │ dispatchACKACK  │ With control ring: push to lock-free ring          │   │
│  │                 │ Without: acquire lock, update RTT atomics          │   │
│  └─────────────────┴────────────────────────────────────────────────────┘   │
│                                                                             │
│  ═════════════════════════════════════════════════════════════════════════  │
│                                                                             │
│  Control Ring Pattern (lock-free hot path):                                 │
│                                                                             │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  func dispatchKEEPALIVE(p packet.Packet) {                             │ │
│  │      if c.recvControlRing != nil {                                     │ │
│  │          // Lock-free push (CAS, ~10-50ns)                             │ │
│  │          if c.recvControlRing.PushKEEPALIVE() {                        │ │
│  │              return  // Success, EventLoop will process                │ │
│  │          }                                                             │ │
│  │          // Ring full - fall through to locked path                    │ │
│  │      }                                                                 │ │
│  │      // Locked fallback                                                │ │
│  │      c.handleKeepAliveLocked(p)                                        │ │
│  │  }                                                                     │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 4.3.3 KEEPALIVE Specific Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        KEEPALIVE PROCESSING                                 │
│                                                                             │
│  Purpose: Prevent peer idle timeout when no data is flowing                 │
│                                                                             │
│  ┌──────────────┐                                                           │
│  │ Network      │  KEEPALIVE packet (control type = 0x0001)                 │
│  │ (KEEPALIVE)  │  Payload: empty                                           │
│  └──────┬───────┘                                                           │
│         │                                                                   │
│         ▼                                                                   │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  dispatchKEEPALIVE(p)                                                  │ │
│  │                                                                        │ │
│  │  if recvControlRing != nil:                                            │ │
│  │      → Push to ring (lock-free, ~10ns)                                 │ │
│  │      → EventLoop processes: handleKeepAliveEventLoop()                 │ │
│  │  else:                                                                 │ │
│  │      → handleKeepAliveLocked() (direct call)                           │ │
│  │                                                                        │ │
│  └─────────────────────────────┬──────────────────────────────────────────┘ │
│                                │                                            │
│                                ▼                                            │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  handleKeepAlive() (EventLoop) / handleKeepAliveLocked() (Tick)        │ │
│  │                                                                        │ │
│  │  Operations (ALL non-blocking):                                        │ │
│  │                                                                        │ │
│  │  1. c.peerLastActive.Store(time.Now().UnixNano())  // Atomic store     │ │
│  │     - Prevents watchPeerIdleTimeout() from closing connection          │ │
│  │                                                                        │ │
│  │  2. c.metrics.RecvKeepalive.Add(1)  // Atomic increment                │ │
│  │     - Track keepalive count for monitoring                             │ │
│  │                                                                        │ │
│  │  Total time: ~50-100ns (two atomic ops)                                │ │
│  │                                                                        │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Note: KEEPALIVE does NOT interact with sender congestion control           │
│        It only updates connection-level state                               │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 5. Tick Path Deep Dive

### 5.1 Tick Function Flow

```go
// congestion/live/send/tick.go

func (s *sender) Tick(now uint64) {
    s.EnterTick()       // Debug: track context
    defer s.ExitTick()

    s.metrics.SendTickRuns.Add(1)

    // With lock timing metrics:
    if s.lockTiming != nil {
        metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
            s.tickDeliverPackets(now)
        })
        metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
            s.tickDropOldPackets(now)
        })
        metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
            s.tickUpdateRateStats(now)
        })
        return
    }

    // Without metrics:
    s.lock.Lock()
    s.tickDeliverPackets(now)
    s.lock.Unlock()

    s.lock.Lock()
    s.tickDropOldPackets(now)
    s.lock.Unlock()

    s.lock.Lock()
    s.tickUpdateRateStats(now)
    s.lock.Unlock()
}
```

### 5.2 Tick Timing Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        TICK TIMING (10ms default)                           │
│                                                                             │
│  Time ─────────────────────────────────────────────────────────────────►    │
│                                                                             │
│  ├────────────┼────────────┼────────────┼────────────┼────────────┤         │
│  0ms         10ms        20ms         30ms         40ms        50ms         │
│  │            │            │            │            │            │         │
│  │            │            │            │            │            │         │
│  ▼            ▼            ▼            ▼            ▼            ▼         │
│  Tick()      Tick()      Tick()      Tick()      Tick()      Tick()         │
│  │            │            │            │            │            │         │
│  │            │            │            │            │            │         │
│  ├─ drain     ├─ drain     ├─ drain     ├─ drain     ├─ drain     │         │
│  │  ring      │  ring      │  ring      │  ring      │  ring      │         │
│  │            │            │            │            │            │         │
│  ├─ process   ├─ process   ├─ process   ├─ process   ├─ process   │         │
│  │  control   │  control   │  control   │  control   │  control   │         │
│  │            │            │            │            │            │         │
│  ├─ deliver   ├─ deliver   ├─ deliver   ├─ deliver   ├─ deliver   │         │
│  │  ready     │  ready     │  ready     │  ready     │  ready     │         │
│  │            │            │            │            │            │         │
│  ├─ drop      ├─ drop      ├─ drop      ├─ drop      ├─ drop      │         │
│  │  old       │  old       │  old       │  old       │  old       │         │
│  │            │            │            │            │            │         │
│                                                                             │
│  LATENCY: Up to 10ms between packet arrival and delivery                    │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 6. EventLoop Path (Current Implementation)

### 6.1 EventLoop Flow

```go
// congestion/live/send/eventloop.go

func (s *sender) EventLoop(ctx context.Context) {
    s.EnterEventLoop()
    defer s.ExitEventLoop()
    defer s.cleanupOnShutdown()

    dropTicker := time.NewTicker(100 * time.Millisecond)
    defer dropTicker.Stop()

    for {
        m.SendEventLoopIterations.Add(1)

        select {
        case <-ctx.Done():
            return

        case <-dropTicker.C:
            s.dropOldPacketsEventLoop(s.nowFn())

        default:
            nowUs := s.nowFn()

            // 1. Drain data ring to btree
            drained := s.drainRingToBtreeEventLoop()

            // 2. Deliver ready packets
            delivered, tsbpdSleep := s.deliverReadyPacketsEventLoop(nowUs)

            // 3. Process control packets
            controlProcessed := s.processControlPacketsDelta()

            // 4. Adaptive backoff if idle
            if drained == 0 && delivered == 0 && controlProcessed == 0 {
                s.idleBackoff(tsbpdSleep)
            }
        }
    }
}
```

### 6.2 EventLoop Timing (Continuous)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        EVENTLOOP TIMING (Continuous)                        │
│                                                                             │
│  Time ─────────────────────────────────────────────────────────────────►    │
│                                                                             │
│  ├──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┼──┤              │
│  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │                 │
│  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼  ▼                 │
│  Loop iterations (as fast as possible, with adaptive backoff)               │
│                                                                             │
│  Each iteration:                                                            │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │ 1. drainRingToBtreeEventLoop()    ~100ns if empty, ~1µs per packet  │    │
│  │ 2. deliverReadyPacketsEventLoop() ~100ns if none ready              │    │
│  │ 3. processControlPacketsDelta()   ~100ns if empty                   │    │
│  │ 4. idleBackoff() if no work       10µs - 1ms adaptive sleep         │    │
│  └─────────────────────────────────────────────────────────────────────┘    │
│                                                                             │
│  LATENCY: Sub-microsecond when active, up to 1ms during idle                │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 7. Proposed Lock-Free Architecture

### 7.1 Design Goals

1. **No Go channels** in the hot path
2. **No mutexes** in the hot path
3. **TransmitCount tracking** - identify never-sent packets (`TransmitCount == 0`)
4. **Lock-free rings** for packet handoff to EventLoop
5. **31-bit sequence numbers** with proper atomic wraparound
6. **Single buffer lifetime** - no extra copy for network send

### 7.2 Key Insight: TransmitCount-Based First Send (Simpler Approach)

The current architecture buffers packets and waits for EventLoop delivery:

```
CURRENT (slow):
Write() → Channel → writeQueueReader → Push() → Ring → EventLoop → Btree → TSBPD → Deliver
         ~200ns      ~500ns            ~100ns   ~50ns   ~100ns      ~1µs    ~100ns  ~50ns

Total latency: ~2-10µs + up to 10ms Tick interval
```

**Simpler Optimization:** Use `TransmitCount` to identify never-sent packets:

```
PROPOSED (simpler, no extra buffer copy):
Write() → Atomic SeqNum → Push to ring (TransmitCount=0)
          ~10ns           ~50ns
                            │
                            ▼
          EventLoop drains ring → Btree insert
                    ~100ns           ~1µs
                                       │
                                       ▼
          EventLoop btree scan: if TransmitCount == 0 → SEND → TransmitCount++
                                                         ~1µs

Total latency: ~2-5µs (EventLoop runs continuously, no 10ms Tick wait)
```

**Why this is simpler than "send immediately + copy":**
- **No extra buffer copy** - packet buffer flows through system normally
- **No special io_uring tracking** for the first send
- **Single buffer lifetime** - one buffer from Write() to ACK/expire
- **Reuses existing btree scan logic** - just add TransmitCount check
- **EventLoop is continuous** - delay is microseconds, not 10ms like Tick

**Key Change:** Rename `RetransmitCount` → `TransmitCount` in `PacketHeader`:

| Value | Meaning |
|-------|---------|
| `TransmitCount == 0` | Never sent (first transmission pending) |
| `TransmitCount == 1` | Sent once (original transmission complete) |
| `TransmitCount >= 2` | Retransmitted (NAK-triggered) |

**Wire Format Note:** `TransmitCount` is NOT transmitted on wire.
The `RetransmittedPacketFlag` bit in the SRT header is set when `TransmitCount >= 2`.

### 7.3 Proposed Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                PROPOSED: TRANSMITCOUNT-BASED ARCHITECTURE                   │
│                                                                             │
│  ════════════════════════════════════════════════════════════════════════   │
│                      APPLICATION WRITE PATH (HOT PATH)                      │
│  ════════════════════════════════════════════════════════════════════════   │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  Write(data []byte)                                                   │  │
│  │                                                                       │  │
│  │  ┌──────────────────────────────────────────────────────────────────┐ │  │
│  │  │  1. ALLOCATE PACKET (from global pool)                           │ │  │
│  │  │     p := packet.NewPacket(data)                                  │ │  │
│  │  │     p.Header().TransmitCount = 0  // Never sent yet              │ │  │
│  │  │                                                                  │ │  │
│  │  │  2. SET TSBPD TIME (relative to connection start)                │ │  │
│  │  │     p.Header().PktTsbpdTime = time.Since(c.start).Microseconds() │ │  │
│  │  │                                                                  │ │  │
│  │  │  3. ASSIGN 31-BIT SEQUENCE NUMBER (ATOMIC)                       │ │  │
│  │  │     ┌────────────────────────────────────────────────────────┐   │ │  │
│  │  │     │ raw := s.nextSeqAtomic.Add(1) - 1  // Atomic increment │   │ │  │
│  │  │     │ seq := (s.initialSeq + raw) & packet.MAX_SEQUENCENUMBER│   │ │  │
│  │  │     │ p.Header().PacketSequenceNumber =                      │   │ │  │
│  │  │     │     circular.New(seq, packet.MAX_SEQUENCENUMBER)       │   │ │  │
│  │  │     └────────────────────────────────────────────────────────┘   │ │  │
│  │  │                                                                  │ │  │
│  │  │  4. PUSH TO DATA RING (EventLoop will send)                      │ │  │
│  │  │     dataRing.Push(p)  // Lock-free CAS, TransmitCount still 0    │ │  │
│  │  │                                                                  │ │  │
│  │  └──────────────────────────────────────────────────────────────────┘ │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                  │                                          │
│                                  │ Lock-free push                           │
│                                  ▼                                          │
│                         ┌─────────────────┐                                 │
│                         │ dataRing        │  Packets with TransmitCount=0   │
│                         │ (ShardedRing)   │  waiting for first send         │
│                         │ MPSC            │                                 │
│                         └────────┬────────┘                                 │
│                                  │                                          │
│  ════════════════════════════════════════════════════════════════════════   │
│                      NETWORK RECEIVE PATH (CONTROL PACKETS)                 │
│  ════════════════════════════════════════════════════════════════════════   │
│                                  │                                          │
│  ┌───────────────────────────────┼──────────────────────────────────────┐   │
│  │                               │     io_uring / recvfrom handler      │   │
│  │                               │                                      │   │
│  │  Control packets arrive ──────┼─────────────────────────────┐        │   │
│  │  (ACK, NAK, KEEPALIVE)        │                             │        │   │
│  │                               │                             │        │   │
│  │  handlePacketDirect():        │                             ▼        │   │
│  │  - Parse, classify            │                    ┌─────────────────┐   │
│  │  - Push to control ring       │                    │ controlRing     │   │
│  │    (lock-free)                │                    │ (ShardedRing)   │   │
│  │                               │                    │ MPSC            │   │
│  │                               │                    └────────┬────────┘   │
│  └───────────────────────────────┼─────────────────────────────┼────────┘   │
│                                  │                             │            │
│                                  │                             │            │
│  ════════════════════════════════════════════════════════════════════════   │
│                      SENDER EVENTLOOP (Continuous Processing)               │
│  ════════════════════════════════════════════════════════════════════════   │
│                                  │                             │            │
│                                  ▼                             ▼            │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                      SENDER EVENTLOOP (Single Thread)                │   │
│  │                                                                      │   │
│  │  EventLoop sends packets with TransmitCount==0 (first transmission)  │   │
│  │  and handles NAK retransmissions (TransmitCount >= 1)                │   │
│  │                                                                      │   │
│  │  ┌─────────────────────────────────────────────────────────────────┐ │   │
│  │  │ for { // Continuous loop                                        │ │   │
│  │  │                                                                 │ │   │
│  │  │     // 1. DRAIN RETRANSMIT RING TO BTREE (for NAK lookup)       │ │   │
│  │  │     for pkt := dataRing.TryPop(); pkt != nil; {                 │ │   │
│  │  │         packetBtree.Insert(pkt)  // Insert for tracking         │ │   │
│  │  │     }                                                           │ │   │
│  │  │                                                                 │ │   │
│  │  │     // 2. SEND PACKETS WITH TransmitCount == 0 (first send)     │ │   │
│  │  │     packetBtree.IterateFrom(startSeq, func(p) {                 │ │   │
│  │  │         if p.Header().TransmitCount == 0 {                      │ │   │
│  │  │             deliver(p)  // Send via io_uring or syscall         │ │   │
│  │  │             p.Header().TransmitCount = 1  // Mark as sent       │ │   │
│  │  │             m.SendFirstTransmit.Add(1)                          │ │   │
│  │  │         }                                                       │ │   │
│  │  │     })                                                          │ │   │
│  │  │                                                                 │ │   │
│  │  │     // 3. PROCESS CONTROL PACKETS                               │ │   │
│  │  │     for cp := controlRing.TryPop(); cp != nil; {                │ │   │
│  │  │         switch cp.Type {                                        │ │   │
│  │  │         case ACK:                                               │ │   │
│  │  │             ackBtree(cp.Seq)  // Remove ACK'd packets           │ │   │
│  │  │         case NAK:                                               │ │   │
│  │  │             // RETRANSMIT: lookup, send, increment TransmitCount│ │   │
│  │  │             nakRetransmit(cp.Seqs)                              │ │   │
│  │  │         }                                                       │ │   │
│  │  │     }                                                           │ │   │
│  │  │                                                                 │ │   │
│  │  │     // 4. EXPIRE OLD PACKETS (drop threshold exceeded)          │ │   │
│  │  │     now := nowFn()                                              │ │   │
│  │  │     packetBtree.DeleteBeforeFunc(expirySeq, func(p) {           │ │   │
│  │  │         if now - p.PktTsbpdTime > dropThreshold {               │ │   │
│  │  │             p.Decommission()  // Return buffer to pool          │ │   │
│  │  │             m.SendDropTooOld.Add(1)                             │ │   │
│  │  │         }                                                       │ │   │
│  │  │     })                                                          │ │   │
│  │  │                                                                 │ │   │
│  │  │     // 5. Adaptive backoff if idle                              │ │   │
│  │  │ }                                                               │ │   │
│  │  └─────────────────────────────────────────────────────────────────┘ │   │
│  │                                                                      │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 7.4 Comparison: Current vs Proposed

| Aspect | Current Architecture | Proposed Architecture |
|--------|---------------------|----------------------|
| New packet path | Write → Channel → Goroutine → Ring → EventLoop → Btree → TSBPD → Deliver | Write → Ring → EventLoop → Btree → Send (if TransmitCount==0) |
| Latency for new packets | 2-10µs + up to 10ms Tick interval | ~2-5µs (EventLoop runs continuously) |
| Channel usage | `writeQueue` channel | None |
| Extra goroutines | `writeQueueReader` | None |
| Btree purpose | Buffer + TSBPD delivery | Buffer + first send + NAK retransmit |
| EventLoop purpose | Drain + Deliver + Control | Drain + First Send + Control + Expire |
| Sequence number | Not thread-safe | Atomic 31-bit |
| Buffer copies | One buffer, flows through system | One buffer, flows through system (no extra copy) |
| First send trigger | TSBPD time reached | TransmitCount == 0 |

### 7.5 Key Changes Required

| Component | Current | Proposed | Reason |
|-----------|---------|----------|--------|
| `RetransmitCount` | Track retransmits only | Rename to `TransmitCount`, starts at 0 | Identify never-sent packets |
| `nextSequenceNumber` | `circular.Number` | `atomic.Uint32` + 31-bit mask | Thread-safe multi-writer |
| `writeQueue` | `chan packet.Packet` | Eliminated | Push directly to ring |
| `writeQueueReader` | Goroutine | Eliminated | No channel to read |
| `Write()` | Push to channel | Push to ring (TransmitCount=0) | Direct path, no goroutine |
| EventLoop btree scan | Check TSBPD time | Check TransmitCount==0, then send | First send on scan |
| NAK handler | Send, set RetransmittedPacketFlag | Send, increment TransmitCount | Track all transmissions |

### 7.6 Atomic 31-Bit Sequence Number Design

**CRITICAL:** SRT sequence numbers are 31 bits, not 32 bits. See Section 3.3.

**REUSE EXISTING CONSTANT:** `packet.MAX_SEQUENCENUMBER` is already defined:

```go
// packet/packet.go - EXISTING constant
const MAX_SEQUENCENUMBER uint32 = 0b01111111_11111111_11111111_11111111  // 0x7FFFFFFF
```

This constant is already used throughout the codebase for 31-bit masking.

```go
// Current (NOT thread-safe):
type sender struct {
    nextSequenceNumber circular.Number  // NOT atomic, 31-bit internally
}

func (s *sender) pushRing(p packet.Packet) {
    currentSeq := s.nextSequenceNumber
    p.Header().PacketSequenceNumber = currentSeq
    // ... push to ring ...
    s.nextSequenceNumber = s.nextSequenceNumber.Inc()  // RACE CONDITION!
}

// ════════════════════════════════════════════════════════════════════════════
// PROPOSED: Thread-safe 31-bit atomic sequence number
// ════════════════════════════════════════════════════════════════════════════

type sender struct {
    nextSeqAtomic atomic.Uint32  // Atomic counter (32-bit storage)
    initialSeq    uint32          // ISN from handshake (31-bit)
}

// assignSequenceNumber atomically assigns the next 31-bit sequence number
// Safe to call from multiple goroutines
func (s *sender) assignSequenceNumber(p packet.Packet) uint32 {
    // Step 1: Atomically increment counter and get previous value
    rawOffset := s.nextSeqAtomic.Add(1) - 1

    // Step 2: Add to initial sequence number
    rawSeq := s.initialSeq + rawOffset

    // Step 3: Mask to 31 bits using EXISTING constant
    // This ensures: 0x7FFFFFFF + 1 → 0x00000000 (not 0x80000000)
    seq31 := rawSeq & packet.MAX_SEQUENCENUMBER

    // Step 4: Assign to packet header (also uses existing constant)
    p.Header().PacketSequenceNumber = circular.New(seq31, packet.MAX_SEQUENCENUMBER)

    return seq31
}

// Example wraparound:
//   initialSeq = 0x7FFFFFF0 (near max)
//   rawOffset  = 20
//   rawSeq     = 0x80000004 (overflows 31 bits)
//   seq31      = 0x00000004 (correctly wrapped to 31 bits)
```

### 7.7 Buffer Pool Management (Simplified with TransmitCount)

The gosrt library uses a global `sync.Pool` for zero-copy buffer management (see `buffers.go`).
With the TransmitCount approach, buffer management is **simpler** - only ONE buffer per packet:
- No extra buffer copy for "send immediate"
- Packet buffer flows through the entire system
- Returned to pool when ACK'd or expired

#### 7.7.1 Single Buffer Lifetime Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│               BUFFER LIFETIME WITH TRANSMITCOUNT (SIMPLIFIED)               │
│                                                                             │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │                    SINGLE BUFFER STRATEGY                              │ │
│  │                                                                        │ │
│  │  ONE buffer per packet - flows through entire system:                  │ │
│  │                                                                        │ │
│  │  GetBuffer() → Write() → Ring → Btree → Send (TransmitCount++) →       │ │
│  │                              ↓                                         │ │
│  │                         NAK retransmit (TransmitCount++)               │ │
│  │                              ↓                                         │ │
│  │                    ACK or Expire → Decommission() → PutBuffer()        │ │
│  │                                                                        │ │
│  │  NO EXTRA COPY NEEDED because:                                         │ │
│  │  - io_uring/WriteTo gets pointer to packet buffer                      │ │
│  │  - Buffer stays in btree while io_uring completes                      │ │
│  │  - Retransmit uses same buffer (just re-sends)                         │ │
│  │  - Only freed when ACK'd (no longer needed) or expired                 │ │
│  │                                                                        │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Write(data) Flow:                                                          │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  // Allocate packet with buffer from global pool                      │  │
│  │  p := packet.NewPacket(nil)  // GetBuffer() internally                │  │
│  │  p.SetData(data)                                                      │  │
│  │  p.Header().TransmitCount = 0  // Never sent yet                      │  │
│  │  p.Header().PktTsbpdTime = c.getTimestamp()                           │  │
│  │  c.snd.AssignSequenceNumber(p)  // Atomic 31-bit                      │  │
│  │                                                                       │  │
│  │  // Push to ring (buffer stays with packet)                           │  │
│  │  dataRing.Push(p)                                                     │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                  │                                          │
│                                  ▼                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  // EventLoop drains ring → btree                                     │  │
│  │  pkt := dataRing.TryPop()                                             │  │
│  │  packetBtree.Insert(pkt)  // Buffer still with packet                 │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                  │                                          │
│                                  ▼                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  // EventLoop scans btree, sends packets with TransmitCount == 0      │  │
│  │  if p.Header().TransmitCount == 0 {                                   │  │
│  │      deliver(p)  // p.Bytes() points to same buffer                   │  │
│  │      p.Header().TransmitCount = 1                                     │  │
│  │  }                                                                    │  │
│  │  // Buffer STAYS in btree for potential NAK retransmit                │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                  │                                          │
│                                  ▼                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  // On NAK: retransmit from btree (same buffer)                       │  │
│  │  p := packetBtree.Get(nakSeq)                                         │  │
│  │  if p != nil {                                                        │  │
│  │      p.Header().RetransmittedPacketFlag = true  // Wire flag          │  │
│  │      deliver(p)  // Same buffer, re-sent                              │  │
│  │      p.Header().TransmitCount++  // Track retransmit count            │  │
│  │  }                                                                    │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                  │                                          │
│                                  ▼                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │  // On ACK or Expire: return buffer to pool                           │  │
│  │  packetBtree.DeleteBefore(ackSeq)  // or expire threshold             │  │
│  │  for each removed packet p {                                          │  │
│  │      p.Decommission()  // Returns buffer to global pool               │  │
│  │  }                                                                    │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 7.7.2 Buffer Pool Summary (TransmitCount Approach)

| Buffer | Source | When Returned | Returned By |
|--------|--------|---------------|-------------|
| Packet buffer | `packet.NewPacket()` → global pool | ACK received or expire threshold | `p.Decommission()` |

**Only ONE buffer per packet** - no extra send buffer needed!

#### 7.7.3 io_uring Consideration

With TransmitCount approach, io_uring sends use the packet buffer directly:
- Buffer stays in btree while io_uring completes
- Same buffer can be retransmitted on NAK
- Only freed when ACK'd or expired

**Note:** The existing io_uring send path (`connection_linux.go`) already marshals the packet to a per-connection buffer pool. This is fine - that's for the wire format, separate from the packet payload buffer.

### 7.8 TransmitCount-Based Write() Implementation

```go
// connection_io.go - TransmitCount-based implementation

func (c *srtConn) Write(b []byte) (int, error) {
    // ... existing buffer handling ...

    for c.writeBuffer.Len() > 0 {
        n := min(c.writeBuffer.Len(), int(c.payloadSize))
        c.writeBuffer.Read(c.writeData[:n])

        // 1. Allocate packet buffer (from global pool)
        p := packet.NewPacket(nil)
        p.SetData(c.writeData[:n])
        p.Header().IsControlPacket = false

        // 2. Initialize TransmitCount = 0 (never sent)
        p.Header().TransmitCount = 0

        // 3. Set TSBPD time (relative to connection start)
        p.Header().PktTsbpdTime = c.getTimestamp()

        // 4. Assign 31-bit sequence number (ATOMIC, thread-safe)
        seq := c.snd.AssignSequenceNumber(p)

        // 5. Push to data ring (EventLoop will send when TransmitCount==0)
        if !c.snd.Push(p) {
            // Ring full - return error or drop
            c.metrics.SendRingDropped.Add(1)
            p.Decommission()
        }
    }

    return len(b), nil
}
```

### 7.9 EventLoop First-Send and Retransmit Logic

#### 7.9.1 Control Packet Priority Pattern

To minimize RTT measurement latency, we service the control ring **between every action**:

```go
// congestion/live/send/eventloop.go - Control-priority pattern

func (s *sender) EventLoop(ctx context.Context) {
    // Define ordered actions for the EventLoop
    actions := []func(){
        s.drainRingToBtree,           // Move data packets from ring to btree
        s.deliverReadyPackets,        // TSBPD scan + TransmitCount=0 check (unified)
        s.dropOldPacketsEventLoop,    // Remove packets past drop threshold
    }

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        workDone := false

        // Execute each action, servicing control ring BEFORE each one
        for _, action := range actions {
            // ════════════════════════════════════════════════════════════
            // SERVICE CONTROL RING FIRST (minimize ACK/NAK latency)
            // ════════════════════════════════════════════════════════════
            if s.processControlPackets() > 0 {
                workDone = true
            }

            // Then execute the action
            action()
        }

        // Final control packet check after all actions
        if s.processControlPackets() > 0 {
            workDone = true
        }

        // Adaptive backoff only if no work done
        if !workDone {
            s.idleBackoff()
        }
    }
}
```

#### 7.9.2 Why This Pattern Matters

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    CONTROL PACKET PRIORITY PATTERN                           │
│                                                                             │
│  BEFORE (control processed once per iteration):                             │
│                                                                             │
│    ┌─────────┐   ┌─────────┐   ┌─────────┐   ┌─────────┐                   │
│    │ drain   │──►│ deliver │──►│ drop    │──►│ control │                   │
│    │ ring    │   │ ready   │   │ old     │   │ packets │                   │
│    │ (~1µs)  │   │ (~10µs) │   │ (~1µs)  │   │         │                   │
│    └─────────┘   └─────────┘   └─────────┘   └─────────┘                   │
│                                                                             │
│    ACKACK arrives during "deliver ready" → waits ~11µs to be processed     │
│                                                                             │
│  ─────────────────────────────────────────────────────────────────────────  │
│                                                                             │
│  AFTER (control processed before each action):                              │
│                                                                             │
│    ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐ │
│    │ control │►│ drain   │►│ control │►│ deliver │►│ control │►│ drop    │ │
│    │         │ │ ring    │ │         │ │ ready   │ │         │ │ old     │ │
│    └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘ └─────────┘ │
│                                                                             │
│    ACKACK arrives during "deliver ready" → processed within ~1µs           │
│                                                                             │
│  RESULT: Control packet latency reduced from ~11µs to ~1µs                  │
│          RTT measurements are more accurate                                 │
│          NAK retransmissions happen faster                                  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 7.9.3 Unified TSBPD + TransmitCount Logic

Instead of a separate `sendFirstTransmitPackets()`, we extend the existing `deliverReadyPacketsEventLoop()`:

```go
// deliverReadyPacketsEventLoop - EXTENDED with TransmitCount
// Existing function already scans from deliveryStartPoint, checks TSBPD.
// We add TransmitCount check to track first-transmission vs already-sent.

s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
    tsbpdTime := p.Header().PktTsbpdTime

    // TSBPD check (existing)
    if tsbpdTime > nowUs {
        nextDeliveryIn = time.Duration(tsbpdTime-nowUs) * time.Microsecond
        return false // Stop iteration - not ready yet
    }

    // ═══════════════════════════════════════════════════════════════════
    // NEW: TransmitCount check - only send if not already transmitted
    // ═══════════════════════════════════════════════════════════════════
    if p.Header().TransmitCount == 0 {
        // First transmission - send and mark as sent
        s.deliver(p)
        p.Header().TransmitCount = 1
        delivered++
        m.SendFirstTransmit.Add(1)
        m.CongestionSendPktUnique.Add(1)  // First-time send
    } else {
        // Already sent - skip (retransmissions handled by NAK processing)
        m.SendAlreadySent.Add(1)
    }

    // Update delivery start point (move past this packet)
    s.deliveryStartPoint.Store(uint64(circular.SeqAdd(seq, 1)))

    return true // Continue to next packet
})
```

**Why This Works:**

| Scenario | tsbpdTime | TransmitCount | Action |
|----------|-----------|---------------|--------|
| New packet, ready | `<= nowUs` | 0 | Send, set TransmitCount=1 |
| New packet, not ready | `> nowUs` | 0 | Stop iteration (wait) |
| Already sent, in btree | `<= nowUs` | ≥1 | Skip (stays for NAK retransmit) |
| Retransmit (NAK) | n/a | ≥1 | NAK handler sends, increments TransmitCount |

**Key Insight:** The packet stays in the btree after first transmission for potential NAK-triggered retransmission. It's only removed when ACK'd or dropped as too-old.

#### 7.9.4 NAK Retransmission (Separate Path)

```go
func (s *sender) nakRetransmit(seqs []circular.Number) {
    for _, seq := range seqs {
        item := s.packetBtree.Get(seq.Val())
        if item == nil {
            // Packet not found - already ACK'd or expired
            s.metrics.SendNakNotFound.Add(1)
            continue
        }

        p := item.packet

        // Set wire flag for retransmission
        p.Header().RetransmittedPacketFlag = true

        // Send retransmission
        s.deliver(p)

        // Increment transmit count
        p.Header().TransmitCount++

        s.metrics.SendRetransmit.Add(1)
    }
}
```

---

## 8. Implementation Phases

### Phase 1: Rename RetransmitCount to TransmitCount

1. In `packet/packet.go` `PacketHeader` struct:
   - Rename `RetransmitCount` → `TransmitCount`
   - Update comment: "Number of times this packet has been transmitted (0=never sent)"
2. Update all call sites:
   - `congestion/live/send/*.go`
   - Any tests referencing `RetransmitCount`
3. Initialize `TransmitCount = 0` in `packet.NewPacket()`

### Phase 2: Atomic 31-Bit Sequence Number

1. Add `nextSeqAtomic atomic.Uint32` and `initialSeq uint32` to sender struct
2. Create `AssignSequenceNumber(p packet.Packet) uint32` method
3. Use 31-bit mask: `(initialSeq + offset) & packet.MAX_SEQUENCENUMBER`
4. Add comprehensive tests for wraparound at `packet.MAX_SEQUENCENUMBER → 0x00000000`
5. Verify sequence numbers match `circular/seq_math_31bit_wraparound_test.go` expectations

### Phase 3: Extend deliverReadyPacketsEventLoop() with TransmitCount

1. Modify existing `deliverReadyPacketsEventLoop()` to add TransmitCount check
2. In btree scan, after TSBPD check, add: `if p.Header().TransmitCount == 0`
3. Call `deliver(p)` and set `TransmitCount = 1` only for first transmission
4. Skip packets with `TransmitCount >= 1` (already sent, kept for NAK retransmit)
5. Add metric `SendFirstTransmit` and `SendAlreadySent`
6. Test that packets with TransmitCount==0 get sent, TransmitCount>=1 skipped

### Phase 4: Update NAK Handler for TransmitCount

1. Modify `nakRetransmit()` to increment `TransmitCount` on each retransmit
2. Set `RetransmittedPacketFlag = true` when `TransmitCount >= 2`
3. Add metric `SendNakNotFound` for NAK'd packets already ACK'd/expired
4. Test retransmit increments TransmitCount correctly

### Phase 5: Eliminate writeQueueReader

1. In `Write()`, push directly to dataRing (TransmitCount=0)
2. Don't start `writeQueueReader` goroutine
3. Don't create `writeQueue` channel
4. Add config option `UseDirectPush bool` (default true with EventLoop)
5. Verify all tests pass

### Phase 6: Full Integration and Metrics

1. Enable by default with EventLoop mode
2. Add metrics:
   - `send_first_transmit_total` - Packets sent with TransmitCount=0→1
   - `send_retransmit_total` - NAK-triggered retransmissions
   - `send_nak_not_found_total` - NAK for missing packets
3. Benchmark latency improvement (expected: 2-5µs vs 10ms+ with Tick)

---

## 9. Risk Analysis

| Risk | Impact | Mitigation |
|------|--------|------------|
| 31-bit wraparound bugs | Packet ordering failures | Comprehensive tests + circular package |
| Sequence gaps (ring full) | Receiver sends NAK, can't retransmit | Acceptable - packet was sent, just not buffered |
| Send fails after seq assigned | Gap in sequence numbers | Receiver handles via NAK |
| Multi-writer contention on seq | Atomic handles this | ~10ns per atomic op, acceptable |
| Retransmit ring full | Can't retransmit on NAK | Monitor metric, size ring appropriately |
| io_uring submit fails | Packet not sent | Return error to application |

---

## 10. Metrics for Monitoring

| Metric | Description |
|--------|-------------|
| `send_direct_push_total` | Packets pushed directly (bypassing channel) |
| `send_seq_atomic_ops_total` | Atomic sequence number assignments |
| `send_ring_contention_total` | Ring push retries due to contention |
| `send_seq_gaps_total` | Sequence gaps created (ring full drops) |

---

## 11. Next Steps

1. [ ] Review this design with user
2. [ ] Implement Phase 1 (atomic sequence number)
3. [ ] Benchmark single-writer vs multi-writer performance
4. [ ] Implement Phase 2 (direct push)
5. [ ] Measure latency reduction
6. [ ] Full integration testing

