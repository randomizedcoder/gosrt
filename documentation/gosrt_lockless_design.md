# GoSRT Lockless Design

**Status**: DRAFT
**Date**: 2025-12-19
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
    PacketRingSize:            1000,                      // 1000 packets total capacity
    PacketRingShards:          4,                         // 4 shards (250 packets per shard)
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
| PacketRingSize | 1000 | 64-8192 | Sufficient for 10ms at 100 Mb/s |
| PacketRingShards | 4 | 1-16 | Balance contention vs memory |
| Per-Shard | 250 | (calculated) | PacketRingSize / PacketRingShards |
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

    // Tick interval for legacy tick loop (only used when UseEventLoop=false)
    TickInterval time.Duration

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
        p.dataLen = 0
    }
    // Return packet to pool
    p.Decommission()
}
```
- **Pros**: Packet-centric, works without receiver
- **Cons**: Requires passing pool reference, changes Decommission signature

**Option D: Register Pool in Packet (Most Encapsulated)**
```go
// During deserialization
pkt.SetBufferPool(bufferPool)

// Later, single call handles everything
pkt.DecommissionFully()
```
Implementation:
```go
type Packet struct {
    Header     Header
    recvBuffer *[]byte
    dataLen    int
    bufferPool *sync.Pool  // NEW: Optional pool reference for self-cleanup
}

func (p *Packet) SetBufferPool(pool *sync.Pool) {
    p.bufferPool = pool
}

func (p *Packet) DecommissionFully() {
    // Release buffer if pool is set
    if p.recvBuffer != nil && p.bufferPool != nil {
        *p.recvBuffer = (*p.recvBuffer)[:0]
        p.bufferPool.Put(p.recvBuffer)
        p.recvBuffer = nil
        p.dataLen = 0
    }
    p.bufferPool = nil  // Clear pool reference
    // Return packet to pool
    p.Decommission()
}
```
- **Pros**: Most encapsulated, single call, works anywhere
- **Cons**: Adds field to packet struct, pool reference per packet

#### Recommendation: Option B + Option C Hybrid

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
        p.dataLen = 0
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

The new `UnmarshalZeroCopy(buf, n, addr)` sets `recvBuffer` and `dataLen` internally **BEFORE** any validation. This ensures `DecommissionWithBuffer()` can always return the buffer, even if parsing fails:

```go
// NEW SIMPLIFIED PATTERN: Single call does everything
pkt := packetPool.Get().(*packet.Packet)
err := pkt.UnmarshalZeroCopy(buffer, n, addr)  // Sets recvBuffer+dataLen, then parses
if err != nil {
    // recvBuffer is ALREADY tracked (set before validation in UnmarshalZeroCopy)
    pkt.DecommissionWithBuffer(pool)           // ✓ Will return buffer to pool
    return
}
// Success - pkt.GetPayload() returns (*recvBuffer)[HeaderSize:dataLen]
```

**Why this is safe:** `UnmarshalZeroCopy` stores `recvBuffer` and `dataLen` as its FIRST action, before any validation or parsing. If header parsing fails, the buffer is still tracked.

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

**Optimization insight**: Since `HeaderSize` is constant (16 bytes) and we know `n` (bytes received), we don't need to store a separate `Payload` slice. We can compute the payload on-demand:

```go
payload = (*p.recvBuffer)[HeaderSize:p.dataLen]
```

This is more efficient:
- **No Payload slice storage** - just store `dataLen` (an int)
- **Simpler struct** - one pointer + one int instead of two pointers
- **Faster UnmarshalZeroCopy** - no need to create/store a sub-slice

**Solution**: Store `recvBuffer` (original pool buffer) and `dataLen` (bytes received):

```go
type Packet struct {
    Header     Header
    recvBuffer *[]byte  // ORIGINAL buffer from recvBufferPool (for return)
    dataLen    int      // Total bytes received (n from ReadFromUDP/io_uring)
}

