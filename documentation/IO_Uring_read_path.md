# IO_Uring Read Path Implementation

This document focuses on implementing io_uring for the **read path** in GoSRT, specifically accelerating the `ReadFrom()` operations that read UDP packets from the network socket.

## Overview

The read path is critical for performance as it's the entry point for all incoming packets. By replacing the blocking `ReadFrom()` syscall with io_uring's asynchronous `RecvMsg` operations, we can:

- **Eliminate blocking syscalls**: Multiple receive operations can be pending simultaneously
- **Reduce latency**: Packets are processed as soon as they arrive, without waiting for syscall completion
- **Improve throughput**: Maintain a constant number of pending receives to keep the ring busy
- **Better CPU utilization**: Asynchronous I/O reduces context switching overhead

## Current Read Path Implementation

### Network Receive Flow

The current implementation uses blocking `ReadFrom()` syscalls in dedicated goroutines:

**Listener** (`listen.go:225`):
```go
go func() {
    buffer := make([]byte, config.MSS) // MTU size

    for {
        if ln.isShutdown() {
            return
        }

        ln.pc.SetReadDeadline(time.Now().Add(3 * time.Second))
        n, addr, err := ln.pc.ReadFrom(buffer)
        if err != nil {
            // Handle errors...
            continue
        }

        p, err := packet.NewPacketFromData(addr, buffer[:n])
        if err != nil {
            continue
        }

        // Non-blocking send to rcvQueue
        select {
        case ln.rcvQueue <- p:
        default:
            ln.log("listen", func() string { return "receive queue is full" })
        }
    }
}()
```

**Dialer** (`dial.go:145`):
```go
go func() {
    buffer := make([]byte, MAX_MSS_SIZE) // MTU size

    for {
        if dl.isShutdown() {
            return
        }

        pc.SetReadDeadline(time.Now().Add(3 * time.Second))
        n, _, err := pc.ReadFrom(buffer)
        if err != nil {
            // Handle errors...
            continue
        }

        p, err := packet.NewPacketFromData(dl.remoteAddr, buffer[:n])
        if err != nil {
            continue
        }

        // Non-blocking send to rcvQueue
        select {
        case dl.rcvQueue <- p:
        default:
            dl.log("dial", func() string { return "receive queue is full" })
        }
    }
}()
```

### Current Flow Diagram

```
Network Socket (UDP)
    |
    | [ReadFrom() syscall - blocking]
    v
Listener/Dialer Goroutine
    | (listen.go:225, dial.go:145)
    | - Reads into buffer
    | - Parses packet with packet.NewPacketFromData()
    |
    v
rcvQueue Channel (2048 buffer)
    | (listen.go:247, dial.go:167)
    |
    v
Listener reader() Goroutine
    | (listen.go:382)
    | - Routes packets to correct connection
    | - Looks up connection in ln.conns map using DestinationSocketId
    |
    v
conn.push() → networkQueue → handlePacket() → congestion control → readQueue → Application
```

**Key Characteristics:**
- Single blocking `ReadFrom()` per listener/dialer
- Buffer allocated once and reused
- 3-second read deadline for timeout handling
- Immediate parsing after read
- Non-blocking queue to `rcvQueue` channel

## io_uring Read Path Design

### Architecture Decision: Shared Receive Ring

Unlike the send path (which uses per-connection rings), the **receive path uses a shared io_uring ring at the listener/dialer level**. This is more efficient because:

1. **Single socket**: Each listener/dialer has one UDP socket that receives all packets
2. **Demultiplexing**: Packets are already routed by destination socket ID after parsing
3. **Simpler management**: One ring per listener/dialer instead of one per connection
4. **Better resource utilization**: Shared ring can handle bursts more efficiently

### io_uring Flow

```
Submit RecvMsg() → ring.Submit() → ring.WaitCQE() → parse → rcvQueue channel
```

**Key Changes:**
- Replace `ReadFrom()` goroutine with io_uring submission/completion loop
- Maintain constant number of pending receives (typically 64)
- Use `PrepareRecvMsg` with `syscall.Msghdr` to get source address
- Process completions asynchronously in dedicated goroutine

### Implementation Details

#### 1. Dependencies

- **Library**: `github.com/randomizedcoder/giouring` (fork)
- **Kernel**: Linux 5.1+ required for io_uring
- **Build tags**: `//go:build linux` for Linux-specific code

#### 2. Buffer Management

We use `[]byte` pool (fixed size `config.MSS`) for receives, which differs from the write path:
- **Write path uses `bytes.Buffer`**: Needs to marshal variable-sized packets, so dynamic sizing is useful
- **Receive path uses `[]byte`**: Always receives into fixed-size buffer (MSS), so fixed-size allocation is more efficient

