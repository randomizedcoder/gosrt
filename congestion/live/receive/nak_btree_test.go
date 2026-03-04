//go:build go1.18

package receive

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
	minSeq, ok := nb.Min()
	require.True(t, ok)
	require.Equal(t, uint32(100), minSeq)

	maxSeq, ok := nb.Max()
	require.True(t, ok)
	require.Equal(t, uint32(200), maxSeq)

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
	nb.Iterate(func(entry NakEntryWithTime) bool {
		seqs = append(seqs, entry.Seq)
		return true
	})
	require.Equal(t, []uint32{100, 200, 300}, seqs)

	// Iterate descending
	seqs = nil
	nb.IterateDescending(func(entry NakEntryWithTime) bool {
		seqs = append(seqs, entry.Seq)
		return true
	})
	require.Equal(t, []uint32{300, 200, 100}, seqs)

	// Iterate with early stop
	seqs = nil
	nb.Iterate(func(entry NakEntryWithTime) bool {
		seqs = append(seqs, entry.Seq)
		return entry.Seq < 200 // Stop after 200
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
	nb.Iterate(func(entry NakEntryWithTime) bool {
		seqs = append(seqs, entry.Seq)
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
	nb.Iterate(func(entry NakEntryWithTime) bool {
		seqs = append(seqs, entry.Seq)
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
	nb.Iterate(func(entry NakEntryWithTime) bool {
		count++
		return true
	})
	require.Equal(t, 0, count)
}

func TestNakBtree_ConcurrentAccess(t *testing.T) {
	nb := newNakBtree(32)
	var wg sync.WaitGroup

	// Concurrent inserts - MUST use InsertLocking for thread safety
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(seq uint32) {
			defer wg.Done()
			nb.InsertLocking(seq)
		}(uint32(i))
	}
	wg.Wait()

	require.Equal(t, 100, nb.LenLocking())

	// Concurrent reads while deleting
	// Use *Locking() versions for concurrent access
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			nb.DeleteLocking(uint32(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = nb.LenLocking()
			_ = nb.HasLocking(uint32(i))
		}
	}()
	wg.Wait()

	require.Equal(t, 50, nb.LenLocking())
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

// TestNakBtree_NakEntryWithTime verifies that NakEntryWithTime fields are initialized correctly
func TestNakBtree_NakEntryWithTime(t *testing.T) {
	nb := newNakBtree(32)

	// Insert creates entry with zero LastNakedAtUs and NakCount
	nb.Insert(100)
	nb.Insert(200)

	nb.Iterate(func(entry NakEntryWithTime) bool {
		require.Equal(t, uint64(0), entry.LastNakedAtUs, "New entries should have LastNakedAtUs=0")
		require.Equal(t, uint32(0), entry.NakCount, "New entries should have NakCount=0")
		return true
	})
}

// TestNakBtree_IterateAndUpdate verifies that IterateAndUpdate can modify entries
func TestNakBtree_IterateAndUpdate(t *testing.T) {
	nb := newNakBtree(32)

	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)

	nowUs := uint64(1000000) // 1 second

	// Update all entries with NAK tracking
	var updated int
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		entry.LastNakedAtUs = nowUs
		entry.NakCount++
		updated++
		return entry, true, true // Update, continue
	})

	require.Equal(t, 3, updated)

	// Verify updates were applied
	nb.Iterate(func(entry NakEntryWithTime) bool {
		require.Equal(t, nowUs, entry.LastNakedAtUs, "LastNakedAtUs should be updated")
		require.Equal(t, uint32(1), entry.NakCount, "NakCount should be incremented")
		return true
	})

	// Update again, simulating second NAK
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		entry.LastNakedAtUs = nowUs + 100000 // +100ms
		entry.NakCount++
		return entry, true, true
	})

	// Verify cumulative updates
	nb.Iterate(func(entry NakEntryWithTime) bool {
		require.Equal(t, nowUs+100000, entry.LastNakedAtUs, "LastNakedAtUs should be updated again")
		require.Equal(t, uint32(2), entry.NakCount, "NakCount should be 2")
		return true
	})
}