// Memory layout:
//
// recvBuffer ──► ┌──────────────────┬──────────────────────────┬─────────┐
//                │ Header (16 bytes)│ Payload Data             │ Unused  │
//                └──────────────────┴──────────────────────────┴─────────┘
//                │◄──────────────── dataLen bytes ────────────►│
//
// GetPayload() computes: (*recvBuffer)[HeaderSize:dataLen]

// GetPayload returns the payload portion of the buffer (computed on-demand)
// This is more efficient than storing a separate Payload slice
func (p *Packet) GetPayload() []byte {
    if p.recvBuffer == nil {
        return nil
    }
    return (*p.recvBuffer)[HeaderSize:p.dataLen]
}

// GetPayloadLen returns the payload length (dataLen - HeaderSize)
func (p *Packet) GetPayloadLen() int {
    if p.dataLen <= HeaderSize {
        return 0
    }
    return p.dataLen - HeaderSize
}

// GetRecvBuffer returns the original buffer reference for pool release
func (p *Packet) GetRecvBuffer() *[]byte {
    return p.recvBuffer
}

// ClearRecvBuffer clears the buffer reference after pool release
func (p *Packet) ClearRecvBuffer() {
    p.recvBuffer = nil
    p.dataLen = 0
}

// HasRecvBuffer returns true if packet has a tracked pool buffer
func (p *Packet) HasRecvBuffer() bool {
    return p.recvBuffer != nil
}

// NOTE: No SetRecvBuffer() needed!
// UnmarshalZeroCopy(buf, n, addr) sets both recvBuffer and dataLen internally.
// This is cleaner and ensures buffer is always tracked if parsing fails.
```

**Why this is better than storing a Payload slice:**

| Approach | Storage | GetPayload() | UnmarshalZeroCopy |
|----------|---------|--------------|-------------------|
| Store `Payload *[]byte` | 24 bytes (slice header) | Direct return | Must create sub-slice |
| Store `dataLen int` | 8 bytes (int) | Compute slice | Just store n |
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
                                     │ dataLen int          │ ← bytes received
                                     └──────────────────────┘
                                             │
                                             │ GetPayload() computes:
                                             │ (*recvBuffer)[HeaderSize:dataLen]
                                             ▼
```

**Key insight**: The Packet struct stores:
1. `recvBuffer` - Original pool buffer (for `Put()` back to pool)
2. `dataLen` - Bytes received (to compute payload slice on-demand)

