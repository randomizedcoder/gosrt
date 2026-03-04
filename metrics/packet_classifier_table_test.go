//go:build go1.18

package metrics

import (
	"sync"
	"testing"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Packet Classifier Comprehensive Table-Driven Tests
//
// Phase 4.1: Complete coverage of packet classification and metrics tracking.
// Tests all control types, error paths, drop reasons, and edge cases.
//
// Reference: unit_test_coverage_improvement_plan.md Phase 4.1
// =============================================================================

// =============================================================================
// IncrementRecvMetrics Tests
// =============================================================================

// TestIncrementRecvMetrics_ControlTypes_TableDriven tests all control packet types.
func TestIncrementRecvMetrics_ControlTypes_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		ctrlType   packet.CtrlType
		subType    packet.CtrlSubType
		checkField func(*ConnectionMetrics) uint64
	}{
		{"ACK", packet.CTRLTYPE_ACK, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvACKSuccess.Load() }},
		{"ACKACK", packet.CTRLTYPE_ACKACK, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvACKACKSuccess.Load() }},
		{"NAK", packet.CTRLTYPE_NAK, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvNAKSuccess.Load() }},
		{"KEEPALIVE", packet.CTRLTYPE_KEEPALIVE, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvKeepaliveSuccess.Load() }},
		{"SHUTDOWN", packet.CTRLTYPE_SHUTDOWN, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvShutdownSuccess.Load() }},
		{"HANDSHAKE", packet.CTRLTYPE_HANDSHAKE, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvHandshakeSuccess.Load() }},
		{"USER_KMREQ", packet.CTRLTYPE_USER, packet.EXTTYPE_KMREQ, func(m *ConnectionMetrics) uint64 { return m.PktRecvKMSuccess.Load() }},
		{"USER_KMRSP", packet.CTRLTYPE_USER, packet.EXTTYPE_KMRSP, func(m *ConnectionMetrics) uint64 { return m.PktRecvKMSuccess.Load() }},
		{"USER_UNKNOWN", packet.CTRLTYPE_USER, packet.CtrlSubType(99), func(m *ConnectionMetrics) uint64 { return m.PktRecvSubTypeUnknown.Load() }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockControlPacketWithSubType(tc.ctrlType, tc.subType)

			IncrementRecvMetrics(m, p, false, true, 0)

			require.Equal(t, uint64(1), tc.checkField(m), "%s counter should be 1", tc.name)
			require.Equal(t, uint64(1), m.PktRecvSuccess.Load(), "PktRecvSuccess should be 1")
		})
	}
}

// TestIncrementRecvMetrics_UnknownControlType tests unknown control type handling.
func TestIncrementRecvMetrics_UnknownControlType(t *testing.T) {
	m := &ConnectionMetrics{}
	p := createMockControlPacketWithSubType(packet.CtrlType(255), 0) // Unknown control type

	IncrementRecvMetrics(m, p, false, true, 0)

	require.Equal(t, uint64(1), m.PktRecvControlUnknown.Load(), "Unknown control type should be tracked")
	require.Equal(t, uint64(1), m.PktRecvSuccess.Load(), "PktRecvSuccess should still be 1")
}

// TestIncrementRecvMetrics_DataPacket tests data packet handling.
func TestIncrementRecvMetrics_DataPacket(t *testing.T) {
	m := &ConnectionMetrics{}
	p := createMockDataPacket()

	IncrementRecvMetrics(m, p, false, true, 0)

	require.Equal(t, uint64(1), m.PktRecvDataSuccess.Load())
	require.Equal(t, uint64(1016), m.ByteRecvDataSuccess.Load()) // 1000 data + 16 header
	require.Equal(t, uint64(1), m.PktRecvSuccess.Load())
}

