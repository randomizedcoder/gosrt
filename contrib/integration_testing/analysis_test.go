package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// Tests for parsePrometheusMetrics
// =============================================================================

func TestParsePrometheusMetrics_BasicCounter(t *testing.T) {
	raw := `gosrt_connection_packets_received_total{socket_id="0x12345678",type="data"} 100`

	metrics := parsePrometheusMetrics(raw)

	require.Len(t, metrics, 1)
	require.Contains(t, metrics, `gosrt_connection_packets_received_total{socket_id="0x12345678",type="data"}`)
	require.Equal(t, float64(100), metrics[`gosrt_connection_packets_received_total{socket_id="0x12345678",type="data"}`])
}

func TestParsePrometheusMetrics_MultipleLabels(t *testing.T) {
	raw := `gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"} 50`

	metrics := parsePrometheusMetrics(raw)

	key := `gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"}`
	require.Contains(t, metrics, key, "Should parse metric with multiple labels")
	require.Equal(t, float64(50), metrics[key])
}

func TestParsePrometheusMetrics_SkipsComments(t *testing.T) {
	raw := `# HELP gosrt_connection_packets_received_total
# TYPE gosrt_connection_packets_received_total counter
gosrt_connection_packets_received_total{socket_id="0x12345678"} 100`

	metrics := parsePrometheusMetrics(raw)

	require.Len(t, metrics, 1, "Should skip comment lines")
	require.Contains(t, metrics, `gosrt_connection_packets_received_total{socket_id="0x12345678"}`)
}

func TestParsePrometheusMetrics_MultipleConnections(t *testing.T) {
	raw := `gosrt_connection_packets_received_total{socket_id="0xAAAAAAAA",type="data"} 100
gosrt_connection_packets_received_total{socket_id="0xBBBBBBBB",type="data"} 200`

	metrics := parsePrometheusMetrics(raw)

	require.Len(t, metrics, 2, "Should parse metrics from multiple connections")
	require.Equal(t, float64(100), metrics[`gosrt_connection_packets_received_total{socket_id="0xAAAAAAAA",type="data"}`])
	require.Equal(t, float64(200), metrics[`gosrt_connection_packets_received_total{socket_id="0xBBBBBBBB",type="data"}`])
}

func TestParsePrometheusMetrics_NAKDetailMetrics(t *testing.T) {
	// This is the exact format the handler produces
	raw := `gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="single"} 5
gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"} 10
gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"} 50
gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="single"} 4
gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="range"} 8
gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="recv"} 40`

	metrics := parsePrometheusMetrics(raw)

	require.Len(t, metrics, 6, "Should parse all NAK detail metrics")

	// Verify each metric is parsed correctly
	require.Equal(t, float64(5), metrics[`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="single"}`])
	require.Equal(t, float64(10), metrics[`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"}`])
	require.Equal(t, float64(50), metrics[`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"}`])
	require.Equal(t, float64(4), metrics[`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="single"}`])
	require.Equal(t, float64(8), metrics[`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="range"}`])
	require.Equal(t, float64(40), metrics[`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="recv"}`])
}

// =============================================================================
// Tests for getSumByPrefix
// =============================================================================

func TestGetSumByPrefix_SingleMatch(t *testing.T) {
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			`gosrt_connection_packets_received_total{socket_id="0x12345678"}`: 100,
		},
	}

	sum := getSumByPrefix(snapshot, "gosrt_connection_packets_received_total")
	require.Equal(t, float64(100), sum)
}

func TestGetSumByPrefix_MultipleMatches(t *testing.T) {
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			`gosrt_connection_packets_received_total{socket_id="0xAAAAAAAA"}`: 100,
			`gosrt_connection_packets_received_total{socket_id="0xBBBBBBBB"}`: 200,
		},
	}

	sum := getSumByPrefix(snapshot, "gosrt_connection_packets_received_total")
	require.Equal(t, float64(300), sum, "Should sum metrics from all connections")
}

func TestGetSumByPrefix_NoMatch(t *testing.T) {
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			`gosrt_connection_packets_received_total{socket_id="0x12345678"}`: 100,
		},
	}

	sum := getSumByPrefix(snapshot, "gosrt_nonexistent_metric")
	require.Equal(t, float64(0), sum, "Should return 0 when no matches")
}

// =============================================================================
// Tests for getSumByPrefixContaining
// =============================================================================

func TestGetSumByPrefixContaining_ExactMatch(t *testing.T) {
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"}`: 50,
		},
	}

	sum := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_packets_requested_total", `direction="sent"`)
	require.Equal(t, float64(50), sum)
}

