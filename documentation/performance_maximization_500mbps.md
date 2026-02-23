# Performance Maximization: Reaching 500 Mb/s

This document tracks the systematic effort to push gosrt throughput from the current
~300 Mb/s ceiling to 500+ Mb/s, suitable for 4K production video workflows.

## Target Use Case

**4K Production Video**:
- 4K ProRes 422 HQ: ~330 Mb/s
- 4K ProRes 4444: ~500 Mb/s
- 4K RAW (compressed): 400-800 Mb/s

**Goal**: Sustain 500 Mb/s for 60+ seconds without connection failure.

---

## NEW: Automated Performance Testing System

### Problem with Current Approach

The current isolation tests have limitations:
1. **Requires sudo** for network namespace creation
2. **Fixed bitrates** - must manually run multiple tests to find ceiling
3. **Slow iteration** - each test runs fixed duration regardless of stability
4. **Binary pass/fail** - no automatic ceiling discovery

### Solution: Self-Tuning Performance Tests

A new `./contrib/performance/` system that:
1. **No sudo required** - runs on loopback interface directly
2. **Dynamic bitrate control** - adjusts bitrate in real-time
3. **PID-like controller** - automatically seeks maximum stable throughput
4. **Fast iteration** - adapts quickly to stability/instability signals

---

## Performance Test Architecture

### Component Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                     Performance Test Runner                              │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │                    PID-like Controller                           │    │
│  │  • Monitors Prometheus metrics (gaps, NAKs, RTT, throughput)    │    │
│  │  • Detects stability (N seconds with <X% variation)             │    │
│  │  • Sends bitrate commands to client-seeker                      │    │
│  │  • Finds maximum stable throughput automatically                │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│         │                        │                        │             │
│    ▼ metrics              ▼ metrics              ▼ control msgs         │
└────┬────────────────────────┬─────────────────────────┬─────────────────┘
     │                        │                         │
     │  Prometheus UDS        │  Prometheus UDS         │  Control Socket
     │                        │                         │
┌────▼────┐             ┌─────▼─────┐            ┌──────▼──────┐
│ Server  │◄────SRT─────│  Client   │◄────SRT────│ Client-     │
│         │             │ (optional)│            │ Seeker      │
│ :6000   │             │           │            │ (Publisher) │
└─────────┘             └───────────┘            └─────────────┘
    127.0.0.1               127.0.0.2                127.0.0.3
```

### Key Components

#### 1. Client-Seeker (`./contrib/client-seeker/`)

A modified client-generator that:
- Accepts **control messages** via Unix socket to adjust bitrate
- Supports **gradual ramping** (smooth bitrate transitions)
- Reports **current bitrate** and **stability metrics**

```go
// Control message types
type ControlMessage struct {
    Command string  // "set_bitrate", "ramp_to", "get_status", "stop"
    Bitrate int64   // Target bitrate in bps
    RampMs  int64   // Ramp duration (for smooth transitions)
}

type StatusResponse struct {
    CurrentBitrate int64   // Current sending bitrate
    TargetBitrate  int64   // Target (if ramping)
    PacketsSent    uint64  // Total packets sent
    BytesSent      uint64  // Total bytes sent
    Stable         bool    // Sending at stable rate
}
```

#### 2. PID-like Bandwidth Controller

The controller monitors metrics and adjusts bitrate using a state machine:

```
                    ┌──────────────┐
                    │   SEEKING    │◄─────────────────────┐
                    │  (ramping    │                      │
                    │   up)        │                      │
                    └──────┬───────┘                      │
                           │                              │
              stability    │                              │ stability
              detected     ▼                              │ detected
                    ┌──────────────┐                      │
                    │   STABLE     │──────────────────────┘
                    │  (holding    │   after hold_time
                    │   bitrate)   │   increase bitrate
                    └──────┬───────┘
                           │
              instability  │
              detected     ▼
                    ┌──────────────┐
                    │   BACKING    │
                    │   OFF        │
                    │  (reducing   │
                    │   bitrate)   │
                    └──────┬───────┘
                           │
              stability    │
              detected     ▼
                    ┌──────────────┐
                    │  CONVERGED   │
                    │  (found max) │
                    └──────────────┘
```

**Stability Detection Criteria:**
- Gap rate < 0.1% for 5 consecutive seconds
- NAK rate < 1% of packets for 5 consecutive seconds
- Throughput variance < 5% of target for 5 seconds
- No connection errors

**Instability Detection Criteria:**
- Gap rate > 1% (immediate backoff)
- NAK rate > 5% (immediate backoff)
- Connection EOF/timeout (immediate backoff)
- Throughput < 80% of target for 3 seconds

#### 3. Performance Test Runner (`./contrib/performance/`)

Orchestrates the entire test:

```go
type PerformanceTest struct {
    // Components
    Server       *exec.Cmd
    ClientSeeker *exec.Cmd
    Client       *exec.Cmd  // optional subscriber

    // Control
    SeekerControl net.Conn  // Unix socket to client-seeker

    // Metrics collection
    ServerMetrics   *MetricsCollector
    SeekerMetrics   *MetricsCollector
    ClientMetrics   *MetricsCollector

    // Controller
    Controller *BandwidthController

    // Configuration
    Config PerformanceConfig
}

