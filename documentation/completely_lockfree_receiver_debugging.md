# Completely Lock-Free Receiver - Debugging Log

## Overview

This document tracks issues discovered during integration testing of the completely
lock-free receiver implementation and documents the debugging process.

**Reference Documents:**
- `completely_lockfree_receiver.md` - Design document
- `completely_lockfree_receiver_implementation_plan.md` - Implementation plan
- `completely_lockfree_receiver_implementation_log.md` - Implementation log

---

## Current Status (January 13, 2026)

### Root Cause Identified ✓

**DEFECT: Control packets (ACKACK, KEEPALIVE) are NOT processed in the Receiver EventLoop**

| Component | Sender | Receiver | Issue |
|-----------|--------|----------|-------|
| Control packet processing | IN EventLoop ✓ | Separate goroutine ✗ | **Architectural mismatch** |
| Control ring drain | `processControlPacketsDelta()` | `recvControlRingLoop()` ticker | **100µs polling delay** |
| RTT impact | None | +100-300µs | **3.3x RTT inflation** |

### Symptoms

| Metric | Control | Test (LockFree) | Delta |
|--------|---------|-----------------|-------|
| RTT | 157µs | 525µs | **+234%** |
| NAKs Sent | 0 | 376 | **NEW** |
| Gaps Detected | 0 | 0 | Same |
| Retrans | 0 | 0 | Same |

### Proposed Fix

**Move control packet processing INTO the receiver's EventLoop** (like the sender does):

```go
// event_loop.go - ADD after drainRingByDelta()
controlDrained := r.processControlPackets()  // ← NEW
```

