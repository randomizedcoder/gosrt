package common

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	srt "github.com/datarhei/gosrt"
)

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
		PktSent           uint64   `json:"pkt_sent_data"`
		PktRecv           uint64   `json:"pkt_recv_data"`
		PktSentACK        uint64   `json:"pkt_sent_ack"`
		PktRecvACK        uint64   `json:"pkt_recv_ack"`
		PktSentACKACK     *uint64  `json:"pkt_sent_ackack,omitempty"`
		PktRecvACKACK     *uint64  `json:"pkt_recv_ackack,omitempty"`
		PktSentNAK        uint64   `json:"pkt_sent_nak"`
		PktRecvNAK        uint64   `json:"pkt_recv_nak"`
		PktRetrans        uint64   `json:"pkt_retrans_total"`
		PktRetransFromNAK *uint64  `json:"pkt_retrans_from_nak,omitempty"`
		PktRetransPercent *float64 `json:"pkt_retrans_percent,omitempty"`
		PktRecvLoss       uint64   `json:"pkt_recv_loss"`
		PktRecvLossRate   float64  `json:"pkt_recv_loss_rate"`
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
		connStat.Accumulated.PktRecvLossRate = stats.Instantaneous.PktRecvLossRate

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
