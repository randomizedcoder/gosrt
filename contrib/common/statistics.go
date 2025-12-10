package common

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	srt "github.com/datarhei/gosrt"
)

// ThroughputGetter is a function that returns current bytes, packets, gaps, skips, and retransmits
// This allows the display to work with any counter source (metrics.ConnectionMetrics, etc.)
// Returns: (bytes, pkts, gapsPkts, skipsPkts, retransPkts)
//   - bytes: total bytes transferred (for MB display)
//   - pkts: packets for rate calculation (for pkt/s display)
//   - gapsPkts: sequence gaps detected (triggers NAK/retransmission)
//   - skipsPkts: TSBPD skips = packets that NEVER arrived (TRUE losses)
//   - retransPkts: total retransmissions (ARQ recovery activity)
type ThroughputGetter func() (bytes uint64, pkts uint64, gapsPkts uint64, skipsPkts uint64, retransPkts uint64)

// RunThroughputDisplay runs a throughput display loop that periodically prints stats
// The getter function is called to retrieve current byte/packet/success/loss/retrans totals
// The loop exits when ctx is cancelled
//
// Output format (fixed-width columns):
//
//	[label] HH:MM:SS.xx | 999.9 pkt/s | 99.99 MB | 9.999 Mb/s | 9999k ok / 999 gaps / 999 retx | recovery=100.0%
//
// - "gaps" = sequence gaps detected (triggers NAK/retransmission)
// - "retx" = retransmissions sent/received (ARQ recovery activity)
// - "recovery" = (gaps - TSBPD_skips) / gaps = % of gaps successfully recovered
//   - 100% = all gaps recovered via retransmission (no true losses)
//   - <100% = some packets never arrived before TSBPD timeout (true losses)
func RunThroughputDisplay(ctx context.Context, period time.Duration, getter ThroughputGetter) {
	RunThroughputDisplayWithLabel(ctx, period, "", getter)
}

// RunThroughputDisplayWithLabel runs a throughput display loop with a component label
func RunThroughputDisplayWithLabel(ctx context.Context, period time.Duration, label string, getter ThroughputGetter) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	var prevBytes, prevPkts uint64
	last := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			currentBytes, currentPkts, gapsPkts, skipsPkts, retransPkts := getter()

			diff := now.Sub(last)
			if diff.Seconds() <= 0 {
				continue // Avoid division by zero
			}

			mbps := float64(currentBytes-prevBytes) * 8 / (1000 * 1000 * diff.Seconds())
			pps := float64(currentPkts-prevPkts) / diff.Seconds()

			// Calculate RECOVERY rate: (gaps - skips) / gaps
			// - gaps = sequence gaps detected (triggers NAK/retrans)
			// - skips = TSBPD skips (packets that NEVER arrived - true losses)
			// Recovery = 100% means all gaps were successfully retransmitted
			var recoveryPct float64 = 100.0
			if gapsPkts > 0 {
				recoveryPct = (1.0 - float64(skipsPkts)/float64(gapsPkts)) * 100.0
				if recoveryPct < 0 {
					recoveryPct = 0
				}
			}

			// Format time with 2 decimal places: HH:MM:SS.xx
			timeStr := now.Format("15:04:05.00")

			// Simplified format: [label] time | rate | total MB | Mb/s | packets ok / gaps / retx | recovery=%
			// Use currentPkts for "ok" since it's the actual received packet count
			// "gaps" = sequence gaps detected (triggers NAK/retrans)
			// "recovery" = % of gaps that were successfully retransmitted (100% = no true losses)
			labelStr := ""
			if label != "" {
				labelStr = fmt.Sprintf("[%s] ", label)
			}
			// Use newline instead of carriage return so both [PUB] and [SUB] lines are visible
			// when running multiple applications simultaneously
			fmt.Fprintf(os.Stderr, "%s%s | %7.1f pkt/s | %7.2f MB | %6.3f Mb/s | %6.1fk ok / %5d gaps / %5d retx | recovery=%5.1f%%\n",
				labelStr,
				timeStr,
				pps,
				float64(currentBytes)/(1024*1024),
				mbps,
				float64(currentPkts)/1000,
				gapsPkts,
				retransPkts,
				recoveryPct)

			prevBytes, prevPkts = currentBytes, currentPkts
			last = now
		}
	}
}

// ConnectionTypeLabeler is a function that returns a label for a connection at a given index.
// Returns empty string if no label should be shown.
type ConnectionTypeLabeler func(index int, total int) string

// ConnectionStatistics represents the statistics for a single connection in JSON format
type ConnectionStatistics struct {
	ConnectionNumber            int      `json:"connection_number,omitempty"`
	ConnectionType              string   `json:"connection_type,omitempty"`
	SocketID                    string   `json:"socket_id"`
	RemoteAddr                  string   `json:"remote_addr"`
	PeerIdleTimeoutRemainingSec *float64 `json:"peer_idle_timeout_remaining_seconds,omitempty"`
	Accumulated                 struct {
		PktSent            uint64   `json:"pkt_sent_data"`
		PktRecv            uint64   `json:"pkt_recv_data"`
		PktSentACK         uint64   `json:"pkt_sent_ack"`
		PktRecvACK         uint64   `json:"pkt_recv_ack"`
		PktSentACKACK      *uint64  `json:"pkt_sent_ackack,omitempty"`
		PktRecvACKACK      *uint64  `json:"pkt_recv_ackack,omitempty"`
		PktSentNAK         uint64   `json:"pkt_sent_nak"`
		PktRecvNAK         uint64   `json:"pkt_recv_nak"`
		PktRetrans         uint64   `json:"pkt_retrans_total"`
		PktRetransFromNAK  *uint64  `json:"pkt_retrans_from_nak,omitempty"`
		PktRetransPercent  *float64 `json:"pkt_retrans_percent,omitempty"`
		PktRecvLoss        uint64   `json:"pkt_recv_loss"`
		PktRecvRetransRate float64  `json:"pkt_recv_retrans_rate"` // Retransmission rate (NOT loss rate)
	} `json:"accumulated"`
	Instantaneous struct {
		MbpsSentRate              float64  `json:"mbps_sent_rate"`
		MbpsRecvRate              float64  `json:"mbps_recv_rate"`
		MsRTT                     float64  `json:"ms_rtt"`
		PeerIdleTimeoutRemainingS *float64 `json:"peer_idle_timeout_remaining_seconds,omitempty"`
	} `json:"instantaneous"`
}