**Why `[]byte` for receives:**
- **Simpler**: No Reset(), Grow(), or Write() needed - just get and use
- **More efficient**: Direct access to underlying array, perfect for io_uring iovec
- **Lower overhead**: No bytes.Buffer structure overhead
- **Faster**: No buffer operations needed
- **Perfect fit**: Receives are always into fixed-size buffer (MSS)

**Buffer Pool Structure:**
```go
// Per-listener/dialer receive buffer pool (fixed size MSS)
recvBufferPool := sync.Pool{
    New: func() interface{} {
        return make([]byte, config.MSS)
    },
}

// Usage:
// Get buffer from pool (already the right size, no setup needed)
buffer := recvBufferPool.Get().([]byte)

// Use directly for io_uring iovec (no conversion needed)
iovec.Base = &buffer[0]
iovec.SetLen(len(buffer))

// After receiving, return to pool (no reset needed - kernel overwrites)
recvBufferPool.Put(buffer)
```

#### 3. Completion Tracking Structure

Following the write path pattern, we use:
- Atomic counter for generating unique request IDs
- Map to track pending completions (protected by lock)
- Minimal completion info structure (just buffer)

```go
// Completion tracking - minimal structure for performance (same pattern as send path)
recvCompletions map[uint64]*recvCompletionInfo // Maps request ID to completion info
recvCompLock    sync.RWMutex                   // Protects recvCompletions map

// Atomic counter for generating unique request IDs (same pattern as send path)
recvRequestID atomic.Uint64

// recvCompletionInfo stores minimal information needed for completion handling
// Key insight: We only need the buffer (to return to pool after deserialization)
// and rsa (to extract source address). The msg and iovec are only used during
// SQE setup in submitRecvRequest(), not in the completion handler.
type recvCompletionInfo struct {
    buffer []byte            // Buffer to return to pool after deserialization completes
    rsa    syscall.RawSockaddrAny  // Kernel fills this during receive, used to extract source address
}
```

**Note**: Unlike the write path which uses per-connection rings, the receive path uses a shared ring at listener/dialer level. However, we still use the same atomic counter and map pattern for tracking completions.

#### 4. io_uring Ring Setup

**In `Listen()` or `Dial()`:**
```go
// Create io_uring ring for receive operations
ring := giouring.NewRing()
ringSize := uint32(256) // Configurable, default 256
err := ring.QueueInit(ringSize, 0) // ring size, no flags
if err != nil {
    // Fall back to regular ReadFrom() if io_uring unavailable
    return nil, err
}

// Store ring in listener/dialer struct
ln.recvRing = ring
ln.recvRingFd = socketFd // UDP socket file descriptor

// Initialize receive buffer pool (fixed size MSS, simpler than send path)
ln.recvBufferPool = sync.Pool{
    New: func() interface{} {
        return make([]byte, ln.config.MSS)
    },
}

// Initialize completion tracking (same pattern as send path)
ln.recvCompletions = make(map[uint64]*recvCompletionInfo)

// Create context for completion handler (same pattern as send path)
ln.recvCompCtx, ln.recvCompCancel = context.WithCancel(context.Background())

// Start completion handler goroutine (same pattern as send path)
ln.recvCompWg.Add(1)
go ln.recvCompletionHandler(ln.recvCompCtx)
```

#### 5. Submit Receive Request Function

Following the write path pattern, we submit receives one at a time using atomic request IDs and completion tracking:

