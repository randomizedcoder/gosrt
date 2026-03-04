//go:build linux
// +build linux

// UDP Echo Server using io_uring
//
// This example demonstrates how to use io_uring for UDP networking following
// the patterns established in the GoSRT library (listen_linux.go, connection_linux.go).
//
// Key concepts demonstrated:
// - PrepareRecvMsg for UDP receive (captures source address)
// - PrepareSendMsg for UDP send (to captured address)
// - WaitCQETimeout for efficient blocking (zero latency on completion)
// - Msghdr/Iovec/RawSockaddrAny structures for UDP
// - Completion tracking with request IDs
// - Buffer pooling with sync.Pool
// - BATCHED SUBMISSION: Prepare multiple SQEs, then single Submit() syscall
//   This is the key optimization from gosrt - reduces syscall overhead by ~32x
//
// Batch Submission Pattern (from listen_linux.go submitRecvRequestBatch):
//   1. Phase 1: Loop to prepare all SQEs (GetSQE + PrepareRecvMsg)
//   2. Phase 2: Single ring.Submit() for all prepared SQEs
//   3. On completion: accumulate resubmits, flush when batch threshold reached
//   4. On timeout: flush pending resubmits to prevent ring from running dry
//
// Usage:
//   go build -o udp_echo ./contrib/udp_echo
//   ./udp_echo -addr :9999
//
// Test with netcat:
//   echo "hello" | nc -u 127.0.0.1 9999

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/randomizedcoder/giouring"
)

const (
	// Ring configuration
	defaultRingSize  = 64   // io_uring ring size (power of 2)
	maxPacketSize    = 1500 // Maximum UDP packet size
	defaultBatchSize = 32   // Batch size for resubmissions (matches gosrt pattern)
	maxGetSQERetries = 3    // Retries when GetSQE() returns nil
	maxSubmitRetries = 3    // Retries for Submit() on transient errors
	retryBackoffUs   = 100  // Microseconds between retries

	// Timeout for WaitCQETimeout - kernel blocks until completion or timeout
	// 10ms provides good balance: responsive to completions AND shutdown signals
	waitTimeoutMs = 10
)

// Operation types to distinguish recv vs send completions
const (
	opRecv = iota
	opSend
)

// completionInfo tracks pending io_uring operations
type completionInfo struct {
	op     int                     // opRecv or opSend
	buffer []byte                  // Buffer for the operation
	msg    *syscall.Msghdr         // Msghdr for recv/send
	iovec  *syscall.Iovec          // Iovec pointing to buffer
	rsa    *syscall.RawSockaddrAny // Source address (recv) or dest address (send)
}

// echoServer is a simple UDP echo server using io_uring
type echoServer struct {
	addr     string
	ringSize uint32

	// Socket
	fd   int
	conn *net.UDPConn

	// io_uring
	ring *giouring.Ring

	// Completion tracking
	completions map[uint64]*completionInfo
	compLock    sync.Mutex
	nextID      uint64

	// Buffer pool for efficient memory reuse
	bufferPool sync.Pool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func main() {
	addr := flag.String("addr", ":9999", "UDP listen address")
	ringSize := flag.Uint("ringsize", defaultRingSize, "io_uring ring size (power of 2)")
	flag.Parse()

	// Create server
	server := &echoServer{
		addr:        *addr,
		ringSize:    uint32(*ringSize),
		completions: make(map[uint64]*completionInfo),
		bufferPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, maxPacketSize)
			},
		},
	}

	// Setup context for graceful shutdown
	server.ctx, server.cancel = context.WithCancel(context.Background())

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutdown signal received")
		server.cancel()
	}()

	// Run server
	if err := server.run(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func (s *echoServer) run() error {
	// Step 1: Create UDP socket
	if err := s.createSocket(); err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	defer func() {
		if closeErr := s.conn.Close(); closeErr != nil {
			log.Printf("Warning: failed to close connection: %v", closeErr)
		}
	}()

	log.Printf("UDP echo server listening on %s", s.addr)

	// Step 2: Initialize io_uring
	if err := s.initIoUring(); err != nil {
		return fmt.Errorf("failed to init io_uring: %w", err)
	}
	defer s.ring.QueueExit()

	log.Printf("io_uring initialized (ring_size=%d)", s.ringSize)

	// Step 3: Pre-populate ring with receive requests using batch submission
	// This is a key pattern from the main library - have receives ready before any packets arrive
	// Uses batch submission for efficiency (single syscall for all initial requests)
	s.submitRecvRequestBatch(int(s.ringSize / 2)) // Fill half the ring initially

	// Step 4: Run completion handler
	s.wg.Add(1)
	go s.completionHandler()

	// Wait for shutdown
	<-s.ctx.Done()
	log.Println("Shutting down...")

	// Wait for completion handler to exit
	s.wg.Wait()

	return nil
}

// createSocket creates and binds a UDP socket
func (s *echoServer) createSocket() error {
	// Parse address
	udpAddr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return err
	}

	// Create UDP connection
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	s.conn = conn

	// Get raw file descriptor for io_uring
	file, err := conn.File()
	if err != nil {
		return err
	}
	s.fd = int(file.Fd())

	// Don't close the file - we need the fd to stay valid
	// The fd is duplicated by File(), so we need to keep it open

	return nil
}

