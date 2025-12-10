//go:build linux
// +build linux

package srt

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/randomizedcoder/giouring"
)

// ioUringPollInterval is the interval between io_uring completion queue polls
// when no completions are immediately available (EAGAIN).
//
// Trade-offs:
//   - Lower values (1ms): Faster shutdown detection, but ~1000 wakeups/sec when idle
//   - Higher values (100ms): Lower CPU usage when idle, but slower shutdown response
//
// 10ms provides a good balance: ~100 wakeups/sec when idle, and shutdown
// response time that feels instant to users (<10ms added latency).
//
// Note: This only affects idle polling. During active data flow, completions
// are immediately available and PeekCQE() returns without sleeping.
const ioUringPollInterval = 10 * time.Millisecond

// initializeIoUring initializes the io_uring send ring for the connection
func (c *srtConn) initializeIoUring(config srtConnConfig) {
	if !c.config.IoUringEnabled {
		return
	}

	// Store socket FD
	c.sendRingFd = config.socketFd

	// Determine ring size (default: 64)
	ringSize := uint32(64)
	if c.config.IoUringSendRingSize > 0 {
		ringSize = uint32(c.config.IoUringSendRingSize)
	}

	// Create io_uring ring using giouring
	ring := giouring.NewRing()
	err := ring.QueueInit(ringSize, 0) // ringSize entries, no flags
	if err != nil {
		// io_uring should be available - this is an unexpected error
		c.log("connection:io_uring:error", func() string {
			return fmt.Sprintf("failed to create io_uring ring: %v", err)
		})
		// Continue without io_uring - connection will fall back to regular sends
		return
	}

	c.sendRing = ring // Store as interface{} to allow conditional compilation

	// Pre-compute sockaddr structure for UDP sends (reused for connection lifetime)
	// The remote address is known and doesn't change during the connection
	if c.remoteAddr != nil {
		c.sendSockaddrLen = convertUDPAddrToSockaddr(c.remoteAddr, &c.sendSockaddr)
	}

	// Initialize per-connection send buffer pool (eliminates lock contention)
	c.sendBufferPool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}

	// Initialize completion tracking
	c.sendCompletions = make(map[uint64]*sendCompletionInfo)

	// Create context for completion handler (inherits from connection context)
	c.sendCompCtx, c.sendCompCancel = context.WithCancel(c.ctx)

	// Start completion handler goroutine (polls CQEs directly)
	c.sendCompWg.Add(1)
	go c.sendCompletionHandler(c.sendCompCtx)

	// Update onSend callback to use connection's io_uring send method
	c.onSend = c.send
}

// cleanupIoUring cleans up the io_uring send ring for the connection
func (c *srtConn) cleanupIoUring() {
	if c.sendRing == nil {
		return
	}

	// Stop completion handler
	if c.sendCompCancel != nil {
		c.sendCompCancel()
	}

	// IMPORTANT: Close the ring FIRST to wake up blocked WaitCQE()
	// WaitCQE() will return EBADF when the ring is closed, allowing
	// the completion handler to exit cleanly. If we wait before closing,
	// the handler stays blocked in WaitCQE() and we timeout.
	ring, ok := c.sendRing.(*giouring.Ring)
	if ok {
		ring.QueueExit()
	}

	// Wait for completion handler to finish (with timeout)
	done := make(chan struct{})
	go func() {
		c.sendCompWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Completion handler finished
	case <-time.After(2 * time.Second):
		// Timeout - log warning but continue (reduced from 5s since QueueExit should wake it)
		c.log("connection:io_uring:cleanup", func() string {
			return "timeout waiting for completion handler"
		})
	}

	// Note: drainCompletions() is NOT called here because the ring is already closed.
	// The completion handler processes all pending CQEs until it receives EBADF from
	// WaitCQE(), so there's nothing left to drain. Calling PeekCQE() after QueueExit()
	// would cause a segfault.

	// Clean up completion map and return all buffers to pool
	c.sendCompLock.Lock()
	for _, compInfo := range c.sendCompletions {
		compInfo.buffer.Reset()
		c.sendBufferPool.Put(compInfo.buffer)
	}
	c.sendCompletions = nil
	c.sendCompLock.Unlock()
}

