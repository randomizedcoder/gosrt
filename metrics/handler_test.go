package metrics

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestConnectionMetrics creates a ConnectionMetrics for testing with LockTiming initialized
func newTestConnectionMetrics() *ConnectionMetrics {
	return &ConnectionMetrics{
		HandlePacketLockTiming: &LockTimingMetrics{},
		ReceiverLockTiming:     &LockTimingMetrics{},
		SenderLockTiming:       &LockTimingMetrics{},
	}
}

// newTestConnectionInfo creates a ConnectionInfo for testing with the given metrics and instance name
func newTestConnectionInfo(m *ConnectionMetrics, instanceName string) *ConnectionInfo {
	return &ConnectionInfo{
		Metrics:      m,
		InstanceName: instanceName,
		RemoteAddr:   "127.0.0.1:1234",
		StreamId:     "test-stream",
		PeerType:     "unknown",
		PeerSocketID: 0x87654321,
		StartTime:    time.Now(),
	}
}

// TestPrometheusOutputFormat verifies the Prometheus output is valid exposition format
func TestPrometheusOutputFormat(t *testing.T) {
	// Create a connection with known socket ID
	socketId := uint32(0x12345678)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set some values
	m.PktRecvDataSuccess.Store(100)
	m.PktSentDataSuccess.Store(200)
	m.ByteRecvDataSuccess.Store(1000)

	// Get Prometheus output
	output := getPrometheusOutput(t)

	// Verify basic format requirements
	require.Contains(t, output, "gosrt_connection_packets_received_total")
	require.Contains(t, output, "gosrt_connection_packets_sent_total")
	require.Contains(t, output, "gosrt_connection_bytes_received_total")

	// Verify socket ID label format
	require.Contains(t, output, `socket_id="0x12345678"`)

	// Verify metric value format: name{labels} value OR name value (for runtime metrics without labels)
	// Example: gosrt_connection_packets_received_total{socket_id="0x12345678",type="data",status="success"} 100
	// Example: go_goroutines 5.000000000
	metricWithLabelsRegex := regexp.MustCompile(`^[a-z_]+\{[^}]+\} \d+(\.\d+)?$`)
	metricNoLabelsRegex := regexp.MustCompile(`^[a-z_]+ \d+(\.\d+)?$`)

	lines := strings.Split(output, "\n")
	metricLineCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}
		metricLineCount++
		validFormat := metricWithLabelsRegex.MatchString(line) || metricNoLabelsRegex.MatchString(line)
		require.True(t, validFormat,
			"Line does not match Prometheus format: %s", line)
	}

	require.Greater(t, metricLineCount, 0, "Should have at least one metric line")
}