```go
// submitRecvRequest submits a new receive request to the ring
// This is called both at startup (to pre-populate) and after each completion (to maintain constant pending)
func (ln *listener) submitRecvRequest() {
    ring, ok := ln.recvRing.(*giouring.Ring)
    if !ok {
        return
    }

    // Get buffer from pool (fixed size MSS, no setup needed)
    buffer := ln.recvBufferPool.Get().([]byte)
    // No Reset() needed - kernel will overwrite the buffer

    // Setup iovec using buffer directly (no conversion needed)
    var iovec syscall.Iovec
    iovec.Base = &buffer[0]
    iovec.SetLen(len(buffer))

    // Setup msghdr for UDP (to get source address)
    var msg syscall.Msghdr
    var rsa syscall.RawSockaddrAny
    msg.Name = (*byte)(unsafe.Pointer(&rsa))
    msg.Namelen = uint32(syscall.SizeofSockaddrAny)
    msg.Iov = &iovec
    msg.Iovlen = 1

    // Generate unique request ID using atomic counter (same pattern as send path)
    requestID := ln.recvRequestID.Add(1)

    // Create minimal completion info (only buffer and rsa needed)
    // msg and iovec are only used for SQE setup, not stored in completion info
    compInfo := &recvCompletionInfo{
        buffer: buffer, // Keep buffer alive until deserialization completes
        rsa:    rsa,    // Kernel will fill this during receive
    }

    // Store completion info in map (protected by lock, same pattern as send path)
    ln.recvCompLock.Lock()
    ln.recvCompletions[requestID] = compInfo
    ln.recvCompLock.Unlock()

    // Get SQE from ring with retry loop (same pattern as send path)
    var sqe *giouring.SubmissionQueueEntry
    const maxRetries = 3
    for i := 0; i < maxRetries; i++ {
        sqe = ring.GetSQE()
        if sqe != nil {
            break // Got an SQE, proceed
        }

        // Ring full - wait a bit and retry (completions may free up space)
        if i < maxRetries-1 {
            time.Sleep(100 * time.Microsecond)
        }
    }

    if sqe == nil {
        // Ring still full after retries - clean up (same pattern as send path)
        ln.recvCompLock.Lock()
        delete(ln.recvCompletions, requestID)
        ln.recvCompLock.Unlock()

        ln.recvBufferPool.Put(buffer)

        ln.log("listen:recv:error", func() string {
            return "io_uring ring full after retries"
        })
        return
    }

    // Prepare recvmsg operation
    sqe.PrepareRecvMsg(ln.recvRingFd, &msg, 0)

    // Store request ID in user data for completion correlation (same pattern as send path)
    sqe.SetData64(requestID)

    // Submit to ring with retry loop (same pattern as send path)
    var err error
    const maxSubmitRetries = 3
    for i := 0; i < maxSubmitRetries; i++ {
        _, err = ring.Submit()
        if err == nil {
            break // Submission successful
        }

        // Only retry transient errors (EINTR, EAGAIN)
        if err != syscall.EINTR && err != syscall.EAGAIN {
            // Fatal error - don't retry
            break
        }

        // Transient error - wait and retry
        if i < maxSubmitRetries-1 {
            time.Sleep(100 * time.Microsecond) // Same delay as GetSQE retry
        }
    }

    if err != nil {
        // Submission failed - clean up (same pattern as send path)
        ln.recvCompLock.Lock()
        delete(ln.recvCompletions, requestID)
        ln.recvCompLock.Unlock()

        ln.recvBufferPool.Put(buffer)

        ln.log("listen:recv:error", func() string {
            return fmt.Sprintf("failed to submit receive request: %v", err)
        })
        return
    }

    // Request submitted successfully
    // Completion will be handled asynchronously by completion handler
}

// Pre-populate ring with initial pending receives (runs once at startup)
func (ln *listener) prePopulateRecvRing() {
    initialPending := ln.config.IoUringRecvInitialPending
    if initialPending <= 0 {
        initialPending = 128 // Default
    }

    // Submit initial batch of receives
    for i := 0; i < initialPending; i++ {
        ln.submitRecvRequest()
    }
}
```

**Key Points:**
- Uses atomic counter for request IDs (same as send path)
- Uses map to track completions with lock (same as send path)
- Retry loops for GetSQE and Submit (same as send path)
- Same error handling patterns as send path
- Pre-population runs **once** at startup
- After each completion, we call `submitRecvRequest()` again to maintain constant pending count

#### 6. Completion Handler with Batching

Process completions in batches to reduce syscall overhead, then batch resubmit to maintain constant pending count:

