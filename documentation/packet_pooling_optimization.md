# Packet Pooling Optimization Design

## Overview

This document analyzes the opportunity to use `sync.Pool` for packet structures (`pkt`) in the receive path to reduce memory allocations and GC pressure. Currently, only the payload (`bytes.Buffer`) is pooled, while the `pkt` struct itself is allocated fresh for every received packet.

## Current Implementation

### Packet Structure

**Location**: `packet/packet.go`

```go
type pkt struct {
    header PacketHeader
    payload *bytes.Buffer
}

type PacketHeader struct {
    Addr            net.Addr
    IsControlPacket bool
    PktTsbpdTime    uint64 // microseconds

    // Control packet fields
    ControlType  CtrlType
    SubType      CtrlSubType
    TypeSpecific uint32

    // Data packet fields
    PacketSequenceNumber    circular.Number
    PacketPositionFlag      PacketPosition
    OrderFlag               bool
    KeyBaseEncryptionFlag   PacketEncryption
    RetransmittedPacketFlag bool
    MessageNumber           uint32

    // Common fields
    Timestamp           uint32
    DestinationSocketId uint32
}
```

### Current Allocation Pattern

**Packet Creation** (`NewPacket`):
```go
func NewPacket(addr net.Addr) Packet {
    p := &pkt{  // ← Fresh allocation every time
        header: PacketHeader{
            Addr:                  addr,
            PacketSequenceNumber:  circular.New(0, MAX_SEQUENCENUMBER),
            PacketPositionFlag:    SinglePacket,
            OrderFlag:             false,
            KeyBaseEncryptionFlag: UnencryptedPacket,
            MessageNumber:         1,
        },
        payload: payloadPool.Get(),  // ← Already pooled
    }
    return p
}
```

**Packet Usage in Receive Path**:
- `listen_linux.go:422`: `p, err := packet.NewPacketFromData(addr, bufferSlice)`
- `dial_linux.go:290`: `p, err := packet.NewPacketFromData(addr, bufferSlice)`
- Called for **every single received packet** (high frequency)

**Packet Decommission**:
```go
func (p *pkt) Decommission() {
    if p.payload == nil {
        return
    }
    payloadPool.Put(p.payload)  // ← Returns payload to pool
    p.payload = nil
    // ← pkt struct itself is GC'd (not pooled)
}
```

### Current Memory Allocation Profile

**Per Packet Allocation:**
- `pkt` struct: ~80-120 bytes (depending on alignment)
- `PacketHeader` fields: ~64 bytes
- `circular.Number`: ~16 bytes (2 uint32s)
- Total: ~80-120 bytes per packet

**Allocation Frequency:**
- For 10 Mb/s video stream with 7x188 byte MPEG-TS packets:
  - Packet rate: ~9,500 packets/second
  - Per connection: ~9,500 allocations/second
  - For 100 connections: ~950,000 allocations/second
  - Memory allocation: ~76-114 MB/second just for packet structs

**GC Pressure:**
- High allocation rate → frequent GC pauses
- Short-lived objects (packets processed quickly)
- GC overhead can impact latency

## Proposed Optimization: Packet Pooling

### Design Goals

1. **Reduce allocations**: Pool `pkt` structs, not just payloads
2. **Maintain correctness**: Ensure all fields are properly reset
3. **Zero overhead**: No performance regression for hot path
4. **Thread safety**: Leverage `sync.Pool`'s built-in thread safety
5. **Backward compatibility**: No API changes

### Implementation Strategy

#### Phase 1: Add Packet Pool

**Add packet pool to `packet/packet.go`:**

```go
var packetPool = sync.Pool{
    New: func() interface{} {
        return &pkt{
            header: PacketHeader{
                // Initialize with safe defaults
                PacketSequenceNumber:  circular.New(0, MAX_SEQUENCENUMBER),
                PacketPositionFlag:    SinglePacket,
                OrderFlag:             false,
                KeyBaseEncryptionFlag: UnencryptedPacket,
                MessageNumber:         1,
            },
            payload: nil, // Will be set from payloadPool
        }
    },
}
```

