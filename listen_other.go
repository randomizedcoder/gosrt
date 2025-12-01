//go:build !linux
// +build !linux

package srt

// recvCompletionInfo is a stub type for non-Linux platforms
// The actual type is defined in listen_linux.go
type recvCompletionInfo struct{}

// initializeIoUringRecv is a no-op on non-Linux platforms
func (ln *listener) initializeIoUringRecv() error {
	return nil // io_uring not available on this platform
}

// cleanupIoUringRecv is a no-op on non-Linux platforms
func (ln *listener) cleanupIoUringRecv() {
	// Nothing to clean up
}
