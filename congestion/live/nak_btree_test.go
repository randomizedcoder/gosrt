//go:build go1.18

package live

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNakBtree_BasicOperations(t *testing.T) {
	nb := newNakBtree(32)

	// Initially empty
	require.Equal(t, 0, nb.Len())
	require.False(t, nb.Has(100))

	// Insert
	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(150)
	require.Equal(t, 3, nb.Len())
	require.True(t, nb.Has(100))
	require.True(t, nb.Has(150))
	require.True(t, nb.Has(200))
	require.False(t, nb.Has(300))

	// Min/Max
	min, ok := nb.Min()
	require.True(t, ok)
	require.Equal(t, uint32(100), min)

	max, ok := nb.Max()
	require.True(t, ok)
	require.Equal(t, uint32(200), max)

	// Delete
	found := nb.Delete(150)
	require.True(t, found)
	require.Equal(t, 2, nb.Len())
	require.False(t, nb.Has(150))

	// Delete non-existent
	found = nb.Delete(999)
	require.False(t, found)

	// Clear
	nb.Clear()
	require.Equal(t, 0, nb.Len())
}

func TestNakBtree_Iterate(t *testing.T) {
	nb := newNakBtree(32)

	// Insert out of order
	nb.Insert(300)
	nb.Insert(100)
	nb.Insert(200)

	// Iterate ascending - should be in sequence order
	var seqs []uint32
	nb.Iterate(func(seq uint32) bool {
		seqs = append(seqs, seq)
		return true
	})
	require.Equal(t, []uint32{100, 200, 300}, seqs)

	// Iterate descending
	seqs = nil
	nb.IterateDescending(func(seq uint32) bool {
		seqs = append(seqs, seq)
		return true
	})
	require.Equal(t, []uint32{300, 200, 100}, seqs)

	// Iterate with early stop
	seqs = nil
	nb.Iterate(func(seq uint32) bool {
		seqs = append(seqs, seq)
		return seq < 200 // Stop after 200
	})
	require.Equal(t, []uint32{100, 200}, seqs)
}

func TestNakBtree_DeleteBefore(t *testing.T) {
	nb := newNakBtree(32)

	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)
	nb.Insert(400)
	nb.Insert(500)

	// Delete sequences before 300
	deleted := nb.DeleteBefore(300)
	require.Equal(t, 2, deleted) // 100 and 200

	require.Equal(t, 3, nb.Len())
	require.False(t, nb.Has(100))
	require.False(t, nb.Has(200))
	require.True(t, nb.Has(300))
	require.True(t, nb.Has(400))
	require.True(t, nb.Has(500))
}

func TestNakBtree_SequenceOrder(t *testing.T) {
	nb := newNakBtree(32)

	// Test that btree maintains sequence order for realistic gaps
	// In practice, packets in buffer are close to each other (within thousands)
	nb.Insert(1000)
	nb.Insert(1005)
	nb.Insert(1002)
	nb.Insert(1010)
	nb.Insert(1001)

	// Should iterate in sequence order
	var seqs []uint32
	nb.Iterate(func(seq uint32) bool {
		seqs = append(seqs, seq)
		return true
	})

	require.Equal(t, []uint32{1000, 1001, 1002, 1005, 1010}, seqs)
}

func TestNakBtree_LargeSequenceNumbers(t *testing.T) {
	nb := newNakBtree(32)

	// Test with large sequence numbers (but still realistic gaps)
	// Max 31-bit sequence is 0x7FFFFFFF = 2147483647
	baseSeq := uint32(2147483000) // Near max but not at wraparound

	nb.Insert(baseSeq)
	nb.Insert(baseSeq + 100)
	nb.Insert(baseSeq + 50)
	nb.Insert(baseSeq + 200)

	var seqs []uint32
	nb.Iterate(func(seq uint32) bool {
		seqs = append(seqs, seq)
		return true
	})

	require.Equal(t, 4, len(seqs))
	require.Equal(t, baseSeq, seqs[0])
	require.Equal(t, baseSeq+50, seqs[1])
	require.Equal(t, baseSeq+100, seqs[2])
	require.Equal(t, baseSeq+200, seqs[3])
}

func TestNakBtree_DuplicateInsert(t *testing.T) {
	nb := newNakBtree(32)

	nb.Insert(100)
	nb.Insert(100) // Duplicate
	nb.Insert(100) // Duplicate

	require.Equal(t, 1, nb.Len())
}

func TestNakBtree_EmptyOperations(t *testing.T) {
	nb := newNakBtree(32)

	// Min/Max on empty
	_, ok := nb.Min()
	require.False(t, ok)
	_, ok = nb.Max()
	require.False(t, ok)

	// Delete on empty
	found := nb.Delete(100)
	require.False(t, found)

	// DeleteBefore on empty
	deleted := nb.DeleteBefore(100)
	require.Equal(t, 0, deleted)

	// Iterate on empty
	count := 0
	nb.Iterate(func(seq uint32) bool {
		count++
		return true
	})
	require.Equal(t, 0, count)
}

func TestNakBtree_ConcurrentAccess(t *testing.T) {
	nb := newNakBtree(32)
	var wg sync.WaitGroup

	// Concurrent inserts
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(seq uint32) {
			defer wg.Done()
			nb.Insert(seq)
		}(uint32(i))
	}
	wg.Wait()

	require.Equal(t, 100, nb.Len())

	// Concurrent reads while deleting
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			nb.Delete(uint32(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = nb.Len()
			_ = nb.Has(uint32(i))
		}
	}()
	wg.Wait()

	require.Equal(t, 50, nb.Len())
}

