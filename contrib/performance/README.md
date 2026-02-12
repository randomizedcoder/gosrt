# Performance Testing Orchestrator

An automated performance testing tool that discovers the maximum sustainable throughput for a given SRT configuration using AIMD (Additive Increase, Multiplicative Decrease) search.

## Overview

The performance orchestrator:
- Spawns and manages server + client-seeker processes
- Uses binary search with AIMD to find maximum throughput
- Evaluates stability based on gap rate, NAK rate, RTT, and throughput efficiency
- Provides CPU monitoring during tests
- Outputs results in terminal or JSON format

## Features

- **AIMD Search Algorithm**: Binary search with additive increase, multiplicative decrease
- **Stability Gate**: Single binary oracle for stability decisions
- **Multi-Phase Probing**: Ramp → warm-up → stability evaluation
- **Watchdog Integration**: Heartbeats to seeker process
- **CPU Monitoring**: Real-time CPU usage for server and seeker
- **Bottleneck Detection**: Automated hypothesis flagging at failure points
- **JSON Output**: CI-friendly output format

## Usage

```bash
./performance [options]
```

### Search Flags

| Flag | Description |
|------|-------------|
| `-initial` | Starting bitrate (default: `200000000` = 200 Mb/s) |
| `-min-bitrate` | Minimum bitrate floor (default: `50000000` = 50 Mb/s) |
| `-max-bitrate` | Maximum bitrate ceiling (default: `600000000` = 600 Mb/s) |
| `-step` | Additive increase step (default: `10000000` = 10 Mb/s) |
| `-precision` | Search stops when high-low < precision (default: `5000000` = 5 Mb/s) |
| `-search-timeout` | Maximum search time (default: `10m`) |
| `-decrease` | Multiplicative decrease on failure (default: `0.25` = 25%) |

### Stability Flags

| Flag | Description |
|------|-------------|
| `-warmup` | Warm-up duration after bitrate change (default: `2s`) |
| `-stability-window` | Stability evaluation window (default: `5s`) |
| `-sample-interval` | Prometheus scrape interval (default: `500ms`) |
| `-max-gap-rate` | Max gap rate for stability (default: `0.01` = 1%) |
| `-max-nak-rate` | Max NAK rate for stability (default: `0.02` = 2%) |
| `-max-rtt` | Max RTT in milliseconds (default: `100`) |
| `-min-throughput` | Min throughput ratio vs target (default: `0.95` = 95%) |

### Output Flags

| Flag | Description |
|------|-------------|
| `-test-verbose` | Enable verbose output |
| `-test-json` | Output results as JSON |
| `-test-output` | Path for result output file |
| `-profile-dir` | Directory for profile captures (default: `/tmp/srt_profiles`) |
| `-status-interval` | Progress status interval (default: `5s`, `0`=disabled) |

### Control Flags

| Flag | Description |
|------|-------------|
| `-help` | Show help |
| `-version` | Show version |
| `-dry-run` | Validate config without running |

### SRT Configuration Flags

All SRT flags from `contrib/common/flags.go` are passed through to server and client-seeker:

```bash
# Example with full lock-free configuration
./performance -initial 350000000 \
  -fc 102400 -rcvbuf 67108864 \
  -iouringrecvenabled -iouringrecvringcount 2 \
  -useeventloop -usepacketring \
  -usesendbtree -usesendring -usesendcontrolring
```

## Algorithm

### AIMD Search

The search maintains two bounds:
- **low**: Last proven stable bitrate (monotonically increases)
- **high**: Last proven unstable bitrate (monotonically decreases)

```
         low                    high
          ↓                      ↓
 stable | ████████████▓▓▓▓▓▓▓▓░░░░░░░░░ | unstable
                     ↑
              search range
```

**Additive Increase**: When stable, try `current + step` (or binary midpoint if high is known)

**Multiplicative Decrease**: When unstable, back off by `decrease` factor (25% by default)

### Stability Gate

The StabilityGate acts as a binary oracle: stable or unstable.

1. **Warm-up Phase**: Ignore metrics immediately after bitrate change
2. **Evaluation Phase**: Collect Prometheus metrics over stability window
3. **Decision**: Stable if ALL criteria met, unstable otherwise

### Probe Lifecycle