// TestNakBtree_IterateAndUpdate_PartialUpdate verifies selective updates
func TestNakBtree_IterateAndUpdate_PartialUpdate(t *testing.T) {
	nb := newNakBtree(32)

	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)

	nowUs := uint64(1000000)

	// Only update entries with Seq >= 200
	var updateCount, skipCount int
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		if entry.Seq >= 200 {
			entry.LastNakedAtUs = nowUs
			entry.NakCount++
			updateCount++
			return entry, true, true // Update, continue
		}
		skipCount++
		return entry, false, true // Don't update, continue
	})

	require.Equal(t, 2, updateCount, "Should update 2 entries (200, 300)")
	require.Equal(t, 1, skipCount, "Should skip 1 entry (100)")

	// Verify selective updates
	nb.Iterate(func(entry NakEntryWithTime) bool {
		if entry.Seq == 100 {
			require.Equal(t, uint64(0), entry.LastNakedAtUs, "Entry 100 should not be updated")
			require.Equal(t, uint32(0), entry.NakCount, "Entry 100 NakCount should be 0")
		} else {
			require.Equal(t, nowUs, entry.LastNakedAtUs, "Entry %d should be updated", entry.Seq)
			require.Equal(t, uint32(1), entry.NakCount, "Entry %d NakCount should be 1", entry.Seq)
		}
		return true
	})
}

// TestNakBtree_IterateAndUpdate_EarlyStop verifies early termination
func TestNakBtree_IterateAndUpdate_EarlyStop(t *testing.T) {
	nb := newNakBtree(32)

	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)

	// Stop after first entry
	var count int
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		count++
		entry.LastNakedAtUs = 1
		return entry, true, false // Update, but STOP
	})

	require.Equal(t, 1, count, "Should only process 1 entry")

	// Verify only first entry was updated
	var updatedCount int
	nb.Iterate(func(entry NakEntryWithTime) bool {
		if entry.LastNakedAtUs == 1 {
			updatedCount++
		}
		return true
	})
	require.Equal(t, 1, updatedCount, "Only 1 entry should be updated")
}

// ═══════════════════════════════════════════════════════════════════════════════
// EVENT LOOP SIMULATION TESTS (Single-Threaded, Lock-Free)
// These tests verify that lock-free functions work correctly in the single-threaded
// event loop context. No concurrent access - operations are sequential.
// ═══════════════════════════════════════════════════════════════════════════════

// TestNakBtree_EventLoop_BasicOperations simulates basic event loop operations
func TestNakBtree_EventLoop_BasicOperations(t *testing.T) {
	nb := newNakBtree(32)

	// Simulate event loop: sequential operations, no locking needed
	// These are the lock-free versions
	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)

	require.Equal(t, 3, nb.Len())
	require.True(t, nb.Has(100))
	require.True(t, nb.Has(200))
	require.True(t, nb.Has(300))
	require.False(t, nb.Has(400))

	// Delete (lock-free)
	found := nb.Delete(200)
	require.True(t, found)
	require.Equal(t, 2, nb.Len())
	require.False(t, nb.Has(200))
}

// TestNakBtree_EventLoop_InsertBatch simulates batch insert in event loop
func TestNakBtree_EventLoop_InsertBatch(t *testing.T) {
	nb := newNakBtree(32)

	// Simulate finding multiple gaps in one scan
	gaps := []uint32{100, 101, 102, 200, 201, 202, 300}
	count := nb.InsertBatch(gaps)

	require.Equal(t, 7, count)
	require.Equal(t, 7, nb.Len())

	// Verify all present
	for _, seq := range gaps {
		require.True(t, nb.Has(seq), "seq %d should be present", seq)
	}
}

