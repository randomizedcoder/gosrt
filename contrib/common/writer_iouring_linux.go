//go:build linux
// +build linux

// Package common provides shared utilities for contrib programs.
//
// This file contains the io_uring-based async writer for Linux.
//
// IMPORTANT: This file uses the 'unsafe' package for io_uring integration.
//
// WHY UNSAFE IS NEEDED:
// io_uring is a Linux kernel interface that performs async I/O by sharing
// memory between userspace and kernel. To submit a write operation, we must
// pass the actual memory address of the buffer to the kernel. Go's type
// system does not allow this without unsafe.Pointer.
//
// The io_uring interface requires:
// 1. A raw pointer (uintptr) to the buffer data
// 2. The buffer must not be moved by GC during the async operation
// 3. The kernel reads directly from this memory address
//
// SAFETY MEASURES:
// - Buffers are allocated from sync.Pool and kept alive until completion
// - The completion handler returns buffers to the pool only after kernel is done
// - This pattern is proven in the SRT send path (connection_linux.go)
//
// USAGE:
// This writer is OPTIONAL and requires the -iouringoutput CLI flag.
// For most use cases, DirectWriter (writer.go) is recommended as it:
// - Has zero unsafe code
// - Uses battle-tested stdlib
// - Works on all platforms
package common

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe" // Required for io_uring buffer address passing - see comments above

	"github.com/randomizedcoder/giouring"
)

// ioUringPollInterval is the interval between io_uring completion queue polls
// when no completions are immediately available (EAGAIN).
//
// Trade-offs:
//   - Lower values (1ms): Faster shutdown detection, but ~1000 wakeups/sec when idle
//   - Higher values (100ms): Lower CPU usage when idle, but slower shutdown response
//
// 10ms provides a good balance: ~100 wakeups/sec when idle, and shutdown
// response time that feels instant to users (<10ms added latency).
//
// Note: This only affects idle polling. During active data flow, completions
// are immediately available and PeekCQE() returns without sleeping.
const ioUringPollInterval = 10 * time.Millisecond

// IoUringWriter provides async writes using Linux io_uring.
//
// Benefits over blocking writes:
// - Submit returns immediately (non-blocking)
// - Kernel executes write asynchronously
// - Application never blocks on slow consumers
//
// Trade-offs:
// - Requires unsafe package (see file-level comments)
// - More complex than DirectWriter
// - Linux only
type IoUringWriter struct {
	ring        *giouring.Ring
	fd          int
	pool        sync.Pool
	compLock    sync.Mutex
	completions map[uint64]*ioUringWriteCompletion
	requestID   atomic.Uint64
	wg          sync.WaitGroup
	closed      atomic.Bool
}

// ioUringWriteCompletion tracks a pending write operation
type ioUringWriteCompletion struct {
	buffer *[]byte // Buffer to return to pool after completion
}

// NewIoUringWriter creates an io_uring-based async writer for the given fd.
//
// Parameters:
//   - fd: File descriptor to write to (e.g., 1 for stdout)
//   - ringSize: Size of the io_uring ring (0 for default of 256)
//
// Returns error if io_uring initialization fails.
func NewIoUringWriter(fd int, ringSize uint32) (*IoUringWriter, error) {
	if ringSize == 0 {
		ringSize = 256
	}

	ring := giouring.NewRing()
	if err := ring.QueueInit(ringSize, 0); err != nil {
		return nil, fmt.Errorf("io_uring init failed: %w", err)
	}

	w := &IoUringWriter{
		ring:        ring,
		fd:          fd,
		completions: make(map[uint64]*ioUringWriteCompletion),
		pool: sync.Pool{
			New: func() interface{} {
				buf := make([]byte, 2048)
				return &buf
			},
		},
	}

	// Start completion handler goroutine
	w.wg.Add(1)
	go w.completionHandler()

	return w, nil
}

// NewIoUringStdoutWriter creates an io_uring writer for stdout.
func NewIoUringStdoutWriter() (*IoUringWriter, error) {
	return NewIoUringWriter(1, 256) // fd 1 = stdout
}

// NewIoUringFileWriter creates an io_uring writer for a file.
func NewIoUringFileWriter(fd int) (*IoUringWriter, error) {
	return NewIoUringWriter(fd, 256)
}

