package metrics

import (
	"sync"
	"time"
)

// CloseReason indicates why a connection was closed.
// Used for tracking connection lifecycle in metrics.
type CloseReason int

const (
	CloseReasonGraceful      CloseReason = iota // Normal shutdown (Close() called)
	CloseReasonPeerIdle                         // Peer idle timeout expired
	CloseReasonContextCancel                    // Parent context cancelled
	CloseReasonError                            // Error during operation
)

// ConnectionInfo holds metadata for a registered connection.
// This enables richer Prometheus labels for connection identification.
type ConnectionInfo struct {
	Metrics      *ConnectionMetrics
	InstanceName string    // Instance name for Prometheus labeling (e.g., "baseline-server")
	RemoteAddr   string    // Remote IP:port (e.g., "10.1.1.2:45678")
	StreamId     string    // Stream ID (e.g., "publish:/test-stream")
	PeerType     string    // "publisher", "subscriber", or "unknown"
	PeerSocketID uint32    // Remote peer's socket ID (for cross-process connection correlation)
	StartTime    time.Time // When connection was established (for age metrics)
}

// MetricsRegistry holds all connection metrics
type MetricsRegistry struct {
	connections map[uint32]*ConnectionInfo
	mu          sync.RWMutex
}

var globalRegistry = &MetricsRegistry{
	connections: make(map[uint32]*ConnectionInfo),
}

// globalListenerMetrics holds listener-level metrics (not per-connection).
// This is a singleton since we track aggregate listener behavior.
var globalListenerMetrics = NewListenerMetrics()

// GetListenerMetrics returns the global listener metrics instance.
// This is safe to call concurrently - all fields are atomic.
func GetListenerMetrics() *ListenerMetrics {
	return globalListenerMetrics
}

// RegisterConnection registers a connection's metrics with metadata.
// The ConnectionInfo contains all information needed for Prometheus labeling.
func RegisterConnection(socketId uint32, info *ConnectionInfo) {
	globalRegistry.mu.Lock()
	globalRegistry.connections[socketId] = info
	globalRegistry.mu.Unlock()

	// Increment lifecycle counters (lock-free atomics)
	globalListenerMetrics.ConnectionsActive.Add(1)
	globalListenerMetrics.ConnectionsEstablished.Add(1)
}

// UnregisterConnection removes a connection's metrics and increments the
// appropriate close counter based on the reason.
func UnregisterConnection(socketId uint32, reason CloseReason) {
	globalRegistry.mu.Lock()
	delete(globalRegistry.connections, socketId)
	globalRegistry.mu.Unlock()

	// Decrement active count and increment close counters (lock-free atomics)
	globalListenerMetrics.ConnectionsActive.Add(-1)
	globalListenerMetrics.ConnectionsClosedTotal.Add(1)

	switch reason {
	case CloseReasonGraceful:
		globalListenerMetrics.ConnectionsClosedGraceful.Add(1)
	case CloseReasonPeerIdle:
		globalListenerMetrics.ConnectionsClosedPeerIdle.Add(1)
	case CloseReasonContextCancel:
		globalListenerMetrics.ConnectionsClosedContextCancel.Add(1)
	case CloseReasonError:
		globalListenerMetrics.ConnectionsClosedError.Add(1)
	}
}

// GetConnections returns a snapshot of all registered connections with their metadata.
// Returns: map of socketId -> ConnectionInfo (contains metrics + metadata)
// This is safe to call concurrently (uses RLock for concurrent reads).
func GetConnections() map[uint32]*ConnectionInfo {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	connections := make(map[uint32]*ConnectionInfo, len(globalRegistry.connections))
	for socketId, info := range globalRegistry.connections {
		connections[socketId] = info
	}

	return connections
}
