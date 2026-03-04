package send

import (
	"math"
	"testing"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Drop Threshold Calculation Tests
//
// These tests verify the drop threshold calculation handles uint64 underflow
// correctly. The bug was discovered in the 20M EventLoop intermittent failure:
// when nowUs < dropThreshold, the subtraction underflows to ~18.4e18, causing
// ALL packets to be dropped as "too old".
//
// Reference: lockless_sender_implementation_tracking.md "CRITICAL BUG FIX"
// ═══════════════════════════════════════════════════════════════════════════════

func TestCalculateDropThreshold_NormalCase(t *testing.T) {
	// Normal case: nowUs > dropThreshold
	// At 5 seconds into connection, with 1 second drop threshold
	nowUs := uint64(5_000_000)         // 5 seconds in µs
	dropThreshold := uint64(1_000_000) // 1 second in µs

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	if !shouldDrop {
		t.Error("Expected shouldDrop=true when nowUs > dropThreshold")
	}

	expectedThreshold := uint64(4_000_000) // 5s - 1s = 4s
	if threshold != expectedThreshold {
		t.Errorf("Expected threshold=%d, got %d", expectedThreshold, threshold)
	}

	// A packet at 3.5 seconds should be dropped (3.5s < 4s threshold)
	pktTsbpdTime := uint64(3_500_000) // 3.5 seconds
	if !shouldDropPacket(pktTsbpdTime, threshold) {
		t.Error("Packet at 3.5s should be dropped when threshold is 4s")
	}

	// A packet at 4.5 seconds should NOT be dropped (4.5s > 4s threshold)
	pktTsbpdTime = uint64(4_500_000) // 4.5 seconds
	if shouldDropPacket(pktTsbpdTime, threshold) {
		t.Error("Packet at 4.5s should NOT be dropped when threshold is 4s")
	}
}

func TestCalculateDropThreshold_Underflow_StartupScenario(t *testing.T) {
	// ═══════════════════════════════════════════════════════════════════════════
	// CRITICAL TEST: This catches the uint64 underflow bug!
	//
	// At connection startup, nowUs might be small (e.g., 100ms) while
	// dropThreshold is 1 second. Without protection, this causes:
	//   threshold = 100,000 - 1,000,000 = underflow to ~18.4e18
	//
	// This was the root cause of 20% intermittent failure in 20M tests.
	// ═══════════════════════════════════════════════════════════════════════════

	nowUs := uint64(100_000)           // 100ms into connection
	dropThreshold := uint64(1_000_000) // 1 second

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	// At startup (nowUs < dropThreshold), we should NOT drop any packets
	if shouldDrop {
		t.Error("UNDERFLOW BUG: shouldDrop should be false when nowUs < dropThreshold")
	}

	// If the bug exists, threshold will be a huge number due to underflow
	if threshold > math.MaxUint64/2 {
		t.Errorf("UNDERFLOW BUG: threshold=%d is clearly an underflowed value", threshold)
	}

	// Even if shouldDrop is incorrectly true, verify the threshold isn't absurd
	// A valid threshold should be <= nowUs (since it's nowUs - something)
	if shouldDrop && threshold > nowUs {
		t.Errorf("UNDERFLOW BUG: threshold=%d > nowUs=%d indicates underflow", threshold, nowUs)
	}
}

func TestCalculateDropThreshold_ExactBoundary(t *testing.T) {
	// Edge case: nowUs exactly equals dropThreshold
	// threshold = 1,000,000 - 1,000,000 = 0
	nowUs := uint64(1_000_000)
	dropThreshold := uint64(1_000_000)

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	// At exactly 1 second, we CAN start dropping (threshold = 0)
	// Only packets at PktTsbpdTime=0 would be dropped, which is unlikely but valid
	if !shouldDrop {
		t.Error("At exact boundary (nowUs == dropThreshold), shouldDrop should be true")
	}

	if threshold != 0 {
		t.Errorf("Expected threshold=0 at boundary, got %d", threshold)
	}
}

func TestCalculateDropThreshold_JustAfterBoundary(t *testing.T) {
	// Edge case: nowUs is just 1µs after dropThreshold
	nowUs := uint64(1_000_001)         // 1 second + 1µs
	dropThreshold := uint64(1_000_000) // 1 second

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	if !shouldDrop {
		t.Error("Just after boundary, shouldDrop should be true")
	}

	if threshold != 1 {
		t.Errorf("Expected threshold=1, got %d", threshold)
	}
}

func TestCalculateDropThreshold_JustBeforeBoundary(t *testing.T) {
	// Edge case: nowUs is just 1µs before dropThreshold
	// This should NOT cause underflow
	nowUs := uint64(999_999)           // 1 second - 1µs
	dropThreshold := uint64(1_000_000) // 1 second

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	// Before boundary, we should NOT drop any packets
	if shouldDrop {
		t.Error("UNDERFLOW BUG: Just before boundary, shouldDrop should be false")
	}

	// If buggy, threshold would be math.MaxUint64 (underflow)
	if threshold == math.MaxUint64 {
		t.Error("UNDERFLOW BUG: threshold is MaxUint64 due to underflow")
	}
}

func TestCalculateDropThreshold_ZeroNowUs(t *testing.T) {
	// Edge case: nowUs is 0 (immediate after connection start)
	nowUs := uint64(0)
	dropThreshold := uint64(1_000_000)

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	if shouldDrop {
		t.Error("UNDERFLOW BUG: At nowUs=0, shouldDrop should be false")
	}

	// Check for underflow
	if threshold > 0 && nowUs == 0 && dropThreshold > 0 {
		t.Errorf("UNDERFLOW BUG: threshold=%d when nowUs=0 indicates underflow", threshold)
	}
}

func TestCalculateDropThreshold_LargeValues(t *testing.T) {
	// Test with large values (simulating long-running connection)
	// At 24 hours into connection
	nowUs := uint64(24 * 60 * 60 * 1_000_000) // 24 hours in µs
	dropThreshold := uint64(1_000_000)        // 1 second

	threshold, shouldDrop := calculateDropThreshold(nowUs, dropThreshold)

	if !shouldDrop {
		t.Error("After 24 hours, shouldDrop should definitely be true")
	}

	expected := nowUs - dropThreshold
	if threshold != expected {
		t.Errorf("Expected threshold=%d, got %d", expected, threshold)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Table-driven test covering multiple scenarios
// ═══════════════════════════════════════════════════════════════════════════════

func TestCalculateDropThreshold_Table(t *testing.T) {
	tests := []struct {
		name           string
		nowUs          uint64
		dropThreshold  uint64
		wantShouldDrop bool
		wantThreshold  uint64 // Only checked if wantShouldDrop is true
	}{
		{
			name:           "Startup: 100ms into 1s threshold",
			nowUs:          100_000,
			dropThreshold:  1_000_000,
			wantShouldDrop: false,
		},
		{
			name:           "Startup: 500ms into 1s threshold",
			nowUs:          500_000,
			dropThreshold:  1_000_000,
			wantShouldDrop: false,
		},
		{
			name:           "Exact boundary: 1s into 1s threshold",
			nowUs:          1_000_000,
			dropThreshold:  1_000_000,
			wantShouldDrop: true,
			wantThreshold:  0,
		},
		{
			name:           "Normal: 5s into 1s threshold",
			nowUs:          5_000_000,
			dropThreshold:  1_000_000,
			wantShouldDrop: true,
			wantThreshold:  4_000_000,
		},
		{
			name:           "Normal: 60s into 1s threshold",
			nowUs:          60_000_000,
			dropThreshold:  1_000_000,
			wantShouldDrop: true,
			wantThreshold:  59_000_000,
		},
		{
			name:           "Zero dropThreshold (disabled)",
			nowUs:          100_000,
			dropThreshold:  0,
			wantShouldDrop: true,
			wantThreshold:  100_000,
		},
		{
			name:           "Zero nowUs with non-zero threshold",
			nowUs:          0,
			dropThreshold:  1_000_000,
			wantShouldDrop: false,
		},
		{
			name:           "Large threshold (3s)",
			nowUs:          2_000_000, // 2 seconds
			dropThreshold:  3_000_000, // 3 seconds
			wantShouldDrop: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			threshold, shouldDrop := calculateDropThreshold(tc.nowUs, tc.dropThreshold)

			if shouldDrop != tc.wantShouldDrop {
				t.Errorf("shouldDrop: got %v, want %v", shouldDrop, tc.wantShouldDrop)
			}

			if tc.wantShouldDrop && threshold != tc.wantThreshold {
				t.Errorf("threshold: got %d, want %d", threshold, tc.wantThreshold)
			}

			// Always check for underflow indicators
			if shouldDrop && threshold > tc.nowUs && tc.dropThreshold > 0 {
				t.Errorf("UNDERFLOW: threshold=%d > nowUs=%d", threshold, tc.nowUs)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Benchmark to ensure the fix doesn't add overhead
// ═══════════════════════════════════════════════════════════════════════════════

func BenchmarkCalculateDropThreshold(b *testing.B) {
	nowUs := uint64(5_000_000)
	dropThreshold := uint64(1_000_000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = calculateDropThreshold(nowUs, dropThreshold)
	}
}
