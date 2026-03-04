package receive

import (
	"sync/atomic"
	"testing"

	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Phase 2.3: NAK Generation Edge Cases
// Tests for TSBPD estimation, EWMA warm-up, and expiry threshold calculation.
// These functions had 0% or low coverage.
// =============================================================================

// =============================================================================
// estimateTsbpdForSeq Tests
// Tests linear interpolation for TSBPD estimation with edge cases.
// =============================================================================

func TestEstimateTsbpdForSeq_TableDriven(t *testing.T) {
	testCases := []struct {
		name       string
		missingSeq uint32
		lowerSeq   uint32
		lowerTsbpd uint64
		upperSeq   uint32
		upperTsbpd uint64
		expected   uint64
	}{
		// Normal interpolation cases
		{
			name:       "Midpoint interpolation",
			missingSeq: 50,
			lowerSeq:   0,
			lowerTsbpd: 1000000,
			upperSeq:   100,
			upperTsbpd: 2000000,
			expected:   1500000, // 1M + 50/100 * 1M = 1.5M
		},
		{
			name:       "Quarter point interpolation",
			missingSeq: 25,
			lowerSeq:   0,
			lowerTsbpd: 1000000,
			upperSeq:   100,
			upperTsbpd: 2000000,
			expected:   1250000, // 1M + 25/100 * 1M = 1.25M
		},
		{
			name:       "Three quarter point",
			missingSeq: 75,
			lowerSeq:   0,
			lowerTsbpd: 1000000,
			upperSeq:   100,
			upperTsbpd: 2000000,
			expected:   1750000,
		},
		{
			name:       "At lower boundary",
			missingSeq: 0,
			lowerSeq:   0,
			lowerTsbpd: 1000000,
			upperSeq:   100,
			upperTsbpd: 2000000,
			expected:   1000000, // At lower = lower TSBPD
		},
		{
			name:       "At upper boundary",
			missingSeq: 100,
			lowerSeq:   0,
			lowerTsbpd: 1000000,
			upperSeq:   100,
			upperTsbpd: 2000000,
			expected:   2000000, // At upper = upper TSBPD
		},

		// Guard cases - inverted/equal TSBPD
		{
			name:       "Guard: inverted TSBPD (upper < lower)",
			missingSeq: 50,
			lowerSeq:   0,
			lowerTsbpd: 2000000,
			upperSeq:   100,
			upperTsbpd: 1000000, // Inverted!
			expected:   2000000, // Returns lowerTsbpd
		},
		{
			name:       "Guard: equal TSBPD",
			missingSeq: 50,
			lowerSeq:   0,
			lowerTsbpd: 1500000,
			upperSeq:   100,
			upperTsbpd: 1500000, // Equal
			expected:   1500000, // Returns lowerTsbpd
		},

		// Guard cases - equal sequence
		{
			name:       "Guard: equal sequences",
			missingSeq: 50,
			lowerSeq:   100,
			lowerTsbpd: 1000000,
			upperSeq:   100, // Same as lowerSeq
			upperTsbpd: 2000000,
			expected:   1000000, // Returns lowerTsbpd
		},

		// Wraparound cases (31-bit sequence arithmetic)
		{
			name:       "Wraparound: near max sequence",
			missingSeq: 0x7FFFFF00,
			lowerSeq:   0x7FFFFE00,
			lowerTsbpd: 1000000,
			upperSeq:   0x7FFFFF80,
			upperTsbpd: 1000384, // ~384 packets at ~1us each
			expected:   1000256, // Interpolate within range
		},
		{
			name:       "Wraparound: across boundary",
			missingSeq: 50, // After wrap
			lowerSeq:   0x7FFFFFFE,
			lowerTsbpd: 1000000,
			upperSeq:   100, // After wrap
			upperTsbpd: 1000102,
			expected:   1000052, // ~52 packets from lower
		},

		// Small range cases
		{
			name:       "Small range: 2 packets",
			missingSeq: 5,
			lowerSeq:   4,
			lowerTsbpd: 1000000,
			upperSeq:   6,
			upperTsbpd: 1000020,
			expected:   1000010, // Midpoint
		},
		{
			name:       "Small range: adjacent",
			missingSeq: 5,
			lowerSeq:   4,
			lowerTsbpd: 1000000,
			upperSeq:   5,
			upperTsbpd: 1000010,
			expected:   1000010, // At upper
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)
			require.Equal(t, tc.expected, result, "TSBPD estimate mismatch")
		})
	}
}

// =============================================================================
// isEWMAWarm Tests (0% coverage)
// Tests EWMA warm-up detection for inter-packet interval estimation.
// =============================================================================

