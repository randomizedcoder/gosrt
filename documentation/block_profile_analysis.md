# Block Profile Analysis and Optimization Opportunities

## Overview

Block profiling in Go measures where goroutines are blocked waiting on synchronization primitives:
- **Mutexes** (`sync.Mutex`, `sync.RWMutex`) - waiting to acquire locks
- **Channels** - waiting to send or receive on channels
- **Select statements** - waiting for one of multiple channel operations
- **Sync primitives** - `sync.Cond`, `sync.WaitGroup`, etc.

**Key Difference from CPU Profiling:**
- **CPU Profile**: Shows where CPU time is spent (active computation)
- **Block Profile**: Shows where goroutines are waiting (contention/blocking)

Block profiling is crucial for understanding:
- Lock contention (multiple goroutines waiting for same mutex)
- Channel contention (goroutines blocked on full/empty channels)
- Backpressure points (where data flow is constrained)

## How to Generate Block Profile

```bash
# Enable block profiling in your application
go tool pprof http://localhost:6060/debug/pprof/block

# Or collect during runtime
go tool pprof -http=:8080 block.pprof
```

**In the server/client code:**
```go
import _ "net/http/pprof"

// Block profiling is enabled by default when pprof is imported
// Access via: http://localhost:6060/debug/pprof/block
```

## Expected Blocking Points in GoSRT

### 1. Channel Operations (High Likelihood)

#### A. `networkQueueReader` - Receiving from `networkQueue`
**Location**: `connection.go:networkQueueReader()`

```go
func (c *srtConn) networkQueueReader(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case p := <-c.networkQueue:  // ← BLOCKING POINT
            c.handlePacket(p)
        }
    }
}
```

**Analysis**:
- **Blocking occurs when**: `networkQueue` is empty (reader waits for packets)
- **Expected behavior**: Normal - reader should wait when no packets available
- **Contention risk**: Low - only one reader per connection
- **With io_uring bypass**: This path is **bypassed** for io_uring-enabled connections, so blocking here should be minimal

**Optimization Status**: ✅ **Already optimized** - Phase 5 (Channel Bypass) routes packets directly to `handlePacketDirect()`, bypassing `networkQueue` for io_uring connections.

#### B. `writeQueueReader` - Receiving from `writeQueue`
**Location**: `connection.go:writeQueueReader()`

```go
func (c *srtConn) writeQueueReader(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case p := <-c.writeQueue:  // ← BLOCKING POINT
            c.snd.Push(p)
        }
    }
}
```

**Analysis**:
- **Blocking occurs when**: `writeQueue` is empty (reader waits for packets to send)
- **Expected behavior**: Normal - reader should wait when no data to send
- **Contention risk**: Low - only one reader per connection
- **Optimization opportunity**: None - this is expected behavior for send path

#### C. `deliver()` - Sending to `readQueue`
**Location**: `connection.go:deliver()`

```go
func (c *srtConn) deliver(p packet.Packet) {
    select {
    case <-c.ctx.Done():
    case c.readQueue <- p:  // ← BLOCKING POINT (with default)
    default:
        // Drops packet if queue full
    }
}
```

