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

	"github.com/randomizedcoder/giouring"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// sendRingState holds all state for a single send io_uring ring.
// Each ring has its own dedicated completion handler goroutine.
// Unlike receive paths, send requires a per-ring lock because:
// - Sender goroutine writes to completion map during submit
// - Completion handler reads/deletes from completion map
// See multi_iouring_design.md Section 4.3 for design rationale.
type sendRingState struct {
	ring        *giouring.Ring                 // io_uring ring
	completions map[uint64]*sendCompletionInfo // Maps request ID to completion info
	nextID      uint64                         // Request ID counter (atomic for multi-writer)
	fd          int                            // Socket fd (shared, read-only)
	ringIndex   int                            // Ring index for metrics/logging
	compLock    sync.Mutex                     // Needed: sender writes, completer reads
}

// newSendRingState creates a new sendRingState with initialized completion map
func newSendRingState(ring *giouring.Ring, fd int, ringIndex int) *sendRingState {
	return &sendRingState{
		ring:        ring,
		completions: make(map[uint64]*sendCompletionInfo),
		nextID:      0,
		fd:          fd,
		ringIndex:   ringIndex,
	}
}

// getNextID returns the next request ID for this ring
// Note: In multi-ring mode, this is called from sender goroutine with lock held
func (srs *sendRingState) getNextID() uint64 {
	srs.nextID++
	return srs.nextID
}

// Helper functions for incrementing send per-ring metrics.
// These use ConnectionMetrics.IoUringSendRingMetrics with per-ring counters.
// See multi_iouring_design.md Section 5.12 for the unified metrics approach.

func (c *srtConn) incrementSendGetSQERetries(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].GetSQERetries.Add(1)
	}
}

func (c *srtConn) incrementSendSubmitRingFull(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].SubmitRingFull.Add(1)
	}
}

func (c *srtConn) incrementSendSubmitRetries(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].SubmitRetries.Add(1)
	}
}

func (c *srtConn) incrementSendSubmitError(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].SubmitError.Add(1)
	}
}

func (c *srtConn) incrementSendSubmitSuccess(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].SubmitSuccess.Add(1)
	}
}

func (c *srtConn) incrementSendCompletionCtxCancelled(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].CompletionCtxCancelled.Add(1)
	}
}

func (c *srtConn) incrementSendCompletionEBADF(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].CompletionEBADF.Add(1)
	}
}

func (c *srtConn) incrementSendCompletionTimeout(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].CompletionTimeout.Add(1)
	}
}

func (c *srtConn) incrementSendCompletionEINTR(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].CompletionEINTR.Add(1)
	}
}

func (c *srtConn) incrementSendCompletionError(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].CompletionError.Add(1)
	}
}

func (c *srtConn) incrementSendCompletionSuccess(ringIdx int) {
	if c.metrics != nil && c.metrics.IoUringSendRingMetrics != nil && ringIdx < len(c.metrics.IoUringSendRingMetrics) {
		c.metrics.IoUringSendRingMetrics[ringIdx].CompletionSuccess.Add(1)
	}
}

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

// initializeIoUring initializes the io_uring send ring(s) for the connection
// When IoUringSendRingCount > 1, creates multiple independent rings for parallel processing.
// See multi_iouring_design.md Section 4.3 for design rationale.
func (c *srtConn) initializeIoUring(config srtConnConfig) {
	if !c.config.IoUringEnabled {
		return
	}

	// Store socket FD (shared by all rings)
	c.sendRingFd = config.socketFd

	// Determine ring size (default: 64)
	ringSize := uint32(64)
	if c.config.IoUringSendRingSize > 0 {
		ringSize = uint32(c.config.IoUringSendRingSize)
	}

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

	// Create context for completion handler (inherits from connection context)
	c.sendCompCtx, c.sendCompCancel = context.WithCancel(c.ctx)

	// Determine ring count (default: 1)
	ringCount := c.config.IoUringSendRingCount
	if ringCount <= 0 {
		ringCount = 1
	}

	// Multi-ring mode: create multiple independent ring states
	if ringCount > 1 {
		c.initializeIoUringSendMultiRing(ringSize, ringCount)
		return
	}

	// Single-ring mode (legacy path)
	c.initializeIoUringSendSingleRing(ringSize)
}

