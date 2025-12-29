# io_uring WaitCQETimeout Implementation

## Overview

This document tracks the implementation of the `WaitCQETimeout` optimization for io_uring completion handlers, replacing the inefficient `PeekCQE` + sleep polling approach.

**Design Document**: [ack_ackack_redesign_progress.md](./ack_ackack_redesign_progress.md) - See sections:
- "Alternative Strategy: Blocking with Timeout (`WaitCQETimeout`)"
- "Detailed Implementation Plan: WaitCQETimeout"
- "Phase M: New Metrics for io_uring Submission and Completion Handlers"

**Related**:
- [ring_retry_strategy_integration.md](./ring_retry_strategy_integration.md) - Ring buffer retry strategies to avoid sleeping when ring is full
- [ring_retry_strategy_integration_implementation.md](./ring_retry_strategy_integration_implementation.md) - Implementation progress tracking

## Problem Statement

The current io_uring completion handlers use `PeekCQE()` with a fixed 10ms sleep (`ioUringPollInterval`), causing:
- **RTT inflation**: ~5ms instead of ~0.1ms
- **Latency**: Sleep happens AFTER completion arrives, adding unnecessary delay

## Solution

Replace `PeekCQE()` + `time.Sleep()` with `WaitCQETimeout()`:
- Kernel blocks until completion arrives OR timeout expires
- **Zero latency** when completions are available
- Same graceful shutdown behavior (timeout allows ctx.Done() check)

## Implementation Progress

| Phase | Description | Status | Notes |
|-------|-------------|--------|-------|
| M.2.1 | Add metrics to `metrics/metrics.go` | ✅ Done | 33 metrics added |
| M.2.2 | Add Prometheus export to `metrics/handler.go` | ✅ Done | 33 exports added |
| M.2.3 | Add unit tests to `metrics/handler_test.go` | ✅ Done | 2 test functions added |
| M.2.4 | Instrument submission functions | ✅ Done | All 3 files instrumented |
| M.2.4 | Instrument completion functions | ✅ Done | All 3 files - WaitCQETimeout |
| M.2.5 | Update `analysis.go` | ⏸️ Deferred | Will add validation later |
| M.2.6 | Update `test_isolation_mode.go` | ✅ Done | 12 rows added |
| M.3 | Run `make audit-metrics` | ✅ Done | 0 io_uring metrics unused |
| 1.1 | Update constants in `connection_linux.go` | ✅ Done | WaitTimeout + retry constants |
| 1.2 | Update `sendCompletionHandler()` | ✅ Done | Uses WaitCQETimeout |
| 2.1 | Update comment in `listen_linux.go` | ✅ Done | Helper functions added |
| 2.2 | Update `getRecvCompletion()` in listener | ✅ Done | Uses WaitCQETimeout |
| 3.1 | Update comment in `dial_linux.go` | ✅ Done | |
| 3.2 | Update `getRecvCompletion()` in dialer | ✅ Done | Uses WaitCQETimeout |
| T1 | Unit tests pass | ✅ Done | All tests passing |
| T2 | Integration tests pass | ✅ Done | 6 strategy tests passed |
| T3 | RTT verification | ⚠️ Partial | Improved 10x but not to control |
| T4 | Shutdown tests pass | ✅ Done | Graceful shutdown works |
| T5 | Fix SIGSEGV on shutdown | ✅ Done | Context-only approach (no atomic flag) |

## Implementation Log

### 2025-12-28: Fixed SIGSEGV on Shutdown (Refined)

**Problem**: Race condition during shutdown caused SIGSEGV in `giouring` library:
- `cleanupIoUring()` was calling `ring.QueueExit()` BEFORE handler exited
- Handler blocked in `WaitCQETimeout()` would SIGSEGV when ring memory was unmapped

**Solution**: Context-only approach following `context_and_cancellation_design.md`:
- **No atomic flags needed** - `ctx.Done()` is sufficient and avoids hot path overhead
- Changed cleanup order: **wait for WaitGroup BEFORE calling QueueExit()**
- If handler doesn't exit within 2s, skip QueueExit (minor leak vs crash)
- Set `sendRing = nil` after QueueExit for graceful failure on late sends

**Files Modified**:
- `connection.go`: **Removed** `sendRingClosed atomic.Bool`
- `connection_linux.go`: Wait 2s for handler, then conditionally QueueExit
- `listen_linux.go`: Same pattern for recv handler
- `dial_linux.go`: Same pattern for recv handler

**Design Document**: `documentation/iouring_shutdown_design.md`

**Verified**: Isolation test `Isolation-5M-FullEventLoop` completes without SIGSEGV

---

### 2025-12-27: Implementation Complete

#### Phase M: Metrics
- Added 33 new atomic counters to `metrics/metrics.go` (15 submission + 18 completion)
- Added Prometheus exports to `metrics/handler.go`
- Added 2 unit tests to `metrics/handler_test.go`
- Added 12 rows to isolation test output in `test_isolation_mode.go`

