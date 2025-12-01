//go:build linux
// +build linux

package srt

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/datarhei/gosrt/packet"
	"github.com/randomizedcoder/giouring"
)

// recvCompletionInfo stores minimal information needed for completion handling
// Key insight: We only need the buffer (to return to pool after deserialization)
// and rsa (to extract source address). The msg and iovec are only used during
// SQE setup in submitRecvRequest(), not in the completion handler.
type recvCompletionInfo struct {
	buffer []byte                 // Buffer to return to pool after deserialization completes
	rsa    syscall.RawSockaddrAny // Kernel fills this during receive, used to extract source address
}

// getUDPConnFd extracts the file descriptor from a *net.UDPConn
// This is needed for io_uring operations which require the raw file descriptor.
// Uses pc.File() which is the natural and supported way to get the file descriptor
// on Linux. We duplicate the FD so that closing the returned *os.File doesn't
// close the underlying socket.
func getUDPConnFd(pc *net.UDPConn) (int, error) {
	file, err := pc.File()
	if err != nil {
		return -1, fmt.Errorf("failed to get file from UDPConn: %w", err)
	}
	defer file.Close()

	fd := int(file.Fd())
	// Duplicate the fd so closing the file doesn't close the socket
	// This is important because we need to keep the socket alive for io_uring
	newFd, err := syscall.Dup(fd)
	if err != nil {
		return -1, fmt.Errorf("failed to duplicate fd: %w", err)
	}

	return newFd, nil
}

// extractAddrFromRSA extracts net.Addr from syscall.RawSockaddrAny
func extractAddrFromRSA(rsa *syscall.RawSockaddrAny) net.Addr {
	if rsa == nil {
		return nil
	}

	switch rsa.Addr.Family {
	case syscall.AF_INET:
		p := (*syscall.RawSockaddrInet4)(unsafe.Pointer(rsa))
		addr := &net.UDPAddr{
			IP:   net.IPv4(p.Addr[0], p.Addr[1], p.Addr[2], p.Addr[3]),
			Port: int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&p.Port))[:])),
		}
		return addr

	case syscall.AF_INET6:
		p := (*syscall.RawSockaddrInet6)(unsafe.Pointer(rsa))
		addr := &net.UDPAddr{
			IP:   make(net.IP, net.IPv6len),
			Port: int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&p.Port))[:])),
			Zone: zoneToString(int(p.Scope_id)),
		}
		copy(addr.IP, p.Addr[:])
		return addr

	default:
		return nil
	}
}

// zoneToString converts IPv6 scope ID to zone string
func zoneToString(zone int) string {
	if zone == 0 {
		return ""
	}
	// For now, return numeric string
	// Could be enhanced to resolve interface names
	return strconv.Itoa(zone)
}

// initializeIoUringRecv initializes the io_uring receive ring for the listener
func (ln *listener) initializeIoUringRecv() error {
	if !ln.config.IoUringRecvEnabled {
		return nil // io_uring not enabled, skip initialization
	}

	// Extract socket file descriptor
	fd, err := getUDPConnFd(ln.pc)
	if err != nil {
		return fmt.Errorf("failed to extract socket fd: %w", err)
	}
	ln.recvRingFd = fd

	// Determine ring size (default: 512)
	ringSize := uint32(512)
	if ln.config.IoUringRecvRingSize > 0 {
		ringSize = uint32(ln.config.IoUringRecvRingSize)
	}

	// Create io_uring ring
	ring := giouring.NewRing()
	err = ring.QueueInit(ringSize, 0) // ringSize entries, no flags
	if err != nil {
		// io_uring unavailable or failed - log and continue without it
		ln.log("listen:io_uring:recv:init", func() string {
			return fmt.Sprintf("failed to create io_uring receive ring: %v (falling back to ReadFrom)", err)
		})
		return err // Return error but don't fail listener creation
	}

	ln.recvRing = ring // Store as interface{} for conditional compilation

	// Initialize receive buffer pool (fixed size MSS)
	ln.recvBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, ln.config.MSS)
		},
	}

	// Initialize completion tracking
	ln.recvCompletions = make(map[uint64]*recvCompletionInfo)

	// Create context for completion handler
	ln.recvCompCtx, ln.recvCompCancel = context.WithCancel(context.Background())

	// Start completion handler goroutine
	ln.recvCompWg.Add(1)
	go ln.recvCompletionHandler(ln.recvCompCtx)

	// Pre-populate ring with initial pending receives
	ln.prePopulateRecvRing()

	return nil
}

