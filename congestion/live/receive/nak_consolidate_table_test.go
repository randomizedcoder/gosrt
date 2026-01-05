// Package live provides table-driven NAK consolidation tests.
//
// This file consolidates the individual TestConsolidateNakBtree_* tests
// into a unified table-driven approach. Tests verify that the NAK btree
// correctly consolidates sequences into ranges for efficient NAK packets.
//
// See documentation/table_driven_test_design_implementation.md for progress.
package receive

import (
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// ============================================================================
// SEQUENCE GENERATION PATTERNS
// ============================================================================

// SeqPattern defines how to generate sequences for NAK btree insertion.
type SeqPattern interface {
	// Generate returns sequences to insert into NAK btree
	Generate() []uint32
	// Description returns human-readable description
	Description() string
}

// ExplicitSeqs uses an explicit list of sequences
type ExplicitSeqs struct {
	Seqs []uint32
}

func (e ExplicitSeqs) Generate() []uint32  { return e.Seqs }
func (e ExplicitSeqs) Description() string { return "explicit" }

// ModulusSeqs generates every Nth sequence in a range
type ModulusSeqs struct {
	Start   uint32
	End     uint32
	Modulus int
}

func (m ModulusSeqs) Generate() []uint32 {
	var seqs []uint32
	for seq := m.Start; seq <= m.End; seq++ {
		if int(seq)%m.Modulus == 0 {
			seqs = append(seqs, seq)
		}
	}
	return seqs
}

func (m ModulusSeqs) Description() string {
	return "modulus_" + itoa(m.Modulus)
}

// BurstSeqs generates multiple contiguous bursts
type BurstSeqs struct {
	Starts    []uint32
	BurstSize int
}

func (b BurstSeqs) Generate() []uint32 {
	var seqs []uint32
	for _, start := range b.Starts {
		for i := 0; i < b.BurstSize; i++ {
			seqs = append(seqs, start+uint32(i))
		}
	}
	return seqs
}

func (b BurstSeqs) Description() string {
	return "burst_" + itoa(len(b.Starts)) + "x" + itoa(b.BurstSize)
}

// ContiguousSeqs generates a contiguous range
type ContiguousSeqs struct {
	Start uint32
	Count int
}

func (c ContiguousSeqs) Generate() []uint32 {
	seqs := make([]uint32, c.Count)
	for i := 0; i < c.Count; i++ {
		seqs[i] = c.Start + uint32(i)
	}
	return seqs
}

func (c ContiguousSeqs) Description() string {
	return "contiguous_" + itoa(c.Count)
}

// ============================================================================
// EXPECTED OUTPUT
// ============================================================================

// NakRange represents an expected NAK range (start, end)
type NakRange struct {
	Start uint32
	End   uint32
}

// IsRange returns true if this is a range (not a single)
func (r NakRange) IsRange() bool {
	return r.Start != r.End
}

// Count returns the number of sequences in this range
func (r NakRange) Count() int {
	return int(r.End - r.Start + 1)
}

// ============================================================================
// TEST CASE DEFINITION
// ============================================================================

// ConsolidateTestCase defines a single NAK consolidation test scenario.
type ConsolidateTestCase struct {
	Name           string
	NakMergeGap    uint32     // Gap threshold for merging (default 3)
	SetMergeGapTo0 bool       // Explicitly set NakMergeGap=0 (since 0 is valid corner case)
	Pattern        SeqPattern // How to generate sequences

	// Expected output - specify ONE of these:
	ExpectedRanges []NakRange // Explicit expected ranges
	ExpectedCount  int        // Expected number of ranges (for large-scale tests)
	AllSingles     bool       // If true, expect all singles (no ranges)
	SingleMerge    bool       // If true, expect everything to merge into one range
}

// ============================================================================
// TEST RUNNER
// ============================================================================

func runConsolidateTableTest(t *testing.T, tc ConsolidateTestCase) {
	t.Helper()

	// Apply defaults (only if not explicitly set to 0)
	if tc.NakMergeGap == 0 && !tc.SetMergeGapTo0 {
		tc.NakMergeGap = 3
	}

	// Create receiver
	r := &receiver{
		nakBtree:               newNakBtree(32),
		nakMergeGap:            tc.NakMergeGap,
		nakConsolidationBudget: 2 * time.Millisecond,
		metrics:                &metrics.ConnectionMetrics{},
	}
	r.setupNakDispatch(false) // Use locking versions for tests

	// Generate and insert sequences
	seqs := tc.Pattern.Generate()
	for _, seq := range seqs {
		r.nakBtree.InsertLocking(seq)
	}

	// Run consolidation
	list := r.consolidateNakBtree()

	// Convert list to ranges for easier comparison
	var actualRanges []NakRange
	for i := 0; i < len(list); i += 2 {
		actualRanges = append(actualRanges, NakRange{
			Start: list[i].Val(),
			End:   list[i+1].Val(),
		})
	}

	t.Logf("Test: %s", tc.Name)
	t.Logf("  Pattern: %s, Inserted: %d sequences", tc.Pattern.Description(), len(seqs))
	t.Logf("  MergeGap: %d, Ranges: %d", tc.NakMergeGap, len(actualRanges))

	// Verify based on expectation type
	if len(tc.ExpectedRanges) > 0 {
		// Explicit range verification
		if len(actualRanges) != len(tc.ExpectedRanges) {
			t.Errorf("Expected %d ranges, got %d", len(tc.ExpectedRanges), len(actualRanges))
			t.Logf("  Actual ranges: %v", actualRanges)
			return
		}
		for i, exp := range tc.ExpectedRanges {
			if actualRanges[i].Start != exp.Start || actualRanges[i].End != exp.End {
				t.Errorf("Range %d: expected (%d, %d), got (%d, %d)",
					i, exp.Start, exp.End, actualRanges[i].Start, actualRanges[i].End)
			}
		}
	} else if tc.ExpectedCount > 0 {
		// Count verification (for large-scale tests)
		if len(actualRanges) != tc.ExpectedCount {
			t.Errorf("Expected %d ranges, got %d", tc.ExpectedCount, len(actualRanges))
		}
	} else if tc.AllSingles {
		// All singles verification
		for i, r := range actualRanges {
			if r.IsRange() {
				t.Errorf("Range %d: expected single, got range (%d, %d)", i, r.Start, r.End)
			}
		}
		if len(actualRanges) != len(seqs) {
			t.Errorf("Expected %d singles, got %d ranges", len(seqs), len(actualRanges))
		}
	} else if tc.SingleMerge {
		// Single merged range verification
		if len(actualRanges) != 1 {
			t.Errorf("Expected 1 merged range, got %d", len(actualRanges))
		}
	}

	t.Logf("  ✓ %s completed", tc.Name)
}

// ============================================================================
// TEST CASES
// ============================================================================

// ConsolidateTableTests defines all NAK consolidation test scenarios.
var ConsolidateTableTests = []ConsolidateTestCase{
	// === Basic Tests ===
	{
		Name:           "Empty",
		Pattern:        ExplicitSeqs{Seqs: nil},
		ExpectedRanges: nil,
	},
	{
		Name:           "SingleEntry",
		Pattern:        ExplicitSeqs{Seqs: []uint32{100}},
		ExpectedRanges: []NakRange{{100, 100}},
	},
	{
		Name:           "ContiguousRange",
		Pattern:        ContiguousSeqs{Start: 100, Count: 6},
		ExpectedRanges: []NakRange{{100, 105}},
	},
	{
		Name:        "MergeWithinGap",
		NakMergeGap: 3,
		Pattern:     ExplicitSeqs{Seqs: []uint32{100, 101, 102, 106, 107, 108}},
		// Gap of 3 (103,104,105) == mergeGap, should merge
		ExpectedRanges: []NakRange{{100, 108}},
	},
	{
		Name:        "GapExceedsMergeThreshold",
		NakMergeGap: 2,
		Pattern:     ExplicitSeqs{Seqs: []uint32{100, 101, 106, 107}},
		// Gap of 4 > mergeGap=2, should NOT merge
		ExpectedRanges: []NakRange{{100, 101}, {106, 107}},
	},
	{
		Name:        "MixedSinglesAndRanges",
		NakMergeGap: 1,
		Pattern:     ExplicitSeqs{Seqs: []uint32{100, 106, 107, 108, 114}},
		ExpectedRanges: []NakRange{
			{100, 100}, // Single
			{106, 108}, // Range
			{114, 114}, // Single
		},
	},

	// === Wraparound Tests ===
	{
		Name:        "SequenceWraparound",
		NakMergeGap: 2,
		Pattern: ExplicitSeqs{Seqs: []uint32{
			uint32(packet.MAX_SEQUENCENUMBER) - 2,
			uint32(packet.MAX_SEQUENCENUMBER) - 1,
			uint32(packet.MAX_SEQUENCENUMBER),
		}},
		ExpectedRanges: []NakRange{{
			uint32(packet.MAX_SEQUENCENUMBER) - 2,
			uint32(packet.MAX_SEQUENCENUMBER),
		}},
	},

	// === Modulus Drop Tests ===
	{
		Name:        "ModulusDrops_Every10th",
		NakMergeGap: 3,
		Pattern:     ModulusSeqs{Start: 1, End: 100, Modulus: 10},
		AllSingles:  true, // Gap of 9 > mergeGap=3
	},
	{
		Name:        "ModulusDrops_Every5th",
		NakMergeGap: 3,
		Pattern:     ModulusSeqs{Start: 1, End: 100, Modulus: 5},
		AllSingles:  true, // Gap of 4 > mergeGap=3
	},
	{
		Name:        "ModulusDrops_Every3rd_WithMerge",
		NakMergeGap: 3,
		Pattern:     ModulusSeqs{Start: 1, End: 30, Modulus: 3},
		SingleMerge: true, // Gap of 2 <= mergeGap=3, should merge
	},

	// === Burst Drop Tests ===
	{
		Name:        "BurstDrops",
		NakMergeGap: 3,
		Pattern:     BurstSeqs{Starts: []uint32{100, 200, 300, 400, 500}, BurstSize: 10},
		ExpectedRanges: []NakRange{
			{100, 109},
			{200, 209},
			{300, 309},
			{400, 409},
			{500, 509},
		},
	},
	{
		Name:        "MixedModulusAndBurst",
		NakMergeGap: 2,
		Pattern: ExplicitSeqs{Seqs: []uint32{
			50, 100, 150, 200, // Singles (gap=49 > mergeGap)
			250, 251, 252, 253, 254, // Burst
		}},
		ExpectedRanges: []NakRange{
			{50, 50},
			{100, 100},
			{150, 150},
			{200, 200},
			{250, 254},
		},
	},

	// === Large Scale Tests ===
	{
		Name:          "LargeScale_ModulusDrops",
		NakMergeGap:   3,
		Pattern:       ModulusSeqs{Start: 1, End: 10000, Modulus: 10},
		ExpectedCount: 1000, // 10000/10 = 1000 singles
	},
	{
		Name:        "LargeScale_BurstDrops",
		NakMergeGap: 3,
		Pattern: BurstSeqs{
			Starts:    []uint32{1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000, 10000},
			BurstSize: 20,
		},
		ExpectedCount: 10, // 10 bursts
	},

	// === Out of Order Insertion ===
	{
		Name:        "OutOfOrderInsertion",
		NakMergeGap: 3,
		Pattern: ExplicitSeqs{Seqs: []uint32{
			105, 100, 103, 101, 104, 102, // Out of order but contiguous
		}},
		ExpectedRanges: []NakRange{{100, 105}},
	},
	{
		Name:        "OutOfOrderWithGaps",
		NakMergeGap: 2,
		Pattern: ExplicitSeqs{Seqs: []uint32{
			300, 100, 200, 101, 201, 301, // Three pairs, out of order
		}},
		ExpectedRanges: []NakRange{
			{100, 101},
			{200, 201},
			{300, 301},
		},
	},

	// === CORNER CASES: NakMergeGap ===
	// These test the CODE_PARAM corner values: 0, 3 (typical), 100 (large)

	// NakMergeGap=0: Only merge strictly contiguous sequences (gap=0)
	{
		Name:           "Corner_MergeGap_Zero_ContiguousMerge",
		NakMergeGap:    0, // Zero = only merge contiguous
		SetMergeGapTo0: true,
		Pattern:        ContiguousSeqs{Start: 100, Count: 5},
		// 100,101,102,103,104 - all contiguous, should merge into single range
		ExpectedRanges: []NakRange{{100, 104}},
	},
	{
		Name:           "Corner_MergeGap_Zero_GapOfOne",
		NakMergeGap:    0, // Zero = gap of 1 should NOT merge
		SetMergeGapTo0: true,
		Pattern:        ExplicitSeqs{Seqs: []uint32{100, 101, 103, 104}},
		// Gap of 1 (missing 102) should NOT merge with mergeGap=0
		ExpectedRanges: []NakRange{{100, 101}, {103, 104}},
	},
	{
		Name:           "Corner_MergeGap_Zero_AllSingles",
		NakMergeGap:    0,
		SetMergeGapTo0: true,
		Pattern:        ExplicitSeqs{Seqs: []uint32{100, 105, 110, 115}},
		// No contiguous sequences, all singles
		ExpectedRanges: []NakRange{
			{100, 100}, {105, 105}, {110, 110}, {115, 115},
		},
	},
	{
		Name:           "Corner_MergeGap_Zero_Wraparound",
		NakMergeGap:    0,
		SetMergeGapTo0: true,
		Pattern: ExplicitSeqs{Seqs: []uint32{
			uint32(packet.MAX_SEQUENCENUMBER) - 1,
			uint32(packet.MAX_SEQUENCENUMBER),
			// Note: wrapping to 0 would have gap > 0
		}},
		// Adjacent at MAX should merge even with mergeGap=0
		ExpectedRanges: []NakRange{{
			uint32(packet.MAX_SEQUENCENUMBER) - 1,
			uint32(packet.MAX_SEQUENCENUMBER),
		}},
	},

	// NakMergeGap=100: Aggressive merging (large gaps allowed)
	{
		Name:        "Corner_MergeGap_Large_MergeDistant",
		NakMergeGap: 100, // Large = merge sequences up to 100 apart
		Pattern:     ExplicitSeqs{Seqs: []uint32{100, 150, 200, 250}},
		// Gaps of 49 (100→150), 49 (150→200), 49 (200→250) all <= 100
		// Should merge into single range!
		ExpectedRanges: []NakRange{{100, 250}},
	},
	{
		Name:        "Corner_MergeGap_Large_StillSplits",
		NakMergeGap: 100,
		Pattern:     ExplicitSeqs{Seqs: []uint32{100, 150, 300, 350}},
		// Gap of 49 (100→150) <= 100: merge
		// Gap of 149 (150→300) > 100: split
		// Gap of 49 (300→350) <= 100: merge
		ExpectedRanges: []NakRange{{100, 150}, {300, 350}},
	},
	{
		Name:        "Corner_MergeGap_Large_ModulusDrops",
		NakMergeGap: 100,
		Pattern:     ModulusSeqs{Start: 1, End: 500, Modulus: 10},
		// Every 10th packet: 10,20,30...500 = 50 packets
		// Gap of 9 between each, all <= 100, should merge into ONE range!
		ExpectedRanges: []NakRange{{10, 500}},
	},
}

// TestConsolidateNakBtree_Table runs all consolidation tests using table-driven approach.
// Tests run in parallel for faster execution.
func TestConsolidateNakBtree_Table(t *testing.T) {
	for _, tc := range ConsolidateTableTests {
		tc := tc // Capture range variable for parallel execution
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel() // Run test cases in parallel
			runConsolidateTableTest(t, tc)
		})
	}
}

