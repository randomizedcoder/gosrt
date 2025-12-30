package srt

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// reader reads packets from the receive queue and pushes them into the connection
func (dl *dialer) reader(ctx context.Context) {
	defer func() {
		dl.log("dial", func() string { return "left reader loop" })
	}()

	dl.log("dial", func() string { return "reader loop started" })

	for {
		select {
		case <-ctx.Done():
			return
		case p := <-dl.rcvQueue:
			if dl.isShutdown() {
				break
			}

			dl.log("packet:recv:dump", func() string { return p.Dump() })

			if p.Header().DestinationSocketId != dl.socketId {
				break
			}

			if p.Header().IsControlPacket && p.Header().ControlType == packet.CTRLTYPE_HANDSHAKE {
				dl.handleHandshake(p)
				break
			}

			dl.connLock.RLock()
			if dl.conn == nil {
				dl.connLock.RUnlock()
				// Note: Can't track metrics here - no connection yet
				break
			}
			conn := dl.conn
			dl.connLock.RUnlock()

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
func (dl *dialer) send(p packet.Packet) {
	dl.sndMutex.Lock()
	defer dl.sndMutex.Unlock()

	dl.sndData.Reset()

	if err := p.Marshal(&dl.sndData); err != nil {
		p.Decommission()
		dl.log("packet:send:error", func() string { return "marshalling packet failed" })
		// Try to find connection for metrics tracking
		dl.connLock.RLock()
		conn := dl.conn
		dl.connLock.RUnlock()
		if conn != nil && conn.metrics != nil {
			metrics.IncrementSendMetrics(conn.metrics, p, false, false, metrics.DropReasonMarshal)
		}
		return
	}

	buffer := dl.sndData.Bytes()

	dl.log("packet:send:dump", func() string { return p.Dump() })

	// Write the packet's contents to the wire
	_, writeErr := dl.pc.Write(buffer)
	if writeErr != nil {
		dl.log("packet:send:error", func() string { return fmt.Sprintf("failed to write packet to network: %v", writeErr) })
		// Try to find connection for metrics tracking
		dl.connLock.RLock()
		conn := dl.conn
		dl.connLock.RUnlock()
		if conn != nil && conn.metrics != nil {
			metrics.IncrementSendMetrics(conn.metrics, p, false, false, metrics.DropReasonWrite)
		}
	} else {
		// Success - try to find connection for metrics tracking
		dl.connLock.RLock()
		conn := dl.conn
		dl.connLock.RUnlock()
		if conn != nil && conn.metrics != nil {
			metrics.IncrementSendMetrics(conn.metrics, p, false, true, 0)
		}
	}

	if p.Header().IsControlPacket {
		// Control packets can be decommissioned because they will not be sent again (data packets might be retransferred)
		p.Decommission()
	}
}

func (dl *dialer) LocalAddr() net.Addr {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil
	}

	return dl.conn.LocalAddr()
}

func (dl *dialer) RemoteAddr() net.Addr {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil
	}

	return dl.conn.RemoteAddr()
}

func (dl *dialer) SocketId() uint32 {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.SocketId()
}

func (dl *dialer) PeerSocketId() uint32 {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.PeerSocketId()
}

func (dl *dialer) StreamId() string {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return ""
	}

	return dl.conn.StreamId()
}

func (dl *dialer) Version() uint32 {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.Version()
}

func (dl *dialer) isShutdown() bool {
	dl.shutdownLock.RLock()
	defer dl.shutdownLock.RUnlock()

	return dl.shutdown
}

func (dl *dialer) Close() error {
	dl.shutdownOnce.Do(func() {
		dl.shutdownLock.Lock()
		dl.shutdown = true
		dl.shutdownLock.Unlock()

		// Note: All goroutines will exit when dl.ctx is cancelled (via client context cancellation)
		// No need to call stopReader() - it was a no-op anyway since we use dl.ctx directly

		dl.connLock.RLock()
		if dl.conn != nil {
			dl.conn.Close() // Connection will call connWg.Done() when done (Phase 5)
		}
		dl.connLock.RUnlock()

		// Wait for connection to shutdown
		done := make(chan struct{})
		go func() {
			dl.connWg.Wait()
			close(done)
		}()

		// Use config shutdown delay as timeout, or default to 5 seconds
		timeout := 5 * time.Second
		if dl.config.ShutdownDelay > 0 {
			timeout = dl.config.ShutdownDelay
		}

		select {
		case <-done:
			// Connection closed
		case <-time.After(timeout):
			// Timeout - log warning but continue
			// Note: In production, we might want to log this
		}

		// Wait for receive completion handler (io_uring)
		done = make(chan struct{})
		go func() {
			dl.recvCompWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Receive handler exited
		case <-time.After(timeout):
			// Timeout - log warning but continue
		}

		// Cleanup io_uring receive ring (if initialized)
		dl.cleanupIoUringRecv()

		dl.log("dial", func() string { return "closing socket" })
		dl.pc.Close()

		select {
		case <-dl.doneChan:
		default:
		}

		// Notify root waitgroup
		if dl.shutdownWg != nil {
			dl.shutdownWg.Done()
		}
	})

	return nil
}

func (dl *dialer) Read(p []byte) (n int, err error) {
	if err := dl.checkConnection(); err != nil {
		return 0, err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0, fmt.Errorf("no connection")
	}

	return dl.conn.Read(p)
}

func (dl *dialer) ReadPacket() (packet.Packet, error) {
	if err := dl.checkConnection(); err != nil {
		return nil, err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil, fmt.Errorf("no connection")
	}

	return dl.conn.ReadPacket()
}

func (dl *dialer) Write(p []byte) (n int, err error) {
	if err := dl.checkConnection(); err != nil {
		return 0, err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0, fmt.Errorf("no connection")
	}

	return dl.conn.Write(p)
}

func (dl *dialer) WritePacket(p packet.Packet) error {
	if err := dl.checkConnection(); err != nil {
		return err
	}

	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return fmt.Errorf("no connection")
	}

	return dl.conn.WritePacket(p)
}

func (dl *dialer) SetDeadline(t time.Time) error      { return dl.conn.SetDeadline(t) }
func (dl *dialer) SetReadDeadline(t time.Time) error  { return dl.conn.SetReadDeadline(t) }
func (dl *dialer) SetWriteDeadline(t time.Time) error { return dl.conn.SetWriteDeadline(t) }

func (dl *dialer) Stats(s *Statistics) {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return
	}

	dl.conn.Stats(s)
}

func (dl *dialer) GetExtendedStatistics() *ExtendedStatistics {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return nil
	}

	return dl.conn.GetExtendedStatistics()
}

func (dl *dialer) GetPeerIdleTimeoutRemaining() time.Duration {
	dl.connLock.RLock()
	defer dl.connLock.RUnlock()

	if dl.conn == nil {
		return 0
	}

	return dl.conn.GetPeerIdleTimeoutRemaining()
}

func (dl *dialer) log(topic string, message func() string) {
	if dl.config.Logger == nil {
		return
	}

	dl.config.Logger.Print(topic, dl.socketId, 2, message)
}

