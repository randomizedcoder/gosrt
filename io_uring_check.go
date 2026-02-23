//go:build linux
// +build linux

package srt

import (
	"fmt"

	"github.com/randomizedcoder/giouring"
)

// IoUringAvailable checks if io_uring is available on the system.
// Returns true if the kernel version is >= 5.1 (minimum required for io_uring).
// Returns an error if kernel version cannot be determined.
func IoUringAvailable() (bool, error) {
	// Check kernel version (io_uring requires Linux 5.1+)
	available, err := giouring.CheckKernelVersion(5, 1, 0)
	if err != nil {
		return false, fmt.Errorf("failed to check kernel version: %w", err)
	}

	if !available {
		return false, nil
	}

	// Try to create a test ring to verify io_uring actually works
	// This catches cases where kernel version is sufficient but io_uring
	// is disabled or unavailable for other reasons
	testRing := giouring.NewRing()
	err = testRing.QueueInit(8, 0) // Small test ring
	if err != nil {
		// Kernel version is sufficient but io_uring doesn't work
		// (might be disabled, or insufficient permissions, etc.)
		return false, nil
	}

	// Clean up test ring
	testRing.QueueExit()

	return true, nil
}

// IoUringKernelVersion returns the current kernel version information.
// Returns an error if kernel version cannot be determined.
func IoUringKernelVersion() (*giouring.KernelVersion, error) {
	return giouring.GetKernelVersion()
}
