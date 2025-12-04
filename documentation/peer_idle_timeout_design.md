# Peer Idle Timeout Design

## Overview

The peer idle timeout is a critical SRT feature that closes a connection if no packets are received from the peer within a configurable duration (default: 2 seconds). This timeout must be reset on every received packet, making it part of the **hot path** of the system.

## Current Implementation (Phase 7 - Context-Based)

### Implementation Details

**Location**: `connection.go`

**Current Approach**:
- Uses `context.WithTimeout()` wrapping connection context
- Requires `sync.Mutex` (`peerIdleTimeoutLock`) to protect context/cancel function
- `resetPeerIdleTimeout()` is called on every packet (hot path)
- `watchPeerIdleTimeout()` goroutine watches for timeout

**Code Structure**:
```go
type srtConn struct {
    peerIdleTimeoutCtx       context.Context
    peerIdleTimeoutCancel    context.CancelFunc
    peerIdleTimeoutLock      sync.Mutex  // ⚠️ Lock in hot path
    peerIdleTimeoutLastReset time.Time
}

func (c *srtConn) resetPeerIdleTimeout() {
    c.peerIdleTimeoutLock.Lock()  // ⚠️ Lock acquisition in hot path
    defer c.peerIdleTimeoutLock.Unlock()

    if c.peerIdleTimeoutCancel != nil {
        c.peerIdleTimeoutCancel()
    }
    c.peerIdleTimeoutCtx, c.peerIdleTimeoutCancel = context.WithTimeout(c.ctx, c.config.PeerIdleTimeout)
    c.peerIdleTimeoutLastReset = time.Now()
}
```

### Performance Concerns

1. **Mutex Lock in Hot Path**: `resetPeerIdleTimeout()` is called on every received packet (line 859, 964 in `handlePacket()` and `handleKeepAlive()`)
2. **Context Creation Overhead**: Creating new contexts on every packet reset has overhead
3. **Goroutine Coordination**: Requires coordination between packet handler and watcher goroutine
4. **Lock Contention**: With many connections, mutex contention could become a bottleneck

### Pros
- ✅ Respects context cancellation (signal cancellation cancels timeout)
- ✅ Integrates with context hierarchy
- ✅ Go idiomatic (uses context)

### Cons
- ❌ **Mutex lock in hot path** (performance concern)
- ❌ Context creation overhead on every packet
- ❌ More complex than necessary for this use case
- ❌ Potential lock contention with many connections

---

## Design Option 1: Atomic Counter + Simple Timer (Recommended)

### Concept

Use a **single atomic counter** (`PktRecvSuccess`) to track received packets, and a simple `time.Timer` that checks the counter periodically and on expiration. This eliminates the need to sum multiple counters, reducing the hot path to a single atomic load operation.

### Implementation

```go
type srtConn struct {
    // Remove: peerIdleTimeoutCtx, peerIdleTimeoutCancel, peerIdleTimeoutLock
    peerIdleTimeout          *time.Timer
    peerIdleTimeoutLastReset time.Time

    // Use existing atomic counter from metrics
    // PktRecvData, PktRecvACK, PktRecvNAK, etc. are already tracked atomically
}

// resetPeerIdleTimeout resets the timer (called on every packet - hot path)
func (c *srtConn) resetPeerIdleTimeout() {
    // No lock needed - just reset the timer
    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
    c.peerIdleTimeoutLastReset = time.Now()
}

// watchPeerIdleTimeout watches for timeout using atomic counter checks
func (c *srtConn) watchPeerIdleTimeout() {
    defer c.connWg.Done()

    // Get initial packet count
    initialCount := c.getTotalReceivedPackets()

    // Determine ticker interval based on timeout duration
    // For longer timeouts (>6s), check more frequently (1/4) for better responsiveness
    // For shorter timeouts (<=6s), check at 1/2 interval
    tickerInterval := c.config.PeerIdleTimeout / 2
    if c.config.PeerIdleTimeout > 6*time.Second {
        tickerInterval = c.config.PeerIdleTimeout / 4
    }
    ticker := time.NewTicker(tickerInterval)
    defer ticker.Stop()

    for {
        select {
        case <-c.peerIdleTimeout.C:
            // Timer expired - check if packets were received
            currentCount := c.getTotalReceivedPackets()
            if currentCount == initialCount {
                // No packets received - timeout occurred
                c.log("connection:close:reason", func() string {
                    return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
                })
                go c.close()
                return
            }
            // Packets were received - will reset timer after select

        case <-ticker.C:
            // Periodic check (1/2 timeout for <=6s, 1/4 timeout for >6s)
            // Will check counter and reset if needed after select

        case <-c.ctx.Done():
            // Connection closing
            return
        }

        // Check if packets were received (common logic for both timer and ticker)
        // This is executed after the select, making the code more DRY and Go-idiomatic
        currentCount := c.getTotalReceivedPackets()
        if currentCount > initialCount {
            // Packets received - reset timer and update count
            initialCount = currentCount
            c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
            c.peerIdleTimeoutLastReset = time.Now()
        }
    }
}
```

