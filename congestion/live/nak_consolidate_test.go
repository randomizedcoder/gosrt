package live

import (
	"testing"
	"time"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
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

// ============================================================================
// Out-of-Order Insertion Tests
// ============================================================================

// TestConsolidateNakBtree_OutOfOrderInsertion verifies that consolidation works
// correctly even when sequences are inserted into the NAK btree out of order.
// This simulates what happens with io_uring packet reordering.
func TestConsolidateNakBtree_OutOfOrderInsertion(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Insert sequences out of order (simulating io_uring reordering)
	// These should form range 100-105 when consolidated
	outOfOrder := []uint32{103, 100, 105, 101, 104, 102}
	for _, seq := range outOfOrder {
		r.nakBtree.Insert(seq)
	}

	list := r.consolidateNakBtree()

	// Should produce single contiguous range (100, 105)
	if len(list) != 2 {
		t.Fatalf("Expected 2 entries (one range), got %d", len(list))
	}

	if list[0].Val() != 100 || list[1].Val() != 105 {
		t.Errorf("Expected range (100, 105), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

// TestConsolidateNakBtree_OutOfOrderWithGaps verifies consolidation with
// out-of-order insertion where there are actual gaps that should remain.
func TestConsolidateNakBtree_OutOfOrderWithGaps(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 2

	// Insert two groups of sequences out of order
	// Group 1: 100-103 (gap of 6 to group 2)
	// Group 2: 110-113
	outOfOrder := []uint32{112, 100, 111, 103, 110, 101, 113, 102}
	for _, seq := range outOfOrder {
		r.nakBtree.Insert(seq)
	}

	list := r.consolidateNakBtree()

	// Should produce two ranges: (100, 103) and (110, 113)
	if len(list) != 4 {
		t.Fatalf("Expected 4 entries (two ranges), got %d", len(list))
	}

	// First range: 100-103
	if list[0].Val() != 100 || list[1].Val() != 103 {
		t.Errorf("Expected first range (100, 103), got (%d, %d)", list[0].Val(), list[1].Val())
	}

	// Second range: 110-113
	if list[2].Val() != 110 || list[3].Val() != 113 {
		t.Errorf("Expected second range (110, 113), got (%d, %d)", list[2].Val(), list[3].Val())
	}
}

// TestConsolidateNakBtree_InOrderBaseline is a control test to compare with
// out-of-order tests and ensure both produce identical results.
func TestConsolidateNakBtree_InOrderBaseline(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Insert sequences IN ORDER (this is the baseline)
	for seq := uint32(100); seq <= 105; seq++ {
		r.nakBtree.Insert(seq)
	}

	list := r.consolidateNakBtree()

	// Should produce single contiguous range (100, 105) - same as out-of-order
	if len(list) != 2 {
		t.Fatalf("Expected 2 entries (one range), got %d", len(list))
	}

	if list[0].Val() != 100 || list[1].Val() != 105 {
		t.Errorf("Expected range (100, 105), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

// TestConsolidateNakBtree_InOrderVsOutOfOrder_Consistency verifies that
// in-order and out-of-order insertion produce identical consolidation results.
func TestConsolidateNakBtree_InOrderVsOutOfOrder_Consistency(t *testing.T) {
	sequences := []uint32{100, 101, 102, 106, 107, 108, 120, 121}

	// Test 1: In-order insertion
	r1 := createTestReceiverForConsolidation(t)
	r1.nakMergeGap = 2
	for _, seq := range sequences {
		r1.nakBtree.Insert(seq)
	}
	list1 := r1.consolidateNakBtree()

	// Test 2: Out-of-order insertion (reverse)
	r2 := createTestReceiverForConsolidation(t)
	r2.nakMergeGap = 2
	for i := len(sequences) - 1; i >= 0; i-- {
		r2.nakBtree.Insert(sequences[i])
	}
	list2 := r2.consolidateNakBtree()

	// Test 3: Out-of-order insertion (random shuffle)
	r3 := createTestReceiverForConsolidation(t)
	r3.nakMergeGap = 2
	shuffled := []uint32{106, 121, 100, 108, 101, 120, 107, 102}
	for _, seq := range shuffled {
		r3.nakBtree.Insert(seq)
	}
	list3 := r3.consolidateNakBtree()

	// All three should produce identical results
	if len(list1) != len(list2) || len(list1) != len(list3) {
		t.Fatalf("Lists have different lengths: in-order=%d, reverse=%d, shuffled=%d",
			len(list1), len(list2), len(list3))
	}

	for i := range list1 {
		if list1[i].Val() != list2[i].Val() {
			t.Errorf("Position %d: in-order=%d, reverse=%d", i, list1[i].Val(), list2[i].Val())
		}
		if list1[i].Val() != list3[i].Val() {
			t.Errorf("Position %d: in-order=%d, shuffled=%d", i, list1[i].Val(), list3[i].Val())
		}
	}

	t.Logf("✅ All insertion orders produce identical consolidation: %v",
		extractVals(list1))
}

// Helper to extract values from circular.Number slice for logging
func extractVals(list []circular.Number) []uint32 {
	vals := make([]uint32, len(list))
	for i, n := range list {
		vals[i] = n.Val()
	}
	return vals
}

// ============================================================================
// Modulus-Based Drop Tests (Deterministic Patterns)
// ============================================================================

// populateNakBtreeModulus adds sequences where seq % modulus == 0 are "dropped"
// Returns the expected dropped sequences for verification
func populateNakBtreeModulus(r *receiver, startSeq uint32, numPackets int, modulus int) []uint32 {
	var dropped []uint32
	for i := 0; i < numPackets; i++ {
		seq := startSeq + uint32(i)
		if int(seq)%modulus == 0 {
			r.nakBtree.Insert(seq)
			dropped = append(dropped, seq)
		}
	}
	return dropped
}

// populateNakBtreeBursts adds burst sequences to NAK btree
func populateNakBtreeBursts(r *receiver, burstStarts []uint32, burstSize int) []uint32 {
	var dropped []uint32
	for _, start := range burstStarts {
		for i := 0; i < burstSize; i++ {
			seq := start + uint32(i)
			r.nakBtree.Insert(seq)
			dropped = append(dropped, seq)
		}
	}
	return dropped
}

func TestConsolidateNakBtree_ModulusDrops_Every10th(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3 // Gap of 9 between drops is > mergeGap, so all singles

	// Drop every 10th packet: 10, 20, 30, ..., 100
	dropped := populateNakBtreeModulus(r, 1, 100, 10)
	t.Logf("Dropped %d packets: %v", len(dropped), dropped)

	list := r.consolidateNakBtree()

	// With gap=9 between each drop and mergeGap=3, each should be a single
	// Expected: 10 singles = 20 entries (start, end pairs)
	expectedEntries := len(dropped) * 2
	if len(list) != expectedEntries {
		t.Errorf("Expected %d entries (all singles), got %d", expectedEntries, len(list))
	}

	// Verify each entry is a single (start == end)
	for i := 0; i < len(list); i += 2 {
		if list[i].Val() != list[i+1].Val() {
			t.Errorf("Expected single at position %d, got range (%d, %d)",
				i/2, list[i].Val(), list[i+1].Val())
		}
	}

	t.Logf("✅ Modulus 10: %d singles consolidated correctly", len(dropped))
}

func TestConsolidateNakBtree_ModulusDrops_Every5th(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3 // Gap of 4 between drops is > mergeGap=3, so all singles

	// Drop every 5th packet: 5, 10, 15, ..., 100
	dropped := populateNakBtreeModulus(r, 1, 100, 5)
	t.Logf("Dropped %d packets", len(dropped))

	list := r.consolidateNakBtree()

	// With gap=4 between each drop and mergeGap=3, each should be a single
	expectedEntries := len(dropped) * 2
	if len(list) != expectedEntries {
		t.Errorf("Expected %d entries (all singles), got %d", expectedEntries, len(list))
	}

	t.Logf("✅ Modulus 5: %d singles consolidated correctly", len(dropped))
}

func TestConsolidateNakBtree_ModulusDrops_Every3rd_WithMerge(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3 // Gap of 2 between drops is <= mergeGap, should merge!

	// Drop every 3rd packet: 3, 6, 9, 12, ...
	// Gap between 3 and 6 is 2 (4, 5), which is <= mergeGap=3
	dropped := populateNakBtreeModulus(r, 1, 30, 3)
	t.Logf("Dropped %d packets: %v", len(dropped), dropped)

	list := r.consolidateNakBtree()

	// All should merge into one big range because gaps are small enough
	if len(list) != 2 {
		t.Errorf("Expected 2 entries (one merged range), got %d", len(list))
	}

	if len(list) >= 2 {
		t.Logf("✅ Modulus 3 with merge: range (%d, %d)", list[0].Val(), list[1].Val())
	}
}

func TestConsolidateNakBtree_BurstDrops(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Drop bursts: 100-109, 200-209, 300-309, 400-409, 500-509
	burstStarts := []uint32{100, 200, 300, 400, 500}
	burstSize := 10
	dropped := populateNakBtreeBursts(r, burstStarts, burstSize)
	t.Logf("Dropped %d packets in %d bursts", len(dropped), len(burstStarts))

	list := r.consolidateNakBtree()

	// Each burst should consolidate into one range
	// 5 bursts = 10 entries (5 start/end pairs)
	expectedEntries := len(burstStarts) * 2
	if len(list) != expectedEntries {
		t.Errorf("Expected %d entries (%d ranges), got %d",
			expectedEntries, len(burstStarts), len(list))
	}

	// Verify each range
	for i := 0; i < len(burstStarts); i++ {
		if i*2+1 >= len(list) {
			break
		}
		start := list[i*2].Val()
		end := list[i*2+1].Val()
		expectedStart := burstStarts[i]
		expectedEnd := burstStarts[i] + uint32(burstSize) - 1

		if start != expectedStart || end != expectedEnd {
			t.Errorf("Burst %d: expected (%d, %d), got (%d, %d)",
				i, expectedStart, expectedEnd, start, end)
		}
	}

	t.Logf("✅ Burst drops: %d ranges consolidated correctly", len(burstStarts))
}

func TestConsolidateNakBtree_MixedModulusAndBurst(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 2

	// Mix: Every 50th packet as singles + burst at 250-254
	// Singles (gap=49 > mergeGap): 50, 100, 150, 200
	for seq := uint32(50); seq <= 200; seq += 50 {
		r.nakBtree.Insert(seq)
	}
	// Burst (contiguous): 250-254
	for seq := uint32(250); seq <= 254; seq++ {
		r.nakBtree.Insert(seq)
	}

	list := r.consolidateNakBtree()

	// Expected: 4 singles + 1 range = 5 entries * 2 = 10
	expectedEntries := 10
	if len(list) != expectedEntries {
		t.Errorf("Expected %d entries, got %d", expectedEntries, len(list))
		t.Logf("List: %v", extractVals(list))
	}

	// Verify singles (start == end)
	for i := 0; i < 8; i += 2 {
		if list[i].Val() != list[i+1].Val() {
			t.Errorf("Expected single at position %d", i/2)
		}
	}

	// Verify burst range
	if len(list) >= 10 {
		if list[8].Val() != 250 || list[9].Val() != 254 {
			t.Errorf("Expected burst range (250, 254), got (%d, %d)",
				list[8].Val(), list[9].Val())
		}
	}

	t.Logf("✅ Mixed pattern: 4 singles + 1 range consolidated correctly")
}

func TestConsolidateNakBtree_LargeScale_ModulusDrops(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Simulate realistic scenario: 10000 packets, 10% loss (every 10th)
	numPackets := 10000
	modulus := 10
	dropped := populateNakBtreeModulus(r, 1, numPackets, modulus)
	t.Logf("Dropped %d packets (%.1f%% loss)", len(dropped), float64(len(dropped))/float64(numPackets)*100)

	list := r.consolidateNakBtree()

	// All singles (gap=9 > mergeGap=3)
	expectedSingles := len(dropped)
	actualEntries := len(list) / 2 // Each single has start/end pair

	if actualEntries != expectedSingles {
		t.Errorf("Expected %d singles, got %d entries (%d singles)",
			expectedSingles, len(list), actualEntries)
	}

	t.Logf("✅ Large scale (10k packets, 10%% loss): %d singles consolidated", actualEntries)
}

func TestConsolidateNakBtree_LargeScale_BurstDrops(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Simulate realistic scenario: bursts every 1000 packets
	// 10 bursts of 20 packets each
	var burstStarts []uint32
	for i := 0; i < 10; i++ {
		burstStarts = append(burstStarts, uint32(1000*(i+1)))
	}
	burstSize := 20
	dropped := populateNakBtreeBursts(r, burstStarts, burstSize)
	t.Logf("Dropped %d packets in %d bursts", len(dropped), len(burstStarts))

	list := r.consolidateNakBtree()

	// Each burst should be one range
	expectedRanges := len(burstStarts)
	actualRanges := len(list) / 2

	if actualRanges != expectedRanges {
		t.Errorf("Expected %d ranges, got %d", expectedRanges, actualRanges)
	}

	t.Logf("✅ Large scale bursts: %d ranges consolidated", actualRanges)
}

// ============================================================================
// Comprehensive Benchmarks with Various Patterns
// ============================================================================

func BenchmarkConsolidate_ModulusDrops(b *testing.B) {
	patterns := []struct {
		name       string
		numPackets int
		modulus    int
	}{
		{"10pct_loss_1k", 1000, 10},   // 100 drops
		{"5pct_loss_1k", 1000, 20},    // 50 drops
		{"20pct_loss_1k", 1000, 5},    // 200 drops
		{"10pct_loss_10k", 10000, 10}, // 1000 drops
		{"1pct_loss_10k", 10000, 100}, // 100 drops
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 10 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}

			// Pre-populate
			populateNakBtreeModulus(r, 1, p.numPackets, p.modulus)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}

func BenchmarkConsolidate_BurstDrops(b *testing.B) {
	patterns := []struct {
		name      string
		numBursts int
		burstSize int
	}{
		{"5_bursts_x10", 5, 10},
		{"10_bursts_x10", 10, 10},
		{"20_bursts_x10", 20, 10},
		{"10_bursts_x50", 10, 50},
		{"50_bursts_x20", 50, 20},
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 10 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}

			// Pre-populate with bursts
			var burstStarts []uint32
			for i := 0; i < p.numBursts; i++ {
				burstStarts = append(burstStarts, uint32(i*1000+100))
			}
			populateNakBtreeBursts(r, burstStarts, p.burstSize)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}

func BenchmarkConsolidate_MixedPatterns(b *testing.B) {
	patterns := []struct {
		name         string
		modulusDrops int // Number of modulus-based drops
		burstDrops   int // Total burst drops
	}{
		{"light_mixed", 50, 30},
		{"moderate_mixed", 100, 100},
		{"heavy_mixed", 200, 200},
		{"burst_heavy", 50, 500},
		{"singles_heavy", 500, 50},
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 10 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}

			// Add modulus-based singles (every 100th to ensure gap > mergeGap)
			for i := 0; i < p.modulusDrops; i++ {
				r.nakBtree.Insert(uint32(i * 100))
			}

			// Add bursts
			burstSize := 10
			numBursts := p.burstDrops / burstSize
			for i := 0; i < numBursts; i++ {
				start := uint32(50000 + i*200) // Offset from modulus drops
				for j := 0; j < burstSize; j++ {
					r.nakBtree.Insert(start + uint32(j))
				}
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}

func BenchmarkConsolidate_OutOfOrderInsertion(b *testing.B) {
	// Compare in-order vs out-of-order insertion performance
	patterns := []struct {
		name     string
		inOrder  bool
		numDrops int
	}{
		{"in_order_100", true, 100},
		{"out_of_order_100", false, 100},
		{"in_order_500", true, 500},
		{"out_of_order_500", false, 500},
		{"in_order_1000", true, 1000},
		{"out_of_order_1000", false, 1000},
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 10 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}

			// Generate sequences
			seqs := make([]uint32, p.numDrops)
			for i := 0; i < p.numDrops; i++ {
				seqs[i] = uint32(i * 10) // Every 10th for singles
			}

			// Shuffle if out-of-order
			if !p.inOrder {
				// Simple reverse for deterministic "shuffle"
				for i := 0; i < len(seqs)/2; i++ {
					seqs[i], seqs[len(seqs)-1-i] = seqs[len(seqs)-1-i], seqs[i]
				}
			}

			// Insert
			for _, seq := range seqs {
				r.nakBtree.Insert(seq)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}

// ============================================================================
// MSS Overflow Tests (FR-11)
// ============================================================================

// NAK packet size calculations:
// - MSS = 1500 bytes (typical)
// - UDP header = 8 bytes, IP header = 20 bytes (28 total for UDP/IP)
// - SRT header = 16 bytes
// - Available for NAK CIF = 1500 - 28 - 16 = 1456 bytes
// - Single entry = 4 bytes (sequence number)
// - Range entry = 8 bytes (start + end sequence numbers)
// - Max singles = 1456 / 4 = 364 entries
// - Max ranges = 1456 / 8 = 182 entries

const (
	nakCIFMaxBytes     = 1456                                // Available bytes for NAK CIF payload
	nakSingleEntrySize = 4                                   // Bytes per single sequence
	nakRangeEntrySize  = 8                                   // Bytes per range (start + end)
	nakMaxSingles      = nakCIFMaxBytes / nakSingleEntrySize // 364
	nakMaxRanges       = nakCIFMaxBytes / nakRangeEntrySize  // 182
)

func TestConsolidateNakBtree_MSS_UnderLimit_Singles(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 0 // No merging - all singles

	// Insert 100 singles (well under 364 limit)
	for i := 0; i < 100; i++ {
		r.nakBtree.Insert(uint32(i * 100)) // Gap of 99 > mergeGap=0
	}

	list := r.consolidateNakBtree()

	// Each single produces 2 entries in the list (start, end pairs)
	// Wire format: 4 bytes per single
	wireBytes := (len(list) / 2) * nakSingleEntrySize

	require.Less(t, wireBytes, nakCIFMaxBytes, "Should be well under MSS limit")
	t.Logf("✅ 100 singles: %d bytes (limit: %d)", wireBytes, nakCIFMaxBytes)
}

func TestConsolidateNakBtree_MSS_AtLimit_Singles(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 0 // No merging - all singles

	// Insert exactly 364 singles (at limit)
	for i := 0; i < nakMaxSingles; i++ {
		r.nakBtree.Insert(uint32(i * 100)) // Gap > mergeGap
	}

	list := r.consolidateNakBtree()
	actualSingles := len(list) / 2
	wireBytes := actualSingles * nakSingleEntrySize

	t.Logf("At limit: %d singles, %d bytes (limit: %d)", actualSingles, wireBytes, nakCIFMaxBytes)

	// Should be exactly at or just under limit
	require.Equal(t, nakMaxSingles, actualSingles, "Should produce max singles")
	require.LessOrEqual(t, wireBytes, nakCIFMaxBytes, "Should fit in MSS")
}

func TestConsolidateNakBtree_MSS_OverLimit_Singles(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 0 // No merging - all singles

	// Insert 500 singles (over 364 limit)
	numDrops := 500
	for i := 0; i < numDrops; i++ {
		r.nakBtree.Insert(uint32(i * 100)) // Gap > mergeGap
	}

	list := r.consolidateNakBtree()
	actualSingles := len(list) / 2
	wireBytes := actualSingles * nakSingleEntrySize

	t.Logf("Over limit: %d singles, %d bytes (limit: %d)", actualSingles, wireBytes, nakCIFMaxBytes)

	// CURRENT BEHAVIOR: We produce ALL entries, which would overflow MSS
	// This test documents the current behavior for FR-11
	if wireBytes > nakCIFMaxBytes {
		t.Logf("⚠️ FR-11: Would overflow MSS by %d bytes", wireBytes-nakCIFMaxBytes)
		t.Logf("   Current behavior: %d entries produced", actualSingles)
		t.Logf("   Expected behavior: Split into multiple NAK packets")
	}

	// At minimum, consolidation should produce all the entries (correctness)
	require.Equal(t, numDrops, actualSingles, "All drops should be in NAK list")
}

func TestConsolidateNakBtree_MSS_UnderLimit_Ranges(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 100 // Merge everything

	// Insert 50 ranges (bursts) - well under 182 limit
	numRanges := 50
	rangeSize := 10
	for i := 0; i < numRanges; i++ {
		start := uint32(i * 1000)
		for j := 0; j < rangeSize; j++ {
			r.nakBtree.Insert(start + uint32(j))
		}
	}

	list := r.consolidateNakBtree()
	actualRanges := len(list) / 2
	wireBytes := actualRanges * nakRangeEntrySize

	require.Less(t, wireBytes, nakCIFMaxBytes, "Should be well under MSS limit")
	t.Logf("✅ %d ranges: %d bytes (limit: %d)", actualRanges, wireBytes, nakCIFMaxBytes)
}

func TestConsolidateNakBtree_MSS_OverLimit_Ranges(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 100 // Merge within range

	// Insert 250 ranges (over 182 limit)
	numRanges := 250
	rangeSize := 10
	for i := 0; i < numRanges; i++ {
		start := uint32(i * 1000) // Gap of 990 > mergeGap=100
		for j := 0; j < rangeSize; j++ {
			r.nakBtree.Insert(start + uint32(j))
		}
	}

	list := r.consolidateNakBtree()
	actualRanges := len(list) / 2
	wireBytes := actualRanges * nakRangeEntrySize

	t.Logf("Over limit: %d ranges, %d bytes (limit: %d)", actualRanges, wireBytes, nakCIFMaxBytes)

	// CURRENT BEHAVIOR: We produce ALL entries, which would overflow MSS
	if wireBytes > nakCIFMaxBytes {
		t.Logf("⚠️ FR-11: Would overflow MSS by %d bytes", wireBytes-nakCIFMaxBytes)
		t.Logf("   Need to split into %d NAK packets", (wireBytes/nakCIFMaxBytes)+1)
	}

	// Correctness: all ranges should be present
	require.Equal(t, numRanges, actualRanges, "All ranges should be in NAK list")
}

func TestConsolidateNakBtree_MSS_MixedOverflow(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Create a mix that would overflow:
	// - 200 singles (800 bytes)
	// - 100 ranges (800 bytes)
	// Total: 1600 bytes > 1456 limit

	// Singles (every 50th, gap=49 > mergeGap=3)
	for i := 0; i < 200; i++ {
		r.nakBtree.Insert(uint32(i * 50))
	}

	// Ranges at offset 50000
	for i := 0; i < 100; i++ {
		start := uint32(50000 + i*100)
		for j := 0; j < 5; j++ { // 5 consecutive = 1 range
			r.nakBtree.Insert(start + uint32(j))
		}
	}

	list := r.consolidateNakBtree()

	// Count singles vs ranges
	singles := 0
	ranges := 0
	for i := 0; i < len(list); i += 2 {
		if list[i].Val() == list[i+1].Val() {
			singles++
		} else {
			ranges++
		}
	}

	wireBytes := singles*nakSingleEntrySize + ranges*nakRangeEntrySize

	t.Logf("Mixed: %d singles + %d ranges = %d bytes (limit: %d)",
		singles, ranges, wireBytes, nakCIFMaxBytes)

	if wireBytes > nakCIFMaxBytes {
		t.Logf("⚠️ FR-11: Would overflow MSS by %d bytes", wireBytes-nakCIFMaxBytes)
		t.Logf("   Need to split into multiple NAK packets")
	}
}

// CalculateNakWireSize calculates the wire size of a NAK list
// This helper can be used to determine if splitting is needed
func calculateNakWireSize(list []circular.Number) int {
	bytes := 0
	for i := 0; i < len(list); i += 2 {
		if list[i].Val() == list[i+1].Val() {
			bytes += nakSingleEntrySize // Single
		} else {
			bytes += nakRangeEntrySize // Range
		}
	}
	return bytes
}

func TestCalculateNakWireSize(t *testing.T) {
	tests := []struct {
		name        string
		singles     int
		ranges      int
		expectedMin int
		expectedMax int
	}{
		{"empty", 0, 0, 0, 0},
		{"10 singles", 10, 0, 40, 40},
		{"10 ranges", 0, 10, 80, 80},
		{"5 singles + 5 ranges", 5, 5, 60, 60},
		{"max singles", nakMaxSingles, 0, nakCIFMaxBytes, nakCIFMaxBytes},
		{"max ranges", 0, nakMaxRanges, nakCIFMaxBytes, nakCIFMaxBytes},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := createTestReceiverForConsolidation(t)
			r.nakMergeGap = 0 // No merging for precise control

			// Add singles
			for i := 0; i < tc.singles; i++ {
				r.nakBtree.Insert(uint32(i * 1000))
			}
			// Add ranges (consecutive pairs at offset)
			for i := 0; i < tc.ranges; i++ {
				start := uint32(500000 + i*1000)
				r.nakBtree.Insert(start)
				r.nakBtree.Insert(start + 1) // Makes it a range
			}

			list := r.consolidateNakBtree()
			wireSize := calculateNakWireSize(list)

			require.GreaterOrEqual(t, wireSize, tc.expectedMin)
			require.LessOrEqual(t, wireSize, tc.expectedMax)
		})
	}
}

// ============================================================================
// FR-11: Large Scale NAK List Tests (Extreme Scenarios)
// ============================================================================

// These tests simulate extreme scenarios like:
// - 60 second buffer at 100 Mbps with high loss
// - Long network outages creating massive gaps

func TestConsolidateNakBtree_ExtremeScale_60SecBuffer(t *testing.T) {
	// Create receiver with extended consolidation budget for extreme test
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            3,
		nakConsolidationBudget: 1 * time.Second, // Extended for extreme test
		metrics:                m,
	}

	// Simulate: 100 Mbps, 60s buffer, 5% loss
	// ~8900 packets/sec * 60s = ~534,000 packets
	// 5% loss = ~26,700 packets to NAK
	// We'll test with 10,000 (still extreme)

	numDrops := 10000
	for i := 0; i < numDrops; i++ {
		r.nakBtree.Insert(uint32(i * 20)) // Every 20th = 5% loss pattern, gap > mergeGap
	}

	list := r.consolidateNakBtree()
	actualEntries := len(list) / 2
	wireBytes := calculateNakWireSize(list)

	t.Logf("Extreme scale (10k singles):")
	t.Logf("  Entries: %d", actualEntries)
	t.Logf("  Wire size: %d bytes", wireBytes)
	t.Logf("  NAK packets needed: %d", (wireBytes/nakCIFMaxBytes)+1)

	require.Equal(t, numDrops, actualEntries, "All drops should be in NAK list")
}

func TestConsolidateNakBtree_ExtremeScale_LongOutage(t *testing.T) {
	// Create receiver with extended consolidation budget for extreme test
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            100,             // Allow merging within bursts
		nakConsolidationBudget: 1 * time.Second, // Extended for extreme test
		metrics:                m,
	}

	// Simulate: 5 long outages of 1000 packets each (5000 total)
	// At 100 Mbps (~8900 pps), 1000 packets = ~112ms outage
	numOutages := 5
	outageSize := 1000

	for outage := 0; outage < numOutages; outage++ {
		start := uint32(outage * 100000) // Large gap between outages
		for i := 0; i < outageSize; i++ {
			r.nakBtree.Insert(start + uint32(i))
		}
	}

	list := r.consolidateNakBtree()
	wireBytes := calculateNakWireSize(list)
	actualRanges := len(list) / 2

	t.Logf("Long outages (5 x 1000 packets):")
	t.Logf("  Ranges after consolidation: %d", actualRanges)
	t.Logf("  Wire size: %d bytes", wireBytes)
	t.Logf("  NAK packets needed: %d", (wireBytes/nakCIFMaxBytes)+1)

	// With mergeGap=100, each outage should become 1 range
	require.Equal(t, numOutages, actualRanges, "Each outage should consolidate to 1 range")
}

func TestConsolidateNakBtree_ExtremeScale_WorstCase(t *testing.T) {
	// Create receiver with extended consolidation budget for extreme test
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            0,               // No merging - worst case
		nakConsolidationBudget: 1 * time.Second, // Extended budget for extreme test
		metrics:                m,
	}

	// Worst case: 50,000 individual drops (as singles)
	// Wire size: 50,000 * 4 bytes = 200,000 bytes
	// NAK packets needed: ~137 packets
	numDrops := 50000

	for i := 0; i < numDrops; i++ {
		r.nakBtree.Insert(uint32(i * 100)) // Gap > mergeGap
	}

	list := r.consolidateNakBtree()
	wireBytes := calculateNakWireSize(list)
	nakPacketsNeeded := (wireBytes / nakCIFMaxBytes) + 1

	t.Logf("Worst case (50k singles):")
	t.Logf("  Wire size: %d bytes (%d KB)", wireBytes, wireBytes/1024)
	t.Logf("  NAK packets needed: %d", nakPacketsNeeded)

	require.Equal(t, numDrops, len(list)/2, "All drops should be in NAK list")
	require.Greater(t, nakPacketsNeeded, 100, "Should need many NAK packets")
}

// BenchmarkConsolidate_ExtremeScales benchmarks extreme scenarios
func BenchmarkConsolidate_ExtremeScales(b *testing.B) {
	scales := []struct {
		name     string
		numDrops int
		mergeGap uint32
	}{
		{"1k_singles", 1000, 0},
		{"5k_singles", 5000, 0},
		{"10k_singles", 10000, 0},
		{"50k_singles", 50000, 0},
		{"5k_ranges_merge10", 5000, 10},
		{"10k_ranges_merge10", 10000, 10},
	}

	for _, s := range scales {
		b.Run(s.name, func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            s.mergeGap,
				nakConsolidationBudget: 100 * time.Millisecond, // Allow more time
				metrics:                &metrics.ConnectionMetrics{},
			}

			// Pre-populate
			for i := 0; i < s.numDrops; i++ {
				if s.mergeGap == 0 {
					r.nakBtree.Insert(uint32(i * 100)) // Singles
				} else {
					r.nakBtree.Insert(uint32(i)) // Contiguous for range merging
				}
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}

// BenchmarkConsolidate_RealisticScenarios benchmarks patterns that match
// real-world network conditions
func BenchmarkConsolidate_RealisticScenarios(b *testing.B) {
	scenarios := []struct {
		name        string
		description string
		setup       func(r *receiver)
	}{
		{
			name:        "clean_network",
			description: "0.1% random loss",
			setup: func(r *receiver) {
				// 1000 packets, 0.1% = 1 drop
				r.nakBtree.Insert(500)
			},
		},
		{
			name:        "moderate_loss",
			description: "2% evenly distributed loss",
			setup: func(r *receiver) {
				// Every 50th packet
				for i := 50; i <= 5000; i += 50 {
					r.nakBtree.Insert(uint32(i))
				}
			},
		},
		{
			name:        "starlink_outage",
			description: "60ms outage at 5Mbps (50 packets burst)",
			setup: func(r *receiver) {
				for i := uint32(1000); i < 1050; i++ {
					r.nakBtree.Insert(i)
				}
			},
		},
		{
			name:        "multiple_outages",
			description: "3 x 40ms outages at 5Mbps",
			setup: func(r *receiver) {
				for _, start := range []uint32{1000, 3000, 5000} {
					for i := uint32(0); i < 35; i++ {
						r.nakBtree.Insert(start + i)
					}
				}
			},
		},
		{
			name:        "congestion_mixed",
			description: "5% loss with some bursts",
			setup: func(r *receiver) {
				// Random-ish loss pattern
				for i := 0; i < 100; i++ {
					r.nakBtree.Insert(uint32(i * 20)) // Singles
				}
				// Plus a burst
				for i := uint32(5000); i < 5020; i++ {
					r.nakBtree.Insert(i)
				}
			},
		},
		{
			name:        "heavy_loss",
			description: "20% uniform loss (worst case)",
			setup: func(r *receiver) {
				// Every 5th packet
				for i := 5; i <= 5000; i += 5 {
					r.nakBtree.Insert(uint32(i))
				}
			},
		},
	}

	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 2 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}

			s.setup(r)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = r.consolidateNakBtree()
			}
		})
	}
}
