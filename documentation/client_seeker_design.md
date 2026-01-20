# Client-Seeker Design Document

> **Parent Document**: [Performance Maximization: Reaching 500 Mb/s](performance_maximization_500mbps.md)
> **Status**: Draft Outline
> **Location**: `./contrib/client-seeker/`

---

## 1. Overview

### 1.1 Purpose

The **Client-Seeker** is a controllable SRT data generator that accepts real-time bitrate
adjustments via a Unix domain socket. Unlike the existing `client-generator` which runs at
a fixed bitrate, client-seeker can:

- Start at an initial bitrate
- Accept commands to change bitrate (instant or ramped)
- Report current status and metrics
- Support smooth bitrate transitions to avoid protocol shock

### 1.2 Why Not Modify `client-generator`?

The existing `client-generator` is optimized for simplicity and fixed-rate testing. Adding
control socket functionality would complicate its design. A separate tool allows:

- Clean separation of concerns
- Backward compatibility
- Easier testing and debugging
- Clear API boundary

### 1.3 Relationship to Parent Document

This component implements the "Client-Seeker" described in Section "Performance Test
Architecture" of the parent document. It provides the controllable data source that
the Performance Test Orchestrator drives.

---

## 2. Architecture

### 2.1 Component Diagram

```
┌────────────────────────────────────────────────────────────────────┐
│                          Client-Seeker                              │
├────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌─────────────────┐    ┌──────────────────┐    ┌───────────────┐  │
│  │  Control Server │◄───│  Bitrate Manager │───►│ SRT Publisher │  │
│  │  (Unix Socket)  │    │  (Ramping Logic) │    │ (Data Sender) │  │
│  └────────┬────────┘    └────────▲─────────┘    └───────┬───────┘  │
│           │                      │                      │          │
│           │   JSON commands      │   current rate       │  SRT     │
│           └──────────────────────┘                      │  packets │
│                                                         ▼          │
│  ┌─────────────────┐                           ┌───────────────┐   │
│  │ Prometheus UDS  │                           │   Server      │   │
│  │ (Metrics Export)│                           │ (127.0.0.1)   │   │
│  └─────────────────┘                           └───────────────┘   │
│                                                                     │
└────────────────────────────────────────────────────────────────────┘
```

### 2.2 Goroutine Model

```
main()
├── controlServer()          [goroutine: handles control socket]
├── bitrateManager()         [goroutine: manages rate transitions]
├── dataGenerator()          [goroutine: generates test data]
├── srtPublisher()           [goroutine: sends data via SRT]
└── metricsServer()          [goroutine: Prometheus UDS server]
```

---

## 3. Control Protocol

### 3.1 Design Philosophy: Keep Seeker "Dumb"

The client-seeker is intentionally kept **stateless and simple**. All complex logic
(ramping, timing, state machine) lives in the Orchestrator. This provides:

- **Single source of truth**: Orchestrator owns all control logic
- **Easier debugging**: Seeker just does what it's told
- **Simpler testing**: Mock seeker with predictable behavior
- **Crash recovery**: Orchestrator can restart seeker without state sync

**Key decision**: No `ramp_to` command. The Orchestrator handles ramping by sending
frequent `set_bitrate` updates (e.g., every 100ms during a ramp).

### 3.2 Socket Location

```
Default: /tmp/client_seeker_control.sock
Override: -control /path/to/socket.sock
```

### 3.3 Message Format (JSON over Unix Socket)

#### Request Messages

```json
// Set bitrate immediately (Orchestrator handles ramping externally)
{
    "command": "set_bitrate",
    "bitrate": 200000000
}

// Get current status
{
    "command": "get_status"
}

// Stop gracefully
{
    "command": "stop"
}

// Heartbeat (resets watchdog timer)
{
    "command": "heartbeat"
}
```

#### Response Messages

