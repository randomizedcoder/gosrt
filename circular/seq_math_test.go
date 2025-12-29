package circular

import (
	"testing"
)

func TestSeqLess(t *testing.T) {
	tests := []struct {
		name string
		a    uint32
		b    uint32
		want bool
	}{
		// Normal cases (no wraparound)
		{"5 < 10", 5, 10, true},
		{"10 < 5", 10, 5, false},
		{"0 < 1", 0, 1, true},
		{"1 < 0", 1, 0, false},
		{"same value", 100, 100, false},

		// Edge cases near zero
		{"0 < 100", 0, 100, true},
		{"100 < 0", 100, 0, false},

		// Near wraparound boundary (adjacent sequences)
		{"max-1 < max", MaxSeqNumber31 - 1, MaxSeqNumber31, true},
		{"max < max-1", MaxSeqNumber31, MaxSeqNumber31 - 1, false},

		// Large gaps within valid range (less than half of max)
		{"0 < quarter", 0, MaxSeqNumber31 / 4, true},
		{"quarter < 0", MaxSeqNumber31 / 4, 0, false},

		// Practical SRT buffer sizes (thousands of packets)
		{"1000000 < 1001000", 1000000, 1001000, true},
		{"1001000 < 1000000", 1001000, 1000000, false},

		// At exactly half range boundary
		{"1 < 1+half", 1, 1 + MaxSeqNumber31/2, true},
		{"1+half < 1", 1 + MaxSeqNumber31/2, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqLess(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqLess(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSeqGreater(t *testing.T) {
	tests := []struct {
		name string
		a    uint32
		b    uint32
		want bool
	}{
		{"10 > 5", 10, 5, true},
		{"5 > 10", 5, 10, false},
		{"same value", 100, 100, false},
		// Practical SRT scenarios
		{"1001000 > 1000000", 1001000, 1000000, true},
		{"1000000 > 1001000", 1000000, 1001000, false},
		{"quarter > 0", MaxSeqNumber31 / 4, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqGreater(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqGreater(%d, %d) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSeqDiff(t *testing.T) {
	tests := []struct {
		name string
		a    uint32
		b    uint32
		want int32
	}{
		{"10 - 5", 10, 5, 5},
		{"5 - 10", 5, 10, -5},
		{"same", 100, 100, 0},
		// Practical SRT scenarios
		{"1001000 - 1000000", 1001000, 1000000, 1000},
		{"1000000 - 1001000", 1000000, 1001000, -1000},
		{"quarter - 0", MaxSeqNumber31 / 4, 0, int32(MaxSeqNumber31 / 4)},
		{"0 - quarter", 0, MaxSeqNumber31 / 4, -int32(MaxSeqNumber31 / 4)},

		// =================================================================
		// WRAPAROUND TESTS - These test 31-bit sequence number wraparound
		// =================================================================
		// When MAX wraps to 0, MAX is "before" 0 in circular space
		// So SeqDiff(0, MAX) should be positive (0 is "after" MAX)
		// And SeqDiff(MAX, 0) should be negative (MAX is "before" 0)

		// MAX is "before" small numbers (negative diff)
		{"MAX - 0", MaxSeqNumber31, 0, -1},
		{"MAX - 1", MaxSeqNumber31, 1, -2},
		{"MAX - 50", MaxSeqNumber31, 50, -51},
		{"MAX-10 - 5", MaxSeqNumber31 - 10, 5, -16}, // (MAX-10) to 5 is 16 steps forward

		// Small numbers are "after" MAX (positive diff)
		{"0 - MAX", 0, MaxSeqNumber31, 1},
		{"1 - MAX", 1, MaxSeqNumber31, 2},
		{"50 - MAX", 50, MaxSeqNumber31, 51},
		{"5 - MAX-10", 5, MaxSeqNumber31 - 10, 16}, // 5 is 16 steps after (MAX-10)

		// Symmetric around MAX→0 boundary
		{"MAX-100 - 50", MaxSeqNumber31 - 100, 50, -151},
		{"50 - MAX-100", 50, MaxSeqNumber31 - 100, 151},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqDiff(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqDiff(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSeqDistance(t *testing.T) {
	tests := []struct {
		name string
		a    uint32
		b    uint32
		want uint32
	}{
		{"10 to 5", 10, 5, 5},
		{"5 to 10", 5, 10, 5},
		{"same", 100, 100, 0},
		// Practical SRT scenarios
		{"1001000 to 1000000", 1001000, 1000000, 1000},
		{"1000000 to 1001000", 1000000, 1001000, 1000},
		{"quarter to 0", MaxSeqNumber31 / 4, 0, MaxSeqNumber31 / 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqDistance(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("SeqDistance(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSeqAdd(t *testing.T) {
	tests := []struct {
		name  string
		seq   uint32
		delta uint32
		want  uint32
	}{
		{"normal add", 100, 50, 150},
		{"add zero", 100, 0, 100},
		{"wraparound", MaxSeqNumber31, 1, 0},
		{"wraparound by 10", MaxSeqNumber31, 10, 9},
		{"from zero", 0, 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqAdd(tt.seq, tt.delta)
			if got != tt.want {
				t.Errorf("SeqAdd(%d, %d) = %d, want %d", tt.seq, tt.delta, got, tt.want)
			}
		})
	}
}

func TestSeqSub(t *testing.T) {
	tests := []struct {
		name  string
		seq   uint32
		delta uint32
		want  uint32
	}{
		{"normal sub", 150, 50, 100},
		{"sub zero", 100, 0, 100},
		{"wraparound", 0, 1, MaxSeqNumber31},
		{"wraparound by 10", 5, 10, MaxSeqNumber31 - 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqSub(tt.seq, tt.delta)
			if got != tt.want {
				t.Errorf("SeqSub(%d, %d) = %d, want %d", tt.seq, tt.delta, got, tt.want)
			}
		})
	}
}

func TestSeqInRange(t *testing.T) {
	tests := []struct {
		name  string
		seq   uint32
		start uint32
		end   uint32
		want  bool
	}{
		// Normal ranges
		{"in range", 5, 1, 10, true},
		{"at start", 1, 1, 10, true},
		{"at end", 10, 1, 10, true},
		{"before range", 0, 1, 10, false},
		{"after range", 15, 1, 10, false},

		// Wraparound ranges (start > end in circular space)
		{"in wraparound range (low)", 5, MaxSeqNumber31 - 5, 10, true},
		{"in wraparound range (high)", MaxSeqNumber31 - 3, MaxSeqNumber31 - 5, 10, true},
		{"at wraparound start", MaxSeqNumber31 - 5, MaxSeqNumber31 - 5, 10, true},
		{"at wraparound end", 10, MaxSeqNumber31 - 5, 10, true},
		{"outside wraparound", 100, MaxSeqNumber31 - 5, 10, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SeqInRange(tt.seq, tt.start, tt.end)
			if got != tt.want {
				t.Errorf("SeqInRange(%d, %d, %d) = %v, want %v",
					tt.seq, tt.start, tt.end, got, tt.want)
			}
		})
	}
}

// Benchmark SeqLess for performance comparison
func BenchmarkSeqLess(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqLess(a, c)
	}
}

// Benchmark SeqDiff
func BenchmarkSeqDiff(b *testing.B) {
	a := uint32(1000000)
	c := uint32(1000050)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SeqDiff(a, c)
	}
}
