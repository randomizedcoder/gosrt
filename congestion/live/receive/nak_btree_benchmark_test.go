//go:build go1.18

package receive

// nak_btree_benchmark_test.go - Benchmarks for NAK btree TSBPD-aware operations
// See nak_btree_expiry_optimization.md for design details
//
// Run with: go test -bench=. -benchmem ./congestion/live/receive/...

import (
	"fmt"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════
// DeleteBeforeTsbpd Benchmarks
// ═══════════════════════════════════════════════════════════════════════════

// BenchmarkDeleteBeforeTsbpd_Optimized tests the current implementation
// Setup cost is amortized by running multiple operations per b.N iteration
func BenchmarkDeleteBeforeTsbpd_Optimized(b *testing.B) {
	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			// Pre-create btrees for all iterations
			btrees := make([]*nakBtree, b.N)
			baseTime := uint64(1_000_000)
			for i := 0; i < b.N; i++ {
				btrees[i] = newNakBtree(32)
				for j := 0; j < size; j++ {
					btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
				}
			}
			threshold := baseTime + uint64(size/2)*1000

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				btrees[i].DeleteBeforeTsbpd(threshold)
			}
		})
	}
}

// BenchmarkDeleteBeforeTsbpd_Slow tests the collect-then-delete implementation
func BenchmarkDeleteBeforeTsbpd_Slow(b *testing.B) {
	sizes := []int{10, 100, 1000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			btrees := make([]*nakBtree, b.N)
			baseTime := uint64(1_000_000)
			for i := 0; i < b.N; i++ {
				btrees[i] = newNakBtree(32)
				for j := 0; j < size; j++ {
					btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
				}
			}
			threshold := baseTime + uint64(size/2)*1000

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				btrees[i].DeleteBeforeTsbpdSlow(threshold)
			}
		})
	}
}

// BenchmarkDeleteBeforeTsbpd_ExpireNone tests when no entries are expired
func BenchmarkDeleteBeforeTsbpd_ExpireNone(b *testing.B) {
	size := 100
	btrees := make([]*nakBtree, b.N)
	baseTime := uint64(1_000_000)
	for i := 0; i < b.N; i++ {
		btrees[i] = newNakBtree(32)
		for j := 0; j < size; j++ {
			btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
		}
	}
	// Threshold below all entries - nothing expires
	threshold := baseTime - 1000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		btrees[i].DeleteBeforeTsbpd(threshold)
	}
}

