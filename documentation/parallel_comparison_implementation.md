# Parallel Comparison Test Implementation

**Design Document**: `parallel_comparison_test_design.md`
**Status**: In Progress
**Started**: 2024-12-11

## Implementation Phases Overview

| Phase | Description | Status |
|-------|-------------|--------|
| Phase 1 | Network Infrastructure Updates | ✅ Complete |
| Phase 2 | Parallel Process Management | ✅ Complete |
| Phase 3 | Metrics Collection and Comparison | ✅ Basic Complete |
| Phase 4 | Profiling Support | ⏳ Pending |
| Phase 5 | Test Configurations and Makefile | ✅ Complete |
| Phase 6 | NAK btree Isolation Tests | ✅ Complete |

---

## Phase 1: Network Infrastructure Updates

### Objectives
1. Add `setup_parallel_ips()` to `lib.sh` - adds .3 addresses to namespaces
2. Add `set_blackhole_loss_parallel()` to `lib.sh` - handles 6 IPs on both routers
3. Add `clear_blackhole_loss_parallel()` to `lib.sh` - removes all 6 blackhole routes
4. Add connectivity verification for dual-IP setup
5. Verify single-pipeline tests still work unchanged

### Files to Modify
- [ ] `contrib/integration_testing/network/lib.sh`

### Implementation Details

#### 1.1 New Constants for Parallel IPs

```bash
# Parallel test IPs (HighPerf pipeline uses .3 addresses)
readonly IP_PUBLISHER_HIGHPERF="${SUBNET_PUBLISHER}.3"
readonly IP_SUBSCRIBER_HIGHPERF="${SUBNET_SUBSCRIBER}.3"
readonly IP_SERVER_HIGHPERF="${SUBNET_SERVER}.3"
```

#### 1.2 `setup_parallel_ips()` Function

Adds secondary IP addresses to existing namespaces for parallel tests:

```bash
setup_parallel_ips() {
    log "Adding parallel test IPs (.3 addresses)..."

    # Add .3 IPs to publisher namespace
    run_in_namespace "${NAMESPACE_PUBLISHER}" ip addr add "${IP_PUBLISHER_HIGHPERF}/24" dev eth0

    # Add .3 IPs to subscriber namespace
    run_in_namespace "${NAMESPACE_SUBSCRIBER}" ip addr add "${IP_SUBSCRIBER_HIGHPERF}/24" dev eth0

    # Add .3 IPs to server namespace
    run_in_namespace "${NAMESPACE_SERVER}" ip addr add "${IP_SERVER_HIGHPERF}/24" dev eth0

    log "Parallel IPs configured"
}
```

#### 1.3 `set_blackhole_loss_parallel()` Function

Applies blackhole routes to all 6 participant IPs on both routers:

```bash
set_blackhole_loss_parallel() {
    log "Setting 100% blackhole loss for parallel test (6 IPs on both routers)..."

    # Router A - Block all 6 participants
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${IP_PUBLISHER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${IP_PUBLISHER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${IP_SUBSCRIBER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${IP_SUBSCRIBER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${IP_SERVER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${IP_SERVER_HIGHPERF}/32" 2>/dev/null || true

    # Router B - Block all 6 participants
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route add blackhole "${IP_PUBLISHER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route add blackhole "${IP_PUBLISHER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route add blackhole "${IP_SUBSCRIBER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route add blackhole "${IP_SUBSCRIBER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route add blackhole "${IP_SERVER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route add blackhole "${IP_SERVER_HIGHPERF}/32" 2>/dev/null || true
}
```

#### 1.4 `clear_blackhole_loss_parallel()` Function

Removes all 6 blackhole routes from both routers:

```bash
clear_blackhole_loss_parallel() {
    log "Clearing blackhole loss for parallel test..."

    # Router A - Remove all 6 blackholes
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${IP_PUBLISHER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${IP_PUBLISHER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${IP_SUBSCRIBER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${IP_SUBSCRIBER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${IP_SERVER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${IP_SERVER_HIGHPERF}/32" 2>/dev/null || true

    # Router B - Remove all 6 blackholes
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route del blackhole "${IP_PUBLISHER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route del blackhole "${IP_PUBLISHER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route del blackhole "${IP_SUBSCRIBER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route del blackhole "${IP_SUBSCRIBER_HIGHPERF}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route del blackhole "${IP_SERVER}/32" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route del blackhole "${IP_SERVER_HIGHPERF}/32" 2>/dev/null || true
}
```

