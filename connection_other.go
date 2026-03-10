//go:build !linux
// +build !linux

package srt

import (
	"context"

	"github.com/randomizedcoder/gosrt/packet"
)

// sendRingState is a stub type for non-Linux platforms
// The actual type is defined in connection_linux.go
type sendRingState struct{}

// initializeIoUring is a stub for non-Linux platforms
func (c *srtConn) initializeIoUring(config srtConnConfig) {
	// io_uring is only available on Linux
}

// cleanupIoUring is a stub for non-Linux platforms
func (c *srtConn) cleanupIoUring() {
	// io_uring is only available on Linux
}

// sendIoUring is a stub for non-Linux platforms
func (c *srtConn) sendIoUring(p packet.Packet) {
	// io_uring is only available on Linux
	// This should never be called if io_uring is disabled
	p.Decommission()
}

// sendCompletionHandler is a stub for non-Linux platforms
func (c *srtConn) sendCompletionHandler(ctx context.Context) {
	// io_uring is only available on Linux
}