func TestGetSumByPrefixContaining_FiltersByDirection(t *testing.T) {
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"}`: 50,
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="recv"}`: 40,
		},
	}

	sentSum := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_packets_requested_total", `direction="sent"`)
	require.Equal(t, float64(50), sentSum, "Should only sum 'sent' direction")

	recvSum := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_packets_requested_total", `direction="recv"`)
	require.Equal(t, float64(40), recvSum, "Should only sum 'recv' direction")
}

func TestGetSumByPrefixContaining_NAKEntriesWithTypeFilter(t *testing.T) {
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="single"}`: 5,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"}`:  10,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="single"}`: 4,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="range"}`:  8,
		},
	}

	// Filter by direction="sent",type="single"
	sentSingle := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="single"`)
	require.Equal(t, float64(5), sentSingle, "Should find sent single entries")

	// Filter by direction="sent",type="range"
	sentRange := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="range"`)
	require.Equal(t, float64(10), sentRange, "Should find sent range entries")

	// Filter by direction="recv",type="single"
	recvSingle := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="recv",type="single"`)
	require.Equal(t, float64(4), recvSingle, "Should find recv single entries")

	// Filter by direction="recv",type="range"
	recvRange := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="recv",type="range"`)
	require.Equal(t, float64(8), recvRange, "Should find recv range entries")
}

func TestGetSumByPrefixContaining_MultipleConnections(t *testing.T) {
	// Server has two connections - should sum metrics from both
	snapshot := &MetricsSnapshot{
		Metrics: map[string]float64{
			// Connection 1: Server as receiver (from CG)
			`gosrt_connection_nak_packets_requested_total{socket_id="0xAAAAAAAA",direction="sent"}`: 50,
			// Connection 2: Server as sender (to Client)
			`gosrt_connection_nak_packets_requested_total{socket_id="0xBBBBBBBB",direction="sent"}`: 0,
		},
	}

	sum := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_packets_requested_total", `direction="sent"`)
	require.Equal(t, float64(50), sum, "Should sum from both connections (0 + 50 = 50)")
}

// =============================================================================
// Tests for ComputeDerivedMetrics (specifically NAK detail counters)
// =============================================================================

func TestComputeDerivedMetrics_NAKDetailCounters(t *testing.T) {
	// NAK counters now track PACKETS, not entries:
	//   NAKSingle + NAKRange = NAKPktsTotal = expected retransmissions
	first := &MetricsSnapshot{
		Timestamp: time.Now(),
		Metrics: map[string]float64{
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="single"}`:  0,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"}`:   0,
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"}`:      0,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="single"}`:  0,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="range"}`:   0,
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="recv"}`:      0,
			`gosrt_connection_congestion_packets_lost_total{socket_id="0x12345678",direction="recv"}`:    0,
			`gosrt_connection_congestion_retransmissions_total{socket_id="0x12345678",direction="send"}`: 0,
		},
	}

	// Values now represent PACKETS:
	// - NAKSinglesSent=5: 5 packets via single NAK entries
	// - NAKRangesSent=45: 45 packets via range NAK entries
	// - NAKPktsRequested=50: total = 5 + 45
	last := &MetricsSnapshot{
		Timestamp: time.Now().Add(10 * time.Second),
		Metrics: map[string]float64{
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="single"}`:  5,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"}`:   45,
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"}`:      50,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="single"}`:  4,
			`gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="recv",type="range"}`:   36,
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="recv"}`:      40,
			`gosrt_connection_congestion_packets_lost_total{socket_id="0x12345678",direction="recv"}`:    100,
			`gosrt_connection_congestion_retransmissions_total{socket_id="0x12345678",direction="send"}`: 80,
		},
	}

	ts := MetricsTimeSeries{
		Snapshots: []*MetricsSnapshot{first, last},
	}

	dm := ComputeDerivedMetrics(ts)

	// Verify NAK detail counters (now representing packets)
	require.Equal(t, int64(5), dm.NAKSinglesSent, "NAKSinglesSent should be 5 packets")
	require.Equal(t, int64(45), dm.NAKRangesSent, "NAKRangesSent should be 45 packets")
	require.Equal(t, int64(50), dm.NAKPktsRequested, "NAKPktsRequested should be 50 packets")

	// Verify invariant: NAKSingle + NAKRange = NAKPktsTotal
	require.Equal(t, dm.NAKSinglesSent+dm.NAKRangesSent, dm.NAKPktsRequested,
		"NAKSinglesSent + NAKRangesSent should equal NAKPktsRequested")

	require.Equal(t, int64(4), dm.NAKSinglesRecv, "NAKSinglesRecv should be 4 packets")
	require.Equal(t, int64(36), dm.NAKRangesRecv, "NAKRangesRecv should be 36 packets")
	require.Equal(t, int64(40), dm.NAKPktsReceived, "NAKPktsReceived should be 40 packets")

	// Verify invariant for receiver side too
	require.Equal(t, dm.NAKSinglesRecv+dm.NAKRangesRecv, dm.NAKPktsReceived,
		"NAKSinglesRecv + NAKRangesRecv should equal NAKPktsReceived")

	// Also verify basic counters still work
	require.Equal(t, int64(100), dm.TotalGapsDetected, "TotalGapsDetected should be 100")
	require.Equal(t, int64(80), dm.TotalRetransmissions, "TotalRetransmissions should be 80")
}

// =============================================================================
// End-to-end test: Parse + Search
// =============================================================================

func TestEndToEnd_ParseAndSearchNAKMetrics(t *testing.T) {
	// Simulate raw Prometheus output from a server
	raw := `# HELP gosrt_connection_nak_entries_total NAK entries by type
