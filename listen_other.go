//go:build !linux
// +build !linux

package srt

// initializeIoUringRecv is a no-op on non-Linux platforms
func (ln *listener) initializeIoUringRecv() error {
	return nil // io_uring not available on this platform
}

// cleanupIoUringRecv is a no-op on non-Linux platforms
func (ln *listener) cleanupIoUringRecv() {
	// Nothing to clean up
}