```json
// Success response
{
    "status": "ok",
    "current_bitrate": 200000000,
    "packets_sent": 1234567,
    "bytes_sent": 1800000000,
    "connection_alive": true,
    "uptime_seconds": 45.3
}

// Error response
{
    "status": "error",
    "error": "invalid bitrate: cannot be negative"
}
```

### 3.4 Command Details

| Command | Description | Parameters | Response |
|---------|-------------|------------|----------|
| `set_bitrate` | Change bitrate instantly | `bitrate` (bps) | Current state |
| `get_status` | Query current state | (none) | Full status object |
| `stop` | Graceful shutdown | (none) | Acknowledgment |
| `heartbeat` | Reset watchdog timer | (none) | Acknowledgment |

### 3.5 Watchdog Safety Mechanism

If the Orchestrator crashes, the client-seeker could continue blasting data at high
bitrate, wasting CPU/network resources. The **watchdog** prevents this:

```
┌─────────────────────────────────────────────────────────────────┐
│                    WATCHDOG STATE MACHINE                        │
│                                                                  │
│   Normal Operation:                                              │
│   ┌──────────────────────────────────────────────────────────┐  │
│   │  Orchestrator sends heartbeat every 2 seconds            │  │
│   │  Seeker resets watchdog timer on each heartbeat          │  │
│   └──────────────────────────────────────────────────────────┘  │
│                                                                  │
│   Orchestrator Crash Detected (no heartbeat for 5 seconds):     │
│   ┌──────────────────────────────────────────────────────────┐  │
│   │  1. Log warning: "Watchdog triggered - no heartbeat"     │  │
│   │  2. Drop bitrate to MIN_SAFE (e.g., 10 Mb/s)            │  │
│   │  3. Continue running (allows Orchestrator to recover)    │  │
│   └──────────────────────────────────────────────────────────┘  │
│                                                                  │
│   Optional: Stop entirely after extended timeout (30 seconds)   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

```go
type WatchdogConfig struct {
    Enabled        bool          // Enable watchdog (default: true)
    Timeout        time.Duration // Time without heartbeat before action (default: 5s)
    SafeBitrate    int64         // Fallback bitrate (default: 10 Mb/s)
    StopTimeout    time.Duration // Stop entirely after this (default: 30s, 0 = never)
}

// WatchdogState tracks tiered backoff state
type WatchdogState int

const (
    WatchdogNormal WatchdogState = iota  // Receiving heartbeats
    WatchdogWarning                       // Missed 1 heartbeat, reduced to SafeBitrate
    WatchdogCritical                      // Extended timeout, preparing to stop
)