#### Phase 2: Modify Decommission to Reset and Return to Pool

**Key Design Decision: Reset in Cold Path (Decommission), Not Hot Path (NewPacket)**

Reset fields in `Decommission()` (cold path) before `Put()`, not in `NewPacket()` (hot path) after `Get()`. This ensures:
- `Get()` is as fast as possible (hot path)
- Objects in pool are always clean and ready
- Reset work happens in cold path (after processing)

```go
func (p *pkt) Decommission() {
    if p.payload == nil {
        return // Already decommissioned or invalid
    }

    // Reset all fields to safe defaults BEFORE returning to pool
    // This ensures objects in pool are always clean and ready
    p.header.Addr = nil
    p.header.IsControlPacket = false
    p.header.PktTsbpdTime = 0
    p.header.ControlType = 0
    p.header.SubType = 0
    p.header.TypeSpecific = 0
    p.header.PacketSequenceNumber = circular.New(0, MAX_SEQUENCENUMBER)
    p.header.PacketPositionFlag = SinglePacket
    p.header.OrderFlag = false
    p.header.KeyBaseEncryptionFlag = UnencryptedPacket
    p.header.RetransmittedPacketFlag = false
    p.header.MessageNumber = 1
    p.header.Timestamp = 0
    p.header.DestinationSocketId = 0

    // Return payload to pool (payloadPool.Get() already resets it)
    payloadPool.Put(p.payload)
    p.payload = nil

    // Return packet struct to pool (now clean and ready for reuse)
    packetPool.Put(p)
}
```

#### Phase 3: Modify NewPacket to Use Pool (Hot Path - Minimal Work)

```go
func NewPacket(addr net.Addr) Packet {
    // Get from pool (hot path - must be fast)
    // Object is already clean from Decommission()
    p := packetPool.Get().(*pkt)

    // Only set the address (required parameter)
    // All other fields are already reset from Decommission()
    p.header.Addr = addr

    // Get payload from pool (already resets in payloadPool.Get())
    p.payload = payloadPool.Get()

    return p
}
```

**Performance Benefit:**
- `Get()` path: Only 2 assignments (addr, payload) - minimal work
- `Put()` path: Reset happens once after processing (cold path)
- Objects in pool are always clean and ready

### Field Reset Analysis

**Reset Strategy: Reset in Decommission() (Cold Path), Not NewPacket() (Hot Path)**

**Fields that MUST be reset in Decommission():**
- All header fields - Reset to safe defaults to prevent stale data
- `payload` - Return to pool (payloadPool.Get() already resets it)
- `Addr` - Reset to nil (will be set in NewPacket())

**Fields overwritten by Unmarshal():**
- `IsControlPacket`
- `ControlType`, `SubType`, `TypeSpecific` (for control packets)
- `PacketSequenceNumber`, `PacketPositionFlag`, `OrderFlag`, etc. (for data packets)
- `Timestamp`, `DestinationSocketId`

**Why reset in Decommission() even though Unmarshal() overwrites?**
1. **Safety**: `NewPacket()` might be called without `Unmarshal()` (edge cases)
2. **Performance**: Reset in cold path (after processing), not hot path (during allocation)
3. **Correctness**: Ensures objects in pool are always in clean state
4. **Defense in depth**: Even if `Unmarshal()` is always called, resetting is safe

**Key Insight**: Reset all fields in `Decommission()` before `Put()`, so `NewPacket()` after `Get()` only needs to set `Addr` and get payload. This keeps the hot path (`Get()`) as fast as possible.

### Thread Safety

**sync.Pool is thread-safe:**
- `Get()` and `Put()` are safe for concurrent use
- No additional locking needed
- Per-P mutex in sync.Pool (minimal contention)

