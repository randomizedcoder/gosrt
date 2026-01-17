package metrics

import "sync/atomic"

// ListenerMetrics holds listener-level metrics (not per-connection).
// These track events that happen before a connection is established or
// after a connection is closed, where we cannot associate with a specific connection.
//
// Use cases:
// - Detect silent failures in map lookups (Bug 3 style issues)
// - Monitor for attacks or malformed packets targeting unknown connections
// - Track handshake anomalies
type ListenerMetrics struct {
	// === Receive Path Lookup Failures ===
	// These occur when a packet arrives for an unknown socket ID.
	// Normal during shutdown (connection closed before packet processed).
	// High counts during operation may indicate bugs or attacks.

	// RecvConnLookupNotFound increments when listen.go reader() receives a packet
	// with a DestinationSocketId that doesn't match any known connection.
	RecvConnLookupNotFound atomic.Uint64

	// RecvConnLookupNotFoundIoUring increments when listen_linux.go io_uring path
	// receives a packet with a socket ID that doesn't match any known connection.
	RecvConnLookupNotFoundIoUring atomic.Uint64

	// === Handshake Path Lookup Failures ===
	// These indicate programming errors - should never happen in correct code.

	// HandshakeRejectNotFound increments when Reject() is called on a connection
	// request that doesn't exist in connReqs map. This is a programming error.
	HandshakeRejectNotFound atomic.Uint64

	// HandshakeAcceptNotFound increments when Accept() is called on a connection
	// request that doesn't exist in connReqs map. This is a programming error.
	HandshakeAcceptNotFound atomic.Uint64

	// === Informational Counters ===
	// These are expected behavior, not errors, but useful for debugging.

	// HandshakeDuplicateRequest increments when a duplicate handshake request
	// is received (same SRTSocketId as an existing pending request).
	HandshakeDuplicateRequest atomic.Uint64

	// SocketIdCollision increments when generateSocketId() finds a randomly
	// generated socket ID is already in use in the conns map.
	SocketIdCollision atomic.Uint64

	// === Send Path Lookup Failures (Bug 3 Detection) ===
	// This counter detects the specific bug where listener.send() used the wrong
	// map lookup key (DestinationSocketId instead of local socketId).

	// SendConnLookupNotFound increments when listener.send() tries to look up
	// a connection by DestinationSocketId and fails. This indicates Bug 3.
	// Should always be 0 with the closure-based fix in place.
	SendConnLookupNotFound atomic.Uint64

	// === Connection Lifecycle Counters ===
	// Track connection establishment and closure for debugging and testing.
	// These help detect connection replacements during network impairment tests
	// (e.g., Starlink pattern causing unexpected reconnects).

	// ConnectionsActive is a gauge tracking current active connections.
	// Uses Int64 to support Add(-1) for decrements. Lock-free atomic.
	ConnectionsActive atomic.Int64

	// ConnectionsEstablished increments when RegisterConnection() is called.
	// This is the total count of connections established over the listener lifetime.
	ConnectionsEstablished atomic.Uint64

	// ConnectionsClosedTotal is the total count of all closed connections.
	// Should equal ConnectionsEstablished at test end (no leaks).
	ConnectionsClosedTotal atomic.Uint64

	// ConnectionsClosed tracks connections closed by reason.
	// These help identify unexpected connection terminations during tests.
	// Sum of all reasons should equal ConnectionsClosedTotal.
	ConnectionsClosedGraceful      atomic.Uint64 // Normal shutdown (Close() called)
	ConnectionsClosedPeerIdle      atomic.Uint64 // Peer idle timeout expired
	ConnectionsClosedContextCancel atomic.Uint64 // Parent context cancelled
	ConnectionsClosedError         atomic.Uint64 // Error during operation

	// ========================================================================
	// io_uring Metrics (Unified Per-Ring - Phase 5 Refactoring)
	// ========================================================================
	// ALWAYS uses per-ring array, even when ringCount=1.
	// This replaced legacy single-ring counters (see multi_iouring_design.md 5.12).

	// Per-ring metrics for listener recv path (listen_linux.go)
	IoUringRecvRingMetrics []*IoUringRingMetrics
	IoUringRecvRingCount   int // Number of listener recv rings (for gauge export)
}

// NewListenerMetrics creates a new ListenerMetrics instance with all counters at zero.
func NewListenerMetrics() *ListenerMetrics {
	return &ListenerMetrics{}
}

// InitListenerRecvRingMetrics initializes per-ring metrics for the listener.
// ALWAYS creates the per-ring array, even for ringCount=1 (unified approach).
// Call this from io_uring initialization. Safe to call multiple times.
func (lm *ListenerMetrics) InitListenerRecvRingMetrics(ringCount int) {
	if ringCount < 1 {
		ringCount = 1 // Minimum 1 ring
	}
	lm.IoUringRecvRingMetrics = NewIoUringRingMetrics(ringCount)
	lm.IoUringRecvRingCount = ringCount
}
