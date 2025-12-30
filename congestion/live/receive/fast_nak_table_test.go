// Package live provides table-driven FastNAK tests.
//
// This file consolidates TestCheckFastNak_* and TestCheckFastNakRecent_* tests
// into a unified table-driven approach.
//
// FastNAK optimizes NAK responses by detecting:
// 1. Long silence periods (no packets received)
// 2. Sequence jumps indicating loss
// 3. Large burst losses (Starlink-style outages)
//
// See documentation/table_driven_test_design_implementation.md for progress.
package receive

import (
	"math"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// FAST NAK CONDITION TEST CASES
// ============================================================================

// FastNakConditionTestCase tests FastNAK triggering conditions.
type FastNakConditionTestCase struct {
	Name string

	// Configuration
	FastNakEnabled  bool
	UseNakBtree     bool
	HasPrevPacket   bool           // Whether lastPacketArrivalTime is set
	SilenceDuration time.Duration  // Time since last packet

	// Expected
	ExpectTrigger bool
}

var FastNakConditionTests = []FastNakConditionTestCase{
	// === Disabled/Missing Prerequisites ===
	{
		Name:           "Disabled",
		FastNakEnabled: false,
		UseNakBtree:    true,
		HasPrevPacket:  true,
		SilenceDuration: 100 * time.Millisecond,
		ExpectTrigger:  false,
	},
	{
		Name:           "NoNakBtree",
		FastNakEnabled: true,
		UseNakBtree:    false,
		HasPrevPacket:  true,
		SilenceDuration: 100 * time.Millisecond,
		ExpectTrigger:  false,
	},
	{
		Name:           "NoPreviousPacket",
		FastNakEnabled: true,
		UseNakBtree:    true,
		HasPrevPacket:  false,
		SilenceDuration: 0,
		ExpectTrigger:  false,
	},

	// === Silence Duration Tests ===
	{
		Name:           "ShortSilence_10ms",
		FastNakEnabled: true,
		UseNakBtree:    true,
		HasPrevPacket:  true,
		SilenceDuration: 10 * time.Millisecond,
		ExpectTrigger:  false, // Below 50ms threshold
	},
	{
		Name:           "ShortSilence_40ms",
		FastNakEnabled: true,
		UseNakBtree:    true,
		HasPrevPacket:  true,
		SilenceDuration: 40 * time.Millisecond,
		ExpectTrigger:  false, // Below 50ms threshold
	},
}

func TestCheckFastNak_Table(t *testing.T) {
	for _, tc := range FastNakConditionTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			m := &metrics.ConnectionMetrics{}
			r := &receiver{
				nakBtree:         newNakBtree(32),
				useNakBtree:      tc.UseNakBtree,
				fastNakEnabled:   tc.FastNakEnabled,
				fastNakThreshold: 50 * time.Millisecond,
				metrics:          m,
			}

			now := time.Now()
			if tc.HasPrevPacket {
				r.lastPacketArrivalTime.Store(now.Add(-tc.SilenceDuration))
			}

			r.checkFastNak(now)

			triggered := m.NakFastTriggers.Load() > 0
			require.Equal(t, tc.ExpectTrigger, triggered,
				"FastNAK trigger mismatch for %s", tc.Name)

			t.Logf("  ✓ %s completed (triggered=%v)", tc.Name, triggered)
		})
	}
}

// ============================================================================
// FAST NAK RECENT TEST CASES
// ============================================================================

// FastNakRecentTestCase tests FastNAKRecent insertion conditions.
type FastNakRecentTestCase struct {
	Name string

	// Configuration
	Enabled          bool
	HasNakBtree      bool
	HasPrevPacket    bool
	SilenceDuration  time.Duration
	LastSeq          uint32
	NewSeq           uint32
	PacketsPerSec    float64 // Simulated receive rate

	// Expected
	ExpectInserts bool
	MinInserts    uint64 // Minimum expected inserts (0 = don't check)
	MaxInserts    uint64 // Maximum expected inserts (0 = don't check)
}