type PerformanceConfig struct {
    // Starting point
    InitialBitrate int64  // e.g., 200_000_000 (200 Mb/s)

    // Seeking parameters
    StepSize       int64  // e.g., 5_000_000 (5 Mb/s increments)
    MaxBitrate     int64  // e.g., 600_000_000 (600 Mb/s ceiling)
    MinBitrate     int64  // e.g., 50_000_000 (50 Mb/s floor)

    // Stability detection
    StabilityWindow    time.Duration  // e.g., 5 seconds
    StabilityThreshold float64        // e.g., 0.05 (5% variance)

    // Timing
    HoldTime      time.Duration  // e.g., 10 seconds (hold before stepping up)
    RampDuration  time.Duration  // e.g., 2 seconds (gradual bitrate change)
    MaxTestTime   time.Duration  // e.g., 5 minutes total

    // SRT configuration (dynamic)
    SRTConfig map[string]string  // Key-value pairs for SRT settings
}
```

---

## Dynamic Configuration System

### Problem with Static Configs

Current system uses named profiles like `CONFIG=Isolation-400M-Aggressive`:
- Must pre-define every configuration combination
- Combinatorial explosion of profiles
- Hard to experiment with specific settings

### Solution: Key-Value Configuration

New system accepts dynamic configuration:

```bash
# Old way (static profile)
make test-performance CONFIG=Isolation-400M-Aggressive

# New way (dynamic key-value)
make test-performance \
  INITIAL=200M \
  MAX=600M \
  STEP=10M \
  FC=204800 \
  RECV_RINGS=2 \
  PACKET_RING_SIZE=32768 \
  BACKOFF_MIN=1us
```

### Configuration Parser

```go
// ParseDynamicConfig converts key-value pairs to SRTConfig
func ParseDynamicConfig(args map[string]string) SRTConfig {
    config := GetSRTConfig(ConfigFullELLockFree) // Start with best baseline

    for key, value := range args {
        switch strings.ToUpper(key) {
        // Buffer settings
        case "FC":
            config.FC = parseUint32(value)
        case "RECV_BUF":
            config.RecvBuf = parseBytes(value)  // "128M" -> 134217728
        case "SEND_BUF":
            config.SendBuf = parseBytes(value)
        case "LATENCY":
            config.Latency = parseDuration(value)  // "5s" -> 5*time.Second

        // Ring settings
        case "RECV_RINGS":
            config.IoUringRecvRingCount = parseInt(value)
        case "SEND_RINGS":
            config.IoUringSendRingCount = parseInt(value)
        case "PACKET_RING_SIZE":
            config.PacketRingSize = parseInt(value)
        case "PACKET_RING_SHARDS":
            config.PacketRingShards = parseInt(value)
        case "SEND_RING_SIZE":
            config.SendRingSize = parseInt(value)

        // Timer settings
        case "TICK_MS":
            config.TickIntervalMs = parseUint64(value)
        case "NAK_MS":
            config.PeriodicNakIntervalMs = parseUint64(value)
        case "ACK_MS":
            config.PeriodicAckIntervalMs = parseUint64(value)

        // Backoff settings
        case "BACKOFF_MIN":
            config.BackoffMinSleep = parseDuration(value)
        case "BACKOFF_MAX":
            config.BackoffMaxSleep = parseDuration(value)

        // Presets (can combine with other settings)
        case "PRESET":
            switch value {
            case "aggressive":
                config = config.WithAggressiveBuffers().WithAggressiveTimers()
            case "extreme":
                config = config.WithExtremeBuffers().WithExtremeTimers()
            case "ultra":
                config = config.WithUltraHighThroughput()
            }
        }
    }

    return config
}

// Value parsers
func parseBytes(s string) uint32 {
    // "128M" -> 134217728, "1G" -> 1073741824
    s = strings.ToUpper(s)
    multiplier := uint32(1)
    if strings.HasSuffix(s, "M") {
        multiplier = 1024 * 1024
        s = s[:len(s)-1]
    } else if strings.HasSuffix(s, "G") {
        multiplier = 1024 * 1024 * 1024
        s = s[:len(s)-1]
    } else if strings.HasSuffix(s, "K") {
        multiplier = 1024
        s = s[:len(s)-1]
    }
    val, _ := strconv.ParseUint(s, 10, 32)
    return uint32(val) * multiplier
}

func parseBitrate(s string) int64 {
    // "200M" -> 200_000_000, "1G" -> 1_000_000_000
    s = strings.ToUpper(s)
    multiplier := int64(1)
    if strings.HasSuffix(s, "M") {
        multiplier = 1_000_000
        s = s[:len(s)-1]
    } else if strings.HasSuffix(s, "G") {
        multiplier = 1_000_000_000
        s = s[:len(s)-1]
    } else if strings.HasSuffix(s, "K") {
        multiplier = 1_000
        s = s[:len(s)-1]
    }
    val, _ := strconv.ParseInt(s, 10, 64)
    return val * multiplier
}
```

---

## Example Test Run

### Command

```bash
# Find maximum throughput with aggressive buffers
make test-performance \
  INITIAL=200M \
  MAX=600M \
  STEP=10M \
  PRESET=aggressive \
  RECV_RINGS=2

# Find maximum throughput with custom settings
make test-performance \
  INITIAL=300M \
  MAX=600M \
  STEP=5M \
  FC=409600 \
  RECV_BUF=256M \
  PACKET_RING_SIZE=65536 \
  BACKOFF_MIN=1us
```

### Expected Output

```
╔═══════════════════════════════════════════════════════════════════════════╗
║  PERFORMANCE TEST: Automatic Throughput Ceiling Discovery                  ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  Initial Bitrate:  200 Mb/s                                               ║
║  Maximum Target:   600 Mb/s                                               ║
║  Step Size:        10 Mb/s                                                ║
║  Stability Window: 5s                                                     ║
╚═══════════════════════════════════════════════════════════════════════════╝

