package srt

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Stats fills the provided Statistics struct with current connection statistics.
func (c *srtConn) Stats(s *Statistics) {
	if s == nil {
		return
	}

	now := uint64(time.Since(c.start).Milliseconds())

	// Read from atomic counters directly (lock-free)
	// Still call Stats() to update instantaneous values (MsBuf, bandwidth, etc.)
	send := c.snd.Stats()
	recv := c.recv.Stats()

	previous := s.Accumulated
	interval := now - s.MsTimeStamp

	// Read from atomic counters (no lock needed)
	if c.metrics == nil {
		// Fallback if metrics not initialized (shouldn't happen)
		return
	}

	headerSize := c.metrics.HeaderSize.Load()

	// Accumulated - read directly from atomic counters (lock-free)
	s.Accumulated = StatisticsAccumulated{
		PktSent:        c.metrics.CongestionSendPkt.Load(),
		PktRecv:        c.metrics.CongestionRecvPkt.Load(),
		PktSentUnique:  c.metrics.CongestionSendPktUnique.Load(),
		PktRecvUnique:  c.metrics.CongestionRecvPktUnique.Load(),
		PktSendLoss:    c.metrics.CongestionSendPktLoss.Load(),
		PktRecvLoss:    c.metrics.CongestionRecvPktLoss.Load(),
		PktRetrans:     c.metrics.CongestionSendPktRetrans.Load(),
		PktRecvRetrans: c.metrics.CongestionRecvPktRetrans.Load(),
		PktSentACK:     c.metrics.PktSentACKSuccess.Load(),
		PktRecvACK:     c.metrics.PktRecvACKSuccess.Load(),
		PktSentNAK:     c.metrics.PktSentNAKSuccess.Load(),
		PktRecvNAK:     c.metrics.PktRecvNAKSuccess.Load(),
		PktSentKM:      c.metrics.PktSentKMSuccess.Load(),
		PktRecvKM:      c.metrics.PktRecvKMSuccess.Load(),
		UsSndDuration:  c.metrics.CongestionSendUsSndDuration.Load(),
		PktSendDrop: c.metrics.CongestionSendDataDropTooOld.Load() +
			c.metrics.PktSentDataErrorMarshal.Load() +
			c.metrics.PktSentDataRingFull.Load() +
			c.metrics.PktSentDataErrorSubmit.Load() +
			c.metrics.PktSentDataErrorIoUring.Load(),
		PktRecvDrop: c.metrics.CongestionRecvDataDropTooOld.Load() +
			c.metrics.CongestionRecvDataDropAlreadyAcked.Load() +
			c.metrics.CongestionRecvDataDropDuplicate.Load() +
			c.metrics.CongestionRecvDataDropStoreInsertFailed.Load(),
		PktRecvUndecrypt:  c.metrics.PktRecvUndecrypt.Load(),
		ByteSent:          c.metrics.CongestionSendByte.Load() + (c.metrics.CongestionSendPkt.Load() * headerSize),
		ByteRecv:          c.metrics.CongestionRecvByte.Load() + (c.metrics.CongestionRecvPkt.Load() * headerSize),
		ByteSentUnique:    c.metrics.CongestionSendByteUnique.Load() + (c.metrics.CongestionSendPktUnique.Load() * headerSize),
		ByteRecvUnique:    c.metrics.CongestionRecvByteUnique.Load() + (c.metrics.CongestionRecvPktUnique.Load() * headerSize),
		ByteRecvLoss:      c.metrics.CongestionRecvByteLoss.Load() + (c.metrics.CongestionRecvPktLoss.Load() * headerSize),
		ByteRetrans:       c.metrics.CongestionSendByteRetrans.Load() + (c.metrics.CongestionSendPktRetrans.Load() * headerSize),
		ByteRecvRetrans:   c.metrics.CongestionRecvByteRetrans.Load() + (c.metrics.CongestionRecvPktRetrans.Load() * headerSize),
		ByteSendDrop:      c.metrics.CongestionSendByteDrop.Load() + (s.Accumulated.PktSendDrop * headerSize),
		ByteRecvDrop:      c.metrics.CongestionRecvByteDrop.Load() + (s.Accumulated.PktRecvDrop * headerSize),
		ByteRecvUndecrypt: c.metrics.ByteRecvUndecrypt.Load() + (c.metrics.PktRecvUndecrypt.Load() * headerSize),
	}

	// Interval
	s.Interval = StatisticsInterval{
		MsInterval:         interval,
		PktSent:            s.Accumulated.PktSent - previous.PktSent,
		PktRecv:            s.Accumulated.PktRecv - previous.PktRecv,
		PktSentUnique:      s.Accumulated.PktSentUnique - previous.PktSentUnique,
		PktRecvUnique:      s.Accumulated.PktRecvUnique - previous.PktRecvUnique,
		PktSendLoss:        s.Accumulated.PktSendLoss - previous.PktSendLoss,
		PktRecvLoss:        s.Accumulated.PktRecvLoss - previous.PktRecvLoss,
		PktRetrans:         s.Accumulated.PktRetrans - previous.PktRetrans,
		PktRecvRetrans:     s.Accumulated.PktRecvRetrans - previous.PktRecvRetrans,
		PktSentACK:         s.Accumulated.PktSentACK - previous.PktSentACK,
		PktRecvACK:         s.Accumulated.PktRecvACK - previous.PktRecvACK,
		PktSentNAK:         s.Accumulated.PktSentNAK - previous.PktSentNAK,
		PktRecvNAK:         s.Accumulated.PktRecvNAK - previous.PktRecvNAK,
		MbpsSendRate:       float64(s.Accumulated.ByteSent-previous.ByteSent) * 8 / 1024 / 1024 / (float64(interval) / 1000),
		MbpsRecvRate:       float64(s.Accumulated.ByteRecv-previous.ByteRecv) * 8 / 1024 / 1024 / (float64(interval) / 1000),
		UsSndDuration:      s.Accumulated.UsSndDuration - previous.UsSndDuration,
		PktReorderDistance: 0,
		PktRecvBelated:     s.Accumulated.PktRecvBelated - previous.PktRecvBelated,
		PktSndDrop:         s.Accumulated.PktSendDrop - previous.PktSendDrop,
		PktRecvDrop:        s.Accumulated.PktRecvDrop - previous.PktRecvDrop,
		PktRecvUndecrypt:   s.Accumulated.PktRecvUndecrypt - previous.PktRecvUndecrypt,
		ByteSent:           s.Accumulated.ByteSent - previous.ByteSent,
		ByteRecv:           s.Accumulated.ByteRecv - previous.ByteRecv,
		ByteSentUnique:     s.Accumulated.ByteSentUnique - previous.ByteSentUnique,
		ByteRecvUnique:     s.Accumulated.ByteRecvUnique - previous.ByteRecvUnique,
		ByteRecvLoss:       s.Accumulated.ByteRecvLoss - previous.ByteRecvLoss,
		ByteRetrans:        s.Accumulated.ByteRetrans - previous.ByteRetrans,
		ByteRecvRetrans:    s.Accumulated.ByteRecvRetrans - previous.ByteRecvRetrans,
		ByteRecvBelated:    s.Accumulated.ByteRecvBelated - previous.ByteRecvBelated,
		ByteSendDrop:       s.Accumulated.ByteSendDrop - previous.ByteSendDrop,
		ByteRecvDrop:       s.Accumulated.ByteRecvDrop - previous.ByteRecvDrop,
		ByteRecvUndecrypt:  s.Accumulated.ByteRecvUndecrypt - previous.ByteRecvUndecrypt,
	}

	// Instantaneous
	s.Instantaneous = StatisticsInstantaneous{
		UsPktSendPeriod:       send.UsPktSndPeriod,
		PktFlowWindow:         uint64(c.config.FC),
		PktFlightSize:         send.PktFlightSize,
		MsRTT:                 c.rtt.RTT() / 1000,
		MbpsSentRate:          send.MbpsEstimatedSentBandwidth,
		MbpsRecvRate:          recv.MbpsEstimatedRecvBandwidth,
		MbpsLinkCapacity:      recv.MbpsEstimatedLinkCapacity,
		ByteAvailSendBuf:      0, // unlimited
		ByteAvailRecvBuf:      0, // unlimited
		MbpsMaxBW:             float64(c.config.MaxBW) / 1024 / 1024,
		ByteMSS:               uint64(c.config.MSS),
		PktSendBuf:            send.PktBuf,
		ByteSendBuf:           send.ByteBuf,
		MsSendBuf:             send.MsBuf,
		MsSendTsbPdDelay:      c.peerTsbpdDelay / 1000,
		PktRecvBuf:            recv.PktBuf,
		ByteRecvBuf:           recv.ByteBuf,
		MsRecvBuf:             recv.MsBuf,
		MsRecvTsbPdDelay:      c.tsbpdDelay / 1000,
		PktReorderTolerance:   uint64(c.config.LossMaxTTL),
		PktRecvAvgBelatedTime: 0,
		PktSendRetransRate:    send.PktRetransRate,
		PktRecvRetransRate:    recv.PktRetransRate,
	}

	// If we're only sending, the receiver congestion control value for the link capacity is zero,
	// use the value that we got from the receiver via the ACK packets.
	if s.Instantaneous.MbpsLinkCapacity == 0 {
		// Convert from uint64 (Mbps * 1000) back to float64 (Mbps)
		mbpsLinkCapacity := float64(c.metrics.MbpsLinkCapacity.Load()) / 1000.0
		s.Instantaneous.MbpsLinkCapacity = mbpsLinkCapacity
	}

	if c.config.MaxBW < 0 {
		s.Instantaneous.MbpsMaxBW = -1
	}

	s.MsTimeStamp = now
}

