# Metrics Analysis Design

## Overview

This document describes the design for automated metrics analysis in the GoSRT integration
testing framework. Metrics analysis validates that SRT behavior matches expectations by
examining Prometheus metrics collected during test runs.

**Related Documents**:
- [Integration Testing Design](integration_testing_design.md) - Parent integration testing framework
- [Metrics and Statistics Design](metrics_and_statistics_design.md) - Counter definitions and Prometheus format
- [Packet Loss Injection Design](packet_loss_injection_design.md) - Network impairment patterns

---

## Objectives

### 1. Error Detection
Identify unexpected errors by monitoring error counters:
- No errors expected in clean network tests
- Expected error patterns in network impairment tests

### 2. Positive Signal Validation
Verify that expected behaviors occurred:
- Packets were sent and received
- Connections were established
- Data throughput matches configured bitrate

### 3. Statistical Validation
For network impairment tests, verify that observed metrics match expected impairment levels:
- Loss rates within statistical tolerance
- Retransmission counts proportional to loss
- NAK generation matches missing packet detection

### 4. Regression Detection
Compare metrics across test runs to detect performance regressions:
- Unexpected increase in error rates
- Degraded throughput
- Increased latency indicators

---

## Architecture

### Data Flow

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Integration Test Orchestrator                        │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Metrics Collection                                  │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                         │
│  │   Server    │  │ Client-Gen  │  │   Client    │                         │
│  │  /metrics   │  │  /metrics   │  │  /metrics   │                         │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘                         │
│         └────────────────┼────────────────┘                                 │
│                          ▼                                                  │
│              ┌───────────────────────┐                                      │
│              │   MetricsTimeSeries   │  Snapshots at t0, t1, t2, ...       │
│              └───────────────────────┘                                      │
└─────────────────────────────────────────────────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Metrics Analysis                                    │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐             │
│  │  Error Checker  │  │ Positive Signal │  │   Statistical   │             │
│  │                 │  │    Validator    │  │    Validator    │             │
│  └────────┬────────┘  └────────┬────────┘  └────────┬────────┘             │
│           └────────────────────┼────────────────────┘                       │
│                                ▼                                            │
│                    ┌───────────────────────┐                                │
│                    │    AnalysisResult     │                                │
│                    │  - Passed/Failed      │                                │
│                    │  - Violations[]       │                                │
│                    │  - Warnings[]         │                                │
│                    └───────────────────────┘                                │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Time Series Data Model

Since the integration test polls `/metrics` multiple times during a test run, we collect
a time series of snapshots:

```go
// MetricsTimeSeries holds all snapshots for one component
type MetricsTimeSeries struct {
    Component  string             // "server", "client-generator", "client"
    Snapshots  []MetricsSnapshot  // Ordered by time
}

// MetricsSnapshot is a single point-in-time capture
type MetricsSnapshot struct {
    Timestamp time.Time
    Point     string             // "initial", "mid-1", "mid-2", "pre-shutdown", "final"
    Metrics   map[string]float64 // metric_name -> value
    Error     error              // Collection error, if any
}

// TestMetricsTimeSeries holds time series for all components
type TestMetricsTimeSeries struct {
    Server          MetricsTimeSeries
    ClientGenerator MetricsTimeSeries
    Client          MetricsTimeSeries

    // Test context
    TestName    string
    TestMode    TestMode          // "clean" or "network"
    Impairment  NetworkImpairment // Only for network mode
    StartTime   time.Time
    EndTime     time.Time
}
```

### Derived Metrics

From the time series, we compute derived metrics:

```go
// DerivedMetrics computed from the time series
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

    // Rates (computed from time series)
    AvgSendRateMbps      float64
    AvgRecvRateMbps      float64
    AvgLossRate          float64  // packets lost / packets sent
    AvgRetransRate       float64  // retransmissions / packets sent

    // Error breakdown
    ErrorsByType         map[string]int64
}

func ComputeDerivedMetrics(ts MetricsTimeSeries) DerivedMetrics {
    if len(ts.Snapshots) < 2 {
        return DerivedMetrics{}
    }

    first := ts.Snapshots[0]
    last := ts.Snapshots[len(ts.Snapshots)-1]
    duration := last.Timestamp.Sub(first.Timestamp)

    // Compute deltas
    dm := DerivedMetrics{
        TotalPacketsSent: int64(last.Metrics["gosrt_pkt_sent_data_total"] -
                                first.Metrics["gosrt_pkt_sent_data_total"]),
        TotalPacketsRecv: int64(last.Metrics["gosrt_pkt_recv_data_total"] -
                                first.Metrics["gosrt_pkt_recv_data_total"]),
        TotalPacketsLost: int64(last.Metrics["gosrt_congestion_recv_pkt_loss_total"] -
                                first.Metrics["gosrt_congestion_recv_pkt_loss_total"]),
        // ... etc
    }

    // Compute rates
    if dm.TotalPacketsSent > 0 {
        dm.AvgLossRate = float64(dm.TotalPacketsLost) / float64(dm.TotalPacketsSent)
    }

    // Compute throughput from bytes
    bytesSent := last.Metrics["gosrt_bytes_sent_data_total"] -
                 first.Metrics["gosrt_bytes_sent_data_total"]
    dm.AvgSendRateMbps = (bytesSent * 8) / duration.Seconds() / 1_000_000

    return dm
}
```

---

## Analysis Components

### 1. Error Counter Analysis

Check that error counters are zero (or within expected bounds):

