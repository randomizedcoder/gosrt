# Lockless Design Defect Investigation

**Document**: lockless_defect_investigation.md
**Created**: 2025-12-24
**Status**: Active Investigation

This document tracks all defects discovered during the lockless design implementation (Phases 1-5) with hypotheses, investigation plans, and fixes.

---

## Table of Contents

1. [Summary of Defects](#summary-of-defects)
2. [Defect #1: Zero-Length Buffer Pool Bug](#defect-1-zero-length-buffer-pool-bug)
3. [Defect #2: Spurious NAKs with io_uring + EventLoop](#defect-2-spurious-naks-with-io_uring--eventloop)
4. [Defect #3: 31-bit Sequence Number Wraparound](#defect-3-31-bit-sequence-number-wraparound)
5. [Defect #4: NakConsolidationBudget Default Bug](#defect-4-nakconsolidationbudget-default-bug)
6. [Defect #5: SIGSEGV at 400 Mb/s (CRITICAL)](#defect-5-sigsegv-at-400-mbs-critical)

---

## Summary of Defects

| ID | Description | Severity | Status | Phase |
|----|-------------|----------|--------|-------|
| #1 | Zero-length buffer pool bug | High | ✅ Fixed | Phase 2 |
| #2 | Spurious NAKs with io_uring + EventLoop | Medium | ✅ Fixed | Phase 4 |
| #3 | 31-bit sequence wraparound in SeqLess | High | ✅ Fixed | Phase 4 |
| #4 | NakConsolidationBudget defaults to 0 | Medium | ✅ Fixed | Phase 4 |
| #5 | SIGSEGV crash at 400 Mb/s | **Critical** | 🔴 Open | Phase 5 |

---

## Defect #1: Zero-Length Buffer Pool Bug

**Phase**: 2 (Zero-Copy Buffer Lifetime)
**Severity**: High
**Status**: ✅ Fixed

### Symptom
Panic in `listen_linux.go` when io_uring tries to use buffers returned from `sync.Pool`.

### Root Cause
`DecommissionWithBuffer()` was zeroing the slice length before returning it to the pool:
```go
// BROKEN:
func (p *pkt) DecommissionWithBuffer(bufferPool *sync.Pool) {
    if p.recvBuffer != nil && bufferPool != nil {
        *p.recvBuffer = (*p.recvBuffer)[:0]  // ← WRONG: zeroes length
        bufferPool.Put(p.recvBuffer)
    }
}
```

When io_uring gets the buffer back, it expects `buffer[0]` to be accessible, but the slice has length 0.

### Fix
```go
// FIXED:
func (p *pkt) DecommissionWithBuffer(bufferPool *sync.Pool) {
    if p.recvBuffer != nil && bufferPool != nil {
        // DO NOT zero the slice - just put it back
        // The buffer will be overwritten during next receive
        bufferPool.Put(p.recvBuffer)
        p.recvBuffer = nil
        p.n = 0
    }
    p.Decommission()
}
```

### Files Modified
- `packet/packet.go`

---

## Defect #2: Spurious NAKs with io_uring + EventLoop

**Phase**: 4 (Event Loop and NAK Btree)
**Severity**: Medium
**Status**: ✅ Fixed

### Symptom
Excessive NAKs generated for packets that had already been delivered.

### Root Cause
The `nakScanStartPoint` was behind `btree_min` because packets had been delivered and removed from the btree. The NAK scan was then incorrectly reporting these delivered packets as "missing".

### Investigation
Debug logging revealed:
```
periodicNakBtree: SCAN WINDOW: startSeq=1000, btree_min=5000, btree_size=100
```

The `nakScanStartPoint` (1000) was far behind `btree_min` (5000) because packets 1000-4999 had been delivered.

### Fix
Adjusted `periodicNakBtree()` to use `lastDeliveredSequenceNumber` to correctly distinguish between delivered packets (not missing) and truly lost packets.

### Files Modified
- `congestion/live/receive.go`

---

## Defect #3: 31-bit Sequence Number Wraparound

**Phase**: 4 (Event Loop and NAK Btree)
**Severity**: High
**Status**: ✅ Fixed

### Symptom
Unit tests for wraparound scenarios failing. NAK generation incorrect when sequence numbers wrapped around `MAX_SEQUENCENUMBER` (0x7FFFFFFF).

### Root Cause
`circular.SeqLess()` used signed arithmetic that only works for full 32-bit sequences:
```go
// BROKEN for 31-bit:
func SeqLess(a, b uint32) bool {
    return int32(a-b) < 0  // int32(0x7FFFFFFF - 0) = 2147483647 (positive!)
}
```

For 31-bit sequences, `int32(MAX_31BIT - 0)` doesn't overflow, so it stays positive.

### Fix
Threshold-based comparison:
```go
// FIXED:
func SeqLess(a, b uint32) bool {
    if a == b { return false }
    var d uint32
    aLessRaw := a < b
    if aLessRaw { d = b - a } else { d = a - b }
    if d <= MaxSeqNumber31/2 { return aLessRaw }
    return !aLessRaw  // Wraparound: invert
}
```

### Files Modified
- `circular/seq_math_generic.go`
- `congestion/live/receive.go`

### Documentation
See `receiver_stream_tests_design.md` Section 12 for detailed analysis.

---

## Defect #4: NakConsolidationBudget Default Bug

**Phase**: 4 (Event Loop and NAK Btree)
**Severity**: Medium
**Status**: ✅ Fixed

### Symptom
NAK consolidation timing out immediately because budget was 0.

### Root Cause
`NakConsolidationBudget` defaulted to 0 in config, and `NewReceiver` didn't apply a sensible default.

### Fix
```go
const DefaultNakConsolidationBudgetUs = 2_000 // 2ms

func defaultNakConsolidationBudget(budgetUs uint64) uint64 {
    if budgetUs == 0 {
        return DefaultNakConsolidationBudgetUs
    }
    return budgetUs
}
```

### Files Modified
- `congestion/live/receive.go`

---

## Defect #5: SIGSEGV at 400 Mb/s (CRITICAL)

**Phase**: 5 (Validation and Testing)
**Severity**: **Critical** (crash)
**Status**: 🔴 Open - Active Investigation

### Symptom

Running at 400 Mb/s causes a segmentation fault:

```
unexpected fault address 0x7fca9dc19014
fatal error: fault
[signal SIGSEGV: segmentation violation code=0x1 addr=0x7fca9dc19014 pc=0x6767a7]

goroutine 37 [running]:
github.com/randomizedcoder/giouring.internalPeekCQE(0xc00015e040, 0x0)
    vendor/github.com/randomizedcoder/giouring/lib.go:241 +0x27
github.com/randomizedcoder/giouring.(*Ring).PeekCQE(0xc00015e040)
    vendor/github.com/randomizedcoder/giouring/lib.go:284 +0x1a
github.com/datarhei/gosrt.(*dialer).getRecvCompletion()
    dial_linux.go:364 +0x6c
github.com/datarhei/gosrt.(*dialer).recvCompletionHandler()
    dial_linux.go:508 +0xfd
```

### Context

- Both control AND test pipelines crashed within seconds
- Control pipeline showed 28% retransmission rate before crash
- Test pipeline lasted only ~130ms before crash
- Crash location: `giouring/lib.go:241` - dereferencing `ring.cqRing.ringMask`

### Current io_uring Configuration

```go
// config.go defaults:
IoUringRecvRingSize:       512,   // ← SMALL
IoUringRecvInitialPending: 512,
IoUringRecvBatchSize:      256,   // Resubmit batch size
```

At 400 Mb/s with 1500-byte packets:
- **Packet rate**: ~34,400 packets/second
- **Per-millisecond**: ~34 packets
- **Ring saturation**: 512 entries / 34 pkt/ms = **~15ms to saturate**

### Hypothesis 1: io_uring Ring Overflow

**Theory**: At 400 Mb/s, the completion queue (CQ) fills faster than we can drain it.

**What happens when CQ overflows?**

From the io_uring documentation:
1. If CQ is full, the kernel **cannot** post new completions
2. The kernel may drop completions or return `-EBUSY` from `io_uring_enter`
3. This can cause the ring to become inconsistent

**Evidence**:
- Ring size 512 is small for 34,400 pkt/s
- Batch size 256 means we only resubmit after processing 256 completions
- If processing is slow, completions accumulate faster than we drain

### Hypothesis 2: Race Condition During Cleanup

**Theory**: Connection closes under load, and `QueueExit()` is called while `PeekCQE()` is in progress.

From `defect9_client_shutdown.md`:
> "Calling `PeekCQE()` on a closed ring causes SIGSEGV"
> "`QueueExit()` unmaps memory - calling ring functions after that causes SIGSEGV"

**Sequence**:
1. Connection fails under extreme load (EOF error)
2. `Close()` → `cleanupIoUringRecv()` → `QueueExit()` (unmaps memory)
3. Meanwhile, `recvCompletionHandler` in another goroutine calls `PeekCQE()`
4. Ring memory is unmapped → **SIGSEGV**

**Evidence**:
```
Error: write: EOF                           ← Connection failed
github.com/datarhei/gosrt.(*dialer).cleanupIoUringRecv  ← Cleanup started
github.com/datarhei/gosrt.(*dialer).recvCompletionHandler ← Still running!
```

### Hypothesis 3: GetSQE Failure Cascade

**Theory**: Submission queue (SQ) fills up, `GetSQE()` fails, leading to buffer exhaustion.

In `dial_linux.go:submitRecvRequestBatch()`:
```go
for j := 0; j < maxRetries; j++ {
    sqe = ring.GetSQE()
    if sqe != nil {
        break
    }
    if j < maxRetries-1 {
        time.Sleep(100 * time.Microsecond)  // 100μs delay
    }
}

if sqe == nil {
    // Can't get SQE - give up on this request
    delete(dl.recvCompletions, requestID)
    GetRecvBufferPool().Put(bufferPtr)
    break  // ← Exits loop, losing potential submissions
}
```

At high rates, all 3 retries might fail, leading to:
1. Fewer pending receives
2. Ring drains faster than we replenish
3. Eventually no pending receives = no completions
4. But packets still arriving = kernel buffer overflow

---

## Investigation Plan for Defect #5

### Phase 1: Increase io_uring Ring Size

**Goal**: Eliminate ring overflow as a factor.

**Changes**:
```go
// config.go - increase defaults:
IoUringRecvRingSize:       8192,  // 16x larger (was 512)
IoUringRecvInitialPending: 8192,  // Match ring size
IoUringRecvBatchSize:      256,   // Keep batch size same for now
```

**Files to Modify**:
- `config.go` - Default values
- Add CLI flags if not already present (check `flags.go`)

**Rationale**:
- 8192 entries at 34,400 pkt/s = ~238ms of buffer
- Even at 10x processing delay, we have 23ms buffer
- This should eliminate CQ overflow

**Test**:
```bash
# Run with larger ring
sudo make test-isolation CONFIG=Isolation-400M-FullEventLoop \
  -iouringrecvringsize 8192 \
  -iouringrecvinitialpending 8192
```

### Phase 2: Add Ring State Validation

**Goal**: Detect and handle ring closure safely before accessing ring memory.

**Proposed Changes in `getRecvCompletion()`**:
```go
func (dl *dialer) getRecvCompletion(ctx context.Context, ring *giouring.Ring) (*giouring.CompletionQueueEvent, *recvCompletionInfo) {
    // Check if ring is still valid
    if dl.recvRing == nil {
        return nil, nil
    }

    // Add flag to track ring closed state
    if dl.recvRingClosed.Load() {
        return nil, nil
    }

    // ... rest of function
}
```

**Proposed Changes in `cleanupIoUringRecv()`**:
```go
func (dl *dialer) cleanupIoUringRecv() {
    // Set flag BEFORE QueueExit to prevent race
    dl.recvRingClosed.Store(true)

    // Give completion handler time to see the flag
    time.Sleep(1 * time.Millisecond)

    // Now safe to close
    ring.QueueExit()
    // ...
}
```

### Phase 3: Design Multiple Completion Handlers (Future)

**Goal**: Scale completion processing to match packet arrival rate.

**Architecture**:
```
                    ┌──────────────────┐
                    │   io_uring CQ    │
                    │  (8192 entries)  │
                    └────────┬─────────┘
                             │
           ┌─────────────────┼─────────────────┐
           │                 │                 │
           ▼                 ▼                 ▼
   ┌───────────────┐ ┌───────────────┐ ┌───────────────┐
   │   Handler 1   │ │   Handler 2   │ │   Handler N   │
   │ (goroutine)   │ │ (goroutine)   │ │ (goroutine)   │
   └───────┬───────┘ └───────┬───────┘ └───────┬───────┘
           │                 │                 │
           └─────────────────┼─────────────────┘
                             │
                             ▼
                    ┌──────────────────┐
                    │  Lock-Free Ring  │
                    │  (per-receiver)  │
                    └──────────────────┘
```

**Considerations**:
1. **CQ is single-consumer by design** - only one goroutine should call `PeekCQE()`
2. **Alternative**: Use `io_uring_peek_batch_cqe()` to get multiple completions at once
3. **Distribute work**: Handler 1 gets completions, distributes to worker pool
4. **Lock-free ring** already handles multi-producer (io_uring) → single-consumer (receiver)

**Proposed Design**:
```go
// Option A: Single CQ consumer, multiple packet processors
func (dl *dialer) recvCompletionHandler(ctx context.Context) {
    workerPool := make(chan *processWork, 1000)

    // Start N packet processor workers
    for i := 0; i < numWorkers; i++ {
        go dl.packetProcessorWorker(workerPool)
    }

    for {
        cqe, compInfo := dl.getRecvCompletion(ctx, ring)
        if cqe == nil { continue }

        // Dispatch to worker pool (non-blocking)
        select {
        case workerPool <- &processWork{cqe, compInfo}:
        default:
            // Worker pool full - process inline
            dl.processRecvCompletion(ring, cqe, compInfo)
        }
    }
}
```

---

## Files Referenced

| File | Purpose |
|------|---------|
| `dial_linux.go` | Dialer io_uring implementation (crash location) |
| `listen_linux.go` | Listener io_uring implementation |
| `config.go` | Configuration defaults for ring sizes |
| `contrib/common/flags.go` | CLI flags |
| `vendor/github.com/randomizedcoder/giouring/lib.go` | giouring library (crash point) |
| `documentation/IO_Uring_read_path.md` | io_uring read path design |
| `documentation/defect9_client_shutdown.md` | Previous SIGSEGV fix (related) |

---

## Defect #6: Nil Map Panic in sendIoUring (NEW)

**Phase**: 5 (Validation and Testing)  
**Severity**: **Critical** (panic)  
**Status**: 🔴 Open - Active Investigation  
**Discovered**: 2025-12-23 (during 400M LargeRing test)

### Symptom

After fixing the io_uring ring size (Defect #5 hypothesis confirmed!), a new panic occurred:

```
panic: assignment to entry in nil map

goroutine 11 [running]:
github.com/datarhei/gosrt.(*srtConn).sendIoUring(0xc0002aa008, {0x87bf98, 0xc000807600})
    /home/das/Downloads/srt/gosrt/connection_linux.go:222 +0x2d0
github.com/datarhei/gosrt.(*srtConn).send(...)
    connection_linux.go:538
github.com/datarhei/gosrt.(*srtConn).pop(...)
    connection.go:803
github.com/datarhei/gosrt.(*srtConn).sendACKACK(...)
    connection.go:1774
github.com/datarhei/gosrt.(*srtConn).handleACK(...)
    connection.go:1124
github.com/datarhei/gosrt.(*srtConn).handlePacket(...)
    connection.go:958
github.com/datarhei/gosrt.(*srtConn).handlePacketDirect(...)
    connection.go:876
github.com/datarhei/gosrt.(*dialer).processRecvCompletion(...)
    dial_linux.go:344
```

### Root Cause

**Race condition between cleanup and packet processing:**

1. Connection is closing due to EOF (high-rate packet loss)
2. `cleanupIoUring()` is called, which sets `c.sendCompletions = nil` (line 138)
3. Meanwhile, `recvCompletionHandler` is still running in another goroutine
4. An ACK packet arrives and is processed: `handleACK` → `sendACKACK` → `sendIoUring`
5. `sendIoUring` tries to write: `c.sendCompletions[requestID] = compInfo` (line 222)
6. But `sendCompletions` is now `nil` → **PANIC**

### Code Analysis

**In `cleanupIoUring()` (connection_linux.go:138):**
```go
// Clean up completion map and return all buffers to pool
c.sendCompLock.Lock()
for _, compInfo := range c.sendCompletions {
    compInfo.buffer.Reset()
    c.sendBufferPool.Put(compInfo.buffer)
}
c.sendCompletions = nil  // ← Sets to nil
c.sendCompLock.Unlock()
```

**In `sendIoUring()` (connection_linux.go:222):**
```go
// Store completion info in map (protected by lock)
c.sendCompLock.Lock()
c.sendCompletions[requestID] = compInfo  // ← PANIC if nil!
c.sendCompLock.Unlock()
```

### Fix Options

**Option A: Check for nil map before assignment**
```go
// In sendIoUring():
c.sendCompLock.Lock()
if c.sendCompletions == nil {
    c.sendCompLock.Unlock()
    // Connection is shutting down - discard packet
    sendBuffer.Reset()
    c.sendBufferPool.Put(sendBuffer)
    p.Decommission()
    return
}
c.sendCompletions[requestID] = compInfo
c.sendCompLock.Unlock()
```

**Option B: Use atomic shutdown flag**
```go
// Add to srtConn struct:
sendShutdown atomic.Bool

// In cleanupIoUring():
c.sendShutdown.Store(true)  // Set BEFORE clearing map
// ... rest of cleanup

// In sendIoUring():
if c.sendShutdown.Load() {
    // Discard packet - connection shutting down
    return
}
```

**Option C: Keep map non-nil, just empty it**
```go
// In cleanupIoUring():
c.sendCompLock.Lock()
for requestID, compInfo := range c.sendCompletions {
    compInfo.buffer.Reset()
    c.sendBufferPool.Put(compInfo.buffer)
    delete(c.sendCompletions, requestID)  // Delete instead of nil
}
// Don't set to nil - keep empty map
c.sendCompLock.Unlock()
```

### Recommended Fix: Option A + B Combined

Most robust approach:
1. Add `sendShutdown` atomic flag (fast check without lock)
2. Also check for nil map (defense in depth)

### Files to Modify
- `connection_linux.go`: Add nil check in `sendIoUring()`, add shutdown flag

---

## Progress Update: Defect #5 Partially Confirmed!

The larger ring test (8192 entries) **did NOT crash with SIGSEGV**. This confirms:

✅ **Ring overflow was a contributing factor** - larger ring prevented the original crash
❌ **But revealed Defect #6** - nil map panic in send path

The 400M test with large ring lasted ~500ms (vs ~130ms with small ring) before hitting the new bug.

---

## Immediate Action Items

1. [ ] **Fix Defect #6 first** - Add nil check in `sendIoUring()` 
2. [ ] **Add shutdown flag** to prevent sends during cleanup
3. [ ] **Re-test 400M with large ring** after Defect #6 fix
4. [ ] **Add `recvRingClosed` atomic flag** (original Defect #5 mitigation)
5. [ ] **Check for similar issues** in `listen_linux.go`
6. [ ] **Add 300 Mb/s test** to find exact throughput ceiling without crashes

---

## Test Commands

```bash
# Test 1: 300 Mb/s to find exact ceiling (standard ring)
sudo make test-isolation CONFIG=Isolation-300M-FullEventLoop

# Test 2: 400 Mb/s with LARGE io_uring ring (test ring overflow hypothesis)
sudo make test-isolation CONFIG=Isolation-400M-FullEventLoop-LargeRing

# Test 3: Original 400 Mb/s (expected to crash - for comparison)
sudo make test-isolation CONFIG=Isolation-400M-FullEventLoop
```

## Test Configurations Added

| Config | Ring Size | Batch Size | Purpose |
|--------|-----------|------------|---------|
| `Isolation-300M-FullEventLoop` | 512 (default) | 256 | Find throughput ceiling |
| `Isolation-400M-FullEventLoop-LargeRing` | **8192** | **512** | Test ring overflow hypothesis |
| `Isolation-400M-FullEventLoop` | 512 (default) | 256 | Original (crashes) |

