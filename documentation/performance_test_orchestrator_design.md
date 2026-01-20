# Performance Test Orchestrator Design Document

> **Parent Document**: [Performance Maximization: Reaching 500 Mb/s](performance_maximization_500mbps.md)
> **Companion Document**: [Client-Seeker Design](client_seeker_design.md)
> **Status**: Draft Outline
> **Location**: `./contrib/performance/`

---

## 1. Overview

### 1.1 Purpose

The **Performance Test Orchestrator** is an automated testing system that discovers the
maximum sustainable throughput for a given SRT configuration. Unlike fixed-bitrate
isolation tests, it:

- **Seeks maximum throughput** automatically (no manual binary search)
- **Runs without sudo** (loopback interface, no network namespaces)
- **Accepts dynamic configuration** (key-value pairs, not named profiles)
- **Uses AIMD control** (Additive Increase, Multiplicative Decrease) to find stability ceiling

### 1.2 Problem Statement

Current approach limitations (from parent document):

| Problem | Impact |
|---------|--------|
| Requires sudo | Slow iteration, not CI-friendly |
| Fixed bitrates | Manual binary search for ceiling |
| Named profiles | Combinatorial explosion of configs |
| Binary pass/fail | No ceiling discovery |

### 1.3 Solution Overview

```
Performance Test = Client-Seeker + Server + Controller + Metrics

┌─────────────────────────────────────────────────────────────────────┐
│                    Performance Orchestrator                          │
│                                                                      │
│   1. Start server and client-seeker on loopback                     │
│   2. Begin at initial bitrate (e.g., 200 Mb/s)                      │
│   3. Monitor stability metrics from Prometheus                       │
│   4. If stable: increase bitrate                                    │
│   5. If unstable: decrease bitrate                                  │
│   6. Repeat until ceiling found                                     │
│   7. Report maximum sustainable throughput                          │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 2. Architecture

### 2.1 High-Level Component Diagram

```
                                      make test-performance
                                              │
                                              ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Performance Orchestrator                             │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         Config Parser                                  │   │
│  │  INITIAL=200M MAX=600M STEP=10M FC=204800 RECV_RINGS=2 ...           │   │
│  └───────────────────────────────┬──────────────────────────────────────┘   │
│                                  │                                          │
│                                  ▼                                          │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                       Process Manager                                 │   │
│  │  • Starts server process (127.0.0.1:6000)                            │   │
│  │  • Starts client-seeker process (publisher)                          │   │
│  │  • Optionally starts client process (subscriber)                     │   │
│  └───────────────────────────────┬──────────────────────────────────────┘   │
│                                  │                                          │
│                                  ▼                                          │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                      Bandwidth Controller                             │   │
│  │                                                                       │   │
│  │  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐  │   │
│  │  │ Metrics         │───►│ Stability       │───►│ State Machine   │  │   │
│  │  │ Collector       │    │ Detector        │    │ (PID-like)      │  │   │
│  │  └─────────────────┘    └─────────────────┘    └─────────┬───────┘  │   │
│  │                                                          │          │   │
│  │                              control commands ◄──────────┘          │   │
│  │                                     │                               │   │
│  └─────────────────────────────────────┼───────────────────────────────┘   │
│                                        │                                    │
│  ┌─────────────────────────────────────▼───────────────────────────────┐   │
│  │                           Reporter                                   │   │
│  │  • Real-time progress output                                        │   │
│  │  • Final results summary                                            │   │
│  │  • Optional JSON/CSV export                                         │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
        │                    │                    │
        ▼                    ▼                    ▼
┌───────────────┐    ┌───────────────┐    ┌───────────────┐
│    Server     │    │ Client-Seeker │    │    Client     │
│ 127.0.0.1:6000│◄───│   (Publisher) │    │  (Subscriber) │
│               │    │               │    │   (optional)  │
│  Prom UDS ────┼────┼── Prom UDS ───┼────┼── Prom UDS    │
└───────────────┘    └───────────────┘    └───────────────┘
```

### 2.2 Goroutine Model

```
main()
├── configParser()           [init: parse CLI/env config]
├── processManager()         [goroutine: manages server/client-seeker/client]
│   ├── serverProcess()      [subprocess: SRT server]
│   ├── seekerProcess()      [subprocess: client-seeker]
│   └── clientProcess()      [subprocess: client (optional)]
├── metricsCollector()       [goroutine: polls Prometheus UDS sockets]
├── bandwidthController()    [goroutine: PID-like control loop]
└── reporter()               [goroutine: progress output]
```

---

## 3. Dynamic Configuration System

### 3.1 Configuration Sources

```
Priority (highest to lowest):
1. Command-line arguments (KEY=value)
2. Environment variables (PERF_KEY=value)
3. Config file (-config file.yaml)
4. Built-in defaults
```

### 3.2 Key-Value Schema

#### Test Control Parameters

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `INITIAL` | bitrate | `200M` | Starting bitrate |
| `MAX` | bitrate | `600M` | Maximum bitrate to seek |
| `MIN` | bitrate | `50M` | Minimum bitrate floor |
| `STEP` | bitrate | `10M` | Bitrate increment/decrement |
| `HOLD` | duration | `10s` | Time to hold stable before stepping |
| `STABILITY` | duration | `5s` | Window for stability detection |
| `TIMEOUT` | duration | `5m` | Maximum test duration |
| `RAMP` | duration | `2s` | Bitrate change ramp time |

#### SRT Configuration Parameters

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `FC` | integer | `102400` | Flow control window |
| `RECV_BUF` | bytes | `64M` | Receive buffer size |
| `SEND_BUF` | bytes | `64M` | Send buffer size |
| `LATENCY` | ms | `5000` | SRT latency |
| `RECV_RINGS` | integer | `2` | io_uring receive ring count |
| `SEND_RINGS` | integer | `1` | io_uring send ring count |
| `PACKET_RING_SIZE` | integer | `16384` | Packet ring buffer size |
| `PACKET_RING_SHARDS` | integer | `8` | Packet ring shards |
| `SEND_RING_SIZE` | integer | `8192` | Send ring buffer size |
| `SEND_RING_SHARDS` | integer | `4` | Send ring shards |
| `TICK_MS` | integer | `10` | EventLoop tick interval |
| `NAK_MS` | integer | `20` | Periodic NAK interval |
| `ACK_MS` | integer | `10` | Periodic ACK interval |
| `BACKOFF_MIN` | duration | `10us` | EventLoop min backoff |
| `BACKOFF_MAX` | duration | `1ms` | EventLoop max backoff |

#### Presets (Shorthand for Common Configurations)

| Key | Value | Description |
|-----|-------|-------------|
| `PRESET` | `default` | Standard configuration |
| `PRESET` | `aggressive` | Doubled buffers, faster timers |
| `PRESET` | `extreme` | Quadrupled buffers, minimal timers |
| `PRESET` | `ultra` | Maximum everything |

### 3.3 Explicit Control Contracts

To prevent timing mismatches and implicit coupling, we define formal contracts between
components:

```go
// Contract: Orchestrator → Seeker timing guarantees
type OrchestratorContract struct {
    // Ramp Cadence: Orchestrator sends set_bitrate at this interval during ramps
    RampUpdateInterval  time.Duration  // 100ms (10 updates/second)

    // Heartbeat: Orchestrator sends heartbeat at this interval
    HeartbeatInterval   time.Duration  // 2 seconds

    // Probe Duration: Minimum time at a bitrate before stability evaluation
    MinProbeDuration    time.Duration  // WarmUp + StabilityWindow (e.g., 7 seconds)
}