#### Phase 1-3: WaitCQETimeout Implementation
- Updated `connection_linux.go`:
  - Replaced `ioUringPollInterval` with `ioUringWaitTimeout` (Timespec)
  - Added `ioUringRetryBackoff`, `ioUringMaxGetSQERetries`, `ioUringMaxSubmitRetries` constants
  - Instrumented `sendIoUring()` with submission metrics
  - Updated `sendCompletionHandler()` to use `WaitCQETimeout` with completion metrics

- Updated `listen_linux.go`:
  - Updated `getRecvCompletion()` to use `WaitCQETimeout`
  - Instrumented `submitRecvRequest()` with submission metrics
  - Added helper functions for incrementing metrics via sync.Map

- Updated `dial_linux.go`:
  - Updated `getRecvCompletion()` to use `WaitCQETimeout`
  - Instrumented `submitRecvRequest()` with submission metrics

#### Metrics Audit Result
```
make audit-metrics
...
✅ Fully Aligned (defined, used, exported): 228 fields
⚠️  Defined but never used: 2 fields (pre-existing NAK metrics)
❌ Used but NOT exported to Prometheus: 0 fields
```

---

## Files Modified

| File | Changes |
|------|---------|
| `metrics/metrics.go` | Added 33 io_uring metrics (lines 365-429) |
| `metrics/handler.go` | Added 33 Prometheus exports (lines 820-930) |
| `metrics/handler_test.go` | Added 2 test functions |
| `connection_linux.go` | Updated constants, sendIoUring, sendCompletionHandler |
| `listen_linux.go` | Updated getRecvCompletion, submitRecvRequest, added helpers |
| `dial_linux.go` | Updated getRecvCompletion, submitRecvRequest |
| `contrib/integration_testing/test_isolation_mode.go` | Added 12 metric rows |

## Test Results

### Unit Tests
```
=== RUN   TestPrometheusIoUringSubmissionMetrics
--- PASS: TestPrometheusIoUringSubmissionMetrics (0.00s)
=== RUN   TestPrometheusIoUringCompletionMetrics
--- PASS: TestPrometheusIoUringCompletionMetrics (0.00s)
PASS
ok      github.com/datarhei/gosrt/metrics       0.004s

go test ./... -short -count=1
ok      github.com/datarhei/gosrt       35.206s
ok      github.com/datarhei/gosrt/congestion/live       45.465s
... all tests passing
```

### Integration Tests (2025-12-27)

Ran 6 strategy isolation tests (all using WaitCQETimeout):

```bash
sudo make test-isolation-strategies
```

| Strategy | Test RTT (µs) | Control RTT (µs) | Drops | io_uring Timeouts |
|----------|---------------|------------------|-------|-------------------|
| Sleep | 413 | 76 | 130 | Snd:20, Rcv:7 |
| Next | 469 | 128 | 124 | Snd:60, Rcv:46 |
| Random | 352 | 82 | 131 | Snd:28, Rcv:2 |
| Adaptive | 345 | 81 | 140 | Snd:34, Rcv:25 |
| Spin | 470 | 78 | 126 | Snd:7, Rcv:2 |
| Hybrid | 427 | 119 | 150 | Snd:9, Rcv:3 |

### Isolation Tests (RTT Verification)

**Result**: RTT improved from ~5-10ms (PeekCQE+sleep) to **~350-470µs** (WaitCQETimeout)

**However**: RTT is still 3-5x higher than control (~80-120µs), and drops persist (124-150 per test).

### Analysis: Why Drops Still Occur

1. **WaitCQETimeout works correctly**:
   - io_uring completions succeed (Snd:~2890, Rcv:~8800)
   - Low timeout counts (Snd:7-60, Rcv:2-46)
   - No errors or ring full conditions

2. **RTT improved but not to control levels**:
   - Before: ~5-10ms (PeekCQE + 10ms sleep)
   - After: ~350-470µs (WaitCQETimeout)
   - Control: ~80-120µs
   - Gap: ~250-400µs still unexplained

3. **Drops source**: TSBPD timeout (packets delivered too late)
   - Not caused by ring strategy or io_uring
   - Likely caused by Full ACK timer not firing at right time

### Drop Analysis (2025-12-27)

#### Test Results Summary (Updated)

| Test | Duration | RTT (µs) | Drops | Notes |
|------|----------|----------|-------|-------|
| Isolation-5M-EventLoop | 30s | 78 ✅ | 321 ❌ | EventLoop only, no io_uring |
| Isolation-5M-EventLoop-NoIOUring | 10s | 93 ✅ | 118 ❌ | EventLoop + NAK btree, no io_uring |

**RTT is now excellent** (~78-93µs) - matching or beating control!

#### Bug Fix: Metrics Label Mismatch ✅

**Fixed** in `metrics_collector.go`:
- `"too_late"` → `"too_old"` ✅
- `"buffer_full"` → `"store_insert_failed"` ✅
- Added `store_fail` to output ✅

