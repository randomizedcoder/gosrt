# Parallel Comparison Integration Test Design

## Overview

This document describes the design for a new "parallel" integration test that runs two complete SRT streaming pipelines simultaneously over the same network infrastructure. The goal is to enable direct, side-by-side comparison of different GoSRT configurations under identical network conditions.

## Goals

1. **Direct Comparison**: Run two complete (client-generator → server → client) pipelines in parallel
2. **Configuration Variants**: Compare baseline (linked-list, no io_uring) vs high-performance (btree, io_uring)
3. **Identical Network Conditions**: Both pipelines experience the same network impairments simultaneously
4. **Metrics Comparison**: Compare SRT-level metrics between pipelines
5. **Performance Profiling**: Sequential CPU, memory, and blocking profiling for deeper analysis
6. **Backward Compatibility**: Do not break any existing single-pipeline tests

## Network Topology

### Existing Single-Pipeline Topology (Reference)

This is the current topology that works and must not be broken:

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                   Host System                                            │
│                                                                                          │
│  ┌──────────────┐                                               ┌──────────────┐        │
│  │ ns_publisher │                                               │ns_subscriber │        │
│  │  (CG)        │                                               │  (Client)    │        │
│  │ 10.1.1.2     │                                               │ 10.1.2.2     │        │
│  └──────┬───────┘                                               └──────┬───────┘        │
│         │ veth                                                   veth  │                 │
│         ▼                                                              ▼                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                           ns_router_a (Client Router)                         │       │
│  │  eth_pub (10.1.1.1)                                    eth_sub (10.1.2.1)    │       │
│  │                                                                               │       │
│  │  ─────────────────────── link1_a ◄──────────────────────────────────────────  │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth pair (with netem/blackhole)               │
│                                        ▼                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                          ns_router_b (Server Router)                          │       │
│  │                                                                               │       │
│  │  ─────────────────────── link1_b ◄──────────────────────────────────────────  │       │
│  │                                                                               │       │
│  │  eth_srv (10.2.1.1)                                                          │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth                                            │
│                                        ▼                                                 │
│                               ┌──────────────┐                                          │
│                               │  ns_server   │                                          │
│                               │  (Server)    │                                          │
│                               │  10.2.1.2    │                                          │
│                               └──────────────┘                                          │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

### Updated Parallel Topology (Two Pipelines)