```go
func (ln *listener) recvCompletionHandler(ctx context.Context) {
    defer ln.recvCompWg.Done()

    ring, ok := ln.recvRing.(*giouring.Ring)
    if !ok {
        return
    }

    // Get batch size from config (default: 20)
    batchSize := ln.config.IoUringRecvBatchSize
    if batchSize <= 0 {
        batchSize = 20 // Default
    }

    for {
        // Check for context cancellation first (same pattern as send path)
        select {
        case <-ctx.Done():
            // Drain any remaining completions
            ln.drainRecvCompletions()
            return
        default:
        }

        // Collect completions in a batch (reduces syscall overhead)
        // Process up to batchSize completions before resubmitting
        var completions []*giouring.CompletionQueueEvent
        var completionInfos []*recvCompletionInfo

        // Collect batch of completions (non-blocking peek)
        for len(completions) < batchSize {
            cqe, err := ring.PeekCQE()
            if err != nil {
                if err == syscall.EAGAIN {
                    // No more completions available - process what we have
                    break
                }

                // Check if context was cancelled
                select {
                case <-ctx.Done():
                    ln.drainRecvCompletions()
                    return
                default:
                }

                // Handle different error conditions (same pattern as send path)
                if err == syscall.EBADF {
                    // Ring closed - listener is shutting down
                    return
                }

                // EINTR is normal (interrupted by signal)
                if err != syscall.EINTR {
                    ln.log("listen:recv:completion:error", func() string {
                        return fmt.Sprintf("error peeking completion: %v", err)
                    })
                }
                break
            }

            // Get request ID from completion user data
            requestID := cqe.UserData

            // Look up completion info
            ln.recvCompLock.Lock()
            compInfo, exists := ln.recvCompletions[requestID]
            if !exists {
                ln.recvCompLock.Unlock()
                ln.log("listen:recv:completion:error", func() string {
                    return fmt.Sprintf("completion for unknown request ID: %d", requestID)
                })
                ring.CQESeen(cqe)
                continue
            }
            delete(ln.recvCompletions, requestID)
            ln.recvCompLock.Unlock()

            completions = append(completions, cqe)
            completionInfos = append(completionInfos, compInfo)
        }

        // If no completions collected, wait for one (blocking)
        if len(completions) == 0 {
            cqe, err := ring.WaitCQE()
            if err != nil {
                // Check if context was cancelled
                select {
                case <-ctx.Done():
                    ln.drainRecvCompletions()
                    return
                default:
                }

                if err == syscall.EBADF {
                    return
                }

                if err != syscall.EAGAIN && err != syscall.EINTR {
                    ln.log("listen:recv:completion:error", func() string {
                        return fmt.Sprintf("error waiting for completion: %v", err)
                    })
                }
                continue
            }

            // Look up completion info for the waited completion
            requestID := cqe.UserData
            ln.recvCompLock.Lock()
            compInfo, exists := ln.recvCompletions[requestID]
            if !exists {
                ln.recvCompLock.Unlock()
                ln.log("listen:recv:completion:error", func() string {
                    return fmt.Sprintf("completion for unknown request ID: %d", requestID)
                })
                ring.CQESeen(cqe)
                continue
            }
            delete(ln.recvCompletions, requestID)
            ln.recvCompLock.Unlock()

            completions = append(completions, cqe)
            completionInfos = append(completionInfos, compInfo)
        }

        // Process batch of completions
        resubmitCount := 0
        for i, cqe := range completions {
            compInfo := completionInfos[i]
            buffer := compInfo.buffer

            // Check for receive errors
            if cqe.Res < 0 {
                errno := -cqe.Res
                ln.log("listen:recv:completion:error", func() string {
                    return fmt.Sprintf("receive failed: %s (errno %d)", syscall.Errno(errno).Error(), errno)
                })
                // Return buffer and mark for resubmit
                ring.CQESeen(cqe)
                ln.recvBufferPool.Put(buffer)
                resubmitCount++
                continue
            }

            // Successful receive
            bytesReceived := int(cqe.Res)
            if bytesReceived == 0 {
                // Empty datagram - return buffer and mark for resubmit
                ring.CQESeen(cqe)
                ln.recvBufferPool.Put(buffer)
                resubmitCount++
                continue
            }

            // Extract source address from RawSockaddrAny (kernel filled this during receive)
            addr := extractAddrFromRSA(&compInfo.rsa)

            // Use buffer directly (kernel wrote directly to it via iovec)
            bufferSlice := buffer[:bytesReceived]

            // Deserialize packet (NewPacketFromData copies the data into packet structure)
            p, err := packet.NewPacketFromData(addr, bufferSlice)

            // After deserialization, we can return buffer to pool immediately
            // (NewPacketFromData has copied the data, so buffer is no longer needed)
            ln.recvBufferPool.Put(buffer)

            if err != nil {
                // Deserialization error - log and mark for resubmit
                ln.log("listen:recv:parse:error", func() string {
                    return fmt.Sprintf("failed to parse packet: %v", err)
                })
                ring.CQESeen(cqe)
                resubmitCount++
                continue
            }

            // Queue packet (non-blocking, same as current implementation)
            select {
            case ln.rcvQueue <- p:
                // Success - packet queued, buffer already returned to pool
            default:
                // Queue full - log and drop packet
                ln.log("listen", func() string { return "receive queue is full" })
                p.Decommission() // Clean up dropped packet
            }

            // Mark CQE as seen (required by giouring)
            ring.CQESeen(cqe)
            resubmitCount++ // Always resubmit to maintain constant pending count
        }

        // Batch resubmit to maintain constant pending count
        // This reduces syscall overhead compared to 1:1 resubmission
        if resubmitCount > 0 {
            ln.submitRecvRequestBatch(resubmitCount)
        }
    }
}

// submitRecvRequestBatch submits multiple receive requests in a batch
// This is more efficient than calling submitRecvRequest() multiple times
// Reduces syscall overhead by batching multiple submissions together
func (ln *listener) submitRecvRequestBatch(count int) {
    ring, ok := ln.recvRing.(*giouring.Ring)
    if !ok {
        return
    }

    // Collect SQEs for batch submission
    var sqes []*giouring.SubmissionQueueEntry
    var compInfos []*recvCompletionInfo
    var requestIDs []uint64 // Track request IDs for error cleanup

    for i := 0; i < count; i++ {
        // Get buffer from pool
        buffer := ln.recvBufferPool.Get().([]byte)

        // Setup iovec using buffer directly
        var iovec syscall.Iovec
        iovec.Base = &buffer[0]
        iovec.SetLen(len(buffer))

        // Setup msghdr for UDP (to get source address)
        var msg syscall.Msghdr
        var rsa syscall.RawSockaddrAny
        msg.Name = (*byte)(unsafe.Pointer(&rsa))
        msg.Namelen = uint32(syscall.SizeofSockaddrAny)
        msg.Iov = &iovec
        msg.Iovlen = 1

        // Generate unique request ID
        requestID := ln.recvRequestID.Add(1)

        // Create completion info
        compInfo := &recvCompletionInfo{
            buffer: buffer,
            rsa:    rsa,
        }

        // Store completion info in map
        ln.recvCompLock.Lock()
        ln.recvCompletions[requestID] = compInfo
        ln.recvCompLock.Unlock()

        // Get SQE (with retry if needed)
        var sqe *giouring.SubmissionQueueEntry
        const maxRetries = 3
        for j := 0; j < maxRetries; j++ {
            sqe = ring.GetSQE()
            if sqe != nil {
                break
            }
            if j < maxRetries-1 {
                time.Sleep(100 * time.Microsecond)
            }
        }

        if sqe == nil {
            // Ring full - clean up and break
            ln.recvCompLock.Lock()
            delete(ln.recvCompletions, requestID)
            ln.recvCompLock.Unlock()
            ln.recvBufferPool.Put(buffer)
            break
        }

        // Prepare recvmsg operation
        sqe.PrepareRecvMsg(ln.recvRingFd, &msg, 0)
        sqe.SetData64(requestID)

        sqes = append(sqes, sqe)
        compInfos = append(compInfos, compInfo)
        requestIDs = append(requestIDs, requestID)
    }

    // Batch submit all SQEs at once (single syscall)
    if len(sqes) > 0 {
        _, err := ring.Submit()
        if err != nil {
            // Submission failed - clean up all requests in batch
            ln.recvCompLock.Lock()
            for i, requestID := range requestIDs {
                delete(ln.recvCompletions, requestID)
                ln.recvBufferPool.Put(compInfos[i].buffer)
            }
            ln.recvCompLock.Unlock()
            ln.log("listen:recv:error", func() string {
                return fmt.Sprintf("failed to submit receive batch: %v", err)
            })
        }
    }
}
```

