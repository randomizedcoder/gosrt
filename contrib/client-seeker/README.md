# SRT Client Seeker

A controllable SRT data generator designed for automated performance testing. It accepts commands via Unix domain socket to dynamically adjust bitrate, enabling the performance orchestrator to find maximum sustainable throughput.

## Overview

The client-seeker is designed for orchestration:
- Accepts bitrate commands via JSON over Unix socket
- Sends heartbeats to confirm liveness
- Includes watchdog for automatic safety fallback
- Provides detailed Prometheus metrics for bottleneck detection
- Uses efficient token bucket rate limiting

## Features

- **Dynamic Bitrate Control**: Adjust rate without reconnecting
- **Control Socket**: JSON commands over Unix domain socket
- **Metrics Socket**: Prometheus metrics for monitoring
- **Watchdog**: Automatic fallback to safe rate if orchestrator fails
- **Token Bucket Modes**: Sleep, hybrid, or spin for different CPU/accuracy tradeoffs
- **Bottleneck Detection**: Instrumentation to identify throughput limiters
- **Graceful Shutdown**: Clean termination on commands or signals

## Usage

```bash
./client-seeker -target srt://host:port/path [options]
```

### Required Flags

| Flag | Description |
|------|-------------|
| `-target` | SRT target URL (e.g., `srt://127.0.0.1:6000/stream`) |

### Seeker-Specific Flags

| Flag | Description |
|------|-------------|
| `-min-bitrate-seeker` | Minimum allowed bitrate (default: `1000000` = 1 Mb/s) |
| `-max-bitrate-seeker` | Maximum allowed bitrate (default: `1000000000` = 1 Gb/s) |
| `-packet-size` | Packet size in bytes (default: `1456`) |
| `-refill-mode` | Token bucket mode: `sleep` (default), `hybrid`, `spin` |

### Socket Flags

| Flag | Description |
|------|-------------|
| `-control-socket` | Control UDS path (default: `/tmp/srt_seeker_control.sock`) |
| `-metrics-socket` | Metrics UDS path (default: `/tmp/srt_seeker_metrics.sock`) |

### Watchdog Flags

| Flag | Description |
|------|-------------|
| `-watchdog` | Enable watchdog (default: `true`) |
| `-watchdog-safe` | Safe bitrate on trigger (default: `10000000` = 10 Mb/s) |
| `-watchdog-stop` | Timeout before stopping (default: `30s`, `0` = never) |

### Profiling Flags

| Flag | Description |
|------|-------------|
| `-profile` | Profile type: `cpu`, `mem`, `mutex`, `block`, `trace` |
| `-profilepath` | Directory to write profile files (default: `.`) |

## Control Protocol

The control socket accepts JSON commands (one per line) and returns JSON responses.

### Commands

#### Set Bitrate

```json
{"command": "set_bitrate", "bitrate": 100000000}
```

Response:
```json
{
  "status": "ok",
  "current_bitrate": 100000000,
  "target_bitrate": 100000000,
  "connection_alive": true,
  "uptime_sec": 45.2
}
```

#### Get Status

```json
{"command": "get_status"}
```

Response:
```json
{
  "status": "ok",
  "current_bitrate": 100000000,
  "target_bitrate": 100000000,
  "packets_sent": 123456,
  "bytes_sent": 179883936,
  "connection_alive": true,
  "uptime_sec": 45.2,
  "watchdog_state": "normal"
}
```

#### Heartbeat

```json
{"command": "heartbeat"}
```

Response:
```json
{"status": "ok"}
```

#### Stop

```json
{"command": "stop"}
```

Response:
```json
{"status": "ok"}
```

### Using netcat

```bash
# Set bitrate
echo '{"command":"set_bitrate","bitrate":200000000}' | nc -U /tmp/srt_seeker_control.sock

# Get status
echo '{"command":"get_status"}' | nc -U /tmp/srt_seeker_control.sock

# Send heartbeat
echo '{"command":"heartbeat"}' | nc -U /tmp/srt_seeker_control.sock
```

## Metrics Endpoint

The metrics socket serves Prometheus metrics:

```bash
curl --unix-socket /tmp/srt_seeker_metrics.sock http://localhost/metrics
```

### Key Metrics

