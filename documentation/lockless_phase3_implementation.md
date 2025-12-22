# Lockless Design Phase 3: Lock-Free Ring Integration Implementation

**Status**: 📋 PLANNED
**Started**: -
**Completed**: -
**Design Document**: [`gosrt_lockless_design.md`](./gosrt_lockless_design.md) - Section 5, Section 12 Phase 3

---

## Overview

This document tracks the implementation progress of Phase 3 of the GoSRT Lockless Design. Phase 3 introduces a lock-free ring buffer per connection to eliminate lock contention between packet arrival (io_uring completion handler) and packet processing (Tick/event loop).

**Goal**: Eliminate lock contention between packet arrival and processing by decoupling producers (io_uring completions) from consumers (receiver processing).

**Key Changes**:
- Add lock-free ring buffer per connection using `github.com/randomizedcoder/go-lock-free-ring`
- Add `UsePacketRing` feature flag for gradual rollout
- Update receiver to use function dispatch pattern (ring vs. legacy locked path)
- Event loop drains ring buffer before processing

**Expected Duration**: 4-5 hours

**Reference Documents**:
- [`gosrt_lockless_design.md`](./gosrt_lockless_design.md) - Section 5 (Lock-Free Ring), Section 12.3 (Phase 3 Plan)
- [`integration_testing_profiling_design.md`](./integration_testing_profiling_design.md) - For performance validation

**Prerequisites**:
- ✅ Phase 1 (Rate Metrics Atomics) - COMPLETE
- ✅ Phase 2 (Zero-Copy Buffer Lifetime) - COMPLETE

---

## Implementation Checklist

### Step 1: Add Lock-Free Ring Dependency

**File**: `go.mod`

- [ ] Add `github.com/randomizedcoder/go-lock-free-ring` dependency

```bash
go get github.com/randomizedcoder/go-lock-free-ring
```

**Status**: ⏳ Pending

---

### Step 2: Add Configuration Options (`config.go`)

**File**: `config.go`

- [ ] Add `UsePacketRing bool` feature flag
- [ ] Add `PacketRingSize int` (default: 1024, power of 2)
- [ ] Add `PacketRingShards int` (default: 4, power of 2)
- [ ] Add `PacketRingMaxRetries int` (default: 10)
- [ ] Add `PacketRingBackoffDuration time.Duration` (default: 100µs)
- [ ] Add `PacketRingMaxBackoffs int` (default: 0 = unlimited)
- [ ] Add validation in `Validate()`

**New Config Fields**:

```go
// Lock-Free Ring Buffer (Phase 3: Lockless Design)
UsePacketRing             bool          `json:"use_packet_ring"`
PacketRingSize            int           `json:"packet_ring_size"`            // Default: 1024
PacketRingShards          int           `json:"packet_ring_shards"`          // Default: 4
PacketRingMaxRetries      int           `json:"packet_ring_max_retries"`     // Default: 10
PacketRingBackoffDuration time.Duration `json:"packet_ring_backoff_duration"` // Default: 100µs
PacketRingMaxBackoffs     int           `json:"packet_ring_max_backoffs"`    // Default: 0
```

**Status**: ⏳ Pending

---

### Step 3: Add CLI Flags (`contrib/common/flags.go`)

**File**: `contrib/common/flags.go`

- [ ] Add `-usepacketring` flag
- [ ] Add `-packetringsize` flag
- [ ] Add `-packetringshards` flag

**Status**: ⏳ Pending

---

### Step 4: Update Test Flags (`contrib/common/test_flags.sh`)

**File**: `contrib/common/test_flags.sh`

- [ ] Add `$USE_PACKET_RING` variable
- [ ] Add `$PACKET_RING_SIZE` variable
- [ ] Update pipeline configurations

**Status**: ⏳ Pending

---

### Step 5: Update Receiver Configuration (`congestion/live/receive.go`)

**File**: `congestion/live/receive.go`

- [ ] Add `UsePacketRing bool` to `ReceiveConfig`
- [ ] Add ring configuration fields to `ReceiveConfig`
- [ ] Import `ring "github.com/randomizedcoder/go-lock-free-ring"`
- [ ] Add `packetRing *ring.ShardedRing` field to `receiver` struct
- [ ] Add `writeConfig ring.WriteConfig` field to `receiver` struct
- [ ] Add `pushFn func(packet.Packet)` function dispatch field

