package metrics

import (
	"sync"
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

// MetricsRegistry holds all connection metrics
type MetricsRegistry struct {
	connections   map[uint32]*ConnectionMetrics
	instanceNames map[uint32]string // socketId -> instance name
	mu            sync.RWMutex
}

var globalRegistry = &MetricsRegistry{
	connections:   make(map[uint32]*ConnectionMetrics),
	instanceNames: make(map[uint32]string),
}

// globalListenerMetrics holds listener-level metrics (not per-connection).
// This is a singleton since we track aggregate listener behavior.
var globalListenerMetrics = NewListenerMetrics()

// GetListenerMetrics returns the global listener metrics instance.
// This is safe to call concurrently - all fields are atomic.
func GetListenerMetrics() *ListenerMetrics {
	return globalListenerMetrics
}

// RegisterConnection registers a connection's metrics with an optional instance name
// The instance name is used as a label in Prometheus metrics output
func RegisterConnection(socketId uint32, metrics *ConnectionMetrics, instanceName string) {
	globalRegistry.mu.Lock()
	globalRegistry.connections[socketId] = metrics
	globalRegistry.instanceNames[socketId] = instanceName
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
	delete(globalRegistry.instanceNames, socketId)
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

// GetConnections returns a snapshot of all registered connections
// Returns: connections map, socket IDs list, and instance names map
// This is safe to call concurrently
func GetConnections() (map[uint32]*ConnectionMetrics, []uint32, map[uint32]string) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	connections := make(map[uint32]*ConnectionMetrics, len(globalRegistry.connections))
	socketIds := make([]uint32, 0, len(globalRegistry.connections))
	instanceNames := make(map[uint32]string, len(globalRegistry.instanceNames))

	for socketId, metrics := range globalRegistry.connections {
		connections[socketId] = metrics
		socketIds = append(socketIds, socketId)
	}

	for socketId, name := range globalRegistry.instanceNames {
		instanceNames[socketId] = name
	}

	return connections, socketIds, instanceNames
}
