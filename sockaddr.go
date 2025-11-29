package srt

import (
	"net"
	"syscall"
	"unsafe"
)

// convertUDPAddrToSockaddr converts a net.Addr (UDPAddr) to a syscall.RawSockaddrAny structure.
// This function uses direct struct assignment via unsafe.Pointer, following
// the cleaner, more efficient pattern used by the Go standard library's syscall package.
// Returns the length of the sockaddr structure (SizeofSockaddrInet4 or SizeofSockaddrInet6).
// The converted sockaddr is stored in the provided out parameter.
func convertUDPAddrToSockaddr(addr net.Addr, out *syscall.RawSockaddrAny) uint32 {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0
	}

	// Standard Go syscall code uses BigEndian (network byte order) for port and IP bytes.
	// The syscall structs handle any necessary host/network byte order conversions internally
	// for the Family field, but we must manually handle Port (BigEndian) and IP.

	if ip4 := udpAddr.IP.To4(); ip4 != nil {
		// IPv4
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(out))

		// 1. Family: Set the address family. This is usually set in Host Byte Order by the OS.
		sa.Family = syscall.AF_INET

		// 2. Port: Set the port in Network Byte Order (Big Endian).
		// Go's standard library often uses unsafe tricks like this for efficiency:
		portBytes := (*[2]byte)(unsafe.Pointer(&sa.Port))
		port := uint16(udpAddr.Port)
		portBytes[0] = byte(port >> 8) // High byte
		portBytes[1] = byte(port)      // Low byte

		// 3. Address: Copy the 4-byte IPv4 address.
		copy(sa.Addr[:], ip4)

		return syscall.SizeofSockaddrInet4

	} else if ip6 := udpAddr.IP.To16(); ip6 != nil {
		// IPv6
		sa := (*syscall.RawSockaddrInet6)(unsafe.Pointer(out))

		// 1. Family
		sa.Family = syscall.AF_INET6

		// 2. Port (Network Byte Order)
		portBytes := (*[2]byte)(unsafe.Pointer(&sa.Port))
		port := uint16(udpAddr.Port)
		portBytes[0] = byte(port >> 8)
		portBytes[1] = byte(port)

		// 3. Flowinfo (set to zero)
		sa.Flowinfo = 0

		// 4. Address: Copy the 16-byte IPv6 address.
		copy(sa.Addr[:], ip6)

		// 5. Scope_id (set to zero for basic UDP, unless handling scoped addresses)
		sa.Scope_id = 0

		return syscall.SizeofSockaddrInet6
	}

	return 0
}

