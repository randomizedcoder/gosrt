package metrics

import (
	"github.com/datarhei/gosrt/packet"
)

// IncrementRecvMetrics increments the appropriate receive metrics based on packet type and outcome
// This is a helper function to reduce code duplication in receive paths
// Exported for use in receive paths (listen_linux.go, dial_linux.go, etc.)
// dropReason should be DropReason(0) for success cases
func IncrementRecvMetrics(m *ConnectionMetrics, p packet.Packet, isIoUring bool, success bool, dropReason DropReason) {
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
		// Track nil packet edge case (should never happen, but defensive programming)
		m.PktRecvNil.Add(1)

		// No packet - can't classify type, but track error
		// For parse errors, we typically don't have packet info
		if !success {
			// If we have a specific drop reason, use it; otherwise default to parse error
			if dropReason == DropReasonParse {
				m.PktRecvErrorParse.Add(1)
			} else if dropReason != 0 {
				// Unknown drop reason when we have no packet
				m.PktRecvErrorUnknown.Add(1)
			} else {
				// No drop reason specified - assume parse error (most common when p == nil)
				m.PktRecvErrorParse.Add(1)
			}
		}
		return
	}

	h := p.Header()
	pktLen := uint64(p.Len())

	if !success {
		// Track error/drop using granular counters
		// We have packet (already checked above) - use granular error drop counter
		isData := !h.IsControlPacket
		IncrementRecvErrorDrop(m, p, dropReason, isData)
		// Also track legacy counters for backward compatibility
		switch dropReason {
		case DropReasonParse:
			m.PktRecvErrorParse.Add(1)
		case DropReasonRoute:
			m.PktRecvErrorRoute.Add(1)
		case DropReasonEmpty:
			m.PktRecvErrorEmpty.Add(1)
		case DropReasonUnknownSocket:
			m.PktRecvUnknownSocketId.Add(1)
		case DropReasonNilConnection:
			m.PktRecvNilConnection.Add(1)
		case DropReasonWrongPeer:
			m.PktRecvWrongPeer.Add(1)
		case DropReasonBacklogFull:
			m.PktRecvBacklogFull.Add(1)
		case DropReasonQueueFull:
			m.PktRecvQueueFull.Add(1)
		default:
			// Unknown drop reason - track as unknown error
			m.PktRecvErrorUnknown.Add(1)
		}
		return
	}

	// Success case - increment single success counter (for peer idle timeout)
	// This is done immediately after the !success check for performance
	m.PktRecvSuccess.Add(1)

	// Classify by packet type (for detailed metrics)
	// Handle data packets first (early return) to reduce nesting
	if !h.IsControlPacket {
		// Data packet
		m.PktRecvDataSuccess.Add(1)
		m.ByteRecvDataSuccess.Add(pktLen)
		return
	}

	// Control packet - switch on control type
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
			// Unknown USER subtype - track separately (should never happen, but defensive programming)
			m.PktRecvSubTypeUnknown.Add(1)
		}
	default:
		// Unknown control type - track separately (should never happen, but defensive programming)
		m.PktRecvControlUnknown.Add(1)
	}
}

// IncrementRecvErrorMetrics increments error metrics for cases where we don't have a packet
// Exported for use in receive paths
func IncrementRecvErrorMetrics(m *ConnectionMetrics, isIoUring bool, errorType DropReason) {
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
	case DropReasonParse:
		m.PktRecvErrorParse.Add(1)
	case DropReasonEmpty:
		m.PktRecvErrorEmpty.Add(1)
	case DropReasonIoUring:
		m.PktRecvErrorIoUring.Add(1)
	default:
		m.PktRecvErrorUnknown.Add(1)
	}
}