Starting components on loopback interface...
  Server:        127.0.0.1:6000 ✓
  Client-Seeker: 127.0.0.3 → 127.0.0.1:6000 ✓
  Client:        127.0.0.2 → 127.0.0.1:6000 ✓

=== Phase 1: Seeking Maximum Throughput ===

[00:05] Bitrate: 200 Mb/s | Gaps: 0.00% | NAKs: 0.02% | RTT: 1.2ms | STABLE ✓
        → Increasing to 210 Mb/s

[00:12] Bitrate: 210 Mb/s | Gaps: 0.00% | NAKs: 0.03% | RTT: 1.3ms | STABLE ✓
        → Increasing to 220 Mb/s

[00:19] Bitrate: 220 Mb/s | Gaps: 0.00% | NAKs: 0.04% | RTT: 1.4ms | STABLE ✓
        → Increasing to 230 Mb/s

...

[01:45] Bitrate: 350 Mb/s | Gaps: 0.00% | NAKs: 0.15% | RTT: 2.1ms | STABLE ✓
        → Increasing to 360 Mb/s

[01:52] Bitrate: 360 Mb/s | Gaps: 0.02% | NAKs: 0.31% | RTT: 2.8ms | STABLE ✓
        → Increasing to 370 Mb/s

[01:57] Bitrate: 370 Mb/s | Gaps: 1.24% | NAKs: 2.85% | RTT: 8.5ms | UNSTABLE ✗
        → Backing off to 360 Mb/s

[02:05] Bitrate: 360 Mb/s | Gaps: 0.01% | NAKs: 0.28% | RTT: 2.6ms | STABLE ✓
        → Holding at 360 Mb/s (verifying ceiling)

[02:15] Bitrate: 360 Mb/s | Gaps: 0.00% | NAKs: 0.25% | RTT: 2.5ms | STABLE ✓

=== Phase 2: Ceiling Verification ===

[02:25] Bitrate: 360 Mb/s | STABLE for 20s | Attempting 365 Mb/s

[02:32] Bitrate: 365 Mb/s | Gaps: 0.85% | NAKs: 1.92% | UNSTABLE
        → Confirming ceiling at 360 Mb/s

=== Final Results ===

╔═══════════════════════════════════════════════════════════════════════════╗
║  MAXIMUM SUSTAINABLE THROUGHPUT: 360 Mb/s                                  ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  Test Duration:     2m 45s                                                ║
║  Stability Window:  5s                                                    ║
║  Verified Ceiling:  360 Mb/s (365 Mb/s failed)                           ║
║                                                                           ║
║  Configuration Used:                                                      ║
║    FC=204800, RecvBuf=128M, RecvRings=2, PacketRingSize=32768            ║
║                                                                           ║
║  At Maximum Throughput:                                                   ║
║    Gap Rate:    0.01%                                                    ║
║    NAK Rate:    0.25%                                                    ║
║    RTT:         2.5ms                                                    ║
║    Recovery:    100%                                                     ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

---

## Implementation Plan

### Phase 1: Client-Seeker Component

Create `./contrib/client-seeker/` with:

1. **main.go** - Entry point, CLI flags
2. **control.go** - Unix socket control interface
3. **generator.go** - Data generation (from existing client-generator)
4. **ramp.go** - Smooth bitrate ramping

**CLI Flags:**
```
-to           SRT URL to publish to
-initial      Initial bitrate (e.g., "200M")
-control      Control socket path (e.g., "/tmp/seeker_control.sock")
-promuds      Prometheus metrics socket
-name         Instance name
```

### Phase 2: Bandwidth Controller

Create `./contrib/performance/controller.go`:

1. **StabilityDetector** - Monitors metrics for stability
2. **BandwidthController** - State machine for seeking
3. **MetricsAggregator** - Combines metrics from all components

### Phase 3: Test Runner

Create `./contrib/performance/runner.go`:

1. **ProcessManager** - Starts/stops server, seeker, client
2. **ConfigParser** - Dynamic key-value configuration
3. **Reporter** - Progress and final results output

### Phase 4: Makefile Integration

```makefile
# Performance test target (no sudo required!)
.PHONY: test-performance
test-performance: contrib/performance/performance contrib/client-seeker/client-seeker
	./contrib/performance/performance $(PERF_ARGS)

# Build performance tools
contrib/performance/performance:
	cd contrib/performance && go build -o performance

contrib/client-seeker/client-seeker:
	cd contrib/client-seeker && go build -o client-seeker
```

---

## Comparison: Old vs New Approach

| Aspect | Old (Isolation Tests) | New (Performance Tests) |
|--------|----------------------|------------------------|
| **Sudo required** | Yes (network namespaces) | No (loopback only) |
| **Bitrate** | Fixed per test | Dynamic, auto-seeking |
| **Ceiling discovery** | Manual (run multiple tests) | Automatic |
| **Iteration speed** | Slow (fixed duration) | Fast (adaptive) |
| **Configuration** | Named profiles | Dynamic key-value |
| **Test duration** | Fixed (30-60s) | Adaptive (until converged) |
| **Network impairment** | Yes (netem) | No (clean path) |
| **Output** | Pass/fail | Maximum stable throughput |

---

## Folder Structure