func (s *Seeker) watchdogLoop() {
    ticker := time.NewTicker(500 * time.Millisecond)  // Check 2x per second
    state := WatchdogNormal

    for range ticker.C {
        elapsed := time.Since(s.lastHeartbeat)

        switch state {
        case WatchdogNormal:
            if elapsed > s.watchdog.Timeout {
                // === SOFT LANDING: Drop to safe rate immediately ===
                // This handles Orchestrator GC pauses (can be 10-50ms)
                s.log("Watchdog: WARNING - no heartbeat for %v, soft-landing to %d Mb/s",
                    elapsed, s.watchdog.SafeBitrate/1_000_000)
                s.setBitrate(s.watchdog.SafeBitrate)
                state = WatchdogWarning
            }

        case WatchdogWarning:
            if elapsed < s.watchdog.Timeout {
                // Orchestrator recovered! Resume normal operation
                // (Don't auto-restore bitrate - let Orchestrator set it)
                s.log("Watchdog: Orchestrator recovered after %v", elapsed)
                state = WatchdogNormal
            } else if elapsed > s.watchdog.StopTimeout {
                // Extended timeout - Orchestrator is dead
                state = WatchdogCritical
            }

        case WatchdogCritical:
            if elapsed < s.watchdog.Timeout {
                // Miracle recovery
                s.log("Watchdog: Orchestrator recovered from critical state")
                state = WatchdogNormal
            } else if s.watchdog.StopTimeout > 0 {
                // Final stop
                s.log("Watchdog: CRITICAL - stopping after %v without heartbeat", elapsed)
                s.stop()
                return
            }
        }
    }
}
```

**Tiered Backoff Timeline**:

```
Time since last heartbeat:
────────────────────────────────────────────────────────────────────────
0s        5s (Timeout)                    30s (StopTimeout)
│         │                               │
│ NORMAL  │ WARNING (soft-land to safe)  │ CRITICAL (stop)
│         │                               │
│         └─ Drop to 10 Mb/s             └─ Process exits
│            (allows GC recovery)            (prevents zombie)
│
└─ Full bitrate operation
```

**Why tiered backoff?**
- **GC pauses**: Go GC can pause 10-50ms at high allocation rates
- **Orchestrator lag**: Metrics processing may occasionally exceed heartbeat interval
- **Recovery**: Allows Orchestrator to resume without restart
- **Safety**: Eventually stops if Orchestrator truly crashed

**Benefits:**
- Prevents runaway CPU/network usage if Orchestrator crashes
- Allows Orchestrator to recover and resume control
- Configurable timeouts for different environments

---

## 4. Bitrate Manager

### 4.1 Simple Instant Changes

Since the Orchestrator handles ramping (by sending frequent `set_bitrate` commands),
the client-seeker's bitrate manager is intentionally simple:

```go
type BitrateManager struct {
    current     atomic.Int64  // Current bitrate in bps
    minBitrate  int64         // Floor (default: 1 Mb/s)
    maxBitrate  int64         // Ceiling (default: 1 Gb/s)
}

func (b *BitrateManager) Set(bitrate int64) error {
    // Clamp to bounds
    if bitrate < b.minBitrate {
        bitrate = b.minBitrate
    }
    if bitrate > b.maxBitrate {
        bitrate = b.maxBitrate
    }

    b.current.Store(bitrate)
    return nil
}
```

### 4.2 Ramping is Orchestrator's Responsibility

The Orchestrator implements ramping by sending frequent `set_bitrate` commands:

```
Orchestrator-Driven Ramp (200 Mb/s → 300 Mb/s over 2 seconds):
─────────────────────────────────────────────────────────────

Time    Command from Orchestrator         Seeker Bitrate
────    ─────────────────────────         ──────────────
0ms     set_bitrate(200000000)            200 Mb/s
100ms   set_bitrate(205000000)            205 Mb/s
200ms   set_bitrate(210000000)            210 Mb/s
...     ...                               ...
1900ms  set_bitrate(295000000)            295 Mb/s
2000ms  set_bitrate(300000000)            300 Mb/s
```

**Benefits of this approach:**
- Seeker stays simple and stateless
- Orchestrator has full control over ramp timing
- Easy to implement different ramp profiles (linear, exponential, etc.)
- Ramp can be cancelled instantly by sending a different bitrate

### 4.3 Rate Limiting with Token Bucket

The token bucket algorithm provides smooth, precise rate limiting without bursts:

```go
// tokenbucket.go - Precise rate limiting for high-throughput streaming

package main

import (
    "sync"
    "sync/atomic"
    "time"
)

// TokenBucket implements a token bucket rate limiter
// Thread-safe for concurrent use by DataGenerator and BitrateManager
type TokenBucket struct {
    // Atomic for lock-free reads by DataGenerator
    tokens      atomic.Int64   // Current available tokens (in bytes)
    rate        atomic.Int64   // Refill rate (bytes per second)

    // Configuration
    maxTokens   int64          // Bucket capacity (allows small bursts)

    // Refill timing
    mu          sync.Mutex
    lastRefill  time.Time
}

func NewTokenBucket(initialRate int64) *TokenBucket {
    tb := &TokenBucket{
        maxTokens:  initialRate / 8,  // Allow 125ms burst
        lastRefill: time.Now(),
    }
    tb.rate.Store(initialRate / 8)  // Convert bps to Bps
    tb.tokens.Store(tb.maxTokens)
    return tb
}

