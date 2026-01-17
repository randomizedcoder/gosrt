//go:build linux
// +build linux

package srt

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
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

// recvRingState holds all state for a single receive io_uring ring.
// Each ring has its own dedicated completion handler goroutine.
// This struct is designed for ZERO cross-ring locking:
// - completions: owned exclusively by this ring's handler (no lock needed)
// - nextID: owned exclusively by this ring's handler (no lock needed)
// - ring: owned exclusively by this ring's handler after init
// - fd: shared but read-only (no lock needed)
// See multi_iouring_design.md Section 4.1 for design rationale.
type recvRingState struct {
	ring        *giouring.Ring                 // io_uring ring (owned by handler)
	completions map[uint64]*recvCompletionInfo // Maps request ID to completion info (no lock - owned)
	nextID      uint64                         // Request ID counter (no atomic - owned)
	fd          int                            // Socket fd (shared, read-only)
	ringIndex   int                            // Ring index for metrics/logging
}

// newRecvRingState creates a new recvRingState with initialized completion map
func newRecvRingState(ring *giouring.Ring, fd int, ringIndex int) *recvRingState {
	return &recvRingState{
		ring:        ring,
		completions: make(map[uint64]*recvCompletionInfo),
		nextID:      0,
		fd:          fd,
		ringIndex:   ringIndex,
	}
}

// getNextID returns the next request ID for this ring (no atomic needed - single owner)
func (rs *recvRingState) getNextID() uint64 {
	rs.nextID++
	return rs.nextID
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

// initializeIoUringRecv initializes the io_uring receive ring(s) for the listener
// When IoUringRecvRingCount > 1, creates multiple independent rings for parallel processing.
// See multi_iouring_design.md Section 4.1 for design rationale.
func (ln *listener) initializeIoUringRecv() error {
	if !ln.config.IoUringRecvEnabled {
		return nil // io_uring not enabled, skip initialization
	}

	// Extract socket file descriptor (shared by all rings)
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

	// Determine ring count (default: 1)
	ringCount := ln.config.IoUringRecvRingCount
	if ringCount <= 0 {
		ringCount = 1
	}

	// Multi-ring mode: create multiple independent ring states
	if ringCount > 1 {
		return ln.initializeIoUringRecvMultiRing(ringSize, ringCount)
	}

	// Single-ring mode (legacy path): use original fields
	return ln.initializeIoUringRecvSingleRing(ringSize)
}

// initializeIoUringRecvSingleRing initializes a single io_uring ring (legacy path)
func (ln *listener) initializeIoUringRecvSingleRing(ringSize uint32) error {
	// Initialize per-ring metrics (unified approach - even single-ring uses per-ring metrics)
	// This MUST be done before starting handler, otherwise metric increments are silently dropped
	metrics.GetListenerMetrics().InitListenerRecvRingMetrics(1)

	// Create io_uring ring
	ring := giouring.NewRing()
	err := ring.QueueInit(ringSize, 0) // ringSize entries, no flags
	if err != nil {
		// io_uring unavailable or failed - log and continue without it
		ln.log("listen:io_uring:recv:init", func() string {
			return fmt.Sprintf("failed to create io_uring receive ring: %v (falling back to ReadFrom)", err)
		})
		return err // Return error but don't fail listener creation
	}

	ln.recvRing = ring // Store as interface{} for conditional compilation

	// Note: Using shared globalRecvBufferPool (see buffers.go)
	// Both io_uring and standard paths share the same global pool

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

// initializeIoUringRecvMultiRing initializes multiple independent io_uring rings
// Each ring has its own completion handler for parallel processing.
// See multi_iouring_design.md Section 4.1 for design rationale.
func (ln *listener) initializeIoUringRecvMultiRing(ringSize uint32, ringCount int) error {
	// Initialize per-ring metrics FIRST (unified approach - always use per-ring metrics)
	// This MUST be done before starting handlers, otherwise metric increments are silently dropped
	metrics.GetListenerMetrics().InitListenerRecvRingMetrics(ringCount)

	// Create slice for ring states
	ln.recvRingStates = make([]*recvRingState, 0, ringCount)

	// Calculate per-ring initial pending (divide total across rings)
	initialPending := ln.config.IoUringRecvInitialPending
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
			ln.cleanupIoUringRecvMultiRing()
			ln.log("listen:io_uring:recv:init", func() string {
				return fmt.Sprintf("failed to create io_uring receive ring %d: %v (falling back to ReadFrom)", i, err)
			})
			return err
		}

		// Create ring state (owns completion map and ID counter)
		state := newRecvRingState(ring, ln.recvRingFd, i)
		ln.recvRingStates = append(ln.recvRingStates, state)

		// Start independent completion handler for this ring
		ln.recvCompWg.Add(1)
		go ln.recvCompletionHandlerIndependent(ln.ctx, state)

		// Pre-populate this ring
		ln.prePopulateRecvRingForState(state, perRingPending)
	}

	ln.log("listen:io_uring:recv:init", func() string {
		return fmt.Sprintf("io_uring multi-ring receive initialized: rings=%d, ring_size=%d, per_ring_pending=%d, fd=%d",
			ringCount, ringSize, perRingPending, ln.recvRingFd)
	})

	return nil
}