```
./contrib/
├── client-generator/     # Existing (unchanged)
│   ├── main.go
│   └── ...
│
├── client-seeker/        # NEW: Controllable bitrate generator
│   ├── main.go           # Entry point
│   ├── control.go        # Control socket (bitrate commands)
│   ├── generator.go      # Data generation (reuse from client-generator)
│   ├── ramp.go           # Smooth bitrate ramping
│   └── README.md
│
├── performance/          # NEW: Performance test orchestrator
│   ├── main.go           # Entry point
│   ├── runner.go         # Test orchestration
│   ├── controller.go     # PID-like bandwidth controller
│   ├── config.go         # Dynamic configuration parser
│   ├── metrics.go        # Metrics collection and aggregation
│   ├── reporter.go       # Progress and results output
│   └── README.md
│
├── server/               # Existing (unchanged)
└── client/               # Existing (unchanged)
```

---

## Stability Detection Algorithm

### Metrics Monitored

```go
type StabilityMetrics struct {
    // From server
    ServerGapRate      float64  // gaps / packets received
    ServerNAKRate      float64  // NAKs sent / packets received
    ServerRetransRate  float64  // retransmissions / packets received
    ServerRTT          float64  // milliseconds
    ServerRTTVar       float64  // RTT variance

    // From client-seeker
    SeekerThroughput   float64  // actual Mb/s achieved
    SeekerTargetRate   float64  // target Mb/s
    SeekerPacketsSent  uint64
    SeekerNAKsRecv     uint64

    // Connection health
    ConnectionAlive    bool
    ErrorCount         int
}
```

### Stability Thresholds

```go
type StabilityThresholds struct {
    // Good thresholds (consider stable)
    MaxGapRate         float64  // 0.001 (0.1%)
    MaxNAKRate         float64  // 0.01 (1%)
    MaxRetransRate     float64  // 0.02 (2%)
    MaxRTTMs           float64  // 10.0 ms
    MinThroughputRatio float64  // 0.95 (95% of target)

    // Critical thresholds (immediate backoff)
    CriticalGapRate    float64  // 0.01 (1%)
    CriticalNAKRate    float64  // 0.05 (5%)
    CriticalRTTMs      float64  // 50.0 ms

    // Timing
    StabilityWindowMs  int64    // 5000 (5 seconds)
    SampleIntervalMs   int64    // 500 (0.5 seconds)
}
```

### Stability Detection State Machine

```go
type StabilityState int

const (
    StateUnknown StabilityState = iota
    StateStable
    StateUnstable
    StateCritical
)

func (d *StabilityDetector) Evaluate(m StabilityMetrics) StabilityState {
    // Critical conditions (immediate backoff)
    if m.ServerGapRate > d.thresholds.CriticalGapRate ||
       m.ServerNAKRate > d.thresholds.CriticalNAKRate ||
       m.ServerRTT > d.thresholds.CriticalRTTMs ||
       !m.ConnectionAlive {
        d.consecutiveStable = 0
        d.consecutiveUnstable++
        return StateCritical
    }

    // Unstable conditions
    if m.ServerGapRate > d.thresholds.MaxGapRate ||
       m.ServerNAKRate > d.thresholds.MaxNAKRate ||
       m.SeekerThroughput < d.thresholds.MinThroughputRatio * m.SeekerTargetRate {
        d.consecutiveStable = 0
        d.consecutiveUnstable++
        if d.consecutiveUnstable >= d.unstableThreshold {
            return StateUnstable
        }
        return StateUnknown
    }

    // Stable conditions
    d.consecutiveUnstable = 0
    d.consecutiveStable++
    if d.consecutiveStable >= d.stableThreshold {
        return StateStable
    }

    return StateUnknown
}
```

### Bandwidth Controller Algorithm

```go
func (c *BandwidthController) Run(ctx context.Context) {
    ticker := time.NewTicker(c.config.SampleInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            metrics := c.collectMetrics()
            state := c.detector.Evaluate(metrics)

            switch c.state {
            case ControllerSeeking:
                c.handleSeeking(state, metrics)
            case ControllerStable:
                c.handleStable(state, metrics)
            case ControllerBackingOff:
                c.handleBackingOff(state, metrics)
            case ControllerConverged:
                c.handleConverged(state, metrics)
            }
        }
    }
}

func (c *BandwidthController) handleSeeking(state StabilityState, m StabilityMetrics) {
    switch state {
    case StateCritical:
        // Immediate large backoff
        c.backoffBitrate(c.config.StepSize * 2)
        c.state = ControllerBackingOff
        c.log("CRITICAL: Backing off 2x step")

    case StateUnstable:
        // Normal backoff
        c.backoffBitrate(c.config.StepSize)
        c.state = ControllerBackingOff
        c.log("UNSTABLE: Backing off 1x step")

    case StateStable:
        // Mark stable and start hold timer
        c.stableAt = c.currentBitrate
        c.stableSince = time.Now()
        c.state = ControllerStable
        c.log("STABLE at %d Mb/s", c.currentBitrate/1_000_000)

    case StateUnknown:
        // Keep waiting for stability signal
    }
}

func (c *BandwidthController) handleStable(state StabilityState, m StabilityMetrics) {
    if state == StateCritical || state == StateUnstable {
        c.backoffBitrate(c.config.StepSize)
        c.state = ControllerBackingOff
        return
    }

    // Check if we've held stable long enough
    if time.Since(c.stableSince) >= c.config.HoldTime {
        if c.currentBitrate >= c.config.MaxBitrate {
            c.state = ControllerConverged
            c.log("CONVERGED at maximum: %d Mb/s", c.currentBitrate/1_000_000)
            return
        }

        // Step up and seek again
        c.increaseBitrate(c.config.StepSize)
        c.state = ControllerSeeking
        c.log("Increasing to %d Mb/s", c.currentBitrate/1_000_000)
    }
}

func (c *BandwidthController) handleBackingOff(state StabilityState, m StabilityMetrics) {
    if state == StateStable {
        // We've recovered - this is likely near the ceiling
        c.ceilingCandidate = c.currentBitrate + c.config.StepSize
        c.state = ControllerStable
        c.stableSince = time.Now()
        c.log("RECOVERED at %d Mb/s (ceiling candidate: %d Mb/s)",
            c.currentBitrate/1_000_000,
            c.ceilingCandidate/1_000_000)
    }
}
```