// SetRate updates the rate (called by BitrateManager on Orchestrator command)
// Uses atomic store - safe for concurrent reads
func (tb *TokenBucket) SetRate(bitsPerSecond int64) {
    bytesPerSecond := bitsPerSecond / 8
    tb.rate.Store(bytesPerSecond)

    // Update max tokens (125ms worth of data)
    tb.mu.Lock()
    tb.maxTokens = bytesPerSecond / 8
    tb.mu.Unlock()
}

// Consume attempts to consume tokens for sending a packet
// Returns true if tokens were available, false if caller should wait
// This is the HOT PATH - must be fast
func (tb *TokenBucket) Consume(bytes int64) bool {
    // Fast path: try to consume atomically
    for {
        current := tb.tokens.Load()
        if current < bytes {
            return false  // Not enough tokens
        }
        if tb.tokens.CompareAndSwap(current, current-bytes) {
            return true  // Successfully consumed
        }
        // CAS failed, retry
    }
}

// ConsumeOrWait blocks until tokens are available (used by DataGenerator)
func (tb *TokenBucket) ConsumeOrWait(ctx context.Context, bytes int64) error {
    for {
        if tb.Consume(bytes) {
            return nil
        }

        // Calculate wait time based on current rate
        rate := tb.rate.Load()
        if rate == 0 {
            return fmt.Errorf("rate is zero")
        }

        waitTime := time.Duration(float64(bytes) / float64(rate) * float64(time.Second))
        if waitTime < time.Microsecond {
            waitTime = time.Microsecond
        }

        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-time.After(waitTime):
            // Refill and retry
            tb.refill()
        }
    }
}

// refill adds tokens based on elapsed time
func (tb *TokenBucket) refill() {
    tb.mu.Lock()
    defer tb.mu.Unlock()

    now := time.Now()
    elapsed := now.Sub(tb.lastRefill)
    tb.lastRefill = now

    rate := tb.rate.Load()
    addTokens := int64(float64(rate) * elapsed.Seconds())

    current := tb.tokens.Load()
    newTokens := current + addTokens
    if newTokens > tb.maxTokens {
        newTokens = tb.maxTokens
    }
    tb.tokens.Store(newTokens)
}

// StartRefillLoop runs background token refill (call as goroutine)
func (tb *TokenBucket) StartRefillLoop(ctx context.Context) {
    ticker := time.NewTicker(10 * time.Millisecond)  // Refill every 10ms
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            tb.refill()
        }
    }
}

// Stats returns current bucket state (for metrics)
func (tb *TokenBucket) Stats() (tokens, rate int64) {
    return tb.tokens.Load(), tb.rate.Load() * 8  // Convert back to bps
}
```

**Integration with DataGenerator**:

```go
// generator.go - Data generation using token bucket

type DataGenerator struct {
    bucket     *TokenBucket
    packetSize int           // 1456 bytes (SRT payload)
    conn       *srt.Conn
}

func (g *DataGenerator) Run(ctx context.Context) error {
    packet := make([]byte, g.packetSize)

    for {
        // Wait for tokens (blocks if rate limited)
        if err := g.bucket.ConsumeOrWait(ctx, int64(g.packetSize)); err != nil {
            return err
        }

        // Generate and send packet
        g.fillPacket(packet)
        if _, err := g.conn.Write(packet); err != nil {
            return fmt.Errorf("write failed: %w", err)
        }
    }
}
```

**Key Properties**:
- **Lock-free hot path**: `Consume()` uses atomic CAS, no mutex
- **Atomic rate updates**: BitrateManager can change rate without blocking DataGenerator
- **Burst tolerance**: Allows 125ms burst to handle timing jitter
- **Precise rate**: 10ms refill interval = <1% rate error at 500 Mb/s

---

## 5. Data Generator

### 5.1 Reuse from `client-generator`

The data generation logic will be extracted from the existing `client-generator`:

```go
// Existing code in contrib/client-generator/main.go
// Will be moved to a shared package or copied with modifications

