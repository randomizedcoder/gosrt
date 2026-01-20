# Client Performance Analysis and Optimization Opportunities

## Overview

This document analyzes the `contrib/client/main.go` and related client code for performance bottlenecks, and identifies optimization opportunities following the patterns established in the server implementation.

The server has been extensively optimized with:
- io_uring for send and receive paths
- Atomic counters replacing mutex-protected statistics
- sync.Pool for buffer and packet reuse
- Channel bypass for direct packet processing
- Lock-free data structures where possible

This analysis identifies where the client can adopt similar optimizations.

**Key Decision:** This document proposes creating a new high-performance client at `contrib/client-performance/main.go` that maximizes performance with a more radical design, while keeping the existing `contrib/client/main.go` for compatibility and simpler use cases.

---

## Current Client Architecture

### Data Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          CLIENT DATA PATH                                │
└─────────────────────────────────────────────────────────────────────────┘

Source (r io.ReadCloser)
    │
    │ [r.Read(buffer)]
    │   - SRT Conn: Uses SetReadDeadline(2s) for periodic context checks
    │   - UDP: Standard ReadFrom
    │   - Stdin/File: Blocking read
    │
    ▼
Main Read/Write Loop (main.go:303-416)
    │
    │ [buffer := make([]byte, CHANNEL_SIZE)]
    │   - Single buffer allocation (GOOD)
    │   - Size: 2048 bytes
    │
    ├─── stats.update(n) ──► stats struct ◄── stats.tick() ───┐
    │        │                    │                            │
    │        │  [sync.Mutex]      │                            │
    │        │  ⚠️ LOCK           │                            │
    │        └────────────────────┘                            │
    │                                                          │
    ▼                                                          │
Destination (w io.WriteCloser)                                 │
    │                                                          │
    │ [w.Write(buffer[:n])]                                    │
    │   - SRT Conn: Uses io_uring send (if enabled)            │
    │   - UDP: Standard Write                                  │
    │   - Stdout/File: NonblockingWriter                       │
    │        └─── ⚠️ RWMutex per write                         │
    │   - null: NullWriter (no-op)                             │
    │                                                          │
    └──────────────────────────────────────────────────────────┘
```

### SRT Connection Data Path (Inside gosrt library)

```
SRT Read Path (when client is subscriber):
──────────────────────────────────────────

Network Socket (UDP)
    │
    ├── [io_uring ENABLED: dial_linux.go]
    │     │
    │     ▼
    │   recvCompletionHandler()
    │     │
    │     ├── processRecvCompletion()
    │     │     │
    │     │     ├── packet.NewPacketFromData()
    │     │     │
    │     │     └── conn.handlePacketDirect() ◄── Direct call, no channel
    │     │
    │     └── recvBufferPool (sync.Pool) ◄── Buffer reuse ✓
    │
    │
    └── [io_uring DISABLED: dial.go:199-266]
          │
          ▼
        ReadFrom goroutine
          │
          ├── buffer := make([]byte, MAX_MSS_SIZE) ◄── One-time allocation ✓
          │
          ├── packet.NewPacketFromData()
          │
          └── dl.rcvQueue <- p ◄── Channel (drops if full)
                    │
                    ▼
          dl.reader() goroutine
                    │
                    └── conn.push(p) → networkQueue channel


SRT Write Path (when client is publisher):
──────────────────────────────────────────

Application Write
    │
    ▼
conn.Write(data)
    │
    ▼
conn.WritePacket(p)
    │
    ├── writeQueue channel ◄── Per-connection channel
    │
    ▼
writeQueueReader goroutine
    │
    ▼
snd.Push(p) (congestion control)
    │
    ▼
onSend callback
    │
    ├── [io_uring ENABLED: connection_linux.go]
    │     │
    │     ▼
    │   sendIoUring(p)
    │     │
    │     ├── sendBufferPool (sync.Pool) ◄── Per-connection pool ✓
    │     │
    │     └── Direct io_uring submit
    │
    └── [io_uring DISABLED: dial.go:send()]
          │
          ▼
        sndMutex.Lock() ◄── Fallback mutex
          │
          └── pc.Write()
```

---

## Performance Bottleneck Analysis

### 1. Stats Struct - Mutex Contention (CRITICAL)

**Location:** `contrib/client/main.go:32-89`

**Current Implementation:**
```go
type stats struct {
    bprev  uint64
    btotal uint64
    prev   uint64
    total  uint64

    lock sync.Mutex  // ⚠️ LOCK on every update AND every tick

    period time.Duration
    last   time.Time
}

func (s *stats) update(n uint64) {
    s.lock.Lock()         // ⚠️ Called for EVERY packet
    defer s.lock.Unlock()
    s.btotal += n
    s.total++
}
```

**Problem:**
- `update()` is called for **every packet** received
- At 10 Mb/s with 1316-byte packets: ~950 packets/second = 950 lock/unlock pairs per second
- The `tick()` goroutine also acquires the lock every 200ms
- Creates contention between hot path (packet processing) and statistics reporting

**Impact:** HIGH - This is in the critical data path

---

### 2. NonblockingWriter - Lock Contention (HIGH)

**Location:** `contrib/client/writer.go:17-96`

**Current Implementation:**
```go
type nonblockingWriter struct {
    dst  io.WriteCloser
    buf  *bytes.Buffer    // ⚠️ Unbounded growth potential
    lock sync.RWMutex     // ⚠️ LOCK on every write AND every read
    size int
    done bool
}

func (u *nonblockingWriter) Write(p []byte) (int, error) {
    if u.done {
        return 0, io.EOF
    }
    u.lock.Lock()          // ⚠️ Called for EVERY write
    defer u.lock.Unlock()
    return u.buf.Write(p)
}

func (u *nonblockingWriter) writer() {
    for {
        u.lock.RLock()     // ⚠️ Constantly polling with lock
        n, err := u.buf.Read(p)
        u.lock.RUnlock()

        if n == 0 || err == io.EOF {
            time.Sleep(10 * time.Millisecond)  // ⚠️ Polling with sleep
            continue
        }
        // ...
    }
}
```

**Problems:**
1. **Lock contention**: Every write acquires mutex, every read (constant polling) acquires read lock
2. **bytes.Buffer growth**: No pool, allocates dynamically as data grows
3. **Polling with sleep**: Inefficient 10ms sleep between checks
4. **No backpressure**: Buffer can grow unbounded if writer is slow

**Impact:** HIGH when using stdout/file output

---

### 3. debugReader - Channel-Based Data Generation (MEDIUM)

**Location:** `contrib/client/reader.go:42-60`

**Current Implementation:**
```go
func (r *debugReader) Read(p []byte) (int, error) {
    for b := range r.data {  // ⚠️ Channel receive per byte
        p[i] = b
        i += 1
        if i == len {
            break
        }
    }
    return i, nil
}
```

**Problem:**
- Receives **one byte at a time** from channel
- Extremely inefficient - should batch data

**Impact:** MEDIUM (only used for debug/testing)

---

### 4. SRT Connection Channels (MEDIUM)

**Location:** `dial.go`, `connection.go`

**Current State:**
```go
// dial.go:166
dl.rcvQueue = make(chan packet.Packet, rcvQueueSize)  // Default: 2048