// TestPrometheusCounterAccuracy verifies Prometheus values match internal counters
func TestPrometheusCounterAccuracy(t *testing.T) {
	socketId := uint32(0xABCD1234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set specific values
	m.PktRecvDataSuccess.Store(12345)
	m.PktSentDataSuccess.Store(67890)
	m.ByteRecvDataSuccess.Store(1234567890)
	m.PktRecvNAKSuccess.Store(42)
	m.PktSentNAKSuccess.Store(37)
	m.PktRetransFromNAK.Store(99)

	output := getPrometheusOutput(t)

	// Verify exact values appear in output (with instance label)
	require.Contains(t, output, `gosrt_connection_packets_received_total{socket_id="0xabcd1234",instance="default",type="data",status="success"} 12345`)
	require.Contains(t, output, `gosrt_connection_packets_sent_total{socket_id="0xabcd1234",instance="default",type="data",status="success"} 67890`)
	require.Contains(t, output, `gosrt_connection_bytes_received_total{socket_id="0xabcd1234",instance="default",type="data",status="success"} 1234567890`)
	require.Contains(t, output, `gosrt_connection_packets_received_total{socket_id="0xabcd1234",instance="default",type="nak",status="success"} 42`)
	require.Contains(t, output, `gosrt_connection_packets_sent_total{socket_id="0xabcd1234",instance="default",type="nak",status="success"} 37`)
	require.Contains(t, output, `gosrt_connection_retransmissions_from_nak_total{socket_id="0xabcd1234",instance="default"} 99`)
}

// TestPrometheusACKLiteFullCounters verifies Light and Full ACK metrics are correctly exported
// Phase 12: ACK Optimization - validates new ack_lite/ack_full type labels
func TestPrometheusACKLiteFullCounters(t *testing.T) {
	socketId := uint32(0xACAC1234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set Light/Full ACK counters (Phase 5: ACK Optimization)
	m.PktSentACKLiteSuccess.Store(1234)
	m.PktSentACKFullSuccess.Store(567)
	m.PktRecvACKLiteSuccess.Store(890)
	m.PktRecvACKFullSuccess.Store(111)

	output := getPrometheusOutput(t)

	// Verify Light ACK metrics with type="ack_lite" label
	require.Contains(t, output,
		`gosrt_connection_packets_sent_total{socket_id="0xacac1234",instance="default",type="ack_lite",status="success"} 1234`,
		"Light ACKs sent should be exported with type=ack_lite")
	require.Contains(t, output,
		`gosrt_connection_packets_received_total{socket_id="0xacac1234",instance="default",type="ack_lite",status="success"} 890`,
		"Light ACKs received should be exported with type=ack_lite")

	// Verify Full ACK metrics with type="ack_full" label
	require.Contains(t, output,
		`gosrt_connection_packets_sent_total{socket_id="0xacac1234",instance="default",type="ack_full",status="success"} 567`,
		"Full ACKs sent should be exported with type=ack_full")
	require.Contains(t, output,
		`gosrt_connection_packets_received_total{socket_id="0xacac1234",instance="default",type="ack_full",status="success"} 111`,
		"Full ACKs received should be exported with type=ack_full")
}

// TestPrometheusExportsAllCounters uses reflection to verify every atomic counter
// in ConnectionMetrics is exported to Prometheus output.
// This catches any metrics that are added to the struct but forgotten in handler.go.
func TestPrometheusExportsAllCounters(t *testing.T) {
	socketId := uint32(0xDEADBEEF)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Use reflection to find all atomic.Uint64 and atomic.Int64 fields
	// and set each to a unique value
	val := reflect.ValueOf(m).Elem()
	typ := val.Type()

	// Map field name -> unique value we set
	fieldValues := make(map[string]uint64)

	// Skip fields that are not metrics (LockTimingMetrics, HeaderSize, etc.)
	skipFields := map[string]bool{
		"HandlePacketLockTiming": true,
		"ReceiverLockTiming":     true,
		"SenderLockTiming":       true,
		"HeaderSize":             true,
	}

	// Fields that are TRULY not exported to Prometheus
	// ONLY includes fields that are:
	// 1. Commented out in metrics.go (never incremented, not implemented)
	// 2. Calculated rate fields (not cumulative counters)
	//
	// NOTE: All other fields ARE actively used and SHOULD be exported!
	// See: packet_classifier.go, connection.go, congestion/live/*.go for increment locations
	intentionallyNotExported := map[string]bool{
		// ========== Commented out in metrics.go (not implemented) ==========
		// Control packet dropped/error counters - control packets currently never fail
		"PktRecvACKDropped":       true,
		"PktRecvACKError":         true,
		"PktSentACKDropped":       true,
		"PktSentACKError":         true,
		"PktRecvACKACKDropped":    true,
		"PktRecvACKACKError":      true,
		"PktSentACKACKDropped":    true,
		"PktSentACKACKError":      true,
		"PktRecvNAKDropped":       true,
		"PktRecvNAKError":         true,
		"PktSentNAKDropped":       true,
		"PktSentNAKError":         true,
		"PktRecvKMDropped":        true,
		"PktRecvKMError":          true,
		"PktSentKMDropped":        true,
		"PktSentKMError":          true,
		"PktRecvKeepaliveDropped": true,
		"PktRecvKeepaliveError":   true,
		"PktSentKeepaliveDropped": true,
		"PktSentKeepaliveError":   true,
		"PktRecvShutdownDropped":  true,
		"PktRecvShutdownError":    true,
		"PktSentShutdownDropped":  true,
		"PktSentShutdownError":    true,
		"PktRecvHandshakeDropped": true,
		"PktRecvHandshakeError":   true,
		"PktSentHandshakeDropped": true,
		"PktSentHandshakeError":   true,
		// Byte drop counters - commented out, drops tracked via CongestionSendPktDrop
		"ByteRecvDataDropped": true,
		"ByteSentDataDropped": true,
		// Delivery failed counters - commented out, callbacks don't fail
		"CongestionRecvDeliveryFailed": true,
		"CongestionSendDeliveryFailed": true,
		// Loss rate counters - commented out, renamed to retrans rate
		"CongestionRecvPktLossRate": true,
		"CongestionSendPktLossRate": true,

		// ========== Rate metrics (stored as percentage * 100, not cumulative counters) ==========
		// These are calculated rates updated periodically, not monotonically increasing
		"CongestionRecvPktRetransRate": true,
		"CongestionSendPktRetransRate": true,

		// ========== Phase 1: Rate Metrics (float64 stored as uint64 bits) ==========
		// These are gauge values exported via getter helpers (tested in TestRateMetricsExported)
		"RecvRatePacketsPerSec":  true, // Exported as gauge via GetRecvRatePacketsPerSec()
		"RecvRateBytesPerSec":    true, // Exported as gauge via GetRecvRateBytesPerSec()
		"RecvRatePktRetransRate": true, // Exported as gauge via GetRecvRateRetransPercent()
		"SendRateEstInputBW":     true, // Exported as gauge via GetSendRateEstInputBW()
		"SendRateEstSentBW":      true, // Exported as gauge via GetSendRateEstSentBW()
		"SendRatePktRetransRate": true, // Exported as gauge via GetSendRateRetransPercent()
		// Note: Raw rate counters are now exported as gosrt_*_rate_*_raw metrics
	}

	uniqueValue := uint64(1000000) // Start with a large unique base
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldVal := val.Field(i)

		if skipFields[field.Name] {
			continue
		}

		// Handle atomic.Uint64
		if field.Type == reflect.TypeOf(atomic.Uint64{}) {
			uniqueValue++
			ptr := fieldVal.Addr().Interface().(*atomic.Uint64)
			ptr.Store(uniqueValue)
			fieldValues[field.Name] = uniqueValue
		}

		// Handle atomic.Int64
		if field.Type == reflect.TypeOf(atomic.Int64{}) {
			uniqueValue++
			ptr := fieldVal.Addr().Interface().(*atomic.Int64)
			ptr.Store(int64(uniqueValue))
			fieldValues[field.Name] = uniqueValue
		}
	}

	// Get Prometheus output
	output := getPrometheusOutput(t)

	// Verify each unique value appears in the output
	missingFields := []string{}
	skippedFields := []string{}
	for fieldName, expectedValue := range fieldValues {
		// Skip intentionally not exported fields
		if intentionallyNotExported[fieldName] {
			skippedFields = append(skippedFields, fieldName)
			continue
		}

		valueStr := strconv.FormatUint(expectedValue, 10)
		if !strings.Contains(output, valueStr) {
			missingFields = append(missingFields, fmt.Sprintf("%s (expected value %d)", fieldName, expectedValue))
		}
	}

	if len(missingFields) > 0 {
		t.Errorf("The following ConnectionMetrics fields are NOT exported to Prometheus (but should be):\n  %s",
			strings.Join(missingFields, "\n  "))
	}

	// Log summary
	exportedCount := len(fieldValues) - len(skippedFields) - len(missingFields)
	t.Logf("Summary: %d fields exported, %d intentionally skipped, %d missing",
		exportedCount, len(skippedFields), len(missingFields))
}

// TestPrometheusLabels verifies all required labels are present
func TestPrometheusLabels(t *testing.T) {
	socketId := uint32(0x99887766)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set values for different packet types
	m.PktRecvDataSuccess.Store(1)
	m.PktRecvACKSuccess.Store(1)
	m.PktRecvNAKSuccess.Store(1)
	m.PktSentDataSuccess.Store(1)
	m.CongestionRecvPkt.Store(1)
	m.CongestionSendPkt.Store(1)
	m.ByteRecvDataSuccess.Store(1) // Needed for bytes_received_total check

	output := getPrometheusOutput(t)

	// Verify label presence for different metric types
	labelChecks := []struct {
		metric string
		labels []string
	}{
		{`gosrt_connection_packets_received_total`, []string{`socket_id=`, `type=`, `status=`}},
		{`gosrt_connection_packets_sent_total`, []string{`socket_id=`, `type=`, `status=`}},
		{`gosrt_connection_congestion_packets_total`, []string{`socket_id=`, `direction=`}},
		{`gosrt_connection_bytes_received_total`, []string{`socket_id=`, `type=`}},
	}

	for _, check := range labelChecks {
		// Find a line with this metric
		found := false
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, check.metric) {
				found = true
				for _, label := range check.labels {
					require.Contains(t, line, label,
						"Metric %s should have label %s", check.metric, label)
				}
				break
			}
		}
		if !found {
			t.Logf("Warning: metric %s not found in output", check.metric)
		}
	}
}

// TestPrometheusMultipleConnections verifies metrics are correctly separated per connection
func TestPrometheusMultipleConnections(t *testing.T) {
	socketId1 := uint32(0x11111111)
	socketId2 := uint32(0x22222222)

	m1 := newTestConnectionMetrics()
	m2 := newTestConnectionMetrics()

	RegisterConnection(socketId1, newTestConnectionInfo(m1, ""))
	RegisterConnection(socketId2, newTestConnectionInfo(m2, ""))
	defer UnregisterConnection(socketId1, CloseReasonGraceful)
	defer UnregisterConnection(socketId2, CloseReasonGraceful)

	// Set different values for each connection
	m1.PktRecvDataSuccess.Store(1111)
	m2.PktRecvDataSuccess.Store(2222)

	output := getPrometheusOutput(t)

	// Verify both connections appear with correct values (with instance label)
	require.Contains(t, output, `socket_id="0x11111111"`)
	require.Contains(t, output, `socket_id="0x22222222"`)
	require.Contains(t, output, `"0x11111111",instance="default",type="data",status="success"} 1111`)
	require.Contains(t, output, `"0x22222222",instance="default",type="data",status="success"} 2222`)
}

// ========== Phase 1: Rate Metrics Tests ==========