# TYPE gosrt_connection_nak_entries_total counter
gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="single"} 5
gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"} 10
gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"} 50
# Server also has recv-side metrics (from receiving NAKs on its sender connection)
gosrt_connection_nak_entries_total{socket_id="0x87654321",direction="recv",type="single"} 4
gosrt_connection_nak_entries_total{socket_id="0x87654321",direction="recv",type="range"} 8
gosrt_connection_nak_packets_requested_total{socket_id="0x87654321",direction="recv"} 40`

	// Parse
	metrics := parsePrometheusMetrics(raw)
	require.Len(t, metrics, 6, "Should parse 6 metrics (excluding comments)")

	// Create snapshot
	snapshot := &MetricsSnapshot{
		Metrics: metrics,
	}

	// Search for sent NAK packets requested
	sentPkts := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_packets_requested_total", `direction="sent"`)
	require.Equal(t, float64(50), sentPkts, "Should find sent NAK packets requested")

	// Search for recv NAK packets requested
	recvPkts := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_packets_requested_total", `direction="recv"`)
	require.Equal(t, float64(40), recvPkts, "Should find recv NAK packets requested")

	// Search for sent range entries
	sentRanges := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="range"`)
	require.Equal(t, float64(10), sentRanges, "Should find sent range NAK entries")

	// Search for sent single entries
	sentSingles := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="single"`)
	require.Equal(t, float64(5), sentSingles, "Should find sent single NAK entries")
}

// =============================================================================
// Regression tests for known issues
// =============================================================================

func TestRegression_NAKMetricsNotFoundDueToLabelOrder(t *testing.T) {
	// This test ensures we handle different label orderings correctly
	// The handler writes: socket_id first, then direction, then type
	raw := `gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"} 10`

	metrics := parsePrometheusMetrics(raw)
	snapshot := &MetricsSnapshot{Metrics: metrics}

	// The search pattern MUST match the exact substring in the metric name
	// Search for direction="sent",type="range" (comma-separated, in order)
	sum := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="range"`)
	require.Equal(t, float64(10), sum, "Should find metric with correct label substring")

	// Wrong order should NOT match (type before direction)
	wrongOrder := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `type="range",direction="sent"`)
	require.Equal(t, float64(0), wrongOrder, "Wrong label order should NOT match")
}

func TestRegression_ZeroValueMetricsNotWritten(t *testing.T) {
	// writeCounterIfNonZero skips zero values, so they won't appear in output
	// The search should handle this gracefully (return 0, not error)
	raw := `gosrt_connection_nak_entries_total{socket_id="0x12345678",direction="sent",type="range"} 10`
	// Note: no "single" entry because it was 0

	metrics := parsePrometheusMetrics(raw)
	snapshot := &MetricsSnapshot{Metrics: metrics}

	// Searching for a zero-valued metric should return 0
	singles := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="single"`)
	require.Equal(t, float64(0), singles, "Missing metric should return 0")

	// But the existing metric should still be found
	ranges := getSumByPrefixContaining(snapshot, "gosrt_connection_nak_entries_total", `direction="sent",type="range"`)
	require.Equal(t, float64(10), ranges, "Existing metric should be found")
}

// =============================================================================
// Tests for duplicate packet tracking (over-NAKing confirmation)
// =============================================================================