The parallel test adds secondary IP addresses (.3) to each namespace while keeping
the existing .2 addresses. Both pipelines share the same network infrastructure
and experience identical network impairments.

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                   Host System                                            │
│                                                                                          │
│   Test Orchestrator                                                                      │
│   ├── Metrics Collection (6 UDS sockets)                                                 │
│   ├── Pattern Controller (starlink, etc.)                                                │
│   └── Profile Collection (pprof - servers only)                                          │
│                                                                                          │
│  ┌──────────────────┐                                        ┌──────────────────┐       │
│  │   ns_publisher   │                                        │  ns_subscriber   │       │
│  │                  │                                        │                  │       │
│  │  ┌────────────┐  │                                        │  ┌────────────┐  │       │
│  │  │CG-Baseline │  │                                        │  │Cl-Baseline │  │       │
│  │  │ 10.1.1.2   │  │                                        │  │ 10.1.2.2   │  │       │
│  │  │ list/no-io │  │                                        │  │ list/no-io │  │       │
│  │  └────────────┘  │                                        │  └────────────┘  │       │
│  │                  │                                        │                  │       │
│  │  ┌────────────┐  │                                        │  ┌────────────┐  │       │
│  │  │CG-HighPerf │  │                                        │  │Cl-HighPerf │  │       │
│  │  │ 10.1.1.3   │  │                                        │  │ 10.1.2.3   │  │       │
│  │  │ btree/io_u │  │                                        │  │ btree/io_u │  │       │
│  │  └────────────┘  │                                        │  └────────────┘  │       │
│  │                  │                                        │                  │       │
│  │  eth0: 10.1.1.1  │                                        │  eth0: 10.1.2.1  │       │
│  │  (+ .2 and .3)   │                                        │  (+ .2 and .3)   │       │
│  └────────┬─────────┘                                        └────────┬─────────┘       │
│           │ veth                                                veth  │                  │
│           ▼                                                           ▼                  │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                           ns_router_a (Client Router)                         │       │
│  │                                                                               │       │
│  │  eth_pub (10.1.1.1)                                    eth_sub (10.1.2.1)    │       │
│  │                                                                               │       │
│  │  Blackhole Routes (when 100% loss active):                                    │       │
│  │  ├── blackhole 10.1.1.2/32  (Baseline Publisher)                              │       │
│  │  ├── blackhole 10.1.1.3/32  (HighPerf Publisher)                              │       │
│  │  ├── blackhole 10.1.2.2/32  (Baseline Subscriber)                             │       │
│  │  ├── blackhole 10.1.2.3/32  (HighPerf Subscriber)                             │       │
│  │  ├── blackhole 10.2.1.2/32  (Baseline Server)                                 │       │
│  │  └── blackhole 10.2.1.3/32  (HighPerf Server)                                 │       │
│  │                                                                               │       │
│  │  ─────────────────────── link1_a ◄──────────────────────────────────────────  │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth pair (netem latency/loss applied here)    │
│                                        ▼                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                          ns_router_b (Server Router)                          │       │
│  │                                                                               │       │
│  │  Blackhole Routes (when 100% loss active):                                    │       │
│  │  ├── blackhole 10.1.1.2/32  (Baseline Publisher)                              │       │
│  │  ├── blackhole 10.1.1.3/32  (HighPerf Publisher)                              │       │
│  │  ├── blackhole 10.1.2.2/32  (Baseline Subscriber)                             │       │
│  │  ├── blackhole 10.1.2.3/32  (HighPerf Subscriber)                             │       │
│  │  ├── blackhole 10.2.1.2/32  (Baseline Server)                                 │       │
│  │  └── blackhole 10.2.1.3/32  (HighPerf Server)                                 │       │
│  │                                                                               │       │
│  │  ─────────────────────── link1_b ◄──────────────────────────────────────────  │       │
│  │                                                                               │       │
│  │  eth_srv (10.2.1.1)                                                          │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth                                            │
│                                        ▼                                                 │
│                            ┌────────────────────┐                                       │
│                            │     ns_server      │                                       │
│                            │                    │                                       │
│                            │  ┌──────────────┐  │                                       │
│                            │  │ Srv-Baseline │  │                                       │
│                            │  │ 10.2.1.2     │  │                                       │
│                            │  │ :6000        │  │                                       │
│                            │  │ list/no-io   │  │                                       │
│                            │  └──────────────┘  │                                       │
│                            │                    │                                       │
│                            │  ┌──────────────┐  │                                       │
│                            │  │ Srv-HighPerf │  │                                       │
│                            │  │ 10.2.1.3     │  │                                       │
│                            │  │ :6001        │  │                                       │
│                            │  │ btree/io_u   │  │                                       │
│                            │  └──────────────┘  │                                       │
│                            │                    │                                       │
│                            │  eth0: 10.2.1.1    │                                       │
│                            │  (+ .2 and .3)     │                                       │
│                            └────────────────────┘                                       │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

### IP Address Assignments

| Component | Baseline IP | HighPerf IP | Port | Stream ID |
|-----------|-------------|-------------|------|-----------|
| Publisher (Client-Generator) | 10.1.1.2 | 10.1.1.3 | N/A (client) | N/A |
| Server | 10.2.1.2 | 10.2.1.3 | :6000 / :6001 | N/A |
| Subscriber (Client) | 10.1.2.2 | 10.1.2.3 | N/A (client) | N/A |
| Stream (publish) | - | - | - | `/test-stream-baseline` / `/test-stream-highperf` |
| Stream (subscribe) | - | - | - | `subscribe:/test-stream-baseline` / `subscribe:/test-stream-highperf` |