// TestIncrementRecvMetrics_Paths_TableDriven tests io_uring vs non-io_uring paths.
func TestIncrementRecvMetrics_Paths_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		isIoUring      bool
		expectIoUring  uint64
		expectReadFrom uint64
	}{
		{"io_uring_path", true, 1, 0},
		{"readfrom_path", false, 0, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockDataPacket()

			IncrementRecvMetrics(m, p, tc.isIoUring, true, 0)

			require.Equal(t, tc.expectIoUring, m.PktRecvIoUring.Load())
			require.Equal(t, tc.expectReadFrom, m.PktRecvReadFrom.Load())
		})
	}
}

// TestIncrementRecvMetrics_ErrorPaths_TableDriven tests all error drop reasons.
func TestIncrementRecvMetrics_ErrorPaths_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		dropReason DropReason
		checkField func(*ConnectionMetrics) uint64
	}{
		{"parse_error", DropReasonParse, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorParse.Load() }},
		{"route_error", DropReasonRoute, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorRoute.Load() }},
		{"empty_error", DropReasonEmpty, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorEmpty.Load() }},
		{"unknown_socket", DropReasonUnknownSocket, func(m *ConnectionMetrics) uint64 { return m.PktRecvUnknownSocketId.Load() }},
		{"nil_connection", DropReasonNilConnection, func(m *ConnectionMetrics) uint64 { return m.PktRecvNilConnection.Load() }},
		{"wrong_peer", DropReasonWrongPeer, func(m *ConnectionMetrics) uint64 { return m.PktRecvWrongPeer.Load() }},
		{"backlog_full", DropReasonBacklogFull, func(m *ConnectionMetrics) uint64 { return m.PktRecvBacklogFull.Load() }},
		{"queue_full", DropReasonQueueFull, func(m *ConnectionMetrics) uint64 { return m.PktRecvQueueFull.Load() }},
		{"unknown_reason", DropReason(99), func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorUnknown.Load() }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockDataPacket()

			IncrementRecvMetrics(m, p, false, false, tc.dropReason)

			require.Equal(t, uint64(1), tc.checkField(m), "%s counter should be 1", tc.name)
			require.Equal(t, uint64(0), m.PktRecvSuccess.Load(), "Success should be 0 on error")
		})
	}
}

// TestIncrementRecvMetrics_NilPacket_TableDriven tests nil packet handling.
func TestIncrementRecvMetrics_NilPacket_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		success    bool
		dropReason DropReason
		expectNil  uint64
		expectErr  func(*ConnectionMetrics) uint64
	}{
		{"nil_success", true, 0, 1, func(m *ConnectionMetrics) uint64 { return 0 }},
		{"nil_parse_error", false, DropReasonParse, 1, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorParse.Load() }},
		{"nil_no_reason", false, 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorParse.Load() }},
		{"nil_unknown_reason", false, DropReason(99), 1, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorUnknown.Load() }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}

			IncrementRecvMetrics(m, nil, false, tc.success, tc.dropReason)

			require.Equal(t, tc.expectNil, m.PktRecvNil.Load())
			if !tc.success {
				require.Equal(t, uint64(1), tc.expectErr(m))
			}
		})
	}
}

// TestIncrementRecvMetrics_NilMetrics tests nil metrics handling.
func TestIncrementRecvMetrics_NilMetrics(t *testing.T) {
	// Should not panic
	IncrementRecvMetrics(nil, createMockDataPacket(), false, true, 0)
}

// =============================================================================
// IncrementRecvErrorMetrics Tests
// =============================================================================