// sendIoUring implements the Linux-specific io_uring send path
func (c *srtConn) sendIoUring(p packet.Packet) {
	// Type assert to *giouring.Ring (only available on Linux)
	ring, ok := c.sendRing.(*giouring.Ring)
	if !ok {
		// This shouldn't happen if io_uring is enabled, but handle gracefully
		c.log("connection:send:error", func() string {
			return "io_uring ring type assertion failed"
		})
		// Track error (ring type assertion failed)
		if c.metrics != nil {
			metrics.IncrementSendErrorMetrics(c.metrics, true, metrics.DropReasonIoUring)
		}
		p.Decommission()
		return
	}

	// Get buffer from per-connection pool (no lock contention, no Reset on critical path!)
	sendBuffer := c.sendBufferPool.Get().(*bytes.Buffer)

	// Marshal packet into buffer
	if err := p.Marshal(sendBuffer); err != nil {
		sendBuffer.Reset() // Reset before putting back
		c.sendBufferPool.Put(sendBuffer)
		// Track marshal error
		if c.metrics != nil {
			metrics.IncrementSendMetrics(c.metrics, p, true, false, metrics.DropReasonMarshal)
		}
		p.Decommission()
		c.log("connection:send:error", func() string {
			return fmt.Sprintf("marshalling packet failed: %v", err)
		})
		return
	}

	// Store packet for metrics tracking (before decommissioning control packets)
	// For control packets, we capture the control type BEFORE decommissioning,
	// then increment the counter directly since IncrementSendMetrics can't classify nil packets.
	packetForMetrics := p
	var controlType packet.CtrlType
	isControlPacket := p.Header().IsControlPacket
	if isControlPacket {
		controlType = p.Header().ControlType
		// Decommission control packets immediately (they won't be retransmitted)
		p.Decommission()
		packetForMetrics = nil // Can't use after decommission
	}
	// Data packets are handled by congestion control (may be retransmitted)

	// Get underlying slice (valid as long as buffer isn't modified)
	bufferSlice := sendBuffer.Bytes()

	// Generate unique request ID using atomic counter
	requestID := c.sendRequestID.Add(1)

	// Prepare syscall structures for UDP send
	// The remote address is known and pre-computed at connection initialization
	// Note: Even though the listener uses an unconnected UDP socket (shared across connections),
	// each connection knows its remote address and it doesn't change, so we always use PrepareSendMsg
	var iovec syscall.Iovec
	iovec.Base = &bufferSlice[0]
	iovec.SetLen(len(bufferSlice))

	var msg syscall.Msghdr
	// Use pre-computed sockaddr (computed once at connection init, reused for all sends)
	// The sockaddr structure is stored in the connection and remains valid for its lifetime
	msg.Name = (*byte)(unsafe.Pointer(&c.sendSockaddr))
	msg.Namelen = c.sendSockaddrLen
	msg.Iov = &iovec
	msg.Iovlen = 1

	// Create minimal completion info (buffer and packet info for metrics)
	compInfo := &sendCompletionInfo{
		buffer:    sendBuffer,       // Keep buffer alive until send completes
		packet:    packetForMetrics, // Packet for metrics (nil for control packets)
		isIoUring: true,             // Track path
	}

	// Store completion info in map (protected by lock)
	c.sendCompLock.Lock()
	c.sendCompletions[requestID] = compInfo
	c.sendCompLock.Unlock()

	// Get SQE from ring with retry loop (ring may be temporarily full)
	var sqe *giouring.SubmissionQueueEntry
	const maxRetries = 3
	for i := 0; i < maxRetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break // Got an SQE, proceed
		}

		// Ring full - wait a bit and retry (completions may free up space)
		if i < maxRetries-1 {
			time.Sleep(100 * time.Microsecond)
		}
	}

	if sqe == nil {
		// Ring still full after retries - clean up
		c.sendCompLock.Lock()
		delete(c.sendCompletions, requestID)
		c.sendCompLock.Unlock()

		sendBuffer.Reset() // Reset before putting back
		c.sendBufferPool.Put(sendBuffer)

		// Track ring full error (packet dropped)
		if c.metrics != nil {
			// Use packetForMetrics if available, otherwise nil (will track as generic error)
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, false, metrics.DropReasonRingFull)
		}

		c.log("connection:send:error", func() string {
			return "io_uring ring full after retries"
		})
		return
	}

	// Prepare send operation
	// Always use PrepareSendMsg with pre-computed address
	// The remote address is known and doesn't change during the connection lifetime
	sqe.PrepareSendMsg(c.sendRingFd, &msg, 0)

	// Store request ID in user data for completion correlation
	sqe.SetData64(requestID)

	// Submit to ring with retry loop (may be temporarily unavailable)
	// Retry for transient errors (EINTR, EAGAIN) similar to GetSQE retry logic
	var err error
	const maxSubmitRetries = 3
	for i := 0; i < maxSubmitRetries; i++ {
		_, err = ring.Submit()
		if err == nil {
			break // Submission successful
		}

		// Only retry transient errors (EINTR, EAGAIN)
		if err != syscall.EINTR && err != syscall.EAGAIN {
			// Fatal error - don't retry
			break
		}

		// Transient error - wait and retry
		if i < maxSubmitRetries-1 {
			time.Sleep(100 * time.Microsecond) // Same delay as GetSQE retry
		}
	}

	if err != nil {
		// Submission failed - clean up
		c.sendCompLock.Lock()
		delete(c.sendCompletions, requestID)
		c.sendCompLock.Unlock()

		sendBuffer.Reset() // Reset before putting back
		c.sendBufferPool.Put(sendBuffer)

		// Track submit error
		if c.metrics != nil {
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, false, metrics.DropReasonSubmit)
		}

		c.log("connection:send:error", func() string {
			return fmt.Sprintf("failed to submit send request: %v", err)
		})
		return
	}

	// Request submitted successfully - track submission
	// This counter helps detect packets that are submitted but never complete
	if c.metrics != nil {
		c.metrics.PktSentSubmitted.Add(1)
	}

	// Request submitted successfully - track success
	// Note: We track success here (not in completion handler) because:
	// 1. Control packets are decommissioned, so we can't get type in completion handler
	// 2. Submission success means the packet will be sent (completion errors are rare)
	if c.metrics != nil {
		if isControlPacket {
			// Control packets were decommissioned, so we use the captured controlType
			c.metrics.PktSentIoUring.Add(1)
			metrics.IncrementSendControlMetric(c.metrics, controlType)
		} else {
			// Data packets: use standard path
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, true, 0)
		}
	}

	// Completion will be handled asynchronously by completion handler
	// Errors in completion handler will be tracked separately
}