See [DEFECT: Control Packets Not Processed in Receiver EventLoop](#defect-control-packets-not-processed-in-receiver-eventloop-january-13-2026) for full details.

---

## Test Configuration

**Test:** `Isolation-5M-FullELLockFree`
- **Bitrate:** 5 Mb/s
- **Duration:** 30 seconds
- **Control:** Legacy path (Tick-based, no io_uring, list packet store)
- **Test:** Full lock-free (io_uring, EventLoop, sender+receiver control rings)

---

## Issue 1: Phantom NAKs in Lock-Free Path

### Symptoms (After Bug 2a Fix)

| Metric | Control | Test | Issue |
|--------|---------|------|-------|
| NAKs Sent | 0 | 904 | **+904 phantom NAKs** |
| Packets Received | 13746 | 13735 | -0.1% |
| Gaps Detected | 0 | 0 | Same |
| Drops | 0 | 0 | Same |
| Recovery | 100% | 100% | Same |

**Key Observation:** NAKs are sent but no actual gaps or packet loss occurs.

### Prometheus Metrics Analysis

```
# Server (Test) - NAK generation
gosrt_nak_btree_inserts_total{instance="test-server"} 918
gosrt_nak_btree_expired_early_total{instance="test-server"} 918
gosrt_nak_btree_scan_gaps_total{instance="test-server"} 918
gosrt_nak_btree_scan_packets_total{instance="test-server"} 521267

# CG (Test) - NAK reception
gosrt_connection_congestion_internal_total{type="nak_before_ack"} 918
gosrt_connection_congestion_internal_total{type="nak_not_found"} 918
gosrt_connection_congestion_packets_lost_total{direction="send"} 918
```

### Root Cause Hypothesis

The "phantom NAK" pattern is occurring:
1. Server receives packet, detects temporary "gap" due to reordering
2. Server sends NAK before packet is fully processed
3. By the time NAK reaches CG, the packet was already ACKed
4. CG can't find packet in btree → `nak_not_found` counter increments

**Why more NAKs in lock-free path?**
- io_uring completion batching may cause temporary reordering
- EventLoop processing cadence differs from Tick-based timing
- The `tooRecentThreshold` logic may not be calibrated for io_uring

### Next Steps

1. [ ] Check if `NakRecentPercent` (0.10 = 10%) is appropriate
2. [ ] Review io_uring batch processing order
3. [ ] Add metrics to track reordering distance
4. [ ] Consider increasing `NakRecentPercent` for lock-free path

---

## Issue 2: Higher RTT in Lock-Free Path

### Symptoms (After Bug 2a Fix)

| Metric | Control | Test | Diff |
|--------|---------|------|------|
| RTT (µs) | 88 | 244 | **+177%** |
| RTT Var (µs) | 18 | 48 | **+167%** |
| ACKs Sent | 2368 | 3214 | **+35.7%** |

**Metrics Fix Confirmed:**
```
# Before fix: Double-counting
gosrt_recv_control_ring_pushed_ackack_total  = 3209
gosrt_recv_control_ring_processed_ackack_total = 6418  # 2x!

# After fix: Correct
gosrt_recv_control_ring_pushed_ackack_total  = 3210
gosrt_recv_control_ring_processed_ackack_total = 3210  # ✅ Match!
```

### Current Prometheus Metrics

```
# Control
gosrt_rtt_microseconds{instance="control-server"} 88
gosrt_rtt_var_microseconds{instance="control-server"} 18

# Test
gosrt_rtt_microseconds{instance="test-server"} 244
gosrt_rtt_var_microseconds{instance="test-server"} 48
```

### Root Cause Hypothesis

The RTT is measured from ACK send → ACKACK receive. Higher RTT could mean:

1. **Control Ring Latency:** ACKACKs go through control ring → 10kHz polling adds up to 100µs latency
2. **Double Processing?** Suspicious metric:
   ```
   gosrt_recv_control_ring_pushed_ackack_total{instance="test-server"} 3209
   gosrt_recv_control_ring_processed_ackack_total{instance="test-server"} 6418  # DOUBLE!
   ```
   This suggests each ACKACK is being counted twice in `processed_ackack`.

3. **EventLoop Backoff:** The EventLoop may be sleeping when ACKACK arrives

### Investigation Areas

1. **Potential Bug:** `RecvControlRingProcessedACKACK` is incremented twice
   - Once in `drainRecvControlRing()` after `handleACKACK()`
   - Once in `handleACKACK()` itself? Need to check.

2. **Control Ring Polling Rate:** 10kHz = 100µs max latency
   - Actual network RTT should be ~100µs (loopback)
   - Control ring adds 0-100µs latency
   - Total: 100-200µs expected, but seeing 300µs+

3. **ACKACK Path:** Compare paths:
   - **Control:** io_uring recv → handlePacket → handleACKACKLocked → RTT update
   - **Test:** io_uring recv → handlePacket → dispatchACKACK → push to ring → poll → drain → handleACKACK → RTT update

### Bugs Found

#### Bug 2a: Double-counting `RecvControlRingProcessedACKACK` ✅ FIXED

**Root Cause:** Metric was incremented in two places:
1. Inside `handleACKACK()` at `connection_handlers.go:495`
2. After calling `handleACKACK()` in `drainRecvControlRing()` at `connection.go:693`

**Evidence:**
```
gosrt_recv_control_ring_pushed_ackack_total{instance="test-server"} 3209
gosrt_recv_control_ring_processed_ackack_total{instance="test-server"} 6418  # DOUBLE!
```

**Fix:** Removed duplicate increment from `handleACKACK()` at line 495.

**Note:** This bug does NOT affect RTT calculation - it only affected metric accuracy.
The actual ACKACK processing was correct, just counted twice.

### Next Steps

1. [ ] Audit `RecvControlRingProcessedACKACK` increment locations
2. [ ] Add timestamp logging to ACKACK path to measure latency components
3. [ ] Consider higher polling frequency or alternative notification mechanism
4. [ ] Compare RTT when control ring is disabled but EventLoop is enabled

---

## Issue 3: Lock Metrics Show Zero for Test Path

### Observation

```
# Control Server - Locks used
gosrt_connection_lock_acquisitions_total{lock="receiver"} 23352
gosrt_connection_lock_acquisitions_total{lock="sender"} 11922

# Test Server - No receiver/sender locks!
gosrt_connection_lock_acquisitions_total{lock="handle_packet"} 20148
gosrt_connection_lock_hold_seconds_avg{lock="receiver"} 0
gosrt_connection_lock_hold_seconds_avg{lock="sender"} 0
```

### Analysis

This is **expected and correct** for the lock-free path:
- Control uses locks for receiver (Tick-based processing)
- Test uses EventLoop (single-threaded, no locks needed)
- `handle_packet` lock is still acquired for io_uring packet routing

**Status:** ✅ Working as designed

---

## Issue 4: io_uring Send Completion Timeouts

### Observation

```
gosrt_iouring_send_completion_timeout_total{instance="test-server"} 581
gosrt_iouring_send_completion_timeout_total{instance="test-cg"} 0
```

### Analysis

Server side has 581 send completion timeouts. This might indicate:
- io_uring CQ not being drained fast enough
- Send operations taking longer than expected

**Impact:** Unclear, needs investigation.

### Next Steps

1. [ ] Check io_uring ring sizes
2. [ ] Verify CQ drain frequency

---

## Summary Table

| Issue | Severity | Status | Impact |
|-------|----------|--------|--------|
| Phantom NAKs | Medium | Investigating | Unnecessary retransmit requests |
| High RTT | High | Investigating | Affects pacing, congestion control |
| Lock metrics = 0 | N/A | Expected | Lock-free working correctly |
| io_uring timeouts | Low | Investigating | Unknown |

---

## Test Commands

```bash
# Run isolation test with prometheus output
sudo make test-isolation CONFIG=Isolation-5M-FullELLockFree PRINT_PROM=true &> /tmp/Isolation-5M-FullELLockFree

# Run with specific config
sudo make test-isolation CONFIG=Isolation-5M-RecvControlRing PRINT_PROM=true

# Compare just receiver control ring (no sender lock-free)
sudo make test-isolation CONFIG=Isolation-5M-FullEventLoop PRINT_PROM=true
```

---

## Root Cause Analysis: Why `make audit-metrics` Missed the Bug

### What the Audit Tool Does

The `metrics-audit` tool in `tools/metrics-audit/main.go` performs:
1. **Phase 1**: Parse `metrics.go` for defined metrics
2. **Phase 2**: Scan codebase for `.Add()/.Store()` calls
3. **Phase 3**: Parse `handler.go` for `.Load()` calls (Prometheus export)
4. **Phase 4**: Compare and report missing exports
5. **Phase 5**: Analyze multiple increment locations with mutual exclusion config

### Why It Missed the Double-Counting Bug

**Issue**: `RecvControlRingProcessedACKACK` was incremented in TWO places:
1. `connection_handlers.go:495` inside `handleACKACK()`
2. `connection.go:693` inside `drainRecvControlRing()` after calling `handleACKACK()`

The audit tool's **Phase 5** checks for multiple increment locations, but:

1. **Call Chain Blindness**: The tool only looks at **function-level grouping**, not **call chains**.
   It doesn't know that `drainRecvControlRing()` CALLS `handleACKACK()`, making both increments
   happen in the SAME execution path.

2. **Same Group = OK (False Assumption)**: When multiple locations are in the same "group"
   (or no group), the tool assumes they're mutually exclusive alternatives (e.g., EventLoop vs Tick).
   But in this case, both were in EventLoop mode - one called the other!

3. **Missing Group Configuration**: The new receiver control ring code paths (`handleACKACK`,
   `drainRecvControlRing`) weren't added to `mutual_exclusion.yaml`, so neither was assigned
   to a group.

4. **New Code**: This was newly written code during Phase 5/6 implementation - no historical
   baseline to catch regressions against.

### Gaps in Audit Coverage

The audit tool may miss these patterns:

| Pattern | Example | Why Missed |
|---------|---------|------------|
| **Caller/Callee double-count** | A() calls B(), both increment same metric | No call-chain analysis |
| **New code without config** | New functions not in mutual_exclusion.yaml | Config is manual |
| **Same-path increments** | Loop increments + summary increment | Assumes function-level exclusivity |
| **Complex call chains** | A→B→C where A and C both increment | Only pairwise comparison |

### Recommended Improvements to Audit Tool

1. **Add call-chain analysis**: Use `go/callgraph` to detect when one increment location calls another
2. **Require group assignment**: Fail audit if a metric has multiple locations without groups
3. **Add test-time verification**: Instrument metrics to detect runtime double-counting
4. **Auto-detect new functions**: Flag new functions that aren't in any group

### Immediate Mitigation

Update `mutual_exclusion.yaml` to include the new receiver control ring functions:

```yaml
# Add to groups section
recv_control_ring:
  description: "Receiver control ring processing"
  functions:
    - "drainRecvControlRing"
    - "handleACKACK"
    - "handleKeepAliveEventLoop"
```

---

## Action Items

### Completed (P0) ✅

1. [x] ~~Fix `RecvControlRingProcessedACKACK` double-counting~~ - Fixed: removed duplicate increment

### Immediate Investigation (P0)

2. [ ] **Test ACKACK direct processing (bypass control ring)**
   - Hypothesis: Control ring polling latency causes ~150µs RTT increase
   - Test: Process ACKACK directly in `dispatchACKACK()` instead of pushing to ring
   - Expected: RTT should drop close to control (88µs)

3. [ ] **Add control ring latency metrics**
   - Time from push to pop for each packet type
   - Track: `RecvControlRingLatencyACKACKUs` (histogram)

### Short-term (P1)

4. [ ] Review phantom NAK rate and adjust `NakRecentPercent` if needed
   - Current: 904 NAKs with no actual loss
   - Hypothesis: io_uring batch arrivals cause temporary "gaps"

5. [ ] Consider ACKACK-specific fast path
   - ACKACK is timing-critical (RTT measurement)
   - Other control packets (KEEPALIVE) can use ring

### Medium-term (P2)

6. [ ] Evaluate multiple completion handler goroutines
   - Connection-affinity sharding for parallel processing
   - Would help at higher bitrates (not blocking at 5Mb/s)

7. [ ] Document expected RTT overhead from control ring architecture
   - Update design doc with measured latencies

---

## Revision History

| Date | Change |
|------|--------|
| 2026-01-13 | Initial document created from Isolation-5M-FullELLockFree test results |
| 2026-01-13 | Fixed Bug 2a: Removed duplicate `RecvControlRingProcessedACKACK` increment |
| 2026-01-13 | Added root cause analysis for why `make audit-metrics` missed the bug |
| 2026-01-13 | Updated `mutual_exclusion.yaml` with receiver control ring groups |

---

## Issue 5: io_uring Completion Queue Goroutine Architecture

### Current Architecture

The io_uring receive path uses a **single goroutine** per listener to process ALL completions:

```
┌─────────────────────────────────────────────────────────────────────────┐
│ recvCompletionHandler() - SINGLE GOROUTINE                             │
│                                                                         │
│  for {                                                                  │
│      cqe = ring.WaitCQETimeout()      // Block waiting for completion   │
│      processRecvCompletion()          // For EVERY packet               │
│          ├── Deserialize packet                                         │
│          ├── Lookup connection (sync.Map)                               │
│          └── conn.handlePacketDirect(p) ──┐                             │
│  }                                        │                             │
│                                           ▼                             │
│                              handlePacketDirect() {                     │
│                                  c.handlePacketMutex.Lock()  // BLOCKS  │
│                                  c.handlePacket(p)                      │
│                                  c.handlePacketMutex.Unlock()           │
│                              }                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

### How ACKACK Processing Adds Latency

**Control Path (No Control Ring) - ~88µs RTT:**
```
ACK sent → network → ACKACK arrives → io_uring CQ
    → recvCompletionHandler() (single goroutine)
    → processRecvCompletion()
    → handlePacketDirect() (acquire mutex)
    → handleACKACKLocked()
    → RTT updated immediately
```

**Test Path (With Control Ring) - ~244µs RTT:**
```
ACK sent → network → ACKACK arrives → io_uring CQ
    → recvCompletionHandler() (single goroutine)
    → processRecvCompletion()
    → handlePacketDirect() (acquire mutex)
    → dispatchACKACK()
    → push to RecvControlRing    ←── ADDITIONAL LATENCY
    → recvControlRingLoop() polls (separate goroutine)
    → handleACKACK()
    → RTT updated (delayed by ring + poll)
```

### Latency Breakdown

| Step | Control Path | Test Path | Delta |
|------|-------------|-----------|-------|
| Network RTT | ~100µs | ~100µs | 0 |
| io_uring completion | ~5µs | ~5µs | 0 |
| handlePacketDirect mutex | ~10µs | ~10µs | 0 |
| Control packet dispatch | immediate | push to ring | +50-100µs |
| EventLoop poll + drain | N/A | polling latency | +50µs |
| **Total** | **~88µs** | **~244µs** | **+156µs** |

### Potential Bottleneck: Single Completion Handler

The single `recvCompletionHandler` goroutine could be a bottleneck because:

1. **Sequential Processing**: ALL packets go through one goroutine
2. **Mutex Contention**: `handlePacketMutex` serializes packet handling
3. **Ring Push Latency**: Control packets take extra time to push to ring

### Investigation: Multiple Completion Handler Goroutines

**Concept**: Use multiple goroutines to process io_uring completions in parallel

```
                    ┌─────────────────────────────────┐
                    │      io_uring CQ (kernel)       │
                    └───────────────┬─────────────────┘
                                    │
                    ┌───────────────┴───────────────┐
                    │        Sharded Handlers       │
                    │                               │
     ┌──────────────┼──────────────┬───────────────┤
     ▼              ▼              ▼               ▼
┌─────────┐  ┌─────────┐   ┌─────────┐    ┌─────────┐
│ Handler │  │ Handler │   │ Handler │    │ Handler │
│ Shard 0 │  │ Shard 1 │   │ Shard 2 │    │ Shard 3 │
└────┬────┘  └────┬────┘   └────┬────┘    └────┬────┘
     │            │             │              │
     └──────┬─────┴──────┬──────┴───────┬──────┘
            │            │              │
     ┌──────▼──────┐  ┌──▼──┐    ┌──────▼──────┐
     │ Connection A│  │Conn B│   │ Connection C│
     └─────────────┘  └─────┘    └─────────────┘
```

### Implementation Considerations

**Option A: Connection-Affinity Sharding**
- Hash `socketId` to select handler goroutine
- Maintains packet ordering per connection
- Reduces mutex contention (each handler handles subset of connections)

**Option B: Work-Stealing Queue**
- Multiple handlers steal from shared queue
- Higher throughput but more complex
- Potential ordering issues

**Option C: Eliminate Control Ring for ACKACK**
- ACKACK is timing-critical (RTT measurement)
- Process ACKACK directly in completion handler (no ring)
- Keep other control packets going through ring

### Metrics to Monitor

Current test shows:
```
gosrt_connection_lock_hold_seconds_avg{lock="handle_packet"} 0.000009938  # ~10µs
gosrt_connection_lock_wait_seconds_avg{lock="handle_packet"} 0.0000001293 # ~0.1µs
gosrt_iouring_listener_recv_completion_success_total = 20148
```

Lock contention is low (~0.1µs wait), suggesting:
- Mutex is not the bottleneck
- **Control ring polling latency** is the likely culprit

---

## Summary of Fixes Applied

### Bug 2a Fix: Double-Counting `RecvControlRingProcessedACKACK`

**File:** `connection_handlers.go`
**Change:** Removed `.Add(1)` from `handleACKACK()` function

**Before:**
```go
if c.metrics != nil {
    c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
    c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
    c.metrics.RecvControlRingProcessedACKACK.Add(1)  // BUG: Double-counted!
}
```

**After:**
```go
if c.metrics != nil {
    c.metrics.AckBtreeEntriesExpired.Add(uint64(expiredCount))
    c.metrics.AckBtreeSize.Store(uint64(btreeLenAfter))
    // Note: RecvControlRingProcessedACKACK is incremented by drainRecvControlRing()
    // after this function returns, not here (to avoid double-counting)
}
```

**Impact:** Metric now correctly shows actual ACKACK count (was showing 2x before).

### Audit Tool Enhancement

**File:** `tools/metrics-audit/mutual_exclusion.yaml`
**Change:** Added `recv_control_ring` and `recv_control_direct` groups

This ensures future double-counting issues in this code path will be flagged.

---

## Next Steps

1. Re-run isolation test to verify metrics are now correct
2. Investigate remaining issues (phantom NAKs, high RTT variance)
3. **NEW**: Implement multiple io_uring rings - see `multi_iouring_design.md`

```bash
sudo make test-isolation CONFIG=Isolation-5M-FullELLockFree PRINT_PROM=true &> /tmp/Isolation-5M-FullELLockFree-fixed
```

---

## Multi io_uring Design

**Document**: `multi_iouring_design.md`

**Problem**: The single completion handler goroutine may be a bottleneck, contributing to RTT inflation.

**Solution**: Support configurable multiple io_uring rings, each with its own completion handler:

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

**New Configuration**:
```go
IoUringRecvRingCount int  // Number of receive rings (default: 1)
IoUringSendRingCount int  // Number of send rings per connection (default: 1)
```

**Expected Improvement**: ~30% RTT reduction with 4 rings at high packet rates.

---

## Proposed Debugging Plan

### Experiment 1: Bypass Control Ring for ACKACK

**Hypothesis**: The control ring polling latency (~100µs) is causing the RTT increase.

**Approach**: Modify `dispatchACKACK()` to process ACKACK directly instead of pushing to ring.

**Current Code** (`connection_handlers.go`):
```go
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    if c.recvControlRing != nil {
        // Push to control ring for EventLoop processing
        cp := receive.RecvControlPacket{...}
        if c.recvControlRing.Push(cp) { ... }
    }
    // Fallback to locked handler
    c.handleACKACKLocked(p)
}
```

**Proposed Change**:
```go
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    // EXPERIMENT: Process ACKACK directly (bypass ring for timing-critical RTT)
    if c.useEventLoop && c.recvControlRing != nil {
        // Direct processing - no ring latency
        c.handleACKACK(ackNum, arrivalTime)  // Lock-free (EventLoop context)
        return
    }
    c.handleACKACKLocked(p)
}
```

**Expected Result**: RTT should drop from ~244µs to ~100µs (close to control).

**Risk**: May violate EventLoop single-consumer guarantee if io_uring handler and EventLoop both call handleACKACK.

### Experiment 2: Add Latency Tracing

**Approach**: Add timestamps at key points to measure where latency accumulates.

```go
// In dispatchACKACK - record push time
pushTime := time.Now()
if c.recvControlRing.Push(cp) {
    c.metrics.RecvControlRingPushTimeUs.Store(uint64(pushTime.UnixMicro()))
}

// In drainRecvControlRing - record pop time and calculate latency
popTime := time.Now()
latencyUs := popTime.Sub(time.Unix(0, cp.Timestamp))
c.metrics.RecvControlRingLatencyUs.Add(uint64(latencyUs.Microseconds()))
```

### Experiment 3: Multiple io_uring Rings (RECOMMENDED)

**Goal**: Parallelize packet processing without adding locks to ACKACK handling.

**User Requirement**: No new Go channels.

#### Why Can't Multiple Goroutines Read the Same CQ?

Looking at `giouring/lib.go`:

```go
// internalPeekCQE - NOT thread-safe!
func internalPeekCQE(ring *Ring, nrAvailable *uint32) (*CompletionQueueEvent, error) {
    // ...
    head := *ring.cqRing.head  // NON-ATOMIC read!
    // ...
    cqe = (*CompletionQueueEvent)(
        unsafe.Add(..., uintptr((head&mask)<<shift)*...))  // Uses same head
    // ...
}