**Refactoring Benefits**:
- ✅ **DRY (Don't Repeat Yourself)**: Timer reset logic is in one place instead of duplicated in both select cases
- ✅ **Go Idiomatic**: Common pattern of handling shared logic after a select statement
- ✅ **Maintainability**: Changes to reset logic only need to be made in one place
- ✅ **Readability**: Clearer separation between event handling (select) and state updates (after select)

// getTotalReceivedPackets returns total received packets (atomic read)
// This counts all packets that successfully reached the connection, indicating peer is alive
func (c *srtConn) getTotalReceivedPackets() uint64 {
    if c.metrics == nil {
        return 0
    }
    // Single atomic load - much faster than summing 8 counters
    // PktRecvSuccess is incremented for ANY successful packet (data or control)
    // This counter is incremented in IncrementRecvMetrics() right after the !success check
    return c.metrics.PktRecvSuccess.Load()
}
```

### Pros
- ✅ **No locks in hot path** - `timer.Reset()` is lock-free
- ✅ **Atomic counter reads** - fast, no contention
- ✅ **Simple and efficient** - uses standard `time.Timer`
- ✅ **Periodic checks** - catches resets even if timer.Reset() is missed (adaptive interval: 1/2 for <=6s, 1/4 for >6s)
- ✅ **Leverages existing metrics** - reuses atomic counters we already have
- ✅ **Respects context cancellation** - watcher exits on context cancel

### Cons
- ⚠️ `timer.Reset()` must be called on stopped or expired timer (Go requirement)
- ⚠️ Need to ensure timer is properly initialized before first use
- ⚠️ Counter-based approach means we're checking "did packets arrive" rather than "when did last packet arrive"

### Performance Characteristics
- **Hot path**: `timer.Reset()` - O(1), lock-free, very fast
- **Watcher**: Periodic atomic reads - O(1), no contention
- **Memory**: Single timer + ticker, minimal overhead

---

## Design Option 2: Atomic Timestamp + Timer

### Concept

Store the last packet receive time as an atomic timestamp, and have the watcher check this timestamp.

### Implementation

```go
type srtConn struct {
    peerIdleTimeout          *time.Timer
    peerIdleTimeoutLastReset atomic.Int64  // Unix nano timestamp (atomic)
}

func (c *srtConn) resetPeerIdleTimeout() {
    // Update atomic timestamp (hot path - lock-free)
    c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())
    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
}

func (c *srtConn) watchPeerIdleTimeout() {
    defer c.connWg.Done()

    ticker := time.NewTicker(c.config.PeerIdleTimeout / 2)
    defer ticker.Stop()

    for {
        select {
        case <-c.peerIdleTimeout.C:
            // Timer expired - check timestamp
            lastReset := time.Unix(0, c.peerIdleTimeoutLastReset.Load())
            elapsed := time.Since(lastReset)
            if elapsed >= c.config.PeerIdleTimeout {
                // Timeout occurred
                c.log("connection:close:reason", func() string {
                    return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
                })
                go c.close()
                return
            }
            // Reset timer (packets were received)
            c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)

        case <-ticker.C:
            // Periodic check
            lastReset := time.Unix(0, c.peerIdleTimeoutLastReset.Load())
            elapsed := time.Since(lastReset)
            if elapsed < c.config.PeerIdleTimeout {
                // Reset timer if needed
                remaining := c.config.PeerIdleTimeout - elapsed
                c.peerIdleTimeout.Reset(remaining)
            }

        case <-c.ctx.Done():
            return
        }
    }
}
```

### Pros
- ✅ **No locks in hot path** - atomic timestamp update
- ✅ **Precise timing** - tracks exact last receive time
- ✅ **Simple timer management** - standard `time.Timer`

### Cons
- ⚠️ Requires atomic timestamp (additional field)
- ⚠️ More complex timer reset logic (calculating remaining time)
- ⚠️ Still need to handle `timer.Reset()` requirements

---

## Design Option 3: Channel-Based Reset Signal

### Concept

Use a channel to signal timer resets, avoiding locks but using channel operations.

### Implementation

```go
type srtConn struct {
    peerIdleTimeout          *time.Timer
    peerIdleTimeoutReset    chan struct{}  // Buffered channel for reset signals
    peerIdleTimeoutLastReset time.Time
}

