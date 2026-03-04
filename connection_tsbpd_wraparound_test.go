package srt

import (
	"testing"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Phase 1.3: TSBPD Timestamp Wraparound Tests
// =============================================================================
// Tests for the TSBPD time base calculation at the 32-bit microsecond timestamp
// wraparound boundary (~71.58 minutes).
//
// The 32-bit timestamp wraps every ~4.29 billion microseconds (71.58 minutes).
// SRT handles this by:
// 1. Detecting when timestamp approaches MAX (within 30s of wrap)
// 2. Entering "wrap period" mode
// 3. Adding MAX_TIMESTAMP+1 to offset when packets arrive with small timestamps
// 4. Exiting wrap period when timestamp is between 30s and 60s
//
// Reference: SRT RFC Section 4.5.1.1 "TSBPD Time Base Calculation"
// Reference: documentation/unit_test_coverage_improvement_plan.md
// =============================================================================

const (
	// Key constants from packet.MAX_TIMESTAMP
	maxTimestamp uint32 = 0xFFFFFFFF // 4,294,967,295 µs (~71.58 minutes)

	// Wrap detection boundaries (in microseconds)
	thirtySecondsUs uint32 = 30_000_000                         // 30 seconds
	sixtySecondsUs  uint32 = 60_000_000                         // 60 seconds
	wrapEntryPoint  uint32 = maxTimestamp - thirtySecondsUs + 1 // First timestamp that triggers wrap period

	// Common test values
	testTsbpdTimeBase uint64 = 1_000_000_000 // 1000 seconds (arbitrary base)
	testTsbpdDelay    uint64 = 3_000_000     // 3 seconds delay
	testTsbpdDrift    uint64 = 0             // No drift for simplicity
)

// =============================================================================
// TSBPD Wrap State Machine Tests
// =============================================================================
// Tests the state machine that manages wrap period entry and exit.
//
// States:
// - NOT in wrap period: normal operation
// - IN wrap period: handling timestamp wraparound
//
// Transitions:
// - NOT -> IN: timestamp > MAX_TIMESTAMP - 30s
// - IN -> NOT: timestamp in [30s, 60s] range (and offset incremented)
// =============================================================================

func TestTSBPD_WrapPeriodStateMachine_TableDriven(t *testing.T) {
	testCases := []struct {
		name                  string
		initialWrapPeriod     bool
		initialOffset         uint64
		packetTimestamp       uint32
		expectWrapPeriod      bool // Expected state after processing
		expectOffsetIncreased bool // Whether offset should increase by MAX+1
	}{
		// =================================================================
		// NOT in wrap period -> transitions
		// =================================================================
		{
			name:                  "normal: far from wrap boundary",
			initialWrapPeriod:     false,
			initialOffset:         0,
			packetTimestamp:       1_000_000, // 1 second - far from boundary
			expectWrapPeriod:      false,
			expectOffsetIncreased: false,
		},
		{
			name:                  "normal: at half of max timestamp",
			initialWrapPeriod:     false,
			initialOffset:         0,
			packetTimestamp:       maxTimestamp / 2, // ~35.79 minutes
			expectWrapPeriod:      false,
			expectOffsetIncreased: false,
		},
		{
			name:                  "normal: just below wrap entry threshold",
			initialWrapPeriod:     false,
			initialOffset:         0,
			packetTimestamp:       maxTimestamp - thirtySecondsUs, // Exactly at threshold boundary
			expectWrapPeriod:      false,
			expectOffsetIncreased: false,
		},
		{
			name:                  "ENTER wrap: just above threshold",
			initialWrapPeriod:     false,
			initialOffset:         0,
			packetTimestamp:       maxTimestamp - thirtySecondsUs + 1, // First timestamp that triggers wrap
			expectWrapPeriod:      true,
			expectOffsetIncreased: false,
		},
		{
			name:                  "ENTER wrap: at MAX_TIMESTAMP",
			initialWrapPeriod:     false,
			initialOffset:         0,
			packetTimestamp:       maxTimestamp, // Maximum possible timestamp
			expectWrapPeriod:      true,
			expectOffsetIncreased: false,
		},
		{
			name:                  "ENTER wrap: 1 second before max",
			initialWrapPeriod:     false,
			initialOffset:         0,
			packetTimestamp:       maxTimestamp - 1_000_000, // 1 second before max
			expectWrapPeriod:      true,
			expectOffsetIncreased: false,
		},

		// =================================================================
		// IN wrap period -> stay in wrap period
		// =================================================================
		{
			name:                  "IN wrap: high timestamp stays in wrap",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       maxTimestamp - 10_000_000, // 10 seconds before max
			expectWrapPeriod:      true,
			expectOffsetIncreased: false,
		},
		{
			name:                  "IN wrap: very small timestamp (wrapped packet)",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       1_000_000, // 1 second (packet that already wrapped)
			expectWrapPeriod:      true,      // Still in wrap period
			expectOffsetIncreased: false,     // But local offset adjustment happens, not permanent
		},
		{
			name:                  "IN wrap: timestamp just below exit threshold",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       thirtySecondsUs - 1, // Just below 30s
			expectWrapPeriod:      true,                // Still in wrap (hasn't reached exit window)
			expectOffsetIncreased: false,
		},

		// =================================================================
		// IN wrap period -> EXIT wrap period
		// =================================================================
		{
			name:                  "EXIT wrap: timestamp at 30s (start of exit window)",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       thirtySecondsUs, // Exactly 30 seconds
			expectWrapPeriod:      false,
			expectOffsetIncreased: true, // Offset increases when exiting
		},
		{
			name:                  "EXIT wrap: timestamp at 45s (middle of exit window)",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       45_000_000, // 45 seconds
			expectWrapPeriod:      false,
			expectOffsetIncreased: true,
		},
		{
			name:                  "EXIT wrap: timestamp at 60s (end of exit window)",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       sixtySecondsUs, // Exactly 60 seconds
			expectWrapPeriod:      false,
			expectOffsetIncreased: true,
		},
		{
			name:                  "IN wrap: timestamp just above exit window",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       sixtySecondsUs + 1, // Just above 60s
			expectWrapPeriod:      true,               // Stays in wrap (above exit window)
			expectOffsetIncreased: false,
		},
		{
			name:                  "IN wrap: timestamp well above exit window",
			initialWrapPeriod:     true,
			initialOffset:         0,
			packetTimestamp:       100_000_000, // 100 seconds
			expectWrapPeriod:      true,        // Still in wrap
			expectOffsetIncreased: false,
		},

		// =================================================================
		// Multiple wrap cycles (offset already incremented)
		// =================================================================
		{
			name:                  "second wrap cycle: enter wrap period",
			initialWrapPeriod:     false,
			initialOffset:         uint64(maxTimestamp) + 1, // Already wrapped once
			packetTimestamp:       maxTimestamp - 10_000_000,
			expectWrapPeriod:      true,
			expectOffsetIncreased: false, // No additional increment on entry
		},
		{
			name:                  "second wrap cycle: exit wrap period",
			initialWrapPeriod:     true,
			initialOffset:         uint64(maxTimestamp) + 1, // Already wrapped once
			packetTimestamp:       45_000_000,               // In exit window
			expectWrapPeriod:      false,
			expectOffsetIncreased: true, // Another MAX+1 added
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the wrap period state machine logic from connection_handlers.go
			wrapPeriod := tc.initialWrapPeriod
			offset := tc.initialOffset
			timestamp := tc.packetTimestamp

			// State machine logic (lines 163-174 in connection_handlers.go)
			if !wrapPeriod {
				// Check for wrap period entry
				if timestamp > maxTimestamp-thirtySecondsUs {
					wrapPeriod = true
				}
			} else {
				// Check for wrap period exit
				if timestamp >= thirtySecondsUs && timestamp <= sixtySecondsUs {
					wrapPeriod = false
					offset += uint64(maxTimestamp) + 1
				}
			}

			require.Equal(t, tc.expectWrapPeriod, wrapPeriod,
				"wrapPeriod: got %v, want %v (timestamp=%d)",
				wrapPeriod, tc.expectWrapPeriod, timestamp)

			expectedOffset := tc.initialOffset
			if tc.expectOffsetIncreased {
				expectedOffset += uint64(maxTimestamp) + 1
			}
			require.Equal(t, expectedOffset, offset,
				"offset: got %d, want %d", offset, expectedOffset)
		})
	}
}

