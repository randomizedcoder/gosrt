# ACK Optimization Implementation Plan

**Status**: READY FOR IMPLEMENTATION
**Date**: 2025-12-24
**Related Documents**:
- [`ack_optimization.md`](./ack_optimization.md) - Design exploration and benchmarks
- [`lockless_phase4_implementation.md`](./lockless_phase4_implementation.md) - RTT increase root cause
- [`integration_testing_matrix_design.md`](./integration_testing_matrix_design.md) - Test matrix structure
- [SRT RFC Section 3.2.4](https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-3.2.4) - ACK packet types

---

## 1. Background: SRT ACK Types (RFC Section 3.2.4)

The SRT specification defines **three types** of ACK packets:

| ACK Type | Content | Type-Specific Field | When Sent |
|----------|---------|---------------------|-----------|
| **Full ACK** | All fields (seq, RTT, buffer, stats) | ACK sequence number | Every 10ms |
| **Light ACK** | Only Last Acknowledged Packet Sequence Number | 0 | Every 64 packets (recommended) |
| **Small ACK** | Fields up to Available Buffer Size | 0 | High data rates |

**From the RFC**:
> "The Light ACK and Small ACK packets are used in cases when the receiver should acknowledge received data packets more often than every 10 ms. This is usually needed at high data rates. It is up to the receiver to decide the condition and the type of ACK packet to send (Light or Small). **The recommendation is to send a Light ACK for every 64 packets received.**"

**Key insight**: The RFC explicitly recommends Light ACK every 64 packets for high data rates.

---

## 2. Problem Statement

### RTT Increased from ~0.08ms to ~10ms in EventLoop Mode

When EventLoop mode was introduced (Phase 4), ACKs are only sent when the `ackTicker` fires (every 10ms). This results in:

- **RTT increased** from ~0.08ms (legacy) to ~10ms (EventLoop)
- **Sender feedback delayed** - congestion control responds slowly
- **Inaccurate RTT measurement** - RTT reflects ticker interval, not network latency

**Root cause**: In EventLoop mode, ACKs are batched on a timer instead of being sent when sequential packets arrive.

