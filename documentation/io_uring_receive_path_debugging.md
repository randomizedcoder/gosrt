# io_uring Receive Path Debugging Analysis

## Problem Statement

A connection (172.16.40.212:8439, socket ID `0xc4bf813e`) stopped processing packets, leading to peer idle timeout. However:

- **Network traffic was still arriving** at the server (Grafana shows ~40 Mb/s drop when connection closed)
- **Other connections continued working** normally
- **Client was actively sending packets** (client logs show increasing `pkt_sent_ack`, `pkt_sent_nak`)
- **Server stopped receiving packets** for this specific connection (server `pkt_recv_ack` frozen)

This suggests a **per-connection processing issue** rather than a network or system-wide problem.

## Potential Failure Points

### 1. `handlePacketDirect()` Lock Deadlock/Stuck ⚠️ **HIGH PRIORITY**

**Location**: `connection.go:739-745`

```go
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    c.handlePacketMutex.Lock()  // ← Could get stuck here
    defer c.handlePacketMutex.Unlock()

    c.handlePacket(p)  // ← Could get stuck here (infinite loop, panic, deadlock)
}
```

**Potential Issues**:
- **Lock held indefinitely**: If `handlePacket()` enters an infinite loop, the mutex is never released
- **Deadlock**: If `handlePacket()` tries to acquire another lock that's held by a goroutine waiting for `handlePacketMutex`
- **Long-running operation**: If `handlePacket()` performs a very slow operation, it blocks all subsequent packets
- **Blocking operation**: If `handlePacket()` calls a blocking operation (channel send without default, mutex lock, etc.) that never completes

**Impact**:
- All packets for this connection queue up waiting for the lock
- `processRecvCompletion()` continues to be called (io_uring keeps processing)
- But `handlePacketDirect()` blocks indefinitely
- Eventually, peer idle timeout fires (no packets processed = no timer reset)

**Evidence Supporting This Theory**:
- Network traffic still arriving (packets at network level)
- Other connections working (not a system-wide issue)
- Client sending packets (not a client issue)
- Server not processing (per-connection processing stuck)

### 2. `processRecvCompletion()` Silent Failure ⚠️ **MEDIUM PRIORITY**

**Location**: `listen_linux.go:372-510`