func TestComputeDerivedMetrics_DuplicatePackets(t *testing.T) {
	// Duplicate packets confirm over-NAKing: when range NAKs request packets
	// that weren't actually lost, they arrive as duplicates
	first := &MetricsSnapshot{
		Timestamp: time.Now(),
		Metrics: map[string]float64{
			`gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="duplicate"}`: 0,
			`gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="too_late"}`:  0,
		},
	}

	last := &MetricsSnapshot{
		Timestamp: time.Now().Add(10 * time.Second),
		Metrics: map[string]float64{
			// 25 duplicate packets arrived (already in buffer)
			`gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="duplicate"}`: 25,
			`gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="too_late"}`:  3,
		},
	}

	ts := MetricsTimeSeries{
		Snapshots: []*MetricsSnapshot{first, last},
	}

	dm := ComputeDerivedMetrics(ts)

	require.Equal(t, int64(25), dm.TotalDuplicates, "TotalDuplicates should be 25")
}

func TestComputeDerivedMetrics_DuplicatesConfirmOverNAKing(t *testing.T) {
	// Test scenario: 2% netem loss causes ~8% gap detection due to over-NAKing
	// Duplicates should ≈ (NAKPktsRequested - GapsDetected)
	first := &MetricsSnapshot{
		Timestamp: time.Now(),
		Metrics:   map[string]float64{},
	}

	last := &MetricsSnapshot{
		Timestamp: time.Now().Add(10 * time.Second),
		Metrics: map[string]float64{
			// 100 gaps detected (sequence holes)
			`gosrt_connection_congestion_packets_lost_total{socket_id="0x12345678",direction="recv"}`: 100,
			// 200 packets requested via NAKs (over-requesting)
			`gosrt_connection_nak_packets_requested_total{socket_id="0x12345678",direction="sent"}`: 200,
			// ~100 duplicates (200 requested - 100 actually lost)
			`gosrt_connection_congestion_recv_data_drop_total{socket_id="0x12345678",reason="duplicate"}`: 95,
		},
	}

	ts := MetricsTimeSeries{
		Snapshots: []*MetricsSnapshot{first, last},
	}

	dm := ComputeDerivedMetrics(ts)

	require.Equal(t, int64(100), dm.TotalGapsDetected, "TotalGapsDetected should be 100")
	require.Equal(t, int64(200), dm.NAKPktsRequested, "NAKPktsRequested should be 200")
	require.Equal(t, int64(95), dm.TotalDuplicates, "TotalDuplicates should be 95")

	// Over-NAK amount = NAKPktsRequested - GapsDetected = 200 - 100 = 100
	// Duplicates (95) ≈ Over-NAK amount (100) confirms the hypothesis
	overNAKAmount := dm.NAKPktsRequested - dm.TotalGapsDetected
	require.Equal(t, int64(100), overNAKAmount, "Over-NAK amount should be 100")

	// Duplicates should be close to over-NAK amount (within 10%)
	ratio := float64(dm.TotalDuplicates) / float64(overNAKAmount)
	require.InDelta(t, 1.0, ratio, 0.15, "Duplicates should be ~95-105% of over-NAK amount")
}

// =============================================================================
// Tests for ConnectionAnalysis.computeRates()
// =============================================================================

func TestConnectionAnalysis_RangePktRatio(t *testing.T) {
	// Test the RangePktRatio calculation
	conn := ConnectionAnalysis{
		Name:             "test-connection",
		NAKSinglesSent:   10,  // 10 packets via single NAK entries
		NAKRangesSent:    90,  // 90 packets via range NAK entries
		NAKPktsRequested: 100, // Total = 10 + 90
		NAKPktsReceived:  95,  // 95% delivery
		RetransSent:      93,  // 93 retransmissions sent
		GapsDetected:     50,  // Only 50 actual gaps
	}

	conn.computeRates()

	// RangePktRatio = NAKRangesSent / NAKPktsRequested = 90/100 = 0.90
	require.InDelta(t, 0.90, conn.RangePktRatio, 0.001, "RangePktRatio should be 90%")

	// NAK Delivery = NAKPktsReceived / NAKPktsRequested = 95/100 = 0.95
	require.InDelta(t, 0.95, conn.NAKDeliveryRate, 0.001, "NAKDeliveryRate should be 95%")

	// NAK Fulfillment = RetransSent / NAKPktsReceived = 93/95 ≈ 0.979
	require.InDelta(t, 0.979, conn.NAKFulfillmentRate, 0.01, "NAKFulfillmentRate should be ~98%")
}

