# Sender Lock-Free Implementation - Debugging Log

## Overview

This document tracks issues discovered during integration testing of the sender lock-free implementation after completing `sender_lockfree_implementation_log.md`.

**Reference Documents:**
- `sender_lockfree_architecture.md` - Architecture design
- `sender_lockfree_implementation_plan.md` - Implementation plan
- `sender_lockfree_implementation_log.md` - Implementation log
- `completely_lockfree_receiver_debugging.md` - Receiver debugging (similar patterns)

---

## Current Status (January 14, 2026)

### Integration Test Results

**Test:** `Isolation-5M-FullELLockFree`

| Metric | Control | Test | Issue |
|--------|---------|------|-------|
| Packets | 13746 | 13735 | ✅ Similar |
| Gaps | 0 | 0 | ✅ None |
| Recovery | 100% | 100% | ✅ Good |
| RTT (µs) | 79 | 260 | ⚠️ +229% |
| NAKs Sent | 0 | 967 | ⚠️ Phantom NAKs |

### Critical Discovery

**The sender EventLoop is NEVER started from connection.go!**

Even when `-usesendeventloop` is configured:
- `senderTickLoop()` is started → calls `snd.Tick()`
- `snd.EventLoop()` is NEVER called

**Evidence from integration test:**
```
# Test CG uses snd.Tick(), NOT snd.EventLoop()
gosrt_send_tick_runs_total{instance="test-cg"} 3202
gosrt_send_tick_delivered_packets_total{instance="test-cg"} 13735

# Sender EventLoop metrics are MISSING (never started)
# gosrt_send_eventloop_iterations_total NOT PRESENT
# gosrt_send_eventloop_delivered_total NOT PRESENT
```

---

## Issue 1: Sender EventLoop Never Started

### Root Cause

`connection.go` lines 502-518:

```go
if c.recv.UseEventLoop() {
    // CASE 1: Receiver EventLoop mode
    c.connWg.Add(1)
    go c.recv.EventLoop(c.ctx, &c.connWg)

    // Sender still needs to be driven (sender EventLoop not yet started from connection.go)
    // Use senderTickLoop to drive sender's Tick() in a separate goroutine
    c.connWg.Add(1)
    go c.senderTickLoop(c.ctx, &c.connWg)  // ← ALWAYS calls snd.Tick(), NEVER snd.EventLoop()
} else {
    // CASE 2: Receiver Tick mode (legacy)
    c.connWg.Add(1)
    go c.ticker(c.ctx, &c.connWg)  // ← Also calls snd.Tick()
}
```

### Problem Analysis

| Mode | Receiver | Sender | Issue |
|------|----------|--------|-------|
| Full EventLoop | `recv.EventLoop()` | `snd.Tick()` | ❌ Sender EventLoop NOT used |
| Full Tick | `recv.Tick()` | `snd.Tick()` | ✅ As expected |

### Missing Check

The startup code checks `c.recv.UseEventLoop()` but does NOT check `c.snd.UseEventLoop()`.

### Comparison with Receiver

**Receiver startup (correctly implemented):**
```go
if c.recv.UseEventLoop() {
    go c.recv.EventLoop(c.ctx, &c.connWg)  // ✅ EventLoop started
}
```

**Sender startup (MISSING):**
```go
// NO equivalent for sender!
// if c.snd.UseEventLoop() {
//     go c.snd.EventLoop(c.ctx)  // ❌ NEVER CALLED
// }
```

---

## Issue 2: Comparison of New() Functions

### Receiver: `congestion/live/receive/receiver.go` - `New()`

| Feature | Implementation |
|---------|---------------|
| Storage selection | `PacketReorderAlgorithm == "btree"` → btree, else list |
| Ring initialization | `if recvConfig.UsePacketRing` |
| EventLoop config | `if recvConfig.UseEventLoop` → sets `r.useEventLoop = true` |
| Goroutine start | **NOT HERE** - done in `connection.go` |

**Key point:** Receiver `New()` only initializes state. Goroutine start is in `connection.go`.