### Progress

- [x] Add parallel IP constants to `lib.sh`
- [x] Implement `setup_parallel_ips()` function
- [x] Implement `set_blackhole_loss_parallel()` function
- [x] Implement `clear_blackhole_loss_parallel()` function
- [x] Implement `set_loss_percent_parallel()` function (combined)
- [x] Add `verify_parallel_connectivity()` function
- [x] Add `cleanup_parallel_ips()` function
- [x] Update `get_ip()` to support `pipeline` parameter
- [x] Update `print_network_status()` to show parallel mode
- [x] Shellcheck validation passed
- [x] Test: Verify existing single-pipeline tests still pass (Network-Starlink-5Mbps ✓)
- [ ] Test: Verify parallel IPs can be added and removed (deferred to Phase 2 integration)

---

## Phase 2: Parallel Process Management

### Objectives
1. Create `ParallelTestConfig` struct in `config.go`
2. Create `test_parallel_mode.go` for parallel orchestration
3. Implement process spawning for 6 processes (2 sets of 3)
4. Implement UDS socket naming for 6 metrics endpoints
5. Add synchronization for parallel startup
6. Implement parallel graceful quiesce

### Files to Create/Modify
- [ ] `contrib/integration_testing/config.go` - Add `ParallelTestConfig`
- [ ] `contrib/integration_testing/test_parallel_mode.go` - New file

### Implementation Details

#### 2.1 ParallelTestConfig Struct

```go
type ParallelTestConfig struct {
    Name            string
    Description     string

    // Network impairment settings (shared by both pipelines)
    Impairment      NetworkImpairment

    // Baseline pipeline config
    Baseline        PipelineConfig

    // HighPerf pipeline config
    HighPerf        PipelineConfig

    // Test timing
    TestDuration    time.Duration
    ConnectionWait  time.Duration
    CollectInterval time.Duration

    // Profiling settings
    ProfilingEnabled bool
    ProfileTypes     []string  // "cpu", "heap", "allocs", "block", "mutex"
    ProfileDuration  time.Duration
}

type PipelineConfig struct {
    PublisherIP   string  // e.g., "10.1.1.2" or "10.1.1.3"
    ServerIP      string  // e.g., "10.2.1.2" or "10.2.1.3"
    SubscriberIP  string  // e.g., "10.1.2.2" or "10.1.2.3"
    ServerPort    int     // e.g., 6000 or 6001
    StreamID      string  // e.g., "test-stream-baseline"
    SRT           SRTConfig
    ClientConfig  ComponentConfig
}
```

#### 2.2 UDS Socket Naming

```go
// 6 unique UDS sockets for metrics collection
const (
    UDSServerBaseline   = "/tmp/srt_server_baseline_%s.sock"
    UDSServerHighPerf   = "/tmp/srt_server_highperf_%s.sock"
    UDSClientGenBaseline = "/tmp/srt_clientgen_baseline_%s.sock"
    UDSClientGenHighPerf = "/tmp/srt_clientgen_highperf_%s.sock"
    UDSClientBaseline   = "/tmp/srt_client_baseline_%s.sock"
    UDSClientHighPerf   = "/tmp/srt_client_highperf_%s.sock"
)
```

### Progress

- [x] Define `ParallelTestConfig` struct (in `config.go`)
- [x] Define `PipelineConfig` struct (in `config.go`)
- [x] Define `BaselineSRTConfig` and `HighPerfSRTConfig` presets
- [x] Implement `GetServerFlags`, `GetClientGeneratorFlags`, `GetClientFlags` for both pipelines
- [x] Implement `GetAllUDSPaths()` for 6-endpoint metrics collection
- [x] Add `ParallelTestConfigs` slice with initial configurations:
  - `Parallel-Starlink-5Mbps`
  - `Parallel-Starlink-20Mbps`
  - `Parallel-Loss2pct-5Mbps`
- [x] Add `GetParallelTestConfigByName()` lookup function
- [x] Create `test_parallel_mode.go` (parallel orchestration)
- [x] Implement `runParallelModeTest()` function
- [x] Implement `ParallelProcessSet` for managing 3-process pipelines
- [x] Implement parallel process spawning (6 processes in network namespaces)
- [x] Implement startup synchronization
- [x] Implement parallel graceful quiesce (SIGUSR1 to both client-generators)
- [x] Implement `shutdownParallelPipelines()` for parallel shutdown
- [x] Add `SetupParallelIPs()`, `SetLossParallel()`, `StartPatternParallel()` to NetworkController
- [x] Add `GetLastSnapshot()` to TestMetrics for comparison
- [x] Add `printParallelComparisonSummary()` for metrics comparison output
- [x] Add main.go integration (`parallel-test`, `parallel-test-all`, `list-parallel-configs`)
- [x] Add Makefile targets (`test-parallel`, `test-parallel-all`, `test-parallel-list`)
- [ ] Test: 6 processes start and establish connections (user to run)

