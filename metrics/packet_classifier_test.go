package metrics

import (
	"io"
	"net"
	"sync"
	"testing"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// TestIncrementSendControlMetric verifies that the io_uring helper function
// correctly increments counters for each control packet type.
// This is critical because io_uring decommissions control packets before
// IncrementSendMetrics can classify them.
func TestIncrementSendControlMetric(t *testing.T) {
	tests := []struct {
		name        string
		controlType packet.CtrlType
		checkField  func(*ConnectionMetrics) uint64
	}{
		{
			name:        "ACK",
			controlType: packet.CTRLTYPE_ACK,
			checkField:  func(m *ConnectionMetrics) uint64 { return m.PktSentACKSuccess.Load() },
		},
		{
			name:        "ACKACK",
			controlType: packet.CTRLTYPE_ACKACK,
			checkField:  func(m *ConnectionMetrics) uint64 { return m.PktSentACKACKSuccess.Load() },
		},
		{
			name:        "NAK",
			controlType: packet.CTRLTYPE_NAK,
			checkField:  func(m *ConnectionMetrics) uint64 { return m.PktSentNAKSuccess.Load() },
		},
		{
			name:        "Keepalive",
			controlType: packet.CTRLTYPE_KEEPALIVE,
			checkField:  func(m *ConnectionMetrics) uint64 { return m.PktSentKeepaliveSuccess.Load() },
		},
		{
			name:        "Shutdown",
			controlType: packet.CTRLTYPE_SHUTDOWN,
			checkField:  func(m *ConnectionMetrics) uint64 { return m.PktSentShutdownSuccess.Load() },
		},
		{
			name:        "Handshake",
			controlType: packet.CTRLTYPE_HANDSHAKE,
			checkField:  func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &ConnectionMetrics{}

			// Call the helper
			IncrementSendControlMetric(m, tt.controlType)

			// Verify the correct counter was incremented
			require.Equal(t, uint64(1), tt.checkField(m),
				"Expected %s counter to be 1", tt.name)

			// Call again to verify accumulation
			IncrementSendControlMetric(m, tt.controlType)
			require.Equal(t, uint64(2), tt.checkField(m),
				"Expected %s counter to be 2 after second call", tt.name)
		})
	}
}

// TestIncrementSendControlMetricNilMetrics verifies the function handles nil safely
func TestIncrementSendControlMetricNilMetrics(t *testing.T) {
	// Should not panic
	IncrementSendControlMetric(nil, packet.CTRLTYPE_NAK)
}

// TestIncrementRecvMetricsNAK verifies that NAK packets are correctly counted
// in the receive path. This ensures no double-counting occurs.
func TestIncrementRecvMetricsNAK(t *testing.T) {
	m := &ConnectionMetrics{}

	// Create a mock NAK packet
	p := createMockControlPacket(packet.CTRLTYPE_NAK)

	// Call IncrementRecvMetrics (the single source of truth)
	IncrementRecvMetrics(m, p, false, true, 0)

	// Verify NAK counter is incremented exactly once
	require.Equal(t, uint64(1), m.PktRecvNAKSuccess.Load(),
		"PktRecvNAKSuccess should be 1 after single IncrementRecvMetrics call")

	// Verify path counter is also incremented
	require.Equal(t, uint64(1), m.PktRecvReadFrom.Load(),
		"PktRecvReadFrom should be 1 (non-io_uring path)")

	// Call again to verify accumulation
	IncrementRecvMetrics(m, p, false, true, 0)
	require.Equal(t, uint64(2), m.PktRecvNAKSuccess.Load(),
		"PktRecvNAKSuccess should be 2 after second call")
}

// TestIncrementRecvMetricsIoUringPath verifies io_uring receive path
func TestIncrementRecvMetricsIoUringPath(t *testing.T) {
	m := &ConnectionMetrics{}

	p := createMockControlPacket(packet.CTRLTYPE_NAK)

	// io_uring path
	IncrementRecvMetrics(m, p, true, true, 0)

	require.Equal(t, uint64(1), m.PktRecvNAKSuccess.Load(),
		"PktRecvNAKSuccess should be 1")
	require.Equal(t, uint64(1), m.PktRecvIoUring.Load(),
		"PktRecvIoUring should be 1 (io_uring path)")
	require.Equal(t, uint64(0), m.PktRecvReadFrom.Load(),
		"PktRecvReadFrom should be 0 (not ReadFrom path)")
}