// Contract: Seeker guarantees to Orchestrator
type SeekerContract struct {
    // Bitrate changes are applied within this latency
    MaxApplyLatency     time.Duration  // 50ms

    // Metrics are fresh within this window
    MetricsStaleness    time.Duration  // 500ms (matches scrape interval)

    // Watchdog triggers after this duration without heartbeat
    WatchdogTimeout     time.Duration  // 5 seconds
}

// Contract: Timing invariants that MUST hold
//
// INVARIANT 1: Warm-up > 2 * RampUpdateInterval
//   Ensures last ramp update has settled before evaluation
//
// INVARIANT 2: StabilityWindow > 3 * ScrapeInterval
//   Ensures at least 3 samples in stability window
//
// INVARIANT 3: HeartbeatInterval < WatchdogTimeout / 2
//   Ensures at least 2 heartbeats before watchdog triggers
//
// INVARIANT 4: MinProbeDuration = WarmUp + StabilityWindow
//   No shortcuts - full warm-up + full evaluation window
```

**Why this matters**: Without explicit contracts, small timing drifts cause:
- False instability detection (metrics from previous bitrate)
- Oscillation near ceiling (warm-up too short)
- Non-reproducible results between runs

### 3.5 Value Parsers

```go
// Bitrate: "200M" → 200_000_000, "1G" → 1_000_000_000
func parseBitrate(s string) int64

// Bytes: "64M" → 67_108_864, "1G" → 1_073_741_824
func parseBytes(s string) uint64

// Duration: "5s" → 5*time.Second, "100ms" → 100*time.Millisecond
func parseDuration(s string) time.Duration

// Integer: "1024" → 1024
func parseInt(s string) int
```

### 3.4 Functional Options Pattern

Instead of a massive switch statement in the parser, use functional options for clean
configuration composition:

```go
// SRTOption is a functional option for SRTConfig
type SRTOption func(*SRTConfig)

// NewSRTConfig creates a config with options applied
func NewSRTConfig(opts ...SRTOption) SRTConfig {
    config := DefaultSRTConfig()  // Start with sensible defaults
    for _, opt := range opts {
        opt(&config)
    }
    return config
}

// Buffer options
func WithFC(fc uint32) SRTOption {
    return func(c *SRTConfig) { c.FC = fc }
}

func WithRecvBuf(size uint32) SRTOption {
    return func(c *SRTConfig) { c.RecvBuf = size }
}

func WithSendBuf(size uint32) SRTOption {
    return func(c *SRTConfig) { c.SendBuf = size }
}

// Ring options
func WithRecvRings(count int) SRTOption {
    return func(c *SRTConfig) { c.IoUringRecvRingCount = count }
}

func WithPacketRing(size, shards int) SRTOption {
    return func(c *SRTConfig) {
        c.PacketRingSize = size
        c.PacketRingShards = shards
    }
}

// Timer options
func WithTimers(tickMs, nakMs, ackMs uint64) SRTOption {
    return func(c *SRTConfig) {
        c.TickIntervalMs = tickMs
        c.PeriodicNakIntervalMs = nakMs
        c.PeriodicAckIntervalMs = ackMs
    }
}

// Preset options (compose multiple options)
func WithAggressivePreset() SRTOption {
    return func(c *SRTConfig) {
        WithFC(204800)(c)
        WithRecvBuf(128 * 1024 * 1024)(c)
        WithSendBuf(128 * 1024 * 1024)(c)
        WithPacketRing(32768, 16)(c)
        WithTimers(5, 10, 5)(c)
    }
}

func WithExtremePreset() SRTOption {
    return func(c *SRTConfig) {
        WithFC(409600)(c)
        WithRecvBuf(256 * 1024 * 1024)(c)
        WithSendBuf(256 * 1024 * 1024)(c)
        WithPacketRing(65536, 32)(c)
        WithTimers(2, 5, 2)(c)
    }
}
```

**Usage in runner.go:**

```go
// Clean composition - no switch statement needed
config := NewSRTConfig(
    WithAggressivePreset(),     // Start with preset
    WithFC(409600),             // Override specific values
    WithRecvRings(2),
)

// Or parse from CLI args
opts := parseArgsToOptions(args)  // Returns []SRTOption
config := NewSRTConfig(opts...)
```

### 3.6 Example Configurations

```bash
# Quick test: find approximate ceiling
make test-performance INITIAL=200M MAX=600M STEP=20M HOLD=3s

# Precise test: find exact ceiling
make test-performance INITIAL=300M MAX=400M STEP=5M HOLD=10s

# Aggressive buffers
make test-performance PRESET=aggressive FC=204800 RECV_BUF=128M

# Custom everything
make test-performance \
    INITIAL=200M MAX=600M STEP=10M \
    FC=409600 RECV_BUF=256M SEND_BUF=256M \
    PACKET_RING_SIZE=65536 PACKET_RING_SHARDS=32 \
    RECV_RINGS=2 BACKOFF_MIN=1us TICK_MS=5
```

---

## 4. Process Manager

### 4.1 Process Lifecycle

```
                        ┌────────────┐
                        │   START    │
                        └─────┬──────┘
                              │
              ┌───────────────┴───────────────┐
              ▼                               ▼
    ┌─────────────────┐             ┌─────────────────┐
    │  Start Server   │             │ Start Seeker    │
    │  wait for ready │             │ wait for ready  │
    └────────┬────────┘             └────────┬────────┘
             │                               │
             └───────────────┬───────────────┘
                             ▼
                   ┌─────────────────┐
                   │ Verify          │
                   │ Connection      │
                   └────────┬────────┘
                            │
                            ▼
                   ┌─────────────────┐
                   │   RUNNING       │◄───────────┐
                   └────────┬────────┘            │
                            │                     │
            ┌───────────────┼───────────────┐     │
            │               │               │     │
            ▼               ▼               ▼     │
       ┌────────┐     ┌────────┐     ┌────────┐  │
       │Process │     │Test    │     │Normal  │  │
       │Crash   │     │Timeout │     │Complete│  │
       └───┬────┘     └───┬────┘     └───┬────┘  │
           │              │              │       │
           ▼              ▼              ▼       │
      ┌────────┐    ┌────────────┐  ┌────────┐  │
      │Restart │───►│   STOP     │◄─│ Report │  │
      │& Retry │    │            │  │ Results│  │
      └────────┘    └────────────┘  └────────┘  │
           │                                     │
           │  retry < max_retries                │
           └─────────────────────────────────────┘
