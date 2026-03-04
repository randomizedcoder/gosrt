//go:build !windows

package srt

import (
	"net"
	"syscall"
)

// getUDPConnFD extracts the file descriptor from a net.UDPConn.
// It duplicates the FD so the caller owns it and can use it independently.
// The returned FD should be closed when no longer needed (though in practice,
// it will be closed when the connection is closed).
func getUDPConnFD(pc *net.UDPConn) (int, error) {
	file, err := pc.File()
	if err != nil {
		return -1, err
	}
	// Close error ignored: this releases Go's file handle reference, not the
	// underlying socket FD (which we duplicated). Errors are not actionable.
	defer func() { _ = file.Close() }()

	fd := int(file.Fd())
	// Duplicate the FD so we own it
	dupFd, err := syscall.Dup(fd)
	if err != nil {
		return -1, err
	}

	return dupFd, nil
}

func ListenControl(config Config) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var opErr error
		err := c.Control(func(fd uintptr) {
			// Set REUSEADDR
			opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			if opErr != nil {
				return
			}

			// Set TOS
			if config.IPTOS > 0 {
				opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, config.IPTOS)
				if opErr != nil {
					return
				}
			}

			// Set TTL
			if config.IPTTL > 0 {
				opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, config.IPTTL)
				if opErr != nil {
					return
				}
			}
		})
		if err != nil {
			return err
		}
		return opErr
	}
}

func DialControl(config Config) func(network string, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var opErr error
		err := c.Control(func(fd uintptr) {
			// Set TOS
			if config.IPTOS > 0 {
				opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, config.IPTOS)
				if opErr != nil {
					return
				}
			}

			// Set TTL
			if config.IPTTL > 0 {
				opErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, config.IPTTL)
				if opErr != nil {
					return
				}
			}
		})
		if err != nil {
			return err
		}
		return opErr
	}
}