// connection.go (per-connection)
networkQueue = make(chan packet.Packet, 1024)
writeQueue   = make(chan packet.Packet, 1024)
readQueue    = make(chan packet.Packet, 1024)
```

**Analysis:**
- When **io_uring is ENABLED**: `rcvQueue` is bypassed (channel bypass optimization already implemented) ✓
- When **io_uring is DISABLED**: Packets go through `rcvQueue` → `reader()` → `networkQueue`
- Write path always goes through `writeQueue` channel

**Impact:** MEDIUM (mitigated when io_uring enabled)

---

## Comparison with Server Implementation

| Feature | Server | Client | Gap |
|---------|--------|--------|-----|
| io_uring receive | ✅ Yes (listen_linux.go) | ✅ Yes (dial_linux.go) | None |
| io_uring send | ✅ Yes (connection_linux.go) | ✅ Yes (connection_linux.go) | None |
| Receive buffer pool | ✅ sync.Pool | ✅ sync.Pool | None |
| Send buffer pool | ✅ Per-connection sync.Pool | ✅ Per-connection sync.Pool | None |
| Packet pool | ✅ packetPool | ✅ packetPool | None |
| Atomic statistics | ✅ ConnectionMetrics | ❌ Mutex-based stats struct | **Gap** |
| Channel bypass (recv) | ✅ handlePacketDirect() | ✅ handlePacketDirect() | None |
| Main loop buffer | N/A | ✅ Single allocation | None |
| NonblockingWriter | N/A | ❌ Lock + unbounded buffer | **Gap** |

---

## Optimization Opportunities

### Priority 1: Reuse Server's metrics.ConnectionMetrics

**Goal:** Replace mutex-based `stats` struct by reusing the server's `metrics.ConnectionMetrics`

**Rationale:**
- Less code duplication between client and server
- Consistent statistics interface
- Already proven to be high-performance (atomic counters)
- Even if client doesn't use all counters, the overhead is negligible (unused atomic counters have zero cost)

**Server's metrics.ConnectionMetrics (already exists):**

```go
// From metrics/connection_metrics.go
type ConnectionMetrics struct {
    // Receive counters
    PktRecvDataSuccess      atomic.Uint64
    PktRecvControlSuccess   atomic.Uint64
    ByteRecv                atomic.Uint64

    // Send counters
    PktSentDataSuccess      atomic.Uint64
    PktSentControlSuccess   atomic.Uint64
    ByteSent                atomic.Uint64

    // ... many more counters for detailed statistics
}
```

**Proposed Changes for client-performance:**

```go
// Instead of custom stats struct, use metrics.ConnectionMetrics
import "github.com/datarhei/gosrt/metrics"

// In main():
clientMetrics := &metrics.ConnectionMetrics{}

// In read/write loop:
clientMetrics.ByteRecv.Add(uint64(n))
clientMetrics.PktRecvDataSuccess.Add(1)

// For periodic stats printing, use existing common.PrintConnectionStatistics()
// or read directly from atomic counters:
func printStats(m *metrics.ConnectionMetrics, period time.Duration) {
    ticker := time.NewTicker(period)
    defer ticker.Stop()

    var prevBytes, prevPkts uint64
    last := time.Now()

    for c := range ticker.C {
        currentBytes := m.ByteRecv.Load()
        currentPkts := m.PktRecvDataSuccess.Load()

        diff := c.Sub(last)
        mbps := float64(currentBytes-prevBytes) * 8 / (1000 * 1000 * diff.Seconds())
        pps := float64(currentPkts-prevPkts) / diff.Seconds()

        fmt.Fprintf(os.Stderr, "\r%.3f Mbps, %.0f pkt/s", mbps, pps)

        prevBytes, prevPkts = currentBytes, currentPkts
        last = c
    }
}
```

**Benefits:**
- Eliminates lock contention in hot path
- ~950 fewer lock/unlock operations per second at 10 Mb/s
- Reuses server code (less maintenance)
- Compatible with existing statistics printing infrastructure
- Prometheus `/metrics` endpoint can expose client stats too

**Effort:** Low (30 minutes)

---

### Priority 2: Eliminate NonblockingWriter Entirely

**Problem with Channel-Based Approach:**
Go channels are **not lock-free**. Internally, channels use:
- A mutex (`lock`) to protect the buffer
- A send lock when enqueueing
- A receive lock when dequeueing

Replacing mutex with channels would potentially make things **worse** (two locks instead of one).

**Analysis: What is the fastest way to copy data from source to destination?**

| Approach | Locks | Copies | Latency | Use Case |
|----------|-------|--------|---------|----------|
| io_uring async write | 0 | 1 | Lowest | Any fd: stdout, files, sockets |
| Direct blocking write | 0 | 1 | Low | Simple cases, fast destinations |
| splice() | 0 | 0 | Lowest | Socket-to-file, file-to-socket (kernel) |
| sendfile() | 0 | 0 | Lowest | File-to-socket (kernel) |
| Channel-based | 2+ | 2+ | Higher | Decoupling (not for hot path) |
| Mutex-based buffer | 1 | 2 | Higher | Legacy |

**Key Insight:** io_uring works with ANY file descriptor - stdout (fd 1) is no exception!

The advantage of io_uring for writes:
1. **Submit to queue** → returns immediately (non-blocking)
2. **Kernel executes** → happens asynchronously
3. **Completion handler** → runs in separate goroutine, returns buffers to pool

This splits the write into two halves: submission (fast) and completion (background).

**Recommended approach for all destinations:**

1. **SRT → null**: No write at all (NullWriter - already optimal ✓)
2. **SRT → stdout**: io_uring async write to fd 1
3. **SRT → file**: io_uring async write to file fd
4. **SRT → UDP**: io_uring async write (already using this in send path)
5. **SRT → SRT**: Direct packet forwarding (no data copy needed at app level)

**Proposed Design for client-performance:**

```go
// Unified io_uring writer for ANY file descriptor (stdout, files, etc.)
// Same pattern as server's io_uring send path

type IoUringWriter struct {
    ring       *giouring.Ring
    fd         int                          // stdout=1, or file fd
    pool       sync.Pool                    // Buffer pool
    compLock   sync.Mutex                   // Protects completions map
    completions map[uint64]*writeCompletionInfo
    requestID  atomic.Uint64
    wg         sync.WaitGroup               // For completion handler
}

type writeCompletionInfo struct {
    buffer *[]byte  // Buffer to return to pool
}

func NewIoUringWriter(fd int) (*IoUringWriter, error) {
    ring := giouring.NewRing()
    if err := ring.QueueInit(256, 0); err != nil {
        return nil, err
    }

    w := &IoUringWriter{
        ring:        ring,
        fd:          fd,
        completions: make(map[uint64]*writeCompletionInfo),
        pool: sync.Pool{
            New: func() interface{} {
                buf := make([]byte, 2048)
                return &buf
            },
        },
    }

    // Start completion handler in separate goroutine
    w.wg.Add(1)
    go w.completionHandler()

    return w, nil
}

// Write submits to io_uring queue and returns immediately (non-blocking!)
func (w *IoUringWriter) Write(p []byte) (int, error) {
    // Get buffer from pool
    bufPtr := w.pool.Get().(*[]byte)
    buf := (*bufPtr)[:len(p)]
    copy(buf, p)

    // Generate request ID for completion tracking
    reqID := w.requestID.Add(1)

    // Store completion info
    w.compLock.Lock()
    w.completions[reqID] = &writeCompletionInfo{buffer: bufPtr}
    w.compLock.Unlock()

    // Submit async write to io_uring
    sqe := w.ring.GetSQE()
    sqe.PrepareWrite(w.fd, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), 0)
    sqe.SetData64(reqID)
    w.ring.Submit()

    // Return immediately - completion happens in background
    return len(p), nil
}

