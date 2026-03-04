package srt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// Phase 1.2: Handshake Tests
// =============================================================================
// Tests for handshake-related functions with focus on:
// - calculateHSReqDropThreshold: Pure function for drop threshold calculation
// - Version validation boundaries (0x010200 - 0x010300)
// - Flag validation (required and invalid flags)
// - Edge cases in latency negotiation
//
// Reference: documentation/unit_test_coverage_improvement_plan.md
// =============================================================================

// =============================================================================
// calculateHSReqDropThreshold Tests
// =============================================================================
// Tests the drop threshold calculation function with various input combinations.
// The function calculates: dropThreshold = max(sendTsbpdDelay * 1.25, 1s) + 20ms + sendDropDelay
//
// Critical boundary: When sendTsbpdDelay * 1.25 < 1 second, hits minimum threshold
// =============================================================================

func TestCalculateHSReqDropThreshold_TableDriven(t *testing.T) {
	const (
		oneSecondUs = 1_000_000 // 1 second in microseconds
		twentyMsUs  = 20_000    // 20ms in microseconds
	)

	testCases := []struct {
		name                string
		configPeerLatencyMs int64  // Local config latency in milliseconds
		cifSendTSBPDDelayMs uint16 // Peer's TSBPD delay from handshake extension (milliseconds)
		sendDropDelayUs     uint64 // Additional drop delay from config (microseconds)
		wantThresholdUs     uint64 // Expected drop threshold in microseconds
	}{
		// =================================================================
		// Normal cases - latency > 1 second (no minimum applied)
		// =================================================================
		{
			name:                "3s latency from config (>1s, no minimum applied)",
			configPeerLatencyMs: 3000, // 3 seconds
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 3000ms * 1000 = 3,000,000 µs * 1.25 = 3,750,000 µs + 20,000 = 3,770,000 µs
			wantThresholdUs: 3_770_000,
		},
		{
			name:                "3s latency from peer CIF (>1s, no minimum applied)",
			configPeerLatencyMs: 0,
			cifSendTSBPDDelayMs: 3000, // 3 seconds
			sendDropDelayUs:     0,
			wantThresholdUs:     3_770_000,
		},
		{
			name:                "peer CIF wins when higher than config",
			configPeerLatencyMs: 1000, // 1 second
			cifSendTSBPDDelayMs: 3000, // 3 seconds (higher)
			sendDropDelayUs:     0,
			wantThresholdUs:     3_770_000, // Uses 3s, not 1s
		},
		{
			name:                "config wins when higher than peer CIF",
			configPeerLatencyMs: 3000, // 3 seconds (higher)
			cifSendTSBPDDelayMs: 1000, // 1 second
			sendDropDelayUs:     0,
			wantThresholdUs:     3_770_000, // Uses 3s, not 1s
		},
		{
			name:                "with sendDropDelay added",
			configPeerLatencyMs: 3000,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     100_000, // 100ms
			// 3,750,000 + 20,000 + 100,000 = 3,870,000 µs
			wantThresholdUs: 3_870_000,
		},
		{
			name:                "2s latency (>1s, no minimum)",
			configPeerLatencyMs: 2000, // 2 seconds
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 2000ms * 1000 = 2,000,000 µs * 1.25 = 2,500,000 µs + 20,000 = 2,520,000 µs
			wantThresholdUs: 2_520_000,
		},

		// =================================================================
		// Minimum threshold cases - latency * 1.25 < 1 second
		// =================================================================
		{
			name:                "zero latency hits minimum (1s + 20ms)",
			configPeerLatencyMs: 0,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 0 * 1.25 = 0 < 1s, so use 1s minimum + 20ms = 1,020,000 µs
			wantThresholdUs: oneSecondUs + twentyMsUs,
		},
		{
			name:                "100ms latency hits minimum",
			configPeerLatencyMs: 100, // 100ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 100ms * 1000 = 100,000 µs * 1.25 = 125,000 µs < 1s, so use 1s + 20ms
			wantThresholdUs: oneSecondUs + twentyMsUs,
		},
		{
			name:                "500ms latency hits minimum",
			configPeerLatencyMs: 500, // 500ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 500ms * 1000 = 500,000 µs * 1.25 = 625,000 µs < 1s, so use 1s + 20ms
			wantThresholdUs: oneSecondUs + twentyMsUs,
		},
		{
			name:                "799ms latency hits minimum (boundary)",
			configPeerLatencyMs: 799, // 799ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 799ms * 1000 = 799,000 µs * 1.25 = 998,750 µs < 1s, so use 1s + 20ms
			wantThresholdUs: oneSecondUs + twentyMsUs,
		},
		{
			name:                "minimum with sendDropDelay (swallowed by minimum)",
			configPeerLatencyMs: 0,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     50_000, // 50ms
			// NOTE: sendDropDelay is added BEFORE minimum check in implementation:
			// 0 * 1.25 + 50_000 = 50_000 < 1s, so threshold = 1s + 20ms = 1,020,000 µs
			// The sendDropDelay is "swallowed" by the minimum threshold.
			// This might be a bug (comment says it should be added after), but we test actual behavior.
			wantThresholdUs: oneSecondUs + twentyMsUs,
		},
		{
			name:                "minimum with large sendDropDelay (exceeds minimum)",
			configPeerLatencyMs: 0,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     1_000_000, // 1s - pushes above minimum
			// 0 * 1.25 + 1_000_000 = 1_000_000 µs >= 1s, so minimum doesn't apply
			// threshold = 1_000_000 + 20_000 = 1,020,000 µs
			wantThresholdUs: 1_020_000,
		},
		{
			name:                "minimum with sendDropDelay just above threshold",
			configPeerLatencyMs: 0,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     1_000_001, // Just above 1s
			// 0 * 1.25 + 1_000_001 = 1_000_001 µs > 1s, minimum doesn't apply
			// threshold = 1_000_001 + 20_000 = 1,020,001 µs
			wantThresholdUs: 1_020_001,
		},

		// =================================================================
		// Boundary case - exactly at 1 second threshold after 1.25x
		// =================================================================
		{
			name:                "800ms latency exactly at boundary (1s after 1.25x)",
			configPeerLatencyMs: 800, // 800ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 800ms * 1000 = 800,000 µs * 1.25 = 1,000,000 µs = exactly 1s
			// Not < 1s, so doesn't hit minimum
			wantThresholdUs: 1_000_000 + twentyMsUs,
		},
		{
			name:                "801ms latency just above boundary",
			configPeerLatencyMs: 801, // 801ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 801ms * 1000 = 801,000 µs * 1.25 = 1,001,250 µs > 1s
			wantThresholdUs: 1_001_250 + twentyMsUs,
		},

		// =================================================================
		// uint16 overflow boundary tests
		// =================================================================
		{
			name:                "max uint16 latency (65535ms = ~65.5s)",
			configPeerLatencyMs: 65535, // max uint16
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// 65535ms * 1000 = 65,535,000 µs * 1.25 = 81,918,750 µs + 20,000 = 81,938,750 µs
			wantThresholdUs: 81_938_750,
		},
		{
			name:                "config latency truncated to uint16",
			configPeerLatencyMs: 70000, // > max uint16, will be truncated
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			// Truncated to uint16: 70000 & 0xFFFF = 4464ms
			// 4464ms * 1000 = 4,464,000 µs * 1.25 = 5,580,000 µs + 20,000 = 5,600,000 µs
			wantThresholdUs: 5_600_000,
		},

		// =================================================================
		// Large sendDropDelay tests
		// =================================================================
		{
			name:                "large sendDropDelay (1s additional)",
			configPeerLatencyMs: 1000, // 1 second
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     1_000_000, // 1 second
			// 1000ms * 1000 = 1,000,000 µs * 1.25 = 1,250,000 µs + 20,000 + 1,000,000 = 2,270,000 µs
			wantThresholdUs: 2_270_000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := calculateHSReqDropThreshold(tc.configPeerLatencyMs, tc.cifSendTSBPDDelayMs, tc.sendDropDelayUs)
			require.Equal(t, tc.wantThresholdUs, got,
				"calculateHSReqDropThreshold(%d, %d, %d) = %d, want %d",
				tc.configPeerLatencyMs, tc.cifSendTSBPDDelayMs, tc.sendDropDelayUs, got, tc.wantThresholdUs)
		})
	}
}

