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

		// Write listener-level metrics (not per-connection)
		writeListenerMetrics(b)

		// Write application-specific metrics
		connections, socketIds, instanceNames := GetConnections()

		// Write metrics for each connection
		for _, socketId := range socketIds {
			metrics := connections[socketId]
			if metrics == nil {
				continue
			}

			socketIdStr := fmt.Sprintf("0x%08x", socketId)
			instanceName := instanceNames[socketId]
			if instanceName == "" {
				instanceName = "default"
			}

			// Single success counter (for peer idle timeout)
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "all", "status", "success")

			// Edge case counters (should be 0, but track for debugging)
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvNil.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "nil", "status", "error")

			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvControlUnknown.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "control_unknown", "status", "error")

			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvSubTypeUnknown.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "subtype_unknown", "status", "error")

			// Crypto operation error counters
			writeCounterIfNonZero(b, "gosrt_connection_crypto_error_total",
				metrics.CryptoErrorEncrypt.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "operation", "encrypt")
			writeCounterIfNonZero(b, "gosrt_connection_crypto_error_total",
				metrics.CryptoErrorGenerateSEK.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "operation", "generate_sek")
			writeCounterIfNonZero(b, "gosrt_connection_crypto_error_total",
				metrics.CryptoErrorMarshalKM.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "operation", "marshal_km")

			// ========== Control Packet Counters ==========
			// These metrics track all SRT control packet types for comprehensive visibility
			// Note: Dropped/Error counters for control packets are not implemented as they currently never fail

			// ACK packets - received/sent (success only)
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvACKSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "ack", "status", "success")
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentACKSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "ack", "status", "success")

			// ACKACK packets - received/sent (success only)
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvACKACKSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "ackack", "status", "success")
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentACKACKSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "ackack", "status", "success")

			// NAK packets - received/sent (CRITICAL for ARQ validation, success only)
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvNAKSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "nak", "status", "success")
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentNAKSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "nak", "status", "success")

			// DATA packets - received
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvDataSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "success")
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvDataDropped.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "dropped")
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvDataError.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "error")

			// DATA packets - sent
			// Note: PktSentDataDropped not implemented - send drops tracked via CongestionSendPktDrop
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentDataSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "success")
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentDataError.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "error")

			// Keepalive packets - received
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvKeepaliveSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "keepalive", "status", "success")

			// Keepalive packets - sent
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentKeepaliveSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "keepalive", "status", "success")

			// Shutdown packets - received
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvShutdownSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "shutdown", "status", "success")

			// Shutdown packets - sent
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentShutdownSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "shutdown", "status", "success")

			// Handshake packets - received
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvHandshakeSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "handshake", "status", "success")

			// Handshake packets - sent
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentHandshakeSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "handshake", "status", "success")

			// KM (Key Material) packets - received
			writeCounterIfNonZero(b, "gosrt_connection_packets_received_total",
				metrics.PktRecvKMSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "km", "status", "success")

			// KM (Key Material) packets - sent
			writeCounterIfNonZero(b, "gosrt_connection_packets_sent_total",
				metrics.PktSentKMSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "km", "status", "success")

			// ========== Retransmission Counters ==========
			// Direct retransmission counter from NAK handling
			writeCounterIfNonZero(b, "gosrt_connection_retransmissions_from_nak_total",
				metrics.PktRetransFromNAK.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Byte Counters ==========
			writeCounterIfNonZero(b, "gosrt_connection_bytes_received_total",
				metrics.ByteRecvDataSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "success")
			writeCounterIfNonZero(b, "gosrt_connection_bytes_received_total",
				metrics.ByteRecvDataDropped.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "dropped")
			writeCounterIfNonZero(b, "gosrt_connection_bytes_sent_total",
				metrics.ByteSentDataSuccess.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "type", "data", "status", "success")
			// ByteSentDataDropped not implemented - send drops tracked via CongestionSendByteDrop

			// Lock timing (average and max) - handlePacketMutex
			if metrics.HandlePacketLockTiming != nil {
				holdAvg, holdMax, waitAvg, waitMax := metrics.HandlePacketLockTiming.GetStats()
				writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
					holdAvg, "socket_id", socketIdStr, "instance", instanceName, "lock", "handle_packet")
				writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
					holdMax, "socket_id", socketIdStr, "instance", instanceName, "lock", "handle_packet")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
					waitAvg, "socket_id", socketIdStr, "instance", instanceName, "lock", "handle_packet")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
					waitMax, "socket_id", socketIdStr, "instance", instanceName, "lock", "handle_packet")

				writeCounterIfNonZero(b, "gosrt_connection_lock_acquisitions_total",
					metrics.HandlePacketLockTiming.GetTotalAcquisitions(),
					"socket_id", socketIdStr, "instance", instanceName, "lock", "handle_packet")
			}

			// Lock timing - receiver.lock
			if metrics.ReceiverLockTiming != nil {
				holdAvg, holdMax, waitAvg, waitMax := metrics.ReceiverLockTiming.GetStats()
				writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
					holdAvg, "socket_id", socketIdStr, "instance", instanceName, "lock", "receiver")
				writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
					holdMax, "socket_id", socketIdStr, "instance", instanceName, "lock", "receiver")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
					waitAvg, "socket_id", socketIdStr, "instance", instanceName, "lock", "receiver")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
					waitMax, "socket_id", socketIdStr, "instance", instanceName, "lock", "receiver")

				writeCounterIfNonZero(b, "gosrt_connection_lock_acquisitions_total",
					metrics.ReceiverLockTiming.GetTotalAcquisitions(),
					"socket_id", socketIdStr, "instance", instanceName, "lock", "receiver")
			}

			// Lock timing - sender.lock
			if metrics.SenderLockTiming != nil {
				holdAvg, holdMax, waitAvg, waitMax := metrics.SenderLockTiming.GetStats()
				writeGauge(b, "gosrt_connection_lock_hold_seconds_avg",
					holdAvg, "socket_id", socketIdStr, "instance", instanceName, "lock", "sender")
				writeGauge(b, "gosrt_connection_lock_hold_seconds_max",
					holdMax, "socket_id", socketIdStr, "instance", instanceName, "lock", "sender")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_avg",
					waitAvg, "socket_id", socketIdStr, "instance", instanceName, "lock", "sender")
				writeGauge(b, "gosrt_connection_lock_wait_seconds_max",
					waitMax, "socket_id", socketIdStr, "instance", instanceName, "lock", "sender")

				writeCounterIfNonZero(b, "gosrt_connection_lock_acquisitions_total",
					metrics.SenderLockTiming.GetTotalAcquisitions(),
					"socket_id", socketIdStr, "instance", instanceName, "lock", "sender")
			}

			// Granular drop counters - Congestion control receiver (DATA packets)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropTooOld.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "too_old")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropAlreadyAcked.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "already_acked")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropDuplicate.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "duplicate")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_recv_data_drop_total",
				metrics.CongestionRecvDataDropStoreInsertFailed.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "store_insert_failed")

			// Granular drop counters - Congestion control sender (DATA packets)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_send_data_drop_total",
				metrics.CongestionSendDataDropTooOld.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "too_old")

			// TSBPD skip counters - Packets that NEVER arrived and were skipped at ACK time
			writeCounterIfNonZero(b, "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total",
				metrics.CongestionRecvPktSkippedTSBPD.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_recv_byte_skipped_tsbpd_total",
				metrics.CongestionRecvByteSkippedTSBPD.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// Periodic timer tick counters - Health monitoring (expected: ACK ~100/sec, NAK ~50/sec)
			writeCounterIfNonZero(b, "gosrt_connection_periodic_ack_runs_total",
				metrics.CongestionRecvPeriodicACKRuns.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_connection_periodic_nak_runs_total",
				metrics.CongestionRecvPeriodicNAKRuns.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// Granular error counters - Connection-level send (DATA packets)
			writeCounterIfNonZero(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataErrorMarshal.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "marshal")
			writeCounterIfNonZero(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataRingFull.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "ring_full")
			writeCounterIfNonZero(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataErrorSubmit.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "submit")
			writeCounterIfNonZero(b, "gosrt_connection_send_data_drop_total",
				metrics.PktSentDataErrorIoUring.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "iouring")

			// Granular error counters - Connection-level send (Control packets)
			writeCounterIfNonZero(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlErrorMarshal.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "marshal")
			writeCounterIfNonZero(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlRingFull.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "ring_full")
			writeCounterIfNonZero(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlErrorSubmit.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "submit")
			writeCounterIfNonZero(b, "gosrt_connection_send_control_drop_total",
				metrics.PktSentControlErrorIoUring.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "reason", "iouring")

			// Granular error counters - Connection-level receive (DATA packets)
			writeCounterIfNonZero(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorParse.Load(),
				"socket_id", socketIdStr, "type", "parse")
			writeCounterIfNonZero(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorIoUring.Load(),
				"socket_id", socketIdStr, "type", "iouring")
			writeCounterIfNonZero(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorEmpty.Load(),
				"socket_id", socketIdStr, "type", "empty")
			writeCounterIfNonZero(b, "gosrt_connection_recv_data_error_total",
				metrics.PktRecvDataErrorRoute.Load(),
				"socket_id", socketIdStr, "type", "route")

			// Granular error counters - Connection-level receive (Control packets)
			writeCounterIfNonZero(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorParse.Load(),
				"socket_id", socketIdStr, "type", "parse")
			writeCounterIfNonZero(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorIoUring.Load(),
				"socket_id", socketIdStr, "type", "iouring")
			writeCounterIfNonZero(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorEmpty.Load(),
				"socket_id", socketIdStr, "type", "empty")
			writeCounterIfNonZero(b, "gosrt_connection_recv_control_error_total",
				metrics.PktRecvControlErrorRoute.Load(),
				"socket_id", socketIdStr, "type", "route")

			// Path-specific counters - io_uring submissions (for detecting lost completions)
			writeCounterIfNonZero(b, "gosrt_connection_send_submitted_total",
				metrics.PktSentSubmitted.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Congestion Control Statistics ==========
			// These metrics are critical for loss rate calculation and ARQ analysis

			// Packets sent/received by congestion control (for cross-endpoint loss calculation)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_total",
				metrics.CongestionSendPkt.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_total",
				metrics.CongestionRecvPkt.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")

			// Unique packets (excludes retransmissions on send, duplicates on recv)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_unique_total",
				metrics.CongestionSendPktUnique.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_unique_total",
				metrics.CongestionRecvPktUnique.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")

			// Packets lost (detected via sequence number gaps by receiver)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_lost_total",
				metrics.CongestionRecvPktLoss.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_lost_total",
				metrics.CongestionSendPktLoss.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")

			// Retransmissions (packets retransmitted by sender, retransmits received by receiver)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_retransmissions_total",
				metrics.CongestionSendPktRetrans.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_retransmissions_total",
				metrics.CongestionRecvPktRetrans.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")

			// Bytes sent/received (for throughput calculation)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_total",
				metrics.CongestionSendByte.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_total",
				metrics.CongestionRecvByte.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")

			// ========== io_uring and Low-Level Path Counters ==========
			// These track packets through specific I/O paths (useful for debugging)
			writeCounterIfNonZero(b, "gosrt_connection_io_path_total",
				metrics.PktRecvIoUring.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "path", "iouring")
			writeCounterIfNonZero(b, "gosrt_connection_io_path_total",
				metrics.PktSentIoUring.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "path", "iouring")
			writeCounterIfNonZero(b, "gosrt_connection_io_path_total",
				metrics.PktRecvReadFrom.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "path", "readfrom")
			writeCounterIfNonZero(b, "gosrt_connection_io_path_total",
				metrics.PktSentWriteTo.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "path", "writeto")

			// ========== Detailed Error Counters ==========
			// Defensive counters for error conditions (usually zero, but critical when non-zero)
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktRecvErrorParse.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "error", "parse")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktRecvErrorRoute.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "error", "route")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktRecvErrorEmpty.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "error", "empty")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktRecvErrorUnknown.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "error", "unknown")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktRecvErrorIoUring.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "error", "iouring")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktSentErrorMarshal.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "error", "marshal")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktSentErrorSubmit.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "error", "submit")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktSentErrorUnknown.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "error", "unknown")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktSentErrorIoUring.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "error", "iouring")
			writeCounterIfNonZero(b, "gosrt_connection_error_detail_total",
				metrics.PktSentRingFull.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "error", "ring_full")

			// ========== Connection-Level Edge Case Counters ==========
			// Counters for unusual situations (defensive coding)
			writeCounterIfNonZero(b, "gosrt_connection_edge_case_total",
				metrics.PktRecvInvalid.Load(),
				"socket_id", socketIdStr, "type", "invalid")
			writeCounterIfNonZero(b, "gosrt_connection_edge_case_total",
				metrics.PktRecvNilConnection.Load(),
				"socket_id", socketIdStr, "type", "nil_connection")
			writeCounterIfNonZero(b, "gosrt_connection_edge_case_total",
				metrics.PktRecvWrongPeer.Load(),
				"socket_id", socketIdStr, "type", "wrong_peer")
			writeCounterIfNonZero(b, "gosrt_connection_edge_case_total",
				metrics.PktRecvBacklogFull.Load(),
				"socket_id", socketIdStr, "type", "backlog_full")
			writeCounterIfNonZero(b, "gosrt_connection_edge_case_total",
				metrics.PktRecvQueueFull.Load(),
				"socket_id", socketIdStr, "type", "queue_full")
			writeCounterIfNonZero(b, "gosrt_connection_edge_case_total",
				metrics.PktRecvUnknownSocketId.Load(),
				"socket_id", socketIdStr, "type", "unknown_socket")

			// ========== Decryption Counters ==========
			writeCounterIfNonZero(b, "gosrt_connection_decrypt_failed_total",
				metrics.PktRecvUndecrypt.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "unit", "packets")
			writeCounterIfNonZero(b, "gosrt_connection_decrypt_failed_bytes_total",
				metrics.ByteRecvUndecrypt.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Congestion Control Buffer Gauges ==========
			// These are point-in-time values, not cumulative counters
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_buffer_ms",
				float64(metrics.CongestionRecvMsBuf.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_buffer_ms",
				float64(metrics.CongestionSendMsBuf.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_buffer_packets",
				float64(metrics.CongestionRecvPktBuf.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_buffer_packets",
				float64(metrics.CongestionSendPktBuf.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_buffer_bytes",
				float64(metrics.CongestionRecvByteBuf.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_buffer_bytes",
				float64(metrics.CongestionSendByteBuf.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_flight_size_packets",
				float64(metrics.CongestionSendPktFlightSize.Load()),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Congestion Control Timing Gauges ==========
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_send_period_us",
				float64(metrics.CongestionSendUsPktSndPeriod.Load()),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_send_duration_us_total",
				metrics.CongestionSendUsSndDuration.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Congestion Control Bandwidth Gauges ==========
			// Values are stored as kbps (mbps * 1000) for precision without floating point
			writeGaugeIfNonZero(b, "gosrt_connection_link_capacity_kbps",
				float64(metrics.MbpsLinkCapacity.Load()),
				"socket_id", socketIdStr, "instance", instanceName)
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_bandwidth_kbps",
				float64(metrics.CongestionRecvMbpsBandwidth.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "type", "bandwidth")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_bandwidth_kbps",
				float64(metrics.CongestionRecvMbpsLinkCapacity.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "type", "link_capacity")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_bandwidth_kbps",
				float64(metrics.CongestionSendMbpsSentBandwidth.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "type", "sent")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_bandwidth_kbps",
				float64(metrics.CongestionSendMbpsInputBandwidth.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send", "type", "input")

			// ========== Rate Metrics (Phase 1: Lockless Design) ==========
			// Float values stored as uint64 using math.Float64bits/Float64frombits
			// These are instantaneous rates calculated from atomic counters

			// Receiver rate metrics
			writeGauge(b, "gosrt_recv_rate_packets_per_sec",
				metrics.GetRecvRatePacketsPerSec(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeGauge(b, "gosrt_recv_rate_bytes_per_sec",
				metrics.GetRecvRateBytesPerSec(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeGauge(b, "gosrt_recv_rate_retrans_percent",
				metrics.GetRecvRateRetransPercent(),
				"socket_id", socketIdStr, "instance", instanceName)

			// Sender rate metrics
			writeGauge(b, "gosrt_send_rate_input_bandwidth_bps",
				metrics.GetSendRateEstInputBW(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeGauge(b, "gosrt_send_rate_sent_bandwidth_bps",
				metrics.GetSendRateEstSentBW(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeGauge(b, "gosrt_send_rate_retrans_percent",
				metrics.GetSendRateRetransPercent(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Congestion Control Byte-Level Detail ==========
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_unique_total",
				metrics.CongestionRecvByteUnique.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_unique_total",
				metrics.CongestionSendByteUnique.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_lost_total",
				metrics.CongestionRecvByteLoss.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_lost_total",
				metrics.CongestionSendByteLoss.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_retrans_total",
				metrics.CongestionRecvByteRetrans.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_retrans_total",
				metrics.CongestionSendByteRetrans.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_belated_total",
				metrics.CongestionRecvByteBelated.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_drop_total",
				metrics.CongestionRecvByteDrop.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_drop_total",
				metrics.CongestionSendByteDrop.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_payload_total",
				metrics.CongestionRecvBytePayload.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_bytes_payload_total",
				metrics.CongestionSendBytePayload.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")

			// ========== Congestion Control Drop/Belated Counters ==========
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_belated_total",
				metrics.CongestionRecvPktBelated.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_drop_total",
				metrics.CongestionRecvPktDrop.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_packets_drop_total",
				metrics.CongestionSendPktDrop.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")

			// ========== Congestion Control Internal Counters ==========
			// Defensive counters for edge cases in congestion control
			writeCounterIfNonZero(b, "gosrt_connection_congestion_internal_total",
				metrics.CongestionRecvPktNil.Load(),
				"socket_id", socketIdStr, "type", "recv_pkt_nil")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_internal_total",
				metrics.CongestionRecvPktStoreInsertFailed.Load(),
				"socket_id", socketIdStr, "type", "store_insert_failed")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_internal_total",
				metrics.CongestionSendNAKNotFound.Load(),
				"socket_id", socketIdStr, "type", "nak_not_found")
			writeCounterIfNonZero(b, "gosrt_connection_congestion_internal_total",
				metrics.NakBtreeNilWhenEnabled.Load(),
				"socket_id", socketIdStr, "type", "nak_btree_nil_when_enabled")

			// ========== NAK Detail Counters (RFC SRT Appendix A) ==========
			// Receiver-side: NAKs generated and sent
			// Figure 21: Single packet entries (4 bytes on wire)
			// Figure 22: Range entries (8 bytes on wire)
			writeCounterIfNonZero(b, "gosrt_connection_nak_entries_total",
				metrics.CongestionRecvNAKSingle.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "sent", "type", "single")
			writeCounterIfNonZero(b, "gosrt_connection_nak_entries_total",
				metrics.CongestionRecvNAKRange.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "sent", "type", "range")
			writeCounterIfNonZero(b, "gosrt_connection_nak_packets_requested_total",
				metrics.CongestionRecvNAKPktsTotal.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "sent")

			// Sender-side: NAKs received
			writeCounterIfNonZero(b, "gosrt_connection_nak_entries_total",
				metrics.CongestionSendNAKSingleRecv.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "type", "single")
			writeCounterIfNonZero(b, "gosrt_connection_nak_entries_total",
				metrics.CongestionSendNAKRangeRecv.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv", "type", "range")
			writeCounterIfNonZero(b, "gosrt_connection_nak_packets_requested_total",
				metrics.CongestionSendNAKPktsRecv.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeCounterIfNonZero(b, "gosrt_connection_nak_honored_order_total",
				metrics.CongestionSendNAKHonoredOrder.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== NAK btree Metrics ==========
			// Core operations
			writeCounterIfNonZero(b, "gosrt_nak_btree_inserts_total",
				metrics.NakBtreeInserts.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_btree_deletes_total",
				metrics.NakBtreeDeletes.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_btree_expired_total",
				metrics.NakBtreeExpired.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeGaugeIfNonZero(b, "gosrt_nak_btree_size",
				float64(metrics.NakBtreeSize.Load()),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_btree_scan_packets_total",
				metrics.NakBtreeScanPackets.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_btree_scan_gaps_total",
				metrics.NakBtreeScanGaps.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// Periodic NAK execution
			writeCounterIfNonZero(b, "gosrt_nak_periodic_runs_total",
				metrics.NakPeriodicOriginalRuns.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "impl", "original")
			writeCounterIfNonZero(b, "gosrt_nak_periodic_runs_total",
				metrics.NakPeriodicBtreeRuns.Load(),
				"socket_id", socketIdStr, "instance", instanceName, "impl", "btree")
			writeCounterIfNonZero(b, "gosrt_nak_periodic_skipped_total",
				metrics.NakPeriodicSkipped.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// Consolidation
			writeCounterIfNonZero(b, "gosrt_nak_consolidation_runs_total",
				metrics.NakConsolidationRuns.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_consolidation_entries_total",
				metrics.NakConsolidationEntries.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_consolidation_merged_total",
				metrics.NakConsolidationMerged.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_consolidation_timeout_total",
				metrics.NakConsolidationTimeout.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// FastNAK
			writeCounterIfNonZero(b, "gosrt_nak_fast_triggers_total",
				metrics.NakFastTriggers.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_fast_recent_inserts_total",
				metrics.NakFastRecentInserts.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_fast_recent_skipped_total",
				metrics.NakFastRecentSkipped.Load(),
				"socket_id", socketIdStr, "instance", instanceName)
			writeCounterIfNonZero(b, "gosrt_nak_fast_recent_overflow_total",
				metrics.NakFastRecentOverflow.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// NAK packet splitting (FR-11: MSS overflow)
			writeCounterIfNonZero(b, "gosrt_nak_packets_split_total",
				metrics.NakPacketsSplit.Load(),
				"socket_id", socketIdStr, "instance", instanceName)

			// ========== Congestion Control Rate Gauges ==========
			// Stored as percentage * 100 for precision (e.g., 5.5% = 550)
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_retrans_rate_permille",
				float64(metrics.CongestionRecvPktRetransRate.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "recv")
			writeGaugeIfNonZero(b, "gosrt_connection_congestion_retrans_rate_permille",
				float64(metrics.CongestionSendPktRetransRate.Load()),
				"socket_id", socketIdStr, "instance", instanceName, "direction", "send")

			// ========== Header Size (Configuration) ==========
			writeGaugeIfNonZero(b, "gosrt_connection_header_size_bytes",
				float64(metrics.HeaderSize.Load()),
				"socket_id", socketIdStr, "instance", instanceName)
		}

		w.Write([]byte(b.String()))
	})
}

// writeListenerMetrics writes listener-level metrics (not per-connection).
// These track events that happen before a connection is established or
// after a connection is closed.
func writeListenerMetrics(b *strings.Builder) {
	lm := GetListenerMetrics()

	// ========== Receive Path Lookup Failures ==========
	// These can happen normally during shutdown, but high counts during
	// operation may indicate bugs or attacks
	writeCounterIfNonZero(b, "gosrt_recv_conn_lookup_not_found_total",
		lm.RecvConnLookupNotFound.Load(),
		"path", "standard")
	writeCounterIfNonZero(b, "gosrt_recv_conn_lookup_not_found_total",
		lm.RecvConnLookupNotFoundIoUring.Load(),
		"path", "iouring")

	// ========== Handshake Path Lookup Failures ==========
	// These indicate programming errors - should never happen in correct code
	writeCounterIfNonZero(b, "gosrt_handshake_lookup_not_found_total",
		lm.HandshakeRejectNotFound.Load(),
		"operation", "reject")
	writeCounterIfNonZero(b, "gosrt_handshake_lookup_not_found_total",
		lm.HandshakeAcceptNotFound.Load(),
		"operation", "accept")

	// ========== Informational Counters ==========
	// Expected behavior, not errors, but useful for debugging
	writeCounterIfNonZero(b, "gosrt_handshake_duplicate_total",
		lm.HandshakeDuplicateRequest.Load())
	writeCounterIfNonZero(b, "gosrt_socketid_collision_total",
		lm.SocketIdCollision.Load())

	// ========== Send Path Lookup Failures (Bug 3 Detection) ==========
	// This should always be 0 - indicates the Bug 3 map lookup issue
	writeCounterIfNonZero(b, "gosrt_send_conn_lookup_not_found_total",
		lm.SendConnLookupNotFound.Load())

	// ========== Connection Lifecycle Counters ==========
	// Track connection establishment and closure for debugging and testing.
	// Helps detect connection replacements during network impairment tests.

	// Active connections gauge (can go up and down)
	// Note: Always write gauge even if 0 - "0 active" is meaningful information
	writeGauge(b, "gosrt_connections_active",
		float64(lm.ConnectionsActive.Load()))

	// Total connections established (monotonically increasing)
	writeCounterIfNonZero(b, "gosrt_connections_established_total",
		lm.ConnectionsEstablished.Load())

	// Total connections closed (should equal established at test end)
	writeCounterIfNonZero(b, "gosrt_connections_closed_total",
		lm.ConnectionsClosedTotal.Load())

	// Connections closed by reason (sum should equal gosrt_connections_closed_total)
	writeCounterIfNonZero(b, "gosrt_connections_closed_by_reason_total",
		lm.ConnectionsClosedGraceful.Load(),
		"reason", "graceful")
	writeCounterIfNonZero(b, "gosrt_connections_closed_by_reason_total",
		lm.ConnectionsClosedPeerIdle.Load(),
		"reason", "peer_idle_timeout")
	writeCounterIfNonZero(b, "gosrt_connections_closed_by_reason_total",
		lm.ConnectionsClosedContextCancel.Load(),
		"reason", "context_cancelled")
	writeCounterIfNonZero(b, "gosrt_connections_closed_by_reason_total",
		lm.ConnectionsClosedError.Load(),
		"reason", "error")
}
