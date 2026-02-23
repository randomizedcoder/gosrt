package srt

import (
	"net"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
)

// markDone marks the listener as done by closing
// the done channel & sets the error
func (ln *listener) markDone(err error) {
	ln.doneOnce.Do(func() {
		ln.lock.Lock()
		defer ln.lock.Unlock()
		ln.doneErr = err
		close(ln.doneChan)
	})
}

// error returns the error that caused the listener to be done
// if it's nil then the listener is not done
func (ln *listener) error() error {
	ln.lock.Lock()
	defer ln.lock.Unlock()
	return ln.doneErr
}

func (ln *listener) handleShutdown(socketId uint32) {
	// sync.Map handles locking internally
	ln.conns.Delete(socketId)
}

// getConnections returns all active connections from the listener.
// This is safe to call concurrently and returns a snapshot of connections.
func (ln *listener) getConnections() (connections []Conn) {
	ln.conns.Range(func(key, value interface{}) bool {
		if conn, ok := value.(*srtConn); ok && conn != nil {
			connections = append(connections, conn)
		}
		return true
	})
	return
}

func (ln *listener) isShutdown() bool {
	ln.shutdownLock.RLock()
	defer ln.shutdownLock.RUnlock()

	return ln.shutdown
}

func (ln *listener) Close() {
	ln.shutdownOnce.Do(func() {
		ln.shutdownLock.Lock()
		ln.shutdown = true
		ln.shutdownLock.Unlock()

		// Note: All goroutines will exit when ln.ctx is cancelled (via server context cancellation)
		// No need to call stopReader() - it was a no-op anyway since we use ln.ctx directly

		// Close all connections (triggers connection shutdowns)
		// sync.Map handles locking internally
		ln.conns.Range(func(key, value interface{}) bool {
			// Check if value is nil BEFORE type assertion (nil is stored as placeholder)
			if value == nil {
				return true // continue iteration
			}
			conn, ok := value.(*srtConn)
			if !ok || conn == nil {
				return true // continue iteration
			}
			conn.close(metrics.CloseReasonContextCancel) // Connection will call connWg.Done() when done (Phase 5)
			return true                                  // continue iteration
		})

		// Wait for all connections to shutdown
		done := make(chan struct{})
		go func() {
			ln.connWg.Wait()
			close(done)
		}()

		// Use config shutdown delay as timeout, or default to 5 seconds
		timeout := 5 * time.Second
		if ln.config.ShutdownDelay > 0 {
			timeout = ln.config.ShutdownDelay
		}

		select {
		case <-done:
			// All connections closed
		case <-time.After(timeout):
			// Timeout - log warning but continue
			// Note: In production, we might want to log this
		}

		// Wait for receive completion handler (io_uring)
		done = make(chan struct{})
		go func() {
			ln.recvCompWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Receive handler exited
		case <-time.After(timeout):
			// Timeout - log warning but continue
		}

		// Cleanup io_uring receive ring (if initialized)
		ln.cleanupIoUringRecv()

		ln.log("listen", func() string { return "closing socket" })

		ln.pc.Close()

		// Notify server waitgroup
		if ln.shutdownWg != nil {
			ln.shutdownWg.Done()
		}
	})
}

func (ln *listener) Addr() net.Addr {
	addrString := "0.0.0.0:0"
	if ln.addr != nil {
		addrString = ln.addr.String()
	}

	addr, _ := net.ResolveUDPAddr("udp", addrString)
	return addr
}