// Write submits data to io_uring and returns immediately.
//
// The write is executed asynchronously by the kernel. This method:
// 1. Copies data to a pooled buffer (required for async safety)
// 2. Submits the write to io_uring
// 3. Returns immediately (non-blocking)
//
// The completion handler returns the buffer to the pool when done.
func (w *IoUringWriter) Write(p []byte) (int, error) {
	if w.closed.Load() {
		return 0, io.ErrClosedPipe
	}

	// Get buffer from pool
	bufPtr, ok := w.pool.Get().(*[]byte)
	if !ok {
		// Pool should only contain *[]byte, this is a programming error
		panic("IoUringWriter pool contained non-*[]byte value")
	}

	// Ensure buffer is large enough
	if cap(*bufPtr) < len(p) {
		// Return small buffer and allocate larger one
		w.pool.Put(bufPtr)
		newBuf := make([]byte, len(p))
		bufPtr = &newBuf
	}

	// Copy data to pooled buffer (required: buffer must stay alive until completion)
	buf := (*bufPtr)[:len(p)]
	copy(buf, p)

	// Generate unique request ID
	reqID := w.requestID.Add(1)

	// Store completion info (protected by lock)
	w.compLock.Lock()
	w.completions[reqID] = &ioUringWriteCompletion{buffer: bufPtr}
	w.compLock.Unlock()

	// Get SQE from ring
	sqe := w.ring.GetSQE()
	if sqe == nil {
		// Ring full - clean up and return error
		w.compLock.Lock()
		delete(w.completions, reqID)
		w.compLock.Unlock()
		w.pool.Put(bufPtr)
		return 0, fmt.Errorf("io_uring ring full")
	}

	// Prepare write operation
	// UNSAFE: Required to pass buffer address to kernel (see file-level comments)
	sqe.PrepareWrite(w.fd, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), 0)
	sqe.SetData64(reqID)

	// Submit to kernel
	if _, err := w.ring.Submit(); err != nil {
		w.compLock.Lock()
		delete(w.completions, reqID)
		w.compLock.Unlock()
		w.pool.Put(bufPtr)
		return 0, fmt.Errorf("io_uring submit failed: %w", err)
	}

	return len(p), nil
}

// completionHandler processes io_uring completions in a dedicated goroutine.
// It returns buffers to the pool when writes complete.
//
// IMPORTANT: This handler uses polling with PeekCQE() instead of blocking WaitCQE().
// This allows the handler to check the closed flag regularly and exit promptly
// when Close() is called. WaitCQE() would block forever if there are no pending
// completions, preventing graceful shutdown.
//
// Error handling:
// - EBADF: Ring closed via QueueExit() - clean shutdown
// - EAGAIN: No completions available - sleep and retry
func (w *IoUringWriter) completionHandler() {
	defer w.wg.Done()

	for {
		// Check closed flag first
		if w.closed.Load() {
			return // Normal shutdown
		}

		// Use non-blocking PeekCQE instead of blocking WaitCQE
		// This allows us to check the closed flag regularly
		cqe, err := w.ring.PeekCQE()
		if err != nil {
			// EBADF means the ring was closed via QueueExit()
			if err == syscall.EBADF {
				return // Ring closed - normal shutdown
			}

			// EAGAIN means no completions available - sleep and retry
			if err == syscall.EAGAIN {
				// Check closed flag before sleeping
				if w.closed.Load() {
					return // Normal shutdown
				}
				// Short sleep to avoid busy-spinning (see ioUringPollInterval comment)
				time.Sleep(ioUringPollInterval)
				continue
			}

			// EINTR is normal (interrupted by signal) - retry immediately
			if err == syscall.EINTR {
				continue
			}

			// For other errors, check closed flag and continue
			if w.closed.Load() {
				return
			}
			continue
		}

		reqID := cqe.UserData

		// Return buffer to pool
		w.compLock.Lock()
		if info, ok := w.completions[reqID]; ok {
			w.pool.Put(info.buffer)
			delete(w.completions, reqID)
		}
		w.compLock.Unlock()

		w.ring.CQESeen(cqe)
	}
}

// Close shuts down the io_uring writer.
// It waits for pending completions before returning.
func (w *IoUringWriter) Close() error {
	w.closed.Store(true)
	w.ring.QueueExit()
	w.wg.Wait()

	// Return any remaining buffers to pool
	w.compLock.Lock()
	for _, info := range w.completions {
		w.pool.Put(info.buffer)
	}
	w.completions = nil
	w.compLock.Unlock()

	return nil
}

// IoUringOutputAvailable returns true on Linux where io_uring is supported.
func IoUringOutputAvailable() bool {
	return true
}

// Ensure IoUringWriter implements io.WriteCloser
var _ io.WriteCloser = (*IoUringWriter)(nil)
