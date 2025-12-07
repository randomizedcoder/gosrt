# GoSRT Integration Testing Design

## Overview

This document describes the comprehensive integration testing framework for the GoSRT library. The framework enables automated testing of the complete SRT protocol implementation across various configurations, network conditions, and use cases.

### Goals

1. **Validate SRT Protocol Correctness**: Ensure the GoSRT implementation correctly handles all SRT protocol features
2. **Test Performance Characteristics**: Verify throughput, latency, and resource usage across configurations
3. **Verify Graceful Shutdown**: Confirm proper context cancellation and resource cleanup
4. **Test Loss Recovery**: Validate SRT's core ARQ-based loss recovery mechanism
5. **Video Stream Validation**: Test with real video content to validate end-to-end quality
6. **Verify Encryption Features**: Test SRT encryption (AES-128/192/256) key exchange and data encryption
7. **Long-Duration Stability**: Verify no memory leaks or degradation over 12-24 hour runs
8. **Automated Profiling**: Capture and analyze CPU/memory profiles to detect performance regressions

### Core Principles

#### 1. Fail-Safe by Default (No False Positives)

**The most critical principle**: Tests must **default to FAILED** and only be marked PASSED when
all validation checks explicitly confirm success. We NEVER want automated tests to report a pass
when the test actually failed.

**Rationale**:
- A false positive (reporting PASS when the test failed) destroys human confidence in the test suite
- Developers will start ignoring test results if they can't trust them
- It's far better to have a false negative (FAIL when it should pass) - this prompts investigation
- False negatives are annoying but safe; false positives are dangerous

**Implementation**:
```go
// CORRECT: Start with failed, explicitly set to passed
result := AnalysisResult{Passed: false}  // Default to failed

// Only after ALL checks pass do we set to true
if errorAnalysis.Passed && positiveSignals.Passed && runtimeStability.Passed {
    result.Passed = true
}

// WRONG: Never do this - assumes success
result := AnalysisResult{Passed: true}  // Dangerous assumption!
```

**Code Pattern**: Every analysis function should:
1. Initialize result as `Passed: false`
2. Perform all validation checks
3. Only set `Passed = true` at the end if ALL checks explicitly passed

#### 2. Explicit Validation Over Implicit Assumptions

Every test result must be based on explicit validation of metrics and behavior, not assumptions
about what "should" happen. If we can't measure it, we can't validate it.

#### 3. Defense in Depth

Multiple layers of validation provide confidence:
- Process lifecycle (did processes start/stop correctly?)
- Error counters (are there unexpected errors?)
- Positive signals (did data actually flow?)
- Runtime stability (are resources stable over time?)

### Test Duration Categories

| Category | Duration | Purpose |
|----------|----------|---------|
| **Quick** | 10-30 seconds | Rapid validation during development, CI/CD smoke tests |
| **Standard** | 1-5 minutes | Full configuration testing, metrics validation |
| **Extended** | 30-60 minutes | Stress testing, profiling analysis |
| **Long-Duration** | 12-24 hours | Stability verification, memory leak detection |

### Test Modes

The framework supports two distinct test modes:

| Mode | Network Setup | Purpose |
|------|---------------|---------|
| **Clean Network** | Default namespace, loopback interface | Validate GoSRT code without network variables. If a test fails, the issue is in the code, not the network. |
| **Network Impairment** | Isolated namespaces, dual-router architecture | Test SRT loss recovery under controlled packet loss, latency, and jitter conditions. |

**Key Insight**: Clean network tests establish a baseline. By running on loopback with no network
impairment, any failures or anomalies must originate from the GoSRT implementation itself. Network
impairment tests then layer on controlled network conditions to validate SRT's ARQ recovery.

**Metrics Collection**: Both modes use **Unix Domain Sockets (UDS)** for Prometheus metrics. This
provides consistency and enables metrics collection from namespace-isolated processes.

### Configuration Scenarios Summary

The framework tests various SRT configuration dimensions:

| Category | Scenarios | Description |
|----------|-----------|-------------|
| **Test Mode** | clean, network | Clean loopback vs namespace-isolated with impairment |
| **Bandwidth** | 1, 2, 5, 10, 50 Mb/s | Various throughput levels |
| **Latency/Buffers** | 120ms, 500ms, 3s | Small to large buffer configurations |
| **Packet Reordering** | list, btree | Different reordering algorithms |
| **io_uring** | disabled, send, recv, both, output | Async I/O configurations |
| **Encryption** | none, AES-128, AES-192, AES-256 | Encryption modes (future) |
| **Network Conditions** | clean, 1-10% loss, burst loss, jitter | Loss injection (network mode only) |
| **Content Type** | synthetic data, MPEG-TS, H.264 | Data source types |

### Scope

| Area | Status | Description |
|------|--------|-------------|
| Basic Integration Tests | ✅ Implemented | Graceful shutdown, multiple configurations |
| Configuration Permutations | ✅ Implemented | Buffer sizes, io_uring, packet reordering |
| Metrics Collection (TCP) | ✅ Implemented | Prometheus /metrics endpoint scraping |
| Metrics Collection (UDS) | ✅ Implemented | Unix Domain Socket for namespace isolation |
| Clean Network Tests | ✅ Implemented | Baseline tests on loopback, no impairment |
| Test Mode Framework | 🔲 To Be Implemented | TestConfig.Mode field, namespace setup phases |
| Metrics Analysis | ✅ Design Complete | Error detection, statistical validation ([design](metrics_analysis_design.md)) |
| Packet Loss Injection | ✅ Design Complete | Namespace isolation, netem, nftables ([design](packet_loss_injection_design.md)) |
| Network Impairment Tests | 🔲 To Be Implemented | Tests using namespace isolation |
| Video Stream Testing | 🔲 To Be Designed | FFmpeg-based end-to-end validation |
| Encryption Testing | 🔲 To Be Designed | AES encryption verification |
| Long-Duration Testing | 🔲 To Be Designed | 12-24 hour stability runs |
| Automated Profiling | 🔲 To Be Designed | CPU/memory profile analysis |

---

## Part 1: Existing Infrastructure

### 1.1 Test Architectures

The integration testing framework supports two testing architectures:

#### Architecture A: Controlled Data Source (Client-Generator)

For protocol testing, metrics validation, and configuration permutations:

```
┌─────────────────────┐     ┌─────────────────────┐     ┌─────────────────────┐
│  Client-Generator   │────▶│       Server        │◀────│       Client        │
│    (Publisher)      │     │                     │     │    (Subscriber)     │
│   127.0.0.20:*      │     │   127.0.0.10:6000   │     │   127.0.0.30:*      │
│   metrics: 5102     │     │   metrics: 5101     │     │   metrics: 5103     │
└─────────────────────┘     └─────────────────────┘     └─────────────────────┘
```

