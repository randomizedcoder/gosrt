// Package circular - seq_math.go provides sequence number math for uint32 values.
//
// These functions handle SRT's 31-bit sequence number wraparound using signed
// arithmetic. They are designed for use with Google btree which requires raw
// uint32 values rather than the Number type.
//
// The approach is inspired by goTrackRTP (github.com/randomizedcoder/goTrackRTP)
// but adapted for SRT's 31-bit sequence numbers.
//
// Reference: documentation/trackRTP_math.go.reference
package circular

// MaxSeqNumber31 is the maximum 31-bit SRT sequence number.
// SRT uses 31-bit sequence numbers (bit 31 is reserved for the message flag).
const MaxSeqNumber31 = 0x7FFFFFFF // 2^31 - 1 = 2147483647

// SeqLess returns true if a < b, handling 31-bit sequence wraparound.
//
// Uses signed comparison: treats the difference (a - b) as a signed int32.
// If the result is negative (high bit set), then a < b in circular space.
//
// This works because when sequences are close together (within half the range),
// the signed difference correctly indicates their relative order. When they're
// far apart (indicating wraparound), the sign inverts to give the correct answer.
//
// Examples:
//   - SeqLess(5, 10) = true       (5 - 10 = -5, negative → a < b)
//   - SeqLess(10, 5) = false      (10 - 5 = 5, positive → a > b)
//   - SeqLess(0, MaxSeqNumber31) = false  (wraparound: 0 is "after" max)
//   - SeqLess(MaxSeqNumber31, 0) = true   (wraparound: max is "before" 0)
func SeqLess(a, b uint32) bool {
	// Mask to 31 bits to ensure we're in SRT sequence space
	a = a & MaxSeqNumber31
	b = b & MaxSeqNumber31

	// Signed comparison handles wraparound
	diff := int32(a - b)
	return diff < 0
}

// SeqGreater returns true if a > b, handling 31-bit sequence wraparound.
func SeqGreater(a, b uint32) bool {
	a = a & MaxSeqNumber31
	b = b & MaxSeqNumber31

	diff := int32(a - b)
	return diff > 0
}

// SeqLessOrEqual returns true if a <= b, handling 31-bit sequence wraparound.
func SeqLessOrEqual(a, b uint32) bool {
	return !SeqGreater(a, b)
}

// SeqGreaterOrEqual returns true if a >= b, handling 31-bit sequence wraparound.
func SeqGreaterOrEqual(a, b uint32) bool {
	return !SeqLess(a, b)
}

// SeqDiff returns the signed difference (a - b), handling wraparound.
// Positive if a > b, negative if a < b, zero if a == b.
//
// The result is the shortest distance between the two sequence numbers,
// which may involve wrapping around the sequence space.
func SeqDiff(a, b uint32) int32 {
	a = a & MaxSeqNumber31
	b = b & MaxSeqNumber31

	return int32(a - b)
}

// SeqDistance returns the unsigned distance between two sequence numbers.
// Always returns the shortest distance (never more than MaxSeqNumber31/2).
func SeqDistance(a, b uint32) uint32 {
	diff := SeqDiff(a, b)
	if diff < 0 {
		return uint32(-diff)
	}
	return uint32(diff)
}

// SeqAdd adds delta to a sequence number, handling wraparound.
func SeqAdd(seq uint32, delta uint32) uint32 {
	return (seq + delta) & MaxSeqNumber31
}

// SeqSub subtracts delta from a sequence number, handling wraparound.
func SeqSub(seq uint32, delta uint32) uint32 {
	return (seq - delta) & MaxSeqNumber31
}

// SeqInRange returns true if seq is in the range [start, end], inclusive.
// Handles wraparound correctly.
//
// Examples:
//   - SeqInRange(5, 1, 10) = true
//   - SeqInRange(15, 1, 10) = false
//   - SeqInRange(0, MaxSeqNumber31-5, 5) = true  (wraparound range)
func SeqInRange(seq, start, end uint32) bool {
	// If start <= end, it's a normal range
	// If start > end, it's a wraparound range
	if SeqLessOrEqual(start, end) {
		// Normal range: seq must be >= start AND <= end
		return SeqGreaterOrEqual(seq, start) && SeqLessOrEqual(seq, end)
	}
	// Wraparound range: seq must be >= start OR <= end
	return SeqGreaterOrEqual(seq, start) || SeqLessOrEqual(seq, end)
}
