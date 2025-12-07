package main

import (
	"fmt"
	"strings"
	"time"
)

// Note: strings package is used for metric name prefix/substring matching

// MetricsTimeSeries wraps ComponentMetrics for analysis
type MetricsTimeSeries struct {
	Component string             // "server", "client-generator", "client"
	Snapshots []*MetricsSnapshot // Ordered by time
}

// TestMetricsTimeSeries holds time series for all components
type TestMetricsTimeSeries struct {
	Server          MetricsTimeSeries
	ClientGenerator MetricsTimeSeries
	Client          MetricsTimeSeries

	// Test context
	TestName   string
	StartTime  time.Time
	EndTime    time.Time
	TestConfig *TestConfig
}

// NewTestMetricsTimeSeries creates a TestMetricsTimeSeries from TestMetrics
func NewTestMetricsTimeSeries(tm *TestMetrics, testName string, config *TestConfig, startTime, endTime time.Time) *TestMetricsTimeSeries {
	return &TestMetricsTimeSeries{
		Server: MetricsTimeSeries{
			Component: tm.Server.Component,
			Snapshots: tm.Server.Snapshots,
		},
		ClientGenerator: MetricsTimeSeries{
			Component: tm.ClientGenerator.Component,
			Snapshots: tm.ClientGenerator.Snapshots,
		},
		Client: MetricsTimeSeries{
			Component: tm.Client.Component,
			Snapshots: tm.Client.Snapshots,
		},
		TestName:   testName,
		StartTime:  startTime,
		EndTime:    endTime,
		TestConfig: config,
	}
}

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

	// Bytes sent/received
	TotalBytesSent int64
	TotalBytesRecv int64

	// Rates (computed from time series)
	AvgSendRateMbps float64
	AvgRecvRateMbps float64
	AvgLossRate     float64 // packets lost / packets sent
	AvgRetransRate  float64 // retransmissions / packets lost

	// Duration
	Duration time.Duration

	// Error breakdown
	ErrorsByType map[string]int64
}

// ComputeDerivedMetrics computes derived metrics from a time series
func ComputeDerivedMetrics(ts MetricsTimeSeries) DerivedMetrics {
	dm := DerivedMetrics{
		ErrorsByType: make(map[string]int64),
	}

	if len(ts.Snapshots) < 2 {
		return dm
	}

	// Find first and last successful snapshots
	var first, last *MetricsSnapshot
	for _, s := range ts.Snapshots {
		if s.Error == nil {
			if first == nil {
				first = s
			}
			last = s
		}
	}

	if first == nil || last == nil || first == last {
		return dm
	}

	dm.Duration = last.Timestamp.Sub(first.Timestamp)

	// The Prometheus metrics have labels (socket_id, type, status, etc.)
	// We need to sum across all connections for each metric type

	// Packet counters - sum data packets with status=success across all socket_ids
	// Note: The current Prometheus handler only exports packets_received, not packets_sent
	// For packets sent, we need to use a different metric or estimate
	dm.TotalPacketsRecv = int64(getSumByPrefix(last, "gosrt_connection_packets_received_total") -
		getSumByPrefix(first, "gosrt_connection_packets_received_total"))

	// TotalPacketsSent is not directly available in Prometheus metrics
	// Use submissions as a proxy (io_uring submissions = packets sent attempts)
	dm.TotalPacketsSent = int64(getSumByPrefix(last, "gosrt_connection_send_submitted_total") -
		getSumByPrefix(first, "gosrt_connection_send_submitted_total"))

	// ACK counters - look for type="ack" in the metrics
	dm.TotalACKsSent = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_sent_total", "type=\"ack\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_sent_total", "type=\"ack\""))
	dm.TotalACKsRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_received_total", "type=\"ack\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_received_total", "type=\"ack\""))

	// NAK counters
	dm.TotalNAKsSent = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_sent_total", "type=\"nak\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_sent_total", "type=\"nak\""))
	dm.TotalNAKsRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_received_total", "type=\"nak\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_received_total", "type=\"nak\""))

	// Error counters
	dm.TotalErrors = int64(getSumByPrefix(last, "gosrt_connection_crypto_error_total") -
		getSumByPrefix(first, "gosrt_connection_crypto_error_total"))
	dm.TotalErrors += int64(getSumByPrefix(last, "gosrt_connection_recv_data_error_total") -
		getSumByPrefix(first, "gosrt_connection_recv_data_error_total"))
	dm.TotalErrors += int64(getSumByPrefix(last, "gosrt_connection_recv_control_error_total") -
		getSumByPrefix(first, "gosrt_connection_recv_control_error_total"))
	dm.TotalErrors += int64(getSumByPrefix(last, "gosrt_connection_send_data_drop_total") -
		getSumByPrefix(first, "gosrt_connection_send_data_drop_total"))
	dm.TotalErrors += int64(getSumByPrefix(last, "gosrt_connection_send_control_drop_total") -
		getSumByPrefix(first, "gosrt_connection_send_control_drop_total"))

	// Populate error breakdown
	for name, lastVal := range last.Metrics {
		if strings.Contains(name, "error") || strings.Contains(name, "drop") {
			firstVal := first.Metrics[name]
			delta := lastVal - firstVal
			if delta > 0 {
				dm.ErrorsByType[name] = int64(delta)
			}
		}
	}

	// Compute rates
	if dm.Duration.Seconds() > 0 && dm.TotalPacketsSent > 0 {
		// Estimate bytes from packets (assuming ~1068 bytes payload per packet)
		dm.TotalBytesSent = dm.TotalPacketsSent * 1068
		dm.TotalBytesRecv = dm.TotalPacketsRecv * 1068
		dm.AvgSendRateMbps = float64(dm.TotalBytesSent*8) / dm.Duration.Seconds() / 1_000_000
		dm.AvgRecvRateMbps = float64(dm.TotalBytesRecv*8) / dm.Duration.Seconds() / 1_000_000
	}

	if dm.TotalPacketsSent > 0 {
		dm.AvgLossRate = float64(dm.TotalPacketsLost) / float64(dm.TotalPacketsSent)
	}

	if dm.TotalPacketsLost > 0 {
		dm.AvgRetransRate = float64(dm.TotalRetransmissions) / float64(dm.TotalPacketsLost)
	}

	return dm
}

