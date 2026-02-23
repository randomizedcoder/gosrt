# SRT Client Generator

A rate-limited data generator that publishes test data to an SRT server. Used for testing and benchmarking when you don't have a real media source.

## Overview

The client-generator:
- Generates test data at a precise bitrate
- Publishes to an SRT server as a stream source
- Displays real-time throughput and NAK statistics
- Supports graceful pause via SIGUSR1 signal

## Features

- **Precise Bitrate Control**: Token bucket rate limiting
- **High-Rate Optimization**: Time-based pacing for >1000 pkt/s
- **Real-time Statistics**: Throughput, NAKs received, retransmissions
- **Graceful Pause**: SIGUSR1 stops data generation without disconnecting
- **Prometheus Metrics**: HTTP and Unix socket endpoints
- **Colored Output**: Distinguish from other processes
- **Graceful Shutdown**: Clean termination on SIGINT/SIGTERM

## Usage

```bash
./client-generator -to srt://host:port/path -bitrate <bps> [options]
```

### Required Flags

| Flag | Description |
|------|-------------|
| `-to` | SRT destination URL (e.g., `srt://127.0.0.1:6000/stream`) |

### Generator-Specific Flags

| Flag | Description |
|------|-------------|
| `-bitrate` | Target bitrate in bits/second (default: `2000000` = 2 Mb/s) |
| `-statsperiod` | Statistics display interval (default: `1s`) |
| `-color` | Output color: `red`, `green`, `yellow`, `blue`, `magenta`, `cyan` |
| `-logtopics` | Comma-separated log topics for debugging |

### Metrics Flags

| Flag | Description |
|------|-------------|
| `-promhttp` | Prometheus HTTP endpoint (e.g., `:9092`) |
| `-promuds` | Prometheus Unix socket path |

### Profiling Flags

| Flag | Description |
|------|-------------|
| `-profile` | Profile type: `cpu`, `mem`, `mutex`, `block`, `trace` |
| `-profilepath` | Directory to write profile files (default: `.`) |

## Statistics Display

The generator displays real-time statistics every `statsperiod`:

```
[PUB             ] 12:34:56.78 |  1234.5 pkt/s |   56.78 MB | 10.123 Mb/s |  123.4k ok /     0 gaps /     3 NAKs /     2 retx | recovery=100.0%
```

| Field | Description |
|-------|-------------|
| `pkt/s` | Packets sent per second |
| `MB` | Total megabytes sent |
| `Mb/s` | Current throughput in megabits per second |
| `ok` | Total packets sent |
| `gaps` | Always 0 (sender-side metric) |
| `NAKs` | NAK packets received from receiver |
| `retx` | Retransmissions sent |
| `recovery` | Always 100% for sender |

## How It Works

### Data Generation

1. **Payload Pre-fill**: Test pattern is generated once at startup
2. **Rate Limiting**: Token bucket controls packet generation rate
3. **High-Rate Mode**: For >1000 pkt/s, uses time-based pacing instead of sleep()
4. **Zero Allocations**: Hot path reuses pre-filled buffer

### Rate Calculation

```
packets_per_sec = bitrate / 8 / payload_size
interval = 1 / packets_per_sec

Example: 10 Mb/s with 1456-byte packets
  bytes/sec = 10,000,000 / 8 = 1,250,000
  pkt/sec = 1,250,000 / 1456 = 858.5
  interval = 1.165 ms per packet
```

## Examples

### Basic Publishing

```bash
./client-generator -to srt://127.0.0.1:6000/test -bitrate 5000000
```

### High Throughput

```bash
./client-generator -to srt://127.0.0.1:6000/test -bitrate 100000000 \
  -statsperiod 10s
```

### With Metrics

```bash
./client-generator -to srt://127.0.0.1:6000/test -bitrate 10000000 \
  -promuds /tmp/client_generator.sock
```

### Multiple Publishers (Color Coded)

```bash
# Terminal 1 - Red publisher
./client-generator -to srt://127.0.0.1:6000/stream1 -bitrate 5000000 -color red

# Terminal 2 - Green publisher
./client-generator -to srt://127.0.0.1:6000/stream2 -bitrate 5000000 -color green
```

### Graceful Pause

```bash
# Start generator
./client-generator -to srt://127.0.0.1:6000/test -bitrate 10000000 &
PID=$!

# Pause data generation (connection stays open)
kill -USR1 $PID

# Resume by restarting (or implement SIGUSR2 for resume)
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Client Generator                          │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│   ┌───────────────┐                                         │
│   │ DataGenerator │                                         │
│   │               │                                         │
│   │ - Pre-filled  │                                         │
│   │   payload     │                                         │
│   │ - Token bucket│                                         │
│   │   rate limit  │                                         │
│   └───────┬───────┘                                         │
│           │                                                 │
│           ▼                                                 │
│   ┌───────────────┐      ┌───────────────┐                 │
│   │  Main Loop    │─────▶│  SRT Write    │                 │
│   │               │      │               │                 │
│   │ - Generate    │      │ - Dial server │                 │
│   │ - Write       │      │ - Push data   │                 │
│   │ - Stats       │      │ - Track NAKs  │                 │
│   └───────────────┘      └───────────────┘                 │
│                                                             │
│   ┌───────────────────────────────────────────────────┐    │
│   │              Statistics Display                    │    │
│   │  [PUB] 12:34:56 | 1234 pkt/s | 10.12 Mb/s | ...   │    │
│   └───────────────────────────────────────────────────┘    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## Compared to client-seeker

| Feature | client-generator | client-seeker |
|---------|------------------|---------------|
| Bitrate Control | Fixed at startup | Dynamic via control socket |
| Orchestration | Manual | Automated (performance tool) |
| Use Case | Simple testing | Performance measurement |
| Rate Limiting | Token bucket (fixed) | Token bucket (adjustable) |
| Watchdog | None | Built-in safety fallback |

Use **client-generator** for:
- Simple manual testing
- Fixed bitrate scenarios
- When you don't need dynamic rate changes

Use **client-seeker** for:
- Automated performance testing
- Dynamic bitrate adjustment
- Integration with performance orchestrator

## Building

```bash
# Build via Makefile (no explicit target, built as part of build-performance)
make build-performance

# Or build directly
go build -o client-generator ./contrib/client-generator
```

## Files

| File | Description |
|------|-------------|
| `main.go` | Main entry point, connection setup, write loop |

The data generator implementation is in `contrib/common/data_generator.go`.