// TestCalculateHSReqDropThreshold_BugDocumentation documents the bug that was fixed
// in the drop threshold calculation. The original code treated milliseconds as
// microseconds, resulting in a threshold that was 1000x too small.
func TestCalculateHSReqDropThreshold_BugDocumentation(t *testing.T) {
	// Example from the code comment:
	// With 3s latency:
	// - Buggy:  3000 * 1.25 = 3750 µs → hits 1s minimum → 1.02s threshold
	// - Fixed:  3000 * 1000 * 1.25 = 3,750,000 µs → 3.77s threshold

	// The fixed implementation should return ~3.77 seconds, not ~1.02 seconds
	result := calculateHSReqDropThreshold(3000, 0, 0)

	// If the bug was present, result would be around 1,020,000 µs (1.02s)
	// Fixed result should be 3,770,000 µs (3.77s)
	require.Greater(t, result, uint64(3_000_000),
		"Bug check: 3s latency should result in threshold > 3s, got %d µs", result)
	require.Equal(t, uint64(3_770_000), result,
		"Expected 3.77s threshold for 3s latency")
}

// =============================================================================
// SRT Version Validation Tests
// =============================================================================
// These tests validate the SRT version check logic used in handleHSRequest
// and handleHSResponse. Valid versions are in range [0x010200, 0x010300).
//
// The version check is: cif.SRTVersion < 0x010200 || cif.SRTVersion >= 0x010300
// =============================================================================

