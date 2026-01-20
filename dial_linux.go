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

// dialerRecvRingState holds all state for a single dialer receive io_uring ring.
// Each ring has its own dedicated completion handler goroutine.
// This struct is designed for ZERO cross-ring locking:
// - completions: owned exclusively by this ring's handler (no lock needed)
// - nextID: owned exclusively by this ring's handler (no lock needed)
// - ring: owned exclusively by this ring's handler after init
// - fd: shared but read-only (no lock needed)
// See multi_iouring_design.md Section 4.2 for design rationale.
type dialerRecvRingState struct {
	ring        *giouring.Ring                 // io_uring ring (owned by handler)
	completions map[uint64]*recvCompletionInfo // Maps request ID to completion info (no lock - owned)
	nextID      uint64                         // Request ID counter (no atomic - owned)
	fd          int                            // Socket fd (shared, read-only)
	ringIndex   int                            // Ring index for metrics/logging
}

// newDialerRecvRingState creates a new dialerRecvRingState with initialized completion map
func newDialerRecvRingState(ring *giouring.Ring, fd int, ringIndex int) *dialerRecvRingState {
	return &dialerRecvRingState{
		ring:        ring,
		completions: make(map[uint64]*recvCompletionInfo),
		nextID:      0,
		fd:          fd,
		ringIndex:   ringIndex,
	}
}

// getNextID returns the next request ID for this ring (no atomic needed - single owner)
func (drs *dialerRecvRingState) getNextID() uint64 {
	drs.nextID++
	return drs.nextID
}

// Helper functions for incrementing dialer recv per-ring metrics.
// These use ConnectionMetrics.IoUringDialerRecvRingMetrics with per-ring counters.
// See multi_iouring_design.md Section 5.12 for the unified metrics approach.

func (dl *dialer) incrementDialerRecvGetSQERetries(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].GetSQERetries.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvSubmitRingFull(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].SubmitRingFull.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvSubmitRetries(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].SubmitRetries.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvSubmitError(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].SubmitError.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvSubmitSuccess(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].SubmitSuccess.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvCompletionCtxCancelled(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].CompletionCtxCancelled.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvCompletionSuccess(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].CompletionSuccess.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvCompletionEBADF(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].CompletionEBADF.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvCompletionTimeout(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].CompletionTimeout.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvCompletionEINTR(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].CompletionEINTR.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvCompletionError(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].CompletionError.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvPacketsProcessed(ringIdx int) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].PacketsProcessed.Add(1)
	}
}

func (dl *dialer) incrementDialerRecvBytesProcessed(ringIdx int, bytes uint64) {
	if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && ringIdx < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
		dl.conn.metrics.IoUringDialerRecvRingMetrics[ringIdx].BytesProcessed.Add(bytes)
	}
}

// initializeIoUringRecv initializes the io_uring receive ring(s) for the dialer
// When IoUringRecvRingCount > 1, creates multiple independent rings for parallel processing.
// See multi_iouring_design.md Section 4.2 for design rationale.
func (dl *dialer) initializeIoUringRecv() error {
	if !dl.config.IoUringRecvEnabled {
		return nil // io_uring not enabled, skip initialization
	}

	// Extract socket file descriptor (shared by all rings)
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

	// Determine ring count (default: 1)
	ringCount := dl.config.IoUringRecvRingCount
	if ringCount <= 0 {
		ringCount = 1
	}

	// Multi-ring mode: create multiple independent ring states
	if ringCount > 1 {
		return dl.initializeIoUringRecvMultiRing(ringSize, ringCount)
	}

	// Single-ring mode (legacy path): use original fields
	return dl.initializeIoUringRecvSingleRing(ringSize)
}