// getSumByPrefix sums all metric values that start with the given prefix
func getSumByPrefix(snapshot *MetricsSnapshot, prefix string) float64 {
	var sum float64
	for name, value := range snapshot.Metrics {
		if strings.HasPrefix(name, prefix) {
			sum += value
		}
	}
	return sum
}

// getSumByPrefixContaining sums all metric values that start with prefix and contain substr
func getSumByPrefixContaining(snapshot *MetricsSnapshot, prefix, substr string) float64 {
	var sum float64
	for name, value := range snapshot.Metrics {
		if strings.HasPrefix(name, prefix) && strings.Contains(name, substr) {
			sum += value
		}
	}
	return sum
}

// AnalysisErrorCounterPrefixes is the list of error counter prefixes to check
// The actual Prometheus metrics have labels (socket_id, reason, etc.)
var AnalysisErrorCounterPrefixes = []string{
	// Crypto errors
	"gosrt_connection_crypto_error_total",

	// Receive path errors
	"gosrt_connection_recv_data_error_total",
	"gosrt_connection_recv_control_error_total",

	// Send path errors (drops)
	"gosrt_connection_send_data_drop_total",
	"gosrt_connection_send_control_drop_total",

	// Congestion control drops
	"gosrt_connection_congestion_recv_data_drop_total",
	"gosrt_connection_congestion_send_data_drop_total",
}

// Drop counters (may be expected in some tests)
var DropCounterPrefixes = []string{
	"gosrt_connection_packets_dropped_total",
}

// ErrorViolation represents an unexpected error counter value
type ErrorViolation struct {
	Counter   string
	Component string
	Expected  int64
	Actual    int64
	Message   string
}

// ErrorAnalysisResult holds the result of error counter analysis
type ErrorAnalysisResult struct {
	Passed     bool
	Violations []ErrorViolation
}

// AnalyzeErrors checks that error counters are zero (or within expected bounds)
// FAIL-SAFE: Defaults to failed, only passes when we confirm no unexpected errors
func AnalyzeErrors(ts *TestMetricsTimeSeries, config *TestConfig) ErrorAnalysisResult {
	// FAIL-SAFE: Start with failed - we must explicitly confirm no errors
	result := ErrorAnalysisResult{Passed: false}

	componentsChecked := 0

	// Analyze each component
	for _, component := range []MetricsTimeSeries{ts.Server, ts.ClientGenerator, ts.Client} {
		if len(component.Snapshots) < 2 {
			continue
		}

		// Find first and last successful snapshots
		var first, last *MetricsSnapshot
		for _, s := range component.Snapshots {
			if s.Error == nil {
				if first == nil {
					first = s
				}
				last = s
			}
		}

		if first == nil || last == nil || first == last {
			continue
		}

		componentsChecked++

		// Check each error counter prefix
		for _, prefix := range AnalysisErrorCounterPrefixes {
			delta := getSumByPrefix(last, prefix) - getSumByPrefix(first, prefix)
			if delta > 0 {
				expected := getExpectedErrorCount(prefix, config)
				if int64(delta) > expected {
					result.Violations = append(result.Violations, ErrorViolation{
						Counter:   prefix,
						Component: component.Component,
						Expected:  expected,
						Actual:    int64(delta),
						Message: fmt.Sprintf("%s: %s increased by %d (expected <= %d)",
							component.Component, prefix, int64(delta), expected),
					})
				}
			}
		}
	}

	// EXPLICIT PASS: Only pass if we checked components AND found no violations
	if componentsChecked > 0 && len(result.Violations) == 0 {
		result.Passed = true
	}

	return result
}

