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

## Recommendation (Initial Evaluation)

**Note**: This section reflects the initial evaluation. The final decision is documented in the "Recommendation" section below, which selects **giouring** as the implementation library.

**Initial evaluation notes**:
- iouring-go offers easier integration with channel-based APIs
- giouring offers maximum performance and avoids additional channel overhead
- Profiling identified channel blocking as a major bottleneck (see "Blocking Channel Operations" section)
- **Decision**: giouring selected to avoid introducing more channels and optimize the send path bottleneck

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

### Approach 1: iouring-go (Channel-Based Integration) - NOT SELECTED

**Note**: This approach was evaluated but not selected. giouring was chosen instead to avoid introducing additional channels. This section is retained for reference.

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

### Approach 2: giouring (Low-Level Integration) - SELECTED

**This is the selected approach for implementation.**

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
   err := ring.QueueInit(1024, 0) // ring size, flags
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

**Key Benefits:**
- **Eliminates mutex bottleneck**: Each `send()` gets its own buffer from `sync.Pool`, allowing concurrent sends
- **No additional channels**: Direct ring access, no channel overhead
- **Async I/O**: Multiple sends can be in-flight simultaneously
- **Better performance**: Removes the single-threaded mutex serialization

**Implementation Details:**

1. **Context Tracking with Map**:

   **Why we need a map-based approach:**
   - The submission queue and completion queue are separate - there's a delay between submission and completion
   - When we submit, the kernel will copy the buffer data, but we must keep the buffer alive until completion confirms the copy is done
   - We need to map from the completion's `UserData` (a `uint64`) back to our context
   - Using unsafe pointers directly is risky - the context could be garbage collected
   - A map with proper locking is safer and more explicit

   ```go
   // Minimal context - only what we need for cleanup
   type sendContext struct {
       packet packet.Packet      // To decommission if control packet
       buffer *bytes.Buffer      // To return to pool
   }

   // In listener struct:
   type listener struct {
       // ... existing fields ...
       ring        *giouring.Ring
       sendContexts map[uint64]*sendContext  // Map from user_data to context
       sendContextLock sync.Mutex            // Protects sendContexts map
       nextSendID     uint64                 // Atomic counter for unique IDs
   }
   ```

   **Why this approach:**
   - **Unique IDs**: Each send gets a unique `uint64` ID (atomic counter)
   - **Map lookup**: Store context in map, keyed by ID
   - **Thread-safe**: Lock protects map during add/delete operations
   - **Memory safe**: Context stays alive as long as it's in the map
   - **Explicit lifecycle**: Clear when context is created (on submit) and destroyed (on completion)