```go
// ErrorCounters to monitor (from metrics_and_statistics_design.md)
var ErrorCounters = []string{
    // Send path errors
    "gosrt_pkt_sent_error_marshal_total",
    "gosrt_pkt_sent_error_write_total",
    "gosrt_pkt_sent_error_unknown_total",

    // Receive path errors
    "gosrt_pkt_recv_error_unmarshal_total",
    "gosrt_pkt_recv_error_unknown_total",

    // Crypto errors
    "gosrt_crypto_error_encrypt_total",
    "gosrt_crypto_error_decrypt_total",
    "gosrt_crypto_error_generate_sek_total",
    "gosrt_crypto_error_marshal_km_total",

    // Drop counters (may be expected in some tests)
    "gosrt_pkt_drop_too_late_total",
    "gosrt_pkt_drop_buffer_full_total",
}

type ErrorAnalysisResult struct {
    Passed     bool
    Violations []ErrorViolation
}

type ErrorViolation struct {
    Counter   string
    Component string
    Expected  int64  // Usually 0 for clean tests
    Actual    int64
    Message   string
}

func AnalyzeErrors(ts TestMetricsTimeSeries, config TestConfig) ErrorAnalysisResult {
    result := ErrorAnalysisResult{Passed: true}

    for _, component := range []MetricsTimeSeries{ts.Server, ts.ClientGenerator, ts.Client} {
        dm := ComputeDerivedMetrics(component)

        for counter, count := range dm.ErrorsByType {
            expected := getExpectedErrorCount(counter, config)

            if count > expected {
                result.Passed = false
                result.Violations = append(result.Violations, ErrorViolation{
                    Counter:   counter,
                    Component: component.Component,
                    Expected:  expected,
                    Actual:    count,
                    Message:   fmt.Sprintf("%s: %d errors (expected <= %d)", counter, count, expected),
                })
            }
        }
    }

    return result
}
```

### 2. Positive Signal Validation

Verify that expected behaviors occurred:

```go
type PositiveSignalResult struct {
    Passed     bool
    Violations []SignalViolation
}

type SignalViolation struct {
    Signal    string
    Component string
    Expected  string
    Actual    string
    Message   string
}

// PositiveSignals defines what we expect to see
type PositiveSignals struct {
    MinPacketsSent       int64   // At least this many packets sent
    MinPacketsRecv       int64   // At least this many packets received
    MinThroughputMbps    float64 // At least this throughput
    MaxThroughputMbps    float64 // No more than this (sanity check)
    RequireACKs          bool    // ACKs must be exchanged
    RequireNAKsOnLoss    bool    // NAKs expected if loss > 0
}

func ValidatePositiveSignals(ts TestMetricsTimeSeries, config TestConfig) PositiveSignalResult {
    result := PositiveSignalResult{Passed: true}

    expected := computeExpectedSignals(config)

    // Check client-generator sent packets
    cgMetrics := ComputeDerivedMetrics(ts.ClientGenerator)
    if cgMetrics.TotalPacketsSent < expected.MinPacketsSent {
        result.Passed = false
        result.Violations = append(result.Violations, SignalViolation{
            Signal:    "MinPacketsSent",
            Component: "client-generator",
            Expected:  fmt.Sprintf(">= %d", expected.MinPacketsSent),
            Actual:    fmt.Sprintf("%d", cgMetrics.TotalPacketsSent),
            Message:   "Fewer packets sent than expected for configured bitrate",
        })
    }

    // Check client received packets
    clientMetrics := ComputeDerivedMetrics(ts.Client)
    if clientMetrics.TotalPacketsRecv < expected.MinPacketsRecv {
        result.Passed = false
        result.Violations = append(result.Violations, SignalViolation{
            Signal:    "MinPacketsRecv",
            Component: "client",
            Expected:  fmt.Sprintf(">= %d", expected.MinPacketsRecv),
            Actual:    fmt.Sprintf("%d", clientMetrics.TotalPacketsRecv),
            Message:   "Fewer packets received than expected",
        })
    }

    // Check throughput is within expected range
    if clientMetrics.AvgRecvRateMbps < expected.MinThroughputMbps {
        result.Passed = false
        result.Violations = append(result.Violations, SignalViolation{
            Signal:    "MinThroughput",
            Component: "client",
            Expected:  fmt.Sprintf(">= %.2f Mbps", expected.MinThroughputMbps),
            Actual:    fmt.Sprintf("%.2f Mbps", clientMetrics.AvgRecvRateMbps),
            Message:   "Throughput below expected minimum",
        })
    }

    // Verify ACK exchange occurred
    if expected.RequireACKs {
        serverMetrics := ComputeDerivedMetrics(ts.Server)
        if serverMetrics.TotalACKsRecv == 0 {
            result.Passed = false
            result.Violations = append(result.Violations, SignalViolation{
                Signal:    "ACKExchange",
                Component: "server",
                Expected:  "> 0 ACKs received",
                Actual:    "0",
                Message:   "No ACKs received - connection may not be working",
            })
        }
    }

    return result
}

func computeExpectedSignals(config TestConfig) PositiveSignals {
    // Calculate expected packet count from bitrate and duration
    // Assuming ~1316 byte payload per packet
    bytesExpected := float64(config.Bitrate) / 8 * config.TestDuration.Seconds()
    packetsExpected := int64(bytesExpected / 1316)

    // Allow 10% variance for timing
    minPackets := int64(float64(packetsExpected) * 0.9)

    // Throughput should be close to configured bitrate
    minThroughput := float64(config.Bitrate) / 1_000_000 * 0.85 // 85% of target
    maxThroughput := float64(config.Bitrate) / 1_000_000 * 1.15 // 115% of target

    return PositiveSignals{
        MinPacketsSent:    minPackets,
        MinPacketsRecv:    minPackets, // Adjusted for loss in network mode
        MinThroughputMbps: minThroughput,
        MaxThroughputMbps: maxThroughput,
        RequireACKs:       true,
        RequireNAKsOnLoss: config.Mode == TestModeNetwork,
    }
}
```