// initializeIoUringRecvSingleRing initializes a single io_uring ring (legacy path)
func (dl *dialer) initializeIoUringRecvSingleRing(ringSize uint32) error {
	// Initialize per-ring metrics (unified approach - even single-ring uses per-ring metrics)
	// This MUST be done before starting handler, otherwise metric increments are silently dropped
	// Note: dl.conn may not be set yet, so we'll also init in dial.go when conn is created
	if dl.conn != nil && dl.conn.metrics != nil {
		dl.conn.metrics.IoUringDialerRecvRingMetrics = metrics.NewIoUringRingMetrics(1)
		dl.conn.metrics.IoUringDialerRecvRingCount = 1
	}

	// Create io_uring ring
	ring := giouring.NewRing()
	err := ring.QueueInit(ringSize, 0) // ringSize entries, no flags
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

// initializeIoUringRecvMultiRing initializes multiple independent io_uring rings
// Each ring has its own completion handler for parallel processing.
// See multi_iouring_design.md Section 4.2 for design rationale.
func (dl *dialer) initializeIoUringRecvMultiRing(ringSize uint32, ringCount int) error {
	// Initialize per-ring metrics FIRST (unified approach - always use per-ring metrics)
	// This MUST be done before starting handlers, otherwise metric increments are silently dropped
	// Note: dl.conn may not be set yet, so we'll also init in dial.go when conn is created
	if dl.conn != nil && dl.conn.metrics != nil {
		dl.conn.metrics.IoUringDialerRecvRingMetrics = metrics.NewIoUringRingMetrics(ringCount)
		dl.conn.metrics.IoUringDialerRecvRingCount = ringCount
	}

	// Create slice for ring states
	dl.dialerRecvRingStates = make([]*dialerRecvRingState, 0, ringCount)

	// Calculate per-ring initial pending (divide total across rings)
	initialPending := dl.config.IoUringRecvInitialPending
	if initialPending <= 0 {
		initialPending = int(ringSize) // Default: full ring size
	}
	perRingPending := initialPending / ringCount
	if perRingPending < 1 {
		perRingPending = 1
	}

	// Create each ring and its handler
	for i := 0; i < ringCount; i++ {
		// Create io_uring ring
		ring := giouring.NewRing()
		err := ring.QueueInit(ringSize, 0)
		if err != nil {
			// Cleanup any rings already created
			dl.cleanupIoUringRecvMultiRing()
			return err
		}

		// Create ring state (owns completion map and ID counter)
		state := newDialerRecvRingState(ring, dl.recvRingFd, i)
		dl.dialerRecvRingStates = append(dl.dialerRecvRingStates, state)

		// Start independent completion handler for this ring
		dl.recvCompWg.Add(1)
		go dl.dialerRecvCompletionHandlerIndependent(dl.ctx, state)

		// Pre-populate this ring
		dl.prePopulateRecvRingForState(state, perRingPending)
	}

	return nil
}

// initializeIoUringDialerRecvMetrics initializes the dialer recv io_uring metrics.
// This MUST be called after dl.conn is set (after handshake completes).
// During initializeIoUringRecv*, dl.conn is nil so metrics can't be initialized.
// See multi_iouring_design.md Phase 3 for design rationale.
func (dl *dialer) initializeIoUringDialerRecvMetrics() {
	if dl.conn == nil || dl.conn.metrics == nil {
		return
	}

	// Multi-ring mode: initialize per-ring metrics
	if len(dl.dialerRecvRingStates) > 0 {
		ringCount := len(dl.dialerRecvRingStates)
		dl.conn.metrics.IoUringDialerRecvRingMetrics = metrics.NewIoUringRingMetrics(ringCount)
		dl.conn.metrics.IoUringDialerRecvRingCount = ringCount
		return
	}

	// Single-ring mode: initialize single-ring metrics
	if dl.recvRing != nil {
		dl.conn.metrics.IoUringDialerRecvRingMetrics = metrics.NewIoUringRingMetrics(1)
		dl.conn.metrics.IoUringDialerRecvRingCount = 1
	}
}

// cleanupIoUringRecv cleans up the io_uring receive ring(s) for the dialer
// Following context_and_cancellation_design.md pattern:
// 1. Context is cancelled by parent (dl.ctx via client shutdown)
// 2. Wait for handler(s) to exit (via WaitGroup)
// 3. Only then clean up resources (QueueExit)
func (dl *dialer) cleanupIoUringRecv() {
	// Multi-ring mode: use dedicated cleanup
	if len(dl.dialerRecvRingStates) > 0 {
		dl.cleanupIoUringRecvMultiRing()
		return
	}

	// Single-ring mode (legacy path)
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
		dl.incrementDialerRecvGetSQERetries(0) // ringIdx=0 for single-ring mode
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
		dl.incrementDialerRecvSubmitRingFull(0) // ringIdx=0 for single-ring mode
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
		dl.incrementDialerRecvSubmitRetries(0) // ringIdx=0 for single-ring mode
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
		dl.incrementDialerRecvSubmitError(0) // ringIdx=0 for single-ring mode
		return
	}

	// Track success
	dl.incrementDialerRecvSubmitSuccess(0) // ringIdx=0 for single-ring mode
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
			dl.incrementDialerRecvCompletionCtxCancelled(0) // ringIdx=0 for single-ring mode
			return nil, nil
		default:
		}

		// Block waiting for completion OR timeout
		cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
		if err == nil {
			dl.incrementDialerRecvCompletionSuccess(0) // ringIdx=0 for single-ring mode
			compInfo := dl.lookupAndRemoveRecvCompletion(cqe, ring)
			if compInfo == nil {
				return nil, nil
			}
			return cqe, compInfo
		}

		// EBADF means ring was closed via QueueExit()
		if err == syscall.EBADF {
			dl.incrementDialerRecvCompletionEBADF(0) // ringIdx=0 for single-ring mode
			return nil, nil
		}

		// ETIME means timeout expired - return to allow handler to flush pending resubmits
		// This is critical to prevent the ring from running dry at high throughput!
		// Without this, the handler never gets a chance to resubmit pending requests on timeout.
		if err == syscall.ETIME {
			dl.incrementDialerRecvCompletionTimeout(0) // ringIdx=0 for single-ring mode
			return nil, nil // Return on timeout so handler can flush pending
		}

		// EINTR is normal (interrupted by signal) - retry immediately
		if err == syscall.EINTR {
			dl.incrementDialerRecvCompletionEINTR(0) // ringIdx=0 for single-ring mode
			continue
		}

		// Other errors - return nil to let caller handle
		dl.incrementDialerRecvCompletionError(0) // ringIdx=0 for single-ring mode
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
			// CRITICAL: On timeout or error, flush any pending resubmits to prevent ring from running dry
			// Without this, the ring can empty out and never receive more completions
			// This mirrors the multi-ring handler behavior (completionResultTimeout case)
			if pendingResubmits > 0 {
				dl.submitRecvRequestBatch(pendingResubmits)
				pendingResubmits = 0
			}
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

