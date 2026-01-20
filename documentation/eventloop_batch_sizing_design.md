# EventLoop Batch Sizing Design

> **Status**: Design Phase
> **Parent**: [performance_testing_implementation_log.md](performance_testing_implementation_log.md)
> **Goal**: Break through the ~350 Mb/s throughput ceiling by optimizing EventLoop batching

---

## Table of Contents

1. [Problem Statement](#problem-statement)
2. [All Batching Layers](#all-batching-layers)
3. [Current Implementation Analysis](#current-implementation-analysis)
4. [Root Cause: Unbounded Drain](#root-cause-unbounded-drain)
5. [Proposed Solution: Interleaved Batching](#proposed-solution-interleaved-batching)
6. [Design Options](#design-options)
7. [Recommended Approach](#recommended-approach)
8. [Implementation Plan](#implementation-plan)
9. [Testing Strategy](#testing-strategy)
10. [Metrics & Observability](#metrics--observability)

---

## Problem Statement

At ~350 Mb/s, the system hits **EventLoop Starvation**:

---

## All Batching Layers

There are **three distinct batching layers** in the system. Understanding all three is critical for optimization.

### Layer 1: io_uring Batching (Network → Lock-Free Ring)

**Where**: `listen_linux.go`, `dial_linux.go`

**How io_uring puts packets INTO the lock-free ring:**

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        NETWORK → RING FLOW                              │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  Network ──► io_uring CQE ──► processRecvCompletion() ──► handleData() │
│                  │                      │                     │         │
│                  │                      │                     ▼         │
│                  │                      │              recv.Push(pkt)   │
│                  │                      │                     │         │
│                  │                      ▼                     ▼         │
│                  │              (one at a time!)       packetRing.Write │
│                  │                                                      │
│                  ▼                                                      │
│            batchSize controls resubmit batching (NOT packet batching)   │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

**Key insight**: io_uring processes completions **ONE AT A TIME** and calls `Push()` for each:

```go
// listen_linux.go:1085-1091
for {
    cqe, compInfo, result := ln.getRecvCompletionFromRing(ctx, state)  // Get ONE completion

    switch result {
    case completionResultSuccess:
        ln.processRecvCompletionFromRing(state, cqe, compInfo)  // Process ONE packet
        // ... eventually calls recv.Push(pkt) - ONE packet
```

**Batching parameters:**
- `IoUringRecvBatchSize` (default: 32): Controls resubmit batching, NOT packet processing
- `IoUringRecvRingCount` (default: 2): Multiple rings = multiple goroutines = parallel completion handlers

### Layer 2: io_uring Resubmit Batching (Buffer Management)

**Where**: `submitRecvRequestBatchToRing()` in `listen_linux.go`

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     BUFFER RESUBMIT BATCHING                            │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  Completion Handler:                                                    │
│    Process CQE #1 → pendingResubmits++                                  │
│    Process CQE #2 → pendingResubmits++                                  │
│    ...                                                                  │
│    Process CQE #N → pendingResubmits++                                  │
│                                                                         │
│    if pendingResubmits >= batchSize:                                    │
│        submitRecvRequestBatchToRing(state, pendingResubmits)  ←──┐      │
│        pendingResubmits = 0                                      │      │
│                                                                  │      │
│  This batches SQE SUBMISSION, not packet processing!     ────────┘      │
│  (Reduces syscalls: one Submit() call for N buffers)                    │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Layer 3: EventLoop Ring Drain (Lock-Free Ring → Btree)

**Where**: `eventloop.go` (sender), `event_loop.go` (receiver)

**This is the layer we need to optimize!**

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     EVENTLOOP RING SERVICING                            │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                         │
│  ┌─────────────────────┐    ┌─────────────────────┐                     │
│  │   DATA RING         │    │   CONTROL RING      │                     │
│  │   (packet data)     │    │   (ACK/NAK/Tick)    │                     │
│  │                     │    │                     │                     │
│  │   Filled by:        │    │   Filled by:        │                     │
│  │   - io_uring compl  │    │   - connection      │                     │
│  │   - recv.Push()     │    │   - handlers        │                     │
│  └─────────┬───────────┘    └─────────┬───────────┘                     │
│            │                          │                                 │
│            ▼                          ▼                                 │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │                      EVENT LOOP                                  │    │
│  │                                                                  │    │
│  │   SENDER EventLoop (eventloop.go):                              │    │
│  │   ┌─────────────────────────────────────────────────────────┐   │    │
│  │   │ 1. processControlPacketsDelta()   ← drain ALL control   │   │    │
│  │   │ 2. drainRingToBtreeEventLoop()    ← drain ALL data !!!  │   │    │
│  │   │ 3. processControlPacketsDelta()   ← drain ALL control   │   │    │
│  │   │ 4. deliverReadyPacketsEventLoop() ← iterate btree       │   │    │
│  │   │ 5. processControlPacketsDelta()   ← drain ALL control   │   │    │
│  │   │ 6. adaptiveBackoff.Wait()                                │   │    │
│  │   └─────────────────────────────────────────────────────────┘   │    │
│  │                                                                  │    │
│  │   RECEIVER EventLoop (event_loop.go):                           │    │
│  │   ┌─────────────────────────────────────────────────────────┐   │    │
│  │   │ 1. processControlPacketsWithMetrics()                    │   │    │
│  │   │ 2. Handle tickers (ACK/NAK/Rate)                         │   │    │
│  │   │ 3. processControlPacketsWithMetrics()                    │   │    │
│  │   │ 4. drainRingByDelta()              ← drain BY DELTA      │   │    │
│  │   │ 5. processControlPacketsWithMetrics()                    │   │    │
│  │   │ 6. deliverReadyPackets()                                 │   │    │
│  │   │ 7. processControlPacketsWithMetrics()                    │   │    │
│  │   │ 8. processOnePacket()              ← process ONE         │   │    │
│  │   │ 9. contiguousScan()                                      │   │    │
│  │   │ 10. adaptive backoff                                     │   │    │
│  │   └─────────────────────────────────────────────────────────┘   │    │
│  └──────────────────────────────────────────────────────────────────┘    │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### The Key Difference: Sender vs Receiver

| Aspect | Sender EventLoop | Receiver EventLoop |
|--------|------------------|-------------------|
| Data drain | `drainRingToBtreeEventLoop()` - **drains ALL** | `drainRingByDelta()` - **drains by delta** |
| Additional | None | `processOnePacket()` - processes ONE extra |
| Control checks | 3× per iteration | 4× per iteration |
| Problem | **Unbounded data drain** | More balanced |

### Configuration Parameters by Layer

| Layer | Parameter | Default | Purpose |
|-------|-----------|---------|---------|
| **io_uring** | `IoUringRecvBatchSize` | 32 | Resubmit batching |
| **io_uring** | `IoUringRecvRingCount` | 2 | Parallel handlers |
| **io_uring** | `IoUringRecvRingSize` | 256 | SQE/CQE capacity |
| **io_uring** | `IoUringRecvInitialPending` | 512 | Pre-posted buffers |
| **Sender Ring** | `SendRingSize` | 8192 | Per-shard capacity |
| **Sender Ring** | `SendRingShards` | 4 | Parallelism |
| **Receiver Ring** | `PacketRingSize` | 1024 | Per-shard capacity |
| **Receiver Ring** | `PacketRingShards` | 4 | Parallelism |
| **EventLoop** | **NEW: DataBatchSize** | **64** | **Packets per batch** |
| **EventLoop** | **NEW: MaxDataPerIteration** | **512** | **Cap per iteration** |

---

## Current Implementation Analysis (Sender Focus)

The **sender** EventLoop has the unbounded drain problem. The receiver already uses `drainRingByDelta()` which is bounded by the delta between received and processed counters.

### Sender EventLoop (`congestion/live/send/eventloop.go`)

```go
for {
    // 1. Service control ring (ACK/NAK)
    processControlPacketsDelta()       // ← Fast: just what's there

    // 2. Drain ALL data ring → btree
    drainRingToBtreeEventLoop()        // ← BOTTLENECK: unbounded!

    // 3. Service control ring again
    processControlPacketsDelta()

    // 4. Deliver ready packets
    deliverReadyPacketsEventLoop()     // ← btree iteration

    // 5. Service control ring again
    processControlPacketsDelta()

    // 6. Adaptive backoff
    adaptiveBackoff.Wait(hadActivity)
}
```

### The Problem: `drainRingToBtreeEventLoop()`

```go
func (s *sender) drainRingToBtreeEventLoop() int {
    for {
        p, ok := s.packetRing.TryPop()
        if !ok {
            break  // ← Only exits when ring is EMPTY
        }
        // Insert to btree...
    }
}
```

At 350 Mb/s with 1456-byte packets:
- **~30,000 packets/second** arrive
- Ring could have **thousands of packets** accumulated
- Draining all takes **milliseconds**
- During this time, **control ring processing is blocked**

### Why Control Delay Matters

1. **ACK Delay**: Sender doesn't know receiver got packets → unnecessary retransmits
2. **NAK Delay**: Lost packet detection delayed → recovery takes longer
3. **RTT Measurement**: ACKACK timing is off → sender pacing is wrong

---

## Root Cause: Unbounded Drain

### Evidence from Performance Test

```
BOTTLENECK DETECTION: LIBRARY-LIMITED
Throughput Efficiency: 89.4%
Generator Efficiency: 89.4%
HYPOTHESIS: EventLoop Starvation
```

The 10.6% efficiency loss suggests ~10% of time is spent in long operations that block progress.

### Mathematical Analysis

At 350 Mb/s:
- Packet rate: 350,000,000 / (1456 × 8) = **30,036 packets/sec**
- If ring batches 100ms: **3,000 packets** to drain
- btree insert: ~500ns/packet
- Drain time: 3,000 × 500ns = **1.5ms**

During those 1.5ms:
- Control ring grows with incoming ACKs
- ACK latency = 1.5ms + processing
- At 10ms ACK interval, that's **15% of the ACK budget!**

---

## Proposed Solution: Tight Loop with Control Priority

### Core Insight

Checking an empty control ring is **nearly free** (~2ns - one atomic load). So we should check control after EVERY data packet, not in batches.

### Old Approach (Batched)
```
[Drain 64 data] → [Control] → [Drain 64 data] → [Control] → ... → [Deliver]
Control latency: up to 64 × 500ns = 32µs
```

### New Approach (Tight Loop)
```
[Control] → [1 data] → [Control] → [1 data] → [Control] → ... → [Deliver]
Control latency: ~500ns (one btree insert)
```

### Why Tight Loop is Better

| Metric | Batched (64) | Tight Loop (1) |
|--------|--------------|----------------|
| Control latency | 32µs worst case | **500ns worst case** |
| Check overhead | 30,000/64 = 469/sec | 30,000/sec |
| Check cost | ~2ns each | ~2ns each |
| **Total overhead** | ~1µs/sec | **~60µs/sec = 0.006%** |

The overhead difference is negligible, but the latency improvement is 64×!

### The Math

At 350 Mb/s:
- **Data packets**: ~30,000/sec
- **Control packets**: ~150-200/sec (ACK@10ms + NAK@20ms)
- **Ratio**: 200:1 (data much higher volume than control)

Empty control ring check cost:
```go
cp, ok := s.controlRing.TryPop()  // Single atomic load + comparison
if !ok { break }                   // Branch prediction: always taken when empty
```
**Cost: ~2ns**

Total overhead: 30,000 × 2ns = **60µs/sec = 0.006%** ← negligible!

### Key Parameters

```go
const (
    // Only one parameter needed!
    MaxDataPerIteration = 512   // Cap to prevent delivery starvation
)
```

### Rationale

**MaxDataPerIteration = 512**:
- Caps worst-case data processing to ~256µs
- Ensures delivery happens regularly
- Prevents starvation if ring fills faster than we drain

---

## Design Options

### Option A: Fixed Batch Interleaving (Original Proposal)

```go
func (s *sender) drainRingToBtreeEventLoopBatched() int {
    totalDrained := 0

    for totalDrained < MaxDataPerIteration {
        // Batch of 64 data packets
        for batchDrained := 0; batchDrained < 64; batchDrained++ {
            p, ok := s.packetRing.TryPop()
            if !ok {
                return totalDrained
            }
            s.insertToBtree(p)
            totalDrained++
        }
        // Check control after each batch
        s.processControlPacketsDelta()
    }
    return totalDrained
}
```

**Pros**: Simple, predictable timing
**Cons**: Control latency up to 32µs (64 × 500ns)

### Option B: Tight Loop (RECOMMENDED)

```go
func (s *sender) drainRingToBtreeEventLoopTight() int {
    drained := 0

    for drained < MaxDataPerIteration {
        // 1. ALWAYS check control first (high priority, ~2ns when empty)
        s.processControlPacketsDelta()

        // 2. Process ONE data packet
        p, ok := s.packetRing.TryPop()
        if !ok {
            break  // Data ring empty
        }

        s.insertPacketToBtree(p)
        drained++
    }

    return drained
}
```

**Pros**:
- **Minimum control latency** (~500ns = one btree insert)
- **Simpler** - no batch size tuning
- **Naturally adaptive** - processes control at exactly the right rate
- **Negligible overhead** - empty ring check is ~2ns

**Cons**:
- More function calls (but they're nearly free when ring empty)

### Option C: Hybrid (Tight with Occasional Full Drain)

```go
func (s *sender) drainRingToBtreeEventLoopHybrid() int {
    drained := 0

    for drained < MaxDataPerIteration {
        // Check control
        controlCount := s.processControlPacketsDelta()

        // If control is busy, stay tight. If idle, batch a few.
        batchSize := 1
        if controlCount == 0 && drained > 0 {
            batchSize = 8  // Small batch when control is quiet
        }

        for i := 0; i < batchSize; i++ {
            p, ok := s.packetRing.TryPop()
            if !ok {
                return drained
            }
            s.insertPacketToBtree(p)
            drained++
        }
    }

    return drained
}
```

**Pros**: Balances latency and efficiency
**Cons**: More complex, may not be necessary

---

## Recommended Approach

**Option B: Tight Loop** for implementation.

### Reasons

1. **Minimum latency**: Control processed within ~500ns of arrival
2. **Simplicity**: Only ONE parameter (`MaxDataPerIteration`)
3. **Negligible overhead**: Empty ring check is ~2ns
4. **Self-tuning**: No batch sizes to optimize

### Specific Recommendation

```go
const (
    // Only parameter needed
    MaxDataPerIteration = 512  // Cap to ensure delivery runs regularly
)
```

### Why Tight Loop Wins

The key insight: **checking an empty ring is essentially free**.

```go
// This costs ~2ns when ring is empty:
cp, ok := s.controlRing.TryPop()
if !ok { break }
```

So we can afford to check control after EVERY data packet:
- 30,000 checks/sec × 2ns = 60µs/sec overhead
- That's **0.006%** of CPU time
- But control latency drops from 32µs to **500ns** (64× improvement!)

---

## Implementation Plan

### Phase 1: Add Tight Loop Drain Function

**File**: `congestion/live/send/eventloop.go`

```go
// drainRingToBtreeEventLoopTight drains packets with control-priority tight loop.
// Checks control ring after EVERY data packet for minimum latency.
// Returns total packets drained.
//
// Why tight loop? At 350+ Mb/s:
// - Empty control ring check costs ~2ns (one atomic load)
// - 30,000 checks/sec × 2ns = 60µs/sec = 0.006% overhead
// - But control latency drops from 32µs (batched) to ~500ns (tight)
//
// Reference: eventloop_batch_sizing_design.md "Tight Loop" section
func (s *sender) drainRingToBtreeEventLoopTight() int {
    m := s.metrics
    drained := 0

    for drained < s.maxDataPerIteration {
        // 1. ALWAYS check control first (high priority, ~2ns when empty)
        if controlDrained := s.processControlPacketsDelta(); controlDrained > 0 {
            m.SendEventLoopControlDrained.Add(uint64(controlDrained))
        }

        // 2. Process ONE data packet
        p, ok := s.packetRing.TryPop()
        if !ok {
            break  // Data ring empty
        }

        // 3. Insert to btree (same logic as unbounded version)
        s.insertPacketToBtreeWithMetrics(p)
        drained++
    }

    // Track if we hit the cap
    if drained >= s.maxDataPerIteration {
        m.SendEventLoopTightCapReached.Add(1)
    }

    return drained
}

// insertPacketToBtreeWithMetrics extracts the btree insert logic for reuse.
// Called from both unbounded drain and tight loop drain.
func (s *sender) insertPacketToBtreeWithMetrics(p packet.Packet) {
    m := s.metrics
    pktLen := p.Len()

    m.CongestionSendPktBuf.Add(1)
    m.CongestionSendByteBuf.Add(uint64(pktLen))
    m.SendRateBytes.Add(pktLen)

    // Sequence gap detection (same as unbounded)
    // ... (existing gap detection code)

    // Insert into btree
    inserted, old := s.packetBtree.Insert(p)
    if !inserted && old != nil {
        m.SendBtreeDuplicates.Add(1)
        old.Decommission()
    }

    m.SendBtreeInserted.Add(1)
    m.SendRingDrained.Add(1)
}
```

### Phase 2: Add Configuration

**File**: `config.go`

```go
// EventLoop tight loop (Performance Optimization)
// When > 0, uses tight loop drain with control checks after every packet.
// When 0, uses legacy unbounded drain.
EventLoopMaxDataPerIteration int  // Max data packets per iteration (default: 512)
```

**File**: `contrib/common/flags.go`

```go
EventLoopMaxDataPerIteration = flag.Int("eventloopmaxdata", 512,
    "Max data packets per EventLoop iteration (0 = unbounded legacy mode)")
```

### Phase 3: Update EventLoop

**File**: `congestion/live/send/eventloop.go`

```go
// In EventLoop main loop, replace step 2:

// OLD:
// 2. Drain data ring → btree
dataDrained := s.drainRingToBtreeEventLoop()

// NEW:
// 2. Drain data ring → btree (tight loop with control interleave)
var dataDrained int
if s.maxDataPerIteration > 0 {
    dataDrained = s.drainRingToBtreeEventLoopTight()  // Tight loop
} else {
    dataDrained = s.drainRingToBtreeEventLoop()       // Legacy unbounded
}

// Note: Control is checked INSIDE drainRingToBtreeEventLoopTight,
// so we can remove some of the explicit control checks around it.
```

### Phase 4: Add Metrics

**File**: `metrics/metrics.go`

```go
// EventLoop tight loop metrics
SendEventLoopTightCapReached  atomic.Uint64  // Times MaxDataPerIteration was hit
SendEventLoopTightIterations  atomic.Uint64  // Total tight loop iterations
```

---

## Testing Strategy

### Unit Tests

```go
func TestDrainRingToBtreeEventLoopBatched_BatchSize(t *testing.T) {
    // Verify batches of exactly DataBatchSize
}

func TestDrainRingToBtreeEventLoopBatched_ControlInterleave(t *testing.T) {
    // Verify control is checked between batches
}

func TestDrainRingToBtreeEventLoopBatched_MaxCap(t *testing.T) {
    // Verify MaxDataPerIteration is respected
}
```

### Benchmark Tests

```go
func BenchmarkDrainRingBatched_vs_Unbounded(b *testing.B) {
    // Compare throughput: batched vs unbounded drain
    // Expected: <5% overhead from batching
}

func BenchmarkControlLatency_Batched(b *testing.B) {
    // Measure control packet latency with batching
    // Expected: p99 < 100µs
}
```

### Integration Tests

```go
func TestEventLoop_BatchedDrain_HighThroughput(t *testing.T) {
    // Push 350 Mb/s through batched EventLoop
    // Measure: throughput, control latency, ACK timing
}
```

### Performance Tests (with `performance` tool)

1. **Baseline**: Run with `eventloopdatabatchsize=0` (unbounded)
2. **Batched-64**: Run with default batching
3. **Batched-32**: Run with smaller batches
4. **Batched-128**: Run with larger batches

Compare: Max throughput, throughput efficiency, control ring depth

---

## Metrics & Observability

### New Prometheus Metrics

```
# Batching behavior
send_eventloop_batch_count_total           # Total batches processed
send_eventloop_batch_control_checks_total  # Control checks between batches
send_eventloop_batch_max_reached_total     # Times iteration cap was hit
send_eventloop_batch_avg_size              # Average batch size (gauge)

# Control latency (existing, but now more meaningful)
send_control_ring_depth                    # Current control ring depth
send_control_ring_max_depth                # High watermark
```

### Debug Logging

```go
s.log("sender:eventloop:batch", func() string {
    return fmt.Sprintf("batch=%d drained=%d control=%d",
        batchNum, batchDrained, controlProcessed)
})
```

---

## Expected Impact

### Throughput

| Metric | Current | Expected |
|--------|---------|----------|
| Max throughput | ~350 Mb/s | **400+ Mb/s** |
| Efficiency @ 350 Mb/s | 89% | **95%+** |

### Control Latency

| Metric | Current (Unbounded) | Batched (64) | Tight Loop |
|--------|---------------------|--------------|------------|
| Control processing gap | **1-2ms** | ~32µs | **~500ns** |
| ACK latency p99 | Variable | <1ms | **<100µs** |

### CPU Overhead

| Approach | Overhead |
|----------|----------|
| Unbounded drain | 0% (but high latency) |
| Batched (64) | ~0.001% |
| **Tight loop** | **~0.006%** (still negligible) |

The 6× overhead increase from batched to tight loop is irrelevant at 0.006%.

---

## Test Results (2026-01-18)

### Tight Loop Implementation

**Result: NO IMPROVEMENT** ❌

| Configuration | Max Throughput | Notes |
|---------------|----------------|-------|
| Before (unbounded drain) | ~345 Mb/s | Legacy mode |
| After (tight loop, maxdata=512) | ~345 Mb/s | Same performance |

### Analysis

The tight loop didn't improve throughput because **control latency was NOT the bottleneck**.

The bottleneck is elsewhere:
1. **Btree iteration** in `deliverReadyPacketsEventLoop()` - scans all packets
2. **Btree insert** cost - O(log n) per packet
3. **Something else** in the sender path

### Conclusion

Control-priority tight loop is implemented and works correctly, but the ~350 Mb/s ceiling is caused by something other than control latency. The "EventLoop Starvation" hypothesis may be wrong, or it's being caused by a different part of the EventLoop.

### Next Steps

1. **Profile `deliverReadyPacketsEventLoop()`** - this iterates the btree
2. **Consider batching delivery** - deliver in batches with control interleave
3. **Profile btree insert** - may be the hot path
4. **Look at lock-free ring overhead** - TryPop() cost at high frequency

---

## Open Questions

1. **Should we batch delivery too?** Currently `deliverReadyPacketsEventLoop()` iterates entire btree. At 350 Mb/s with 1s buffer, that's 30,000 packets to scan.

2. **Should batch sizes be rate-adaptive?** E.g., at 100 Mb/s use larger batches (less overhead), at 500 Mb/s use smaller batches (lower latency).

3. **Should we consider sharding the btree?** Multiple smaller btrees could be processed in parallel (future work).

---

## References

- [Performance Testing Implementation Log](performance_testing_implementation_log.md) - Jump-off point
- [Sender Lockfree Architecture](sender_lockfree_architecture.md) - EventLoop design
- [Completely Lockfree Receiver](completely_lockfree_receiver.md) - Receiver EventLoop