// TestIncrementSendMetricsNAK verifies that NAK packets are correctly counted
// in the non-io_uring send path (used by listen.go and dial.go).
func TestIncrementSendMetricsNAK(t *testing.T) {
	m := &ConnectionMetrics{}

	// Create a mock NAK packet
	p := createMockControlPacket(packet.CTRLTYPE_NAK)

	// Call IncrementSendMetrics with valid packet (non-io_uring path)
	IncrementSendMetrics(m, p, false, true, 0)

	// Verify NAK counter is incremented
	require.Equal(t, uint64(1), m.PktSentNAKSuccess.Load(),
		"PktSentNAKSuccess should be 1")

	// Verify path counter
	require.Equal(t, uint64(1), m.PktSentWriteTo.Load(),
		"PktSentWriteTo should be 1 (non-io_uring path)")
}

// TestIncrementSendMetricsNilPacket verifies behavior when packet is nil
// This simulates the io_uring path where control packets are decommissioned
func TestIncrementSendMetricsNilPacket(t *testing.T) {
	m := &ConnectionMetrics{}

	// Call with nil packet (simulates decommissioned control packet)
	IncrementSendMetrics(m, nil, true, true, 0)

	// Verify path counter IS incremented
	require.Equal(t, uint64(1), m.PktSentIoUring.Load(),
		"PktSentIoUring should be 1 even with nil packet")

	// Verify NAK counter is NOT incremented (can't classify nil packet)
	require.Equal(t, uint64(0), m.PktSentNAKSuccess.Load(),
		"PktSentNAKSuccess should be 0 (nil packet can't be classified)")

	// This is why we need IncrementSendControlMetric for io_uring path!
}

// TestAllControlTypesReceive verifies all control packet types are counted on receive
func TestAllControlTypesReceive(t *testing.T) {
	controlTypes := []struct {
		ctrlType   packet.CtrlType
		checkField func(*ConnectionMetrics) uint64
		name       string
	}{
		{packet.CTRLTYPE_ACK, func(m *ConnectionMetrics) uint64 { return m.PktRecvACKSuccess.Load() }, "ACK"},
		{packet.CTRLTYPE_ACKACK, func(m *ConnectionMetrics) uint64 { return m.PktRecvACKACKSuccess.Load() }, "ACKACK"},
		{packet.CTRLTYPE_NAK, func(m *ConnectionMetrics) uint64 { return m.PktRecvNAKSuccess.Load() }, "NAK"},
		{packet.CTRLTYPE_KEEPALIVE, func(m *ConnectionMetrics) uint64 { return m.PktRecvKeepaliveSuccess.Load() }, "Keepalive"},
		{packet.CTRLTYPE_SHUTDOWN, func(m *ConnectionMetrics) uint64 { return m.PktRecvShutdownSuccess.Load() }, "Shutdown"},
		{packet.CTRLTYPE_HANDSHAKE, func(m *ConnectionMetrics) uint64 { return m.PktRecvHandshakeSuccess.Load() }, "Handshake"},
	}

	for _, tt := range controlTypes {
		t.Run(tt.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockControlPacket(tt.ctrlType)

			IncrementRecvMetrics(m, p, false, true, 0)

			require.Equal(t, uint64(1), tt.checkField(m),
				"%s counter should be 1", tt.name)
		})
	}
}

// TestAllControlTypesSend verifies all control packet types are counted on send
func TestAllControlTypesSend(t *testing.T) {
	controlTypes := []struct {
		ctrlType   packet.CtrlType
		checkField func(*ConnectionMetrics) uint64
		name       string
	}{
		{packet.CTRLTYPE_ACK, func(m *ConnectionMetrics) uint64 { return m.PktSentACKSuccess.Load() }, "ACK"},
		{packet.CTRLTYPE_ACKACK, func(m *ConnectionMetrics) uint64 { return m.PktSentACKACKSuccess.Load() }, "ACKACK"},
		{packet.CTRLTYPE_NAK, func(m *ConnectionMetrics) uint64 { return m.PktSentNAKSuccess.Load() }, "NAK"},
		{packet.CTRLTYPE_KEEPALIVE, func(m *ConnectionMetrics) uint64 { return m.PktSentKeepaliveSuccess.Load() }, "Keepalive"},
		{packet.CTRLTYPE_SHUTDOWN, func(m *ConnectionMetrics) uint64 { return m.PktSentShutdownSuccess.Load() }, "Shutdown"},
		{packet.CTRLTYPE_HANDSHAKE, func(m *ConnectionMetrics) uint64 { return m.PktSentHandshakeSuccess.Load() }, "Handshake"},
	}

	for _, tt := range controlTypes {
		t.Run(tt.name, func(t *testing.T) {
			m := &ConnectionMetrics{}
			p := createMockControlPacket(tt.ctrlType)

			IncrementSendMetrics(m, p, false, true, 0)

			require.Equal(t, uint64(1), tt.checkField(m),
				"%s counter should be 1", tt.name)
		})
	}
}