// initializeIoUringSendSingleRing initializes a single io_uring send ring (legacy path)
func (c *srtConn) initializeIoUringSendSingleRing(ringSize uint32) {
	// Initialize per-ring metrics (unified approach - even single-ring uses per-ring metrics)
	// This MUST be done before starting handler, otherwise metric increments are silently dropped
	if c.metrics != nil {
		c.metrics.IoUringSendRingMetrics = metrics.NewIoUringRingMetrics(1)
		c.metrics.IoUringSendRingCount = 1
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

	// Initialize completion tracking
	c.sendCompletions = make(map[uint64]*sendCompletionInfo)

	// Start completion handler goroutine (polls CQEs directly)
	c.sendCompWg.Add(1)
	go c.sendCompletionHandler(c.sendCompCtx)

	// Update onSend callback to use connection's io_uring send method
	c.onSend = c.send
}

// initializeIoUringSendMultiRing initializes multiple independent io_uring send rings
// Each ring has its own completion handler for parallel processing.
// See multi_iouring_design.md Section 4.3 for design rationale.
func (c *srtConn) initializeIoUringSendMultiRing(ringSize uint32, ringCount int) {
	// Initialize per-ring metrics FIRST (unified approach - always use per-ring metrics)
	// This MUST be done before starting handlers, otherwise metric increments are silently dropped
	if c.metrics != nil {
		c.metrics.IoUringSendRingMetrics = metrics.NewIoUringRingMetrics(ringCount)
		c.metrics.IoUringSendRingCount = ringCount
	}

	// Create slice for ring states
	c.sendRingStates = make([]*sendRingState, 0, ringCount)

	// Create each ring and its handler
	for i := 0; i < ringCount; i++ {
		// Create io_uring ring
		ring := giouring.NewRing()
		err := ring.QueueInit(ringSize, 0)
		if err != nil {
			// Cleanup any rings already created
			c.cleanupIoUringSendMultiRing()
			c.log("connection:io_uring:error", func() string {
				return fmt.Sprintf("failed to create io_uring send ring %d: %v", i, err)
			})
			return
		}

		// Create ring state (owns completion map with per-ring lock)
		state := newSendRingState(ring, c.sendRingFd, i)
		c.sendRingStates = append(c.sendRingStates, state)

		c.log("connection:io_uring:ring", func() string {
			return fmt.Sprintf("send ring %d created successfully (fd=%d)", i, c.sendRingFd)
		})

		// Start independent completion handler for this ring
		c.sendCompWg.Add(1)
		go c.sendCompletionHandlerIndependent(c.sendCompCtx, state)
	}

	// Update onSend callback to use connection's io_uring send method
	c.onSend = c.sendMultiRing

	c.log("connection:io_uring:init", func() string {
		return fmt.Sprintf("io_uring multi-ring send initialized: rings=%d (states=%d), ring_size=%d, fd=%d",
			ringCount, len(c.sendRingStates), ringSize, c.sendRingFd)
	})
}

// cleanupIoUring cleans up the io_uring send ring(s) for the connection
// Following context_and_cancellation_design.md pattern:
// 1. Cancel context (signals handler(s) to exit)
// 2. Wait for WaitGroup (handler(s) have exited)
// 3. Only then clean up resources (QueueExit)
func (c *srtConn) cleanupIoUring() {
	// Multi-ring mode: use dedicated cleanup
	if len(c.sendRingStates) > 0 {
		c.cleanupIoUringSendMultiRing()
		return
	}

	// Single-ring mode (legacy path)
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

	// Note: The completion handler processes all pending CQEs until it receives EBADF from
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
	// Check if connection is shutting down (context canceled)
	// This prevents accessing the io_uring ring after it's been closed
	select {
	case <-c.ctx.Done():
		// Connection shutting down - don't try to send
		p.Decommission()
		return
	default:
		// Not shutting down - proceed
	}

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
	sendBuffer, ok := c.sendBufferPool.Get().(*bytes.Buffer)
	if !ok {
		// Pool should only contain *bytes.Buffer, this is a programming error
		panic("sendBufferPool contained non-*bytes.Buffer value")
	}

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
			return fmt.Sprintf("marshaling packet failed: %v", err)
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
		c.incrementSendGetSQERetries(0) // ringIdx=0 for single-ring mode

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
		c.incrementSendSubmitRingFull(0) // ringIdx=0 for single-ring mode
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
		c.incrementSendSubmitRetries(0) // ringIdx=0 for single-ring mode

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
		c.incrementSendSubmitError(0) // ringIdx=0 for single-ring mode
		if c.metrics != nil {
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, false, metrics.DropReasonSubmit)
		}

		c.log("connection:send:error", func() string {
			return fmt.Sprintf("failed to submit send request: %v", err)
		})
		return
	}

	// Request submitted successfully - track submission metrics
	c.incrementSendSubmitSuccess(0) // ringIdx=0 for single-ring mode
	if c.metrics != nil {
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
			// Context canceled - exit gracefully
			c.incrementSendCompletionCtxCancelled(0) // ringIdx=0 for single-ring mode
			return
		default:
		}

		// Block waiting for completion OR timeout (kernel wakes us immediately on completion)
		cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
		if err != nil {
			// EBADF means ring was closed via QueueExit()
			if err == syscall.EBADF {
				c.incrementSendCompletionEBADF(0) // ringIdx=0 for single-ring mode
				return                            // Ring closed - normal shutdown
			}

			// ETIME means timeout expired - loop back to check ctx.Done()
			if err == syscall.ETIME {
				c.incrementSendCompletionTimeout(0) // ringIdx=0 for single-ring mode
				continue
			}

			// EINTR is normal (interrupted by signal) - retry immediately
			if err == syscall.EINTR {
				c.incrementSendCompletionEINTR(0) // ringIdx=0 for single-ring mode
				continue
			}

			// Other errors - log and continue
			c.incrementSendCompletionError(0) // ringIdx=0 for single-ring mode
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("error waiting for completion: %v", err)
			})
			continue
		}

		// Success - completion received
		c.incrementSendCompletionSuccess(0) // ringIdx=0 for single-ring mode

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