type DataGenerator struct {
    bitrate       int64           // Current target bitrate
    packetSize    int             // Typically 1456 bytes (SRT payload)
    pattern       string          // "random", "sequential", "zeros"

    // Rate control
    ticker        *time.Ticker
    tokenBucket   *TokenBucket    // For precise rate limiting
}
```

### 5.2 Token Bucket Rate Control

```
Token Bucket Algorithm:
──────────────────────
┌─────────────────────────────────────┐
│                                     │
│   ┌───────────────┐                 │
│   │ tokens: 1500  │ ← refill at     │
│   │               │   bitrate/8     │
│   └───────┬───────┘   bytes/sec     │
│           │                         │
│           ▼                         │
│   consume(packetSize)               │
│           │                         │
│           ▼                         │
│   if tokens >= packetSize:          │
│       send packet                   │
│       tokens -= packetSize          │
│   else:                             │
│       wait for refill               │
│                                     │
└─────────────────────────────────────┘
```

### 5.3 Packet Generation Modes

| Mode | Description | Use Case |
|------|-------------|----------|
| `random` | Cryptographically random data | Realistic content |
| `sequential` | Incrementing sequence | Easy debug/validation |
| `zeros` | All zeros | Minimal CPU overhead |
| `pattern` | Repeating pattern | Compression testing |

---

## 6. SRT Integration

### 6.1 Connection Management

```go
type SRTPublisher struct {
    url           string          // srt://host:port/streamid
    conn          *srt.Conn
    reconnect     bool            // Auto-reconnect on failure
    maxRetries    int             // Reconnection attempts

    // Metrics
    packetsSent   atomic.Uint64
    bytesSent     atomic.Uint64
    nakCount      atomic.Uint64
    lastError     atomic.Value    // Last error string
}
```

### 6.2 Error Handling

| Error Type | Action | Report |
|------------|--------|--------|
| Connection refused | Retry with backoff | Control socket status |
| Write timeout | Log, continue | Increment timeout counter |
| Write EOF | Attempt reconnect | Mark connection dead |
| NAK received | (handled by SRT) | Increment NAK counter |

### 6.3 SRT Configuration Passthrough

The client-seeker accepts all standard SRT configuration flags:

```bash
client-seeker \
    -to srt://127.0.0.1:6000/stream \
    -initial 200M \
    -control /tmp/seeker.sock \
    -latency 3000 \
    -rcvbuf 67108864 \
    -sndbuf 67108864 \
    -iouringenabled \
    # ... all other SRT flags
```

---

## 7. CLI Interface

### 7.1 Required Flags

| Flag | Type | Description | Example |
|------|------|-------------|---------|
| `-to` | string | SRT URL to publish to | `srt://127.0.0.1:6000/test` |
| `-initial` | string | Initial bitrate | `200M`, `500000000` |
| `-control` | string | Control socket path | `/tmp/seeker.sock` |

### 7.2 Optional Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-promuds` | string | `/tmp/seeker_metrics.sock` | Prometheus UDS path |
| `-name` | string | `client-seeker` | Instance name for metrics |
| `-pattern` | string | `random` | Data generation pattern |
| `-reconnect` | bool | `true` | Auto-reconnect on failure |
| `-minbitrate` | string | `1M` | Minimum allowed bitrate |
| `-maxbitrate` | string | `1G` | Maximum allowed bitrate |
| `-watchdog` | bool | `true` | Enable watchdog safety |
| `-watchdog-timeout` | duration | `5s` | Time without heartbeat before safe mode |
| `-watchdog-safe-bitrate` | string | `10M` | Fallback bitrate on watchdog trigger |

### 7.3 SRT Flags (Passthrough)

All flags from `client-generator` are supported:
- `-latency`, `-rcvlatency`, `-peerlatency`
- `-fc`, `-rcvbuf`, `-sndbuf`
- `-iouringenabled`, `-iouringrecvenabled`, etc.
- (See `contrib/common/flags.go` for full list)

