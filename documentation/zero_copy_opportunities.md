# Zero-Copy Buffer Reuse Opportunities

This document describes advanced zero-copy optimizations for the gosrt library that can eliminate memory copies in the packet processing path, both for clients and servers.

**Status:** Design Phase - Not Yet Implemented

**Prerequisites:**
- Requires modifications to the gosrt library
- Builds on existing io_uring infrastructure

---

## Overview

The key insight: **If we're just forwarding payload to a destination (file/stdout/subscribers), why copy the data at all?**

Instead of:
1. Receive UDP packet into buffer
2. Copy payload into packet.Packet structure
3. Copy from packet to user buffer
4. Copy to io_uring write buffer

We can:
1. Receive UDP packet into buffer
2. Parse header only (no payload copy)
3. Write directly from receive buffer to destination
4. Return buffer to pool after write completes

---

## Client Zero-Copy Buffer Reuse

### Current Flow (2-3 Copies per Packet)

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    CURRENT: 2-3 COPIES PER PACKET                        │
└─────────────────────────────────────────────────────────────────────────┘

1. Kernel → User (recvmsg)
   ┌──────────────────────────────────────────┐
   │ recvBufferPool buffer (size: MSS)        │
   │ [SRT Header][Payload..................] │
   └──────────────────────────────────────────┘
                    │
                    │ COPY #1: packet.NewPacketFromData()
                    ▼
2. Receive buffer → Packet struct
   ┌──────────────────────────────────────────┐
   │ packet.Packet                            │
   │   .header (parsed)                       │
   │   .payload = make([]byte) ← NEW ALLOC    │
   │              copy(payload, buffer[16:])  │
   └──────────────────────────────────────────┘

   [recvBufferPool.Put(buffer)] ← Buffer returned to pool

                    │
                    │ COPY #2: Read() to user buffer
                    ▼
3. Packet → User buffer
   ┌──────────────────────────────────────────┐
   │ user buffer in main.go                   │
   │ buffer := make([]byte, 2048)             │
   │ copy(buffer, packet.Data())              │
   └──────────────────────────────────────────┘

                    │
                    │ COPY #3: Write to io_uring
                    ▼
4. User buffer → io_uring write buffer
   ┌──────────────────────────────────────────┐
   │ writeBufferPool buffer                   │
   │ copy(writeBuf, buffer[:n])               │
   │ sqe.PrepareWrite(fd, writeBuf, ...)      │
   └──────────────────────────────────────────┘
```

### Proposed: Zero-Copy Buffer Reuse

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    PROPOSED: 0 COPIES (BUFFER REUSE)                     │
└─────────────────────────────────────────────────────────────────────────┘

1. Kernel → User (recvmsg) - same as before
   ┌──────────────────────────────────────────┐
   │ recvBufferPool buffer (size: MSS)        │
   │ [SRT Header][Payload..................] │
   │  ↑ 16 bytes  ↑ payloadOffset             │
   └──────────────────────────────────────────┘
                    │
                    │ NO COPY - just parse header offsets
                    ▼
2. Parse header, calculate payload offset
   ┌──────────────────────────────────────────┐
   │ payloadOffset := 16  // SRT header size  │
   │ payloadLen := bytesReceived - 16         │
   │                                          │
   │ // DON'T copy, DON'T return buffer yet!  │
   │ // Keep buffer reference alive           │
   └──────────────────────────────────────────┘
                    │
                    │ DIRECT WRITE from original buffer
                    ▼
3. io_uring write directly from receive buffer
   ┌──────────────────────────────────────────┐
   │ sqe.PrepareWrite(                        │
   │     fd,                                  │
   │     &buffer[payloadOffset],  // SAME BUF │
   │     payloadLen,                          │
   │     offset                               │
   │ )                                        │
   │ sqe.SetData64(bufferID)  // Track buffer │
   │ ring.Submit()                            │
   └──────────────────────────────────────────┘
                    │
                    │ On write completion
                    ▼
4. Return buffer to pool (in completion handler)
   ┌──────────────────────────────────────────┐
   │ // Write completion handler              │
   │ bufferID := cqe.UserData                 │
   │ recvBufferPool.Put(buffers[bufferID])    │
   └──────────────────────────────────────────┘

RESULT: 0 copies, 0 allocations, buffer reused directly!
```

### Implementation: ZeroCopyPacket Structure

