# Completely Lock-Free Receiver Design

> **Document Purpose:** Design for eliminating remaining locks from the receiver's EventLoop path, following the successful sender lockless architecture with control packet ring.
> **Related Documents:**
> - [`lockless_sender_design.md`](./lockless_sender_design.md) - Sender control ring pattern (reference)
> - [`lockless_sender_implementation_plan.md`](./lockless_sender_implementation_plan.md) - Sender implementation details
> - [`gosrt_lockless_design.md`](./gosrt_lockless_design.md) - Original receiver lockless design (Phase 1-4)
> - [`retransmission_and_nak_suppression_design.md`](./retransmission_and_nak_suppression_design.md) - RTO suppression
> - [`ack_optimization_plan.md`](./ack_optimization_plan.md) - ACK/ACKACK optimization

> **Status:** 📋 DESIGN PHASE

---

## Table of Contents

1. [Motivation](#1-motivation)
   - 1.1 [Current State](#11-current-state)
   - 1.2 [Problem: Remaining Locks in EventLoop](#12-problem-remaining-locks-in-eventloop)
   - 1.3 [Goal: Completely Lock-Free EventLoop](#13-goal-completely-lock-free-eventloop)
   - 1.4 [Function Dispatch Pattern](#14-function-dispatch-pattern)
   - 1.5 [Benefits](#15-benefits)
2. [Current Architecture Analysis](#2-current-architecture-analysis)
   - 2.1 [Sender Control Ring Pattern (Reference)](#21-sender-control-ring-pattern-reference)
   - 2.2 [Current Receiver Locking Points](#22-current-receiver-locking-points)
   - 2.3 [EventLoop vs Tick Mode](#23-eventloop-vs-tick-mode)
3. [High-Level Design](#3-high-level-design)
   - 3.1 [Architecture Overview](#31-architecture-overview)
   - 3.2 [Control Packet Flow](#32-control-packet-flow)
   - 3.3 [Data Flow Comparison](#33-data-flow-comparison)
   - 3.4 [Sender vs Receiver Architecture Comparison](#34-sender-vs-receiver-architecture-comparison)
     - 3.4.1 [Architecture Diagram Comparison](#341-architecture-diagram-comparison)
     - 3.4.2 [Component-by-Component Comparison](#342-component-by-component-comparison)
     - 3.4.3 [Control Packet Type Comparison](#343-control-packet-type-comparison)
     - 3.4.4 [Step-by-Step Flow Comparison](#344-step-by-step-flow-comparison)
     - 3.4.5 [Function Naming Convention Comparison](#345-function-naming-convention-comparison)
     - 3.4.6 [Primary Functions vs Locking Wrappers](#346-primary-functions-vs-locking-wrappers)
     - 3.4.7 [Data Structure Comparison](#347-data-structure-comparison)
     - 3.4.8 [Key Differences](#348-key-differences)
     - 3.4.9 [Code Reuse via common.ControlRing[T]](#349-code-reuse-via-commoncontrolringt)
       - 3.4.9.1 [Package Structure for Shared Code](#3491-package-structure-for-shared-code)
       - 3.4.9.2 [Why common.ControlRing[T] (Not Alternatives)](#3492-why-commoncontrolringt-not-alternatives)
       - 3.4.9.3 [Benefits of Adopted Approach](#3493-benefits-of-adopted-approach)
     - 3.4.10 [Required Refactoring: Naming Consistency](#3410-required-refactoring-naming-consistency)
     - 3.4.11 [Verification Checklist](#3411-verification-checklist)
4. [Detailed Design](#4-detailed-design)
   - 4.1 [RecvControlRing](#41-recvcontrolring)
     - 4.1.1 [Shared Infrastructure](#411-shared-infrastructure-congestionlivecommon)
     - 4.1.2 [Receiver-Specific Types](#412-receiver-specific-types-congestionlivereceive)
     - 4.1.3 [Design Rationale](#413-design-rationale)
   - 4.2 [Lock-Free Function Variants](#42-lock-free-function-variants)
   - 4.3 [ACKACK Handling Strategy](#43-ackack-handling-strategy)
     - 4.3.1 [io_uring Handler (Lock-Free)](#431-io_uring-handler-lock-free)
     - 4.3.2 [EventLoop Processing](#432-eventloop-processing)
     - 4.3.3 [Function Dispatch Pattern](#433-function-dispatch-pattern)
   - 4.4 [Function Dispatch Pattern](#44-function-dispatch-pattern)
5. [Implementation Details](#5-implementation-details)
   - 5.1 [Control Ring Implementation (Using Code Reuse)](#51-control-ring-implementation-using-code-reuse)
     - 5.1.1 [Shared Generic Ring](#511-shared-generic-ring-congestionlivecommoncontrol_ringgo)
     - 5.1.2 [Receiver Control Ring](#512-receiver-control-ring-congestionlivereceivecontrol_ringgo)
     - 5.1.3 [Comparison: Sender vs Receiver](#513-comparison-sender-vs-receiver-control-ring)
     - 5.1.4 [Refactoring SendControlRing](#514-refactoring-sendcontrolring-to-use-commoncontrolring)
   - 5.2 [Lock-Free periodicACK](#52-lock-free-periodicack)
   - 5.3 [Lock-Free periodicNAK](#53-lock-free-periodicnak)
   - 5.4 [Lock-Free deliverReadyPackets](#54-lock-free-deliverreadypackets)
   - 5.5 [Lock-Free contiguousScan](#55-lock-free-contiguousscan)
   - 5.6 [EventLoop Integration](#56-eventloop-integration)
   - 5.7 [Context Asserts (Runtime Verification)](#57-context-asserts-runtime-verification)
     - 5.7.1 [Assert Functions](#571-assert-functions)
     - 5.7.2 [Sender Functions Requiring Asserts](#572-sender-functions-requiring-asserts)
     - 5.7.3 [Receiver Functions Requiring Asserts](#573-receiver-functions-requiring-asserts)
     - 5.7.4 [Connection-Level Functions](#574-connection-level-functions-handleackack)
     - 5.7.5 [Implementation Pattern](#575-implementation-pattern)
     - 5.7.6 [Summary: Required Changes](#576-summary-required-changes)
6. [Configuration](#6-configuration)
   - 6.1 [Control Ring Configuration (Consolidated)](#61-control-ring-configuration-consolidated)
     - 6.1.1 [Current Sender Implementation Analysis](#611-current-sender-implementation-analysis)
     - 6.1.2 [Proposed Simplified Design](#612-proposed-simplified-design)
     - 6.1.3 [Unified Configuration](#613-unified-configuration)
     - 6.1.4 [CLI Flags](#614-cli-flags)
     - 6.1.5 [Struct-Level Changes](#615-struct-level-changes)
     - 6.1.6 [Configuration Summary Table](#616-configuration-summary-table)
     - 6.1.7 [Required Refactoring (Sender)](#617-required-refactoring-sender)
     - 6.1.8 [Breaking Changes](#618-breaking-changes)
   - 6.2 [Configuration Hierarchy](#62-configuration-hierarchy)
7. [Metrics](#7-metrics)
   - 7.1 [Sender vs Receiver Metrics Comparison](#71-sender-vs-receiver-metrics-comparison)
     - 7.1.1 [Control Ring Metrics](#711-control-ring-metrics)
     - 7.1.2 [EventLoop Startup Metrics](#712-eventloop-startup-metrics)
     - 7.1.3 [EventLoop Iteration Metrics](#713-eventloop-iteration-metrics)
     - 7.1.4 [EventLoop Drain Diagnostics](#714-eventloop-drain-diagnostics)
     - 7.1.5 [EventLoop Sleep/Timing Metrics](#715-eventloop-sleeptiming-metrics)
     - 7.1.6 [Delivery/Processing Metrics](#716-deliveryprocessing-metrics)
     - 7.1.7 [Debug/Diagnostic Metrics](#717-debugdiagnostic-metrics)
   - 7.2 [Complete Receiver Metrics Definition](#72-complete-receiver-metrics-definition)
   - 7.3 [Prometheus Export](#73-prometheus-export)
   - 7.4 [Metrics Naming Convention](#74-metrics-naming-convention)
   - 7.5 [Verification Checklist](#75-verification-checklist)
8. [Testing Strategy](#8-testing-strategy)
   - 8.1 [Risk Analysis: What Can Go Wrong](#81-risk-analysis-what-can-go-wrong)
   - 8.2 [Table-Driven Tests: Control Ring](#82-table-driven-tests-control-ring)
   - 8.3 [Table-Driven Tests: RecvControlRing Specific](#83-table-driven-tests-recvcontrolring-specific)
   - 8.4 [Table-Driven Tests: Function Dispatch](#84-table-driven-tests-function-dispatch)
   - 8.5 [Table-Driven Tests: ACKACK Processing](#85-table-driven-tests-ackack-processing)
   - 8.6 [Table-Driven Tests: Ring Full Fallback](#86-table-driven-tests-ring-full-fallback)
   - 8.7 [Context Assert Tests](#87-context-assert-tests)
   - 8.8 [Metrics Invariant Tests](#88-metrics-invariant-tests)
   - 8.9 [Concurrency Tests](#89-concurrency-tests)
   - 8.10 [Benchmarks](#810-benchmarks)
     - 8.10.1 [Existing Benchmark Coverage](#8101-existing-benchmark-coverage)
     - 8.10.2 [New Benchmarks Required](#8102-new-benchmarks-required)
     - 8.10.3 [Benchmark Coverage Summary](#8103-benchmark-coverage-summary)
     - 8.10.4 [Performance Targets](#8104-performance-targets)
     - 8.10.5 [Running Benchmarks](#8105-running-benchmarks)
   - 8.11 [Integration Tests](#811-integration-tests)
   - 8.12 [Test Summary by Component](#812-test-summary-by-component)
9. [Integration Test Updates](#9-integration-test-updates)
   - 9.1 [Current Test Landscape](#91-current-test-landscape)
     - 9.1.1 [Existing EventLoop Tests](#911-existing-eventloop-tests-from-integration_testing_matrix_designmd)
     - 9.1.2 [Existing Parallel Tests](#912-existing-parallel-tests)
   - 9.2 [New Configuration: FullELLockFree](#92-new-configuration-fullellockfree)
   - 9.3 [New Isolation Tests](#93-new-isolation-tests)
   - 9.4 [New Parallel Tests](#94-new-parallel-tests)
   - 9.5 [Test Matrix Integration](#95-test-matrix-integration)
   - 9.6 [Expected Metrics Changes](#96-expected-metrics-changes)
   - 9.7 [Validation Criteria](#97-validation-criteria)
   - 9.8 [Implementation Checklist](#98-implementation-checklist)
     - 9.8.1 [CLI Flag Testing](#981-cli-flag-testing)
   - 9.9 [Running the New Tests](#99-running-the-new-tests)
10. [Migration Path](#10-migration-path)
    - 10.1 [Phase 1: Add Control Ring Infrastructure](#101-phase-1-add-control-ring-infrastructure)
    - 10.2 [Phase 2: Create EventLoop Function Variants](#102-phase-2-create-eventloop-function-variants)
    - 10.3 [Phase 3: Function Dispatch Setup](#103-phase-3-function-dispatch-setup)
    - 10.4 [Phase 4: ACKACK Routing](#104-phase-4-ackack-routing)
    - 10.5 [Rollback Procedure](#105-rollback-procedure)
11. [Appendix: File Summary](#appendix-file-summary)
12. [Summary](#summary)

---

## 1. Motivation

### 1.1 Current State

The receiver was partially converted to lock-free operation in Phases 1-4:
- **Phase 1:** Atomic metrics (no lock for counters)
- **Phase 2:** Buffer pooling (zero-copy)
- **Phase 3:** Lock-free packet ring (Push() → ring → btree)
- **Phase 4:** EventLoop (continuous processing vs Tick)

However, the EventLoop still uses `sync.RWMutex` for:
- `periodicACK()` - reads/writes receiver state
- `periodicNAK()` - reads packet btree and NAK btree
- `deliverReadyPackets()` - reads/removes from packet btree
- `contiguousScan()` - iterates packet btree

### 1.2 Problem: Remaining Locks in EventLoop

Even with the packet ring, the EventLoop still acquires locks internally:
1. **`r.lock`** - Used by `periodicACK()`, `periodicNAK()`, `deliverReadyPackets()`, `contiguousScan()`
2. **`c.ackLock`** - Used by `handleACKACK()` when ACKACK packets arrive from io_uring

**Note:** Tick() and EventLoop are **mutually exclusive** - only one is active based on configuration (`UseEventLoop`). They do not run concurrently.

### 1.3 Goal: Completely Lock-Free EventLoop

Following the sender pattern, we will:
1. **Route ALL incoming control packets through a control ring** (ACKACK, KEEPALIVE)
2. **Create lock-free function variants** for all btree operations
3. **Use function dispatch pattern** - EventLoop calls lock-free versions, Tick() calls locking wrappers
4. **Backward compatibility** - Continue to allow the Tick() based using the locking wrappers

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                        COMPLETELY LOCK-FREE ARCHITECTURE                                │
├─────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                         │
│                      SENDER                                    RECEIVER                 │
│                      ══════                                    ════════                 │
│                                                                                         │
│  Network (in)        ACK/NAK arrives                           ACKACK/KEEPALIVE arrives │
│      │                    │                                         │                   │
│      ▼                    ▼                                         ▼                   │
│  io_uring         ┌───────────────┐                         ┌───────────────┐           │
│  handler          │ SendControlRing│                        │ RecvControlRing│          │
│  (NO LOCK)        │ (ACK/NAK)      │                        │ (ACKACK/KA)    │          │
│                   └───────┬───────┘                         └───────┬───────┘           │
│                           │                                         │                   │
│                           ▼                                         ▼                   │
│  EventLoop        processControlPackets()                   processControlPackets()     │
│  (single          ackBtree() ← NO LOCK                      ackackEventLoop() ← NO LOCK │
│   consumer)       nakBtree() ← NO LOCK                      periodicACK() ← NO LOCK     │
│                   deliverReady() ← NO LOCK                  periodicNAK() ← NO LOCK     │
│                                                             deliverReady() ← NO LOCK    │
│                                                                                          │
│  Tick()           ackLocked()                               periodicACKLocked()         │
│  (fallback)       nakLocked()                               periodicNAKLocked()         │
│                   deliverReadyLocked()                      deliverReadyLocked()        │
│                                                                                          │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

### 1.4 Function Dispatch Pattern

Following `lockless_sender_implementation_plan.md`, we use function pointers configured at startup:

```go
// receiver.go - Function dispatch fields (configured once at startup)
type receiver struct {
    // ... existing fields ...

    // Function dispatch: EventLoop calls lock-free, Tick calls locking wrappers
    periodicACKFn         func(now uint64) (bool, circular.Number, bool)
    periodicNAKFn         func(now uint64) []circular.Number
    deliverReadyPacketsFn func(now uint64) int
    contiguousScanFn      func() (bool, uint32)

    // ACKACK function dispatch
    handleACKACKFn        func(ackNum uint32, arrivalTime time.Time)
}

// Configuration at startup:
if config.UseEventLoop && config.UseRecvControlRing {
    // EventLoop mode - NO LOCKS (single consumer)
    r.periodicACKFn = r.periodicACK              // Lock-free version
    r.handleACKACKFn = r.handleACKACKEventLoop   // Lock-free version
} else {
    // Tick mode - WITH LOCKS (locking wrappers)
    r.periodicACKFn = r.periodicACKLocked        // Acquires r.lock
    r.handleACKACKFn = r.handleACKACKLocked      // Acquires c.ackLock
}
```

### 1.5 Benefits

1. **Zero lock contention** - EventLoop is single consumer of ALL state
2. **Lower latency** - No lock acquisition overhead in hot path
3. **Predictable timing** - No lock waits or priority inversions
4. **Consistent with sender** - Same control ring pattern, easier maintenance
5. **Graceful fallback** - Tick() mode still works with locking wrappers

---

## 2. Current Architecture Analysis

### 2.1 Sender Control Ring Pattern (Reference)

From `congestion/live/send/control_ring.go`:

```go
// ControlPacketType identifies the type of control packet
type ControlPacketType uint8

const (
    ControlTypeACK ControlPacketType = iota
    ControlTypeNAK
)

// ControlPacket wraps an ACK or NAK for ring transport.
// Value type (not pointer) to avoid allocations in the hot path.
type ControlPacket struct {
    Type         ControlPacketType
    ACKSequence  uint32           // For ACK: the acknowledged sequence number
    NAKCount     int              // For NAK: number of sequences in NAKSequences
    NAKSequences [32]uint32       // For NAK: inline array (no allocation)
}

// SendControlRing wraps the lock-free ring for control packets.
type SendControlRing struct {
    ring   *ring.ShardedRing
    shards int
}

// PushACK - called from io_uring handler (multiple producers)
func (r *SendControlRing) PushACK(seq circular.Number) bool

// PushNAK - called from io_uring handler (multiple producers)
func (r *SendControlRing) PushNAK(seqs []circular.Number) bool

// TryPop - called from EventLoop (single consumer)
func (r *SendControlRing) TryPop() (ControlPacket, bool)
```

From `congestion/live/send/eventloop.go`:

```go
// processControlPacketsDelta drains and processes control packets from the ring.
// Called from EventLoop - NO LOCKING (single-threaded btree access).
func (s *sender) processControlPacketsDelta() int {
    for {
        cp, ok := s.controlRing.TryPop()
        if !ok {
            break
        }
        switch cp.Type {
        case ControlTypeACK:
            // NO LOCKING: EventLoop is single consumer of btree
            s.ackBtree(circular.New(cp.ACKSequence, packet.MAX_SEQUENCENUMBER))
        case ControlTypeNAK:
            // NO LOCKING: EventLoop is single consumer of btree
            s.nakBtree(seqs)
        }
    }
}
```

### 2.2 Current Receiver Locking Points

From `congestion/live/receive/`:

| Function | File:Line | Lock Type | Purpose |
|----------|-----------|-----------|---------|
| `periodicACK()` | `ack.go:131` | RLock→Lock | Read btree, update state |
| `periodicACKLocked()` | `ack.go:14` | RLock | Read btree, update state |
| `periodicNAK()` | `nak.go` | RLock | Read packet btree, NAK btree |
| `deliverReadyPacketsLocked()` | `tick.go` | Lock | Remove from btree, deliver |
| `contiguousScan()` | `scan.go` | RLock | Iterate btree |
| `contiguousScanWithTime()` | `scan.go` | RLock | Iterate btree with TSBPD |
| `pushLocked()` | `push.go` | Lock | Insert into btree |
| `drainRingByDelta()` | `ring.go` | (none) | Read from ring, insert btree |

### 2.3 EventLoop vs Tick Mode

**Tick Mode** (`useEventLoop=false`):
- Timer fires every 10ms
- Calls `Tick()` which acquires locks for each operation
- Multiple entry points can call Tick()

**EventLoop Mode** (`useEventLoop=true`):
- Continuous loop with adaptive backoff
- Single goroutine processes all operations
- Still uses locks internally (the problem!)

Current EventLoop flow (from `tick.go:142-339`):
```go
func (r *receiver) EventLoop(ctx context.Context) {
    for {
        select {
        case <-fullACKTicker.C:
            r.drainRingByDelta()
            if ok, newContiguous := r.contiguousScan(); ok {  // Uses lock!
                r.sendACK(...)
            }
        case <-nakTicker.C:
            r.drainRingByDelta()
            if list := r.periodicNAK(now); len(list) != 0 {  // Uses lock!
                r.sendNAK(list)
            }
        default:
            delivered := r.deliverReadyPackets()  // Uses lock!
            processed := r.processOnePacket()
            ok, newContiguous := r.contiguousScan()  // Uses lock!
        }
    }
}
```

---

## 3. High-Level Design

### 3.1 Architecture Overview

The receiver architecture uses a **shared control ring infrastructure** from `congestion/live/common/`. Both sender and receiver embed `common.ControlRing[T]` with their respective packet types, eliminating code duplication while maintaining type safety.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     COMPLETELY LOCK-FREE RECEIVER                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  io_uring (Data)              io_uring (Control)       Connection Level     │
│      │                             │                        │               │
│      │ Data packet                 │ ACKACK arrives         │ Stats query   │
│      │ arrives                     │                        │               │
│      ▼                             ▼                        ▼               │
│  ┌───────────────────┐       ┌───────────────────┐    ┌──────────────┐      │
│  │  RecvPacketRing   │       │  RecvControlRing  │    │ Atomic Stats │      │
│  │  (existing)       │       │  embeds common.   │    │ (existing)   │      │
│  │  MPSC ring        │       │  ControlRing[T]   │    │ No locks     │      │
│  └─────────┬─────────┘       └─────────┬─────────┘    └──────────────┘      │
│            │                           │                                    │
│            └───────────┬───────────────┘                                    │
│                        │                                                    │
│                        ▼                                                    │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         EventLoop (SINGLE CONSUMER)                  │   │
│  ├──────────────────────────────────────────────────────────────────────┤   │
│  │                                                                      │   │
│  │  1. drainRingToBtreeEventLoop()     ← Data ring → packet btree       │   │
│  │  2. processControlPacketsEventLoop() ← Control ring → ACKACK         │   │
│  │  3. periodicACKEventLoop()          ← Generate ACK (no lock)         │   │
│  │  4. periodicNAKEventLoop()          ← Generate NAK (no lock)         │   │
│  │  5. deliverReadyPacketsEventLoop()  ← Deliver packets (no lock)      │   │
│  │  6. contiguousScanEventLoop()       ← Scan btree (no lock)           │   │
│  │                                                                      │   │
│  │  ALL btree operations are single-threaded - NO LOCKING REQUIRED      │   │
│  │                                                                      │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘

SHARED INFRASTRUCTURE:
┌─────────────────────────────────────────────────────────────────────────────┐
│                    congestion/live/common/control_ring.go                   │
├─────────────────────────────────────────────────────────────────────────────┤
│  type ControlRing[T any] struct {                                           │
│      ring   *ring.ShardedRing                                              │
│      shards int                                                            │
│  }                                                                          │
│                                                                             │
│  func NewControlRing[T any](size, shards int) (*ControlRing[T], error)     │
│  func (r *ControlRing[T]) Push(shardID uint64, packet T) bool              │
│  func (r *ControlRing[T]) TryPop() (T, bool)                               │
│  func (r *ControlRing[T]) Len() int                                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  SENDER:                              RECEIVER:                             │
│  SendControlRing {                    RecvControlRing {                     │
│    *common.ControlRing[               *common.ControlRing[                  │
│       ControlPacket]                     RecvControlPacket]                 │
│  }                                    }                                     │
│  - ControlPacket: ACK, NAK            - RecvControlPacket: ACKACK, KEEPALIVE│
│  - PushACK(), PushNAK()               - PushACKACK(), PushKEEPALIVE()       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Control Packet Flow

**Control Ring Architecture:** `RecvControlRing` embeds `common.ControlRing[RecvControlPacket]` from `congestion/live/common/`. This provides the shared MPSC ring infrastructure while allowing receiver-specific packet types and convenience methods.

**Receiver Control Packets:**

| Direction | Packet Type | Current Handler | Proposed Handler |
|-----------|-------------|-----------------|------------------|
| Incoming | ACKACK | `handleACKACK()` with `c.ackLock` | Route through `RecvControlRing` (embeds `common.ControlRing`) → EventLoop |
| Incoming | KEEPALIVE | `handleKeepAlive()` | Route through `RecvControlRing` (embeds `common.ControlRing`) → EventLoop |
| Outgoing | ACK | `sendACK()` callback | No change (just callback) |
| Outgoing | NAK | `sendNAK()` callback | No change (just callback) |

#### 3.2.1 How ACKACK Works Today

The ACK/ACKACK mechanism is documented in `ack_ackack_redesign_progress.md` and RFC Section 4.10.
It provides RTT (Round-Trip Time) measurement for the receiver.

**Current Implementation Files:**
- `connection_send.go:124-210` - `sendACK()`: Creates ACK packets, stores timestamp in ackNumbers btree
- `connection_handlers.go:368-419` - `handleACKACK()`: Receives ACKACK, calculates RTT
- `ack_btree.go` - `ackEntryBtree`: O(log n) storage for ACK timestamps
- `rtt.go` - `rtt` struct: Atomic RTT/RTTVar calculation with EWMA

**ACK/ACKACK Sequence Diagram:**

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                              ACK/ACKACK FLOW (Current)                                  │
├─────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                         │
│   RECEIVER (Server)                              SENDER (Client-Generator)              │
│   ═══════════════════                            ════════════════════════════           │
│                                                                                         │
│   ┌─────────────────────┐                                                               │
│   │ EventLoop or Tick() │                                                               │
│   │   periodicACK()     │                                                               │
│   │   contiguousScan()  │                                                               │
│   └──────────┬──────────┘                                                               │
│              │                                                                          │
│              │ Decides to send Full ACK (every 10ms)                                    │
│              ▼                                                                          │
│   ┌─────────────────────┐                                                               │
│   │ sendACK(seq, lite)  │                                                               │
│   │   connection_send.go│                                                               │
│   │                     │                                                               │
│   │ If Full ACK:        │                                                               │
│   │  1. ackNum = getNext│                                                               │
│   │     ACKNumber()     │                  ┌───────────────────────────────┐            │
│   │  2. entry = GetAck  │                  │           Network             │            │
│   │     Entry()         │                  │        ~0.05-0.1ms            │            │
│   │  3. entry.timestamp │                  └───────────────────────────────┘            │
│   │     = time.Now()    │                                                               │
│   │  4. c.ackLock.Lock()│                                                               │
│   │  5. ackNumbers.     │     Full ACK                                                  │
│   │     Insert(entry)   │  ─────────────────────────────────────────────────►           │
│   │  6. c.ackLock.      │  TypeSpecific=ackNum                                          │
│   │     Unlock()        │  CIF: RTT, RTTVar, seq                                        │
│   └─────────────────────┘                                     │                         │
│                                                                ▼                        │
│                                                   ┌─────────────────────┐               │
│                                                   │ handleACK(p)        │               │
│                                                   │ connection_handlers │               │
│                                                   │                     │               │
│                                                   │ 1. Parse CIF        │               │
│                                                   │ 2. c.snd.ACK(seq)   │               │
│                                                   │    → removes ACK'd  │               │
│                                                   │      packets        │               │
│                                                   │ 3. c.recalculateRTT │               │
│                                                   │    (from CIF.RTT)   │               │
│                                                   │ 4. sendACKACK(      │               │
│                                                   │    ackNum)          │               │
│   ┌─────────────────────┐                         └──────────┬──────────┘               │
│   │ handleACKACK(p)     │                                    │                          │
│   │ connection_handlers │     ACKACK                         │                          │
│   │                     │  ◄─────────────────────────────────┘                          │
│   │ 1. ackNum = p.      │  TypeSpecific=ackNum (echoed)                                 │
│   │    TypeSpecific     │                                                               │
│   │ 2. now = time.Now() │                                                               │
│   │ 3. c.ackLock.Lock() │                                                               │
│   │ 4. entry = ackNumbers                                                               │
│   │    .Get(ackNum)     │                                                               │
│   │ 5. rttDuration =    │                                                               │
│   │    now - entry.     │                                                               │
│   │    timestamp        │                                                               │
│   │ 6. c.recalculateRTT │                                                               │
│   │    (rttDuration)    │   RTT = time_now - time_when_ack_was_sent                     │
│   │ 7. ackNumbers.Delete│   RTT smoothing: RTT = RTT*0.875 + lastRTT*0.125              │
│   │    (ackNum)         │   RTTVar = RTTVar*0.75 + |RTT-lastRTT|*0.25                   │
│   │ 8. c.ackLock.Unlock()                                                               │
│   │ 9. recv.SetNAK      │                                                               │
│   │    Interval()       │                                                               │
│   └─────────────────────┘                                                               │
│                                                                                         │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

**Key Data Structures:**

```go
// ack_btree.go - ACK number to timestamp mapping
type ackEntry struct {
    ackNum    uint32    // ACK number (monotonic, from getNextACKNumber)
    timestamp time.Time // When Full ACK was sent
}

type ackEntryBtree struct {
    tree *btree.BTreeG[*ackEntry]  // O(log n) lookup
}

// connection.go - Connection state
type srtConn struct {
    ackNumbers    *ackEntryBtree  // Maps ackNum → timestamp
    ackLock       sync.Mutex      // Protects ackNumbers (separate from r.lock!)
    nextACKNumber atomic.Uint32   // Monotonic ACK counter
    rtt           *rtt            // Atomic RTT calculation
}
```

**Current Locking in ACKACK Path:**

| Operation | Lock | File:Line |
|-----------|------|-----------|
| `sendACK()` - Full ACK btree insert | `c.ackLock` | `connection_send.go:181-186` |
| `handleACKACK()` - btree lookup/delete | `c.ackLock` | `connection_handlers.go:378-407` |
| `recalculateRTT()` | Atomic (no lock) | `connection_handlers.go:422-433` |
| `recv.SetNAKInterval()` | Atomic | `connection_handlers.go:418` |

#### 3.2.2 Control Packet Routing via RecvControlRing

To achieve a **completely lock-free EventLoop**, ALL incoming control packets are routed through the control ring:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                     COMPLETELY LOCK-FREE: CONTROL RING                        │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                               │
│   io_uring Completion Handler                    EventLoop (SINGLE CONSUMER)  │
│   ═══════════════════════════                    ═════════════════════════    │
│   NO LOCKS IN THIS PATH                          ALL STATE ACCESS HERE        │
│                                                                               │
│   ┌─────────────────────────┐                    ┌─────────────────────────┐  │
│   │ Control packet arrives  │                    │ processControlPackets():│  │
│   │                         │                    │                         │  │
│   │ switch p.ControlType {  │   RecvControlRing  │ for cp := TryPop() {    │  │
│   │ case ACKACK:            │ ════════════════►  │   switch cp.Type {      │  │
│   │   controlRing.Push(     │                    │   case ACKACK:          │  │
│   │     ACKACK,             │                    │     handleACKACK(cp)    │  │
│   │     ackNum,             │                    │   case KEEPALIVE:       │  │
│   │     arrivalTime)        │                    │     handleKeepAlive()   │  │
│   │ case KEEPALIVE:         │                    │   }                     │  │
│   │   controlRing.Push(     │                    │ }                       │  │
│   │     KEEPALIVE)          │                    │                         │  │
│   │ }                       │                    │ // Then packet ops:     │  │
│   │                         │                    │ drainRingToBtree()      │  │
│   │ // NO LOCK acquired     │                    │ periodicACK()           │  │
│   │ // Just ring.Write()    │                    │ periodicNAK()           │  │
│   │                         │                    │ deliverReadyPackets()   │  │
│   └─────────────────────────┘                    └─────────────────────────┘  │
│                                                                               │
└──────────────────────────────────────────────────────────────────────────────┘
```

**Why Route ALL Control Packets Through Ring?**

1. **Consistency:** Matches sender pattern exactly - all control packets via ring
2. **Single Consumer:** EventLoop is the ONLY goroutine touching ANY state
3. **No Lock in io_uring:** io_uring completion handlers become trivially simple
4. **Future-Proof:** Easy to add more control packet types (e.g., SHUTDOWN)
5. **Testability:** Single entry point for all state mutations

**RTT Accuracy:**

The ACKACK arrival time is captured in the io_uring handler and passed through the ring:

```go
// In io_uring handler (NO LOCK - just ring push):
arrivalTime := time.Now()  // Capture immediately on packet arrival
controlRing.PushACKACK(ackNum, arrivalTime)

// In EventLoop (single consumer - safe to access all state):
func (c *srtConn) processControlPackets() {
    for {
        cp, ok := c.controlRing.TryPop()
        if !ok {
            break
        }
        switch cp.Type {
        case RecvControlTypeACKACK:
            c.handleACKACKFn(cp.ACKNumber, time.Unix(0, cp.Timestamp))
        case RecvControlTypeKEEPALIVE:
            c.handleKeepAliveFn()
        }
    }
}
```

**Control Packet Types:**

| Type | Data Passed Through Ring | Handler Function |
|------|--------------------------|------------------|
| ACKACK | `ackNum uint32`, `arrivalTime int64` (UnixNano) | `handleACKACK()` / `handleACKACKLocked()` |
| KEEPALIVE | (none - just packet type) | `handleKeepAlive()` / `handleKeepAliveLocked()` |

**Function Dispatch for Control Packets:**

Following the sender pattern from `lockless_sender_implementation_plan.md`:

```go
// connection.go - Function dispatch fields
type srtConn struct {
    // ... existing fields ...

    // Control packet function dispatch (configured once at startup)
    // EventLoop mode: lock-free versions (single consumer)
    // Legacy mode: locking wrappers
    handleACKACKFn    func(ackNum uint32, arrivalTime time.Time)
    handleKeepAliveFn func()
}

// Setup at connection initialization:
if config.UseRecvControlRing {
    // EventLoop mode - functions called from single-threaded EventLoop
    c.handleACKACKFn = c.handleACKACK        // Primary lock-free function
    c.handleKeepAliveFn = c.handleKeepAlive  // Primary lock-free function
} else {
    // Legacy mode - functions called from io_uring handlers (need locks)
    c.handleACKACKFn = c.handleACKACKLocked        // Locking wrapper
    c.handleKeepAliveFn = c.handleKeepAliveLocked  // Locking wrapper
}
```

### 3.3 Data Flow Comparison

**Before (Locking - Current):**
```
Push() ─────────────► r.lock ─────────► btree.Insert()
                         ↕ contention
Tick() ─────────────► r.lock ─────────► btree.Iterate() ─► sendACK/NAK
                         ↕ contention
ACKACK (io_uring) ──► c.ackLock ──────► ackNumbers.Get() ─► recalculateRTT()
KEEPALIVE (io_uring) ► (direct) ──────► resetPeerIdleTimeout()
```

**After (Completely Lock-Free EventLoop):**
```
┌─────────────────────────────────────────────────────────────────────────────┐
│  io_uring Handlers (NO LOCKS - just ring pushes)                             │
├─────────────────────────────────────────────────────────────────────────────┤
│  Push() ────────────────► packetRing.Write() ────────┐                      │
│  ACKACK ────────────────► controlRing.Push(ACKACK) ──┤                      │
│  KEEPALIVE ─────────────► controlRing.Push(KEEPALIVE)┤                      │
│                                                      │                      │
├─────────────────────────────────────────────────────────────────────────────┤
│  EventLoop (SINGLE CONSUMER - COMPLETELY LOCK-FREE)                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                      │                      │
│  EventLoop ◄─────────────────────────────────────────┘                      │
│      │                                                                      │
│      ├── processControlPackets()                                            │
│      │       ├── handleACKACK() ────► ackNumbers btree (NO LOCK)           │
│      │       └── handleKeepAlive() ─► resetPeerIdleTimeout() (atomic)      │
│      │                                                                      │
│      ├── drainRingToBtree() ────────► packetStore btree (NO LOCK)          │
│      ├── periodicACK() ─────────────► packetStore iterate (NO LOCK)        │
│      ├── periodicNAK() ─────────────► nakBtree iterate (NO LOCK)           │
│      └── deliverReadyPackets() ─────► packetStore DeleteMin (NO LOCK)      │
│                                                                             │
│  ALL operations are single-threaded - NO LOCKS REQUIRED                     │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘

Tick() Mode (Legacy - still supported):
  - Uses *Locked() wrapper functions (e.g., handleACKACKLocked with c.ackLock)
  - Each function acquires its own lock
  - No control ring used
```

### 3.4 Sender vs Receiver Architecture Comparison

The receiver design intentionally mirrors the sender architecture to maximize code consistency and maintainability. This section provides a detailed comparison.

#### 3.4.1 Architecture Diagram Comparison

Both sender and receiver use the shared `common.ControlRing[T]` infrastructure:

```
┌──────────────────────────────────────────────────────────────────────────────────────────┐
│                              SENDER (congestion/live/send/)                              │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                           │
│  Application                       io_uring Handler                                       │
│      │                                 │                                                  │
│      │ s.Push(pkt)                     │ ACK/NAK arrives                                  │
│      ▼                                 ▼                                                  │
│  SendPacketRing                   SendControlRing                                         │
│  (data packets)                   embeds common.ControlRing[ControlPacket]                │
│      │                                 │                                                  │
│      └────────────┬────────────────────┘                                                  │
│                   │                                                                       │
│                   ▼                                                                       │
│            Sender EventLoop (single consumer)                                             │
│                   │                                                                       │
│    ┌──────────────┼──────────────┬────────────────┐                                       │
│    ▼              ▼              ▼                ▼                                       │
│  drainRing    processACK    processNAK    deliverReady                                   │
│  toBtree()    (delete≤seq)  (retransmit)  (TSBPD)                                        │
│                                                                                           │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│                             RECEIVER (congestion/live/receive/)                           │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                           │
│  io_uring Handler                  io_uring Handler                                       │
│      │                                 │                                                  │
│      │ r.Push(pkt)                     │ ACKACK/KEEPALIVE arrives                         │
│      ▼                                 ▼                                                  │
│  RecvPacketRing                   RecvControlRing                                         │
│  (data packets)                   embeds common.ControlRing[RecvControlPacket]            │
│      │                                 │                                                  │
│      └────────────┬────────────────────┘                                                  │
│                   │                                                                       │
│                   ▼                                                                       │
│           Receiver EventLoop (single consumer)                                            │
│                   │                                                                       │
│    ┌──────────────┼──────────────┬────────────────┐                                       │
│    ▼              ▼              ▼                ▼                                       │
│  drainRing    processACKACK  periodicACK/  deliverReady                                  │
│  toBtree()    (RTT calc)     periodicNAK   (TSBPD)                                       │
│                                                                                           │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│                       SHARED: congestion/live/common/control_ring.go                      │
├──────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                           │
│  common.ControlRing[T] provides:                                                          │
│    - NewControlRing[T](size, shards)   → Create MPSC ring                                │
│    - Push(shardID, packet)             → Thread-safe write                               │
│    - TryPop()                          → Single-consumer read                            │
│    - Len()                             → Approximate length                              │
│                                                                                           │
└──────────────────────────────────────────────────────────────────────────────────────────┘
```

#### 3.4.2 Component-by-Component Comparison

| Component | Sender | Receiver | Shared Infrastructure |
|-----------|--------|----------|----------------------|
| **Data Ring** | `SendPacketRing` | `RecvPacketRing` (existing) | Same MPSC ring pattern |
| **Control Ring** | `SendControlRing` embeds `common.ControlRing[ControlPacket]` | `RecvControlRing` embeds `common.ControlRing[RecvControlPacket]` | `common.ControlRing[T]` |
| **Primary Btree** | `SendPacketBtree` | `packetStore` (btree) | Same btree pattern |
| **Secondary Btree** | (none) | `nakBtree` | Receiver-specific |
| **EventLoop** | `sender.EventLoop()` | `receiver.EventLoop()` (existing) | Same continuous loop |

#### 3.4.3 Control Packet Type Comparison

Both rings embed `common.ControlRing[T]` with their respective packet types:

| Aspect | Sender (`SendControlRing`) | Receiver (`RecvControlRing`) | Notes |
|--------|---------------------------|------------------------------|-------|
| **Embeds** | `*common.ControlRing[ControlPacket]` | `*common.ControlRing[RecvControlPacket]` | Same generic infrastructure |
| **Packet Types** | ACK, NAK | ACKACK, KEEPALIVE | Different packet types |
| **Volume** | ~1000/sec (high) | ~100/sec (low) | Receiver has lower volume |
| **Ring Size** | 128 | 128 | Same default size |
| **Shards** | 1 | 1 | Single shard for simplicity |
| **Push Methods** | `PushACK()`, `PushNAK()` | `PushACKACK()`, `PushKEEPALIVE()` | Type-specific convenience |
| **TryPop/Len** | Inherited from `common.ControlRing` | Inherited from `common.ControlRing` | Shared implementation |

#### 3.4.4 Step-by-Step Flow Comparison

| Step | Sender Flow | Receiver Flow | Code Similarity |
|------|-------------|---------------|-----------------|
| **1. Packet Arrival** | io_uring → `handleACK()`/`handleNAK()` | io_uring → `handleACKACK()`/`handleKeepAlive()` | Same dispatch pattern |
| **2. Ring Push** | `controlRing.PushACK(seq)` / `controlRing.PushNAK(seqs)` | `controlRing.PushACKACK(ackNum, time)` / `controlRing.PushKEEPALIVE()` | Same method signature style |
| **3. Ring Pop** | `controlRing.TryPop()` → `ControlPacket` | `controlRing.TryPop()` → `RecvControlPacket` | Identical loop pattern |
| **4. Type Switch** | `switch cp.Type { case ACK: case NAK: }` | `switch cp.Type { case ACKACK: case KEEPALIVE: }` | Identical dispatch pattern |
| **5. Processing** | `s.ackBtree(seq)` / `s.nakBtree(seqs)` | `c.handleACKACK(ackNum, time)` / `c.handleKeepAlive()` | Primary function calls |
| **6. Fallback** | `s.ackLocked()` / `s.nakLocked()` | `c.handleACKACKLocked()` / `c.handleKeepAliveLocked()` | Locking wrapper pattern |

#### 3.4.5 Function Naming Convention Comparison

| Pattern | Sender Example | Receiver Example | Convention |
|---------|----------------|------------------|------------|
| **Primary (lock-free)** | `ackBtree()` | `periodicACK()` | No suffix |
| **Locking wrapper** | `ackLocked()` | `periodicACKLocked()` | `*Locked` suffix |
| **EventLoop-specific** | `processControlPacketsDelta()` | `processControlPackets()` | Process prefix |
| **Ring push** | `PushACK()`, `PushNAK()` | `PushACKACK()`, `PushKEEPALIVE()` | Push prefix |
| **Ring pop** | `TryPop()` | `TryPop()` | Identical |

#### 3.4.6 Primary Functions vs Locking Wrappers

This table shows the complete mapping of non-locking primary functions (called from EventLoop) and their corresponding locking wrappers (called from Tick mode or external callers).

**SENDER Functions:**

| Category | Primary Function (No Lock) | Locking Wrapper | Called From | File |
|----------|---------------------------|-----------------|-------------|------|
| **ACK Processing** | `ackBtree(seq)` | `ackLocked(seq)` | EventLoop / io_uring | `ack.go` |
| **NAK Processing** | `nakBtree(seqs)` | `nakLocked(seqs)` | EventLoop / io_uring | `nak.go` |
| **Data Ring Drain** | `drainRingToBtreeEventLoop()` | `drainRingToBtree()` | EventLoop / Tick | `eventloop.go`, `tick.go` |
| **Packet Delivery** | `deliverReadyPacketsEventLoop(now)` | `tickDeliverPacketsBtree(now)` | EventLoop / Tick | `eventloop.go`, `tick.go` |
| **Drop Old Packets** | (in EventLoop) | `tickDropOldPacketsBtree(now)` | EventLoop / Tick | `eventloop.go`, `tick.go` |
| **Push to Btree** | `pushBtree(pkt)` | `pushLocked(pkt)` | Ring drain / Push() | `push.go` |
| **Control Processing** | `processControlPacketsDelta()` | (N/A - EventLoop only) | EventLoop | `eventloop.go` |

**RECEIVER Functions:**

| Category | Primary Function (No Lock) | Locking Wrapper | Called From | File |
|----------|---------------------------|-----------------|-------------|------|
| **Periodic ACK** | `periodicACK(now)` | `periodicACKLocked(now)` | EventLoop / Tick | `ack.go` |
| **Periodic NAK** | `periodicNakBtree(now)` | `periodicNakBtreeLocked(now)` | EventLoop / Tick | `nak.go` |
| **Packet Delivery** | `deliverReadyPackets(now)` | `deliverReadyPacketsLocked(now)` | EventLoop / Tick | `tick.go` |
| **Contiguous Scan** | `contiguousScan(now)` | `contiguousScanLocked(now)` | EventLoop / Tick | `scan.go` |
| **Data Ring Drain** | `drainRingByDelta()` | (no lock variant) | EventLoop / Tick | `ring.go` |
| **Push to Btree** | `pushLockedNakBtree(pkt)` | `pushLocked(pkt)` | Ring drain / Push() | `push.go` |
| **ACKACK Processing** | `handleACKACK(ackNum, time)` | `handleACKACKLocked(pkt)` | EventLoop / io_uring | `connection.go` (NEW) |
| **KEEPALIVE Processing** | `handleKeepAlive()` | `handleKeepAliveLocked()` | EventLoop / io_uring | `connection.go` (NEW) |
| **Control Processing** | `processControlPackets()` | (N/A - EventLoop only) | EventLoop | `connection.go` (NEW) |

**Side-by-Side Comparison:**

| Operation | Sender Primary | Sender Locked | Receiver Primary | Receiver Locked |
|-----------|---------------|---------------|------------------|-----------------|
| **Control Pkt Type 1** | `ackBtree()` | `ackLocked()` | `handleACKACK()` | `handleACKACKLocked()` |
| **Control Pkt Type 2** | `nakBtree()` | `nakLocked()` | `handleKeepAlive()` | `handleKeepAliveLocked()` |
| **Drain Control Ring** | `processControlPacketsDelta()` | — | `processControlPackets()` | — |
| **Drain Data Ring** | `drainRingToBtreeEventLoop()` | `drainRingToBtree()` | `drainRingByDelta()` | — |
| **Deliver Packets** | `deliverReadyPacketsEventLoop()` | `tickDeliverPacketsBtree()` | `deliverReadyPackets(now)` | `deliverReadyPacketsLocked(now)` |
| **Periodic Timer 1** | — | — | `periodicACK()` | `periodicACKLocked()` |
| **Periodic Timer 2** | — | — | `periodicNakBtree()` | `periodicNakBtreeLocked()` |
| **Push to Store** | `pushBtree()` | `pushLocked()` | `pushLockedNakBtree()` | `pushLocked()` |

**Function Dispatch Configuration:**

```go
// SENDER (congestion/live/send/sender.go)
// Configured at initialization based on useEventLoop and useControlRing

// Control packet handling:
// - EventLoop mode: ACK() → controlRing.PushACK() → EventLoop → ackBtree()
// - Tick mode: ACK() → ackLocked()

// Data handling:
// - EventLoop mode: Push() → packetRing.Write() → EventLoop → pushBtree()
// - Tick mode: Push() → pushLocked()


// RECEIVER (congestion/live/receive/receiver.go + connection.go)
// Configured at initialization based on useEventLoop and useRecvControlRing

// Control packet handling:
// - EventLoop mode: ACKACK → controlRing.PushACKACK() → EventLoop → handleACKACK()
// - Tick mode: ACKACK → handleACKACKLocked()

// Data handling:
// - EventLoop mode: Push() → packetRing.Write() → EventLoop → periodic functions (no lock)
// - Tick mode: Push() → pushLocked() → Tick() → periodic*Locked()
```

#### 3.4.7 Data Structure Comparison

```go
// ═══════════════════════════════════════════════════════════════════════════
// SHARED: congestion/live/common/control_ring.go
// ═══════════════════════════════════════════════════════════════════════════

// Generic control ring - used by both sender and receiver
type ControlRing[T any] struct {
    ring   *ring.ShardedRing
    shards int
}

func NewControlRing[T any](size, shards int) (*ControlRing[T], error) { ... }
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool { ... }
func (r *ControlRing[T]) TryPop() (T, bool) { ... }
func (r *ControlRing[T]) Len() int { ... }

// ═══════════════════════════════════════════════════════════════════════════
// SENDER: congestion/live/send/control_ring.go
// ═══════════════════════════════════════════════════════════════════════════

type ControlPacketType uint8
const (
    ControlTypeACK ControlPacketType = iota
    ControlTypeNAK
)

type ControlPacket struct {
    Type        ControlPacketType
    ACKSequence uint32           // For ACK
    NAKCount    int              // For NAK
    NAKSequences [32]uint32      // For NAK (fixed array, no alloc)
}

// SendControlRing embeds the generic ring with sender-specific methods
type SendControlRing struct {
    *common.ControlRing[ControlPacket]  // Embedded generic ring
}

func NewSendControlRing(size, shards int) (*SendControlRing, error) {
    ring, err := common.NewControlRing[ControlPacket](size, shards)
    if err != nil { return nil, err }
    return &SendControlRing{ring}, nil
}

func (r *SendControlRing) PushACK(seq circular.Number) bool { ... }
func (r *SendControlRing) PushNAK(seqs []circular.Number) bool { ... }
// TryPop() and Len() inherited from common.ControlRing

// ═══════════════════════════════════════════════════════════════════════════
// RECEIVER: congestion/live/receive/control_ring.go
// ═══════════════════════════════════════════════════════════════════════════

type RecvControlPacketType uint8
const (
    RecvControlTypeACKACK RecvControlPacketType = iota
    RecvControlTypeKEEPALIVE
)

type RecvControlPacket struct {
    Type      RecvControlPacketType
    ACKNumber uint32            // For ACKACK (mirrors ACKSequence)
    Timestamp int64             // For ACKACK (RTT calculation)
    // KEEPALIVE has no additional data (simpler than NAK)
}

// RecvControlRing embeds the generic ring with receiver-specific methods
type RecvControlRing struct {
    *common.ControlRing[RecvControlPacket]  // Embedded generic ring
}

func NewRecvControlRing(size, shards int) (*RecvControlRing, error) {
    ring, err := common.NewControlRing[RecvControlPacket](size, shards)
    if err != nil { return nil, err }
    return &RecvControlRing{ring}, nil
}

func (r *RecvControlRing) PushACKACK(ackNum uint32, arrivalTime time.Time) bool { ... }
func (r *RecvControlRing) PushKEEPALIVE() bool { ... }
// TryPop() and Len() inherited from common.ControlRing
```

#### 3.4.8 Key Differences

| Aspect | Sender | Receiver | Why Different |
|--------|--------|----------|---------------|
| **Ring Infrastructure** | `common.ControlRing[ControlPacket]` | `common.ControlRing[RecvControlPacket]` | Same shared generic |
| **Timestamp in ControlPacket** | No | Yes | ACKACK needs arrival time for RTT |
| **NAK data** | Fixed array [32]uint32 | (N/A) | Sender receives NAK list, receiver sends NAK |
| **Control ring location** | `congestion/live/send/` | Connection level | ACKACK involves `c.ackNumbers` at connection level |
| **Second btree** | None | `nakBtree` | Receiver tracks pending NAKs |
| **Outgoing control** | Via `c.sendACK`/`c.sendNAK` callbacks | Via `sendACK`/`sendNAK` callbacks | Same callback pattern |

**Shared Infrastructure (in `common/`):**
- Generic `ControlRing[T]` with `Push()`, `TryPop()`, `Len()`, `Shards()`
- Ring creation logic (`NewControlRing[T]()`)
- MPSC (multi-producer, single-consumer) semantics

**Type-Specific (in `send/` and `receive/`):**
- Packet type definitions (`ControlPacket`, `RecvControlPacket`)
- Convenience push methods (`PushACK()`, `PushNAK()`, `PushACKACK()`, `PushKEEPALIVE()`)

#### 3.4.9 Code Reuse via `common.ControlRing[T]`

With the shared `common.ControlRing[T]` infrastructure, most ring mechanics are implemented once and shared between sender and receiver:

**What is Shared (in `common/control_ring.go`):**

```go
// Generic ring - Push/TryPop/Len implemented once
type ControlRing[T any] struct { ... }
func NewControlRing[T any](size, shards int) (*ControlRing[T], error) { ... }
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool { ... }
func (r *ControlRing[T]) TryPop() (T, bool) { ... }
func (r *ControlRing[T]) Len() int { ... }
```

**What is Type-Specific (in `send/` and `receive/`):**

```go
// Sender: send/control_ring.go
type SendControlRing struct {
    *common.ControlRing[ControlPacket]  // Embed shared ring
}
func (r *SendControlRing) PushACK(seq circular.Number) bool { ... }
func (r *SendControlRing) PushNAK(seqs []circular.Number) bool { ... }

// Receiver: receive/control_ring.go
type RecvControlRing struct {
    *common.ControlRing[RecvControlPacket]  // Embed shared ring
}
func (r *RecvControlRing) PushACKACK(ackNum uint32, time time.Time) bool { ... }
func (r *RecvControlRing) PushKEEPALIVE() bool { ... }
```

**EventLoop Pop Pattern (same structure for both):**

```go
// Sender EventLoop (send/eventloop.go)
for {
    cp, ok := s.controlRing.TryPop()  // Uses inherited TryPop()
    if !ok { break }
    switch cp.Type {
    case ControlTypeACK: s.ackBtree(...)
    case ControlTypeNAK: s.nakBtree(...)
    }
}

// Receiver EventLoop (connection.go)
for {
    cp, ok := c.controlRing.TryPop()  // Uses inherited TryPop()
    if !ok { break }
    switch cp.Type {
    case RecvControlTypeACKACK: c.handleACKACKFn(...)
    case RecvControlTypeKEEPALIVE: c.handleKeepAliveFn()
    }
}
```

**Function Dispatch Setup (same pattern for both):**

```go
// Sender initialization
if config.UseControlRing {
    s.ackFn = s.ackBtree        // Primary (lock-free)
} else {
    s.ackFn = s.ackLocked       // Locking wrapper
}

// Receiver initialization
if config.UseRecvControlRing {
    c.handleACKACKFn = c.handleACKACK        // Primary (lock-free)
} else {
    c.handleACKACKFn = c.handleACKACKLocked  // Locking wrapper
}
```

##### 3.4.9.1 Package Structure for Shared Code

**Adopted Structure:**

```
congestion/live/
├── common/           # SHARED: Generic control ring infrastructure
│   ├── control_ring.go      # Generic ControlRing[T] with type parameter
│   ├── control_ring_test.go # Shared tests for ring mechanics
│   └── doc.go
├── receive/          # Receiver-specific code
│   ├── receiver.go
│   ├── control_ring.go      # RecvControlRing embeds common.ControlRing
│   ├── ack.go
│   ├── nak.go
│   ├── tick.go
│   ├── ring.go              # Data packet ring
│   └── ...
├── send/             # Sender-specific code
│   ├── sender.go
│   ├── control_ring.go      # SendControlRing embeds common.ControlRing (REFACTORED)
│   ├── ack.go
│   ├── nak.go
│   ├── tick.go
│   ├── data_ring.go         # Data packet ring
│   └── ...
├── doc.go
├── fake.go
└── testing.go
```

**What's in `common/control_ring.go`:**

```go
// congestion/live/common/control_ring.go

package common

import (
    ring "github.com/randomizedcoder/go-lock-free-ring"
)

// ControlRing is a generic lock-free MPSC ring for control packets.
// T is the control packet type (send.ControlPacket or receive.RecvControlPacket).
//
// Provides shared infrastructure - sender and receiver define their own packet types.
type ControlRing[T any] struct {
    ring   *ring.ShardedRing
    shards int
}

// NewControlRing creates a control ring with configurable size and shards.
func NewControlRing[T any](size, shards int) (*ControlRing[T], error) {
    if shards < 1 {
        shards = 1  // Default: 1 shard for simplicity
    }
    if size < 1 {
        size = 128  // Default size: 128
    }

    totalCapacity := uint64(size * shards)
    r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
    if err != nil {
        return nil, fmt.Errorf("failed to create control ring: %w", err)
    }

    return &ControlRing[T]{ring: r, shards: shards}, nil
}

// Push writes a control packet to the ring using the given shard ID.
// Thread-safe: called from io_uring handlers (multiple goroutines).
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool {
    return r.ring.Write(shardID, packet)
}

// TryPop reads a control packet from the ring.
// NOT thread-safe for multiple consumers - designed for single EventLoop.
func (r *ControlRing[T]) TryPop() (T, bool) {
    item, ok := r.ring.TryRead()
    if !ok {
        var zero T
        return zero, false
    }
    packet, ok := item.(T)
    if !ok {
        var zero T
        return zero, false
    }
    return packet, true
}

// Len returns the current ring length.
func (r *ControlRing[T]) Len() int {
    return int(r.ring.Len())
}
```

**Usage in Sender:**

```go
// congestion/live/send/control_ring.go

package send

import "github.com/randomizedcoder/gosrt/congestion/live/common"

// ControlPacket is sender-specific (ACK/NAK data)
type ControlPacket struct {
    Type         ControlPacketType
    ACKSequence  uint32
    NAKCount     int
    NAKSequences [32]uint32
}

// SendControlRing wraps the generic ring with sender-specific methods.
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

// PushACK is a convenience method for pushing ACK packets.
func (r *SendControlRing) PushACK(seq circular.Number) bool {
    return r.Push(uint64(ControlTypeACK), ControlPacket{
        Type:        ControlTypeACK,
        ACKSequence: seq.Val(),
    })
}
```

**Usage in Receiver:**

```go
// congestion/live/receive/control_ring.go (NEW)

package receive

import "github.com/randomizedcoder/gosrt/congestion/live/common"

// RecvControlPacket is receiver-specific (ACKACK/KEEPALIVE data)
type RecvControlPacket struct {
    Type      RecvControlPacketType
    ACKNumber uint32
    Timestamp int64
}

// RecvControlRing wraps the generic ring with receiver-specific methods.
type RecvControlRing struct {
    *common.ControlRing[RecvControlPacket]
}

func NewRecvControlRing(size, shards int) (*RecvControlRing, error) {
    ring, err := common.NewControlRing[RecvControlPacket](size, shards)
    if err != nil {
        return nil, err
    }
    return &RecvControlRing{ring}, nil
}

// PushACKACK is a convenience method for pushing ACKACK packets.
func (r *RecvControlRing) PushACKACK(ackNum uint32, arrivalTime time.Time) bool {
    return r.Push(uint64(RecvControlTypeACKACK), RecvControlPacket{
        Type:      RecvControlTypeACKACK,
        ACKNumber: ackNum,
        Timestamp: arrivalTime.UnixNano(),
    })
}
```

##### 3.4.9.2 Why `common.ControlRing[T]` (Not Alternatives)

**Alternatives Considered and Rejected:**

**Alternative B: Interface-Based Sharing**

```go
// Would define interfaces instead of generic struct
type ControlRingPusher interface { Len() int }
type ControlRingPopper[T any] interface { TryPop() (T, bool); Len() int }
```

*Rejected because:*
- Less code sharing (implementation still duplicated)
- Each ring would have completely different implementations
- Testing must be duplicated

**Alternative C: Keep Separate (No Sharing)**

```
congestion/live/
├── receive/control_ring.go  # RecvControlRing (copy pattern from sender)
├── send/control_ring.go     # SendControlRing (existing)
```

*Rejected because:*
- Code duplication (~100 lines)
- Tests must be duplicated
- Bug fixes need to be applied twice

##### 3.4.9.3 Benefits of Adopted Approach

**Adopted: `congestion/live/common/` with generic `ControlRing[T]`** for the following reasons:

1. **Reduced code duplication:** Generic `ControlRing[T]` eliminates ~80% of duplicate code
2. **Single test suite:** Tests for ring mechanics live in `common/`, only type-specific tests in sender/receiver
3. **Consistent behavior:** Bug fixes in `common/` apply to both sender and receiver
4. **Clear separation:** Packet type definitions stay in sender/receiver packages
5. **Go 1.18+ generics:** Modern Go idiom, type-safe without reflection

**Sharing Breakdown:**

| Component | In `common/` | In `send/` or `receive/` | Reason |
|-----------|:------------:|:------------------------:|--------|
| `ControlRing[T]` struct | ✅ | — | Core ring mechanics identical |
| `NewControlRing[T]()` | ✅ | — | Ring creation identical |
| `Push()`, `TryPop()`, `Len()` | ✅ | — | Ring operations identical |
| Packet types | — | ✅ | Different fields (NAKSequences vs Timestamp) |
| Push convenience methods | — | ✅ | Type-specific (`PushACK` vs `PushACKACK`) |
| EventLoop drain loop | — | ✅ | Processing logic differs per packet type |

**Implementation Steps:**

1. ✅ Create `congestion/live/common/` package
2. ✅ Implement generic `ControlRing[T]` in `common/control_ring.go`
3. ✅ Create `RecvControlRing` embedding `common.ControlRing[RecvControlPacket]`
4. ⏳ Refactor existing `SendControlRing` to embed `common.ControlRing[ControlPacket]`
5. ⏳ Verify sender tests pass after refactoring
6. ⏳ Write receiver-specific tests for `RecvControlRing`

#### 3.4.10 Required Refactoring: Naming Consistency

Two naming inconsistencies need to be addressed:

##### Issue 1: `NoLock` Suffix on Primary Function

**Problem:** `deliverReadyPacketsNoLock` violates the naming convention. Per section 3.4.5, primary (lock-free) functions should have **no suffix**, while locking wrappers have `*Locked` suffix.

**Required Rename:** `deliverReadyPacketsNoLock` → DELETE (use primary function directly)

##### Issue 2: `WithTime` Suffix is Redundant

**Problem:** Many functions take `now uint64` as an argument, so `WithTime` suffix is redundant and inconsistent. The sender uses `(nowUs uint64)` without a `WithTime` suffix.

**Functions with `WithTime` suffix to rename:**

| File | Line | Current Function | Proposed Name |
|------|------|------------------|---------------|
| `congestion/live/receive/tick.go` | 424 | `deliverReadyPacketsWithTime(now uint64)` | `deliverReadyPacketsCore(now uint64)` or just inline |
| `congestion/live/receive/scan.go` | 83 | `contiguousScanWithTime(now uint64)` | `contiguousScan(now uint64)` |

**Note:** `NakEntryWithTime` is a **type name** describing struct contents, not a function - this is correct and should NOT be renamed.

##### Complete Refactoring Plan

**File: `congestion/live/receive/tick.go`**

```go
// BEFORE:
func (r *receiver) deliverReadyPackets() int {              // Line 459: no-arg wrapper
    return r.deliverReadyPacketsWithTime(r.nowFn())
}
func (r *receiver) deliverReadyPacketsLocked(now uint64) int {  // Line 465: locking wrapper
    r.lock.Lock()
    defer r.lock.Unlock()
    return r.deliverReadyPacketsWithTime(now)
}
func (r *receiver) deliverReadyPacketsWithTime(now uint64) int {  // Line 424: core implementation
    // ... implementation ...
}
func (r *receiver) deliverReadyPacketsNoLock(now uint64) int {  // Line 481: WRONG NAME
    return r.deliverReadyPacketsWithTime(now)
}

// AFTER:
func (r *receiver) deliverReadyPackets(now uint64) int {        // Primary function (takes now)
    // ... core implementation (moved from deliverReadyPacketsWithTime) ...
}
func (r *receiver) deliverReadyPacketsLocked(now uint64) int {  // Locking wrapper
    r.lock.Lock()
    defer r.lock.Unlock()
    return r.deliverReadyPackets(now)
}
// DELETE: deliverReadyPacketsWithTime (renamed to deliverReadyPackets)
// DELETE: deliverReadyPacketsNoLock (callers use deliverReadyPackets directly)
// DELETE: no-arg deliverReadyPackets() (callers pass r.nowFn() explicitly)
```

**File: `congestion/live/receive/scan.go`**

```go
// BEFORE:
func (r *receiver) contiguousScan() (ok bool, ackSeq uint32) {           // Line 75
    ok, ackSeq, _ = r.contiguousScanWithTime(r.nowFn())
    return ok, ackSeq
}
func (r *receiver) contiguousScanWithTime(now uint64) (...) {            // Line 83
    // ... implementation ...
}

// AFTER:
func (r *receiver) contiguousScan(now uint64) (ok bool, ackSeq uint32, skippedPkts uint64) {
    // ... implementation (moved from contiguousScanWithTime) ...
}
func (r *receiver) contiguousScanLocked(now uint64) (...) {              // NEW: locking wrapper
    r.lock.RLock()
    defer r.lock.RUnlock()
    return r.contiguousScan(now)
}
// DELETE: contiguousScanWithTime (renamed to contiguousScan)
// DELETE: no-arg contiguousScan() (callers pass now explicitly)
```

##### Call Site Updates Required

| File | Line | Current | After |
|------|------|---------|-------|
| `tick.go` | 481 | `deliverReadyPacketsNoLock(now)` | DELETE function |
| `tick.go` | 460 | `deliverReadyPacketsWithTime(r.nowFn())` | `deliverReadyPackets(r.nowFn())` |
| `tick.go` | 469,474 | `deliverReadyPacketsWithTime(now)` | `deliverReadyPackets(now)` |
| `scan.go` | 76 | `contiguousScanWithTime(r.nowFn())` | `contiguousScan(r.nowFn())` |
| `utility_test.go` | 196 | `deliverReadyPacketsNoLock(...)` | `deliverReadyPackets(...)` |
| `hotpath_bench_test.go` | 327 | `deliverReadyPacketsWithTime(nowUs)` | `deliverReadyPackets(nowUs)` |

##### Final Naming Convention

| Pattern | Example | Description |
|---------|---------|-------------|
| `functionName(now uint64)` | `deliverReadyPackets(now)` | Primary function (no suffix) |
| `functionNameLocked(now uint64)` | `deliverReadyPacketsLocked(now)` | Locking wrapper |
| NO `WithTime` suffix | — | Redundant since `now` arg is explicit |
| NO `NoLock` suffix | — | Primary functions are lock-free by default |

**Related Documentation:**
- `ack_optimization_plan.md:1365-1374` - Already notes this rename
- `ack_optimization_implementation_plan.md:64-95` - Details the rename

#### 3.4.11 Verification Checklist

To ensure the receiver mirrors the sender correctly, verify:

- [ ] `RecvControlRing` has same MPSC semantics as `SendControlRing`
- [ ] `TryPop()` loop in EventLoop matches sender pattern
- [ ] Function dispatch configured at startup (not runtime check)
- [ ] Primary functions have no suffix, wrappers have `*Locked`
- [ ] **Rename `deliverReadyPacketsNoLock` → remove or rename** (see 3.4.10)
- [ ] Metrics follow same naming pattern (`recv_control_ring_*` vs `send_control_ring_*`)
- [ ] Ring full fallback to locked path (same as sender)
- [ ] Integration test uses same configuration flags pattern

---

## 4. Detailed Design

### 4.1 RecvControlRing

**Purpose:** Route ALL incoming control packets (ACKACK, KEEPALIVE) to EventLoop for processing, making io_uring handlers completely lock-free.

**Architecture:** Uses the shared `common.ControlRing[T]` generic type (see Section 3.4.9.1) with receiver-specific packet types.

#### 4.1.1 Shared Infrastructure (`congestion/live/common/`)

```go
// File: congestion/live/common/control_ring.go

package common

import (
    "fmt"
    ring "github.com/randomizedcoder/go-lock-free-ring"
)

// ═══════════════════════════════════════════════════════════════════════════════
// ControlRing[T] - Generic lock-free ring for control packets
//
// Used by both sender (ControlPacket) and receiver (RecvControlPacket).
// Eliminates code duplication while allowing type-specific packet definitions.
//
// Reference: Section 3.4.9.1 Package Structure for Shared Code
// ═══════════════════════════════════════════════════════════════════════════════

// ControlRing is a generic lock-free ring for control packets.
// T is the control packet type (send.ControlPacket or receive.RecvControlPacket).
type ControlRing[T any] struct {
    ring   *ring.ShardedRing
    shards int
}

// NewControlRing creates a control ring with configurable size and shards.
func NewControlRing[T any](size, shards int) (*ControlRing[T], error) {
    if shards < 1 {
        shards = 1  // Default: 1 shard for simplicity
    }
    if size < 1 {
        size = 128  // Default size
    }

    totalCapacity := uint64(size * shards)
    r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
    if err != nil {
        return nil, fmt.Errorf("failed to create control ring: %w", err)
    }

    return &ControlRing[T]{ring: r, shards: shards}, nil
}

// Push writes a control packet to the ring using the given shard ID.
// Thread-safe: called from io_uring handlers (multiple goroutines).
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool {
    return r.ring.Write(shardID, packet)
}

// TryPop reads a control packet from the ring.
// NOT thread-safe for multiple consumers - designed for single EventLoop.
func (r *ControlRing[T]) TryPop() (T, bool) {
    item, ok := r.ring.TryRead()
    if !ok {
        var zero T
        return zero, false
    }
    packet, ok := item.(T)
    if !ok {
        var zero T
        return zero, false
    }
    return packet, true
}

// Len returns the current ring length.
func (r *ControlRing[T]) Len() int {
    return int(r.ring.Len())
}
```

#### 4.1.2 Receiver-Specific Types (`congestion/live/receive/`)

```go
// File: congestion/live/receive/control_ring.go

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
    // Future: RecvControlTypeSHUTDOWN if needed
)

// RecvControlPacket wraps control packets for ring transport.
// Value type (not pointer) to avoid allocations in the hot path.
type RecvControlPacket struct {
    Type      RecvControlPacketType
    ACKNumber uint32  // For ACKACK: the ACK number being acknowledged
    Timestamp int64   // For ACKACK: arrival time (UnixNano) for RTT calculation
    // KEEPALIVE needs no additional data - just the packet type
}

// RecvControlRing wraps the generic ControlRing with receiver-specific methods.
// Embeds common.ControlRing[RecvControlPacket] for shared functionality.
type RecvControlRing struct {
    *common.ControlRing[RecvControlPacket]
}

// NewRecvControlRing creates a receiver control ring.
// Defaults: 1 shard, 128 entries (same as sender for consistency).
func NewRecvControlRing(size, shards int) (*RecvControlRing, error) {
    if size < 1 {
        size = 128  // Same default as sender
    }
    if shards < 1 {
        shards = 1  // Single shard for simplicity
    }
    ring, err := common.NewControlRing[RecvControlPacket](size, shards)
    if err != nil {
        return nil, err
    }
    return &RecvControlRing{ring}, nil
}

// PushACKACK pushes an ACKACK to the control ring.
// Thread-safe: called from io_uring handlers (multiple goroutines).
// Captures arrival time for accurate RTT calculation.
func (r *RecvControlRing) PushACKACK(ackNum uint32, arrivalTime time.Time) bool {
    return r.Push(uint64(RecvControlTypeACKACK), RecvControlPacket{
        Type:      RecvControlTypeACKACK,
        ACKNumber: ackNum,
        Timestamp: arrivalTime.UnixNano(),
    })
}

// PushKEEPALIVE pushes a KEEPALIVE to the control ring.
// Thread-safe: called from io_uring handlers (multiple goroutines).
func (r *RecvControlRing) PushKEEPALIVE() bool {
    return r.Push(uint64(RecvControlTypeKEEPALIVE), RecvControlPacket{
        Type: RecvControlTypeKEEPALIVE,
    })
}
```

#### 4.1.3 Design Rationale

| Aspect | Decision | Rationale |
|--------|----------|-----------|
| **Generic `ControlRing[T]`** | Shared in `common/` | Eliminates ~80% code duplication |
| **Packet types** | Receiver-specific | Different fields (Timestamp vs NAKSequences) |
| **Push methods** | Receiver-specific | Type-safe convenience methods |
| **TryPop** | Inherited from generic | Same behavior for sender and receiver |
| **Default size** | 128 (same as sender) | Consistent defaults for simplicity |

**Benefits of Code Reuse:**

1. **Single test suite** for ring mechanics in `common/`
2. **Bug fixes apply to both** sender and receiver
3. **Consistent behavior** guaranteed by shared implementation
4. **Type safety** via Go 1.18+ generics (no reflection)
5. **Clear separation** - packet types stay in sender/receiver packages

### 4.2 Lock-Free Function Variants

Following the sender pattern of "Locked" vs "Btree/EventLoop" function variants:

| Current Function | Locked Variant | EventLoop Variant |
|-----------------|----------------|-------------------|
| `periodicACK()` | `periodicACKLocked()` | `periodicACKEventLoop()` |
| `periodicNAK()` | `periodicNAKLocked()` | `periodicNAKEventLoop()` |
| `deliverReadyPackets()` | `deliverReadyPacketsLocked()` | `deliverReadyPacketsEventLoop()` |
| `contiguousScan()` | `contiguousScanLocked()` | `contiguousScanEventLoop()` |
| `drainRingByDelta()` | N/A | `drainRingToBtreeEventLoop()` |

### 4.3 ACKACK Handling Strategy

**See Section 3.2.2** for control ring routing diagram.

**Summary:** Route ACKACK through control ring, process in EventLoop.

#### 4.3.1 io_uring Handler (Lock-Free)

```go
// connection_handlers.go - Modified to route through control ring
func (c *srtConn) handleACKACKPacket(p packet.Packet) {
    if c.useRecvControlRing && c.recvControlRing != nil {
        // EventLoop mode: route through control ring (NO LOCK)
        ackNum := p.Header().TypeSpecific
        arrivalTime := time.Now()  // Capture arrival time immediately

        if c.recvControlRing.PushACKACK(ackNum, arrivalTime) {
            c.metrics.RecvControlRingPushedACKACK.Add(1)
            return
        }
        // Ring full - fallback to direct processing
        c.metrics.RecvControlRingDroppedACKACK.Add(1)
    }

    // Legacy path OR ring-full fallback: process with lock
    c.handleACKACKLocked(p)
}
```

#### 4.3.2 EventLoop Processing

```go
// connection.go - EventLoop processes control packets
func (c *srtConn) processControlPackets() int {
    if c.recvControlRing == nil {
        return 0
    }

    processed := 0
    for {
        cp, ok := c.recvControlRing.TryPop()
        if !ok {
            break
        }

        c.metrics.RecvControlRingDrained.Add(1)

        switch cp.Type {
        case RecvControlTypeACKACK:
            // Convert UnixNano back to time.Time for RTT calculation
            arrivalTime := time.Unix(0, cp.Timestamp)
            c.handleACKACKFn(cp.ACKNumber, arrivalTime)
            c.metrics.RecvControlRingProcessedACKACK.Add(1)

        case RecvControlTypeKEEPALIVE:
            c.handleKeepAliveFn()
            c.metrics.RecvControlRingProcessedKEEPALIVE.Add(1)
        }

        processed++
    }
    return processed
}
```

#### 4.3.3 Function Dispatch Pattern

Following `lockless_sender_implementation_plan.md` Step 1.8:

```go
// connection.go - Function dispatch fields
type srtConn struct {
    // ... existing fields ...

    // Control packet function dispatch
    // Primary functions (no lock prefix) - called from EventLoop
    // Locking wrappers (*Locked suffix) - called from Tick/legacy path
    handleACKACKFn    func(ackNum uint32, arrivalTime time.Time)
    handleKeepAliveFn func()

    // Control ring
    useRecvControlRing bool
    recvControlRing    *RecvControlRing
}

// handleACKACK is the primary (lock-free) implementation.
// Called from EventLoop - single consumer, NO LOCK needed.
//
// The EventLoop is the SOLE accessor of c.ackNumbers btree, so no locking
// is required. This is the key design goal - completely lock-free EventLoop.
func (c *srtConn) handleACKACK(ackNum uint32, arrivalTime time.Time) {
    // NO LOCK - EventLoop is sole accessor of ackNumbers btree
    entry := c.ackNumbers.Get(ackNum)
    if entry != nil {
        rttDuration := arrivalTime.Sub(entry.timestamp)
        c.recalculateRTT(rttDuration)  // Atomic - no lock needed
        c.ackNumbers.Delete(ackNum)
        PutAckEntry(entry)
    }
    expired := c.ackNumbers.ExpireOlderThan(ackNum)
    // NO UNLOCK - no lock was acquired

    PutAckEntries(expired)
    c.recv.SetNAKInterval(uint64(c.rtt.NAKInterval()))  // Atomic - no lock needed
}

// handleACKACKLocked is the locking wrapper for Tick/legacy mode.
// Called from io_uring handlers when control ring is NOT enabled.
// Acquires c.ackLock to protect ackNumbers btree access.
func (c *srtConn) handleACKACKLocked(p packet.Packet) {
    ackNum := p.Header().TypeSpecific
    arrivalTime := time.Now()

    c.ackLock.Lock()
    defer c.ackLock.Unlock()
    c.handleACKACK(ackNum, arrivalTime)
}
```

**Key Points:**
1. io_uring handler does NO locking - just `ring.Write()`
2. EventLoop calls primary `handleACKACK()` function
3. Tick/legacy mode calls `handleACKACKLocked()` wrapper
4. RTT accuracy preserved by capturing arrival time in io_uring handler

### 4.4 Function Dispatch Pattern

Following `lockless_sender_implementation_plan.md` Step 1.8 and existing receiver pattern in `receiver.go:171-186`.

**Pattern:** Primary functions (no suffix) are lock-free, locking wrappers have `*Locked` suffix.

```go
// File: congestion/live/receive/receiver.go

type receiver struct {
    // ... existing fields ...

    // ═══════════════════════════════════════════════════════════════════════
    // Function Dispatch Pattern (following sender: lockless_sender_design.md)
    //
    // Primary functions:      Lock-free, called from EventLoop (single consumer)
    // Locking wrappers:       *Locked suffix, called from Tick() mode
    //
    // This pattern allows:
    // - Zero lock overhead in EventLoop mode
    // - Safe operation in Tick() mode (concurrent Push/Tick)
    // - Single implementation with thin locking wrappers
    // ═══════════════════════════════════════════════════════════════════════

    // Packet processing function dispatch
    contiguousScanFn      func() (bool, uint32)
    periodicACKFn         func(now uint64) (bool, circular.Number, bool)
    periodicNAKFn         func(now uint64) []circular.Number
    deliverReadyPacketsFn func(now uint64) int

    // Existing NAK btree function dispatch (already implemented)
    // nakInsert, nakInsertBatch, nakDelete, nakDeleteBefore, nakLen
    // nakInsertBatchWithTsbpd, nakDeleteBeforeTsbpd, nakIterateAndUpdate

    // Configuration
    useEventLoop       bool
    useRecvControlRing bool  // NEW: Enable control ring for completely lock-free
}

// setupFunctionDispatch configures function pointers based on execution mode.
// Called once at receiver initialization for zero runtime overhead.
//
// Reference: lockless_sender_implementation_plan.md Step 1.8
// Reference: congestion/live/receive/receiver.go:544-580 (existing setupNakDispatch)
func (r *receiver) setupFunctionDispatch() {
    if r.useEventLoop && r.useRecvControlRing {
        // ═══════════════════════════════════════════════════════════════════
        // COMPLETELY LOCK-FREE MODE
        // EventLoop is single consumer - NO LOCKS for receiver state (r.lock)
        // ═══════════════════════════════════════════════════════════════════
        r.contiguousScanFn = r.contiguousScan           // Primary (no lock)
        r.periodicACKFn = r.periodicACK                 // Primary (no lock)
        r.periodicNAKFn = r.periodicNAK                 // Primary (no lock)
        r.deliverReadyPacketsFn = r.deliverReadyPackets // Primary (no lock)

    } else {
        // ═══════════════════════════════════════════════════════════════════
        // TICK MODE (or EventLoop without control ring)
        // Multiple goroutines may call these - NEED LOCKS
        // ═══════════════════════════════════════════════════════════════════
        r.contiguousScanFn = r.contiguousScanLocked           // Acquires r.lock
        r.periodicACKFn = r.periodicACKLocked                 // Acquires r.lock
        r.periodicNAKFn = r.periodicNAKLocked                 // Acquires r.lock
        r.deliverReadyPacketsFn = r.deliverReadyPacketsLocked // Acquires r.lock
    }
}
```

**Naming Convention (Following Sender):**

| Function Type | Naming | Lock Behavior | Called From |
|--------------|--------|---------------|-------------|
| Primary | `contiguousScan()` | NO LOCK | EventLoop (single consumer) |
| Locking Wrapper | `contiguousScanLocked()` | Acquires `r.lock` | Tick() mode |
| Public API | Uses function pointer | Dispatches to correct variant | External callers |

**Example: periodicACK Function Pair:**

```go
// periodicACK is the PRIMARY (lock-free) implementation.
// Called from EventLoop - single consumer of packetStore.
// Reference: lockless_sender_design.md Section 7.1
func (r *receiver) periodicACK(now uint64) (bool, circular.Number, bool) {
    // Step 7.5.2: Assert EventLoop context (no-op in release builds)
    r.AssertEventLoopContext()

    // NO LOCK: EventLoop is single consumer
    // ... implementation accesses packetStore directly ...
}

// periodicACKLocked is the LOCKING WRAPPER for Tick() mode.
// Acquires r.lock then calls primary function logic.
func (r *receiver) periodicACKLocked(now uint64) (bool, circular.Number, bool) {
    // Step 7.5.2: Assert Tick context (no-op in release builds)
    r.AssertTickContext()

    r.lock.RLock()
    defer r.lock.RUnlock()
    // ... same logic as periodicACK but with lock held ...
}
```

---

## 5. Implementation Details

### 5.1 Control Ring Implementation (Using Code Reuse)

This section details the full implementation using the shared `common.ControlRing[T]` pattern identified in Section 3.4.9.

#### 5.1.1 Shared Generic Ring (`congestion/live/common/control_ring.go`)

**File:** `congestion/live/common/control_ring.go`

```go
//go:build go1.18

// Package common provides shared infrastructure for sender and receiver.
package common

import (
    "fmt"
    ring "github.com/randomizedcoder/go-lock-free-ring"
)

// ═══════════════════════════════════════════════════════════════════════════════
// ControlRing[T] - Generic lock-free ring buffer for control packets
//
// CRITICAL: This ring routes control packets to EventLoop so that the EventLoop
// is the ONLY goroutine accessing btree state. Without this, control packet
// handlers would access state concurrently with EventLoop, causing races.
//
// Used by:
//   - send.SendControlRing (ACK/NAK packets)
//   - receive.RecvControlRing (ACKACK/KEEPALIVE packets)
//
// Reference: lockless_sender_design.md Section 7.4
// Reference: completely_lockfree_receiver.md Section 3.4.9
// ═══════════════════════════════════════════════════════════════════════════════

// ControlRing is a generic lock-free MPSC ring for control packets.
// T is the control packet type - must be a value type (not pointer) for zero allocation.
//
// Thread-safety:
//   - Push(): Thread-safe, can be called from multiple goroutines (io_uring handlers)
//   - TryPop(): NOT thread-safe for multiple consumers, designed for single EventLoop
type ControlRing[T any] struct {
    ring   *ring.ShardedRing
    shards int
}

// NewControlRing creates a generic control ring with configurable size and shards.
//
// Parameters:
//   - size: per-shard capacity (will be rounded to power of 2 by ring library)
//   - shards: number of shards for producer separation (e.g., 2 for ACK/NAK)
//
// Example:
//   ring, _ := NewControlRing[MyPacketType](128, 1)
func NewControlRing[T any](size, shards int) (*ControlRing[T], error) {
    if shards < 1 {
        shards = 1  // Default: 1 shard for simplicity
    }
    if size < 1 {
        size = 128  // Default size
    }

    totalCapacity := uint64(size * shards)
    r, err := ring.NewShardedRing(totalCapacity, uint64(shards))
    if err != nil {
        return nil, fmt.Errorf("failed to create control ring: %w", err)
    }

    return &ControlRing[T]{ring: r, shards: shards}, nil
}

// Push writes a control packet to the ring using the given shard ID.
// Thread-safe: called from io_uring handlers (multiple goroutines).
// Returns true if successful, false if ring is full.
//
// With single shard (default), shardID=0 for all packet types.
// If multiple shards are configured, shardID can be used for load distribution:
//   - Sender: ACK=0, NAK=1
//   - Receiver: ACKACK=0, KEEPALIVE=1
func (r *ControlRing[T]) Push(shardID uint64, packet T) bool {
    return r.ring.Write(shardID, packet)
}

// TryPop reads a control packet from the ring.
// Returns (packet, true) if successful, (zero, false) if ring is empty.
//
// NOT thread-safe for multiple consumers - designed for single EventLoop.
func (r *ControlRing[T]) TryPop() (T, bool) {
    item, ok := r.ring.TryRead()
    if !ok {
        var zero T
        return zero, false
    }
    packet, ok := item.(T)
    if !ok {
        var zero T
        return zero, false
    }
    return packet, true
}

// Len returns an approximate count of items in the ring.
// May be slightly stale due to concurrent access.
func (r *ControlRing[T]) Len() int {
    return int(r.ring.Len())
}

// Shards returns the number of shards configured for this ring.
func (r *ControlRing[T]) Shards() int {
    return r.shards
}
```

#### 5.1.2 Receiver Control Ring (`congestion/live/receive/control_ring.go`)

**File:** `congestion/live/receive/control_ring.go`

```go
//go:build go1.18

package receive

import (
    "time"
    "github.com/randomizedcoder/gosrt/congestion/live/common"
)

// ═══════════════════════════════════════════════════════════════════════════════
// RecvControlRing - Receiver-specific control ring using shared infrastructure
//
// Embeds common.ControlRing[RecvControlPacket] and adds receiver-specific
// convenience methods (PushACKACK, PushKEEPALIVE).
//
// Reference: congestion/live/common/control_ring.go (shared generic)
// Reference: congestion/live/send/control_ring.go (sender equivalent)
// ═══════════════════════════════════════════════════════════════════════════════

// RecvControlPacketType identifies the type of receiver control packet
type RecvControlPacketType uint8

const (
    RecvControlTypeACKACK RecvControlPacketType = iota
    RecvControlTypeKEEPALIVE
    // Future: RecvControlTypeSHUTDOWN if needed
)

// RecvControlPacket wraps control packets for ring transport.
// Value type (not pointer) to avoid allocations in the hot path.
//
// Receiver-specific: includes Timestamp for RTT calculation (sender doesn't need this)
type RecvControlPacket struct {
    Type      RecvControlPacketType
    ACKNumber uint32  // For ACKACK: the ACK number being acknowledged
    Timestamp int64   // For ACKACK: arrival time (UnixNano) for RTT calculation
    // KEEPALIVE needs no additional data - Type field is sufficient
}

// RecvControlRing wraps the generic ControlRing with receiver-specific methods.
// Embedding provides TryPop() and Len() automatically from common.ControlRing.
type RecvControlRing struct {
    *common.ControlRing[RecvControlPacket]
}

// NewRecvControlRing creates a receiver control ring.
//
// Defaults optimized for receiver traffic patterns:
//   - shards: 1 (single shard for simplicity)
//   - size: 128 (same as sender for consistency)
func NewRecvControlRing(size, shards int) (*RecvControlRing, error) {
    if size < 1 {
        size = 128  // Same default as sender
    }
    if shards < 1 {
        shards = 1  // Single shard for simplicity
    }

    ring, err := common.NewControlRing[RecvControlPacket](size, shards)
    if err != nil {
        return nil, err
    }
    return &RecvControlRing{ring}, nil
}

// PushACKACK pushes an ACKACK to the control ring.
// Thread-safe: called from io_uring handlers (multiple goroutines).
// Returns true if successful, false if ring is full.
//
// The arrivalTime is captured in the io_uring handler to preserve RTT accuracy.
func (r *RecvControlRing) PushACKACK(ackNumber uint32, arrivalTime time.Time) bool {
    return r.Push(uint64(RecvControlTypeACKACK), RecvControlPacket{
        Type:      RecvControlTypeACKACK,
        ACKNumber: ackNumber,
        Timestamp: arrivalTime.UnixNano(),
    })
}

// PushKEEPALIVE pushes a KEEPALIVE to the control ring.
// Thread-safe: called from io_uring handlers (multiple goroutines).
// Returns true if successful, false if ring is full.
func (r *RecvControlRing) PushKEEPALIVE() bool {
    return r.Push(uint64(RecvControlTypeKEEPALIVE), RecvControlPacket{
        Type: RecvControlTypeKEEPALIVE,
    })
}
```

#### 5.1.3 Comparison: Sender vs Receiver Control Ring

| Aspect | Sender (`send/control_ring.go`) | Receiver (`receive/control_ring.go`) |
|--------|--------------------------------|-------------------------------------|
| **Embeds** | `*common.ControlRing[ControlPacket]` | `*common.ControlRing[RecvControlPacket]` |
| **Packet types** | ACK, NAK | ACKACK, KEEPALIVE |
| **Default size** | 128 | 128 |
| **Default shards** | 1 | 1 |
| **Extra fields** | NAKSequences [32]uint32 | Timestamp int64 |
| **Push methods** | `PushACK()`, `PushNAK()` | `PushACKACK()`, `PushKEEPALIVE()` |
| **TryPop** | Inherited from common | Inherited from common |
| **Len** | Inherited from common | Inherited from common |

#### 5.1.4 Refactoring SendControlRing to Use common.ControlRing

After implementing `common.ControlRing[T]`, the existing `SendControlRing` should be refactored:

**File:** `congestion/live/send/control_ring.go` (MODIFIED)

```go
package send

import "github.com/randomizedcoder/gosrt/congestion/live/common"

// SendControlRing wraps the generic ControlRing with sender-specific methods.
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

// PushACK, PushNAK methods unchanged - just call r.Push(...)
```

### 5.2 Lock-Free periodicACK

**File:** `congestion/live/receive/ack.go`

```go
// periodicACKEventLoop generates ACK without locking.
// Called from EventLoop - single consumer of btree, no lock needed.
//
// Reference: lockless_sender_implementation_plan.md Step 4.1
func (r *receiver) periodicACKEventLoop(now uint64) (ok bool, seq circular.Number, lite bool) {
    // Step 7.5.2: Assert EventLoop context (no-op in release builds)
    r.AssertEventLoopContext()

    m := r.metrics

    // Early return check
    needLiteACK := false
    if now-r.lastPeriodicACK < r.periodicACKInterval {
        currentSeq := r.maxSeenSequenceNumber.Val()
        diff := circular.SeqSub(currentSeq, r.lastLightACKSeq)
        if diff >= r.lightACKDifference {
            needLiteACK = true
        } else {
            return false, circular.Number{}, false
        }
    }

    // NO LOCK: EventLoop is single consumer of packetStore
    minPktTsbpdTime, maxPktTsbpdTime := uint64(0), uint64(0)
    ackSequenceNumber := r.lastACKSequenceNumber

    minPkt := r.packetStore.Min()
    if minPkt == nil {
        // No packets - send keepalive ACK
        return r.periodicACKWriteEventLoop(now, needLiteACK, ackSequenceNumber,
            minPktTsbpdTime, maxPktTsbpdTime, circular.Number{})
    }

    minH := minPkt.Header()
    minPktTsbpdTime = minH.PktTsbpdTime
    maxPktTsbpdTime = minH.PktTsbpdTime

    // NO LOCK: Iterate btree to find contiguous sequence
    lastContiguousSeq := ackSequenceNumber
    r.packetStore.Ascend(func(p packet.Packet) bool {
        h := p.Header()

        // TSBPD skip logic (same as locked version)
        if h.PktTsbpdTime <= now {
            ackSequenceNumber = h.PacketSequenceNumber
            lastContiguousSeq = ackSequenceNumber
            return true
        }

        if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
            ackSequenceNumber = h.PacketSequenceNumber
            lastContiguousSeq = ackSequenceNumber
            maxPktTsbpdTime = h.PktTsbpdTime
            return true
        }

        return false // Gap found
    })

    return r.periodicACKWriteEventLoop(now, needLiteACK, ackSequenceNumber,
        minPktTsbpdTime, maxPktTsbpdTime, lastContiguousSeq)
}

// periodicACKWriteEventLoop updates state after ACK calculation.
// Called from EventLoop - NO LOCKING (single consumer).
func (r *receiver) periodicACKWriteEventLoop(now uint64, needLiteACK bool,
    ackSequenceNumber circular.Number, minPktTsbpdTime, maxPktTsbpdTime uint64,
    newHighWaterMark circular.Number) (ok bool, sequenceNumber circular.Number, lite bool) {

    m := r.metrics

    if now-r.lastPeriodicACK < r.periodicACKInterval {
        if !needLiteACK {
            return
        }
        lite = true
    }

    r.lastLightACKSeq = ackSequenceNumber.Val()

    if m != nil {
        m.CongestionRecvPeriodicACKRuns.Add(1)
    }

    ok = true
    sequenceNumber = ackSequenceNumber.Inc()
    r.lastACKSequenceNumber = ackSequenceNumber

    // Update high water mark if valid
    if !newHighWaterMark.IsZero() {
        r.contiguousPoint.Store(newHighWaterMark.Val())
    }

    if !lite {
        r.lastPeriodicACK = now
    }

    return
}
```

### 5.3 Lock-Free periodicNAK

**File:** `congestion/live/receive/nak.go`

```go
// periodicNAKEventLoop generates NAK without locking.
// Called from EventLoop - single consumer of btree, no lock needed.
//
// Reference: lockless_sender_implementation_plan.md Step 4.1
func (r *receiver) periodicNAKEventLoop(now uint64) []circular.Number {
    // Step 7.5.2: Assert EventLoop context (no-op in release builds)
    r.AssertEventLoopContext()

    m := r.metrics

    // Check NAK interval
    if now-r.lastPeriodicNAK < r.periodicNAKInterval {
        return nil
    }
    r.lastPeriodicNAK = now

    if m != nil {
        m.CongestionRecvPeriodicNAKRuns.Add(1)
    }

    // Dispatch to appropriate implementation
    if r.useNakBtree {
        return r.periodicNakBtreeEventLoop(now)
    }
    return r.periodicNakOriginalEventLoop(now)
}

// periodicNakBtreeEventLoop implements NAK with btree (no locking).
// Called from EventLoop - NO LOCKING (single consumer).
func (r *receiver) periodicNakBtreeEventLoop(now uint64) []circular.Number {
    // Same logic as periodicNakBtree() but without locks
    // NO LOCK: EventLoop is single consumer of nakBtree

    m := r.metrics
    gaps := r.nakBtree.GetGapsForNAK(now, r.nakRecentPercent)

    if len(gaps) == 0 {
        return nil
    }

    // Consolidate gaps into NAK list
    budget := r.nakConsolidationBudget
    return r.consolidateGapsEventLoop(gaps, budget, now)
}

// consolidateGapsEventLoop consolidates gaps into NAK list (no locking).
// Same logic as consolidateGaps() but for EventLoop context.
func (r *receiver) consolidateGapsEventLoop(gaps []uint32, budget time.Duration, now uint64) []circular.Number {
    // Same implementation as consolidateGaps
    // NO LOCK needed - EventLoop is single consumer

    if len(gaps) == 0 {
        return nil
    }

    // Convert gaps to NAK list with range consolidation
    result := make([]circular.Number, 0, len(gaps)*2)

    // ... same logic as consolidateGaps ...

    return result
}
```

### 5.4 Lock-Free deliverReadyPackets

**File:** `congestion/live/receive/tick.go`

```go
// deliverReadyPacketsEventLoop delivers TSBPD-ready packets without locking.
// Called from EventLoop - single consumer of btree, no lock needed.
//
// Reference: lockless_sender_implementation_plan.md Step 4.1
func (r *receiver) deliverReadyPacketsEventLoop(now uint64) int {
    // Step 7.5.2: Assert EventLoop context (no-op in release builds)
    r.AssertEventLoopContext()

    m := r.metrics
    delivered := 0

    // NO LOCK: EventLoop is single consumer of packetStore
    for {
        minPkt := r.packetStore.Min()
        if minPkt == nil {
            break
        }

        h := minPkt.Header()

        // Check TSBPD time
        if h.PktTsbpdTime > now {
            break // Not ready yet
        }

        // Check sequence order (must be <= lastACKSequenceNumber)
        if h.PacketSequenceNumber.Gt(r.lastACKSequenceNumber) {
            break // Out of order
        }

        // Remove from btree (NO LOCK)
        removed := r.packetStore.DeleteMin()
        if removed == nil {
            break
        }

        // Update metrics
        if m != nil {
            m.CongestionRecvPktDelivered.Add(1)
            m.CongestionRecvByteDelivered.Add(uint64(removed.Len()))
        }

        // Deliver to application
        r.deliver(removed)
        delivered++
    }

    return delivered
}
```

### 5.5 Lock-Free contiguousScan

**File:** `congestion/live/receive/scan.go`

```go
// contiguousScanEventLoop scans btree for contiguous packets without locking.
// Called from EventLoop - single consumer of btree, no lock needed.
//
// Reference: lockless_sender_implementation_plan.md Step 4.1
func (r *receiver) contiguousScanEventLoop() (ok bool, newContiguous uint32) {
    // Step 7.5.2: Assert EventLoop context (no-op in release builds)
    r.AssertEventLoopContext()

    // NO LOCK: EventLoop is single consumer of packetStore
    currentCP := r.contiguousPoint.Load()
    expectedNext := circular.SeqAdd(currentCP, 1)

    found := false
    lastContiguous := currentCP

    r.packetStore.Ascend(func(p packet.Packet) bool {
        seq := p.Header().PacketSequenceNumber.Val()

        if seq == expectedNext {
            lastContiguous = seq
            expectedNext = circular.SeqAdd(seq, 1)
            found = true
            return true // Continue
        }

        return false // Gap found
    })

    if found {
        r.contiguousPoint.Store(lastContiguous)
        return true, lastContiguous
    }

    return false, currentCP
}
```

### 5.6 EventLoop Integration

**File:** `congestion/live/receive/tick.go`

```go
// EventLoop runs the continuous event loop for packet processing.
// COMPLETELY LOCK-FREE when UseRecvControlRing=true.
//
// Reference: completely_lockfree_receiver.md
func (r *receiver) EventLoop(ctx context.Context) {
    if !r.useEventLoop {
        return
    }
    if r.packetRing == nil {
        return
    }

    // Step 7.5.2: Runtime Verification (Debug Mode)
    r.EnterEventLoop()
    defer r.ExitEventLoop()

    // Create backoff manager
    backoff := newAdaptiveBackoff(r.metrics, r.backoffMinSleep, r.backoffMaxSleep, r.backoffColdStartPkts)

    // Tickers
    ackInterval := time.Duration(r.periodicACKInterval) * time.Microsecond
    nakInterval := time.Duration(r.periodicNAKInterval) * time.Microsecond
    rateInterval := r.eventLoopRateInterval

    fullACKTicker := time.NewTicker(ackInterval)
    defer fullACKTicker.Stop()

    time.Sleep(ackInterval / 2) // Offset NAK
    nakTicker := time.NewTicker(nakInterval)
    defer nakTicker.Stop()

    time.Sleep(ackInterval / 4) // Offset rate
    rateTicker := time.NewTicker(rateInterval)
    defer rateTicker.Stop()

    for {
        r.metrics.EventLoopIterations.Add(1)

        select {
        case <-ctx.Done():
            return

        case <-fullACKTicker.C:
            r.metrics.EventLoopFullACKFires.Add(1)
            // Drain ring first
            r.drainRingToBtreeEventLoop()
            // Use EventLoop variant (NO LOCK)
            if ok, newContiguous := r.contiguousScanFn(); ok {
                r.lastACKSequenceNumber = circular.New(newContiguous, packet.MAX_SEQUENCENUMBER)
                r.sendACK(circular.New(circular.SeqAdd(newContiguous, 1), packet.MAX_SEQUENCENUMBER), false)
                r.lastLightACKSeq = newContiguous
            }

        case <-nakTicker.C:
            r.metrics.EventLoopNAKFires.Add(1)
            r.drainRingToBtreeEventLoop()
            now := r.nowFn()
            // Use EventLoop variant (NO LOCK)
            if list := r.periodicNAKFn(now); len(list) != 0 {
                metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
                r.sendNAK(list)
            }
            if r.useNakBtree && r.nakBtree != nil {
                r.expireNakEntriesEventLoop()
            }

        case <-rateTicker.C:
            r.metrics.EventLoopRateFires.Add(1)
            now := r.nowFn()
            r.updateRateStats(now)

        default:
            // No ticker fired
        }

        // Packet processing (every iteration)
        r.metrics.EventLoopDefaultRuns.Add(1)

        // Use EventLoop variants (NO LOCK)
        delivered := r.deliverReadyPacketsFn()
        processed := r.processOnePacket()

        ok, newContiguous := r.contiguousScanFn()
        if ok {
            diff := circular.SeqSub(newContiguous, r.lastLightACKSeq)
            if diff >= r.lightACKDifference {
                forceFullACK := diff >= (r.lightACKDifference * 4)
                lite := !forceFullACK
                r.lastACKSequenceNumber = circular.New(newContiguous, packet.MAX_SEQUENCENUMBER)
                r.sendACK(circular.New(circular.SeqAdd(newContiguous, 1), packet.MAX_SEQUENCENUMBER), lite)
                r.lastLightACKSeq = newContiguous
            }
        }

        // Adaptive backoff
        if !processed && delivered == 0 && !ok {
            r.metrics.EventLoopIdleBackoffs.Add(1)
            time.Sleep(backoff.getSleepDuration())
        } else {
            backoff.recordActivity()
        }
    }
}

// drainRingToBtreeEventLoop drains packets from ring to btree.
// Called from EventLoop - NO LOCKING (single-threaded btree access).
//
// Reference: lockless_sender_implementation_plan.md Step 2.6
func (r *receiver) drainRingToBtreeEventLoop() int {
    // Step 7.5.2: Assert EventLoop context
    r.AssertEventLoopContext()

    if r.packetRing == nil {
        return 0
    }

    m := r.metrics
    drained := 0

    for {
        item, ok := r.packetRing.TryRead()
        if !ok {
            break
        }

        pkt, ok := item.(packet.Packet)
        if !ok {
            continue
        }

        // Insert into btree (NO LOCK - single consumer)
        r.packetStore.Insert(pkt)

        // Update inter-packet timing for TSBPD estimation
        r.updateInterPacketInterval(pkt)

        m.RecvRingDrained.Add(1)
        drained++
    }

    return drained
}
```

### 5.7 Context Asserts (Runtime Verification)

To ensure lock-free functions are only called from the correct context (EventLoop vs Tick), we use runtime asserts in debug builds. This section documents **all** functions that require context asserts for both sender and receiver.

#### 5.7.1 Assert Functions

```go
// debug_context.go (receiver) / debug.go (sender)

// AssertEventLoopContext panics if NOT in EventLoop context.
// Called at start of all lock-free primary functions.
func (r *receiver) AssertEventLoopContext() {
    if !r.debugContext.inEventLoop.Load() {
        panic("receiver: function called outside EventLoop context")
    }
}

// AssertTickContext panics if NOT in Tick context.
// Called at start of all *Locked wrapper functions.
func (r *receiver) AssertTickContext() {
    if !r.debugContext.inTick.Load() {
        panic("receiver: function called outside Tick context")
    }
}

// Context entry/exit (called by EventLoop/Tick):
func (r *receiver) EnterEventLoop() { r.debugContext.inEventLoop.Store(true) }
func (r *receiver) ExitEventLoop()  { r.debugContext.inEventLoop.Store(false) }
func (r *receiver) EnterTick()      { r.debugContext.inTick.Store(true) }
func (r *receiver) ExitTick()       { r.debugContext.inTick.Store(false) }
```

#### 5.7.2 Sender Functions Requiring Asserts

**EventLoop-Only Functions (need `AssertEventLoopContext()`):**

| Function | File | Current Status | Action |
|----------|------|----------------|--------|
| `processControlPacketsDelta()` | `eventloop.go:176` | ✅ Has assert | — |
| `drainRingToBtreeEventLoop()` | `eventloop.go:229` | ✅ Has assert | — |
| `deliverReadyPacketsEventLoop()` | `eventloop.go:318` | ✅ Has assert | — |
| `dropOldPacketsEventLoop()` | `eventloop.go:425` | ✅ Has assert | — |
| `ackBtree()` | `ack.go:48` | ❌ Missing | Add assert |
| `nakBtree()` | `nak.go:58` | ❌ Missing | Add assert |
| `pushBtree()` | `push.go:64` | ❌ Missing | Add assert |

**Tick-Only Functions (need `AssertTickContext()`):**

| Function | File | Current Status | Action |
|----------|------|----------------|--------|
| `tickDeliverPacketsBtree()` | `tick.go:157` | ❌ Missing | Add assert |
| `tickDropOldPacketsBtree()` | `tick.go:241` | ❌ Missing | Add assert |
| `drainRingToBtree()` | `tick.go:73` | ❌ Missing | Add assert |
| `processControlRing()` | `tick.go:113` | ❌ Missing | Add assert |
| `ackLocked()` | `ack.go:38` | ❌ Missing | Add assert |
| `nakLocked()` | `nak.go:45` | ❌ Missing | Add assert |
| `pushLocked()` | `push.go:54` | ❌ Missing | Add assert |

#### 5.7.3 Receiver Functions Requiring Asserts

**EventLoop-Only Functions (need `AssertEventLoopContext()`):**

| Function | File | Current Status | Action |
|----------|------|----------------|--------|
| `processOnePacket()` | `tick.go:347` | ✅ Has assert | — |
| `periodicACK(now)` | `ack.go:131` | ❌ Missing | Add assert |
| `periodicNakBtree(now)` | `nak.go:186` | ❌ Missing | Add assert |
| `deliverReadyPackets(now)` | `tick.go` | ❌ Missing | Add assert |
| `contiguousScan(now)` | `scan.go` | ❌ Missing | Add assert |
| `drainRingByDelta()` | `ring.go:265` | ❌ Missing | Add assert |
| `drainRingToBtreeEventLoop()` | `ring.go` | ❌ Missing | Add assert |
| `expireNakEntriesEventLoop()` | `nak.go` | ❌ Missing | Add assert |

**Tick-Only Functions (need `AssertTickContext()`):**

| Function | File | Current Status | Action |
|----------|------|----------------|--------|
| `periodicACKLocked(now)` | `ack.go:14` | ❌ Missing | Add assert |
| `periodicNakBtreeLocked(now)` | `nak.go:14` | ❌ Missing | Add assert |
| `deliverReadyPacketsLocked(now)` | `tick.go:465` | ❌ Missing | Add assert |
| `contiguousScanLocked(now)` | `scan.go` | ❌ Missing | Add assert |
| `pushLocked()` | `push.go:54` | ❌ Missing | Add assert |

#### 5.7.4 Connection-Level Functions (handleACKACK)

**EventLoop-Only (need `AssertEventLoopContext()` on connection):**

| Function | File | Current Status | Action |
|----------|------|----------------|--------|
| `handleACKACK(ackNum, time)` | `connection.go` | ❌ Missing | Add assert |
| `handleKeepAlive()` | `connection.go` | ❌ Missing | Add assert |
| `processControlPackets()` | `connection.go` | ❌ Missing | Add assert |

**Tick-Only (need `AssertTickContext()` on connection):**

| Function | File | Current Status | Action |
|----------|------|----------------|--------|
| `handleACKACKLocked(pkt)` | `connection.go` | ❌ Missing | Add assert |
| `handleKeepAliveLocked()` | `connection.go` | ❌ Missing | Add assert |

**Note:** Connection-level functions need their own debug context tracking since they're not part of receiver/sender structs:

```go
// connection.go - Add debug context for connection-level EventLoop
type srtConn struct {
    // ... existing fields ...

    // Debug context for connection-level EventLoop verification
    debugContext struct {
        inEventLoop atomic.Bool
        inTick      atomic.Bool
    }
}

func (c *srtConn) AssertEventLoopContext() {
    if !c.debugContext.inEventLoop.Load() {
        panic("srtConn: function called outside EventLoop context")
    }
}

func (c *srtConn) AssertTickContext() {
    if !c.debugContext.inTick.Load() {
        panic("srtConn: function called outside Tick context")
    }
}
```

#### 5.7.5 Implementation Pattern

```go
// EventLoop primary function pattern:
func (r *receiver) periodicACK(now uint64) (bool, circular.Number, bool) {
    r.AssertEventLoopContext()  // MUST be first line
    // ... lock-free implementation ...
}

// Tick locking wrapper pattern:
func (r *receiver) periodicACKLocked(now uint64) (bool, circular.Number, bool) {
    r.AssertTickContext()  // MUST be first line
    r.lock.Lock()
    defer r.lock.Unlock()
    return r.periodicACK(now)
}

// Connection-level EventLoop function pattern:
func (c *srtConn) handleACKACK(ackNum uint32, arrivalTime time.Time) {
    c.AssertEventLoopContext()  // MUST be first line
    // ... lock-free implementation ...
}

// Connection-level Tick wrapper pattern:
func (c *srtConn) handleACKACKLocked(p packet.Packet) {
    c.AssertTickContext()  // MUST be first line
    c.ackLock.Lock()
    defer c.ackLock.Unlock()
    c.handleACKACK(p.Header().TypeSpecific, time.Now())
}
```

#### 5.7.6 Summary: Required Changes

| Package | Functions Needing `AssertEventLoopContext` | Functions Needing `AssertTickContext` |
|---------|-------------------------------------------|---------------------------------------|
| **send** | `ackBtree`, `nakBtree`, `pushBtree` | `ackLocked`, `nakLocked`, `pushLocked`, `tickDeliverPacketsBtree`, `tickDropOldPacketsBtree`, `drainRingToBtree`, `processControlRing` |
| **receive** | `periodicACK`, `periodicNakBtree`, `deliverReadyPackets`, `contiguousScan`, `drainRingByDelta`, `drainRingToBtreeEventLoop`, `expireNakEntriesEventLoop` | `periodicACKLocked`, `periodicNakBtreeLocked`, `deliverReadyPacketsLocked`, `contiguousScanLocked`, `pushLocked` |
| **connection** | `handleACKACK`, `handleKeepAlive`, `processControlPackets` | `handleACKACKLocked`, `handleKeepAliveLocked` |

**Total additions needed:**
- Sender: 3 EventLoop asserts, 7 Tick asserts
- Receiver: 7 EventLoop asserts, 5 Tick asserts
- Connection: 3 EventLoop asserts, 2 Tick asserts

---

## 6. Configuration

### 6.1 Control Ring Configuration (Consolidated)

With the introduction of `common.ControlRing[T]`, both sender and receiver control rings should use **consistent configuration**. This section consolidates and standardizes control ring configuration, removing redundancy.

#### 6.1.1 Current Sender Implementation Analysis

**Problem:** The current sender has redundant fields:

**File:** `congestion/live/send/sender.go` (lines 174-175)

```go
type sender struct {
    // ... other fields ...
    useControlRing bool              // REDUNDANT - just check controlRing != nil
    controlRing    *SendControlRing
}
```

**Usage patterns:**
- `ack.go:16`: `if s.useControlRing { s.controlRing.PushACK() }` - uses bool
- `eventloop.go:159`: `if s.controlRing != nil` - checks pointer directly

The bool `useControlRing` is set to `true` only when `controlRing` is successfully created (line 284).
This means `useControlRing == true` implies `controlRing != nil`, making the bool redundant.

**File:** `config.go` (current config options)

```go
UseSendControlRing    bool  // Enable flag
SendControlRingSize   int   // Default: 256
SendControlRingShards int   // Default: 2
```

#### 6.1.2 Proposed Simplified Design

**Design Principle:** Eliminate redundancy - use pointer nil check instead of separate bool.

**Config Level (keep as-is):**
- `Use*ControlRing` bool - explicit enable/disable (useful for CLI)
- `*ControlRingSize` int - size configuration
- `*ControlRingShards` int - shard configuration

**Struct Level (simplify):**
- **Remove:** `useControlRing bool` / `useRecvControlRing bool`
- **Keep:** `controlRing *SendControlRing` / `recvControlRing *RecvControlRing`
- **Runtime check:** `if s.controlRing != nil` (not `if s.useControlRing && s.controlRing != nil`)

**Initialization pattern:**

```go
// In sender/receiver initialization:
if config.UseSendControlRing {
    ring, err := NewSendControlRing(config.SendControlRingSize, config.SendControlRingShards)
    if err != nil {
        // Log error - will use locked path (ring stays nil)
        return
    }
    s.controlRing = ring  // Only set if creation succeeds
}
// No separate bool needed - controlRing != nil means enabled
```

**Usage pattern (simplified):**

```go
// BEFORE (redundant):
if s.useControlRing {
    if s.controlRing.PushACK(seq) { ... }
}

// AFTER (simplified):
if s.controlRing != nil {
    if s.controlRing.PushACK(seq) { ... }
}
```

#### 6.1.3 Unified Configuration

**File:** `config.go` (UPDATED)

```go
type Config struct {
    // ... existing fields ...

    // ═══════════════════════════════════════════════════════════════════════
    // Control Ring Configuration (Sender and Receiver)
    // Both use common.ControlRing[T] from congestion/live/common/
    // ═══════════════════════════════════════════════════════════════════════

    // --- Sender Control Ring (ACK/NAK routing to EventLoop) ---
    // When true AND UseEventLoop=true, creates control ring for lock-free ACK/NAK.
    // At runtime, check `sender.controlRing != nil` (no separate bool field).
    UseSendControlRing    bool  // Default: false
    SendControlRingSize   int   // Default: 128 (was 256)
    SendControlRingShards int   // Default: 1 (was 2)

    // --- Receiver Control Ring (ACKACK/KEEPALIVE routing to EventLoop) ---
    // When true AND UseEventLoop=true, creates control ring for lock-free ACKACK.
    // At runtime, check `conn.recvControlRing != nil` (no separate bool field).
    UseRecvControlRing    bool  // Default: false
    RecvControlRingSize   int   // Default: 128
    RecvControlRingShards int   // Default: 1
}

// DefaultConfig
var DefaultConfig = Config{
    // Control Ring Defaults (unified)
    UseSendControlRing:    false,
    SendControlRingSize:   128,   // CHANGED from 256
    SendControlRingShards: 1,     // CHANGED from 2

    UseRecvControlRing:    false,
    RecvControlRingSize:   128,
    RecvControlRingShards: 1,
}
```

#### 6.1.4 CLI Flags

**File:** `contrib/common/flags.go` (UPDATED)

```go
// ═══════════════════════════════════════════════════════════════════════════
// Control Ring Flags (Sender and Receiver)
// ═══════════════════════════════════════════════════════════════════════════

// Sender Control Ring
var SendControlRing = flag.Bool("sendcontrolring", false,
    "Enable sender control ring for lock-free ACK/NAK processing")
var SendControlRingSize = flag.Int("sendcontrolringsize", 128,
    "Sender control ring size (default 128)")
var SendControlRingShards = flag.Int("sendcontrolringshards", 1,
    "Sender control ring shards (default 1)")

// Receiver Control Ring (NEW)
var RecvControlRing = flag.Bool("recvcontrolring", false,
    "Enable receiver control ring for lock-free ACKACK processing")
var RecvControlRingSize = flag.Int("recvcontrolringsize", 128,
    "Receiver control ring size (default 128)")
var RecvControlRingShards = flag.Int("recvcontrolringshards", 1,
    "Receiver control ring shards (default 1)")
```

#### 6.1.5 Struct-Level Changes

**Sender (refactor existing):**

```go
// congestion/live/send/sender.go
type sender struct {
    // REMOVE: useControlRing bool  // No longer needed
    controlRing *SendControlRing    // nil means disabled, non-nil means enabled
}

// Usage:
func (s *sender) ACK(seq circular.Number) {
    if s.controlRing != nil {  // Simplified check
        if s.controlRing.PushACK(seq) {
            return
        }
        // Ring full - fall through to locked path
    }
    // Locked path...
}
```

**Receiver (new):**

```go
// connection.go
type srtConn struct {
    // SIMPLIFIED: No separate bool
    recvControlRing *receive.RecvControlRing  // nil means disabled
}

// Usage:
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    if c.recvControlRing != nil {  // Single nil check
        if c.recvControlRing.PushACKACK(ackNum, arrivalTime) {
            return
        }
        // Ring full - fall through
    }
    c.handleACKACKLocked(p)
}
```

#### 6.1.6 Configuration Summary Table

| Layer | Sender (Current) | Sender (New) | Receiver (New) |
|-------|-----------------|--------------|----------------|
| **Config** | `UseSendControlRing` | `UseSendControlRing` | `UseRecvControlRing` |
| **Struct bool** | `useControlRing` | **REMOVE** | **N/A** |
| **Struct pointer** | `controlRing` | `controlRing` | `recvControlRing` |
| **Runtime check** | `useControlRing && controlRing != nil` | `controlRing != nil` | `recvControlRing != nil` |

| Parameter | Sender (Current) | Sender (New) | Receiver (New) |
|-----------|-----------------|--------------|----------------|
| `*ControlRingSize` | 256 | **128** | 128 |
| `*ControlRingShards` | 2 | **1** | 1 |

#### 6.1.7 Required Refactoring (Sender)

To consolidate, the sender should be refactored to remove `useControlRing` bool:

**Files to update:**
- `congestion/live/send/sender.go` - Remove `useControlRing` field (line 174)
- `congestion/live/send/ack.go` - Change `if s.useControlRing` to `if s.controlRing != nil` (line 16)
- `congestion/live/send/nak.go` - Change `if s.useControlRing` to `if s.controlRing != nil` (line 21)
- `congestion/live/send/tick.go` - Change `if s.useControlRing` to `if s.controlRing != nil` (line 59)

**This refactoring is optional but recommended for consistency.**

#### 6.1.8 Breaking Changes

| Change | Impact | Migration |
|--------|--------|-----------|
| `SendControlRingSize` default 256→128 | Lower capacity | Override with `-sendcontrolringsize=256` if needed |
| `SendControlRingShards` default 2→1 | Single shard | Override with `-sendcontrolringshards=2` if needed |
| Remove `useControlRing` bool (sender) | Internal only | No external impact |

**Note:** These changes should have minimal impact as control packet rates are low (~100-1000/sec) and 128 entries is ample capacity.

### 6.2 Configuration Hierarchy

The following hierarchy shows how configuration options combine to enable different levels of lock-free operation:

```
Level 0: Baseline (Tick mode, locks)
  UseEventLoop=false
  UsePacketRing=false
  UseRecvControlRing=false  (ignored)

Level 1: Packet Ring Only
  UseEventLoop=false
  UsePacketRing=true
  UseRecvControlRing=false  (ignored)

Level 2: EventLoop with Locks
  UseEventLoop=true
  UsePacketRing=true
  UseRecvControlRing=false
  → Uses locked variants of periodicACK, periodicNAK, etc.

Level 3: EventLoop Lock-Free (NEW)
  UseEventLoop=true
  UsePacketRing=true
  UseRecvControlRing=true
  → Uses EventLoop variants (no locks)
  → COMPLETELY LOCK-FREE
```

---

## 7. Metrics

### 7.1 Sender vs Receiver Metrics Comparison

The receiver metrics must mirror the sender metrics to provide symmetric observability. This table shows the existing sender metrics and their required receiver counterparts.

#### 7.1.1 Control Ring Metrics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **Push Success** | `SendControlRingPushedACK` | `RecvControlRingPushedACKACK` | Control packets successfully pushed to ring |
| | `SendControlRingPushedNAK` | `RecvControlRingPushedKEEPALIVE` | |
| **Push Failed** | `SendControlRingDroppedACK` | `RecvControlRingDroppedACKACK` | Control packets dropped (ring full) |
| | `SendControlRingDroppedNAK` | `RecvControlRingDroppedKEEPALIVE` | |
| **Drained** | `SendControlRingDrained` | `RecvControlRingDrained` | Control packets drained by EventLoop |
| **Processed Total** | `SendControlRingProcessed` | `RecvControlRingProcessed` | Total control packets processed |
| **Processed by Type** | `SendControlRingProcessedACK` | `RecvControlRingProcessedACKACK` | Per-type processing counts |
| | `SendControlRingProcessedNAK` | `RecvControlRingProcessedKEEPALIVE` | |

#### 7.1.2 EventLoop Startup Metrics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **Start Attempts** | `SendEventLoopStartAttempts` | `RecvEventLoopStartAttempts` | Times EventLoop() was called |
| **Skipped (Disabled)** | `SendEventLoopSkippedDisabled` | `RecvEventLoopSkippedDisabled` | Times returned early (disabled) |
| **Started** | `SendEventLoopStarted` | `RecvEventLoopStarted` | Times entered main loop |

#### 7.1.3 EventLoop Iteration Metrics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **Total Iterations** | `SendEventLoopIterations` | `RecvEventLoopIterations` | Total EventLoop iterations |
| **Default Runs** | `SendEventLoopDefaultRuns` | `RecvEventLoopDefaultRuns` | Default case runs (no timer fired) |
| **Timer Fires** | `SendEventLoopDropFires` | `RecvEventLoopACKFires` | Timer-based operations |
| | | `RecvEventLoopNAKFires` | |
| | | `RecvEventLoopRateFires` | |
| **Data Drained** | `SendEventLoopDataDrained` | `RecvEventLoopDataDrained` | Data packets drained from ring |
| **Control Drained** | `SendEventLoopControlDrained` | `RecvEventLoopControlDrained` | Control packets drained from ring |
| **Control Processed** | `SendEventLoopACKsProcessed` | `RecvEventLoopACKACKsProcessed` | Per-type processing |
| | `SendEventLoopNAKsProcessed` | `RecvEventLoopKEEPALIVEsProcessed` | |
| **Idle Backoffs** | `SendEventLoopIdleBackoffs` | `RecvEventLoopIdleBackoffs` | Idle backoff events |

#### 7.1.4 EventLoop Drain Diagnostics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **Drain Attempts** | `SendEventLoopDrainAttempts` | `RecvEventLoopDrainAttempts` | Times drain was called |
| **Ring Nil** | `SendEventLoopDrainRingNil` | `RecvEventLoopDrainRingNil` | Times ring was nil |
| **Ring Empty** | `SendEventLoopDrainRingEmpty` | `RecvEventLoopDrainRingEmpty` | Times TryPop returned empty |
| **Ring Had Data** | `SendEventLoopDrainRingHadData` | `RecvEventLoopDrainRingHadData` | Times ring had data |

#### 7.1.5 EventLoop Sleep/Timing Metrics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **TSBPD Sleeps** | `SendEventLoopTsbpdSleeps` | `RecvEventLoopTsbpdSleeps` | TSBPD-aware sleep events |
| **Empty Btree Sleeps** | `SendEventLoopEmptyBtreeSleeps` | `RecvEventLoopEmptyBtreeSleeps` | Sleep due to empty btree |
| **Sleep Clamped Min** | `SendEventLoopSleepClampedMin` | `RecvEventLoopSleepClampedMin` | Sleep clamped to minimum |
| **Sleep Clamped Max** | `SendEventLoopSleepClampedMax` | `RecvEventLoopSleepClampedMax` | Sleep clamped to maximum |
| **Total Sleep Time** | `SendEventLoopSleepTotalUs` | `RecvEventLoopSleepTotalUs` | Total sleep time (µs) |
| **Next Delivery** | `SendEventLoopNextDeliveryTotalUs` | `RecvEventLoopNextDeliveryTotalUs` | Total next delivery time (µs) |

#### 7.1.6 Delivery/Processing Metrics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **Packets Delivered** | `SendDeliveryPackets` | `RecvDeliveryPackets` | Packets delivered |
| **Btree Length** | `SendBtreeLen` | `RecvBtreeLen` | Current btree length |
| **Delivery Attempts** | `SendDeliveryAttempts` | `RecvDeliveryAttempts` | Delivery function calls |
| **Btree Empty** | `SendDeliveryBtreeEmpty` | `RecvDeliveryBtreeEmpty` | Btree empty on delivery |
| **Iter Started** | `SendDeliveryIterStarted` | `RecvDeliveryIterStarted` | Iterator had packets |
| **TSBPD Not Ready** | `SendDeliveryTsbpdNotReady` | `RecvDeliveryTsbpdNotReady` | First packet not ready |

#### 7.1.7 Debug/Diagnostic Metrics

| Category | Sender Metric (Existing) | Receiver Metric (NEW) | Purpose |
|----------|--------------------------|----------------------|---------|
| **Last NowUs** | `SendDeliveryLastNowUs` | `RecvDeliveryLastNowUs` | Last nowUs value |
| **Last TSBPD** | `SendDeliveryLastTsbpd` | `RecvDeliveryLastTsbpd` | Last TSBPD time |
| **Start Seq** | `SendDeliveryStartSeq` | `RecvDeliveryStartSeq` | Last start point |
| **Btree Min Seq** | `SendDeliveryBtreeMinSeq` | `RecvDeliveryBtreeMinSeq` | Btree minimum sequence |

### 7.2 Complete Receiver Metrics Definition

**File:** `metrics/metrics.go`

```go
// ========================================================================
// Receiver Control Ring Metrics (completely_lockfree_receiver.md)
// ========================================================================
// Tracks control packet (ACKACK/KEEPALIVE) ring buffer operations.
// Symmetric with Sender Control Ring Metrics.

RecvControlRingPushedACKACK      atomic.Uint64 // ACKACK successfully pushed to control ring
RecvControlRingPushedKEEPALIVE   atomic.Uint64 // KEEPALIVE successfully pushed to control ring
RecvControlRingDroppedACKACK     atomic.Uint64 // ACKACK dropped due to control ring full
RecvControlRingDroppedKEEPALIVE  atomic.Uint64 // KEEPALIVE dropped due to control ring full
RecvControlRingDrained           atomic.Uint64 // Control packets drained by EventLoop
RecvControlRingProcessed         atomic.Uint64 // Control packets processed by EventLoop (total)
RecvControlRingProcessedACKACK   atomic.Uint64 // ACKACK processed by EventLoop
RecvControlRingProcessedKEEPALIVE atomic.Uint64 // KEEPALIVE processed by EventLoop

// ========================================================================
// Receiver EventLoop Metrics (completely_lockfree_receiver.md)
// ========================================================================
// Tracks receiver EventLoop iterations, processing, and sleep behavior.
// Symmetric with Sender EventLoop Metrics.

// Startup diagnostics (debug intermittent failures)
RecvEventLoopStartAttempts   atomic.Uint64 // Times EventLoop() was called
RecvEventLoopSkippedDisabled atomic.Uint64 // Times EventLoop returned early (useEventLoop=false)
RecvEventLoopStarted         atomic.Uint64 // Times EventLoop entered main loop

// Iteration metrics
RecvEventLoopIterations     atomic.Uint64 // Total EventLoop iterations
RecvEventLoopDefaultRuns    atomic.Uint64 // Default case runs (no timer fired)
RecvEventLoopACKFires       atomic.Uint64 // Full ACK ticker fires
RecvEventLoopNAKFires       atomic.Uint64 // NAK ticker fires
RecvEventLoopRateFires      atomic.Uint64 // Rate calculation ticker fires
RecvEventLoopDataDrained    atomic.Uint64 // Data packets drained from ring
RecvEventLoopControlDrained atomic.Uint64 // Control packets drained from ring
RecvEventLoopACKACKsProcessed   atomic.Uint64 // ACKACKs processed by EventLoop
RecvEventLoopKEEPALIVEsProcessed atomic.Uint64 // KEEPALIVEs processed by EventLoop
RecvEventLoopIdleBackoffs   atomic.Uint64 // Times EventLoop entered idle backoff

// Diagnostic metrics for drain debugging
RecvEventLoopDrainAttempts    atomic.Uint64 // Times drain was called
RecvEventLoopDrainRingNil     atomic.Uint64 // Times packetRing was nil
RecvEventLoopDrainRingEmpty   atomic.Uint64 // Times TryPop returned empty (first try)
RecvEventLoopDrainRingHadData atomic.Uint64 // Times ring.Len() > 0 before drain

// Sleep/timing metrics
RecvEventLoopTsbpdSleeps         atomic.Uint64 // Times EventLoop used TSBPD-aware sleep
RecvEventLoopEmptyBtreeSleeps    atomic.Uint64 // Times EventLoop slept due to empty btree
RecvEventLoopSleepClampedMin     atomic.Uint64 // Times sleep was clamped to minimum
RecvEventLoopSleepClampedMax     atomic.Uint64 // Times sleep was clamped to maximum
RecvEventLoopSleepTotalUs        atomic.Uint64 // Total sleep time in microseconds
RecvEventLoopNextDeliveryTotalUs atomic.Uint64 // Total next delivery time in microseconds

// Delivery metrics
RecvDeliveryPackets       atomic.Uint64 // Packets delivered by EventLoop
RecvBtreeLen              atomic.Uint64 // Current btree length (updated per iteration)
RecvDeliveryAttempts      atomic.Uint64 // Times deliverReadyPackets was called
RecvDeliveryBtreeEmpty    atomic.Uint64 // Times btree was empty when trying to deliver
RecvDeliveryIterStarted   atomic.Uint64 // Times IterateFrom called callback (had packets)
RecvDeliveryTsbpdNotReady atomic.Uint64 // Times first packet had tsbpdTime > nowUs

// Debug metrics (for troubleshooting)
RecvDeliveryLastNowUs   atomic.Uint64 // Last nowUs value (for debugging)
RecvDeliveryLastTsbpd   atomic.Uint64 // Last first packet's tsbpdTime (for debugging)
RecvDeliveryStartSeq    atomic.Uint64 // Last contiguousPoint value (for debugging)
RecvDeliveryBtreeMinSeq atomic.Uint64 // Btree min sequence (for debugging)
```

### 7.3 Prometheus Export

**File:** `metrics/handler.go`

```go
// ════════════════════════════════════════════════════════════════════════════
// Receiver Control Ring Metrics (symmetric with send_control_ring_*)
// ════════════════════════════════════════════════════════════════════════════

recv_control_ring_pushed_ackack_total{...}
recv_control_ring_pushed_keepalive_total{...}
recv_control_ring_dropped_ackack_total{...}
recv_control_ring_dropped_keepalive_total{...}
recv_control_ring_drained_total{...}
recv_control_ring_processed_total{...}
recv_control_ring_processed_ackack_total{...}
recv_control_ring_processed_keepalive_total{...}

// ════════════════════════════════════════════════════════════════════════════
// Receiver EventLoop Metrics (symmetric with send_eventloop_*)
// ════════════════════════════════════════════════════════════════════════════

// Startup
recv_eventloop_start_attempts_total{...}
recv_eventloop_skipped_disabled_total{...}
recv_eventloop_started_total{...}

// Iterations
recv_eventloop_iterations_total{...}
recv_eventloop_default_runs_total{...}
recv_eventloop_ack_fires_total{...}
recv_eventloop_nak_fires_total{...}
recv_eventloop_rate_fires_total{...}
recv_eventloop_data_drained_total{...}
recv_eventloop_control_drained_total{...}
recv_eventloop_ackacks_processed_total{...}
recv_eventloop_keepalives_processed_total{...}
recv_eventloop_idle_backoffs_total{...}

// Drain diagnostics
recv_eventloop_drain_attempts_total{...}
recv_eventloop_drain_ring_nil_total{...}
recv_eventloop_drain_ring_empty_total{...}
recv_eventloop_drain_ring_had_data_total{...}

// Sleep/timing
recv_eventloop_tsbpd_sleeps_total{...}
recv_eventloop_empty_btree_sleeps_total{...}
recv_eventloop_sleep_clamped_min_total{...}
recv_eventloop_sleep_clamped_max_total{...}
recv_eventloop_sleep_total_us{...}
recv_eventloop_next_delivery_total_us{...}

// Delivery
recv_delivery_packets_total{...}
recv_btree_len{...}  // Gauge
recv_delivery_attempts_total{...}
recv_delivery_btree_empty_total{...}
recv_delivery_iter_started_total{...}
recv_delivery_tsbpd_not_ready_total{...}

// Debug (optionally exposed)
recv_delivery_last_now_us{...}
recv_delivery_last_tsbpd{...}
recv_delivery_start_seq{...}
recv_delivery_btree_min_seq{...}
```

### 7.4 Metrics Naming Convention

| Aspect | Sender Pattern | Receiver Pattern | Example |
|--------|----------------|------------------|---------|
| **Prefix** | `Send*` / `send_*` | `Recv*` / `recv_*` | `SendControlRing*` → `RecvControlRing*` |
| **Ring** | `SendControlRing*` | `RecvControlRing*` | Push/Drop/Drain/Processed |
| **EventLoop** | `SendEventLoop*` | `RecvEventLoop*` | Iterations/Fires/Drained |
| **Delivery** | `SendDelivery*` | `RecvDelivery*` | Packets/Attempts/Empty |
| **Control Types** | ACK, NAK | ACKACK, KEEPALIVE | Different packet types |

### 7.5 Verification Checklist

To ensure symmetric metrics coverage:

- [ ] Every `SendControlRing*` has a corresponding `RecvControlRing*`
- [ ] Every `SendEventLoop*` has a corresponding `RecvEventLoop*`
- [ ] Every `SendDelivery*` has a corresponding `RecvDelivery*`
- [ ] Prometheus metrics follow same naming pattern with `recv_` prefix
- [ ] Integration tests verify metrics increment correctly (./metrics/handler.go, ./metrics/hanlder_test.go)
- [ ] `make code-audit-metrics` confirms single increments of counters, and all metrics exported to prometheus

---

## 8. Testing Strategy

This section provides a detailed testing strategy focused on **what can go wrong** with the lock-free receiver implementation, using table-driven tests to systematically cover failure modes.

### 8.1 Risk Analysis: What Can Go Wrong

| Category | Risk | Impact | Likelihood | Mitigation |
|----------|------|--------|------------|------------|
| **Race Conditions** | EventLoop function called from io_uring | Data corruption | Medium | Context asserts |
| **Race Conditions** | Locking wrapper doesn't acquire lock | Data race on btree | Low | AssertTickContext |
| **Race Conditions** | Concurrent access to ackNumbers | RTT corruption | Medium | Single consumer via ring |
| **Ring Buffer** | Ring full, ACKACK dropped | RTT not updated | Medium | Fallback to locked path |
| **Ring Buffer** | Timestamp precision loss | RTT accuracy degraded | Low | Use int64 nanoseconds |
| **Ring Buffer** | Wrong packet type in ring | Wrong handler called | Low | Type switch validation |
| **Function Dispatch** | Nil function pointer | Panic | Low | Init-time validation |
| **Function Dispatch** | Wrong function assigned | Lock contention or race | Medium | Table-driven init tests |
| **RTT Calculation** | Arrival time not captured | RTT = 0 or huge | Medium | Validate before push |
| **RTT Calculation** | ackNum not found in btree | RTT not updated | Medium | Metrics for lookup failures |
| **Control Flow** | ACKACK lost in ring | NAK interval wrong | Low | Ring pushed/processed metrics |
| **Metrics** | Count mismatch | Hard to debug | Medium | Invariant checks |

### 8.2 Table-Driven Tests: Control Ring

**File:** `congestion/live/common/control_ring_test.go`

```go
func TestControlRing_PushPop(t *testing.T) {
    tests := []struct {
        name        string
        ringSize    int
        ringShards  int
        pushCount   int
        expectPops  int
        expectDrops int
    }{
        {
            name:        "basic_push_pop",
            ringSize:    128,
            ringShards:  1,
            pushCount:   10,
            expectPops:  10,
            expectDrops: 0,
        },
        {
            name:        "ring_full_drops",
            ringSize:    8,
            ringShards:  1,
            pushCount:   20,
            expectPops:  8,  // Ring capacity
            expectDrops: 12,
        },
        {
            name:        "empty_ring_tryPop",
            ringSize:    128,
            ringShards:  1,
            pushCount:   0,
            expectPops:  0,
            expectDrops: 0,
        },
        {
            name:        "single_item",
            ringSize:    128,
            ringShards:  1,
            pushCount:   1,
            expectPops:  1,
            expectDrops: 0,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            ring, err := common.NewControlRing[TestPacket](tt.ringSize, tt.ringShards)
            require.NoError(t, err)

            pushed, dropped := 0, 0
            for i := 0; i < tt.pushCount; i++ {
                if ring.Push(0, TestPacket{Seq: uint32(i)}) {
                    pushed++
                } else {
                    dropped++
                }
            }

            popped := 0
            for {
                _, ok := ring.TryPop()
                if !ok {
                    break
                }
                popped++
            }

            assert.Equal(t, tt.expectPops, popped, "popped count")
            assert.Equal(t, tt.expectDrops, dropped, "dropped count")
        })
    }
}
```

### 8.3 Table-Driven Tests: RecvControlRing Specific

**File:** `congestion/live/receive/control_ring_test.go`

```go
func TestRecvControlRing_PacketTypes(t *testing.T) {
    tests := []struct {
        name         string
        pushFunc     func(r *RecvControlRing) bool
        expectType   RecvControlPacketType
        expectHasACK bool
        expectHasTS  bool
    }{
        {
            name: "ackack_with_timestamp",
            pushFunc: func(r *RecvControlRing) bool {
                return r.PushACKACK(42, time.Now())
            },
            expectType:   RecvControlTypeACKACK,
            expectHasACK: true,
            expectHasTS:  true,
        },
        {
            name: "keepalive_no_data",
            pushFunc: func(r *RecvControlRing) bool {
                return r.PushKEEPALIVE()
            },
            expectType:   RecvControlTypeKEEPALIVE,
            expectHasACK: false,
            expectHasTS:  false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            ring, _ := NewRecvControlRing(128, 1)

            ok := tt.pushFunc(ring)
            require.True(t, ok, "push should succeed")

            cp, ok := ring.TryPop()
            require.True(t, ok, "pop should succeed")

            assert.Equal(t, tt.expectType, cp.Type)
            if tt.expectHasACK {
                assert.NotZero(t, cp.ACKNumber)
            }
            if tt.expectHasTS {
                assert.NotZero(t, cp.Timestamp)
            }
        })
    }
}

func TestRecvControlRing_TimestampAccuracy(t *testing.T) {
    tests := []struct {
        name          string
        timeOffset    time.Duration
        maxDriftNanos int64
    }{
        {
            name:          "immediate",
            timeOffset:    0,
            maxDriftNanos: 1_000_000, // 1ms tolerance
        },
        {
            name:          "1ms_ago",
            timeOffset:    -1 * time.Millisecond,
            maxDriftNanos: 1_000_000,
        },
        {
            name:          "100us_ago",
            timeOffset:    -100 * time.Microsecond,
            maxDriftNanos: 500_000, // 0.5ms tolerance
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            ring, _ := NewRecvControlRing(128, 1)

            arrivalTime := time.Now().Add(tt.timeOffset)
            ring.PushACKACK(1, arrivalTime)

            cp, ok := ring.TryPop()
            require.True(t, ok)

            recoveredTime := time.Unix(0, cp.Timestamp)
            drift := arrivalTime.Sub(recoveredTime).Abs()

            assert.Less(t, drift.Nanoseconds(), tt.maxDriftNanos,
                "timestamp drift %v exceeds tolerance", drift)
        })
    }
}
```

### 8.4 Table-Driven Tests: Function Dispatch

**File:** `congestion/live/receive/dispatch_test.go`

```go
func TestFunctionDispatch_Initialization(t *testing.T) {
    tests := []struct {
        name               string
        useEventLoop       bool
        useRecvControlRing bool
        expectPeriodicACK  string // "locked" or "eventloop"
        expectPeriodicNAK  string
        expectHandleACKACK string
    }{
        {
            name:               "tick_mode_no_ring",
            useEventLoop:       false,
            useRecvControlRing: false,
            expectPeriodicACK:  "locked",
            expectPeriodicNAK:  "locked",
            expectHandleACKACK: "locked",
        },
        {
            name:               "eventloop_mode_no_ring",
            useEventLoop:       true,
            useRecvControlRing: false,
            expectPeriodicACK:  "eventloop",
            expectPeriodicNAK:  "eventloop",
            expectHandleACKACK: "locked", // Still locked without ring
        },
        {
            name:               "eventloop_mode_with_ring",
            useEventLoop:       true,
            useRecvControlRing: true,
            expectPeriodicACK:  "eventloop",
            expectPeriodicNAK:  "eventloop",
            expectHandleACKACK: "eventloop", // Lock-free via ring
        },
        {
            name:               "tick_mode_with_ring_ignored",
            useEventLoop:       false,
            useRecvControlRing: true, // Ring ignored in tick mode
            expectPeriodicACK:  "locked",
            expectPeriodicNAK:  "locked",
            expectHandleACKACK: "locked",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            config := &Config{
                UseEventLoop:       tt.useEventLoop,
                UseRecvControlRing: tt.useRecvControlRing,
            }

            r := newReceiver(config, mockCallbacks)

            // Verify function pointers are set correctly
            // Use reflection or exported test helpers
            verifyDispatch(t, r, tt.expectPeriodicACK, tt.expectPeriodicNAK)
        })
    }
}
```

### 8.5 Table-Driven Tests: ACKACK Processing

**File:** `connection_ackack_test.go`

```go
func TestHandleACKACK_RTTCalculation(t *testing.T) {
    tests := []struct {
        name           string
        ackNum         uint32
        sentTime       time.Time
        arrivalTime    time.Time
        expectedRTTMin time.Duration
        expectedRTTMax time.Duration
        expectFound    bool
    }{
        {
            name:           "normal_rtt_1ms",
            ackNum:         100,
            sentTime:       time.Now().Add(-1 * time.Millisecond),
            arrivalTime:    time.Now(),
            expectedRTTMin: 900 * time.Microsecond,
            expectedRTTMax: 1100 * time.Microsecond,
            expectFound:    true,
        },
        {
            name:           "normal_rtt_10ms",
            ackNum:         101,
            sentTime:       time.Now().Add(-10 * time.Millisecond),
            arrivalTime:    time.Now(),
            expectedRTTMin: 9 * time.Millisecond,
            expectedRTTMax: 11 * time.Millisecond,
            expectFound:    true,
        },
        {
            name:           "unknown_acknum",
            ackNum:         999, // Not in btree
            sentTime:       time.Now(),
            arrivalTime:    time.Now(),
            expectedRTTMin: 0,
            expectedRTTMax: 0,
            expectFound:    false,
        },
        {
            name:           "zero_rtt_same_time",
            ackNum:         102,
            sentTime:       time.Now(),
            arrivalTime:    time.Now(), // Same as sent
            expectedRTTMin: 0,
            expectedRTTMax: 100 * time.Microsecond,
            expectFound:    true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            conn := newMockConnection()

            // Setup: Insert ack entry with sentTime
            if tt.expectFound {
                entry := GetAckEntry()
                entry.ackNum = tt.ackNum
                entry.timestamp = tt.sentTime
                conn.ackNumbers.Insert(entry)
            }

            // Exercise: Call handleACKACK
            conn.handleACKACK(tt.ackNum, tt.arrivalTime)

            // Verify RTT
            if tt.expectFound {
                rtt := conn.rtt.Get()
                assert.GreaterOrEqual(t, rtt, tt.expectedRTTMin)
                assert.LessOrEqual(t, rtt, tt.expectedRTTMax)
            }

            // Verify entry removed
            entry := conn.ackNumbers.Get(tt.ackNum)
            assert.Nil(t, entry, "entry should be deleted after processing")
        })
    }
}
```

### 8.6 Table-Driven Tests: Ring Full Fallback

**File:** `congestion/live/receive/ring_fallback_test.go`

```go
func TestControlRing_FallbackBehavior(t *testing.T) {
    tests := []struct {
        name            string
        ringSize        int
        pushCount       int
        expectPushed    int
        expectFallback  int
        expectProcessed int
    }{
        {
            name:            "no_fallback_plenty_space",
            ringSize:        128,
            pushCount:       10,
            expectPushed:    10,
            expectFallback:  0,
            expectProcessed: 10,
        },
        {
            name:            "all_fallback_ring_full",
            ringSize:        4,
            pushCount:       20,
            expectPushed:    4,
            expectFallback:  16,
            expectProcessed: 20, // All processed (via ring + fallback)
        },
        {
            name:            "partial_fallback",
            ringSize:        8,
            pushCount:       12,
            expectPushed:    8,
            expectFallback:  4,
            expectProcessed: 12,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            metrics := newTestMetrics()
            ring, _ := NewRecvControlRing(tt.ringSize, 1)
            conn := newMockConnectionWithRing(ring, metrics)

            for i := 0; i < tt.pushCount; i++ {
                // Simulate ACKACK arrival
                conn.onACKACKArrival(uint32(i), time.Now())
            }

            // Drain ring via EventLoop
            conn.processControlPackets()

            pushed := metrics.RecvControlRingPushedACKACK.Load()
            dropped := metrics.RecvControlRingDroppedACKACK.Load()
            processed := metrics.RecvControlRingProcessedACKACK.Load()

            assert.Equal(t, uint64(tt.expectPushed), pushed)
            assert.Equal(t, uint64(tt.expectFallback), dropped)
            // Note: dropped packets use fallback locked path
            assert.Equal(t, uint64(tt.expectProcessed), processed+dropped)
        })
    }
}
```

### 8.7 Context Assert Tests

**File:** `congestion/live/receive/context_assert_test.go`

```go
func TestContextAsserts_PanicOnWrongContext(t *testing.T) {
    tests := []struct {
        name        string
        setupFunc   func(r *receiver)
        callFunc    func(r *receiver)
        expectPanic bool
        panicMsg    string
    }{
        {
            name: "eventloop_func_from_tick_panics",
            setupFunc: func(r *receiver) {
                r.EnterTick()
            },
            callFunc: func(r *receiver) {
                r.periodicACK(0) // EventLoop function
            },
            expectPanic: true,
            panicMsg:    "EventLoop context",
        },
        {
            name: "tick_func_from_eventloop_panics",
            setupFunc: func(r *receiver) {
                r.EnterEventLoop()
            },
            callFunc: func(r *receiver) {
                r.periodicACKLocked(0) // Tick function
            },
            expectPanic: true,
            panicMsg:    "Tick context",
        },
        {
            name: "eventloop_func_from_eventloop_ok",
            setupFunc: func(r *receiver) {
                r.EnterEventLoop()
            },
            callFunc: func(r *receiver) {
                r.periodicACK(0)
            },
            expectPanic: false,
        },
        {
            name: "tick_func_from_tick_ok",
            setupFunc: func(r *receiver) {
                r.EnterTick()
            },
            callFunc: func(r *receiver) {
                r.periodicACKLocked(0)
            },
            expectPanic: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            r := newTestReceiver()
            tt.setupFunc(r)

            if tt.expectPanic {
                assert.PanicsWithValue(t, tt.panicMsg, func() {
                    tt.callFunc(r)
                })
            } else {
                assert.NotPanics(t, func() {
                    tt.callFunc(r)
                })
            }
        })
    }
}
```

### 8.8 Metrics Invariant Tests

**File:** `congestion/live/receive/metrics_invariant_test.go`

```go
func TestMetricsInvariants(t *testing.T) {
    tests := []struct {
        name      string
        scenario  func(conn *srtConn, count int)
        count     int
        invariant func(m *metrics.Metrics) bool
        message   string
    }{
        {
            name: "pushed_equals_processed_plus_dropped",
            scenario: func(conn *srtConn, count int) {
                for i := 0; i < count; i++ {
                    conn.onACKACKArrival(uint32(i), time.Now())
                }
                conn.processControlPackets()
            },
            count: 100,
            invariant: func(m *metrics.Metrics) bool {
                pushed := m.RecvControlRingPushedACKACK.Load()
                processed := m.RecvControlRingProcessedACKACK.Load()
                dropped := m.RecvControlRingDroppedACKACK.Load()
                return pushed == processed || pushed+dropped >= processed
            },
            message: "pushed should equal processed + dropped",
        },
        {
            name: "eventloop_iterations_positive",
            scenario: func(conn *srtConn, count int) {
                ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
                defer cancel()
                go conn.recv.EventLoop(ctx)
                time.Sleep(50 * time.Millisecond)
            },
            count: 1,
            invariant: func(m *metrics.Metrics) bool {
                return m.RecvEventLoopIterations.Load() > 0
            },
            message: "EventLoop should have iterations",
        },
        {
            name: "drain_attempts_match_ring_checks",
            scenario: func(conn *srtConn, count int) {
                for i := 0; i < count; i++ {
                    conn.recv.drainRingByDelta()
                }
            },
            count: 50,
            invariant: func(m *metrics.Metrics) bool {
                attempts := m.RecvEventLoopDrainAttempts.Load()
                nilRing := m.RecvEventLoopDrainRingNil.Load()
                empty := m.RecvEventLoopDrainRingEmpty.Load()
                hadData := m.RecvEventLoopDrainRingHadData.Load()
                return attempts == nilRing+empty+hadData
            },
            message: "drain attempts should equal nil + empty + hadData",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            m := metrics.NewMetrics()
            conn := newMockConnectionWithMetrics(m)

            tt.scenario(conn, tt.count)

            assert.True(t, tt.invariant(m), tt.message)
        })
    }
}
```

### 8.9 Concurrency Tests

**File:** `congestion/live/receive/concurrency_test.go`

```go
func TestConcurrentPushPop(t *testing.T) {
    tests := []struct {
        name       string
        producers  int
        pushEach   int
        ringSize   int
    }{
        {"single_producer", 1, 1000, 128},
        {"two_producers", 2, 500, 128},
        {"many_producers", 10, 100, 128},
        {"stress_small_ring", 5, 200, 16},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            ring, _ := NewRecvControlRing(tt.ringSize, 1)

            var wg sync.WaitGroup
            var pushed, popped atomic.Uint64

            // Producers
            for p := 0; p < tt.producers; p++ {
                wg.Add(1)
                go func(id int) {
                    defer wg.Done()
                    for i := 0; i < tt.pushEach; i++ {
                        if ring.PushACKACK(uint32(id*1000+i), time.Now()) {
                            pushed.Add(1)
                        }
                    }
                }(p)
            }

            // Single consumer (EventLoop pattern)
            done := make(chan struct{})
            go func() {
                for {
                    select {
                    case <-done:
                        return
                    default:
                        if _, ok := ring.TryPop(); ok {
                            popped.Add(1)
                        } else {
                            time.Sleep(10 * time.Microsecond)
                        }
                    }
                }
            }()

            wg.Wait()
            time.Sleep(10 * time.Millisecond) // Drain remaining
            close(done)

            // Final drain
            for {
                if _, ok := ring.TryPop(); !ok {
                    break
                }
                popped.Add(1)
            }

            assert.Equal(t, pushed.Load(), popped.Load(),
                "all pushed items should be popped")
        })
    }
}
```

### 8.10 Benchmarks

#### 8.10.1 Existing Benchmark Coverage

The following existing benchmarks cover code being modified:

**File: `congestion/live/receive/hotpath_bench_test.go`**

| Benchmark | Function Covered | Status |
|-----------|-----------------|--------|
| `BenchmarkHotPath_ContiguousScan` | `contiguousScan()` | ✅ Exists |
| `BenchmarkHotPath_ContiguousScanWithGaps` | `contiguousScan()` with gaps | ✅ Exists |
| `BenchmarkHotPath_DeliverReadyPackets` | `deliverReadyPackets()` | ✅ Exists |
| `BenchmarkHotPath_DrainRingByDelta` | `drainRingByDelta()` | ✅ Exists |
| `BenchmarkHotPath_EventLoopIteration` | `EventLoop` single iteration | ✅ Exists |
| `BenchmarkHotPath_EventLoopFull` | `EventLoop` full cycle | ✅ Exists |
| `BenchmarkHotPath_GapScan` | `gapScan()` | ✅ Exists |
| `BenchmarkHotPath_NakBtreeOperations` | NAK btree operations | ✅ Exists |

**File: `congestion/live/receive/receive_bench_test.go`**

| Benchmark | Function Covered | Status |
|-----------|-----------------|--------|
| `BenchmarkTick` | `Tick()` path | ✅ Exists |
| `BenchmarkFullPipeline` | End-to-end | ✅ Exists |
| `BenchmarkConfigComparison` | Different configs | ✅ Exists |

**File: `congestion/live/send/control_ring_test.go` (Pattern Reference)**

| Benchmark | Pattern For | Status |
|-----------|-------------|--------|
| `BenchmarkSendControlRing_PushACK` | `RecvControlRing.PushACKACK` | ✅ Pattern exists |
| `BenchmarkSendControlRing_PushNAK_Small` | `RecvControlRing.PushKEEPALIVE` | ✅ Pattern exists |
| `BenchmarkSendControlRing_PushACK_Concurrent` | Concurrent push | ✅ Pattern exists |
| `BenchmarkSendControlRing_TryPop_SingleConsumer` | EventLoop drain | ✅ Pattern exists |

#### 8.10.2 New Benchmarks Required

The following benchmarks are **missing** and must be added:

**File: `congestion/live/common/control_ring_bench_test.go` (NEW)**

```go
// Generic control ring benchmarks (shared infrastructure)

func BenchmarkControlRing_Push(b *testing.B) {
    sizes := []int{128, 1024, 8192}
    for _, size := range sizes {
        b.Run(fmt.Sprintf("Size%d", size), func(b *testing.B) {
            ring, _ := common.NewControlRing[TestPacket](size, 1)
            pkt := TestPacket{Seq: 100}

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                if !ring.Push(0, pkt) {
                    // Drain if full
                    for { if _, ok := ring.TryPop(); !ok { break } }
                }
            }
        })
    }
}

func BenchmarkControlRing_TryPop(b *testing.B) {
    ring, _ := common.NewControlRing[TestPacket](8192, 1)
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
    ring, _ := common.NewControlRing[TestPacket](128, 1)
    pkt := TestPacket{Seq: 100}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        ring.Push(0, pkt)
        ring.TryPop()
    }
}
```

**File: `congestion/live/receive/control_ring_bench_test.go` (NEW)**

```go
// RecvControlRing-specific benchmarks (mirror send/control_ring_test.go)

func BenchmarkRecvControlRing_PushACKACK(b *testing.B) {
    ring, _ := NewRecvControlRing(8192, 1)
    now := time.Now()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        if !ring.PushACKACK(uint32(i), now) {
            // Ring full, drain
            for { if _, ok := ring.TryPop(); !ok { break } }
            ring.PushACKACK(uint32(i), now)
        }
    }
}

func BenchmarkRecvControlRing_PushKEEPALIVE(b *testing.B) {
    ring, _ := NewRecvControlRing(8192, 1)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        if !ring.PushKEEPALIVE() {
            for { if _, ok := ring.TryPop(); !ok { break } }
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

**File: `congestion/live/receive/periodic_bench_test.go` (NEW)**

```go
// periodicACK and periodicNAK benchmarks - compare Locked vs EventLoop

func BenchmarkPeriodicACK_Comparison(b *testing.B) {
    modes := []struct {
        name string
        fn   func(r *receiver, now uint64)
    }{
        {"Locked", func(r *receiver, now uint64) {
            r.EnterTick()
            defer r.ExitTick()
            r.periodicACKLocked(now)
        }},
        {"EventLoop", func(r *receiver, now uint64) {
            r.EnterEventLoop()
            defer r.ExitEventLoop()
            r.periodicACK(now)
        }},
    }

    for _, m := range modes {
        b.Run(m.name, func(b *testing.B) {
            recv := createBenchReceiver(b, 1000)
            now := uint64(time.Now().UnixMicro())

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                m.fn(recv, now)
            }
        })
    }
}

func BenchmarkPeriodicNAK_Comparison(b *testing.B) {
    modes := []struct {
        name string
        fn   func(r *receiver, now uint64)
    }{
        {"Locked", func(r *receiver, now uint64) {
            r.EnterTick()
            defer r.ExitTick()
            r.periodicNakBtreeLocked(now)
        }},
        {"EventLoop", func(r *receiver, now uint64) {
            r.EnterEventLoop()
            defer r.ExitEventLoop()
            r.periodicNakBtree(now)
        }},
    }

    for _, m := range modes {
        b.Run(m.name, func(b *testing.B) {
            recv := createBenchReceiverWithGaps(b, 1000, 50) // 5% gaps
            now := uint64(time.Now().UnixMicro())

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                m.fn(recv, now)
            }
        })
    }
}
```

**File: `connection_ackack_bench_test.go` (NEW)**

```go
// ACKACK processing benchmarks

func BenchmarkHandleACKACK_Comparison(b *testing.B) {
    modes := []struct {
        name string
        fn   func(c *srtConn, ackNum uint32, t time.Time)
    }{
        {"Locked", func(c *srtConn, ackNum uint32, t time.Time) {
            c.handleACKACKLocked(mockPacket(ackNum))
        }},
        {"EventLoop", func(c *srtConn, ackNum uint32, t time.Time) {
            c.handleACKACK(ackNum, t)
        }},
    }

    for _, m := range modes {
        b.Run(m.name, func(b *testing.B) {
            conn := newBenchConnection()
            now := time.Now()

            // Pre-populate ackNumbers btree
            for i := uint32(0); i < 1000; i++ {
                entry := GetAckEntry()
                entry.ackNum = i
                entry.timestamp = now.Add(-time.Millisecond)
                conn.ackNumbers.Insert(entry)
            }

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                ackNum := uint32(i % 1000)
                m.fn(conn, ackNum, now)

                // Re-insert for next iteration
                entry := GetAckEntry()
                entry.ackNum = ackNum
                entry.timestamp = now.Add(-time.Millisecond)
                conn.ackNumbers.Insert(entry)
            }
        })
    }
}

func BenchmarkProcessControlPackets(b *testing.B) {
    batchSizes := []int{1, 10, 50, 100}

    for _, batch := range batchSizes {
        b.Run(fmt.Sprintf("Batch%d", batch), func(b *testing.B) {
            conn := newBenchConnectionWithRing()
            now := time.Now()

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                // Push batch
                for j := 0; j < batch; j++ {
                    conn.controlRing.PushACKACK(uint32(j), now)
                }

                // Drain
                conn.processControlPackets()
            }
        })
    }
}
```

#### 8.10.3 Benchmark Coverage Summary

| Component | Existing | New Required | Files |
|-----------|----------|--------------|-------|
| `common.ControlRing[T]` | ❌ | ✅ Push, TryPop, Balanced | `common/control_ring_bench_test.go` |
| `RecvControlRing` | ❌ | ✅ PushACKACK, PushKEEPALIVE, Concurrent, TryPop | `receive/control_ring_bench_test.go` |
| `periodicACK` | ❌ | ✅ Locked vs EventLoop comparison | `receive/periodic_bench_test.go` |
| `periodicNAK` | ❌ | ✅ Locked vs EventLoop comparison | `receive/periodic_bench_test.go` |
| `handleACKACK` | ❌ | ✅ Locked vs EventLoop comparison | `connection_ackack_bench_test.go` |
| `processControlPackets` | ❌ | ✅ Batch sizes | `connection_ackack_bench_test.go` |
| `contiguousScan` | ✅ | — | `receive/hotpath_bench_test.go` |
| `deliverReadyPackets` | ✅ | — | `receive/hotpath_bench_test.go` |
| `drainRingByDelta` | ✅ | — | `receive/hotpath_bench_test.go` |
| `EventLoop` | ✅ | — | `receive/hotpath_bench_test.go` |
| `Tick` | ✅ | — | `receive/receive_bench_test.go` |

#### 8.10.4 Performance Targets

Based on sender benchmarks, establish these targets for receiver:

| Benchmark | Target | Rationale |
|-----------|--------|-----------|
| `RecvControlRing_PushACKACK` | < 100 ns/op | Same as `SendControlRing_PushACK` |
| `RecvControlRing_TryPop` | < 50 ns/op | Same as sender |
| `periodicACK (EventLoop)` | ≤ 1.0x `periodicACKLocked` | No regression |
| `handleACKACK (EventLoop)` | ≤ 0.9x `handleACKACKLocked` | Remove lock overhead |
| `processControlPackets` | < 1 µs per packet | Batch amortizes overhead |

#### 8.10.5 Running Benchmarks

```bash
# Run all receiver benchmarks
go test -bench=. -benchmem ./congestion/live/receive/...

# Run control ring benchmarks specifically
go test -bench=BenchmarkRecvControlRing -benchmem ./congestion/live/receive/

# Compare Locked vs EventLoop (use benchstat)
go test -bench=BenchmarkPeriodicACK_Comparison -count=10 ./congestion/live/receive/ > bench.txt
benchstat bench.txt

# Run with race detector (slower but catches races)
go test -bench=BenchmarkRecvControlRing_Concurrent -race ./congestion/live/receive/

# Profile CPU
go test -bench=BenchmarkHotPath_EventLoopFull -cpuprofile=cpu.prof ./congestion/live/receive/
go tool pprof cpu.prof
```
```

### 8.11 Integration Tests

**File:** `contrib/integration_testing/lockfree_receiver_test.go`

```go
func TestIntegration_LockFreeReceiver(t *testing.T) {
    tests := []struct {
        name           string
        serverConfig   Config
        clientConfig   Config
        duration       time.Duration
        expectRTTValid bool
        expectNoDrops  bool
    }{
        {
            name: "both_eventloop_lockfree",
            serverConfig: Config{
                UseEventLoop:       true,
                UseRecvControlRing: true,
            },
            clientConfig: Config{
                UseEventLoop:       true,
                UseControlRing:     true,
            },
            duration:       5 * time.Second,
            expectRTTValid: true,
            expectNoDrops:  true,
        },
        {
            name: "server_lockfree_client_tick",
            serverConfig: Config{
                UseEventLoop:       true,
                UseRecvControlRing: true,
            },
            clientConfig: Config{
                UseEventLoop: false,
            },
            duration:       5 * time.Second,
            expectRTTValid: true,
            expectNoDrops:  true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            server := startServer(tt.serverConfig)
            client := startClient(tt.clientConfig, server.Addr())

            // Send data for duration
            sendTestData(client, tt.duration)

            // Verify RTT was calculated
            if tt.expectRTTValid {
                rtt := server.RTT()
                assert.Greater(t, rtt, time.Duration(0))
                assert.Less(t, rtt, 100*time.Millisecond)
            }

            // Verify no unexpected drops
            if tt.expectNoDrops {
                metrics := server.Metrics()
                assert.Zero(t, metrics.RecvControlRingDroppedACKACK.Load())
            }
        })
    }
}
```

### 8.12 Test Summary by Component

| Component | Test File | Key Tests | Coverage Target |
|-----------|-----------|-----------|-----------------|
| `common.ControlRing[T]` | `control_ring_test.go` | Push/Pop, Full, Empty, Concurrent | 100% |
| `RecvControlRing` | `recv_control_ring_test.go` | PacketTypes, Timestamp, Fallback | 100% |
| Function Dispatch | `dispatch_test.go` | Init combinations, Nil checks | 100% |
| `handleACKACK` | `connection_ackack_test.go` | RTT calculation, Not found | 95% |
| Context Asserts | `context_assert_test.go` | Panic on wrong context | 100% |
| Metrics | `metrics_invariant_test.go` | Invariants hold | 90% |
| Concurrency | `concurrency_test.go` | Race detection | 100% |
| Integration | `lockfree_receiver_test.go` | End-to-end | 80% |

---

## 9. Integration Test Updates

This section describes the required updates to integration tests (isolation and parallel tests) to cover the completely lock-free receiver.

### 9.1 Current Test Landscape

#### 9.1.1 Existing EventLoop Tests (from `integration_testing_matrix_design.md`)

| Test Name | Description | Status |
|-----------|-------------|--------|
| `Isolation-5M-Server-Ring` | Ring buffer only | ✅ Exists |
| `Isolation-5M-Server-Ring-IoUr` | Ring + io_uring recv | ✅ Exists |
| `Isolation-5M-EventLoop` | EventLoop default | ✅ Exists |
| `Isolation-5M-FullEventLoop` | Full Phase 4 | ✅ Exists |
| `Isolation-20M-FullEventLoop` | Phase 4 at 20 Mb/s | ✅ Exists |
| `Isolation-50M-FullEventLoop` | Phase 4 at 50 Mb/s | ✅ Exists |
| `Isolation-100M-FullEventLoop` | Phase 4 at 100 Mb/s | ✅ Exists |

#### 9.1.2 Existing Parallel Tests

| Test Name | Comparison | Status |
|-----------|------------|--------|
| `Parallel-Clean-20M-Base-vs-Full` | Baseline vs Full stack | ✅ Exists |
| `Parallel-Clean-20M-EventLoop-vs-Tick` | EventLoop vs Tick mode | ✅ Exists |
| `Parallel-Starlink-20M-Base-vs-NakBtree` | NAK btree comparison | ✅ Exists |

### 9.2 New Configuration: `FullELLockFree`

**Config Abbreviation:** `FullELLF` (Full EventLoop Lock-Free)

**Features Enabled:**
```go
ConfigFullELLockFree = SRTConfig{
    // Packet Store
    UseBtree: true,

    // io_uring
    IOUringEnabled:     true,
    IOUringRecvEnabled: true,

    // NAK Btree
    UseNakBtree:       true,
    FastNakEnabled:    true,
    FastNakRecentEnabled: true,
    HonorNakOrder:     true,

    // Sender Lock-Free
    UsePacketRing:       true,
    UseSendControlRing:  true,   // Lock-free ACK/NAK
    SendControlRingSize: 128,

    // Receiver Lock-Free (NEW)
    UseEventLoop:        true,
    UseRecvControlRing:  true,   // Lock-free ACKACK (NEW)
    RecvControlRingSize: 128,
}
```

**CLI Flags:**
```bash
-btree -iouringenabled -iouringrecvenabled \
-nakbtree -fastnaksenabled -fastnakrecentenabled -honornakorder \
-packetring -sendcontrolring -sendcontrolringsize=128 \
-eventloop -recvcontrolring -recvcontrolringsize=128
```

### 9.3 New Isolation Tests

**File:** `contrib/integration_testing/test_configs.go`

| Test Name | Description | Variable Isolated |
|-----------|-------------|-------------------|
| `Isolation-5M-RecvControlRing` | Receiver control ring only | `UseRecvControlRing=true` |
| `Isolation-5M-FullELLockFree` | Completely lock-free (sender + receiver) | Full config |
| `Isolation-20M-FullELLockFree` | Lock-free at 20 Mb/s | Throughput |
| `Isolation-50M-FullELLockFree` | Lock-free at 50 Mb/s | High throughput |
| `Isolation-5M-RecvControlRing-NoSendRing` | Recv ring only (sender uses locks) | Asymmetric |
| `Isolation-5M-SendControlRing-NoRecvRing` | Send ring only (receiver uses locks) | Asymmetric |

**Test Configuration Details:**

```go
// Test: Isolation-5M-RecvControlRing
// Purpose: Isolate receiver control ring from other features
{
    Name:        "Isolation-5M-RecvControlRing",
    Description: "Receiver control ring only (ACKACK lock-free)",
    Config: SRTConfig{
        // Baseline features
        UseBtree:          true,
        IOUringEnabled:    true,
        IOUringRecvEnabled: true,
        UseNakBtree:       true,

        // EventLoop (no sender ring)
        UseEventLoop:      true,
        UsePacketRing:     true,

        // NEW: Receiver control ring
        UseRecvControlRing:  true,
        RecvControlRingSize: 128,

        // Sender uses locked path
        UseSendControlRing: false,
    },
    Duration: 30 * time.Second,
    Bitrate:  5_000_000,
}

// Test: Isolation-5M-FullELLockFree
// Purpose: Full lock-free path (sender + receiver)
{
    Name:        "Isolation-5M-FullELLockFree",
    Description: "Completely lock-free EventLoop (sender + receiver)",
    Config: ConfigFullELLockFree,
    Duration: 30 * time.Second,
    Bitrate:  5_000_000,
}
```

### 9.4 New Parallel Tests

**File:** `contrib/integration_testing/test_configs.go`

Following the naming convention from `integration_testing_matrix_design.md`:

| Test Name | Control | Test | Purpose |
|-----------|---------|------|---------|
| `Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF` | `FullEL` | `FullELLockFree` | Lock-free vs locked EventLoop |
| `Parallel-Clean-50M-5s-R0-FullEL-vs-FullELLF` | `FullEL` | `FullELLockFree` | High throughput comparison |
| `Parallel-Clean-20M-5s-R60-FullEL-vs-FullELLF` | `FullEL` | `FullELLockFree` | With 60ms RTT |
| `Parallel-Clean-20M-5s-R130-FullEL-vs-FullELLF` | `FullEL` | `FullELLockFree` | Intercontinental latency |
| `Parallel-Starlink-20M-5s-R60-FullEL-vs-FullELLF` | `FullEL` | `FullELLockFree` | With Starlink impairment |
| `Parallel-Loss-L5-20M-5s-R60-FullEL-vs-FullELLF` | `FullEL` | `FullELLockFree` | With 5% loss |

**Test Configuration Details:**

```go
// Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF
{
    Name:        "Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF",
    Description: "EventLoop (locked) vs EventLoop (lock-free)",
    Baseline: PipelineConfig{
        Name: "FullEL",
        SRT:  ConfigFullEL,  // Existing: EventLoop with locks
    },
    HighPerf: PipelineConfig{
        Name: "FullELLF",
        SRT:  ConfigFullELLockFree,  // NEW: Completely lock-free
    },
    NetworkProfile: CleanNetwork,
    Bitrate:        20_000_000,
    Buffer:         5 * time.Second,
    RTT:            0,
    Duration:       60 * time.Second,
}
```

### 9.5 Test Matrix Integration

**Update `integration_testing_matrix_design.md` Config Abbreviations:**

| Abbrev | Full Name | Features Enabled |
|--------|-----------|------------------|
| `FullEL` | Full EventLoop | btree + io_uring + NAK btree + Ring + EventLoop (existing) |
| `FullELLF` | Full EventLoop Lock-Free | FullEL + SendControlRing + RecvControlRing (NEW) |

**Phase Progression (Updated):**

```
Phase 1: Btree                          - btree packet store
Phase 2: NakBtree/NakBtreeF/NakBtreeFr  - NAK btree optimizations
Phase 3: Ring/FullRing                  - Lock-free packet handoff
Phase 4: EventLoop/FullEL               - Continuous event loop (replaces Tick)
Phase 5: FullELLF                       - Completely lock-free (NEW)
```

### 9.6 Expected Metrics Changes

When comparing `FullEL` (locked) vs `FullELLF` (lock-free):

| Metric | FullEL | FullELLF | Change |
|--------|--------|----------|--------|
| `recv_control_ring_pushed_total` | 0 | > 0 | NEW metric active |
| `recv_control_ring_processed_total` | 0 | > 0 | NEW metric active |
| `recv_eventloop_control_drained_total` | 0 | > 0 | Control packets via ring |
| Lock contention | Present | Absent | Performance improvement |
| RTT accuracy | Same | Same | Should be identical |
| Drop rate | Same | Same | Should be identical |

### 9.7 Validation Criteria

| Criterion | Requirement | How to Verify |
|-----------|-------------|---------------|
| **No regressions** | Drop rate ≤ FullEL | Compare `recv_drop_*` metrics |
| **RTT accuracy** | Within 5% of FullEL | Compare `rtt_us` distribution |
| **Control packets processed** | All ACKACK processed | `pushed == processed + dropped` |
| **No deadlocks** | Test completes in timeout | 60s timeout |
| **Race-free** | Pass with `-race` | Run with race detector |

### 9.8 Implementation Checklist

| Task | File | Status |
|------|------|--------|
| Add `ConfigFullELLockFree` config | `test_configs.go` | ⏳ |
| Add `Isolation-5M-RecvControlRing` | `test_configs.go` | ⏳ |
| Add `Isolation-5M-FullELLockFree` | `test_configs.go` | ⏳ |
| Add `Isolation-20M-FullELLockFree` | `test_configs.go` | ⏳ |
| Add `Isolation-50M-FullELLockFree` | `test_configs.go` | ⏳ |
| Add parallel comparison tests (6) | `test_configs.go` | ⏳ |
| Update `parallel_isolation_test_plan.md` | docs | ⏳ |
| Update `integration_testing_matrix_design.md` | docs | ⏳ |
| Add `FullELLF` to test matrix generator | `test_matrix.go` | ⏳ |
| **Add flag tests for new CLI flags** | `contrib/common/test_flags.sh` | ⏳ |
| **Run `make test-flags`** | - | ⏳ |
| Run and validate isolation tests | - | ⏳ |
| Run and validate parallel tests | - | ⏳ |

### 9.8.1 CLI Flag Testing

When new CLI flags are added, they must be tested via the flag testing infrastructure:

**File:** `contrib/common/test_flags.sh`

The following tests must be added for the new receiver control ring flags:

```bash
# Test: recvcontrolring flag
run_test "recvcontrolring flag" \
    "-recvcontrolring" \
    "UseRecvControlRing:.*true" \
    "$SERVER_BIN"

# Test: recvcontrolringsize flag
run_test "recvcontrolringsize flag" \
    "-recvcontrolringsize=256" \
    "RecvControlRingSize:.*256" \
    "$SERVER_BIN"

# Test: recvcontrolringshards flag
run_test "recvcontrolringshards flag" \
    "-recvcontrolringshards=2" \
    "RecvControlRingShards:.*2" \
    "$SERVER_BIN"

# Test: Combined receiver control ring flags
run_test "combined recv control ring flags" \
    "-recvcontrolring -recvcontrolringsize=128 -recvcontrolringshards=1" \
    "UseRecvControlRing:.*true.*RecvControlRingSize:.*128.*RecvControlRingShards:.*1" \
    "$SERVER_BIN"

# Test: Full lock-free configuration
run_test "full lock-free eventloop" \
    "-eventloop -sendcontrolring -recvcontrolring" \
    "UseEventLoop:.*true.*UseSendControlRing:.*true.*UseRecvControlRing:.*true" \
    "$SERVER_BIN"
```

**Running flag tests:**

```bash
# Run all flag tests
make test-flags

# Or run directly
./contrib/common/test_flags.sh
```

**Note:** The `-testflags` option must be implemented in server/client/client-generator to output the parsed config for validation.

### 9.9 Running the New Tests

```bash
# Run isolation tests for lock-free receiver
make test-isolation TEST=Isolation-5M-RecvControlRing
make test-isolation TEST=Isolation-5M-FullELLockFree
make test-isolation TEST=Isolation-20M-FullELLockFree

# Run parallel comparison tests
make test-parallel TEST=Parallel-Clean-20M-5s-R0-FullEL-vs-FullELLF

# Run all lock-free tests
make test-integration-lockfree

# Run with race detector (slower)
make test-isolation-race TEST=Isolation-5M-FullELLockFree
```

---

## 10. Migration Path

### 10.1 Phase 1: Add Control Ring Infrastructure

1. Create `control_ring.go` with RecvControlRing
2. Add config options (UseRecvControlRing, RecvControlRingSize)
3. Add metrics for control ring operations
4. Unit tests for control ring

### 10.2 Phase 2: Create EventLoop Function Variants

1. Create `periodicACKEventLoop()` (copy of locked version, remove locks)
2. Create `periodicNAKEventLoop()` (copy of locked version, remove locks)
3. Create `deliverReadyPacketsEventLoop()` (copy of locked version, remove locks)
4. Create `contiguousScanEventLoop()` (copy of locked version, remove locks)
5. Unit tests for each EventLoop variant

### 10.3 Phase 3: Function Dispatch Setup

1. Add function pointer fields to receiver struct
2. Initialize based on UseRecvControlRing config
3. Update EventLoop to use function pointers
4. Integration tests

### 10.4 Phase 4: ACKACK Routing

**Note:** Based on analysis, ACKACK routing via ring is NOT recommended (see Section 4.3).
Keep ACKACK direct for simplicity and minimal latency.

### 10.5 Rollback Procedure

```bash
# Disable lock-free mode
./server -recvcontrolring=false

# Or via config
config.UseRecvControlRing = false
```

---

## Appendix: File Summary

### Connection Level (Control Packet Routing)

| File | Description | New/Modified |
|------|-------------|--------------|
| `recv_control_ring.go` | RecvControlRing implementation | NEW |
| `connection.go` | Add useRecvControlRing, recvControlRing fields | MODIFIED |
| `connection_handlers.go` | Route ACKACK/KEEPALIVE through ring | MODIFIED |
| `connection_eventloop.go` | Add processControlPackets() | NEW or MODIFIED |
| `recv_control_ring_test.go` | Control ring unit tests | NEW |

### Receiver Level (Lock-Free Functions)

| File | Description | New/Modified |
|------|-------------|--------------|
| `congestion/live/receive/ack.go` | Add periodicACK (primary), periodicACKLocked (wrapper) | MODIFIED |
| `congestion/live/receive/nak.go` | Add periodicNAK (primary), periodicNAKLocked (wrapper) | MODIFIED |
| `congestion/live/receive/tick.go` | Add deliverReadyPackets (primary), *Locked (wrapper) | MODIFIED |
| `congestion/live/receive/scan.go` | Add contiguousScan (primary), *Locked (wrapper) | MODIFIED |
| `congestion/live/receive/receiver.go` | Add function dispatch fields, setupFunctionDispatch() | MODIFIED |
| `congestion/live/receive/eventloop_lockfree_test.go` | Unit tests for lock-free functions | NEW |
| `congestion/live/receive/eventloop_lockfree_bench_test.go` | Benchmarks | NEW |

### Configuration and Infrastructure

| File | Description | New/Modified |
|------|-------------|--------------|
| `config.go` | Add UseRecvControlRing, RecvControlRingSize options | MODIFIED |
| `contrib/common/flags.go` | Add CLI flags | MODIFIED |
| `metrics/metrics.go` | Add control ring + EventLoop metrics | MODIFIED |
| `metrics/handler.go` | Export new Prometheus metrics | MODIFIED |
| `contrib/integration_testing/test_configs.go` | Add test config for lock-free mode | MODIFIED |

---

## Summary

This design makes the receiver **completely lock-free** in EventLoop mode by:

1. **Control Packet Ring** - Route ALL incoming control packets (ACKACK, KEEPALIVE) through `RecvControlRing`
2. **Function Dispatch Pattern** - Primary functions (no suffix) are lock-free, `*Locked` wrappers for Tick mode
3. **Single Consumer Model** - EventLoop is the ONLY goroutine accessing receiver state
4. **Consistent with Sender** - Same control ring pattern as `SendControlRing`

**Architecture:**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       COMPLETELY LOCK-FREE RECEIVER                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   io_uring Handlers                              EventLoop                   │
│   (NO LOCKS)                                     (SINGLE CONSUMER)           │
│                                                                              │
│   Data packet ──────► packetRing.Write() ──┐                                │
│   ACKACK ───────────► controlRing.Push() ──┼────► EventLoop                 │
│   KEEPALIVE ────────► controlRing.Push() ──┘         │                      │
│                                                       │                      │
│                                              ┌───────┴───────┐              │
│                                              │               │              │
│                                              ▼               ▼              │
│                                        Control Pkts    Packet Ops           │
│                                        handleACKACK()  periodicACK()        │
│                                        handleKeepAlive periodicNAK()        │
│                                                        deliverReady()       │
│                                                        drainRing()          │
│                                                                              │
│   Tick() Mode (fallback):                                                   │
│   - Uses *Locked() wrappers                                                 │
│   - Each function acquires appropriate lock                                 │
│   - No control ring used                                                    │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Function Dispatch Summary:**

| Mode | Control Packets | Packet Operations | Locks in io_uring |
|------|-----------------|-------------------|-------------------|
| EventLoop + ControlRing | Via ring → EventLoop | Primary functions | **NONE** |
| EventLoop (no ring) | Direct with lock | Primary functions | c.ackLock |
| Tick() | Direct with lock | *Locked wrappers | c.ackLock + r.lock |

**Expected Benefits:**
- **Zero locks in io_uring handlers** - Just ring.Write() calls
- **Zero lock contention** - EventLoop is single consumer of all state
- **Lower and more predictable latency** - No lock waits in hot path
- **Consistent architecture** - Same pattern as sender control ring
- **Graceful fallback** - Tick() mode still works with locking wrappers

