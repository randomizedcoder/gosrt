# SRT Client

A flexible SRT client that can read from various sources and write to various destinations. Use it to subscribe to streams, relay between protocols, or record to files.

## Overview

The client is a general-purpose data mover that:
- Reads from SRT, UDP, stdin, or a debug generator
- Writes to SRT, UDP, files, stdout, or discards data (null)
- Displays real-time throughput with recovery statistics
- Supports both caller and listener modes

## Features

- **Multiple Sources**: SRT, UDP, stdin, debug generator
- **Multiple Destinations**: SRT, UDP, files, stdout, null (discard)
- **Caller/Listener Modes**: Connect to servers or accept connections
- **Real-time Statistics**: Throughput, gaps, NAKs, retransmissions, recovery rate
- **io_uring Output**: Optional high-performance output on Linux
- **Prometheus Metrics**: HTTP and Unix socket endpoints
- **Colored Output**: Distinguish between multiple clients
- **Graceful Shutdown**: Clean termination with statistics on exit

## Usage

```bash
./client -from <source> -to <destination> [options]
```

### Required Flags

| Flag | Description |
|------|-------------|
| `-from` | Source URL (see Source Formats below) |
| `-to` | Destination URL (see Destination Formats below) |

### Client-Specific Flags

| Flag | Description |
|------|-------------|
| `-statsperiod` | Throughput display interval (default: `1s`) |
| `-color` | Output color: `red`, `green`, `yellow`, `blue`, `magenta`, `cyan` |
| `-iouringoutput` | Use io_uring for output writes (Linux only) |
| `-logtopics` | Comma-separated log topics for debugging |

### Metrics Flags

| Flag | Description |
|------|-------------|
| `-promhttp` | Prometheus HTTP endpoint (e.g., `:9091`) |
| `-promuds` | Prometheus Unix socket path |

### Profiling Flags

| Flag | Description |
|------|-------------|
| `-profile` | Profile type: `cpu`, `mem`, `mutex`, `block`, `trace` |
| `-profilepath` | Directory to write profile files (default: `.`) |

## Source Formats

| Format | Description |
|--------|-------------|
| `srt://host:port?streamid=...` | SRT connection (caller mode) |
| `srt://host:port?mode=listener` | SRT listener mode |
| `udp://host:port` | UDP socket listener |
| `-` | Standard input |
| `debug://?bitrate=5000000` | Debug data generator at specified bitrate |

### SRT URL Parameters

| Parameter | Description |
|-----------|-------------|
| `streamid` | Stream identifier (e.g., `subscribe:/stream`) |
| `mode` | Connection mode: `caller` (default) or `listener` |
| `passphrase` | Encryption passphrase |
| `latency` | Latency in milliseconds |

## Destination Formats

| Format | Description |
|--------|-------------|
| `srt://host:port?streamid=...` | SRT connection (caller mode) |
| `srt://host:port?mode=listener` | SRT listener mode |
| `udp://host:port` | UDP socket |
| `file:///path/to/file` | Write to file |
| `-` | Standard output |
| `null` or `discard` | Discard data (for benchmarking) |

## Statistics Display

The client displays real-time statistics every `statsperiod`:

```
[SUB             ] 12:34:56.78 |  1234.5 pkt/s |   56.78 MB | 10.123 Mb/s |  123.4k ok /     5 gaps /     3 NAKs /     2 retx | recovery= 99.5%
```

| Field | Description |
|-------|-------------|
| `pkt/s` | Packets received per second |
| `MB` | Total megabytes received |
| `Mb/s` | Current throughput in megabits per second |
| `ok` | Total packets successfully received |
| `gaps` | Sequence gaps detected (missing packets) |
| `NAKs` | NAK packets sent requesting retransmission |
| `retx` | Retransmissions received |
| `recovery` | `(gaps - TSBPD_skips) / gaps` - 100% means all gaps recovered |

## Examples

### Subscribe to SRT Stream

```bash
# Connect to server and display data
./client -from "srt://server:6000?streamid=subscribe:/stream" -to -

# Pipe to video player
./client -from "srt://server:6000?streamid=subscribe:/stream" -to - | ffplay -
```

### Record Stream to File

```bash
./client -from "srt://server:6000?streamid=subscribe:/stream" -to file:///tmp/recording.ts
```

### Relay SRT to UDP Multicast

```bash
./client -from "srt://server:6000?streamid=subscribe:/stream" -to udp://239.0.0.1:5000
```

### Benchmark Receiver Performance

```bash
# Discard output to measure pure receive performance
./client -from "srt://server:6000?streamid=subscribe:/stream" -to null
```

### Listen Mode (Accept Connections)

```bash
# Wait for publisher to push data
./client -from "srt://:6000?mode=listener&streamid=mystream" -to file:///tmp/received.ts
```

### Multiple Clients with Color Coding

```bash
# Terminal 1 - Blue client
./client -from "srt://server:6000?streamid=subscribe:/stream1" -to null -color blue

# Terminal 2 - Green client
./client -from "srt://server:6000?streamid=subscribe:/stream2" -to null -color green
```

### High-Performance Configuration

```bash
./client -from "srt://server:6000?streamid=subscribe:/stream" -to null \
  -fc 102400 \
  -rcvbuf 67108864 \
  -iouringrecvenabled \
  -useeventloop \
  -usepacketring
```

### Debug Generator (No Server Needed)

```bash
# Generate test data at 10 Mb/s
./client -from "debug://?bitrate=10000000" -to file:///tmp/test.dat
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        SRT Client                            │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   ┌───────────┐      ┌────────────┐      ┌───────────┐     │
│   │  Reader   │─────▶│  Main Loop │─────▶│  Writer   │     │
│   │           │      │            │      │           │     │
│   │ - SRT     │      │ - Buffer   │      │ - SRT     │     │
│   │ - UDP     │      │ - Metrics  │      │ - UDP     │     │
│   │ - stdin   │      │ - Stats    │      │ - file    │     │
│   │ - debug   │      │            │      │ - stdout  │     │
│   └───────────┘      └────────────┘      │ - null    │     │
│                                          └───────────┘     │
│                                                             │
│   ┌───────────────────────────────────────────────────┐    │
│   │              Statistics Display                    │    │
│   │  [SUB] 12:34:56 | 1234 pkt/s | 10.12 Mb/s | ...   │    │
│   └───────────────────────────────────────────────────┘    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Special Writers

### NullWriter

Discards all data without any I/O overhead. Use for benchmarking the receive path.

### NonblockingWriter

Buffers data when the underlying writer blocks, preventing backpressure from affecting the reader.

## Building

```bash
# Standard build
make client

# Debug build (with symbols for profiling)
make client-debug
```

## Files

| File | Description |
|------|-------------|
| `main.go` | Main entry point, connection setup, read/write loop |
| `reader.go` | DebugReader implementation for test data generation |
| `writer.go` | NonblockingWriter for buffered output |