**Potential Issues**:
- **Connection lookup failure**: `ln.conns.Load(socketId)` returns `false` (connection removed from map?)
- **Type assertion failure**: `val.(*srtConn)` returns nil (shouldn't happen, but possible if wrong type stored)
- **Peer address validation failure**: `AllowPeerIpChange=false` and address mismatch causes packet drop
- **Error in packet parsing**: Packet deserialization fails, packet dropped, but no logging for this connection

**Code Paths That Drop Packets**:
```go
// Unknown socket ID
val, ok := ln.conns.Load(socketId)
if !ok {
    ring.CQESeen(cqe)
    p.Decommission()  // ← Packet dropped silently (unless logging enabled)
    return
}

// Nil connection
conn := val.(*srtConn)
if conn == nil {
    ring.CQESeen(cqe)
    p.Decommission()  // ← Packet dropped silently
    return
}

// Wrong peer address
if !ln.config.AllowPeerIpChange {
    if h.Addr.String() != conn.RemoteAddr().String() {
        ring.CQESeen(cqe)
        p.Decommission()  // ← Packet dropped silently (with log, but only if logging enabled)
        return
    }
}
```

**Impact**:
- Packets arrive at network level
- But are dropped before reaching `handlePacket()`
- Timer never resets → timeout

### 3. io_uring Completion Handler Stuck ⚠️ **LOW PRIORITY**

**Location**: `listen_linux.go:recvCompletionHandler()`

**Potential Issues**:
- Completion handler goroutine exited unexpectedly (though unlikely, as other connections work)
- Completion handler stuck in a loop (unlikely, as other connections work)
- Completion handler not processing completions for this connection's packets (unlikely, as same handler processes all)

**Impact**:
- Completions queue up in io_uring
- But `processRecvCompletion()` never called
- Packets never processed

**Evidence Against This Theory**:
- Other connections working (same completion handler processes all connections)
- Network traffic still arriving (io_uring receiving packets)

### 4. Connection Removed from Map Prematurely ⚠️ **MEDIUM PRIORITY**

**Location**: `listen.go:conns sync.Map`

**Potential Issues**:
- Connection removed from `ln.conns` while still active
- Race condition: connection closing but packets still in flight
- `conns.Delete(socketId)` called before connection fully closed

**Impact**:
- Packets arrive but connection lookup fails
- Packets dropped silently
- Timer never resets

## Diagnostic Options (Ranked by Likelihood of Success)

### Option 1: Lock Monitoring Goroutine ⭐⭐⭐ **HIGHEST PRIORITY**

**Implementation**:
- Add a monitoring goroutine per connection that attempts to acquire `handlePacketMutex` every 10 seconds
- If lock acquisition fails (timeout after 1 second), log warning and increment counter
- Track "lock contention time" and "lock stuck events"

**Why This Helps**:
- **Directly tests the hypothesis** that `handlePacketMutex` is stuck
- **Low overhead** (one goroutine per connection, runs every 10 seconds)
- **Immediate detection** of stuck locks
- **Actionable data** - if lock is stuck, we know immediately

**Implementation Details**:
```go
type srtConn struct {
    // ... existing fields ...
    handlePacketMutex sync.Mutex
    lockStuckCounter  atomic.Uint64  // Count of times lock was stuck
    lastLockAcquire   atomic.Int64   // Timestamp of last successful lock acquire
}

// Start lock monitoring goroutine in newSRTConn()
go func() {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            // Try to acquire lock with timeout
            done := make(chan struct{})
            go func() {
                c.handlePacketMutex.Lock()
                defer c.handlePacketMutex.Unlock()
                close(done)
            }()

            select {
            case <-done:
                // Lock acquired successfully
                c.lastLockAcquire.Store(time.Now().Unix())
            case <-time.After(1 * time.Second):
                // Lock stuck - log and increment counter
                c.lockStuckCounter.Add(1)
                c.log("connection:lock:stuck", func() string {
                    return fmt.Sprintf("handlePacketMutex stuck for >1s (count: %d)", c.lockStuckCounter.Load())
                })
            }
        case <-c.ctx.Done():
            return
        }
    }
}()
```

**Expected Outcome**:
- If lock is stuck, we'll see "lock stuck" logs before timeout
- Counter will increment, confirming the issue
- We can then investigate what's holding the lock

### Option 2: Enhanced Error Logging in processRecvCompletion ⭐⭐ **HIGH PRIORITY**

**Implementation**:
- Add detailed logging for all packet drop paths
- Log connection state when packets are dropped
- Track statistics: packets dropped per connection, reason for drop

**Why This Helps**:
- **Identifies silent packet drops** - if packets are being dropped, we'll see why
- **Low overhead** - only logs when packets are dropped
- **Actionable data** - shows exactly where packets are being lost

**Implementation Details**:
```go
// In processRecvCompletion()
val, ok := ln.conns.Load(socketId)
if !ok {
    // Enhanced logging
    ln.log("listen:recv:error", func() string {
        return fmt.Sprintf("unknown destination socket ID: %#08x (connection may have been closed)", socketId)
    })
    // Track statistics
    // ... increment drop counter ...
    ring.CQESeen(cqe)
    p.Decommission()
    return
}

conn := val.(*srtConn)
if conn == nil {
    ln.log("listen:recv:error", func() string {
        return fmt.Sprintf("connection is nil for socket ID: %#08x", socketId)
    })
    ring.CQESeen(cqe)
    p.Decommission()
    return
}

// Add logging for peer address mismatch
if !ln.config.AllowPeerIpChange {
    if h.Addr.String() != conn.RemoteAddr().String() {
        ln.log("listen:recv:error", func() string {
            return fmt.Sprintf("packet from wrong peer for socket %#08x: expected %s, got %s",
                socketId, conn.RemoteAddr().String(), h.Addr.String())
        })
        ring.CQESeen(cqe)
        p.Decommission()
        return
    }
}
```

**Expected Outcome**:
- If packets are being dropped, we'll see logs explaining why
- Can identify if connection was removed from map prematurely
- Can identify if peer address changed

### Option 3: Race Condition Testing ⭐⭐ **MEDIUM-HIGH PRIORITY**

**Implementation**:
- Use Go's race detector (`go test -race`)
- Add stress tests with concurrent packet processing
- Test connection close while packets in flight
- Test lock acquisition under high load

**Why This Helps**:
- **Detects race conditions** that might cause connection state corruption
- **Identifies timing issues** that only occur under specific conditions
- **Validates thread safety** of the io_uring receive path

**Implementation Details**:
```go
// Test concurrent packet processing
func TestConcurrentHandlePacketDirect(t *testing.T) {
    conn := newTestConnection()

    // Send many packets concurrently
    var wg sync.WaitGroup
    for i := 0; i < 1000; i++ {
        wg.Add(1)
        go func(seqNum int) {
            defer wg.Done()
            p := createTestPacket(seqNum)
            conn.handlePacketDirect(p)
        }(i)
    }

    wg.Wait()
    // Verify connection state is consistent
}

// Test connection close while packets in flight
func TestConnectionCloseDuringPacketProcessing(t *testing.T) {
    conn := newTestConnection()

    // Start sending packets
    go func() {
        for i := 0; i < 10000; i++ {
            p := createTestPacket(i)
            conn.handlePacketDirect(p)
        }
    }()

    // Close connection while packets are being processed
    time.Sleep(100 * time.Millisecond)
    conn.Close()

    // Verify no panics, no deadlocks
}
```

**Expected Outcome**:
- Race detector will identify any data races
- Stress tests will reveal timing issues
- Can validate that connection close is safe

### Option 4: Add Timeout to handlePacket() ⭐ **LOW-MEDIUM PRIORITY**

**Implementation**:
- Wrap `handlePacket()` call with a timeout
- If timeout expires, log error and release lock
- Track timeout events

**Why This Helps**:
- **Prevents indefinite blocking** - if `handlePacket()` gets stuck, we can recover
- **Allows connection to continue processing** other packets

**Concerns**:
- **Complexity** - requires goroutine management
- **Risk** - might mask real bugs instead of fixing them
- **Performance** - adds overhead to hot path

**Implementation Details**:
```go
func (c *srtConn) handlePacketDirect(p packet.Packet) {
    c.handlePacketMutex.Lock()
    defer c.handlePacketMutex.Unlock()

    // Use context with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    done := make(chan struct{})
    go func() {
        defer close(done)
        c.handlePacket(p)
    }()

    select {
    case <-done:
        // Normal completion
    case <-ctx.Done():
        // Timeout - log and continue
        c.log("connection:handlePacket:timeout", func() string {
            return "handlePacket() exceeded 5s timeout"
        })
        // Note: handlePacket() is still running in goroutine
        // This is a problem - we've released the lock but handlePacket() is still running
    }
}
```

**Note**: This approach has a **critical flaw** - if we timeout and release the lock, `handlePacket()` is still running in the background, which could cause state corruption. This needs careful design.

**Also Note**: Panics are unlikely to be the issue here, as a panic would crash the entire server process, not just affect one connection. Since other connections continued working, we can rule out panics as a cause.

### Option 5: Add Packet Processing Statistics ⭐ **MEDIUM PRIORITY**

**Implementation**:
- Track packets received vs packets processed per connection
- Track time spent in `handlePacketDirect()`
- Track lock contention metrics

**Why This Helps**:
- **Identifies processing bottlenecks** - if packets are received but not processed
- **Shows lock contention** - if lock is frequently held
- **Historical data** - can see trends leading up to failure

**Implementation Details**:
```go
type srtConn struct {
    // ... existing fields ...
    packetsReceived   atomic.Uint64  // Packets received from network
    packetsProcessed  atomic.Uint64  // Packets successfully processed
    handlePacketTime  atomic.Int64   // Total time spent in handlePacket (microseconds)
    lockWaitTime      atomic.Int64   // Total time waiting for lock (microseconds)
}

func (c *srtConn) handlePacketDirect(p packet.Packet) {
    start := time.Now()
    c.packetsReceived.Add(1)

    // Track lock wait time
    lockStart := time.Now()
    c.handlePacketMutex.Lock()
    lockWait := time.Since(lockStart)
    c.lockWaitTime.Add(lockWait.Microseconds())

    defer func() {
        processTime := time.Since(start)
        c.handlePacketTime.Add(processTime.Microseconds())
        c.handlePacketMutex.Unlock()
        c.packetsProcessed.Add(1)
    }()

    c.handlePacket(p)
}
```

**Expected Outcome**:
- Can see if `packetsReceived` > `packetsProcessed` (packets being dropped)
- Can see if `handlePacketTime` is increasing (processing getting slower)
- Can see if `lockWaitTime` is high (lock contention)

## Recommended Implementation Order

1. **Option 1: Lock Monitoring Goroutine** ⭐⭐⭐
   - **Why first**: Directly tests the most likely hypothesis
   - **Effort**: Medium (1-2 hours)
   - **Risk**: Low (additive, doesn't change existing behavior)
   - **Value**: High (immediate detection of stuck locks)

2. **Option 2: Enhanced Error Logging** ⭐⭐
   - **Why second**: Identifies silent packet drops
   - **Effort**: Low (30 minutes)
   - **Risk**: Very low (only adds logging)
   - **Value**: High (shows where packets are lost)

3. **Option 5: Packet Processing Statistics** ⭐
   - **Why third**: Provides baseline metrics for monitoring
   - **Effort**: Medium (1 hour)
   - **Risk**: Low (additive)
   - **Value**: Medium (helps with ongoing monitoring)

4. **Option 3: Race Condition Testing** ⭐⭐
   - **Why fourth**: Validates thread safety, but may not catch the specific issue
   - **Effort**: High (2-4 hours)
   - **Risk**: Low (testing only)
   - **Value**: Medium (catches potential issues, but may not be the root cause)

5. **Option 4: Timeout to handlePacket()** ⭐
   - **Why last**: Complex, risky, might mask real bugs
   - **Effort**: High (2-3 hours)
   - **Risk**: High (could introduce new bugs)
   - **Value**: Low (workaround, not a fix)

## Next Steps

1. **Immediate**: Implement Option 1 (Lock Monitoring) - this will quickly confirm or rule out the stuck lock hypothesis
2. **Short-term**: Implement Option 2 (Enhanced Logging) - this will show if packets are being dropped silently
3. **Medium-term**: Implement Option 5 (Statistics) - this provides ongoing monitoring
4. **Long-term**: Implement Option 3 (Race Testing) - this validates overall thread safety

## Questions to Answer

1. **Is the lock stuck?** (Option 1 will answer) - **Most likely cause**
2. **Are packets being dropped?** (Option 2 will answer) - **Second most likely**
3. **Is handlePacket() getting slower or blocking?** (Option 5 will answer) - **Could explain stuck lock**
4. **Is there a race condition?** (Option 3 will answer) - **Less likely but worth checking**

## Ruled Out Scenarios

- **Server panic**: Server was processing other connections, so no panic occurred
- **io_uring completion handler failure**: Same handler processes all connections, so if it failed, all connections would fail
- **Network stack failure**: Network traffic was still arriving (Grafana shows traffic), and other connections worked

## Most Likely Root Cause

Given the evidence:
- Network traffic arriving (Grafana)
- Other connections working
- Client sending packets
- Server not processing packets for this specific connection

**The most likely cause is `handlePacketMutex` being held indefinitely**, preventing `handlePacketDirect()` from processing new packets. This would:
- Block all packets for this connection (waiting for lock)
- Not affect other connections (each has its own mutex)
- Cause peer idle timeout (no packets processed = no timer reset)
- Allow server to continue processing other connections

Once we have answers to these questions, we can determine the root cause and implement a proper fix.