func TestIsEWMAWarm_TableDriven(t *testing.T) {
	testCases := []struct {
		name                string
		ewmaWarmupThreshold uint32
		sampleCount         uint32
		expectedWarm        bool
	}{
		{
			name:                "Threshold 0 (disabled) - always warm",
			ewmaWarmupThreshold: 0,
			sampleCount:         0,
			expectedWarm:        true,
		},
		{
			name:                "Threshold 0 with samples - always warm",
			ewmaWarmupThreshold: 0,
			sampleCount:         100,
			expectedWarm:        true,
		},
		{
			name:                "Below threshold - cold",
			ewmaWarmupThreshold: 100,
			sampleCount:         50,
			expectedWarm:        false,
		},
		{
			name:                "At threshold - warm",
			ewmaWarmupThreshold: 100,
			sampleCount:         100,
			expectedWarm:        true,
		},
		{
			name:                "Above threshold - warm",
			ewmaWarmupThreshold: 100,
			sampleCount:         200,
			expectedWarm:        true,
		},
		{
			name:                "Just below threshold - cold",
			ewmaWarmupThreshold: 100,
			sampleCount:         99,
			expectedWarm:        false,
		},
		{
			name:                "Large threshold, zero samples - cold",
			ewmaWarmupThreshold: 10000,
			sampleCount:         0,
			expectedWarm:        false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{
				ewmaWarmupThreshold: tc.ewmaWarmupThreshold,
				interPacketEst: interPacketEstimator{
					sampleCount: atomic.Uint32{},
				},
			}
			r.interPacketEst.sampleCount.Store(tc.sampleCount)

			result := r.isEWMAWarm()
			require.Equal(t, tc.expectedWarm, result, "isEWMAWarm mismatch")
		})
	}
}

// =============================================================================
// estimateTsbpdFallback Tests (0% coverage)
// Tests fallback TSBPD estimation when linear interpolation not possible.
// =============================================================================

func TestEstimateTsbpdFallback_TableDriven(t *testing.T) {
	testCases := []struct {
		name             string
		missingSeq       uint32
		refSeq           uint32
		refTsbpd         uint64
		ewmaWarm         bool
		avgIntervalUs    uint64
		tsbpdDelay       uint64
		expectedEstimate uint64
		expectColdMetric bool
	}{
		// Cold EWMA cases
		{
			name:             "Cold EWMA - uses tsbpdDelay fallback",
			missingSeq:       100,
			refSeq:           50,
			refTsbpd:         1000000,
			ewmaWarm:         false,
			avgIntervalUs:    1000, // Ignored when cold
			tsbpdDelay:       120000,
			expectedEstimate: 1120000, // refTsbpd + tsbpdDelay
			expectColdMetric: true,
		},
		{
			name:             "Cold EWMA - zero tsbpdDelay",
			missingSeq:       100,
			refSeq:           50,
			refTsbpd:         1000000,
			ewmaWarm:         false,
			avgIntervalUs:    1000,
			tsbpdDelay:       0,
			expectedEstimate: 1000000, // refTsbpd + 0
			expectColdMetric: true,
		},

		// Warm EWMA cases
		{
			name:             "Warm EWMA - forward gap",
			missingSeq:       100,
			refSeq:           50,
			refTsbpd:         1000000,
			ewmaWarm:         true,
			avgIntervalUs:    1000, // 1ms per packet
			tsbpdDelay:       120000,
			expectedEstimate: 1050000, // refTsbpd + 50 * 1000
			expectColdMetric: false,
		},
		// Note: Backward gaps (missingSeq < refSeq) are not a valid use case
		// for estimateTsbpdFallback since it uses unsigned SeqSub
		{
			name:             "Warm EWMA - zero interval uses default",
			missingSeq:       100,
			refSeq:           50,
			refTsbpd:         1000000,
			ewmaWarm:         true,
			avgIntervalUs:    0, // Will use InterPacketIntervalDefaultUs
			tsbpdDelay:       120000,
			expectedEstimate: 1000000 + 50*InterPacketIntervalDefaultUs,
			expectColdMetric: false,
		},
		{
			name:             "Warm EWMA - same sequence",
			missingSeq:       100,
			refSeq:           100,
			refTsbpd:         1000000,
			ewmaWarm:         true,
			avgIntervalUs:    1000,
			tsbpdDelay:       120000,
			expectedEstimate: 1000000, // refTsbpd + 0 * 1000
			expectColdMetric: false,
		},
		{
			name:             "Warm EWMA - large forward gap",
			missingSeq:       1000,
			refSeq:           0,
			refTsbpd:         1000000,
			ewmaWarm:         true,
			avgIntervalUs:    1000, // 1ms per packet
			tsbpdDelay:       120000,
			expectedEstimate: 2000000, // refTsbpd + 1000 * 1000
			expectColdMetric: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := &metrics.ConnectionMetrics{}
			r := &receiver{
				ewmaWarmupThreshold: 100,
				tsbpdDelay:          tc.tsbpdDelay,
				metrics:             m,
				interPacketEst: interPacketEstimator{
					sampleCount:   atomic.Uint32{},
					avgIntervalUs: atomic.Uint64{},
				},
			}

			// Set warm state
			if tc.ewmaWarm {
				r.interPacketEst.sampleCount.Store(200) // Above threshold
			} else {
				r.interPacketEst.sampleCount.Store(0) // Below threshold
			}
			r.interPacketEst.avgIntervalUs.Store(tc.avgIntervalUs)

			result := r.estimateTsbpdFallback(tc.missingSeq, tc.refSeq, tc.refTsbpd)
			require.Equal(t, tc.expectedEstimate, result, "TSBPD fallback estimate mismatch")

			// Check cold metric
			if tc.expectColdMetric {
				require.Equal(t, uint64(1), m.NakTsbpdEstColdFallback.Load(),
					"Expected cold fallback metric increment")
			} else {
				require.Equal(t, uint64(0), m.NakTsbpdEstColdFallback.Load(),
					"Unexpected cold fallback metric increment")
			}
		})
	}
}