// cleanupIoUringRecv cleans up the io_uring receive ring for the listener
func (ln *listener) cleanupIoUringRecv() {
	if ln.recvRing == nil {
		return // Nothing to clean up
	}

	// Stop completion handler (if started in Phase 3)
	if ln.recvCompCancel != nil {
		ln.recvCompCancel()
	}

	// Wait for completion handler to finish (with timeout)
	done := make(chan struct{})
	go func() {
		ln.recvCompWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Completion handler finished
	case <-time.After(5 * time.Second):
		// Timeout - log warning but continue
		ln.log("listen:io_uring:recv:cleanup", func() string {
			return "timeout waiting for completion handler"
		})
	}

	// Close the ring
	ring, ok := ln.recvRing.(*giouring.Ring)
	if ok {
		ring.QueueExit()
	}

	// Clean up completion map and return all buffers to pool
	ln.recvCompLock.Lock()
	for _, compInfo := range ln.recvCompletions {
		ln.recvBufferPool.Put(compInfo.buffer)
	}
	ln.recvCompletions = nil
	ln.recvCompLock.Unlock()

	// Close the duplicated file descriptor
	if ln.recvRingFd > 0 {
		syscall.Close(ln.recvRingFd)
		ln.recvRingFd = -1
	}
}

// submitRecvRequest submits a new receive request to the ring
// This is called both at startup (to pre-populate) and after each completion (to maintain constant pending)
func (ln *listener) submitRecvRequest() {
	ring, ok := ln.recvRing.(*giouring.Ring)
	if !ok {
		return
	}

	// Get buffer from pool (fixed size MSS, no setup needed)
	buffer := ln.recvBufferPool.Get().([]byte)
	// No Reset() needed - kernel will overwrite the buffer

	// Setup iovec using buffer directly (no conversion needed)
	var iovec syscall.Iovec
	iovec.Base = &buffer[0]
	iovec.SetLen(len(buffer))

	// Setup msghdr for UDP (to get source address)
	var msg syscall.Msghdr
	var rsa syscall.RawSockaddrAny
	msg.Name = (*byte)(unsafe.Pointer(&rsa))
	msg.Namelen = uint32(syscall.SizeofSockaddrAny)
	msg.Iov = &iovec
	msg.Iovlen = 1

	// Generate unique request ID using atomic counter (same pattern as send path)
	requestID := ln.recvRequestID.Add(1)

	// Create minimal completion info (only buffer and rsa needed)
	// msg and iovec are only used for SQE setup, not stored in completion info
	compInfo := &recvCompletionInfo{
		buffer: buffer, // Keep buffer alive until deserialization completes
		rsa:    rsa,    // Kernel will fill this during receive
	}

	// Store completion info in map (protected by lock, same pattern as send path)
	ln.recvCompLock.Lock()
	ln.recvCompletions[requestID] = compInfo
	ln.recvCompLock.Unlock()

	// Get SQE from ring with retry loop (same pattern as send path)
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
		// Ring still full after retries - clean up (same pattern as send path)
		ln.recvCompLock.Lock()
		delete(ln.recvCompletions, requestID)
		ln.recvCompLock.Unlock()

		ln.recvBufferPool.Put(buffer)

		ln.log("listen:recv:error", func() string {
			return "io_uring ring full after retries"
		})
		return
	}

	// Prepare recvmsg operation
	sqe.PrepareRecvMsg(ln.recvRingFd, &msg, 0)

	// Store request ID in user data for completion correlation (same pattern as send path)
	sqe.SetData64(requestID)

	// Submit to ring with retry loop (same pattern as send path)
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
		// Submission failed - clean up (same pattern as send path)
		ln.recvCompLock.Lock()
		delete(ln.recvCompletions, requestID)
		ln.recvCompLock.Unlock()

		ln.recvBufferPool.Put(buffer)

		ln.log("listen:recv:error", func() string {
			return fmt.Sprintf("failed to submit receive request: %v", err)
		})
		return
	}

	// Request submitted successfully
	// Completion will be handled asynchronously by completion handler
}