// cleanupIoUringRecv cleans up the io_uring receive ring(s) for the listener
// Following context_and_cancellation_design.md pattern:
// 1. Context is cancelled by parent (ln.ctx via server shutdown)
// 2. Wait for handler(s) to exit (via WaitGroup)
// 3. Only then clean up resources (QueueExit)
func (ln *listener) cleanupIoUringRecv() {
	// Multi-ring mode: use dedicated cleanup
	if len(ln.recvRingStates) > 0 {
		ln.cleanupIoUringRecvMultiRing()
		return
	}

	// Single-ring mode (legacy path)
	if ln.recvRing == nil {
		return // Nothing to clean up
	}

	// Step 1: Context should already be cancelled by parent (server context cancellation)
	// The handler checks ln.ctx.Done() at top of loop and will exit within ~10ms

	// Step 2: Wait for completion handler to exit BEFORE calling QueueExit()
	// We MUST wait because QueueExit() unmaps ring memory - if handler is still
	// inside WaitCQETimeout(), the giouring library will SIGSEGV when it tries
	// to peek at the unmapped CQ.
	done := make(chan struct{})
	go func() {
		ln.recvCompWg.Wait()
		close(done)
	}()

	handlerExited := false
	select {
	case <-done:
		handlerExited = true
	case <-time.After(2 * time.Second):
		// CRITICAL: Handler did not exit - DO NOT call QueueExit
		// Minor resource leak is better than SIGSEGV crash
		if ln.config.Logger != nil {
			ln.config.Logger.Print("listen:io_uring:recv:cleanup", 0, 2, func() string {
				return "CRITICAL: completion handler did not exit within 2s - skipping QueueExit to prevent SIGSEGV"
			})
		}
	}

	// Step 3: Only close ring if handler has exited
	if handlerExited {
		ring, ok := ln.recvRing.(*giouring.Ring)
		if ok {
			ring.QueueExit()
		}
		ln.recvRing = nil // Fail gracefully if any late accesses
	}

	// Step 4: Clean up completion map and return all buffers to pool
	ln.recvCompLock.Lock()
	for _, compInfo := range ln.recvCompletions {
		GetRecvBufferPool().Put(compInfo.buffer)
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
	bufferPtr := GetRecvBufferPool().Get().(*[]byte)
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
	for i := 0; i < ioUringMaxGetSQERetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break // Got an SQE, proceed
		}

		// Track retry (ring temporarily full)
		ln.incrementListenerRecvGetSQERetries(0) // ringIdx=0 for single-ring mode

		// Ring full - wait a bit and retry (completions may free up space)
		if i < ioUringMaxGetSQERetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if sqe == nil {
		// Ring still full after retries - clean up (same pattern as send path)
		ln.recvCompLock.Lock()
		delete(ln.recvCompletions, requestID)
		ln.recvCompLock.Unlock()

		GetRecvBufferPool().Put(bufferPtr)

		// Track ring full error
		ln.incrementListenerRecvSubmitRingFull(0) // ringIdx=0 for single-ring mode

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
		ln.incrementListenerRecvSubmitRetries(0) // ringIdx=0 for single-ring mode

		// Transient error - wait and retry
		if i < ioUringMaxSubmitRetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if err != nil {
		// Submission failed - clean up (same pattern as send path)
		ln.recvCompLock.Lock()
		delete(ln.recvCompletions, requestID)
		ln.recvCompLock.Unlock()

		GetRecvBufferPool().Put(bufferPtr)

		// Track submit error
		ln.incrementListenerRecvSubmitError(0) // ringIdx=0 for single-ring mode

		ln.log("listen:recv:error", func() string {
			return fmt.Sprintf("failed to submit receive request: %v", err)
		})
		return
	}

	// Request submitted successfully - track success
	ln.incrementListenerRecvSubmitSuccess(0) // ringIdx=0 for single-ring mode

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
	// Note: buffer variable removed - Phase 2 uses bufferPtr directly for zero-copy

	// Check for receive errors
	if cqe.Res < 0 {
		errno := -cqe.Res
		ln.log("listen:recv:completion:error", func() string {
			return fmt.Sprintf("receive failed: %s (errno %d)", syscall.Errno(errno).Error(), errno)
		})
		// Note: Can't track metrics here - no connection identified yet
		ring.CQESeen(cqe)
		GetRecvBufferPool().Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	// Successful receive
	bytesReceived := int(cqe.Res)
	if bytesReceived == 0 {
		// Empty datagram - return buffer and resubmit
		// Note: Can't track metrics here - no connection identified yet
		ring.CQESeen(cqe)
		GetRecvBufferPool().Put(bufferPtr)
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
		GetRecvBufferPool().Put(bufferPtr)
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
		GetRecvBufferPool().Put(bufferPtr)
		return // Always resubmit to maintain constant pending count
	}

	// Phase 2: Zero-copy - buffer lifetime extends until packet delivery
	// The packet will reference the buffer directly via recvBuffer field.
	// Buffer is returned to pool by receiver.releasePacketFully() after delivery.

	// Get packet from pool and unmarshal with zero-copy
	p := packet.NewPacket(addr)

	// UnmarshalZeroCopy stores buffer reference FIRST (before validation)
	// This ensures DecommissionWithBuffer can always return the buffer
	if err := p.UnmarshalZeroCopy(bufferPtr, bytesReceived, addr); err != nil {
		// Deserialization error - log, cleanup, and resubmit
		ln.log("listen:recv:parse:error", func() string {
			return fmt.Sprintf("failed to parse packet: %v", err)
		})
		// Note: Can't track metrics here - no connection identified yet (parse failed)
		// DecommissionWithBuffer returns buffer to pool and decommissions packet
		p.DecommissionWithBuffer(GetRecvBufferPool())
		ring.CQESeen(cqe)
		return // Always resubmit to maintain constant pending count
	}

	// NOTE: Buffer is NOT returned to pool here! It's referenced by the packet.
	// The buffer will be returned to pool by receiver.releasePacketFully() after
	// packet delivery (Phase 2: zero-copy buffer lifetime extension).

	// Route directly (bypass channels) - Channel Bypass Optimization
	// Cache header pointer to avoid multiple function calls (optimization: reduce Header() overhead)
	h := p.Header()
	socketId := h.DestinationSocketId

	// Debug logging for sequence analysis (topic: listen:io_uring:completion:seq)
	// This helps diagnose out-of-order packet delivery in io_uring path
	if !h.IsControlPacket {
		ln.log("listen:io_uring:completion:seq", func() string {
			return fmt.Sprintf("DATA seq=%d reqID=%d socketID=0x%08x",
				h.PacketSequenceNumber.Val(), cqe.UserData, socketId)
		})
	}

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

// getRecvCompletion gets a single completion using blocking wait with timeout.
// WaitCQETimeout blocks in the kernel until either:
//  1. A completion arrives (returns immediately - zero latency!)
//  2. Timeout expires (returns ETIME, allows ctx.Done() check)
//  3. Ring is closed (returns EBADF, normal shutdown)
func (ln *listener) getRecvCompletion(ctx context.Context, ring *giouring.Ring) (*giouring.CompletionQueueEvent, *recvCompletionInfo) {
	for {
		// Check context first (non-blocking)
		select {
		case <-ctx.Done():
			ln.incrementListenerRecvCompletionCtxCancelled(0) // ringIdx=0 for single-ring mode
			return nil, nil
		default:
		}

		// Block waiting for completion OR timeout
		cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
		if err == nil {
			// Success - we have a completion, look it up and return
			ln.incrementListenerRecvCompletionSuccess(0) // ringIdx=0 for single-ring mode
			compInfo := ln.lookupAndRemoveRecvCompletion(cqe, ring)
			if compInfo == nil {
				return nil, nil // Unknown request ID, skip
			}
			return cqe, compInfo
		}

		// EBADF means ring was closed via QueueExit()
		if err == syscall.EBADF {
			ln.incrementListenerRecvCompletionEBADF(0) // ringIdx=0 for single-ring mode
			return nil, nil
		}

		// ETIME means timeout expired - loop back to check ctx.Done()
		if err == syscall.ETIME {
			ln.incrementListenerRecvCompletionTimeout(0) // ringIdx=0 for single-ring mode
			continue
		}

		// EINTR is normal (interrupted by signal) - retry immediately
		if err == syscall.EINTR {
			ln.incrementListenerRecvCompletionEINTR(0) // ringIdx=0 for single-ring mode
			continue
		}

		// Other errors - log and return nil
		ln.incrementListenerRecvCompletionError(0) // ringIdx=0 for single-ring mode
		ln.log("listen:recv:completion:error", func() string {
			return fmt.Sprintf("error waiting for completion: %v", err)
		})
		return nil, nil
	}
}

// Helper functions for incrementing listener recv per-ring metrics.
// These use the global ListenerMetrics with per-ring counters.
// See multi_iouring_design.md Section 5.12 for the unified metrics approach.

func (ln *listener) incrementListenerRecvCompletionSuccess(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].CompletionSuccess.Add(1)
	}
}

func (ln *listener) incrementListenerRecvCompletionTimeout(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].CompletionTimeout.Add(1)
	}
}