**Data Flow**: Client-Generator → Server → Client

**Advantages**:
- **Full Metrics Visibility**: All three components expose Prometheus metrics
- **Precise Control**: Exact bitrate, packet size, timing controlled programmatically
- **Reproducible**: Deterministic data generation for consistent test results
- **Lightweight**: No external dependencies (FFmpeg not required)

**Use Cases**:
- Protocol correctness testing
- Configuration permutation testing (buffers, io_uring, packet reordering)
- Graceful shutdown validation
- SRT metrics validation
- Performance benchmarking

---

### 1.2 Test Modes: Clean Network vs Network Impairment

The integration testing framework supports two test modes that differ in network isolation:

#### Clean Network Mode (Default)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Default Network Namespace                           │
│                                                                             │
│  ┌─────────────┐     ┌─────────────┐     ┌─────────────┐                   │
│  │ Client-Gen  │────▶│   Server    │◀────│   Client    │                   │
│  │ 127.0.0.20  │     │ 127.0.0.10  │     │ 127.0.0.30  │                   │
│  └─────────────┘     └─────────────┘     └─────────────┘                   │
│         │                   │                   │                          │
│         └───────────────────┼───────────────────┘                          │
│                             ▼                                               │
│                    Loopback Interface (lo)                                  │
│                    (Zero latency, zero loss)                                │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Characteristics**:
- All processes run in the host's default namespace
- Traffic flows through the loopback interface (lo)
- Zero network latency, zero packet loss
- Any test failures must originate from the GoSRT code

**Purpose**: Establish a baseline. When tests run on a "perfect" network with no impairment,
any anomalies, errors, or failures are definitively caused by the GoSRT implementation.

#### Network Impairment Mode

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                                                                             │
│  ┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌───────────┐ │
│  │  ns_pub     │     │ ns_router_a │═════│ ns_router_b │     │  ns_srv   │ │
│  │ Client-Gen  │────▶│  (netem)    │     │  (netem)    │────▶│  Server   │ │
│  └─────────────┘     │  (nftables) │     └─────────────┘     └───────────┘ │
│                      └─────────────┘                               ▲       │
│                            ▲                                       │       │
│  ┌─────────────┐           │                                       │       │
│  │  ns_sub     │───────────┘                                       │       │
│  │  Client     │───────────────────────────────────────────────────┘       │
│  └─────────────┘                                                           │
│                                                                             │
│  ══════ = Multiple parallel links with fixed latency (0/10/60/130/300ms)   │
│  Latency switching via routing, loss injection via nftables DROP           │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Characteristics**:
- Each process runs in an isolated network namespace
- Traffic routed through dual-router architecture with configurable impairment
- Latency: 0ms, 10ms, 60ms, 130ms, 300ms RTT (switched via routing)
- Loss: 0-100% (injected via nftables DROP rules)
- Starlink pattern, burst loss, and complex patterns supported

**Purpose**: Validate SRT's ARQ-based loss recovery under realistic network conditions.

#### Metrics Collection (Both Modes)

Regardless of test mode, **all processes use Unix Domain Sockets (UDS)** for Prometheus metrics:

```bash
# Clean network mode
./server -addr 127.0.0.10:6000 -promuds /tmp/srt_server.sock

# Network impairment mode
ip netns exec ns_srv ./server -addr 10.0.1.1:6000 -promuds /tmp/srt_server.sock

# Query metrics (same command in both modes)
curl --unix-socket /tmp/srt_server.sock http://localhost/metrics
```

**Rationale**: Using UDS consistently in both modes:
- Simplifies the test orchestrator (one collection method)
- Works across namespace boundaries (filesystem is shared)
- Eliminates TCP port binding issues

#### TestConfig Mode Field

```go
type TestMode string

const (
    TestModeClean   TestMode = "clean"   // Default namespace, loopback
    TestModeNetwork TestMode = "network" // Isolated namespaces with impairment
)

type TestConfig struct {
    // ... existing fields ...

    // Test mode determines network setup
    Mode TestMode // "clean" (default) or "network"

    // Network impairment settings (only used when Mode == "network")
    Impairment NetworkImpairment
}
```

#### Test Phases by Mode

| Phase | Clean Network | Network Impairment |
|-------|---------------|---------------------|
| **Setup** | (no-op) | Create namespaces, configure routes/netem |
| **Start Processes** | Direct execution | `ip netns exec <ns> ./binary` |
| **Metrics Collection** | UDS: `/tmp/srt_*.sock` | UDS: `/tmp/srt_*.sock` |
| **Apply Impairment** | (no-op) | Adjust routes/nftables dynamically |
| **Teardown** | (no-op) | Delete namespaces |

---

#### Architecture B: Real Video Stream (FFmpeg → FFplay)

For end-to-end video validation with real media content:

```
┌─────────────────────┐     ┌─────────────────────┐     ┌─────────────────────┐     ┌─────────────────────┐
│       FFmpeg        │────▶│       Server        │◀────│       Client        │────▶│       FFplay        │
│    (Publisher)      │ SRT │                     │     │    (Subscriber)     │     │  (Video Validator)  │
│                     │     │   127.0.0.10:6000   │     │   127.0.0.30:*      │     │                     │
│   test patterns,    │     │   metrics: 5101     │     │   metrics: 5103     │     │   frame count,      │
│   video files       │     │                     │     │   -to -             │     │   timestamps,       │
│                     │     │                     │     │   (stdout pipe)     │     │   VMAF/SSIM         │
└─────────────────────┘     └─────────────────────┘     └─────────────────────┘     └─────────────────────┘
```

**Data Flow**: FFmpeg → Server (SRT) → Client (SRT) → stdout → FFplay/FFmpeg

**Advantages**:
- **Real Video Content**: Tests with actual MPEG-TS, H.264, HEVC streams
- **Visual Validation**: FFplay can display video for manual verification
- **Quality Metrics**: FFmpeg can compute VMAF, SSIM, PSNR scores
- **Industry Standard**: Uses same tools as production video workflows

**Use Cases**:
- Video codec compatibility testing
- End-to-end quality validation (VMAF/SSIM)
- Frame timing and PTS continuity verification
- Audio/video sync validation
- Real-world bitrate patterns (VBR, CBR)

---

#### Component Summary

| Component | Role | Metrics | Notes |
|-----------|------|---------|-------|
| Client-Generator | Controlled data publisher | ✅ Full | Architecture A only |
| Server | SRT relay/fanout | ✅ Full | Both architectures |
| Client | SRT subscriber | ✅ Full | Both architectures |
| FFmpeg | Real video publisher | ❌ External | Architecture B only |
| FFplay | Video validation | ❌ External | Architecture B only |