```go
// In packet.go - new zero-copy packet structure
type ZeroCopyPacket struct {
    buffer       *[]byte  // Original receive buffer (from pool)
    bufferPool   *sync.Pool
    payloadStart int      // Offset where payload begins
    payloadLen   int      // Length of payload
    header       Header   // Parsed header (small, stack-allocated)
}

// PayloadSlice returns a slice of the original buffer (no copy!)
func (p *ZeroCopyPacket) PayloadSlice() []byte {
    buf := *p.buffer
    return buf[p.payloadStart : p.payloadStart+p.payloadLen]
}

// Decommission returns the buffer to pool (call after write completes!)
func (p *ZeroCopyPacket) Decommission() {
    if p.buffer != nil {
        p.bufferPool.Put(p.buffer)
        p.buffer = nil
    }
}
```

### Modified Receive Completion

```go
// In dial_linux.go - modified receive completion
func (dl *dialer) processRecvCompletionZeroCopy(ring *giouring.Ring, cqe *giouring.CompletionQueueEvent, compInfo *recvCompletionInfo) {
    bufferPtr := compInfo.buffer
    buffer := *bufferPtr
    bytesReceived := int(cqe.Res)

    // Parse header only (no payload copy)
    header, err := packet.ParseHeaderOnly(buffer[:bytesReceived])
    if err != nil {
        dl.recvBufferPool.Put(bufferPtr)
        ring.CQESeen(cqe)
        return
    }

    // Create zero-copy packet (keeps buffer reference)
    zcp := &ZeroCopyPacket{
        buffer:       bufferPtr,
        bufferPool:   &dl.recvBufferPool,
        payloadStart: packet.SRT_HEADER_SIZE,  // 16 bytes
        payloadLen:   bytesReceived - packet.SRT_HEADER_SIZE,
        header:       header,
    }

    // DON'T return buffer to pool yet!
    // Pass to output writer which will call Decommission() after write completes
    dl.zeroCopyOutput <- zcp

    ring.CQESeen(cqe)
}
```

### Zero-Copy Write Path

```go
// In client-performance - zero-copy write path
func runSrtToStdoutZeroCopy(ctx context.Context, srtConn ZeroCopyConn, writer *IoUringWriter) {
    for {
        // Receive zero-copy packet (buffer not freed yet)
        zcp, err := srtConn.ReadZeroCopyPacket()
        if err != nil {
            return
        }

        // Get payload slice (no copy - points into original recv buffer)
        payload := zcp.PayloadSlice()

        // Submit io_uring write directly from recv buffer
        // Pass zcp so completion handler can call Decommission()
        writer.WriteZeroCopy(payload, zcp)
    }
}

// IoUringWriter with zero-copy support
func (w *IoUringWriter) WriteZeroCopy(data []byte, packet *ZeroCopyPacket) error {
    reqID := w.requestID.Add(1)

    // Store packet reference for completion handler
    w.compLock.Lock()
    w.completions[reqID] = &writeCompletionInfo{
        zeroCopyPacket: packet,  // Will call Decommission() on completion
    }
    w.compLock.Unlock()

    // Write directly from original receive buffer!
    sqe := w.ring.GetSQE()
    sqe.PrepareWrite(w.fd, uintptr(unsafe.Pointer(&data[0])), uint32(len(data)), 0)
    sqe.SetData64(reqID)
    w.ring.Submit()

    return nil
}

// Completion handler
func (w *IoUringWriter) completionHandler() {
    for {
        cqe, err := w.ring.WaitCQE()
        if err != nil {
            return
        }

        reqID := cqe.UserData

        w.compLock.Lock()
        if info, ok := w.completions[reqID]; ok {
            // Return receive buffer to pool via Decommission()
            if info.zeroCopyPacket != nil {
                info.zeroCopyPacket.Decommission()
            }
            delete(w.completions, reqID)
        }
        w.compLock.Unlock()

        w.ring.CQESeen(cqe)
    }
}
```

### Buffer Lifetime

```
Time ──────────────────────────────────────────────────────────────────────►

Buffer A:
┌─────────────────────────────────────────────────────────────────────────┐
│ Get from pool    recvmsg     Parse header   io_uring write   Return    │
│      │              │             │               │         to pool    │
│      ▼              ▼             ▼               ▼            │       │
│   [alloc]────────[recv]───────[parse]─────────[write]──────[decommit]  │
│                                                                         │
│   ◄─────────────── Buffer A stays alive ────────────────────►          │
└─────────────────────────────────────────────────────────────────────────┘

Key: Buffer is NOT returned to pool until write completion!
```

### Advanced: io_uring Linked Operations