#### Key Finding: ALL Drops Are `too_old` (TSBPD Expiry)

After the fix, we can now see the drop classification:
```
⚠ DROPS: +19 (too_old: 19, already_ack: 0, dupes: 0, store_fail: 0)
⚠ DROPS: +20 (too_old: 20, already_ack: 0, dupes: 0, store_fail: 0)
⚠ DROPS: +22 (too_old: 22, already_ack: 0, dupes: 0, store_fail: 0)
⚠ DROPS: +24 (too_old: 24, already_ack: 0, dupes: 0, store_fail: 0)
⚠ DROPS: +13 (too_old: 13, already_ack: 0, dupes: 0, store_fail: 0)
```

**100% of drops are `too_old`** - packets arriving AFTER their TSBPD deadline!

#### Root Cause Analysis

The data shows a consistent pattern per 2-second period:
- CG sends: ~858 packets
- Server receives: ~835-845 packets
- **Delta: 13-24 packets = `too_old` drops (~2% rate)**

Ring buffer analysis from verbose output:
```
Ring: processed=5148, backlog=0, drops=0, drained=5030
```
- Ring processed: 5148 (packets pushed into ring)
- Ring drained: 5030 (packets pulled from ring)
- **Difference: 118 = exactly the final drop count!**

**Conclusion**: Packets are spending too long in the ring buffer → btree → delivery pipeline.
By the time they reach `deliverReadyPackets()`, their TSBPD time has passed.

#### Why Are Packets Late?

EventLoop metrics show:
```
║ EL Iterations                           0        19469          NEW ║
║ EL FullACK Fires                        0         1200          NEW ║
║ EL Default Runs                         0        17659          NEW ║
║ EL Idle Backoffs                        0        11716          NEW ║
```

- 11716 idle backoffs / 19469 iterations = **60% of iterations sleeping**
- That's a LOT of sleeping during a 10s test

Possible causes:

1. **Adaptive backoff sleeping too long**
   - `backoffminsleep=10µs`, `backoffmaxsleep=1ms`
   - At 430 pkt/s, inter-packet time ≈ 2.3ms
   - If we sleep 1ms when idle, we miss packets

2. **Packet arrival timing mismatch**
   - Packets may arrive in bursts
   - While EventLoop is sleeping, packets accumulate
   - By time we wake up, oldest packets are already expired

3. **TSBPD deadline is tight**
   - TSBPD latency: 3000ms (should be plenty!)
   - But if ring backlog grows, packets can exceed this

### Root Cause Found: Uneven Shard Draining

The user correctly pointed out that with a 3000ms TSBPD buffer, timing is NOT the issue.

**Key Insight**: The sharded ring's `TryRead()` always starts from shard 0. If shard 0 keeps getting refilled (packets 4, 8, 12, 16... all go to shard 0), we preferentially drain shard 0 while packets 1, 2, 3, 5, 6, 7... accumulate in shards 1, 2, 3.

**Scenario causing drops**:
1. Packets 1-8 arrive: shard0={4,8}, shard1={1,5}, shard2={2,6}, shard3={3,7}
2. `TryRead()` always tries shard 0 first → reads 4, then 8
3. More packets arrive: shard0={12}, shard1={1,5,9}, shard2={2,6,10}, shard3={3,7,11}
4. `TryRead()` reads 12 from shard 0
5. Eventually btree has: {4, 8, 12, 16...} but missing {1, 2, 3, 5, 6, 7...}
6. `contiguousScan` can't advance (waiting for packet 1)
7. Full ACK ticker fires, `drainRingByDelta()` drains ALL packets
8. NOW btree has everything, contiguous scan advances, delivery happens
9. `lastDeliveredSequenceNumber` advances to latest
10. BUT some packets from shards 1,2,3 were still in ring during step 7
11. Those packets now read as "too_old" → DROPPED

**The fix**: Round-robin shard reading (see Fix 1 below).

**Understanding the Shard Distribution**:

With 4 shards and `shard = seq % 4`:
- Shard 0: packets 4, 8, 12, 16, 20...
- Shard 1: packets 1, 5, 9, 13, 17...
- Shard 2: packets 2, 6, 10, 14, 18...
- Shard 3: packets 3, 7, 11, 15, 19...

Current `TryRead()` always tries shard 0 first. If shard 0 has data, it returns immediately without checking other shards.

**Why this causes drops**:

The `contiguousScan` can ONLY advance when it finds contiguous packets. If we read {4, 8, 12} from shard 0 before reading {1, 2, 3} from shards 1, 2, 3, the btree has gaps and `contiguousScan` is stuck.

Meanwhile, when `fullACKTicker.C` fires, it calls `drainRingByDelta()` which rapidly reads ALL remaining packets. But by this time, delivery may have already advanced via other mechanisms, causing some packets to be "too_old" when they're finally read.

### Proposed Fixes

#### Fix 1: Round-Robin Ring Reading