Each GoSRT component:
- Has a distinct loopback IP address for easy packet capture
- Exposes Prometheus metrics on a dedicated port
- Supports graceful shutdown via SIGINT

### 1.3 Test Configuration System

The `TestConfig` structure enables testing different configuration combinations:

```go
type TestMode string

const (
    TestModeClean   TestMode = "clean"   // Default namespace, loopback (default)
    TestModeNetwork TestMode = "network" // Isolated namespaces with impairment
)

type TestConfig struct {
    // Test identification
    Name        string
    Description string

    // Test mode (clean network vs network impairment)
    Mode TestMode // "clean" (default) or "network"

    // Network configuration
    ServerNetwork          NetworkConfig
    ClientGeneratorNetwork NetworkConfig
    ClientNetwork          NetworkConfig

    // Test parameters
    Bitrate        int64
    TestDuration   time.Duration
    ConnectionWait time.Duration

    // Component-specific configurations
    Server          ComponentConfig
    ClientGenerator ComponentConfig
    Client          ComponentConfig

    // Shared SRT configuration
    SharedSRT *SRTConfig

    // Metrics collection (always via UDS)
    MetricsEnabled  bool
    CollectInterval time.Duration

    // Network impairment (only used when Mode == TestModeNetwork)
    Impairment NetworkImpairment

    // Expected results
    ExpectedErrors     []string
    MaxExpectedDrops   int64
    MaxExpectedRetrans int64
}
```

#### SRT Configuration Options

```go
type SRTConfig struct {
    // Timeouts
    ConnectionTimeout time.Duration
    PeerIdleTimeout   time.Duration
    HandshakeTimeout  time.Duration

    // Latency
    Latency     time.Duration
    RecvLatency time.Duration
    PeerLatency time.Duration

    // Buffers
    FC      uint32
    RecvBuf uint32
    SendBuf uint32

    // Packet handling
    TLPktDrop              bool
    PacketReorderAlgorithm string  // "list" or "btree"
    BTreeDegree            int

    // io_uring
    IoUringEnabled       bool
    IoUringRecvEnabled   bool
    IoUringSendRingSize  int
    IoUringRecvRingSize  int
    IoUringRecvBatchSize int

    // Other
    Congestion string
    MaxBW      int64
    NAKReport  bool
}
```

#### Component-Specific Options

```go
type ComponentConfig struct {
    SRT        SRTConfig
    ExtraFlags []string

    // Client-specific
    IoUringOutput bool  // Use io_uring for output writes
}
```

### 1.4 Test Configuration Generation

Rather than manually defining each test configuration, the framework uses **programmatic generation**
from configuration dimensions. This ensures complete coverage and simplifies adding new options.

#### Configuration Dimensions

Each dimension defines a set of values that can be combined:

```go
// TestDimensions defines all configurable aspects
type TestDimensions struct {
    // Test mode (required - determines namespace setup)
    Modes []TestMode

    // Bandwidth levels
    Bitrates []BitrateOption

    // Buffer/latency configurations
    BufferConfigs []BufferConfig

    // Packet reordering algorithms
    ReorderAlgorithms []ReorderOption

    // io_uring configurations
    IoUringConfigs []IoUringConfig

    // Network impairment patterns (only for Mode=network)
    ImpairmentPatterns []ImpairmentOption

    // Encryption (future)
    EncryptionModes []EncryptionOption
}
```

#### Dimension Definitions

```go
// ========== Test Mode ==========
type TestMode string
const (
    ModeClean   TestMode = "clean"   // Default namespace, loopback
    ModeNetwork TestMode = "network" // Isolated namespaces, impairment
)
var TestModes = []TestMode{ModeClean, ModeNetwork}

// ========== Bitrate ==========
type BitrateOption struct {
    Name     string // e.g., "1Mbps", "10Mbps"
    Bitrate  int64  // bits per second
    Category DurationCategory
}
var Bitrates = []BitrateOption{
    {"1Mbps", 1_000_000, CategoryQuick},
    {"2Mbps", 2_000_000, CategoryQuick},
    {"5Mbps", 5_000_000, CategoryStandard},
    {"10Mbps", 10_000_000, CategoryStandard},
    {"50Mbps", 50_000_000, CategoryExtended},
}

// ========== Buffer Configuration ==========
type BufferConfig struct {
    Name       string
    Latency    time.Duration // -latency flag
    RecvBuf    uint32        // -rcvbuf
    SendBuf    uint32        // -sndbuf
    FC         uint32        // -fc
    Category   DurationCategory
}
var BufferConfigs = []BufferConfig{
    {"Default", 0, 0, 0, 0, CategoryQuick},                           // Use defaults
    {"SmallBuf", 120*time.Millisecond, 0, 0, 0, CategoryQuick},       // Low latency
    {"MediumBuf", 500*time.Millisecond, 0, 0, 0, CategoryStandard},   // Moderate
    {"LargeBuf", 3*time.Second, 2097152, 2097152, 25600, CategoryStandard}, // Large buffers
    {"ExtraLargeBuf", 5*time.Second, 4194304, 4194304, 51200, CategoryExtended}, // Stress test
}

// ExtraLargeBuf (5s) RATIONALE:
// - NOT a recommended production configuration
// - Designed to stress-test ACK/NAK handling under heavy packet loss
// - With 5s of buffering at 10Mbps = ~6MB of data in flight
// - At 5% loss, this means ~300KB of data needing retransmission tracking
// - Exercises: NAK list management, ACK coalescing, memory pressure, timing edge cases
// - Useful for profiling and optimizing the congestion control code paths

// ========== Packet Reordering ==========
type ReorderOption struct {
    Name      string
    Algorithm string // "list" or "btree"
    Degree    int    // btree degree (0 = default)
    Category  DurationCategory
}
var ReorderAlgorithms = []ReorderOption{
    {"List", "list", 0, CategoryQuick},
    {"BTree", "btree", 32, CategoryQuick},
    {"BTree64", "btree", 64, CategoryStandard},
}

// ========== io_uring Configuration ==========
type IoUringConfig struct {
    Name          string
    SendEnabled   bool
    RecvEnabled   bool
    OutputEnabled bool // client-side output writer
    Category      DurationCategory
}
var IoUringConfigs = []IoUringConfig{
    {"NoIoUring", false, false, false, CategoryQuick},
    {"IoUringSend", true, false, false, CategoryStandard},
    {"IoUringRecv", false, true, false, CategoryStandard},
    {"IoUringBoth", true, true, false, CategoryStandard},
    {"IoUringOutput", false, false, true, CategoryStandard},
    {"IoUringFull", true, true, true, CategoryExtended},
}

// ========== Network Impairment (Mode=network only) ==========
type ImpairmentOption struct {
    Name     string
    Pattern  string // matches ImpairmentPatterns keys
    Category DurationCategory
}
var ImpairmentPatterns = []ImpairmentOption{
    {"Clean", "clean", CategoryQuick},
    {"Loss1pct", "lossy-1pct", CategoryStandard},
    {"Loss5pct", "lossy-5pct", CategoryStandard},
    {"LatencyTier2", "latency-tier2", CategoryStandard},
    {"LatencyTier3", "latency-tier3", CategoryStandard},
    {"LatencyGeo", "latency-geo", CategoryExtended},
    {"Starlink", "starlink", CategoryExtended},
    {"StarlinkWithLoss", "starlink-with-loss", CategoryLongDuration},
}
```