// mockPacket implements packet.Packet interface for testing
type mockPacket struct {
	header *packet.PacketHeader
	data   []byte
}

func (m *mockPacket) String() string               { return "mock packet" }
func (m *mockPacket) Clone() packet.Packet         { return m }
func (m *mockPacket) Header() *packet.PacketHeader { return m.header }
func (m *mockPacket) Data() []byte                 { return m.data }
func (m *mockPacket) SetData(d []byte)             { m.data = d }
func (m *mockPacket) Len() uint64                  { return uint64(len(m.data) + 16) } // 16 bytes for header
func (m *mockPacket) Marshal(w io.Writer) error    { return nil }
func (m *mockPacket) Unmarshal(data []byte) error  { return nil }
func (m *mockPacket) Dump() string                 { return "mock packet dump" }
func (m *mockPacket) MarshalCIF(cif packet.CIF) error {
	return nil
}
func (m *mockPacket) UnmarshalCIF(cif packet.CIF) error {
	return nil
}
func (m *mockPacket) Decommission() {}

// Phase 2: Zero-copy interface methods
func (m *mockPacket) DecommissionWithBuffer(bufferPool *sync.Pool) {}
func (m *mockPacket) HasRecvBuffer() bool                          { return false }
func (m *mockPacket) GetRecvBuffer() *[]byte                       { return nil }
func (m *mockPacket) ClearRecvBuffer()                             {}
func (m *mockPacket) UnmarshalZeroCopy(buf *[]byte, n int, addr net.Addr) error {
	return nil
}

// createMockControlPacket creates a mock control packet for testing
func createMockControlPacket(ctrlType packet.CtrlType) packet.Packet {
	return &mockPacket{
		header: &packet.PacketHeader{
			IsControlPacket: true,
			ControlType:     ctrlType,
		},
		data: make([]byte, 100),
	}
}

// createMockDataPacket creates a mock data packet for testing
func createMockDataPacket() packet.Packet {
	return &mockPacket{
		header: &packet.PacketHeader{
			IsControlPacket: false,
		},
		data: make([]byte, 1000),
	}
}

// TestDataPacketCounting verifies data packets are counted correctly
func TestDataPacketCounting(t *testing.T) {
	m := &ConnectionMetrics{}
	p := createMockDataPacket()

	// Receive path
	IncrementRecvMetrics(m, p, false, true, 0)
	require.Equal(t, uint64(1), m.PktRecvDataSuccess.Load(),
		"PktRecvDataSuccess should be 1")

	// Send path
	IncrementSendMetrics(m, p, false, true, 0)
	require.Equal(t, uint64(1), m.PktSentDataSuccess.Load(),
		"PktSentDataSuccess should be 1")
}