// TestRateMetricsExported verifies rate metrics are correctly exported to Prometheus
func TestRateMetricsExported(t *testing.T) {
	socketId := uint32(0xBA7E1234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, "test-rate"))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set known rate values (stored as float64 bits)
	// Receiver rates
	m.RecvRatePacketsPerSec.Store(math.Float64bits(1000.5))  // 1000.5 pkt/s
	m.RecvRateBytesPerSec.Store(math.Float64bits(1250000.0)) // 1.25 MB/s (~10 Mbps)
	m.RecvRatePktRetransRate.Store(math.Float64bits(2.5))    // 2.5% retrans

	// Sender rates
	m.SendRateEstInputBW.Store(math.Float64bits(1500000.0)) // 1.5 MB/s input
	m.SendRateEstSentBW.Store(math.Float64bits(1450000.0))  // 1.45 MB/s sent
	m.SendRatePktRetransRate.Store(math.Float64bits(1.8))   // 1.8% retrans

	output := getPrometheusOutput(t)

	// Verify receiver rate metrics present
	require.Contains(t, output, "gosrt_recv_rate_packets_per_sec")
	require.Contains(t, output, "gosrt_recv_rate_bytes_per_sec")
	require.Contains(t, output, "gosrt_recv_rate_retrans_percent")

	// Verify sender rate metrics present
	require.Contains(t, output, "gosrt_send_rate_input_bandwidth_bps")
	require.Contains(t, output, "gosrt_send_rate_sent_bandwidth_bps")
	require.Contains(t, output, "gosrt_send_rate_retrans_percent")

	// Verify socket_id label (hex lowercase)
	require.Contains(t, output, `socket_id="0xba7e1234"`)
}

