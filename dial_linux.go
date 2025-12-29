//go:build linux
// +build linux

package srt

import (
	"context"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/randomizedcoder/giouring"
)

// Note: ioUringPollInterval is defined in connection_linux.go

// initializeIoUringRecv initializes the io_uring receive ring for the dialer
func (dl *dialer) initializeIoUringRecv() error {
	if !dl.config.IoUringRecvEnabled {
		return nil // io_uring not enabled, skip initialization
	}

	// Extract socket file descriptor
	fd, err := getUDPConnFd(dl.pc)
	if err != nil {
		return fmt.Errorf("failed to extract socket fd: %w", err)
	}
	dl.recvRingFd = fd

	// Determine ring size (default: 512)
	ringSize := uint32(512)
	if dl.config.IoUringRecvRingSize > 0 {
		ringSize = uint32(dl.config.IoUringRecvRingSize)
	}

	// Create io_uring ring
	ring := giouring.NewRing()
	err = ring.QueueInit(ringSize, 0) // ringSize entries, no flags
	if err != nil {
		// io_uring unavailable or failed - log and continue without it
		// Note: dialer doesn't have a log() method, so we'll skip logging for now
		// In production, this would be logged via config.Logger if available
		return err // Return error but don't fail dialer creation
	}

	dl.recvRing = ring // Store as interface{} for conditional compilation

	// Note: Using shared globalRecvBufferPool (see buffers.go)
	// Both io_uring and standard paths share the same global pool

	// Initialize completion tracking
	dl.recvCompletions = make(map[uint64]*recvCompletionInfo)

	// Start completion handler goroutine with dialer context
	// Note: handler will exit when dl.ctx is cancelled (via client context cancellation)
	dl.recvCompWg.Add(1)
	go dl.recvCompletionHandler(dl.ctx)

	// Pre-populate ring with initial pending receives
	dl.prePopulateRecvRing()

	return nil
}

// cleanupIoUringRecv cleans up the io_uring receive ring for the dialer
// Following context_and_cancellation_design.md pattern:
// 1. Context is cancelled by parent (dl.ctx via client shutdown)
// 2. Wait for handler to exit (via WaitGroup)
// 3. Only then clean up resources (QueueExit)
func (dl *dialer) cleanupIoUringRecv() {
	if dl.recvRing == nil {
		return // Nothing to clean up
	}

	// Step 1: Context should already be cancelled by parent (client context cancellation)
	// The handler checks dl.ctx.Done() at top of loop and will exit within ~10ms

	// Step 2: Wait for completion handler to exit BEFORE calling QueueExit()
	// We MUST wait because QueueExit() unmaps ring memory - if handler is still
	// inside WaitCQETimeout(), the giouring library will SIGSEGV when it tries
	// to peek at the unmapped CQ.
	done := make(chan struct{})
	go func() {
		dl.recvCompWg.Wait()
		close(done)
	}()

	handlerExited := false
	select {
	case <-done:
		handlerExited = true
	case <-time.After(2 * time.Second):
		// CRITICAL: Handler did not exit - DO NOT call QueueExit
		// Minor resource leak is better than SIGSEGV crash
		// Note: No logger available on dialer, skip logging
	}

	// Step 3: Only close ring if handler has exited
	if handlerExited {
		ring, ok := dl.recvRing.(*giouring.Ring)
		if ok {
			ring.QueueExit()
		}
		dl.recvRing = nil // Fail gracefully if any late accesses
	}

	// Step 4: Clean up completion map and return all buffers to pool
	dl.recvCompLock.Lock()
	for _, compInfo := range dl.recvCompletions {
		GetRecvBufferPool().Put(compInfo.buffer)
	}
	dl.recvCompletions = nil
	dl.recvCompLock.Unlock()

	// Close the duplicated file descriptor
	if dl.recvRingFd > 0 {
		syscall.Close(dl.recvRingFd)
		dl.recvRingFd = -1
	}
}