// completionHandler runs in separate goroutine, processes completions
func (w *IoUringWriter) completionHandler() {
    defer w.wg.Done()

    for {
        cqe, err := w.ring.WaitCQE()
        if err != nil {
            return // Ring closed or error
        }

        reqID := cqe.UserData

        // Return buffer to pool
        w.compLock.Lock()
        if info, ok := w.completions[reqID]; ok {
            w.pool.Put(info.buffer)
            delete(w.completions, reqID)
        }
        w.compLock.Unlock()

        w.ring.CQESeen(cqe)
    }
}

func (w *IoUringWriter) Close() error {
    w.ring.QueueExit()
    w.wg.Wait()
    return nil
}

// For null output: No-op (already have NullWriter)
type NullWriter struct{}
func (n *NullWriter) Write(p []byte) (int, error) { return len(p), nil }
```

**Key Advantage: Split Write into Two Halves**

```
Traditional blocking write:
┌─────────────────────────────────────────────────────────┐
│ Application Thread                                       │
│ ┌─────────┐  ┌─────────────────────────────────┐        │
│ │ Write() │──│ Block until kernel completes... │        │
│ └─────────┘  └─────────────────────────────────┘        │
└─────────────────────────────────────────────────────────┘
              ↑ Application blocked during write ↑

io_uring async write:
┌─────────────────────────────────────────────────────────┐
│ Application Thread                                       │
│ ┌─────────────┐ ┌────────────────┐                      │
│ │ Submit SQE  │ │ Return (fast!) │                      │
│ └─────────────┘ └────────────────┘                      │
└─────────────────────────────────────────────────────────┘
        │
        │ (async)
        ▼
┌─────────────────────────────────────────────────────────┐
│ Kernel                                                   │
│ ┌────────────────────────────────────────────┐          │
│ │ Execute write to stdout/file in background │          │
│ └────────────────────────────────────────────┘          │
└─────────────────────────────────────────────────────────┘
        │
        │ (completion)
        ▼
┌─────────────────────────────────────────────────────────┐
│ Completion Handler Goroutine                             │
│ ┌───────────┐ ┌────────────────────┐                    │
│ │ WaitCQE() │ │ Return buf to pool │                    │
│ └───────────┘ └────────────────────┘                    │
└─────────────────────────────────────────────────────────┘
```

**Benefits for stdout (`fd=1`):**
- Submit returns immediately - application never blocks on pipe buffer
- If downstream (ffplay) is slow, buffers queue in io_uring ring
- Natural backpressure: if ring fills, Submit() blocks (but ring is large)
- Completion handler returns buffers to pool in background

**Works for ANY destination:**
- stdout: `NewIoUringWriter(1)` (fd 1)
- stderr: `NewIoUringWriter(2)` (fd 2)
- file: `NewIoUringWriter(int(file.Fd()))`

**Recommendation:**
1. Use `IoUringWriter` for all output destinations on Linux
2. Fallback to direct write on non-Linux (or if io_uring unavailable)
3. Remove NonblockingWriter entirely

**Effort:** Medium (1-2 hours) - follows existing io_uring patterns

---

### Priority 3: Improved debugReader (Low Priority)

**Goal:** Batch data generation instead of per-byte channel

**Proposed Changes:**

```go
type debugReader struct {
    bytesPerSec uint64
    ctx         context.Context
    cancel      context.CancelFunc
    data        chan []byte  // Batched data chunks
    pool        sync.Pool
}

func (r *debugReader) Read(p []byte) (int, error) {
    select {
    case <-r.ctx.Done():
        return 0, io.EOF
    case chunk := <-r.data:
        n := copy(p, chunk)
        r.returnBuffer(chunk)
        return n, nil
    }
}

func (r *debugReader) generator(ctx context.Context) {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    s := []byte("abcdefghijklmnopqrstuvwxyz*")
    bytesPerTick := r.bytesPerSec / 10

    for {
        select {
        case <-ctx.Done():
            close(r.data)
            return
        case <-ticker.C:
            // Get buffer from pool
            bufPtr := r.pool.Get().(*[]byte)
            buf := (*bufPtr)[:bytesPerTick]

            // Fill buffer
            for i := range buf {
                buf[i] = s[i % len(s)]
            }

            r.data <- buf
        }
    }
}
```

**Benefits:**
- Single channel receive per chunk instead of per byte
- sync.Pool for buffer reuse
- Much more efficient for high bitrates

**Effort:** Low (30 minutes)

---

### Priority 4: io_uring Configuration Alignment

**Goal:** Ensure client uses same io_uring configuration patterns as server

**Current State:**
The client already supports io_uring via config flags:
- `IoUringRecvEnabled`
- `IoUringRecvRingSize`
- `IoUringRecvBatchSize`
- `IoUringRecvInitialPending`
- `IoUringEnabled` (for send path)
- `IoUringSendRingSize`

**Recommended Defaults for Client:**
```go
// Optimized defaults for client use case
IoUringRecvRingSize:       512,   // Smaller than server (client has 1 connection)
IoUringRecvBatchSize:      64,    // Smaller batches for lower latency
IoUringRecvInitialPending: 256,   // Half the ring size
IoUringSendRingSize:       256,   // Smaller for client
```

**Effort:** Low (configuration only)

---

### Priority 5: Zero-Copy Optimizations (Advanced - Separate Document)

**See:** `zero_copy_opportunities.md` for detailed zero-copy design.

**Summary:**
- Eliminates 2-3 copies per packet for clients
- Eliminates ~76 copies per packet for servers with fan-out (25 subscribers)
- Uses reference counting for buffer lifetime management
- Requires gosrt library modifications

**Key Concepts:**
- `ZeroCopyPacket` struct holds buffer reference instead of copying payload
- Buffer returned to pool only after all writes complete
- For server fan-out: atomic `refCount` tracks pending subscribers

---

## Detailed Implementation Plans

The following sections provide step-by-step implementation plans for the immediate optimizations.

### Implementation Plan 1: Migrate to metrics.ConnectionMetrics

**Goal:** Replace mutex-based `stats` struct in client and client-generator with `metrics.ConnectionMetrics`

**Files to Modify:**
- `contrib/client/main.go`
- `contrib/client-generator/main.go`

#### Step 1.1: Update contrib/client/main.go

| Task | Description |
|------|-------------|
| 1.1.1 | Add import for `github.com/datarhei/gosrt/metrics` |
| 1.1.2 | Remove the `stats` struct definition (lines 32-42) |
| 1.1.3 | Remove `stats.init()` and `stats.tick()` methods |
| 1.1.4 | Remove `stats.update()` method |
| 1.1.5 | Create `clientMetrics := &metrics.ConnectionMetrics{}` in `main()` |
| 1.1.6 | Register with metrics: `metrics.RegisterConnection("client", clientMetrics)` |
| 1.1.7 | Replace `s.update(uint64(n))` with `clientMetrics.ByteRecv.Add(uint64(n))` and `clientMetrics.PktRecvDataSuccess.Add(1)` |
| 1.1.8 | Update stats printing to use atomic loads from `clientMetrics` |

**Before (current code):**
```go
type stats struct {
    bprev  uint64
    btotal uint64
    prev   uint64
    total  uint64
    lock   sync.Mutex
    period time.Duration
    last   time.Time
}

