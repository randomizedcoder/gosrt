# Completely Lock-Free Receiver Implementation Plan

## Table of Contents

- [Overview](#overview)
- [Implementation Notes](#implementation-notes)
  - [Note 1: Use common.ControlRing[T] Generic Pattern](#note-1-use-commoncontrolringt-generic-pattern)
  - [Note 2: Follow Sender Control Ring Patterns](#note-2-follow-sender-control-ring-patterns)
  - [Note 3: Context Assert Consistency](#note-3-context-assert-consistency)
  - [Note 4: Naming Convention - No "NoLock" or "WithTime" Suffixes](#note-4-naming-convention---no-nolock-or-withtime-suffixes)
- [Phase 1: Common Control Ring Infrastructure](#phase-1-common-control-ring-infrastructure)
  - [Step 1.1: Create common Package Directory](#step-11-create-common-package-directory)
  - [Step 1.2: Implement Generic ControlRing[T]](#step-12-implement-generic-controlringt)
  - [Step 1.3: Add Unit Tests for common.ControlRing[T]](#step-13-add-unit-tests-for-commoncontrolringt)
  - [Step 1.4: Add Benchmarks for common.ControlRing[T]](#step-14-add-benchmarks-for-commoncontrolringt)
- [Phase 2: Receiver Control Ring](#phase-2-receiver-control-ring)
  - [Step 2.1: Create RecvControlRing Types](#step-21-create-recvcontrolring-types)
  - [Step 2.2: Implement RecvControlRing Methods](#step-22-implement-recvcontrolring-methods)
  - [Step 2.3: Add Unit Tests for RecvControlRing](#step-23-add-unit-tests-for-recvcontrolring)
  - [Step 2.4: Add Benchmarks for RecvControlRing](#step-24-add-benchmarks-for-recvcontrolring)
- [Phase 3: Configuration and CLI Flags](#phase-3-configuration-and-cli-flags)
  - [Step 3.1: Add Config Options](#step-31-add-config-options)
  - [Step 3.2: Add CLI Flags](#step-32-add-cli-flags)
  - [Step 3.3: Update Default Config](#step-33-update-default-config)
  - [Step 3.4: Update Sender Config Defaults](#step-34-update-sender-config-defaults-consolidation)
  - [Step 3.5: Add Flag Tests](#step-35-add-flag-tests)
- [Phase 4: Metrics](#phase-4-metrics)
  - [Step 4.1: Add Receiver Control Ring Metrics](#step-41-add-receiver-control-ring-metrics)
  - [Step 4.2: Export Prometheus Metrics](#step-42-export-prometheus-metrics)
  - [Step 4.3: Add Metrics Tests](#step-43-add-metrics-tests)
- [Phase 5: Lock-Free Function Variants](#phase-5-lock-free-function-variants)
  - [Step 5.1: Create handleACKACK Lock-Free Variant](#step-51-create-handleackack-lock-free-variant)
  - [Step 5.2: Create handleACKACKLocked Wrapper](#step-52-create-handleackacklocked-wrapper)
  - [Step 5.3: Create handleKeepAlive Lock-Free Variant](#step-53-create-handlekeepalive-lock-free-variant)
  - [Step 5.4: Create handleKeepAliveLocked Wrapper](#step-54-create-handlekeepalivelocked-wrapper)
  - [Step 5.5: Add Context Asserts to Existing Receiver Functions](#step-55-add-context-asserts-to-existing-receiver-functions)
  - [Step 5.6: Add Unit Tests for Lock-Free Variants](#step-56-add-unit-tests-for-lock-free-variants)
- [Phase 6: Control Ring Integration](#phase-6-control-ring-integration)
  - [Step 6.1: Add Control Ring to srtConn](#step-61-add-control-ring-to-srtconn)
  - [Step 6.2: Initialize Control Ring on Connection](#step-62-initialize-control-ring-on-connection)
  - [Step 6.3: Route ACKACK Through Control Ring](#step-63-route-ackack-through-control-ring)
  - [Step 6.4: Route KEEPALIVE Through Control Ring](#step-64-route-keepalive-through-control-ring)
- [Phase 7: EventLoop Integration](#phase-7-eventloop-integration)
  - [Step 7.1: Add processControlPackets to Receiver EventLoop](#step-71-add-processcontrolpackets-to-receiver-eventloop)
  - [Step 7.2: Add Control Ring Drain to EventLoop](#step-72-add-control-ring-drain-to-eventloop)
  - [Step 7.3: Function Dispatch Setup](#step-73-function-dispatch-setup)
- [Phase 8: Refactor SendControlRing](#phase-8-refactor-sendcontrolring-to-use-common)
  - [Step 8.1: Update SendControlRing to Embed common.ControlRing](#step-81-update-sendcontrolring-to-embed-commoncontrolring)
  - [Step 8.2: Verify Sender Tests Still Pass](#step-82-verify-sender-tests-still-pass)
- [Phase 9: Integration Testing](#phase-9-integration-testing)
  - [Step 9.1: Add Test Configuration](#step-91-add-test-configuration)
  - [Step 9.2: Add Isolation Tests](#step-92-add-isolation-tests)
  - [Step 9.3: Add Parallel Tests](#step-93-add-parallel-tests)
  - [Step 9.4: Run All Tests](#step-94-run-all-tests)
- [Phase 10: Documentation and Cleanup](#phase-10-documentation-and-cleanup)
  - [Step 10.1: Update Integration Test Docs](#step-101-update-integration-test-docs)
  - [Step 10.2: Naming Refactoring (Optional)](#step-102-naming-refactoring-optional)
- [Summary: Implementation Order](#summary-implementation-order)
- [Appendix: File Summary](#appendix-file-summary)
- [Checkpoint Commands](#checkpoint-commands)

---

## Overview

This document provides a detailed, step-by-step implementation plan for the completely lock-free receiver design described in `completely_lockfree_receiver.md`. Each step includes specific file paths, line numbers, function signatures, and build/test checkpoints.

**Related Documents:**
- `completely_lockfree_receiver.md` - High-level design and architecture
- `lockless_sender_design.md` - Sender control ring pattern (reference)
- `lockless_sender_implementation_plan.md` - Sender implementation (reference)
- `gosrt_lockless_design.md` - Original receiver lockless patterns

**Current State:**
- Receiver EventLoop handles data packets via lock-free ring
- Control packets (ACKACK, KEEPALIVE) still use locks
- `handleACKACK()` acquires `c.ackLock` for ackNumbers btree access
- Sender has fully lock-free control ring pattern

**Target State:**
- Receiver EventLoop is COMPLETELY lock-free
- Control packets routed via `RecvControlRing`
- `handleACKACK()` and `handleKeepAlive()` are lock-free in EventLoop mode
- Shared `common.ControlRing[T]` used by both sender and receiver

---

## Implementation Notes

### Note 1: Use common.ControlRing[T] Generic Pattern

**⚠️ CRITICAL: Create shared generic ring in `congestion/live/common/`**

Both sender and receiver control rings share the same core functionality:
- Push control packets from io_uring handlers
- Pop control packets in EventLoop
- Thread-safe MPSC (multi-producer, single-consumer)

Using Go generics (1.18+), we create a single implementation:

```go
// congestion/live/common/control_ring.go
package common

type ControlRing[T any] struct {
    ring   *ring.ShardedRing
    shards int
}

func NewControlRing[T any](size, shards int) (*ControlRing[T], error) { ... }
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool { ... }
func (r *ControlRing[T]) TryPop() (T, bool) { ... }
func (r *ControlRing[T]) Len() int { ... }
```

### Note 2: Follow Sender Control Ring Patterns

**Reference:** `congestion/live/send/control_ring.go` lines 27-94

The receiver control ring MUST follow the same patterns:
- `ControlPacketType` enum for packet types
- `ControlPacket` struct with inline arrays (avoid allocations)
- `Push*()` methods for each packet type
- `TryPop()` for EventLoop consumption

### Note 3: Context Assert Consistency

**⚠️ Add asserts to ALL functions called by EventLoop or Tick**

Functions called from EventLoop must have `AssertEventLoopContext()`.
Functions called from Tick must have `AssertTickContext()`.

This catches incorrect function dispatch at runtime (debug builds only).

**Reference:** `congestion/live/receive/debug_context.go` lines 62-152

### Note 4: Naming Convention - No "NoLock" or "WithTime" Suffixes

**Naming Rules:**
- Primary functions (EventLoop): no suffix (e.g., `periodicACK`, `deliverReadyPackets`)
- Locking wrappers (Tick): `*Locked` suffix (e.g., `periodicACKLocked`)

**Do NOT use:**
- `*NoLock` suffix (confusing - implies lock exists but not taken)
- `*WithTime` suffix (all functions take time parameter anyway)

**Phase 10.2** includes optional refactoring of existing inconsistent names.

---

## Phase 1: Common Control Ring Infrastructure

**Objective:** Create the shared generic control ring that both sender and receiver will use.

### Step 1.1: Create common Package Directory

**File:** `congestion/live/common/doc.go` (NEW)

```go
// Package common provides shared infrastructure for sender and receiver
// congestion control implementations.
//
// Key Components:
// - ControlRing[T]: Generic lock-free ring for control packets (ACK, NAK, ACKACK, KEEPALIVE)
package common
```

**Checkpoint:**
```bash
# Verify package compiles
go build ./congestion/live/common/
```

### Step 1.2: Implement Generic ControlRing[T]

**File:** `congestion/live/common/control_ring.go` (NEW)

**Content:** See `completely_lockfree_receiver.md` Section 5.1.1

```go
//go:build go1.18

package common

import (
    "fmt"
    ring "github.com/randomizedcoder/go-lock-free-ring"
)

// ControlRing is a generic lock-free ring for control packets.
// T is the control packet type (e.g., send.ControlPacket or receive.RecvControlPacket).
//
// Thread-safety:
// - Push(): Safe to call from multiple goroutines (io_uring handlers)
// - TryPop(): Single consumer only (EventLoop)
//
// Reference: completely_lockfree_receiver.md Section 5.1.1
type ControlRing[T any] struct {
    ring   *ring.ShardedRing
    shards int
}

// NewControlRing creates a generic control ring with configurable size and shards.
//
// Parameters:
//   - size: per-shard capacity (default: 128)
//   - shards: number of shards (default: 1)
func NewControlRing[T any](size, shards int) (*ControlRing[T], error) {
    if shards < 1 {
        shards = 1 // Default: 1 shard
    }
    if size < 1 {
        size = 128 // Default size
    }

    totalCapacity := uint64(size * shards)
    r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
    if err != nil {
        return nil, fmt.Errorf("failed to create control ring: %w", err)
    }
    return &ControlRing[T]{ring: r, shards: shards}, nil
}

// Push writes a control packet to the ring using the given shard ID.
// Thread-safe: can be called from multiple goroutines.
// Returns true if successful, false if ring is full.
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool {
    return r.ring.Write(shardID, packet)
}

// TryPop attempts to pop a control packet from the ring.
// NOT thread-safe: must be called from single consumer (EventLoop).
func (r *ControlRing[T]) TryPop() (T, bool) {
    item, ok := r.ring.TryRead()
    if !ok {
        var zero T
        return zero, false
    }
    cp, ok := item.(T)
    if !ok {
        var zero T
        return zero, false
    }
    return cp, true
}

// Len returns an approximate count of items in the ring.
func (r *ControlRing[T]) Len() int {
    return int(r.ring.Len())
}

// Shards returns the number of shards configured for this ring.
func (r *ControlRing[T]) Shards() int {
    return r.shards
}
```

**Line count:** ~65 lines

**Checkpoint:**
```bash
go build ./congestion/live/common/
```

### Step 1.3: Add Unit Tests for common.ControlRing[T]

**File:** `congestion/live/common/control_ring_test.go` (NEW)

```go
package common

import (
    "testing"
)

// TestPacket is a simple test packet type
type TestPacket struct {
    Type uint8
    Seq  uint32
}

func TestControlRing_Basic(t *testing.T) {
    tests := []struct {
        name   string
        size   int
        shards int
    }{
        {"default", 128, 1},
        {"small", 16, 1},
        {"multi_shard", 64, 2},
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            ring, err := NewControlRing[TestPacket](tc.size, tc.shards)
            if err != nil {
                t.Fatalf("NewControlRing failed: %v", err)
            }
            if ring.Shards() != tc.shards {
                t.Errorf("Shards() = %d, want %d", ring.Shards(), tc.shards)
            }
        })
    }
}

func TestControlRing_PushPop(t *testing.T) {
    ring, _ := NewControlRing[TestPacket](128, 1)

    // Push
    pkt := TestPacket{Type: 1, Seq: 100}
    if !ring.Push(0, pkt) {
        t.Fatal("Push failed")
    }

    // Pop
    got, ok := ring.TryPop()
    if !ok {
        t.Fatal("TryPop failed")
    }
    if got.Type != pkt.Type || got.Seq != pkt.Seq {
        t.Errorf("got %+v, want %+v", got, pkt)
    }

    // Empty
    _, ok = ring.TryPop()
    if ok {
        t.Error("TryPop should return false on empty ring")
    }
}

func TestControlRing_Full(t *testing.T) {
    ring, _ := NewControlRing[TestPacket](4, 1)

    // Fill ring
    for i := 0; i < 4; i++ {
        if !ring.Push(0, TestPacket{Seq: uint32(i)}) {
            t.Fatalf("Push %d failed before ring full", i)
        }
    }

    // Next push should fail (ring full)
    if ring.Push(0, TestPacket{Seq: 999}) {
        t.Error("Push should fail when ring is full")
    }
}

func TestControlRing_Len(t *testing.T) {
    ring, _ := NewControlRing[TestPacket](128, 1)

    if ring.Len() != 0 {
        t.Errorf("Len() = %d, want 0", ring.Len())
    }

    ring.Push(0, TestPacket{Seq: 1})
    ring.Push(0, TestPacket{Seq: 2})

    if ring.Len() != 2 {
        t.Errorf("Len() = %d, want 2", ring.Len())
    }
}
```

**Checkpoint:**
```bash
go test -v ./congestion/live/common/
```

### Step 1.4: Add Benchmarks for common.ControlRing[T]

**File:** `congestion/live/common/control_ring_bench_test.go` (NEW)

```go
package common

import (
    "fmt"
    "testing"
)

func BenchmarkControlRing_Push(b *testing.B) {
    sizes := []int{128, 1024, 8192}
    for _, size := range sizes {
        b.Run(fmt.Sprintf("Size%d", size), func(b *testing.B) {
            ring, _ := NewControlRing[TestPacket](size, 1)
            pkt := TestPacket{Seq: 100}

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                if !ring.Push(0, pkt) {
                    // Drain if full
                    for {
                        if _, ok := ring.TryPop(); !ok {
                            break
                        }
                    }
                }
            }
        })
    }
}

func BenchmarkControlRing_TryPop(b *testing.B) {
    ring, _ := NewControlRing[TestPacket](8192, 1)
    // Pre-fill
    for i := 0; i < 8000; i++ {
        ring.Push(0, TestPacket{Seq: uint32(i)})
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        if _, ok := ring.TryPop(); !ok {
            // Refill
            for j := 0; j < 1000; j++ {
                ring.Push(0, TestPacket{Seq: uint32(j)})
            }
        }
    }
}

func BenchmarkControlRing_PushPop_Balanced(b *testing.B) {
    ring, _ := NewControlRing[TestPacket](128, 1)
    pkt := TestPacket{Seq: 100}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        ring.Push(0, pkt)
        ring.TryPop()
    }
}
```

**Checkpoint:**
```bash
go test -bench=. -benchmem ./congestion/live/common/
```

---

## Phase 2: Receiver Control Ring

**Objective:** Create the receiver-specific control ring that embeds the generic ring.

### Step 2.1: Create RecvControlRing Types

**File:** `congestion/live/receive/control_ring.go` (NEW)

```go
//go:build go1.18

package receive

import (
    "time"
    "github.com/randomizedcoder/gosrt/congestion/live/common"
)

// RecvControlPacketType identifies the type of receiver control packet
type RecvControlPacketType uint8

const (
    RecvControlTypeACKACK RecvControlPacketType = iota
    RecvControlTypeKEEPALIVE
)

// RecvControlPacket wraps an ACKACK or KEEPALIVE for ring transport.
// This is a value type (not pointer) to avoid allocations in the hot path.
type RecvControlPacket struct {
    Type      RecvControlPacketType
    ACKNumber uint32 // For ACKACK: the ACK number being acknowledged
    Timestamp int64  // For ACKACK: arrival time in nanoseconds (time.Now().UnixNano())
}

// RecvControlRing wraps the generic control ring for receiver control packets.
// Push*() is called from io_uring completion handlers (multiple goroutines).
// TryPop() is called from EventLoop (single consumer).
type RecvControlRing struct {
    *common.ControlRing[RecvControlPacket]
}
```

**Line count:** ~35 lines

### Step 2.2: Implement RecvControlRing Methods

**File:** `congestion/live/receive/control_ring.go` (continued)

```go
// NewRecvControlRing creates a receiver control ring.
//
// Parameters:
//   - size: per-shard capacity (default: 128)
//   - shards: number of shards (default: 1)
//
// Reference: completely_lockfree_receiver.md Section 5.1.2
func NewRecvControlRing(size, shards int) (*RecvControlRing, error) {
    ring, err := common.NewControlRing[RecvControlPacket](size, shards)
    if err != nil {
        return nil, err
    }
    return &RecvControlRing{ring}, nil
}

// PushACKACK pushes an ACKACK to the control ring.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if successful, false if ring is full.
//
// The arrivalTime is captured at push time to ensure accurate RTT calculation.
func (r *RecvControlRing) PushACKACK(ackNum uint32, arrivalTime time.Time) bool {
    return r.Push(uint64(RecvControlTypeACKACK), RecvControlPacket{
        Type:      RecvControlTypeACKACK,
        ACKNumber: ackNum,
        Timestamp: arrivalTime.UnixNano(),
    })
}

// PushKEEPALIVE pushes a KEEPALIVE to the control ring.
// Thread-safe: can be called from multiple goroutines (io_uring handlers).
// Returns true if successful, false if ring is full.
func (r *RecvControlRing) PushKEEPALIVE() bool {
    return r.Push(uint64(RecvControlTypeKEEPALIVE), RecvControlPacket{
        Type: RecvControlTypeKEEPALIVE,
    })
}
```

**Line count:** ~40 additional lines (~75 total)

**Checkpoint:**
```bash
go build ./congestion/live/receive/
```

### Step 2.3: Add Unit Tests for RecvControlRing

**File:** `congestion/live/receive/control_ring_test.go` (NEW)

```go
package receive

import (
    "testing"
    "time"
)

func TestRecvControlRing_Basic(t *testing.T) {
    ring, err := NewRecvControlRing(128, 1)
    if err != nil {
        t.Fatalf("NewRecvControlRing failed: %v", err)
    }

    if ring.Len() != 0 {
        t.Errorf("Len() = %d, want 0", ring.Len())
    }
}

func TestRecvControlRing_PushACKACK(t *testing.T) {
    ring, _ := NewRecvControlRing(128, 1)
    now := time.Now()

    if !ring.PushACKACK(42, now) {
        t.Fatal("PushACKACK failed")
    }

    pkt, ok := ring.TryPop()
    if !ok {
        t.Fatal("TryPop failed")
    }

    if pkt.Type != RecvControlTypeACKACK {
        t.Errorf("Type = %d, want %d", pkt.Type, RecvControlTypeACKACK)
    }
    if pkt.ACKNumber != 42 {
        t.Errorf("ACKNumber = %d, want 42", pkt.ACKNumber)
    }
    if pkt.Timestamp != now.UnixNano() {
        t.Errorf("Timestamp = %d, want %d", pkt.Timestamp, now.UnixNano())
    }
}

func TestRecvControlRing_PushKEEPALIVE(t *testing.T) {
    ring, _ := NewRecvControlRing(128, 1)

    if !ring.PushKEEPALIVE() {
        t.Fatal("PushKEEPALIVE failed")
    }

    pkt, ok := ring.TryPop()
    if !ok {
        t.Fatal("TryPop failed")
    }

    if pkt.Type != RecvControlTypeKEEPALIVE {
        t.Errorf("Type = %d, want %d", pkt.Type, RecvControlTypeKEEPALIVE)
    }
}

func TestRecvControlRing_Mixed(t *testing.T) {
    ring, _ := NewRecvControlRing(128, 1)
    now := time.Now()

    ring.PushACKACK(1, now)
    ring.PushKEEPALIVE()
    ring.PushACKACK(2, now.Add(time.Millisecond))

    // Should get packets in order
    pkt1, _ := ring.TryPop()
    if pkt1.Type != RecvControlTypeACKACK || pkt1.ACKNumber != 1 {
        t.Errorf("pkt1: got %+v", pkt1)
    }

    pkt2, _ := ring.TryPop()
    if pkt2.Type != RecvControlTypeKEEPALIVE {
        t.Errorf("pkt2: got %+v", pkt2)
    }

    pkt3, _ := ring.TryPop()
    if pkt3.Type != RecvControlTypeACKACK || pkt3.ACKNumber != 2 {
        t.Errorf("pkt3: got %+v", pkt3)
    }
}

func TestRecvControlRing_Full_Fallback(t *testing.T) {
    ring, _ := NewRecvControlRing(4, 1)
    now := time.Now()

    // Fill ring
    for i := 0; i < 4; i++ {
        if !ring.PushACKACK(uint32(i), now) {
            t.Fatalf("Push %d failed before ring full", i)
        }
    }

    // Next push should fail (ring full)
    if ring.PushACKACK(999, now) {
        t.Error("Push should fail when ring is full")
    }
}
```

**Checkpoint:**
```bash
go test -v ./congestion/live/receive/ -run TestRecvControlRing
```

### Step 2.4: Add Benchmarks for RecvControlRing

**File:** `congestion/live/receive/control_ring_bench_test.go` (NEW)

```go
package receive

import (
    "testing"
    "time"
)

func BenchmarkRecvControlRing_PushACKACK(b *testing.B) {
    ring, _ := NewRecvControlRing(8192, 1)
    now := time.Now()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        if !ring.PushACKACK(uint32(i), now) {
            // Drain if full
            for {
                if _, ok := ring.TryPop(); !ok {
                    break
                }
            }
            ring.PushACKACK(uint32(i), now)
        }
    }
}

func BenchmarkRecvControlRing_PushKEEPALIVE(b *testing.B) {
    ring, _ := NewRecvControlRing(8192, 1)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        if !ring.PushKEEPALIVE() {
            for {
                if _, ok := ring.TryPop(); !ok {
                    break
                }
            }
            ring.PushKEEPALIVE()
        }
    }
}

func BenchmarkRecvControlRing_PushACKACK_Concurrent(b *testing.B) {
    ring, _ := NewRecvControlRing(65536, 1)
    now := time.Now()

    b.RunParallel(func(pb *testing.PB) {
        seq := uint32(0)
        for pb.Next() {
            ring.PushACKACK(seq, now)
            seq++
        }
    })
}

func BenchmarkRecvControlRing_TryPop_SingleConsumer(b *testing.B) {
    ring, _ := NewRecvControlRing(65536, 1)
    now := time.Now()

    // Pre-fill
    for i := 0; i < 60000; i++ {
        ring.PushACKACK(uint32(i), now)
    }

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        if _, ok := ring.TryPop(); !ok {
            // Refill
            for j := 0; j < 10000; j++ {
                ring.PushACKACK(uint32(j), now)
            }
        }
    }
}
```

**Checkpoint:**
```bash
go test -bench=BenchmarkRecvControlRing -benchmem ./congestion/live/receive/
```

---

## Phase 3: Configuration and CLI Flags

**Objective:** Add configuration options and CLI flags for receiver control ring.

### Step 3.1: Add Config Options

**File:** `config.go`

**Location:** After line 455 (after `SendControlRingShards`)

**Add:**
```go
    // ═══════════════════════════════════════════════════════════════════════
    // Receiver Control Ring Configuration
    // ═══════════════════════════════════════════════════════════════════════

    // UseRecvControlRing enables lock-free ring for ACKACK/KEEPALIVE routing.
    // When enabled with UseEventLoop, the receiver is completely lock-free.
    // Default: false (for backward compatibility)
    UseRecvControlRing bool

    // RecvControlRingSize is the control ring capacity per shard.
    // Default: 128
    RecvControlRingSize int

    // RecvControlRingShards is the number of control ring shards.
    // Default: 1
    RecvControlRingShards int
```

**Checkpoint:**
```bash
go build .
```

### Step 3.2: Add CLI Flags

**File:** `contrib/common/flags.go`

**Location:** After line 105 (after `SendControlRingShards` flag)

**Add:**
```go
    // Receiver Control Ring flags
    RecvControlRing       = flag.Bool("recvcontrolring", false, "Enable receiver control ring for lock-free ACKACK processing")
    RecvControlRingSize   = flag.Int("recvcontrolringsize", 128, "Receiver control ring size (default: 128)")
    RecvControlRingShards = flag.Int("recvcontrolringshards", 1, "Receiver control ring shards (default: 1)")
```

**Location:** In `ApplyFlagsToConfig()` function (around line 440-450)

**Add:**
```go
    // Receiver Control Ring
    if FlagSet["recvcontrolring"] {
        config.UseRecvControlRing = *RecvControlRing
    }
    if FlagSet["recvcontrolringsize"] {
        config.RecvControlRingSize = *RecvControlRingSize
    }
    if FlagSet["recvcontrolringshards"] {
        config.RecvControlRingShards = *RecvControlRingShards
    }
```

**Checkpoint:**
```bash
go build ./contrib/server/
go build ./contrib/client/
go build ./contrib/client-generator/
```

### Step 3.3: Update Default Config

**File:** `config.go`

**Location:** In `DefaultConfig` var (around line 680-705)

**Add after `SendControlRingShards`:**
```go
    UseRecvControlRing:    false, // Legacy path by default
    RecvControlRingSize:   128,   // Per-shard capacity
    RecvControlRingShards: 1,     // Single shard
```

**Checkpoint:**
```bash
go build .
go test -run TestDefaultConfig .
```

### Step 3.4: Update Sender Config Defaults (Consolidation)

**File:** `config.go`

**Location:** Lines 682-683

**Change FROM:**
```go
    SendControlRingSize:   256,   // Per-shard capacity
    SendControlRingShards: 2,     // 2 shards (ACK/NAK separation)
```

**Change TO:**
```go
    SendControlRingSize:   128,   // Per-shard capacity (unified with receiver)
    SendControlRingShards: 1,     // Single shard (unified with receiver)
```

**⚠️ Breaking Change:** Document in release notes.

**Checkpoint:**
```bash
go test -run TestSender ./congestion/live/send/
```

### Step 3.5: Add Flag Tests

**File:** `contrib/common/test_flags.sh`

**Location:** After existing sender control ring tests

**Add:**
```bash
# ═══════════════════════════════════════════════════════════════════════════
# Receiver Control Ring flag tests
# ═══════════════════════════════════════════════════════════════════════════

run_test "recvcontrolring flag" \
    "-recvcontrolring" \
    "UseRecvControlRing:.*true" \
    "$SERVER_BIN"

run_test "recvcontrolringsize flag" \
    "-recvcontrolringsize=256" \
    "RecvControlRingSize:.*256" \
    "$SERVER_BIN"

run_test "recvcontrolringshards flag" \
    "-recvcontrolringshards=2" \
    "RecvControlRingShards:.*2" \
    "$SERVER_BIN"

run_test "combined recv control ring flags" \
    "-recvcontrolring -recvcontrolringsize=128 -recvcontrolringshards=1" \
    "UseRecvControlRing:.*true.*RecvControlRingSize:.*128.*RecvControlRingShards:.*1" \
    "$SERVER_BIN"

run_test "full lock-free eventloop" \
    "-eventloop -sendcontrolring -recvcontrolring" \
    "UseEventLoop:.*true.*UseSendControlRing:.*true.*UseRecvControlRing:.*true" \
    "$SERVER_BIN"
```

**Checkpoint:**
```bash
make test-flags
```

---

## Phase 4: Metrics

**Objective:** Add metrics for receiver control ring operations.

### Step 4.1: Add Receiver Control Ring Metrics

**File:** `metrics/metrics.go`

**Location:** After sender control ring metrics (around line 150-170)

**Add:**
```go
    // ═══════════════════════════════════════════════════════════════════════
    // Receiver Control Ring Metrics
    // ═══════════════════════════════════════════════════════════════════════

    // Control Ring Push/Drop
    RecvControlRingPushedACKACK    atomic.Uint64
    RecvControlRingDroppedACKACK   atomic.Uint64
    RecvControlRingPushedKEEPALIVE atomic.Uint64
    RecvControlRingDroppedKEEPALIVE atomic.Uint64

    // Control Ring Drain
    RecvControlRingDrained         atomic.Uint64
    RecvControlRingProcessed       atomic.Uint64
    RecvControlRingProcessedACKACK atomic.Uint64
    RecvControlRingProcessedKEEPALIVE atomic.Uint64
```

**Checkpoint:**
```bash
go build ./metrics/
```

### Step 4.2: Export Prometheus Metrics

**File:** `metrics/handler.go`

**Location:** In `Handler()` function, after sender control ring metrics

**Add:**
```go
    // Receiver Control Ring
    fmt.Fprintf(w, "recv_control_ring_pushed_ackack_total %d\n", m.RecvControlRingPushedACKACK.Load())
    fmt.Fprintf(w, "recv_control_ring_dropped_ackack_total %d\n", m.RecvControlRingDroppedACKACK.Load())
    fmt.Fprintf(w, "recv_control_ring_pushed_keepalive_total %d\n", m.RecvControlRingPushedKEEPALIVE.Load())
    fmt.Fprintf(w, "recv_control_ring_dropped_keepalive_total %d\n", m.RecvControlRingDroppedKEEPALIVE.Load())
    fmt.Fprintf(w, "recv_control_ring_drained_total %d\n", m.RecvControlRingDrained.Load())
    fmt.Fprintf(w, "recv_control_ring_processed_total %d\n", m.RecvControlRingProcessed.Load())
    fmt.Fprintf(w, "recv_control_ring_processed_ackack_total %d\n", m.RecvControlRingProcessedACKACK.Load())
    fmt.Fprintf(w, "recv_control_ring_processed_keepalive_total %d\n", m.RecvControlRingProcessedKEEPALIVE.Load())
```

**Checkpoint:**
```bash
go build ./metrics/
go test -run TestPrometheus ./metrics/
```

### Step 4.3: Add Metrics Tests

**File:** `metrics/metrics_test.go`

**Add test:**
```go
func TestRecvControlRingMetrics(t *testing.T) {
    m := NewConnectionMetrics()

    m.RecvControlRingPushedACKACK.Add(10)
    m.RecvControlRingDroppedACKACK.Add(1)
    m.RecvControlRingProcessedACKACK.Add(9)

    // Verify invariant: pushed == processed + dropped
    pushed := m.RecvControlRingPushedACKACK.Load()
    processed := m.RecvControlRingProcessedACKACK.Load()
    dropped := m.RecvControlRingDroppedACKACK.Load()

    if pushed != processed+dropped {
        t.Errorf("Invariant violated: pushed(%d) != processed(%d) + dropped(%d)", pushed, processed, dropped)
    }
}
```

**Checkpoint:**
```bash
go test -v ./metrics/ -run TestRecvControlRingMetrics
```

---

## Phase 5: Lock-Free Function Variants

**Objective:** Create lock-free variants of ACKACK and KEEPALIVE handlers.

### Step 5.1: Create handleACKACK Lock-Free Variant

**File:** `connection_handlers.go`

**Location:** Lines 368-419 (current `handleACKACK`)

**Rename** current function to `handleACKACKLocked` and create new lock-free version:

**NEW function (insert before handleACKACKLocked):**
```go
// handleACKACK is the lock-free variant for EventLoop mode.
// Called when control packets arrive via RecvControlRing.
//
// Parameters:
//   - ackNum: the ACK number from the ACKACK packet
//   - arrivalTime: when the ACKACK arrived (captured at ring push time)
//
// Reference: completely_lockfree_receiver.md Section 5.1
func (c *srtConn) handleACKACK(ackNum uint32, arrivalTime time.Time) {
    c.AssertEventLoopContext() // MUST be first line

    // Note: NO LOCK - EventLoop is single-threaded consumer of ackNumbers btree

    entry := c.ackNumbers.Get(ackNum)
    btreeLen := c.ackNumbers.Len()

    if entry != nil {
        // 4.10. Round-Trip Time Estimation
        rttDuration := arrivalTime.Sub(entry.timestamp)
        c.recalculateRTT(rttDuration)

        c.log("control:recv:ACKACK:rtt:debug", func() string {
            return fmt.Sprintf("ACKACK RTT (EventLoop): ackNum=%d, entryTimestamp=%s, arrivalTime=%s, rtt=%v, btreeLen=%d",
                ackNum, entry.timestamp.Format("15:04:05.000000"), arrivalTime.Format("15:04:05.000000"),
                rttDuration, btreeLen)
        })

        c.ackNumbers.Delete(ackNum)
        PutAckEntry(entry) // Return to pool
    } else {
        c.log("control:recv:ACKACK:error", func() string {
            return fmt.Sprintf("got unknown ACKACK (%d), btreeLen=%d", ackNum, btreeLen)
        })
        if c.metrics != nil {
            c.metrics.PktRecvInvalid.Add(1)
            c.metrics.AckBtreeUnknownACKACK.Add(1)
        }
    }

    // Bulk cleanup of stale entries
    expiredCount, expired := c.ackNumbers.ExpireOlderThan(ackNum)
    btreeLenAfter := c.ackNumbers.Len()

    // Update metrics
    if c.metrics != nil {
        c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
        c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
        c.metrics.RecvControlRingProcessedACKACK.Add(1)
    }

    // Return expired entries to pool
    PutAckEntries(expired)

    c.recv.SetNAKInterval(uint64(c.rtt.NAKInterval()))
}
```

### Step 5.2: Create handleACKACKLocked Wrapper

**File:** `connection_handlers.go`

**MODIFY** existing `handleACKACK` (lines 368-419):

**Rename to `handleACKACKLocked` and update:**
```go
// handleACKACKLocked is the locking wrapper for Tick/legacy mode.
// Called from io_uring handlers when control ring is NOT enabled.
// Acquires c.ackLock to protect ackNumbers btree access.
func (c *srtConn) handleACKACKLocked(p packet.Packet) {
    c.AssertTickContext() // MUST be first line

    c.log("control:recv:ACKACK:dump", func() string { return p.Dump() })

    ackNum := p.Header().TypeSpecific
    arrivalTime := time.Now()

    c.ackLock.Lock()
    defer c.ackLock.Unlock()

    // Delegate to lock-free implementation (btree access is now protected by ackLock)
    // Note: We temporarily enter "EventLoop context" for the assert in handleACKACK
    // This is safe because we hold the lock.

    entry := c.ackNumbers.Get(ackNum)
    btreeLen := c.ackNumbers.Len()

    if entry != nil {
        rttDuration := arrivalTime.Sub(entry.timestamp)
        c.recalculateRTT(rttDuration)

        c.log("control:recv:ACKACK:rtt:debug", func() string {
            return fmt.Sprintf("ACKACK RTT (Locked): ackNum=%d, rtt=%v, btreeLen=%d",
                ackNum, rttDuration, btreeLen)
        })

        c.ackNumbers.Delete(ackNum)
        PutAckEntry(entry)
    } else {
        c.log("control:recv:ACKACK:error", func() string {
            return fmt.Sprintf("got unknown ACKACK (%d), btreeLen=%d", ackNum, btreeLen)
        })
        if c.metrics != nil {
            c.metrics.PktRecvInvalid.Add(1)
            c.metrics.AckBtreeUnknownACKACK.Add(1)
        }
    }

    expiredCount, expired := c.ackNumbers.ExpireOlderThan(ackNum)
    btreeLenAfter := c.ackNumbers.Len()

    // Unlock before pool operations (not needed for metrics updates)
    // Note: Lock is already deferred, so it will unlock after this function returns

    if c.metrics != nil {
        c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
        c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
    }

    PutAckEntries(expired)

    c.recv.SetNAKInterval(uint64(c.rtt.NAKInterval()))
}
```

### Step 5.3: Create handleKeepAlive Lock-Free Variant

**File:** `connection_handlers.go`

**Location:** After line 203 (after current `handleKeepAlive`)

**NEW function:**
```go
// handleKeepAliveEventLoop is the lock-free variant for EventLoop mode.
// Called when KEEPALIVE packets arrive via RecvControlRing.
func (c *srtConn) handleKeepAliveEventLoop() {
    c.AssertEventLoopContext() // MUST be first line

    c.resetPeerIdleTimeout()

    if c.metrics != nil {
        c.metrics.RecvControlRingProcessedKEEPALIVE.Add(1)
    }

    c.log("control:recv:keepalive:eventloop", func() string {
        return "keepalive processed via EventLoop"
    })
}
```

### Step 5.4: Create handleKeepAliveLocked Wrapper

**File:** `connection_handlers.go`

**MODIFY** existing `handleKeepAlive` (lines 191-203):

```go
// handleKeepAlive resets the idle timeout and sends a keepalive to the peer.
// This is the locking wrapper for Tick/legacy mode.
func (c *srtConn) handleKeepAlive(p packet.Packet) {
    c.AssertTickContext() // MUST be first line

    c.log("control:recv:keepalive:dump", func() string { return p.Dump() })

    c.resetPeerIdleTimeout()

    c.log("control:send:keepalive:dump", func() string { return p.Dump() })

    c.pop(p)
}
```

### Step 5.5: Add Context Asserts to Existing Receiver Functions

**File:** `congestion/live/receive/ack.go`

**Location:** Line 131 (start of `periodicACK`)

**Add after function signature:**
```go
func (r *receiver) periodicACK(now uint64) (ok bool, sequenceNumber circular.Number, lite bool) {
    r.AssertEventLoopContext() // MUST be first line
    // ... rest of function
}
```

**Location:** Line 14 (start of `periodicACKLocked`)

**Add after function signature:**
```go
func (r *receiver) periodicACKLocked(now uint64) (ok bool, seq circular.Number, lite bool) {
    r.AssertTickContext() // MUST be first line
    // ... rest of function
}
```

**File:** `congestion/live/receive/nak.go`

**Location:** Line 79 (start of `periodicNAK`)

**Add after function signature:**
```go
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    r.AssertEventLoopContext() // MUST be first line
    // ... rest of function
}
```

**File:** `congestion/live/receive/tick.go`

**Location:** Line 424 (start of `deliverReadyPacketsWithTime`) and similar functions

**Add asserts to all delivery/scan functions as listed in design doc Section 5.7.**

**Checkpoint:**
```bash
go build ./congestion/live/receive/
go test -v ./congestion/live/receive/ -run TestPeriodicACK
```

### Step 5.6: Add Unit Tests for Lock-Free Variants

**File:** `connection_ackack_test.go` (NEW or existing)

```go
func TestHandleACKACK_EventLoop(t *testing.T) {
    conn := newTestConnection()
    conn.EnterEventLoop()
    defer conn.ExitEventLoop()

    // Add an entry to ackNumbers
    entry := GetAckEntry()
    entry.ackNum = 42
    entry.timestamp = time.Now().Add(-10 * time.Millisecond)
    conn.ackNumbers.Insert(entry)

    // Process ACKACK
    arrivalTime := time.Now()
    conn.handleACKACK(42, arrivalTime)

    // Verify entry was removed
    if conn.ackNumbers.Get(42) != nil {
        t.Error("entry should have been removed")
    }

    // Verify RTT was updated
    if conn.rtt.RTT() == 0 {
        t.Error("RTT should have been updated")
    }
}

func TestHandleACKACK_Unknown(t *testing.T) {
    conn := newTestConnection()
    conn.EnterEventLoop()
    defer conn.ExitEventLoop()

    // Process ACKACK for unknown entry
    conn.handleACKACK(999, time.Now())

    // Should have incremented invalid metric
    if conn.metrics.AckBtreeUnknownACKACK.Load() != 1 {
        t.Error("should have incremented unknown ACKACK metric")
    }
}
```

**Checkpoint:**
```bash
go test -v . -run TestHandleACKACK
```

---

## Phase 6: Control Ring Integration

**Objective:** Add control ring to connection and route control packets through it.

### Step 6.1: Add Control Ring to srtConn

**File:** `connection.go`

**Location:** In `srtConn` struct (around line 100-150)

**Add:**
```go
    // Receiver Control Ring (Phase 5: Completely Lock-Free Receiver)
    // Routes ACKACK and KEEPALIVE to EventLoop for lock-free processing.
    // nil means disabled, non-nil means enabled (no separate bool needed).
    // Reference: completely_lockfree_receiver.md Section 6.1.5
    recvControlRing *receive.RecvControlRing
```

**Note:** No separate `useRecvControlRing bool` - just check `recvControlRing != nil`.
This follows the consolidated design in Section 6.1.2 of the design document.

**Checkpoint:**
```bash
go build .
```

### Step 6.2: Initialize Control Ring on Connection

**File:** `connection.go`

**Location:** In connection initialization (look for where sender control ring is initialized)

**Add:**
```go
    // Initialize receiver control ring if enabled
    // Note: No separate bool - recvControlRing != nil means enabled
    if c.config.UseRecvControlRing && c.config.UseEventLoop {
        ring, err := receive.NewRecvControlRing(
            c.config.RecvControlRingSize,
            c.config.RecvControlRingShards,
        )
        if err != nil {
            // Log error but continue - will use locked path (ring stays nil)
            c.log("connection:init:error", func() string {
                return fmt.Sprintf("failed to create recv control ring: %v", err)
            })
        } else {
            c.recvControlRing = ring  // Set only on success
        }
    }
```

**Checkpoint:**
```bash
go build .
go test -v . -run TestConnection
```

### Step 6.3: Route ACKACK Through Control Ring

**File:** `connection_handlers.go`

**Location:** Update control packet dispatch table (line 52)

**Change FROM:**
```go
    packet.CTRLTYPE_ACKACK: (*srtConn).handleACKACK,
```

**Create new dispatch function:**
```go
// dispatchACKACK routes ACKACK to control ring or locked handler.
// Simplified: just check recvControlRing != nil (no separate bool).
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    if c.recvControlRing != nil {
        // Push to control ring for EventLoop processing
        ackNum := p.Header().TypeSpecific
        arrivalTime := time.Now()

        if c.recvControlRing.PushACKACK(ackNum, arrivalTime) {
            if c.metrics != nil {
                c.metrics.RecvControlRingPushedACKACK.Add(1)
            }
            return
        }

        // Ring full - fall through to locked path
        if c.metrics != nil {
            c.metrics.RecvControlRingDroppedACKACK.Add(1)
        }
    }

    // Locked path (ring disabled or full)
    c.handleACKACKLocked(p)
}
```

**Update dispatch table:**
```go
    packet.CTRLTYPE_ACKACK: (*srtConn).dispatchACKACK,
```

### Step 6.4: Route KEEPALIVE Through Control Ring

**File:** `connection_handlers.go`

**Location:** Line 48 (dispatch table)

**Create new dispatch function:**
```go
// dispatchKeepAlive routes KEEPALIVE to control ring or locked handler.
// Simplified: just check recvControlRing != nil (no separate bool).
func (c *srtConn) dispatchKeepAlive(p packet.Packet) {
    if c.recvControlRing != nil {
        if c.recvControlRing.PushKEEPALIVE() {
            if c.metrics != nil {
                c.metrics.RecvControlRingPushedKEEPALIVE.Add(1)
            }
            return
        }

        if c.metrics != nil {
            c.metrics.RecvControlRingDroppedKEEPALIVE.Add(1)
        }
    }

    // Locked path
    c.handleKeepAlive(p)
}
```

**Update dispatch table:**
```go
    packet.CTRLTYPE_KEEPALIVE: (*srtConn).dispatchKeepAlive,
```

**Checkpoint:**
```bash
go build .
go test -v . -run TestControlPacket
```

---

## Phase 7: EventLoop Integration

**Objective:** Add control ring processing to receiver EventLoop.

### Step 7.1: Add processControlPackets to Receiver EventLoop

**File:** `congestion/live/receive/tick.go`

**Location:** After EventLoop function (around line 142)

**NEW function:**
```go
// processControlPackets drains control packets from the ring and processes them.
// Called from EventLoop - completely lock-free (single-threaded btree access).
//
// Returns the number of control packets processed.
func (r *receiver) processControlPackets(processACKACK func(ackNum uint32, ts time.Time), processKEEPALIVE func()) int {
    r.AssertEventLoopContext()

    if r.controlRing == nil {
        return 0
    }

    count := 0
    for {
        cp, ok := r.controlRing.TryPop()
        if !ok {
            break
        }

        count++
        switch cp.Type {
        case RecvControlTypeACKACK:
            arrivalTime := time.Unix(0, cp.Timestamp)
            processACKACK(cp.ACKNumber, arrivalTime)
        case RecvControlTypeKEEPALIVE:
            processKEEPALIVE()
        }
    }

    return count
}
```

**Note:** The actual ACKACK/KEEPALIVE handlers are passed as function pointers from the connection level.

### Step 7.2: Add Control Ring Drain to EventLoop

**File:** `congestion/live/receive/tick.go`

**Location:** In EventLoop function (around line 142-200)

**Add in EventLoop select default case (where data ring is drained):**

```go
        default:
            // 1. Drain data ring → process packets
            dataDrained := r.drainRingByDelta(now, batchSize)
            if dataDrained > 0 {
                m.RecvEventLoopDataDrained.Add(uint64(dataDrained))
            }

            // 2. Drain control ring → process ACKACK/KEEPALIVE
            // Note: Control processing is done via callback from connection level
            // This is handled in the connection's EventLoop wrapper
```

**The actual integration happens at the connection level** because `handleACKACK` needs access to `c.ackNumbers` btree which is at connection level.

### Step 7.3: Function Dispatch Setup

**File:** `connection.go`

**Location:** In connection initialization

**Add EventLoop wrapper that drains both data and control rings:**

```go
// recvEventLoopWithControlRing runs the receiver EventLoop with control ring draining.
func (c *srtConn) recvEventLoopWithControlRing(ctx context.Context) {
    c.EnterEventLoop()
    defer c.ExitEventLoop()

    // Run receiver's EventLoop in a goroutine
    go c.recv.EventLoop(ctx)

    // Control ring drain loop
    controlTicker := time.NewTicker(100 * time.Microsecond) // 10kHz check
    defer controlTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-controlTicker.C:
            c.drainRecvControlRing()
        }
    }
}

// drainRecvControlRing processes all pending control packets.
func (c *srtConn) drainRecvControlRing() {
    if c.recvControlRing == nil {
        return
    }

    for {
        cp, ok := c.recvControlRing.TryPop()
        if !ok {
            break
        }

        switch cp.Type {
        case receive.RecvControlTypeACKACK:
            arrivalTime := time.Unix(0, cp.Timestamp)
            c.handleACKACK(cp.ACKNumber, arrivalTime)
        case receive.RecvControlTypeKEEPALIVE:
            c.handleKeepAliveEventLoop()
        }

        if c.metrics != nil {
            c.metrics.RecvControlRingDrained.Add(1)
        }
    }
}
```

**Checkpoint:**
```bash
go build .
go test -v . -run TestEventLoop
```

### Step 7.4: Verify Metrics Usage

After completing Phase 7, all receiver control ring metrics should be in use.

**Checkpoint:**
```bash
# Verify all RecvControlRing metrics are now used (not just defined)
make audit-metrics

# Expected: RecvControlRing* metrics should no longer appear in
# "Defined but never used" list
```

**Metrics now in use:**
| Metric | Used In |
|--------|---------|
| `RecvControlRingPushedACKACK` | `dispatchACKACK()` in `connection_handlers.go` |
| `RecvControlRingPushedKEEPALIVE` | `dispatchKeepAlive()` in `connection_handlers.go` |
| `RecvControlRingDroppedACKACK` | `dispatchACKACK()` fallback path |
| `RecvControlRingDroppedKEEPALIVE` | `dispatchKeepAlive()` fallback path |
| `RecvControlRingDrained` | `drainRecvControlRing()` in `connection.go` |
| `RecvControlRingProcessed` | `drainRecvControlRing()` per-packet |
| `RecvControlRingProcessedACKACK` | `handleACKACK()` in `connection_handlers.go` |
| `RecvControlRingProcessedKEEPALIVE` | `handleKeepAliveEventLoop()` in `connection_handlers.go` |

---

## Phase 8: Refactor SendControlRing to Use common

**Objective:** Update sender to use the shared generic control ring AND remove redundant `useControlRing` bool.

### Step 8.0: Remove Redundant useControlRing bool (Sender Cleanup)

**File:** `congestion/live/send/sender.go`

**Location:** Line 174

**Remove:**
```go
    useControlRing bool  // REMOVE - redundant with controlRing != nil
```

**Update these files to use `s.controlRing != nil` instead of `s.useControlRing`:**

| File | Line | Change |
|------|------|--------|
| `ack.go` | 16 | `if s.useControlRing` → `if s.controlRing != nil` |
| `nak.go` | 21 | `if s.useControlRing` → `if s.controlRing != nil` |
| `tick.go` | 59 | `if s.useControlRing` → `if s.controlRing != nil` |
| `sender.go` | 284 | Remove `s.useControlRing = true` line |

**Update tests** that reference `useControlRing`:
- `sender_config_test.go` lines 313, 316

**Checkpoint:**
```bash
go build ./congestion/live/send/
go test -v ./congestion/live/send/ -run TestSender
```

### Step 8.1: Update SendControlRing to Embed common.ControlRing

**File:** `congestion/live/send/control_ring.go`

**Change FROM:**
```go
type SendControlRing struct {
    ring   *ring.ShardedRing
    shards int
}
```

**Change TO:**
```go
import (
    "github.com/randomizedcoder/gosrt/congestion/live/common"
)

type SendControlRing struct {
    *common.ControlRing[ControlPacket]
}

func NewSendControlRing(size, shards int) (*SendControlRing, error) {
    ring, err := common.NewControlRing[ControlPacket](size, shards)
    if err != nil {
        return nil, err
    }
    return &SendControlRing{ring}, nil
}

// PushACK - keep existing signature, delegate to generic ring
func (r *SendControlRing) PushACK(seq circular.Number) bool {
    cp := ControlPacket{
        Type:        ControlTypeACK,
        ACKSequence: seq.Val(),
    }
    return r.Push(uint64(ControlTypeACK), cp)
}

// ... similar for PushNAK, TryPop
```

### Step 8.2: Verify Sender Tests Still Pass

**Checkpoint:**
```bash
go test -v ./congestion/live/send/ -run TestSendControlRing
go test -v ./congestion/live/send/ -run TestSender
go test -bench=BenchmarkSendControlRing ./congestion/live/send/
```

---

## Phase 9: Integration Testing

**Objective:** Add integration tests for completely lock-free receiver.

### Step 9.1: Add Test Configuration

**File:** `contrib/integration_testing/test_configs.go`

**Add:**
```go
// ConfigFullELLockFree is completely lock-free EventLoop (sender + receiver)
var ConfigFullELLockFree = SRTConfig{
    // Packet Store
    UseBtree: true,

    // io_uring
    IOUringEnabled:     true,
    IOUringRecvEnabled: true,

    // NAK Btree
    UseNakBtree:          true,
    FastNakEnabled:       true,
    FastNakRecentEnabled: true,
    HonorNakOrder:        true,

    // Sender Lock-Free
    UsePacketRing:      true,
    UseSendControlRing: true,
    SendControlRingSize: 128,

    // Receiver Lock-Free
    UseEventLoop:       true,
    UseRecvControlRing: true,
    RecvControlRingSize: 128,
}
```

### Step 9.2: Add Isolation Tests

**File:** `contrib/integration_testing/test_configs.go`

**Add test definitions:**
```go
// Isolation-5M-RecvControlRing
{
    Name:        "Isolation-5M-RecvControlRing",
    Description: "Receiver control ring only",
    Config:      ConfigRecvControlRingOnly,
    Bitrate:     5_000_000,
    Duration:    30 * time.Second,
}

// Isolation-5M-FullELLockFree
{
    Name:        "Isolation-5M-FullELLockFree",
    Description: "Completely lock-free EventLoop",
    Config:      ConfigFullELLockFree,
    Bitrate:     5_000_000,
    Duration:    30 * time.Second,
}
```

### Step 9.3: Add Parallel Tests

**File:** `contrib/integration_testing/test_configs.go`

**Add:**
```go
// Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF
{
    Name: "Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF",
    Baseline: PipelineConfig{
        Name: "FullEL",
        SRT:  ConfigFullEL,
    },
    HighPerf: PipelineConfig{
        Name: "FullELLF",
        SRT:  ConfigFullELLockFree,
    },
    Bitrate:  20_000_000,
    Duration: 60 * time.Second,
}
```

### Step 9.4: Run All Tests

**Checkpoint:**
```bash
# Unit tests
go test -v ./congestion/live/common/
go test -v ./congestion/live/receive/ -run TestRecvControlRing
go test -v . -run TestHandleACKACK

# Flag tests
make test-flags

# Isolation tests
make test-isolation TEST=Isolation-5M-RecvControlRing
make test-isolation TEST=Isolation-5M-FullELLockFree

# Parallel tests
make test-parallel TEST=Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF

# Race detector
go test -race ./congestion/live/receive/ -run TestRecvControlRing
go test -race . -run TestHandleACKACK
```

---

## Phase 10: Documentation and Cleanup

### Step 10.1: Update Integration Test Docs

**Files to update:**
- `documentation/integration_testing_matrix_design.md` - Add `FullELLF` config
- `documentation/parallel_isolation_test_plan.md` - Add lock-free tests

### Step 10.2: Naming Refactoring (Optional)

**Rename for consistency (can be done in separate PR):**

| Current Name | New Name | File | Line |
|--------------|----------|------|------|
| `deliverReadyPacketsNoLock` | `deliverReadyPackets` | `tick.go` | 481 |
| `deliverReadyPacketsWithTime` | `deliverReadyPackets` | `tick.go` | 424 |
| `contiguousScanWithTime` | `contiguousScan` | `scan.go` | 83 |

---

## Summary: Implementation Order

| Phase | Duration | Key Deliverables |
|-------|----------|------------------|
| Phase 1 | 1 day | `common.ControlRing[T]` generic ring |
| Phase 2 | 0.5 day | `RecvControlRing` types and methods |
| Phase 3 | 0.5 day | Config options, CLI flags |
| Phase 4 | 0.5 day | Metrics |
| Phase 5 | 1 day | Lock-free function variants |
| Phase 6 | 1 day | Control ring integration |
| Phase 7 | 1 day | EventLoop integration |
| Phase 8 | 0.5 day | Refactor SendControlRing + remove redundant bool |
| Phase 9 | 1 day | Integration tests |
| Phase 10 | 0.5 day | Documentation |
| **Total** | **~7-8 days** | Completely lock-free receiver |

---

## Appendix: File Summary

### New Files

| File | Description | Lines (est) |
|------|-------------|-------------|
| `congestion/live/common/doc.go` | Package documentation | 5 |
| `congestion/live/common/control_ring.go` | Generic control ring | 65 |
| `congestion/live/common/control_ring_test.go` | Unit tests | 80 |
| `congestion/live/common/control_ring_bench_test.go` | Benchmarks | 50 |
| `congestion/live/receive/control_ring.go` | Receiver control ring | 75 |
| `congestion/live/receive/control_ring_test.go` | Unit tests | 100 |
| `congestion/live/receive/control_ring_bench_test.go` | Benchmarks | 70 |

### Modified Files

| File | Changes |
|------|---------|
| `config.go` | Add `UseRecvControlRing`, `RecvControlRingSize`, `RecvControlRingShards` |
| `contrib/common/flags.go` | Add CLI flags for receiver control ring |
| `contrib/common/test_flags.sh` | Add flag tests |
| `metrics/metrics.go` | Add receiver control ring metrics |
| `metrics/handler.go` | Export Prometheus metrics |
| `connection.go` | Add `recvControlRing` field, initialization |
| `connection_handlers.go` | Add `handleACKACK`, `handleACKACKLocked`, dispatch functions |
| `congestion/live/receive/tick.go` | Add `processControlPackets` |
| `congestion/live/receive/ack.go` | Add context asserts |
| `congestion/live/receive/nak.go` | Add context asserts |
| `congestion/live/send/sender.go` | Remove `useControlRing` bool |
| `congestion/live/send/ack.go` | Change `useControlRing` to `controlRing != nil` |
| `congestion/live/send/nak.go` | Change `useControlRing` to `controlRing != nil` |
| `congestion/live/send/tick.go` | Change `useControlRing` to `controlRing != nil` |
| `congestion/live/send/control_ring.go` | Refactor to use `common.ControlRing[T]` |
| `contrib/integration_testing/test_configs.go` | Add `ConfigFullELLockFree`, test definitions |

---

## Checkpoint Commands

Run these commands at each phase completion:

```bash
# Build check
go build ./...

# Unit tests
go test ./congestion/live/common/
go test ./congestion/live/receive/
go test ./congestion/live/send/
go test .

# Race detector
go test -race ./congestion/live/receive/
go test -race .

# Benchmarks
go test -bench=. -benchmem ./congestion/live/common/
go test -bench=BenchmarkRecvControlRing -benchmem ./congestion/live/receive/

# Flag tests
make test-flags

# Linting
golangci-lint run ./...

# Integration tests (after Phase 9)
make test-isolation TEST=Isolation-5M-FullELLockFree
```