**See**: [`lockless_phase4_implementation.md` Section "Root Cause Identified: ACK Timing Difference"](./lockless_phase4_implementation.md#root-cause-identified-ack-timing-difference)

---

## 3. Solution Overview

**Core idea**: Remove the `ackTicker` and run ACK scanning continuously in the event loop. The scan is optimized (~250-400ns) but **Light ACKs are only SENT every 64 packets** per RFC recommendation.

**Two separate concerns**:
1. **Scanning** - Track contiguous sequence progress (runs every iteration)
2. **Sending** - Generate ACK packet (every 64 packets for Light, every 10ms for Full)

**Key changes**:
1. Rename scan functions: `periodicACK()` → `contiguousScan()`, `periodicNAK()` → `gapScan()`
2. Remove `ackTicker.C` case from EventLoop
3. Call `contiguousScan()` after `deliverReadyPackets()` in the `default:` case
4. **Send Light ACK every 64 packets** (RFC recommendation, using existing counter)
5. **Send Full ACK every 10ms** (unchanged, for RTT measurement)
6. **Unified `contiguousPoint`** - Single atomic tracking variable for both scans

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Continuous Scan + RFC ACK Timing                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  EventLoop iteration:                                                       │
│                                                                             │
│  1. deliverReadyPackets()      → deliver TSBPD-ready packets               │
│  2. processOnePacket()         → insert packet into btree                  │
│  3. contiguousScan()           → update contiguousPoint (ALWAYS runs)      │
│                                                                             │
│  4. SEND DECISION:                                                          │
│     ├─ If RecvLightACKCounter >= 64:                                        │
│     │      → Send Light ACK (sequence only)                                │
│     │      → Reset counter                                                  │
│     │                                                                       │
│     └─ If 10ms elapsed since last Full ACK:                                │
│          → Send Full ACK (sequence + RTT + stats)                          │
│          → Triggers ACKACK for RTT measurement                             │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Note**: The existing `RecvLightACKCounter` (incremented on each packet received) already implements the "64 packets" threshold. The continuous scan optimization doesn't change WHEN we send ACKs - it just ensures `contiguousPoint` is always up-to-date.

### 3.1 Unified Scan Starting Point

**Current Problem**: Today we have TWO separate scan starting points:
- `ackScanHighWaterMark` - Used by `periodicACK()` to track ACK scan progress
- `nakScanStartPoint` - Used by `periodicNAK()` to track NAK scan progress

Both scan the same packet btree, but independently. This creates redundant work.

**Insight**: The ACK sequence represents the **minimum point** where we tell the sender "we have everything up to here". NAK scanning only needs to look at packets **after** this point (gaps between ACK and incoming packets).

**Solution**: Unify into a single `contiguousPoint`:
- Initialized from handshake: `r.contiguousPoint.Store(recvConfig.InitialSequenceNumber.Val())`
- `contiguousScan` advances `contiguousPoint` forward as contiguous packets are found
- `gapScan` also advances `contiguousPoint` if it finds contiguous packets before the first gap
- Both scans start from `contiguousPoint` (no redundant re-scanning)

### 3.2 Unified Scan Window Visualization

```
                        Packet Btree (sorted by sequence number)
    ═══════════════════════════════════════════════════════════════════════════►
                                                                    sequence →

    InitialSequenceNumber                                              maxSeen
           │                                                              │
           ▼                                                              ▼
    ┌──────┬────────────────────────────────────────────────────────────────┐
    │ ISN  │  1   2   3   4   5   ·   7   8   ·   ·   11  12  13  ·   15    │
    └──────┴────────────────────────────────────────────────────────────────┘
                              │                         │         │
                              │                         │         │
                    contiguousPoint          tooRecentThreshold  now
                         (=5)                      (=12)

    ════════════════════════════════════════════════════════════════════════════

    REGIONS:

    │◄─── CONTIGUOUS ─────►│◄────── GAP SCAN WINDOW ──────►│◄─ TOO RECENT ─►│
    │   (already ACKed)    │    (check for gaps here)      │  (don't NAK)   │
    │                      │                               │                │
    │  seq 1-5 contiguous  │  gaps at 6, 9, 10, 14        │  seq 13, 15    │
    │  ACK sent for seq 5  │  NAK these if not too recent │  might be OOO  │

    ════════════════════════════════════════════════════════════════════════════

    EVENT LOOP SCAN ORDER:

    1. contiguousScan (every iteration, for ACK):
       - Start at contiguousPoint (5)
       - Scan forward: 6 missing → STOP
       - No progress this iteration (still at 5)
       - If packet 6 arrives → advance to 6, 7, 8 → contiguousPoint = 8

    2. gapScan (periodic timer, for NAK):
       - Start at contiguousPoint (5 or 8 after contiguousScan progress)
       - Scan forward until tooRecentThreshold (12)
       - Found packets 7, 8 present (if contiguousPoint was 5) → advance to 8
       - Found gaps: 6, 9, 10 → send NAK for these

    ════════════════════════════════════════════════════════════════════════════

    EXAMPLE: gapScan advances contiguousPoint

    State: contiguousPoint = 5, btree has [1,2,3,4,5,7,8,11,12]

    gapScan runs:
    - Starts at 5, expects 6
    - Finds 7 (gap at 6!) → gaps = [6]
    - BUT packets 7,8 are present before gap at 9
    - WAIT: Once we find first gap, we don't advance contiguousPoint further
    - We only advance if we find contiguous packets BEFORE the first gap

    Better example - contiguousPoint = 5, btree has [1,2,3,4,5,6,7,8,11,12]:
    - Starts at 5, expects 6
    - Finds 6 (no gap!) → lastContiguous = 6
    - Finds 7 (no gap!) → lastContiguous = 7
    - Finds 8 (no gap!) → lastContiguous = 8
    - Expects 9, finds 11 (gap!) → gaps = [9, 10]
    - contiguousPoint advanced: 5 → 8

    ════════════════════════════════════════════════════════════════════════════

    EDGE CASE: contiguousScan advances past gapScan window

    Before contiguousScan: contiguousPoint = 5
    Packets 6,7,8,9,10,11 arrive in burst
    After contiguousScan:  contiguousPoint = 11

    → gapScan checks: contiguousPoint (11) >= tooRecentThreshold (12)?
      - If yes: Nothing to scan, return early (no gaps possible)
      - If no:  Scan from 11 to 12 (minimal window)

    This gracefully handles the case where contiguousScan catches up to near-realtime.
```

### 3.3 Benefits of Unified Contiguous Point

| Aspect | Current (Two Variables) | Proposed (Unified) |
|--------|-------------------------|-------------------|
| Btree scans | ACK + NAK may re-scan same region | Both start from `contiguousPoint` |
| Advancement | Only ACK advances its variable | Both scans can advance `contiguousPoint` |
| Initialization | Two separate init paths | Single init from handshake ISN |
| State tracking | `ackScanHighWaterMark` + `nakScanStartPoint` | Single `contiguousPoint` |
| Wraparound handling | Two places to handle | One place to handle |
| Code complexity | Higher | Lower |

### 3.4 Implementation Notes

1. **`contiguousScan` runs every iteration** (in EventLoop `default:` case)
   - Advances `contiguousPoint` forward on contiguous arrivals
   - Used for generating ACK packets

2. **`gapScan` runs on timer** (in EventLoop `nakTicker.C` case)
   - Starts from `contiguousPoint` (shared with `contiguousScan`)
   - Advances `contiguousPoint` if contiguous packets found before first gap
   - Only scans up to `tooRecentThreshold`
   - If `contiguousPoint >= tooRecentThreshold`: early return (nothing to NAK)
   - Used for generating NAK packets

3. **Race condition avoided**: EventLoop is single-threaded consumer
   - No locks needed for `contiguousPoint` updates
   - `contiguousScan` and `gapScan` never run concurrently

### 3.5 Applying Unified Scan to Locked Functions (Tick-Based Loop)

The unified scan starting point optimization also benefits the **locked** (tick-based) functions. Since the library remains configurable to support both EventLoop and Tick-based modes, we can improve both.

#### 3.5.1 Current Functions and Scan Tracking Variables

| Function | File:Lines | Scan Start Variable | Lock Pattern |
|----------|------------|---------------------|--------------|
| `periodicACK()` | `receive.go:760-922` | `ackScanHighWaterMark` (circular.Number, lock-protected) | RLock→scan→RUnlock→WLock→write→WUnlock |
| `periodicACKWriteLocked()` | `receive.go:926-965` | Updates `ackScanHighWaterMark` | Called with WLock held |
| `periodicNAK()` | `receive.go:969-1072` | Dispatches to btree or list impl | RLock around scan |
| `periodicNakBtree()` | `receive.go:1076-1350` | `nakScanStartPoint` (atomic.Uint32) | RLock around btree scan |

**Current state**:
- `ackScanHighWaterMark`: Protected by `r.lock`, updated in `periodicACKWriteLocked()`
- `nakScanStartPoint`: Uses `atomic.Uint32`, updated in `periodicNakBtree()`

These are **two separate tracking variables** even though they track related concepts.

#### 3.5.2 Code Flow Analysis

**ACK Scan (`periodicACK` line 812)**:
```go
scanStartPoint := r.ackScanHighWaterMark  // Start from last ACK progress
// ... scan forward for contiguous packets ...
// Update in periodicACKWriteLocked (line 956-957):
if newHighWaterMark.Val() > 0 && newHighWaterMark.Gt(r.ackScanHighWaterMark) {
    r.ackScanHighWaterMark = newHighWaterMark
}
```

**NAK Scan (`periodicNakBtree` line 1134)**:
```go
startSeq := r.nakScanStartPoint.Load()  // Start from last NAK progress
// ... scan forward looking for gaps ...
// Update (line 1346):
if lastScannedSeq > 0 {
    r.nakScanStartPoint.Store(lastScannedSeq)
}
```

**Problem**: NAK scan may re-scan packets that ACK has already verified as contiguous.

#### 3.5.3 Unified Scan Design for Locked Functions

**SRT Sequence Numbers**: 31 bits (0 to `MAX_SEQUENCENUMBER = 0x7FFFFFFF`), stored in `uint32`.

**New unified field** (replaces both `ackScanHighWaterMark` and `nakScanStartPoint`):
```go
// receiver struct
contiguousPoint atomic.Uint32  // Last known contiguous sequence number (31-bit SRT seq)
                               // Both contiguousScan and gapScan can advance this
```

**Initialization** (in `NewReceiver`, line ~401):
```go
r.contiguousPoint.Store(recvConfig.InitialSequenceNumber.Val())  // uint32
```

#### 3.5.4 Simplified Design: Core Functions Handle Atomics

**Key insight**: If core functions handle atomic Load/Store internally, wrappers become trivial.

**Design principles**:
1. Core functions do atomic Load at start, atomic Store at end (if progress)
2. Wrappers only handle locking (if needed)
3. Use `uint32` throughout for efficiency (circular.Number only at API boundary)
4. Minimize function count - don't create unnecessary layers
5. **Both** `contiguousScan` and `gapScan` can advance `contiguousPoint`

**Simplified contiguous scan** (for ACK):
```go
// contiguousScan scans packet btree for contiguous sequences (ACKing process).
// Updates contiguousPoint atomically when progress is made.
// Thread-safe: uses atomic for contiguousPoint, caller handles btree access.
// Returns: ok=true if ACK should be sent, ackSeq=sequence to ACK
//
// IMPORTANT: Uses circular.SeqLessOrEqual for 31-bit wraparound safety.
// See: seq_math_31bit_wraparound_test.go for the wraparound bug that was fixed.
func (r *receiver) contiguousScan() (ok bool, ackSeq uint32) {
    // Atomic load of contiguous point
    lastContiguous := r.contiguousPoint.Load()

    // Get min packet (need btree access - caller ensures safety)
    minPkt := r.packetStore.Min()
    if minPkt == nil {
        return false, 0  // Empty btree
    }

    // Scan forward looking for contiguous sequence
    startSeq := lastContiguous
    r.packetStore.IterateFrom(circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
        func(p packet.Packet) bool {
            seq := p.Header().PacketSequenceNumber.Val()

            // Skip packets at or before current contiguous point
            // MUST use circular.SeqLessOrEqual for 31-bit wraparound!
            // Bug scenario: contiguousPoint=MAX-1, seq=2
            //   Raw: 2 <= MAX-1 → true (WRONG! 2 is circularly AFTER MAX-1)
            //   Circular: SeqLessOrEqual(2, MAX-1) → false (correct)
            if circular.SeqLessOrEqual(seq, startSeq) {
                return true
            }

            // Check if next in sequence
            expected := circular.SeqAdd(lastContiguous, 1)
            if seq == expected {
                lastContiguous = seq
                return true  // Continue scanning
            }

            return false  // Gap found, stop
        })

    // No progress?
    if lastContiguous == startSeq {
        return false, 0
    }

    // Atomic store of new contiguous point
    r.contiguousPoint.Store(lastContiguous)

    // Return the sequence number to ACK (lastContiguous + 1 per SRT spec)
    // https://datatracker.ietf.org/doc/html/draft-sharabayko-srt-01#section-3.2.4
    //   Last Acknowledged Packet Sequence Number: 32 bits.  This field
    //   contains the sequence number of the last data packet being
    //   acknowledged plus one.  In other words, it is the sequence number
    //   of the first unacknowledged packet.
    return true, circular.SeqAdd(lastContiguous, 1)
}

// ═══════════════════════════════════════════════════════════════════════════
// LOCKED WRAPPER: For Tick-based mode
// ═══════════════════════════════════════════════════════════════════════════
func (r *receiver) periodicACKLocked() (ok bool, seq circular.Number, lite bool) {
    r.lock.RLock()
    ok, ackSeq := r.contiguousScan()
    r.lock.RUnlock()

    if !ok {
        return false, circular.Number{}, false
    }

    // Convert uint32 to circular.Number for API compatibility
    return true, circular.New(ackSeq, packet.MAX_SEQUENCENUMBER), false
}

// NOTE: No "contiguousScanNoLock" needed - contiguousScan() IS the no-lock version.
// EventLoop calls contiguousScan() directly.
```

**Simplified gap scan** (for NAK):
```go
// gapScan scans packet btree for gaps (missing sequences).
// Updates contiguousPoint atomically when contiguous packets are found before gaps.
// Thread-safe: uses atomic for contiguousPoint, caller handles btree access.
// Returns: list of missing sequence numbers to NAK
//
// IMPORTANT: Uses circular.SeqLessOrEqual for 31-bit wraparound safety.
// See: seq_math_31bit_wraparound_test.go for the wraparound bug that was fixed.
func (r *receiver) gapScan() []uint32 {
    // Atomic load of contiguous point (shared with contiguousScan)
    lastContiguous := r.contiguousPoint.Load()

    // Get current time for "too recent" threshold calculation
    now := uint64(time.Now().UnixMicro())

    // Calculate tooRecentThreshold - don't NAK packets that arrived recently
    // (they might be out-of-order, not lost)
    tooRecentThreshold := now + uint64(float64(r.tsbpdDelay)*(1.0-r.nakRecentPercent))

    // Scan forward looking for gaps
    var gaps []uint32
    startSeq := lastContiguous
    expectedSeq := circular.SeqAdd(lastContiguous, 1)

    r.packetStore.IterateFrom(circular.New(startSeq, packet.MAX_SEQUENCENUMBER),
        func(p packet.Packet) bool {
            h := p.Header()
            seq := h.PacketSequenceNumber.Val()

            // Stop if too recent
            if h.PktTsbpdTime > tooRecentThreshold {
                return false
            }

            // Skip packets at or before contiguous point
            // MUST use circular.SeqLessOrEqual for 31-bit wraparound!
            if circular.SeqLessOrEqual(seq, startSeq) {
                return true
            }

            // Record gaps between expectedSeq and seq
            // Use circular.SeqLess to handle wraparound in gap detection
            for expectedSeq != seq && circular.SeqLess(expectedSeq, seq) {
                gaps = append(gaps, expectedSeq)
                expectedSeq = circular.SeqAdd(expectedSeq, 1)
            }

            // This packet is present - if no gaps before it, advance contiguousPoint
            // Example: contiguousPoint=5, we find 6,7,8 present, gap at 9
            //          → lastContiguous advances to 8, gaps=[9]
            if len(gaps) == 0 {
                lastContiguous = seq
            }

            expectedSeq = circular.SeqAdd(seq, 1)
            return true
        })

    // Update contiguousPoint if we found contiguous packets before first gap
    if lastContiguous != startSeq {
        r.contiguousPoint.Store(lastContiguous)
    }

    return gaps
}

// ═══════════════════════════════════════════════════════════════════════════
// LOCKED WRAPPER: For Tick-based mode
// ═══════════════════════════════════════════════════════════════════════════
func (r *receiver) periodicNakBtreeLocked() []circular.Number {
    r.lock.RLock()
    gaps := r.gapScan()
    r.lock.RUnlock()

    // Convert []uint32 to []circular.Number for API compatibility
    result := make([]circular.Number, len(gaps))
    for i, seq := range gaps {
        result[i] = circular.New(seq, packet.MAX_SEQUENCENUMBER)
    }
    return result
}

// NOTE: No "gapScanNoLock" needed - gapScan() IS the no-lock version.
// EventLoop calls gapScan() directly.
```

#### 3.5.5 31-Bit Wraparound Safety (CRITICAL)

**Background**: During Phase 4 implementation, we discovered a critical bug in `SeqLess` that caused incorrect sequence comparisons at the 31-bit wraparound boundary (MAX → 0). This was fixed and documented in `seq_math_31bit_wraparound_test.go`.

**The Bug**:
```go
// BROKEN - Raw comparison fails at wraparound!
if seq <= startSeq {  // When startSeq=MAX-1, seq=2: 2 <= MAX-1 → true (WRONG!)
    return true
}

// FIXED - Uses threshold-based comparison
if circular.SeqLessOrEqual(seq, startSeq) {  // Returns false (correct)
    return true
}
```

**Rule**: ALL sequence comparisons MUST use functions from `circular/seq_math.go`:

| Operation | Use This | NOT This |
|-----------|----------|----------|
| `a < b` | `circular.SeqLess(a, b)` | `a < b` |
| `a <= b` | `circular.SeqLessOrEqual(a, b)` | `a <= b` |
| `a > b` | `circular.SeqGreater(a, b)` | `a > b` |
| `a >= b` | `circular.SeqGreaterOrEqual(a, b)` | `a >= b` |
| `a + delta` | `circular.SeqAdd(a, delta)` | `(a + delta) & MAX` |
| `a - delta` | `circular.SeqSub(a, delta)` | `(a - delta) & MAX` |
| `a == b` | `a == b` | ✓ (equality is safe) |

**Note**: Equality (`==`) is safe because we're comparing bit patterns. Only ordering comparisons need wraparound handling.

#### 3.5.6 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **`atomic.Uint32`** not `Uint64` | SRT sequence is 31 bits, fits in uint32 |
| **`contiguousPoint`** naming | Describes what it represents: last known contiguous sequence |
| **`contiguousScan` / `gapScan`** | Describes what each function does, not what control packet it's for |
| **Both scans can advance `contiguousPoint`** | If gapScan finds contiguous packets before a gap, it advances |
| **Core handles atomics** | Wrappers become trivial (just lock + call) |
| **Use `uint32` internally** | Avoid circular.Number overhead in hot path |
| **Use `circular.Seq*` for comparisons** | Prevents 31-bit wraparound bugs (see 2.5.5) |
| **Convert at API boundary** | Only create circular.Number when returning to caller |

#### 3.5.7 Benefits Summary

| Aspect | Current | Proposed |
|--------|---------|----------|
| **Scan tracking** | Two variables (`ackScanHighWaterMark`, `nakScanStartPoint`) | Single `contiguousPoint atomic.Uint32` |
| **Naming** | `periodicACK`/`periodicNAK` (named for control packets) | `contiguousScan`/`gapScan` (named for what they do) |
| **Code duplication** | Separate locked/unlocked implementations | Single core + thin wrappers |
| **Atomic operations** | Mixed (Uint32 for NAK, none for ACK) | Consistent Uint32 atomics |
| **Internal types** | `circular.Number` everywhere | `uint32` internally, convert at boundary |
| **Wraparound safety** | Ad-hoc handling | Consistent use of `circular.Seq*` functions |
| **Scan efficiency** | May re-scan same region | Both start from same `contiguousPoint` |

#### 3.5.8 Migration Path

1. **Phase 1**: Add `contiguousPoint atomic.Uint32`, init from ISN
2. **Phase 2**: Implement `contiguousScan() (ok bool, ackSeq uint32)` with `circular.Seq*` functions
3. **Phase 3**: Rename `periodicACK()` → `periodicACKLocked()`, refactor to call `contiguousScan()`
4. **Phase 4**: Implement `gapScan() []uint32` using shared `contiguousPoint` with `circular.Seq*` functions
5. **Phase 5**: Rename `periodicNakBtree()` → `periodicNakBtreeLocked()`, refactor to call `gapScan()`
6. **Phase 6**: Update EventLoop to call `contiguousScan()` and `gapScan()` directly (no wrappers needed)
7. **Phase 7**: Update Tick() to call `periodicACKLocked()` and `periodicNakBtreeLocked()`
8. **Phase 8**: Remove old `ackScanHighWaterMark` and `nakScanStartPoint` fields
9. **Phase 9**: Add wraparound unit tests (see `seq_math_31bit_wraparound_test.go` for pattern)

Tests pass at each step. The core functions (`contiguousScan`, `gapScan`) can be unit tested directly, including wraparound edge cases.

**Naming Convention**:
- Core functions (no lock): `contiguousScan()`, `gapScan()`, `deliverReadyPackets()`
- Locked wrappers: `periodicACKLocked()`, `periodicNakBtreeLocked()`, `deliverReadyPacketsLocked()`
- No `*NoLock` functions needed - the core IS the no-lock version

### 3.6 EventLoop Order Optimization: Deliver Before Process

#### 3.6.1 Current Order

```go
default:
    now := uint64(time.Now().UnixMicro())
    processed := r.processOnePacket()           // 1. Insert into btree
    delivered := r.deliverReadyPackets()  // 2. Then deliver
```

#### 3.6.2 Proposed Order

```go
default:
    delivered := r.deliverReadyPackets()  // 1. Deliver first (shrink btree)
    processed := r.processOnePacket()           // 2. Then insert into smaller btree

    if processed || delivered > 0 {
        if ok, seq := r.contiguousScan(); ok {
            // ACK logic with modulus...
        }
    }
```

#### 3.6.3 Rationale

**Deliver first, then process:**

| Aspect | Current Order | Proposed Order |
|--------|---------------|----------------|
| **Btree size during insert** | Larger (includes ready-to-deliver packets) | Smaller (delivered packets removed first) |
| **Insert cost** | O(log n) with larger n | O(log n) with smaller n |
| **Delivery latency** | Packet waits for next iteration | Delivered immediately when ready |
| **Memory usage** | Higher (packets stay longer) | Lower (freed sooner) |
| **NAK btree size** | Larger | Smaller |

**Why there's no downside:**
- A newly arrived packet from the ring has a TSBPD time in the future
- It can't be ready for delivery in the same iteration it arrives
- So delivering first never delays the new packet

**Additional simplification**: `deliverReadyPackets()` can get `now` internally (like `gapScan`), removing the need to pass it as a parameter.

#### 3.6.4 How `deliverReadyPackets` Works

**File**: `congestion/live/receive.go` lines 1971-2003

```go
// deliverReadyPackets delivers all packets whose TSBPD time has arrived.
// Called every loop iteration for smooth, non-bursty delivery.
// Returns the count of packets delivered.
// NO LOCK needed - event loop has exclusive access to btree.
// Or called under lock from deliverReadyPacketsLocked() in Tick mode.
func (r *receiver) deliverReadyPackets() int {
    now := uint64(time.Now().UnixMicro())
    m := r.metrics
    delivered := 0

    // Iterate from btree.Min() forward, delivering packets whose time has come
    // Stop when we hit a packet still in the future
    removed := r.packetStore.RemoveAll(
        func(p packet.Packet) bool {
            // Check if packet is ready for delivery:
            // 1. Must be <= lastACKSequenceNumber (acknowledged)
            // 2. Must have TSBPD time <= now (ready for playback)
            h := p.Header()
            return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
        },
        func(p packet.Packet) {
            // Update metrics
            pktLen := p.Len()
            m.CongestionRecvPktBuf.Add(^uint64(0))                   // Decrement by 1
            m.CongestionRecvByteBuf.Add(^uint64(uint64(pktLen) - 1)) // Subtract pktLen

            // Update last delivered sequence
            h := p.Header()
            r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

            // Deliver to application
            r.deliver(p)
            delivered++
        },
    )
    _ = removed

    return delivered
}
```

**Caller Code** (showing predicate and deliverFunc):

**File**: `congestion/live/receive.go` lines 1977-1999

```go
removed := r.packetStore.RemoveAll(
    // PREDICATE: Check if packet is ready for delivery
    func(p packet.Packet) bool {
        h := p.Header()  // ← First Header() call
        return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
    },
    // DELIVER FUNC: Called for each packet that passes predicate
    func(p packet.Packet) {
        pktLen := p.Len()
        m.CongestionRecvPktBuf.Add(^uint64(0))
        m.CongestionRecvByteBuf.Add(^uint64(uint64(pktLen) - 1))

        h := p.Header()  // ← Second Header() call (redundant!)
        r.lastDeliveredSequenceNumber = h.PacketSequenceNumber

        r.deliver(p)
        delivered++
    },
)
```

**Underlying `RemoveAll` Implementation** (btree version - current):

**File**: `congestion/live/packet_store_btree.go` lines 92-111

```go
func (s *btreePacketStore) RemoveAll(predicate func(pkt packet.Packet) bool,
                                      deliverFunc func(pkt packet.Packet)) int {
    removed := 0
    var toRemove []*packetItem  // ← Allocates slice

    // PHASE 1: Traverse and collect
    s.tree.Ascend(func(item *packetItem) bool {
        if predicate(item.packet) {
            deliverFunc(item.packet)
            toRemove = append(toRemove, item)  // ← Grows slice
            removed++
            return true // Continue
        }
        return false // Stop at first non-matching
    })

    // PHASE 2: Delete collected items (requires re-traversing btree to find each)
    for _, item := range toRemove {
        s.tree.Delete(item)  // ← O(log n) lookup for each delete
    }

    return removed
}
```

#### 3.6.5 Delivery Logic Analysis

**Two conditions for delivery** (both must be true):
1. `h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber)` - Packet has been acknowledged
2. `h.PktTsbpdTime <= now` - TSBPD time has arrived

**Flow**:
```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           deliverReadyPackets()                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  1. RemoveAll starts from btree.Min() (lowest sequence number)             │
│                                                                             │
│  2. For each packet, check predicate:                                       │
│     ┌─────────────────────────────────────────────────────────────────┐    │
│     │ seq <= lastACKSequenceNumber  AND  PktTsbpdTime <= now         │    │
│     └─────────────────────────────────────────────────────────────────┘    │
│                                                                             │
│  3. If predicate TRUE:                                                      │
│     - Call deliverFunc (update metrics, deliver to app)                    │
│     - Add to toRemove list                                                  │
│     - Continue to next packet                                               │
│                                                                             │
│  4. If predicate FALSE:                                                     │
│     - STOP iteration (don't check remaining packets)                       │
│     - This is the "early exit" optimization                                 │
│                                                                             │
│  5. After iteration: batch delete all delivered packets from btree         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### 3.6.6 Potential Optimizations to Consider

| Observation | Current Behavior | Potential Optimization |
|-------------|------------------|------------------------|
| **Two-phase removal** | Collect in `toRemove` slice, then delete each with O(log n) lookup | Use `DeleteMin` in a loop - O(1) to find min |
| **Predicate calls `p.Header()` twice** | Once in predicate, once in deliverFunc | Could cache header in predicate closure |
| **`Lte()` uses circular.Number** | Creates/compares circular.Number | Could use raw uint32 + `circular.SeqLessOrEqual` |
| **`now` parameter** | Passed from caller | Get internally with `time.Now().UnixMicro()` |

**Note on `Lte()` and wraparound**: The `h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber)` call uses `circular.Number.Lte()` which should handle wraparound correctly (verify this is using the fixed `SeqLess` internally).

#### 3.6.7 RemoveAll Optimization Plan

**Problem**: Current `RemoveAll` is inefficient:
1. **Two-phase**: Collect items → Delete each (requires re-traversing btree)
2. **Slice allocation**: `toRemove` slice grows dynamically
3. **O(log n) per delete**: Each `Delete(item)` must traverse btree to find item

**Solution**: Single-pass using `DeleteMin`:

```go
// RemoveAllSlow - Original implementation (renamed for comparison)
func (s *btreePacketStore) RemoveAllSlow(predicate func(pkt packet.Packet) bool,
                                          deliverFunc func(pkt packet.Packet)) int {
    // ... existing two-phase implementation ...
}

