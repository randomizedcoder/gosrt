package receive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCalcTooRecentThreshold verifies the formula for calculating
// the "too recent" threshold for NAK scanning.
//
// The threshold determines which packets are considered "too recent" to NAK.
// Packets that just arrived might be out-of-order, not actually lost.
// We wait nakRecentPercent of TSBPD before declaring them lost.
func TestCalcTooRecentThreshold(t *testing.T) {
	tests := []struct {
		name             string
		now              uint64
		tsbpdDelay       uint64
		nakRecentPercent float64
		expected         uint64
		description      string
	}{
		{
			name:             "Standard 10% window",
			now:              1_000_000,
			tsbpdDelay:       3_000_000,             // 3 seconds
			nakRecentPercent: 0.10,                  // 10%
			expected:         1_000_000 + 2_700_000, // now + 90% of TSBPD
			description:      "With 10% recent window, 90% of TSBPD is scannable",
		},
		{
			name:             "20% window",
			now:              1_000_000,
			tsbpdDelay:       3_000_000,
			nakRecentPercent: 0.20,
			expected:         1_000_000 + 2_400_000, // now + 80% of TSBPD
			description:      "With 20% recent window, 80% of TSBPD is scannable",
		},
		{
			name:             "0% window (scan all immediately)",
			now:              1_000_000,
			tsbpdDelay:       3_000_000,
			nakRecentPercent: 0.0,
			expected:         1_000_000, // now (no extension)
			description:      "With 0% recent window, only packets at or before now are scannable",
		},
		{
			name:             "100% window (never scan)",
			now:              1_000_000,
			tsbpdDelay:       3_000_000,
			nakRecentPercent: 1.0,
			expected:         1_000_000 + 0, // now + 0% of TSBPD
			description:      "With 100% recent window, nothing is scannable (extreme case)",
		},
		{
			name:             "Zero TSBPD delay",
			now:              1_000_000,
			tsbpdDelay:       0,
			nakRecentPercent: 0.10,
			expected:         1_000_000, // now (no extension when TSBPD is 0)
			description:      "With zero TSBPD, threshold equals now",
		},
		{
			name:             "Negative nakRecentPercent treated as zero",
			now:              1_000_000,
			tsbpdDelay:       3_000_000,
			nakRecentPercent: -0.10,
			expected:         1_000_000, // now (invalid percent treated as zero)
			description:      "Negative percent should not extend threshold",
		},
		{
			name:             "Large timestamp",
			now:              1_735_400_000_000_000, // ~2025 in microseconds
			tsbpdDelay:       3_000_000,
			nakRecentPercent: 0.10,
			expected:         1_735_400_000_000_000 + 2_700_000,
			description:      "Works with realistic large timestamps",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalcTooRecentThreshold(tt.now, tt.tsbpdDelay, tt.nakRecentPercent)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

// TestTooRecentThreshold_PacketClassification verifies that the threshold
// correctly classifies packets as "too recent" or "scannable".
func TestTooRecentThreshold_PacketClassification(t *testing.T) {
	// Setup: now=1s, tsbpdDelay=3s, nakRecentPercent=10%
	// threshold = 1s + 2.7s = 3.7s
	now := uint64(1_000_000)
	tsbpdDelay := uint64(3_000_000)
	nakRecentPercent := 0.10
	threshold := CalcTooRecentThreshold(now, tsbpdDelay, nakRecentPercent)

	// Expected threshold: now + tsbpdDelay * 0.90 = 1s + 2.7s = 3.7s
	assert.Equal(t, uint64(3_700_000), threshold)

	tests := []struct {
		name         string
		pktTsbpdTime uint64
		isTooRecent  bool
		description  string
	}{
		{
			name:         "Packet at threshold boundary",
			pktTsbpdTime: 3_700_000,
			isTooRecent:  false, // <= threshold is NOT too recent
			description:  "Packet exactly at threshold is scannable",
		},
		{
			name:         "Packet just past threshold",
			pktTsbpdTime: 3_700_001,
			isTooRecent:  true, // > threshold IS too recent
			description:  "Packet just past threshold is too recent",
		},
		{
			name:         "Packet well before threshold",
			pktTsbpdTime: 2_000_000,
			isTooRecent:  false,
			description:  "Packet well before threshold is scannable",
		},
		{
			name:         "Packet at now",
			pktTsbpdTime: 1_000_000,
			isTooRecent:  false,
			description:  "Packet at now is scannable (should be delivered now)",
		},
		{
			name:         "Packet in the past (already expired)",
			pktTsbpdTime: 500_000,
			isTooRecent:  false,
			description:  "Packet in past is scannable (might be TSBPD-expired)",
		},
		{
			name:         "Packet far in future",
			pktTsbpdTime: 10_000_000,
			isTooRecent:  true,
			description:  "Packet far in future is too recent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The condition for "too recent" is: pktTsbpdTime > threshold
			actualTooRecent := tt.pktTsbpdTime > threshold
			assert.Equal(t, tt.isTooRecent, actualTooRecent, tt.description)
		})
	}
}

// TestTooRecentThreshold_Timeline demonstrates the relationship between
// packet arrival time, TSBPD time, and the NAK scan window.
func TestTooRecentThreshold_Timeline(t *testing.T) {
	// Scenario: Stream at 20Mbps with 1400-byte packets
	// - Packet interval: ~560µs
	// - TSBPD delay: 3 seconds
	// - nakRecentPercent: 10%

	tsbpdDelay := uint64(3_000_000)
	nakRecentPercent := 0.10
	packetInterval := uint64(560)

	// Packet N arrives at arrivalTime(N) and has:
	//   PktTsbpdTime(N) = arrivalTime(N) + tsbpdDelay
	//
	// At current time "now", packets are classified as:
	//   - TSBPD-expired: now > PktTsbpdTime (can't be recovered)
	//   - Scannable: tooRecentThreshold >= PktTsbpdTime > now
	//   - Too recent: PktTsbpdTime > tooRecentThreshold (might still arrive)

	// Test at now = 5s (5_000_000 µs)
	now := uint64(5_000_000)
	threshold := CalcTooRecentThreshold(now, tsbpdDelay, nakRecentPercent)

	// Expected: threshold = 5s + 2.7s = 7.7s
	assert.Equal(t, uint64(7_700_000), threshold)

	// Packet arrival times and their TSBPD times
	// Packet arrived at t=1s: TSBPD = 4s (expired, now > 4s)
	// Packet arrived at t=2s: TSBPD = 5s (exactly at now, scannable)
	// Packet arrived at t=3s: TSBPD = 6s (in future, scannable)
	// Packet arrived at t=4s: TSBPD = 7s (in future, scannable)
	// Packet arrived at t=5s: TSBPD = 8s (in future, too recent!)

	type packetInfo struct {
		arrivalTime uint64
		tsbpdTime   uint64
		status      string
	}

	packets := []packetInfo{
		{1_000_000, 4_000_000, "TSBPD-expired"},
		{2_000_000, 5_000_000, "scannable"}, // At now
		{3_000_000, 6_000_000, "scannable"},
		{4_000_000, 7_000_000, "scannable"},
		{4_700_000, 7_700_000, "scannable"},  // At threshold
		{4_700_001, 7_700_001, "too-recent"}, // Just past threshold
		{5_000_000, 8_000_000, "too-recent"},
	}

	for _, pkt := range packets {
		arrivalTimeMs := pkt.arrivalTime / 1000
		tsbpdTimeMs := pkt.tsbpdTime / 1000

		var actualStatus string
		switch {
		case now > pkt.tsbpdTime:
			actualStatus = "TSBPD-expired"
		case pkt.tsbpdTime > threshold:
			actualStatus = "too-recent"
		default:
			actualStatus = "scannable"
		}

		t.Logf("Packet arrived at %dms, TSBPD=%dms: %s", arrivalTimeMs, tsbpdTimeMs, actualStatus)
		assert.Equal(t, pkt.status, actualStatus,
			"Packet with TSBPD=%d should be %s (threshold=%d, now=%d)",
			pkt.tsbpdTime, pkt.status, threshold, now)
	}

	// Verify the NAK scan window size
	// With 10% recent, 90% of TSBPD is scannable
	scanWindowSize := threshold - now
	expectedWindowSize := uint64(float64(tsbpdDelay) * (1.0 - nakRecentPercent))
	assert.Equal(t, expectedWindowSize, scanWindowSize,
		"Scan window should be 90%% of TSBPD delay")

	// Calculate how many packets fit in the scan window at 20Mbps
	packetsInWindow := scanWindowSize / packetInterval
	t.Logf("Scan window: %dms (%d packets at %dµs interval)",
		scanWindowSize/1000, packetsInWindow, packetInterval)
}