// submitRecvRequest submits a new receive request to the ring
func (dl *dialer) submitRecvRequest() {
	ring, ok := dl.recvRing.(*giouring.Ring)
	if !ok {
		return
	}

	// Get buffer from pool (fixed size MSS, no setup needed)
	bufferPtr := GetRecvBufferPool().Get().(*[]byte)
	buffer := *bufferPtr

	// Setup iovec using buffer directly
	var iovec syscall.Iovec
	iovec.Base = &buffer[0]
	iovec.SetLen(len(buffer))

	// Setup msghdr for UDP (to get source address)
	// Allocate rsa and msg on heap so they persist until completion is processed
	// The kernel needs these structures to remain valid until the completion is processed
	rsa := new(syscall.RawSockaddrAny)
	msg := new(syscall.Msghdr)
	msg.Name = (*byte)(unsafe.Pointer(rsa))
	msg.Namelen = uint32(syscall.SizeofSockaddrAny)
	msg.Iov = &iovec
	msg.Iovlen = 1

	// Generate unique request ID
	requestID := dl.recvRequestID.Add(1)

	// Create completion info
	compInfo := &recvCompletionInfo{
		buffer: bufferPtr,
		rsa:    rsa,
		msg:    msg,
	}

	// Store completion info in map
	dl.recvCompLock.Lock()
	dl.recvCompletions[requestID] = compInfo
	dl.recvCompLock.Unlock()

	// Get SQE from ring with retry loop
	var sqe *giouring.SubmissionQueueEntry
	for i := 0; i < ioUringMaxGetSQERetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break
		}
		// Track retry (ring temporarily full)
		if dl.conn != nil && dl.conn.metrics != nil {
			dl.conn.metrics.IoUringDialerRecvGetSQERetries.Add(1)
		}
		if i < ioUringMaxGetSQERetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if sqe == nil {
		// Ring still full after retries - clean up
		dl.recvCompLock.Lock()
		delete(dl.recvCompletions, requestID)
		dl.recvCompLock.Unlock()
		GetRecvBufferPool().Put(bufferPtr)
		// Track ring full error
		if dl.conn != nil && dl.conn.metrics != nil {
			dl.conn.metrics.IoUringDialerRecvSubmitRingFull.Add(1)
		}
		return
	}

	// Prepare recvmsg operation
	// Pass pointer to heap-allocated msg so it stays valid until completion
	sqe.PrepareRecvMsg(dl.recvRingFd, msg, 0)
	sqe.SetData64(requestID)

	// Submit to ring with retry loop
	var err error
	for i := 0; i < ioUringMaxSubmitRetries; i++ {
		_, err = ring.Submit()
		if err == nil {
			break
		}
		if err != syscall.EINTR && err != syscall.EAGAIN {
			break
		}
		// Track retry (transient error)
		if dl.conn != nil && dl.conn.metrics != nil {
			dl.conn.metrics.IoUringDialerRecvSubmitRetries.Add(1)
		}
		if i < ioUringMaxSubmitRetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if err != nil {
		// Submission failed - clean up
		dl.recvCompLock.Lock()
		delete(dl.recvCompletions, requestID)
		dl.recvCompLock.Unlock()
		GetRecvBufferPool().Put(bufferPtr)
		// Track submit error
		if dl.conn != nil && dl.conn.metrics != nil {
			dl.conn.metrics.IoUringDialerRecvSubmitError.Add(1)
		}
		return
	}

	// Track success
	if dl.conn != nil && dl.conn.metrics != nil {
		dl.conn.metrics.IoUringDialerRecvSubmitSuccess.Add(1)
	}
}

// prePopulateRecvRing pre-populates ring with initial pending receives
func (dl *dialer) prePopulateRecvRing() {
	initialPending := dl.config.IoUringRecvInitialPending
	if initialPending <= 0 {
		ringSize := dl.config.IoUringRecvRingSize
		if ringSize <= 0 {
			ringSize = 512
		}
		initialPending = ringSize
	}

	for i := 0; i < initialPending; i++ {
		dl.submitRecvRequest()
	}
}

