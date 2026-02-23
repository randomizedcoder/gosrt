# Parallel Comparison Improvement Plan

**Date:** 2025-12-30
**Status:** PHASE 2 COMPLETE - Ready for testing

---

## Recent Additions (Phase 1 Complete)

The following metrics and labels have been implemented to enable improved parallel comparisons:

### New Connection Labels (in all connection metrics)

| Label | Description | Example |
|-------|-------------|---------|
| `peer_type` | Identifies connection role | `"client-generator"`, `"client"`, `"server"` |
| `remote_addr` | Peer's network address | `"10.1.1.2:45678"` |
| `stream_id` | SRT stream identifier | `"test-stream-baseline"` |
| `peer_socket_id` | Remote peer's socket ID | `"0x12345678"` |

### New Timing Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_process_start_time_seconds` | Gauge | Unix timestamp when process started |
| `gosrt_connection_start_time_seconds` | Gauge | Unix timestamp when connection was established |

**Usage for stability checks:**
```promql
# Detect process restarts during test
current_time - gosrt_process_start_time_seconds  # Should match test duration

# Detect connection re-establishments
current_time - gosrt_connection_start_time_seconds  # Should match connection duration
```

### New CPU Performance Metrics (Linux only)

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_process_cpu_user_jiffies_total` | Gauge | User-mode CPU time (jiffies) |
| `gosrt_process_cpu_system_jiffies_total` | Gauge | Kernel-mode CPU time (jiffies) |

**Usage for performance comparison:**
```
# Baseline vs HighPerf CPU efficiency:
# - User jiffies: Higher = more userland processing
# - System jiffies: Higher = more syscalls/kernel ops
# - io_uring should reduce system jiffies compared to traditional syscalls
```

### Example Prometheus Output

```
# Connection with full metadata
gosrt_connection_packets_sent_total{socket_id="0xabc123",instance="baseline-server",type="data",status="success",remote_addr="10.1.1.2:45678",stream_id="test-stream-baseline",peer_socket_id="0xdef456",peer_type="client-generator"} 165000

# Connection start time
gosrt_connection_start_time_seconds{socket_id="0xabc123",instance="baseline-server",remote_addr="10.1.1.2:45678",stream_id="test-stream-baseline",peer_type="client-generator"} 1735580000