**Key Points:**
- **Batching**: Collects up to `batchSize` completions before processing (reduces syscall overhead)
- **Batch Processing**: Processes all completions in batch, then batch resubmits
- **Configurable**: Batch size is configurable via `IoUringRecvBatchSize` (default: 20)
- **Efficiency**: Instead of 1:1 (process 1, submit 1), uses N:N (process N, submit N in batch) - reduces syscalls by batch size ratio (e.g., 1:20)
- **No Latency Impact**: Since we maintain constant pending receives (e.g., 128), there are always many requests already queued. Batching just reduces syscall overhead - packets are already being received asynchronously.
- **Maintains Pending Count**: Still maintains constant pending receives, just with fewer syscalls
- **Better Under Load**: Less expensive work between Go userland and OS, especially beneficial under high load
- **Fallback**: If no completions available via PeekCQE, falls back to WaitCQE (blocking)

#### 7. Address Extraction Helper

Extract `net.Addr` from `syscall.RawSockaddrAny`:

```go
// extractAddrFromRSA extracts net.Addr from syscall.RawSockaddrAny
func extractAddrFromRSA(rsa *syscall.RawSockaddrAny) net.Addr {
    if rsa == nil {
        return nil
    }

    switch rsa.Addr.Family {
    case syscall.AF_INET:
        p := (*syscall.RawSockaddrInet4)(unsafe.Pointer(rsa))
        addr := &net.UDPAddr{
            IP:   net.IPv4(p.Addr[0], p.Addr[1], p.Addr[2], p.Addr[3]),
            Port: int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&p.Port))[:])),
        }
        return addr

    case syscall.AF_INET6:
        p := (*syscall.RawSockaddrInet6)(unsafe.Pointer(rsa))
        addr := &net.UDPAddr{
            IP:   make(net.IP, net.IPv6len),
            Port: int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&p.Port))[:])),
            Zone: zoneToString(int(p.Scope_id)),
        }
        copy(addr.IP, p.Addr[:])
        return addr

    default:
        return nil
    }
}

// zoneToString converts IPv6 scope ID to zone string
func zoneToString(zone int) string {
    if zone == 0 {
        return ""
    }
    // For now, return numeric string
    // Could be enhanced to resolve interface names
    return strconv.Itoa(zone)
}
```

#### 8. Cleanup and Shutdown

Drain remaining completions during shutdown (same pattern as send path):