#### Duration Categories

Each configuration option is tagged with a duration category:

```go
type DurationCategory int

const (
    CategoryQuick        DurationCategory = iota // 10-30s, CI smoke tests
    CategoryStandard                             // 1-5 min, full validation
    CategoryExtended                             // 30-60 min, stress testing
    CategoryLongDuration                         // 12-24 hours, stability
)

// Test duration per category
var CategoryDurations = map[DurationCategory]time.Duration{
    CategoryQuick:        15 * time.Second,
    CategoryStandard:     2 * time.Minute,
    CategoryExtended:     30 * time.Minute,
    CategoryLongDuration: 12 * time.Hour,
}

// Connection wait per category (higher for larger buffers/latency)
var CategoryConnectionWait = map[DurationCategory]time.Duration{
    CategoryQuick:        2 * time.Second,
    CategoryStandard:     5 * time.Second,
    CategoryExtended:     10 * time.Second,
    CategoryLongDuration: 30 * time.Second,
}
```

#### Generation Rules

Not all combinations are valid or useful. The generator applies rules:

```go
type GenerationRules struct {
    // Skip invalid combinations
    SkipRules []SkipRule

    // Reduce combinatorial explosion for certain dimensions
    // e.g., only test impairment patterns with default buffers
    ConstraintRules []ConstraintRule
}

var DefaultRules = GenerationRules{
    SkipRules: []SkipRule{
        // Network impairment only valid for Mode=network
        {Condition: "Mode == clean && Impairment != clean"},

        // io_uring output only makes sense for client (handled internally)

        // Long duration tests only with default settings
        {Condition: "Category == LongDuration && !IsDefaultConfig()"},
    },

    ConstraintRules: []ConstraintRule{
        // For high bitrates, only test with optimized settings
        {When: "Bitrate >= 50Mbps", Require: "IoUring.SendEnabled || IoUring.RecvEnabled"},

        // For network mode, limit io_uring variations (focus on buffers + impairment)
        // Buffer sizes are CRITICAL for loss recovery - test all buffer configs
        {When: "Mode == network", Limit: "IoUringConfigs", To: []string{"NoIoUring", "IoUringBoth"}},

        // For network mode, limit reorder algorithm variations
        {When: "Mode == network", Limit: "ReorderAlgorithms", To: []string{"List", "BTree"}},
    },
}

// RATIONALE for network mode buffer testing:
// - SmallBuf (120ms): Tests SRT behavior with minimal buffering - more susceptible to loss
// - MediumBuf (500ms): Balanced configuration - common for live streaming
// - LargeBuf (3s): Tests SRT's deep buffering - should recover from most impairments
// - ExtraLargeBuf (5s): Stress test - pushes ACK/NAK handling to extremes
//
// Each buffer size combined with each impairment pattern validates:
// - Whether the buffer is sufficient for the RTT + jitter
// - How loss recovery behaves under different buffer constraints
// - Edge cases where buffers overflow or underflow
// - ACK/NAK list management under heavy retransmission load (ExtraLargeBuf)
```

#### Test Name Generation

Names are auto-generated from the combination:

```go
func GenerateTestName(mode TestMode, bitrate BitrateOption,
                       buffer BufferConfig, reorder ReorderOption,
                       iouring IoUringConfig, impairment ImpairmentOption) string {
    parts := []string{}

    // Mode prefix (only for network mode)
    if mode == ModeNetwork {
        parts = append(parts, "Net")
    }

    // Non-default options
    if buffer.Name != "Default" {
        parts = append(parts, buffer.Name)
    }
    if reorder.Algorithm != "list" {
        parts = append(parts, reorder.Name)
    }
    if iouring.SendEnabled || iouring.RecvEnabled || iouring.OutputEnabled {
        parts = append(parts, iouring.Name)
    }
    if mode == ModeNetwork && impairment.Pattern != "clean" {
        parts = append(parts, impairment.Name)
    }

    // Always include bitrate
    parts = append(parts, bitrate.Name)

    // Default fallback
    if len(parts) == 1 {
        parts = []string{"Default", bitrate.Name}
    }

    return strings.Join(parts, "-")
}

// Examples:
// - Default-2Mbps           (clean, default config, 2Mbps)
// - BTree-10Mbps            (clean, btree reordering, 10Mbps)
// - IoUringFull-LargeBuf-10Mbps
// - Net-Loss5pct-2Mbps      (network mode, 5% loss)
// - Net-Starlink-5Mbps      (network mode, starlink pattern)
```

#### Generator Function

```go
func GenerateTestConfigs(category DurationCategory, rules GenerationRules) []TestConfig {
    var configs []TestConfig

    for _, mode := range TestModes {
        for _, bitrate := range Bitrates {
            for _, buffer := range BufferConfigs {
                for _, reorder := range ReorderAlgorithms {
                    for _, iouring := range IoUringConfigs {
                        for _, impairment := range ImpairmentPatterns {
                            // Determine effective category (max of all dimensions)
                            effectiveCategory := maxCategory(
                                bitrate.Category, buffer.Category,
                                reorder.Category, iouring.Category,
                                impairment.Category,
                            )

                            // Skip if not in requested category
                            if effectiveCategory != category {
                                continue
                            }

                            // Apply skip rules
                            if shouldSkip(mode, bitrate, buffer, reorder, iouring, impairment, rules) {
                                continue
                            }

                            // Apply constraint rules
                            if !meetsConstraints(mode, bitrate, buffer, reorder, iouring, impairment, rules) {
                                continue
                            }

                            configs = append(configs, buildTestConfig(
                                mode, bitrate, buffer, reorder, iouring, impairment,
                            ))
                        }
                    }
                }
            }
        }
    }

    return configs
}
```

#### Running by Category

```bash
# Run quick tests only (CI smoke tests, ~5 minutes total)
make test-integration-quick

# Run standard tests (full validation, ~30 minutes)
make test-integration-standard

# Run extended tests (stress testing, ~2 hours)
make test-integration-extended

# Run all clean network tests (no namespaces needed)
make test-integration-clean

# Run all network impairment tests (requires root)
sudo make test-integration-network
```