# Process metrics
gosrt_process_start_time_seconds 1735579990
gosrt_process_cpu_user_jiffies_total 1250
gosrt_process_cpu_system_jiffies_total 340
```

### Impact on Comparison Implementation

With `peer_type` label now available, server-side connection filtering is straightforward:

```go
// Filter server metrics by peer type - NO HEURISTICS NEEDED
serverCGMetrics := filterMetricsByLabel(serverMetrics, "peer_type", "client-generator")
serverClientMetrics := filterMetricsByLabel(serverMetrics, "peer_type", "client")
```

---

## Current Problem

The current comparison output is hard to read and understand because:

1. **Server metrics are summed across both connections** - We can't distinguish CG→Server from Server→Client stats
2. **No same-connection validation** - We can't compare both ends of a single connection to detect bugs
3. **Single comparison view** - Only baseline vs highperf, no connection-level analysis

## Architecture Review

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Test Architecture                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  BASELINE PIPELINE                        HIGHPERF PIPELINE                 │
│  ─────────────────                        ─────────────────                 │
│                                                                             │
│  ┌──────────────┐                         ┌──────────────┐                  │
│  │ CG-Baseline  │                         │ CG-HighPerf  │                  │
│  │ (1 socket)   │                         │ (1 socket)   │                  │
│  │ 10.1.1.2     │                         │ 10.1.1.3     │                  │
│  └──────┬───────┘                         └──────┬───────┘                  │
│         │                                        │                          │
│         │ Connection A                           │ Connection C             │
│         │ (publish stream)                       │ (publish stream)         │
│         ▼                                        ▼                          │
│  ┌──────────────────────────┐            ┌──────────────────────────┐      │
│  │    Server-Baseline       │            │    Server-HighPerf       │      │
│  │    (2 sockets)           │            │    (2 sockets)           │      │
│  │                          │            │                          │      │
│  │  Socket 1: CG-side       │            │  Socket 1: CG-side       │      │
│  │  Socket 2: Client-side   │            │  Socket 2: Client-side   │      │
│  │    10.2.1.2:6000         │            │    10.2.1.3:6001         │      │
│  └──────────────────────────┘            └──────────────────────────┘      │
│         │                                        │                          │
│         │ Connection B                           │ Connection D             │
│         │ (subscribe stream)                     │ (subscribe stream)       │
│         ▼                                        ▼                          │
│  ┌──────────────┐                         ┌──────────────┐                  │
│  │ Cl-Baseline  │                         │ Cl-HighPerf  │                  │
│  │ (1 socket)   │                         │ (1 socket)   │                  │
│  │ 10.1.2.2     │                         │ 10.1.2.3     │                  │
│  └──────────────┘                         └──────────────┘                  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Connection Inventory

| Connection | From | To | Stream Type | Purpose |
|------------|------|-----|-------------|---------|
| A | CG-Baseline (10.1.1.2) | Server-Baseline (10.2.1.2:6000) | publish | Data ingress |
| B | Server-Baseline (10.2.1.2:6000) | Cl-Baseline (10.1.2.2) | subscribe | Data egress |
| C | CG-HighPerf (10.1.1.3) | Server-HighPerf (10.2.1.3:6001) | publish | Data ingress |
| D | Server-HighPerf (10.2.1.3:6001) | Cl-HighPerf (10.1.2.3) | subscribe | Data egress |

### Socket Count Per Process

| Process | Sockets | Notes |
|---------|---------|-------|
| CG-Baseline | 1 | Single connection to server |
| CG-HighPerf | 1 | Single connection to server |
| Server-Baseline | 2 | CG-side + Client-side |
| Server-HighPerf | 2 | CG-side + Client-side |
| Client-Baseline | 1 | Single connection to server |
| Client-HighPerf | 1 | Single connection to server |

---

## Required Comparisons

### Type A: Cross-Pipeline Comparison (Baseline vs HighPerf)

**Purpose:** Determine which pipeline configuration performs better.

| Comparison | Baseline Side | HighPerf Side | Key Metrics |
|------------|---------------|---------------|-------------|
| A1 | CG-Baseline | CG-HighPerf | packets_sent, retrans, NAKs received |
| A2 | Server-CG-side-Baseline | Server-CG-side-HighPerf | packets_recv, gaps, loss detection |
| A3 | Server-Client-side-Baseline | Server-Client-side-HighPerf | packets_sent, retrans, NAKs received |
| A4 | Client-Baseline | Client-HighPerf | packets_recv, gaps, drops, recovery |

**Output Format:**
```
╔══════════════════════════════════════════════════════════════════════════════╗
║         CROSS-PIPELINE COMPARISON: Baseline vs HighPerf                      ║
╚══════════════════════════════════════════════════════════════════════════════╝

┌─────────────────────────────────────────────────────────────────────────────┐
│ A1: Client-Generator (Baseline vs HighPerf)                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│ Metric                            Baseline     HighPerf         Δ           │
│ packets_sent_total                 165,000      165,000          =          │
│ retransmissions_total               28,365       24,955       -12%          │
│ nak_entries_recv_total              28,179       15,277       -46%  ✓       │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ A2: Server (CG-side) (Baseline vs HighPerf)                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│ [Stats for server receiving from CG only]                                   │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ A3: Server (Client-side) (Baseline vs HighPerf)                             │
├─────────────────────────────────────────────────────────────────────────────┤
│ [Stats for server sending to Client only]                                   │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ A4: Client (Baseline vs HighPerf)                                           │
├─────────────────────────────────────────────────────────────────────────────┤
│ gaps_total                          885            0        -100%  ✓       │
│ packets_recv_total                165,000      165,000          =          │
│ drops_too_old                      12,786        7,979       -38%  ✓       │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

### Type B: Same-Connection Validation (Both Ends)

**Purpose:** Detect bugs or discrepancies where sender/receiver stats don't match on the same connection.

| Comparison | End 1 | End 2 | What to Validate |
|------------|-------|-------|------------------|
| B1 | CG-Baseline | Server-CG-side-Baseline | packets_sent ≈ packets_recv |
| B2 | CG-HighPerf | Server-CG-side-HighPerf | packets_sent ≈ packets_recv |
| B3 | Server-Client-side-Baseline | Client-Baseline | packets_sent ≈ packets_recv |
| B4 | Server-Client-side-HighPerf | Client-HighPerf | packets_sent ≈ packets_recv |

