//go:build go1.18

package receive

// nak_btree_tsbpd_test.go - Unit tests for NAK btree TSBPD-aware operations
// See nak_btree_expiry_optimization.md for design details

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// InsertWithTsbpd Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestInsertWithTsbpd(t *testing.T) {
	t.Parallel()

	nb := newNakBtree(32)

	// Insert with TSBPD time
	nb.InsertWithTsbpd(100, 5_000_000)
	nb.InsertWithTsbpd(101, 5_001_000)
	nb.InsertWithTsbpd(102, 5_002_000)

	require.Equal(t, 3, nb.Len())

	// Verify entries have correct TSBPD times
	var entries []NakEntryWithTime
	nb.Iterate(func(e NakEntryWithTime) bool {
		entries = append(entries, e)
		return true
	})

	require.Len(t, entries, 3)
	require.Equal(t, uint32(100), entries[0].Seq)
	require.Equal(t, uint64(5_000_000), entries[0].TsbpdTimeUs)
	require.Equal(t, uint32(101), entries[1].Seq)
	require.Equal(t, uint64(5_001_000), entries[1].TsbpdTimeUs)
	require.Equal(t, uint32(102), entries[2].Seq)
	require.Equal(t, uint64(5_002_000), entries[2].TsbpdTimeUs)
}

func TestInsertBatchWithTsbpd(t *testing.T) {
	t.Parallel()

	nb := newNakBtree(32)

	seqs := []uint32{100, 101, 102, 103}
	tsbpds := []uint64{5_000_000, 5_001_000, 5_002_000, 5_003_000}

	inserted := nb.InsertBatchWithTsbpd(seqs, tsbpds)
	require.Equal(t, 4, inserted)
	require.Equal(t, 4, nb.Len())

	// Insert duplicates - should not increase count
	inserted = nb.InsertBatchWithTsbpd(seqs, tsbpds)
	require.Equal(t, 0, inserted)
	require.Equal(t, 4, nb.Len())
}

func TestInsertBatchWithTsbpd_MismatchedLengths(t *testing.T) {
	t.Parallel()

	nb := newNakBtree(32)

	// Mismatched lengths should return 0
	seqs := []uint32{100, 101, 102}
	tsbpds := []uint64{5_000_000, 5_001_000} // One short

	inserted := nb.InsertBatchWithTsbpd(seqs, tsbpds)
	require.Equal(t, 0, inserted)
	require.Equal(t, 0, nb.Len())
}

// ═══════════════════════════════════════════════════════════════════════════
// DeleteBeforeTsbpd Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestDeleteBeforeTsbpd(t *testing.T) {
	tests := []struct {
		name            string
		entries         []NakEntryWithTime
		nowUs           uint64
		rtoUs           uint64
		nakExpiryMargin float64
		wantExpired     int
		wantRemaining   []uint32
	}{
		{
			name: "no_entries_expired_when_all_recent",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_020_000},
				{Seq: 101, TsbpdTimeUs: 1_021_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     0,
			wantRemaining:   []uint32{100, 101},
		},
		{
			name: "oldest_entries_expired",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_010_000},
				{Seq: 101, TsbpdTimeUs: 1_011_000},
				{Seq: 102, TsbpdTimeUs: 1_020_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     2,
			wantRemaining:   []uint32{102},
		},
		{
			name: "nakExpiryMargin_protects_borderline_entry",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 1_017_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10, // threshold = 1s + 16.5ms = 1.0165s
			wantExpired:     0,    // 1.017s > 1.0165s → kept
			wantRemaining:   []uint32{100},
		},
		{
			name:            "empty_tree",
			entries:         []NakEntryWithTime{},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     0,
			wantRemaining:   []uint32{},
		},
		{
			name: "all_entries_expired",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 500_000},
				{Seq: 101, TsbpdTimeUs: 600_000},
				{Seq: 102, TsbpdTimeUs: 700_000},
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10, // threshold = 1.0165s
			wantExpired:     3,
			wantRemaining:   []uint32{},
		},

		// === ADVERSARIAL MONOTONICITY VIOLATION TESTS ===
		{
			name: "adversarial_zero_tsbpd_expires",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 0}, // Uninitialized/buggy
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     1, // Zero TSBPD always < threshold
			wantRemaining:   []uint32{},
		},
		{
			name: "adversarial_max_uint64_never_expires",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: math.MaxUint64}, // Overflow/corruption
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     0, // MaxUint64 always > threshold
			wantRemaining:   []uint32{100},
		},
		{
			name: "adversarial_mixed_valid_and_corrupt",
			entries: []NakEntryWithTime{
				{Seq: 100, TsbpdTimeUs: 0},              // Bug: zero
				{Seq: 101, TsbpdTimeUs: 1_020_000},      // Valid
				{Seq: 102, TsbpdTimeUs: math.MaxUint64}, // Bug: max
			},
			nowUs:           1_000_000,
			rtoUs:           15_000,
			nakExpiryMargin: 0.10,
			wantExpired:     1, // Only zero expires
			wantRemaining:   []uint32{101, 102},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nb := newNakBtree(32)
			for _, e := range tc.entries {
				nb.InsertWithTsbpd(e.Seq, e.TsbpdTimeUs)
			}

			// Calculate threshold: now + RTO * (1 + margin)
			expiryThreshold := tc.nowUs + uint64(float64(tc.rtoUs)*(1.0+tc.nakExpiryMargin))
			expired := nb.DeleteBeforeTsbpd(expiryThreshold)

			require.Equal(t, tc.wantExpired, expired, "expired count mismatch")
			require.Equal(t, len(tc.wantRemaining), nb.Len(), "remaining count mismatch")

			// Verify remaining entries
			remaining := make([]uint32, 0)
			nb.Iterate(func(e NakEntryWithTime) bool {
				remaining = append(remaining, e.Seq)
				return true
			})
			require.Equal(t, tc.wantRemaining, remaining, "remaining entries mismatch")
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// estimateTsbpdForSeq Tests (Linear Interpolation)
// ═══════════════════════════════════════════════════════════════════════════

func TestEstimateTsbpdForSeq(t *testing.T) {
	tests := []struct {
		name       string
		missingSeq uint32
		lowerSeq   uint32
		lowerTsbpd uint64
		upperSeq   uint32
		upperTsbpd uint64
		wantTsbpd  uint64
	}{
		{
			name:       "mid_point_interpolation",
			missingSeq: 105,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   110,
			upperTsbpd: 1_010_000,
			wantTsbpd:  1_005_000, // (105-100)/(110-100) * 10ms = 5ms
		},
		{
			name:       "single_packet_gap",
			missingSeq: 101,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   102,
			upperTsbpd: 1_002_000,
			wantTsbpd:  1_001_000,
		},
		{
			name:       "quarter_point",
			missingSeq: 102,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   108,
			upperTsbpd: 1_008_000,
			wantTsbpd:  1_002_000, // 2/8 = 0.25
		},
		{
			name:       "same_as_lower",
			missingSeq: 100,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   110,
			upperTsbpd: 1_010_000,
			wantTsbpd:  1_000_000, // Same as lower
		},
		{
			name:       "one_before_upper",
			missingSeq: 109,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   110,
			upperTsbpd: 1_010_000,
			wantTsbpd:  1_009_000, // 9/10 of range
		},
		{
			name:       "same_boundary_returns_lower",
			missingSeq: 100,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   100,
			upperTsbpd: 1_000_000,
			wantTsbpd:  1_000_000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)
			require.Equal(t, tc.wantTsbpd, result)
		})
	}
}