2. **Send Method Replacement** (using existing payloadPool - NO COPY NEEDED):
   ```go
   // Use existing payloadPool from packet.go - no new pool needed!
   // Import: "github.com/datarhei/gosrt/packet"

   func (ln *listener) send(p packet.Packet) {
       // Get buffer from existing payloadPool (bytes.Buffer)
       // NO MUTEX NEEDED for buffer access - each send() gets its own buffer!
       sendBuffer := payloadPool.Get() // From packet package

       // Marshal directly into the pooled buffer
       if err := p.Marshal(sendBuffer); err != nil {
           payloadPool.Put(sendBuffer) // Return buffer on error
           p.Decommission()
           return
       }

       // Get the underlying slice - valid as long as buffer isn't modified
       bufferSlice := sendBuffer.Bytes()

       // Prepare sendmsg structures (local variables - don't need to keep alive)
       var iovec syscall.Iovec
       iovec.Base = &bufferSlice[0]
       iovec.SetLen(len(bufferSlice))

       var msg syscall.Msghdr
       addrPtr, addrLen, err := sockaddrToPtr(p.Header().Addr)
       if err != nil {
           payloadPool.Put(sendBuffer)
           return err
       }
       msg.Name = (*byte)(addrPtr)
       msg.Namelen = addrLen
       msg.Iov = &iovec
       msg.Iovlen = 1

       // Get unique ID for this send operation
       sendID := atomic.AddUint64(&ln.nextSendID, 1)

       // Create context - only store packet if it's a control packet
       // Data packets don't need decommissioning here (handled by congestion control)
       // This allows GC to free data packets sooner
       ctx := &sendContext{
           buffer: sendBuffer,  // Always need buffer to return to pool
           packet: nil,         // Will be set only for control packets
       }

       // Only store packet pointer if it's a control packet (needs decommissioning)
       if p.Header().IsControlPacket {
           ctx.packet = p
       }
       // For data packets, ctx.packet remains nil, allowing GC to free the packet

       // Add to map BEFORE submission (protects context from GC)
       ln.sendContextLock.Lock()
       ln.sendContexts[sendID] = ctx
       ln.sendContextLock.Unlock()

       sqe := ln.ring.GetSQE()
       if sqe == nil {
           // Ring full - remove from map and clean up
           ln.sendContextLock.Lock()
           delete(ln.sendContexts, sendID)
           ln.sendContextLock.Unlock()
           payloadPool.Put(sendBuffer)
           if p.Header().IsControlPacket {
               p.Decommission()
           }
           return
       }

       // Submit the send operation
       // IMPORTANT: The kernel reads from the buffer when it processes the submission queue entry,
       // NOT during PrepareSendMsg(). We must keep the buffer alive until the completion queue
       // entry confirms the kernel has finished reading.
       sqe.PrepareSendMsg(fd, &msg, 0)
       sqe.SetData(sendID)  // Store unique ID for map lookup

       ln.ring.Submit()

       // Function returns immediately - send is now async
       // Completion handler will clean up buffer and packet (if control packet)
   }
   ```

   **Key Points:**
   - **No mutex for buffer access**: Each `send()` gets its own buffer from the pool, allowing concurrent sends
   - **Map with lock**: Protects the context map during add (in `send()`) and delete (in completion handler)
   - **Unique IDs**: Atomic counter ensures each send has a unique identifier for map lookup
   - **Kernel reads during processing**: The kernel reads from the buffer when it processes the submission queue entry, NOT during `PrepareSendMsg()`
   - **Buffer must stay alive**: We keep the buffer in the map until the completion queue entry confirms the kernel has finished reading
   - **Optimized packet tracking**: Only control packets are stored in context (`ctx.packet != nil`); data packets have `ctx.packet == nil`, allowing GC to free them immediately after submission
   - **Async submission**: Function returns immediately after submission, allowing concurrent sends
   - **Explicit lifecycle**: Context is created on submit (added to map) and destroyed on completion (removed from map)