// sendCompletionHandler processes io_uring send completions using polling (not blocking WaitCQE).
// This allows the handler to check ctx.Done() regularly and exit promptly when
// the context is cancelled, without waiting for QueueExit() to be called.
func (c *srtConn) sendCompletionHandler(ctx context.Context) {
	defer c.sendCompWg.Done()

	ring, ok := c.sendRing.(*giouring.Ring)
	if !ok {
		return
	}

	for {
		// Check for context cancellation first
		select {
		case <-ctx.Done():
			// Connection closing - exit immediately
			// Note: Do NOT call drainCompletions() here - the ring may already be closed
			// by QueueExit() in cleanupIoUring(), which would cause a SIGSEGV.
			return
		default:
		}

		// Use non-blocking PeekCQE instead of blocking WaitCQE
		// This allows us to check ctx.Done() regularly and exit promptly
		cqe, err := ring.PeekCQE()
		if err != nil {
			// EBADF means ring was closed via QueueExit()
			if err == syscall.EBADF {
				return // Ring closed - normal shutdown
			}

			// EAGAIN means no completions available - sleep and retry
			if err == syscall.EAGAIN {
				select {
				case <-ctx.Done():
					return
				case <-time.After(ioUringPollInterval):
					continue
				}
			}

			// EINTR is normal (interrupted by signal) - retry immediately
			if err == syscall.EINTR {
				continue
			}

			// Other errors - log and continue
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("error peeking completion: %v", err)
			})
			continue
		}

		// Get request ID from completion user data
		requestID := cqe.UserData

		// Look up completion info
		c.sendCompLock.Lock()
		compInfo, exists := c.sendCompletions[requestID]
		if !exists {
			c.sendCompLock.Unlock()
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("completion for unknown request ID: %d", requestID)
			})
			ring.CQESeen(cqe)
			continue
		}
		delete(c.sendCompletions, requestID)
		c.sendCompLock.Unlock()

		// Process completion
		buffer := compInfo.buffer
		if cqe.Res < 0 {
			errno := -cqe.Res
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("send failed: %s (errno %d)", syscall.Errno(errno).Error(), errno)
			})
			// Track send error (io_uring completion error)
			// Note: packet may be nil for control packets (already decommissioned)
			if c.metrics != nil {
				if compInfo.packet != nil {
					// We have the packet - track with type
					metrics.IncrementSendMetrics(c.metrics, compInfo.packet, compInfo.isIoUring, false, metrics.DropReasonIoUring)
				} else {
					// No packet (control packet decommissioned) - track generic error
					metrics.IncrementSendErrorMetrics(c.metrics, compInfo.isIoUring, metrics.DropReasonIoUring)
				}
			}
		} else {
			bytesSent := int(cqe.Res)
			if bytesSent < len(buffer.Bytes()) {
				c.log("connection:send:completion:warning", func() string {
					return fmt.Sprintf("partial send: %d/%d bytes", bytesSent, len(buffer.Bytes()))
				})
				// Partial send - track as error
				if c.metrics != nil {
					if compInfo.packet != nil {
						metrics.IncrementSendMetrics(c.metrics, compInfo.packet, compInfo.isIoUring, false, metrics.DropReasonIoUring)
					} else {
						metrics.IncrementSendErrorMetrics(c.metrics, compInfo.isIoUring, metrics.DropReasonIoUring)
					}
				}
			}
			// Full send success - already tracked in sendIoUring() after successful submit
		}

		ring.CQESeen(cqe)
		buffer.Reset()
		c.sendBufferPool.Put(buffer)
	}
}