func (s *stats) update(n uint64) {
    s.lock.Lock()
    defer s.lock.Unlock()
    s.btotal += n
    s.total++
}
```

**After (using metrics.ConnectionMetrics):**
```go
import "github.com/datarhei/gosrt/metrics"

// In main():
clientMetrics := &metrics.ConnectionMetrics{}
metrics.RegisterConnection("client", clientMetrics)
defer metrics.UnregisterConnection("client")

// In read loop:
clientMetrics.ByteRecv.Add(uint64(n))
clientMetrics.PktRecvDataSuccess.Add(1)

// For stats printing (in tick goroutine):
func printStats(ctx context.Context, m *metrics.ConnectionMetrics, period time.Duration) {
    ticker := time.NewTicker(period)
    defer ticker.Stop()

    var prevBytes, prevPkts uint64
    last := time.Now()

    for {
        select {
        case <-ctx.Done():
            return
        case c := <-ticker.C:
            currentBytes := m.ByteRecv.Load()
            currentPkts := m.PktRecvDataSuccess.Load()

            diff := c.Sub(last)
            mbps := float64(currentBytes-prevBytes) * 8 / (1000 * 1000 * diff.Seconds())
            pps := float64(currentPkts-prevPkts) / diff.Seconds()

            fmt.Fprintf(os.Stderr, "\r%.3f Mbps, %.0f pkt/s", mbps, pps)

            prevBytes, prevPkts = currentBytes, currentPkts
            last = c
        }
    }
}
```

#### Step 1.2: Update contrib/client-generator/main.go

| Task | Description |
|------|-------------|
| 1.2.1 | Add import for `github.com/datarhei/gosrt/metrics` |
| 1.2.2 | Remove any custom stats tracking |
| 1.2.3 | Create `clientMetrics := &metrics.ConnectionMetrics{}` |
| 1.2.4 | Register with metrics: `metrics.RegisterConnection("client-generator", clientMetrics)` |
| 1.2.5 | Track `ByteSent` and `PktSentDataSuccess` for outgoing data |
| 1.2.6 | Update stats printing to use atomic loads |

**Estimated Effort:** 1-2 hours for both files

---

### Implementation Plan 2: io_uring Writer for Output

**Goal:** Create unified `IoUringWriter` for all output destinations (stdout, file, etc.)

**Files to Create/Modify:**
- `contrib/client-performance/writer_iouring.go` (new)
- `contrib/client-performance/writer_iouring_linux.go` (new)
- `contrib/client-performance/writer_direct.go` (new, fallback)

#### Step 2.1: Define Writer Interface

```go
// writer.go
package main

import "io"

// Writer interface for all output modes
type Writer interface {
    io.WriteCloser
}
```

#### Step 2.2: Implement IoUringWriter (Linux)

```go
// writer_iouring_linux.go
//go:build linux

package main

import (
    "sync"
    "sync/atomic"
    "unsafe"

    "github.com/randomizedcoder/giouring"
)

type IoUringWriter struct {
    ring        *giouring.Ring
    fd          int
    pool        sync.Pool
    compLock    sync.Mutex
    completions map[uint64]*writeCompletionInfo
    requestID   atomic.Uint64
    wg          sync.WaitGroup
    closed      atomic.Bool
}

type writeCompletionInfo struct {
    buffer *[]byte
}

func NewIoUringWriter(fd int, ringSize uint32) (*IoUringWriter, error) {
    if ringSize == 0 {
        ringSize = 256
    }

    ring := giouring.NewRing()
    if err := ring.QueueInit(ringSize, 0); err != nil {
        return nil, err
    }

    w := &IoUringWriter{
        ring:        ring,
        fd:          fd,
        completions: make(map[uint64]*writeCompletionInfo),
        pool: sync.Pool{
            New: func() interface{} {
                buf := make([]byte, 2048)
                return &buf
            },
        },
    }

    // Start completion handler
    w.wg.Add(1)
    go w.completionHandler()

    return w, nil
}

func (w *IoUringWriter) Write(p []byte) (int, error) {
    if w.closed.Load() {
        return 0, io.ErrClosedPipe
    }

    // Get buffer from pool
    bufPtr := w.pool.Get().(*[]byte)
    buf := (*bufPtr)[:len(p)]
    copy(buf, p)

    reqID := w.requestID.Add(1)

    // Store completion info
    w.compLock.Lock()
    w.completions[reqID] = &writeCompletionInfo{buffer: bufPtr}
    w.compLock.Unlock()

    // Submit to io_uring
    sqe := w.ring.GetSQE()
    if sqe == nil {
        w.compLock.Lock()
        delete(w.completions, reqID)
        w.compLock.Unlock()
        w.pool.Put(bufPtr)
        return 0, fmt.Errorf("ring full")
    }

    sqe.PrepareWrite(w.fd, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), 0)
    sqe.SetData64(reqID)

    if _, err := w.ring.Submit(); err != nil {
        w.compLock.Lock()
        delete(w.completions, reqID)
        w.compLock.Unlock()
        w.pool.Put(bufPtr)
        return 0, err
    }

    return len(p), nil
}

func (w *IoUringWriter) completionHandler() {
    defer w.wg.Done()

    for {
        cqe, err := w.ring.WaitCQE()
        if err != nil {
            if w.closed.Load() {
                return
            }
            continue
        }

        reqID := cqe.UserData

        w.compLock.Lock()
        if info, ok := w.completions[reqID]; ok {
            w.pool.Put(info.buffer)
            delete(w.completions, reqID)
        }
        w.compLock.Unlock()

        w.ring.CQESeen(cqe)
    }
}

func (w *IoUringWriter) Close() error {
    w.closed.Store(true)
    w.ring.QueueExit()
    w.wg.Wait()
    return nil
}
```

#### Step 2.3: Implement DirectWriter (Fallback)

```go
// writer_direct.go
package main

import (
    "io"
    "os"
)

type DirectWriter struct {
    dst io.WriteCloser
}

func NewDirectWriter(fd int) *DirectWriter {
    return &DirectWriter{
        dst: os.NewFile(uintptr(fd), "output"),
    }
}

func (w *DirectWriter) Write(p []byte) (int, error) {
    return w.dst.Write(p)
}

func (w *DirectWriter) Close() error {
    return w.dst.Close()
}
```

#### Step 2.4: Factory Function

```go
// writer.go (continued)
import "runtime"

func NewWriter(fd int) (Writer, error) {
    if runtime.GOOS == "linux" {
        w, err := NewIoUringWriter(fd, 256)
        if err != nil {
            // Fallback to direct writer
            return NewDirectWriter(fd), nil
        }
        return w, nil
    }
    return NewDirectWriter(fd), nil
}

// Convenience functions
func NewStdoutWriter() (Writer, error) {
    return NewWriter(1)  // stdout = fd 1
}