var FastNakRecentTests = []FastNakRecentTestCase{
	// === Disabled/Missing Prerequisites ===
	{
		Name:          "Disabled",
		Enabled:       false,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:       100,
		NewSeq:        200,
		ExpectInserts: false,
	},
	{
		Name:          "NoNakBtree",
		Enabled:       true,
		HasNakBtree:   false,
		HasPrevPacket: true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:       100,
		NewSeq:        200,
		ExpectInserts: false,
	},
	{
		Name:          "NoPreviousPacket",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: false,
		SilenceDuration: 0,
		LastSeq:       0,
		NewSeq:        100,
		ExpectInserts: false,
	},

	// === Silence/Jump Conditions ===
	{
		Name:          "ShortSilence",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 10 * time.Millisecond,
		LastSeq:       100,
		NewSeq:        105,
		PacketsPerSec: 500,
		ExpectInserts: false, // Silence too short
	},
	{
		Name:          "NoSequenceJump",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:       100,
		NewSeq:        101, // Sequential, no gap
		PacketsPerSec: 500,
		ExpectInserts: false,
	},
	{
		Name:          "SignificantJump",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:       100,
		NewSeq:        150, // 49-packet gap (matches original test)
		PacketsPerSec: 500, // 100ms * 500pps = 50 expected packets
		ExpectInserts: true,
		MinInserts:    40,
		MaxInserts:    60,
	},

	// === Large Burst Loss Tests (Starlink scenarios) ===
	{
		Name:          "LargeBurstLoss_5Mbps",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 60 * time.Millisecond, // Starlink-style outage
		LastSeq:       10000,
		NewSeq:        10300, // ~300 packet gap
		PacketsPerSec: 5000,  // 5Mbps @ 1000 byte packets
		ExpectInserts: true,
		MinInserts:    200,
		MaxInserts:    400,
	},
	{
		Name:          "LargeBurstLoss_20Mbps",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 60 * time.Millisecond,
		LastSeq:       50000,
		NewSeq:        51200, // ~1200 packet gap
		PacketsPerSec: 20000,
		ExpectInserts: true,
		MinInserts:    1000,
		MaxInserts:    1400,
	},
	{
		Name:          "LargeBurstLoss_100Mbps",
		Enabled:       true,
		HasNakBtree:   true,
		HasPrevPacket: true,
		SilenceDuration: 60 * time.Millisecond,
		LastSeq:       100000,
		NewSeq:        106000, // ~6000 packet gap
		PacketsPerSec: 100000,
		ExpectInserts: true,
		MinInserts:    1000, // May be capped by maxFastNakRecentGap
		MaxInserts:    10000,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// CORNER CASE TESTS: Sequence Wraparound
	// Tests 31-bit sequence wraparound at MAX_SEQUENCENUMBER boundary
	//
	// DISC-005: These tests FAIL - checkFastNakRecent() may not handle
	// 31-bit wraparound correctly. Deferred for investigation.
	// ═══════════════════════════════════════════════════════════════════════

	// ═══════════════════════════════════════════════════════════════════════
	// DISC-005 FIXED: SeqDiff now handles wraparound correctly
	// DISC-008 FIXED: minUpperBound=1000 allows gaps up to 1000 even with low pps
	// ═══════════════════════════════════════════════════════════════════════

	// Corner: LastSeq near MAX, NewSeq after wrap (small gap ~266 packets)
	{
		Name:            "Corner_Wraparound_SmallGap",
		Enabled:         true,
		HasNakBtree:     true,
		HasPrevPacket:   true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:         0x7FFFFF00, // Near MAX
		NewSeq:          10,         // After wrap, actualGap=266
		PacketsPerSec:   500,        // expectedGap=50, but minUpperBound=1000 allows it
		ExpectInserts:   true,
		MinInserts:      200, // ~265 packets gap (0xFF + 10)
		MaxInserts:      300,
	},

	// Corner: NewSeq at 0 after wrap (~256 packets)
	{
		Name:            "Corner_Wraparound_NewSeqZero",
		Enabled:         true,
		HasNakBtree:     true,
		HasPrevPacket:   true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:         0x7FFFFF00, // Near MAX
		NewSeq:          0,          // Exactly at wrap, actualGap=256
		PacketsPerSec:   500,        // expectedGap=50, but minUpperBound=1000 allows it
		ExpectInserts:   true,
		MinInserts:      200, // ~256 packets gap
		MaxInserts:      300,
	},

	// Corner: Large gap across wraparound boundary (~755 packets)
	{
		Name:            "Corner_Wraparound_LargeGap",
		Enabled:         true,
		HasNakBtree:     true,
		HasPrevPacket:   true,
		SilenceDuration: 100 * time.Millisecond,
		LastSeq:         0x7FFFFF00,
		NewSeq:          500, // actualGap=755 < minUpperBound=1000 ✓
		PacketsPerSec:   5000,
		ExpectInserts:   true,
		MinInserts:      500,
		MaxInserts:      1000,
	},
}

func TestCheckFastNakRecent_Table(t *testing.T) {
	for _, tc := range FastNakRecentTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			m := &metrics.ConnectionMetrics{}
			r := &receiver{
				useNakBtree:            true,
				fastNakEnabled:         true,
				fastNakThreshold:       50 * time.Millisecond,
				fastNakRecentEnabled:   tc.Enabled,
				periodicNAKInterval:    20000,
				nakMergeGap:            3,
				nakConsolidationBudget: 5 * time.Second,
				metrics:                m,
			}

			if tc.HasNakBtree {
				r.nakBtree = newNakBtree(32)
			}

			if tc.PacketsPerSec > 0 {
				m.RecvRatePacketsPerSec.Store(math.Float64bits(tc.PacketsPerSec))
			}

			now := time.Now()
			if tc.HasPrevPacket {
				r.lastPacketArrivalTime.Store(now.Add(-tc.SilenceDuration))
				r.lastDataPacketSeq.Store(tc.LastSeq)
			}

			r.checkFastNakRecent(tc.NewSeq, now)

			inserts := m.NakFastRecentInserts.Load()
			hasInserts := inserts > 0

			t.Logf("Test: %s", tc.Name)
			t.Logf("  Inserts: %d, Expected: %v", inserts, tc.ExpectInserts)

			require.Equal(t, tc.ExpectInserts, hasInserts,
				"FastNAKRecent insert mismatch")

			if tc.MinInserts > 0 {
				require.GreaterOrEqual(t, inserts, tc.MinInserts,
					"FastNAKRecent inserts below minimum")
			}
			if tc.MaxInserts > 0 {
				require.LessOrEqual(t, inserts, tc.MaxInserts,
					"FastNAKRecent inserts above maximum")
			}

			t.Logf("  ✓ %s completed", tc.Name)
		})
	}
}

