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
	"unsafe" // Required for io_uring buffer address passing - see comments above

	"github.com/randomizedcoder/giouring"
)

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
	bufPtr := w.pool.Get().(*[]byte)

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
func (w *IoUringWriter) completionHandler() {
	defer w.wg.Done()

	for {
		cqe, err := w.ring.WaitCQE()
		if err != nil {
			if w.closed.Load() {
				return // Normal shutdown
			}
			continue // Retry on transient errors
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

