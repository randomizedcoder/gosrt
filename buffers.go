// buffers.go - Shared buffer pools for zero-copy packet handling
//
// This file contains THE most performance-critical memory in gosrt.
// The globalRecvBufferPool is shared across ALL listeners and dialers
// for maximum memory reuse and minimal allocation overhead.

package srt

import "sync"

// DefaultRecvBufferSize is the standard MTU size for Ethernet.
// All receive buffers are this size to ensure they can hold any valid SRT packet.
// Exported so applications can validate payload sizes before sending.
//
// Reference: lockless_sender_design.md Section 6.2
const DefaultRecvBufferSize = 1500

// MaxPayloadSize is the maximum SRT payload size (MTU - headers).
// SRT header is 16 bytes, UDP header is 8 bytes, IP header is 20 bytes.
// 1500 - 16 - 8 - 20 = 1456 bytes max payload
// However, SRT typically uses 1316 bytes (188 * 7 MPEG-TS packets).
const MaxPayloadSize = 1316

// globalRecvBufferPool is the shared pool for all receive AND send buffers.
// This single pool serves ALL listeners, dialers, and senders in the process,
// enabling maximum buffer reuse across connections.
//
// Design rationale:
//   - Single pool = maximum sharing between all connections
//   - Fixed 1500-byte size = standard Ethernet MTU, fits all SRT packets
//   - Buffers flow freely between listeners, dialers, senders, and connections
//   - Reduces GC pressure by reusing allocations
//   - Sender zero-copy: application acquires buffer, fills it, sends, ACK returns to pool
//
// Usage (receive):
//
//	bufPtr := GetRecvBufferPool().Get().(*[]byte)
//	n, addr, _ := conn.ReadFrom(*bufPtr)
//	// ... use buffer ...
//	GetRecvBufferPool().Put(bufPtr)
//
// Usage (send - zero-copy):
//
//	bufPtr := GetBuffer()
//	copy((*bufPtr)[:payloadLen], data)
//	packet.SetPayload(bufPtr)  // Packet now owns buffer
//	sender.Push(packet)        // Buffer returned to pool when ACK'd
//
// Reference: lockless_sender_design.md Section 6.2
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

// GetBuffer acquires a buffer from the global pool.
// The buffer is 1500 bytes (DefaultRecvBufferSize).
// Caller is responsible for returning it via PutBuffer() or packet.Decommission().
//
// For zero-copy sending:
//  1. Call GetBuffer() to acquire a buffer
//  2. Fill the buffer with payload data (up to MaxPayloadSize bytes)
//  3. Create a packet with the buffer
//  4. Call sender.Push(packet)
//  5. Buffer is automatically returned to pool when ACK'd or dropped
//
// Reference: lockless_sender_design.md Section 6.2
func GetBuffer() *[]byte {
	return globalRecvBufferPool.Get().(*[]byte)
}

// PutBuffer returns a buffer to the global pool.
// Use this only if you acquired a buffer but didn't use it for a packet.
// For packets, use packet.Decommission() instead (handles both packet and buffer).
func PutBuffer(buf *[]byte) {
	if buf == nil {
		return
	}
	globalRecvBufferPool.Put(buf)
}

// ValidatePayloadSize checks if a payload size is valid for pooled buffers.
// Returns true if the size is within bounds, false otherwise.
//
// Use this before filling a buffer to ensure the payload fits:
//
//	if !srt.ValidatePayloadSize(len(data)) {
//	    return fmt.Errorf("payload too large: %d > %d", len(data), srt.MaxPayloadSize)
//	}
func ValidatePayloadSize(size int) bool {
	return size >= 0 && size <= MaxPayloadSize
}

// ValidateBufferSize checks if a size fits in the buffer pool.
// This is more permissive than ValidatePayloadSize - allows up to full MTU.
func ValidateBufferSize(size int) bool {
	return size >= 0 && size <= DefaultRecvBufferSize
}
