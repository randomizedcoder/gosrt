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

// TestPrometheusOutputFormat verifies the Prometheus output is valid exposition format
func TestPrometheusOutputFormat(t *testing.T) {
	// Create a connection with known socket ID
	socketId := uint32(0x12345678)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, m, "")
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
	RegisterConnection(socketId, m, "")
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

// TestPrometheusExportsAllCounters uses reflection to verify every atomic counter
// in ConnectionMetrics is exported to Prometheus output.
// This catches any metrics that are added to the struct but forgotten in handler.go.
func TestPrometheusExportsAllCounters(t *testing.T) {
	socketId := uint32(0xDEADBEEF)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, m, "")
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
		// These are gauge values exported separately via writeGauge() in handler.go
		// Not included in the counter export loop - tested in TestRateMetricsExported
		"RecvRatePeriodUs":       true, // Internal timing, not exported
		"RecvRateLastUs":         true, // Internal timing, not exported
		"RecvRatePackets":        true, // Raw counter for calculation, not exported
		"RecvRateBytes":          true, // Raw counter for calculation, not exported
		"RecvRateBytesRetrans":   true, // Raw counter for calculation, not exported
		"RecvRatePacketsPerSec":  true, // Exported as gauge (tested separately)
		"RecvRateBytesPerSec":    true, // Exported as gauge (tested separately)
		"RecvRatePktRetransRate": true, // Exported as gauge (tested separately)
		"SendRatePeriodUs":       true, // Internal timing, not exported
		"SendRateLastUs":         true, // Internal timing, not exported
		"SendRateBytes":          true, // Raw counter for calculation, not exported
		"SendRateBytesSent":      true, // Raw counter for calculation, not exported
		"SendRateBytesRetrans":   true, // Raw counter for calculation, not exported
		"SendRateEstInputBW":     true, // Exported as gauge (tested separately)
		"SendRateEstSentBW":      true, // Exported as gauge (tested separately)
		"SendRatePktRetransRate": true, // Exported as gauge (tested separately)
		"RecvLightACKCounter":    true, // Internal counter, not exported
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
	RegisterConnection(socketId, m, "")
	defer UnregisterConnection(socketId, CloseReasonGraceful)

	// Set values for different packet types
	m.PktRecvDataSuccess.Store(1)
	m.PktRecvACKSuccess.Store(1)
	m.PktRecvNAKSuccess.Store(1)
	m.PktSentDataSuccess.Store(1)
	m.CongestionRecvPkt.Store(1)
	m.CongestionSendPkt.Store(1)

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

	RegisterConnection(socketId1, m1, "")
	RegisterConnection(socketId2, m2, "")
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
	RegisterConnection(socketId, m, "test-rate")
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
	RegisterConnection(socketId, m, "")
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
	RegisterConnection(socketId, m, "")
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

// TestPrometheusZeroFiltering verifies that zero values are not exported
func TestPrometheusZeroFiltering(t *testing.T) {
	socketId := uint32(0x77777777)
	m := newTestConnectionMetrics()
	RegisterConnection(socketId, m, "")
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
	RegisterConnection(socketId, m, "")
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
	RegisterConnection(socketId, m, "")
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
			RegisterConnection(socketId, m, "")
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
	RegisterConnection(socketId, m, "")
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
		RegisterConnection(socketId, m, "")
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
		RegisterConnection(socketId, m, "")
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
				RegisterConnection(socketId, m, "")
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
				req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
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
		RegisterConnection(socketId, m, "")
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