// lookupAndRemoveRecvCompletion looks up completion info by request ID and removes it from the map
func (dl *dialer) lookupAndRemoveRecvCompletion(cqe *giouring.CompletionQueueEvent, ring *giouring.Ring) *recvCompletionInfo {
	requestID := cqe.UserData

	dl.recvCompLock.Lock()
	compInfo, exists := dl.recvCompletions[requestID]
	if !exists {
		dl.recvCompLock.Unlock()
		ring.CQESeen(cqe)
		return nil
	}
	delete(dl.recvCompletions, requestID)
	dl.recvCompLock.Unlock()

	return compInfo
}

// processRecvCompletion processes a single completion
func (dl *dialer) processRecvCompletion(ring *giouring.Ring, cqe *giouring.CompletionQueueEvent, compInfo *recvCompletionInfo) {
	bufferPtr := compInfo.buffer
	// Note: buffer variable removed - Phase 2 uses bufferPtr directly for zero-copy

	// Check for receive errors
	if cqe.Res < 0 {
		// Dialer doesn't have log method, skip logging
		ring.CQESeen(cqe)
		GetRecvBufferPool().Put(bufferPtr)
		return
	}

	// Successful receive
	bytesReceived := int(cqe.Res)
	if bytesReceived == 0 {
		ring.CQESeen(cqe)
		GetRecvBufferPool().Put(bufferPtr)
		return
	}

	// Extract source address (for dialer, we use remoteAddr from config)
	addr := dl.remoteAddr
	if addr == nil {
		// Fallback to extracting from RSA if remoteAddr not set
		if compInfo.rsa == nil {
			// RSA is nil - this shouldn't happen, but handle gracefully
			// For dialer, we can continue without address if remoteAddr is set
			ring.CQESeen(cqe)
			GetRecvBufferPool().Put(bufferPtr) // Return buffer since packet won't be created
			return
		}
		addr = extractAddrFromRSA(compInfo.rsa)
		if addr == nil {
			// Failed to extract address - can't process packet without address
			ring.CQESeen(cqe)
			GetRecvBufferPool().Put(bufferPtr) // Return buffer since packet won't be created
			return
		}
	}

	// Phase 2: Zero-copy - buffer lifetime extends until packet delivery
	p := packet.NewPacket(addr)

	// UnmarshalZeroCopy stores buffer reference FIRST (before validation)
	if err := p.UnmarshalZeroCopy(bufferPtr, bytesReceived, addr); err != nil {
		// Deserialization error - return buffer to pool and decommission packet
		p.DecommissionWithBuffer(GetRecvBufferPool())
		ring.CQESeen(cqe)
		return
	}

	// NOTE: Buffer is NOT returned to pool here! It's referenced by the packet.
	// Buffer will be returned by receiver.releasePacketFully() after delivery.

	// Route directly (bypass channels) - Channel Bypass Optimization
	// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
	h := p.Header()

	// Debug logging for sequence analysis (topic: dial:io_uring:completion:seq)
	// This helps diagnose out-of-order packet delivery in io_uring path
	if !h.IsControlPacket {
		dl.log("dial:io_uring:completion:seq", func() string {
			return fmt.Sprintf("DATA seq=%d reqID=%d",
				h.PacketSequenceNumber.Val(), cqe.UserData)
		})
	}

	// For dialer, we need to handle handshake packets before connection is established
	if h.IsControlPacket && h.ControlType == packet.CTRLTYPE_HANDSHAKE {
		// Handshake packet - route to handleHandshake (non-blocking channel)
		// This is needed during connection establishment before conn is set
		select {
		case dl.rcvQueue <- p:
			// Success - handshake packet queued
		default:
			// Queue full - drop packet (shouldn't happen with reasonable buffer size)
			p.Decommission()
		}
		ring.CQESeen(cqe)
		return // Always resubmit to maintain constant pending count
	}

	// For non-handshake packets, route to connection if it exists
	dl.connLock.RLock()
	conn := dl.conn
	dl.connLock.RUnlock()

	if conn == nil {
		// No connection yet and not a handshake packet - drop it
		// Note: Can't track metrics here - no connection yet
		ring.CQESeen(cqe)
		p.Decommission()
		return // Always resubmit to maintain constant pending count
	}

	// Track successful receive (io_uring path)
	if conn.metrics != nil {
		metrics.IncrementRecvMetrics(conn.metrics, p, true, true, 0)
	}

	// Direct call to handlePacket (blocking mutex - never drops packets)
	conn.handlePacketDirect(p)

	ring.CQESeen(cqe)
}

