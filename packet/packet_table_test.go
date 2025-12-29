package packet

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Table-Driven Tests for Packet Types and Utilities
// ═══════════════════════════════════════════════════════════════════════════

// TestControlType_String tests ControlType.String() method
func TestControlType_String(t *testing.T) {
	tests := []struct {
		name     string
		ct       CtrlType
		expected string
	}{
		{"Handshake", CTRLTYPE_HANDSHAKE, "HANDSHAKE"},
		{"KeepAlive", CTRLTYPE_KEEPALIVE, "KEEPALIVE"},
		{"ACK", CTRLTYPE_ACK, "ACK"},
		{"NAK", CTRLTYPE_NAK, "NAK"},
		{"Shutdown", CTRLTYPE_SHUTDOWN, "SHUTDOWN"},
		{"ACKACK", CTRLTYPE_ACKACK, "ACKACK"},
		{"User", CTRLTYPE_USER, "USER"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.ct.String()
			require.Equal(t, tc.expected, result)
		})
	}
}

// TestHandshakeType_String tests HandshakeType.String() method
func TestHandshakeType_String(t *testing.T) {
	tests := []struct {
		name     string
		ht       HandshakeType
		expected string
	}{
		{"Done", HSTYPE_DONE, "DONE"},
		{"Agreement", HSTYPE_AGREEMENT, "AGREEMENT"},
		{"Conclusion", HSTYPE_CONCLUSION, "CONCLUSION"},
		{"Wavehand", HSTYPE_WAVEHAND, "WAVEHAND"},
		{"Induction", HSTYPE_INDUCTION, "INDUCTION"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.ht.String()
			require.Equal(t, tc.expected, result)
		})
	}
}

// TestHandshakeType_IsHandshake tests HandshakeType.IsHandshake() method
func TestHandshakeType_IsHandshake(t *testing.T) {
	tests := []struct {
		name        string
		ht          HandshakeType
		isHandshake bool
	}{
		{"Done", HSTYPE_DONE, true},
		{"Agreement", HSTYPE_AGREEMENT, true},
		{"Conclusion", HSTYPE_CONCLUSION, true},
		{"Wavehand", HSTYPE_WAVEHAND, true},
		{"Induction", HSTYPE_INDUCTION, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.ht.IsHandshake()
			require.Equal(t, tc.isHandshake, result)
		})
	}
}

// TestHandshakeType_IsRejection tests HandshakeType.IsRejection() method
func TestHandshakeType_IsRejection(t *testing.T) {
	tests := []struct {
		name        string
		ht          HandshakeType
		isRejection bool
	}{
		{"Done_not_rejection", HSTYPE_DONE, false},
		{"Unknown_rejection", 1000, true}, // Unknown types are rejections
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.ht.IsRejection()
			require.Equal(t, tc.isRejection, result)
		})
	}
}

// TestPacketPosition_String tests PacketPosition.String() method
func TestPacketPosition_String(t *testing.T) {
	tests := []struct {
		name     string
		pp       PacketPosition
		expected string
	}{
		{"Middle", MiddlePacket, "middle"},
		{"Last", LastPacket, "last"},
		{"First", FirstPacket, "first"},
		{"Single", SinglePacket, "single"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.pp.String()
			require.Equal(t, tc.expected, result)
		})
	}
}

// TestPacketPosition_IsValid tests PacketPosition.IsValid() method
func TestPacketPosition_IsValid(t *testing.T) {
	tests := []struct {
		name    string
		pp      PacketPosition
		isValid bool
	}{
		{"Middle", MiddlePacket, true},
		{"Last", LastPacket, true},
		{"First", FirstPacket, true},
		{"Single", SinglePacket, true},
		{"Invalid", PacketPosition(10), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.pp.IsValid()
			require.Equal(t, tc.isValid, result)
		})
	}
}

// TestPacket_Clone tests Packet.Clone() method
func TestPacket_Clone(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	tests := []struct {
		name string
		data []byte
	}{
		{"Empty", nil},
		{"SmallData", []byte("hello")},
		{"LargeData", make([]byte, 1000)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPacket(addr)
			if tc.data != nil {
				p.SetData(tc.data)
			}

			clone := p.Clone()

			// Verify clone has same data length
			require.Equal(t, len(p.Data()), len(clone.Data()))
		})
	}
}

// TestPacket_ClearRecvBuffer tests Packet.ClearRecvBuffer() method
func TestPacket_ClearRecvBuffer(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)
	p.SetData([]byte("test data"))

	// Clear should not panic
	p.ClearRecvBuffer()

	// Packet should still be usable
	require.NotNil(t, p.Header())
}

// TestPacket_Dump tests Packet.Dump() method (for debugging)
func TestPacket_Dump(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)
	p.SetData([]byte("test"))

	// Dump should not panic
	dump := p.Dump()
	require.NotEmpty(t, dump)
}

// ═══════════════════════════════════════════════════════════════════════════
// Control Packet Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestControlPacketTypes(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	tests := []struct {
		name       string
		ctrlType   CtrlType
		expectCtrl bool
	}{
		{"ACK", CTRLTYPE_ACK, true},
		{"NAK", CTRLTYPE_NAK, true},
		{"ACKACK", CTRLTYPE_ACKACK, true},
		{"Handshake", CTRLTYPE_HANDSHAKE, true},
		{"Shutdown", CTRLTYPE_SHUTDOWN, true},
		{"KeepAlive", CTRLTYPE_KEEPALIVE, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPacket(addr)
			p.Header().IsControlPacket = tc.expectCtrl
			p.Header().ControlType = tc.ctrlType

			require.Equal(t, tc.expectCtrl, p.Header().IsControlPacket)
			require.Equal(t, tc.ctrlType, p.Header().ControlType)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Data Packet Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestDataPacketPosition(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	tests := []struct {
		name     string
		position PacketPosition
	}{
		{"First", FirstPacket},
		{"Middle", MiddlePacket},
		{"Last", LastPacket},
		{"Single", SinglePacket},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewPacket(addr)
			p.Header().IsControlPacket = false
			p.Header().PacketPositionFlag = tc.position

			require.Equal(t, tc.position, p.Header().PacketPositionFlag)
		})
	}
}
