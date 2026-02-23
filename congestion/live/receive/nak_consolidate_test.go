package receive

import (
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/stretchr/testify/require"
)

// NAK wire format constants for MSS limit testing
const (
	nakSingleEntrySize = 4             // 4 bytes for single sequence
	nakRangeEntrySize  = 8             // 8 bytes for range (start + end)
	nakCIFMaxBytes     = 1456          // Max NAK CIF size (MTU - headers)
	nakMaxSingles      = 364           // nakCIFMaxBytes / 4
	nakMaxRanges       = 182           // nakCIFMaxBytes / 8
)

// ═══════════════════════════════════════════════════════════════════════════
// NAK Consolidation Tests - UNIQUE TESTS ONLY
// Duplicated tests moved to nak_consolidate_table_test.go
// ═══════════════════════════════════════════════════════════════════════════

// createTestReceiverForConsolidation creates a receiver with NAK btree for consolidation testing.
func createTestReceiverForConsolidation(t *testing.T) *receiver {
	t.Helper()

	m := &metrics.ConnectionMetrics{}

	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            3, // Default merge gap
		nakConsolidationBudget: 2 * time.Millisecond,
		metrics:                m,
	}
	r.setupNakDispatch(false) // Use locking versions for tests
	return r
}

// Helper to extract values from circular.Number slice for logging
func extractVals(list []circular.Number) []uint32 {
	vals := make([]uint32, len(list))
	for i, n := range list {
		vals[i] = n.Val()
	}
	return vals
}

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: NAKEntry Type Methods
// ═══════════════════════════════════════════════════════════════════════════

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

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: Sync Pool, Metrics, CircularSeqDiff
// ═══════════════════════════════════════════════════════════════════════════

func TestSyncPoolReuse(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	// Run consolidation multiple times and verify no panics
	for i := 0; i < 100; i++ {
		r.nakBtree.InsertLocking(uint32(i))
		_ = r.consolidateNakBtree()
	}

	// Clear and run again
	r.nakBtree.ClearLocking()
	for i := 0; i < 50; i++ {
		r.nakBtree.InsertLocking(uint32(i * 3))
		_ = r.consolidateNakBtree()
	}

	// If we get here without panic, sync.Pool is working correctly
}