// getExpectedErrorCount returns the expected maximum for an error counter
func getExpectedErrorCount(counter string, config *TestConfig) int64 {
	if config == nil {
		return 0
	}

	// Check if this counter is in the expected errors list
	for _, expected := range config.ExpectedErrors {
		if expected == counter {
			// Allow some errors for known expected cases
			return 100 // Configurable threshold
		}
	}

	return 0
}

// SignalViolation represents a missing positive signal
type SignalViolation struct {
	Signal    string
	Component string
	Expected  string
	Actual    string
	Message   string
}

// PositiveSignalResult holds the result of positive signal validation
type PositiveSignalResult struct {
	Passed     bool
	Violations []SignalViolation
}

// PositiveSignals defines expected positive signals
type PositiveSignals struct {
	MinPacketsSent    int64   // At least this many packets sent
	MinPacketsRecv    int64   // At least this many packets received
	MinThroughputMbps float64 // At least this throughput
	MaxThroughputMbps float64 // No more than this (sanity check)
	RequireACKs       bool    // ACKs must be exchanged
	RequireNAKsOnLoss bool    // NAKs expected if loss > 0
}

// ValidatePositiveSignals verifies that expected behaviors occurred
// FAIL-SAFE: Defaults to failed, only passes when we confirm positive signals
func ValidatePositiveSignals(ts *TestMetricsTimeSeries, config *TestConfig) PositiveSignalResult {
	// FAIL-SAFE: Start with failed - we must explicitly confirm positive signals
	result := PositiveSignalResult{Passed: false}

	expected := computeExpectedSignals(config)

	// Get metrics for all components
	serverMetrics := ComputeDerivedMetrics(ts.Server)
	cgMetrics := ComputeDerivedMetrics(ts.ClientGenerator)
	clientMetrics := ComputeDerivedMetrics(ts.Client)

	// Track positive confirmations
	serverDataFlowOK := false
	clientDataFlowOK := false
	ackExchangeOK := false

	// Primary check: Server received packets (from client-generator publishing)
	// The server receives the data from the publisher
	serverDataRecv := serverMetrics.TotalPacketsRecv
	if serverDataRecv >= expected.MinPacketsRecv {
		serverDataFlowOK = true
	} else if serverMetrics.TotalACKsRecv > 0 {
		// ACKs are an alternative signal that data is flowing
		serverDataFlowOK = true
	} else {
		result.Violations = append(result.Violations, SignalViolation{
			Signal:    "ServerDataFlow",
			Component: "server",
			Expected:  fmt.Sprintf(">= %d packets or > 0 ACKs", expected.MinPacketsRecv),
			Actual:    fmt.Sprintf("%d packets, %d ACKs", serverDataRecv, serverMetrics.TotalACKsRecv),
			Message:   "Server not receiving expected data flow",
		})
	}

	// Secondary check: Client received packets (from server fanout)
	if clientMetrics.TotalPacketsRecv >= expected.MinPacketsRecv {
		clientDataFlowOK = true
	} else {
		result.Violations = append(result.Violations, SignalViolation{
			Signal:    "ClientDataFlow",
			Component: "client",
			Expected:  fmt.Sprintf(">= %d packets", expected.MinPacketsRecv),
			Actual:    fmt.Sprintf("%d packets", clientMetrics.TotalPacketsRecv),
			Message:   "Client not receiving expected data",
		})
	}

	// Verify ACK exchange occurred (bidirectional SRT control path)
	if expected.RequireACKs {
		totalACKs := serverMetrics.TotalACKsRecv + cgMetrics.TotalACKsRecv + clientMetrics.TotalACKsRecv
		if totalACKs > 0 {
			ackExchangeOK = true
		} else {
			result.Violations = append(result.Violations, SignalViolation{
				Signal:    "ACKExchange",
				Component: "all",
				Expected:  "> 0 ACKs received across all components",
				Actual:    "0",
				Message:   "No ACKs received - SRT control path may not be working",
			})
		}
	} else {
		ackExchangeOK = true // Not required, so OK
	}

	// EXPLICIT PASS: Only pass when ALL positive signals are confirmed
	if serverDataFlowOK && clientDataFlowOK && ackExchangeOK {
		result.Passed = true
	}

	return result
}

