# GoSRT Contrib Tools

This directory contains command-line tools and libraries for testing, benchmarking, and using the GoSRT implementation.

## Overview

The contrib directory provides a complete toolkit for SRT streaming and performance testing:

| Directory | Type | Purpose |
|-----------|------|---------|
| [server](#server) | Binary | SRT server with pub/sub support |
| [client](#client) | Binary | Flexible SRT client (subscribe, relay, record) |
| [client-generator](#client-generator) | Binary | Rate-limited data publisher for testing |
| [client-seeker](#client-seeker) | Binary | Controllable publisher for performance testing |
| [performance](#performance) | Binary | Automated throughput discovery (AIMD search) |
| [udp_echo](#udp-echo) | Binary | io_uring UDP echo example |
| [common](#common) | Library | Shared flags, utilities, and metrics |
| [integration_testing](#integration-testing) | Library | Test framework for isolation/network/parallel tests |

## Table of Contents

- [Quick Start](#quick-start)
- [Server](#server)
- [Client](#client)
- [Client-Generator](#client-generator)
- [Client-Seeker](#client-seeker)
- [Performance](#performance)
- [UDP Echo](#udp-echo)
- [Common](#common)
- [Integration Testing](#integration-testing)
- [io_uring Implementation](#io_uring-implementation)
- [Building](#building)

## Quick Start

Basic streaming setup:

```bash
# Terminal 1: Start server
./server -addr :6000

# Terminal 2: Start publisher
./client-generator -to srt://127.0.0.1:6000/test-stream -bitrate 5000000

# Terminal 3: Start subscriber
./client -from "srt://127.0.0.1:6000?streamid=subscribe:/test-stream" -to -
```

## Server

**Location:** `contrib/server/` | **[Detailed Documentation](server/README.md)**

An SRT server that accepts publish and subscribe connections. It routes data from publishers to subscribers on matching stream paths.

### Features

- Pub/Sub model with automatic stream routing
- SRT v4 (legacy) and v5 (modern streamid) protocol support
- AES encryption with passphrase
- Token-based access control
- Prometheus metrics export (HTTP and Unix socket)
- Configurable profiling (CPU, memory, mutex, etc.)

### Usage

```bash
./server -addr :6000 [options]
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-addr` | Listen address (required, e.g., `:6000` or `127.0.0.1:6000`) |
| `-app` | Path prefix filter for streamid (e.g., `/live`) |
| `-token` | Required token query parameter for access control |
| `-passphrase` | Encryption passphrase |
| `-promhttp` | Prometheus metrics HTTP endpoint (e.g., `:9090`) |
| `-promuds` | Prometheus metrics Unix socket path |
| `-profile` | Enable profiling (`cpu`, `mem`, `mutex`, `block`, `trace`) |

### Stream ID Format

Publishers connect with: `publish:/path/to/stream`
Subscribers connect with: `subscribe:/path/to/stream`

### Example

```bash
# Server with encryption and metrics
./server -addr :6000 -passphrase "secret123" -promhttp :9090

# Server with app prefix and token auth
./server -addr :6000 -app /live -token mytoken123
```

## Client

**Location:** `contrib/client/` | **[Detailed Documentation](client/README.md)**

A flexible SRT client that can read from various sources and write to various destinations. Use it to subscribe to streams, relay between protocols, or record to files.

### Features

- Multiple input sources: SRT, UDP, stdin, debug generator
- Multiple output targets: SRT, UDP, files, stdout, null (discard)
- Caller and listener modes
- Real-time throughput display with recovery statistics
- io_uring output support (Linux)

### Usage

```bash
./client -from <source> -to <destination> [options]
```

### Source/Destination Formats

| Format | Description |
|--------|-------------|
| `srt://host:port/path` | SRT connection |
| `srt://host:port?mode=listener` | SRT listener mode |
| `udp://host:port` | UDP socket |
| `file:///path/to/file` | File output |
| `-` | stdin (source) or stdout (destination) |
| `null` | Discard output (for benchmarking) |
| `debug://?bitrate=5000000` | Debug data generator |

### Key Flags

| Flag | Description |
|------|-------------|
| `-from` | Source URL (required) |
| `-to` | Destination URL (required, use `null` to discard) |
| `-statsperiod` | Throughput display interval (default: 1s) |
| `-color` | Output color for terminal (`red`, `green`, `blue`, etc.) |
| `-iouringoutput` | Use io_uring for output writes (Linux only) |

### Examples

```bash
# Subscribe and play (pipe to ffplay)
./client -from "srt://server:6000?streamid=subscribe:/stream" -to - | ffplay -

# Record stream to file
./client -from "srt://server:6000?streamid=subscribe:/stream" -to file:///tmp/recording.ts

# Relay SRT to UDP
./client -from "srt://server:6000?streamid=subscribe:/stream" -to udp://239.0.0.1:5000

# Benchmark receiver (discard output)
./client -from "srt://server:6000?streamid=subscribe:/stream" -to null
```

## Client-Generator

**Location:** `contrib/client-generator/` | **[Detailed Documentation](client-generator/README.md)**

A rate-limited data generator that publishes to an SRT server. Used for testing and benchmarking when you don't have a real media source.

### Features

- Precise bitrate control
- Packet-level rate limiting (efficient at high rates)
- Graceful pause via SIGUSR1
- Real-time throughput and NAK statistics display

### Usage

```bash
./client-generator -to srt://host:port/path -bitrate <bps> [options]
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-to` | SRT destination URL (required) |
| `-bitrate` | Target bitrate in bits/second (default: 2 Mb/s) |
| `-statsperiod` | Stats display interval (default: 1s) |
| `-color` | Output color for terminal |

### Examples

```bash
# Publish at 10 Mb/s
./client-generator -to srt://127.0.0.1:6000/test -bitrate 10000000

# Publish at 100 Mb/s with less frequent stats
./client-generator -to srt://127.0.0.1:6000/test -bitrate 100000000 -statsperiod 10s
```

## Client-Seeker

**Location:** `contrib/client-seeker/` | **[Detailed Documentation](client-seeker/README.md)**

A controllable SRT data generator designed for automated performance testing. It accepts commands via Unix domain socket to dynamically adjust bitrate.

### Features

- Dynamic bitrate control via JSON commands
- Watchdog for automatic safety fallback
- Prometheus metrics via Unix socket
- Token bucket rate limiting with configurable modes
- Integration with performance orchestrator

### Usage

```bash
./client-seeker -target srt://host:port/path [options]
```

### Control Protocol

Send JSON commands to the control socket:

```bash
# Set bitrate to 100 Mb/s
echo '{"command":"set_bitrate","bitrate":100000000}' | nc -U /tmp/srt_seeker_control.sock

# Get current status
echo '{"command":"get_status"}' | nc -U /tmp/srt_seeker_control.sock

# Heartbeat (keeps watchdog happy)
echo '{"command":"heartbeat"}' | nc -U /tmp/srt_seeker_control.sock

# Graceful stop
echo '{"command":"stop"}' | nc -U /tmp/srt_seeker_control.sock
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-target` | SRT target URL |
| `-control-socket` | Control UDS path (default: `/tmp/srt_seeker_control.sock`) |
| `-metrics-socket` | Metrics UDS path (default: `/tmp/srt_seeker_metrics.sock`) |
| `-min-bitrate-seeker` | Minimum allowed bitrate (default: 1 Mb/s) |
| `-max-bitrate-seeker` | Maximum allowed bitrate (default: 1 Gb/s) |
| `-watchdog` | Enable watchdog (default: true) |
| `-watchdog-timeout` | Timeout before fallback to safe rate |
| `-refill-mode` | Token bucket mode: `sleep`, `hybrid`, `spin` |

### Example

```bash
# Start seeker with custom control socket
./client-seeker -target srt://127.0.0.1:6000/perf-test \
  -control-socket /tmp/my_control.sock \
  -min-bitrate-seeker 10000000 \
  -max-bitrate-seeker 500000000
```

## Performance

**Location:** `contrib/performance/` | **[Detailed Documentation](performance/README.md)**

An automated performance testing orchestrator that discovers maximum sustainable throughput using AIMD (Additive Increase, Multiplicative Decrease) search.

### Features

- Automatic binary search for max throughput
- Configurable stability criteria (gap rate, NAK rate, RTT)
- Spawns and manages server + client-seeker processes
- CPU monitoring during tests
- JSON output for CI integration

### Usage

```bash
./performance [options]
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-initial` | Starting bitrate (default: 200 Mb/s) |
| `-min-bitrate` | Minimum bitrate floor (default: 50 Mb/s) |
| `-max-bitrate` | Maximum bitrate ceiling (default: 600 Mb/s) |
| `-step` | Additive increase step (default: 10 Mb/s) |
| `-precision` | Search precision (default: 5 Mb/s) |
| `-warmup` | Warm-up duration after bitrate change (default: 2s) |
| `-stability-window` | Stability evaluation window (default: 5s) |
| `-test-verbose` | Verbose output |
| `-test-json` | JSON output |
| `-dry-run` | Validate config without running |

### SRT Configuration

The performance tool passes SRT flags to both server and seeker:

```bash
# Test with io_uring and event loop enabled
./performance -initial 350000000 \
  -fc 102400 -rcvbuf 67108864 \
  -iouringrecvenabled -iouringrecvringcount 2 \
  -useeventloop -usepacketring
```

### Example

```bash
# Basic performance test
./performance

# High-throughput test with custom SRT config
./performance -initial 400000000 -max-bitrate 600000000 \
  -fc 204800 -rcvbuf 134217728 \
  -useeventloop -usesendbtree -usesendring

# Verbose with JSON output
./performance -test-verbose -test-json -test-output results.json
```

## UDP Echo

**Location:** `contrib/udp_echo/` | **[Detailed Documentation](udp_echo/README.md)**

A simple UDP echo server demonstrating io_uring patterns from the GoSRT library. Use this as a starting point to understand how io_uring is used for UDP networking.

### Features

- `PrepareRecvMsg`/`PrepareSendMsg` for UDP operations
- `WaitCQETimeout` for efficient blocking
- Msghdr/Iovec/RawSockaddrAny structure setup
- Completion tracking with request IDs
- Buffer pooling with sync.Pool
- Pre-populated ring for low latency

### Usage

```bash
# Build and run
go build -o udp_echo ./contrib/udp_echo
./udp_echo -addr :9999

# Test with netcat
echo "hello" | nc -u 127.0.0.1 9999
```

### Key Flags

| Flag | Description |
|------|-------------|
| `-addr` | UDP listen address (default: `:9999`) |
| `-ringsize` | io_uring ring size (default: `64`) |

## Common

**Location:** `contrib/common/` | **[Detailed Documentation](common/README.md)**

Shared library used by all binaries. Not a standalone tool.

### Contents

| File | Purpose |
|------|---------|
| `flags.go` | Unified CLI flags for all SRT configuration options |
| `data_generator.go` | Efficient rate-limited data generation |
| `statistics.go` | Connection statistics formatting |
| `metrics_server.go` | Prometheus metrics HTTP/UDS server |
| `metrics_client.go` | Prometheus metrics client |
| `writer.go` | Output writers (file, stdout, null) |
| `writer_iouring_linux.go` | io_uring-based writer (Linux only) |
| `colors.go` | Terminal color support |

### Flag Categories

The shared flags cover:

- **Connection**: timeouts, latency, buffer sizes
- **io_uring**: send/receive ring configuration
- **Lock-free design**: packet rings, event loops, control rings
- **NAK handling**: btree, FastNAK, RTO suppression
- **Performance test**: search parameters, stability thresholds

## Integration Testing

**Location:** `contrib/integration_testing/` | **[Detailed Documentation](integration_testing/README.md)**

Test framework library for running integration tests. Used by Makefile targets, not run directly.

### Test Modes

| Mode | Description |
|------|-------------|
| `clean` | Default namespace, loopback, no impairment |
| `network` | Isolated namespaces with packet loss/latency |
| `parallel` | Two pipelines side-by-side for A/B comparison |
| `isolation` | Simplified CG→Server tests to isolate variables |

### Key Files

| File | Purpose |
|------|---------|
| `config.go` | Test configuration types and presets |
| `test_configs.go` | Pre-defined test configurations |
| `test_matrix.go` | Matrix test generation |
| `network_controller.go` | Network namespace and netem control |
| `profiling.go` | Go profiler integration |
| `profile_analyzer.go` | Automated profile analysis |
| `profile_report.go` | HTML/JSON report generation |

### Configuration Presets

| Preset | Description |
|--------|-------------|
| `Base` | Baseline: list store, no io_uring |
| `Btree` | B-tree packet store only |
| `IoUr` | io_uring only |
| `NakBtree` | NAK btree only |
| `Full` | All features: btree + io_uring + NAK btree |
| `EventLoop` | Lock-free ring + event loop |
| `SendEL` | Complete lockless sender |
| `FullELLockFree` | Ultimate lock-free config |

### Running Tests

```bash
# Isolation tests (require root)
sudo make test-isolation CONFIG=Isolation-5M-CG-SendEventLoop

# Network impairment tests (require root)
sudo make test-network CONFIG=Network-Loss2pct-5Mbps

# Parallel comparison tests (require root)
sudo make test-parallel CONFIG=Parallel-Starlink-5Mbps

# Performance tests (no root required)
make test-performance
```

## io_uring Implementation

This section documents how io_uring is used across the contrib tools and compares it to the main library implementation.

### Contrib Implementation (Output Writer)

**Location:** `contrib/common/writer_iouring_linux.go`

The contrib tools use a simple io_uring writer for **file/stdout output only** (not networking):

```go
// Ring initialization (simple, no special flags)
ring.QueueInit(ringSize, 0)  // ringSize=256 default

// Write submission (PrepareWrite for files, not networking)
sqe := ring.GetSQE()
sqe.PrepareWrite(fd, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), 0)
sqe.SetData64(reqID)
ring.Submit()

// Completion handling (polling with sleep)
cqe, err := ring.PeekCQE()  // Non-blocking peek
if err == syscall.EAGAIN {
    time.Sleep(10 * time.Millisecond)  // Poll interval
}
```

**Characteristics:**
- Single ring per writer
- PeekCQE polling (non-blocking) with 10ms sleep between checks
- `PrepareWrite` for file I/O (not UDP)
- Per-writer `sync.Pool` for buffers
- Completion map for tracking pending operations

### Main Library Implementation (UDP Networking)

**Location:** `listen_linux.go` (receive), `connection_linux.go` (send)

The main GoSRT library uses io_uring for **high-performance UDP networking**:

#### Receive Path (Listener-level)

```go
// Msghdr setup for UDP receive with source address capture
rsa := new(syscall.RawSockaddrAny)
msg := new(syscall.Msghdr)
msg.Name = (*byte)(unsafe.Pointer(rsa))
msg.Namelen = uint32(syscall.SizeofSockaddrAny)
msg.Iov = &iovec
msg.Iovlen = 1

// Use PrepareRecvMsg for UDP (captures source address)
sqe.PrepareRecvMsg(fd, msg, 0)

// Blocking wait with timeout (kernel wakes us immediately on completion)
cqe, err := ring.WaitCQETimeout(&timeout)  // 10ms timeout
```

#### Send Path (Connection-level)

```go
// Pre-computed sockaddr (computed once at connection init)
msg.Name = (*byte)(unsafe.Pointer(&c.sendSockaddr))
msg.Namelen = c.sendSockaddrLen
msg.Iov = &iovec
msg.Iovlen = 1

// Use PrepareSendMsg for UDP (always send to known remote)
sqe.PrepareSendMsg(fd, &msg, 0)
```

**Characteristics:**
- Multi-ring support (parallel processing)
- `WaitCQETimeout` blocking (zero latency on completion arrival)
- `PrepareRecvMsg`/`PrepareSendMsg` for UDP networking
- Batched resubmission for reduced syscalls
- Pre-populated rings at startup
- Per-connection buffer pools
- Zero-copy where possible

### Comparison Table

| Aspect | Contrib (Writer) | Main Library (Networking) |
|--------|------------------|---------------------------|
| **Purpose** | File/stdout output | UDP send/receive |
| **Operations** | `PrepareWrite` | `PrepareRecvMsg`/`PrepareSendMsg` |
| **Completion** | `PeekCQE` + sleep | `WaitCQETimeout` (blocking) |
| **Latency** | Up to 10ms poll delay | Zero (kernel wake) |
| **Multi-ring** | No | Yes (round-robin for send) |
| **Batching** | No | Yes (resubmission batches) |
| **Scope** | Per-writer | Per-listener/per-connection |

### Key Differences

1. **Blocking vs Polling**: The main library uses `WaitCQETimeout` which blocks in the kernel until a completion arrives, providing **zero latency**. The contrib writer uses `PeekCQE` with a 10ms poll interval, which can add up to 10ms latency.

2. **UDP vs File I/O**: The main library uses `PrepareRecvMsg`/`PrepareSendMsg` with `Msghdr` structures to capture/specify source/destination addresses. The contrib writer uses `PrepareWrite` which only works for file descriptors.

3. **Multi-ring**: The main library supports multiple rings for parallel processing (critical for high throughput). The contrib writer uses a single ring.

4. **Pre-population**: The main library pre-populates the receive ring with pending requests at startup, reducing latency for the first packets.

### Potential Improvements

1. **Contrib Writer**: Could switch from `PeekCQE` + sleep to `WaitCQETimeout` for lower latency file writes (though file I/O typically isn't latency-sensitive).

2. **UDP Tools**: The contrib tools (server, client, client-generator) delegate networking to the main GoSRT library, which already has optimized io_uring support. No changes needed for networking.

3. **Example Code**: A simple UDP echo example demonstrating io_uring patterns from the main library would help users understand the implementation (see `contrib/udp_echo/`).

## Building

```bash
# Build all tools
make build

# Build production binaries
make client server

# Build with debug symbols (for profiling)
make client-debug server-debug

# Build performance testing tools
make build-performance
```

### Build Requirements

- Go 1.25+ (for experimental features: jsonv2, greenteagc)
- Linux (for io_uring support)