// TestIncrementRecvErrorMetrics_TableDriven tests error metrics without packet.
func TestIncrementRecvErrorMetrics_TableDriven(t *testing.T) {
	testCases := []struct {
		name           string
		isIoUring      bool
		errorType      DropReason
		expectIoUring  uint64
		expectReadFrom uint64
		checkField     func(*ConnectionMetrics) uint64
		expectCount    uint64 // Expected count for checkField
	}{
		{"parse_error_readfrom", false, DropReasonParse, 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorParse.Load() }, 1},
		{"parse_error_iouring", true, DropReasonParse, 1, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorParse.Load() }, 1},
		{"empty_error", false, DropReasonEmpty, 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorEmpty.Load() }, 1},
		{"iouring_error", true, DropReasonIoUring, 1, 0, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorIoUring.Load() }, 2}, // 2: once from path, once from error type
		{"unknown_error", false, DropReason(99), 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktRecvErrorUnknown.Load() }, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}

			IncrementRecvErrorMetrics(m, tc.isIoUring, tc.errorType)

			require.Equal(t, tc.expectIoUring, m.PktRecvIoUring.Load())
			require.Equal(t, tc.expectReadFrom, m.PktRecvReadFrom.Load())
			require.Equal(t, tc.expectCount, tc.checkField(m))
		})
	}
}

// TestIncrementRecvErrorMetrics_IoUringPath tests io_uring specific error tracking.
func TestIncrementRecvErrorMetrics_IoUringPath(t *testing.T) {
	m := &ConnectionMetrics{}

	IncrementRecvErrorMetrics(m, true, DropReasonIoUring)

	require.Equal(t, uint64(1), m.PktRecvIoUring.Load())
	// io_uring path adds both to PktRecvErrorIoUring counter
	require.Equal(t, uint64(2), m.PktRecvErrorIoUring.Load()) // Once from path tracking, once from error type
}

// TestIncrementRecvErrorMetrics_NilMetrics tests nil metrics handling.
func TestIncrementRecvErrorMetrics_NilMetrics(t *testing.T) {
	// Should not panic
	IncrementRecvErrorMetrics(nil, false, DropReasonParse)
}

// =============================================================================
// IncrementSendMetrics Tests
// =============================================================================

// TestIncrementSendMetrics_ControlTypes_TableDriven tests all control packet types.
func TestIncrementSendMetrics_ControlTypes_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		ctrlType   packet.CtrlType
		subType    packet.CtrlSubType
		checkField func(*ConnectionMetrics) uint64
	}{
		{"ACK", packet.CTRLTYPE_ACK, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentACKSuccess.Load() }},
		{"ACKACK", packet.CTRLTYPE_ACKACK, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentACKACKSuccess.Load() }},
		{"NAK", packet.CTRLTYPE_NAK, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentNAKSuccess.Load() }},
		{"KEEPALIVE", packet.CTRLTYPE_KEEPALIVE, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentKeepaliveSuccess.Load() }},
		{"SHUTDOWN", packet.CTRLTYPE_SHUTDOWN, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentShutdownSuccess.Load() }},
		{"HANDSHAKE", packet.CTRLTYPE_HANDSHAKE, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }},
		{"USER_KMREQ", packet.CTRLTYPE_USER, packet.EXTTYPE_KMREQ, func(m *ConnectionMetrics) uint64 { return m.PktSentKMSuccess.Load() }},
		{"USER_KMRSP", packet.CTRLTYPE_USER, packet.EXTTYPE_KMRSP, func(m *ConnectionMetrics) uint64 { return m.PktSentKMSuccess.Load() }},
		{"USER_OTHER", packet.CTRLTYPE_USER, packet.CtrlSubType(99), func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }},
		{"UNKNOWN_CTRL", packet.CtrlType(255), 0, func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockControlPacketWithSubType(tc.ctrlType, tc.subType)

			IncrementSendMetrics(m, p, false, true, 0)

			require.Equal(t, uint64(1), tc.checkField(m), "%s counter should be 1", tc.name)
		})
	}
}

// TestIncrementSendMetrics_DataPacket tests data packet handling.
func TestIncrementSendMetrics_DataPacket(t *testing.T) {
	m := &ConnectionMetrics{}
	p := createMockDataPacket()

	IncrementSendMetrics(m, p, false, true, 0)

	require.Equal(t, uint64(1), m.PktSentDataSuccess.Load())
	require.Equal(t, uint64(1016), m.ByteSentDataSuccess.Load())
}