// CQAdvance - read-modify-write is NOT atomic!
func (ring *Ring) CQAdvance(numberOfCQEs uint32) {
    atomic.StoreUint32(ring.cqRing.head, *ring.cqRing.head+numberOfCQEs)
    //                                    ^^^^^^^^^^^^^^^^^ NON-ATOMIC read
}
```

**Race Condition with Multiple Readers**:
```
Time    Goroutine 1              Goroutine 2
────    ───────────              ───────────
T1      head := *cqRing.head     (waiting)
        (head = 5)
T2      (processing CQE[5])      head := *cqRing.head
                                 (head = 5)  ← SAME CQE!
T3      CQAdvance(1)             (processing CQE[5])
        store head=6
T4                               CQAdvance(1)
                                 read head=6, store head=7

Result: CQE[5] processed TWICE, CQE[6] SKIPPED!
```

**Conclusion**: io_uring CQ is **single-consumer by kernel/liburing design**.

#### Solution: Multiple io_uring Rings

Instead of multiple consumers per ring, create **multiple rings** - each with its own CQ reader!

```
UDP Socket (fd=5)
       │
       │  Submit N/2 recvmsg operations to each ring
       │
       ├──────────────────────────────────────────┐
       ▼                                          ▼
┌─────────────────────┐                  ┌─────────────────────┐
│   io_uring Ring 0   │                  │   io_uring Ring 1   │
│   (64 pending recv) │                  │   (64 pending recv) │
└─────────┬───────────┘                  └─────────┬───────────┘
          │                                        │
          ▼                                        ▼
┌─────────────────────┐                  ┌─────────────────────┐
│ CQ Handler 0        │                  │ CQ Handler 1        │
│ - WaitCQETimeout()  │                  │ - WaitCQETimeout()  │
│ - processRecvComp() │                  │ - processRecvComp() │
│ - resubmit to Ring0 │                  │ - resubmit to Ring1 │
└─────────────────────┘                  └─────────────────────┘
          │                                        │
          └──────────┬─────────────────────────────┘
                     ▼
            handlePacketDirect(p)
            (connection lookup, dispatch)
```

**How it works**:
1. Create 2 (or 4) io_uring rings at listener startup
2. Submit half the pending recvmsg operations to each ring
3. Each ring has its own completion handler goroutine
4. When a packet arrives, it completes on whichever ring's recvmsg operation was used
5. Each handler processes its completions independently
6. Resubmissions go back to the SAME ring (maintains balance)

**Why this works for UDP**:
- All recvmsg operations are submitted on the SAME UDP socket (fd)
- The kernel distributes incoming packets to available recvmsg operations
- Completions go to the ring that submitted the operation
- Natural load balancing - busier ring processes more, idle ring waits

#### Architecture Comparison

**Current Architecture (Single Ring, Single Handler)**:
```
                    io_uring Ring 0
                           │
                    64 pending recvmsg
                           │
                           ▼
            ┌──────────────────────────────┐
            │   recvCompletionHandler      │
            │   (SINGLE goroutine)         │
            │   1. WaitCQETimeout()        │
            │   2. processRecvCompletion() │
            │   3. handlePacketDirect(p)   │ ◄── SERIALIZED!
            │   4. resubmit recvmsg        │
            └──────────────────────────────┘
                           │
            For ACKACK: pushes to RecvControlRing
                           │
            ┌──────────────────────────────┐
            │   recvControlRingLoop        │
            │   (polls ring, ~150µs delay) │ ◄── LATENCY SOURCE
            │   calls handleACKACK()       │
            └──────────────────────────────┘
```

**Proposed Architecture (Multiple Rings, Multiple Handlers)**:
```
                    UDP Socket (fd=5)
                           │
       ┌───────────────────┴───────────────────┐
       ▼                                       ▼
┌─────────────────┐                   ┌─────────────────┐
│ io_uring Ring 0 │                   │ io_uring Ring 1 │
│ 32 pending recv │                   │ 32 pending recv │
└────────┬────────┘                   └────────┬────────┘
         │                                     │
         ▼                                     ▼
┌─────────────────┐                   ┌─────────────────┐
│ Handler 0       │                   │ Handler 1       │
│ WaitCQETimeout()│                   │ WaitCQETimeout()│
│ processRecv()   │ ◄── PARALLEL! ──► │ processRecv()   │
│ handlePacket()  │                   │ handlePacket()  │
│ resubmit→Ring0  │                   │ resubmit→Ring1  │
└─────────────────┘                   └─────────────────┘
         │                                     │
         └──────────────┬──────────────────────┘
                        ▼
              For ACKACK: handleACKACK() directly!
              (no control ring needed - handler IS the context)
```

#### Key Benefits

1. **True parallelism**: Each handler has its own ring - no shared CQ state
2. **No new Go channels**: Direct function calls within each handler
3. **No locks needed for ACKACK**: Handler goroutine owns its processing context
4. **Natural load balancing**: Kernel distributes packets to available recvmsg operations
5. **Same connection routing**: Packets for same connection may go to different handlers, but `handlePacketMutex` ensures serialization per-connection
6. **Preserved architecture**: Each handler follows the existing completion handler pattern

#### Detailed Design for 2 Rings

```go
// listen_linux.go - Modified structures

// In listener struct
type listener struct {
    // ... existing fields ...

    // Multiple io_uring rings for parallel receive processing
    numRecvRings     int                      // Number of rings (default: 1, configurable: 2 or 4)
    recvRings        []interface{}            // []*giouring.Ring
    recvRingFds      []int                    // File descriptors for each ring
    recvCompletions  []map[uint64]*recvCompletionInfo  // Per-ring completion maps
    recvCompLocks    []sync.Mutex             // Per-ring locks for completion maps
    recvRequestIDs   []atomic.Uint64          // Per-ring request ID counters
    recvCompWgs      []sync.WaitGroup         // Per-ring completion handler wait groups
}

// Initialize multiple rings in setupIoUringRecv
func (ln *listener) setupIoUringRecvMulti(numRings int) error {
    ln.numRecvRings = numRings
    ln.recvRings = make([]interface{}, numRings)
    ln.recvRingFds = make([]int, numRings)
    ln.recvCompletions = make([]map[uint64]*recvCompletionInfo, numRings)
    ln.recvCompLocks = make([]sync.Mutex, numRings)
    ln.recvRequestIDs = make([]atomic.Uint64, numRings)
    ln.recvCompWgs = make([]sync.WaitGroup, numRings)

    ringSize := ln.config.IoUringRecvRingSize
    pendingPerRing := ln.config.IoUringRecvInitialPending / numRings

    for i := 0; i < numRings; i++ {
        // Create ring
        ring := giouring.NewRing()
        if err := ring.QueueInit(uint32(ringSize), 0); err != nil {
            return fmt.Errorf("failed to create ring %d: %w", i, err)
        }
        ln.recvRings[i] = ring
        ln.recvRingFds[i] = ln.recvRingFd  // Same UDP socket fd for all rings!
        ln.recvCompletions[i] = make(map[uint64]*recvCompletionInfo)

        // Start completion handler for this ring
        ln.recvCompWgs[i].Add(1)
        go ln.recvCompletionHandlerN(ln.ctx, i)

        // Pre-populate with pending receives
        ln.submitRecvRequestBatchN(i, pendingPerRing)
    }

    return nil
}

// Per-ring completion handler (same logic, different ring index)
func (ln *listener) recvCompletionHandlerN(ctx context.Context, ringIdx int) {
    defer ln.recvCompWgs[ringIdx].Done()

    ring := ln.recvRings[ringIdx].(*giouring.Ring)
    batchSize := ln.config.IoUringRecvBatchSize / ln.numRecvRings
    pendingResubmits := 0

    for {
        select {
        case <-ctx.Done():
            if pendingResubmits > 0 {
                ln.submitRecvRequestBatchN(ringIdx, pendingResubmits)
            }
            return
        default:
        }

        // Each handler waits on ITS OWN ring's CQ - no contention!
        cqe, compInfo := ln.getRecvCompletionN(ctx, ring, ringIdx)
        if cqe == nil {
            continue
        }

        // Process completion (same logic as single-ring)
        ln.processRecvCompletionN(ring, cqe, compInfo, ringIdx)

        pendingResubmits++
        if pendingResubmits >= batchSize {
            ln.submitRecvRequestBatchN(ringIdx, pendingResubmits)
            pendingResubmits = 0
        }
    }
}
```

#### ACKACK Handling with Multi-Ring

**Key Insight**: With multiple handlers, each handler can process ACKACK directly!

The existing `handlePacketMutex` on `srtConn` already serializes packet processing per-connection. This means:
- Handler 0 and Handler 1 may both receive packets for the same connection
- `handlePacketMutex` ensures only one processes at a time
- ACKACK processing happens in the handler context - NO control ring latency!

```go
// Modified dispatchACKACK - can process directly when in handler context
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    // With multi-ring handlers, we're already serialized by handlePacketMutex
    // Process ACKACK directly - no need for control ring indirection!
    if c.useEventLoop {
        arrivalTime := uint64(time.Now().UnixMicro())
        ackNum := p.Header().TypeSpecific
        c.handleACKACK(ackNum, arrivalTime)  // Direct call
        c.metrics.RecvControlRingProcessedACKACK.Add(1)
        p.Decommission()
        return
    }

    // Fallback for non-eventloop mode
    c.handleACKACKLocked(p)
}
```

#### Evaluation: 2 vs 4 Rings

| Aspect | 2 Rings | 4 Rings |
|--------|---------|---------|
| **Memory** | ~2x ring buffers | ~4x ring buffers |
| **Kernel Resources** | 2 io_uring instances | 4 io_uring instances |
| **CPU Cores** | Good for 2-4 core systems | Better for 8+ core systems |
| **Latency** | Lower (less kernel overhead) | Slightly higher |
| **Throughput** | ~2x baseline | ~4x baseline |
| **Pending Receives** | Split 50/50 | Split 25/25/25/25 |
| **Complexity** | Manageable | More rings to manage |

**Recommendation**: Start with **2 rings** for 5Mb/s testing:
- Sufficient parallelism for most use cases
- Lower kernel resource usage
- Easier to debug and monitor
- Scale to 4 if running on 8+ cores with high throughput needs

#### Implementation Checklist

- [ ] Add multi-ring structures to `listener` struct (arrays instead of single values)
- [ ] Implement `setupIoUringRecvMulti()` for N rings
- [ ] Implement `recvCompletionHandlerN()` per-ring handler
- [ ] Implement `getRecvCompletionN()` per-ring completion getter
- [ ] Implement `processRecvCompletionN()` per-ring processing
- [ ] Implement `submitRecvRequestBatchN()` per-ring submission
- [ ] Modify `dispatchACKACK()` to process directly (no control ring)
- [ ] Add metrics: `IoUringRecvRing0Completions`, `IoUringRecvRing1Completions`
- [ ] Update cleanup to wait for all `recvCompWgs`
- [ ] Configuration flag: `-numrecvrings` (default: 1)

#### Expected Performance Impact

| Metric | Before (1 Ring) | After (2 Rings) | After (4 Rings) |
|--------|-----------------|-----------------|-----------------|
| **RTT** | ~244µs | ~100-120µs (est.) | ~100-120µs (est.) |
| **NAKs** | 904 | ~0-50 (est.) | ~0-50 (est.) |
| **CPU Usage** | 1 core | 2 cores | 4 cores |
| **Max Throughput** | ~100Mb/s | ~200Mb/s | ~400Mb/s |
| **Control Ring** | Required (latency source) | NOT required | NOT required |

**Key improvement**: ACKACK processing happens directly in the handler - **no control ring latency!**

### Why Multiple Rings Work for UDP

**Q: Won't packets get mixed up between rings?**

No! Here's why:

1. **Same UDP socket fd**: All rings share the same file descriptor
2. **Kernel handles demux**: When a UDP packet arrives, the kernel picks ONE available recvmsg operation
3. **Completion goes to submitting ring**: The completion appears on the ring that submitted that recvmsg
4. **Resubmit to same ring**: Handler resubmits to its own ring, maintaining balance

```
Kernel UDP socket (fd=5)
       │
       │  Packet arrives
       │
       ▼