---

## Configuration Examples

### Quick Test (Find approximate ceiling)

```bash
make test-performance \
  INITIAL=200M \
  MAX=600M \
  STEP=20M \
  HOLD=3s \
  STABILITY=3s
```

### Precise Test (Find exact ceiling)

```bash
make test-performance \
  INITIAL=300M \
  MAX=400M \
  STEP=5M \
  HOLD=10s \
  STABILITY=5s
```

### Custom Configuration Test

```bash
make test-performance \
  INITIAL=200M \
  MAX=600M \
  STEP=10M \
  FC=409600 \
  RECV_BUF=256M \
  SEND_BUF=256M \
  PACKET_RING_SIZE=65536 \
  PACKET_RING_SHARDS=32 \
  RECV_RINGS=2 \
  BACKOFF_MIN=1us \
  TICK_MS=5
```

### A/B Configuration Comparison

```bash
# Test A: Aggressive buffers
make test-performance PRESET=aggressive NAME=test_a &> results_a.log

# Test B: Extreme buffers
make test-performance PRESET=extreme NAME=test_b &> results_b.log

# Compare
diff <(grep "MAXIMUM" results_a.log) <(grep "MAXIMUM" results_b.log)
```

---

## Implementation Timeline

### Week 1: Client-Seeker

- [ ] Create `./contrib/client-seeker/` folder structure
- [ ] Implement control socket (Unix domain socket)
- [ ] Implement bitrate ramping
- [ ] Reuse data generation from client-generator
- [ ] Add control message protocol (JSON over socket)
- [ ] Test standalone operation

### Week 2: Performance Runner

- [ ] Create `./contrib/performance/` folder structure
- [ ] Implement dynamic configuration parser
- [ ] Implement process manager (start/stop components)
- [ ] Implement metrics collection from Prometheus UDS
- [ ] Implement basic reporter (progress output)

### Week 3: Bandwidth Controller

- [ ] Implement stability detector
- [ ] Implement state machine
- [ ] Implement backoff/increase logic
- [ ] Implement convergence detection
- [ ] Test with various bitrate profiles

### Week 4: Integration & Polish

- [ ] Makefile integration
- [ ] Documentation
- [ ] Error handling improvements
- [ ] Add profiling support (optional PROFILES=cpu)
- [ ] Test on various machines

---

## Benefits Summary

1. **No sudo required** - Runs as regular user on loopback
2. **Fast iteration** - Adaptive duration based on stability
3. **Automatic ceiling discovery** - No manual binary search
4. **Dynamic configuration** - Experiment without code changes
5. **Reproducible results** - Same algorithm finds same ceiling
6. **A/B testing** - Easy comparison of different configs
7. **CI/CD friendly** - Can run in automated pipelines

---

## Questions to Resolve

1. **Ramp duration**: How fast should bitrate changes be? Instant vs gradual?
   - Instant: Faster tests, but may cause transient instability
   - Gradual (2-5 seconds): More realistic, but slower tests

2. **Hold time**: How long to wait at stable rate before stepping up?
   - Short (3s): Faster tests, but may miss slow-developing issues
   - Long (10s): More confident, but slower tests

3. **Step size**: How much to increase/decrease bitrate?
   - Large (20 Mb/s): Faster but less precise
   - Small (5 Mb/s): More precise but slower

4. **Backoff strategy**: How much to reduce on instability?
   - Same as step: May oscillate
   - 2x step: More conservative, faster convergence

5. **Client (subscriber) inclusion**: Should we test full pub/sub relay?
   - Publisher-only: Simpler, tests sender capacity
   - Full relay: More realistic, tests end-to-end

---

## Current State (2026-01-16)

### Throughput Ceiling

| Bitrate | Duration | Result | Notes |
|---------|----------|--------|-------|
| 300 Mb/s | 60s | ✅ PASS | Maximum sustainable |
| 400 Mb/s | ~4s | ❌ FAIL | Connection dies with EOF |
| 500 Mb/s | <1s | ❌ FAIL | Immediate failure |

### Current Configuration (`WithUltraHighThroughput()`)

```go
// io_uring settings
IoUringRecvRingSize:  16384   // Large receive ring
IoUringRecvBatchSize: 1024    // Large batch size

// SRT buffer settings
FC:        102400              // 4x default flow control (25600 * 4)
RecvBuf:   64 * 1024 * 1024   // 64 MB receive buffer
SendBuf:   64 * 1024 * 1024   // 64 MB send buffer
Latency:   5000 * time.Millisecond  // 5 second latency buffer

// Packet ring settings
PacketRingSize:   16384       // Large ring (131k total with 8 shards)
PacketRingShards: 8

// Send ring settings
SendRingSize:   8192
SendRingShards: 4
```

### Failure Analysis

At 400 Mb/s (~34,300 packets/second with 1456-byte payloads):
1. **Connection Duration**: 3-4 seconds before EOF
2. **Server closes "gracefully"**: Server-side closure, client gets EOF
3. **Both ring configs fail**: 2-ring and 4-ring both die
4. **Partial throughput**: Only achieving ~170-285 Mb/s actual before failure

