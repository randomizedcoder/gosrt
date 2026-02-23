# CPU Profile Analysis and Optimization Opportunities

## Profile Summary

**Total CPU Time**: 467.55s (37.02% of total runtime)
**Profile Duration**: 1262.93s

## Top CPU Consumers

### 1. System Calls (50.36% - Expected)
- `syscall.Syscall6` / `syscall.RawSyscall6`: 235.46s (50.36%)
- **Analysis**: This is io_uring syscalls - expected and good. This is where we want CPU time to be spent (kernel doing the work).

### 2. Runtime Futex (24.24% - Expected)
- `runtime.futex`: 113.34s (24.24%)
- **Analysis**: Blocking on `WaitCQE()` syscalls. This is expected with io_uring's blocking completion waiting. The high percentage indicates we're efficiently waiting for I/O rather than spinning.

### 3. Packet Header Access (2.66% - Optimization Opportunity)
- `packet.(*pkt).Header`: 12.44s (2.66% flat), 12.88s (2.75% cum)
- **Analysis**: `Header()` is called very frequently. While it's just `return &p.header`, the function call overhead adds up when called millions of times.

### 4. B-Tree Iteration (1.33% - Acceptable)
- `btree.(*node).iterate`: 3.80s (0.81%)
- `btreePacketStore.Iterate.func1`: 2.43s (0.52%)
- **Analysis**: B-tree iteration overhead. This is reasonable for the benefits it provides (O(log n) vs O(n) for list).

### 5. Periodic ACK Processing (0.64% - Optimization Opportunity)
- `receiver.periodicACK.func1`: 3.01s (0.64% flat), 11.93s (2.55% cum)
- **Analysis**: Called frequently, and within the iteration, `p.Header()` is called **5 times per packet**.

### 6. Process Receive Completion (0.27% flat, 9.95% cum - Called Frequently)
- `listener.processRecvCompletion`: 1.25s (0.27% flat), 46.51s (9.95% cum)
- **Analysis**: Called for every received packet. Low flat time means most time is in callees, which is good.

## Optimization Opportunities

### 1. Cache `Header()` Result in Hot Loops ⭐ **HIGH IMPACT**

**Problem**: `p.Header()` is called multiple times per packet in iteration loops:
- `periodicACK`: 5 calls per packet
- `periodicNAK`: 3 calls per packet
- `processRecvCompletion`: 3-4 calls per packet

**Solution**: Cache the header pointer at the start of each iteration:

```go
// Before (periodicACK):
r.packetStore.Iterate(func(p packet.Packet) bool {
    if p.Header().PacketSequenceNumber.Lte(ackSequenceNumber) {
        return true
    }
    if p.Header().PktTsbpdTime <= now {
        ackSequenceNumber = p.Header().PacketSequenceNumber
        return true
    }
    if p.Header().PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
        ackSequenceNumber = p.Header().PacketSequenceNumber
        maxPktTsbpdTime = p.Header().PktTsbpdTime
        return true
    }
    return false
})

// After (optimized):
r.packetStore.Iterate(func(p packet.Packet) bool {
    h := p.Header() // Cache header pointer - single function call
    if h.PacketSequenceNumber.Lte(ackSequenceNumber) {
        return true
    }
    if h.PktTsbpdTime <= now {
        ackSequenceNumber = h.PacketSequenceNumber
        return true
    }
    if h.PacketSequenceNumber.Equals(ackSequenceNumber.Inc()) {
        ackSequenceNumber = h.PacketSequenceNumber
        maxPktTsbpdTime = h.PktTsbpdTime
        return true
    }
    return false
})
```

**Expected Impact**:
- Reduce `Header()` calls by 60-80% in hot loops
- Estimated 1-2% CPU reduction
- Simple change, low risk

**Files to modify**:
- `congestion/live/receive.go`: `periodicACK`, `periodicNAK`, `Tick`
- `listen_linux.go`: `processRecvCompletion`
- `dial_linux.go`: `processRecvCompletion`

### 2. Optimize `processRecvCompletion` Header Access ⭐ **MEDIUM IMPACT**

**Problem**: `processRecvCompletion` calls `p.Header()` 3-4 times:
- Line 442: `socketId := p.Header().DestinationSocketId`
- Line 446: `p.Header().IsControlPacket && p.Header().ControlType`
- Line 491: `p.Header().Addr.String()`

**Solution**: Cache header at the start:

```go
func (ln *listener) processRecvCompletion(...) {
    // ... parse packet ...

    h := p.Header() // Cache header - single call
    socketId := h.DestinationSocketId

    if socketId == 0 {
        if h.IsControlPacket && h.ControlType == packet.CTRLTYPE_HANDSHAKE {
            // ...
        }
    }

    // ... later ...
    if h.Addr.String() != conn.RemoteAddr().String() {
        // ...
    }
}
```

**Expected Impact**:
- Reduce function call overhead in hot path
- Estimated 0.5-1% CPU reduction

### 3. B-Tree Iteration Overhead (1.33% - Acceptable)

**Current**: B-tree iteration shows 1.33% CPU time
- `btree.(*node).iterate`: 0.81%
- `btreePacketStore.Iterate.func1`: 0.52%

**Analysis**: This is reasonable overhead for the benefits:
- O(log n) search vs O(n) for list
- Better performance with large buffers (3-second buffers)
- Scales better with 100 connections

**Recommendation**: Keep as-is. The overhead is acceptable for the benefits.

### 4. Consider Inlining `Header()` Method ⚠️ **LOW IMPACT**

**Current**: `Header()` is a method call that returns `&p.header`

**Option**: Make `header` field public or use a getter that compiler can inline

**Analysis**:
- Go compiler may already inline this in some cases
- Making field public breaks encapsulation
- Low impact (2.66% is already small)

**Recommendation**: Skip for now. Caching in hot loops (optimization #1) is more impactful.

### 5. Reduce `Addr.String()` Calls ⭐ **MEDIUM IMPACT**

**Problem**: `p.Header().Addr.String()` is called in hot paths:
- `processRecvCompletion`: Called for peer validation
- Creates string allocation each time

**Solution**: Cache the string or compare addresses directly:

```go
// Option 1: Cache string (if Addr doesn't change)
// Option 2: Compare addresses directly without string conversion
if !ln.config.AllowPeerIpChange {
    pktAddr := h.Addr
    connAddr := conn.RemoteAddr()
    // Compare UDP addresses directly without string conversion
    if !addrsEqual(pktAddr, connAddr) {
        // Drop packet
    }
}
```

**Expected Impact**:
- Reduce string allocations
- Estimated 0.3-0.5% CPU reduction

### 6. Optimize `periodicACK` and `periodicNAK` Logic

**Current**: Both functions iterate through all packets and call `Header()` multiple times

**Potential**:
- Early exit optimizations (already done)
- Batch processing
- Reduce redundant comparisons

**Analysis**: Already fairly optimized. Caching `Header()` (optimization #1) will have the biggest impact here.

## Priority Ranking

1. **HIGH**: Cache `Header()` in hot loops (`periodicACK`, `periodicNAK`, `processRecvCompletion`)
   - Impact: 1-2% CPU reduction
   - Effort: Low
   - Risk: Very low

2. **MEDIUM**: Optimize `Addr.String()` calls
   - Impact: 0.3-0.5% CPU reduction
   - Effort: Low
   - Risk: Low

3. **LOW**: Other micro-optimizations
   - Impact: <0.5% CPU reduction
   - Effort: Medium
   - Risk: Medium

## Current Performance Characteristics

**Good Signs**:
- ✅ 50%+ CPU time in syscalls (kernel doing work)
- ✅ 24% in futex (efficient blocking, not spinning)
- ✅ Low flat time in `processRecvCompletion` (most time in callees)
- ✅ B-tree overhead is reasonable (1.33%)

**Areas for Improvement**:
- ⚠️ `Header()` called too frequently (2.66%)
- ⚠️ Multiple `Header()` calls per packet in loops
- ⚠️ String allocations in hot path (`Addr.String()`)

## Implementation Plan

### Phase 1: Cache Header in Hot Loops (Recommended First)
1. Update `periodicACK` to cache header
2. Update `periodicNAK` to cache header
3. Update `Tick` to cache header
4. Update `processRecvCompletion` (listener and dialer)

### Phase 2: Optimize Address Comparison
1. Add `addrsEqual()` helper function
2. Replace `Addr.String()` comparisons with direct address comparison

### Phase 3: Profile Again
1. Run CPU profile with optimizations
2. Compare before/after
3. Identify next optimization targets

## Expected Overall Impact

**Conservative Estimate**:
- Header caching: 1-2% CPU reduction
- Address comparison: 0.3-0.5% CPU reduction
- **Total: 1.3-2.5% CPU reduction**

**Best Case**:
- Header caching: 2-3% CPU reduction
- Address comparison: 0.5-1% CPU reduction
- **Total: 2.5-4% CPU reduction**

These optimizations are low-risk, high-value improvements that should provide measurable performance gains.