#### Estimated Test Counts

| Category | Clean Mode | Network Mode | Total |
|----------|------------|--------------|-------|
| Quick | ~15 | ~10 | ~25 |
| Standard | ~40 | ~80 | ~120 |
| Extended | ~20 | ~40 | ~60 |
| Long-Duration | ~5 | ~10 | ~15 |

**Network mode has more tests** because buffer configurations are fully tested with each
impairment pattern. This is intentional: buffer sizing is critical for SRT loss recovery.

Example network mode combinations for a single impairment pattern (e.g., `Loss5pct`):
- `Net-SmallBuf-Loss5pct-2Mbps` - 120ms buffer, may struggle with 5% loss
- `Net-MediumBuf-Loss5pct-2Mbps` - 500ms buffer, moderate recovery margin
- `Net-LargeBuf-Loss5pct-2Mbps` - 3s buffer, should handle 5% loss easily

**Note**: Actual counts depend on constraint rules. The generator can output a manifest:

```bash
# List all tests without running
make test-integration-list
# Output: 130 total tests (20 quick, 65 standard, 35 extended, 10 long-duration)
```

---

### 1.5 Current Test Configurations (Legacy)

The following manually-defined tests exist for backward compatibility. New tests should use
the generation framework above.

| Category | Test Name | Description |
|----------|-----------|-------------|
| **Basic Bandwidth** | Default-1Mbps | Default configuration at 1 Mb/s |
| | Default-2Mbps | Default configuration at 2 Mb/s |
| | Default-5Mbps | Default configuration at 5 Mb/s |
| | Default-10Mbps | Default configuration at 10 Mb/s |
| **Buffer Sizes** | SmallBuffers-2Mbps | 120ms latency buffers |
| | LargeBuffers-2Mbps | 3s latency buffers |
| **Packet Reordering** | BTree-2Mbps | B-tree packet reordering |
| | List-2Mbps | List-based packet reordering |
| **io_uring (SRT)** | IoUring-2Mbps | io_uring for SRT operations |
| | IoUring-10Mbps | io_uring at high throughput |
| **Combined** | IoUring-LargeBuffers-BTree-10Mbps | Full optimization stack |
| | AsymmetricLatency-2Mbps | Different latency per component |
| **io_uring Output** | IoUringOutput-2Mbps | Client io_uring output writer |
| | IoUringOutput-10Mbps | High throughput io_uring output |
| **Full io_uring** | FullIoUring-2Mbps | io_uring everywhere |
| | FullIoUring-10Mbps | Full io_uring at 10 Mb/s |
| **High Performance** | HighPerf-10Mbps | Maximum performance config |

### 1.7 Test Phases

Each integration test follows three phases:

#### Phase 1: Setup
```
1. Start Server
   - Wait for "Listening on..." message
   - Verify Prometheus metrics endpoint accessible

2. Start Client-Generator (Publisher)
   - Wait for connection established
   - Verify data generation started

3. Start Client (Subscriber)
   - Wait for connection established
   - Verify data reception started

4. Collect Initial Metrics Snapshot
```

#### Phase 2: Steady State Run
```
1. Run for configured TestDuration (e.g., 10-15 seconds)

2. Periodically collect metrics (every CollectInterval)
   - Server metrics
   - Client-Generator metrics
   - Client metrics

3. Monitor for errors/crashes
```

#### Phase 3: Graceful Shutdown
```
1. Send SIGINT to Client (subscriber first)
   - Verify graceful exit within timeout
   - Verify exit code 0

2. Send SIGINT to Client-Generator
   - Verify graceful exit within timeout
   - Verify exit code 0

3. Send SIGINT to Server
   - Verify graceful shutdown
   - Verify exit code 0

4. Collect Final Metrics Snapshot
```

### 1.8 Metrics Collection Infrastructure

The framework collects Prometheus metrics from all components:

```go
type MetricsSnapshot struct {
    Timestamp time.Time
    Point     string            // "initial", "mid-test-1", "pre-shutdown", etc.
    Metrics   map[string]float64
    RawData   string
    Error     error
}

type MetricsCollector struct {
    ServerURL    string
    ClientGenURL string
    ClientURL    string
    Snapshots    struct {
        Server    []MetricsSnapshot
        ClientGen []MetricsSnapshot
        Client    []MetricsSnapshot
    }
}
```

**Collection Points**:
- Initial (after all processes start)
- Mid-test (every CollectInterval during steady state)
- Pre-shutdown (just before SIGINT sequence)

---

## Part 2: Metrics Analysis Design

**Detailed Design Document**: [metrics_analysis_design.md](metrics_analysis_design.md)

The metrics analysis framework validates SRT behavior by examining Prometheus metrics collected
during test runs. It includes error detection, positive signal validation, and statistical
validation for network impairment tests.

**Key Components**:
- **Error Analysis**: Verify error counters are zero (or within expected bounds)
- **Positive Signals**: Confirm packets sent/received, throughput achieved, ACK/NAK exchanged
- **Statistical Validation**: For network tests, verify loss rates match configured impairment (±50% tolerance)
- **Time Series**: Analyze metrics across multiple collection points

### 2.1 Error Metrics Categories

The GoSRT library exposes error counters via the Prometheus `/metrics` endpoint. These must be analyzed to detect failures.

#### Receive Path Errors

| Metric | Description | Expected |
|--------|-------------|----------|
| `gosrt_pkt_recv_error_nil` | Nil packet received | 0 |
| `gosrt_pkt_recv_error_header` | Header parse error | 0 |
| `gosrt_pkt_recv_error_unknown` | Unknown receive error | 0 |
| `gosrt_pkt_recv_control_unknown` | Unknown control packet type | 0 |
| `gosrt_pkt_recv_subtype_unknown` | Unknown USER packet subtype | 0 |

#### Send Path Errors

| Metric | Description | Expected |
|--------|-------------|----------|
| `gosrt_pkt_sent_error_marshal` | Packet marshaling error | 0 |
| `gosrt_pkt_sent_error_write` | Write syscall error | 0 |
| `gosrt_pkt_sent_error_iouring` | io_uring submission error | 0 |
| `gosrt_pkt_sent_error_unknown` | Unknown send error | 0 |

#### Crypto Errors

| Metric | Description | Expected |
|--------|-------------|----------|
| `gosrt_crypto_error_encrypt` | Encryption failure | 0 |
| `gosrt_crypto_error_generate_sek` | SEK generation failure | 0 |
| `gosrt_crypto_error_marshal_km` | Key material marshal error | 0 |

#### Drop Counters (May Be Non-Zero)

| Metric | Description | Notes |
|--------|-------------|-------|
| `gosrt_pkt_drop_too_late` | TSBPD too-late drops | Expected under stress |
| `gosrt_congestion_recv_drop_too_old` | Congestion control drops | May occur with loss |

