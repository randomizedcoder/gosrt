# Sender Lock-Free Architecture Implementation Plan

## Introduction

This document provides a detailed, step-by-step implementation plan for the lock-free sender architecture described in [sender_lockfree_architecture.md](./sender_lockfree_architecture.md). The goal is to eliminate the dependency on channels and locks in the sender's hot path by:

1. **Renaming `RetransmitCount` to `TransmitCount`** - Track whether a packet has been sent (0 = never sent)
2. **Implementing atomic 31-bit sequence numbers** - Thread-safe sequence assignment in `Write()`
3. **Extending `deliverReadyPacketsEventLoop()`** - Unified TSBPD + TransmitCount logic
4. **Updating NAK handlers** - Increment `TransmitCount` on retransmission
5. **Implementing control packet priority pattern** - Service control ring between each action
6. **Eliminating `writeQueue` channel and `writeQueueReader`** - Direct push from `Write()` to sender ring
7. **Adding comprehensive metrics** - Track new code paths

**Architecture Terminology:**
- The design document refers to `dataRing` - this is implemented as `packetRing` (`*SendPacketRing`) in the sender
- `controlRing` (`*SendControlRing`) handles ACK/NAK packets from network
- `packetBtree` (`*SendPacketBtree`) stores packets for TSBPD ordering and NAK retransmit lookup

**Key Connection-Level Changes:**
The `writeQueue` channel and `writeQueueReader` goroutine live at the **connection level** (`connection.go`, `connection_io.go`), not in the sender package. Phase 6 addresses eliminating these.

Each phase includes specific files, functions, line numbers, and required test updates.

---

## Table of Contents