```

### 4.2 Process Configuration

```go
type ProcessConfig struct {
    // Server process
    ServerAddr      string        // "127.0.0.1:6000"
    ServerPromUDS   string        // "/tmp/perf_server.sock"
    ServerArgs      []string      // Additional CLI args

    // Client-seeker process
    SeekerTarget    string        // "srt://127.0.0.1:6000/stream"
    SeekerControlUDS string       // "/tmp/perf_seeker_control.sock"
    SeekerPromUDS   string        // "/tmp/perf_seeker.sock"
    SeekerArgs      []string      // Additional CLI args

    // Optional client process
    ClientEnabled   bool          // false by default
    ClientAddr      string        // "127.0.0.1:6001"
    ClientPromUDS   string        // "/tmp/perf_client.sock"

    // Lifecycle
    StartTimeout    time.Duration // 5 seconds
    MaxRestarts     int           // 3
}
```

### 4.3 Health Checking

```go
type HealthChecker struct {
    // Check methods
    checkServerReady()   bool    // Server accepting connections
    checkSeekerReady()   bool    // Seeker connected and sending
    checkMetricsReady()  bool    // Prometheus endpoints responding

    // Readiness probes
    readinessTimeout    time.Duration  // 10 seconds
    readinessInterval   time.Duration  // 100ms
}
```

---

## 5. Metrics Collector

### 5.1 Prometheus via UDS

The orchestrator collects metrics by scraping Prometheus endpoints exposed via Unix Domain
Sockets (UDS). This reuses the existing metrics infrastructure without modification.

**Why Prometheus text format is sufficient:**

| Factor | Value | Impact |
|--------|-------|--------|
| Poll interval | 500ms | Only 2 scrapes/second |
| Parse time | ~1ms | Negligible vs poll interval |
| CPU overhead | <0.1% | Not a bottleneck |

The controller polls metrics every 500ms, which means only 2 HTTP requests per component
per second. At this frequency, Prometheus text parsing overhead is negligible compared to
the actual SRT data path (34,000+ packets/second at 500 Mb/s).

**Note**: If future requirements demand sub-100ms polling, a binary protocol could be
considered. For now, Prometheus provides excellent debugging visibility and compatibility.

```go
type MetricsCollector struct {
    // UDS connections (reuse existing Prometheus endpoints)
    serverConn    net.Conn
    seekerConn    net.Conn
    clientConn    net.Conn  // optional

    // Scrape configuration
    scrapeInterval time.Duration  // 500ms (2 scrapes/second - not a bottleneck)

    // Parsed metrics
    serverMetrics   ServerMetrics
    seekerMetrics   SeekerMetrics
    clientMetrics   ClientMetrics

    // Aggregated stability metrics
    stability       StabilityMetrics
}
```

### 5.4 Key Metrics Collected

#### From Server

| Metric | Type | Use |
|--------|------|-----|
| `gosrt_receiver_packets_received_total` | counter | Throughput |
| `gosrt_receiver_packets_recovered_total` | counter | Recovery rate |
| `gosrt_receiver_packets_lost_total` | counter | Gap rate |
| `gosrt_receiver_naks_sent_total` | counter | NAK rate |
| `gosrt_receiver_rtt_us` | gauge | Connection health |
| `gosrt_receiver_rtt_var_us` | gauge | Connection stability |

#### From Client-Seeker

| Metric | Type | Use |
|--------|------|-----|
| `client_seeker_current_bitrate_bps` | gauge | Actual bitrate |
| `client_seeker_target_bitrate_bps` | gauge | Target bitrate |
| `client_seeker_packets_sent_total` | counter | Throughput |
| `client_seeker_naks_received_total` | counter | Sender NAKs |
| `client_seeker_connection_alive` | gauge | Health |

### 5.5 Stability Metrics Aggregation

```go
type StabilityMetrics struct {
    // Computed rates (per second)
    GapRate         float64   // gaps / total packets
    NAKRate         float64   // NAKs / total packets
    RetransRate     float64   // retransmissions / total packets
    RecoveryRate    float64   // recovered / (recovered + lost)

    // Connection health
    RTT             float64   // milliseconds
    RTTVariance     float64   // milliseconds

    // Throughput
    ActualBitrate   float64   // measured Mb/s
    TargetBitrate   float64   // target Mb/s
    ThroughputRatio float64   // actual / target

    // Connection state
    ConnectionAlive bool
    ErrorCount      int
}
```

---

## 6. Bandwidth Controller (Two-Loop Architecture)

### 6.1 Why Two Loops Instead of a Complex FSM

The original 7-state FSM (STARTING, SEEKING, HOLDING, BACKING_OFF, VERIFYING, CONVERGED,
FAILED) is correct but **fragile**. Most bugs in such systems come from:
- Forgotten state transitions
- Edge-cases after restarts
- Metrics arriving late or missing
- Race conditions between states

**Solution**: Restructure as two independent loops with clear responsibilities:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         TWO-LOOP ARCHITECTURE                                │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  LOOP 1: SEARCH LOOP (Outer)                                           │ │
│  │  ─────────────────────────────                                         │ │
│  │  • Maintains bounds: [low, high)                                       │ │
│  │  • Decides WHERE to probe next (binary search or linear)               │ │
│  │  • Monotonically narrows bounds toward ceiling                         │ │
│  │  • Only cares about: "Did probe X pass or fail?"                       │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                              │                                               │
│                              │ probe_bitrate(X)                              │
│                              ▼                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐ │
│  │  LOOP 2: STABILITY GATE (Inner)                                        │ │
│  │  ─────────────────────────────                                         │ │
│  │  • Answers ONLY: "Is bitrate X stable?" → bool                         │ │
│  │  • Handles: warm-up, metrics collection, threshold evaluation          │ │
│  │  • Returns: { stable: bool, metrics: StabilityMetrics }                │ │
│  │  • Has NO memory of previous probes                                    │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Benefits:**
- Each loop has ONE responsibility (easier to test)
- No forgotten state transitions (only 2 decisions: stable/unstable, search/done)
- Stability gate is **pure**: same bitrate + same network = same answer
- Search loop is **monotonic**: bounds only narrow, never widen

### 6.2 Stability Gate (Inner Loop)

The stability gate is a **pure function** that answers: "Is bitrate X stable?"

```go
// StabilityGate is stateless - same inputs produce same outputs
type StabilityGate struct {
    config    StabilityConfig
    collector *MetricsCollector
    seeker    *SeekerControl
}

// Probe tests whether a bitrate is stable
// This is a BLOCKING call that takes (WarmUp + StabilityWindow) time
func (g *StabilityGate) Probe(ctx context.Context, bitrate int64) ProbeResult {
    // 1. Set bitrate (instant, no ramping - that's orchestrator's job)
    g.seeker.SetBitrate(bitrate)

    // 2. Wait for warm-up (metrics from this period are IGNORED)
    select {
    case <-ctx.Done():
        return ProbeResult{Cancelled: true}
    case <-time.After(g.config.WarmUpDuration):
    }

    // 3. Collect metrics over stability window
    samples := make([]StabilityMetrics, 0, g.config.MinSamples)
    ticker := time.NewTicker(g.config.SampleInterval)
    deadline := time.Now().Add(g.config.StabilityWindow)

    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return ProbeResult{Cancelled: true}
        case <-ticker.C:
            m := g.collector.Collect()
            samples = append(samples, m)

            // Early exit on CRITICAL (no point waiting)
            if g.isCritical(m) {
                return ProbeResult{
                    Stable:   false,
                    Critical: true,
                    Metrics:  m,
                    Samples:  samples,
                }
            }
        }
    }

    // 4. Evaluate: ALL samples must pass thresholds
    stable := g.evaluateSamples(samples)

    return ProbeResult{
        Stable:  stable,
        Metrics: g.aggregate(samples),
        Samples: samples,
    }
}

type ProbeResult struct {
    Stable    bool              // All thresholds passed for full window
    Critical  bool              // Hit critical threshold (early exit)
    Cancelled bool              // Context cancelled
    Metrics   StabilityMetrics  // Aggregated metrics from probe
    Samples   []StabilityMetrics // Raw samples for debugging
}
```

**Key properties:**
- **Blocking**: Takes exactly `WarmUp + StabilityWindow` time (no shortcuts)
- **Stateless**: No memory between probes
- **Deterministic**: Same network conditions → same result
- **Early exit**: Critical failures abort immediately

### 6.3 Search Loop (Outer Loop)

The search loop maintains bounds and decides where to probe next:

```go
type SearchLoop struct {
    config    SearchConfig
    gate      *StabilityGate
    reporter  *Reporter

    // Search state (monotonic)
    low       int64   // Last known stable bitrate (starts at 0)
    high      int64   // Last known unstable bitrate (starts at MaxBitrate)

    // Result
    ceiling   int64   // Confirmed ceiling (0 until proven)
}