// =============================================================================
// TSBPD Time Calculation Tests
// =============================================================================
// Tests the full PktTsbpdTime calculation including local offset adjustment
// for packets that have already wrapped while in wrap period.
//
// Formula: PktTsbpdTime = tsbpdTimeBase + tsbpdTimeBaseOffset + timestamp + tsbpdDelay + tsbpdDrift
//
// During wrap period, packets with timestamp < 30s get an additional
// MAX_TIMESTAMP+1 added to their local offset (not the global offset).
// =============================================================================

func TestTSBPD_TimeCalculation_TableDriven(t *testing.T) {
	testCases := []struct {
		name            string
		tsbpdTimeBase   uint64
		tsbpdOffset     uint64
		tsbpdWrapPeriod bool
		tsbpdDelay      uint64
		tsbpdDrift      uint64
		packetTimestamp uint32
		expectedTime    uint64
	}{
		// =================================================================
		// Normal operation (no wrap period)
		// =================================================================
		{
			name:            "normal: simple calculation",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 5_000_000, // 5 seconds
			// Expected: 1,000,000,000 + 0 + 5,000,000 + 3,000,000 + 0 = 1,008,000,000
			expectedTime: testTsbpdTimeBase + 5_000_000 + testTsbpdDelay,
		},
		{
			name:            "normal: with drift",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      100_000, // 100ms drift
			packetTimestamp: 5_000_000,
			expectedTime:    testTsbpdTimeBase + 5_000_000 + testTsbpdDelay + 100_000,
		},
		{
			name:            "normal: high timestamp near wrap threshold",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: maxTimestamp - thirtySecondsUs, // At boundary
			expectedTime:    testTsbpdTimeBase + uint64(maxTimestamp-thirtySecondsUs) + testTsbpdDelay,
		},

		// =================================================================
		// In wrap period - high timestamps (no local adjustment)
		// =================================================================
		{
			name:            "wrap period: high timestamp (no local adjust)",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: maxTimestamp - 10_000_000, // 10s before max, still high
			// No local adjustment because timestamp >= 30s
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp-10_000_000) + testTsbpdDelay,
		},
		{
			name:            "wrap period: timestamp at MAX",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: maxTimestamp,
			// No local adjustment because timestamp >= 30s
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + testTsbpdDelay,
		},
		{
			name:            "wrap period: timestamp exactly at 30s",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: thirtySecondsUs, // Exactly 30s
			// No local adjustment because timestamp >= 30s
			expectedTime: testTsbpdTimeBase + uint64(thirtySecondsUs) + testTsbpdDelay,
		},

		// =================================================================
		// In wrap period - LOW timestamps (LOCAL adjustment needed)
		// These are packets that have already wrapped around
		// =================================================================
		{
			name:            "wrap period: wrapped packet at 1s (local adjust)",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 1_000_000, // 1 second (wrapped)
			// LOCAL adjustment: add MAX_TIMESTAMP+1 because timestamp < 30s
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + 1 + 1_000_000 + testTsbpdDelay,
		},
		{
			name:            "wrap period: wrapped packet at 0",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 0, // Timestamp 0 (just wrapped)
			// LOCAL adjustment: add MAX_TIMESTAMP+1
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + 1 + 0 + testTsbpdDelay,
		},
		{
			name:            "wrap period: wrapped packet at 29s (just below threshold)",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: thirtySecondsUs - 1, // Just below 30s
			// LOCAL adjustment: add MAX_TIMESTAMP+1
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + 1 + uint64(thirtySecondsUs-1) + testTsbpdDelay,
		},

		// =================================================================
		// After wrap (offset already incremented)
		// =================================================================
		{
			name:            "after wrap: normal packet with offset",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     uint64(maxTimestamp) + 1, // Already wrapped once
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 45_000_000, // 45 seconds (normal timestamp in new cycle)
			// Uses global offset
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + 1 + 45_000_000 + testTsbpdDelay,
		},
		{
			name:            "after wrap: approaching second wrap",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     uint64(maxTimestamp) + 1,
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: maxTimestamp - thirtySecondsUs, // Near wrap again
			expectedTime:    testTsbpdTimeBase + uint64(maxTimestamp) + 1 + uint64(maxTimestamp-thirtySecondsUs) + testTsbpdDelay,
		},

		// =================================================================
		// Second wrap cycle
		// =================================================================
		{
			name:            "second wrap: in wrap period with high timestamp",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     uint64(maxTimestamp) + 1, // First wrap offset
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: maxTimestamp - 5_000_000, // 5s before max
			// No local adjustment
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + 1 + uint64(maxTimestamp-5_000_000) + testTsbpdDelay,
		},
		{
			name:            "second wrap: wrapped packet needs local adjust",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     uint64(maxTimestamp) + 1, // First wrap offset
			tsbpdWrapPeriod: true,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 5_000_000, // 5 seconds (wrapped)
			// LOCAL adjustment adds another MAX_TIMESTAMP+1
			expectedTime: testTsbpdTimeBase + uint64(maxTimestamp) + 1 + uint64(maxTimestamp) + 1 + 5_000_000 + testTsbpdDelay,
		},
		{
			name:            "after second wrap: offset doubled",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     2 * (uint64(maxTimestamp) + 1), // Second wrap offset
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 60_000_000, // 60 seconds
			expectedTime:    testTsbpdTimeBase + 2*(uint64(maxTimestamp)+1) + 60_000_000 + testTsbpdDelay,
		},

		// =================================================================
		// Edge cases with zero values
		// =================================================================
		{
			name:            "zero base time",
			tsbpdTimeBase:   0,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: false,
			tsbpdDelay:      testTsbpdDelay,
			tsbpdDrift:      0,
			packetTimestamp: 10_000_000,
			expectedTime:    10_000_000 + testTsbpdDelay,
		},
		{
			name:            "zero delay",
			tsbpdTimeBase:   testTsbpdTimeBase,
			tsbpdOffset:     0,
			tsbpdWrapPeriod: false,
			tsbpdDelay:      0,
			tsbpdDrift:      0,
			packetTimestamp: 10_000_000,
			expectedTime:    testTsbpdTimeBase + 10_000_000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Calculate TSBPD time using the logic from connection_handlers.go (lines 176-183)
			localOffset := tc.tsbpdOffset

			// Local offset adjustment for wrapped packets (line 177-180)
			if tc.tsbpdWrapPeriod {
				if tc.packetTimestamp < thirtySecondsUs {
					localOffset += uint64(maxTimestamp) + 1
				}
			}

			// Calculate PktTsbpdTime (line 183)
			pktTsbpdTime := tc.tsbpdTimeBase + localOffset + uint64(tc.packetTimestamp) + tc.tsbpdDelay + tc.tsbpdDrift

			require.Equal(t, tc.expectedTime, pktTsbpdTime,
				"PktTsbpdTime calculation: got %d, want %d\n"+
					"  base=%d, offset=%d (local=%d), timestamp=%d, delay=%d, drift=%d",
				pktTsbpdTime, tc.expectedTime,
				tc.tsbpdTimeBase, tc.tsbpdOffset, localOffset, tc.packetTimestamp, tc.tsbpdDelay, tc.tsbpdDrift)
		})
	}
}