### 3. Statistical Validation (Network Impairment Tests)

For network impairment tests, verify that observed metrics match expected impairment:

```go
type StatisticalValidationResult struct {
    Passed     bool
    Violations []StatisticalViolation
    Warnings   []StatisticalWarning
}

type StatisticalViolation struct {
    Metric           string
    ExpectedRange    string
    Observed         float64
    ZScore           float64  // How many std deviations from expected
    Message          string
}

type StatisticalWarning struct {
    Metric  string
    Message string
}

// StatisticalExpectation defines expected behavior under impairment
type StatisticalExpectation struct {
    // Loss rate expectations
    ExpectedLossRate    float64 // e.g., 0.02 for 2%
    LossRateTolerance   float64 // e.g., 0.5 means ±50% of expected

    // Retransmission expectations (should be proportional to loss)
    MinRetransRate      float64 // At least this fraction of lost packets retransmitted
    MaxRetransRate      float64 // No more than this (indicates excessive retrans)

    // NAK expectations
    ExpectNAKs          bool
    MinNAKsPerLostPkt   float64 // At least this many NAKs per lost packet
    MaxNAKsPerLostPkt   float64 // No more than this (indicates NAK storms)

    // Recovery expectations
    MinRecoveryRate     float64 // Fraction of lost packets successfully recovered
}

func ValidateStatistical(ts TestMetricsTimeSeries, config TestConfig) StatisticalValidationResult {
    result := StatisticalValidationResult{Passed: true}

    // Only applicable to network mode with impairment
    if config.Mode != TestModeNetwork || config.Impairment.Pattern == "clean" {
        return result
    }

    expected := computeStatisticalExpectations(config.Impairment)
    observed := computeObservedStatistics(ts)

    // Validate loss rate
    if !isWithinTolerance(observed.LossRate, expected.ExpectedLossRate, expected.LossRateTolerance) {
        lowerBound := expected.ExpectedLossRate * (1 - expected.LossRateTolerance)
        upperBound := expected.ExpectedLossRate * (1 + expected.LossRateTolerance)

        result.Passed = false
        result.Violations = append(result.Violations, StatisticalViolation{
            Metric:        "LossRate",
            ExpectedRange: fmt.Sprintf("%.1f%% - %.1f%%", lowerBound*100, upperBound*100),
            Observed:      observed.LossRate,
            Message: fmt.Sprintf(
                "Observed loss rate %.2f%% outside expected range for %.1f%% configured loss",
                observed.LossRate*100, expected.ExpectedLossRate*100),
        })
    }

    // Validate retransmission rate
    if observed.RetransRate < expected.MinRetransRate {
        result.Passed = false
        result.Violations = append(result.Violations, StatisticalViolation{
            Metric:        "RetransRate",
            ExpectedRange: fmt.Sprintf(">= %.1f%%", expected.MinRetransRate*100),
            Observed:      observed.RetransRate,
            Message:       "Too few retransmissions - loss recovery may not be working",
        })
    }

    // Validate NAK behavior
    if expected.ExpectNAKs && observed.NAKsPerLostPacket < expected.MinNAKsPerLostPkt {
        result.Passed = false
        result.Violations = append(result.Violations, StatisticalViolation{
            Metric:        "NAKsPerLostPacket",
            ExpectedRange: fmt.Sprintf(">= %.2f", expected.MinNAKsPerLostPkt),
            Observed:      observed.NAKsPerLostPacket,
            Message:       "Too few NAKs - receiver may not be detecting losses",
        })
    }

    // Warn on NAK storms
    if observed.NAKsPerLostPacket > expected.MaxNAKsPerLostPkt {
        result.Warnings = append(result.Warnings, StatisticalWarning{
            Metric: "NAKsPerLostPacket",
            Message: fmt.Sprintf(
                "High NAK rate (%.2f per lost packet) - possible NAK storm",
                observed.NAKsPerLostPacket),
        })
    }

    // Validate recovery rate
    if observed.RecoveryRate < expected.MinRecoveryRate {
        result.Passed = false
        result.Violations = append(result.Violations, StatisticalViolation{
            Metric:        "RecoveryRate",
            ExpectedRange: fmt.Sprintf(">= %.1f%%", expected.MinRecoveryRate*100),
            Observed:      observed.RecoveryRate,
            Message:       "Poor loss recovery - too many unrecoverable packets",
        })
    }

    return result
}

func computeStatisticalExpectations(imp NetworkImpairment) StatisticalExpectation {
    exp := StatisticalExpectation{
        ExpectedLossRate:  imp.LossRate,
        LossRateTolerance: 0.5, // ±50% tolerance (netem is statistical)
        MinRetransRate:    0.8, // At least 80% of lost packets should trigger retrans
        MaxRetransRate:    3.0, // No more than 3x retransmissions per lost packet
        ExpectNAKs:        imp.LossRate > 0,
        MinNAKsPerLostPkt: 0.5, // At least 0.5 NAKs per lost packet (batching OK)
        MaxNAKsPerLostPkt: 5.0, // More than 5 NAKs per lost packet is a storm
        MinRecoveryRate:   0.95, // 95% of lost packets should be recovered
    }

    // Adjust for latency - higher latency allows more recovery time
    if imp.LatencyProfile == "geo-satellite" || imp.LatencyProfile == "tier3-high" {
        exp.MinRecoveryRate = 0.90 // Slightly lower expectation for high latency
    }

    // Adjust for pattern-based impairment (Starlink, etc.)
    if imp.Pattern == "starlink" {
        // Starlink has 100% loss bursts - recovery depends on buffer size
        exp.LossRateTolerance = 1.0 // Higher tolerance for burst patterns
        exp.MinRecoveryRate = 0.85  // Some packets may be unrecoverable
    }

    return exp
}

type ObservedStatistics struct {
    LossRate          float64 // Packets lost / packets sent
    RetransRate       float64 // Retransmissions / packets lost
    NAKsPerLostPacket float64 // NAKs sent / packets lost
    RecoveryRate      float64 // (Packets lost - unrecoverable) / packets lost
}

func computeObservedStatistics(ts TestMetricsTimeSeries) ObservedStatistics {
    // Compute from client-generator (sender) and client (receiver) perspectives
    sender := ComputeDerivedMetrics(ts.ClientGenerator)
    receiver := ComputeDerivedMetrics(ts.Client)

    stats := ObservedStatistics{}

    if sender.TotalPacketsSent > 0 {
        // Loss rate from receiver's perspective
        stats.LossRate = float64(receiver.TotalPacketsLost) / float64(sender.TotalPacketsSent)
    }

    if receiver.TotalPacketsLost > 0 {
        // Retransmission rate
        stats.RetransRate = float64(sender.TotalRetransmissions) / float64(receiver.TotalPacketsLost)

        // NAKs per lost packet
        stats.NAKsPerLostPacket = float64(receiver.TotalNAKsSent) / float64(receiver.TotalPacketsLost)

        // Recovery rate (packets received / packets that should have been received)
        expectedPackets := sender.TotalPacketsSent
        stats.RecoveryRate = float64(receiver.TotalPacketsRecv) / float64(expectedPackets)
    } else {
        stats.RecoveryRate = 1.0 // No loss = 100% recovery
    }

    return stats
}

func isWithinTolerance(observed, expected, tolerance float64) bool {
    if expected == 0 {
        return observed == 0
    }
    lowerBound := expected * (1 - tolerance)
    upperBound := expected * (1 + tolerance)
    return observed >= lowerBound && observed <= upperBound
}
```