### Blackhole Routes (100% Loss Events)

When applying 100% packet loss (e.g., Starlink reconvergence), blackhole routes
are applied to **all 6 participant IPs** on **both routers**. This ensures that
any packet in transit to any participant is dropped, regardless of which router
it's currently traversing.

```bash
# Router A - Block ALL participant traffic
ip route add blackhole 10.1.1.2/32  # Baseline publisher
ip route add blackhole 10.1.1.3/32  # HighPerf publisher
ip route add blackhole 10.1.2.2/32  # Baseline subscriber
ip route add blackhole 10.1.2.3/32  # HighPerf subscriber
ip route add blackhole 10.2.1.2/32  # Baseline server
ip route add blackhole 10.2.1.3/32  # HighPerf server

# Router B - Block ALL participant traffic (same 6 routes)
ip route add blackhole 10.1.1.2/32  # Baseline publisher
ip route add blackhole 10.1.1.3/32  # HighPerf publisher
ip route add blackhole 10.1.2.2/32  # Baseline subscriber
ip route add blackhole 10.1.2.3/32  # HighPerf subscriber
ip route add blackhole 10.2.1.2/32  # Baseline server
ip route add blackhole 10.2.1.3/32  # HighPerf server
```

## Configuration Comparison

### Baseline Configuration (Set A)

```go
BaselineSRTConfig := SRTConfig{
    ConnectionTimeout:      3000 * time.Millisecond,
    PeerIdleTimeout:        30000 * time.Millisecond,
    Latency:                3000 * time.Millisecond,
    RecvLatency:            3000 * time.Millisecond,
    PeerLatency:            3000 * time.Millisecond,
    IoUringEnabled:         false,   // Traditional WriteTo
    IoUringRecvEnabled:     false,   // Traditional ReadFrom
    PacketReorderAlgorithm: "list",  // Linked list packet store
    TLPktDrop:              true,
}
// Client does NOT use io_uring output
```

### HighPerf Configuration (Set B)

```go
HighPerfSRTConfig := SRTConfig{
    ConnectionTimeout:      3000 * time.Millisecond,
    PeerIdleTimeout:        30000 * time.Millisecond,
    Latency:                3000 * time.Millisecond,
    RecvLatency:            3000 * time.Millisecond,
    PeerLatency:            3000 * time.Millisecond,
    IoUringEnabled:         true,    // io_uring for SRT send
    IoUringRecvEnabled:     true,    // io_uring for SRT recv
    PacketReorderAlgorithm: "btree", // B-tree packet store
    BTreeDegree:            32,
    TLPktDrop:              true,
}
// Client uses io_uring output
```

## Metrics Comparison Design

### SRT-Level Metrics (Per Pipeline)

These metrics are collected from each pipeline and compared:

| Metric | Description | Expected Comparison |
|--------|-------------|---------------------|
| `TotalPacketsSent` | Packets sent by client-generator | Should be identical |
| `TotalPacketsRecv` | Packets received by client | May differ slightly |
| `TotalGapsDetected` | Sequence gaps observed | Should be identical (same loss) |
| `TotalPacketsRetrans` | Retransmitted packets | May differ (timing) |
| `RecoveryRate` | Gaps recovered / Gaps detected | Primary comparison metric |
| `NAKDeliveryRate` | NAKs received / NAKs sent | Should be similar |
| `TotalDropsTooLate` | Packets dropped (TSBPD expired) | HighPerf should be ≤ Baseline |
| `TotalPacketsSkippedTSBPD` | Packets never arrived | Should be 0 for both |

### Performance Metrics (Per Pipeline)

These metrics compare performance characteristics:

| Metric | Description | Expected Comparison |
|--------|-------------|---------------------|
| `go_memstats_alloc_bytes` | Current heap allocation | HighPerf may be lower |
| `go_memstats_heap_objects` | Current heap objects | HighPerf may be lower |
| `go_memstats_gc_cpu_fraction` | GC CPU usage | HighPerf should be lower |
| `go_goroutines` | Active goroutines | Should be similar |
| `process_cpu_seconds_total` | Total CPU time | HighPerf may be lower |

### Comparison Output Format

```
=== Parallel Comparison: Starlink-20Mbps ===

Pipeline Configuration:
  Baseline: list + no io_uring (10.1.1.2 → 10.2.1.2:6000 → 10.1.2.2)
  HighPerf: btree + io_uring  (10.1.1.3 → 10.2.1.3:6001 → 10.1.2.3)

Network Events:
  Pattern: starlink (60ms 100% loss at 12,27,42,57s intervals)
  Duration: 90s
  Total Events: 6

=== SRT Metrics Comparison ===

                          Baseline      HighPerf      Diff
  Packets Sent:           114,000       114,000       =
  Packets Received:       113,850       113,920       +0.06%
  Gaps Detected:          9,425         9,425         =
  Retransmissions:        21,500        21,351        -0.69%
  Recovery Rate:          100.0%        100.0%        =
  Drops (too_late):       12            3             -75.0% ✓
  TSBPD Skips:            0             0             =
  NAK Delivery Rate:      99.1%         99.2%         +0.1%

=== Performance Metrics Comparison ===

                          Baseline      HighPerf      Diff
  Heap Allocated (MB):    45.2          38.7          -14.4% ✓
  Heap Objects:           125,432       98,765        -21.3% ✓
  GC CPU Fraction:        0.82%         0.54%         -34.1% ✓
  Goroutines:             42            44            +4.8%
  CPU Time (s):           8.45          7.21          -14.7% ✓

=== Summary ===
  ✓ HighPerf shows 14.4% lower memory usage
  ✓ HighPerf shows 34.1% lower GC overhead
  ✓ HighPerf shows 75.0% fewer late drops
  = Recovery rate identical (100%)
```

## Test Modes

### Mode 1: Normal Comparison Run

```bash
sudo make test-parallel CONFIG=Parallel-Starlink-20Mbps
```

- Duration: 90 seconds (same as single-pipeline Starlink)
- No profiling
- Full metrics comparison
- Quick validation of SRT-level equivalence

### Mode 2: Profiling Comparison Run

The profiling mode runs **sequential 5-minute tests**, one for each profile type.
Only the **server processes** are profiled (not client-generator or client) since
the server handles both receive and send paths and is the most representative
component for performance analysis.

```bash
# Run all profile types sequentially
sudo make test-parallel-profile CONFIG=Parallel-Starlink-20Mbps PROFILES=cpu,heap,allocs

# This will run:
#   1. 5-minute test with CPU profiling enabled on both servers
#   2. 5-minute test with heap profiling enabled on both servers
#   3. 5-minute test with allocs profiling enabled on both servers
```

Each profile run:
1. Starts the servers with the specified pprof profile enabled
2. Runs the test for 5 minutes under load
3. Collects the profile from each server at test end
4. Saves profiles to the output directory

### Profile Types

| Profile | Description | Server Flag | Use Case |
|---------|-------------|-------------|----------|
| `cpu` | CPU usage sampling | `-cpuprofile=file` | Identify hot functions |
| `heap` | Heap memory snapshot | `-memprofile=file` | Current memory usage |
| `allocs` | All allocations | `-memprofile=file -memprofilerate=1` | Allocation rate |
| `block` | Blocking events | `-blockprofile=file` | Lock contention |
| `mutex` | Mutex contention | `-mutexprofile=file` | Lock efficiency |

### Profile Output Structure