┌──────────────────────────────────────────┐
│  Kernel: "Which recvmsg is available?"   │
│                                          │
│  Ring 0: recvmsg[0], recvmsg[1], ...     │
│  Ring 1: recvmsg[32], recvmsg[33], ...   │
│                                          │
│  → Pick first available (e.g., Ring 1's  │
│    recvmsg[33])                          │
│  → Complete on Ring 1's CQ               │
└──────────────────────────────────────────┘
```

**Load Distribution**: If one handler is busy processing, the kernel uses the OTHER ring's pending operations. Natural load balancing!

### Thread Safety with Multiple Handlers

**Q: What about same-connection packets processed by different handlers?**

The existing `handlePacketMutex` on `srtConn` handles this:

```go
// connection.go - already exists
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    c.handlePacketMutex.Lock()    // Serializes per-connection
    defer c.handlePacketMutex.Unlock()
    c.handlePacket(p)
}
```

This means:
- Handler 0 receives packet for conn X → acquires mutex → processes
- Handler 1 receives next packet for conn X → waits for mutex → processes after
- **Ordering preserved** within the connection

### Recommended Order (Updated)

1. **Experiment 3 (Multiple Rings)** - True parallelism, no channels, no new locks
2. **Experiment 2 (Latency Tracing)** - If multi-ring doesn't help, trace to find bottleneck
3. **Experiment 1 (Direct ACKACK)** - Would require locking, not recommended

---

## Spurious NAK Investigation (January 13, 2026)

### Observation from Debug Logs

Running `Isolation-20M-FullELLockFree-Debug` revealed spurious NAKs:

```
Control:  NAK btree: inserts=0,   deletes=0, size=0, scan_gaps=0
Test:     NAK btree: inserts=87,  deletes=0, size=0, scan_gaps=87
          ⚠ SPURIOUS NAKs: 87 NAKs sent but 0 gaps detected!

Test:     NAK btree: inserts=173, deletes=0, size=0, scan_gaps=173
          ⚠ SPURIOUS NAKs: 173 NAKs sent but 0 gaps detected!
```

### Key Metrics

| Metric | Control | Test (LockFree) | Issue |
|--------|---------|-----------------|-------|
| NAKs Sent | 0 | 376 | **Phantom NAKs** |
| scan_gaps | 0 | 173+ | **Spurious gaps found** |
| deletes | 0 | 0 | **NAK entries never cleaned** |
| Gaps Detected | 0 | 0 | **No real gaps** |
| Retrans Received | 0 | 0 | **No actual loss** |
| RTT | 157µs | 525µs | **3.3x higher** |

### Root Cause Hypothesis: Race Between NAK Scan and Packet Insertion

**Lock-Free Path Flow:**
```
io_uring completion                    EventLoop (separate goroutine)
       │                                      │
       ▼                                      ▼
handlePacketDirect()                   periodicNakBtree()
       │                                      │
       │ (acquires handlePacketMutex)         │ (scans btree for gaps)
       │                                      │
       ▼                                      ▼
recv.Push() ─────────────────────────► btree lookup
       │                                      │
       │                                      │ GAP DETECTED! (packet not yet in btree)
       │                                      │
       ▼                                      ▼
packet added to btree                  NAK sent (spurious!)
```

**The Problem:**
1. io_uring receives packet N
2. EventLoop runs NAK scan (sees gap: packet N not in btree yet)
3. NAK scan reports gap, inserts to NAK btree
4. Packet N gets added to btree (too late!)
5. NAK is sent for packet N (which is actually received)

### Why Control Path Doesn't Have This Issue

**Control Path (Tick-based):**
- Packets processed via `networkQueue` channel
- `Tick()` timer is slower (~10ms interval)
- More time for packets to be inserted before NAK scan
- Natural synchronization via channel ordering

**Lock-Free Path:**
- EventLoop runs at high frequency (~10kHz for control ring)
- NAK scan may execute between packet arrival and btree insertion
- No channel synchronization to serialize operations

### Potential Fixes

**Option 1: Synchronize NAK Scan with Packet Insertion**
- Add a brief window after packet arrival before NAK scan
- Could use `lastPacketTime` to skip NAK scan if recent packet

**Option 2: NAK Confirmation Delay**
- Don't send NAK immediately when gap found
- Re-check after brief delay (e.g., 100µs)
- Only send NAK if gap persists

**Option 3: Atomic "Packet In Flight" Counter**
- Track packets currently being processed
- Skip NAK scan if packets are in flight

**Option 4: Use handlePacketMutex in NAK Scan**
- NAK scan acquires read lock on packet processing
- Ensures all pending packets are in btree before scan
- May add contention, but ensures correctness

### Recommended Next Step

Add debug logging to confirm the race:
```go
// In recv.Push()
r.logFunc("receiver:push:seq", func() string {
    return fmt.Sprintf("PUSH seq=%d, now=%d", seq, time.Now().UnixMicro())
})

// In periodicNakBtree() when gap found
r.logFunc("receiver:nak:gap", func() string {
    return fmt.Sprintf("GAP expected=%d, actual=%d, now=%d", expectedSeq, actualSeq, now)
})
```

If push timestamp is AFTER gap detection timestamp, the race is confirmed.

---

## DEFECT: Control Packets Not Processed in Receiver EventLoop (January 13, 2026)

### Discovery

Comparing the sender and receiver EventLoop implementations revealed a critical architectural difference:

**Sender's EventLoop** (`congestion/live/send/eventloop.go` lines 83-101):
```go
for {
    // 1. Drain data ring → btree
    dataDrained := s.drainRingToBtreeEventLoop()

    // 2. Drain control ring → process ACK/NAK   ← CONTROL PACKETS IN EVENTLOOP!
    controlDrained := s.processControlPacketsDelta()

    // 3. Deliver ready packets (TSBPD)
    delivered, nextDeliveryIn := s.deliverReadyPacketsEventLoop(nowUs)

    // 4. Sleep/backoff
}
```

**Receiver's EventLoop** (`congestion/live/receive/event_loop.go` lines 98-229):
```go
for {
    r.drainRingByDelta()   // ← Only drains DATA packets

    select {
    case <-nakTicker.C:
        // NAK processing
    case <-fullACKTicker.C:
        // ACK processing
    // ...
    }

    delivered := r.deliverReadyPackets()
    processed := r.processOnePacket()

    // ← NO CONTROL PACKET PROCESSING HERE!
}
```

### The Architectural Problem

The receiver's control packets (ACKACK, KEEPALIVE) are processed in a **SEPARATE goroutine**, not in the receiver's EventLoop:

**connection.go lines 713-731:**
```go
func (c *srtConn) recvControlRingLoop(ctx context.Context) {
    ticker := time.NewTicker(100 * time.Microsecond)  // ← 10kHz polling!
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            c.drainRecvControlRing()
            return
        case <-ticker.C:
            c.drainRecvControlRing()  // ← Processes ACKACK here
        }
    }
}
```

### Why This Causes Problems

1. **Added Latency**: The 10kHz ticker adds ~100µs worst-case latency to ACKACK processing
2. **Separate Goroutine**: Control packets compete for CPU with the EventLoop goroutine
3. **Inconsistent with Sender**: The sender processes control packets IN its EventLoop (no separate goroutine)

**Timeline of ACKACK Processing (Current - WRONG):**
```
Time 0µs:     io_uring receives ACKACK
Time 1µs:     handlePacketDirect() → dispatchACKACK() → push to RecvControlRing
Time 1-100µs: ACKACK sits in ring waiting for ticker...
Time 100µs:   recvControlRingLoop ticker fires
Time 101µs:   drainRecvControlRing() → handleACKACK() → RTT updated

              RTT calculation delayed by ~100µs!
```

**Timeline of ACKACK Processing (Sender Pattern - CORRECT):**
```
Time 0µs:     io_uring receives ACK
Time 1µs:     handlePacketDirect() → dispatchACK() → push to SendControlRing
Time 2µs:     (same goroutine) EventLoop iteration
Time 3µs:     processControlPacketsDelta() → ackBtree() → packet removed

              No added latency - processed in same EventLoop iteration!
```

### Impact Analysis

| Symptom | Cause | Severity |
|---------|-------|----------|
| **RTT 3.3x higher** | 100µs polling delay added to ACKACK processing | HIGH |
| **Spurious NAKs** | High RTT → aggressive timeouts → false gap detection | HIGH |
| **CPU waste** | Separate goroutine + ticker overhead | MEDIUM |

### Proposed Fix: Control Packet Processing

#### Understanding the Architecture Difference

**Sender's control ring processes ACK/NAK** which affect the **sender's btree** (same package):
```
congestion/live/send/eventloop.go:
    s.processControlPacketsDelta()  → calls s.ackBtree() / s.nakBtree()
                                      (all in same package)
```

**Receiver's control ring processes ACKACK/KEEPALIVE** which affect the **connection's ackNumbers btree** (different package):
```
connection.go:
    c.drainRecvControlRing()  → calls c.handleACKACK()
                                (connection level, not receiver)
```

This architectural difference means we can't simply add `processControlPackets()` to the receiver's EventLoop - the ACKACK handler is on `srtConn`, not `receiver`.

#### Option A: Process in Receiver EventLoop via Callback (Complex)

Add a callback function to receiver that gets called from EventLoop:

```go
// congestion/live/receive/receiver.go
type Config struct {
    // ... existing fields ...
    OnProcessControlPackets func() int  // NEW: Callback to drain control ring
}

// congestion/live/receive/event_loop.go
for {
    // 1. NEW: Process control packets FIRST (timing-critical)
    if r.onProcessControlPackets != nil {
        controlDrained := r.onProcessControlPackets()
    }

    // 2. Drain data ring → btree
    r.drainRingByDelta()

    // ... rest of EventLoop
}

// connection.go - set the callback
recv := receive.New(receive.Config{
    OnProcessControlPackets: c.drainRecvControlRing,  // Set callback
    // ...
})
```

**Pros:** Mirrors sender pattern, control ring processed in EventLoop
**Cons:** Requires callback indirection, adds coupling

#### Option B: Direct ACKACK Processing - Bypass Ring (Simpler)

Since `handlePacketMutex` already serializes all packet processing per connection,
we can process ACKACK directly in the io_uring handler:

```go
// connection_handlers.go - Modified dispatchACKACK
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    ackNum := p.Header().TypeSpecific
    arrivalTime := time.Now()

    if c.recv.UseEventLoop() {
        // EventLoop mode: process directly (serialized by handlePacketMutex)
        c.handleACKACK(ackNum, arrivalTime)  // Lock-free version
        c.metrics.RecvControlRingProcessedACKACK.Add(1)
        p.Decommission()
        return
    }

    // Tick mode: use locked version
    c.handleACKACKLocked(p)
}
```

**Pros:** Simple, no ring latency at all, no callback needed
**Cons:** ACKACK processed in io_uring handler context (not EventLoop)

**Why this is safe:**
- `handlePacketMutex` serializes all calls to `handlePacketDirect()`
- Only one goroutine processes packets for a given connection at a time
- `handleACKACK()` only touches `ackNumbers` btree (not receiver btree)

#### Option C: Hybrid - Control Ring in Receiver with Connection Callback (Recommended)

This option keeps the control ring for buffering but processes it in the receiver EventLoop:

**Step 1: Add callback to receiver Config**
```go
// congestion/live/receive/config.go
type Config struct {
    // NEW: Callback to process connection-level control packets
    // Returns number processed. Called from EventLoop before data ring drain.
    ProcessConnectionControlPackets func() int
}
```

**Step 2: Call callback in EventLoop**
```go
// congestion/live/receive/event_loop.go
for {
    // 1. Process connection control packets FIRST (timing-critical)
    controlDrained := 0
    if r.processConnectionControlPackets != nil {
        controlDrained = r.processConnectionControlPackets()
    }

    // 2. Drain data ring → btree
    r.drainRingByDelta()

    // ... rest of EventLoop

    // Update backoff to include control packets
    if !processed && delivered == 0 && !ok && controlDrained == 0 {
        time.Sleep(backoff.getSleepDuration())
    }
}
```

**Step 3: Set callback in connection.go**
```go
// connection.go - newSRTConn() or similar
recvConfig := receive.Config{
    // ... existing fields ...
    ProcessConnectionControlPackets: c.drainRecvControlRing,
}
```

**Step 4: Remove separate goroutine**
```go
// connection.go ticker() - REMOVE:
if c.recvControlRing != nil {
    eventLoopWg.Add(1)
    go func() {
        defer eventLoopWg.Done()
        c.recvControlRingLoop(ctx)  // DELETE THIS
    }()
}
```

### Tick() Path - How Control Packets Are Processed

For **Tick mode (non-EventLoop)**, control packets are processed via the locked path:

```go
// connection_handlers.go - dispatchACKACK
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    if c.recvControlRing != nil && c.recv.UseEventLoop() {
        // EventLoop mode: push to ring for EventLoop processing
        c.recvControlRing.PushACKACK(ackNum, arrivalTime)
        return
    }

    // Tick mode (or EventLoop disabled): use LOCKED version
    c.handleACKACKLocked(p)  // Acquires c.ackLock
}
```

**Flow in Tick mode:**
```
io_uring handler → dispatchACKACK() → handleACKACKLocked()
                                              │
                                              ▼
                                      c.ackLock.Lock()
                                      c.handleACKACK()
                                      c.ackLock.Unlock()