---

## Analysis Result Aggregation

Combine all analysis components into a final result:

```go
type AnalysisResult struct {
    TestName    string
    TestConfig  TestConfig
    Passed      bool

    // Component results
    ErrorAnalysis      ErrorAnalysisResult
    PositiveSignals    PositiveSignalResult
    StatisticalAnalysis StatisticalValidationResult

    // Summary
    TotalViolations int
    TotalWarnings   int
    Summary         string
}

func AnalyzeTestMetrics(ts TestMetricsTimeSeries, config TestConfig) AnalysisResult {
    errorResult := AnalyzeErrors(ts, config)
    signalResult := ValidatePositiveSignals(ts, config)
    statsResult := ValidateStatistical(ts, config)

    result := AnalysisResult{
        TestName:            config.Name,
        TestConfig:          config,
        ErrorAnalysis:       errorResult,
        PositiveSignals:     signalResult,
        StatisticalAnalysis: statsResult,
    }

    // Aggregate pass/fail
    result.Passed = errorResult.Passed && signalResult.Passed && statsResult.Passed

    // Count violations and warnings
    result.TotalViolations = len(errorResult.Violations) +
                             len(signalResult.Violations) +
                             len(statsResult.Violations)
    result.TotalWarnings = len(statsResult.Warnings)

    // Generate summary
    if result.Passed {
        result.Summary = fmt.Sprintf("PASSED: %s", config.Name)
        if result.TotalWarnings > 0 {
            result.Summary += fmt.Sprintf(" (%d warnings)", result.TotalWarnings)
        }
    } else {
        result.Summary = fmt.Sprintf("FAILED: %s (%d violations)", config.Name, result.TotalViolations)
    }

    return result
}
```

---

## Output Formats

### Console Output

```
=== Metrics Analysis: Net-LargeBuf-Loss5pct-10Mbps ===

Error Analysis: PASSED
  ✓ No unexpected errors

Positive Signals: PASSED
  ✓ Packets sent: 75,234 (expected >= 68,000)
  ✓ Packets received: 71,472 (expected >= 64,600)
  ✓ Throughput: 9.52 Mbps (expected >= 8.5 Mbps)
  ✓ ACK exchange verified

Statistical Validation: PASSED
  ✓ Loss rate: 4.8% (expected 2.5% - 7.5%)
  ✓ Retransmission rate: 92% of lost packets
  ✓ NAKs per lost packet: 1.2
  ✓ Recovery rate: 97.3%
  ⚠ Warning: High NAK rate (1.2 per lost packet) - monitor for NAK storms

RESULT: PASSED (1 warning)
```

### JSON Output

```json
{
  "testName": "Net-LargeBuf-Loss5pct-10Mbps",
  "passed": true,
  "totalViolations": 0,
  "totalWarnings": 1,
  "errorAnalysis": {
    "passed": true,
    "violations": []
  },
  "positiveSignals": {
    "passed": true,
    "violations": []
  },
  "statisticalAnalysis": {
    "passed": true,
    "violations": [],
    "warnings": [
      {
        "metric": "NAKsPerLostPacket",
        "message": "High NAK rate (1.2 per lost packet) - possible NAK storm"
      }
    ]
  },
  "derivedMetrics": {
    "sender": {
      "totalPacketsSent": 75234,
      "totalRetransmissions": 3521,
      "avgSendRateMbps": 9.98
    },
    "receiver": {
      "totalPacketsRecv": 71472,
      "totalPacketsLost": 3762,
      "avgRecvRateMbps": 9.52,
      "lossRate": 0.048,
      "recoveryRate": 0.973
    }
  }
}
```

---

## Integration with Test Framework

### Metrics Analysis Hook

```go
func RunTestWithAnalysis(config TestConfig) (TestResult, AnalysisResult) {
    // Run the test (existing implementation)
    testResult, metricsTimeSeries := RunIntegrationTest(config)

    // Analyze metrics
    analysisResult := AnalyzeTestMetrics(metricsTimeSeries, config)

    // Combine results
    if testResult.Passed && !analysisResult.Passed {
        testResult.Passed = false
        testResult.FailureReason = "Metrics analysis failed: " + analysisResult.Summary
    }

    return testResult, analysisResult
}
```