func (c *srtConn) resetPeerIdleTimeout() {
    // Non-blocking send (hot path)
    select {
    case c.peerIdleTimeoutReset <- struct{}{}:
        // Reset signal sent
    default:
        // Channel full - watcher will catch it on next check
    }
    c.peerIdleTimeoutLastReset = time.Now()
}

func (c *srtConn) watchPeerIdleTimeout() {
    defer c.connWg.Done()

    for {
        select {
        case <-c.peerIdleTimeout.C:
            // Timer expired - check if reset was sent
            select {
            case <-c.peerIdleTimeoutReset:
                // Reset signal received - reset timer
                c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
            default:
                // No reset - timeout occurred
                c.log("connection:close:reason", func() string {
                    return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
                })
                go c.close()
                return
            }

        case <-c.peerIdleTimeoutReset:
            // Reset signal - reset timer
            c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
            c.peerIdleTimeoutLastReset = time.Now()

        case <-c.ctx.Done():
            return
        }
    }
}
```

### Pros
- ✅ **No locks** - uses channels (Go's preferred communication)
- ✅ **Non-blocking sends** - won't block hot path
- ✅ **Go idiomatic** - uses channels for coordination

### Cons
- ❌ **Channel overhead** - channel operations have overhead
- ❌ **Potential signal loss** - if channel is full, reset signal might be missed
- ❌ **More complex** - requires channel management
- ❌ **Memory overhead** - buffered channel per connection

---

## Design Option 4: Hybrid - Atomic Counter + Timer (Simplified)

### Concept

Simplified version of Option 1: Use atomic counter, but only check on timer expiration (no periodic checks).

### Implementation

```go
type srtConn struct {
    peerIdleTimeout          *time.Timer
    peerIdleTimeoutLastReset time.Time
    // Use existing metrics.PktRecvData, etc. (atomic)
}

func (c *srtConn) resetPeerIdleTimeout() {
    // Hot path - just reset timer (lock-free)
    c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
    c.peerIdleTimeoutLastReset = time.Now()
}