// RemoveAll - Optimized single-pass implementation
func (s *btreePacketStore) RemoveAll(predicate func(pkt packet.Packet) bool,
                                      deliverFunc func(pkt packet.Packet)) int {
    removed := 0

    for {
        // Peek at minimum item (O(1) - btree maintains min pointer)
        min := s.tree.Min()
        if min == nil {
            break  // Empty tree
        }

        // Check predicate
        if !predicate(min.packet) {
            break  // First non-matching - stop (btree is sorted)
        }

        // Deliver the packet
        deliverFunc(min.packet)

        // Delete the minimum (O(log n) but no lookup needed - already at min)
        s.tree.DeleteMin()
        removed++
    }

    return removed
}
```

**Why `DeleteMin` is faster**:
- `Delete(item)` → O(log n) to FIND the item + O(log n) to rebalance = 2 × O(log n)
- `DeleteMin()` → O(1) to find min (cached) + O(log n) to rebalance = O(log n)
- For N deletions: Current = O(N × 2 × log N), Optimized = O(N × log N)

**Migration Plan**:

| Phase | Step | Details |
|-------|------|---------|
| 1 | Rename | `RemoveAll` → `RemoveAllSlow` |
| 2 | Implement | New `RemoveAll` using `DeleteMin` loop |
| 3 | Update tests | Test both `RemoveAllSlow` and `RemoveAll` |
| 4 | Add benchmarks | Benchmark both implementations |
| 5 | Run benchmarks | Confirm `RemoveAll` is faster |
| 6 | Deprecate | Mark `RemoveAllSlow` for removal |

**Test file**: `congestion/live/packet_store_btree_test.go`

```go
func TestRemoveAllSlow(t *testing.T) {
    // Existing tests renamed
}

