// Package common provides shared utilities for contrib programs.
//
// This file contains high-performance writers for output destinations.
// All writers in this file use only the Go standard library - no unsafe package.
package common

import (
	"io"
	"os"
)

// NullWriter discards all data written to it.
// Useful for profiling/benchmarking without I/O overhead.
type NullWriter struct{}

// Write discards all data and returns success.
func (n *NullWriter) Write(p []byte) (int, error) { return len(p), nil }

// Close is a no-op for NullWriter.
func (n *NullWriter) Close() error { return nil }

// DirectWriter wraps os.File for zero-overhead writes.
//
// Benefits over NonblockingWriter:
//   - No locks in our code
//   - No channels
//   - Single syscall per write
//   - Uses battle-tested stdlib
//
// This is the recommended writer for most use cases (stdout, files).
type DirectWriter struct {
	file      *os.File
	closeFile bool // Whether to close the file on Close()
}

// NewDirectWriter creates a writer for the given file.
// If closeOnClose is true, the file will be closed when Close() is called.
func NewDirectWriter(f *os.File, closeOnClose bool) *DirectWriter {
	return &DirectWriter{
		file:      f,
		closeFile: closeOnClose,
	}
}

// NewStdoutWriter creates a writer for stdout.
// The underlying file (os.Stdout) is not closed on Close().
func NewStdoutWriter() *DirectWriter {
	return &DirectWriter{
		file:      os.Stdout,
		closeFile: false, // Don't close stdout
	}
}

// NewStderrWriter creates a writer for stderr.
// The underlying file (os.Stderr) is not closed on Close().
func NewStderrWriter() *DirectWriter {
	return &DirectWriter{
		file:      os.Stderr,
		closeFile: false, // Don't close stderr
	}
}

// NewFileWriter creates a writer for a new file at the given path.
// The file is created with default permissions (0644).
// The file will be closed when Close() is called.
func NewFileWriter(path string) (*DirectWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &DirectWriter{
		file:      f,
		closeFile: true,
	}, nil
}

// Write writes p to the underlying file.
// This is a direct syscall with no buffering or locking.
func (w *DirectWriter) Write(p []byte) (int, error) {
	return w.file.Write(p)
}

// Close closes the underlying file if closeFile is true.
// For stdout/stderr writers, this is a no-op.
func (w *DirectWriter) Close() error {
	if w.closeFile && w.file != nil {
		return w.file.Close()
	}
	return nil
}

// Fd returns the file descriptor of the underlying file.
// This is useful for the io_uring upgrade path (Phase 2).
// Returns -1 if the file is nil.
func (w *DirectWriter) Fd() int {
	if w.file == nil {
		return -1
	}
	return int(w.file.Fd())
}

// Ensure DirectWriter implements io.WriteCloser
var _ io.WriteCloser = (*DirectWriter)(nil)
var _ io.WriteCloser = (*NullWriter)(nil)
