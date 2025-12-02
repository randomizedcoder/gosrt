package circular

import (
	"testing"
)

// BenchmarkLt benchmarks the standard Lt() function
func BenchmarkLt(b *testing.B) {
	a := New(1000, max)
	vals := make([]Number, 1000)
	for i := range vals {
		vals[i] = New(uint32(1000+i), max)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, val := range vals {
			_ = a.Lt(val)
		}
	}
}

// BenchmarkLtBranchless benchmarks the branchless LtBranchless() function
func BenchmarkLtBranchless(b *testing.B) {
	a := New(1000, max)
	vals := make([]Number, 1000)
	for i := range vals {
		vals[i] = New(uint32(1000+i), max)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, val := range vals {
			_ = a.LtBranchless(val)
		}
	}
}

// BenchmarkLt_Wraparound benchmarks Lt() with wraparound cases
func BenchmarkLt_Wraparound(b *testing.B) {
	a := New(max-100, max)
	vals := make([]Number, 1000)
	for i := range vals {
		vals[i] = New(uint32(i), max) // Mix of before and after wraparound
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, val := range vals {
			_ = a.Lt(val)
		}
	}
}

// BenchmarkLtBranchless_Wraparound benchmarks LtBranchless() with wraparound cases
func BenchmarkLtBranchless_Wraparound(b *testing.B) {
	a := New(max-100, max)
	vals := make([]Number, 1000)
	for i := range vals {
		vals[i] = New(uint32(i), max) // Mix of before and after wraparound
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, val := range vals {
			_ = a.LtBranchless(val)
		}
	}
}

// BenchmarkLt_Random benchmarks Lt() with random sequence numbers (simulating out-of-order packets)
func BenchmarkLt_Random(b *testing.B) {
	// Create sequence numbers that simulate out-of-order packet arrival
	vals := make([]Number, 1000)
	for i := range vals {
		// Mix of in-order and out-of-order
		seq := uint32(1000 + (i*7)%1000) // Pseudo-random pattern
		vals[i] = New(seq, max)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < len(vals)-1; j++ {
			_ = vals[j].Lt(vals[j+1])
		}
	}
}

// BenchmarkLtBranchless_Random benchmarks LtBranchless() with random sequence numbers
func BenchmarkLtBranchless_Random(b *testing.B) {
	// Create sequence numbers that simulate out-of-order packet arrival
	vals := make([]Number, 1000)
	for i := range vals {
		// Mix of in-order and out-of-order
		seq := uint32(1000 + (i*7)%1000) // Pseudo-random pattern
		vals[i] = New(seq, max)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < len(vals)-1; j++ {
			_ = vals[j].LtBranchless(vals[j+1])
		}
	}
}

// BenchmarkLt_Equals benchmarks Lt() with many equal values (common in sorted structures)
func BenchmarkLt_Equals(b *testing.B) {
	a := New(1000, max)
	b_val := New(1000, max) // Same value

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.Lt(b_val) // Should return false quickly
	}
}

// BenchmarkLtBranchless_Equals benchmarks LtBranchless() with many equal values
func BenchmarkLtBranchless_Equals(b *testing.B) {
	a := New(1000, max)
	b_val := New(1000, max) // Same value

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.LtBranchless(b_val) // Should return false quickly
	}
}