---

## 8. Metrics

### 8.1 Prometheus Metrics

```
# Bitrate control
client_seeker_current_bitrate_bps       # Current sending bitrate
client_seeker_target_bitrate_bps        # Target bitrate (if ramping)
client_seeker_ramping                   # 1 if ramping, 0 if stable

# Data transmission
client_seeker_packets_sent_total        # Total packets sent
client_seeker_bytes_sent_total          # Total bytes sent
client_seeker_send_rate_bps             # Actual send rate (measured)

# Connection health
client_seeker_connection_alive          # 1 if connected, 0 if not
client_seeker_reconnect_count_total     # Number of reconnections
client_seeker_naks_received_total       # NAKs from receiver

# Rate limiting
client_seeker_tokens_available          # Current token bucket level
client_seeker_throttle_events_total     # Times we had to wait for tokens

# Errors
client_seeker_errors_total              # Total errors by type
```

### 8.2 Metrics Labels

```
# All metrics include these labels:
{instance="<name>", target="<srt_url>"}
```

---

## 9. File Structure

```
contrib/client-seeker/
├── main.go                 # Entry point, CLI parsing
├── control.go              # Control socket server
├── bitrate.go              # BitrateManager (atomic rate updates)
├── tokenbucket.go          # Lock-free token bucket rate limiter
├── generator.go            # Data generation (from client-generator)
├── publisher.go            # SRT connection and sending
├── watchdog.go             # Tiered watchdog with soft-landing
├── metrics.go              # Prometheus metrics
├── protocol.go             # Control message types (JSON)
└── README.md               # Usage documentation
```

---

## 10. Implementation Plan

### 10.1 Phase 1: Core Structure (Day 1-2)

- [ ] Create folder structure and `main.go`
- [ ] Implement CLI flag parsing (reuse from client-generator)
- [ ] Set up basic SRT connection (copy from client-generator)
- [ ] Implement fixed-rate data generation

**Deliverable**: Client-seeker runs at fixed bitrate (like client-generator)

### 10.2 Phase 2: Control Socket (Day 3-4)

- [ ] Implement Unix socket server
- [ ] Define JSON protocol types
- [ ] Implement `set_bitrate` command
- [ ] Implement `get_status` command
- [ ] Implement `heartbeat` command
- [ ] Add basic error handling

**Deliverable**: Can change bitrate via socket commands

### 10.3 Phase 3: Watchdog & Safety (Day 5-6)

- [ ] Implement watchdog timer
- [ ] Implement safe bitrate fallback on timeout
- [ ] Implement token bucket rate limiter
- [ ] Test orchestrator crash recovery scenario

**Deliverable**: Seeker safely handles orchestrator crashes

### 10.4 Phase 4: Metrics & Polish (Day 7)

- [ ] Add all Prometheus metrics
- [ ] Implement reconnection logic
- [ ] Add comprehensive logging
- [ ] Write README documentation
- [ ] Add integration test

**Deliverable**: Production-ready client-seeker

---

## 11. Testing Strategy

### 11.1 Unit Tests

```go
// bitrate_test.go
func TestBitrateManager_Set(t *testing.T)
func TestBitrateManager_Bounds(t *testing.T)
func TestTokenBucket_RefillRate(t *testing.T)
func TestTokenBucket_Consume(t *testing.T)

// watchdog_test.go
func TestWatchdog_TriggersOnTimeout(t *testing.T)
func TestWatchdog_ResetOnHeartbeat(t *testing.T)
func TestWatchdog_FallbackBitrate(t *testing.T)

// protocol_test.go
func TestParseSetBitrate(t *testing.T)
func TestParseHeartbeat(t *testing.T)
func TestFormatStatusResponse(t *testing.T)

// control_test.go
func TestControlSocket_Connect(t *testing.T)
func TestControlSocket_SetBitrate(t *testing.T)
func TestControlSocket_Heartbeat(t *testing.T)
```

