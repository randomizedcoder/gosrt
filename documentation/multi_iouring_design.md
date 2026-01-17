# Multi io_uring Design Document

## Executive Summary

### The Problem
With a single io_uring completion handler, packets arriving in bursts must wait in a queue while the handler processes them one at a time. Additionally, a **critical bug** was discovered: `handlePacketDirect()` acquires `handlePacketMutex` even when the lock-free path is enabled, defeating the purpose of lock-free processing.

### Two-Part Solution

**Part 1: Fix Unnecessary Mutex (Phase 0)** - CRITICAL
- `handlePacketDirect()` should bypass `handlePacketMutex` when `UseEventLoop=true`
- All downstream operations are already lock-free (packetRing, controlRing)
- **Immediate RTT improvement expected**

**Part 2: Multiple io_uring Rings (Phases 1-5)**
- Multiple rings allow **true parallel processing** with no serialization
- Each handler processes packets completely independently
- Scales with CPU cores

### What Becomes Parallelizable (After Phase 0 Fix)

**ALL work in the completion handler:**
- Packet deserialization (~300ns)
- Address extraction (~100ns)
- Connection lookup (~50ns)
- Metric updates (~20ns)
- Lock-free ring push (~100ns)
- Lock-free packet ring write (~100ns)

**Total per packet: ~770ns - ALL PARALLEL, NO MUTEX!**

### Expected Improvement

| Change | RTT Before | RTT After | Improvement |
|--------|------------|-----------|-------------|
| Phase 0 (mutex fix) | 215µs | ~150µs | **-30%** |
| + Multi-ring (4 rings) | ~150µs | ~100µs | **Additional -33%** |
| **Combined** | **215µs** | **~100µs** | **-53%** |

