//go:build !linux
// +build !linux

package srt

// dialerRecvRingState is a stub type for non-Linux platforms
// The actual type is defined in dial_linux.go
type dialerRecvRingState struct{}

// initializeIoUringRecv is a no-op on non-Linux platforms
func (dl *dialer) initializeIoUringRecv() error {
	return nil // io_uring not available on this platform
}

// cleanupIoUringRecv is a no-op on non-Linux platforms
func (dl *dialer) cleanupIoUringRecv() {
	// Nothing to clean up
}

// initializeIoUringDialerRecvMetrics is a no-op on non-Linux platforms
func (dl *dialer) initializeIoUringDialerRecvMetrics() {
	// Nothing to initialize
}