func (s *SearchLoop) Run(ctx context.Context) SearchResult {
    s.low = 0
    s.high = s.config.MaxBitrate

    // Start with initial probe
    current := s.config.InitialBitrate

    for {
        // Check termination conditions
        if s.high - s.low <= s.config.Precision {
            // Bounds converged - ceiling found
            return s.proveCeiling(ctx)
        }

        if time.Since(s.startTime) > s.config.Timeout {
            return SearchResult{
                Status:  StatusTimeout,
                Ceiling: s.low,  // Best known stable
            }
        }

        // Probe current bitrate
        s.reporter.ProbeStart(current)
        result := s.gate.Probe(ctx, current)
        s.reporter.ProbeEnd(current, result)

        if result.Cancelled {
            return SearchResult{Status: StatusCancelled}
        }

        // Update bounds based on result
        if result.Stable {
            // INVARIANT: low only increases
            s.low = max(s.low, current)

            // Additive increase: try higher
            current = s.nextProbeUp(current, result.Critical)
        } else {
            // INVARIANT: high only decreases
            s.high = min(s.high, current)

            // Multiplicative decrease: try lower
            current = s.nextProbeDown(current, result.Critical)
        }

        // Clamp to valid range
        current = clamp(current, s.config.MinBitrate, s.config.MaxBitrate)
    }
}

// nextProbeUp decides next bitrate after a stable probe
func (s *SearchLoop) nextProbeUp(current int64, wasCritical bool) int64 {
    // If we have an upper bound, binary search
    if s.high < s.config.MaxBitrate {
        return (s.low + s.high) / 2
    }

    // Otherwise, additive increase
    return current + s.config.StepSize
}

// nextProbeDown decides next bitrate after an unstable probe
func (s *SearchLoop) nextProbeDown(current int64, wasCritical bool) int64 {
    // Multiplicative decrease (AIMD)
    factor := s.config.DecreasePercent
    if wasCritical {
        factor = s.config.CriticalPercent
    }

    return int64(float64(current) * (1.0 - factor))
}
```

**AIMD in the search loop:**
- **Additive Increase**: When stable and no upper bound known, step up linearly
- **Multiplicative Decrease**: When unstable, back off by percentage
- **Binary Search**: When both bounds known, converge efficiently

### 6.4 Ceiling Proof Phase

Once bounds converge, we **prove** the ceiling with extended verification:

```go
func (s *SearchLoop) proveCeiling(ctx context.Context) SearchResult {
    candidate := s.low

    s.reporter.Log("Bounds converged: [%d, %d), proving ceiling at %d Mb/s",
        s.low/1e6, s.high/1e6, candidate/1e6)

    // Prove ceiling with EXTENDED stability window (2x normal)
    proofConfig := s.gate.config
    proofConfig.StabilityWindow *= 2
    proofConfig.MinSamples *= 2

    proofGate := s.gate.WithConfig(proofConfig)
    result := proofGate.Probe(ctx, candidate)

    if result.Cancelled {
        return SearchResult{Status: StatusCancelled}
    }

    if result.Stable {
        return SearchResult{
            Status:  StatusConverged,
            Ceiling: candidate,
            Metrics: result.Metrics,
            Proven:  true,
        }
    }

    // Proof failed - ceiling is lower than we thought
    // Back off and try again
    s.high = candidate
    s.low = int64(float64(candidate) * (1.0 - s.config.DecreasePercent))

    s.reporter.Log("Proof failed at %d Mb/s, continuing search", candidate/1e6)
    return s.Run(ctx)  // Continue searching
}
```

### 6.5 Monotonicity Guarantees

**Hard invariants that MUST hold:**

```go
// These invariants are checked after every probe
func (s *SearchLoop) checkInvariants() {
    // INVARIANT 1: Bounds are valid
    if s.low > s.high {
        panic("invariant violation: low > high")
    }

    // INVARIANT 2: Low is always stable (or 0)
    // If low > 0, we have proven it stable at some point

    // INVARIANT 3: High is always unstable (or MaxBitrate)
    // If high < MaxBitrate, we have proven it unstable at some point

    // INVARIANT 4: Bounds only converge (low increases, high decreases)
    // Enforced by max/min in update logic
}
```

### 6.6 Stability Detection (Used by Stability Gate)

```go
type StabilityThresholds struct {
    // Stable thresholds (must all be satisfied)
    MaxGapRate          float64   // 0.001 (0.1%)
    MaxNAKRate          float64   // 0.01 (1%)
    MaxRetransRate      float64   // 0.02 (2%)
    MaxRTTMs            float64   // 10.0 ms
    MinThroughputRatio  float64   // 0.95 (95% of target)

    // Critical thresholds (immediate backoff)
    CriticalGapRate     float64   // 0.01 (1%)
    CriticalNAKRate     float64   // 0.05 (5%)
    CriticalRTTMs       float64   // 50.0 ms

    // Timing
    StabilityWindowMs   int64     // 5000 (5 seconds)
    SampleIntervalMs    int64     // 500 (0.5 seconds)
}

type StabilityState int

const (
    StabilityUnknown  StabilityState = iota  // Not enough samples
    StabilityStable                          // All thresholds satisfied
    StabilityUnstable                        // Some thresholds exceeded
    StabilityCritical                        // Critical thresholds exceeded
)
```

### 6.7 Search Configuration

```go
type SearchConfig struct {
    // Bitrate bounds
    InitialBitrate  int64         // Starting point (e.g., 200 Mb/s)
    MinBitrate      int64         // Floor (e.g., 50 Mb/s)
    MaxBitrate      int64         // Ceiling to seek (e.g., 600 Mb/s)

    // AIMD parameters
    StepSize        int64         // Additive increase (e.g., +10 Mb/s)
    DecreasePercent float64       // Multiplicative decrease (e.g., 0.25 = -25%)
    CriticalPercent float64       // Critical backoff (e.g., 0.40 = -40%)

    // Convergence
    Precision       int64         // Stop when |high - low| < precision (e.g., 5 Mb/s)
    Timeout         time.Duration // Maximum test duration (e.g., 5 minutes)
}

type StabilityConfig struct {
    // Timing (see Section 3.3 for contracts)
    WarmUpDuration   time.Duration // 2 seconds (transient suppression)
    StabilityWindow  time.Duration // 5 seconds (evaluation window)
    SampleInterval   time.Duration // 500ms (scrape interval)
    MinSamples       int           // 10 (minimum samples for decision)

    // Thresholds
    MaxGapRate       float64       // 0.001 (0.1%)
    MaxNAKRate       float64       // 0.01 (1%)
    MaxRTTMs         float64       // 10.0 ms
    MinThroughput    float64       // 0.95 (95% of target)

    // Critical thresholds (early exit)
    CriticalGapRate  float64       // 0.01 (1%)
    CriticalNAKRate  float64       // 0.05 (5%)
}

// Defaults
var DefaultSearchConfig = SearchConfig{
    InitialBitrate:  200_000_000,
    MinBitrate:      50_000_000,
    MaxBitrate:      600_000_000,
    StepSize:        10_000_000,
    DecreasePercent: 0.25,
    CriticalPercent: 0.40,
    Precision:       5_000_000,
    Timeout:         5 * time.Minute,
}