3. **Send Completion Handler**:
   ```go
   // sendCompletionHandler processes io_uring send completions
   // This is a named function (not anonymous) for clarity
   func (ln *listener) sendCompletionHandler() {
       defer func() {
           ln.log("listen", func() string { return "left send completion handler loop" })
       }()

       for {
           // Check for shutdown before blocking on WaitCQE
           // Non-blocking select allows graceful shutdown
           select {
           case <-ln.doneChan:
               // Shutdown requested - drain any pending completions and exit
               ln.drainPendingCompletions()
               return
           default:
               // Continue to wait for completions
           }

           cqe, err := ln.ring.WaitCQE()
           if err != nil {
               // Check if we're shutting down (WaitCQE might return error on shutdown)
               if ln.isShutdown() {
                   return
               }
               continue
           }

           // Get the unique ID from completion
           sendID := cqe.UserData

           // Look up context in map (with lock)
           ln.sendContextLock.Lock()
           ctx, exists := ln.sendContexts[sendID]
           if !exists {
               // Context not found (shouldn't happen, but handle gracefully)
               ln.sendContextLock.Unlock()
               ln.ring.CQESeen(cqe)
               continue
           }

           // Remove from map immediately (we have the context now)
           delete(ln.sendContexts, sendID)
           ln.sendContextLock.Unlock()

           // Now we can safely use the context
           buffer := ctx.buffer

           // Return buffer to pool (payloadPool from packet.go)
           // Safe to do now - kernel has finished reading the data (confirmed by completion)
           payloadPool.Put(buffer)

           // Check send result
           if cqe.Res < 0 {
               // Send error - packet may need retransmission
               // For data packets, congestion control will handle retransmission
               // For control packets, they're decommissioned anyway
           }

           // Decommission control packets if present
           // Data packets have ctx.packet == nil, so nothing to do
           if ctx.packet != nil {
               ctx.packet.Decommission()
           }

           // Mark completion as seen
           ln.ring.CQESeen(cqe)
       }
   }

   // drainPendingCompletions processes any remaining completions during shutdown
   func (ln *listener) drainPendingCompletions() {
       // Process any remaining completions in the queue
       // Use PeekCQE to check if there are completions (non-blocking),
       // then WaitCQE to actually consume them
       for {
           // Peek to check if there are completions (non-blocking)
           peekCQE := ln.ring.PeekCQE()
           if peekCQE == nil {
               // No more completions in queue
               break
           }

           // Actually wait for and consume the completion
           // This will return immediately since we know there's one available
           cqe, err := ln.ring.WaitCQE()
           if err != nil {
               // Error or no more completions
               break
           }

           // Process the completion
           sendID := cqe.UserData

           ln.sendContextLock.Lock()
           ctx, exists := ln.sendContexts[sendID]
           if exists {
               delete(ln.sendContexts, sendID)
           }
           ln.sendContextLock.Unlock()

           if exists {
               // Clean up context
               payloadPool.Put(ctx.buffer)
               if ctx.packet != nil {
                   ctx.packet.Decommission()
               }
           }

           // Mark completion as seen
           ln.ring.CQESeen(cqe)
       }
   }
   ```

   **Graceful Shutdown:**
   - **Non-blocking check**: Before calling `WaitCQE()`, we check `doneChan` with a non-blocking select
     - If `doneChan` is closed (shutdown requested), we drain pending completions and exit
     - Otherwise, we continue to wait for completions
   - **Drain pending**: `drainPendingCompletions()` processes any remaining completions:
     - Uses `PeekCQE()` to check if there are completions (non-blocking check)
     - If a completion exists, calls `WaitCQE()` to actually consume it (returns immediately since we know one is available)
     - Processes the completion and marks it as seen with `CQESeen()`
     - Repeats until no more completions are available
   - **Cleanup**: All contexts are cleaned up (buffers returned to pool, control packets decommissioned)
   - **Pattern**: Matches the existing pattern - `doneChan` is closed in `Close()` (listen.go:398), and other goroutines check it similarly
   - **Note**: `WaitCQE()` is blocking, so we check `doneChan` before each call. If shutdown happens while waiting, `WaitCQE()` may return an error, which we also check for.

   **Optimization benefits:**
   - **Data packets**: `ctx.packet == nil`, allowing GC to free the packet immediately after submission
   - **Control packets**: `ctx.packet != nil`, kept alive until completion for decommissioning
   - **Reduced memory pressure**: Data packets (which are the majority) don't need to stay alive until completion
   - **Clearer code**: Named function makes the completion handler more explicit and testable

   **Why the map-based approach is needed:**
   - **Submission/Completion separation**: The submission queue and completion queue are separate - there's a delay between submission and completion
   - **Kernel reads during processing**: The kernel reads from the buffer when it processes the submission queue entry, NOT during `PrepareSendMsg()`
   - **Buffer must stay alive**: We must keep the buffer alive until the completion queue entry confirms the kernel has finished reading
   - **Unique ID mapping**: We store a unique `uint64` ID in `sqe.SetData()`, which is returned in `cqe.UserData`
   - **Map lookup**: Use the ID to look up the context in the map
   - **Lock protection**: Lock protects the map during add (in `send()`) and delete (in completion handler)
   - **Memory safety**: Context stays alive as long as it's in the map, preventing garbage collection
   - **Explicit lifecycle**: Clear when context is created (on submit) and destroyed (on completion)

   **Why not use unsafe pointers directly:**
   - Unsafe pointers could become invalid if the context struct is garbage collected between submission and completion
   - The map approach is safer and more explicit about memory management
   - The lock overhead is minimal compared to the network I/O latency

