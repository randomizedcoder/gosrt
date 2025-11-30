# IO_Uring Read Path: Phased Implementation Plan

## Overview

The IO_Uring read path implementation is a large undertaking that involves multiple components. This document breaks down the implementation into manageable phases, prioritizing based on:
- **Risk**: Lower risk changes first
- **Dependencies**: Independent changes first
- **Value**: Immediate benefits first
- **Complexity**: Simpler changes first

## Phase Comparison

### Option 1: B-Tree First
**Pros:**
- ✅ **Lower risk**: Isolated change, doesn't affect network I/O
- ✅ **Immediate value**: Provides 42-230x speedup for out-of-order packets
- ✅ **Independent**: Can be implemented and tested separately
- ✅ **No kernel dependency**: Works on all platforms
- ✅ **Easier to validate**: Can compare list vs btree side-by-side
- ✅ **Foundation**: Optimizes congestion control before optimizing network I/O

**Cons:**
- ⚠️ **Doesn't address network I/O**: Still uses blocking ReadFrom()
- ⚠️ **Limited benefit for in-order packets**: B-tree is slower for in-order (2.4x)

### Option 2: io_uring First
**Pros:**
- ✅ **Bigger impact**: Eliminates blocking syscalls entirely
- ✅ **Foundation**: Network I/O optimization is the core goal
- ✅ **Enables further optimizations**: Once async I/O is working, other optimizations follow

**Cons:**
- ⚠️ **Higher risk**: More complex, more moving parts
- ⚠️ **Kernel dependency**: Requires Linux 5.1+
- ⚠️ **Harder to test**: Requires kernel support, more error paths
- ⚠️ **More dependencies**: Buffer management, completion handlers, error handling

## Recommended Approach: B-Tree First

**Rationale:**
1. **Lower Risk**: B-tree is a contained change in one file (`congestion/live/receive.go`)
2. **Immediate Value**: Provides significant performance improvement for out-of-order packets
3. **Independent**: Can be implemented, tested, and validated separately
4. **Foundation**: Optimizes the congestion control layer before optimizing network I/O
5. **Validation**: Easier to validate correctness (can run both implementations side-by-side)

**Then io_uring:**
- Once congestion control is optimized, we can focus on network I/O
- B-tree will handle the increased packet rate from io_uring better
- Less risk of bottlenecks in congestion control when io_uring increases throughput

## Phased Implementation Plan

### Phase 1: B-Tree Implementation (Recommended First)

**Goal**: Replace `container/list.List` with `github.com/google/btree` in congestion control receiver.

**Scope:**
- Modify `congestion/live/receive.go`
- Add configuration option `PacketReorderAlgorithm` ("list" or "btree")
- Implement btree-based `Push()`, `Tick()`, `periodicACK()`, `periodicNAK()`
- Implement optimized read/write locking strategy
- Add comprehensive tests

**Benefits:**
- 42-230x speedup for out-of-order packets
- 1-14% CPU savings per connection (100-1400% total for 100 connections)
- Better performance for large buffers (2,757 packets)
- Critical for 2-3% packet loss scenarios

**Dependencies:**
- None (independent change)

**Risk Level:** Low

**Estimated Effort:** 2-3 days

**Deliverables:**
- B-tree implementation in `congestion/live/receive.go`
- Configuration option in `config.go`
- Comprehensive tests in `congestion/live/receive_test.go`
- Performance benchmarks comparing list vs btree

---

### Phase 2: io_uring Read Path Foundation

**Goal**: Replace blocking `ReadFrom()` syscalls with io_uring asynchronous receives.

**Scope:**
- Add io_uring ring initialization in `listen.go` and `dial.go`
- Implement buffer pool (`sync.Pool` of `[]byte`)
- Implement completion tracking (atomic counters, map)
- Extract socket file descriptor
- Platform-specific files (`listen_linux.go`, `listen_other.go`, `dial_linux.go`, `dial_other.go`)

**Benefits:**
- Eliminates blocking syscalls
- Multiple pending receives simultaneously
- Better CPU utilization
- Foundation for further optimizations

**Dependencies:**
- None (independent of Phase 1)

**Risk Level:** Medium

**Estimated Effort:** 3-5 days

**Deliverables:**
- Ring initialization and cleanup
- Buffer pool implementation
- Completion tracking infrastructure
- Platform-specific code separation

---

### Phase 3: io_uring Completion Handler

**Goal**: Implement completion handler to process received packets.

**Scope:**
- Implement `recvCompletionHandler()` goroutine
- Implement `getRecvCompletion()` (non-blocking peek, then blocking wait)
- Implement `processRecvCompletion()` (error handling, deserialization, routing)
- Implement `submitRecvRequestBatch()` (batch resubmission)
- Implement `drainRecvCompletions()` (cleanup on shutdown)

**Benefits:**
- Processes packets as they arrive
- Maintains constant pending receives
- Batched resubmission reduces syscalls

**Dependencies:**
- Phase 2 (ring initialization)

**Risk Level:** Medium