// TestRateMetricsAccuracy verifies rate metric values are correctly encoded/decoded
func TestRateMetricsAccuracy(t *testing.T) {
	socketId := uint32(0xACC01234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set precise rate values
	expectedPPS := 8642.75
	expectedBPS := 12500000.0 // 12.5 MB/s = 100 Mbps

	m.RecvRatePacketsPerSec.Store(math.Float64bits(expectedPPS))
	m.RecvRateBytesPerSec.Store(math.Float64bits(expectedBPS))

	output := getPrometheusOutput(t)

	// Parse the output to verify values
	// Rate metrics are floats, so look for the value in scientific or decimal notation
	// gosrt_recv_rate_packets_per_sec{socket_id="0xaccu1234"} 8642.75
	require.Contains(t, output, "8642.75", "Expected packets per sec value")
	require.Contains(t, output, "12500000", "Expected bytes per sec value")
}

// TestRateMetricsZeroValues verifies zero rates are exported correctly
func TestRateMetricsZeroValues(t *testing.T) {
	socketId := uint32(0xEE001234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Rate fields default to 0 (math.Float64bits(0) = 0)
	// Verify zero values are still exported (not omitted)
	output := getPrometheusOutput(t)

	// Should see the metric with socket_id (value will be 0)
	// We use writeGauge not writeGaugeIfNonZero for rate metrics
	require.Contains(t, output, "gosrt_recv_rate_packets_per_sec")
	require.Contains(t, output, "gosrt_recv_rate_bytes_per_sec")
	require.Contains(t, output, `socket_id="0xee001234"`)
}

// TestGetterHelpers verifies the float64 getter helpers work correctly
func TestGetterHelpers(t *testing.T) {
	m := newTestConnectionMetrics()

	// Set values using raw atomic
	m.RecvRatePacketsPerSec.Store(math.Float64bits(1234.5))
	m.RecvRateBytesPerSec.Store(math.Float64bits(1310720.0)) // 1.25 MB/s

	// Verify getters decode correctly
	require.InDelta(t, 1234.5, m.GetRecvRatePacketsPerSec(), 0.001)
	require.InDelta(t, 1310720.0, m.GetRecvRateBytesPerSec(), 0.001)

	// Verify Mbps conversion (1310720 bytes/s * 8 / 1024 / 1024 = 10 Mbps)
	require.InDelta(t, 10.0, m.GetRecvRateMbps(), 0.001)

	// Test sender getters
	m.SendRateEstSentBW.Store(math.Float64bits(2621440.0)) // 2.5 MB/s
	require.InDelta(t, 2621440.0, m.GetSendRateEstSentBW(), 0.001)
	require.InDelta(t, 20.0, m.GetSendRateMbps(), 0.001) // 20 Mbps
}

// TestPrometheusRuntimeMetrics verifies Go runtime metrics are included
func TestPrometheusRuntimeMetrics(t *testing.T) {
	output := getPrometheusOutput(t)

	// These are standard Go runtime metrics (using go_memstats_* naming)
	runtimeMetrics := []string{
		"go_goroutines",
		"go_memstats_alloc_bytes",
		"go_memstats_heap_alloc_bytes",
		"go_memstats_gc_duration_seconds",
		"go_cpu_count",
	}

	for _, metric := range runtimeMetrics {
		require.Contains(t, output, metric, "Should include Go runtime metric: %s", metric)
	}
}

// TestProcessStartTimeMetric verifies process start time is exported
func TestProcessStartTimeMetric(t *testing.T) {
	output := getPrometheusOutput(t)

	// Process start time should be present
	require.Contains(t, output, "gosrt_process_start_time_seconds",
		"Should export process start time metric")

	// Value should be a Unix timestamp (recent)
	// Extract the value and verify it's reasonable
	startTime := GetProgramStartTime()
	expectedValue := float64(startTime.Unix())

	// The metric should contain a value close to the expected timestamp
	// (within a reasonable range since tests might run for a while)
	require.Contains(t, output, fmt.Sprintf("%.0f", expectedValue),
		"Process start time should be the Unix timestamp of program start")

	// Verify the timestamp is reasonable (within the last hour)
	now := time.Now().Unix()
	require.Less(t, int64(expectedValue), now+1,
		"Process start time should not be in the future")
	require.Greater(t, int64(expectedValue), now-3600,
		"Process start time should be within the last hour")
}

// TestConnectionStartTimeMetric verifies connection start time is exported
func TestConnectionStartTimeMetric(t *testing.T) {
	socketId := uint32(0xABCDEF01)
	m := newTestConnectionMetrics()

	// Create connection info with a specific start time
	connStartTime := time.Now().Add(-30 * time.Second) // Started 30 seconds ago
	info := &ConnectionInfo{
		Metrics:      m,
		InstanceName: "test-instance",
		RemoteAddr:   "192.168.1.100:5000",
		StreamId:     "publish:/test-stream",
		PeerType:     "publisher",
		PeerSocketID: 0x12345678,
		StartTime:    connStartTime,
	}
	RegisterConnection(socketId, info)
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	output := getPrometheusOutput(t)

	// Connection start time metric should be present
	require.Contains(t, output, "gosrt_connection_start_time_seconds",
		"Should export connection start time metric")

	// Should contain the socket ID
	require.Contains(t, output, `socket_id="0xabcdef01"`,
		"Connection start time should include socket_id label")

	// Should contain the instance name
	require.Contains(t, output, `instance="test-instance"`,
		"Connection start time should include instance label")

	// Should contain the remote address
	require.Contains(t, output, `remote_addr="192.168.1.100:5000"`,
		"Connection start time should include remote_addr label")

	// Should contain the stream ID
	require.Contains(t, output, `stream_id="publish:/test-stream"`,
		"Connection start time should include stream_id label")

	// Should contain the peer type
	require.Contains(t, output, `peer_type="publisher"`,
		"Connection start time should include peer_type label")

	// The timestamp value should be approximately connStartTime.Unix()
	expectedTimestamp := connStartTime.Unix()
	require.Contains(t, output, fmt.Sprintf("%.0f", float64(expectedTimestamp)),
		"Connection start time value should match the Unix timestamp")
}

// TestPrometheusZeroFiltering verifies that zero values are not exported
func TestPrometheusZeroFiltering(t *testing.T) {
	socketId := uint32(0x77777777)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set some counters to non-zero, leave others at zero
	m.PktRecvDataSuccess.Store(12345) // Non-zero - should appear
	m.PktSentDataSuccess.Store(67890) // Non-zero - should appear
	// PktRecvDataDropped stays at 0 - should NOT appear

	output := getPrometheusOutput(t)

	// Non-zero values should appear
	require.Contains(t, output, "12345", "Non-zero PktRecvDataSuccess should be exported")
	require.Contains(t, output, "67890", "Non-zero PktSentDataSuccess should be exported")

	// Zero values should NOT appear (they're filtered out)
	// Count occurrences of socket_id - if zero filtering works, we should have fewer lines
	socketIdCount := strings.Count(output, `socket_id="0x77777777"`)
	t.Logf("Found %d metrics for socket 0x77777777 (zero values filtered)", socketIdCount)

	// Should have much fewer than the ~65 possible metrics because most are zero
	// Note: 6 rate metrics are always exported (even when zero) via writeGauge()
	// plus lock timing metrics, so allow up to 30
	require.Less(t, socketIdCount, 30, "Should have fewer metrics when most are zero (zero filtering)")
}

// TestPrometheusCongestionMetrics verifies congestion control metrics are exported
func TestPrometheusCongestionMetrics(t *testing.T) {
	socketId := uint32(0xCCCCCCCC)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set congestion control values
	m.CongestionRecvPkt.Store(5000)
	m.CongestionRecvPktUnique.Store(4900)
	m.CongestionRecvPktLoss.Store(100)
	m.CongestionRecvPktRetrans.Store(50)
	m.CongestionSendPkt.Store(6000)
	m.CongestionSendPktUnique.Store(5800)
	m.CongestionSendPktRetrans.Store(200)

	output := getPrometheusOutput(t)

	// Verify congestion metrics are present (with instance label)
	require.Contains(t, output, `gosrt_connection_congestion_packets_total{socket_id="0xcccccccc",instance="default",direction="recv"} 5000`)
	require.Contains(t, output, `gosrt_connection_congestion_packets_unique_total{socket_id="0xcccccccc",instance="default",direction="recv"} 4900`)
	require.Contains(t, output, `gosrt_connection_congestion_packets_lost_total{socket_id="0xcccccccc",instance="default",direction="recv"} 100`)
	require.Contains(t, output, `gosrt_connection_congestion_retransmissions_total{socket_id="0xcccccccc",instance="default",direction="recv"} 50`)
	require.Contains(t, output, `gosrt_connection_congestion_packets_total{socket_id="0xcccccccc",instance="default",direction="send"} 6000`)
	require.Contains(t, output, `gosrt_connection_congestion_packets_unique_total{socket_id="0xcccccccc",instance="default",direction="send"} 5800`)
	require.Contains(t, output, `gosrt_connection_congestion_retransmissions_total{socket_id="0xcccccccc",instance="default",direction="send"} 200`)
}

// TestPrometheusNAKDetailMetrics verifies NAK detail counters are exported
// with the correct metric names and labels (RFC SRT Appendix A)
func TestPrometheusNAKDetailMetrics(t *testing.T) {
	socketId := uint32(0xdddddddd)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set NAK detail values (receiver side - sends NAKs)
	m.CongestionRecvNAKSingle.Store(5)
	m.CongestionRecvNAKRange.Store(10)
	m.CongestionRecvNAKPktsTotal.Store(50)

	// Set NAK detail values (sender side - receives NAKs)
	m.CongestionSendNAKSingleRecv.Store(4)
	m.CongestionSendNAKRangeRecv.Store(8)
	m.CongestionSendNAKPktsRecv.Store(40)

	output := getPrometheusOutput(t)

	// Verify receiver-side NAK detail metrics (direction="sent" because receiver SENDS NAKs)
	// With instance label
	require.Contains(t, output, `gosrt_connection_nak_entries_total{socket_id="0xdddddddd",instance="default",direction="sent",type="single"} 5`,
		"NAK single entries (sent by receiver)")
	require.Contains(t, output, `gosrt_connection_nak_entries_total{socket_id="0xdddddddd",instance="default",direction="sent",type="range"} 10`,
		"NAK range entries (sent by receiver)")
	require.Contains(t, output, `gosrt_connection_nak_packets_requested_total{socket_id="0xdddddddd",instance="default",direction="sent"} 50`,
		"NAK packets requested (sent by receiver)")

	// Verify sender-side NAK detail metrics (direction="recv" because sender RECEIVES NAKs)
	require.Contains(t, output, `gosrt_connection_nak_entries_total{socket_id="0xdddddddd",instance="default",direction="recv",type="single"} 4`,
		"NAK single entries (received by sender)")
	require.Contains(t, output, `gosrt_connection_nak_entries_total{socket_id="0xdddddddd",instance="default",direction="recv",type="range"} 8`,
		"NAK range entries (received by sender)")
	require.Contains(t, output, `gosrt_connection_nak_packets_requested_total{socket_id="0xdddddddd",instance="default",direction="recv"} 40`,
		"NAK packets requested (received by sender)")
}

// TestPrometheusRingBufferMetrics verifies ring buffer metrics are exported
// including the computed backlog gauge (Phase 4)
func TestPrometheusRingBufferMetrics(t *testing.T) {
	socketId := uint32(0xEEEEEEEE)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set ring buffer metrics
	m.RingDropsTotal.Store(5)
	m.RingDrainedPackets.Store(10000)
	m.RingPacketsProcessed.Store(9500)
	m.RecvRatePackets.Store(10000) // Total received (for backlog calculation)

	output := getPrometheusOutput(t)

	// Verify ring counter metrics are present
	require.Contains(t, output, `gosrt_ring_drops_total{socket_id="0xeeeeeeee",instance="default"} 5`)
	require.Contains(t, output, `gosrt_ring_drained_packets_total{socket_id="0xeeeeeeee",instance="default"} 10000`)
	require.Contains(t, output, `gosrt_ring_packets_processed_total{socket_id="0xeeeeeeee",instance="default"} 9500`)

	// Verify computed backlog gauge (10000 received - 9500 processed = 500 backlog)
	require.Contains(t, output, `gosrt_ring_backlog_packets{socket_id="0xeeeeeeee",instance="default"} 500`)
}

// TestPrometheusEventLoopMetrics verifies EventLoop metrics are correctly exported
// Phase 4: ACK/ACKACK Redesign - validates EventLoop diagnostic metrics
func TestPrometheusEventLoopMetrics(t *testing.T) {
	socketId := uint32(0xE0E01234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set EventLoop metrics
	m.EventLoopIterations.Store(100000)
	m.EventLoopFullACKFires.Store(100)      // 100 Full ACK fires in 1 second
	m.EventLoopNAKFires.Store(50)           // 50 NAK fires in 1 second
	m.EventLoopRateFires.Store(10)          // 10 rate fires in 10 seconds
	m.EventLoopDefaultRuns.Store(99800)     // Most iterations are default
	m.EventLoopIdleBackoffs.Store(500)      // 500 idle backoffs
	m.EventLoopControlProcessed.Store(3000) // 3000 control packets (ACKACK, KEEPALIVE) processed inline

	output := getPrometheusOutput(t)

	// Verify EventLoop metrics are present
	require.Contains(t, output,
		`gosrt_eventloop_iterations_total{socket_id="0xe0e01234",instance="default"} 100000`,
		"EventLoop iterations should be exported")
	require.Contains(t, output,
		`gosrt_eventloop_fullack_fires_total{socket_id="0xe0e01234",instance="default"} 100`,
		"EventLoop fullACK fires should be exported")
	require.Contains(t, output,
		`gosrt_eventloop_nak_fires_total{socket_id="0xe0e01234",instance="default"} 50`,
		"EventLoop NAK fires should be exported")
	require.Contains(t, output,
		`gosrt_eventloop_rate_fires_total{socket_id="0xe0e01234",instance="default"} 10`,
		"EventLoop rate fires should be exported")
	require.Contains(t, output,
		`gosrt_eventloop_default_runs_total{socket_id="0xe0e01234",instance="default"} 99800`,
		"EventLoop default runs should be exported")
	require.Contains(t, output,
		`gosrt_eventloop_idle_backoffs_total{socket_id="0xe0e01234",instance="default"} 500`,
		"EventLoop idle backoffs should be exported")
	require.Contains(t, output,
		`gosrt_eventloop_control_processed_total{socket_id="0xe0e01234",instance="default"} 3000`,
		"EventLoop control processed should be exported")
}

// TestPrometheusAckBtreeMetrics verifies ACK btree metrics are correctly exported
// Phase 4: ACK/ACKACK Redesign - validates ACK btree diagnostic metrics
func TestPrometheusAckBtreeMetrics(t *testing.T) {
	socketId := uint32(0xACB71234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set ACK btree metrics
	m.AckBtreeSize.Store(5)            // 5 pending Full ACKs awaiting ACKACK
	m.AckBtreeEntriesExpired.Store(10) // 10 entries expired
	m.AckBtreeUnknownACKACK.Store(2)   // 2 unknown ACKACKs

	output := getPrometheusOutput(t)

	// Verify ACK btree metrics are present
	require.Contains(t, output,
		`gosrt_ack_btree_size{socket_id="0xacb71234",instance="default"} 5`,
		"ACK btree size should be exported")
	require.Contains(t, output,
		`gosrt_ack_btree_expired_total{socket_id="0xacb71234",instance="default"} 10`,
		"ACK btree expired entries should be exported")
	require.Contains(t, output,
		`gosrt_ack_btree_unknown_ackack_total{socket_id="0xacb71234",instance="default"} 2`,
		"ACK btree unknown ACKACK should be exported")
}

// TestPrometheusRTTMetrics verifies RTT metrics are correctly exported
// Phase 4: ACK/ACKACK Redesign - validates RTT gauge metrics
func TestPrometheusRTTMetrics(t *testing.T) {
	socketId := uint32(0xA7712345)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set RTT metrics (microseconds)
	m.RTTMicroseconds.Store(150)   // 150 microseconds = 0.15ms RTT
	m.RTTVarMicroseconds.Store(25) // 25 microseconds variance

	output := getPrometheusOutput(t)

	// Verify RTT metrics are present
	require.Contains(t, output,
		`gosrt_rtt_microseconds{socket_id="0xa7712345",instance="default"} 150`,
		"RTT microseconds should be exported")
	require.Contains(t, output,
		`gosrt_rtt_var_microseconds{socket_id="0xa7712345",instance="default"} 25`,
		"RTT variance microseconds should be exported")
}

// TestPrometheusSuppressionMetrics verifies RTO-based suppression metrics are correctly exported
// Phase 6: RTO Suppression - validates sender and receiver suppression metrics
func TestPrometheusSuppressionMetrics(t *testing.T) {
	socketId := uint32(0xABC61234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set sender-side suppression metrics
	m.RetransSuppressed.Store(100) // Retransmits blocked by suppression
	m.RetransAllowed.Store(50)     // Retransmits that passed threshold
	m.RetransFirstTime.Store(40)   // First-time retransmits

	// Set receiver-side NAK suppression metrics
	m.NakSuppressedSeqs.Store(75) // NAK entries blocked by suppression
	m.NakAllowedSeqs.Store(25)    // NAK entries that passed threshold

	output := getPrometheusOutput(t)

	// Verify sender suppression metrics are present
	require.Contains(t, output,
		`gosrt_retrans_suppressed_total{socket_id="0xabc61234",instance="default"} 100`,
		"Retransmit suppressed should be exported")
	require.Contains(t, output,
		`gosrt_retrans_allowed_total{socket_id="0xabc61234",instance="default"} 50`,
		"Retransmit allowed should be exported")
	require.Contains(t, output,
		`gosrt_retrans_first_time_total{socket_id="0xabc61234",instance="default"} 40`,
		"Retransmit first-time should be exported")

	// Verify receiver NAK suppression metrics are present
	require.Contains(t, output,
		`gosrt_nak_suppressed_seqs_total{socket_id="0xabc61234",instance="default"} 75`,
		"NAK suppressed seqs should be exported")
	require.Contains(t, output,
		`gosrt_nak_allowed_seqs_total{socket_id="0xabc61234",instance="default"} 25`,
		"NAK allowed seqs should be exported")
}

// TestPrometheusIoUringSubmissionMetrics verifies io_uring submission metrics are correctly exported
// Phase 5 Refactoring: Now uses unified per-ring metrics (even for single-ring mode)
// See multi_iouring_design.md Section 5.12 for design details
func TestPrometheusIoUringSubmissionMetrics(t *testing.T) {
	socketId := uint32(0xABCD1234)
	m := newTestConnectionMetrics()

	// Initialize per-ring metrics (unified approach - always create array)
	m.IoUringSendRingMetrics = NewIoUringRingMetrics(1)
	m.IoUringSendRingCount = 1
	m.IoUringDialerRecvRingMetrics = NewIoUringRingMetrics(1)
	m.IoUringDialerRecvRingCount = 1

	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set submission metrics on per-ring counters
	m.IoUringSendRingMetrics[0].SubmitSuccess.Store(5000)
	m.IoUringDialerRecvRingMetrics[0].SubmitSuccess.Store(5000)

	// Set retry counters (occasional retries OK)
	m.IoUringSendRingMetrics[0].GetSQERetries.Store(3)
	m.IoUringSendRingMetrics[0].SubmitRetries.Store(1)

	output := getPrometheusOutput(t)

	// Verify submission success metrics are present (now with ring label)
	require.Contains(t, output,
		`gosrt_iouring_send_submit_success_total{socket_id="0xabcd1234",instance="default",ring="0"} 5000`,
		"Send submit success should be exported with ring label")
	require.Contains(t, output,
		`gosrt_iouring_dialer_recv_submit_success_total{socket_id="0xabcd1234",instance="default",ring="0"} 5000`,
		"Dialer recv submit success should be exported with ring label")

	// Verify retry metrics are present (now with ring label)
	require.Contains(t, output,
		`gosrt_iouring_send_getsqe_retries_total{socket_id="0xabcd1234",instance="default",ring="0"} 3`,
		"GetSQE retries should be exported with ring label")
	require.Contains(t, output,
		`gosrt_iouring_send_submit_retries_total{socket_id="0xabcd1234",instance="default",ring="0"} 1`,
		"Submit retries should be exported with ring label")

	// Verify ring count gauges are present
	require.Contains(t, output, `gosrt_iouring_send_ring_count{socket_id="0xabcd1234",instance="default"} 1`,
		"Send ring count gauge should be exported")
	require.Contains(t, output, `gosrt_iouring_dialer_recv_ring_count{socket_id="0xabcd1234",instance="default"} 1`,
		"Dialer recv ring count gauge should be exported")
}

// TestPrometheusIoUringCompletionMetrics verifies io_uring completion metrics are correctly exported
// Phase 5 Refactoring: Now uses unified per-ring metrics (even for single-ring mode)
// See multi_iouring_design.md Section 5.12 for design details
func TestPrometheusIoUringCompletionMetrics(t *testing.T) {
	socketId := uint32(0xEFEF5678)
	m := newTestConnectionMetrics()

	// Initialize per-ring metrics (unified approach - always create array)
	m.IoUringSendRingMetrics = NewIoUringRingMetrics(1)
	m.IoUringSendRingCount = 1
	m.IoUringDialerRecvRingMetrics = NewIoUringRingMetrics(1)
	m.IoUringDialerRecvRingCount = 1

	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set completion metrics - success and healthy timeout (on per-ring counters)
	m.IoUringSendRingMetrics[0].CompletionSuccess.Store(5000)
	m.IoUringSendRingMetrics[0].CompletionTimeout.Store(3000)   // Expected - healthy
	m.IoUringSendRingMetrics[0].CompletionEBADF.Store(1)        // Once at shutdown
	m.IoUringSendRingMetrics[0].CompletionCtxCancelled.Store(1) // Once at shutdown
	m.IoUringDialerRecvRingMetrics[0].CompletionSuccess.Store(5000)
	m.IoUringDialerRecvRingMetrics[0].CompletionTimeout.Store(1500)
	m.IoUringDialerRecvRingMetrics[0].CompletionEBADF.Store(1)

	output := getPrometheusOutput(t)

	// Verify completion success metrics are present (now with ring label)
	require.Contains(t, output,
		`gosrt_iouring_send_completion_success_total{socket_id="0xefef5678",instance="default",ring="0"} 5000`,
		"Send completion success should be exported with ring label")
	require.Contains(t, output,
		`gosrt_iouring_dialer_recv_completion_success_total{socket_id="0xefef5678",instance="default",ring="0"} 5000`,
		"Dialer recv completion success should be exported with ring label")

	// Verify timeout metrics (healthy behavior, now with ring label)
	require.Contains(t, output,
		`gosrt_iouring_send_completion_timeout_total{socket_id="0xefef5678",instance="default",ring="0"} 3000`,
		"Send completion timeout should be exported with ring label")

	// Verify shutdown metrics (now with ring label)
	require.Contains(t, output,
		`gosrt_iouring_send_completion_ebadf_total{socket_id="0xefef5678",instance="default",ring="0"} 1`,
		"Send completion EBADF should be exported with ring label")
	require.Contains(t, output,
		`gosrt_iouring_send_completion_ctx_canceled_total{socket_id="0xefef5678",instance="default",ring="0"} 1`,
		"Send completion ctx canceled should be exported with ring label")

	// Verify ring count gauges are present
	require.Contains(t, output, `gosrt_iouring_send_ring_count{socket_id="0xefef5678",instance="default"} 1`,
		"Send ring count gauge should be exported")
}

// TestPrometheusTSBPDAdvancementMetrics verifies TSBPD advancement metrics are correctly exported
// Phase: ContiguousPoint TSBPD-Based Advancement Design
func TestPrometheusTSBPDAdvancementMetrics(t *testing.T) {
	socketId := uint32(0xBEEF1234)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set TSBPD advancement metrics
	m.CongestionRecvPktSkippedTSBPD.Store(150)        // 150 packets skipped
	m.CongestionRecvByteSkippedTSBPD.Store(197400)    // 150 * 1316 bytes
	m.ContiguousPointTSBPDAdvancements.Store(3)       // 3 advancement events
	m.ContiguousPointTSBPDSkippedPktsTotal.Store(150) // Same as pkt skipped

	output := getPrometheusOutput(t)

	// Verify TSBPD skip counters are exported
	require.Contains(t, output,
		`gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total{socket_id="0xbeef1234",instance="default"} 150`,
		"TSBPD skipped packets should be exported")
	require.Contains(t, output,
		`gosrt_connection_congestion_recv_byte_skipped_tsbpd_total{socket_id="0xbeef1234",instance="default"} 197400`,
		"TSBPD skipped bytes should be exported")

	// Verify contiguousPoint advancement counters are exported
	require.Contains(t, output,
		`gosrt_connection_contiguous_point_tsbpd_advancements_total{socket_id="0xbeef1234",instance="default"} 3`,
		"ContiguousPoint TSBPD advancements should be exported")
	require.Contains(t, output,
		`gosrt_connection_contiguous_point_tsbpd_skipped_pkts_total{socket_id="0xbeef1234",instance="default"} 150`,
		"ContiguousPoint TSBPD skipped packets total should be exported")
}

// TestPrometheusTSBPDAdvancementMetricsZero verifies metrics don't appear when zero
func TestPrometheusTSBPDAdvancementMetricsZero(t *testing.T) {
	socketId := uint32(0xBEEF0000)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// All TSBPD metrics are zero (default)

	output := getPrometheusOutput(t)

	// Verify TSBPD metrics don't appear when zero (writeCounterIfNonZero behavior)
	require.NotContains(t, output, "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total",
		"TSBPD skipped packets should not appear when zero")
	require.NotContains(t, output, "gosrt_connection_contiguous_point_tsbpd_advancements_total",
		"ContiguousPoint TSBPD advancements should not appear when zero")
}

// TestPrometheusRingBacklogZero verifies backlog is zero when caught up
func TestPrometheusRingBacklogZero(t *testing.T) {
	socketId := uint32(0xFFFFFFFF)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// EventLoop caught up - processed == received
	m.RecvRatePackets.Store(5000)
	m.RingPacketsProcessed.Store(5000)

	output := getPrometheusOutput(t)

	// Backlog should be 0
	require.Contains(t, output, `gosrt_ring_backlog_packets{socket_id="0xffffffff",instance="default"} 0`)
}

// Helper function to get Prometheus output as string
func getPrometheusOutput(t *testing.T) string {
	t.Helper()

	handler := MetricsHandler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/plain")

	return rec.Body.String()
}

// TestPrometheusOutputSize measures and logs the size of Prometheus output
func TestPrometheusOutputSize(t *testing.T) {
	// Test with 0, 1, and 10 connections
	scenarios := []int{0, 1, 5, 10}

	for _, numConn := range scenarios {
		// Setup connections
		for i := 0; i < numConn; i++ {
			socketId := uint32(0x50000000 + i)
			m := newTestConnectionMetrics()
			RegisterConnection(socketId, newTestConnectionInfo(m, ""))
			defer UnregisterConnection(socketId, CloseReasonGraceful)

			// Set realistic values
			m.PktRecvDataSuccess.Store(uint64(100000 * (i + 1)))
			m.ByteRecvDataSuccess.Store(uint64(140000000 * (i + 1)))
			m.PktSentACKSuccess.Store(uint64(50000 * (i + 1)))
			m.CongestionRecvPkt.Store(uint64(100000 * (i + 1)))
		}

		output := getPrometheusOutput(t)
		outputSize := len(output)
		lineCount := len(strings.Split(output, "\n"))

		t.Logf("%d connections: %d bytes, %d lines (%.1f KB)",
			numConn, outputSize, lineCount, float64(outputSize)/1024)

		// Cleanup for next iteration
		for i := 0; i < numConn; i++ {
			UnregisterConnection(uint32(0x50000000+i), CloseReasonGraceful)
		}
	}
}

// TestPrometheusRecvControlRingMetrics verifies receiver control ring metrics are exported
// Completely Lock-Free Receiver: validates ACKACK/KEEPALIVE ring metrics
func TestPrometheusRecvControlRingMetrics(t *testing.T) {
	socketId := uint32(0xAC012345)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set receiver control ring metrics - simulate typical operation
	m.RecvControlRingPushedACKACK.Store(1000)      // 1000 ACKACKs pushed
	m.RecvControlRingPushedKEEPALIVE.Store(100)    // 100 KEEPALIVEs pushed
	m.RecvControlRingDroppedACKACK.Store(2)        // 2 ACKACKs dropped (ring full fallback)
	m.RecvControlRingDroppedKEEPALIVE.Store(0)     // 0 KEEPALIVEs dropped
	m.RecvControlRingDrained.Store(5000)           // 5000 drain operations
	m.RecvControlRingProcessed.Store(1098)         // 1098 total processed
	m.RecvControlRingProcessedACKACK.Store(998)    // 998 ACKACKs processed
	m.RecvControlRingProcessedKEEPALIVE.Store(100) // 100 KEEPALIVEs processed

	output := getPrometheusOutput(t)

	// Verify push metrics
	require.Contains(t, output,
		`gosrt_recv_control_ring_pushed_ackack_total{socket_id="0xac012345",instance="default"} 1000`,
		"RecvControlRingPushedACKACK should be exported")
	require.Contains(t, output,
		`gosrt_recv_control_ring_pushed_keepalive_total{socket_id="0xac012345",instance="default"} 100`,
		"RecvControlRingPushedKEEPALIVE should be exported")

	// Verify drop metrics (fallback path)
	require.Contains(t, output,
		`gosrt_recv_control_ring_dropped_ackack_total{socket_id="0xac012345",instance="default"} 2`,
		"RecvControlRingDroppedACKACK should be exported")
	// Note: KEEPALIVEs dropped = 0, should not appear due to zero filtering

	// Verify drain/processed metrics
	require.Contains(t, output,
		`gosrt_recv_control_ring_drained_total{socket_id="0xac012345",instance="default"} 5000`,
		"RecvControlRingDrained should be exported")
	require.Contains(t, output,
		`gosrt_recv_control_ring_processed_total{socket_id="0xac012345",instance="default"} 1098`,
		"RecvControlRingProcessed should be exported")
	require.Contains(t, output,
		`gosrt_recv_control_ring_processed_ackack_total{socket_id="0xac012345",instance="default"} 998`,
		"RecvControlRingProcessedACKACK should be exported")
	require.Contains(t, output,
		`gosrt_recv_control_ring_processed_keepalive_total{socket_id="0xac012345",instance="default"} 100`,
		"RecvControlRingProcessedKEEPALIVE should be exported")

	// Verify invariant: pushed == processed + dropped
	pushedACKACK := m.RecvControlRingPushedACKACK.Load()
	processedACKACK := m.RecvControlRingProcessedACKACK.Load()
	droppedACKACK := m.RecvControlRingDroppedACKACK.Load()
	require.Equal(t, pushedACKACK, processedACKACK+droppedACKACK,
		"Invariant: pushed ACKACK should equal processed + dropped")
}

// TestPrometheusSenderTickBaselineMetrics verifies sender Tick baseline metrics are exported
// These metrics enable burst detection comparison (Packets/Iteration ratio)
func TestPrometheusSenderTickBaselineMetrics(t *testing.T) {
	socketId := uint32(0xB0851001)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set baseline Tick metrics - typical values for burst detection
	m.SendTickRuns.Store(900)                // ~900 ticks in a 90s test (100ms interval)
	m.SendTickDeliveredPackets.Store(135000) // 135k packets delivered

	output := getPrometheusOutput(t)

	// Verify baseline Tick metrics are present
	require.Contains(t, output,
		`gosrt_send_tick_runs_total{socket_id="0xb0851001",instance="default"} 900`,
		"Send Tick runs should be exported")
	require.Contains(t, output,
		`gosrt_send_tick_delivered_packets_total{socket_id="0xb0851001",instance="default"} 135000`,
		"Send Tick delivered packets should be exported")

	// Verify burst ratio calculation would work:
	// Packets/Iteration = 135000 / 900 = 150 (bursty - typical for Tick mode)
	tickRuns := m.SendTickRuns.Load()
	deliveredPackets := m.SendTickDeliveredPackets.Load()
	packetsPerIteration := float64(deliveredPackets) / float64(tickRuns)
	require.InDelta(t, 150.0, packetsPerIteration, 0.1,
		"Packets/Iteration ratio should be ~150 for baseline Tick mode")
}

// ==================== Benchmarks ====================

// BenchmarkPrometheusHandlerNoConnections benchmarks handler with only runtime metrics
func BenchmarkPrometheusHandlerNoConnections(b *testing.B) {
	handler := MetricsHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkPrometheusHandlerSingleConnection benchmarks handler with one active connection
func BenchmarkPrometheusHandlerSingleConnection(b *testing.B) {
	socketId := uint32(0x12345678)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, ""))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set realistic counter values
	m.PktRecvDataSuccess.Store(100000)
	m.PktSentDataSuccess.Store(100000)
	m.ByteRecvDataSuccess.Store(140000000) // ~140MB
	m.ByteSentDataSuccess.Store(140000000)
	m.PktRecvACKSuccess.Store(50000)
	m.PktSentACKSuccess.Store(50000)
	m.PktRecvACKACKSuccess.Store(50000)
	m.PktSentACKACKSuccess.Store(50000)
	m.CongestionRecvPkt.Store(100000)
	m.CongestionSendPkt.Store(100000)

	handler := MetricsHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkPrometheusHandler10Connections benchmarks handler with 10 connections
func BenchmarkPrometheusHandler10Connections(b *testing.B) {
	connections := make([]*ConnectionMetrics, 10)
	for i := 0; i < 10; i++ {
		socketId := uint32(0x10000000 + i)
		m := newTestConnectionMetrics()
		connections[i] = m
		RegisterConnection(socketId, newTestConnectionInfo(m, ""))
		defer UnregisterConnection(socketId, CloseReasonGraceful)

		// Set realistic values
		m.PktRecvDataSuccess.Store(uint64(10000 * (i + 1)))
		m.PktSentDataSuccess.Store(uint64(10000 * (i + 1)))
		m.ByteRecvDataSuccess.Store(uint64(14000000 * (i + 1)))
		m.CongestionRecvPkt.Store(uint64(10000 * (i + 1)))
	}

	handler := MetricsHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkPrometheusHandler100Connections benchmarks handler with 100 connections (stress test)
func BenchmarkPrometheusHandler100Connections(b *testing.B) {
	connections := make([]*ConnectionMetrics, 100)
	for i := 0; i < 100; i++ {
		socketId := uint32(0x20000000 + i)
		m := newTestConnectionMetrics()
		connections[i] = m
		RegisterConnection(socketId, newTestConnectionInfo(m, ""))
		defer UnregisterConnection(socketId, CloseReasonGraceful)

		m.PktRecvDataSuccess.Store(uint64(1000 * (i + 1)))
		m.ByteRecvDataSuccess.Store(uint64(1400000 * (i + 1)))
	}

	handler := MetricsHandler()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkPrometheusOutputSize measures the size of Prometheus output
func BenchmarkPrometheusOutputSize(b *testing.B) {
	// Setup connections to measure realistic output size
	scenarios := []struct {
		name        string
		connections int
	}{
		{"0_connections", 0},
		{"1_connection", 1},
		{"10_connections", 10},
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			// Setup connections
			for i := 0; i < sc.connections; i++ {
				socketId := uint32(0x40000000 + i)
				m := newTestConnectionMetrics()
				RegisterConnection(socketId, newTestConnectionInfo(m, ""))
				defer UnregisterConnection(socketId, CloseReasonGraceful)

				m.PktRecvDataSuccess.Store(uint64(100000 * (i + 1)))
				m.ByteRecvDataSuccess.Store(uint64(140000000 * (i + 1)))
			}

			handler := MetricsHandler()

			// Measure output size
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			outputSize := rec.Body.Len()
			b.ReportMetric(float64(outputSize), "bytes/response")

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
				benchRec := httptest.NewRecorder()
				handler.ServeHTTP(benchRec, benchReq)
			}
		})
	}
}

// BenchmarkPrometheusHandlerParallel benchmarks handler under concurrent load
func BenchmarkPrometheusHandlerParallel(b *testing.B) {
	// Setup 5 connections
	for i := 0; i < 5; i++ {
		socketId := uint32(0x30000000 + i)
		m := newTestConnectionMetrics()
		RegisterConnection(socketId, newTestConnectionInfo(m, ""))
		defer UnregisterConnection(socketId, CloseReasonGraceful)

		m.PktRecvDataSuccess.Store(uint64(50000 * (i + 1)))
		m.ByteRecvDataSuccess.Store(uint64(70000000 * (i + 1)))
	}

	handler := MetricsHandler()

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
		}
	})
}

// TestPrometheusSenderLockfreeMetrics tests the sender lockfree metrics exports
// Reference: sender_lockfree_implementation_plan.md Phase 7
func TestPrometheusSenderLockfreeMetrics(t *testing.T) {
	socketId := uint32(0xDEADBEEF)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, newTestConnectionInfo(m, "test-sender"))
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set TransmitCount metrics (Phase 3)
	m.SendFirstTransmit.Store(1000)
	m.SendAlreadySent.Store(50)

	// Set sequence number metrics (Phase 2)
	m.SendSeqAssigned.Store(5000)
	m.SendSeqWraparound.Store(2)

	// Get Prometheus output
	output := getPrometheusOutput(t)

	// Verify TransmitCount metrics
	require.Contains(t, output, "gosrt_send_first_transmit_total",
		"Should export first transmit metric")
	require.Contains(t, output, "gosrt_send_already_sent_total",
		"Should export already sent metric")

	// Verify sequence metrics
	require.Contains(t, output, "gosrt_send_seq_assigned_total",
		"Should export sequence assigned metric")
	require.Contains(t, output, "gosrt_send_seq_wraparound_total",
		"Should export sequence wraparound metric")

	// Verify values (check both metric name and socket ID)
	// Note: socket ID is output in lowercase hex
	require.Contains(t, output, `socket_id="0xdeadbeef"`,
		"Should have correct socket ID")

	// Verify non-zero values are exported
	require.Contains(t, output, "1000",
		"First transmit count should be 1000")
	require.Contains(t, output, "5000",
		"Seq assigned should be 5000")

	t.Logf("Sender lockfree metrics verified successfully")
}