4. **Address Conversion Helper**:

   **Library Review and Recommendation:**

   Converting `net.Addr` (specifically `net.UDPAddr`) to `syscall` sockaddr structures is required for `io_uring` operations. Several options were evaluated:

   **Option 1: golang.org/x/sys/unix (RECOMMENDED)**
   - **Library**: `golang.org/x/sys/unix` (already a dependency in `go.mod`)
   - **Types**: `SockaddrInet4` and `SockaddrInet6` implement the `Sockaddr` interface
   - **Method**: `sockaddr() (ptr unsafe.Pointer, len _Socklen, err error)`
   - **Advantages**:
     - ✅ **Well-tested**: Part of the official Go extended standard library, maintained by the Go team
     - ✅ **Already available**: Already in `go.mod` (`golang.org/x/sys v0.38.0`)
     - ✅ **No unsafe code in application**: The `sockaddr()` method handles all unsafe pointer conversions internally
     - ✅ **High performance**: Direct struct construction, minimal overhead
     - ✅ **Type-safe**: Uses proper Go types (`SockaddrInet4`, `SockaddrInet6`) instead of raw structs
     - ✅ **Cross-platform**: Works on Linux, BSD, and other Unix-like systems
     - ✅ **Maintained**: Actively maintained as part of the Go project
   - **Implementation**: Construct `SockaddrInet4` or `SockaddrInet6` from `net.UDPAddr`, then call `sockaddr()` to get the pointer and length
   - **Performance**: Very high - just struct field assignment and a method call (the `sockaddr()` method is optimized and handles byte order conversion internally)

   **Option 2: Standard library `syscall` package**
   - **Library**: `syscall` (standard library)
   - **Types**: `syscall.RawSockaddrInet4`, `syscall.RawSockaddrInet6`
   - **Advantages**:
     - ✅ Part of standard library
   - **Disadvantages**:
     - ❌ **Requires unsafe code**: Direct manipulation of `RawSockaddrInet4/6` structs requires `unsafe.Pointer` conversions
     - ❌ **Manual byte order conversion**: Must manually handle `htons()` for port conversion
     - ❌ **More error-prone**: Direct struct manipulation is more prone to bugs
     - ❌ **Less type-safe**: Uses raw C structures directly
   - **Performance**: High, but requires manual unsafe operations

   **Option 3: Custom helper function (current draft)**
   - **Implementation**: Custom function with `unsafe.Pointer` and manual struct construction
   - **Disadvantages**:
     - ❌ **Requires unsafe code**: Uses `unsafe.Pointer` directly in application code
     - ❌ **Not well-tested**: Custom code needs extensive testing
     - ❌ **Maintenance burden**: Must maintain and test the conversion logic
     - ❌ **Manual byte order**: Must implement `htons()` or similar
   - **Performance**: High, but adds maintenance overhead

   **Recommendation: Use `golang.org/x/sys/unix`**

   The `golang.org/x/sys/unix` package provides the best balance of safety, performance, and maintainability. It's already a dependency, well-tested, and avoids unsafe code in our application.

   **Implementation using golang.org/x/sys/unix:**
   ```go
   import (
       "net"
       "golang.org/x/sys/unix"
       "unsafe"
   )

   // Helper to convert net.Addr to syscall sockaddr pointer
   // Uses golang.org/x/sys/unix types to avoid unsafe code in application
   func sockaddrToPtr(addr net.Addr) (unsafe.Pointer, uint32, error) {
       switch a := addr.(type) {
       case *net.UDPAddr:
           if a.IP.To4() != nil {
               // IPv4
               sa := &unix.SockaddrInet4{
                   Port: a.Port,
               }
               copy(sa.Addr[:], a.IP.To4())

               // sockaddr() handles all unsafe conversions internally
               ptr, len, err := sa.sockaddr()
               if err != nil {
                   return nil, 0, err
               }
               return ptr, uint32(len), nil
           } else {
               // IPv6
               sa := &unix.SockaddrInet6{
                   Port: a.Port,
               }
               copy(sa.Addr[:], a.IP)
               // Note: ZoneId handling for IPv6 link-local addresses
               // If needed, can be extracted from a.Zone using net.InterfaceByName

               ptr, len, err := sa.sockaddr()
               if err != nil {
                   return nil, 0, err
               }
               return ptr, uint32(len), nil
           }
       default:
           return nil, 0, fmt.Errorf("unsupported address type: %T", addr)
       }
   }
   ```

   **Performance Considerations:**
   - **Struct construction**: O(1) - just copying IP address bytes and setting port
   - **sockaddr() call**: O(1) - optimized method that handles byte order conversion
   - **Total overhead**: Minimal - suitable for high-frequency calls in the send path
   - **Memory**: Stack-allocated structs, no heap allocation
   - **Note**: The `sockaddr()` method returns a pointer to an internal `raw` field, which is safe as long as the `SockaddrInet4/6` struct remains in scope (which it does in our usage pattern)

   **IPv6 Zone ID Handling:**
   - For IPv6 link-local addresses, `net.UDPAddr` may have a `Zone` field
   - `golang.org/x/sys/unix.SockaddrInet6` has a `ZoneId` field (uint32)
   - If needed, can convert zone name to zone ID using `net.InterfaceByName()` and `net.Interface.Index`
   - For most use cases (non-link-local addresses), `ZoneId` can be left as 0