```
profiles/
├── Parallel-Starlink-20Mbps-cpu/
│   ├── server-baseline.pprof
│   └── server-highperf.pprof
├── Parallel-Starlink-20Mbps-heap/
│   ├── server-baseline.pprof
│   └── server-highperf.pprof
└── Parallel-Starlink-20Mbps-allocs/
    ├── server-baseline.pprof
    └── server-highperf.pprof
```

### Profile Comparison Commands

After all profile runs complete, compare the profiles:

```bash
# CPU profile comparison - shows functions where HighPerf differs from Baseline
go tool pprof -top -diff_base=profiles/Parallel-Starlink-20Mbps-cpu/server-baseline.pprof \
    profiles/Parallel-Starlink-20Mbps-cpu/server-highperf.pprof

# Heap profile comparison
go tool pprof -top -diff_base=profiles/Parallel-Starlink-20Mbps-heap/server-baseline.pprof \
    profiles/Parallel-Starlink-20Mbps-heap/server-highperf.pprof

# Interactive web UI for detailed analysis
go tool pprof -http=:8080 profiles/Parallel-Starlink-20Mbps-cpu/server-highperf.pprof
```

## Backward Compatibility

### Existing Tests Unaffected

The parallel test infrastructure is designed to be **additive only**:

1. **Existing shell scripts**: `lib.sh` changes add new functions, don't modify existing ones
2. **Existing IP addresses**: `.2` addresses remain unchanged; `.3` addresses are new
3. **Existing blackhole logic**: Single-pipeline tests still use 3 IPs; parallel tests use 6
4. **Existing test configs**: All `NetworkTestConfigs` entries remain unchanged
5. **Existing Makefile targets**: `test-network`, `test-network-all` unchanged

### New Parallel-Specific Code

| File | Changes |
|------|---------|
| `lib.sh` | Add `setup_parallel_ips()`, `set_blackhole_loss_parallel()` functions |
| `test_configs.go` | Add `ParallelTestConfigs` slice (separate from `NetworkTestConfigs`) |
| `config.go` | Add `ParallelTestConfig` struct |
| `test_parallel_mode.go` | New file for parallel test orchestration |
| `parallel_analysis.go` | New file for comparison logic |
| `Makefile` | Add `test-parallel`, `test-parallel-profile` targets |

### Detection Logic

The test orchestrator detects parallel mode via configuration:

```go
type TestConfig struct {
    // ... existing fields ...

    // Parallel test mode (when set, runs two pipelines)
    ParallelMode bool
}
```

When `ParallelMode` is false (default), the existing single-pipeline code path is used.
When `ParallelMode` is true, the new parallel orchestration code is used.

## Implementation Plan

### Phase 1: Network Infrastructure Updates

1. Add `setup_parallel_ips()` to `lib.sh` - adds .3 addresses to namespaces
2. Add `set_blackhole_loss_parallel()` to `lib.sh` - handles 6 IPs on both routers
3. Add `clear_blackhole_loss_parallel()` to `lib.sh` - removes all 6 blackhole routes
4. Add connectivity verification for dual-IP setup
5. **Test**: Verify single-pipeline tests still work unchanged

### Phase 2: Parallel Process Management

1. Create `ParallelTestConfig` struct in `config.go`
2. Create `test_parallel_mode.go` for parallel orchestration
3. Implement process spawning for 6 processes (2 sets of 3)
4. Implement UDS socket naming for 6 metrics endpoints
5. Add synchronization for parallel startup (both pipelines ready before impairment)
6. Implement parallel graceful quiesce (both pipelines drain together)
7. **Test**: Verify parallel processes start and communicate correctly

### Phase 3: Metrics Collection and Comparison

1. Create `ParallelMetricsCollector` that collects from 6 endpoints
2. Create `PipelineAnalysis` struct for per-pipeline results
3. Create `ComparisonResult` struct for diff calculation
4. Implement `PrintComparisonResult()` for formatted output
5. Add JSON output option for comparison results
6. **Test**: Verify metrics comparison produces expected output