func TestRemoveAll(t *testing.T) {
    // Same tests, new optimized function
}

func BenchmarkRemoveAllSlow(b *testing.B) {
    // Benchmark original
}

func BenchmarkRemoveAll(b *testing.B) {
    // Benchmark optimized - expect this to be faster
}
```

#### 3.6.8 Light ACK, Small ACK, and Full ACK (RFC Section 3.2.4)

**Note**: See Section 1 for the complete RFC specification of ACK types.

**Key Insight for Continuous Scan Optimization**:

The RFC recommends: **"send a Light ACK for every 64 packets received"**

This means our continuous scan optimization separates two concerns:

| Concern | What It Does | Frequency |
|---------|-------------|-----------|
| **Scanning** | Update `contiguousPoint` (track progress) | Every EventLoop iteration |
| **Sending** | Generate and send ACK packet | Per RFC recommendation |

**Current Implementation** (uses separate counter):

```go
// On every Push():
m.RecvLightACKCounter.Add(1)  // Atomic increment

// In periodicACK():
lightACKCount := r.metrics.RecvLightACKCounter.Load()
if lightACKCount >= 64 {
    needLiteACK = true
}
```

**Problems with counter approach**:
1. Counts ALL received packets (including out-of-order, duplicates)
2. Requires atomic increment on every `Push()` (hot path overhead)
3. May trigger Light ACK even when no contiguous progress made

**Proposed: Use Sequence Modulus Instead**:

Since `contiguousPoint` tracks the last contiguous sequence number, we can use it directly:

```go
// EventLoop default case:
delivered := r.deliverReadyPackets()
processed := r.processOnePacket()