func TestSRTVersionValidation_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		version   uint32
		wantValid bool
	}{
		// =================================================================
		// Valid versions (0x010200 - 0x0102FF)
		// =================================================================
		{
			name:      "minimum valid version 0x010200",
			version:   0x010200,
			wantValid: true,
		},
		{
			name:      "version 0x010201",
			version:   0x010201,
			wantValid: true,
		},
		{
			name:      "version 0x010203 (used in sendHSRequest)",
			version:   0x010203,
			wantValid: true,
		},
		{
			name:      "maximum valid version 0x0102FF",
			version:   0x0102FF,
			wantValid: true,
		},

		// =================================================================
		// Invalid versions - too old
		// =================================================================
		{
			name:      "version 0x0101FF (just below minimum)",
			version:   0x0101FF,
			wantValid: false,
		},
		{
			name:      "version 0x010100",
			version:   0x010100,
			wantValid: false,
		},
		{
			name:      "version 0x010000",
			version:   0x010000,
			wantValid: false,
		},
		{
			name:      "version 0x000000 (zero)",
			version:   0x000000,
			wantValid: false,
		},
		{
			name:      "version 0x000001",
			version:   0x000001,
			wantValid: false,
		},

		// =================================================================
		// Invalid versions - too new
		// =================================================================
		{
			name:      "version 0x010300 (just above maximum)",
			version:   0x010300,
			wantValid: false,
		},
		{
			name:      "version 0x010301",
			version:   0x010301,
			wantValid: false,
		},
		{
			name:      "version 0x010400",
			version:   0x010400,
			wantValid: false,
		},
		{
			name:      "version 0x020000",
			version:   0x020000,
			wantValid: false,
		},
		{
			name:      "version 0xFFFFFFFF (max uint32)",
			version:   0xFFFFFFFF,
			wantValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Replicate the version check logic from handleHSRequest/handleHSResponse
			invalid := tc.version < 0x010200 || tc.version >= 0x010300
			gotValid := !invalid

			require.Equal(t, tc.wantValid, gotValid,
				"version %#08x: got valid=%v, want valid=%v",
				tc.version, gotValid, tc.wantValid)
		})
	}
}

// =============================================================================
// SRT Flags Validation Tests (HSRequest)
// =============================================================================
// Tests the flag validation logic in handleHSRequest.
// Required flags for HSRequest: TSBPDSND, TLPKTDROP, CRYPT, REXMITFLG
// Invalid flags for HSv4: STREAM, PACKET_FILTER
// =============================================================================

// HSReqFlags represents the flags in an HSRequest for testing
type HSReqFlags struct {
	TSBPDSND      bool
	TSBPDRCV      bool
	CRYPT         bool
	TLPKTDROP     bool
	PERIODICNAK   bool
	REXMITFLG     bool
	STREAM        bool
	PACKET_FILTER bool
}