// send submits a packet to the connection's io_uring send ring (single-ring mode)
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

// =============================================================================
// Multi-Ring Support Functions
// =============================================================================

// sendMultiRing submits a packet to the connection's io_uring send rings (multi-ring mode)
// Uses round-robin ring selection for load distribution
func (c *srtConn) sendMultiRing(p packet.Packet) {
	// DEBUG: Log entry to help trace packet flow
	c.log("connection:send:multiring", func() string {
		if p == nil {
			return "sendMultiRing called with nil packet"
		}
		return fmt.Sprintf("sendMultiRing: isControl=%v, ctrlType=%d, rings=%d",
			p.Header().IsControlPacket, p.Header().ControlType, len(c.sendRingStates))
	})

	// Check if connection is shutting down (context canceled)
	// This prevents accessing the io_uring rings after they're been closed
	select {
	case <-c.ctx.Done():
		// Connection shutting down - don't try to send
		p.Decommission()
		return
	default:
		// Not shutting down - proceed
	}

	if len(c.sendRingStates) == 0 {
		c.log("connection:send:error", func() string {
			return "io_uring multi-ring not available, packet dropped"
		})
		if c.metrics != nil {
			metrics.IncrementSendErrorMetrics(c.metrics, true, metrics.DropReasonIoUring)
		}
		p.Decommission()
		return
	}

	// Select ring using round-robin
	ringCount := uint32(len(c.sendRingStates))
	idx := c.sendRingNextIdx.Add(1) % ringCount
	state := c.sendRingStates[idx]

	// Call multi-ring send implementation
	c.sendIoUringToRing(state, p)
}

