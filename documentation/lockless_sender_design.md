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
   - 3.1 [New Architecture Overview](#31-new-architecture-overview)
   - 3.2 [Key Components](#32-key-components)
   - 3.3 [Data Flow Comparison](#33-data-flow-comparison)
   - 3.4 [Why Control Packet Ring is Required](#34-why-control-packet-ring-is-required)
4. [Detailed Design](#4-detailed-design)
5. [SendPacketBtree Design](#5-sendpacketbtree-design)
6. [Zero-Copy Buffer Management](#6-zero-copy-buffer-management)
7. [Event Loop Architecture](#7-event-loop-architecture)
   - 7.1 [Sender EventLoop Design](#71-sender-eventloop-design)
   - 7.2 [Delivery Logic](#72-delivery-logic)
   - 7.3 [Drop Logic](#73-drop-logic)
   - 7.4 [CRITICAL: Control Packet Routing to EventLoop](#74-critical-control-packet-routing-to-eventloop)
8. [NAK Processing Optimization](#8-nak-processing-optimization)
9. [ACK Processing Optimization](#9-ack-processing-optimization)
10. [Configuration Options](#10-configuration-options)
11. [Metrics](#11-metrics)
    - 11.1 [New Metrics](#111-new-metrics)
    - 11.2 [Prometheus Handler](#112-prometheus-handler)
    - 11.3 [Baseline Tick Instrumentation](#113-baseline-tick-instrumentation)
    - 11.4 [Burst Detection Metric Derivation](#114-burst-detection-metric-derivation)
    - 11.5 [Metrics Audit](#115-metrics-audit)
12. [Implementation Phases](#12-implementation-phases)
    - Phase 8: Migration Path
      - 8.1 [Feature Flag Hierarchy](#81-feature-flag-hierarchy)
      - 8.2 [Validation at Each Level](#82-validation-at-each-level)
      - 8.3 [Sender EventLoop Metrics (Parity with Receiver)](#83-sender-eventloop-metrics-parity-with-receiver)
      - 8.4 [Prometheus Export for Sender EventLoop](#84-prometheus-export-for-sender-eventloop)
      - 8.5 [Diagnostics via Metrics](#85-diagnostics-via-metrics)
      - 8.6 [A/B Testing Configuration](#86-ab-testing-configuration)
      - 8.7 [Rollback Procedures](#87-rollback-procedures)
      - 8.8 [Production Rollout Checklist](#88-production-rollout-checklist)
      - 8.9 [Config Variants for Integration Test Matrix](#89-config-variants-for-integration-test-matrix)
      - 8.10 [Parallel Comparison Tests for Sender](#810-parallel-comparison-tests-for-sender)
      - 8.11 [Test Configuration Files](#811-test-configuration-files)
      - 8.12 [Integration with Test Matrix Generator](#812-integration-with-test-matrix-generator)
      - 8.13 [Expected Metrics Comparison Output](#813-expected-metrics-comparison-output)
      - 8.14 [Makefile Targets](#814-makefile-targets)
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
│  Application                    io_uring Completion Handler                 │
│      │                               │                                      │
│      │ Push(p)                       │ ACK/NAK arrives                      │
│      │ (lock-free)                   │ (different goroutine)                │
│      ▼                               ▼                                      │
│  ┌───────────────────┐         ┌───────────────────┐                        │
│  │  SendPacketRing   │         │ ControlPacketRing │   ← CRITICAL:          │
│  │  (Data packets)   │         │ (ACK/NAK packets) │     Routes control     │
│  │  MPSC ring        │         │  MPSC ring        │     packets to         │
│  └─────────┬─────────┘         └─────────┬─────────┘     EventLoop          │
│            │                             │                                  │
│            │                             │                                  │
│            └──────────────┬──────────────┘                                  │
│                           │                                                 │
│                           │ EventLoop (SINGLE CONSUMER)                     │
│                           │ drains BOTH rings                               │
│                           ▼                                                 │
│                 ┌───────────────────┐                                       │
│                 │ SendPacketBtree   │  ← Single ordered btree               │
│                 │ (single-threaded) │    O(log n) operations                │
│                 │ NO LOCKS NEEDED!  │    All access from EventLoop          │
│                 └─────────┬─────────┘                                       │
│                           │                                                 │
│                           │ Tracking Points:                                │
│                           │   - contiguousPoint (ACK'd sequence)            │
│                           │   - DeliveryStartPoint (TSBPD scan start)       │
│                           │                                                 │
│            ┌──────────────┼──────────────┬──────────────────┐               │
│            │              │              │                  │               │
│            ▼              ▼              ▼                  ▼               │
│       DeliverPackets  ProcessACK    ProcessNAK          DropOld             │
│       (TSBPD time)    (deleteMin)   (O(log n) lookup)   (threshold)         │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│  KEY INSIGHT: Both data AND control packets flow through lock-free rings,   │
│               ensuring the EventLoop is the ONLY goroutine accessing the    │
│               btree. This eliminates ALL race conditions by design.         │
│               See Section 7.4 for detailed race analysis.                   │
│  ═══════════════════════════════════════════════════════════════════════    │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Key Components

| Component | Purpose | Lock-Free? |
|-----------|---------|------------|
| `SendPacketRing` | Buffer incoming Push() calls | Yes (MPSC) |
| `ControlPacketRing` | Buffer incoming ACK/NAK for EventLoop | Yes (MPSC) |
| `SendPacketBtree` | Ordered packet storage | Single-threaded (no lock needed) |
| `SenderEventLoop` | Process rings, deliver packets, handle control | Single goroutine |
| `DeliveryStartPoint` | Track TSBPD scan position | Atomic |
| `contiguousPoint` | Track highest ACK'd sequence (same name as receiver) | Atomic |

**Critical Insight:** The `ControlPacketRing` is essential for achieving true lock-free operation. Without it, ACK/NAK processing would require locks because control packets arrive on the io_uring completion handler goroutine, separate from the EventLoop. See Section 7.4 for detailed design.

### 3.3 Data Flow Comparison

**Current (Locked) - Multiple Lock Acquisitions:**
```
Push() ──[LOCK]──► packetList ──[LOCK/TICK]──► lossList ──[LOCK]──► deliver()
                                    │
                                    └──[LOCK]──► NAK lookup (O(n))

ACK arrives ──[LOCK]──► lossList.Delete() ──► RACE with Tick()!
NAK arrives ──[LOCK]──► lossList.Traverse() ──► RACE with ACK/Tick()!
```

**New (Lockless) - Single Consumer via Rings:**
```
┌─────────────────────────────────────────────────────────────────────────────┐
│  DATA PATH (Application → Network)                                          │
│                                                                             │
│  Push() ──[atomic]──► SendPacketRing ──┐                                    │
│                                        │                                    │
│                                        │ EventLoop                          │
│                                        │ (single consumer)                  │
│                                        ▼                                    │
│                                  SendPacketBtree ──► deliver() (smooth)     │
│                                        │                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│  CONTROL PATH (Network → Btree operations)                                  │
│                                                                             │
│  ACK arrives ──[atomic]──► ControlPacketRing ──┐                            │
│                                                │                            │
│  NAK arrives ──[atomic]──► ControlPacketRing ──┤ EventLoop                  │
│                                                │ (same single consumer!)    │
│                                                ▼                            │
│                                          SendPacketBtree                    │
│                                                │                            │
│                        ┌───────────────────────┴───────────────────────┐    │
│                        ▼                                               ▼    │
│                  ProcessACK()                                   ProcessNAK()│
│                  DeleteBefore()                                 Get() O(log n)
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘

KEY: All paths converge on EventLoop ──► NO CONCURRENT ACCESS ──► NO LOCKS NEEDED
```

### 3.4 Why Control Packet Ring is Required

The control packet ring is **not optional** for lock-free sender. Without it:

| Scenario | What Happens | Result |
|----------|--------------|--------|
| ACK on io_uring goroutine | `processACK()` modifies btree | **Race with EventLoop** |
| NAK on io_uring goroutine | `processNAK()` traverses btree | **Race with EventLoop** |
| NAK + ACK concurrent | Both access btree | **Race with each other** |

See Section 7.4 for detailed race condition analysis with 5 specific scenarios.

---

## 4. Detailed Design

### 4.1 SendPacketRing

Following the receiver pattern (`congestion/live/receive/ring.go`), with **configurable shard count**.

**Initial Implementation:** Start with **1 shard** (default) to preserve strict packet ordering.

**High Write Rate Optimization:** For applications with very high write rates where multiple
goroutines call `Push()` concurrently, increasing the shard count (e.g., 4 or 8) can reduce
contention. The btree will sort packets by sequence number regardless, so ordering is preserved
in the final delivery. However, for most use cases, a single shard is sufficient and simpler.

**Configuration:**
- `SendRingShards`: Number of shards (power of 2, default: 1)
- `SendRingSize`: Per-shard capacity (power of 2, default: 1024)

**File:** `congestion/live/send/ring.go` (new)

```go
package send

import (
    ring "github.com/randomizedcoder/go-lock-free-ring"
    "github.com/randomizedcoder/gosrt/packet"
)

// SendPacketRing is a lock-free MPSC ring for incoming packets.
// Shard count is configurable for high-throughput scenarios.
type SendPacketRing struct {
    ring *ring.MultiShardLockFreeRing[packet.Packet]
}

// NewSendPacketRing creates a ring with configurable shards.
// For strict ordering, use shards=1 (default).
// For high write throughput, increase shards (btree will sort).
func NewSendPacketRing(size, shards int) *SendPacketRing {
    if shards < 1 {
        shards = 1 // Default: single shard for ordering
    }
    return &SendPacketRing{
        ring: ring.NewMultiShardLockFreeRing[packet.Packet](
            size,    // Per-shard capacity (power of 2)
            shards,  // Configurable shards
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

Following the receiver pattern (`pushWithLock` wraps `pushLocked`), we use a consistent naming convention where the lock-free function is the "core" and the locking version is the wrapper.

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

// pushLocking is the wrapper that acquires lock and calls push().
// Used by Tick mode for concurrent access.
// NOTE: This replaces the old "pushLocked" naming - the locking version is the wrapper.
func (s *sender) pushLocking(p packet.Packet) {
    s.lock.Lock()
    defer s.lock.Unlock()
    s.push(p)  // Calls the same core implementation
}
```

**Naming Convention (consistent with receiver):**
- `push()` - Lock-free core function
- `pushLocking()` - Wrapper that acquires lock and calls `push()`

Both Tick mode and EventLoop mode can use `push()` directly when appropriate locks are already held, or use `pushLocking()` for safe concurrent access.

### 4.3 Function Dispatch Pattern

Following the receiver pattern (`congestion/live/receive/receiver.go:234-237`):

**File:** `congestion/live/send/sender.go`

```go
type sender struct {
    // ... existing fields ...

    // Function dispatch for data packets (configured at creation)
    pushFn       func(p packet.Packet)                   // push or pushLocking
    processACKFn func(ackSeq uint32) int                 // processACK or processACKLocking
    processNAKFn func(list []circular.Number) int       // processNAK or processNAKLocking

    // Function dispatch for control packet routing (see Section 7.4)
    handleACKFn  func(p packet.Packet)  // pushACKToControlRing or handleACKDirect
    handleNAKFn  func(p packet.Packet)  // pushNAKToControlRing or handleNAKDirect

    // New lock-free components
    sendPacketRing    *SendPacketRing     // Data packets from Push()
    controlPacketRing *ControlPacketRing  // ACK/NAK from io_uring (CRITICAL for lockless)
    sendPacketBtree   *SendPacketBtree    // Ordered storage (single-threaded access)
    useEventLoop      bool
    useSendRing       bool
    useControlRing    bool                // Must be true for lock-free sender

    // Tracking points (consistent naming with receiver)
    contiguousPoint    atomic.Uint32  // Highest ACK'd sequence (same name as receiver)
    deliveryStartSeq   atomic.Uint32  // Sequence to start TSBPD scan

    // Adaptive backoff configuration (same as receiver)
    backoffMinSleep      time.Duration
    backoffMaxSleep      time.Duration
    backoffColdStartPkts int
}

func NewSender(sendConfig SendConfig) congestion.Sender {
    s := &sender{
        // ... existing initialization ...
    }

    // Function dispatch based on configuration
    // Pattern: lock-free functions are the "core", Locking versions are wrappers
    if sendConfig.UseSendRing {
        // ═══════════════════════════════════════════════════════════════════
        // EventLoop mode: lock-free path
        // CRITICAL: Both data ring AND control ring must be enabled for
        // true lock-free operation. See Section 7.4 for details.
        // ═══════════════════════════════════════════════════════════════════

        // Data packet ring (from application Push())
        s.sendPacketRing = NewSendPacketRing(sendConfig.SendRingSize)
        s.sendPacketBtree = NewSendPacketBtree(sendConfig.BtreeDegree)

        // Control packet ring (from io_uring ACK/NAK handlers)
        // This is REQUIRED for lock-free sender - routes control packets
        // through EventLoop instead of processing directly with locks.
        s.controlPacketRing = NewControlPacketRing(
            sendConfig.ControlRingSize,    // Default: 256
            sendConfig.ControlRingShards,  // Default: 2
        )

        // Data packet functions (lock-free, called from EventLoop)
        s.pushFn = s.push              // Lock-free
        s.processACKFn = s.processACK  // Lock-free
        s.processNAKFn = s.processNAK  // Lock-free

        // Control packet routing (pushes to ring for EventLoop)
        s.handleACKFn = s.pushACKToControlRing
        s.handleNAKFn = s.pushNAKToControlRing

        s.useSendRing = true
        s.useControlRing = true
        s.useEventLoop = sendConfig.UseSendEventLoop

        // Backoff configuration
        s.backoffMinSleep = sendConfig.BackoffMinSleep
        s.backoffMaxSleep = sendConfig.BackoffMaxSleep
        s.backoffColdStartPkts = sendConfig.BackoffColdStartPkts
    } else {
        // ═══════════════════════════════════════════════════════════════════
        // Tick mode: locked path (backward compatible)
        // ═══════════════════════════════════════════════════════════════════
        s.pushFn = s.pushLocking              // Wrapper with lock
        s.processACKFn = s.processACKLocking  // Wrapper with lock
        s.processNAKFn = s.processNAKLocking  // Wrapper with lock

        // Control packets processed directly with lock
        s.handleACKFn = s.handleACKDirect
        s.handleNAKFn = s.handleNAKDirect

        s.useSendRing = false
        s.useControlRing = false
        s.useEventLoop = false
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

    "github.com/google/btree"
    "github.com/randomizedcoder/gosrt/circular"
    "github.com/randomizedcoder/gosrt/packet"
)

// sendPacketItem wraps a packet for storage in btree (same pattern as receiver)
type sendPacketItem struct {
    seqNum circular.Number
    packet packet.Packet
}

// SendPacketBtree stores packets ordered by sequence number.
// In EventLoop mode: single-threaded, no lock needed.
// In Tick mode: uses mutex for concurrent access.
type SendPacketBtree struct {
    tree *btree.BTreeG[*sendPacketItem]
    mu   sync.RWMutex  // Only used in Tick mode
}

// NewSendPacketBtree creates a new packet btree.
// IMPORTANT: Uses circular.SeqLess for sequence number comparison to handle
// 31-bit wraparound correctly. This is the same comparison used by:
//   - Receiver packet btree (congestion/live/receive/packet_store_btree.go:26-29)
//   - NAK btree (congestion/live/receive/nak_btree.go:33-34)
//
// See ./circular/seq_math_31bit_wraparound_test.go for wraparound test cases.
func NewSendPacketBtree(degree int) *SendPacketBtree {
    if degree < 2 {
        degree = 32  // Default degree (same as receiver)
    }
    return &SendPacketBtree{
        tree: btree.NewG(degree, func(a, b *sendPacketItem) bool {
            // Use circular.SeqLess for proper 31-bit wraparound handling
            // ~10% faster than circular.Number.Lt() method
            return circular.SeqLess(a.seqNum.Val(), b.seqNum.Val())
        }),
    }
}

// ─────────────────────────────────────────────────────────────────────────────
// Lock-Free Operations (for EventLoop mode)
// ─────────────────────────────────────────────────────────────────────────────

// Insert adds a packet to the btree using single-traversal ReplaceOrInsert.
// EventLoop mode: called from single goroutine, no lock needed.
//
// CRITICAL: Uses ReplaceOrInsert to ensure single btree traversal.
// This follows the pattern established in packet_store_btree.go Insert().
// See retransmission_and_nak_suppression_design.md Section 5.1 "Bug Fix:
// Duplicate Packet Handling in btree Insert" for the detailed rationale.
//
// Key insight: ReplaceOrInsert atomically swaps, so on duplicate we keep
// the new packet and return the old one for release - no second traversal needed.
//
// Returns: (inserted bool, duplicatePacket packet.Packet)
//   - inserted=true, duplicatePacket=nil: new packet inserted successfully
//   - inserted=false, duplicatePacket=pkt: duplicate detected, caller should release
func (bt *SendPacketBtree) Insert(p packet.Packet) (bool, packet.Packet) {
    h := p.Header()
    item := &sendPacketItem{
        seqNum: h.PacketSequenceNumber,
        packet: p,
    }

    // Single traversal - ReplaceOrInsert returns (oldItem, replaced bool)
    // If duplicate exists, new packet replaces old - we keep the new one
    old, replaced := bt.tree.ReplaceOrInsert(item)

    if replaced {
        // Duplicate! New packet is now in tree, old packet was kicked out.
        // Return old packet for caller to release (saves 2nd traversal).
        return false, old.packet
    }

    return true, nil
}

// Get retrieves a packet by sequence number (for NAK lookup).
// Returns nil if not found. O(log n).
func (bt *SendPacketBtree) Get(seq uint32) packet.Packet {
    // Create lookup pivot with target sequence
    pivot := &sendPacketItem{seqNum: circular.New(seq, packet.MAX_SEQUENCENUMBER)}

    item, found := bt.tree.Get(pivot)
    if !found {
        return nil
    }
    return item.packet
}

// DeleteMin removes and returns the minimum (oldest) packet.
// Used for ACK processing.
func (bt *SendPacketBtree) DeleteMin() (packet.Packet, bool) {
    item, ok := bt.tree.DeleteMin()
    if !ok {
        return nil, false
    }
    return item.packet, true
}

// DeleteBefore removes all packets with seq < cutoff.
// Returns count of deleted packets. Used for ACK processing.
func (bt *SendPacketBtree) DeleteBefore(cutoff uint32) int {
    count := 0
    for {
        item, ok := bt.tree.Min()
        if !ok {
            break
        }
        // Use circular.SeqLessOrEqual for proper wraparound comparison
        if !circular.SeqLess(item.seqNum.Val(), cutoff) {
            break
        }
        bt.tree.DeleteMin()
        item.packet.Decommission()  // Return to pool
        count++
    }
    return count
}

// Min returns the minimum packet without removing it.
func (bt *SendPacketBtree) Min() (packet.Packet, bool) {
    item, ok := bt.tree.Min()
    if !ok {
        return nil, false
    }
    return item.packet, true
}

// Len returns the number of packets in the btree.
func (bt *SendPacketBtree) Len() int {
    return bt.tree.Len()
}

// Iterate traverses packets in sequence order.
// Callback returns false to stop iteration.
func (bt *SendPacketBtree) Iterate(fn func(p packet.Packet) bool) {
    bt.tree.Ascend(func(item *sendPacketItem) bool {
        return fn(item.packet)
    })
}

// IterateFrom traverses packets starting from seq.
// Callback returns false to stop iteration.
func (bt *SendPacketBtree) IterateFrom(seq uint32, fn func(p packet.Packet) bool) {
    pivot := &sendPacketItem{seqNum: circular.New(seq, packet.MAX_SEQUENCENUMBER)}
    bt.tree.AscendGreaterOrEqual(pivot, func(item *sendPacketItem) bool {
        return fn(item.packet)
    })
}

// ─────────────────────────────────────────────────────────────────────────────
// Locking Operations (for Tick mode)
// Following the pattern from nakBtree: lock-free core + *Locking wrapper
// ─────────────────────────────────────────────────────────────────────────────

// InsertLocking acquires lock and calls Insert.
func (bt *SendPacketBtree) InsertLocking(p packet.Packet) (bool, packet.Packet) {
    bt.mu.Lock()
    defer bt.mu.Unlock()
    return bt.Insert(p)
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

// DeleteMinLocking acquires lock and calls DeleteMin.
func (bt *SendPacketBtree) DeleteMinLocking() (packet.Packet, bool) {
    bt.mu.Lock()
    defer bt.mu.Unlock()
    return bt.DeleteMin()
}

// LenLocking acquires read lock and calls Len.
func (bt *SendPacketBtree) LenLocking() int {
    bt.mu.RLock()
    defer bt.mu.RUnlock()
    return bt.Len()
}
```

### 5.1.1 Unit Tests Required

Following the pattern from `congestion/live/receive/nak_btree_test.go`, we need:

**File:** `congestion/live/send/send_packet_btree_test.go`

```go
func TestSendPacketBtree_Insert(t *testing.T)           // Basic insert
func TestSendPacketBtree_InsertDuplicate(t *testing.T)  // Duplicate handling
func TestSendPacketBtree_Get(t *testing.T)              // O(log n) lookup
func TestSendPacketBtree_DeleteBefore(t *testing.T)     // ACK processing
func TestSendPacketBtree_IterateFrom(t *testing.T)      // Delivery scan
func TestSendPacketBtree_SeqWraparound(t *testing.T)    // 31-bit wraparound
func TestSendPacketBtree_ConcurrentAccess(t *testing.T) // Race test with Locking
func BenchmarkSendPacketBtree_Get(b *testing.B)         // NAK lookup performance
func BenchmarkSendPacketBtree_Insert(b *testing.B)      // Insert performance
```

### 5.2 TSBPD Timeline and Tracking Points

```
                        TSBPD Timeline
    ───────────────────────────────────────────────────────────────────────────►

    │◄───────────────── tsbpdDelay (e.g., 3000ms) ──────────────────►│

    │                                                                │
    │  contiguousPoint   DeliveryStartPoint                   now+tsbpdDelay
    │          │                   │                                 │
    │          ▼                   ▼                                 ▼
    ├──────────┬───────────────────┬─────────────────────────────────┤
    │   ACKed  │  Waiting to Send  │         Ready to Send           │
    │ (delete) │                   │        (scan this range)        │
    ├──────────┴───────────────────┴─────────────────────────────────┤

    Sequence Numbers:
    [1000]     [1010]              [1050]                        [1100]
       │          │                   │                             │
       └─ DeleteMin() up to          └─ Scan for PktTsbpdTime <= now
          contiguousPoint               (only moves small amount each iteration)
```

**Tracking Points:**

| Point | Purpose | Update Trigger |
|-------|---------|----------------|
| `contiguousPoint` | Highest ACK'd sequence | On ACK received - deleteMin up to this point |
| `DeliveryStartPoint` | Start of TSBPD scan | Updated after each delivery scan |

**Naming Convention:** Using `contiguousPoint` instead of "ACKPoint" to match the receiver naming (`receiver.contiguousPoint`). This maintains consistency across the library and makes the code easier to understand.

**DeliveryStartPoint Behavior:** In each EventLoop iteration, `DeliveryStartPoint` should only advance by a small number of packets (typically 1-10). The TSBPD schedule ensures packets become ready incrementally, not all at once. If `DeliveryStartPoint` jumps by a large amount, it indicates a problem (ring backup, processing delay, etc.) and should be logged/metriced.

### 5.3 ACK Processing - DeleteMin Pattern

Following `ack_optimization_implementation.md` "RemoveAll Optimization":

```go
// processACK removes all packets up to the contiguous point.
// Called from EventLoop when ACK is received (lock-free version).
func (s *sender) processACK(ackSeq uint32) int {
    count := s.sendPacketBtree.DeleteBefore(ackSeq)
    s.metrics.SendPktACKed.Add(uint64(count))

    // Update contiguousPoint
    s.contiguousPoint.Store(ackSeq)

    return count
}

// processACKLocking is the wrapper for Tick mode.
func (s *sender) processACKLocking(ackSeq uint32) int {
    s.lock.Lock()
    defer s.lock.Unlock()
    return s.processACK(ackSeq)
}
```

### 5.4 NAK Processing - O(log n) Lookup

Replaces the O(n) linear search in current `nakLockedHonorOrder()`:

```go
// processNAK handles NAK packet with O(log n) lookup per sequence.
// Lock-free version for EventLoop mode.
func (s *sender) processNAK(list []circular.Number) int {
    retransCount := 0

    // Pre-fetch time and RTO threshold once (optimization from RTO suppression)
    nowUs := uint64(time.Now().UnixMicro())
    var oneWayDelay uint64
    if s.rtoUs != nil {
        oneWayDelay = s.rtoUs.Load() / 2
    }

    for _, seq := range list {
        // O(log n) lookup instead of O(n) linear search!
        p := s.sendPacketBtree.Get(seq.Val())
        if p == nil {
            s.metrics.InternalNakNotFound.Add(1)
            continue
        }

        // RTO-based suppression check
        if s.rtoUs != nil && nowUs - p.Header().LastRetransmitTimeUs < oneWayDelay {
            s.metrics.RetransSuppressed.Add(1)
            continue
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

// processNAKLocking is the wrapper for Tick mode.
func (s *sender) processNAKLocking(list []circular.Number) int {
    s.lock.Lock()
    defer s.lock.Unlock()
    return s.processNAK(list)
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

**Key Insight:** We can reuse the existing `globalRecvBufferPool` from `buffers.go` rather than creating a separate sender pool. The pool contains 1500-byte buffers (standard MTU), which is the same size needed for sending.

**File:** `buffers.go` (existing - no changes needed!)

```go
// globalRecvBufferPool is the shared pool for all receive buffers.
// This single pool serves ALL listeners and dialers in the process,
// enabling maximum buffer reuse across connections.
//
// Design rationale:
//   - Single pool = maximum sharing between all connections
//   - Fixed 1500-byte size = standard Ethernet MTU, fits all SRT packets
//   - Buffers flow freely between listeners, dialers, and connections
//   - Reduces GC pressure by reusing allocations
var globalRecvBufferPool = &sync.Pool{
    New: func() any {
        buf := make([]byte, DefaultRecvBufferSize)
        return &buf
    },
}

// GetRecvBufferPool returns the shared receive buffer pool.
func GetRecvBufferPool() *sync.Pool {
    return globalRecvBufferPool
}
```

**Why reuse the receiver pool?**
1. Same buffer size (1500 bytes MTU)
2. Maximum memory sharing - buffers can flow between send and receive paths
3. No additional pool management overhead
4. Already battle-tested for zero-copy operations

**Application Usage:**
```go
// Application gets buffer from shared pool
bufPtr := srt.GetRecvBufferPool().Get().(*[]byte)
buf := *bufPtr

// Application fills buffer with payload
copy(buf, payloadData)

// Application creates packet with buffer
// Buffer is returned to pool when packet is Decommissioned (after ACK)
```

**Note:** The pool is named "RecvBufferPool" but serves both send and receive. A future rename to `GetBufferPool()` could clarify this, but the functionality is correct as-is.

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

Following the receiver EventLoop pattern (`congestion/live/receive/tick.go:135-330`):

**File:** `congestion/live/send/eventloop.go` (new)

```go
package send

import (
    "context"
    "time"
)

// EventLoop is the main processing loop for lock-free sender mode.
// Single goroutine that:
// 1. Drains packets from SendPacketRing → SendPacketBtree (continuous)
// 2. Delivers packets when PktTsbpdTime <= now (continuous)
// 3. Drops old packets periodically (timer-based)
//
// Key design: Packet delivery runs EVERY iteration, not just on timer.
// This enables smooth, low-latency packet transmission rather than bursts.
// Timer-driven checks (drop) are handled via select, but delivery is continuous.
//
// TSBPD-Aware Sleep Optimization:
// Instead of generic adaptive backoff, we use the actual TSBPD timeline to
// determine exactly how long to sleep. deliverReadyPackets() returns the
// duration until the next packet is ready, allowing precise sleep timing.
// This is more efficient than both:
// - Pure adaptive backoff (might sleep too long, missing deadlines)
// - No sleep (100% CPU when idle)
func (s *sender) EventLoop(ctx context.Context) {
    // Drop check is timer-based (infrequent)
    dropTicker := time.NewTicker(100 * time.Millisecond)
    defer dropTicker.Stop()

    // Minimum sleep to prevent CPU spinning when btree is empty
    const minIdleSleep = 100 * time.Microsecond
    const maxIdleSleep = 1 * time.Millisecond

    for {
        s.metrics.SendEventLoopIterations.Add(1)

        // Handle timer-based operations via select with default
        select {
        case <-ctx.Done():
            return

        case <-dropTicker.C:
            // Timer-based: Drop packets past threshold
            s.metrics.SendEventLoopDropFires.Add(1)
            s.dropOldPackets()

        default:
            s.metrics.SendEventLoopDefaultRuns.Add(1)
            // Non-blocking - fall through to continuous processing
        }

        // =====================================================================
        // Continuous Processing (runs every iteration)
        // This is the key difference from timer-driven Tick():
        // - Packets are delivered as soon as their TSBPD time arrives
        // - Not waiting for next timer tick (which could be 10ms away!)
        // =====================================================================

        // 1. Drain ring → btree (get new packets into ordered storage)
        drained := s.drainRingToBtree()
        s.metrics.SendEventLoopDataDrained.Add(uint64(drained))

        // 2. Deliver ready packets (TSBPD time reached)
        //    Returns: delivered count AND duration until next packet is ready
        delivered, nextDeliveryIn := s.deliverReadyPackets()
        s.metrics.SendDeliveryPackets.Add(uint64(delivered))

        // TSBPD-Aware Sleep (extracted to separate function for testability)
        if drained == 0 && delivered == 0 {
            s.tsbpdAwareSleep(nextDeliveryIn, minIdleSleep, maxIdleSleep)
        }
        // Note: No need to track "activity" - TSBPD timeline drives everything
    }
}

// ═══════════════════════════════════════════════════════════════════════════
// TSBPD-Aware Sleep Functions (split for testability)
//
// The logic is split into two functions:
// 1. calculateTsbpdSleepDuration() - Pure calculation, updates metrics, no sleep
// 2. tsbpdAwareSleep() - Calls calculate + actually sleeps
//
// This allows unit tests to verify sleep calculations without blocking.
// ═══════════════════════════════════════════════════════════════════════════

// tsbpdSleepResult contains the calculation result and what happened
type tsbpdSleepResult struct {
    Duration     time.Duration
    WasTsbpd     bool  // true if we had a packet to wait for
    WasEmpty     bool  // true if btree was empty
    ClampedMin   bool  // true if duration was clamped to minimum
    ClampedMax   bool  // true if duration was clamped to maximum
}

// calculateTsbpdSleepDuration computes the optimal sleep duration based on
// the TSBPD timeline. This is a pure calculation function - no side effects
// except metric updates. This makes it easily unit testable.
//
// Parameters:
//   - nextDeliveryIn: Duration until next packet's TSBPD time (0 if btree empty)
//   - minSleep: Minimum sleep duration (prevents busy-spinning)
//   - maxSleep: Maximum sleep duration (caps sleep when btree empty)
//
// Returns tsbpdSleepResult with the duration and flags indicating what happened.
func (s *sender) calculateTsbpdSleepDuration(
    nextDeliveryIn, minSleep, maxSleep time.Duration,
) tsbpdSleepResult {
    s.metrics.SendEventLoopIdleBackoffs.Add(1)

    result := tsbpdSleepResult{}

    if nextDeliveryIn > 0 {
        // We have a packet waiting - calculate sleep until it's ready
        // Use configured percentage of the duration to wake up slightly early
        // This accounts for scheduling jitter (default: 90% = wake up 10% early)
        result.WasTsbpd = true
        s.metrics.SendEventLoopTsbpdSleeps.Add(1)
        s.metrics.SendEventLoopNextDeliveryTotalUs.Add(uint64(nextDeliveryIn.Microseconds()))

        // tsbpdSleepFactor is configured in SendConfig (default: 0.9)
        // Lower values = wake earlier (more CPU, lower latency)
        // Higher values = sleep longer (less CPU, higher latency variance)
        result.Duration = time.Duration(float64(nextDeliveryIn) * s.tsbpdSleepFactor)

        // Clamp to reasonable bounds and track when clamping occurs
        if result.Duration < minSleep {
            result.ClampedMin = true
            s.metrics.SendEventLoopSleepClampedMin.Add(1)
            result.Duration = minSleep
        } else if result.Duration > maxSleep {
            result.ClampedMax = true
            s.metrics.SendEventLoopSleepClampedMax.Add(1)
            result.Duration = maxSleep
        }
    } else {
        // Btree empty - use max sleep (no packets waiting)
        result.WasEmpty = true
        s.metrics.SendEventLoopEmptyBtreeSleeps.Add(1)
        result.Duration = maxSleep
    }

    // Track total sleep time for efficiency analysis
    s.metrics.SendEventLoopSleepTotalUs.Add(uint64(result.Duration.Microseconds()))

    return result
}

// tsbpdAwareSleep calculates and executes the optimal sleep duration.
// This is the function called by EventLoop - it calls calculateTsbpdSleepDuration
// and then actually performs the sleep.
//
// Returns the sleep result for logging/debugging.
func (s *sender) tsbpdAwareSleep(
    nextDeliveryIn, minSleep, maxSleep time.Duration,
) tsbpdSleepResult {
    result := s.calculateTsbpdSleepDuration(nextDeliveryIn, minSleep, maxSleep)
    time.Sleep(result.Duration)
    return result
}
```

**TSBPD-Aware Sleep: Why This Is Better Than Adaptive Backoff**

The receiver uses adaptive backoff (doubling sleep duration when idle), but for the sender we have a better option: **TSBPD-aware sleep**.

**Comparison:**

| Approach | How It Works | Pros | Cons |
|----------|--------------|------|------|
| **No sleep** | Loop spins continuously | Lowest latency | 100% CPU when idle |
| **Adaptive backoff** | Double sleep each idle iteration | Reduces CPU | May sleep too long, miss deadlines |
| **TSBPD-aware sleep** | Sleep until next packet ready | Precise timing | Requires tracking next packet |

**Why TSBPD-aware is ideal for sender:**

```
Timeline:  ─────────────────────────────────────────────────────────►
                                                                time
Packets:   [pkt1]     [pkt2]          [pkt3]     [pkt4]
           ready      ready+2ms       ready+5ms  ready+8ms

Adaptive Backoff:
  Loop 1: Deliver pkt1, pkt2
  Loop 2: Nothing ready → sleep 10µs
  Loop 3: Nothing ready → sleep 20µs
  Loop 4: Nothing ready → sleep 40µs  ← Might MISS pkt3!
  Loop 5: pkt3 late...

TSBPD-Aware Sleep:
  Loop 1: Deliver pkt1, pkt2, nextDeliveryIn = 3ms
  Loop 2: Sleep 2.7ms (90% of 3ms)
  Loop 3: Deliver pkt3 ON TIME, nextDeliveryIn = 3ms
  Loop 4: Sleep 2.7ms
  Loop 5: Deliver pkt4 ON TIME
```

**Key insight:** We know EXACTLY when the next packet needs to be sent because the TSBPD timestamp is in the packet header. Using this information:
- We never sleep longer than necessary
- We wake up just before the next packet is due
- No guessing with exponential backoff

The 90% factor (`sleepDuration = time.Duration(float64(nextDeliveryIn) * 0.9)`) accounts for:
- OS scheduling jitter
- Time spent in other EventLoop operations
- Small clock drift

// drainRingToBtree moves packets from lock-free ring to btree.
// Single-threaded - no lock needed for btree operations.
// Returns count of packets drained for backoff decision.
func (s *sender) drainRingToBtree() int {
    const maxBatch = 64  // Process up to 64 packets per drain
    drained := 0

    for i := 0; i < maxBatch; i++ {
        p := s.sendPacketRing.TryPop()
        if p == nil {
            break
        }

        // Insert into btree (O(log n), single traversal via ReplaceOrInsert)
        inserted, duplicate := s.sendPacketBtree.Insert(p)
        if !inserted && duplicate != nil {
            // Duplicate packet - release the old one
            duplicate.Decommission()
            s.metrics.SendBtreeDuplicates.Add(1)
        }
        s.metrics.SendBtreeInserted.Add(1)
        drained++
    }

    s.metrics.SendRingDrained.Add(uint64(drained))
    return drained
}

// deliverReadyPackets sends packets whose TSBPD time has arrived.
// Scans from DeliveryStartPoint forward.
//
// Returns:
//   - delivered: count of packets delivered this iteration
//   - nextDeliveryIn: duration until the next packet is ready (0 if btree empty)
//
// The nextDeliveryIn return value enables TSBPD-aware sleep optimization:
// instead of generic adaptive backoff, the EventLoop can sleep precisely
// until the next packet's TSBPD time arrives.
//
// NOTE: In each EventLoop iteration, DeliveryStartPoint should only advance
// by a small number (typically 1-10 packets). The TSBPD schedule ensures
// packets become ready incrementally. A large jump indicates a problem.
func (s *sender) deliverReadyPackets() (delivered int, nextDeliveryIn time.Duration) {
    nowUs := uint64(time.Now().UnixMicro())
    deliveredCount := 0
    var nextPktTsbpdTime uint64 = 0

    // Start scan from last delivery point
    startSeq := s.deliveryStartSeq.Load()

    s.sendPacketBtree.IterateFrom(startSeq, func(p packet.Packet) bool {
        pktTsbpdTime := p.Header().PktTsbpdTime

        if pktTsbpdTime > nowUs {
            // Not ready yet - record when it WILL be ready, then stop scanning
            nextPktTsbpdTime = pktTsbpdTime
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

    // Calculate duration until next packet is ready
    // Returns 0 if btree is empty (no more packets waiting)
    var nextIn time.Duration
    if nextPktTsbpdTime > 0 {
        // Convert microseconds difference to time.Duration
        nextIn = time.Duration(nextPktTsbpdTime-nowUs) * time.Microsecond
    }

    return deliveredCount, nextIn
}

// dropOldPackets removes packets past the drop threshold.
//
// CRITICAL: Must guard against uint64 underflow when nowUs < dropThreshold!
// Without this, threshold wraps to ~18.4e18 at startup, dropping ALL packets.
// See implementation in eventloop.go for the fixed version with the guard.
func (s *sender) dropOldPackets() {
    nowUs := s.nowFn()  // Use relative time (same base as PktTsbpdTime)

    // Guard against uint64 underflow at startup
    if nowUs < s.dropThreshold {
        return // Too early - no packets can be old enough to drop
    }
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

### 7.4 CRITICAL: Control Packet Routing to EventLoop

**Why This Section is Critical:**

Without proper control packet routing, the lockless sender design **cannot work**. There are **multiple race conditions** that can occur when control packets are processed directly:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                  MULTIPLE RACE CONDITIONS (Without Control Ring)             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  io_uring Completion Handler           Sender EventLoop                     │
│  (different goroutine)                 (single goroutine)                   │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│  RACE 1: ACK Delete vs EventLoop Insert                                     │
│  ═══════════════════════════════════════════════════════════════════════    │
│       ┌──────────────┐                      ┌──────────────┐                │
│       │ ACK arrives  │                      │ drainRing    │                │
│       │ handleACK()  │                      │ ToBtree()    │                │
│       │     ↓        │                      │     ↓        │                │
│       │ processACK() │                      │ btree.Insert │                │
│       │     ↓        │                      │              │                │
│       │ btree.Delete │────► RACE! ◄────────│              │                │
│       └──────────────┘                      └──────────────┘                │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│  RACE 2: NAK Traversal vs EventLoop Insert                                  │
│  ═══════════════════════════════════════════════════════════════════════    │
│       ┌──────────────┐                      ┌──────────────┐                │
│       │ NAK arrives  │                      │ drainRing    │                │
│       │ handleNAK()  │                      │ ToBtree()    │                │
│       │     ↓        │                      │     ↓        │                │
│       │ processNAK() │                      │ btree.Insert │                │
│       │     ↓        │                      │ (modifies    │                │
│       │ btree.Get()  │────► RACE! ◄────────│  tree nodes) │                │
│       │ (traversing) │                      │              │                │
│       └──────────────┘                      └──────────────┘                │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│  RACE 3: NAK Traversal vs ACK Delete                                        │
│  ═══════════════════════════════════════════════════════════════════════    │
│       ┌──────────────┐                      ┌──────────────┐                │
│       │ NAK arrives  │                      │ ACK arrives  │                │
│       │ handleNAK()  │                      │ handleACK()  │                │
│       │     ↓        │                      │     ↓        │                │
│       │ processNAK() │                      │ processACK() │                │
│       │     ↓        │                      │     ↓        │                │
│       │ btree.Get()  │────► RACE! ◄────────│ btree.Delete │                │
│       │ (might read  │                      │ (deleting    │                │
│       │  deleted pkt)│                      │  same seq?)  │                │
│       └──────────────┘                      └──────────────┘                │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│  RACE 4: NAK Traversal vs EventLoop Drop                                    │
│  ═══════════════════════════════════════════════════════════════════════    │
│       ┌──────────────┐                      ┌──────────────┐                │
│       │ NAK arrives  │                      │ dropOld      │                │
│       │ handleNAK()  │                      │ Packets()    │                │
│       │     ↓        │                      │     ↓        │                │
│       │ processNAK() │                      │ btree.Delete │                │
│       │     ↓        │                      │ Min() loop   │                │
│       │ btree iter   │────► RACE! ◄────────│              │                │
│       └──────────────┘                      └──────────────┘                │
│                                                                             │
│  ═══════════════════════════════════════════════════════════════════════    │
│  RACE 5: Retransmit Header Update vs EventLoop Read                         │
│  ═══════════════════════════════════════════════════════════════════════    │
│       ┌──────────────┐                      ┌──────────────┐                │
│       │ processNAK() │                      │ deliverReady │                │
│       │     ↓        │                      │ Packets()    │                │
│       │ p.Header().  │                      │     ↓        │                │
│       │ LastRetrans  │────► RACE! ◄────────│ p.Header().  │                │
│       │ mitTimeUs =  │                      │ PktTsbpdTime │                │
│       │ nowUs        │                      │ (same pkt?)  │                │
│       └──────────────┘                      └──────────────┘                │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Summary of Race Scenarios:**

| Race | Operation A | Operation B | Conflict |
|------|-------------|-------------|----------|
| 1 | ACK Delete | EventLoop Insert | Concurrent btree modification |
| 2 | NAK Get/Traverse | EventLoop Insert | Read during tree restructure |
| 3 | NAK Get | ACK Delete | Read deleted/invalid node |
| 4 | NAK Traverse | Drop DeleteMin | Iterator invalidation |
| 5 | NAK Header Write | Delivery Header Read | Packet header data race |

**Solution:** Route control packets through a lock-free ring, so the **EventLoop is the ONLY goroutine** that accesses `SendPacketBtree`. This eliminates ALL race conditions by design.

This follows the design in `retransmission_and_nak_suppression_design.md` Section 4.4 "Future Enhancement: Lock-Free Control Packet Ring".

#### 7.4.1 Sender Control Packet Ring

**File:** `congestion/live/send/control_ring.go` (new)

```go
package send

import (
    "github.com/randomizedcoder/gosrt/lockfree"
    "github.com/randomizedcoder/gosrt/packet"
)

// ControlPacketRing buffers incoming control packets for EventLoop processing.
// Same pattern as receiver's data packet ring.
type ControlPacketRing struct {
    ring *lockfree.Ring[packet.Packet]
}

// NewControlPacketRing creates a ring for control packet buffering.
// Size is smaller than data ring since control packets are fewer.
func NewControlPacketRing(size, shards int) *ControlPacketRing {
    return &ControlPacketRing{
        ring: lockfree.NewRing[packet.Packet](size, shards),
    }
}

// Push adds a control packet to the ring.
// Called from io_uring completion handler goroutine.
func (r *ControlPacketRing) Push(p packet.Packet) bool {
    return r.ring.Write(p)
}

// TryPop retrieves a control packet from the ring.
// Called from EventLoop (single consumer).
func (r *ControlPacketRing) TryPop() (packet.Packet, bool) {
    return r.ring.TryRead()
}
```

#### 7.4.2 Updated Sender Struct

**File:** `congestion/live/send/sender.go`

```go
type sender struct {
    // ... existing fields ...

    // Control packet ring (for EventLoop mode)
    controlPacketRing *ControlPacketRing
    useControlRing    bool

    // Function dispatch for control packet handling
    handleACKFn func(p packet.Packet)  // pushToControlRing or handleACKDirect
    handleNAKFn func(p packet.Packet)  // pushToControlRing or handleNAKDirect
}
```

#### 7.4.3 Connection-Level Control Packet Routing

**File:** `connection_handlers.go` (modify)

The key change is routing sender-destined control packets through the sender's control ring:

```go
// handleACK routes ACK packets to sender.
// In EventLoop mode: pushes to sender's control ring.
// In Tick mode: processes directly with lock.
func (c *srtConn) handleACK(p packet.Packet) {
    // ... existing ACK parsing ...

    // Route to sender
    c.snd.HandleACK(p)  // Sender decides: ring or direct
}

// handleNAK routes NAK packets to sender.
func (c *srtConn) handleNAK(p packet.Packet) {
    // ... existing NAK parsing ...

    // Route to sender
    c.snd.HandleNAK(p)  // Sender decides: ring or direct
}
```

**Sender-side routing:**

```go
// HandleACK routes ACK to appropriate processing path.
// EventLoop mode: pushes to control ring for EventLoop to process.
// Tick mode: processes immediately with lock.
func (s *sender) HandleACK(p packet.Packet) {
    s.handleACKFn(p)  // Function dispatch
}

func (s *sender) HandleNAK(p packet.Packet) {
    s.handleNAKFn(p)  // Function dispatch
}

// pushACKToControlRing buffers ACK for EventLoop processing.
func (s *sender) pushACKToControlRing(p packet.Packet) {
    if !s.controlPacketRing.Push(p) {
        // Ring full - process directly (fallback)
        s.metrics.ControlRingDropsACK.Add(1)
        s.handleACKDirect(p)
    }
}

// pushNAKToControlRing buffers NAK for EventLoop processing.
func (s *sender) pushNAKToControlRing(p packet.Packet) {
    if !s.controlPacketRing.Push(p) {
        // Ring full - process directly (fallback)
        s.metrics.ControlRingDropsNAK.Add(1)
        s.handleNAKDirect(p)
    }
}

// handleACKDirect processes ACK with lock (Tick mode).
func (s *sender) handleACKDirect(p packet.Packet) {
    s.lock.Lock()
    defer s.lock.Unlock()
    s.processACK(p.Header().TypeSpecific)  // ACK sequence from TypeSpecific field
}

// handleNAKDirect processes NAK with lock (Tick mode).
func (s *sender) handleNAKDirect(p packet.Packet) {
    s.lock.Lock()
    defer s.lock.Unlock()
    list := parseNAKList(p)  // Parse NAK packet into sequence list
    s.processNAK(list)
}
```

#### 7.4.4 Updated EventLoop with Control Processing

```go
func (s *sender) EventLoop(ctx context.Context) {
    backoff := newAdaptiveBackoff(s.metrics, ...)
    dropTicker := time.NewTicker(100 * time.Millisecond)
    defer dropTicker.Stop()

    for {
        s.metrics.SendEventLoopRuns.Add(1)

        select {
        case <-ctx.Done():
            return
        case <-dropTicker.C:
            s.dropOldPackets()
        default:
        }

        // =====================================================================
        // CRITICAL: Process control packets FIRST
        // This ensures ACK/NAK are handled before new data is added to btree.
        // =====================================================================
        controlProcessed := s.processControlPacketsDelta()

        // Process data packets
        drained := s.drainRingToBtree()
        delivered := s.deliverReadyPackets()

        // Adaptive backoff
        if controlProcessed == 0 && drained == 0 && delivered == 0 {
            s.metrics.SendEventLoopIdleBackoffs.Add(1)
            time.Sleep(backoff.getSleepDuration())
        } else {
            backoff.recordActivity()
        }
    }
}

// processControlPacketsDelta processes all accumulated control packets.
// Single atomic update for batch (O(1) vs O(n) per-packet updates).
// See retransmission_and_nak_suppression_design.md Section 4.4.6.
func (s *sender) processControlPacketsDelta() int {
    if s.controlPacketRing == nil {
        return 0
    }

    const maxBatch = 50  // Cap to prevent starvation of data processing
    count := 0

    for i := 0; i < maxBatch; i++ {
        p, ok := s.controlPacketRing.TryPop()
        if !ok {
            break  // Ring empty
        }

        // Dispatch based on control type
        switch p.Header().ControlType {
        case packet.CTRLTYPE_ACK:
            ackSeq := p.Header().TypeSpecific  // ACK sequence number
            s.processACK(ackSeq)
        case packet.CTRLTYPE_NAK:
            list := parseNAKList(p)
            s.processNAK(list)
        // Other control types handled at connection level (ACKACK, Keepalive, etc.)
        }

        count++
    }

    if count > 0 {
        s.metrics.ControlRingPacketsProcessed.Add(uint64(count))
    }

    return count
}
```

#### 7.4.5 Function Dispatch Configuration

```go
func NewSender(sendConfig SendConfig) congestion.Sender {
    s := &sender{
        // ... existing initialization ...
    }

    if sendConfig.UseSendRing {
        // EventLoop mode: route control packets through ring
        s.controlPacketRing = NewControlPacketRing(
            sendConfig.ControlRingSize,    // Default: 256
            sendConfig.ControlRingShards,  // Default: 2
        )
        s.useControlRing = true
        s.handleACKFn = s.pushACKToControlRing
        s.handleNAKFn = s.pushNAKToControlRing

        // Data packet functions (lock-free)
        s.pushFn = s.push
        s.processACKFn = s.processACK
        s.processNAKFn = s.processNAK
    } else {
        // Tick mode: process control packets directly with lock
        s.useControlRing = false
        s.handleACKFn = s.handleACKDirect
        s.handleNAKFn = s.handleNAKDirect

        // Data packet functions (locked)
        s.pushFn = s.pushLocking
        s.processACKFn = s.processACKLocking
        s.processNAKFn = s.processNAKLocking
    }

    return s
}
```

#### 7.4.6 Concurrency Safety Summary

With control packet routing, ALL access to `SendPacketBtree` is from the EventLoop:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    CORRECT ARCHITECTURE (With Control Ring)                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  io_uring Completion Handler           Sender EventLoop                     │
│  (PRODUCER goroutine)                  (SINGLE CONSUMER goroutine)          │
│                                                                             │
│       ┌──────────────┐                      ┌──────────────┐                │
│       │ ACK arrives  │                      │ 1. Process   │                │
│       │ handleACK()  │                      │    control   │                │
│       │     ↓        │                      │    packets   │                │
│       │ pushToRing() │──────► Control ──────│    (ACK/NAK) │                │
│       └──────────────┘        Ring          │     ↓        │                │
│                                             │ processACK() │                │
│       ┌──────────────┐                      │ processNAK() │                │
│       │ Push(p)      │                      │     ↓        │                │
│       │ pushToRing() │──────► Send ─────────│ 2. Drain     │                │
│       └──────────────┘        Ring          │    data ring │                │
│                                             │     ↓        │                │
│                                             │ 3. Deliver   │                │
│                                             │    packets   │                │
│                                             │     ↓        │                │
│                                             │ btree ops    │← SINGLE-THREAD │
│                                             │ (NO LOCKS!)  │   ACCESS       │
│                                             └──────────────┘                │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

| Operation | Goroutine | Via Ring? | Btree Access? |
|-----------|-----------|-----------|---------------|
| `Push(p)` | App/io_uring | Yes (Send Ring) | No (ring only) |
| `ACK received` | io_uring | Yes (Control Ring) | No (ring only) |
| `NAK received` | io_uring | Yes (Control Ring) | No (ring only) |
| `processACK()` | EventLoop | N/A (consumer) | Yes (DeleteBefore) |
| `processNAK()` | EventLoop | N/A (consumer) | Yes (Get) |
| `drainRingToBtree()` | EventLoop | N/A (consumer) | Yes (Insert) |
| `deliverReadyPackets()` | EventLoop | N/A (consumer) | Yes (Iterate) |
| `dropOldPackets()` | EventLoop | N/A (consumer) | Yes (DeleteMin) |

**All btree operations happen in the EventLoop goroutine → No locks needed!**

#### 7.4.7 New Config Options

```go
// config.go - Sender Control Ring options
type Config struct {
    // ... existing fields ...

    // Sender Control Ring (for lock-free sender)
    UseSendControlRing         bool          // Enable control packet ring for sender
    SenderControlRingSize      int           // Default: 256 (fewer control than data packets)
    SenderControlRingShards    int           // Default: 2 (less parallelism needed)
}

const (
    DefaultSenderControlRingSize   = 256
    DefaultSenderControlRingShards = 2
)
```

#### 7.4.8 Metrics for Sender Control Ring

```go
// metrics/metrics.go
ControlRingPacketsReceived  atomic.Uint64  // Control packets pushed to ring
ControlRingPacketsProcessed atomic.Uint64  // Control packets consumed by EventLoop
ControlRingDropsACK         atomic.Uint64  // ACK packets dropped (ring full, fallback)
ControlRingDropsNAK         atomic.Uint64  // NAK packets dropped (ring full, fallback)
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

    // Lock-free data ring buffer for sender (Push() path)
    UseSendRing   bool // Enable lock-free ring for Push() (default: false)
    SendRingSize  int  // Ring capacity (must be power of 2, default: 4096)

    // Lock-free control ring buffer (ACK/NAK path) - REQUIRED for lock-free sender!
    // Without this, ACK/NAK would be processed on io_uring goroutine,
    // causing race conditions with EventLoop. See Section 7.4.
    UseSendControlRing    bool // Enable control packet ring (default: false)
    SendControlRingSize   int  // Control ring capacity (default: 256)
    SendControlRingShards int  // Control ring shards (default: 2)

    // Sender btree configuration
    UseSendBtree    bool // Use btree instead of linked lists (default: false)
    SendBtreeDegree int  // B-tree degree (default: 32)

    // Sender event loop (requires BOTH UseSendRing AND UseSendControlRing)
    UseSendEventLoop       bool          // Enable sender event loop
    SendDeliveryIntervalMs int           // TSBPD delivery check interval (default: 1ms)
    SendDropIntervalMs     int           // Drop check interval (default: 100ms)

    // TSBPD-aware sleep configuration
    // The EventLoop sleeps for (nextDeliveryIn * SendTsbpdSleepFactor) to wake up
    // just before the next packet is ready. Lower values = earlier wake-up.
    // Range: 0.5 to 0.99. Default: 0.9 (wake up 10% early)
    SendTsbpdSleepFactor float64 // Sleep factor for TSBPD timing (default: 0.9)

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
    UseSendControlRing:     false,    // CRITICAL: must enable for EventLoop!
    SendControlRingSize:    256,      // Smaller than data ring
    SendControlRingShards:  2,        // Fewer shards needed
    UseSendBtree:           false,
    SendBtreeDegree:        32,
    UseSendEventLoop:       false,
    SendDeliveryIntervalMs: 1,
    SendDropIntervalMs:     100,
    SendTsbpdSleepFactor:   0.9,        // Wake up 10% early (accounts for jitter)
    UseSendPayloadPool:     false,
    SendPayloadSize:        1316,
}
```

### 10.3 Input Validation

```go
func (c *Config) Validate() error {
    // Sender data ring validation
    if c.UseSendRing {
        if c.SendRingSize < 64 {
            return fmt.Errorf("SendRingSize must be >= 64, got %d", c.SendRingSize)
        }
        if c.SendRingSize&(c.SendRingSize-1) != 0 {
            return fmt.Errorf("SendRingSize must be power of 2, got %d", c.SendRingSize)
        }
    }

    // Event loop requires data ring
    if c.UseSendEventLoop && !c.UseSendRing {
        return fmt.Errorf("UseSendEventLoop requires UseSendRing=true")
    }

    // ═══════════════════════════════════════════════════════════════════════
    // CRITICAL: Lock-free sender requires control ring!
    // Without control ring, ACK/NAK would be processed on io_uring goroutine,
    // causing race conditions with EventLoop's btree access.
    // See Section 7.4 for detailed race analysis.
    // ═══════════════════════════════════════════════════════════════════════
    if c.UseSendEventLoop && !c.UseSendControlRing {
        return fmt.Errorf("UseSendEventLoop requires UseSendControlRing=true (see Section 7.4)")
    }

    // Control ring validation
    if c.UseSendControlRing {
        if c.SendControlRingSize < 32 {
            return fmt.Errorf("SendControlRingSize must be >= 32, got %d", c.SendControlRingSize)
        }
        if c.SendControlRingSize&(c.SendControlRingSize-1) != 0 {
            return fmt.Errorf("SendControlRingSize must be power of 2, got %d", c.SendControlRingSize)
        }
    }

    // Btree degree validation
    if c.SendBtreeDegree < 2 {
        c.SendBtreeDegree = 32  // Use default
    }

    // TSBPD sleep factor validation (see Implementation Note 5)
    // Range: 0.5-0.99 (lower = earlier wake, higher = longer sleep)
    if c.SendTsbpdSleepFactor == 0 {
        c.SendTsbpdSleepFactor = 0.9 // Default
    }
    if c.SendTsbpdSleepFactor < 0.5 || c.SendTsbpdSleepFactor > 0.99 {
        return fmt.Errorf("SendTsbpdSleepFactor must be 0.5-0.99, got %f", c.SendTsbpdSleepFactor)
    }

    return nil
}
```

### 10.4 CLI Flags

**File:** `contrib/common/flags.go`

```go
var (
    // Sender data ring flags
    UseSendRing = flag.Bool("usesendring", false,
        "Enable lock-free ring for sender Push() operations")
    SendRingSize = flag.Int("sendringsize", 4096,
        "Sender ring capacity (must be power of 2)")
    UseSendBtree = flag.Bool("usesendbtree", false,
        "Use btree for sender packet storage (O(log n) NAK lookup)")
    UseSendEventLoop = flag.Bool("usesendeventloop", false,
        "Enable sender event loop for smooth packet delivery (requires -usesendring)")
    SendTsbpdSleepFactor = flag.Float64("sendtsbpdsleepfactor", 0.9,
        "TSBPD sleep factor (0.5-0.99, lower=earlier wake, default: 0.9)")

    // Sender control ring flags (REQUIRED for lock-free sender - see Section 7.4)
    UseSendControlRing = flag.Bool("usesendcontrolring", false,
        "Enable control packet ring for sender (routes ACK/NAK through EventLoop)")
    SendControlRingSize = flag.Int("sendcontrolringsize", 256,
        "Sender control ring capacity (default: 256)")
    SendControlRingShards = flag.Int("sendcontrolringshards", 2,
        "Sender control ring shards (default: 2)")
)

// ApplyFlagsToConfig updates config with CLI flag values
func ApplyFlagsToConfig(c *gosrt.Config) {
    // ... existing flag applications ...

    // Sender data ring flags
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
    if *SendTsbpdSleepFactor != 0.9 {
        c.SendTsbpdSleepFactor = *SendTsbpdSleepFactor
    }

    // Sender control ring flags
    if *UseSendControlRing {
        c.UseSendControlRing = true
    }
    if *SendControlRingSize != 256 {
        c.SendControlRingSize = *SendControlRingSize
    }
    if *SendControlRingShards != 2 {
        c.SendControlRingShards = *SendControlRingShards
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
    // SENDER TICK METRICS (Baseline mode - for burst detection comparison)
    // These metrics enable comparison of burst behavior between Tick and EventLoop
    // ═══════════════════════════════════════════════════════════════════════

    SendTickRuns             atomic.Uint64 // Number of Tick() invocations
    SendTickDeliveredPackets atomic.Uint64 // Packets delivered in Tick mode

    // ═══════════════════════════════════════════════════════════════════════
    // SENDER LOCKLESS METRICS
    // ═══════════════════════════════════════════════════════════════════════

    // Data ring buffer metrics
    SendRingPushed     atomic.Uint64 // Packets pushed to ring
    SendRingDropped    atomic.Uint64 // Packets dropped (ring full)
    SendRingDrained    atomic.Uint64 // Packets drained to btree

    // Control ring buffer metrics (CRITICAL for lockless sender - Section 7.4)
    SendControlRingPacketsReceived  atomic.Uint64 // Control packets pushed to ring
    SendControlRingPacketsProcessed atomic.Uint64 // Control packets consumed by EventLoop
    SendControlRingDropsACK         atomic.Uint64 // ACK packets dropped (ring full, fallback)
    SendControlRingDropsNAK         atomic.Uint64 // NAK packets dropped (ring full, fallback)

    // Btree metrics
    SendBtreeInserted   atomic.Uint64 // Packets inserted to btree
    SendBtreeDuplicates atomic.Uint64 // Duplicate packets detected
    SendBtreeLen        atomic.Uint64 // Current btree size

    // ═══════════════════════════════════════════════════════════════════════
    // SENDER EVENTLOOP METRICS (parity with receiver EventLoop)
    // Reference: receiver has EventLoopIterations, EventLoopFullACKFires, etc.
    // See Phase 8.3 for detailed documentation and diagnostics guide.
    // ═══════════════════════════════════════════════════════════════════════

    // Core loop metrics
    SendEventLoopIterations      atomic.Uint64 // Total loop iterations
    SendEventLoopDropFires       atomic.Uint64 // Times drop check ticker fired
    SendEventLoopDefaultRuns     atomic.Uint64 // Times default (non-blocking) case ran
    SendEventLoopIdleBackoffs    atomic.Uint64 // Times idle sleep triggered (total)

    // TSBPD-Aware Sleep metrics (key observability for smooth delivery)
    SendEventLoopTsbpdSleeps       atomic.Uint64 // Times TSBPD-aware sleep used (precise)
    SendEventLoopEmptyBtreeSleeps  atomic.Uint64 // Times btree empty (max sleep)
    SendEventLoopSleepClampedMin   atomic.Uint64 // Times sleep clamped to min (too short)
    SendEventLoopSleepClampedMax   atomic.Uint64 // Times sleep clamped to max (too long)
    SendEventLoopSleepTotalUs      atomic.Uint64 // Total microseconds spent sleeping
    SendEventLoopNextDeliveryTotalUs atomic.Uint64 // Sum of nextDeliveryIn values (for avg)

    // Ring drain metrics (per iteration visibility)
    SendEventLoopDataDrained    atomic.Uint64 // Data packets drained from ring
    SendEventLoopControlDrained atomic.Uint64 // Control packets drained from ring

    // Control packet processing metrics
    SendEventLoopACKsProcessed atomic.Uint64 // ACKs processed via control ring
    SendEventLoopNAKsProcessed atomic.Uint64 // NAKs processed via control ring

    // Delivery metrics
    SendDeliveryPackets atomic.Uint64 // Packets delivered (TSBPD time reached)

    // NAK performance metrics
    SendNakLookups      atomic.Uint64 // NAK btree lookups
    SendNakLookupHits   atomic.Uint64 // Successful NAK lookups
    SendNakLookupMisses atomic.Uint64 // NAK sequence not found

    // ACK performance metrics
    SendAckDeleteMinCalls   atomic.Uint64 // DeleteMin operations
    SendAckDeleteMinPackets atomic.Uint64 // Packets removed via ACK
}
```

### 11.2 Prometheus Handler

**File:** `metrics/handler.go`

```go
// Add to writeConnectionMetrics:

// ═══════════════════════════════════════════════════════════════════════════
// SENDER TICK METRICS (Baseline mode - for burst detection comparison)
// ═══════════════════════════════════════════════════════════════════════════
writeCounterIfNonZero(b, "gosrt_send_tick_runs_total", m.SendTickRuns.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_tick_delivered_packets_total", m.SendTickDeliveredPackets.Load(), socketLabel)

// ═══════════════════════════════════════════════════════════════════════════
// SENDER LOCKLESS METRICS
// ═══════════════════════════════════════════════════════════════════════════

// Sender data ring metrics
writeCounterIfNonZero(b, "gosrt_send_ring_pushed_total", m.SendRingPushed.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ring_dropped_total", m.SendRingDropped.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ring_drained_total", m.SendRingDrained.Load(), socketLabel)

// Sender control ring metrics (Section 7.4 - lock-free sender requires this)
writeCounterIfNonZero(b, "gosrt_send_control_ring_received_total", m.SendControlRingPacketsReceived.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_control_ring_processed_total", m.SendControlRingPacketsProcessed.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_control_ring_drops_ack_total", m.SendControlRingDropsACK.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_control_ring_drops_nak_total", m.SendControlRingDropsNAK.Load(), socketLabel)

// Sender btree metrics
writeGaugeIfNonZero(b, "gosrt_send_btree_len", float64(m.SendBtreeLen.Load()), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_btree_inserted_total", m.SendBtreeInserted.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_btree_duplicates_total", m.SendBtreeDuplicates.Load(), socketLabel)

// Sender EventLoop metrics (parity with receiver - see Phase 8.3)
writeCounterIfNonZero(b, "gosrt_send_eventloop_iterations_total", m.SendEventLoopIterations.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_drop_fires_total", m.SendEventLoopDropFires.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_default_runs_total", m.SendEventLoopDefaultRuns.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_idle_backoffs_total", m.SendEventLoopIdleBackoffs.Load(), socketLabel)

// TSBPD-Aware Sleep metrics (key observability for smooth delivery)
writeCounterIfNonZero(b, "gosrt_send_eventloop_tsbpd_sleeps_total", m.SendEventLoopTsbpdSleeps.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_empty_btree_sleeps_total", m.SendEventLoopEmptyBtreeSleeps.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_sleep_clamped_min_total", m.SendEventLoopSleepClampedMin.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_sleep_clamped_max_total", m.SendEventLoopSleepClampedMax.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_sleep_total_us", m.SendEventLoopSleepTotalUs.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_next_delivery_total_us", m.SendEventLoopNextDeliveryTotalUs.Load(), socketLabel)

// Sender EventLoop drain metrics (visibility into ring processing)
writeCounterIfNonZero(b, "gosrt_send_eventloop_data_drained_total", m.SendEventLoopDataDrained.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_control_drained_total", m.SendEventLoopControlDrained.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_acks_processed_total", m.SendEventLoopACKsProcessed.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_naks_processed_total", m.SendEventLoopNAKsProcessed.Load(), socketLabel)

// Sender delivery metrics
writeCounterIfNonZero(b, "gosrt_send_delivery_packets_total", m.SendDeliveryPackets.Load(), socketLabel)

// NAK performance
writeCounterIfNonZero(b, "gosrt_send_nak_lookups_total", m.SendNakLookups.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_nak_lookup_hits_total", m.SendNakLookupHits.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_nak_lookup_misses_total", m.SendNakLookupMisses.Load(), socketLabel)

// ACK performance
writeCounterIfNonZero(b, "gosrt_send_ack_deletemin_calls_total", m.SendAckDeleteMinCalls.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_ack_deletemin_packets_total", m.SendAckDeleteMinPackets.Load(), socketLabel)
```

### 11.3 Baseline Tick Instrumentation

To enable burst detection comparison, the existing sender `Tick()` function needs
instrumentation. This is required even for baseline mode to measure burst behavior.

**File:** `congestion/live/send/tick.go`

```go
func (s *sender) Tick(now uint64) {
    s.metrics.SendTickRuns.Add(1)  // NEW: Track tick invocations

    // ... existing Tick() code ...

    // In the packet delivery section:
    delivered := s.deliverPackets(now)
    s.metrics.SendTickDeliveredPackets.Add(uint64(delivered))  // NEW: Track packets per tick
}
```

**Note:** This instrumentation is minimal and adds only 2 atomic operations per Tick(),
which is negligible overhead compared to the Tick interval (typically 10-100ms).

### 11.4 Burst Detection Metric Derivation

The burst detection metric is **not** a direct Prometheus metric, but a derived
calculation performed by the parallel comparison tool during analysis:

```
Packets/Iteration = DeliveredPackets / Iterations
```

| Mode | Metric Sources | Typical Ratio |
|------|----------------|---------------|
| Baseline (Tick) | `SendTickDeliveredPackets / SendTickRuns` | 50-200 (bursty) |
| HighPerf (EventLoop) | `SendDeliveryPackets / SendEventLoopIterations` | 0.1-2.0 (smooth) |

**Interpretation:**
- **High ratio (>10):** Bursty delivery - many packets per iteration
- **Low ratio (<2):** Smooth delivery - packets spread across many iterations
- **Target:** HighPerf should show 10-100x lower ratio than Baseline

### 11.5 Metrics Audit

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

### Phase 3: Control Packet Ring (CRITICAL)

**Goal:** Route ACK/NAK through lock-free ring so EventLoop is single consumer of btree.

**Why this phase is CRITICAL:**
Without control packet routing, the sender cannot be truly lock-free. ACK/NAK arrive on io_uring completion handler (different goroutine) and would require locks to access the btree. See Section 7.4 for detailed design.

**Files to modify:**
| File | Changes |
|------|---------|
| `congestion/live/send/control_ring.go` | New file - control packet ring |
| `congestion/live/send/sender.go` | Add controlPacketRing, handleACKFn, handleNAKFn |
| `connection_handlers.go` | Route ACK/NAK through sender's HandleACK/HandleNAK |
| `config.go` | Add UseSendControlRing, SendControlRingSize, SendControlRingShards |
| `contrib/common/flags.go` | Add CLI flags |

**Tests:**
- `send_control_ring_test.go` - Ring unit tests
- `send_control_routing_test.go` - ACK/NAK routing tests

**Checkpoint:** `go test ./...` passes, `make test-flags` passes

### Phase 4: Sender EventLoop

**Goal:** Add continuous event loop that processes BOTH data ring AND control ring.

**Critical Design:** The EventLoop is the ONLY consumer that accesses `SendPacketBtree`:
1. Drain `SendPacketRing` → insert packets into btree
2. Drain `ControlPacketRing` → process ACKs (DeleteBefore) and NAKs (Get + retransmit)
3. Deliver ready packets (TSBPD time reached)
4. Drop old packets (threshold reached)

This ensures **single-threaded btree access** with **zero locks**.

**Files to modify:**
| File | Changes |
|------|---------|
| `congestion/live/send/eventloop.go` | New file - event loop with data + control processing |
| `congestion/live/send/sender.go` | Add UseEventLoop() method |
| `connection.go` | Start sender EventLoop if enabled |
| `config.go` | Add UseSendEventLoop |

**Tests:**
- `send_eventloop_test.go` - EventLoop unit tests (data AND control)
- `send_eventloop_race_test.go` - Race condition tests
- `send_eventloop_control_test.go` - Verify ACK/NAK processing via ring

**Checkpoint:** `go test -race ./...` passes

### Phase 5: Zero-Copy Payload Pool

**Goal:** Enable zero-copy buffer management for applications (reuse `globalRecvBufferPool`).

**Files to modify:**
| File | Changes |
|------|---------|
| `buffers.go` | Verify sender can use `globalRecvBufferPool` |
| `congestion/live/send/push.go` | Update to use buffer pool |
| `contrib/client-generator/main.go` | Update to acquire buffers from pool |

**Tests:**
- `send_buffer_pool_test.go` - Verify buffer reuse
- `send_buffer_pool_bench_test.go` - Allocation benchmark

### Phase 6: Metrics and Observability

**Goal:** Add all metrics (including control ring metrics), ensure audit passes.

**Files to modify:**
| File | Changes |
|------|---------|
| `metrics/metrics.go` | Add sender lockless metrics (incl. control ring) |
| `metrics/handler.go` | Add Prometheus export |
| `metrics/handler_test.go` | Add metric tests |
| `contrib/integration_testing/parallel_analysis.go` | Add sender metrics category |

**Checkpoint:** `make audit-metrics` passes

### Phase 7: Integration Testing

**Goal:** Verify smooth delivery and performance improvement.

**Tests:**
- Add `Parallel-Sender-EventLoop-*` test configurations
- Compare burst behavior: Tick vs EventLoop
- Measure packet spacing on network

### Phase 8: Migration Path

**Goal:** Enable gradual rollout with feature flags and comprehensive observability.

#### 8.1 Feature Flag Hierarchy

The sender lockless features are enabled incrementally. Each level builds on the previous:

```
Level 0: Default (all disabled)
         └─ Tick mode, linked lists, locked operations

Level 1: UseSendBtree = true
         └─ Btree replaces linked lists (still Tick mode, still locked)
         └─ Benefit: O(log n) NAK lookup
         └─ Risk: Low (same concurrency model)

Level 2: UseSendRing = true (requires Level 1)
         └─ Lock-free ring for Push() operations
         └─ Benefit: Reduces Push() lock contention
         └─ Risk: Medium (new data path)

Level 3: UseSendControlRing = true (requires Level 2)
         └─ Lock-free ring for ACK/NAK control packets
         └─ Benefit: Routes control packets to EventLoop
         └─ Risk: Medium (new control path)

Level 4: UseSendEventLoop = true (requires Level 2 + Level 3)
         └─ Full lock-free sender
         └─ Benefit: Smooth delivery, zero locks
         └─ Risk: Higher (new execution model)
```

#### 8.2 Validation at Each Level

| Level | Validation Command | Expected Outcome |
|-------|-------------------|------------------|
| 1 | `go test ./congestion/live/send/...` | All btree tests pass |
| 2 | `make test-flags` | CLI flags work, ring metrics appear |
| 3 | `go test -race ./...` | No race conditions with control ring |
| 4 | `sudo make test-parallel CONFIG=Parallel-Sender-EventLoop` | Performance improvement |

#### 8.3 Sender EventLoop Metrics (Parity with Receiver)

The sender EventLoop should have equivalent observability to the receiver EventLoop. This allows operators to diagnose issues and compare behavior.

**Receiver EventLoop Metrics (reference from `metrics/metrics.go:351-361`):**

| Metric | Purpose |
|--------|---------|
| `EventLoopIterations` | Total loop iterations |
| `EventLoopFullACKFires` | Times fullACK ticker fired |
| `EventLoopNAKFires` | Times NAK ticker fired |
| `EventLoopRateFires` | Times rate calculation ticker fired |
| `EventLoopDefaultRuns` | Times default (non-blocking) case ran |
| `EventLoopIdleBackoffs` | Times adaptive backoff sleep triggered |

**Sender EventLoop Metrics (new - matching pattern):**

| Metric | Purpose |
|--------|---------|
| `SendEventLoopIterations` | Total loop iterations |
| `SendEventLoopDropFires` | Times drop check ticker fired |
| `SendEventLoopDefaultRuns` | Times default (non-blocking) case ran |
| `SendEventLoopIdleBackoffs` | Times idle sleep triggered (total) |
| `SendEventLoopDataDrained` | Packets drained from data ring per iteration |
| `SendEventLoopControlDrained` | Packets drained from control ring per iteration |
| `SendEventLoopACKsProcessed` | ACKs processed via control ring |
| `SendEventLoopNAKsProcessed` | NAKs processed via control ring |

**TSBPD-Aware Sleep Metrics (key observability):**

| Metric | Purpose |
|--------|---------|
| `SendEventLoopTsbpdSleeps` | Times TSBPD-aware sleep used (precise timing) |
| `SendEventLoopEmptyBtreeSleeps` | Times btree was empty (max sleep used) |
| `SendEventLoopSleepClampedMin` | Times sleep clamped to minimum (was too short) |
| `SendEventLoopSleepClampedMax` | Times sleep clamped to maximum (was too long) |
| `SendEventLoopSleepTotalUs` | Total microseconds spent sleeping |
| `SendEventLoopNextDeliveryTotalUs` | Sum of nextDeliveryIn values (for calculating average) |

**Derived Metrics (calculated in Prometheus/Grafana):**

| Calculation | Meaning |
|-------------|---------|
| `TsbpdSleeps / IdleBackoffs` | Ratio of precise sleeps (closer to 1.0 = better) |
| `SleepTotalUs / TsbpdSleeps` | Average TSBPD sleep duration in µs |
| `NextDeliveryTotalUs / TsbpdSleeps` | Average time until next packet |
| `SleepClampedMin / TsbpdSleeps` | Ratio of under-clamps (high = packets too close together) |
| `SleepClampedMax / TsbpdSleeps` | Ratio of over-clamps (high = packets too spread out) |

**File:** `metrics/metrics.go` additions:

```go
// Sender EventLoop Metrics (parity with receiver EventLoop)
// See receiver: EventLoopIterations, EventLoopFullACKFires, etc.
SendEventLoopIterations    atomic.Uint64 // Total loop iterations
SendEventLoopDeliveryFires atomic.Uint64 // Times delivery ticker fired
SendEventLoopDropFires     atomic.Uint64 // Times drop check ticker fired
SendEventLoopDefaultRuns   atomic.Uint64 // Times default case ran
SendEventLoopIdleBackoffs  atomic.Uint64 // Times idle backoff triggered
SendEventLoopDataDrained   atomic.Uint64 // Data packets drained from ring
SendEventLoopControlDrained atomic.Uint64 // Control packets drained from ring
SendEventLoopACKsProcessed atomic.Uint64 // ACKs processed via control ring
SendEventLoopNAKsProcessed atomic.Uint64 // NAKs processed via control ring
```

**EventLoop code with metrics:**

```go
// congestion/live/send/eventloop.go
func (s *sender) EventLoop(ctx context.Context) {
    deliveryTicker := time.NewTicker(time.Duration(s.config.SendDeliveryIntervalMs) * time.Millisecond)
    dropTicker := time.NewTicker(time.Duration(s.config.SendDropIntervalMs) * time.Millisecond)
    defer deliveryTicker.Stop()
    defer dropTicker.Stop()

    backoff := newAdaptiveBackoff(s.backoffMinSleep, s.backoffMaxSleep, s.backoffColdStartPkts)

    for {
        s.metrics.SendEventLoopIterations.Add(1)

        select {
        case <-ctx.Done():
            return

        case <-deliveryTicker.C:
            s.metrics.SendEventLoopDeliveryFires.Add(1)
            // Drain data ring → btree
            drained := s.drainDataRingToBtree()
            s.metrics.SendEventLoopDataDrained.Add(uint64(drained))

        case <-dropTicker.C:
            s.metrics.SendEventLoopDropFires.Add(1)
            s.dropOldPackets()

        default:
            s.metrics.SendEventLoopDefaultRuns.Add(1)
        }

        // ═══════════════════════════════════════════════════════════════════
        // CRITICAL: Process control ring EVERY iteration
        // This ensures ACK/NAK are processed with minimal latency
        // ═══════════════════════════════════════════════════════════════════
        controlDrained := s.processControlPacketsDelta()
        s.metrics.SendEventLoopControlDrained.Add(uint64(controlDrained))

        // Deliver ready packets (TSBPD time reached)
        delivered := s.deliverReadyPackets()

        // Adaptive backoff when idle (prevents CPU spin)
        if delivered == 0 && controlDrained == 0 {
            s.metrics.SendEventLoopIdleBackoffs.Add(1)
            time.Sleep(backoff.getSleepDuration())
        } else {
            backoff.recordActivity()
        }
    }
}
```

#### 8.4 Prometheus Export for Sender EventLoop

**File:** `metrics/handler.go` additions:

```go
// Sender EventLoop metrics (parity with receiver)
writeCounterIfNonZero(b, "gosrt_send_eventloop_iterations_total",
    m.SendEventLoopIterations.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_delivery_fires_total",
    m.SendEventLoopDeliveryFires.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_drop_fires_total",
    m.SendEventLoopDropFires.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_default_runs_total",
    m.SendEventLoopDefaultRuns.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_idle_backoffs_total",
    m.SendEventLoopIdleBackoffs.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_data_drained_total",
    m.SendEventLoopDataDrained.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_control_drained_total",
    m.SendEventLoopControlDrained.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_acks_processed_total",
    m.SendEventLoopACKsProcessed.Load(), socketLabel)
writeCounterIfNonZero(b, "gosrt_send_eventloop_naks_processed_total",
    m.SendEventLoopNAKsProcessed.Load(), socketLabel)
```

#### 8.5 Diagnostics via Metrics

The metrics enable diagnosing common issues:

| Symptom | Metric to Check | Interpretation |
|---------|-----------------|----------------|
| High CPU usage | `SendEventLoopIdleBackoffs` low | Loop not sleeping, spinning |
| High CPU + idle | `SendEventLoopEmptyBtreeSleeps` high | Btree empty, sleeping max duration |
| Packet bursting | `SendEventLoopTsbpdSleeps` low | Not using TSBPD timing |
| Missed deadlines | `SleepClampedMin` high | Packets arriving faster than min sleep |
| Wide packet gaps | `SleepClampedMax` high | Packets spaced > 1ms apart |
| ACK/NAK backlog | `SendEventLoopControlDrained` high | Control ring not draining fast enough |
| Ring overflow | `SendRingDropped` non-zero | Ring capacity too small |
| Slow NAK response | `SendEventLoopNAKsProcessed` vs `NakRecvTotal` | NAKs queuing in ring |

**TSBPD Sleep Efficiency Analysis:**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ TSBPD Sleep Metrics Dashboard                                               │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Prometheus Queries for Grafana:                                            │
│                                                                             │
│  1. Sleep Type Distribution (pie chart):                                    │
│     - gosrt_send_eventloop_tsbpd_sleeps_total                               │
│     - gosrt_send_eventloop_empty_btree_sleeps_total                         │
│                                                                             │
│  2. TSBPD Efficiency Ratio (single stat, higher=better):                    │
│     rate(gosrt_send_eventloop_tsbpd_sleeps_total[1m]) /                     │
│     rate(gosrt_send_eventloop_idle_backoffs_total[1m])                      │
│                                                                             │
│  3. Average Sleep Duration (µs, should be stable):                          │
│     rate(gosrt_send_eventloop_sleep_total_us[1m]) /                         │
│     rate(gosrt_send_eventloop_idle_backoffs_total[1m])                      │
│                                                                             │
│  4. Average Next Delivery Time (µs, packet spacing):                        │
│     rate(gosrt_send_eventloop_next_delivery_total_us[1m]) /                 │
│     rate(gosrt_send_eventloop_tsbpd_sleeps_total[1m])                       │
│                                                                             │
│  5. Clamp Ratio (should be low):                                            │
│     (rate(gosrt_send_eventloop_sleep_clamped_min_total[1m]) +               │
│      rate(gosrt_send_eventloop_sleep_clamped_max_total[1m])) /              │
│     rate(gosrt_send_eventloop_tsbpd_sleeps_total[1m])                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Interpreting TSBPD Sleep Metrics:**

| Metric Ratio | Value | Interpretation |
|--------------|-------|----------------|
| `TsbpdSleeps / IdleBackoffs` | ~1.0 | Excellent: Always precise timing |
| `TsbpdSleeps / IdleBackoffs` | ~0.5 | Good: Mix of precise and empty |
| `TsbpdSleeps / IdleBackoffs` | ~0.0 | Problem: Btree always empty |
| `SleepClampedMin / TsbpdSleeps` | >0.1 | Warning: Packets too close (increase min sleep or bitrate issue) |
| `SleepClampedMax / TsbpdSleeps` | >0.5 | Warning: Packets too spread out (normal for low bitrate) |
| `SleepTotalUs / TsbpdSleeps` | ~500µs | Typical for 20 Mbps stream |
| `NextDeliveryTotalUs / TsbpdSleeps` | ~550µs | Slightly higher than sleep (90% factor) |

#### 8.6 A/B Testing Configuration

Create parallel test configs for comparison:

**File:** `contrib/integration_testing/network/configs/Parallel-Sender-Tick-vs-EventLoop.sh`

```bash
#!/bin/bash
# Compare Tick-based sender vs EventLoop sender

TEST_NAME="Parallel-Sender-Tick-vs-EventLoop"
TEST_DURATION="90s"
DATA_RATE="20000000"  # 20 Mbps
LATENCY_MS="60"
LOSS_PERCENT="5"

# Baseline: Default Tick mode
BASELINE_FLAGS=""

# HighPerf: Full lockless sender
HIGHPERF_FLAGS="-usesendbtree -usesendring -usesendcontrolring -usesendeventloop"

export TEST_NAME TEST_DURATION DATA_RATE LATENCY_MS LOSS_PERCENT
export BASELINE_FLAGS HIGHPERF_FLAGS
```

#### 8.7 Rollback Procedures

| Issue | Symptom | Rollback |
|-------|---------|----------|
| EventLoop CPU spin | `SendEventLoopIdleBackoffs = 0`, high CPU | Set `UseSendEventLoop = false` |
| Control ring overflow | `SendControlRingDropsACK > 0` | Increase `SendControlRingSize` or disable |
| Data ring overflow | `SendRingDropped > 0` | Increase `SendRingSize` or disable |
| Btree performance | NAK lookup still slow | Check `SendNakLookupHits` vs `SendNakLookupMisses` |
| Race condition | `-race` test fails | Disable `UseSendEventLoop`, investigate |

#### 8.8 Production Rollout Checklist

```markdown
□ Phase 1: Enable btree only (UseSendBtree = true)
  □ Deploy to canary
  □ Monitor NAK lookup performance
  □ Compare `SendNakLookupHits` to baseline
  □ Run for 24+ hours

□ Phase 2: Enable data ring (UseSendRing = true)
  □ Deploy to canary
  □ Monitor `SendRingPushed`, `SendRingDropped`
  □ Verify no dropped packets
  □ Run for 24+ hours

□ Phase 3: Enable control ring (UseSendControlRing = true)
  □ Deploy to canary
  □ Monitor `SendControlRingPacketsReceived`, `SendControlRingDropsACK/NAK`
  □ Verify ACK/NAK processing via metrics
  □ Run for 24+ hours

□ Phase 4: Enable EventLoop (UseSendEventLoop = true)
  □ Deploy to canary
  □ Monitor all EventLoop metrics
  □ Compare packet spacing (should be smoother)
  □ Verify CPU usage acceptable
  □ Run for 7+ days before wider rollout
```

#### 8.9 Config Variants for Integration Test Matrix

Following the patterns in [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md), we add new config variants for sender lockless features.

**Existing Receiver Config Variants (reference):**

| Abbrev | Full Name | Features Enabled |
|--------|-----------|------------------|
| `Ring` | Ring buffer only | list + lock-free ring buffer |
| `FullRing` | Full Phase 3 | btree + io_uring + NAK btree + Ring |
| `EventLoop` | Event loop only | Ring + continuous event loop |
| `FullEL` | Full Phase 4 | btree + io_uring + NAK btree + Ring + EventLoop |

**New Sender Config Variants:**

| Abbrev | Full Name | Sender Features Enabled |
|--------|-----------|------------------------|
| `SendBtree` | Sender btree only | Sender btree (list packet store), no rings |
| `SendRing` | Sender ring only | Sender btree + data ring (no control ring, no EventLoop) |
| `SendCRing` | Sender control ring | Sender btree + data ring + control ring (no EventLoop) |
| `SendEL` | Sender EventLoop | Sender btree + data ring + control ring + EventLoop |
| `FullSendEL` | Full Sender Phase 4 | All receiver features + all sender features |

**Config Variant Implementation:**

Add to `contrib/integration_testing/test_configs.go`:

```go
// ConfigVariant additions for sender lockless
const (
    // Sender-specific variants (sender locked, receiver varies)
    ConfigSendBtree  ConfigVariant = "SendBtree"   // Sender btree only
    ConfigSendRing   ConfigVariant = "SendRing"    // Sender btree + data ring
    ConfigSendCRing  ConfigVariant = "SendCRing"   // Sender btree + data + control rings
    ConfigSendEL     ConfigVariant = "SendEL"      // Full sender lockless

    // Combined variants (both sender and receiver lockless)
    ConfigFullSendEL ConfigVariant = "FullSendEL"  // Full receiver + full sender lockless
)

// variantToSenderConfig maps config variants to sender settings
func variantToSenderConfig(v ConfigVariant) SenderConfig {
    switch v {
    case ConfigSendBtree:
        return SenderConfig{UseSendBtree: true}
    case ConfigSendRing:
        return SenderConfig{UseSendBtree: true, UseSendRing: true}
    case ConfigSendCRing:
        return SenderConfig{UseSendBtree: true, UseSendRing: true, UseSendControlRing: true}
    case ConfigSendEL, ConfigFullSendEL:
        return SenderConfig{
            UseSendBtree:       true,
            UseSendRing:        true,
            UseSendControlRing: true,
            UseSendEventLoop:   true,
        }
    default:
        return SenderConfig{} // Default: all disabled
    }
}
```

#### 8.10 Parallel Comparison Tests for Sender

Following [`parallel_comparison_test_design.md`](./parallel_comparison_test_design.md), we create parallel tests that compare:
- **Baseline**: Locked sender (Tick mode, linked lists)
- **HighPerf**: Lockless sender (EventLoop, btree, rings)

Both pipelines run simultaneously under identical network conditions for direct comparison.

**Test Naming Convention:**

```
Parallel-{Pattern}[-{Loss}]-{Bitrate}-{Buffer}-{RTT}-{BaselineConfig}-vs-{HighPerfConfig}
```

**Sender Comparison Tests:**

| Test Name | What it Compares |
|-----------|------------------|
| `Parallel-Clean-20M-3s-R0-Base-vs-SendBtree` | Linked list vs btree NAK lookup |
| `Parallel-Clean-20M-3s-R0-Base-vs-SendRing` | Locked Push() vs ring Push() |
| `Parallel-Clean-20M-3s-R0-Base-vs-SendEL` | Tick mode vs EventLoop (full sender lockless) |
| `Parallel-Clean-50M-3s-R0-Base-vs-SendEL` | High throughput sender comparison |
| `Parallel-Loss-L5-20M-3s-R60-Base-vs-SendEL` | Sender lockless under packet loss |
| `Parallel-Loss-L5-20M-3s-R130-Base-vs-SendEL` | Sender lockless with intercontinental RTT |
| `Parallel-Starlink-20M-5s-R60-Base-vs-SendEL` | Sender lockless under Starlink outages |
| `Parallel-Clean-20M-3s-R0-FullEL-vs-FullSendEL` | Full receiver vs full receiver+sender lockless |

**Combined Receiver + Sender Tests:**

| Test Name | What it Compares |
|-----------|------------------|
| `Parallel-Clean-20M-3s-R0-Base-vs-FullSendEL` | Baseline vs full lockless (both paths) |
| `Parallel-Loss-L5-20M-5s-R60-Base-vs-FullSendEL` | Full lockless under loss + latency |
| `Parallel-Starlink-20M-5s-R60-Base-vs-FullSendEL` | Full lockless under Starlink |

#### 8.11 Test Configuration Files

**File:** `contrib/integration_testing/network/configs/Parallel-Clean-20M-Base-vs-SendEL.sh`

```bash
#!/bin/bash
# Parallel test comparing locked sender vs lockless sender on clean network

TEST_NAME="Parallel-Clean-20M-Base-vs-SendEL"
TEST_DURATION="90s"
DATA_RATE="20000000"  # 20 Mbps
LATENCY_MS="0"
LOSS_PERCENT="0"

# Baseline: Default locked sender (Tick mode)
BASELINE_FLAGS=""

# HighPerf: Full lockless sender
HIGHPERF_FLAGS="-usesendbtree -usesendring -usesendcontrolring -usesendeventloop"

export TEST_NAME TEST_DURATION DATA_RATE LATENCY_MS LOSS_PERCENT
export BASELINE_FLAGS HIGHPERF_FLAGS
```

**File:** `contrib/integration_testing/network/configs/Parallel-Loss-L5-20M-R60-Base-vs-SendEL.sh`

```bash
#!/bin/bash
# Parallel test comparing locked vs lockless sender under 5% loss + 60ms RTT

TEST_NAME="Parallel-Loss-L5-20M-R60-Base-vs-SendEL"
TEST_DURATION="90s"
DATA_RATE="20000000"
LATENCY_MS="60"      # 30ms each direction
LOSS_PERCENT="5"

BASELINE_FLAGS=""
HIGHPERF_FLAGS="-usesendbtree -usesendring -usesendcontrolring -usesendeventloop"

export TEST_NAME TEST_DURATION DATA_RATE LATENCY_MS LOSS_PERCENT
export BASELINE_FLAGS HIGHPERF_FLAGS
```

**File:** `contrib/integration_testing/network/configs/Parallel-Starlink-20M-Base-vs-FullSendEL.sh`

```bash
#!/bin/bash
# Parallel test: full lockless (receiver + sender) vs baseline under Starlink

TEST_NAME="Parallel-Starlink-20M-Base-vs-FullSendEL"
TEST_DURATION="90s"
DATA_RATE="20000000"
PATTERN="starlink"   # Periodic 100% loss events
LATENCY_MS="60"

BASELINE_FLAGS=""

# Full lockless: receiver + sender features
HIGHPERF_FLAGS="\
-packetreorderalgorithm=btree \
-iouringenabled \
-iouringrecvenabled \
-usenakbtree \
-fastnakenabled \
-fastnakrecentenabled \
-usepacketring \
-useeventloop \
-honornakorder \
-usesendbtree \
-usesendring \
-usesendcontrolring \
-usesendeventloop"

export TEST_NAME TEST_DURATION DATA_RATE PATTERN LATENCY_MS
export BASELINE_FLAGS HIGHPERF_FLAGS
```

#### 8.12 Integration with Test Matrix Generator

Add sender config variants to the matrix generator in `contrib/integration_testing/test_matrix.go`:

```go
// SenderConfigVariant specifies sender-side feature configuration
type SenderConfigVariant string

const (
    SenderBase    SenderConfigVariant = "SBase"    // Locked sender (default)
    SenderBtree   SenderConfigVariant = "SBtree"   // Sender btree only
    SenderRing    SenderConfigVariant = "SRing"    // Sender data ring
    SenderCRing   SenderConfigVariant = "SCRing"   // Sender control ring
    SenderEL      SenderConfigVariant = "SEL"      // Full sender lockless
)

// Extended test params for sender comparison
type SenderTestParams struct {
    TestParams
    SenderBaseline SenderConfigVariant
    SenderHighPerf SenderConfigVariant
}

// GenerateSenderComparisonTests creates sender-specific parallel tests
func GenerateSenderComparisonTests() []GeneratedTestConfig {
    var tests []GeneratedTestConfig

    // Core sender comparison tests (clean network)
    senderVariants := []SenderConfigVariant{SenderBtree, SenderRing, SenderCRing, SenderEL}
    for _, variant := range senderVariants {
        tests = append(tests, GeneratedTestConfig{
            Name:           fmt.Sprintf("Parallel-Clean-20M-3s-R0-Base-vs-%s", variant),
            Mode:           TestModeParallel,
            Bitrate:        20_000_000,
            Buffer:         3 * time.Second,
            RTT:            RTT0,
            Loss:           0,
            BaselineConfig: ConfigBase,
            HighPerfConfig: ConfigVariant(variant),
            Tier:           TierCore,
        })
    }

    // Sender tests under network stress
    stressConfigs := []struct {
        loss float64
        rtt  RTTProfile
        tier TestTier
    }{
        {0.05, RTT60, TierCore},     // 5% loss, cross-continental
        {0.05, RTT130, TierDaily},   // 5% loss, intercontinental
        {0.10, RTT60, TierNightly},  // 10% loss
        {0.05, RTT300, TierNightly}, // GEO satellite
    }

    for _, sc := range stressConfigs {
        tests = append(tests, GeneratedTestConfig{
            Name:           fmt.Sprintf("Parallel-Loss-L%d-20M-3s-%s-Base-vs-SendEL", int(sc.loss*100), sc.rtt),
            Mode:           TestModeParallel,
            Bitrate:        20_000_000,
            Buffer:         3 * time.Second,
            RTT:            sc.rtt,
            Loss:           sc.loss,
            BaselineConfig: ConfigBase,
            HighPerfConfig: ConfigSendEL,
            Tier:           sc.tier,
        })
    }

    return tests
}
```

#### 8.13 Expected Metrics Comparison Output

When running parallel sender comparison tests, the analysis will show sender-specific metrics:

```
=== Parallel Comparison: Clean-20M-Base-vs-SendEL ===

Pipeline Configuration:
  Baseline: Tick mode, linked lists, locked Push()
  HighPerf: EventLoop, btree, lock-free rings

Network Conditions:
  Pattern: clean (no impairment)
  RTT: 0ms
  Loss: 0%
  Duration: 90s

=== Sender Metrics Comparison ===

                            Baseline      HighPerf      Diff
  Packets Pushed:           135,000       135,000       =
  Push() Time (avg µs):     2.45          0.82          -66.5% ✓
  NAK Lookups:              4,500         4,500         =
  NAK Lookup Time (avg µs): 125.3         3.8           -97.0% ✓
  Packet Delivery Variance: 8.2ms         0.5ms         -93.9% ✓
  Retransmit Latency (avg): 12.4ms        2.1ms         -83.1% ✓

=== Delivery Smoothness (Burst Detection) ===

  Metric                      Baseline      HighPerf      Diff
  ─────────────────────────────────────────────────────────────
  Iterations/Ticks:           900           892,451       N/A
  Packets Delivered:          135,000       135,000       =
  Packets/Iteration:          150.0         0.15          -99.9% ✓ SMOOTH

  Interpretation:
    • Baseline (Tick mode): 150 packets per Tick() = BURSTY
      Tick() fires every 100ms, delivers all due packets at once
      → Network sees burst of 150 packets, then silence

    • HighPerf (EventLoop): 0.15 packets per iteration = SMOOTH
      EventLoop processes packets continuously, 1-2 at a time
      → Network sees evenly-spaced packets, minimal bursting

  Burst Impact:
    Lower packets/iteration = smoother delivery = better for:
    - Network buffer utilization (no overflow)
    - Receiver processing (steady load)
    - Real-time applications (consistent latency)

=== EventLoop Metrics (HighPerf only) ===

  SendEventLoopIterations:     892,451
  SendEventLoopDeliveryFires:  90,000
  SendEventLoopDropFires:      900
  SendEventLoopDefaultRuns:    801,551
  SendEventLoopIdleBackoffs:   445,231 (49.9%)
  SendEventLoopDataDrained:    135,000
  SendEventLoopControlDrained: 12,423
  SendEventLoopACKsProcessed:  8,912
  SendEventLoopNAKsProcessed:  3,511

=== TSBPD Sleep Metrics (HighPerf only) ===

  SendEventLoopTsbpdSleeps:      445,231
  SendEventLoopEmptyBtreeSleeps: 1,234
  SendEventLoopSleepClampedMin:  12,345
  SendEventLoopSleepClampedMax:  523
  Avg Sleep Duration:            112µs (SleepTotalUs / TsbpdSleeps)
  Avg Next Delivery:             987µs (NextDeliveryTotalUs / TsbpdSleeps)

=== Summary ===
  ✓ HighPerf shows 66.5% faster Push() operations (lock-free ring)
  ✓ HighPerf shows 97.0% faster NAK lookup (O(log n) btree)
  ✓ HighPerf shows 93.9% smoother packet delivery (EventLoop)
  ✓ HighPerf shows 83.1% lower retransmit latency
  ✓ HighPerf shows 99.9% reduction in packets/iteration (burst elimination)
  = Recovery rate identical (100%)
```

**Burst Detection Metric Explained:**

The **Packets/Iteration** ratio is a simple but effective metric to detect and compare burst behavior:

| Mode | Typical Packets/Iteration | Behavior |
|------|---------------------------|----------|
| Tick (100ms) | 50-200 | Very bursty - all packets at once |
| Tick (10ms) | 5-20 | Moderate bursting |
| EventLoop | 0.1-2.0 | Smooth - continuous delivery |

**Calculation:**

```go
// For Baseline (Tick mode):
packetsPerIteration := float64(tickDeliveredPackets) / float64(tickRuns)

// For HighPerf (EventLoop mode):
packetsPerIteration := float64(sendDeliveredPackets) / float64(sendEventLoopIterations)
```

**Why This Works:**

- **Tick mode**: `Tick()` fires periodically (e.g., every 100ms), then delivers ALL packets
  whose TSBPD time has passed. At 20 Mbps with 1500-byte packets, that's ~150 packets
  per 100ms tick - delivered as a burst.

- **EventLoop mode**: The loop runs continuously, processing 1-3 packets per iteration
  as their TSBPD times arrive. Even if iterating 10,000 times/second, each iteration
  delivers only the packets ready RIGHT NOW.

**Required Metrics for Burst Detection:**

To enable burst detection comparison, we need these metrics:

| Metric | Mode | Location | Purpose |
|--------|------|----------|---------|
| `SendTickRuns` | Baseline (Tick) | `metrics/metrics.go` | Count Tick() invocations |
| `SendTickDeliveredPackets` | Baseline (Tick) | `metrics/metrics.go` | Packets delivered per Tick() |
| `SendEventLoopIterations` | HighPerf (EventLoop) | Already planned | Count EventLoop iterations |
| `SendEventLoopDeliveredPackets` | HighPerf (EventLoop) | Already planned | Packets delivered |

**Note:** `SendTickRuns` and `SendTickDeliveredPackets` need to be added to the existing
sender Tick() function as part of this implementation to enable baseline comparison.

**Integration Test Implementation:**

The burst detection is added to `parallel_comparison.go`:

```go
func (a *Analyzer) calculateBurstMetrics(baseline, highperf *PipelineMetrics) {
    // Baseline: uses Tick mode (requires SendTickRuns metric)
    baselineIterations := baseline.SendTickRuns
    if baselineIterations == 0 {
        baselineIterations = 1 // Avoid division by zero
    }
    baselinePacketsPerIter := float64(baseline.SendTickDeliveredPackets) / float64(baselineIterations)

    // HighPerf: uses EventLoop
    highperfIterations := highperf.SendEventLoopIterations
    if highperfIterations == 0 {
        highperfIterations = 1
    }
    highperfPacketsPerIter := float64(highperf.SendEventLoopDeliveredPackets) / float64(highperfIterations)

    // Report
    fmt.Printf("=== Delivery Smoothness (Burst Detection) ===\n\n")
    fmt.Printf("  %-28s %-12s %-12s %s\n", "Metric", "Baseline", "HighPerf", "Diff")
    fmt.Printf("  %s\n", strings.Repeat("─", 61))
    fmt.Printf("  %-28s %-12d %-12d N/A\n", "Iterations/Ticks:",
        baselineIterations, highperfIterations)
    fmt.Printf("  %-28s %-12d %-12d =\n", "Packets Delivered:",
        baseline.DeliveredPackets, highperf.DeliveredPackets)

    // Calculate and display packets/iteration
    diff := ((highperfPacketsPerIter - baselinePacketsPerIter) / baselinePacketsPerIter) * 100
    status := "⚠"
    if highperfPacketsPerIter < baselinePacketsPerIter {
        status = "✓ SMOOTH"
    }
    fmt.Printf("  %-28s %-12.2f %-12.2f %.1f%% %s\n", "Packets/Iteration:",
        baselinePacketsPerIter, highperfPacketsPerIter, diff, status)
}
```

#### 8.14 Makefile Targets

Add to `Makefile`:

```makefile
# Sender lockless comparison tests
test-parallel-sender:
	@echo "Running sender lockless comparison tests..."
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendEL

test-parallel-sender-all:
	@echo "Running all sender lockless tests..."
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendBtree
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendRing
	sudo ./integration_testing parallel-run Parallel-Clean-20M-Base-vs-SendEL
	sudo ./integration_testing parallel-run Parallel-Loss-L5-20M-R60-Base-vs-SendEL

test-parallel-full-lockless:
	@echo "Running full lockless (receiver + sender) test..."
	sudo ./integration_testing parallel-run Parallel-Starlink-20M-Base-vs-FullSendEL

# List all sender tests
test-parallel-sender-list:
	@./integration_testing parallel-list | grep -E "Send|FullSend"
```

---

## 13. Testing Strategy

### 13.1 Unit Tests

| Test File | Coverage |
|-----------|----------|
| `send_packet_btree_test.go` | Insert, Get, Delete, Iterate |
| `send_packet_btree_table_test.go` | Table-driven edge cases |
| `send_ring_test.go` | Push, TryPop, DrainBatch (data ring) |
| `send_control_ring_test.go` | ACK/NAK routing through control ring |
| `send_eventloop_test.go` | Delivery timing, drop behavior |
| `send_eventloop_control_test.go` | Control packet processing via ring |
| `send_tsbpd_sleep_test.go` | TSBPD-aware sleep calculations |
| `send_push_test.go` | Both push paths |

#### TSBPD Sleep Unit Tests

**File:** `congestion/live/send/tsbpd_sleep_test.go`

```go
package send

import (
    "testing"
    "time"

    "github.com/randomizedcoder/gosrt/metrics"
)

func TestCalculateTsbpdSleepDuration_Table(t *testing.T) {
    tests := []struct {
        name             string
        nextDeliveryIn   time.Duration
        minSleep         time.Duration
        maxSleep         time.Duration
        expectedDuration time.Duration
        expectWasTsbpd   bool
        expectWasEmpty   bool
        expectClampedMin bool
        expectClampedMax bool
    }{
        {
            name:             "normal_tsbpd_sleep",
            nextDeliveryIn:   1000 * time.Microsecond, // 1ms until next packet
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 900 * time.Microsecond, // 90% of 1ms
            expectWasTsbpd:   true,
        },
        {
            name:             "clamped_to_min",
            nextDeliveryIn:   50 * time.Microsecond, // Very short
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 100 * time.Microsecond, // Clamped to min
            expectWasTsbpd:   true,
            expectClampedMin: true,
        },
        {
            name:             "clamped_to_max",
            nextDeliveryIn:   10 * time.Millisecond, // Very long
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 1 * time.Millisecond, // Clamped to max
            expectWasTsbpd:   true,
            expectClampedMax: true,
        },
        {
            name:             "btree_empty",
            nextDeliveryIn:   0, // No packets waiting
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 1 * time.Millisecond, // Uses max sleep
            expectWasEmpty:   true,
        },
        {
            name:             "exactly_at_90_percent_boundary",
            nextDeliveryIn:   111 * time.Microsecond, // 90% = 99.9µs
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 100 * time.Microsecond, // Just under min, clamped
            expectWasTsbpd:   true,
            expectClampedMin: true,
        },
        {
            name:             "large_gap_between_packets",
            nextDeliveryIn:   5 * time.Millisecond, // 5ms gap
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 1 * time.Millisecond, // 90% of 5ms = 4.5ms, clamped to max
            expectWasTsbpd:   true,
            expectClampedMax: true,
        },
        {
            name:             "typical_20mbps_spacing",
            nextDeliveryIn:   550 * time.Microsecond, // ~550µs between packets at 20 Mbps
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 495 * time.Microsecond, // 90% of 550µs
            expectWasTsbpd:   true,
        },
        {
            name:             "exactly_at_min_boundary",
            nextDeliveryIn:   112 * time.Microsecond, // 90% = 100.8µs, just above min
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 100 * time.Microsecond, // Rounds down to 100µs
            expectWasTsbpd:   true,
            // Not clamped because 100.8 >= 100
        },
        {
            name:             "high_bitrate_50mbps",
            nextDeliveryIn:   220 * time.Microsecond, // ~220µs between packets at 50 Mbps
            minSleep:         100 * time.Microsecond,
            maxSleep:         1 * time.Millisecond,
            expectedDuration: 198 * time.Microsecond, // 90% of 220µs
            expectWasTsbpd:   true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Create sender with fresh metrics
            m := metrics.NewConnectionMetrics()
            s := &sender{metrics: m}

            // Call the pure calculation function (no sleep!)
            result := s.calculateTsbpdSleepDuration(
                tt.nextDeliveryIn, tt.minSleep, tt.maxSleep,
            )

            // Verify duration
            if result.Duration != tt.expectedDuration {
                t.Errorf("Duration: expected %v, got %v", tt.expectedDuration, result.Duration)
            }

            // Verify result flags
            if result.WasTsbpd != tt.expectWasTsbpd {
                t.Errorf("WasTsbpd: expected %v, got %v", tt.expectWasTsbpd, result.WasTsbpd)
            }
            if result.WasEmpty != tt.expectWasEmpty {
                t.Errorf("WasEmpty: expected %v, got %v", tt.expectWasEmpty, result.WasEmpty)
            }
            if result.ClampedMin != tt.expectClampedMin {
                t.Errorf("ClampedMin: expected %v, got %v", tt.expectClampedMin, result.ClampedMin)
            }
            if result.ClampedMax != tt.expectClampedMax {
                t.Errorf("ClampedMax: expected %v, got %v", tt.expectClampedMax, result.ClampedMax)
            }

            // Verify metrics were updated
            if tt.expectWasTsbpd {
                if m.SendEventLoopTsbpdSleeps.Load() != 1 {
                    t.Error("expected TsbpdSleeps to be 1")
                }
            }
            if tt.expectWasEmpty {
                if m.SendEventLoopEmptyBtreeSleeps.Load() != 1 {
                    t.Error("expected EmptyBtreeSleeps to be 1")
                }
            }
            if tt.expectClampedMin {
                if m.SendEventLoopSleepClampedMin.Load() != 1 {
                    t.Error("expected SleepClampedMin to be 1")
                }
            }
            if tt.expectClampedMax {
                if m.SendEventLoopSleepClampedMax.Load() != 1 {
                    t.Error("expected SleepClampedMax to be 1")
                }
            }

            // Verify IdleBackoffs always incremented
            if m.SendEventLoopIdleBackoffs.Load() != 1 {
                t.Error("expected IdleBackoffs to be 1")
            }

            // Verify SleepTotalUs tracked
            if m.SendEventLoopSleepTotalUs.Load() != uint64(result.Duration.Microseconds()) {
                t.Errorf("SleepTotalUs: expected %d, got %d",
                    result.Duration.Microseconds(),
                    m.SendEventLoopSleepTotalUs.Load())
            }
        })
    }
}

// Test that the 90% factor is applied correctly
func TestCalculateTsbpdSleepDuration_90PercentFactor(t *testing.T) {
    m := metrics.NewConnectionMetrics()
    s := &sender{metrics: m}

    // Use large bounds so we don't clamp
    result := s.calculateTsbpdSleepDuration(
        1000*time.Microsecond, // 1ms
        1*time.Microsecond,    // tiny min
        10*time.Millisecond,   // large max
    )

    expected := 900 * time.Microsecond // 90% of 1ms
    if result.Duration != expected {
        t.Errorf("90%% factor: expected %v, got %v", expected, result.Duration)
    }
}

// Benchmark to verify sleep calculation is fast (no allocations)
func BenchmarkCalculateTsbpdSleepDuration(b *testing.B) {
    m := metrics.NewConnectionMetrics()
    s := &sender{metrics: m}

    nextDeliveryIn := 550 * time.Microsecond
    minSleep := 100 * time.Microsecond
    maxSleep := 1 * time.Millisecond

    b.ResetTimer()
    b.ReportAllocs()
    for i := 0; i < b.N; i++ {
        _ = s.calculateTsbpdSleepDuration(nextDeliveryIn, minSleep, maxSleep)
    }
}

// Benchmark different scenarios
func BenchmarkCalculateTsbpdSleepDuration_Scenarios(b *testing.B) {
    scenarios := []struct {
        name           string
        nextDeliveryIn time.Duration
    }{
        {"tsbpd_normal", 550 * time.Microsecond},
        {"tsbpd_clamped_min", 50 * time.Microsecond},
        {"tsbpd_clamped_max", 5 * time.Millisecond},
        {"btree_empty", 0},
    }

    for _, sc := range scenarios {
        b.Run(sc.name, func(b *testing.B) {
            m := metrics.NewConnectionMetrics()
            s := &sender{metrics: m}

            b.ResetTimer()
            b.ReportAllocs()
            for i := 0; i < b.N; i++ {
                _ = s.calculateTsbpdSleepDuration(
                    sc.nextDeliveryIn,
                    100*time.Microsecond,
                    1*time.Millisecond,
                )
            }
        })
    }
}
```

**Key Testing Benefits:**

1. **Pure calculation function** - `calculateTsbpdSleepDuration()` doesn't sleep, making tests fast
2. **Result struct** - `tsbpdSleepResult` captures all state for verification
3. **Table-driven tests** - Easy to add new scenarios
4. **Metric verification** - Tests verify correct metrics are updated
5. **Benchmarks** - Verify no allocations in hot path

### 13.2 Benchmarks

| Benchmark | Comparison |
|-----------|------------|
| `BenchmarkSendNakLookup_List` | Current O(n) lookup |
| `BenchmarkSendNakLookup_Btree` | New O(log n) lookup |
| `BenchmarkSendPush_Locked` | Current locked Push |
| `BenchmarkSendPush_Lockfree` | New ring Push |
| `BenchmarkControlDispatch` | ACK/NAK routing overhead |

### 13.3 Race Tests

```go
// send_race_test.go - Data ring
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

// send_control_race_test.go - Control ring + EventLoop
// This test verifies that control packets routed through the ring
// do NOT cause races when EventLoop processes them.
func TestRace_ControlRingAndEventLoop(t *testing.T) {
    sender := createTestSender(t, SendConfig{
        UseSendRing:        true,
        UseSendControlRing: true,
        UseSendEventLoop:   true,
    })
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    // Start EventLoop in background
    go sender.EventLoop(ctx)

    // Simulate io_uring completion handler pushing ACK/NAK
    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(2)
        go func() {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                sender.HandleACK(uint32(j)) // Routes through control ring
            }
        }()
        go func() {
            defer wg.Done()
            for j := 0; j < 100; j++ {
                sender.HandleNAK([]circular.Number{{Val: uint32(j)}})
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
// UseSendControlRing: false
// UseSendBtree: false
// UseSendEventLoop: false

// Enable incrementally for testing
config.UseSendBtree = true         // Phase 1: Just btree (still using locks)
config.UseSendRing = true          // Phase 2: Add data ring for Push()
config.UseSendControlRing = true   // Phase 3: Add control ring for ACK/NAK (REQUIRED for lockless!)
config.UseSendEventLoop = true     // Phase 4: Full lockless

// IMPORTANT: EventLoop requires BOTH rings!
// - SendRing: Routes Push() data packets to EventLoop
// - ControlRing: Routes ACK/NAK control packets to EventLoop
// Without both, btree would have concurrent access → race conditions
```

### 14.2 A/B Testing

The parallel test framework supports comparing:
- Baseline (Tick mode, linked lists) vs HighPerf (EventLoop, btree)

```bash
# Compare burst behavior
sudo make test-parallel CONFIG=Parallel-Sender-Tick-vs-EventLoop
```

### 14.3 Rollback

If issues found, rollback in reverse order:
1. Disable EventLoop: `UseSendEventLoop = false` (returns to Tick mode)
2. Disable control ring: `UseSendControlRing = false` (ACK/NAK processed directly)
3. Disable data ring: `UseSendRing = false` (Push uses lock)
4. Disable btree: `UseSendBtree = false` (returns to linked lists)
5. Full rollback: Remove all config flags, use defaults

**Note:** The system gracefully degrades at each level. Disabling EventLoop but keeping btree still provides O(log n) NAK lookup benefit.

---

## Appendix A: File Summary

| File | Status | Description |
|------|--------|-------------|
| `congestion/live/send/sender.go` | Modify | Add btree, rings, dispatch |
| `congestion/live/send/send_packet_btree.go` | New | Btree implementation |
| `congestion/live/send/ring.go` | New | Lock-free data ring |
| `congestion/live/send/control_ring.go` | New | **CRITICAL:** Lock-free control packet ring |
| `congestion/live/send/push.go` | Modify | Add lock-free push |
| `congestion/live/send/eventloop.go` | New | Sender event loop (data + control) |
| `congestion/live/send/nak.go` | Modify | O(log n) NAK lookup |
| `congestion/live/send/ack.go` | Modify | DeleteBefore optimization |
| `config.go` | Modify | Add config options (incl. control ring) |
| `contrib/common/flags.go` | Modify | Add CLI flags (incl. control ring) |
| `metrics/metrics.go` | Modify | Add sender metrics (incl. control ring) |
| `metrics/handler.go` | Modify | Add Prometheus export |

---

## Appendix B: Expected Performance Improvements

| Operation | Current | New | Improvement |
|-----------|---------|-----|-------------|
| Push() | O(1) + lock | O(1) lock-free via ring | ~3× faster |
| NAK lookup | O(n) | O(log n) btree | 100-5000× faster |
| ACK processing | O(n) scan | O(k × log n) deleteMin | 10-100× faster |
| Control packet handling | Lock per ACK/NAK | Lock-free ring | ~2× faster |
| Packet delivery | Burst (10ms) | Smooth (1ms) | Better network behavior |
| Lock contention | High (3+ goroutines) | Zero (single consumer) | Eliminates bottleneck |

---

## Appendix C: Concurrency Model Summary

| Goroutine | Accesses | Via |
|-----------|----------|-----|
| Application (Push) | SendPacketRing | Atomic write |
| io_uring (ACK) | ControlPacketRing | Atomic write |
| io_uring (NAK) | ControlPacketRing | Atomic write |
| **EventLoop** | **SendPacketBtree** | **Single-threaded read/write** |

**Key insight:** All writes to rings are atomic (lock-free). The EventLoop is the **only consumer** of both rings, making it the **only accessor** of the btree. This eliminates all race conditions by design.

---

**This design is ready for implementation review.**

