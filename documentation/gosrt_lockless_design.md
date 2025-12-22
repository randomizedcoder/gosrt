# GoSRT Lockless Design

**Status**: IN PROGRESS - Phase 3 Complete ✅
**Date**: 2025-12-19
**Last Updated**: 2025-12-22
**Related Documents**:
- [`receive_lock_contention_analysis.md`](./receive_lock_contention_analysis.md) - Lock contention evidence
- [`rate_metrics_performance_design.md`](./rate_metrics_performance_design.md) - Rate metrics migration plan
- [`IO_Uring_read_path.md`](./IO_Uring_read_path.md) - io_uring receive path implementation
- [`IO_Uring.md`](./IO_Uring.md) - io_uring send path design
- [`zero_copy_opportunities.md`](./zero_copy_opportunities.md) - Zero-copy buffer reuse design

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [Key Insight: Locks Are Unnecessary](#3-key-insight-locks-are-unnecessary)
4. [Architecture Overview](#4-architecture-overview)
5. [Component 1: Lock-Free Ring Buffer Per Connection](#5-component-1-lock-free-ring-buffer-per-connection)
6. [Component 2: Buffer Lifetime Management](#6-component-2-buffer-lifetime-management)
7. [Component 3: Processing Model Redesign](#7-component-3-processing-model-redesign)
8. [Component 4: Rate Metrics Migration to Atomics](#8-component-4-rate-metrics-migration-to-atomics)
9. [Event Loop Architecture (Recommended)](#9-event-loop-architecture-recommended)
10. [Data Flow Comparison](#10-data-flow-comparison)
11. [Feature Flags and Backwards Compatibility](#11-feature-flags-and-backwards-compatibility)
12. [Implementation Plan](#12-implementation-plan)
13. [Risk Analysis](#13-risk-analysis)
14. [Success Metrics](#14-success-metrics)

---

## 1. Executive Summary

### The Problem

Lock contention is killing GoSRT performance. The test server shows **44% CPU in `runtime.futex`** (vs 4.2% on control), with **54% of mutex profile in inlined lock operations**. This manifests as:

- RTT increased from ~1ms to ~10ms (10x degradation)
- Throughput limited at higher bitrates (75-100+ Mb/s)
- Scalability bottleneck under packet loss (more NAK operations = more locks)

### The Solution

**Eliminate locks entirely from the packet receive hot path** by introducing a lock-free ring buffer per connection combined with a **continuous event loop** architecture. The key insight is that locks are not needed if we control access patterns:

1. **io_uring completion handler** writes packets to a lock-free ring (multiple producers can write)
2. **Event loop** is the single consumer - it continuously processes packets as they arrive
3. **No concurrent access** to packet btree or NAK btree - only the event loop touches them
4. **Continuous delivery** - packets are delivered immediately when TSBPD-ready, not in batches

The event loop replaces the timer-based `Tick()` function, providing smoother packet processing and lower latency.

### Implementation Progress 🚀

| Phase | Description | Status |
|-------|-------------|--------|
| **Phase 1** | Rate Metrics Atomics | ✅ **COMPLETE** - All integration tests pass |
| **Phase 2** | Buffer Lifetime (Zero-Copy) | ✅ **COMPLETE** - Shared global pool, zero-copy |
| **Phase 3** | Lock-Free Ring Integration | ✅ **COMPLETE** - 12% lock contention reduction |
| Phase 4 | Event Loop Architecture | 🔲 Pending |
| Phase 5 | Full Integration Testing | 🔲 Pending |

See [Section 12: Implementation Plan](#12-implementation-plan) for detailed status.

### Expected Outcome

| Metric | Current | Target |
|--------|---------|--------|
| `runtime.futex` CPU | 44% | < 5% |
| RTT | 10-11ms | < 2ms |
| Throughput ceiling | ~75 Mb/s | 200+ Mb/s |
| Lock acquisitions/sec | ~4300 | 0 |

---

## 2. Problem Statement

### 2.1 Lock Contention Evidence

From `receive_lock_contention_analysis.md`:

| Metric | Control Server | Test Server | Change |
|--------|----------------|-------------|--------|
| `runtime.futex` | 4.2% | 44% | **+947%** |
| `sync.(*RWMutex).RUnlock` | 9.6% | 36.7% | **+282%** |
| `runtime.lock2` (inlined) | - | 54% | - |

### 2.2 Current Lock Hotspots

| Hotspot | Location | Impact |
|---------|----------|--------|
| **Push() rate metrics** | `receive.go:301-316` | ~4300 lock ops/sec at 50 Mb/s |
| **Nested NAK btree lock** | `receive.go:339-343` | Extended contention window |
| **Tick() delivery under lock** | `receive.go:954-971` | Blocks Push() during delivery |
| **Tick() rate stats** | `receive.go:998-1006` | Blocks Push() during calculation |

### 2.3 Root Cause: Competing Access Patterns

The current design has **two competing goroutines** accessing shared data structures:

1. **Packet arrival goroutine** (via `recv.Push()`)
   - Inserts packets into packet btree
   - Deletes from NAK btree
   - Updates rate counters

2. **Tick goroutine** (via `recv.Tick()`)
   - Iterates packet btree for ACK/NAK
   - Delivers packets to application
   - Updates rate statistics

Both require locks because they can run concurrently on the same data.

---

## 3. Key Insight: Locks Are Unnecessary

### The Realization

If we change the access pattern so that **only one goroutine ever accesses the btrees**, locks become unnecessary. The current system has two goroutines competing (Push and Tick), but if we consolidate all btree access into a **single event loop**, locks become unnecessary.

The event loop is the sole consumer of packets and the sole accessor of btrees:

1. Read packets from ring buffer
2. Insert into packet btree
3. Delete from NAK btree
4. `periodicACK()` (on timer)
5. `periodicNAK()` (on timer)
6. Packet delivery (continuous, when TSBPD ready)

Since all these operations happen in the **same goroutine**, **all btree access is single-threaded**.

### The Lock-Free Ring Buffer + Event Loop Pattern

```
io_uring completion       Lock-Free Ring            Event Loop
      (producers)              Buffer               (consumer)
          │                      │                      │
          ├── Write(pkt1) ──────►│                      │
          ├── Write(pkt2) ──────►│◄── TryRead() ───────┤
          ├── Write(pkt3) ──────►│                      │
          │                      │                      │
                                                        ▼
                                              ┌─────────────────────┐
                                              │ Continuous Loop:    │
                                              │ • TryRead() packet  │
                                              │ • Insert btree      │
                                              │ • Delete NAK btree  │
                                              │ • Deliver if ready  │
                                              │ • Check ACK timer   │
                                              │ • Check NAK timer   │
                                              └─────────────────────┘
```

**Key Properties**:
- Multiple io_uring completion handlers can write concurrently (MPSC pattern)
- Single event loop reads exclusively (single consumer)
- No locks needed for btree operations - event loop has exclusive access
- Lock-free ring uses atomic operations internally
- **Continuous processing** - packets processed immediately, not batched
- **Smooth delivery** - packets delivered when TSBPD-ready, not in bursts

---

## 4. Architecture Overview

### 4.1 High-Level Design

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Network Socket                                  │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    io_uring Receive Completion Handler                       │
│  - Deserializes packet from buffer                                          │
│  - Routes to connection via sync.Map lookup                                 │
│  - Writes packet to connection's lock-free ring (NOT btree)                 │
│  - Buffer stays with packet (lifetime extended)                             │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                     ┌────────────────┼────────────────┐
                     ▼                ▼                ▼
              ┌──────────┐     ┌──────────┐     ┌──────────┐
              │  Conn 1  │     │  Conn 2  │     │  Conn N  │
              │ LF Ring  │     │ LF Ring  │     │ LF Ring  │
              └──────────┘     └──────────┘     └──────────┘
                     │                │                │
                     ▼                ▼                ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Event Loop (Per Connection Goroutine)                    │
│                                                                             │
│  Continuous loop (not timer-triggered):                                     │
│                                                                             │
│  for {                                                                      │
│    select {                                                                 │
│      case <-ctx.Done(): return                                              │
│      case <-ackTicker.C: periodicACK()                                      │
│      case <-nakTicker.C: periodicNAK()                                      │
│      default:                                                               │
│        • TryRead() one packet from ring                                     │
│        • Insert into packet btree                                           │
│        • Delete from NAK btree                                              │
│        • Deliver ready packets (check TSBPD each iteration)                 │
│        • Adaptive sleep if idle (rate-based backoff)                        │
│    }                                                                        │
│  }                                                                          │
│                                                                             │
│  BENEFITS:                                                                  │
│  • ALL btree access is single-threaded - NO LOCKS NEEDED                    │
│  • Continuous packet delivery - not bursty                                  │
│  • Lower latency - process packets as they arrive                           │
│  • Smoother CPU utilization                                                 │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Application readQueue                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 Lock-Free Ring Library

We will use **go-lock-free-ring** library:
- Repository: https://github.com/randomizedcoder/go-lock-free-ring
- Local clone: `~/Downloads/go-lock-free-ring/`
- API Reference: `~/Downloads/go-lock-free-ring/README.md`
- Example: `~/Downloads/go-lock-free-ring/cmd/ring/ring.go`

**Key Features**:
- MPSC (Multi-Producer, Single-Consumer) design
- Sharded architecture reduces producer contention
- Zero-allocation steady-state with `sync.Pool` integration
- Tested up to 400 Mb/s sustained throughput
- Lock-free using atomic operations
- **Built-in backoff mechanism** for handling full ring conditions

**Write Backoff Mechanism**:

The library provides `WriteWithBackoff()` which handles contention when the ring is full:

1. **Retry loop**: Attempts `MaxRetries` writes before sleeping (default: 10)
2. **Backoff sleep**: If all retries fail, sleeps for `BackoffDuration` (default: 100µs)
3. **Max backoffs**: Optionally limits total backoff attempts with `MaxBackoffs` (default: 0 = unlimited)
4. **Returns false**: If `MaxBackoffs` is exceeded, write fails (packet dropped)

This backoff helps the producer (io_uring completion handler) handle temporary bursts when the consumer (event loop) falls behind, reducing CPU spinning and allowing the consumer to catch up.

**Write Methods**:
- `Write()` - Non-blocking, returns false immediately if shard is full
- `WriteWithBackoff()` - Retries with configurable backoff before failing

---

## 5. Component 1: Lock-Free Ring Buffer Per Connection

### 5.1 Overview

Each SRT connection will have its own lock-free ring buffer. This ring accumulates incoming packets between tick intervals, allowing high-speed packet arrival without lock contention.

### 5.2 Ring Configuration

The ring buffer will be configurable via `config.go`, following the existing pattern for io_uring and queue size configuration.

#### Config Struct Additions (`config.go`)

```go
// --- Lock-Free Ring Configuration ---

// PacketRingSize is the total capacity of the lock-free ring buffer per connection
// Used to buffer packets between io_uring arrival and event loop processing
// Default: 1000. Must be power of 2. Range: 64-8192.
PacketRingSize int

// PacketRingShards is the number of shards in the lock-free ring buffer
// More shards reduce producer contention but increase memory and consumer polling
// Default: 4. Must be power of 2. Range: 1-16.
PacketRingShards int

// --- Ring Write Backoff Configuration ---
// These control how WriteWithBackoff() behaves when the ring is full

// PacketRingMaxRetries is the number of write attempts before sleeping
// Higher values reduce latency spikes but increase CPU usage during contention
// Default: 10. Range: 1-100.
PacketRingMaxRetries int

// PacketRingBackoffDuration is how long to sleep between retry batches
// Allows consumer to catch up during bursts
// Default: 100µs. Range: 10µs-10ms.
PacketRingBackoffDuration time.Duration

// PacketRingMaxBackoffs limits total backoff attempts before dropping packet
// 0 = unlimited (keep retrying until success)
// Non-zero = fail after this many backoffs (packet dropped, logged)
// Default: 0 (unlimited). Range: 0-1000.
PacketRingMaxBackoffs int
```

#### Default Values (`config.go`)

```go
var defaultConfig Config = Config{
    // ... existing defaults ...

    // Lock-free ring defaults
    PacketRingSize:            1024,                      // 1024 packets total capacity (power of 2)
    PacketRingShards:          4,                         // 4 shards (256 packets per shard)
    PacketRingMaxRetries:      10,                        // 10 attempts before sleeping
    PacketRingBackoffDuration: 100 * time.Microsecond,   // 100µs sleep between retries
    PacketRingMaxBackoffs:     0,                         // Unlimited backoffs (never drop)
}
```

#### Validation (`config.go` Validate())

```go
// Validate lock-free ring configuration
if c.PacketRingSize > 0 {
    if c.PacketRingSize&(c.PacketRingSize-1) != 0 {
        return fmt.Errorf("config: PacketRingSize must be a power of 2")
    }
    if c.PacketRingSize < 64 || c.PacketRingSize > 8192 {
        return fmt.Errorf("config: PacketRingSize must be between 64 and 8192")
    }
}
if c.PacketRingShards > 0 {
    if c.PacketRingShards&(c.PacketRingShards-1) != 0 {
        return fmt.Errorf("config: PacketRingShards must be a power of 2")
    }
    if c.PacketRingShards < 1 || c.PacketRingShards > 16 {
        return fmt.Errorf("config: PacketRingShards must be between 1 and 16")
    }
}

// Validate ring backoff configuration
if c.PacketRingMaxRetries < 1 || c.PacketRingMaxRetries > 100 {
    return fmt.Errorf("config: PacketRingMaxRetries must be between 1 and 100")
}
if c.PacketRingBackoffDuration < 10*time.Microsecond || c.PacketRingBackoffDuration > 10*time.Millisecond {
    return fmt.Errorf("config: PacketRingBackoffDuration must be between 10µs and 10ms")
}
if c.PacketRingMaxBackoffs < 0 || c.PacketRingMaxBackoffs > 1000 {
    return fmt.Errorf("config: PacketRingMaxBackoffs must be between 0 and 1000")
}
```

#### Default Values Table

| Parameter | Default | Range | Rationale |
|-----------|---------|-------|-----------|
| PacketRingSize | 1024 | 64-8192 | Power of 2, sufficient for 10ms at 100 Mb/s |
| PacketRingShards | 4 | 1-16 | Balance contention vs memory (power of 2) |
| Per-Shard | 256 | (calculated) | PacketRingSize / PacketRingShards |
| PacketRingMaxRetries | 10 | 1-100 | Library default, balances latency/CPU |
| PacketRingBackoffDuration | 100µs | 10µs-10ms | Library default, allows consumer catchup |
| PacketRingMaxBackoffs | 0 | 0-1000 | Unlimited by default (never drop) |

**Sizing Calculation**:
```
At 100 Mb/s with 1450-byte packets:
- Packets/second = (100 * 1,000,000) / (8 * 1450) = ~8,620 packets/sec
- Packets per 10ms tick = 86 packets
- With 2x safety margin = 172 packets
- 1000 provides ~5x headroom for bursts
```

**Backoff Tuning**:
```
Default behavior (MaxBackoffs=0):
- Try 10 writes, sleep 100µs, repeat forever
- Never drops packets, but may block io_uring completion handler

Conservative (MaxBackoffs=10):
- Try 10 writes, sleep 100µs, repeat up to 10 times
- Total max wait: 10 * 100µs = 1ms before drop
- Protects io_uring completion handler from blocking too long
```

### 5.3 Data Structure Changes

**receiver struct additions** (`congestion/live/receive.go`):

```go
import (
    "time"
    ring "github.com/randomizedcoder/go-lock-free-ring"
)

type receiver struct {
    // ... existing fields ...

    // Lock-free ring buffer for incoming packets
    packetRing *ring.ShardedRing

    // Write configuration for backoff behavior
    writeConfig ring.WriteConfig

    // Reference to recvBufferPool for returning data buffers
    // Passed from connection during receiver creation
    bufferPool *sync.Pool

    // Configuration from config.go
    config ReceiverConfig

    // Function dispatch - set during initialization based on config
    // This pattern is used throughout gosrt for feature selection (io_uring, btree, etc.)
    pushFn func(pkt *packet.Packet)  // Points to pushToRing or pushWithLock

    // Lock for legacy path (UsePacketRing=false)
    // When UsePacketRing=true, this lock is not used - event loop has exclusive access
    mu sync.RWMutex
}

// ReceiverConfig holds configuration passed to the receiver
type ReceiverConfig struct {
    // Feature flags (from config.go)
    UsePacketRing bool  // Enable lock-free ring buffer
    UseEventLoop  bool  // Enable continuous event loop (requires UsePacketRing)

    // Ring buffer sizing (only used when UsePacketRing=true)
    PacketRingSize   int
    PacketRingShards int

    // Ring write backoff configuration
    PacketRingMaxRetries      int
    PacketRingBackoffDuration time.Duration
    PacketRingMaxBackoffs     int

    // Timer intervals (all time.Duration for consistency)
    ACKInterval  time.Duration  // Periodic ACK interval (default: 10ms)
    NAKInterval  time.Duration  // Periodic NAK interval (default: 20ms)
    RateInterval time.Duration  // Rate calculation period (default: 1s)
    TickInterval time.Duration  // Tick loop interval (only when UseEventLoop=false)

    // ... other config fields ...
}
```

### 5.4 Ring Initialization and Function Dispatch Setup

**In NewReceiver()** (`congestion/live/receive.go`):

```go
func NewReceiver(config ReceiverConfig, bufferPool *sync.Pool /* other params */) *receiver {
    r := &receiver{
        bufferPool: bufferPool,
        config:     config,
        // ... other initialization ...
    }

    // === Ring Buffer Initialization (only if UsePacketRing=true) ===
    if config.UsePacketRing {
        ringSize := uint64(config.PacketRingSize)
        numShards := uint64(config.PacketRingShards)

        // Apply defaults if not configured
        if ringSize == 0 {
            ringSize = 1000
        }
        if numShards == 0 {
            numShards = 4
        }

        packetRing, err := ring.NewShardedRing(ringSize, numShards)
        if err != nil {
            panic(fmt.Sprintf("failed to create packet ring: %v", err))
        }
        r.packetRing = packetRing

        // Build write config for backoff behavior
        maxRetries := config.PacketRingMaxRetries
        if maxRetries == 0 {
            maxRetries = 10  // Library default
        }
        backoffDuration := config.PacketRingBackoffDuration
        if backoffDuration == 0 {
            backoffDuration = 100 * time.Microsecond
        }
        r.writeConfig = ring.WriteConfig{
            MaxRetries:      maxRetries,
            BackoffDuration: backoffDuration,
            MaxBackoffs:     config.PacketRingMaxBackoffs,
        }
    }

    // === Function Dispatch Setup ===
    // Select Push implementation based on UsePacketRing flag
    // This pattern is consistent with io_uring and btree feature selection in gosrt
    if config.UsePacketRing {
        r.pushFn = r.pushToRing      // NEW: Lock-free ring path
    } else {
        r.pushFn = r.pushWithLock    // LEGACY: Direct btree with locks
    }

    return r
}
```

**Function Dispatch Pattern**: This is the same pattern used throughout gosrt for feature selection:
- **io_uring**: Function dispatch selects between io_uring and standard syscall paths
- **btree**: Function dispatch selects between btree and linked-list packet stores
- **packet ring**: Function dispatch selects between lock-free ring and direct locked insert

**Note:** The `bufferPool` is the same `recvBufferPool` used by the listener/dialer for io_uring receives. It's passed through the connection to the receiver so that `releasePacketFully()` can return data buffers to the correct pool.

**WriteConfig Usage**: The `writeConfig` is stored in the receiver and passed to `WriteWithBackoff()` in the `pushToRing()` method (see Section 5.5.3).

### 5.5 Packet Arrival (Write to Ring or Direct Insert)

The `Push()` method in `congestion/live/receive.go` is the entry point for all received packets. Using **function dispatch** (similar to io_uring and btree feature selection), the implementation branches based on the `UsePacketRing` configuration flag.

#### 5.5.1 Function Dispatch Pattern

**File**: `congestion/live/receive.go`

```go
// receiver struct with function dispatch
type receiver struct {
    // ... existing fields ...

    // Function dispatch - set during initialization based on config
    pushFn func(pkt *packet.Packet)  // Points to pushToRing or pushWithLock
}

// NewReceiver sets up function dispatch based on config
func NewReceiver(config ReceiverConfig, /* other params */) *receiver {
    r := &receiver{
        // ... initialization ...
    }

    // Function dispatch: select Push implementation based on config
    if config.UsePacketRing {
        r.pushFn = r.pushToRing      // NEW: Lock-free ring path
    } else {
        r.pushFn = r.pushWithLock    // LEGACY: Direct btree insert with locks
    }

    return r
}

// Push dispatches to the configured implementation
func (r *receiver) Push(pkt *packet.Packet) {
    // Atomic rate counter updates (always - no flag needed, see 11.3.1)
    r.nPackets.Add(1)
    r.ratePackets.Add(1)
    r.rateBytes.Add(uint64(pkt.Len()))
    if pkt.Header().RetransmittedPacketFlag {
        r.rateBytesRetrans.Add(uint64(pkt.Len()))
    }

    // Dispatch to configured implementation
    r.pushFn(pkt)
}
```

#### 5.5.2 Legacy Path: pushWithLock (UsePacketRing=false)

**File**: `congestion/live/receive.go`

This is the existing implementation with locks. Used when `UsePacketRing=false` (default for backwards compatibility).

```go
// pushWithLock is the LEGACY implementation with mutex locking.
// Used when UsePacketRing=false for backwards compatibility.
func (r *receiver) pushWithLock(pkt *packet.Packet) {
    r.mu.Lock()
    defer r.mu.Unlock()

    seq := pkt.Header().PacketSequenceNumber

    // Initialize sequence tracking on first packet
    if r.initialSequenceNumber == nil {
        seqCopy := seq
        r.initialSequenceNumber = &seqCopy
    }

    // Check for duplicates
    if r.packetStore.Has(seq) {
        r.releasePacketFully(pkt)
        return
    }

    // Check if packet is too old (already delivered)
    if seq.Lt(r.deliveryBase) {
        r.releasePacketFully(pkt)
        return
    }

    // Insert into packet btree (UNDER LOCK)
    r.packetStore.Insert(pkt)

    // Delete from NAK btree (UNDER LOCK)
    if r.nakBtree != nil {
        r.nakBtree.Delete(seq)
    }

    // Update tracking
    r.lastPacketArrivalTime.Store(uint64(time.Now().UnixMicro()))
    r.lastDataPacketSeq.Store(uint64(seq.Val()))
}
```

#### 5.5.3 New Path: pushToRing (UsePacketRing=true)

**File**: `congestion/live/receive.go`

This is the new lock-free implementation. Used when `UsePacketRing=true`.

```go
// pushToRing is the NEW lock-free implementation.
// Used when UsePacketRing=true for lockless architecture.
// Packets are buffered in the ring and processed by the event loop.
func (r *receiver) pushToRing(pkt *packet.Packet) {
    // producerID distributes packets across shards based on sequence number
    producerID := uint64(pkt.Header().PacketSequenceNumber.Val())

    // WriteWithBackoff: retries MaxRetries times, then sleeps BackoffDuration,
    // repeats up to MaxBackoffs times (0 = unlimited)
    if !r.packetRing.WriteWithBackoff(producerID, pkt, r.writeConfig) {
        // Ring still full after all retries and backoffs - packet dropped
        // This only happens if MaxBackoffs > 0 and was exceeded
        r.droppedPackets.Add(1)

        // Log the drop (rate-limited to avoid log spam)
        if r.droppedPackets.Load() % 100 == 1 {
            log.Printf("packet ring full, dropped packet seq=%d (total drops=%d)",
                pkt.Header().PacketSequenceNumber.Val(), r.droppedPackets.Load())
        }

        // Return buffer and packet to pools immediately
        r.releasePacketFully(pkt)
        return
    }

    // Packet successfully queued for processing
    // Buffer and packet lifetimes are extended until event loop delivers
}
```

#### 5.5.4 Write Behavior Summary (Ring Path)

| Config | Behavior | Use Case |
|--------|----------|----------|
| MaxBackoffs=0 (default) | Retry forever until success | Never drop, but may block io_uring handler |
| MaxBackoffs=10 | Drop after ~1ms total wait | Protect io_uring handler, accept rare drops |
| MaxRetries=1, BackoffDuration=1ms | Quick backoff, slower retry | Lower CPU, higher latency |
| MaxRetries=100, BackoffDuration=10µs | Aggressive retry | Lower latency, higher CPU |

#### 5.5.5 Configuration Impact

| UsePacketRing | Push Behavior | Consumer |
|---------------|---------------|----------|
| false (default) | `pushWithLock()` - direct btree insert with mutex | `tickLoop()` with locks |
| true | `pushToRing()` - lock-free ring write | `eventLoop()` or `tickLoop()` |

See **Section 11.4.1** for the complete feature flag design.

### 5.6 Packet Processing (Consumer Side)

The consumer side processes packets from the ring buffer (when `UsePacketRing=true`) or directly from btree (when `UsePacketRing=false`). Using **function dispatch** (similar to the producer side), the implementation branches based on `UseEventLoop` configuration.

#### 5.6.1 Consumer Loop Function Dispatch

**File**: `srt/connection.go` (or `congestion/live/receive.go`)

```go
// Connection.startReceiver sets up the receiver processing loop
func (c *Connection) startReceiver() {
    // Function dispatch based on config flags
    if c.config.UseEventLoop {
        // NEW: Continuous event loop (requires UsePacketRing=true)
        go c.receiver.eventLoop(c.ctx)
    } else {
        // LEGACY: Timer-based tick loop
        go c.receiver.tickLoop(c.ctx)
    }
}
```

#### 5.6.2 Legacy Path: tickLoop (UseEventLoop=false)

**File**: `congestion/live/receive.go`

The legacy tick loop uses a timer to periodically call `Tick()`. Works with both `UsePacketRing=true` (drains ring first) and `UsePacketRing=false` (direct btree access with locks).

```go
// tickLoop is the LEGACY timer-based processing loop.
// Used when UseEventLoop=false.
func (r *receiver) tickLoop(ctx context.Context) {
    ticker := time.NewTicker(r.config.TickInterval)  // Default: 10ms
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.Tick(time.Now())
        }
    }
}

// Tick processes packets - branches based on UsePacketRing flag
func (r *receiver) Tick(now time.Time) {
    if r.config.UsePacketRing {
        // Ring path: drain ring first (no locks)
        r.drainPacketRing()
        // Then process without locks - we own the btrees
        r.periodicACKNoLock(now)
        r.periodicNAKNoLock(now)
        r.deliverPacketsNoLock(now)
    } else {
        // Legacy path: all operations under locks
        r.periodicACK(now)      // Acquires locks internally
        r.periodicNAK(now)      // Acquires locks internally
        r.deliverPackets(now)   // Acquires locks internally
    }

    // Rate stats always use atomics (no lock needed - see 11.3.1)
    r.updateRateStats(now)
}
```

#### 5.6.3 New Path: eventLoop (UseEventLoop=true)

**File**: `congestion/live/receive.go`

The event loop continuously processes packets as they arrive. **Requires `UsePacketRing=true`** (validated in config).

```go
// eventLoop is the NEW continuous processing loop.
// Used when UseEventLoop=true (requires UsePacketRing=true).
// Provides lower latency and smoother CPU utilization.
func (r *receiver) eventLoop(ctx context.Context) {
    // Offset tickers to spread work (see 9.3.1)
    ackTicker := time.NewTicker(ackInterval)
    time.Sleep(ackInterval / 2)
    nakTicker := time.NewTicker(nakInterval)
    defer ackTicker.Stop()
    defer nakTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ackTicker.C:
            r.periodicACKNoLock(time.Now())  // No lock - we own btrees
        case <-nakTicker.C:
            r.periodicNAKNoLock(time.Now())  // No lock - we own btrees
        default:
            // Process one packet from ring (continuous, not batched)
            processed := r.processOnePacket()

            // Deliver any ready packets (check TSBPD every iteration)
            delivered := r.deliverReadyPackets()

            // Adaptive sleep when idle (see 9.7)
            if !processed && delivered == 0 {
                time.Sleep(r.backoff.getSleepDuration())
            }
        }
    }
}

// processOnePacket reads a single packet from the ring and processes it.
// Returns true if a packet was processed, false if ring was empty.
// NO LOCKS - event loop has exclusive btree access.
func (r *receiver) processOnePacket() bool {
    item, ok := r.packetRing.TryRead()
    if !ok {
        return false  // Ring empty
    }

    pkt := item.(*packet.Packet)
    seq := pkt.Header().PacketSequenceNumber

    // Initialize sequence tracking on first packet
    if r.initialSequenceNumber == nil {
        seqCopy := seq
        r.initialSequenceNumber = &seqCopy
    }

    // Check for duplicates
    if r.packetStore.Has(seq) {
        r.releasePacketFully(pkt)
        return true  // Still "processed" (rejected duplicate)
    }

    // Check if packet is too old
    if seq.Lt(r.deliveryBase) {
        r.releasePacketFully(pkt)
        return true
    }

    // Insert into packet btree (NO LOCK)
    r.packetStore.Insert(pkt)

    // Delete from NAK btree (NO LOCK)
    if r.nakBtree != nil {
        r.nakBtree.Delete(seq)
    }

    // Update tracking
    r.lastPacketArrivalTime.Store(uint64(time.Now().UnixMicro()))
    r.lastDataPacketSeq.Store(uint64(seq.Val()))

    return true
}
```

#### 5.6.4 Configuration Impact

| UsePacketRing | UseEventLoop | Processing Model | Lock Behavior |
|---------------|--------------|------------------|---------------|
| false | false | `tickLoop()` → `Tick()` with locks | All ops under mutex |
| true | false | `tickLoop()` → `drainPacketRing()` + ops | No locks (drain first) |
| true | true | `eventLoop()` continuous | No locks (single owner) |
| false | true | **INVALID** - config validation fails | N/A |

See **Section 11.4.2** for the complete event loop feature flag design.

#### 5.6.5 Why Single Packet Processing Over Batch?

| Aspect | Batch (tickLoop) | Single (eventLoop) |
|--------|------------------|---------------------|
| Latency | Waits for 10ms timer | Immediate processing |
| CPU usage | Bursty (all at once) | Smooth (continuous) |
| Delivery | Batched every tick | As soon as TSBPD ready |
| Complexity | `drainPacketRing()` + loop | `TryRead()` single call |

The event loop processes packets **as they arrive** rather than waiting for a timer, resulting in lower latency and smoother CPU utilization.

#### 5.6.6 Design Note: lastDataPacketSeq and Packet Ordering

The `lastDataPacketSeq` field is used by the **FastNAK** feature (see `nak_btree_implementation.md`). With single-packet processing, we update this for each packet rather than tracking across a batch.

**Why this is acceptable:**

1. **FastNAK purpose**: `lastDataPacketSeq` is used to detect sequence "jumps" after a silent period (e.g., Starlink ~60ms outages). The FastNAK feature triggers when:
   - No packets arrived for `FastNakThresholdMs` (default 50ms)
   - Then a packet arrives with a sequence number much higher than expected

2. **Per-packet updates**: With the event loop, we update `lastDataPacketSeq` for every packet. Out-of-order arrivals mean the sequence may not be strictly increasing, but that's fine for FastNAK's gap detection.

---

## 6. Component 2: Buffer Lifetime Management

### 6.1 Overview

Currently, both the io_uring and standard receive paths use a `sync.Pool` for receive buffers. Buffers are returned to the pool immediately after packet deserialization, requiring a **data copy** into the packet structure. By **extending buffer lifetime** until the packet is delivered, we can eliminate this copy entirely.

**Key Insight**: This optimization is **orthogonal** to the lockless design - it benefits ALL implementations:
- io_uring receive path AND standard (syscall) receive path
- Linked list packet store AND btree packet store
- Timer-based Tick() AND event loop processing
- With or without lock-free ring buffer

This is a **universal performance improvement** that reduces memory pressure and eliminates wasteful copying for every implementation path.

This component builds on the concepts in [`zero_copy_opportunities.md`](./zero_copy_opportunities.md), which describes the broader zero-copy architecture.

### 6.2 Applicability Matrix

| Receive Path | Packet Store | Processing Model | Benefits from Zero-Copy? |
|--------------|--------------|------------------|--------------------------|
| io_uring | btree | Event loop | **YES** |
| io_uring | btree | Tick() | **YES** |
| io_uring | linked list | Tick() | **YES** |
| Standard recv() | btree | Event loop | **YES** |
| Standard recv() | btree | Tick() | **YES** |
| Standard recv() | linked list | Tick() | **YES** |

**All paths benefit equally** - the buffer lifetime extension is independent of storage and processing mechanisms.

### 6.3 Two sync.Pools: Data Buffer and Packet Structure

**Critical insight**: The design uses **two separate sync.Pools** to minimize GC pressure:

1. **`recvBufferPool`** - Pool of `*[]byte` data buffers
   - Created at listener/dialer level (shared across connections)
   - Buffer size = config.MSS (typically 1500 bytes)
   - Holds the raw packet data received from the network

2. **`packetPool`** - Pool of `packet.Packet` structures
   - Existing pattern in gosrt (see `Decommission()` throughout codebase)
   - Reuses the packet struct itself, not just the payload
   - Returned via `p.Decommission()` after packet is fully processed

### 6.4 Current Buffer Flow (Problem)

```
io_uring completion:
1. Get data buffer from recvBufferPool (sync.Pool)
2. Kernel fills buffer with packet data
3. Get packet struct from packetPool (sync.Pool)
4. Deserialize: COPY data from buffer into packet.Payload  ← WASTEFUL
5. Reset() and Put() buffer back to recvBufferPool  ← TOO EARLY
6. Route packet to connection
```

The copy in step 4 is wasteful and adds latency.

### 6.5 New Buffer Flow (Solution)

```
io_uring completion:
1. Get data buffer from recvBufferPool (sync.Pool)
2. Kernel fills buffer with packet data
3. Get packet struct from packetPool (sync.Pool)
4. Deserialize: REFERENCE buffer in packet (NO COPY)
   - packet.Payload points to data buffer (IS the buffer reference)
5. Route packet to connection's lock-free ring (or direct insert if UsePacketRing=false)
6. Data buffer stays with packet (lifetime extended)

Event loop (continuous processing):
7. TryRead() packet from ring, insert into btree
8. Check periodicACK timer → send ACK if due
9. Check periodicNAK timer → send NAK if due
10. Check TSBPD → deliver ready packets to application:
    a. Call deliver callback (to application readQueue)
    b. Call r.releasePacketFully(pkt) - SINGLE CALL handles all cleanup:
       - Zeros buffer slice: *buffer = (*buffer)[:0]
       - Returns buffer to recvBufferPool
       - Clears packet references
       - Returns packet struct to packetPool via Decommission()
11. Loop back to step 7 (continuous, not timer-triggered)
```

**Buffer return sequence** (step 10b, using `releasePacketFully()` from Section 6.5.1):

```go
// In event loop delivery:
func (r *receiver) deliverReadyPackets() int {
    // ... iterate ready packets ...

    // Deliver to application
    r.deliver(pkt)

    // SINGLE CALL handles ALL cleanup (see 6.5.1 for implementation)
    r.releasePacketFully(pkt)
}
```

The `releasePacketFully()` method (defined in Section 6.5.1) ensures:
1. Buffer is **always zeroed** (`*buf = (*buf)[:0]`) before pool return
2. Buffer is returned to `recvBufferPool`
3. Packet references are cleared
4. Packet struct is returned to `packetPool` via `Decommission()`

**All in one call** - impossible to forget steps or call in wrong order!

**Key insight:** The packet's `Payload *[]byte` field IS the buffer reference. The receiver holds the pool reference (`bufferPool *sync.Pool`) and `releasePacketFully()` handles the complete cleanup sequence.

### 6.5.1 Buffer Lifecycle Abstraction Design

**Problem**: The current design requires a specific sequence of operations when releasing a packet:
1. Zero the buffer slice: `*buf = (*buf)[:0]`
2. Return buffer to pool: `bufferPool.Put(buf)`
3. Clear packet reference: `p.ClearPayload()`
4. Return packet to pool: `p.Decommission()`

This is **error-prone** - forgetting to zero the buffer, forgetting to call `releasePacketBuffer()`, or calling in wrong order can cause bugs. The io_uring completion handler error path is a good example where this can go wrong.

#### Design Options

**Option A: Current Design - Separate Calls (Error-Prone)**
```go
r.releasePacketBuffer(pkt)  // Step 1-3: buffer handling
pkt.Decommission()          // Step 4: packet return
```
- **Pros**: Clear separation of concerns
- **Cons**: Easy to forget one step, must be called in order, error handling paths often wrong

**Option B: Combined Helper on Receiver (Recommended)**
```go
// Single call handles everything
r.releasePacketFully(pkt)
```
Implementation:
```go
func (r *receiver) releasePacketFully(p *packet.Packet) {
    // Step 1-3: Release ORIGINAL buffer (not Payload sub-slice!)
    if buf := p.GetRecvBuffer(); buf != nil {
        *buf = (*buf)[:0]  // Zero for immediate reuse
        r.bufferPool.Put(buf)
        p.ClearRecvBuffer()  // Clears BOTH recvBuffer and Payload
    }
    // Step 4: Return packet struct to pool
    p.Decommission()
}
```
- **Pros**: Single call, impossible to forget steps, correct ordering guaranteed
- **Cons**: Receiver must always be available

**Option C: Pass Pool to Decommission**
```go
// Packet handles its own buffer release
pkt.DecommissionWithBuffer(bufferPool)
```
Implementation:
```go
func (p *Packet) DecommissionWithBuffer(bufferPool *sync.Pool) {
    // Release ORIGINAL buffer if present
    if p.recvBuffer != nil {
        *p.recvBuffer = (*p.recvBuffer)[:0]  // Zero for immediate reuse
        bufferPool.Put(p.recvBuffer)
        p.recvBuffer = nil
        p.n = 0
    }
    // Return packet to pool
    p.Decommission()
}
```
- **Pros**: Packet-centric, works without receiver
- **Cons**: Requires passing pool reference, changes Decommission signature

**Option D: Register Pool in Packet (NOT SELECTED)**

> ⚠️ **NOT IMPLEMENTED**: This option was considered but rejected. It adds unnecessary memory overhead (one `*sync.Pool` pointer per packet) and couples packet lifecycle to pool knowledge. We use Option B + Option C instead.

```go
// REJECTED APPROACH - shown for completeness only
// During deserialization
pkt.SetBufferPool(bufferPool)

// Later, single call handles everything
pkt.DecommissionFully()
```
<details>
<summary>Implementation (for reference only - NOT USED)</summary>

```go
type Packet struct {
    Header     Header
    recvBuffer *[]byte
    n          int       // Bytes received (from ReadFromUDP/io_uring)
    bufferPool *sync.Pool  // NOT USED: Would add 8 bytes per packet
}

func (p *Packet) SetBufferPool(pool *sync.Pool) {
    p.bufferPool = pool
}

func (p *Packet) DecommissionFully() {
    if p.recvBuffer != nil && p.bufferPool != nil {
        *p.recvBuffer = (*p.recvBuffer)[:0]
        p.bufferPool.Put(p.recvBuffer)
        p.recvBuffer = nil
        p.n = 0
    }
    p.bufferPool = nil
    p.Decommission()
}
```
</details>

- **Pros**: Most encapsulated, single call, works anywhere
- **Cons**: Adds 8 bytes to every packet struct, pool reference per packet, couples packet to pool
- **Reason Not Selected**: Memory overhead and unnecessary coupling; Option B + C achieves same goals without per-packet storage

---

#### ✅ CHOSEN DESIGN: Option B + Option C Hybrid

> **Implementation Decision**: This is the selected design that will be implemented in Phase 2.

Use **Option B** (`r.releasePacketFully()`) as the **primary path** for the event loop where the receiver is available.

Add **Option C** (`pkt.DecommissionWithBuffer(pool)`) for **error paths** in io_uring/standard receive handlers where the receiver isn't available yet.

```go
// Primary path (in receiver/event loop):
r.releasePacketFully(pkt)  // Receiver has pool reference

// Error path (in io_uring completion handler):
pkt.DecommissionWithBuffer(ln.recvBufferPool)  // Pass pool explicitly
```

#### Helper Functions

**On Receiver** (`congestion/live/receive.go`):
```go
// releasePacketFully releases both the buffer and packet to their pools.
// This is the preferred method when the receiver is available.
// Safe to call even if buffer is nil (e.g., copied data, not zero-copy).
//
// IMPORTANT: Uses GetRecvBuffer() NOT Payload - Payload is a sub-slice!
func (r *receiver) releasePacketFully(p *packet.Packet) {
    if buf := p.GetRecvBuffer(); buf != nil {
        *buf = (*buf)[:0]  // Zero slice for immediate reuse on Get()
        r.bufferPool.Put(buf)
        p.ClearRecvBuffer()  // Clears BOTH recvBuffer and Payload
    }
    p.Decommission()
}
```

**On Packet** (`packet/packet.go`):
```go
// DecommissionWithBuffer releases the ORIGINAL buffer to the provided pool
// and then returns the packet struct to the packet pool.
// Use this in error paths where the receiver isn't available.
// Safe to call even if recvBuffer is nil.
func (p *Packet) DecommissionWithBuffer(bufferPool *sync.Pool) {
    if p.recvBuffer != nil && bufferPool != nil {
        *p.recvBuffer = (*p.recvBuffer)[:0]  // Zero slice for immediate reuse
        bufferPool.Put(p.recvBuffer)
        p.recvBuffer = nil
        p.n = 0
    }
    p.Decommission()
}
```

**Buffer-only helper** (for errors before packet allocation):
```go
// returnBufferToPool returns a buffer to the pool without a packet.
// Use when ReadFromUDP or similar fails before packet allocation.
// Can be a standalone function or method on connection/listener.
func returnBufferToPool(pool *sync.Pool, buf *[]byte) {
    *buf = (*buf)[:0]  // Zero slice for immediate reuse
    pool.Put(buf)
}
```

#### Usage Summary

| Context | Method | Notes |
|---------|--------|-------|
| Event loop delivery | `r.releasePacketFully(pkt)` | Receiver has pool |
| io_uring error path (packet allocated) | `pkt.DecommissionWithBuffer(pool)` | Buffer + packet cleanup |
| Standard recv error (packet allocated) | `pkt.DecommissionWithBuffer(pool)` | Buffer + packet cleanup |
| Read error (NO packet yet) | `returnBufferToPool(pool, buf)` | Buffer-only cleanup |
| Packet without buffer | Either method works | Handles nil gracefully |

This design ensures:
1. **Buffer is always zeroed** before pool return
2. **Single call** handles complete cleanup
3. **Impossible to forget steps** or call in wrong order
4. **Works in all contexts** (receiver available or not)

#### Simplified: Single-Call UnmarshalZeroCopy

The new `UnmarshalZeroCopy(buf, n, addr)` sets `recvBuffer` and `n` internally **BEFORE** any validation. This ensures `DecommissionWithBuffer()` can always return the buffer, even if parsing fails:

```go
// NEW SIMPLIFIED PATTERN: Single call does everything
pkt := packetPool.Get().(*packet.Packet)
err := pkt.UnmarshalZeroCopy(buffer, n, addr)  // Sets recvBuffer+n, then parses
if err != nil {
    // recvBuffer is ALREADY tracked (set before validation in UnmarshalZeroCopy)
    pkt.DecommissionWithBuffer(pool)           // ✓ Will return buffer to pool
    return
}
// Success - pkt.GetPayload() returns (*recvBuffer)[HeaderSize:n]
```

**Why this is safe:** `UnmarshalZeroCopy` stores `recvBuffer` and `n` as its FIRST action, before any validation or parsing. If header parsing fails, the buffer is still tracked.

**Pattern applied in:**
- Section 6.8: io_uring Completion Handler
- Section 6.9: Standard Receive Path

### 6.6 Packet Structure Changes

**Decision: Store bufferPool reference in Receiver (Congestion Control)**

The `recvBufferPool` reference is stored in the receiver struct, not in each packet. This provides:
- **Pool reference once per receiver** - not duplicated in every packet
- **Clean separation** - packet tracks buffer reference, receiver knows the pool
- **Natural fit** - receiver owns packet lifecycle, buffer release happens in `deliverPackets()`
- **Testable** - can inject mock pool for testing
- **Follows existing pattern** - receiver already has config references passed to it

#### Packet Structure (`packet/packet.go`)

**Key insight**: Since `HeaderSize` is constant (16 bytes) and we know `n` (bytes received), the payload can be computed on-demand via `GetPayload()`:

```go
payload = (*p.recvBuffer)[HeaderSize:p.n]
```

**Naming Decision: `n` for bytes received**

We use `n` rather than `dataLen` or `length` because:

1. **Go standard library convention** - `n` is the ubiquitous name for "bytes read/written":
   - `io.Reader.Read(p []byte) (n int, err error)`
   - `net.Conn.Read(b []byte) (n int, err error)`
   - `ReadFromUDP(b []byte) (n int, addr *UDPAddr, err error)`

2. **Maintains connection to read operation** - developers immediately recognize `n` from the read call that populated the buffer

3. **Least surprising to Go developers** - storing the same `n` value from the read preserves the mental model

**Solution**: Add `recvBuffer` and `n` for zero-copy, keep `Payload` for backwards compatibility:

```go
type Packet struct {
    Header     Header
    Payload    *[]byte  // LEGACY: points to copied data (nil in zero-copy path)
    recvBuffer *[]byte  // ZERO-COPY: original buffer from recvBufferPool (for return)
    n          int      // ZERO-COPY: bytes received (from ReadFromUDP/io_uring CQE.Res)
}

// IMPORTANT: Use GetPayload() for all payload access - it handles both paths!

// Memory layout (zero-copy path):
//
// recvBuffer ──► ┌──────────────────┬──────────────────────────┬─────────┐
//                │ Header (16 bytes)│ Payload Data             │ Unused  │
//                └──────────────────┴──────────────────────────┴─────────┘
//                │◄──────────────── n bytes ──────────────────►│
//
// GetPayload() computes: (*recvBuffer)[HeaderSize:n]

// GetPayload returns payload data, handling BOTH zero-copy and legacy paths
// - Zero-copy (recvBuffer set): computes slice from recvBuffer
// - Legacy (Payload set): returns *Payload directly
func (p *Packet) GetPayload() []byte {
    // Zero-copy path: compute from recvBuffer
    if p.recvBuffer != nil {
        return (*p.recvBuffer)[HeaderSize:p.n]
    }
    // Legacy path: return stored Payload
    if p.Payload != nil {
        return *p.Payload
    }
    return nil
}

// GetPayloadLen returns the payload length (n - HeaderSize)
func (p *Packet) GetPayloadLen() int {
    if p.n <= HeaderSize {
        return 0
    }
    return p.n - HeaderSize
}

// GetRecvBuffer returns the original buffer reference for pool release
func (p *Packet) GetRecvBuffer() *[]byte {
    return p.recvBuffer
}

// ClearRecvBuffer clears the buffer reference after pool release
func (p *Packet) ClearRecvBuffer() {
    p.recvBuffer = nil
    p.n = 0
}

// HasRecvBuffer returns true if packet has a tracked pool buffer
func (p *Packet) HasRecvBuffer() bool {
    return p.recvBuffer != nil
}

// NOTE: No SetRecvBuffer() needed!
// UnmarshalZeroCopy(buf, n, addr) sets both recvBuffer and n internally.
// This is cleaner and ensures buffer is always tracked if parsing fails.
```

**Backwards Compatibility Note:**

The `Payload *[]byte` field is KEPT for backwards compatibility with legacy code paths. The zero-copy path uses `recvBuffer` + `n` instead, but both work through the unified `GetPayload()` interface.

**Future optimization** (after migration complete): Once ALL code uses `GetPayload()`, the `Payload` field can be removed to save 16-24 bytes per packet. This requires an audit to ensure no direct `p.Payload` access remains.

**Comparison of approaches:**

| Approach | Storage | GetPayload() | UnmarshalZeroCopy |
|----------|---------|--------------|-------------------|
| Keep `Payload` (current) | +24 bytes | Check both paths | Sets recvBuffer+n |
| Remove `Payload` (future) | +8 bytes | Zero-copy only | Sets recvBuffer+n |
| Store `n int` | 8 bytes (int) | Compute slice | Just store n |
| `Payload` | `data[HeaderSize:]` sub-slice | Application access to payload data |

After `UnmarshalZeroCopy`:
- `recvBuffer` → original 1500-byte buffer (for pool return)
- `Payload` → bytes 16+ of that buffer (payload data for application)

#### Receiver Structure (`congestion/live/receive.go`)

```go
type receiver struct {
    // ... existing fields ...

    // Reference to recvBufferPool for returning data buffers
    // Passed from connection during receiver creation
    bufferPool *sync.Pool
}

// releasePacketBuffer returns the ORIGINAL buffer to the pool
// Called during packet delivery/cleanup
// Uses GetRecvBuffer() NOT GetPayloadBuffer() - Payload is a sub-slice!
func (r *receiver) releasePacketBuffer(p *packet.Packet) {
    if buf := p.GetRecvBuffer(); buf != nil {
        *buf = (*buf)[:0]  // Reset slice to zero length for immediate reuse
        r.bufferPool.Put(buf)
        p.ClearRecvBuffer()  // Clears both recvBuffer and Payload
    }
}
```

#### Usage in deliverPackets()

```go
func (r *receiver) deliverPackets(now uint64) {
    r.packetStore.RemoveAll(
        func(p packet.Packet) bool { return p.Ready(now) },
        func(p *packet.Packet) {
            // 1. Deliver to application
            r.deliver(p)

            // 2. Single call handles ALL cleanup (buffer + packet)
            r.releasePacketFully(p)
        },
    )
}
```

#### Pool Reference Flow

```
┌─────────────────┐     creates      ┌─────────────────┐
│ Listener/Dialer │ ───────────────► │ recvBufferPool  │
│                 │                  │ (sync.Pool)     │
└─────────────────┘                  └─────────────────┘
        │                                    │
        │ passes reference                   │ Get()/Put()
        ▼                                    ▼
┌─────────────────┐     passes ref   ┌─────────────────┐
│   Connection    │ ───────────────► │    Receiver     │
│   (srtConn)     │                  │ bufferPool *    │
└─────────────────┘                  └─────────────────┘
                                             │
                                             │ releasePacketBuffer(p)
                                             │ uses p.GetRecvBuffer() to return
                                             ▼
                                     ┌──────────────────────┐
                                     │       Packet         │
                                     │ recvBuffer *[]byte   │ ← for pool Put()
                                     │ n int                │ ← bytes received
                                     └──────────────────────┘
                                             │
                                             │ GetPayload() computes:
                                             │ (*recvBuffer)[HeaderSize:n]
                                             ▼
```

**Key insight**: The Packet struct stores:
1. `recvBuffer` - Original pool buffer (for `Put()` back to pool)
2. `n` - Bytes received (to compute payload slice on-demand)

**No Payload slice stored!** `GetPayload()` computes `(*recvBuffer)[HeaderSize:n]`

### 6.7 Packet Unmarshalling Functions

Understanding the packet unmarshalling functions is essential for the zero-copy receive path. This section documents the key functions in `packet/packet.go`.

#### 6.7.1 Where Does `n` (Bytes Received) Come From?

The `n` parameter represents the **actual number of bytes received** from the network. It comes from the read operation:

**Standard socket read:**
```go
// ReadFromUDP returns: n (bytes read), addr (sender), err
n, addr, err := conn.ReadFromUDP(*buf)
//▲
//└── This is n: actual bytes received into buffer
```

**io_uring completion:**
```go
// Completion Queue Entry (CQE) contains result
cqe := ring.GetCQE()
n := int(cqe.Res)  // Res field = bytes read (or negative error code)
//          ▲
//          └── This is n: actual bytes from kernel
```

**Why is `n` needed?** The pooled buffer is pre-allocated at a fixed size (typically 1500 bytes for MTU), but the actual UDP packet received is usually smaller. We need `n` to know how much of the buffer contains valid data.

```
Pooled Buffer (1500 bytes capacity):
┌─────────────────────────────────────────────────────────────────┐
│ [Actual packet data: n bytes]              │ [Unused padding]   │
└─────────────────────────────────────────────────────────────────┘
│◄─────────── n bytes ──────────────────────►│
│◄────────────────── 1500 bytes (cap) ───────────────────────────►│
```

#### 6.7.2 Legacy Approach: UnmarshalCopy

The existing function copies payload data into a new slice:

```go
// UnmarshalCopy parses a packet from raw bytes, COPYING the payload.
// Used in the current (non-zero-copy) implementation.
//
// Parameters:
//   - addr: Source address of the packet
//   - data: Raw packet bytes (header + payload)
func (p *Packet) UnmarshalCopy(addr net.Addr, data []byte) error {
    // Parse header (16 bytes for data packets)
    if len(data) < HeaderSize {
        return ErrPacketTooShort
    }

    if err := p.Header.Unmarshal(data[:HeaderSize]); err != nil {
        return err
    }

    // COPY payload data into new slice (allocates memory)
    payloadData := data[HeaderSize:]
    payloadCopy := make([]byte, len(payloadData))  // ← Allocation!
    copy(payloadCopy, payloadData)                  // ← Copy bytes!
    p.Payload = &payloadCopy  // Points to the COPY

    p.Addr = addr
    return nil
}
```

**Problem**: The `make()` allocates new memory and `copy()` copies bytes, adding latency and GC pressure.

#### 6.7.3 New Approach: UnmarshalZeroCopy

For zero-copy, everything is done in a single `UnmarshalZeroCopy(buf, n, addr)` call. The function stores the buffer reference first (for error safety), then parses the header - **no Payload slice needed**:

```go
// UnmarshalZeroCopy parses a packet where the receive buffer is already assigned.
// This is the zero-copy path - no data is copied, no sub-slices created.
//
// Parameters:
//   - buf:  Pointer to pooled buffer from recvBufferPool
//   - n:    Number of bytes received (from ReadFromUDP or io_uring CQE.Res)
//   - addr: Source address of the packet
//
// This function:
// 1. Stores buf and n in the packet (for pool return and payload access)
// 2. Validates n >= HeaderSize
// 3. Parses the header
//
// Payload access is via GetPayload() which computes: (*recvBuffer)[HeaderSize:n]
//
// This is MORE EFFICIENT than separate SetRecvBuffer() + UnmarshalZeroCopy():
// - Single function call
// - Can't forget to set buffer before parsing
// - Buffer is always tracked if parsing fails (safe error handling)
func (p *Packet) UnmarshalZeroCopy(buf *[]byte, n int, addr net.Addr) error {
    // Store buffer and length FIRST (before any validation that might fail)
    // This ensures DecommissionWithBuffer() can always return the buffer
    p.recvBuffer = buf
    p.n = n

    // Now validate
    if n < HeaderSize {
        return ErrPacketTooShort
    }

    // Parse header directly from recvBuffer (no need to slice to n first)
    if err := p.Header.Unmarshal((*buf)[:HeaderSize]); err != nil {
        return err
    }

    p.Addr = addr
    return nil

    // NOTE: No Payload slice created!
    // Callers use GetPayload() which computes: (*recvBuffer)[HeaderSize:n]
}
```

**Key insight**: `UnmarshalZeroCopy` now does everything in one call:
1. Store buffer reference and length
2. Validate `n >= HeaderSize`
3. Parse header
4. Done!

**Error safety**: Buffer is stored FIRST, before validation. This means if parsing fails, `DecommissionWithBuffer()` can still return the buffer to the pool. No separate `SetRecvBuffer()` call needed!

#### 6.7.4 Memory Layout Visualization

```
After UnmarshalZeroCopy(buf, n=200, addr):
┌─────────────────────────────────────────────────────────────────┐
│ Pooled Buffer (1500 bytes from recvBufferPool)                  │
├──────────────────┬──────────────────────────┬───────────────────┤
│ Header (16 bytes)│ Payload Data (184 bytes) │ Unused (1300)     │
└──────────────────┴──────────────────────────┴───────────────────┘
▲                                              ▲
p.recvBuffer points here                       p.n = 200
(original pool buffer)

GetPayload() computes: (*recvBuffer)[HeaderSize:n]
                       (*recvBuffer)[16:200] → 184 bytes of payload

NO separate Payload pointer stored - computed on demand!
```

#### 6.7.5 Comparison

| Aspect | UnmarshalCopy | UnmarshalZeroCopy |
|--------|---------------|-------------------|
| Signature | `(addr, data []byte)` | `(addr)` |
| Data copy | Yes (`copy()`) | No |
| Memory allocation | Yes (`make()`) | No |
| Payload storage | `Payload *[]byte` (24 bytes) | `n int` (8 bytes) |
| Payload access | Direct (`p.Payload`) | Computed (`GetPayload()`) |
| Buffer lifetime | Short (returned immediately) | Extended (until delivery) |
| Precondition | None | None (single call does everything) |
| Pool return | N/A (no pool buffer) | Use `GetRecvBuffer()` |
| Use case | Legacy path, `UseZeroCopyBuffers=false` | Zero-copy path |

#### 6.7.6 Usage Pattern

The zero-copy pattern requires explicit buffer assignment before parsing:

```go
// 1. Get buffer and packet from pools
buf := pool.Get().(*[]byte)
pkt := packetPool.Get().(*packet.Packet)

// 2. Read data into buffer (io_uring or ReadFromUDP)
n, addr, err := conn.ReadFromUDP(*buf)  // n = actual bytes received
if err != nil {
    returnBufferToPool(pool, buf)  // No packet yet
    return
}

// 3. SINGLE CALL: Parse header AND track buffer (zero-copy)
//    UnmarshalZeroCopy stores buf and n internally, then parses header
err = pkt.UnmarshalZeroCopy(buf, n, addr)
if err != nil {
    // recvBuffer was set BEFORE validation, so cleanup is safe
    pkt.DecommissionWithBuffer(pool)
    return
}

// 4. Packet ready:
//    - pkt.recvBuffer → original pool buffer (for return)
//    - pkt.n = n (bytes received)
//    - pkt.GetPayload() → computed slice (*recvBuffer)[HeaderSize:n]
```

This pattern is used in both the io_uring (Section 6.8) and standard receive (Section 6.9) paths.

### 6.8 io_uring Completion Handler Changes

**In listener/dialer completion handler**:

```go
// Current (copies data):
func (ln *listener) processRecvCompletion(buffer []byte, n int, addr net.Addr) {
    // Get packet struct from packet pool
    p := packetPool.Get().(*packet.Packet)

    // NewPacketFromData COPIES data into packet
    err := p.UnmarshalCopy(addr, buffer[:n])  // COPIES payload
    if err != nil {
        packetPool.Put(p)  // Return packet to pool
        return
    }

    ln.recvBufferPool.Put(buffer)  // Returns buffer immediately
    // route packet...
}

// New (references buffer - NO COPY):
func (ln *listener) processRecvCompletion(buffer *[]byte, n int, addr net.Addr) {
    // Get packet struct from packet pool
    p := packetPool.Get().(*packet.Packet)

    // SINGLE CALL: Parse header AND track buffer
    // UnmarshalZeroCopy stores buffer+n first, then parses header
    err := p.UnmarshalZeroCopy(buffer, n, addr)
    if err != nil {
        // ERROR PATH: recvBuffer was set before validation, cleanup is safe
        p.DecommissionWithBuffer(ln.recvBufferPool)
        return
    }

    // After success:
    //   p.recvBuffer → original pool buffer (for return)
    //   p.n = n (bytes received)
    //   p.GetPayload() → computed slice (*recvBuffer)[HeaderSize:n]
    // route packet to connection's lock-free ring...
}
```

**Key changes in packet unmarshalling:**
- **Single `UnmarshalZeroCopy(buf, n, addr)` call** - tracks buffer AND parses
- No separate `SetRecvBuffer()` needed
- Payload access via `GetPayload()` which computes slice on-demand
- No `copy()` of payload data, no extra slice storage
- Buffer lifetime extended until delivery
- **Error paths safe** - buffer stored BEFORE validation in `UnmarshalZeroCopy`

**Note on io_uring read errors**: The `processRecvCompletion` function is only called when io_uring completion indicates success (n > 0). Read errors (network errors, timeouts) are handled at the completion queue level:

```go
// In io_uring completion queue processing (simplified)
func (ln *listener) processCompletions() {
    for cqe := range completionQueue {
        buf := cqe.UserData.(*[]byte)  // Buffer submitted with SQE

        if cqe.Res < 0 {
            // READ ERROR: No packet allocated - use buffer-only helper
            returnBufferToPool(ln.recvBufferPool, buf)
            continue
        }

        // Success - process the received data
        ln.processRecvCompletion(buf, int(cqe.Res), cqe.Addr)
    }
}
```

### 6.9 Standard (Non-io_uring) Receive Path

The zero-copy optimization applies equally to the standard receive path:

**Current Standard Receive (with copy):**
```go
func (c *connection) readLoop() {
    for {
        // Get buffer from pool
        buf := c.recvBufferPool.Get().(*[]byte)

        // Read from socket
        n, addr, err := c.conn.ReadFromUDP(*buf)
        if err != nil { /* handle */ }

        // Get packet from pool
        pkt := packetPool.Get().(*packet.Packet)

        // COPIES data into packet
        err = pkt.UnmarshalCopy(addr, (*buf)[:n])

        // Returns buffer immediately (BEFORE packet is processed)
        c.recvBufferPool.Put(buf)

        // Route packet...
    }
}
```

**New Standard Receive (zero-copy):**
```go
func (c *connection) readLoop() {
    for {
        // Get buffer from pool
        buf := c.recvBufferPool.Get().(*[]byte)

        // Read from socket
        n, addr, err := c.conn.ReadFromUDP(*buf)
        if err != nil {
            // ERROR PATH 1: Read failed - no packet allocated yet
            // Use buffer-only helper (see 6.5.1)
            returnBufferToPool(c.recvBufferPool, buf)
            // handle error (log, break, continue depending on error type)
            continue
        }

        // Get packet from pool
        pkt := packetPool.Get().(*packet.Packet)

        // SINGLE CALL: Parse header AND track buffer
        // UnmarshalZeroCopy stores buf+n first, then parses header
        err = pkt.UnmarshalZeroCopy(buf, n, addr)
        if err != nil {
            // ERROR PATH 2: Parse failed - recvBuffer was set before validation
            pkt.DecommissionWithBuffer(c.recvBufferPool)
            continue
        }

        // After success:
        //   pkt.recvBuffer → original pool buffer (for return)
        //   pkt.n = n (bytes received)
        //   pkt.GetPayload() → computed slice for app access
        // Route packet to connection
        c.handlePacket(pkt)
    }
}
```

**Key changes:**
- **Error Path 1**: Read fails → `returnBufferToPool()` (buffer-only, no packet)
- **Error Path 2**: Parse fails → `pkt.DecommissionWithBuffer()` (buffer stored before validation)
- **Single `UnmarshalZeroCopy(buf, n, addr)` call** - tracks buffer AND parses
- Payload access via `GetPayload()` → computed `(*recvBuffer)[HeaderSize:n]`
- Buffer is NOT returned to pool after successful read
- Buffer lifetime extended until packet delivery
- Primary path uses `r.releasePacketFully()` which uses `GetRecvBuffer()`

### 6.10 Linked List Packet Store Compatibility

The linked list packet store uses a different removal pattern but benefits equally from the buffer lifecycle abstraction:

**Linked list delivery with zero-copy:**
```go
func (ps *linkedListPacketStore) deliverReady(deliverFn func(p *packet.Packet)) {
    node := ps.head
    for node != nil {
        if !node.pkt.Ready(now) {
            break
        }

        next := node.next

        // Deliver to application (callback handles cleanup)
        deliverFn(node.pkt)

        // Remove node from list
        ps.removeNode(node)

        node = next
    }
}

// Caller (receiver) handles cleanup with single call:
func (r *receiver) deliverPackets(now uint64) {
    r.packetStore.deliverReady(func(p *packet.Packet) {
        // 1. Deliver to application
        r.deliver(p)

        // 2. Single call handles buffer + packet cleanup
        r.releasePacketFully(p)
    })
}
```

**Key point:** The `releasePacketFully()` function is **packet store agnostic** - it works identically whether packets come from btree, linked list, or any other storage. It handles:
1. Zeroing the buffer slice
2. Returning buffer to pool
3. Clearing packet references
4. Returning packet struct to pool

### 6.11 Packet Deletion with Buffer Return

**In btree removal during delivery** (`receive.go`):

With the `releasePacketFully()` abstraction, the delivery process becomes simple and safe:

```go
func (r *receiver) deliverPackets(now uint64) {
    // Remove delivered packets from btree
    r.packetStore.RemoveAll(
        func(p packet.Packet) bool {
            // Filter: packets ready for delivery
            return p.Header().PacketSequenceNumber.Val() <= r.lastACK.Val()
        },
        func(p *packet.Packet) {
            // Step 1: Deliver packet data to application
            r.deliver(p)

            // Step 2: Single call handles ALL cleanup
            // - Zeros buffer slice for immediate reuse
            // - Returns buffer to recvBufferPool
            // - Clears packet references
            // - Returns packet struct to packetPool
            r.releasePacketFully(p)
            // Packet is deleted from btree by RemoveAll after callback returns
        },
    )
}
```

**What `releasePacketFully()` does internally:**

1. **Gets ORIGINAL buffer**: `buf := p.GetRecvBuffer()`
2. **Zeros buffer slice**: `*buf = (*buf)[:0]` - resets length (capacity preserved)
   - Next `recvBufferPool.Get()` returns a buffer ready to use
   - No need to reallocate or clear memory
3. **Returns buffer to pool**: `r.bufferPool.Put(buf)`
4. **Clears buffer reference**: `p.ClearRecvBuffer()` (sets `recvBuffer = nil`, `n = 0`)
5. **Returns packet to pool**: `p.Decommission()`
   - Clears packet fields for reuse
   - Existing gosrt pattern used throughout codebase

**All in one call** - impossible to forget steps or call in wrong order!

---

## 7. Component 3: Processing Model Redesign

This section describes two approaches to the lockless processing model:

1. **Event Loop (Recommended)** - Continuous processing with Go idioms (see Section 9)
2. **Lockless Tick()** - Timer-based batch processing (legacy compatibility)

Both approaches eliminate locks by ensuring single-threaded btree access. The **Event Loop is recommended** for new deployments due to lower latency and smoother delivery. The Lockless Tick() approach is available via feature flag (`UseEventLoop=false`) for backwards compatibility.

### 7.1 Current Tick Function Flow (Reference)

The current `Tick()` function in `receive.go` performs these operations:

```go
func (r *receiver) Tick(now uint64) {
    // 1. periodicACK() - builds and sends ACK
    //    - Acquires read lock to iterate packet btree
    //    - Acquires write lock briefly for updates

    // 2. periodicNAK() - detects losses and sends NAK
    //    - Acquires read lock to iterate packet btree
    //    - Acquires write lock for NAK btree operations

    // 3. periodicTLPKTDROP() - drops late packets
    //    - Acquires write lock to remove from btree

    // 4. Packet delivery
    //    - Acquires write lock to iterate and remove packets
    //    - Calls deliver callback under lock (!)

    // 5. Rate stats update
    //    - Acquires write lock for rate calculations
}
```

### 7.2 Lockless Tick Function Flow (Alternative to Event Loop)

When `UseEventLoop=false`, the timer-based Tick() function is used with batch processing:

```go
func (r *receiver) Tick(now uint64) {
    // ===== NEW STEP 0: Drain lock-free ring =====
    // This is the ONLY place that writes to packet btree
    // All subsequent operations read/modify btrees without locks
    r.drainPacketRing()

    // ===== Step 1: periodicACK =====
    // NO LOCK - single-threaded access to packet btree
    ok, ackSeq, lite := r.periodicACK(now)
    if ok {
        r.sendACK(ackSeq, lite)
    }

    // ===== Step 2: periodicNAK =====
    // NO LOCK - single-threaded access to packet btree and NAK btree
    nakList := r.periodicNAK(now)
    if len(nakList) > 0 {
        r.sendNAK(nakList)
    }

    // ===== Step 3: periodicTLPKTDROP =====
    // NO LOCK - single-threaded removal from packet btree
    r.periodicTLPKTDROP(now)

    // ===== Step 4: Deliver packets =====
    // NO LOCK - single-threaded iteration and removal
    // Delivery callback runs outside any lock
    r.deliverPackets(now)

    // ===== Step 5: Rate stats =====
    // Uses atomics - no locks needed
    r.updateRateStatsAtomic(now)
}
```

### 7.3 Function Modifications

Each periodic function needs to be modified to remove lock acquisition:

**periodicACK() changes**:
```go
func (r *receiver) periodicACK(now uint64) (bool, circular.Number, bool) {
    // REMOVE: r.lock.RLock() / r.lock.RUnlock()
    // REMOVE: metrics.WithWLockTiming(...)

    // Direct iteration - no lock needed
    // ... existing logic ...
}
```

**periodicNAK() changes**:
```go
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    // REMOVE: r.lock.RLock() / r.lock.RUnlock()

    // Direct iteration and NAK btree operations - no lock needed
    // ... existing logic ...
}
```

**deliverPackets() changes**:
```go
func (r *receiver) deliverPackets(now uint64) {
    // REMOVE: r.lock.Lock() / r.lock.Unlock()
    // REMOVE: metrics.WithWLockTiming(...)

    // Collect packets to deliver
    toDeliver := r.packetStore.RemoveAllCollect(func(p packet.Packet) bool {
        return p.Ready(now)
    })

    // Deliver OUTSIDE any lock (already no lock, but making explicit)
    for _, p := range toDeliver {
        // 1. Deliver to application
        r.deliver(p)

        // 2. Single call handles buffer + packet cleanup
        r.releasePacketFully(p)
    }
}
```

### 7.4 drainPacketRing() Detailed Implementation

```go
// drainPacketRing processes all packets waiting in the lock-free ring.
// This is called at the START of each Tick() to batch-process arrivals.
// Since Tick() is single-threaded, no locks are needed for btree operations.
func (r *receiver) drainPacketRing() {
    // Read all available packets (up to configured ring size)
    items := r.packetRing.ReadBatch(r.config.PacketRingSize)

    if len(items) == 0 {
        return // Nothing to process
    }

    // Track highest sequence for FastNAK (see Design Note in section 5.6)
    var maxSeq uint64 = 0

    // Process each packet
    for _, item := range items {
        pkt, ok := item.(packet.Packet)
        if !ok {
            continue // Skip invalid items
        }

        seq := pkt.Header().PacketSequenceNumber
        seqVal := seq.Val()

        // Track highest sequence seen
        if seqVal > maxSeq {
            maxSeq = seqVal
        }

        // === Validation (same as current pushLocked) ===

        // Initialize sequence tracking on first packet
        if r.initialSequenceNumber == nil {
            seqCopy := seq
            r.initialSequenceNumber = &seqCopy
        }

        // Check for duplicates
        if r.packetStore.Has(seq) {
            // Duplicate packet - single call handles cleanup
            r.releasePacketFully(pkt)
            continue
        }

        // Check if packet is too old (already delivered)
        if seq.Lt(r.deliveryBase) {
            // Old packet - single call handles cleanup
            r.releasePacketFully(pkt)
            continue
        }

        // === Insert into packet btree ===
        // NO LOCK - Tick() has exclusive access
        r.packetStore.Insert(pkt)

        // === Delete from NAK btree ===
        // NO LOCK - Tick() has exclusive access
        if r.nakBtree != nil {
            if r.nakBtree.Delete(seq) {
                // Packet arrived, remove from NAK list
                r.nakBtreeDeletes.Add(1)
            }
        }
    }

    // === Update tracking after batch ===
    // NOTE: Batching is a MICRO-OPTIMIZATION. Per-packet updates (as in Event Loop)
    // are equally acceptable. At 100Mb/s (~8,600 pkt/s), atomic store overhead is
    // negligible (~0.1µs per store). See Section 5.6.6 for comparison.
    r.lastPacketArrivalTime.Store(uint64(time.Now().UnixMicro()))
    r.lastDataPacketSeq.Store(maxSeq)  // Highest sequence in batch

    // If we read a full batch, there might be more - read again
    // This ensures we don't leave packets in the ring
    if len(items) == r.config.PacketRingSize {
        r.drainPacketRing() // Recursive call to drain remaining
    }
}

// releasePacketFully releases ORIGINAL buffer AND returns packet to pool (see 6.5.1)
func (r *receiver) releasePacketFully(p *packet.Packet) {
    if buf := p.GetRecvBuffer(); buf != nil {  // NOT GetPayloadBuffer - Payload is sub-slice!
        *buf = (*buf)[:0]  // Reset slice to zero length
        r.bufferPool.Put(buf)
        p.ClearRecvBuffer()  // Clears BOTH recvBuffer and Payload
    }
    p.Decommission()
}
```

**Key improvements from detailed implementation:**
1. **Batch tracking updates** - `lastPacketArrivalTime` and `lastDataPacketSeq` updated once per batch (micro-optimization)
2. **Track max sequence** - Stores highest sequence seen for meaningful FastNAK tracking
3. **Proper cleanup** - Single `releasePacketFully()` call handles both buffer and packet
4. **Config-based batch size** - Uses `PacketRingSize` from config

**Note on tracking update frequency:**

The batch update in `drainPacketRing()` is a **micro-optimization**, not a requirement. Both approaches are valid:

| Approach | When Used | Overhead at 100Mb/s | Pros |
|----------|-----------|---------------------|------|
| Per-packet update | Event Loop (5.6.6) | ~8,600 stores/sec (~0.8ms total) | Consistent, simpler |
| Batch update | Tick Loop (7.4) | ~100 stores/sec | Slightly lower overhead |

**Recommendation**: Per-packet updates are preferred for consistency. The overhead difference is negligible unless you're processing millions of packets per second.

---

## 8. Component 4: Rate Metrics Migration to Atomics ✅ IMPLEMENTED

### 8.1 Overview

> **Status**: ✅ **COMPLETE** - See [Phase 1 Implementation](#phase-1-rate-metrics-atomics--complete-no-flag---always-on)
>
> All rate metrics have been migrated to `atomic.Uint64` fields in `ConnectionMetrics` and are exported via the Prometheus handler. Integration tests confirm ~95% rate accuracy.

Rate metrics are currently updated under lock in `Push()` and `Tick()`. With the lockless design, these must use atomic operations exclusively. This section references and expands on `rate_metrics_performance_design.md`.

### 8.2 Fields to Migrate

**Hot Path Fields** (every packet):

| Field | Current Type | New Type |
|-------|-------------|----------|
| `nPackets` | uint | `atomic.Uint64` |
| `rate.packets` | uint64 | `atomic.Uint64` |
| `rate.bytes` | uint64 | `atomic.Uint64` |
| `rate.bytesRetrans` | uint64 | `atomic.Uint64` |

**Periodic Fields** (once per second):

| Field | Current Type | New Type |
|-------|-------------|----------|
| `rate.packetsPerSecond` | float64 | `atomic.Value` |
| `rate.bytesPerSecond` | float64 | `atomic.Value` |
| `rate.pktRetransRate` | float64 | `atomic.Value` |
| `rate.last` | uint64 | `atomic.Uint64` |

**Running Averages** (CAS-based):

| Field | Current Type | New Type |
|-------|-------------|----------|
| `avgPayloadSize` | float64 | `atomic.Uint64` (Float64bits) |
| `avgLinkCapacity` | float64 | `atomic.Uint64` (Float64bits) |

### 8.3 Struct Changes

```go
type receiver struct {
    // ... existing fields ...

    // Rate counters (atomic - hot path)
    nPackets         atomic.Uint64
    ratePackets      atomic.Uint64
    rateBytes        atomic.Uint64
    rateBytesRetrans atomic.Uint64
    rateLast         atomic.Uint64

    // Rate computed values (atomic.Value for float64)
    ratePacketsPerSecond atomic.Value // stores float64
    rateBytesPerSecond   atomic.Value // stores float64
    ratePktRetransRate   atomic.Value // stores float64

    // Running averages (atomic uint64 with Float64bits/Float64frombits)
    avgPayloadSizeBits  atomic.Uint64
    avgLinkCapacityBits atomic.Uint64

    // Dropped packets counter (new for ring overflow tracking)
    droppedPackets atomic.Uint64

    // Config (immutable after init)
    ratePeriod uint64
}
```

### 8.4 Running Average Update (CAS Loop)

```go
// updateAvgPayloadSize updates the running average using lock-free CAS
func (r *receiver) updateAvgPayloadSize(pktLen uint64) {
    for {
        oldBits := r.avgPayloadSizeBits.Load()
        old := math.Float64frombits(oldBits)

        // EMA: new = 0.875 * old + 0.125 * sample
        new := 0.875*old + 0.125*float64(pktLen)
        newBits := math.Float64bits(new)

        if r.avgPayloadSizeBits.CompareAndSwap(oldBits, newBits) {
            return // Success
        }
        // CAS failed - another goroutine updated, retry
    }
}
```

### 8.5 Rate Stats Update (Atomic)

```go
func (r *receiver) updateRateStatsAtomic(now uint64) {
    last := r.rateLast.Load()
    period := r.ratePeriod

    if now-last < period {
        return // Not time yet
    }

    // Atomically swap counters to get values and reset
    packets := r.ratePackets.Swap(0)
    bytes := r.rateBytes.Swap(0)
    bytesRetrans := r.rateBytesRetrans.Swap(0)

    // Calculate rates
    tdiff := float64(now - last)
    pps := float64(packets) / (tdiff / 1e6)
    bps := float64(bytes) / (tdiff / 1e6)
    retransRate := 0.0
    if bytes > 0 {
        retransRate = float64(bytesRetrans) / float64(bytes)
    }

    // Store computed values
    r.ratePacketsPerSecond.Store(pps)
    r.rateBytesPerSecond.Store(bps)
    r.ratePktRetransRate.Store(retransRate)

    // Update timestamp
    r.rateLast.Store(now)
}
```

---

## 9. Event Loop Architecture (Recommended)

This section describes the **recommended processing model** for the lockless design. The Event Loop provides lower latency, smoother CPU utilization, and continuous packet delivery compared to timer-based batch processing.

### 9.1 The Problem with Timer-Triggered Processing

The current design (and even the lock-free ring + Tick() approach in Section 7) still has a fundamental issue:

**Timer-based Tick() creates artificial batching:**

```
Time ─────────────────────────────────────────────────────────────────►

Packets:    │p1│p2│   │p3│p4│p5│p6│   │p7│        │p8│p9│p10│
            ▼  ▼      ▼  ▼  ▼  ▼      ▼           ▼  ▼  ▼
Ring:       [accumulating packets...]  [accumulating...]  [accumulating...]
            ────────────────────────────────────────────────────────────
                        │                    │                   │
                        ▼                    ▼                   ▼
Tick():              BATCH 1              BATCH 2             BATCH 3
                   (process 6)          (process 1)         (process 3)
                    ~10ms                 ~10ms               ~10ms
```

With the lock-free ring approach, packets accumulate in the ring until `Tick()` fires (every ~10ms). Then `Tick()` must:
1. Drain all accumulated packets (could be many)
2. Run periodicACK
3. Run periodicNAK
4. Deliver packets

This creates **bursty CPU usage** - idle while accumulating, busy during Tick().

### 9.2 The Radical Alternative: Continuous Event Loop

**What if we inverted the model?**

Instead of timer-triggered batch processing, use a **continuous event loop** that:
1. Primarily consumes from the lock-free queue
2. Inserts packets into btree immediately
3. Removes from NAK btree immediately
4. Checks timers opportunistically during the loop

```
Time ─────────────────────────────────────────────────────────────────►

Packets:    │p1│p2│   │p3│p4│p5│p6│   │p7│        │p8│p9│p10│
            ▼  ▼      ▼  ▼  ▼  ▼      ▼           ▼  ▼  ▼
Loop:       ○──○──○───○──○──○──○──○───○──○──○──○──○──○──○───○──○──○──►
            │  │      │  │  │  │      │           │  │  │
            └──┴──────┴──┴──┴──┴──────┴───────────┴──┴──┴─── Process + deliver immediately
                      │           │
                      ACK         NAK
                      (ticker)    (ticker)
```

### 9.3 Event Loop Design (Go Idiomatic)

Using Go's `time.Ticker` and `select` for clean, idiomatic code. Key changes from the original concept:

1. **Use tickers** - OS-managed timers via channels, integrates naturally with `select`
2. **Offset tickers** - Stagger timer firing to spread work evenly over time
3. **Deliver every iteration** - No batching! Check TSBPD continuously for smooth traffic
4. **Simple loop** - Just iterate; no explicit "continue" logic needed

#### Ticker Collision Problem

With ACK at 10ms and NAK at 20ms, every 20ms both fire together:

```
Time:    0    10   20   30   40   50   60   ...
ACK:     ✓    ✓    ✓    ✓    ✓    ✓    ✓
NAK:     ✓         ✓         ✓         ✓
                   ↑         ↑         ↑
               COLLISION! Both fire at same time
```

#### Solution: Offset Tickers

Stagger the ticker start times to spread work evenly:

```go
// Per-connection event loop (replaces Tick())
// NOTE: Simplified - see "Recommended" version below for rateTicker and adaptive backoff
func (r *receiver) eventLoop(ctx context.Context) {
    // Config uses time.Duration (not *Ms suffix)
    // See Section 5.3 for ReceiverConfig struct
    ackInterval := r.config.ACKInterval   // Default: 10ms
    nakInterval := r.config.NAKInterval   // Default: 20ms

    // Create ACK ticker immediately
    ackTicker := time.NewTicker(ackInterval)
    defer ackTicker.Stop()

    // Offset NAK ticker by half of ACK interval to spread work
    // With 10ms ACK and 20ms NAK: NAK fires at 5ms, 25ms, 45ms, ...
    time.Sleep(ackInterval / 2)
    nakTicker := time.NewTicker(nakInterval)
    defer nakTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return

        case <-ackTicker.C:
            r.periodicACK(time.Now())

        case <-nakTicker.C:
            r.periodicNAK(time.Now())

        default:
            // === PRIMARY WORK: Process packets and deliver ===
            r.processOnePacket()
            r.deliverReadyPackets()
        }
    }
}
```

**Result with offset:**
```
Time:    0    5    10   15   20   25   30   35   40   45   ...
ACK:     ✓         ✓         ✓         ✓         ✓
NAK:          ✓              ✓              ✓              ✓
         ↑    ↑    ↑    ↑    ↑    ↑    ↑    ↑    ↑    ↑
         Work spread evenly - no collisions!
```

#### Alternative: Single Ticker with Counters

Even simpler - use one ticker and track iterations:

```go
func (r *receiver) eventLoop(ctx context.Context) {
    // Single ticker at the GCD interval (10ms)
    ticker := time.NewTicker(r.config.ACKInterval)  // Config uses time.Duration
    defer ticker.Stop()

    tickCount := uint64(0)
    nakEveryN := r.config.NAKInterval / r.config.ACKInterval // e.g., 20ms/10ms = 2

    for {
        select {
        case <-ctx.Done():
            return

        case <-ticker.C:
            tickCount++
            now := time.Now()

            // ACK runs every tick
            r.periodicACK(now)

            // NAK runs every Nth tick
            if tickCount%nakEveryN == 0 {
                r.periodicNAK(now)
            }

        default:
            r.processOnePacket()
            r.deliverReadyPackets()
        }
    }
}
```

**Pros of single ticker:**
- Simpler timer management
- Guaranteed no collisions (sequential in same tick handler)
- Predictable timing

**Cons:**
- ACK and NAK run in same tick (small burst)
- Less flexible if intervals aren't multiples

#### Recommended: Inline Offset with Idle Handling

The cleanest solution uses an inline sleep during startup. The 5ms delay is negligible for a connection that runs for seconds/minutes:

```go
func (r *receiver) eventLoop(ctx context.Context) {
    ackInterval := r.config.ACKInterval
    nakInterval := r.config.NAKInterval
    rateInterval := r.config.RateInterval  // Default: 1s

    // Create ACK ticker first
    ackTicker := time.NewTicker(ackInterval)
    defer ackTicker.Stop()

    // Brief offset before NAK ticker (5ms at default 10ms ACK interval)
    // This is a ONE-TIME startup delay, acceptable for long-running connections
    time.Sleep(ackInterval / 2)

    nakTicker := time.NewTicker(nakInterval)
    defer nakTicker.Stop()

    // Phase shift rate ticker to spread work evenly
    // ACK fires at 0, 10, 20, ...
    // NAK fires at 5, 25, 45, ... (offset by ackInterval/2)
    // Rate fires at 7.5, 1007.5, 2007.5, ... (offset by ackInterval/4)
    time.Sleep(ackInterval / 4)

    // Rate ticker fires at the actual calculation period (e.g., 1s)
    // No need for early-return check in updateRecvRate - we fire at the right interval
    rateTicker := time.NewTicker(rateInterval)
    defer rateTicker.Stop()

    backoff := newAdaptiveBackoff(r.config)

    for {
        select {
        case <-ctx.Done():
            return

        case <-ackTicker.C:
            r.periodicACKNoLock(time.Now())

        case <-nakTicker.C:
            r.periodicNAKNoLock(time.Now())

        case <-rateTicker.C:
            // Update rate metrics atomically (no lock needed)
            // See Phase 1: Rate Metrics Atomics
            r.updateRecvRate(uint64(time.Now().UnixMicro()))

        default:
            // Process packets and deliver
            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()

            // Adaptive backoff when idle (see Section 9.7)
            if !processed && delivered == 0 {
                time.Sleep(backoff.getSleepDuration())
            } else {
                backoff.recordActivity()
            }
        }
    }
}
```

**Why inline sleep is acceptable:**
- The 5ms delay happens ONCE at connection startup
- Connections typically run for seconds/minutes, making 5ms negligible
- Simpler code than stop/reset/goroutine approach
- No race conditions or missed first ticks

**Why NOT use goroutine for offset:**
- Adds complexity (stop/reset ticker pattern)
- Potential for missed first NAK tick during reset
- Goroutine coordination overhead
- Not worth the complexity to save 5ms at startup

#### Rate Ticker Design

The `rateTicker` is separate from ACK/NAK tickers because rate calculation has different requirements:

| Timer | Interval | Purpose |
|-------|----------|---------|
| `ackTicker` | 10ms | Send ACK packets to peer |
| `nakTicker` | 20ms | Detect losses and send NAK packets |
| `rateTicker` | 1s | Update rate metrics for Stats()/Prometheus |

**Why a separate rate ticker?**

1. **Different timescales**: ACK/NAK are protocol-level (fast), rate calculation is observability-level (slower)
2. **Configurable independently**: `RateInterval` can be tuned without affecting protocol timing
3. **No strict timing requirements**: Unlike ACK/NAK, rate calculation tolerates jitter

**Simplified design:**

The `rateTicker` fires at the actual rate calculation period (default 1s). When it fires, `updateRecvRate()` unconditionally calculates the rate - no early-return check needed.

```go
case <-rateTicker.C:
    // Always calculate - ticker fires at the right interval
    r.updateRecvRate(uint64(time.Now().UnixMicro()))
```

**Legacy Tick Path:**

For the legacy `UseEventLoop=false` path, rate updates still happen in `Tick()`:

```go
func (r *receiver) Tick(now uint64) {
    // ... other operations ...

    // ===== Step 5: Rate stats =====
    // Called every Tick (~10ms), but only calculates when period (1s) elapses
    r.updateRateStatsAtomic(now)
}
```

The legacy path still needs the time-elapsed check because `Tick()` fires every 10ms.

// processOnePacket consumes one packet from the ring and inserts into btree
// Returns true if a packet was processed, false if ring was empty
func (r *receiver) processOnePacket() bool {
    item, ok := r.packetRing.TryRead()
    if !ok {
        return false // Ring empty
    }

    pkt := item.(packet.Packet)
    seq := pkt.Header().PacketSequenceNumber

    // Validation: check for duplicates
    if r.packetStore.Has(seq) {
        r.releasePacketFully(pkt)  // Single call handles cleanup
        return true // Still processed (rejected duplicate)
    }

    // Insert into packet btree (NO LOCK - single goroutine)
    r.packetStore.Insert(pkt)

    // Remove from NAK btree (packet arrived)
    if r.nakBtree != nil {
        r.nakBtree.Delete(seq)
    }

    // Update tracking
    r.lastPacketArrivalTime.Store(uint64(time.Now().UnixMicro()))
    r.lastDataPacketSeq.Store(uint64(seq.Val()))

    return true
}

// deliverReadyPackets delivers all packets whose TSBPD time has arrived
// Called every loop iteration for smooth, non-bursty delivery
// Returns count of packets delivered (0 if none ready)
func (r *receiver) deliverReadyPackets() int {
    now := time.Now()
    delivered := 0

    // Iterate from btree.Min() forward, delivering packets whose time has come
    // Stop when we hit a packet still in the future
    for {
        pkt := r.packetStore.Min()
        if pkt == nil {
            return delivered // btree empty
        }

        // Check TSBPD: is this packet ready for delivery?
        if !r.isReadyForDelivery(pkt, now) {
            return delivered // This packet (and all after it) are still in the future
        }

        // Remove from btree and deliver
        r.packetStore.DeleteMin()

        // Deliver to application
        r.deliver(pkt)

        // Single call handles buffer + packet cleanup
        r.releasePacketFully(pkt)

        delivered++
    }
}

// isReadyForDelivery checks if packet's TSBPD timestamp has arrived
func (r *receiver) isReadyForDelivery(pkt packet.Packet, now time.Time) bool {
    // TSBPD: Timestamp-Based Packet Delivery
    // Packet is ready when: now >= packet.Timestamp + TSBPD_delay
    deliveryTime := pkt.Timestamp().Add(r.tsbpdDelay)
    return now.After(deliveryTime) || now.Equal(deliveryTime)
}
```

### 9.3.1 Why Deliver Every Iteration?

**Current behavior (timer-based):**
```
Time ─────────────────────────────────────────────────────────────────►
Packets ready:  p1  p2  p3      p4  p5      p6  p7  p8  p9
                │   │   │       │   │       │   │   │   │
Delivery:       └───┴───┴───────┴───┴───────┴───┴───┴───┘
                    BATCH 1         BATCH 2     BATCH 3
                    (3 pkts)        (2 pkts)    (4 pkts)
                    @ 10ms          @ 20ms      @ 30ms

Result: Bursty delivery to application - packets delivered in clumps
```

**New behavior (continuous):**
```
Time ─────────────────────────────────────────────────────────────────►
Packets ready:  p1  p2  p3      p4  p5      p6  p7  p8  p9
                │   │   │       │   │       │   │   │   │
Delivery:       ▼   ▼   ▼       ▼   ▼       ▼   ▼   ▼   ▼
              (imm)(imm)(imm)  (imm)(imm)  (imm)(imm)(imm)(imm)

Result: Smooth delivery - each packet delivered as soon as TSBPD allows
```

**Benefits:**
- **Less bursty traffic** to application
- **Lower latency** - packets delivered immediately when ready, not held until next tick
- **Simpler code** - no batch accumulation logic
- **Better for real-time** - more consistent packet spacing

### 9.3.2 CPU Considerations

The `default` case in `select` means the loop runs continuously. However:

1. **When packets arriving**: Loop is doing useful work (processing + delivering)
2. **When idle**: `TryRead()` and `deliverReadyPackets()` return quickly

If CPU usage is a concern during idle periods, add a small sleep:

```go
default:
    processed := r.processOnePacket()
    delivered := r.deliverReadyPackets()

    // If nothing happened, brief sleep to avoid busy-wait
    if !processed && delivered == 0 {
        time.Sleep(100 * time.Microsecond)
    }
}
```

Or use a notification channel from the ring:

```go
default:
    select {
    case <-r.packetRing.NotifyChannel():
        // Packet available - process it
        r.processOnePacket()
        r.deliverReadyPackets()
    case <-time.After(100 * time.Microsecond):
        // Timeout - just check delivery
        r.deliverReadyPackets()
    }
}
```

### 9.4 Benefits of Event Loop Architecture

| Aspect | Timer-Based Tick() | Event Loop |
|--------|-------------------|------------|
| **CPU Usage** | Bursty (idle → busy → idle) | Smooth, continuous |
| **Packet Processing Latency** | Up to tick interval (10ms) | Immediate |
| **Delivery Latency** | Batched every 10ms | Immediate when TSBPD ready |
| **Traffic Pattern to App** | Bursty (batches) | Smooth (packet-by-packet) |
| **Batch Size** | Variable (depends on arrival rate) | Single packet at a time |
| **Timer Work** | Heavy (process entire batch) | Light (just ACK/NAK) |
| **Code Style** | Go idiomatic (tickers + select) | Go idiomatic (tickers + select) |

**Key insight:** When tickers fire, there's **less work to do** because we've already:
- Inserted all arrived packets into the btree
- Removed arrived packets from NAK btree
- Delivered all TSBPD-ready packets

The ticker functions just need to:
- **periodicACK**: Scan btree for ACK range (already populated, packets already delivered)
- **periodicNAK**: Scan for gaps (already have accurate picture)

**Note:** Delivery is NOT on a timer - it happens continuously, every loop iteration. This eliminates artificial batching and provides smooth traffic to the application.

### 9.5 Hybrid Approach: Adaptive Batching

For very high packet rates, processing one packet at a time may not keep up. A hybrid approach processes multiple packets per iteration while still delivering continuously:

```go
func (r *receiver) eventLoop(ctx context.Context) {
    ackTicker := time.NewTicker(r.config.ACKInterval)   // Config uses time.Duration
    nakTicker := time.NewTicker(r.config.NAKInterval)   // Config uses time.Duration
    defer ackTicker.Stop()
    defer nakTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return

        case <-ackTicker.C:
            r.periodicACK(time.Now())

        case <-nakTicker.C:
            r.periodicNAK(time.Now())

        default:
            // Process up to N packets per iteration (adaptive batching)
            for i := 0; i < 10; i++ {
                if !r.processOnePacket() {
                    break // Ring empty
                }
            }

            // Always check delivery - continuous, not batched
            r.deliverReadyPackets()
        }
    }
}
```

### 9.6 Comparison: Three Architectures

```
┌────────────────────────────────────────────────────────────────────────┐
│                    ARCHITECTURE COMPARISON                              │
├────────────────────────────────────────────────────────────────────────┤
│                                                                        │
│  1. CURRENT: Lock-Based Push() + Timer Tick()                         │
│     ┌─────┐        ┌─────────┐                                        │
│     │Push │──LOCK──│ btree   │                                        │
│     └─────┘        └─────────┘                                        │
│                         ▲                                              │
│     ┌─────┐        ┌────┴────┐                                        │
│     │Tick │──LOCK──│ btree   │ (contention!)                          │
│     └─────┘        └─────────┘                                        │
│     - Batched delivery every 10ms (bursty to app)                     │
│                                                                        │
│  2. PROPOSED: Lock-Free Ring + Timer Tick()                           │
│     ┌─────┐        ┌─────────┐        ┌─────────┐                     │
│     │Push │──────►│ Ring    │        │ btree   │                     │
│     └─────┘        └─────────┘        └─────────┘                     │
│                         │                  ▲                          │
│                         │     ┌────────────┘                          │
│     ┌─────┐             │     │                                       │
│     │Tick │─────────────┴─────┘ (batch drain + batch delivery)        │
│     └─────┘                                                           │
│     - Still batched delivery every 10ms (bursty to app)               │
│                                                                        │
│  3. RADICAL: Lock-Free Ring + Event Loop + Continuous Delivery        │
│     ┌─────┐        ┌─────────┐        ┌─────────┐        ┌─────┐     │
│     │Push │──────►│ Ring    │        │ btree   │──────►│ App │     │
│     └─────┘        └─────────┘        └─────────┘        └─────┘     │
│                         │                  ▲                 ▲        │
│                         ▼                  │                 │        │
│     ┌──────────────────────────────────────────────────────────┐     │
│     │              Event Loop (select)                          │     │
│     │  ┌──────────────────────────────────────────────────────┐│     │
│     │  │ select {                                             ││     │
│     │  │   case <-ctx.Done(): return                          ││     │
│     │  │   case <-ackTicker.C: periodicACK()                  ││     │
│     │  │   case <-nakTicker.C: periodicNAK()                  ││     │
│     │  │   default:                                           ││     │
│     │  │     processOnePacket() ──────────────────────────────┼┘     │
│     │  │     deliverReadyPackets() ───────────────────────────┼──────┘
│     │  │ }                                                    │      │
│     │  └──────────────────────────────────────────────────────┘      │
│     └──────────────────────────────────────────────────────────┘     │
│     - Continuous delivery: each packet delivered when TSBPD ready    │
│     - Smooth traffic to app (not bursty)                             │
│                                                                        │
└────────────────────────────────────────────────────────────────────────┘
```

### 9.7 Adaptive Rate-Based Backoff

#### The Backoff Problem

When no packets are available, the event loop must decide how long to wait before checking again. Common approaches:

| Strategy | Description | Pros | Cons |
|----------|-------------|------|------|
| **Fixed sleep** | Always sleep same duration | Simple, predictable | Wastes CPU at low rates, adds latency at high rates |
| **Exponential backoff** | Double sleep time each miss | Eventually backs off | The longer you wait, the closer you are to next packet! |
| **Linear backoff** | Increase sleep linearly | Gentler than exponential | Same timing paradox |
| **Busy spin** | No sleep | Lowest latency | Wastes CPU |

**The Timing Paradox**: Traditional backoff strategies have a fundamental flaw - the longer you've been waiting without a packet, the MORE likely the next packet is about to arrive (assuming relatively steady traffic). Sleeping longer when you've already waited is backwards!

#### Rate-Based Adaptive Sleep

The insight: **we already calculate packet rates** (per `rate_metrics_performance_design.md`). Use this to estimate inter-packet arrival time:

```
Inter-packet time = 1 / (packets per second)

Example at different bitrates (assuming 1316 byte packets):
┌──────────────┬─────────────┬─────────────────┬──────────────────┐
│ Bitrate      │ Packets/sec │ Inter-packet    │ Safe sleep       │
├──────────────┼─────────────┼─────────────────┼──────────────────┤
│ 3 Mbps       │ ~285        │ ~3.5ms          │ 1-2ms            │
│ 10 Mbps      │ ~950        │ ~1.05ms         │ 500µs            │
│ 50 Mbps      │ ~4,750      │ ~210µs          │ 100µs            │
│ 100 Mbps     │ ~9,500      │ ~105µs          │ 50µs             │
│ 1 Gbps       │ ~95,000     │ ~10.5µs         │ 5µs or spin      │
└──────────────┴─────────────┴─────────────────┴──────────────────┘
```

**Safe sleep** = fraction of inter-packet time (e.g., 50%) to avoid missing packets.

#### Cold Start Problem

On startup, we don't know the rate yet. Strategy:

```
Phase 1 (Cold Start): First N packets (e.g., 1000)
  - Aggressive polling with minimal sleep (10µs)
  - Building up rate statistics

Phase 2 (Warm): After N packets
  - Use calculated rate to determine sleep time
  - Periodically recalculate as rate changes
```

#### Implementation

**Dependency on Phase 1 (Rate Metrics Atomics):**

The adaptive backoff reads `packetsPerSecond` from `ConnectionMetrics`. Phase 1 provides:
- `RecvRatePacketsPerSec atomic.Uint64` - stores float64 as uint64 bits
- `GetRecvRatePacketsPerSec() float64` - helper getter that decodes the value

```go
type adaptiveBackoff struct {
    // Reference to metrics (uses Phase 1 rate metrics)
    metrics *metrics.ConnectionMetrics

    // Backoff state
    coldStartPackets int64          // Countdown to warm state
    minSleep         time.Duration  // Floor (never sleep less)
    maxSleep         time.Duration  // Ceiling (never sleep more)
    sleepFraction    float64        // Fraction of inter-packet time
}

func newAdaptiveBackoff(m *metrics.ConnectionMetrics, config ReceiverConfig) *adaptiveBackoff {
    return &adaptiveBackoff{
        metrics:          m,
        coldStartPackets: int64(config.BackoffColdStartPkts), // Default: 1000
        minSleep:         config.BackoffMinSleep,              // Default: 10µs
        maxSleep:         config.BackoffMaxSleep,              // Default: 1ms
        sleepFraction:    0.25,                                // Sleep for 25% of expected inter-packet time
    }
}

// Called after each packet processed
func (ab *adaptiveBackoff) recordPacket() {
    if ab.coldStartPackets > 0 {
        atomic.AddInt64(&ab.coldStartPackets, -1)
    }
}

// Returns appropriate sleep duration when no packet available
func (ab *adaptiveBackoff) getSleepDuration() time.Duration {
    // Cold start: aggressive polling
    if atomic.LoadInt64(&ab.coldStartPackets) > 0 {
        return ab.minSleep
    }

    // Get current rate using Phase 1 helper getter
    pps := ab.metrics.GetRecvRatePacketsPerSec()
    if pps <= 0 {
        return ab.minSleep  // No rate data yet
    }

    // Calculate inter-packet time
    interPacketNs := float64(time.Second) / pps

    // Sleep for fraction of expected inter-packet time
    sleepNs := interPacketNs * ab.sleepFraction
    sleep := time.Duration(sleepNs)

    // Clamp to bounds
    if sleep < ab.minSleep {
        return ab.minSleep
    }
    if sleep > ab.maxSleep {
        return ab.maxSleep
    }
    return sleep
}
```

#### Integration with Event Loop

```go
func (r *receiver) eventLoop(ctx context.Context) {
    // Create backoff with reference to ConnectionMetrics (Phase 1)
    backoff := newAdaptiveBackoff(r.metrics, r.config)

    // Offset tickers as discussed
    ackTicker := time.NewTicker(ackInterval)
    time.Sleep(ackInterval / 2)
    nakTicker := time.NewTicker(nakInterval)
    defer ackTicker.Stop()
    defer nakTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ackTicker.C:
            r.periodicACK(time.Now())
        case <-nakTicker.C:
            r.periodicNAK(time.Now())
        default:
            processed := r.processOnePacket()
            if processed {
                backoff.recordPacket()  // Update cold start counter
            }

            delivered := r.deliverReadyPackets()

            // Adaptive sleep when idle
            if !processed && delivered == 0 {
                time.Sleep(backoff.getSleepDuration())
            }
        }
    }
}
```

#### Advanced: Inverse Backoff (Optional)

Instead of sleeping longer when idle, sleep SHORTER (inverse of traditional backoff):

```go
// inverseSleepDuration: the longer we've waited, the less we sleep
func (ab *adaptiveBackoff) inverseSleepDuration(idleDuration time.Duration) time.Duration {
    baseSleep := ab.getSleepDuration()

    // Expected inter-packet time (using Phase 1 getter)
    pps := ab.metrics.GetRecvRatePacketsPerSec()
    if pps <= 0 {
        return ab.minSleep
    }
    expectedInterval := time.Duration(float64(time.Second) / pps)

    // How much of the expected interval have we already waited?
    waitedFraction := float64(idleDuration) / float64(expectedInterval)

    if waitedFraction >= 0.8 {
        // We've waited 80%+ of expected interval - packet imminent, spin
        return ab.minSleep
    }

    // Reduce sleep as we approach expected arrival
    remainingFraction := 1.0 - waitedFraction
    return time.Duration(float64(baseSleep) * remainingFraction)
}
```

Usage:
```go
var idleStart time.Time
var isIdle bool

// In event loop
if !processed && delivered == 0 {
    if !isIdle {
        idleStart = time.Now()
        isIdle = true
    }
    idleDuration := time.Since(idleStart)
    time.Sleep(backoff.inverseSleepDuration(idleDuration))
} else {
    isIdle = false
}
```

#### Comparison: go-lock-free-ring Backoff vs Our Needs

The `go-lock-free-ring` library includes backoff for **writes** (producer side):

```go
// DefaultWriteConfig - for when ring is FULL
WriteConfig{
    MaxRetries:      10,
    BackoffDuration: 100 * time.Microsecond,
    MaxBackoffs:     0,  // unlimited
}
```

This is different from our consumer-side idle backoff:
- **Ring write backoff**: Producer waiting for space (ring full)
- **Our backoff**: Consumer waiting for data (ring empty)

Both scenarios benefit from adaptive strategies, but the rate-based approach is more relevant for the consumer side where we can predict arrival times.

#### Configuration

Add to `config.go`:

```go
type Config struct {
    // ... existing fields ...

    // Adaptive Backoff Configuration
    BackoffColdStartPackets int           // Packets before adapting (default: 1000)
    BackoffMinSleep         time.Duration // Minimum sleep (default: 1µs)
    BackoffMaxSleep         time.Duration // Maximum sleep (default: 1ms)
    BackoffSleepFraction    float64       // Fraction of inter-packet time (default: 0.25)
}
```

#### Summary

| Approach | CPU @ 3Mbps | CPU @ 100Mbps | Latency | Complexity |
|----------|-------------|---------------|---------|------------|
| Fixed 100µs | Medium | Low | Medium | Simple |
| Exponential backoff | Low | Low | High (timing paradox) | Simple |
| Rate-based adaptive | Optimal | Optimal | Low | Medium |
| Inverse backoff | Optimal | Optimal | Lowest | Higher |

**Recommendation**: Start with **rate-based adaptive** backoff. It provides the best balance of CPU efficiency and latency while leveraging metrics we're already calculating.

### 9.8 Implementation Considerations

#### Thread/Goroutine Model

The event loop requires **one goroutine per connection** that runs continuously:

```go
// In connection setup
go r.eventLoop(conn.ctx)
```

This is similar to the current model where `Tick()` is called from a goroutine, but:
- **Current**: Goroutine sleeps on timer, wakes for Tick()
- **Event Loop**: Goroutine runs continuously, processes as packets arrive

#### Idle CPU Usage

When no packets are arriving, the loop will spin. Mitigations:
1. **Sleep on empty ring**: `time.Sleep(100µs)` when `TryRead()` returns false
2. **Condition variable**: Signal the loop when packets are written
3. **Hybrid**: Switch to timer-based when traffic is low

#### Timer Precision

Checking timers in the loop may have slightly different timing characteristics:
- **Timer-based**: Precise intervals via `time.Ticker`
- **Event loop**: Timers checked opportunistically, may drift slightly under load

For SRT's ~10ms intervals, this drift is likely acceptable.

### 9.9 When to Consider Alternatives

The Event Loop is the **default recommendation** for most deployments. Consider the Lockless Tick() approach only in these specific scenarios:

| Scenario | Recommendation |
|----------|----------------|
| Default / New deployments | **Event Loop** (recommended) |
| Low latency critical | **Event Loop** |
| Variable traffic (bursty) | **Event Loop** with adaptive batching |
| Steady high throughput | **Event Loop** |
| Low CPU usage priority | Lockless Tick() (sleeps on timer) |
| Many connections (1000+) | Lockless Tick() (fewer active goroutines) |

### 9.10 Summary

**The Event Loop is the recommended processing model** for the lockless design:

1. **Default**: Enable `UseEventLoop=true` for new deployments
2. **Fallback**: Use `UseEventLoop=false` (Lockless Tick) if:
   - Running many connections (1000+) where goroutine overhead matters
   - CPU usage is more important than latency
3. **Testing**: Use feature flags to compare both approaches in your environment

The Lockless Tick() approach (Section 7.2) remains available via feature flag for backwards compatibility and specific use cases where timer-based batching is preferred.

---

## 10. Data Flow Comparison

### 10.1 Current Flow (With Locks)

```
io_uring CQE → deserialize → Push() ──┬── LOCK ────► packet btree insert
                                      │              NAK btree delete
                                      │              rate metrics update
                                      └── UNLOCK ──►

Tick() ──────────────────────────────┬── LOCK ────► periodicACK (read btree)
                                      │              periodicNAK (read/write btree)
                                      │              delivery (remove btree)
                                      │              rate stats
                                      └── UNLOCK ──►
```

**Problem**: Push() and Tick() compete for the same lock, causing contention.

### 10.2 New Flow (Lockless)

```
io_uring CQE → deserialize → Push() ──────────────► ring.Write() [atomic]
                                                    rate.Add() [atomic]

Tick() ──────────────────────────────────────────► ring.ReadBatch() [atomic]
                                                    packet btree insert [no lock]
                                                    NAK btree delete [no lock]
                                                    periodicACK [no lock]
                                                    periodicNAK [no lock]
                                                    delivery [no lock]
                                                    rate stats [atomic]
```

**Solution**: Push() and Tick() never compete - Push() writes to ring (atomic), Tick() owns btrees exclusively.

### 10.3 Timing Analysis

| Operation | Current | New |
|-----------|---------|-----|
| Push() lock wait | 0-500µs (contention) | 0 (no lock) |
| Push() execution | ~10µs | ~1µs (ring write) |
| Tick() lock wait | 0-500µs (contention) | 0 (no lock) |
| Tick() ring drain | N/A | ~100µs (batch 100 pkts) |
| Total per packet | ~520µs worst case | ~1µs average |

---

## 11. Feature Flags and Backwards Compatibility

This section defines which features should be configurable via feature flags to maintain backwards compatibility and enable parallel comparison testing (per `integration_testing_matrix_design.md` and `parallel_comparison_test_design.md`).

### 11.1 Design Principles

1. **Parallel Testing**: Must be able to run old and new implementations side-by-side for comparison
2. **Incremental Rollout**: Enable new features gradually in production
3. **Fallback Safety**: Easy to disable new features if issues arise
4. **Minimal Code Duplication**: Use clean branching, not copy-paste implementations

### 11.2 Feature Flag Summary

| Feature | Flag Required? | Rationale |
|---------|----------------|-----------|
| Rate metrics atomics | **NO** | Benefits both old and new paths; pure improvement |
| Buffer lifetime extension (zero-copy) | **OPTIONAL** | Benefits ALL paths; flag for API contract change |
| Lock-free ring buffer | **YES** | Core architectural change requiring comparison |
| Event loop vs Tick() | **YES** | Fundamentally different execution models |
| Packet struct changes | **NO** | Required for buffer tracking; backwards compatible |

**Note on Buffer Lifetime Extension**: Unlike the ring buffer and event loop which are specifically for the lockless architecture, zero-copy buffers benefit **all implementation paths** equally:
- io_uring AND standard recv() paths
- btree AND linked list packet stores
- Tick() AND event loop processing models

The flag is primarily for API contract safety (applications must not hold packet data after callback returns), not for comparing lockless vs legacy implementations.

### 11.3 Features WITHOUT Flags (Always On)

#### 11.3.1 Rate Metrics Atomics

**Rationale**: Atomic rate calculations improve performance for BOTH legacy and new implementations:
- No lock contention when updating metrics
- Simpler code (no lock/unlock pairs)
- Zero behavioral change - just faster

**Implementation**: Replace all rate metric fields with atomics globally. This is a transparent optimization that benefits the existing tick()-based approach.

```go
// Before (with locks)
r.mu.Lock()
r.packetsReceived++
r.mu.Unlock()

// After (atomic - works with both old and new)
r.packetsReceived.Add(1)
```

#### 11.3.2 Packet Structure Changes

**Rationale**: The packet struct changes are backwards compatible because:

1. **New fields added** (no removal of existing fields):
   - `recvBuffer *[]byte` - tracks original pool buffer (nil in legacy path)
   - `n int` - tracks received bytes (0 in legacy path)

2. **`Payload` field behavior** (see Section 6.6):
   - Legacy path: `Payload *[]byte` is set to a NEW copied slice
   - Zero-copy path: `Payload` is NOT used; use `GetPayload()` instead

3. **`GetPayload()` works for both paths**:
   - If `recvBuffer != nil`: returns computed slice `(*recvBuffer)[HeaderSize:n]`
   - If `recvBuffer == nil` and `Payload != nil`: returns `*Payload` (legacy)

4. **Memory optimization** (optional, future):
   - Once all code uses `GetPayload()`, the `Payload` field can be removed
   - Saves 16-24 bytes per packet
   - Requires audit of all direct `Payload` access

**Current design (Section 6.6)**: Keeps `Payload` for backwards compatibility, adds `recvBuffer` + `n` for zero-copy. The `GetPayload()` function abstracts the difference.

### 11.4 Features WITH Flags

#### 11.4.1 Lock-Free Ring Buffer

**Config Field**: `UsePacketRing bool`

**Implementation**: Use **function dispatch** (not if/else) for hot paths. This avoids a branch on every packet. See Section 5.5.1 for the pattern.

**Branch Points (using function dispatch)**:

1. **receiver.Push()** - Packet arrival path:

```go
// Function dispatch is set up in NewReceiver() (see Section 5.4):
//   r.pushFn = r.pushToRing   (when UsePacketRing=true)
//   r.pushFn = r.pushWithLock (when UsePacketRing=false)

func (r *receiver) Push(pkt *packet.Packet) {
    // Update rate metrics (always atomic - Phase 1)
    r.packetsReceived.Add(1)
    r.bytesReceived.Add(uint64(pkt.Len()))

    // Function dispatch - NO if/else check per packet!
    r.pushFn(pkt)
}

// LEGACY path (selected when UsePacketRing=false)
func (r *receiver) pushWithLock(pkt *packet.Packet) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.packetStore.ReplaceOrInsert(pkt)
    r.nakBtree.Delete(pkt.Header.PacketSequenceNumber)
}

// NEW path (selected when UsePacketRing=true)
func (r *receiver) pushToRing(pkt *packet.Packet) {
    producerID := uint64(pkt.Header().PacketSequenceNumber.Val())
    if !r.packetRing.WriteWithBackoff(producerID, pkt, r.writeConfig) {
        r.droppedPackets.Add(1)
        r.releasePacketFully(pkt)
    }
}
```

2. **Consumer loop** - Processing path:

```go
// Function dispatch is set up in startReceiver() (see Section 5.6.1):
//   go r.eventLoop(ctx)  (when UseEventLoop=true, requires UsePacketRing=true)
//   go r.tickLoop(ctx)   (when UseEventLoop=false)

// The loop selection happens ONCE at startup, not per-iteration!
```

**Why function dispatch over if/else?**
- Hot path (Push) is called for EVERY packet (~8,600/sec at 100Mb/s)
- Function dispatch: indirect call overhead (~1ns)
- If/else: branch + possible misprediction (~2-5ns)
- At scale, these small differences matter
```

**Config**:

```go
type Config struct {
    // ... existing fields ...

    // Lock-Free Ring Configuration
    UsePacketRing    bool  // Enable lock-free ring buffer (default: false)
    PacketRingSize   int   // Ring capacity (default: 1024, must be power of 2)
    PacketRingShards int   // Number of shards (default: 4, must be power of 2)
}
```

#### 11.4.2 Event Loop vs Tick()

**Config Field**: `UseEventLoop bool`

**Branch Point**: Connection setup

```go
func (c *Connection) startReceiver() {
    if c.config.UseEventLoop {
        // NEW: Continuous event loop
        go c.receiver.eventLoop(c.ctx)
    } else {
        // LEGACY: Timer-based tick loop
        go c.receiver.tickLoop(c.ctx)
    }
}

// LEGACY: Timer-based approach
func (r *receiver) tickLoop(ctx context.Context) {
    ticker := time.NewTicker(r.config.TickInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.Tick(time.Now())
        }
    }
}

// NEW: Continuous event loop (from section 9.3)
func (r *receiver) eventLoop(ctx context.Context) {
    // ... as defined in section 9.3 ...
}
```

**Dependency**: `UseEventLoop` requires `UsePacketRing`:

```go
func (c *Config) Validate() error {
    if c.UseEventLoop && !c.UsePacketRing {
        return errors.New("UseEventLoop requires UsePacketRing=true")
    }
    return nil
}
```

#### 11.4.3 Buffer Lifetime Extension (Zero-Copy)

**Config Field**: `UseZeroCopyBuffers bool`

**Key Insight**: Unlike other flags, this optimization benefits **ALL implementation paths**:

| Receive Path | Packet Store | Processing | Benefit |
|--------------|--------------|------------|---------|
| io_uring | btree | Tick() | ✅ No copy |
| io_uring | btree | Event loop | ✅ No copy |
| io_uring | linked list | Tick() | ✅ No copy |
| Standard recv() | btree | Tick() | ✅ No copy |
| Standard recv() | btree | Event loop | ✅ No copy |
| Standard recv() | linked list | Tick() | ✅ No copy |

**Branch Points** (same code handles all paths):

1. **Packet deserialization** (both io_uring and standard paths):

```go
// Works for BOTH io_uring completion AND standard readLoop
func deserializePacket(buf *[]byte, n int, addr net.Addr, config *Config) *packet.Packet {
    pkt := packetPool.Get().(*packet.Packet)

    if config.UseZeroCopyBuffers {
        // NEW: Single call - tracks buffer AND parses (see 6.7.3)
        err := pkt.UnmarshalZeroCopy(buf, n, addr)
        // Buffer lifetime extended - returned in delivery
    } else {
        // LEGACY: Copy buffer and return immediately
        err := pkt.UnmarshalCopy(addr, (*buf)[:n])
        *buf = (*buf)[:0]
        bufferPool.Put(buf)
    }
    return pkt
}
```

2. **Packet delivery** (works with ANY packet store):

```go
// Works with btree OR linked list packet stores
func (r *receiver) deliverPacket(pkt *packet.Packet) {
    // Deliver to application
    r.deliverCallback(pkt)

    // Use releasePacketFully which handles:
    // - Buffer release (if UseZeroCopyBuffers is enabled and buffer exists)
    // - Packet decommission (always)
    // The method is safe even if Payload is nil (copied data, not zero-copy)
    r.releasePacketFully(pkt)
}
```

**Note**: `releasePacketFully()` checks if `Payload` is non-nil before attempting buffer release, making it safe for both zero-copy and legacy (copied) paths.

**Risk Note**: Zero-copy changes buffer ownership semantics. The application callback receives a buffer that will be recycled. Applications must copy data if they need to retain it.

**Recommendation**: Consider making this **always on** (no flag) if API contract can be documented clearly. The performance benefit is significant and applies universally.

### 11.5 Feature Flag Combinations

Valid configuration combinations for testing:

| UsePacketRing | UseEventLoop | UseZeroCopy | Description |
|---------------|--------------|-------------|-------------|
| false | false | false | **Legacy**: Original implementation |
| false | false | true | **Legacy + zero-copy** (immediate perf win) |
| true | false | false | Ring buffer + tick() |
| true | false | true | Ring buffer + tick() + zero-copy |
| true | true | false | Event loop |
| true | true | true | **Full lockless** (maximum perf) |
| false | true | * | **Invalid**: Event loop requires ring |

**Key Insight**: `UseZeroCopyBuffers` is **orthogonal** to the other flags:

```
                        UseZeroCopyBuffers
                    false           true
                ┌───────────┬───────────────┐
UsePacketRing   │           │               │
    false       │  Legacy   │ Legacy+ZeroCopy│  ← Both valid, zero-copy helps!
                │           │               │
                ├───────────┼───────────────┤
UsePacketRing   │           │               │
    true        │  Ring     │ Ring+ZeroCopy │  ← Both valid, zero-copy helps!
                │           │               │
                └───────────┴───────────────┘
```

This means you can enable zero-copy buffers **immediately** on the existing legacy implementation for an instant performance boost, before implementing any of the lockless changes.

### 11.6 Integration Test Matrix

Per `integration_testing_matrix_design.md`, add these new test dimensions:

```go
var locklessTestConfigs = []Config{
    // === Zero-Copy Testing (Independent of Lockless) ===
    // These can be deployed immediately for instant performance gain
    {UsePacketRing: false, UseEventLoop: false, UseZeroCopyBuffers: false},  // Legacy baseline
    {UsePacketRing: false, UseEventLoop: false, UseZeroCopyBuffers: true},   // Legacy + zero-copy

    // === Lockless Architecture Testing ===
    // Progressive enablement of lockless features
    {UsePacketRing: true, UseEventLoop: false, UseZeroCopyBuffers: false},   // Ring only
    {UsePacketRing: true, UseEventLoop: false, UseZeroCopyBuffers: true},    // Ring + zero-copy
    {UsePacketRing: true, UseEventLoop: true, UseZeroCopyBuffers: false},    // Event loop
    {UsePacketRing: true, UseEventLoop: true, UseZeroCopyBuffers: true},     // Full lockless
}

func TestLocklessComparison(t *testing.T) {
    for _, cfg := range locklessTestConfigs {
        t.Run(cfg.String(), func(t *testing.T) {
            // Run identical workload
            // Compare: throughput, latency, CPU, memory
        })
    }
}

// Separate test to isolate zero-copy benefit on legacy implementation
func TestZeroCopyOnLegacy(t *testing.T) {
    legacyCfg := Config{UsePacketRing: false, UseEventLoop: false, UseZeroCopyBuffers: false}
    legacyZeroCopyCfg := Config{UsePacketRing: false, UseEventLoop: false, UseZeroCopyBuffers: true}

    // Run both and compare memory allocations, throughput
    // This proves zero-copy value BEFORE any lockless changes
}
```

### 11.7 Parallel Comparison Test Design

Enable A/B testing by running connections with different configurations simultaneously:

```go
func TestParallelLocklessComparison(t *testing.T) {
    // Connection A: Legacy
    cfgA := Config{UsePacketRing: false, UseEventLoop: false}
    connA := NewConnection(cfgA)

    // Connection B: New lockless
    cfgB := Config{UsePacketRing: true, UseEventLoop: true}
    connB := NewConnection(cfgB)

    // Send identical streams to both
    // Compare metrics
}
```

### 11.8 Config.go Changes

```go
type Config struct {
    // ... existing fields ...

    // === Lockless Architecture Feature Flags ===

    // UsePacketRing enables lock-free ring buffer for packet arrival.
    // When true, packets are written to a ring buffer and batch-processed.
    // When false, packets are inserted directly into btree with locks.
    // Default: false (legacy behavior)
    UsePacketRing bool

    // PacketRingSize is the total capacity of the lock-free ring.
    // Only used when UsePacketRing=true.
    // Default: 1000
    PacketRingSize int

    // PacketRingShards is the number of ring shards (must be power of 2).
    // Only used when UsePacketRing=true.
    // Default: 4
    PacketRingShards int

    // UseEventLoop enables continuous event loop instead of timer-based Tick().
    // Requires UsePacketRing=true.
    // When true, receiver runs a tight loop processing packets as they arrive.
    // When false, receiver uses timer-triggered Tick() calls.
    // Default: false (legacy behavior)
    UseEventLoop bool

    // UseZeroCopyBuffers enables extended buffer lifetime for zero-copy.
    // When true, packet payloads reference pooled buffers directly.
    // When false, packet payloads are copied and buffers returned immediately.
    // Default: false (legacy behavior)
    UseZeroCopyBuffers bool

    // === Adaptive Backoff Configuration (Event Loop only) ===

    // BackoffColdStartPackets is the number of packets to process before
    // enabling rate-based adaptive backoff. During cold start, minimal
    // sleep is used.
    // Only used when UseEventLoop=true.
    // Default: 1000
    BackoffColdStartPackets int

    // BackoffMinSleep is the minimum sleep duration when no packets available.
    // Only used when UseEventLoop=true.
    // Default: 1µs
    BackoffMinSleep time.Duration

    // BackoffMaxSleep is the maximum sleep duration when no packets available.
    // Only used when UseEventLoop=true.
    // Default: 1ms
    BackoffMaxSleep time.Duration
}

// DefaultConfig returns configuration with legacy behavior
func DefaultConfig() Config {
    return Config{
        // ... existing defaults ...

        // Lockless features default to OFF for backwards compatibility
        UsePacketRing:           false,
        PacketRingSize:          1024,  // Must be power of 2
        PacketRingShards:        4,     // Must be power of 2
        UseEventLoop:            false,
        UseZeroCopyBuffers:      false,
        BackoffColdStartPackets: 1000,
        BackoffMinSleep:         1 * time.Microsecond,
        BackoffMaxSleep:         1 * time.Millisecond,
    }
}

// Validate checks configuration consistency
func (c *Config) Validate() error {
    // ... existing validation ...

    // Event loop requires packet ring
    if c.UseEventLoop && !c.UsePacketRing {
        return errors.New("UseEventLoop requires UsePacketRing=true")
    }

    // Ring configuration validation (must match Section 5.2)
    if c.UsePacketRing {
        if c.PacketRingSize < 64 || c.PacketRingSize > 8192 {
            return errors.New("PacketRingSize must be between 64 and 8192")
        }
        if c.PacketRingSize&(c.PacketRingSize-1) != 0 {
            return errors.New("PacketRingSize must be a power of 2")
        }
        if c.PacketRingShards < 1 || (c.PacketRingShards&(c.PacketRingShards-1)) != 0 {
            return errors.New("PacketRingShards must be a power of 2")
        }
    }

    return nil
}
```

### 11.9 Migration Path

Recommended rollout sequence (universal improvements FIRST):

```
┌─────────────────────────────────────────────────────────────────────────┐
│ PHASE 1: Rate Atomics (No flag needed) - UNIVERSAL                      │
├─────────────────────────────────────────────────────────────────────────┤
│ ├── Update rate metrics to use atomics                                  │
│ ├── Run all existing tests                                              │
│ └── Deploy - transparent improvement for ALL paths                      │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ PHASE 2: Zero-Copy Buffers (UseZeroCopyBuffers flag) - UNIVERSAL        │
├─────────────────────────────────────────────────────────────────────────┤
│ ├── Implement buffer lifetime extension                                 │
│ ├── Works with ALL paths (io_uring, standard, btree, linked list)       │
│ ├── Document application API contract (don't hold packet data)          │
│ ├── Test memory safety extensively                                      │
│ ├── Deploy with flag=false, validate correctness                        │
│ └── Enable flag=true in production ← IMMEDIATE PERF WIN!                │
│                                                                          │
│ At this point, existing legacy implementation is FASTER                  │
│ without any architectural changes!                                       │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ PHASE 3: Packet Ring (UsePacketRing flag) - LOCKLESS ARCHITECTURE       │
├─────────────────────────────────────────────────────────────────────────┤
│ ├── Implement ring buffer integration                                   │
│ ├── Run parallel comparison tests (legacy vs ring)                      │
│ ├── Validate correctness with flag=false                                │
│ ├── Enable in staging, compare metrics                                  │
│ └── Gradual production rollout                                          │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ PHASE 4: Event Loop (UseEventLoop flag) - LOCKLESS ARCHITECTURE         │
├─────────────────────────────────────────────────────────────────────────┤
│ ├── Implement event loop (requires UsePacketRing=true)                  │
│ ├── Run parallel comparison tests (tick vs event loop)                  │
│ ├── Profile CPU/latency differences                                     │
│ └── Enable for latency-sensitive use cases                              │
└─────────────────────────────────────────────────────────────────────────┘
```

**Key Insight**: Phases 1-2 deliver value to the EXISTING implementation. You can stop after Phase 2 and still have significant performance improvements!

### 11.10 Backwards Compatibility Matrix

| Existing Feature | Compatible With Lockless? | Notes |
|------------------|---------------------------|-------|
| Linked list packet store | **YES** | Different code path, same interface |
| btree packet store | **YES** | Works with both ring and direct insert |
| io_uring receive | **YES** | Completion handler branches on config |
| Standard receive (no io_uring) | **YES** | Same Push() interface |
| NAK btree | **YES** | Works with both approaches |
| TSBPD delivery | **YES** | Delivery logic unchanged |
| Rate statistics | **YES** | Atomics work everywhere |

---

## 12. Implementation Plan

The implementation is organized so that **universal improvements** (benefits ALL paths) come first, followed by lockless-specific changes. Each phase includes a **validation checkpoint** where we run integration tests before proceeding.

### Implementation Strategy

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         IMPLEMENTATION APPROACH                              │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. UNIVERSAL IMPROVEMENTS FIRST                                             │
│     - Rate atomics + zero-copy benefit ALL existing code                     │
│     - Lower risk - isolated changes                                          │
│     - Immediate production value                                             │
│     - Validates approach before complex changes                              │
│                                                                              │
│  2. VALIDATE BETWEEN PHASES                                                  │
│     - Run integration tests after each phase (see testing docs below)        │
│     - Use parallel comparison for identical-network validation               │
│     - Ensure no regressions in Tier 1/2/3 tests                              │
│                                                                              │
│  3. LOCKLESS CHANGES LAST                                                    │
│     - More complex, higher risk                                              │
│     - Built on validated foundation                                          │
│     - Feature-flagged for safe rollout                                       │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Integration Testing References

Each phase includes a **Validation Checkpoint** that uses the existing integration testing framework:

| Document | Purpose | Key Features |
|----------|---------|--------------|
| [`integration_testing_design.md`](./integration_testing_design.md) | Core framework | Fail-safe principles, loss recovery validation, 12-24h stability tests |
| [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md) | Matrix-based tests | Tier 1/2/3, RTT profiles (R0-R300), loss patterns, config variants |
| [`parallel_comparison_test_design.md`](./parallel_comparison_test_design.md) | Side-by-side comparison | Two pipelines, identical network, metrics comparison |

**Test Tiers** (from `integration_testing_matrix_design.md`):
- **Tier 1 (Core)**: ~20 tests, essential coverage, runs on every PR
- **Tier 2 (Extended)**: ~50 tests, broader coverage, daily CI
- **Tier 3 (Comprehensive)**: ~150 tests, full matrix, nightly/weekly

**Key Test Patterns**:
- `Net-Starlink-*`: Starlink-style 60ms outages (gap recovery validation)
- `Parallel-*-vs-*`: Same network, different configs (direct comparison)
- `Net-Clean-*`: No impairment (baseline performance)
- `Net-Loss-L{2,5,10,15}-*`: Uniform packet loss (ARQ stress test)

---

### Phase 1: Rate Metrics Atomics ✅ COMPLETE [NO FLAG - Always On]

**Status**: ✅ **IMPLEMENTED AND VALIDATED** (December 2024)

**Goal**: Eliminate lock contention in rate calculations. Benefits ALL paths.

**Reference**: `rate_metrics_performance_design.md`

**Implementation Tracking**: [`lockless_phase1_implementation.md`](./lockless_phase1_implementation.md)

#### 🎉 Integration Test Results

All isolation tests pass with accurate rate metrics:

| Test | Features Enabled | Target | Actual Rate | Status |
|------|------------------|--------|-------------|--------|
| `Isolation-5M-Control` | Baseline | 5 Mbps | 4.77 Mbps | ✅ PASS |
| `Isolation-5M-Server-Btree` | Btree packet store | 5 Mbps | 4.77 Mbps | ✅ PASS |
| `Isolation-5M-Full` | io_uring + Btree + NAK Btree | 5 Mbps | 4.77 Mbps | ✅ PASS |

**Key Accomplishments:**
- ✅ Lock-free rate calculations using `atomic.Uint64` and `math.Float64bits()`
- ✅ 6 new rate metrics exported via Prometheus (`gosrt_recv_rate_*`, `gosrt_send_rate_*`)
- ✅ Rate validation integrated into `analysis.go` for post-test verification
- ✅ All 57 metrics tests pass, all integration tests pass

#### 1.1 Architecture Decision: Move Rate Metrics to ConnectionMetrics

**Rationale**: The `metrics_and_statistics_design.md` established a unified metrics architecture using `ConnectionMetrics` in `metrics/metrics.go`. Moving rate metrics to this struct:

1. **Consistency** - All connection metrics in one place
2. **Prometheus integration** - Already exposed via custom high-performance `metrics/handler.go`
3. **Tooling support** - `tools/metrics-audit/main.go` verifies completeness
4. **Clean separation** - Congestion control logic separate from metrics storage

**Important: GoSRT's Custom Prometheus Handler**

GoSRT does NOT use the `promauto` or `prometheus/client_golang` libraries for metrics. Instead, it uses a custom high-performance HTTP handler (`metrics/handler.go`) because:

| promauto Limitation | GoSRT Solution |
|---------------------|----------------|
| Can't read metric values back | Atomic fields with `.Load()` |
| Memory overhead from descriptors | Direct `strings.Builder` output |
| Global registry conflicts | Per-connection `ConnectionMetrics` structs |
| Allocation per scrape | `sync.Pool` for 64KB pre-allocated buffers |

**Pattern summary:**
- Store values in `atomic.Uint64` fields in `ConnectionMetrics`
- Float values encoded via `math.Float64bits()`, decoded via `math.Float64frombits()`
- Export via `writeGauge()` / `writeCounterIfNonZero()` helpers in `handler.go`
- Verify with `go run tools/metrics-audit/main.go`

#### 1.2 Files to Modify

| File | Changes |
|------|---------|
| `metrics/metrics.go` | Add rate counter fields to `ConnectionMetrics`, add getter helpers |
| `metrics/handler.go` | Export new rate metrics to Prometheus |
| `metrics/handler_test.go` | Add tests for new rate metric exports |
| `congestion/live/receive.go` | Remove embedded `rate` struct, use `*metrics.ConnectionMetrics` |
| `congestion/live/send.go` | Same - use shared ConnectionMetrics |
| `congestion/live/fake.go` | Update rate handling |
| `contrib/integration_testing/analysis.go` | Add `VerifyRateMetrics()` validation function |

#### 1.3 Detailed Steps

**Step 1: Add rate fields to ConnectionMetrics (`metrics/metrics.go`)**

```go
// File: metrics/metrics.go

type ConnectionMetrics struct {
    // ... existing fields ...

    // === Rate Calculation Fields (Phase 1: Lockless Design) ===
    // These replace the embedded `rate struct` in congestion/live/receive.go and send.go

    // Receiver rate counters (atomic for lock-free access)
    RecvRatePeriodUs      atomic.Uint64 // Rate calculation period (microseconds)
    RecvRateLastUs        atomic.Uint64 // Last rate calculation time (microseconds)
    RecvRatePackets       atomic.Uint64 // Packets in current period
    RecvRateBytes         atomic.Uint64 // Bytes in current period
    RecvRateBytesRetrans  atomic.Uint64 // Retransmit bytes in current period

    // Receiver computed rates (stored as uint64, use Float64frombits/Float64bits)
    // These are updated atomically via CAS during rate calculation
    RecvRatePacketsPerSec atomic.Uint64 // Packets/second (float64 bits)
    RecvRateBytesPerSec   atomic.Uint64 // Bytes/second (float64 bits)
    RecvRatePktRetransRate atomic.Uint64 // Retransmission rate % (float64 bits)

    // Sender rate counters
    SendRatePeriodUs      atomic.Uint64
    SendRateLastUs        atomic.Uint64
    SendRateBytes         atomic.Uint64
    SendRateBytesSent     atomic.Uint64
    SendRateBytesRetrans  atomic.Uint64

    // Sender computed rates
    SendRateEstInputBW    atomic.Uint64 // Estimated input bandwidth (float64 bits)
    SendRateEstSentBW     atomic.Uint64 // Estimated sent bandwidth (float64 bits)
    SendRatePktRetransRate atomic.Uint64 // Retransmission rate % (float64 bits)

    // Light ACK threshold counter (replaces nPackets in receiver)
    RecvLightACKCounter   atomic.Uint64 // Packets since last ACK (for light ACK threshold)
}

// Helper getter functions for float64 rates stored as uint64 bits
// These encapsulate the Float64frombits() conversion for cleaner code

// GetRecvRatePacketsPerSec returns packets per second as float64
func (m *ConnectionMetrics) GetRecvRatePacketsPerSec() float64 {
    return math.Float64frombits(m.RecvRatePacketsPerSec.Load())
}

// GetRecvRateBytesPerSec returns bytes per second as float64
func (m *ConnectionMetrics) GetRecvRateBytesPerSec() float64 {
    return math.Float64frombits(m.RecvRateBytesPerSec.Load())
}

// GetRecvRateMbps returns receive rate in megabits per second
func (m *ConnectionMetrics) GetRecvRateMbps() float64 {
    return m.GetRecvRateBytesPerSec() * 8 / 1024 / 1024
}

// GetRecvRateRetransPercent returns retransmission percentage
func (m *ConnectionMetrics) GetRecvRateRetransPercent() float64 {
    return math.Float64frombits(m.RecvRatePktRetransRate.Load())
}

// GetSendRateEstInputBW returns estimated input bandwidth in bytes/sec
func (m *ConnectionMetrics) GetSendRateEstInputBW() float64 {
    return math.Float64frombits(m.SendRateEstInputBW.Load())
}

// GetSendRateEstSentBW returns estimated sent bandwidth in bytes/sec
func (m *ConnectionMetrics) GetSendRateEstSentBW() float64 {
    return math.Float64frombits(m.SendRateEstSentBW.Load())
}

// GetSendRateMbps returns sent rate in megabits per second
func (m *ConnectionMetrics) GetSendRateMbps() float64 {
    return m.GetSendRateEstSentBW() * 8 / 1024 / 1024
}

// GetSendRateRetransPercent returns sender retransmission percentage
func (m *ConnectionMetrics) GetSendRateRetransPercent() float64 {
    return math.Float64frombits(m.SendRatePktRetransRate.Load())
}
```

**Why helper getters?**

1. **Encapsulation** - Hide `math.Float64frombits()` from callers
2. **Convenience** - `GetRecvRateMbps()` does both decode AND unit conversion
3. **Consistency** - Same pattern as other metric accessors
4. **Cleaner Stats()** - `receiver.Stats()` and `sender.Stats()` can use getters

**Step 2: Export rate metrics via Prometheus (`metrics/handler.go`)**

**Why NOT promauto?** GoSRT uses a custom high-performance HTTP handler instead of the `promauto` library:

1. **Read-back limitation**: `promauto` metrics can't be read back - you can't call `.Get()` on a Gauge
2. **Memory overhead**: `promauto` creates descriptor objects for each metric
3. **Global registration**: `promauto` uses a global registry which conflicts with multiple instances
4. **Performance**: Custom handler uses `sync.Pool` for `strings.Builder` to avoid allocations

**GoSRT's pattern** (see `metrics/helpers.go` and `metrics/handler.go`):
- Atomic counters in `ConnectionMetrics` struct (`.Load()` for reads)
- Custom `writeGauge()` and `writeCounterIfNonZero()` helpers
- `sync.Pool` for `strings.Builder` (pre-allocated 64KB buffers)
- Direct Prometheus text format output (no intermediate objects)

```go
// File: metrics/handler.go

// In MetricsHandler(), inside the per-connection loop, add rate metrics:
// (This follows the existing pattern - see handler.go lines 183-205 for gauge examples)

// ========== Rate Metrics (Phase 1: Lockless Design) ==========
// Float values stored as uint64 using math.Float64bits/Float64frombits

// Receiver rate metrics
writeGauge(b, "gosrt_recv_rate_packets_per_sec",
    math.Float64frombits(metrics.RecvRatePacketsPerSec.Load()),
    "socket_id", socketIdStr, "instance", instanceName)

writeGauge(b, "gosrt_recv_rate_bytes_per_sec",
    math.Float64frombits(metrics.RecvRateBytesPerSec.Load()),
    "socket_id", socketIdStr, "instance", instanceName)

writeGauge(b, "gosrt_recv_rate_retrans_percent",
    math.Float64frombits(metrics.RecvRatePktRetransRate.Load()),
    "socket_id", socketIdStr, "instance", instanceName)

// Sender rate metrics
writeGauge(b, "gosrt_send_rate_input_bandwidth_bps",
    math.Float64frombits(metrics.SendRateEstInputBW.Load()),
    "socket_id", socketIdStr, "instance", instanceName)

writeGauge(b, "gosrt_send_rate_sent_bandwidth_bps",
    math.Float64frombits(metrics.SendRateEstSentBW.Load()),
    "socket_id", socketIdStr, "instance", instanceName)

writeGauge(b, "gosrt_send_rate_retrans_percent",
    math.Float64frombits(metrics.SendRatePktRetransRate.Load()),
    "socket_id", socketIdStr, "instance", instanceName)
```

**Key differences from promauto pattern:**
- No metric registration - metrics are written directly to the HTTP response
- `writeGauge()` takes `float64` value directly (use `math.Float64frombits()` to decode)
- Labels are variadic string pairs: `"key1", "value1", "key2", "value2", ...`
- Minimal allocations via `sync.Pool` for `strings.Builder`

**Step 2b: Add tests for rate metrics (`metrics/handler_test.go`)**

The existing test `TestPrometheusAllFieldsExported` excludes rate metrics because they're calculated values, not cumulative counters. We need to add specific tests for the new rate gauges:

```go
// File: metrics/handler_test.go

// TestRateMetricsExported verifies rate metrics are correctly exported to Prometheus
func TestRateMetricsExported(t *testing.T) {
    socketId := uint32(0xRATE1234)
    m := newTestConnectionMetrics()
    RegisterConnection(socketId, m, "test-instance")
    defer UnregisterConnection(socketId, CloseReasonGraceful)

    // Set known rate values (stored as float64 bits)
    // Receiver rates
    m.RecvRatePacketsPerSec.Store(math.Float64bits(1000.5))   // 1000.5 pkt/s
    m.RecvRateBytesPerSec.Store(math.Float64bits(1250000.0))  // 1.25 MB/s (~10 Mbps)
    m.RecvRatePktRetransRate.Store(math.Float64bits(2.5))     // 2.5% retrans

    // Sender rates
    m.SendRateEstInputBW.Store(math.Float64bits(1500000.0))   // 1.5 MB/s input
    m.SendRateEstSentBW.Store(math.Float64bits(1450000.0))    // 1.45 MB/s sent
    m.SendRatePktRetransRate.Store(math.Float64bits(1.8))     // 1.8% retrans

    output := getPrometheusOutput(t)

    // Verify receiver rate metrics present
    require.Contains(t, output, "gosrt_recv_rate_packets_per_sec")
    require.Contains(t, output, "gosrt_recv_rate_bytes_per_sec")
    require.Contains(t, output, "gosrt_recv_rate_retrans_percent")

    // Verify sender rate metrics present
    require.Contains(t, output, "gosrt_send_rate_input_bandwidth_bps")
    require.Contains(t, output, "gosrt_send_rate_sent_bandwidth_bps")
    require.Contains(t, output, "gosrt_send_rate_retrans_percent")

    // Verify socket_id label
    require.Contains(t, output, `socket_id="0xrate1234"`)
}

// TestRateMetricsAccuracy verifies rate metric values are correctly encoded/decoded
func TestRateMetricsAccuracy(t *testing.T) {
    socketId := uint32(0xACCU1234)
    m := newTestConnectionMetrics()
    RegisterConnection(socketId, m, "")
    defer UnregisterConnection(socketId, CloseReasonGraceful)

    // Set precise rate values
    expectedPPS := 8642.75
    expectedBPS := 12500000.0  // 12.5 MB/s = 100 Mbps

    m.RecvRatePacketsPerSec.Store(math.Float64bits(expectedPPS))
    m.RecvRateBytesPerSec.Store(math.Float64bits(expectedBPS))

    output := getPrometheusOutput(t)

    // Parse the output to verify values
    // Rate metrics are floats, so look for the approximate value
    // gosrt_recv_rate_packets_per_sec{socket_id="0xaccu1234"} 8642.750000
    require.Contains(t, output, "8642.75")
    require.Contains(t, output, "12500000")
}

// TestRateMetricsZeroValues verifies zero rates are exported correctly
func TestRateMetricsZeroValues(t *testing.T) {
    socketId := uint32(0xZERO1234)
    m := newTestConnectionMetrics()
    RegisterConnection(socketId, m, "")
    defer UnregisterConnection(socketId, CloseReasonGraceful)

    // Rate fields default to 0 (math.Float64bits(0) = 0)
    // Verify zero values are still exported (not omitted)
    output := getPrometheusOutput(t)

    // Should see the metric with value 0
    require.Contains(t, output, "gosrt_recv_rate_packets_per_sec")
    require.Contains(t, output, "gosrt_recv_rate_bytes_per_sec")
}

// TestGetterHelpers verifies the float64 getter helpers work correctly
func TestGetterHelpers(t *testing.T) {
    m := newTestConnectionMetrics()

    // Set values using raw atomic
    m.RecvRatePacketsPerSec.Store(math.Float64bits(1234.5))
    m.RecvRateBytesPerSec.Store(math.Float64bits(1310720.0)) // 1.25 MB/s

    // Verify getters decode correctly
    require.InDelta(t, 1234.5, m.GetRecvRatePacketsPerSec(), 0.001)
    require.InDelta(t, 1310720.0, m.GetRecvRateBytesPerSec(), 0.001)

    // Verify Mbps conversion (1310720 bytes/s * 8 / 1024 / 1024 = 10 Mbps)
    require.InDelta(t, 10.0, m.GetRecvRateMbps(), 0.001)
}
```

**Update `intentionallyNotExported` map:**

The existing `TestPrometheusAllFieldsExported` test has an `intentionallyNotExported` map. Add the new rate fields:

```go
// In TestPrometheusAllFieldsExported:
intentionallyNotExported := map[string]bool{
    // ... existing entries ...

    // ========== Phase 1: Rate Metrics (float64 stored as uint64 bits) ==========
    // These are gauge values, exported separately via writeGauge()
    // Not included in the counter export loop
    "RecvRatePeriodUs":        true,  // Internal timing, not exported
    "RecvRateLastUs":          true,  // Internal timing, not exported
    "RecvRatePackets":         true,  // Raw counter for calculation, not exported
    "RecvRateBytes":           true,  // Raw counter for calculation, not exported
    "RecvRateBytesRetrans":    true,  // Raw counter for calculation, not exported
    "RecvRatePacketsPerSec":   true,  // Exported as gauge (tested in TestRateMetricsExported)
    "RecvRateBytesPerSec":     true,  // Exported as gauge
    "RecvRatePktRetransRate":  true,  // Exported as gauge
    "SendRatePeriodUs":        true,
    "SendRateLastUs":          true,
    "SendRateBytes":           true,
    "SendRateBytesSent":       true,
    "SendRateBytesRetrans":    true,
    "SendRateEstInputBW":      true,  // Exported as gauge
    "SendRateEstSentBW":       true,  // Exported as gauge
    "SendRatePktRetransRate":  true,  // Exported as gauge
    "RecvLightACKCounter":     true,  // Internal counter, not exported
}
```

**Step 3: Update congestion/live/receive.go to use ConnectionMetrics**

```go
// File: congestion/live/receive.go

// BEFORE:
type receiver struct {
    // ... other fields ...

    nPackets uint  // For light ACK threshold

    rate struct {
        last   uint64 // microseconds
        period uint64
        packets      uint64
        bytes        uint64
        bytesRetrans uint64
        packetsPerSecond float64
        bytesPerSecond   float64
        pktRetransRate float64
    }

    lock sync.RWMutex
}

// Push increments under lock:
r.lock.Lock()
r.nPackets++
r.rate.packets++
r.rate.bytes += uint64(pktLen)
r.lock.Unlock()

// AFTER:
type receiver struct {
    // ... other fields ...
    metrics *metrics.ConnectionMetrics  // Shared metrics (already exists)
    // Remove nPackets and rate struct - use metrics.* instead
}

// Push increments atomically (NO LOCK):
r.metrics.RecvLightACKCounter.Add(1)
r.metrics.RecvRatePackets.Add(1)
r.metrics.RecvRateBytes.Add(uint64(pktLen))
```

**Step 3b: Update receiver.Stats() and sender.Stats() to use getters**

```go
// File: congestion/live/receive.go

// BEFORE (reads from embedded rate struct under lock):
func (r *receiver) Stats() congestion.RecvStats {
    r.lock.RLock()
    mbpsBandwidth := r.rate.bytesPerSecond * 8 / 1024 / 1024
    pktRetransRate := r.rate.pktRetransRate
    r.lock.RUnlock()
    // ... populate stats ...
}

// AFTER (reads from ConnectionMetrics atomically, NO LOCK):
func (r *receiver) Stats() congestion.RecvStats {
    m := r.metrics

    // Use helper getters for float64 rates
    mbpsBandwidth := m.GetRecvRateMbps()            // Encapsulates Float64frombits + unit conversion
    pktRetransRate := m.GetRecvRateRetransPercent() // Encapsulates Float64frombits

    return congestion.RecvStats{
        // ... other fields ...
        MbpsEstimatedRecvBandwidth: mbpsBandwidth,
        PktRetransRate:             pktRetransRate,
    }
}
```

```go
// File: congestion/live/send.go

// BEFORE:
func (s *sender) Stats() congestion.SendStats {
    s.lock.RLock()
    mbpsSentBW := s.rate.estimatedSentBW * 8 / 1024 / 1024
    s.lock.RUnlock()
    // ...
}

// AFTER:
func (s *sender) Stats() congestion.SendStats {
    m := s.metrics

    // Use helper getters
    mbpsSentBW := m.GetSendRateMbps()  // Encapsulates Float64frombits + unit conversion

    return congestion.SendStats{
        // ... other fields ...
        MbpsEstimatedSentBandwidth: mbpsSentBW,
    }
}
```

**Step 4: Implement CAS-based rate calculation**

Two versions depending on the processing model:

```go
// File: congestion/live/receive.go

// updateRecvRate calculates and stores rate metrics atomically
// EVENT LOOP version: called by rateTicker at the correct interval
// No early-return check needed - ticker fires at the right time
func (r *receiver) updateRecvRate(nowUs uint64) {
    m := r.metrics
    lastUs := m.RecvRateLastUs.Load()

    // Calculate rates based on actual elapsed time
    packets := m.RecvRatePackets.Swap(0)  // Atomic swap and reset
    bytes := m.RecvRateBytes.Swap(0)
    bytesRetrans := m.RecvRateBytesRetrans.Swap(0)

    elapsed := float64(nowUs - lastUs) / 1_000_000.0  // Seconds
    if elapsed > 0 {
        pps := float64(packets) / elapsed
        bps := float64(bytes) / elapsed

        // Store as uint64 bits (single atomic store, no CAS loop needed)
        m.RecvRatePacketsPerSec.Store(math.Float64bits(pps))
        m.RecvRateBytesPerSec.Store(math.Float64bits(bps))

        // Retransmission rate: bytesRetrans / bytes * 100
        var retransRate float64
        if bytes > 0 {
            retransRate = float64(bytesRetrans) / float64(bytes) * 100.0
        }
        m.RecvRatePktRetransRate.Store(math.Float64bits(retransRate))
    }

    m.RecvRateLastUs.Store(nowUs)
}

// updateRecvRateTick is for LEGACY Tick path
// Called every Tick (~10ms), but only calculates when period (1s) elapses
func (r *receiver) updateRecvRateTick(nowUs uint64) {
    m := r.metrics
    lastUs := m.RecvRateLastUs.Load()
    periodUs := m.RecvRatePeriodUs.Load()

    if nowUs-lastUs < periodUs {
        return // Not time yet - wait for full period
    }

    // Same calculation as updateRecvRate
    r.updateRecvRate(nowUs)
}
```

**Note on CAS vs Store:**

The original design used CAS loops, but simple `Store()` is sufficient here because:
1. Only ONE goroutine (event loop or Tick) writes to these fields
2. Multiple readers (Stats(), Prometheus handler) just need a consistent snapshot
3. `atomic.Store` provides the necessary memory ordering

#### 1.4 Impact on contrib/main.go Files

**Question**: Do `contrib/client/main.go`, `contrib/client-generator/main.go`, and `contrib/server/main.go` need updates?

**Answer**: **No changes required** - these files use `ThroughputGetter` which computes rates locally:

```go
// File: contrib/common/statistics.go (UNCHANGED)

// ThroughputGetter returns raw byte/packet counts (not rates)
type ThroughputGetter func() (bytes, pkts, gaps, naks, skips, retrans uint64)

// RunThroughputDisplayWithLabel computes rates locally
func RunThroughputDisplayWithLabel(...) {
    // ...
    mbps := float64(currentBytes-prevBytes) * 8 / (1000 * 1000 * diff.Seconds())
    // ...
}
```

```go
// File: contrib/client/main.go (UNCHANGED)

common.RunThroughputDisplayWithLabel(ctx, *common.StatsPeriod, instanceLabel,
    func() (uint64, uint64, uint64, uint64, uint64, uint64) {
        // These raw counters remain atomic.Uint64 - no API change
        return clientMetrics.ByteRecvDataSuccess.Load(),
               clientMetrics.PktRecvDataSuccess.Load(),
               gaps, naks, skips, retrans
    })
```

**Why no changes needed:**

| Pattern | Used By | Data Source | Change Required? |
|---------|---------|-------------|------------------|
| `ThroughputGetter` | client/main.go, client-generator/main.go | Raw byte/pkt counts (`.Load()`) | ❌ No |
| `PrintConnectionStatistics` | All main.go files | `conn.Stats()` → `Statistics.Instantaneous.MbpsRecvRate` | ❌ No (Stats() updated internally) |

**Optional enhancement** (not required for Phase 1):

If we wanted main.go files to display the pre-computed rates from `ConnectionMetrics`:

```go
// contrib/client/main.go - OPTIONAL enhancement
// Instead of computing rate locally, use pre-computed rate

// Option A: Use getter for Mbps rate
if connMetrics != nil {
    mbps := connMetrics.GetRecvRateMbps()
}

// Option B: Use bytes/sec rate and convert
if connMetrics != nil {
    bps := connMetrics.GetRecvRateBytesPerSec()
    mbps := bps * 8 / 1_000_000
}
```

This is optional because:
1. Local computation works fine (current behavior preserved)
2. Local computation is actually fresher (per-display-period vs per-rate-period)
3. Adding getter calls would require additional metric lookups per display tick

**Conclusion**: The main.go files will continue to work unchanged. The rate helper getters are primarily for internal use by `receiver.Stats()` and `sender.Stats()`, and for Prometheus export.

#### 1.5 Analysis.go Rate Validation

**File**: `contrib/integration_testing/analysis.go`

The integration tests use `analysis.go` to perform post-test validation. We need to add a new validation function that compares the Prometheus rate metrics to the computed rates from byte deltas.

**Step 5: Add rate metrics validation to analysis.go**

```go
// File: contrib/integration_testing/analysis.go

// RateMetricsValidationResult holds the result of rate metrics validation
type RateMetricsValidationResult struct {
    Passed bool

    // Expected rate (computed from byte deltas and duration)
    ExpectedRecvRateMbps float64
    ExpectedSendRateMbps float64

    // Reported rate (from Prometheus gauges - new atomic rate metrics)
    ReportedRecvRateMbps float64
    ReportedSendRateMbps float64

    // Variance (should be < 5% for healthy rate calculation)
    RecvRateVariance float64  // |expected - reported| / expected * 100
    SendRateVariance float64

    // Tolerance
    MaxVariancePercent float64  // Default: 5%

    // Messages
    Violations []string
    Warnings   []string
}

// VerifyRateMetrics validates that Prometheus rate metrics match computed rates
// This is critical for Phase 1 (Rate Atomics) - ensures new atomic rates are accurate
func VerifyRateMetrics(
    computedRecvMbps, computedSendMbps float64,
    prometheusRecvBps, prometheusSendBps float64,
    config *TestConfig,
) RateMetricsValidationResult {
    result := RateMetricsValidationResult{
        Passed:              false, // Fail-safe: default to failed
        ExpectedRecvRateMbps: computedRecvMbps,
        ExpectedSendRateMbps: computedSendMbps,
        ReportedRecvRateMbps: prometheusSendBps * 8 / 1_000_000, // bytes/sec to Mbps
        ReportedSendRateMbps: prometheusRecvBps * 8 / 1_000_000,
        MaxVariancePercent:   5.0, // 5% tolerance
    }

    // Calculate variance for receive rate
    if computedRecvMbps > 0 {
        result.RecvRateVariance = math.Abs(result.ReportedRecvRateMbps-computedRecvMbps) /
            computedRecvMbps * 100
    }

    // Calculate variance for send rate
    if computedSendMbps > 0 {
        result.SendRateVariance = math.Abs(result.ReportedSendRateMbps-computedSendMbps) /
            computedSendMbps * 100
    }

    // Check receive rate
    recvPassed := result.RecvRateVariance <= result.MaxVariancePercent
    if !recvPassed {
        result.Violations = append(result.Violations,
            fmt.Sprintf("Recv rate variance too high: expected %.2f Mbps, got %.2f Mbps (%.1f%% variance, max %.1f%%)",
                computedRecvMbps, result.ReportedRecvRateMbps,
                result.RecvRateVariance, result.MaxVariancePercent))
    }

    // Check send rate
    sendPassed := result.SendRateVariance <= result.MaxVariancePercent
    if !sendPassed {
        result.Violations = append(result.Violations,
            fmt.Sprintf("Send rate variance too high: expected %.2f Mbps, got %.2f Mbps (%.1f%% variance, max %.1f%%)",
                computedSendMbps, result.ReportedSendRateMbps,
                result.SendRateVariance, result.MaxVariancePercent))
    }

    // Also verify rate is close to configured test bitrate
    if config != nil && config.Bitrate > 0 {
        targetMbps := float64(config.Bitrate) / 1_000_000
        targetVariance := math.Abs(result.ReportedRecvRateMbps-targetMbps) / targetMbps * 100

        if targetVariance > 15 { // 15% from target is a warning
            result.Warnings = append(result.Warnings,
                fmt.Sprintf("Recv rate %.2f Mbps is %.1f%% off target %.2f Mbps",
                    result.ReportedRecvRateMbps, targetVariance, targetMbps))
        }
    }

    result.Passed = recvPassed && sendPassed
    return result
}

// PrintRateMetricsValidation prints the rate validation result
func PrintRateMetricsValidation(result RateMetricsValidationResult) {
    fmt.Println("\nRate Metrics Validation (Phase 1 - Atomic Rates):")
    if result.Passed {
        fmt.Println("  ✓ PASSED")
        fmt.Printf("    Recv: computed=%.2f Mbps, reported=%.2f Mbps (%.1f%% variance)\n",
            result.ExpectedRecvRateMbps, result.ReportedRecvRateMbps, result.RecvRateVariance)
        fmt.Printf("    Send: computed=%.2f Mbps, reported=%.2f Mbps (%.1f%% variance)\n",
            result.ExpectedSendRateMbps, result.ReportedSendRateMbps, result.SendRateVariance)
    } else {
        fmt.Println("  ✗ FAILED")
        for _, v := range result.Violations {
            fmt.Printf("    ✗ %s\n", v)
        }
    }
    for _, w := range result.Warnings {
        fmt.Printf("    ⚠ %s\n", w)
    }
}
```

**Step 6: Integrate into test analysis**

```go
// In analyzeTest() function, add rate validation:

func analyzeTest(ts *TestMetricsTimeSeries) AnalysisResult {
    // ... existing analysis ...

    // Get Prometheus rate metrics from final snapshot
    lastSnapshot := ts.Client.Snapshots[len(ts.Client.Snapshots)-1]
    prometheusRecvBps := getMetricValue(lastSnapshot, "gosrt_recv_rate_bytes_per_sec")
    prometheusSendBps := getMetricValue(lastSnapshot, "gosrt_send_rate_bytes_per_sec")

    // Validate rate metrics match computed rates
    rateResult := VerifyRateMetrics(
        clientMetrics.AvgRecvRateMbps,  // Computed from byte deltas
        serverMetrics.AvgSendRateMbps,  // Computed from byte deltas
        prometheusRecvBps,               // From new Prometheus gauge
        prometheusSendBps,               // From new Prometheus gauge
        ts.TestConfig,
    )

    PrintRateMetricsValidation(rateResult)

    // Include in overall result
    result.RateMetricsValidation = rateResult
    if !rateResult.Passed {
        result.Passed = false
        result.Violations = append(result.Violations,
            "Rate metrics validation failed - atomic rate calculation may be incorrect")
    }

    // ... rest of analysis ...
}
```

#### 1.6 Validation Checkpoint

**Reference**: `integration_testing_design.md`, `integration_testing_matrix_design.md`

```bash
# 1. Run metrics audit tool to verify no duplication
go run tools/metrics-audit/main.go
# Expected: All rate metrics defined, exported, and used without duplication

# 2. Unit tests with race detector
go test -race -v ./...

# 3. Run Tier 1 integration tests (Core Validation)
# See integration_testing_matrix_design.md Section 9
# analysis.go will now validate rate metrics as part of the test
cd contrib/integration-tests
./run-tests.sh --tier=1 --config=Base

# Expected output includes:
# Rate Metrics Validation (Phase 1 - Atomic Rates):
#   ✓ PASSED
#     Recv: computed=20.00 Mbps, reported=19.95 Mbps (0.3% variance)
#     Send: computed=20.00 Mbps, reported=20.02 Mbps (0.1% variance)

# 4. Verify Prometheus metrics are exported
curl http://localhost:8080/metrics | grep gosrt_recv_rate
# Expected: gosrt_recv_rate_packets_per_sec, gosrt_recv_rate_bytes_per_sec, etc.

# 5. Parallel comparison: verify metrics identical before/after
# See parallel_comparison_test_design.md
./run-tests.sh --mode=parallel --test=Net-Clean-20M-5s-R0-Base

# 6. Run at multiple bitrates to verify rate accuracy scales
./run-tests.sh --mode=network --test=Net-Clean-5M-5s-R0-Base   # 5 Mbps
./run-tests.sh --mode=network --test=Net-Clean-20M-5s-R0-Base  # 20 Mbps
./run-tests.sh --mode=network --test=Net-Clean-50M-5s-R0-Base  # 50 Mbps
```

**What we're validating:**
- `metrics-audit` tool reports no missing/duplicate metrics
- Race detector passes (no data races in atomics)
- Prometheus endpoint exposes all new rate metrics
- **NEW**: `analysis.go` validates rate metrics match computed rates (< 5% variance)
- **NEW**: Rate metrics accuracy verified at multiple bitrates (5M, 20M, 50M)
- Tier 1 tests pass with identical metrics to baseline
- Rate calculations produce same values as locked version

#### 1.7 Acceptance Criteria

- [ ] `go run tools/metrics-audit/main.go` shows all rate metrics defined and exported
- [ ] All tests pass with `-race` flag
- [ ] Prometheus endpoint shows `gosrt_recv_rate_*` and `gosrt_send_rate_*` metrics
- [ ] **NEW**: `analysis.go` rate validation passes (< 5% variance from computed rates)
- [ ] **NEW**: Rate metrics accurate at 5M, 20M, and 50M bitrates
- [ ] Rate metrics values match legacy implementation (< 0.1% difference)
- [ ] No performance regression in benchmarks
- [ ] Lock contention reduced (verify with `pprof` mutex profile)

---

### Phase 2: Zero-Copy Buffer Lifetime Extension (7-8 hours) [NO FLAG - Always On]

**Status**: ✅ **COMPLETE** (December 2024)

**Goal**: Eliminate buffer copying in packet receive path. Benefits ALL paths.

**Reference**: Section 6 (Component 2: Buffer Lifetime Management)

**Implementation Tracking**: [`lockless_phase2_implementation.md`](./lockless_phase2_implementation.md)

#### 🎉 Integration Test Results

| Test | Status | Notes |
|------|--------|-------|
| `Isolation-5M-Control` | ✅ PASS | 100% recovery, 0 gaps |
| `Parallel-Starlink-5Mbps` | ✅ PASS | HighPerf: 0 gaps vs Baseline: 329 gaps |

**Key Accomplishments:**
- ✅ Shared global `recvBufferPool` in `buffers.go` - maximum memory reuse
- ✅ `UnmarshalZeroCopy()` - zero-copy packet deserialization
- ✅ `DecommissionWithBuffer()` - proper buffer lifecycle
- ✅ Updated all receive paths (io_uring, standard) to use zero-copy
- ✅ CPU profile shows `packet.Header` eliminated from hot path (17% → 0%)

---

#### Design Decision: Always-On (No Feature Flag)

Per Section 11.4.3 recommendation, zero-copy is implemented as **always-on** because:

1. **Universal benefit** - ALL implementation paths benefit equally:
   - io_uring AND standard recv() paths
   - btree AND linked list packet stores
   - Tick() AND event loop processing models

2. **No behavioral change** - Same packet data, same delivery timing, just less copying

3. **API contract is simple** - Applications must not hold packet data references after delivery callback returns (this is already a reasonable expectation)

4. **Simpler implementation** - No conditional branches in hot paths

**Contrast with Ring Buffer/Event Loop**: Those features ARE gated by flags because they fundamentally change execution semantics and need A/B comparison testing.

---

#### 2.1 Files to Modify - Complete Matrix

| File | Changes | Priority | Dependencies |
|------|---------|----------|--------------|
| `packet/packet.go` | Add `recvBuffer`, `n`, new methods, `UnmarshalZeroCopy`, `DecommissionWithBuffer` | 1 | None |
| `packet/packet_test.go` | Add tests for new packet methods | 1.1 | packet.go |
| `congestion/live/receive.go` | Add `releasePacketFully()`, `bufferPool` field, update `deliverPackets()` | 2 | packet.go |
| `congestion/live/receive_test.go` | Add tests for `releasePacketFully()` | 2.1 | receive.go |
| `listen_linux.go` | Update io_uring completion handler to use `UnmarshalZeroCopy` | 3 | All above |
| `dial_linux.go` | Update io_uring path for dial side | 3 | All above |
| `listen.go` | Update standard receive path to use `UnmarshalZeroCopy` | 4 | All above |
| `dial.go` | Update standard receive path for dial side | 4 | All above |
| `connection.go` | Pass `bufferPool` to receiver | 5 | All above |

**Note**: No changes needed to `config.go` or `contrib/common/flags.go` - this is always-on!

---

#### 2.2 Detailed Implementation Steps

##### **Step 1: Update packet structure (`packet/packet.go`)**

**File**: `packet/packet.go`

**Changes to `pkt` struct** (around line 240-280):

```go
type pkt struct {
    header  PacketHeader
    payload *payloadBuffer  // LEGACY: copied payload data

    // Zero-copy fields
    recvBuffer *[]byte  // Original buffer from recvBufferPool (for pool return)
    n          int      // Bytes received (from ReadFromUDP/io_uring CQE.Res)
}
```

**New methods to add** (after existing methods, ~line 500+):

```go
// ========== Zero-Copy Support (Phase 2: Lockless Design) ==========

// UnmarshalZeroCopy parses a packet using zero-copy - the buffer reference is
// stored for later pool return. No data is copied.
//
// IMPORTANT: This sets recvBuffer BEFORE validation, ensuring DecommissionWithBuffer()
// can always return the buffer even if parsing fails.
func (p *pkt) UnmarshalZeroCopy(buf *[]byte, n int, addr net.Addr) error {
    // Store buffer reference FIRST (before any validation that might fail)
    // This ensures DecommissionWithBuffer() can always return the buffer
    p.recvBuffer = buf
    p.n = n
    p.header.Addr = addr

    // Validate minimum size
    if n < HeaderSize {
        return fmt.Errorf("packet too short (%d bytes, need %d)", n, HeaderSize)
    }

    // Parse header directly from buffer (no intermediate slice needed)
    // We access (*buf) directly - no need for data := (*buf)[:n]
    p.header.IsControlPacket = ((*buf)[0] & 0x80) != 0

    if p.header.IsControlPacket {
        p.header.ControlType = CtrlType(binary.BigEndian.Uint16((*buf)[0:]) & ^uint16(1<<15))
        p.header.SubType = CtrlSubType(binary.BigEndian.Uint16((*buf)[2:]))
        p.header.TypeSpecific = binary.BigEndian.Uint32((*buf)[4:])
    } else {
        p.header.PacketSequenceNumber = circular.New(binary.BigEndian.Uint32((*buf)[0:]), MAX_SEQUENCENUMBER)
        p.header.PacketPositionFlag = PacketPosition(((*buf)[4] & 0b11000000) >> 6)
        p.header.OrderFlag = ((*buf)[4] & 0b00100000) != 0
        p.header.KeyBaseEncryptionFlag = PacketEncryption(((*buf)[4] & 0b00011000) >> 3)
        p.header.RetransmittedPacketFlag = ((*buf)[4] & 0b00000100) != 0
        p.header.MessageNumber = binary.BigEndian.Uint32((*buf)[4:]) & ^uint32(0b11111100<<24)
    }

    p.header.Timestamp = binary.BigEndian.Uint32((*buf)[8:])
    p.header.DestinationSocketId = binary.BigEndian.Uint32((*buf)[12:])

    // NOTE: No payload copy! Payload is computed on-demand via GetPayload()
    // GetPayload() returns (*recvBuffer)[HeaderSize:n]
    return nil
}

// DecommissionWithBuffer returns the buffer to the provided pool, then
// returns the packet struct to the packet pool.
// Safe to call even if recvBuffer is nil (handles both legacy and zero-copy).
func (p *pkt) DecommissionWithBuffer(bufferPool *sync.Pool) {
    if p.recvBuffer != nil && bufferPool != nil {
        // Zero buffer for immediate reuse
        *p.recvBuffer = (*p.recvBuffer)[:0]
        bufferPool.Put(p.recvBuffer)
        p.recvBuffer = nil
        p.n = 0
    }
    p.Decommission()
}

// GetRecvBuffer returns the original pool buffer reference (for zero-copy path).
func (p *pkt) GetRecvBuffer() *[]byte {
    return p.recvBuffer
}

// HasRecvBuffer returns true if packet has a tracked pool buffer (zero-copy path).
func (p *pkt) HasRecvBuffer() bool {
    return p.recvBuffer != nil
}

// ClearRecvBuffer clears the buffer reference after pool return.
func (p *pkt) ClearRecvBuffer() {
    p.recvBuffer = nil
    p.n = 0
}

// GetPayload returns payload data, handling BOTH zero-copy and legacy paths.
// For zero-copy: computes slice from recvBuffer: (*recvBuffer)[HeaderSize:n]
// For legacy: returns stored payload directly
func (p *pkt) GetPayload() []byte {
    if p.recvBuffer != nil {
        if p.n <= HeaderSize {
            return nil
        }
        return (*p.recvBuffer)[HeaderSize:p.n]
    }
    // Legacy path
    if p.payload != nil {
        return p.payload.Bytes()
    }
    return nil
}

// GetPayloadLen returns payload length (n - HeaderSize for zero-copy).
func (p *pkt) GetPayloadLen() int {
    if p.recvBuffer != nil {
        if p.n <= HeaderSize {
            return 0
        }
        return p.n - HeaderSize
    }
    // Legacy path
    if p.payload != nil {
        return p.payload.Len()
    }
    return 0
}
```

**Update existing `Payload()` method** to handle both paths:

```go
func (p *pkt) Payload() []byte {
    // Zero-copy path: compute from recvBuffer
    if p.recvBuffer != nil {
        return p.GetPayloadZeroCopy()
    }
    // Legacy path: return stored payload
    if p.payload == nil {
        return nil
    }
    return p.payload.Bytes()
}
```

**Update `Decommission()` to clear zero-copy fields**:

```go
func (p *pkt) Decommission() {
    // ... existing cleanup ...

    // Clear zero-copy fields
    p.recvBuffer = nil
    p.n = 0

    // Return to pool
    packetPool.Put(p)
}
```

**Comment out legacy `NewPacketFromData()` for historical reference**:

Instead of deleting the legacy copying function, comment it out with an explanation:

```go
// DEPRECATED: NewPacketFromData - replaced by UnmarshalZeroCopy (Phase 2: Lockless Design)
// This function copied packet data into a new buffer. The new UnmarshalZeroCopy
// references the pooled buffer directly, eliminating the copy and extending
// buffer lifetime until packet delivery.
//
// Kept for historical reference - can be removed after migration is validated.
//
// func NewPacketFromData(addr net.Addr, data []byte) (Packet, error) {
//     p := NewPacket(addr)
//     if err := p.Unmarshal(data); err != nil {
//         p.Decommission()
//         return nil, fmt.Errorf("invalid data: %w", err)
//     }
//     return p, nil
// }
```

This preserves the legacy code for reference while clearly marking that `UnmarshalZeroCopy` is the replacement.

---

##### **Step 1.1: Add packet tests (`packet/packet_test.go`)**

**File**: `packet/packet_test.go`

This is a **critical testing step** because `UnmarshalZeroCopy` is on the hot path for every packet received. We need:
1. **Functional tests** - Verify correct behavior
2. **Round-trip tests** - Ensure Marshal/Unmarshal compatibility
3. **Cross-compatibility tests** - Verify `UnmarshalZeroCopy` produces same results as `UnmarshalCopy`
4. **Benchmarks** - Measure performance improvement and prevent regressions

**Existing tests to reference**: The existing `packet/packet_test.go` already has tests like `TestPacketRoundTrip` that marshal and unmarshal packets. We'll follow this pattern.

---

**New functional tests to add**:

```go
func TestUnmarshalZeroCopy(t *testing.T) {
    t.Run("successful data packet", func(t *testing.T) {
        // Create a valid data packet buffer
        buf := createTestDataPacket(t, 1234, 100) // seq=1234, payload=100 bytes
        bufPtr := &buf
        n := len(buf)

        pkt := NewPacket(nil)
        err := pkt.UnmarshalZeroCopy(bufPtr, n, testAddr)
        require.NoError(t, err)

        // Verify header parsed correctly
        require.False(t, pkt.Header().IsControlPacket)
        require.Equal(t, uint32(1234), pkt.Header().PacketSequenceNumber.Val())

        // Verify buffer tracking
        require.True(t, pkt.HasRecvBuffer())
        require.Equal(t, bufPtr, pkt.GetRecvBuffer())

        // Verify payload access
        payload := pkt.GetPayload()
        require.Len(t, payload, 100)
    })

    t.Run("successful control packet", func(t *testing.T) {
        buf := createTestControlPacket(t, CtrlTypeACK)
        bufPtr := &buf

        pkt := NewPacket(nil)
        err := pkt.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
        require.NoError(t, err)
        require.True(t, pkt.Header().IsControlPacket)
        require.Equal(t, CtrlTypeACK, pkt.Header().ControlType)
    })

    t.Run("packet too short returns error", func(t *testing.T) {
        buf := make([]byte, HeaderSize-1) // One byte too short
        bufPtr := &buf

        pkt := NewPacket(nil)
        err := pkt.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
        require.Error(t, err)
        require.Contains(t, err.Error(), "too short")

        // CRITICAL: Buffer must still be tracked even on error!
        // This allows DecommissionWithBuffer to clean up properly
        require.True(t, pkt.HasRecvBuffer())
        require.Equal(t, bufPtr, pkt.GetRecvBuffer())
    })

    t.Run("n field stored correctly", func(t *testing.T) {
        buf := make([]byte, 1500)
        createValidHeader(buf, false) // data packet
        bufPtr := &buf

        pkt := NewPacket(nil)
        n := 200 // Only use first 200 bytes
        err := pkt.UnmarshalZeroCopy(bufPtr, n, testAddr)
        require.NoError(t, err)

        // GetPayload should only return up to n, not full buffer
        payload := pkt.GetPayload()
        require.Len(t, payload, n-HeaderSize) // 200-16 = 184
    })
}

func TestDecommissionWithBuffer(t *testing.T) {
    t.Run("returns buffer to pool", func(t *testing.T) {
        pool := &sync.Pool{New: func() interface{} { b := make([]byte, 1500); return &b }}

        bufPtr := pool.Get().(*[]byte)
        createValidHeader(*bufPtr, false)

        pkt := NewPacket(nil)
        pkt.UnmarshalZeroCopy(bufPtr, 200, testAddr)
        require.True(t, pkt.HasRecvBuffer())

        pkt.DecommissionWithBuffer(pool)

        // Buffer should be cleared
        require.False(t, pkt.HasRecvBuffer())
        require.Nil(t, pkt.GetRecvBuffer())
    })

    t.Run("handles nil buffer gracefully", func(t *testing.T) {
        pool := &sync.Pool{New: func() interface{} { b := make([]byte, 1500); return &b }}

        pkt := NewPacket(nil) // No buffer set
        require.False(t, pkt.HasRecvBuffer())

        // Should not panic
        pkt.DecommissionWithBuffer(pool)
    })

    t.Run("handles nil pool gracefully", func(t *testing.T) {
        buf := make([]byte, 200)
        createValidHeader(buf, false)
        bufPtr := &buf

        pkt := NewPacket(nil)
        pkt.UnmarshalZeroCopy(bufPtr, 200, testAddr)

        // Should not panic with nil pool
        pkt.DecommissionWithBuffer(nil)
    })
}

func TestGetPayload(t *testing.T) {
    t.Run("zero-copy path computes slice correctly", func(t *testing.T) {
        payloadData := []byte("test payload data here")
        buf := make([]byte, HeaderSize+len(payloadData))
        createValidHeader(buf, false)
        copy(buf[HeaderSize:], payloadData)
        bufPtr := &buf

        pkt := NewPacket(nil)
        pkt.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)

        payload := pkt.GetPayload()
        require.Equal(t, payloadData, payload)
    })

    t.Run("returns nil for header-only packet", func(t *testing.T) {
        buf := make([]byte, HeaderSize) // Exactly header, no payload
        createValidHeader(buf, false)
        bufPtr := &buf

        pkt := NewPacket(nil)
        pkt.UnmarshalZeroCopy(bufPtr, HeaderSize, testAddr)

        payload := pkt.GetPayload()
        require.Empty(t, payload)
    })

    t.Run("returns nil when recvBuffer is nil", func(t *testing.T) {
        pkt := NewPacket(nil)
        require.Nil(t, pkt.GetPayload())
    })

    t.Run("legacy path still works", func(t *testing.T) {
        // Use legacy Unmarshal (copying) path
        payloadData := []byte("legacy payload")
        buf := make([]byte, HeaderSize+len(payloadData))
        createValidHeader(buf, false)
        copy(buf[HeaderSize:], payloadData)

        pkt := NewPacket(testAddr)
        pkt.Unmarshal(buf) // Legacy copying unmarshal

        payload := pkt.GetPayload()
        require.Equal(t, payloadData, payload)
    })
}

func TestGetPayloadLen(t *testing.T) {
    t.Run("returns correct length for zero-copy", func(t *testing.T) {
        buf := make([]byte, 500)
        createValidHeader(buf, false)
        bufPtr := &buf
        n := 200

        pkt := NewPacket(nil)
        pkt.UnmarshalZeroCopy(bufPtr, n, testAddr)

        require.Equal(t, n-HeaderSize, pkt.GetPayloadLen())
    })

    t.Run("returns zero for header-only", func(t *testing.T) {
        buf := make([]byte, HeaderSize)
        createValidHeader(buf, false)
        bufPtr := &buf

        pkt := NewPacket(nil)
        pkt.UnmarshalZeroCopy(bufPtr, HeaderSize, testAddr)

        require.Equal(t, 0, pkt.GetPayloadLen())
    })
}
```

---

**Round-trip and cross-compatibility tests**:

```go
// TestUnmarshalZeroCopyRoundTrip verifies that a packet can be marshaled,
// then unmarshaled with UnmarshalZeroCopy, and produce identical header values.
func TestUnmarshalZeroCopyRoundTrip(t *testing.T) {
    testCases := []struct {
        name        string
        isControl   bool
        ctrlType    CtrlType
        seq         uint32
        timestamp   uint32
        socketID    uint32
        payloadSize int
    }{
        {"data packet small payload", false, 0, 12345, 1000000, 0xABCD1234, 100},
        {"data packet large payload", false, 0, 99999, 5000000, 0x12345678, 1400},
        {"data packet min payload", false, 0, 1, 0, 0x1, 1},
        {"ACK control packet", true, CtrlTypeACK, 0, 2000000, 0xFACE0000, 0},
        {"NAK control packet", true, CtrlTypeNAK, 0, 3000000, 0xBEEF0000, 0},
        {"keepalive packet", true, CtrlTypeKeepalive, 0, 4000000, 0xDEAD0000, 0},
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Create original packet
            original := createTestPacketWithValues(t, tc.isControl, tc.ctrlType,
                tc.seq, tc.timestamp, tc.socketID, tc.payloadSize)

            // Marshal to bytes
            marshaled := make([]byte, HeaderSize+tc.payloadSize)
            original.Marshal(marshaled)

            // Unmarshal with zero-copy
            bufPtr := &marshaled
            restored := NewPacket(nil)
            err := restored.UnmarshalZeroCopy(bufPtr, len(marshaled), testAddr)
            require.NoError(t, err)

            // Verify all header fields match
            require.Equal(t, original.Header().IsControlPacket, restored.Header().IsControlPacket)
            require.Equal(t, original.Header().Timestamp, restored.Header().Timestamp)
            require.Equal(t, original.Header().DestinationSocketId, restored.Header().DestinationSocketId)

            if tc.isControl {
                require.Equal(t, original.Header().ControlType, restored.Header().ControlType)
            } else {
                require.Equal(t, original.Header().PacketSequenceNumber.Val(),
                              restored.Header().PacketSequenceNumber.Val())
            }

            // Verify payload matches
            if tc.payloadSize > 0 {
                require.Equal(t, original.GetPayload(), restored.GetPayload())
            }
        })
    }
}

// TestUnmarshalZeroCopyVsCopyEquivalence verifies that UnmarshalZeroCopy
// produces the same results as the legacy UnmarshalCopy for all packet types.
func TestUnmarshalZeroCopyVsCopyEquivalence(t *testing.T) {
    testCases := []struct {
        name        string
        isControl   bool
        payloadSize int
    }{
        {"data packet 100 bytes", false, 100},
        {"data packet 1400 bytes", false, 1400},
        {"control ACK packet", true, 0},
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Create a test packet buffer
            buf := createTestPacketBuffer(t, tc.isControl, tc.payloadSize)

            // Unmarshal with legacy copy method
            pktCopy := NewPacket(testAddr)
            errCopy := pktCopy.Unmarshal(buf)
            require.NoError(t, errCopy)

            // Unmarshal with zero-copy method
            bufCopy := make([]byte, len(buf))
            copy(bufCopy, buf) // Make a copy to avoid interference
            bufPtr := &bufCopy
            pktZero := NewPacket(nil)
            errZero := pktZero.UnmarshalZeroCopy(bufPtr, len(bufCopy), testAddr)
            require.NoError(t, errZero)

            // Compare all header fields
            require.Equal(t, pktCopy.Header().IsControlPacket, pktZero.Header().IsControlPacket,
                "IsControlPacket mismatch")
            require.Equal(t, pktCopy.Header().Timestamp, pktZero.Header().Timestamp,
                "Timestamp mismatch")
            require.Equal(t, pktCopy.Header().DestinationSocketId, pktZero.Header().DestinationSocketId,
                "DestinationSocketId mismatch")

            if tc.isControl {
                require.Equal(t, pktCopy.Header().ControlType, pktZero.Header().ControlType,
                    "ControlType mismatch")
                require.Equal(t, pktCopy.Header().SubType, pktZero.Header().SubType,
                    "SubType mismatch")
                require.Equal(t, pktCopy.Header().TypeSpecific, pktZero.Header().TypeSpecific,
                    "TypeSpecific mismatch")
            } else {
                require.Equal(t, pktCopy.Header().PacketSequenceNumber.Val(),
                              pktZero.Header().PacketSequenceNumber.Val(),
                    "PacketSequenceNumber mismatch")
                require.Equal(t, pktCopy.Header().PacketPositionFlag, pktZero.Header().PacketPositionFlag,
                    "PacketPositionFlag mismatch")
                require.Equal(t, pktCopy.Header().OrderFlag, pktZero.Header().OrderFlag,
                    "OrderFlag mismatch")
                require.Equal(t, pktCopy.Header().KeyBaseEncryptionFlag, pktZero.Header().KeyBaseEncryptionFlag,
                    "KeyBaseEncryptionFlag mismatch")
                require.Equal(t, pktCopy.Header().RetransmittedPacketFlag, pktZero.Header().RetransmittedPacketFlag,
                    "RetransmittedPacketFlag mismatch")
                require.Equal(t, pktCopy.Header().MessageNumber, pktZero.Header().MessageNumber,
                    "MessageNumber mismatch")
            }

            // Compare payloads (content should be identical)
            require.Equal(t, pktCopy.GetPayload(), pktZero.GetPayload(),
                "Payload content mismatch")
            require.Equal(t, pktCopy.GetPayloadLen(), pktZero.GetPayloadLen(),
                "PayloadLen mismatch")
        })
    }
}
```

---

**Benchmarks (CRITICAL for performance validation)**:

```go
const (
    // MPEG-TS packet size (ISO/IEC 13818-1 standard)
    MpegTsPacketSize = 188

    // Number of MPEG-TS packets typically packed into one SRT payload.
    // This is a common configuration for video streaming.
    // Total payload: 188 * 7 = 1316 bytes
    MpegTsPacketsPerPayload = 7

    // Realistic payload size for video streaming benchmarks
    // 1316 bytes payload + 16 bytes header = 1332 bytes total packet
    RealisticPayloadSize = MpegTsPacketSize * MpegTsPacketsPerPayload // 1316 bytes
)

// BenchmarkUnmarshalZeroCopy measures zero-copy unmarshal performance.
// Uses realistic 7x MPEG-TS payload (1316 bytes) to demonstrate real-world benefit.
// Expected: Significantly faster than BenchmarkUnmarshalCopy due to no allocations.
func BenchmarkUnmarshalZeroCopy(b *testing.B) {
    // Realistic payload: 7 MPEG-TS packets = 1316 bytes
    buf := createTestPacketBuffer(nil, false, RealisticPayloadSize)
    bufPtr := &buf

    pool := &sync.Pool{New: func() interface{} { return NewPacket(nil) }}

    b.ResetTimer()
    b.ReportAllocs()

    for i := 0; i < b.N; i++ {
        pkt := pool.Get().(Packet)
        _ = pkt.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
        pkt.ClearRecvBuffer() // Don't return buffer in benchmark
        pkt.Decommission()
    }
}

// BenchmarkUnmarshalCopy measures legacy copying unmarshal for comparison.
// Uses same realistic payload size for fair comparison.
func BenchmarkUnmarshalCopy(b *testing.B) {
    buf := createTestPacketBuffer(nil, false, RealisticPayloadSize)

    pool := &sync.Pool{New: func() interface{} { return NewPacket(nil) }}

    b.ResetTimer()
    b.ReportAllocs()

    for i := 0; i < b.N; i++ {
        pkt := pool.Get().(Packet)
        _ = pkt.Unmarshal(buf) // Legacy copy path
        pkt.Decommission()
    }
}

// BenchmarkUnmarshalComparison runs both methods with various payload sizes
// to demonstrate performance difference across packet sizes.
func BenchmarkUnmarshalComparison(b *testing.B) {
    payloadSizes := []struct {
        name string
        size int
    }{
        {"1_MPEGTS", MpegTsPacketSize * 1},           // 188 bytes (minimum)
        {"4_MPEGTS", MpegTsPacketSize * 4},           // 752 bytes
        {"7_MPEGTS_typical", MpegTsPacketSize * 7},   // 1316 bytes (typical)
        {"max_payload", 1400},                         // Near MTU limit
    }

    for _, tc := range payloadSizes {
        buf := createTestPacketBuffer(nil, false, tc.size)
        bufCopy := make([]byte, len(buf))
        copy(bufCopy, buf)
        bufPtr := &buf

        b.Run(fmt.Sprintf("ZeroCopy/%s_%d_bytes", tc.name, tc.size+HeaderSize), func(b *testing.B) {
            pool := &sync.Pool{New: func() interface{} { return NewPacket(nil) }}
            b.ResetTimer()
            b.ReportAllocs()

            for i := 0; i < b.N; i++ {
                pkt := pool.Get().(Packet)
                _ = pkt.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
                pkt.ClearRecvBuffer()
                pkt.Decommission()
            }
        })

        b.Run(fmt.Sprintf("Copy/%s_%d_bytes", tc.name, tc.size+HeaderSize), func(b *testing.B) {
            pool := &sync.Pool{New: func() interface{} { return NewPacket(nil) }}
            b.ResetTimer()
            b.ReportAllocs()

            for i := 0; i < b.N; i++ {
                pkt := pool.Get().(Packet)
                _ = pkt.Unmarshal(bufCopy)
                pkt.Decommission()
            }
        })
    }
}

// BenchmarkGetPayload measures payload access overhead.
// Uses realistic 7x MPEG-TS payload size.
func BenchmarkGetPayload(b *testing.B) {
    buf := createTestPacketBuffer(nil, false, RealisticPayloadSize)
    bufPtr := &buf

    pkt := NewPacket(nil)
    _ = pkt.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)

    b.ResetTimer()
    b.ReportAllocs()

    for i := 0; i < b.N; i++ {
        payload := pkt.GetPayload()
        _ = payload // Prevent optimization
    }
}
```

---

**Expected benchmark results** (theory to validate):

| Benchmark | Payload | Zero-Copy | Legacy Copy | Improvement |
|-----------|---------|-----------|-------------|-------------|
| 1 MPEG-TS | 188 bytes | ~50 ns | ~120 ns | ~2.4x faster |
| 7 MPEG-TS (typical) | 1316 bytes | ~50 ns | ~350 ns | ~7x faster |
| Near MTU | 1400 bytes | ~50 ns | ~400 ns | ~8x faster |
| Allocations/op | any | 0 | 1 | ∞ improvement |
| Bytes/op | any | 0 | ~payload size | ∞ improvement |

**Why 7 MPEG-TS packets is the realistic benchmark**:
- MPEG-TS is the standard transport stream format for video (DVB, ATSC, IPTV)
- Each MPEG-TS packet is exactly 188 bytes (ISO/IEC 13818-1)
- 7 packets × 188 bytes = 1316 bytes fits well within typical MTU
- This is a common real-world configuration for video streaming over SRT

The zero-copy approach eliminates the `copy(payload, data[HeaderSize:])` operation and the allocation for the payload buffer, which provides significant speedup especially for larger payloads like 7× MPEG-TS.

**Running benchmarks**:
```bash
# Quick comparison
go test -bench=BenchmarkUnmarshal -benchmem ./packet/

# Detailed comparison with count for statistical significance
go test -bench=BenchmarkUnmarshalComparison -benchmem -count=5 ./packet/ | tee benchmark_results.txt

# Compare before/after using benchstat (if available)
# Before: go test -bench=. -benchmem -count=10 ./packet/ > old.txt
# After:  go test -bench=. -benchmem -count=10 ./packet/ > new.txt
# benchstat old.txt new.txt
```

---

##### **Step 2: Update receiver (`congestion/live/receive.go`)**

**File**: `congestion/live/receive.go`

**Add to `receiver` struct** (~line 55):

```go
type receiver struct {
    // ... existing fields ...

    // Zero-copy buffer management (Phase 2: Lockless Design)
    bufferPool *sync.Pool  // Reference to recvBufferPool for buffer return
}
```

**Update `NewReceiver` signature and initialization** (~line 100+):

```go
// Add bufferPool parameter to NewReceiver
func NewReceiver(
    config ReceiverConfig,
    bufferPool *sync.Pool,  // NEW: for zero-copy buffer return
    // ... other params ...
) *receiver {
    r := &receiver{
        // ... existing initialization ...
        bufferPool: bufferPool,
    }
    return r
}
```

**Add `releasePacketFully` method** (after `NewReceiver`):

```go
// releasePacketFully releases both the buffer and the packet to their pools.
// This is THE method to use for packet cleanup in all delivery paths.
// Safe to call even if recvBuffer is nil (handles legacy packets gracefully).
func (r *receiver) releasePacketFully(p packet.Packet) {
    // Return buffer to pool if present (zero-copy path)
    if buf := p.GetRecvBuffer(); buf != nil {
        *buf = (*buf)[:0]  // Zero for immediate reuse
        r.bufferPool.Put(buf)
        p.ClearRecvBuffer()
    }
    p.Decommission()
}
```

**Update `deliverPackets()` to use `releasePacketFully`** (~line in delivery code):

Find existing `p.Decommission()` calls in delivery path and replace with:
```go
r.releasePacketFully(p)
```

---

##### **Step 2.1: Add receiver tests (`congestion/live/receive_test.go`)**

**File**: `congestion/live/receive_test.go`

**New tests to add**:

```go
func TestReleasePacketFully_WithBuffer(t *testing.T) {
    // Create receiver with bufferPool
    // Create packet with recvBuffer set (zero-copy path)
    // Call releasePacketFully
    // Verify buffer returned to pool
    // Verify packet decommissioned
}

func TestReleasePacketFully_NilBuffer(t *testing.T) {
    // Create receiver with bufferPool
    // Create packet with NO recvBuffer (edge case)
    // Call releasePacketFully
    // Verify packet decommissioned
    // Verify no panic with nil buffer
}

func TestNewReceiverWithBufferPool(t *testing.T) {
    // Verify receiver correctly stores bufferPool reference
    // Verify bufferPool is accessible for releasePacketFully
}
```

---

##### **Step 3: Update io_uring receive path (`listen_linux.go`)**

**File**: `listen_linux.go`

**Update completion handler** (~line 425-446):

Find the `NewPacketFromData` call and replace with zero-copy always-on:

```go
// CURRENT code (copies data):
// p, err := packet.NewPacketFromData(addr, bufferSlice)
// ln.recvBufferPool.Put(bufferPtr)  // Returns buffer immediately

// NEW code (zero-copy always-on):
p := packet.NewPacket(addr)

// Zero-copy: parse header in place, buffer stays with packet
err := p.UnmarshalZeroCopy(bufferPtr, bytesReceived, addr)
if err != nil {
    ln.log("listen:recv:parse:error", func() string {
        return fmt.Sprintf("failed to parse packet: %v", err)
    })
    p.DecommissionWithBuffer(&ln.recvBufferPool)  // Returns buffer + decommissions packet
    ring.CQESeen(cqe)
    return
}

// Buffer stays with packet until delivery (no Put() here!)
ring.CQESeen(cqe)
// Continue with packet routing...
```

**Also update `dial_linux.go`** with similar changes for dialer-side io_uring.

---

##### **Step 4: Update standard receive path (`listen.go`)**

**File**: `listen.go`

**Update receive loop** (~line 302-327):

```go
// CURRENT code (copies data):
// p, err := packet.NewPacketFromData(addr, buffer[:n])

// NEW code (zero-copy always-on):
// Get buffer from pool (already done)
buf := ln.recvBufferPool.Get().(*[]byte)

n, addr, err := ln.conn.ReadFromUDP(*buf)
if err != nil {
    returnBufferToPool(&ln.recvBufferPool, buf)  // Helper function
    continue
}

p := packet.NewPacket(addr)

// Zero-copy: parse header in place, buffer stays with packet
err = p.UnmarshalZeroCopy(buf, n, addr)
if err != nil {
    p.DecommissionWithBuffer(&ln.recvBufferPool)
    continue
}

// Buffer stays with packet until delivery (no Put() here!)
// Route packet...
```

---

##### **Step 5: Update connection to pass bufferPool to receiver**

**File**: `connection.go` (and `dial.go`)

Find where `NewReceiver()` is called and add `bufferPool` parameter:

```go
// Find existing call like:
// receiver := live.NewReceiver(config, ...)

// Update to:
receiver := live.NewReceiver(config, &c.recvBufferPool, ...)
// OR if recvBufferPool is on listener:
receiver := live.NewReceiver(config, ln.recvBufferPool, ...)
```

---

##### **Step 6: Add helper function for buffer-only cleanup**

**File**: `listen_linux.go`, `listen.go`, `dial_linux.go`, `dial.go`

Add helper for error paths where no packet is allocated:

```go
// returnBufferToPool returns a buffer to the pool without a packet.
// Use when read fails before packet allocation.
func returnBufferToPool(pool *sync.Pool, buf *[]byte) {
    *buf = (*buf)[:0]  // Zero for immediate reuse
    pool.Put(buf)
}
```

#### 2.2 Detailed Steps

**Step 1: Update config.go**

```go
// File: config.go

type Config struct {
    // ... existing fields ...

    // Zero-Copy Buffer Management
    // When true, packet buffers are not copied during deserialization.
    // The buffer lifetime is extended until packet delivery.
    // NOTE: Applications must NOT hold references to packet data after
    // the delivery callback returns.
    UseZeroCopyBuffers bool `json:"use_zero_copy_buffers"`
}

func (c *Config) Validate() error {
    // ... existing validation ...

    // No special validation for UseZeroCopyBuffers
    // It's safe to enable/disable independently
    return nil
}
```

---

#### 2.3 Test Files to Update/Create

| Test File | Changes | Priority |
|-----------|---------|----------|
| `packet/packet_test.go` | **Comprehensive test suite** (see Step 1.1 for details): | **HIGH** |
|  | - `TestUnmarshalZeroCopy` - functional tests (4 sub-tests) | |
|  | - `TestDecommissionWithBuffer` - cleanup tests (3 sub-tests) | |
|  | - `TestGetPayload` - payload access tests (4 sub-tests) | |
|  | - `TestGetPayloadLen` - length calculation tests (2 sub-tests) | |
|  | - `TestUnmarshalZeroCopyRoundTrip` - marshal/unmarshal cycle (6 test cases) | |
|  | - `TestUnmarshalZeroCopyVsCopyEquivalence` - cross-method validation (3 test cases) | |
| `packet/packet_bench_test.go` | **Benchmarks** (CRITICAL for performance validation): | **HIGH** |
|  | - `BenchmarkUnmarshalZeroCopy` - measure zero-copy performance | |
|  | - `BenchmarkUnmarshalCopy` - measure legacy performance for comparison | |
|  | - `BenchmarkUnmarshalComparison` - side-by-side across payload sizes | |
|  | - `BenchmarkGetPayload` - measure payload access overhead | |
| `congestion/live/receive_test.go` | Add `TestReleasePacketFully`, `TestNewReceiverWithBufferPool` | MEDIUM |
| `congestion/live/receive_bench_test.go` | Add benchmark for zero-copy delivery path | MEDIUM |
| `listen_test.go` | Add test for io_uring completion with zero-copy | LOW |
| `dial_test.go` | Add test for dial-side zero-copy | LOW |

**Testing Philosophy**:
1. **Functional correctness** - Every code path must be tested
2. **Round-trip validation** - Marshal → UnmarshalZeroCopy must produce identical packets
3. **Cross-method equivalence** - `UnmarshalZeroCopy` must produce same results as `UnmarshalCopy`
4. **Performance validation** - Benchmarks prove zero-copy is faster and allocation-free
5. **Error handling** - Buffer must be tracked even on parse errors

**Note**: No flag-related tests needed since zero-copy is always-on.

---

#### 2.4 Validation Checkpoint

**Reference**: `integration_testing_design.md`, `integration_testing_matrix_design.md`, `integration_testing_profiling_design.md`, `integration_testing_profiling_design_implementation.md`

##### Unit Testing

```bash
# 1. Build verification
go build ./...

# 2. Unit tests with race detector
go test -race -v ./packet/...
go test -race -v ./congestion/live/...
go test -race -v .

# 3. Run metrics audit (ensure no regressions)
go run tools/metrics-audit/main.go
```

##### Benchmark Testing

```bash
# 4. Run packet benchmarks to validate zero-copy performance
cd /home/das/Downloads/srt/gosrt

# Quick comparison
go test -bench=BenchmarkUnmarshal -benchmem ./packet/

# Detailed comparison with statistical significance
go test -bench=BenchmarkUnmarshalComparison -benchmem -count=5 ./packet/ | tee benchmark_results.txt

# Expected results:
# - BenchmarkUnmarshalZeroCopy/7_MPEGTS: 0 allocs/op, ~50 ns/op
# - BenchmarkUnmarshalCopy/7_MPEGTS: 1 alloc/op, ~350 ns/op
# - Performance improvement: ~7x faster for realistic payload
```

##### Integration Testing

```bash
# 5. Run key isolation tests (zero-copy is always-on now)
cd /home/das/Downloads/srt/gosrt
sudo bash -c 'cd contrib/integration_testing && go run . isolation-test "Isolation-5M-Control"'
sudo bash -c 'cd contrib/integration_testing && go run . isolation-test "Isolation-5M-Server-Btree"'
sudo bash -c 'cd contrib/integration_testing && go run . isolation-test "Isolation-5M-Full"'
```

##### Automated Profiling Analysis (CRITICAL for Performance Validation)

**Reference**: See `integration_testing_profiling_design.md` and `integration_testing_profiling_design_implementation.md` for full details on the automated profiling infrastructure.

The zero-copy optimization targets **memory allocation reduction** and **CPU reduction** (eliminating copy operations). We leverage the existing automated profiling capabilities to validate these improvements:

```bash
# 6. BEFORE Phase 2 implementation - capture baseline profiles
# Save these for comparison!
cd /home/das/Downloads/srt/gosrt

# Baseline CPU profile (measure copy overhead)
sudo PROFILES=cpu bash -c 'cd contrib/integration_testing && go run . isolation-test "Isolation-50M-Full"'
# Output: /tmp/profile_Isolation-50M-Full_*/report.html

# Baseline memory profiles (measure allocation patterns)
sudo PROFILES=heap,allocs bash -c 'cd contrib/integration_testing && go run . isolation-test "Isolation-50M-Full"'
# Expected to show:
# - bytes.makeSlice or similar in top allocators
# - runtime.mallocgc contributing to CPU time
# - Significant allocation counts per packet

# Save baseline report
cp -r /tmp/profile_Isolation-50M-Full_* ./documentation/phase2_baseline_profiles/
```

```bash
# 7. AFTER Phase 2 implementation - capture new profiles
sudo PROFILES=cpu,heap,allocs bash -c 'cd contrib/integration_testing && go run . isolation-test "Isolation-50M-Full"'

# Compare with baseline
# The HTML report will show:
# - Reduced allocation counts (target: ~28-50% reduction)
# - Reduced bytes allocated (target: significant reduction)
# - Reduced GC pressure (fewer GC cycles, shorter pauses)
# - bytes.makeSlice/copy operations should disappear or reduce
```

**What the Profiling Will Show (Expected):**

| Metric | Baseline (Before) | Zero-Copy (After) | Expected Delta |
|--------|-------------------|-------------------|----------------|
| Allocations/sec (server) | ~50,000 | ~35,000 | -30% ⬇ |
| Bytes allocated (server) | ~2 GB | ~0.8 GB | -60% ⬇ |
| GC cycles (60s test) | ~150 | ~60 | -60% ⬇ |
| `runtime.memmove` CPU% | 5-8% | <1% | -80% ⬇ |
| `bytes.makeSlice` CPU% | 3-5% | 0% | -100% ⬇ |
| `runtime.mallocgc` CPU% | 8-12% | 3-5% | -60% ⬇ |

**Profile Types to Capture:**

| Profile Type | What It Measures | Why It Matters for Zero-Copy |
|--------------|------------------|------------------------------|
| `cpu` | CPU time per function | Should show reduction in `memmove`, `mallocgc` |
| `allocs` | Allocation count by function | Should show significant reduction in packet path |
| `heap` | Heap memory in use | Should show lower peak memory, smaller working set |

**Automated Analysis Features:**
- **HTML Report**: Generated automatically at `/tmp/profile_.../report.html`
- **Top Functions**: Parsed from `pprof -top` output
- **Recommendations**: Automatic detection of patterns (channel overhead, allocation hotspots, etc.)
- **Delta Calculations**: When comparing profiles, shows % change per function

##### What We're Validating

| Check | Status | Validation Method |
|-------|--------|-------------------|
| All existing tests pass | ✅ Required | `go test -race ./...` |
| No race conditions | ✅ Required | Race detector |
| No memory leaks | ✅ Required | `heap` profile shows stable memory |
| Buffer pool not exhausted | ✅ Required | No panics under 50Mb/s load |
| Memory allocations reduced | ✅ Expected | `allocs` profile comparison |
| CPU time for copy reduced | ✅ Expected | `cpu` profile shows `memmove` reduction |
| GC pressure reduced | ✅ Expected | Fewer GC cycles in profile |

---

#### 2.5 Acceptance Criteria

##### Functional Requirements

- [ ] All existing tests pass (zero-copy is transparent to existing behavior)
- [ ] Race detector passes: `go test -race ./...`
- [ ] `Isolation-5M-Control` integration test passes
- [ ] `Isolation-5M-Full` (io_uring + btree + NAK btree) integration test passes
- [ ] `Isolation-5M-Server-Btree` integration test passes

##### Performance Requirements (Validated via Automated Profiling)

- [ ] `BenchmarkUnmarshalZeroCopy` shows 0 allocs/op
- [ ] `BenchmarkUnmarshalComparison` shows ~7x speedup for 1316-byte payload
- [ ] `PROFILES=allocs` shows allocation reduction (target: >25%)
- [ ] `PROFILES=heap` shows heap bytes reduction (target: >40%)
- [ ] `PROFILES=cpu` shows `runtime.memmove` reduction (target: >75%)
- [ ] No buffer pool exhaustion under sustained 50Mb/s load for 5 minutes
- [ ] Throughput equal or better (verify via isolation test throughput)

##### Code Quality Requirements

- [ ] All new methods have doc comments
- [ ] Error paths tested (parse failures, buffer cleanup)
- [ ] No linter errors

---

#### 2.6 Implementation Order Summary

```
========== PRE-IMPLEMENTATION (Capture Baseline) ==========
Step 0:   Baseline profiling           [~30 min]  - CRITICAL: Capture BEFORE any changes!
          sudo PROFILES=cpu,heap,allocs make test-isolation CONFIG=Isolation-50M-Full
          cp -r /tmp/profile_Isolation-50M-Full_* ./documentation/phase2_baseline_profiles/

========== IMPLEMENTATION ==========
Step 1:   packet/packet.go             [~60 min]  - Add recvBuffer, n, new methods
Step 1.1: packet/packet_test.go        [~90 min]  - Comprehensive tests + benchmarks
          - Functional tests (4 test functions, ~15 sub-tests)
          - Round-trip tests (6 test cases)
          - Cross-compatibility tests (3 test cases)
          - Benchmarks (4 benchmark functions)
Step 2:   congestion/live/receive.go   [~45 min]  - Add releasePacketFully, bufferPool
Step 2.1: congestion/live/*_test.go    [~20 min]  - Add receiver tests
Step 3:   listen_linux.go              [~45 min]  - Update io_uring completion handler
Step 4:   listen.go                    [~30 min]  - Update standard receive path
Step 5:   connection.go/dial.go        [~30 min]  - Pass bufferPool to receiver

========== VALIDATION ==========
Step 6:   Benchmark validation         [~30 min]  - Run BenchmarkUnmarshalComparison
Step 7:   Integration testing          [~30 min]  - Run isolation tests (verify no regressions)
Step 8:   Profile comparison           [~45 min]  - Run PROFILES=cpu,heap,allocs
          sudo PROFILES=cpu,heap,allocs make test-isolation CONFIG=Isolation-50M-Full
          Compare HTML report with Step 0 baseline
          Verify: allocs reduced, heap reduced, memmove/mallocgc CPU% reduced
```

**Total Estimated Time**: 7-8 hours (including baseline capture)

**⚠️ IMPORTANT**: Step 0 (baseline profiling) MUST be done BEFORE any code changes. This captures the current allocation and CPU behavior so we can measure the improvement. Without baseline data, we cannot prove the optimization works.

**Key Simplification**: No config flags or CLI flag setup needed - zero-copy is always-on!

**Testing is prioritized** because `UnmarshalZeroCopy` is on the hot path for every packet. The benchmarks will validate our performance improvement hypothesis and provide a baseline to prevent future regressions.

**Automated profiling is critical** for validating the memory and CPU improvements that are the primary goal of this phase. The existing profiling infrastructure generates HTML reports with allocation analysis, CPU hotspots, and automatic recommendations.

---

#### 2.7 Risk Mitigation

| Risk | Mitigation |
|------|------------|
| Buffer pool exhaustion | Test with Starlink pattern (60ms outages with packet bursts) |
| Application holds payload reference | Document API contract: "Do not hold packet data after callback returns" |
| Memory leaks | Run long-duration tests, check with pprof |
| Performance regression | Benchmark before/after, compare allocations |
| Race conditions | All tests run with `-race` flag |
| Breaking existing behavior | Zero-copy is transparent - `GetPayload()` works for both paths |

---

### Phase 3: Lock-Free Ring Integration (4-5 hours) [UsePacketRing FLAG]

**Status**: ✅ **COMPLETE** - 2025-12-22

**Implementation Document**: [`lockless_phase3_implementation.md`](./lockless_phase3_implementation.md)

**Goal**: Eliminate lock contention between packet arrival and processing.

**Results**:
- `runtime.futex` (lock wait time) reduced 11-12% on server and client
- All integration tests pass (100% packet recovery)
- Unit tests with race detector pass (14 tests)

**Reference**: Section 5 (Component 1: Lock-Free Ring Buffer)

**Implementation Tracking**: [`lockless_phase3_implementation.md`](./lockless_phase3_implementation.md)

**Prerequisite**: Phase 1 and Phase 2 completed and validated ✅

#### 3.1 Files to Modify

| File | Changes |
|------|---------|
| `go.mod` | Add `github.com/randomizedcoder/go-lock-free-ring` |
| `config.go` | Add `UsePacketRing`, ring config fields |
| `congestion/live/receive.go` | Add ring buffer, function dispatch |

#### 3.2 Detailed Steps

**Step 1: Add dependency**

```bash
go get github.com/randomizedcoder/go-lock-free-ring
```

**Step 2: Update config.go**

```go
// File: config.go

type Config struct {
    // ... existing fields ...

    // Lock-Free Ring Buffer
    UsePacketRing           bool          `json:"use_packet_ring"`
    PacketRingSize          int           `json:"packet_ring_size"`           // Default: 1024 (power of 2)
    PacketRingShards        int           `json:"packet_ring_shards"`         // Default: 4 (power of 2)
    PacketRingMaxRetries    int           `json:"packet_ring_max_retries"`    // Default: 10
    PacketRingBackoffDuration time.Duration `json:"packet_ring_backoff_duration"` // Default: 100µs
    PacketRingMaxBackoffs   int           `json:"packet_ring_max_backoffs"`   // Default: 0 (unlimited)
}

func (c *Config) Validate() error {
    // ... existing validation ...

    if c.UsePacketRing {
        if c.PacketRingSize <= 0 {
            c.PacketRingSize = 1024  // Default: power of 2
        }
        if c.PacketRingShards <= 0 {
            c.PacketRingShards = 4
        }
        // Shards must be power of 2
        if c.PacketRingShards & (c.PacketRingShards-1) != 0 {
            return errors.New("PacketRingShards must be power of 2")
        }
    }
    return nil
}
```

**Step 3: Update congestion/live/receive.go with function dispatch**

```go
// File: congestion/live/receive.go

import ring "github.com/randomizedcoder/go-lock-free-ring"

type receiver struct {
    // ... existing fields ...

    // Lock-free ring (only when UsePacketRing=true)
    packetRing  *ring.ShardedRing
    writeConfig ring.WriteConfig

    // Function dispatch
    pushFn func(pkt *packet.Packet)
}

func NewReceiver(config ReceiverConfig, /* ... */) *receiver {
    r := &receiver{
        // ... existing initialization ...
    }

    // Initialize ring if enabled
    if config.UsePacketRing {
        var err error
        r.packetRing, err = ring.NewShardedRing(
            uint64(config.PacketRingSize),
            uint64(config.PacketRingShards),
        )
        if err != nil {
            panic(fmt.Sprintf("failed to create packet ring: %v", err))
        }

        r.writeConfig = ring.WriteConfig{
            MaxRetries:      config.PacketRingMaxRetries,
            BackoffDuration: config.PacketRingBackoffDuration,
            MaxBackoffs:     config.PacketRingMaxBackoffs,
        }

        r.pushFn = r.pushToRing
    } else {
        r.pushFn = r.pushWithLock
    }

    return r
}

// Push dispatches to configured implementation
func (r *receiver) Push(pkt *packet.Packet) {
    // Rate metrics (always atomic - Phase 1)
    r.packetsReceived.Add(1)
    r.bytesReceived.Add(uint64(pkt.Len()))

    r.pushFn(pkt)
}

// pushWithLock - LEGACY path (UsePacketRing=false)
func (r *receiver) pushWithLock(pkt *packet.Packet) {
    r.mu.Lock()
    defer r.mu.Unlock()

    // ... existing locked insert logic ...
}

// pushToRing - NEW path (UsePacketRing=true)
func (r *receiver) pushToRing(pkt *packet.Packet) {
    producerID := uint64(pkt.Header().PacketSequenceNumber.Val())

    if !r.packetRing.WriteWithBackoff(producerID, pkt, r.writeConfig) {
        r.droppedPackets.Add(1)
        r.releasePacketFully(pkt)
    }
}

// Tick dispatches to configured implementation
func (r *receiver) Tick(now time.Time) {
    if r.config.UsePacketRing {
        r.drainPacketRing()
        // No locks needed - we own the btrees
        r.periodicACKNoLock(now)
        r.periodicNAKNoLock(now)
        r.deliverPacketsNoLock(now)
    } else {
        // Legacy path with locks
        r.periodicACK(now)
        r.periodicNAK(now)
        r.deliverPackets(now)
    }
}

// drainPacketRing consumes all packets from ring into btree
func (r *receiver) drainPacketRing() {
    for {
        item, ok := r.packetRing.TryRead()
        if !ok {
            return
        }

        pkt := item.(*packet.Packet)
        seq := pkt.Header().PacketSequenceNumber

        // Duplicate/old packet check
        if r.packetStore.Has(seq) || seq.Lt(r.deliveryBase) {
            r.releasePacketFully(pkt)
            continue
        }

        // Insert into btree (NO LOCK)
        r.packetStore.Insert(pkt)

        // Delete from NAK btree (NO LOCK)
        if r.nakBtree != nil {
            r.nakBtree.Delete(seq)
        }
    }
}
```

#### 3.3 Validation Checkpoint

**Reference**: `integration_testing_design.md`, `integration_testing_matrix_design.md`, `parallel_comparison_test_design.md`

```bash
# 1. Unit tests with race detector
go test -race -v ./...

# 2. Run Tier 1 AND Tier 2 tests with BOTH flag values
# See integration_testing_matrix_design.md Sections 9-10
cd contrib/integration-tests

# Legacy path (ring disabled)
./run-tests.sh --tier=1,2 --flag=UsePacketRing=false

# New path (ring enabled)
./run-tests.sh --tier=1,2 --flag=UsePacketRing=true

# 3. Parallel comparison: legacy vs ring under IDENTICAL network
# See parallel_comparison_test_design.md - this is the PRIMARY validation
./run-tests.sh --mode=parallel \
    --test=Parallel-Starlink-L5-20M-10s-R60-Base-vs-Ring \
    --compare-all

# 4. Starlink recovery test (gap handling)
# Critical: verify ring doesn't drop packets during 60ms outages
./run-tests.sh --mode=network \
    --test=Net-Starlink-20M-10s-R60-Ring \
    --pattern=starlink

# 5. High-throughput stress test
./run-tests.sh --mode=network \
    --test=Net-Clean-50M-5s-R10-Ring \
    --duration=5m

# 6. CPU profile to verify lock reduction
go test -bench=BenchmarkReceive ./... -cpuprofile=phase3.prof
go tool pprof -top phase3.prof | grep -E "futex|lock"
```

**What we're validating:**
- Tier 1 + Tier 2 pass with both `UsePacketRing=false` and `true`
- Parallel comparison shows identical packet delivery
- Starlink gaps recovered (no ring overflow)
- `runtime.futex` CPU reduced in profile

#### 3.4 Acceptance Criteria

- [ ] All Tier 1 + Tier 2 tests pass with `UsePacketRing=false`
- [ ] All Tier 1 + Tier 2 tests pass with `UsePacketRing=true`
- [ ] Parallel comparison: packet delivery order identical
- [ ] No ring drops under normal load (check `gosrt_ring_drops_total`)
- [ ] `runtime.futex` reduced from 44% to < 10% (pprof)
- [ ] Starlink pattern: 100% recovery rate maintained

---

### Phase 4: Event Loop Architecture (3-4 hours) [UseEventLoop FLAG]

**Goal**: Replace timer-driven Tick() with continuous event loop.

**Reference**: Section 9 (Event Loop Architecture)

**Prerequisites**:
- Phase 1 (Rate Metrics Atomics) - adaptive backoff uses `GetRecvRatePacketsPerSec()`
- Phase 3 (Lock-Free Ring) - event loop consumes from ring buffer

#### 4.1 Files to Modify

| File | Changes |
|------|---------|
| `config.go` | Add `UseEventLoop`, adaptive backoff config |
| `congestion/live/receive.go` | Add `eventLoop()`, adaptive backoff |
| `srt/conn.go` | Add branch in `startReceiver()` |

#### 4.2 Detailed Steps

**Step 1: Update config.go**

```go
// File: config.go

type Config struct {
    // ... existing fields ...

    // Event Loop (requires UsePacketRing=true)
    UseEventLoop         bool          `json:"use_event_loop"`
    RateInterval         time.Duration `json:"rate_interval"`              // Default: 1s
    BackoffColdStartPkts int           `json:"backoff_cold_start_packets"` // Default: 1000
    BackoffMinSleep      time.Duration `json:"backoff_min_sleep"`          // Default: 10µs
    BackoffMaxSleep      time.Duration `json:"backoff_max_sleep"`          // Default: 1ms
}

func (c *Config) Validate() error {
    // ... existing validation ...

    if c.UseEventLoop && !c.UsePacketRing {
        return errors.New("UseEventLoop requires UsePacketRing=true")
    }
    return nil
}
```

**Step 2: Implement event loop in congestion/live/receive.go**

```go
// File: congestion/live/receive.go

// eventLoop is the NEW continuous processing loop
func (r *receiver) eventLoop(ctx context.Context) {
    // Offset tickers to spread work
    ackTicker := time.NewTicker(r.config.ACKInterval)
    time.Sleep(r.config.ACKInterval / 2)
    nakTicker := time.NewTicker(r.config.NAKInterval)
    defer ackTicker.Stop()
    defer nakTicker.Stop()

    backoff := newAdaptiveBackoff(r.config)

    for {
        select {
        case <-ctx.Done():
            return
        case <-ackTicker.C:
            r.periodicACKNoLock(time.Now())
        case <-nakTicker.C:
            r.periodicNAKNoLock(time.Now())
        default:
            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()

            if !processed && delivered == 0 {
                time.Sleep(backoff.getSleepDuration())
            } else {
                backoff.recordActivity()
            }
        }
    }
}

// tickLoop is the LEGACY timer-driven loop
func (r *receiver) tickLoop(ctx context.Context) {
    ticker := time.NewTicker(r.config.TickInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.Tick(time.Now())
        }
    }
}
```

**Step 3: Update srt/conn.go**

```go
// File: srt/conn.go

func (c *connection) startReceiver() {
    if c.config.UseEventLoop {
        go c.receiver.eventLoop(c.ctx)
    } else {
        go c.receiver.tickLoop(c.ctx)
    }
}
```

#### 4.3 Validation Checkpoint

**Reference**: `integration_testing_design.md`, `integration_testing_matrix_design.md`, `parallel_comparison_test_design.md`

```bash
# 1. Unit tests with race detector
go test -race -v ./...

# 2. Run full Tier 1 + Tier 2 + Tier 3 (Comprehensive)
# See integration_testing_matrix_design.md Sections 9-11
cd contrib/integration-tests

# Tick loop (event loop disabled)
./run-tests.sh --tier=1,2,3 --flag=UseEventLoop=false

# Event loop enabled
./run-tests.sh --tier=1,2,3 --flag=UseEventLoop=true

# 3. Parallel comparison: Tick vs Event Loop
# See parallel_comparison_test_design.md - compare latency metrics
./run-tests.sh --mode=parallel \
    --test=Parallel-Starlink-L5-20M-10s-R60-Tick-vs-EventLoop \
    --compare-latency

# 4. Latency-focused tests (continuous delivery verification)
# Event loop should show smoother delivery (lower jitter)
./run-tests.sh --mode=network \
    --test=Net-Starlink-20M-10s-R130-EventLoop \
    --measure-latency-percentiles

# 5. CPU profile comparison: tick vs event loop
go test -bench=BenchmarkReceive ./... -cpuprofile=tick.prof
# Then with event loop enabled
go test -bench=BenchmarkReceive ./... -cpuprofile=eventloop.prof
```

**What we're validating:**
- All Tier tests pass with both `UseEventLoop=false` and `true`
- Event loop shows lower P99 latency than tick loop
- Event loop shows smoother CPU usage (less bursty)
- Packet delivery timing is more continuous (lower jitter)

#### 4.4 Acceptance Criteria

- [ ] All Tier 1 + Tier 2 + Tier 3 tests pass with `UseEventLoop=false`
- [ ] All Tier 1 + Tier 2 + Tier 3 tests pass with `UseEventLoop=true`
- [ ] Parallel comparison: P99 latency improved with event loop
- [ ] Delivery jitter reduced (continuous vs batched)
- [ ] CPU profile shows smoother utilization (less bursty)
- [ ] Adaptive backoff prevents busy-waiting when idle

---

### Phase 5: Integration Testing & Validation (2-3 hours)

**Goal**: Comprehensive validation of all flag combinations using the existing integration testing framework.

**Reference Documents**:
- `integration_testing_design.md` - Core framework and principles
- `integration_testing_matrix_design.md` - Matrix-based test generation
- `parallel_comparison_test_design.md` - Side-by-side configuration comparison

#### 5.1 Test Matrix

The lockless design introduces 3 new feature flags. Combined with existing config variants, we have:

```
Flag Combinations (5 configs):
  Legacy:           UseZeroCopyBuffers=false, UsePacketRing=false, UseEventLoop=false
  ZeroCopy:         UseZeroCopyBuffers=true,  UsePacketRing=false, UseEventLoop=false
  Ring:             UseZeroCopyBuffers=false, UsePacketRing=true,  UseEventLoop=false
  Ring+ZeroCopy:    UseZeroCopyBuffers=true,  UsePacketRing=true,  UseEventLoop=false
  Full Lockless:    UseZeroCopyBuffers=true,  UsePacketRing=true,  UseEventLoop=true

Cross with existing matrix (from integration_testing_matrix_design.md):
  Config Variants:  Base, Btree, IoUr, NakBtree, NakBtreeF, Full
  RTT Profiles:     R0, R10, R60, R130, R300
  Buffer Sizes:     1s, 5s, 10s, 30s
  Bitrates:         20M, 50M
  Loss Patterns:    Clean, L2, L5, L10, Starlink
```

#### 5.2 Integration Test Execution

```bash
cd contrib/integration-tests

# 1. Run ALL tiers for EACH lockless flag combination
for flags in "Legacy" "ZeroCopy" "Ring" "Ring+ZeroCopy" "FullLockless"; do
    ./run-tests.sh --tier=1,2,3 --lockless-config=$flags
done

# 2. Parallel comparison tests (primary validation)
# See parallel_comparison_test_design.md
./run-tests.sh --mode=parallel \
    --test=Parallel-Starlink-L5-20M-10s-R60-Legacy-vs-FullLockless \
    --compare-all

./run-tests.sh --mode=parallel \
    --test=Parallel-Starlink-L5-50M-30s-R130-Legacy-vs-FullLockless \
    --compare-all

# 3. High-RTT tests (GEO satellite simulation)
# See integration_testing_matrix_design.md Section 4 (RTT Profiles)
./run-tests.sh --mode=network \
    --test=Net-Starlink-20M-30s-R300-FullLockless \
    --pattern=starlink

# 4. Stress tests
./run-tests.sh --mode=network \
    --test=Net-Loss-L15-50M-10s-R60-FullLockless \
    --duration=10m

# 5. Long-duration stability (12-24h)
# See integration_testing_design.md Section "Long-Duration Stability"
./run-tests.sh --mode=network \
    --test=Net-Clean-20M-5s-R10-FullLockless \
    --duration=24h \
    --check-memory-leaks
```

#### 5.3 Parallel Comparison Validation

From `parallel_comparison_test_design.md`, the parallel tests run TWO pipelines through IDENTICAL network conditions:

```
=== Parallel Comparison: Starlink-20Mbps ===

Pipeline Configuration:
  Legacy:       list + no io_uring + locks + tick loop
  FullLockless: btree + io_uring + ring + event loop

Network Events:
  Pattern: starlink (60ms 100% loss at 12,27,42,57s intervals)

=== Expected Results ===

                          Legacy        FullLockless    Improvement
  Recovery Rate:          100.0%        100.0%          = (both recover)
  Drops (too_late):       12            3               -75% ✓
  P99 Latency (ms):       15.2          8.4             -45% ✓
  CPU Time (s):           8.45          6.21            -26% ✓
  Heap Allocated (MB):    45.2          32.7            -28% ✓
  runtime.futex:          44%           < 5%            -90% ✓
```

#### 5.4 Acceptance Criteria

- [ ] All Tier 1 + Tier 2 + Tier 3 pass for ALL 5 flag combinations
- [ ] Parallel comparison: identical packet delivery, lower latency
- [ ] Starlink pattern: 100% recovery rate maintained
- [ ] High-RTT (R300): no timeout issues
- [ ] 24h stability: no memory leaks, no degradation
- [ ] `runtime.futex` CPU reduced from 44% to < 5%

---

### Total Estimated Effort

| Phase | Effort | Risk | Value | Status |
|-------|--------|------|-------|--------|
| Phase 1: Rate Atomics | 2-3 hours | Low | Medium (foundation) | ✅ **COMPLETE** |
| Phase 2: Zero-Copy | 3-4 hours | Low | **High** (immediate perf) | ✅ **COMPLETE** |
| Phase 3: Packet Ring | 4-5 hours | Medium | High (lockless) | ✅ **COMPLETE** |
| Phase 4: Event Loop | 3-4 hours | Medium | High (latency) | 🔲 Pending |
| Phase 5: Testing | 2-3 hours | Low | Critical (validation) | 🔲 Pending |
| **Total** | **14-19 hours** | | | **2/5 Complete** |

---

### Recommended Implementation Order

```
Week 1: Universal Improvements
├── Day 1-2: Phase 1 (Rate Atomics) ✅ DONE
│   └── Validated: integration tests pass ✅
├── Day 3-4: Phase 2 (Zero-Copy) ✅ DONE
│   └── Validated: Parallel-Starlink-5Mbps passes ✅
│   └── CPU profile: packet.Header eliminated (17%→0%) ✅
│   └── Shared globalRecvBufferPool for max memory reuse ✅
│
Week 2: Lockless Architecture
├── Day 5-6: Phase 3 (Packet Ring) ✅ DONE
│   └── Add lock-free ring buffer per connection ✅
│   └── Validated: Parallel-Starlink-5M-Full-vs-FullRing ✅
│   └── CPU profile: runtime.futex reduced 11-12% ✅
├── Day 7-8: Phase 4 (Event Loop) ← NEXT
│   └── Validate: latency benchmarks
│
Week 3: Production Rollout
├── Day 9: Phase 5 (Full Testing)
├── Day 10: Enable UsePacketRing in staging
└── Day 11+: Gradual production rollout
```

### Phase Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      IMPLEMENTATION PHASES                                   │
└─────────────────────────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────────────────────┐
  │ PHASE 1: Rate Atomics                                    [NO FLAG]      │
  │ ├── Convert counters to atomic.Uint64                                   │
  │ ├── CAS-based running averages                                          │
  │ └── VALIDATE: integration tests, race detector                          │
  └────────────────────────────────────┬────────────────────────────────────┘
                                       │
                                       ▼
  ┌─────────────────────────────────────────────────────────────────────────┐
  │ PHASE 2: Zero-Copy Buffers                    [UseZeroCopyBuffers flag] │
  │ ├── Add recvBuffer, n to Packet struct                                  │
  │ ├── Implement UnmarshalZeroCopy(), releasePacketFully()                 │
  │ ├── Branch in io_uring + standard receive paths                         │
  │ ├── VALIDATE: integration tests, memory leak tests                      │
  │ └── ★ DEPLOY TO PRODUCTION ★ (immediate perf win!)                      │
  └────────────────────────────────────┬────────────────────────────────────┘
                                       │
  ══════════════════════════════════════════════════════════════════════════
                    Universal improvements complete!
                    Can stop here with significant gains.
  ══════════════════════════════════════════════════════════════════════════
                                       │
                                       ▼
  ┌─────────────────────────────────────────────────────────────────────────┐
  │ PHASE 3: Packet Ring                              [UsePacketRing flag]  │
  │ ├── Add go-lock-free-ring dependency                                    │
  │ ├── Function dispatch: pushWithLock vs pushToRing                       │
  │ ├── Implement drainPacketRing(), *NoLock() variants                     │
  │ ├── VALIDATE: parallel comparison tests (legacy vs ring)                │
  │ └── Enable in staging, compare metrics                                  │
  └────────────────────────────────────┬────────────────────────────────────┘
                                       │
                                       ▼
  ┌─────────────────────────────────────────────────────────────────────────┐
  │ PHASE 4: Event Loop                                [UseEventLoop flag]  │
  │ ├── Implement eventLoop() with offset tickers                           │
  │ ├── Implement adaptive backoff                                          │
  │ ├── Function dispatch: tickLoop vs eventLoop                            │
  │ ├── VALIDATE: latency comparison tests                                  │
  │ └── Enable for latency-sensitive use cases                              │
  └────────────────────────────────────┬────────────────────────────────────┘
                                       │
                                       ▼
  ┌─────────────────────────────────────────────────────────────────────────┐
  │ PHASE 5: Integration Testing                                            │
  │ ├── Run all flag combinations (5 configs)                               │
  │ ├── Parallel comparison: legacy vs full-lockless                        │
  │ ├── Stress tests: high bitrate, packet loss                             │
  │ └── Performance benchmarks: CPU, latency, throughput                    │
  └────────────────────────────────────┬────────────────────────────────────┘
                                       │
                                       ▼
  ┌─────────────────────────────────────────────────────────────────────────┐
  │ PRODUCTION ROLLOUT (incremental)                                        │
  │ ├── Enable UseZeroCopyBuffers=true (after Phase 2)                      │
  │ ├── Enable UsePacketRing=true in staging (after Phase 3)                │
  │ ├── Enable UseEventLoop=true for select connections (after Phase 4)    │
  │ └── Monitor and expand rollout                                          │
  └─────────────────────────────────────────────────────────────────────────┘
```

### Deployment Strategy

| After Phase | Action | Risk | Value |
|-------------|--------|------|-------|
| Phase 1 | Ship atomics (always on) | Very Low | Foundation |
| Phase 2 | Enable `UseZeroCopyBuffers=true` | Low | **Immediate perf!** |
| Phase 3 | Enable `UsePacketRing=true` in staging | Medium | Lockless |
| Phase 4 | Enable `UseEventLoop=true` selectively | Medium | Low latency |

**Key Insight**: You can deploy after Phase 2 and get significant performance improvements without any lockless changes. Phases 3-4 are optional optimizations for maximum performance.

---

## 13. Risk Analysis

### 13.1 Ring Buffer Overflow

**Risk**: Ring fills faster than Tick() drains it.

**Mitigation**:
- Default ring size (1000) provides 5x headroom at 100 Mb/s
- Monitor `droppedPackets` counter
- Consider adaptive ring sizing based on traffic

### 13.2 Tick() Latency Increase

**Risk**: Draining ring adds latency to Tick().

**Mitigation**:
- Batch reads are O(n) but very fast (~1µs per packet)
- 100 packets at 100 Mb/s = ~100µs
- This is less than current lock wait time

### 13.3 Buffer Pool Starvation

**Risk**: Buffers held longer, pool runs out.

**Mitigation**:
- Increase pool size to match ring size + margin
- Monitor pool allocations
- Implement backpressure if pool exhausted

### 13.4 Ordering Guarantees

**Risk**: Lock-free ring doesn't preserve strict order.

**Mitigation**:
- Per-shard ordering is preserved
- SRT already handles out-of-order via sequence numbers
- Packet btree sorts by sequence anyway

---

## 14. Success Metrics

### 14.1 Performance Targets

| Metric | Current | Target | How to Measure |
|--------|---------|--------|----------------|
| `runtime.futex` CPU | 44% | < 5% | `pprof` CPU profile |
| Lock wait time | 0-500µs | 0 | `pprof` mutex profile |
| RTT | 10-11ms | < 2ms | SRT statistics |
| Throughput | 75 Mb/s | 200+ Mb/s | Integration tests |
| Lock acquisitions | ~4300/sec | 0 | Remove lock timing |

### 14.2 Correctness Tests

| Test | Description |
|------|-------------|
| Race detector | `go test -race` must pass |
| Packet delivery | All packets delivered in sequence |
| NAK generation | Losses detected correctly |
| ACK generation | ACKs sent at correct intervals |
| Memory leaks | No buffer pool exhaustion |

### 14.3 Monitoring

Add Prometheus metrics:

```go
// Ring buffer metrics
gosrt_ring_writes_total         // Total packets written to ring
gosrt_ring_reads_total          // Total packets read from ring
gosrt_ring_drops_total          // Packets dropped due to full ring
gosrt_ring_drain_duration_ns    // Time to drain ring in Tick()

// Buffer pool metrics
gosrt_buffer_pool_gets_total    // Pool Get() calls
gosrt_buffer_pool_puts_total    // Pool Put() calls
gosrt_buffer_pool_news_total    // Pool New() calls (allocations)
```

---

## Appendix A: go-lock-free-ring API Reference

### Constructor

```go
func NewShardedRing(totalCapacity uint64, numShards uint64) (*ShardedRing, error)
```

- `totalCapacity`: Total ring capacity (distributed across shards)
- `numShards`: Number of shards (must be power of 2)

### Producer Methods

```go
// Non-blocking write, returns false if shard is full
func (r *ShardedRing) Write(producerID uint64, value any) bool

// Write with configurable backoff when full
func (r *ShardedRing) WriteWithBackoff(producerID uint64, value any, config WriteConfig) bool
```

### Consumer Methods

```go
// Non-blocking read of single item
func (r *ShardedRing) TryRead() (any, bool)

// Batch read up to maxItems (more efficient)
func (r *ShardedRing) ReadBatch(maxItems int) []any

// Zero-allocation batch read with pre-allocated buffer
func (r *ShardedRing) ReadBatchInto(buf []any, maxItems int) int
```

### Utility

```go
func (r *ShardedRing) Len() int   // Current item count
func (r *ShardedRing) Cap() int   // Total capacity
```

---

## Appendix B: Reference Implementation

See `~/Downloads/go-lock-free-ring/cmd/ring/ring.go` for a complete example showing:

- Producer goroutines writing at configurable rates
- Consumer goroutine with batch reading
- Integration with btree for ordered storage
- sync.Pool usage for both packets and buffers
- Proper buffer lifecycle management