**No Payload slice stored!** `GetPayload()` computes `(*recvBuffer)[HeaderSize:dataLen]`

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
// Payload access is via GetPayload() which computes: (*recvBuffer)[HeaderSize:dataLen]
//
// This is MORE EFFICIENT than separate SetRecvBuffer() + UnmarshalZeroCopy():
// - Single function call
// - Can't forget to set buffer before parsing
// - Buffer is always tracked if parsing fails (safe error handling)
func (p *Packet) UnmarshalZeroCopy(buf *[]byte, n int, addr net.Addr) error {
    // Store buffer and length FIRST (before any validation that might fail)
    // This ensures DecommissionWithBuffer() can always return the buffer
    p.recvBuffer = buf
    p.dataLen = n

    // Now validate
    if n < HeaderSize {
        return ErrPacketTooShort
    }

    // Parse header directly from recvBuffer
    if err := p.Header.Unmarshal((*p.recvBuffer)[:HeaderSize]); err != nil {
        return err
    }

    p.Addr = addr
    return nil

    // NOTE: No Payload slice created!
    // Callers use GetPayload() which computes: (*recvBuffer)[HeaderSize:dataLen]
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
p.recvBuffer points here                       p.dataLen = 200
(original pool buffer)

GetPayload() computes: (*recvBuffer)[HeaderSize:dataLen]
                       (*recvBuffer)[16:200] → 184 bytes of payload

NO separate Payload pointer stored - computed on demand!
```

#### 6.7.5 Comparison

| Aspect | UnmarshalCopy | UnmarshalZeroCopy |
|--------|---------------|-------------------|
| Signature | `(addr, data []byte)` | `(addr)` |
| Data copy | Yes (`copy()`) | No |
| Memory allocation | Yes (`make()`) | No |
| Payload storage | `Payload *[]byte` (24 bytes) | `dataLen int` (8 bytes) |
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
//    - pkt.dataLen = n (bytes received)
//    - pkt.GetPayload() → computed slice (*recvBuffer)[HeaderSize:dataLen]
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
    //   p.dataLen = n (bytes received)
    //   p.GetPayload() → computed slice (*recvBuffer)[HeaderSize:dataLen]
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
        //   pkt.dataLen = n (bytes received)
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
- Payload access via `GetPayload()` → computed `(*recvBuffer)[HeaderSize:dataLen]`
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
4. **Clears buffer reference**: `p.ClearRecvBuffer()` (sets `recvBuffer = nil`, `dataLen = 0`)
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

    // === Update tracking ONCE after batch ===
    // This reduces atomic store overhead vs per-packet updates
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
1. **Batch tracking updates** - `lastPacketArrivalTime` and `lastDataPacketSeq` updated once per batch
2. **Track max sequence** - Stores highest sequence seen for meaningful FastNAK tracking
3. **Proper cleanup** - Single `releasePacketFully()` call handles both buffer and packet
4. **Config-based batch size** - Uses `PacketRingSize` from config

---

## 8. Component 4: Rate Metrics Migration to Atomics

### 8.1 Overview

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
func (r *receiver) eventLoop(ctx context.Context) {
    ackInterval := time.Duration(r.config.PeriodicAckIntervalMs) * time.Millisecond
    nakInterval := time.Duration(r.config.PeriodicNakIntervalMs) * time.Millisecond

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
    ticker := time.NewTicker(time.Duration(r.config.PeriodicAckIntervalMs) * time.Millisecond)
    defer ticker.Stop()

    tickCount := uint64(0)
    nakEveryN := r.config.PeriodicNakIntervalMs / r.config.PeriodicAckIntervalMs // e.g., 20/10 = 2

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

#### Recommended: Offset Tickers with Idle Handling

The cleanest solution combines offset tickers with intelligent idle handling:

```go
func (r *receiver) eventLoop(ctx context.Context) {
    ackInterval := time.Duration(r.config.PeriodicAckIntervalMs) * time.Millisecond
    nakInterval := time.Duration(r.config.PeriodicNakIntervalMs) * time.Millisecond

    // Offset NAK by half ACK interval
    ackTicker := time.NewTicker(ackInterval)
    defer ackTicker.Stop()

    // Small delay before starting NAK ticker
    nakTicker := time.NewTicker(nakInterval)
    nakTicker.Stop() // Stop immediately, we'll restart with offset

    // Start NAK ticker with offset
    go func() {
        time.Sleep(ackInterval / 2)
        nakTicker.Reset(nakInterval)
    }()
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
            // Process packets and deliver
            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()

            // If idle (nothing to do), yield briefly to avoid spinning
            if !processed && delivered == 0 {
                // Check if any ticker is about to fire
                // If not, sleep briefly
                time.Sleep(100 * time.Microsecond)
            }
        }
    }
}
```

**Why the idle sleep helps with spinning:**
- When no packets arriving and no deliveries pending, the `default` case would spin
- The 100µs sleep yields CPU while still being responsive
- When packets ARE arriving, the sleep is skipped (processed=true)
- Tickers still fire promptly via their channels

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
    ackTicker := time.NewTicker(time.Duration(r.config.PeriodicAckIntervalMs) * time.Millisecond)
    nakTicker := time.NewTicker(time.Duration(r.config.PeriodicNakIntervalMs) * time.Millisecond)
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

```go
type adaptiveBackoff struct {
    // Rate tracking (from rate_metrics)
    packetsPerSecond atomic.Uint64  // Stored as Float64bits

    // Backoff state
    coldStartPackets int64          // Countdown to warm state
    minSleep         time.Duration  // Floor (never sleep less)
    maxSleep         time.Duration  // Ceiling (never sleep more)
    sleepFraction    float64        // Fraction of inter-packet time
}

