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

// printConnectionSummary prints a summary for a single SRT connection
// receiverMetrics: metrics from the RECEIVER side of this connection
// senderMetrics: metrics from the SENDER side of this connection
func printConnectionSummary(connName string, receiverMetrics, senderMetrics DerivedMetrics) {
	// Determine connection label
	var connLabel string
	if connName == "Connection1" {
		connLabel = "Publisher → Server"
	} else {
		connLabel = "Server → Subscriber"
	}

	fmt.Printf("    %s (%s):\n", connName, connLabel)

	// Sender side stats
	retransPct := float64(0)
	if senderMetrics.TotalPacketsSent > 0 {
		retransPct = float64(senderMetrics.TotalRetransmissions) / float64(senderMetrics.TotalPacketsSent) * 100
	}
	fmt.Printf("      Sender: sent=%d, retrans=%d (%.2f%%)\n",
		senderMetrics.TotalPacketsSent, senderMetrics.TotalRetransmissions, retransPct)

	// Receiver side stats
	gapPct := float64(0)
	if senderMetrics.TotalPacketsSent > 0 {
		gapPct = float64(receiverMetrics.TotalGapsDetected) / float64(senderMetrics.TotalPacketsSent) * 100
	}
	// TRUE recovery rate: Only count TSBPD skips as unrecoverable
	// ALL drop types (too_late, already_acked, duplicate) are REDUNDANT ARRIVALS, not true losses:
	// - too_late: packet arrived, but was either already delivered or we moved past it (TSBPD skip)
	// - already_acked: retransmit arrived after gap was filled by earlier retransmit
	// - duplicate: same packet arrived twice while still buffered
	// - skips: packets that NEVER arrived before TSBPD - these are the ONLY TRUE losses
	//
	// Note: If a packet was TSBPD-skipped and later arrives, it's counted as both
	// "skipped" (when TSBPD expired) and "too_late" (when it arrived). This is correct:
	// the skip counter tracks the moment of true loss, the too_late counter tracks the
	// wasted/late arrival.
	recoveryRate := float64(100)
	trueLosses := receiverMetrics.TotalPacketsSkippedTSBPD // ONLY skips are true losses
	if receiverMetrics.TotalGapsDetected > 0 {
		recoveryRate = (1.0 - float64(trueLosses)/float64(receiverMetrics.TotalGapsDetected)) * 100
	}
	fmt.Printf("      Receiver: recv=%d, gaps=%d (%.2f%%), true_loss=%d, recovery=%.1f%%\n",
		receiverMetrics.TotalPacketsRecv, receiverMetrics.TotalGapsDetected, gapPct,
		trueLosses, recoveryRate)
	fmt.Printf("        Drops: %d (too_late=%d, already_ack=%d, dupes=%d), Skips: %d\n",
		receiverMetrics.TotalPacketsDropped,
		receiverMetrics.TotalDropsTooLate, receiverMetrics.TotalDropsAlreadyAck, receiverMetrics.TotalDropsDuplicate,
		receiverMetrics.TotalPacketsSkippedTSBPD)
	fmt.Printf("      NAKs: sent=%d, recv=%d | ACKs: sent=%d, recv=%d\n",
		receiverMetrics.TotalNAKsSent, senderMetrics.TotalNAKsRecv,
		receiverMetrics.TotalACKsSent, senderMetrics.TotalACKsRecv)
}