// Remember old point before scan
oldContiguous := r.contiguousPoint.Load()

// ALWAYS scan to update contiguousPoint
ok, newContiguous := r.contiguousScan()

if ok {
    // Check if we crossed a 64-packet boundary
    // Using integer division: (old / 64) != (new / 64)
    if (oldContiguous >> 6) != (newContiguous >> 6) {
        // Crossed boundary - send Light ACK
        r.sendACK(circular.New(newContiguous, packet.MAX_SEQUENCENUMBER), true)
    }
}

// Full ACK still on timer for RTT measurement (10ms)
```

**Why Modulus is Better**:

| Aspect | Counter (`RecvLightACKCounter`) | Modulus (`contiguousPoint`) |
|--------|--------------------------------|----------------------------|
| **What's counted** | All received packets | Only contiguous (acknowledged) |
| **Hot path overhead** | Atomic increment every Push | None |
| **Triggers on** | Any 64 packets received | 64 packets of actual progress |
| **State needed** | Separate atomic counter | Already have `contiguousPoint` |
| **Reset needed** | Yes, after sending | No |

**Example**:
```
Packets received: 1, 2, 3, 5, 6, 7, 4, 8, 9, ...  (4 was delayed)

Counter approach:
  After 64 packets received → Light ACK (even if gap at 4)

Modulus approach:
  contiguousPoint stays at 3 until 4 arrives
  Once 4 arrives: contiguousPoint jumps 3→9
  If crosses 64 boundary → Light ACK (reflects real progress)
```

**Can Remove `RecvLightACKCounter`?**:

With the modulus approach, `RecvLightACKCounter` becomes unnecessary for Light ACK triggering. However, it's currently also used for:
- Metrics/monitoring (packets received between Full ACKs)

If we keep it for metrics only, we could rename it to clarify its purpose. Or remove it entirely if the metric isn't needed.

**Recommended Implementation**:

```go
// contiguousScan returns ok=true and the new contiguous point when progress made
func (r *receiver) contiguousScan() (ok bool, newContiguous uint32) {
    oldContiguous := r.contiguousPoint.Load()
    // ... scan logic ...
    if lastContiguous != oldContiguous {
        r.contiguousPoint.Store(lastContiguous)
        return true, lastContiguous
    }
    return false, oldContiguous
}

// In EventLoop:
oldContiguous := r.contiguousPoint.Load()
ok, newContiguous := r.contiguousScan()

// Check if crossed a LightACKModulus boundary
if ok && (oldContiguous / r.lightACKModulus) != (newContiguous / r.lightACKModulus) {
    r.sendACK(circular.New(newContiguous+1, packet.MAX_SEQUENCENUMBER), true)
}
```

**Note**: ACK sequence is `contiguousPoint + 1` per SRT spec (first unacknowledged).

#### 3.6.9 LightACKDifference Configuration

**Simpler approach**: Instead of division-based boundary checking, use simple subtraction:

```go
// Division approach - SLOW and complex
if (oldContiguous / lightACKModulus) != (newContiguous / lightACKModulus) { ... }