**Output Format:**
```
╔══════════════════════════════════════════════════════════════════════════════╗
║         SAME-CONNECTION VALIDATION: Sender ↔ Receiver                        ║
╚══════════════════════════════════════════════════════════════════════════════╝

┌─────────────────────────────────────────────────────────────────────────────┐
│ B1: Baseline CG → Server (Connection A)                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│ Metric                            CG (send)   Server (recv)      Δ         │
│ packets_data_total                  165,000      163,500      -0.9%         │
│ nak_packets_sent                     28,365       28,300      -0.2%         │
│ retransmissions                      28,365       28,365         =          │
│ STATUS: ✓ OK (discrepancies within expected range)                          │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────────┐
│ B3: Baseline Server → Client (Connection B)                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│ Metric                          Server (send)  Client (recv)     Δ         │
│ packets_data_total                  163,500      160,000      -2.1%         │
│ nak_packets_sent                     31,561       31,500      -0.2%         │
│ STATUS: ✓ OK (discrepancies within expected range)                          │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Implementation Challenges

### Challenge 1: Identifying Server Socket Types ✅ RESOLVED

**Problem:** The server has 2 sockets but we can't tell which is CG-side vs Client-side from Prometheus metrics alone.

**Solution Implemented:** Option A - Added `peer_type` label to all connection metrics.

**New Prometheus labels:**
```
gosrt_connection_packets_sent_total{socket_id="0xabc123",instance="baseline-server",peer_type="client-generator",...}
gosrt_connection_packets_sent_total{socket_id="0xdef456",instance="baseline-server",peer_type="client",...}
```

**Implementation details:**
- Added `PeerType` field to `metrics.ConnectionInfo` struct
- Set during connection registration based on stream ID analysis
- Values: `"client-generator"`, `"client"`, `"server"`
- Included in all connection metric labels via `metrics/handler.go`

---

### Challenge 2: Matching Socket IDs Across Comparison Types

For same-connection validation, we need to correlate:
- CG socket_id → Server's CG-side socket_id
- Server's Client-side socket_id → Client socket_id

**Solution:** Use `peer_type` label to filter metrics before comparison.

```go
// For server metrics, filter by peer_type
serverCGMetrics := filterMetricsByLabel(serverMetrics, "peer_type", "publisher")
serverClientMetrics := filterMetricsByLabel(serverMetrics, "peer_type", "subscriber")
```

---

## Implementation Plan

### Phase 1: Add `peer_type` Label to Metrics ✅ COMPLETE

**Files modified:**
- `metrics/registry.go` - Added `ConnectionInfo` struct with `PeerType`, `RemoteAddr`, `StreamID`, `PeerSocketID`, `StartTime`
- `metrics/handler.go` - Include all new labels in connection metrics
- `metrics/runtime.go` - Added `gosrt_process_start_time_seconds` and CPU jiffies metrics
- `metrics/proc_stat_linux.go` - Efficient parsing of `/proc/self/stat` for CPU metrics
- `connection.go` - Pass connection metadata to metrics registration
- `dial_handshake.go`, `conn_request.go` - Set peer type based on connection role

**New metrics added:**
- Connection labels: `peer_type`, `remote_addr`, `stream_id`, `peer_socket_id`
- `gosrt_connection_start_time_seconds` (per connection)
- `gosrt_process_start_time_seconds` (per process)
- `gosrt_process_cpu_user_jiffies_total` (Linux only)
- `gosrt_process_cpu_system_jiffies_total` (Linux only)

**Timeline:** Completed

### Phase 2: Update Comparison Logic

**Files to modify:**
- `contrib/integration_testing/parallel_analysis.go` - Complete rewrite
  - `CompareParallelPipelinesCrossed()` for Type A comparisons
  - `ValidateSameConnection()` for Type B comparisons
  - New output formatting functions

**New functions:**
```go
// Type A: Cross-pipeline comparison
func CompareParallelPipelinesCrossed(
    baseline, highperf *TestMetrics,
) CrossPipelineComparison

// Type B: Same-connection validation
func ValidateSameConnection(
    senderMetrics, receiverMetrics map[string]float64,
    connectionName string,
) ConnectionValidation

