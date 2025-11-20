# IO_Uring

This is a design document to plan how to implement IO_Uring in GoSRT.

# IO_Uring Background

IO_Uring is a Linux kernel feature that allows for asynchronous I/O operations. It is a user-space library that provides a high-performance, low-latency I/O interface for applications.

IO_Uring means that the network read and write operations are done asynchronously, without blocking the main thread, and show significant performance improvement.

# Golang IO_Uring Libraries

There are multiple libraries available for Go to use IO_Uring, so these will need to be evaluated to determine which one to use.

## giouring
There is one that is essentially a port of the liburing library, so it is a direct port of the C library.
https://github.com/pawelgaczynski/giouring

```
[das@l:~/Downloads]$ cd giouring/

[das@l:~/Downloads/giouring]$ ls
bench           completion_test.go  include.go      lib_test.go       network_test.go  probe_test.go  register.go       splice_test.go      udp_recv_send_test.go
buffer.go       const.go            kernel.go       LICENSE           prepare.go       queue.go       register_test.go  submission_test.go  version.go
buffer_test.go  go.mod              kernel_test.go  linked.go         prepare_test.go  README.md      ring.go           syscall.go
common_test.go  go.sum              lib.go          msg_ring_test.go  probe.go         recvmsg.go     setup.go          sys.go

[das@l:~/Downloads/giouring]$ head README.md
<a name="readme-top"></a>

# giouring - about the project

**giouring** is a Go port of the [liburing](https://github.com/axboe/liburing) library. It is written entirely in Go. No cgo.

Almost all functions and structures from [liburing](https://github.com/axboe/liburing) was implemented.

* **giouring** versioning is aligned with [liburing](https://github.com/axboe/liburing) versioning.
* **giouring** is currently up to date with [liburing](https://github.com/axboe/liburing) commit: [e1e758ae8360521334399c2a6eace05fa518e218](https://github.com/axboe/liburing/commit/e1e758ae8360521334399c2a6eace05fa518e218)

[das@l:~/Downloads/giouring]$
```

## iouring-go
This is a library that is a wrapper around the liburing library, and provides a higher-level API for using IO_Uring.
https://github.com/Iceber/iouring-go

iouring-go repo has been cloned into a local folder for analysis.  The code includes example implmentations, which can potentially be largely copied.
```
[das@l:~/Downloads]$ cd iouring-go/

[das@l:~/Downloads/iouring-go]$ ls -la
total 216
drwxr-xr-x  5 das users  4096 Nov 16 12:59 .
drwxr-xr-x 76 das users 65536 Nov 16 12:59 ..
-rw-r--r--  1 das users  1154 Nov 16 12:59 BUILD.bazel
-rw-r--r--  1 das users   489 Nov 16 12:59 errors.go
-rw-r--r--  1 das users   486 Nov 16 12:59 eventfd.go
drwxr-xr-x 15 das users  4096 Nov 16 12:59 examples
-rw-r--r--  1 das users   574 Nov 16 12:59 fixed_buffers.go
-rw-r--r--  1 das users  5474 Nov 16 12:59 fixed_files.go
drwxr-xr-x  7 das users  4096 Nov 16 12:59 .git
-rw-r--r--  1 das users   106 Nov 16 12:59 go.mod
-rw-r--r--  1 das users   207 Nov 16 12:59 go.sum
-rw-r--r--  1 das users  8853 Nov 16 12:59 iouring.go
-rw-r--r--  1 das users   951 Nov 16 12:59 iouring_test.go
-rw-r--r--  1 das users  1065 Nov 16 12:59 LICENSE
-rw-r--r--  1 das users  2319 Nov 16 12:59 link_request.go
-rw-r--r--  1 das users  4177 Nov 16 12:59 mmap.go
-rw-r--r--  1 das users  2694 Nov 16 12:59 options.go
-rw-r--r--  1 das users  1982 Nov 16 12:59 poller.go
-rw-r--r--  1 das users 18181 Nov 16 12:59 prep_request.go
-rw-r--r--  1 das users    33 Nov 16 12:59 probe.go
-rw-r--r--  1 das users  4003 Nov 16 12:59 README.md
-rw-r--r--  1 das users  6338 Nov 16 12:59 request.go
drwxr-xr-x  2 das users  4096 Nov 16 12:59 syscall
-rw-r--r--  1 das users  1785 Nov 16 12:59 timeout.go
-rw-r--r--  1 das users  7456 Nov 16 12:59 types.go
-rw-r--r--  1 das users  1238 Nov 16 12:59 user_data.go
-rw-r--r--  1 das users   600 Nov 16 12:59 utils.go
-rw-r--r--  1 das users  1058 Nov 16 12:59 WORKSPACE

[das@l:~/Downloads/iouring-go]$ ls ./examples/
cat/                cp/                 echo-with-callback/ link/               mv/                 rm/                 timeout/
concurrent-cat/     echo/               hardlink/           mkdir/              nvme-id-ctrl/       symlink/

[das@l:~/Downloads/iouring-go]$ ls ./examples/echo/
BUILD.bazel  client.go    README.md    server.go

[das@l:~/Downloads/iouring-go]$ ls ./examples/echo/server.go
./examples/echo/server.go
```


# Evaluation Criteria

The following criteria will be used to evaluate the libraries:

- Performance
- Ease of use
- Documentation
- Community support
- License
- Compatibility
- Features
- Limitations

# Evaluation

## giouring

### Performance
- **Excellent**: Pure Go implementation with no cgo overhead, providing direct syscall access
- Direct port of liburing, ensuring optimal performance characteristics
- Supports all advanced io_uring features including:
  - Fixed files and buffers
  - Buffer rings for zero-copy operations
  - Multishot operations for reduced syscall overhead
  - SQPoll mode for kernel polling (though not yet implemented in tests)
- Used by Gain, a high-performance networking framework, demonstrating real-world performance

### Ease of Use
- **Moderate**: Low-level API that closely mirrors liburing C API
- Requires understanding of io_uring concepts (SQE, CQE, submission, completion)
- Manual buffer management and unsafe pointer usage required
- More verbose code but provides fine-grained control
- Example: UDP operations require manual setup of `syscall.Msghdr` structures

### Documentation
- **Good**: Comprehensive README with implementation status
- Go documentation available on pkg.go.dev
- Well-documented API mapping to liburing functions
- Test files provide good examples (network_test.go, udp_recv_send_test.go)
- Version alignment with liburing makes it easy to reference liburing documentation

### Community Support
- **Moderate**: Active project with recent commits
- Used by at least one production project (Gain framework)
- MIT License encourages adoption
- Single maintainer (Paweł Gaczyński)

### License
- **MIT License**: Permissive, compatible with most projects

### Compatibility
- **Excellent**: Pure Go, no cgo dependencies
- Requires Go 1.20+
- Tested on kernel 6.2.0+, but should work on older kernels (5.1+ for basic io_uring)
- No external C library dependencies

### Features
- **Comprehensive**: Almost complete port of liburing
- Supports all major io_uring operations:
  - Network I/O: `PrepareRecv`, `PrepareSend`, `PrepareRecvMsg`, `PrepareSendMsg`, `PrepareSendto`, `PrepareRecvfrom`
  - File I/O: Read, Write, Readv, Writev
  - Socket operations: Accept, Connect, Socket
  - Advanced features: Buffer rings, fixed files, multishot operations
- UDP support via `PrepareRecvMsg` and `PrepareSendMsg` (as shown in udp_recv_send_test.go)
- Direct file descriptor operations

### Limitations
- Low-level API requires more boilerplate code
- Manual memory management with unsafe pointers
- Requires deeper understanding of io_uring internals
- Test coverage is currently low (acknowledged in README)
- Some newer kernel features may not be tested on older kernels

### GoSRT Integration Notes
- UDP operations would use `PrepareRecvMsg` for receiving (with `syscall.Msghdr` containing address info)
- UDP operations would use `PrepareSendMsg` for sending (with destination address in `syscall.Msghdr`)
- Would need to manage ring submission and completion queues manually
- Buffer management could leverage fixed buffers or buffer rings for performance