giouring supports linked SQEs - we could chain recv → write:

```go
// Submit linked recv → write (kernel executes sequentially)
sqeRecv := ring.GetSQE()
sqeRecv.PrepareRecvMsg(recvFd, msg, 0)
sqeRecv.Flags |= giouring.SqeIOLink  // Link to next SQE

sqeWrite := ring.GetSQE()
sqeWrite.PrepareWrite(writeFd, &buffer[payloadOffset], payloadLen, 0)

ring.Submit()  // Both submitted together, write waits for recv
```

### When to Use Zero-Copy

| Use Case | Zero-Copy Possible? | Notes |
|----------|---------------------|-------|
| SRT → file/stdout (unencrypted) | ✅ YES | Ideal case - just forward payload |
| SRT → file/stdout (encrypted) | ⚠️ PARTIAL | Decryption writes to same buffer |
| SRT → SRT relay | ✅ YES | Forward entire packet, not just payload |
| SRT → application processing | ❌ NO | App needs to read/modify data |

### Constraints

1. **Buffer lifetime**: Must track when write completes before returning buffer
2. **Encryption**: If SRT payload is encrypted, decryption happens in-place (still zero-copy)
3. **Congestion control**: For receive, still need to track sequence numbers (header parsing)
4. **Library modification**: Requires changes to gosrt library's packet handling

### Expected Improvement

- Eliminates 2-3 copies per packet
- Eliminates 1-2 allocations per packet
- Reduces memory bandwidth by 60-80%
- Lower latency (no copy overhead)

---

## io_uring Registered Buffers

For even more performance, use `io_uring_register_buffers()` to pin buffers:

```go
// Register fixed buffers with kernel (done once at startup)
func (dl *dialer) registerBuffers() error {
    // Create fixed buffer array
    dl.fixedBuffers = make([][]byte, RING_SIZE)
    for i := range dl.fixedBuffers {
        dl.fixedBuffers[i] = make([]byte, dl.config.MSS)
    }

    // Register with io_uring
    return dl.ring.RegisterBuffers(dl.fixedBuffers)
}

// Use fixed buffer for receive
sqe.PrepareRecvMsgFixed(fd, msg, 0, bufferIndex)

// Use same fixed buffer for write (zero-copy!)
sqe.PrepareWriteFixed(fd, bufferIndex, payloadLen, offset)
```

**Benefits:**
- Buffers pinned in memory (no page faults)
- Kernel can DMA directly to/from buffers
- Even lower latency than regular io_uring

---

## Server Zero-Copy with Fan-Out

### Server Use Case

- 10-20 publish streams @ 10-20 Mb/s each = 100-400 Mb/s total ingest
- 20-30 downstream SRT subscribers per stream
- Fan-out ratio: 1 publish → 20-30 subscribers = 20-30x write amplification
- TSBPD (Timestamp-Based Packet Delivery) holds packets until delivery time

### Current Server Flow (Multiple Copies per Subscriber)

```
┌─────────────────────────────────────────────────────────────────────────┐
│     CURRENT SERVER: COPIES MULTIPLY WITH SUBSCRIBERS                    │
└─────────────────────────────────────────────────────────────────────────┘

Publisher → Server:

1. recvmsg → recvBufferPool buffer
   ┌──────────────────────────────────────────┐
   │ [SRT Header][Payload..................] │
   └──────────────────────────────────────────┘
                    │
                    │ COPY #1: NewPacketFromData()
                    ▼
2. Create packet.Packet (copies payload)
   ┌──────────────────────────────────────────┐
   │ packet.Packet                            │
   │   .payload = copy of payload             │
   └──────────────────────────────────────────┘
   [recvBufferPool.Put(buffer)] ← Returned immediately

                    │
                    │ TSBPD holds packet until delivery time
                    ▼
3. Congestion control / reordering buffer
   ┌──────────────────────────────────────────┐
   │ recv.Push(packet) → internal queue       │
   │ Waits for TSBPD timestamp...             │
   └──────────────────────────────────────────┘

Server → Subscribers (FOR EACH of 20-30 subscribers!):
                    │
                    ▼
4. deliver() to each subscriber (20-30x)
   ┌──────────────────────────────────────────┐
   │ For each subscriber:                     │
   │   COPY #2: packet.Clone() or reference   │
   │   COPY #3: Write to subscriber's buffer  │
   │   COPY #4: io_uring send buffer          │
   └──────────────────────────────────────────┘

Total: 1 + (3 × 25 subscribers) = ~76 copies per packet!
At 10 Mb/s (950 pkt/s): 72,000+ copies/second per stream!
```