**Problem**: `TryRead()` always starts from shard 0, causing preferential draining of shard 0 while other shards accumulate.

**Current Code** (go-lock-free-ring/ring.go):
```go
func (r *ShardedRing) TryRead() (any, bool) {
    for i := uint64(0); i < r.numShards; i++ {  // Always starts at 0!
        if val, ok := r.shards[i].tryRead(); ok {
            return val, true
        }
    }
    return nil, false
}
```

**Proposed Fix**: Add round-robin starting position:
```go
// In ShardedRing struct:
readStartShard atomic.Uint64  // Track where to start reading

func (r *ShardedRing) TryReadRoundRobin() (any, bool) {
    start := r.readStartShard.Add(1) - 1  // Atomic increment
    for i := uint64(0); i < r.numShards; i++ {
        idx := (start + i) & r.mask  // Round-robin with mask
        if val, ok := r.shards[idx].tryRead(); ok {
            return val, true
        }
    }
    return nil, false
}
```

This ensures fair draining across all shards, preventing sequence number reordering.

#### Fix 2: EventLoop Structure Refactoring

**Problem**: Processing code is inside `default:` case, meaning it only runs when NO ticker fires.

**Current Structure**:
```go
for {
    select {
    case <-ctx.Done(): return
    case <-fullACKTicker.C: /* ACK handling */
    case <-nakTicker.C: /* NAK handling */
    default:
        // ALL processing nested here (deeply indented)
        delivered := r.deliverReadyPackets()
        processed := r.processOnePacket()
        // ... etc
    }
}
```

**Proposed Structure** (move processing after select):
```go
for {
    select {
    case <-ctx.Done():
        return
    case <-fullACKTicker.C:
        r.metrics.EventLoopFullACKFires.Add(1)
        r.drainRingByDelta()
        // ... ACK handling ...
        // Don't continue - let processing run too
    case <-nakTicker.C:
        r.metrics.EventLoopNAKFires.Add(1)
        r.drainRingByDelta()
        // ... NAK handling ...
    case <-rateTicker.C:
        r.metrics.EventLoopRateFires.Add(1)
        r.updateRateStats(now)
    default:
        // Empty - just fall through to processing
    }

    // Always runs (moved left, less nesting)
    delivered := r.deliverReadyPackets()
    processed := r.processOnePacket()
    ok, newContiguous := r.contiguousScan()
    // ... rest of processing ...

    // Adaptive backoff
    if !processed && delivered == 0 && !ok {
        time.Sleep(backoff.getSleepDuration())
    }
}
```

**Benefits**:
1. **Less nesting** - code moves left (Go style preference)
2. **Processing always happens** - not skipped when a ticker fires
3. **Separation of concerns** - tickers handle their specific task, processing is separate
4. **Potentially fewer drops** - ensures packets are always being read from ring

### Implementation Status (2025-12-28)

#### ✅ Fix 1: Round-Robin Ring Reading - DONE

Updated `go-lock-free-ring` library to v1.0.2:
```bash
go get -u github.com/randomizedcoder/go-lock-free-ring@v1.0.2
go mod tidy
go mod vendor
```

The library now uses round-robin shard selection for reads, ensuring fair draining.

#### ✅ Fix 2: EventLoop Structure Refactoring - DONE

Refactored `receive.go` EventLoop (line 2332-2443):
- Moved packet processing code from `default:` case to after the `select` statement
- `default:` case is now empty (just falls through)
- Processing runs every iteration, not just when no ticker fires
- Less nesting, cleaner code structure

### Test Results After Fixes (2025-12-28)

#### Isolation Test: `Isolation-5M-FullEventLoop`

| Metric | Control | Test | Diff |
|--------|---------|------|------|
| Packets Received | 13746 | 13415 | -2.4% |
| **Drops (too_old)** | 0 | **331** | ❌ NEW |
| RTT (µs) | 77 | **567** | +636% |
| RTT Var (µs) | 15 | 106 | +607% |
| Ring processed | - | 13746 | |
| Ring drained | - | 13415 | = 13746 - 331 |

**Key Observations**:
1. **331 drops** - all classified as `too_old` (TSBPD expiry)
2. **RTT still elevated**: 567µs vs 77µs control
3. **Ring processed vs drained delta = drops**: `13746 - 13415 = 331`
4. **No SIGSEGV** - shutdown fix works! ✅

#### Analysis: Why Drops Persist

The round-robin fix and EventLoop restructure were supposed to fix this, but drops still occur. Key questions:

1. **Is round-robin actually being used?**
   - Need to verify go-lock-free-ring version in vendor

2. **RTT gap is still large**: 567µs vs 77µs
   - Control uses traditional readfrom/writeto
   - Test uses io_uring + EventLoop
   - 490µs unexplained latency

3. **Too many EventLoop iterations?**
   ```
   EL Iterations: 47164
   EL Default Runs: 47164  (same - no ticker fires consumed iterations)
   EL Idle Backoffs: 30862 (65% of iterations sleeping)
   ```