func NewFileWriter(path string) (Writer, error) {
    f, err := os.Create(path)
    if err != nil {
        return nil, err
    }
    return NewWriter(int(f.Fd()))
}
```

#### Step 2.5: Integration with client-performance

```go
// In main.go of client-performance
func main() {
    // ... parse flags, setup context ...

    // Create output writer based on destination
    var writer Writer
    var err error

    switch {
    case *to == "null" || *to == "":
        writer = &NullWriter{}
    case *to == "-":
        writer, err = NewStdoutWriter()
    case strings.HasPrefix(*to, "file://"):
        writer, err = NewFileWriter(strings.TrimPrefix(*to, "file://"))
    }

    if err != nil {
        log.Fatal(err)
    }
    defer writer.Close()

    // Main read/write loop
    buffer := make([]byte, 2048)
    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        n, err := srtConn.Read(buffer)
        if err != nil {
            return
        }

        clientMetrics.ByteRecv.Add(uint64(n))
        clientMetrics.PktRecvDataSuccess.Add(1)

        if _, err := writer.Write(buffer[:n]); err != nil {
            return
        }
    }
}
```

**Estimated Effort:** 2-3 hours

---

### Implementation Checklist

#### Phase 1: metrics.ConnectionMetrics Migration

- [ ] Update `contrib/client/main.go` to use `metrics.ConnectionMetrics`
- [ ] Update `contrib/client-generator/main.go` to use `metrics.ConnectionMetrics`
- [ ] Test that stats printing still works correctly
- [ ] Verify Prometheus `/metrics` endpoint shows client stats
- [ ] Run benchmarks to confirm lock elimination

#### Phase 2: IoUringWriter Implementation

- [ ] Create `contrib/client-performance/` directory
- [ ] Implement `writer_iouring_linux.go`
- [ ] Implement `writer_direct.go` (fallback)
- [ ] Implement factory function in `writer.go`
- [ ] Create basic `main.go` with mode selection
- [ ] Test stdout mode (`-to -`)
- [ ] Test file mode (`-to file://...`)
- [ ] Test null mode (`-to null`)
- [ ] Run benchmarks comparing to original client

#### Phase 3: Validation

- [ ] CPU profile comparison: original client vs client-performance
- [ ] Memory profile comparison
- [ ] Mutex profile: confirm 0 lock operations in hot path
- [ ] Throughput test: packets/second at 10 Mb/s

---

## High-Performance Client Design: `contrib/client-performance/main.go`

### Design Philosophy

Create a new high-performance client that:
1. **Reuses server infrastructure**: metrics, pools, io_uring patterns
2. **Eliminates unnecessary buffering**: Direct writes where possible
3. **Minimizes code paths**: Specialized modes instead of generic abstraction
4. **Zero locks in hot path**: Atomic counters only

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    HIGH-PERFORMANCE CLIENT DESIGN                        │
└─────────────────────────────────────────────────────────────────────────┘

                         ┌─────────────────┐
                         │   CLI Flags     │
                         │  -from -to      │
                         │  -mode          │
                         └────────┬────────┘
                                  │
                    ┌─────────────┴─────────────┐
                    │      Mode Selection       │
                    │  (compile-time optimal)   │
                    └─────────────┬─────────────┘
                                  │
        ┌─────────────┬───────────┼───────────┬─────────────┐
        ▼             ▼           ▼           ▼             ▼
   ┌─────────┐  ┌──────────┐ ┌─────────┐ ┌─────────┐  ┌──────────┐
   │SRT→null │  │SRT→stdout│ │SRT→file │ │SRT→UDP  │  │SRT→SRT   │
   │         │  │          │ │         │ │         │  │(forward) │
   └────┬────┘  └────┬─────┘ └────┬────┘ └────┬────┘  └────┬─────┘
        │            │            │           │            │
        ▼            ▼            ▼           ▼            ▼
   No write     Direct       io_uring    Direct       Packet
   (discard)    syscall      async       syscall      forward

   [0 copies]   [1 copy]     [1 copy]   [1 copy]     [0 copies]
   [0 locks]    [0 locks]    [0 locks]  [0 locks]    [0 locks]
```

### Mode-Specific Implementations

#### Mode 1: SRT → null (Profiling/Benchmarking)

```go
// Absolute minimum overhead for profiling SRT receive performance
func runSrtToNull(ctx context.Context, srtConn srt.Conn, m *metrics.ConnectionMetrics) {
    buffer := make([]byte, 2048)  // Single allocation

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        n, err := srtConn.Read(buffer)
        if err != nil {
            return
        }

        // Just count, don't write
        m.ByteRecv.Add(uint64(n))
        m.PktRecvDataSuccess.Add(1)
    }
}
```

**Characteristics:**
- Zero writes
- Single buffer (no allocation per packet)
- Atomic counter updates only
- Optimal for measuring SRT receive performance in isolation

#### Mode 2: SRT → stdout (Pipe to ffplay, etc.) - io_uring

```go
// io_uring async write to stdout (fd 1)
// Submit returns immediately, completion handled in background
func runSrtToStdout(ctx context.Context, srtConn srt.Conn, m *metrics.ConnectionMetrics) {
    // Create io_uring writer for stdout (fd 1)
    writer, err := NewIoUringWriter(1)  // stdout = fd 1
    if err != nil {
        // Fallback to direct write if io_uring unavailable
        runSrtToStdoutDirect(ctx, srtConn, m)
        return
    }
    defer writer.Close()

    buffer := make([]byte, 2048)

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        n, err := srtConn.Read(buffer)
        if err != nil {
            return
        }

        m.ByteRecv.Add(uint64(n))
        m.PktRecvDataSuccess.Add(1)

        // Submit to io_uring - returns immediately!
        // Buffer is copied to pool buffer, completion handler frees it
        if _, err := writer.Write(buffer[:n]); err != nil {
            return
        }
    }
}

// Fallback for non-Linux or if io_uring unavailable
func runSrtToStdoutDirect(ctx context.Context, srtConn srt.Conn, m *metrics.ConnectionMetrics) {
    buffer := make([]byte, 2048)

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        n, err := srtConn.Read(buffer)
        if err != nil {
            return
        }

        m.ByteRecv.Add(uint64(n))
        m.PktRecvDataSuccess.Add(1)

        if _, err := os.Stdout.Write(buffer[:n]); err != nil {
            return
        }
    }
}
```

**Characteristics:**
- io_uring async write to stdout (fd 1)
- Submit returns immediately - never blocks on pipe buffer
- Completion handler returns buffers to pool in background
- Same pattern as io_uring socket sends
- Fallback to direct write on non-Linux

#### Mode 3: SRT → file (io_uring async writes)

```go
// High-throughput file writing using unified IoUringWriter
func runSrtToFile(ctx context.Context, srtConn srt.Conn, path string, m *metrics.ConnectionMetrics) {
    // Open file
    f, err := os.Create(path)
    if err != nil {
        return
    }
    defer f.Close()

    // Create io_uring writer for file fd
    writer, err := NewIoUringWriter(int(f.Fd()))
    if err != nil {
        // Fallback to direct write
        runSrtToFileDirect(ctx, srtConn, f, m)
        return
    }
    defer writer.Close()

    buffer := make([]byte, 2048)

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        n, err := srtConn.Read(buffer)
        if err != nil {
            return
        }

        m.ByteRecv.Add(uint64(n))
        m.PktRecvDataSuccess.Add(1)

        // Submit to io_uring - returns immediately!
        if _, err := writer.Write(buffer[:n]); err != nil {
            return
        }
    }
}
```

**Note on file writes with io_uring:**

For sequential file writes, `PrepareWrite` with `offset=0` appends. For random access,
track offset and pass to `PrepareWrite`. The unified `IoUringWriter` can be extended:

```go
// Extended for file writes with offset tracking
type IoUringFileWriter struct {
    IoUringWriter
    offset atomic.Int64
}