**Analysis**:
- **Blocking occurs when**: `readQueue` is full AND `default` case not taken (shouldn't happen with current code)
- **Current behavior**: Non-blocking with `default` - **drops packets** ❌
- **Contention risk**: Medium - congestion control calls `deliver()` frequently
- **Optimization opportunity**: Consider blocking instead of dropping (but this is receive path, not send path)

**Note**: This is the **receive path** (delivering packets to application), not the network receive path. The io_uring bypass doesn't affect this.

#### D. Listener `backlog` Channel
**Location**: `listen.go`, `listen_linux.go`

```go
// Handshake packets queued to backlog
select {
case ln.backlog <- p:  // ← BLOCKING POINT
    // Success
default:
    // Queue full - drop packet
}
```

**Analysis**:
- **Blocking occurs when**: `backlog` is full (many simultaneous connection attempts)
- **Current behavior**: Non-blocking with `default` - drops handshake packets if backlog full
- **Contention risk**: Low-Medium - only during connection establishment bursts
- **Optimization opportunity**: Consider larger backlog buffer or blocking (but drops are acceptable for handshakes)

#### E. Dialer `rcvQueue` Channel
**Location**: `dial.go`, `dial_linux.go`

```go
// Handshake packets during connection establishment
select {
case dl.rcvQueue <- p:  // ← BLOCKING POINT
    // Success
default:
    // Queue full - drop packet
}
```

**Analysis**:
- **Blocking occurs when**: `rcvQueue` is full (rare - only during handshake)
- **Current behavior**: Non-blocking with `default` - drops handshake packets if queue full
- **Contention risk**: Very Low - only during connection establishment
- **Optimization opportunity**: None needed - handshake is short-lived

### 2. Mutex Operations (Medium Likelihood)

#### A. `handlePacketMutex` - Per-Connection Packet Processing
**Location**: `connection.go:handlePacketDirect()`

```go
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    c.handlePacketMutex.Lock()  // ← BLOCKING POINT
    defer c.handlePacketMutex.Unlock()

    c.handlePacket(p)
}
```

**Analysis**:
- **Blocking occurs when**: Multiple io_uring completion handlers process packets for the same connection simultaneously
- **Expected behavior**: Rare - `handlePacket()` is fast (<10μs typically)
- **Contention risk**: Low-Medium - depends on:
  - Number of concurrent completion handlers
  - Packet rate per connection
  - `handlePacket()` execution time
- **Optimization opportunity**:
  - ✅ **Already optimized** - Per-connection mutex (not global) minimizes contention
  - If contention becomes an issue, consider per-connection worker pool (but adds complexity)

**Expected Block Profile**: Should show minimal blocking here if `handlePacket()` is fast.

#### B. Receiver `lock` (RWMutex) - Packet Store Operations
**Location**: `congestion/live/receive.go`

```go
type receiver struct {
    lock sync.RWMutex  // ← BLOCKING POINT
    // ...
}

func (r *receiver) Push(p packet.Packet) {
    r.lock.Lock()  // Write lock
    defer r.lock.Unlock()
    // ...
}

func (r *receiver) periodicACK(now uint64) {
    r.lock.RLock()  // Read lock
    defer r.lock.RUnlock()
    // ...
}
```

**Analysis**:
- **Blocking occurs when**:
  - **Write lock**: `Push()` blocks if `periodicACK()`/`periodicNAK()`/`Tick()` hold read locks
  - **Read lock**: `periodicACK()`/`periodicNAK()`/`Tick()` block if `Push()` holds write lock
- **Expected behavior**:
  - **Read locks**: Should be common (ACK/NAK generation happens frequently)
  - **Write locks**: Should be less common (only when packets arrive)
- **Contention risk**: Medium - depends on:
  - Packet arrival rate
  - ACK/NAK frequency
  - B-tree iteration time (read locks held during iteration)
- **Optimization status**: ✅ **Already optimized** - Using `sync.RWMutex` with optimized read/write locking strategy (read locks for iteration, write locks for modifications)

**Expected Block Profile**: Should show some blocking here, but optimized locking should minimize it.

#### C. Logger `logQueue` Channel
**Location**: `log.go`

```go
func (l *logger) Print(topic string, fn func() string) {
    // ...
    select {
    case l.logQueue <- log:  // ← BLOCKING POINT
        // Success
    default:
        // Queue full - drop log message
    }
}
```

**Analysis**:
- **Blocking occurs when**: `logQueue` is full (many log messages)
- **Current behavior**: Non-blocking with `default` - drops log messages if queue full
- **Contention risk**: Low - logging is not critical path
- **Optimization opportunity**: None needed - dropping logs is acceptable

### 3. Select Statements (High Likelihood)

#### A. Completion Handler Waiting for Completions
**Location**: `listen_linux.go:recvCompletionHandler()`, `dial_linux.go:recvCompletionHandler()`

```go
func (ln *listener) recvCompletionHandler() {
    for {
        select {
        case <-ln.recvCompCtx.Done():
            return
        default:
            // Peek for completions
            cqe, compInfo := ln.getRecvCompletion(ln.recvCompCtx, ln.recvRing)
            // ...
        }
    }
}
```

**Analysis**:
- **Blocking occurs when**: `getRecvCompletion()` calls `WaitCQE()` (blocking syscall)
- **Expected behavior**: Normal - completion handler should wait for I/O completions
- **Contention risk**: Low - each completion handler is independent
- **Note**: Blocking in `WaitCQE()` shows up as `runtime.futex` in CPU profile, not block profile

**Block Profile**: Should show minimal blocking here (syscall blocking is different from synchronization blocking).

#### B. Context Cancellation Checks
**Location**: Various locations with `select { case <-ctx.Done(): ... }`

**Analysis**:
- **Blocking occurs when**: Waiting for context cancellation
- **Expected behavior**: Normal - graceful shutdown mechanism
- **Contention risk**: Very Low - only during shutdown
- **Optimization opportunity**: None needed

## Interpreting Block Profile Results

### What to Look For

1. **High Blocking Time in Expected Locations** ✅
   - `networkQueueReader` waiting on empty queue: **Normal**
   - `writeQueueReader` waiting on empty queue: **Normal**
   - Completion handlers waiting for I/O: **Normal**

2. **High Blocking Time in Unexpected Locations** ⚠️
   - `handlePacketMutex.Lock()`: **Investigate** - indicates contention
   - `receiver.lock.Lock()` (write lock): **Investigate** - indicates contention between `Push()` and read operations
   - Channel sends blocking (without `default`): **Investigate** - indicates backpressure

3. **Blocking Time Distribution**
   - **Even distribution**: Good - no single bottleneck
   - **Concentrated in one location**: **Investigate** - potential optimization target

### Expected Block Profile Characteristics

**With io_uring and Channel Bypass (Phase 5):**

1. **Minimal blocking in `networkQueueReader`** ✅
   - io_uring bypass routes packets directly to `handlePacketDirect()`
   - `networkQueue` should be mostly unused for io_uring connections

2. **Low blocking in `handlePacketMutex`** ✅
   - Per-connection mutex minimizes contention
   - `handlePacket()` is fast, so blocking should be rare

3. **Moderate blocking in `receiver.lock`** ✅
   - Read locks (ACK/NAK) should be common but non-blocking
   - Write locks (Push) may block read locks, but should be brief

4. **Normal blocking in `writeQueueReader`** ✅
   - Expected - reader waits when no data to send

**Without io_uring (Traditional Path):**

1. **High blocking in `networkQueueReader`** ⚠️
   - Reader waits for packets from `ReadFrom()` goroutine
   - This is expected but indicates channel overhead

2. **Blocking in `ReadFrom()` goroutine** ⚠️
   - Waiting on `conn.ReadFromUDP()` syscall
   - Shows up as syscall blocking, not synchronization blocking

## Optimization Opportunities

### 1. Monitor `handlePacketMutex` Contention ⭐ **HIGH PRIORITY**

**If block profile shows significant blocking in `handlePacketMutex.Lock()`:**

**Symptoms**:
- High block time in `handlePacketDirect()`
- Multiple completion handlers processing packets for same connection
- `handlePacket()` execution time increased

**Solutions**:

**Option A: Per-Connection Worker Pool** (if contention is high)
```go
type srtConn struct {
    // ...
    handlePacketWorkers chan struct{}   // Semaphore (size: 1-4)
    handlePacketQueue   chan packet.Packet // Buffered queue
}

func (c *srtConn) handlePacketDirect(p packet.Packet) {
    select {
    case c.handlePacketWorkers <- struct{}{}:
        go func() {
            defer func() { <-c.handlePacketWorkers }()
            c.handlePacket(p)
        }()
    default:
        // All workers busy - queue packet (never drop)
        c.handlePacketQueue <- p
    }
}
```

**Option B: Optimize `handlePacket()` Performance** (preferred)
- Profile `handlePacket()` to identify slow operations
- Optimize control packet handling
- Reduce allocations in hot path

**Recommendation**: Start with Option B. Only use Option A if profiling shows high contention.

### 2. Monitor `receiver.lock` Contention ⭐ **MEDIUM PRIORITY**

**If block profile shows significant blocking in `receiver.lock`:**

**Symptoms**:
- High block time in `Push()` (write lock)
- High block time in `periodicACK()`/`periodicNAK()`/`Tick()` (read lock)
- Lock contention between packet arrival and ACK/NAK generation

**Solutions**:

**Option A: Further Optimize Locking Strategy** (if needed)
- Reduce time holding read locks (optimize b-tree iteration)
- Batch operations to reduce lock acquisitions
- Consider lock-free data structures for read-only operations

**Option B: Reduce ACK/NAK Frequency** (if acceptable)
- Increase `PeriodicACKInterval` and `PeriodicNAKInterval`
- Trade-off: Slightly higher latency for lower contention

**Current Status**: ✅ Already using optimized RWMutex strategy with read locks for iteration.

### 3. Monitor Channel Backpressure ⭐ **LOW PRIORITY**

**If block profile shows blocking on channel sends:**

**Symptoms**:
- Blocking on `readQueue <- p` (shouldn't happen with current `default` case)
- Blocking on `backlog <- p` (shouldn't happen with current `default` case)
- Blocking on `rcvQueue <- p` (shouldn't happen with current `default` case)

**Solutions**:
- Increase channel buffer sizes (if blocking is frequent)
- Consider blocking instead of dropping (for critical paths)
- Investigate why channels are full (backpressure upstream)

**Current Status**: Most channels use `default` case to avoid blocking, which means drops instead of blocks.

## Block Profile Analysis Checklist

When analyzing a block profile, check:

- [ ] **Total blocking time**: Is it reasonable for the workload?
- [ ] **Blocking distribution**: Is it evenly distributed or concentrated?
- [ ] **`handlePacketMutex` blocking**: Should be minimal (<1% of total)
- [ ] **`receiver.lock` blocking**: Should be moderate (read locks common, write locks brief)
- [ ] **Channel blocking**: Should be minimal with io_uring bypass
- [ ] **Unexpected blocking**: Any blocking in unexpected locations?

## Comparison: Before vs. After Phase 5 (Channel Bypass)

### Before Phase 5 (Traditional Channel Path)

**Expected Block Profile**:
- **High blocking**: `networkQueueReader` waiting on `networkQueue`
- **High blocking**: `ReadFrom()` goroutine waiting on syscalls
- **Moderate blocking**: Channel contention in receive path

### After Phase 5 (io_uring + Channel Bypass)

**Expected Block Profile**:
- **Low blocking**: `networkQueueReader` (mostly unused for io_uring connections)
- **Low blocking**: `handlePacketMutex` (per-connection, fast execution)
- **Moderate blocking**: `receiver.lock` (optimized with RWMutex)
- **Normal blocking**: `writeQueueReader` (expected for send path)

**Key Improvement**: Elimination of channel overhead in receive path should significantly reduce blocking time.

## Next Steps

1. **Collect Block Profile**: Run server with block profiling enabled
2. **Analyze Results**: Compare against expected blocking points
3. **Identify Contention**: Look for unexpected high blocking times
4. **Optimize Hot Spots**: Focus on areas with high blocking time
5. **Re-profile**: Verify improvements after optimizations

## Tools for Analysis

```bash
# View block profile in web interface
go tool pprof -http=:8080 block.pprof

# Compare two profiles
go tool pprof -base=before.pprof -http=:8080 after.pprof

# Generate text report
go tool pprof -top block.pprof

# Generate call graph
go tool pprof -png -output=block.png block.pprof
```

## Actual Block Profile Results (Critical Findings)

### Profile Summary
- **Total Blocking Time**: 7.48s
- **Primary Bottleneck**: `sync.(*Mutex).Unlock` - **6.62s (88.55%)** ⚠️ **CRITICAL**
- **Secondary Bottleneck**: `sync.(*RWMutex).RUnlock` - **0.67s (8.98%)**

### Critical Issue #1: `sync.Once` Contention in `listener.Close()` ⚠️ **CRITICAL**

**Finding**: 66.88% of blocking time (5s) is in `sync.(*Once).doSlow` related to `listener.Close()`

**Root Cause Analysis**:
```go
func (ln *listener) Close() {
    ln.shutdownOnce.Do(func() {
        // ... shutdown logic ...
        ln.conns.Range(func(key, value interface{}) bool {
            conn := value.(*srtConn)
            if conn == nil {
                return true
            }
            conn.close()  // ← This calls sync.Once.Do() for each connection!
            return true
        })
        // ...
    })
}
```

**Problem**:
1. `listener.Close()` uses `sync.Once` to ensure it only runs once
2. Inside `sync.Once.Do()`, it calls `ln.conns.Range()` which iterates over **all connections**
3. For each connection, it calls `conn.close()`, which also uses `sync.Once`
4. `sync.Map.Range()` internally uses a mutex and can take significant time with many connections
5. If multiple goroutines try to call `Close()` simultaneously, they all block waiting for the first one to complete
6. With many connections (e.g., 100 connections), the `Range()` operation can take seconds, causing severe blocking

**Impact**:
- **88.55% of CPU time** spent in mutex unlock operations
- **66.88% directly related to `listener.Close()`**
- This is the **primary bottleneck** in the system

**Optimization Strategy**:

**Option A: Remove `sync.Once` from `listener.Close()` (Recommended)**
```go
func (ln *listener) Close() {
    // Use atomic flag instead of sync.Once for faster path
    if !atomic.CompareAndSwapUint32(&ln.shutdownFlag, 0, 1) {
        return // Already closed
    }

    ln.shutdownLock.Lock()
    ln.shutdown = true
    ln.shutdownLock.Unlock()

    // Rest of shutdown logic...
}
```

**Pros**:
- Eliminates `sync.Once` overhead
- Faster check (atomic operation vs mutex)
- Still ensures Close() only runs once

**Cons**:
- Need to add `shutdownFlag atomic.Uint32` field
- Slightly more code

**Option B: Optimize `sync.Map.Range()` Usage**
- Batch connection closing (close in parallel)
- Use goroutine pool for parallel closing
- Reduce time holding the Range() lock

**Option C: Defer Connection Closing**
- Don't close connections synchronously in `Close()`
- Signal connections to close themselves
- Use a separate goroutine to close connections

**Recommendation**: **Option A** - Remove `sync.Once` from `listener.Close()`. The `sync.Once` is designed for one-time initialization, not for shutdown operations that may be called from multiple goroutines. An atomic flag is more appropriate and faster.

### Critical Issue #2: `RWMutex.RUnlock` Contention in `periodicNAK` ⚠️ **HIGH PRIORITY**

**Finding**: 8.98% of blocking time (0.67s) is in `sync.(*RWMutex).RUnlock` from `periodicNAK`

**Root Cause**:
```go
func (r *receiver) periodicNAK(now uint64) []circular.Number {
    r.lock.RLock()  // ← Read lock acquired
    defer r.lock.RUnlock()  // ← Blocking on unlock

    // ... iteration over packet store ...
    r.packetStore.Iterate(func(p packet.Packet) bool {
        // ... processing ...
    })
}
```

**Problem**:
- `periodicNAK()` holds a read lock during b-tree iteration
- If `Push()` (write lock) is called frequently, it blocks all read locks
- Multiple connections calling `periodicNAK()` simultaneously can contend on `RUnlock()`
- B-tree iteration can take time, holding the read lock longer

**Impact**:
- **8.98% of blocking time** in `RUnlock()` operations
- Contention between `Push()` (write) and `periodicNAK()`/`periodicACK()` (read)

**Optimization Strategy**:

**Option A: Reduce Read Lock Hold Time** (Already Partially Done)
- ✅ Already optimized: Cache `Header()` calls (reduces iteration time)
- Further optimize: Minimize work done while holding read lock
- Consider: Move some work outside the lock

**Option B: Batch Operations**
- Collect work to do while holding lock
- Release lock, then perform work
- Reduces lock hold time

**Option C: Lock-Free Read Path** (Complex)
- Use atomic operations for read-only paths
- Only use mutex for writes
- More complex, but eliminates read lock contention

**Recommendation**: **Option A + B** - Continue optimizing iteration performance and minimize lock hold time. The 8.98% is significant but manageable compared to the 88.55% from `listener.Close()`.

### Issue #3: `handlePacketMutex` Contention (Low in Profile)

**Finding**: `handlePacketDirect()` shows minimal blocking in the profile

**Analysis**:
- Per-connection mutex is working as designed
- Contention is low (not showing up as major blocker)
- This is **good** - the optimization is working

**Status**: ✅ **No action needed** - This is performing well.

## Updated Priority Ranking

1. **CRITICAL**: Fix `listener.Close()` `sync.Once` contention (88.55% of blocking)
   - Impact: **Massive** - will eliminate primary bottleneck
   - Effort: Low-Medium
   - Risk: Low (atomic flag is well-understood pattern)

2. **HIGH**: Optimize `receiver.lock` RUnlock contention (8.98% of blocking)
   - Impact: **Significant** - will reduce secondary bottleneck
   - Effort: Medium
   - Risk: Low (incremental optimizations)

3. **LOW**: Monitor `handlePacketMutex` (not showing as issue)
   - Impact: Minimal (already optimized)
   - Effort: None
   - Risk: None

## Implementation Plan

### Phase 1: Fix `listener.Close()` Contention (CRITICAL)

1. **Add atomic flag to listener struct**:
   ```go
   type listener struct {
       // ... existing fields ...
       shutdownFlag atomic.Uint32  // 0 = not closed, 1 = closed
   }
   ```

2. **Replace `sync.Once` with atomic check**:
   ```go
   func (ln *listener) Close() {
       if !atomic.CompareAndSwapUint32(&ln.shutdownFlag, 0, 1) {
           return // Already closed
       }

       ln.shutdownLock.Lock()
       ln.shutdown = true
       ln.shutdownLock.Unlock()

       // Rest of shutdown logic (unchanged)
   }
   ```

3. **Test**: Verify Close() still works correctly and blocking is reduced

**Expected Impact**:
- Eliminate 66.88% of blocking time
- Reduce total blocking from 88.55% to ~20-25%
- **Massive performance improvement**

### Phase 2: Optimize `receiver.lock` RUnlock Contention

1. **Profile `periodicNAK` and `periodicACK`** to identify slow operations
2. **Minimize work done while holding read lock**
3. **Consider batching operations** (collect work, release lock, perform work)
4. **Re-profile** to measure improvement

**Expected Impact**:
- Reduce 8.98% blocking to ~3-5%
- Incremental improvement

## Conclusion

Block profiling revealed **critical mutex contention issues**:

- ⚠️ **88.55% of blocking time** in `sync.(*Mutex).Unlock` - **CRITICAL**
  - Primary cause: `sync.Once` in `listener.Close()` with `sync.Map.Range()` over many connections
  - **Fix**: Replace `sync.Once` with atomic flag

- ⚠️ **8.98% of blocking time** in `sync.(*RWMutex).RUnlock` - **HIGH PRIORITY**
  - Primary cause: `periodicNAK()` holding read lock during iteration
  - **Fix**: Continue optimizing iteration, minimize lock hold time

- ✅ **`handlePacketMutex`** - Performing well, no action needed

**Next Steps**:
1. **Immediately**: Fix `listener.Close()` contention (Phase 1)
2. **Next**: Optimize `receiver.lock` RUnlock (Phase 2)
3. **Re-profile**: Verify improvements after each phase

These optimizations should provide **dramatic performance improvements**, especially for servers with many concurrent connections.