// =============================================================================
// calculateExpiryThreshold Tests (28.6% coverage)
// Tests NAK entry expiry threshold calculation.
// =============================================================================

// mockRTTProvider implements RTTProvider interface for testing
type mockRTTProvider struct {
	rtoUs uint64
}

func (m *mockRTTProvider) RTOUs() uint64 {
	return m.rtoUs
}

func (m *mockRTTProvider) RTTUs() uint64 {
	return m.rtoUs / 4 // Approximate RTT from RTO
}

func (m *mockRTTProvider) RTTVarUs() uint64 {
	return m.rtoUs / 8
}

func (m *mockRTTProvider) NAKInterval() float64 {
	return float64(m.rtoUs) / 1000.0
}

func TestCalculateExpiryThreshold_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		hasRTT          bool
		rtoUs           uint64
		nakExpiryMargin float64
		nowUs           uint64
		expectedResult  uint64
	}{
		// No RTT provider cases
		{
			name:            "No RTT provider - returns 0",
			hasRTT:          false,
			rtoUs:           0,
			nakExpiryMargin: 0.5,
			nowUs:           1000000,
			expectedResult:  0, // Fallback indicator
		},

		// RTT not measured (rtoUs = 0)
		{
			name:            "RTT not measured - returns 0",
			hasRTT:          true,
			rtoUs:           0,
			nakExpiryMargin: 0.5,
			nowUs:           1000000,
			expectedResult:  0, // Fallback indicator
		},

		// Normal cases with various margins
		{
			name:            "Normal RTO 100ms, margin 0",
			hasRTT:          true,
			rtoUs:           100000, // 100ms
			nakExpiryMargin: 0.0,
			nowUs:           1000000,
			expectedResult:  1100000, // now + RTO * 1.0
		},
		{
			name:            "Normal RTO 100ms, margin 50%",
			hasRTT:          true,
			rtoUs:           100000,
			nakExpiryMargin: 0.5,
			nowUs:           1000000,
			expectedResult:  1150000, // now + RTO * 1.5
		},
		{
			name:            "Normal RTO 100ms, margin 100%",
			hasRTT:          true,
			rtoUs:           100000,
			nakExpiryMargin: 1.0,
			nowUs:           1000000,
			expectedResult:  1200000, // now + RTO * 2.0
		},
		{
			name:            "Small RTO 10ms, margin 25%",
			hasRTT:          true,
			rtoUs:           10000,
			nakExpiryMargin: 0.25,
			nowUs:           500000,
			expectedResult:  512500, // now + 10000 * 1.25
		},
		{
			name:            "Large RTO 1s, margin 50%",
			hasRTT:          true,
			rtoUs:           1000000, // 1 second
			nakExpiryMargin: 0.5,
			nowUs:           2000000,
			expectedResult:  3500000, // now + 1000000 * 1.5
		},

		// Edge cases
		{
			name:            "Zero nowUs",
			hasRTT:          true,
			rtoUs:           100000,
			nakExpiryMargin: 0.5,
			nowUs:           0,
			expectedResult:  150000, // 0 + RTO * 1.5
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{
				nakExpiryMargin: tc.nakExpiryMargin,
			}

			if tc.hasRTT {
				r.rtt = &mockRTTProvider{rtoUs: tc.rtoUs}
			}

			result := r.calculateExpiryThreshold(tc.nowUs)
			require.Equal(t, tc.expectedResult, result, "Expiry threshold mismatch")
		})
	}
}

// =============================================================================
// updateInterPacketInterval Tests
// Tests EWMA update for inter-packet interval estimation.
// Note: updateInterPacketInterval is a package-level function with signature:
// func updateInterPacketInterval(nowUs, lastArrivalUs, oldInterval uint64) (newInterval uint64, valid bool)
// =============================================================================

