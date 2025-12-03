package metrics

import (
	"github.com/datarhei/gosrt/packet"
)

// IncrementRecvMetrics increments the appropriate receive metrics based on packet type and outcome
// This is a helper function to reduce code duplication in receive paths
// Exported for use in receive paths (listen_linux.go, dial_linux.go, etc.)
func IncrementRecvMetrics(m *ConnectionMetrics, p packet.Packet, isIoUring bool, success bool, dropReason string) {
	if m == nil {
		return
	}

	// Track path
	if isIoUring {
		m.PktRecvIoUring.Add(1)
	} else {
		m.PktRecvReadFrom.Add(1)
	}

	if p == nil {
		// No packet - can't classify type, but track error
		if !success {
			m.PktRecvErrorParse.Add(1)
		}
		return
	}

	h := p.Header()
	pktLen := uint64(p.Len())

	if !success {
		// Track error/drop based on reason
		switch dropReason {
		case "parse":
			m.PktRecvErrorParse.Add(1)
		case "route":
			m.PktRecvErrorRoute.Add(1)
		case "empty":
			m.PktRecvErrorEmpty.Add(1)
		case "unknown_socket":
			m.PktRecvUnknownSocketId.Add(1)
		case "nil_connection":
			m.PktRecvNilConnection.Add(1)
		case "wrong_peer":
			m.PktRecvWrongPeer.Add(1)
		case "backlog_full":
			m.PktRecvBacklogFull.Add(1)
		case "queue_full":
			m.PktRecvQueueFull.Add(1)
		default:
			// Generic error
			m.PktRecvErrorParse.Add(1)
		}
		return
	}

	// Success case - classify by packet type
	if h.IsControlPacket {
		switch h.ControlType {
		case packet.CTRLTYPE_ACK:
			m.PktRecvACKSuccess.Add(1)
			m.ByteRecvDataSuccess.Add(pktLen) // ACK packets have data too
		case packet.CTRLTYPE_ACKACK:
			m.PktRecvACKACKSuccess.Add(1)
		case packet.CTRLTYPE_NAK:
			m.PktRecvNAKSuccess.Add(1)
		case packet.CTRLTYPE_KEEPALIVE:
			m.PktRecvKeepaliveSuccess.Add(1)
		case packet.CTRLTYPE_SHUTDOWN:
			m.PktRecvShutdownSuccess.Add(1)
		case packet.CTRLTYPE_HANDSHAKE:
			m.PktRecvHandshakeSuccess.Add(1)
		case packet.CTRLTYPE_USER:
			// USER packets can be KM (key material) - check SubType
			switch h.SubType {
			case packet.EXTTYPE_KMREQ, packet.EXTTYPE_KMRSP:
				m.PktRecvKMSuccess.Add(1)
			default:
				// Other USER subtypes - count as handshake for now
				m.PktRecvHandshakeSuccess.Add(1)
			}
		default:
			// Unknown control type - count as generic success
			m.PktRecvHandshakeSuccess.Add(1) // Use handshake as catch-all
		}
	} else {
		// Data packet
		m.PktRecvDataSuccess.Add(1)
		m.ByteRecvDataSuccess.Add(pktLen)
	}
}

// IncrementRecvErrorMetrics increments error metrics for cases where we don't have a packet
// Exported for use in receive paths
func IncrementRecvErrorMetrics(m *ConnectionMetrics, isIoUring bool, errorType string) {
	if m == nil {
		return
	}

	// Track path
	if isIoUring {
		m.PktRecvIoUring.Add(1)
		m.PktRecvErrorIoUring.Add(1)
	} else {
		m.PktRecvReadFrom.Add(1)
	}

	// Track error type
	switch errorType {
	case "parse":
		m.PktRecvErrorParse.Add(1)
	case "empty":
		m.PktRecvErrorEmpty.Add(1)
	case "iouring":
		m.PktRecvErrorIoUring.Add(1)
	default:
		m.PktRecvErrorParse.Add(1)
	}
}