```go
func (ln *listener) drainRecvCompletions() {
    ring, ok := ln.recvRing.(*giouring.Ring)
    if !ok || ring == nil {
        return
    }

    timeout := time.NewTimer(5 * time.Second)
    defer timeout.Stop()

    for {
        select {
        case <-timeout.C:
            // Timeout - give up on remaining completions
            ln.log("listen:recv:drain", func() string {
                return "timeout draining receive completions"
            })
            return

        default:
            // Try to get completion (non-blocking, same pattern as send path)
            cqe, err := ring.PeekCQE()
            if err != nil {
                if err == syscall.EAGAIN {
                    // No completions available - check if map is empty
                    ln.recvCompLock.RLock()
                    empty := len(ln.recvCompletions) == 0
                    ln.recvCompLock.RUnlock()

                    if empty {
                        return // All completions processed
                    }

                    // Wait a bit before checking again
                    time.Sleep(10 * time.Millisecond)
                    continue
                }

                // Other error
                ln.log("listen:recv:drain:error", func() string {
                    return fmt.Sprintf("error peeking completion: %v", err)
                })
                return
            }

            // Process completion (same pattern as send path)
            requestID := cqe.UserData

            ln.recvCompLock.Lock()
            compInfo, exists := ln.recvCompletions[requestID]
            if !exists {
                ln.recvCompLock.Unlock()
                ring.CQESeen(cqe)
                continue
            }
            delete(ln.recvCompletions, requestID)
            ln.recvCompLock.Unlock()

            // Cleanup (no reset needed - kernel overwrites on next use)
            ln.recvBufferPool.Put(compInfo.buffer)

            ring.CQESeen(cqe)
        }
    }
}

func (ln *listener) cleanupRecvRing() {
    if ln.recvRing == nil {
        return
    }

    // Stop completion handler (same pattern as send path)
    if ln.recvCompCancel != nil {
        ln.recvCompCancel()
    }

    // Wait for completion handler to finish (with timeout, same pattern as send path)
    done := make(chan struct{})
    go func() {
        ln.recvCompWg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Completion handler finished
    case <-time.After(5 * time.Second):
        // Timeout - log warning but continue
        ln.log("listen:recv:cleanup", func() string {
            return "timeout waiting for receive completion handler"
        })
    }

    // Drain any remaining completions
    ln.drainRecvCompletions()

    // Close the ring (same pattern as send path)
    ring, ok := ln.recvRing.(*giouring.Ring)
    if ok {
        ring.QueueExit()
    }

    // Clean up completion map and return all buffers to pool (no reset needed)
    ln.recvCompLock.Lock()
    for _, compInfo := range ln.recvCompletions {
        ln.recvBufferPool.Put(compInfo.buffer)
    }
    ln.recvCompletions = nil
    ln.recvCompLock.Unlock()
}
```

## Implementation Plan

### Phase 1: Foundation and Infrastructure

**Goal**: Set up basic infrastructure without changing the read path.

1. **Add Configuration Options** (`config.go`):
   - Add `IoUringRecvEnabled bool` flag to enable/disable io_uring for receives
   - Add `IoUringRecvRingSize int` for receive ring size (default: 256)
   - Add `IoUringRecvInitialPending int` for initial pending receives (default: 128)
   - Add `IoUringRecvBatchSize int` for completion batch size (default: 20)
   - Add validation to ensure ring size is power of 2 and within reasonable bounds (64-1024)
   - Add validation to ensure initial pending is reasonable (16-256, must be <= ring size)
   - Add validation to ensure batch size is reasonable (1-100)

2. **Create Helper Functions**:
   - Implement `extractAddrFromRSA()` function in `sockaddr.go` or new file
   - Add unit tests for IPv4 and IPv6 address extraction
   - Verify unsafe pointer usage follows Go stdlib patterns

3. **Add Receive Buffer Pool**:
   - Create `recvBufferPool` as `sync.Pool` of `[]byte` (fixed size `config.MSS`)
   - Simpler than send path - no Reset() needed, just Get() and Put()
   - Add to listener/dialer structs

### Phase 2: Ring Initialization and Cleanup

**Goal**: Initialize and clean up io_uring rings for receive operations.

1. **Add Ring Fields to Listener/Dialer**:
   - Add `recvRing interface{}` (type: `*giouring.Ring` on Linux, `nil` on others)
   - Add `recvRingFd int` for UDP socket file descriptor
   - Add `recvBufferPool sync.Pool` for receive buffers
   - Add completion tracking fields (same pattern as send path):
     - `recvCompletions map[uint64]*recvCompletionInfo` - Maps request ID to completion info
     - `recvCompLock sync.RWMutex` - Protects recvCompletions map
     - `recvRequestID atomic.Uint64` - Atomic counter for generating unique request IDs
   - Add completion handler lifecycle fields (same pattern as send path):
     - `recvCompCtx context.Context`
     - `recvCompCancel context.CancelFunc`
     - `recvCompWg sync.WaitGroup`

2. **Implement Ring Initialization** (`listen_linux.go`, `dial_linux.go`, same pattern as send path):
   - Create `initializeRecvRing()` method
   - Check `config.IoUringRecvEnabled`
   - Create ring with `giouring.NewRing()` and `QueueInit()`
   - Extract socket FD using `getUDPConnFD()`
   - Initialize receive buffer pool (same pattern as send path)
   - Initialize completion tracking map (same pattern as send path)
   - Create context for completion handler (same pattern as send path)
   - Start completion handler goroutine (same pattern as send path)
   - Pre-populate ring with initial pending receives (configurable, default 128) using `prePopulateRecvRing()`

