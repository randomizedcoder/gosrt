# Lockless Sender Design

> **Document Purpose:** Design for a lock-free sender implementation using an event loop pattern, following the successful receiver lockless architecture.
> **Related Documents:**
> - [`retransmission_and_nak_suppression_design.md`](./retransmission_and_nak_suppression_design.md) - Section 3.4 (current sender architecture)
> - [`gosrt_lockless_design.md`](./gosrt_lockless_design.md) - Receiver lockless patterns
> - [`ack_optimization_plan.md`](./ack_optimization_plan.md) - Scan window visualization
> - [`IO_Uring_read_path.md`](./IO_Uring_read_path.md) - io_uring integration patterns
> - [`zero_copy_opportunities.md`](./zero_copy_opportunities.md) - Buffer pooling
> - [`packet_pooling_optimization.md`](./packet_pooling_optimization.md) - Packet lifecycle

> **Status:** 📋 DESIGN PHASE

---

## Table of Contents

1. [Motivation](#1-motivation)
2. [Current Architecture Problems](#2-current-architecture-problems)
3. [High-Level Design](#3-high-level-design)
4. [Detailed Design](#4-detailed-design)
5. [SendPacketBtree Design](#5-sendpacketbtree-design)
6. [Zero-Copy Buffer Management](#6-zero-copy-buffer-management)
7. [Event Loop Architecture](#7-event-loop-architecture)
8. [NAK Processing Optimization](#8-nak-processing-optimization)
9. [ACK Processing Optimization](#9-ack-processing-optimization)
10. [Configuration Options](#10-configuration-options)
11. [Metrics](#11-metrics)
12. [Implementation Phases](#12-implementation-phases)
13. [Testing Strategy](#13-testing-strategy)
14. [Migration Path](#14-migration-path)

---

## 1. Motivation

### 1.1 The Burst Problem

The current sender uses a Tick()-based architecture that introduces packet bursting:

```
Current Behavior:

Application Pushes:    ─●─────●─────●─────●─────●─────●─────●─────●─────●─────●──►
                        (evenly spaced packets from application)

Tick() runs:           ─────────────────────────────────────────────────|─────────►
                                                                        │
Network Output:        ─────────────────────────────────────────────●●●●●●●●●●────►
                                                                    (BURST!)

Problem: Packets arrive with natural spacing, but Tick() batches them into a burst.
         This burst overwhelms network queues, causing MORE loss than necessary.
```

### 1.2 Benefits of Lockless Design

Following the successful receiver lockless implementation (`gosrt_lockless_design.md`):

1. **Smooth packet delivery** - Packets sent at their TSBPD time, not batched
2. **Lower latency** - No waiting for Tick() interval (default 10ms)
3. **Better network utilization** - Reduces burst-induced congestion
4. **Reduced lock contention** - Single-threaded event loop for btree operations
5. **Efficient NAK lookup** - O(log n) btree vs O(n) linked list
6. **Simplified packet lifecycle** - Single btree instead of packetList + lossList

### 1.3 Reference: Receiver Lockless Success

The receiver lockless design achieved:
- **50-70% reduction** in redundant retransmissions (via RTO suppression)
- **Zero lock contention** in the hot path
- **Continuous packet processing** instead of batched Tick()

See `retransmission_and_nak_suppression_design.md` Section 3.3 for the receiver pattern.

---

## 2. Current Architecture Problems

### 2.1 Current Sender Data Structures

From `congestion/live/send/sender.go:38-68`:

```go
type sender struct {
    nextSequenceNumber circular.Number
    lastACKedSequence  circular.Number
    dropThreshold      uint64

    packetList *list.List    // Packets waiting to be sent
    lossList   *list.List    // Packets sent, waiting for ACK
    lock       sync.RWMutex
    // ...
}
```

**Problems:**
1. **Two linked lists** - Packets move between them (inefficient)
2. **O(n) NAK lookup** - Linear scan in `nakLockedHonorOrder()`
3. **Mutex contention** - Push() and Tick() compete for lock
4. **Bursty delivery** - All ready packets sent in single Tick()

### 2.2 Current Packet Lifecycle

From `retransmission_and_nak_suppression_design.md` Section 3.4.1:

```
Application Data
      │
      │ Push(p) [LOCK]
      ▼
┌─────────────┐
│ packetList  │  ← Packets waiting for TSBPD time
└──────┬──────┘
       │
       │ tickDeliverPackets() [LOCK, moves all ready packets]
       │ PROBLEM: All packets sent in burst!
       ▼
┌─────────────┐
│  lossList   │  ← Packets waiting for ACK
└──────┬──────┘
       │
       │ ACK received or drop threshold
       ▼
┌─────────────┐
│ Decommission│
└─────────────┘
```

### 2.3 Inefficient NAK Lookup

From `congestion/live/send/nak.go`:

```go
// nakLockedHonorOrder - O(n) linear scan!
func (s *sender) nakLockedHonorOrder(list []circular.Number) int {
    for _, seq := range list {
        // Search lossList for each NAK'd sequence
        for e := s.lossList.Front(); e != nil; e = e.Next() {  // O(n) per NAK!
            if p.Header().PacketSequenceNumber == seq {
                // Found - retransmit
            }
        }
    }
}
```

With 10,000 packets in flight and 100 NAK entries, this is **1,000,000 comparisons!**

---

## 3. High-Level Design

### 3.1 New Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     LOCKLESS SENDER ARCHITECTURE                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Application                                                                │
│      │                                                                      │
│      │ Push(p) - lock-free, atomic                                          │
│      ▼                                                                      │
│  ┌───────────────────┐                                                      │
│  │  SendPacketRing   │  ← Lock-free MPSC ring (single shard for ordering)   │
│  │  (lock-free)      │    Multiple producers, single consumer               │
│  └─────────┬─────────┘                                                      │
│            │                                                                │
│            │ EventLoop drains ring                                          │
│            ▼                                                                │
│  ┌───────────────────┐                                                      │
│  │ SendPacketBtree   │  ← Single ordered btree (replaces both lists)        │
│  │ (single-threaded) │    O(log n) lookup for NAK, ACK, delivery            │
│  └─────────┬─────────┘                                                      │
│            │                                                                │
│            │ Tracking Points:                                               │
│            │   - ACKPoint (deleteMin cutoff)                                │
│            │   - DeliveryStartPoint (scan start for TSBPD delivery)         │
│            │                                                                │
│            ├───────────────┬────────────────┬──────────────────┐            │
│            │               │                │                  │            │
│            ▼               ▼                ▼                  ▼            │
│       DeliverPackets   ProcessACK      ProcessNAK         DropOld           │
│       (when TSBPD      (deleteMin)    (O(log n) lookup)  (drop threshold)   │
│        time arrives)                                                        │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Key Components

| Component | Purpose | Lock-Free? |
|-----------|---------|------------|
| `SendPacketRing` | Buffer incoming Push() calls | Yes (MPSC) |
| `SendPacketBtree` | Ordered packet storage | Single-threaded (no lock needed) |
| `SenderEventLoop` | Process ring, deliver packets, handle timeouts | Single goroutine |
| `DeliveryStartPoint` | Track TSBPD scan position | Atomic or EventLoop-only |
| `ACKPoint` | Track highest ACK'd sequence | Atomic |

### 3.3 Data Flow Comparison

**Current (Locked):**
```
Push() ──[LOCK]──► packetList ──[LOCK/TICK]──► lossList ──[LOCK]──► deliver()
                                    │
                                    └──[LOCK]──► NAK lookup (O(n))
```

**New (Lockless):**
```
Push() ──[atomic]──► SendPacketRing ──[EventLoop]──► SendPacketBtree
                                                          │
                                     ┌────────────────────┼────────────────────┐
                                     ▼                    ▼                    ▼
                               deliver() (smooth)   NAK lookup (O(log n))   ACK deleteMin
```

---

## 4. Detailed Design

### 4.1 SendPacketRing

Following the receiver pattern (`congestion/live/receive/ring.go`), but with **single shard** to preserve packet ordering.

**File:** `congestion/live/send/ring.go` (new)

```go
package send

import (
    ring "github.com/randomizedcoder/go-lock-free-ring"
    "github.com/randomizedcoder/gosrt/packet"
)

// SendPacketRing is a lock-free MPSC ring for incoming packets.
// Uses single shard to preserve application packet ordering.
type SendPacketRing struct {
    ring *ring.MultiShardLockFreeRing[packet.Packet]
}

// NewSendPacketRing creates a ring with single shard for ordering.
func NewSendPacketRing(size int) *SendPacketRing {
    return &SendPacketRing{
        ring: ring.NewMultiShardLockFreeRing[packet.Packet](
            size,  // Per-shard capacity (power of 2)
            1,     // Single shard - preserves order!
            ring.WithMaxRetries[packet.Packet](3),
            ring.WithBackoffDuration[packet.Packet](100*time.Microsecond),
        ),
    }
}

// Push adds a packet to the ring (lock-free).
// Returns false if ring is full.
func (r *SendPacketRing) Push(p packet.Packet) bool {
    return r.ring.Push(p)
}

// TryPop retrieves a packet from the ring (single consumer).
// Returns nil if ring is empty.
func (r *SendPacketRing) TryPop() packet.Packet {
    if p, ok := r.ring.TryPop(); ok {
        return p
    }
    return nil
}

// DrainBatch retrieves up to n packets from the ring.
func (r *SendPacketRing) DrainBatch(n int) []packet.Packet {
    result := make([]packet.Packet, 0, n)
    for i := 0; i < n; i++ {
        p := r.TryPop()
        if p == nil {
            break
        }
        result = append(result, p)
    }
    return result
}
```

### 4.2 Push() Function Redesign

**File:** `congestion/live/send/push.go` (new)

```go
package send

import (
    "github.com/randomizedcoder/gosrt/packet"
)

// push is the lock-free version (EventLoop mode).
// Writes to ring buffer, sequence number assigned in EventLoop.
func (s *sender) push(p packet.Packet) {
    if p == nil {
        return
    }

    // Assign sequence number atomically
    // Note: In EventLoop mode, this could be done in EventLoop for full single-threading
    seq := s.nextSequenceNumber.Load()
    s.nextSequenceNumber.Inc()
    p.Header().PacketSequenceNumber = seq

    // Update metrics (atomic)
    pktLen := p.Len()
    s.metrics.CongestionSendPktBuf.Add(1)
    s.metrics.CongestionSendByteBuf.Add(uint64(pktLen))
    s.metrics.SendRateBytes.Add(pktLen)

    // Push to ring (lock-free)
    if !s.sendPacketRing.Push(p) {
        // Ring full - metric and potential backpressure
        s.metrics.SendRingDropped.Add(1)
        // Return packet to pool
        p.Decommission()
        return
    }
    s.metrics.SendRingPushed.Add(1)
}

// pushLocking is the legacy version (Tick mode).
// Acquires lock and calls pushLocked().
func (s *sender) pushLocking(p packet.Packet) {
    s.lock.Lock()
    defer s.lock.Unlock()
    s.pushLocked(p)
}

// pushLocked is the original implementation (unchanged).
// Used by Tick mode and as reference.
func (s *sender) pushLocked(p packet.Packet) {
    // ... existing implementation ...
}
```

### 4.3 Function Dispatch Pattern

Following the receiver pattern (`congestion/live/receive/receiver.go:234-237`):

**File:** `congestion/live/send/sender.go`

```go
type sender struct {
    // ... existing fields ...

    // Function dispatch (configured at creation)
    pushFn func(p packet.Packet)  // push or pushLocking

    // New lock-free components
    sendPacketRing   *SendPacketRing
    sendPacketBtree  *SendPacketBtree
    useEventLoop     bool
    useSendRing      bool

    // Tracking points (for EventLoop mode)
    deliveryStartSeq atomic.Uint32  // Sequence to start TSBPD scan
}

func NewSender(sendConfig SendConfig) congestion.Sender {
    s := &sender{
        // ... existing initialization ...
    }

    // Function dispatch based on configuration
    if sendConfig.UseSendRing {
        s.sendPacketRing = NewSendPacketRing(sendConfig.SendRingSize)
        s.sendPacketBtree = NewSendPacketBtree(sendConfig.BtreeDegree)
        s.pushFn = s.push  // Lock-free path
        s.useSendRing = true
    } else {
        s.pushFn = s.pushLocking  // Legacy locked path
        s.useSendRing = false
    }

    return s
}

// Push dispatches to configured implementation.
func (s *sender) Push(p packet.Packet) {
    s.pushFn(p)
}
```

---

## 5. SendPacketBtree Design

### 5.1 Data Structure

Replaces both `packetList` and `lossList` with a single ordered btree.

**File:** `congestion/live/send/send_packet_btree.go` (new)

```go
package send

import (
    "sync"
    "github.com/tidwall/btree"
    "github.com/randomizedcoder/gosrt/packet"
)

// SendPacketBtree stores packets ordered by sequence number.
// In EventLoop mode: single-threaded, no lock needed.
// In Tick mode: uses mutex for concurrent access.
type SendPacketBtree struct {
    tree *btree.BTreeG[packet.Packet]
    mu   sync.RWMutex  // Only used in Tick mode
}

// packetLess compares packets by sequence number.
func packetLess(a, b packet.Packet) bool {
    return a.Header().PacketSequenceNumber.Val() < b.Header().PacketSequenceNumber.Val()
}

// NewSendPacketBtree creates a new packet btree.
func NewSendPacketBtree(degree int) *SendPacketBtree {
    if degree < 2 {
        degree = 32  // Default degree
    }
    return &SendPacketBtree{
        tree: btree.NewBTreeG[packet.Packet](packetLess),
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Lock-Free Operations (for EventLoop mode)
// ─────────────────────────────────────────────────────────────────────────────

// Insert adds a packet to the btree.
// EventLoop mode: called from single goroutine, no lock needed.
func (bt *SendPacketBtree) Insert(p packet.Packet) {
    bt.tree.Set(p)
}

// Get retrieves a packet by sequence number (for NAK lookup).
// Returns nil if not found. O(log n).
func (bt *SendPacketBtree) Get(seq uint32) packet.Packet {
    // Create a lookup key
    p, found := bt.tree.Get(makeSeqLookupPacket(seq))
    if !found {
        return nil
    }
    return p
}

// DeleteMin removes and returns the minimum (oldest) packet.
// Used for ACK processing.
func (bt *SendPacketBtree) DeleteMin() (packet.Packet, bool) {
    return bt.tree.PopMin()
}

// DeleteBefore removes all packets with seq < cutoff.
// Returns count of deleted packets. Used for ACK processing.
func (bt *SendPacketBtree) DeleteBefore(cutoff uint32) int {
    count := 0
    for {
        p, ok := bt.tree.Min()
        if !ok {
            break
        }
        if p.Header().PacketSequenceNumber.Val() >= cutoff {
            break
        }
        bt.tree.PopMin()
        p.Decommission()  // Return to pool
        count++
    }
    return count
}

// Min returns the minimum packet without removing it.
func (bt *SendPacketBtree) Min() (packet.Packet, bool) {
    return bt.tree.Min()
}

// Len returns the number of packets in the btree.
func (bt *SendPacketBtree) Len() int {
    return bt.tree.Len()
}

// Iterate traverses packets in sequence order.
// Callback returns false to stop iteration.
func (bt *SendPacketBtree) Iterate(fn func(p packet.Packet) bool) {
    bt.tree.Scan(fn)
}

// IterateFrom traverses packets starting from seq.
// Callback returns false to stop iteration.
func (bt *SendPacketBtree) IterateFrom(seq uint32, fn func(p packet.Packet) bool) {
    pivot := makeSeqLookupPacket(seq)
    bt.tree.Ascend(pivot, fn)
}

// ─────────────────────────────────────────────────────────────────────────────
// Locking Operations (for Tick mode)
// ─────────────────────────────────────────────────────────────────────────────

// InsertLocking acquires lock and calls Insert.
func (bt *SendPacketBtree) InsertLocking(p packet.Packet) {
    bt.mu.Lock()
    defer bt.mu.Unlock()
    bt.Insert(p)
}

// GetLocking acquires read lock and calls Get.
func (bt *SendPacketBtree) GetLocking(seq uint32) packet.Packet {
    bt.mu.RLock()
    defer bt.mu.RUnlock()
    return bt.Get(seq)
}

// DeleteBeforeLocking acquires lock and calls DeleteBefore.
func (bt *SendPacketBtree) DeleteBeforeLocking(cutoff uint32) int {
    bt.mu.Lock()
    defer bt.mu.Unlock()
    return bt.DeleteBefore(cutoff)
}

// Helper for sequence number lookup
func makeSeqLookupPacket(seq uint32) packet.Packet {
    // Create minimal packet for btree lookup
    // Implementation depends on packet.Packet interface
    // This is a placeholder - actual implementation needed
    return nil
}
```

### 5.2 TSBPD Timeline and Tracking Points

```
                        TSBPD Timeline
    ───────────────────────────────────────────────────────────────────────────►

    │◄───────────────── tsbpdDelay (e.g., 3000ms) ──────────────────►│

    │                                                                │
    │       ACKPoint        DeliveryStartPoint                  now+tsbpdDelay
    │          │                   │                                 │
    │          ▼                   ▼                                 ▼
    ├──────────┬───────────────────┬─────────────────────────────────┤
    │   ACKed  │  Waiting to Send  │         Ready to Send           │
    │ (delete) │                   │        (scan this range)        │
    ├──────────┴───────────────────┴─────────────────────────────────┤

    Sequence Numbers:
    [1000]     [1010]              [1050]                        [1100]
       │          │                   │                             │
       └─ DeleteMin() up to ACK point │                             │
                                      └─ Scan for PktTsbpdTime <= now
```

**Tracking Points:**

| Point | Purpose | Update Trigger |
|-------|---------|----------------|
| `ACKPoint` | Highest ACK'd sequence | On ACK received - deleteMin up to this point |
| `DeliveryStartPoint` | Start of TSBPD scan | Updated after each delivery scan |

### 5.3 ACK Processing - DeleteMin Pattern

Following `ack_optimization_implementation.md` "RemoveAll Optimization":

```go
// ProcessACK removes all packets up to the ACK point.
// Called from EventLoop when ACK is received.
func (s *sender) processACK(ackSeq uint32) {
    count := s.sendPacketBtree.DeleteBefore(ackSeq)
    s.metrics.SendPktACKed.Add(uint64(count))
}
```

### 5.4 NAK Processing - O(log n) Lookup

Replaces the O(n) linear search in current `nakLockedHonorOrder()`:

```go
// processNAK handles NAK packet with O(log n) lookup per sequence.
func (s *sender) processNAK(list []circular.Number) int {
    retransCount := 0

    for _, seq := range list {
        // O(log n) lookup instead of O(n) linear search!
        p := s.sendPacketBtree.Get(seq.Val())
        if p == nil {
            s.metrics.InternalNakNotFound.Add(1)
            continue
        }

        // RTO-based suppression check
        nowUs := uint64(time.Now().UnixMicro())
        if s.rtoUs != nil {
            oneWayDelay := s.rtoUs.Load() / 2
            if nowUs - p.Header().LastRetransmitTimeUs < oneWayDelay {
                s.metrics.RetransSuppressed.Add(1)
                continue
            }
        }

        // Retransmit
        p.Header().LastRetransmitTimeUs = nowUs
        p.Header().RetransmitCount++
        s.deliver(p)
        retransCount++

        if p.Header().RetransmitCount == 1 {
            s.metrics.RetransFirstTime.Add(1)
        }
        s.metrics.RetransAllowed.Add(1)
    }

    return retransCount
}
```

---

## 6. Zero-Copy Buffer Management

### 6.1 Current Receiver Pattern

From `zero_copy_opportunities.md` and `packet_pooling_optimization.md`:

```go
// Receiver side: recvBufferPool provides buffers for io_uring/syscall
// Buffer → packet.Packet → btree → deliver → Decommission → pool return
```

### 6.2 Sender Zero-Copy Design

For applications using goSRT, provide a buffer pool for payload allocation:

**File:** `sender_buffer_pool.go` (new)

```go
package send

import (
    "sync"
)

// PayloadPool provides reusable payload buffers for applications.
// Applications Get() a buffer, populate it, call Push(p), and the
// buffer is automatically returned to the pool when ACK'd.
type PayloadPool struct {
    pool *sync.Pool
    size int
}

// NewPayloadPool creates a pool of payload buffers.
func NewPayloadPool(bufferSize int) *PayloadPool {
    return &PayloadPool{
        pool: &sync.Pool{
            New: func() interface{} {
                return make([]byte, bufferSize)
            },
        },
        size: bufferSize,
    }
}

// Get returns a buffer from the pool.
// Application populates this and passes to Push().
func (p *PayloadPool) Get() []byte {
    return p.pool.Get().([]byte)
}

// put returns a buffer to the pool (called internally on Decommission).
func (p *PayloadPool) put(buf []byte) {
    if cap(buf) >= p.size {
        p.pool.Put(buf[:p.size])
    }
}
```

### 6.3 Application Usage Example

Changes needed in `contrib/client-generator/main.go`:

```go
// Current: Application allocates new buffer each time
func generatePacket() []byte {
    buf := make([]byte, payloadSize)  // Allocation every packet!
    fillWithData(buf)
    return buf
}

// New: Application uses sender's payload pool
func generatePacket(pool *send.PayloadPool) []byte {
    buf := pool.Get()  // Reuse from pool
    fillWithData(buf)
    return buf  // Returned to pool when ACK'd
}
```

### 6.4 Packet Lifecycle with Zero-Copy

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                 ZERO-COPY SENDER PACKET LIFECYCLE                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  PayloadPool                                                                │
│      │                                                                      │
│      │ pool.Get()                                                           │
│      ▼                                                                      │
│  ┌──────────────┐                                                           │
│  │ Payload Buf  │  ← Application fills with data                            │
│  └──────┬───────┘                                                           │
│         │                                                                   │
│         │ Push(p) - wraps in packet.Packet (zero copy of payload)           │
│         ▼                                                                   │
│  ┌──────────────┐                                                           │
│  │ PacketPool   │  ← packet.Packet wraps payload pointer                    │
│  │  .Get()      │                                                           │
│  └──────┬───────┘                                                           │
│         │                                                                   │
│         │ SendPacketRing → SendPacketBtree                                  │
│         ▼                                                                   │
│  ┌──────────────┐                                                           │
│  │ SendPacket   │  ← In btree until ACK'd                                   │
│  │   Btree      │                                                           │
│  └──────┬───────┘                                                           │
│         │                                                                   │
│         │ ACK received (or drop threshold)                                  │
│         ▼                                                                   │
│  ┌──────────────┐                                                           │
│  │ Decommission │  ← Returns payload to PayloadPool                         │
│  │              │    Returns packet to PacketPool                           │
│  └──────────────┘                                                           │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 7. Event Loop Architecture

### 7.1 Sender EventLoop Design

Following the receiver EventLoop pattern (`congestion/live/receive/tick.go:135`):

**File:** `congestion/live/send/eventloop.go` (new)

```go
package send

import (
    "context"
    "time"
)

// EventLoop is the main processing loop for lock-free sender mode.
// Single goroutine that:
// 1. Drains packets from SendPacketRing → SendPacketBtree
// 2. Delivers packets when PktTsbpdTime <= now
// 3. Processes timeouts (drop old packets)
func (s *sender) EventLoop(ctx context.Context) {
    // Timing intervals
    deliveryCheckInterval := 1 * time.Millisecond  // Check for ready packets
    dropCheckInterval := 100 * time.Millisecond    // Check for stale packets

    deliveryTicker := time.NewTicker(deliveryCheckInterval)
    dropTicker := time.NewTicker(dropCheckInterval)
    defer deliveryTicker.Stop()
    defer dropTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return

        case <-deliveryTicker.C:
            // 1. Drain ring → btree
            s.drainRingToBtree()

            // 2. Deliver ready packets (TSBPD time reached)
            s.deliverReadyPackets()

        case <-dropTicker.C:
            // 3. Drop packets past threshold
            s.dropOldPackets()
        }
    }
}

// drainRingToBtree moves packets from lock-free ring to btree.
// Single-threaded - no lock needed for btree operations.
func (s *sender) drainRingToBtree() {
    const maxBatch = 64  // Process up to 64 packets per drain

    for i := 0; i < maxBatch; i++ {
        p := s.sendPacketRing.TryPop()
        if p == nil {
            break
        }

        // Insert into btree (O(log n))
        s.sendPacketBtree.Insert(p)
        s.metrics.SendBtreeInserted.Add(1)
    }
}

// deliverReadyPackets sends packets whose TSBPD time has arrived.
// Scans from DeliveryStartPoint forward.
func (s *sender) deliverReadyPackets() {
    nowUs := uint64(time.Now().UnixMicro())
    deliveredCount := 0

    // Start scan from last delivery point
    startSeq := s.deliveryStartSeq.Load()

    s.sendPacketBtree.IterateFrom(startSeq, func(p packet.Packet) bool {
        if p.Header().PktTsbpdTime > nowUs {
            // Not ready yet - stop scanning
            return false
        }

        // TSBPD time reached - deliver packet
        s.deliver(p)
        deliveredCount++

        // Update metrics
        s.metrics.CongestionSendPkt.Add(1)
        s.metrics.CongestionSendByte.Add(uint64(p.Len()))

        // Mark as sent (for retransmission tracking)
        p.Header().FirstSendTimeUs = nowUs
        p.Header().LastRetransmitTimeUs = 0  // Clear for RTO suppression

        // Update delivery start point
        s.deliveryStartSeq.Store(p.Header().PacketSequenceNumber.Val() + 1)

        return true  // Continue scanning
    })

    if deliveredCount > 0 {
        s.metrics.SendDeliveryRuns.Add(1)
    }
}

// dropOldPackets removes packets past the drop threshold.
func (s *sender) dropOldPackets() {
    nowUs := uint64(time.Now().UnixMicro())
    threshold := nowUs - s.dropThreshold

    droppedCount := 0
    for {
        p, ok := s.sendPacketBtree.Min()
        if !ok {
            break
        }

        if p.Header().PktTsbpdTime > threshold {
            break  // Not past threshold yet
        }

        // Drop this packet
        s.sendPacketBtree.DeleteMin()
        p.Decommission()
        droppedCount++
    }

    if droppedCount > 0 {
        s.metrics.SendPktDropped.Add(uint64(droppedCount))
    }
}
```

### 7.2 Smooth Delivery vs Bursty Tick

```
NEW BEHAVIOR (EventLoop):

Application Pushes:    ─●─────●─────●─────●─────●─────●─────●─────●─────●─────●──►
                        (evenly spaced packets from application)

EventLoop Checks:      ─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|─|──►
                        (1ms intervals - checks for ready packets)

Network Output:        ─●─────●─────●─────●─────●─────●─────●─────●─────●─────●──►
                        (SMOOTH! Packets sent at their TSBPD time)

BENEFIT: Network sees evenly-spaced packets, reducing queue overflow and loss.
```

### 7.3 Legacy Tick Mode Compatibility

The existing `Tick()` function remains for backward compatibility:

```go
// Tick is the legacy entry point (used when EventLoop is disabled).
// Acquires lock and processes all operations.
func (s *sender) Tick(now uint64) {
    if s.useEventLoop {
        return  // EventLoop handles everything
    }

    s.lock.Lock()
    defer s.lock.Unlock()

    s.tickDeliverPackets(now)
    s.tickDropOldPackets(now)
}
```

---

## 8. NAK Processing Optimization

### 8.1 Current O(n) Problem

From `congestion/live/send/nak.go:nakLockedHonorOrder()`:

```go
// CURRENT: O(n) linear search per NAK entry
for _, seq := range list {
    for e := s.lossList.Front(); e != nil; e = e.Next() {
        if p.Header().PacketSequenceNumber == seq {
            // Found
        }
    }
}
// Total: O(n × m) where n=list size, m=NAK count
```

### 8.2 New O(log n) Lookup

With `SendPacketBtree`:

```go
// NEW: O(log n) lookup per NAK entry
for _, seq := range list {
    p := s.sendPacketBtree.Get(seq.Val())  // O(log n)
    if p != nil {
        // Found - retransmit
    }
}
// Total: O(m × log n) - massive improvement!
```

### 8.3 Performance Comparison

| Packets in Flight | NAK Entries | Current O(n×m) | New O(m×log n) | Speedup |
|-------------------|-------------|----------------|----------------|---------|
| 1,000 | 10 | 10,000 | 100 | 100× |
| 10,000 | 100 | 1,000,000 | 1,400 | 714× |
| 100,000 | 1,000 | 100,000,000 | 17,000 | 5,882× |

---

## 9. ACK Processing Optimization

### 9.1 DeleteMin Pattern

When ACK is received, delete all packets up to ACK sequence:

```go
// processACK removes all ACK'd packets using efficient deleteMin.
func (s *sender) processACK(ackSeq uint32) int {
    count := 0
    for {
        p, ok := s.sendPacketBtree.Min()
        if !ok {
            break
        }
        if p.Header().PacketSequenceNumber.Val() >= ackSeq {
            break
        }

        // Remove and decommission
        s.sendPacketBtree.DeleteMin()
        p.Decommission()
        count++
    }

    s.metrics.SendPktACKed.Add(uint64(count))
    return count
}
```

### 9.2 Advantages Over Current Design

**Current:**
- Linear scan of `lossList` to find packets to remove
- Packets must be moved from `packetList` to `lossList`

**New:**
- DeleteMin is O(log n) (btree maintains min at leftmost)
- Single data structure - no movement needed

---

## 10. Configuration Options

### 10.1 New Config Fields

**File:** `config.go`

```go
// Add to existing Config struct
type Config struct {
    // ... existing fields ...

    // ═══════════════════════════════════════════════════════════════════════
    // SENDER LOCKLESS CONFIGURATION
    // ═══════════════════════════════════════════════════════════════════════

    // Lock-free ring buffer for sender
    UseSendRing   bool // Enable lock-free ring for Push() (default: false)
    SendRingSize  int  // Ring capacity (must be power of 2, default: 4096)

    // Sender btree configuration
    UseSendBtree    bool // Use btree instead of linked lists (default: false)
    SendBtreeDegree int  // B-tree degree (default: 32)

    // Sender event loop
    UseSendEventLoop       bool          // Enable sender event loop (requires UseSendRing)
    SendDeliveryIntervalMs int           // TSBPD delivery check interval (default: 1ms)
    SendDropIntervalMs     int           // Drop check interval (default: 100ms)

    // Sender payload pool (zero-copy)
    UseSendPayloadPool bool // Enable payload pool for zero-copy (default: false)
    SendPayloadSize    int  // Payload buffer size (default: 1316 bytes)
}
```

### 10.2 Default Values

```go
var defaultConfig = Config{
    // ... existing defaults ...

    // Sender lockless defaults (conservative - disabled by default)
    UseSendRing:            false,
    SendRingSize:           4096,
    UseSendBtree:           false,
    SendBtreeDegree:        32,
    UseSendEventLoop:       false,
    SendDeliveryIntervalMs: 1,
    SendDropIntervalMs:     100,
    UseSendPayloadPool:     false,
    SendPayloadSize:        1316,
}
```

### 10.3 Input Validation

```go
func (c *Config) Validate() error {
    // Sender ring validation
    if c.UseSendRing {
        if c.SendRingSize < 64 {
            return fmt.Errorf("SendRingSize must be >= 64, got %d", c.SendRingSize)
        }
        if c.SendRingSize&(c.SendRingSize-1) != 0 {
            return fmt.Errorf("SendRingSize must be power of 2, got %d", c.SendRingSize)
        }
    }

    // Event loop requires ring
    if c.UseSendEventLoop && !c.UseSendRing {
        return fmt.Errorf("UseSendEventLoop requires UseSendRing=true")
    }

    // Btree degree validation
    if c.SendBtreeDegree < 2 {
        c.SendBtreeDegree = 32  // Use default
    }

    return nil
}
```

### 10.4 CLI Flags

**File:** `contrib/common/flags.go`

```go
var (
    // Sender lockless flags
    UseSendRing = flag.Bool("usesendring", false,
        "Enable lock-free ring for sender Push() operations")
    SendRingSize = flag.Int("sendringsize", 4096,
        "Sender ring capacity (must be power of 2)")
    UseSendBtree = flag.Bool("usesendbtree", false,
        "Use btree for sender packet storage (O(log n) NAK lookup)")
    UseSendEventLoop = flag.Bool("usesendeventloop", false,
        "Enable sender event loop for smooth packet delivery (requires -usesendring)")
)

// ApplyFlagsToConfig updates config with CLI flag values
func ApplyFlagsToConfig(c *gosrt.Config) {
    // ... existing flag applications ...

    // Sender lockless flags
    if *UseSendRing {
        c.UseSendRing = true
    }
    if *SendRingSize != 4096 {
        c.SendRingSize = *SendRingSize
    }
    if *UseSendBtree {
        c.UseSendBtree = true
    }
    if *UseSendEventLoop {
        c.UseSendEventLoop = true
    }
}
```

---

## 11. Metrics

### 11.1 New Metrics

**File:** `metrics/metrics.go`

```go
type ConnectionMetrics struct {
    // ... existing fields ...

    // ═══════════════════════════════════════════════════════════════════════
    // SENDER LOCKLESS METRICS
    // ═══════════════════════════════════════════════════════════════════════

    // Ring buffer metrics
    SendRingPushed     atomic.Uint64 // Packets pushed to ring
    SendRingDropped    atomic.Uint64 // Packets dropped (ring full)
    SendRingDrained    atomic.Uint64 // Packets drained to btree

    // Btree metrics
    SendBtreeInserted  atomic.Uint64 // Packets inserted to btree
    SendBtreeLen       atomic.Uint64 // Current btree size

    // EventLoop metrics
    SendEventLoopRuns       atomic.Uint64 // EventLoop iterations
    SendDeliveryRuns        atomic.Uint64 // Delivery check runs
    SendDeliveryPackets     atomic.Uint64 // Packets delivered (smooth)
    SendDeliveryBackoffs    atomic.Uint64 // Idle backoffs

    // NAK performance metrics
    SendNakLookups          atomic.Uint64 // NAK btree lookups
    SendNakLookupHits       atomic.Uint64 // Successful NAK lookups
    SendNakLookupMisses     atomic.Uint64 // NAK sequence not found

    // ACK performance metrics
    SendAckDeleteMinCalls   atomic.Uint64 // DeleteMin operations
    SendAckDeleteMinPackets atomic.Uint64 // Packets removed via ACK
}
```

### 11.2 Prometheus Handler

**File:** `metrics/handler.go`

```go
// Add to writeConnectionMetrics:

// Sender ring metrics
writeCounterIfNonZero(b, "gosrt_send_ring_pushed_total", m.SendRingPushed.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ring_dropped_total", m.SendRingDropped.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ring_drained_total", m.SendRingDrained.Load(), socketLabel)

// Sender btree metrics
writeGaugeIfNonZero(b, "gosrt_send_btree_len", float64(m.SendBtreeLen.Load()), socketLabel)

// Sender EventLoop metrics
writeCounterIfNonZero(b, "gosrt_send_eventloop_runs_total", m.SendEventLoopRuns.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_delivery_runs_total", m.SendDeliveryRuns.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_delivery_packets_total", m.SendDeliveryPackets.Load(), socketLabel)

// NAK performance
writeCounterIfNonZero(b, "gosrt_send_nak_lookups_total", m.SendNakLookups.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_nak_lookup_hits_total", m.SendNakLookupHits.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_nak_lookup_misses_total", m.SendNakLookupMisses.Load(), socketLabel)
```

### 11.3 Metrics Audit

Run `make audit-metrics` after implementation to verify:
- All new metrics are defined in `metrics/metrics.go`
- All new metrics are exported in `metrics/handler.go`
- All new metrics have tests in `metrics/handler_test.go`

---

## 12. Implementation Phases

### Phase 1: SendPacketBtree (Foundation)

**Goal:** Replace linked lists with btree, maintaining current locking.

**Files to modify:**
| File | Changes |
|------|---------|
| `congestion/live/send/send_packet_btree.go` | New file - btree implementation |
| `congestion/live/send/sender.go` | Add btree field, initialize |
| `congestion/live/send/push.go` | Use btree.Insert instead of list.PushBack |
| `congestion/live/send/tick.go` | Use btree.IterateFrom for delivery |
| `congestion/live/send/nak.go` | Use btree.Get for O(log n) lookup |
| `congestion/live/send/ack.go` | Use btree.DeleteBefore |

**Tests:**
- `send_packet_btree_test.go` - Unit tests for btree operations
- `send_packet_btree_bench_test.go` - Benchmark btree vs linked list

**Checkpoint:** `go test ./congestion/live/send/...` passes

### Phase 2: SendPacketRing (Lock-Free Buffer)

**Goal:** Add lock-free ring for Push() operations.

**Files to modify:**
| File | Changes |
|------|---------|
| `congestion/live/send/ring.go` | New file - ring implementation |
| `congestion/live/send/sender.go` | Add ring field, function dispatch |
| `congestion/live/send/push.go` | Add push() lock-free version |
| `config.go` | Add UseSendRing, SendRingSize |
| `contrib/common/flags.go` | Add CLI flags |

**Tests:**
- `send_ring_test.go` - Ring unit tests
- `send_ring_bench_test.go` - Ring benchmark
- `send_push_test.go` - Both push paths

**Checkpoint:** `make test-flags` passes

### Phase 3: Sender EventLoop

**Goal:** Add continuous event loop for smooth delivery.

**Files to modify:**
| File | Changes |
|------|---------|
| `congestion/live/send/eventloop.go` | New file - event loop |
| `congestion/live/send/sender.go` | Add UseEventLoop() method |
| `connection.go` | Start sender EventLoop if enabled |
| `config.go` | Add UseSendEventLoop |

**Tests:**
- `send_eventloop_test.go` - EventLoop unit tests
- `send_eventloop_race_test.go` - Race condition tests

**Checkpoint:** `go test -race ./...` passes

### Phase 4: Zero-Copy Payload Pool

**Goal:** Enable zero-copy buffer management for applications.

**Files to modify:**
| File | Changes |
|------|---------|
| `congestion/live/send/payload_pool.go` | New file - payload pool |
| `packet/packet.go` | Add PayloadPool field for return |
| `contrib/client-generator/main.go` | Update to use payload pool |

**Tests:**
- `payload_pool_test.go` - Pool unit tests
- `payload_pool_bench_test.go` - Allocation benchmark

### Phase 5: Metrics and Observability

**Goal:** Add all metrics, ensure audit passes.

**Files to modify:**
| File | Changes |
|------|---------|
| `metrics/metrics.go` | Add sender lockless metrics |
| `metrics/handler.go` | Add Prometheus export |
| `metrics/handler_test.go` | Add metric tests |
| `contrib/integration_testing/parallel_analysis.go` | Add sender metrics category |

**Checkpoint:** `make audit-metrics` passes

### Phase 6: Integration Testing

**Goal:** Verify smooth delivery and performance improvement.

**Tests:**
- Add `Parallel-Sender-EventLoop-*` test configurations
- Compare burst behavior: Tick vs EventLoop
- Measure packet spacing on network

---

## 13. Testing Strategy

### 13.1 Unit Tests

| Test File | Coverage |
|-----------|----------|
| `send_packet_btree_test.go` | Insert, Get, Delete, Iterate |
| `send_packet_btree_table_test.go` | Table-driven edge cases |
| `send_ring_test.go` | Push, TryPop, DrainBatch |
| `send_eventloop_test.go` | Delivery timing, drop behavior |
| `send_push_test.go` | Both push paths |

### 13.2 Benchmarks

| Benchmark | Comparison |
|-----------|------------|
| `BenchmarkSendNakLookup_List` | Current O(n) lookup |
| `BenchmarkSendNakLookup_Btree` | New O(log n) lookup |
| `BenchmarkSendPush_Locked` | Current locked Push |
| `BenchmarkSendPush_Lockfree` | New ring Push |

### 13.3 Race Tests

```go
// send_race_test.go
func TestRace_SendRingConcurrentPush(t *testing.T) {
    ring := NewSendPacketRing(1024)

    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < 1000; j++ {
                p := packet.NewPacket(nil)
                ring.Push(p)
            }
        }()
    }
    wg.Wait()
}
```

### 13.4 Integration Tests

```bash
# Test smooth delivery (compare packet spacing)
sudo make test-parallel CONFIG=Parallel-Sender-EventLoop-20M-Base-vs-FullEL

# Verify no regression
sudo make test-parallel CONFIG=Parallel-Clean-50M-Base-vs-SenderEventLoop
```

---

## 14. Migration Path

### 14.1 Feature Flags

All new features are **opt-in** via configuration:

```go
// Conservative default: all disabled
config := gosrt.DefaultConfig()
// UseSendRing: false
// UseSendBtree: false
// UseSendEventLoop: false

// Enable incrementally for testing
config.UseSendBtree = true      // Phase 1: Just btree
config.UseSendRing = true       // Phase 2: Add ring
config.UseSendEventLoop = true  // Phase 3: Full lockless
```

### 14.2 A/B Testing

The parallel test framework supports comparing:
- Baseline (Tick mode, linked lists) vs HighPerf (EventLoop, btree)

```bash
# Compare burst behavior
sudo make test-parallel CONFIG=Parallel-Sender-Tick-vs-EventLoop
```

### 14.3 Rollback

If issues found:
1. Disable EventLoop: `UseSendEventLoop = false`
2. Disable ring: `UseSendRing = false`
3. Full rollback: Remove config flags, use defaults

---

## Appendix A: File Summary

| File | Status | Description |
|------|--------|-------------|
| `congestion/live/send/sender.go` | Modify | Add btree, ring, dispatch |
| `congestion/live/send/send_packet_btree.go` | New | Btree implementation |
| `congestion/live/send/ring.go` | New | Lock-free ring |
| `congestion/live/send/push.go` | Modify | Add lock-free push |
| `congestion/live/send/eventloop.go` | New | Sender event loop |
| `congestion/live/send/nak.go` | Modify | O(log n) NAK lookup |
| `congestion/live/send/ack.go` | Modify | DeleteBefore optimization |
| `congestion/live/send/payload_pool.go` | New | Zero-copy buffer pool |
| `config.go` | Modify | Add config options |
| `contrib/common/flags.go` | Modify | Add CLI flags |
| `metrics/metrics.go` | Modify | Add sender metrics |
| `metrics/handler.go` | Modify | Add Prometheus export |

---

## Appendix B: Expected Performance Improvements

| Operation | Current | New | Improvement |
|-----------|---------|-----|-------------|
| Push() | O(1) + lock | O(1) lock-free | ~3× faster |
| NAK lookup | O(n) | O(log n) | 100-5000× faster |
| ACK processing | O(n) scan | O(k × log n) deleteMin | 10-100× faster |
| Packet delivery | Burst (10ms) | Smooth (1ms) | Better network behavior |
| Lock contention | High | Zero | Eliminates bottleneck |

---

**This design is ready for implementation review.**