### 11.2 Integration Tests

```bash
# Start client-seeker
./client-seeker -to srt://127.0.0.1:6000/test -initial 100M -control /tmp/test.sock &

# Send commands
echo '{"command":"get_status"}' | nc -U /tmp/test.sock
echo '{"command":"set_bitrate","bitrate":200000000}' | nc -U /tmp/test.sock
echo '{"command":"heartbeat"}' | nc -U /tmp/test.sock

# Test watchdog (don't send heartbeat for 5+ seconds)
sleep 6
echo '{"command":"get_status"}' | nc -U /tmp/test.sock  # Should show safe bitrate
```

### 11.3 Load Tests

- Rapid bitrate changes (stress test control path)
- Long-duration ramping (memory leaks)
- Connection failure/recovery (reconnect logic)

---

## 12. Example Usage

### 12.1 Basic Usage

```bash
# Start with 200 Mb/s, control via socket
./client-seeker \
    -to srt://127.0.0.1:6000/test-stream \
    -initial 200M \
    -control /tmp/seeker.sock \
    -promuds /tmp/seeker_metrics.sock \
    -name test-seeker
```

### 12.2 Control via Socket

```bash
# Using Python client
python3 -c "
import socket
import json

sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.connect('/tmp/seeker.sock')

# Get status
sock.send(json.dumps({'command': 'get_status'}).encode())
print(sock.recv(4096).decode())

# Set bitrate to 300 Mb/s
sock.send(json.dumps({'command': 'set_bitrate', 'bitrate': 300_000_000}).encode())
print(sock.recv(4096).decode())

# Ramp to 500 Mb/s over 5 seconds
sock.send(json.dumps({'command': 'ramp_to', 'bitrate': 500_000_000, 'ramp_ms': 5000}).encode())
print(sock.recv(4096).decode())

sock.close()
"
```

### 12.3 With Full SRT Configuration

```bash
./client-seeker \
    -to srt://127.0.0.1:6000/test-stream \
    -initial 200M \
    -control /tmp/seeker.sock \
    -latency 5000 \
    -rcvlatency 5000 \
    -peerlatency 5000 \
    -fc 102400 \
    -rcvbuf 67108864 \
    -sndbuf 67108864 \
    -iouringenabled \
    -usepacketring \
    -packetringsize 16384 \
    -usesendring \
    -sendringsize 8192
```

---

## 13. Open Questions

### 13.1 Design Decisions Made

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Ramping | Orchestrator handles | Keep seeker stateless and simple |
| Watchdog | Enabled by default | Prevent runaway resource usage |
| Control protocol | JSON over UDS | Readable, debuggable, sufficient performance |
| Reconnection | Auto-reconnect | Allows recovery from transient failures |

### 13.2 Remaining Questions

1. **Rate limiting precision**
   - Token bucket vs time-based throttle?
   - Trade-off: Precision vs CPU overhead

2. **Maximum concurrent control connections**
   - Single socket per client-seeker?
   - Or support multiple control connections?
   - **Recommendation**: Single connection, simpler to reason about

3. **Watchdog stop behavior**
   - Drop to safe bitrate and continue?
   - Or stop entirely after extended timeout?
   - **Recommendation**: Drop to safe, allow recovery, stop after 30s

### 13.3 Future Enhancements

- **Multiple streams**: Send to multiple SRT destinations simultaneously
- **Adaptive patterns**: Change data pattern based on command
- **Recording mode**: Log all bitrate changes to file
- **Histogram reporting**: Detailed latency/timing histograms

---

## 14. References

- **Parent**: [performance_maximization_500mbps.md](performance_maximization_500mbps.md)
- **Reuse**: [client-generator source](../contrib/client-generator/)
- **SRT Config**: [config.go](../config.go)
- **Integration Testing**: [contrib/integration_testing/](../contrib/integration_testing/)