// DerivedMetrics computed from the time series
type DerivedMetrics struct {
	// Deltas (final - initial)
	TotalPacketsSent     int64
	TotalPacketsRecv     int64
	TotalRetransmissions int64
	TotalRetransFromNAK  int64 // Direct counter from NAK handling
	TotalNAKsSent        int64
	TotalNAKsRecv        int64
	TotalACKsSent        int64
	TotalACKsRecv        int64
	TotalACKACKsSent     int64
	TotalACKACKsRecv     int64
	TotalErrors          int64

	// Network vs SRT Loss (see integration_testing_design.md#3-understanding-loss-network-vs-srt)
	// TotalGapsDetected: Sequence gaps detected by receiver (≈ netem loss, triggers NAK/retrans)
	TotalGapsDetected int64
	// TotalPacketsDropped: Packets that arrived but were discarded (too late, duplicate, etc.)
	TotalPacketsDropped int64
	// Granular drop reasons - for debugging which type of drops are occurring
	TotalDropsTooLate    int64 // Arrived after TSBPD expired
	TotalDropsAlreadyAck int64 // Arrived after ACK advanced past them
	TotalDropsDuplicate  int64 // Already in buffer (duplicate packet)
	// TotalPacketsSkippedTSBPD: Packets that NEVER arrived - skipped when ACK advanced past TSBPD time
	// This is the TRUE unrecoverable loss - distinct from "drops" which track packets that ARRIVED
	TotalPacketsSkippedTSBPD int64
	// TotalPacketsUnrecoverable: Sum of drops + TSBPD skips = all packets NOT delivered to application
	TotalPacketsUnrecoverable int64
	// TotalPacketsLost: Alias for TotalGapsDetected for backward compatibility
	TotalPacketsLost int64
	// TotalDuplicates: Packets already in buffer (over-NAKing confirmation metric)
	// If duplicates ≈ (NAKPktsRequested - GapsDetected), confirms range NAKs over-requesting
	TotalDuplicates int64

	// Timer health counters - verify periodic routines are running
	// Expected: ACK ~100/sec, NAK ~50/sec (linear growth with test duration)
	PeriodicACKRuns int64
	PeriodicNAKRuns int64

	// NAK Detail Counters (RFC SRT Appendix A)
	// Counters track PACKETS, not entries: NAKSinglesSent + NAKRangesSent = NAKPktsRequested
	// This allows direct verification: RetransSent ≈ NAKPktsReceived
	//
	// Receiver side (sends NAKs)
	NAKSinglesSent   int64 // Packets requested via single NAK entries (1 per entry)
	NAKRangesSent    int64 // Packets requested via range NAK entries (sum of range sizes)
	NAKPktsRequested int64 // Total = NAKSinglesSent + NAKRangesSent
	// Sender side (receives NAKs)
	NAKSinglesRecv  int64 // Packets requested via single NAK entries received
	NAKRangesRecv   int64 // Packets requested via range NAK entries received
	NAKPktsReceived int64 // Total = NAKSinglesRecv + NAKRangesRecv

	// Bytes sent/received
	TotalBytesSent int64
	TotalBytesRecv int64

	// Rates (computed from time series)
	AvgSendRateMbps float64
	AvgRecvRateMbps float64
	AvgGapRate      float64 // gaps detected / packets sent (≈ netem loss rate)
	AvgDropRate     float64 // packets dropped / packets sent (= SRT loss rate)
	AvgRetransRate  float64 // retransmissions / gaps detected (recovery efficiency)
	AvgLossRate     float64 // Alias for AvgGapRate for backward compatibility

	// NAK btree metrics (receiver side - for io_uring reorder handling)
	NakBtreeInserts     int64 // Sequences added to NAK btree
	NakBtreeDeletes     int64 // Sequences removed (packet arrived)
	NakBtreeExpired     int64 // Sequences removed (TSBPD expired)
	NakBtreeSize        int64 // Current btree size (gauge)
	NakBtreeScanPackets int64 // Packets scanned in periodicNakBtree()
	NakBtreeScanGaps    int64 // Gaps found during scan
	// NAK btree periodic NAK execution
	NakPeriodicOriginalRuns int64 // Times periodicNakOriginal() ran
	NakPeriodicBtreeRuns    int64 // Times periodicNakBtree() ran
	NakPeriodicSkipped      int64 // Times skipped (interval not elapsed)
	// NAK btree consolidation
	NakConsolidationRuns    int64 // Times consolidation ran
	NakConsolidationEntries int64 // Total entries produced
	NakConsolidationMerged  int64 // Sequences merged into ranges
	NakConsolidationTimeout int64 // Times hit time budget
	// FastNAK metrics
	NakFastTriggers       int64 // FastNAK activations
	NakFastRecentInserts  int64 // Sequences from FastNAKRecent
	NakFastRecentSkipped  int64 // Gap below threshold
	NakFastRecentOverflow int64 // Gap too large
	// Sender honor-order
	NakHonoredOrder int64 // NAK processing with honor-order

	// Duration
	Duration time.Duration

	// Error breakdown
	ErrorsByType map[string]int64

	// Defensive counters (should always be 0 in normal operation)
	NakBtreeNilWhenEnabled int64 // nakBtree nil when useNakBtree=true (ISSUE-001)
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
	// See integration_testing_design.md#3-understanding-loss-network-vs-srt for terminology

	// Packets sent/received from congestion control (includes retransmissions)
	dm.TotalPacketsSent = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_packets_total", "direction=\"send\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_packets_total", "direction=\"send\""))
	dm.TotalPacketsRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_packets_total", "direction=\"recv\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_packets_total", "direction=\"recv\""))

	// Gaps detected - sequence number gaps detected by receiver (≈ netem loss, NOT SRT loss)
	// This triggers NAK/retransmission - most gaps should be recovered
	dm.TotalGapsDetected = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_packets_lost_total", "direction=\"recv\""))
	dm.TotalPacketsLost = dm.TotalGapsDetected // Alias for backward compatibility

	// Packets dropped - packets that ARRIVED but were discarded (too late, duplicate, already ACK'd, etc.)
	dm.TotalPacketsDropped = int64(getSumByPrefix(last, "gosrt_connection_congestion_recv_data_drop_total") -
		getSumByPrefix(first, "gosrt_connection_congestion_recv_data_drop_total"))
	dm.TotalPacketsDropped += int64(getSumByPrefix(last, "gosrt_connection_congestion_send_data_drop_total") -
		getSumByPrefix(first, "gosrt_connection_congestion_send_data_drop_total"))

	// Granular drop reasons - for understanding WHY packets were dropped
	dm.TotalDropsTooLate = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"too_old\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"too_old\""))
	dm.TotalDropsAlreadyAck = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"already_acked\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"already_acked\""))
	dm.TotalDropsDuplicate = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"duplicate\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"duplicate\""))

	// TSBPD skipped - packets that NEVER arrived (skipped when ACK advanced past TSBPD time)
	// This is the TRUE unrecoverable loss counter
	dm.TotalPacketsSkippedTSBPD = int64(getSumByPrefix(last, "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total") -
		getSumByPrefix(first, "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total"))

	// Total unrecoverable = drops (arrived but discarded) + TSBPD skips (never arrived)
	dm.TotalPacketsUnrecoverable = dm.TotalPacketsDropped + dm.TotalPacketsSkippedTSBPD

	// Timer health counters - verify periodic routines are running
	dm.PeriodicACKRuns = int64(getSumByPrefix(last, "gosrt_connection_periodic_ack_runs_total") -
		getSumByPrefix(first, "gosrt_connection_periodic_ack_runs_total"))
	dm.PeriodicNAKRuns = int64(getSumByPrefix(last, "gosrt_connection_periodic_nak_runs_total") -
		getSumByPrefix(first, "gosrt_connection_periodic_nak_runs_total"))

	// Duplicate packets - already in buffer, confirms over-NAKing
	// These are packets that were re-requested by range NAKs but weren't actually lost
	dm.TotalDuplicates = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"duplicate\"") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_recv_data_drop_total", "reason=\"duplicate\""))

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

	// NAK Detail Counters (RFC SRT Appendix A)
	// Receiver side (sends NAKs) - Figure 21 (single) and Figure 22 (range)
	dm.NAKSinglesSent = int64(getSumByPrefixContaining(last, "gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"single\"") -
		getSumByPrefixContaining(first, "gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"single\""))
	dm.NAKRangesSent = int64(getSumByPrefixContaining(last, "gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"range\"") -
		getSumByPrefixContaining(first, "gosrt_connection_nak_entries_total", "direction=\"sent\",type=\"range\""))
	dm.NAKPktsRequested = int64(getSumByPrefixContaining(last, "gosrt_connection_nak_packets_requested_total", "direction=\"sent\"") -
		getSumByPrefixContaining(first, "gosrt_connection_nak_packets_requested_total", "direction=\"sent\""))

	// Sender side (receives NAKs)
	dm.NAKSinglesRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_nak_entries_total", "direction=\"recv\",type=\"single\"") -
		getSumByPrefixContaining(first, "gosrt_connection_nak_entries_total", "direction=\"recv\",type=\"single\""))
	dm.NAKRangesRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_nak_entries_total", "direction=\"recv\",type=\"range\"") -
		getSumByPrefixContaining(first, "gosrt_connection_nak_entries_total", "direction=\"recv\",type=\"range\""))
	dm.NAKPktsReceived = int64(getSumByPrefixContaining(last, "gosrt_connection_nak_packets_requested_total", "direction=\"recv\"") -
		getSumByPrefixContaining(first, "gosrt_connection_nak_packets_requested_total", "direction=\"recv\""))

	// ACKACK counters
	dm.TotalACKACKsSent = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_sent_total", "type=\"ackack\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_sent_total", "type=\"ackack\""))
	dm.TotalACKACKsRecv = int64(getSumByPrefixContaining(last, "gosrt_connection_packets_received_total", "type=\"ackack\"") -
		getSumByPrefixContaining(first, "gosrt_connection_packets_received_total", "type=\"ackack\""))

	// ========== NAK btree Metrics (io_uring reorder handling) ==========
	// Core operations
	dm.NakBtreeInserts = int64(getSumByPrefix(last, "gosrt_nak_btree_inserts_total") -
		getSumByPrefix(first, "gosrt_nak_btree_inserts_total"))
	dm.NakBtreeDeletes = int64(getSumByPrefix(last, "gosrt_nak_btree_deletes_total") -
		getSumByPrefix(first, "gosrt_nak_btree_deletes_total"))
	dm.NakBtreeExpired = int64(getSumByPrefix(last, "gosrt_nak_btree_expired_total") -
		getSumByPrefix(first, "gosrt_nak_btree_expired_total"))
	dm.NakBtreeSize = int64(getSumByPrefix(last, "gosrt_nak_btree_size"))
	dm.NakBtreeScanPackets = int64(getSumByPrefix(last, "gosrt_nak_btree_scan_packets_total") -
		getSumByPrefix(first, "gosrt_nak_btree_scan_packets_total"))
	dm.NakBtreeScanGaps = int64(getSumByPrefix(last, "gosrt_nak_btree_scan_gaps_total") -
		getSumByPrefix(first, "gosrt_nak_btree_scan_gaps_total"))

	// Periodic NAK execution
	dm.NakPeriodicOriginalRuns = int64(getSumByPrefixContaining(last, "gosrt_nak_periodic_runs_total", "impl=\"original\"") -
		getSumByPrefixContaining(first, "gosrt_nak_periodic_runs_total", "impl=\"original\""))
	dm.NakPeriodicBtreeRuns = int64(getSumByPrefixContaining(last, "gosrt_nak_periodic_runs_total", "impl=\"btree\"") -
		getSumByPrefixContaining(first, "gosrt_nak_periodic_runs_total", "impl=\"btree\""))
	dm.NakPeriodicSkipped = int64(getSumByPrefix(last, "gosrt_nak_periodic_skipped_total") -
		getSumByPrefix(first, "gosrt_nak_periodic_skipped_total"))

	// Consolidation
	dm.NakConsolidationRuns = int64(getSumByPrefix(last, "gosrt_nak_consolidation_runs_total") -
		getSumByPrefix(first, "gosrt_nak_consolidation_runs_total"))
	dm.NakConsolidationEntries = int64(getSumByPrefix(last, "gosrt_nak_consolidation_entries_total") -
		getSumByPrefix(first, "gosrt_nak_consolidation_entries_total"))
	dm.NakConsolidationMerged = int64(getSumByPrefix(last, "gosrt_nak_consolidation_merged_total") -
		getSumByPrefix(first, "gosrt_nak_consolidation_merged_total"))
	dm.NakConsolidationTimeout = int64(getSumByPrefix(last, "gosrt_nak_consolidation_timeout_total") -
		getSumByPrefix(first, "gosrt_nak_consolidation_timeout_total"))

	// FastNAK
	dm.NakFastTriggers = int64(getSumByPrefix(last, "gosrt_nak_fast_triggers_total") -
		getSumByPrefix(first, "gosrt_nak_fast_triggers_total"))
	dm.NakFastRecentInserts = int64(getSumByPrefix(last, "gosrt_nak_fast_recent_inserts_total") -
		getSumByPrefix(first, "gosrt_nak_fast_recent_inserts_total"))
	dm.NakFastRecentSkipped = int64(getSumByPrefix(last, "gosrt_nak_fast_recent_skipped_total") -
		getSumByPrefix(first, "gosrt_nak_fast_recent_skipped_total"))
	dm.NakFastRecentOverflow = int64(getSumByPrefix(last, "gosrt_nak_fast_recent_overflow_total") -
		getSumByPrefix(first, "gosrt_nak_fast_recent_overflow_total"))

	// Sender honor-order
	dm.NakHonoredOrder = int64(getSumByPrefix(last, "gosrt_connection_nak_honored_order_total") -
		getSumByPrefix(first, "gosrt_connection_nak_honored_order_total"))

	// Defensive counters (should always be 0)
	dm.NakBtreeNilWhenEnabled = int64(getSumByPrefixContaining(last, "gosrt_connection_congestion_internal_total", "nak_btree_nil_when_enabled") -
		getSumByPrefixContaining(first, "gosrt_connection_congestion_internal_total", "nak_btree_nil_when_enabled"))

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

	// Gap rate = gaps detected / packets sent (≈ netem loss rate)
	if dm.TotalPacketsSent > 0 {
		dm.AvgGapRate = float64(dm.TotalGapsDetected) / float64(dm.TotalPacketsSent)
		dm.AvgLossRate = dm.AvgGapRate // Alias for backward compatibility
	}

	// Drop rate = packets dropped / packets sent (= SRT loss rate, should be near 0)
	if dm.TotalPacketsSent > 0 {
		dm.AvgDropRate = float64(dm.TotalPacketsDropped) / float64(dm.TotalPacketsSent)
	}

	// Retransmission rate = retransmissions / gaps detected (recovery efficiency)
	if dm.TotalGapsDetected > 0 {
		dm.AvgRetransRate = float64(dm.TotalRetransmissions) / float64(dm.TotalGapsDetected)
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
//
// NOTE: Congestion drop counters (gosrt_connection_congestion_*_drop_total) are
// intentionally NOT included here. These drops include:
//   - already_acked: Redundant retransmits (NOT an error)
//   - duplicate: Duplicate packets (NOT an error)
//   - too_old: Arrived after TSBPD (TRUE error, but rare)
//
// The Statistical Validation properly handles drop analysis by checking:
//   - TotalPacketsSkippedTSBPD for TRUE unrecoverable losses
//   - Recovery rate = (gaps - skips) / gaps
//
// See defect11_error_analysis_too_strict.md for details.
var AnalysisErrorCounterPrefixes = []string{
	// Crypto errors - TRUE errors, should always be 0
	"gosrt_connection_crypto_error_total",

	// Receive path errors - TRUE errors, should always be 0
	"gosrt_connection_recv_data_error_total",
	"gosrt_connection_recv_control_error_total",

	// Send path errors - TRUE errors, should always be 0
	"gosrt_connection_send_data_drop_total",
	"gosrt_connection_send_control_drop_total",

	// Listener-level map lookup failures (programming errors - should be 0)
	// Note: These indicate bugs like Defect 8 Bug 3 (wrong lookup key)
	"gosrt_handshake_lookup_not_found_total",

	// Send path lookup failure - specifically detects Bug 3
	// Should always be 0 with the closure-based fix
	"gosrt_send_conn_lookup_not_found_total",
}

// ListenerWarningCounterPrefixes are counters that may indicate issues but
// can be non-zero during normal operation (e.g., during shutdown)
var ListenerWarningCounterPrefixes = []string{
	// Receive path lookup failures - can happen during shutdown
	"gosrt_recv_conn_lookup_not_found_total",
}

// ListenerInfoCounterPrefixes are informational counters (not errors)
var ListenerInfoCounterPrefixes = []string{
	// Duplicate handshakes (expected in some scenarios)
	"gosrt_handshake_duplicate_total",
	// Socket ID collisions (rare, but expected)
	"gosrt_socketid_collision_total",
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
// Note: Congestion drop counters are NOT in the error list (see defect11_error_analysis_too_strict.md)
// so this function no longer needs to handle them.
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

	// All counters in AnalysisErrorCounterPrefixes should be 0
	// Congestion drops are handled by Statistical Validation, not Error Analysis
	return 0
}

// ============================================================================
// CONNECTION LIFECYCLE ANALYSIS
// ============================================================================

// ConnectionLifecycleViolation represents a connection lifecycle issue
type ConnectionLifecycleViolation struct {
	Component string
	Issue     string
	Expected  int64
	Actual    int64
	Message   string
}

// ConnectionLifecycleResult holds connection lifecycle analysis results
type ConnectionLifecycleResult struct {
	Passed     bool
	Violations []ConnectionLifecycleViolation
	Warnings   []string

	// Per-component metrics
	ServerEstablished    int64
	ServerClosed         int64
	ServerClosedByReason map[string]int64

	CGEstablished    int64
	CGClosed         int64
	CGClosedByReason map[string]int64

	ClientEstablished    int64
	ClientClosed         int64
	ClientClosedByReason map[string]int64
}

// AnalyzeConnectionLifecycle checks that connection establishment and closure
// match expectations and detects unexpected close reasons (e.g., peer_idle_timeout).
// This is critical for detecting connection replacements during network impairment tests.
func AnalyzeConnectionLifecycle(ts *TestMetricsTimeSeries, config *TestConfig) ConnectionLifecycleResult {
	result := ConnectionLifecycleResult{
		Passed:               false, // Fail-safe: start with failure
		ServerClosedByReason: make(map[string]int64),
		CGClosedByReason:     make(map[string]int64),
		ClientClosedByReason: make(map[string]int64),
	}

	// Expected connection counts for standard test setup:
	// - Server: 2 connections (one from client-generator, one from client)
	// - Client-Generator: 1 connection (to server)
	// - Client: 1 connection (to server)
	// These defaults work for all current integration tests.
	// If needed, make these configurable via TestConfig in the future.
	expectedServer := int64(2)
	expectedCG := int64(1)
	expectedClient := int64(1)

	componentsChecked := 0

	// Helper to extract lifecycle metrics from a component
	// NOTE: We use ABSOLUTE values from the final snapshot, not deltas.
	// Each test runs fresh processes that start with 0 connections.
	// The "initial metrics" are collected AFTER connections are established,
	// so deltas would incorrectly show 0. The absolute final value IS the
	// count of connections established during this test.
	extractLifecycle := func(component MetricsTimeSeries) (established, closed int64, byReason map[string]int64) {
		byReason = make(map[string]int64)

		if len(component.Snapshots) < 1 {
			return 0, 0, byReason
		}

		// Find the last successful snapshot
		var last *MetricsSnapshot
		for _, s := range component.Snapshots {
			if s.Error == nil {
				last = s
			}
		}
		if last == nil {
			return 0, 0, byReason
		}

		// Get lifecycle counters - use ABSOLUTE values (not deltas)
		// Since each test runs fresh processes, absolute count = test result
		established = int64(getMetricValue(last, "gosrt_connections_established_total"))
		closed = int64(getMetricValue(last, "gosrt_connections_closed_total"))

		// Get close reasons - also absolute values
		reasons := []string{"graceful", "peer_idle_timeout", "context_cancelled", "error"}
		for _, reason := range reasons {
			value := int64(getMetricValueWithLabel(last, "gosrt_connections_closed_by_reason_total", "reason=\""+reason+"\""))
			if value > 0 {
				byReason[reason] = value
			}
		}

		return established, closed, byReason
	}

	// Analyze server
	if len(ts.Server.Snapshots) >= 2 {
		componentsChecked++
		result.ServerEstablished, result.ServerClosed, result.ServerClosedByReason = extractLifecycle(ts.Server)

		// Check established matches expected
		if result.ServerEstablished != expectedServer {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "server",
				Issue:     "established_mismatch",
				Expected:  expectedServer,
				Actual:    result.ServerEstablished,
				Message:   fmt.Sprintf("Server: expected %d connections established, got %d", expectedServer, result.ServerEstablished),
			})
		}

		// Check that connections are still OPEN during pre-shutdown metrics collection.
		// Per graceful_quiesce_design.md, we collect metrics while connections are alive.
		// closed > 0 would indicate premature disconnections during the test (bad!).
		// This is especially important for pattern-based tests (e.g., Starlink) where
		// we want to verify that outages didn't cause connection failures.
		if result.ServerClosed != 0 {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "server",
				Issue:     "premature_closure",
				Expected:  0,
				Actual:    result.ServerClosed,
				Message:   fmt.Sprintf("Server: %d connection(s) closed during test (expected 0 - connections should remain open until shutdown)", result.ServerClosed),
			})
		}

		// Check for unexpected close reasons
		if peerIdle, ok := result.ServerClosedByReason["peer_idle_timeout"]; ok && peerIdle > 0 {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "server",
				Issue:     "peer_idle_timeout",
				Expected:  0,
				Actual:    peerIdle,
				Message:   fmt.Sprintf("Server: %d connection(s) closed due to peer_idle_timeout (unexpected)", peerIdle),
			})
		}
	}

	// Analyze client-generator
	if len(ts.ClientGenerator.Snapshots) >= 2 {
		componentsChecked++
		result.CGEstablished, result.CGClosed, result.CGClosedByReason = extractLifecycle(ts.ClientGenerator)

		if result.CGEstablished != expectedCG {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "client-generator",
				Issue:     "established_mismatch",
				Expected:  expectedCG,
				Actual:    result.CGEstablished,
				Message:   fmt.Sprintf("Client-Generator: expected %d connections established, got %d", expectedCG, result.CGEstablished),
			})
		}

		// Check for premature closures (see server comment for rationale)
		if result.CGClosed != 0 {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "client-generator",
				Issue:     "premature_closure",
				Expected:  0,
				Actual:    result.CGClosed,
				Message:   fmt.Sprintf("Client-Generator: %d connection(s) closed during test (expected 0)", result.CGClosed),
			})
		}

		if peerIdle, ok := result.CGClosedByReason["peer_idle_timeout"]; ok && peerIdle > 0 {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "client-generator",
				Issue:     "peer_idle_timeout",
				Expected:  0,
				Actual:    peerIdle,
				Message:   fmt.Sprintf("Client-Generator: %d connection(s) closed due to peer_idle_timeout (unexpected)", peerIdle),
			})
		}
	}

	// Analyze client
	if len(ts.Client.Snapshots) >= 2 {
		componentsChecked++
		result.ClientEstablished, result.ClientClosed, result.ClientClosedByReason = extractLifecycle(ts.Client)

		if result.ClientEstablished != expectedClient {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "client",
				Issue:     "established_mismatch",
				Expected:  expectedClient,
				Actual:    result.ClientEstablished,
				Message:   fmt.Sprintf("Client: expected %d connections established, got %d", expectedClient, result.ClientEstablished),
			})
		}

		// Check for premature closures (see server comment for rationale)
		if result.ClientClosed != 0 {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "client",
				Issue:     "premature_closure",
				Expected:  0,
				Actual:    result.ClientClosed,
				Message:   fmt.Sprintf("Client: %d connection(s) closed during test (expected 0)", result.ClientClosed),
			})
		}

		if peerIdle, ok := result.ClientClosedByReason["peer_idle_timeout"]; ok && peerIdle > 0 {
			result.Violations = append(result.Violations, ConnectionLifecycleViolation{
				Component: "client",
				Issue:     "peer_idle_timeout",
				Expected:  0,
				Actual:    peerIdle,
				Message:   fmt.Sprintf("Client: %d connection(s) closed due to peer_idle_timeout (unexpected)", peerIdle),
			})
		}
	}

	// EXPLICIT PASS: Only pass if we checked components AND found no violations
	if componentsChecked > 0 && len(result.Violations) == 0 {
		result.Passed = true
	}

	return result
}

