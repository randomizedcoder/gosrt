# UDP Echo Server (io_uring Example)

A simple UDP echo server demonstrating io_uring patterns from the GoSRT library.

## Overview

This example shows how to use io_uring for UDP networking following the patterns established in:
- `listen_linux.go` (receive path)
- `connection_linux.go` (send path)

## Key Concepts Demonstrated

1. **PrepareRecvMsg for UDP receive**: Captures source address for reply
2. **PrepareSendMsg for UDP send**: Sends to specified destination
3. **WaitCQETimeout**: Efficient blocking (zero latency on completion)
4. **Msghdr/Iovec/RawSockaddrAny**: Structures for UDP operations
5. **Completion tracking**: Request ID mapping
6. **Buffer pooling**: sync.Pool for memory reuse
7. **Pre-population**: Fill ring with pending receives at startup

## Usage

```bash
# Build
make build

# Run server
make run

# Test with netcat (in another terminal)
echo "hello world" | nc -u 127.0.0.1 9999

# Or use the automated test
make test-e2e
```

## Flags

| Flag | Description |
|------|-------------|
| `-addr` | UDP listen address (default: `:9999`) |
| `-ringsize` | io_uring ring size, must be power of 2 (default: `64`) |

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                      UDP Echo Server                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│   ┌─────────────────┐       ┌─────────────────┐                │
│   │  UDP Socket     │◀─────▶│   io_uring      │                │
│   │  (net.UDPConn)  │       │   Ring          │                │
│   └─────────────────┘       └────────┬────────┘                │
│                                      │                          │
│                              ┌───────┴───────┐                  │
│                              │               │                  │
│                              ▼               ▼                  │
│                    ┌──────────────┐  ┌──────────────┐          │
│                    │ RecvMsg SQEs │  │ SendMsg SQEs │          │
│                    │ (pre-pop'd)  │  │ (on demand)  │          │
│                    └──────┬───────┘  └──────┬───────┘          │
│                           │                 │                   │
│                           ▼                 ▼                   │
│                    ┌────────────────────────────────┐          │
│                    │    Completion Handler          │          │
│                    │    (WaitCQETimeout)            │          │
│                    │                                │          │
│                    │  • Recv → Extract data & addr  │          │
│                    │  • Send → Log & cleanup        │          │
│                    │  • Resubmit recv requests      │          │
│                    └────────────────────────────────┘          │
│                                                                 │
│   ┌─────────────────┐       ┌─────────────────┐                │
│   │  Buffer Pool    │       │  Completion Map │                │
│   │  (sync.Pool)    │       │  (reqID→info)   │                │
│   └─────────────────┘       └─────────────────┘                │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Code Walkthrough

### 1. Socket Setup

```go
// Create UDP socket
conn, err := net.ListenUDP("udp", udpAddr)
file, err := conn.File()
fd := int(file.Fd())  // Get raw fd for io_uring
```

### 2. io_uring Initialization

```go
ring := giouring.NewRing()
ring.QueueInit(ringSize, 0)
```

### 3. Receive Request (PrepareRecvMsg)

```go
// Setup structures - must stay alive until completion
rsa := new(syscall.RawSockaddrAny)
iovec := new(syscall.Iovec)
msg := new(syscall.Msghdr)

// Iovec points to buffer
iovec.Base = &buffer[0]
iovec.SetLen(len(buffer))

// Msghdr captures source address
msg.Name = (*byte)(unsafe.Pointer(rsa))
msg.Namelen = uint32(syscall.SizeofSockaddrAny)
msg.Iov = iovec
msg.Iovlen = 1

// Submit to ring
sqe := ring.GetSQE()
sqe.PrepareRecvMsg(fd, msg, 0)
sqe.SetData64(requestID)
ring.Submit()
```

### 4. Completion Handling (WaitCQETimeout)

```go
// Blocks until completion OR timeout (kernel wakes immediately on completion)
timeout := syscall.NsecToTimespec(10 * time.Millisecond)
cqe, err := ring.WaitCQETimeout(&timeout)

// Process based on result
if cqe.Res < 0 {
    // Error: errno = -cqe.Res
} else {
    // Success: bytes = cqe.Res
}

ring.CQESeen(cqe)  // Free CQ slot
```

### 5. Send Response (PrepareSendMsg)

```go
// Use captured source address as destination
msg.Name = (*byte)(unsafe.Pointer(rsa))  // rsa from recv
msg.Namelen = rsaLen
msg.Iov = iovec  // Points to response data
msg.Iovlen = 1

sqe := ring.GetSQE()
sqe.PrepareSendMsg(fd, msg, 0)
sqe.SetData64(requestID)
ring.Submit()
```

## Comparison with Main Library

| Aspect | This Example | Main Library |
|--------|--------------|--------------|
| Complexity | Simple, single file | Full protocol implementation |
| Multi-ring | No | Yes (parallel processing) |
| Batched submit | No | Yes (reduced syscalls) |
| Zero-copy | No | Yes (UnmarshalZeroCopy) |
| Error handling | Basic logging | Comprehensive metrics |

## Requirements

- Linux kernel 5.1+ (for io_uring)
- Go 1.21+
- github.com/randomizedcoder/giouring

## Building

```bash
# From contrib/udp_echo directory
make build           # Build binary
make build-debug     # Build with debug symbols

# Or from repo root
go build -o udp_echo ./contrib/udp_echo
```

## Testing

```bash
# Unit tests
make test            # Run tests
make test-race       # Run with race detection

# End-to-end tests with netcat
make test-e2e        # Automated test (starts server, sends message, verifies echo)
make test-quick      # Quick visual test
make test-multi      # Send multiple messages
make test-bench      # Benchmark with 100 messages
make test-interactive # Start server for manual nc testing

# Custom test message
make test-e2e MSG="my custom message"

# Custom port
make run PORT=8888
```

## Makefile Targets

| Target | Description |
|--------|-------------|
| `build` | Build the udp_echo binary |
| `build-debug` | Build with debug symbols for profiling |
| `run` | Build and run server on port 9999 |
| `test` | Run unit tests |
| `test-race` | Run tests with race detection |
| `test-e2e` | Automated end-to-end test |
| `test-quick` | Quick manual test |
| `test-multi` | Send multiple messages |
| `test-bench` | Benchmark with 100 messages |
| `test-interactive` | Start server for manual testing |
| `clean` | Remove built binaries |
| `help` | Show all targets and options |