// TestEstimateTsbpdForSeq_AdversarialMonotonicity tests that the estimator
// safely handles inputs that violate expected TSBPD ordering.
func TestEstimateTsbpdForSeq_AdversarialMonotonicity(t *testing.T) {
	tests := []struct {
		name       string
		missingSeq uint32
		lowerSeq   uint32
		lowerTsbpd uint64
		upperSeq   uint32
		upperTsbpd uint64
		wantTsbpd  uint64
		wantClamp  bool // true if result should clamp to lowerTsbpd
	}{
		// Inverted TSBPD: lower has higher TSBPD than upper (clock jumped back)
		{
			name:       "inverted_tsbpd_clamps_to_lower",
			missingSeq: 105,
			lowerSeq:   100,
			lowerTsbpd: 2_000_000, // Higher than upper!
			upperSeq:   110,
			upperTsbpd: 1_000_000, // Lower than lower
			wantTsbpd:  2_000_000, // Should clamp to lowerTsbpd
			wantClamp:  true,
		},
		// Equal TSBPD: no time progression between packets
		{
			name:       "equal_tsbpd_returns_lower",
			missingSeq: 105,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   110,
			upperTsbpd: 1_000_000, // Same as lower
			wantTsbpd:  1_000_000, // Should return lowerTsbpd
			wantClamp:  true,
		},
		// Zero lower TSBPD (uninitialized)
		{
			name:       "zero_lower_tsbpd",
			missingSeq: 105,
			lowerSeq:   100,
			lowerTsbpd: 0, // Uninitialized
			upperSeq:   110,
			upperTsbpd: 1_000_000,
			wantTsbpd:  500_000, // Should interpolate (50% of range)
			wantClamp:  false,
		},
		// Zero upper TSBPD: treated as inverted
		{
			name:       "zero_upper_tsbpd_clamps",
			missingSeq: 105,
			lowerSeq:   100,
			lowerTsbpd: 1_000_000,
			upperSeq:   110,
			upperTsbpd: 0, // Bug: zero
			wantTsbpd:  1_000_000, // Should clamp to lowerTsbpd (upper < lower)
			wantClamp:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)
			require.Equal(t, tc.wantTsbpd, result)

			if tc.wantClamp {
				require.Equal(t, tc.lowerTsbpd, result,
					"expected result to clamp to lowerTsbpd")
			}
		})
	}
}