```
Ramp → Record probeStart → Warm-up → Evaluate → Verdict
  │           │               │          │          │
  └───────────┴───────────────┴──────────┴──────────┘
        SearchLoop manages timing and bounds
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Performance Orchestrator                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│   ┌─────────────────┐       ┌─────────────────┐                │
│   │  ProcessManager │──────▶│     Server      │                │
│   │                 │       │  (subprocess)   │                │
│   │ - StartServer() │       └─────────────────┘                │
│   │ - StartSeeker() │                                          │
│   │ - WaitReady()   │       ┌─────────────────┐                │
│   │ - GetPIDs()     │──────▶│  Client-Seeker  │                │
│   │ - Stop()        │       │  (subprocess)   │                │
│   └─────────────────┘       └────────┬────────┘                │
│                                      │                          │
│   ┌─────────────────┐                │ Control Socket           │
│   │   SearchLoop    │◀───────────────┘                          │
│   │                 │                                           │
│   │ - Run()         │       ┌─────────────────┐                │
│   │ - rampToTarget()│◀─────▶│  SeekerControl  │                │
│   │ - nextProbe*()  │       │  (JSON/UDS)     │                │
│   │ - checkInvariants()     └─────────────────┘                │
│   └────────┬────────┘                                          │
│            │                                                    │
│            ▼                                                    │
│   ┌─────────────────┐       ┌─────────────────┐                │
│   │  StabilityGate  │◀─────▶│MetricsCollector │                │
│   │  (binary oracle)│       │ (Prometheus)    │                │
│   │                 │       └─────────────────┘                │
│   │ - Probe()       │                                          │
│   │ - warmUp()      │       ┌─────────────────┐                │
│   │ - evaluate()    │──────▶│  CPUMonitor     │                │
│   │ - isCritical()  │       │  (/proc/stat)   │                │
│   └─────────────────┘       └─────────────────┘                │
│                                                                 │
│   ┌─────────────────┐       ┌─────────────────┐                │
│   │ProgressReporter │       │   Profiler      │                │
│   │ (terminal/JSON) │       │ (cpu.pprof)     │                │
│   └─────────────────┘       └─────────────────┘                │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Examples

### Basic Performance Test

```bash
./performance
```

### High-Throughput Test

```bash
./performance -initial 400000000 -max-bitrate 600000000 \
  -fc 204800 -rcvbuf 134217728 \
  -useeventloop -usesendbtree -usesendring
```

### Verbose with JSON Output

```bash
./performance -test-verbose -test-json -test-output results.json
```

### Validate Configuration (Dry Run)

```bash
./performance -dry-run -initial 400000000 -fc 204800
```

### Copy Flags from Isolation Test

```bash
# Flags can be copy-pasted from isolation/parallel test configs
./performance -initial 350000000 \
  -fc 102400 -rcvbuf 67108864 \
  -iouringrecvenabled -iouringrecvringcount 2 \
  -useeventloop -usepacketring \
  -usesendbtree -usesendring -usesendcontrolring -sendcontrolringsize 1024
```

## Probe Verdicts

| Verdict | Meaning | Action |
|---------|---------|--------|
| `stable` | All metrics within thresholds | Increase low bound, try higher |
| `unstable` | Metrics exceeded thresholds | Decrease high bound, back off |
| `critical` | Critical thresholds exceeded | Aggressive backoff |
| `eof` | Connection died | Capture diagnostics, back off |
| `timeout` | Context cancelled | Abort search |

## Bottleneck Detection

On failure (EOF), the orchestrator performs automated hypothesis analysis:

| Hypothesis | Indicators | Recommended Action |
|------------|------------|--------------------|
| H1: FC Exhaustion | NAK spike before EOF | Increase FC, check ACK latency |
| H2: EventLoop Starvation | Low TE, no packet loss | Check cpu.pprof for delivery delays |
| H4: io_uring Backpressure | Low TE with packet loss | Increase ring sizes |
| H5: GC/Memory Pressure | High RTT variance | Check heap.pprof, run with gctrace |

## Building

```bash
# Build via Makefile
make build-performance

# Or build directly
go build -o performance ./contrib/performance
```

## Files

| File | Description |
|------|-------------|
| `main.go` | Entry point, signal handling, component wiring |
| `config.go` | Configuration types and flag parsing |
| `search.go` | AIMD search loop with monotonic bounds |
| `gate.go` | StabilityGate binary oracle |
| `process.go` | ProcessManager for server/seeker lifecycle |
| `seeker.go` | SeekerControl for JSON/UDS communication |
| `metrics.go` | MetricsCollector for Prometheus scraping |
| `cpumonitor.go` | CPUMonitor for /proc/stat polling |
| `profiler.go` | DiagnosticProfiler for pprof capture |
| `reporter.go` | ProgressReporter for terminal/JSON output |
| `timing.go` | TimingModel for timing contracts |
| `types.go` | Shared types (ProbeResult, SearchResult, etc.) |
| `interfaces.go` | Interfaces (Gate, Seeker) for testability |