```

This matches the sender pattern where:
- EventLoop: lock-free functions
- Tick: locked wrapper functions

### Files to Modify

| File | Changes | Mode |
|------|---------|------|
| `congestion/live/receive/config.go` | Add `ProcessConnectionControlPackets` callback field | Both |
| `congestion/live/receive/receiver.go` | Store callback in receiver struct | Both |
| `congestion/live/receive/event_loop.go` | Call callback at start of loop | EventLoop |
| `connection.go` | Set callback when creating receiver | Both |
| `connection.go` | Remove `recvControlRingLoop()` goroutine | EventLoop |
| `connection_handlers.go` | Update dispatch logic for Tick vs EventLoop | Both |

### Expected Results After Fix

| Metric | Before Fix | After Fix | Improvement |
|--------|------------|-----------|-------------|
| RTT | 525µs | ~150µs | ~3.5x better |
| NAKs Sent | 376 | ~0-10 | ~97% reduction |
| Control Ring Latency | ~100µs | ~1µs | ~100x better |

### Relationship to Other Issues

This fix addresses BOTH identified issues:

1. **High RTT (3.3x)**: Eliminated by processing ACKACK in EventLoop (no polling delay)
2. **Spurious NAKs**: Should be greatly reduced once RTT is correct (proper timeout calculations)

The earlier hypothesis about race conditions in `pushToRing()` may be a secondary issue, but the primary cause is the architectural difference between sender and receiver EventLoops.

---

## Multi-Ring Implementation (Deferred)

The multi-ring implementation is deferred until the control packet processing defect is fixed.
The RTT improvement from multi-ring won't help if the architectural issue is causing the delay.

---

## Goroutine Pattern Audit (January 13, 2026)

### Overview

Audit of `go func() {` patterns in the codebase to identify places where a wrapper goroutine
is used unnecessarily. The preferred pattern is:

**Good Pattern:**
```go
wg.Add(1)
someFunc(ctx, &wg)  // someFunc does: defer wg.Done()
```

**Poor Pattern (wrapper goroutine):**
```go
wg.Add(1)
go func() {
    defer wg.Done()
    someFunc(ctx)  // someFunc doesn't know about wg
}()
```

### Audit Results - Core Library Files

#### Pattern A: WaitGroup with Timeout (LEGITIMATE - DO NOT CHANGE)

These use `go func() { wg.Wait(); close(done) }()` to enable timeout on WaitGroup.Wait().
This is necessary because `sync.WaitGroup.Wait()` is blocking with no timeout option.

| File | Line | Purpose |
|------|------|---------|
| `connection_linux.go` | 125 | Wait for `sendCompWg` with 2s timeout |
| `dial_io.go` | 203 | Wait for `connWg` with timeout |
| `dial_io.go` | 224 | Wait for `recvCompWg` with timeout |
| `connection_lifecycle.go` | 165 | Wait for `connWg` with 5s timeout |
| `listen_linux.go` | 174 | Wait for `recvCompWg` with 2s timeout |
| `dial_linux.go` | 86 | Wait for `recvCompWg` with 2s timeout |
| `listen_lifecycle.go` | 79 | Wait for `connWg` with timeout |
| `listen_lifecycle.go` | 100 | Wait for `recvCompWg` with timeout |

**Example (CORRECT):**
```go
done := make(chan struct{})
go func() {
    c.connWg.Wait()
    close(done)
}()
select {
case <-done:
    // Success
case <-time.After(5 * time.Second):
    // Timeout
}
```

#### Pattern B: Fire-and-Forget Callback (LEGITIMATE - DO NOT CHANGE)

| File | Line | Purpose |
|------|------|---------|
| `connection_lifecycle.go` | 191 | Fire-and-forget `c.onShutdown(c.socketId)` |

**Rationale:** The callback runs asynchronously and we don't wait for it.

#### Pattern C: HTTP Server Start (LEGITIMATE - DO NOT CHANGE)

| File | Line | Purpose |
|------|------|---------|
| `server.go` | 235 | Start HTTP metrics server |

**Rationale:** Standard pattern for HTTP servers - `ListenAndServe()` blocks.

#### Pattern D: Inline Read Loops (COULD BE REFACTORED)

| File | Line | Purpose |
|------|------|---------|
| `listen.go` | 263 | ReadFrom loop for non-io_uring path |
| `dial.go` | 200 | ReadFrom loop for non-io_uring path |

**Example:**
```go
go func() {
    defer func() { ln.log("listen", func() string { return "ReadFrom goroutine exited" }) }()
    for {
        // ... inline loop logic ...
    }
}()
```

**Verdict:** Could extract to named function but inline logic is complex. **LOW PRIORITY.**

#### Pattern E: Timeout Handler (LEGITIMATE - DO NOT CHANGE)

| File | Line | Purpose |
|------|------|---------|
| `dial.go` | 292 | Handshake timeout handler with select |

**Rationale:** Timeout handler with complex select logic - appropriate for inline.

#### Pattern F: POOR PATTERN - Wrapper Goroutine (SHOULD REFACTOR)

| File | Line | Called Function | Function File | Function Line |
|------|------|-----------------|---------------|---------------|
| `connection.go` | 612 | `sendHSRequests(ctx)` | `connection_handshake.go` | 264 |
| `connection.go` | 621 | `sendKMRequests(ctx)` | `connection_keymgmt.go` | 147 |

**Current Code (POOR):**
```go
// connection.go:611-615
c.connWg.Add(1)
go func() {
    defer c.connWg.Done()
    c.sendHSRequests(hsrequestsCtx)
}()

// connection.go:620-624
c.connWg.Add(1)
go func() {
    defer c.connWg.Done()
    c.sendKMRequests(kmrequestsCtx)
}()
```

### Proposed Refactoring

#### Step 1: Modify `sendHSRequests` signature

**File:** `connection_handshake.go`
**Line:** 264

**Before:**
```go
func (c *srtConn) sendHSRequests(ctx context.Context) {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    // ...
}
```

**After:**
```go
func (c *srtConn) sendHSRequests(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()

    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    // ...
}
```

#### Step 2: Modify `sendKMRequests` signature

**File:** `connection_keymgmt.go`
**Line:** 147

**Before:**
```go
func (c *srtConn) sendKMRequests(ctx context.Context) {
    // ...
}
```

**After:**
```go
func (c *srtConn) sendKMRequests(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()
    // ...
}
```

#### Step 3: Update call sites in `connection.go`

**File:** `connection.go`
**Lines:** 611-624

**Before:**
```go
c.connWg.Add(1)
go func() {
    defer c.connWg.Done()
    c.sendHSRequests(hsrequestsCtx)
}()

if c.crypto != nil {
    var kmrequestsCtx context.Context
    kmrequestsCtx, c.stopKMRequests = context.WithCancel(c.ctx)
    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.sendKMRequests(kmrequestsCtx)
    }()
}
```

**After:**
```go
c.connWg.Add(1)
go c.sendHSRequests(hsrequestsCtx, &c.connWg)

if c.crypto != nil {
    var kmrequestsCtx context.Context
    kmrequestsCtx, c.stopKMRequests = context.WithCancel(c.ctx)
    c.connWg.Add(1)
    go c.sendKMRequests(kmrequestsCtx, &c.connWg)
}
```

### Summary

| Pattern | Count | Action |
|---------|-------|--------|
| WaitGroup with timeout | 8 | DO NOT CHANGE |
| Fire-and-forget callback | 1 | DO NOT CHANGE |
| HTTP server start | 1 | DO NOT CHANGE |
| Inline read loops | 2 | LOW PRIORITY |
| Timeout handler | 1 | DO NOT CHANGE |
| **Poor pattern (wrapper)** | **2** | **REFACTOR** |

### Files to Modify

| File | Changes |
|------|---------|
| `connection_handshake.go:264` | Add `wg *sync.WaitGroup` param, add `defer wg.Done()` |
| `connection_keymgmt.go:147` | Add `wg *sync.WaitGroup` param, add `defer wg.Done()` |
| `connection.go:611-624` | Change to `go c.sendHSRequests(ctx, &wg)` pattern |

### Implementation Phases (When Ready)

**Phase A: Multi-Ring Infrastructure**
- Add ring arrays to listener struct
- Implement `setupIoUringRecvMulti()`
- Add per-ring completion handlers

**Phase B: Direct ACKACK Processing**
- Remove control ring push from `dispatchACKACK()`
- Call `handleACKACK()` directly in handler context
- Update metrics

**Phase C: Testing & Validation**
- Run isolation tests with `-numrecvrings=2`
- Compare RTT before/after
- Verify no packet drops or ordering issues

---

## 15. Connection Initialization and Goroutine Architecture

### 15.1 Overview

This section documents how connections are initialized and which goroutines are started in different modes.

### 15.2 Connection Startup Flow (`newSRTConn`)

When a new SRT connection is created (`newSRTConn` in `connection.go`), the following goroutines are started:

```
newSRTConn()
├── createReceiver()          // Initialize receiver (not a goroutine)
├── createSender()            // Initialize sender (not a goroutine)
├── initializeControlHandlers()
├── initializeIoUring()       // Only if IoUringRecvEnabled or IoUringEnabled
│
├── [goroutine] networkQueueReader()    // Only if !IoUringRecvEnabled
├── [goroutine] writeQueueReader()      // Only if !IoUringEnabled
│
├── Mode Decision:
│   ├── IF recv.UseEventLoop():
│   │   └── [goroutine] recv.EventLoop()   // Receiver handles packets continuously
│   │
│   └── ELSE (Tick mode):
│       └── [goroutine] ticker()           // Timer-driven processing
│
├── [goroutine] watchPeerIdleTimeout()
│
└── [HSv4 Caller Only]:
    ├── [goroutine] sendHSRequests()
    └── [goroutine] sendKMRequests()      // Only if crypto enabled