func (ln *listener) incrementListenerRecvCompletionEBADF(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].CompletionEBADF.Add(1)
	}
}

func (ln *listener) incrementListenerRecvCompletionEINTR(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].CompletionEINTR.Add(1)
	}
}

func (ln *listener) incrementListenerRecvCompletionError(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].CompletionError.Add(1)
	}
}

func (ln *listener) incrementListenerRecvCompletionCtxCancelled(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].CompletionCtxCancelled.Add(1)
	}
}

// Helper functions for incrementing listener recv submission per-ring metrics.
func (ln *listener) incrementListenerRecvSubmitSuccess(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].SubmitSuccess.Add(1)
	}
}

func (ln *listener) incrementListenerRecvSubmitRingFull(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].SubmitRingFull.Add(1)
	}
}

func (ln *listener) incrementListenerRecvSubmitError(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].SubmitError.Add(1)
	}
}

func (ln *listener) incrementListenerRecvGetSQERetries(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].GetSQERetries.Add(1)
	}
}

func (ln *listener) incrementListenerRecvSubmitRetries(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].SubmitRetries.Add(1)
	}
}

func (ln *listener) incrementListenerRecvPacketsProcessed(ringIdx int) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].PacketsProcessed.Add(1)
	}
}

func (ln *listener) incrementListenerRecvBytesProcessed(ringIdx int, bytes uint64) {
	lm := metrics.GetListenerMetrics()
	if lm.IoUringRecvRingMetrics != nil && ringIdx < len(lm.IoUringRecvRingMetrics) {
		lm.IoUringRecvRingMetrics[ringIdx].BytesProcessed.Add(bytes)
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
		bufferPtr := GetRecvBufferPool().Get().(*[]byte)
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
			GetRecvBufferPool().Put(bufferPtr)
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
				GetRecvBufferPool().Put(compInfos[i].buffer)
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
			GetRecvBufferPool().Put(compInfo.buffer)

			ring.CQESeen(cqe)
		}
	}
}

