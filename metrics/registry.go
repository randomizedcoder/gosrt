package metrics

import (
	"sync"
)

// MetricsRegistry holds all connection metrics
type MetricsRegistry struct {
	connections map[uint32]*ConnectionMetrics
	mu          sync.RWMutex
}

var globalRegistry = &MetricsRegistry{
	connections: make(map[uint32]*ConnectionMetrics),
}

// globalListenerMetrics holds listener-level metrics (not per-connection).
// This is a singleton since we track aggregate listener behavior.
var globalListenerMetrics = NewListenerMetrics()

// GetListenerMetrics returns the global listener metrics instance.
// This is safe to call concurrently - all fields are atomic.
func GetListenerMetrics() *ListenerMetrics {
	return globalListenerMetrics
}

// RegisterConnection registers a connection's metrics
func RegisterConnection(socketId uint32, metrics *ConnectionMetrics) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.connections[socketId] = metrics
}

// UnregisterConnection removes a connection's metrics
func UnregisterConnection(socketId uint32) {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	delete(globalRegistry.connections, socketId)
}

// GetConnections returns a snapshot of all registered connections
// This is safe to call concurrently
func GetConnections() (map[uint32]*ConnectionMetrics, []uint32) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	connections := make(map[uint32]*ConnectionMetrics, len(globalRegistry.connections))
	socketIds := make([]uint32, 0, len(globalRegistry.connections))

	for socketId, metrics := range globalRegistry.connections {
		connections[socketId] = metrics
		socketIds = append(socketIds, socketId)
	}

	return connections, socketIds
}