// ============================================================================
// BUILD NAK LIST TEST CASES
// ============================================================================

type BuildNakListTestCase struct {
	Name        string
	UseNakBtree bool
	Sequences   []uint32 // Sequences to insert into NAK btree
	ExpectEmpty bool
	ExpectCount int // Expected entry count (0 = just check empty/non-empty)
}

var BuildNakListTests = []BuildNakListTestCase{
	{
		Name:        "Empty",
		UseNakBtree: true,
		Sequences:   nil,
		ExpectEmpty: true,
	},
	{
		Name:        "NoNakBtree",
		UseNakBtree: false,
		Sequences:   nil,
		ExpectEmpty: true,
	},
	{
		Name:        "WithEntries",
		UseNakBtree: true,
		Sequences:   []uint32{100, 101, 102},
		ExpectEmpty: false,
		ExpectCount: 2, // Should consolidate to single range (100-102)
	},
	{
		Name:        "MultipleRanges",
		UseNakBtree: true,
		Sequences:   []uint32{100, 101, 200, 201},
		ExpectEmpty: false,
		ExpectCount: 4, // Two ranges: 100-101, 200-201
	},
}

func TestBuildNakListLocked_Table(t *testing.T) {
	for _, tc := range BuildNakListTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			m := &metrics.ConnectionMetrics{}
			r := &receiver{
				useNakBtree:            tc.UseNakBtree,
				nakMergeGap:            3,
				nakConsolidationBudget: 2 * time.Millisecond,
				metrics:                m,
			}

			if tc.UseNakBtree {
				r.nakBtree = newNakBtree(32)
				for _, seq := range tc.Sequences {
					r.nakBtree.Insert(seq)
				}
			}

			list := r.buildNakListLocked()

			t.Logf("Test: %s", tc.Name)
			t.Logf("  Input sequences: %v", tc.Sequences)
			t.Logf("  Output entries: %d", len(list))

			if tc.ExpectEmpty {
				require.Empty(t, list, "Expected empty list")
			} else {
				require.NotEmpty(t, list, "Expected non-empty list")
				if tc.ExpectCount > 0 {
					require.Equal(t, tc.ExpectCount, len(list),
						"Unexpected entry count")
				}
			}

			t.Logf("  ✓ %s completed", tc.Name)
		})
	}
}