**Estimated Effort:** 3-4 days

**Deliverables:**
- Completion handler implementation
- Error handling for all syscalls
- Batch resubmission logic
- Cleanup and shutdown handling

---

### Phase 4: io_uring Integration

**Goal**: Integrate io_uring into listener and dialer, replacing ReadFrom().

**Scope:**
- Update `Listen()` to initialize io_uring ring
- Update `Dial()` to initialize io_uring ring
- Replace `ReadFrom()` goroutine with io_uring completion handler
- Maintain backward compatibility (fallback to ReadFrom if io_uring disabled)
- Update cleanup to close rings

**Benefits:**
- Complete replacement of blocking syscalls
- Maintains backward compatibility
- Can be enabled/disabled via configuration

**Dependencies:**
- Phase 2 (ring initialization)
- Phase 3 (completion handler)

**Risk Level:** Medium-High

**Estimated Effort:** 2-3 days

**Deliverables:**
- Integrated io_uring in listener and dialer
- Backward compatibility maintained
- Configuration option `IoUringRecvEnabled`

---

### Phase 5: Channel Bypass Optimization (Optional)

**Goal**: Eliminate channels, route packets directly to `handlePacket()`.

**Scope:**
- Replace `map[uint32]*srtConn` with `sync.Map` for connection routing
- Implement direct routing from completion handler to `handlePacket()`
- Add per-connection mutex for serialization
- Remove `rcvQueue` and `networkQueue` channels
- Remove `reader()` and `networkQueueReader()` goroutines

**Benefits:**
- 10x latency reduction (50μs → 5μs)
- 50% throughput increase (100K → 150K pps)
- 20% CPU reduction
- 50% memory reduction
- Zero packet drops

**Dependencies:**
- Phase 4 (io_uring integration)

**Risk Level:** High (significant architectural change)

**Estimated Effort:** 4-5 days

**Deliverables:**
- Direct routing implementation
- Channel removal
- Performance validation

---

## Alternative: io_uring First Approach

If you prefer to start with io_uring, the phases would be:

### Phase 1: io_uring Foundation
- Ring initialization
- Buffer pool
- Completion tracking
- Socket FD extraction

### Phase 2: Completion Handler
- Completion processing
- Error handling
- Batch resubmission

### Phase 3: Integration
- Replace ReadFrom() with io_uring
- Backward compatibility

### Phase 4: B-Tree Implementation
- Replace list with btree
- Optimized locking
- Performance validation

### Phase 5: Channel Bypass (Optional)
- Direct routing
- Channel removal

**Why this might make sense:**
- io_uring is the "bigger" architectural change
- Once async I/O is working, congestion control optimizations follow naturally
- B-tree can be added later as an optimization

**Why this might not make sense:**
- Higher risk (more complex)
- B-tree provides immediate value independently
- Congestion control might become bottleneck with increased io_uring throughput

## Recommendation: B-Tree First

**Start with Phase 1 (B-Tree)** because:

1. **Lower Risk**: Contained change, easier to validate
2. **Immediate Value**: Significant performance improvement
3. **Independent**: Can be implemented and tested separately
4. **Foundation**: Optimizes congestion control before network I/O
5. **Validation**: Easier to verify correctness

**Then proceed with Phases 2-4 (io_uring)** to:
- Eliminate blocking syscalls
- Increase network I/O throughput
- Leverage the optimized congestion control from Phase 1

**Finally, consider Phase 5 (Channel Bypass)** if:
- Further latency reduction is needed
- Throughput is still a concern
- You're willing to accept the architectural complexity

## Implementation Timeline

**B-Tree First Approach:**
- **Week 1**: Phase 1 (B-Tree) - 2-3 days
- **Week 2-3**: Phases 2-4 (io_uring) - 8-12 days
- **Week 4**: Phase 5 (Channel Bypass, optional) - 4-5 days

**Total: 3-4 weeks** (excluding Phase 5)

**io_uring First Approach:**
- **Week 1-2**: Phases 1-3 (io_uring) - 8-12 days
- **Week 3**: Phase 4 (B-Tree) - 2-3 days
- **Week 4**: Phase 5 (Channel Bypass, optional) - 4-5 days

**Total: 3-4 weeks** (excluding Phase 5)

## Decision Matrix

| Factor | B-Tree First | io_uring First |
|--------|-------------|----------------|
| **Risk** | Low | Medium-High |
| **Complexity** | Low | High |
| **Dependencies** | None | Kernel 5.1+ |
| **Immediate Value** | High (out-of-order) | High (all packets) |
| **Testing Ease** | Easy | Harder |
| **Validation** | Easy (side-by-side) | Requires kernel |
| **Foundation** | Optimizes congestion control | Optimizes network I/O |

## Conclusion

**Recommended: Start with B-Tree (Phase 1)**

This provides:
- Lower risk
- Immediate value
- Independent validation
- Foundation for io_uring

Then proceed with io_uring (Phases 2-4) to complete the network I/O optimization.

Optionally add Channel Bypass (Phase 5) for maximum performance.