```

### 15.3 Mode Comparison: Tick vs EventLoop

#### 15.3.1 Tick Mode Architecture

```
ticker() goroutine
├── [sub-goroutine] recvControlRingLoop()   // Polls control ring at 10kHz (if recvControlRing != nil)
│   └── drainRecvControlRing()              // Processes ACKACK, KEEPALIVE
│
└── Main Loop (every c.tick interval, default 10ms):
    ├── recv.Tick(tickTime)
    │   ├── drainRingByDelta()              // IF usePacketRing: drain data packets from ring to btree
    │   ├── periodicNAK()                   // Generate and send NAKs
    │   ├── periodicACKLocked()             // Generate and send ACKs
    │   ├── expireNakEntries()              // IF useNakBtree: clean up NAK btree
    │   ├── deliverReadyPacketsLocked()     // Deliver ready packets to application
    │   └── updateRateStats()               // Rate statistics
    │
    └── snd.Tick(tickTime)
        └── tickDeliverPackets(now)
            ├── drainRingToBtree()          // IF useRing: drain data packets from ring to btree
            ├── processControlRing()        // IF controlRing != nil: process ACK/NAK from control ring
            └── tickDeliverPacketsBtree()   // OR tickDeliverPacketsList() - send ready packets
        └── tickDropOldPackets()            // Drop expired packets (too late to send)
        └── tickUpdateRateStats()           // Rate statistics
```

**What `drainRingToBtree()` does (sender):**
- Pops data packets from lock-free `packetRing` (where `Push()` writes them)
- Inserts each packet into `packetBtree` for ordered processing
- Updates metrics: `CongestionSendPktBuf`, `CongestionSendByteBuf`, `SendBtreeInserted`, etc.
- Purpose: Decouples packet arrival (from application's `Write()` → `Push()`) from packet processing (Tick)

**What `drainRingByDelta()` does (receiver):**
- Pops data packets from lock-free `packetRing` (where `Push()` writes them from network)
- Inserts each packet into `packetStore` (btree) for ordered processing
- Uses delta-based drain: `received - processed = in ring`
- Purpose: Decouples packet arrival (from io_uring/network) from packet processing (Tick)

**Key points:**
- Both receiver and sender `Tick()` are called from the same goroutine (serialized)
- Receiver control packets (ACKACK, KEEPALIVE) are processed by a **separate** `recvControlRingLoop()` goroutine
- The `recvControlRingLoop()` polls at 10kHz (100µs interval)

#### 15.3.2 EventLoop Mode Architecture (Current - With Issues)

```
recv.EventLoop() goroutine
├── Main Loop (continuous):
│   ├── drainRingByDelta()                  // Drain data packets from ring to btree
│   │
│   ├── Tickers (offset to spread work):
│   │   ├── fullACKTicker.C (10ms)          // Periodic Full ACK for RTT
│   │   │   └── drainRingByDelta() + contiguousScan() + sendACK()
│   │   ├── nakTicker.C (20ms)              // Periodic NAK scan
│   │   │   └── drainRingByDelta() + periodicNAK() + expireNakEntries()
│   │   └── rateTicker.C (1s)               // Rate statistics
│   │       └── updateRateStats()
│   │
│   ├── deliverReadyPackets()               // Deliver TSBPD-ready packets
│   ├── processOnePacket()                  // Process one packet from ring
│   ├── contiguousScan()                    // ACK scan with Light ACK
│   └── Adaptive backoff when idle
│
ticker() goroutine (STILL RUNNING!)                    ← PROBLEM: Should not run in EventLoop mode
├── [sub-goroutine] recvControlRingLoop()              ← PROBLEM: Control packets processed here
│   └── drainRecvControlRing()                         ← PROBLEM: ~100µs polling latency
│       ├── handleACKACK()                             // RTT update
│       └── handleKeepAliveEventLoop()
│
└── Main Loop (every c.tick interval):
    └── snd.Tick(tickTime)                             // ONLY SENDER TICK IS CALLED
```

**CRITICAL DISCOVERY:**

1. **Receiver EventLoop does NOT process control packets!**
   - Control packets (ACKACK, KEEPALIVE) are still processed by `recvControlRingLoop()`
   - This introduces ~100µs+ latency for ACKACK processing

2. **`ticker()` is always started!**
   - Even in EventLoop mode, `ticker()` runs but only calls `snd.Tick()`
   - The comment at line 661 states: "ticker() only drives the sender (unless sender EventLoop is also enabled)"

3. **Sender EventLoop is NEVER started!**
   - `c.snd.UseEventLoop()` is never checked
   - `c.snd.EventLoop()` is never called
   - The sender EventLoop code exists but is dead code!

### 15.4 Function Comparison: Tick vs EventLoop

#### 15.4.1 Receiver Functions

| Operation | Tick Mode | EventLoop Mode |
|-----------|-----------|----------------|
| **Data ring drain** | `drainRingByDelta()` | `drainRingByDelta()` |
| **NAK generation** | `periodicNAK()` via Tick timer | `periodicNAK()` via nakTicker (20ms) |
| **ACK generation** | `periodicACKLocked()` via Tick timer | `contiguousScan()` + `fullACKTicker` |
| **Packet delivery** | `deliverReadyPacketsLocked()` | `deliverReadyPackets()` |
| **Rate stats** | `updateRateStats()` | `updateRateStats()` via rateTicker |
| **Control packets** | `recvControlRingLoop()` (10kHz) | `recvControlRingLoop()` (10kHz) ← **SAME!** |

#### 15.4.2 Sender Functions

| Operation | Tick Mode | EventLoop Mode (NOT STARTED) |
|-----------|-----------|------------------------------|
| **Data ring drain** | `drainRingToBtree()` | `drainRingToBtreeEventLoop()` |
| **Control ring** | `processControlRing()` | `processControlPacketsDelta()` |
| **Packet delivery** | `tickDeliverPacketsBtree()` | `deliverReadyPacketsEventLoop()` |
| **Drop old packets** | `tickDropOldPackets()` | `dropOldPacketsEventLoop()` |
| **Rate stats** | `tickUpdateRateStats()` | (none - no periodic rate ticker) |

### 15.5 Architectural Issues Identified

#### Issue 1: Receiver Control Packets Not in EventLoop

**Location:** `connection.go` line 743
```go
// In ticker():
eventLoopWg.Add(1)
go c.recvControlRingLoop(ctx, &eventLoopWg)  // ← Started even in EventLoop mode
```

**Impact:**
- ACKACK processing has ~100µs+ latency from polling
- RTT calculation uses stale timestamps
- Not consistent with "completely lock-free EventLoop" design

**Fix Required:** Add control packet processing callback to receiver EventLoop (Option C from earlier analysis).

#### Issue 2: Sender EventLoop Never Started

**Location:** `connection.go` lines 592-598
```go
if c.recv.UseEventLoop() {
    c.connWg.Add(1)
    c.recv.EventLoop(c.ctx, &c.connWg)
} else {
    c.connWg.Add(1)
    c.ticker(c.ctx, &c.connWg)
}
// ← No check for c.snd.UseEventLoop() !
```

**Impact:**
- `UseSendEventLoop=true` has NO effect
- Sender always uses `Tick()` mode
- All sender EventLoop code (`eventloop.go`) is dead code

**Fix Required:** Add sender EventLoop startup:
```go
if c.recv.UseEventLoop() {
    c.connWg.Add(1)
    go c.recv.EventLoop(c.ctx, &c.connWg)

    // Check sender EventLoop separately
    if c.snd.UseEventLoop() {
        c.connWg.Add(1)
        go c.snd.EventLoop(c.ctx)  // Note: sender EventLoop doesn't take WaitGroup
    } else {
        // Still need to drive sender Tick
        c.connWg.Add(1)
        go c.senderTickLoop(c.ctx, &c.connWg)  // New function needed
    }
} else {
    c.connWg.Add(1)
    go c.ticker(c.ctx, &c.connWg)
}
```

### 15.6 Summary Table: Goroutines by Mode

| Goroutine | Tick Mode | EventLoop Mode | Notes |
|-----------|-----------|----------------|-------|
| `networkQueueReader` | ✅ (if !iouring_recv) | ✅ (if !iouring_recv) | Same |
| `writeQueueReader` | ✅ (if !iouring) | ✅ (if !iouring) | Same |
| `recv.EventLoop` | ❌ | ✅ | Receiver packet processing |
| `ticker` | ✅ | ✅ | Always runs! |
| `recvControlRingLoop` | ✅ (sub of ticker) | ✅ (sub of ticker) | **BUG: Same in both modes** |
| `snd.EventLoop` | ❌ | ❌ | **BUG: Never started!** |
| `watchPeerIdleTimeout` | ✅ | ✅ | Same |
| `sendHSRequests` | ✅ (HSv4 caller) | ✅ (HSv4 caller) | Same |
| `sendKMRequests` | ✅ (HSv4 caller+crypto) | ✅ (HSv4 caller+crypto) | Same |

### 15.7 Recommended Fixes

1. **Short-term (for current debugging):**
   - Add `ProcessConnectionControlPackets` callback to receiver EventLoop
   - Remove `recvControlRingLoop()` when receiver EventLoop is active

2. **Medium-term (complete the design):**
   - Start sender EventLoop when `c.snd.UseEventLoop()` is true
   - Create `senderTickLoop()` to drive sender Tick when receiver is in EventLoop mode but sender is not

3. **Long-term (architectural cleanup):**
   - Unify the startup pattern so both sender and receiver can independently be in Tick or EventLoop mode
   - Remove the nested `recvControlRingLoop()` from `ticker()`

---

## 16. Detailed Implementation Plan

### 16.1 Target Architecture (After Fix)

```
CASE 1: Receiver EventLoop + Sender EventLoop (fully lock-free)
─────────────────────────────────────────────────────────────────

recv.EventLoop() goroutine
├── Main Loop (continuous):
│   ├── processConnectionControlPackets()   // ← NEW: Process ACKACK/KEEPALIVE inline
│   │   └── drainRecvControlRing()
│   │       ├── handleACKACK()              // RTT update (lock-free)
│   │       └── handleKeepAliveEventLoop()  // Keep-alive (lock-free)
│   │
│   ├── drainRingByDelta()                  // Drain data packets from ring
│   │
│   ├── Tickers (offset to spread work):
│   │   ├── fullACKTicker.C (10ms)          // Periodic Full ACK
│   │   ├── nakTicker.C (20ms)              // Periodic NAK scan
│   │   └── rateTicker.C (1s)               // Rate statistics
│   │
│   ├── deliverReadyPackets()               // Deliver TSBPD-ready packets
│   ├── processOnePacket()                  // Process one packet from ring
│   ├── contiguousScan()                    // ACK scan with Light ACK
│   └── Adaptive backoff when idle

snd.EventLoop() goroutine                   // ← NEW: Actually started!
├── Main Loop (continuous):
│   ├── drainRingToBtreeEventLoop()         // Drain data packets from ring
│   ├── processControlPacketsDelta()        // Process ACK/NAK from control ring
│   ├── deliverReadyPacketsEventLoop()      // Send TSBPD-ready packets
│   ├── dropTicker.C (100ms)                // Periodic drop check
│   └── TSBPD-aware sleep / adaptive backoff

[NO ticker() goroutine]                     // ← NOT started when both use EventLoop
```

```
CASE 2: Receiver EventLoop + Sender Tick (hybrid)
──────────────────────────────────────────────────

recv.EventLoop() goroutine
├── (same as above)

senderTickLoop() goroutine                  // ← NEW function
└── Main Loop (every c.tick interval):
    └── snd.Tick(tickTime)
        ├── drainRingToBtree()              // IF useRing
        ├── processControlRing()            // IF controlRing != nil
        ├── tickDeliverPackets()
        ├── tickDropOldPackets()
        └── tickUpdateRateStats()

[NO recvControlRingLoop() - control packets handled in recv.EventLoop]
```

```
CASE 3: Receiver Tick + Sender Tick (legacy, current default)
──────────────────────────────────────────────────────────────

ticker() goroutine
├── [sub-goroutine] recvControlRingLoop()   // IF recvControlRing != nil
│   └── drainRecvControlRing()
│
└── Main Loop (every c.tick interval):
    ├── recv.Tick(tickTime)
    └── snd.Tick(tickTime)