var DefaultStabilityConfig = StabilityConfig{
    WarmUpDuration:  2 * time.Second,
    StabilityWindow: 5 * time.Second,
    SampleInterval:  500 * time.Millisecond,
    MinSamples:      10,
    MaxGapRate:      0.001,
    MaxNAKRate:      0.01,
    MaxRTTMs:        10.0,
    MinThroughput:   0.95,
    CriticalGapRate: 0.01,
    CriticalNAKRate: 0.05,
}
```

### 6.8 Dry-Run / Replay Mode

For debugging control logic without running actual tests:

```go
// ReplayMode allows testing controller logic with recorded metrics
type ReplayMode struct {
    // Pre-recorded probe results for each bitrate
    probeResults map[int64]ProbeResult
}

func (r *ReplayMode) Probe(ctx context.Context, bitrate int64) ProbeResult {
    // Return pre-recorded result (no actual network traffic)
    if result, ok := r.probeResults[bitrate]; ok {
        return result
    }
    // Default: interpolate based on nearest recorded bitrates
    return r.interpolate(bitrate)
}

// Usage:
// 1. Run real test, record all probe results to JSON
// 2. Replay with different search parameters to verify logic
// 3. Add regression tests for edge cases
```

**Benefits:**
- Test controller logic without network
- Reproduce edge cases deterministically
- Fast iteration on search algorithm changes

---

## 7. Reporter

### 7.1 Output Modes

| Mode | Description | Use Case |
|------|-------------|----------|
| `terminal` | Color-coded real-time output | Interactive use |
| `json` | Machine-readable JSON | CI/CD integration |
| `csv` | Comma-separated values | Spreadsheet analysis |
| `quiet` | Final result only | Scripting |

### 7.2 Real-Time Terminal Output

The two-loop model produces clean, predictable output:

```
╔═══════════════════════════════════════════════════════════════════════════╗
║  PERFORMANCE TEST: Two-Loop Ceiling Discovery                              ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  Configuration:                                                            ║
║    Initial: 200 Mb/s   Max: 600 Mb/s   Step: 10 Mb/s   Precision: 5 Mb/s  ║
║    WarmUp: 2s   Stability: 5s   Timeout: 5m                               ║
╚═══════════════════════════════════════════════════════════════════════════╝

Components started:
  ✓ Server:        127.0.0.1:6000 (pid 12345)
  ✓ Client-Seeker: 127.0.0.1 → 127.0.0.1:6000 (pid 12346)

════════════════════════════════════════════════════════════════════════════
 Probe#   Bitrate    Bounds [low,high)      Result    Gaps    NAKs    RTT
════════════════════════════════════════════════════════════════════════════
   1      200 Mb/s   [  0, 600) Mb/s        STABLE   0.00%   0.02%   1.2ms
   2      210 Mb/s   [200, 600) Mb/s        STABLE   0.00%   0.03%   1.3ms
   3      220 Mb/s   [210, 600) Mb/s        STABLE   0.00%   0.04%   1.4ms
   ...
  15      350 Mb/s   [340, 600) Mb/s        STABLE   0.01%   0.18%   2.2ms
  16      360 Mb/s   [350, 600) Mb/s        UNSTABLE 1.24%   2.85%   8.5ms
          → AIMD backoff: 360 * 0.75 = 270 Mb/s (bounds now [350, 360))
  17      355 Mb/s   [350, 360) Mb/s        STABLE   0.02%   0.25%   2.4ms
          → Binary search: (350+360)/2 = 355 Mb/s
  18      357 Mb/s   [355, 360) Mb/s        UNSTABLE 0.89%   1.92%   5.1ms
          → Bounds converged to precision (5 Mb/s)

════════════════════════════════════════════════════════════════════════════
 PROVING CEILING at 355 Mb/s (extended stability window: 10s)
════════════════════════════════════════════════════════════════════════════
  19      355 Mb/s   [355, 357) Mb/s        STABLE   0.01%   0.22%   2.3ms
          → CEILING PROVEN ✓
════════════════════════════════════════════════════════════════════════════

╔═══════════════════════════════════════════════════════════════════════════╗
║                           FINAL RESULTS                                    ║
╠═══════════════════════════════════════════════════════════════════════════╣
║  Maximum Sustainable Throughput: 355 Mb/s (PROVEN)                         ║
║                                                                            ║
║  Search Summary:                                                           ║
║    Total Probes:     19                                                   ║
║    Test Duration:    2m 35s                                               ║
║    Final Bounds:     [355, 357) Mb/s                                      ║
║    Precision:        5 Mb/s (achieved: 2 Mb/s)                            ║
║                                                                            ║
║  At Ceiling (355 Mb/s):                                                   ║
║    Gap Rate:     0.01%                                                    ║
║    NAK Rate:     0.22%                                                    ║
║    RTT:          2.3ms                                                    ║
║    Throughput:   99.2% of target                                          ║
║                                                                            ║
║  Failure Point (357 Mb/s):                                                ║
║    Gap Rate:     0.89%                                                    ║
║    NAK Rate:     1.92%                                                    ║
║    RTT:          5.1ms                                                    ║
║                                                                            ║
║  Configuration Used:                                                       ║
║    FC=204800  RecvBuf=128MB  RecvRings=2  PacketRingSize=32768            ║
╚═══════════════════════════════════════════════════════════════════════════╝
```

### 7.3 JSON Output

```json
{
  "test_start": "2026-01-16T14:30:00Z",
  "test_end": "2026-01-16T14:32:35Z",
  "duration_seconds": 155,
  "status": "converged",
  "ceiling": {
    "bitrate_bps": 355000000,
    "proven": true,
    "final_bounds": {"low": 355000000, "high": 357000000}
  },
  "configuration": {
    "search": {
      "initial_bps": 200000000,
      "max_bps": 600000000,
      "step_bps": 10000000,
      "precision_bps": 5000000,
      "decrease_percent": 0.25,
      "critical_percent": 0.40
    },
    "stability": {
      "warmup_ms": 2000,
      "window_ms": 5000,
      "sample_interval_ms": 500
    },
    "srt": {
      "fc": 204800,
      "recv_buf": 134217728,
      "recv_rings": 2
    }
  },
  "probes": [
    {
      "number": 1,
      "bitrate_bps": 200000000,
      "bounds": {"low": 0, "high": 600000000},
      "result": "stable",
      "metrics": {"gap_rate": 0.0000, "nak_rate": 0.0002, "rtt_ms": 1.2}
    },
    {
      "number": 16,
      "bitrate_bps": 360000000,
      "bounds": {"low": 350000000, "high": 600000000},
      "result": "unstable",
      "metrics": {"gap_rate": 0.0124, "nak_rate": 0.0285, "rtt_ms": 8.5}
    },
    {
      "number": 19,
      "bitrate_bps": 355000000,
      "bounds": {"low": 355000000, "high": 357000000},
      "result": "stable",
      "phase": "proof",
      "metrics": {"gap_rate": 0.0001, "nak_rate": 0.0022, "rtt_ms": 2.3}
    }
  ],
  "total_probes": 19
}
```

---

## 8. Makefile Integration

### 8.1 New Targets

```makefile
# Build performance tools
.PHONY: build-performance
build-performance:
	cd contrib/client-seeker && go build -o client-seeker
	cd contrib/performance && go build -o performance

# Run performance test (no sudo required!)
.PHONY: test-performance
test-performance: build-performance
	./contrib/performance/performance $(PERF_ARGS)

