//go:build linux
// +build linux

package srt

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/randomizedcoder/giouring"
)

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

	// Initialize receive buffer pool (fixed size MSS)
	dl.recvBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, dl.config.MSS)
		},
	}

	// Initialize completion tracking
	dl.recvCompletions = make(map[uint64]*recvCompletionInfo)

	// Create context for completion handler
	dl.recvCompCtx, dl.recvCompCancel = context.WithCancel(context.Background())

	// Note: Completion handler will be started in Phase 3
	// For now, we just set up the infrastructure

	return nil
}

// cleanupIoUringRecv cleans up the io_uring receive ring for the dialer
func (dl *dialer) cleanupIoUringRecv() {
	if dl.recvRing == nil {
		return // Nothing to clean up
	}

	// Stop completion handler (if started in Phase 3)
	if dl.recvCompCancel != nil {
		dl.recvCompCancel()
	}

	// Wait for completion handler to finish (with timeout)
	done := make(chan struct{})
	go func() {
		dl.recvCompWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Completion handler finished
	case <-time.After(5 * time.Second):
		// Timeout - continue anyway (dialer doesn't have log method)
	}

	// Close the ring
	ring, ok := dl.recvRing.(*giouring.Ring)
	if ok {
		ring.QueueExit()
	}

	// Clean up completion map and return all buffers to pool
	dl.recvCompLock.Lock()
	for _, compInfo := range dl.recvCompletions {
		dl.recvBufferPool.Put(compInfo.buffer)
	}
	dl.recvCompletions = nil
	dl.recvCompLock.Unlock()

	// Close the duplicated file descriptor
	if dl.recvRingFd > 0 {
		syscall.Close(dl.recvRingFd)
		dl.recvRingFd = -1
	}
}