// =============================================================================
// Multi-Ring Support Functions
// =============================================================================

// recvCompletionHandlerIndependent is the completion handler for a specific ring (multi-ring mode)
// Each ring has its own handler goroutine - NO cross-ring locking required.
// The handler owns its ring state's completion map and ID counter exclusively.
// See multi_iouring_design.md Section 4.1 for design rationale.
func (ln *listener) recvCompletionHandlerIndependent(ctx context.Context, state *recvRingState) {
	defer ln.recvCompWg.Done()

	ring := state.ring
	if ring == nil {
		return
	}

	// Get batch size from config (default: 32)
	// NOTE: Must be smaller than initial pending (default 128) otherwise ring runs dry
	// before batch threshold is reached, causing fallback to readfrom.
	batchSize := ln.config.IoUringRecvBatchSize
	if batchSize <= 0 {
		batchSize = 32 // Small batches ensure ring doesn't run dry
	}

	// Track pending resubmissions for batching
	pendingResubmits := 0

	for {
		// Get single completion (process immediately for low latency)
		cqe, compInfo, result := ln.getRecvCompletionFromRing(ctx, state)

		switch result {
		case completionResultSuccess:
			// Process completion immediately
			ln.processRecvCompletionFromRing(state, cqe, compInfo)

			// Track resubmission for batching
			pendingResubmits++

			// Batch resubmit when we've accumulated enough
			if pendingResubmits >= batchSize {
				ln.submitRecvRequestBatchToRing(state, pendingResubmits)
				pendingResubmits = 0
			}

		case completionResultTimeout:
			// CRITICAL: On timeout, flush any pending resubmits to prevent ring from running dry
			// Without this, the ring can empty out and never receive more completions
			if pendingResubmits > 0 {
				ln.submitRecvRequestBatchToRing(state, pendingResubmits)
				pendingResubmits = 0
			}
			// Continue waiting for more completions

		case completionResultShutdown:
			// Flush any pending resubmits before exiting
			if pendingResubmits > 0 {
				ln.submitRecvRequestBatchToRing(state, pendingResubmits)
			}
			return

		case completionResultError:
			// Log already done in getRecvCompletionFromRing, continue
			continue
		}
	}
}

