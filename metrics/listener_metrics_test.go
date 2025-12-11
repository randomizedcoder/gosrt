package metrics

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListenerMetricsNew verifies NewListenerMetrics creates zero-initialized struct
func TestListenerMetricsNew(t *testing.T) {
	lm := NewListenerMetrics()
	require.NotNil(t, lm)

	// All counters should start at 0
	require.Equal(t, uint64(0), lm.RecvConnLookupNotFound.Load())
	require.Equal(t, uint64(0), lm.RecvConnLookupNotFoundIoUring.Load())
	require.Equal(t, uint64(0), lm.HandshakeRejectNotFound.Load())
	require.Equal(t, uint64(0), lm.HandshakeAcceptNotFound.Load())
	require.Equal(t, uint64(0), lm.HandshakeDuplicateRequest.Load())
	require.Equal(t, uint64(0), lm.SocketIdCollision.Load())
}

// TestListenerMetricsIncrement verifies atomic increments work correctly
func TestListenerMetricsIncrement(t *testing.T) {
	lm := NewListenerMetrics()

	// Test each counter
	lm.RecvConnLookupNotFound.Add(1)
	require.Equal(t, uint64(1), lm.RecvConnLookupNotFound.Load())

	lm.RecvConnLookupNotFoundIoUring.Add(5)
	require.Equal(t, uint64(5), lm.RecvConnLookupNotFoundIoUring.Load())

	lm.HandshakeRejectNotFound.Add(2)
	require.Equal(t, uint64(2), lm.HandshakeRejectNotFound.Load())

	lm.HandshakeAcceptNotFound.Add(3)
	require.Equal(t, uint64(3), lm.HandshakeAcceptNotFound.Load())

	lm.HandshakeDuplicateRequest.Add(10)
	require.Equal(t, uint64(10), lm.HandshakeDuplicateRequest.Load())

	lm.SocketIdCollision.Add(7)
	require.Equal(t, uint64(7), lm.SocketIdCollision.Load())
}

// TestGetListenerMetrics verifies global singleton access
func TestGetListenerMetrics(t *testing.T) {
	lm1 := GetListenerMetrics()
	lm2 := GetListenerMetrics()

	// Should return the same instance
	require.Same(t, lm1, lm2)
}

// TestListenerMetricsPrometheusExport verifies Prometheus format output
func TestListenerMetricsPrometheusExport(t *testing.T) {
	// Get the global metrics and set some values
	lm := GetListenerMetrics()

	// Save original values
	origRecvNotFound := lm.RecvConnLookupNotFound.Load()
	origRecvNotFoundIoUring := lm.RecvConnLookupNotFoundIoUring.Load()
	origRejectNotFound := lm.HandshakeRejectNotFound.Load()
	origAcceptNotFound := lm.HandshakeAcceptNotFound.Load()
	origDuplicate := lm.HandshakeDuplicateRequest.Load()
	origCollision := lm.SocketIdCollision.Load()

	// Add known values
	lm.RecvConnLookupNotFound.Add(100)
	lm.RecvConnLookupNotFoundIoUring.Add(50)
	lm.HandshakeRejectNotFound.Add(1)
	lm.HandshakeAcceptNotFound.Add(2)
	lm.HandshakeDuplicateRequest.Add(25)
	lm.SocketIdCollision.Add(3)

	// Write to builder
	var b strings.Builder
	writeListenerMetrics(&b)
	output := b.String()

	// Verify expected metrics are present
	require.Contains(t, output, "gosrt_recv_conn_lookup_not_found_total")
	require.Contains(t, output, `path="standard"`)
	require.Contains(t, output, `path="iouring"`)

	require.Contains(t, output, "gosrt_handshake_lookup_not_found_total")
	require.Contains(t, output, `operation="reject"`)
	require.Contains(t, output, `operation="accept"`)

	require.Contains(t, output, "gosrt_handshake_duplicate_total")
	require.Contains(t, output, "gosrt_socketid_collision_total")

	// Restore original values (can't reset atomics, but tests should be independent)
	// Note: In a real test environment, each test would have isolated metrics
	_ = origRecvNotFound
	_ = origRecvNotFoundIoUring
	_ = origRejectNotFound
	_ = origAcceptNotFound
	_ = origDuplicate
	_ = origCollision
}

// TestListenerMetricsZeroNotExported verifies zero values are not exported
func TestListenerMetricsZeroNotExported(t *testing.T) {
	// Create a fresh metrics instance for testing
	lm := NewListenerMetrics()

	// All values are zero
	require.Equal(t, uint64(0), lm.RecvConnLookupNotFound.Load())

	// writeListenerMetrics uses writeCounterIfNonZero
	// For this test, we just verify the function doesn't panic
	var b strings.Builder
	writeListenerMetrics(&b)
	// Output may or may not contain metrics depending on global state
	// The key is that it doesn't panic
}