func newAdaptiveBackoff() *adaptiveBackoff {
    return &adaptiveBackoff{
        coldStartPackets: 1000,      // Process 1000 packets before adapting
        minSleep:         1 * time.Microsecond,
        maxSleep:         1 * time.Millisecond,
        sleepFraction:    0.25,      // Sleep for 25% of expected inter-packet time
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

    // Get current rate
    pps := math.Float64frombits(ab.packetsPerSecond.Load())
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
    backoff := newAdaptiveBackoff()

    // Link to rate metrics
    backoff.packetsPerSecond = r.metrics.packetsPerSecond  // Share the atomic

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

    // Expected inter-packet time
    pps := math.Float64frombits(ab.packetsPerSecond.Load())
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

**Rationale**: Changing `Payload` from `[]byte` to `*[]byte` in the packet struct is backwards compatible:
- Old code that copies data simply sets `Payload` to a new slice (not pooled)
- New zero-copy code sets `Payload` to the pooled buffer directly
- No behavioral change unless buffer lifetime extension is enabled
- Zero overhead when not used (Payload still works the same way)

### 11.4 Features WITH Flags

#### 11.4.1 Lock-Free Ring Buffer

**Config Field**: `UsePacketRing bool`

**Branch Points**:

1. **receiver.Push()** - Packet arrival path:

```go
func (r *receiver) Push(pkt *packet.Packet) {
    // Update rate metrics (always atomic)
    r.metrics.packetsReceived.Add(1)

    if r.config.UsePacketRing {
        // NEW: Write to lock-free ring
        r.packetRing.Write(producerID, pkt)
    } else {
        // LEGACY: Lock and insert to btree directly
        r.mu.Lock()
        r.packetStore.ReplaceOrInsert(pkt)
        r.nakBtree.Delete(pkt.Header.PacketSequenceNumber)
        r.mu.Unlock()
    }
}
```

2. **receiver.Tick()** - Processing path:

```go
func (r *receiver) Tick(now time.Time) {
    if r.config.UsePacketRing {
        // NEW: Drain ring into btree (no lock needed)
        r.drainPacketRing()
    }
    // Rest of tick() proceeds identically
    // (periodicACK, periodicNAK, delivery)

    if r.config.UsePacketRing {
        // No locks needed - we own the btrees
        r.periodicACKNoLock(now)
        r.periodicNAKNoLock(now)
        r.deliverPacketsNoLock()
    } else {
        // LEGACY: Use existing locked versions
        r.periodicACK(now)
        r.periodicNAK(now)
        r.deliverPackets()
    }
}
```

**Config**:

```go
type Config struct {
    // ... existing fields ...

    // Lock-Free Ring Configuration
    UsePacketRing    bool  // Enable lock-free ring buffer (default: false)
    PacketRingSize   int   // Ring capacity (default: 1000)
    PacketRingShards int   // Number of shards (default: 4)
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
        PacketRingSize:          1000,
        PacketRingShards:        4,
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

    // Ring configuration validation
    if c.UsePacketRing {
        if c.PacketRingSize < 100 {
            return errors.New("PacketRingSize must be >= 100")
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

The implementation is organized so that **universal improvements** (benefits ALL paths) come first, followed by lockless-specific changes.

### Phase 1: Rate Metrics Atomics (2-3 hours) [NO FLAG - Universal]

1. Convert counter fields to atomic.Uint64
2. Implement CAS-based running averages
3. Convert computed fields to atomic.Value
4. Update all read/write sites
5. Run race detector tests
6. **Parallel tests**: Compare metrics accuracy old vs new

**Note**: Benefits ALL implementations (legacy, lockless, io_uring, standard recv).

### Phase 2: Buffer Lifetime Extension (2-3 hours) [UseZeroCopyBuffers FLAG - Universal]

1. Add `UseZeroCopyBuffers` to Config
2. Change `Payload` from `[]byte` to `*[]byte` in packet struct
3. Add `releasePacketFully()` to receiver and `DecommissionWithBuffer()` to packet
4. Add `UnmarshalZeroCopy()` for zero-copy deserialization (keep `UnmarshalCopy()` for legacy)
5. Add branch in **both** io_uring AND standard receive paths
6. Add branch in delivery: release buffer when flag enabled
7. Test for memory leaks extensively
8. **Parallel tests**: Compare legacy vs legacy+zero-copy (immediate win!)

**Note**: Benefits ALL implementations. Can be deployed immediately before any lockless changes.

**Why Phase 2?** Zero-copy provides immediate performance benefit on the existing legacy implementation. By implementing it early:
- Immediate production value
- Validates buffer management approach before lockless changes
- Reduces risk - smaller, isolated change

### Phase 3: Lock-Free Ring Integration (3-4 hours) [UsePacketRing FLAG]

1. Add `go-lock-free-ring` dependency to `go.mod`
2. Add `UsePacketRing`, `PacketRingSize`, `PacketRingShards` to Config
3. Add ring buffer to receiver struct
4. Implement `drainPacketRing()` function
5. Add branch in `Push()`: ring write vs locked btree insert
6. Add branch in `Tick()`: call `drainPacketRing()` when flag enabled
7. Create locked/unlocked variants of periodic functions
8. **Parallel tests**: Run with flag=false (legacy) and flag=true (new)

### Phase 4: Event Loop (3-4 hours) [UseEventLoop FLAG]

1. Add `UseEventLoop` to Config with validation (requires UsePacketRing)
2. Implement `eventLoop()` function (section 9.3)
3. Implement adaptive backoff (section 9.7)
4. Add branch in connection setup: eventLoop vs tickLoop
5. Add offset ticker initialization
6. **Parallel tests**: Compare latency/throughput: tick vs event loop

### Phase 5: Integration Testing (2-3 hours)

1. Unit tests for each new component
2. Run all flag combinations from section 11.5
3. Parallel comparison tests (section 11.7)
4. Stress tests with packet loss at various bitrates
5. Performance comparison benchmarks
6. Memory leak testing with `-race` flag
7. Update `integration_testing_matrix_design.md` with new dimensions

**Total Estimated Effort**: 13-17 hours

### Phase Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    UNIVERSAL IMPROVEMENTS                                │
│                (Benefits ALL implementations)                            │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Phase 1: Rate Atomics           [NO FLAG - Always On]                  │
│      │                                                                   │
│      ▼                                                                   │
│  Phase 2: Zero-Copy Buffers      [UseZeroCopyBuffers flag]              │
│      │                           ← Can deploy to production NOW!         │
│      │                                                                   │
└──────┼──────────────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                    LOCKLESS ARCHITECTURE                                 │
│                (Optional - for maximum performance)                      │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  Phase 3: Packet Ring            [UsePacketRing flag]                   │
│      │                                                                   │
│      ▼                                                                   │
│  Phase 4: Event Loop             [UseEventLoop flag]                    │
│                                  (requires UsePacketRing)                │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
       │
       ▼
  Phase 5: Integration Testing
       │
       ▼
  Production Rollout (incremental by flag)
```

**Deployment Strategy**:
1. Deploy Phase 1+2 immediately (universal improvements, low risk)
2. Enable `UseZeroCopyBuffers=true` in production (instant perf gain)
3. Deploy Phase 3+4 behind feature flags
4. Gradually enable `UsePacketRing` then `UseEventLoop` per connection/use-case

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

