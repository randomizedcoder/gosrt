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

			// Single success counter (for peer idle timeout)
			writeCounterValue(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvSuccess.Load(),
				"socket_id", socketIdStr, "type", "all", "status", "success")

			// Edge case counters (should be 0, but track for debugging)
			writeCounterValue(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvNil.Load(),
				"socket_id", socketIdStr, "type", "nil", "status", "error")

			writeCounterValue(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvControlUnknown.Load(),
				"socket_id", socketIdStr, "type", "control_unknown", "status", "error")

			writeCounterValue(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvSubTypeUnknown.Load(),
				"socket_id", socketIdStr, "type", "subtype_unknown", "status", "error")

			// Crypto operation error counters
			writeCounterValue(b, "gosrt_connection_crypto_error_total",
				metrics.CryptoErrorEncrypt.Load(),
				"socket_id", socketIdStr, "operation", "encrypt")
			writeCounterValue(b, "gosrt_connection_crypto_error_total",
				metrics.CryptoErrorGenerateSEK.Load(),
				"socket_id", socketIdStr, "operation", "generate_sek")
			writeCounterValue(b, "gosrt_connection_crypto_error_total",
				metrics.CryptoErrorMarshalKM.Load(),
				"socket_id", socketIdStr, "operation", "marshal_km")

			// Packet counters - ACK
			writeCounterValue(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvACKSuccess.Load(),
				"socket_id", socketIdStr, "type", "ack", "path", "iouring")
			writeCounterValue(b, "gosrt_connection_packets_dropped_total",
				metrics.PktRecvACKDropped.Load(),
				"socket_id", socketIdStr, "type", "ack", "reason", "parse_error")

			// ... (similar for all metrics - will be expanded as we add more)

			// Lock timing (average and max) - handlePacketMutex
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

			// Lock timing - receiver.lock
			if metrics.ReceiverLockTiming != nil {
				holdAvg, holdMax, waitAvg, waitMax := metrics.ReceiverLockTiming.GetStats()
				writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
					holdAvg, "socket_id", socketIdStr, "lock", "receiver")
				writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
					holdMax, "socket_id", socketIdStr, "lock", "receiver")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
					waitAvg, "socket_id", socketIdStr, "lock", "receiver")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
					waitMax, "socket_id", socketIdStr, "lock", "receiver")

				writeCounterValue(b, "gosrt_connection_lock_acquisitions_total",
					metrics.ReceiverLockTiming.GetTotalAcquisitions(),
					"socket_id", socketIdStr, "lock", "receiver")
			}

			// Lock timing - sender.lock
			if metrics.SenderLockTiming != nil {
				holdAvg, holdMax, waitAvg, waitMax := metrics.SenderLockTiming.GetStats()
				writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
					holdAvg, "socket_id", socketIdStr, "lock", "sender")
				writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
					holdMax, "socket_id", socketIdStr, "lock", "sender")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
					waitAvg, "socket_id", socketIdStr, "lock", "sender")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
					waitMax, "socket_id", socketIdStr, "lock", "sender")

				writeCounterValue(b, "gosrt_connection_lock_acquisitions_total",
					metrics.SenderLockTiming.GetTotalAcquisitions(),
					"socket_id", socketIdStr, "lock", "sender")
			}

			// Granular drop counters - Congestion control receiver (DATA packets)
			writeCounterValue(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropTooOld.Load(),
				"socket_id", socketIdStr, "reason", "too_old")
			writeCounterValue(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropAlreadyAcked.Load(),
				"socket_id", socketIdStr, "reason", "already_acked")
			writeCounterValue(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropDuplicate.Load(),
				"socket_id", socketIdStr, "reason", "duplicate")
			writeCounterValue(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropStoreInsertFailed.Load(),
				"socket_id", socketIdStr, "reason", "store_insert_failed")

			// Granular drop counters - Congestion control sender (DATA packets)
			writeCounterValue(b, "gosrt_connection_congestion_send_data_drop_total",
				metrics.CongestionSendDataDropTooOld.Load(),
				"socket_id", socketIdStr, "reason", "too_old")

			// Granular error counters - Connection-level send (DATA packets)
			writeCounterValue(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataErrorMarshal.Load(),
				"socket_id", socketIdStr, "reason", "marshal")
			writeCounterValue(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataRingFull.Load(),
				"socket_id", socketIdStr, "reason", "ring_full")
			writeCounterValue(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataErrorSubmit.Load(),
				"socket_id", socketIdStr, "reason", "submit")
			writeCounterValue(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataErrorIoUring.Load(),
				"socket_id", socketIdStr, "reason", "iouring")

			// Granular error counters - Connection-level send (Control packets)
			writeCounterValue(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlErrorMarshal.Load(),
				"socket_id", socketIdStr, "reason", "marshal")
			writeCounterValue(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlRingFull.Load(),
				"socket_id", socketIdStr, "reason", "ring_full")
			writeCounterValue(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlErrorSubmit.Load(),
				"socket_id", socketIdStr, "reason", "submit")
			writeCounterValue(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlErrorIoUring.Load(),
				"socket_id", socketIdStr, "reason", "iouring")

			// Granular error counters - Connection-level receive (DATA packets)
			writeCounterValue(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorParse.Load(),
				"socket_id", socketIdStr, "type", "parse")
			writeCounterValue(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorIoUring.Load(),
				"socket_id", socketIdStr, "type", "iouring")
			writeCounterValue(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorEmpty.Load(),
				"socket_id", socketIdStr, "type", "empty")
			writeCounterValue(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorRoute.Load(),
				"socket_id", socketIdStr, "type", "route")

			// Granular error counters - Connection-level receive (Control packets)
			writeCounterValue(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorParse.Load(),
				"socket_id", socketIdStr, "type", "parse")
			writeCounterValue(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorIoUring.Load(),
				"socket_id", socketIdStr, "type", "iouring")
			writeCounterValue(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorEmpty.Load(),
				"socket_id", socketIdStr, "type", "empty")
			writeCounterValue(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorRoute.Load(),
				"socket_id", socketIdStr, "type", "route")

			// Path-specific counters - io_uring submissions (for detecting lost completions)
			writeCounterValue(b, "gosrt_connection_send_submitted_total",
				metrics.PktSentSubmitted.Load(),
				"socket_id", socketIdStr)
		}

		w.Write([]byte(b.String()))
	})
}