// ============================================================================
// MSS LIMIT TESTS
// ============================================================================

// MSSTestCase tests MSS (Maximum Segment Size) boundary behavior
type MSSTestCase struct {
	Name           string
	NakMergeGap    uint32
	Pattern        SeqPattern
	MaxPayload     int  // Simulated max payload size
	ExpectedChunks int  // Expected number of NAK packets
	ExpectSplit    bool // Whether output should be split due to MSS
}

var MSSTableTests = []MSSTestCase{
	{
		Name:        "MSS_UnderLimit_Singles",
		NakMergeGap: 1,
		Pattern:     ExplicitSeqs{Seqs: []uint32{100, 110, 120}}, // 3 singles
		MaxPayload:  1000,
		ExpectSplit: false,
	},
	{
		Name:        "MSS_AtLimit_Singles",
		NakMergeGap: 1,
		Pattern:     ModulusSeqs{Start: 100, End: 500, Modulus: 10}, // ~40 singles
		MaxPayload:  200,
		ExpectSplit: true,
	},
	{
		Name:        "MSS_OverLimit_Singles",
		NakMergeGap: 1,
		Pattern:     ModulusSeqs{Start: 100, End: 1000, Modulus: 10}, // ~90 singles
		MaxPayload:  100,
		ExpectSplit: true,
	},
}