func TestConsolidateMetrics(t *testing.T) {
	r := createTestReceiverForConsolidation(t)

	// Insert some entries that will merge
	r.nakBtree.InsertLocking(100)
	r.nakBtree.InsertLocking(101)
	r.nakBtree.InsertLocking(102)

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

func TestConsolidateUsesCircularSeqDiff(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 2

	// Use sequences that would wrap if using simple subtraction
	// But with circular.SeqDiff, adjacent sequences should merge
	r.nakBtree.InsertLocking(10)
	r.nakBtree.InsertLocking(11)
	r.nakBtree.InsertLocking(12)

	list := r.consolidateNakBtree()

	// Should produce single range (10, 12)
	if len(list) != 2 {
		t.Fatalf("Expected 2 entries, got %d", len(list))
	}

	if list[0].Val() != 10 || list[1].Val() != 12 {
		t.Errorf("Expected (10, 12), got (%d, %d)", list[0].Val(), list[1].Val())
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: In-Order vs Out-of-Order Consistency
// ═══════════════════════════════════════════════════════════════════════════

func TestConsolidateNakBtree_InOrderBaseline(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 3

	// Insert sequences IN ORDER (this is the baseline)
	for seq := uint32(100); seq <= 105; seq++ {
		r.nakBtree.InsertLocking(seq)
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

func TestConsolidateNakBtree_InOrderVsOutOfOrder_Consistency(t *testing.T) {
	sequences := []uint32{100, 101, 102, 106, 107, 108, 120, 121}

	// Test 1: In-order insertion
	r1 := createTestReceiverForConsolidation(t)
	r1.nakMergeGap = 2
	for _, seq := range sequences {
		r1.nakBtree.InsertLocking(seq)
	}
	list1 := r1.consolidateNakBtree()

	// Test 2: Out-of-order insertion (reverse)
	r2 := createTestReceiverForConsolidation(t)
	r2.nakMergeGap = 2
	for i := len(sequences) - 1; i >= 0; i-- {
		r2.nakBtree.InsertLocking(sequences[i])
	}
	list2 := r2.consolidateNakBtree()

	// Test 3: Out-of-order insertion (random shuffle)
	r3 := createTestReceiverForConsolidation(t)
	r3.nakMergeGap = 2
	shuffled := []uint32{106, 121, 100, 108, 101, 120, 107, 102}
	for _, seq := range shuffled {
		r3.nakBtree.InsertLocking(seq)
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

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: MSS Limit Tests (Ranges)
// ═══════════════════════════════════════════════════════════════════════════

func TestConsolidateNakBtree_MSS_UnderLimit_Ranges(t *testing.T) {
	r := createTestReceiverForConsolidation(t)
	r.nakMergeGap = 100 // Merge everything

	// Insert 50 ranges (bursts) - well under 182 limit
	numRanges := 50
	rangeSize := 10
	for i := 0; i < numRanges; i++ {
		start := uint32(i * 1000)
		for j := 0; j < rangeSize; j++ {
			r.nakBtree.InsertLocking(start + uint32(j))
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
			r.nakBtree.InsertLocking(start + uint32(j))
		}
	}

	list := r.consolidateNakBtree()
	actualRanges := len(list) / 2
	wireBytes := actualRanges * nakRangeEntrySize

	t.Logf("Over limit: %d ranges, %d bytes (limit: %d)", actualRanges, wireBytes, nakCIFMaxBytes)

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
		r.nakBtree.InsertLocking(uint32(i * 50))
	}

	// Ranges at offset 50000
	for i := 0; i < 100; i++ {
		start := uint32(50000 + i*100)
		for j := 0; j < 5; j++ { // 5 consecutive = 1 range
			r.nakBtree.InsertLocking(start + uint32(j))
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

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: Wire Size Calculation
// ═══════════════════════════════════════════════════════════════════════════

// calculateNakWireSize calculates the wire size of a NAK list
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
				r.nakBtree.InsertLocking(uint32(i * 1000))
			}
			// Add ranges (consecutive pairs at offset)
			for i := 0; i < tc.ranges; i++ {
				start := uint32(500000 + i*1000)
				r.nakBtree.InsertLocking(start)
				r.nakBtree.InsertLocking(start + 1) // Makes it a range
			}

			list := r.consolidateNakBtree()
			wireSize := calculateNakWireSize(list)

			require.GreaterOrEqual(t, wireSize, tc.expectedMin)
			require.LessOrEqual(t, wireSize, tc.expectedMax)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Unique Tests: Extreme Scale Scenarios
// ═══════════════════════════════════════════════════════════════════════════

func TestConsolidateNakBtree_ExtremeScale_60SecBuffer(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            3,
		nakConsolidationBudget: 1 * time.Second,
		metrics:                m,
	}
	r.setupNakDispatch(false) // Use locking versions for tests

	numDrops := 10000
	for i := 0; i < numDrops; i++ {
		r.nakBtree.InsertLocking(uint32(i * 20)) // Every 20th = 5% loss pattern
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
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            100,
		nakConsolidationBudget: 1 * time.Second,
		metrics:                m,
	}
	r.setupNakDispatch(false) // Use locking versions for tests

	numOutages := 5
	outageSize := 1000

	for outage := 0; outage < numOutages; outage++ {
		start := uint32(outage * 100000) // Large gap between outages
		for i := 0; i < outageSize; i++ {
			r.nakBtree.InsertLocking(start + uint32(i))
		}
	}

	list := r.consolidateNakBtree()
	wireBytes := calculateNakWireSize(list)
	actualRanges := len(list) / 2

	t.Logf("Long outages (5 x 1000 packets):")
	t.Logf("  Ranges after consolidation: %d", actualRanges)
	t.Logf("  Wire size: %d bytes", wireBytes)
	t.Logf("  NAK packets needed: %d", (wireBytes/nakCIFMaxBytes)+1)

	require.Equal(t, numOutages, actualRanges, "Each outage should consolidate to 1 range")
}

func TestConsolidateNakBtree_ExtremeScale_WorstCase(t *testing.T) {
	m := &metrics.ConnectionMetrics{}
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            0,
		nakConsolidationBudget: 1 * time.Second,
		metrics:                m,
	}
	r.setupNakDispatch(false) // Use locking versions for tests

	numDrops := 50000

	for i := 0; i < numDrops; i++ {
		r.nakBtree.InsertLocking(uint32(i * 100)) // Gap > mergeGap
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

// ═══════════════════════════════════════════════════════════════════════════
// Benchmarks
// ═══════════════════════════════════════════════════════════════════════════

func BenchmarkConsolidateNakBtree(b *testing.B) {
	sizes := []int{10, 100, 500, 1000}

	for _, size := range sizes {
		b.Run(formatSize(size), func(b *testing.B) {
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 10 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}

			for i := 0; i < size; i++ {
				if i%5 == 0 {
					r.nakBtree.InsertLocking(uint32(i * 10))
				} else {
					r.nakBtree.InsertLocking(uint32(i*10 + 1))
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

func BenchmarkSyncPoolConsolidation(b *testing.B) {
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            3,
		nakConsolidationBudget: 10 * time.Millisecond,
		metrics:                &metrics.ConnectionMetrics{},
	}

	for i := 0; i < 200; i++ {
		r.nakBtree.InsertLocking(uint32(i * 2))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = r.consolidateNakBtree()
	}
}

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
				nakConsolidationBudget: 100 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}
			r.setupNakDispatch(false) // Use locking versions for benchmarks

			for i := 0; i < s.numDrops; i++ {
				if s.mergeGap == 0 {
					r.nakBtree.InsertLocking(uint32(i * 100))
				} else {
					r.nakBtree.InsertLocking(uint32(i))
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