# Example with arguments
# make test-performance INITIAL=200M MAX=600M STEP=10M PRESET=aggressive
```

### 8.2 Argument Passing

```makefile
# Parse KEY=VALUE pairs from command line
PERF_ARGS := $(foreach v,$(filter-out $@,$(MAKECMDGOALS)),--$(v))

# Allow KEY=VALUE on command line
%:
	@:
```

---

## 9. File Structure

```
contrib/performance/
├── main.go                 # Entry point, CLI parsing
├── config.go               # Configuration structs and parsers
├── contracts.go            # Explicit timing contracts (Section 3.3)
├── process.go              # Process manager (server, seeker, client)
├── metrics.go              # Prometheus metrics collector
├── gate.go                 # Stability Gate (inner loop - Section 6.2)
├── search.go               # Search Loop (outer loop - Section 6.3)
├── proof.go                # Ceiling proof phase (Section 6.4)
├── replay.go               # Dry-run / replay mode (Section 6.8)
├── reporter.go             # Progress and results output
├── types.go                # Shared type definitions
└── README.md               # Usage documentation
```

---

## 10. Implementation Plan (Hardened)

### 10.1 Phase 1: Foundation + Real-Time Safety (Day 1-2)

- [ ] Create folder structure and `main.go`
- [ ] Implement configuration parser (key-value → struct)
- [ ] Implement explicit timing contracts (`contracts.go`)
- [ ] Add preset support and functional options
- [ ] **Atomic Bitrate Handover**: Use `atomic.Int64` for all rate values

**Deliverable**: Can parse `make test-performance INITIAL=200M PRESET=aggressive`

**Real-Time Safety**: Ensure no partial reads during bitrate updates:
```go
// In client-seeker: BitrateManager uses atomics
type BitrateManager struct {
    target  atomic.Int64  // Orchestrator writes
    current atomic.Int64  // DataGenerator reads
}
```

### 10.2 Phase 2: Process Manager + Readiness Gates (Day 3-4)

- [ ] Implement server process spawning
- [ ] Implement client-seeker process spawning
- [ ] **Readiness Gate**: Verify Prometheus UDS responds before starting
- [ ] **Async health probes**: Non-blocking liveness checks
- [ ] Test loopback networking setup

**Deliverable**: Can start server and client-seeker, verify connection

**Readiness Gate Implementation**:
```go
func (pm *ProcessManager) WaitReady(ctx context.Context) error {
    // Don't start search until ALL components respond
    for _, endpoint := range []string{pm.serverPromUDS, pm.seekerPromUDS} {
        if err := pm.probePrometheus(ctx, endpoint); err != nil {
            return fmt.Errorf("readiness failed for %s: %w", endpoint, err)
        }
    }
    return nil
}
```

### 10.3 Phase 3: Metrics + Diagnostic Integration (Day 5-6)

- [ ] Implement Prometheus UDS scraping
- [ ] Parse relevant metrics (gaps, NAKs, RTT, throughput)
- [ ] **Throughput Efficiency (TE) metric**: `ActualBitrate / TargetBitrate`
- [ ] **High-frequency seeker polling**: 100ms status checks for early EOF detection
- [ ] Add heartbeat sending to client-seeker

**Deliverable**: Can collect metrics + detect EventLoop starvation vs network congestion

**TE Metric for Hypothesis Validation**:
```go
type DiagnosticMetrics struct {
    ThroughputEfficiency float64  // TE < 0.95 without loss = EventLoop starvation
    PacketLossRate       float64  // Loss > 0 with TE < 1 = Network congestion
}

// If TE < 0.95 AND PacketLoss == 0:
//   → Hypothesis 2 confirmed (EventLoop can't keep up)
// If TE < 0.95 AND PacketLoss > 0:
//   → Hypothesis 1/4 (Flow control or io_uring backpressure)
```

### 10.4 Phase 4: Stability Gate + Profiling Triggers (Day 7-8)

- [ ] Implement `StabilityGate.Probe()` with warm-up and evaluation
- [ ] Implement threshold checking (stable/unstable/critical)
- [ ] **Automated profiling on Critical**: Capture CPU/heap at failure point
- [ ] **High-resolution early exit**: 100ms polling for immediate EOF detection
- [ ] Add invariant validation

**Deliverable**: Stability gate with automatic diagnostic capture at failure

**See Section 10.7 for full StabilityGate implementation with profiling**

### 10.5 Phase 5: Search Loop + Convergence Memory (Day 9-10)

- [ ] Implement `SearchLoop.Run()` with bounds tracking
- [ ] Implement AIMD: additive increase + multiplicative decrease
- [ ] Implement binary search when bounds known
- [ ] **Convergence Memory**: Store last-known-good config
- [ ] **Ceiling Proof "Burn-in"**: Jitter test (±5%) for encoder simulation
- [ ] Add monotonicity invariant checks

**Deliverable**: Search loop finds and proves ceiling with dynamic stability

**Jitter Burn-in for 4K ProRes**:
```go
func (s *SearchLoop) jitterTest(ctx context.Context, ceiling int64) bool {
    // Real encoders have variable bitrate - simulate this
    jitterAmounts := []float64{1.0, 1.05, 0.95, 1.03, 0.97, 1.0}

    for _, factor := range jitterAmounts {
        testRate := int64(float64(ceiling) * factor)
        result := s.gate.Probe(ctx, testRate)
        if !result.Stable {
            return false  // Ceiling doesn't handle jitter
        }
    }
    return true
}
```

### 10.6 Phase 6: Replay + CI Regression (Day 11-12)

- [ ] Implement replay mode for testing without network
- [ ] **Comparative Replay**: A/B test different SRTConfigs
- [ ] **Performance Delta Report**: Compare ceiling vs baseline.json
- [ ] Implement terminal reporter (probe-by-probe output)
- [ ] Implement JSON output mode
- [ ] Makefile integration for CI

**Deliverable**: Production-ready orchestrator with regression detection

**CI Integration**:
```bash
# Run performance test and compare to baseline
make test-performance --baseline baseline.json --fail-on-regression 10%

# Output: "REGRESSION: Ceiling dropped from 355 Mb/s to 320 Mb/s (-10%)"
```

### 10.7 StabilityGate Implementation with Profiling Triggers

This is the core implementation that captures diagnostics at the exact moment of failure:

```go
// gate.go - Stability Gate with automatic profiling

package performance

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "runtime/pprof"
    "time"
)

type StabilityGate struct {
    config      StabilityConfig
    collector   *MetricsCollector
    seeker      *SeekerControl
    profiler    *DiagnosticProfiler

    // High-frequency polling for early EOF detection
    fastPollInterval time.Duration  // 100ms
}

type DiagnosticProfiler struct {
    outputDir     string
    captureOnCrit bool
    profiles      []string  // "cpu", "heap", "goroutine"
}

type ProbeResult struct {
    Stable       bool
    Critical     bool
    Cancelled    bool
    Metrics      StabilityMetrics
    Samples      []StabilityMetrics

    // Diagnostic data (populated on Critical)
    Diagnostics  *DiagnosticCapture
}

type DiagnosticCapture struct {
    Timestamp    time.Time
    Bitrate      int64
    ProfilePaths map[string]string  // "cpu" -> "/tmp/profiles/cpu_400M.pprof"
    Metrics      StabilityMetrics
    TEMetric     float64            // Throughput Efficiency
}