// completionResult represents the result of waiting for a completion
type completionResult int

const (
	completionResultSuccess completionResult = iota
	completionResultTimeout
	completionResultShutdown
	completionResultError
)

// getRecvCompletionFromRing gets a completion from a specific ring state (multi-ring mode)
// NO locking - handler owns this ring's completion map exclusively
// Returns:
//   - (cqe, compInfo, completionResultSuccess) on successful completion
//   - (nil, nil, completionResultTimeout) on timeout (caller should flush pending resubmits)
//   - (nil, nil, completionResultShutdown) on context cancellation or EBADF
//   - (nil, nil, completionResultError) on other errors
func (ln *listener) getRecvCompletionFromRing(ctx context.Context, state *recvRingState) (*giouring.CompletionQueueEvent, *recvCompletionInfo, completionResult) {
	ring := state.ring
	ringIdx := state.ringIndex

	// Check context first (non-blocking)
	select {
	case <-ctx.Done():
		ln.incrementListenerRecvCompletionCtxCancelled(ringIdx)
		return nil, nil, completionResultShutdown
	default:
	}

	// Block waiting for completion OR timeout (single attempt)
	cqe, err := ring.WaitCQETimeout(&ioUringWaitTimeout)
	if err == nil {
		// Success - look up completion info (NO LOCK - we own this map)
		ln.incrementListenerRecvCompletionSuccess(ringIdx)
		requestID := cqe.UserData

		compInfo, exists := state.completions[requestID]
		if !exists {
			ln.log("listen:recv:completion:error", func() string {
				return fmt.Sprintf("completion for unknown request ID: %d (ring %d)", requestID, state.ringIndex)
			})
			ring.CQESeen(cqe)
			return nil, nil, completionResultError
		}
		delete(state.completions, requestID)
		return cqe, compInfo, completionResultSuccess
	}

	// Handle errors
	if err == syscall.EBADF {
		ln.incrementListenerRecvCompletionEBADF(ringIdx)
		return nil, nil, completionResultShutdown
	}
	if err == syscall.ETIME {
		ln.incrementListenerRecvCompletionTimeout(ringIdx)
		return nil, nil, completionResultTimeout
	}
	if err == syscall.EINTR {
		ln.incrementListenerRecvCompletionEINTR(ringIdx)
		return nil, nil, completionResultTimeout // Treat EINTR like timeout
	}

	ln.incrementListenerRecvCompletionError(ringIdx)
	ln.log("listen:recv:completion:error", func() string {
		return fmt.Sprintf("error waiting for completion (ring %d): %v", state.ringIndex, err)
	})
	return nil, nil, completionResultError
}

// processRecvCompletionFromRing processes a completion from a specific ring state
// Reuses existing processRecvCompletion logic - the packet handling is identical
func (ln *listener) processRecvCompletionFromRing(state *recvRingState, cqe *giouring.CompletionQueueEvent, compInfo *recvCompletionInfo) {
	// Reuse existing completion processing (handles packet deserialization and routing)
	ln.processRecvCompletion(state.ring, cqe, compInfo)
}

