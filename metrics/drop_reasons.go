package metrics

// DropReason represents the reason for dropping a packet
// Using an enum type instead of strings for better performance
type DropReason uint8

const (
	// Receiver drop reasons
	DropReasonTooOld DropReason = iota
	DropReasonAlreadyAcked
	DropReasonDuplicate
	DropReasonStoreInsertFailed

	// Sender drop reasons
	DropReasonTooOldSend // Same as TooOld but for sender path

	// Error drop reasons (both receive and send)
	DropReasonMarshal
	DropReasonRingFull
	DropReasonSubmit
	DropReasonIoUring
	DropReasonParse
	DropReasonRoute
	DropReasonEmpty
	DropReasonWrite         // Write error (generic I/O error)
	DropReasonWrongPeer     // Wrong peer address
	DropReasonUnknownSocket // Unknown socket ID
	DropReasonNilConnection // Nil connection
	DropReasonBacklogFull   // Handshake backlog full
	DropReasonQueueFull     // Receive queue full
)

// String returns the string representation of the drop reason (for Prometheus labels)
func (r DropReason) String() string {
	switch r {
	case DropReasonTooOld, DropReasonTooOldSend:
		return "too_old"
	case DropReasonAlreadyAcked:
		return "already_acked"
	case DropReasonDuplicate:
		return "duplicate"
	case DropReasonStoreInsertFailed:
		return "store_insert_failed"
	case DropReasonMarshal:
		return "marshal"
	case DropReasonRingFull:
		return "ring_full"
	case DropReasonSubmit:
		return "submit"
	case DropReasonIoUring:
		return "iouring"
	case DropReasonParse:
		return "parse"
	case DropReasonRoute:
		return "route"
	case DropReasonEmpty:
		return "empty"
	case DropReasonWrite:
		return "write"
	case DropReasonWrongPeer:
		return "wrong_peer"
	case DropReasonUnknownSocket:
		return "unknown_socket"
	case DropReasonNilConnection:
		return "nil_connection"
	case DropReasonBacklogFull:
		return "backlog_full"
	case DropReasonQueueFull:
		return "queue_full"
	default:
		return "unknown"
	}
}