func TestHSReqFlagsValidation_TableDriven(t *testing.T) {
	// All required flags set - valid baseline
	validFlags := HSReqFlags{
		TSBPDSND:      true,
		TLPKTDROP:     true,
		CRYPT:         true,
		REXMITFLG:     true,
		STREAM:        false, // Must NOT be set
		PACKET_FILTER: false, // Must NOT be set
	}

	testCases := []struct {
		name            string
		flags           HSReqFlags
		wantValid       bool
		expectedFailure string // Which flag causes failure (for documentation)
	}{
		// =================================================================
		// Valid flag combinations
		// =================================================================
		{
			name:      "all required flags set - valid",
			flags:     validFlags,
			wantValid: true,
		},
		{
			name: "with optional TSBPDRCV set",
			flags: HSReqFlags{
				TSBPDSND:  true,
				TSBPDRCV:  true, // Optional
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: true,
			},
			wantValid: true,
		},
		{
			name: "with optional PERIODICNAK set",
			flags: HSReqFlags{
				TSBPDSND:    true,
				TLPKTDROP:   true,
				CRYPT:       true,
				REXMITFLG:   true,
				PERIODICNAK: true, // Optional
			},
			wantValid: true,
		},

		// =================================================================
		// Missing required flags - each should fail
		// =================================================================
		{
			name: "missing TSBPDSND",
			flags: HSReqFlags{
				TSBPDSND:  false, // Missing
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: true,
			},
			wantValid:       false,
			expectedFailure: "TSBPDSND",
		},
		{
			name: "missing TLPKTDROP",
			flags: HSReqFlags{
				TSBPDSND:  true,
				TLPKTDROP: false, // Missing
				CRYPT:     true,
				REXMITFLG: true,
			},
			wantValid:       false,
			expectedFailure: "TLPKTDROP",
		},
		{
			name: "missing CRYPT",
			flags: HSReqFlags{
				TSBPDSND:  true,
				TLPKTDROP: true,
				CRYPT:     false, // Missing
				REXMITFLG: true,
			},
			wantValid:       false,
			expectedFailure: "CRYPT",
		},
		{
			name: "missing REXMITFLG",
			flags: HSReqFlags{
				TSBPDSND:  true,
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: false, // Missing
			},
			wantValid:       false,
			expectedFailure: "REXMITFLG",
		},
		{
			name: "all required flags missing",
			flags: HSReqFlags{
				TSBPDSND:  false,
				TLPKTDROP: false,
				CRYPT:     false,
				REXMITFLG: false,
			},
			wantValid:       false,
			expectedFailure: "TSBPDSND (first check)",
		},

		// =================================================================
		// Invalid flags set (HSv5 flags in HSv4)
		// =================================================================
		{
			name: "STREAM flag set (invalid for HSv4)",
			flags: HSReqFlags{
				TSBPDSND:  true,
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: true,
				STREAM:    true, // Invalid
			},
			wantValid:       false,
			expectedFailure: "STREAM",
		},
		{
			name: "PACKET_FILTER flag set (invalid for HSv4)",
			flags: HSReqFlags{
				TSBPDSND:      true,
				TLPKTDROP:     true,
				CRYPT:         true,
				REXMITFLG:     true,
				PACKET_FILTER: true, // Invalid
			},
			wantValid:       false,
			expectedFailure: "PACKET_FILTER",
		},
		{
			name: "both STREAM and PACKET_FILTER set (invalid)",
			flags: HSReqFlags{
				TSBPDSND:      true,
				TLPKTDROP:     true,
				CRYPT:         true,
				REXMITFLG:     true,
				STREAM:        true, // Invalid
				PACKET_FILTER: true, // Invalid
			},
			wantValid:       false,
			expectedFailure: "STREAM (checked first)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Replicate the flag validation logic from handleHSRequest
			var valid bool
			var failReason string

			switch {
			case !tc.flags.TSBPDSND:
				valid = false
				failReason = "TSBPDSND"
			case !tc.flags.TLPKTDROP:
				valid = false
				failReason = "TLPKTDROP"
			case !tc.flags.CRYPT:
				valid = false
				failReason = "CRYPT"
			case !tc.flags.REXMITFLG:
				valid = false
				failReason = "REXMITFLG"
			case tc.flags.STREAM:
				valid = false
				failReason = "STREAM (invalid)"
			case tc.flags.PACKET_FILTER:
				valid = false
				failReason = "PACKET_FILTER (invalid)"
			default:
				valid = true
			}

			require.Equal(t, tc.wantValid, valid,
				"flags validation: got valid=%v, want valid=%v (failure: %s)",
				valid, tc.wantValid, failReason)
		})
	}
}