// TestEstimateTsbpdForSeq_AdversarialNoUnderflow verifies that no input
// combination causes underflow, overflow, or panics.
func TestEstimateTsbpdForSeq_AdversarialNoUnderflow(t *testing.T) {
	adversarialInputs := []struct {
		name       string
		missingSeq uint32
		lowerSeq   uint32
		lowerTsbpd uint64
		upperSeq   uint32
		upperTsbpd uint64
	}{
		{"inverted_tsbpd", 105, 100, 2_000_000, 110, 1_000_000},
		{"both_zero_tsbpd", 105, 100, 0, 110, 0},
		{"both_max_tsbpd", 105, 100, math.MaxUint64, 110, math.MaxUint64},
		{"max_lower_zero_upper", 105, 100, math.MaxUint64, 110, 0},
		{"zero_lower_max_upper", 105, 100, 0, 110, math.MaxUint64},
		{"seq_at_boundaries", 100, 100, 1_000_000, 100, 1_010_000},
		{"huge_gap", 500_000, 0, 0, 1_000_000, 10_000_000_000},
	}

	for _, tc := range adversarialInputs {
		t.Run(tc.name, func(t *testing.T) {
			// Must not panic
			require.NotPanics(t, func() {
				result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)

				// Monotonicity: when upper >= lower, result should be >= lowerTsbpd
				if tc.upperTsbpd >= tc.lowerTsbpd {
					require.GreaterOrEqual(t, result, tc.lowerTsbpd,
						"result should respect monotonicity")
				}
			})
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// updateInterPacketInterval Tests (EWMA)
// ═══════════════════════════════════════════════════════════════════════════

func TestUpdateInterPacketInterval(t *testing.T) {
	tests := []struct {
		name          string
		nowUs         uint64
		lastArrivalUs uint64
		oldInterval   uint64
		wantInterval  uint64
		wantValid     bool
	}{
		{
			name:          "first_measurement",
			nowUs:         1_001_000,
			lastArrivalUs: 1_000_000,
			oldInterval:   0,
			wantInterval:  1000, // 1ms interval
			wantValid:     true,
		},
		{
			name:          "ewma_update",
			nowUs:         1_002_000,
			lastArrivalUs: 1_001_000,
			oldInterval:   1000,
			wantInterval:  1000, // 0.875*1000 + 0.125*1000 = 1000
			wantValid:     true,
		},
		{
			name:          "ewma_increase",
			nowUs:         1_002_000,
			lastArrivalUs: 1_000_000,
			oldInterval:   1000,
			wantInterval:  1125, // 0.875*1000 + 0.125*2000 = 1125
			wantValid:     true,
		},
		{
			name:          "interval_too_small",
			nowUs:         1_000_005, // 5µs later
			lastArrivalUs: 1_000_000,
			oldInterval:   1000,
			wantInterval:  0,
			wantValid:     false, // < InterPacketIntervalMinUs
		},
		{
			name:          "interval_too_large",
			nowUs:         1_200_000, // 200ms later
			lastArrivalUs: 1_000_000,
			oldInterval:   1000,
			wantInterval:  0,
			wantValid:     false, // > InterPacketIntervalMaxUs
		},
		{
			name:          "no_previous_arrival",
			nowUs:         1_001_000,
			lastArrivalUs: 0,
			oldInterval:   1000,
			wantInterval:  0,
			wantValid:     false,
		},
		{
			name:          "time_went_backwards",
			nowUs:         999_000,
			lastArrivalUs: 1_000_000,
			oldInterval:   1000,
			wantInterval:  0,
			wantValid:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			newInterval, valid := updateInterPacketInterval(tc.nowUs, tc.lastArrivalUs, tc.oldInterval)
			require.Equal(t, tc.wantValid, valid)
			if valid {
				require.Equal(t, tc.wantInterval, newInterval)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Locking Variant Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestInsertWithTsbpdLocking(t *testing.T) {
	t.Parallel()

	nb := newNakBtree(32)

	// Insert with locking
	nb.InsertWithTsbpdLocking(100, 5_000_000)
	nb.InsertWithTsbpdLocking(101, 5_001_000)

	require.Equal(t, 2, nb.LenLocking())
}

func TestInsertBatchWithTsbpdLocking(t *testing.T) {
	t.Parallel()

	nb := newNakBtree(32)

	seqs := []uint32{100, 101, 102}
	tsbpds := []uint64{5_000_000, 5_001_000, 5_002_000}

	inserted := nb.InsertBatchWithTsbpdLocking(seqs, tsbpds)
	require.Equal(t, 3, inserted)
	require.Equal(t, 3, nb.LenLocking())
}

func TestDeleteBeforeTsbpdLocking(t *testing.T) {
	t.Parallel()

	nb := newNakBtree(32)

	// Insert entries
	nb.InsertWithTsbpdLocking(100, 500_000)
	nb.InsertWithTsbpdLocking(101, 600_000)
	nb.InsertWithTsbpdLocking(102, 2_000_000)

	// Delete entries with TSBPD < 1s
	expired := nb.DeleteBeforeTsbpdLocking(1_000_000)

	require.Equal(t, 2, expired)
	require.Equal(t, 1, nb.LenLocking())
}