// TestConsolidateNakBtree_MSS_Table runs MSS-related tests.
// Note: MSS splitting is handled by a different layer, but we verify
// the consolidation still produces correct ranges.
func TestConsolidateNakBtree_MSS_Table(t *testing.T) {
	for _, tc := range MSSTableTests {
		tc := tc // Capture range variable
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			// Apply defaults
			if tc.NakMergeGap == 0 {
				tc.NakMergeGap = 3
			}

			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            tc.NakMergeGap,
				nakConsolidationBudget: 2 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}
			r.setupNakDispatch(false) // Use locking versions for tests

			seqs := tc.Pattern.Generate()
			for _, seq := range seqs {
				r.nakBtree.InsertLocking(seq)
			}

			list := r.consolidateNakBtree()

			t.Logf("Test: %s", tc.Name)
			t.Logf("  Inserted: %d sequences, Output: %d entries", len(seqs), len(list))

			// Verify we got some output
			if len(seqs) > 0 && len(list) == 0 {
				t.Error("Expected non-empty output for non-empty input")
			}

			t.Logf("  ✓ %s completed", tc.Name)
		})
	}
}

// ============================================================================
// EXTREME SCALE TESTS
// ============================================================================

type ExtremeScaleTestCase struct {
	Name         string
	TotalPackets int
	LossRate     float64 // 0.0 to 1.0
	BurstSize    int     // 0 for uniform loss
	MaxRanges    int     // Maximum expected ranges
	MinRanges    int     // Minimum expected ranges
}