// computeExpectedSignals calculates expected signals from test configuration
func computeExpectedSignals(config *TestConfig) PositiveSignals {
	if config == nil {
		return PositiveSignals{
			RequireACKs: true,
		}
	}

	// Calculate expected packet count from bitrate and duration
	// Assuming ~1316 byte payload per packet (typical SRT MTU)
	bytesExpected := float64(config.Bitrate) / 8 * config.TestDuration.Seconds()
	packetsExpected := int64(bytesExpected / 1316)

	// Allow 10% variance for timing and connection setup/teardown
	minPackets := int64(float64(packetsExpected) * 0.90)

	// For received packets, expect at least 85% (allows for some startup delay)
	minRecv := int64(float64(packetsExpected) * 0.85)

	// Throughput should be close to configured bitrate
	targetMbps := float64(config.Bitrate) / 1_000_000
	minThroughput := targetMbps * 0.85 // 85% of target
	maxThroughput := targetMbps * 1.15 // 115% of target

	return PositiveSignals{
		MinPacketsSent:    minPackets,
		MinPacketsRecv:    minRecv,
		MinThroughputMbps: minThroughput,
		MaxThroughputMbps: maxThroughput,
		RequireACKs:       true,
		RequireNAKsOnLoss: false, // Only for network impairment tests
	}
}

// AnalysisResult aggregates all analysis components
type AnalysisResult struct {
	TestName   string
	TestConfig *TestConfig
	Passed     bool

	// Component results
	ErrorAnalysis   ErrorAnalysisResult
	PositiveSignals PositiveSignalResult

	// Runtime stability (for long-running tests)
	RuntimeStability []RuntimeStabilityResult

	// Derived metrics for each component
	ServerMetrics    DerivedMetrics
	ClientGenMetrics DerivedMetrics
	ClientMetrics    DerivedMetrics

	// Summary
	TotalViolations int
	TotalWarnings   int
	Summary         string
}

// AnalyzeTestMetrics performs comprehensive analysis of test metrics
// IMPORTANT: Follows fail-safe principle - defaults to FAILED, only PASSES when ALL checks confirm success
func AnalyzeTestMetrics(ts *TestMetricsTimeSeries, config *TestConfig) AnalysisResult {
	errorResult := AnalyzeErrors(ts, config)
	signalResult := ValidatePositiveSignals(ts, config)

	// FAIL-SAFE: Default to failed - only set to passed after ALL checks confirm success
	result := AnalysisResult{
		TestName:        ts.TestName,
		TestConfig:      config,
		Passed:          false, // NEVER assume success - must be explicitly confirmed
		ErrorAnalysis:   errorResult,
		PositiveSignals: signalResult,
	}

	// Compute derived metrics for reporting
	result.ServerMetrics = ComputeDerivedMetrics(ts.Server)
	result.ClientGenMetrics = ComputeDerivedMetrics(ts.ClientGenerator)
	result.ClientMetrics = ComputeDerivedMetrics(ts.Client)

	// Count violations and warnings from error and signal analysis
	result.TotalViolations = len(errorResult.Violations) + len(signalResult.Violations)

	// Track runtime stability pass/fail (for long-running tests)
	runtimePassed := true // No runtime analysis = passes by default (not applicable)

	// Perform runtime stability analysis for long-running tests (>= 30 min)
	if config != nil && config.TestDuration >= 30*time.Minute {
		result.RuntimeStability = AnalyzeRuntimeStabilityForAllComponents(ts, config.TestDuration)

		// Check if any runtime analysis failed
		for _, rs := range result.RuntimeStability {
			if !rs.Passed {
				runtimePassed = false
				result.TotalViolations += len(rs.Violations)
			}
			result.TotalWarnings += len(rs.Warnings)
		}
	}

	// EXPLICIT PASS CONDITION: Only set to passed when ALL checks explicitly confirm success
	// This is the ONLY place where Passed can become true
	if errorResult.Passed && signalResult.Passed && runtimePassed {
		result.Passed = true
	}

	// Generate summary
	if result.Passed {
		result.Summary = fmt.Sprintf("PASSED: %s", ts.TestName)
		if result.TotalWarnings > 0 {
			result.Summary += fmt.Sprintf(" (%d warnings)", result.TotalWarnings)
		}
	} else {
		result.Summary = fmt.Sprintf("FAILED: %s (%d violations)", ts.TestName, result.TotalViolations)
	}

	return result
}

