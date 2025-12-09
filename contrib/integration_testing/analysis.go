package main

import (
	"encoding/json"
	"fmt"
	"os"
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

// PipelineBalanceResult holds the result of pipeline balance verification
type PipelineBalanceResult struct {
	Passed bool

	// Metrics from each component
	ClientGenPacketsSent int64 // Packets sent by client-generator
	ServerPacketsRecv    int64 // Packets received by server (from client-gen)
	ServerPacketsSent    int64 // Packets sent by server (to client)
	ClientPacketsRecv    int64 // Packets received by client

	// Balance verification
	IngressBalanced  bool  // ClientGen sent == Server recv
	EgressBalanced   bool  // Server sent == Client recv
	IngressDiff      int64 // Difference (expected 0)
	EgressDiff       int64 // Difference (expected 0)
	AllowedTolerance int64 // Max allowed difference (for timing)

	// Messages
	Violations []string
	Warnings   []string
}

// VerifyPipelineBalance checks that packets flow correctly through the pipeline
// This should be called after the client-generator stops and pipeline drains
func VerifyPipelineBalance(serverMetrics, clientGenMetrics, clientMetrics DerivedMetrics, tolerance int64) PipelineBalanceResult {
	result := PipelineBalanceResult{
		Passed:               false, // Fail-safe: default to failed
		AllowedTolerance:     tolerance,
		ClientGenPacketsSent: clientGenMetrics.TotalPacketsSent,
		ServerPacketsRecv:    serverMetrics.TotalPacketsRecv,
		ServerPacketsSent:    serverMetrics.TotalPacketsSent,
		ClientPacketsRecv:    clientMetrics.TotalPacketsRecv,
	}

	// Check ingress: Client-Generator → Server
	result.IngressDiff = result.ClientGenPacketsSent - result.ServerPacketsRecv
	if result.IngressDiff < 0 {
		result.IngressDiff = -result.IngressDiff // Absolute value
	}
	result.IngressBalanced = result.IngressDiff <= tolerance

	if !result.IngressBalanced {
		result.Violations = append(result.Violations,
			fmt.Sprintf("Ingress imbalance: Client-Gen sent %d, Server recv %d (diff: %d, tolerance: %d)",
				result.ClientGenPacketsSent, result.ServerPacketsRecv, result.IngressDiff, tolerance))
	}

	// Check egress: Server → Client
	result.EgressDiff = result.ServerPacketsSent - result.ClientPacketsRecv
	if result.EgressDiff < 0 {
		result.EgressDiff = -result.EgressDiff // Absolute value
	}
	result.EgressBalanced = result.EgressDiff <= tolerance

	if !result.EgressBalanced {
		result.Violations = append(result.Violations,
			fmt.Sprintf("Egress imbalance: Server sent %d, Client recv %d (diff: %d, tolerance: %d)",
				result.ServerPacketsSent, result.ClientPacketsRecv, result.EgressDiff, tolerance))
	}

	// Only pass if both balanced
	result.Passed = result.IngressBalanced && result.EgressBalanced

	// Add warnings for close-but-not-exact matches
	if result.IngressBalanced && result.IngressDiff > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Small ingress difference: %d packets (within tolerance)", result.IngressDiff))
	}
	if result.EgressBalanced && result.EgressDiff > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Small egress difference: %d packets (within tolerance)", result.EgressDiff))
	}

	return result
}

// PrintPipelineBalance prints the pipeline balance result
func PrintPipelineBalance(result PipelineBalanceResult) {
	fmt.Println("\nPipeline Balance Verification:")
	if result.Passed {
		fmt.Println("  ✓ PASSED")
		fmt.Printf("    Client-Generator → Server: %d → %d (diff: %d)\n",
			result.ClientGenPacketsSent, result.ServerPacketsRecv, result.IngressDiff)
		fmt.Printf("    Server → Client:           %d → %d (diff: %d)\n",
			result.ServerPacketsSent, result.ClientPacketsRecv, result.EgressDiff)
	} else {
		fmt.Println("  ✗ FAILED")
		for _, v := range result.Violations {
			fmt.Printf("    ✗ %s\n", v)
		}
	}
	for _, w := range result.Warnings {
		fmt.Printf("    ⚠ %s\n", w)
	}
}