### Sender: `congestion/live/send/sender.go` - `NewSender()`

| Feature | Implementation |
|---------|---------------|
| Storage selection | `sendConfig.UseBtree` → btree, else lists |
| Ring initialization | `if sendConfig.UseSendRing` |
| Control ring | `if sendConfig.UseSendControlRing` |
| EventLoop config | `if sendConfig.UseSendEventLoop` → sets `s.useEventLoop = true` |
| Goroutine start | **NOT HERE** - should be in `connection.go` but ISN'T |

**Key point:** Sender `NewSender()` initializes state including `s.useEventLoop = true`, but the goroutine is never started from `connection.go`.

### Summary

Both `New()` functions are **similar and correct** in their structure:
- Initialize storage (btree/list)
- Initialize rings if enabled
- Set EventLoop flag if enabled
- Do NOT start goroutines (that's connection.go's job)

**The bug is in `connection.go`** which:
- ✅ Checks `recv.UseEventLoop()` and starts `recv.EventLoop()`
- ❌ Does NOT check `snd.UseEventLoop()` and never starts `snd.EventLoop()`

---

## Issue 3: Proposed Fix

### Modified Startup Logic in `connection.go`

```go
// Start processing goroutines based on receiver/sender mode
//
// CASE 1a: Both EventLoop modes
//   - recv.EventLoop() processes data AND control packets
//   - snd.EventLoop() processes data AND control packets
//
// CASE 1b: Receiver EventLoop, Sender Tick
//   - recv.EventLoop() processes data AND control packets
//   - senderTickLoop() drives sender's Tick()
//
// CASE 2: Both Tick modes (legacy)
//   - ticker() drives both recv.Tick() and snd.Tick()

if c.recv.UseEventLoop() {
    c.connWg.Add(1)
    go c.recv.EventLoop(c.ctx, &c.connWg)

    // NEW: Check sender mode
    if c.snd.UseEventLoop() {
        // CASE 1a: Both in EventLoop mode
        c.connWg.Add(1)
        go c.snd.EventLoop(c.ctx)  // ← NEW!
    } else {
        // CASE 1b: Only receiver in EventLoop
        c.connWg.Add(1)
        go c.senderTickLoop(c.ctx, &c.connWg)
    }
} else {
    // CASE 2: Both in Tick mode
    c.connWg.Add(1)
    go c.ticker(c.ctx, &c.connWg)
}
```

### Interface Change Required

The sender's `EventLoop()` currently doesn't take a `*sync.WaitGroup`:

```go
// Current:
func (s *sender) EventLoop(ctx context.Context) {

// Required (to match receiver pattern):
func (s *sender) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()
    // ...
}
```

---

## Verification Plan

### Step 1: Update Sender EventLoop signature

```go
// congestion/live/send/eventloop.go
func (s *sender) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()
    // ... existing code
}

// congestion/congestion.go
type Sender interface {
    // ...
    EventLoop(ctx context.Context, wg *sync.WaitGroup)  // Add wg parameter
}
```

### Step 2: Update connection.go startup

Add the sender EventLoop check as shown above.

### Step 3: Verify with integration test

```bash
sudo make test-isolation CONFIG=Isolation-5M-FullELLockFree PRINT_PROM=true
```

**Expected metrics after fix:**
```
gosrt_send_eventloop_iterations_total > 0  # NOW should appear
gosrt_send_eventloop_delivered_total > 0   # NOW should appear
gosrt_send_tick_runs_total = 0             # Should be 0 (not used)
```

---

## Related Issues from Receiver Debugging

The `completely_lockfree_receiver_debugging.md` documented a similar issue:

> "I found an issue that actually NewReceiver was always starting the Tick() function, and then nesting the start of the EventLoop. So I moved the start of the EventLoop out of the Tick(), up to the NewReceiver level."

This confirms the pattern: goroutine startup belongs in `connection.go`, not in `New*()` functions.

---

## Fix Applied (January 14, 2026)

### Changes Made

**1. Updated `congestion/congestion.go` - Sender interface:**
```go
// Before:
EventLoop(ctx context.Context)

// After:
EventLoop(ctx context.Context, wg *sync.WaitGroup)
```