---

## Phase 3: Metrics Collection and Comparison

### Objectives
1. ~~Create `ParallelMetricsCollector` for 6 endpoints~~ (using existing TestMetrics)
2. Create `PipelineAnalysis` struct for per-pipeline results
3. Create `ComparisonResult` struct for diff calculation
4. ~~Implement `PrintComparisonResult()` for formatted output~~ (basic version done)
5. Add JSON output option

### Progress
- [x] Use existing `TestMetrics` for each pipeline (2 instances)
- [x] Implement `GetSnapshotByLabel()` method for accessing pre-shutdown metrics
- [x] Create `parallel_analysis.go` with comprehensive comparison:
  - `MetricCategory` and `MetricComparison` structs
  - `CompareParallelPipelines()` function
  - `PrintDetailedComparison()` with formatted output
  - Metric grouping by category (Packet Flow, Gaps, Retrans, NAK, Drops, Timing, Bytes)
  - Significant difference highlighting (>10%)
- [x] Integrated detailed comparison into test output
- [ ] Add JSON output option
- [ ] Add Go runtime metrics comparison (memory, goroutines, GC)

### Files Created/Modified
- [x] `contrib/integration_testing/parallel_analysis.go` - Detailed comparison logic
- [x] `contrib/integration_testing/metrics_collector.go` - Added `GetSnapshotByLabel()`
- [x] `contrib/integration_testing/test_graceful_shutdown.go` - Uses detailed comparison

### Implementation Details

#### 3.1 ComparisonResult Struct

```go
type ComparisonResult struct {
    Baseline    PipelineAnalysis
    HighPerf    PipelineAnalysis

    // Computed differences
    PacketsDiff       float64  // Percentage difference in packets received
    RecoveryDiff      float64  // Difference in recovery rate
    DropsDiff         float64  // Percentage reduction in drops
    HeapDiff          float64  // Percentage difference in heap usage
    GCDiff            float64  // Difference in GC CPU fraction
    CPUDiff           float64  // Percentage difference in CPU time

    // Summary
    HighPerfWins      []string // Areas where HighPerf is better
    BaselineWins      []string // Areas where Baseline is better
    Ties              []string // Areas with no significant difference
}

type PipelineAnalysis struct {
    Name              string  // "Baseline" or "HighPerf"

    // SRT metrics
    PacketsSent       int64
    PacketsRecv       int64
    GapsDetected      int64
    Retransmissions   int64
    RecoveryRate      float64
    DropsTooLate      int64
    TSBPDSkips        int64
    NAKDeliveryRate   float64

    // Performance metrics
    HeapAllocMB       float64
    HeapObjects       int64
    GCCPUFraction     float64
    Goroutines        int
    CPUTimeSeconds    float64
}
```

### Progress

- [ ] Define `PipelineAnalysis` struct
- [ ] Define `ComparisonResult` struct
- [ ] Implement `ParallelMetricsCollector`
- [ ] Implement `AnalyzeParallelTest()` function
- [ ] Implement `PrintComparisonResult()` function
- [ ] Add JSON output option
- [ ] Test: Metrics comparison produces correct output

---

## Phase 4: Profiling Support

### Objectives
1. Add pprof flags to server binary
2. Implement sequential profile run orchestration
3. Add profile collection at test end
4. Create profile output directory structure
5. Add helper script for profile comparison

### Files to Modify
- [ ] `contrib/server/main.go` - Add pprof flags
- [ ] `contrib/integration_testing/test_parallel_mode.go` - Add profile orchestration

### Implementation Details

#### 4.1 Server Profiling Flags

```go
var (
    cpuProfile    = flag.String("cpuprofile", "", "write cpu profile to file")
    memProfile    = flag.String("memprofile", "", "write memory profile to file")
    blockProfile  = flag.String("blockprofile", "", "write block profile to file")
    mutexProfile  = flag.String("mutexprofile", "", "write mutex profile to file")
)
```

#### 4.2 Profile Collection Flow

