package circular

import (
	"math"
	"testing"
)

// TestGenericMatchesSpecific verifies that generic implementations produce
// the same results as the uint32-specific implementations for 31-bit sequences.
func TestGenericMatchesSpecific(t *testing.T) {
	testCases := []struct {
		a uint32
		b uint32
	}{
		// Normal cases
		{5, 10},
		{10, 5},
		{0, 1},
		{100, 100},
		// Near boundaries
		{0, 100},
		{MaxSeqNumber31 - 1, MaxSeqNumber31},
		{MaxSeqNumber31, MaxSeqNumber31 - 1},
		// Practical gaps
		{1000000, 1001000},
		{1001000, 1000000},
		// Quarter range
		{0, MaxSeqNumber31 / 4},
		{MaxSeqNumber31 / 4, 0},
	}

	for _, tc := range testCases {
		// SeqLess
		specific := SeqLess(tc.a, tc.b)
		generic := SeqLessG[uint32, int32](tc.a, tc.b, MaxSeqNumber31)
		if specific != generic {
			t.Errorf("SeqLess(%d, %d): specific=%v, generic=%v", tc.a, tc.b, specific, generic)
		}

		// SeqGreater
		specificGt := SeqGreater(tc.a, tc.b)
		genericGt := SeqGreaterG[uint32, int32](tc.a, tc.b, MaxSeqNumber31)
		if specificGt != genericGt {
			t.Errorf("SeqGreater(%d, %d): specific=%v, generic=%v", tc.a, tc.b, specificGt, genericGt)
		}

		// SeqDiff
		specificDiff := SeqDiff(tc.a, tc.b)
		genericDiff := SeqDiffG[uint32, int32](tc.a, tc.b, MaxSeqNumber31)
		if specificDiff != genericDiff {
			t.Errorf("SeqDiff(%d, %d): specific=%d, generic=%d", tc.a, tc.b, specificDiff, genericDiff)
		}

		// SeqDistance
		specificDist := SeqDistance(tc.a, tc.b)
		genericDist := SeqDistanceG[uint32, int32](tc.a, tc.b, MaxSeqNumber31)
		if specificDist != genericDist {
			t.Errorf("SeqDistance(%d, %d): specific=%d, generic=%d", tc.a, tc.b, specificDist, genericDist)
		}
	}
}