// TestIncrementSendMetrics_Paths_TableDriven tests io_uring vs non-io_uring paths.
func TestIncrementSendMetrics_Paths_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		isIoUring     bool
		expectIoUring uint64
		expectWriteTo uint64
	}{
		{"io_uring_path", true, 1, 0},
		{"writeto_path", false, 0, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockDataPacket()

			IncrementSendMetrics(m, p, tc.isIoUring, true, 0)

			require.Equal(t, tc.expectIoUring, m.PktSentIoUring.Load())
			require.Equal(t, tc.expectWriteTo, m.PktSentWriteTo.Load())
		})
	}
}

// TestIncrementSendMetrics_ErrorPaths_TableDriven tests send error paths.
func TestIncrementSendMetrics_ErrorPaths_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		dropReason DropReason
	}{
		{"marshal_error", DropReasonMarshal},
		{"ring_full", DropReasonRingFull},
		{"submit_error", DropReasonSubmit},
		{"iouring_error", DropReasonIoUring},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockDataPacket()

			IncrementSendMetrics(m, p, false, false, tc.dropReason)

			// Error paths call IncrementSendErrorDrop
			// We just verify it doesn't panic and paths are tracked
			require.Equal(t, uint64(1), m.PktSentWriteTo.Load())
		})
	}
}

// TestIncrementSendMetrics_NilPacket_TableDriven tests nil packet handling.
func TestIncrementSendMetrics_NilPacket_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		success     bool
		dropReason  DropReason
		expectField func(*ConnectionMetrics) uint64
	}{
		{"nil_success", true, 0, func(m *ConnectionMetrics) uint64 { return 0 }},
		{"nil_marshal_error", false, DropReasonMarshal, func(m *ConnectionMetrics) uint64 { return m.PktSentErrorMarshal.Load() }},
		{"nil_no_reason", false, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentErrorMarshal.Load() }},
		{"nil_unknown_reason", false, DropReason(99), func(m *ConnectionMetrics) uint64 { return m.PktSentErrorUnknown.Load() }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}

			IncrementSendMetrics(m, nil, false, tc.success, tc.dropReason)

			if !tc.success {
				require.Equal(t, uint64(1), tc.expectField(m))
			}
		})
	}
}

// TestIncrementSendMetrics_NilMetrics tests nil metrics handling.
func TestIncrementSendMetrics_NilMetrics(t *testing.T) {
	// Should not panic
	IncrementSendMetrics(nil, createMockDataPacket(), false, true, 0)
}

// =============================================================================
// IncrementSendControlMetric Tests
// =============================================================================

// TestIncrementSendControlMetric_AllTypes_TableDriven tests all control types.
func TestIncrementSendControlMetric_AllTypes_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		controlType packet.CtrlType
		checkField  func(*ConnectionMetrics) uint64
	}{
		{"ACK", packet.CTRLTYPE_ACK, func(m *ConnectionMetrics) uint64 { return m.PktSentACKSuccess.Load() }},
		{"ACKACK", packet.CTRLTYPE_ACKACK, func(m *ConnectionMetrics) uint64 { return m.PktSentACKACKSuccess.Load() }},
		{"NAK", packet.CTRLTYPE_NAK, func(m *ConnectionMetrics) uint64 { return m.PktSentNAKSuccess.Load() }},
		{"KEEPALIVE", packet.CTRLTYPE_KEEPALIVE, func(m *ConnectionMetrics) uint64 { return m.PktSentKeepaliveSuccess.Load() }},
		{"SHUTDOWN", packet.CTRLTYPE_SHUTDOWN, func(m *ConnectionMetrics) uint64 { return m.PktSentShutdownSuccess.Load() }},
		{"HANDSHAKE", packet.CTRLTYPE_HANDSHAKE, func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }},
		{"USER", packet.CTRLTYPE_USER, func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }},
		{"UNKNOWN", packet.CtrlType(255), func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}

			IncrementSendControlMetric(m, tc.controlType)

			require.Equal(t, uint64(1), tc.checkField(m), "%s counter should be 1", tc.name)
		})
	}
}