### Proposed Server Zero-Copy Flow

```
┌─────────────────────────────────────────────────────────────────────────┐
│     PROPOSED SERVER: SINGLE BUFFER, REFERENCE COUNTING                  │
└─────────────────────────────────────────────────────────────────────────┘

Publisher → Server:

1. recvmsg → recvBufferPool buffer
   ┌──────────────────────────────────────────┐
   │ [SRT Header][Payload..................] │
   │     16 bytes   ↑ payloadOffset           │
   └──────────────────────────────────────────┘
                    │
                    │ NO COPY - just parse header
                    ▼
2. Create ZeroCopyPacket (keeps buffer reference)
   ┌──────────────────────────────────────────┐
   │ ZeroCopyPacket                           │
   │   .buffer = recvBufferPool buffer (ptr)  │
   │   .payloadOffset = 16                    │
   │   .payloadLen = n - 16                   │
   │   .refCount = atomic.Int32{1}  ← KEY!    │
   │   .header = parsed header                │
   └──────────────────────────────────────────┘

   [Buffer NOT returned to pool - still in use!]

                    │
                    │ TSBPD holds ZeroCopyPacket
                    ▼
3. Congestion control buffer (same ZeroCopyPacket)
   ┌──────────────────────────────────────────┐
   │ recv.Push(zcp)                           │
   │ Packet sits in buffer until TSBPD time   │
   │ Buffer stays alive (refCount = 1)        │
   └──────────────────────────────────────────┘

Server → Subscribers (fan-out with reference counting):
                    │
                    ▼
4. TSBPD triggers delivery
   ┌──────────────────────────────────────────┐
   │ // Increment refCount for each subscriber│
   │ zcp.refCount.Add(int32(numSubscribers))  │
   │ // Now refCount = 1 + 25 = 26            │
   └──────────────────────────────────────────┘
                    │
        ┌───────────┼───────────┬─────────────┐
        ▼           ▼           ▼             ▼
5. Each subscriber writes directly from SAME buffer
   ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐
   │ Sub 1   │ │ Sub 2   │ │ Sub 3   │ │ Sub 25  │
   │         │ │         │ │         │ │         │
   │ io_uring│ │ io_uring│ │ io_uring│ │ io_uring│
   │ write   │ │ write   │ │ write   │ │ write   │
   │ from    │ │ from    │ │ from    │ │ from    │
   │ zcp.buf │ │ zcp.buf │ │ zcp.buf │ │ zcp.buf │
   │ [16:]   │ │ [16:]   │ │ [16:]   │ │ [16:]   │
   └────┬────┘ └────┬────┘ └────┬────┘ └────┬────┘
        │           │           │             │
        ▼           ▼           ▼             ▼
6. Each completion decrements refCount
   ┌──────────────────────────────────────────┐
   │ On each write completion:                │
   │   if zcp.refCount.Add(-1) == 0 {         │
   │       recvBufferPool.Put(zcp.buffer)     │
   │   }                                      │
   └──────────────────────────────────────────┘

Total: 0 copies! Buffer reused by ALL subscribers!
At 10 Mb/s (950 pkt/s): 0 copies/second!
```

### Implementation: ZeroCopyPacket with Reference Counting

```go
// ZeroCopyPacket for server fan-out
type ZeroCopyPacket struct {
    buffer       *[]byte       // Original receive buffer (from pool)
    bufferPool   *sync.Pool    // Pool to return buffer to
    payloadStart int           // Offset where payload begins (typically 16)
    payloadLen   int           // Length of payload
    header       packet.Header // Parsed header (small, stack-allocated)

    // Reference counting for fan-out
    refCount     atomic.Int32  // Number of pending writes
}

// NewZeroCopyPacket creates packet without copying payload
func NewZeroCopyPacket(buffer *[]byte, pool *sync.Pool, bytesReceived int) (*ZeroCopyPacket, error) {
    buf := *buffer

    // Parse header only (no payload copy)
    header, err := packet.ParseHeaderOnly(buf[:bytesReceived])
    if err != nil {
        return nil, err
    }

    return &ZeroCopyPacket{
        buffer:       buffer,
        bufferPool:   pool,
        payloadStart: packet.SRT_HEADER_SIZE,  // 16 bytes
        payloadLen:   bytesReceived - packet.SRT_HEADER_SIZE,
        header:       header,
        refCount:     atomic.Int32{},  // Starts at 0
    }, nil
}

// PayloadSlice returns slice of original buffer (no copy!)
func (p *ZeroCopyPacket) PayloadSlice() []byte {
    buf := *p.buffer
    return buf[p.payloadStart : p.payloadStart+p.payloadLen]
}

// FullPacketSlice returns entire packet including header (for SRT relay)
func (p *ZeroCopyPacket) FullPacketSlice() []byte {
    buf := *p.buffer
    return buf[:p.payloadStart+p.payloadLen]
}

// Retain increments reference count (call before fan-out)
func (p *ZeroCopyPacket) Retain(count int32) {
    p.refCount.Add(count)
}

// Release decrements reference count, returns buffer to pool when 0
func (p *ZeroCopyPacket) Release() {
    if p.refCount.Add(-1) <= 0 {
        // Last reference - return buffer to pool
        p.bufferPool.Put(p.buffer)
        p.buffer = nil
    }
}
```