| Metric | Description |
|--------|-------------|
| `seeker_target_bitrate` | Target bitrate in bps |
| `seeker_actual_bitrate` | Measured bitrate |
| `seeker_efficiency` | Actual/target ratio (1.0 = perfect) |
| `seeker_packets_sent` | Total packets sent |
| `seeker_bytes_sent` | Total bytes sent |
| `seeker_connection_alive` | 1 if connected, 0 if not |
| `tokenbucket_wait_seconds` | Time waiting for tokens |
| `tokenbucket_blocked_total` | Times bucket was empty |
| `srt_write_seconds` | Time in SRT Write() calls |
| `srt_write_blocked_total` | Times Write() blocked |

## Token Bucket Modes

### Sleep (Default)

- Uses `time.Sleep()` for rate limiting
- Low CPU usage
- Suitable for most use cases
- Recommended for high throughput (>300 Mb/s)

### Hybrid

- Combines sleep with spin-wait
- Medium CPU usage
- Better accuracy than sleep
- May become bottleneck at very high rates

### Spin

- Pure spin-wait (busy loop)
- Highest CPU usage
- Best timing accuracy
- Only for latency-critical scenarios

## Watchdog

The watchdog protects against orchestrator failures:

1. **Normal**: Heartbeats received within timeout
2. **Warning**: No heartbeat, reduced to safe bitrate
3. **Critical**: Extended timeout, consider stopping

### States

| State | Condition | Action |
|-------|-----------|--------|
| `normal` | Heartbeat within timeout | Continue normal operation |
| `warning` | Heartbeat timeout | Reduce to safe bitrate |
| `critical` | Extended timeout (if `-watchdog-stop` set) | Graceful shutdown |

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                       Client Seeker                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│   ┌─────────────────┐       ┌─────────────────┐                │
│   │  Control Server │◀─────▶│   Orchestrator  │                │
│   │  (Unix Socket)  │       │   (External)    │                │
│   └────────┬────────┘       └─────────────────┘                │
│            │                                                    │
│            ▼                                                    │
│   ┌─────────────────┐                                          │
│   │ BitrateManager  │                                          │
│   │                 │                                          │
│   │ - Current rate  │                                          │
│   │ - TokenBucket   │                                          │
│   │ - Constraints   │                                          │
│   └────────┬────────┘                                          │
│            │                                                    │
│            ▼                                                    │
│   ┌─────────────────┐      ┌─────────────────┐                │
│   │ DataGenerator   │─────▶│   Publisher     │                │
│   │                 │      │                 │                │
│   │ - Rate control  │      │ - SRT Conn      │                │
│   │ - Stats         │      │ - Write timing  │                │
│   └─────────────────┘      └─────────────────┘                │
│                                                                 │
│   ┌─────────────────┐      ┌─────────────────┐                │
│   │  Metrics Server │      │    Watchdog     │                │
│   │  (Unix Socket)  │      │                 │                │
│   └─────────────────┘      │ - Heartbeat     │                │
│                            │ - Safe fallback │                │
│                            └─────────────────┘                │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Examples

### Basic Usage

```bash
./client-seeker -target srt://127.0.0.1:6000/perf-test
```

### Custom Sockets

```bash
./client-seeker -target srt://127.0.0.1:6000/perf-test \
  -control-socket /tmp/my_control.sock \
  -metrics-socket /tmp/my_metrics.sock
```

### High Throughput Testing

```bash
./client-seeker -target srt://127.0.0.1:6000/perf-test \
  -max-bitrate-seeker 600000000 \
  -refill-mode sleep \
  -fc 102400 \
  -rcvbuf 67108864 \
  -usesendbtree \
  -usesendring
```

### Control-Only Mode (No Target)

```bash
# Start without SRT connection for testing control interface
./client-seeker
```

### With Performance Orchestrator

The seeker is typically started by the `performance` tool:

```bash
# Don't run seeker directly - let performance tool manage it
./performance -initial 200000000 -max-bitrate 500000000
```

## Building

```bash
# Build via Makefile
make build-performance

# Or build directly
go build -o client-seeker ./contrib/client-seeker
```

## Files

| File | Description |
|------|-------------|
| `main.go` | Entry point, startup, shutdown |
| `bitrate.go` | BitrateManager with min/max constraints |
| `control.go` | Control server (Unix socket, JSON protocol) |
| `generator.go` | Data generator with rate limiting |
| `publisher.go` | SRT connection and write loop |
| `protocol.go` | JSON request/response types |
| `tokenbucket.go` | Token bucket implementation |
| `watchdog.go` | Heartbeat monitoring and safety fallback |
| `metrics.go` | Prometheus metrics server |
| `bottleneck.go` | Bottleneck detection logic |