func TestConnectionAnalysis_RangePktRatioZeroNAKs(t *testing.T) {
	// Edge case: no NAKs sent (clean network)
	conn := ConnectionAnalysis{
		Name:             "clean-network",
		NAKSinglesSent:   0,
		NAKRangesSent:    0,
		NAKPktsRequested: 0,
	}

	conn.computeRates()

	// Should not panic, RangePktRatio should be 0
	require.Equal(t, float64(0), conn.RangePktRatio, "RangePktRatio should be 0 when no NAKs")
	require.Equal(t, float64(1.0), conn.NAKDeliveryRate, "NAKDeliveryRate should be 1.0 when no NAKs")
	require.Equal(t, float64(1.0), conn.NAKFulfillmentRate, "NAKFulfillmentRate should be 1.0 when no NAKs")
}

// ============================================================================
// Phase 12: ACK Rate Validation Tests (ACK Optimization)
// ============================================================================

// TestValidateACKRates_EventLoop_Default tests ACK validation in EventLoop mode
// with default LightACKDifference=64
func TestValidateACKRates_EventLoop_Default(t *testing.T) {
	// Scenario: 10 second test, 90000 packets, LightACKDifference=64
	// Expected Light ACKs: 90000 / 64 = 1406
	dm := DerivedMetrics{
		ACKLiteSent: 1400, // Within ±20% of 1406
		ACKFullSent: 10,   // Some Full ACKs from massive jumps
	}

	result := ValidateACKRates(dm, 10.0, 64, 90000, true) // EventLoop mode

	require.True(t, result.Passed, "ACK rates should be valid: LightACKError=%s, FullACKError=%s",
		result.LightACKError, result.FullACKError)
	require.True(t, result.IsEventLoopMode, "Should be EventLoop mode")
	require.Equal(t, int64(1406), result.ExpectedLightACKs, "Expected Light ACKs")
}

// TestValidateACKRates_EventLoop_TooFewLightACKs tests detection of insufficient Light ACKs
func TestValidateACKRates_EventLoop_TooFewLightACKs(t *testing.T) {
	// Scenario: 10 second test, 90000 packets, LightACKDifference=64
	// Expected Light ACKs: 90000 / 64 = 1406
	// But only 100 sent - way too few
	dm := DerivedMetrics{
		ACKLiteSent: 100,
		ACKFullSent: 10,
	}

	result := ValidateACKRates(dm, 10.0, 64, 90000, true) // EventLoop mode

	require.False(t, result.Passed, "Should fail with too few Light ACKs")
	require.Contains(t, result.LightACKError, "expected", "Should have Light ACK error")
}

// TestValidateACKRates_Tick_Default tests ACK validation in Tick mode
// with expected Full ACKs at 10ms interval
func TestValidateACKRates_Tick_Default(t *testing.T) {
	// Scenario: 10 second test, Full ACKs at 10ms = 100/sec
	// Expected Full ACKs: 10 * 100 = 1000
	dm := DerivedMetrics{
		ACKLiteSent: 0,    // No Light ACKs in Tick mode
		ACKFullSent: 1000, // Within expected range
	}

	result := ValidateACKRates(dm, 10.0, 64, 90000, false) // Tick mode

	require.True(t, result.Passed, "ACK rates should be valid: FullACKError=%s", result.FullACKError)
	require.False(t, result.IsEventLoopMode, "Should be Tick mode")
	require.Equal(t, int64(1000), result.ExpectedFullACKs, "Expected Full ACKs")
}

// TestValidateACKRates_Tick_TooFewFullACKs tests detection of insufficient Full ACKs
func TestValidateACKRates_Tick_TooFewFullACKs(t *testing.T) {
	// Scenario: 10 second test, expected ~1000 Full ACKs
	// But only 500 sent - too few
	dm := DerivedMetrics{
		ACKLiteSent: 0,
		ACKFullSent: 500,
	}

	result := ValidateACKRates(dm, 10.0, 64, 90000, false) // Tick mode

	require.False(t, result.Passed, "Should fail with too few Full ACKs")
	require.Contains(t, result.FullACKError, "expected", "Should have Full ACK error")
}

// TestValidateACKRates_HighBitrate tests with high bitrate and custom LightACKDifference
func TestValidateACKRates_HighBitrate(t *testing.T) {
	// Scenario: 10 second test at 200Mbps, LightACKDifference=256 (high bitrate optimization)
	// ~1400 byte packets = ~180000 packets
	// Expected Light ACKs: 180000 / 256 = 703
	dm := DerivedMetrics{
		ACKLiteSent: 700,
		ACKFullSent: 5,
	}

	result := ValidateACKRates(dm, 10.0, 256, 180000, true) // EventLoop mode

	require.True(t, result.Passed, "ACK rates should be valid for high bitrate")
	require.Equal(t, int64(256), result.LightACKDifference, "Should use configured LightACKDifference")
}