**Status**: ⏳ Pending

---

### Step 6: Initialize Ring Buffer in `NewReceiver()`

**File**: `congestion/live/receive.go`

- [ ] Create ring buffer when `UsePacketRing=true`
- [ ] Set up `writeConfig` for backoff behavior
- [ ] Set up function dispatch (`pushFn`)

```go
// In NewReceiver():
if config.UsePacketRing {
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

    r.pushFn = r.pushToRing  // NEW path
} else {
    r.pushFn = r.pushWithLock  // LEGACY path
}
```

**Status**: ⏳ Pending

---

### Step 7: Implement Function Dispatch for Push()

**File**: `congestion/live/receive.go`

- [ ] Refactor `Push()` to use function dispatch
- [ ] Implement `pushToRing()` - writes to ring buffer
- [ ] Implement `pushWithLock()` - legacy locked path

```go
// Push dispatches to configured implementation
func (r *receiver) Push(p packet.Packet) {
    // Rate metrics (always atomic - Phase 1)
    r.metrics.RecvLightACKCounter.Add(1)
    r.metrics.RecvRatePackets.Add(1)
    r.metrics.RecvRateBytes.Add(uint64(p.Len()))

    // Dispatch to configured implementation
    r.pushFn(p)
}

// pushToRing - NEW path (UsePacketRing=true)
func (r *receiver) pushToRing(p packet.Packet) {
    producerID := uint64(p.Header().PacketSequenceNumber.Val())

    if !r.packetRing.WriteWithBackoff(producerID, p, r.writeConfig) {
        r.metrics.RingDropsTotal.Add(1)
        r.releasePacketFully(p)
    }
}

// pushWithLock - LEGACY path (UsePacketRing=false)
func (r *receiver) pushWithLock(p packet.Packet) {
    r.lock.Lock()
    defer r.lock.Unlock()
    // ... existing locked insert logic ...
}
```

**Status**: ⏳ Pending

---

### Step 8: Implement Ring Drain in Tick()

**File**: `congestion/live/receive.go`

- [ ] Add `drainPacketRing()` method
- [ ] Update `Tick()` to drain ring before processing
- [ ] Add no-lock versions of periodicACK, periodicNAK, deliverPackets

```go
// Tick dispatches to configured implementation
func (r *receiver) Tick(now time.Time) {
    if r.config.UsePacketRing {
        r.drainPacketRing()
        // No locks needed - we own the btrees after drain
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
// NOTE: TryRead() is NON-BLOCKING - returns (nil, false) when ring is empty
// This means the loop terminates immediately when there are no more packets
func (r *receiver) drainPacketRing() {
    for {
        item, ok := r.packetRing.TryRead()
        if !ok {
            // Ring is empty - exit loop and proceed with ACK/NAK/delivery
            return
        }

        p := item.(packet.Packet)
        seq := p.Header().PacketSequenceNumber

        // Duplicate/old packet check
        if r.packetStore.Has(seq) || seq.Lt(r.deliveryBase) {
            r.releasePacketFully(p)
            continue
        }

        // Insert into btree (NO LOCK - exclusive access)
        r.packetStore.Insert(p)

        // Delete from NAK btree (NO LOCK)
        if r.nakBtree != nil {
            r.nakBtree.Delete(seq)
        }
    }
}
```

**Status**: ⏳ Pending

---

### Step 9: Add Ring Metrics

**File**: `metrics/metrics.go`

- [ ] Add `RingDropsTotal atomic.Uint64` to `ConnectionMetrics`

**File**: `metrics/handler.go`

- [ ] Export `gosrt_ring_drops_total` counter

**Where counters are incremented**:

The `RingDropsTotal` counter is incremented in `pushToRing()` (Step 7) when a write fails:

```go
// In pushToRing() - from Step 7:
func (r *receiver) pushToRing(p packet.Packet) {
    producerID := uint64(p.Header().PacketSequenceNumber.Val())

    if !r.packetRing.WriteWithBackoff(producerID, p, r.writeConfig) {
        // Ring write failed (ring full after all backoff retries)
        r.metrics.RingDropsTotal.Add(1)  // <-- INCREMENT HERE
        r.releasePacketFully(p)
    }
}
```