// =============================================================================
// IncrementSendErrorMetrics Tests
// =============================================================================

// TestIncrementSendErrorMetrics_TableDriven tests error metrics without packet.
func TestIncrementSendErrorMetrics_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		isIoUring     bool
		errorType     DropReason
		expectIoUring uint64
		expectWriteTo uint64
		checkField    func(*ConnectionMetrics) uint64
		expectCount   uint64 // Expected count for checkField
	}{
		{"marshal_error", false, DropReasonMarshal, 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktSentErrorMarshal.Load() }, 1},
		{"ring_full", false, DropReasonRingFull, 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktSentRingFull.Load() }, 1},
		{"submit_error", true, DropReasonSubmit, 1, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentErrorSubmit.Load() }, 1},
		{"iouring_error", true, DropReasonIoUring, 1, 0, func(m *ConnectionMetrics) uint64 { return m.PktSentErrorIoUring.Load() }, 2}, // 2: once from path, once from error type
		{"unknown_error", false, DropReason(99), 0, 1, func(m *ConnectionMetrics) uint64 { return m.PktSentErrorUnknown.Load() }, 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &ConnectionMetrics{}

			IncrementSendErrorMetrics(m, tc.isIoUring, tc.errorType)

			require.Equal(t, tc.expectIoUring, m.PktSentIoUring.Load())
			require.Equal(t, tc.expectWriteTo, m.PktSentWriteTo.Load())
			require.Equal(t, tc.expectCount, tc.checkField(m))
		})
	}
}

// TestIncrementSendErrorMetrics_IoUringPath tests io_uring specific error tracking.
func TestIncrementSendErrorMetrics_IoUringPath(t *testing.T) {
	m := &ConnectionMetrics{}

	IncrementSendErrorMetrics(m, true, DropReasonIoUring)

	require.Equal(t, uint64(1), m.PktSentIoUring.Load())
	require.Equal(t, uint64(2), m.PktSentErrorIoUring.Load()) // Once from path, once from error type
}

// TestIncrementSendErrorMetrics_NilMetrics tests nil metrics handling.
func TestIncrementSendErrorMetrics_NilMetrics(t *testing.T) {
	// Should not panic
	IncrementSendErrorMetrics(nil, false, DropReasonMarshal)
}

// =============================================================================
// DropReason String Tests
// =============================================================================

// TestDropReason_String_TableDriven tests all drop reason string conversions.
func TestDropReason_String_TableDriven(t *testing.T) {
	testCases := []struct {
		reason   DropReason
		expected string
	}{
		{DropReasonTooOld, "too_old"},
		{DropReasonTooOldSend, "too_old"},
		{DropReasonAlreadyAcked, "already_acked"},
		{DropReasonDuplicate, "duplicate"},
		{DropReasonStoreInsertFailed, "store_insert_failed"},
		{DropReasonMarshal, "marshal"},
		{DropReasonRingFull, "ring_full"},
		{DropReasonSubmit, "submit"},
		{DropReasonIoUring, "iouring"},
		{DropReasonParse, "parse"},
		{DropReasonRoute, "route"},
		{DropReasonEmpty, "empty"},
		{DropReasonWrite, "write"},
		{DropReasonWrongPeer, "wrong_peer"},
		{DropReasonUnknownSocket, "unknown_socket"},
		{DropReasonNilConnection, "nil_connection"},
		{DropReasonBacklogFull, "backlog_full"},
		{DropReasonQueueFull, "queue_full"},
		{DropReason(99), "unknown"},
		{DropReason(255), "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.expected, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.reason.String())
		})
	}
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