// =============================================================================
// Monotonicity Tests
// =============================================================================
// Tests that PktTsbpdTime increases monotonically across wrap boundaries.
// This is critical for TSBPD delivery ordering.
// =============================================================================

func TestTSBPD_MonotonicityAcrossWrap(t *testing.T) {
	// Test that TSBPD times are monotonically increasing across the wrap boundary
	// even with out-of-order packet arrival

	type packetSim struct {
		timestamp       uint32
		description     string
		wrapPeriodAfter bool   // Expected wrap period after processing
		offsetAfter     uint64 // Expected global offset after processing
	}

	// Simulate a sequence of packets arriving across the wrap boundary
	packets := []packetSim{
		// Pre-wrap packets
		{maxTimestamp - 40_000_000, "40s before wrap", false, 0},
		{maxTimestamp - 35_000_000, "35s before wrap", false, 0},
		// Enter wrap period
		{maxTimestamp - 25_000_000, "25s before wrap (enters wrap)", true, 0},
		{maxTimestamp - 10_000_000, "10s before wrap", true, 0},
		{maxTimestamp - 1_000_000, "1s before wrap", true, 0},
		// Wrapped packets (small timestamps, but should have higher TSBPD times)
		{5_000_000, "5s after wrap (small timestamp)", true, 0},
		{15_000_000, "15s after wrap", true, 0},
		{25_000_000, "25s after wrap", true, 0},
		// Exit wrap period
		{35_000_000, "35s after wrap (exits wrap)", false, uint64(maxTimestamp) + 1},
		{50_000_000, "50s after wrap", false, uint64(maxTimestamp) + 1},
	}

	tsbpdTimeBase := testTsbpdTimeBase
	tsbpdDelay := testTsbpdDelay
	tsbpdDrift := uint64(0)

	wrapPeriod := false
	globalOffset := uint64(0)
	var prevTsbpdTime uint64

	for i, pkt := range packets {
		// Update state machine
		if !wrapPeriod {
			if pkt.timestamp > maxTimestamp-thirtySecondsUs {
				wrapPeriod = true
			}
		} else {
			if pkt.timestamp >= thirtySecondsUs && pkt.timestamp <= sixtySecondsUs {
				wrapPeriod = false
				globalOffset += uint64(maxTimestamp) + 1
			}
		}

		// Calculate local offset
		localOffset := globalOffset
		if wrapPeriod && pkt.timestamp < thirtySecondsUs {
			localOffset += uint64(maxTimestamp) + 1
		}

		// Calculate TSBPD time
		tsbpdTime := tsbpdTimeBase + localOffset + uint64(pkt.timestamp) + tsbpdDelay + tsbpdDrift

		// Verify state matches expectations
		require.Equal(t, pkt.wrapPeriodAfter, wrapPeriod,
			"packet %d (%s): wrapPeriod got %v, want %v",
			i, pkt.description, wrapPeriod, pkt.wrapPeriodAfter)
		require.Equal(t, pkt.offsetAfter, globalOffset,
			"packet %d (%s): globalOffset got %d, want %d",
			i, pkt.description, globalOffset, pkt.offsetAfter)

		// Verify monotonicity
		if i > 0 {
			require.Greater(t, tsbpdTime, prevTsbpdTime,
				"packet %d (%s): TSBPD time %d should be > previous %d (monotonicity violation!)",
				i, pkt.description, tsbpdTime, prevTsbpdTime)
		}

		prevTsbpdTime = tsbpdTime
		t.Logf("packet %d: ts=%d, wrapPeriod=%v, globalOffset=%d, tsbpdTime=%d (%s)",
			i, pkt.timestamp, wrapPeriod, globalOffset, tsbpdTime, pkt.description)
	}
}