5. **Initialization** (in `Listen()` or `Dial()`):
   ```go
   // Initialize io_uring ring
   ln.ring = giouring.NewRing()

   // Ring size considerations:
   // - Must be a power of 2 (256, 512, 1024, 2048, 4096, etc.)
   // - Typical range: 256 to 8192 (kernel 5.1+), up to 65536 (kernel 5.19+)
   // - Larger rings allow more in-flight operations but use more memory
   // - Memory usage: ~(ring_size * 64 bytes) for submission queue + completion queue
   // - For GoSRT: 1024-2048 is recommended for high-throughput scenarios
   //   - 1024: ~128 KB memory, allows 1024 concurrent sends
   //   - 2048: ~256 KB memory, allows 2048 concurrent sends
   // - Smaller rings (256-512) are fine for low-throughput scenarios
   ringSize := 1024  // Or make configurable via Config
   err := ln.ring.QueueInit(ringSize, 0) // ring size, flags
   if err != nil {
       return nil, err
   }

   // Initialize send context tracking
   ln.sendContexts = make(map[uint64]*sendContext)
   ln.nextSendID = 0  // Start from 0, first send will be 1

   // Start completion handler goroutine (named function for clarity)
   go ln.sendCompletionHandler()
   ```

   **Ring Size Selection Guidelines:**

   - **Ring size must be a power of 2**: 256, 512, 1024, 2048, 4096, 8192, etc.
   - **Kernel limits**:
     - Kernel 5.1-5.18: Typically up to 8192 entries
     - Kernel 5.19+: Up to 65536 entries (with IORING_SETUP_SINGLE_ISSUER flag)
     - Practical limit is often lower due to memory constraints
   - **Memory usage**: Approximately `ring_size * 64 bytes` for both submission and completion queues
     - 256 entries: ~32 KB
     - 1024 entries: ~128 KB
     - 2048 entries: ~256 KB
     - 4096 entries: ~512 KB

   **Choosing the right size:**

   - **For GoSRT (high send throughput)**: 1024-2048 is recommended
     - Allows 1024-2048 concurrent in-flight sends
     - Balances memory usage with throughput capacity
     - Prevents ring full conditions under high load
   - **For low-throughput scenarios**: 256-512 is sufficient
     - Lower memory footprint
     - Still allows reasonable concurrency
   - **Considerations**:
     - **Ring full condition**: If `GetSQE()` returns `nil`, the ring is full
       - This means you have `ring_size` operations already in-flight
       - Larger rings reduce the chance of this happening
     - **Memory vs. throughput trade-off**: Larger rings use more memory but allow more concurrency
     - **Per-connection rings**: If each connection has its own ring, smaller sizes (256-512) may be appropriate
     - **Shared ring (listener/dialer)**: Larger sizes (1024-2048) are better since all connections share it

   **Downsides of larger rings (1024-2048):**
   - **Memory usage**: More memory per ring (128-256 KB per ring)
   - **Cache effects**: Larger rings may have worse cache locality
   - **Over-provisioning**: If you never have that many concurrent sends, memory is wasted
   - **For GoSRT**: These downsides are minimal compared to the benefit of avoiding ring full conditions

   **Recommendation for GoSRT:**
   - **Start with 1024**: Good balance for most use cases
   - **Increase to 2048** if you see ring full conditions under load
   - **Make configurable**: Add `RingSize` to `Config` struct to allow tuning per deployment

6. **Changes Required**:
   - Remove `sndMutex` from listener/dialer (no longer needed - replaced by `sendContextLock`)
   - Remove `sndData` buffer from listener/dialer (use `payloadPool` instead)
   - Add `ring *giouring.Ring` to listener/dialer
   - Add `sendContexts map[uint64]*sendContext` to listener/dialer
   - Add `sendContextLock sync.Mutex` to listener/dialer
   - Add `nextSendID uint64` (atomic counter) to listener/dialer
   - Initialize ring and map in `Listen()`/`Dial()`
   - Start completion handler goroutine in `Listen()`/`Dial()`
   - Replace `WriteTo()`/`Write()` with `PrepareSendMsg()` + `ring.Submit()`
   - Move packet decommissioning to completion handler
   - Handle ring full condition (when `GetSQE()` returns nil)
   - Clean up map and ring on shutdown

#### Advantages of giouring Approach

- **Maximum control**: Direct access to io_uring features
- **Performance**: No channel overhead, direct ring access
- **Advanced features**: Can use buffer rings, fixed files, multishot
- **Fine-grained**: Control over every aspect of io_uring

#### Disadvantages of giouring Approach

- **More code changes**: Requires manual ring management
- **Complexity**: More boilerplate code (map management, lock handling)
- **Error handling**: Manual error handling from CQE results
- **Buffer management**: Requires map-based tracking with locks (but still simpler than channels)
- **Lock overhead**: Map operations require locking, but this is minimal compared to network I/O latency

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