// sendIoUringToRing sends a packet using a specific ring (multi-ring mode)
// Uses per-ring lock since sender and completer access concurrently
func (c *srtConn) sendIoUringToRing(state *sendRingState, p packet.Packet) {
	// Check if connection is shutting down (context canceled)
	// This prevents accessing the io_uring ring after it's been closed
	select {
	case <-c.ctx.Done():
		// Connection shutting down - don't try to send
		p.Decommission()
		return
	default:
		// Not shutting down - proceed
	}

	ring := state.ring
	if ring == nil {
		p.Decommission()
		return
	}

	// Get serialization buffer from per-connection pool
	sendBuffer, ok := c.sendBufferPool.Get().(*bytes.Buffer)
	if !ok {
		// Pool should only contain *bytes.Buffer, this is a programming error
		panic("sendBufferPool contained non-*bytes.Buffer value")
	}

	// DEBUG: Check packet state before marshal
	// This helps diagnose use-after-decommission bugs
	if p == nil {
		sendBuffer.Reset()
		c.sendBufferPool.Put(sendBuffer)
		c.log("connection:send:error", func() string {
			return "packet is nil (ring " + fmt.Sprint(state.ringIndex) + ")"
		})
		return
	}

	// Marshal packet into buffer
	if err := p.Marshal(sendBuffer); err != nil {
		sendBuffer.Reset()
		c.sendBufferPool.Put(sendBuffer)
		if c.metrics != nil {
			metrics.IncrementSendMetrics(c.metrics, p, true, false, metrics.DropReasonMarshal)
		}
		// Log more details about the failing packet
		c.log("connection:send:error", func() string {
			return fmt.Sprintf("marshaling packet failed: %v (ring=%d, isControl=%v, ctrlType=%d)",
				err, state.ringIndex, p.Header().IsControlPacket, p.Header().ControlType)
		})
		p.Decommission()
		return
	}

	// Store packet for metrics tracking (before decommissioning control packets)
	packetForMetrics := p
	var controlType packet.CtrlType
	isControlPacket := p.Header().IsControlPacket
	if isControlPacket {
		controlType = p.Header().ControlType
		p.Decommission()
		packetForMetrics = nil
	}

	// Get buffer bytes
	bufferSlice := sendBuffer.Bytes()

	// Get SQE with retry (under lock for multi-ring mode)
	state.compLock.Lock()

	var sqe *giouring.SubmissionQueueEntry
	for i := 0; i < ioUringMaxGetSQERetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break
		}
		c.incrementSendGetSQERetries(state.ringIndex) // per-ring metrics
		if i < ioUringMaxGetSQERetries-1 {
			state.compLock.Unlock()
			time.Sleep(ioUringRetryBackoff)
			state.compLock.Lock()
		}
	}

	if sqe == nil {
		state.compLock.Unlock()
		c.log("connection:send:error", func() string {
			return fmt.Sprintf("io_uring ring %d full after retries", state.ringIndex)
		})
		sendBuffer.Reset()
		c.sendBufferPool.Put(sendBuffer)
		c.incrementSendSubmitRingFull(state.ringIndex) // per-ring metrics
		if c.metrics != nil {
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, false, metrics.DropReasonRingFull)
		}
		return
	}

	// Generate unique request ID (under lock)
	requestID := state.getNextID()

	// Prepare sendmsg operation
	var iovec syscall.Iovec
	iovec.Base = &bufferSlice[0]
	iovec.SetLen(len(bufferSlice))

	var msg syscall.Msghdr
	msg.Name = (*byte)(unsafe.Pointer(&c.sendSockaddr))
	msg.Namelen = c.sendSockaddrLen
	msg.Iov = &iovec
	msg.Iovlen = 1

	sqe.PrepareSendMsg(state.fd, &msg, 0)
	sqe.SetData64(requestID)

	// Store completion info for later (under lock)
	state.completions[requestID] = &sendCompletionInfo{
		buffer:    sendBuffer,
		packet:    packetForMetrics,
		isIoUring: true,
	}

	// Submit to ring with retry (under lock)
	var submitErr error
	for i := 0; i < ioUringMaxSubmitRetries; i++ {
		_, submitErr = ring.Submit()
		if submitErr == nil {
			break
		}
		if submitErr != syscall.EINTR && submitErr != syscall.EAGAIN {
			break
		}
		c.incrementSendSubmitRetries(state.ringIndex) // per-ring metrics
		if i < ioUringMaxSubmitRetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if submitErr != nil {
		delete(state.completions, requestID)
		state.compLock.Unlock()
		c.log("connection:send:submit:error", func() string {
			return fmt.Sprintf("failed to submit to ring %d: %v", state.ringIndex, submitErr)
		})
		sendBuffer.Reset()
		c.sendBufferPool.Put(sendBuffer)
		c.incrementSendSubmitError(state.ringIndex) // per-ring metrics
		if c.metrics != nil {
			metrics.IncrementSendMetrics(c.metrics, packetForMetrics, true, false, metrics.DropReasonSubmit)
		}
		return
	}

	state.compLock.Unlock()

	// Track success
	c.incrementSendSubmitSuccess(state.ringIndex) // per-ring metrics
	if c.metrics != nil {
		c.metrics.PktSentSubmitted.Add(1)
		if isControlPacket {
			c.metrics.PktSentIoUring.Add(1)
			metrics.IncrementSendControlMetric(c.metrics, controlType)
		}
	}
}