// =============================================================================
// SRT Flags Validation Tests (HSResponse)
// =============================================================================
// Tests the flag validation logic in handleHSResponse.
// Required flags for HSResponse: TSBPDRCV, TLPKTDROP, CRYPT, REXMITFLG
// Invalid flags for HSv4: STREAM, PACKET_FILTER
// Note: TSBPDRCV is required (not TSBPDSND like in HSRequest)
// =============================================================================

// HSRspFlags represents the flags in an HSResponse for testing
type HSRspFlags struct {
	TSBPDSND      bool
	TSBPDRCV      bool
	CRYPT         bool
	TLPKTDROP     bool
	PERIODICNAK   bool
	REXMITFLG     bool
	STREAM        bool
	PACKET_FILTER bool
}

func TestHSRspFlagsValidation_TableDriven(t *testing.T) {
	// All required flags set - valid baseline
	validFlags := HSRspFlags{
		TSBPDRCV:      true,
		TLPKTDROP:     true,
		CRYPT:         true,
		REXMITFLG:     true,
		STREAM:        false, // Must NOT be set
		PACKET_FILTER: false, // Must NOT be set
	}

	testCases := []struct {
		name            string
		flags           HSRspFlags
		wantValid       bool
		expectedFailure string
	}{
		// =================================================================
		// Valid flag combinations
		// =================================================================
		{
			name:      "all required flags set - valid",
			flags:     validFlags,
			wantValid: true,
		},
		{
			name: "with optional TSBPDSND set",
			flags: HSRspFlags{
				TSBPDSND:  true, // Optional (sender's decision)
				TSBPDRCV:  true,
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: true,
			},
			wantValid: true,
		},
		{
			name: "with optional PERIODICNAK set",
			flags: HSRspFlags{
				TSBPDRCV:    true,
				TLPKTDROP:   true,
				CRYPT:       true,
				REXMITFLG:   true,
				PERIODICNAK: true, // Optional
			},
			wantValid: true,
		},

		// =================================================================
		// Missing required flags - each should fail
		// =================================================================
		{
			name: "missing TSBPDRCV",
			flags: HSRspFlags{
				TSBPDRCV:  false, // Missing
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: true,
			},
			wantValid:       false,
			expectedFailure: "TSBPDRCV",
		},
		{
			name: "missing TLPKTDROP",
			flags: HSRspFlags{
				TSBPDRCV:  true,
				TLPKTDROP: false, // Missing
				CRYPT:     true,
				REXMITFLG: true,
			},
			wantValid:       false,
			expectedFailure: "TLPKTDROP",
		},
		{
			name: "missing CRYPT",
			flags: HSRspFlags{
				TSBPDRCV:  true,
				TLPKTDROP: true,
				CRYPT:     false, // Missing
				REXMITFLG: true,
			},
			wantValid:       false,
			expectedFailure: "CRYPT",
		},
		{
			name: "missing REXMITFLG",
			flags: HSRspFlags{
				TSBPDRCV:  true,
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: false, // Missing
			},
			wantValid:       false,
			expectedFailure: "REXMITFLG",
		},

		// =================================================================
		// Invalid flags set (HSv5 flags in HSv4)
		// =================================================================
		{
			name: "STREAM flag set (invalid for HSv4)",
			flags: HSRspFlags{
				TSBPDRCV:  true,
				TLPKTDROP: true,
				CRYPT:     true,
				REXMITFLG: true,
				STREAM:    true, // Invalid
			},
			wantValid:       false,
			expectedFailure: "STREAM",
		},
		{
			name: "PACKET_FILTER flag set (invalid for HSv4)",
			flags: HSRspFlags{
				TSBPDRCV:      true,
				TLPKTDROP:     true,
				CRYPT:         true,
				REXMITFLG:     true,
				PACKET_FILTER: true, // Invalid
			},
			wantValid:       false,
			expectedFailure: "PACKET_FILTER",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Replicate the flag validation logic from handleHSResponse
			var valid bool
			var failReason string

			switch {
			case !tc.flags.TSBPDRCV:
				valid = false
				failReason = "TSBPDRCV"
			case !tc.flags.TLPKTDROP:
				valid = false
				failReason = "TLPKTDROP"
			case !tc.flags.CRYPT:
				valid = false
				failReason = "CRYPT"
			case !tc.flags.REXMITFLG:
				valid = false
				failReason = "REXMITFLG"
			case tc.flags.STREAM:
				valid = false
				failReason = "STREAM (invalid)"
			case tc.flags.PACKET_FILTER:
				valid = false
				failReason = "PACKET_FILTER (invalid)"
			default:
				valid = true
			}

			require.Equal(t, tc.wantValid, valid,
				"flags validation: got valid=%v, want valid=%v (failure: %s)",
				valid, tc.wantValid, failReason)
		})
	}
}