### Batch Analysis

```go
func RunTestSuiteWithAnalysis(configs []TestConfig) SuiteResult {
    var results []struct {
        Test     TestResult
        Analysis AnalysisResult
    }

    for _, config := range configs {
        testResult, analysisResult := RunTestWithAnalysis(config)
        results = append(results, struct {
            Test     TestResult
            Analysis AnalysisResult
        }{testResult, analysisResult})
    }

    // Generate suite summary
    passed := 0
    failed := 0
    warnings := 0
    for _, r := range results {
        if r.Analysis.Passed {
            passed++
        } else {
            failed++
        }
        warnings += r.Analysis.TotalWarnings
    }

    return SuiteResult{
        TotalTests:    len(configs),
        Passed:        passed,
        Failed:        failed,
        TotalWarnings: warnings,
        Results:       results,
    }
}
```

---

## Go Runtime Metrics Analysis

For long-running tests (1 hour+), we must verify that Go runtime metrics remain stable and don't
indicate resource leaks. The GoSRT library uses `sync.Pool` extensively for packet buffers, so we
expect memory to grow rapidly during warmup, then stabilize.

### Runtime Metrics Collected

The `/metrics` endpoint exposes Go runtime metrics (similar to `promhttp` defaults):

```go
// RuntimeMetrics collected at each snapshot
type RuntimeMetrics struct {
    // Memory
    HeapAllocBytes   uint64  // go_memstats_heap_alloc_bytes
    HeapInuseBytes   uint64  // go_memstats_heap_inuse_bytes
    HeapSysBytes     uint64  // go_memstats_heap_sys_bytes
    StackInuseBytes  uint64  // go_memstats_stack_inuse_bytes

    // GC
    GCPauseNsTotal   uint64  // go_gc_duration_seconds (sum, converted to ns)
    NumGC            uint64  // go_memstats_num_gc

    // Goroutines
    NumGoroutines    int     // go_goroutines

    // CPU (if available via pprof)
    CPUUsagePercent  float64 // Computed from process CPU time
}
```

### Stability Analysis Approach

For long-running tests, we use **linear regression** to detect trends:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  Memory Usage Over Time                                                     │
│                                                                             │
│  MB │                                                                       │
│ 200 │                      ┌─────────────────────────────────────────────   │
│     │                     /                                                 │
│ 150 │                    /   Stable phase (fit line here)                   │
│     │                   /    Gradient should be ~0                          │
│ 100 │         ┌────────/                                                    │
│     │        /                                                              │
│  50 │   ┌───/                                                               │
│     │  /     Warmup (sync.Pool filling)                                     │
│   0 │─/─────────────────────────────────────────────────────────────────▶   │
│     0    5min   10min                                            Time       │
│         └─────┘                                                             │
│         Ignore warmup period                                                │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Warmup Period

The first portion of a long-running test is ignored for stability analysis:

```go
// WarmupDuration determines how much to skip based on test duration
func WarmupDuration(testDuration time.Duration) time.Duration {
    switch {
    case testDuration >= 12*time.Hour:
        return 15 * time.Minute  // 12h+ test: skip first 15 min
    case testDuration >= 1*time.Hour:
        return 10 * time.Minute  // 1h+ test: skip first 10 min
    case testDuration >= 30*time.Minute:
        return 5 * time.Minute   // 30m+ test: skip first 5 min
    default:
        return 0                 // Shorter tests: no warmup skip
    }
}
```

### Linear Regression for Trend Detection