4. **Full ACK timer vs packet arrival**:
   - 3202 full ACK fires in 30s = ~107/s
   - 429 packets/s arriving
   - 4 packets arrive per full ACK fire

**Hypothesis**: The 65% idle backoff rate suggests we're sleeping too much. When we wake up, multiple packets have accumulated, potentially causing sequence ordering issues.

### Verified: Round-Robin is Active

go-lock-free-ring v1.0.2 is in vendor with `readStartShard` rotating:
```go
func (r *ShardedRing) TryRead() (any, bool) {
    start := r.readStartShard
    r.readStartShard++  // Rotates on each read
    for i := uint64(0); i < r.numShards; i++ {
        idx := (start + i) & r.mask  // Round-robin
        ...
    }
}
```

### Root Cause Hypothesis: RTT Inflation Causes Late Packet Detection

**Observation**: RTT is 567µs (test) vs 77µs (control) - **7x higher**

**Why this matters for drops**:
1. RTT is used for TSBPD calculations
2. Higher RTT variance (106µs vs 15µs) causes unpredictable timing
3. If RTT is artificially inflated, packets may be flagged as "too_old" incorrectly

**Why is RTT higher?**

RTT = time between sending Full ACK and receiving ACKACK:
```
Receiver sends ACK  →  [transit] →  Sender receives ACK
                                    Sender sends ACKACK
Receiver gets ACKACK  ←  [transit] ←
```

**Control path** (readfrom/writeto - synchronous):
- `recv()` system call returns immediately when packet available
- `send()` system call completes immediately

**Test path** (io_uring - async with 10ms timeout):
- Receive: `WaitCQETimeout()` may add up to 10ms latency if idle
- Send: `WaitCQETimeout()` for completions may add latency
- EventLoop: 65% idle backoffs mean packets wait longer in ring

**Chain of latency sources**:
1. ACKACK takes longer to be "received" (completion handler idle time)
2. This inflates RTT measurement
3. Inflated RTT affects TSBPD timing decisions
4. Result: packets marked "too_old" when they're not actually late

### io_uring Timeout Analysis

From Prometheus metrics, io_uring receive timeouts are **very low**:
```
gosrt_iouring_listener_recv_completion_timeout_total: 3   (out of 96647)
gosrt_iouring_listener_recv_completion_success_total: 96647
```

This means `WaitCQETimeout()` is returning almost immediately (0.003% timeout rate).
**The 10ms timeout is NOT causing latency.**

### Keepalive Analysis (Unexpected Finding)

```
Control: 375112 keepalives + 13746 data = 388858 total
Test:     76497 keepalives + 13746 data =  90243 total
```

Control has **4.3x more total packets** to process! This is suspicious but likely just
because the non-io_uring path has different timing characteristics.

### Revised Root Cause Hypothesis

The 331 drops (2.4% of 13746) happen because:

1. **Ring buffer reordering**: Even with round-robin, packets may be read out of order
2. **contiguousScan can't advance** until gaps are filled
3. **Full ACK ticker fires** every 10ms, calling `drainRingByDelta()`
4. **lastACKSequenceNumber jumps** ahead (per spec: "next expected")
5. **Late packets from ring** are now "behind" lastDeliveredSequenceNumber
6. **TSBPD check** marks them as `too_old` → dropped

**Key question**: Why do packets in the ring become "too old" when TSBPD is 3000ms?

The issue isn't actual TSBPD timing - it's the `lastDeliveredSequenceNumber` check
in `deliverReadyPackets()`. Once we deliver packet N, any packet < N arriving later
is dropped as "too_old" even if its TSBPD hasn't expired.

### Root Cause Confirmed: Out-of-Order Ring Read Causes Drops

**Found in `drainRingByDelta()` (line 2084-2089)**:
```go
if seq.Lte(r.lastDeliveredSequenceNumber) {
    metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
    r.releasePacketFully(p)
    continue
}
```

**The bug sequence**:
1. Packets 1, 2, 3, 4, 5 arrive → pushed to ring (in order)
2. Round-robin `TryRead()` reads 4, 5, 3, 2, 1 (different order)
3. Packets 4, 5 inserted to btree first
4. `deliverReadyPackets()` delivers 4, 5 → `lastDeliveredSequenceNumber = 5`
5. Packet 3 read from ring → check `3 <= 5` → TRUE → **DROPPED as too_old!**
6. Packets 2, 1 also dropped

**Why round-robin doesn't help**:
Round-robin ensures fair *shard* reading, but packets are distributed across shards
by sequence number modulo. With 4 shards:
- Shard 0: seq % 4 == 0 → packets 4, 8, 12...
- Shard 1: seq % 4 == 1 → packets 1, 5, 9...
- Shard 2: seq % 4 == 2 → packets 2, 6, 10...
- Shard 3: seq % 4 == 3 → packets 3, 7, 11...

If we read shard 0 first, we get 4 before 1, 2, 3!

### Possible Fixes

