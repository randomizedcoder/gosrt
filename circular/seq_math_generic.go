// Package circular - seq_math_generic.go provides generic sequence number math.
//
// These generic functions work with any unsigned integer type (uint16, uint32, uint64).
// They use the same signed arithmetic approach as the uint32-specific functions,
// allowing verification that the wraparound logic works correctly across different
// bit widths.
//
// For production use with SRT (31-bit sequences), prefer the uint32-specific
// functions in seq_math.go for optimal performance.
package circular

import "math"

// Unsigned is a constraint for unsigned integer types.
type Unsigned interface {
	~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uint
}

// Signed is a constraint for signed integer types.
type Signed interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~int
}

// SeqLessG returns true if a < b using signed arithmetic wraparound detection.
// Works with any unsigned integer type.
//
// The max parameter specifies the maximum valid sequence number.
// For 16-bit: max = 0xFFFF
// For 31-bit SRT: max = 0x7FFFFFFF
// For 32-bit: max = 0xFFFFFFFF
func SeqLessG[U Unsigned, S Signed](a, b, max U) bool {
	// Mask to valid range
	a = a & max
	b = b & max

	// Convert to signed for wraparound detection
	diff := S(a) - S(b)
	return diff < 0
}

// SeqGreaterG returns true if a > b using signed arithmetic wraparound detection.
func SeqGreaterG[U Unsigned, S Signed](a, b, max U) bool {
	a = a & max
	b = b & max

	diff := S(a) - S(b)
	return diff > 0
}

// SeqLessOrEqualG returns true if a <= b.
func SeqLessOrEqualG[U Unsigned, S Signed](a, b, max U) bool {
	return !SeqGreaterG[U, S](a, b, max)
}

// SeqGreaterOrEqualG returns true if a >= b.
func SeqGreaterOrEqualG[U Unsigned, S Signed](a, b, max U) bool {
	return !SeqLessG[U, S](a, b, max)
}

// SeqDiffG returns the signed difference (a - b).
func SeqDiffG[U Unsigned, S Signed](a, b, max U) S {
	a = a & max
	b = b & max

	return S(a) - S(b)
}

// SeqDistanceG returns the unsigned distance between two sequence numbers.
func SeqDistanceG[U Unsigned, S Signed](a, b, max U) U {
	diff := SeqDiffG[U, S](a, b, max)
	if diff < 0 {
		return U(-diff)
	}
	return U(diff)
}

// SeqAddG adds delta to a sequence number with wraparound.
func SeqAddG[U Unsigned](seq, delta, max U) U {
	return (seq + delta) & max
}

// SeqSubG subtracts delta from a sequence number with wraparound.
func SeqSubG[U Unsigned](seq, delta, max U) U {
	return (seq - delta) & max
}

// --- Convenience wrappers for common bit widths ---

// Max values for sequence number widths using Go standard library constants
const (
	MaxSeqNumber16 = uint16(math.MaxUint16) // 16-bit max (RTP): 65535
	MaxSeqNumber32 = uint32(math.MaxUint32) // Full 32-bit max: 4294967295
	MaxSeqNumber64 = uint64(math.MaxUint64) // Full 64-bit max: 18446744073709551615
)

// --- 16-bit wrappers (RTP-style) ---

// SeqLess16 compares 16-bit sequence numbers (like RTP).
func SeqLess16(a, b uint16) bool {
	return SeqLessG[uint16, int16](a, b, MaxSeqNumber16)
}

// SeqGreater16 returns true if a > b for 16-bit sequences.
func SeqGreater16(a, b uint16) bool {
	return SeqGreaterG[uint16, int16](a, b, MaxSeqNumber16)
}

// SeqDiff16 returns signed difference for 16-bit sequences.
func SeqDiff16(a, b uint16) int16 {
	return SeqDiffG[uint16, int16](a, b, MaxSeqNumber16)
}

// SeqDistance16 returns unsigned distance for 16-bit sequences.
func SeqDistance16(a, b uint16) uint16 {
	return SeqDistanceG[uint16, int16](a, b, MaxSeqNumber16)
}

// --- 32-bit wrappers (full range) ---

// SeqLess32Full compares full 32-bit sequence numbers.
func SeqLess32Full(a, b uint32) bool {
	return SeqLessG[uint32, int32](a, b, MaxSeqNumber32)
}

// SeqGreater32Full returns true if a > b for full 32-bit sequences.
func SeqGreater32Full(a, b uint32) bool {
	return SeqGreaterG[uint32, int32](a, b, MaxSeqNumber32)
}

// SeqDiff32Full returns signed difference for full 32-bit sequences.
func SeqDiff32Full(a, b uint32) int32 {
	return SeqDiffG[uint32, int32](a, b, MaxSeqNumber32)
}

// SeqDistance32Full returns unsigned distance for full 32-bit sequences.
func SeqDistance32Full(a, b uint32) uint32 {
	return SeqDistanceG[uint32, int32](a, b, MaxSeqNumber32)
}

// --- 64-bit wrappers (future-proofing) ---

// SeqLess64 compares 64-bit sequence numbers.
// Useful for high-throughput applications that may exhaust 32-bit sequences.
func SeqLess64(a, b uint64) bool {
	return SeqLessG[uint64, int64](a, b, MaxSeqNumber64)
}

// SeqGreater64 returns true if a > b for 64-bit sequences.
func SeqGreater64(a, b uint64) bool {
	return SeqGreaterG[uint64, int64](a, b, MaxSeqNumber64)
}

// SeqDiff64 returns signed difference for 64-bit sequences.
func SeqDiff64(a, b uint64) int64 {
	return SeqDiffG[uint64, int64](a, b, MaxSeqNumber64)
}

// SeqDistance64 returns unsigned distance for 64-bit sequences.
func SeqDistance64(a, b uint64) uint64 {
	return SeqDistanceG[uint64, int64](a, b, MaxSeqNumber64)
}

// SeqAdd64 adds delta to a 64-bit sequence number with wraparound.
func SeqAdd64(seq, delta uint64) uint64 {
	return SeqAddG(seq, delta, MaxSeqNumber64)
}

// SeqSub64 subtracts delta from a 64-bit sequence number with wraparound.
func SeqSub64(seq, delta uint64) uint64 {
	return SeqSubG(seq, delta, MaxSeqNumber64)
}