```
For each profile type in PROFILES:
    1. Start baseline server with -${type}profile=/tmp/baseline-${type}.pprof
    2. Start highperf server with -${type}profile=/tmp/highperf-${type}.pprof
    3. Start remaining 4 processes (client-generators and clients)
    4. Run test for 5 minutes
    5. Graceful quiesce
    6. Shutdown all processes
    7. Copy profiles to profiles/${CONFIG}-${type}/
    8. Repeat for next profile type
```

### Progress

- [ ] Add profiling flags to `contrib/server/main.go`
- [ ] Implement profile writing on shutdown
- [ ] Add profile orchestration to parallel test runner
- [ ] Create profile output directory structure
- [ ] Create `scripts/compare_profiles.sh` helper
- [ ] Test: CPU profiles generated and valid
- [ ] Test: Heap profiles generated and valid

---

## Phase 5: Test Configurations and Makefile

### Objectives
1. Create `ParallelTestConfigs` slice
2. Add parallel Starlink configurations
3. Add Makefile targets
4. Update documentation

### Files to Modify
- [ ] `contrib/integration_testing/test_configs.go` - Add `ParallelTestConfigs`
- [ ] `Makefile` - Add parallel test targets

### Implementation Details

#### 5.1 ParallelTestConfigs

```go
var ParallelTestConfigs = []ParallelTestConfig{
    {
        Name:        "Parallel-Starlink-5Mbps",
        Description: "Parallel comparison: Starlink pattern at 5 Mb/s",
        Impairment: NetworkImpairment{
            Pattern:        "starlink",
            LatencyProfile: "regional",
        },
        Baseline: PipelineConfig{
            PublisherIP:  "10.1.1.2",
            ServerIP:     "10.2.1.2",
            SubscriberIP: "10.1.2.2",
            ServerPort:   6000,
            StreamID:     "test-stream-baseline",
            SRT:          BaselineSRTConfig,
        },
        HighPerf: PipelineConfig{
            PublisherIP:  "10.1.1.3",
            ServerIP:     "10.2.1.3",
            SubscriberIP: "10.1.2.3",
            ServerPort:   6001,
            StreamID:     "test-stream-highperf",
            SRT:          HighPerfSRTConfig,
        },
        TestDuration:    90 * time.Second,
        ConnectionWait:  3 * time.Second,
        CollectInterval: 2 * time.Second,
    },
    // ... Parallel-Starlink-20Mbps ...
}
```

#### 5.2 Makefile Targets

```makefile
# List parallel test configurations
test-parallel-list:
	@./contrib/integration_testing/integration_testing parallel-list

# Run a specific parallel test
test-parallel:
	@sudo ./contrib/integration_testing/integration_testing parallel-run $(CONFIG)

# Run parallel test with profiling
test-parallel-profile:
	@sudo ./contrib/integration_testing/integration_testing parallel-run $(CONFIG) --profile=$(PROFILES)
```

### Progress

- [ ] Add `BaselineSRTConfig` to `test_configs.go`
- [ ] Add `HighPerfSRTConfig` to `test_configs.go`
- [ ] Add `ParallelTestConfigs` slice
- [ ] Add `Parallel-Starlink-5Mbps` configuration
- [ ] Add `Parallel-Starlink-20Mbps` configuration
- [ ] Add Makefile targets
- [ ] Test: `make test-parallel-list` works
- [ ] Test: `make test-parallel CONFIG=Parallel-Starlink-5Mbps` works
- [ ] Test: `make test-parallel-profile` works

---

---

## Phase 6: NAK btree Isolation Tests

**Status**: ✅ Complete
**Date Added**: 2024-12-15

### Objectives
Add isolation tests specifically for the NAK btree mechanism to compare against the default gosrt implementation.

### Background
The NAK btree is a completely different feature from the packet store btree:
- **Packet store btree** (Test 6): Stores received packets in sorted order for O(log n) insertion/lookup
- **NAK btree** (Tests 7-10): Tracks missing sequence numbers for the NAK mechanism

### New Test Configurations Added

| Test # | Name | Description |
|--------|------|-------------|
| 7 | `Isolation-Server-NakBtree` | Server NAK btree for gap detection (replaces lossList scan) |
| 8 | `Isolation-Server-NakBtree-IoUringRecv` | Server NAK btree + io_uring recv (combined receiver path) |
| 9 | `Isolation-CG-HonorNakOrder` | Client-Generator HonorNakOrder (sender retransmits in NAK order) |
| 10 | `Isolation-FullNakBtree` | Full NAK btree pipeline (Server NAK btree + CG HonorNakOrder) |