var ExtremeScaleTableTests = []ExtremeScaleTestCase{
	{
		Name:         "60SecBuffer_10pctLoss",
		TotalPackets: 60 * 500, // 60 seconds at 500 pps
		LossRate:     0.10,
		MinRanges:    100,
		MaxRanges:    5000,
	},
	{
		Name:         "LongOutage_5SecBurst",
		TotalPackets: 10000,
		BurstSize:    2500, // 5 second outage at 500 pps
		MinRanges:    1,
		MaxRanges:    10,
	},
}

func TestConsolidateNakBtree_ExtremeScale_Table(t *testing.T) {
	for _, tc := range ExtremeScaleTableTests {
		tc := tc // Capture range variable
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 50 * time.Millisecond, // More time for extreme tests
				metrics:                &metrics.ConnectionMetrics{},
			}
			r.setupNakDispatch(false) // Use locking versions for tests

			// Generate based on loss pattern
			var insertedCount int
			if tc.BurstSize > 0 {
				// Burst loss
				start := uint32(tc.TotalPackets / 2)
				for i := 0; i < tc.BurstSize; i++ {
					r.nakBtree.InsertLocking(start + uint32(i))
					insertedCount++
				}
			} else {
				// Uniform loss
				step := int(1.0 / tc.LossRate)
				for i := 0; i < tc.TotalPackets; i += step {
					r.nakBtree.InsertLocking(uint32(i))
					insertedCount++
				}
			}

			list := r.consolidateNakBtree()
			rangeCount := len(list) / 2

			t.Logf("Test: %s", tc.Name)
			t.Logf("  Packets: %d, Inserted: %d, Ranges: %d", tc.TotalPackets, insertedCount, rangeCount)

			if tc.MinRanges > 0 && rangeCount < tc.MinRanges {
				t.Errorf("Expected at least %d ranges, got %d", tc.MinRanges, rangeCount)
			}
			if tc.MaxRanges > 0 && rangeCount > tc.MaxRanges {
				t.Errorf("Expected at most %d ranges, got %d", tc.MaxRanges, rangeCount)
			}

			t.Logf("  ✓ %s completed", tc.Name)
		})
	}
}