// StatisticsOutput represents the complete statistics output in JSON format
type StatisticsOutput struct {
	Timestamp         string                 `json:"timestamp"`
	Interval          string                 `json:"interval"`
	ActiveConnections int                    `json:"active_connections"`
	Connections       []ConnectionStatistics `json:"connections"`
}

// PrintConnectionStatistics prints statistics for a list of connections in JSON format.
// It handles both standard SRT statistics and extended statistics (like PktRetransFromNAK).
// The labeler function can be used to add connection type labels (e.g., "reader", "writer").
// If labeler is nil, no labels will be shown.
func PrintConnectionStatistics(connections []srt.Conn, interval string, labeler ConnectionTypeLabeler) {
	if len(connections) == 0 {
		return
	}

	output := StatisticsOutput{
		Timestamp:         time.Now().Format(time.RFC3339Nano),
		Interval:          interval,
		ActiveConnections: len(connections),
		Connections:       make([]ConnectionStatistics, 0, len(connections)),
	}

	for i, conn := range connections {
		stats := &srt.Statistics{}
		conn.Stats(stats)

		remoteAddr := "unknown"
		if conn.RemoteAddr() != nil {
			remoteAddr = conn.RemoteAddr().String()
		}

		// Get connection type label if labeler is provided
		connType := ""
		if labeler != nil {
			connType = labeler(i, len(connections))
		}

		connStat := ConnectionStatistics{
			ConnectionNumber: i + 1,
			SocketID:         fmt.Sprintf("0x%08x", conn.SocketId()),
			RemoteAddr:       remoteAddr,
		}

		if connType != "" {
			connStat.ConnectionType = connType
		}

		// Get remaining peer idle timeout
		remainingTimeout := conn.GetPeerIdleTimeoutRemaining()
		if remainingTimeout > 0 {
			remainingSec := remainingTimeout.Seconds()
			connStat.PeerIdleTimeoutRemainingSec = &remainingSec
		}

		// Accumulated statistics
		connStat.Accumulated.PktSent = stats.Accumulated.PktSent
		connStat.Accumulated.PktRecv = stats.Accumulated.PktRecv
		connStat.Accumulated.PktSentACK = stats.Accumulated.PktSentACK
		connStat.Accumulated.PktRecvACK = stats.Accumulated.PktRecvACK
		connStat.Accumulated.PktSentNAK = stats.Accumulated.PktSentNAK
		connStat.Accumulated.PktRecvNAK = stats.Accumulated.PktRecvNAK
		connStat.Accumulated.PktRetrans = stats.Accumulated.PktRetrans
		connStat.Accumulated.PktRecvLoss = stats.Accumulated.PktRecvLoss
		connStat.Accumulated.PktRecvRetransRate = stats.Instantaneous.PktRecvRetransRate

		// Get extended statistics (not part of standard SRT stats) in a single call
		extStats := conn.GetExtendedStatistics()
		if extStats != nil {
			connStat.Accumulated.PktSentACKACK = &extStats.PktSentACKACK
			connStat.Accumulated.PktRecvACKACK = &extStats.PktRecvACKACK
			connStat.Accumulated.PktRetransFromNAK = &extStats.PktRetransFromNAK
		}

		// Calculate retransmit percentage: (PktRetrans / PktSent) * 100
		// Only calculate if PktSent > 0 to avoid division by zero
		if stats.Accumulated.PktSent > 0 {
			retransPercent := (float64(stats.Accumulated.PktRetrans) / float64(stats.Accumulated.PktSent)) * 100.0
			connStat.Accumulated.PktRetransPercent = &retransPercent
		}

		// Instantaneous statistics
		connStat.Instantaneous.MbpsSentRate = stats.Instantaneous.MbpsSentRate
		connStat.Instantaneous.MbpsRecvRate = stats.Instantaneous.MbpsRecvRate
		connStat.Instantaneous.MsRTT = stats.Instantaneous.MsRTT

		// Get peer idle timeout remaining (if available)
		type connWithTimeout interface {
			GetPeerIdleTimeoutRemaining() time.Duration
		}
		if connWithTimeout, ok := conn.(connWithTimeout); ok {
			remaining := connWithTimeout.GetPeerIdleTimeoutRemaining()
			if remaining > 0 {
				remainingSeconds := remaining.Seconds()
				connStat.Instantaneous.PeerIdleTimeoutRemainingS = &remainingSeconds
			}
		}

		output.Connections = append(output.Connections, connStat)
	}

	// Encode to JSON with indentation for readability
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		// Fallback to error message if JSON encoding fails
		fmt.Fprintf(os.Stderr, "Error encoding statistics to JSON: %v\n", err)
		return
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", string(jsonData))
}