// getRecvCompletion gets a single completion using blocking wait with timeout.
// WaitCQETimeout blocks in the kernel until either:
//  1. A completion arrives (returns immediately - zero latency!)
//  2. Timeout expires (returns ETIME, allows ctx.Done() check)
//  3. Ring is closed (returns EBADF, normal shutdown)
func (dl *dialer) getRecvCompletion(ctx context.Context, ring *giouring.Ring) (*giouring.CompletionQueueEvent, *recvCompletionInfo) {
	for {
		// Check context first (non-blocking)
		select {
		case <-ctx.Done():
			if dl.conn != nil && dl.conn.metrics != nil {
				dl.conn.metrics.IoUringDialerRecvCompletionCtxCancelled.Add(1)
			}
			return nil, nil
		default:
		}

		// Block waiting for completion OR timeout
		cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
		if err == nil {
			if dl.conn != nil && dl.conn.metrics != nil {
				dl.conn.metrics.IoUringDialerRecvCompletionSuccess.Add(1)
			}
			compInfo := dl.lookupAndRemoveRecvCompletion(cqe, ring)
			if compInfo == nil {
				return nil, nil
			}
			return cqe, compInfo
		}

		// EBADF means ring was closed via QueueExit()
		if err == syscall.EBADF {
			if dl.conn != nil && dl.conn.metrics != nil {
				dl.conn.metrics.IoUringDialerRecvCompletionEBADF.Add(1)
			}
			return nil, nil
		}

		// ETIME means timeout expired - loop back to check ctx.Done()
		if err == syscall.ETIME {
			if dl.conn != nil && dl.conn.metrics != nil {
				dl.conn.metrics.IoUringDialerRecvCompletionTimeout.Add(1)
			}
			continue
		}

		// EINTR is normal (interrupted by signal) - retry immediately
		if err == syscall.EINTR {
			if dl.conn != nil && dl.conn.metrics != nil {
				dl.conn.metrics.IoUringDialerRecvCompletionEINTR.Add(1)
			}
			continue
		}

		// Other errors - return nil to let caller handle
		if dl.conn != nil && dl.conn.metrics != nil {
			dl.conn.metrics.IoUringDialerRecvCompletionError.Add(1)
		}
		return nil, nil
	}
}

