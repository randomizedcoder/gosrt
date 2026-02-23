# Common Library

Shared library providing CLI flags, data generation, statistics, and utilities used by all SRT tools in the contrib directory.

## Overview

The common package provides:
- Unified CLI flags for all SRT configuration options
- Rate-limited data generation for testing
- Throughput statistics display
- Prometheus metrics endpoints (HTTP and Unix socket)
- Output writers (file, stdout, null, io_uring)
- Terminal color support

## Contents

| File | Purpose |
|------|---------|
| `flags.go` | Unified CLI flags for all SRT configuration |
| `data_generator.go` | Token bucket rate-limited data generation |
| `statistics.go` | Connection statistics formatting and display |
| `metrics_server.go` | Prometheus metrics HTTP/UDS server |
| `metrics_client.go` | Prometheus metrics client for scraping |
| `writer.go` | Output writers (DirectWriter, NullWriter) |
| `writer_iouring_linux.go` | io_uring-based writer (Linux only) |
| `writer_iouring_stub.go` | Stub for non-Linux platforms |
| `colors.go` | ANSI color codes for terminal output |

## Flags

The `flags.go` file defines all CLI flags shared across tools. Flags are organized by category:

### Connection Flags

| Flag | Description |
|------|-------------|
| `-conntimeo` | Connection timeout (milliseconds) |
| `-peeridletimeo` | Peer idle timeout (milliseconds) |
| `-latency` | Maximum transmission latency (milliseconds) |
| `-rcvlatency` | Receiver-side latency (milliseconds) |
| `-peerlatency` | Peer latency request (milliseconds) |

### Buffer Flags

| Flag | Description |
|------|-------------|
| `-fc` | Flow control window (packets) |
| `-rcvbuf` | Receiver buffer size (bytes) |
| `-sndbuf` | Sender buffer size (bytes) |
| `-payloadsize` | Maximum payload size (bytes) |

### io_uring Flags

| Flag | Description |
|------|-------------|
| `-iouringenabled` | Enable io_uring for send |
| `-iouringrecvenabled` | Enable io_uring for receive |
| `-iouringrecvringsize` | Receive ring size (power of 2) |
| `-iouringrecvringcount` | Number of receive rings (1-16) |
| `-iouringrecvbatchsize` | Batch size for resubmissions |

### Lock-Free Receiver Flags

| Flag | Description |
|------|-------------|
| `-usepacketring` | Enable lock-free packet ring |
| `-packetringsize` | Ring size per shard |
| `-packetringshards` | Number of shards |
| `-useeventloop` | Enable continuous EventLoop |
| `-userecvcontrolring` | Enable receiver control ring |

### Lock-Free Sender Flags

| Flag | Description |
|------|-------------|
| `-usesendbtree` | Enable btree for sender packets |
| `-usesendring` | Enable lock-free sender ring |
| `-usesendcontrolring` | Enable control ring for ACK/NAK |
| `-usesendeventloop` | Enable sender EventLoop |

### NAK Configuration Flags

| Flag | Description |
|------|-------------|
| `-usenakbtree` | Enable NAK btree for gap detection |
| `-fastnakenabled` | Enable FastNAK optimization |
| `-fastnakrecentenabled` | Enable FastNAKRecent |
| `-honornakorder` | Retransmit in NAK packet order |

### Performance Test Flags

| Flag | Description |
|------|-------------|
| `-initial` | Starting bitrate for search |
| `-min-bitrate` | Minimum bitrate floor |
| `-max-bitrate` | Maximum bitrate ceiling |
| `-warmup` | Warm-up duration after bitrate change |
| `-stability-window` | Stability evaluation window |

### Application Flags

| Flag | Description |
|------|-------------|
| `-statsperiod` | Throughput display interval |
| `-color` | Terminal output color |
| `-promhttp` | Prometheus HTTP endpoint |
| `-promuds` | Prometheus Unix socket path |
| `-name` | Instance name for labeling |

## Usage

### Parsing Flags

```go
import "github.com/randomizedcoder/gosrt/contrib/common"

func main() {
    // Parse all flags and track which were explicitly set
    common.ParseFlags()

    // Apply flags to SRT config
    config := srt.DefaultConfig()
    common.ApplyFlagsToConfig(config)
}
```