// Filter server metrics by peer type
func FilterServerMetricsByPeerType(
    metrics map[string]float64,
    peerType string, // "publisher" or "subscriber"
) map[string]float64
```

**Timeline:** ~4-6 hours

### Phase 3: Update Output Formatting

**Files to modify:**
- `contrib/integration_testing/parallel_analysis.go` - New print functions
  - `PrintCrossPipelineComparison()`
  - `PrintConnectionValidation()`
  - `PrintCombinedReport()` - Overall summary

**Timeline:** ~2-3 hours

---

## Alternative: Heuristic-Based Approach (No Longer Needed)

~~This approach was considered but is no longer needed since we implemented the proper `peer_type` label solution.~~

The `peer_type` label provides explicit identification without relying on traffic pattern heuristics.

---

## Design Decisions (Phase 2)

1. ~~**Which approach for Challenge 1?**~~ ✅ Resolved - implemented `peer_type` label

2. **Output format:** ✅ DECIDED
   - Separate sections for Type A (Cross-Pipeline) vs Type B (Same-Connection)
   - Summary at bottom
   - Use same colors as mid-test stats (blue for baseline, green for highperf) for consistency

3. **Validation thresholds:** ✅ DECIDED
   - Keep tight to catch bugs early
   - <1% difference = ✓ OK
   - 1-3% difference = ⚠ WARNING
   - >3% difference = ✗ ERROR (potential bug)

4. **Sorting:** ✅ DECIDED
   - Sort metrics by biggest difference first
   - Makes issues immediately visible at top of each section

5. **CPU efficiency comparison:** ✅ YES
   - Compare `gosrt_process_cpu_user_jiffies_total` baseline vs highperf
   - Compare `gosrt_process_cpu_system_jiffies_total` baseline vs highperf
   - Expect: io_uring should reduce system jiffies
   - Expect: optimized code should reduce user jiffies

6. **Stability checks:** ✅ YES
   - Verify process uptime ≈ test duration
   - Verify connection age ≈ expected connection duration
   - If times don't match → TEST FAILED (indicates restart/reconnection)
   - Run stability checks FIRST before other comparisons

---

## Phase 2 Implementation Spec

### Output Structure

```
╔══════════════════════════════════════════════════════════════════════════════╗
║                         STABILITY CHECKS                                      ║
╚══════════════════════════════════════════════════════════════════════════════╝
[Check process uptimes and connection ages match expected test duration]
[If any fail → abort further comparison, report failure]

╔══════════════════════════════════════════════════════════════════════════════╗
║                         CPU EFFICIENCY COMPARISON                             ║
╚══════════════════════════════════════════════════════════════════════════════╝
┌─────────────────────────────────────────────────────────────────────────────┐
│ Process                    User Jiffies    System Jiffies    Total          │
├─────────────────────────────────────────────────────────────────────────────┤
│ baseline-cg                      1250             340          1590         │
│ highperf-cg                       980             220          1200  -24%   │
│ baseline-server                  2100             890          2990         │
│ highperf-server                  1650             450          2100  -30%   │
│ baseline-client                   850             280          1130         │
│ highperf-client                   720             180           900  -20%   │
└─────────────────────────────────────────────────────────────────────────────┘

╔══════════════════════════════════════════════════════════════════════════════╗
║     TYPE A: CROSS-PIPELINE COMPARISON (Baseline vs HighPerf)                 ║
║     [BLUE] Baseline  vs  [GREEN] HighPerf                                    ║
╚══════════════════════════════════════════════════════════════════════════════╝
[A1: CG comparison - sorted by biggest diff]
[A2: Server CG-side comparison - sorted by biggest diff]
[A3: Server Client-side comparison - sorted by biggest diff]
[A4: Client comparison - sorted by biggest diff]

╔══════════════════════════════════════════════════════════════════════════════╗
║     TYPE B: SAME-CONNECTION VALIDATION (Sender ↔ Receiver)                   ║
╚══════════════════════════════════════════════════════════════════════════════╝
[B1: Baseline CG ↔ Server - sorted by biggest diff]
[B2: HighPerf CG ↔ Server - sorted by biggest diff]
[B3: Baseline Server ↔ Client - sorted by biggest diff]
[B4: HighPerf Server ↔ Client - sorted by biggest diff]

