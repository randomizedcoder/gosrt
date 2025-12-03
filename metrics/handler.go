package metrics

import (
	"fmt"
	"net/http"
	"strings"
)

// MetricsHandler returns an HTTP handler that serves Prometheus-formatted metrics
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")

		// Get strings.Builder from pool
		b := metricsBuilderPool.Get().(*strings.Builder)
		defer func() {
			// Reset and return to pool
			b.Reset()
			// Keep the grown capacity (don't shrink)
			metricsBuilderPool.Put(b)
		}()

		// Write Go runtime metrics first (standard metrics, compatible with prometheus/client_golang)
		writeRuntimeMetrics(b)

		// Write application-specific metrics
		connections, socketIds := GetConnections()

		// Write metrics for each connection
		for _, socketId := range socketIds {
			metrics := connections[socketId]
			if metrics == nil {
				continue
			}

			socketIdStr := fmt.Sprintf("0x%08x", socketId)

			// Packet counters - ACK
			writeCounterValue(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvACKSuccess.Load(),
				"socket_id", socketIdStr, "type", "ack", "path", "iouring")
			writeCounterValue(b, "gosrt_connection_packets_dropped_total",
				metrics.PktRecvACKDropped.Load(),
				"socket_id", socketIdStr, "type", "ack", "reason", "parse_error")

			// ... (similar for all metrics - will be expanded as we add more)

			// Lock timing (average and max)
			if metrics.HandlePacketLockTiming != nil {
				holdAvg, holdMax, waitAvg, waitMax := metrics.HandlePacketLockTiming.GetStats()
				writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
					holdAvg, "socket_id", socketIdStr, "lock", "handle_packet")
				writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
					holdMax, "socket_id", socketIdStr, "lock", "handle_packet")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
					waitAvg, "socket_id", socketIdStr, "lock", "handle_packet")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
					waitMax, "socket_id", socketIdStr, "lock", "handle_packet")

				writeCounterValue(b, "gosrt_connection_lock_acquisitions_total",
					metrics.HandlePacketLockTiming.GetTotalAcquisitions(),
					"socket_id", socketIdStr, "lock", "handle_packet")
			}
		}

		w.Write([]byte(b.String()))
	})
}