func (c *srtConn) watchPeerIdleTimeout() {
    defer c.connWg.Done()

    initialCount := c.getTotalReceivedPackets()

    for {
        select {
        case <-c.peerIdleTimeout.C:
            // Timer expired - check counter
            currentCount := c.getTotalReceivedPackets()
            if currentCount == initialCount {
                // No packets received - timeout
                c.log("connection:close:reason", func() string {
                    return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
                })
                go c.close()
                return
            }
            // Packets received - reset
            initialCount = currentCount
            c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
            c.peerIdleTimeoutLastReset = time.Now()

        case <-c.ctx.Done():
            return
        }
    }
}
```

### Pros
- ✅ **Simplest approach** - minimal code
- ✅ **No locks in hot path**
- ✅ **No periodic checks** - only checks on timer expiration
- ✅ **Atomic counter reads** - fast

### Cons
- ⚠️ **Less responsive** - only checks on timer expiration (no 1/2 timeout check)
- ⚠️ If `timer.Reset()` fails (timer already expired), might miss timeout

---

## Recommended Solution: Option 1 (Atomic Counter + Timer with Periodic Checks)

### Rationale

1. **Performance**: No locks in hot path - `timer.Reset()` is lock-free and very fast
2. **Reliability**: Periodic checks at 1/2 timeout catch missed resets
3. **Simplicity**: Uses standard Go `time.Timer` - well-understood and tested
4. **Leverages Existing Infrastructure**: Reuses atomic counters from metrics system
5. **Go Idiomatic**: Uses standard library patterns

### Implementation Details

**Counter Selection**:
- **New counter**: `PktRecvSuccess` - Single atomic counter for ANY successful packet (data or control)
- This counter is incremented in `IncrementRecvMetrics()` **immediately after** the `if !success {` block
- This means it tracks packets that successfully arrived from the network and were parsed
- Any packet from the peer (data or control) that successfully reaches the connection should reset the timeout
- **Performance benefit**: Single atomic load instead of summing 8 separate counters
- **Additional counters** (defensive programming, should never increment):
  - `PktRecvNil` - Tracks edge case where `p == nil`
  - `PktRecvControlUnknown` - Tracks unknown control packet types (instead of defaulting to handshake)
  - `PktRecvSubTypeUnknown` - Tracks unknown USER packet subtypes (instead of defaulting to handshake)

**Timer Reset Behavior**:
- `timer.Reset()` must be called on a stopped or expired timer (Go requirement)
- If timer is already expired when `Reset()` is called, it will fire immediately
- Our watcher handles this by checking the counter when timer fires

**Periodic Checks**:
- Check interval is adaptive based on timeout duration:
  - For timeouts <= 6 seconds: Check at 1/2 timeout interval (e.g., every 1 second for 2-second timeout)
  - For timeouts > 6 seconds: Check at 1/4 timeout interval (e.g., every 2 seconds for 8-second timeout)
- If counter has incremented, reset the timer
- This provides redundancy in case `timer.Reset()` is called incorrectly
- More frequent checks for longer timeouts improve responsiveness

### Migration Path

1. Revert context-based implementation
2. Restore `*time.Timer` field
3. Implement `getTotalReceivedPackets()` helper
4. Update `watchPeerIdleTimeout()` to use atomic counter checks
5. Remove `peerIdleTimeoutLock` mutex

---

## Performance Comparison

| Approach | Hot Path (reset) | Lock Contention | Memory | Complexity |
|----------|-----------------|-----------------|--------|------------|
| **Current (Context)** | Mutex lock + context creation | High | Medium | High |
| **Option 1 (Atomic + Timer)** | `timer.Reset()` (lock-free) | None | Low | Low |
| **Option 2 (Atomic Timestamp)** | Atomic store + `timer.Reset()` | None | Low | Medium |
| **Option 3 (Channel)** | Non-blocking channel send | None | Medium | Medium |
| **Option 4 (Simplified)** | `timer.Reset()` (lock-free) | None | Low | Very Low |

**Winner**: Option 1 or Option 4 (depending on whether periodic checks are desired)

---

## Recommendation

**Use Option 1 (Atomic Counter + Timer with Periodic Checks)** because:
1. Eliminates mutex lock from hot path
2. Provides redundancy with periodic checks
3. Leverages existing atomic counters
4. Simple and maintainable
5. Respects context cancellation (watcher exits on `c.ctx.Done()`)

**Alternative**: If periodic checks are not desired, Option 4 (Simplified) is also acceptable and even simpler.

---

## Context Cancellation Consideration

**Question**: Do we need the peer idle timeout to respect context cancellation?

**Answer**: Yes, but only for the watcher goroutine. The timeout itself should fire based on packet reception, not context cancellation. However, the watcher should exit when the connection context is cancelled (connection closing).

**Solution**: The watcher checks `c.ctx.Done()` and exits gracefully. The timer itself doesn't need to be cancelled - if the connection is closing, the watcher will exit and the timer will be stopped in `close()`.

---

## Implementation Notes

1. **Timer Initialization**: Must initialize timer before starting watcher goroutine
2. **Timer.Reset() Requirements**: Must be called on stopped or expired timer
3. **Counter Selection**:
   - **New counter**: `PktRecvSuccess` - Single atomic counter incremented for ANY successful packet
   - This counter is incremented in `IncrementRecvMetrics()` **immediately after** the `if !success {` block
   - This means it tracks packets that successfully arrived from the network, which is exactly what we need
   - **Performance**: Single atomic load instead of summing 8 separate counters
   - **Additional counter**: `PktRecvNil` - Tracks edge case where `p == nil` (defensive programming)
   - Do NOT use "Dropped" or "Error" counters - those track packets that failed or were dropped
4. **Race Conditions**: Atomic counter reads are safe, but need to ensure timer operations are thread-safe (they are - `timer.Reset()` is safe to call from any goroutine)

---

## Testing Considerations

1. **Test timeout expiration**: Verify connection closes when no packets received
2. **Test timer reset**: Verify timer resets on packet reception
3. **Test periodic checks**: Verify periodic checks catch missed resets
4. **Test context cancellation**: Verify watcher exits on context cancel
5. **Performance test**: Measure hot path overhead (should be minimal)

---

## Conclusion

The context-based approach (Phase 7) was well-intentioned but introduces unnecessary complexity and performance overhead for this specific use case. The peer idle timeout is a simple "reset on packet, expire if no packets" mechanism that doesn't need the full context hierarchy.

**Recommended**: Revert to `time.Timer` with **single atomic counter** (`PktRecvSuccess`) for optimal performance. This reduces the hot path from 8 atomic loads to 1 atomic load.

---

## Code Changes Required

### 1. Add New Counters to `ConnectionMetrics`

```go
// metrics/metrics.go
type ConnectionMetrics struct {
    // ... existing counters ...

    // Single counter for all successful receives (for peer idle timeout)
    PktRecvSuccess atomic.Uint64

    // Edge case tracking (should never increment, but defensive programming)
    PktRecvNil atomic.Uint64
    PktRecvControlUnknown atomic.Uint64  // Unknown control packet types
    PktRecvSubTypeUnknown atomic.Uint64   // Unknown USER packet subtypes
}
```

### 2. Update `IncrementRecvMetrics()` in `metrics/packet_classifier.go`

**Changes**:
1. Add `PktRecvNil` counter increment when `p == nil`
2. Add `PktRecvSuccess` counter increment immediately after `if !success {` block
3. Refactor control packet logic: handle data packets first (early return), then switch for control packets
4. Add `PktRecvControlUnknown` for unknown control types (instead of defaulting to handshake)
5. Add `PktRecvSubTypeUnknown` for unknown USER subtypes (instead of defaulting to handshake)
4. Add `PktRecvControlUnknown` for unknown control types (instead of defaulting to handshake)
5. Add `PktRecvSubTypeUnknown` for unknown USER subtypes (instead of defaulting to handshake)

**Proposed Code**:
```go
func IncrementRecvMetrics(m *ConnectionMetrics, p packet.Packet, isIoUring bool, success bool, dropReason DropReason) {
    if m == nil {
        return
    }

    // Track path
    if isIoUring {
        m.PktRecvIoUring.Add(1)
    } else {
        m.PktRecvReadFrom.Add(1)
    }

    if p == nil {
        // Track nil packet edge case (should never happen, but defensive programming)
        m.PktRecvNil.Add(1)

        // No packet - can't classify type, but track error
        // For parse errors, we typically don't have packet info
        if !success {
            // If we have a specific drop reason, use it; otherwise default to parse error
            if dropReason == DropReasonParse {
                m.PktRecvErrorParse.Add(1)
            } else if dropReason != 0 {
                // Unknown drop reason when we have no packet
                m.PktRecvErrorUnknown.Add(1)
            } else {
                // No drop reason specified - assume parse error (most common when p == nil)
                m.PktRecvErrorParse.Add(1)
            }
        }
        return
    }

    h := p.Header()
    pktLen := uint64(p.Len())

    if !success {
        // Track error/drop using granular counters
        // We have packet (already checked above) - use granular error drop counter
        isData := !h.IsControlPacket
        IncrementRecvErrorDrop(m, p, dropReason, isData)
        // Also track legacy counters for backward compatibility
        switch dropReason {
        case DropReasonParse:
            m.PktRecvErrorParse.Add(1)
        case DropReasonRoute:
            m.PktRecvErrorRoute.Add(1)
        case DropReasonEmpty:
            m.PktRecvErrorEmpty.Add(1)
        case DropReasonUnknownSocket:
            m.PktRecvUnknownSocketId.Add(1)
        case DropReasonNilConnection:
            m.PktRecvNilConnection.Add(1)
        case DropReasonWrongPeer:
            m.PktRecvWrongPeer.Add(1)
        case DropReasonBacklogFull:
            m.PktRecvBacklogFull.Add(1)
        case DropReasonQueueFull:
            m.PktRecvQueueFull.Add(1)
        default:
            // Unknown drop reason - track as unknown error
            m.PktRecvErrorUnknown.Add(1)
        }
        return
    }

    // Success case - increment single success counter (for peer idle timeout)
    // This is done immediately after the !success check for performance
    m.PktRecvSuccess.Add(1)

    // Classify by packet type (for detailed metrics)
    // Handle data packets first (early return) to reduce nesting
    if !h.IsControlPacket {
        // Data packet
        m.PktRecvDataSuccess.Add(1)
        m.ByteRecvDataSuccess.Add(pktLen)
        return
    }

    // Control packet - switch on control type
    switch h.ControlType {
    case packet.CTRLTYPE_ACK:
        m.PktRecvACKSuccess.Add(1)
        m.ByteRecvDataSuccess.Add(pktLen) // ACK packets have data too
    case packet.CTRLTYPE_ACKACK:
        m.PktRecvACKACKSuccess.Add(1)
    case packet.CTRLTYPE_NAK:
        m.PktRecvNAKSuccess.Add(1)
    case packet.CTRLTYPE_KEEPALIVE:
        m.PktRecvKeepaliveSuccess.Add(1)
    case packet.CTRLTYPE_SHUTDOWN:
        m.PktRecvShutdownSuccess.Add(1)
    case packet.CTRLTYPE_HANDSHAKE:
        m.PktRecvHandshakeSuccess.Add(1)
    case packet.CTRLTYPE_USER:
        // USER packets can be KM (key material) - check SubType
        switch h.SubType {
        case packet.EXTTYPE_KMREQ, packet.EXTTYPE_KMRSP:
            m.PktRecvKMSuccess.Add(1)
        default:
            // Unknown USER subtype - track separately (should never happen, but defensive programming)
            m.PktRecvSubTypeUnknown.Add(1)
        }
    default:
        // Unknown control type - track separately (should never happen, but defensive programming)
        m.PktRecvControlUnknown.Add(1)
    }
}
```

**Benefits of Refactoring**:
- ✅ **Single atomic load** in `getTotalReceivedPackets()` - much faster
- ✅ **Reduced nesting** - data packets handled first with early return
- ✅ **Defensive programming** - `PktRecvNil`, `PktRecvControlUnknown`, and `PktRecvSubTypeUnknown` track edge cases
- ✅ **Code clarity** - control packet switch is no longer nested in if block
- ✅ **Better observability** - unknown packet types are tracked separately instead of being misclassified

### 3. Update `getTotalReceivedPackets()` in `connection.go`

```go
// getTotalReceivedPackets returns total received packets (atomic read)
// This counts all packets that successfully reached the connection, indicating peer is alive
func (c *srtConn) getTotalReceivedPackets() uint64 {
    if c.metrics == nil {
        return 0
    }
    // Single atomic load - much faster than summing 8 counters
    return c.metrics.PktRecvSuccess.Load()
}
```

### 4. Update Prometheus HTTP Handler in `metrics/handler.go`

**New Metrics to Add**:

The following new counters need to be exposed in the Prometheus HTTP handler:

```go
// In MetricsHandler() function, add these metrics for each connection:

// Single success counter (for peer idle timeout)
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvSuccess.Load(),
    "socket_id", socketIdStr, "type", "all", "status", "success")

// Edge case counters (should be 0, but track for debugging)
writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvNil.Load(),
    "socket_id", socketIdStr, "type", "nil", "status", "error")

writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvControlUnknown.Load(),
    "socket_id", socketIdStr, "type", "control_unknown", "status", "error")