### 2.2 Analysis Implementation

```go
// ErrorCounters lists metrics that should always be zero
var ErrorCounters = []string{
    // Receive errors
    "gosrt_pkt_recv_error_nil",
    "gosrt_pkt_recv_error_header",
    "gosrt_pkt_recv_error_unknown",
    "gosrt_pkt_recv_control_unknown",
    "gosrt_pkt_recv_subtype_unknown",

    // Send errors
    "gosrt_pkt_sent_error_marshal",
    "gosrt_pkt_sent_error_write",
    "gosrt_pkt_sent_error_iouring",
    "gosrt_pkt_sent_error_unknown",

    // Crypto errors
    "gosrt_crypto_error_encrypt",
    "gosrt_crypto_error_generate_sek",
    "gosrt_crypto_error_marshal_km",
}

type AnalysisResult struct {
    Passed       bool
    ErrorMetrics []ErrorMetric
    Warnings     []string
}

type ErrorMetric struct {
    Component string  // "server", "client-generator", "client"
    Metric    string
    Value     float64
    Expected  float64
}

func AnalyzeMetrics(snapshots MetricsCollector) AnalysisResult {
    result := AnalysisResult{Passed: true}

    // Check all error counters
    for _, counter := range ErrorCounters {
        // Check each component's final snapshot
        for component, snaps := range map[string][]MetricsSnapshot{
            "server":           snapshots.Snapshots.Server,
            "client-generator": snapshots.Snapshots.ClientGen,
            "client":           snapshots.Snapshots.Client,
        } {
            if len(snaps) == 0 {
                continue
            }

            finalSnap := snaps[len(snaps)-1]
            if value, ok := finalSnap.Metrics[counter]; ok && value > 0 {
                result.Passed = false
                result.ErrorMetrics = append(result.ErrorMetrics, ErrorMetric{
                    Component: component,
                    Metric:    counter,
                    Value:     value,
                    Expected:  0,
                })
            }
        }
    }

    return result
}
```

### 2.3 Per-Test Specific Checks

Beyond error detection, each test configuration may have specific validation requirements:

```go
type TestValidator interface {
    ValidateMetrics(config TestConfig, snapshots MetricsCollector) error
}
```

**Examples of per-test validations** (to be designed later):

| Test | Validation |
|------|------------|
| Default-* | Throughput matches configured bitrate ±5% |
| SmallBuffers | Low latency confirmed (TSBPD delay) |
| LargeBuffers | No drops under normal conditions |
| IoUring-* | io_uring submission count > 0 |
| Loss Recovery | Retransmit count matches induced loss |

### 2.4 Analysis Output Format

```go
type TestReport struct {
    Config       TestConfig
    StartTime    time.Time
    EndTime      time.Time
    Duration     time.Duration

    // Phase results
    SetupPassed    bool
    RunPassed      bool
    ShutdownPassed bool

    // Metrics analysis
    ErrorAnalysis   AnalysisResult
    CustomAnalysis  map[string]interface{}

    // Process info
    Processes struct {
        Server    ProcessResult
        ClientGen ProcessResult
        Client    ProcessResult
    }
}

type ProcessResult struct {
    Started      bool
    ExitCode     int
    ExitDuration time.Duration
    GracefulExit bool
}
```

---

## Part 3: Future Testing Capabilities

### 3.1 Packet Loss Injection

**Status**: ✅ Design Complete

**Detailed Design**: [packet_loss_injection_design.md](packet_loss_injection_design.md)

SRT's core value proposition is ARQ-based loss recovery. To properly test this, we need to introduce controlled packet loss.

#### Architecture Summary

The packet loss injection design uses **Linux network namespaces** to isolate the publisher,
server, and subscriber processes. A **dual-router architecture** (`ns_router_a` ↔ `ns_router_b`)
with multiple parallel veth pairs enables:

- **Latency control**: Five fixed-latency links (0ms, 10ms, 60ms, 130ms, 300ms RTT); latency
  switching via routing table updates (no queue flush)
- **Loss injection**: `nftables` DROP rules for instant packet loss without affecting netem queues
- **Queue sizing**: 50,000 packet netem queue limit to prevent tail-drop at high latency

#### Metrics Collection

Since processes run in isolated network namespaces, TCP-based Prometheus endpoints are
unreachable from the test orchestrator. **Unix Domain Sockets (UDS)** solve this:

```bash
# Start server with UDS metrics
ip netns exec ns_srv ./server -promuds /tmp/srt_server.sock

# Query from host via shared filesystem
curl --unix-socket /tmp/srt_server.sock http://localhost/metrics
```

#### Key Metrics to Validate

- `gosrt_pkt_recv_loss` matches induced loss
- `gosrt_pkt_retrans_total` > 0 (recovery happening)
- `gosrt_congestion_recv_pkt_loss` tracks unrecoverable loss
- Final data integrity (no missing packets after recovery)

### 3.2 Video Stream Testing

**Status**: 🔲 To Be Designed

GoSRT is designed to carry video streams. Testing with real video content validates the complete stack using Architecture B (see Section 1.1).

#### Architecture (FFmpeg → Server → Client → FFplay)

```
┌─────────────────────┐     ┌─────────────────────┐     ┌─────────────────────┐     ┌─────────────────────┐
│       FFmpeg        │────▶│       Server        │◀────│       Client        │────▶│       FFplay        │
│    (Publisher)      │ SRT │                     │     │    (Subscriber)     │     │  (Video Validator)  │
│                     │     │   127.0.0.10:6000   │     │   127.0.0.30:*      │     │                     │
│   lavfi patterns,   │     │   metrics: 5101     │     │   metrics: 5103     │     │   visual check,     │
│   test.ts files,    │     │                     │     │   -to - (stdout)    │     │   frame count,      │
│   live sources      │     │                     │     │                     │     │   VMAF/SSIM         │
└─────────────────────┘     └─────────────────────┘     └─────────────────────┘     └─────────────────────┘
```

**Data Flow**: FFmpeg → Server (SRT publish) → Client (SRT subscribe) → stdout pipe → FFplay/FFmpeg

**Key Points**:
- FFmpeg publishes directly to GoSRT Server via SRT
- GoSRT Client subscribes and outputs to stdout (`-to -`)
- FFplay/FFmpeg receives video via pipe for validation
- Server and Client expose Prometheus metrics for monitoring

#### Requirements

1. **Video Sources**:
   - Pre-recorded test patterns (color bars, timestamps)
   - Live-generated patterns (FFmpeg lavfi)
   - Various codecs (H.264, HEVC, MPEG-TS)

2. **Validation Methods**:
   - Frame count verification
   - Timestamp continuity
   - Visual quality (VMAF/SSIM)
   - Audio sync verification