// DerivedMetrics computed from the time series
type DerivedMetrics struct {
	// Deltas (final - initial)
	TotalPacketsSent     int64
	TotalPacketsRecv     int64
	TotalPacketsLost     int64
	TotalRetransmissions int64
	TotalRetransFromNAK  int64 // Direct counter from NAK handling
	TotalNAKsSent        int64
	TotalNAKsRecv        int64
	TotalACKsSent        int64
	TotalACKsRecv        int64
	TotalACKACKsSent     int64
	TotalACKACKsRecv     int64
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

	// ========== Congestion Control Statistics (Primary Source) ==========
	// These come from the congestion control layer and are the most accurate

	// Packets sent/received from congestion control (includes retransmissions)
	dm.TotalPacketsSent = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_packets_total", "direction=\"send\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_packets_total", "direction=\"send\""))
	dm.TotalPacketsRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_packets_total", "direction=\"recv\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_packets_total", "direction=\"recv\""))

	// Packets lost - detected via sequence number gaps by receiver
	dm.TotalPacketsLost = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\""))

	// Retransmissions - packets retransmitted by sender
	dm.TotalRetransmissions = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_retransmissions_total", "direction=\"send\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_retransmissions_total", "direction=\"send\""))

	// Bytes sent/received (for accurate throughput calculation)
	dm.TotalBytesSent = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_bytes_total", "direction=\"send\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_bytes_total", "direction=\"send\""))
	dm.TotalBytesRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_bytes_total", "direction=\"recv\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_bytes_total", "direction=\"recv\""))

	// ========== Control Packet Statistics ==========
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

	// ACKACK counters
	dm.TotalACKACKsSent = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_sent_total", "type=\"ackack\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_sent_total", "type=\"ackack\""))
	dm.TotalACKACKsRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_received_total", "type=\"ackack\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_received_total", "type=\"ackack\""))

	// Direct retransmission counter (from NAK handling)
	dm.TotalRetransFromNAK = int64(getSumByPrefix(last, "gosrt_connection_retransmissions_from_nak_total") -
		getSumByPrefix(first, "gosrt_connection_retransmissions_from_nak_total"))

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

	// Compute rates using actual byte counts from Prometheus
	if dm.Duration.Seconds() > 0 {
		if dm.TotalBytesSent > 0 {
			dm.AvgSendRateMbps = float64(dm.TotalBytesSent*8) / dm.Duration.Seconds() / 1_000_000
		}
		if dm.TotalBytesRecv > 0 {
			dm.AvgRecvRateMbps = float64(dm.TotalBytesRecv*8) / dm.Duration.Seconds() / 1_000_000
		}
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

	// For network impairment tests, congestion drops are expected
	if config.Mode == TestModeNetwork && config.Impairment.LossRate > 0 {
		// These counters indicate packets dropped due to arriving too late
		// or buffer overflow - expected with packet loss
		if counter == "gosrt_connection_congestion_recv_data_drop_total" ||
			counter == "gosrt_connection_congestion_send_data_drop_total" ||
			counter == "gosrt_connection_packets_dropped_total" {
			// Allow drops proportional to expected loss rate
			// e.g., 2% loss with 10000 packets = ~200 drops allowed (with margin)
			expectedDrops := int64(config.Impairment.LossRate * 10000 * 5) // 5x margin
			if expectedDrops < 100 {
				expectedDrops = 100
			}
			return expectedDrops
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
	ErrorAnalysis         ErrorAnalysisResult
	PositiveSignals       PositiveSignalResult
	StatisticalValidation StatisticalValidationResult // For network impairment tests
	PipelineBalance       PipelineBalanceResult       // Clean network pipeline verification

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
	statisticalResult := ValidateStatistical(ts, config)

	// FAIL-SAFE: Default to failed - only set to passed after ALL checks confirm success
	result := AnalysisResult{
		TestName:              ts.TestName,
		TestConfig:            config,
		Passed:                false, // NEVER assume success - must be explicitly confirmed
		ErrorAnalysis:         errorResult,
		PositiveSignals:       signalResult,
		StatisticalValidation: statisticalResult,
	}

	// Compute derived metrics for reporting
	result.ServerMetrics = ComputeDerivedMetrics(ts.Server)
	result.ClientGenMetrics = ComputeDerivedMetrics(ts.ClientGenerator)
	result.ClientMetrics = ComputeDerivedMetrics(ts.Client)

	// Pipeline balance verification for clean network tests
	// Uses a small tolerance for timing differences (5 packets)
	// Note: Mode defaults to "" (empty), treat "" and "clean" as clean network
	pipelineBalanceRequired := false
	if config == nil || config.Mode == TestModeClean || config.Mode == "" {
		pipelineBalanceRequired = true
		// Allow small tolerance for timing: ~0.1% or 5 packets, whichever is larger
		tolerance := int64(5)
		if result.ServerMetrics.TotalPacketsSent > 5000 {
			tolerance = result.ServerMetrics.TotalPacketsSent / 1000 // 0.1%
		}
		result.PipelineBalance = VerifyPipelineBalance(
			result.ServerMetrics, result.ClientGenMetrics, result.ClientMetrics, tolerance)
		if !result.PipelineBalance.Passed {
			result.TotalViolations += len(result.PipelineBalance.Violations)
		}
		result.TotalWarnings += len(result.PipelineBalance.Warnings)
	} else {
		// For network impairment tests, pipeline balance is not expected
		result.PipelineBalance = PipelineBalanceResult{Passed: true}
	}

	// Count violations and warnings from error and signal analysis
	result.TotalViolations += len(errorResult.Violations) + len(signalResult.Violations) +
		len(statisticalResult.Violations)
	result.TotalWarnings += len(statisticalResult.Warnings)

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
	pipelinePassed := !pipelineBalanceRequired || result.PipelineBalance.Passed
	if errorResult.Passed && signalResult.Passed && statisticalResult.Passed && runtimePassed && pipelinePassed {
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

	// Statistical Validation (only for network impairment tests)
	if result.TestConfig != nil && result.TestConfig.Mode == TestModeNetwork &&
		result.TestConfig.Impairment.LossRate > 0 {
		if result.StatisticalValidation.Passed {
			fmt.Println("\nStatistical Validation: ✓ PASSED")
			fmt.Printf("  ✓ Loss rate within tolerance (configured: %.1f%%)\n",
				result.TestConfig.Impairment.LossRate*100)
		} else {
			fmt.Println("\nStatistical Validation: ✗ FAILED")
			for _, v := range result.StatisticalValidation.Violations {
				fmt.Printf("  ✗ %s: expected %s, got %.2f\n", v.Metric, v.ExpectedRange, v.Observed)
				fmt.Printf("    %s\n", v.Message)
			}
		}
		// Print warnings even if passed
		for _, w := range result.StatisticalValidation.Warnings {
			fmt.Printf("  ⚠ %s: %s\n", w.Metric, w.Message)
		}
		// Print observed statistics summary for network impairment tests
		fmt.Println("\n  Observed Statistics:")
		retransPctOfSent := float64(0)
		if result.ClientGenMetrics.TotalPacketsSent > 0 {
			retransPctOfSent = float64(result.ClientGenMetrics.TotalRetransmissions) /
				float64(result.ClientGenMetrics.TotalPacketsSent) * 100
		}
		fmt.Printf("    Configured loss: %.1f%%, Retrans%% of sent: %.2f%%\n",
			result.TestConfig.Impairment.LossRate*100, retransPctOfSent)
		fmt.Printf("    Packets sent: %d, Retransmissions: %d, Lost: %d\n",
			result.ClientGenMetrics.TotalPacketsSent,
			result.ClientGenMetrics.TotalRetransmissions,
			result.ClientMetrics.TotalPacketsLost)
	}

	// Pipeline Balance (only for clean network tests)
	// Note: Mode defaults to "" (empty), treat "" and "clean" as clean network
	if result.TestConfig == nil || result.TestConfig.Mode == TestModeClean || result.TestConfig.Mode == "" {
		PrintPipelineBalance(result.PipelineBalance)
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

// ============================================================================
// JSON Output
// ============================================================================

// JSONAnalysisResult is a JSON-serializable version of AnalysisResult
type JSONAnalysisResult struct {
	TestName  string `json:"test_name"`
	Passed    bool   `json:"passed"`
	Summary   string `json:"summary"`
	Timestamp string `json:"timestamp"`
	Duration  string `json:"duration,omitempty"`

	// Violation and warning counts
	TotalViolations int `json:"total_violations"`
	TotalWarnings   int `json:"total_warnings"`

	// Component results
	ErrorAnalysis         JSONErrorAnalysis         `json:"error_analysis"`
	PositiveSignals       JSONPositiveSignals       `json:"positive_signals"`
	StatisticalValidation JSONStatisticalValidation `json:"statistical_validation,omitempty"`
	RuntimeStability      []JSONRuntimeStability    `json:"runtime_stability,omitempty"`

	// Metrics summaries
	Metrics JSONMetricsSummary `json:"metrics"`
}

// JSONErrorAnalysis is JSON-serializable error analysis
type JSONErrorAnalysis struct {
	Passed     bool                 `json:"passed"`
	Violations []JSONErrorViolation `json:"violations,omitempty"`
}

// JSONErrorViolation is a JSON-serializable error violation
type JSONErrorViolation struct {
	Counter   string `json:"counter"`
	Component string `json:"component"`
	Expected  int64  `json:"expected"`
	Actual    int64  `json:"actual"`
	Message   string `json:"message"`
}

// JSONPositiveSignals is JSON-serializable positive signal result
type JSONPositiveSignals struct {
	Passed     bool                  `json:"passed"`
	Violations []JSONSignalViolation `json:"violations,omitempty"`
}

// JSONSignalViolation is a JSON-serializable signal violation
type JSONSignalViolation struct {
	Signal    string `json:"signal"`
	Component string `json:"component"`
	Expected  string `json:"expected"`
	Actual    string `json:"actual"`
	Message   string `json:"message"`
}

// JSONStatisticalValidation is JSON-serializable statistical validation
type JSONStatisticalValidation struct {
	Passed     bool                       `json:"passed"`
	Violations []JSONStatisticalViolation `json:"violations,omitempty"`
	Warnings   []JSONStatisticalWarning   `json:"warnings,omitempty"`
}

// JSONStatisticalViolation is a JSON-serializable statistical violation
type JSONStatisticalViolation struct {
	Metric        string  `json:"metric"`
	ExpectedRange string  `json:"expected_range"`
	Observed      float64 `json:"observed"`
	Message       string  `json:"message"`
}

// JSONStatisticalWarning is a JSON-serializable statistical warning
type JSONStatisticalWarning struct {
	Metric  string `json:"metric"`
	Message string `json:"message"`
}

// JSONRuntimeStability is JSON-serializable runtime stability result
type JSONRuntimeStability struct {
	Component           string  `json:"component"`
	Passed              bool    `json:"passed"`
	HeapGrowthMBPerHour float64 `json:"heap_growth_mb_per_hour"`
	GoroutineGrowthRate float64 `json:"goroutine_growth_rate"`
	ViolationCount      int     `json:"violation_count"`
	WarningCount        int     `json:"warning_count"`
}

// JSONMetricsSummary contains metrics summaries for all components
type JSONMetricsSummary struct {
	Server          JSONComponentMetrics `json:"server"`
	ClientGenerator JSONComponentMetrics `json:"client_generator"`
	Client          JSONComponentMetrics `json:"client"`
}

// JSONComponentMetrics is a JSON-serializable component metrics summary
type JSONComponentMetrics struct {
	PacketsRecv     int64   `json:"packets_recv"`
	PacketsSent     int64   `json:"packets_sent"`
	PacketsLost     int64   `json:"packets_lost"`
	Retransmissions int64   `json:"retransmissions"`
	ACKsRecv        int64   `json:"acks_recv"`
	NAKsRecv        int64   `json:"naks_recv"`
	AvgRecvRateMbps float64 `json:"avg_recv_rate_mbps,omitempty"`
}

// ToJSON converts AnalysisResult to JSON-serializable format
func (r *AnalysisResult) ToJSON() JSONAnalysisResult {
	jr := JSONAnalysisResult{
		TestName:        r.TestName,
		Passed:          r.Passed,
		Summary:         r.Summary,
		Timestamp:       time.Now().Format(time.RFC3339),
		TotalViolations: r.TotalViolations,
		TotalWarnings:   r.TotalWarnings,
	}

	// Error analysis
	jr.ErrorAnalysis = JSONErrorAnalysis{Passed: r.ErrorAnalysis.Passed}
	for _, v := range r.ErrorAnalysis.Violations {
		jr.ErrorAnalysis.Violations = append(jr.ErrorAnalysis.Violations, JSONErrorViolation{
			Counter:   v.Counter,
			Component: v.Component,
			Expected:  v.Expected,
			Actual:    v.Actual,
			Message:   v.Message,
		})
	}

	// Positive signals
	jr.PositiveSignals = JSONPositiveSignals{Passed: r.PositiveSignals.Passed}
	for _, v := range r.PositiveSignals.Violations {
		jr.PositiveSignals.Violations = append(jr.PositiveSignals.Violations, JSONSignalViolation{
			Signal:    v.Signal,
			Component: v.Component,
			Expected:  v.Expected,
			Actual:    v.Actual,
			Message:   v.Message,
		})
	}

	// Statistical validation
	jr.StatisticalValidation = JSONStatisticalValidation{Passed: r.StatisticalValidation.Passed}
	for _, v := range r.StatisticalValidation.Violations {
		jr.StatisticalValidation.Violations = append(jr.StatisticalValidation.Violations, JSONStatisticalViolation{
			Metric:        v.Metric,
			ExpectedRange: v.ExpectedRange,
			Observed:      v.Observed,
			Message:       v.Message,
		})
	}
	for _, w := range r.StatisticalValidation.Warnings {
		jr.StatisticalValidation.Warnings = append(jr.StatisticalValidation.Warnings, JSONStatisticalWarning{
			Metric:  w.Metric,
			Message: w.Message,
		})
	}

	// Runtime stability
	for _, rs := range r.RuntimeStability {
		jr.RuntimeStability = append(jr.RuntimeStability, JSONRuntimeStability{
			Component:           rs.Component,
			Passed:              rs.Passed,
			HeapGrowthMBPerHour: rs.Summary.HeapGrowthMBPerHour,
			GoroutineGrowthRate: rs.Summary.GoroutineGrowthRate,
			ViolationCount:      len(rs.Violations),
			WarningCount:        len(rs.Warnings),
		})
	}

	// Metrics summaries
	jr.Metrics = JSONMetricsSummary{
		Server: JSONComponentMetrics{
			PacketsRecv:     r.ServerMetrics.TotalPacketsRecv,
			PacketsSent:     r.ServerMetrics.TotalPacketsSent,
			PacketsLost:     r.ServerMetrics.TotalPacketsLost,
			Retransmissions: r.ServerMetrics.TotalRetransmissions,
			ACKsRecv:        r.ServerMetrics.TotalACKsRecv,
			NAKsRecv:        r.ServerMetrics.TotalNAKsRecv,
			AvgRecvRateMbps: r.ServerMetrics.AvgRecvRateMbps,
		},
		ClientGenerator: JSONComponentMetrics{
			PacketsRecv:     r.ClientGenMetrics.TotalPacketsRecv,
			PacketsSent:     r.ClientGenMetrics.TotalPacketsSent,
			PacketsLost:     r.ClientGenMetrics.TotalPacketsLost,
			Retransmissions: r.ClientGenMetrics.TotalRetransmissions,
			ACKsRecv:        r.ClientGenMetrics.TotalACKsRecv,
			NAKsRecv:        r.ClientGenMetrics.TotalNAKsRecv,
		},
		Client: JSONComponentMetrics{
			PacketsRecv:     r.ClientMetrics.TotalPacketsRecv,
			PacketsSent:     r.ClientMetrics.TotalPacketsSent,
			PacketsLost:     r.ClientMetrics.TotalPacketsLost,
			Retransmissions: r.ClientMetrics.TotalRetransmissions,
			ACKsRecv:        r.ClientMetrics.TotalACKsRecv,
			NAKsRecv:        r.ClientMetrics.TotalNAKsRecv,
			AvgRecvRateMbps: r.ClientMetrics.AvgRecvRateMbps,
		},
	}

	return jr
}

// WriteJSON writes the analysis result to a file in JSON format
func (r *AnalysisResult) WriteJSON(filename string) error {
	jr := r.ToJSON()
	data, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return os.WriteFile(filename, data, 0644)
}

// PrintJSON outputs the analysis result to stdout in JSON format
func (r *AnalysisResult) PrintJSON() error {
	jr := r.ToJSON()
	data, err := json.MarshalIndent(jr, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// ============================================================================
// Statistical Validation (for Network Impairment Tests)
// ============================================================================

// StatisticalViolation represents a statistical validation failure
type StatisticalViolation struct {
	Metric        string
	ExpectedRange string
	Observed      float64
	ZScore        float64 // How many std deviations from expected
	Message       string
}

// StatisticalWarning represents a statistical anomaly that's not a failure
type StatisticalWarning struct {
	Metric  string
	Message string
}

// StatisticalValidationResult holds the result of statistical validation
type StatisticalValidationResult struct {
	Passed     bool
	Violations []StatisticalViolation
	Warnings   []StatisticalWarning
}

// StatisticalExpectation defines expected behavior under network impairment
type StatisticalExpectation struct {
	// Loss rate expectations
	ExpectedLossRate  float64 // e.g., 0.02 for 2%
	LossRateTolerance float64 // e.g., 0.5 means ±50% of expected

	// Retransmission expectations (should be proportional to loss)
	MinRetransRate float64 // At least this fraction of lost packets retransmitted
	MaxRetransRate float64 // No more than this (indicates excessive retrans)

	// NAK expectations
	ExpectNAKs        bool
	MinNAKsPerLostPkt float64 // At least this many NAKs per lost packet
	MaxNAKsPerLostPkt float64 // No more than this (indicates NAK storms)

	// Recovery expectations
	MinRecoveryRate float64 // Fraction of lost packets successfully recovered
}

// ObservedStatistics holds computed statistics from metrics
type ObservedStatistics struct {
	// Primary loss rate (using the best available method)
	LossRate float64 // Best estimate of packets lost / packets sent

	// Cross-endpoint loss calculation (sender packets - receiver packets)
	CrossEndpointLossRate float64 // (PacketsSent - PacketsRecv) / PacketsSent
	CrossEndpointLossAbs  int64   // Absolute packets lost via cross-endpoint

	// Reported loss from sequence gap detection
	ReportedLossRate float64 // PacketsLost / PacketsSent (from receiver's detection)
	ReportedLossAbs  int64   // Absolute packets lost via sequence gaps

	// Cross-check: do both methods agree?
	LossMethodsAgree bool    // True if both methods agree within tolerance
	LossDiscrepancy  float64 // Difference between methods (for debugging)

	// Other statistics
	RetransRate       float64 // Retransmissions / packets lost (should be ~1.0 for full recovery)
	RetransPctOfSent  float64 // Retransmissions / packets sent (should match loss rate)
	NAKsPerLostPacket float64 // NAKs sent / packets lost
	RecoveryRate      float64 // (Packets sent - unrecoverable) / packets sent
}

// ValidateStatistical performs statistical validation for network impairment tests
// FAIL-SAFE: Defaults to failed for applicable tests, passes for clean network tests
func ValidateStatistical(ts *TestMetricsTimeSeries, config *TestConfig) StatisticalValidationResult {
	// FAIL-SAFE: Start with failed for applicable tests
	result := StatisticalValidationResult{Passed: false}

	// For clean network tests or no impairment, statistical validation is not applicable
	// Pass immediately since there's nothing to validate
	if config == nil || config.Mode != TestModeNetwork {
		result.Passed = true
		return result
	}

	// For network mode with "clean" pattern, also skip
	if config.Impairment.Pattern == "clean" || config.Impairment.LossRate == 0 {
		result.Passed = true
		return result
	}

	expected := computeStatisticalExpectations(config.Impairment)
	observed := computeObservedStatistics(ts)

	// Track what we validated successfully
	checksPerformed := 0
	checksPassed := 0

	// Validate loss rate (using best available method)
	checksPerformed++
	if isWithinTolerance(observed.LossRate, expected.ExpectedLossRate, expected.LossRateTolerance) {
		checksPassed++
	} else {
		lowerBound := expected.ExpectedLossRate * (1 - expected.LossRateTolerance)
		upperBound := expected.ExpectedLossRate * (1 + expected.LossRateTolerance)
		result.Violations = append(result.Violations, StatisticalViolation{
			Metric:        "LossRate",
			ExpectedRange: fmt.Sprintf("%.1f%% - %.1f%%", lowerBound*100, upperBound*100),
			Observed:      observed.LossRate * 100,
			Message: fmt.Sprintf(
				"Observed loss rate %.2f%% outside expected range for %.1f%% configured loss (cross-endpoint: %.2f%%, reported: %.2f%%)",
				observed.LossRate*100, expected.ExpectedLossRate*100,
				observed.CrossEndpointLossRate*100, observed.ReportedLossRate*100),
		})
	}

	// Add warning if loss calculation methods disagree significantly
	if !observed.LossMethodsAgree && observed.LossDiscrepancy > 0 {
		result.Warnings = append(result.Warnings, StatisticalWarning{
			Metric: "LossMethodDiscrepancy",
			Message: fmt.Sprintf(
				"Loss detection methods disagree by %.1f%%: cross-endpoint=%.2f%% (%d pkts), reported=%.2f%% (%d pkts)",
				observed.LossDiscrepancy*100,
				observed.CrossEndpointLossRate*100, observed.CrossEndpointLossAbs,
				observed.ReportedLossRate*100, observed.ReportedLossAbs),
		})
	}

	// Validate retransmission rate (only if there was loss)
	if observed.LossRate > 0 {
		checksPerformed++
		if observed.RetransRate >= expected.MinRetransRate {
			checksPassed++
		} else {
			result.Violations = append(result.Violations, StatisticalViolation{
				Metric:        "RetransRate",
				ExpectedRange: fmt.Sprintf(">= %.1f%%", expected.MinRetransRate*100),
				Observed:      observed.RetransRate * 100,
				Message:       "Too few retransmissions - loss recovery may not be working",
			})
		}

		// Warn on excessive retransmissions
		if observed.RetransRate > expected.MaxRetransRate {
			result.Warnings = append(result.Warnings, StatisticalWarning{
				Metric: "RetransRate",
				Message: fmt.Sprintf(
					"High retransmission rate (%.1f%%) - possible retransmission storm",
					observed.RetransRate*100),
			})
		}

		// Cross-check: retransmissions as % of sent should be close to loss rate
		// This validates that the ARQ is working correctly end-to-end
		// With loss rate L and retrans recovery R, expected retrans % ≈ L / (1 - L*R) ≈ L for small L
		// Use wider tolerance (2x) since retransmissions can also be lost
		checksPerformed++
		lowerBound := expected.ExpectedLossRate * 0.5 // At least 50% of loss rate
		upperBound := expected.ExpectedLossRate * 3.0 // No more than 3x loss rate
		if observed.RetransPctOfSent >= lowerBound && observed.RetransPctOfSent <= upperBound {
			checksPassed++
		} else {
			result.Warnings = append(result.Warnings, StatisticalWarning{
				Metric: "RetransPctOfSent",
				Message: fmt.Sprintf(
					"Retransmissions (%.2f%% of sent) don't match loss rate (%.1f%% expected) - "+
						"expected range %.2f%% - %.2f%%",
					observed.RetransPctOfSent*100, expected.ExpectedLossRate*100,
					lowerBound*100, upperBound*100),
			})
			// Don't fail the test, just warn - this is a sanity check, not a hard requirement
			checksPassed++ // Count as passed since it's just a warning
		}
	}

	// Validate NAK behavior (only if expected and there was loss)
	if expected.ExpectNAKs && observed.LossRate > 0 {
		checksPerformed++
		if observed.NAKsPerLostPacket >= expected.MinNAKsPerLostPkt {
			checksPassed++
		} else {
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
	}

	// Validate recovery rate
	checksPerformed++
	if observed.RecoveryRate >= expected.MinRecoveryRate {
		checksPassed++
	} else {
		result.Violations = append(result.Violations, StatisticalViolation{
			Metric:        "RecoveryRate",
			ExpectedRange: fmt.Sprintf(">= %.1f%%", expected.MinRecoveryRate*100),
			Observed:      observed.RecoveryRate * 100,
			Message:       "Poor loss recovery - too many unrecoverable packets",
		})
	}

	// EXPLICIT PASS: Only pass when all checks succeed
	if checksPerformed > 0 && checksPassed == checksPerformed {
		result.Passed = true
	}

	return result
}

// computeStatisticalExpectations calculates expected behavior based on impairment config.
// If imp.Thresholds is set, those values are used directly.
// Otherwise, defaults are computed based on impairment type.
func computeStatisticalExpectations(imp NetworkImpairment) StatisticalExpectation {
	// Start with default expectations
	exp := StatisticalExpectation{
		ExpectedLossRate:  imp.LossRate,
		LossRateTolerance: 0.5, // ±50% tolerance (netem is statistical)
		MinRetransRate:    0.8, // At least 80% of lost packets should trigger retrans
		MaxRetransRate:    3.0, // No more than 3x retransmissions per lost packet
		ExpectNAKs:        imp.LossRate > 0,
		MinNAKsPerLostPkt: 0.5,  // At least 0.5 NAKs per lost packet (batching OK)
		MaxNAKsPerLostPkt: 5.0,  // More than 5 NAKs per lost packet is a storm
		MinRecoveryRate:   0.95, // 95% of packets should be successfully received
	}

	// If explicit thresholds are provided, use them directly
	if imp.Thresholds != nil {
		t := imp.Thresholds
		if t.LossRateTolerance > 0 {
			exp.LossRateTolerance = t.LossRateTolerance
		}
		if t.MinRetransRate > 0 {
			exp.MinRetransRate = t.MinRetransRate
		}
		if t.MaxRetransRate > 0 {
			exp.MaxRetransRate = t.MaxRetransRate
		}
		if t.MinNAKsPerLostPkt > 0 {
			exp.MinNAKsPerLostPkt = t.MinNAKsPerLostPkt
		}
		if t.MaxNAKsPerLostPkt > 0 {
			exp.MaxNAKsPerLostPkt = t.MaxNAKsPerLostPkt
		}
		if t.MinRecoveryRate > 0 {
			exp.MinRecoveryRate = t.MinRecoveryRate
		}
		return exp
	}

	// Otherwise, compute defaults based on impairment type
	// Adjust for high latency - allows more recovery time but harder to retransmit
	if imp.LatencyProfile == "geo-satellite" || imp.LatencyProfile == "tier3-high" {
		exp.MinRecoveryRate = 0.90  // Slightly lower expectation for high latency
		exp.LossRateTolerance = 0.6 // More tolerance due to timing effects
	}

	// Adjust for pattern-based impairment
	switch imp.Pattern {
	case "starlink":
		// Starlink has 100% loss bursts - recovery depends on buffer size
		exp.LossRateTolerance = 1.0 // Higher tolerance for burst patterns
		exp.MinRecoveryRate = 0.85  // Some packets may be unrecoverable during bursts
	case "high-loss":
		exp.LossRateTolerance = 1.0 // High tolerance for burst patterns
		exp.MinRecoveryRate = 0.80  // Heavy impairment = lower recovery expectation
	case "heavy":
		exp.MinRecoveryRate = 0.80 // Heavy impairment = lower recovery expectation
	case "moderate":
		exp.MinRecoveryRate = 0.90
	}

	return exp
}

// computeObservedStatistics calculates actual statistics from metrics
// Uses two independent methods for loss calculation and cross-checks them:
// 1. Cross-endpoint: PacketsSent (sender) - PacketsRecv (receiver)
// 2. Reported loss: Sequence gap detection by receiver
func computeObservedStatistics(ts *TestMetricsTimeSeries) ObservedStatistics {
	// Get derived metrics for each component
	// Topology: Client-Generator → Server → Client
	// - Client-generator is the sender (publisher)
	// - Client is the final receiver (subscriber)
	// - Server is the relay AND the loss detector (sends NAKs back to client-generator)
	sender := ComputeDerivedMetrics(ts.ClientGenerator)
	receiver := ComputeDerivedMetrics(ts.Client)
	server := ComputeDerivedMetrics(ts.Server)

	stats := ObservedStatistics{}

	// Packets sent by client-generator (publisher)
	packetsSent := sender.TotalPacketsSent
	if packetsSent == 0 {
		// Fallback: estimate from bytes sent
		if sender.TotalBytesSent > 0 {
			packetsSent = sender.TotalBytesSent / 1316 // Approximate packet size
		}
	}

	if packetsSent > 0 {
		// ========== Method 1: Cross-Endpoint Loss Calculation ==========
		// Compare packets sent by sender vs packets received by receiver
		packetsReceived := receiver.TotalPacketsRecv
		if packetsReceived < packetsSent {
			stats.CrossEndpointLossAbs = packetsSent - packetsReceived
			stats.CrossEndpointLossRate = float64(stats.CrossEndpointLossAbs) / float64(packetsSent)
		}

		// ========== Method 2: Reported Loss (Sequence Gap Detection) ==========
		// Packets lost as detected by receiver via sequence number gaps
		stats.ReportedLossAbs = receiver.TotalPacketsLost
		if stats.ReportedLossAbs > 0 {
			stats.ReportedLossRate = float64(stats.ReportedLossAbs) / float64(packetsSent)
		}

		// ========== Cross-Check: Do Both Methods Agree? ==========
		// Allow 50% discrepancy between methods (due to timing, in-flight packets, etc.)
		const crossCheckTolerance = 0.5
		if stats.CrossEndpointLossRate > 0 && stats.ReportedLossRate > 0 {
			// Calculate relative difference
			maxRate := stats.CrossEndpointLossRate
			if stats.ReportedLossRate > maxRate {
				maxRate = stats.ReportedLossRate
			}
			minRate := stats.CrossEndpointLossRate
			if stats.ReportedLossRate < minRate {
				minRate = stats.ReportedLossRate
			}
			if maxRate > 0 {
				stats.LossDiscrepancy = (maxRate - minRate) / maxRate
				stats.LossMethodsAgree = stats.LossDiscrepancy <= crossCheckTolerance
			}
		} else {
			// If only one method detected loss, they don't really "agree"
			// but we shouldn't flag it as a discrepancy
			stats.LossMethodsAgree = true
		}

		// ========== Use Best Available Loss Rate ==========
		// Prefer the higher of the two methods (more conservative/defensive)
		// This catches losses that either method might miss
		if stats.CrossEndpointLossRate > stats.ReportedLossRate {
			stats.LossRate = stats.CrossEndpointLossRate
		} else {
			stats.LossRate = stats.ReportedLossRate
		}

		// ========== Recovery Rate ==========
		// What fraction of sent packets were successfully received
		stats.RecoveryRate = float64(packetsReceived) / float64(packetsSent)
		if stats.RecoveryRate > 1.0 {
			stats.RecoveryRate = 1.0 // Cap at 100%
		}
	} else {
		stats.RecoveryRate = 1.0 // No packets sent = 100% recovery (nothing to lose)
		stats.LossMethodsAgree = true
	}

	// ========== Retransmission and NAK Rates ==========
	// Use the best available loss count for denominators
	lossCount := stats.ReportedLossAbs
	if stats.CrossEndpointLossAbs > lossCount {
		lossCount = stats.CrossEndpointLossAbs
	}

	if lossCount > 0 {
		stats.RetransRate = float64(sender.TotalRetransmissions) / float64(lossCount)
		// NAKs are sent by the SERVER (which receives from client-generator and detects loss)
		// NOT by the final client (which just receives relayed data from server)
		stats.NAKsPerLostPacket = float64(server.TotalNAKsSent) / float64(lossCount)
	}

	// Retransmissions as percentage of packets sent
	// This should match the configured loss rate (e.g., 2% loss → ~2% retransmissions)
	if packetsSent > 0 {
		stats.RetransPctOfSent = float64(sender.TotalRetransmissions) / float64(packetsSent)
	}

	return stats
}

// isWithinTolerance checks if observed value is within tolerance of expected
func isWithinTolerance(observed, expected, tolerance float64) bool {
	if expected == 0 {
		// For expected 0, observed must also be 0 (or very close)
		return observed < 0.001 // Less than 0.1%
	}
	lowerBound := expected * (1 - tolerance)
	upperBound := expected * (1 + tolerance)
	return observed >= lowerBound && observed <= upperBound
}
