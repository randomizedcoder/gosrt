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

	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/randomizedcoder/giouring"
)

// Note: ioUringPollInterval is defined in connection_linux.go

// recvCompletionInfo stores minimal information needed for completion handling
// Key insight: We only need the buffer (to return to pool after deserialization)
// and rsa (to extract source address). The msg and iovec are only used during
// SQE setup in submitRecvRequest(), not in the completion handler.
// Note: msg must be kept alive until completion, so we store it here too.
type recvCompletionInfo struct {
	buffer *[]byte                 // Buffer pointer to return to pool after deserialization completes (pointer to avoid allocations)
	rsa    *syscall.RawSockaddrAny // Pointer to rsa that kernel fills during receive (must be heap-allocated)
	msg    *syscall.Msghdr         // Pointer to msg that kernel uses (must be heap-allocated to stay valid)
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

	// Check if address family is valid (0 means uninitialized)
	family := rsa.Addr.Family
	if family == 0 {
		return nil
	}

	switch family {
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
	// Store *[]byte to avoid allocations when putting back (staticcheck SA6002)
	ln.recvBufferPool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, ln.config.MSS)
			return &buf
		},
	}

	// Initialize completion tracking
	ln.recvCompletions = make(map[uint64]*recvCompletionInfo)

	// Start completion handler goroutine with listener context
	// Note: handler will exit when ln.ctx is cancelled (via server context cancellation)
	ln.recvCompWg.Add(1)
	go ln.recvCompletionHandler(ln.ctx)

	// Pre-populate ring with initial pending receives
	ln.prePopulateRecvRing()

	ln.log("listen:io_uring:recv:init", func() string {
		return fmt.Sprintf("io_uring receive initialized: ring_size=%d, initial_pending=%d, fd=%d", ringSize, ln.config.IoUringRecvInitialPending, ln.recvRingFd)
	})

	return nil
}