func (w *IoUringFileWriter) Write(p []byte) (int, error) {
    // Same as IoUringWriter.Write but with offset
    sqe := w.ring.GetSQE()
    currentOffset := w.offset.Add(int64(len(p))) - int64(len(p))
    sqe.PrepareWrite(w.fd, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), uint64(currentOffset))
    // ... rest same
}
```

**Characteristics:**
- Uses unified IoUringWriter pattern
- Async file I/O via io_uring
- No blocking on file writes
- Same buffer pooling as stdout mode
- High throughput for fast storage

#### Mode 4: SRT → SRT (Forwarding/Relay)

```go
// Zero-copy forwarding between SRT connections
func runSrtToSrt(ctx context.Context, srcConn, dstConn srt.Conn, m *metrics.ConnectionMetrics) {
    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        // Read packet (not bytes) to avoid re-packetization
        p, err := srcConn.ReadPacket()
        if err != nil {
            return
        }

        m.ByteRecv.Add(uint64(len(p.Data())))
        m.PktRecvDataSuccess.Add(1)

        // Forward packet directly (avoids re-packetization)
        if err := dstConn.WritePacket(p); err != nil {
            p.Decommission()
            return
        }

        m.ByteSent.Add(uint64(len(p.Data())))
        m.PktSentDataSuccess.Add(1)
    }
}
```

**Characteristics:**
- Uses ReadPacket/WritePacket API (no byte array copying)
- Packet is forwarded directly
- Minimal overhead for relay scenarios

#### Mode 5: stdin/file → SRT (Publishing)

```go
// High-performance publishing from stdin or file
func runStdinToSrt(ctx context.Context, srtConn srt.Conn, m *metrics.ConnectionMetrics) {
    // Use sync.Pool for read buffers
    bufferPool := sync.Pool{
        New: func() interface{} {
            buf := make([]byte, 1316)  // Typical MPEG-TS packet size
            return &buf
        },
    }

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        bufPtr := bufferPool.Get().(*[]byte)
        buffer := *bufPtr

        n, err := os.Stdin.Read(buffer)
        if err != nil {
            bufferPool.Put(bufPtr)
            return
        }

        m.ByteRecv.Add(uint64(n))
        m.PktRecvDataSuccess.Add(1)

        if _, err := srtConn.Write(buffer[:n]); err != nil {
            bufferPool.Put(bufPtr)
            return
        }

        m.ByteSent.Add(uint64(n))
        m.PktSentDataSuccess.Add(1)

        bufferPool.Put(bufPtr)
    }
}

// For file input with io_uring (Linux)
func runFileToSrt(ctx context.Context, srtConn srt.Conn, path string, m *metrics.ConnectionMetrics) {
    f, _ := os.Open(path)
    fd := int(f.Fd())

    // Initialize io_uring for async file reads
    ring := giouring.NewRing()
    ring.QueueInit(256, 0)
    defer ring.QueueExit()

    // ... similar pattern to file write but reversed
}
```

**Characteristics:**
- Pooled buffers for read operations
- Direct write to SRT connection
- Optional io_uring for file input (async reads)

### Shared Infrastructure

```go
// main.go for client-performance
package main

import (
    "context"
    "flag"
    "os"
    "os/signal"
    "syscall"

    srt "github.com/datarhei/gosrt"
    "github.com/datarhei/gosrt/metrics"
)

var (
    from = flag.String("from", "", "SRT source URL")
    to   = flag.String("to", "null", "Destination: null, -, file://, udp://, srt://")
)

func main() {
    flag.Parse()

    // Context for graceful shutdown
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    // Reuse server's metrics infrastructure
    clientMetrics := &metrics.ConnectionMetrics{}
    metrics.RegisterConnection("client", clientMetrics)
    defer metrics.UnregisterConnection("client")

    // Start Prometheus endpoint (optional)
    if *metricsEnabled {
        go startMetricsServer(ctx)
    }

    // Start stats printer (using server's infrastructure)
    go printStats(ctx, clientMetrics, *statsPeriod)

    // Open SRT connection
    config := srt.DefaultConfig()
    config.IoUringRecvEnabled = true  // Default: use io_uring
    config.IoUringEnabled = true

    srtConn, err := srt.Dial(ctx, "srt", *from, config, nil)
    if err != nil {
        log.Fatal(err)
    }
    defer srtConn.Close()

    // Select mode based on destination
    switch {
    case *to == "null" || *to == "":
        runSrtToNull(ctx, srtConn, clientMetrics)
    case *to == "-":
        runSrtToStdout(ctx, srtConn, clientMetrics)
    case strings.HasPrefix(*to, "file://"):
        runSrtToFile(ctx, srtConn, strings.TrimPrefix(*to, "file://"), clientMetrics)
    case strings.HasPrefix(*to, "srt://"):
        dstConn := openSrtWriter(*to)
        runSrtToSrt(ctx, srtConn, dstConn, clientMetrics)
    default:
        log.Fatalf("unsupported destination: %s", *to)
    }
}
```

### Comparison: Original Client vs Performance Client

| Aspect | contrib/client | contrib/client-performance |
|--------|---------------|---------------------------|
| Stats | Mutex-based custom struct | Reuse metrics.ConnectionMetrics |
| Stdout write | NonblockingWriter (locks + polling) | io_uring async (submit & forget) |
| File write | NonblockingWriter | io_uring async |
| null write | NullWriter | NullWriter (same) |
| Buffer management | Various | Unified sync.Pool |
| Write pattern | Blocking syscall | Submit to ring, completion in background |
| Mode selection | Generic io.Reader/Writer | Specialized per-mode |
| Code complexity | Higher (abstractions) | Lower (direct) |
| Hot path locks | 2+ per packet | 0 (submit is lockfree) |
| Memory allocations | Per-packet possible | Pooled |

### io_uring Write Pattern (Used for ALL output)

```
┌────────────────────────────────────────────────────────────────────┐
│                     UNIFIED IO_URING WRITE PATH                     │
└────────────────────────────────────────────────────────────────────┘

┌─────────────┐     ┌─────────────────────────────────────────────────┐
│ SRT Read    │────▶│ IoUringWriter.Write()                           │
│ (packet)    │     │                                                 │
└─────────────┘     │  1. Get buffer from pool                        │
                    │  2. Copy data to pool buffer                    │
                    │  3. sqe.PrepareWrite(fd, buf, len, offset)      │
                    │  4. ring.Submit()                               │
                    │  5. Return immediately ← NON-BLOCKING           │
                    └───────────────────────────┬─────────────────────┘
                                                │
                              (kernel executes in background)
                                                │
                                                ▼
                    ┌─────────────────────────────────────────────────┐
                    │ Completion Handler Goroutine                     │
                    │                                                 │
                    │  1. ring.WaitCQE()                              │
                    │  2. Extract request ID from cqe.UserData        │
                    │  3. Return buffer to pool                       │
                    │  4. ring.CQESeen(cqe)                           │
                    └─────────────────────────────────────────────────┘

Works for:
  - stdout (fd 1)  ──┐
  - stderr (fd 2)  ──┼── Same IoUringWriter, different fd
  - files (fd N)   ──┘