// SetDeadline sets the read and write deadlines (not implemented)
func (c *srtConn) SetDeadline(t time.Time) error { return nil }

// SetReadDeadline sets the read deadline (not implemented)
func (c *srtConn) SetReadDeadline(t time.Time) error { return nil }

// SetWriteDeadline sets the write deadline (not implemented)
func (c *srtConn) SetWriteDeadline(t time.Time) error { return nil }

// printCloseStatistics prints connection statistics in JSON format when the connection closes.
// This is called from close() before the connection is fully shut down.
func (c *srtConn) printCloseStatistics() {
	stats := &Statistics{}
	c.Stats(stats)

	remoteAddr := "unknown"
	if c.remoteAddr != nil {
		remoteAddr = c.remoteAddr.String()
	}

	// Get extended statistics
	extStats := c.GetExtendedStatistics()

	// Calculate retransmit percentage
	var retransPercent *float64
	if stats.Accumulated.PktSent > 0 {
		percent := (float64(stats.Accumulated.PktRetrans) / float64(stats.Accumulated.PktSent)) * 100.0
		retransPercent = &percent
	}

	// Get remaining peer idle timeout
	remainingTimeout := c.GetPeerIdleTimeoutRemaining()
	remainingSeconds := float64(remainingTimeout.Seconds())

	// Build JSON output
	output := map[string]interface{}{
		"timestamp":                           time.Now().Format(time.RFC3339Nano),
		"event":                               "connection_closed",
		"instance":                            c.config.InstanceName,
		"socket_id":                           fmt.Sprintf("0x%08x", c.socketId),
		"remote_addr":                         remoteAddr,
		"connection_duration":                 time.Since(c.start).String(),
		"peer_idle_timeout_remaining_seconds": remainingSeconds,
		"accumulated": map[string]interface{}{
			"pkt_sent_data":         stats.Accumulated.PktSent,
			"pkt_recv_data":         stats.Accumulated.PktRecv,
			"pkt_sent_ack":          stats.Accumulated.PktSentACK,
			"pkt_recv_ack":          stats.Accumulated.PktRecvACK,
			"pkt_sent_nak":          stats.Accumulated.PktSentNAK,
			"pkt_recv_nak":          stats.Accumulated.PktRecvNAK,
			"pkt_retrans_total":     stats.Accumulated.PktRetrans,
			"pkt_recv_loss":         stats.Accumulated.PktRecvLoss,
			"pkt_recv_retrans_rate": stats.Interval.PktRecvRetrans,
		},
		"instantaneous": map[string]interface{}{
			"mbps_sent_rate": stats.Instantaneous.MbpsSentRate,
			"mbps_recv_rate": stats.Instantaneous.MbpsRecvRate,
			"ms_rtt":         stats.Instantaneous.MsRTT,
		},
	}

	if extStats != nil {
		output["accumulated"].(map[string]interface{})["pkt_sent_ackack"] = extStats.PktSentACKACK
		output["accumulated"].(map[string]interface{})["pkt_recv_ackack"] = extStats.PktRecvACKACK
		output["accumulated"].(map[string]interface{})["pkt_retrans_from_nak"] = extStats.PktRetransFromNAK
	}

	if retransPercent != nil {
		output["accumulated"].(map[string]interface{})["pkt_retrans_percent"] = *retransPercent
	}

	jsonData, err := json.Marshal(output)
	if err != nil {
		c.log("connection:close:error", func() string {
			return fmt.Sprintf("failed to encode close statistics: %v", err)
		})
		return
	}

	fmt.Fprintf(os.Stderr, "%s\n", string(jsonData))
}