```

### 16.2 Phase 1: Add Control Packet Processing to Receiver EventLoop

**Objective:** Process ACKACK and KEEPALIVE inline in the receiver EventLoop, eliminating the polling latency.

#### Step 1.1: Add callback to receiver Config

**File:** `congestion/live/receive/receiver.go`

```go
// In Config struct, add:
type Config struct {
    // ... existing fields ...

    // ProcessConnectionControlPackets is called by EventLoop to process
    // connection-level control packets (ACKACK, KEEPALIVE).
    // Returns number of packets processed.
    // Set by connection.go to c.drainRecvControlRing.
    ProcessConnectionControlPackets func() int
}
```

#### Step 1.2: Store callback in receiver

**File:** `congestion/live/receive/receiver.go`

```go
// In receiver struct, add:
type receiver struct {
    // ... existing fields ...

    // Callback to process connection control packets
    processConnectionControlPackets func() int
}

// In NewReceiver(), add:
func NewReceiver(config Config, ...) *receiver {
    r := &receiver{
        // ... existing fields ...
        processConnectionControlPackets: config.ProcessConnectionControlPackets,
    }
    return r
}
```

#### Step 1.3: Call callback in EventLoop

**File:** `congestion/live/receive/event_loop.go`

```go
func (r *receiver) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
    // ... existing setup ...

    for {
        r.metrics.EventLoopIterations.Add(1)

        // NEW: Process connection control packets FIRST (before data)
        // This ensures ACKACK is processed promptly for accurate RTT
        if r.processConnectionControlPackets != nil {
            controlProcessed := r.processConnectionControlPackets()
            if controlProcessed > 0 {
                r.metrics.EventLoopControlProcessed.Add(uint64(controlProcessed))
            }
        }

        // Then drain data ring (existing)
        r.drainRingByDelta()

        // ... rest of existing loop ...
    }
}
```

#### Step 1.4: Set callback in connection.go

**File:** `connection.go`

```go
// In createReceiver(), update the Config:
func createReceiver(c *srtConn) congestion.Receiver {
    recvConfig := receive.Config{
        // ... existing fields ...

        // NEW: Set callback for control packet processing
        ProcessConnectionControlPackets: nil, // Set later if EventLoop mode
    }

    recv := receive.NewReceiver(recvConfig, ...)

    // Set callback after receiver is created (if EventLoop + ControlRing enabled)
    if c.config.UseEventLoop && c.config.UseRecvControlRing {
        // Note: c.drainRecvControlRing requires c.recvControlRing which is set later
        // So we use a closure that captures c
        recv.SetProcessConnectionControlPackets(func() int {
            return c.drainRecvControlRing()
        })
    }

    return recv
}
```

Or simpler approach - set it after recvControlRing is initialized in `newSRTConn`:

```go
// In newSRTConn(), after recvControlRing initialization:
if c.recvControlRing != nil && c.recv.UseEventLoop() {
    c.recv.SetProcessConnectionControlPackets(c.drainRecvControlRing)
}
```

### 16.3 Phase 2: Fix Connection Goroutine Startup

**Objective:** Properly start sender EventLoop and remove redundant `recvControlRingLoop`.

#### Step 2.1: Refactor goroutine startup in newSRTConn

**File:** `connection.go`

**Current code (lines 592-598):**
```go
if c.recv.UseEventLoop() {
    c.connWg.Add(1)
    c.recv.EventLoop(c.ctx, &c.connWg)
} else {
    c.connWg.Add(1)
    c.ticker(c.ctx, &c.connWg)
}
```

**Proposed code:**
```go
// Determine which mode each component uses
recvUseEventLoop := c.recv.UseEventLoop()
sndUseEventLoop := c.snd.UseEventLoop()

if recvUseEventLoop && sndUseEventLoop {
    // CASE 1: Both use EventLoop - fully lock-free
    c.connWg.Add(1)
    go c.recv.EventLoop(c.ctx, &c.connWg)

    c.connWg.Add(1)
    go c.senderEventLoopWrapper(c.ctx, &c.connWg)

} else if recvUseEventLoop && !sndUseEventLoop {
    // CASE 2: Receiver EventLoop + Sender Tick (hybrid)
    c.connWg.Add(1)
    go c.recv.EventLoop(c.ctx, &c.connWg)

    c.connWg.Add(1)
    go c.senderTickLoop(c.ctx, &c.connWg)

} else {
    // CASE 3: Both use Tick (legacy)
    c.connWg.Add(1)
    go c.ticker(c.ctx, &c.connWg)
}
```

#### Step 2.2: Add senderEventLoopWrapper function

**File:** `connection.go`

```go
// senderEventLoopWrapper wraps the sender EventLoop to integrate with WaitGroup.
// The sender's EventLoop signature is EventLoop(ctx) without WaitGroup,
// so we wrap it to match our goroutine pattern.
func (c *srtConn) senderEventLoopWrapper(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()
    c.snd.EventLoop(ctx)
}
```

#### Step 2.3: Add senderTickLoop function

**File:** `connection.go`

```go
// senderTickLoop drives only the sender's Tick() in a loop.
// Used when receiver is in EventLoop mode but sender is in Tick mode.
func (c *srtConn) senderTickLoop(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()

    ticker := time.NewTicker(c.tick)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            c.log("connection:close", func() string { return "left senderTickLoop" })
            return
        case t := <-ticker.C:
            tickTime := uint64(t.Sub(c.start).Microseconds())
            c.snd.Tick(tickTime)
        }
    }
}
```

#### Step 2.4: Modify ticker() to NOT start recvControlRingLoop when receiver uses EventLoop

**File:** `connection.go`

**Current code (lines 737-765):**
```go
func (c *srtConn) ticker(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()

    var eventLoopWg sync.WaitGroup

    eventLoopWg.Add(1)
    go c.recvControlRingLoop(ctx, &eventLoopWg)  // ← Always starts!

    // ... rest of ticker ...
}
```

**Proposed code:**
```go
func (c *srtConn) ticker(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()

    var eventLoopWg sync.WaitGroup

    // Only start recvControlRingLoop if:
    // 1. Control ring is enabled
    // 2. Receiver is NOT in EventLoop mode (EventLoop handles it inline)
    if c.recvControlRing != nil && !c.recv.UseEventLoop() {
        eventLoopWg.Add(1)
        go c.recvControlRingLoop(ctx, &eventLoopWg)
    }

    ticker := time.NewTicker(c.tick)
    defer ticker.Stop()

loop:
    for {
        select {
        case <-ctx.Done():
            break loop
        case t := <-ticker.C:
            tickTime := uint64(t.Sub(c.start).Microseconds())
            c.recv.Tick(c.tsbpdTimeBase + tickTime)
            c.snd.Tick(tickTime)
        }
    }

    c.log("connection:close", func() string { return "waiting for EventLoop goroutines" })
    eventLoopWg.Wait()
    c.log("connection:close", func() string { return "left ticker loop" })
}
```

### 16.4 Phase 3: Add SetProcessConnectionControlPackets Method

**File:** `congestion/live/receive/receiver.go`

```go
// SetProcessConnectionControlPackets sets the callback for processing
// connection-level control packets in EventLoop mode.
// Called by connection.go after receiver is created.
func (r *receiver) SetProcessConnectionControlPackets(fn func() int) {
    r.processConnectionControlPackets = fn
}
```

**File:** `congestion/congestion.go` (interface)

```go
type Receiver interface {
    // ... existing methods ...

    // SetProcessConnectionControlPackets sets callback for control packet processing.
    // Only used in EventLoop mode.
    SetProcessConnectionControlPackets(func() int)
}
```

### 16.5 Summary of File Changes

| File | Changes |
|------|---------|
| `congestion/live/receive/receiver.go` | Add `ProcessConnectionControlPackets` to Config, add field to receiver, add setter method |
| `congestion/live/receive/event_loop.go` | Call `processConnectionControlPackets()` at start of each iteration |
| `congestion/congestion.go` | Add `SetProcessConnectionControlPackets` to Receiver interface |
| `congestion/live/fake.go` | Add stub implementation of `SetProcessConnectionControlPackets` |
| `connection.go` | Refactor goroutine startup, add `senderEventLoopWrapper()`, add `senderTickLoop()`, modify `ticker()` |

### 16.6 Expected Results After Fix

| Metric | Before Fix | After Fix | Improvement |
|--------|------------|-----------|-------------|
| RTT (Test vs Control) | 244µs vs 88µs (+177%) | ~100µs vs 88µs (~14%) | ~2.5x better |
| RTT Variance | 48µs vs 18µs (+167%) | ~20µs vs 18µs (~11%) | ~2.4x better |
| NAKs Sent | 904 | ~0-50 | ~18x fewer |
| Control Packet Latency | ~100µs (polling) | ~0µs (inline) | Eliminated |

### 16.7 Risk Assessment

| Risk | Mitigation |
|------|------------|
| Breaking existing Tick mode | `ticker()` unchanged when receiver not in EventLoop |
| Interface change (Receiver) | Add stub to fake.go, change is additive |
| Sender EventLoop never tested | Integration tests with `UseSendEventLoop=true` |
| Callback nil panic | Check `!= nil` before calling |

### 16.8 Testing Plan

1. **Unit Tests:**
   - Test `SetProcessConnectionControlPackets` setter
   - Test EventLoop calls callback when set
   - Test EventLoop skips callback when nil

2. **Integration Tests:**
   - New config: `Isolation-5M-FullELLockFree-Fixed` with proper goroutine startup
   - Compare RTT, NAKs, and throughput against control

3. **Regression Tests:**
   - Verify existing `Isolation-5M-HighPerf` still passes (uses Tick mode)
   - Verify `Isolation-5M-EventLoop` (receiver EventLoop only) still works

---

## 18. Implementation Progress

### 18.1 Phase 1-3 Implementation (Completed)

**Date:** 2026-01-13

**Changes Made:**

#### Phase 1: Add Control Packet Processing Callback

1. **`congestion/live/receive/ring.go`** - Added `ProcessConnectionControlPackets func() int` to Config struct

2. **`congestion/live/receive/receiver.go`** - Added `processConnectionControlPackets func() int` field and initialization

3. **`congestion/live/receive/tick.go`** - Added `SetProcessConnectionControlPackets()` method

4. **`congestion/live/receive/event_loop.go`** - Added callback invocation at start of each EventLoop iteration:
   ```go
   if r.processConnectionControlPackets != nil {
       controlProcessed := r.processConnectionControlPackets()
       if controlProcessed > 0 {
           r.metrics.EventLoopControlProcessed.Add(uint64(controlProcessed))
       }
   }
   ```

5. **`metrics/metrics.go`** - Added `EventLoopControlProcessed atomic.Uint64` metric

6. **`metrics/handler.go`** - Added Prometheus export for new metric

#### Phase 2: Fix Connection Goroutine Startup

7. **`connection.go`** - Set callback after recvControlRing initialization:
   ```go
   c.recv.SetProcessConnectionControlPackets(func() int {
       return c.drainRecvControlRingCount()
   })
   ```

8. **`connection.go`** - Refactored `drainRecvControlRing()` → `drainRecvControlRingCount()` returning int

9. **`connection.go`** - Refactored goroutine startup logic:
   - **Receiver EventLoop mode:** Start `recv.EventLoop()` + `senderTickLoop()` (NO `recvControlRingLoop`)
   - **Receiver Tick mode:** Start `ticker()` which starts `recvControlRingLoop` if needed

10. **`connection.go`** - Added new `senderTickLoop()` function

11. **`connection.go`** - Modified `ticker()` to only start `recvControlRingLoop` if `recvControlRing != nil`

#### Phase 3: Interface Changes

12. **`congestion/congestion.go`** - Added `SetProcessConnectionControlPackets(func() int)` to Receiver interface

13. **`congestion/live/fake.go`** - Added stub implementation

### 18.2 Verification

```
$ go build ./...
# Success