// Difference approach - FAST and clear
if newContiguous - lastLightACKSeq >= lightACKDifference {
    r.sendACK(seq, lite=true)
    lastLightACKSeq = newContiguous
    r.metrics.RecvLightACKCounter.Add(1)  // Track Light ACKs sent
}
```

**Why difference is better**:
1. **Fast**: Subtraction + comparison vs division
2. **Clear intent**: "Send Light ACK when we've progressed by N packets"
3. **Handles jumps**: Works correctly when `contiguousPoint` advances by multiple packets

**Rationale**: The RFC recommends Light ACK every 64 packets, but at high bitrates this may be excessive:

| Bitrate | Packets/sec | Light ACKs/sec (64) | Light ACKs/sec (256) |
|---------|-------------|---------------------|----------------------|
| 50 Mb/s | ~4,450 | ~70 | ~17 |
| 100 Mb/s | ~8,900 | ~139 | ~35 |
| 200 Mb/s | ~17,800 | ~278 | ~70 |

At 200Mb/s with difference=64, we'd send 278 Light ACKs/second. With difference=256, only 70.

**Config Addition** (`config.go`):

```go
// LightACKDifference controls how often Light ACK packets are sent.
// A Light ACK is sent when the contiguous sequence has advanced by
// at least this many packets since the last Light ACK.
// RFC recommends 64, but higher values reduce overhead at high bitrates.
// Default: 64 (RFC recommendation)
// Suggested for high bitrate: 256
LightACKDifference uint32
```

**Default value** (in `DefaultConfig()`):
```go
LightACKDifference: 64,  // RFC recommendation
```

**Validation**:
```go
if c.LightACKDifference == 0 {
    c.LightACKDifference = 64  // Sensible default
}
```

**CLI Flag** (`contrib/common/flags.go`):

```go
LightACKDifference = flag.Int("lightackdifference", -1,
    "Send Light ACK after N contiguous packets progress (default: 64, RFC recommendation)")
```

**Flag Application**:
```go
if FlagSet["lightackdifference"] && *LightACKDifference > 0 {
    config.LightACKDifference = uint32(*LightACKDifference)
}
```

**Test Script** (`contrib/common/test_flags.sh`):

```bash
# LightACKDifference flag tests
run_test "LightACKDifference default" \
    "-useeventloop -usepacketring" \
    "LightACKDifference.*64" \
    "$SERVER_BIN"

run_test "LightACKDifference=128" \
    "-useeventloop -usepacketring -lightackdifference 128" \
    "LightACKDifference.*128" \
    "$SERVER_BIN"

run_test "LightACKDifference=256" \
    "-useeventloop -usepacketring -lightackdifference 256" \
    "LightACKDifference.*256" \
    "$SERVER_BIN"
```

**Receiver Struct Update** (`congestion/live/receive.go`):

```go
type receiver struct {
    // ... existing fields ...
    lightACKDifference uint32  // Light ACK threshold (default: 64)
    lastLightACKSeq    uint32  // Sequence when last Light ACK was sent
}
```

**ReceiveConfig Update**:

```go
type ReceiveConfig struct {
    // ... existing fields ...
    LightACKDifference uint32  // Light ACK threshold (default: 64)
}
```

**Receiver Initialization**:
```go
func NewReceiver(recvConfig ReceiveConfig) *receiver {
    lightACKDifference := recvConfig.LightACKDifference
    if lightACKDifference == 0 {
        lightACKDifference = 64  // Default
    }

    return &receiver{
        // ...
        lightACKDifference: lightACKDifference,
        lastLightACKSeq:    recvConfig.InitialSequenceNumber.Val(),
    }
}
```

**EventLoop Light ACK Logic**:

```go
// In EventLoop default case:
ok, newContiguous := r.contiguousScan()

if ok {
    // Send Light ACK when we've advanced by >= LightACKDifference
    // Use circular.SeqSub for 31-bit wraparound safety
    diff := circular.SeqSub(newContiguous, r.lastLightACKSeq)
    if diff >= r.lightACKDifference {
        r.sendACK(circular.New(newContiguous+1, packet.MAX_SEQUENCENUMBER), true)
        r.lastLightACKSeq = newContiguous
        r.metrics.RecvLightACKCounter.Add(1)  // Track Light ACKs sent
    }
}
```

**Metrics Analysis: Light ACK vs Full ACK**

**Current state** (`metrics/metrics.go`, `metrics/packet_classifier.go`):
- `PktSentACKSuccess` - All ACKs sent (no Lite/Full distinction)
- `PktRecvACKSuccess` - All ACKs received (no Lite/Full distinction)
- `RecvLightACKCounter` - Packets received since last ACK (misleading name!)

**Problem**: Cannot distinguish Light ACK from Full ACK in metrics.

**Solution**: Add separate counters for Light and Full ACK:

```go
// In metrics/metrics.go - ADD:
PktSentACKLiteSuccess atomic.Uint64  // Light ACKs sent
PktSentACKFullSuccess atomic.Uint64  // Full ACKs sent
PktRecvACKLiteSuccess atomic.Uint64  // Light ACKs received
PktRecvACKFullSuccess atomic.Uint64  // Full ACKs received
```

**Where to increment** (NOT in packet_classifier - too late, packet may be gone):

1. **Send side** - in `connection.go` `sendACK()`:
```go
func (c *srtConn) sendACK(seq circular.Number, lite bool) {
    // ... existing code ...

    if lite {
        cif.IsLite = true
        p.Header().TypeSpecific = 0
        c.metrics.PktSentACKLiteSuccess.Add(1)  // NEW
    } else {
        // ... Full ACK setup ...
        c.metrics.PktSentACKFullSuccess.Add(1)  // NEW
    }
    // ... rest of function ...
}
```

2. **Receive side** - in `connection.go` `handleACK()`:
```go
func (c *srtConn) handleACK(p packet.Packet) {
    // ... unmarshal cif ...

    if cif.IsLite {
        c.metrics.PktRecvACKLiteSuccess.Add(1)  // NEW
    } else {
        c.metrics.PktRecvACKFullSuccess.Add(1)  // NEW
        // Full ACK triggers ACKACK
        c.sendACKACK(p.Header().TypeSpecific)
    }
}
```

**Remove `RecvLightACKCounter`**:

The counter name is misleading and its current purpose (packets received since ACK) is replaced by the difference-based Light ACK triggering.

**New metric names are clearer**:
- `PktSentACKLiteSuccess` - Light ACKs we sent
- `PktSentACKFullSuccess` - Full ACKs we sent
- `PktRecvACKLiteSuccess` - Light ACKs we received from peer
- `PktRecvACKFullSuccess` - Full ACKs we received from peer

**Files to update for new metrics**:

| File | Change |
|------|--------|
| `metrics/metrics.go` | Add 4 new atomic counters |
| `connection.go` | Increment in `sendACK()` and `handleACK()` |
| `metrics/handler.go` | Export new counters to Prometheus |
| `metrics/handler_test.go` | Add tests for new counters |
| `metrics/metrics.go` | **Remove** `RecvLightACKCounter` |
| `congestion/live/receive.go` | Remove `RecvLightACKCounter.Add(1)` from Push functions |
| `congestion/live/fake.go` | Remove `RecvLightACKCounter.Add(1)` |
| `congestion/live/receive_ring_test.go` | Update test expectations |

**Files Requiring Updates**:

| File | Line(s) | Current Code | Change |
|------|---------|--------------|--------|
| `congestion/live/receive.go` | 509, 553, 660 | `m.RecvLightACKCounter.Add(1)` | **Remove** (no longer needed) |
| `congestion/live/receive.go` | 767-769 | Counter load + `if lightACKCount >= 64` | Change to difference check |
| `congestion/live/receive.go` | 961 | `r.metrics.RecvLightACKCounter.Store(0)` | **Remove** |
| `congestion/live/receive.go` | struct | — | Add `lastLightACKSeq uint32` field |
| `congestion/live/fake.go` | 127 | `m.RecvLightACKCounter.Add(1)` | **Remove** |
| `congestion/live/fake.go` | 159-163 | Counter load + `if lightACKCount >= 64` | Change to difference check |
| `congestion/live/fake.go` | 176 | `r.metrics.RecvLightACKCounter.Store(0)` | **Remove** |
| `metrics/metrics.go` | 326 | `RecvLightACKCounter atomic.Uint64` | **Remove** (replaced by new counters) |
| `metrics/metrics.go` | (new) | — | Add `PktSentACKLiteSuccess`, `PktSentACKFullSuccess` |
| `metrics/metrics.go` | (new) | — | Add `PktRecvACKLiteSuccess`, `PktRecvACKFullSuccess` |
| `connection.go` | ~1724 | `sendACK()` | Add `PktSentACKLiteSuccess/FullSuccess.Add(1)` |
| `connection.go` | ~1093 | `handleACK()` | Add `PktRecvACKLiteSuccess/FullSuccess.Add(1)` |
| `metrics/handler.go` | (new) | — | Export 4 new Lite/Full ACK counters to Prometheus |
| `metrics/handler_test.go` | 196 | `"RecvLightACKCounter": true` | **Remove** entry |
| `metrics/handler_test.go` | (new) | — | Add tests for 4 new counters |
| `congestion/live/receive_ring_test.go` | 91 | `RecvLightACKCounter` check | **Remove** or update test |
| `config.go` | (new) | — | Add `LightACKDifference uint32` |
| `contrib/common/flags.go` | (new) | — | Add `-lightackdifference` flag |
| `contrib/common/test_flags.sh` | (new) | — | Add test cases |

**Summary of Changes**:

1. **Keep `RecvLightACKCounter`** - But change its meaning:
   - **OLD**: Packets received since last ACK (reset on send)
   - **NEW**: Cumulative count of Light ACKs sent (for metrics/monitoring)

2. **Remove counter increments from hot path** - No more `Add(1)` on every `Push()`
   - Removes 3 atomic increments per packet in `receive.go`
   - Removes 1 atomic increment per packet in `fake.go`

3. **Add `LightACKDifference` config** - Configurable threshold (default 64)

4. **Change Light ACK trigger** - From counter to sequence difference check:
   ```go
   // OLD (counter-based):
   lightACKCount := r.metrics.RecvLightACKCounter.Load()
   if lightACKCount >= 64 { needLiteACK = true }
   r.metrics.RecvLightACKCounter.Store(0)  // Reset

   // NEW (difference-based):
   diff := circular.SeqSub(newContiguous, r.lastLightACKSeq)
   if diff >= r.lightACKDifference {
       r.sendACK(seq, lite=true)
       r.lastLightACKSeq = newContiguous
       r.metrics.RecvLightACKCounter.Add(1)  // Count Light ACKs sent
   }
   ```

**Migration Path Update**:

| Phase | Step |
|-------|------|
| 1 | Add `LightACKDifference` to `Config` struct with default 64 |
| 2 | Add `-lightackdifference` CLI flag |
| 3 | Add `LightACKDifference` to `ReceiveConfig` and pass to receiver |
| 4 | Add `lastLightACKSeq` field to receiver struct |
| 5 | Add 4 new metrics: `PktSentACKLiteSuccess`, `PktSentACKFullSuccess`, `PktRecvACKLiteSuccess`, `PktRecvACKFullSuccess` |
| 6 | Update `connection.go` `sendACK()` to increment `PktSentACKLite/FullSuccess` |
| 7 | Update `connection.go` `handleACK()` to increment `PktRecvACKLite/FullSuccess` |
| 8 | Export 4 new metrics in `metrics/handler.go` |
| 9 | Remove `RecvLightACKCounter` from `metrics/metrics.go` |
| 10 | Remove `RecvLightACKCounter.Add(1)` from all `Push*()` functions |
| 11 | Update `periodicACKLocked()` to use difference check |
| 12 | Update `fake.go` `periodicACK()` similarly |
| 13 | Update `metrics/handler_test.go` - remove old, add new tests |
| 14 | Update `receive_ring_test.go` |
| 15 | Update EventLoop to use difference-based Light ACK |
| 16 | Run `test_flags.sh` to validate |
| 17 | Benchmark at 100Mb/s with different difference values |

**Performance Impact**:

Removing `RecvLightACKCounter.Add(1)` from the hot path eliminates:
- 3 atomic operations per packet in `receive.go` (lines 509, 553, 660)
- 1 atomic operation per packet in `fake.go` (line 127)

At 100Mb/s (~8,900 packets/sec), this saves ~35,600 atomic operations/second.

**Improved Monitoring**:

New metrics provide clearer visibility:

| Metric | Meaning | Use Case |
|--------|---------|----------|
| `PktSentACKLiteSuccess` | Light ACKs we sent | Track Light ACK rate |
| `PktSentACKFullSuccess` | Full ACKs we sent | Should be ~100/sec (10ms interval) |
| `PktRecvACKLiteSuccess` | Light ACKs received from peer | Verify peer is sending Light ACKs |
| `PktRecvACKFullSuccess` | Full ACKs received from peer | Verify RTT feedback loop |

**Example Prometheus queries**:
```promql
# Light ACK rate (should increase with bitrate)
rate(gosrt_pkt_sent_ack_lite_success[1m])