3. **Test Scenarios**:
   - Clean path (no loss)
   - With packet loss (verify recovery)
   - With network jitter
   - Bitrate variations (CBR, VBR)

#### FFmpeg/FFplay Command Examples

**1. FFmpeg Publisher (generate test pattern and publish to server)**:
```bash
# Generate color bars with timestamp overlay
ffmpeg -re -f lavfi -i "testsrc2=size=1920x1080:rate=30" \
       -f lavfi -i "sine=frequency=1000:sample_rate=48000" \
       -c:v libx264 -preset ultrafast -tune zerolatency \
       -c:a aac -b:a 128k \
       -f mpegts "srt://127.0.0.10:6000?streamid=publish:/stream1"

# Or publish existing test file
ffmpeg -re -i test.ts -c copy -f mpegts "srt://127.0.0.10:6000?streamid=publish:/stream1"
```

**2. GoSRT Client → FFplay (visual verification)**:
```bash
# Live video display with FFplay
./contrib/client/client \
  -from "srt://127.0.0.10:6000?streamid=subscribe:/stream1" \
  -to - | ffplay -i -

# With stats overlay
./contrib/client/client \
  -from "srt://127.0.0.10:6000?streamid=subscribe:/stream1" \
  -to - | ffplay -i - -stats
```

**3. GoSRT Client → FFmpeg (automated analysis)**:
```bash
# Frame count and bitrate analysis
./contrib/client/client \
  -from "srt://127.0.0.10:6000?streamid=subscribe:/stream1" \
  -to - | ffmpeg -i - -f null - 2>&1 | grep -E "(frame|fps|bitrate)"

# VMAF quality comparison against reference
./contrib/client/client \
  -from "srt://127.0.0.10:6000?streamid=subscribe:/stream1" \
  -to - | ffmpeg -i - -i reference.ts -lavfi libvmaf -f null -
```

**4. Complete Test Pipeline (one-liner)**:
```bash
# Start server, then run this pipeline:
ffmpeg -re -f lavfi -i testsrc2 -c:v libx264 -f mpegts \
  "srt://127.0.0.10:6000?streamid=publish:/test" &

./contrib/client/client \
  -from "srt://127.0.0.10:6000?streamid=subscribe:/test" \
  -to - | ffplay -i - -autoexit
```

#### Key Metrics

| Metric | Description | Threshold |
|--------|-------------|-----------|
| Frame drop | Frames lost in transmission | 0 (clean), <0.1% (with loss) |
| PTS discontinuity | Timestamp jumps | 0 |
| Bitrate match | Output vs input bitrate | ±5% |
| VMAF score | Visual quality | >95 (clean), >90 (with loss) |

### 3.3 Encryption Testing

**Status**: 🔲 To Be Designed

SRT supports AES encryption for secure media transport. Testing must verify correct key exchange and data encryption.

#### Encryption Modes to Test

| Mode | Key Length | Use Case |
|------|------------|----------|
| None | - | Baseline (no encryption) |
| AES-128 | 128 bits | Standard security |
| AES-192 | 192 bits | Enhanced security |
| AES-256 | 256 bits | Maximum security |

#### Test Scenarios

1. **Successful Encryption**:
   - Publisher and subscriber with matching passphrase
   - Verify data is encrypted on wire (packet inspection)
   - Verify data is correctly decrypted at receiver

2. **Key Exchange Validation**:
   - Verify KM (Key Material) packets are exchanged
   - Test key refresh during long streams
   - Validate `kmpreannounce` and `kmrefreshrate` settings

3. **Failure Cases**:
   - Mismatched passphrases → connection rejected
   - Different key lengths → connection rejected (with `enforcedencryption`)
   - Verify `gosrt_crypto_error_*` metrics increment on failures

4. **Performance Impact**:
   - Measure throughput with/without encryption
   - CPU usage comparison

#### CLI Flags

```bash
# Publisher with encryption
./client-generator -to "srt://..." -passphrase "secret123" -pbkeylen 32

# Subscriber with matching encryption
./client -from "srt://..." -passphrase "secret123" -pbkeylen 32
```

### 3.4 Long-Duration Stability Testing

**Status**: 🔲 To Be Designed

Long-duration tests verify system stability over extended periods, detecting memory leaks, resource exhaustion, and performance degradation.

#### Test Durations

| Duration | Purpose |
|----------|---------|
| 1 hour | Quick stability check |
| 12 hours | Overnight stability run |
| 24 hours | Full-day production simulation |
| 72 hours | Extended stress test |

#### Metrics to Monitor

1. **Memory**:
   - Heap allocations over time (should stabilize, not grow)
   - Goroutine count (should remain constant)
   - `go_memstats_heap_alloc_bytes` from Prometheus

2. **Performance**:
   - Throughput consistency (no degradation)
   - Latency stability
   - CPU usage patterns

3. **Resources**:
   - File descriptor count
   - Socket count
   - Buffer pool utilization

#### Implementation Approach

```go
type LongDurationTestConfig struct {
    Duration          time.Duration  // e.g., 24 * time.Hour
    SampleInterval    time.Duration  // e.g., 1 * time.Minute
    AlertThresholds   AlertConfig
}

type AlertConfig struct {
    MaxMemoryGrowthPercent float64  // e.g., 10% (alert if heap grows >10%)
    MaxGoroutineGrowth     int      // e.g., 50 (alert if goroutines grow by >50)
    MinThroughputPercent   float64  // e.g., 95% (alert if throughput drops <95%)
}
```

#### Makefile Targets (Future)

```bash
# 1-hour stability test
make test-integration-stability-1h

# 12-hour overnight test
make test-integration-stability-12h

# 24-hour full stability test
make test-integration-stability-24h
```

### 3.5 Automated Profiling Tests

**Status**: 🔲 To Be Designed

Automated profiling captures CPU and memory profiles during test runs, enabling detection of performance regressions and hot spots.

#### Profiling Types

| Profile | Flag | Description |
|---------|------|-------------|
| CPU | `-profile cpu` | CPU usage by function |
| Memory | `-profile mem` | Memory allocations |
| Heap | `-profile heap` | In-use heap memory |
| Allocations | `-profile allocs` | All allocations (including freed) |
| Goroutine | `-profile goroutine` | Goroutine stacks |
| Block | `-profile block` | Blocking operations |
| Mutex | `-profile mutex` | Lock contention |

#### Test Workflow

```
1. Start Server with profiling
   └── ./server -profile cpu,mem -addr 127.0.0.10:6000

2. Start Client-Generator with profiling
   └── ./client-generator -profile cpu,mem -to srt://... -bitrate 10000000

3. Start Client with profiling
   └── ./client -profile cpu,mem -from srt://... -to null

4. Run for profile duration (e.g., 5 minutes)

5. Collect profile files
   └── cpu.pprof, mem.pprof from each component

6. Analyze profiles
   └── go tool pprof -top cpu.pprof
   └── go tool pprof -web mem.pprof

7. Compare against baseline
   └── Detect regressions in top functions
```