$ go test ./congestion/... -count=1 -timeout 180s
ok  github.com/randomizedcoder/gosrt/congestion/live          0.003s
ok  github.com/randomizedcoder/gosrt/congestion/live/common   0.003s
ok  github.com/randomizedcoder/gosrt/congestion/live/receive  56.544s
ok  github.com/randomizedcoder/gosrt/congestion/live/send     3.223s

$ go test ./metrics/... -count=1
ok  github.com/randomizedcoder/gosrt/metrics  0.306s
```

### 18.3 Architecture After Fix

**Receiver EventLoop Mode (now correct):**
```
recv.EventLoop() goroutine
├── processConnectionControlPackets()      ← NEW: Control packets processed INLINE
│   └── drainRecvControlRingCount()
│       ├── handleACKACK()                 // RTT update (lock-free)
│       └── handleKeepAliveEventLoop()     // Keep-alive (lock-free)
│
├── drainRingByDelta()                     // Data packets
├── [tickers for ACK/NAK/Rate]
├── deliverReadyPackets()
└── Adaptive backoff

senderTickLoop() goroutine                 ← NEW: Separate sender loop
└── Main Loop (every c.tick):
    └── snd.Tick(tickTime)

[NO recvControlRingLoop - control packets processed inline!]
```

### 18.4 Expected Results

| Metric | Before Fix | Expected After Fix |
|--------|------------|-------------------|
| RTT (Test vs Control) | 244µs vs 88µs (+177%) | ~100µs vs 88µs (~14%) |
| Control Packet Latency | ~100µs (polling) | ~0µs (inline) |
| NAKs Sent | 904 | ~0-50 |

### 18.5 Next Steps

1. Run integration test `Isolation-5M-FullELLockFree` to verify RTT improvement
2. If RTT still high, investigate remaining issues
3. Consider implementing sender EventLoop startup (currently dead code)

---

## 17. Function Rename Plan

### 17.1 Motivation

The current function names `Tick()` and `EventLoop()` are ambiguous when reading code - it's not immediately clear whether they belong to the sender or receiver. Renaming to include the component name makes the code self-documenting.

### 17.2 Proposed Renames

| Current Name | New Name | Component |
|--------------|----------|-----------|
| `recv.Tick()` | `recv.ReceiverTick()` | Receiver |
| `recv.EventLoop()` | `recv.ReceiverEventLoop()` | Receiver |
| `recv.UseEventLoop()` | `recv.UseReceiverEventLoop()` | Receiver |
| `snd.Tick()` | `snd.SenderTick()` | Sender |
| `snd.EventLoop()` | `snd.SenderEventLoop()` | Sender |
| `snd.UseEventLoop()` | `snd.UseSenderEventLoop()` | Sender |

### 17.3 Files Requiring Changes

#### 17.3.1 Interface Definitions

**File:** `congestion/congestion.go`

```go
// Sender interface changes:
type Sender interface {
    // ... existing methods ...

    // Old:
    // Tick(now uint64)
    // EventLoop(ctx context.Context)
    // UseEventLoop() bool

    // New:
    SenderTick(now uint64)
    SenderEventLoop(ctx context.Context)
    UseSenderEventLoop() bool
}

// Receiver interface changes:
type Receiver interface {
    // ... existing methods ...

    // Old:
    // Tick(now uint64)
    // EventLoop(ctx context.Context, wg *sync.WaitGroup)
    // UseEventLoop() bool

    // New:
    ReceiverTick(now uint64)
    ReceiverEventLoop(ctx context.Context, wg *sync.WaitGroup)
    UseReceiverEventLoop() bool
}
```

#### 17.3.2 Sender Implementation

**File:** `congestion/live/send/tick.go`
- Rename `func (s *sender) Tick(now uint64)` → `func (s *sender) SenderTick(now uint64)`

**File:** `congestion/live/send/eventloop.go`
- Rename `func (s *sender) EventLoop(ctx context.Context)` → `func (s *sender) SenderEventLoop(ctx context.Context)`
- Rename `func (s *sender) UseEventLoop() bool` → `func (s *sender) UseSenderEventLoop() bool`

#### 17.3.3 Receiver Implementation

**File:** `congestion/live/receive/tick.go`
- Rename `func (r *receiver) Tick(now uint64)` → `func (r *receiver) ReceiverTick(now uint64)`
- Rename `func (r *receiver) UseEventLoop() bool` → `func (r *receiver) UseReceiverEventLoop() bool`

**File:** `congestion/live/receive/event_loop.go`
- Rename `func (r *receiver) EventLoop(ctx context.Context, wg *sync.WaitGroup)` → `func (r *receiver) ReceiverEventLoop(ctx context.Context, wg *sync.WaitGroup)`

#### 17.3.4 Fake Implementation

**File:** `congestion/live/fake.go`
- Rename all fake implementations to match new interface names

#### 17.3.5 Connection Call Sites

**File:** `connection.go`

```go
// Line 479 (current):
go c.recv.EventLoop(c.ctx, &c.connWg)
// After:
go c.recv.ReceiverEventLoop(c.ctx, &c.connWg)

// Line 756 (current):
c.recv.Tick(c.tsbpdTimeBase + tickTime)
// After:
c.recv.ReceiverTick(c.tsbpdTimeBase + tickTime)

// Line 758 (current):
c.snd.Tick(tickTime)
// After:
c.snd.SenderTick(tickTime)
```

### 17.4 Test Files Requiring Updates

| Directory | Files | Approximate Call Sites |
|-----------|-------|------------------------|
| `congestion/live/receive/` | `receive_race_test.go` | 12 |
| `congestion/live/receive/` | `eventloop_test.go` | 21 |
| `congestion/live/receive/` | `hotpath_bench_test.go` | 1 |
| `congestion/live/receive/` | `receive_iouring_reorder_test.go` | 8 |
| `congestion/live/receive/` | `metrics_test.go` | 12 |
| `congestion/live/receive/` | `tsbpd_advancement_test.go` | 18 |
| `congestion/live/receive/` | `stream_test_helpers_test.go` | 2 |
| `congestion/live/receive/` | `receive_ring_test.go` | 5 |
| `congestion/live/receive/` | `receive_drop_table_test.go` | 2 |
| `congestion/live/receive/` | `receive_config_test.go` | 1 |
| `congestion/live/receive/` | `receive_bench_test.go` | 13 |
| `congestion/live/receive/` | `receive_basic_test.go` | 31 |
| `congestion/live/receive/` | `nak_large_merge_ack_test.go` | 18 |
| `congestion/live/receive/` | `nak_btree_scan_stream_test.go` | 22 |
| `congestion/live/receive/` | `loss_recovery_table_test.go` | 4 |
| `congestion/live/send/` | `sender_delivery_gap_test.go` | 1 |
| `congestion/live/send/` | `eventloop_test.go` | 9 |
| `congestion/live/send/` | `sender_tick_table_test.go` | 14 |
| `congestion/live/send/` | `sender_race_test.go` | 6 |
| `congestion/live/send/` | `sender_test.go` | 16 |
| `congestion/live/send/` | `nak_table_test.go` | 3 |
| `congestion/live/` | `metrics_test.go` | 4 |
| **Total** | | **~218 call sites** |

### 17.5 Implementation Order

1. **Phase A: Interface Changes**
   - Update `congestion/congestion.go` with new method names
   - This will cause compile errors everywhere

2. **Phase B: Implementation Changes**
   - Update sender implementation (`tick.go`, `eventloop.go`)
   - Update receiver implementation (`tick.go`, `event_loop.go`)
   - Update fake implementation (`fake.go`)

3. **Phase C: Call Site Updates**
   - Update `connection.go`
   - Fix all test files (use `sed` or IDE rename)

4. **Phase D: Verification**
   - `go build ./...` - should compile
   - `go test ./...` - all tests should pass

### 17.6 Automated Rename Commands

```bash
# Sender renames
find . -name "*.go" -exec sed -i 's/\.Tick(now uint64)/\.SenderTick(now uint64)/g' {} \;
find . -name "*.go" -exec sed -i 's/snd\.Tick(/snd.SenderTick(/g' {} \;
find . -name "*.go" -exec sed -i 's/send\.Tick(/send.SenderTick(/g' {} \;
find . -name "*.go" -exec sed -i 's/s\.Tick(/s.SenderTick(/g' {} \;  # In sender package only

find . -name "*.go" -exec sed -i 's/\.EventLoop(ctx context\.Context)/\.SenderEventLoop(ctx context.Context)/g' {} \;
find . -name "*.go" -exec sed -i 's/snd\.EventLoop(/snd.SenderEventLoop(/g' {} \;
find . -name "*.go" -exec sed -i 's/s\.EventLoop(/s.SenderEventLoop(/g' {} \;  # In sender package only

find . -name "*.go" -exec sed -i 's/\.UseEventLoop() bool/\.UseSenderEventLoop() bool/g' {} \;  # Sender interface only

# Receiver renames (similar pattern)
# Note: Need to be careful not to affect sender renames
```

### 17.7 Risk Assessment

| Risk | Mitigation |
|------|------------|
| Incomplete rename | Use `go build ./...` to find remaining errors |
| Breaking external code | This is an internal package, no external consumers |
| Test failures | Run full test suite after rename |
| Documentation inconsistency | Update doc comments with new names |

### 17.8 Decision

**Recommendation:** Implement rename AFTER the architectural fixes (Phases 1-3) are working, to avoid combining two large changes.

**Alternative:** Keep current names but add clear doc comments emphasizing the component ownership.

---

## 18. DEFECT: writeQueueReader Not Started When IoUringEnabled

### 18.1 Discovery (2026-01-13)

**Symptom:** Test CG with `-iouringenabled` sends `pkt_sent_data=0` (no data packets sent) even though ACK/ACKACK exchange worked.

**Root Cause:** In `connection.go` lines 479-481:
```go
if !c.config.IoUringEnabled {
    c.connWg.Add(1)
    go c.writeQueueReader(c.ctx, &c.connWg)
}
```

When `-iouringenabled` is set, `writeQueueReader` is NOT started! But there's no io_uring alternative for the write queue. The io_uring flags (`IoUringEnabled`, `IoUringRecvEnabled`) are for network I/O (recv/send operations), NOT for the internal write queue that feeds application data to the sender.

**Impact:**
- Application calls `Write()` which puts packets into `writeQueue`
- Nothing reads from `writeQueue`
- Packets never reach the sender
- `pkt_sent_data = 0` even though connection is established

### 18.2 Fix Applied

Changed from:
```go
if !c.config.IoUringEnabled {
    c.connWg.Add(1)
    go c.writeQueueReader(c.ctx, &c.connWg)
}
```

To:
```go
// writeQueueReader takes packets from application Write() calls and pushes
// them to the sender congestion control. This is ALWAYS needed regardless
// of io_uring mode - io_uring is for network I/O (recv/send), not for the
// internal write queue which feeds application data to the sender.
c.connWg.Add(1)
go c.writeQueueReader(c.ctx, &c.connWg)
```

### 18.3 Related Fixes in This Session

1. **Missing `go` for `watchPeerIdleTimeout`** (line 513→520)
   - `c.watchPeerIdleTimeout(...)` was called without `go`, blocking forever
   - Fixed: `go c.watchPeerIdleTimeout(...)`

2. **writeQueueReader not started with io_uring** (this fix)
   - Removed incorrect `if !c.config.IoUringEnabled` guard
   - writeQueueReader is now always started

### 18.4 Future Optimization: Lock-Free Sender Architecture

The current fix restores functionality, but the `writeQueue` channel adds latency. For optimal io_uring performance, we should bypass the channel entirely.

**See:** [sender_lockfree_architecture.md](sender_lockfree_architecture.md) for detailed design of:
- Current architecture with channels
- Sequence number assignment flow
- Control packet processing (ACK, NAK, KEEPALIVE)
- Retransmission mechanism
- Proposed lock-free design with atomic sequence numbers