**Note**: The `go-lock-free-ring` library handles backoff internally. If we want to track backoff events, we'd need to either:
1. Check if the library exposes backoff stats, or
2. Wrap the write with our own retry loop and track manually

For Phase 3, just tracking drops is sufficient - it tells us if the ring is ever overflowing.

**Status**: ⏳ Pending

---

### Step 10: Add Unit Tests

**File**: `congestion/live/receive_test.go`

- [ ] `TestPushToRing` - verify packets written to ring
- [ ] `TestDrainPacketRing` - verify packets consumed correctly
- [ ] `TestRingBackoff` - verify backoff behavior on full ring
- [ ] `TestFunctionDispatch` - verify correct path selection

**Status**: ⏳ Pending

---

### Step 11: Integration Testing

**Validation Checkpoint**:

```bash
# 1. Unit tests with race detector
go test -race -v ./...

# 2. Legacy path (ring disabled) - must still work
sudo make test-isolation CONFIG=Isolation-5M-Control

# 3. New path (ring enabled)
sudo USE_PACKET_RING=true make test-isolation CONFIG=Isolation-5M-Full

# 4. Parallel comparison: legacy vs ring under IDENTICAL network
sudo make test-parallel CONFIG=Parallel-Starlink-5M-Base-vs-Ring

# 5. CPU profile to verify lock reduction
sudo PROFILES=cpu,mutex make test-parallel CONFIG=Parallel-Starlink-5Mbps
# Check: runtime.futex should be reduced
```

**Acceptance Criteria**:
- [ ] All unit tests pass with race detector
- [ ] All integration tests pass with `UsePacketRing=false`
- [ ] All integration tests pass with `UsePacketRing=true`
- [ ] Parallel comparison: packet delivery identical
- [ ] No ring drops under normal load (`gosrt_ring_drops_total = 0`)
- [ ] `runtime.futex` CPU reduced in profile

**Status**: ⏳ Pending

---

## Progress Log

| Date | Step | Action | Status |
|------|------|--------|--------|
| 2025-12-22 | - | Phase 3 plan created | 📋 |

---

## Risk Assessment

| Risk | Mitigation |
|------|------------|
| Ring overflow under burst | Use `WriteWithBackoff()` with configurable retries |
| Memory pressure from ring | Ring size configurable, uses existing pooled buffers |
| Regression in legacy path | Function dispatch ensures legacy path unchanged |
| Lock semantics change | Comprehensive testing with both paths |

---

## Performance Expectations

| Metric | Before (Phase 2) | Expected (Phase 3) |
|--------|------------------|-------------------|
| `runtime.futex` CPU | 44% | < 20% |
| Push() lock contention | High | Zero (ring writes are lock-free) |
| Tick() lock contention | High | Zero (exclusive access after drain) |
| Memory allocations | Same | Same (uses existing pools) |

---

## Architecture Overview

```
                    Phase 3 Architecture

io_uring Completion          Lock-Free Ring           Tick() / Event Loop
   (Producer)                    Buffer                  (Consumer)
       │                           │                         │
       ├── WriteWithBackoff() ────►│                         │
       ├── WriteWithBackoff() ────►│◄──── TryRead() ────────┤
       ├── WriteWithBackoff() ────►│                         │
       │                           │                         │
       │                           │                         ▼
       │                           │                ┌─────────────────────┐
       │                           │                │ drainPacketRing()   │
       │                           │                │ • TryRead() packets │
       │                           │                │ • Insert btree      │
       │                           │                │ • Delete NAK btree  │
       │                           │                │ (NO LOCKS - single  │
       │                           │                │  threaded access)   │
       │                           │                └─────────────────────┘
```

**Key Properties**:
- Multiple io_uring completions can write concurrently (MPSC pattern)
- Single Tick() goroutine reads exclusively (single consumer)
- No locks needed for btree operations - Tick() has exclusive access after drain
- Lock-free ring uses atomic operations internally
- `TryRead()` is **non-blocking** - returns immediately when ring is empty
- `WriteWithBackoff()` retries with configurable backoff if ring is full