// BenchmarkDeleteBeforeTsbpd_ExpireAll tests when all entries are expired
func BenchmarkDeleteBeforeTsbpd_ExpireAll(b *testing.B) {
	size := 100
	btrees := make([]*nakBtree, b.N)
	baseTime := uint64(1_000_000)
	for i := 0; i < b.N; i++ {
		btrees[i] = newNakBtree(32)
		for j := 0; j < size; j++ {
			btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
		}
	}
	// Threshold above all entries - everything expires
	threshold := baseTime + uint64(size)*1000 + 1000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		btrees[i].DeleteBeforeTsbpd(threshold)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Insert Benchmarks
// ═══════════════════════════════════════════════════════════════════════════

// BenchmarkInsertWithTsbpd measures insert performance with TSBPD
func BenchmarkInsertWithTsbpd(b *testing.B) {
	nb := newNakBtree(32)
	baseTime := uint64(1_000_000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nb.InsertWithTsbpd(uint32(i), baseTime+uint64(i)*1000)
	}
}

// BenchmarkInsertBatchWithTsbpd measures batch insert performance
func BenchmarkInsertBatchWithTsbpd(b *testing.B) {
	batchSizes := []int{10, 50, 100}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("batch=%d", batchSize), func(b *testing.B) {
			seqs := make([]uint32, batchSize)
			tsbpds := make([]uint64, batchSize)
			baseTime := uint64(1_000_000)

			// Pre-create btrees
			btrees := make([]*nakBtree, b.N)
			for i := 0; i < b.N; i++ {
				btrees[i] = newNakBtree(32)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Prepare batch data
				for j := 0; j < batchSize; j++ {
					seqs[j] = uint32(i*batchSize + j)
					tsbpds[j] = baseTime + uint64(j)*1000
				}
				btrees[i].InsertBatchWithTsbpd(seqs, tsbpds)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// TSBPD Estimation Benchmarks
// ═══════════════════════════════════════════════════════════════════════════

// BenchmarkEstimateTsbpdForSeq measures linear interpolation performance
func BenchmarkEstimateTsbpdForSeq(b *testing.B) {
	lowerSeq := uint32(100)
	lowerTsbpd := uint64(1_000_000)
	upperSeq := uint32(200)
	upperTsbpd := uint64(2_000_000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		missingSeq := uint32(100 + (i % 100))
		_ = estimateTsbpdForSeq(missingSeq, lowerSeq, lowerTsbpd, upperSeq, upperTsbpd)
	}
}

// BenchmarkEstimateTsbpdForSeq_VaryingGaps tests with different gap sizes
func BenchmarkEstimateTsbpdForSeq_VaryingGaps(b *testing.B) {
	gaps := []int{2, 10, 100}

	for _, gap := range gaps {
		b.Run(fmt.Sprintf("gap=%d", gap), func(b *testing.B) {
			lowerSeq := uint32(100)
			lowerTsbpd := uint64(1_000_000)
			upperSeq := uint32(100 + gap)
			upperTsbpd := uint64(1_000_000 + uint64(gap)*1000)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				missingSeq := uint32(100 + (i % gap))
				_ = estimateTsbpdForSeq(missingSeq, lowerSeq, lowerTsbpd, upperSeq, upperTsbpd)
			}
		})
	}
}

// BenchmarkUpdateInterPacketInterval measures EWMA update performance
func BenchmarkUpdateInterPacketInterval(b *testing.B) {
	lastArrivalUs := uint64(1_000_000)
	oldInterval := uint64(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nowUs := lastArrivalUs + 1000 + uint64(i%100)
		_, _ = updateInterPacketInterval(nowUs, lastArrivalUs, oldInterval)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Comparison Benchmarks (small scale for quick runs)
// ═══════════════════════════════════════════════════════════════════════════

// BenchmarkDeleteComparison provides side-by-side comparison data
func BenchmarkDeleteComparison(b *testing.B) {
	size := 100
	expirePercent := []int{0, 50, 100}

	for _, pct := range expirePercent {
		b.Run(fmt.Sprintf("expire=%d%%_optimized", pct), func(b *testing.B) {
			btrees := make([]*nakBtree, b.N)
			baseTime := uint64(1_000_000)
			for i := 0; i < b.N; i++ {
				btrees[i] = newNakBtree(32)
				for j := 0; j < size; j++ {
					btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
				}
			}
			threshold := baseTime + uint64(size*pct/100)*1000

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				btrees[i].DeleteBeforeTsbpd(threshold)
			}
		})

		b.Run(fmt.Sprintf("expire=%d%%_slow", pct), func(b *testing.B) {
			btrees := make([]*nakBtree, b.N)
			baseTime := uint64(1_000_000)
			for i := 0; i < b.N; i++ {
				btrees[i] = newNakBtree(32)
				for j := 0; j < size; j++ {
					btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
				}
			}
			threshold := baseTime + uint64(size*pct/100)*1000

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				btrees[i].DeleteBeforeTsbpdSlow(threshold)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Memory Allocation Benchmarks
// ═══════════════════════════════════════════════════════════════════════════

// BenchmarkMemoryAlloc_Delete measures memory pressure during delete
func BenchmarkMemoryAlloc_Delete(b *testing.B) {
	b.Run("optimized", func(b *testing.B) {
		btrees := make([]*nakBtree, b.N)
		baseTime := uint64(1_000_000)
		for i := 0; i < b.N; i++ {
			btrees[i] = newNakBtree(32)
			for j := 0; j < 100; j++ {
				btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
			}
		}
		threshold := baseTime + 50*1000

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			btrees[i].DeleteBeforeTsbpd(threshold)
		}
	})

	b.Run("slow", func(b *testing.B) {
		btrees := make([]*nakBtree, b.N)
		baseTime := uint64(1_000_000)
		for i := 0; i < b.N; i++ {
			btrees[i] = newNakBtree(32)
			for j := 0; j < 100; j++ {
				btrees[i].InsertWithTsbpd(uint32(j), baseTime+uint64(j)*1000)
			}
		}
		threshold := baseTime + 50*1000

		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			btrees[i].DeleteBeforeTsbpdSlow(threshold)
		}
	})
}