// =============================================================================
// Multi-Ring Support Functions
// =============================================================================

// dialerRecvCompletionHandlerIndependent is the completion handler for a specific ring (multi-ring mode)
// Each ring has its own handler goroutine - NO cross-ring locking required.
// The handler owns its ring state's completion map and ID counter exclusively.
// See multi_iouring_design.md Section 4.2 for design rationale.
func (dl *dialer) dialerRecvCompletionHandlerIndependent(ctx context.Context, state *dialerRecvRingState) {
	defer dl.recvCompWg.Done()

	ring := state.ring
	if ring == nil {
		return
	}

	// Get batch size from config (default: 32)
	// NOTE: Must be smaller than initial pending (default 128) otherwise ring runs dry
	// before batch threshold is reached, causing fallback to readfrom.
	batchSize := dl.config.IoUringRecvBatchSize
	if batchSize <= 0 {
		batchSize = 32 // Small batches ensure ring doesn't run dry
	}

	// Track pending resubmissions for batching
	pendingResubmits := 0

	for {
		// Get single completion (process immediately for low latency)
		cqe, compInfo, result := dl.getRecvCompletionFromRing(ctx, state)

		switch result {
		case completionResultSuccess:
			// Process completion immediately
			dl.processRecvCompletionFromRing(state, cqe, compInfo)

			// Track resubmission for batching
			pendingResubmits++

			// Batch resubmit when we've accumulated enough
			if pendingResubmits >= batchSize {
				dl.submitRecvRequestBatchToRing(state, pendingResubmits)
				pendingResubmits = 0
			}

		case completionResultTimeout:
			// CRITICAL: On timeout, flush any pending resubmits to prevent ring from running dry
			// Without this, the ring can empty out and never receive more completions
			if pendingResubmits > 0 {
				dl.submitRecvRequestBatchToRing(state, pendingResubmits)
				pendingResubmits = 0
			}
			// Continue waiting for more completions

		case completionResultShutdown:
			// Flush any pending resubmits before exiting
			if pendingResubmits > 0 {
				dl.submitRecvRequestBatchToRing(state, pendingResubmits)
			}
			return

		case completionResultError:
			// Continue
			continue
		}
	}
}