// PrintAnalysisResult outputs the analysis result to console
func PrintAnalysisResult(result AnalysisResult) {
	fmt.Printf("\n=== Metrics Analysis: %s ===\n", result.TestName)

	// Error Analysis
	if result.ErrorAnalysis.Passed {
		fmt.Println("\nError Analysis: ✓ PASSED")
		fmt.Println("  ✓ No unexpected errors")
	} else {
		fmt.Println("\nError Analysis: ✗ FAILED")
		for _, v := range result.ErrorAnalysis.Violations {
			fmt.Printf("  ✗ %s\n", v.Message)
		}
	}

	// Positive Signals
	if result.PositiveSignals.Passed {
		fmt.Println("\nPositive Signals: ✓ PASSED")
		fmt.Printf("  ✓ Server received: %d packets\n", result.ServerMetrics.TotalPacketsRecv)
		fmt.Printf("  ✓ Client received: %d packets\n", result.ClientMetrics.TotalPacketsRecv)
		totalACKs := result.ServerMetrics.TotalACKsRecv + result.ClientGenMetrics.TotalACKsRecv + result.ClientMetrics.TotalACKsRecv
		if totalACKs > 0 {
			fmt.Printf("  ✓ ACK exchange verified: %d ACKs total\n", totalACKs)
		}
	} else {
		fmt.Println("\nPositive Signals: ✗ FAILED")
		for _, v := range result.PositiveSignals.Violations {
			fmt.Printf("  ✗ %s: expected %s, got %s\n", v.Signal, v.Expected, v.Actual)
			fmt.Printf("    %s\n", v.Message)
		}
	}

	// Metrics Summary
	fmt.Println("\nMetrics Summary:")
	fmt.Printf("  Server: recv'd %d packets, %d ACKs\n",
		result.ServerMetrics.TotalPacketsRecv, result.ServerMetrics.TotalACKsRecv)
	fmt.Printf("  Client-Generator: recv'd %d ACKs\n",
		result.ClientGenMetrics.TotalACKsRecv)
	fmt.Printf("  Client: recv'd %d packets, %d ACKs\n",
		result.ClientMetrics.TotalPacketsRecv, result.ClientMetrics.TotalACKsRecv)

	// Runtime Stability (for long-running tests)
	if len(result.RuntimeStability) > 0 {
		fmt.Println("\nRuntime Stability:")
		allStable := true
		for _, rs := range result.RuntimeStability {
			status := "✓ STABLE"
			if !rs.Passed {
				status = "✗ UNSTABLE"
				allStable = false
			} else if len(rs.Warnings) > 0 {
				status = "⚠ WARNINGS"
			}
			fmt.Printf("  %s: %s\n", rs.Component, status)

			// Print brief summary for each component
			if rs.Summary.HeapGrowthMBPerHour != 0 || !rs.Passed {
				fmt.Printf("    Heap: %.2f MB/hr, Goroutines: %.1f/hr\n",
					rs.Summary.HeapGrowthMBPerHour, rs.Summary.GoroutineGrowthRate)
			}
		}

		// Print violations if any
		for _, rs := range result.RuntimeStability {
			for _, v := range rs.Violations {
				fmt.Printf("  ✗ [%s] %s\n", rs.Component, v.Message)
			}
		}

		// Option to print detailed analysis
		if !allStable {
			fmt.Println("\n  (Run with -verbose for detailed runtime analysis)")
		}
	}

	// Final Result
	if result.Passed {
		fmt.Printf("\nRESULT: ✓ %s\n", result.Summary)
	} else {
		fmt.Printf("\nRESULT: ✗ %s\n", result.Summary)
	}

	fmt.Println(strings.Repeat("=", 50))
}

// AnalyzeTestResults analyzes metrics after a test has completed
// This can be called from the existing test infrastructure after runTestWithConfig
func AnalyzeTestResults(testMetrics *TestMetrics, config *TestConfig, startTime, endTime time.Time) AnalysisResult {
	// Create time series for analysis
	ts := NewTestMetricsTimeSeries(testMetrics, config.Name, config, startTime, endTime)

	// Perform analysis
	return AnalyzeTestMetrics(ts, config)
}
