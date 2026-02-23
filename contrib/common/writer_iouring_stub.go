//go:build !linux
// +build !linux

// Package common provides shared utilities for contrib programs.
//
// This file provides stub implementations for non-Linux platforms
// where io_uring is not available.
package common

import (
	"fmt"
	"io"
)

// IoUringWriter is not available on non-Linux platforms.
// Use DirectWriter instead.
type IoUringWriter struct{}

// NewIoUringWriter returns an error on non-Linux platforms.
func NewIoUringWriter(fd int, ringSize uint32) (*IoUringWriter, error) {
	return nil, fmt.Errorf("io_uring is only available on Linux")
}

// NewIoUringStdoutWriter returns an error on non-Linux platforms.
func NewIoUringStdoutWriter() (*IoUringWriter, error) {
	return nil, fmt.Errorf("io_uring is only available on Linux")
}

// NewIoUringFileWriter returns an error on non-Linux platforms.
func NewIoUringFileWriter(fd int) (*IoUringWriter, error) {
	return nil, fmt.Errorf("io_uring is only available on Linux")
}

// Write is not implemented on non-Linux platforms.
func (w *IoUringWriter) Write(p []byte) (int, error) {
	return 0, fmt.Errorf("io_uring is only available on Linux")
}

// Close is not implemented on non-Linux platforms.
func (w *IoUringWriter) Close() error {
	return nil
}

// IoUringOutputAvailable returns false on non-Linux platforms.
func IoUringOutputAvailable() bool {
	return false
}

// Ensure IoUringWriter implements io.WriteCloser
var _ io.WriteCloser = (*IoUringWriter)(nil)