**2. Updated `congestion/live/send/eventloop.go` - EventLoop signature:**
```go
// Added wg parameter and defer wg.Done()
func (s *sender) EventLoop(ctx context.Context, wg *sync.WaitGroup) {
    defer wg.Done()
    // ... existing code
}
```

**3. Updated `connection.go` - Startup logic:**
```go
if c.recv.UseEventLoop() {
    go c.recv.EventLoop(c.ctx, &c.connWg)

    // NEW: Check sender mode too
    if c.snd.UseEventLoop() {
        // CASE 1a: Both receiver and sender in EventLoop mode
        go c.snd.EventLoop(c.ctx, &c.connWg)
    } else {
        // CASE 1b: Receiver EventLoop + Sender Tick
        go c.senderTickLoop(c.ctx, &c.connWg)
    }
}
```

**4. Updated test files:**
- `congestion/live/send/eventloop_test.go` - All EventLoop calls now pass `&wg`
- `congestion/live/send/sender_delivery_gap_test.go` - EventLoop call now passes `&wg`

### Verification

```bash
go build ./...       # ✅ Pass
go test ./congestion/live/send/... # ✅ Pass (3.221s)
```

---

---

## Integration Test Results After Fix (January 14, 2026)

### Test: `Isolation-5M-FullELLockFree`

| Metric | Control | Test (Before Fix) | Test (After Fix) | Status |
|--------|---------|-------------------|------------------|--------|
| Packets | 13746 | 13735 | 13747 | ✅ |
| Gaps | 0 | 0 | 0 | ✅ |
| NAKs Sent | 0 | 904 → 967 | **0** | ✅ **FIXED!** |
| Retransmissions | 0 | ? | 0 | ✅ |
| Recovery | 100% | 100% | 100% | ✅ |
| RTT (µs) | 120 | 260 | 251 | ⚠️ Still 2x |

### Key Evidence: Sender EventLoop Now Running

**Before Fix (snd.Tick used):**
```
gosrt_send_tick_runs_total{instance="test-cg"} 3202
gosrt_send_eventloop_iterations_total NOT PRESENT
```

**After Fix (snd.EventLoop used):**
```
gosrt_send_eventloop_iterations_total{instance="test-cg"} 42817  ✅
gosrt_send_eventloop_started_total{instance="test-cg"} 1        ✅
gosrt_send_first_transmit_total{instance="test-cg"} 13748       ✅
gosrt_send_delivery_packets_total{instance="test-cg"} 13748     ✅
```

### Analysis

1. **NAKs Eliminated**: The phantom NAKs (904-967) were caused by the sender NOT using EventLoop mode, leading to timing mismatches. Now with proper EventLoop, **zero NAKs**.

2. **Sender EventLoop Active**: The `gosrt_send_eventloop_started_total=1` and `gosrt_send_eventloop_iterations_total=42817` confirm the sender EventLoop is now running.

3. **RTT Still Elevated**: Test RTT (251µs) is still ~2x higher than Control (120µs). This is likely due to:
   - Additional processing overhead in EventLoop path
   - io_uring completion handling latency
   - Control ring polling latency for ACKACKs

   However, this is acceptable for now since the protocol is working correctly.

---

## Summary

| Issue | Status | Notes |
|-------|--------|-------|
| Sender EventLoop never started | ✅ **FIXED** | Added check for `snd.UseEventLoop()` in connection.go |
| Phantom NAKs | ✅ **FIXED** | Zero NAKs after fix |
| RTT 2x higher than control | ⚠️ Open | Not critical - protocol works correctly |

---

## Additional Fix: Control Packet Priority Pattern in Receiver (January 14, 2026)

### Problem

The receiver EventLoop only processed control packets ONCE at the beginning of each iteration, while the sender EventLoop services control packets between EVERY major step (3 times per iteration). This pattern ensures minimal latency for ACKACK processing.

### Changes Made

**File: `congestion/live/receive/event_loop.go`**