func (g *StabilityGate) Probe(ctx context.Context, bitrate int64) ProbeResult {
    // 1. Set bitrate
    if err := g.seeker.SetBitrate(bitrate); err != nil {
        return ProbeResult{Cancelled: true}
    }

    // 2. Wait for warm-up (HIGH-FREQUENCY polling for early EOF)
    if earlyFailure := g.warmUpWithFastPoll(ctx, bitrate); earlyFailure != nil {
        return *earlyFailure
    }

    // 3. Collect samples over stability window
    samples := make([]StabilityMetrics, 0, g.config.MinSamples)
    ticker := time.NewTicker(g.config.SampleInterval)
    defer ticker.Stop()

    deadline := time.Now().Add(g.config.StabilityWindow)

    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return ProbeResult{Cancelled: true}
        case <-ticker.C:
            m := g.collector.Collect()
            samples = append(samples, m)

            // Check for critical failure
            if g.isCritical(m) {
                // === AUTOMATED PROFILING TRIGGER ===
                diag := g.captureAtFailure(bitrate, m)

                return ProbeResult{
                    Stable:      false,
                    Critical:    true,
                    Metrics:     m,
                    Samples:     samples,
                    Diagnostics: diag,
                }
            }

            // Check for connection death (high-frequency)
            if !m.ConnectionAlive {
                diag := g.captureAtFailure(bitrate, m)
                return ProbeResult{
                    Stable:      false,
                    Critical:    true,
                    Metrics:     m,
                    Samples:     samples,
                    Diagnostics: diag,
                }
            }
        }
    }

    // 4. Evaluate all samples
    stable := g.evaluateSamples(samples)

    return ProbeResult{
        Stable:  stable,
        Metrics: g.aggregate(samples),
        Samples: samples,
    }
}

// warmUpWithFastPoll detects early failures during warm-up
func (g *StabilityGate) warmUpWithFastPoll(ctx context.Context, bitrate int64) *ProbeResult {
    fastTicker := time.NewTicker(g.fastPollInterval)  // 100ms
    defer fastTicker.Stop()

    deadline := time.Now().Add(g.config.WarmUpDuration)

    for time.Now().Before(deadline) {
        select {
        case <-ctx.Done():
            return &ProbeResult{Cancelled: true}
        case <-fastTicker.C:
            // Quick status check (not full Prometheus scrape)
            status, err := g.seeker.GetStatus()
            if err != nil || !status.ConnectionAlive {
                m := g.collector.Collect()
                diag := g.captureAtFailure(bitrate, m)
                return &ProbeResult{
                    Stable:      false,
                    Critical:    true,
                    Metrics:     m,
                    Diagnostics: diag,
                }
            }
        }
    }

    return nil  // Warm-up completed successfully
}

// captureAtFailure captures profiles at the exact moment of failure
func (g *StabilityGate) captureAtFailure(bitrate int64, m StabilityMetrics) *DiagnosticCapture {
    if g.profiler == nil || !g.profiler.captureOnCrit {
        return nil
    }

    capture := &DiagnosticCapture{
        Timestamp:    time.Now(),
        Bitrate:      bitrate,
        ProfilePaths: make(map[string]string),
        Metrics:      m,
        TEMetric:     m.ActualBitrate / m.TargetBitrate,
    }

    // Create output directory
    dirName := fmt.Sprintf("failure_%dM_%s",
        bitrate/1_000_000,
        time.Now().Format("20060102_150405"))
    outDir := filepath.Join(g.profiler.outputDir, dirName)
    os.MkdirAll(outDir, 0755)

    // Capture requested profiles
    for _, profileType := range g.profiler.profiles {
        path := filepath.Join(outDir, profileType+".pprof")

        switch profileType {
        case "cpu":
            // CPU profile is special - needs duration
            // For failure capture, we take a 2-second snapshot
            f, _ := os.Create(path)
            pprof.StartCPUProfile(f)
            time.Sleep(2 * time.Second)
            pprof.StopCPUProfile()
            f.Close()

        case "heap":
            f, _ := os.Create(path)
            pprof.WriteHeapProfile(f)
            f.Close()

        case "goroutine":
            f, _ := os.Create(path)
            pprof.Lookup("goroutine").WriteTo(f, 0)
            f.Close()

        case "allocs":
            f, _ := os.Create(path)
            pprof.Lookup("allocs").WriteTo(f, 0)
            f.Close()
        }

        capture.ProfilePaths[profileType] = path
    }

    // Also capture TE analysis
    g.logTEAnalysis(capture)

    return capture
}

// logTEAnalysis logs throughput efficiency analysis for hypothesis validation
func (g *StabilityGate) logTEAnalysis(capture *DiagnosticCapture) {
    te := capture.TEMetric
    m := capture.Metrics

    fmt.Printf("\n=== DIAGNOSTIC ANALYSIS at %d Mb/s ===\n", capture.Bitrate/1_000_000)
    fmt.Printf("Throughput Efficiency: %.1f%%\n", te*100)
    fmt.Printf("Gap Rate: %.3f%%\n", m.GapRate*100)
    fmt.Printf("NAK Rate: %.3f%%\n", m.NAKRate*100)
    fmt.Printf("RTT: %.2f ms\n", m.RTT)

    // Hypothesis validation
    if te < 0.95 && m.GapRate < 0.001 {
        fmt.Println("\n→ HYPOTHESIS 2 LIKELY: EventLoop starvation")
        fmt.Println("  (Low TE without packet loss indicates sender can't keep up)")
        fmt.Println("  Check: cpu.pprof for deliverReadyPacketsEventLoop() hotspot")
    } else if te < 0.95 && m.GapRate > 0.001 {
        fmt.Println("\n→ HYPOTHESIS 1/4 LIKELY: Flow control or io_uring backpressure")
        fmt.Println("  (Low TE with packet loss indicates network/protocol congestion)")
        fmt.Println("  Check: FC window size, io_uring completion queue depth")
    } else if m.RTT > 20.0 {
        fmt.Println("\n→ HYPOTHESIS 3 LIKELY: ACK processing latency")
        fmt.Println("  (High RTT indicates ACK path is slow)")
        fmt.Println("  Check: ackBtree() in cpu.pprof")
    }

    fmt.Printf("\nProfiles saved to: %v\n", capture.ProfilePaths)
    fmt.Println("============================================\n")
}

// isCritical checks if metrics exceed critical thresholds
func (g *StabilityGate) isCritical(m StabilityMetrics) bool {
    return m.GapRate > g.config.CriticalGapRate ||
           m.NAKRate > g.config.CriticalNAKRate ||
           m.RTT > g.config.CriticalRTTMs ||
           !m.ConnectionAlive
}

// evaluateSamples checks if ALL samples pass stability thresholds
func (g *StabilityGate) evaluateSamples(samples []StabilityMetrics) bool {
    if len(samples) < g.config.MinSamples {
        return false  // Not enough samples
    }

    for _, m := range samples {
        if m.GapRate > g.config.MaxGapRate ||
           m.NAKRate > g.config.MaxNAKRate ||
           m.RTT > g.config.MaxRTTMs ||
           m.ThroughputRatio < g.config.MinThroughput {
            return false
        }
    }

    return true
}