// =============================================================================
// Out-of-Order Packet Tests
// =============================================================================
// Tests that TSBPD handles out-of-order packets correctly across wrap boundaries.
// =============================================================================

func TestTSBPD_OutOfOrderAcrossWrap(t *testing.T) {
	testCases := []struct {
		name                 string
		existingWrapPeriod   bool
		existingOffset       uint64
		timestamp1           uint32 // First arriving packet
		timestamp2           uint32 // Second arriving packet (may be out of order)
		expectTime1LessThan2 bool   // Should TSBPD(ts1) < TSBPD(ts2)?
	}{
		{
			name:                 "normal order: both before wrap",
			existingWrapPeriod:   false,
			existingOffset:       0,
			timestamp1:           maxTimestamp - 40_000_000, // 40s before wrap
			timestamp2:           maxTimestamp - 35_000_000, // 35s before wrap
			expectTime1LessThan2: true,
		},
		{
			name:                 "normal order: both after wrap",
			existingWrapPeriod:   true,
			existingOffset:       0,
			timestamp1:           5_000_000,  // 5s after wrap
			timestamp2:           10_000_000, // 10s after wrap
			expectTime1LessThan2: true,
		},
		{
			name:                 "spanning wrap: pre-wrap then post-wrap",
			existingWrapPeriod:   true,
			existingOffset:       0,
			timestamp1:           maxTimestamp - 5_000_000, // 5s before wrap
			timestamp2:           5_000_000,                // 5s after wrap
			expectTime1LessThan2: true,                     // Post-wrap should be later
		},
		{
			name:                 "out of order: post-wrap arrives first",
			existingWrapPeriod:   true,
			existingOffset:       0,
			timestamp1:           5_000_000,                // 5s after wrap (arrives first)
			timestamp2:           maxTimestamp - 5_000_000, // 5s before wrap (arrives second, but earlier in time)
			expectTime1LessThan2: false,                    // ts2 is earlier, so TSBPD(ts2) < TSBPD(ts1)
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tsbpdTimeBase := testTsbpdTimeBase
			tsbpdDelay := testTsbpdDelay

			// Calculate TSBPD time for timestamp1
			localOffset1 := tc.existingOffset
			if tc.existingWrapPeriod && tc.timestamp1 < thirtySecondsUs {
				localOffset1 += uint64(maxTimestamp) + 1
			}
			tsbpdTime1 := tsbpdTimeBase + localOffset1 + uint64(tc.timestamp1) + tsbpdDelay

			// Calculate TSBPD time for timestamp2
			localOffset2 := tc.existingOffset
			if tc.existingWrapPeriod && tc.timestamp2 < thirtySecondsUs {
				localOffset2 += uint64(maxTimestamp) + 1
			}
			tsbpdTime2 := tsbpdTimeBase + localOffset2 + uint64(tc.timestamp2) + tsbpdDelay

			if tc.expectTime1LessThan2 {
				require.Less(t, tsbpdTime1, tsbpdTime2,
					"Expected TSBPD(ts1=%d)=%d < TSBPD(ts2=%d)=%d",
					tc.timestamp1, tsbpdTime1, tc.timestamp2, tsbpdTime2)
			} else {
				require.Greater(t, tsbpdTime1, tsbpdTime2,
					"Expected TSBPD(ts1=%d)=%d > TSBPD(ts2=%d)=%d",
					tc.timestamp1, tsbpdTime1, tc.timestamp2, tsbpdTime2)
			}
		})
	}
}

