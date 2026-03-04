// Package circular - seq_math.go provides sequence number math for uint32 values.
//
// These functions handle SRT's 31-bit sequence number wraparound using threshold-based
// comparison. They are designed for use with Google btree which requires raw
// uint32 values rather than the Number type.
//
// IMPORTANT: SRT uses 31-bit sequence numbers (per RFC Section 3.1), NOT 32-bit.
// The signed arithmetic approach used in RTP (16-bit) doesn't work for 31-bit because
// int32(MaxSeqNumber31 - 0) = int32(2147483647) doesn't overflow (stays positive).
//
// Instead, we use threshold-based comparison: if the distance between two sequence
// numbers is greater than half the sequence space, we invert the comparison result
// to correctly handle wraparound.
//
// Reference: documentation/receiver_stream_tests_design.md Section 12 (Wraparound Bug Analysis)
package circular

// MaxSeqNumber31 is the maximum 31-bit SRT sequence number.
// SRT uses 31-bit sequence numbers (bit 0 is reserved for the data/control flag).
// Per SRT RFC Section 3.1: "Packet Sequence Number: 31 bits."
const MaxSeqNumber31 = 0x7FFFFFFF // 2^31 - 1 = 2147483647

// seqThreshold31 is half the 31-bit sequence space, used for wraparound detection.
// If the distance between two sequence numbers exceeds this, we're in wraparound.
const seqThreshold31 = MaxSeqNumber31 / 2 // ~1.07 billion

// SeqLess returns true if a < b, handling 31-bit sequence wraparound.
//
// Uses threshold-based comparison: if the distance between a and b is greater than
// half the sequence space (seqThreshold31), the result is inverted to handle wraparound.
//
// Why not signed arithmetic? For 31-bit sequences, int32(MAX - 0) = 2147483647 (positive),
// which doesn't overflow like it would for 16-bit or 32-bit sequences.
//
// Examples:
//   - SeqLess(5, 10) = true        (normal: 5 < 10)
//   - SeqLess(10, 5) = false       (normal: 10 > 5)
//   - SeqLess(MaxSeqNumber31, 0) = true   (wraparound: max is "before" 0)
//   - SeqLess(0, MaxSeqNumber31) = false  (wraparound: 0 is "after" max)
//   - SeqLess(MaxSeqNumber31-10, 5) = true (wraparound: MAX-10 is "before" 5)
func SeqLess(a, b uint32) bool {
	// Mask to 31 bits to ensure we're in SRT sequence space
	a &= MaxSeqNumber31
	b &= MaxSeqNumber31

	if a == b {
		return false
	}

	// Calculate distance and raw comparison
	var d uint32
	aLessRaw := a < b
	if aLessRaw {
		d = b - a
	} else {
		d = a - b
	}

	// If distance is within threshold (inclusive), use raw comparison
	// If distance exceeds threshold, we're in wraparound - invert result
	// Note: At exactly half-range, we use raw comparison (consistent with existing tests)
	if d <= seqThreshold31 {
		return aLessRaw
	}
	return !aLessRaw
}

// SeqGreater returns true if a > b, handling 31-bit sequence wraparound.
func SeqGreater(a, b uint32) bool {
	a &= MaxSeqNumber31
	b &= MaxSeqNumber31

	if a == b {
		return false
	}

	var d uint32
	aGreaterRaw := a > b
	if aGreaterRaw {
		d = a - b
	} else {
		d = b - a
	}

	// Note: At exactly half-range, we use raw comparison (consistent with existing tests)
	if d <= seqThreshold31 {
		return aGreaterRaw
	}
	return !aGreaterRaw
}

// SeqLessOrEqual returns true if a <= b, handling 31-bit sequence wraparound.
func SeqLessOrEqual(a, b uint32) bool {
	return !SeqGreater(a, b)
}

// SeqGreaterOrEqual returns true if a >= b, handling 31-bit sequence wraparound.
func SeqGreaterOrEqual(a, b uint32) bool {
	return !SeqLess(a, b)
}

// SeqDiff returns the signed difference (a - b), handling 31-bit wraparound.
// Positive if a > b (in circular terms), negative if a < b, zero if a == b.
//
// The result represents the shortest path from b to a in the circular sequence space.
// This correctly handles wraparound: when a=10 and b=MaxSeqNumber31, the result
// is positive (~10, not negative ~-2 billion) because 10 is "after" MAX.
//
// Uses threshold-based comparison (same as SeqLess) rather than int32(a-b) which
// fails for 31-bit sequences because int32(MAX - 0) doesn't overflow.
//
// Examples:
//   - SeqDiff(10, 5) = 5          (normal: 10 is 5 after 5)
//   - SeqDiff(5, 10) = -5         (normal: 5 is 5 before 10)
//   - SeqDiff(0, MAX) = 1         (wraparound: 0 is 1 after MAX)
//   - SeqDiff(MAX, 0) = -1        (wraparound: MAX is 1 before 0)
//   - SeqDiff(50, MAX-100) = 151  (wraparound: 50 is 151 after MAX-100)
func SeqDiff(a, b uint32) int32 {
	a &= MaxSeqNumber31
	b &= MaxSeqNumber31

	if a == b {
		return 0
	}

	// Calculate unsigned distance
	var d uint32
	aGreaterRaw := a > b
	if aGreaterRaw {
		d = a - b
	} else {
		d = b - a
	}

	// If distance exceeds threshold, we're in wraparound territory
	// The "true" distance is the complement: MaxSeqNumber31 + 1 - d
	if d > seqThreshold31 {
		// Wraparound case: invert the sign
		complement := MaxSeqNumber31 + 1 - d
		if aGreaterRaw {
			// Raw says a > b, but wraparound means a is actually BEFORE b
			return -int32(complement)
		}
		// Raw says a < b, but wraparound means a is actually AFTER b
		return int32(complement)
	}

	// Normal case: use raw comparison
	if aGreaterRaw {
		return int32(d)
	}
	return -int32(d)
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