# Full ACK rate (should be ~100/sec regardless of bitrate)
rate(gosrt_pkt_sent_ack_full_success[1m])

# Ratio of Light to Full ACKs
gosrt_pkt_sent_ack_lite_success / gosrt_pkt_sent_ack_full_success
```

#### 3.6.10 Tick-Based Delivery Analysis

**File**: `congestion/live/receive.go` lines 1643-1720

The `Tick()` function has delivery logic **inlined and duplicated**:

```go
func (r *receiver) Tick(now uint64) {
    // 1. Drain ring buffer (if enabled)
    if r.usePacketRing {
        r.drainRingByDelta()
    }

    // 2. ACK processing
    if ok, sequenceNumber, lite := r.periodicACK(now); ok {
        r.sendACK(sequenceNumber, lite)
    }

    // 3. NAK processing
    if list := r.periodicNAK(now); len(list) != 0 {
        metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
        r.sendNAK(list)
    }

    // 4. Expire NAK entries
    if r.useNakBtree && r.nakBtree != nil {
        r.expireNakEntries()
    }

    // 5. DELIVERY (inlined, duplicated for lock timing)
    m := r.metrics
    if r.lockTiming != nil {
        // Branch A: With lock timing wrapper
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            removed = r.packetStore.RemoveAll(...)  // Same predicate as deliverReadyPackets
        })
    } else {
        // Branch B: Without lock timing
        r.lock.Lock()
        removed := r.packetStore.RemoveAll(...)     // Same predicate DUPLICATED
        r.lock.Unlock()
    }
}
```

**Issues Identified**:

| Issue | Impact |
|-------|--------|
| **Order**: ACK → NAK → deliver | Btree larger during ACK/NAK scans |
| **Code duplication** | Delivery logic duplicated between `Tick()` and `deliverReadyPackets()` |
| **Lock timing branch** | Same predicate logic repeated twice in `Tick()` |

**Proposed Refactoring**:

```go
// ═══════════════════════════════════════════════════════════════════════════
// CORE: Delivery logic (no locks, assumes exclusive btree access)
// ═══════════════════════════════════════════════════════════════════════════
func (r *receiver) deliverReadyPackets() int {
    now := uint64(time.Now().UnixMicro())
    m := r.metrics
    delivered := 0

    r.packetStore.RemoveAll(
        func(p packet.Packet) bool {
            h := p.Header()
            return circular.SeqLessOrEqual(h.PacketSequenceNumber.Val(),
                                           r.lastACKSequenceNumber.Val()) &&
                   h.PktTsbpdTime <= now
        },
        func(p packet.Packet) {
            // ... metrics and delivery logic ...
            delivered++
        },
    )
    return delivered
}

// ═══════════════════════════════════════════════════════════════════════════
// LOCKED WRAPPER: For Tick-based mode
// ═══════════════════════════════════════════════════════════════════════════
func (r *receiver) deliverReadyPacketsLocked() int {
    if r.lockTiming != nil {
        var delivered int
        metrics.WithWLockTiming(r.lockTiming, &r.lock, func() {
            delivered = r.deliverReadyPackets()
        })
        return delivered
    }

    r.lock.Lock()
    delivered := r.deliverReadyPackets()
    r.lock.Unlock()
    return delivered
}