---

## Bottleneck Hypotheses

### Hypothesis 1: Flow Control Window Exhaustion

**Theory**: The flow control window (FC=102,400 packets) fills in ~3 seconds at 500 Mb/s.
If ACKs aren't processed fast enough, the sender blocks and the connection times out.

**Evidence**:
- Connection dies after 3-4 seconds
- Server closes "gracefully" (not peer timeout)
- Flow control window at 102,400 packets = 149 MB = ~2.4s at 500 Mb/s

**Test**: Increase FC to 204,800 or higher

### Hypothesis 2: Sender EventLoop Delivery Lag

**Theory**: The sender EventLoop's `deliverReadyPacketsEventLoop()` can't iterate
through the btree fast enough at high packet rates.

**Evidence**:
- Btree iteration is O(n) for each delivery pass
- At 34,000+ packets in btree, each iteration scans thousands of nodes
- EventLoop backoff sleep may miss delivery windows

**Test**: Profile `deliverReadyPacketsEventLoop()` and measure iteration time

### Hypothesis 3: ACK Processing Latency

**Theory**: Full ACKs arrive every ~10ms, but at 500 Mb/s that's ~3,400 packets
between ACKs. If the sender can't clear packets from the btree fast enough,
memory grows unbounded.

**Evidence**:
- ACK btree cleanup happens in EventLoop
- Btree deletion is O(log n) per packet
- 3,400 deletions × O(log n) = significant CPU time

**Test**: Increase ACK frequency (reduce `PeriodicAckIntervalMs`)

### Hypothesis 4: io_uring Completion Backpressure

**Theory**: io_uring completion queues fill faster than completion handlers can drain.
This causes dropped completions or blocking.

**Evidence**:
- `IOU RcvCmp Timeout` increases with more rings at high throughput
- Multi-ring adds cross-ring reordering overhead

**Test**: Increase `IoUringRecvRingSize` further, profile completion handlers

### Hypothesis 5: Memory Allocation Pressure

**Theory**: At 500 Mb/s, packet allocation rate exceeds sync.Pool's ability to recycle
buffers, causing GC pressure that blocks the EventLoop.

**Evidence**:
- GC pauses can be 1-10ms
- At 34,000 pkt/s, even 1ms pause = 34 packets delayed

**Test**: Run with `GODEBUG=gctrace=1`, profile heap allocations

### Hypothesis 6: Lock Contention in Metrics

**Theory**: Even lock-free paths increment atomic counters. At 500 Mb/s, false sharing
or atomic contention becomes measurable.

**Evidence**:
- Many metrics incremented per-packet
- Cache line bouncing between cores

**Test**: Reduce metrics granularity, use batch increments

---

## Configuration Tuning Matrix

### Buffer & Flow Control Settings

| Parameter | Current | Conservative | Aggressive | Extreme |
|-----------|---------|--------------|------------|---------|
| `FC` | 102,400 | 204,800 | 409,600 | 819,200 |
| `RecvBuf` | 64 MB | 128 MB | 256 MB | 512 MB |
| `SendBuf` | 64 MB | 128 MB | 256 MB | 512 MB |
| `Latency` | 5,000 ms | 8,000 ms | 10,000 ms | 15,000 ms |

### Ring Buffer Settings

| Parameter | Current | Conservative | Aggressive | Extreme |
|-----------|---------|--------------|------------|---------|
| `PacketRingSize` | 16,384 | 32,768 | 65,536 | 131,072 |
| `PacketRingShards` | 8 | 16 | 32 | 64 |
| `SendRingSize` | 8,192 | 16,384 | 32,768 | 65,536 |
| `SendRingShards` | 4 | 8 | 16 | 32 |

### io_uring Settings

| Parameter | Current | Conservative | Aggressive | Extreme |
|-----------|---------|--------------|------------|---------|
| `IoUringRecvRingSize` | 16,384 | 32,768 | 65,536 | 131,072 |
| `IoUringRecvBatchSize` | 1,024 | 2,048 | 4,096 | 8,192 |
| `IoUringRecvRingCount` | 4 | 4 | 2 | 2 |
| `IoUringSendRingSize` | 64 | 256 | 512 | 1,024 |

### Timer Settings

| Parameter | Current | Conservative | Aggressive | Notes |
|-----------|---------|--------------|------------|-------|
| `TickIntervalMs` | 10 | 5 | 2 | Lower = more responsive |
| `PeriodicNakIntervalMs` | 20 | 10 | 5 | Lower = faster NAK |
| `PeriodicAckIntervalMs` | 10 | 5 | 2 | Lower = faster ACK |
| `LightACKDifference` | 64 | 128 | 256 | Higher = fewer ACKs |

### EventLoop Backoff Settings

| Parameter | Current | Conservative | Aggressive | Notes |
|-----------|---------|--------------|------------|-------|
| `BackoffMinSleep` | 10 µs | 5 µs | 1 µs | Lower = more responsive |
| `BackoffMaxSleep` | 1 ms | 500 µs | 100 µs | Lower = more CPU |
| `SendEventLoopBackoffMinSleep` | 100 µs | 50 µs | 10 µs | |
| `SendEventLoopBackoffMaxSleep` | 1 ms | 500 µs | 100 µs | |
| `SendTsbpdSleepFactor` | 0.9 | 0.95 | 0.99 | Higher = less early wake |

---

## Profiling Methodology

### Running Profiled Tests