#### Fix A: Read all shards before delivering (batch drain)
Accumulate packets from ring into a local buffer, sort by sequence, then process.
- Pro: Preserves order
- Con: Adds latency (batch delay), memory allocation

#### Fix B: Don't deliver out-of-order
Change `deliverReadyPackets()` to only deliver if seq == lastDelivered + 1.
- Pro: Strict ordering
- Con: Stalls delivery if gap exists (defeats purpose of TSBPD)

#### Fix C: Use single-shard ring (serialize order)
Disable sharding when order matters.
- Pro: Simple, no out-of-order
- Con: Loses parallelism benefits

#### Fix D: Track "pending in ring" separately
Don't update lastDeliveredSequenceNumber past what's been read from ring.
- Pro: Allows out-of-order btree insertion without drops
- Con: Complex tracking, may break ACK logic

#### Fix E: Sort shards by sequence before reading
Read shards in order of their lowest pending sequence.
- Pro: Maintains order with sharding
- Con: Requires scanning all shards to find min (O(shards) per read)

### ~~Recommended Fix: Fix A (Batch Sort)~~ SUPERSEDED

~~1. `drainRingByDelta()` reads ALL pending packets into local slice~~
~~2. Sort slice by sequence number~~
~~3. Process in sorted order~~

**SUPERSEDED**: After further analysis, the root cause is NOT the ring read order.
The real issue is implementation deviations from the design. See below.

---

## Root Cause Analysis: Design Deviation (2025-12-28)

### Reference: Design Documents

**Primary Design**: `documentation/ack_optimization_plan.md`
- Section 3.1: "Unified Scan Starting Point" (lines 90-102)
- Section 3.2: "Unified Scan Window Visualization" (lines 103-181)
- Section 3.3: "Benefits of Unified Contiguous Point" (lines 183-193)

**Key Design Principle** (line 100-101):
> "`gapScan` also advances `contiguousPoint` if it finds contiguous packets before the first gap"
> "Both scans start from `contiguousPoint` (no redundant re-scanning)"

### Chain of Thought Reasoning

The design document (`ack_optimization_plan.md` Section 3.1-3.2) clearly describes:
- `contiguousPoint` is the unified scan starting point
- `contiguousScan` advances ONLY when packets are CONTIGUOUS
- `gapScan` finds gaps between `contiguousPoint` and `tooRecentThreshold`
- Btree sorts packets by sequence number
- Delivery is based on TSBPD time

The design does NOT include:
- `lastDeliveredSequenceNumber` as a gating check
- "Stale gap" handling that jumps `contiguousPoint` forward

### The Chain of Issues (with code references)

**Step 1: io_uring delivers packets out of order**
- Packets arrive via io_uring completion queue
- Pushed to sharded ring in arrival order

**Step 2: Round-robin ring read brings higher seq packets to btree first**
- `TryRead()` uses round-robin starting position
- With 4 shards: seq 4,8,12... in shard 0; seq 1,5,9... in shard 1; etc.
- If shard 0 is read first, packet 4 enters btree before packets 1,2,3

**Step 3: "Stale gap" handling jumps `contiguousPoint` forward**

**File**: `congestion/live/receive.go` lines 863-877 (`contiguousScanWithTime`)
```go
const staleGapThreshold = uint32(64)

expectedNextSeq := circular.SeqAdd(lastContiguous, 1)
gapSize := circular.SeqSub(minSeq, expectedNextSeq)

if circular.SeqLess(expectedNextSeq, minSeq) && gapSize >= staleGapThreshold {
    // Large gap between contiguousPoint and btree.Min - packets were delivered
    // Advance to just before btree.Min
    lastContiguous = circular.SeqSub(minSeq, 1)
}
```

**Also present in**:
- `gapScan()` lines 984-998
- `periodicNakBtreeLocked()` lines 1640-1649

**Problem**: If only high-seq packets are in btree (e.g., btree.Min()=100, contiguousPoint=0),
this code jumps `lastContiguous` to 99, assuming packets 1-99 were already delivered.
But they weren't - they're still in the ring!

**Step 4: Full ACK ticker updates `lastACKSequenceNumber` even without progress**

**File**: `congestion/live/receive.go` lines 2354-2368
```go
} else {
    // No progress from scan, but MUST still update lastACKSequenceNumber
    // to enable packet delivery. Without this, packets accumulate in btree
    // but can't be delivered because deliverReadyPackets() checks:
    //   seq <= lastACKSequenceNumber
    //
    // BUG FIX (2025-12-26): Previously this else branch only sent ACK
    // but didn't update lastACKSequenceNumber, causing packets to expire
    // via TSBPD before delivery could happen → drops!
    currentSeq := r.contiguousPoint.Load()
    if currentSeq > 0 {
        r.lastACKSequenceNumber = circular.New(currentSeq, packet.MAX_SEQUENCENUMBER)
        r.sendACK(...)
    }
}
```

