# SRT Server

An SRT server that accepts publish and subscribe connections, routing data from publishers to subscribers on matching stream paths.

## Overview

The server implements a pub/sub model where:
- **Publishers** connect with `publish:/path` to send data
- **Subscribers** connect with `subscribe:/path` to receive data
- Data is automatically routed from publishers to all matching subscribers

## Features

- **Pub/Sub Routing**: Automatic stream routing based on path matching
- **SRT v4/v5 Support**: Both legacy and modern streamid protocols
- **AES Encryption**: Optional passphrase-based encryption
- **Token Authentication**: Query parameter-based access control
- **Path Prefix Filtering**: Restrict streams to specific app paths
- **Prometheus Metrics**: HTTP and Unix socket endpoints
- **Profiling Support**: CPU, memory, mutex, block, trace profiling
- **Graceful Shutdown**: Clean connection termination on SIGINT/SIGTERM

## Usage

```bash
./server -addr :6000 [options]
```

### Required Flags

| Flag | Description |
|------|-------------|
| `-addr` | Listen address (e.g., `:6000` or `127.0.0.1:6000`) |

### Server-Specific Flags

| Flag | Description |
|------|-------------|
| `-app` | Path prefix filter for streamid (e.g., `/live`) |
| `-token` | Required token query parameter for access control |
| `-passphrase` | Encryption passphrase for encrypted streams |
| `-logtopics` | Comma-separated log topics for debugging |

### Metrics Flags

| Flag | Description |
|------|-------------|
| `-promhttp` | Prometheus HTTP endpoint (e.g., `:9090`) |
| `-promuds` | Prometheus Unix socket path |

### Profiling Flags

| Flag | Description |
|------|-------------|
| `-profile` | Profile type: `cpu`, `mem`, `allocs`, `heap`, `rate`, `mutex`, `block`, `thread`, `trace` |
| `-profilepath` | Directory to write profile files (default: `.`) |

### SRT Configuration Flags

All flags from `contrib/common/flags.go` are available for SRT configuration:
- Connection timeouts (`-conntimeo`, `-peeridletimeo`)
- Latency settings (`-latency`, `-rcvlatency`, `-peerlatency`)
- Buffer sizes (`-fc`, `-rcvbuf`, `-sndbuf`)
- io_uring configuration (`-iouringenabled`, `-iouringrecvenabled`, etc.)
- Lock-free architecture (`-useeventloop`, `-usepacketring`, etc.)

## Stream ID Format

### SRT v5 (Modern)

Publishers use: `publish:/path/to/stream`
Subscribers use: `subscribe:/path/to/stream`

With optional query parameters:
```
publish:/live/stream1?token=secret123
subscribe:/live/stream1?token=secret123
```

### SRT v4 (Legacy)

Legacy clients without streamid are assigned a path based on their remote address.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                         SRT Server                            │
├──────────────────────────────────────────────────────────────┤
│  ┌──────────────┐       ┌──────────────┐                     │
│  │  Publisher   │──────▶│   PubSub     │──────▶ Subscriber 1 │
│  │  (Writer)    │       │   Channel    │──────▶ Subscriber 2 │
│  └──────────────┘       │  /live/test  │──────▶ Subscriber N │
│                         └──────────────┘                     │
│                                                              │
│  ┌──────────────┐       ┌──────────────┐                     │
│  │  Publisher   │──────▶│   PubSub     │──────▶ Subscriber   │
│  │  (Writer)    │       │   Channel    │                     │
│  └──────────────┘       │  /other/path │                     │
│                         └──────────────┘                     │
└──────────────────────────────────────────────────────────────┘
```

## Connection Handling

### `handleConnect`

Called when a client initiates a connection:
1. Parses streamid to determine mode (publish/subscribe)
2. Validates token if required
3. Validates app path prefix if configured
4. Sets passphrase for encrypted connections
5. Returns `PUBLISH`, `SUBSCRIBE`, or `REJECT`

### `handlePublish`

Called when a publisher is accepted:
1. Creates or retrieves PubSub channel for the path
2. Blocks while publisher sends data
3. Removes channel when publisher disconnects
4. Logs statistics on completion

### `handleSubscribe`

Called when a subscriber is accepted:
1. Looks up existing PubSub channel for the path
2. Subscribes to receive data
3. Blocks while receiving data
4. Logs statistics on completion

## Examples

### Basic Server

```bash
./server -addr :6000
```

### Server with Encryption

```bash
./server -addr :6000 -passphrase "my-secret-key"
```

### Server with Access Control

```bash
./server -addr :6000 -app /live -token "secret123"
```

Clients must connect with:
- Path starting with `/live`
- Token query parameter: `subscribe:/live/stream?token=secret123`

### Server with Metrics

```bash
# HTTP metrics endpoint
./server -addr :6000 -promhttp :9090

# Unix socket metrics (for isolated namespaces)
./server -addr :6000 -promuds /tmp/srt_server.sock
```

### Server with High-Performance Config

```bash
./server -addr :6000 \
  -fc 102400 \
  -rcvbuf 67108864 \
  -iouringrecvenabled \
  -useeventloop \
  -usepacketring
```

### Server with CPU Profiling

```bash
./server -addr :6000 -profile cpu -profilepath /tmp/profiles
```

## Building

```bash
# Standard build
make server

# Debug build (with symbols for profiling)
make server-debug
```

## Files

| File | Description |
|------|-------------|
| `main.go` | Server implementation with pub/sub routing |