### Files Modified

| File | Changes |
|------|---------|
| `contrib/integration_testing/test_configs.go` | Added 4 new `IsolationTestConfig` entries (Tests 7-10) |
| `contrib/integration_testing/config.go` | Added `WithHonorNakOrder()` helper method |
| `contrib/integration_testing/run_isolation_tests.sh` | Updated to run all 11 isolation tests |
| `documentation/parallel_isolation_test_plan.md` | Added NAK btree test documentation |

### Helper Method Added

```go
// WithHonorNakOrder returns a copy of the config with HonorNakOrder enabled
// This is a SENDER feature: retransmit packets in the order specified by the NAK packet
func (c SRTConfig) WithHonorNakOrder() SRTConfig {
    c.HonorNakOrder = true
    return c
}
```

### NAK btree Features Being Tested

| Feature | CLI Flag | Test Coverage |
|---------|----------|---------------|
| NAK btree for gap detection | `-usenakbtree` | Tests 7, 8, 10 |
| Suppress immediate NAK | (auto-set with `-usenakbtree`) | Tests 7, 8, 10 |
| FastNAK for burst detection | `-fastnakenabled` | Tests 7, 8, 10 |
| FastNAK sequence jump detection | `-fastnakrecentenabled` | Tests 7, 8, 10 |
| Honor NAK order in sender | `-honornakorder` | Tests 9, 10 |

### Expected Metrics on Clean Network

| Metric | Expected Value | Meaning |
|--------|---------------|---------|
| `gosrt_nak_btree_inserts_total` | 0 | No gaps detected (no loss) |
| `gosrt_nak_periodic_btree_runs_total` | > 0 | NAK btree path is active |
| `gosrt_nak_periodic_original_runs_total` | 0 | Original path is disabled |
| `gosrt_nak_fast_triggers_total` | 0 | No burst loss detected |
| `gosrt_nak_honored_order_total` | 0 | No NAKs to process |

### How to Run

```bash
# Single NAK btree test
cd contrib/integration_testing
sudo go run . isolation-test Isolation-Server-NakBtree

# All NAK btree tests
sudo go run . isolation-test Isolation-Server-NakBtree
sudo go run . isolation-test Isolation-Server-NakBtree-IoUringRecv
sudo go run . isolation-test Isolation-CG-HonorNakOrder
sudo go run . isolation-test Isolation-FullNakBtree

# All 11 isolation tests (~6.5 minutes)
sudo ./run_isolation_tests.sh
```

### Progress

- [x] Add `WithHonorNakOrder()` helper method to `config.go`
- [x] Add `Isolation-Server-NakBtree` test configuration
- [x] Add `Isolation-Server-NakBtree-IoUringRecv` test configuration
- [x] Add `Isolation-CG-HonorNakOrder` test configuration
- [x] Add `Isolation-FullNakBtree` test configuration
- [x] Update `run_isolation_tests.sh` to include all 11 tests
- [x] Update `parallel_isolation_test_plan.md` documentation
- [x] Verify code compiles (`go build ./contrib/integration_testing/...`)
- [ ] Run isolation tests with root privileges (user to run)

---

## Testing Checklist

### Backward Compatibility Tests
- [ ] `make test-integration` still works (all 17 clean network tests)
- [ ] `sudo make test-network CONFIG=Network-Starlink-5Mbps` still works
- [ ] `sudo make test-network-all` still passes (all 16 network tests)

### Parallel Test Validation
- [ ] Parallel processes start correctly in namespaces
- [ ] Both pipelines establish connections
- [ ] Network impairments affect both pipelines equally
- [ ] Metrics collection works for all 6 endpoints
- [ ] Comparison output is correctly formatted
- [ ] Profiling produces valid pprof files

### Isolation Test Validation (Phase 6)
- [ ] All 11 isolation tests complete without errors
- [ ] Control pipeline shows 0 gaps on clean network
- [ ] Test pipeline shows 0 gaps on clean network (for each isolated variable)
- [ ] NAK btree metrics are correctly exported and collected

---

## Design Revisions

*Track any changes to the design document here*

| Date | Change | Reason |
|------|--------|--------|
| - | - | - |

---

## Notes and Observations

*Capture implementation insights and decisions here*

---

## Related Issues

*Link to any bugs or issues discovered during implementation*

| Issue | Description | Status |
|-------|-------------|--------|
| - | - | - |

