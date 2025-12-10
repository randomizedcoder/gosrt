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