// prePopulateRecvRing pre-populates ring with initial pending receives (runs once at startup)
func (ln *listener) prePopulateRecvRing() {
	initialPending := ln.config.IoUringRecvInitialPending
	if initialPending <= 0 {
		// Default: full ring size (maximize pending receives)
		ringSize := ln.config.IoUringRecvRingSize
		if ringSize <= 0 {
			ringSize = 512 // Default ring size
		}
		initialPending = ringSize
	}

	// Submit initial batch of receives
	for i := 0; i < initialPending; i++ {
		ln.submitRecvRequest()
	}
}

// lookupAndRemoveRecvCompletion looks up completion info by request ID and removes it from the map
func (ln *listener) lookupAndRemoveRecvCompletion(cqe *giouring.CompletionQueueEvent, ring *giouring.Ring) *recvCompletionInfo {
	requestID := cqe.UserData

	ln.recvCompLock.Lock()
	compInfo, exists := ln.recvCompletions[requestID]
	if !exists {
		ln.recvCompLock.Unlock()
		ln.log("listen:recv:completion:error", func() string {
			return fmt.Sprintf("completion for unknown request ID: %d", requestID)
		})
		ring.CQESeen(cqe)
		return nil
	}
	delete(ln.recvCompletions, requestID)
	ln.recvCompLock.Unlock()

	return compInfo
}

// processRecvCompletion processes a single completion
// Always resubmits to maintain constant pending count (caller handles batching)
func (ln *listener) processRecvCompletion(ring *giouring.Ring, cqe *giouring.CompletionQueueEvent, compInfo *recvCompletionInfo) {
	buffer := compInfo.buffer

	// Check for receive errors
	if cqe.Res < 0 {
		errno := -cqe.Res
		ln.log("listen:recv:completion:error", func() string {
			return fmt.Sprintf("receive failed: %s (errno %d)", syscall.Errno(errno).Error(), errno)
		})
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(buffer)
		return // Always resubmit to maintain constant pending count
	}

	// Successful receive
	bytesReceived := int(cqe.Res)
	if bytesReceived == 0 {
		// Empty datagram - return buffer and resubmit
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(buffer)
		return // Always resubmit to maintain constant pending count
	}

	// Extract source address from RawSockaddrAny (kernel filled this during receive)
	addr := extractAddrFromRSA(&compInfo.rsa)

	// Use buffer directly (kernel wrote directly to it via iovec)
	bufferSlice := buffer[:bytesReceived]

	// Deserialize packet (NewPacketFromData copies the data into packet structure)
	p, err := packet.NewPacketFromData(addr, bufferSlice)

	// After deserialization, we can return buffer to pool immediately
	// (NewPacketFromData has copied the data, so buffer is no longer needed)
	ln.recvBufferPool.Put(buffer)

	if err != nil {
		// Deserialization error - log and resubmit
		ln.log("listen:recv:parse:error", func() string {
			return fmt.Sprintf("failed to parse packet: %v", err)
		})
		ring.CQESeen(cqe)
		return // Always resubmit to maintain constant pending count
	}

	// Queue packet (non-blocking, same as current implementation)
	select {
	case ln.rcvQueue <- p:
		// Success - packet queued, buffer already returned to pool
	default:
		// Queue full - log and drop packet
		ln.log("listen", func() string { return "receive queue is full" })
		p.Decommission() // Clean up dropped packet
	}

	// Mark CQE as seen (required by giouring)
	ring.CQESeen(cqe)
	// Always resubmit to maintain constant pending count (handled by caller)
}