### Server Fan-Out with Zero-Copy

```go
// In server's deliver() function
func (s *server) deliverToSubscribers(zcp *ZeroCopyPacket, subscribers []*srtConn) {
    numSubs := int32(len(subscribers))
    if numSubs == 0 {
        zcp.Release()  // No subscribers, release immediately
        return
    }

    // Increment refCount for all subscribers at once
    zcp.Retain(numSubs)

    // Get payload slice (no copy - all subscribers share this!)
    payload := zcp.PayloadSlice()

    // Fan-out to all subscribers
    for _, sub := range subscribers {
        // Submit io_uring write from shared buffer
        sub.writeZeroCopy(payload, zcp)
    }

    // Original reference from receive path
    zcp.Release()
}

// Per-subscriber zero-copy write
func (c *srtConn) writeZeroCopy(data []byte, zcp *ZeroCopyPacket) {
    reqID := c.sendRequestID.Add(1)

    // Track ZeroCopyPacket for completion handler
    c.sendCompLock.Lock()
    c.sendCompletions[reqID] = &sendCompletionInfo{
        zeroCopyPacket: zcp,  // Will call Release() on completion
    }
    c.sendCompLock.Unlock()

    // io_uring write directly from shared receive buffer!
    sqe := c.sendRing.GetSQE()
    sqe.PrepareWrite(c.sendRingFd, uintptr(unsafe.Pointer(&data[0])), uint32(len(data)), 0)
    sqe.SetData64(reqID)
    c.sendRing.Submit()
}

// Completion handler (per-connection)
func (c *srtConn) sendCompletionHandler() {
    for {
        cqe, err := c.sendRing.WaitCQE()
        if err != nil {
            return
        }

        reqID := cqe.UserData

        c.sendCompLock.Lock()
        if info, ok := c.sendCompletions[reqID]; ok {
            // Decrement refCount, return buffer when all done
            if info.zeroCopyPacket != nil {
                info.zeroCopyPacket.Release()
            }
            delete(c.sendCompletions, reqID)
        }
        c.sendCompLock.Unlock()

        c.sendRing.CQESeen(cqe)
    }
}
```

### TSBPD Integration

The key insight: **Buffer stays alive through entire TSBPD delay!**

```
Time ──────────────────────────────────────────────────────────────────────►

                                    TSBPD Delay
                              ◄────────────────────►

Buffer A lifecycle:
┌─────────────────────────────────────────────────────────────────────────┐
│ Get    recvmsg   Parse   Hold in     TSBPD      Fan-out    Return      │
│ from            header   reorder    triggers    writes     to pool     │
│ pool             only    buffer                 (25x)                  │
│   │       │        │        │           │          │           │       │
│   ▼       ▼        ▼        ▼           ▼          ▼           ▼       │
│ [get]──[recv]──[parse]──[buffer]────[deliver]──[write×25]──[put]      │
│                                                                         │
│   ◄────────────── Buffer A stays alive ────────────────────────►       │
│   refCount: 1       1        1         26         25...1       0       │
└─────────────────────────────────────────────────────────────────────────┘
```

### Congestion Control Buffer with Zero-Copy

```go
// Modified recv.Push() for zero-copy
func (r *liveReceiver) PushZeroCopy(zcp *ZeroCopyPacket) {
    seq := zcp.header.PacketSequenceNumber

    // Retain packet while in reorder buffer
    zcp.Retain(1)

    // Add to reorder buffer (btree or list)
    r.buffer.Insert(seq, zcp)

    // When TSBPD time arrives, deliver() is called
    // which fans out to subscribers
}

// Modified OnTick() for TSBPD delivery
func (r *liveReceiver) OnTick(now time.Time) {
    // Find packets ready for delivery (TSBPD time reached)
    ready := r.buffer.GetReady(now)

    for _, zcp := range ready {
        // Fan-out to all subscribers (zero-copy!)
        r.onDeliver(zcp)

        // Release our reference (from PushZeroCopy)
        zcp.Release()
    }
}
```