// =============================================================================
// Boundary Value Tests
// =============================================================================
// Tests exact boundary values for wrap period entry/exit.
// =============================================================================

func TestTSBPD_ExactBoundaryValues(t *testing.T) {
	// Test exact boundary values with off-by-one checks

	t.Run("wrap entry boundary", func(t *testing.T) {
		threshold := maxTimestamp - thirtySecondsUs

		// Just at threshold - should NOT enter wrap
		atThreshold := threshold
		require.False(t, atThreshold > threshold, "at threshold should not trigger wrap")

		// One above threshold - should enter wrap
		aboveThreshold := threshold + 1
		require.True(t, aboveThreshold > threshold, "above threshold should trigger wrap")
	})

	t.Run("wrap exit boundary", func(t *testing.T) {
		// Exit window is [30s, 60s] inclusive

		// Just below 30s - should NOT exit
		below30 := thirtySecondsUs - 1
		inExitWindow := below30 >= thirtySecondsUs && below30 <= sixtySecondsUs
		require.False(t, inExitWindow, "below 30s should not be in exit window")

		// At 30s - should exit
		at30 := thirtySecondsUs
		inExitWindow = at30 >= thirtySecondsUs && at30 <= sixtySecondsUs
		require.True(t, inExitWindow, "at 30s should be in exit window")

		// At 60s - should exit
		at60 := sixtySecondsUs
		inExitWindow = at60 >= thirtySecondsUs && at60 <= sixtySecondsUs
		require.True(t, inExitWindow, "at 60s should be in exit window")

		// Just above 60s - should NOT exit
		above60 := sixtySecondsUs + 1
		inExitWindow = above60 >= thirtySecondsUs && above60 <= sixtySecondsUs
		require.False(t, inExitWindow, "above 60s should not be in exit window")
	})

	t.Run("local offset adjustment boundary", func(t *testing.T) {
		wrapPeriod := true

		// Just below 30s - needs local adjustment
		below30 := thirtySecondsUs - 1
		needsLocalAdjust := wrapPeriod && below30 < thirtySecondsUs
		require.True(t, needsLocalAdjust, "below 30s in wrap period needs local adjustment")

		// At 30s - NO local adjustment (but may exit wrap)
		at30 := thirtySecondsUs
		needsLocalAdjust = wrapPeriod && at30 < thirtySecondsUs
		require.False(t, needsLocalAdjust, "at 30s should NOT need local adjustment")
	})
}