func TestNakBtree_InsertBatch(t *testing.T) {
	nb := newNakBtree(32)

	// Insert batch
	seqs := []uint32{10, 20, 30, 40, 50}
	count := nb.InsertBatch(seqs)

	require.Equal(t, 5, count, "Expected 5 inserts")
	require.Equal(t, 5, nb.Len())

	// Verify all are present
	for _, seq := range seqs {
		require.True(t, nb.Has(seq), "Expected seq %d to be present", seq)
	}

	// Insert overlapping batch (should not double-count existing)
	seqs2 := []uint32{30, 40, 60, 70}
	count2 := nb.InsertBatch(seqs2)

	require.Equal(t, 2, count2, "Expected 2 new inserts (60, 70)")
	require.Equal(t, 7, nb.Len())

	// Empty batch
	count3 := nb.InsertBatch([]uint32{})
	require.Equal(t, 0, count3)
	require.Equal(t, 7, nb.Len())

	// Nil batch
	count4 := nb.InsertBatch(nil)
	require.Equal(t, 0, count4)
	require.Equal(t, 7, nb.Len())
}

// Benchmarks for Insert performance comparison

// BenchmarkNakBtree_InsertIndividual benchmarks inserting N sequences one at a time
// This simulates the CURRENT behavior where each Insert() acquires/releases the lock
func BenchmarkNakBtree_InsertIndividual(b *testing.B) {
	for _, n := range []int{10, 50, 100, 500} {
		b.Run(formatBenchName("n", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				nb := newNakBtree(32)
				for j := 0; j < n; j++ {
					nb.Insert(uint32(j))
				}
			}
		})
	}
}

// BenchmarkNakBtree_InsertBatch benchmarks inserting N sequences in one batch
// This simulates the NEW behavior where InsertBatch() acquires the lock once
func BenchmarkNakBtree_InsertBatch(b *testing.B) {
	for _, n := range []int{10, 50, 100, 500} {
		b.Run(formatBenchName("n", n), func(b *testing.B) {
			seqs := make([]uint32, n)
			for j := 0; j < n; j++ {
				seqs[j] = uint32(j)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nb := newNakBtree(32)
				nb.InsertBatch(seqs)
			}
		})
	}
}

// BenchmarkNakBtree_InsertIndividual_Parallel benchmarks concurrent individual inserts
// This shows lock contention impact with individual Insert() calls
func BenchmarkNakBtree_InsertIndividual_Parallel(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(formatBenchName("n", n), func(b *testing.B) {
			nb := newNakBtree(32)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				seq := uint32(0)
				for pb.Next() {
					for j := 0; j < n; j++ {
						nb.Insert(seq)
						seq++
					}
				}
			})
		})
	}
}

// BenchmarkNakBtree_InsertBatch_Parallel benchmarks concurrent batch inserts
// This shows reduced lock contention with InsertBatch()
func BenchmarkNakBtree_InsertBatch_Parallel(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(formatBenchName("n", n), func(b *testing.B) {
			nb := newNakBtree(32)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				seq := uint32(0)
				seqs := make([]uint32, n)
				for pb.Next() {
					for j := 0; j < n; j++ {
						seqs[j] = seq
						seq++
					}
					nb.InsertBatch(seqs)
				}
			})
		})
	}
}

func formatBenchName(prefix string, value int) string {
	return prefix + "=" + formatInt(value)
}

func formatInt(n int) string {
	if n >= 1000 {
		return formatInt(n/1000) + "k"
	}
	return string(rune('0'+n/100)) + string(rune('0'+(n%100)/10)) + string(rune('0'+n%10))
}

// BenchmarkNakBtree_RealisticGapPattern benchmarks a realistic periodic NAK scan
// Simulates: scan finds 5-20 gaps, inserts them, then later deletes as packets arrive
func BenchmarkNakBtree_RealisticGapPattern(b *testing.B) {
	for _, gapSize := range []int{5, 10, 20, 50} {
		b.Run(formatBenchName("gap", gapSize)+"_individual", func(b *testing.B) {
			nb := newNakBtree(32)
			baseSeq := uint32(1000)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Simulate periodicNakBtree finding gaps
				for j := 0; j < gapSize; j++ {
					nb.Insert(baseSeq + uint32(j))
				}
				// Simulate packets arriving and being deleted
				for j := 0; j < gapSize; j++ {
					nb.Delete(baseSeq + uint32(j))
				}
				baseSeq += uint32(gapSize)
			}
		})

		b.Run(formatBenchName("gap", gapSize)+"_batch", func(b *testing.B) {
			nb := newNakBtree(32)
			baseSeq := uint32(1000)
			seqs := make([]uint32, gapSize)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Simulate periodicNakBtree finding gaps - BATCHED
				for j := 0; j < gapSize; j++ {
					seqs[j] = baseSeq + uint32(j)
				}
				nb.InsertBatch(seqs)
				// Simulate packets arriving and being deleted (still individual)
				for j := 0; j < gapSize; j++ {
					nb.Delete(baseSeq + uint32(j))
				}
				baseSeq += uint32(gapSize)
			}
		})
	}
}