#### Automated Analysis (Future)

```go
type ProfileAnalysis struct {
    Component   string            // "server", "client", etc.
    ProfileType string            // "cpu", "mem", etc.
    TopN        []ProfileEntry    // Top N entries by resource usage
    Regressions []RegressionAlert // Functions that regressed vs baseline
}

type ProfileEntry struct {
    Function   string
    Cumulative float64  // Percentage of total
    Self       float64  // Self percentage
}

type RegressionAlert struct {
    Function      string
    BaselineValue float64
    CurrentValue  float64
    ChangePercent float64
}
```

#### Key Functions to Monitor

```
# Server hot spots
- (*listener).ioUringCompletionHandler
- (*srtConn).handlePacket
- (*receiver).push
- (*sender).pop

# Client hot spots
- main.readLoop
- common.DirectWriter.Write
- metrics.IncrementRecvMetrics
```

#### Makefile Targets (Future)

```bash
# Run 5-minute profile capture
make test-integration-profile DURATION=5m

# Run and compare against baseline
make test-integration-profile COMPARE=baseline.json

# Generate profile report
make test-integration-profile REPORT=html > profile_report.html
```

---

## Part 4: Test Execution

### 4.1 Running Tests

```bash
# Run default test configuration
make test-integration

# Run all test configurations
make test-integration-all

# Run specific configuration
make test-integration CONFIG=IoUring-10Mbps

# List available configurations
make test-integration LIST=true
```

### 4.2 Test Output

The test framework provides:

1. **Console Output**: Real-time progress with throughput statistics
2. **Exit Codes**: 0 = all passed, 1 = failures
3. **Metrics Summary**: Snapshot counts and collection success

### 4.3 Future: Test Reports

```bash
# Generate JSON report
make test-integration REPORT=json > report.json

# Generate HTML report
make test-integration REPORT=html > report.html
```

---

## Part 5: Implementation Roadmap

### Phase 1: Current State ✅

- [x] Basic test orchestration
- [x] 17 test configurations
- [x] Metrics collection infrastructure
- [x] Graceful shutdown verification
- [x] Process lifecycle management

### Phase 2: Metrics Analysis ✅ Phases 1 & 3 Implemented

**Design Document**: [metrics_analysis_design.md](metrics_analysis_design.md)
**Implementation Tracking**: [integration_testing_metrics_analysis_implementation.md](integration_testing_metrics_analysis_implementation.md)

- [x] Design error counter analysis
- [x] Design positive signal validation
- [x] Design statistical validation for network impairment tests
- [x] Design time series data model
- [x] Implement error counter analysis (`AnalyzeErrors()`)
- [x] Implement positive signal validators (`ValidatePositiveSignals()`)
- [x] Generate console output (`PrintAnalysisResult()`)
- [x] Integrate with test framework (`runTestWithMetrics()` + `AnalyzeTestResults()`)
- [x] Add `montanaflynn/stats` dependency for linear regression
- [x] Implement Go runtime stability analysis (`runtime_analysis.go`)
- [x] Auto-analyze memory, goroutines, GC, CPU for tests ≥30 min
- [ ] Implement statistical validation (loss rate tolerance) - needs network impairment tests
- [ ] Generate structured test reports (JSON)
- [ ] Add configurable thresholds per test

### Phase 3: Packet Loss Testing ✅ Design Complete

**Design Document**: [packet_loss_injection_design.md](packet_loss_injection_design.md)

- [x] Design namespace-based network isolation
- [x] Design dual-router architecture with netem
- [x] Design nftables-based loss injection
- [x] Design impairment patterns (Starlink, burst loss)
- [ ] Implement namespace setup scripts
- [ ] Implement Go network controller
- [ ] Add network impairment test configurations
- [ ] Validate ARQ mechanism under loss

### Phase 4: Video Testing 🔲

- [ ] Design FFmpeg integration
- [ ] Create test video sources
- [ ] Implement video validation (FFplay)
- [ ] Add video quality metrics (VMAF/SSIM)

### Phase 5: Encryption Testing 🔲

- [ ] Add encryption test configurations (AES-128/192/256)
- [ ] Test key exchange (KM packets)
- [ ] Test passphrase mismatch rejection
- [ ] Validate crypto error metrics
- [ ] Measure encryption performance impact

### Phase 6: Long-Duration Testing 🔲

- [ ] Design long-duration test framework
- [ ] Implement memory/resource monitoring
- [ ] Add alerting for growth/degradation
- [ ] Create 1h, 12h, 24h test targets
- [ ] Establish baseline metrics

### Phase 7: Automated Profiling 🔲

- [ ] Design profile capture workflow
- [ ] Implement automated pprof collection
- [ ] Create profile analysis tooling
- [ ] Add baseline comparison
- [ ] Detect performance regressions

---

## Appendix A: File Structure

```
contrib/integration_testing/
├── main.go                    # Entry point, CLI parsing
├── config.go                  # SRTConfig, ComponentConfig, NetworkConfig
├── test_configs.go            # TestConfigs array
├── defaults.go                # Default network addresses, ports
├── metrics_collector.go       # Prometheus scraping
├── test_graceful_shutdown.go  # Test orchestration
├── analysis.go                # (Future) Metrics analysis
├── validators.go              # (Future) Per-test validators
├── report.go                  # (Future) Report generation
├── long_duration.go           # (Future) Long-duration test support
├── profiling.go               # (Future) Automated profiling
└── encryption_tests.go        # (Future) Encryption test configs
```

## Appendix B: Related Documents

| Document | Description |
|----------|-------------|
| `packet_loss_injection_design.md` | Packet loss injection design (detailed) |
| `test_1.1_detailed_design.md` | Original graceful shutdown test design |
| `context_and_cancellation_new_design.md` | Context cancellation patterns |
| `metrics_and_statistics_design.md` | Metrics infrastructure design |
| `client_performance_analysis.md` | Client optimization analysis |
| `amt.sh` | Linux kernel network namespace reference |

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-06 | Initial comprehensive design document | - |
| 2024-12-06 | Added 17 test configurations | - |
| 2024-12-06 | Documented metrics analysis design | - |
| 2024-12-06 | Noted packet loss and video testing requirements | - |
| 2024-12-07 | Implemented Phase 1 metrics analysis (error + signal validation) | - |
| 2024-12-07 | Added montanaflynn/stats for linear regression | - |
| 2024-12-07 | Implemented Go runtime stability analysis (Phase 3) | - |

