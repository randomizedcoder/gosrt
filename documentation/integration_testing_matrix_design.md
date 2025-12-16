# Integration Testing Matrix Design

**Status**: DRAFT - Pending Review
**Date**: 2025-12-15
**Author**: Integration Testing Team

---

## Table of Contents

1. [Overview](#1-overview)
2. [Motivation](#2-motivation)
3. [Test Naming Convention](#3-test-naming-convention)
4. [Parameter Space](#4-parameter-space)
5. [Configuration Flags Coverage](#5-configuration-flags-coverage)
6. [Test Categories](#6-test-categories)
7. [Test Matrix Generator](#7-test-matrix-generator)
8. [Strategic Test Selection](#8-strategic-test-selection)
9. [Tier 1: Core Validation Tests](#9-tier-1-core-validation-tests)
10. [Tier 2: Extended Coverage Tests](#10-tier-2-extended-coverage-tests)
11. [Tier 3: Comprehensive Tests](#11-tier-3-comprehensive-tests)
12. [Helper Functions](#12-helper-functions)
13. [CLI Support](#13-cli-support)
14. [Migration Plan: Current → New Names](#14-migration-plan-current--new-names)
15. [Implementation Plan](#15-implementation-plan)
16. [Appendix: Full Test Matrix Tables](#appendix-full-test-matrix-tables)

---

## 1. Overview

This document describes a **matrix-based approach** to integration testing for the GoSRT library. Instead of manually defining hundreds of individual test configurations, we use a **test matrix generator** that creates test configurations programmatically from parameter combinations.

### Key Features

- **Systematic coverage** of parameter combinations
- **Consistent naming convention** that describes test parameters
- **Tiered execution** for different CI stages (PR, Daily, Nightly)
- **Strategic selection** to avoid combinatorial explosion
- **Self-documenting** test names

---

## 2. Motivation

### Problems with Current Approach

| Issue | Current State | Impact |
|-------|---------------|--------|
| **Inconsistent naming** | `Parallel-Starlink-5Mbps` vs `Network-Starlink-5Mbps-NakBtree` | Hard to understand what's tested |
| **Vague config names** | `HighPerfSRTConfig` | Doesn't describe enabled features |
| **Missing parameters** | Buffer size not in test name | Assumes defaults, not explicit |
| **Manual duplication** | Each test config is hand-written | Error-prone, hard to maintain |
| **No RTT testing** | RTT infrastructure exists but unused | Missing coverage dimension |
| **Cartesian explosion** | Many parameter combinations needed | Can't test everything manually |

### Solution: Matrix Generator

```
Parameters:
  Bitrate:  [5M, 20M, 50M]           → 3 values
  Buffer:   [1s, 3s, 5s, 10s, 15s, 30s] → 6 values
  RTT:      [R0, R10, R60, R130, R300]  → 5 values
  Loss:     [0%, 2%, 5%, 10%, 15%]      → 5 values
  Config:   [Base, Nak, NakFk, NakFkr, Full] → 5 variants

Full Cartesian Product: 3 × 6 × 5 × 5 × 5 = 2,250 tests (too many!)
Strategic Selection:    ~150 tests (good coverage, reasonable runtime)
```

---

## 3. Test Naming Convention

### Format

```
{Mode}-{Pattern}[-{Loss}]-{Bitrate}-{Buffer}-{RTT}-{Config}
```

### Components

| Component | Values | Description | Examples |
|-----------|--------|-------------|----------|
| **Mode** | `Net`, `Parallel`, `Isolation` | Test mode | Network, Parallel, Isolation |
| **Pattern** | `Starlink`, `Clean`, `Loss` | Network impairment | Starlink outages, clean network, loss only |
| **Loss** | `L2`, `L5`, `L10`, `L15` | Background loss % | Omit if 0% |
| **Bitrate** | `20M`, `50M` | Megabits/second | Data rate |
| **Buffer** | `1s`, `5s`, `10s`, `30s` | SRT latency buffer | Recovery window |
| **RTT** | `R0`, `R10`, `R60`, `R130`, `R300` | Round-trip time | Network latency |
| **Config** | See below | Feature configuration | What's enabled |

### Config Abbreviations

| Abbrev | Full Name | Features Enabled |
|--------|-----------|------------------|
| `Base` | Baseline | list packet store, no io_uring, no NAK btree |
| `Btree` | BTree only | btree packet store, no io_uring |
| `IoUr` | io_uring only | list packet store, io_uring send+recv |
| `NakBtree` | NAK btree only | NAK btree, no FastNAK, no FastNAKRecent |
| `NakBtreeF` | NAK btree + FastNAK | NAK btree + FastNAK (no FastNAKRecent) |
| `NakBtreeFr` | NAK btree + FastNAK + Recent | NAK btree + FastNAK + FastNAKRecent |
| `Full` | Full Stack | btree + io_uring + NAK btree + FastNAK + FastNAKRecent + HonorNakOrder |

### Examples

| Test Name | Meaning |
|-----------|---------|
| `Parallel-Starlink-20M-5s-R60-Base-vs-Full` | Parallel: 20Mbps, 5s buffer, 60ms RTT, Baseline vs Full |
| `Net-Starlink-L5-20M-10s-R130-Full` | Network: Starlink+5% loss, 20Mbps, 10s buffer, 130ms RTT |
| `Parallel-Starlink-50M-30s-R300-Base-vs-NakBtreeF` | Parallel: 50Mbps, 30s buffer, GEO satellite, vs NAK btree+FastNAK |
| `Isolation-Clean-20M-5s-R0-Base-vs-NakBtree` | Isolation: Clean network, 20Mbps, 5s buffer, no latency |

---

## 4. Parameter Space

### Available Parameters

| Parameter | Values | Count | Description |
|-----------|--------|-------|-------------|
| **Buffer Size** | 1s, 5s, 10s, 30s | 4 | SRT latency/recovery window |
| **Bitrate** | 20 Mb/s, 50 Mb/s | 2 | Data transmission rate |
| **RTT** | 0ms, 10ms, 60ms, 130ms, 300ms | 5 | Network round-trip time |
| **Background Loss** | 0%, 2%, 5%, 10%, 15% | 5 | Uniform packet loss rate |
| **Config Variant** | Base, NakBtree, NakBtreeF, NakBtreeFr, Full | 5 | Feature configuration |

### RTT Profiles (from packet_loss_injection_design.md)

| Profile | RTT | Netem Delay | Use Case | Abbrev |
|---------|-----|-------------|----------|--------|
| Link 0 | 0ms | 0ms | Baseline/local | `R0` |
| Link 1 | 10ms | 5ms each | Regional datacenter | `R10` |
| Link 2 | 60ms | 30ms each | Cross-continental | `R60` |
| Link 3 | 130ms | 65ms each | Intercontinental | `R130` |
| Link 4 | 300ms | 150ms each | GEO satellite | `R300` |

### Theoretical Maximum (Full Cartesian Product)

```
Parallel Tests: 4 buffers × 2 bitrates × 5 RTTs × 5 losses × 4 configs = 800 tests
Network Tests:  4 buffers × 2 bitrates × 5 RTTs × 5 losses = 200 tests
Total:          1,000 tests
```

**Still too many!** We use strategic selection instead (targeting ~100-150 tests).

---

## 5. Configuration Flags Coverage

This section documents ALL configuration options from `contrib/common/flags.go` and identifies which should be tested in the matrix.

### 5.1 Complete Flag Inventory

#### Core SRT Parameters (Always Tested)

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-latency` | int (ms) | 120 | ✅ | Buffer (1s-30s) |
| `-rcvlatency` | int (ms) | 120 | ✅ | Buffer (1s-30s) |
| `-peerlatency` | int (ms) | 120 | ✅ | Buffer (1s-30s) |
| `-tlpktdrop` | bool | false | ✅ | Always true in tests |
| `-packetreorderalgorithm` | string | "list" | ✅ | Config variant |
| `-btreedegree` | int | 32 | ✅ | Fixed at 32 |

#### io_uring Configuration

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-iouringenabled` | bool | false | ✅ | Config variant |
| `-iouringrecvenabled` | bool | false | ✅ | Config variant |
| `-iouringsendringsize` | int | 256 | ❌ | Skip for now |
| `-iouringrecvringsize` | int | 1024 | ❌ | Skip for now |
| `-iouringrecvbatchsize` | int | 256 | ❌ | Skip for now |
| `-iouringrecvinitialpending` | int | ring size | ❌ | Skip for now |

**Note**: We have detailed io_uring counters in our metrics. If those counters reveal issues (errors, retries, etc.), we can add targeted tests for ring sizes and batch sizes. For now, the default values work well.

#### NAK btree Configuration

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-usenakbtree` | bool | false | ✅ | Config variant |
| `-nakrecentpercent` | float | 0.10 | ✅ | Fixed at 0.10 |
| `-nakmergegap` | int | 3 | ❌ | **TO ADD** |
| `-nakconsolidationbudgetms` | int | 2 | ❌ | Low priority |

#### FastNAK Configuration

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-fastnakenabled` | bool | true (with NAK btree) | ✅ | Config variant |
| `-fastnakrecentenabled` | bool | true | ✅ | Config variant |
| `-fastnakthresholdms` | int | 50 | ❌ | **TO ADD** |

#### Sender Configuration

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-honornakorder` | bool | false | ✅ | Config variant |

#### Timer Intervals ⚠️ HIGH PRIORITY

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-tickintervalms` | int | 10 | ❌ | **TO ADD: Tick** |
| `-periodicnakintervalms` | int | 20 | ❌ | **TO ADD: NakInt** |
| `-periodicackintervalms` | int | 10 | ❌ | **TO ADD: AckInt** |

#### Queue/Buffer Sizes

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-fc` | int | 25600 | ❌ | Skip for now |
| `-sndbuf` | int | auto | ❌ | Skip |
| `-rcvbuf` | int | auto | ❌ | Skip |
| `-networkqueuesize` | int | 2048 | ❌ | Skip |
| `-writequeuesize` | int | 2048 | ❌ | Skip |
| `-readqueuesize` | int | 2048 | ❌ | Skip |
| `-receivequeuesize` | int | 2048 | ❌ | Skip |

**Note**: The Go channel queue sizes are less important to test because:
1. The io_uring features bypass most of the Go channels
2. Plan to remove remaining channel usage soon
3. Default values are well-tuned for typical workloads

#### Connection Parameters

| Flag | Type | Default | Currently Tested | Matrix Dimension |
|------|------|---------|------------------|------------------|
| `-conntimeo` | int (ms) | 3000 | ✅ | Fixed |
| `-peeridletimeo` | int (ms) | 30000 | ✅ | Fixed |
| `-mss` | int | 1500 | ❌ | Skip |
| `-payloadsize` | int | 1316 | ❌ | Skip |
| `-maxbw` | int | -1 (unlimited) | ❌ | Skip |
| `-lossmaxttl` | int | 0 | ❌ | Skip |

**Note**: Connection parameters use well-tested defaults. Not a priority for matrix testing.

#### Not Applicable for Matrix Testing

| Flag | Reason |
|------|--------|
| `-passphrase-flag`, `-pbkeylen` | Encryption tests (separate suite) |
| `-streamid` | Set per-stream, not a tuning parameter |
| `-congestion`, `-transtype` | Always "live" for streaming tests |
| `-drifttracer` | Diagnostic, not functional |
| `-messageapi` | Different API mode |
| `-groupconnect`, `-groupstabtimeo` | Bonding tests (separate suite) |
| `-promhttp`, `-promuds` | Infrastructure, not SRT behavior |

### 5.2 New Matrix Dimensions to Add

Based on flag analysis, these parameters should be added to the matrix:

#### Timer Interval Dimension

Testing different timer frequencies can reveal timing-related issues:

| Profile | Tick | NAK | ACK | Use Case |
|---------|------|-----|-----|----------|
| `T-Default` | 10ms | 20ms | 10ms | Default settings |
| `T-Fast` | 5ms | 10ms | 5ms | Aggressive responsiveness |
| `T-Slow` | 20ms | 40ms | 20ms | Reduced CPU overhead |
| `T-FastNak` | 10ms | 5ms | 10ms | Fast NAK only |
| `T-SlowNak` | 10ms | 50ms | 10ms | Slow NAK (stress test) |

#### FastNAK Threshold Dimension (Future)

| Profile | Threshold | Use Case |
|---------|-----------|----------|
| `Fk25` | 25ms | Aggressive FastNAK |
| `Fk50` | 50ms | Default |
| `Fk100` | 100ms | Conservative FastNAK |

**Note**: FastNAK threshold testing is lower priority. The default 50ms works well.

#### Flow Control Dimension (Skipped)

Flow control window testing (`-fc` flag) is not a priority for the initial matrix. The default FC of 25600 packets is well-tuned for streaming workloads.

### 5.3 Updated Naming Convention

Extend the naming to support timer profiles:

```
{Mode}-{Pattern}[-{Loss}]-{Bitrate}-{Buffer}-{RTT}[-{Timer}]-{Config}
```

Example: `Par-Starlink-20M-3s-R60-T-FastNak-Base-vs-Full`

---

## 6. Test Categories

The matrix covers **four distinct test categories**, each serving a different purpose:

### 6.1 Clean Network Tests (Integration Mode)

Clean network tests validate basic SRT functionality **without network impairment**. These are the existing `make test-integration` tests.

#### Current Clean Network Tests

| Current Name | New Name | What it Tests |
|--------------|----------|---------------|
| `Default-1Mbps` | `Int-Clean-1M-120ms-R0-Base` | Baseline at 1 Mb/s |
| `Default-2Mbps` | `Int-Clean-2M-120ms-R0-Base` | Baseline at 2 Mb/s |
| `Default-5Mbps` | `Int-Clean-5M-120ms-R0-Base` | Baseline at 5 Mb/s |
| `Default-10Mbps` | `Int-Clean-10M-120ms-R0-Base` | Baseline at 10 Mb/s |
| `SmallBuffers-2Mbps` | `Int-Clean-2M-120ms-R0-SmallBuf` | 120ms latency (minimal) |
| `LargeBuffers-2Mbps` | `Int-Clean-2M-3s-R0-LargeBuf` | 3s latency (high resilience) |
| `BTree-2Mbps` | `Int-Clean-2M-120ms-R0-Btree` | btree packet store |
| `List-2Mbps` | `Int-Clean-2M-120ms-R0-List` | list packet store |
| `IoUring-2Mbps` | `Int-Clean-2M-120ms-R0-IoUr` | io_uring enabled |
| `IoUring-10Mbps` | `Int-Clean-10M-120ms-R0-IoUr` | io_uring at high throughput |
| `IoUring-LargeBuffers-BTree-10Mbps` | `Int-Clean-10M-3s-R0-IoUrBtree` | io_uring + btree + large buffers |
| `AsymmetricLatency-2Mbps` | `Int-Clean-2M-Asym-R0-Base` | Different server/client latency |
| `IoUringOutput-2Mbps` | `Int-Clean-2M-120ms-R0-IoUrOut` | io_uring output path |
| `IoUringOutput-10Mbps` | `Int-Clean-10M-120ms-R0-IoUrOut` | io_uring output at high rate |
| `FullIoUring-2Mbps` | `Int-Clean-2M-120ms-R0-FullIoUr` | io_uring everywhere |
| `FullIoUring-10Mbps` | `Int-Clean-10M-120ms-R0-FullIoUr` | Full io_uring at high rate |
| `HighPerf-10Mbps` | `Int-Clean-10M-3s-R0-Full` | Maximum performance config |

#### New Clean Network Tests to Add

| New Name | What it Tests |
|----------|---------------|
| `Int-Clean-20M-5s-R0-NakBtree` | NAK btree on clean network |
| `Int-Clean-20M-5s-R0-NakBtreeF` | NAK btree + FastNAK |
| `Int-Clean-20M-5s-R0-NakBtreeFr` | NAK btree + FastNAK + FastNAKRecent |
| `Int-Clean-20M-5s-R0-Full` | Full config (verify no false positives) |
| `Int-Clean-50M-5s-R0-Full` | Full config at 50 Mb/s |
| `Int-Clean-20M-5s-R60-Full` | Full config with 60ms RTT |
| `Int-Clean-20M-5s-R130-Full` | Full config with 130ms RTT |
| `Int-Clean-20M-5s-R300-Full` | Full config with GEO RTT |

#### Timer Interval Tests (Clean Network)

| New Name | Timer Profile | What it Tests |
|----------|---------------|---------------|
| `Int-Clean-20M-5s-R0-T-Fast-Full` | 5ms tick, 10ms NAK | Fast timers |
| `Int-Clean-20M-5s-R0-T-Slow-Full` | 20ms tick, 40ms NAK | Slow timers |
| `Int-Clean-20M-5s-R0-T-FastNak-Full` | 10ms tick, 5ms NAK | Aggressive NAK |
| `Int-Clean-20M-5s-R0-T-SlowNak-Full` | 10ms tick, 50ms NAK | Conservative NAK |

### 6.2 Isolation Tests (Parallel, Clean Network)

Isolation tests run **two pipelines in parallel on a clean network** to compare configurations. They verify no regression between Baseline and a test configuration.

- Pattern: `Iso-Clean-{Bitrate}-{Buffer}-{RTT}-{Baseline}-vs-{Test}`
- Purpose: Detect regressions, verify no false positives
- Current count: 17 tests

### 6.3 Network Impairment Tests (Single Pipeline)

Network tests run a **single pipeline with network impairment** (loss, Starlink patterns).

- Pattern: `Net-{Pattern}[-{Loss}]-{Bitrate}-{Buffer}-{RTT}-{Config}`
- Purpose: Validate loss recovery, NAK generation
- Current tests: Loss at 2%/5%/10%, Starlink at 5/20/50 Mb/s

### 6.4 Parallel Comparison Tests (Dual Pipeline, Impairment)

Parallel tests run **two pipelines with identical network impairment** to compare Baseline vs HighPerf.

- Pattern: `Par-{Pattern}[-{Loss}]-{Bitrate}-{Buffer}-{RTT}-{Baseline}-vs-{HighPerf}`
- Purpose: Quantify improvement from features
- Key result: HighPerf eliminates 100% of gaps vs Baseline under Starlink

---

## 7. Test Matrix Generator

### 7.1 Design Goals

1. **Programmatic generation** - No manual test config duplication
2. **Consistent naming** - All tests follow the same convention
3. **Strategic selection** - Generate high-value combinations, not everything
4. **Tier support** - Different test sets for PR, Daily, Nightly
5. **Extensible** - Easy to add new parameter values

### 7.2 Core Types

```go
package integration_testing

import "time"

// ConfigVariant represents a feature configuration preset
type ConfigVariant string

const (
    ConfigBase       ConfigVariant = "Base"       // list, no io_uring, no NAK btree
    ConfigBtree      ConfigVariant = "Btree"      // btree packet store only
    ConfigIoUr       ConfigVariant = "IoUr"       // io_uring only
    ConfigNakBtree   ConfigVariant = "NakBtree"   // NAK btree only (no FastNAK)
    ConfigNakBtreeF  ConfigVariant = "NakBtreeF"  // NAK btree + FastNAK
    ConfigNakBtreeFr ConfigVariant = "NakBtreeFr" // NAK btree + FastNAK + FastNAKRecent
    ConfigFull       ConfigVariant = "Full"       // Everything enabled
)

// RTTProfile represents a network latency profile
type RTTProfile string

const (
    RTT0   RTTProfile = "R0"    // 0ms - Local/baseline
    RTT10  RTTProfile = "R10"   // 10ms - Regional datacenter
    RTT60  RTTProfile = "R60"   // 60ms - Cross-continental
    RTT130 RTTProfile = "R130"  // 130ms - Intercontinental
    RTT300 RTTProfile = "R300"  // 300ms - GEO satellite
)

// SelectionStrategy controls which test combinations to generate
type SelectionStrategy int

const (
    // StrategyFull generates all combinations (cartesian product)
    StrategyFull SelectionStrategy = iota

    // StrategyDiagonal varies one parameter at a time, keeps others at default
    StrategyDiagonal

    // StrategyStrategic generates high-value combinations (corners, sweeps)
    StrategyStrategic

    // StrategyCustom uses a custom filter function
    StrategyCustom
)

// TestTier indicates when a test should run
type TestTier int

const (
    TierCore    TestTier = 1  // Run on every PR/commit (~30 tests)
    TierDaily   TestTier = 2  // Run daily (~40 additional tests)
    TierNightly TestTier = 3  // Run nightly (~80 additional tests)
)
```

### 7.3 Matrix Configuration

```go
// TestMatrixConfig defines the parameters for test generation
type TestMatrixConfig struct {
    // Test mode
    Mode    TestMode  // TestModeNetwork, TestModeParallel, TestModeIsolation
    Pattern string    // "starlink", "clean", "loss"

    // Parameter ranges
    Bitrates []int64           // e.g., []int64{20_000_000, 50_000_000}
    Buffers  []time.Duration   // e.g., []time.Duration{1*time.Second, 5*time.Second, 10*time.Second, 30*time.Second}
    RTTs     []RTTProfile      // e.g., []RTTProfile{RTT0, RTT10, RTT60, RTT130, RTT300}
    Losses   []float64         // e.g., []float64{0, 0.02, 0.05, 0.10, 0.15}
    Configs  []ConfigVariant   // e.g., []ConfigVariant{ConfigNakBtree, ConfigNakBtreeF, ConfigFull}

    // Selection strategy
    Strategy     SelectionStrategy
    CustomFilter func(TestParams) bool  // For StrategyCustom

    // For parallel tests
    BaselineConfig ConfigVariant  // What to compare against (usually ConfigBase)

    // Test execution parameters
    Duration    time.Duration  // Test duration (default 90s)
    ExtendedRTT bool           // Run longer for high-RTT tests

    // Tier assignment
    Tier TestTier
}

// TestParams represents a single test's parameters
type TestParams struct {
    Mode     TestMode
    Pattern  string
    Bitrate  int64
    Buffer   time.Duration
    RTT      RTTProfile
    Loss     float64
    Baseline ConfigVariant  // For parallel tests
    HighPerf ConfigVariant  // For parallel tests
}
```

### 7.4 Generator Functions

```go
// GenerateTestMatrix creates test configurations from matrix config
func GenerateTestMatrix(cfg TestMatrixConfig) []GeneratedTestConfig {
    switch cfg.Strategy {
    case StrategyFull:
        return generateFullMatrix(cfg)
    case StrategyDiagonal:
        return generateDiagonalMatrix(cfg)
    case StrategyStrategic:
        return generateStrategicMatrix(cfg)
    case StrategyCustom:
        return generateWithFilter(cfg)
    }
    return nil
}

// generateStrategicMatrix creates high-value test combinations
func generateStrategicMatrix(cfg TestMatrixConfig) []GeneratedTestConfig {
    var tests []GeneratedTestConfig

    // Define defaults
    defaultBitrate := int64(20_000_000)  // 20 Mb/s
    defaultBuffer := 5 * time.Second     // 5s buffer
    defaultRTT := RTT60                  // Cross-continental
    defaultLoss := 0.0

    // 1. SWEEPS: Vary one dimension at a time (others at default)

    // Bitrate sweep
    for _, bitrate := range cfg.Bitrates {
        tests = append(tests, createTestConfig(cfg, bitrate, defaultBuffer, defaultRTT, defaultLoss))
    }

    // Buffer sweep
    for _, buffer := range cfg.Buffers {
        if buffer == defaultBuffer { continue }
        tests = append(tests, createTestConfig(cfg, defaultBitrate, buffer, defaultRTT, defaultLoss))
    }

    // RTT sweep
    for _, rtt := range cfg.RTTs {
        if rtt == defaultRTT { continue }
        tests = append(tests, createTestConfig(cfg, defaultBitrate, defaultBuffer, rtt, defaultLoss))
    }

    // Loss sweep
    for _, loss := range cfg.Losses {
        if loss == defaultLoss { continue }
        tests = append(tests, createTestConfig(cfg, defaultBitrate, defaultBuffer, defaultRTT, loss))
    }

    // 2. CORNERS: Extreme combinations
    corners := []TestParams{
        // Extreme stress: high bitrate, small buffer, high RTT, high loss
        {Bitrate: 50_000_000, Buffer: 1 * time.Second, RTT: RTT300, Loss: 0.15},
        // Easy mode: low bitrate, large buffer, no RTT, no loss
        {Bitrate: 5_000_000, Buffer: 30 * time.Second, RTT: RTT0, Loss: 0},
        // High throughput: max bitrate, min buffer, moderate RTT
        {Bitrate: 50_000_000, Buffer: 1 * time.Second, RTT: RTT60, Loss: 0.05},
        // GEO satellite: high RTT, moderate params
        {Bitrate: 20_000_000, Buffer: 10 * time.Second, RTT: RTT300, Loss: 0.02},
        // Intercontinental with loss
        {Bitrate: 20_000_000, Buffer: 5 * time.Second, RTT: RTT130, Loss: 0.10},
    }

    for _, c := range corners {
        tests = append(tests, createTestConfig(cfg, c.Bitrate, c.Buffer, c.RTT, c.Loss))
    }

    // 3. CONFIG PERMUTATIONS: Test each config at default params
    for _, config := range cfg.Configs {
        t := createTestConfig(cfg, defaultBitrate, defaultBuffer, defaultRTT, defaultLoss)
        if cfg.Mode == TestModeParallel {
            t.HighPerfConfig = config
        }
        tests = append(tests, t)
    }

    return deduplicateTests(tests)
}

// GenerateTestName creates a standardized test name
func GenerateTestName(p TestParams) string {
    var parts []string

    // Mode prefix
    switch p.Mode {
    case TestModeNetwork:
        parts = append(parts, "Net")
    case TestModeParallel:
        parts = append(parts, "Parallel")
    case TestModeIsolation:
        parts = append(parts, "Isolation")
    }

    // Pattern with optional loss
    if p.Loss > 0 {
        parts = append(parts, fmt.Sprintf("%s-L%d", p.Pattern, int(p.Loss*100)))
    } else {
        parts = append(parts, p.Pattern)
    }

    // Bitrate
    parts = append(parts, fmt.Sprintf("%dM", p.Bitrate/1_000_000))

    // Buffer
    parts = append(parts, fmt.Sprintf("%ds", int(p.Buffer.Seconds())))

    // RTT
    parts = append(parts, string(p.RTT))

    // Config (parallel: baseline-vs-highperf, others: just config)
    if p.Mode == TestModeParallel {
        parts = append(parts, fmt.Sprintf("%s-vs-%s", p.Baseline, p.HighPerf))
    } else {
        parts = append(parts, string(p.HighPerf))
    }

    return strings.Join(parts, "-")
}
```

### 7.5 Generated Test Config Structure

```go
// GeneratedTestConfig is the output from the matrix generator
type GeneratedTestConfig struct {
    // Identification
    Name        string    // Generated name following convention
    Description string    // Human-readable description
    Tier        TestTier  // When to run this test

    // Test parameters (matches existing TestConfig/ParallelTestConfig)
    Mode       TestMode
    Impairment NetworkImpairment
    Bitrate    int64
    Duration   time.Duration

    // For parallel tests
    BaselineSRT SRTConfig
    HighPerfSRT SRTConfig

    // For network tests
    ServerConfig SRTConfig
    CGConfig     SRTConfig

    // Metadata for filtering/reporting
    Params TestParams
}

// ToTestConfig converts to the existing TestConfig structure
func (g GeneratedTestConfig) ToTestConfig() TestConfig {
    // ... conversion logic
}

// ToParallelTestConfig converts to existing ParallelTestConfig
func (g GeneratedTestConfig) ToParallelTestConfig() ParallelTestConfig {
    // ... conversion logic
}
```

---

## 8. Strategic Test Selection

### Selection Principles

1. **Diagonal sweeps** - Vary one parameter at a time to isolate effects
2. **Corner cases** - Test extremes (min/max combinations)
3. **Known problems** - Test scenarios that historically caused issues
4. **Feature validation** - Each feature config must be tested
5. **Diminishing returns** - Don't test every combination

### Tier Definitions

| Tier | Name | When to Run | Test Count | Purpose |
|------|------|-------------|------------|---------|
| **Tier 1** | Core | Every PR/commit | ~30 | Catch regressions quickly |
| **Tier 2** | Extended | Daily CI | ~40 | Broader coverage |
| **Tier 3** | Comprehensive | Nightly/Release | ~80 | Full validation |

### Tier 1: Core Validation (~30 tests)

| Category | What it tests | Count |
|----------|---------------|-------|
| FastNAK Permutations | 3 bitrates × 3 configs | 9 |
| RTT Sweep | 5 RTTs at 20M/3s | 5 |
| Buffer Sweep | 6 buffers at 5M/R60 | 6 |
| Background Loss | 4 loss levels at 20M | 4 |
| Stress Tests | 50M + corner cases | 5 |
| **Total** | | **29** |

### Tier 2: Extended Coverage (~40 additional tests)

| Category | What it tests | Count |
|----------|---------------|-------|
| Bitrate × Buffer | Cross-product at R60 | 12 |
| RTT × Loss | Cross-product at 20M | 9 |
| High Stress Corners | 50M extremes | 6 |
| FastNAK × RTT | Config variants per RTT | 9 |
| **Total** | | **36** |

### Tier 3: Comprehensive (~80 additional tests)

| Category | What it tests | Count |
|----------|---------------|-------|
| Full RTT × Buffer | All combinations at 20M | 30 |
| Full Loss × Bitrate | All combinations at R60 | 15 |
| FastNAK × All Params | Full config coverage | 27 |
| **Total** | | **72** |

### Summary

| Tier | Cumulative Tests | Runtime Estimate |
|------|------------------|------------------|
| Tier 1 only | ~30 | ~45 minutes |
| Tier 1 + 2 | ~70 | ~2 hours |
| Tier 1 + 2 + 3 | ~150 | ~4 hours |

---

## 9. Tier 1: Core Validation Tests

### 9.1 FastNAK Permutations (6 tests)

Tests each FastNAK configuration at each bitrate:

| Test Name | Bitrate | Buffer | RTT | Config |
|-----------|---------|--------|-----|--------|
| `Parallel-Starlink-20M-5s-R60-Base-vs-NakBtree` | 20M | 5s | R60 | NakBtree |
| `Parallel-Starlink-20M-5s-R60-Base-vs-NakBtreeF` | 20M | 5s | R60 | NakBtreeF |
| `Parallel-Starlink-20M-5s-R60-Base-vs-NakBtreeFr` | 20M | 5s | R60 | NakBtreeFr |
| `Parallel-Starlink-50M-5s-R60-Base-vs-NakBtree` | 50M | 5s | R60 | NakBtree |
| `Parallel-Starlink-50M-5s-R60-Base-vs-NakBtreeF` | 50M | 5s | R60 | NakBtreeF |
| `Parallel-Starlink-50M-5s-R60-Base-vs-NakBtreeFr` | 50M | 5s | R60 | NakBtreeFr |

### 9.2 RTT Sweep (5 tests)

Tests each RTT profile at fixed bitrate/buffer:

| Test Name | Bitrate | Buffer | RTT | Config |
|-----------|---------|--------|-----|--------|
| `Parallel-Starlink-20M-5s-R0-Base-vs-Full` | 20M | 5s | R0 | Full |
| `Parallel-Starlink-20M-5s-R10-Base-vs-Full` | 20M | 5s | R10 | Full |
| `Parallel-Starlink-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 | Full |
| `Parallel-Starlink-20M-5s-R130-Base-vs-Full` | 20M | 5s | R130 | Full |
| `Parallel-Starlink-20M-5s-R300-Base-vs-Full` | 20M | 5s | R300 | Full |

### 9.3 Buffer Sweep (4 tests)

Tests each buffer size at fixed bitrate/RTT:

| Test Name | Bitrate | Buffer | RTT | Config |
|-----------|---------|--------|-----|--------|
| `Parallel-Starlink-20M-1s-R60-Base-vs-Full` | 20M | 1s | R60 | Full |
| `Parallel-Starlink-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 | Full |
| `Parallel-Starlink-20M-10s-R60-Base-vs-Full` | 20M | 10s | R60 | Full |
| `Parallel-Starlink-20M-30s-R60-Base-vs-Full` | 20M | 30s | R60 | Full |

### 9.4 Background Loss Sweep (4 tests)

Tests each loss level at fixed params:

| Test Name | Bitrate | Buffer | RTT | Loss | Config |
|-----------|---------|--------|-----|------|--------|
| `Parallel-Starlink-L2-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 | 2% | Full |
| `Parallel-Starlink-L5-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 | 5% | Full |
| `Parallel-Starlink-L10-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 | 10% | Full |
| `Parallel-Starlink-L15-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 | 15% | Full |

### 9.5 Stress Tests (4 tests)

Tests extreme/corner case combinations:

| Test Name | Bitrate | Buffer | RTT | Loss | Notes |
|-----------|---------|--------|-----|------|-------|
| `Parallel-Starlink-50M-1s-R60-Base-vs-Full` | 50M | 1s | R60 | 0% | Min buffer stress |
| `Parallel-Starlink-50M-30s-R60-Base-vs-Full` | 50M | 30s | R60 | 0% | Max buffer |
| `Parallel-Starlink-20M-5s-R300-Base-vs-Full` | 20M | 5s | R300 | 0% | GEO satellite |
| `Parallel-Starlink-50M-5s-R300-Base-vs-Full` | 50M | 5s | R300 | 0% | GEO + high rate |

### 9.6 Timer Interval Tests (4 tests)

Tests varying NAK/ACK/Tick timer intervals:

| Test Name | Tick | NAK | ACK | Notes |
|-----------|------|-----|-----|-------|
| `Parallel-Starlink-20M-5s-R60-T-Fast-Base-vs-Full` | 5ms | 10ms | 5ms | Aggressive timers |
| `Parallel-Starlink-20M-5s-R60-T-Slow-Base-vs-Full` | 20ms | 40ms | 20ms | Conservative timers |
| `Parallel-Starlink-20M-5s-R60-T-FastNak-Base-vs-Full` | 10ms | 5ms | 10ms | Fast NAK recovery |
| `Parallel-Starlink-20M-5s-R60-T-SlowNak-Base-vs-Full` | 10ms | 50ms | 10ms | Delayed NAK (stress test) |

**Rationale**: The periodic NAK interval is particularly interesting:
- **Fast NAK (5ms)**: Should detect gaps faster but may generate more NAK traffic
- **Slow NAK (50ms)**: Tests if the system can still recover with delayed gap detection
- May reveal timing-sensitive bugs in the NAK btree scan logic

---

## 10. Tier 2: Extended Coverage Tests

### 10.1 Bitrate × Buffer Cross-Product (8 tests)

| Test Name | Bitrate | Buffer | RTT |
|-----------|---------|--------|-----|
| `Parallel-Starlink-20M-1s-R60-Base-vs-Full` | 20M | 1s | R60 |
| `Parallel-Starlink-20M-5s-R60-Base-vs-Full` | 20M | 5s | R60 |
| `Parallel-Starlink-20M-10s-R60-Base-vs-Full` | 20M | 10s | R60 |
| `Parallel-Starlink-20M-30s-R60-Base-vs-Full` | 20M | 30s | R60 |
| `Parallel-Starlink-50M-1s-R60-Base-vs-Full` | 50M | 1s | R60 |
| `Parallel-Starlink-50M-5s-R60-Base-vs-Full` | 50M | 5s | R60 |
| `Parallel-Starlink-50M-10s-R60-Base-vs-Full` | 50M | 10s | R60 |
| `Parallel-Starlink-50M-30s-R60-Base-vs-Full` | 50M | 30s | R60 |

### 10.2 RTT × Loss Cross-Product (9 tests)

| Test Name | RTT | Loss |
|-----------|-----|------|
| `Parallel-Starlink-L2-20M-5s-R10-Base-vs-Full` | R10 | 2% |
| `Parallel-Starlink-L5-20M-5s-R10-Base-vs-Full` | R10 | 5% |
| `Parallel-Starlink-L10-20M-5s-R10-Base-vs-Full` | R10 | 10% |
| `Parallel-Starlink-L2-20M-5s-R60-Base-vs-Full` | R60 | 2% |
| `Parallel-Starlink-L5-20M-5s-R60-Base-vs-Full` | R60 | 5% |
| `Parallel-Starlink-L10-20M-5s-R60-Base-vs-Full` | R60 | 10% |
| `Parallel-Starlink-L2-20M-5s-R130-Base-vs-Full` | R130 | 2% |
| `Parallel-Starlink-L5-20M-5s-R130-Base-vs-Full` | R130 | 5% |
| `Parallel-Starlink-L10-20M-5s-R130-Base-vs-Full` | R130 | 10% |

### 10.3 FastNAK × RTT Cross-Product (9 tests)

| Test Name | RTT | Config |
|-----------|-----|--------|
| `Parallel-Starlink-20M-5s-R10-Base-vs-NakBtree` | R10 | NakBtree |
| `Parallel-Starlink-20M-5s-R10-Base-vs-NakBtreeF` | R10 | NakBtreeF |
| `Parallel-Starlink-20M-5s-R10-Base-vs-NakBtreeFr` | R10 | NakBtreeFr |
| `Parallel-Starlink-20M-5s-R130-Base-vs-NakBtree` | R130 | NakBtree |
| `Parallel-Starlink-20M-5s-R130-Base-vs-NakBtreeF` | R130 | NakBtreeF |
| `Parallel-Starlink-20M-5s-R130-Base-vs-NakBtreeFr` | R130 | NakBtreeFr |
| `Parallel-Starlink-20M-5s-R300-Base-vs-NakBtree` | R300 | NakBtree |
| `Parallel-Starlink-20M-5s-R300-Base-vs-NakBtreeF` | R300 | NakBtreeF |
| `Parallel-Starlink-20M-5s-R300-Base-vs-NakBtreeFr` | R300 | NakBtreeFr |

---

## 11. Tier 3: Comprehensive Tests

### 11.1 Full RTT × Buffer Matrix (20 tests)

All combinations of RTT and Buffer at 20M bitrate:

| RTT \ Buffer | 1s | 5s | 10s | 30s |
|--------------|----|----|-----|-----|
| R0 | ✓ | ✓ | ✓ | ✓ |
| R10 | ✓ | ✓ | ✓ | ✓ |
| R60 | ✓ | ✓ | ✓ | ✓ |
| R130 | ✓ | ✓ | ✓ | ✓ |
| R300 | ✓ | ✓ | ✓ | ✓ |

### 11.2 Full Loss × Bitrate Matrix (10 tests)

All combinations of Loss and Bitrate at R60/5s:

| Loss \ Bitrate | 20M | 50M |
|----------------|-----|-----|
| 0% | ✓ | ✓ |
| 2% | ✓ | ✓ |
| 5% | ✓ | ✓ |
| 10% | ✓ | ✓ |
| 15% | ✓ | ✓ |

### 11.3 FastNAK × Bitrate × RTT (18 tests)

All combinations of Config × Bitrate × RTT (at 5s buffer, 0% loss):

| Config | Bitrate | RTTs |
|--------|---------|------|
| NakBtree | 20M, 50M | R10, R60, R130 |
| NakBtreeF | 20M, 50M | R10, R60, R130 |
| NakBtreeFr | 20M, 50M | R10, R60, R130 |

---

## 12. Helper Functions

### 12.1 SRTConfig Helpers (config.go)

```go
// WithLatency returns a copy with all latency/buffer settings adjusted
func (c SRTConfig) WithLatency(d time.Duration) SRTConfig {
    c.Latency = d
    c.RecvLatency = d
    c.PeerLatency = d
    return c
}

// WithoutFastNakRecent disables FastNAKRecent only (keeps FastNAK if enabled)
func (c SRTConfig) WithoutFastNakRecent() SRTConfig {
    c.FastNakRecentEnabled = false
    return c
}
```

### 12.2 Config Variant Functions

```go
// GetSRTConfig returns the SRTConfig for a given variant
func GetSRTConfig(variant ConfigVariant) SRTConfig {
    switch variant {
    case ConfigBase:
        return BaselineSRTConfig
    case ConfigBtree:
        return ControlSRTConfig.WithBtree(32)
    case ConfigIoUr:
        return ControlSRTConfig.WithIoUringSend().WithIoUringRecv()
    case ConfigNakBtree:
        return ControlSRTConfig.WithIoUringRecv().WithNakBtreeOnly()
    case ConfigNakBtreeF:
        return ControlSRTConfig.WithIoUringRecv().WithNakBtreeOnly().WithFastNak()
    case ConfigNakBtreeFr:
        return ControlSRTConfig.WithIoUringRecv().WithNakBtreeOnly().WithFastNak().WithFastNakRecent()
    case ConfigFull:
        return HighPerfSRTConfig
    default:
        return BaselineSRTConfig
    }
}

// GetSRTConfigWithLatency returns config with custom latency
func GetSRTConfigWithLatency(variant ConfigVariant, latency time.Duration) SRTConfig {
    return GetSRTConfig(variant).WithLatency(latency)
}
```

### 12.3 RTT Profile Functions

```go
// GetRTTMs returns the RTT in milliseconds for a profile
func GetRTTMs(profile RTTProfile) int {
    switch profile {
    case RTT0:
        return 0
    case RTT10:
        return 10
    case RTT60:
        return 60
    case RTT130:
        return 130
    case RTT300:
        return 300
    default:
        return 60
    }
}

// GetLatencyProfile returns the network latency profile string
func GetLatencyProfile(profile RTTProfile) string {
    switch profile {
    case RTT0:
        return "none"
    case RTT10:
        return "regional"
    case RTT60:
        return "continental"
    case RTT130:
        return "intercontinental"
    case RTT300:
        return "geo_satellite"
    default:
        return "continental"
    }
}
```

---

## 13. CLI Support

### 13.1 Makefile Targets

```makefile
# Run tests by tier
test-matrix-tier1:
	cd contrib/integration_testing && go run . matrix --tier=1

test-matrix-tier2:
	cd contrib/integration_testing && go run . matrix --tier=1,2

test-matrix-all:
	cd contrib/integration_testing && go run . matrix --tier=all

# Run tests with filters
test-matrix:
	cd contrib/integration_testing && go run . matrix $(ARGS)

# List tests without running
test-matrix-list:
	cd contrib/integration_testing && go run . matrix --list $(ARGS)

# Generate test configs to file
test-matrix-export:
	cd contrib/integration_testing && go run . matrix --export=tests.json $(ARGS)
```

### 13.2 CLI Arguments

```bash
# Run all Tier 1 tests
make test-matrix-tier1

# Run with specific filters
make test-matrix ARGS="--bitrate=50M"
make test-matrix ARGS="--rtt=R300"
make test-matrix ARGS="--buffer=1s"
make test-matrix ARGS="--config=NakFk"
make test-matrix ARGS="--loss=5"

# Combine filters
make test-matrix ARGS="--bitrate=50M --rtt=R300 --tier=1"

# List matching tests
make test-matrix-list ARGS="--tier=1"
make test-matrix-list ARGS="--bitrate=50M"

# Run specific test by name
make test-parallel NAME="Par-Starlink-50M-3s-R300-Base-vs-Full"

# Export test definitions
make test-matrix-export ARGS="--tier=all"
```

### 13.3 Go CLI Implementation

```go
// cmd/matrix.go
func runMatrixCommand(cmd *cobra.Command, args []string) {
    // Parse flags
    tier := cmd.Flag("tier").Value.String()
    bitrate := cmd.Flag("bitrate").Value.String()
    rtt := cmd.Flag("rtt").Value.String()
    buffer := cmd.Flag("buffer").Value.String()
    config := cmd.Flag("config").Value.String()
    listOnly := cmd.Flag("list").Changed
    exportFile := cmd.Flag("export").Value.String()

    // Generate test matrix
    tests := GenerateTestMatrix(buildMatrixConfig(tier, bitrate, rtt, buffer, config))

    // List mode
    if listOnly {
        for _, t := range tests {
            fmt.Printf("  %s\n", t.Name)
        }
        fmt.Printf("\nTotal: %d tests\n", len(tests))
        return
    }

    // Export mode
    if exportFile != "" {
        exportTestsToJSON(tests, exportFile)
        return
    }

    // Run tests
    for _, t := range tests {
        runTest(t)
    }
}
```

---

## 14. Migration Plan: Current → New Names

### Backwards Compatibility

During migration, support both naming conventions:

```go
type TestConfig struct {
    Name       string  // New naming convention
    LegacyName string  // Old name (for backwards compatibility)
    // ...
}
```

### Mapping Table

#### Isolation Tests

| Current Name | Proposed Name |
|--------------|---------------|
| `Isolation-Control` | `Isolation-Clean-20M-5s-R0-Base-vs-Base` |
| `Isolation-CG-IoUringSend` | `Isolation-Clean-20M-5s-R0-Base-vs-IoUrSend` |
| `Isolation-CG-IoUringRecv` | `Isolation-Clean-20M-5s-R0-Base-vs-IoUrRecv` |
| `Isolation-CG-Btree` | `Isolation-Clean-20M-5s-R0-Base-vs-Btree` |
| `Isolation-Server-IoUringSend` | `Isolation-Clean-20M-5s-R0-Base-vs-IoUrSend-Srv` |
| `Isolation-Server-IoUringRecv` | `Isolation-Clean-20M-5s-R0-Base-vs-IoUrRecv-Srv` |
| `Isolation-Server-Btree` | `Isolation-Clean-20M-5s-R0-Base-vs-Btree-Srv` |
| `Isolation-Server-NakBtree` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtree-Srv` |
| `Isolation-Server-NakBtree-IoUringRecv` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtreeIoUr-Srv` |
| `Isolation-CG-HonorNakOrder` | `Isolation-Clean-20M-5s-R0-Base-vs-Honor` |
| `Isolation-FullNakBtree` | `Isolation-Clean-20M-5s-R0-Base-vs-Full` |
| `Isolation-NakBtree-Only` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtree` |
| `Isolation-NakBtree-FastNak` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtreeF` |
| `Isolation-NakBtree-FastNakRecent` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtreeFr` |
| `Isolation-NakBtree-HonorNakOrder` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtreeHonor` |
| `Isolation-NakBtree-FastNak-HonorNakOrder` | `Isolation-Clean-20M-5s-R0-Base-vs-NakBtreeFHonor` |

#### Network Tests

| Current Name | Proposed Name |
|--------------|---------------|
| `Network-Loss2pct-5Mbps` | `Net-Loss-L2-20M-5s-R60-Base` |
| `Network-Loss5pct-5Mbps` | `Net-Loss-L5-20M-5s-R60-Base` |
| `Network-Loss10pct-5Mbps` | `Net-Loss-L10-20M-5s-R60-Base` |
| `Network-Starlink-5Mbps` | `Net-Starlink-20M-5s-R60-Base` |
| `Network-Starlink-20Mbps` | `Net-Starlink-20M-5s-R60-Base` |
| `Network-Starlink-5Mbps-NakBtree` | `Net-Starlink-20M-5s-R60-Full` |
| `Network-Starlink-20Mbps-NakBtree` | `Net-Starlink-20M-5s-R60-Full` |

#### Parallel Tests

| Current Name | Proposed Name |
|--------------|---------------|
| `Parallel-Starlink-5Mbps` | `Parallel-Starlink-20M-5s-R60-Base-vs-Full` |
| `Parallel-Starlink-20Mbps` | `Parallel-Starlink-20M-5s-R60-Base-vs-Full` |
| `Parallel-Starlink-FastNak-Impact` | `Parallel-Starlink-20M-5s-R60-NakBtree-vs-NakBtreeF` |
| `Parallel-Starlink-FastNakRecent-Impact` | `Parallel-Starlink-20M-5s-R60-NakBtreeF-vs-NakBtreeFr` |
| `Parallel-Starlink-Full-NakBtree` | `Parallel-Starlink-20M-5s-R60-Base-vs-Full` |

---

## 15. Implementation Progress

### Status: IN PROGRESS

| Phase | Status | Started | Completed | Notes |
|-------|--------|---------|-----------|-------|
| Phase 1: Foundation | ✅ Complete | 2025-12-16 | 2025-12-16 | Types and helpers in config.go |
| Phase 2: Timer Interval Support | ✅ Complete | 2025-12-16 | 2025-12-16 | Merged with Phase 1 |
| Phase 3: Test Matrix Generator | ✅ Complete | 2025-12-16 | 2025-12-16 | 64 tests generated, 8 unit tests passing |
| Phase 4: CLI Integration | ✅ Complete | 2025-12-16 | 2025-12-16 | 7 new Makefile targets |
| Phase 5: Clean Network Tests | ✅ Complete | 2025-12-16 | 2025-12-16 | 37 tests, 7 new Makefile targets |
| Phase 6: Tier 1 Tests | ⏳ Pending | - | - | |
| Phase 7: Tier 2 & 3 Tests | ⏳ Pending | - | - | |
| Phase 8: Migration | ⏳ Pending | - | - | |
| Phase 9: Documentation | ⏳ Pending | - | - | |

### Detailed Progress Log

#### 2025-12-16: Phase 1-2 Complete
`config.go` already contains all required types and helpers:
- [x] `ConfigVariant` type and constants (Base, Btree, IoUr, NakBtree, NakBtreeF, NakBtreeFr, Full)
- [x] `RTTProfile` type and constants (R0, R10, R60, R130, R300)
- [x] `TimerProfile` type and presets (Default, Fast, Slow, FastNak, SlowNak)
- [x] `GetSRTConfig()` function - returns SRTConfig for each variant
- [x] `GetRTTMs()` function - returns RTT in milliseconds
- [x] `GetLatencyProfile()` function - maps RTTProfile to netem names
- [x] `GetTimerIntervals()` function - returns tick/NAK/ACK intervals
- [x] `WithLatency()` helper - sets all latency fields together
- [x] `WithTimerProfile()` helper - applies timer profile to config

#### 2025-12-16: Phase 3 Complete - Test Matrix Generator ✅
- [x] Create `test_matrix.go` with types (`TestMatrixParams`, `GeneratedParallelTest`, `TestTier`)
- [x] Implement `GenerateTestName()` function - generates standardized test names
- [x] Implement `ParallelMatrixConfig` and `DefaultParallelMatrixConfig()`
- [x] Implement `GenerateParallelTests()` - generates all tiered tests
- [x] Implement `buildParallelConfig()` - creates full test configs
- [x] Implement tier filtering: `CountByTier()`, `FilterTestsByTier()`
- [x] Implement config filtering: `FilterTestsByConfig()`, `FilterTestsByRTT()`, `FilterTestsByBitrate()`
- [x] Implement `deduplicateTests()` - removes duplicate test names
- [x] Implement `PrintTestMatrix()` and `PrintTestSummary()` for debugging
- [x] All 8 unit tests passing

**Generated Test Counts:**
- Tier 1 (Core): 25 tests
- Tier 2 (Daily): 17 tests
- Tier 3 (Nightly): 22 tests
- Total unique: 64 tests

**Files Modified:**
- `contrib/integration_testing/test_matrix.go` - ~450 lines of generator code
- `contrib/integration_testing/test_matrix_test.go` - Fixed to match new API

#### 2025-12-16: Phase 4 Complete - CLI Integration ✅
- [x] Add CLI commands to `test_graceful_shutdown.go`:
  - `matrix-list` - List all 64 tests with tier and duration
  - `matrix-summary` - Show counts by tier, config, and RTT
  - `matrix-list-tier1` - List Tier 1 (Core) tests with estimated runtime
  - `matrix-list-tier2` - List Tier 1+2 (Daily) tests with estimated runtime
  - `matrix-run-tier1` - Run Tier 1 tests (~39 min)
  - `matrix-run-tier2` - Run Tier 1+2 tests (~65 min)
  - `matrix-run-all` - Run all matrix tests (~95 min)
- [x] Update `printUsage()` with new commands
- [x] Implement `runMatrixTestsByTier()` with proper test execution
- [x] Add Makefile targets:
  - `make test-matrix-list` - List all tests
  - `make test-matrix-summary` - Show summary
  - `make test-matrix-tier1-list` - List Tier 1 tests
  - `make test-matrix-tier2-list` - List Tier 1+2 tests
  - `sudo make test-matrix-tier1` - Run Tier 1 tests
  - `sudo make test-matrix-tier2` - Run Tier 1+2 tests
  - `sudo make test-matrix-all` - Run all tests
- [x] Add .PHONY targets for all new targets
- [x] Verified build and CLI output

**Example Output:**
```
$ make test-matrix-summary
Test Matrix Summary
===================
Total tests: 64

By Tier:
  Tier 1 (Core):    25 tests
  Tier 2 (Daily):   17 tests
  Tier 3 (Nightly): 22 tests

By Config:
  NakBtree: 7 tests
  NakBtreeF: 7 tests
  NakBtreeFr: 7 tests
  Full: 43 tests

By RTT:
  R0: 4 tests
  R10: 13 tests
  R60: 26 tests
  R130: 13 tests
  R300: 8 tests
```

**Files Modified:**
- `contrib/integration_testing/test_graceful_shutdown.go` - Added 7 CLI commands + implementations
- `Makefile` - Added 7 new targets

#### 2025-12-16: Phase 5 Complete - Clean Network Tests ✅
- [x] Add `GeneratedCleanTest` type and `CleanTestParams` struct
- [x] Implement `GenerateCleanTestName()` following naming convention: `Int-Clean-{Bitrate}-{Buffer}-{Config}[-{Timer}]`
- [x] Implement `GenerateCleanNetworkTests()` with tiered test generation:
  - Tier 1 (Core): Config variant sweep, bitrate sweep, buffer sweep (12 tests)
  - Tier 2 (Daily): NAK btree at different bitrates, timer profiles (10 tests)
  - Tier 3 (Nightly): Bitrate × Buffer, NAK btree × Buffer cross-products (15 tests)
- [x] Add helper functions: `FilterCleanTestsByTier()`, `CountCleanByTier()`, `PrintCleanTestMatrix()`, `PrintCleanTestSummary()`
- [x] Add CLI commands to `test_graceful_shutdown.go`:
  - `clean-matrix-list` - List all 37 tests with tier and duration
  - `clean-matrix-summary` - Show summary by tier with estimated runtime
  - `clean-matrix-tier1-list` - List Tier 1 tests
  - `clean-matrix-tier2-list` - List Tier 1+2 tests
  - `clean-matrix-run-tier1` - Run Tier 1 tests (~3 min)
  - `clean-matrix-run-tier2` - Run Tier 1+2 tests (~6 min)
  - `clean-matrix-run-all` - Run all tests (~9 min)
- [x] Add Makefile targets (7 new targets, no root required)
- [x] Verified build and CLI output

**Generated Clean Network Test Counts:**
- Tier 1 (Core):    12 tests
- Tier 2 (Daily):   10 tests
- Tier 3 (Nightly): 15 tests
- Total unique:     37 tests

**Example Output:**
```
$ make test-clean-matrix-tier1-list
Tier 1 (Core) Clean Network Tests (12 tests):

    1. Int-Clean-20M-5s-Base                         15s
    2. Int-Clean-20M-5s-Btree                        15s
    3. Int-Clean-20M-5s-IoUr                         15s
    4. Int-Clean-20M-5s-NakBtree                     15s
    5. Int-Clean-20M-5s-NakBtreeF                    15s
    6. Int-Clean-20M-5s-NakBtreeFr                   15s
    7. Int-Clean-20M-5s-Full                         15s
    8. Int-Clean-5M-5s-Full                          15s
    9. Int-Clean-50M-5s-Full                         15s
   10. Int-Clean-20M-1s-Full                         15s
   11. Int-Clean-20M-10s-Full                        15s
   12. Int-Clean-20M-30s-Full                        15s

Estimated total runtime: 3m0s
```

**Files Modified:**
- `contrib/integration_testing/test_matrix.go` - Added clean network test generation (~200 lines)
- `contrib/integration_testing/test_graceful_shutdown.go` - Added CLI commands (~100 lines)
- `Makefile` - Added 7 new targets

---

## 16. Implementation Plan

### Phase 1: Foundation (Week 1)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 1.1 | Add `WithLatency()` helper | `config.go` | 30 min |
| 1.2 | Add `WithoutFastNakRecent()` helper | `config.go` | 15 min |
| 1.3 | Define `ConfigVariant` and `RTTProfile` types | `config.go` | 30 min |
| 1.4 | Implement `GetSRTConfig()` function | `config.go` | 30 min |
| 1.5 | Implement `GetLatencyProfile()` function | `config.go` | 15 min |
| 1.6 | Add timer interval types and helpers | `config.go` | 30 min |
| 1.7 | Add unit tests for new helpers | `config_test.go` | 1 hour |

### Phase 2: Timer Interval Support (Week 1)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 2.1 | Add `TimerProfile` type with presets | `config.go` | 30 min |
| 2.2 | Add `WithTimerProfile()` helper | `config.go` | 30 min |
| 2.3 | Add `WithNakInterval()` helper | `config.go` | 15 min |
| 2.4 | Add `WithAckInterval()` helper | `config.go` | 15 min |
| 2.5 | Add `WithTickInterval()` helper | `config.go` | 15 min |
| 2.6 | Add CLI flag support for timer profile | `flags.go` | 30 min |
| 2.7 | Unit tests for timer helpers | `config_test.go` | 45 min |

### Phase 3: Test Matrix Generator (Week 2)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 3.1 | Create `test_matrix.go` with types | `test_matrix.go` | 1 hour |
| 3.2 | Implement `GenerateTestName()` | `test_matrix.go` | 30 min |
| 3.3 | Implement `generateStrategicMatrix()` | `test_matrix.go` | 2 hours |
| 3.4 | Implement `generateFullMatrix()` | `test_matrix.go` | 1 hour |
| 3.5 | Implement `GeneratedTestConfig.ToTestConfig()` | `test_matrix.go` | 1 hour |
| 3.6 | Implement `GeneratedTestConfig.ToParallelTestConfig()` | `test_matrix.go` | 1 hour |
| 3.7 | Add unit tests for generator | `test_matrix_test.go` | 2 hours |

### Phase 4: CLI Integration (Week 2)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 4.1 | Add `matrix` subcommand | `main.go` | 1 hour |
| 4.2 | Implement tier filtering | `main.go` | 30 min |
| 4.3 | Implement parameter filtering | `main.go` | 1 hour |
| 4.4 | Implement `--list` mode | `main.go` | 30 min |
| 4.5 | Implement `--export` mode | `main.go` | 30 min |
| 4.6 | Update Makefile targets | `Makefile` | 30 min |

### Phase 5: Clean Network Tests (Week 2-3)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 5.1 | Add NAK btree clean network tests (4) | `test_configs.go` | 30 min |
| 5.2 | Add RTT variation clean tests (3) | `test_configs.go` | 30 min |
| 5.3 | Add timer interval tests (4) | `test_configs.go` | 30 min |
| 5.4 | Add high bitrate clean tests (3) | `test_configs.go` | 30 min |
| 5.5 | Run and validate clean network tests | - | 1 hour |
| 5.6 | Update `make test-integration-list` | `test_configs.go` | 30 min |

### Phase 6: Generate Tier 1 Tests (Week 3)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 6.1 | Generate FastNAK permutation tests (9) | `test_matrix.go` | 1 hour |
| 6.2 | Generate RTT sweep tests (5) | `test_matrix.go` | 30 min |
| 6.3 | Generate buffer sweep tests (6) | `test_matrix.go` | 30 min |
| 6.4 | Generate loss sweep tests (4) | `test_matrix.go` | 30 min |
| 6.5 | Generate stress tests (5) | `test_matrix.go` | 30 min |
| 6.6 | Generate timer interval tests (4) | `test_matrix.go` | 30 min |
| 6.7 | Run and validate Tier 1 tests | - | 2 hours |

### Phase 7: Generate Tier 2 & 3 Tests (Week 3-4)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 7.1 | Generate Tier 2 cross-product tests | `test_matrix.go` | 2 hours |
| 7.2 | Generate Tier 3 comprehensive tests | `test_matrix.go` | 2 hours |
| 7.3 | Run Tier 2 tests (daily CI validation) | - | 2 hours |
| 7.4 | Run Tier 3 tests (nightly validation) | - | 4 hours |

### Phase 8: Migration (Week 4)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 8.1 | Add `LegacyName` support | `config.go`, `test_matrix.go` | 1 hour |
| 8.2 | Create name mapping | `test_configs.go` | 2 hours |
| 8.3 | Update existing scripts to use new names | `run_isolation_tests.sh`, etc. | 2 hours |
| 8.4 | Update documentation | `*.md` | 2 hours |
| 8.5 | Deprecation notice for old names | - | 30 min |

### Phase 9: Documentation & Cleanup (Week 4-5)

| Step | Task | Files | Est. Time |
|------|------|-------|-----------|
| 9.1 | Update `integration_testing_design.md` | docs | 2 hours |
| 9.2 | Update `nak_btree_integration_testing.md` | docs | 1 hour |
| 9.3 | Create runbook for matrix tests | docs | 1 hour |
| 9.4 | Add CI pipeline configuration | `.github/` or similar | 2 hours |

### Timeline Summary

| Phase | Duration | Deliverables |
|-------|----------|--------------|
| Phase 1 | 2 days | Foundation - helper functions, types |
| Phase 2 | 1 day | Timer interval support |
| Phase 3 | 3 days | Test matrix generator |
| Phase 4 | 2 days | CLI integration |
| Phase 5 | 2 days | Clean network tests (~12 new tests) |
| Phase 6 | 3 days | Tier 1 tests (~27 tests) |
| Phase 7 | 3 days | Tier 2 & 3 tests (~74 tests) |
| Phase 8 | 3 days | Migration, backwards compat |
| Phase 9 | 2 days | Documentation |
| **Total** | **~5 weeks** | **~144 tests** |

### Test Count Summary

| Category | Current | After Matrix |
|----------|---------|--------------|
| Clean Network (Integration) | 17 | ~29 |
| Isolation Tests | 17 | ~20 |
| Network Impairment | 6 | ~20 |
| Parallel Comparison | 8 | ~75 |
| **Total** | **48** | **~144** |

### Success Criteria

| Metric | Target |
|--------|--------|
| Tier 1 tests passing | 100% |
| Tier 2 tests passing | 100% |
| Tier 3 tests passing | ≥95% (some edge cases may fail) |
| Timer interval tests passing | 100% |
| Clean network tests passing | 100% |
| Test naming consistency | 100% new convention |
| Backwards compatibility | Old names still work |
| CI integration | All tiers in pipeline |
| Documentation | Complete and up-to-date |

---

## Appendix: Full Test Matrix Tables

### A.1 All Tier 1 Tests (27)

| # | Test Name | Tier |
|---|-----------|------|
| 1 | `Parallel-Starlink-20M-5s-R60-Base-vs-NakBtree` | 1 |
| 2 | `Parallel-Starlink-20M-5s-R60-Base-vs-NakBtreeF` | 1 |
| 3 | `Parallel-Starlink-20M-5s-R60-Base-vs-NakBtreeFr` | 1 |
| 4 | `Parallel-Starlink-50M-5s-R60-Base-vs-NakBtree` | 1 |
| 5 | `Parallel-Starlink-50M-5s-R60-Base-vs-NakBtreeF` | 1 |
| 6 | `Parallel-Starlink-50M-5s-R60-Base-vs-NakBtreeFr` | 1 |
| 7 | `Parallel-Starlink-20M-5s-R0-Base-vs-Full` | 1 |
| 8 | `Parallel-Starlink-20M-5s-R10-Base-vs-Full` | 1 |
| 9 | `Parallel-Starlink-20M-5s-R60-Base-vs-Full` | 1 |
| 10 | `Parallel-Starlink-20M-5s-R130-Base-vs-Full` | 1 |
| 11 | `Parallel-Starlink-20M-5s-R300-Base-vs-Full` | 1 |
| 12 | `Parallel-Starlink-20M-1s-R60-Base-vs-Full` | 1 |
| 13 | `Parallel-Starlink-20M-5s-R60-Base-vs-Full` | 1 |
| 14 | `Parallel-Starlink-20M-10s-R60-Base-vs-Full` | 1 |
| 15 | `Parallel-Starlink-20M-30s-R60-Base-vs-Full` | 1 |
| 16 | `Parallel-Starlink-L2-20M-5s-R60-Base-vs-Full` | 1 |
| 17 | `Parallel-Starlink-L5-20M-5s-R60-Base-vs-Full` | 1 |
| 18 | `Parallel-Starlink-L10-20M-5s-R60-Base-vs-Full` | 1 |
| 19 | `Parallel-Starlink-L15-20M-5s-R60-Base-vs-Full` | 1 |
| 20 | `Parallel-Starlink-50M-1s-R60-Base-vs-Full` | 1 |
| 21 | `Parallel-Starlink-50M-30s-R60-Base-vs-Full` | 1 |
| 22 | `Parallel-Starlink-20M-5s-R300-Base-vs-Full` | 1 |
| 23 | `Parallel-Starlink-50M-5s-R300-Base-vs-Full` | 1 |
| 24 | `Parallel-Starlink-20M-5s-R60-T-Fast-Base-vs-Full` | 1 |
| 25 | `Parallel-Starlink-20M-5s-R60-T-Slow-Base-vs-Full` | 1 |
| 26 | `Parallel-Starlink-20M-5s-R60-T-FastNak-Base-vs-Full` | 1 |
| 27 | `Parallel-Starlink-20M-5s-R60-T-SlowNak-Base-vs-Full` | 1 |

### A.2 Config Variant Reference

| Variant | Packet Store | io_uring | NAK btree | FastNAK | FastNAKRecent | HonorNakOrder |
|---------|--------------|----------|-----------|---------|---------------|---------------|
| `Base` | list | ❌ | ❌ | ❌ | ❌ | ❌ |
| `Btree` | btree | ❌ | ❌ | ❌ | ❌ | ❌ |
| `IoUr` | list | ✅ | ❌ | ❌ | ❌ | ❌ |
| `NakBtree` | list | ✅ recv | ✅ | ❌ | ❌ | ❌ |
| `NakBtreeF` | list | ✅ recv | ✅ | ✅ | ❌ | ❌ |
| `NakBtreeFr` | list | ✅ recv | ✅ | ✅ | ✅ | ❌ |
| `Full` | btree | ✅ | ✅ | ✅ | ✅ | ✅ |

### A.3 RTT Profile Reference

| Profile | RTT | Netem Delay | Use Case |
|---------|-----|-------------|----------|
| `R0` | 0ms | 0ms | Baseline/local |
| `R10` | 10ms | 5ms each | Regional datacenter |
| `R60` | 60ms | 30ms each | Cross-continental |
| `R130` | 130ms | 65ms each | Intercontinental |
| `R300` | 300ms | 150ms each | GEO satellite |

---

## References

- [integration_testing_design.md](integration_testing_design.md) - Overall integration testing design
- [packet_loss_injection_design.md](packet_loss_injection_design.md) - Network impairment infrastructure
- [nak_btree_integration_testing.md](nak_btree_integration_testing.md) - NAK btree specific tests
- [parallel_comparison_test_design.md](parallel_comparison_test_design.md) - Parallel test methodology