---

## iouring-go

### Performance
- **Good**: Wrapper around liburing syscalls, minimal overhead
- Higher-level abstractions may add slight overhead compared to direct syscalls
- Supports concurrent request submission
- Channel-based result handling provides good Go integration
- Request linking support for dependent operations

### Ease of Use
- **Excellent**: High-level, idiomatic Go API
- Channel-based completion handling fits Go's concurrency model
- Request builder pattern with method chaining (`.WithInfo()`, `.WithCallback()`)
- Cleaner API: `iouring.Sendto()`, `iouring.Recvfrom()` vs manual SQE preparation
- Example code shows simple, readable patterns

### Documentation
- **Good**: README with quickstart examples
- Multiple example implementations (echo server, cat, cp, etc.)
- Go documentation on pkg.go.dev
- Examples demonstrate common use cases
- Less comprehensive than giouring's liburing mapping

### Community Support
- **Moderate**: Active project with examples
- MIT License
- Single maintainer (Iceber)
- Less production usage visibility compared to giouring

### License
- **MIT License**: Permissive, compatible with most projects

### Compatibility
- **Good**: Pure Go with syscall package
- Requires Go 1.15+
- Requires Linux Kernel >= 5.6 (higher than giouring's 5.1+)
- No cgo dependencies

### Features
- **Good**: Covers most common io_uring operations
- Network I/O: `Send`, `Recv`, `Sendto`, `Recvfrom`, `Sendmsg`, `Recvmsg`, `Accept`
- File I/O: `Read`, `Write`, `Pread`, `Pwrite`, `Readv`, `Writev`
- Advanced features:
  - Request linking
  - Timeouts
  - Request cancellation
  - Extra info attachment (`.WithInfo()`)
  - Callbacks (`.WithCallback()`)
- Fixed files support
- **Missing**: Buffer rings, some advanced features (acknowledged in README TODO)

### Limitations
- Higher minimum kernel version (5.6 vs 5.1)
- Some advanced io_uring features not yet implemented (buffer rings, SQPoll)
- Less control over low-level io_uring parameters
- Channel-based model may not fit all use cases (though generally good for Go)

### GoSRT Integration Notes
- UDP operations would use `iouring.Recvfrom()` and `iouring.Sendto()` - much simpler API
- Result handling via channels fits GoSRT's existing channel-based architecture
- Request linking could be useful for dependent operations
- `.WithInfo()` allows attaching packet metadata to requests
- Less manual buffer management required

---

## Comparison Summary

| Criterion | giouring | iouring-go | Winner |
|-----------|----------|------------|--------|
| Performance | Excellent (pure Go, no cgo) | Good (wrapper overhead) | giouring |
| Ease of Use | Moderate (low-level) | Excellent (high-level) | iouring-go |
| Documentation | Good | Good | Tie |
| Community | Moderate | Moderate | Tie |
| License | MIT | MIT | Tie |
| Compatibility | Excellent (Go 1.20+, kernel 5.1+) | Good (Go 1.15+, kernel 5.6+) | giouring |
| Features | Comprehensive | Good (missing some advanced) | giouring |
| Go Idioms | Low-level | High-level, idiomatic | iouring-go |

## Recommendation

**For GoSRT, iouring-go appears to be the better initial choice** for the following reasons:

1. **Ease of Integration**: The high-level API with channels aligns well with GoSRT's existing channel-based packet handling
2. **UDP Support**: Simple `Recvfrom`/`Sendto` APIs match GoSRT's UDP-based protocol
3. **Go Idioms**: More idiomatic Go code, easier to maintain
4. **Sufficient Features**: Has all features needed for initial io_uring implementation (UDP send/recv)

**However, giouring should be considered** if:
- Maximum performance is critical and the extra complexity is acceptable
- Advanced features like buffer rings are needed for optimization
- Fine-grained control over io_uring parameters is required

**Migration Path**: Start with iouring-go for easier implementation and validation. If performance profiling shows bottlenecks, consider migrating specific hot paths to giouring for optimization.

---

# GoSRT current network send/receive

The current GoSRT library uses standard systemcall-based network I/O through Go's `net` package.

## Opening socket/listen

The network socket gets opened in:
- **File**: `listen.go`
- **Function**: `Listen()` (line 159)
- **Implementation**: Uses `net.ListenPacket()` to create a UDP socket, then casts to `*net.UDPConn`
- **Socket options**: Set via `ListenControl()` in `net.go` (SO_REUSEADDR, IP_TOS, IP_TTL)

## Network read

The network read occurs in:
- **File**: `listen.go` (line 225) and `dial.go` (line 145)
- **Function**: `ReadFrom()` on `*net.UDPConn`
- **Implementation**:
  - Listener: `ln.pc.ReadFrom(buffer)` in a goroutine loop (line 225)
  - Dialer: `pc.ReadFrom(buffer)` in a goroutine loop (line 145)
- **Buffer**: Pre-allocated buffer of size `config.MSS` (Maximum Segment Size)
- **Deadline**: Set to 3 seconds with `SetReadDeadline()`
- **Result**: Packets are parsed and queued to `rcvQueue` channel

## Network write

The network write occurs in:
- **File**: `listen.go` (line 444) and `dial.go` (line 275)
- **Function**:
  - Listener: `ln.pc.WriteTo(buffer, addr)` in `send()` method (line 444)
  - Dialer: `dl.pc.Write(buffer)` in `send()` method (line 275) - connected socket
- **Implementation**:
  - Packets are marshaled to bytes
  - Written synchronously with mutex protection (`sndMutex`)
  - Listener uses `WriteTo` for unconnected UDP (requires address)
  - Dialer uses `Write` for connected UDP socket

## Channel-based Queue System

GoSRT uses a channel-based architecture to decouple network I/O from packet processing. The system uses three main queues within each connection (`srtConn`):

1. **`networkQueue`** (chan packet.Packet, buffer size 1024): Receives packets from the network layer
2. **`writeQueue`** (chan packet.Packet, buffer size 1024): Receives packets from application writes
3. **`readQueue`** (chan packet.Packet, buffer size 1024): Delivers processed packets to application reads

Additionally, the listener/dialer level uses:
- **`rcvQueue`** (chan packet.Packet, buffer size 2048): Receives raw packets from the UDP socket before routing to connections
- **`backlog`** (chan packet.Packet, buffer size 128): Queues handshake packets for connection acceptance

### Blocking Channel Operations

The following locations contain **blocking** channel read/write operations that can cause goroutines to block waiting for channel data. These are identified as potential bottlenecks in performance profiling:

#### Connection-level Blocking Operations

1. **`connection.go:550`** - `ReadPacket()` method
   - **Operation**: `case p = <-c.readQueue:`
   - **Blocking**: Blocks waiting for packets to be delivered to the read queue
   - **Context**: Called by application `Read()` operations

2. **`connection.go:536`** - `ticker()` method
   - **Operation**: `case t := <-ticker.C:`
   - **Blocking**: Blocks waiting for ticker events (10ms intervals)
   - **Context**: Congestion control timing loop, runs per connection

3. **`connection.go:738`** - `networkQueueReader()` method
   - **Operation**: `case p := <-c.networkQueue:`
   - **Blocking**: Blocks waiting for packets from network layer
   - **Context**: Processes incoming packets for the connection
   - **Profile Impact**: 16.50% of blocking time (473.20s)

4. **`connection.go:756`** - `writeQueueReader()` method
   - **Operation**: `case p := <-c.writeQueue:`
   - **Blocking**: Blocks waiting for packets from application writes
   - **Context**: Processes outgoing packets for congestion control
   - **Profile Impact**: 16.68% of blocking time (478.29s)

#### Listener-level Blocking Operations

5. **`listen.go:346`** - `Accept2()` method
   - **Operation**: `case p := <-ln.backlog:`
   - **Blocking**: Blocks waiting for handshake packets in backlog queue
   - **Context**: Server connection acceptance loop
   - **Profile Impact**: Part of `Server.Serve()` blocking (8.58% total, 246.03s)

6. **`listen.go:465`** - `reader()` method
   - **Operation**: `case p := <-ln.rcvQueue:`
   - **Blocking**: Blocks waiting for packets from network receive queue
   - **Context**: Routes received packets to appropriate connections
   - **Profile Impact**: Part of `Server.Serve()` blocking

#### Dialer-level Blocking Operations

7. **`dial.go:273`** - `reader()` method
   - **Operation**: `case p := <-dl.rcvQueue:`
   - **Blocking**: Blocks waiting for packets from network receive queue
   - **Context**: Processes received packets for client connections

### Block Profile Analysis

Performance profiling shows that channel blocking is a significant bottleneck:

```
File: server-debug
Type: delay
Time: 2025-11-20 08:51:22 PST

(pprof) top -cum runtime.selectgo
Showing nodes accounting for 2620.68s, 91.37% of 2868.08s total

      flat  flat%   sum%        cum   cum%
  2620.68s 91.37% 91.37%   2620.68s 91.37%  runtime.selectgo
         0     0%  91.37%    478.29s 16.68%  github.com/datarhei/gosrt.(*srtConn).writeQueueReader
         0     0%  91.37%    478.29s 16.68%  github.com/datarhei/gosrt.newSRTConn.gowrap2
         0     0%  91.37%    478.02s 16.67%  github.com/datarhei/gosrt.(*Server).Serve.func1
         0     0%  91.37%    478.02s 16.67%  github.com/datarhei/gosrt.(*Server).Serve.gowrap1
         0     0%  91.37%    473.20s 16.50%  github.com/datarhei/gosrt.(*srtConn).networkQueueReader
         0     0%  91.37%    473.20s 16.50%  github.com/datarhei/gosrt.newSRTConn.gowrap1
         0     0%  91.37%    464.96s 16.21%  github.com/datarhei/gosrt.(*srtConn).ticker
         0     0%  91.37%    464.96s 16.21%  github.com/datarhei/gosrt.newSRTConn.gowrap3
         0     0%  91.37%    246.03s  8.58%  github.com/datarhei/gosrt.(*Server).ListenAndServe
```

**Key Findings:**
- **91.37%** of blocking time is spent in `runtime.selectgo` (channel select operations)
- **writeQueueReader**: 16.68% of blocking time (478.29s) - waiting for application writes
- **networkQueueReader**: 16.50% of blocking time (473.20s) - waiting for network packets
- **ticker**: 16.21% of blocking time (464.96s) - waiting for ticker events
- **Server.Serve**: 8.58% of blocking time (246.03s) - includes `Accept2()` blocking on backlog

**Implications for io_uring:**
- Channel blocking indicates that packet processing may be faster than network I/O
- io_uring's asynchronous I/O could reduce blocking by allowing more concurrent operations
- However, channel operations will still be necessary for packet routing and processing
- The bottleneck may shift from network I/O blocking to channel contention if io_uring increases throughput

### Network Receive Flow

Packets flow from the network socket through multiple stages of processing:

#### Current Implementation (with rcvQueue)

```
Network Socket (UDP)
    |
    | [ReadFrom() syscall - blocking]
    v
Listener/Dialer Goroutine
    | (listen.go:282-320, dial.go:170-211)
    | - Reads into buffer
    | - Parses packet with packet.NewPacketFromData()
    | - Non-blocking send to rcvQueue
    |
    v
rcvQueue Channel (2048 buffer)
    | (listen.go:265, dial.go:167)
    | - Buffers packets between network reader and router
    |
    v
Listener reader() Goroutine
    | (listen.go:454-512, dial.go:259-303)
    | - Blocks on rcvQueue (listen.go:465, dial.go:273)
    | - Routes packets to correct connection
    | - Looks up connection in ln.conns sync.Map using DestinationSocketId (listen.go:487)
    | - Validates destination socket ID and peer address (listen.go:499-505)
    | - Handles handshake packets (DestinationSocketId == 0) → backlog channel
    |
    v
conn.push() method
    | (connection.go:651-666)
    | - Non-blocking send to connection's networkQueue
    |
    v
networkQueue Channel (1024 buffer)
    | (connection.go:738)
    |
    v
networkQueueReader() Goroutine
    | (connection.go:728-743)
    | - Blocks on networkQueue (connection.go:738)
    | - Processes packets sequentially
    |
    v
handlePacket() method
    | (connection.go:785+)
    | - Handles control packets (ACK, NAK, etc.)
    | - For data packets: calls recv.Push()
    |
    v
Congestion Control Receiver
    | (congestion/live/receive.go)
    | - Reorders packets
    | - Detects losses
    | - Sends ACK/NAK
    | - OnTick(): calls OnDeliver callback
    |
    v
deliver() method
    | (connection.go:765-780)
    | - Non-blocking send
    |
    v
readQueue Channel (1024 buffer)
    | (connection.go:550)
    |
    v
Application Read
    | (connection.go:545-567, 569-590)
    | - ReadPacket() or Read()
```

**Key Points:**
- The `ReadFrom()` syscall blocks in a dedicated goroutine, allowing other operations to continue
- Packets are parsed immediately after reading from the socket
- The `rcvQueue` acts as a buffer between the socket reader and connection router
- **Connection Routing**: The listener maintains a `sync.Map` `conns` (listen.go:133) that maps destination socket IDs to connection objects. The `reader()` goroutine looks up the connection using `p.Header().DestinationSocketId` (listen.go:487) and calls `conn.push(p)` to route the packet to that connection's `networkQueue` channel
- Each connection has its own `networkQueue` channel, ensuring packets are processed sequentially per connection
- Congestion control handles reordering, loss detection, and flow control
- The `readQueue` buffers packets ready for application consumption

#### Potential Optimization: Eliminate rcvQueue

**Proposed Flow (direct routing):**

```
Network Socket (UDP)
    |
    | [ReadFrom() syscall - blocking]
    v
Listener/Dialer Goroutine
    | (listen.go:282-320, dial.go:170-211)
    | - Reads into buffer
    | - Parses packet with packet.NewPacketFromData()
    | - **Immediately routes packet:**
    |   - If DestinationSocketId == 0: send to backlog (handshake)
    |   - Else: lookup connection in sync.Map (listen.go:487)
    |   - Validate peer address (listen.go:499-505)
    |   - Call conn.push() directly
    |
    v
networkQueue Channel (1024 buffer) [per connection]
    | (connection.go:738)
    |
    v
[Rest of flow remains the same...]
```

**Benefits of eliminating rcvQueue:**
1. **Reduced latency**: One less channel hop and goroutine context switch
2. **Lower memory**: Eliminates 2048-buffer channel per listener/dialer
3. **Simpler code**: One less goroutine to manage
4. **Better for io_uring**: With async I/O, the completion handler can do routing directly without an intermediate queue

**Considerations:**
1. **Routing performance**: The routing logic is fast:
   - `sync.Map.Load()` is O(1) average case (read-heavy workload, already optimized)
   - String comparison for peer validation is fast
   - `conn.push()` is non-blocking (won't block the network reader)
2. **Blocking risk**: If routing logic becomes slow, it could delay the network reader. However:
   - Current routing logic is simple and fast
   - `conn.push()` uses non-blocking channel send (select with default)
   - Even if a connection's `networkQueue` is full, other connections aren't affected
3. **Handshake packets**: Special handling for `DestinationSocketId == 0` packets (send to `backlog`) can be done in the network reader goroutine
4. **Error handling**: Parse errors already handled in network reader; routing errors (unknown destination) can be handled there too

**Implementation for io_uring:**
With io_uring, this optimization becomes even more attractive:
- The receive completion handler runs in a separate goroutine (not blocking on syscall)
- It can immediately do the routing without needing an intermediate queue
- This reduces latency and eliminates the `rcvQueue` entirely for the io_uring path

**Recommendation:**
This optimization is viable and recommended, especially for the io_uring implementation. The routing logic is fast enough that it won't block the network reader, and eliminating the intermediate queue reduces latency and memory usage.

### Network Transmit Flow

Packets flow from application writes through congestion control to the network:

```
Application Write
    | (connection.go:608-648)
    | - Write() or WritePacket()
    | - Creates packet with timestamp (PktTsbpdTime)
    |
    v
writeQueue Channel (1024 buffer) [PER CONNECTION]
    | (connection.go:413, 632)
    | - Non-blocking send (select with default)
    | - **If full: returns io.EOF, packet is dropped** (connection.go:634-637)
    | - Each connection has its own writeQueue
    |
    v
writeQueueReader() Goroutine [PER CONNECTION]
    | (connection.go:747-762)
    | - Blocks on writeQueue (connection.go:756)
    | - Processes packets sequentially per connection
    | - Calls c.snd.Push(p) to add to congestion control
    |
    v
Congestion Control Sender
    | (congestion/live/send.go)
    | - snd.Push(): Assigns sequence numbers, adds to packetList
    | - OnTick() (every 10ms):
    |   * Checks packets where PktTsbpdTime <= now
    |   * Calls OnDeliver callback (c.pop) for ready packets
    |   * Rate limiting is **time-based**, not bandwidth-based:
    |     - Packets are scheduled by their PktTsbpdTime timestamp
    |     - Rate determined by packet send period (pktSndPeriod)
    |     - pktSndPeriod = (avgPayloadSize + 16) * 1_000_000 / maxBW (microseconds)
    |     - Default maxBW = 128 MB/s (1 Gbit/s)
    |   * Drops packets that are too old (PktTsbpdTime + dropThreshold <= now)
    |   * Statistics tracked: PktDrop, PktLoss, estimatedInputBW, estimatedSentBW
    |
    v
pop() method
    | (connection.go:681-726)
    | - Sets destination address/socket ID
    | - Encrypts packet if needed
    | - Calls onSend() callback (no channels used here)
    |
    v
onSend() callback
    | (connection.go:725)
    | - Set to listener.send() or dialer.send()
    | - Direct function call, no channels
    |
    v
Listener/Dialer send() method
    | (listen.go:514-557, dial.go:307-336)
    | - **Mutex protects sndData buffer during marshaling** (listen.go:522, dial.go:311)
    | - Mutex is NOT protecting the socket write itself
    | - Multiple connections share the same sndData buffer for marshaling
    | - Without mutex: concurrent marshaling would corrupt the shared buffer
    | - Marshals packet to bytes using shared sndData buffer
    |
    v
Network Socket (UDP)
    | [WriteTo() or Write() syscall - blocking]
    | (listen.go:551, dial.go:328)
    | - Kernel handles socket-level concurrency
    | - Mutex is released before syscall, so it doesn't block socket writes
    |
    v
Network Wire
```

**Key Points:**

1. **writeQueue is per connection**: Each `srtConn` has its own `writeQueue` channel (connection.go:413), ensuring packets from different connections don't interfere with each other.

2. **Non-blocking send with packet drops**: When `writeQueue` is full, the application write returns `io.EOF` and the packet is dropped (connection.go:634-637). This prevents blocking but can cause data loss if the application writes faster than packets can be processed. The drop is tracked via Prometheus metrics (`connectionChannelBlockedCount`).

3. **Rate limiting mechanism**:
   - **Time-based scheduling**: Packets are sent when their `PktTsbpdTime` timestamp arrives, not based on explicit bandwidth limits
   - **Packet send period**: Calculated as `(avgPayloadSize + 16) * 1_000_000 / maxBW` microseconds
   - **Default rate**: 128 MB/s (1 Gbit/s) maximum bandwidth
   - **Rate determination**: The `pktSndPeriod` determines the minimum time between packets, effectively limiting the send rate
   - **Drop detection**: Packets older than `PktTsbpdTime + dropThreshold` are dropped and tracked in statistics (`PktDrop`, `PktLoss`)
   - **Statistics**: Congestion control tracks `estimatedInputBW`, `estimatedSentBW`, and `pktLossRate` for monitoring

4. **No channels after congestion control**: After `OnDeliver` callback (which calls `pop()`), there are no more channels. The `pop()` method directly calls `onSend()` callback, which directly calls `listener.send()` or `dialer.send()`. This is a direct function call chain, not channel-based.

5. **Mutex purpose**: The `sndMutex` (listen.go:193, dial.go:76) protects the **shared `sndData` buffer** during marshaling, not the socket write itself. This is necessary because:
   - Multiple connections (goroutines) can call `send()` concurrently
   - All connections on the same listener/dialer share the same `sndData` buffer
   - Without the mutex, concurrent marshaling would corrupt the buffer
   - The mutex is released **before** the syscall, so it doesn't block socket writes
   - The kernel handles socket-level concurrency internally
   - **For io_uring**: This mutex can be removed since each send operation will use its own pooled buffer, eliminating the shared buffer issue

6. **Examining `send()` function and `Decommission()` flow**:

   The `send()` function comment states: *"This function must be synchronous in order to allow to safely call Packet.Decommission() afterward."* (listen.go:514, dial.go:305)

   **Understanding `Decommission()`**:
   - `Decommission()` returns the packet's payload buffer to `payloadPool` (packet/packet.go:309-316)
   - It sets `p.payload = nil` to prevent reuse
   - Once decommissioned, the packet should not be used

   **Why "synchronous" is required**:
   - **Control packets**: Decommissioned immediately after `WriteTo()`/`Write()` completes (listen.go:545, dial.go:334)
     - Control packets are never retransmitted, so safe to decommission immediately
   - **Data packets**: NOT decommissioned in `send()` - they remain in congestion control for potential retransmission
     - Data packets are decommissioned later when ACK'd or dropped (congestion/live/send.go:257, 311)

   **The mutex does NOT protect `Decommission()`**:
   - The mutex only protects the shared `sndData` buffer during marshaling
   - `Decommission()` is called on the packet object, which is per-packet and thread-safe
   - The "synchronous" requirement ensures the syscall completes before decommissioning, so the packet data remains valid during the write
   - With blocking syscalls, this is naturally synchronous - the function doesn't return until the write completes

   **For async io_uring**:
   - The send becomes asynchronous, so we can't decommission immediately
   - `Decommission()` must be moved to the completion handler
   - The packet and buffer must be kept alive until the completion handler runs
   - This is why the io_uring design keeps the packet and buffer in the completion info

7. **sync.Pool optimization analysis**:

   **Current approach (shared buffer)**:
   ```go
   // Single shared buffer per listener/dialer
   sndData bytes.Buffer  // shared across all connections
   sndMutex sync.Mutex   // protects sndData

   func (ln *listener) send(p packet.Packet) {
       ln.sndMutex.Lock()           // Serialize access to shared buffer
       defer ln.sndMutex.Unlock()
       ln.sndData.Reset()
       p.Marshal(&ln.sndData)       // Marshal into shared buffer
       buffer := ln.sndData.Bytes() // Get slice
       ln.pc.WriteTo(buffer, ...)   // Write (blocking)
       // Mutex released here, but write already completed
   }
   ```

   **Proposed sync.Pool approach**:
   ```go
   // Pool of buffers shared across all connections
   var sendBufferPool = &sync.Pool{
       New: func() interface{} {
           return new(bytes.Buffer)
       },
   }

   func (ln *listener) send(p packet.Packet) {
       // Get buffer from pool (no lock needed - sync.Pool is thread-safe)
       sendBuffer := sendBufferPool.Get().(*bytes.Buffer)
       defer func() {
           sendBuffer.Reset()
           sendBufferPool.Put(sendBuffer)  // Return to pool
       }()

       // Marshal into pooled buffer (no lock needed - each send() has its own buffer)
       if err := p.Marshal(sendBuffer); err != nil {
           p.Decommission()
           return
       }

       buffer := sendBuffer.Bytes()  // Get slice
       ln.pc.WriteTo(buffer, ...)    // Write (blocking)
       // Buffer stays alive until defer executes
   }
   ```

   **Benefits of sync.Pool approach**:
   - **No mutex needed**: Each `send()` call gets its own buffer from the pool
   - **Concurrent sends**: Multiple connections can send simultaneously without blocking
   - **Buffer reuse**: Buffers are recycled, reducing allocations
   - **Better performance**: Eliminates mutex contention on the hot path

   **Considerations**:
   - **Buffer lifetime**: The buffer must stay alive until the write completes
     - With blocking syscalls: defer ensures buffer stays alive until function returns
     - With async io_uring: buffer must be kept in completion info until completion handler runs
   - **Pool sizing**: The pool will automatically grow/shrink based on demand
   - **Memory usage**: More buffers in use concurrently, but they're recycled

   **Answer: Yes, the lock can be removed with sync.Pool**:
   - Each `send()` gets its own buffer from the pool
   - No shared state to protect
   - `sync.Pool.Get()` and `Put()` are already thread-safe
   - The only requirement is keeping the buffer alive until the write completes (or completion handler runs for io_uring)

   **This is exactly what the io_uring design does**: Uses `payloadPool` (which is a sync.Pool) for send buffers, eliminating the need for `sndMutex`.

### Connection Routing Details

The listener uses a map-based routing system to direct packets to the correct connection:

- **Map Structure**: `ln.conns map[uint32]*srtConn` (listen.go:126) - maps destination socket ID (uint32) to connection object pointer
- **Routing Logic**:
  1. Packet arrives with `DestinationSocketId` in header (line 393)
  2. If `DestinationSocketId == 0`, packet is a handshake and goes to `backlog` channel (line 396)
  3. Otherwise, connection is looked up: `conn, ok := ln.conns[p.Header().DestinationSocketId]` (line 405)
  4. If connection exists, packet is routed via `conn.push(p)` (line 421)
  5. `push()` sends packet to that connection's `networkQueue` channel (connection.go:522)
- **Security**: Before routing, the listener validates the peer address matches the connection's expected remote address (unless `AllowPeerIpChange` is enabled) (line 413-418)
- **Thread Safety**: Map access is protected with `sync.RWMutex` (read lock for lookups, write lock for additions/removals)

**Note**: It's not a map of connection IDs to channels directly, but rather a map of socket IDs to connection objects, where each connection object contains its own `networkQueue` channel. This design allows each connection to have its own independent processing pipeline.

#### Performance Optimization: Using sync.Map

The current implementation uses a standard Go map with `sync.RWMutex` for thread safety. However, given that connection routing is a **read-heavy workload** (packets are routed much more frequently than connections are added/removed), `sync.Map` could provide better performance.

**Current Implementation:**
- Map: `conns map[uint32]*srtConn`
- Lock: `sync.RWMutex` with `RLock()` for reads, `Lock()` for writes
- Read pattern: `ln.lock.RLock(); conn, ok := ln.conns[socketId]; ln.lock.RUnlock()`
- Write pattern: `ln.lock.Lock(); ln.conns[socketId] = conn; ln.lock.Unlock()`

**Changes Required for sync.Map:**

1. **Type Declaration** (listen.go:126):
   ```go
   // Change from:
   conns map[uint32]*srtConn

   // To:
   conns sync.Map  // key: uint32, value: *srtConn
   ```

2. **Initialization** (listen.go:194):
   ```go
   // Change from:
   ln.conns = make(map[uint32]*srtConn)

   // To:
   // sync.Map zero value is ready to use, no initialization needed
   // (or can be removed from initialization)
   ```

3. **Read Operations** (listen.go:404-406):
   ```go
   // Change from:
   ln.lock.RLock()
   conn, ok := ln.conns[p.Header().DestinationSocketId]
   ln.lock.RUnlock()

   // To:
   val, ok := ln.conns.Load(p.Header().DestinationSocketId)
   var conn *srtConn
   if ok {
       conn = val.(*srtConn)
   }
   ```

4. **Write Operations** (conn_request.go:282, 474):
   ```go
   // Change from:
   ln.lock.Lock()
   ln.conns[socketId] = conn
   ln.lock.Unlock()

   // To:
   ln.conns.Store(socketId, conn)
   ```

5. **Delete Operations** (listen.go:331):
   ```go
   // Change from:
   ln.lock.Lock()
   delete(ln.conns, socketId)
   ln.lock.Unlock()

   // To:
   ln.conns.Delete(socketId)
   ```

6. **Iteration** (listen.go:348-355):
   ```go
   // Change from:
   ln.lock.RLock()
   for _, conn := range ln.conns {
       if conn == nil {
           continue
       }
       conn.close()
   }
   ln.lock.RUnlock()

   // To:
   ln.conns.Range(func(key, value interface{}) bool {
       conn := value.(*srtConn)
       if conn == nil {
           return true  // continue iteration
       }
       conn.close()
       return true  // continue iteration
   })
   ```

7. **Existence Check** (conn_request.go:383):
   ```go
   // Change from:
   if _, found := req.ln.conns[socketId]; !found {
       return socketId, nil
   }

   // To:
   if _, found := req.ln.conns.Load(socketId); !found {
       return socketId, nil
   }
   ```

8. **Remove RWMutex** (listen.go:127):
   - The `lock sync.RWMutex` field can be removed if it's only used for `conns` map access
   - Note: Check if `lock` is used for other purposes (e.g., `connReqs` map, `doneErr`)

**Performance Benefits:**
- **Read-heavy workloads**: `sync.Map` is optimized for cases where entries are written once and read many times, which matches the connection routing pattern
- **Reduced lock contention**: No need for read locks on the hot path (packet routing)
- **Better scalability**: Multiple goroutines can read concurrently without blocking each other

**Considerations:**
- **Type assertions**: `sync.Map` uses `interface{}` for keys and values, requiring type assertions
- **Memory overhead**: `sync.Map` may use slightly more memory than a standard map
- **Write performance**: Writes may be slightly slower than a standard map with a write lock, but this is acceptable since writes are infrequent
- **Other map usage**: The `connReqs` map would need similar changes if it's also read-heavy, or could remain as a standard map if write-heavy

**Testing:**
- Benchmark the routing hot path to measure actual performance improvement
- Verify thread safety under high concurrency
- Ensure no race conditions in connection lifecycle (add/remove)

### Queue Interface with Syscalls

The syscalls interface with channels as follows:

**Receive Side:**
- **Syscall**: `ReadFrom()` blocks in a goroutine, reading raw UDP datagrams
- **Channel Write**: After parsing, packets are sent to `rcvQueue` (non-blocking with default case for overflow)
- **Channel Read**: Listener's `reader()` goroutine reads from `rcvQueue` and routes to connection's `networkQueue`
- **Processing**: Connection's `networkQueueReader()` processes packets and eventually delivers to `readQueue`
- **Application**: Application reads from `readQueue` via `ReadPacket()` or `Read()`

**Note**: For io_uring implementation, we can optimize by eliminating `rcvQueue` and routing directly from the completion handler (see "Potential Optimization: Eliminate rcvQueue" section above).

**Transmit Side:**
- **Application**: Application writes to `writeQueue` (non-blocking)
- **Channel Read**: `writeQueueReader()` reads from `writeQueue` and feeds congestion control
- **Channel Write**: Congestion control eventually calls `pop()` which triggers `onSend()` callback
- **Syscall**: The callback performs synchronous `WriteTo()` or `Write()` syscall with mutex protection

This architecture provides:
- **Decoupling**: Network I/O is separated from packet processing
- **Buffering**: Channels provide natural backpressure and buffering
- **Concurrency**: Multiple goroutines can process different stages simultaneously
- **Non-blocking**: Application writes don't block on network I/O
- **Ordering**: Sequential processing within each connection ensures packet order


# Conversion to IO_Uring

For each of the above described network operations, these are mapped to the IO_uring versions.

Potentially not all the syscalls need to be converted to IO_Uring to benefit.  For example, the goSRT server does not need to handle very many sockets or new socket setups per second, so potentially the initial implementation can focus on the packet read and writes.

## Design Goals

The io_uring integration should:
1. **Minimize changes** to existing GoSRT codebase
2. **Maintain compatibility** with existing channel-based architecture
3. **Preserve packet ordering** and processing logic
4. **Handle errors gracefully** with proper cleanup
5. **Support both listener and dialer** patterns
6. **Use sync.Pool for all buffers**: All buffers required for io_uring operations MUST come from `sync.Pool` to reduce memory pressure. This is a hard requirement.
   - **Prefer existing pools**: Use existing `sync.Pool` instances from `packet.go` where possible
   - **For sends**: Use existing `payloadPool` from `packet/packet.go` (returns `*bytes.Buffer`)
     - Marshal directly into the pooled `bytes.Buffer`
     - Use `bytes.Buffer.Bytes()` to get the underlying slice (see [Go docs](https://pkg.go.dev/bytes#Buffer.Bytes))
     - Keep the `bytes.Buffer` alive in completion info until send completes
     - **No copy needed!** The slice from `Bytes()` is valid as long as the buffer isn't modified
   - **For receives**: Need a separate `[]byte` pool (following same pattern structure)
     - `iouring.Recvfrom()` requires a `[]byte` that it can write into directly
     - `bytes.Buffer.Bytes()` returns a slice of existing data, not a writable slice for receiving
     - Using `[]byte` pool is more efficient - data goes directly from kernel to buffer, then to packet
   - **Follow existing patterns**: If new pools are needed, follow the same coding pattern as `payloadPool` in `packet/packet.go`:
     ```go
     type pool struct {
         pool sync.Pool
     }

     func newPool() *pool {
         return &pool{
             pool: sync.Pool{
                 New: func() interface{} {
                     return new(bytes.Buffer)  // or make([]byte, config.MSS) for receives
                 },
             },
         }
     }

     func (p *pool) Get() *bytes.Buffer {  // or []byte for receives
         b := p.pool.Get().(*bytes.Buffer)
         b.Reset()  // only for bytes.Buffer
         return b
     }

     func (p *pool) Put(b *bytes.Buffer) {  // or []byte for receives
         p.pool.Put(b)
     }
     ```
   - **Buffer lifecycle**: Buffers must be returned to the pool after use (in completion handlers)
   - **No allocations**: Avoid allocations in the hot path by reusing pooled buffers

## Implementation Approaches

Two implementation approaches are considered, one for each library:

### Approach 1: iouring-go (Channel-Based Integration)

**Philosophy**: Leverage iouring-go's channel-based API to integrate seamlessly with GoSRT's existing channel architecture.

#### Architecture Overview

The iouring-go approach introduces a completion handler goroutine that processes io_uring completions and feeds them into the existing channel system. This minimizes changes to the rest of the codebase.

#### Receive Path Design

**Current Flow:**
```
ReadFrom() syscall (blocking) → parse → rcvQueue channel → reader() goroutine → route to connection's networkQueue
```

**io_uring Flow (Optimized - Direct Routing):**
```
Submit Recvfrom() → io_uring completion → completion handler → parse → route directly to connection's networkQueue
```

**Note**: The io_uring implementation will eliminate the `rcvQueue` intermediate step. The completion handler will:
1. Parse the packet
2. Look up the connection in `sync.Map` using `DestinationSocketId`
3. Route directly to the connection's `networkQueue` (or `backlog` for handshake packets)
This reduces latency by eliminating one channel hop and one goroutine context switch.

**Implementation Details:**

1. **Buffer Management**:
   - Pre-allocate a pool of buffers (e.g., 64-128 buffers of `config.MSS` size)
   - Each buffer is associated with a pending io_uring request
   - Buffers are recycled after packet parsing

2. **IO_Uring Setup** (in `Listen()` or `Dial()`):
   ```go
   iour, err := iouring.New(256) // ring size
   if err != nil {
       return nil, err
   }
   defer iour.Close()

   completionChan := make(chan iouring.Result, 256)
   ```

3. **Receive Loop Replacement** (replaces the ReadFrom goroutine):
   ```go
   // Buffer pool for receive operations
   // Note: iouring.Recvfrom requires []byte to write into directly
   // bytes.Buffer.Bytes() returns a read-only slice of existing data,
   // so we need a []byte pool for receives (but follow same pattern structure)
   type recvBufferPool struct {
       pool sync.Pool
   }

   var recvPool = &recvBufferPool{
       pool: sync.Pool{
           New: func() interface{} {
               return make([]byte, config.MSS)
           },
       },
   }

   func (p *recvBufferPool) Get() []byte {
       return p.pool.Get().([]byte)
   }

   func (p *recvBufferPool) Put(b []byte) {
       p.pool.Put(b)
   }

   // Submit multiple recv requests to keep the ring busy
   // This runs once at startup to pre-populate the ring with pending receives
   for i := 0; i < 64; i++ {
       buffer := recvPool.Get()
       prep := iouring.Recvfrom(fd, buffer, 0).WithInfo(buffer)
       iour.SubmitRequest(prep, completionChan)
   }

   // Process completions
   // This loop maintains a constant number of pending receives in the ring
   go func() {
       for {
           select {
           case <-ctx.Done():
               return
           case result := <-completionChan:
               if result.Err() != nil {
                   // Handle error (timeout, shutdown, etc.)
                   // Still need to recycle buffer and resubmit
                   buffer := result.GetRequestInfo().([]byte)
                   recvPool.Put(buffer)
                   buffer = recvPool.Get()
                   prep := iouring.Recvfrom(fd, buffer, 0).WithInfo(buffer)
                   iour.SubmitRequest(prep, completionChan)
                   continue
               }

               buffer := result.GetRequestInfo().([]byte)
               n := result.ReturnValue0().(int)
               addr := result.ReturnValue1().(net.Addr)

               // Parse packet
               // Note: NewPacketFromData will copy the data into its own buffer
               p, err := packet.NewPacketFromData(addr, buffer[:n])

               // Return buffer to pool immediately after copying
               recvPool.Put(buffer)

               if err != nil {
                   continue
               }

               // Route packet directly to connection (eliminating rcvQueue)
               // This is the optimization: do routing in completion handler instead of separate goroutine
               if p.Header().DestinationSocketId == 0 {
                   // Handshake packet - send to backlog
                   if p.Header().IsControlPacket && p.Header().ControlType == packet.CTRLTYPE_HANDSHAKE {
                       select {
                       case ln.backlog <- p:
                       default:
                           // backlog full, drop packet
                       }
                   }
                   continue
               }

               // Look up connection and route directly
               val, ok := ln.conns.Load(p.Header().DestinationSocketId)
               if !ok {
                   // Unknown destination, drop packet
                   continue
               }

               conn := val.(*srtConn)
               if conn == nil {
                   continue
               }

               // Validate peer address if configured
               if !ln.config.AllowPeerIpChange {
                   if p.Header().Addr.String() != conn.RemoteAddr().String() {
                       // Peer IP mismatch, drop packet
                       continue
                   }
               }

               // Route directly to connection's networkQueue (non-blocking)
               conn.push(p)

               // Submit new recv to maintain constant pending count
               // This ensures there are always ~64 pending receives in the ring
               buffer = recvPool.Get()
               prep := iouring.Recvfrom(fd, buffer, 0).WithInfo(buffer)
               iour.SubmitRequest(prep, completionChan)
           }
       }
   }()
   ```

   **Why []byte pool for receives:**
   - `iouring.Recvfrom()` needs a `[]byte` that it can write into directly
   - `bytes.Buffer.Bytes()` returns a slice of existing data, not a writable slice for receiving
   - We could receive into `[]byte`, then write into a `bytes.Buffer`, but that adds an extra copy
   - Using `[]byte` pool for receives is more efficient - data goes directly from kernel to our buffer, then to packet

   **Key Points:**
   - The initial loop (64 iterations) runs **once** at startup to pre-populate the ring
   - After that, each completion handler immediately submits a new receive request
   - This maintains a **constant number of pending receives** (typically 64) in the ring at all times
   - The ring stays "busy" with pending operations, maximizing throughput
   - Buffers are recycled immediately after data is copied into the packet

4. **Changes Required**:
   - Replace the `ReadFrom()` goroutine with io_uring submission/completion loop
   - Add buffer pool management
   - Handle timeouts via io_uring timeout requests instead of `SetReadDeadline()`
   - **Eliminate `rcvQueue` and `reader()` goroutine**: Route packets directly from completion handler to connection's `networkQueue`
   - This optimization reduces latency by eliminating one channel hop and one goroutine context switch

#### Transmit Path Design

**Current Flow:**
```
send() method → marshal → WriteTo()/Write() syscall (blocking, mutex-protected)
```

**io_uring Flow:**
```
send() method → marshal → submit Sendto()/Send() → io_uring completion → packet cleanup
```

**Implementation Details:**

1. **Send Method Replacement** (using existing payloadPool - NO COPY NEEDED):
   ```go
   // Use existing payloadPool from packet.go - no new pool needed!
   // Import: "github.com/datarhei/gosrt/packet"

   func (ln *listener) send(p packet.Packet) {
       // Get buffer from existing payloadPool (bytes.Buffer)
       sendBuffer := packet.GetSendBuffer() // Helper to get from payloadPool
       // Or directly: sendBuffer := payloadPool.Get()

       // Marshal directly into the pooled buffer
       if err := p.Marshal(sendBuffer); err != nil {
           payloadPool.Put(sendBuffer) // Return buffer on error
           p.Decommission()
           return
       }

       // Get the underlying slice - this is valid as long as buffer isn't modified
       // We'll keep the buffer alive in the completion handler
       bufferSlice := sendBuffer.Bytes()

       // Submit async send with buffer and packet info
       // The completion handler will return the buffer to the pool
       prep := iouring.Sendto(fd, bufferSlice, p.Header().Addr, 0).
           WithInfo(sendInfo{packet: p, buffer: sendBuffer})
       _, err := ln.iour.SubmitRequest(prep, ln.sendCompletionChan)
       if err != nil {
           payloadPool.Put(sendBuffer) // Return buffer on error
           p.Decommission()
           return
       }

       // Note: p.Decommission() and buffer return will be called in completion handler
   }

   type sendInfo struct {
       packet packet.Packet
       buffer *bytes.Buffer  // Keep the bytes.Buffer alive until send completes
   }
   ```

   **Key Insight:**
   - `bytes.Buffer.Bytes()` returns the underlying slice (see [Go docs](https://pkg.go.dev/bytes#Buffer.Bytes))
   - The slice is valid as long as the buffer isn't modified
   - We keep the `bytes.Buffer` in the completion info, so it stays alive until the send completes
   - **No copy needed!** We use the slice directly from `Bytes()`
   - The existing `payloadPool` from `packet.go` can be reused for sends

   **Note:** We may need to add a helper function or export `payloadPool` from the packet package, or create a similar pool specifically for send operations following the same pattern.

2. **Send Completion Handler**:
   ```go
   go func() {
       for result := range ln.sendCompletionChan {
           info := result.GetRequestInfo().(sendInfo)
           p := info.packet
           buffer := info.buffer  // This is the *bytes.Buffer from payloadPool

           // Return buffer to pool (payloadPool from packet.go)
           payloadPool.Put(buffer)

           if result.Err() != nil {
               // Handle send error
               // Packet may need to be retransmitted (handled by congestion control)
           }

           // Decommission packet after send completes
           if p.Header().IsControlPacket {
               p.Decommission()
           }
       }
   }()
   ```

3. **Changes Required**:
   - Replace `WriteTo()`/`Write()` with `iouring.Sendto()`/`iouring.Send()`
   - Remove mutex protection (io_uring handles concurrency)
   - Move packet decommissioning to completion handler
   - Handle send errors asynchronously

#### Advantages of iouring-go Approach

- **Minimal code changes**: Channel-based API fits existing architecture
- **Easy integration**: Completion handling via channels matches GoSRT patterns
- **Error handling**: Errors come through channels, easy to integrate
- **Buffer management**: Can use existing buffer patterns or simple pool

#### Disadvantages of iouring-go Approach

- **Additional channels**: Introduces completion channels (but fits Go idiom)
- **Buffer copying**: May need to copy buffers for async sends
- **Less control**: Higher-level API provides less fine-grained control

---

### Approach 2: giouring (Low-Level Integration)

**Philosophy**: Use giouring's low-level API for maximum control and performance, managing the io_uring ring directly.

#### Architecture Overview

The giouring approach requires manual management of the submission and completion queues, providing more control but requiring more code changes.

#### Receive Path Design

**Current Flow:**
```
ReadFrom() syscall (blocking) → parse → rcvQueue channel → reader() goroutine → route to connection's networkQueue
```

**io_uring Flow (Optimized - Direct Routing):**
```
Submit RecvMsg() → ring.Submit() → ring.WaitCQE() → parse → route directly to connection's networkQueue
```

**Note**: The io_uring implementation will eliminate the `rcvQueue` intermediate step. The completion handler will route packets directly to connections, reducing latency.

**Implementation Details:**

1. **IO_Uring Setup**:
   ```go
   ring := giouring.NewRing()
   err := ring.QueueInit(256, 0) // ring size, flags
   if err != nil {
       return nil, err
   }
   defer ring.QueueExit()
   ```

2. **Buffer Management**:
   - Use fixed buffers or provide buffers per request
   - For UDP, use `PrepareRecvMsg` with `syscall.Msghdr` to get source address
   - Manage buffer lifecycle manually

3. **Receive Loop Replacement**:
   ```go
   // Buffer pool for receive operations
   // Note: giouring PrepareRecvMsg requires []byte, so we need a []byte pool
   // Following the same pattern structure as bytes.Buffer pools
   type recvBufferPool struct {
       pool sync.Pool
   }

   var recvPool = &recvBufferPool{
       pool: sync.Pool{
           New: func() interface{} {
               return make([]byte, config.MSS)
           },
       },
   }

   func (p *recvBufferPool) Get() []byte {
       return p.pool.Get().([]byte)
   }

   func (p *recvBufferPool) Put(b []byte) {
       p.pool.Put(b)
   }

   // Track buffers and msghdr structures for each pending receive
   type recvContext struct {
       buffer []byte
       msg    syscall.Msghdr
       rsa    syscall.RawSockaddrAny
       iovec  syscall.Iovec
   }

   // Pre-submit multiple recv requests (runs once at startup)
   recvContexts := make([]*recvContext, 64)
   for i := range recvContexts {
       ctx := &recvContext{
           buffer: recvPool.Get(),
       }
       recvContexts[i] = ctx

       ctx.iovec.Base = &ctx.buffer[0]
       ctx.iovec.SetLen(len(ctx.buffer))

       ctx.msg.Name = (*byte)(unsafe.Pointer(&ctx.rsa))
       ctx.msg.Namelen = uint32(syscall.SizeofSockaddrAny)
       ctx.msg.Iov = &ctx.iovec
       ctx.msg.Iovlen = 1

       sqe := ring.GetSQE()
       sqe.PrepareRecvMsg(fd, &ctx.msg, 0)
       sqe.SetData(uint64(i)) // store context index

       ring.Submit()
   }

   // Process completions (maintains constant pending receives)
   go func() {
       for {
           cqe, err := ring.WaitCQE()
           if err != nil {
               // Handle error, but still need to resubmit
               ctxIdx := int(cqe.UserData)
               ctx := recvContexts[ctxIdx]

               // Re-submit recv for this buffer
               sqe := ring.GetSQE()
               sqe.PrepareRecvMsg(fd, &ctx.msg, 0)
               sqe.SetData(uint64(ctxIdx))
               ring.Submit()
               ring.CQESeen(cqe)
               continue
           }

           ctxIdx := int(cqe.UserData)
           ctx := recvContexts[ctxIdx]
           buffer := ctx.buffer

           // Extract address from msghdr
           addr := extractAddrFromRSA(&ctx.rsa)

           // Parse and queue (NewPacketFromData copies the data)
           p, err := packet.NewPacketFromData(addr, buffer[:cqe.Res])

           // Note: Buffer is reused in the same context, no need to return to pool
           // The buffer stays with the recvContext for the lifetime of the listener

           if err != nil {
               continue
           }

           // Route packet directly to connection (eliminating rcvQueue)
           // This is the optimization: do routing in completion handler instead of separate goroutine
           if p.Header().DestinationSocketId == 0 {
               // Handshake packet - send to backlog
               if p.Header().IsControlPacket && p.Header().ControlType == packet.CTRLTYPE_HANDSHAKE {
                   select {
                   case ln.backlog <- p:
                   default:
                       // backlog full, drop packet
                   }
               }
               continue
           }

           // Look up connection and route directly
           val, ok := ln.conns.Load(p.Header().DestinationSocketId)
           if !ok {
               // Unknown destination, drop packet
               continue
           }

           conn := val.(*srtConn)
           if conn == nil {
               continue
           }

           // Validate peer address if configured
           if !ln.config.AllowPeerIpChange {
               if p.Header().Addr.String() != conn.RemoteAddr().String() {
                   // Peer IP mismatch, drop packet
                   continue
               }
           }

           // Route directly to connection's networkQueue (non-blocking)
           conn.push(p)

           // Re-submit recv for this buffer to maintain constant pending count
           sqe := ring.GetSQE()
           sqe.PrepareRecvMsg(fd, &ctx.msg, 0)
           sqe.SetData(uint64(ctxIdx))
           ring.Submit()

           ring.CQESeen(cqe)
       }
   }()
   ```

   **Key Points:**
   - The initial loop (64 iterations) runs **once** at startup to pre-populate the ring
   - Each completion immediately submits a new receive request using the same buffer/context
   - This maintains a **constant number of pending receives** (64) in the ring
   - Buffers are allocated from pool once and reused for the lifetime of the listener
   - No buffer copying needed - data is copied into packet during `NewPacketFromData()`

4. **Changes Required**:
   - Replace `ReadFrom()` with manual SQE preparation and submission
   - Manage `syscall.Msghdr` structures for UDP address handling
   - Handle buffer lifecycle and reuse
   - Manual completion queue processing

#### Transmit Path Design

**Current Flow:**
```
send() method → marshal → WriteTo()/Write() syscall (blocking, mutex-protected)
```

**io_uring Flow:**
```
send() method → marshal → PrepareSendMsg() → ring.Submit() → completion → cleanup
```

**Implementation Details:**

1. **Send Method Replacement** (using existing payloadPool - NO COPY NEEDED):
   ```go
   // Use existing payloadPool from packet.go - no new pool needed!
   // Import: "github.com/datarhei/gosrt/packet"

   // Context to track send operations
   type sendContext struct {
       packet packet.Packet
       buffer *bytes.Buffer  // Keep bytes.Buffer alive until send completes
       msg    syscall.Msghdr
       iovec  syscall.Iovec
   }

   func (ln *listener) send(p packet.Packet) {
       // Get buffer from existing payloadPool (bytes.Buffer)
       sendBuffer := payloadPool.Get() // From packet package

       // Marshal directly into the pooled buffer
       if err := p.Marshal(sendBuffer); err != nil {
           payloadPool.Put(sendBuffer) // Return buffer on error
           p.Decommission()
           return
       }

       // Get the underlying slice - valid as long as buffer isn't modified
       // We keep the buffer in sendContext so it stays alive
       bufferSlice := sendBuffer.Bytes()

       // Prepare sendmsg for UDP with address
       var iovec syscall.Iovec
       iovec.Base = &bufferSlice[0]
       iovec.SetLen(len(bufferSlice))

       var msg syscall.Msghdr
       addrPtr, addrLen := sockaddrToPtr(p.Header().Addr)
       msg.Name = (*byte)(addrPtr)
       msg.Namelen = addrLen
       msg.Iov = &iovec
       msg.Iovlen = 1

       // Create context to track buffer and packet
       ctx := &sendContext{
           packet: p,
           buffer: sendBuffer,  // Keep bytes.Buffer alive
           msg:    msg,
           iovec:  iovec,
       }

       sqe := ln.ring.GetSQE()
       if sqe == nil {
           // Ring full, need to wait or handle
           payloadPool.Put(sendBuffer)
           p.Decommission()
           return
       }

       sqe.PrepareSendMsg(fd, &msg, 0)
       sqe.SetData(uint64(uintptr(unsafe.Pointer(ctx)))) // Store context pointer

       ln.ring.Submit()

       // Note: Completion handling in separate goroutine
   }
   ```

   **Key Insight:**
   - `bytes.Buffer.Bytes()` returns the underlying slice (see [Go docs](https://pkg.go.dev/bytes#Buffer.Bytes))
   - The slice is valid as long as the buffer isn't modified
   - We keep the `bytes.Buffer` in `sendContext`, so it stays alive until the send completes
   - **No copy needed!** We use the slice directly from `Bytes()`
   - The existing `payloadPool` from `packet.go` can be reused for sends

2. **Send Completion Handler**:
   ```go
   go func() {
       for {
           cqe, err := ln.ring.WaitCQE()
           if err != nil {
               continue
           }

           ctx := (*sendContext)(unsafe.Pointer(uintptr(cqe.UserData)))
           p := ctx.packet
           buffer := ctx.buffer  // This is the *bytes.Buffer from payloadPool

           // Return buffer to pool (payloadPool from packet.go)
           payloadPool.Put(buffer)

           if cqe.Res < 0 {
               // Send error - packet may need retransmission
           } else {
               // Send successful
           }

           if p.Header().IsControlPacket {
               p.Decommission()
           }

           ln.ring.CQESeen(cqe)
       }
   }()
   ```

3. **Changes Required**:
   - Manual SQE preparation for sends
   - `syscall.Msghdr` setup for UDP addresses
   - Separate completion processing goroutine
   - Manual ring submission and completion handling
   - Unsafe pointer usage for packet tracking

#### Advantages of giouring Approach

- **Maximum control**: Direct access to io_uring features
- **Performance**: No channel overhead, direct ring access
- **Advanced features**: Can use buffer rings, fixed files, multishot
- **Fine-grained**: Control over every aspect of io_uring

#### Disadvantages of giouring Approach

- **More code changes**: Requires manual ring management
- **Complexity**: More boilerplate code, unsafe pointers
- **Error handling**: Manual error handling from CQE results
- **Buffer management**: More complex buffer lifecycle management

---

## Comparison Summary

| Aspect | iouring-go | giouring |
|--------|------------|----------|
| **Code Changes** | Minimal (channel-based) | Moderate (manual ring mgmt) |
| **Integration Effort** | Low (fits existing patterns) | Medium (new patterns needed) |
| **Performance** | Good (some channel overhead) | Excellent (direct ring access) |
| **Complexity** | Low | Medium-High |
| **Buffer Management** | Simple (pool or copy) | Complex (manual lifecycle) |
| **Error Handling** | Channel-based (idiomatic) | Manual CQE checking |
| **Maintainability** | High (fits Go idioms) | Medium (more C-like code) |
| **Advanced Features** | Limited | Full access |

## Recommendation

**For initial implementation: iouring-go** is recommended because:
1. **Minimal disruption**: Fits existing channel-based architecture
2. **Faster development**: Less code to write and maintain
3. **Easier testing**: Channel-based completion is easier to test
4. **Good enough performance**: Channel overhead is minimal compared to network I/O

**Future optimization**: If profiling shows bottlenecks, consider migrating specific hot paths to giouring for maximum performance.

## Implementation Plan

1. **Phase 1**: Implement receive path with iouring-go
   - Replace ReadFrom() goroutine
   - Add buffer pool
   - Test with existing packet processing

2. **Phase 2**: Implement transmit path with iouring-go
   - Replace WriteTo()/Write() in send()
   - Add completion handler
   - Verify packet ordering

3. **Phase 3**: Performance testing and optimization
   - Benchmark against current implementation
   - Profile and identify bottlenecks
   - Consider giouring for hot paths if needed

4. **Phase 4**: Advanced features (optional)
   - Buffer rings for zero-copy
   - Multishot receives
   - Fixed files for socket management

The following table summarizes the changes