**Problem**: This updates `lastACKSequenceNumber` to `contiguousPoint` even when
`contiguousScan()` returned `ok=false` (no progress). Combined with Step 3,
this can set `lastACKSequenceNumber` to a jumped-ahead value.

**Step 5: Delivery happens based on `lastACKSequenceNumber`**

**File**: `congestion/live/receive.go` lines 2552-2558
```go
removed := r.packetStore.RemoveAll(
    func(p packet.Packet) bool {
        h := p.Header()
        return h.PacketSequenceNumber.Lte(r.lastACKSequenceNumber) && h.PktTsbpdTime <= now
    },
    ...
)
```

With `lastACKSequenceNumber` jumped ahead (e.g., to 99), packets up to 99 are
delivered. But only packet 100 was in btree, so only 100 gets delivered.
`lastDeliveredSequenceNumber` becomes 100.

**Step 6: Late packets from ring are rejected as "too_old"**

**File**: `congestion/live/receive.go` - THREE locations have this check:
1. `drainPacketRing()` lines 1955-1963
2. `drainRingByDelta()` lines 2083-2090
3. `processOnePacket()` lines 2482-2490

```go
if seq.Lte(r.lastDeliveredSequenceNumber) {
    m.CongestionRecvPktBelated.Add(1)
    m.CongestionRecvByteBelated.Add(uint64(pktLen))
    metrics.IncrementRecvDataDrop(m, metrics.DropReasonTooOld, uint64(pktLen))
    r.releasePacketFully(p)
    return true // Still processed (rejected)
}
```

Packets 1-99, when finally read from ring, have `seq <= 100`, so they're dropped!

---

## What's Wrong with the "Stale Gap" Implementation

### 1. Violates Design Principle

**Design** (`ack_optimization_plan.md` Section 3.1):
> "contiguousScan runs every iteration... Advances contiguousPoint forward on contiguous arrivals"

The design explicitly says advancement happens on **contiguous arrivals**.
The stale gap handling advances on **gaps**, which is the opposite.

### 2. Incorrect Assumption

The stale gap code assumes:
> "Large gap between contiguousPoint and btree.Min - packets were delivered"

This assumption is WRONG in the io_uring + ring buffer scenario. The gap might
exist because:
- Packets are in the ring but not yet processed
- Round-robin reading brought high-seq packets to btree first
- io_uring completions arrived out of order

### 3. Creates Cascading Failures

The stale gap handling creates a domino effect:
1. `contiguousPoint` jumps ahead (incorrectly)
2. `lastACKSequenceNumber` gets updated to jumped value
3. Delivery can happen for the jumped range
4. `lastDeliveredSequenceNumber` advances
5. Packets still in ring are dropped as "too_old"

### 4. The "BUG FIX" that Made Things Worse

The comment at line 2360-2362 says the else branch was a "BUG FIX" for packets
expiring via TSBPD. But this fix only works if `contiguousPoint` is correct.
With the stale gap handling jumping `contiguousPoint`, this "fix" amplifies the problem.

---

## Proposed Fix

### ~~Option 1: Remove Stale Gap Handling~~ → REPLACED

**Status**: ~~Recommended~~ → **SUPERSEDED by TSBPD-aware advancement**

The stale gap handling addresses a real need (unrecoverable packet loss), but
uses the wrong trigger (gap size instead of TSBPD time).

**New approach**: Replace with TSBPD-aware advancement (see design document).

### Option 2: Keep Full ACK Ticker Else Branch ✅ DECIDED

**Status**: Keep for now

The else branch is still needed for periodic RTT measurement when no contiguous
progress occurred. With TSBPD-aware advancement, contiguousPoint will advance
correctly, so the else branch will work properly..m

### Option 3: Use `contiguousPoint` Instead of `lastDeliveredSequenceNumber` ✅ DECIDED

**Status**: **APPROVED - Implement this**

Replace `lastDeliveredSequenceNumber` with `contiguousPoint` checks.

**Locations to modify**:
1. `drainPacketRing()` line 1957
2. `drainRingByDelta()` line 2084
3. `processOnePacket()` line 2484

```go
// BEFORE:
if seq.Lte(r.lastDeliveredSequenceNumber) {

// AFTER:
if seq.Val() <= r.contiguousPoint.Load() {
```

**Also**: Remove `lastDeliveredSequenceNumber` field entirely:
- Line 191: Remove field declaration
- Line 296: Remove initialization
- Line 2568: Remove update in delivery function

**Rationale**: `contiguousPoint` is the unified boundary:
- Everything ≤ `contiguousPoint` is "handled" (delivered or TSBPD-skipped)
- Simplifies code, fewer variables to track

---

## Approved Implementation Plan

### Step 1: Replace `lastDeliveredSequenceNumber` with `contiguousPoint`

See Phase 2 in `contiguous_point_tsbpd_advancement_design.md`

### Step 2: Replace Stale Gap with TSBPD-Aware Advancement

See Phase 3 in `contiguous_point_tsbpd_advancement_design.md`

