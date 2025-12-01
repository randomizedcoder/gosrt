//go:build linux
// +build linux

package srt

import (
	"context"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

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

	// Note: Completion handler will be started in Phase 3
	// For now, we just set up the infrastructure

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