// getRecvCompletionFromRing gets a completion from a specific ring state (multi-ring mode)
// NO locking - handler owns this ring's completion map exclusively
// Returns:
//   - (cqe, compInfo, completionResultSuccess) on successful completion
//   - (nil, nil, completionResultTimeout) on timeout (caller should flush pending resubmits)
//   - (nil, nil, completionResultShutdown) on context cancellation or EBADF
//   - (nil, nil, completionResultError) on other errors
func (dl *dialer) getRecvCompletionFromRing(ctx context.Context, state *dialerRecvRingState) (*giouring.CompletionQueueEvent, *recvCompletionInfo, completionResult) {
	ring := state.ring
	ringIdx := state.ringIndex

	// Check context first (non-blocking)
	select {
	case <-ctx.Done():
		dl.incrementDialerRecvCompletionCtxCancelled(ringIdx)
		return nil, nil, completionResultShutdown
	default:
	}

	// Block waiting for completion OR timeout (single attempt)
	cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
	if err == nil {
		// Success - look up completion info (NO LOCK - we own this map)
		dl.incrementDialerRecvCompletionSuccess(ringIdx)
		requestID := cqe.UserData

		compInfo, exists := state.completions[requestID]
		if !exists {
			ring.CQESeen(cqe)
			return nil, nil, completionResultError
		}
		delete(state.completions, requestID)
		return cqe, compInfo, completionResultSuccess
	}

	// Handle errors
	if err == syscall.EBADF {
		dl.incrementDialerRecvCompletionEBADF(ringIdx)
		return nil, nil, completionResultShutdown
	}
	if err == syscall.ETIME {
		dl.incrementDialerRecvCompletionTimeout(ringIdx)
		return nil, nil, completionResultTimeout
	}
	if err == syscall.EINTR {
		dl.incrementDialerRecvCompletionEINTR(ringIdx)
		return nil, nil, completionResultTimeout // Treat EINTR like timeout
	}

	dl.incrementDialerRecvCompletionError(ringIdx)
	return nil, nil, completionResultError
}

// processRecvCompletionFromRing processes a completion from a specific ring state
// Reuses existing processRecvCompletion logic - the packet handling is identical
func (dl *dialer) processRecvCompletionFromRing(state *dialerRecvRingState, cqe *giouring.CompletionQueueEvent, compInfo *recvCompletionInfo) {
	// Reuse existing completion processing (handles packet deserialization and routing)
	dl.processRecvCompletion(state.ring, cqe, compInfo)
}

// submitRecvRequestToRing submits a single receive request to a specific ring (multi-ring mode)
// NO locking - handler owns this ring's completion map and ID counter exclusively
func (dl *dialer) submitRecvRequestToRing(state *dialerRecvRingState) {
	ring := state.ring
	if ring == nil {
		return
	}

	// Get buffer from global pool (see buffers.go - global sync.Pool)
	bufferPtr := GetRecvBufferPool().Get().(*[]byte)
	buffer := *bufferPtr

	// Setup iovec
	var iovec syscall.Iovec
	iovec.Base = &buffer[0]
	iovec.SetLen(len(buffer))

	// Setup msghdr
	rsa := new(syscall.RawSockaddrAny)
	msg := new(syscall.Msghdr)
	msg.Name = (*byte)(unsafe.Pointer(rsa))
	msg.Namelen = uint32(syscall.SizeofSockaddrAny)
	msg.Iov = &iovec
	msg.Iovlen = 1

	// Generate request ID (NO ATOMIC - we own this counter)
	requestID := state.getNextID()

	// Create completion info
	compInfo := &recvCompletionInfo{
		buffer: bufferPtr,
		rsa:    rsa,
		msg:    msg,
	}

	// Store in completion map (NO LOCK - we own this map)
	state.completions[requestID] = compInfo

	// Get SQE with retry
	var sqe *giouring.SubmissionQueueEntry
	for i := 0; i < ioUringMaxGetSQERetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break
		}
		if i < ioUringMaxGetSQERetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if sqe == nil {
		// Ring full - clean up (NO LOCK)
		delete(state.completions, requestID)
		GetRecvBufferPool().Put(bufferPtr)
		return
	}

	// Prepare recvmsg operation
	sqe.PrepareRecvMsg(state.fd, msg, 0)
	sqe.SetData64(requestID)

	// Submit with retry
	var err error
	for i := 0; i < ioUringMaxSubmitRetries; i++ {
		_, err = ring.Submit()
		if err == nil {
			break
		}
		if err != syscall.EINTR && err != syscall.EAGAIN {
			break
		}
		if i < ioUringMaxSubmitRetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if err != nil {
		// Submission failed - clean up (NO LOCK)
		delete(state.completions, requestID)
		GetRecvBufferPool().Put(bufferPtr)
		return
	}
}

