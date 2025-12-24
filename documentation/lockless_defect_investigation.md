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
6. [Defect #5: SIGSEGV at 400 Mb/s (io_uring ring overflow)](#defect-5-sigsegv-at-400-mbs-critical)
7. [Defect #6: WriteQueue Overflow at High Bitrates (ROOT CAUSE)](#defect-6-writequeue-overflow-at-high-bitrates-root-cause)
8. [Defect #6b: Nil Map Panic in sendIoUring](#defect-6b-nil-map-panic-in-sendiouringring-secondary)

---

## Summary of Defects

| ID | Description | Severity | Status | Phase |
|----|-------------|----------|--------|-------|
| #1 | Zero-length buffer pool bug | High | ✅ Fixed | Phase 2 |
| #2 | Spurious NAKs with io_uring + EventLoop | Medium | ✅ Fixed | Phase 4 |
| #3 | 31-bit sequence wraparound in SeqLess | High | ✅ Fixed | Phase 4 |
| #4 | NakConsolidationBudget defaults to 0 | Medium | ✅ Fixed | Phase 4 |
| #5 | SIGSEGV crash at 400 Mb/s (io_uring ring overflow) | **Critical** | 🟡 Mitigated | Phase 5 |
| **#6** | **WriteQueue overflow at high bitrates (ROOT CAUSE)** | **Critical** | 🔴 Identified | Phase 5 |
| #6b | Nil map panic in sendIoUring (secondary to #6) | Critical | 🔴 Open | Phase 5 |

### Defect Chain at 400 Mb/s

```
WriteQueue fills (30ms) → Write() returns EOF → Connection closes →
  ├─→ cleanupIoUring() sets sendCompletions = nil
  └─→ recvCompletionHandler still processing ACKs → sendACKACK() →
      sendIoUring() → PANIC (nil map)
```

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

## Defect #6: WriteQueue Overflow at High Bitrates (ROOT CAUSE)

**Phase**: 5 (Validation and Testing)
**Severity**: **Critical** (causes cascade of failures)
**Status**: 🔴 Root Cause Identified - Needs Fix
**Discovered**: 2025-12-24

### Symptom

Both control and test pipelines fail with `Error: write: EOF` within ~500ms of starting at 400 Mb/s. The connections DO establish successfully (PUBLISH START appears), but then fail very quickly.

### Root Cause Analysis

The `Write()` function in `connection.go` uses a **non-blocking channel write**:

```go
// connection.go lines 694-701
func (c *srtConn) Write(b []byte) (int, error) {
    // ...
    select {
    case <-c.ctx.Done():
        return 0, io.EOF
    case c.writeQueue <- p:
        // Success - packet queued
    default:
        return 0, io.EOF  // <-- WriteQueue full → IMMEDIATE EOF!
    }
    // ...
}
```

**Queue Math at 400 Mb/s**:
- Packet rate: ~34,400 packets/second (400M bps / 1500 bytes × 8 bits / 1000ms)
- WriteQueueSize: 1024 (default in `config.go:451`)
- **Time to overflow: 1024 / 34,400 ≈ 30 milliseconds!**

If the congestion control sender can't drain the queue fast enough, the queue fills in ~30ms and ALL subsequent writes return EOF.

### Why This Wasn't Seen Before

| Bitrate | Packets/s | Queue Fill Time |
|---------|-----------|-----------------|
| 5 Mb/s | ~430 | 2.4 seconds |
| 20 Mb/s | ~1,700 | 600ms |
| 100 Mb/s | ~8,600 | 120ms |
| **400 Mb/s** | **~34,400** | **~30ms** |

At lower bitrates, the queue can absorb bursts. At 400 Mb/s, 30ms is not enough time for the sender to react.

### Cascade Effect

1. `Write()` returns `io.EOF` (queue full)
2. Client-generator sees EOF error
3. Client-generator calls `w.Close()`
4. Connection cleanup starts (`cleanupIoUring()`)
5. Receive completion handler is still running → **Defect #6b (nil map panic)**

### Potential Solutions

**Option A: Increase WriteQueueSize for high bitrates**
```go
// For 400 Mb/s with 3s latency buffer:
// 34,400 pkt/s × 3s = 103,200 packets minimum
WriteQueueSize: 110000
```
Pros: Simple fix
Cons: High memory overhead (each queue entry is a packet pointer)

**Option B: Make Write() blocking with timeout**
```go
select {
case <-c.ctx.Done():
    return 0, io.EOF
case c.writeQueue <- p:
    // Success
case <-time.After(100 * time.Millisecond):
    return 0, fmt.Errorf("write timeout: queue full")
}
```
Pros: Allows backpressure, application can react
Cons: Blocks application thread

**Option C: Dynamic queue sizing based on FC (Flow Control)**
```go
// Queue size = Flow Control window × safety factor
queueSize := config.FC * 2  // FC default is 25600
```
Pros: Automatically scales with configured buffer size
Cons: May still not be enough for extreme rates

**Option D: Rate limit at application level**
- Client-generator should pace sends to match network capacity
- SRT already does congestion control internally
- This is more of a workaround than a fix

---

## Deep Dive: WriteQueue Architecture and Channel Overhead

### Context: Why Channels Are Problematic for High-Performance Packet Paths

As documented extensively in `IO_Uring.md`, Go channels have significant overhead for high-throughput packet processing:

1. **Every channel send/receive requires lock acquisition** (internal mutex)
2. **Goroutine scheduling overhead** when channels block
3. **Memory allocation** for channel infrastructure
4. **Cache line contention** when multiple goroutines access the same channel

This is why the io_uring work (Phases 1-4) focused on **bypassing channels on the receive path** using direct packet handling (`handlePacketDirect()`) and lock-free ring buffers. The send path, however, still uses the original channel-based `writeQueue`.

### WriteQueue Write Flow (Application → Network)

**Summary**: Application writes data → `Write()` → `writeQueue` channel → `writeQueueReader()` goroutine → congestion control `snd.Push()` → `pop()` → `onSend()` callback → network syscall

#### Step-by-Step Flow with File/Function References

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    WRITE PATH (Application → Network)                        │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  1. APPLICATION WRITE                                                        │
│     ─────────────────                                                        │
│     File: connection.go                                                      │
│     Func: Write(b []byte) (int, error)                                       │
│     Line: ~677                                                               │
│                                                                              │
│     - Creates packet.NewPacket()                                             │
│     - Sets payload via p.SetData(c.writeData[:n])                            │
│     - Sets timestamp: p.Header().PktTsbpdTime = c.getTimestamp()             │
│                                                                              │
│                              │                                               │
│                              ▼                                               │
│  2. NON-BLOCKING CHANNEL SEND (⚠️ BOTTLENECK AT HIGH RATES)                  │
│     ────────────────────────                                                 │
│     File: connection.go                                                      │
│     Func: Write()                                                            │
│     Lines: 694-701                                                           │
│                                                                              │
│     select {                                                                 │
│     case <-c.ctx.Done():                                                     │
│         return 0, io.EOF                                                     │
│     case c.writeQueue <- p:    // ← SUCCESS: packet queued                   │
│     default:                                                                 │
│         return 0, io.EOF       // ← FAILURE: queue full → immediate EOF!     │
│     }                                                                        │
│                                                                              │
│     ⚠️ DEFAULT CASE: If channel is full, Write() returns EOF immediately!    │
│     ⚠️ At 400 Mb/s: ~34,400 pkt/s, queue fills in ~30ms                      │
│                                                                              │
│                              │                                               │
│                              ▼                                               │
│  3. CHANNEL BUFFER                                                           │
│     ──────────────                                                           │
│     File: connection.go                                                      │
│     Func: newSRTConn()                                                       │
│     Lines: 382-386                                                           │
│                                                                              │
│     writeQueueSize := c.config.WriteQueueSize                                │
│     if writeQueueSize <= 0 {                                                 │
│         writeQueueSize = 1024    // ← DEFAULT: only 1024 packets!            │
│     }                                                                        │
│     c.writeQueue = make(chan packet.Packet, writeQueueSize)                  │
│                                                                              │
│                              │                                               │
│                              ▼                                               │
│  4. CHANNEL READER GOROUTINE                                                 │
│     ────────────────────────                                                 │
│     File: connection.go                                                      │
│     Func: writeQueueReader(ctx context.Context)                              │
│     Lines: 822-838                                                           │
│     Started at: newSRTConn() line 506-510                                    │
│                                                                              │
│     for {                                                                    │
│         select {                                                             │
│         case <-ctx.Done():                                                   │
│             return                                                           │
│         case p := <-c.writeQueue:  // ← Blocking read from channel           │
│             c.snd.Push(p)          // ← Feed to congestion control           │
│         }                                                                    │
│     }                                                                        │
│                                                                              │
│     ⚠️ This goroutine must drain packets faster than Write() produces them   │
│                                                                              │
│                              │                                               │
│                              ▼                                               │
│  5. CONGESTION CONTROL SENDER                                                │
│     ─────────────────────────                                                │
│     File: congestion/live/send.go                                            │
│     Func: Push(p packet.Packet)                                              │
│                                                                              │
│     - Assigns sequence number                                                │
│     - Adds to send buffer (for potential retransmission)                     │
│     - Rate limiting / pacing                                                 │
│     - Calls OnDeliver callback when packet ready to send                     │
│                                                                              │
│                              │                                               │
│                              ▼                                               │
│  6. POP AND SEND CALLBACK                                                    │
│     ─────────────────────                                                    │
│     File: connection.go                                                      │
│     Func: pop(p packet.Packet)                                               │
│     Line: ~737                                                               │
│                                                                              │
│     - Sets destination address: p.Header().Addr = c.remoteAddr               │
│     - Sets socket ID: p.Header().DestinationSocketId = c.peerSocketId        │
│     - Encrypts payload if needed                                             │
│     - Calls: c.onSend(p)                                                     │
│                                                                              │
│                              │                                               │
│                              ▼                                               │
│  7. NETWORK SEND (io_uring OR standard)                                      │
│     ─────────────────────────────────                                        │
│                                                                              │
│     IF io_uring enabled (per-connection ring):                               │
│     File: connection_linux.go                                                │
│     Func: sendIoUring(p packet.Packet)                                       │
│     Lines: ~142-259                                                          │
│     - Get buffer from pool                                                   │
│     - Marshal packet                                                         │
│     - Prepare SQE with PrepareSendMsg()                                      │
│     - Submit to per-connection io_uring ring                                 │
│     - Completion handler processes result asynchronously                     │
│                                                                              │
│     IF standard path (listener):                                             │
│     File: listen.go                                                          │
│     Func: send(p packet.Packet)                                              │
│     Lines: ~427-453                                                          │
│     - Marshal packet to bytes                                                │
│     - WriteTo(buffer, addr) syscall                                          │
│                                                                              │
│     IF standard path (dialer):                                               │
│     File: dial.go                                                            │
│     Func: send(p packet.Packet)                                              │
│     Lines: ~258-284                                                          │
│     - Marshal packet to bytes                                                │
│     - Write(buffer) syscall (connected UDP)                                  │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Key Observations

#### 1. The Bottleneck is NOT io_uring

The io_uring send path (`sendIoUring()`) is already implemented and working. The problem is **before** io_uring - at the `writeQueue` channel.

#### 2. Non-Blocking Write with No Backpressure

```go
// connection.go lines 694-701
select {
case c.writeQueue <- p:
default:
    return 0, io.EOF  // ← Drops packet immediately if queue full
}
```

This design choice was made to prevent application blocking, but at high rates it causes:
- Silent packet drops (returned as EOF, not a distinct error)
- No opportunity for backpressure to slow the application

#### 3. Comparison with Receive Path (Already Optimized)

The **receive path** was optimized in Phases 1-4 to bypass channels:

```
io_uring CQE → processRecvCompletion() → handlePacketDirect() → Push() → Lock-Free Ring
                  │
                  └─ NO CHANNEL on hot path! Data flows directly to receiver
```

But the **write path** still uses the original channel:

```
Write() → writeQueue CHANNEL → writeQueueReader() → snd.Push() → io_uring
              │
              └─ CHANNEL is the bottleneck, even though io_uring is used for actual send
```

### Why Receive Path Doesn't Have This Problem

The receive path was already optimized to **bypass channels** using io_uring + lock-free ring:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                    OPTIMIZED RECEIVE PATH (io_uring)                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  io_uring CQE (Completion Queue Entry)                                       │
│       │                                                                      │
│       ▼                                                                      │
│  recvCompletionHandler()          File: listen_linux.go / dial_linux.go      │
│       │                                                                      │
│       ▼                                                                      │
│  processRecvCompletion()          - Parses packet from buffer                │
│       │                           - NO CHANNEL INVOLVED!                     │
│       ▼                                                                      │
│  handlePacketDirect()             File: connection.go                        │
│       │                           - Handles control packets inline           │
│       ▼                           - Calls recv.Push() for data packets       │
│  recv.Push()                      File: congestion/live/receive.go           │
│       │                                                                      │
│       ▼                                                                      │
│  ┌─ IF UsePacketRing ──────────────────────────────────────────────┐         │
│  │  pushToRing()  →  Lock-Free Ring  →  drainPacketRing()          │         │
│  │                        │                                         │         │
│  │                        └─ NO LOCKS on hot path!                  │         │
│  └─────────────────────────────────────────────────────────────────┘         │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Key differences:**
- Receive path: `io_uring CQE → handlePacketDirect() → Lock-Free Ring` (no channels)
- Write path: `Write() → writeQueue CHANNEL → writeQueueReader()` (channel bottleneck)

### Proposed Fix Approaches

#### Approach 1: Quick Fix - Increase WriteQueueSize (Immediate)

For high-bitrate tests, increase `WriteQueueSize` via CLI flag:
```bash
-writequeuesize 110000  # ~3 seconds buffer at 400 Mb/s
```

**Pros**: Simple, immediate
**Cons**: High memory usage, doesn't fix architectural issue

#### Approach 2: Auto-Size WriteQueue Based on Bitrate (Medium-term)

Add validation in `config.go` to automatically size the queue:
```go
// Calculate queue size based on expected packet rate and latency
// At 400Mbps, 3s latency: 34,400 × 3 = 103,200 packets
estimatedPktRate := config.MaxBW / (config.PayloadSize * 8)
minQueueSize := estimatedPktRate * uint64(config.ReceiverLatency.Seconds()) * 2
if minQueueSize > uint64(config.WriteQueueSize) {
    config.WriteQueueSize = int(minQueueSize)
}
```

**Pros**: Automatic, scales with bitrate
**Cons**: Still uses channel (architectural limit remains)

#### Approach 3: Bypass WriteQueue Channel (Long-term)

Mirror the receive path optimization: bypass the `writeQueue` channel entirely using a lock-free ring or direct submission to congestion control.

```
CURRENT:
Write() → writeQueue CHANNEL → writeQueueReader() → snd.Push()

PROPOSED:
Write() → Lock-Free Ring → eventLoop drains → snd.Push()
    OR
Write() → Direct snd.Push() (with backpressure mechanism)
```

**Pros**: Eliminates channel overhead entirely
**Cons**: Significant code changes, needs design

### Files to Modify

| File | Change | Priority |
|------|--------|----------|
| `config.go` | Validation/auto-sizing for WriteQueueSize | Medium |
| `connection.go` | Better queue full handling / blocking variant | Medium |
| `contrib/integration_testing/config.go` | Add WriteQueueSize to 100M+ test configs | Immediate |
| `contrib/integration_testing/test_configs.go` | Add WriteQueueSize to high-bitrate tests | Immediate |

---

## Defect #6b: Nil Map Panic in sendIoUring (Secondary)

**Phase**: 5 (Validation and Testing)
**Severity**: **Critical** (panic)
**Status**: 🔴 Open - Active Investigation
**Discovered**: 2025-12-24 (during 400M LargeRing test)

### Symptom

After fixing the io_uring ring size (Defect #5 hypothesis confirmed!), a new panic occurred.

**Also observed**: `Error: write: EOF` from client-generator before the panic:
```
Error: write: EOF
```

This comes from `contrib/client-generator/main.go:333`:
```go
doneChan <- fmt.Errorf("write: %w", err)
```

The `io.EOF` indicates the SRT connection closed unexpectedly. At 400 Mb/s, the high packet rate causes connection failure, which then triggers the cleanup race condition leading to the panic.

**Full panic stack**:

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

### Why EOF Triggers This Bug

The sequence of events at 400 Mb/s:

1. **High packet rate** causes receiver to fall behind
2. **Packet drops** accumulate (553 drops in 500ms = ~1100 drops/s)
3. **Connection times out or peer closes** → EOF on write
4. **EOF triggers cleanup** → `cleanupIoUring()` sets `sendCompletions = nil`
5. **Meanwhile, ACK still being processed** → `sendACKACK()` → `sendIoUring()`
6. **Race condition** → panic on nil map assignment

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

### Priority 1: Fix Root Cause (Defect #6 - WriteQueue Overflow)
1. [ ] **Increase WriteQueueSize** for high-bitrate tests (110000+ for 400 Mb/s)
2. [ ] **Consider blocking Write()** or better queue full handling
3. [ ] **Add WriteQueueSize to test configs** for 100M+ tests

### Priority 2: Fix Secondary Panic (Defect #6b - Nil Map)
4. [ ] **Add nil check in `sendIoUring()`** - defense in depth
5. [ ] **Add `sendShutdown` atomic flag** - prevent sends during cleanup
6. [ ] **Check for similar issues** in `listen_linux.go`

### Priority 3: Validation
7. [ ] **Add `recvRingClosed` atomic flag** (Defect #5 mitigation)
8. [ ] **Re-test 400M with WriteQueueSize fix**
9. [ ] **Add 300 Mb/s test** to find exact throughput ceiling

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