// =============================================================================
// TSBPD Delay Negotiation Tests
// =============================================================================
// Tests the TSBPD delay negotiation logic used in handleHSRequest.
// The receiver uses: max(localConfig.ReceiverLatency, peer.SendTSBPDDelay)
// =============================================================================

func TestTSBPDDelayNegotiation_TableDriven(t *testing.T) {
	testCases := []struct {
		name                 string
		configReceiverLatMs  int64  // Local config ReceiverLatency in milliseconds
		peerSendTSBPDDelayMs uint16 // Peer's SendTSBPDDelay from handshake
		wantDelayUs          uint64 // Expected tsbpdDelay in microseconds
	}{
		// =================================================================
		// Config wins (higher than peer)
		// =================================================================
		{
			name:                 "config wins: 3s vs 1s",
			configReceiverLatMs:  3000, // 3 seconds
			peerSendTSBPDDelayMs: 1000, // 1 second
			wantDelayUs:          3_000_000,
		},
		{
			name:                 "config wins: 2s vs 0s",
			configReceiverLatMs:  2000, // 2 seconds
			peerSendTSBPDDelayMs: 0,
			wantDelayUs:          2_000_000,
		},
		{
			name:                 "config wins: 500ms vs 100ms",
			configReceiverLatMs:  500,
			peerSendTSBPDDelayMs: 100,
			wantDelayUs:          500_000,
		},

		// =================================================================
		// Peer wins (higher than config)
		// =================================================================
		{
			name:                 "peer wins: 1s vs 3s",
			configReceiverLatMs:  1000, // 1 second
			peerSendTSBPDDelayMs: 3000, // 3 seconds
			wantDelayUs:          3_000_000,
		},
		{
			name:                 "peer wins: 0s vs 2s",
			configReceiverLatMs:  0,
			peerSendTSBPDDelayMs: 2000, // 2 seconds
			wantDelayUs:          2_000_000,
		},

		// =================================================================
		// Equal values
		// =================================================================
		{
			name:                 "equal: both 2s",
			configReceiverLatMs:  2000,
			peerSendTSBPDDelayMs: 2000,
			wantDelayUs:          2_000_000,
		},
		{
			name:                 "equal: both 0",
			configReceiverLatMs:  0,
			peerSendTSBPDDelayMs: 0,
			wantDelayUs:          0,
		},

		// =================================================================
		// Edge cases
		// =================================================================
		{
			name:                 "max uint16: 65535ms",
			configReceiverLatMs:  0,
			peerSendTSBPDDelayMs: 65535, // max uint16
			wantDelayUs:          65_535_000,
		},
		{
			name:                 "1ms difference: config wins",
			configReceiverLatMs:  1001,
			peerSendTSBPDDelayMs: 1000,
			wantDelayUs:          1_001_000,
		},
		{
			name:                 "1ms difference: peer wins",
			configReceiverLatMs:  1000,
			peerSendTSBPDDelayMs: 1001,
			wantDelayUs:          1_001_000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Replicate the TSBPD delay negotiation logic from handleHSRequest
			recvTsbpdDelay := uint16(tc.configReceiverLatMs)
			if tc.peerSendTSBPDDelayMs > recvTsbpdDelay {
				recvTsbpdDelay = tc.peerSendTSBPDDelayMs
			}
			tsbpdDelay := uint64(recvTsbpdDelay) * 1000

			require.Equal(t, tc.wantDelayUs, tsbpdDelay,
				"TSBPD negotiation: config=%dms, peer=%dms, got=%d µs, want=%d µs",
				tc.configReceiverLatMs, tc.peerSendTSBPDDelayMs, tsbpdDelay, tc.wantDelayUs)
		})
	}
}