// drainCompletions processes any remaining completions during shutdown
func (c *srtConn) drainCompletions() {
	ring, ok := c.sendRing.(*giouring.Ring)
	if !ok || ring == nil {
		return
	}

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case <-timeout.C:
			// Timeout - give up on remaining completions
			c.log("connection:send:drain", func() string {
				return "timeout draining completions"
			})
			return

		default:
			// Try to get completion (non-blocking)
			cqe, err := ring.PeekCQE()
			if err != nil {
				// EBADF means ring was closed via QueueExit() - exit immediately
				// This is the normal case when drainCompletions is called during shutdown
				if err == syscall.EBADF {
					return
				}

				if err == syscall.EAGAIN {
					// No completions available - check if map is empty
					c.sendCompLock.RLock()
					empty := len(c.sendCompletions) == 0
					c.sendCompLock.RUnlock()

					if empty {
						return // All completions processed
					}

					// Wait a bit before checking again
					time.Sleep(10 * time.Millisecond)
					continue
				}

				// Other error
				c.log("connection:send:drain:error", func() string {
					return fmt.Sprintf("error peeking completion: %v", err)
				})
				return
			}

			// Process completion
			requestID := cqe.UserData

			c.sendCompLock.Lock()
			compInfo, exists := c.sendCompletions[requestID]
			if !exists {
				c.sendCompLock.Unlock()
				ring.CQESeen(cqe)
				continue
			}
			delete(c.sendCompletions, requestID)
			c.sendCompLock.Unlock()

			// Cleanup
			compInfo.buffer.Reset() // Reset before putting back
			c.sendBufferPool.Put(compInfo.buffer)

			ring.CQESeen(cqe)
		}
	}
}

// send submits a packet to the connection's io_uring send ring
func (c *srtConn) send(p packet.Packet) {
	// If io_uring is not enabled or ring is not available, fall back to original send
	if c.sendRing == nil {
		// This shouldn't happen if io_uring is enabled, but handle gracefully
		c.log("connection:send:error", func() string {
			return "io_uring ring not available, packet dropped"
		})
		// Track error (ring not available)
		if c.metrics != nil {
			metrics.IncrementSendErrorMetrics(c.metrics, true, metrics.DropReasonIoUring)
		}
		p.Decommission()
		return
	}

	// Call Linux-specific send implementation
	c.sendIoUring(p)
}