**Selected Library: giouring**

After evaluation, **giouring** has been selected for the io_uring implementation because:

1. **Avoids additional channels**: giouring provides direct ring access without requiring channel-based completion handling, which aligns with the goal of reducing channel overhead identified in profiling
2. **Maximum performance**: Pure Go implementation with no cgo overhead, direct syscall access, and full access to advanced io_uring features
3. **Send path optimization priority**: The send path has been identified as a bottleneck (see "Blocking Channel Operations" section), and giouring's direct ring access provides the best optimization opportunity
4. **No channel overhead**: Unlike iouring-go which introduces channels for completion handling, giouring allows direct completion processing without additional channel hops
5. **Future-proof**: Full access to advanced features (buffer rings, multishot, fixed files) for future optimizations

**Implementation Strategy**: Start with Phase 1 (send path) to address the identified bottleneck, then proceed to Phase 2 (receive path) for complete io_uring integration.

## Implementation Plan

1. **Phase 1**: Implement transmit path with giouring
   - Replace WriteTo()/Write() in send() with io_uring
   - Add completion handler goroutine
   - Implement context tracking for buffer/packet lifecycle
   - Verify packet ordering and error handling

2. **Phase 2**: Implement receive path with giouring
   - Replace ReadFrom() goroutine with io_uring
   - Add buffer pool for receive buffers
   - Implement direct routing to per-connection channels (eliminate rcvQueue)
   - Test with existing packet processing

3. **Phase 3**: Performance testing and optimization
   - Benchmark against current implementation
   - Profile and identify bottlenecks
   - Tune ring sizes and buffer pool sizes
   - Optimize hot paths based on profiling data

4. **Phase 4**: Advanced features (optional)
   - Buffer rings for zero-copy receives
   - Multishot receives for reduced syscall overhead
   - Fixed files for socket management

## Detailed Implementation Plan

This section provides step-by-step instructions for implementing the io_uring integration using giouring, following the optimized design described in this document.

### Phase 1: Transmit Path Implementation

#### Step 1.1: Add Dependencies and Imports

1. **Add giouring dependency**:
   ```bash
   go get github.com/pawelgaczynski/giouring@latest
   ```

2. **Update imports in `listen.go` and `dial.go`**:
   ```go
   import (
       "github.com/pawelgaczynski/giouring"
       "golang.org/x/sys/unix"
       "sync/atomic"
       // ... existing imports
   )
   ```

#### Step 1.2: Update Listener/Dialer Struct

1. **Add io_uring fields to `listener` struct** (listen.go):
   ```go
   type listener struct {
       // ... existing fields ...

       // io_uring for async I/O
       ring            *giouring.Ring
       sendContexts    map[uint64]*sendContext
       sendContextLock sync.Mutex
       nextSendID      uint64  // atomic counter
   }
   ```

2. **Add sendContext type** (in listen.go or a new file):
   ```go
   type sendContext struct {
       packet packet.Packet  // Only set for control packets (nil for data packets)
       buffer *bytes.Buffer  // Buffer from payloadPool to return
   }
   ```

3. **Repeat for `dialer` struct** (dial.go) with the same fields.

#### Step 1.3: Implement Address Conversion Helper

1. **Create `sockaddrToPtr()` function** (in listen.go or a new helper file):
   - Use the implementation from the "Address Conversion Helper" section
   - Uses `golang.org/x/sys/unix.SockaddrInet4` and `SockaddrInet6`
   - Returns `(unsafe.Pointer, uint32, error)`

2. **Add error handling** for unsupported address types

#### Step 1.4: Initialize io_uring Ring

1. **Update `Listen()` function** (listen.go):
   - After creating UDP connection, initialize ring:
     ```go
     ln.ring = giouring.NewRing()
     ringSize := 1024  // Or from config
     err := ln.ring.QueueInit(ringSize, 0)
     if err != nil {
         return nil, fmt.Errorf("failed to initialize io_uring: %w", err)
     }
     ```
   - Initialize `sendContexts` map: `ln.sendContexts = make(map[uint64]*sendContext)`
   - Initialize `nextSendID`: `ln.nextSendID = 0`

2. **Start completion handler goroutine**:
   ```go
   go ln.sendCompletionHandler()
   ```

3. **Repeat for `Dial()` function** (dial.go)