// TestPacketClassifier_Concurrent tests concurrent access to metrics.
func TestPacketClassifier_Concurrent(t *testing.T) {
	m := &ConnectionMetrics{}

	var wg sync.WaitGroup
	iterations := 1000

	// Concurrent recv metrics
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				p := createMockDataPacket()
				IncrementRecvMetrics(m, p, j%2 == 0, true, 0)
			}
		}()
	}

	// Concurrent send metrics
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				p := createMockDataPacket()
				IncrementSendMetrics(m, p, j%2 == 0, true, 0)
			}
		}()
	}

	// Concurrent error metrics
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				IncrementRecvErrorMetrics(m, j%2 == 0, DropReasonParse)
				IncrementSendErrorMetrics(m, j%2 == 0, DropReasonMarshal)
			}
		}()
	}

	wg.Wait()

	// Verify counts are reasonable (4 goroutines * 1000 iterations)
	expectedRecvData := uint64(4 * iterations)
	expectedSendData := uint64(4 * iterations)
	expectedRecvErr := uint64(2 * iterations)
	expectedSendErr := uint64(2 * iterations)

	require.Equal(t, expectedRecvData, m.PktRecvDataSuccess.Load())
	require.Equal(t, expectedSendData, m.PktSentDataSuccess.Load())
	require.Equal(t, expectedRecvErr, m.PktRecvErrorParse.Load())
	require.Equal(t, expectedSendErr, m.PktSentErrorMarshal.Load())
}

// =============================================================================
// Edge Case Tests
// =============================================================================

// TestPacketClassifier_ByteCounterOverflow tests large byte counters.
func TestPacketClassifier_ByteCounterOverflow(t *testing.T) {
	m := &ConnectionMetrics{}

	// Simulate many large packets
	for i := 0; i < 100; i++ {
		p := &mockPacket{
			header: &packet.PacketHeader{IsControlPacket: false},
			data:   make([]byte, 65536), // Max payload
		}
		IncrementRecvMetrics(m, p, false, true, 0)
	}

	// Should accumulate correctly
	expectedBytes := uint64(100 * (65536 + 16))
	require.Equal(t, expectedBytes, m.ByteRecvDataSuccess.Load())
}

// TestPacketClassifier_RapidAccumulation tests rapid metric increments.
func TestPacketClassifier_RapidAccumulation(t *testing.T) {
	m := &ConnectionMetrics{}
	p := createMockControlPacket(packet.CTRLTYPE_NAK)

	// Rapid increments
	count := uint64(10000)
	for i := uint64(0); i < count; i++ {
		IncrementRecvMetrics(m, p, false, true, 0)
	}

	require.Equal(t, count, m.PktRecvNAKSuccess.Load())
	require.Equal(t, count, m.PktRecvSuccess.Load())
}

// =============================================================================
// Helper Functions
// =============================================================================

// createMockControlPacketWithSubType creates a control packet with subtype
func createMockControlPacketWithSubType(ctrlType packet.CtrlType, subType packet.CtrlSubType) packet.Packet {
	return &mockPacket{
		header: &packet.PacketHeader{
			IsControlPacket: true,
			ControlType:     ctrlType,
			SubType:         subType,
		},
		data: make([]byte, 100),
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkIncrementRecvMetrics_DataPacket(b *testing.B) {
	m := &ConnectionMetrics{}
	p := createMockDataPacket()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IncrementRecvMetrics(m, p, false, true, 0)
	}
}

func BenchmarkIncrementRecvMetrics_ControlPacket(b *testing.B) {
	m := &ConnectionMetrics{}
	p := createMockControlPacket(packet.CTRLTYPE_ACK)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IncrementRecvMetrics(m, p, false, true, 0)
	}
}

func BenchmarkIncrementSendMetrics_DataPacket(b *testing.B) {
	m := &ConnectionMetrics{}
	p := createMockDataPacket()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IncrementSendMetrics(m, p, false, true, 0)
	}
}

func BenchmarkIncrementSendControlMetric(b *testing.B) {
	m := &ConnectionMetrics{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IncrementSendControlMetric(m, packet.CTRLTYPE_NAK)
	}
}

func BenchmarkPacketClassifier_Concurrent(b *testing.B) {
	m := &ConnectionMetrics{}
	p := createMockDataPacket()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			IncrementRecvMetrics(m, p, false, true, 0)
		}
	})
}