// sendCompletionHandlerIndependent is the completion handler for a specific send ring (multi-ring mode)
// Uses per-ring lock since sender and completer access completion map concurrently
// See multi_iouring_design.md Section 4.3 for design rationale.
func (c *srtConn) sendCompletionHandlerIndependent(ctx context.Context, state *sendRingState) {
	defer c.sendCompWg.Done()

	ring := state.ring
	if ring == nil {
		return
	}

	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Block waiting for completion OR timeout
		cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
		if err != nil {
			if err == syscall.EBADF {
				c.incrementSendCompletionEBADF(state.ringIndex) // per-ring metrics
				return
			}
			if err == syscall.ETIME {
				c.incrementSendCompletionTimeout(state.ringIndex) // per-ring metrics
				continue
			}
			if err == syscall.EINTR {
				c.incrementSendCompletionEINTR(state.ringIndex) // per-ring metrics
				continue
			}
			c.incrementSendCompletionError(state.ringIndex) // per-ring metrics
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("error waiting for completion (ring %d): %v", state.ringIndex, err)
			})
			continue
		}

		// Track successful completion wait
		c.incrementSendCompletionSuccess(state.ringIndex) // per-ring metrics

		// Get request ID and lookup completion info (under lock)
		requestID := cqe.UserData

		state.compLock.Lock()
		compInfo, exists := state.completions[requestID]
		if !exists {
			state.compLock.Unlock()
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("completion for unknown request ID: %d (ring %d)", requestID, state.ringIndex)
			})
			ring.CQESeen(cqe)
			continue
		}
		delete(state.completions, requestID)
		state.compLock.Unlock()

		// Process completion result
		buffer := compInfo.buffer
		if cqe.Res < 0 {
			errno := -cqe.Res
			c.log("connection:send:completion:error", func() string {
				return fmt.Sprintf("send failed (ring %d): %s (errno %d)", state.ringIndex, syscall.Errno(errno).Error(), errno)
			})
			// Track send error
			if c.metrics != nil {
				if compInfo.packet != nil {
					metrics.IncrementSendMetrics(c.metrics, compInfo.packet, compInfo.isIoUring, false, metrics.DropReasonIoUring)
				} else {
					metrics.IncrementSendErrorMetrics(c.metrics, compInfo.isIoUring, metrics.DropReasonIoUring)
				}
			}
		} else {
			bytesSent := int(cqe.Res)
			if bytesSent < len(buffer.Bytes()) {
				c.log("connection:send:completion:warning", func() string {
					return fmt.Sprintf("partial send (ring %d): %d/%d bytes", state.ringIndex, bytesSent, len(buffer.Bytes()))
				})
				if c.metrics != nil {
					if compInfo.packet != nil {
						metrics.IncrementSendMetrics(c.metrics, compInfo.packet, compInfo.isIoUring, false, metrics.DropReasonIoUring)
					} else {
						metrics.IncrementSendErrorMetrics(c.metrics, compInfo.isIoUring, metrics.DropReasonIoUring)
					}
				}
			}
			// Full send success - already tracked in sendIoUringToRing() after successful submit
		}

		// Cleanup buffer only - do NOT decommission packet!
		// Data packets remain in SendPacketBtree for potential NAK retransmit.
		// Packets are only decommissioned when:
		//   - ACK received → packet deleted from btree
		//   - Drop threshold exceeded → packet dropped from btree
		// Control packets were already decommissioned in sendIoUringToRing()
		// (compInfo.packet is nil for control packets)
		//
		// BUG FIX: Previously, we called compInfo.packet.Decommission() here,
		// which set payload to nil while packet was still in btree.
		// NAK retransmits would then fail with "invalid payload" because
		// the packet in btree had been decommissioned.
		buffer.Reset()
		c.sendBufferPool.Put(buffer)
		// Note: compInfo.packet is nil for control packets (already decommissioned)
		// For data packets, packet remains valid in btree until ACK/drop

		ring.CQESeen(cqe)
	}
}

// cleanupIoUringSendMultiRing cleans up multi-ring send resources
func (c *srtConn) cleanupIoUringSendMultiRing() {
	if len(c.sendRingStates) == 0 {
		return
	}

	// Step 1: Cancel context
	if c.sendCompCancel != nil {
		c.sendCompCancel()
	}

	// Step 2: Wait for all handlers to exit
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
		c.log("connection:io_uring:cleanup", func() string {
			return "CRITICAL: multi-ring completion handlers did not exit within 2s"
		})
	}

	// Step 3: Clean up each ring state
	if handlerExited {
		for _, state := range c.sendRingStates {
			if state.ring != nil {
				state.ring.QueueExit()
			}
			// Return buffers to pool (handlers have exited)
			state.compLock.Lock()
			for _, compInfo := range state.completions {
				compInfo.buffer.Reset()
				c.sendBufferPool.Put(compInfo.buffer)
			}
			state.compLock.Unlock()
		}
	}

	c.sendRingStates = nil
}