// ============================================================================
// MULTIPLE BURST LOSS TESTS
// ============================================================================

// MultipleBurstTestCase tests multiple sequential outages.
type MultipleBurstTestCase struct {
	Name          string
	Bursts        []struct {
		SilenceMs int
		LastSeq   uint32
		NewSeq    uint32
	}
	PacketsPerSec float64
	MinTotalNaks  int
	MaxRanges     int // Expected consolidated ranges
}

var MultipleBurstTests = []MultipleBurstTestCase{
	{
		Name: "ThreeStarlinkOutages",
		Bursts: []struct {
			SilenceMs int
			LastSeq   uint32
			NewSeq    uint32
		}{
			{60, 10000, 10300},  // First outage: ~300 packets
			{60, 11000, 11300},  // Second outage: ~300 packets
			{60, 12000, 12300},  // Third outage: ~300 packets
		},
		PacketsPerSec: 5000,
		MinTotalNaks:  600, // At least 600 NAKs total
		MaxRanges:     3,   // Should consolidate to 3 ranges
	},
}

func TestCheckFastNakRecent_MultipleBursts_Table(t *testing.T) {
	for _, tc := range MultipleBurstTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			m := &metrics.ConnectionMetrics{}
			r := &receiver{
				nakBtree:               newNakBtree(32),
				useNakBtree:            true,
				fastNakEnabled:         true,
				fastNakThreshold:       50 * time.Millisecond,
				fastNakRecentEnabled:   true,
				periodicNAKInterval:    20000,
				nakMergeGap:            3,
				nakConsolidationBudget: 5 * time.Second,
				metrics:                m,
			}
			m.RecvRatePacketsPerSec.Store(math.Float64bits(tc.PacketsPerSec))

			now := time.Now()
			for i, burst := range tc.Bursts {
				r.lastPacketArrivalTime.Store(now.Add(-time.Duration(burst.SilenceMs) * time.Millisecond))
				r.lastDataPacketSeq.Store(burst.LastSeq)
				r.checkFastNakRecent(burst.NewSeq, now)

				t.Logf("Burst %d: silence=%dms, gap=%d",
					i+1, burst.SilenceMs, burst.NewSeq-burst.LastSeq-1)
			}

			totalInserts := m.NakFastRecentInserts.Load()
			t.Logf("Total inserts: %d", totalInserts)

			require.GreaterOrEqual(t, int(totalInserts), tc.MinTotalNaks,
				"Total NAKs below minimum")

			// Verify consolidation
			list := r.consolidateNakBtree()
			ranges := len(list) / 2
			t.Logf("Consolidated to %d ranges", ranges)

			require.LessOrEqual(t, ranges, tc.MaxRanges,
				"Too many consolidated ranges")

			t.Logf("  ✓ %s completed", tc.Name)
		})
	}
}