// =============================================================================
// Overflow Safety Tests
// =============================================================================
// Tests that calculations don't overflow even with extreme values.
// =============================================================================

func TestTSBPD_OverflowSafety(t *testing.T) {
	testCases := []struct {
		name          string
		tsbpdTimeBase uint64
		tsbpdOffset   uint64
		timestamp     uint32
		tsbpdDelay    uint64
		tsbpdDrift    uint64
		wrapPeriod    bool
	}{
		{
			name:          "max practical values",
			tsbpdTimeBase: 1 << 50,                         // ~35 years in microseconds
			tsbpdOffset:   10 * (uint64(maxTimestamp) + 1), // 10 wrap cycles
			timestamp:     maxTimestamp,
			tsbpdDelay:    60_000_000,    // 60 seconds
			tsbpdDrift:    1_000_000_000, // 1000 seconds drift
			wrapPeriod:    false,
		},
		{
			name:          "wrap period with local adjustment",
			tsbpdTimeBase: 1 << 50,
			tsbpdOffset:   100 * (uint64(maxTimestamp) + 1), // 100 wrap cycles
			timestamp:     0,                                // Wrapped to 0
			tsbpdDelay:    60_000_000,
			tsbpdDrift:    1_000_000_000,
			wrapPeriod:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			localOffset := tc.tsbpdOffset
			if tc.wrapPeriod && tc.timestamp < thirtySecondsUs {
				localOffset += uint64(maxTimestamp) + 1
			}

			// This should not panic or overflow
			result := tc.tsbpdTimeBase + localOffset + uint64(tc.timestamp) + tc.tsbpdDelay + tc.tsbpdDrift

			// Verify result is reasonable (greater than base + offset)
			require.Greater(t, result, tc.tsbpdTimeBase,
				"result should be greater than base time")
			require.Greater(t, result, localOffset,
				"result should be greater than local offset")
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkTSBPD_WrapPeriodCheck(b *testing.B) {
	timestamps := []uint32{
		1_000_000,                          // Normal
		maxTimestamp / 2,                   // Middle
		maxTimestamp - thirtySecondsUs - 1, // Just below threshold
		maxTimestamp - thirtySecondsUs + 1, // Just above threshold
		maxTimestamp,                       // Max
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, ts := range timestamps {
			_ = ts > maxTimestamp-thirtySecondsUs
		}
	}
}

func BenchmarkTSBPD_TimeCalculation(b *testing.B) {
	tsbpdTimeBase := testTsbpdTimeBase
	tsbpdOffset := uint64(maxTimestamp) + 1
	tsbpdDelay := testTsbpdDelay
	tsbpdDrift := uint64(100_000)

	timestamps := []uint32{
		1_000_000,        // Needs local adjustment in wrap
		35_000_000,       // No local adjustment
		maxTimestamp - 1, // Near max
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wrapPeriod := true
		for _, ts := range timestamps {
			localOffset := tsbpdOffset
			if wrapPeriod && ts < thirtySecondsUs {
				localOffset += uint64(maxTimestamp) + 1
			}
			_ = tsbpdTimeBase + localOffset + uint64(ts) + tsbpdDelay + tsbpdDrift
		}
	}
}

// =============================================================================
// Constants Verification Test
// =============================================================================
// Verifies our test constants match the actual packet constants.
// =============================================================================

func TestTSBPD_ConstantsMatch(t *testing.T) {
	require.Equal(t, packet.MAX_TIMESTAMP, maxTimestamp,
		"Test constant maxTimestamp should match packet.MAX_TIMESTAMP")

	// Verify the 30-second and 60-second constants
	require.Equal(t, uint32(30_000_000), thirtySecondsUs,
		"30 seconds in microseconds")
	require.Equal(t, uint32(60_000_000), sixtySecondsUs,
		"60 seconds in microseconds")

	// Verify wrap entry point calculation
	expectedWrapEntry := maxTimestamp - thirtySecondsUs + 1
	require.Equal(t, expectedWrapEntry, wrapEntryPoint,
		"Wrap entry point calculation")
}