```bash
# CPU profile (120 seconds)
PROFILES=cpu sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4

# All profiles
PROFILES=all sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4

# Specific profiles
PROFILES=cpu,heap,mutex sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4
```

### Analyzing Profiles

```bash
# Interactive web UI
go tool pprof -http=:8080 /tmp/profile_xxx/server/server_cpu.pprof

# Top functions
go tool pprof -top /tmp/profile_xxx/server/server_cpu.pprof

# Flamegraph
go tool pprof -svg /tmp/profile_xxx/server/server_cpu.pprof > flame.svg
```

### Key Functions to Profile

1. **Sender EventLoop**: `congestion/live/send/eventloop.go`
   - `EventLoop()` - main loop
   - `deliverReadyPacketsEventLoop()` - btree iteration
   - `drainRingToBtreeEventLoop()` - ring → btree
   - `processControlPacketsDelta()` - ACK/NAK handling

2. **Receiver EventLoop**: `congestion/live/receive/event_loop.go`
   - `EventLoop()` - main loop
   - `drainRingBatched()` - packet processing
   - `processControlPackets()` - ACKACK handling

3. **io_uring Handlers**: `listen_linux.go`, `dial_linux.go`, `connection_linux.go`
   - `recvCompletionHandlerIndependent()` - receive completion
   - `sendCompletionHandlerIndependent()` - send completion

4. **Btree Operations**: `google/btree`
   - Insert, Delete, Iterate operations

---

## Experiment Plan

### Phase 1: Baseline Profiling

**Goal**: Understand where time is spent at 300 Mb/s (working) vs 400 Mb/s (failing)

1. **Run 300 Mb/s with CPU profiling**:
   ```bash
   PROFILES=cpu sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4 \
     PRINT_PROM=true &> /tmp/profile-300M.log
   ```

2. **Run 400 Mb/s with CPU profiling** (will fail, but capture before failure):
   ```bash
   PROFILES=cpu sudo make test-isolation CONFIG=Isolation-400M-Ring2-vs-Ring4 \
     PRINT_PROM=true &> /tmp/profile-400M.log
   ```

3. **Compare profiles**:
   - Which functions dominate at each bitrate?
   - Where does time increase disproportionately?

### Phase 2: Buffer Scaling

**Goal**: Determine if buffer exhaustion is the primary bottleneck

1. **Create new test config with doubled buffers**:
   ```go
   // In test_configs.go
   "Isolation-400M-DoubleBuffers": {
       // FC: 204800, RecvBuf: 128MB, SendBuf: 128MB
       // PacketRingSize: 32768, etc.
   }
   ```

2. **Run and compare**:
   - Does doubling buffers extend survival time?
   - What's the new failure point?

### Phase 3: EventLoop Optimization

**Goal**: Reduce EventLoop overhead

1. **Reduce timer overhead**:
   - Set `TickIntervalMs: 5`
   - Set `PeriodicAckIntervalMs: 5`
   - Set `LightACKDifference: 256`

2. **Reduce backoff latency**:
   - Set `BackoffMinSleep: 1µs`
   - Set `BackoffMaxSleep: 100µs`

3. **Profile to verify improvement**

### Phase 4: Ring Count Optimization

**Goal**: Find optimal ring configuration for 400+ Mb/s

1. **Test single large ring vs multiple smaller rings**:
   - 1 ring × 65536 size vs 4 rings × 16384 size
   - Fewer rings = less cross-ring reordering
   - Larger ring = more headroom before overflow

2. **Compare NAK rates and connection stability**

### Phase 5: Memory Optimization

**Goal**: Reduce GC pressure

1. **Heap profile analysis**:
   ```bash
   PROFILES=heap,allocs sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4
   ```

2. **Identify allocation hotspots**:
   - Packet buffers
   - Btree nodes
   - Metrics structures

3. **Consider pre-allocation or larger pools**

---

## New Test Configurations

Add to `contrib/integration_testing/test_configs.go`:

```go
// Isolation-400M-Aggressive: Aggressive tuning for 400 Mb/s
"Isolation-400M-Aggressive": &IsolationTestConfig{
    Name:        "Isolation-400M-Aggressive",
    Description: "400 Mb/s with aggressive buffer and timer tuning",
    TestDuration: 60 * time.Second,
    Bitrate:      400_000_000,
    StatsPeriod:  10 * time.Second,

    ControlCG: GetSRTConfig(ConfigFullELLockFree).
        WithUltraHighThroughput().
        WithMultipleRecvRings(2),
    ControlServer: GetSRTConfig(ConfigFullELLockFree).
        WithUltraHighThroughput().
        WithMultipleRecvRings(2),

    TestCG: GetSRTConfig(ConfigFullELLockFree).
        WithUltraHighThroughput().
        WithMultipleRecvRings(2).
        WithAggressiveBuffers().  // NEW: Double all buffers
        WithAggressiveTimers(),   // NEW: Faster timers
    TestServer: GetSRTConfig(ConfigFullELLockFree).
        WithUltraHighThroughput().
        WithMultipleRecvRings(2).
        WithAggressiveBuffers().
        WithAggressiveTimers(),
},

// Isolation-500M-Extreme: Maximum tuning for 500 Mb/s
"Isolation-500M-Extreme": &IsolationTestConfig{
    Name:        "Isolation-500M-Extreme",
    Description: "500 Mb/s with extreme buffer and timer tuning",
    TestDuration: 60 * time.Second,
    Bitrate:      500_000_000,
    StatsPeriod:  10 * time.Second,

    // ... similar with even larger buffers
},
```

### New Helper Methods to Add