func TestUpdateInterPacketInterval_Edge_TableDriven(t *testing.T) {
	testCases := []struct {
		name             string
		nowUs            uint64
		lastArrivalUs    uint64
		oldInterval      uint64
		expectedValid    bool
		minExpectedRange uint64
		maxExpectedRange uint64
	}{
		{
			name:             "Normal update - 1ms interval",
			nowUs:            1001000,
			lastArrivalUs:    1000000,
			oldInterval:      1000,
			expectedValid:    true,
			minExpectedRange: 900,
			maxExpectedRange: 1100,
		},
		{
			name:             "First update from zero interval",
			nowUs:            1001000,
			lastArrivalUs:    1000000,
			oldInterval:      0,
			expectedValid:    true,
			minExpectedRange: 900,
			maxExpectedRange: 1100,
		},
		{
			name:          "Same time - zero interval ignored",
			nowUs:         1000000,
			lastArrivalUs: 1000000,
			oldInterval:   1000,
			expectedValid: false,
		},
		{
			name:          "Backward time - invalid",
			nowUs:         999000,
			lastArrivalUs: 1000000,
			oldInterval:   1000,
			expectedValid: false,
		},
		{
			name:             "Large interval - 10ms",
			nowUs:            1010000,
			lastArrivalUs:    1000000,
			oldInterval:      1000,
			expectedValid:    true,
			minExpectedRange: 1000,
			maxExpectedRange: 10000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			newValue, valid := updateInterPacketInterval(tc.nowUs, tc.lastArrivalUs, tc.oldInterval)

			require.Equal(t, tc.expectedValid, valid, "Valid flag mismatch")

			if tc.expectedValid {
				require.GreaterOrEqual(t, newValue, tc.minExpectedRange,
					"New value below expected range")
				require.LessOrEqual(t, newValue, tc.maxExpectedRange,
					"New value above expected range")
			}
		})
	}
}

// Note: periodicNAK dispatch tests are covered by existing integration tests
// in nak_periodic_table_test.go as they require full receiver initialization.

// =============================================================================
// Sequence Wraparound Edge Cases
// Tests NAK estimation at 31-bit sequence number boundaries.
// =============================================================================

func TestEstimateTsbpdForSeq_SequenceWraparound(t *testing.T) {
	const maxSeq = uint32(0x7FFFFFFF) // 31-bit max

	testCases := []struct {
		name       string
		missingSeq uint32
		lowerSeq   uint32
		lowerTsbpd uint64
		upperSeq   uint32
		upperTsbpd uint64
		expected   uint64
	}{
		{
			name:       "Gap spanning wraparound boundary",
			missingSeq: 10,          // Just after wrap
			lowerSeq:   maxSeq - 10, // Before wrap
			lowerTsbpd: 1000000,
			upperSeq:   20,      // After wrap
			upperTsbpd: 1000031, // ~31 packets
			expected:   1000021, // Interpolated
		},
		{
			name:       "Missing seq at max boundary",
			missingSeq: maxSeq,
			lowerSeq:   maxSeq - 10,
			lowerTsbpd: 1000000,
			upperSeq:   maxSeq,
			upperTsbpd: 1000010,
			expected:   1000010, // At upper
		},
		{
			name:       "Missing seq at zero after wrap",
			missingSeq: 0,
			lowerSeq:   maxSeq - 5,
			lowerTsbpd: 1000000,
			upperSeq:   5,
			upperTsbpd: 1000011,
			expected:   1000006, // 6 packets from lower
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := estimateTsbpdForSeq(tc.missingSeq, tc.lowerSeq, tc.lowerTsbpd, tc.upperSeq, tc.upperTsbpd)
			// For wraparound cases, allow some tolerance
			diff := int64(result) - int64(tc.expected)
			if diff < 0 {
				diff = -diff
			}
			require.LessOrEqual(t, diff, int64(5),
				"TSBPD estimate mismatch: got %d, expected %d", result, tc.expected)
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkEstimateTsbpdForSeq_EdgeCases(b *testing.B) {
	// Test with various edge case inputs
	for i := 0; i < b.N; i++ {
		_ = estimateTsbpdForSeq(50, 0, 1000000, 100, 2000000)
	}
}

func BenchmarkIsEWMAWarm(b *testing.B) {
	r := &receiver{
		ewmaWarmupThreshold: 100,
		interPacketEst: interPacketEstimator{
			sampleCount: atomic.Uint32{},
		},
	}
	r.interPacketEst.sampleCount.Store(200)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.isEWMAWarm()
	}
}

func BenchmarkCalculateExpiryThreshold(b *testing.B) {
	r := &receiver{
		nakExpiryMargin: 0.5,
		rtt:             &mockRTTProvider{rtoUs: 100000},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.calculateExpiryThreshold(1000000)
	}
}