╔══════════════════════════════════════════════════════════════════════════════╗
║                              SUMMARY                                          ║
╚══════════════════════════════════════════════════════════════════════════════╝
[Overall pass/fail status]
[Key findings]
[Any warnings or errors]
```

### Color Scheme (matching mid-test stats)

| Element | Color | ANSI Code |
|---------|-------|-----------|
| Baseline | Blue | `\033[34m` |
| HighPerf | Green | `\033[32m` |
| OK (✓) | Green | `\033[32m` |
| Warning (⚠) | Yellow | `\033[33m` |
| Error (✗) | Red | `\033[31m` |
| Headers | Cyan | `\033[36m` |

### Threshold Constants

```go
const (
    // Same-connection validation thresholds
    ThresholdOK      = 0.01  // <1% difference = OK
    ThresholdWarning = 0.03  // 1-3% difference = WARNING
    // >3% = ERROR

    // Stability check thresholds
    ProcessUptimeTolerance    = 5 * time.Second  // Process uptime should be within 5s of test duration
    ConnectionAgeTolerance    = 5 * time.Second  // Connection age should be within 5s of expected
)
```

### Implementation Order

1. **Stability checks** - Parse start times, compare to test duration
2. **CPU comparison** - Parse jiffies from both pipelines, compute deltas
3. **Metric parsing** - Extract `peer_type` label to separate server connections
4. **Type A comparisons** - Cross-pipeline with sorting by delta
5. **Type B comparisons** - Same-connection with tight thresholds
6. **Summary generation** - Aggregate pass/fail/warning counts

---

## Summary

| Phase | Task | Status | Est. Time |
|-------|------|--------|-----------|
| 1 | Add connection metadata labels + timing/CPU metrics | ✅ COMPLETE | Done |
| 2 | Update comparison logic + output formatting | ✅ COMPLETE | Done |
| **Total** | | **All phases complete** | - |

### What's Now Possible

With Phase 1 complete, we can now:

1. **Filter server metrics by peer type** - Use `peer_type` label to separate CG-side from Client-side
2. **Match connection ends** - Use `peer_socket_id` and `remote_addr` to correlate sender/receiver
3. **Verify test stability** - Check process/connection start times match expected test duration
4. **Compare CPU efficiency** - User/system jiffies show relative CPU usage of baseline vs optimized code

### Phase 2 Tasks

| Task | Description | File | Status |
|------|-------------|------|--------|
| 2.1 | Add stability check functions | `parallel_comparison.go` | ✅ Done |
| 2.2 | Add CPU efficiency comparison | `parallel_comparison.go` | ✅ Done |
| 2.3 | Parse `peer_type` label from metrics | `parallel_comparison.go` | ✅ Done |
| 2.4 | Implement Type A cross-pipeline comparisons | `parallel_comparison.go` | ✅ Done |
| 2.5 | Implement Type B same-connection validation | `parallel_comparison.go` | ✅ Done |
| 2.6 | Sort results by biggest difference | `parallel_comparison.go` | ✅ Done |
| 2.7 | Apply color scheme (blue/green) | `parallel_comparison.go` | ✅ Done |
| 2.8 | Integrate into test runner | `test_graceful_shutdown.go` | ✅ Done |

### Implementation Notes

**New file created:** `contrib/integration_testing/parallel_comparison.go`

Contains:
- `PrintEnhancedComparison()` - Main entry point for enhanced comparison output
- `CheckStability()` - Verifies process uptimes match expected test duration
- `CompareCPU()` - Compares CPU jiffies between baseline and highperf
- `GroupMetricsByPeerType()` - Separates server metrics by peer_type label
- `CompareMetricMaps()` - Compares metric maps with sorting by difference
- `printStabilityChecks()` - Outputs stability check results with colors
- `printCPUComparison()` - Outputs CPU efficiency comparison
- `printCrossPipelineComparison()` - Type A comparisons (A1-A4)
- `printSameConnectionValidation()` - Type B comparisons (B1-B4)
- `printComparisonSummary()` - Overall summary with pass/fail status

**Test runner updated:** `test_graceful_shutdown.go`
- Replaced `CompareParallelPipelines()` + `PrintDetailedComparison()` with `PrintEnhancedComparison()`
- Updated all 3 call sites