// ═══════════════════════════════════════════════════════════════════════════
// Tick() simplified
// ═══════════════════════════════════════════════════════════════════════════
func (r *receiver) Tick(now uint64) {
    if r.usePacketRing {
        r.drainRingByDelta()
    }

    // REORDERED: Deliver FIRST to shrink btree
    r.deliverReadyPacketsLocked()

    // Then ACK and NAK (on smaller btree)
    if ok, seq, lite := r.periodicACKLocked(); ok {
        r.sendACK(seq, lite)
    }

    if list := r.periodicNakBtreeLocked(); len(list) != 0 {
        metrics.CountNAKEntries(r.metrics, list, metrics.NAKCounterSend)
        r.sendNAK(list)
    }

    if r.useNakBtree && r.nakBtree != nil {
        r.expireNakEntries()
    }
}
```

#### 3.6.11 Benefits of Refactored Delivery

| Aspect | Current | Proposed |
|--------|---------|----------|
| **Tick order** | ACK → NAK → deliver | **deliver → ACK → NAK** |
| **Code duplication** | 3 copies of delivery predicate | 1 copy in `deliverReadyPackets()` |
| **Lock timing** | Inline branching in Tick | Encapsulated in wrapper |
| **Function naming** | `deliverReadyPacketsNoLock` (misleading name) | `deliverReadyPackets` + `deliverReadyPacketsLocked` |
| **`now` parameter** | Passed to functions | Get internally |

#### 3.6.12 Updated Migration Path

Add to Phase 7:
- **Phase 7a**: Create `deliverReadyPackets()` core function (get `now` internally)
- **Phase 7b**: Create `deliverReadyPacketsLocked()` wrapper
- **Phase 7c**: Refactor `Tick()` to use `deliverReadyPacketsLocked()` and reorder: deliver → ACK → NAK
- **Phase 7d**: Rename `deliverReadyPacketsNoLock()` → `deliverReadyPackets()`

---

## 4. Current Implementation

### 4.1 EventLoop Structure

**File**: `congestion/live/receive.go` lines 1784-1882

```go
func (r *receiver) EventLoop(ctx context.Context) {
    // ... setup tickers ...

    ackTicker := time.NewTicker(ackInterval)   // ← PROBLEM: 10ms timer
    defer ackTicker.Stop()

    // ... other tickers ...

    for {
        select {
        case <-ctx.Done():
            return

        case <-ackTicker.C:                     // ← ACK only fires every 10ms
            r.drainRingByDelta()
            now := uint64(time.Now().UnixMicro())
            if ok, seq, lite := r.periodicACK(now); ok {
                r.sendACK(seq, lite)
            }

        case <-nakTicker.C:
            // NAK processing...

        case <-rateTicker.C:
            // Rate stats...

        default:
            now := uint64(time.Now().UnixMicro())
            processed := r.processOnePacket()
            delivered := r.deliverReadyPackets()
            // ... adaptive backoff ...
        }
    }
}
```

## 5. Testing Plan

### 5.1 Unit Tests

**File**: `congestion/live/receive_test.go`

Add tests for:
- `contiguousScan()` correctly advances ACK sequence
- ACKModulus=1 sends ACK every packet
- ACKModulus=10 sends ACK every 10th packet
- ACKModulus behavior with out-of-order packets (counter resets)

### 5.2 Integration Test Matrix Update

**File**: `contrib/integration_testing/configs.go`

Add `ACKModulus` as a new dimension for EventLoop tests:

| ACKModulus | Behavior | Test Abbrev |
|------------|----------|-------------|
| 1 | ACK every packet (legacy-like) | `Ack1` |
| 10 | ACK every 10th packet (default) | `Ack10` |
| 50 | ACK every 50th packet (aggressive) | `Ack50` |

**New config variants** (EventLoop only):

| Config Name | Features |
|-------------|----------|
| `FullEL_Ack1` | Full + EventLoop + ACKModulus=1 |
| `FullEL_Ack10` | Full + EventLoop + ACKModulus=10 |
| `FullEL_Ack50` | Full + EventLoop + ACKModulus=50 |

### 5.3 Test Commands

**Isolation tests** (single server/client, no network impairment):
```bash
make test-isolation-starlink-5Mbps-EL-Ack1
make test-isolation-starlink-5Mbps-EL-Ack10
make test-isolation-starlink-5Mbps-EL-Ack50
```

**Parallel comparison tests** (Base vs EventLoop variants):
```bash
make test-parallel-starlink-5Mbps-Base-vs-FullEL_Ack1
make test-parallel-starlink-5Mbps-Base-vs-FullEL_Ack10
make test-parallel-starlink-5Mbps-Base-vs-FullEL_Ack50
```

### 5.4 Success Criteria

| Metric | Current (EventLoop) | Target (ACKModulus=10) | Target (ACKModulus=1) |
|--------|---------------------|------------------------|----------------------|
| RTT (MsRTT) | ~10ms | <2ms | <1ms |
| ACK packets/sec | ~100 | ~890 | ~8,900 |
| CPU overhead | ~0.05% | ~0.25% | ~0.35% |
| Packet loss | baseline | ≤ baseline | ≤ baseline |
| Retransmissions | baseline | ≤ baseline | ≤ baseline |

**Key validation**: RTT should decrease from ~10ms to <2ms with ACKModulus=10, and <1ms with ACKModulus=1.

---

## 6. Code Locations Summary

| File | Line | Change |
|------|------|--------|
| `congestion/live/receive.go` | 758-922 | Create `contiguousScan()`, rename `periodicACK` → `periodicACKLocked()` |
| `congestion/live/receive.go` | 1784-1882 | Modify `EventLoop()`: remove `ackTicker`, add continuous ACK |
| `config.go` | ~381 | Add `ACKModulus` field |
| `config.go` | ~490 | Add `ACKModulus` default value (10) |
| `config.go` | ~1135 | Add `ACKModulus` validation |
| `contrib/common/flags.go` | ~104 | Add `-ackmodulus` flag |
| `contrib/common/flags.go` | ~377 | Apply `ackmodulus` flag to config |
| `contrib/common/test_flags.sh` | EOF | Add ACKModulus test cases |
| `contrib/integration_testing/configs.go` | TBD | Add `FullEL_Ack1/10/50` configs |

---

## 7. Rollout Plan

### 7.1 Development (This PR)

1. Implement Phase A-D code changes
2. Run `test_flags.sh` to validate flag parsing
3. Run unit tests
4. Run isolation tests with ACKModulus=1,10,50

### 7.2 Validation

1. Run parallel comparison tests
2. Verify RTT reduction in test results
3. Check for regressions in packet loss / retransmissions
4. Document results in `lockless_phase5_implementation.md`

### 7.3 Default Behavior

- `ACKModulus=10` as default (good balance of RTT vs overhead)
- Users can set `ACKModulus=1` for minimum RTT (legacy-like behavior)
- Users can set `ACKModulus=50+` for minimum overhead (higher latency environments)

---

## 8. Appendix: ACKModulus Trade-offs

From `ack_optimization.md` benchmarks at 100 Mb/s:

| ACKModulus | ACK Latency | ACKs/sec | CPU Overhead |
|------------|-------------|----------|--------------|
| Timer (10ms) | 5ms avg | ~100 | ~0.05% |
| 50 | ~5.6ms | ~180 | ~0.25% |
| 10 | ~1.1ms | ~890 | ~0.25% |
| 1 | <0.1ms | ~8,900 | ~0.35% |

**Recommendation**: Start with `ACKModulus=10` (default). This provides:
- ~10x RTT reduction (10ms → ~1ms)
- ~10x more ACKs than timer-based
- Only ~5x more CPU than timer-based (~0.25% vs ~0.05%)