// getMetricValue gets a single metric value from a snapshot by exact name
func getMetricValue(snapshot *MetricsSnapshot, name string) float64 {
	if snapshot == nil {
		return 0
	}
	for metricName, value := range snapshot.Metrics {
		if metricName == name {
			return value
		}
	}
	return 0
}

// getMetricValueWithLabel gets a metric value with a specific label
func getMetricValueWithLabel(snapshot *MetricsSnapshot, prefix, labelSubstr string) float64 {
	if snapshot == nil {
		return 0
	}
	for metricName, value := range snapshot.Metrics {
		if strings.HasPrefix(metricName, prefix) && strings.Contains(metricName, labelSubstr) {
			return value
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

	// For received packets, account for TSBPD buffer delay
	// With large buffers, subscriber won't receive data until after the buffer delay
	// Effective receive time = test_duration - buffer_delay
	latency := getEffectiveLatency(config)
	effectiveRecvDuration := config.TestDuration - latency
	if effectiveRecvDuration < 0 {
		effectiveRecvDuration = 0
	}

	// Calculate what percentage of data the subscriber will have received
	recvRatio := effectiveRecvDuration.Seconds() / config.TestDuration.Seconds()
	if recvRatio > 1.0 {
		recvRatio = 1.0
	}

	// For received packets, expect at least 80% of the effective receive ratio
	// (allows for startup delay and buffer timing)
	minRecv := int64(float64(packetsExpected) * recvRatio * 0.80)
	if minRecv < 100 {
		minRecv = 100 // Minimum sanity check
	}

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

// getEffectiveLatency returns the effective TSBPD latency from config
func getEffectiveLatency(config *TestConfig) time.Duration {
	// Check SharedSRT first
	if config.SharedSRT != nil && config.SharedSRT.Latency > 0 {
		return config.SharedSRT.Latency
	}
	// Then check component configs
	if config.Server.SRT.Latency > 0 {
		return config.Server.SRT.Latency
	}
	if config.ClientGenerator.SRT.Latency > 0 {
		return config.ClientGenerator.SRT.Latency
	}
	if config.Client.SRT.Latency > 0 {
		return config.Client.SRT.Latency
	}
	// Default SRT latency is 120ms
	return 120 * time.Millisecond
}

// ============================================================================
// RATE METRICS VALIDATION (Phase 1: Lockless Design)
// ============================================================================
// Validates that reported rate metrics from Prometheus closely match:
// 1. Computed average rates (total bytes / test duration)
// 2. Configured test bitrate (if specified)
//
// This validates the Phase 1 lockless rate metrics implementation.
// See: gosrt_lockless_design.md

// RateMetricsViolation describes a rate metric validation failure
type RateMetricsViolation struct {
	Component    string  // "Server", "ClientGenerator", "Client"
	MetricName   string  // e.g., "RecvRateMbps", "SendRateMbps"
	ExpectedMbps float64 // Expected rate (from computed avg or config)
	ActualMbps   float64 // Actual reported rate from Prometheus
	VariancePct  float64 // Percentage variance
	Threshold    float64 // Allowed variance threshold
	Message      string  // Human-readable description
}

// RateMetricsResult holds the result of rate metrics validation
type RateMetricsResult struct {
	Passed     bool
	Violations []RateMetricsViolation
	Warnings   []string

	// Component rate summaries
	ServerRecvRateMbps    float64
	ServerSendRateMbps    float64
	ClientGenSendRateMbps float64
	ClientRecvRateMbps    float64
	ConfiguredBitrateMbps float64
}

// VerifyRateMetrics validates that Prometheus rate metrics match computed averages
// and configured bitrate. This is a key validation for Phase 1 lockless design.
//
// Validation thresholds:
// - Computed avg vs Prometheus rate: 20% variance allowed (rates are smoothed)
// - Prometheus rate vs configured bitrate: 15% variance allowed
func VerifyRateMetrics(ts *TestMetricsTimeSeries, config *TestConfig) RateMetricsResult {
	result := RateMetricsResult{
		Passed: false, // Fail-safe: default to failed
	}

	// Get configured bitrate
	if config != nil && config.Bitrate > 0 {
		result.ConfiguredBitrateMbps = float64(config.Bitrate) / 1_000_000
	}

	// Compute derived metrics for each component
	serverMetrics := ComputeDerivedMetrics(ts.Server)
	clientGenMetrics := ComputeDerivedMetrics(ts.ClientGenerator)
	clientMetrics := ComputeDerivedMetrics(ts.Client)

	// Store computed rates for reporting
	result.ServerRecvRateMbps = serverMetrics.AvgRecvRateMbps
	result.ClientGenSendRateMbps = clientGenMetrics.AvgSendRateMbps
	result.ClientRecvRateMbps = clientMetrics.AvgRecvRateMbps

	// Define variance thresholds
	const computedVsReportedThreshold = 0.20 // 20% variance for computed vs Prometheus
	const configuredThreshold = 0.15         // 15% variance for configured bitrate check

	// Helper function to check rate variance
	checkRate := func(component, metricName string, expectedMbps, actualMbps, threshold float64) {
		if expectedMbps <= 0 {
			return // Skip if no expected rate
		}

		variancePct := 0.0
		if expectedMbps > 0 {
			variancePct = (actualMbps - expectedMbps) / expectedMbps
			if variancePct < 0 {
				variancePct = -variancePct // Absolute value
			}
		}

		if variancePct > threshold {
			result.Violations = append(result.Violations, RateMetricsViolation{
				Component:    component,
				MetricName:   metricName,
				ExpectedMbps: expectedMbps,
				ActualMbps:   actualMbps,
				VariancePct:  variancePct * 100,
				Threshold:    threshold * 100,
				Message: fmt.Sprintf("%s %s: expected %.2f Mbps, got %.2f Mbps (%.1f%% variance, threshold %.0f%%)",
					component, metricName, expectedMbps, actualMbps, variancePct*100, threshold*100),
			})
		}
	}

	// Check server receive rate against computed average
	// Server receives from client-generator, so compare with clientGen send rate
	if clientGenMetrics.AvgSendRateMbps > 0 && serverMetrics.AvgRecvRateMbps > 0 {
		checkRate("Server", "RecvRate vs ClientGen SendRate",
			clientGenMetrics.AvgSendRateMbps, serverMetrics.AvgRecvRateMbps, computedVsReportedThreshold)
	}

	// Check client receive rate against server send rate (passthrough)
	if serverMetrics.AvgSendRateMbps > 0 && clientMetrics.AvgRecvRateMbps > 0 {
		checkRate("Client", "RecvRate vs Server SendRate",
			serverMetrics.AvgSendRateMbps, clientMetrics.AvgRecvRateMbps, computedVsReportedThreshold)
	}

	// Check against configured bitrate if available
	if result.ConfiguredBitrateMbps > 0 && config != nil && config.TestDuration > 0 {
		// Re-compute rates using TestDuration (active transmission period) instead of
		// snapshot duration (which includes the quiesce phase where rate is 0).
		// This fixes rate validation for tests with a quiesce phase.
		activeSeconds := config.TestDuration.Seconds()

		// Client-generator should be sending at configured rate
		if clientGenMetrics.TotalBytesSent > 0 {
			// Compute rate using active transmission duration
			activeSendRateMbps := float64(clientGenMetrics.TotalBytesSent*8) / activeSeconds / 1_000_000
			result.ClientGenSendRateMbps = activeSendRateMbps // Update stored rate
			checkRate("ClientGenerator", "SendRate vs Configured",
				result.ConfiguredBitrateMbps, activeSendRateMbps, configuredThreshold)
		}

		// Client should be receiving at approximately configured rate (minus losses)
		if clientMetrics.TotalBytesRecv > 0 {
			// Compute rate using active transmission duration
			activeRecvRateMbps := float64(clientMetrics.TotalBytesRecv*8) / activeSeconds / 1_000_000
			result.ClientRecvRateMbps = activeRecvRateMbps // Update stored rate
			// Allow more variance for receive (account for potential losses)
			checkRate("Client", "RecvRate vs Configured",
				result.ConfiguredBitrateMbps, activeRecvRateMbps, configuredThreshold+0.10)
		}
	}

	// Add warnings for missing rate data
	if clientGenMetrics.AvgSendRateMbps == 0 {
		result.Warnings = append(result.Warnings, "ClientGenerator: No send rate data available")
	}
	if serverMetrics.AvgRecvRateMbps == 0 {
		result.Warnings = append(result.Warnings, "Server: No receive rate data available")
	}
	if clientMetrics.AvgRecvRateMbps == 0 {
		result.Warnings = append(result.Warnings, "Client: No receive rate data available")
	}

	// Pass if no violations (warnings are allowed)
	result.Passed = len(result.Violations) == 0

	return result
}

// AnalysisResult aggregates all analysis components
type AnalysisResult struct {
	TestName   string
	TestConfig *TestConfig
	Passed     bool

	// Component results
	ErrorAnalysis         ErrorAnalysisResult
	PositiveSignals       PositiveSignalResult
	ConnectionLifecycle   ConnectionLifecycleResult   // Connection establishment/closure tracking
	StatisticalValidation StatisticalValidationResult // For network impairment tests
	PipelineBalance       PipelineBalanceResult       // Clean network pipeline verification
	RateMetrics           RateMetricsResult           // Phase 1: Lockless rate metrics validation

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
	lifecycleResult := AnalyzeConnectionLifecycle(ts, config)
	statisticalResult := ValidateStatistical(ts, config)
	rateMetricsResult := VerifyRateMetrics(ts, config) // Phase 1: Lockless rate validation

	// FAIL-SAFE: Default to failed - only set to passed after ALL checks confirm success
	result := AnalysisResult{
		TestName:              ts.TestName,
		TestConfig:            config,
		Passed:                false, // NEVER assume success - must be explicitly confirmed
		ErrorAnalysis:         errorResult,
		PositiveSignals:       signalResult,
		ConnectionLifecycle:   lifecycleResult,
		StatisticalValidation: statisticalResult,
		RateMetrics:           rateMetricsResult,
	}

	// Compute derived metrics for reporting
	result.ServerMetrics = ComputeDerivedMetrics(ts.Server)
	result.ClientGenMetrics = ComputeDerivedMetrics(ts.ClientGenerator)
	result.ClientMetrics = ComputeDerivedMetrics(ts.Client)

	// Pipeline balance verification for clean network tests
	// Uses a tolerance for timing differences during shutdown when packets
	// may still be in the TSBPD buffer waiting for delivery.
	// Note: Mode defaults to "" (empty), treat "" and "clean" as clean network
	pipelineBalanceRequired := false
	if config == nil || config.Mode == TestModeClean || config.Mode == "" {
		pipelineBalanceRequired = true
		// Allow tolerance for timing: ~0.5% or 10 packets, whichever is larger
		// This accounts for packets still in TSBPD buffer at shutdown time
		tolerance := int64(10)
		if result.ServerMetrics.TotalPacketsSent > 2000 {
			tolerance = result.ServerMetrics.TotalPacketsSent / 200 // 0.5%
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
		len(statisticalResult.Violations) + len(rateMetricsResult.Violations)
	result.TotalWarnings += len(statisticalResult.Warnings) + len(rateMetricsResult.Warnings)

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
	lifecyclePassed := lifecycleResult.Passed
	rateMetricsPassed := rateMetricsResult.Passed // Phase 1: Lockless rate metrics
	if errorResult.Passed && signalResult.Passed && lifecyclePassed && statisticalResult.Passed && runtimePassed && pipelinePassed && rateMetricsPassed {
		result.Passed = true
	}

	// Add lifecycle violations to total
	result.TotalViolations += len(lifecycleResult.Violations)

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

	// Connection Lifecycle
	if result.ConnectionLifecycle.Passed {
		fmt.Println("\nConnection Lifecycle: ✓ PASSED")
		fmt.Printf("  ✓ Server: %d established, %d closed (graceful)\n",
			result.ConnectionLifecycle.ServerEstablished, result.ConnectionLifecycle.ServerClosed)
		fmt.Printf("  ✓ Client-Generator: %d established, %d closed (graceful)\n",
			result.ConnectionLifecycle.CGEstablished, result.ConnectionLifecycle.CGClosed)
		fmt.Printf("  ✓ Client: %d established, %d closed (graceful)\n",
			result.ConnectionLifecycle.ClientEstablished, result.ConnectionLifecycle.ClientClosed)
	} else {
		fmt.Println("\nConnection Lifecycle: ✗ FAILED")
		for _, v := range result.ConnectionLifecycle.Violations {
			fmt.Printf("  ✗ %s\n", v.Message)
		}
		// Show details even if failed
		if result.ConnectionLifecycle.ServerEstablished > 0 || result.ConnectionLifecycle.ServerClosed > 0 {
			fmt.Printf("    Server: %d established, %d closed",
				result.ConnectionLifecycle.ServerEstablished, result.ConnectionLifecycle.ServerClosed)
			if len(result.ConnectionLifecycle.ServerClosedByReason) > 0 {
				fmt.Printf(" (")
				first := true
				for reason, count := range result.ConnectionLifecycle.ServerClosedByReason {
					if !first {
						fmt.Printf(", ")
					}
					fmt.Printf("%s=%d", reason, count)
					first = false
				}
				fmt.Printf(")")
			}
			fmt.Println()
		}
		if result.ConnectionLifecycle.CGEstablished > 0 || result.ConnectionLifecycle.CGClosed > 0 {
			fmt.Printf("    Client-Generator: %d established, %d closed",
				result.ConnectionLifecycle.CGEstablished, result.ConnectionLifecycle.CGClosed)
			if len(result.ConnectionLifecycle.CGClosedByReason) > 0 {
				fmt.Printf(" (")
				first := true
				for reason, count := range result.ConnectionLifecycle.CGClosedByReason {
					if !first {
						fmt.Printf(", ")
					}
					fmt.Printf("%s=%d", reason, count)
					first = false
				}
				fmt.Printf(")")
			}
			fmt.Println()
		}
		if result.ConnectionLifecycle.ClientEstablished > 0 || result.ConnectionLifecycle.ClientClosed > 0 {
			fmt.Printf("    Client: %d established, %d closed",
				result.ConnectionLifecycle.ClientEstablished, result.ConnectionLifecycle.ClientClosed)
			if len(result.ConnectionLifecycle.ClientClosedByReason) > 0 {
				fmt.Printf(" (")
				first := true
				for reason, count := range result.ConnectionLifecycle.ClientClosedByReason {
					if !first {
						fmt.Printf(", ")
					}
					fmt.Printf("%s=%d", reason, count)
					first = false
				}
				fmt.Printf(")")
			}
			fmt.Println()
		}
	}

	// Statistical Validation (only for network impairment tests)
	if result.TestConfig != nil && result.TestConfig.Mode == TestModeNetwork &&
		result.TestConfig.Impairment.LossRate > 0 {
		if result.StatisticalValidation.Passed {
			fmt.Println("\nStatistical Validation: ✓ PASSED")
			// Show the measured retransmission rate and the tolerance range
			configuredLoss := result.TestConfig.Impairment.LossRate
			tolerance := float64(1.0) // ±100% default tolerance for bidirectional loss
			if result.TestConfig.Impairment.Thresholds != nil && result.TestConfig.Impairment.Thresholds.LossRateTolerance > 0 {
				tolerance = result.TestConfig.Impairment.Thresholds.LossRateTolerance
			}
			lowerBound := configuredLoss * (1 - tolerance) * 100
			upperBound := configuredLoss * (1 + tolerance) * 100
			if lowerBound < 0 {
				lowerBound = 0
			}
			// Calculate measured retrans rate from actual metrics
			packetsSent := result.ClientGenMetrics.TotalPacketsSent
			totalRetrans := result.ClientGenMetrics.TotalRetransmissions
			measuredRetrans := float64(0)
			if packetsSent > 0 {
				measuredRetrans = float64(totalRetrans) / float64(packetsSent) * 100
			}
			fmt.Printf("  ✓ RetransPctOfSent: %.2f%% within tolerance (expected: %.1f%% - %.1f%% for %.1f%% netem loss)\n",
				measuredRetrans, lowerBound, upperBound, configuredLoss*100)
		} else {
			fmt.Println("\nStatistical Validation: ✗ FAILED")
			for _, v := range result.StatisticalValidation.Violations {
				fmt.Printf("  ✗ %s: expected %s, got %.2f\n", v.Metric, v.ExpectedRange, v.Observed)
				fmt.Printf("    %s\n", v.Message)
			}
		}
		// Print non-per-connection warnings
		for _, w := range result.StatisticalValidation.Warnings {
			// Skip per-connection stats (printed separately below)
			if !strings.Contains(w.Metric, "/Stats") {
				fmt.Printf("  ⚠ %s: %s\n", w.Metric, w.Message)
			}
		}

		// Print per-connection analysis summary
		fmt.Println("\n  Per-Connection Analysis:")
		printConnectionSummary("Connection1", result.ServerMetrics, result.ClientGenMetrics)
		printConnectionSummary("Connection2", result.ClientMetrics, result.ServerMetrics)

		// Print combined statistics summary
		fmt.Println("\n  Combined Statistics (see integration_testing_design.md section 3):")
		packetsSent := result.ClientGenMetrics.TotalPacketsSent
		totalGaps := result.ServerMetrics.TotalGapsDetected + result.ClientMetrics.TotalGapsDetected
		totalRetrans := result.ClientGenMetrics.TotalRetransmissions + result.ServerMetrics.TotalRetransmissions

		// TRUE losses: ONLY skips (packets that NEVER arrived before TSBPD)
		// ALL drops (too_late, already_acked, duplicate) are redundant arrivals
		totalTrueLosses := result.ServerMetrics.TotalPacketsSkippedTSBPD + result.ClientMetrics.TotalPacketsSkippedTSBPD
		totalRedundant := result.ServerMetrics.TotalPacketsDropped + result.ClientMetrics.TotalPacketsDropped

		retransPctOfSent := float64(0)
		gapsPctOfSent := float64(0)
		if packetsSent > 0 {
			retransPctOfSent = float64(totalRetrans) / float64(packetsSent) * 100
			gapsPctOfSent = float64(totalGaps) / float64(packetsSent) * 100
		}
		recoveryRate := float64(0)
		if totalGaps > 0 {
			// Only TSBPD skips (never arrived) are true losses
			recoveryRate = (1.0 - float64(totalTrueLosses)/float64(totalGaps)) * 100
		} else {
			recoveryRate = 100.0
		}

		fmt.Printf("    netem configured: %.1f%% bidirectional loss\n", result.TestConfig.Impairment.LossRate*100)
		fmt.Printf("    Original packets: %d\n", packetsSent)
		fmt.Printf("    Total gaps: %d (%.2f%% combined rate)\n", totalGaps, gapsPctOfSent)
		fmt.Printf("    Total retransmissions: %d (%.2f%% of original)\n", totalRetrans, retransPctOfSent)
		fmt.Printf("    True losses (TSBPD skips): %d\n", totalTrueLosses)
		fmt.Printf("    Redundant copies discarded: %d (late arrivals)\n", totalRedundant)
		fmt.Printf("    Combined recovery rate: %.1f%%\n", recoveryRate)

		// NAK Detail Analysis (RFC SRT Appendix A)
		// NAK counters track PACKETS: SinglesPkts + RangesPkts = TotalPktsRequested
		if len(result.StatisticalValidation.NAKDetailResults) > 0 {
			fmt.Println("\n  NAK Detail Analysis (RFC SRT Appendix A):")
			fmt.Println("    (NAK counters track packets, not entries)")
			for _, nak := range result.StatisticalValidation.NAKDetailResults {
				if nak.TotalPktsRequested > 0 {
					fmt.Printf("    %s: %d pkts (singles) + %d pkts (ranges) = %d total\n",
						nak.ConnectionName, nak.SinglesPkts, nak.RangesPkts, nak.TotalPktsRequested)
					fmt.Printf("      Delivery: %.1f%%, Fulfillment: %.1f%%, Range ratio: %.0f%%\n",
						nak.NAKDeliveryRate*100, nak.NAKFulfillmentRate*100, nak.RangePktRatio*100)
				}
			}
		}
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

	// Rate Metrics Validation (Phase 1: Lockless)
	if result.RateMetrics.ConfiguredBitrateMbps > 0 || result.RateMetrics.ServerRecvRateMbps > 0 {
		if result.RateMetrics.Passed {
			fmt.Println("\nRate Metrics: ✓ PASSED")
			if result.RateMetrics.ConfiguredBitrateMbps > 0 {
				fmt.Printf("  ✓ Configured: %.2f Mbps\n", result.RateMetrics.ConfiguredBitrateMbps)
			}
			if result.RateMetrics.ClientGenSendRateMbps > 0 {
				fmt.Printf("  ✓ ClientGen send: %.2f Mbps\n", result.RateMetrics.ClientGenSendRateMbps)
			}
			if result.RateMetrics.ServerRecvRateMbps > 0 {
				fmt.Printf("  ✓ Server recv: %.2f Mbps\n", result.RateMetrics.ServerRecvRateMbps)
			}
			if result.RateMetrics.ClientRecvRateMbps > 0 {
				fmt.Printf("  ✓ Client recv: %.2f Mbps\n", result.RateMetrics.ClientRecvRateMbps)
			}
		} else {
			fmt.Println("\nRate Metrics: ✗ FAILED")
			for _, v := range result.RateMetrics.Violations {
				fmt.Printf("  ✗ %s\n", v.Message)
			}
		}
		for _, w := range result.RateMetrics.Warnings {
			fmt.Printf("  ⚠ %s\n", w)
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

	// NAK Detail Analysis (RFC SRT Appendix A)
	NAKDetailResults []NAKDetailResult
}

// NAKDetailResult contains per-connection NAK detail analysis
// NAK counters track PACKETS (not entries): SinglesPkts + RangesPkts = TotalPktsRequested
type NAKDetailResult struct {
	ConnectionName     string
	NAKDeliveryRate    float64 // NAKPktsReceived / NAKPktsRequested (should be ~1.0)
	NAKFulfillmentRate float64 // RetransSent / NAKPktsReceived (should be ~1.0)
	RangePktRatio      float64 // NAKRangesSent / NAKPktsRequested (proportion from ranges)
	SinglesPkts        int64   // Packets requested via single NAK entries
	RangesPkts         int64   // Packets requested via range NAK entries
	TotalPktsRequested int64   // Total = SinglesPkts + RangesPkts
	HasIssues          bool    // True if any validation failed
	Issues             []string
}

// ValidateNAKDetail validates NAK detail metrics for a single connection
// NAK counters track PACKETS: SinglesPkts + RangesPkts = TotalPktsRequested
func ValidateNAKDetail(conn ConnectionAnalysis) NAKDetailResult {
	result := NAKDetailResult{
		ConnectionName:     conn.Name,
		SinglesPkts:        conn.NAKSinglesSent,
		RangesPkts:         conn.NAKRangesSent,
		TotalPktsRequested: conn.NAKPktsRequested,
	}

	// Use pre-computed rates from ConnectionAnalysis
	result.NAKDeliveryRate = conn.NAKDeliveryRate
	result.NAKFulfillmentRate = conn.NAKFulfillmentRate
	result.RangePktRatio = conn.RangePktRatio

	// Verify counter invariant: SinglesPkts + RangesPkts = TotalPktsRequested
	expectedTotal := result.SinglesPkts + result.RangesPkts
	if expectedTotal != result.TotalPktsRequested {
		result.HasIssues = true
		result.Issues = append(result.Issues,
			fmt.Sprintf("NAK counter invariant violation: %d + %d ≠ %d",
				result.SinglesPkts, result.RangesPkts, result.TotalPktsRequested))
	}

	// Validate NAK delivery (are NAKs getting through the network?)
	if conn.NAKPktsRequested > 0 && result.NAKDeliveryRate < 0.9 {
		result.HasIssues = true
		result.Issues = append(result.Issues,
			fmt.Sprintf("NAK delivery rate %.1f%% - NAKs being lost by network", result.NAKDeliveryRate*100))
	}

	// Validate NAK fulfillment (are retransmissions being sent?)
	// Key Verification: RetransSent ≈ NAKPktsReceived
	if conn.NAKPktsReceived > 0 && result.NAKFulfillmentRate < 0.8 {
		result.Issues = append(result.Issues,
			fmt.Sprintf("NAK fulfillment rate %.1f%% - sender can't retransmit (buffer exhausted?)", result.NAKFulfillmentRate*100))
	}
	if conn.NAKPktsReceived > 0 && result.NAKFulfillmentRate > 1.1 {
		result.Issues = append(result.Issues,
			fmt.Sprintf("NAK fulfillment rate %.1f%% - retransmitting more than requested (possible bug)", result.NAKFulfillmentRate*100))
	}

	return result
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
// See integration_testing_design.md#3-understanding-loss-network-vs-srt for terminology
// ConnectionAnalysis holds metrics for a single SRT connection endpoint pair
// This enables independent analysis of each of the two SRT connections:
// Connection 1: ClientGenerator (publisher) → Server
// Connection 2: Server → Client (subscriber)
type ConnectionAnalysis struct {
	Name string // "publisher-to-server" or "server-to-subscriber"

	// === FROM SENDER SIDE ===
	PacketsSent       int64 // congestion_packets_total{direction="send"}
	PacketsSentUnique int64 // congestion_packets_unique_total{direction="send"} (excl. retrans)
	RetransSent       int64 // congestion_retransmissions_total{direction="send"}
	NAKsReceived      int64 // packets_received_total{type="nak"}
	ACKsReceived      int64 // packets_received_total{type="ack"}
	SendDrops         int64 // congestion_send_data_drop_total

	// === NAK DETAIL - SENDER (receives NAKs) ===
	// RFC SRT Appendix A: NAK loss list encoding
	// Counters track PACKETS (not entries): NAKSinglesRecv + NAKRangesRecv = NAKPktsReceived
	NAKSinglesRecv  int64 // Packets requested via single NAK entries received
	NAKRangesRecv   int64 // Packets requested via range NAK entries received
	NAKPktsReceived int64 // Total = NAKSinglesRecv + NAKRangesRecv

	// === FROM RECEIVER SIDE ===
	PacketsRecv       int64 // congestion_packets_total{direction="recv"}
	PacketsRecvUnique int64 // congestion_packets_unique_total{direction="recv"} (excl. dupes)
	GapsDetected      int64 // congestion_packets_lost_total{direction="recv"}
	RetransRecv       int64 // congestion_retransmissions_total{direction="recv"}
	NAKsSent          int64 // packets_sent_total{type="nak"}
	ACKsSent          int64 // packets_sent_total{type="ack"}
	RecvDrops         int64 // congestion_recv_data_drop_total (arrived but discarded)
	RecvSkippedTSBPD  int64 // congestion_recv_pkt_skipped_tsbpd_total (NEVER arrived)
	RecvUnrecoverable int64 // RecvDrops + RecvSkippedTSBPD = true unrecoverable

	// === TIMER HEALTH ===
	PeriodicACKRuns int64 // periodic_ack_runs_total (~100/sec expected)
	PeriodicNAKRuns int64 // periodic_nak_runs_total (~50/sec expected)

	// === NAK DETAIL - RECEIVER (sends NAKs) ===
	// RFC SRT Appendix A: NAK loss list encoding
	// Counters track PACKETS (not entries): NAKSinglesSent + NAKRangesSent = NAKPktsRequested
	NAKSinglesSent   int64 // Packets requested via single NAK entries sent
	NAKRangesSent    int64 // Packets requested via range NAK entries sent
	NAKPktsRequested int64 // Total = NAKSinglesSent + NAKRangesSent

	// === COMPUTED METRICS ===
	GapRate          float64 // GapsDetected / PacketsSent (≈ netem loss)
	RetransPctOfSent float64 // RetransSent / PacketsSent (should match netem loss)
	RecoveryRate     float64 // 1 - (RecvUnrecoverable / GapsDetected) = % successfully recovered
	NAKEfficiency    float64 // NAKsSent / GapsDetected (should be ~1.0)

	// === NAK DETAIL COMPUTED METRICS ===
	// Key Verification: RetransSent ≈ NAKPktsReceived (sender fulfilling NAK requests)
	NAKDeliveryRate    float64 // NAKPktsReceived / NAKPktsRequested (should be ~1.0)
	NAKFulfillmentRate float64 // RetransSent / NAKPktsReceived (should be ~1.0)
	RangePktRatio      float64 // NAKRangesSent / NAKPktsRequested (proportion from ranges)
}

// computeRates calculates derived rates for a connection
func (c *ConnectionAnalysis) computeRates() {
	if c.PacketsSent > 0 {
		c.GapRate = float64(c.GapsDetected) / float64(c.PacketsSent)
		c.RetransPctOfSent = float64(c.RetransSent) / float64(c.PacketsSent)
	}

	// RecvUnrecoverable is now ONLY TSBPD skips (true losses)
	// RecvDrops are redundant arrivals (late copies, duplicates) - NOT true losses
	c.RecvUnrecoverable = c.RecvSkippedTSBPD

	if c.GapsDetected > 0 {
		// Recovery rate = 1 - (true losses / gaps)
		// Only TSBPD skips are true losses; all drops are redundant arrivals
		c.RecoveryRate = 1.0 - (float64(c.RecvUnrecoverable) / float64(c.GapsDetected))
		if c.RecoveryRate < 0 {
			c.RecoveryRate = 0
		}
		c.NAKEfficiency = float64(c.NAKsSent) / float64(c.GapsDetected)
	} else {
		c.RecoveryRate = 1.0 // No gaps = 100% recovery
		c.NAKEfficiency = 0  // No NAKs needed
	}

	// NAK Detail Rates (RFC SRT Appendix A)
	// Key Invariant: NAKSinglesSent + NAKRangesSent = NAKPktsRequested (packets, not entries)
	//
	// NAK Delivery: Are NAKs getting through the network?
	if c.NAKPktsRequested > 0 {
		c.NAKDeliveryRate = float64(c.NAKPktsReceived) / float64(c.NAKPktsRequested)
	} else {
		c.NAKDeliveryRate = 1.0 // No NAKs needed
	}

	// NAK Fulfillment: Are retransmissions being sent for NAK requests?
	// Key Verification: RetransSent ≈ NAKPktsReceived
	if c.NAKPktsReceived > 0 {
		c.NAKFulfillmentRate = float64(c.RetransSent) / float64(c.NAKPktsReceived)
	} else {
		c.NAKFulfillmentRate = 1.0 // No NAKs to fulfill
	}

	// Range packet ratio: what proportion of NAK'd packets came from ranges?
	// High ratio indicates burst losses (immediate NAKs for gaps)
	// Low ratio indicates scattered losses (periodic NAKs for individual packets)
	if c.NAKPktsRequested > 0 {
		c.RangePktRatio = float64(c.NAKRangesSent) / float64(c.NAKPktsRequested)
	}
}

type ObservedStatistics struct {
	// === PER-CONNECTION ANALYSIS ===
	Connection1 ConnectionAnalysis // Publisher → Server
	Connection2 ConnectionAnalysis // Server → Subscriber

	// === COMBINED METRICS (backward compatibility and summary) ===
	// Network-level gap detection (≈ netem loss, triggers NAK/retransmission)
	GapRate float64 // Sequence gaps detected / packets sent (≈ netem loss)
	GapsAbs int64   // Absolute count of gaps detected

	// TRUE unrecoverable packets (TSBPD skips only)
	// Drops (too_late, already_acked, duplicate) are redundant arrivals, NOT true losses
	SkipsAbs int64   // TSBPD skips = packets that NEVER arrived (TRUE losses)
	DropsAbs int64   // All drops = redundant arrivals (NOT true losses, for info only)
	DropRate float64 // Drops / packets sent (for backward compat, but misleading)

	// Cross-endpoint validation (sender - receiver)
	CrossEndpointLossRate float64 // (PacketsSent - PacketsRecv) / PacketsSent
	CrossEndpointLossAbs  int64   // Absolute difference

	// For backward compatibility
	LossRate         float64 // Alias for GapRate
	ReportedLossRate float64 // Alias for GapRate
	ReportedLossAbs  int64   // Alias for GapsAbs

	// Cross-check: do gap detection and cross-endpoint agree?
	LossMethodsAgree bool    // True if both methods agree within tolerance
	LossDiscrepancy  float64 // Difference between methods (for debugging)

	// Recovery and retransmission statistics
	RetransRate       float64 // Retransmissions / gaps detected
	RetransPctOfSent  float64 // Retransmissions / packets sent
	NAKsPerGap        float64 // NAKs sent / gaps detected
	NAKsPerLostPacket float64 // Alias for NAKsPerGap
	RecoveryRate      float64 // (Gaps - Drops) / Gaps = % of gaps successfully recovered
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

	// ========== TERMINOLOGY (see integration_testing_design.md Section 3) ==========
	// - "netem loss": Packets dropped by network (what we configure in tc netem)
	// - "Gaps detected": Sequence gaps seen by receiver - triggers NAK/retransmission
	// - "Retransmissions": Packets re-sent to repair gaps
	// - "SRT loss": Packets that arrived too late (TSBPD expired) - actual data loss
	//
	// Key insight: GapRate can be HIGHER than netem loss because:
	// - Retransmissions can also be lost, causing cascading gaps
	// - Example: 5% netem loss → ~5% gaps → retransmit → some lost again → more gaps
	//
	// The correct metric to validate against netem loss is RetransPctOfSent (retrans/sent),
	// which directly measures the repair activity needed.

	// ========== Validate Netem Loss Correlation ==========
	// RetransPctOfSent should approximately equal the configured netem loss rate
	// This is the most reliable indicator because each dropped packet triggers a retransmission
	checksPerformed++
	if isWithinTolerance(observed.RetransPctOfSent, expected.ExpectedLossRate, expected.LossRateTolerance) {
		checksPassed++
	} else {
		lowerBound := expected.ExpectedLossRate * (1 - expected.LossRateTolerance)
		upperBound := expected.ExpectedLossRate * (1 + expected.LossRateTolerance)
		result.Violations = append(result.Violations, StatisticalViolation{
			Metric:        "RetransPctOfSent",
			ExpectedRange: fmt.Sprintf("%.1f%% - %.1f%%", lowerBound*100, upperBound*100),
			Observed:      observed.RetransPctOfSent * 100,
			Message: fmt.Sprintf(
				"Retransmission rate (%.2f%%) outside expected range for %.1f%% netem loss",
				observed.RetransPctOfSent*100, expected.ExpectedLossRate*100),
		})
	}

	// ========== Informational: Gap Detection Statistics ==========
	// GapRate can be higher than netem loss due to cascading gaps
	// This is informational, not a validation failure
	if observed.GapRate > expected.ExpectedLossRate*2.0 {
		result.Warnings = append(result.Warnings, StatisticalWarning{
			Metric: "GapRate",
			Message: fmt.Sprintf(
				"Gap detection rate (%.2f%%) is %.1fx the netem loss rate (%.1f%%) - "+
					"likely due to cascading gaps from retransmission loss",
				observed.GapRate*100, observed.GapRate/expected.ExpectedLossRate, expected.ExpectedLossRate*100),
		})
	}

	// ========== Cross-Check: Methods Agreement ==========
	// Add warning if gap detection and cross-endpoint methods disagree significantly
	if !observed.LossMethodsAgree && observed.LossDiscrepancy > 0 {
		result.Warnings = append(result.Warnings, StatisticalWarning{
			Metric: "LossMethodDiscrepancy",
			Message: fmt.Sprintf(
				"Gap detection methods disagree by %.1f%%: cross-endpoint=%.2f%% (%d pkts), gaps=%.2f%% (%d pkts)",
				observed.LossDiscrepancy*100,
				observed.CrossEndpointLossRate*100, observed.CrossEndpointLossAbs,
				observed.GapRate*100, observed.GapsAbs),
		})
	}

	// ========== Validate NAK Behavior ==========
	// NAKs should be sent when gaps are detected (only validate if expected and gaps detected)
	if expected.ExpectNAKs && observed.GapsAbs > 0 {
		checksPerformed++
		if observed.NAKsPerGap >= expected.MinNAKsPerLostPkt {
			checksPassed++
		} else {
			result.Violations = append(result.Violations, StatisticalViolation{
				Metric:        "NAKsPerGap",
				ExpectedRange: fmt.Sprintf(">= %.2f", expected.MinNAKsPerLostPkt),
				Observed:      observed.NAKsPerGap,
				Message:       "Too few NAKs - receiver may not be requesting retransmissions",
			})
		}

		// Warn on NAK storms (way too many NAKs per gap)
		if observed.NAKsPerGap > expected.MaxNAKsPerLostPkt {
			result.Warnings = append(result.Warnings, StatisticalWarning{
				Metric: "NAKsPerGap",
				Message: fmt.Sprintf(
					"High NAK rate (%.2f per gap) - possible NAK storm",
					observed.NAKsPerGap),
			})
		}
	}

	// ========== Validate Recovery Rate (THE KEY METRIC) ==========
	// This is the most important metric: what % of gaps were successfully recovered?
	// RecoveryRate = (Gaps - TSBPD_Skips) / Gaps
	// Only TSBPD skips (packets that NEVER arrived) are true losses
	// Drops (too_late, already_acked, duplicate) are redundant arrivals, not true losses
	checksPerformed++
	if observed.RecoveryRate >= expected.MinRecoveryRate {
		checksPassed++
	} else {
		result.Violations = append(result.Violations, StatisticalViolation{
			Metric:        "RecoveryRate",
			ExpectedRange: fmt.Sprintf(">= %.1f%%", expected.MinRecoveryRate*100),
			Observed:      observed.RecoveryRate * 100,
			Message: fmt.Sprintf(
				"Poor loss recovery - %d of %d gaps never recovered (TSBPD skips, true losses)",
				observed.SkipsAbs, observed.GapsAbs),
		})
	}

	// ========== PER-CONNECTION VALIDATION ==========
	// Validate each connection independently for more detailed diagnostics
	conn1Passed, conn1Checks := checkConnectionAnalysis(
		observed.Connection1, expected, &result, "Connection1")
	conn2Passed, conn2Checks := checkConnectionAnalysis(
		observed.Connection2, expected, &result, "Connection2")

	checksPerformed += conn1Checks + conn2Checks
	if conn1Passed {
		checksPassed += conn1Checks
	}
	if conn2Passed {
		checksPassed += conn2Checks
	}

	// ========== NAK DETAIL VALIDATION (RFC SRT Appendix A) ==========
	// Validate NAK request/fulfillment pipeline for each connection
	nakResult1 := ValidateNAKDetail(observed.Connection1)
	nakResult2 := ValidateNAKDetail(observed.Connection2)
	result.NAKDetailResults = append(result.NAKDetailResults, nakResult1, nakResult2)

	// Add NAK delivery issues as warnings (not failures - informational for now)
	for _, issue := range nakResult1.Issues {
		result.Warnings = append(result.Warnings, StatisticalWarning{
			Metric:  "Connection1/NAKDetail",
			Message: issue,
		})
	}
	for _, issue := range nakResult2.Issues {
		result.Warnings = append(result.Warnings, StatisticalWarning{
			Metric:  "Connection2/NAKDetail",
			Message: issue,
		})
	}

	// EXPLICIT PASS: Only pass when all checks succeed
	if checksPerformed > 0 && checksPassed == checksPerformed {
		result.Passed = true
	}

	return result
}

// checkConnectionAnalysis validates a single SRT connection and returns (passed, numChecks)
func checkConnectionAnalysis(
	conn ConnectionAnalysis,
	expected StatisticalExpectation,
	result *StatisticalValidationResult,
	connName string,
) (bool, int) {
	checksPerformed := 0
	checksPassed := 0

	// Skip validation if no packets sent on this connection
	if conn.PacketsSent == 0 {
		return true, 0 // No data = passes by default
	}

	// 1. Recovery Rate for this connection
	// RecoveryRate is now based on TSBPD skips only (true losses)
	checksPerformed++
	if conn.RecoveryRate >= expected.MinRecoveryRate {
		checksPassed++
	} else {
		result.Violations = append(result.Violations, StatisticalViolation{
			Metric:        fmt.Sprintf("%s/RecoveryRate", connName),
			ExpectedRange: fmt.Sprintf(">= %.1f%%", expected.MinRecoveryRate*100),
			Observed:      conn.RecoveryRate * 100,
			Message: fmt.Sprintf("%s (%s): Poor recovery rate %.1f%% - %d gaps, %d TSBPD skips (true losses)",
				connName, conn.Name, conn.RecoveryRate*100, conn.GapsDetected, conn.RecvSkippedTSBPD),
		})
	}

	// 2. NAK Efficiency (if gaps exist)
	if conn.GapsDetected > 0 && expected.ExpectNAKs {
		checksPerformed++
		if conn.NAKEfficiency >= expected.MinNAKsPerLostPkt {
			checksPassed++
		} else {
			result.Violations = append(result.Violations, StatisticalViolation{
				Metric:        fmt.Sprintf("%s/NAKEfficiency", connName),
				ExpectedRange: fmt.Sprintf(">= %.2f NAKs per gap", expected.MinNAKsPerLostPkt),
				Observed:      conn.NAKEfficiency,
				Message: fmt.Sprintf("%s (%s): Low NAK efficiency %.2f - %d gaps but only %d NAKs sent",
					connName, conn.Name, conn.NAKEfficiency, conn.GapsDetected, conn.NAKsSent),
			})
		}

		// Warn on NAK storms
		if conn.NAKEfficiency > expected.MaxNAKsPerLostPkt {
			result.Warnings = append(result.Warnings, StatisticalWarning{
				Metric: fmt.Sprintf("%s/NAKEfficiency", connName),
				Message: fmt.Sprintf("%s (%s): High NAK rate %.2f per gap - possible NAK storm",
					connName, conn.Name, conn.NAKEfficiency),
			})
		}
	}

	// Add informational message about this connection's statistics
	if conn.GapsDetected > 0 {
		result.Warnings = append(result.Warnings, StatisticalWarning{
			Metric: fmt.Sprintf("%s/Stats", connName),
			Message: fmt.Sprintf("%s: sent=%d, gaps=%d (%.2f%%), retrans=%d, unrecov=%d (drops=%d, skips=%d), recovery=%.1f%%",
				conn.Name, conn.PacketsSent, conn.GapsDetected, conn.GapRate*100,
				conn.RetransSent, conn.RecvUnrecoverable, conn.RecvDrops, conn.RecvSkippedTSBPD, conn.RecoveryRate*100),
		})
	}

	return checksPassed == checksPerformed, checksPerformed
}

// computeStatisticalExpectations calculates expected behavior based on impairment config.
// If imp.Thresholds is set, those values are used directly.
// Otherwise, defaults are computed based on impairment type.
func computeStatisticalExpectations(imp NetworkImpairment) StatisticalExpectation {
	// Start with default expectations
	// With bidirectional netem loss, retransmission rate is HIGHER than configured loss:
	// - Original packets can be lost (2%)
	// - NAKs can be lost (2%)
	// - Retransmissions can be lost (2%)
	// This leads to cascading gaps, so actual retrans rate ≈ 1.5-2x configured loss
	// Use ±100% tolerance (0% to 2x configured) to account for this
	exp := StatisticalExpectation{
		ExpectedLossRate:  imp.LossRate,
		LossRateTolerance: 1.0, // ±100% tolerance (accounts for bidirectional cascading)
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
	// Two INDEPENDENT SRT connections:
	//   Connection 1: Client-Generator (publisher) → Server
	//   Connection 2: Server → Client (subscriber)
	cgMetrics := ComputeDerivedMetrics(ts.ClientGenerator)
	serverMetrics := ComputeDerivedMetrics(ts.Server)
	clientMetrics := ComputeDerivedMetrics(ts.Client)

	stats := ObservedStatistics{}

	// ========== CONNECTION 1: ClientGenerator → Server ==========
	// ClientGenerator is SENDER, Server is RECEIVER
	stats.Connection1 = ConnectionAnalysis{
		Name: "publisher-to-server",
		// From ClientGenerator (sender role)
		PacketsSent:  cgMetrics.TotalPacketsSent,
		RetransSent:  cgMetrics.TotalRetransmissions,
		NAKsReceived: cgMetrics.TotalNAKsRecv,
		ACKsReceived: cgMetrics.TotalACKsRecv,
		// NAK detail - sender receives NAKs (RFC SRT Appendix A)
		NAKSinglesRecv:  cgMetrics.NAKSinglesRecv,
		NAKRangesRecv:   cgMetrics.NAKRangesRecv,
		NAKPktsReceived: cgMetrics.NAKPktsReceived,
		// From Server (receiver role for Connection 1)
		PacketsRecv:      serverMetrics.TotalPacketsRecv,
		GapsDetected:     serverMetrics.TotalGapsDetected,
		NAKsSent:         serverMetrics.TotalNAKsSent,
		ACKsSent:         serverMetrics.TotalACKsSent,
		RecvDrops:        serverMetrics.TotalPacketsDropped,
		RecvSkippedTSBPD: serverMetrics.TotalPacketsSkippedTSBPD,
		// Timer health
		PeriodicACKRuns: serverMetrics.PeriodicACKRuns,
		PeriodicNAKRuns: serverMetrics.PeriodicNAKRuns,
		// NAK detail - receiver sends NAKs (RFC SRT Appendix A)
		NAKSinglesSent:   serverMetrics.NAKSinglesSent,
		NAKRangesSent:    serverMetrics.NAKRangesSent,
		NAKPktsRequested: serverMetrics.NAKPktsRequested,
	}
	stats.Connection1.computeRates()

	// ========== CONNECTION 2: Server → Client ==========
	// Server is SENDER (relay), Client is RECEIVER
	stats.Connection2 = ConnectionAnalysis{
		Name: "server-to-subscriber",
		// From Server (sender role for Connection 2)
		PacketsSent:  serverMetrics.TotalPacketsSent,
		RetransSent:  serverMetrics.TotalRetransmissions, // Server's retransmissions to Client
		NAKsReceived: serverMetrics.TotalNAKsRecv,        // NAKs from Client (not from CG)
		ACKsReceived: serverMetrics.TotalACKsRecv,        // ACKs from Client
		// NAK detail - sender receives NAKs (RFC SRT Appendix A)
		NAKSinglesRecv:  serverMetrics.NAKSinglesRecv,
		NAKRangesRecv:   serverMetrics.NAKRangesRecv,
		NAKPktsReceived: serverMetrics.NAKPktsReceived,
		// From Client (receiver role)
		PacketsRecv:      clientMetrics.TotalPacketsRecv,
		GapsDetected:     clientMetrics.TotalGapsDetected,
		NAKsSent:         clientMetrics.TotalNAKsSent,
		ACKsSent:         clientMetrics.TotalACKsSent,
		RecvDrops:        clientMetrics.TotalPacketsDropped,
		RecvSkippedTSBPD: clientMetrics.TotalPacketsSkippedTSBPD,
		// Timer health
		PeriodicACKRuns: clientMetrics.PeriodicACKRuns,
		PeriodicNAKRuns: clientMetrics.PeriodicNAKRuns,
		// NAK detail - receiver sends NAKs (RFC SRT Appendix A)
		NAKSinglesSent:   clientMetrics.NAKSinglesSent,
		NAKRangesSent:    clientMetrics.NAKRangesSent,
		NAKPktsRequested: clientMetrics.NAKPktsRequested,
	}
	stats.Connection2.computeRates()

	// ========== COMBINED METRICS (for backward compatibility) ==========
	// Use Connection 1's sender (ClientGenerator) as the primary sender
	packetsSent := stats.Connection1.PacketsSent
	if packetsSent == 0 {
		// Fallback: estimate from bytes sent
		if cgMetrics.TotalBytesSent > 0 {
			packetsSent = cgMetrics.TotalBytesSent / 1316 // Approximate packet size
		}
	}

	if packetsSent > 0 {
		// Gap Detection: Sum of gaps across both connections
		stats.GapsAbs = stats.Connection1.GapsDetected + stats.Connection2.GapsDetected
		stats.GapRate = float64(stats.GapsAbs) / float64(packetsSent)

		// Backward compatibility aliases
		stats.ReportedLossAbs = stats.GapsAbs
		stats.ReportedLossRate = stats.GapRate
		stats.LossRate = stats.GapRate

		// True Loss Detection: TSBPD skips only (packets that NEVER arrived)
		stats.SkipsAbs = stats.Connection1.RecvSkippedTSBPD + stats.Connection2.RecvSkippedTSBPD
		// Drop Detection: Sum of drops (redundant arrivals, for info only)
		stats.DropsAbs = stats.Connection1.RecvDrops + stats.Connection2.RecvDrops
		stats.DropRate = float64(stats.DropsAbs) / float64(packetsSent)

		// Cross-Endpoint Validation: Original sender vs final receiver
		packetsReceived := clientMetrics.TotalPacketsRecv
		if packetsReceived < packetsSent {
			stats.CrossEndpointLossAbs = packetsSent - packetsReceived
			stats.CrossEndpointLossRate = float64(stats.CrossEndpointLossAbs) / float64(packetsSent)
		}

		// Cross-Check: Do methods agree?
		const crossCheckTolerance = 0.5
		if stats.CrossEndpointLossRate > 0 && stats.GapRate > 0 {
			maxRate := stats.CrossEndpointLossRate
			if stats.GapRate > maxRate {
				maxRate = stats.GapRate
			}
			minRate := stats.CrossEndpointLossRate
			if stats.GapRate < minRate {
				minRate = stats.GapRate
			}
			if maxRate > 0 {
				stats.LossDiscrepancy = (maxRate - minRate) / maxRate
				stats.LossMethodsAgree = stats.LossDiscrepancy <= crossCheckTolerance
			}
		} else {
			stats.LossMethodsAgree = true
		}

		// Combined Recovery Rate: Based on TSBPD skips only (true losses)
		// DropsAbs are redundant arrivals, NOT true losses
		if stats.GapsAbs > 0 {
			stats.RecoveryRate = 1.0 - (float64(stats.SkipsAbs) / float64(stats.GapsAbs))
			if stats.RecoveryRate < 0 {
				stats.RecoveryRate = 0
			}
		} else {
			stats.RecoveryRate = 1.0
		}
	} else {
		stats.RecoveryRate = 1.0
		stats.LossMethodsAgree = true
	}

	// Combined Retransmission and NAK Rates
	totalRetrans := stats.Connection1.RetransSent + stats.Connection2.RetransSent
	if stats.GapsAbs > 0 {
		stats.RetransRate = float64(totalRetrans) / float64(stats.GapsAbs)
		totalNAKs := stats.Connection1.NAKsSent + stats.Connection2.NAKsSent
		stats.NAKsPerGap = float64(totalNAKs) / float64(stats.GapsAbs)
		stats.NAKsPerLostPacket = stats.NAKsPerGap
	}

	// RetransPctOfSent using Connection 1's retransmissions (primary sender)
	if packetsSent > 0 {
		stats.RetransPctOfSent = float64(stats.Connection1.RetransSent) / float64(packetsSent)
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