### Phase 4: Profiling Support

1. Add pprof flags to server binary (`-cpuprofile`, `-memprofile`, etc.)
2. Implement sequential profile run orchestration
3. Add profile download/collection at each test end
4. Create profile output directory structure
5. Add helper script for profile comparison
6. **Test**: Verify profiles are generated and can be compared

### Phase 5: Test Configurations and Makefile

1. Create `ParallelTestConfigs` slice in `test_configs.go`
2. Add `Parallel-Starlink-5Mbps` configuration
3. Add `Parallel-Starlink-20Mbps` configuration
4. Add Makefile targets: `test-parallel`, `test-parallel-profile`, `test-parallel-list`
5. Update documentation with usage examples
6. **Test**: End-to-end parallel test runs

## Makefile Targets

```makefile
# List parallel test configurations
test-parallel-list:
	@./integration_testing parallel-list

# Run a specific parallel test (normal mode)
test-parallel:
	@./integration_testing parallel-run $(CONFIG)

# Run parallel test with profiling (sequential 5-minute runs per profile type)
# Example: make test-parallel-profile CONFIG=Parallel-Starlink-20Mbps PROFILES=cpu,heap
test-parallel-profile:
	@./integration_testing parallel-run $(CONFIG) --profile=$(PROFILES)

# Compare saved profiles (helper)
profile-compare:
	@./scripts/compare_profiles.sh $(CONFIG) $(PROFILE_TYPE)
```

## Success Criteria

### SRT-Level Metrics

| Metric | Success Criteria |
|--------|------------------|
| Recovery Rate | Both pipelines ≥ 99% |
| TSBPD Skips | Both pipelines = 0 |
| Packets Received | Within 0.1% of each other |
| Gaps Detected | Identical (same network loss) |

### Performance Metrics (HighPerf vs Baseline)

| Metric | Success Criteria |
|--------|------------------|
| Heap Allocation | HighPerf ≤ Baseline |
| GC CPU Fraction | HighPerf ≤ Baseline |
| Drops (too_late) | HighPerf ≤ Baseline |
| CPU Time | HighPerf ≤ Baseline (ideally) |

### Profiling Mode

| Metric | Success Criteria |
|--------|------------------|
| Profile Files | 2 files per profile type (baseline + highperf server) |
| Profile Size | Non-empty, valid pprof format |
| Test Duration | Full 5 minutes per profile type without crash |

## Example Usage

### Quick Comparison Run

```bash
# Run 90-second parallel Starlink test
sudo make test-parallel CONFIG=Parallel-Starlink-20Mbps

# Expected output:
# === Parallel Comparison: Starlink-20Mbps ===
# ... metrics comparison ...
# RESULT: ✓ Both pipelines passed, HighPerf shows improvement in X, Y, Z
```

### Full Profiling Analysis

```bash
# Run sequential 5-minute profile tests (15 minutes total for 3 profiles)
sudo make test-parallel-profile CONFIG=Parallel-Starlink-20Mbps PROFILES=cpu,heap,allocs

# After completion, compare CPU profiles:
go tool pprof -top -diff_base=profiles/Parallel-Starlink-20Mbps-cpu/server-baseline.pprof \
    profiles/Parallel-Starlink-20Mbps-cpu/server-highperf.pprof

# Or use interactive web UI:
go tool pprof -http=:8080 profiles/Parallel-Starlink-20Mbps-cpu/server-highperf.pprof
```

## Related Documents

- `integration_testing_design.md` - Base integration testing framework
- `packet_loss_injection_design.md` - Network impairment patterns
- `graceful_quiesce_design.md` - Graceful shutdown for metrics collection
- `defect10_high_loss_rate.md` - Network topology diagrams (reference)
- `defect12_starlink_negative_metrics.md` - Starlink test fixes (prerequisite)