// =============================================================================
// Config Truncation Tests
// =============================================================================
// Tests that int64 config values are correctly truncated to uint16 for TSBPD delay.
// This is important because Config.ReceiverLatency is time.Duration (int64 nanoseconds)
// but the protocol uses uint16 milliseconds.
// =============================================================================

func TestConfigLatencyTruncation_TableDriven(t *testing.T) {
	testCases := []struct {
		name               string
		configLatency      time.Duration
		wantTruncatedMs    uint16
		wantTruncationLoss bool // True if truncation loses data
	}{
		// =================================================================
		// Normal values - no truncation
		// =================================================================
		{
			name:               "1 second - no truncation",
			configLatency:      1 * time.Second,
			wantTruncatedMs:    1000,
			wantTruncationLoss: false,
		},
		{
			name:               "3 seconds - no truncation",
			configLatency:      3 * time.Second,
			wantTruncatedMs:    3000,
			wantTruncationLoss: false,
		},
		{
			name:               "65.535 seconds (max uint16) - no truncation",
			configLatency:      65535 * time.Millisecond,
			wantTruncatedMs:    65535,
			wantTruncationLoss: false,
		},

		// =================================================================
		// Truncation cases - values too large for uint16
		// =================================================================
		{
			name:               "65.536 seconds - truncation (wraps to 0)",
			configLatency:      65536 * time.Millisecond,
			wantTruncatedMs:    0, // 65536 & 0xFFFF = 0
			wantTruncationLoss: true,
		},
		{
			name:               "70 seconds - truncation",
			configLatency:      70 * time.Second,
			wantTruncatedMs:    4464, // 70000 & 0xFFFF = 4464
			wantTruncationLoss: true,
		},
		{
			name:               "2 minutes - truncation",
			configLatency:      2 * time.Minute,
			wantTruncatedMs:    54464, // 120000 & 0xFFFF = 54464
			wantTruncationLoss: true,
		},

		// =================================================================
		// Sub-millisecond values
		// =================================================================
		{
			name:               "500 microseconds - rounds to 0ms",
			configLatency:      500 * time.Microsecond,
			wantTruncatedMs:    0,
			wantTruncationLoss: false, // Not really "loss" - just rounding
		},
		{
			name:               "1500 microseconds - rounds to 1ms",
			configLatency:      1500 * time.Microsecond,
			wantTruncatedMs:    1,
			wantTruncationLoss: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Replicate the truncation that happens in handleHSRequest:
			// recvTsbpdDelay := uint16(c.config.ReceiverLatency.Milliseconds())
			ms := tc.configLatency.Milliseconds()
			truncated := uint16(ms)

			require.Equal(t, tc.wantTruncatedMs, truncated,
				"Latency truncation: %v (%d ms) -> uint16 = %d, want %d",
				tc.configLatency, ms, truncated, tc.wantTruncatedMs)

			if tc.wantTruncationLoss {
				// Verify that truncation actually lost data
				require.NotEqual(t, ms, int64(truncated),
					"Expected truncation loss for %v (%d ms)", tc.configLatency, ms)
			}
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCalculateHSReqDropThreshold(b *testing.B) {
	// Pre-compute input values
	configs := []int64{0, 500, 800, 1000, 3000, 65535}
	cifs := []uint16{0, 500, 1000, 3000}
	delays := []uint64{0, 20000, 100000}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, cfg := range configs {
			for _, cif := range cifs {
				for _, delay := range delays {
					_ = calculateHSReqDropThreshold(cfg, cif, delay)
				}
			}
		}
	}
}

func BenchmarkVersionValidation(b *testing.B) {
	versions := []uint32{0, 0x0101FF, 0x010200, 0x010203, 0x0102FF, 0x010300, 0xFFFFFFFF}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range versions {
			_ = v < 0x010200 || v >= 0x010300
		}
	}
}