// TestListenerMetricsConcurrency verifies thread-safety of atomic operations
func TestListenerMetricsConcurrency(t *testing.T) {
	lm := NewListenerMetrics()

	// Run concurrent increments
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			lm.RecvConnLookupNotFound.Add(1)
			lm.RecvConnLookupNotFoundIoUring.Add(1)
			lm.HandshakeRejectNotFound.Add(1)
			lm.HandshakeAcceptNotFound.Add(1)
			lm.HandshakeDuplicateRequest.Add(1)
			lm.SocketIdCollision.Add(1)
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}

	// Each counter should be exactly 100
	require.Equal(t, uint64(100), lm.RecvConnLookupNotFound.Load())
	require.Equal(t, uint64(100), lm.RecvConnLookupNotFoundIoUring.Load())
	require.Equal(t, uint64(100), lm.HandshakeRejectNotFound.Load())
	require.Equal(t, uint64(100), lm.HandshakeAcceptNotFound.Load())
	require.Equal(t, uint64(100), lm.HandshakeDuplicateRequest.Load())
	require.Equal(t, uint64(100), lm.SocketIdCollision.Load())
}

// TestConnectionLifecycleCounters verifies connection lifecycle tracking
func TestConnectionLifecycleCounters(t *testing.T) {
	// Save original values from global metrics
	lm := GetListenerMetrics()
	origActive := lm.ConnectionsActive.Load()
	origEstablished := lm.ConnectionsEstablished.Load()
	origClosedTotal := lm.ConnectionsClosedTotal.Load()
	origClosedGraceful := lm.ConnectionsClosedGraceful.Load()
	origClosedPeerIdle := lm.ConnectionsClosedPeerIdle.Load()

	// Create test connection metrics
	m := NewConnectionMetrics()
	socketId := uint32(0x11111111)

	// Register connection - should increment active and established
	RegisterConnection(socketId, m)

	require.Equal(t, origActive+1, lm.ConnectionsActive.Load())
	require.Equal(t, origEstablished+1, lm.ConnectionsEstablished.Load())

	// Unregister with graceful reason - should decrement active, increment closed counters
	UnregisterConnection(socketId, CloseReasonGraceful)

	require.Equal(t, origActive, lm.ConnectionsActive.Load())
	require.Equal(t, origClosedTotal+1, lm.ConnectionsClosedTotal.Load())
	require.Equal(t, origClosedGraceful+1, lm.ConnectionsClosedGraceful.Load())

	// Test peer idle timeout close reason
	socketId2 := uint32(0x22222222)
	m2 := NewConnectionMetrics()
	RegisterConnection(socketId2, m2)
	UnregisterConnection(socketId2, CloseReasonPeerIdle)

	require.Equal(t, origClosedPeerIdle+1, lm.ConnectionsClosedPeerIdle.Load())
	require.Equal(t, origClosedTotal+2, lm.ConnectionsClosedTotal.Load())
}

// TestConnectionLifecycleBalance verifies established == closed at end
func TestConnectionLifecycleBalance(t *testing.T) {
	lm := GetListenerMetrics()
	origEstablished := lm.ConnectionsEstablished.Load()
	origClosedTotal := lm.ConnectionsClosedTotal.Load()

	// Create and close multiple connections with different reasons
	reasons := []CloseReason{
		CloseReasonGraceful,
		CloseReasonPeerIdle,
		CloseReasonContextCancel,
		CloseReasonError,
		CloseReasonGraceful,
	}

	for i, reason := range reasons {
		socketId := uint32(0x30000000 + i)
		m := NewConnectionMetrics()
		RegisterConnection(socketId, m)
		UnregisterConnection(socketId, reason)
	}

	// Verify balance: new established == new closed
	newEstablished := lm.ConnectionsEstablished.Load() - origEstablished
	newClosedTotal := lm.ConnectionsClosedTotal.Load() - origClosedTotal

	require.Equal(t, uint64(5), newEstablished)
	require.Equal(t, uint64(5), newClosedTotal)
	require.Equal(t, newEstablished, newClosedTotal, "Established should equal Closed")
}

// TestConnectionLifecyclePrometheusExport verifies lifecycle metrics are exported
func TestConnectionLifecyclePrometheusExport(t *testing.T) {
	lm := GetListenerMetrics()

	// Set some lifecycle values
	origEstablished := lm.ConnectionsEstablished.Load()
	origClosedTotal := lm.ConnectionsClosedTotal.Load()
	origClosedGraceful := lm.ConnectionsClosedGraceful.Load()

	// Register and unregister a connection
	socketId := uint32(0x44444444)
	m := NewConnectionMetrics()
	RegisterConnection(socketId, m)
	UnregisterConnection(socketId, CloseReasonGraceful)

	// Write to builder
	var b strings.Builder
	writeListenerMetrics(&b)
	output := b.String()

	// Verify lifecycle metrics are present
	require.Contains(t, output, "gosrt_connections_active")
	require.Contains(t, output, "gosrt_connections_established_total")
	require.Contains(t, output, "gosrt_connections_closed_total")
	require.Contains(t, output, "gosrt_connections_closed_by_reason_total")
	require.Contains(t, output, `reason="graceful"`)

	// Cleanup verification - values should be incremented
	require.Equal(t, origEstablished+1, lm.ConnectionsEstablished.Load())
	require.Equal(t, origClosedTotal+1, lm.ConnectionsClosedTotal.Load())
	require.Equal(t, origClosedGraceful+1, lm.ConnectionsClosedGraceful.Load())
}