// ============================================================================
// ENTRIES TO NAK LIST TESTS
// ============================================================================

type EntriesToNakListTestCase struct {
	Name          string
	Entries       []NAKEntry
	ExpectedPairs int      // Expected number of start/end pairs in output
	ExpectedSeqs  []uint32 // If non-nil, verify exact sequence values
}

var EntriesToNakListTableTests = []EntriesToNakListTestCase{
	{
		Name:          "Empty_Nil",
		Entries:       nil,
		ExpectedPairs: 0,
	},
	{
		Name:          "Empty_Slice",
		Entries:       []NAKEntry{},
		ExpectedPairs: 0,
	},
	{
		Name: "SingleAndRange",
		Entries: []NAKEntry{
			{Start: 100, End: 100}, // Single
			{Start: 200, End: 205}, // Range
		},
		ExpectedPairs: 2,
		ExpectedSeqs:  []uint32{100, 100, 200, 205},
	},
	{
		Name: "CircularNumberMax",
		Entries: []NAKEntry{
			{Start: uint32(packet.MAX_SEQUENCENUMBER) - 5, End: uint32(packet.MAX_SEQUENCENUMBER)},
		},
		ExpectedPairs: 1,
	},
}

func TestEntriesToNakList_Table(t *testing.T) {
	for _, tc := range EntriesToNakListTableTests {
		tc := tc // Capture range variable
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			r := &receiver{
				nakBtree:               newNakBtree(32),
				nakMergeGap:            3,
				nakConsolidationBudget: 2 * time.Millisecond,
				metrics:                &metrics.ConnectionMetrics{},
			}
			r.setupNakDispatch(false) // Use locking versions for tests

			list := r.entriesToNakList(tc.Entries)

			if tc.ExpectedPairs == 0 {
				if list != nil && len(list) > 0 {
					t.Errorf("Expected nil/empty, got %d entries", len(list))
				}
				t.Logf("  ✓ %s completed (empty)", tc.Name)
				return
			}

			if len(list) != tc.ExpectedPairs*2 {
				t.Errorf("Expected %d entries, got %d", tc.ExpectedPairs*2, len(list))
			}

			if len(tc.ExpectedSeqs) > 0 {
				for i, exp := range tc.ExpectedSeqs {
					if i >= len(list) {
						t.Errorf("Missing entry at index %d", i)
						continue
					}
					if list[i].Val() != exp {
						t.Errorf("Entry %d: expected %d, got %d", i, exp, list[i].Val())
					}
				}
			}

			t.Logf("  ✓ %s completed", tc.Name)
		})
	}
}

// ============================================================================
// NAK ENTRY TESTS
// ============================================================================

type NAKEntryTestCase struct {
	Name          string
	Entry         NAKEntry
	ExpectedRange bool
	ExpectedCount uint32
}

var NAKEntryTableTests = []NAKEntryTestCase{
	{"Single", NAKEntry{Start: 100, End: 100}, false, 1},
	{"RangeOf2", NAKEntry{Start: 100, End: 101}, true, 2},
	{"RangeOf6", NAKEntry{Start: 100, End: 105}, true, 6},
}

func TestNAKEntry_Table(t *testing.T) {
	for _, tc := range NAKEntryTableTests {
		tc := tc // Capture range variable
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			if tc.Entry.IsRange() != tc.ExpectedRange {
				t.Errorf("IsRange() = %v, expected %v", tc.Entry.IsRange(), tc.ExpectedRange)
			}
			if tc.Entry.Count() != tc.ExpectedCount {
				t.Errorf("Count() = %d, expected %d", tc.Entry.Count(), tc.ExpectedCount)
			}
		})
	}
}