writeCounterValue(b, "gosrt_connection_packets_received_total",
    metrics.PktRecvSubTypeUnknown.Load(),
    "socket_id", socketIdStr, "type", "subtype_unknown", "status", "error")
```

**Prometheus Metric Naming**:
- **Metric Name**: `gosrt_connection_packets_received_total`
- **Labels**:
  - `socket_id`: Connection socket ID (hex format: `0x%08x`)
  - `type`: Packet type (`all`, `nil`, `control_unknown`, `subtype_unknown`, or existing types like `ack`, `nak`, etc.)
  - `status`: Status (`success` or `error`)

**Rationale**:
- `PktRecvSuccess`: Aggregated counter for all successful receives (used by peer idle timeout)
- `PktRecvNil`: Tracks nil packet edge case (should never increment)
- `PktRecvControlUnknown`: Tracks unknown control packet types (should never increment)
- `PktRecvSubTypeUnknown`: Tracks unknown USER packet subtypes (should never increment)

**Monitoring**:
- These counters should remain at 0 in normal operation
- If they increment, it indicates:
  - A bug in packet parsing/classification
  - An unexpected packet type from the peer
  - A protocol violation or implementation issue
- Alerting can be set up to notify when these counters are non-zero

**Example Prometheus Query**:
```promql
# Check for any edge case packets
gosrt_connection_packets_received_total{type=~"nil|control_unknown|subtype_unknown"} > 0

# Total successful receives (for peer idle timeout monitoring)
sum(gosrt_connection_packets_received_total{type="all",status="success"}) by (socket_id)
```