// getRecvCompletion gets a single completion (non-blocking peek, then blocking wait if needed)
// Returns immediately with the completion for low-latency processing
func (ln *listener) getRecvCompletion(ctx context.Context, ring *giouring.Ring) (*giouring.CompletionQueueEvent, *recvCompletionInfo) {
	// Try non-blocking peek first
	cqe, err := ring.PeekCQE()
	if err == nil {
		// Success - we have a completion, look it up and return
		compInfo := ln.lookupAndRemoveRecvCompletion(cqe, ring)
		if compInfo == nil {
			return nil, nil // Unknown request ID, skip
		}
		return cqe, compInfo
	}

	// PeekCQE returned an error - handle based on error type
	if err != syscall.EAGAIN {
		// Error other than EAGAIN - handle and return early
		select {
		case <-ctx.Done():
			return nil, nil
		default:
		}

		if err == syscall.EBADF {
			// Ring closed - listener is shutting down
			return nil, nil
		}

		// EINTR is normal (interrupted by signal)
		if err != syscall.EINTR {
			ln.log("listen:recv:completion:error", func() string {
				return fmt.Sprintf("error peeking completion: %v", err)
			})
		}
		return nil, nil
	}

	// EAGAIN - no completions available, wait for one (blocking)
	// Check context before blocking call
	select {
	case <-ctx.Done():
		return nil, nil
	default:
	}

	cqe, err = ring.WaitCQE()
	if err != nil {
		// Check if context was cancelled while waiting
		select {
		case <-ctx.Done():
			return nil, nil
		default:
		}

		if err == syscall.EBADF {
			return nil, nil
		}

		if err != syscall.EAGAIN && err != syscall.EINTR {
			ln.log("listen:recv:completion:error", func() string {
				return fmt.Sprintf("error waiting for completion: %v", err)
			})
		}
		return nil, nil
	}

	// Successfully got completion from WaitCQE - look it up and return
	compInfo := ln.lookupAndRemoveRecvCompletion(cqe, ring)
	if compInfo == nil {
		return nil, nil // Unknown request ID, skip
	}

	return cqe, compInfo
}

// submitRecvRequestBatch submits multiple receive requests in a batch
// This is more efficient than calling submitRecvRequest() multiple times
// Reduces syscall overhead by batching multiple submissions together
func (ln *listener) submitRecvRequestBatch(count int) {
	ring, ok := ln.recvRing.(*giouring.Ring)
	if !ok {
		return
	}

	// Collect SQEs for batch submission
	var sqes []*giouring.SubmissionQueueEntry
	var compInfos []*recvCompletionInfo
	var requestIDs []uint64 // Track request IDs for error cleanup

	for i := 0; i < count; i++ {
		// Get buffer from pool
		buffer := ln.recvBufferPool.Get().([]byte)

		// Setup iovec using buffer directly
		var iovec syscall.Iovec
		iovec.Base = &buffer[0]
		iovec.SetLen(len(buffer))

		// Setup msghdr for UDP (to get source address)
		var msg syscall.Msghdr
		var rsa syscall.RawSockaddrAny
		msg.Name = (*byte)(unsafe.Pointer(&rsa))
		msg.Namelen = uint32(syscall.SizeofSockaddrAny)
		msg.Iov = &iovec
		msg.Iovlen = 1

		// Generate unique request ID
		requestID := ln.recvRequestID.Add(1)

		// Create completion info
		compInfo := &recvCompletionInfo{
			buffer: buffer,
			rsa:    rsa,
		}

		// Store completion info in map
		ln.recvCompLock.Lock()
		ln.recvCompletions[requestID] = compInfo
		ln.recvCompLock.Unlock()

		// Get SQE (with retry if needed)
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
			// Ring full - clean up and break
			ln.recvCompLock.Lock()
			delete(ln.recvCompletions, requestID)
			ln.recvCompLock.Unlock()
			ln.recvBufferPool.Put(buffer)
			break
		}

		// Prepare recvmsg operation
		sqe.PrepareRecvMsg(ln.recvRingFd, &msg, 0)
		sqe.SetData64(requestID)

		sqes = append(sqes, sqe)
		compInfos = append(compInfos, compInfo)
		requestIDs = append(requestIDs, requestID)
	}

	// Batch submit all SQEs at once (single syscall)
	if len(sqes) > 0 {
		_, err := ring.Submit()
		if err != nil {
			// Submission failed - clean up all requests in batch
			ln.recvCompLock.Lock()
			for i, requestID := range requestIDs {
				delete(ln.recvCompletions, requestID)
				ln.recvBufferPool.Put(compInfos[i].buffer)
			}
			ln.recvCompLock.Unlock()
			ln.log("listen:recv:error", func() string {
				return fmt.Sprintf("failed to submit receive batch: %v", err)
			})
		}
	}
}