- [Introduction](#introduction)
- [Architecture Overview](#architecture-overview)
  - [Current Data Flow](#current-data-flow)
  - [Proposed Data Flow](#proposed-data-flow)
- [Prerequisites](#prerequisites)
- [Phase 1: Rename RetransmitCount to TransmitCount](#phase-1-rename-retransmitcount-to-transmitcount)
  - [1.1 Objective](#11-objective)
  - [1.2 Files and Changes](#12-files-and-changes)
  - [1.3 Unit Test Updates](#13-unit-test-updates)
  - [1.4 Verification Steps](#14-verification-steps)
- [Phase 2: Atomic 31-Bit Sequence Number](#phase-2-atomic-31-bit-sequence-number)
  - [2.1 Objective](#21-objective)
  - [2.2 Files and Changes](#22-files-and-changes)
  - [2.3 Unit Test Updates](#23-unit-test-updates)
  - [2.4 Verification Steps](#24-verification-steps)
- [Phase 3: Extend deliverReadyPacketsEventLoop() with TransmitCount](#phase-3-extend-deliverreadypacketseventloop-with-transmitcount)
  - [3.1 Objective](#31-objective)
  - [3.2 Files and Changes](#32-files-and-changes)
  - [3.3 Unit Test Updates](#33-unit-test-updates)
  - [3.4 Verification Steps](#34-verification-steps)
- [Phase 4: Update NAK Handler for TransmitCount](#phase-4-update-nak-handler-for-transmitcount)
  - [4.1 Objective](#41-objective)
  - [4.2 Files and Changes](#42-files-and-changes)
  - [4.3 Unit Test Updates](#43-unit-test-updates)
  - [4.4 Verification Steps](#44-verification-steps)
- [Phase 5: Implement Control Packet Priority Pattern](#phase-5-implement-control-packet-priority-pattern)
  - [5.1 Objective](#51-objective)
  - [5.2 Files and Changes](#52-files-and-changes)
  - [5.3 Unit Test Updates](#53-unit-test-updates)
  - [5.4 Verification Steps](#54-verification-steps)
- [Phase 6: Eliminate writeQueue Channel (Connection Level)](#phase-6-eliminate-writequeue-channel-connection-level)
  - [6.1 Objective](#61-objective)
  - [6.2 Files and Changes](#62-files-and-changes)
  - [6.3 Unit Test Updates](#63-unit-test-updates)
  - [6.4 Verification Steps](#64-verification-steps)
- [Phase 7: Full Integration and Metrics](#phase-7-full-integration-and-metrics)
  - [7.1 Objective](#71-objective)
  - [7.2 Files and Changes](#72-files-and-changes)
  - [7.3 Unit Test Updates](#73-unit-test-updates)
  - [7.4 Verification Steps](#74-verification-steps)
- [Phase 8: Integration Tests](#phase-8-integration-tests)
- [Post-Implementation Checklist](#post-implementation-checklist)
- [Risk Mitigation](#risk-mitigation)

---

## Architecture Overview

### Current Data Flow

The current write path goes through a Go channel and dedicated goroutine:

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        CURRENT WRITE PATH (SLOW)                                 │
│                                                                                 │
│  ┌──────────────┐    ┌─────────────────┐    ┌────────────────────┐              │
│  │ App Write()  │───►│ writeQueue      │───►│ writeQueueReader() │              │
│  │ connection_  │    │ (chan, 1024)    │    │ goroutine          │              │
│  │ io.go:112    │    │ connection.go   │    │ connection_io.go   │              │
│  └──────────────┘    └─────────────────┘    └─────────┬──────────┘              │
│                                                       │                          │
│                                                       ▼                          │
│                                             ┌─────────────────┐                  │
│                                             │ snd.Push()      │                  │
│                                             │ push.go         │                  │
│                                             └────────┬────────┘                  │
│                                                      │                           │
│                     ┌────────────────────────────────┼────────────────────────┐  │
│                     │                                │                        │  │
│                     ▼                                ▼                        │  │
│           ┌─────────────────┐              ┌─────────────────┐                │  │
│           │ useSendRing=true│              │ useSendRing=false│               │  │
│           │ pushRing()      │              │ pushBtree()      │               │  │
│           └────────┬────────┘              └─────────────────┘                │  │
│                    │                                                          │  │
│                    ▼                                                          │  │
│           ┌─────────────────┐                                                 │  │
│           │ packetRing      │  ◄── "dataRing" in architecture doc             │  │
│           │ (ShardedRing)   │                                                 │  │
│           └────────┬────────┘                                                 │  │
│                    │                                                          │  │
│                    ▼                                                          │  │
│           ┌─────────────────┐                                                 │  │
│           │ EventLoop       │                                                 │  │
│           │ drainRingTo     │                                                 │  │
│           │ BtreeEventLoop()│                                                 │  │
│           └────────┬────────┘                                                 │  │
│                    │                                                          │  │
│                    ▼                                                          │  │
│           ┌─────────────────┐                                                 │  │
│           │ packetBtree     │                                                 │  │
│           │ (SendPacket     │                                                 │  │
│           │  Btree)         │                                                 │  │
│           └────────┬────────┘                                                 │  │
│                    │                                                          │  │
│                    ▼                                                          │  │
│           ┌─────────────────┐                                                 │  │
│           │ deliverReady    │                                                 │  │
│           │ PacketsEventLoop│  TSBPD check only (current)                     │  │
│           │ () → deliver()  │                                                 │  │
│           └─────────────────┘                                                 │  │
│                                                                               │  │
│  LATENCY: ~200-500ns channel + goroutine context switch + TSBPD wait          │  │
│                                                                               │  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### Proposed Data Flow

The proposed architecture eliminates the channel and goroutine:

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        PROPOSED WRITE PATH (FAST)                                │
│                                                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────┐   │
│  │ App Write()                                                               │   │
│  │ connection_io.go                                                          │   │
│  │                                                                           │   │
│  │  1. Allocate packet: packet.NewPacket(data)                               │   │
│  │  2. Set PktTsbpdTime = c.getTimestamp()                                   │   │
│  │  3. Set TransmitCount = 0  (NEW - never sent)                             │   │
│  │  4. Assign sequence: snd.assignSequenceNumber(p) (NEW - atomic 31-bit)    │   │
│  │  5. Push to ring: snd.pushRingDirect(p) (NEW - bypass channel)            │   │
│  │                                                                           │   │
│  └──────────────────────────────────────────────────────────────────────────┘   │
│                    │                                                             │
│                    │ Direct push (NO channel, NO goroutine)                      │
│                    ▼                                                             │
│           ┌─────────────────┐                                                    │
│           │ packetRing      │  "dataRing" in design - packets with               │
│           │ (ShardedRing)   │  TransmitCount=0 waiting for first send            │
│           └────────┬────────┘                                                    │
│                    │                                                             │
│                    ▼                                                             │
│           ┌─────────────────────────────────────────────────────────────────┐    │
│           │ EventLoop (Continuous - single thread)                          │    │
│           │                                                                 │    │
│           │  for { // Control packet priority pattern                       │    │
│           │      processControlPackets()  // Service ACK/NAK first          │    │
│           │      drainRingToBtreeEventLoop()                                │    │
│           │      processControlPackets()  // Service ACK/NAK                │    │
│           │      deliverReadyPacketsEventLoop() // TSBPD + TransmitCount    │    │
│           │      processControlPackets()  // Service ACK/NAK                │    │
│           │      dropOldPackets()                                           │    │
│           │  }                                                              │    │
│           └─────────────────────────────────────────────────────────────────┘    │
│                    │                                                             │
│                    ▼                                                             │
│           ┌─────────────────┐                                                    │
│           │ deliverReady    │                                                    │
│           │ PacketsEventLoop│                                                    │
│           │                 │                                                    │
│           │ if TSBPD ready  │                                                    │
│           │   AND           │  ◄── NEW: Check TransmitCount==0                   │
│           │   TransmitCount │                                                    │
│           │   == 0:         │                                                    │
│           │     deliver(p)  │                                                    │
│           │     TransmitCount│                                                   │
│           │     = 1         │                                                    │
│           └─────────────────┘                                                    │
│                                                                                  │
│  LATENCY: ~50ns atomic + ~50ns ring push = ~100ns (vs ~500ns+ before)            │
│                                                                                  │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### Key Files Reference

| Component | Current File | Line Numbers |
|-----------|--------------|--------------|
| `writeQueue` channel | `connection.go` | Line 147, 383-387 |
| `writeQueueReader` goroutine | `connection_io.go` | Lines 243-263 |
| `Write()` with channel | `connection_io.go` | Lines 106-115 |
| `snd.Push()` | `congestion/live/send/push.go` | Lines 24-51 |
| `pushRing()` | `congestion/live/send/push.go` | Lines 115-157 |
| `packetRing` (dataRing) | `congestion/live/send/sender.go` | Line 171 |
| `EventLoop` | `congestion/live/send/eventloop.go` | Lines 36-130 |
| `deliverReadyPacketsEventLoop` | `congestion/live/send/eventloop.go` | Lines 318-416 |

---

## Prerequisites

Before starting implementation:

1. **Verify current tests pass:**
   ```bash
   go test ./congestion/live/send/... -v -race
   go test ./packet/... -v -race
   ```

2. **Review architecture document:**
   - [sender_lockfree_architecture.md](./sender_lockfree_architecture.md)

3. **Ensure dependencies are current:**
   ```bash
   go mod tidy
   ```

---

## Phase 1: Rename RetransmitCount to TransmitCount

### 1.1 Objective

Rename `RetransmitCount` to `TransmitCount` to better reflect its purpose: tracking total transmissions (0 = never sent, 1 = first send, 2+ = retransmits).

**Reference:** [sender_lockfree_architecture.md Section 7.3](./sender_lockfree_architecture.md#73-transmitcount-field-semantics)

### 1.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `packet/packet.go` | Line 268 | Rename `RetransmitCount` → `TransmitCount` |
| `packet/packet.go` | Line 407 | Update `p.header.RetransmitCount = 0` → `p.header.TransmitCount = 0` |
| `congestion/live/send/nak.go` | Lines 93, 100 | Update `RetransmitCount` references |
| `congestion/live/send/nak.go` | Lines 202, 210 | Update `RetransmitCount` references |
| `congestion/live/send/nak.go` | Lines 310-320 | Update `RetransmitCount` references |

#### packet/packet.go

**Line 268 - Struct field rename:**
```go
// BEFORE:
RetransmitCount      uint32 // Number of times this packet has been retransmitted

// AFTER:
TransmitCount        uint32 // Number of times this packet has been transmitted (0=never, 1=first, 2+=retransmit)
```

**Line 407 - Reset in NewPacket:**
```go
// BEFORE:
p.header.RetransmitCount = 0      // Reset retransmit tracking (Phase 6: RTO Suppression)

// AFTER:
p.header.TransmitCount = 0        // Initialize transmission count (0 = never sent)
```

#### congestion/live/send/nak.go

**Search and replace all occurrences:**
```bash
# Find all occurrences
grep -n "RetransmitCount" congestion/live/send/nak.go
```

Each occurrence changes:
```go
// BEFORE:
p.Header().RetransmitCount++
p.Header().RetransmittedPacketFlag = p.Header().RetransmitCount > 0

// AFTER:
p.Header().TransmitCount++
p.Header().RetransmittedPacketFlag = p.Header().TransmitCount > 1  // >1 means retransmit
```

### 1.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `packet/packet_test.go` | Update any tests referencing `RetransmitCount` |
| `congestion/live/send/nak_table_test.go` | Update tests to verify `TransmitCount` behavior |
| `congestion/live/send/sender_coverage_test.go` | Update `nakLockedOriginal` and `nakLockedHonorOrder` tests |

**New test to add in `packet/packet_test.go`:**
```go
func TestPacketHeader_TransmitCount(t *testing.T) {
    p := NewPacket(nil)

    // Initial state
    require.Equal(t, uint32(0), p.Header().TransmitCount, "New packet should have TransmitCount=0")
    require.False(t, p.Header().RetransmittedPacketFlag, "New packet should not be marked retransmitted")

    // After first transmission
    p.Header().TransmitCount = 1
    require.False(t, p.Header().RetransmittedPacketFlag, "First send should not set retransmit flag")

    // After retransmission
    p.Header().TransmitCount = 2
    p.Header().RetransmittedPacketFlag = p.Header().TransmitCount > 1
    require.True(t, p.Header().RetransmittedPacketFlag, "Retransmit should set flag")
}
```

### 1.4 Verification Steps

```bash
# 1. Build to check for compile errors
go build ./...

# 2. Run packet tests
go test ./packet/... -v -race

# 3. Run sender tests
go test ./congestion/live/send/... -v -race

# 4. Run full test suite
go test ./... -race
```

---

## Phase 2: Atomic 31-Bit Sequence Number

### 2.1 Objective

Replace the non-atomic `nextSequenceNumber circular.Number` with an atomic counter that properly handles 31-bit wraparound per SRT protocol spec.

**Reference:** [sender_lockfree_architecture.md Section 3.3](./sender_lockfree_architecture.md#33-31-bit-sequence-number-requirement)

### 2.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `congestion/live/send/sender.go` | Line 124 | Add atomic sequence fields |
| `congestion/live/send/sender.go` | Lines 203-204 | Initialize atomic fields |
| `congestion/live/send/push.go` | Lines 71-77, 121-130 | Use atomic sequence assignment |

#### congestion/live/send/sender.go

**Line 124 - Add new fields to sender struct:**
```go
type sender struct {
    // EXISTING (keep for legacy list mode):
    nextSequenceNumber circular.Number

    // NEW: Atomic 31-bit sequence for ring mode
    nextSeqOffset  atomic.Uint32  // Offset from initialSeq (incremented atomically)
    initialSeq     uint32          // Starting sequence number (set once at init)

    // ... rest of struct unchanged
}
```

**Lines 203-204 - Initialize in NewSender:**
```go
s := &sender{
    nextSequenceNumber: sendConfig.InitialSequenceNumber,  // Legacy
    initialSeq:         sendConfig.InitialSequenceNumber.Val(),  // NEW: For atomic mode
    // ... rest unchanged
}
```

#### congestion/live/send/push.go

**Create new function `assignSequenceNumber` (add after line 51):**
```go
// assignSequenceNumber atomically assigns a 31-bit sequence number.
// Uses offset from initialSeq to handle wraparound correctly.
// Formula: (initialSeq + offset) & packet.MAX_SEQUENCENUMBER
//
// Reference: sender_lockfree_architecture.md Section 3.3
func (s *sender) assignSequenceNumber() circular.Number {
    offset := s.nextSeqOffset.Add(1) - 1  // Get current, then increment
    rawSeq := (s.initialSeq + offset) & packet.MAX_SEQUENCENUMBER
    return circular.New(rawSeq, packet.MAX_SEQUENCENUMBER)
}
```

**Lines 121-130 - Update pushRing to use atomic:**
```go
func (s *sender) pushRing(p packet.Packet) {
    if p == nil {
        return
    }
    m := s.metrics

    // CHANGED: Use atomic sequence assignment
    // Assigns sequence number BEFORE ring push for deterministic ordering
    seqNum := s.assignSequenceNumber()
    p.Header().PacketSequenceNumber = seqNum
    p.Header().PacketPositionFlag = packet.SinglePacket
    p.Header().OrderFlag = false
    p.Header().MessageNumber = 1

    // NEW: Initialize TransmitCount to 0 (never sent)
    p.Header().TransmitCount = 0

    // ... rest unchanged (timestamp, probe, ring push)
}
```

### 2.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `congestion/live/send/push_test.go` | Add atomic sequence tests |
| `congestion/live/send/sender_wraparound_table_test.go` | Verify 31-bit wraparound with atomic |

**New tests to add in `congestion/live/send/push_test.go`:**
```go
func TestAssignSequenceNumber_Atomic(t *testing.T) {
    cfg := SendConfig{
        InitialSequenceNumber: circular.New(100, packet.MAX_SEQUENCENUMBER),
        ConnectionMetrics:     metrics.NewConnectionMetrics(0),
        UseBtree:              true,
        UseSendRing:           true,
    }
    s := NewSender(cfg).(*sender)

    // First assignment
    seq1 := s.assignSequenceNumber()
    require.Equal(t, uint32(100), seq1.Val())

    // Second assignment
    seq2 := s.assignSequenceNumber()
    require.Equal(t, uint32(101), seq2.Val())
}

func TestAssignSequenceNumber_Wraparound(t *testing.T) {
    // Start near MAX_SEQUENCENUMBER
    cfg := SendConfig{
        InitialSequenceNumber: circular.New(packet.MAX_SEQUENCENUMBER-1, packet.MAX_SEQUENCENUMBER),
        ConnectionMetrics:     metrics.NewConnectionMetrics(0),
        UseBtree:              true,
        UseSendRing:           true,
    }
    s := NewSender(cfg).(*sender)

    seq1 := s.assignSequenceNumber()
    require.Equal(t, packet.MAX_SEQUENCENUMBER-1, seq1.Val())

    seq2 := s.assignSequenceNumber()
    require.Equal(t, packet.MAX_SEQUENCENUMBER, seq2.Val())

    // Wraparound to 0
    seq3 := s.assignSequenceNumber()
    require.Equal(t, uint32(0), seq3.Val())

    seq4 := s.assignSequenceNumber()
    require.Equal(t, uint32(1), seq4.Val())
}

func TestAssignSequenceNumber_Concurrent(t *testing.T) {
    cfg := SendConfig{
        InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
        ConnectionMetrics:     metrics.NewConnectionMetrics(0),
        UseBtree:              true,
        UseSendRing:           true,
    }
    s := NewSender(cfg).(*sender)

    const goroutines = 10
    const perGoroutine = 1000

    var wg sync.WaitGroup
    seqChan := make(chan uint32, goroutines*perGoroutine)

    for i := 0; i < goroutines; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < perGoroutine; j++ {
                seq := s.assignSequenceNumber()
                seqChan <- seq.Val()
            }
        }()
    }

    wg.Wait()
    close(seqChan)

    // Collect all sequences
    seqs := make(map[uint32]bool)
    for seq := range seqChan {
        require.False(t, seqs[seq], "Duplicate sequence: %d", seq)
        seqs[seq] = true
    }

    require.Len(t, seqs, goroutines*perGoroutine, "Should have unique sequences")
}
```

### 2.4 Verification Steps

```bash
# 1. Build
go build ./...

# 2. Run sender tests with race detector
go test ./congestion/live/send/... -v -race -run "Sequence|Wraparound"

# 3. Run the new concurrent test
go test ./congestion/live/send/... -v -race -run "Concurrent"

# 4. Full test suite
go test ./... -race
```

---

## Phase 3: Extend deliverReadyPacketsEventLoop() with TransmitCount

### 3.1 Objective

Modify `deliverReadyPacketsEventLoop()` to check `TransmitCount == 0` and only send packets that haven't been transmitted yet. Packets with `TransmitCount >= 1` stay in btree for potential NAK retransmission.

**Reference:** [sender_lockfree_architecture.md Section 7.9.3](./sender_lockfree_architecture.md#793-unified-tsbpd--transmitcount-logic)

### 3.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `congestion/live/send/eventloop.go` | Lines 360-412 | Add TransmitCount check |
| `metrics/metrics.go` | Add new fields | `SendFirstTransmit`, `SendAlreadySent` |

#### congestion/live/send/eventloop.go

**Lines 360-412 - Update the btree iteration:**
```go
// Iterate from delivery point and deliver ready packets
s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
    if !iterStarted {
        iterStarted = true
        m.SendDeliveryIterStarted.Add(1)
    }

    tsbpdTime := p.Header().PktTsbpdTime
    m.SendDeliveryLastTsbpd.Store(tsbpdTime)

    if tsbpdTime > nowUs {
        // This packet not ready yet - calculate time until ready
        if delivered == 0 {
            m.SendDeliveryTsbpdNotReady.Add(1)
        }
        nextDeliveryIn = time.Duration(tsbpdTime-nowUs) * time.Microsecond
        return false // Stop iteration
    }

    // ═══════════════════════════════════════════════════════════════════
    // NEW: TransmitCount check - only send if not already transmitted
    // Reference: sender_lockfree_architecture.md Section 7.9.3
    // ═══════════════════════════════════════════════════════════════════
    if p.Header().TransmitCount == 0 {
        // First transmission - send and mark as sent
        pktLen := p.Len()
        seq := p.Header().PacketSequenceNumber.Val()

        // Log packet delivery
        if s.log != nil {
            s.log("sender:eventloop:delivery:packet", func() string {
                return fmt.Sprintf("seq=%d tsbpdTime=%d nowUs=%d transmitCount=0->1", seq, tsbpdTime, nowUs)
            })
        }

        m.CongestionSendPkt.Add(1)
        m.CongestionSendPktUnique.Add(1)
        m.CongestionSendByte.Add(uint64(pktLen))
        m.CongestionSendByteUnique.Add(uint64(pktLen))
        m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))
        m.SendRateBytesSent.Add(pktLen)
        m.SendFirstTransmit.Add(1)  // NEW metric

        s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
        s.deliver(p)

        // Mark as transmitted
        p.Header().TransmitCount = 1
        delivered++
    } else {
        // Already sent - skip (stays in btree for NAK retransmit)
        m.SendAlreadySent.Add(1)  // NEW metric

        if s.log != nil {
            seq := p.Header().PacketSequenceNumber.Val()
            s.log("sender:eventloop:delivery:skip", func() string {
                return fmt.Sprintf("seq=%d transmitCount=%d (already sent)", seq, p.Header().TransmitCount)
            })
        }
    }

    // Update delivery point to next sequence
    seq := p.Header().PacketSequenceNumber.Val()
    s.deliveryStartPoint.Store(uint64(circular.SeqAdd(seq, 1)))

    return true
})
```

#### metrics/metrics.go

**Add new atomic fields:**
```go
// In ConnectionMetrics struct, add:
SendFirstTransmit atomic.Uint64 // Packets sent with TransmitCount=0→1
SendAlreadySent   atomic.Uint64 // Packets skipped (TransmitCount>=1)
```

### 3.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `congestion/live/send/sender_delivery_table_test.go` | Add TransmitCount test cases |
| `congestion/live/send/eventloop_test.go` | Test first-send vs already-sent behavior |

**New table-driven test:**
```go
func TestDeliverReadyPackets_TransmitCount(t *testing.T) {
    testCases := []struct {
        name             string
        initialTransmit  uint32
        expectDelivered  bool
        expectMetric     string
    }{
        {
            name:            "TransmitCount=0 should deliver",
            initialTransmit: 0,
            expectDelivered: true,
            expectMetric:    "SendFirstTransmit",
        },
        {
            name:            "TransmitCount=1 should skip",
            initialTransmit: 1,
            expectDelivered: false,
            expectMetric:    "SendAlreadySent",
        },
        {
            name:            "TransmitCount=2 should skip",
            initialTransmit: 2,
            expectDelivered: false,
            expectMetric:    "SendAlreadySent",
        },
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Setup sender with btree mode
            m := metrics.NewConnectionMetrics(0)
            delivered := false
            cfg := SendConfig{
                ConnectionMetrics: m,
                UseBtree:          true,
                UseSendRing:       true,
                UseEventLoop:      true,
                OnDeliver: func(p packet.Packet) {
                    delivered = true
                },
            }
            s := NewSender(cfg).(*sender)

            // Create and insert packet
            p := packet.NewPacket(nil)
            p.Header().PktTsbpdTime = 1000  // Ready (< nowUs)
            p.Header().TransmitCount = tc.initialTransmit
            p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
            s.packetBtree.Insert(p)

            // Run delivery
            nowUs := uint64(2000)  // After TSBPD time
            s.deliverReadyPacketsEventLoop(nowUs)

            require.Equal(t, tc.expectDelivered, delivered, "Delivery mismatch")

            if tc.expectMetric == "SendFirstTransmit" {
                require.Equal(t, uint64(1), m.SendFirstTransmit.Load())
            } else {
                require.Equal(t, uint64(1), m.SendAlreadySent.Load())
            }
        })
    }
}
```

### 3.4 Verification Steps

```bash
# 1. Build
go build ./...

# 2. Run delivery tests
go test ./congestion/live/send/... -v -race -run "Deliver|TransmitCount"

# 3. Run EventLoop tests
go test ./congestion/live/send/... -v -race -run "EventLoop"

# 4. Full test suite
go test ./... -race
```

---

## Phase 4: Update NAK Handler for TransmitCount

### 4.1 Objective

Update NAK processing to increment `TransmitCount` when retransmitting, and set `RetransmittedPacketFlag` when `TransmitCount > 1`.

**Reference:** [sender_lockfree_architecture.md Section 7.9.4](./sender_lockfree_architecture.md#794-nak-retransmission-separate-path)

### 4.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `congestion/live/send/nak.go` | Lines 75-110 | Update `nakBtree()` |
| `congestion/live/send/nak.go` | Lines 190-220 | Update `nakLockedOriginal()` |
| `congestion/live/send/nak.go` | Lines 280-330 | Update `nakLockedHonorOrder()` |

#### congestion/live/send/nak.go

**Update retransmit logic in all NAK functions:**
```go
// In nakBtree(), nakLockedOriginal(), nakLockedHonorOrder():

// When retransmitting a packet:
p.Header().TransmitCount++
p.Header().RetransmittedPacketFlag = true  // Always true for NAK retransmits

// Update LastRetransmitTimeUs (existing)
p.Header().LastRetransmitTimeUs = uint64(time.Now().UnixMicro())
```

**Example for nakBtree() around line 100:**
```go
// Get packet from btree
p := s.packetBtree.Get(seq.Val())
if p == nil {
    m.InternalNakNotFound.Add(1)
    continue
}

// Increment transmit count and set retransmit flag
p.Header().TransmitCount++
p.Header().RetransmittedPacketFlag = true

// RTO suppression check (existing)
if s.rtoUs != nil && nowUs-p.Header().LastRetransmitTimeUs < oneWayDelay {
    m.InternalNakRTOSuppressed.Add(1)
    continue
}

// Update retransmit time
p.Header().LastRetransmitTimeUs = nowUs

// Retransmit
s.deliver(p)
m.SendRetransmit.Add(1)  // Track retransmits separately
```

### 4.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `congestion/live/send/nak_table_test.go` | Verify TransmitCount increments on NAK |
| `congestion/live/send/sender_coverage_test.go` | Update NAK tests |

**New test:**
```go
func TestNAK_TransmitCountIncrement(t *testing.T) {
    testCases := []struct {
        name                string
        initialTransmit     uint32
        nakCount            int
        expectedTransmit    uint32
        expectedRetransmit  bool
    }{
        {
            name:               "First NAK increments 0→1",
            initialTransmit:    0,  // Never sent (edge case - shouldn't happen in practice)
            nakCount:           1,
            expectedTransmit:   1,
            expectedRetransmit: true,
        },
        {
            name:               "NAK after first send increments 1→2",
            initialTransmit:    1,
            nakCount:           1,
            expectedTransmit:   2,
            expectedRetransmit: true,
        },
        {
            name:               "Multiple NAKs increment correctly",
            initialTransmit:    1,
            nakCount:           3,
            expectedTransmit:   4,
            expectedRetransmit: true,
        },
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Setup
            m := metrics.NewConnectionMetrics(0)
            s := setupSenderWithBtree(m)

            // Insert packet
            p := packet.NewPacket(nil)
            p.Header().PacketSequenceNumber = circular.New(100, packet.MAX_SEQUENCENUMBER)
            p.Header().TransmitCount = tc.initialTransmit
            s.packetBtree.Insert(p)

            // Send NAKs
            for i := 0; i < tc.nakCount; i++ {
                s.NAK([]circular.Number{circular.New(100, packet.MAX_SEQUENCENUMBER)})
            }

            // Verify
            resultPkt := s.packetBtree.Get(100)
            require.NotNil(t, resultPkt)
            require.Equal(t, tc.expectedTransmit, resultPkt.Header().TransmitCount)
            require.Equal(t, tc.expectedRetransmit, resultPkt.Header().RetransmittedPacketFlag)
        })
    }
}
```

### 4.4 Verification Steps

```bash
# 1. Build
go build ./...

# 2. Run NAK tests
go test ./congestion/live/send/... -v -race -run "NAK|Nak"

# 3. Full test suite
go test ./... -race
```

---

## Phase 5: Implement Control Packet Priority Pattern

### 5.1 Objective

Modify `EventLoop()` to service control packets **between every action**, minimizing ACK/NAK latency for accurate RTT measurements.

**Reference:** [sender_lockfree_architecture.md Section 7.9.1](./sender_lockfree_architecture.md#791-control-packet-priority-pattern)

### 5.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `congestion/live/send/eventloop.go` | Lines 67-115 | Restructure main loop |

#### congestion/live/send/eventloop.go

**Restructure the main loop (lines 67-115):**
```go
func (s *sender) EventLoop(ctx context.Context) {
    m := s.metrics

    // ... existing setup code ...

    // Define ordered actions for the EventLoop
    type eventLoopAction struct {
        name string
        fn   func()
    }

    actions := []eventLoopAction{
        {"drainRing", func() { s.drainRingToBtreeEventLoop() }},
        {"deliverReady", func() { s.deliverReadyPacketsEventLoop(s.nowFn()) }},
    }

    dropTicker := time.NewTicker(100 * time.Millisecond)
    defer dropTicker.Stop()

    for {
        m.SendEventLoopIterations.Add(1)

        select {
        case <-ctx.Done():
            return

        case <-dropTicker.C:
            m.SendEventLoopDropFires.Add(1)
            s.dropOldPacketsEventLoop(s.nowFn())

        default:
        }

        workDone := false

        // Execute each action, servicing control ring BEFORE each one
        for _, action := range actions {
            // ════════════════════════════════════════════════════════════
            // SERVICE CONTROL RING FIRST (minimize ACK/NAK latency)
            // ════════════════════════════════════════════════════════════
            if drained := s.processControlPacketsDelta(); drained > 0 {
                m.SendEventLoopControlDrained.Add(uint64(drained))
                workDone = true
            }

            // Then execute the action
            action.fn()
        }

        // Final control packet check after all actions
        if drained := s.processControlPacketsDelta(); drained > 0 {
            m.SendEventLoopControlDrained.Add(uint64(drained))
            workDone = true
        }

        // Adaptive backoff only if no work done
        if !workDone {
            s.adaptiveBackoff()
        }
    }
}
```

### 5.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `congestion/live/send/eventloop_test.go` | Test control packet interleaving |

**New test:**
```go
func TestEventLoop_ControlPriority(t *testing.T) {
    // This test verifies that control packets are processed between actions
    // by checking that ACKs are processed quickly even when data is flowing

    m := metrics.NewConnectionMetrics(0)
    controlProcessed := atomic.Uint64{}

    cfg := SendConfig{
        ConnectionMetrics:  m,
        UseBtree:           true,
        UseSendRing:        true,
        UseSendControlRing: true,
        UseEventLoop:       true,
        OnDeliver:          func(p packet.Packet) {},
    }
    s := NewSender(cfg).(*sender)

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    // Start EventLoop
    go s.EventLoop(ctx)

    // Push some data packets
    for i := 0; i < 100; i++ {
        p := packet.NewPacket(nil)
        s.Push(p)
    }

    // Push control packets (ACKs)
    for i := 0; i < 10; i++ {
        s.controlRing.PushACK(packet.ControlPacketACK{ACKNumber: uint32(i)})
    }

    // Wait for processing
    time.Sleep(50 * time.Millisecond)

    // Verify control packets were processed
    require.Greater(t, m.SendEventLoopControlDrained.Load(), uint64(0),
        "Control packets should be processed")
}
```

### 5.4 Verification Steps

```bash
# 1. Build
go build ./...

# 2. Run EventLoop tests
go test ./congestion/live/send/... -v -race -run "EventLoop"

# 3. Run benchmarks to verify no regression
go test ./congestion/live/send/... -bench "EventLoop" -benchtime 3s
```

---

## Phase 6: Eliminate writeQueue Channel (Connection Level)

### 6.1 Objective

Remove the `writeQueue` channel and `writeQueueReader` goroutine from the connection-level write path. This is the final step to achieve a completely lock-free and channel-free sender path.

**Reference:** [sender_lockfree_architecture.md Section 7.3](./sender_lockfree_architecture.md#73-proposed-architecture-diagram)

**Current Path (with channel):**
```
Write() → writeQueue channel → writeQueueReader goroutine → snd.Push()
```

**Proposed Path (direct):**
```
Write() → snd.PushDirect() (atomic seq assignment + ring push)
```

### 6.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `connection.go` | Line 147 | Remove `writeQueue chan packet.Packet` field |
| `connection.go` | Lines 383-387 | Remove `writeQueue` initialization |
| `connection.go` | Line 484 | Remove `writeQueueReader` goroutine start |
| `connection_io.go` | Lines 106-115 | Replace channel send with direct ring push |
| `connection_io.go` | Lines 243-263 | Remove `writeQueueReader` function |
| `congestion/live/send/push.go` | New function | Add `PushDirect(p packet.Packet)` for external use |
| `congestion/interface.go` | Sender interface | Add `PushDirect` method |

#### connection.go

**Line 147 - Remove field:**
```go
// BEFORE:
type srtConn struct {
    // ...
    writeQueue  chan packet.Packet
    // ...
}

// AFTER:
type srtConn struct {
    // writeQueue removed - using direct ring push
    // ...
}
```

**Lines 383-387 - Remove initialization:**
```go
// BEFORE:
writeQueueSize := c.config.WriteQueueSize
if writeQueueSize <= 0 {
    writeQueueSize = 1024
}
c.writeQueue = make(chan packet.Packet, writeQueueSize)

// AFTER:
// writeQueue initialization removed - packets push directly to sender ring
```

**Line 484 - Remove goroutine start:**
```go
// BEFORE:
go c.writeQueueReader(c.ctx, &c.connWg)

// AFTER:
// writeQueueReader goroutine removed - no longer needed
```

#### connection_io.go

**Lines 106-115 - Replace channel with direct push:**
```go
// BEFORE:
// Non-blocking write to the write queue.
select {
case <-c.ctx.Done():
    return 0, io.EOF
case c.writeQueue <- p:
default:
    return 0, io.EOF
}

// AFTER:
// Direct push to sender ring (lock-free, no channel)
// Sequence number assigned atomically inside PushDirect
select {
case <-c.ctx.Done():
    return 0, io.EOF
default:
    if !c.snd.PushDirect(p) {
        // Ring full - drop packet
        p.Decommission()
        c.metrics.SendRingDropped.Add(1)
        return 0, io.EOF
    }
}
```

**Lines 243-263 - Remove entire function:**
```go
// BEFORE:
// writeQueueReader reads the packets from the write queue and puts them into congestion
// control's send buffer.
func (c *srtConn) writeQueueReader(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()
    // ... function body ...
}

// AFTER:
// Function removed entirely - packets push directly to ring
```

#### congestion/live/send/push.go

**Add new method for external direct push:**
```go
// PushDirect pushes a packet directly to the lock-free ring.
// Called from connection.Write() when bypassing writeQueue channel.
// Sequence number is assigned atomically inside this function.
// Returns false if ring is full.
//
// Reference: sender_lockfree_architecture.md Section 7.8
func (s *sender) PushDirect(p packet.Packet) bool {
    if p == nil {
        return false
    }

    // Assign 31-bit sequence number atomically
    s.assignSequenceNumber(p)

    // Initialize TransmitCount (never sent)
    p.Header().TransmitCount = 0

    // Set timestamp
    p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

    // Push to lock-free ring
    if !s.packetRing.Push(p) {
        s.metrics.SendRingDropped.Add(1)
        return false
    }

    s.metrics.SendRingPushed.Add(1)
    return true
}
```

#### congestion/interface.go

**Update Sender interface:**
```go
// BEFORE:
type Sender interface {
    Push(p packet.Packet)
    // ...
}

// AFTER:
type Sender interface {
    Push(p packet.Packet)       // Legacy path (via writeQueueReader)
    PushDirect(p packet.Packet) bool  // NEW: Direct ring push (bypasses channel)
    // ...
}
```

### 6.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `connection_test.go` | Update Write() tests to not expect writeQueue |
| `connection_io_test.go` | Remove writeQueueReader tests, add PushDirect tests |
| `congestion/live/send/push_test.go` | Add PushDirect tests |

**New test for PushDirect:**
```go
func TestPushDirect_AtomicSequence(t *testing.T) {
    m := metrics.NewConnectionMetrics(0)
    cfg := SendConfig{
        InitialSequenceNumber: circular.New(100, packet.MAX_SEQUENCENUMBER),
        ConnectionMetrics:     m,
        UseBtree:              true,
        UseSendRing:           true,
    }
    s := NewSender(cfg).(*sender)

    // Push multiple packets
    for i := 0; i < 10; i++ {
        p := packet.NewPacket(nil)
        p.Header().PktTsbpdTime = uint64(1000 + i*100)

        ok := s.PushDirect(p)
        require.True(t, ok, "Push should succeed")
    }

    // Verify ring has 10 packets
    require.Equal(t, 10, s.packetRing.Len())

    // Verify sequence numbers are 100-109
    for i := 0; i < 10; i++ {
        p, ok := s.packetRing.TryPop()
        require.True(t, ok)
        require.Equal(t, uint32(100+i), p.Header().PacketSequenceNumber.Val())
        require.Equal(t, uint32(0), p.Header().TransmitCount, "Should be never sent")
    }
}

func TestPushDirect_Concurrent(t *testing.T) {
    m := metrics.NewConnectionMetrics(0)
    cfg := SendConfig{
        InitialSequenceNumber: circular.New(0, packet.MAX_SEQUENCENUMBER),
        ConnectionMetrics:     m,
        UseBtree:              true,
        UseSendRing:           true,
        SendRingSize:          4096,
        SendRingShards:        4,
    }
    s := NewSender(cfg).(*sender)

    const goroutines = 4
    const perGoroutine = 100

    var wg sync.WaitGroup
    for i := 0; i < goroutines; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < perGoroutine; j++ {
                p := packet.NewPacket(nil)
                p.Header().PktTsbpdTime = uint64(time.Now().UnixMicro())
                s.PushDirect(p)
            }
        }()
    }
    wg.Wait()

    // All packets should be in ring (no drops at this rate)
    require.Equal(t, goroutines*perGoroutine, s.packetRing.Len())
}
```

### 6.4 Verification Steps

```bash
# 1. Build to check for compile errors
go build ./...

# 2. Run connection tests
go test ./... -v -race -run "Connection|Write"

# 3. Run sender tests
go test ./congestion/live/send/... -v -race -run "PushDirect"

# 4. Run full test suite with race detector
go test ./... -race

# 5. Benchmark comparison (before/after)
go test ./congestion/live/send/... -bench "Push" -benchtime 3s
```

### 6.5 Migration Notes

**Backward Compatibility:**
- Keep `writeQueue` configuration options but mark deprecated
- If `WriteQueueSize` is set, log warning about deprecated option
- The `Push()` method remains for any internal uses

**Feature Flag (Optional):**
```go
// In connection.go, add temporary flag for gradual rollout:
UseDirectPush bool // Default: true with EventLoop mode

// In Write():
if c.config.UseDirectPush {
    c.snd.PushDirect(p)
} else {
    c.writeQueue <- p  // Legacy path
}
```

---

## Phase 7: Full Integration and Metrics

### 7.1 Objective

Add comprehensive metrics to track the new code paths and enable monitoring.

### 7.2 Files and Changes

| File | Location | Change |
|------|----------|--------|
| `metrics/metrics.go` | Add fields | New metrics |
| `metrics/handler.go` | Add exports | Prometheus exports |
| `metrics/handler_test.go` | Add tests | Test new exports |

#### metrics/metrics.go

**Add new atomic fields to ConnectionMetrics:**
```go
// Sender TransmitCount metrics
SendFirstTransmit atomic.Uint64 // Packets sent with TransmitCount=0→1
SendAlreadySent   atomic.Uint64 // Packets skipped in delivery (TransmitCount>=1)
SendRetransmit    atomic.Uint64 // NAK-triggered retransmissions

// Sequence number metrics
SendSeqAssigned   atomic.Uint64 // Total sequence numbers assigned
SendSeqWraparound atomic.Uint64 // Times sequence wrapped past MAX_SEQUENCENUMBER
```

#### metrics/handler.go

**Add Prometheus exports:**
```go
// In writeConnectionMetrics():

// TransmitCount metrics
fmt.Fprintf(w, "gosrt_send_first_transmit_total{%s} %d\n", labels, m.SendFirstTransmit.Load())
fmt.Fprintf(w, "gosrt_send_already_sent_total{%s} %d\n", labels, m.SendAlreadySent.Load())
fmt.Fprintf(w, "gosrt_send_retransmit_total{%s} %d\n", labels, m.SendRetransmit.Load())
fmt.Fprintf(w, "gosrt_send_seq_assigned_total{%s} %d\n", labels, m.SendSeqAssigned.Load())
fmt.Fprintf(w, "gosrt_send_seq_wraparound_total{%s} %d\n", labels, m.SendSeqWraparound.Load())
```

### 7.3 Unit Test Updates

| Test File | Changes Required |
|-----------|-----------------|
| `metrics/handler_test.go` | Test new Prometheus exports |

**Add test cases:**
```go
func TestPrometheusTransmitCountMetrics(t *testing.T) {
    m := NewConnectionMetrics(0xDEADBEEF)
    m.SendFirstTransmit.Store(100)
    m.SendAlreadySent.Store(10)
    m.SendRetransmit.Store(5)

    var buf bytes.Buffer
    WritePrometheusMetrics(&buf, []*ConnectionMetrics{m})

    output := buf.String()
    require.Contains(t, output, "gosrt_send_first_transmit_total")
    require.Contains(t, output, "gosrt_send_already_sent_total")
    require.Contains(t, output, "gosrt_send_retransmit_total")
}
```

### 7.4 Verification Steps

```bash
# 1. Build
go build ./...

# 2. Run metrics tests
go test ./metrics/... -v -race

# 3. Run metrics audit
make audit-metrics

# 4. Full test suite
go test ./... -race

# 5. Integration tests
make test-isolation CONFIG=Isolation-5M-FullELLockFree
```

---

## Phase 8: Integration Tests

After all code changes are complete, run the full integration test suite:

```bash
# 1. Build binaries
make build

# 2. Run unit tests
go test ./... -race -v

# 3. Run isolation tests
make test-isolation CONFIG=Isolation-5M-Control
make test-isolation CONFIG=Isolation-5M-FullELLockFree

# 4. Run parallel tests
make test-parallel CONFIG=Parallel-5M-Side-by-Side

# 5. Test flags validation
make test-flags
```

---

## Post-Implementation Checklist

- [ ] All phases complete
- [ ] All unit tests pass: `go test ./... -race`
- [ ] No linter errors: `golangci-lint run`
- [ ] Metrics audit passes: `make audit-metrics`
- [ ] Isolation tests pass at 5Mbps
- [ ] Parallel tests show no regression
- [ ] Documentation updated
- [ ] Implementation log updated

---

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| 31-bit wraparound bugs | Comprehensive table-driven tests at boundary values |
| Race conditions | Run all tests with `-race` flag |
| Performance regression | Benchmark before and after each phase |
| Metric double-counting | Use metrics audit tool to verify |
| TransmitCount not initialized | Test in packet creation path |

---

## References

- [sender_lockfree_architecture.md](./sender_lockfree_architecture.md) - Architecture design
- [lockless_sender_design.md](./lockless_sender_design.md) - Original sender lockless design
- [circular/seq_math_31bit_wraparound_test.go](../circular/seq_math_31bit_wraparound_test.go) - 31-bit wraparound tests