We use the [montanaflynn/stats](https://github.com/montanaflynn/stats) library for statistical
analysis. It's well-tested, has no dependencies, and provides linear regression, variance
calculation, and other statistical functions we need.

```go
import "github.com/montanaflynn/stats"

// TrendAnalysis results from linear regression
type TrendAnalysis struct {
    Slope     float64 // Growth rate per second (positive = growing)
    Intercept float64 // Initial value
    RSquared  float64 // Goodness of fit (0-1, higher = better fit)
}

// AnalyzeTrend performs linear regression on time series data
func AnalyzeTrend(timestamps []time.Time, values []float64) (TrendAnalysis, error) {
    if len(timestamps) < 2 {
        return TrendAnalysis{}, fmt.Errorf("need at least 2 data points")
    }

    // Convert to stats.Series (X = seconds from start, Y = metric value)
    startTime := timestamps[0]
    series := make(stats.Series, len(timestamps))
    for i, t := range timestamps {
        series[i] = stats.Coordinate{
            X: t.Sub(startTime).Seconds(),
            Y: values[i],
        }
    }

    // Perform linear regression
    regressionLine, err := stats.LinearRegression(series)
    if err != nil {
        return TrendAnalysis{}, fmt.Errorf("linear regression failed: %w", err)
    }

    // Extract slope from regression line (slope = (y2-y1)/(x2-x1))
    if len(regressionLine) < 2 {
        return TrendAnalysis{}, fmt.Errorf("regression produced insufficient points")
    }

    slope := (regressionLine[1].Y - regressionLine[0].Y) /
             (regressionLine[1].X - regressionLine[0].X)
    intercept := regressionLine[0].Y

    // Calculate R-squared (coefficient of determination)
    yValues := make(stats.Float64Data, len(values))
    copy(yValues, values)

    mean, _ := stats.Mean(yValues)

    ssTotal := 0.0
    ssResidual := 0.0
    for i, v := range values {
        ssTotal += (v - mean) * (v - mean)
        predicted := slope*series[i].X + intercept
        ssResidual += (v - predicted) * (v - predicted)
    }

    rSquared := 0.0
    if ssTotal > 0 {
        rSquared = 1 - (ssResidual / ssTotal)
    }

    return TrendAnalysis{
        Slope:     slope,
        Intercept: intercept,
        RSquared:  rSquared,
    }, nil
}

// ComputeVariance calculates coefficient of variation (CV) for stability analysis
func ComputeVariance(values []float64) (mean, cv float64, err error) {
    data := stats.Float64Data(values)

    mean, err = stats.Mean(data)
    if err != nil {
        return 0, 0, err
    }

    stdDev, err := stats.StandardDeviation(data)
    if err != nil {
        return 0, 0, err
    }

    // Coefficient of variation = stddev / mean * 100 (as percentage)
    if mean != 0 {
        cv = (stdDev / mean) * 100
    }

    return mean, cv, nil
}

// GetMinMax returns min and max values for peak detection
func GetMinMax(values []float64) (min, max float64, err error) {
    data := stats.Float64Data(values)

    min, err = stats.Min(data)
    if err != nil {
        return 0, 0, err
    }

    max, err = stats.Max(data)
    if err != nil {
        return 0, 0, err
    }

    return min, max, nil
}
```

**Why montanaflynn/stats?**
- Well-tested (100% code coverage)
- No external dependencies
- MIT licensed
- Provides: `LinearRegression`, `Mean`, `StandardDeviation`, `Variance`, `Min`, `Max`, `Percentile`
- Active maintenance

### Stability Thresholds

Define acceptable growth rates for each metric:

```go
type RuntimeStabilityThresholds struct {
    // Memory: max bytes/hour growth (after warmup)
    // 0 = no growth allowed, small positive = some growth OK
    MaxHeapGrowthBytesPerHour  int64   // Default: 1 MB/hour
    MaxStackGrowthBytesPerHour int64   // Default: 100 KB/hour

    // Goroutines: max growth rate
    MaxGoroutineGrowthPerHour  float64 // Default: 0 (should not grow)

    // GC: max increase in GC pause time per hour
    MaxGCPauseGrowthMsPerHour  float64 // Default: 100ms/hour

    // CPU: max variance in CPU usage (coefficient of variation)
    MaxCPUVariancePercent      float64 // Default: 20%
}

var DefaultRuntimeThresholds = RuntimeStabilityThresholds{
    MaxHeapGrowthBytesPerHour:  1 * 1024 * 1024, // 1 MB/hour
    MaxStackGrowthBytesPerHour: 100 * 1024,      // 100 KB/hour
    MaxGoroutineGrowthPerHour:  0.0,             // No growth
    MaxGCPauseGrowthMsPerHour:  100.0,           // 100ms/hour
    MaxCPUVariancePercent:      20.0,            // 20% variance
}
```

### Runtime Stability Validation

```go
type RuntimeStabilityResult struct {
    Passed     bool
    Component  string
    Violations []RuntimeViolation
    Warnings   []RuntimeWarning
    Summary    RuntimeSummary
}

type RuntimeViolation struct {
    Metric      string
    GrowthRate  float64 // per hour
    Threshold   float64
    Message     string
}

type RuntimeWarning struct {
    Metric  string
    Message string
}

// RuntimeSummary provides a snapshot of runtime health
type RuntimeSummary struct {
    // Memory
    InitialHeapMB       float64
    FinalHeapMB         float64
    PeakHeapMB          float64
    HeapGrowthMBPerHour float64
    HeapStable          bool

    // Goroutines
    InitialGoroutines   int
    FinalGoroutines     int
    PeakGoroutines      int
    GoroutineGrowthRate float64
    GoroutinesStable    bool

    // GC
    TotalGCPauseMs      float64
    AvgGCPauseMs        float64
    GCPauseGrowthRate   float64
    GCStable            bool

    // CPU
    AvgCPUPercent       float64
    CPUVariancePercent  float64
    CPUStable           bool
}

func ValidateRuntimeStability(ts MetricsTimeSeries, testDuration time.Duration,
                               thresholds RuntimeStabilityThresholds) RuntimeStabilityResult {
    result := RuntimeStabilityResult{Passed: true, Component: ts.Component}

    // Skip if test is too short for stability analysis
    if testDuration < 30*time.Minute {
        result.Summary.HeapStable = true // Assume stable for short tests
        result.Summary.GoroutinesStable = true
        result.Summary.GCStable = true
        result.Summary.CPUStable = true
        return result
    }

    // Extract data points after warmup
    warmup := WarmupDuration(testDuration)
    stableSnapshots := filterAfterWarmup(ts.Snapshots, warmup)

    if len(stableSnapshots) < 3 {
        result.Warnings = append(result.Warnings, RuntimeWarning{
            Metric:  "samples",
            Message: "Insufficient samples after warmup for stability analysis",
        })
        return result
    }

    // Analyze heap memory trend
    heapTrend, err := analyzeMetricTrend(stableSnapshots, "go_memstats_heap_alloc_bytes")
    if err != nil {
        result.Warnings = append(result.Warnings, RuntimeWarning{
            Metric:  "HeapMemory",
            Message: fmt.Sprintf("Could not analyze heap trend: %v", err),
        })
    } else {
        result.Summary.HeapGrowthMBPerHour = heapTrend.Slope * 3600 / (1024 * 1024)
        result.Summary.HeapStable = heapTrend.Slope*3600 <= float64(thresholds.MaxHeapGrowthBytesPerHour)

        if !result.Summary.HeapStable {
            result.Passed = false
            result.Violations = append(result.Violations, RuntimeViolation{
                Metric:     "HeapMemory",
                GrowthRate: result.Summary.HeapGrowthMBPerHour,
                Threshold:  float64(thresholds.MaxHeapGrowthBytesPerHour) / (1024 * 1024),
                Message: fmt.Sprintf("Heap growing at %.2f MB/hour (max: %.2f MB/hour) - possible memory leak",
                    result.Summary.HeapGrowthMBPerHour,
                    float64(thresholds.MaxHeapGrowthBytesPerHour)/(1024*1024)),
            })
        }
    }

    // Analyze goroutine trend
    goroutineTrend, err := analyzeMetricTrend(stableSnapshots, "go_goroutines")
    if err == nil {
        result.Summary.GoroutineGrowthRate = goroutineTrend.Slope * 3600
        result.Summary.GoroutinesStable = goroutineTrend.Slope*3600 <= thresholds.MaxGoroutineGrowthPerHour

        if !result.Summary.GoroutinesStable {
            result.Passed = false
            result.Violations = append(result.Violations, RuntimeViolation{
                Metric:     "Goroutines",
                GrowthRate: result.Summary.GoroutineGrowthRate,
                Threshold:  thresholds.MaxGoroutineGrowthPerHour,
                Message: fmt.Sprintf("Goroutines growing at %.1f/hour - possible goroutine leak",
                    result.Summary.GoroutineGrowthRate),
            })
        }
    }

    // Analyze GC pause time trend
    gcTrend, err := analyzeMetricTrend(stableSnapshots, "go_gc_duration_seconds_sum")
    if err == nil {
        result.Summary.GCPauseGrowthRate = gcTrend.Slope * 3600 * 1000 // Convert to ms/hour
        result.Summary.GCStable = result.Summary.GCPauseGrowthRate <= thresholds.MaxGCPauseGrowthMsPerHour

        // GC pause growth is a warning, not a failure (can be influenced by system load)
        if !result.Summary.GCStable {
            result.Warnings = append(result.Warnings, RuntimeWarning{
                Metric: "GCPause",
                Message: fmt.Sprintf("GC pause time growing at %.1f ms/hour (threshold: %.1f ms/hour)",
                    result.Summary.GCPauseGrowthRate, thresholds.MaxGCPauseGrowthMsPerHour),
            })
        }
    }

    // Analyze CPU variance using stats library
    cpuValues := extractMetricValues(stableSnapshots, "process_cpu_seconds_total")
    avgCPU, cpuCV, err := ComputeVariance(cpuValues)
    if err == nil {
        result.Summary.AvgCPUPercent = avgCPU
        result.Summary.CPUVariancePercent = cpuCV
        result.Summary.CPUStable = cpuCV <= thresholds.MaxCPUVariancePercent

        // CPU variance is a warning, not a failure
        if !result.Summary.CPUStable {
            result.Warnings = append(result.Warnings, RuntimeWarning{
                Metric: "CPUVariance",
                Message: fmt.Sprintf("CPU usage variance %.1f%% (threshold: %.1f%%)",
                    cpuCV, thresholds.MaxCPUVariancePercent),
            })
        }
    }

    // Populate summary with initial/final/peak values using stats.Min/Max
    populateSummaryStats(&result.Summary, ts.Snapshots)

    return result
}

func analyzeMetricTrend(snapshots []MetricsSnapshot, metric string) (TrendAnalysis, error) {
    timestamps := make([]time.Time, len(snapshots))
    values := make([]float64, len(snapshots))

    for i, s := range snapshots {
        timestamps[i] = s.Timestamp
        values[i] = s.Metrics[metric]
    }

    return AnalyzeTrend(timestamps, values)
}

func populateSummaryStats(summary *RuntimeSummary, snapshots []MetricsSnapshot) {
    if len(snapshots) == 0 {
        return
    }

    // Extract heap values for min/max/initial/final
    heapValues := make([]float64, len(snapshots))
    goroutineValues := make([]float64, len(snapshots))

    for i, s := range snapshots {
        heapValues[i] = s.Metrics["go_memstats_heap_alloc_bytes"]
        goroutineValues[i] = s.Metrics["go_goroutines"]
    }

    // Use stats library for min/max
    _, peakHeap, _ := GetMinMax(heapValues)
    _, peakGoroutines, _ := GetMinMax(goroutineValues)

    summary.InitialHeapMB = heapValues[0] / (1024 * 1024)
    summary.FinalHeapMB = heapValues[len(heapValues)-1] / (1024 * 1024)
    summary.PeakHeapMB = peakHeap / (1024 * 1024)

    summary.InitialGoroutines = int(goroutineValues[0])
    summary.FinalGoroutines = int(goroutineValues[len(goroutineValues)-1])
    summary.PeakGoroutines = int(peakGoroutines)
}
```

### Runtime Stability Output

#### Console Format

```
=== Runtime Stability Analysis: LongDuration-Default-2Mbps (12h) ===

Memory Analysis:
  Initial Heap:     45.2 MB
  Final Heap:       48.7 MB
  Peak Heap:        52.1 MB
  Growth Rate:      0.29 MB/hour
  Status:           ✓ STABLE (threshold: 1.0 MB/hour)

Goroutine Analysis:
  Initial:          12
  Final:            12
  Peak:             15
  Growth Rate:      0.0/hour
  Status:           ✓ STABLE

GC Analysis:
  Total GC Pause:   1,234 ms
  Avg GC Pause:     2.1 ms
  Pause Growth:     8.5 ms/hour
  Status:           ✓ STABLE

CPU Analysis:
  Average CPU:      12.3%
  Variance:         8.5%
  Status:           ✓ STABLE

RUNTIME STABILITY: PASSED
```

#### JSON Format

```json
{
  "component": "server",
  "passed": true,
  "summary": {
    "memory": {
      "initialHeapMB": 45.2,
      "finalHeapMB": 48.7,
      "peakHeapMB": 52.1,
      "growthMBPerHour": 0.29,
      "stable": true
    },
    "goroutines": {
      "initial": 12,
      "final": 12,
      "peak": 15,
      "growthPerHour": 0.0,
      "stable": true
    },
    "gc": {
      "totalPauseMs": 1234,
      "avgPauseMs": 2.1,
      "pauseGrowthMsPerHour": 8.5,
      "stable": true
    },
    "cpu": {
      "avgPercent": 12.3,
      "variancePercent": 8.5,
      "stable": true
    }
  },
  "violations": [],
  "warnings": []
}
```

### Integration with Test Summary

For each test, produce a consolidated runtime summary:

```go
type TestRuntimeSummary struct {
    TestName     string
    TestDuration time.Duration

    // Per-component summaries
    Server          RuntimeSummary
    ClientGenerator RuntimeSummary
    Client          RuntimeSummary

    // Aggregate status
    AllComponentsStable bool
    TotalViolations     int
    TotalWarnings       int
}

func GenerateTestRuntimeSummary(ts TestMetricsTimeSeries,
                                 config TestConfig) TestRuntimeSummary {
    summary := TestRuntimeSummary{
        TestName:     config.Name,
        TestDuration: config.TestDuration,
    }

    // Analyze each component
    serverResult := ValidateRuntimeStability(ts.Server, config.TestDuration, DefaultRuntimeThresholds)
    cgResult := ValidateRuntimeStability(ts.ClientGenerator, config.TestDuration, DefaultRuntimeThresholds)
    clientResult := ValidateRuntimeStability(ts.Client, config.TestDuration, DefaultRuntimeThresholds)

    summary.Server = serverResult.Summary
    summary.ClientGenerator = cgResult.Summary
    summary.Client = clientResult.Summary

    summary.AllComponentsStable = serverResult.Passed && cgResult.Passed && clientResult.Passed
    summary.TotalViolations = len(serverResult.Violations) + len(cgResult.Violations) + len(clientResult.Violations)
    summary.TotalWarnings = len(serverResult.Warnings) + len(cgResult.Warnings) + len(clientResult.Warnings)

    return summary
}
```

### Expected Behavior for GoSRT

Given GoSRT's `sync.Pool` optimizations:

| Metric | Expected Behavior |
|--------|-------------------|
| **Heap Memory** | Rapid growth in first 5-10 min as pools fill, then flat. Small growth acceptable (1 MB/hour) due to GC and pool eviction. |
| **Goroutines** | Should remain constant after connections established. Any growth indicates a leak. |
| **GC Pause** | Should remain proportional to heap size. Growth indicates memory pressure. |
| **CPU** | Should remain proportional to throughput. High variance indicates contention issues. |

### Failure Examples

```
=== Runtime Stability Analysis: LongDuration-Stress-10Mbps (12h) ===

Memory Analysis:
  Initial Heap:     50.2 MB
  Final Heap:       892.4 MB    ❌
  Peak Heap:        892.4 MB
  Growth Rate:      70.2 MB/hour
  Status:           ❌ UNSTABLE (threshold: 1.0 MB/hour)

VIOLATION: Heap growing at 70.2 MB/hour - possible memory leak

Goroutine Analysis:
  Initial:          12
  Final:            1,847       ❌
  Peak:             1,847
  Growth Rate:      152.9/hour
  Status:           ❌ UNSTABLE

VIOLATION: Goroutines growing at 152.9/hour - possible goroutine leak

RUNTIME STABILITY: FAILED (2 violations)
```

---

## Configuration

### Analysis Thresholds

Thresholds can be configured per test or globally:

```go
type AnalysisThresholds struct {
    // Error thresholds
    MaxUnexpectedErrors int64

    // Positive signal thresholds
    MinPacketDeliveryRate float64 // Default: 0.90 (90%)
    ThroughputTolerance   float64 // Default: 0.15 (±15%)

    // Statistical thresholds
    LossRateTolerance     float64 // Default: 0.50 (±50%)
    MinRetransRate        float64 // Default: 0.80 (80%)
    MaxNAKsPerLostPacket  float64 // Default: 5.0
    MinRecoveryRate       float64 // Default: 0.95 (95%)
}

var DefaultThresholds = AnalysisThresholds{
    MaxUnexpectedErrors:   0,
    MinPacketDeliveryRate: 0.90,
    ThroughputTolerance:   0.15,
    LossRateTolerance:     0.50,
    MinRetransRate:        0.80,
    MaxNAKsPerLostPacket:  5.0,
    MinRecoveryRate:       0.95,
}

// Per-test override
type TestConfig struct {
    // ... existing fields ...

    // Analysis configuration
    AnalysisThresholds *AnalysisThresholds // nil = use defaults
}
```

---

## Implementation Phases

### Phase 1: Basic Error and Signal Validation
- Implement error counter analysis
- Implement positive signal validation
- Integration with existing test framework
- Console and JSON output

### Phase 2: Statistical Validation
- Implement statistical expectations calculation
- Implement tolerance-based validation
- Add support for pattern-based impairment (Starlink)
- Warning system for anomalies

### Phase 3: Go Runtime Stability Analysis
- Implement warmup period detection
- Implement linear regression for trend analysis
- Memory growth rate validation (heap, stack)
- Goroutine count stability validation
- GC pause time analysis
- CPU variance analysis
- Per-component and aggregate summaries

### Phase 4: Advanced Features
- Historical comparison (regression detection)
- Trend analysis across test runs
- Configurable thresholds per test
- Dashboard integration
- Automated alerting for regressions

---

## Change Log

| Date | Change | Author |
|------|--------|--------|
| 2024-12-06 | Initial design document | - |
| 2024-12-06 | Added Go runtime metrics analysis for long-running tests | - |
| 2024-12-06 | Use montanaflynn/stats library for statistical functions | - |