```

### Build Tags for Platform-Specific Optimization

```go
// client_performance_linux.go
//go:build linux

func runSrtToFile(...) {
    // Use io_uring for async file writes
}

// client_performance_other.go
//go:build !linux

func runSrtToFile(...) {
    // Fallback to direct syscall
}
```

---

## Implementation Roadmap

### Phase 1: Core Infrastructure (2-3 hours)

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| Basic skeleton | High | 30 min | CLI, signal handling, context |
| Reuse metrics.ConnectionMetrics | High | 30 min | Replace custom stats |
| IoUringWriter (unified) | High | 1 hour | Single writer for all fds |
| Completion handler | High | 30 min | Background goroutine for completions |

### Phase 2: Output Modes (2-3 hours)

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| SRT → null mode | High | 15 min | Trivial, just count |
| SRT → stdout mode | High | 30 min | IoUringWriter(fd=1) |
| SRT → file mode | Medium | 30 min | IoUringWriter(file.Fd()) |
| SRT → SRT forwarding | Medium | 1 hour | Packet-level, no copy |

### Phase 3: Input Modes (1-2 hours)

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| stdin → SRT (publish) | Medium | 30 min | Direct read + SRT write |
| file → SRT (publish) | Medium | 30 min | io_uring read + SRT write |

### Phase 4: Polish and Fallback (1-2 hours)

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| Non-Linux fallback | Medium | 30 min | DirectWriter for macOS/Windows |
| Error handling | Medium | 30 min | Graceful degradation |
| Build tags | Low | 15 min | Conditional compilation |

### Phase 5: Benchmarking and Validation (2 hours)

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| Benchmark suite | High | 1 hour | Compare original vs performance |
| Profile comparison | High | 1 hour | CPU, memory, lock contention |

### Phase 6: Zero-Copy Buffer Reuse (4-6 hours) - ADVANCED

Requires modifying gosrt library:

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| ZeroCopyPacket struct | High | 1 hour | New packet type that holds buffer reference |
| ParseHeaderOnly() | High | 30 min | Parse header without payload copy |
| ReadZeroCopyPacket() API | High | 1 hour | New API for zero-copy receive |
| WriteZeroCopy() | High | 1 hour | Write directly from recv buffer |
| Completion chaining | Medium | 1 hour | Return buffer after write completes |
| Linked SQEs (optional) | Medium | 1 hour | Chain recv → write in io_uring |

### Phase 7: Server Zero-Copy with Fan-Out (6-8 hours) - ADVANCED

Requires modifying gosrt library for server fan-out:

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| Reference counting | High | 1 hour | Add atomic refCount to ZeroCopyPacket |
| Retain/Release API | High | 1 hour | Thread-safe reference management |
| Modified recv.Push() | High | 2 hours | Store ZeroCopyPacket in reorder buffer |
| Modified deliver() | High | 2 hours | Fan-out with reference counting |
| Subscriber completion | High | 1 hour | Release on write completion |
| TSBPD integration | High | 1 hour | Ensure buffer lives through delay |

### Phase 8: Registered Buffers (2-3 hours) - OPTIONAL

| Task | Impact | Effort | Description |
|------|--------|--------|-------------|
| RegisterBuffers() | Medium | 1 hour | Pin buffers with io_uring |
| Fixed buffer recv | Medium | 1 hour | Use registered buffers for recv |
| Fixed buffer write | Medium | 1 hour | Use same buffers for write |

---

## Benchmarking Strategy

### Metrics to Measure

1. **Throughput**: Packets/second, Mbps
2. **Latency**: Per-packet processing time
3. **CPU Usage**: User/system time, context switches
4. **Memory**: Allocations/second, heap size
5. **Lock Contention**: Time spent waiting for locks (mutex profile)

### Comparative Benchmarks

```bash
# Test 1: SRT → null (pure receive performance)
# Original client
./contrib/client/client -from srt://server:6000/stream -to null -profile cpu
# Performance client
./contrib/client-performance/client-performance -from srt://server:6000/stream -to null -profile cpu

# Test 2: SRT → stdout | ffplay (with downstream consumer)
# Original client
./contrib/client/client -from srt://server:6000/stream -to - | ffplay -
# Performance client
./contrib/client-performance/client-performance -from srt://server:6000/stream -to - | ffplay -

# Test 3: SRT → file (disk I/O bound)
# Original client
./contrib/client/client -from srt://server:6000/stream -to file:///tmp/output.ts
# Performance client (uses io_uring)
./contrib/client-performance/client-performance -from srt://server:6000/stream -to file:///tmp/output.ts
```

### Micro-benchmarks

```go
// Benchmark: Mutex stats vs Atomic stats
func BenchmarkStatsUpdate_Mutex(b *testing.B) {
    s := &stats{}  // Current implementation with mutex
    s.init(200*time.Millisecond, context.Background())

    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            s.update(1316)
        }
    })
}

func BenchmarkStatsUpdate_Atomic(b *testing.B) {
    m := &metrics.ConnectionMetrics{}  // Server's atomic counters

    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            m.ByteRecv.Add(1316)
            m.PktRecvDataSuccess.Add(1)
        }
    })
}

// Benchmark: NonblockingWriter vs Direct write
func BenchmarkWrite_NonblockingWriter(b *testing.B) {
    w := NewNonblockingWriter(io.Discard, 2048)
    data := make([]byte, 1316)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        w.Write(data)
    }
    w.Close()
}

func BenchmarkWrite_Direct(b *testing.B) {
    data := make([]byte, 1316)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        io.Discard.Write(data)
    }
}
```

### Expected Results

| Benchmark | Original | Performance | Improvement |
|-----------|----------|-------------|-------------|
| Stats update (ns/op) | ~50-100 | ~5-10 | 10x faster |
| Write throughput | Limited by locks | Syscall speed | 2-5x faster |
| Allocations/op | >0 | 0 (pooled) | Reduced GC |
| Lock contention | Visible in mutex profile | None | Eliminated |

### Profile Commands

```bash
# CPU profile during SRT receive
./client-performance -from srt://server:6000/stream -to null -profile cpu

# Memory profile
./client-performance -from srt://server:6000/stream -to null -profile heap

# Compare mutex profiles (should show much less contention)
./client -from srt://server:6000/stream -to null -profile mutex
./client-performance -from srt://server:6000/stream -to null -profile mutex