// TestPrometheusPerRingMetrics verifies multi-ring io_uring metrics are correctly exported
// Phase 5 Refactoring: Now uses unified per-ring metrics for ALL ring counts (including single-ring)
// See multi_iouring_design.md Section 5.5 for unified Prometheus format
func TestPrometheusPerRingMetrics(t *testing.T) {
	tests := []struct {
		name      string
		ringCount int
		wantRings []string // All ring counts now have ring labels (unified approach)
	}{
		{"single_ring", 1, []string{`ring="0"`}},                                    // Unified: single-ring has ring label too
		{"two_rings", 2, []string{`ring="0"`, `ring="1"`}},                          // Multi-ring
		{"four_rings", 4, []string{`ring="0"`, `ring="1"`, `ring="2"`, `ring="3"`}}, // Multi-ring
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup connection with per-ring metrics
			socketId := uint32(0xABABABAB)
			m := newTestConnectionMetrics()

			// Initialize per-ring metrics for send path (always create array - unified approach)
			m.IoUringSendRingMetrics = NewIoUringRingMetrics(tt.ringCount)
			m.IoUringSendRingCount = tt.ringCount

			// Initialize per-ring metrics for dialer recv path (always create array - unified approach)
			m.IoUringDialerRecvRingMetrics = NewIoUringRingMetrics(tt.ringCount)
			m.IoUringDialerRecvRingCount = tt.ringCount

			RegisterConnection(socketId, newTestConnectionInfo(m, "per-ring-test"))
			defer UnregisterConnection(socketId, CloseReasonGraceful)

			// Simulate traffic on each ring
			for i, rm := range m.IoUringSendRingMetrics {
				rm.CompletionSuccess.Add(uint64(100 * (i + 1)))
				rm.PacketsProcessed.Add(uint64(100 * (i + 1)))
			}
			for i, rm := range m.IoUringDialerRecvRingMetrics {
				rm.CompletionSuccess.Add(uint64(200 * (i + 1)))
				rm.PacketsProcessed.Add(uint64(200 * (i + 1)))
			}

			// Get Prometheus output
			output := getPrometheusOutput(t)

			// Verify per-ring labels present for ALL ring counts (unified approach)
			for _, ringLabel := range tt.wantRings {
				require.Contains(t, output, ringLabel,
					"Should contain ring label %s", ringLabel)
			}

			// Verify ring count gauges are always exported (unified approach)
			require.Contains(t, output, "gosrt_iouring_send_ring_count",
				"Should export send ring count gauge")
			require.Contains(t, output, "gosrt_iouring_dialer_recv_ring_count",
				"Should export dialer recv ring count gauge")

			// Verify specific metric values for ring 0
			require.Contains(t, output,
				`gosrt_iouring_send_completion_success_total{socket_id="0xabababab",instance="per-ring-test",ring="0"} 100`,
				"Ring 0 send completion success should be 100")
			require.Contains(t, output,
				`gosrt_iouring_dialer_recv_completion_success_total{socket_id="0xabababab",instance="per-ring-test",ring="0"} 200`,
				"Ring 0 dialer recv completion success should be 200")

			t.Logf("Per-ring metrics verified for %d rings (unified approach)", tt.ringCount)
		})
	}
}