Added control packet priority pattern matching the sender:

```go
for {
    // 1. SERVICE CONTROL FIRST
    totalControlProcessed += r.processControlPacketsWithMetrics()

    // 2. Handle tickers
    select { ... }

    // 3. SERVICE CONTROL AFTER TICKERS ← NEW
    totalControlProcessed += r.processControlPacketsWithMetrics()

    // 4. drainRingByDelta()

    // 5. SERVICE CONTROL AFTER DRAIN ← NEW
    totalControlProcessed += r.processControlPacketsWithMetrics()

    // 6. deliverReadyPackets()

    // 7. SERVICE CONTROL AFTER DELIVERY ← NEW
    totalControlProcessed += r.processControlPacketsWithMetrics()

    // 8. processOnePacket() + contiguousScan()

    // 9. Adaptive backoff (now considers totalControlProcessed)
}
```

Added helper function to reduce code duplication:

```go
func (r *receiver) processControlPacketsWithMetrics() int {
    if r.processConnectionControlPackets == nil {
        return 0
    }
    n := r.processConnectionControlPackets()
    if n > 0 && r.metrics != nil {
        r.metrics.EventLoopControlProcessed.Add(uint64(n))
    }
    return n
}
```

### Benefits

1. **Lower ACKACK latency**: Control packets processed 4x per iteration instead of 1x
2. **Better RTT accuracy**: ACKACK processed more promptly
3. **Consistent pattern**: Matches sender EventLoop design
4. **Improved backoff**: Now considers control packet activity to avoid unnecessary sleep

### Verification

```bash
go build ./...                              # ✅ Pass
go test ./congestion/live/receive/... -count=1  # ✅ Pass (56.592s)
```

### Integration Test Results After Control Packet Priority (January 14, 2026)

| Metric | Before Priority | After Priority | Control | Improvement |
|--------|----------------|----------------|---------|-------------|
| RTT (Test Server) | 251µs | **190µs** | 70µs | **24% faster** |
| RTT (Test CG) | 258µs | **189µs** | 80µs | **27% faster** |
| RTT Var (Test Server) | 56µs | **28µs** | 15µs | **50% reduction** |
| NAKs | 0 | 0 | 0 | ✅ |
| Gaps | 0 | 0 | 0 | ✅ |
| Recovery | 100% | 100% | 100% | ✅ |

**Key Evidence:**
```
# Test Server RTT: 251µs → 190µs (24% improvement)
gosrt_rtt_microseconds{instance="test-server"} 190

# Test CG RTT: 258µs → 189µs (27% improvement)
gosrt_rtt_microseconds{instance="test-cg"} 189
```

**Remaining RTT Gap Analysis:**
- Control: 70-80µs
- Test (EventLoop): 189-190µs
- Ratio: ~2.7x (improved from ~3.6x before priority pattern)

The remaining gap is likely due to:
1. io_uring completion handling overhead
2. Lock-free ring push/pop overhead
3. EventLoop iteration scheduling

---

## Summary: All Fixes Applied Successfully

| Fix | Before | After | Impact |
|-----|--------|-------|--------|
| Start sender EventLoop | Never started | Started ✅ | **Eliminated phantom NAKs (904→0)** |
| Control packet priority (receiver) | 1x/iteration | 4x/iteration | **24-27% RTT improvement** |

---

## Analysis: Control Packet Priority Pattern and RTT

### Why Faster ACKACK Processing Does NOT Affect RTT Calculation

After careful analysis, we determined that the control packet priority pattern
**does not directly improve RTT accuracy**. Here's why:

#### 1. Arrival Time is Captured at Push Time

```go
// connection_handlers.go - dispatchACKACK()
func (c *srtConn) dispatchACKACK(p packet.Packet) {
    if c.recvControlRing != nil {
        ackNum := p.Header().TypeSpecific
        arrivalTime := time.Now()  // ← Captured HERE at io_uring completion!

        if c.recvControlRing.PushACKACK(ackNum, arrivalTime) {
            // ...
        }
    }
}
```