// submitRecvRequestToRing submits a single receive request to a specific ring (multi-ring mode)
// NO locking - handler owns this ring's completion map and ID counter exclusively
func (ln *listener) submitRecvRequestToRing(state *recvRingState) {
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
	ringIdx := state.ringIndex

	// Get SQE with retry
	var sqe *giouring.SubmissionQueueEntry
	for i := 0; i < ioUringMaxGetSQERetries; i++ {
		sqe = ring.GetSQE()
		if sqe != nil {
			break
		}
		ln.incrementListenerRecvGetSQERetries(ringIdx)
		if i < ioUringMaxGetSQERetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if sqe == nil {
		// Ring full - clean up (NO LOCK)
		delete(state.completions, requestID)
		GetRecvBufferPool().Put(bufferPtr)
		ln.incrementListenerRecvSubmitRingFull(ringIdx)
		ln.log("listen:recv:error", func() string {
			return fmt.Sprintf("io_uring ring %d full after retries", state.ringIndex)
		})
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
		ln.incrementListenerRecvSubmitRetries(ringIdx)
		if i < ioUringMaxSubmitRetries-1 {
			time.Sleep(ioUringRetryBackoff)
		}
	}

	if err != nil {
		// Submission failed - clean up (NO LOCK)
		delete(state.completions, requestID)
		GetRecvBufferPool().Put(bufferPtr)
		ln.incrementListenerRecvSubmitError(ringIdx)
		ln.log("listen:recv:error", func() string {
			return fmt.Sprintf("failed to submit receive request (ring %d): %v", state.ringIndex, err)
		})
		return
	}

	ln.incrementListenerRecvSubmitSuccess(ringIdx)
}

// submitRecvRequestBatchToRing submits multiple receive requests to a specific ring
// Batches submissions for efficiency (reduced syscalls)
// NO locking - handler owns this ring's completion map and ID counter exclusively
func (ln *listener) submitRecvRequestBatchToRing(state *recvRingState, count int) {
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
			ln.incrementListenerRecvSubmitError(state.ringIndex)
			ln.log("listen:recv:error", func() string {
				return fmt.Sprintf("failed to submit receive batch (ring %d): %v", state.ringIndex, err)
			})
		} else {
			// Success - increment metrics for each request in batch
			lm := metrics.GetListenerMetrics()
			if lm.IoUringRecvRingMetrics != nil && state.ringIndex < len(lm.IoUringRecvRingMetrics) {
				lm.IoUringRecvRingMetrics[state.ringIndex].SubmitSuccess.Add(uint64(len(requestIDs)))
			}
		}
	}
}

// prePopulateRecvRingForState pre-populates a specific ring with initial pending receives
func (ln *listener) prePopulateRecvRingForState(state *recvRingState, count int) {
	for i := 0; i < count; i++ {
		ln.submitRecvRequestToRing(state)
	}
}

// cleanupIoUringRecvMultiRing cleans up multi-ring resources
func (ln *listener) cleanupIoUringRecvMultiRing() {
	if len(ln.recvRingStates) == 0 {
		return
	}

	// Wait for all handlers to exit (context should already be cancelled)
	done := make(chan struct{})
	go func() {
		ln.recvCompWg.Wait()
		close(done)
	}()

	handlerExited := false
	select {
	case <-done:
		handlerExited = true
	case <-time.After(2 * time.Second):
		if ln.config.Logger != nil {
			ln.config.Logger.Print("listen:io_uring:recv:cleanup", 0, 2, func() string {
				return "CRITICAL: multi-ring completion handlers did not exit within 2s"
			})
		}
	}

	// Clean up each ring state
	if handlerExited {
		for _, state := range ln.recvRingStates {
			if state.ring != nil {
				state.ring.QueueExit()
			}
			// Return buffers to pool (NO LOCK - handlers have exited)
			for _, compInfo := range state.completions {
				GetRecvBufferPool().Put(compInfo.buffer)
			}
		}
	}

	ln.recvRingStates = nil

	// Close the duplicated file descriptor
	if ln.recvRingFd > 0 {
		syscall.Close(ln.recvRingFd)
		ln.recvRingFd = -1
	}
}
