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

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/randomizedcoder/giouring"
)

// ioUringWaitTimeout is the timeout for WaitCQETimeout when waiting for completions.
// The kernel blocks until either:
//  1. A completion arrives (returns immediately - zero latency!)
//  2. Timeout expires (returns ETIME, allows ctx.Done() check)
//
// 10ms provides good balance: responsive to completions AND shutdown signals.
// Unlike polling+sleep, this has ZERO latency when completions arrive.
var ioUringWaitTimeout = syscall.NsecToTimespec((10 * time.Millisecond).Nanoseconds())

const (
	// ioUringRetryBackoff is the sleep duration between retries when GetSQE()
	// or Submit() fails transiently. Short enough to be responsive, long enough
	// to allow completions to free ring slots.
	ioUringRetryBackoff = 100 * time.Microsecond

	// ioUringMaxGetSQERetries is the maximum number of retries when GetSQE()
	// returns nil (ring temporarily full). After this, the packet is dropped.
	ioUringMaxGetSQERetries = 3

	// ioUringMaxSubmitRetries is the maximum number of retries when Submit()
	// returns a transient error (EINTR, EAGAIN). After this, the packet is dropped.
	ioUringMaxSubmitRetries = 3
)

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
// Following context_and_cancellation_design.md pattern:
// 1. Cancel context (signals handler to exit)
// 2. Wait for WaitGroup (handler has exited)
// 3. Only then clean up resources (QueueExit)
func (c *srtConn) cleanupIoUring() {
	if c.sendRing == nil {
		return
	}

	// Step 1: Cancel the completion handler's context
	// This signals the handler to exit on its next ctx.Done() check
	if c.sendCompCancel != nil {
		c.sendCompCancel()
	}

	// Step 2: Wait for completion handler to exit BEFORE calling QueueExit()
	// The handler checks ctx.Done() at the top of each loop iteration.
	// With a 10ms WaitCQETimeout, the handler will exit within ~10ms.
	//
	// We MUST wait for the handler to exit completely because:
	// 1. QueueExit() unmaps the ring memory
	// 2. If handler is inside WaitCQETimeout(), the syscall will return
	// 3. The giouring library then tries to peek at the CQ -> SIGSEGV!
	done := make(chan struct{})
	go func() {
		c.sendCompWg.Wait()
		close(done)
	}()

	handlerExited := false
	select {
	case <-done:
		handlerExited = true
	case <-time.After(2 * time.Second):
		// CRITICAL: Handler did not exit - DO NOT call QueueExit
		// Minor resource leak is better than SIGSEGV crash
		c.log("connection:io_uring:cleanup", func() string {
			return "CRITICAL: completion handler did not exit within 2s - skipping QueueExit to prevent SIGSEGV"
		})
	}

	// Step 3: Only close ring if handler has exited
	if handlerExited {
		ring, ok := c.sendRing.(*giouring.Ring)
		if ok {
			ring.QueueExit()
		}
		// Set sendRing to nil so any late sends fail gracefully
		// (type assertion in sendIoUring will fail)
		c.sendRing = nil
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
	// Note: If sendRing is nil (set by cleanupIoUring after QueueExit),
	// this type assertion will fail and we'll handle gracefully below
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
	for i := 0; i < ioUringMaxGetSQERetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break // Got an SQE, proceed
		}

		// Track retry (ring temporarily full)
		if c.metrics != nil {
			c.metrics.IoUringSendGetSQERetries.Add(1)
		}

		// Ring full - wait a bit and retry (completions may free up space)
		if i < ioUringMaxGetSQERetries-1 {
			time.Sleep(ioUringRetryBackoff)
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
			c.metrics.IoUringSendSubmitRingFull.Add(1)
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
	for i := 0; i < ioUringMaxSubmitRetries; i++ {
		_, err = ring.Submit()
		if err == nil {
			break // Submission successful
		}

		// Only retry transient errors (EINTR, EAGAIN)
		if err != syscall.EINTR && err != syscall.EAGAIN {
			// Fatal error - don't retry
			break
		}

		// Track retry (transient error)
		if c.metrics != nil {
			c.metrics.IoUringSendSubmitRetries.Add(1)
		}

		// Transient error - wait and retry
		if i < ioUringMaxSubmitRetries-1 {
			time.Sleep(ioUringRetryBackoff)
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
			c.metrics.IoUringSendSubmitError.Add(1)
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, false, metrics.DropReasonSubmit)
		}

		c.log("connection:send:error", func() string {
			return fmt.Sprintf("failed to submit send request: %v", err)
		})
		return
	}

	// Request submitted successfully - track submission metrics
	if c.metrics != nil {
		c.metrics.IoUringSendSubmitSuccess.Add(1)
		c.metrics.PktSentSubmitted.Add(1) // Legacy counter for compatibility
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

// sendCompletionHandler processes io_uring send completions using blocking wait with timeout.
// WaitCQETimeout blocks in the kernel until either:
//  1. A completion arrives (returns immediately - zero latency!)
//  2. Timeout expires (returns ETIME, allows ctx.Done() check)
//  3. Ring is closed (returns EBADF, normal shutdown)
//
// This replaces the inefficient polling+sleep approach where we could sleep
// for up to 10ms AFTER a completion arrived.
func (c *srtConn) sendCompletionHandler(ctx context.Context) {
	defer c.sendCompWg.Done()

	ring, ok := c.sendRing.(*giouring.Ring)
	if !ok {
		return
	}

	for {
		// Check for context cancellation at top of loop (non-blocking)
		// This is the standard Go pattern - no atomic flag needed
		// Ring is guaranteed to stay mapped because cleanupIoUring waits for us to exit
		select {
		case <-ctx.Done():
			// Context cancelled - exit gracefully
			if c.metrics != nil {
				c.metrics.IoUringSendCompletionCtxCancelled.Add(1)
			}
			return
		default:
		}

		// Block waiting for completion OR timeout (kernel wakes us immediately on completion)
		cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
		if err != nil {
			// EBADF means ring was closed via QueueExit()
			if err == syscall.EBADF {
				if c.metrics != nil {
					c.metrics.IoUringSendCompletionEBADF.Add(1)
				}
				return // Ring closed - normal shutdown
			}

			// ETIME means timeout expired - loop back to check ctx.Done()
			if err == syscall.ETIME {
				if c.metrics != nil {
					c.metrics.IoUringSendCompletionTimeout.Add(1)
				}
				continue
			}

			// EINTR is normal (interrupted by signal) - retry immediately
			if err == syscall.EINTR {
				if c.metrics != nil {
					c.metrics.IoUringSendCompletionEINTR.Add(1)
				}
				continue
			}

			// Other errors - log and continue
			if c.metrics != nil {
				c.metrics.IoUringSendCompletionError.Add(1)
			}
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("error waiting for completion: %v", err)
			})
			continue
		}

		// Success - completion received
		if c.metrics != nil {
			c.metrics.IoUringSendCompletionSuccess.Add(1)
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
		// No extra flag check needed here - ctx.Done() at top of loop is sufficient
		// With 10ms WaitCQETimeout, we'll check context within 10ms anyway
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
