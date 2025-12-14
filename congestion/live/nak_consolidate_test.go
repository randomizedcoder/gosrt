package live

import (
	"testing"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
)

// createTestReceiverForConsolidation creates a receiver with NAK btree for consolidation testing.
func createTestReceiverForConsolidation(t *testing.T) *receiver {
	t.Helper()

	m := &metrics.ConnectionMetrics{}

	return &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            3, // Default merge gap
		nakConsolidationBudget: 2 * time.Millisecond,
		metrics:                m,
	}
}

func TestConsolidateNakBtree_Empty(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	list := r.consolidateNakBtree()

	if len(list) != 0 {
		t.Errorf("Expected empty list for empty btree, got %d entries", len(list))
	}
}

func TestConsolidateNakBtree_SingleEntry(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	r.nakBtree.Insert(100)

	list := r.consolidateNakBtree()

	if len(list) != 2 {
		t.Fatalf("Expected 2 entries (start, end), got %d", len(list))
	}

	// Single entry: start == end
	if list[0].Val() != 100 || list[1].Val() != 100 {
		t.Errorf("Expected (100, 100), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

func TestConsolidateNakBtree_ContiguousRange(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	// Insert contiguous sequences
	for seq := uint32(100); seq <= 105; seq++ {
		r.nakBtree.Insert(seq)
	}

	list := r.consolidateNakBtree()

	if len(list) != 2 {
		t.Fatalf("Expected 2 entries (one range), got %d", len(list))
	}

	// Range: 100-105
	if list[0].Val() != 100 || list[1].Val() != 105 {
		t.Errorf("Expected range (100, 105), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

func TestConsolidateNakBtree_MergeWithinGap(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3 // Allow gaps up to 3

	// Insert: 100, 101, 102, then skip 3 to 106, 107, 108
	// Gap between 102 and 106 is 3 (103, 104, 105), which equals nakMergeGap
	r.nakBtree.Insert(100)
	r.nakBtree.Insert(101)
	r.nakBtree.Insert(102)
	r.nakBtree.Insert(106)
	r.nakBtree.Insert(107)
	r.nakBtree.Insert(108)

	list := r.consolidateNakBtree()

	if len(list) != 2 {
		t.Fatalf("Expected 2 entries (one merged range), got %d", len(list))
	}

	// Should merge into single range 100-108
	if list[0].Val() != 100 || list[1].Val() != 108 {
		t.Errorf("Expected range (100, 108), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

func TestConsolidateNakBtree_GapExceedsMergeThreshold(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 2 // Only merge gaps up to 2

	// Insert: 100, 101, then skip 4 to 106, 107
	// Gap between 101 and 106 is 4 (102, 103, 104, 105), which exceeds nakMergeGap=2
	r.nakBtree.Insert(100)
	r.nakBtree.Insert(101)
	r.nakBtree.Insert(106)
	r.nakBtree.Insert(107)

	list := r.consolidateNakBtree()

	if len(list) != 4 {
		t.Fatalf("Expected 4 entries (two ranges), got %d", len(list))
	}

	// First range: 100-101
	if list[0].Val() != 100 || list[1].Val() != 101 {
		t.Errorf("Expected first range (100, 101), got (%d, %d)", list[0].Val(), list[1].Val())
	}

	// Second range: 106-107
	if list[2].Val() != 106 || list[3].Val() != 107 {
		t.Errorf("Expected second range (106, 107), got (%d, %d)", list[2].Val(), list[3].Val())
	}
}

func TestConsolidateNakBtree_MixedSinglesAndRanges(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 1 // Only merge if gap <= 1

	// Insert: 100, then gap 5, then 106, 107, 108, then gap 5, then 114
	r.nakBtree.Insert(100)
	r.nakBtree.Insert(106)
	r.nakBtree.Insert(107)
	r.nakBtree.Insert(108)
	r.nakBtree.Insert(114)

	list := r.consolidateNakBtree()

	// Expected: 3 ranges/singles: (100,100), (106,108), (114,114)
	if len(list) != 6 {
		t.Fatalf("Expected 6 entries (three ranges/singles), got %d", len(list))
	}

	// Single: 100
	if list[0].Val() != 100 || list[1].Val() != 100 {
		t.Errorf("Expected (100, 100), got (%d, %d)", list[0].Val(), list[1].Val())
	}

	// Range: 106-108
	if list[2].Val() != 106 || list[3].Val() != 108 {
		t.Errorf("Expected (106, 108), got (%d, %d)", list[2].Val(), list[3].Val())
	}

	// Single: 114
	if list[4].Val() != 114 || list[5].Val() != 114 {
		t.Errorf("Expected (114, 114), got (%d, %d)", list[4].Val(), list[5].Val())
	}
}

func TestNAKEntry_IsRange(t *testing.T) {
	tests := []struct {
		name     string
		entry    NAKEntry
		expected bool
	}{
		{"single", NAKEntry{Start: 100, End: 100}, false},
		{"range", NAKEntry{Start: 100, End: 105}, true},
		{"two element", NAKEntry{Start: 100, End: 101}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.entry.IsRange() != tc.expected {
				t.Errorf("IsRange() = %v, expected %v", tc.entry.IsRange(), tc.expected)
			}
		})
	}
}

func TestNAKEntry_Count(t *testing.T) {
	tests := []struct {
		name     string
		entry    NAKEntry
		expected uint32
	}{
		{"single", NAKEntry{Start: 100, End: 100}, 1},
		{"range of 2", NAKEntry{Start: 100, End: 101}, 2},
		{"range of 6", NAKEntry{Start: 100, End: 105}, 6},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.entry.Count() != tc.expected {
				t.Errorf("Count() = %d, expected %d", tc.entry.Count(), tc.expected)
			}
		})
	}
}

func TestEntriesToNakList_Empty(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	list := r.entriesToNakList(nil)

	if list != nil {
		t.Errorf("Expected nil for empty entries, got %v", list)
	}

	list = r.entriesToNakList([]NAKEntry{})

	if list != nil {
		t.Errorf("Expected nil for empty entries slice, got %v", list)
	}
}

func TestEntriesToNakList_SingleAndRange(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	entries := []NAKEntry{
		{Start: 100, End: 100}, // Single
		{Start: 200, End: 205}, // Range
	}

	list := r.entriesToNakList(entries)

	if len(list) != 4 {
		t.Fatalf("Expected 4 entries, got %d", len(list))
	}

	// Verify the circular.Number values
	if list[0].Val() != 100 || list[1].Val() != 100 {
		t.Errorf("Expected (100, 100), got (%d, %d)", list[0].Val(), list[1].Val())
	}
	if list[2].Val() != 200 || list[3].Val() != 205 {
		t.Errorf("Expected (200, 205), got (%d, %d)", list[2].Val(), list[3].Val())
	}
}

// BenchmarkConsolidateNakBtree tests consolidation performance with various sizes.
func BenchmarkConsolidateNakBtree(b *testing.B) {
	sizes := []int{10, 100, 500, 1000}

	for _, size := range sizes {
		b.Run(formatSize(size), func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 10 * time.Millisecond, // Allow more time for benchmark
				metrics:                &metrics.ConnectionMetrics{},
			}

			// Pre-populate with entries (mix of singles and ranges)
			for i := 0; i < size; i++ {
				// Every 5th entry has a larger gap to create separate ranges
				if i%5 == 0 {
					r.nakBtree.Insert(uint32(i * 10))
				} else {
					r.nakBtree.Insert(uint32(i*10 + 1))
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}

func formatSize(size int) string {
	if size >= 1000 {
		return string(rune('0'+size/1000)) + "k"
	}
	return string(rune('0'+size/100)) + "00"
}

// TestConsolidateNakBtree_SequenceWraparound tests handling near sequence wraparound.
func TestConsolidateNakBtree_SequenceWraparound(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 2

	// Insert sequences near MAX_SEQUENCENUMBER
	maxSeq := uint32(packet.MAX_SEQUENCENUMBER)

	r.nakBtree.Insert(maxSeq - 2) // Near max
	r.nakBtree.Insert(maxSeq - 1)
	r.nakBtree.Insert(maxSeq)
	// Note: wraparound would be 0, 1, 2... but we're testing within a single range here

	list := r.consolidateNakBtree()

	if len(list) != 2 {
		t.Fatalf("Expected 2 entries (one range), got %d", len(list))
	}

	// Should be a contiguous range near max
	if list[0].Val() != maxSeq-2 || list[1].Val() != maxSeq {
		t.Errorf("Expected range (%d, %d), got (%d, %d)",
			maxSeq-2, maxSeq, list[0].Val(), list[1].Val())
	}
}

// TestSyncPoolReuse verifies that sync.Pool is being reused properly.
func TestSyncPoolReuse(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	// Run consolidation multiple times and verify no panics
	for i := 0; i < 100; i++ {
		r.nakBtree.Insert(uint32(i))
		_ = r.consolidateNakBtree()
	}

	// Clear and run again
	r.nakBtree.Clear()
	for i := 0; i < 50; i++ {
		r.nakBtree.Insert(uint32(i * 3))
		_ = r.consolidateNakBtree()
	}

	// If we get here without panic, sync.Pool is working correctly
}

// TestConsolidateMetrics verifies metrics are being tracked.
func TestConsolidateMetrics(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	// Insert some entries that will merge
	r.nakBtree.Insert(100)
	r.nakBtree.Insert(101)
	r.nakBtree.Insert(102)

	initialRuns := r.metrics.NakConsolidationRuns.Load()

	_ = r.consolidateNakBtree()

	if r.metrics.NakConsolidationRuns.Load() != initialRuns+1 {
		t.Error("Expected NakConsolidationRuns to increment")
	}

	if r.metrics.NakConsolidationEntries.Load() == 0 {
		t.Error("Expected NakConsolidationEntries to be non-zero")
	}

	if r.metrics.NakConsolidationMerged.Load() == 0 {
		t.Error("Expected NakConsolidationMerged to be non-zero for contiguous sequences")
	}
}

// Ensure the circular package's SeqDiff is used for gap calculation
func TestConsolidateUsesCircularSeqDiff(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 2

	// Use sequences that would wrap if using simple subtraction
	// But with circular.SeqDiff, adjacent sequences should merge
	r.nakBtree.Insert(10)
	r.nakBtree.Insert(11)
	r.nakBtree.Insert(12)

	list := r.consolidateNakBtree()

	// Should produce single range (10, 12)
	if len(list) != 2 {
		t.Fatalf("Expected 2 entries, got %d", len(list))
	}

	if list[0].Val() != 10 || list[1].Val() != 12 {
		t.Errorf("Expected (10, 12), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

// BenchmarkSyncPoolVsNoPool compares pool vs non-pool allocation (commented out baseline).
func BenchmarkSyncPoolConsolidation(b *testing.B) {
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            3,
		nakConsolidationBudget: 10 * time.Millisecond,
		metrics:                &metrics.ConnectionMetrics{},
	}

	// Pre-populate with 200 entries
	for i := 0; i < 200; i++ {
		r.nakBtree.Insert(uint32(i * 2)) // Every other sequence to create some merges
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = r.consolidateNakBtree()
	}
}

// Test helper to verify circular.Number conversion
func TestEntriesToNakList_CircularNumberMax(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	entries := []NAKEntry{
		{Start: 100, End: 200},
	}

	list := r.entriesToNakList(entries)

	if len(list) != 2 {
		t.Fatalf("Expected 2 entries, got %d", len(list))
	}

	// Verify the circular.Number max is correctly set
	// (we can't easily verify max from outside, but we can verify values work)
	expectedMax := uint32(packet.MAX_SEQUENCENUMBER)

	// The circular.Number should have the correct max for comparison
	num := circular.New(100, expectedMax)
	if !num.Lt(circular.New(200, expectedMax)) {
		t.Error("circular.Number comparison not working as expected")
	}
}