// submitRecvRequestBatch submits multiple receive requests in a batch
func (dl *dialer) submitRecvRequestBatch(count int) {
	ring, ok := dl.recvRing.(*giouring.Ring)
	if !ok {
		return
	}

	var sqes []*giouring.SubmissionQueueEntry
	var compInfos []*recvCompletionInfo
	var requestIDs []uint64

	for i := 0; i < count; i++ {
		bufferPtr := GetRecvBufferPool().Get().(*[]byte)
		buffer := *bufferPtr

		var iovec syscall.Iovec
		iovec.Base = &buffer[0]
		iovec.SetLen(len(buffer))

		// Allocate rsa and msg on heap so they persist until completion is processed
		rsa := new(syscall.RawSockaddrAny)
		msg := new(syscall.Msghdr)
		msg.Name = (*byte)(unsafe.Pointer(rsa))
		msg.Namelen = uint32(syscall.SizeofSockaddrAny)
		msg.Iov = &iovec
		msg.Iovlen = 1

		requestID := dl.recvRequestID.Add(1)

		compInfo := &recvCompletionInfo{
			buffer: bufferPtr,
			rsa:    rsa,
			msg:    msg,
		}

		dl.recvCompLock.Lock()
		dl.recvCompletions[requestID] = compInfo
		dl.recvCompLock.Unlock()

		var sqe *giouring.SubmissionQueueEntry
		const maxRetries = 3
		for j := 0; j < maxRetries; j++ {
			sqe = ring.GetSQE()
			if sqe != nil {
				break
			}
			if j < maxRetries-1 {
				time.Sleep(100 * time.Microsecond)
			}
		}

		if sqe == nil {
			dl.recvCompLock.Lock()
			delete(dl.recvCompletions, requestID)
			dl.recvCompLock.Unlock()
			GetRecvBufferPool().Put(bufferPtr)
			break
		}

		// Pass pointer to heap-allocated msg so it stays valid until completion
		sqe.PrepareRecvMsg(dl.recvRingFd, msg, 0)
		sqe.SetData64(requestID)

		sqes = append(sqes, sqe)
		compInfos = append(compInfos, compInfo)
		requestIDs = append(requestIDs, requestID)
	}

	if len(sqes) > 0 {
		_, err := ring.Submit()
		if err != nil {
			dl.recvCompLock.Lock()
			for i, requestID := range requestIDs {
				delete(dl.recvCompletions, requestID)
				GetRecvBufferPool().Put(compInfos[i].buffer)
			}
			dl.recvCompLock.Unlock()
		}
	}
}

// recvCompletionHandler is the main completion handler loop
func (dl *dialer) recvCompletionHandler(ctx context.Context) {
	defer dl.recvCompWg.Done()

	ring, ok := dl.recvRing.(*giouring.Ring)
	if !ok {
		return
	}

	batchSize := dl.config.IoUringRecvBatchSize
	if batchSize <= 0 {
		batchSize = 256
	}

	pendingResubmits := 0

	for {
		select {
		case <-ctx.Done():
			if pendingResubmits > 0 {
				dl.submitRecvRequestBatch(pendingResubmits)
			}
			// Skip drainRecvCompletions - it takes too long (5s timeout) and
			// the ring will be closed by cleanupIoUringRecv() anyway.
			return
		default:
		}

		cqe, compInfo := dl.getRecvCompletion(ctx, ring)
		if cqe == nil {
			continue
		}

		dl.processRecvCompletion(ring, cqe, compInfo)

		pendingResubmits++

		if pendingResubmits >= batchSize {
			dl.submitRecvRequestBatch(pendingResubmits)
			pendingResubmits = 0
		}
	}
}

// drainRecvCompletions drains remaining completions during shutdown
func (dl *dialer) drainRecvCompletions() {
	ring, ok := dl.recvRing.(*giouring.Ring)
	if !ok || ring == nil {
		return
	}

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case <-timeout.C:
			return

		default:
			cqe, err := ring.PeekCQE()
			if err != nil {
				if err == syscall.EAGAIN {
					dl.recvCompLock.Lock()
					empty := len(dl.recvCompletions) == 0
					dl.recvCompLock.Unlock()

					if empty {
						return
					}

					time.Sleep(10 * time.Millisecond)
					continue
				}
				return
			}

			requestID := cqe.UserData

			dl.recvCompLock.Lock()
			compInfo, exists := dl.recvCompletions[requestID]
			if !exists {
				dl.recvCompLock.Unlock()
				ring.CQESeen(cqe)
				continue
			}
			delete(dl.recvCompletions, requestID)
			dl.recvCompLock.Unlock()

			GetRecvBufferPool().Put(compInfo.buffer)
			ring.CQESeen(cqe)
		}
	}
}