3. **Implement Ring Cleanup**:
   - Create `cleanupRecvRing()` method
   - Stop completion handler
   - Drain remaining completions
   - Close ring with `QueueExit()`
   - Return buffers to pool

4. **Create Platform-Specific Files**:
   - `listen_linux.go`: Linux-specific receive ring implementation
   - `listen_other.go`: Stub implementations for non-Linux platforms
   - `dial_linux.go`: Linux-specific receive ring implementation
   - `dial_other.go`: Stub implementations for non-Linux platforms

### Phase 3: Completion Handler Implementation

**Goal**: Implement the completion handler that processes receive completions.

1. **Implement Completion Handler with Batching**:
   - Create `recvCompletionHandler()` method with batching support
   - Use `PeekCQE()` to collect batch of completions (non-blocking)
   - Fall back to `WaitCQE()` if no completions available (blocking)
   - Process batch of completions together
   - Look up completion info by request ID from map (same pattern as send path)
   - Extract source address from `RawSockaddrAny`
   - Parse packets with `packet.NewPacketFromData()`
   - Queue packets to `rcvQueue` (non-blocking)
   - Return buffers to pool after deserialization
   - Batch resubmit via `submitRecvRequestBatch()` to maintain constant pending count (reduces syscalls)

2. **Implement Batch Submission Function**:
   - Create `submitRecvRequestBatch()` method
   - Collects multiple SQEs before submitting (single syscall)
   - Handles errors gracefully (cleans up all requests in batch on failure)

2. **Implement Drain Function**:
   - Create `drainRecvCompletions()` method
   - Process remaining completions during shutdown
   - Handle timeout gracefully

3. **Error Handling**:
   - Handle transient errors (EAGAIN, EINTR) with resubmission
   - Handle fatal errors (EBADF) with cleanup
   - Log errors appropriately

### Phase 4: Integration and Migration

**Goal**: Integrate io_uring receive path into existing codebase.

1. **Update Listen() Method**:
   - Call `initializeRecvRing()` after socket creation
   - Conditionally use io_uring or fall back to `ReadFrom()` goroutine
   - Ensure socket FD is available for io_uring

2. **Update Dial() Method**:
   - Call `initializeRecvRing()` after socket creation
   - Conditionally use io_uring or fall back to `ReadFrom()` goroutine
   - Ensure socket FD is available for io_uring

3. **Update Cleanup**:
   - Call `cleanupRecvRing()` in listener/dialer shutdown
   - Ensure proper cleanup order

4. **Maintain Backward Compatibility**:
   - Fall back to `ReadFrom()` if io_uring unavailable
   - Fall back to `ReadFrom()` if `IoUringRecvEnabled` is false
   - Ensure existing tests pass with both paths

### Phase 5: Testing and Validation

**Goal**: Verify correctness and performance of io_uring receive path.

1. **Unit Tests**:
   - Test `extractAddrFromRSA()` with IPv4 and IPv6 addresses
   - Test buffer pool operations
   - Test ring initialization and cleanup

2. **Integration Tests**:
   - Test receive path with io_uring enabled
   - Test fallback to `ReadFrom()` when io_uring disabled
   - Test error handling (timeouts, shutdown, etc.)
   - Test with multiple connections

3. **Performance Tests**:
   - Benchmark receive path with io_uring vs. `ReadFrom()`
   - Measure latency improvements
   - Measure throughput improvements
   - Profile CPU usage

4. **Stress Tests**:
   - Test with high packet rates
   - Test with many connections
   - Test with packet loss scenarios

## Configuration

### New Config Options

```go
type Config struct {
    // ... existing fields ...

    // io_uring receive path configuration
    IoUringRecvEnabled      bool // Enable io_uring for receive operations
    IoUringRecvRingSize     int  // Size of receive ring (must be power of 2, 64-1024)
    IoUringRecvInitialPending int // Initial pending receives at startup (default: 128, must be <= ring size)
    IoUringRecvBatchSize    int  // Batch size for processing completions (default: 20, 1-100)
}
```

### Default Values

```go
defaultConfig := Config{
    // ... existing defaults ...

    IoUringRecvEnabled:      false, // Disabled by default (opt-in)
    IoUringRecvRingSize:     256,   // Default ring size
    IoUringRecvInitialPending: 128, // Default initial pending receives
    IoUringRecvBatchSize:    20,    // Default batch size
}
```

### Validation

