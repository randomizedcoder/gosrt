# Lockless Sender Implementation Plan

## Table of Contents

- [Overview](#overview)
- [Implementation Notes (Design Review Feedback)](#implementation-notes-design-review-feedback)
  - [Note 1: Btree Implementation - USE GENERIC BTREE](#note-1-btree-implementation---use-generic-btree-btreeg)
  - [Note 2: EventLoop Shutdown Cleanup](#note-2-eventloop-shutdown-cleanup-phase-4)
  - [Note 3: Buffer Size Safety](#note-3-buffer-size-safety-phase-5)
  - [Note 4: SendBtreeLen Atomic Optimization](#note-4-sendbtreelen-atomic-optimization-phase-46)
  - [Note 5: TSBPD Sleep Factor Configuration](#note-5-tsbpd-sleep-factor-configuration)
- [Phase 1: SendPacketBtree (Foundation)](#phase-1-sendpacketbtree-foundation)
  - [Step 1.1: Create SendPacketBtree Data Structure](#step-11-create-sendpacketbtree-data-structure)
  - [Step 1.2: Add Unit Tests](#step-12-add-unit-tests-for-sendpacketbtree)
  - [Step 1.3-1.7: Config, Flags, Structs](#step-13-add-config-option-for-btree)
  - [Step 1.8: Function Dispatch Pattern](#step-18-create-function-dispatch-pattern)
  - [Step 1.9-1.12: Btree Operations](#step-19-implement-btree-push)
  - [Step 1.13-1.14: Connection & Benchmarks](#step-113-update-connectiongo-to-pass-config)
- [Phase 2: SendPacketRing (Lock-Free Data Buffer)](#phase-2-sendpacketring-lock-free-data-buffer)
  - [Step 2.1: Create SendPacketRing](#step-21-create-sendpacketring)
  - [Step 2.2-2.4: Config, Flags, Structs](#step-22-add-config-options)
  - [Step 2.5-2.6: Ring Push & Drain](#step-25-implement-ring-based-push)
  - [Step 2.7-2.8: Unit & Integration Tests](#step-27-add-sendpacketring-unit-tests)
- [Phase 3: Control Packet Ring (CRITICAL)](#phase-3-control-packet-ring-critical)
  - [Step 3.1: Create Control Packet Ring](#step-31-create-control-packet-ring)
  - [Step 3.2-3.4: Config & ACK/NAK Entry Points](#step-32-add-config-options)
- [Phase 4: Sender EventLoop](#phase-4-sender-eventloop)
  - [Step 4.1: Create EventLoop File](#step-41-create-eventloop-file)
  - [Step 4.2: TSBPD-Aware Sleep](#step-42-add-tsbpd-aware-sleep)
  - [Step 4.3-4.6: Config, Flags, Tests](#step-43-add-config-options)
- [Phase 5: Zero-Copy Payload Pool](#phase-5-zero-copy-payload-pool)
  - [Step 5.1-5.3: Buffer Export & Validation](#step-51-export-buffer-size-constant)
- [Phase 6: Metrics and Observability](#phase-6-metrics-and-observability)
  - [Step 6.1-6.3: Metrics, Prometheus, Tests](#step-61-add-metrics-to-metricsgo)
- [Phase 7: Integration Testing](#phase-7-integration-testing)
  - [Step 7.1-7.3: Test Configs & Makefile](#step-71-create-test-configuration)
- [Phase 7.5: Function Call Verification (CRITICAL)](#phase-75-function-call-verification-critical)
  - [Step 7.5.1: AST-Based Static Analyzer](#step-751-ast-based-static-analyzer)
  - [Step 7.5.2: Runtime Verification](#step-752-runtime-verification-debug-mode)
  - [Step 7.5.3: CI Integration](#step-753-ci-integration)
- [Phase 8: Migration Path](#phase-8-migration-path)
- [Summary: Implementation Order](#summary-implementation-order)
- [Appendix: File Summary](#appendix-file-summary)
- [Post-Implementation TODO: Btree Consistency](#post-implementation-todo-btree-consistency-and-performance-verification)

---

## Overview

This document provides a detailed, step-by-step implementation plan for the lock-free sender
design described in `lockless_sender_design.md`. Each step includes specific file paths, line
numbers, function signatures, and build/test checkpoints.

**Related Documents:**
- `lockless_sender_design.md` - High-level design and architecture
- `lockless_sender_implementation_tracking.md` - **Implementation progress tracking**
- `retransmission_and_nak_suppression_design.md` - RTO suppression (already implemented)
- `gosrt_lockless_design.md` - Receiver lockless patterns (reference)
- `large_file_refactoring_plan_send.md` - Sender file organization

**Current State:**
- Sender uses `container/list.List` for `packetList` and `lossList`
- All operations protected by `sync.RWMutex` (`sender.lock`)
- `Tick()` mode fires periodically, delivering packets in batches

**Target State:**
- Sender uses btree for O(log n) operations
- Lock-free ring for `Push()` operations
- Lock-free control ring for ACK/NAK routing
- EventLoop for continuous, smooth packet delivery

---

## Implementation Notes (Design Review Feedback)

### Note 1: Btree Implementation - USE GENERIC BTREE (BTreeG)

**⚠️ CRITICAL: Use `btree.BTreeG[T]` (generic), NOT `btree.BTree` (interface)!**

The receiver's `packet_store_btree.go` uses the **generic btree** which is significantly
faster than the interface-based btree because:

1. **No interface boxing/unboxing** - direct typed access
2. **No type assertions** - compiler knows all types
3. **No `Less()` interface method** - uses typed comparator function

**Reference:** `congestion/live/receive/packet_store_btree.go` lines 19-31

```go
// CORRECT: Generic btree with typed comparator (what receiver uses)
type btreePacketStore struct {
    tree *btree.BTreeG[*packetItem]  // Generic btree!
}

func NewBTreePacketStore(degree int) packetStore {
    return &btreePacketStore{
        tree: btree.NewG(degree, func(a, b *packetItem) bool {
            // Direct typed comparison - no interfaces!
            return circular.SeqLess(a.seqNum.Val(), b.seqNum.Val())
        }),
    }
}

// WRONG: Interface-based btree (avoid this!)
type SendPacketBtree struct {
    tree *btree.BTree  // Interface-based - slower!
}
```

**The sender MUST follow the same pattern as the receiver for consistency and performance.**

See **Post-Implementation TODO** section at the end of this document for benchmarking
requirements to verify performance parity with the receiver implementation.

### Note 2: EventLoop Shutdown Cleanup (Phase 4)

When the EventLoop exits on `ctx.Done()`, it MUST drain remaining packets:

```go
func (s *sender) EventLoop(ctx context.Context) {
    defer s.cleanupOnShutdown() // CRITICAL: Drain and decommission packets

    for {
        select {
        case <-ctx.Done():
            return // cleanupOnShutdown() called via defer
        // ...
        }
    }
}

// cleanupOnShutdown drains the ring and decommissions all packets.
// Called when EventLoop exits to prevent packet/buffer leaks.
func (s *sender) cleanupOnShutdown() {
    // 1. Drain any remaining packets from ring to btree
    for {
        p, ok := s.packetRing.TryPop()
        if !ok {
            break
        }
        p.Decommission() // Return to pool without inserting
    }

    // 2. Decommission all packets in btree
    for {
        p := s.packetBtree.DeleteMin()
        if p == nil {
            break
        }
        p.Decommission()
    }

    // 3. Drain control ring
    for {
        _, ok := s.controlRing.TryPop()
        if !ok {
            break
        }
    }
}
```

### Note 3: Buffer Size Safety (Phase 5)

When reusing `globalRecvBufferPool`, ensure applications respect buffer size:

```go
// buffers.go - Export the constant for application use
const MaxRecvBufferSize = 2048 // Must match pool allocation

// Validate payload size in Push() or generator
func validatePayloadSize(data []byte) error {
    if len(data) > MaxRecvBufferSize {
        return fmt.Errorf("payload size %d exceeds MaxRecvBufferSize %d",
            len(data), MaxRecvBufferSize)
    }
    return nil
}
```

### Note 4: SendBtreeLen Atomic Optimization (Phase 4/6)

Update `SendBtreeLen` **once per EventLoop iteration**, not on every insert/delete:

```go
// BAD: Update on every operation (high atomic overhead)
func (bt *SendPacketBtree) Insert(p packet.Packet) {
    // ...
    m.SendBtreeLen.Store(uint64(bt.tree.Len())) // ❌ Called per packet
}

// GOOD: Update once at end of EventLoop iteration
func (s *sender) EventLoop(ctx context.Context) {
    for {
        // ... process packets ...

        // Update btree length ONCE at end of iteration
        s.metrics.SendBtreeLen.Store(uint64(s.packetBtree.Len()))
    }
}
```

This reduces atomic operations from O(packets) to O(1) per iteration.

### Note 5: TSBPD Sleep Factor Configuration

The TSBPD sleep factor (default 0.9) is configurable:

```go
// config.go
SendTsbpdSleepFactor float64 // Range: 0.5-0.99, default: 0.9

// flags.go
flag.Float64Var(&config.SendTsbpdSleepFactor, "sendtsbpdsleepfactor", 0.9,
    "TSBPD sleep factor (0.5-0.99, lower=earlier wake, default: 0.9)")

// validation
if c.SendTsbpdSleepFactor < 0.5 || c.SendTsbpdSleepFactor > 0.99 {
    return fmt.Errorf("SendTsbpdSleepFactor must be 0.5-0.99, got %f", c.SendTsbpdSleepFactor)
}
```

---

## Table of Contents

1. [Phase 1: SendPacketBtree (Foundation)](#phase-1-sendpacketbtree-foundation)
2. [Phase 2: SendPacketRing (Lock-Free Data Buffer)](#phase-2-sendpacketring-lock-free-data-buffer)
3. [Phase 3: Control Packet Ring (CRITICAL)](#phase-3-control-packet-ring-critical)
4. [Phase 4: Sender EventLoop](#phase-4-sender-eventloop)
5. [Phase 5: Zero-Copy Payload Pool](#phase-5-zero-copy-payload-pool)
6. [Phase 6: Metrics and Observability](#phase-6-metrics-and-observability)
7. [Phase 7: Integration Testing](#phase-7-integration-testing)
8. [Phase 8: Migration Path](#phase-8-migration-path)

---

## Phase 1: SendPacketBtree (Foundation)

**Goal:** Replace `container/list.List` with btree for O(log n) operations while maintaining
current locking model. This is the lowest-risk change and provides immediate NAK lookup improvement.

### Step 1.1: Create SendPacketBtree Data Structure

**File:** `congestion/live/send/send_packet_btree.go` (NEW)

```go
//go:build go1.18

package send

import (
    "github.com/google/btree"
    "github.com/randomizedcoder/gosrt/circular"
    "github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════
// GENERIC BTREE IMPLEMENTATION (matches receiver packet_store_btree.go)
// - Uses btree.BTreeG[*sendPacketItem] (generic) - NO interfaces!
// - Typed comparator function - NO reflection!
// - See Post-Implementation TODO for consistency verification
// ═══════════════════════════════════════════════════════════════════════════

// sendPacketItem wraps a packet for btree storage.
// Stores seqNum separately for fast comparison (avoids Header() call).
type sendPacketItem struct {
    seqNum circular.Number
    packet packet.Packet
}

// SendPacketBtree provides O(log n) packet storage using generic btree.
// NOT thread-safe - caller must hold appropriate lock (or use in EventLoop).
type SendPacketBtree struct {
    tree *btree.BTreeG[*sendPacketItem]  // Generic btree - NO interfaces!
}

// NewSendPacketBtree creates a new btree with the specified degree.
// Uses typed comparator function for maximum performance.
// Reference: congestion/live/receive/packet_store_btree.go:24-31
func NewSendPacketBtree(degree int) *SendPacketBtree {
    if degree < 2 {
        degree = 32 // Default (same as receiver)
    }
    return &SendPacketBtree{
        tree: btree.NewG(degree, func(a, b *sendPacketItem) bool {
            // Direct typed comparison - no interfaces, no reflection!
            // Uses optimized SeqLess function on raw uint32 values
            // ~10% faster than circular.Number.Lt() method
            return circular.SeqLess(a.seqNum.Val(), b.seqNum.Val())
        }),
    }
}

// Insert adds a packet using single-traversal ReplaceOrInsert pattern.
// Returns (inserted bool, duplicatePacket) following receiver pattern.
// Reference: congestion/live/receive/packet_store_btree.go:54-72
// NOTE: Does NOT update SendBtreeLen metric (done once per EventLoop iteration)
func (bt *SendPacketBtree) Insert(pkt packet.Packet) (bool, packet.Packet) {
    h := pkt.Header()
    item := &sendPacketItem{
        seqNum: h.PacketSequenceNumber,
        packet: pkt,
    }

    // Single traversal - ReplaceOrInsert returns (oldItem, replaced bool)
    old, replaced := bt.tree.ReplaceOrInsert(item)

    if replaced {
        // Duplicate! Return old packet for caller to release.
        return false, old.packet
    }

    return true, nil
}

// Get retrieves a packet by sequence number. O(log n).
// Uses pivot item for lookup (same pattern as receiver).
func (bt *SendPacketBtree) Get(seq uint32) packet.Packet {
    pivot := &sendPacketItem{seqNum: circular.New(seq, packet.MAX_SEQUENCENUMBER)}
    item, found := bt.tree.Get(pivot)
    if !found {
        return nil
    }
    return item.packet
}

// Delete removes a packet by sequence number. O(log n).
// Reference: congestion/live/receive/packet_store_btree.go:121-132 (Remove)
func (bt *SendPacketBtree) Delete(seq uint32) packet.Packet {
    pivot := &sendPacketItem{seqNum: circular.New(seq, packet.MAX_SEQUENCENUMBER)}
    removed, found := bt.tree.Delete(pivot)
    if !found {
        return nil
    }
    return removed.packet
}

// DeleteMin removes and returns the packet with the lowest sequence number.
// O(log n) - no lookup needed. Used by shutdown cleanup.
func (bt *SendPacketBtree) DeleteMin() packet.Packet {
    item, found := bt.tree.DeleteMin()
    if !found {
        return nil
    }
    return item.packet
}

// Min returns the packet with the lowest sequence number without removing.
func (bt *SendPacketBtree) Min() packet.Packet {
    item, found := bt.tree.Min()
    if !found {
        return nil
    }
    return item.packet
}

// Has checks if a packet with the given sequence number exists.
// Reference: congestion/live/receive/packet_store_btree.go:194-200
func (bt *SendPacketBtree) Has(seq uint32) bool {
    pivot := &sendPacketItem{seqNum: circular.New(seq, packet.MAX_SEQUENCENUMBER)}
    return bt.tree.Has(pivot)
}

// DeleteBefore removes all packets with sequence numbers < seq.
// Uses optimized DeleteMin pattern from receiver.
// Reference: congestion/live/receive/packet_store_btree.go:144-168 (RemoveAll)
// NOTE: Does NOT update SendBtreeLen metric (done once per EventLoop iteration)
func (bt *SendPacketBtree) DeleteBefore(seq uint32) (removed int, packets []packet.Packet) {
    packets = make([]packet.Packet, 0, 16)

    for {
        // Get the minimum element
        minItem, found := bt.tree.Min()
        if !found {
            break // Tree is empty
        }

        // Check if it's before the threshold
        if !circular.SeqLess(minItem.seqNum.Val(), seq) {
            break // Stop at first non-matching (sorted order)
        }

        // Delete the minimum (O(log n), no lookup needed)
        bt.tree.DeleteMin()
        packets = append(packets, minItem.packet)
        removed++
    }

    return removed, packets
}

// IterateFrom iterates packets starting from seq (inclusive).
// Reference: congestion/live/receive/packet_store_btree.go:108-119
func (bt *SendPacketBtree) IterateFrom(seq uint32, fn func(p packet.Packet) bool) bool {
    pivot := &sendPacketItem{seqNum: circular.New(seq, packet.MAX_SEQUENCENUMBER)}
    stopped := false
    bt.tree.AscendGreaterOrEqual(pivot, func(item *sendPacketItem) bool {
        if !fn(item.packet) {
            stopped = true
            return false // Stop iteration
        }
        return true // Continue
    })
    return !stopped // Return true if completed
}

// Iterate iterates all packets in sequence order.
// Reference: congestion/live/receive/packet_store_btree.go:94-104
func (bt *SendPacketBtree) Iterate(fn func(p packet.Packet) bool) bool {
    stopped := false
    bt.tree.Ascend(func(item *sendPacketItem) bool {
        if !fn(item.packet) {
            stopped = true
            return false
        }
        return true
    })
    return !stopped
}

// Len returns the number of packets in the btree.
func (bt *SendPacketBtree) Len() int {
    return bt.tree.Len()
}

// Clear removes all packets from the btree.
// Reference: congestion/live/receive/packet_store_btree.go:206-208
func (bt *SendPacketBtree) Clear() {
    bt.tree.Clear(false) // Don't add nodes to freelist
}
```

**Performance characteristics (matching receiver):**
- `Insert()`: O(log n), ~636 ns/op, single traversal
- `Get()`: O(log n), ~300 ns/op
- `Delete()`: O(log n), ~300 ns/op
- `DeleteBefore()`: O(k log n), uses optimized DeleteMin pattern
- `IterateFrom()`: O(log n) seek + O(k) iteration

**Checkpoint:** `go build ./congestion/live/send/...`

### Step 1.2: Add Unit Tests for SendPacketBtree

**File:** `congestion/live/send/send_packet_btree_test.go` (NEW)

Reference: `congestion/live/receive/nak_btree_test.go` for test patterns.

Test cases:
- `TestSendPacketBtree_Insert_Basic`
- `TestSendPacketBtree_Insert_Duplicate` (ReplaceOrInsert behavior)
- `TestSendPacketBtree_Get_Found`
- `TestSendPacketBtree_Get_NotFound`
- `TestSendPacketBtree_Delete_Exists`
- `TestSendPacketBtree_DeleteMin_Multiple`
- `TestSendPacketBtree_DeleteBefore_Range`
- `TestSendPacketBtree_IterateFrom_Ordering`
- `TestSendPacketBtree_Wraparound` (31-bit sequence wraparound)

**Checkpoint:** `go test ./congestion/live/send/... -run SendPacketBtree`

### Step 1.3: Add Config Option for Btree

**File:** `config.go` (lines ~340-350, after existing NAK btree config)

```go
// --- Sender Lockless Configuration ---

// UseSendBtree enables btree for sender packet storage
// When enabled, replaces linked lists with O(log n) btree operations
// Default: false (use linked lists)
UseSendBtree bool

// SendBtreeDegree is the B-tree degree for sender packet storage
// Default: 32. Higher values use more memory but may reduce tree height
SendBtreeDegree int
```

**Checkpoint:** `go build ./...`

### Step 1.4: Add CLI Flags

**File:** `contrib/common/flags.go` (add after existing NAK btree flags)

```go
// Sender lockless flags
flag.BoolVar(&config.UseSendBtree, "usesendbtree", false,
    "enable btree for sender packet storage (O(log n) NAK lookup)")
flag.IntVar(&config.SendBtreeDegree, "sendbtreesize", 32,
    "btree degree for sender (default: 32)")
```

**Checkpoint:** `make test-flags`

### Step 1.5: Update sender Struct

**File:** `congestion/live/send/sender.go` (lines 37-68)

Add new fields to `sender` struct:

```go
type sender struct {
    // ... existing fields ...

    // Phase 1: Btree (replaces packetList/lossList when enabled)
    useBtree    bool
    packetBtree *SendPacketBtree  // NEW: Packets waiting to be sent
    // Note: lossList concept merged into packetBtree - all packets tracked in one structure
    // After delivery, packets stay in btree until ACK'd or dropped

    // Tracking points (replaces list iteration)
    contiguousPoint      atomic.Uint64  // Highest contiguous seq delivered (like receiver)
    deliveryStartPoint   atomic.Uint64  // Start of TSBPD delivery window
}
```

### Step 1.6: Update SendConfig

**File:** `congestion/live/send/sender.go` (lines 16-35)

```go
type SendConfig struct {
    // ... existing fields ...

    // Phase 1: Btree configuration
    UseBtree    bool
    BtreeDegree int

    // Phase 2: Ring configuration
    UseSendRing    bool
    SendRingSize   int  // Per-shard capacity (default: 1024)
    SendRingShards int  // Number of shards (default: 1 for ordering)

    // Phase 3: Control ring configuration
    UseSendControlRing    bool
    SendControlRingSize   int  // Per-shard capacity (default: 256)
    SendControlRingShards int  // Number of shards (default: 2)

    // Phase 4: EventLoop configuration
    UseSendEventLoop bool
}
```

### Step 1.7: Update NewSender

**File:** `congestion/live/send/sender.go` (lines 70-107)

In `NewSender()`:

```go
func NewSender(sendConfig SendConfig) congestion.Sender {
    s := &sender{
        // ... existing initialization ...

        // Phase 1: Btree
        useBtree: sendConfig.UseBtree,
    }

    // Initialize storage based on config
    if s.useBtree {
        degree := sendConfig.BtreeDegree
        if degree == 0 {
            degree = 32
        }
        s.packetBtree = NewSendPacketBtree(degree)
    } else {
        // Legacy: linked lists
        s.packetList = list.New()
        s.lossList = list.New()
    }

    // ... rest of initialization ...
}
```

### Step 1.8: Create Function Dispatch Pattern

**File:** `congestion/live/send/sender.go` (add after struct definition)

Following receiver pattern (`congestion/live/receive/receiver.go` lines 100-150):

```go
// Function dispatch for btree vs list operations
type (
    pushFn     func(p packet.Packet)
    nakFn      func(seqs []circular.Number) uint64
    ackFn      func(seq circular.Number)
    deliverFn  func(now uint64) int
    dropFn     func(now uint64) int
)

func (s *sender) setupFunctionDispatch() {
    if s.useBtree {
        s.pushFn = s.pushBtree
        s.nakFn = s.nakBtree
        s.ackFn = s.ackBtree
        s.deliverFn = s.deliverBtree
        s.dropFn = s.dropBtree
    } else {
        s.pushFn = s.pushList
        s.nakFn = s.nakList
        s.ackFn = s.ackList
        s.deliverFn = s.deliverList
        s.dropFn = s.dropList
    }
}
```

### Step 1.9: Implement Btree Push

**File:** `congestion/live/send/push.go` (add new function)

```go
// pushBtree inserts packet into btree (Phase 1: Btree mode)
func (s *sender) pushBtree(p packet.Packet) {
    if p == nil {
        return
    }
    m := s.metrics

    // Assign sequence number
    p.Header().PacketSequenceNumber = s.nextSequenceNumber
    p.Header().PacketPositionFlag = packet.SinglePacket
    p.Header().OrderFlag = false
    p.Header().MessageNumber = 1
    s.nextSequenceNumber = s.nextSequenceNumber.Inc()

    pktLen := p.Len()
    m.CongestionSendPktBuf.Add(1)
    m.CongestionSendByteBuf.Add(uint64(pktLen))
    m.SendRateBytes.Add(pktLen)

    p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

    // Link capacity probing (same as list mode)
    probe := p.Header().PacketSequenceNumber.Val() & 0xF
    switch probe {
    case 0:
        s.probeTime = p.Header().PktTsbpdTime
    case 1:
        p.Header().PktTsbpdTime = s.probeTime
    }

    // Insert into btree (O(log n))
    // ReplaceOrInsert handles duplicates with single traversal
    if old := s.packetBtree.Insert(p); old != nil {
        // Duplicate detected (should not happen in normal operation)
        m.SendBtreeDuplicates.Add(1)
        old.Decommission() // Return old packet to pool
    }

    m.SendBtreeInserted.Add(1)
    m.CongestionSendPktFlightSize.Store(uint64(s.packetBtree.Len()))
}

// pushList is the legacy linked list implementation (renamed from pushLocked)
func (s *sender) pushList(p packet.Packet) {
    // ... existing pushLocked code ...
}
```

### Step 1.10: Implement Btree NAK Lookup

**File:** `congestion/live/send/nak.go` (add new function after nakLockedHonorOrder)

```go
// nakBtree processes NAK using O(log n) btree lookup (Phase 1: Btree mode)
func (s *sender) nakBtree(sequenceNumbers []circular.Number) uint64 {
    m := s.metrics
    s.checkNakBeforeACK(sequenceNumbers)

    totalLossCount := metrics.CountNAKEntries(m, sequenceNumbers, metrics.NAKCounterRecv)
    totalLossBytes := totalLossCount * uint64(s.avgPayloadSize)

    m.CongestionSendPktLoss.Add(totalLossCount)
    m.CongestionSendByteLoss.Add(totalLossBytes)

    // Pre-fetch RTO values once
    nowUs := uint64(time.Now().UnixMicro())
    var oneWayThreshold uint64
    if s.rtoUs != nil {
        oneWayThreshold = s.rtoUs.Load() / 2
    }

    retransCount := uint64(0)

    // Process each range in NAK
    for i := 0; i < len(sequenceNumbers); i += 2 {
        startSeq := sequenceNumbers[i].Val()
        endSeq := sequenceNumbers[i+1].Val()

        // O(log n) lookup for each sequence in range
        for seq := startSeq; circular.SeqLessOrEqual(seq, endSeq); seq = circular.SeqAdd(seq, 1) {
            m.SendNakLookups.Add(1)

            p := s.packetBtree.Get(seq)
            if p == nil {
                m.SendNakLookupMisses.Add(1)
                continue
            }
            m.SendNakLookupHits.Add(1)

            h := p.Header()

            // RTO suppression check
            if h.LastRetransmitTimeUs > 0 && oneWayThreshold > 0 {
                if nowUs-h.LastRetransmitTimeUs < oneWayThreshold {
                    m.RetransSuppressed.Add(1)
                    continue
                }
            }

            // Retransmit
            h.LastRetransmitTimeUs = nowUs
            h.RetransmitCount++
            if h.RetransmitCount == 1 {
                m.RetransFirstTime.Add(1)
            }
            m.RetransAllowed.Add(1)

            pktLen := p.Len()
            m.CongestionSendPktRetrans.Add(1)
            m.CongestionSendPkt.Add(1)
            m.CongestionSendByteRetrans.Add(uint64(pktLen))
            m.CongestionSendByte.Add(uint64(pktLen))

            s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
            m.SendRateBytesSent.Add(pktLen)
            m.SendRateBytesRetrans.Add(pktLen)

            h.RetransmittedPacketFlag = true
            s.deliver(p)
            retransCount++
        }
    }

    if retransCount < totalLossCount {
        m.CongestionSendNAKNotFound.Add(totalLossCount - retransCount)
    }

    return retransCount
}
```

### Step 1.11: Implement Btree ACK Processing

**File:** `congestion/live/send/ack.go` (add new function)

```go
// ackBtree processes ACK using btree DeleteBefore (Phase 1: Btree mode)
func (s *sender) ackBtree(sequenceNumber circular.Number) {
    m := s.metrics

    // Track highest ACK'd sequence
    if sequenceNumber.Gt(s.lastACKedSequence) {
        s.lastACKedSequence = sequenceNumber
    }

    // Remove all packets before ACK sequence (O(k log n) where k = removed count)
    removed, packets := s.packetBtree.DeleteBefore(sequenceNumber.Val())

    m.SendAckDeleteMinCalls.Add(1)
    m.SendAckDeleteMinPackets.Add(uint64(removed))

    // Decommission removed packets
    for _, p := range packets {
        pktLen := p.Len()
        m.CongestionSendPktBuf.Add(^uint64(0))                    // Decrement
        m.CongestionSendByteBuf.Add(^uint64(uint64(pktLen) - 1))
        p.Decommission()
    }

    s.pktSndPeriod = (s.avgPayloadSize + 16) * 1000000 / s.maxBW
}
```

### Step 1.12: Implement Btree Delivery

**File:** `congestion/live/send/tick.go` (add new function)

```go
// deliverBtree delivers packets using btree iteration (Phase 1: Btree mode)
func (s *sender) deliverBtree(now uint64) int {
    m := s.metrics
    delivered := 0

    // Start from deliveryStartPoint and iterate forward
    startSeq := s.deliveryStartPoint.Load()

    s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
        if p.Header().PktTsbpdTime > now {
            return false // Stop - this and following packets not ready
        }

        pktLen := p.Len()
        m.CongestionSendPkt.Add(1)
        m.CongestionSendPktUnique.Add(1)
        m.CongestionSendByte.Add(uint64(pktLen))
        m.CongestionSendByteUnique.Add(uint64(pktLen))
        m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))

        s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
        m.SendRateBytesSent.Add(pktLen)

        s.deliver(p)
        delivered++

        // Update delivery point
        nextSeq := p.Header().PacketSequenceNumber.Val()
        s.deliveryStartPoint.Store(uint64(circular.SeqAdd(nextSeq, 1)))

        return true // Continue iteration
    })

    return delivered
}
```

### Step 1.13: Update connection.go to Pass Config

**File:** `connection.go` (lines 476-487)

```go
c.snd = send.NewSender(send.SendConfig{
    InitialSequenceNumber: c.initialPacketSequenceNumber,
    DropThreshold:         c.dropThreshold,
    MaxBW:                 c.config.MaxBW,
    InputBW:               c.config.InputBW,
    MinInputBW:            c.config.MinInputBW,
    OverheadBW:            c.config.OverheadBW,
    OnDeliver:             c.pop,
    LockTimingMetrics:     c.metrics.SenderLockTiming,
    ConnectionMetrics:     c.metrics,
    HonorNakOrder:         c.config.HonorNakOrder,
    RTOUs:                 &c.rtt.rtoUs,  // Already added
    // Phase 1: Btree
    UseBtree:              c.config.UseSendBtree,
    BtreeDegree:           c.config.SendBtreeDegree,
})
```

### Step 1.14: Add Benchmarks

**File:** `congestion/live/send/send_packet_btree_bench_test.go` (NEW)

Benchmark cases:
- `BenchmarkSendPacketBtree_Insert`
- `BenchmarkSendPacketBtree_Get`
- `BenchmarkSendPacketBtree_Delete`
- `BenchmarkSendPacketBtree_DeleteBefore`
- `BenchmarkSendPacketBtree_vs_List_NAKLookup` (compare with linked list)

**Phase 1 Checkpoint:**
```bash
go test ./congestion/live/send/... -v
go test ./congestion/live/send/... -bench=. -benchmem
make test-flags
make build-integration
```

---

## Phase 2: SendPacketRing (Lock-Free Data Buffer)

**Goal:** Add lock-free ring buffer for `Push()` operations, eliminating lock contention
on the hot path.

### Step 2.1: Create SendPacketRing

**File:** `congestion/live/send/data_ring.go` (NEW)

Reference implementation: `congestion/live/receive/ring.go` and `receiver.go:228`

**Shard Configuration:**
- Default: 1 shard (preserves strict packet ordering)
- For high write throughput: increase shards (btree sorts packets anyway)

```go
package send

import (
    "fmt"
    ring "github.com/randomizedcoder/go-lock-free-ring"
    "github.com/randomizedcoder/gosrt/packet"
)

// SendPacketRing wraps the lock-free ring for sender data packets.
// Push() writes to ring (lock-free), EventLoop drains to btree.
type SendPacketRing struct {
    ring *ring.ShardedRing
}

// NewSendPacketRing creates a send ring with configurable size and shards.
// size: per-shard capacity (will be rounded to power of 2)
// shards: number of shards (default 1 for strict ordering)
//
// Reference: congestion/live/receive/receiver.go:228
func NewSendPacketRing(size, shards int) (*SendPacketRing, error) {
    if shards < 1 {
        shards = 1 // Default: single shard for ordering
    }
    totalCapacity := uint64(size * shards)

    r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
    if err != nil {
        return nil, fmt.Errorf("failed to create send ring: %w", err)
    }

    return &SendPacketRing{ring: r}, nil
}

// TryPush attempts to push a packet to the ring without blocking.
func (r *SendPacketRing) TryPush(p packet.Packet) bool {
    return r.ring.TryWrite(p)
}

// Push pushes a packet to the ring with retries and backoff.
// Note: For sender, blocking writes should be avoided - caller should
// handle ring-full by applying backpressure to application.
func (r *SendPacketRing) Push(p packet.Packet) bool {
    return r.ring.TryWrite(p)
}

// TryPop attempts to pop a packet from the ring without blocking.
func (r *SendPacketRing) TryPop() (packet.Packet, bool) {
    return r.ring.TryRead()
}

// DrainBatch drains up to max packets from the ring.
func (r *SendPacketRing) DrainBatch(max int) []packet.Packet {
    result := make([]packet.Packet, 0, max)
    for i := 0; i < max; i++ {
        p, ok := r.ring.TryRead()
        if !ok {
            break
        }
        result = append(result, p)
    }
    return result
}
```

### Step 2.2: Add Config Options

**File:** `config.go`

```go
// UseSendRing enables lock-free ring for Push() operations
// When enabled, Push() writes to ring, Tick()/EventLoop drains to btree
// REQUIRES: UseSendBtree=true
// Default: false
UseSendRing bool

// SendRingSize is the ring capacity per shard (must be power of 2)
// Default: 1024
SendRingSize int

// SendRingShards is the number of ring shards (must be power of 2)
// Default: 1 (preserves strict ordering)
// Increase for high write throughput (btree will sort)
SendRingShards int
```

### Step 2.3: Add CLI Flags

**File:** `contrib/common/flags.go`

```go
flag.BoolVar(&config.UseSendRing, "usesendring", false,
    "enable lock-free ring for sender Push() (requires -usesendbtree)")
flag.IntVar(&config.SendRingSize, "sendringsize", 1024,
    "sender ring size per shard (power of 2)")
flag.IntVar(&config.SendRingShards, "sendringshards", 1,
    "sender ring shards (power of 2, increase for high write throughput)")
```

### Step 2.4: Update sender Struct and Initialization

**File:** `congestion/live/send/sender.go`

Add to struct:
```go
type sender struct {
    // ... existing fields ...

    // Phase 2: Lock-free ring
    useRing      bool
    packetRing   *SendPacketRing
}
```

Update `NewSender()` to initialize ring when enabled:
```go
// In NewSender() - after btree initialization:

// Phase 2: Initialize ring if enabled
if sendConfig.UseSendRing {
    if !sendConfig.UseSendBtree {
        panic("UseSendRing requires UseSendBtree")
    }

    ringSize := sendConfig.SendRingSize
    if ringSize == 0 {
        ringSize = 1024 // Default
    }

    ringShards := sendConfig.SendRingShards
    if ringShards == 0 {
        ringShards = 1 // Default: single shard for ordering
    }

    var err error
    s.packetRing, err = NewSendPacketRing(ringSize, ringShards)
    if err != nil {
        panic(fmt.Sprintf("failed to create send packet ring: %v", err))
    }
    s.useRing = true
}
```

**Note:** Default `SendRingShards=1` preserves strict packet ordering.
For high write throughput with multiple concurrent producers, increase shards
(btree will sort packets by sequence number anyway).

### Step 2.5: Implement Ring-Based Push

**File:** `congestion/live/send/push.go`

```go
// Push is the public API - dispatches to ring or direct path
func (s *sender) Push(p packet.Packet) {
    if s.useRing {
        s.pushRing(p)
        return
    }
    // Legacy path with locking
    if s.lockTiming != nil {
        metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
            s.pushFn(p)
        })
        return
    }
    s.lock.Lock()
    defer s.lock.Unlock()
    s.pushFn(p)
}

// pushRing pushes to lock-free ring (Phase 2: Ring mode)
// Sequence number assignment happens here for deterministic ordering.
func (s *sender) pushRing(p packet.Packet) {
    if p == nil {
        return
    }
    m := s.metrics

    // Assign sequence number BEFORE pushing to ring
    // This ensures sequence numbers are assigned in Push() order
    p.Header().PacketSequenceNumber = s.nextSequenceNumber
    p.Header().PacketPositionFlag = packet.SinglePacket
    p.Header().OrderFlag = false
    p.Header().MessageNumber = 1
    s.nextSequenceNumber = s.nextSequenceNumber.Inc()

    // Set timestamp
    p.Header().Timestamp = uint32(p.Header().PktTsbpdTime & uint64(packet.MAX_TIMESTAMP))

    // Link capacity probing
    probe := p.Header().PacketSequenceNumber.Val() & 0xF
    switch probe {
    case 0:
        s.probeTime = p.Header().PktTsbpdTime
    case 1:
        p.Header().PktTsbpdTime = s.probeTime
    }

    // Push to ring (lock-free)
    if !s.packetRing.Push(p) {
        m.SendRingDropped.Add(1)
        p.Decommission() // Return to pool
        return
    }

    m.SendRingPushed.Add(1)
}
```

### Step 2.6: Implement Ring Drain

**File:** `congestion/live/send/tick.go`

```go
// drainRingToBtree drains packets from ring to btree.
// Called by Tick() or EventLoop before processing.
func (s *sender) drainRingToBtree() int {
    if s.packetRing == nil {
        return 0
    }

    m := s.metrics
    drained := 0

    // Drain all available packets
    for {
        p, ok := s.packetRing.TryPop()
        if !ok {
            break
        }

        pktLen := p.Len()
        m.CongestionSendPktBuf.Add(1)
        m.CongestionSendByteBuf.Add(uint64(pktLen))
        m.SendRateBytes.Add(pktLen)

        // Insert into btree
        if old := s.packetBtree.Insert(p); old != nil {
            m.SendBtreeDuplicates.Add(1)
            old.Decommission()
        }

        m.SendBtreeInserted.Add(1)
        m.SendRingDrained.Add(1)
        drained++
    }

    if drained > 0 {
        m.CongestionSendPktFlightSize.Store(uint64(s.packetBtree.Len()))
    }

    return drained
}
```

### Step 2.7: Add SendPacketRing Unit Tests

**File:** `congestion/live/send/data_ring_test.go` (NEW)

Tests must cover both single-shard (default) and multi-shard configurations.

```go
package send

import (
    "sync"
    "testing"

    "github.com/randomizedcoder/gosrt/packet"
)

// ═══════════════════════════════════════════════════════════════════════════
// SINGLE SHARD TESTS (default configuration - strict ordering)
// ═══════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_SingleShard_Basic(t *testing.T) {
    r, err := NewSendPacketRing(64, 1) // Single shard
    if err != nil {
        t.Fatalf("failed to create ring: %v", err)
    }

    // Create test packets
    pkts := createTestPackets(t, 10)

    // Push all packets
    for _, p := range pkts {
        if !r.Push(p) {
            t.Fatalf("failed to push packet")
        }
    }

    // Pop and verify order (single shard preserves order)
    for i := 0; i < 10; i++ {
        p, ok := r.TryPop()
        if !ok {
            t.Fatalf("expected packet %d, got none", i)
        }
        if p.Header().PacketSequenceNumber.Val() != pkts[i].Header().PacketSequenceNumber.Val() {
            t.Errorf("packet %d: got seq %d, want %d",
                i, p.Header().PacketSequenceNumber.Val(),
                pkts[i].Header().PacketSequenceNumber.Val())
        }
    }
}

func TestSendPacketRing_SingleShard_Ordering(t *testing.T) {
    r, err := NewSendPacketRing(128, 1)
    if err != nil {
        t.Fatalf("failed to create ring: %v", err)
    }

    // Push 100 packets in sequence
    for i := uint32(0); i < 100; i++ {
        p := createPacketWithSeq(t, i)
        if !r.Push(p) {
            t.Fatalf("failed to push packet %d", i)
        }
    }

    // Verify strict FIFO ordering
    for i := uint32(0); i < 100; i++ {
        p, ok := r.TryPop()
        if !ok {
            t.Fatalf("expected packet at index %d", i)
        }
        if p.Header().PacketSequenceNumber.Val() != i {
            t.Errorf("ordering broken at %d: got %d", i, p.Header().PacketSequenceNumber.Val())
        }
    }
}

func TestSendPacketRing_SingleShard_DrainBatch(t *testing.T) {
    r, err := NewSendPacketRing(64, 1)
    if err != nil {
        t.Fatalf("failed to create ring: %v", err)
    }

    // Push 20 packets
    for i := uint32(0); i < 20; i++ {
        r.Push(createPacketWithSeq(t, i))
    }

    // Drain batch of 10
    batch := r.DrainBatch(10)
    if len(batch) != 10 {
        t.Errorf("got %d packets, want 10", len(batch))
    }

    // Verify order in batch
    for i, p := range batch {
        if p.Header().PacketSequenceNumber.Val() != uint32(i) {
            t.Errorf("batch[%d]: got seq %d, want %d", i, p.Header().PacketSequenceNumber.Val(), i)
        }
    }

    // Drain remaining
    batch = r.DrainBatch(20)
    if len(batch) != 10 {
        t.Errorf("got %d packets, want 10", len(batch))
    }
}

// ═══════════════════════════════════════════════════════════════════════════
// MULTI-SHARD TESTS (high-throughput configuration)
// ═══════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_MultiShard_Basic(t *testing.T) {
    r, err := NewSendPacketRing(64, 4) // 4 shards
    if err != nil {
        t.Fatalf("failed to create ring: %v", err)
    }

    // Push packets
    for i := uint32(0); i < 50; i++ {
        if !r.Push(createPacketWithSeq(t, i)) {
            t.Fatalf("failed to push packet %d", i)
        }
    }

    // Pop all - order NOT guaranteed with multiple shards
    // but all packets should be present
    seen := make(map[uint32]bool)
    for i := 0; i < 50; i++ {
        p, ok := r.TryPop()
        if !ok {
            t.Fatalf("expected packet at index %d, got none", i)
        }
        seq := p.Header().PacketSequenceNumber.Val()
        if seen[seq] {
            t.Errorf("duplicate packet seq %d", seq)
        }
        seen[seq] = true
    }

    // Verify all packets received
    for i := uint32(0); i < 50; i++ {
        if !seen[i] {
            t.Errorf("missing packet seq %d", i)
        }
    }
}

func TestSendPacketRing_MultiShard_ConcurrentPush(t *testing.T) {
    r, err := NewSendPacketRing(256, 4) // 4 shards for concurrent access
    if err != nil {
        t.Fatalf("failed to create ring: %v", err)
    }

    const numGoroutines = 4
    const packetsPerGoroutine = 100

    var wg sync.WaitGroup

    // Concurrent pushes from multiple goroutines
    for g := 0; g < numGoroutines; g++ {
        wg.Add(1)
        go func(goroutineID int) {
            defer wg.Done()
            baseSeq := uint32(goroutineID * packetsPerGoroutine)
            for i := uint32(0); i < packetsPerGoroutine; i++ {
                p := createPacketWithSeq(t, baseSeq+i)
                for !r.Push(p) {
                    // Retry if ring full (shouldn't happen with proper sizing)
                }
            }
        }(g)
    }
    wg.Wait()

    // Verify all packets arrived (order not guaranteed)
    seen := make(map[uint32]bool)
    for i := 0; i < numGoroutines*packetsPerGoroutine; i++ {
        p, ok := r.TryPop()
        if !ok {
            t.Fatalf("missing packet at count %d", i)
        }
        seq := p.Header().PacketSequenceNumber.Val()
        if seen[seq] {
            t.Errorf("duplicate packet seq %d", seq)
        }
        seen[seq] = true
    }

    if len(seen) != numGoroutines*packetsPerGoroutine {
        t.Errorf("got %d unique packets, want %d", len(seen), numGoroutines*packetsPerGoroutine)
    }
}

// ═══════════════════════════════════════════════════════════════════════════
// TABLE-DRIVEN TESTS FOR SHARD CONFIGURATIONS
// ═══════════════════════════════════════════════════════════════════════════

func TestSendPacketRing_ShardConfigurations(t *testing.T) {
    tests := []struct {
        name      string
        size      int
        shards    int
        packets   int
        wantOrder bool // true if strict FIFO expected
    }{
        {"1_shard_small", 64, 1, 50, true},
        {"1_shard_large", 1024, 1, 500, true},
        {"2_shards", 64, 2, 50, false},
        {"4_shards", 64, 4, 100, false},
        {"8_shards", 128, 8, 200, false},
        {"0_shards_defaults_to_1", 64, 0, 50, true}, // Should default to 1
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            r, err := NewSendPacketRing(tt.size, tt.shards)
            if err != nil {
                t.Fatalf("failed to create ring: %v", err)
            }

            // Push packets
            for i := uint32(0); i < uint32(tt.packets); i++ {
                if !r.Push(createPacketWithSeq(t, i)) {
                    t.Fatalf("failed to push packet %d", i)
                }
            }

            // Pop and verify
            var lastSeq uint32
            orderPreserved := true
            seen := make(map[uint32]bool)

            for i := 0; i < tt.packets; i++ {
                p, ok := r.TryPop()
                if !ok {
                    t.Fatalf("missing packet at index %d", i)
                }
                seq := p.Header().PacketSequenceNumber.Val()

                if seen[seq] {
                    t.Errorf("duplicate packet seq %d", seq)
                }
                seen[seq] = true

                if i > 0 && seq != lastSeq+1 {
                    orderPreserved = false
                }
                lastSeq = seq
            }

            if tt.wantOrder && !orderPreserved {
                t.Errorf("expected strict FIFO ordering with %d shard(s)", tt.shards)
            }

            if len(seen) != tt.packets {
                t.Errorf("got %d unique packets, want %d", len(seen), tt.packets)
            }
        })
    }
}

// ═══════════════════════════════════════════════════════════════════════════
// BENCHMARK TESTS FOR SHARD COMPARISON
// ═══════════════════════════════════════════════════════════════════════════

func BenchmarkSendPacketRing_Push_1Shard(b *testing.B) {
    benchmarkRingPush(b, 1)
}

func BenchmarkSendPacketRing_Push_4Shards(b *testing.B) {
    benchmarkRingPush(b, 4)
}

func BenchmarkSendPacketRing_Push_8Shards(b *testing.B) {
    benchmarkRingPush(b, 8)
}

func benchmarkRingPush(b *testing.B, shards int) {
    r, _ := NewSendPacketRing(8192, shards)
    pkts := make([]packet.Packet, 1000)
    for i := range pkts {
        pkts[i] = createPacketWithSeq(nil, uint32(i))
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        p := pkts[i%len(pkts)]
        r.Push(p)
        r.TryPop() // Drain to prevent full
    }
}

// ═══════════════════════════════════════════════════════════════════════════
// HELPER FUNCTIONS
// ═══════════════════════════════════════════════════════════════════════════

func createTestPackets(t *testing.T, n int) []packet.Packet {
    pkts := make([]packet.Packet, n)
    for i := range pkts {
        pkts[i] = createPacketWithSeq(t, uint32(i))
    }
    return pkts
}

func createPacketWithSeq(t *testing.T, seq uint32) packet.Packet {
    // Implementation uses mock packet or real packet with sequence
    // Similar to receiver tests
    return packet.NewTestPacketWithSeq(seq)
}
```

### Step 2.8: Add Ring Integration Tests

**File:** `contrib/integration_testing/network/configs/Parallel-Clean-20M-Base-vs-SendRing-1Shard.env`

Tests single-shard ring (default, strict ordering):

```bash
# Test configuration for SendPacketRing with 1 shard (strict ordering)
DURATION=60
BITRATE=20M
LATENCY_MS=0
LOSS_PERCENT=0

# Baseline: Traditional locking sender
BASELINE_FLAGS=""

# HighPerf: Lock-free ring with single shard
HIGHPERF_FLAGS="-usesendbtree -usesendring -sendringshards=1"

EXPECTED_PACKET_LOSS=0
EXPECTED_RECOVERY=100
```

**File:** `contrib/integration_testing/network/configs/Parallel-Clean-20M-Base-vs-SendRing-4Shards.env`

Tests multi-shard ring (high throughput):

```bash
# Test configuration for SendPacketRing with 4 shards (high throughput)
DURATION=60
BITRATE=20M
LATENCY_MS=0
LOSS_PERCENT=0

# Baseline: Traditional locking sender
BASELINE_FLAGS=""

# HighPerf: Lock-free ring with 4 shards
HIGHPERF_FLAGS="-usesendbtree -usesendring -sendringshards=4"

EXPECTED_PACKET_LOSS=0
EXPECTED_RECOVERY=100
```

**File:** `contrib/integration_testing/network/configs/Parallel-Loss-L5-20M-Base-vs-SendRing-1Shard.env`

Tests single-shard under packet loss:

```bash
# Test SendPacketRing single shard with 5% packet loss
DURATION=60
BITRATE=20M
LATENCY_MS=60
LOSS_PERCENT=5

# Baseline: Traditional locking sender
BASELINE_FLAGS=""

# HighPerf: Lock-free ring with single shard
HIGHPERF_FLAGS="-usesendbtree -usesendring -sendringshards=1"

EXPECTED_PACKET_LOSS=5
EXPECTED_RECOVERY=100
```

**Add to Makefile:**

```makefile
# Send ring integration tests (Phase 2)
test-parallel-sendring-1shard:
	sudo ./contrib/integration_testing/network/integration_testing.sh parallel-run \
		Parallel-Clean-20M-Base-vs-SendRing-1Shard

test-parallel-sendring-4shards:
	sudo ./contrib/integration_testing/network/integration_testing.sh parallel-run \
		Parallel-Clean-20M-Base-vs-SendRing-4Shards

test-parallel-sendring-loss:
	sudo ./contrib/integration_testing/network/integration_testing.sh parallel-run \
		Parallel-Loss-L5-20M-Base-vs-SendRing-1Shard

test-sendring-all: test-parallel-sendring-1shard test-parallel-sendring-4shards test-parallel-sendring-loss
```

**Phase 2 Checkpoint:**
```bash
go test ./congestion/live/send/... -v -run "Ring"
go test ./congestion/live/send/... -race -run "Ring"
go test ./congestion/live/send/... -bench "Ring"
make test-flags
make test-sendring-all  # Integration tests
```

---

## Phase 3: Control Packet Ring (CRITICAL)

**Goal:** Route ACK/NAK control packets through a lock-free ring so EventLoop is the
single consumer of the btree. **This phase is CRITICAL for lock-free sender.**

Reference: `lockless_sender_design.md` Section 7.4

### Step 3.1: Create Control Packet Ring

**File:** `congestion/live/send/control_ring.go` (NEW)

```go
package send

import (
    ring "github.com/randomizedcoder/go-lock-free-ring"
    "github.com/randomizedcoder/gosrt/circular"
)

// ControlPacketType identifies the type of control packet
type ControlPacketType uint8

const (
    ControlTypeACK ControlPacketType = iota
    ControlTypeNAK
)

// ControlPacket wraps an ACK or NAK for ring transport
type ControlPacket struct {
    Type            ControlPacketType
    ACKSequence     circular.Number   // For ACK
    NAKSequences    []circular.Number // For NAK
}

// SendControlRing wraps the lock-free ring for control packets.
type SendControlRing struct {
    ring *ring.LockFreeRing[ControlPacket]
}

func NewSendControlRing(config ring.RingConfig) *SendControlRing {
    return &SendControlRing{
        ring: ring.NewLockFreeRing[ControlPacket](config),
    }
}

func (r *SendControlRing) PushACK(seq circular.Number) bool {
    return r.ring.Push(ControlPacket{
        Type:        ControlTypeACK,
        ACKSequence: seq,
    })
}

func (r *SendControlRing) PushNAK(seqs []circular.Number) bool {
    return r.ring.Push(ControlPacket{
        Type:         ControlTypeNAK,
        NAKSequences: seqs,
    })
}

func (r *SendControlRing) TryPop() (ControlPacket, bool) {
    return r.ring.TryPop()
}
```

### Step 3.2: Add Config Options

**File:** `config.go`

```go
// UseSendControlRing enables lock-free ring for ACK/NAK routing
// When enabled, control packets are queued to EventLoop via ring
// REQUIRES: UseSendRing=true
// Default: false
UseSendControlRing bool

// SendControlRingSize is the control ring capacity per shard
// Default: 256
SendControlRingSize int

// SendControlRingShards is the number of control ring shards
// Default: 2
SendControlRingShards int
```

### Step 3.3: Update sender Struct

**File:** `congestion/live/send/sender.go`

```go
type sender struct {
    // ... existing fields ...

    // Phase 3: Control ring
    useControlRing   bool
    controlRing      *SendControlRing
}
```

### Step 3.4: Update ACK/NAK Entry Points

**File:** `congestion/live/send/ack.go`

```go
// ACK is the public API for receiving ACKs
func (s *sender) ACK(sequenceNumber circular.Number) {
    if s.useControlRing {
        // Route through control ring for EventLoop processing
        if !s.controlRing.PushACK(sequenceNumber) {
            s.metrics.SendControlRingDropsACK.Add(1)
            // Fallback: process directly with lock
            s.lock.Lock()
            s.ackFn(sequenceNumber)
            s.lock.Unlock()
        } else {
            s.metrics.SendControlRingPacketsReceived.Add(1)
        }
        return
    }
    // Legacy path
    if s.lockTiming != nil {
        metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
            s.ackFn(sequenceNumber)
        })
        return
    }
    s.lock.Lock()
    defer s.lock.Unlock()
    s.ackFn(sequenceNumber)
}
```

**File:** `congestion/live/send/nak.go`

```go
// NAK is the public API for receiving NAKs
func (s *sender) NAK(sequenceNumbers []circular.Number) uint64 {
    if len(sequenceNumbers) == 0 {
        return 0
    }

    if s.useControlRing {
        // Route through control ring for EventLoop processing
        if !s.controlRing.PushNAK(sequenceNumbers) {
            s.metrics.SendControlRingDropsNAK.Add(1)
            // Fallback: process directly with lock
            s.lock.Lock()
            result := s.nakFn(sequenceNumbers)
            s.lock.Unlock()
            return result
        }
        s.metrics.SendControlRingPacketsReceived.Add(1)
        return 0 // Actual count will be tracked when EventLoop processes
    }
    // Legacy path
    if s.lockTiming != nil {
        var result uint64
        metrics.WithWLockTiming(s.lockTiming, &s.lock, func() {
            result = s.nakFn(sequenceNumbers)
        })
        return result
    }
    s.lock.Lock()
    defer s.lock.Unlock()
    return s.nakFn(sequenceNumbers)
}
```

**Phase 3 Checkpoint:**
```bash
go test ./congestion/live/send/... -v -race
make test-flags
```

---

## Phase 4: Sender EventLoop

**Goal:** Add continuous event loop that processes BOTH data ring AND control ring,
achieving single-threaded btree access with zero locks.

Reference: `congestion/live/receive/tick.go` lines 136-300 (receiver EventLoop)

### Step 4.1: Create EventLoop File

**File:** `congestion/live/send/eventloop.go` (NEW)

```go
package send

import (
    "context"
    "time"
)

// EventLoop runs the continuous sender processing loop.
// REQUIRES: UseSendBtree, UseSendRing, UseSendControlRing all enabled.
//
// The EventLoop is the ONLY goroutine that accesses SendPacketBtree:
// 1. Drains SendPacketRing → inserts to btree
// 2. Drains SendControlRing → processes ACKs (DeleteBefore) and NAKs (Get + retransmit)
// 3. Delivers ready packets (TSBPD time reached)
// 4. Drops old packets (threshold reached)
//
// This ensures single-threaded btree access with zero locks.
// Reference: lockless_sender_design.md Section 7.1
func (s *sender) EventLoop(ctx context.Context) {
    if !s.useEventLoop {
        return
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // CRITICAL: Cleanup on shutdown (see Implementation Note 2)
    // Must drain rings and decommission all packets to prevent leaks.
    // ═══════════════════════════════════════════════════════════════════════════
    defer s.cleanupOnShutdown()

    m := s.metrics

    // Initialize adaptive backoff
    backoff := newSenderAdaptiveBackoff(
        m,
        s.backoffMinSleep,
        s.backoffMaxSleep,
        s.backoffColdStartPkts,
    )

    // Drop ticker (periodic old packet cleanup)
    dropInterval := 100 * time.Millisecond // Check every 100ms
    dropTicker := time.NewTicker(dropInterval)
    defer dropTicker.Stop()

    for {
        m.SendEventLoopIterations.Add(1)

        select {
        case <-ctx.Done():
            return // cleanupOnShutdown() called via defer

        case <-dropTicker.C:
            m.SendEventLoopDropFires.Add(1)
            s.dropOldPackets(uint64(time.Now().UnixMicro()))

        default:
            m.SendEventLoopDefaultRuns.Add(1)
        }

        // 1. Drain data ring → btree
        dataDrained := s.drainRingToBtree()
        if dataDrained > 0 {
            m.SendEventLoopDataDrained.Add(uint64(dataDrained))
        }

        // 2. Drain control ring → process ACK/NAK
        controlDrained := s.processControlPacketsDelta()
        if controlDrained > 0 {
            m.SendEventLoopControlDrained.Add(uint64(controlDrained))
        }

        // 3. Deliver ready packets (TSBPD)
        nowUs := uint64(time.Now().UnixMicro())
        delivered, nextDeliveryIn := s.deliverReadyPackets(nowUs)

        // ═══════════════════════════════════════════════════════════════════════
        // OPTIMIZATION: Update SendBtreeLen ONCE per iteration (see Note 4)
        // This minimizes atomic overhead - O(1) per iteration instead of O(packets)
        // ═══════════════════════════════════════════════════════════════════════
        m.SendBtreeLen.Store(uint64(s.packetBtree.Len()))

        // 4. TSBPD-aware sleep or adaptive backoff
        // Uses configurable tsbpdSleepFactor (see Note 5)
        sleepResult := s.calculateTsbpdSleepDuration(
            nextDeliveryIn,
            delivered,
            controlDrained,
            s.backoffMinSleep,
            s.backoffMaxSleep,
        )

        if sleepResult.Duration > 0 {
            time.Sleep(sleepResult.Duration)
            if sleepResult.Duration >= s.backoffMinSleep {
                m.SendEventLoopIdleBackoffs.Add(1)
            }
        }
    }
}

// cleanupOnShutdown drains rings and decommissions all packets on EventLoop exit.
// CRITICAL: Prevents packet/buffer leaks when connection closes.
// See Implementation Note 2 for design rationale.
func (s *sender) cleanupOnShutdown() {
    // 1. Drain any remaining packets from data ring (return to pool without inserting)
    if s.packetRing != nil {
        for {
            p, ok := s.packetRing.TryPop()
            if !ok {
                break
            }
            p.Decommission() // Return payload buffer to pool
            s.metrics.SendRingDrained.Add(1)
        }
    }

    // 2. Decommission all packets remaining in btree
    if s.packetBtree != nil {
        for {
            p := s.packetBtree.DeleteMin()
            if p == nil {
                break
            }
            p.Decommission()
        }
    }

    // 3. Drain control ring (just discard - no buffers to return)
    if s.controlRing != nil {
        for {
            _, ok := s.controlRing.TryPop()
            if !ok {
                break
            }
            s.metrics.SendControlRingPacketsProcessed.Add(1)
        }
    }

    // Final btree length update
    s.metrics.SendBtreeLen.Store(0)
}

// processControlPacketsDelta drains and processes control packets from ring.
// Returns the number of control packets processed.
func (s *sender) processControlPacketsDelta() int {
    if s.controlRing == nil {
        return 0
    }

    m := s.metrics
    processed := 0

    for {
        cp, ok := s.controlRing.TryPop()
        if !ok {
            break
        }

        m.SendControlRingPacketsProcessed.Add(1)

        switch cp.Type {
        case ControlTypeACK:
            s.ackFn(cp.ACKSequence)
            m.SendEventLoopACKsProcessed.Add(1)
        case ControlTypeNAK:
            s.nakFn(cp.NAKSequences)
            m.SendEventLoopNAKsProcessed.Add(1)
        }

        processed++
    }

    return processed
}

// deliverReadyPackets delivers packets whose TSBPD time has passed.
// Returns (delivered count, duration until next packet).
func (s *sender) deliverReadyPackets(nowUs uint64) (int, time.Duration) {
    m := s.metrics
    delivered := 0
    var nextDeliveryIn time.Duration

    startSeq := s.deliveryStartPoint.Load()

    s.packetBtree.IterateFrom(uint32(startSeq), func(p packet.Packet) bool {
        tsbpdTime := p.Header().PktTsbpdTime

        if tsbpdTime > nowUs {
            // This packet not ready yet - calculate time until ready
            nextDeliveryIn = time.Duration(tsbpdTime-nowUs) * time.Microsecond
            return false // Stop iteration
        }

        // Deliver packet
        pktLen := p.Len()
        m.CongestionSendPkt.Add(1)
        m.CongestionSendPktUnique.Add(1)
        m.CongestionSendByte.Add(uint64(pktLen))
        m.CongestionSendByteUnique.Add(uint64(pktLen))
        m.CongestionSendUsSndDuration.Add(uint64(s.pktSndPeriod))
        m.SendRateBytesSent.Add(pktLen)

        s.avgPayloadSize = 0.875*s.avgPayloadSize + 0.125*float64(pktLen)
        s.deliver(p)
        delivered++

        // Update delivery point
        nextSeq := p.Header().PacketSequenceNumber.Val()
        s.deliveryStartPoint.Store(uint64(circular.SeqAdd(nextSeq, 1)))

        return true
    })

    m.SendDeliveryPackets.Add(uint64(delivered))
    return delivered, nextDeliveryIn
}

// dropOldPackets removes packets that have exceeded the drop threshold.
func (s *sender) dropOldPackets(nowUs uint64) {
    m := s.metrics
    threshold := nowUs - s.dropThreshold

    // Iterate from beginning and drop old packets
    for {
        p := s.packetBtree.Min()
        if p == nil {
            break
        }

        if p.Header().PktTsbpdTime > threshold {
            break // Remaining packets are not old enough
        }

        // Remove and drop
        s.packetBtree.Delete(p.Header().PacketSequenceNumber.Val())

        pktLen := p.Len()
        metrics.IncrementSendDataDrop(m, metrics.DropReasonTooOldSend, uint64(pktLen))
        m.CongestionSendPktBuf.Add(^uint64(0))
        m.CongestionSendByteBuf.Add(^uint64(uint64(pktLen) - 1))

        p.Decommission()
    }
}

// UseEventLoop returns whether EventLoop mode is enabled
func (s *sender) UseEventLoop() bool {
    return s.useEventLoop
}
```

### Step 4.2: Add TSBPD-Aware Sleep

**File:** `congestion/live/send/eventloop.go` (continued)

```go
// tsbpdSleepResult contains the result of sleep duration calculation.
type tsbpdSleepResult struct {
    Duration   time.Duration
    WasTsbpd   bool // True if sleep based on next packet TSBPD
    WasEmpty   bool // True if btree was empty (used max sleep)
    ClampedMin bool // True if duration was clamped to minimum
    ClampedMax bool // True if duration was clamped to maximum
}

// calculateTsbpdSleepDuration determines optimal sleep based on TSBPD.
// Reference: lockless_sender_design.md Section 7.1 "TSBPD-Aware Sleep"
func (s *sender) calculateTsbpdSleepDuration(
    nextDeliveryIn time.Duration,
    deliveredCount int,
    controlDrained int,
    minSleep time.Duration,
    maxSleep time.Duration,
) tsbpdSleepResult {
    res := tsbpdSleepResult{
        Duration: maxSleep,
        WasEmpty: true,
    }

    m := s.metrics

    // If there was activity, don't sleep
    if deliveredCount > 0 || controlDrained > 0 {
        res.Duration = 0
        res.WasEmpty = false
        return res
    }

    // Use TSBPD time for sleep if available
    if nextDeliveryIn > 0 {
        // Sleep until 90% of next packet's TSBPD time
        calculatedSleep := time.Duration(float64(nextDeliveryIn) * 0.9)

        res.Duration = calculatedSleep
        res.WasTsbpd = true
        res.WasEmpty = false

        // Clamp to configured bounds
        if res.Duration < minSleep {
            res.Duration = minSleep
            res.ClampedMin = true
        } else if res.Duration > maxSleep {
            res.Duration = maxSleep
            res.ClampedMax = true
        }
    }

    // Update metrics
    if res.WasTsbpd {
        m.SendEventLoopTsbpdSleeps.Add(1)
        m.SendEventLoopNextDeliveryTotalUs.Add(uint64(nextDeliveryIn.Microseconds()))
    } else if res.WasEmpty {
        m.SendEventLoopEmptyBtreeSleeps.Add(1)
    }
    if res.ClampedMin {
        m.SendEventLoopSleepClampedMin.Add(1)
    }
    if res.ClampedMax {
        m.SendEventLoopSleepClampedMax.Add(1)
    }
    m.SendEventLoopSleepTotalUs.Add(uint64(res.Duration.Microseconds()))

    return res
}
```

### Step 4.3: Add Config Options

**File:** `config.go`

```go
// UseSendEventLoop enables continuous event loop for sender
// When enabled, replaces Tick() with continuous EventLoop
// REQUIRES: UseSendBtree, UseSendRing, UseSendControlRing
// Default: false
UseSendEventLoop bool

// SendEventLoopBackoffMinSleep is minimum sleep during idle periods
// Default: 100µs
SendEventLoopBackoffMinSleep time.Duration

// SendEventLoopBackoffMaxSleep is maximum sleep during idle periods
// Default: 1ms
SendEventLoopBackoffMaxSleep time.Duration

// SendEventLoopBackoffColdStartPkts is packets before adaptive backoff engages
// Default: 100
SendEventLoopBackoffColdStartPkts int
```

### Step 4.4: Add CLI Flags

**File:** `contrib/common/flags.go`

```go
flag.BoolVar(&config.UseSendEventLoop, "usesendeventloop", false,
    "enable sender EventLoop (requires -usesendbtree -usesendring -usesendcontrolring)")
flag.DurationVar(&config.SendEventLoopBackoffMinSleep, "sendeventloopbackoffminsleep", 100*time.Microsecond,
    "sender EventLoop minimum sleep during idle")
flag.DurationVar(&config.SendEventLoopBackoffMaxSleep, "sendeventloopbackoffmaxsleep", 1*time.Millisecond,
    "sender EventLoop maximum sleep during idle")
flag.IntVar(&config.SendEventLoopBackoffColdStartPkts, "sendeventloopbackoffcoldstartpkts", 100,
    "sender EventLoop cold start packets before backoff")
```

### Step 4.5: Start EventLoop in connection.go

**File:** `connection.go` (after sender creation, around line 520)

```go
// Start sender EventLoop if enabled
if c.snd.UseEventLoop() {
    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.snd.(*send.Sender).EventLoop(c.ctx)
    }()
}
```

### Step 4.6: Add Unit Tests

**File:** `congestion/live/send/eventloop_test.go` (NEW)

Test cases:
- `TestSendEventLoop_Basic_Delivery`
- `TestSendEventLoop_ACK_Processing`
- `TestSendEventLoop_NAK_Processing`
- `TestSendEventLoop_TSBPD_Sleep`
- `TestSendEventLoop_ContextCancellation`
- `TestSendEventLoop_IdleBackoff`
- `TestSendEventLoop_HighThroughput`

**File:** `congestion/live/send/eventloop_race_test.go` (NEW)

- `TestRace_SendEventLoop_DataAndControl`

**Phase 4 Checkpoint:**
```bash
go test ./congestion/live/send/... -v -race
go test ./congestion/live/send/... -run EventLoop
make test-flags
```

---

## Phase 5: Zero-Copy Payload Pool

**Goal:** Enable zero-copy buffer management, reusing `globalRecvBufferPool`.

Reference: `buffers.go`, `lockless_sender_design.md` Section 6.2

**⚠️ IMPORTANT: See Implementation Note 3 for buffer size safety!**

### Step 5.1: Export Buffer Size Constant

**File:** `buffers.go` (lines 15-45)

Ensure `MaxRecvBufferSize` is exported for applications to validate payload size:

```go
// MaxRecvBufferSize is the size of pooled buffers.
// Applications MUST ensure payloads don't exceed this size.
// Exported so applications can validate before Push().
const MaxRecvBufferSize = 2048 // bytes

var globalRecvBufferPool = sync.Pool{
    New: func() interface{} {
        buf := make([]byte, MaxRecvBufferSize)
        return &buf
    },
}

// GetRecvBuffer acquires a buffer from the pool
func GetRecvBuffer() *[]byte {
    return globalRecvBufferPool.Get().(*[]byte)
}

// PutRecvBuffer returns a buffer to the pool
func PutRecvBuffer(buf *[]byte) {
    if buf == nil {
        return
    }
    globalRecvBufferPool.Put(buf)
}
```

### Step 5.2: Add Payload Size Validation

**File:** `congestion/live/send/push.go`

Add validation to prevent buffer overflow:

```go
// validatePayloadSize checks that the payload fits in pooled buffers.
// Returns error if payload exceeds MaxRecvBufferSize.
func validatePayloadSize(dataLen int) error {
    if dataLen > srt.MaxRecvBufferSize {
        return fmt.Errorf("payload size %d exceeds MaxRecvBufferSize %d",
            dataLen, srt.MaxRecvBufferSize)
    }
    return nil
}

// In Push() or the application:
func (s *sender) Push(p packet.Packet) {
    if err := validatePayloadSize(p.Len()); err != nil {
        s.metrics.SendPayloadSizeErrors.Add(1)
        // Log and drop, or return error to application
        return
    }
    // ... rest of Push()
}
```

### Step 5.3: Update client-generator to Use Pool

**File:** `contrib/client-generator/main.go`

Update data generation to acquire buffers from pool instead of allocating:

```go
import "github.com/randomizedcoder/gosrt"

func generateData() []byte {
    buf := srt.GetRecvBuffer()
    // Ensure we don't exceed buffer size!
    dataLen := min(payloadSize, srt.MaxRecvBufferSize)
    return (*buf)[:dataLen]
}
```

**Phase 5 Checkpoint:**
```bash
go test ./congestion/live/send/... -bench=. -benchmem
# Verify no allocations in hot path
```

---

## Phase 6: Metrics and Observability

**Goal:** Add all sender lockless metrics, ensure `make audit-metrics` passes.

### Step 6.1: Add Metrics to metrics.go

**File:** `metrics/metrics.go` (after existing sender metrics, ~line 500)

```go
// ═══════════════════════════════════════════════════════════════════════════
// SENDER TICK METRICS (Baseline mode - for burst detection comparison)
// ═══════════════════════════════════════════════════════════════════════════

SendTickRuns             atomic.Uint64 // Number of Tick() invocations
SendTickDeliveredPackets atomic.Uint64 // Packets delivered in Tick mode

// ═══════════════════════════════════════════════════════════════════════════
// SENDER LOCKLESS METRICS
// ═══════════════════════════════════════════════════════════════════════════

// Data ring metrics
SendRingPushed     atomic.Uint64 // Packets pushed to ring
SendRingDropped    atomic.Uint64 // Packets dropped (ring full)
SendRingDrained    atomic.Uint64 // Packets drained to btree

// Control ring metrics
SendControlRingPacketsReceived  atomic.Uint64 // Control packets pushed to ring
SendControlRingPacketsProcessed atomic.Uint64 // Control packets consumed by EventLoop
SendControlRingDropsACK         atomic.Uint64 // ACK packets dropped (ring full)
SendControlRingDropsNAK         atomic.Uint64 // NAK packets dropped (ring full)

// Btree metrics
SendBtreeInserted   atomic.Uint64 // Packets inserted to btree
SendBtreeDuplicates atomic.Uint64 // Duplicate packets detected
SendBtreeLen        atomic.Uint64 // Current btree size

// EventLoop metrics
SendEventLoopIterations      atomic.Uint64 // Total loop iterations
SendEventLoopDropFires       atomic.Uint64 // Times drop ticker fired
SendEventLoopDefaultRuns     atomic.Uint64 // Times default case ran
SendEventLoopIdleBackoffs    atomic.Uint64 // Times idle sleep triggered
SendEventLoopDataDrained     atomic.Uint64 // Data packets drained
SendEventLoopControlDrained  atomic.Uint64 // Control packets drained
SendEventLoopACKsProcessed   atomic.Uint64 // ACKs processed
SendEventLoopNAKsProcessed   atomic.Uint64 // NAKs processed
SendDeliveryPackets          atomic.Uint64 // Packets delivered

// TSBPD sleep metrics
SendEventLoopTsbpdSleeps       atomic.Uint64 // TSBPD-aware sleeps
SendEventLoopEmptyBtreeSleeps  atomic.Uint64 // Empty btree sleeps
SendEventLoopSleepClampedMin   atomic.Uint64 // Sleep clamped to min
SendEventLoopSleepClampedMax   atomic.Uint64 // Sleep clamped to max
SendEventLoopSleepTotalUs      atomic.Uint64 // Total sleep microseconds
SendEventLoopNextDeliveryTotalUs atomic.Uint64 // Sum of next delivery times

// NAK/ACK performance
SendNakLookups        atomic.Uint64 // NAK btree lookups
SendNakLookupHits     atomic.Uint64 // Successful lookups
SendNakLookupMisses   atomic.Uint64 // Failed lookups
SendAckDeleteMinCalls atomic.Uint64 // DeleteBefore calls
SendAckDeleteMinPackets atomic.Uint64 // Packets removed
```

### Step 6.2: Add Prometheus Exports

**File:** `metrics/handler.go` (in writeConnectionMetrics)

```go
// Sender Tick metrics
writeCounterIfNonZero(b, "gosrt_send_tick_runs_total", m.SendTickRuns.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_tick_delivered_packets_total", m.SendTickDeliveredPackets.Load(), socketLabel)

// Sender lockless metrics
writeCounterIfNonZero(b, "gosrt_send_ring_pushed_total", m.SendRingPushed.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ring_dropped_total", m.SendRingDropped.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ring_drained_total", m.SendRingDrained.Load(), socketLabel)

// ... (add all metrics from Step 6.1)
```

### Step 6.3: Add Handler Tests

**File:** `metrics/handler_test.go`

Add test cases for all new sender metrics.

**Phase 6 Checkpoint:**
```bash
make audit-metrics
go test ./metrics/... -v
```

---

## Phase 7: Integration Testing

**Goal:** Verify smooth delivery and performance improvement using parallel comparison tests.

### Step 7.1: Create Test Configuration

**File:** `contrib/integration_testing/network/configs/Parallel-Clean-20M-Base-vs-SendEL.sh` (NEW)

```bash
#!/bin/bash
TEST_NAME="Parallel-Clean-20M-Base-vs-SendEL"
TEST_DURATION="90s"
DATA_RATE="20000000"
LATENCY_MS="0"
LOSS_PERCENT="0"

BASELINE_FLAGS=""
HIGHPERF_FLAGS="-usesendbtree -usesendring -usesendcontrolring -usesendeventloop"

export TEST_NAME TEST_DURATION DATA_RATE LATENCY_MS LOSS_PERCENT
export BASELINE_FLAGS HIGHPERF_FLAGS
```

### Step 7.2: Add Makefile Targets

**File:** `Makefile`

```makefile
test-parallel-sender:
	@echo "Running sender lockless comparison test..."
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendEL

test-parallel-sender-all:
	@echo "Running all sender lockless tests..."
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendBtree
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendRing
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendEL
```

### Step 7.3: Update Comparison Analysis

**File:** `contrib/integration_testing/parallel_comparison.go`

Add sender-specific metrics to comparison output, including burst detection.

**Phase 7 Checkpoint:**
```bash
make build-integration
sudo make test-parallel-sender
```

---

## Phase 7.5: Function Call Verification (CRITICAL)

**⚠️ CRITICAL: Ensuring correct function variants are called in each context!**

The lock-free design relies on a strict separation:
- **EventLoop context**: Must use NON-locking functions (lock-free)
- **Tick() context**: Must use locking wrapper functions

Calling the wrong variant causes:
- **Locking function from EventLoop**: Deadlock (EventLoop holds no locks, locking function acquires lock that may already be held)
- **Non-locking function from Tick()**: Data race (concurrent access without synchronization)

### Step 7.5.1: AST-Based Static Analyzer

**File:** `contrib/tools/verify_lockfree/main.go` (NEW)

Create a static analysis tool that parses Go AST and verifies function call patterns:

```go
package main

import (
    "fmt"
    "go/ast"
    "go/parser"
    "go/token"
    "os"
    "path/filepath"
    "strings"
)

// FunctionRule defines which functions can/cannot be called from a context
type FunctionRule struct {
    CallerPattern   string   // Function name pattern (e.g., "EventLoop", "runEventLoop")
    AllowedCalls    []string // Functions allowed to be called
    ForbiddenCalls  []string // Functions forbidden from being called
}

var rules = []FunctionRule{
    // EventLoop MUST NOT call locking functions
    {
        CallerPattern: "EventLoop",
        ForbiddenCalls: []string{
            "pushLocking",
            "nakLocking",
            "ackLocking",
            "deliverLocking",
            "dropLocking",
            "InsertLocking",
            "DeleteLocking",
            "DeleteBeforeLocking",
            "IterateLocking",
        },
    },
    {
        CallerPattern: "runEventLoop",
        ForbiddenCalls: []string{
            "pushLocking",
            "nakLocking",
            "ackLocking",
            "deliverLocking",
            "dropLocking",
            "InsertLocking",
            "DeleteLocking",
            "DeleteBeforeLocking",
            "IterateLocking",
        },
    },
    {
        CallerPattern: "drainRingToBtree",
        ForbiddenCalls: []string{
            "Lock", "RLock", "Unlock", "RUnlock",
        },
    },
    {
        CallerPattern: "deliverReadyPackets",
        ForbiddenCalls: []string{
            "Lock", "RLock", "Unlock", "RUnlock",
        },
    },
    {
        CallerPattern: "processControlPackets",
        ForbiddenCalls: []string{
            "Lock", "RLock", "Unlock", "RUnlock",
        },
    },

    // Tick() MUST use locking functions (or acquire lock itself)
    {
        CallerPattern: "tickDeliverPackets",
        AllowedCalls: []string{
            "deliverLocking", "deliver", // deliver is ok if Tick holds lock
        },
        ForbiddenCalls: []string{
            // Should not call non-locking btree functions directly
            // unless lock is held at Tick level
        },
    },
}

// Violation represents a detected rule violation
type Violation struct {
    File     string
    Line     int
    Caller   string
    Callee   string
    Rule     string
}

func main() {
    if len(os.Args) < 2 {
        fmt.Println("Usage: verify_lockfree <directory>")
        os.Exit(1)
    }

    dir := os.Args[1]
    violations := analyzeDirectory(dir)

    if len(violations) > 0 {
        fmt.Printf("❌ Found %d lock-free violations:\n\n", len(violations))
        for _, v := range violations {
            fmt.Printf("  %s:%d\n", v.File, v.Line)
            fmt.Printf("    Caller: %s\n", v.Caller)
            fmt.Printf("    Callee: %s (FORBIDDEN)\n", v.Callee)
            fmt.Printf("    Rule: %s\n\n", v.Rule)
        }
        os.Exit(1)
    }

    fmt.Println("✓ No lock-free violations detected")
}

func analyzeDirectory(dir string) []Violation {
    var violations []Violation

    filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
            return nil
        }
        if strings.HasSuffix(path, "_test.go") {
            return nil // Skip test files
        }

        v := analyzeFile(path)
        violations = append(violations, v...)
        return nil
    })

    return violations
}

func analyzeFile(filename string) []Violation {
    var violations []Violation

    fset := token.NewFileSet()
    node, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
    if err != nil {
        return nil
    }

    // Track current function context
    ast.Inspect(node, func(n ast.Node) bool {
        fn, ok := n.(*ast.FuncDecl)
        if !ok {
            return true
        }

        callerName := fn.Name.Name

        // Check if this function matches any caller pattern
        for _, rule := range rules {
            if !strings.Contains(callerName, rule.CallerPattern) {
                continue
            }

            // Inspect function body for forbidden calls
            ast.Inspect(fn.Body, func(n ast.Node) bool {
                call, ok := n.(*ast.CallExpr)
                if !ok {
                    return true
                }

                calleeName := getCalleeName(call)

                for _, forbidden := range rule.ForbiddenCalls {
                    if strings.Contains(calleeName, forbidden) {
                        violations = append(violations, Violation{
                            File:   filename,
                            Line:   fset.Position(call.Pos()).Line,
                            Caller: callerName,
                            Callee: calleeName,
                            Rule:   fmt.Sprintf("%s cannot call %s", rule.CallerPattern, forbidden),
                        })
                    }
                }

                return true
            })
        }

        return true
    })

    return violations
}

func getCalleeName(call *ast.CallExpr) string {
    switch fn := call.Fun.(type) {
    case *ast.Ident:
        return fn.Name
    case *ast.SelectorExpr:
        return fn.Sel.Name
    }
    return ""
}
```

**Makefile target:**
```makefile
verify-lockfree:
	go run ./contrib/tools/verify_lockfree ./congestion/live/send/
```

### Step 7.5.2: Runtime Verification (Debug Mode)

Add debug assertions that verify the correct context at runtime.

**File:** `congestion/live/send/debug.go` (NEW)

```go
//go:build debug

package send

import (
    "runtime"
    "strings"
    "sync/atomic"
)

// Context tracking for debug verification
var (
    inEventLoop atomic.Bool
    inTick      atomic.Bool
)

// EnterEventLoop marks entry into EventLoop context
func (s *sender) EnterEventLoop() {
    if inTick.Load() {
        panic("LOCKFREE VIOLATION: EnterEventLoop called while in Tick context")
    }
    inEventLoop.Store(true)
}

// ExitEventLoop marks exit from EventLoop context
func (s *sender) ExitEventLoop() {
    inEventLoop.Store(false)
}

// EnterTick marks entry into Tick context
func (s *sender) EnterTick() {
    if inEventLoop.Load() {
        panic("LOCKFREE VIOLATION: EnterTick called while in EventLoop context")
    }
    inTick.Store(true)
}

// ExitTick marks exit from Tick context
func (s *sender) ExitTick() {
    inTick.Store(false)
}

// AssertEventLoopContext panics if not in EventLoop
func AssertEventLoopContext() {
    if !inEventLoop.Load() {
        caller := getCallerName(2)
        panic("LOCKFREE VIOLATION: " + caller + " called outside EventLoop context")
    }
}

// AssertTickContext panics if not in Tick context
func AssertTickContext() {
    if !inTick.Load() {
        caller := getCallerName(2)
        panic("LOCKFREE VIOLATION: " + caller + " called outside Tick context")
    }
}

// AssertNoLockHeld verifies mutex is not held (for EventLoop functions)
func (s *sender) AssertNoLockHeld() {
    // Try to acquire lock - if we can, it wasn't held
    // This is a debug-only check, so the overhead is acceptable
    if s.lock.TryLock() {
        s.lock.Unlock()
    } else {
        panic("LOCKFREE VIOLATION: Lock held in EventLoop context")
    }
}

func getCallerName(skip int) string {
    pc, _, _, ok := runtime.Caller(skip)
    if !ok {
        return "unknown"
    }
    fn := runtime.FuncForPC(pc)
    if fn == nil {
        return "unknown"
    }
    name := fn.Name()
    if idx := strings.LastIndex(name, "."); idx >= 0 {
        name = name[idx+1:]
    }
    return name
}
```

**File:** `congestion/live/send/debug_stub.go` (for non-debug builds)

```go
//go:build !debug

package send

// No-op stubs for release builds
func (s *sender) EnterEventLoop()    {}
func (s *sender) ExitEventLoop()     {}
func (s *sender) EnterTick()         {}
func (s *sender) ExitTick()          {}
func AssertEventLoopContext()        {}
func AssertTickContext()             {}
func (s *sender) AssertNoLockHeld()  {}
```

**Usage in EventLoop:**
```go
func (s *sender) runEventLoop(ctx context.Context) {
    s.EnterEventLoop()
    defer s.ExitEventLoop()

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        // These functions should NOT acquire locks
        AssertEventLoopContext()
        s.drainRingToBtree()
        s.processControlPackets()
        s.deliverReadyPackets()
        // ...
    }
}
```

**Usage in Tick:**
```go
func (s *sender) Tick(now uint64) {
    s.EnterTick()
    defer s.ExitTick()

    s.lock.Lock()
    defer s.lock.Unlock()

    AssertTickContext()
    // ... tick operations using locking functions
}
```

**Build with debug assertions:**
```bash
go build -tags debug ./congestion/live/send/...
go test -tags debug ./congestion/live/send/... -v
```

### Step 7.5.3: CI Integration

**File:** `.github/workflows/lockfree-verify.yml` (or add to existing CI)

```yaml
name: Lock-Free Verification

on: [push, pull_request]

jobs:
  verify-lockfree:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.21'

      - name: Run Lock-Free Verification
        run: |
          go run ./contrib/tools/verify_lockfree ./congestion/live/send/
          go run ./contrib/tools/verify_lockfree ./congestion/live/receive/

      - name: Run Debug Build Tests
        run: |
          go test -tags debug ./congestion/live/send/... -v -race
          go test -tags debug ./congestion/live/receive/... -v -race
```

**Add to Makefile:**
```makefile
# Lock-free verification
verify-lockfree:
	@echo "=== Verifying lock-free function calls ==="
	go run ./contrib/tools/verify_lockfree ./congestion/live/send/
	go run ./contrib/tools/verify_lockfree ./congestion/live/receive/
	@echo "✓ Lock-free verification passed"

# Debug build with runtime assertions
test-debug:
	go test -tags debug ./congestion/live/send/... -v
	go test -tags debug ./congestion/live/receive/... -v

# Full verification suite
verify-all: verify-lockfree test-debug
	@echo "✓ All verifications passed"
```

### Step 7.5.4: Function Naming Convention Checklist

**⚠️ VERIFY these naming conventions are followed:**

| Context | Function Pattern | Example | Acquires Lock? |
|---------|-----------------|---------|----------------|
| EventLoop | `funcName` (no suffix) | `push`, `nak`, `deliver` | NO |
| Tick | `funcNameLocking` | `pushLocking`, `nakLocking` | YES (wrapper) |
| Btree | `Insert`, `Delete`, etc. | `packetBtree.Insert(p)` | NO |
| Btree | `InsertLocking`, etc. | `packetBtree.InsertLocking(p)` | YES |

**Manual Verification Checklist:**

- [ ] `eventloop.go` - ONLY calls non-locking functions
- [ ] `tick.go` - ONLY calls locking wrapper functions (or acquires lock first)
- [ ] `push.go` - `pushRing()` is lock-free, `pushLocking()` acquires lock
- [ ] `nak.go` - `nakFromRing()` is lock-free, `nakLocking()` acquires lock
- [ ] `ack.go` - `ackFromRing()` is lock-free, `ackLocking()` acquires lock
- [ ] No `Lock()`/`Unlock()` calls inside EventLoop functions
- [ ] All btree access in EventLoop uses non-locking methods

**Phase 7.5 Checkpoint:**
```bash
make verify-lockfree
make test-debug
go test ./congestion/live/send/... -race
```

---

## Phase 8: Migration Path

See `lockless_sender_design.md` Section 8 for detailed migration documentation.

**Feature Flag Hierarchy:**
```
Level 0: Default (all disabled)
Level 1: UseSendBtree = true
Level 2: UseSendRing = true (requires Level 1)
Level 3: UseSendControlRing = true (requires Level 2)
Level 4: UseSendEventLoop = true (requires Level 2 + Level 3)
```

**Validation Commands:**
```bash
# Level 1: Btree only
go test ./congestion/live/send/... -v

# Level 2: Ring
make test-flags

# Level 3: Control ring
go test -race ./...

# Level 4: Full EventLoop
sudo make test-parallel-sender
```

---

## Summary: Implementation Order

| Phase | Goal | Risk | Duration |
|-------|------|------|----------|
| 1 | SendPacketBtree | Low | 1-2 days |
| 2 | SendPacketRing | Medium | 1-2 days |
| 3 | Control Ring | Medium | 1 day |
| 4 | EventLoop | High | 2-3 days |
| 5 | Zero-Copy Pool | Low | 0.5 day |
| 6 | Metrics | Low | 1 day |
| 7 | Integration Tests | Low | 1-2 days |
| 8 | Migration Path | Low | 1 day |

**Total Estimated Time:** 8-14 days

---

## Appendix: File Summary

| File | Status | Purpose |
|------|--------|---------|
| `send/send_packet_btree.go` | NEW | Btree data structure |
| `send/send_packet_btree_test.go` | NEW | Btree unit tests |
| `send/data_ring.go` | NEW | Data packet ring |
| `send/control_ring.go` | NEW | Control packet ring |
| `send/eventloop.go` | NEW | Sender EventLoop |
| `send/eventloop_test.go` | NEW | EventLoop tests |
| `send/sender.go` | MODIFY | Add btree/ring/eventloop fields |
| `send/push.go` | MODIFY | Add ring-based push |
| `send/nak.go` | MODIFY | Add btree NAK lookup |
| `send/ack.go` | MODIFY | Add btree ACK processing |
| `send/tick.go` | MODIFY | Add btree delivery, ring drain |
| `config.go` | MODIFY | Add sender lockless config |
| `contrib/common/flags.go` | MODIFY | Add CLI flags |
| `connection.go` | MODIFY | Pass config, start EventLoop |
| `metrics/metrics.go` | MODIFY | Add sender lockless metrics |
| `metrics/handler.go` | MODIFY | Add Prometheus exports |
| `metrics/handler_test.go` | MODIFY | Add metric tests |

---

## Post-Implementation TODO: Btree Consistency and Performance Verification

**⚠️ CRITICAL: Complete these tasks AFTER Phase 1 implementation!**

The sender `SendPacketBtree` MUST be consistent with the receiver `btreePacketStore`
(`congestion/live/receive/packet_store_btree.go`) for maintainability and performance.

### TODO 1: Verify Generic Btree Usage

- [ ] Confirm `SendPacketBtree` uses `btree.BTreeG[*sendPacketItem]` (generic)
- [ ] Confirm it does NOT use `btree.BTree` (interface-based)
- [ ] Confirm comparator function uses `circular.SeqLess()` directly

### TODO 2: API Consistency Check

Compare APIs between sender and receiver btrees:

| Operation | Receiver (`btreePacketStore`) | Sender (`SendPacketBtree`) | Consistent? |
|-----------|------------------------------|---------------------------|-------------|
| Insert | `Insert(pkt) (bool, Packet)` | `Insert(pkt) Packet` | ⬜ Verify |
| Get | N/A (uses Has) | `Get(seq) Packet` | ⬜ Verify |
| Delete | `Remove(seq) Packet` | `Delete(seq) Packet` | ⬜ Verify |
| DeleteMin | `tree.DeleteMin()` | `DeleteMin() Packet` | ⬜ Verify |
| DeleteBefore | `RemoveAll(pred, fn)` | `DeleteBefore(seq)` | ⬜ Verify |
| Iterate | `Iterate(fn) bool` | `Iterate(fn)` | ⬜ Verify |
| IterateFrom | `IterateFrom(seq, fn)` | `IterateFrom(seq, fn)` | ⬜ Verify |
| Has | `Has(seq) bool` | N/A | ⬜ Add if needed |
| Len | `Len() int` | `Len() int` | ⬜ Verify |
| Min | `Min() Packet` | `Min() Packet` | ⬜ Verify |

### TODO 3: Benchmarking

Create benchmarks that compare sender vs receiver btree performance:

**File:** `congestion/live/send/send_packet_btree_bench_test.go`

```go
// Benchmarks to implement:
func BenchmarkSendBtree_Insert(b *testing.B)           // Compare with receiver
func BenchmarkSendBtree_Get(b *testing.B)              // Sender-specific
func BenchmarkSendBtree_Delete(b *testing.B)           // Compare with receiver Remove
func BenchmarkSendBtree_DeleteBefore(b *testing.B)     // Compare with receiver RemoveAll
func BenchmarkSendBtree_IterateFrom(b *testing.B)      // Compare with receiver

// Cross-comparison benchmark
func BenchmarkBtree_SenderVsReceiver_Insert(b *testing.B) {
    // Run same workload on both implementations
    // Expected: within 10% of each other
}
```

**Performance Targets:**
- Insert: ≤ 700 ns/op (receiver: ~636 ns/op)
- Get/Delete: ≤ 400 ns/op
- IterateFrom: ≤ 100 ns/op per element
- Memory: Same allocation count as receiver

### TODO 4: Duplicate Packet Handling

Verify duplicate packet handling matches receiver pattern:

```go
// Receiver pattern (packet_store_btree.go:54-72):
// - Uses ReplaceOrInsert for single-traversal
// - Returns old packet for caller to release
// - New packet stays in tree (same seq#/data anyway)

// Sender MUST follow same pattern to avoid:
// - Double traversal on duplicates
// - Memory leaks from unreleased packets
```

### TODO 5: Document Differences

If any intentional differences exist between sender and receiver btrees, document them:

| Difference | Reason | Performance Impact |
|------------|--------|-------------------|
| (to be filled after implementation) | | |

### Completion Criteria

- [ ] All TODO items checked
- [ ] Benchmarks show sender within 10% of receiver performance
- [ ] No type assertions or type switches in hot paths
- [ ] Generic btree (`BTreeG`) used, not interface btree
- [ ] Code review confirms consistency with receiver