// initIoUring initializes the io_uring ring
func (s *echoServer) initIoUring() error {
	s.ring = giouring.NewRing()
	return s.ring.QueueInit(s.ringSize, 0)
}

// submitRecvRequestBatch submits multiple receive requests in a single batch
// This is the key pattern from gosrt's listen_linux.go - prepare ALL SQEs first,
// then submit with a SINGLE ring.Submit() syscall.
//
// Why batch? Each ring.Submit() is a syscall. At high packet rates:
// - Individual submits: 10,000 packets/sec = 10,000 syscalls/sec
// - Batched submits: 10,000 packets/sec = ~312 syscalls/sec (batch size 32)
//
// This reduces syscall overhead by ~32x at the cost of slightly more complex code.
func (s *echoServer) submitRecvRequestBatch(count int) {
	// Track what we've prepared for cleanup on error
	type pendingRequest struct {
		requestID uint64
		buffer    []byte
		rsa       *syscall.RawSockaddrAny
		msg       *syscall.Msghdr
		iovec     *syscall.Iovec
	}
	pending := make([]pendingRequest, 0, count)

	// Phase 1: Prepare all SQEs (no Submit yet)
	for i := 0; i < count; i++ {
		// Get buffer from pool
		buffer, ok := s.bufferPool.Get().([]byte)
		if !ok {
			// Pool should only contain []byte, this is a programming error
			panic("bufferPool contained non-[]byte value")
		}

		// Create structures for recvmsg (must stay alive until completion)
		rsa := new(syscall.RawSockaddrAny)
		iovec := new(syscall.Iovec)
		msg := new(syscall.Msghdr)

		// Setup iovec to point to our buffer
		iovec.Base = &buffer[0]
		iovec.SetLen(len(buffer))

		// Setup msghdr for UDP receive
		msg.Name = (*byte)(unsafe.Pointer(rsa))
		msg.Namelen = uint32(syscall.SizeofSockaddrAny)
		msg.Iov = iovec
		msg.Iovlen = 1

		// Generate request ID and store completion info
		s.compLock.Lock()
		requestID := s.nextID
		s.nextID++
		s.completions[requestID] = &completionInfo{
			op:     opRecv,
			buffer: buffer,
			msg:    msg,
			iovec:  iovec,
			rsa:    rsa,
		}
		s.compLock.Unlock()

		// Get SQE with retry loop (ring may be temporarily full)
		var sqe *giouring.SubmissionQueueEntry
		for retry := 0; retry < maxGetSQERetries; retry++ {
			sqe = s.ring.GetSQE()
			if sqe != nil {
				break
			}
			// Ring temporarily full - wait for completions to free space
			if retry < maxGetSQERetries-1 {
				time.Sleep(time.Duration(retryBackoffUs) * time.Microsecond)
			}
		}

		if sqe == nil {
			// Ring still full after retries - clean up this request and stop batching
			s.compLock.Lock()
			delete(s.completions, requestID)
			s.compLock.Unlock()
			s.bufferPool.Put(buffer)
			log.Printf("Warning: ring full after %d retries, submitted %d/%d requests", maxGetSQERetries, i, count)
			break
		}

		// Prepare the SQE (but don't submit yet!)
		sqe.PrepareRecvMsg(s.fd, msg, 0)
		sqe.SetData64(requestID)

		// Track for potential cleanup
		pending = append(pending, pendingRequest{
			requestID: requestID,
			buffer:    buffer,
			rsa:       rsa,
			msg:       msg,
			iovec:     iovec,
		})
	}

	// Phase 2: Single Submit() for ALL prepared SQEs
	if len(pending) > 0 {
		var err error
		for retry := 0; retry < maxSubmitRetries; retry++ {
			_, err = s.ring.Submit()
			if err == nil {
				break // Success!
			}
			// Only retry transient errors
			if err != syscall.EINTR && err != syscall.EAGAIN {
				break
			}
			if retry < maxSubmitRetries-1 {
				time.Sleep(time.Duration(retryBackoffUs) * time.Microsecond)
			}
		}

		if err != nil {
			// Batch submission failed - clean up ALL prepared requests
			log.Printf("Warning: batch submit failed: %v (cleaning up %d requests)", err, len(pending))
			s.compLock.Lock()
			for _, req := range pending {
				delete(s.completions, req.requestID)
				s.bufferPool.Put(req.buffer)
			}
			s.compLock.Unlock()
		}
	}
}