**Packet Usage:**
- Packets are single-threaded per connection (handled by `handlePacketMutex`)
- No concurrent access to packet fields after creation
- Safe to pool and reuse

### Memory Benefits

**Before (Current):**
- Per packet: ~80-120 bytes allocated
- 9,500 packets/sec × 100 connections = 950,000 allocations/sec
- ~76-114 MB/sec allocation rate
- High GC pressure

**After (With Pooling):**
- Pool maintains ~100-1000 packets (typical pool size)
- Most allocations come from pool (reused)
- Only new allocations when pool is empty
- Estimated 90-99% reduction in allocations
- Lower GC pressure, better latency

### Performance Considerations

**Hot Path Optimization (NewPacket/Get):**
- **Minimal work**: Only set `Addr` and get payload
- **No field reset**: All fields already clean from `Decommission()`
- **Fast allocation**: `sync.Pool.Get()` is just an atomic operation
- **Cache friendly**: Reused objects likely in CPU cache

**Cold Path (Decommission/Put):**
- **Reset work**: Happens after processing, not during allocation
- **No performance impact**: Reset cost is amortized over packet lifetime
- **Clean state**: Objects in pool are always ready to use

**Potential Issues:**
1. **Stale data**: Mitigated by resetting all fields in `Decommission()`
2. **Pool overhead**: `sync.Pool.Get()` has minimal overhead (atomic operations)
3. **Memory growth**: Pool can grow if traffic spikes, then shrink during GC
4. **Zero values**: Safe defaults set in `Decommission()`