```go
func (c *Config) Validate() error {
    // ... existing validation ...

    if c.IoUringRecvRingSize > 0 {
        // Must be power of 2
        if c.IoUringRecvRingSize&(c.IoUringRecvRingSize-1) != 0 {
            return fmt.Errorf("IoUringRecvRingSize must be power of 2")
        }

        // Must be within reasonable bounds
        if c.IoUringRecvRingSize < 64 || c.IoUringRecvRingSize > 1024 {
            return fmt.Errorf("IoUringRecvRingSize must be between 64 and 1024")
        }
    }

    if c.IoUringRecvInitialPending > 0 {
        // Must be reasonable
        if c.IoUringRecvInitialPending < 16 || c.IoUringRecvInitialPending > 256 {
            return fmt.Errorf("IoUringRecvInitialPending must be between 16 and 256")
        }

        // Must not exceed ring size
        if c.IoUringRecvRingSize > 0 && c.IoUringRecvInitialPending > c.IoUringRecvRingSize {
            return fmt.Errorf("IoUringRecvInitialPending (%d) must not exceed IoUringRecvRingSize (%d)",
                c.IoUringRecvInitialPending, c.IoUringRecvRingSize)
        }
    }

    if c.IoUringRecvBatchSize > 0 {
        // Must be reasonable
        if c.IoUringRecvBatchSize < 1 || c.IoUringRecvBatchSize > 100 {
            return fmt.Errorf("IoUringRecvBatchSize must be between 1 and 100")
        }
    }

    return nil
}
```

## Differences from Send Path

1. **Shared Ring**: Receive uses one ring per listener/dialer (not per-connection like send path)
2. **Constant Pending Receives**: We maintain a constant pool of pending receives (default: 128) by batch resubmitting after processing completions. Send path only submits when there's a packet to send.
3. **Batching**: Receive path uses batching to reduce syscall overhead (process N completions, submit N requests in batch). This is a pure win - no latency impact since packets are already being received by pending requests.
4. **Buffer Type**: Receive uses `[]byte` pool (fixed size MSS) - simpler and more efficient. Send path uses `bytes.Buffer` pool for variable-sized packet marshaling.
5. **Buffer Lifecycle**: Receive buffers are returned to pool after each completion (no Reset() needed - kernel overwrites). Send path keeps buffers in completion map until send completes.
6. **Pre-population**: Receive path pre-populates ring at startup with configurable pending receives (default: 128). Send path has no pre-population.
7. **Address Handling**: Receive path uses `syscall.Msghdr` and `RawSockaddrAny` to get source address (send path uses pre-computed address).

**Similarities (following same patterns):**
- Uses atomic counter for request IDs (`recvRequestID atomic.Uint64`)
- Uses map to track completions with lock (`recvCompletions map[uint64]*recvCompletionInfo`, `recvCompLock sync.RWMutex`)
- Same retry loops for GetSQE and Submit (maxRetries = 3, maxSubmitRetries = 3, 100μs sleep)
- Same error handling patterns (EINTR, EAGAIN retries, fatal errors don't retry)
- Same completion handler structure (WaitCQE, request ID lookup from map, cleanup)
- Same drain completions pattern (PeekCQE, check map empty, timeout)
- Same cleanup and shutdown pattern (context cancellation, wait with timeout, drain, QueueExit)

## Performance Considerations

1. **Ring Size**: Larger rings (256-512) can handle more bursts but use more memory
2. **Initial Pending Count**: More initial pending receives (128-256) improve throughput but use more buffers. Should be <= ring size.
3. **Batch Size**: Larger batch sizes (20-50) reduce syscall overhead with no latency impact. Smaller batches (5-10) have more syscall overhead but same latency. Since we maintain constant pending receives (e.g., 128), packets are already being received asynchronously - batching just reduces syscall overhead.
4. **Buffer Size**: Must match `config.MSS` to avoid truncation
5. **Batching Benefits**:
   - **Reduced Syscalls**: Instead of 1:1 (process 1 completion, submit 1 request), batching uses N:N (process N completions, submit N requests in batch)
   - **Example**: With batch size 20, processing 100 completions requires ~5 syscalls instead of 100
   - **No Latency Impact**: Since we maintain a constant pool of pending receives (e.g., 128), there are always many requests already queued. Batching just reduces syscall overhead without affecting latency - packets are already being received by the pending requests.
   - **Better Under Load**: Reducing syscall overhead (1:20 ratio) means less expensive work between Go userland and the OS, which helps performance especially under high load
   - **Pure Win**: This is the essence of io_uring - queue up all read requests in advance, so packets can arrive asynchronously while we batch process completions and resubmit

## Future Optimizations

1. **Multishot Receives**: Use `IORING_RECV_MULTISHOT` flag for even better performance (kernel 5.20+)
2. **Fixed Buffers**: Use `IORING_SETUP_SQE128` and fixed buffers for zero-copy receives
3. **Shared Completion Polling**: Multiple listeners could share a completion poller (advanced)

## References

- [io_uring Documentation](https://kernel.dk/io_uring.pdf)
- [giouring Library](https://github.com/randomizedcoder/giouring)
- [GoSRT Send Path Implementation](./io_uring_implementation.md)
- [IO_Uring Design Document](./IO_Uring.md)