### SRT-to-UDP Extraction (Client Subscriber)

For subscribers that want just the payload (not full SRT packet):

```go
// Subscriber receives ZeroCopyPacket, extracts payload to UDP
func (client *subscriber) receiveAndForwardToUDP(zcp *ZeroCopyPacket) {
    // Wait for TSBPD timestamp (packet.Header has timestamp)
    client.waitForTSBPD(zcp.header.Timestamp)

    // Get payload slice (no copy - points into original recv buffer)
    payload := zcp.PayloadSlice()

    // Write payload to UDP output (io_uring)
    // Still zero-copy - writing from same buffer!
    client.udpWriter.WriteZeroCopy(payload, zcp)

    // Buffer returned to pool when UDP write completes
}
```

---

## Performance Impact Summary

### Client Zero-Copy

| Metric | Current Client | Zero-Copy Client |
|--------|---------------|------------------|
| Copies/packet | 2-3 | 0 |
| Allocations/packet | 1-2 | 0 |
| Memory bandwidth | ~4 KB/packet | ~0 |

### Server Zero-Copy (20 streams × 25 subscribers)

| Metric | Current Server | Zero-Copy Server | Improvement |
|--------|----------------|------------------|-------------|
| Copies/packet (25 subs) | ~76 | 0 | **100% reduction** |
| Memory bandwidth | 1.8 GB/sec | ~0 | **1.8 GB/sec saved** |
| Buffer pool operations | 25x per packet | 1x per packet | **25x reduction** |
| CPU cycles (memcpy) | High | Near-zero | **Significant** |
| Cache efficiency | Poor (thrashing) | Good (no data touch) | **Better** |

---

## Constraints and Considerations

1. **Buffer Lifetime**: Buffers held longer (through TSBPD delay + all subscriber writes)
   - May need larger buffer pool
   - Pool size = (packets_in_flight × max_tsbpd_delay × num_streams)

2. **Reference Counting Overhead**: Atomic operations for refCount
   - Still much cheaper than copying data
   - One atomic Add per subscriber, not per byte

3. **Memory Pinning**: Consider `io_uring_register_buffers()` for fixed buffers
   - Reduces page faults during writes
   - Kernel can DMA directly

4. **Encryption**: If SRT encryption enabled:
   - Decryption happens in-place (still zero-copy!)
   - Decrypt before fan-out, all subscribers get decrypted data

---

## Implementation Phases

### Phase 1: Client Zero-Copy (4-6 hours)

| Task | Effort | Description |
|------|--------|-------------|
| ZeroCopyPacket struct | 1 hour | New packet type holding buffer reference |
| ParseHeaderOnly() | 30 min | Parse header without payload copy |
| ReadZeroCopyPacket() API | 1 hour | New API for zero-copy receive |
| WriteZeroCopy() | 1 hour | Write directly from recv buffer |
| Completion chaining | 1 hour | Return buffer after write completes |
| Linked SQEs (optional) | 1 hour | Chain recv → write in io_uring |

### Phase 2: Server Zero-Copy with Fan-Out (6-8 hours)

| Task | Effort | Description |
|------|--------|-------------|
| Reference counting | 1 hour | Add atomic refCount to ZeroCopyPacket |
| Retain/Release API | 1 hour | Thread-safe reference management |
| Modified recv.Push() | 2 hours | Store ZeroCopyPacket in reorder buffer |
| Modified deliver() | 2 hours | Fan-out with reference counting |
| Subscriber completion | 1 hour | Release on write completion |
| TSBPD integration | 1 hour | Ensure buffer lives through delay |

### Phase 3: Registered Buffers (2-3 hours)

| Task | Effort | Description |
|------|--------|-------------|
| RegisterBuffers() | 1 hour | Pin buffers with io_uring |
| Fixed buffer recv | 1 hour | Use registered buffers for recv |
| Fixed buffer write | 1 hour | Use same buffers for write |

---

## Related Documents

- `client_performance_analysis.md` - Client performance overview and immediate optimizations
- `IO_Uring.md` - io_uring send path implementation
- `IO_Uring_read_path.md` - io_uring receive path implementation