### Building Flag Arguments

```go
// Get explicitly-set flags for subprocess
args := common.BuildFlagArgs()
// Returns: ["-fc=102400", "-useeventloop", ...]

// Filter specific flags
args := common.BuildFlagArgsFiltered("addr", "promuds")
```

### Flag Dependencies

```go
// Validate and auto-enable dependencies
warnings := common.ValidateFlagDependencies()
// Auto-enables: usesendbtree if usesendring is set, etc.
```

## DataGenerator

The `DataGenerator` provides rate-limited test data generation with precise bitrate control.

### Features

- Token bucket rate limiting
- High-rate optimization (time-based pacing for >1000 pkt/s)
- Zero allocations in hot path (pre-filled payload)
- Atomic statistics counters

### Usage

```go
// Create rate-limited generator
gen := common.NewDataGenerator(ctx, 10_000_000, 1456) // 10 Mb/s, 1456 byte packets

// Read generates data at the configured rate
buf := make([]byte, 1500)
n, err := gen.Read(buf)

// Get statistics
stats := gen.Stats()
fmt.Printf("Actual: %.2f Mb/s\n", stats.ActualBitrate/1_000_000)

// Unlimited mode (for stress testing)
gen := common.NewDataGeneratorUnlimited(ctx, 1456)
```

### Rate Calculation

```
packets_per_sec = bitrate / 8 / payload_size

Example: 10 Mb/s with 1456-byte packets
  bytes/sec = 10,000,000 / 8 = 1,250,000
  pkt/sec = 1,250,000 / 1456 = 858.5
  interval = 1.165 ms per packet
```

### High-Rate Mode

For >1000 packets/sec, the generator uses time-based pacing:

1. Calculate target time for each packet: `startTime + N * interval`
2. For intervals >1ms: sleep + busy-wait
3. For intervals <1ms: pure busy-wait (required for sub-millisecond precision)

## Statistics

The `statistics.go` file provides throughput display formatting.

### Usage

```go
// Create throughput display
go common.RunThroughputDisplayWithLabelAndColor(
    ctx,
    srtConn,           // ThroughputGetter interface
    "SUB",             // Label
    "green",           // Color
    1*time.Second,     // Display interval
)
```

### Output Format

```
[SUB             ] 12:34:56.78 |  1234.5 pkt/s |   56.78 MB | 10.123 Mb/s |  123.4k ok /     5 gaps /     3 NAKs /     2 retx | recovery= 99.5%
```

## Writers

### NullWriter

Discards all data without I/O overhead. Use for benchmarking:

```go
writer := common.NewNullWriter()
io.Copy(writer, reader) // Data is discarded
stats := writer.Stats()
```

### DirectWriter

Writes directly to an underlying writer with statistics:

```go
file, _ := os.Create("/tmp/output.ts")
writer := common.NewDirectWriter(file)
io.Copy(writer, reader)
```

### IoUringWriter (Linux only)

High-performance writer using io_uring for zero-copy writes:

```go
writer, _ := common.NewIoUringWriter(file, 256)
io.Copy(writer, reader)
writer.Close()
```

## Metrics Server

Start Prometheus metrics endpoints:

```go
// Start HTTP and/or Unix socket metrics servers
common.StartMetricsServers(ctx, ":9090", "/tmp/metrics.sock")
```

### Scraping

```bash
# HTTP endpoint
curl http://localhost:9090/metrics

# Unix socket
curl --unix-socket /tmp/metrics.sock http://localhost/metrics
```

## Colors

Terminal colors for output differentiation:

```go
color := common.GetColor("green")
reset := common.ColorReset
fmt.Printf("%s[HighPerf]%s Processing...\n", color, reset)
```

Available colors: `red`, `green`, `yellow`, `blue`, `magenta`, `cyan`

## Building

This is a library package, not a standalone binary. It's imported by other tools:

```go
import "github.com/randomizedcoder/gosrt/contrib/common"
```

Build tools that use this library:

```bash
make build-performance  # Builds all tools
make server client      # Builds specific tools
```