// IncrementSendMetrics increments the appropriate send metrics based on packet type and outcome
// This is a helper function to reduce code duplication in send paths
// Exported for use in send paths (connection_linux.go, connection.go, etc.)
// dropReason should be DropReason(0) for success cases
func IncrementSendMetrics(m *ConnectionMetrics, p packet.Packet, isIoUring bool, success bool, dropReason DropReason) {
	if m == nil {
		return
	}

	// Track path
	if isIoUring {
		m.PktSentIoUring.Add(1)
	} else {
		m.PktSentWriteTo.Add(1)
	}

	if p == nil {
		// No packet - can't classify type, but track error
		if !success {
			// If we have a specific drop reason, use it; otherwise default to marshal error
			if dropReason == DropReasonMarshal {
				m.PktSentErrorMarshal.Add(1)
			} else if dropReason != 0 {
				// Unknown drop reason when we have no packet
				m.PktSentErrorUnknown.Add(1)
			} else {
				// No drop reason specified - assume marshal error (most common when p == nil)
				m.PktSentErrorMarshal.Add(1)
			}
		}
		return
	}

	h := p.Header()
	pktLen := uint64(p.Len())

	if !success {
		// Track error/drop using granular counters
		// We have packet (already checked above) - use granular error drop counter
		IncrementSendErrorDrop(m, p, dropReason, pktLen)
		return
	}

	// Success case - classify by packet type
	if h.IsControlPacket {
		switch h.ControlType {
		case packet.CTRLTYPE_ACK:
			m.PktSentACKSuccess.Add(1)
		case packet.CTRLTYPE_ACKACK:
			m.PktSentACKACKSuccess.Add(1)
		case packet.CTRLTYPE_NAK:
			m.PktSentNAKSuccess.Add(1)
		case packet.CTRLTYPE_KEEPALIVE:
			m.PktSentKeepaliveSuccess.Add(1)
		case packet.CTRLTYPE_SHUTDOWN:
			m.PktSentShutdownSuccess.Add(1)
		case packet.CTRLTYPE_HANDSHAKE:
			m.PktSentHandshakeSuccess.Add(1)
		case packet.CTRLTYPE_USER:
			// USER packets can be KM (key material) - check SubType
			switch h.SubType {
			case packet.EXTTYPE_KMREQ, packet.EXTTYPE_KMRSP:
				m.PktSentKMSuccess.Add(1)
			default:
				// Other USER subtypes - count as handshake for now
				m.PktSentHandshakeSuccess.Add(1)
			}
		default:
			// Unknown control type - count as generic success
			m.PktSentHandshakeSuccess.Add(1) // Use handshake as catch-all
		}
	} else {
		// Data packet
		m.PktSentDataSuccess.Add(1)
		m.ByteSentDataSuccess.Add(pktLen)
	}
}

// IncrementSendControlMetric increments the appropriate send counter for a control packet type.
// This is used by the io_uring path where control packets are decommissioned before
// IncrementSendMetrics can classify them.
func IncrementSendControlMetric(m *ConnectionMetrics, controlType packet.CtrlType) {
	if m == nil {
		return
	}

	switch controlType {
	case packet.CTRLTYPE_ACK:
		m.PktSentACKSuccess.Add(1)
	case packet.CTRLTYPE_ACKACK:
		m.PktSentACKACKSuccess.Add(1)
	case packet.CTRLTYPE_NAK:
		m.PktSentNAKSuccess.Add(1)
	case packet.CTRLTYPE_KEEPALIVE:
		m.PktSentKeepaliveSuccess.Add(1)
	case packet.CTRLTYPE_SHUTDOWN:
		m.PktSentShutdownSuccess.Add(1)
	case packet.CTRLTYPE_HANDSHAKE:
		m.PktSentHandshakeSuccess.Add(1)
	case packet.CTRLTYPE_USER:
		// USER packets are typically KM - track as handshake for simplicity
		m.PktSentHandshakeSuccess.Add(1)
	default:
		// Unknown control type - count as generic success
		m.PktSentHandshakeSuccess.Add(1)
	}
}

// IncrementSendErrorMetrics increments error metrics for cases where we don't have a packet
// Exported for use in send paths
func IncrementSendErrorMetrics(m *ConnectionMetrics, isIoUring bool, errorType DropReason) {
	if m == nil {
		return
	}

	// Track path
	if isIoUring {
		m.PktSentIoUring.Add(1)
		m.PktSentErrorIoUring.Add(1)
	} else {
		m.PktSentWriteTo.Add(1)
	}

	// Track error type
	switch errorType {
	case DropReasonMarshal:
		m.PktSentErrorMarshal.Add(1)
	case DropReasonRingFull:
		m.PktSentRingFull.Add(1)
	case DropReasonSubmit:
		m.PktSentErrorSubmit.Add(1)
	case DropReasonIoUring:
		m.PktSentErrorIoUring.Add(1)
	default:
		m.PktSentErrorUnknown.Add(1)
	}
}