// TestNAKCountingBothPaths verifies that NAK counting is correct for BOTH
// io_uring and non-io_uring paths. This test documents the fix for Defect 8.
//
// The bug was:
// - Receive path: double-counting (packet classifier + handleNAK)
// - io_uring send path: no counting (packet was nil after decommission)
//
// The fix ensures single counting in all paths.
func TestNAKCountingBothPaths(t *testing.T) {
	t.Run("ReceivePath_NonIoUring", func(t *testing.T) {
		m := &ConnectionMetrics{}
		p := createMockControlPacket(packet.CTRLTYPE_NAK)

		// Simulate non-io_uring receive path (listen.go/dial.go)
		IncrementRecvMetrics(m, p, false, true, 0)

		require.Equal(t, uint64(1), m.PktRecvNAKSuccess.Load(),
			"NAK should be counted exactly once")
		require.Equal(t, uint64(1), m.PktRecvReadFrom.Load(),
			"Should track ReadFrom path")
		require.Equal(t, uint64(0), m.PktRecvIoUring.Load(),
			"Should NOT track io_uring path")
	})

	t.Run("ReceivePath_IoUring", func(t *testing.T) {
		m := &ConnectionMetrics{}
		p := createMockControlPacket(packet.CTRLTYPE_NAK)

		// Simulate io_uring receive path (listen_linux.go/dial_linux.go)
		IncrementRecvMetrics(m, p, true, true, 0)

		require.Equal(t, uint64(1), m.PktRecvNAKSuccess.Load(),
			"NAK should be counted exactly once")
		require.Equal(t, uint64(0), m.PktRecvReadFrom.Load(),
			"Should NOT track ReadFrom path")
		require.Equal(t, uint64(1), m.PktRecvIoUring.Load(),
			"Should track io_uring path")
	})

	t.Run("SendPath_NonIoUring", func(t *testing.T) {
		m := &ConnectionMetrics{}
		p := createMockControlPacket(packet.CTRLTYPE_NAK)

		// Simulate non-io_uring send path (listen.go/dial.go)
		// The packet is valid, so IncrementSendMetrics can classify it
		IncrementSendMetrics(m, p, false, true, 0)

		require.Equal(t, uint64(1), m.PktSentNAKSuccess.Load(),
			"NAK should be counted exactly once")
		require.Equal(t, uint64(1), m.PktSentWriteTo.Load(),
			"Should track WriteTo path")
		require.Equal(t, uint64(0), m.PktSentIoUring.Load(),
			"Should NOT track io_uring path")
	})

	t.Run("SendPath_IoUring", func(t *testing.T) {
		m := &ConnectionMetrics{}

		// Simulate io_uring send path (connection_linux.go)
		// The packet is decommissioned before metrics, so we use the helper
		controlType := packet.CTRLTYPE_NAK

		// Step 1: Track io_uring path (done in connection_linux.go)
		m.PktSentIoUring.Add(1)

		// Step 2: Use helper to count control type (done in connection_linux.go)
		IncrementSendControlMetric(m, controlType)

		require.Equal(t, uint64(1), m.PktSentNAKSuccess.Load(),
			"NAK should be counted exactly once")
		require.Equal(t, uint64(0), m.PktSentWriteTo.Load(),
			"Should NOT track WriteTo path")
		require.Equal(t, uint64(1), m.PktSentIoUring.Load(),
			"Should track io_uring path")
	})

	t.Run("SendPath_IoUring_OldBehavior_Broken", func(t *testing.T) {
		// This test documents the OLD broken behavior where io_uring
		// control packets were not counted because p was nil
		m := &ConnectionMetrics{}

		// OLD BROKEN: IncrementSendMetrics was called with nil packet
		IncrementSendMetrics(m, nil, true, true, 0)

		// The NAK counter was NOT incremented (bug!)
		require.Equal(t, uint64(0), m.PktSentNAKSuccess.Load(),
			"OLD BEHAVIOR: nil packet cannot be classified - NAK not counted")

		// But io_uring path WAS tracked
		require.Equal(t, uint64(1), m.PktSentIoUring.Load(),
			"Path was tracked even with nil packet")
	})
}

// TestNoDoubleCountingReceive verifies that calling IncrementRecvMetrics
// multiple times does NOT cause unexpected double-counting.
// This is important because handleNAK() used to also increment the counter.
func TestNoDoubleCountingReceive(t *testing.T) {
	m := &ConnectionMetrics{}
	p := createMockControlPacket(packet.CTRLTYPE_NAK)

	// Simulate: IncrementRecvMetrics called once (as it should be)
	IncrementRecvMetrics(m, p, false, true, 0)
	require.Equal(t, uint64(1), m.PktRecvNAKSuccess.Load())

	// If handleNAK also incremented (the old bug), we'd see 2 here
	// But now handleNAK doesn't increment, so this counter stays at 1
	// We can't directly test handleNAK here, but the integration test verifies this
}

// TestNoDoubleCountingSend verifies that the io_uring and non-io_uring
// paths don't accidentally double-count.
func TestNoDoubleCountingSend(t *testing.T) {
	t.Run("NonIoUring_SingleCall", func(t *testing.T) {
		m := &ConnectionMetrics{}
		p := createMockControlPacket(packet.CTRLTYPE_NAK)

		// Single call to IncrementSendMetrics
		IncrementSendMetrics(m, p, false, true, 0)
		require.Equal(t, uint64(1), m.PktSentNAKSuccess.Load(),
			"Should be exactly 1, not double-counted")
	})

	t.Run("IoUring_SingleCall", func(t *testing.T) {
		m := &ConnectionMetrics{}

		// Single call to IncrementSendControlMetric
		m.PktSentIoUring.Add(1)
		IncrementSendControlMetric(m, packet.CTRLTYPE_NAK)
		require.Equal(t, uint64(1), m.PktSentNAKSuccess.Load(),
			"Should be exactly 1, not double-counted")
	})
}