// submitSendRequest submits a send request to echo data back
func (s *echoServer) submitSendRequest(data []byte, rsa *syscall.RawSockaddrAny, rsaLen uint32) {
	// Get buffer from pool and copy data
	buffer, ok := s.bufferPool.Get().([]byte)
	if !ok {
		// Pool should only contain []byte, this is a programming error
		panic("bufferPool contained non-[]byte value")
	}
	n := copy(buffer, data)

	// Create structures for sendmsg
	iovec := new(syscall.Iovec)
	msg := new(syscall.Msghdr)

	// Setup iovec
	iovec.Base = &buffer[0]
	iovec.SetLen(n)

	// Setup msghdr for UDP send
	// Use the captured source address as destination (echo back)
	msg.Name = (*byte)(unsafe.Pointer(rsa))
	msg.Namelen = rsaLen
	msg.Iov = iovec
	msg.Iovlen = 1

	// Generate request ID
	s.compLock.Lock()
	requestID := s.nextID
	s.nextID++

	// Store completion info
	s.completions[requestID] = &completionInfo{
		op:     opSend,
		buffer: buffer,
		msg:    msg,
		iovec:  iovec,
		rsa:    rsa,
	}
	s.compLock.Unlock()

	// Get SQE and prepare send operation
	sqe := s.ring.GetSQE()
	if sqe == nil {
		s.compLock.Lock()
		delete(s.completions, requestID)
		s.compLock.Unlock()
		s.bufferPool.Put(buffer)
		log.Println("Warning: ring full, could not submit send request")
		return
	}

	// PrepareSendMsg sends to the address specified in msg.Name
	sqe.PrepareSendMsg(s.fd, msg, 0)
	sqe.SetData64(requestID)

	// Submit to kernel
	if _, err := s.ring.Submit(); err != nil {
		s.compLock.Lock()
		delete(s.completions, requestID)
		s.compLock.Unlock()
		s.bufferPool.Put(buffer)
		log.Printf("Warning: submit failed: %v", err)
	}
}