// TestNakBtree_EventLoop_IterateAndUpdate simulates NAK suppression in event loop
func TestNakBtree_EventLoop_IterateAndUpdate(t *testing.T) {
	nb := newNakBtree(32)

	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)

	nowUs := uint64(1000000) // 1 second

	// Simulate NAK consolidation: iterate and update LastNakedAtUs
	var processedCount int
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		processedCount++
		entry.LastNakedAtUs = nowUs
		entry.NakCount++
		return entry, true, true // Update, continue
	})

	require.Equal(t, 3, processedCount)

	// Verify updates were applied
	nb.Iterate(func(entry NakEntryWithTime) bool {
		require.Equal(t, nowUs, entry.LastNakedAtUs)
		require.Equal(t, uint32(1), entry.NakCount)
		return true
	})
}

// TestNakBtree_EventLoop_DeleteBefore simulates expiring old NAK entries
func TestNakBtree_EventLoop_DeleteBefore(t *testing.T) {
	nb := newNakBtree(32)

	// Insert some sequences
	nb.Insert(100)
	nb.Insert(200)
	nb.Insert(300)
	nb.Insert(400)
	nb.Insert(500)

	// Delete entries before 300 (simulate expiring old entries)
	deleted := nb.DeleteBefore(300)
	require.Equal(t, 2, deleted)

	// Verify remaining entries
	require.Equal(t, 3, nb.Len())
	require.False(t, nb.Has(100))
	require.False(t, nb.Has(200))
	require.True(t, nb.Has(300))
	require.True(t, nb.Has(400))
	require.True(t, nb.Has(500))
}

// TestNakBtree_EventLoop_FullCycle simulates a complete NAK processing cycle
func TestNakBtree_EventLoop_FullCycle(t *testing.T) {
	nb := newNakBtree(32)

	// Phase 1: Gap scan finds missing packets
	gaps := []uint32{100, 101, 102, 150, 200, 201}
	nb.InsertBatch(gaps)
	require.Equal(t, 6, nb.Len())

	// Phase 2: NAK consolidation marks entries as NAK'd
	nowUs := uint64(1000000)
	var nakList []uint32
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		nakList = append(nakList, entry.Seq)
		entry.LastNakedAtUs = nowUs
		entry.NakCount++
		return entry, true, true
	})
	require.Equal(t, []uint32{100, 101, 102, 150, 200, 201}, nakList)

	// Phase 3: Some retransmits arrive
	nb.Delete(101)
	nb.Delete(150)
	nb.Delete(201)
	require.Equal(t, 3, nb.Len())

	// Phase 4: Another NAK cycle - only remaining entries processed
	nakList = nil
	nb.IterateAndUpdate(func(entry NakEntryWithTime) (NakEntryWithTime, bool, bool) {
		nakList = append(nakList, entry.Seq)
		return entry, false, true // Don't update, just collect
	})
	require.Equal(t, []uint32{100, 102, 200}, nakList)

	// Phase 5: Clear all (connection close or reset)
	nb.Clear()
	require.Equal(t, 0, nb.Len())
}

// ═══════════════════════════════════════════════════════════════════════════════
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
// This shows lock contention impact with individual InsertLocking() calls
// Uses *Locking versions because goroutines run concurrently
func BenchmarkNakBtree_InsertIndividual_Parallel(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(formatBenchName("n", n), func(b *testing.B) {
			nb := newNakBtree(32)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				seq := uint32(0)
				for pb.Next() {
					for j := 0; j < n; j++ {
						nb.InsertLocking(seq)
						seq++
					}
				}
			})
		})
	}
}

// BenchmarkNakBtree_InsertBatch_Parallel benchmarks concurrent batch inserts
// This shows reduced lock contention with InsertBatchLocking()
// Uses *Locking versions because goroutines run concurrently
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
					nb.InsertBatchLocking(seqs)
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