# Block profile (time spent blocking)
./client-performance -from srt://server:6000/stream -to null -profile block
```

---

## Summary

### Current State

The gosrt library already has significant optimizations:
- ✅ io_uring for receive path (when enabled)
- ✅ io_uring for send path (per-connection)
- ✅ sync.Pool for receive and send buffers
- ✅ Packet pooling
- ✅ Channel bypass with handlePacketDirect()

However, `contrib/client/main.go` has bottlenecks:

| Gap | Severity | Location |
|-----|----------|----------|
| Mutex-based stats | High | main.go |
| Lock-heavy NonblockingWriter | Medium | writer.go |
| Per-byte debugReader | Low | reader.go |

### Recommended Approach

Instead of incrementally fixing `contrib/client/main.go`, create a new **`contrib/client-performance/main.go`** that:

1. **Reuses server infrastructure**: `metrics.ConnectionMetrics` for statistics
2. **Eliminates NonblockingWriter**: Use direct writes (blocking is fine for fast destinations)
3. **Mode-specific optimizations**: Specialized code paths for each destination type
4. **Zero locks in hot path**: Atomic counters only

### Two Client Strategy

| Client | Purpose | Trade-offs |
|--------|---------|------------|
| `contrib/client` | Compatibility, flexibility | Some performance overhead |
| `contrib/client-performance` | Maximum throughput | Less flexible, more specialized |

### Expected Improvements (client-performance vs client)

| Metric | Original Client | Performance Client |
|--------|----------------|-------------------|
| Hot path locks | 2+ per packet | 0 |
| Stats overhead | ~950 mutex ops/sec | 2 atomic ops/sec (tick only) |
| Memory per packet | Variable | Pooled, constant |
| Code complexity | Higher | Lower (direct paths) |

### Key Design Decisions

1. **Reuse `metrics.ConnectionMetrics`**: Same counters as server, less code
2. **io_uring for ALL writes**: Unified async I/O for stdout, files, sockets
   - Submit returns immediately (non-blocking)
   - Completion handled in background goroutine
   - Same pattern as server's io_uring send path
3. **Unified IoUringWriter**: One implementation works for any fd
4. **Packet-level forwarding**: For SRT→SRT relay, avoid byte-level copying
5. **Split write into two halves**: Submit (fast, in hot path) + Completion (background)
6. **Zero-copy buffer reuse** (advanced):
   - Don't copy payload during deserialization
   - Write directly from receive buffer to destination
   - Return buffer to pool only after write completes
   - Eliminates 2-3 copies per packet
7. **Server fan-out with reference counting** (advanced):
   - One receive buffer shared by ALL subscribers
   - Atomic refCount tracks pending writes
   - Buffer returned to pool when last subscriber completes
   - Eliminates 1.8 GB/sec memory bandwidth for 20 streams × 25 subscribers

---

## When to Use Which Client

### Use `contrib/client` (Original)

- **Flexibility needed**: Multiple source/destination types in one session
- **Debugging**: NonblockingWriter provides buffering that can help with slow destinations
- **Compatibility**: Works with all edge cases and legacy configurations
- **Development**: Easier to understand and modify

### Use `contrib/client-performance` (New)

- **Maximum throughput**: When every packet/second matters
- **Profiling**: Clean baseline without overhead for SRT performance analysis
- **Production streaming**: High-bitrate, low-latency scenarios
- **Benchmarking**: Comparing SRT implementations

### Performance Comparison Summary

```
                    ┌─────────────────────────────────────────────┐
                    │         Packets per Second Capacity         │
                    │                                             │
   Original Client  │████████████████░░░░░░░░░░░░░░░░  ~60%       │
                    │                                             │
   Perf Client      │████████████████████████████░░░░  ~90%       │
                    │                                             │
   Perf + Zero-Copy │████████████████████████████████  ~100%      │
                    │                                             │
                    └─────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────┐
                    │            Lock Operations / sec            │
                    │                                             │
   Original Client  │████████████████████  ~2000 (stats + writer) │
                    │                                             │
   Perf Client      │█  ~10 (periodic stats print only)          │
                    │                                             │
                    └─────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────┐
                    │           Memory Copies per Packet          │
                    │                                             │
   Original Client  │████████████  3 copies                       │
                    │  (recv→packet→user→write)                   │
                    │                                             │
   Perf Client      │████████  2 copies                           │
                    │  (recv→packet→write)                        │
                    │                                             │
   Perf + Zero-Copy │  0 copies                                   │
                    │  (recv buffer → write directly)             │
                    │                                             │
                    └─────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────┐
                    │         Allocations per Packet              │
                    │                                             │
   Original Client  │████████████  2-3 allocs                     │
                    │  (packet, payload, write buffer)            │
                    │                                             │
   Perf Client      │████  1 alloc (pooled write buffer)         │
                    │                                             │
   Perf + Zero-Copy │  0 allocs                                   │
                    │  (reuse recv buffer for write)              │
                    │                                             │
                    └─────────────────────────────────────────────┘

SERVER FAN-OUT (20 streams × 10Mb/s × 25 subscribers):

                    ┌─────────────────────────────────────────────┐
                    │      Memory Bandwidth (copies/sec)          │
                    │                                             │
   Current Server   │████████████████████████████████████████████ │
                    │  1.8 GB/sec (1.4M copies × 1316 bytes)      │
                    │                                             │
   Zero-Copy Server │                                             │
                    │  ~0 GB/sec (pointer passing only)           │
                    │                                             │
                    └─────────────────────────────────────────────┘

                    ┌─────────────────────────────────────────────┐
                    │      Buffer Pool Operations / Packet        │
                    │                                             │
   Current Server   │████████████████████████████████████████████ │
                    │  ~25 Get/Put pairs per packet (one/sub)     │
                    │                                             │
   Zero-Copy Server │██                                           │
                    │  1 Get/Put pair per packet (shared buffer)  │
                    │                                             │
                    └─────────────────────────────────────────────┘
```

---

## File Structure

```
contrib/
├── client/                      # Original client (keep for compatibility)
│   ├── main.go
│   ├── reader.go
│   └── writer.go
│
├── client-performance/          # NEW: High-performance client
│   ├── main.go                  # CLI, mode selection, signal handling
│   │
│   ├── writer_iouring.go        # Unified IoUringWriter (Linux)
│   ├── writer_iouring_linux.go  # Linux-specific io_uring implementation
│   ├── writer_direct.go         # Fallback direct writer (non-Linux)
│   │
│   ├── mode_null.go             # SRT → null (no-op)
│   ├── mode_stdout.go           # SRT → stdout (io_uring fd=1)
│   ├── mode_file.go             # SRT → file (io_uring)
│   ├── mode_srt.go              # SRT → SRT forwarding (packet-level)
│   │
│   ├── mode_publish_stdin.go    # stdin → SRT
│   ├── mode_publish_file.go     # file → SRT (io_uring read)
│   │
│   └── stats.go                 # Reuses metrics.ConnectionMetrics
│
├── client-generator/            # Data generator for testing
│   └── main.go
│
└── server/                      # SRT server
    └── main.go
```

### Unified Writer Interface

```go
// writer.go - interface for all output modes
type Writer interface {
    Write(p []byte) (n int, err error)
    Close() error
}

// Factory function selects best writer for platform/destination
func NewWriter(dest string) (Writer, error) {
    switch {
    case dest == "null":
        return &NullWriter{}, nil
    case dest == "-":
        return newIoUringWriterOrFallback(1)  // stdout
    case strings.HasPrefix(dest, "file://"):
        f, _ := os.Create(strings.TrimPrefix(dest, "file://"))
        return newIoUringWriterOrFallback(int(f.Fd()))
    default:
        return nil, fmt.Errorf("unsupported destination: %s", dest)
    }
}

// Linux: use io_uring, others: fallback to direct write
func newIoUringWriterOrFallback(fd int) (Writer, error) {
    if runtime.GOOS == "linux" {
        return NewIoUringWriter(fd)
    }
    return &DirectWriter{fd: fd}, nil
}
```

---

## Related Documents

- `IO_Uring.md` - io_uring send path implementation
- `IO_Uring_read_path.md` - io_uring receive path implementation
- `IO_Uring_read_path_phases.md` - Phased implementation plan
- `metrics_and_statistics_design.md` - Atomic counters design
- `packet_pooling_design.md` - sync.Pool usage for packets
- `context_and_cancellation_new_design.md` - Graceful shutdown patterns