// completionHandler processes io_uring completions with batched resubmission
// This matches the gosrt library pattern (listen_linux.go recvCompletionHandler):
// - Process completions immediately (low latency)
// - Batch resubmissions (reduced syscalls)
// - Flush on timeout to prevent ring from running dry
func (s *echoServer) completionHandler() {
	defer s.wg.Done()

	// Create timeout for WaitCQETimeout
	// Kernel blocks until completion arrives OR timeout expires
	timeout := syscall.NsecToTimespec(int64(waitTimeoutMs * time.Millisecond))

	// Track pending resubmissions for batching
	// This is the key pattern from gosrt - accumulate resubmits and flush in batches
	pendingResubmits := 0

	for {
		// Check for shutdown (non-blocking check before blocking wait)
		select {
		case <-s.ctx.Done():
			// Flush any pending resubmits before exiting
			if pendingResubmits > 0 {
				s.submitRecvRequestBatch(pendingResubmits)
			}
			return
		default:
		}

		// Block until completion OR timeout
		cqe, err := s.ring.WaitCQETimeout(&timeout)
		if err != nil {
			if err == syscall.ETIME {
				// CRITICAL: On timeout, flush pending resubmits to prevent ring from running dry!
				// Without this, the ring can empty out and stop receiving packets.
				// This mirrors gosrt's recvCompletionHandler behavior.
				if pendingResubmits > 0 {
					s.submitRecvRequestBatch(pendingResubmits)
					pendingResubmits = 0
				}
				continue
			}
			if err == syscall.EINTR {
				// Interrupted by signal - flush and retry
				if pendingResubmits > 0 {
					s.submitRecvRequestBatch(pendingResubmits)
					pendingResubmits = 0
				}
				continue
			}
			if err == syscall.EBADF {
				// Ring closed - normal shutdown
				return
			}
			log.Printf("WaitCQETimeout error: %v", err)
			continue
		}

		// Look up and remove completion info
		requestID := cqe.UserData

		s.compLock.Lock()
		compInfo, exists := s.completions[requestID]
		if !exists {
			s.compLock.Unlock()
			s.ring.CQESeen(cqe)
			log.Printf("Warning: unknown request ID %d", requestID)
			continue
		}
		delete(s.completions, requestID)
		s.compLock.Unlock()

		// Handle based on operation type
		// Note: handleRecvCompletion returns true if we should resubmit
		switch compInfo.op {
		case opRecv:
			if s.handleRecvCompletion(cqe, compInfo) {
				pendingResubmits++
			}
		case opSend:
			s.handleSendCompletion(cqe, compInfo)
			// Send completions don't need resubmission
		}

		// Mark CQE as seen (frees slot in CQ)
		s.ring.CQESeen(cqe)

		// Batch resubmit when we've accumulated enough
		// This reduces syscall overhead significantly at high throughput
		if pendingResubmits >= defaultBatchSize {
			s.submitRecvRequestBatch(pendingResubmits)
			pendingResubmits = 0
		}
	}
}

// handleRecvCompletion processes a receive completion
// Returns true if caller should schedule a resubmit (batched by caller)
// This matches the gosrt pattern where completions are processed immediately
// but resubmissions are batched for efficiency.
func (s *echoServer) handleRecvCompletion(cqe *giouring.CompletionQueueEvent, compInfo *completionInfo) bool {
	if cqe.Res < 0 {
		// Receive error
		errno := syscall.Errno(-cqe.Res)
		log.Printf("Recv error: %v", errno)
		s.bufferPool.Put(compInfo.buffer)
		// Signal caller to resubmit (will be batched)
		return true
	}

	bytesReceived := int(cqe.Res)
	if bytesReceived == 0 {
		s.bufferPool.Put(compInfo.buffer)
		return true // Resubmit needed
	}

	// Extract source address for logging
	srcAddr := sockaddrToString(compInfo.rsa)
	log.Printf("Received %d bytes from %s: %q", bytesReceived, srcAddr, string(compInfo.buffer[:bytesReceived]))

	// Echo back: send the data to the source address
	// We reuse the rsa (source address becomes destination)
	s.submitSendRequest(compInfo.buffer[:bytesReceived], compInfo.rsa, compInfo.msg.Namelen)

	// Return recv buffer to pool
	s.bufferPool.Put(compInfo.buffer)

	// Signal caller to schedule a resubmit (will be batched)
	// This maintains the "pre-populated" ring state
	return true
}

// handleSendCompletion processes a send completion
func (s *echoServer) handleSendCompletion(cqe *giouring.CompletionQueueEvent, compInfo *completionInfo) {
	if cqe.Res < 0 {
		errno := syscall.Errno(-cqe.Res)
		log.Printf("Send error: %v", errno)
	} else {
		bytesSent := int(cqe.Res)
		log.Printf("Sent %d bytes (echo)", bytesSent)
	}

	// Return buffer to pool
	s.bufferPool.Put(compInfo.buffer)
	// Note: compInfo.rsa was shared with the recv, already freed
}

// sockaddrToString converts a RawSockaddrAny to a human-readable string
func sockaddrToString(rsa *syscall.RawSockaddrAny) string {
	// Check address family
	switch rsa.Addr.Family {
	case syscall.AF_INET:
		// IPv4
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(rsa))
		ip := net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
		port := int(sa.Port>>8) | int(sa.Port&0xff)<<8 // Convert from network byte order
		return fmt.Sprintf("%s:%d", ip, port)
	case syscall.AF_INET6:
		// IPv6
		sa := (*syscall.RawSockaddrInet6)(unsafe.Pointer(rsa))
		ip := net.IP(sa.Addr[:])
		port := int(sa.Port>>8) | int(sa.Port&0xff)<<8
		return fmt.Sprintf("[%s]:%d", ip, port)
	default:
		return fmt.Sprintf("unknown family %d", rsa.Addr.Family)
	}
}