```go
// Replace (3 locations):
if gapSize >= staleGapThreshold {
    lastContiguous = minSeq - 1
}

// With:
if gapSize > 0 && now > minPkt.Header().PktTsbpdTime {
    lastContiguous = circular.SeqSub(minSeq, 1)
    r.metrics.CongestionRecvPktSkippedTSBPD.Add(uint64(gapSize))
}
```

### Step 3: Run Isolation Tests

Verify:
- No "too_old" drops from ring timing
- TSBPD skips tracked correctly
- RTT back to normal levels

---

## Critical Design Gap Discovered

### The "Stale Gap" Problem is Real (But Implemented Wrong)

Upon further analysis, the "stale gap" handling was addressing a **real problem**:
- If packets are **permanently lost** (not just delayed), `contiguousPoint` gets stuck
- NAK retransmissions may also be lost
- Eventually, the TSBPD deadline passes and packets are unrecoverable
- `contiguousPoint` MUST be able to advance past unrecoverable gaps

**The current implementation is wrong because**:
- It uses `gap size >= 64` as the trigger (arbitrary threshold)
- It assumes "packets were delivered" (wrong - they're in the ring!)
- It should use **TSBPD expiry time** as the authority

### New Design Document Created

**See**: `documentation/contiguous_point_tsbpd_advancement_design.md`

This document addresses:
1. **Problem Statement**: How `contiguousPoint` can get stuck forever
2. **Real-World Scenarios**: Network outages, large gaps, tens of seconds loss
3. **Proposed Algorithm**: TSBPD-aware contiguous scan
4. **Key Insight**: Use `now > minPkt.TSBPD` instead of `gap >= threshold`
5. **Edge Cases**: Long outages, reordering vs loss, clock skew, wraparound

### Comparison: Current vs Proposed

| Aspect | Current (Wrong) | Proposed (TSBPD-aware) |
|--------|-----------------|------------------------|
| **Trigger** | Gap size >= 64 | Gap AND btree.Min().TSBPD expired |
| **Assumption** | "Packets delivered" | "Packets unrecoverable" |
| **Timing** | Immediate | Waits for TSBPD deadline |
| **Correctness** | Wrong | Correct |

---

## Revised Understanding

### The Chain of Issues (Updated)

1. **io_uring out-of-order delivery** → packets in ring out of order
2. **Round-robin ring read** → high-seq packets to btree first
3. **Stale gap handling (WRONG TRIGGER)** → jumps contiguousPoint using gap size
4. **Should use TSBPD time** → only jump when btree.Min().TSBPD has expired

### Why Current Code Fails

The current code checks:
```go
if gapSize >= staleGapThreshold {  // 64 packets
    lastContiguous = minSeq - 1
}
```

It should check:
```go
if gapSize > 0 && now > minPkt.Header().PktTsbpdTime {
    // minPkt's TSBPD has passed, so ALL earlier packets are expired
    lastContiguous = minSeq - 1
}
```

---

## Design Decisions (Reviewed 2025-12-28)

1. **TSBPD-aware approach: YES** ✅
   - `now > minPkt.TSBPD` correctly indicates earlier packets are unrecoverable

2. **Skip entire gap to btree.Min()-1** ✅
   - Efficient and logically correct

3. **Full ACK ticker else branch: Keep for now** ✅
   - Still needed for periodic RTT measurement
   - Will work correctly with TSBPD-aware advancement

4. **Replace `lastDeliveredSequenceNumber` with `contiguousPoint`** ✅
   - Simplifies design - single unified boundary
   - `contiguousPoint` represents "everything handled" (delivered or skipped)

5. **Sender needs similar treatment** ⚠️ TODO
   - Sender-side TSBPD expiry handling needed
   - Create separate design document

---

## Next Steps

### Immediate (Fix Current Drops)

1. **Phase 2**: Replace `lastDeliveredSequenceNumber` with `contiguousPoint` checks
2. **Phase 3**: Replace stale gap handling with TSBPD-aware advancement
3. **Phase 6**: Run isolation tests to verify fix

### Follow-up (Sender Side)

4. **Phase 5**: Review and fix sender-side TSBPD handling

See `contiguous_point_tsbpd_advancement_design.md` for detailed implementation plan.

## Key Changes Summary

### Before (Polling + Sleep)
```go
cqe, err := ring.PeekCQE()
if err == syscall.EAGAIN {
    time.Sleep(ioUringPollInterval)  // Fixed 10ms sleep AFTER completion arrives
    continue
}
```

### After (Blocking with Timeout)
```go
cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
if err == syscall.ETIME {
    continue  // Timeout - check ctx.Done()
}
// Completion received immediately - ZERO latency!
```

### New Constants
```go
var ioUringWaitTimeout = syscall.NsecToTimespec((10 * time.Millisecond).Nanoseconds())

const (
    ioUringRetryBackoff     = 100 * time.Microsecond
    ioUringMaxGetSQERetries = 3
    ioUringMaxSubmitRetries = 3
)
```

