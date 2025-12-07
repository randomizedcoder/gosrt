# Integration Testing Metrics Analysis Implementation

## Overview

This document tracks the implementation progress of the metrics analysis framework for GoSRT
integration testing, based on the design in [metrics_analysis_design.md](metrics_analysis_design.md).

**Related Documents**:
- [Metrics Analysis Design](metrics_analysis_design.md) - The design document
- [Integration Testing Design](integration_testing_design.md) - Parent integration testing framework

---

## Core Principle: Fail-Safe by Default

All analysis functions follow the **fail-safe principle**: tests default to FAILED and are only
marked PASSED when all validation checks explicitly confirm success. This prevents false positives
which would destroy confidence in the test suite.

**Implementation pattern** (used throughout):
```go
// Every analysis function:
result := AnalysisResult{Passed: false}  // Start FAILED

// Perform explicit validation checks...
// Track what was validated successfully...

// Only at the end, after ALL checks pass:
if allChecksExplicitlyPassed {
    result.Passed = true
}
```

See [Integration Testing Design - Core Principles](integration_testing_design.md#core-principles) for details.

---

## Implementation Status

### Phase 1: Basic Error and Signal Validation ✅ COMPLETE

| Task | Status | File | Notes |
|------|--------|------|-------|
| Time series data model | ✅ Complete | `analysis.go` | `MetricsTimeSeries`, `TestMetricsTimeSeries`, `DerivedMetrics` |
| Derived metrics computation | ✅ Complete | `analysis.go` | `ComputeDerivedMetrics()` - computes deltas and rates |
| Error counter list | ✅ Complete | `analysis.go` | `AnalysisErrorCounters` - extended list |
| Error counter analysis | ✅ Complete | `analysis.go` | `AnalyzeErrors()` - checks all error counters |
| Positive signal validation | ✅ Complete | `analysis.go` | `ValidatePositiveSignals()` - packets, throughput, ACKs |
| Expected signals computation | ✅ Complete | `analysis.go` | `computeExpectedSignals()` - from bitrate and duration |
| Analysis result aggregation | ✅ Complete | `analysis.go` | `AnalyzeTestMetrics()` - combines all analysis |
| Console output format | ✅ Complete | `analysis.go` | `PrintAnalysisResult()` - formatted console output |
| Test framework integration | ✅ Complete | `test_graceful_shutdown.go` | `runTestWithMetrics()` + `AnalyzeTestResults()` |
| JSON output format | 🔲 Pending | - | For CI/CD integration |

### Phase 2: Statistical Validation

| Task | Status | File | Notes |
|------|--------|------|-------|
| Statistical expectations | 🔲 Pending | - | For network impairment tests |
| Tolerance-based validation | 🔲 Pending | - | ±50% tolerance for netem |
| Loss rate validation | 🔲 Pending | - | |
| Retransmission rate validation | 🔲 Pending | - | |
| NAK behavior validation | 🔲 Pending | - | |
| Recovery rate validation | 🔲 Pending | - | |

### Phase 3: Go Runtime Stability Analysis ✅ COMPLETE

| Task | Status | File | Notes |
|------|--------|------|-------|
| Add `montanaflynn/stats` dependency | ✅ Complete | `go.mod` | v0.7.1 |
| Warmup period detection | ✅ Complete | `runtime_analysis.go` | `WarmupDuration()` |
| Linear regression trend analysis | ✅ Complete | `runtime_analysis.go` | `AnalyzeTrend()` |
| Memory growth validation | ✅ Complete | `runtime_analysis.go` | Heap growth rate |
| Goroutine count validation | ✅ Complete | `runtime_analysis.go` | Goroutine growth rate |
| GC pause time analysis | ✅ Complete | `runtime_analysis.go` | GC pause growth rate |
| CPU variance analysis | ✅ Complete | `runtime_analysis.go` | Coefficient of variation |
| Integration with main analysis | ✅ Complete | `analysis.go` | Auto-runs for tests ≥30 min |

### Phase 4: Integration

| Task | Status | File | Notes |
|------|--------|------|-------|
| `RunTestWithAnalysis()` wrapper | 🔲 Pending | - | |
| Batch analysis for test suites | 🔲 Pending | - | |
| Per-test threshold configuration | 🔲 Pending | - | |

---

## Files Created/Modified

### New Files

| File | Purpose |
|------|---------|
| `contrib/integration_testing/analysis.go` | Main metrics analysis implementation |
| `contrib/integration_testing/runtime_analysis.go` | Go runtime stability analysis (memory, goroutines, GC, CPU) |

### Modified Files

| File | Changes |
|------|---------|
| `contrib/integration_testing/test_graceful_shutdown.go` | Split `runTestWithConfig()` into wrapper + `runTestWithMetrics()`; added `AnalyzeTestResults()` call |

---

## Implementation Details

### Phase 1 Implementation Notes

#### Data Flow

```
runTestWithConfig(config)
    │
    ▼
runTestWithMetrics(config) ─────────▶ TestMetrics collected
    │                                     │
    ▼                                     ▼
return (passed, testMetrics, ...)    AnalyzeTestResults(testMetrics, config, ...)
                                          │
                                          ▼
                                     AnalysisResult
                                          │
                                          ▼
                                     PrintAnalysisResult()
```

#### Time Series Data Model

The time series data model builds on the existing `MetricsSnapshot` and `ComponentMetrics`
structures in `metrics_collector.go`. The `MetricsTimeSeries` wrapper provides a cleaner
interface for analysis:

```go
type MetricsTimeSeries struct {
    Component string              // "server", "client-generator", "client"
    Snapshots []*MetricsSnapshot  // Ordered by time
}

type TestMetricsTimeSeries struct {
    Server          MetricsTimeSeries
    ClientGenerator MetricsTimeSeries
    Client          MetricsTimeSeries
    TestName        string
    StartTime       time.Time
    EndTime         time.Time
    TestConfig      *TestConfig
}
```

#### Derived Metrics

`DerivedMetrics` computes deltas and rates from the time series:

```go
type DerivedMetrics struct {
    // Deltas (final - initial)
    TotalPacketsSent     int64
    TotalPacketsRecv     int64
    TotalPacketsLost     int64
    TotalRetransmissions int64
    TotalNAKsSent        int64
    TotalNAKsRecv        int64
    TotalACKsSent        int64
    TotalACKsRecv        int64
    TotalErrors          int64
    TotalBytesSent       int64
    TotalBytesRecv       int64

    // Rates
    AvgSendRateMbps float64
    AvgRecvRateMbps float64
    AvgLossRate     float64  // packets lost / packets sent
    AvgRetransRate  float64  // retransmissions / packets lost

    Duration     time.Duration
    ErrorsByType map[string]int64
}
```

#### Error Counter Analysis

Extended the error counter list from `metrics_collector.go` to include all known error metrics:

| Category | Counters |
|----------|----------|
| **Send Errors** | `gosrt_pkt_sent_error_total`, `gosrt_pkt_sent_error_marshal_total`, `gosrt_pkt_sent_error_write_total`, `gosrt_pkt_sent_error_iouring_total`, `gosrt_pkt_sent_error_unknown_total` |
| **Receive Errors** | `gosrt_pkt_recv_error_total`, `gosrt_pkt_recv_error_unmarshal_total`, `gosrt_pkt_recv_error_unknown_total`, `gosrt_pkt_recv_control_unknown_total`, `gosrt_pkt_recv_subtype_unknown_total` |
| **Crypto Errors** | `gosrt_crypto_error_encrypt_total`, `gosrt_crypto_error_decrypt_total`, `gosrt_crypto_error_generate_sek_total`, `gosrt_crypto_error_marshal_km_total` |

#### Positive Signal Validation

Validates that expected behaviors occurred:

| Signal | Expectation | Notes |
|--------|-------------|-------|
| **ServerDataFlow** | Server received ≥ MinPacketsRecv or > 0 ACKs | Verifies publisher → server data path |
| **ClientDataFlow** | Client received ≥ MinPacketsRecv | Verifies server → client data path |
| **ACKExchange** | > 0 total ACKs across all components | Confirms bidirectional SRT control path |

#### Prometheus Metrics Matching

The Prometheus metrics use labels (socket_id, type, status, etc.), so the analysis uses prefix-based
matching to sum across all connections:

```go
// getSumByPrefix sums all metric values that start with the given prefix
func getSumByPrefix(snapshot *MetricsSnapshot, prefix string) float64

// getSumByPrefixContaining sums metrics that start with prefix and contain substr
func getSumByPrefixContaining(snapshot *MetricsSnapshot, prefix, substr string) float64
```

Key metric prefixes used:
- `gosrt_connection_packets_received_total` - Packets received (with type/status labels)
- `gosrt_connection_crypto_error_total` - Crypto errors
- `gosrt_connection_recv_data_error_total` - Receive path errors
- `gosrt_connection_send_data_drop_total` - Send path drops

---

## Testing

### Running Analysis

After implementation, analysis is automatically invoked after each integration test:

```bash
# Run integration test with metrics analysis
make test-integration

# Or run specific configuration
make test-integration CONFIG=Default-2Mbps
```

### Expected Output

After the test execution phase, you'll see metrics analysis output:

```
=== Metrics Analysis: Default-2Mbps ===

Error Analysis: ✓ PASSED
  ✓ No unexpected errors

Positive Signals: ✓ PASSED
  ✓ Server received: 7081 packets
  ✓ Client received: 4808 packets
  ✓ ACK exchange verified: 3100 ACKs total

Metrics Summary:
  Server: recv'd 7081 packets, 1539 ACKs
  Client-Generator: recv'd 775 ACKs
  Client: recv'd 4808 packets, 786 ACKs

RESULT: ✓ PASSED: Default-2Mbps
==================================================
```

### Failure Example

If validation fails, you'll see details about the violation:

```
=== Metrics Analysis: IoUring-10Mbps ===

Error Analysis: ✗ FAILED
  ✗ client: gosrt_connection_recv_data_error_total increased by 5 (expected <= 0)

Positive Signals: ✓ PASSED
  ...

RESULT: ✗ FAILED: IoUring-10Mbps (1 violations)
==================================================
```

### Verifying Implementation

```bash
# Build and verify
cd /home/das/Downloads/srt/gosrt
go build ./contrib/integration_testing/...

# Run a quick test
cd contrib/integration_testing
go run . graceful-shutdown-sigint
```

---

---

## Runtime Stability Analysis (Phase 3)

For long-running tests (≥30 minutes), the framework automatically performs runtime stability analysis
using linear regression to detect memory leaks, goroutine leaks, and other resource issues.

### Key Functions

| Function | Purpose |
|----------|---------|
| `WarmupDuration()` | Determines how much initial data to skip (5-15 min based on test duration) |
| `AnalyzeTrend()` | Linear regression using `montanaflynn/stats` to compute growth rate |
| `ComputeVarianceStats()` | Calculates coefficient of variation for CPU stability |
| `ValidateRuntimeStability()` | Main analysis function for a single component |

### Thresholds (Defaults)

| Metric | Threshold | Notes |
|--------|-----------|-------|
| Heap Growth | ≤ 1 MB/hour | Failure if exceeded |
| Goroutine Growth | ≤ 1/hour | Failure if exceeded |
| GC Pause Growth | ≤ 100 ms/hour | Warning only |
| CPU Variance | ≤ 30% CV | Warning only |

### Output Example (Long-Running Test)

```
Runtime Stability:
  server: ✓ STABLE
    Heap: 0.12 MB/hr, Goroutines: 0.0/hr
  client-generator: ✓ STABLE
    Heap: 0.08 MB/hr, Goroutines: 0.0/hr
  client: ✓ STABLE
    Heap: 0.15 MB/hr, Goroutines: 0.0/hr
```

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-07 | Initial implementation document | - |
| 2024-12-07 | Implemented Phase 1: Error and Signal Validation | - |
| 2024-12-07 | Fixed metric name matching for labeled Prometheus metrics | - |
| 2024-12-07 | Verified integration test passing with metrics analysis | - |
| 2024-12-07 | Added montanaflynn/stats dependency (v0.7.1) | - |
| 2024-12-07 | Implemented Phase 3: Go Runtime Stability Analysis | - |