// cleanupIoUringRecv cleans up the io_uring receive ring for the listener
func (ln *listener) cleanupIoUringRecv() {
	if ln.recvRing == nil {
		return // Nothing to clean up
	}

	// Note: Completion handler will exit when ln.ctx is cancelled (via server context cancellation)
	// No need to call recvCompCancel() - it was a no-op anyway since we use ln.ctx directly

	// IMPORTANT: Close the ring FIRST to wake up blocked WaitCQE()
	// WaitCQE() will return EBADF when the ring is closed, allowing
	// the completion handler to exit cleanly. If we wait before closing,
	// the handler stays blocked in WaitCQE() and we timeout.
	ring, ok := ln.recvRing.(*giouring.Ring)
	if ok {
		ring.QueueExit()
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
	case <-time.After(2 * time.Second):
		// Timeout - log warning but continue (reduced from 5s since QueueExit should wake it)
		// Use safe logging (won't panic if logger is closed)
		if ln.config.Logger != nil {
			ln.config.Logger.Print("listen:io_uring:recv:cleanup", 0, 2, func() string {
				return "timeout waiting for completion handler"
			})
		}
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
	bufferPtr := ln.recvBufferPool.Get().(*[]byte)
	buffer := *bufferPtr
	// No Reset() needed - kernel will overwrite the buffer

	// Setup iovec using buffer directly (no conversion needed)
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

	// Generate unique request ID using atomic counter (same pattern as send path)
	requestID := ln.recvRequestID.Add(1)

	// Create minimal completion info (buffer, rsa, and msg needed)
	// msg must be kept alive because kernel uses it during completion
	compInfo := &recvCompletionInfo{
		buffer: bufferPtr, // Keep buffer pointer alive until deserialization completes
		rsa:    rsa,       // Pointer to rsa that kernel will fill during receive
		msg:    msg,       // Pointer to msg that kernel uses (must stay valid)
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

		ln.recvBufferPool.Put(bufferPtr)

		ln.log("listen:recv:error", func() string {
			return "io_uring ring full after retries"
		})
		return
	}

	// Prepare recvmsg operation
	// Pass pointer to heap-allocated msg so it stays valid until completion
	sqe.PrepareRecvMsg(ln.recvRingFd, msg, 0)

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

		ln.recvBufferPool.Put(bufferPtr)

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
	bufferPtr := compInfo.buffer
	buffer := *bufferPtr

	// Check for receive errors
	if cqe.Res < 0 {
		errno := -cqe.Res
		ln.log("listen:recv:completion:error", func() string {
			return fmt.Sprintf("receive failed: %s (errno %d)", syscall.Errno(errno).Error(), errno)
		})
		// Note: Can't track metrics here - no connection identified yet
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	// Successful receive
	bytesReceived := int(cqe.Res)
	if bytesReceived == 0 {
		// Empty datagram - return buffer and resubmit
		// Note: Can't track metrics here - no connection identified yet
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	// Extract source address from RawSockaddrAny (kernel filled this during receive)
	// Note: The kernel fills rsa during the recvmsg operation
	if compInfo.rsa == nil {
		// RSA is nil - this shouldn't happen, but handle gracefully
		ln.log("listen:recv:parse:error", func() string {
			return "rsa is nil in completion info"
		})
		// Note: Can't track metrics here - no connection identified yet
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	addr := extractAddrFromRSA(compInfo.rsa)
	if addr == nil {
		// Failed to extract address - log with details and resubmit
		family := compInfo.rsa.Addr.Family
		ln.log("listen:recv:parse:error", func() string {
			return fmt.Sprintf("failed to extract source address from RawSockaddrAny: family=%d (0=uninitialized, 2=AF_INET, 10=AF_INET6)", family)
		})
		// Note: Can't track metrics here - no connection identified yet
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	// Use buffer directly (kernel wrote directly to it via iovec)
	bufferSlice := buffer[:bytesReceived]

	// Deserialize packet (NewPacketFromData copies the data into packet structure)
	p, err := packet.NewPacketFromData(addr, bufferSlice)

	if err != nil {
		// Deserialization error - log and resubmit
		ln.log("listen:recv:parse:error", func() string {
			return fmt.Sprintf("failed to parse packet: %v", err)
		})
		// Note: Can't track metrics here - no connection identified yet (parse failed)
		ring.CQESeen(cqe)
		ln.recvBufferPool.Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	// After successful deserialization, we can return buffer to pool immediately
	// (NewPacketFromData has copied the data, so buffer is no longer needed)
	ln.recvBufferPool.Put(bufferPtr)

	// Route directly (bypass channels) - Channel Bypass Optimization
	// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
	h := p.Header()
	socketId := h.DestinationSocketId

	// Handle handshake packets (DestinationSocketId == 0)
	if socketId == 0 {
		if h.IsControlPacket && h.ControlType == packet.CTRLTYPE_HANDSHAKE {
			ln.log("listen:recv:handshake", func() string {
				return fmt.Sprintf("received handshake packet from %s", h.Addr.String())
			})
			select {
			case ln.backlog <- p:
				// Success - handshake packet queued to backlog
				// Note: Can't track metrics here - no connection yet (handshake in progress)
			default:
				ln.log("handshake:recv:error", func() string { return "backlog is full" })
				// Note: Can't track metrics here - no connection yet
				p.Decommission() // Clean up dropped packet
			}
		} else {
			// Non-handshake packet with socketId == 0 - drop it
			// Note: Can't track metrics here - no connection identified
			p.Decommission()
		}
		ring.CQESeen(cqe)
		return // Always resubmit to maintain constant pending count
	}

	// Lookup connection (sync.Map handles locking internally)
	val, ok := ln.conns.Load(socketId)
	if !ok {
		// Unknown destination - drop packet
		// During shutdown, connections may be closed before all packets are processed
		// Only log if not shutting down to avoid noise during graceful shutdown
		if !ln.isShutdown() {
			ln.log("listen:recv:error", func() string {
				return fmt.Sprintf("unknown destination socket ID: %d", socketId)
			})
		}
		// Track at listener level since we can't associate with a connection
		metrics.GetListenerMetrics().RecvConnLookupNotFoundIoUring.Add(1)
		ring.CQESeen(cqe)
		p.Decommission()
		return // Always resubmit to maintain constant pending count
	}

	conn := val.(*srtConn)
	if conn == nil {
		// Connection is nil - drop packet
		// Track at listener level since connection is nil
		metrics.GetListenerMetrics().RecvConnLookupNotFoundIoUring.Add(1)
		ring.CQESeen(cqe)
		p.Decommission()
		return // Always resubmit to maintain constant pending count
	}

	// Validate peer address (if required)
	if !ln.config.AllowPeerIpChange {
		if h.Addr.String() != conn.RemoteAddr().String() {
			// Wrong peer - drop packet
			ln.log("listen:recv:error", func() string {
				return fmt.Sprintf("packet from wrong peer: expected %s, got %s", conn.RemoteAddr().String(), h.Addr.String())
			})
			// Track metrics for wrong peer (we have connection now)
			if conn.metrics != nil {
				metrics.IncrementRecvErrorMetrics(conn.metrics, true, metrics.DropReasonWrongPeer)
			}
			ring.CQESeen(cqe)
			p.Decommission()
			return // Always resubmit to maintain constant pending count
		}
	}

	// Track successful receive (io_uring path)
	if conn.metrics != nil {
		metrics.IncrementRecvMetrics(conn.metrics, p, true, true, 0)
	}

	// Direct call to handlePacket (blocking mutex - never drops packets)
	conn.handlePacketDirect(p)

	// Mark CQE as seen (required by giouring)
	ring.CQESeen(cqe)
	// Always resubmit to maintain constant pending count (handled by caller)
}

// getRecvCompletion gets a single completion using polling (no blocking WaitCQE).
// This allows the handler to check ctx.Done() regularly and exit promptly when
// the context is cancelled, without waiting for QueueExit() to be called.
func (ln *listener) getRecvCompletion(ctx context.Context, ring *giouring.Ring) (*giouring.CompletionQueueEvent, *recvCompletionInfo) {
	// Use polling with PeekCQE instead of blocking WaitCQE
	// This allows us to check ctx.Done() regularly and exit promptly
	for {
		// Check context first
		select {
		case <-ctx.Done():
			return nil, nil
		default:
		}

		// Try non-blocking peek
		cqe, err := ring.PeekCQE()
		if err == nil {
			// Success - we have a completion, look it up and return
			compInfo := ln.lookupAndRemoveRecvCompletion(cqe, ring)
			if compInfo == nil {
				return nil, nil // Unknown request ID, skip
			}
			return cqe, compInfo
		}

		// EBADF means ring was closed via QueueExit()
		if err == syscall.EBADF {
			return nil, nil
		}

		// EAGAIN means no completions available - sleep and retry
		if err == syscall.EAGAIN {
			// Short sleep to avoid busy-spinning, but still responsive to ctx cancellation
			select {
			case <-ctx.Done():
				return nil, nil
			case <-time.After(ioUringPollInterval):
				continue
			}
		}

		// EINTR is normal (interrupted by signal) - retry immediately
		if err == syscall.EINTR {
			continue
		}

		// Other errors - log and return nil
		ln.log("listen:recv:completion:error", func() string {
			return fmt.Sprintf("error peeking completion: %v", err)
		})
		return nil, nil
	}
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
		bufferPtr := ln.recvBufferPool.Get().(*[]byte)
		buffer := *bufferPtr

		// Setup iovec using buffer directly
		var iovec syscall.Iovec
		iovec.Base = &buffer[0]
		iovec.SetLen(len(buffer))

		// Setup msghdr for UDP (to get source address)
		// Allocate rsa and msg on heap so they persist until completion is processed
		rsa := new(syscall.RawSockaddrAny)
		msg := new(syscall.Msghdr)
		msg.Name = (*byte)(unsafe.Pointer(rsa))
		msg.Namelen = uint32(syscall.SizeofSockaddrAny)
		msg.Iov = &iovec
		msg.Iovlen = 1

		// Generate unique request ID
		requestID := ln.recvRequestID.Add(1)

		// Create completion info
		compInfo := &recvCompletionInfo{
			buffer: bufferPtr,
			rsa:    rsa,
			msg:    msg,
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
			ln.recvBufferPool.Put(bufferPtr)
			break
		}

		// Prepare recvmsg operation
		// Pass pointer to heap-allocated msg so it stays valid until completion
		sqe.PrepareRecvMsg(ln.recvRingFd, msg, 0)
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
			// Flush any pending resubmits
			if pendingResubmits > 0 {
				ln.submitRecvRequestBatch(pendingResubmits)
			}
			// Skip drainRecvCompletions - it takes too long (5s timeout) and
			// the ring will be closed by cleanupIoUringRecv() anyway.
			// ln.drainRecvCompletions()
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
			// Don't log during shutdown if logger might be closed
			if !ln.isShutdown() {
				ln.log("listen:recv:drain", func() string {
					return "timeout draining receive completions"
				})
			}
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