```go
// WithAggressiveBuffers increases all buffer sizes 2x
func (c SRTConfig) WithAggressiveBuffers() SRTConfig {
    c.FC = 204800                  // 2x flow control
    c.RecvBuf = 128 * 1024 * 1024  // 128 MB
    c.SendBuf = 128 * 1024 * 1024  // 128 MB
    c.PacketRingSize = 32768       // 2x packet ring
    c.PacketRingShards = 16        // 2x shards
    c.SendRingSize = 16384         // 2x send ring
    c.SendRingShards = 8           // 2x send shards
    c.IoUringRecvRingSize = 32768  // 2x io_uring recv
    c.IoUringRecvBatchSize = 2048  // 2x batch
    return c
}

// WithAggressiveTimers reduces timer intervals for faster response
func (c SRTConfig) WithAggressiveTimers() SRTConfig {
    c.TickIntervalMs = 5           // 5ms tick (was 10)
    c.PeriodicNakIntervalMs = 10   // 10ms NAK (was 20)
    c.PeriodicAckIntervalMs = 5    // 5ms ACK (was 10)
    c.BackoffMinSleep = 1 * time.Microsecond   // 1µs (was 10µs)
    c.BackoffMaxSleep = 100 * time.Microsecond // 100µs (was 1ms)
    c.SendEventLoopBackoffMinSleep = 10 * time.Microsecond
    c.SendEventLoopBackoffMaxSleep = 100 * time.Microsecond
    return c
}

// WithExtremeBuffers increases all buffer sizes 4x for 500 Mb/s target
func (c SRTConfig) WithExtremeBuffers() SRTConfig {
    c.FC = 409600                   // 4x flow control
    c.RecvBuf = 256 * 1024 * 1024   // 256 MB
    c.SendBuf = 256 * 1024 * 1024   // 256 MB
    c.PacketRingSize = 65536        // 4x packet ring
    c.PacketRingShards = 32         // 4x shards
    c.SendRingSize = 32768          // 4x send ring
    c.SendRingShards = 16           // 4x send shards
    c.IoUringRecvRingSize = 65536   // 4x io_uring recv
    c.IoUringRecvBatchSize = 4096   // 4x batch
    c.Latency = 10000 * time.Millisecond  // 10s latency buffer
    c.RecvLatency = 10000 * time.Millisecond
    c.PeerLatency = 10000 * time.Millisecond
    return c
}
```

---

## Step-by-Step Debug Plan

### Step 1: Profile Current 300 Mb/s Success Case

```bash
# Run with all profiles
PROFILES=all sudo make test-isolation CONFIG=Isolation-300M-Ring2-vs-Ring4 PRINT_PROM=true

# Examine CPU profile
go tool pprof -top /tmp/profile_*/test_server/test_server_cpu.pprof | head -30

# Look for:
# - Time in EventLoop functions
# - Time in btree operations
# - Time in io_uring handlers
```

### Step 2: Profile 400 Mb/s Failure Case

```bash
# Run and capture before failure
PROFILES=cpu sudo make test-isolation CONFIG=Isolation-400M-Ring2-vs-Ring4 PRINT_PROM=true

# Compare with 300 Mb/s:
# - Which functions grow disproportionately?
# - Where is the cliff?
```

### Step 3: Test Buffer Scaling

After adding `WithAggressiveBuffers()`:

```bash
sudo make test-isolation CONFIG=Isolation-400M-Aggressive PRINT_PROM=true &> /tmp/400M-aggressive.log
```

Expected outcomes:
- **Success**: Buffer exhaustion was the bottleneck → move to 500 Mb/s
- **Same failure**: Not buffer-related → focus on EventLoop/CPU

### Step 4: Test Timer Tuning

After adding `WithAggressiveTimers()`:

```bash
sudo make test-isolation CONFIG=Isolation-400M-Timers PRINT_PROM=true &> /tmp/400M-timers.log
```

Expected outcomes:
- **Success**: EventLoop latency was critical → document optimal timers
- **Same failure**: Not timer-related → focus on other areas

### Step 5: Test Combined Optimizations

```bash
sudo make test-isolation CONFIG=Isolation-500M-Extreme PRINT_PROM=true &> /tmp/500M-extreme.log
```

---

## Success Criteria

### 400 Mb/s Target

- [ ] Connection survives 60 seconds
- [ ] No `write: EOF` errors
- [ ] Actual throughput ≥ 380 Mb/s (95%)
- [ ] Recovery rate ≥ 99%
- [ ] NAK rate < 1% of packets

### 500 Mb/s Target

- [ ] Connection survives 60 seconds
- [ ] No `write: EOF` errors
- [ ] Actual throughput ≥ 475 Mb/s (95%)
- [ ] Recovery rate ≥ 99%
- [ ] NAK rate < 1% of packets
- [ ] CPU usage sustainable (not 100% on all cores)

---

## Progress Log

### 2026-01-16: Initial Analysis

- Documented current state (300 Mb/s ceiling)
- Identified 6 bottleneck hypotheses
- Created configuration tuning matrix
- Designed experiment plan

**Next Steps**:
1. Add `WithAggressiveBuffers()` and `WithAggressiveTimers()` helpers
2. Create new isolation test configs
3. Run Phase 1 profiling at 300 Mb/s baseline

---

## References

- `multi_iouring_design_implementation_log.md` - Multi-ring test results
- `lockless_sender_design.md` - Sender EventLoop design
- `congestion/live/send/eventloop.go` - EventLoop implementation
- `contrib/integration_testing/config.go` - Test configuration helpers
- `config.go` - All tunable parameters
