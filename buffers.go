// buffers.go - Shared buffer pools for zero-copy packet handling
//
// This file contains THE most performance-critical memory in gosrt.
// The globalRecvBufferPool is shared across ALL listeners and dialers
// for maximum memory reuse and minimal allocation overhead.

package srt

import "sync"

// DefaultRecvBufferSize is the standard MTU size for Ethernet.
// All receive buffers are this size to ensure they can hold any valid SRT packet.
const DefaultRecvBufferSize = 1500

// globalRecvBufferPool is the shared pool for all receive buffers.
// This single pool serves ALL listeners and dialers in the process,
// enabling maximum buffer reuse across connections.
//
// Design rationale:
//   - Single pool = maximum sharing between all connections
//   - Fixed 1500-byte size = standard Ethernet MTU, fits all SRT packets
//   - Buffers flow freely between listeners, dialers, and connections
//   - Reduces GC pressure by reusing allocations
//
// Usage:
//
//	bufPtr := GetRecvBufferPool().Get().(*[]byte)
//	n, addr, _ := conn.ReadFrom(*bufPtr)
//	// ... use buffer ...
//	GetRecvBufferPool().Put(bufPtr)
var globalRecvBufferPool = &sync.Pool{
	New: func() any {
		buf := make([]byte, DefaultRecvBufferSize)
		return &buf
	},
}

// GetRecvBufferPool returns the shared receive buffer pool.
// All listeners and dialers should use this single pool for receive operations.
//
// The returned pool contains *[]byte pointers to 1500-byte buffers.
func GetRecvBufferPool() *sync.Pool {
	return globalRecvBufferPool
}
