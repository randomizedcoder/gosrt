package srt

import (
	"fmt"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
)

// Close closes the connection.
func (c *srtConn) Close() error {
	c.log("connection:close:reason", func() string {
		return "application requested close"
	})
	c.close(metrics.CloseReasonGraceful)

	return nil
}

// GetPeerIdleTimeoutRemaining returns the remaining time until the peer idle timeout fires.
// Returns 0 if the timer is not active or has already fired.
// This implements the Conn interface.
func (c *srtConn) GetPeerIdleTimeoutRemaining() time.Duration {
	// Calculate remaining time based on when it was last reset (atomic read)
	lastResetNano := c.peerIdleTimeoutLastReset.Load()
	if lastResetNano == 0 {
		return 0
	}
	lastReset := time.Unix(0, lastResetNano)
	elapsed := time.Since(lastReset)
	remaining := c.config.PeerIdleTimeout - elapsed

	if remaining < 0 {
		return 0
	}
	return remaining
}

// resetPeerIdleTimeout resets the peer idle timeout timer (hot path - lock-free)
func (c *srtConn) resetPeerIdleTimeout() {
	// No lock needed - timer.Reset() and atomic store are thread-safe and lock-free
	c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
	c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())
}

// getTotalReceivedPackets returns total received packets (atomic read)
// This counts all packets that successfully reached the connection, indicating peer is alive
func (c *srtConn) getTotalReceivedPackets() uint64 {
	if c.metrics == nil {
		return 0
	}
	// Single atomic load - much faster than summing 8 counters
	return c.metrics.PktRecvSuccess.Load()
}

// watchPeerIdleTimeout watches for timeout using atomic counter checks
func (c *srtConn) watchPeerIdleTimeout() {

	// Get initial packet count
	initialCount := c.getTotalReceivedPackets()

	// Determine ticker interval based on timeout duration
	// For longer timeouts (>6s), check more frequently (1/4) for better responsiveness
	// For shorter timeouts (<=6s), check at 1/2 interval
	tickerInterval := c.config.PeerIdleTimeout / 2
	if c.config.PeerIdleTimeout > 6*time.Second {
		tickerInterval = c.config.PeerIdleTimeout / 4
	}
	ticker := time.NewTicker(tickerInterval)
	defer ticker.Stop()

	// Proactive keepalive ticker (if enabled)
	// Sends keepalive when connection is idle to prevent timeout
	keepaliveInterval := c.getKeepaliveInterval()
	var keepaliveTicker *time.Ticker
	var keepaliveChan <-chan time.Time
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		keepaliveChan = keepaliveTicker.C
		defer keepaliveTicker.Stop()
	}

	for {
		select {
		case <-c.peerIdleTimeout.C:
			// Timer expired - check if packets were received
			currentCount := c.getTotalReceivedPackets()
			if currentCount == initialCount {
				// No packets received - timeout occurred
				c.log("connection:close:reason", func() string {
					return fmt.Sprintf("peer idle timeout: no data received from peer for %s", c.config.PeerIdleTimeout)
				})
				c.log("connection:close", func() string {
					return fmt.Sprintf("no more data received from peer for %s. shutting down", c.config.PeerIdleTimeout)
				})
				go c.close(metrics.CloseReasonPeerIdle)
				return
			}
			// Packets were received - will reset timer after select

		case <-ticker.C:
			// Periodic check (1/2 timeout for <=6s, 1/4 timeout for >6s)
			// Will check counter and reset if needed after select

		case <-keepaliveChan:
			// Proactive keepalive: send if no recent activity to prevent timeout
			currentCount := c.getTotalReceivedPackets()
			if currentCount == initialCount {
				// No packets received since last check - send keepalive
				c.sendProactiveKeepalive()
			}
			// Note: We don't update initialCount here - that happens in the common logic below

		case <-c.ctx.Done():
			// Connection closing
			return
		}

		// Check if packets were received (common logic for both timer and ticker)
		// This is executed after the select, making the code more DRY and Go-idiomatic
		currentCount := c.getTotalReceivedPackets()
		if currentCount > initialCount {
			// Packets received - reset timer and update count
			initialCount = currentCount
			c.peerIdleTimeout.Reset(c.config.PeerIdleTimeout)
			c.peerIdleTimeoutLastReset.Store(time.Now().UnixNano())
		}
	}
}

// close closes the connection with the specified reason.
// The reason is used for metrics tracking to identify why connections were closed.
func (c *srtConn) close(reason metrics.CloseReason) {

	c.shutdownOnce.Do(func() {
		// Unregister from metrics registry with close reason
		metrics.UnregisterConnection(c.socketId, reason)

		// Print statistics before closing (if logger is available)
		if c.logger != nil {
			c.printCloseStatistics()
		}

		c.log("connection:close", func() string { return "stopping peer idle timeout" })

		// Stop peer idle timeout timer
		if c.peerIdleTimeout != nil {
			c.peerIdleTimeout.Stop()
		}

		c.log("connection:close", func() string { return "sending shutdown message to peer" })

		c.sendShutdown()

		c.log("connection:close", func() string { return "stopping all routines and channels" })

		// Cancel connection context to signal all goroutines to exit
		c.cancelCtx()

		// Wait for all connection goroutines to finish (with timeout)
		c.log("connection:close", func() string { return "waiting for connection goroutines" })
		done := make(chan struct{})
		go func() {
			c.connWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// All connection goroutines finished
		case <-time.After(5 * time.Second):
			c.log("connection:close:warning", func() string {
				return "timeout waiting for connection goroutines"
			})
		}

		// Clean up io_uring resources if enabled (Linux-specific)
		// cleanupIoUring() handles: cancellation, QueueExit (to wake blocked WaitCQE),
		// waiting for completion handler, draining completions, buffer cleanup
		c.cleanupIoUring()

		c.log("connection:close", func() string { return "flushing congestion" })

		c.snd.Flush()
		c.recv.Flush()

		c.log("connection:close", func() string { return "shutdown" })

		go func() {
			c.onShutdown(c.socketId)
		}()

		// Notify parent waitgroup that this connection has shut down
		if c.shutdownWg != nil {
			c.shutdownWg.Done()
		}
	})
}

func (c *srtConn) log(topic string, message func() string) {
	c.logger.Print(topic, c.socketId, 2, message)
}

