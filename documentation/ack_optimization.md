# ACK Optimization: Inline ACK and ACK Modulus

**Status**: PROPOSED
**Date**: 2025-12-24
**Related Documents**:
- [`gosrt_lockless_design.md`](./gosrt_lockless_design.md) - Lockless design overview
- [`lockless_phase4_implementation.md`](./lockless_phase4_implementation.md) - Phase 4 implementation (ACK timing issue identified)
- [`lockless_phase5_implementation.md`](./lockless_phase5_implementation.md) - Phase 5 integration testing

---

## Table of Contents

1. [Motivation](#1-motivation)
2. [Current Architecture Review](#2-current-architecture-review)
   - [2.1 How periodicACK() Works](#21-how-periodicack-works)
   - [2.2 Benchmark Analysis at 100 Mb/s](#22-benchmark-analysis-at-100-mbs)
   - [2.3 Proposal: Continuous periodicACKNoLock (No Timer)](#23-proposal-continuous-periodicacknolock-no-timer)
3. [Inline ACK State: Avoiding Atomics](#3-inline-ack-state-avoiding-atomics)
4. [Lazy ACK: ACK on Sequential Packet Arrival](#4-lazy-ack-ack-on-sequential-packet-arrival)
5. [ACK Modulus: Reducing ACK Frequency](#5-ack-modulus-reducing-ack-frequency)
6. [Configuration Examples](#6-configuration-examples)
7. [Implementation Strategy](#7-implementation-strategy)
8. [Expected Performance Impact](#8-expected-performance-impact)
9. [Interaction with Existing Features](#9-interaction-with-existing-features)
10. [Code Locations Summary](#10-code-locations-summary)
11. [Open Questions](#11-open-questions)

---

## 1. Motivation

The current `periodicACK()` function in `congestion/live/receive.go` is already quite efficient due to the **ACK Scan High Water Mark** optimization (see Section 26 of `integration_testing_50mbps_defect.md`). It remembers where it last verified contiguous packets and only scans forward from that point, achieving a ~96.7% reduction in iterations at steady state.

However, within the event loop architecture, we have an opportunity to go further:

**Key Insight**: When `processOnePacket()` reads a packet from the ring, it already knows the packet's sequence number. If that packet is the **next expected sequence number**, we can update our ACK state inline—without any btree iteration at all.

This document explores three related optimizations:

1. **Inline ACK State Tracking**: Keep ACK state as stack variables within the event loop
2. **Lazy ACK**: ACK immediately when receiving the next sequential packet
3. **ACK Modulus**: Reduce ACK frequency by only ACKing every Nth in-sequence packet

### Problem: RTT Increase in EventLoop Mode

As documented in [`lockless_phase4_implementation.md`](./lockless_phase4_implementation.md#root-cause-identified-ack-timing-difference), the EventLoop mode shows ~10ms RTT vs ~0.08ms in legacy Tick mode. This is because:

- **Legacy mode**: Lite ACK sent immediately on sequential packet arrival
- **EventLoop mode**: ACKs batched on 10ms ticker interval

The inline ACK optimization addresses this by detecting sequential arrivals and sending ACKs inline.

---

## 2. Current Architecture Review

**File**: `congestion/live/receive.go`

The current event loop architecture (see `gosrt_lockless_design.md` Section 5.6.3) processes packets as follows:

```go
// EventLoop - lines 1784-1882 in congestion/live/receive.go
func (r *receiver) EventLoop(ctx context.Context) {
    // ...
    for {
        select {
        case <-ctx.Done():
            return
        case <-ackTicker.C:
            r.drainRingByDelta()
            now := uint64(time.Now().UnixMicro())
            if ok, seq, lite := r.periodicACK(now); ok {
                r.sendACK(seq, lite)
            }
        // ... NAK ticker, rate ticker ...
        default:
            processed := r.processOnePacket()
            delivered := r.deliverReadyPacketsNoLock(now)
            // ... adaptive backoff ...
        }
    }
}
```

The `periodicACK()` function (lines 758-922) uses these key fields:

| Field | Type | Purpose |
|-------|------|---------|
| `r.lastACKSequenceNumber` | `circular.Number` | Last ACKed sequence number |
| `r.ackScanHighWaterMark` | `circular.Number` | Progress marker for btree scan |
| `r.lastPeriodicACK` | `uint64` | Timestamp of last ACK (for interval check) |
| `r.periodicACKInterval` | `uint64` | Minimum interval between ACKs (µs) |

### 2.1 How `periodicACK()` Works

**File**: `congestion/live/receive.go` lines 758-922

The `periodicACK()` function scans the packet btree to find the highest contiguous sequence number that can be acknowledged. It uses several optimizations to minimize work:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         periodicACK() Flow                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. EARLY EXIT CHECK (interval + lite ACK threshold)                         │
│     ├─ If now - lastPeriodicACK < periodicACKInterval (10ms):                │
│     │   ├─ If RecvLightACKCounter >= 64 → send Lite ACK                      │
│     │   └─ Else → return early (no ACK needed)                               │
│     └─ Else → continue to scan                                               │
│                                                                              │
│  2. DETERMINE SCAN START POINT (High Water Mark optimization)                │
│     ├─ scanStartPoint = ackScanHighWaterMark (where we last verified)        │
│     ├─ Handle edge cases:                                                    │
│     │   ├─ Not initialized → start from lastACKSequenceNumber                │
│     │   ├─ Behind lastACK → start from lastACKSequenceNumber                 │
│     │   └─ Behind minPkt (packets delivered) → start from minPkt             │
│     └─ This avoids re-scanning already verified packets                      │
│                                                                              │
│  3. ITERATE FROM scanStartPoint (O(log n) seek + O(k) scan)                  │
│     ├─ IterateFrom(scanStartPoint) → O(log n) btree seek                     │
│     ├─ For each packet:                                                      │
│     │   ├─ Skip if seq <= scanStartPoint                                     │
│     │   ├─ Check gap at first packet (critical for correctness)              │
│     │   ├─ If TSBPD expired → skip (count as lost)                           │
│     │   ├─ If seq == lastACK + 1 → advance lastACK, continue                 │
│     │   └─ Else → gap found, stop iteration                                  │
│     └─ Update lastContiguousSeq for high water mark                          │
│                                                                              │
│  4. UPDATE STATE (brief write lock)                                          │
│     ├─ Update lastACKSequenceNumber                                          │
│     ├─ Update ackScanHighWaterMark = lastContiguousSeq                       │
│     ├─ Update lastPeriodicACK = now                                          │
│     ├─ Reset RecvLightACKCounter = 0                                         │
│     └─ Calculate msBuf for congestion control                                │
│                                                                              │
│  5. RETURN (ok=true, sequenceNumber, lite)                                   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Key optimizations**:
- **ACK Scan High Water Mark**: Only scans NEW packets since last call (~96.7% reduction in iterations)
- **O(log n) seek**: `IterateFrom()` uses btree's `AscendGreaterOrEqual()` for fast seek
- **Early exit**: Returns immediately if interval hasn't elapsed and <64 packets received
- **Batched metrics**: Updates counters once after iteration, not per-packet

### 2.2 Benchmark Analysis at 100 Mb/s

At 100 Mb/s with ~1400 byte packets:

| Parameter | Value | Calculation |
|-----------|-------|-------------|
| Packet rate | ~8,928 pkt/s | 100,000,000 / (1400 × 8) |
| Packets per ACK interval (10ms) | ~89 packets | 8,928 × 0.010 |
| ACK calls per second (timer) | 100 | 1000ms / 10ms |
| ACK calls per second (continuous) | ~8,928 | 1 per packet |

#### Timer-Based (Current): ~89 packets per call

| Operation | Complexity | Estimated Time | Notes |
|-----------|------------|----------------|-------|
| Early exit check | O(1) | ~10-50ns | Atomic load + comparison |
| Get Min packet | O(1) | ~20ns | Btree Min() is cached |
| IterateFrom seek | O(log n) | ~100-200ns | n = buffer size (~100-1000 pkts) |
| Iteration (k packets) | O(k) | ~50ns × k | k ≈ 89 new packets |
| State update (write lock) | O(1) | ~100-500ns | Brief critical section |
| **Total (timer-based)** | **O(log n + k)** | **~5-10µs** | k ≈ 89 at 100Mb/s |

**Timer-based totals**:
- 100 calls/sec × ~5-10µs = ~500-1000µs ACK work per second (~0.05-0.1% CPU)

#### Continuous (Proposed): `periodicACKNoLock()` after every packet

In EventLoop mode, we already use lock-free variants (`*NoLock()` functions) because the event loop is the single consumer of the btree. The proposed change calls `periodicACKNoLock()` after every `processOnePacket()`:

```go
// EventLoop - proposed modification
func (r *receiver) EventLoop(ctx context.Context) {
    // ...
    for {
        select {
        case <-ctx.Done():
            return
        // case <-ackTicker.C:  // REMOVED - no longer needed
        case <-nakTicker.C:
            // NAK still needs timer (gap detection requires time)
            // ...
        default:
            processed := r.processOnePacket()
            delivered := r.deliverReadyPacketsNoLock(now)

            // Run ACK check after every packet (no locks, no timer)
            if processed {
                now := uint64(time.Now().UnixMicro())
                if ok, seq, lite := r.periodicACKNoLock(now); ok {
                    r.sendACK(seq, lite)
                }
            }
            // ... adaptive backoff ...
        }
    }
}
```

**`periodicACKNoLock()` cost breakdown** (no early exit check, no locks):

| Operation | Complexity | Estimated Time | Notes |
|-----------|------------|----------------|-------|
| Determine scan start | O(1) | ~10-20ns | Read `ackScanHighWaterMark`, compare |
| Get Min packet | O(1) | ~20ns | Btree Min() is cached |
| IterateFrom seek | O(log n) | ~100-200ns | n = buffer size (~100-1000 pkts) |
| Iteration (k=1 packet) | O(1) | ~50ns | Only check the new packet |
| State update | O(1) | ~50-100ns | Update 3-4 fields |
| **Total** | **O(log n)** | **~250-400ns** | Dominated by btree seek |

**No lock overhead**: EventLoop already uses `*NoLock()` variants because it's the single btree consumer.

#### Comparison Summary

| Mode | Calls/sec | Time/call | Packets scanned | Total CPU |
|------|-----------|-----------|-----------------|-----------|
| Timer (10ms) | 100 | ~5-10µs | ~89 | ~0.05-0.1% |
| **Continuous** | **8,928** | **~250-400ns** | **1** | **~0.22-0.36%** |

**Key insight**: Even with ~90x more calls, continuous mode only uses ~3-7x more CPU because each call does ~15-25x less work (scanning 1 packet vs 89).

#### Why the btree seek dominates

The `IterateFrom(scanStartPoint)` operation uses `btree.AscendGreaterOrEqual()` which is O(log n). For a buffer of ~1000 packets, this is ~10 comparisons. This dominates the per-call cost.

However, this seek finds exactly the right position to check if the new packet is contiguous - it's the minimum work needed to make progress.

---

### 2.3 Why This Works

The proposal in Section 2.2 works because of the High Water Mark optimization:

1. **High Water Mark makes it cheap**: After `processOnePacket()` inserts packet N, `periodicACKNoLock()` starts scanning from `ackScanHighWaterMark` (N-1 or earlier). It only needs to check if packet N is contiguous with N-1.

2. **O(log n) dominated**: The btree seek is the main cost (~100-200ns), but this is unavoidable - we need to find the right position.

3. **Graceful degradation**: When packets arrive out of order:
   - Gap detected immediately → iteration stops
   - No wasted work scanning past the gap
   - ACK reflects actual contiguous range

4. **Lower RTT**: ACK sent immediately when contiguous packet arrives, not waiting for 10ms timer.

**Trade-offs**:

| Continuous `periodicACKNoLock()` | Timer-based periodicACK |
|----------------------------------|-------------------------|
| ✅ Near-instant ACK feedback | ✅ Lower CPU overhead (~0.05-0.1%) |
| ✅ Accurate RTT measurement | ✅ Fewer ACK packets |
| ✅ Better congestion control response | ✅ Simpler mental model |
| ❌ More ACK packets (~89x more) | ❌ 5ms average ACK delay |
| ❌ Higher CPU (~0.22-0.36%) | ❌ Inaccurate RTT in EventLoop |

**ACK packet reduction with ACKModulus**: Use `ACKModulus` (Section 5) to reduce ACK frequency while still keeping ACK state up-to-date:

```go
// In EventLoop default case:
if processed {
    now := uint64(time.Now().UnixMicro())
    if ok, seq, lite := r.periodicACKNoLock(now); ok {
        // Scan runs every time (ACK state always current)
        // But only SEND ACK every N packets
        consecutiveInSeq++
        if consecutiveInSeq % ackModulus == 0 || lite {
            r.sendACK(seq, lite)
        }
    }
}
```

This gives the best of both worlds:
- ACK state always up-to-date (scan runs continuously)
- Reduced ACK packet overhead (send every Nth)
- Configurable trade-off via `ACKModulus`

---

## 3. Inline ACK State: Avoiding Atomics

**Proposal**: In the event loop, maintain the ACK state as **stack variables** instead of receiver struct fields. Since the event loop is the single consumer (exclusive btree access), no atomics or locks are needed for these variables.

**File**: `congestion/live/receive.go` - `EventLoop()` function

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // Stack-local ACK state - no atomics needed (single consumer)
    localLastACKSeq := r.lastACKSequenceNumber       // Initialize from struct
    localHighWaterMark := r.ackScanHighWaterMark     // Initialize from struct
    localLastACKTime := uint64(0)                    // Track last ACK time locally

    // Track consecutive in-sequence packets for lazy ACK
    consecutiveInSeq := uint64(0)

    ackTicker := time.NewTicker(ackInterval)
    nakTicker := time.NewTicker(nakInterval)
    // ... offset tickers ...

    for {
        select {
        case <-ctx.Done():
            // Sync local state back to struct on exit (for graceful shutdown)
            r.lastACKSequenceNumber = localLastACKSeq
            r.ackScanHighWaterMark = localHighWaterMark
            return

        case <-ackTicker.C:
            // Timer-driven ACK still needed for:
            // 1. Keepalive when no packets arriving
            // 2. Buffer time (msBuf) calculation
            // 3. Catching up when out-of-order arrivals prevent inline ACK
            r.drainRingByDelta()
            now := uint64(time.Now().UnixMicro())
            if ok, seq, lite := r.periodicACKWithState(now, &localLastACKSeq,
                                                        &localHighWaterMark); ok {
                r.sendACK(seq, lite)
                localLastACKTime = now
            }

        default:
            // Process one packet - may trigger inline ACK
            processed, ackSeq := r.processOnePacketWithACK(&localLastACKSeq,
                                                           &consecutiveInSeq)
            if ackSeq.Val() > 0 {
                // Inline ACK triggered (see Section 4 and 5)
                now := uint64(time.Now().UnixMicro())
                r.sendACK(ackSeq.Inc(), false)  // Full ACK, not lite
                localLastACKTime = now
            }
            // ... delivery, backoff ...
        }
    }
}
```

**Benefits**:
- No atomic operations for ACK state updates
- No lock contention (already eliminated by event loop architecture)
- Cache-friendly: stack variables stay in CPU registers/L1

**Trade-offs**:
- State must be synced back to struct on shutdown (for statistics)
- Struct fields become "stale" during event loop operation (acceptable—event loop owns them)

---

## 4. Lazy ACK: ACK on Sequential Packet Arrival

**Proposal**: When `processOnePacket()` reads a packet from the ring and its sequence number is exactly `lastACKSeq + 1`, we know immediately that:

1. This packet extends the contiguous range
2. We can advance `lastACKSeq` without scanning the btree
3. We *could* send an ACK right now

**File**: `congestion/live/receive.go` - new function

```go
// processOnePacketWithACK processes a packet and optionally returns an ACK sequence.
// If the packet is the next expected sequence number, updates local ACK state inline.
// Returns (processed bool, ackSeq circular.Number) - ackSeq.Val() > 0 means send ACK.
func (r *receiver) processOnePacketWithACK(localLastACKSeq *circular.Number,
                                            consecutiveInSeq *uint64) (bool, circular.Number) {
    if r.packetRing == nil {
        return false, circular.Number{}
    }

    item, ok := r.packetRing.TryRead()
    if !ok {
        return false, circular.Number{}  // Ring empty
    }

    p := item.(packet.Packet)
    h := p.Header()
    seq := h.PacketSequenceNumber

    // Track ring consumption (unchanged from processOnePacket)
    if r.metrics != nil {
        r.metrics.RingPacketsProcessed.Add(1)
    }

    // Duplicate/old packet check (unchanged)
    if seq.Lte(r.lastDeliveredSequenceNumber) {
        // ... handle too-old packet ...
        r.releasePacketFully(p)
        return true, circular.Number{}
    }

    // Check if already in btree (duplicate)
    if r.packetStore.Has(seq) {
        r.releasePacketFully(p)
        return true, circular.Number{}
    }

    // Insert into packet btree (NO LOCK - event loop owns btrees)
    r.packetStore.Insert(p)

    // Delete from NAK btree (NO LOCK)
    if r.nakBtree != nil {
        r.nakBtree.Delete(seq)
    }

    // === LAZY ACK LOGIC ===
    // Check if this packet is the next expected sequence
    expectedNext := localLastACKSeq.Inc()
    if seq.Equals(expectedNext) {
        // This packet extends the contiguous range!
        *localLastACKSeq = seq
        *consecutiveInSeq++

        // Return the ACK sequence for potential sending
        // (Caller decides based on ACK Modulus - see Section 5)
        return true, seq
    } else {
        // Out of order - reset consecutive counter
        *consecutiveInSeq = 0
        return true, circular.Number{}
    }
}
```

**Key Insight**: This is O(1) ACK state update—no btree iteration needed when packets arrive in order. The existing `periodicACK()` still runs on its timer to:
1. Handle out-of-order arrivals (scan btree to find actual contiguous range)
2. Calculate buffer time (msBuf) for congestion control
3. Send keepalive ACKs when no packets arriving
4. Send lite ACKs when `RecvLightACKCounter >= 64`

---

## 5. ACK Modulus: Reducing ACK Frequency

**Proposal**: Instead of potentially sending an ACK for every in-sequence packet (which would overwhelm the sender), introduce an `ACKModulus` configuration option. The inline ACK is only sent every Nth consecutive in-sequence packet.

**File**: `congestion/live/receive.go` - `ReceiveConfig` struct

```go
type ReceiveConfig struct {
    // ... existing fields ...

    // ACK Modulus configuration (Phase 5: Inline ACK optimization)
    // When > 0, inline ACKs are only sent every ACKModulus consecutive in-sequence packets.
    // Example: ACKModulus=10 means send ACK after 10, 20, 30... consecutive packets.
    // Default: 0 (disabled - use timer-driven ACKs only)
    ACKModulus uint64

    // ... existing fields ...
}
```

**File**: `congestion/live/receive.go` - event loop modification

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // ... local state setup ...
    ackModulus := r.config.ACKModulus  // Copy to stack (avoid struct access in hot path)

    for {
        select {
        // ... tickers ...

        default:
            processed, ackSeq := r.processOnePacketWithACK(&localLastACKSeq,
                                                           &consecutiveInSeq)

            // ACK Modulus check: only send ACK every N consecutive packets
            if ackSeq.Val() > 0 && ackModulus > 0 {
                if *consecutiveInSeq % ackModulus == 0 {
                    // Send ACK now
                    now := uint64(time.Now().UnixMicro())
                    r.sendACK(ackSeq.Inc(), false)
                    localLastACKTime = now
                }
                // else: skip this ACK, wait for modulus
            }

            // ... delivery, backoff ...
        }
    }
}
```

---

## 6. Configuration Examples

| ACKModulus | Behavior | Use Case |
|------------|----------|----------|
| 0 (default) | Timer-driven ACKs only (current behavior) | Backwards compatibility |
| 1 | ACK every in-sequence packet | Aggressive ACK, maximum responsiveness |
| 10 | ACK every 10th consecutive packet | Balanced: ~10x fewer ACKs |
| 64 | ACK every 64th consecutive packet | Match lite ACK threshold |
| 100 | ACK every 100th consecutive packet | Very low ACK overhead |

**Trade-off Analysis**:

| Lower ACKModulus | Higher ACKModulus |
|------------------|-------------------|
| ✅ Faster sender feedback | ✅ Fewer ACK packets |
| ✅ Better loss detection | ✅ Lower CPU/network overhead |
| ❌ More ACK packets | ❌ Delayed sender feedback |
| ❌ Higher CPU/network overhead | ❌ Slower congestion response |

---

## 7. Implementation Strategy

**Phase 5A**: Inline ACK State (Low Risk)
1. Move ACK state to stack variables in `EventLoop()`
2. Add `periodicACKWithState()` variant that uses passed-in state
3. Sync state on shutdown
4. **Validation**: Unit tests, race detector

**Phase 5B**: Lazy ACK (Medium Risk)
1. Add `processOnePacketWithACK()` function
2. Track `consecutiveInSeq` counter
3. Initially set `ACKModulus=0` (disabled by default)
4. **Validation**: Integration tests with in-order delivery

**Phase 5C**: ACK Modulus Tuning (Low Risk)
1. Add `ACKModulus` to `ReceiveConfig`
2. Expose via connection config
3. Benchmark with different values
4. **Validation**: Performance tests, A/B testing in staging

---

## 8. Expected Performance Impact

| Metric | Current (Timer ACK) | With ACKModulus=10 | With ACKModulus=64 |
|--------|---------------------|--------------------|--------------------|
| ACK packets/sec (at 50Mb/s) | ~100 (10ms interval) | ~400 (inline + timer) | ~65 (inline + timer) |
| Btree iterations per ACK | O(n) scan | O(1) for inline | O(1) for inline |
| Atomic operations per ACK | 3-5 | 0 (stack vars) | 0 (stack vars) |
| Sender feedback latency | 10ms avg | 0.25ms avg | 1.5ms avg |

**Notes**:
- At 50 Mb/s with ~1400 byte packets, we receive ~4400 packets/sec
- With `ACKModulus=10`, inline ACKs trigger every ~2.3ms
- Timer-driven ACKs still run for keepalive and msBuf calculation
- The O(1) inline ACK avoids the btree scan entirely for in-sequence packets

---

## 9. Interaction with Existing Features

| Feature | Interaction | Notes |
|---------|-------------|-------|
| **Lite ACK** | Independent | Lite ACK uses `RecvLightACKCounter >= 64` threshold, inline ACK uses modulus |
| **ACK Scan High Water Mark** | Complementary | Timer-driven `periodicACK()` still uses high water mark for out-of-order cases |
| **FastNAK** | Independent | FastNAK detects gaps after silent periods, unaffected by inline ACK |
| **NAK btree** | No change | `processOnePacketWithACK()` still deletes from NAK btree |
| **UsePacketRing** | Required | Inline ACK only works with event loop (requires `UsePacketRing=true`) |

---

## 10. Code Locations Summary

| File | Function | Purpose |
|------|----------|---------|
| `congestion/live/receive.go` | `ReceiveConfig` (lines 58-96) | Add `ACKModulus` field |
| `congestion/live/receive.go` | `EventLoop()` (lines 1784-1882) | Add inline ACK state, modulus check |
| `congestion/live/receive.go` | `processOnePacketWithACK()` (new) | Inline ACK detection |
| `congestion/live/receive.go` | `periodicACKWithState()` (new) | State-parameterized ACK |
| `congestion/live/receive.go` | `periodicACK()` (lines 758-922) | Unchanged (fallback) |
| `connection.go` | `ticker()` (lines 588-615) | No change needed |

---

## 11. Open Questions

1. **Should inline ACK bypass msBuf calculation?**
   - Pro: Faster ACK path
   - Con: Sender loses buffer occupancy feedback
   - Proposal: Inline ACK sends `msBuf=0`, timer ACK calculates actual value

2. **What's the optimal ACKModulus for different bitrates?**
   - Lower bitrates may benefit from lower modulus (faster feedback)
   - Higher bitrates may benefit from higher modulus (reduce overhead)
   - Proposal: Adaptive modulus based on receive rate?

3. **Should we count only contiguous packets or all packets?**
   - Current proposal: Only contiguous (reset counter on gap)
   - Alternative: Any packet increments counter
   - Proposal: Start with contiguous-only (simpler reasoning)

4. **Continuous `periodicACKNoLock()` vs Inline ACK: Which approach?**
   - **Continuous `periodicACKNoLock()`** (Section 2.2-2.3): Call after every `processOnePacket()`
     - Pro: Reuses existing, well-tested code
     - Pro: Handles all edge cases (gaps, TSBPD expiry, lite ACK threshold)
     - Con: ~250-400ns overhead per packet (~0.22-0.36% CPU at 100Mb/s)
   - **Inline ACK** (Section 4): Custom `processOnePacketWithACK()` with O(1) check
     - Pro: Potentially faster (~50ns vs ~300ns)
     - Pro: Avoids btree seek entirely for sequential packets
     - Con: New code path, must handle edge cases correctly
     - Con: Doesn't update msBuf (needs separate timer for that)
   - **Recommendation**: Start with Continuous `periodicACKNoLock()` (simpler, safer), measure performance, then consider Inline ACK if needed

5. **Should we remove `ackTicker` entirely or keep it as backup?**
   - **Remove**: Simpler code, no timer overhead
   - **Keep as backup**: Ensures ACKs are sent even during processing stalls
   - Proposal: Keep timer but with longer interval (100ms?) as keepalive/fallback

6. **What about Lite ACK behavior with continuous periodicACK?**
   - Currently: Lite ACK sent when `RecvLightACKCounter >= 64` AND interval not elapsed
   - With continuous: Interval check becomes less meaningful
   - Options:
     - Always send full ACK (more data for sender, higher overhead)
     - Send Lite ACK for every modulus, Full ACK every N modulus
     - Keep `RecvLightACKCounter` logic but reset on every ACK sent