// aggregate combines samples into summary metrics
func (g *StabilityGate) aggregate(samples []StabilityMetrics) StabilityMetrics {
    if len(samples) == 0 {
        return StabilityMetrics{}
    }

    var sum StabilityMetrics
    for _, s := range samples {
        sum.GapRate += s.GapRate
        sum.NAKRate += s.NAKRate
        sum.RTT += s.RTT
        sum.ActualBitrate += s.ActualBitrate
        sum.TargetBitrate += s.TargetBitrate
    }

    n := float64(len(samples))
    return StabilityMetrics{
        GapRate:         sum.GapRate / n,
        NAKRate:         sum.NAKRate / n,
        RTT:             sum.RTT / n,
        ActualBitrate:   sum.ActualBitrate / n,
        TargetBitrate:   sum.TargetBitrate / n,
        ThroughputRatio: sum.ActualBitrate / sum.TargetBitrate,
        ConnectionAlive: true,
    }
}
```

---

## 11. Testing Strategy

### 11.1 Unit Tests

```go
// config_test.go
func TestParseBitrate(t *testing.T)      // "200M" → 200_000_000
func TestParseBytes(t *testing.T)        // "128M" → 134_217_728
func TestParseDuration(t *testing.T)     // "5s" → 5*time.Second
func TestParseConfig(t *testing.T)       // Full config parsing

// contracts_test.go
func TestContracts_WarmUpInvariant(t *testing.T)    // WarmUp > 2 * RampInterval
func TestContracts_StabilityInvariant(t *testing.T) // StabilityWindow > 3 * SampleInterval
func TestContracts_HeartbeatInvariant(t *testing.T) // HeartbeatInterval < WatchdogTimeout/2

// gate_test.go (Stability Gate - inner loop)
func TestGate_Probe_Stable(t *testing.T)            // All samples pass
func TestGate_Probe_Unstable(t *testing.T)          // Some samples fail
func TestGate_Probe_Critical_EarlyExit(t *testing.T) // Early exit on critical
func TestGate_Probe_WarmUp_IgnoresEarly(t *testing.T) // Ignores warm-up metrics
func TestGate_Probe_Cancelled(t *testing.T)          // Context cancellation

// search_test.go (Search Loop - outer loop)
func TestSearch_Monotonicity_LowOnlyIncreases(t *testing.T)
func TestSearch_Monotonicity_HighOnlyDecreases(t *testing.T)
func TestSearch_AIMD_AdditiveIncrease(t *testing.T)
func TestSearch_AIMD_MultiplicativeDecrease(t *testing.T)
func TestSearch_BinarySearch_WhenBoundsKnown(t *testing.T)
func TestSearch_Convergence(t *testing.T)

// proof_test.go (Ceiling proof)
func TestProof_ExtendedWindow(t *testing.T)
func TestProof_FailureRestartsSearch(t *testing.T)

// replay_test.go (Dry-run mode)
func TestReplay_DeterministicResults(t *testing.T)
func TestReplay_EdgeCases(t *testing.T)
```

### 11.2 Integration Tests

```bash
# Smoke test: start and stop
make test-performance INITIAL=100M MAX=150M STEP=50M TIMEOUT=30s

# Full test: find ceiling
make test-performance INITIAL=200M MAX=400M STEP=20M

# Compare presets
make test-performance PRESET=default NAME=test1 &> results1.log
make test-performance PRESET=aggressive NAME=test2 &> results2.log
```

### 11.3 Replay Testing (Key for Debugging)

```bash
# Run real test, save probe results
make test-performance INITIAL=200M MAX=400M --save-probes /tmp/probes.json

# Replay with different search parameters
make test-performance --replay /tmp/probes.json STEP=5M PRECISION=2M

# Regression test: verify same probes produce same result
make test-performance --replay testdata/regression_case_1.json
```

### 11.4 Invariant Testing

```go
// Fuzz test: random probe sequences should never violate invariants
func FuzzSearch_Invariants(f *testing.F) {
    f.Fuzz(func(t *testing.T, seed int64) {
        // Generate random stable/unstable sequence
        // Run search loop
        // Assert: low <= high at all times
        // Assert: low only increases, high only decreases
    })
}
```

---

## 12. Dependencies

### 12.1 Internal Dependencies

| Dependency | Location | Used For |
|------------|----------|----------|
| Client-Seeker | `contrib/client-seeker/` | Controllable publisher |
| Server | `contrib/server/` | SRT server (existing) |
| Client | `contrib/client/` | SRT subscriber (optional) |
| SRTConfig | `contrib/integration_testing/config.go` | Config generation |

### 12.2 External Dependencies

| Dependency | Version | Used For |
|------------|---------|----------|
| `prometheus/common` | existing in vendor | Prometheus parsing |
| `fatih/color` | TBD | Colored terminal output |

---

## 13. Open Questions

### 13.1 Design Decisions Made

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Controller | Two-loop (search + gate) | Eliminates state explosion, easier to test |
| FSM states | Eliminated | Replaced by monotonic bounds + stateless gate |
| Warm-up | Explicit in gate | Part of blocking `Probe()` call |
| Convergence | Precision-based | Stop when `high - low < precision` |
| Proof phase | 2x stability window | Extended verification before declaring ceiling |
| Replay mode | JSON probe recording | Test logic without network |

### 13.2 Remaining Questions

1. **Subscriber testing?**
   - Publisher-only: Simpler, tests sender capacity
   - Full relay: More realistic, tests end-to-end
   - Decision: Start with publisher-only, add subscriber as option

2. **Ramp vs instant bitrate changes?**
   - Instant: Faster testing, may cause transient issues
   - Gradual: More realistic, slower tests
   - Decision: Default to 2-second ramp, configurable

3. **Backoff strategy?**
   - Same as step: May oscillate
   - 2x step: More conservative
   - Decision: 1x step for normal, 2x for critical

4. **Maximum test duration?**
   - Fixed: 5 minutes default
   - Adaptive: Until converged + verification
   - Decision: Adaptive with 5-minute maximum

### 13.2 Future Enhancements

- **A/B testing mode**: Run two configs side-by-side
- **Regression testing**: Compare to baseline ceiling
- **CI/CD integration**: GitHub Actions workflow
- **Profile collection**: CPU/memory profiles at each bitrate
- **Multi-stream testing**: Multiple parallel connections

---

## 14. Example Scenarios

### 14.1 Quick Ceiling Discovery

```bash
# Find approximate ceiling in ~2 minutes
make test-performance \
    INITIAL=200M \
    MAX=600M \
    STEP=50M \
    HOLD=5s \
    STABILITY=3s
```

### 14.2 Precise Ceiling Discovery

```bash
# Find exact ceiling (±5 Mb/s)
make test-performance \
    INITIAL=300M \
    MAX=400M \
    STEP=5M \
    HOLD=15s \
    STABILITY=5s
```

### 14.3 Configuration Comparison

```bash
# Test A: Standard buffers
make test-performance PRESET=default NAME=standard > std.json

# Test B: Aggressive buffers
make test-performance PRESET=aggressive NAME=aggressive > agg.json

# Compare
jq -r '.ceiling_bitrate_bps/1e6' std.json agg.json
# Output: 300, 350 (aggressive found 50 Mb/s higher ceiling)
```

### 14.4 Custom Deep-Dive

```bash
# Test specific hypothesis: Does increasing FC help?
make test-performance \
    INITIAL=350M MAX=400M STEP=5M \
    FC=204800 RECV_BUF=128M \
    NAME=fc204800

make test-performance \
    INITIAL=350M MAX=400M STEP=5M \
    FC=409600 RECV_BUF=256M \
    NAME=fc409600
```

---

## 15. References

- **Parent**: [performance_maximization_500mbps.md](performance_maximization_500mbps.md)
- **Companion**: [client_seeker_design.md](client_seeker_design.md)
- **Isolation Tests**: [contrib/integration_testing/](../contrib/integration_testing/)
- **Server**: [contrib/server/](../contrib/server/)
- **SRT Config**: [config.go](../config.go)