#### Step 1.5: Implement Send Completion Handler

1. **Create `sendCompletionHandler()` method** (listen.go):
   - Named function (not anonymous) for clarity
   - Loop with non-blocking select on `doneChan` for graceful shutdown
   - Call `ring.WaitCQE()` to get completions
   - Look up context in `sendContexts` map using `cqe.UserData`
   - Return buffer to `payloadPool`
   - Decommission control packets (if `ctx.packet != nil`)
   - Handle errors from `cqe.Res`
   - Call `ring.CQESeen(cqe)` to mark completion as seen

2. **Implement `drainPendingCompletions()` helper**:
   - Use `PeekCQE()` to check for pending completions
   - Use `WaitCQE()` to consume them
   - Process and clean up all pending contexts

3. **Repeat for dialer** (dial.go)

#### Step 1.6: Replace send() Method

1. **Update `send()` method** (listen.go):
   - Remove `sndMutex` usage (no longer needed)
   - Remove `sndData` buffer (use `payloadPool` instead)
   - Get buffer from `payloadPool`: `sendBuffer := payloadPool.Get()`
   - Marshal packet into buffer: `p.Marshal(sendBuffer)`
   - Get buffer slice: `bufferSlice := sendBuffer.Bytes()`
   - Create `syscall.Iovec` and `syscall.Msghdr` structures
   - Call `sockaddrToPtr()` to get address pointer
   - Generate unique send ID: `sendID := atomic.AddUint64(&ln.nextSendID, 1)`
   - Create `sendContext` (only store packet if control packet)
   - Add context to map (with lock)
   - Get SQE: `sqe := ln.ring.GetSQE()`
   - Handle ring full condition (if `sqe == nil`)
   - Prepare send: `sqe.PrepareSendMsg(fd, &msg, 0)`
   - Set user data: `sqe.SetData(sendID)`
   - Submit: `ln.ring.Submit()`
   - Function returns immediately (async)

2. **Handle ring full condition**:
   - Options:
     - Block until ring has space (poll for completion)
     - Drop packet and log error
     - Return error to caller
   - Recommendation: Log warning and return error (caller can retry)

3. **Repeat for dialer** (dial.go)

#### Step 1.7: Update Shutdown/Cleanup

1. **Update `Close()` method** (listen.go):
   - Close `doneChan` (already done, but verify)
   - Wait for completion handler to exit (optional: use sync.WaitGroup)
   - Close ring: `ln.ring.Close()` (if giouring provides this)
   - Clean up any remaining contexts in map

2. **Verify graceful shutdown**:
   - Completion handler should exit when `doneChan` is closed
   - All pending completions should be drained
   - All buffers should be returned to pool

#### Step 1.8: Remove Obsolete Code

1. **Remove `sndMutex` field** from listener/dialer structs
2. **Remove `sndData` buffer** from listener/dialer structs
3. **Update any code that referenced these fields**

#### Step 1.9: Testing

1. **Unit tests**:
   - Test `sockaddrToPtr()` with IPv4 and IPv6 addresses
   - Test ring initialization and cleanup
   - Test send context creation and cleanup
   - Test completion handler with mock completions

2. **Integration tests**:
   - Test basic send functionality
   - Test high-throughput sends (verify no ring full conditions)
   - Test error handling (invalid addresses, ring full, etc.)
   - Test graceful shutdown (verify all completions processed)

3. **Performance tests**:
   - Benchmark send throughput vs. current implementation
   - Profile memory usage (verify buffers are returned to pool)
   - Check for goroutine leaks

### Phase 2: Receive Path Implementation

#### Step 2.1: Add Receive Buffer Pool

1. **Create receive buffer pool** (in packet.go or new file):
   - Follow the pattern from `payloadPool` (bytes.Buffer pool)
   - Create a `[]byte` pool for receive buffers
   - Pool should return buffers of size `config.MSS`

2. **Add pool to listener/dialer structs**:
   ```go
   recvBufferPool *recvBufferPool  // or similar
   ```

#### Step 2.2: Update Receive Context Structure

1. **Create receive context type**:
   ```go
   type recvContext struct {
       buffer []byte
       msg    syscall.Msghdr
       rsa    syscall.RawSockaddrAny
       iovec  syscall.Iovec
   }
   ```

2. **Pre-allocate receive contexts** (in `Listen()`/`Dial()`):
   - Create array/slice of `recvContext` (e.g., 64 contexts)
   - Each context has its own buffer from pool
   - Initialize `msghdr` structures for each context