The RTT calculation uses `arrivalTime - entry.timestamp`, and `arrivalTime` is
captured when the packet arrives (in the io_uring handler), NOT when it's processed
by the EventLoop.

#### 2. EWMA Uses Same Samples Regardless of Processing Speed

```go
// connection_rtt.go - Recalculate()
newRTTVal := oldRTT*0.875 + lastRTT*0.125  // Same math, same samples
```

Processing ACKACK faster doesn't change which samples we get or how many.
The EWMA calculation is identical.

#### 3. What Faster Processing DOES Affect

- `SetNAKInterval()` propagation (minor)
- When `RTTMicroseconds` metric is updated (cosmetic)
- Code consistency with sender pattern (good practice)

### Observed "Improvement" Was Likely Test Variance

The 251µs → 190µs "improvement" observed after adding control packet priority
was most likely normal test-to-test variance (~60µs), not a causal effect.

### Recommendation: Add Raw RTT Metric ✅ IMPLEMENTED

To properly diagnose RTT issues, we added a **raw RTT metric** (last sample
without EWMA smoothing). This:
- Shows actual per-sample RTT values
- Helps verify arrival time capture is working correctly
- Distinguishes network variance from EWMA smoothing effects

**Implementation:**

```go
// connection_rtt.go
type rtt struct {
    rttBits           atomic.Uint64 // float64 stored as bits (EWMA smoothed)
    rttVarBits        atomic.Uint64 // float64 stored as bits (EWMA smoothed)
    rttLastSampleUs   atomic.Uint64 // Last RTT sample (NO smoothing, raw value)
    // ...
}

func (r *rtt) Recalculate(rtt time.Duration) {
    lastRTT := float64(rtt.Microseconds())

    // Store raw sample (no smoothing) for diagnostics
    r.rttLastSampleUs.Store(uint64(lastRTT))

    // ... EWMA smoothing follows ...
}

func (r *rtt) RTTLastSample() uint64 {
    return r.rttLastSampleUs.Load()
}
```

**Prometheus Metric:**
```
gosrt_rtt_last_sample_microseconds{socket_id="...",instance="..."} 150
```

**Test Output (showing Raw vs Smoothed):**
```
Sample: 50ms  → Raw: 50000 µs,  Smoothed: 93750 µs
Sample: 200ms → Raw: 200000 µs, Smoothed: 107031 µs
Sample: 75ms  → Raw: 75000 µs,  Smoothed: 103027 µs
Sample: 100µs → Raw: 100 µs,    Smoothed: 90161 µs
```

This clearly shows how EWMA (0.875/0.125 weighting) smooths out variance.
The raw metric now allows direct verification that arrival time capture is correct.

---

## Next Steps

1. [x] Update `sender.EventLoop()` signature to take `*sync.WaitGroup`
2. [x] Update `congestion.Sender` interface
3. [x] Add sender EventLoop start logic to `connection.go`
4. [x] Run integration test to verify sender EventLoop is started
5. [x] Verify new metrics appear in Prometheus output
6. [x] Apply control packet priority pattern to receiver EventLoop
7. [x] Run integration test to verify RTT improvement
8. [x] Document: control packet priority doesn't directly affect RTT
9. [x] Add raw RTT metric (without EWMA smoothing) - `gosrt_rtt_last_sample_microseconds`
10. [ ] Run integration test with raw RTT metric to verify arrival time capture
11. [ ] Investigate remaining RTT difference (optional - low priority)

---

## Appendix: Configuration Dependency Chain

```
Sender Configuration Dependencies:
==================================

UseSendBtree = true
    │
    ▼
UseSendRing = true (requires UseBtree)
    │
    ▼
UseSendControlRing = true (requires UseSendRing)
    │
    ▼
UseSendEventLoop = true (requires UseSendControlRing)
    │
    ▼
[MISSING: connection.go must call snd.EventLoop()]


Receiver Configuration Dependencies:
====================================

UsePacketRing = true
    │
    ▼
UseEventLoop = true (requires UsePacketRing)
    │
    ▼
[CORRECT: connection.go calls recv.EventLoop()]
```