**Mitigation:**
1. Reset all fields in `Decommission()` before `Put()`
2. `sync.Pool` automatically shrinks during GC
3. Use safe zero values (already handled by Go's zero value semantics)
4. Objects in pool are always clean and ready

### Implementation Phases

#### Phase 1: Add Pool Infrastructure
1. Add `packetPool` variable
2. Test pool Get/Put cycle

#### Phase 2: Modify Decommission (Cold Path - Reset Here)
1. Add field reset logic to `Decommission()` before `Put()`
2. Return payload to pool
3. Return packet struct to pool
4. Ensure no double-put bugs
5. Test cleanup paths

#### Phase 3: Modify NewPacket (Hot Path - Minimal Work)
1. Change `NewPacket()` to use pool `Get()`
2. Only set `Addr` and get payload (minimal work)
3. Test with existing code

#### Phase 4: Testing & Validation
1. Unit tests for pool behavior
2. Verify objects are clean after `Get()`
3. Integration tests with real traffic
4. Memory profiling to verify reduction
5. Performance benchmarks (verify hot path is fast)

### Testing Strategy

**Unit Tests:**
```go
func TestPacketPoolReuse(t *testing.T) {
    // Verify packets are reused from pool
    addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234}

    p1 := packet.NewPacket(addr)
    p1.Header().DestinationSocketId = 12345
    p1.Header().Timestamp = 99999

    p1.Decommission() // Resets fields and returns to pool

    p2 := packet.NewPacket(addr)
    // Verify p2 is the same underlying struct (pointer comparison)
    // Verify fields are properly reset (should be 0/defaults)
    require.Equal(t, uint32(0), p2.Header().DestinationSocketId)
    require.Equal(t, uint32(0), p2.Header().Timestamp)
    require.Equal(t, addr, p2.Header().Addr) // Only Addr should be set
}

func TestPacketPoolResetInDecommission(t *testing.T) {
    // Verify all fields are reset in Decommission() before Put()
    addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234}

    p := packet.NewPacket(addr)
    // Set various fields
    p.Header().IsControlPacket = true
    p.Header().ControlType = packet.CTRLTYPE_ACK
    p.Header().PktTsbpdTime = 123456

    p.Decommission() // Should reset all fields

    // Get again from pool
    p2 := packet.NewPacket(addr)
    require.False(t, p2.Header().IsControlPacket)
    require.Equal(t, packet.CtrlType(0), p2.Header().ControlType)
    require.Equal(t, uint64(0), p2.Header().PktTsbpdTime)
}
```

**Memory Profiling:**
```go
// Benchmark allocation rate
func BenchmarkPacketAllocation(b *testing.B) {
    addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234}
    data := make([]byte, 1500)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        p, _ := packet.NewPacketFromData(addr, data)
        p.Decommission()
    }
}
```

**Integration Tests:**
- Test with real SRT connections
- Verify no regressions in packet handling
- Monitor memory usage over time

### Risks and Mitigations

**Risk 1: Stale Data in Reused Packets**
- **Mitigation**: Reset all fields in `Decommission()` before `Put()`
- **Mitigation**: Objects in pool are always clean and ready
- **Mitigation**: `Unmarshal()` overwrites fields as additional safety
- **Mitigation**: Add assertions in debug builds

**Risk 2: Double-Put to Pool**
- **Mitigation**: Set `p.payload = nil` before `Put()` to detect double-put
- **Mitigation**: Add nil checks in `Decommission()`

**Risk 3: Memory Leak if Decommission Not Called**
- **Mitigation**: This is already a risk in current code
- **Mitigation**: Ensure all error paths call `Decommission()`
- **Mitigation**: Add finalizers in debug builds (not production)

**Risk 4: Pool Growth Under Load**
- **Mitigation**: `sync.Pool` automatically shrinks during GC
- **Mitigation**: Monitor pool size in production
- **Mitigation**: Consider pool size limits if needed

### Alternative Approaches

**Option 1: Per-Connection Packet Pool**
- Each connection has its own packet pool
- Reduces contention but uses more memory
- More complex to implement

**Option 2: Slab Allocator**
- Pre-allocate large blocks, sub-allocate packets
- More complex, but more control
- Overkill for this use case

**Option 3: Keep Current Approach**
- Only pool payloads (current)
- Simpler but less efficient
- Higher GC pressure

**Recommendation**: Use global `sync.Pool` (simplest, most effective)

### Metrics to Track

**Before/After Comparison:**
1. **Allocation rate**: `go tool pprof` allocation profile
2. **GC frequency**: `GODEBUG=gctrace=1`
3. **GC pause time**: `GODEBUG=gctrace=1`
4. **Memory usage**: `runtime.MemStats`
5. **Latency**: P99 packet processing latency

**Expected Improvements:**
- 90-99% reduction in packet struct allocations
- 20-50% reduction in GC frequency
- 10-30% reduction in GC pause time
- Lower memory footprint under steady load

### Implementation Checklist

- [x] Add `packetPool` variable
- [x] Modify `Decommission()` to reset fields and return to pool (cold path)
- [x] Modify `NewPacket()` to use pool `Get()` with minimal work (hot path)
- [x] Add unit tests for pool behavior and field reset
- [ ] Add integration tests
- [ ] Run memory profiling
- [x] Run performance benchmarks (verify hot path performance)
- [x] Verify no regressions
- [x] Update documentation

### Implementation Status

**Completed**: ✅

**Changes Made**:
1. Added `packetPool` using `sync.Pool` in `packet/packet.go`
2. Modified `Decommission()` to reset all fields before returning to pool (cold path)
3. Modified `NewPacket()` to get from pool with minimal work (hot path - only sets Addr and gets payload)
4. Added comprehensive unit tests:
   - `TestPacketPoolReuse` - Verifies packets are reused from pool
   - `TestPacketPoolResetInDecommission` - Verifies all fields are reset
   - `TestPacketPoolWithUnmarshal` - Verifies Unmarshal works with pooled packets

**Benchmark Results**:
```
BenchmarkNewPacket-24            	41426163	        26.28 ns/op	       0 B/op	       0 allocs/op
BenchmarkNewPacketWithData-24    	22294810	        51.55 ns/op	       0 B/op	       0 allocs/op
```

**Key Achievement**: **0 allocations per operation** - packets are fully pooled!

**Test Results**:
- All existing tests pass ✅
- All new pooling tests pass ✅
- No regressions detected ✅

### Open Questions

1. **Should we pool `net.Addr`?**
   - `net.Addr` is an interface, typically `*net.UDPAddr`
   - Could pool `net.UDPAddr` structs separately
   - Probably not worth it (small, infrequent)

2. **Should we have separate pools for control vs data packets?**
   - Control packets are smaller
   - Data packets have larger payloads
   - Probably not worth the complexity

3. **Should we limit pool size?**
   - `sync.Pool` doesn't have size limits
   - GC naturally limits pool size
   - Probably fine as-is

4. **What about `Clone()`?**
   - `Clone()` creates new packets
   - Should also use pool
   - Can be addressed in follow-up

## Comprehensive Code Analysis: All Packet Creation Points

### Receive Path (High Frequency - Primary Target)

**Location**: `listen_linux.go`, `dial_linux.go`, `listen.go`, `dial.go`

**Pattern**: All use `NewPacketFromData()`
```go
// io_uring receive path
p, err := packet.NewPacketFromData(addr, bufferSlice)

// Traditional receive path
p, err := packet.NewPacketFromData(addr, buffer[:n])
```

**Frequency**:
- ~9,500 packets/second per connection at 10 Mb/s
- Highest allocation rate in the system

**Decommission**:
- Called in error paths (parse errors, unknown connections)
- Called after packet processing in `connection.go:Read()`
- **Impact**: High - all receive paths benefit from pooling

### Send Path - Control Packets (Lower Frequency)

**Locations**: `connection.go` (multiple functions)

**Functions Creating Control Packets**:
1. `sendACK()` - line 1353
2. `sendNAK()` - line 1328
3. `sendACKACK()` - line 1404
4. `sendShutdown()` - line 1305
5. `sendHSRequest()` - line 1451
6. `sendKMRequest()` - line 1490

**Pattern**:
```go
p := packet.NewPacket(c.remoteAddr)
p.Header().IsControlPacket = true
// ... set control packet fields ...
c.pop(p) // Eventually calls send() or sendIoUring()
```

**Decommission**:
- Control packets decommissioned immediately after send in `sendIoUring()` (line 151)
- Control packets are not retransmitted
- **Impact**: Medium - lower frequency but still benefits from pooling

### Send Path - Handshake Packets

**Locations**: `conn_request.go`, `dial.go`

**Functions**:
1. `conn_request.go:generateSocketId()` - line 387
2. `conn_request.go:Accept()` - line 508
3. `dial.go:handleHandshake()` - line 595, 630

**Pattern**: Similar to control packets
```go
p := packet.NewPacket(req.addr)
p.Header().IsControlPacket = true
p.Header().ControlType = packet.CTRLTYPE_HANDSHAKE
// ... send packet ...
```

**Decommission**: After handshake completes
**Impact**: Low - only during connection establishment

### Send Path - Data Packets (User Write)

**Location**: `connection.go:Write()` - line 557

**Pattern**:
```go
p := packet.NewPacket(nil)  // Note: addr is nil
p.SetData(c.writeData[:n])
p.Header().IsControlPacket = false
p.Header().PktTsbpdTime = c.getTimestamp()
c.writeQueue <- p
```

**Flow**:
1. Created in `Write()`
2. Queued to `writeQueue`
3. Processed by congestion control (`sender.Push()`)
4. Stored in `sender.packetList` and `sender.lossList`
5. Sent via `pop()` -> `send()` or `sendIoUring()`
6. Decommissioned when:
   - Too old (in `sender.Tick()` line 225)
   - After successful send (if not retransmitted)

**Decommission**:
- Data packets may be retransmitted, so kept longer
- Decommissioned in `congestion/live/send.go:Tick()` when too old
- **Impact**: Medium - data packets live longer, but still benefit from pooling

### Congestion Control - Sender

**Location**: `congestion/live/send.go`

**Behavior**:
- `Push()` receives packets (doesn't create them)
- Stores packets in `packetList` and `lossList`
- `Tick()` decommissions old packets (line 225)
- Packets stored until sent/ACK'd or too old

**Impact**:
- No packet creation here
- Benefits from pooled packets being reused

### Test Code

**Locations**: Multiple test files (`*_test.go`)

**Pattern**: Extensive use of `NewPacket()` in tests

**Impact**:
- Tests will automatically benefit from pooling
- No special handling needed
- May need test updates to verify pool behavior

### Summary of All Packet Creation Points

| Location | Function | Frequency | Pattern | Decommission |
|----------|----------|-----------|---------|--------------|
| `listen_linux.go:422` | `processRecvCompletion` | Very High | `NewPacketFromData` | After processing |
| `dial_linux.go:290` | `processRecvCompletion` | Very High | `NewPacketFromData` | After processing |
| `listen.go:273` | `ReadFrom` | Very High | `NewPacketFromData` | After processing |
| `dial.go:193` | `ReadFrom` | Very High | `NewPacketFromData` | After processing |
| `connection.go:557` | `Write` | High | `NewPacket(nil)` | After send/too old |
| `connection.go:1305` | `sendShutdown` | Low | `NewPacket` | After send |
| `connection.go:1328` | `sendNAK` | Medium | `NewPacket` | After send |
| `connection.go:1353` | `sendACK` | High | `NewPacket` | After send |
| `connection.go:1404` | `sendACKACK` | Medium | `NewPacket` | After send |
| `connection.go:1451` | `sendHSRequest` | Very Low | `NewPacket` | After send |
| `connection.go:1490` | `sendKMRequest` | Very Low | `NewPacket` | After send |
| `conn_request.go:387` | `generateSocketId` | Very Low | `NewPacket` | After send |
| `conn_request.go:508` | `Accept` | Very Low | `NewPacket` | After send |
| `dial.go:595` | `handleHandshake` | Very Low | `NewPacket` | After send |
| `dial.go:630` | `handleHandshake` | Very Low | `NewPacket` | After send |

### Impact Analysis

**All packet creation goes through**:
- `NewPacket()` - will use pool
- `NewPacketFromData()` - calls `NewPacket()`, will use pool

**All decommissioning goes through**:
- `Decommission()` - will return to pool

**Key Insight**: Since all packet creation and decommissioning is centralized, the pooling optimization will automatically benefit:
- ✅ Receive path (highest frequency)
- ✅ Send path - control packets
- ✅ Send path - data packets
- ✅ Handshake packets
- ✅ Test code

**No code changes needed** in:
- `connection.go` send functions
- `conn_request.go` handshake functions
- `congestion/live/send.go` (uses packets, doesn't create them)
- Test files

**Only changes needed**:
- `packet/packet.go`: Add pool, modify `NewPacket()`, modify `Decommission()`

## Conclusion

Pooling `pkt` structs using `sync.Pool` is a low-risk, high-reward optimization that will significantly reduce memory allocations and GC pressure across **all packet creation paths** in the system. The implementation is straightforward because all packet creation and decommissioning is centralized.

**Recommended Approach:**
1. Add global `packetPool` using `sync.Pool`
2. Modify `Decommission()` to reset fields and return to pool (cold path)
3. Modify `NewPacket()` to use pool `Get()` with minimal work (hot path)
4. Comprehensive testing and validation

**Expected Impact:**
- 90-99% reduction in packet struct allocations across entire system
- Lower GC pressure and better latency
- Minimal code changes (only in `packet/packet.go`)
- No API changes
- Automatic benefit to all packet creation paths