// submitRecvRequestBatchToRing submits multiple receive requests to a specific ring
// Batches submissions for efficiency (reduced syscalls)
// NO locking - handler owns this ring's completion map and ID counter exclusively
func (dl *dialer) submitRecvRequestBatchToRing(state *dialerRecvRingState, count int) {
	ring := state.ring
	if ring == nil || count <= 0 {
		return
	}

	// Pre-allocate slices for batch
	compInfos := make([]*recvCompletionInfo, 0, count)
	requestIDs := make([]uint64, 0, count)

	// Prepare all SQEs
	for i := 0; i < count; i++ {
		bufferPtr := GetRecvBufferPool().Get().(*[]byte)
		buffer := *bufferPtr

		var iovec syscall.Iovec
		iovec.Base = &buffer[0]
		iovec.SetLen(len(buffer))

		rsa := new(syscall.RawSockaddrAny)
		msg := new(syscall.Msghdr)
		msg.Name = (*byte)(unsafe.Pointer(rsa))
		msg.Namelen = uint32(syscall.SizeofSockaddrAny)
		msg.Iov = &iovec
		msg.Iovlen = 1

		// Generate request ID (NO ATOMIC - we own this counter)
		requestID := state.getNextID()

		compInfo := &recvCompletionInfo{
			buffer: bufferPtr,
			rsa:    rsa,
			msg:    msg,
		}

		// Store in completion map (NO LOCK - we own this map)
		state.completions[requestID] = compInfo

		// Get SQE
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
			// Ring full - clean up this request (NO LOCK)
			delete(state.completions, requestID)
			GetRecvBufferPool().Put(bufferPtr)
			break
		}

		// Prepare recvmsg
		sqe.PrepareRecvMsg(state.fd, msg, 0)
		sqe.SetData64(requestID)

		compInfos = append(compInfos, compInfo)
		requestIDs = append(requestIDs, requestID)
	}

	// Batch submit all SQEs at once
	if len(requestIDs) > 0 {
		_, err := ring.Submit()
		if err != nil {
			// Submission failed - clean up all (NO LOCK)
			for i, requestID := range requestIDs {
				delete(state.completions, requestID)
				GetRecvBufferPool().Put(compInfos[i].buffer)
			}
			dl.incrementDialerRecvSubmitError(state.ringIndex)
		} else {
			// Success - increment metrics for each request in batch
			if dl.conn != nil && dl.conn.metrics != nil && dl.conn.metrics.IoUringDialerRecvRingMetrics != nil && state.ringIndex < len(dl.conn.metrics.IoUringDialerRecvRingMetrics) {
				dl.conn.metrics.IoUringDialerRecvRingMetrics[state.ringIndex].SubmitSuccess.Add(uint64(len(requestIDs)))
			}
		}
	}
}

// prePopulateRecvRingForState pre-populates a specific ring with initial pending receives
func (dl *dialer) prePopulateRecvRingForState(state *dialerRecvRingState, count int) {
	for i := 0; i < count; i++ {
		dl.submitRecvRequestToRing(state)
	}
}

// cleanupIoUringRecvMultiRing cleans up multi-ring resources for the dialer
func (dl *dialer) cleanupIoUringRecvMultiRing() {
	if len(dl.dialerRecvRingStates) == 0 {
		return
	}

	// Wait for all handlers to exit (context should already be cancelled)
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
		// CRITICAL: Handlers did not exit - DO NOT call QueueExit
		// Note: No logger available on dialer
	}

	// Clean up each ring state
	if handlerExited {
		for _, state := range dl.dialerRecvRingStates {
			if state.ring != nil {
				state.ring.QueueExit()
			}
			// Return buffers to pool (NO LOCK - handlers have exited)
			for _, compInfo := range state.completions {
				GetRecvBufferPool().Put(compInfo.buffer)
			}
		}
	}

	dl.dialerRecvRingStates = nil

	// Close the duplicated file descriptor
	if dl.recvRingFd > 0 {
		syscall.Close(dl.recvRingFd)
		dl.recvRingFd = -1
	}
}