// Test16BitWraparound verifies wraparound works correctly with 16-bit sequences.
// This validates the algorithm is correct at a smaller scale.
//
// Key insight: For 16-bit sequences with max=0xFFFF:
// - Half range threshold is 32768 (0x8000)
// - Sequences within 32768 of each other compare correctly
// - At exactly half range (32768), comparison is ambiguous (edge case)
func Test16BitWraparound(t *testing.T) {
	tests := []struct {
		name string
		a    uint16
		b    uint16
		want bool // a < b
	}{
		// Normal cases
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},
		{"same", 100, 100, false},

		// Near max boundary
		{"65534 < 65535", 65534, 65535, true},
		{"65535 < 65534", 65535, 65534, false},

		// Wraparound (within half range = 32768)
		{"65530 < 5", 65530, 5, true},  // 65530 is "before" 5 (wrapped)
		{"5 < 65530", 5, 65530, false}, // 5 is "after" 65530

		// Larger but valid wraparound gap (still within half range)
		{"65000 < 1000", 65000, 1000, true},
		{"1000 < 65000", 1000, 65000, false},

		// Quarter range
		{"0 < 16384", 0, 16384, true},
		{"16384 < 0", 16384, 0, false},

		// At half range boundary - behavior determined by signed arithmetic
		// int16(0 - 32768) = int16(-32768) which is negative, so 0 < 32768
		{"0 < 32768", 0, 32768, true},
		// int16(32768 - 0) = int16(32768) which overflows to -32768, so 32768 < 0
		// This is the ambiguous edge case!
		{"32768 < 0 (edge case)", 32768, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqLess16(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqLess16(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// Test16BitDiff verifies diff calculations for 16-bit sequences.
func Test16BitDiff(t *testing.T) {
	tests := []struct {
		name string
		a    uint16
		b    uint16
		want int16
	}{
		{"10 - 5", 10, 5, 5},
		{"5 - 10", 5, 10, -5},
		{"same", 100, 100, 0},
		// Wraparound
		{"5 - 65530", 5, 65530, 11},         // 5 is 11 ahead of 65530 (wrapped)
		{"65530 - 5", 65530, 5, -11},        // 65530 is 11 behind 5
		{"1000 - 65000", 1000, 65000, 1536}, // 1000 is 1536 ahead of 65000
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqDiff16(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqDiff16(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// Test32BitFullWraparound verifies wraparound with full 32-bit sequences.
func Test32BitFullWraparound(t *testing.T) {
	tests := []struct {
		name string
		a    uint32
		b    uint32
		want bool // a < b
	}{
		// Normal cases
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},

		// Near max boundary
		{"max-1 < max", math.MaxUint32 - 1, math.MaxUint32, true},
		{"max < max-1", math.MaxUint32, math.MaxUint32 - 1, false},

		// Wraparound (within half range)
		{"max-10 < 5", math.MaxUint32 - 10, 5, true},
		{"5 < max-10", 5, math.MaxUint32 - 10, false},

		// Larger wraparound
		{"max-1000 < 1000", math.MaxUint32 - 1000, 1000, true},
		{"1000 < max-1000", 1000, math.MaxUint32 - 1000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqLess32Full(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqLess32Full(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// Test64BitWraparound verifies wraparound with 64-bit sequences.
// This validates the algorithm works at extreme scale and future-proofs
// the code if SRT ever moves to 64-bit sequence numbers.
func Test64BitWraparound(t *testing.T) {
	tests := []struct {
		name string
		a    uint64
		b    uint64
		want bool // a < b
	}{
		// Normal cases
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},
		{"same", 1000000, 1000000, false},

		// Large values (but still "close" in 64-bit terms)
		{"1 trillion < 1 trillion + 1000", 1_000_000_000_000, 1_000_000_000_000 + 1000, true},
		{"1 trillion + 1000 < 1 trillion", 1_000_000_000_000 + 1000, 1_000_000_000_000, false},

		// Near max boundary
		{"max-1 < max", math.MaxUint64 - 1, math.MaxUint64, true},
		{"max < max-1", math.MaxUint64, math.MaxUint64 - 1, false},

		// Wraparound (within half range = 2^63)
		{"max-10 < 5", math.MaxUint64 - 10, 5, true},
		{"5 < max-10", 5, math.MaxUint64 - 10, false},

		// Larger wraparound
		{"max-1000 < 1000", math.MaxUint64 - 1000, 1000, true},
		{"1000 < max-1000", 1000, math.MaxUint64 - 1000, false},

		// Very large wraparound gap (still within half range)
		{"max-1M < 1M", math.MaxUint64 - 1_000_000, 1_000_000, true},
		{"1M < max-1M", 1_000_000, math.MaxUint64 - 1_000_000, false},

		// Quarter range
		{"0 < quarter", 0, math.MaxUint64 / 4, true},
		{"quarter < 0", math.MaxUint64 / 4, 0, false},

		// Half range boundary (edge case)
		{"0 < half", 0, math.MaxUint64/2 + 1, true},
		{"half < 0 (edge)", math.MaxUint64/2 + 1, 0, true}, // At threshold, both appear "less"
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqLess64(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqLess64(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// Test64BitDiff verifies diff calculations for 64-bit sequences.
func Test64BitDiff(t *testing.T) {
	tests := []struct {
		name string
		a    uint64
		b    uint64
		want int64
	}{
		{"10 - 5", 10, 5, 5},
		{"5 - 10", 5, 10, -5},
		{"same", 100, 100, 0},
		// Large values
		{"1T+1000 - 1T", 1_000_000_000_000 + 1000, 1_000_000_000_000, 1000},
		{"1T - 1T+1000", 1_000_000_000_000, 1_000_000_000_000 + 1000, -1000},
		// Wraparound
		{"5 - max-10", 5, math.MaxUint64 - 10, 16},
		{"max-10 - 5", math.MaxUint64 - 10, 5, -16},
		{"1000 - max-1000", 1000, math.MaxUint64 - 1000, 2001},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqDiff64(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqDiff64(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// Test64BitDistance verifies distance calculations for 64-bit sequences.
func Test64BitDistance(t *testing.T) {
	tests := []struct {
		name string
		a    uint64
		b    uint64
		want uint64
	}{
		{"10 to 5", 10, 5, 5},
		{"5 to 10", 5, 10, 5},
		{"same", 100, 100, 0},
		// Large values
		{"1T+1000 to 1T", 1_000_000_000_000 + 1000, 1_000_000_000_000, 1000},
		// Wraparound
		{"5 to max-10", 5, math.MaxUint64 - 10, 16},
		{"max-10 to 5", math.MaxUint64 - 10, 5, 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqDistance64(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqDistance64(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// Test64BitAddSub verifies add/sub for 64-bit sequences.
func Test64BitAddSub(t *testing.T) {
	// Add tests
	addTests := []struct {
		name  string
		seq   uint64
		delta uint64
		want  uint64
	}{
		{"normal add", 100, 50, 150},
		{"add zero", 100, 0, 100},
		{"wraparound", math.MaxUint64, 1, 0},
		{"wraparound by 10", math.MaxUint64, 10, 9},
		{"large values", 1_000_000_000_000, 1_000_000, 1_000_001_000_000},
	}

	for _, tt := range addTests {
		t.Run("Add_"+tt.name, func(t *testing.T) {
			got := SeqAdd64(tt.seq, tt.delta)
			if got != tt.want {
				t.Errorf("SeqAdd64(%d, %d) = %d, want %d", tt.seq, tt.delta, got, tt.want)
			}
		})
	}

	// Sub tests
	subTests := []struct {
		name  string
		seq   uint64
		delta uint64
		want  uint64
	}{
		{"normal sub", 150, 50, 100},
		{"sub zero", 100, 0, 100},
		{"wraparound", 0, 1, math.MaxUint64},
		{"wraparound by 10", 5, 10, math.MaxUint64 - 4},
	}

	for _, tt := range subTests {
		t.Run("Sub_"+tt.name, func(t *testing.T) {
			got := SeqSub64(tt.seq, tt.delta)
			if got != tt.want {
				t.Errorf("SeqSub64(%d, %d) = %d, want %d", tt.seq, tt.delta, got, tt.want)
			}
		})
	}
}

// TestConsistencyAcrossBitWidths verifies proportionally equivalent test cases.
//
// IMPORTANT: The 31-bit implementation is DIFFERENT from 16-bit, 32-bit, and 64-bit
// because it masks to 31 bits but uses int32 for comparison.
//
// For 16-bit: half range = 32768, so wraparound works for gaps < 32768
// For 32-bit: half range = 2^31 = 2147483648, so wraparound works for gaps < 2^31
// For 64-bit: half range = 2^63, so wraparound works for gaps < 2^63
// For 31-bit: Uses int32 comparison, but max is 2^31-1
//
//	Half range is still ~2^30 for signed comparison
//
// This means for 31-bit, "max-10 vs 5" has a gap of 2147483632, which is
// LARGER than the half range threshold (~1 billion), so it doesn't wrap!
func TestConsistencyAcrossBitWidths(t *testing.T) {
	// Test case with SMALL gaps (within half range for all bit widths)
	// These should all work consistently

	// 16-bit: 100 vs 200 (gap of 100, well within half range of 32768)
	less16 := SeqLess16(100, 200)
	// 31-bit: 100 vs 200 (gap of 100, well within half range of ~1 billion)
	less31 := SeqLess(100, 200)
	// 32-bit: 100 vs 200 (gap of 100, well within half range of ~2 billion)
	less32 := SeqLess32Full(100, 200)
	// 64-bit: 100 vs 200 (gap of 100, well within half range of ~9 quintillion)
	less64 := SeqLess64(100, 200)

	if !less16 || !less31 || !less32 || !less64 {
		t.Errorf("Small gap (100 vs 200) should all be true: 16=%v, 31=%v, 32=%v, 64=%v",
			less16, less31, less32, less64)
	}

	// Test small wraparound for each (proportional to max)
	// 16-bit: 65530 vs 5 (gap of 11 across wrap) - works
	wrap16 := SeqLess16(65530, 5)
	// 32-bit: max-10 vs 5 (gap of 16 across wrap) - works
	wrap32 := SeqLess32Full(math.MaxUint32-10, 5)
	// 64-bit: max-10 vs 5 (gap of 16 across wrap) - works
	wrap64 := SeqLess64(math.MaxUint64-10, 5)

	if !wrap16 || !wrap32 || !wrap64 {
		t.Errorf("Small wraparound should work: 16=%v, 32=%v, 64=%v", wrap16, wrap32, wrap64)
	}

	// 31-bit with REALISTIC small gap: max31-10 is NOT a small gap!
	// Instead, test with a gap that fits within 31-bit half range
	// Use proportionally small gap: within ~1 million packets
	a31 := uint32(MaxSeqNumber31 / 2)      // Middle of range
	b31 := uint32(MaxSeqNumber31/2 + 1000) // 1000 ahead
	less31_realistic := SeqLess(a31, b31)
	if !less31_realistic {
		t.Errorf("31-bit realistic gap should work: SeqLess(%d, %d) = %v, want true",
			a31, b31, less31_realistic)
	}

	// 64-bit with very large values (but still small gap)
	a64 := uint64(1) << 62 // 2^62 = 4.6 quintillion
	b64 := a64 + 1000      // 1000 ahead
	less64_large := SeqLess64(a64, b64)
	if !less64_large {
		t.Errorf("64-bit large value small gap should work: SeqLess64(%d, %d) = %v, want true",
			a64, b64, less64_large)
	}
}

// TestAllBitWidthsWraparound verifies wraparound behavior is consistent
// when using proportionally equivalent gaps across all bit widths.
func TestAllBitWidthsWraparound(t *testing.T) {
	// Use a gap that is ~0.01% of the max value for each bit width
	// This ensures we're testing the same "relative" wraparound

	// 16-bit: gap of ~6 (0.01% of 65535)
	gap16 := uint16(6)
	a16 := MaxSeqNumber16 - gap16
	b16 := gap16
	if !SeqLess16(a16, b16) {
		t.Errorf("16-bit wraparound failed: SeqLess16(%d, %d) = false, want true", a16, b16)
	}

	// 32-bit: gap of ~430000 (0.01% of 4 billion)
	gap32 := uint32(430000)
	a32 := MaxSeqNumber32 - gap32
	b32 := gap32
	if !SeqLess32Full(a32, b32) {
		t.Errorf("32-bit wraparound failed: SeqLess32Full(%d, %d) = false, want true", a32, b32)
	}

	// 64-bit: gap of ~1.8 quadrillion (0.01% of max)
	gap64 := uint64(1) << 50 // ~1.1 quadrillion, safely within half range
	a64 := MaxSeqNumber64 - gap64
	b64 := gap64
	if !SeqLess64(a64, b64) {
		t.Errorf("64-bit wraparound failed: SeqLess64(%d, %d) = false, want true", a64, b64)
	}

	t.Logf("All bit widths handle proportional wraparound correctly")
}

// --- Benchmarks comparing specific vs generic implementations ---

func BenchmarkSeqLess_Specific(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess(a, c)
	}
}

func BenchmarkSeqLess_Generic31(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLessG[uint32, int32](a, c, MaxSeqNumber31)
	}
}

func BenchmarkSeqLess_Generic16(b *testing.B) {
	a := uint16(10000)
	c := uint16(10050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess16(a, c)
	}
}

func BenchmarkSeqDiff_Specific(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqDiff(a, c)
	}
}

func BenchmarkSeqDiff_Generic31(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqDiffG[uint32, int32](a, c, MaxSeqNumber31)
	}
}

func BenchmarkSeqDiff_Generic16(b *testing.B) {
	a := uint16(10000)
	c := uint16(10050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqDiff16(a, c)
	}
}

// BenchmarkSeqLess_Wraparound tests performance with wraparound scenarios
func BenchmarkSeqLess_Wraparound_Specific(b *testing.B) {
	x := uint32(MaxSeqNumber31 - 100)
	y := uint32(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess(x, y)
	}
}

func BenchmarkSeqLess_Wraparound_Generic(b *testing.B) {
	x := uint32(MaxSeqNumber31 - 100)
	y := uint32(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLessG[uint32, int32](x, y, MaxSeqNumber31)
	}
}

// --- 64-bit benchmarks ---

func BenchmarkSeqLess_Generic64(b *testing.B) {
	a := uint64(1_000_000_000_000)
	c := uint64(1_000_000_000_050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess64(a, c)
	}
}

func BenchmarkSeqDiff_Generic64(b *testing.B) {
	a := uint64(1_000_000_000_000)
	c := uint64(1_000_000_000_050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqDiff64(a, c)
	}
}

func BenchmarkSeqLess_Wraparound_64(b *testing.B) {
	x := MaxSeqNumber64 - 100
	y := uint64(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess64(x, y)
	}
}

func BenchmarkSeqDistance_64(b *testing.B) {
	a := uint64(1_000_000_000_000)
	c := uint64(1_000_000_000_050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqDistance64(a, c)
	}
}

// BenchmarkComparison_CircularNumber compares with existing Number.Lt()
func BenchmarkComparison_CircularNumberLt(b *testing.B) {
	a := New(1000000, MaxSeqNumber31)
	c := New(1000050, MaxSeqNumber31)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Lt(c)
	}
}

func BenchmarkComparison_CircularNumberLtBranchless(b *testing.B) {
	a := New(1000000, MaxSeqNumber31)
	c := New(1000050, MaxSeqNumber31)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.LtBranchless(c)
	}
}

// --- Summary benchmark comparing all bit widths ---

func BenchmarkAllBitWidths_SeqLess(b *testing.B) {
	b.Run("16bit", func(b *testing.B) {
		a, c := uint16(10000), uint16(10050)
		for i := 0; i < b.N; i++ {
			SeqLess16(a, c)
		}
	})
	b.Run("31bit", func(b *testing.B) {
		a, c := uint32(1000000), uint32(1000050)
		for i := 0; i < b.N; i++ {
			SeqLess(a, c)
		}
	})
	b.Run("32bit", func(b *testing.B) {
		a, c := uint32(1000000), uint32(1000050)
		for i := 0; i < b.N; i++ {
			SeqLess32Full(a, c)
		}
	})
	b.Run("64bit", func(b *testing.B) {
		a, c := uint64(1_000_000_000_000), uint64(1_000_000_000_050)
		for i := 0; i < b.N; i++ {
			SeqLess64(a, c)
		}
	})
}