#### Step 2.3: Initialize Receive Ring (if separate from send)

1. **Option A: Use same ring for send and receive**
   - Simpler, but requires coordination
   - Recommended for initial implementation

2. **Option B: Separate ring for receives**
   - More complex, but allows independent tuning
   - Consider for future optimization

#### Step 2.4: Implement Receive Completion Handler

1. **Create receive completion handler**:
   - Similar structure to send completion handler
   - Process completions from ring
   - Extract address from `msghdr` using helper function
   - Parse packet: `packet.NewPacketFromData(addr, buffer[:cqe.Res])`
   - Route packet directly to connection (eliminate rcvQueue):
     - Look up connection in `ln.conns` using `sync.Map.Load()`
     - Send to connection's `networkQueue` channel
   - Re-submit receive request to maintain constant pending receives
   - Handle errors and re-submit on error

2. **Implement address extraction helper**:
   - Convert `syscall.RawSockaddrAny` to `net.Addr`
   - Use `golang.org/x/sys/unix.anyToSockaddr()` or similar
   - Convert to `net.UDPAddr`

#### Step 2.5: Replace ReadFrom() Goroutine

1. **Remove existing `reader()` goroutine** (listen.go):
   - Remove the goroutine that calls `ReadFrom()`
   - Remove `rcvQueue` channel (no longer needed)

2. **Initialize receive requests** (in `Listen()`/`Dial()`):
   - Submit initial batch of receive requests (e.g., 64)
   - Each request uses a pre-allocated context
   - Set user data to context index

3. **Start receive completion handler goroutine**:
   - Handler maintains constant pending receives
   - Re-submits on each completion

#### Step 2.6: Update Connection Routing

1. **Update packet routing**:
   - Packets now routed directly in completion handler
   - No intermediate `rcvQueue` channel
   - Direct send to per-connection `networkQueue`

2. **Handle handshake packets**:
   - Route to `backlog` channel (if destination socket ID is 0)
   - Use non-blocking send to avoid blocking completion handler

#### Step 2.7: Testing

1. **Unit tests**:
   - Test receive buffer pool
   - Test address extraction
   - Test packet parsing from buffers
   - Test connection routing

2. **Integration tests**:
   - Test basic receive functionality
   - Test high-throughput receives
   - Test packet routing to correct connections
   - Test handshake packet handling
   - Test error handling and re-submission

3. **Performance tests**:
   - Benchmark receive throughput vs. current implementation
   - Verify reduced latency (no rcvQueue hop)
   - Check for buffer leaks

### Phase 3: Performance Testing and Optimization

#### Step 3.1: Benchmarking

1. **Create benchmark tests**:
   - Compare io_uring vs. current implementation
   - Test various packet sizes
   - Test various connection counts
   - Test under high load

2. **Metrics to measure**:
   - Throughput (packets/second, bytes/second)
   - Latency (p50, p95, p99)
   - CPU usage
   - Memory usage
   - Goroutine count

#### Step 3.2: Profiling

1. **CPU profiling**:
   - Identify hot paths
   - Check for unnecessary allocations
   - Verify buffer pool effectiveness

2. **Memory profiling**:
   - Check for buffer leaks
   - Verify pool usage
   - Monitor GC pressure

3. **Block profiling**:
   - Verify reduced channel blocking
   - Check for lock contention
   - Identify any new bottlenecks

#### Step 3.3: Tuning

1. **Ring size tuning**:
   - Start with 1024
   - Increase if seeing ring full conditions
   - Decrease if memory constrained

2. **Buffer pool tuning**:
   - Adjust pool size based on usage patterns
   - Monitor pool hit rate

3. **Goroutine tuning**:
   - Verify optimal number of completion handlers
   - Check for goroutine leaks

### Phase 4: Advanced Features (Optional)

#### Step 4.1: Buffer Rings (Zero-Copy Receives)

1. **Research buffer ring setup**:
   - Requires kernel 5.19+
   - More complex setup
   - Provides true zero-copy

2. **Implementation**:
   - Register buffer ring with kernel
   - Use fixed buffers
   - Eliminate buffer copying

#### Step 4.2: Multishot Receives

1. **Enable multishot mode**:
   - Reduces syscall overhead
   - Single submission can receive multiple packets
   - Requires careful buffer management

#### Step 4.3: Fixed Files

1. **Register socket as fixed file**:
   - Reduces per-operation overhead
   - Useful for high-frequency operations

---

The following table summarizes the changes


