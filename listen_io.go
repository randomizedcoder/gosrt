package srt

import (
	"context"
	"fmt"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

func (ln *listener) reader(ctx context.Context) {
	defer func() {
		ln.log("listen", func() string { return "left reader loop" })
	}()

	ln.log("listen", func() string { return "reader loop started" })

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-ln.rcvQueue:
			if ln.isShutdown() {
				break
			}

			ln.log("packet:recv:dump", func() string { return p.Dump() })

			if p.Header().DestinationSocketId == 0 {
				if p.Header().IsControlPacket && p.Header().ControlType == packet.CTRLTYPE_HANDSHAKE {
					select {
					case ln.backlog <- p:
					default:
						ln.log("handshake:recv:error", func() string { return "backlog is full" })
					}
				}
				break
			}

			// sync.Map handles locking internally
			val, loadOk := ln.conns.Load(p.Header().DestinationSocketId)
			var conn *srtConn
			var connOk bool
			if loadOk {
				conn, connOk = val.(*srtConn)
			}

			if !loadOk || !connOk || conn == nil {
				// ignore the packet, we don't know the destination
				// Track at listener level since we can't associate with a connection
				metrics.GetListenerMetrics().RecvConnLookupNotFound.Add(1)
				break
			}

			if !ln.config.AllowPeerIpChange {
				if p.Header().Addr.String() != conn.RemoteAddr().String() {
					// ignore the packet, it's not from the expected peer
					// https://haivision.github.io/srt-rfc/draft-sharabayko-srt.html#name-security-considerations
					// Track metrics for wrong peer (we have connection now)
					if conn.metrics != nil {
						metrics.IncrementRecvErrorMetrics(conn.metrics, false, metrics.DropReasonWrongPeer)
					}
					break
				}
			}

			// Track successful receive (ReadFrom path)
			if conn.metrics != nil {
				metrics.IncrementRecvMetrics(conn.metrics, p, false, true, 0)
			}

			conn.push(p)
		}
	}
}

// Send a packet to the wire. This function must be synchronous in order to allow to safely call Packet.Decommission() afterward.
// NOTE: This is a fallback method used only when io_uring is disabled or unavailable.
// When io_uring is enabled, connections use their own per-connection send() method.
// This overload is used for handshake packets before connection is established (no metrics available).
func (ln *listener) send(p packet.Packet) {
	ln.sendWithMetrics(p, nil)
}

// sendWithMetrics sends a packet with optional metrics tracking.
// This is the primary send method for non-io_uring paths.
// The metrics parameter should be the connection's metrics (nil for pre-connection handshakes).
func (ln *listener) sendWithMetrics(p packet.Packet, m *metrics.ConnectionMetrics) {
	ln.sndMutex.Lock()
	defer ln.sndMutex.Unlock()

	ln.sndData.Reset()

	if err := p.Marshal(&ln.sndData); err != nil {
		p.Decommission()
		ln.log("packet:send:error", func() string { return "marshaling packet failed" })
		if m != nil {
			metrics.IncrementSendMetrics(m, p, false, false, metrics.DropReasonMarshal)
		}
		return
	}

	buffer := ln.sndData.Bytes()

	ln.log("packet:send:dump", func() string { return p.Dump() })

	// Write the packet's contents to the wire
	_, writeErr := ln.pc.WriteTo(buffer, p.Header().Addr)
	if writeErr != nil {
		ln.log("packet:send:error", func() string { return fmt.Sprintf("failed to write packet to network: %v", writeErr) })
		if m != nil {
			metrics.IncrementSendMetrics(m, p, false, false, metrics.DropReasonWrite)
		}
	} else if m != nil {
		// Success - track metrics
		metrics.IncrementSendMetrics(m, p, false, true, 0)
	}

	if p.Header().IsControlPacket {
		// Control packets can be decommissioned because they will not be sent again (data packets might be retransferred)
		p.Decommission()
	}
}

// sendBrokenLookup is the OLD BROKEN implementation for testing error detection.
// This uses the wrong lookup key (DestinationSocketId instead of local socketId).
// DO NOT USE IN PRODUCTION - only for verifying error counters work.
func (ln *listener) sendBrokenLookup(p packet.Packet) {
	ln.sndMutex.Lock()
	defer ln.sndMutex.Unlock()

	ln.sndData.Reset()

	if err := p.Marshal(&ln.sndData); err != nil {
		p.Decommission()
		ln.log("packet:send:error", func() string { return "marshaling packet failed" })
		// Try to find connection for metrics tracking - THIS IS THE BUG!
		// DestinationSocketId is the PEER's socket ID, but ln.conns is keyed by LOCAL socket ID
		h := p.Header()
		if h != nil {
			val, ok := ln.conns.Load(h.DestinationSocketId) // WRONG KEY!
			if !ok {
				// Counter to detect this bug
				metrics.GetListenerMetrics().SendConnLookupNotFound.Add(1)
			} else if conn, isConn := val.(*srtConn); isConn && conn != nil && conn.metrics != nil {
				metrics.IncrementSendMetrics(conn.metrics, p, false, false, metrics.DropReasonMarshal)
			}
		}
		return
	}

	buffer := ln.sndData.Bytes()

	ln.log("packet:send:dump", func() string { return p.Dump() })

	// Write the packet's contents to the wire
	_, writeErr := ln.pc.WriteTo(buffer, p.Header().Addr)
	if writeErr != nil {
		ln.log("packet:send:error", func() string { return fmt.Sprintf("failed to write packet to network: %v", writeErr) })
		// Try to find connection for metrics tracking - THIS IS THE BUG!
		h := p.Header()
		if h != nil {
			val, ok := ln.conns.Load(h.DestinationSocketId) // WRONG KEY!
			if !ok {
				metrics.GetListenerMetrics().SendConnLookupNotFound.Add(1)
			} else if conn, isConn := val.(*srtConn); isConn && conn != nil && conn.metrics != nil {
				metrics.IncrementSendMetrics(conn.metrics, p, false, false, metrics.DropReasonWrite)
			}
		}
	} else {
		// Success - try to find connection for metrics tracking - THIS IS THE BUG!
		h := p.Header()
		if h != nil {
			val, ok := ln.conns.Load(h.DestinationSocketId) // WRONG KEY!
			if !ok {
				metrics.GetListenerMetrics().SendConnLookupNotFound.Add(1)
			} else if conn, isConn := val.(*srtConn); isConn && conn != nil && conn.metrics != nil {
				metrics.IncrementSendMetrics(conn.metrics, p, false, true, 0)
			}
		}
	}

	if p.Header().IsControlPacket {
		p.Decommission()
	}
}

func (ln *listener) log(topic string, message func() string) {
	if ln.config.Logger == nil {
		return
	}

	ln.config.Logger.Print(topic, 0, 2, message)
}
