# Lock-Free Ring Retry Strategy Integration Plan

## Overview

This document plans the integration of the new retry strategies from `go-lock-free-ring` library into gosrt. The key goal is to **avoid sleeping** in the io_uring completion handler when the ring is full, which was identified as a performance bottleneck causing RTT inflation.

**Related Documents**:
- [go-lock-free-ring DESIGN-RETRY-STRATEGIES.md](https://github.com/randomizedcoder/go-lock-free-ring/blob/main/documentation/DESIGN-RETRY-STRATEGIES.md)
- [go-lock-free-ring STRATEGY-TEST-ANALYSIS.md](https://github.com/randomizedcoder/go-lock-free-ring/blob/main/documentation/STRATEGY-TEST-ANALYSIS.md)
- [iouring_waitcqetimeout_implementation.md](./iouring_waitcqetimeout_implementation.md)
- [ack_ackack_redesign_progress.md](./ack_ackack_redesign_progress.md)
- [integration_testing_matrix_design.md](./integration_testing_matrix_design.md)
- [parallel_isolation_test_plan.md](./parallel_isolation_test_plan.md)

## Problem Statement

Currently, when the io_uring completion handler writes to the lock-free ring and the ring is full, it enters a retry loop with sleep backoff, which **blocks the entire io_uring completion goroutine**.

### Current Code Path

#### Step 1: io_uring Completion Handler Calls pushToRing()

```go
// congestion/live/receive.go:539-554
func (r *receiver) pushToRing(pkt packet.Packet) {
    // Rate metrics (always atomic - Phase 1)
    m := r.metrics
    m.RecvLightACKCounter.Add(1)
    m.RecvRatePackets.Add(1)
    m.RecvRateBytes.Add(pkt.Len())

    // Use packet sequence number for shard selection (distributes load)
    producerID := uint64(pkt.Header().PacketSequenceNumber.Val())

    if !r.packetRing.WriteWithBackoff(producerID, pkt, r.writeConfig) {
        // Ring write failed after all backoff retries - ring is persistently full
        m.RingDropsTotal.Add(1)
        r.releasePacketFully(pkt)
    }
}
```

#### Step 2: WriteConfig Initialization (Defaults)

```go
// congestion/live/receive.go:334-346
r.writeConfig = ring.WriteConfig{
    MaxRetries:      recvConfig.PacketRingMaxRetries,      // Default: 10
    BackoffDuration: recvConfig.PacketRingBackoffDuration, // Default: 100µs
    MaxBackoffs:     recvConfig.PacketRingMaxBackoffs,     // Default: 0 (unlimited!)
}
// Apply defaults if not configured
if r.writeConfig.MaxRetries <= 0 {
    r.writeConfig.MaxRetries = 10
}
if r.writeConfig.BackoffDuration <= 0 {
    r.writeConfig.BackoffDuration = 100 * time.Microsecond
}
// MaxBackoffs=0 is valid (unlimited), so no default needed
```

#### Step 3: WriteWithBackoff Retry Loop (SleepBackoff Strategy)

```go
// vendor/github.com/randomizedcoder/go-lock-free-ring/ring.go:133-156
func (r *ShardedRing) WriteWithBackoff(producerID uint64, value any, config WriteConfig) bool {
    shard := r.selectShard(producerID)  // ← ALWAYS same shard (affinity)
    backoffCount := 0

    for {
        // Try MaxRetries times before sleeping
        for retry := 0; retry < config.MaxRetries; retry++ {
            if shard.write(value) {
                return true  // Success!
            }
        }

        // All retries failed, backoff
        backoffCount++

        // Check if we've exceeded max backoffs (if limit is set)
        if config.MaxBackoffs > 0 && backoffCount >= config.MaxBackoffs {
            return false  // Give up - packet will be dropped
        }

        // ⚠️ THIS IS THE PROBLEM: Sleep blocks the io_uring completion handler!
        time.Sleep(config.BackoffDuration)  // 100µs default
    }
}
```

### Behavior Analysis

| Config | Value | Behavior |
|--------|-------|----------|
| `MaxRetries` | 10 | Try same shard 10 times quickly |
| `BackoffDuration` | 100µs | Sleep 100µs after each batch of 10 failures |
| `MaxBackoffs` | **0** (default) | **Unlimited retries - never drops!** |

**Key Insight**: With `MaxBackoffs=0` (the default), `WriteWithBackoff()` will **never return false**. It retries forever with 100µs pauses. This means:

1. **Packets are NOT dropped** (unless MaxBackoffs > 0)
2. **But the io_uring completion handler is blocked** during each 100µs sleep
3. **All subsequent completions queue up** waiting for the sleep to finish
4. **ACK/NAK packets are delayed** behind data packets in the completion queue
5. **RTT inflates** because ACK processing is delayed

### The Core Problem

The current `SleepBackoff` strategy:
- Only tries **one shard** (affinity shard based on producerID)
- When that shard is full, it **sleeps for 100µs**
- Even if **other shards have available space**!

This is inefficient because:
```
Scenario: 4 shards, shard 0 is full, shards 1-3 have space
Current behavior:
  1. Try shard 0, 10 times → all fail
  2. Sleep 100µs  ← BLOCKS io_uring completion handler!
  3. Try shard 0, 10 times → all fail
  4. Sleep 100µs  ← MORE BLOCKING!
  ... repeats until consumer drains shard 0

Better (NextShard/RandomShard):
  1. Try shard 0, 10 times → all fail
  2. Try shard 1 → SUCCESS! (no sleep needed)
```

## Solution: Configurable Retry Strategies

The go-lock-free-ring library now provides 6 retry strategies:

| Strategy | Behavior | Best For |
|----------|----------|----------|
| **SleepBackoff** (default) | Retry same shard, then sleep | Predictable latency, light loads |
| **NextShard** | Try all shards round-robin before sleeping | Bursty traffic |
| **RandomShard** | Try random shards | **Load balancing - fastest in ring benchmarks!** |
| **AdaptiveBackoff** | Exponential backoff with jitter | Sustained contention |
| **SpinThenYield** | `runtime.Gosched()` instead of sleep | Ultra-low latency, high CPU budget |
| **Hybrid** | NextShard + AdaptiveBackoff | Complex workloads |

### Important: Benchmark Results from go-lock-free-ring

In the go-lock-free-ring example code testing, **`RandomShard` was the fastest strategy**, not `NextShard`. This is because:

1. **RandomShard** spreads load across shards more evenly
2. Avoids hot-spot contention from sequential access
3. Better cache behavior with random access patterns

**However**, gosrt has additional complexity:
- io_uring completion handlers
- EventLoop processing
- ACK/NAK timing requirements
- TSBPD delivery constraints

**Therefore, we MUST test all strategies within gosrt** to determine which performs best in this specific context

---

## Implementation Plan

### Phase 1: Update Vendor

**Files**: `go.mod`, `vendor/`

1. Update go-lock-free-ring to latest version with strategies
2. Run `go mod tidy` and `go mod vendor`

```bash
cd /home/das/Downloads/srt/gosrt
go get github.com/randomizedcoder/go-lock-free-ring@latest
go mod tidy
go mod vendor
```

### Phase 2: Add Flag to flags.go

**File**: `contrib/common/flags.go` (after line 98)

```go
// Lock-free ring buffer configuration flags (Phase 3: Lockless Design)
UsePacketRing             = flag.Bool("usepacketring", false, "Enable lock-free ring buffer for packet handoff (decouples io_uring completion from Tick processing)")
PacketRingSize            = flag.Int("packetringsize", 0, "Capacity of the lock-free ring buffer per shard (must be power of 2, default: 1024)")
PacketRingShards          = flag.Int("packetringshards", 0, "Number of shards for the lock-free ring (must be power of 2, default: 4)")
PacketRingMaxRetries      = flag.Int("packetringmaxretries", -1, "Maximum immediate retries before backoff when ring is full (default: 10, -1 = not set)")
PacketRingBackoffDuration = flag.Duration("packetringbackoffduration", 0, "Delay between backoff retries when ring is full (default: 100µs)")
PacketRingMaxBackoffs     = flag.Int("packetringmaxbackoffs", -1, "Maximum backoff iterations before dropping packet (0 = unlimited, -1 = not set)")

// NEW: Ring retry strategy (Phase 5: Ring Strategies)
PacketRingRetryStrategy   = flag.String("packetringretrystrategy", "",
    "Ring write retry strategy when shard is full: "+
    "'sleep' (default, retry same shard then sleep 100µs), "+
    "'next' (try all shards before sleeping - best for io_uring), "+
    "'random' (try random shards), "+
    "'adaptive' (exponential backoff with jitter), "+
    "'spin' (yield CPU instead of sleep - lowest latency, highest CPU), "+
    "'hybrid' (next + adaptive). "+
    "Empty string uses default 'sleep' strategy.")
```

### Phase 3: Update config.go

**File**: `config.go`

Add to `ReceiveConfig` struct:

```go
// ReceiveConfig contains receiver-specific configuration
type ReceiveConfig struct {
    // ... existing fields ...

    // Lock-free ring buffer configuration (Phase 3)
    UsePacketRing             bool
    PacketRingSize            int
    PacketRingShards          int
    PacketRingMaxRetries      int
    PacketRingBackoffDuration time.Duration
    PacketRingMaxBackoffs     int

    // NEW: Ring retry strategy (Phase 5)
    // Options: "", "sleep", "next", "random", "adaptive", "spin", "hybrid"
    PacketRingRetryStrategy   string
}
```

### Phase 4: Add Strategy Parsing Helper

**File**: `congestion/live/receive.go` (new helper function)

```go
// parseRetryStrategy converts string strategy name to ring.RetryStrategy
func parseRetryStrategy(s string) ring.RetryStrategy {
    switch strings.ToLower(strings.TrimSpace(s)) {
    case "next", "nextshard":
        return ring.NextShard
    case "random", "randomshard":
        return ring.RandomShard
    case "adaptive", "adaptivebackoff":
        return ring.AdaptiveBackoff
    case "spin", "yield", "spinthenyield":
        return ring.SpinThenYield
    case "hybrid":
        return ring.Hybrid
    default:
        // "", "sleep", "sleepbackoff", or unknown -> default
        return ring.SleepBackoff
    }
}
```

### Phase 5: Update WriteConfig Initialization

**File**: `congestion/live/receive.go:334-345`

**Before**:
```go
// Configure backoff behavior for ring writes
r.writeConfig = ring.WriteConfig{
    MaxRetries:      recvConfig.PacketRingMaxRetries,
    BackoffDuration: recvConfig.PacketRingBackoffDuration,
    MaxBackoffs:     recvConfig.PacketRingMaxBackoffs,
}
```

**After**:
```go
// Configure backoff behavior for ring writes
r.writeConfig = ring.WriteConfig{
    Strategy:        parseRetryStrategy(recvConfig.PacketRingRetryStrategy),
    MaxRetries:      recvConfig.PacketRingMaxRetries,
    BackoffDuration: recvConfig.PacketRingBackoffDuration,
    MaxBackoffs:     recvConfig.PacketRingMaxBackoffs,
    // Adaptive/Hybrid strategy parameters
    MaxBackoffDuration: 10 * time.Millisecond,
    BackoffMultiplier:  2.0,
}
```

### Phase 6: Update ApplyFlags in flags.go

**File**: `contrib/common/flags.go` (in `ApplyFlags` function)

Add to the function that applies flags to config:

```go
// Apply ring retry strategy if set
if *PacketRingRetryStrategy != "" {
    config.PacketRingRetryStrategy = *PacketRingRetryStrategy
}
```

### Phase 7: Update test_flags.sh

**File**: `contrib/common/test_flags.sh`

Add test cases for all 6 strategies plus default:

```bash
# Test PacketRingRetryStrategy flag - all 6 strategies
test_flag "packetringretrystrategy" "sleep" "sleep"       # SleepBackoff (default behavior)
test_flag "packetringretrystrategy" "next" "next"         # NextShard
test_flag "packetringretrystrategy" "random" "random"     # RandomShard
test_flag "packetringretrystrategy" "adaptive" "adaptive" # AdaptiveBackoff
test_flag "packetringretrystrategy" "spin" "spin"         # SpinThenYield
test_flag "packetringretrystrategy" "hybrid" "hybrid"     # Hybrid
test_flag "packetringretrystrategy" "" ""                 # Empty (uses default SleepBackoff)
```

### Phase 8: Add Isolation Test Configurations

**File**: `contrib/integration_testing/test_configs.go`

Add new isolation test configurations for strategy comparison:

```go
// Strategy comparison isolation tests
"Isolation-5M-Strategy-Sleep": {
    Description: "Baseline: SleepBackoff strategy",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop",
        "-usepacketring",
        "-packetringretrystrategy=sleep",
        // ... other lockless flags ...
    },
},

"Isolation-5M-Strategy-NextShard": {
    Description: "NextShard strategy - should avoid most sleeps",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop",
        "-usepacketring",
        "-packetringretrystrategy=next",
        // ... other lockless flags ...
    },
},

"Isolation-5M-Strategy-Spin": {
    Description: "SpinThenYield strategy - lowest latency, highest CPU",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop",
        "-usepacketring",
        "-packetringretrystrategy=spin",
        // ... other lockless flags ...
    },
},

"Isolation-5M-Strategy-Hybrid": {
    Description: "Hybrid strategy - NextShard + AdaptiveBackoff",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop",
        "-usepacketring",
        "-packetringretrystrategy=hybrid",
        // ... other lockless flags ...
    },
},
```

### Phase 9: Add Ring Strategy Metrics

**File**: `metrics/metrics.go`

Add metrics to track strategy effectiveness:

```go
// Ring Retry Strategy Metrics (Phase 5: Ring Strategies)
// Tracks retry patterns to help tune strategy selection

RingWriteAttempts         atomic.Uint64 // Total write attempts
RingWriteFirstTrySuccess  atomic.Uint64 // Writes that succeeded on first shard
RingWriteOtherShardSuccess atomic.Uint64 // Writes that succeeded on different shard (NextShard/Random/Hybrid only)
RingWriteAfterBackoff     atomic.Uint64 // Writes that succeeded after backoff
RingWriteYields           atomic.Uint64 // Number of runtime.Gosched() calls (SpinThenYield only)
```

### Phase 10: Consider Using ring.NewWriter (Optional Optimization)

The go-lock-free-ring library provides a `Writer` type that pre-resolves the strategy function at creation time, avoiding a switch statement on every write:

```go
// In receiver initialization
if r.usePacketRing {
    // Create writer with strategy resolved once at setup
    r.packetWriter = ring.NewWriter(
        r.packetRing,
        0, // producerID - use packet seq as override
        r.writeConfig,
    )
}

// In push function - potentially faster but less flexible
// The Writer doesn't support changing producerID per-write,
// so for gosrt where we use packet sequence number as producerID,
// we continue using WriteWithBackoff for now.
```

**Note**: For gosrt, we use the packet sequence number as `producerID` for shard selection, which varies per packet. The `Writer` type assumes a fixed `producerID`, so we continue using `WriteWithBackoff` with the strategy set in `WriteConfig`.

---

## Comprehensive Testing Strategy

### Testing Philosophy

Since `RandomShard` was fastest in standalone ring benchmarks but gosrt has additional complexity (io_uring, EventLoop, ACK timing), we **must test all 6 strategies** within the full gosrt stack to determine the best performer.

### Integration with Existing Test Framework

#### 1. Add to `integration_testing_matrix_design.md` Config Variants

Add a new dimension for ring retry strategy:

```
Current Config Variants: Base, Btree, IoUr, NakBtree, NakBtreeF, NakBtreeFr, Full
New Dimension: Ring Strategy (Sleep, Next, Random, Adaptive, Spin, Hybrid)
```

However, to avoid combinatorial explosion, we test strategies **only with Full config**:

| Config Abbrev | Description | Ring Strategy |
|---------------|-------------|---------------|
| `FullSleep` | Full + SleepBackoff | Default |
| `FullNext` | Full + NextShard | Try all shards |
| `FullRandom` | Full + RandomShard | Random selection |
| `FullAdaptive` | Full + AdaptiveBackoff | Exponential |
| `FullSpin` | Full + SpinThenYield | Yield CPU |
| `FullHybrid` | Full + Hybrid | Combined |

#### 2. Integration with `parallel_isolation_test_plan.md`

Add **Phase 4: Ring Strategy Isolation Tests**:

| Test # | Name | Variable Changed | Control Config | Test Config |
|--------|------|------------------|----------------|-------------|
| 11 | Strategy-Sleep | Ring Strategy | Full + Sleep | Full + Sleep (baseline) |
| 12 | Strategy-NextShard | Ring Strategy | Full + Sleep | Full + NextShard |
| 13 | Strategy-RandomShard | Ring Strategy | Full + Sleep | Full + RandomShard |
| 14 | Strategy-AdaptiveBackoff | Ring Strategy | Full + Sleep | Full + AdaptiveBackoff |
| 15 | Strategy-SpinThenYield | Ring Strategy | Full + Sleep | Full + SpinThenYield |
| 16 | Strategy-Hybrid | Ring Strategy | Full + Sleep | Full + Hybrid |

### Test Configuration Matrix

#### Phase T1: Clean Network Strategy Comparison (No Impairment)

Isolate strategy performance without network effects:

```
Network: Clean (0% loss, 0ms RTT)
Bitrate: 5 Mb/s
Duration: 12 seconds
Control: Full + SleepBackoff
Test: Full + {each strategy}
```

| Test Name | Strategy | Expected Behavior |
|-----------|----------|-------------------|
| `Isolation-5M-Strategy-Sleep` | SleepBackoff | Baseline (current) |
| `Isolation-5M-Strategy-Next` | NextShard | Better burst handling |
| `Isolation-5M-Strategy-Random` | RandomShard | Best load distribution |
| `Isolation-5M-Strategy-Adaptive` | AdaptiveBackoff | Graceful degradation |
| `Isolation-5M-Strategy-Spin` | SpinThenYield | Lowest latency, highest CPU |
| `Isolation-5M-Strategy-Hybrid` | Hybrid | Balanced |

#### Phase T2: High Throughput Strategy Comparison

Test at higher bitrate to stress strategies:

```
Network: Clean
Bitrate: 20 Mb/s
Duration: 30 seconds
```

| Test Name | Strategy |
|-----------|----------|
| `Isolation-20M-Strategy-Sleep` | SleepBackoff |
| `Isolation-20M-Strategy-Next` | NextShard |
| `Isolation-20M-Strategy-Random` | RandomShard |
| `Isolation-20M-Strategy-Adaptive` | AdaptiveBackoff |
| `Isolation-20M-Strategy-Spin` | SpinThenYield |
| `Isolation-20M-Strategy-Hybrid` | Hybrid |

#### Phase T3: Network Impairment Strategy Tests

Test how strategies handle packet loss and latency:

```
Network: 2% loss, 60ms RTT (cross-continental)
Bitrate: 5 Mb/s
Duration: 30 seconds
```

| Test Name | Strategy |
|-----------|----------|
| `Net-L2-R60-5M-Strategy-Sleep` | SleepBackoff |
| `Net-L2-R60-5M-Strategy-Random` | RandomShard |
| `Net-L2-R60-5M-Strategy-Spin` | SpinThenYield |

### Metrics to Collect

For each strategy test, collect and compare:

| Metric | Source | Purpose |
|--------|--------|---------|
| RTT (microseconds) | `gosrt_rtt_microseconds` | Primary indicator |
| RTT Variance | `gosrt_rtt_var_microseconds` | Stability |
| Drops | `gosrt_connection_drops_total` | Ring overflow |
| Ring Write Success | (new) `gosrt_ring_write_success_total` | Strategy effectiveness |
| Ring Other Shard Success | (new) `gosrt_ring_other_shard_success_total` | NextShard/Random benefit |
| Ring Backoff Count | (new) `gosrt_ring_backoff_count_total` | Sleep frequency |
| Ring Yield Count | (new) `gosrt_ring_yield_count_total` | Spin strategy activity |
| CPU Usage | System metrics | Resource cost |
| EventLoop Iterations | `gosrt_eventloop_iterations_total` | Throughput |

### Expected Results Matrix

Based on go-lock-free-ring benchmarks and gosrt requirements:

| Strategy | RTT (expected) | Drops (expected) | CPU Usage | Best For |
|----------|----------------|------------------|-----------|----------|
| SleepBackoff | ~5ms | Higher | Low | Light loads |
| NextShard | ~1-2ms | Low | Medium | Bursty patterns |
| **RandomShard** | **~0.5ms** | **Lowest** | Medium | **General use (predicted winner)** |
| AdaptiveBackoff | ~2-5ms | Medium | Low | Sustained contention |
| SpinThenYield | ~0.1ms | Lowest | **High** | Real-time (if CPU available) |
| Hybrid | ~0.5-1ms | Low | Medium-High | Complex workloads |

**Hypothesis**: `RandomShard` will likely be the best general-purpose strategy for gosrt, based on:
1. go-lock-free-ring benchmark results
2. io_uring burst patterns benefiting from load distribution
3. Moderate CPU cost

---

## Implementation Plan

### Phase 1: Core Implementation

| Step | File | Description | Status |
|------|------|-------------|--------|
| P1.1 | `go.mod`, `vendor/` | Update go-lock-free-ring to latest | ⬜ Pending |
| P1.2 | `contrib/common/flags.go` | Add `-packetringretrystrategy` flag | ⬜ Pending |
| P1.3 | `config.go` | Add `PacketRingRetryStrategy` to ReceiveConfig | ⬜ Pending |
| P1.4 | `congestion/live/receive.go` | Add `parseRetryStrategy()` helper | ⬜ Pending |
| P1.5 | `congestion/live/receive.go` | Update WriteConfig initialization | ⬜ Pending |
| P1.6 | `contrib/common/flags.go` | Update ApplyFlags | ⬜ Pending |

### Phase 2: Unit Tests

| Step | File | Description | Status |
|------|------|-------------|--------|
| P2.1 | `contrib/common/test_flags.sh` | Add strategy flag tests | ⬜ Pending |
| P2.2 | `congestion/live/receive_test.go` | Add strategy parsing tests | ⬜ Pending |

### Phase 3: Isolation Test Configuration

| Step | File | Description | Status |
|------|------|-------------|--------|
| P3.1 | `contrib/integration_testing/test_configs.go` | Add 6 strategy isolation configs | ⬜ Pending |
| P3.2 | `contrib/integration_testing/test_isolation_mode.go` | Add strategy to output table | ⬜ Pending |
| P3.3 | `Makefile` | Add `test-isolation-strategies` target | ⬜ Pending |

### Phase 4: Integration Test Matrix

| Step | File | Description | Status |
|------|------|-------------|--------|
| P4.1 | `integration_testing_matrix_design.md` | Add strategy dimension | ⬜ Pending |
| P4.2 | `parallel_isolation_test_plan.md` | Add Phase 4: Strategy tests | ⬜ Pending |

### Phase 5: Metrics (Optional)

| Step | File | Description | Status |
|------|------|-------------|--------|
| P5.1 | `metrics/metrics.go` | Add ring strategy metrics | ⏸️ Optional |
| P5.2 | `metrics/handler.go` | Export strategy metrics | ⏸️ Optional |

---

## Verification Plan

### Phase V1: Unit Tests

```bash
# Run flags tests
./contrib/common/test_flags.sh

# Run ring-specific receiver tests
go test ./congestion/live/... -run "Ring" -v -count=1
```

### Phase V2: Integration Tests

```bash
# Run standard integration tests (should still pass with default strategy)
make test-integration-all
```

### Phase V3: Strategy Isolation Tests (Clean Network)

```bash
# Run all 6 strategy isolation tests
sudo make test-isolation CONFIG=Isolation-5M-Strategy-Sleep
sudo make test-isolation CONFIG=Isolation-5M-Strategy-Next
sudo make test-isolation CONFIG=Isolation-5M-Strategy-Random
sudo make test-isolation CONFIG=Isolation-5M-Strategy-Adaptive
sudo make test-isolation CONFIG=Isolation-5M-Strategy-Spin
sudo make test-isolation CONFIG=Isolation-5M-Strategy-Hybrid

# Or run all at once:
sudo make test-isolation-strategies
```

### Phase V4: Strategy Comparison Analysis

After running all strategy tests, compare:

1. **RTT Metrics**: Which strategy has lowest RTT and RTT variance?
2. **Drop Metrics**: Which strategy has fewest drops?
3. **CPU Usage**: Which strategies are CPU-efficient?
4. **Scalability**: How do strategies perform at 20 Mb/s vs 5 Mb/s?

### Phase V5: High Throughput Tests

```bash
# Higher bitrate tests
sudo make test-isolation CONFIG=Isolation-20M-Strategy-Sleep
sudo make test-isolation CONFIG=Isolation-20M-Strategy-Random
sudo make test-isolation CONFIG=Isolation-20M-Strategy-Spin
```

### Phase V6: Network Impairment Tests

```bash
# Test with packet loss
sudo make test-parallel CONFIG=Net-L2-R60-5M-Strategy-Random
```

---

## Implementation Progress

| Phase | Description | Status | Notes |
|-------|-------------|--------|-------|
| P1.1 | Update vendor | ⬜ Pending | |
| P1.2 | Add flag to flags.go | ⬜ Pending | |
| P1.3 | Update config.go | ⬜ Pending | |
| P1.4 | Add strategy parsing helper | ⬜ Pending | |
| P1.5 | Update WriteConfig initialization | ⬜ Pending | |
| P1.6 | Update ApplyFlags | ⬜ Pending | |
| P2.1 | Update test_flags.sh | ⬜ Pending | |
| P2.2 | Add receive_test.go tests | ⬜ Pending | |
| P3.1 | Add isolation test configs | ⬜ Pending | |
| P3.2 | Update isolation mode output | ⬜ Pending | |
| P3.3 | Add Makefile target | ⬜ Pending | |
| P4.1 | Update matrix design doc | ⬜ Pending | |
| P4.2 | Update isolation test plan doc | ⬜ Pending | |
| V1 | Unit tests | ⬜ Pending | |
| V2 | Integration tests | ⬜ Pending | |
| V3 | Strategy isolation tests | ⬜ Pending | |
| V4 | Strategy comparison analysis | ⬜ Pending | |
| V5 | High throughput tests | ⬜ Pending | |
| V6 | Network impairment tests | ⬜ Pending | |

---

## Recommended Default Strategy

### Before Testing

**Default**: `SleepBackoff` (unchanged, for backwards compatibility)

### After Testing

The recommended strategy will be determined by the comprehensive testing outlined above. Based on go-lock-free-ring benchmarks:

| Candidate | Why It Might Win |
|-----------|------------------|
| **RandomShard** | Fastest in ring benchmarks, good load distribution |
| **NextShard** | Guaranteed to try all shards, predictable |
| **SpinThenYield** | Lowest latency if CPU is available |

### Decision Criteria

After running all strategy tests, we'll select the default based on:

1. **Primary**: Lowest RTT with EventLoop enabled
2. **Secondary**: Lowest drop count
3. **Tertiary**: Reasonable CPU usage

If `RandomShard` or another strategy clearly wins, we may:
1. Change the default for io_uring + EventLoop mode
2. Auto-select strategy based on configuration

### User Configuration

Users can always override:
```bash
# Current default (backwards compatible)
-usepacketring -useeventloop

# Explicit strategy selection (after testing determines best)
-usepacketring -useeventloop -packetringretrystrategy=random

# All strategies available for user testing
-packetringretrystrategy=sleep    # SleepBackoff (current default)
-packetringretrystrategy=next     # NextShard
-packetringretrystrategy=random   # RandomShard
-packetringretrystrategy=adaptive # AdaptiveBackoff
-packetringretrystrategy=spin     # SpinThenYield
-packetringretrystrategy=hybrid   # Hybrid
```

---

## Isolation Test Configurations (test_configs.go)

Add these configurations to `contrib/integration_testing/test_configs.go`:

```go
// Phase 4: Ring Strategy Isolation Tests
// Control: Full config with SleepBackoff (current default)
// Test: Full config with each strategy

"Isolation-5M-Strategy-Sleep": {
    Description: "Baseline: Full + SleepBackoff (current default)",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        // Full config flags
        "-useeventloop", "-usepacketring",
        "-packetreorderalgorithm=btree", "-btreedegree=32",
        "-iouringenabled", "-iouringrecvenabled",
        "-usenakbtree", "-fastnakenabled", "-fastnakrecentenabled", "-honornakorder",
        // Strategy
        "-packetringretrystrategy=sleep",
    },
},

"Isolation-5M-Strategy-Next": {
    Description: "Full + NextShard - try all shards before sleeping",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop", "-usepacketring",
        "-packetreorderalgorithm=btree", "-btreedegree=32",
        "-iouringenabled", "-iouringrecvenabled",
        "-usenakbtree", "-fastnakenabled", "-fastnakrecentenabled", "-honornakorder",
        "-packetringretrystrategy=next",
    },
},

"Isolation-5M-Strategy-Random": {
    Description: "Full + RandomShard - fastest in ring benchmarks",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop", "-usepacketring",
        "-packetreorderalgorithm=btree", "-btreedegree=32",
        "-iouringenabled", "-iouringrecvenabled",
        "-usenakbtree", "-fastnakenabled", "-fastnakrecentenabled", "-honornakorder",
        "-packetringretrystrategy=random",
    },
},

"Isolation-5M-Strategy-Adaptive": {
    Description: "Full + AdaptiveBackoff - exponential backoff with jitter",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop", "-usepacketring",
        "-packetreorderalgorithm=btree", "-btreedegree=32",
        "-iouringenabled", "-iouringrecvenabled",
        "-usenakbtree", "-fastnakenabled", "-fastnakrecentenabled", "-honornakorder",
        "-packetringretrystrategy=adaptive",
    },
},

"Isolation-5M-Strategy-Spin": {
    Description: "Full + SpinThenYield - lowest latency, highest CPU",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop", "-usepacketring",
        "-packetreorderalgorithm=btree", "-btreedegree=32",
        "-iouringenabled", "-iouringrecvenabled",
        "-usenakbtree", "-fastnakenabled", "-fastnakrecentenabled", "-honornakorder",
        "-packetringretrystrategy=spin",
    },
},

"Isolation-5M-Strategy-Hybrid": {
    Description: "Full + Hybrid - NextShard + AdaptiveBackoff",
    Duration:    12 * time.Second,
    Bitrate:     5_000_000,
    ServerOnly:  true,
    TestFlags: []string{
        "-useeventloop", "-usepacketring",
        "-packetreorderalgorithm=btree", "-btreedegree=32",
        "-iouringenabled", "-iouringrecvenabled",
        "-usenakbtree", "-fastnakenabled", "-fastnakrecentenabled", "-honornakorder",
        "-packetringretrystrategy=hybrid",
    },
},
```

---

## Makefile Targets

Add to `Makefile`:

```makefile
## test-isolation-strategies: Run all ring strategy isolation tests
test-isolation-strategies: server client-generator
	@echo "=== Ring Strategy Comparison Tests ==="
	@echo "Running 6 strategy tests (12s each = ~72s total)"
	@for strategy in Sleep Next Random Adaptive Spin Hybrid; do \
		echo ""; \
		echo "=== Strategy: $$strategy ==="; \
		sudo make test-isolation CONFIG=Isolation-5M-Strategy-$$strategy; \
	done
	@echo ""
	@echo "=== Strategy Comparison Complete ==="
	@echo "Compare RTT and drops in the output above"
```

---

## Future Enhancements

1. **Auto-detection**: Automatically select best strategy when io_uring is enabled (after testing confirms winner)
2. **Metrics Dashboard**: Add Grafana dashboard for strategy comparison
3. **Dynamic Strategy**: Allow runtime strategy switching based on load
4. **Strategy Benchmark**: Add gosrt-specific benchmarks comparing strategies
5. **Strategy Documentation**: Document recommended strategy for each use case after testing