// recvCompletionHandler is the main completion handler loop
// Processes completions immediately (low latency) but batches resubmissions (reduced syscalls)
func (ln *listener) recvCompletionHandler(ctx context.Context) {
	defer ln.recvCompWg.Done()

	ring, ok := ln.recvRing.(*giouring.Ring)
	if !ok {
		return
	}

	// Get batch size from config (default: 256, optimized for maximum performance)
	batchSize := ln.config.IoUringRecvBatchSize
	if batchSize <= 0 {
		batchSize = 256 // Default
	}

	// Track pending resubmissions for batching
	pendingResubmits := 0

	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			// Flush any pending resubmits before draining
			if pendingResubmits > 0 {
				ln.submitRecvRequestBatch(pendingResubmits)
			}
			ln.drainRecvCompletions()
			return
		default:
		}

		// Get single completion (process immediately for low latency)
		cqe, compInfo := ln.getRecvCompletion(ctx, ring)
		if cqe == nil {
			// If we have pending resubmits but no completions, flush them
			// This ensures we don't wait indefinitely for completions when we need to resubmit
			if pendingResubmits > 0 && pendingResubmits < batchSize {
				// Optional: Could add a timeout here, but for now just continue
				// The pending resubmits will be flushed when batch size is reached or on shutdown
			}
			continue // No completion available or error
		}

		// Process completion immediately (deserialize and queue to channel)
		// Always resubmits to maintain constant pending count
		ln.processRecvCompletion(ring, cqe, compInfo)

		// Track resubmission for batching (always increment since we always resubmit)
		pendingResubmits++

		// Batch resubmit when we've accumulated enough
		if pendingResubmits >= batchSize {
			ln.submitRecvRequestBatch(pendingResubmits)
			pendingResubmits = 0
		}
	}
}

// drainRecvCompletions drains remaining completions during shutdown
func (ln *listener) drainRecvCompletions() {
	ring, ok := ln.recvRing.(*giouring.Ring)
	if !ok || ring == nil {
		return
	}

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case <-timeout.C:
			// Timeout - give up on remaining completions
			ln.log("listen:recv:drain", func() string {
				return "timeout draining receive completions"
			})
			return

		default:
			// Try to get completion (non-blocking, same pattern as send path)
			cqe, err := ring.PeekCQE()
			if err != nil {
				if err == syscall.EAGAIN {
					// No completions available - check if map is empty
					ln.recvCompLock.Lock()
					empty := len(ln.recvCompletions) == 0
					ln.recvCompLock.Unlock()

					if empty {
						return // All completions processed
					}

					// Wait a bit before checking again
					time.Sleep(10 * time.Millisecond)
					continue
				}

				// Other error
				ln.log("listen:recv:drain:error", func() string {
					return fmt.Sprintf("error peeking completion: %v", err)
				})
				return
			}

			// Process completion (same pattern as send path)
			requestID := cqe.UserData

			ln.recvCompLock.Lock()
			compInfo, exists := ln.recvCompletions[requestID]
			if !exists {
				ln.recvCompLock.Unlock()
				ring.CQESeen(cqe)
				continue
			}
			delete(ln.recvCompletions, requestID)
			ln.recvCompLock.Unlock()

			// Cleanup (no reset needed - kernel overwrites on next use)
			ln.recvBufferPool.Put(compInfo.buffer)

			ring.CQESeen(cqe)
		}
	}
}