// TestPrometheusListenerPerRingMetrics verifies listener-level per-ring metrics
// Phase 5 Refactoring: Now uses unified per-ring metrics for ALL ring counts (including single-ring)
// See multi_iouring_design.md Section 5.5 for unified Prometheus format
func TestPrometheusListenerPerRingMetrics(t *testing.T) {
	tests := []struct {
		name      string
		ringCount int
		wantRings []string // All ring counts now have ring labels (unified approach)
	}{
		{"single_ring", 1, []string{`ring="0"`}},           // Unified: single-ring has ring label too
		{"two_rings", 2, []string{`ring="0"`, `ring="1"`}}, // Multi-ring
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Get the global listener metrics
			lm := GetListenerMetrics()

			// Initialize per-ring metrics (always creates array - unified approach)
			lm.InitListenerRecvRingMetrics(tt.ringCount)

			// Simulate traffic on each ring
			for i, rm := range lm.IoUringRecvRingMetrics {
				rm.CompletionSuccess.Add(uint64(500 * (i + 1)))
				rm.PacketsProcessed.Add(uint64(500 * (i + 1)))
			}

			// Get Prometheus output
			output := getPrometheusOutput(t)

			// Verify per-ring labels present for ALL ring counts (unified approach)
			for _, ringLabel := range tt.wantRings {
				require.Contains(t, output, ringLabel,
					"Should contain ring label %s", ringLabel)
			}

			// Verify ring count gauge is always exported (unified approach)
			require.Contains(t, output, "gosrt_iouring_listener_recv_ring_count",
				"Should export listener recv ring count gauge")

			// Verify specific metric value for ring 0
			require.Contains(t, output, `gosrt_iouring_listener_recv_completion_success_total{ring="0"} 500`,
				"Ring 0 listener recv completion success should be 500")

			// Reset for next test
			lm.IoUringRecvRingMetrics = nil
			lm.IoUringRecvRingCount = 0

			t.Logf("Listener per-ring metrics verified for %d rings (unified approach)", tt.ringCount)
		})
	}
}