### Key Insight
With the mutex fix, there is **NO serialization point** in the lock-free path. Multiple completion handlers can process packets in true parallel, bounded only by CPU cores and lock-free ring throughput.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Problem Statement](#2-problem-statement)
3. [Current Architecture](#3-current-architecture)
   - [3.1 Listener (Server) - Receive Path](#31-listener-server---receive-path)
   - [3.2 Dialer (Client) - Receive Path](#32-dialer-client---receive-path)
   - [3.3 Connection - Send Path](#33-connection---send-path)
   - [3.4 Current File Structure](#34-current-file-structure)
4. [Proposed Architecture](#4-proposed-architecture)
   - [4.1 Multiple Receive Rings (Listener)](#41-multiple-receive-rings-listener)
   - [4.2 Multiple Receive Rings (Dialer)](#42-multiple-receive-rings-dialer)
   - [4.3 Multiple Send Rings (Connection)](#43-multiple-send-rings-connection)
   - [4.4 Ring Selection Strategy](#44-ring-selection-strategy)
5. [Configuration Changes](#5-configuration-changes)
   - [5.1 Integration Test Configuration Updates](#51-integration-test-configuration-updates)
   - [5.2 Isolation Test Configurations](#52-isolation-test-configurations)
   - [5.3 Parallel Test Configurations](#53-parallel-test-configurations)
   - [5.4 Unit Tests and Table-Driven Tests](#54-unit-tests-and-table-driven-tests)
   - [5.5 Context Assert Analysis](#55-context-assert-analysis-current-state-and-improvements)
6. [Detailed Implementation Plan](#6-detailed-implementation-plan)
   - [Phase 0: Remove Unnecessary Mutex (CRITICAL)](#phase-0-remove-unnecessary-mutex-critical)
   - [Phase 0.5: Add Connection-Level Context Asserts](#phase-05-add-connection-level-context-asserts)
   - [Phase 1: Configuration](#phase-1-configuration)
   - [Phase 2: Listener Receive Multi-Ring](#phase-2-listener-receive-multi-ring)
   - [Phase 3: Dialer Receive Multi-Ring](#phase-3-dialer-receive-multi-ring)
   - [Phase 4: Connection Send Multi-Ring](#phase-4-connection-send-multi-ring)
   - [Phase 5: Metrics and Testing](#phase-5-metrics-and-testing)
7. [Risk Analysis](#7-risk-analysis)
8. [Performance Expectations](#8-performance-expectations)

---

## 1. Overview

This document describes the design for supporting multiple io_uring rings in gosrt, allowing parallel completion processing to reduce RTT and improve throughput.

**Goal**: Reduce RTT inflation observed in the lock-free receiver by parallelizing io_uring completion processing.

**Reference**: See `completely_lockfree_receiver_debugging.md` Section "Potential Bottleneck: Single Completion Handler" for the analysis that led to this design.

---

## 2. Problem Statement

### Current Issue

From `Isolation-5M-FullELLockFree` test results:

| Metric | Control | Test (Lock-Free) | Delta |
|--------|---------|------------------|-------|
| RTT (smoothed) | 123µs | 215µs | +74.8% |
| RTT (raw) | 253µs | 215µs | -15.0% |

The lock-free path shows consistently higher RTT, likely due to:

1. **Single Completion Handler**: ALL packets go through one goroutine
2. **Sequential Processing**: No parallelism in packet handling
3. **Queueing Delay**: Packets must wait while previous packet is processed

### Root Cause Analysis

```
┌─────────────────────────────────────────────────────────────────┐
│                    CURRENT ARCHITECTURE                          │
│                                                                  │
│  io_uring Recv CQ ─────► Single Handler ─────► handlePacketMutex │
│                              │                        │          │
│                              ▼                        ▼          │
│                    QUEUEING DELAY            CONTENTION          │
│                  (packets wait in CQ)    (serializes handling)   │
└─────────────────────────────────────────────────────────────────┘
```

### Architectural Issue: Unnecessary Mutex

**IMPORTANT DISCOVERY**: The current `handlePacketDirect()` ALWAYS acquires `handlePacketMutex`, even when lock-free paths are enabled!

```go
// connection_handlers.go:14-40 - CURRENT (BUG)
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    c.handlePacketMutex.Lock()      // ← ALWAYS acquires mutex!
    defer c.handlePacketMutex.Unlock()
    c.handlePacket(p)               // ← Then calls handlePacket → Push()
}
```

But `Push()` dispatches to `pushToRing()` which is **completely lock-free**:

```go
// push.go:37-52 - Already lock-free!
func (r *receiver) pushToRing(pkt packet.Packet) {
    m.RecvRatePackets.Add(1)        // Atomic
    m.RecvRateBytes.Add(pkt.Len())  // Atomic
    r.packetRing.WriteWithBackoff(...)  // Lock-free ring!
}
```

**Proposed Fix**: When `UsePacketRing=true` AND `UseEventLoop=true`, bypass the mutex:

```go
// connection_handlers.go - PROPOSED FIX
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    // Check if we're in completely lock-free mode
    if c.recv.UseEventLoop() && c.recv.UsePacketRing() {
        // Lock-free path: data → ring, control → control ring
        // No mutex needed - all downstream operations are lock-free
        c.handlePacket(p)
        return
    }

    // Legacy path: acquire mutex for sequential processing
    c.handlePacketMutex.Lock()
    defer c.handlePacketMutex.Unlock()
    c.handlePacket(p)
}
```

This fix should be implemented **before or alongside** the multi-ring work, as it removes the primary serialization bottleneck.

---

### Why Multiple Handlers Help (With Fix Applied)

Once `handlePacketMutex` is removed from the lock-free path, multiple handlers provide **true parallelism**:

**Key Insight**: With the fix applied, ALL work in the completion handler becomes parallelizable:

```go
// processRecvCompletion() work breakdown (listen_linux.go:388-559)
//
// ═══════════════════════════════════════════════════════════════════
// ALL WORK IS NOW PARALLELIZABLE (with mutex fix applied)
// ═══════════════════════════════════════════════════════════════════
//
// In io_uring completion handler (parallel across handlers):
// ┌──────────────────────────────────────────────────────────────┐
// │ 1. extractAddrFromRSA()      - Parse source IP/port  ~100ns  │
// │ 2. packet.NewPacket()        - Pool allocation       ~50ns   │
// │ 3. p.UnmarshalZeroCopy()     - Parse SRT header      ~300ns  │
// │ 4. ln.conns.Load()           - sync.Map lookup       ~50ns   │
// │ 5. metrics.IncrementRecv()   - Atomic increments     ~20ns   │
// └──────────────────────────────────────────────────────────────┘
//
// In handlePacketDirect() → handlePacket() (NOW LOCK-FREE):
// ┌──────────────────────────────────────────────────────────────┐
// │ 6. Control: dispatchACKACK() - Push to control ring  ~100ns  │
// │    dispatchKEEPALIVE()       - Push to control ring  ~100ns  │
// │ 7. Data: c.recv.Push()       - pushToRing()          ~150ns  │
// │    └─ Atomic metrics         - RecvRatePackets, etc  ~20ns   │
// │    └─ Lock-free ring write   - packetRing.Write()    ~100ns  │
// └──────────────────────────────────────────────────────────────┘
//
// TOTAL PER PACKET:              ~770ns (ALL PARALLEL!)
// ═══════════════════════════════════════════════════════════════════
```

**Key Point**: There is NO serialization point in the lock-free path. Each completion handler can process packets completely independently.

**With Multiple Handlers (and mutex fix) - TRUE PARALLELISM**:

```
TIME  ──────────────────────────────────────────────────────────►

Handler 0: [Parse+Handle P1 ~770ns][Parse+Handle P5 ~770ns]...
Handler 1: [Parse+Handle P2 ~770ns][Parse+Handle P6 ~770ns]...
Handler 2: [Parse+Handle P3 ~770ns][Parse+Handle P7 ~770ns]...
Handler 3: [Parse+Handle P4 ~770ns][Parse+Handle P8 ~770ns]...
                                    │
                                    ▼
           ┌─────────────────────────────────────────────────┐
           │ ALL 4 packets processed IN PARALLEL!           │
           │ No mutex, no waiting, no serialization         │
           │ Throughput: 4x single handler                  │
           │ Latency: ~770ns regardless of burst size       │
           └─────────────────────────────────────────────────┘
```

**Without Multiple Handlers (Current - with mutex bug)**:

```
TIME  ──────────────────────────────────────────────────────────►

Handler 0: [Parse P1][MUTEX WAIT][Handle P1][Parse P2][MUTEX]...
                          │
                          ▼
           ┌─────────────────────────────────────────────────┐
           │ P2 WAITS for P1 to release mutex               │
           │ Sequential: ~1120ns per packet (parse+handle)  │
           │ At burst: 10 packets = 11.2µs total delay      │
           └─────────────────────────────────────────────────┘
```

**Without Multiple Handlers (with mutex fix applied)**:

```
TIME  ──────────────────────────────────────────────────────────►

Handler 0: [Parse+Handle P1 ~770ns][Parse+Handle P2 ~770ns]...
                                    │
                                    ▼
           ┌─────────────────────────────────────────────────┐
           │ Still sequential (single handler)              │
           │ But NO mutex wait - lock-free throughout       │
           │ Per packet: ~770ns (vs ~1120ns with mutex)     │
           │ At burst: 10 packets = 7.7µs (vs 11.2µs)       │
           └─────────────────────────────────────────────────┘
```

### Queueing Theory Analysis

At **5 Mb/s** with 1456-byte packets:
- Average rate: ~429 packets/second (~2.33ms between packets)
- But packets arrive in **bursts** due to network batching

**io_uring batching behavior**:
- `WaitCQETimeout` returns with 1+ completions
- During burst: 5-20 packets may complete simultaneously
- These must ALL go through the single handler sequentially

**Example: 10-packet burst with single handler**:
```
Completion times for 10 packets in burst:
  P1: 0µs    (immediate)
  P2: 1.1µs  (waits for P1)
  P3: 2.2µs  (waits for P1+P2)
  ...
  P10: 10.0µs (waits for P1-P9)

  Average completion latency: 5µs
  Maximum completion latency: 10µs
```

**Same burst with 4 handlers**:
```
Completion times for 10 packets in burst (4 handlers):
  P1,P2,P3,P4: 0µs    (immediate, parallel parse)
  P5,P6,P7,P8: ~0.6µs (wait for parse, parallel)
  P9,P10:      ~1.2µs (wait for parse)

  Plus mutex serialization: ~2µs additional

  Average completion latency: ~1.5µs
  Maximum completion latency: ~3µs
```

**Estimated Improvement: 3-4x reduction in burst latency**

### Proposed Solution: Fully Independent Rings

**Key Principle**: Each ring is completely self-contained with NO cross-ring interaction during operation.

**Why This Matters**:
- The completion handler already resubmits receive requests after processing
- If each handler resubmits ONLY to its own ring, there's no need for coordination
- Each ring owns its completion map exclusively - NO LOCKS NEEDED
- The only coordination is at startup (distributing initial requests) - one-time cost

```
┌─────────────────────────────────────────────────────────────────┐
│             FULLY INDEPENDENT RING ARCHITECTURE                  │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Ring 0 (Fully Independent)          Ring 1 (Fully Independent) │
│  ┌─────────────────────────┐         ┌─────────────────────────┐│
│  │ buffers[0..127]         │         │ buffers[128..255]       ││
│  │ completionMap (owned)   │         │ completionMap (owned)   ││
│  │ requestID counter       │         │ requestID counter       ││
│  │                         │         │                         ││
│  │  Handler Goroutine:     │         │  Handler Goroutine:     ││
│  │  - Read CQE from ring   │         │  - Read CQE from ring   ││
│  │  - Lookup in OWN map    │         │  - Lookup in OWN map    ││
│  │  - Process packet       │         │  - Process packet       ││
│  │  - Resubmit to SAME ring│         │  - Resubmit to SAME ring││
│  │  NO LOCKS NEEDED!       │         │  NO LOCKS NEEDED!       ││
│  └─────────────────────────┘         └─────────────────────────┘│
│           │                                   │                  │
│           │ (lock-free)                       │ (lock-free)      │
│           ▼                                   ▼                  │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │              handlePacketDirect() - NO MUTEX                 ││
│  │                      (Phase 0 fix)                           ││
│  └─────────────────────────────────────────────────────────────┘│
│                              │                                   │
│                              ▼                                   │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │           Lock-Free Packet Ring + Control Ring               ││
│  │                  (parallel writes OK)                        ││
│  └─────────────────────────────────────────────────────────────┘│
│                              │                                   │
│                              ▼                                   │
│  ┌─────────────────────────────────────────────────────────────┐│
│  │                  EventLoop (single consumer)                 ││
│  └─────────────────────────────────────────────────────────────┘│
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Critical Design Constraint**: Each completion handler resubmits ONLY to its own ring. This eliminates all cross-ring coordination during normal operation.

### Key Distinction: Receive vs Send Paths

| Aspect | Receive Path | Send Path |
|--------|--------------|-----------|
| **Submitter** | Completion handler resubmits | Application (sender EventLoop) |
| **Completer** | Same completion handler | Different completion handler |
| **Goroutines** | Single goroutine owns ring | Two goroutines touch ring |
| **Lock needed?** | **NO** (single owner) | **YES** (cross-goroutine) |
| **Priority** | Critical for RTT | Less critical |

**Why Receive is Lock-Free**:
```
Receive Ring:
┌─────────────────────────────────────────────┐
│  Handler Goroutine (owns everything):       │
│  1. Wait for completion                     │
│  2. Lookup in map (read)     ─┐             │
│  3. Delete from map (write)   │ Same        │
│  4. Process packet            │ goroutine!  │
│  5. Resubmit to ring          │             │
│  6. Add to map (write)       ─┘             │
│  NO LOCK NEEDED!                            │
└─────────────────────────────────────────────┘
```

**Why Send Needs Minimal Synchronization**:
```
Send Ring:
┌─────────────────────────────────────────────┐
│  Sender EventLoop:                          │
│  - Submits request (write to map)           │
│                        ↓                    │
│  Send Completion Handler:                   │
│  - Processes completion (read/delete map)   │
│  Two goroutines → Need sync for map         │
└─────────────────────────────────────────────┘
```

**Mitigation for Send**: The send path is less RTT-critical (it's outbound). A simple per-ring lock or lock-free map is acceptable.

```
┌─────────────────────────────────────────────────────────────────┐
│                    PROPOSED ARCHITECTURE                         │
│                                                                  │
│  io_uring Recv CQ 0 ─────► Handler 0 ─┐                         │
│  io_uring Recv CQ 1 ─────► Handler 1 ─┼───► handlePacketMutex    │
│  io_uring Recv CQ 2 ─────► Handler 2 ─┤                         │
│  io_uring Recv CQ 3 ─────► Handler 3 ─┘                         │
│                                                                  │
│  Each ring operates independently on the SAME UDP socket FD     │
│  Kernel naturally distributes completions across rings          │
└─────────────────────────────────────────────────────────────────┘
```

---

## 3. Current Architecture

### 3.1 Listener (Server) - Receive Path

**File**: `listen_linux.go`

**Key Functions**:
- `initializeIoUringRecv()` - Lines 104-154
- `recvCompletionHandler()` - Lines 828-885
- `submitRecvRequest()` - Lines 219-346
- `processRecvCompletion()` - Lines 388-559

**Current Structure** (from `listen.go`, lines 156-165):
```go
// io_uring receive path (Linux only)
recvRing        interface{}                    // *giouring.Ring on Linux
recvRingFd      int                            // UDP socket file descriptor
recvCompletions map[uint64]*recvCompletionInfo // Maps request ID to completion info
recvCompLock    sync.Mutex                     // Protects recvCompletions map
recvRequestID   atomic.Uint64                  // Unique request ID counter
recvCompCtx     context.Context                // Context for completion handler
recvCompCancel  context.CancelFunc             // Cancel function
recvCompWg      sync.WaitGroup                 // WaitGroup for handler goroutine
```

**Flow**:
1. `initializeIoUringRecv()` creates 1 ring, starts 1 handler goroutine
2. `submitRecvRequest()` submits recvmsg to ring
3. `recvCompletionHandler()` polls for completions (10ms timeout)
4. `processRecvCompletion()` handles each completion → `handlePacketDirect()`

### 3.2 Dialer (Client) - Receive Path

**File**: `dial_linux.go`

**Key Functions**:
- `initializeIoUringRecv()` - Lines 21-66
- `recvCompletionHandler()` - Lines 522-563
- `submitRecvRequest()` - Lines 126-236
- `processRecvCompletion()` - Lines 272-376

**Current Structure** (from `dial.go`):
```go
// io_uring receive path (Linux only)
recvRing        interface{}
recvRingFd      int
recvCompletions map[uint64]*recvCompletionInfo
recvCompLock    sync.Mutex
recvRequestID   atomic.Uint64
recvCompWg      sync.WaitGroup
```

**Flow**: Same pattern as listener.

### 3.3 Connection - Send Path

**File**: `connection_linux.go`

**Key Functions**:
- `initializeIoUring()` - Lines 44-98
- `sendCompletionHandler()` - Lines 393-516
- `sendIoUring()` - Lines 169-383
- `send()` - Lines 592-609

**Current Structure** (from `connection.go`, lines ~150-170):
```go
// io_uring send path (Linux only)
sendRing        interface{}                   // *giouring.Ring
sendRingFd      int                           // Socket file descriptor
sendSockaddr    [28]byte                      // Pre-computed sockaddr
sendSockaddrLen uint32                        // Sockaddr length
sendCompletions map[uint64]*sendCompletionInfo
sendCompLock    sync.Mutex
sendBufferPool  sync.Pool                     // Per-connection buffer pool
sendRequestID   atomic.Uint64
sendCompCtx     context.Context
sendCompCancel  context.CancelFunc
sendCompWg      sync.WaitGroup
```

### 3.4 Current File Structure

| File | Component | Purpose |
|------|-----------|---------|
| `listen.go` | listener struct | Defines `recvRing` fields (lines 156-165) |
| `listen_linux.go` | listener methods | io_uring recv implementation |
| `listen_other.go` | listener methods | Non-Linux stubs |
| `dial.go` | dialer struct | Defines `recvRing` fields |
| `dial_linux.go` | dialer methods | io_uring recv implementation |
| `dial_other.go` | dialer methods | Non-Linux stubs |
| `connection.go` | srtConn struct | Defines `sendRing` fields (lines ~150-170) |
| `connection_linux.go` | srtConn methods | io_uring send implementation |
| `connection_other.go` | srtConn methods | Non-Linux stubs |
| `config.go` | Config struct | io_uring configuration (lines 210-257) |

---

## 4. Proposed Architecture

### 4.1 Multiple Receive Rings (Listener)

**Key Insight**: Multiple io_uring rings can share the same UDP socket file descriptor. Each ring submits `recvmsg` operations, and the kernel naturally distributes incoming packets.

**Design Principle**: Each ring is encapsulated in a single struct that contains ALL its state. This is cleaner than multiple parallel slices and makes ownership explicit.

**Per-Ring State Structure** (fully independent, no locks during operation):
```go
// recvRingState encapsulates ALL state for a single io_uring receive ring.
// Each instance is OWNED EXCLUSIVELY by its handler goroutine - no locks needed.
type recvRingState struct {
    // Core io_uring state
    ring        *giouring.Ring                  // The io_uring ring
    fd          int                             // Socket fd (shared, read-only)

    // Completion tracking (owned exclusively by handler goroutine)
    completions map[uint64]*recvCompletionInfo  // Request ID → completion info
    nextID      uint64                          // Next request ID (plain int, not atomic!)

    // NOTE: Buffers come from global sync.Pool (buffers.go), NOT per-ring pool.
    // See Section 4.5 "Buffer Pool Design Analysis" for rationale.
}
```

**Listener Structure** (single slice of structs):
```go
// In listener struct - CLEAN and MAINTAINABLE
type listener struct {
    // ... other fields ...

    // io_uring receive path (Linux only) - MULTI-RING
    recvRingFd     int                // Shared UDP socket FD (read-only)
    recvRingStates []*recvRingState   // Each ring is fully independent
    recvCompCtx    context.Context    // Shared context for shutdown
    recvCompCancel context.CancelFunc // Shared cancel for shutdown
    recvCompWg     sync.WaitGroup     // WaitGroup for ALL handlers
}
```

**Why Single Slice of Structs is Better**:

| Aspect | Multiple Parallel Slices (Old) | Single Slice of Structs (New) |
|--------|--------------------------------|-------------------------------|
| Code clarity | `recvCompletions[i]`, `recvCompLocks[i]`, `recvRequestIDs[i]` | `recvRingStates[i].completions` |
| Ownership | Scattered across multiple slices | Encapsulated in one struct |
| Adding fields | Update ALL slice initializations | Update ONE struct definition |
| Data locality | Fields spread in memory | Fields grouped together (cache-friendly) |
| Maintenance | Easy to forget a slice | Can't forget - it's all in the struct |

**Diagram**:
```
                     ┌─────────────────────────────────────────┐
                     │           UDP Socket (ln.pc)            │
                     │              (single FD)                │
                     └────────────────────┬────────────────────┘
                                          │
          ┌───────────────┬───────────────┼───────────────┬───────────────┐
          ▼               ▼               ▼               ▼               │
   ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐       │
   │recvRingState│ │recvRingState│ │recvRingState│ │recvRingState│       │
   │     [0]     │ │     [1]     │ │     [2]     │ │     [3]     │       │
   │ ┌─────────┐ │ │ ┌─────────┐ │ │ ┌─────────┐ │ │ ┌─────────┐ │       │
   │ │  ring   │ │ │ │  ring   │ │ │ │  ring   │ │ │ │  ring   │ │       │
   │ │completns│ │ │ │completns│ │ │ │completns│ │ │ │completns│ │       │
   │ │ nextID  │ │ │ │ nextID  │ │ │ │ nextID  │ │ │ │ nextID  │ │       │
   │ │bufferPl │ │ │ │bufferPl │ │ │ │bufferPl │ │ │ │bufferPl │ │       │
   │ └─────────┘ │ │ └─────────┘ │ │ └─────────┘ │ │ └─────────┘ │       │
   └──────┬──────┘ └──────┬──────┘ └──────┬──────┘ └──────┬──────┘       │
          │               │               │               │               │
          ▼               ▼               ▼               ▼               │
   ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐       │
   │  Handler 0  │ │  Handler 1  │ │  Handler 2  │ │  Handler 3  │       │
   │(owns state0)│ │(owns state1)│ │(owns state2)│ │(owns state3)│       │
   │  NO LOCK!   │ │  NO LOCK!   │ │  NO LOCK!   │ │  NO LOCK!   │       │
   └──────┬──────┘ └──────┬──────┘ └──────┬──────┘ └──────┬──────┘       │
          │               │               │               │               │
          └───────────────┴───────────────┴───────────────┘               │
                                          │                               │
                                          ▼                               │
                     ┌─────────────────────────────────────────┐          │
                     │     handlePacketDirect() - NO MUTEX     │          │
                     │        (Phase 0 fix applied)            │          │
                     └─────────────────────────────────────────┘          │
                                          │                               │
                                          ▼                               │
                     ┌─────────────────────────────────────────┐          │
                     │      Lock-Free Packet/Control Rings     │◄─────────┘
                     │         (parallel writes OK)            │  Each handler
                     └─────────────────────────────────────────┘  resubmits to
                                                                  its OWN ring
```

### 4.2 Multiple Receive Rings (Dialer)

Same struct-based pattern as listener:

```go
// dialerRecvRingState encapsulates ALL state for a single io_uring receive ring.
type dialerRecvRingState struct {
    ring        *giouring.Ring
    fd          int
    completions map[uint64]*recvCompletionInfo
    nextID      uint64
    // Buffers from global sync.Pool - see Section 4.5
}

// In dialer struct:
type dialer struct {
    // ... other fields ...
    recvRingFd     int
    recvRingStates []*dialerRecvRingState  // Single slice of structs
    recvCompCtx    context.Context
    recvCompCancel context.CancelFunc
    recvCompWg     sync.WaitGroup
}
```

For client (dialer), multi-ring is less critical since there's typically one connection, but consistency with the listener pattern is valuable for maintainability.

### 4.3 Multiple Send Rings (Connection)

**Key Insight**: Multiple send rings per connection can parallelize send completions.

**Per-Ring State Structure**:
```go
// sendRingState encapsulates ALL state for a single io_uring send ring.
// Note: Send rings have two goroutines (sender + completer), so we need
// a lock for the completion map, unlike receive rings.
type sendRingState struct {
    ring        *giouring.Ring
    fd          int
    completions map[uint64]*sendCompletionInfo
    compLock    sync.Mutex  // Needed: sender writes, completer reads
    nextID      atomic.Uint64  // Atomic: sender goroutine increments
}

// In srtConn struct:
type srtConn struct {
    // ... other fields ...
    sendRingFd     int
    sendRingStates []*sendRingState  // Single slice of structs
    sendRingIdx    atomic.Uint64     // Round-robin selector
    sendCompCtx    context.Context
    sendCompCancel context.CancelFunc
    sendCompWg     sync.WaitGroup
}
```

**Why Send Needs a Lock (but Receive Doesn't)**:
- **Receive**: Handler goroutine both submits AND completes → single owner → no lock
- **Send**: Sender EventLoop submits, Handler goroutine completes → two owners → need lock

This is acceptable because:
1. The send path is less RTT-critical (outbound)
2. The lock is per-ring, so multiple rings reduce contention
3. Lock hold time is minimal (just map insert/delete)

### 4.4 Ring Selection Strategy

**For Receive**: No selection needed! Each handler resubmits to its OWN ring.

**For Send**: Round-robin across rings when sending packets.

**Send Ring Selection** (the only place we need selection):
```go
func (c *srtConn) selectSendRing() *sendRingState {
    idx := c.sendRingIdx.Add(1) - 1
    return c.sendRingStates[idx % uint64(len(c.sendRingStates))]
}
```

### 4.5 Buffer Pool Design Analysis

The `recvRingState` struct initially included per-ring buffer pools:

```go
// Option A: Per-ring buffer pools (maximum independence)
type recvRingState struct {
    // ...
    bufferPool  []*[]byte  // Fixed-size pool for THIS ring only
    bufferIndex int
}
```

However, this raises a critical question: **which pool does a buffer get returned to?**

#### Buffer Lifecycle

```
Buffer Get() ──► io_uring recv ──► packet parse ──► lock-free ring ──► btree ──► expiry/deliver ──► Buffer Put()
     │                                                                                                    │
     │                                                                                                    │
     └────────────────────── LONG JOURNEY, DIFFERENT GOROUTINES ──────────────────────────────────────────┘
```

The buffer's lifecycle spans:
1. **Completion handler** calls `Get()` from pool
2. Buffer travels through **lock-free ring** (different writer/reader)
3. Buffer stored in **btree** (receiver or sender)
4. Eventually **EventLoop** expires or delivers packet
5. **EventLoop** (or delivery callback) calls `Put()` back to pool

**Problem**: By the time `Put()` is called, we're in a completely different goroutine than the one that called `Get()`. If we have per-ring pools, how do we know which pool to return to?

#### Option A: Per-Ring Buffer Pools

```go
// Each ring has its own pool
type recvRingState struct {
    bufferPool *sync.Pool  // Per-ring pool
}

// Buffer needs to track its origin
type trackedBuffer struct {
    data []byte
    pool *sync.Pool  // Which pool to return to
}
```

**Pros**:
- Zero contention between rings
- Complete ring independence
- Better theoretical cache locality

**Cons**:
- **Must track origin pool per buffer** - adds memory overhead
- **Complex lifecycle** - buffer can end up anywhere, must carry pool reference
- **All code paths must use tracked buffer** - invasive change
- **Increased GC pressure** - extra allocation per buffer for tracking struct
- **Bug risk** - returning to wrong pool corrupts memory reuse

#### Option B: Single Global `sync.Pool` (Current Design)

```go
// Single shared pool (buffers.go)
var globalBufferPool = sync.Pool{
    New: func() interface{} {
        buf := make([]byte, maxPacketSize)
        return &buf
    },
}

// All rings use the same pool
func getBuffer() *[]byte {
    return globalBufferPool.Get().(*[]byte)
}

func putBuffer(buf *[]byte) {
    globalBufferPool.Put(buf)
}
```

**Pros**:
- **Simple** - no tracking needed, return from anywhere
- **Already implemented** - current `buffers.go` works
- **`sync.Pool` is optimized** for concurrent access (see below)
- **No extra memory** - just the buffer itself
- **No invasive changes** - existing code continues to work

**Cons**:
- Potential contention when multiple rings call Get/Put simultaneously
- Slightly less cache locality (buffers may migrate between Ps)

#### Why `sync.Pool` Contention is Minimal

`sync.Pool` is **not a simple shared pool with a mutex**. It has sophisticated optimizations:

```
┌─────────────────────────────────────────────────────────────────────┐
│                    sync.Pool Internal Structure                      │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│   P0 (Processor 0)      P1 (Processor 1)      P2 (Processor 2)     │
│   ┌──────────────┐      ┌──────────────┐      ┌──────────────┐     │
│   │ Local Cache  │      │ Local Cache  │      │ Local Cache  │     │
│   │ (per-P, no   │      │ (per-P, no   │      │ (per-P, no   │     │
│   │  lock!)      │      │  lock!)      │      │  lock!)      │     │
│   └──────┬───────┘      └──────┬───────┘      └──────┬───────┘     │
│          │                     │                     │              │
│          └─────────────────────┼─────────────────────┘              │
│                                │                                    │
│                       ┌────────▼────────┐                           │
│                       │  Victim Cache   │                           │
│                       │  (shared, but   │                           │
│                       │   rarely used)  │                           │
│                       └─────────────────┘                           │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

**How it works**:
1. `Get()` first checks **local per-P cache** (no lock, no atomic)
2. If empty, steals from **victim cache** (lock-free linked list)
3. Only if both empty, calls `New()` to allocate

**In our case**:
- If Ring 0 handler runs on P0 and Ring 1 handler runs on P1...
- Each will mostly hit their own **per-P local cache**
- Cross-P contention is rare and cheap (atomic CAS, not mutex)

#### Recommendation: **Single Global `sync.Pool`** ✅ **ACCEPTED**

> **Decision (2026-01-15)**: This recommendation has been accepted. All multi-ring implementations will use the existing global `sync.Pool` from `buffers.go`. Per-ring buffer pools are rejected due to tracking complexity.

**Rationale**:
1. **Simplicity wins** - No tracking, no invasive changes
2. **`sync.Pool` is already optimized** - Per-P caches minimize real contention
3. **Buffer lifecycle is complex** - Tracking origin pool adds bug risk
4. **Measured contention is likely negligible** - Can verify with benchmarks
5. **Maintain existing code** - `buffers.go` already works correctly

**Updated `recvRingState` (without per-ring pool)**:

```go
// recvRingState - uses global buffer pool
type recvRingState struct {
    ring        *giouring.Ring
    fd          int
    completions map[uint64]*recvCompletionInfo
    nextID      uint64
    // NO per-ring buffer pool - use global sync.Pool
}

// In completion handler:
func (ln *listener) recvCompletionHandlerIndependent(ctx context.Context, state *recvRingState) {
    for {
        // Get buffer from GLOBAL pool
        bufferPtr := getBuffer()  // buffers.go global pool

        // ... submit recv, wait for completion, process ...

        // Buffer travels through system...
        // Eventually returned to GLOBAL pool by EventLoop
    }
}
```

**Verification Plan**:
After implementation, benchmark with:
```bash
go test -bench=BenchmarkMultiRingBufferContention -benchtime=10s
```

If contention becomes measurable (>5% overhead), we can revisit per-ring pools with buffer tracking.

#### Decision Summary

| Aspect | Decision |
|--------|----------|
| **Buffer pool design** | **Global `sync.Pool`** (accepted) |
| **Per-ring pools** | Rejected (tracking complexity) |
| **File** | `buffers.go` (existing, no changes needed) |
| **Date** | 2026-01-15 |

---

### 4.6 Summary: Struct-Based Design Benefits

| Aspect | Old (Parallel Slices) | New (Struct Per Ring) |
|--------|----------------------|----------------------|
| **Add new field** | Update 5+ slice declarations | Update 1 struct |
| **Access pattern** | `rings[i]`, `completions[i]`, `locks[i]` | `ringStates[i].ring`, `.completions` |
| **Ownership** | Unclear which slices go together | Explicit: struct owns all its data |
| **Cache locality** | Fields grouped together | Fields grouped together |
| **Initialization** | Multiple `make()` calls | One `newRingState()` call |
| **Cleanup** | Iterate multiple slices | Iterate one slice |
| **Buffer pool** | N/A | **Global `sync.Pool`** (shared, minimal contention) |

---

## 5. Configuration Changes

### New Configuration Fields

**File**: `config.go`

Add after line 257 (after `IoUringRecvBatchSize`):

```go
// Number of io_uring receive rings to create (default: 1)
// Multiple rings allow parallel completion processing
// Valid values: 1-16 (power of 2 recommended)
IoUringRecvRingCount int

// Number of io_uring send rings per connection (default: 1)
// Multiple rings allow parallel send completion processing
// Valid values: 1-8 (power of 2 recommended)
IoUringSendRingCount int
```

### Default Values

**File**: `config.go`, in `DefaultConfig()`:

```go
IoUringRecvRingCount: 1,  // Default to 1 for backward compatibility
IoUringSendRingCount: 1,  // Default to 1 for backward compatibility
```

### CLI Flags

**File**: `contrib/common/flags.go`

Add new flags after line 72 (after `IoUringRecvBatchSize`):

```go
// Multiple io_uring rings configuration flags
IoUringRecvRingCount = flag.Int("iouringrecvringcount", 1, "Number of io_uring receive rings for parallel processing (1-16)")
IoUringSendRingCount = flag.Int("iouringsendringcount", 1, "Number of io_uring send rings per connection (1-8)")
```

Add to `ApplyFlagsToConfig()` function:

```go
if FlagSet["iouringrecvringcount"] {
    config.IoUringRecvRingCount = *IoUringRecvRingCount
}
if FlagSet["iouringsendringcount"] {
    config.IoUringSendRingCount = *IoUringSendRingCount
}
```

### Flag Verification

After adding flags, run:

```bash
make test-flags
```

This verifies that all CLI flags are correctly defined and can be parsed. The test validates:
- Flag names are unique
- Flag types match Config field types
- Default values are valid

### Application Updates

The flags are handled centrally in `contrib/common/flags.go`, which is used by all applications:

| Application | File | Changes Required |
|-------------|------|------------------|
| `client-generator` | `contrib/client-generator/main.go` | None - uses `common.ApplyFlagsToConfig()` |
| `client` | `contrib/client/main.go` | None - uses `common.ApplyFlagsToConfig()` |
| `server` | `contrib/server/main.go` | None - uses `common.ApplyFlagsToConfig()` |

The applications automatically inherit the new flags through the shared flags infrastructure.

---

### Ring Size vs Ring Count Analysis

#### Background

The current io_uring configuration uses relatively large ring sizes (default 512, up to 8192 for high-throughput scenarios) to ensure we don't run out of slots. With multiple rings, should we automatically reduce the per-ring size?

#### The Question

```
Current:  1 ring  × 512 entries = 512 total capacity
Option A: 4 rings × 512 entries = 2048 total capacity (more memory)
Option B: 4 rings × 128 entries = 512 total capacity (same memory)
```

Should `IoUringRecvRingSize` be automatically divided by `IoUringRecvRingCount`?

#### Analysis

**Memory Impact**:
- Each io_uring ring uses memory for SQ (Submission Queue) and CQ (Completion Queue)
- A 512-entry ring ≈ 8-16 KB (small relative to packet buffers)
- 4 rings × 512 entries ≈ 32-64 KB total (still small)

**Burst Handling**:
- Packets aren't evenly distributed across rings during bursts
- Kernel distributes based on timing/scheduling, not load balancing
- One ring might temporarily receive more packets than others
- Smaller rings per-ring = higher risk of CQ overflow during uneven bursts

**CQ Overflow Behavior**:
- io_uring tracks overflow per-ring, not aggregate
- If one ring's CQ overflows, those completions are lost
- Larger per-ring size provides safety margin

**Example Burst Scenario**:
```
4-ring setup, 100 packets arrive in 1ms burst:

Ideal distribution:   Ring0=25, Ring1=25, Ring2=25, Ring3=25
Realistic (uneven):   Ring0=40, Ring1=35, Ring2=15, Ring3=10

With 128-entry rings: Ring0 might overflow if burst continues
With 512-entry rings: Plenty of headroom for uneven distribution
```

#### Recommendation: **Keep Ring Sizes Independent** ✅ **ACCEPTED**

> **Decision (2026-01-15)**: Do NOT automatically reduce ring size based on ring count. Keep them as independent configuration parameters.

**Rationale**:

1. **Memory overhead is negligible** - Even 4×512 = 2048 entries is only ~32KB
2. **Bursts aren't evenly distributed** - Each ring needs headroom for worst-case
3. **Simplicity** - Auto-calculation adds complexity for minimal benefit
4. **User control** - Advanced users can manually tune if needed
5. **Safe defaults** - Better to have extra capacity than overflow

**Configuration Guidance**:

| Scenario | Ring Count | Ring Size | Total Capacity | Notes |
|----------|------------|-----------|----------------|-------|
| Default | 1 | 512 | 512 | Backward compatible |
| Multi-ring (low rate) | 4 | 512 | 2048 | Recommended |
| Multi-ring (high rate) | 4 | 2048 | 8192 | For 100+ Mb/s |
| Memory constrained | 4 | 256 | 1024 | Manual tuning |

**What NOT to Do**:
```go
// DON'T: Auto-reduce ring size
effectiveRingSize := config.IoUringRecvRingSize / config.IoUringRecvRingCount

// DO: Use configured ring size for each ring
for i := 0; i < ringCount; i++ {
    ring.QueueInit(config.IoUringRecvRingSize, 0)  // Full size per ring
}
```

**Future Consideration**:
If memory becomes a concern in embedded/constrained environments, we could add a separate `IoUringRecvRingSizePerRing` config option. But for now, this is unnecessary complexity.

---

## 5.1 Integration Test Configuration Updates

### File: `contrib/integration_testing/config.go`

#### New SRTConfig Fields

Add after `IoUringRecvBatchSize` (~line 410):

```go
IoUringRecvRingCount int  // -iouringrecvringcount
IoUringSendRingCount int  // -iouringsendringcount
```

#### ToCliFlags() Updates

Add flag generation in `ToCliFlags()` (~line 566):

```go
if c.IoUringRecvRingCount > 1 {
    flags = append(flags, "-iouringrecvringcount", strconv.Itoa(c.IoUringRecvRingCount))
}
if c.IoUringSendRingCount > 1 {
    flags = append(flags, "-iouringsendringcount", strconv.Itoa(c.IoUringSendRingCount))
}
```

#### New Helper Methods

Add new builder methods (~line 1565):

```go
// ============================================================================
// MULTIPLE IO_URING RINGS HELPERS (Phase 1-5: Multi-Ring Architecture)
// ============================================================================

// WithMultipleRecvRings enables multiple io_uring receive rings.
// REQUIRES: IoUringRecvEnabled=true
// This enables parallel completion processing for reduced latency.
func (c SRTConfig) WithMultipleRecvRings(count int) SRTConfig {
    if !c.IoUringRecvEnabled {
        c = c.WithIoUringRecv()
    }
    if count < 1 || count > 16 {
        panic("IoUringRecvRingCount must be 1-16")
    }
    c.IoUringRecvRingCount = count
    return c
}

// WithMultipleSendRings enables multiple io_uring send rings per connection.
// REQUIRES: IoUringEnabled=true
// This enables parallel send completion processing.
func (c SRTConfig) WithMultipleSendRings(count int) SRTConfig {
    if !c.IoUringEnabled {
        c = c.WithIoUringSend()
    }
    if count < 1 || count > 8 {
        panic("IoUringSendRingCount must be 1-8")
    }
    c.IoUringSendRingCount = count
    return c
}

// WithParallelIoUring enables multiple rings for both send and receive paths.
// This is the high-performance configuration for lowest latency.
func (c SRTConfig) WithParallelIoUring(recvCount, sendCount int) SRTConfig {
    return c.WithMultipleRecvRings(recvCount).WithMultipleSendRings(sendCount)
}
```

---

## 5.2 Isolation Test Configurations

Add new isolation test configurations to test multi-ring behavior:

### File: `contrib/integration_testing/config.go`

Add after existing isolation configs (~line 195):

```go
// Isolation-5M-MultiRing2 - Test with 2 receive rings
// Purpose: Verify parallel completion processing with 2 workers
"Isolation-5M-MultiRing2": {
    Server: ParticipantConfig{
        SRT: HighPerfSRTConfig.WithMultipleRecvRings(2),
    },
    ClientGen: ParticipantConfig{
        SRT:      HighPerfSRTConfig.WithMultipleSendRings(2),
        Bitrate:  "5M",
        Duration: 10 * time.Second,
    },
    Client: ParticipantConfig{
        SRT: ControlSRTConfig,
    },
},

// Isolation-5M-MultiRing4 - Test with 4 receive rings
// Purpose: Verify parallel completion processing with 4 workers
"Isolation-5M-MultiRing4": {
    Server: ParticipantConfig{
        SRT: HighPerfSRTConfig.WithMultipleRecvRings(4),
    },
    ClientGen: ParticipantConfig{
        SRT:      HighPerfSRTConfig.WithMultipleSendRings(4),
        Bitrate:  "5M",
        Duration: 10 * time.Second,
    },
    Client: ParticipantConfig{
        SRT: ControlSRTConfig,
    },
},

// Isolation-20M-MultiRing4 - Higher throughput with 4 rings
// Purpose: Verify RTT improvement at higher packet rates
"Isolation-20M-MultiRing4": {
    Server: ParticipantConfig{
        SRT: HighPerfSRTConfig.WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
    },
    ClientGen: ParticipantConfig{
        SRT:      HighPerfSRTConfig.WithMultipleSendRings(4),
        Bitrate:  "20M",
        Duration: 10 * time.Second,
    },
    Client: ParticipantConfig{
        SRT: ControlSRTConfig,
    },
},
```

### Expected Results

| Test | Metric | Expected vs Single Ring |
|------|--------|-------------------------|
| `Isolation-5M-MultiRing2` | RTT | ~10-20% improvement |
| `Isolation-5M-MultiRing4` | RTT | ~20-30% improvement |
| `Isolation-20M-MultiRing4` | RTT | ~30-40% improvement |
| All | NAKs | Same or fewer |
| All | Packet Loss | Zero |

---

## 5.3 Parallel Test Configurations

Add parallel test configurations to compare single vs multi-ring performance:

### File: `contrib/integration_testing/config.go`

Add after existing parallel configs:

```go
// ParallelMultiRingComparison - Compare 1, 2, and 4 ring configurations
var ParallelMultiRingComparison = ParallelTestMatrix{
    Name:        "ParallelMultiRingComparison",
    Description: "Compare RTT across different io_uring ring counts",
    Tests: []ParallelTestConfig{
        {
            Name: "Parallel-20M-Ring1",
            Server: ParticipantConfig{
                SRT: HighPerfSRTConfig.WithLargeIoUringRecvRing(),
            },
            ClientGen: ParticipantConfig{
                SRT:      HighPerfSRTConfig,
                Bitrate:  "20M",
                Duration: 15 * time.Second,
            },
        },
        {
            Name: "Parallel-20M-Ring2",
            Server: ParticipantConfig{
                SRT: HighPerfSRTConfig.WithMultipleRecvRings(2).WithLargeIoUringRecvRing(),
            },
            ClientGen: ParticipantConfig{
                SRT:      HighPerfSRTConfig.WithMultipleSendRings(2),
                Bitrate:  "20M",
                Duration: 15 * time.Second,
            },
        },
        {
            Name: "Parallel-20M-Ring4",
            Server: ParticipantConfig{
                SRT: HighPerfSRTConfig.WithMultipleRecvRings(4).WithLargeIoUringRecvRing(),
            },
            ClientGen: ParticipantConfig{
                SRT:      HighPerfSRTConfig.WithMultipleSendRings(4),
                Bitrate:  "20M",
                Duration: 15 * time.Second,
            },
        },
    },
    SuccessCriteria: ParallelSuccessCriteria{
        MaxRTTIncreasePercent: 0,     // Ring2/Ring4 should be <= Ring1
        MaxNAKIncreasePercent: 10,    // Allow small NAK variance
        RequireZeroPacketLoss: true,
    },
}
```

---

## 5.4 Unit Tests and Table-Driven Tests

### New Test Files Required

| File | Purpose | Location |
|------|---------|----------|
| `listen_linux_multiring_test.go` | Listener multi-ring tests | Root package |
| `dial_linux_multiring_test.go` | Dialer multi-ring tests | Root package |
| `connection_linux_multiring_test.go` | Connection send multi-ring tests | Root package |

### Test Categories

#### 5.4.1 Ring Setup Tests (`listen_linux_multiring_test.go`)

```go
func TestMultiRing_Setup(t *testing.T) {
    testCases := []struct {
        name       string
        ringCount  int
        wantErr    bool
        errContains string
    }{
        {"single ring (default)", 1, false, ""},
        {"two rings", 2, false, ""},
        {"four rings", 4, false, ""},
        {"eight rings", 8, false, ""},
        {"sixteen rings (max)", 16, false, ""},
        {"zero rings (invalid)", 0, true, "must be at least 1"},
        {"seventeen rings (too many)", 17, true, "exceeds maximum"},
        {"negative rings", -1, true, "must be positive"},
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            cfg := gosrt.DefaultConfig()
            cfg.IoUringRecvEnabled = true
            cfg.IoUringRecvRingCount = tc.ringCount

            // Test ring initialization
            // ...
        })
    }
}
```

#### 5.4.2 Completion Handler Concurrency Tests

```go
func TestMultiRing_ConcurrentCompletion(t *testing.T) {
    testCases := []struct {
        name         string
        ringCount    int
        packetsPerRing int
        wantTotal    int
    }{
        {"single ring 1000 packets", 1, 1000, 1000},
        {"two rings 500 each", 2, 500, 1000},
        {"four rings 250 each", 4, 250, 1000},
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Verify all completions processed
            // Verify no race conditions
            // Verify packet ordering preserved per-ring
        })
    }
}
```

#### 5.4.3 Ring Distribution Tests

```go
func TestMultiRing_PacketDistribution(t *testing.T) {
    testCases := []struct {
        name        string
        ringCount   int
        totalPackets int
        // Expect ~equal distribution with some variance
        maxVariancePercent float64
    }{
        {"two rings even distribution", 2, 10000, 10.0},
        {"four rings even distribution", 4, 10000, 15.0},
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Verify kernel distributes packets across rings
            // Track packets per ring
            // Assert distribution within variance
        })
    }
}
```

#### 5.4.4 Error Handling Tests

```go
func TestMultiRing_ErrorHandling(t *testing.T) {
    testCases := []struct {
        name          string
        injectError   string
        ringToFail    int
        wantBehavior  string
    }{
        {"ring 0 CQ overflow", "cq_overflow", 0, "continue with other rings"},
        {"ring 1 submission failure", "submit_fail", 1, "retry on same ring"},
        {"all rings busy", "all_busy", -1, "backpressure to sender"},
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Inject error condition
            // Verify graceful handling
            // Verify other rings continue
        })
    }
}
```

#### 5.4.5 Benchmark Tests

```go
func BenchmarkMultiRing_Throughput(b *testing.B) {
    benchmarks := []struct {
        name      string
        ringCount int
    }{
        {"1 ring", 1},
        {"2 rings", 2},
        {"4 rings", 4},
        {"8 rings", 8},
    }

    for _, bm := range benchmarks {
        b.Run(bm.name, func(b *testing.B) {
            // Setup with ringCount
            // Measure packets/second
            // Measure latency percentiles
        })
    }
}

func BenchmarkMultiRing_Latency(b *testing.B) {
    // Same structure, measure completion latency
}
```

### Existing Tests to Update

| File | Test | Required Change |
|------|------|-----------------|
| `listen_linux_test.go` | (currently empty) | Add basic io_uring tests |
| `connection_test.go` | TestConnection_* | Add multi-ring variants |

### Test Run Commands

After implementation, verify with:

```bash
# Run all multi-ring tests
go test -v -tags linux -run "MultiRing" ./...

# Run benchmarks
go test -bench "BenchmarkMultiRing" -benchmem ./...

# Run with race detector
go test -race -tags linux -run "MultiRing" ./...

# Run integration tests
make test-isolation CONFIG=Isolation-5M-MultiRing2
make test-isolation CONFIG=Isolation-5M-MultiRing4
make test-parallel MATRIX=ParallelMultiRingComparison
```

---

## 5.5 Context Assert Analysis: Current State and Improvements

### Current State: Asserts at Sender/Receiver Level Only

The context assert infrastructure currently exists at the **sender** and **receiver** struct levels:

**Sender (`congestion/live/send/debug.go` lines 104-117):**
```go
func (s *sender) AssertEventLoopContext() {
    if !s.debug.inEventLoop.Load() {
        panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside EventLoop context", caller))
    }
}

func (s *sender) AssertTickContext() {
    if !s.debug.inTick.Load() {
        panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside Tick context", caller))
    }
}
```

**Receiver (`congestion/live/receive/debug_context.go` lines 104-117):**
```go
func (r *receiver) AssertEventLoopContext() {
    if !r.debugCtx.inEventLoop.Load() {
        panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside EventLoop context", caller))
    }
}

func (r *receiver) AssertTickContext() {
    if !r.debugCtx.inTick.Load() {
        panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside Tick context", caller))
    }
}
```

**Functions with asserts (implemented):**

| Layer | Function | Assert | File |
|-------|----------|--------|------|
| Sender | `processControlPacketsDelta()` | `AssertEventLoopContext` | `eventloop.go:213` |
| Sender | `drainRingToBtreeEventLoop()` | `AssertEventLoopContext` | `eventloop.go:266` |
| Sender | `deliverReadyPacketsEventLoop()` | `AssertEventLoopContext` | `eventloop.go:355` |
| Sender | `dropOldPacketsEventLoop()` | `AssertEventLoopContext` | `eventloop.go:486` |
| Receiver | `processOnePacket()` | `AssertEventLoopContext` | `tick.go:132` |
| Receiver | `periodicACK()` | `AssertEventLoopContext` | `ack.go:134` |
| Receiver | `periodicACKLocked()` | `AssertTickContext` | `ack.go:15` |
| Receiver | `periodicNakBtreeLocked()` | `AssertTickContext` | `nak.go:18` |

### Why Current Implementation Works Reasonably

The current design works because:

1. **EventLoop/Tick switching happens at sender/receiver level**: The `EnterEventLoop()`/`ExitEventLoop()` and `EnterTick()`/`ExitTick()` calls bracket the respective loops in `sender.EventLoop()` and `receiver.EventLoop()`.

2. **Dispatch pattern works**: Functions like `periodicACK()` vs `periodicACKLocked()` are called from the correct contexts.

3. **Release builds are no-ops**: The asserts only run in debug builds (via build tags), so no production impact.

### Gap: Connection-Level Functions Have NO Asserts

**Critical finding**: Connection-level functions are NOT protected by asserts:

| Function | Expected Assert | Actual | File |
|----------|-----------------|--------|------|
| `handleACKACK()` | `AssertEventLoopContext` | **NONE** | `connection_handlers.go:458` |
| `handleACKACKLocked()` | `AssertTickContext` | **NONE** | `connection_handlers.go:508` |
| `handleKEEPALIVE()` | `AssertEventLoopContext` | **NONE** | `connection_handlers.go` |
| `handleKEEPALIVELocked()` | `AssertTickContext` | **NONE** | `connection_handlers.go` |

**Why this matters**: The design documents (e.g., `completely_lockfree_receiver_implementation_plan.md:1006`) specified these asserts but they were **never implemented**:

```go
// PLANNED (in design doc) but NOT IMPLEMENTED:
func (c *srtConn) handleACKACK(ackNum uint32, arrivalTime time.Time) {
    c.AssertEventLoopContext()  // ← NOT IN ACTUAL CODE
    // ...
}
```

### The Bigger Problem: `srtConn` Has No Assert Methods

The `srtConn` struct doesn't even have `AssertEventLoopContext()` or `AssertTickContext()` methods! They only exist in the design document (`completely_lockfree_receiver.md:2616-2625`), not in the actual code.

### Proposed Improvement: Complete Lock-Free Assurance

To provide **complete assurance** that the EventLoop path is lock-free and Tick path always uses locking wrappers:

#### Step 1: Add Debug Context to `srtConn`

**File:** `connection.go`

```go
type srtConn struct {
    // ... existing fields ...

    // Debug context for lock-free path verification (debug builds only)
    debugCtx *connDebugContext  // nil in release builds
}

type connDebugContext struct {
    inEventLoop atomic.Bool
    inTick      atomic.Bool
}
```

#### Step 2: Add Assert Methods to `srtConn`

**File:** `connection_debug.go` (new, with `//go:build debug` tag)

```go
//go:build debug

func (c *srtConn) EnterEventLoop() {
    c.debugCtx.inEventLoop.Store(true)
}

func (c *srtConn) ExitEventLoop() {
    c.debugCtx.inEventLoop.Store(false)
}

func (c *srtConn) AssertEventLoopContext() {
    if !c.debugCtx.inEventLoop.Load() {
        panic("LOCKFREE VIOLATION: srtConn function called outside EventLoop")
    }
}

func (c *srtConn) AssertTickContext() {
    if !c.debugCtx.inTick.Load() {
        panic("LOCKFREE VIOLATION: srtConn function called outside Tick")
    }
}
```

**File:** `connection_debug_stub.go` (new, with `//go:build !debug` tag)

```go
//go:build !debug

func (c *srtConn) EnterEventLoop()           {}
func (c *srtConn) ExitEventLoop()            {}
func (c *srtConn) AssertEventLoopContext()   {}
func (c *srtConn) AssertTickContext()        {}
```

#### Step 3: Add Asserts to Connection-Level Functions

```go
// connection_handlers.go

func (c *srtConn) handleACKACK(ackNum uint32, arrivalTime time.Time) {
    c.AssertEventLoopContext()  // MUST be first line
    // ... lock-free implementation ...
}

func (c *srtConn) handleACKACKLocked(p packet.Packet) {
    c.AssertTickContext()  // MUST be first line
    c.ackLock.Lock()
    defer c.ackLock.Unlock()
    // ...
}

func (c *srtConn) handleKEEPALIVE(arrivalTime time.Time) {
    c.AssertEventLoopContext()  // MUST be first line
    // ... lock-free implementation ...
}

func (c *srtConn) handleKEEPALIVELocked(p packet.Packet) {
    c.AssertTickContext()  // MUST be first line
    // ...
}
```

#### Step 4: Coordinate Context Between Layers

The EventLoop path spans multiple layers:

```
┌─────────────────────────────────────────────────────────────────┐
│ recv.EventLoop()                                                │
│   └─ r.EnterEventLoop()                                         │
│       └─ c.EnterEventLoop()  ← NEW: Set connection context too  │
│                                                                 │
│   └─ r.processControlPackets()                                  │
│       └─ c.handleACKACK()                                       │
│           └─ c.AssertEventLoopContext() ← NEW: Verify context   │
│                                                                 │
│   └─ r.periodicACK()                                            │
│       └─ r.AssertEventLoopContext() ← Existing: Works           │
│                                                                 │
│   └─ r.ExitEventLoop()                                          │
│       └─ c.ExitEventLoop()  ← NEW: Clear connection context     │
└─────────────────────────────────────────────────────────────────┘
```

### Summary: Current vs Proposed

| Aspect | Current | Proposed |
|--------|---------|----------|
| Sender asserts | ✅ Implemented | ✅ Keep |
| Receiver asserts | ✅ Implemented | ✅ Keep |
| Connection asserts | ❌ NOT implemented | ✅ Add |
| `handlePacketDirect` mutex | ❌ Always acquired | ✅ Bypass in lock-free mode |
| Cross-layer context | ❌ Sender/receiver only | ✅ Propagate to connection |

### Implementation Priority

1. **Phase 0**: Remove `handlePacketMutex` in lock-free mode (immediate RTT improvement)
2. **Phase 0.5**: Add connection-level asserts (verify lock-free path correctness)
3. **Phase 1-5**: Multiple io_uring rings (further RTT improvement)

### Phase 0.5 Progress: Connection-Level Asserts

**Status**: ✅ Implementation Complete, All Tests Pass

**Completed**:
- ✅ Created `connection_debug.go` (debug build: context tracking + asserts)
- ✅ Created `connection_debug_stub.go` (release build: no-op stubs, zero overhead)
- ✅ Created `connection_debug_test.go` (TDD tests verifying assert behavior)
- ✅ Added `debugCtx *connDebugContext` field to `srtConn` struct
- ✅ Added asserts to connection handlers:
  - `handleACKACK()` → `AssertEventLoopContext()`
  - `handleACKACKLocked()` → `AssertTickContext()`
  - `handleKeepAliveEventLoop()` → `AssertEventLoopContext()`
  - `handleKeepAlive()` → `AssertTickContext()`
- ✅ Updated Makefile with debug test targets and documentation

**TDD Validation Results**:

The asserts correctly caught **~90 EventLoop/Tick function calls across ~15 test files** that were missing context setup:

**Sender Tests (`congestion/live/send/`):**

| File | Calls | Status |
|------|-------|--------|
| `sender_metrics_test.go` | 16 | ✅ Fixed |
| `sender_race_test.go` | 14 | ✅ Fixed |
| `sender_eventloop_integration_test.go` | 11 | ✅ Fixed |
| `sender_delivery_gap_test.go` | 8 | ✅ Fixed |
| `sender_ring_flow_table_test.go` | 7 | ✅ Fixed |
| `sender_delivery_table_test.go` | 7 | ✅ Fixed |
| `eventloop_test.go` | 6 | ✅ Already had context |
| `sender_tsbpd_table_test.go` | 2 | ✅ Fixed |
| `sender_wraparound_table_test.go` | 1 | ✅ Fixed |
| `sender_config_test.go` | 1 | ✅ Fixed |
| `sender_coverage_test.go` | 1 | ✅ Fixed |

**Receiver Tests (`congestion/live/receive/`):**

| File | Calls | Status |
|------|-------|--------|
| `ack_periodic_table_test.go` | 5 | ✅ Fixed |
| `nak_periodic_table_test.go` | 6 | ✅ Fixed |

**Test Result**: `make test-debug-quick` ✅ **ALL TESTS PASS**

**Approach**: Using DRY test helpers `runInEventLoopContext()` and `runInTickContext()` to wrap function calls.

**Test Helper Pattern** (created `test_helpers_debug.go` and `test_helpers_stub.go` in both packages):
```go
// For tests calling EventLoop-only functions directly:
runInEventLoopContext(s, func() {
    s.deliverReadyPacketsEventLoop(nowUs)
})

// For tests calling Tick/Locking wrapper functions:
runInTickContext(r, func() {
    result = r.periodicNakBtreeLocked(nowTime)
})

// For goroutines simulating the EventLoop:
go func() {
    s.EnterEventLoop()
    defer s.ExitEventLoop()
    for {
        s.drainRingToBtreeEventLoop()
        // ...
    }
}()
```

### Key Finding: No Production Code Bugs Found

**Question**: Are the asserts successfully identifying actual production code that incorrectly uses locking for the EventLoop, or not locking for the Tick-based methods?

**Answer**: **No, the production code is correctly structured.** The asserts did NOT find any bugs in production code.

**Why Production Code Is Correct**:

The production dispatch logic was already properly implemented:

| Production Entry Point | Context Setup | Location |
|------------------------|---------------|----------|
| `sender.EventLoop()` | `s.EnterEventLoop()` / `defer s.ExitEventLoop()` | `eventloop.go:58-59` |
| `sender.Tick()` | `s.EnterTick()` / `defer s.ExitTick()` | `tick.go:16-17` |
| `receiver.EventLoop()` | `r.EnterEventLoop()` / `defer r.ExitEventLoop()` | `event_loop.go:55-56` |
| `receiver.Tick()` | `r.EnterTick()` / `defer r.ExitTick()` | `tick.go:21-22` |

All function calls that flow from these entry points are automatically in the correct context because:
1. `EventLoop()` sets context at the start and clears it at the end
2. `Tick()` sets context at the start and clears it at the end
3. All internal functions are called from within these bracketed contexts

**What The Asserts Actually Caught**: ~90 instances in **test code** where tests were directly calling internal functions (like `deliverReadyPacketsEventLoop()`, `periodicNakBtreeLocked()`) without going through the proper `EventLoop()` or `Tick()` entry points. This is expected behavior for unit tests that need to test specific internal functions in isolation.

**Value of the Asserts**:
1. **Regression Prevention**: Any future code that incorrectly calls an EventLoop function from a Tick path (or vice versa) will be caught immediately in debug builds
2. **Documentation**: The asserts serve as executable documentation of which functions belong to which context
3. **Test Hygiene**: Tests now explicitly declare their context, making test intent clearer

**New Makefile Targets**:
```bash
make test-debug            # Full debug test suite (with race detector)
make test-debug-quick      # Quick debug tests (no race detector)
make test-debug-connection # Connection-level asserts only
make verify-lockfree-context # Verify debug/release builds compile
```

---

## 6. Detailed Implementation Plan

### Implementation Status Summary

| Phase | Description | Status |
|-------|-------------|--------|
| Phase 0 | Remove `handlePacketMutex` in lock-free mode | ✅ **COMPLETE** |
| Phase 0.5 | Add connection-level context asserts | ✅ **COMPLETE** |
| Phase 1 | Configuration for multi-ring | ⏳ Pending |
| Phase 2 | Listener receive multi-ring | ⏳ Pending |
| Phase 3 | Dialer receive multi-ring | ⏳ Pending |
| Phase 4 | Connection send multi-ring | ⏳ Pending |
| Phase 5 | Metrics and testing | ⏳ Pending |

### Next Steps (Recommended Order)

1. ~~**Phase 0** (CRITICAL): Implement the `handlePacketMutex` bypass in `handlePacketDirect()`~~ ✅ **DONE**

2. **Verify Phase 0**: Run `Isolation-5M-FullELLockFree` to measure RTT improvement
   - Expected: ~30% RTT reduction (215µs → ~150µs)
   - Verify: Lock wait/hold times should be 0µs in lock-free mode

3. **Phase 1**: Add configuration fields for multi-ring support
   - Required before implementing multi-ring phases

4. **Phases 2-5**: Multi-ring implementation (listener, dialer, connection, metrics)

---

### Phase 0: Remove Unnecessary Mutex (CRITICAL)

**Status**: ✅ **COMPLETE** - Implemented 2026-01-15

**Objective**: Enable true lock-free processing when EventLoop + PacketRing are enabled

**Priority**: HIGH - This should be done FIRST as it provides immediate RTT improvement

**Implementation**: Added bypass in `handlePacketDirect()` that checks `c.recv.UseEventLoop()` and skips the mutex when in lock-free mode. All downstream operations (packetRing, recvControlRing) are already lock-free.

**Files to Modify**:

| File | Function | Line | Change |
|------|----------|------|--------|
| `connection_handlers.go` | `handlePacketDirect()` | 14-40 | Add lock-free path bypass |

**Detailed Change**:

```go
// connection_handlers.go - BEFORE (lines 14-40)
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    c.handlePacketMutex.Lock()
    defer c.handlePacketMutex.Unlock()
    c.handlePacket(p)
}

// connection_handlers.go - AFTER
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    // Check if we're in completely lock-free mode
    // When both UseEventLoop and UsePacketRing are enabled:
    // - Data packets → lock-free packetRing → EventLoop drains
    // - Control packets → lock-free recvControlRing → EventLoop processes
    // No mutex needed - all downstream operations are lock-free
    if c.recv != nil && c.recv.UseEventLoop() {
        c.handlePacket(p)
        return
    }

    // Legacy path: acquire mutex for sequential processing
    if c.metrics != nil && c.metrics.HandlePacketLockTiming != nil {
        // ... existing timing code ...
    } else {
        c.handlePacketMutex.Lock()
        defer c.handlePacketMutex.Unlock()
        c.handlePacket(p)
    }
}
```

**Verification**:
1. Run `Isolation-5M-FullELLockFree` before and after
2. Expect: `handlePacketMutex` lock timing → 0 (no acquisitions)
3. Expect: RTT improvement (less queueing)

**Expected Improvement** (Phase 0 alone):
| Metric | Before | After Phase 0 |
|--------|--------|---------------|
| RTT | 215µs | ~150µs |
| Lock wait time | ~10µs avg | 0µs |
| Lock hold time | ~18µs avg | 0µs |

**Actual Results** (2026-01-15 Integration Test `Isolation-5M-FullELLockFree`):

| Metric | Control (Legacy) | Test (Lock-Free + Phase 0) |
|--------|------------------|---------------------------|
| RTT (smoothed) | 102µs | 211µs |
| RTT Raw | 113µs | 151µs |
| `handle_packet` lock wait | N/A (channel path) | **0µs** ✅ |
| `handle_packet` lock hold | N/A (channel path) | **0µs** ✅ |
| NAKs | 0 | 0 |
| Packet Loss | 0 | 0 |
| EventLoop Iterations | N/A | 49,947 |
| Control Ring Processed | N/A | 3,202 |

**Key Finding**: Phase 0 successfully bypasses the mutex (lock timing = 0). The RTT difference between control (102µs) and test (211µs) is NOT due to the mutex but due to:
1. Control uses channel-based `readfrom` path (not `handlePacketDirect`)
2. Test uses `io_uring` + `handlePacketDirect` + control ring polling
3. The control ring polling latency is the remaining bottleneck

**Conclusion**: Phase 0 works correctly. Further RTT improvement requires Phase 1-5 (multiple io_uring rings) to parallelize completion handling.

---

### Phase 0.5: Add Connection-Level Context Asserts

**Status**: ✅ **COMPLETE** - See "Phase 0.5 Progress" section in 5.5

**Objective**: Provide complete assurance that lock-free path is correct

**Priority**: MEDIUM - Should be done after Phase 0 to verify correctness

**Implementation Summary**:
- ✅ Created `connection_debug.go` (debug build: context tracking + asserts)
- ✅ Created `connection_debug_stub.go` (release build: no-op stubs)
- ✅ Created `connection_debug_test.go` (TDD tests)
- ✅ Added `debugCtx connectionDebugContext` field to `srtConn` struct
- ✅ Added asserts to connection handlers (`handleACKACK`, `handleACKACKLocked`, `handleKeepAliveEventLoop`, `handleKeepAlive`)
- ✅ Created DRY test helpers (`runInEventLoopContext`, `runInTickContext`) for sender and receiver packages
- ✅ Fixed ~90 test function calls that were missing context setup
- ✅ Updated Makefile with debug test targets

**Key Finding**: No production code bugs were found. The asserts serve as regression prevention for future development.

**Outstanding Work** (not blocking Phase 1-5):
- ⏳ Propagate context from `recv.EventLoop()` to `srtConn` (call `c.debugCtx.EnterEventLoop()` when receiver EventLoop starts)
- This is needed to fully enable connection-level asserts at runtime, but current implementation catches issues at test time

<details>
<summary>Original Implementation Plan (for reference)</summary>

**New Files to Create**:

| File | Purpose |
|------|---------|
| `connection_debug.go` | Debug build: context tracking and asserts |
| `connection_debug_stub.go` | Release build: no-op stubs |

**connection_debug.go** (with `//go:build debug` tag):
```go
//go:build debug

package srt

import (
    "fmt"
    "runtime"
    "sync/atomic"
)

type connDebugContext struct {
    inEventLoop atomic.Bool
    inTick      atomic.Bool
}

func newConnDebugContext() *connDebugContext {
    return &connDebugContext{}
}

func (c *srtConn) EnterEventLoop() {
    if c.debugCtx != nil {
        c.debugCtx.inEventLoop.Store(true)
    }
}

func (c *srtConn) ExitEventLoop() {
    if c.debugCtx != nil {
        c.debugCtx.inEventLoop.Store(false)
    }
}

func (c *srtConn) EnterTick() {
    if c.debugCtx != nil {
        c.debugCtx.inTick.Store(true)
    }
}

func (c *srtConn) ExitTick() {
    if c.debugCtx != nil {
        c.debugCtx.inTick.Store(false)
    }
}

func (c *srtConn) AssertEventLoopContext() {
    if c.debugCtx == nil || !c.debugCtx.inEventLoop.Load() {
        pc, _, _, _ := runtime.Caller(1)
        fn := runtime.FuncForPC(pc).Name()
        panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside EventLoop context", fn))
    }
}

func (c *srtConn) AssertTickContext() {
    if c.debugCtx == nil || !c.debugCtx.inTick.Load() {
        pc, _, _, _ := runtime.Caller(1)
        fn := runtime.FuncForPC(pc).Name()
        panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside Tick context", fn))
    }
}
```

**connection_debug_stub.go** (with `//go:build !debug` tag):
```go
//go:build !debug

package srt

func newConnDebugContext() *connDebugContext { return nil }
func (c *srtConn) EnterEventLoop()           {}
func (c *srtConn) ExitEventLoop()            {}
func (c *srtConn) EnterTick()                {}
func (c *srtConn) ExitTick()                 {}
func (c *srtConn) AssertEventLoopContext()   {}
func (c *srtConn) AssertTickContext()        {}

type connDebugContext struct{}
```

**Files to Modify**:

| File | Function | Line | Change |
|------|----------|------|--------|
| `connection.go` | `srtConn` struct | ~80 | Add `debugCtx *connDebugContext` field |
| `connection.go` | `newSRTConn()` | ~200 | Initialize `debugCtx: newConnDebugContext()` |
| `connection_handlers.go` | `handleACKACK()` | 458 | Add `c.AssertEventLoopContext()` |
| `connection_handlers.go` | `handleACKACKLocked()` | 508 | Add `c.AssertTickContext()` |
| `connection_handlers.go` | `handleKEEPALIVE()` | TBD | Add `c.AssertEventLoopContext()` |
| `connection_handlers.go` | `handleKEEPALIVELocked()` | TBD | Add `c.AssertTickContext()` |

**Update EventLoop to propagate context**:

| File | Function | Change |
|------|----------|--------|
| `congestion/live/receive/event_loop.go` | `EventLoop()` | Call `r.processConnectionControlPackets` callback with context |
| `connection.go` | `ProcessConnectionControlPackets` callback | Wrap with `c.EnterEventLoop()`/`c.ExitEventLoop()` |

**Verification**:
1. Run tests with `go test -tags debug ./...`
2. Any incorrect cross-context calls will panic with clear error message
3. Release builds remain unaffected (no-op stubs)

</details>

---

### Phase 1: Configuration

**Status**: ✅ **COMPLETE** (2026-01-15)

**Objective**: Add configuration fields for multi-ring support

---

#### Step 1.1: Add Config Fields

**File**: `config.go`

**Location**: Add after `IoUringRecvBatchSize` (line 257)

```go
// Number of io_uring receive rings to create (default: 1)
// Multiple rings allow parallel completion processing
// Valid values: 1-16 (power of 2 recommended)
IoUringRecvRingCount int

// Number of io_uring send rings per connection (default: 1)
// Multiple rings allow parallel send completion processing
// Valid values: 1-8 (power of 2 recommended)
IoUringSendRingCount int
```

| File | Function/Section | Line | Change |
|------|------------------|------|--------|
| `config.go` | `Config` struct | 258 | Add `IoUringRecvRingCount int` |
| `config.go` | `Config` struct | 259 | Add `IoUringSendRingCount int` |

---

#### Step 1.2: Add Default Values

**File**: `config.go`

**Location**: In `DefaultConfig()` after `IoUringRecvBatchSize: 256,` (line 658)

```go
IoUringRecvRingCount: 1,  // Default to 1 for backward compatibility
IoUringSendRingCount: 1,  // Default to 1 for backward compatibility
```

| File | Function | Line | Change |
|------|----------|------|--------|
| `config.go` | `DefaultConfig()` | 659 | Add `IoUringRecvRingCount: 1,` |
| `config.go` | `DefaultConfig()` | 660 | Add `IoUringSendRingCount: 1,` |

---

#### Step 1.3: Add CLI Flags

**File**: `contrib/common/flags.go`

**Location**: Add after `IoUringRecvBatchSize` flag (line 72)

```go
// Multiple io_uring rings configuration flags
IoUringRecvRingCount = flag.Int("iouringrecvringcount", 1, "Number of io_uring receive rings for parallel processing (1-16)")
IoUringSendRingCount = flag.Int("iouringsendringcount", 1, "Number of io_uring send rings per connection (1-8)")
```

| File | Section | Line | Change |
|------|---------|------|--------|
| `contrib/common/flags.go` | flag definitions | 73 | Add `IoUringRecvRingCount` flag |
| `contrib/common/flags.go` | flag definitions | 74 | Add `IoUringSendRingCount` flag |

---

#### Step 1.4: Add ApplyFlagsToConfig Logic

**File**: `contrib/common/flags.go`

**Location**: In `ApplyFlagsToConfig()` function, after `IoUringRecvBatchSize` handling (around line 378)

```go
if FlagSet["iouringrecvringcount"] {
    config.IoUringRecvRingCount = *IoUringRecvRingCount
}
if FlagSet["iouringsendringcount"] {
    config.IoUringSendRingCount = *IoUringSendRingCount
}
```

| File | Function | Line | Change |
|------|----------|------|--------|
| `contrib/common/flags.go` | `ApplyFlagsToConfig()` | ~379 | Add `IoUringRecvRingCount` assignment |
| `contrib/common/flags.go` | `ApplyFlagsToConfig()` | ~382 | Add `IoUringSendRingCount` assignment |

---

#### Step 1.5: Add Integration Test Config Fields

**File**: `contrib/integration_testing/config.go`

**Location**: In `SRTConfig` struct, after `IoUringRecvBatchSize` (line 410)

```go
IoUringRecvRingCount int  // -iouringrecvringcount
IoUringSendRingCount int  // -iouringsendringcount
```

| File | Struct | Line | Change |
|------|--------|------|--------|
| `contrib/integration_testing/config.go` | `SRTConfig` | 411 | Add `IoUringRecvRingCount int` |
| `contrib/integration_testing/config.go` | `SRTConfig` | 412 | Add `IoUringSendRingCount int` |

---

#### Step 1.6: Add ToCliFlags Generation

**File**: `contrib/integration_testing/config.go`

**Location**: In `ToCliFlags()` method, after `IoUringRecvBatchSize` handling (around line 566)

```go
if c.IoUringRecvRingCount > 1 {
    flags = append(flags, "-iouringrecvringcount", strconv.Itoa(c.IoUringRecvRingCount))
}
if c.IoUringSendRingCount > 1 {
    flags = append(flags, "-iouringsendringcount", strconv.Itoa(c.IoUringSendRingCount))
}
```

| File | Function | Line | Change |
|------|----------|------|--------|
| `contrib/integration_testing/config.go` | `ToCliFlags()` | ~567 | Add `IoUringRecvRingCount` flag generation |
| `contrib/integration_testing/config.go` | `ToCliFlags()` | ~570 | Add `IoUringSendRingCount` flag generation |

---

#### Step 1.7: Add Helper Methods

**File**: `contrib/integration_testing/config.go`

**Location**: After existing io_uring helpers (around line 1565)

```go
// ============================================================================
// MULTIPLE IO_URING RINGS HELPERS (Phase 1-5: Multi-Ring Architecture)
// ============================================================================

// WithMultipleRecvRings enables multiple io_uring receive rings.
// REQUIRES: IoUringRecvEnabled=true
// This enables parallel completion processing for reduced latency.
func (c SRTConfig) WithMultipleRecvRings(count int) SRTConfig {
    if !c.IoUringRecvEnabled {
        c = c.WithIoUringRecv()
    }
    if count < 1 || count > 16 {
        panic("IoUringRecvRingCount must be 1-16")
    }
    c.IoUringRecvRingCount = count
    return c
}

// WithMultipleSendRings enables multiple io_uring send rings per connection.
// REQUIRES: IoUringEnabled=true
// This enables parallel send completion processing.
func (c SRTConfig) WithMultipleSendRings(count int) SRTConfig {
    if !c.IoUringEnabled {
        c = c.WithIoUringSend()
    }
    if count < 1 || count > 8 {
        panic("IoUringSendRingCount must be 1-8")
    }
    c.IoUringSendRingCount = count
    return c
}

// WithParallelIoUring enables multiple rings for both send and receive paths.
// This is the high-performance configuration for lowest latency.
func (c SRTConfig) WithParallelIoUring(recvCount, sendCount int) SRTConfig {
    return c.WithMultipleRecvRings(recvCount).WithMultipleSendRings(sendCount)
}
```

| File | Location | Line | Change |
|------|----------|------|--------|
| `contrib/integration_testing/config.go` | After `WithIoUringRecvRingCustom()` | ~1566 | Add `WithMultipleRecvRings()` |
| `contrib/integration_testing/config.go` | After above | ~1578 | Add `WithMultipleSendRings()` |
| `contrib/integration_testing/config.go` | After above | ~1590 | Add `WithParallelIoUring()` |

---

#### Step 1.8: Verification

After completing Phase 1, run:

```bash
# Verify flags are recognized
make test-flags

# Build to verify compilation
go build ./...

# Run unit tests
go test ./... -v -run "Config"

# Verify flag help shows new options
./contrib/server/server -help 2>&1 | grep iouringrecvringcount
./contrib/client-generator/client-generator -help 2>&1 | grep iouringsendringcount
```

---

#### Phase 1 Summary Table

| Step | File | Function/Struct | Line | Change |
|------|------|-----------------|------|--------|
| 1.1 | `config.go` | `Config` struct | 258-259 | Add 2 new fields |
| 1.2 | `config.go` | `DefaultConfig()` | 659-660 | Add 2 default values |
| 1.3 | `contrib/common/flags.go` | flag vars | 73-74 | Add 2 flag definitions |
| 1.4 | `contrib/common/flags.go` | `ApplyFlagsToConfig()` | ~379-382 | Add 2 flag assignments |
| 1.5 | `contrib/integration_testing/config.go` | `SRTConfig` | 411-412 | Add 2 struct fields |
| 1.6 | `contrib/integration_testing/config.go` | `ToCliFlags()` | ~567-570 | Add 2 flag generations |
| 1.7 | `contrib/integration_testing/config.go` | helpers | ~1566+ | Add 3 helper methods |
| 1.8 | N/A | Verification | N/A | `make test-flags`, build, test |

---

#### Phase 1 Implementation Log

**Completed**: 2026-01-15

**Files Modified**:
- `config.go`: Added `IoUringRecvRingCount` and `IoUringSendRingCount` fields (lines 258-268) and defaults (lines 661-662)
- `contrib/common/flags.go`: Added CLI flags (lines 75-76) and `ApplyFlagsToConfig()` handling (lines 383-388)
- `contrib/integration_testing/config.go`: Added struct fields (lines 411-412), `ToCliFlags()` (lines 569-574), and helper methods (`WithMultipleRecvRings`, `WithMultipleSendRings`, `WithParallelIoUring`)

**Verification Results**:
```
✅ go build ./... - PASSED
✅ make test-flags - 98 passed, 0 failed
✅ server -help shows: -iouringrecvringcount int, -iouringsendringcount int
```

---

### Phase 2: Listener Receive Multi-Ring (Fully Independent)

**Status**: ⏳ **PENDING**

**Objective**: Support multiple io_uring receive rings on listener with ZERO cross-ring locking

**Key Design Principle**: Each ring is fully independent:
- Each ring has its own completion handler goroutine
- Each handler owns its completion map exclusively (no locks needed)
- Each handler resubmits ONLY to its own ring
- NO coordination between rings during normal operation

---

#### Phase 2 Architecture: Fully Independent Rings

```
                        ┌─────────────────────────────────────┐
                        │          UDP Socket (shared)        │
                        │        All rings listen on same fd  │
                        └──────────────┬──────────────────────┘
                                       │
       ┌───────────────────────────────┼───────────────────────────────┐
       │                               │                               │
       ▼                               ▼                               ▼
┌──────────────────┐         ┌──────────────────┐         ┌──────────────────┐
│   Ring 0 State   │         │   Ring 1 State   │         │   Ring N State   │
│ (Owned by H0)    │         │ (Owned by H1)    │         │ (Owned by HN)    │
├──────────────────┤         ├──────────────────┤         ├──────────────────┤
│ ring *giouring   │         │ ring *giouring   │         │ ring *giouring   │
│ completions map  │         │ completions map  │         │ completions map  │
│ requestID uint64 │         │ requestID uint64 │         │ requestID uint64 │
│ (NO LOCK!)       │         │ (NO LOCK!)       │         │ (NO LOCK!)       │
└────────┬─────────┘         └────────┬─────────┘         └────────┬─────────┘
         │                            │                            │
         ▼                            ▼                            ▼
┌──────────────────┐         ┌──────────────────┐         ┌──────────────────┐
│ Handler 0 Loop:  │         │ Handler 1 Loop:  │         │ Handler N Loop:  │
│ 1. WaitCQE       │         │ 1. WaitCQE       │         │ 1. WaitCQE       │
│ 2. Lookup(own)   │         │ 2. Lookup(own)   │         │ 2. Lookup(own)   │
│ 3. Process pkt   │         │ 3. Process pkt   │         │ 3. Process pkt   │
│ 4. Submit(own)   │         │ 4. Submit(own)   │         │ 4. Submit(own)   │
│ (self-contained) │         │ (self-contained) │         │ (self-contained) │
└──────────────────┘         └──────────────────┘         └──────────────────┘
```

**Why No Lock is Needed**:
1. Each handler goroutine exclusively owns its ring's state
2. The handler is both the producer (submit) AND consumer (complete) for its own map
3. No other goroutine ever touches that ring's completion map
4. Single-writer/single-reader on the same goroutine = no synchronization needed

---

#### Phase 2.1: New Ring State Structure

**File**: `listen_linux.go`

Create a new struct to encapsulate per-ring state (replaces scattered slice fields):

```go
// recvRingState encapsulates all state for a single io_uring receive ring.
// This struct is OWNED EXCLUSIVELY by its handler goroutine - no locks needed.
type recvRingState struct {
    ring        *giouring.Ring                  // The io_uring ring
    completions map[uint64]*recvCompletionInfo  // Completion tracking (owned)
    nextID      uint64                          // Request ID counter (owned)
    fd          int                             // Socket fd (shared, read-only)

    // NOTE: Buffers come from global sync.Pool (buffers.go), NOT per-ring pool.
    // See Section 4.5 "Buffer Pool Design Analysis" for rationale.
}

// newRecvRingState creates a new ring state.
// Buffers are obtained from global sync.Pool, not pre-allocated per-ring.
func newRecvRingState(ringSize uint32, fd int, expectedPending int) (*recvRingState, error) {
    ring := giouring.NewRing()
    if err := ring.QueueInit(ringSize, 0); err != nil {
        return nil, err
    }

    state := &recvRingState{
        ring:        ring,
        completions: make(map[uint64]*recvCompletionInfo, expectedPending),
        nextID:      0,
        fd:          fd,
    }

    return state, nil
}

// getNextID returns the next request ID (single-threaded, no atomics needed)
func (rs *recvRingState) getNextID() uint64 {
    id := rs.nextID
    rs.nextID++
    return id
}
```

---

#### Phase 2.2: Updated Listener Struct

**File**: `listen.go` (lines 156-165)

```go
// Before (scattered fields):
recvRing        interface{}
recvCompletions map[uint64]*recvCompletionInfo
recvCompLock    sync.Mutex
recvRequestID   atomic.Uint64

// After (encapsulated per-ring state):
recvRingStates []*recvRingState  // Each ring is fully independent
```

| File | Location | Change |
|------|----------|--------|
| `listen.go` | `listener` struct (~line 157) | Replace 4 fields with `recvRingStates []*recvRingState` |

---

#### Phase 2.3: Initialization (One-Time Setup)

**File**: `listen_linux.go` - `initializeIoUringRecv()` (lines 104-154)

```go
func (ln *listener) initializeIoUringRecv() error {
    if !ln.config.IoUringRecvEnabled {
        return nil
    }

    fd, err := getUDPConnFd(ln.pc)
    if err != nil {
        return fmt.Errorf("failed to extract socket fd: %w", err)
    }
    ln.recvRingFd = fd

    ringSize := uint32(512)
    if ln.config.IoUringRecvRingSize > 0 {
        ringSize = uint32(ln.config.IoUringRecvRingSize)
    }

    ringCount := ln.config.IoUringRecvRingCount
    if ringCount <= 0 {
        ringCount = 1
    }

    // Calculate initial pending requests per ring
    initialPending := int(ringSize)
    if ln.config.IoUringRecvInitialPending > 0 {
        initialPending = ln.config.IoUringRecvInitialPending
    }
    pendingPerRing := initialPending / ringCount
    if pendingPerRing < 16 {
        pendingPerRing = 16 // Minimum per ring
    }

    // Create all ring states
    ln.recvRingStates = make([]*recvRingState, ringCount)
    for i := 0; i < ringCount; i++ {
        state, err := newRecvRingState(ringSize, fd, pendingPerRing)
        if err != nil {
            // Cleanup already created rings
            for j := 0; j < i; j++ {
                ln.recvRingStates[j].ring.QueueExit()
            }
            return fmt.Errorf("failed to create ring %d: %w", i, err)
        }
        ln.recvRingStates[i] = state

        // Start handler goroutine for this ring
        ln.recvCompWg.Add(1)
        go ln.recvCompletionHandlerIndependent(ln.ctx, state)
    }

    // Pre-populate all rings with initial recv requests
    // Buffers come from global sync.Pool (buffers.go)
    for _, state := range ln.recvRingStates {
        ln.prePopulateRing(state, pendingPerRing)
    }

    ln.log("listen:io_uring:recv:init", func() string {
        return fmt.Sprintf("io_uring receive initialized: rings=%d, ring_size=%d, pending_per_ring=%d, fd=%d",
            ringCount, ringSize, pendingPerRing, ln.recvRingFd)
    })

    return nil
}
```

---

#### Phase 2.4: Fully Independent Completion Handler

**File**: `listen_linux.go` - new function

```go
// recvCompletionHandlerIndependent is a fully self-contained handler for one ring.
// It owns all state for its ring - no locks, no cross-ring coordination.
func (ln *listener) recvCompletionHandlerIndependent(ctx context.Context, state *recvRingState) {
    defer ln.recvCompWg.Done()

    ring := state.ring
    timeout := syscall.Timespec{Sec: 0, Nsec: 100_000_000} // 100ms

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        // Wait for completion (this ring only)
        cqe, err := ring.WaitCQETimeout(&timeout)
        if err != nil {
            if err == syscall.EAGAIN || err == syscall.EINTR || err == syscall.ETIME {
                continue
            }
            ln.log("listen:recv:wait:error", func() string {
                return fmt.Sprintf("WaitCQETimeout error: %v", err)
            })
            continue
        }

        // Lookup completion info (our own map, no lock!)
        requestID := cqe.UserData
        compInfo, exists := state.completions[requestID]
        if !exists {
            ln.log("listen:recv:completion:error", func() string {
                return fmt.Sprintf("completion for unknown request ID: %d", requestID)
            })
            ring.CQESeen(cqe)
            continue
        }
        delete(state.completions, requestID)

        // Process the completion
        ln.processRecvCompletionIndependent(ring, cqe, compInfo)

        // Resubmit to OUR OWN ring (no cross-ring coordination!)
        ln.submitRecvRequestToRing(state)

        ring.CQESeen(cqe)
    }
}

// submitRecvRequestToRing submits a recv request to a specific ring.
// Called by the ring's own handler - no lock needed for ring state.
func (ln *listener) submitRecvRequestToRing(state *recvRingState) {
    ring := state.ring

    sqe, err := ring.GetSQE()
    if err != nil {
        ln.incrementRecvSQEFullCount()
        return
    }

    // Get buffer from GLOBAL pool (buffers.go)
    // sync.Pool has per-P caches, so this is nearly lock-free
    bufferPtr := getBuffer()

    // Get next request ID (no atomic needed - single-threaded)
    requestID := state.getNextID()

    // Create completion info
    compInfo := &recvCompletionInfo{
        buffer: bufferPtr,
        // ... other fields
    }

    // Store in our own map (no lock needed - single owner!)
    state.completions[requestID] = compInfo

    // Prepare recvmsg
    sqe.PrepareRecvmsg(state.fd, compInfo.msg, 0)
    sqe.UserData = requestID

    // Submit
    _, err = ring.Submit()
    if err != nil {
        delete(state.completions, requestID)
        putBuffer(bufferPtr)  // Return to global pool on error
        ln.incrementRecvSQEFullCount()
    }
}

// prePopulateRing fills a specific ring with initial recv requests.
// Called during init only.
func (ln *listener) prePopulateRing(state *recvRingState, count int) {
    for i := 0; i < count; i++ {
        ln.submitRecvRequestToRing(state)
    }
}
```

---

#### Phase 2.5: Files to Modify Summary

| File | Function/Section | Line | Change |
|------|------------------|------|--------|
| `listen_linux.go` | New struct | Top | Add `recvRingState` struct |
| `listen_linux.go` | New func | Top | Add `newRecvRingState()` |
| `listen.go` | `listener` struct | 157-165 | Replace 4 fields with `recvRingStates []*recvRingState` |
| `listen_linux.go` | `initializeIoUringRecv()` | 104-154 | Create ring states, start independent handlers |
| `listen_linux.go` | New func | After init | Add `recvCompletionHandlerIndependent()` |
| `listen_linux.go` | New func | After handler | Add `submitRecvRequestToRing()` |
| `listen_linux.go` | New func | After submit | Add `prePopulateRing()` |
| `listen_linux.go` | `cleanupIoUringRecv()` | 161-215 | Iterate `recvRingStates` for cleanup |
| `listen_linux.go` | Remove | 219-346 | Delete old `submitRecvRequest()` (round-robin) |
| `listen_linux.go` | Remove | 367-384 | Delete old `lookupAndRemoveRecvCompletion()` |

---

#### Phase 2.6: What Gets Removed (Locking Code)

The following locking code is **completely removed**:

```go
// DELETED - No longer needed:
recvCompLock    sync.Mutex           // No more lock
recvCompLocks   []sync.Mutex         // No per-ring locks either!

// DELETED - Old locked functions:
func (ln *listener) submitRecvRequest() {
    ringIdx := int(ln.recvRingIdx.Add(1)-1) % len(ln.recvRings)  // GONE
    ln.recvCompLocks[ringIdx].Lock()                             // GONE
    ln.recvCompletions[ringIdx][requestID] = compInfo            // GONE
    ln.recvCompLocks[ringIdx].Unlock()                           // GONE
}

func (ln *listener) lookupAndRemoveRecvCompletion(...) {
    ln.recvCompLock.Lock()    // GONE
    // ...
    ln.recvCompLock.Unlock()  // GONE
}
```

---

#### Phase 2.7: Benefits of Fully Independent Design

| Aspect | Old Design (with locks) | New Design (independent) |
|--------|-------------------------|--------------------------|
| Locking | Per-ring locks | **NO LOCKS** |
| Map access | Lock → lookup → unlock | Direct map access |
| Request ID | `atomic.Uint64` | Plain `uint64` (single-threaded) |
| Buffer allocation | Central pool | Per-ring pool |
| Cross-ring coordination | Round-robin selector | **NONE** |
| Complexity | Scattered state | Encapsulated per ring |

---

#### Phase 2.8: Unit Tests

```go
func TestRecvRingState_Independent(t *testing.T) {
    // Test that a single ring state is fully self-contained
}

func TestRecvRingState_NoRace(t *testing.T) {
    // Run with -race to verify no data races
    // (should pass trivially - single goroutine owns each ring)
}

func TestMultipleRings_TrueParallel(t *testing.T) {
    // Verify multiple handlers process in parallel
    // No lock contention metrics (because no locks!)
}
```

---

### Phase 3: Dialer Receive Multi-Ring (Fully Independent)

**Status**: ⏳ **PENDING**

**Objective**: Support multiple io_uring receive rings on dialer with ZERO cross-ring locking

**Design**: Same pattern as listener - fully independent ring states.

---

#### Phase 3.1: New Ring State Structure

**File**: `dial_linux.go`

```go
// dialerRecvRingState encapsulates all state for a single io_uring receive ring.
// This struct is OWNED EXCLUSIVELY by its handler goroutine - no locks needed.
type dialerRecvRingState struct {
    ring        *giouring.Ring
    completions map[uint64]*recvCompletionInfo
    nextID      uint64
    fd          int
    // Buffers from global sync.Pool - see Section 4.5
}
```

---

#### Phase 3.2: Updated Dialer Struct

**File**: `dial.go`

```go
// Before:
recvRing        interface{}
recvCompletions map[uint64]*recvCompletionInfo
recvCompLock    sync.Mutex
recvRequestID   atomic.Uint64

// After:
recvRingStates []*dialerRecvRingState  // Each ring is fully independent
```

---

#### Phase 3.3: Files to Modify Summary

| File | Function/Section | Change |
|------|------------------|--------|
| `dial_linux.go` | New struct | Add `dialerRecvRingState` struct |
| `dial.go` | `dialer` struct | Replace 4 fields with `recvRingStates` |
| `dial_linux.go` | `initializeIoUringRecv()` | Create ring states, start independent handlers |
| `dial_linux.go` | New func | Add `recvCompletionHandlerIndependent()` |
| `dial_linux.go` | New func | Add `submitRecvRequestToRing()` |
| `dial_linux.go` | `cleanupIoUringRecv()` | Iterate `recvRingStates` for cleanup |
| `dial_linux.go` | Remove | Delete old locked `submitRecvRequest()` |

---

### Phase 4: Connection Send Multi-Ring (Fully Independent)

**Status**: ⏳ **PENDING**

**Objective**: Support multiple io_uring send rings per connection with ZERO cross-ring locking

---

#### Phase 4.1: Send Ring State Structure

**File**: `connection_linux.go`

```go
// sendRingState encapsulates all state for a single io_uring send ring.
// NOTE: Unlike receive, send has TWO goroutines accessing state:
// - Sender EventLoop: calls sendIoUring() → writes to completions map
// - Completion Handler: processes completions → reads/deletes from map
// Therefore, we need a per-ring lock for the completion map.
type sendRingState struct {
    ring        *giouring.Ring
    completions map[uint64]*sendCompletionInfo
    compLock    sync.Mutex    // Per-ring lock (sender writes, handler reads)
    nextID      atomic.Uint64 // Atomic: multiple goroutines may submit
    fd          int
    // Buffers from global sync.Pool - see Section 4.5
}
```

---

#### Phase 4.2: Updated Connection Struct

**File**: `connection.go`

```go
// Before:
sendRing        interface{}
sendCompletions map[uint64]*sendCompletionInfo
sendCompLock    sync.Mutex
sendRequestID   atomic.Uint64

// After:
sendRingStates []*sendRingState  // Each ring is fully independent
```

---

#### Phase 4.3: Send Ring Selection Strategy

For send, we have a choice of how to distribute packets across rings:

**Option A: Round-Robin (simple)**
```go
func (c *srtConn) getSendRing() *sendRingState {
    idx := atomic.AddUint64(&c.sendRingIdx, 1) % uint64(len(c.sendRingStates))
    return c.sendRingStates[idx]
}
```

**Option B: Per-Goroutine Affinity (better cache locality)**
```go
// Each goroutine that calls sendIoUring gets a consistent ring
// Based on goroutine ID or hash of caller
```

**Recommended**: Option A (round-robin) for simplicity. The send path is already single-threaded from the EventLoop, so ring affinity provides minimal benefit.

---

#### Phase 4.4: Files to Modify Summary

| File | Function/Section | Change |
|------|------------------|--------|
| `connection_linux.go` | New struct | Add `sendRingState` struct |
| `connection.go` | `srtConn` struct | Replace 4 fields with `sendRingStates` |
| `connection_linux.go` | `initializeIoUring()` | Create ring states, start handlers |
| `connection_linux.go` | New func | Add `sendCompletionHandlerIndependent()` |
| `connection_linux.go` | `sendIoUring()` | Select ring, submit to that ring's state (with lock) |
| `connection_linux.go` | `cleanupIoUring()` | Iterate `sendRingStates` for cleanup |
| `connection_other.go` | Stubs | No change (non-Linux) |

---

#### Phase 4.5: Send Path Considerations

Unlike receive, send has a key difference:
- Receive: Each ring's handler resubmits to its own ring (self-contained loop)
- Send: Application code calls `sendIoUring()` from various contexts

**Solution**: The send completion handler only handles completions (returns buffers to pool). It doesn't need to resubmit - new sends come from the application.

```go
func (c *srtConn) sendCompletionHandlerIndependent(ctx context.Context, state *sendRingState) {
    defer c.sendCompWg.Done()
    timeout := syscall.Timespec{Sec: 0, Nsec: 100_000_000} // 100ms

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        cqe, err := state.ring.WaitCQETimeout(&timeout)
        if err != nil {
            continue
        }

        // Lookup completion (with per-ring lock - cross-goroutine access)
        requestID := cqe.UserData
        state.compLock.Lock()
        compInfo, exists := state.completions[requestID]
        if exists {
            delete(state.completions, requestID)
        }
        state.compLock.Unlock()

        if compInfo != nil {
            // Return buffer to global pool
            putBuffer(compInfo.buffer)
        }

        state.ring.CQESeen(cqe)
    }
}
```

**Note**: The per-ring lock is held only briefly (map lookup + delete). This is acceptable for the send path which is less RTT-critical than receive.

**Accepted Approach**: Per-ring lock for send completion maps.
- This is acceptable overhead for the send path (less RTT-critical than receive)
- Receive path remains fully lock-free (critical for RTT)
- See Section 4.3 for detailed analysis

---

### Phase 5: Metrics Refactoring (Simplified)

**Status**: 🔄 **REDESIGN REQUIRED**

**Objective**: Replace legacy single-ring io_uring metrics with unified per-ring metrics. Always use per-ring counters, even when `ringCount=1`. Remove all old io_uring atomic counters.

---

#### 5.1 Design Philosophy: Unified Per-Ring Metrics

**Principle**: There is only ONE metrics structure: `IoUringRingMetrics`. This is used for ALL io_uring paths, regardless of ring count.

- **When `ringCount=1`**: Create a single-element array `[]*IoUringRingMetrics{&IoUringRingMetrics{}}`
- **When `ringCount>1`**: Create multi-element array `[]*IoUringRingMetrics{...}`

**Benefits**:
1. **Consistent code paths** - no conditional "use old vs new metrics" logic
2. **Clean implementation** - remove 33 legacy atomic counters
3. **Prometheus labels always present** - `ring="0"` for single-ring, `ring="0..N"` for multi-ring
4. **No backward compatibility burden** - breaking change is acceptable

**Prometheus Aggregation** (same for single or multi-ring):
```promql
# Total completions across all rings (works for ringCount=1 or N)
sum(gosrt_iouring_listener_recv_completion_success_total) by (instance)

# Per-ring breakdown (always has ring label)
gosrt_iouring_listener_recv_completion_success_total{ring="0"}
gosrt_iouring_listener_recv_completion_success_total{ring="1"}
```

---

#### 5.2 Legacy Metrics to REMOVE

**File**: `metrics/metrics.go`

The following 33 atomic counters will be **REMOVED** from `ConnectionMetrics`:

**Send Submission (5 counters)**:
```go
// REMOVE - lines 539-544
IoUringSendSubmitSuccess  atomic.Uint64
IoUringSendSubmitRingFull atomic.Uint64
IoUringSendSubmitError    atomic.Uint64
IoUringSendGetSQERetries  atomic.Uint64
IoUringSendSubmitRetries  atomic.Uint64
```

**Send Completion (6 counters)**:
```go
// REMOVE - lines 568-574
IoUringSendCompletionSuccess      atomic.Uint64
IoUringSendCompletionTimeout      atomic.Uint64
IoUringSendCompletionEBADF        atomic.Uint64
IoUringSendCompletionEINTR        atomic.Uint64
IoUringSendCompletionError        atomic.Uint64
IoUringSendCompletionCtxCancelled atomic.Uint64
```

**Listener Recv Submission (5 counters)**:
```go
// REMOVE - lines 546-551
IoUringListenerRecvSubmitSuccess  atomic.Uint64
IoUringListenerRecvSubmitRingFull atomic.Uint64
IoUringListenerRecvSubmitError    atomic.Uint64
IoUringListenerRecvGetSQERetries  atomic.Uint64
IoUringListenerRecvSubmitRetries  atomic.Uint64
```

**Listener Recv Completion (6 counters)**:
```go
// REMOVE - lines 576-582
IoUringListenerRecvCompletionSuccess      atomic.Uint64
IoUringListenerRecvCompletionTimeout      atomic.Uint64
IoUringListenerRecvCompletionEBADF        atomic.Uint64
IoUringListenerRecvCompletionEINTR        atomic.Uint64
IoUringListenerRecvCompletionError        atomic.Uint64
IoUringListenerRecvCompletionCtxCancelled atomic.Uint64
```

**Dialer Recv Submission (5 counters)**:
```go
// REMOVE - lines 553-558
IoUringDialerRecvSubmitSuccess  atomic.Uint64
IoUringDialerRecvSubmitRingFull atomic.Uint64
IoUringDialerRecvSubmitError    atomic.Uint64
IoUringDialerRecvGetSQERetries  atomic.Uint64
IoUringDialerRecvSubmitRetries  atomic.Uint64
```

**Dialer Recv Completion (6 counters)**:
```go
// REMOVE - lines 584-590
IoUringDialerRecvCompletionSuccess      atomic.Uint64
IoUringDialerRecvCompletionTimeout      atomic.Uint64
IoUringDialerRecvCompletionEBADF        atomic.Uint64
IoUringDialerRecvCompletionEINTR        atomic.Uint64
IoUringDialerRecvCompletionError        atomic.Uint64
IoUringDialerRecvCompletionCtxCancelled atomic.Uint64
```

**Total Removed**: 33 atomic counters

---

#### 5.3 New Per-Ring Metrics Structure

**File**: `metrics/metrics.go`

The unified per-ring metrics struct (already exists, keep as-is):

```go
// IoUringRingMetrics holds metrics for a single io_uring ring.
// Each ring has its own instance to avoid cache-line contention across cores.
type IoUringRingMetrics struct {
    // Submit metrics
    SubmitSuccess  atomic.Uint64 // Submit() succeeded
    SubmitRingFull atomic.Uint64 // GetSQE returned nil (ring full)
    SubmitError    atomic.Uint64 // Submit() failed
    GetSQERetries  atomic.Uint64 // GetSQE required retry
    SubmitRetries  atomic.Uint64 // Submit() required retry (EINTR/EAGAIN)

    // Completion metrics
    CompletionSuccess      atomic.Uint64 // WaitCQETimeout returned a completion
    CompletionTimeout      atomic.Uint64 // ETIME: timeout expired (healthy)
    CompletionEBADF        atomic.Uint64 // Ring closed (normal shutdown)
    CompletionEINTR        atomic.Uint64 // Interrupted by signal
    CompletionError        atomic.Uint64 // Other unexpected errors
    CompletionCtxCancelled atomic.Uint64 // Context cancelled (shutdown)

    // Packet processing
    PacketsProcessed atomic.Uint64 // Packets successfully processed
    BytesProcessed   atomic.Uint64 // Bytes processed
}

// NewIoUringRingMetrics creates metrics for the specified number of rings.
// ALWAYS creates the array, even for ringCount=1 (unified approach).
func NewIoUringRingMetrics(ringCount int) []*IoUringRingMetrics {
    if ringCount < 1 {
        ringCount = 1 // Minimum 1 ring
    }
    metrics := make([]*IoUringRingMetrics, ringCount)
    for i := 0; i < ringCount; i++ {
        metrics[i] = &IoUringRingMetrics{}
    }
    return metrics
}
```

**Updated ConnectionMetrics** (after removing legacy counters):

```go
type ConnectionMetrics struct {
    // ... existing non-io_uring fields ...

    // ========================================================================
    // io_uring Metrics (Unified Per-Ring - Phase 5 Refactoring)
    // ========================================================================
    // ALWAYS uses per-ring arrays, even when ringCount=1.
    // This replaces the 33 legacy single-ring atomic counters.

    // Per-ring metrics for send path (connection_linux.go)
    IoUringSendRingMetrics []*IoUringRingMetrics
    IoUringSendRingCount   int // Number of send rings

    // Per-ring metrics for dialer recv path (dial_linux.go)
    IoUringDialerRecvRingMetrics []*IoUringRingMetrics
    IoUringDialerRecvRingCount   int // Number of dialer recv rings
}
```

**Updated ListenerMetrics** (after removing legacy counters):

```go
type ListenerMetrics struct {
    // ... existing non-io_uring fields ...

    // ========================================================================
    // io_uring Metrics (Unified Per-Ring - Phase 5 Refactoring)
    // ========================================================================
    // ALWAYS uses per-ring array, even when ringCount=1.
    // Replaces legacy single-ring counters.

    IoUringRecvRingMetrics []*IoUringRingMetrics
    IoUringRecvRingCount   int // Number of listener recv rings (for gauges)
}
```

**Note**: No separate `DialerMetrics` struct needed. Dialer recv metrics are in `ConnectionMetrics.IoUringDialerRecvRingMetrics`.

---

#### 5.4 Unified Increment Pattern (No Conditionals)

With the legacy counters removed, all increment code is simple and unconditional:

```go
// BEFORE (conditional - had to check nil):
func (state *recvRingState) incrementCompletionSuccess(lm *ListenerMetrics, ringIdx int) {
    if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
        lm.IoUringRecvRingMetrics[ringIdx].CompletionSuccess.Add(1)
    } else {
        lm.IoUringListenerRecvCompletionSuccess.Add(1)  // ← OLD COUNTER (REMOVED)
    }
}

// AFTER (unconditional - always use per-ring):
func (state *recvRingState) incrementCompletionSuccess(lm *ListenerMetrics, ringIdx int) {
    lm.IoUringRecvRingMetrics[ringIdx].CompletionSuccess.Add(1)
}
```

**Benefits**:
- No conditional logic in hot path
- No need to check for nil arrays
- Single code path for all ring counts
- Simpler to reason about

---

#### 5.5 Prometheus Export Format (Unified)

**File**: `metrics/handler.go`

All io_uring metrics now use per-ring format (no legacy metrics to export):

```go
// writeListenerIoUringMetrics writes per-ring io_uring metrics for listener
// This is the ONLY way io_uring metrics are exported (no legacy fallback)
func writeListenerIoUringMetrics(b *strings.Builder, lm *ListenerMetrics) {
    if lm.IoUringRecvRingMetrics == nil {
        return // io_uring not enabled
    }

    ringCount := len(lm.IoUringRecvRingMetrics)

    // Ring count gauge (always present)
    writeGauge(b, "gosrt_iouring_listener_recv_ring_count", float64(ringCount))

    // Per-ring counters (always have ring label, even for ringCount=1)
    for ringIdx, rm := range lm.IoUringRecvRingMetrics {
        ringStr := strconv.Itoa(ringIdx)

        // Submit metrics
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_submit_success_total",
            rm.SubmitSuccess.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_submit_ring_full_total",
            rm.SubmitRingFull.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_submit_error_total",
            rm.SubmitError.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_getsqe_retries_total",
            rm.GetSQERetries.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_submit_retries_total",
            rm.SubmitRetries.Load(), "ring", ringStr)

        // Completion metrics
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_completion_success_total",
            rm.CompletionSuccess.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_completion_timeout_total",
            rm.CompletionTimeout.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_completion_ebadf_total",
            rm.CompletionEBADF.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_completion_eintr_total",
            rm.CompletionEINTR.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_completion_error_total",
            rm.CompletionError.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_completion_ctx_cancelled_total",
            rm.CompletionCtxCancelled.Load(), "ring", ringStr)

        // Packet processing
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_packets_processed_total",
            rm.PacketsProcessed.Load(), "ring", ringStr)
        writeCounterIfNonZero(b, "gosrt_iouring_listener_recv_bytes_processed_total",
            rm.BytesProcessed.Load(), "ring", ringStr)
    }
}
```

**Example Prometheus Output** (single-ring, ringCount=1):

```promql
# Single ring still uses ring label (ring="0")
gosrt_iouring_listener_recv_ring_count 1
gosrt_iouring_listener_recv_completion_success_total{ring="0"} 50000
gosrt_iouring_listener_recv_packets_processed_total{ring="0"} 50000
```

**Example Prometheus Output** (multi-ring, ringCount=4):

```promql
gosrt_iouring_listener_recv_ring_count 4
gosrt_iouring_listener_recv_completion_success_total{ring="0"} 12500
gosrt_iouring_listener_recv_completion_success_total{ring="1"} 12480
gosrt_iouring_listener_recv_completion_success_total{ring="2"} 12510
gosrt_iouring_listener_recv_completion_success_total{ring="3"} 12490
```

**Aggregation Query** (works for any ring count):

```promql
sum(gosrt_iouring_listener_recv_completion_success_total) by (instance)
```

---

#### 5.6 Complete Metrics Mapping (Legacy → Unified)

**REMOVED Legacy Metrics → NEW Per-Ring Metrics**:

| REMOVED (from `ConnectionMetrics`) | NEW (from `IoUringRingMetrics`) | Prometheus Label |
|-----------------------------------|--------------------------------|-----------------|
| `IoUringListenerRecvSubmitSuccess` | `SubmitSuccess` | `gosrt_iouring_listener_recv_submit_success_total{ring=N}` |
| `IoUringListenerRecvSubmitRingFull` | `SubmitRingFull` | `gosrt_iouring_listener_recv_submit_ring_full_total{ring=N}` |
| `IoUringListenerRecvSubmitError` | `SubmitError` | `gosrt_iouring_listener_recv_submit_error_total{ring=N}` |
| `IoUringListenerRecvGetSQERetries` | `GetSQERetries` | `gosrt_iouring_listener_recv_getsqe_retries_total{ring=N}` |
| `IoUringListenerRecvSubmitRetries` | `SubmitRetries` | `gosrt_iouring_listener_recv_submit_retries_total{ring=N}` |
| `IoUringListenerRecvCompletionSuccess` | `CompletionSuccess` | `gosrt_iouring_listener_recv_completion_success_total{ring=N}` |
| `IoUringListenerRecvCompletionTimeout` | `CompletionTimeout` | `gosrt_iouring_listener_recv_completion_timeout_total{ring=N}` |
| `IoUringListenerRecvCompletionEBADF` | `CompletionEBADF` | `gosrt_iouring_listener_recv_completion_ebadf_total{ring=N}` |
| `IoUringListenerRecvCompletionEINTR` | `CompletionEINTR` | `gosrt_iouring_listener_recv_completion_eintr_total{ring=N}` |
| `IoUringListenerRecvCompletionError` | `CompletionError` | `gosrt_iouring_listener_recv_completion_error_total{ring=N}` |
| `IoUringListenerRecvCompletionCtxCancelled` | `CompletionCtxCancelled` | `gosrt_iouring_listener_recv_completion_ctx_cancelled_total{ring=N}` |
| *(none)* | `PacketsProcessed` | `gosrt_iouring_listener_recv_packets_processed_total{ring=N}` |
| *(none)* | `BytesProcessed` | `gosrt_iouring_listener_recv_bytes_processed_total{ring=N}` |

**Same pattern for**:
- **Dialer Receive**: Replace `Listener` with `Dialer` in names, add `socket_id` label
- **Send**: Replace `ListenerRecv` with `Send` in names, add `socket_id` label

**Gauge Metrics**:

| Metric | Description |
|--------|-------------|
| `gosrt_iouring_listener_recv_ring_count` | Number of listener receive rings (always ≥1) |
| `gosrt_iouring_dialer_recv_ring_count` | Number of dialer receive rings per connection |
| `gosrt_iouring_send_ring_count` | Number of send rings per connection |

---

#### 5.7 Files to Modify

| File | Changes |
|------|---------|
| `metrics/metrics.go` | Add `IoUringRingMetrics` struct, `NewIoUringRingMetrics()`, add to `ListenerMetrics`/`ConnectionMetrics` |
| `metrics/handler.go` | Add `writePerRingMetrics()`, update export functions |
| `metrics/handler_test.go` | Update tests for unified per-ring metrics (remove old metric tests) |
| `listen_linux.go` | Update to use `IoUringRingMetrics[ringIdx]` (remove old counter increments) |
| `dial_linux.go` | Same as above |
| `connection_linux.go` | Same as above for send path |

---

#### 5.8 Handler Test Updates (Unified)

**File**: `metrics/handler_test.go`

Update tests to verify unified per-ring metrics (single-ring and multi-ring use same pattern):

```go
func TestPrometheusPerRingMetrics(t *testing.T) {
    tests := []struct {
        name      string
        ringCount int
        wantRings []string // ALL configurations have ring labels now
    }{
        // Single-ring still has ring="0" label (unified approach)
        {"single_ring", 1, []string{`ring="0"`}},
        {"two_rings", 2, []string{`ring="0"`, `ring="1"`}},
        {"four_rings", 4, []string{`ring="0"`, `ring="1"`, `ring="2"`, `ring="3"`}},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Create metrics with specified ring count
            lm := NewListenerMetrics()
            lm.IoUringRecvRingMetrics = NewIoUringRingMetrics(tt.ringCount)
            lm.IoUringRecvRingCount = tt.ringCount

            // Simulate traffic on each ring
            for i, rm := range lm.IoUringRecvRingMetrics {
                rm.CompletionSuccess.Add(uint64(100 * (i + 1)))
                rm.PacketsProcessed.Add(uint64(100 * (i + 1)))
            }

            // Export to Prometheus format
            output := exportListenerMetrics(lm)

            // Verify ALL ring labels are present (even for single-ring)
            for _, ringLabel := range tt.wantRings {
                assert.Contains(t, output, ringLabel)
            }

            // Verify ring count gauge
            assert.Contains(t, output, fmt.Sprintf("ring_count %d", tt.ringCount))

            // Verify NO legacy metrics are present
            assert.NotContains(t, output, "IoUringListenerRecvCompletionSuccess")
        })
    }
}
```

---

#### 5.9 Metrics Audit Verification

After implementation, run:

```bash
# Verify all metrics are properly defined and exported
make code-audit-metrics
```

This should verify:
1. All `IoUringRingMetrics` fields are exported
2. **No legacy io_uring counters remain** (33 counters removed)
3. Proper labeling with `ring` index for ALL ring counts
4. No orphaned or duplicate metrics

**Expected audit output**:

```
✓ gosrt_iouring_listener_recv_ring_count - gauge
✓ gosrt_iouring_listener_recv_submit_success_total{ring=N} - counter
✓ gosrt_iouring_listener_recv_completion_success_total{ring=N} - counter
✓ gosrt_iouring_listener_recv_packets_processed_total{ring=N} - counter
... (all 13 per-ring metrics × 3 paths = 39 metrics + 3 gauges)
✓ No orphaned metrics
✓ No duplicate definitions
✓ Legacy metrics removed: 33 counters
```

---

#### 5.9 Integration Test Configurations

Add to `contrib/integration_testing/config.go`:

```go
// Multi-ring test configurations
var (
    IsolationMultiRing2 = IsolationFullELLockFree.
        WithMultipleRecvRings(2).
        WithName("Isolation-5M-MultiRing2")

    IsolationMultiRing4 = IsolationFullELLockFree.
        WithMultipleRecvRings(4).
        WithName("Isolation-5M-MultiRing4")

    IsolationMultiRing4Send2 = IsolationFullELLockFree.
        WithMultipleRecvRings(4).
        WithMultipleSendRings(2).
        WithName("Isolation-5M-MultiRing4-Send2")
)
```

**Test Matrix**:

| Test Config | Recv Rings | Send Rings | Focus |
|-------------|------------|------------|-------|
| `Isolation-5M-FullELLockFree` | 1 | 1 | Baseline |
| `Isolation-5M-MultiRing2` | 2 | 1 | Basic multi-ring |
| `Isolation-5M-MultiRing4` | 4 | 1 | Full parallelism |
| `Isolation-5M-MultiRing4-Send2` | 4 | 2 | Recv + send multi |
| `Parallel-50M-MultiRing4` | 4 | 1 | High throughput |

---

#### 5.10 Validation Criteria

| Metric | Baseline (1 ring) | Target (4 rings) | Validation |
|--------|-------------------|------------------|------------|
| `completion_success_total` | N | ~N/4 per ring | Even distribution |
| `packets_processed_total` | N | ~N/4 per ring | Even distribution |
| `ring_full_total` | 0 | 0 | No ring overflow |
| RTT (raw) | 150µs | <120µs | -20% improvement |
| Completion latency variance | High | Low | More consistent |

**Distribution Check Query**:

```promql
# Check ring balance (should be ~25% each for 4 rings)
gosrt_iouring_listener_recv_completion_success_total /
  ignoring(ring) group_left sum(gosrt_iouring_listener_recv_completion_success_total)
```

---

#### 5.11 Analysis.go Updates for Multi-Ring

**File**: `contrib/integration_testing/analysis.go`

The current analysis.go has:
- `AnalyzeRTT()` - RTT smoothed vs raw comparison
- `AnalyzeEventLoopHealth()` - Ring backlog and drops
- `VerifyRateMetrics()` - Throughput validation

We need to add io_uring-specific analysis for multi-ring performance validation.

---

##### 5.11.1 New IoUringHealthResult Structure

```go
// IoUringHealthResult holds the result of io_uring multi-ring analysis
// This validates that multi-ring configuration is working correctly
type IoUringHealthResult struct {
    Passed     bool
    Applicable bool // false if io_uring is not enabled or single-ring

    // Configuration
    ListenerRecvRingCount int
    DialerRecvRingCount   int
    SendRingCount         int

    // Per-ring completion counts (listener receive)
    ListenerRecvByRing []uint64 // [ring0, ring1, ...] completion success counts
    ListenerRecvTotal  uint64   // Sum across all rings

    // Per-ring completion counts (dialer receive)
    DialerRecvByRing []uint64
    DialerRecvTotal  uint64

    // Per-ring completion counts (send)
    SendByRing []uint64
    SendTotal  uint64

    // Distribution analysis
    ListenerDistribution DistributionAnalysis // How evenly distributed
    DialerDistribution   DistributionAnalysis
    SendDistribution     DistributionAnalysis

    // Error counts per ring
    ListenerErrorsByRing []uint64
    DialerErrorsByRing   []uint64
    SendErrorsByRing     []uint64

    // Performance comparison (if baseline available)
    BaselineRTT       int64   // RTT with 1 ring (from baseline test)
    CurrentRTT        int64   // RTT with N rings
    RTTImprovement    float64 // Percentage improvement (negative = better)
    RTTImprovementAbs int64   // Absolute improvement in µs

    // Diagnostics
    Violations   []string
    Warnings     []string
    Observations []string
}

// DistributionAnalysis measures how evenly work is distributed across rings
type DistributionAnalysis struct {
    Min            uint64  // Minimum completions on any ring
    Max            uint64  // Maximum completions on any ring
    Mean           float64 // Average completions per ring
    StdDev         float64 // Standard deviation
    CoeffVariation float64 // StdDev/Mean (lower = more even, <0.1 is good)
    Imbalance      float64 // (Max-Min)/Mean (lower = more balanced, <0.2 is good)
}
```

---

##### 5.11.2 AnalyzeIoUringHealth Function

```go
// AnalyzeIoUringHealth analyzes io_uring multi-ring performance
// This is only applicable when IoUringRecvRingCount > 1 or IoUringSendRingCount > 1
func AnalyzeIoUringHealth(ts *TestMetricsTimeSeries, config *TestConfig) IoUringHealthResult {
    result := IoUringHealthResult{
        Passed:     false, // Fail-safe
        Applicable: false,
    }

    if config == nil {
        result.Passed = true // Can't validate
        return result
    }

    // Check if multi-ring is configured
    listenerRings := config.Server.SRT.IoUringRecvRingCount
    dialerRings := config.ClientGenerator.SRT.IoUringRecvRingCount
    sendRings := config.ClientGenerator.SRT.IoUringSendRingCount

    if listenerRings <= 1 && dialerRings <= 1 && sendRings <= 1 {
        result.Passed = true // Single-ring mode - skip analysis
        return result
    }

    result.Applicable = true
    result.ListenerRecvRingCount = listenerRings
    result.DialerRecvRingCount = dialerRings
    result.SendRingCount = sendRings

    // Extract per-ring metrics
    serverFinal := ts.Server.GetFinalSnapshot()
    clientGenFinal := ts.ClientGenerator.GetFinalSnapshot()

    // Extract listener receive completions by ring
    if listenerRings > 1 && serverFinal != nil {
        result.ListenerRecvByRing = extractPerRingMetrics(serverFinal,
            "gosrt_iouring_listener_recv_completion_success_total", listenerRings)
        result.ListenerRecvTotal = sumUint64(result.ListenerRecvByRing)
        result.ListenerDistribution = analyzeDistribution(result.ListenerRecvByRing)

        result.ListenerErrorsByRing = extractPerRingMetrics(serverFinal,
            "gosrt_iouring_listener_recv_completion_error_total", listenerRings)
    }

    // Extract dialer receive completions by ring
    if dialerRings > 1 && clientGenFinal != nil {
        result.DialerRecvByRing = extractPerRingMetrics(clientGenFinal,
            "gosrt_iouring_dialer_recv_completion_success_total", dialerRings)
        result.DialerRecvTotal = sumUint64(result.DialerRecvByRing)
        result.DialerDistribution = analyzeDistribution(result.DialerRecvByRing)
    }

    // Extract send completions by ring (from client-generator)
    if sendRings > 1 && clientGenFinal != nil {
        result.SendByRing = extractPerRingMetrics(clientGenFinal,
            "gosrt_iouring_send_completion_success_total", sendRings)
        result.SendTotal = sumUint64(result.SendByRing)
        result.SendDistribution = analyzeDistribution(result.SendByRing)
    }

    // Extract RTT for comparison
    if serverFinal != nil && serverFinal.Metrics != nil {
        result.CurrentRTT = int64(serverFinal.Metrics["gosrt_rtt_microseconds"])
    }

    // Generate observations and check for issues
    result.analyzeAndReport()

    return result
}

// extractPerRingMetrics extracts metrics for each ring from labeled counters
func extractPerRingMetrics(snap *MetricsSnapshot, prefix string, ringCount int) []uint64 {
    result := make([]uint64, ringCount)
    for i := 0; i < ringCount; i++ {
        // Look for metric with ring="N" label
        ringLabel := fmt.Sprintf("ring=\"%d\"", i)
        for name, value := range snap.Metrics {
            if strings.HasPrefix(name, prefix) && strings.Contains(name, ringLabel) {
                result[i] = uint64(value)
                break
            }
        }
    }
    return result
}

// analyzeDistribution computes distribution statistics
func analyzeDistribution(values []uint64) DistributionAnalysis {
    if len(values) == 0 {
        return DistributionAnalysis{}
    }

    var sum uint64
    min, max := values[0], values[0]
    for _, v := range values {
        sum += v
        if v < min {
            min = v
        }
        if v > max {
            max = v
        }
    }

    mean := float64(sum) / float64(len(values))

    // Calculate standard deviation
    var variance float64
    for _, v := range values {
        diff := float64(v) - mean
        variance += diff * diff
    }
    variance /= float64(len(values))
    stdDev := math.Sqrt(variance)

    result := DistributionAnalysis{
        Min:    min,
        Max:    max,
        Mean:   mean,
        StdDev: stdDev,
    }

    if mean > 0 {
        result.CoeffVariation = stdDev / mean
        result.Imbalance = float64(max-min) / mean
    }

    return result
}

// analyzeAndReport generates observations, warnings, and violations
func (r *IoUringHealthResult) analyzeAndReport() {
    // Check listener distribution
    if len(r.ListenerRecvByRing) > 1 {
        r.Observations = append(r.Observations,
            fmt.Sprintf("Listener recv rings: %d, total completions: %d",
                r.ListenerRecvRingCount, r.ListenerRecvTotal))

        for i, count := range r.ListenerRecvByRing {
            pct := float64(count) / float64(r.ListenerRecvTotal) * 100
            r.Observations = append(r.Observations,
                fmt.Sprintf("  Ring %d: %d (%.1f%%)", i, count, pct))
        }

        // Check for imbalance
        if r.ListenerDistribution.Imbalance > 0.5 {
            r.Warnings = append(r.Warnings,
                fmt.Sprintf("Listener ring imbalance: %.1f%% (max-min)/mean",
                    r.ListenerDistribution.Imbalance*100))
        }

        // Check for ring with zero completions (dead ring)
        for i, count := range r.ListenerRecvByRing {
            if count == 0 && r.ListenerRecvTotal > 0 {
                r.Violations = append(r.Violations,
                    fmt.Sprintf("Listener ring %d received 0 completions (dead ring)", i))
            }
        }
    }

    // Check for errors on any ring
    for i, errors := range r.ListenerErrorsByRing {
        if errors > 0 {
            r.Warnings = append(r.Warnings,
                fmt.Sprintf("Listener ring %d had %d completion errors", i, errors))
        }
    }

    // RTT observation
    if r.CurrentRTT > 0 {
        r.Observations = append(r.Observations,
            fmt.Sprintf("Current RTT: %dµs (with %d recv rings)",
                r.CurrentRTT, r.ListenerRecvRingCount))
    }

    // Pass if no violations
    r.Passed = len(r.Violations) == 0
}
```

---

##### 5.11.3 Print Function

```go
// PrintIoUringHealth prints the io_uring multi-ring analysis result
func PrintIoUringHealth(result IoUringHealthResult) {
    if !result.Applicable {
        return // Not applicable - don't print
    }

    fmt.Println("\nio_uring Multi-Ring Analysis (Phase 5):")
    if result.Passed {
        fmt.Println("  ✓ PASSED")
    } else {
        fmt.Println("  ✗ FAILED")
    }

    // Configuration
    fmt.Printf("    Listener recv rings: %d\n", result.ListenerRecvRingCount)
    if result.DialerRecvRingCount > 1 {
        fmt.Printf("    Dialer recv rings:   %d\n", result.DialerRecvRingCount)
    }
    if result.SendRingCount > 1 {
        fmt.Printf("    Send rings:          %d\n", result.SendRingCount)
    }

    // Distribution summary
    if len(result.ListenerRecvByRing) > 1 {
        fmt.Printf("    Listener completions: %d total\n", result.ListenerRecvTotal)
        fmt.Printf("    Distribution: min=%d, max=%d, CV=%.2f\n",
            result.ListenerDistribution.Min,
            result.ListenerDistribution.Max,
            result.ListenerDistribution.CoeffVariation)

        // Per-ring breakdown
        for i, count := range result.ListenerRecvByRing {
            pct := float64(count) / float64(result.ListenerRecvTotal) * 100
            fmt.Printf("      Ring %d: %6d (%.1f%%)\n", i, count, pct)
        }
    }

    // RTT
    if result.CurrentRTT > 0 {
        fmt.Printf("    RTT (smoothed): %d µs\n", result.CurrentRTT)
    }
    if result.RTTImprovement != 0 {
        fmt.Printf("    RTT improvement: %.1f%% (%+d µs vs baseline)\n",
            result.RTTImprovement, result.RTTImprovementAbs)
    }

    // Violations and warnings
    for _, v := range result.Violations {
        fmt.Printf("    ✗ VIOLATION: %s\n", v)
    }
    for _, w := range result.Warnings {
        fmt.Printf("    ⚠ WARNING: %s\n", w)
    }
}
```

---

##### 5.11.4 Integration with AnalysisResult

Update `AnalysisResult` struct:

```go
type AnalysisResult struct {
    // ... existing fields ...
    RTTAnalysis           RTTAnalysisResult
    IoUringHealth         IoUringHealthResult  // NEW: Multi-ring analysis
}
```

Update `AnalyzeTestMetrics()`:

```go
func AnalyzeTestMetrics(ts *TestMetricsTimeSeries, config *TestConfig) AnalysisResult {
    // ... existing analysis ...
    rttResult := AnalyzeRTT(ts, config)
    ioUringResult := AnalyzeIoUringHealth(ts, config)  // NEW

    return AnalysisResult{
        // ... existing fields ...
        RTTAnalysis:   rttResult,
        IoUringHealth: ioUringResult,  // NEW
    }
}
```

Update `PrintAnalysisResult()`:

```go
func PrintAnalysisResult(result AnalysisResult) {
    // ... existing prints ...
    PrintRTTAnalysis(result.RTTAnalysis)
    PrintIoUringHealth(result.IoUringHealth)  // NEW
    // ... final result ...
}
```

---

##### 5.11.5 Baseline Comparison Support

For meaningful performance comparison, add baseline tracking:

```go
// IoUringBaseline stores baseline metrics for comparison
type IoUringBaseline struct {
    TestName  string
    RingCount int
    RTT       int64
    RTTRaw    int64
    RTTVar    int64
}

// LoadIoUringBaseline loads baseline from previous single-ring test
func LoadIoUringBaseline(testName string) *IoUringBaseline {
    // Look for matching single-ring test result
    baselineFile := fmt.Sprintf("results/%s-baseline.json", testName)
    // ... load from file ...
    return nil // or loaded baseline
}

// CompareToBaseline compares current results to baseline
func (r *IoUringHealthResult) CompareToBaseline(baseline *IoUringBaseline) {
    if baseline == nil || baseline.RTT == 0 {
        return
    }

    r.BaselineRTT = baseline.RTT
    r.RTTImprovementAbs = baseline.RTT - r.CurrentRTT
    if baseline.RTT > 0 {
        r.RTTImprovement = float64(r.RTTImprovementAbs) / float64(baseline.RTT) * 100
    }

    r.Observations = append(r.Observations,
        fmt.Sprintf("Baseline RTT (%d rings): %dµs", baseline.RingCount, baseline.RTT))
    r.Observations = append(r.Observations,
        fmt.Sprintf("Improvement: %+dµs (%.1f%%)", r.RTTImprovementAbs, r.RTTImprovement))
}
```

---

##### 5.11.6 Expected Output

```
io_uring Multi-Ring Analysis (Phase 5):
  ✓ PASSED
    Listener recv rings: 4
    Listener completions: 50000 total
    Distribution: min=12200, max=12800, CV=0.02
      Ring 0:  12500 (25.0%)
      Ring 1:  12200 (24.4%)
      Ring 2:  12800 (25.6%)
      Ring 3:  12500 (25.0%)
    RTT (smoothed): 125 µs
    Baseline RTT (1 ring): 180µs
    RTT improvement: -55µs (-30.6%)
```

---

##### 5.11.7 Files to Modify

| File | Changes |
|------|---------|
| `contrib/integration_testing/analysis.go` | Add `IoUringHealthResult`, `DistributionAnalysis`, `AnalyzeIoUringHealth()`, `PrintIoUringHealth()` |
| `contrib/integration_testing/analysis.go` | Update `AnalysisResult`, `AnalyzeTestMetrics()`, `PrintAnalysisResult()` |
| `contrib/integration_testing/config.go` | Add `IoUringRecvRingCount`, `IoUringSendRingCount` to `SRTConfig` (already done in Phase 1) |

---

##### 5.11.8 Validation Criteria

| Metric | Threshold | Description |
|--------|-----------|-------------|
| **Ring Distribution CV** | < 0.10 | Coefficient of variation - rings should be balanced |
| **Ring Imbalance** | < 0.20 | (max-min)/mean < 20% |
| **Dead Rings** | = 0 | No ring should have 0 completions |
| **Ring Errors** | = 0 | No completion errors per ring |
| **RTT Improvement** | > 0% | Should be better than single-ring baseline |

---

#### 5.12 Phase 5 Implementation Steps (Simplified - Unified Approach)

This section summarizes the implementation steps for the simplified unified per-ring metrics approach.

##### Step 5.12.1: Remove Legacy Metrics from `metrics/metrics.go`

**Delete 33 atomic counters** from `ConnectionMetrics` (lines 539-590):
- Send submission: `IoUringSendSubmitSuccess` through `IoUringSendSubmitRetries` (5)
- Send completion: `IoUringSendCompletionSuccess` through `IoUringSendCompletionCtxCancelled` (6)
- Listener recv submission: `IoUringListenerRecvSubmitSuccess` through `IoUringListenerRecvSubmitRetries` (5)
- Listener recv completion: `IoUringListenerRecvCompletionSuccess` through `IoUringListenerRecvCompletionCtxCancelled` (6)
- Dialer recv submission: `IoUringDialerRecvSubmitSuccess` through `IoUringDialerRecvSubmitRetries` (5)
- Dialer recv completion: `IoUringDialerRecvCompletionSuccess` through `IoUringDialerRecvCompletionCtxCancelled` (6)

##### Step 5.12.2: Update `NewIoUringRingMetrics()` in `metrics/metrics.go`

Change from conditional to always-create:
```go
// BEFORE: Returns nil for ringCount <= 1
// AFTER: Always creates array (minimum 1 element)
func NewIoUringRingMetrics(ringCount int) []*IoUringRingMetrics {
    if ringCount < 1 {
        ringCount = 1
    }
    metrics := make([]*IoUringRingMetrics, ringCount)
    for i := 0; i < ringCount; i++ {
        metrics[i] = &IoUringRingMetrics{}
    }
    return metrics
}
```

##### Step 5.12.3: Update `ListenerMetrics` in `metrics/listener_metrics.go`

Change `IoUringRecvRingCount` from `atomic.Int64` to `int`:
```go
IoUringRecvRingMetrics []*IoUringRingMetrics
IoUringRecvRingCount   int // was atomic.Int64
```

##### Step 5.12.4: Update Prometheus Handler in `metrics/handler.go`

- **Remove** all legacy io_uring metric exports (33 lines)
- **Remove** `writeListenerPerRingMetrics()` and `writeConnectionPerRingMetrics()`
- **Replace with** `writeListenerIoUringMetrics()`, `writeDialerIoUringMetrics()`, `writeConnectionIoUringMetrics()` that ALWAYS export per-ring format

##### Step 5.12.5: Update io_uring Implementation Files

**File**: `listen_linux.go`
- Remove all `conn.metrics.IoUringListenerRecv*` increments
- Replace with `lm.IoUringRecvRingMetrics[ringIdx].*` increments
- Initialize `lm.IoUringRecvRingMetrics` in `initializeIoUringRecv()`

**File**: `dial_linux.go`
- Remove all `conn.metrics.IoUringDialerRecv*` increments
- Replace with `conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].*` increments
- Initialize metrics in `initializeIoUringRecv()`

**File**: `connection_linux.go`
- Remove all `c.metrics.IoUringSend*` increments
- Replace with `c.metrics.IoUringSendRingMetrics[ringIdx].*` increments
- Initialize metrics in `initializeIoUring()`

##### Step 5.12.6: Update Tests

**File**: `metrics/handler_test.go`
- Remove tests for legacy io_uring metrics
- Update per-ring tests to expect `ring="0"` even for single-ring
- Add test verifying NO legacy metric names in output

**File**: `metrics/listener_metrics_test.go`
- Update tests for new `IoUringRecvRingCount` type (int vs atomic)

##### Step 5.12.7: Verify with `make code-audit-metrics`

Run audit to confirm:
- Legacy counters are no longer defined
- Legacy counters are no longer incremented
- Legacy counters are no longer exported
- New per-ring counters are properly defined, used, and exported

---

## 7. Risk Analysis

### Low Risk
- **Backward Compatibility**: Default ring count = 1, no behavioral change
- **Configuration**: Simple integer config, easy to validate

### Medium Risk
- **Ordering**: Multiple rings may process packets out of order
  - **Mitigation**: SRT already handles reordering via btree
- **Mutex Contention**: `handlePacketMutex` still serializes
  - **Mitigation**: Parallelism still helps with io_uring overhead

### High Risk
- **Resource Exhaustion**: More rings = more memory, more FDs
  - **Mitigation**: Validate max ring count (16), document resource usage
- **Complexity**: More goroutines, more state to manage
  - **Mitigation**: Careful testing, gradual rollout

---

## 8. Performance Expectations

### Expected Improvements

| Metric | Current (1 ring) | Expected (4 rings) | Improvement |
|--------|------------------|-------------------|-------------|
| RTT (smoothed) | 215µs | ~150µs | -30% |
| RTT (raw/last sample) | 215µs | ~100µs | -53% |
| Burst completion latency | ~10µs | ~3µs | -70% |
| Completion throughput | ~20k/s | ~60k/s | +200% |
| RTT variance | 40µs | ~20µs | -50% |

### Theoretical Analysis

**Completion Handler Work Breakdown**:

| Operation | Time | Parallelizable? |
|-----------|------|-----------------|
| `extractAddrFromRSA()` | ~100ns | ✓ Yes |
| `packet.NewPacket()` | ~50ns | ✓ Yes |
| `p.UnmarshalZeroCopy()` | ~300ns | ✓ Yes |
| `ln.conns.Load()` | ~50ns | ✓ Yes |
| `metrics.IncrementRecv()` | ~20ns | ✓ Yes |
| Control ring push | ~100ns | ✓ Yes |
| **Subtotal (parallelizable)** | **~620ns** | |
| `handlePacketDirect()` mutex | ~500ns | ✗ No |
| **Total per packet** | **~1120ns** | |

**With N handlers**: Parallelizable work overlaps, reducing effective latency:
- 1 handler: 1120ns × packets in burst
- 4 handlers: 620ns/4 + 500ns × packets in burst = ~155ns + 500ns × burst

**RTT Impact**:

RTT is measured from when server sends ACK to when ACKACK arrives. The ACKACK path:
1. Client receives ACK (completion handler work)
2. Client sends ACKACK
3. Server receives ACKACK (completion handler work)
4. Server processes ACKACK (calculates RTT)

With multiple handlers, steps 1 and 3 complete faster → lower RTT.

### When Multi-Ring Helps Most

1. **Burst traffic**: Packets arriving in clumps (common with video/network batching)
2. **High packet rates**: 10k+ packets/second
3. **Multiple connections**: Many streams to single listener (server scenario)
4. **Control packet latency**: ACKACK, ACK need fast path through completion
5. **CPU cores available**: Multi-core systems benefit most

### When Multi-Ring Doesn't Help

1. **Steady-state low rate**: <1k packets/second with even spacing
2. **Single connection client**: Dialer with one stream (still helps during bursts)
3. **CPU-bound handling**: If `handlePacketDirect()` is the bottleneck
4. **Memory constrained**: Each ring uses additional kernel memory

### Validation Criteria for Integration Tests

#### Test: `Isolation-5M-FullELLockFree-MultiRing4`

**Success Criteria**:

| Metric | Current (1 ring) | Target (4 rings) | Pass Threshold |
|--------|------------------|------------------|----------------|
| RTT (smoothed) | 215µs | ≤170µs | ≤190µs (-12%) |
| RTT (raw) | 215µs | ≤130µs | ≤180µs (-16%) |
| RTT variance | 40µs | ≤25µs | ≤35µs |
| NAKs | 0 | 0 | 0 (no regression) |
| Gaps | 0 | 0 | 0 (no regression) |
| Drops | 0 | 0 | 0 (no regression) |

**Methodology**:
1. Run `Isolation-5M-FullELLockFree` (1 ring) as control
2. Run `Isolation-5M-FullELLockFree-MultiRing4` (4 rings) as test
3. Compare RTT metrics: expect ≥12% improvement
4. Verify no regression in data transfer metrics

#### Test: `Parallel-5M-MultiRing-1vs4`

**Success Criteria**:

| Metric | Improvement |
|--------|-------------|
| RTT reduction | ≥15% |
| RTT variance reduction | ≥25% |
| No NAK regression | 0 new NAKs |
| No drop regression | 0 new drops |

**Test Matrix**:

| Config | Rings | Expected RTT |
|--------|-------|--------------|
| Control (baseline) | 1 | ~215µs |
| Test-2Ring | 2 | ~180µs |
| Test-4Ring | 4 | ~150µs |
| Test-8Ring | 8 | ~140µs (diminishing returns) |

#### Benchmark: `BenchmarkMultiRingCompletion`

**Targets**:

| Scenario | Packets/sec (1 ring) | Packets/sec (4 rings) | Improvement |
|----------|---------------------|----------------------|-------------|
| Steady rate | ~30k | ~80k | +167% |
| Burst (100 pkts) | ~20k | ~60k | +200% |
| Micro-burst (10 pkts) | ~25k | ~70k | +180% |

### Resource Usage Impact

| Resource | 1 Ring | 4 Rings | Increase |
|----------|--------|---------|----------|
| Kernel memory | ~128KB | ~512KB | 4x |
| Goroutines | 1 | 4 | 4x |
| File descriptors | 1 | 1 | Same (shared socket) |
| CPU usage (idle) | ~0.1% | ~0.4% | 4x (negligible) |
| CPU usage (busy) | ~5% | ~8% | 1.6x (better utilization) |

---

## Appendix: File Reference

### Key Line Numbers (as of January 14, 2026)

| File | Key Lines |
|------|-----------|
| `listen.go` | struct: 156-165 |
| `listen_linux.go` | init: 104-154, handler: 828-885, submit: 219-346 |
| `dial_linux.go` | init: 21-66, handler: 522-563, submit: 126-236 |
| `connection_linux.go` | init: 44-98, handler: 393-516, send: 169-383 |
| `config.go` | io_uring config: 210-257, defaults: 647-658 |

### giouring API Reference

```go
ring := giouring.NewRing()
ring.QueueInit(size, flags)      // Create ring
ring.GetSQE()                     // Get submission entry
ring.Submit()                     // Submit to kernel
ring.WaitCQETimeout(&timeout)     // Wait for completion
ring.PeekCQE()                    // Non-blocking peek
ring.CQESeen(cqe)                 // Mark completion as processed
ring.QueueExit()                  // Destroy ring
```

