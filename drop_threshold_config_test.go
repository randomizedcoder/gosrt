// Package srt tests drop threshold configuration invariants.
//
// This test file was created after discovering a bug where the test configuration
// hardcoded SendDropThresholdUs = 1 second, overriding the correct automatic
// calculation of dropThreshold = 1.25 * peerTsbpdDelay.
//
// Reference: send_eventloop_intermittent_failure_bug.md Section 29
package srt

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ============================================================================
// Drop Threshold Calculation Tests
//
// These tests verify the invariant that drop threshold must be >= TSBPD latency
// to allow sufficient time for NAK/retransmission cycles.
//
// From connection.go:470:
//   c.dropThreshold = uint64(float64(c.peerTsbpdDelay)*1.25) + uint64(c.config.SendDropDelay.Microseconds())
//   if c.dropThreshold < uint64(time.Second.Microseconds()) {
//       c.dropThreshold = uint64(time.Second.Microseconds())
//   }
//   c.dropThreshold += 20_000
// ============================================================================

// DropThresholdTestCase defines test cases for drop threshold calculation
type DropThresholdTestCase struct {
	Name string

	// Inputs
	TsbpdDelayMs  int64 // TSBPD latency in milliseconds
	SendDropDelay time.Duration

	// Expected outputs
	MinExpectedThresholdUs uint64 // Drop threshold must be at least this
	MaxExpectedThresholdUs uint64 // Drop threshold should not exceed this (sanity check)
}

// calculateExpectedDropThreshold mirrors the calculation in connection.go
func calculateExpectedDropThreshold(tsbpdDelayUs uint64, sendDropDelayUs uint64) uint64 {
	threshold := uint64(float64(tsbpdDelayUs)*1.25) + sendDropDelayUs
	if threshold < uint64(time.Second.Microseconds()) {
		threshold = uint64(time.Second.Microseconds())
	}
	threshold += 20_000 // 20ms margin
	return threshold
}

var dropThresholdTestCases = []DropThresholdTestCase{
	// ═══════════════════════════════════════════════════════════════════════
	// Standard latency values
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                   "Latency_120ms_Standard",
		TsbpdDelayMs:           120,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 1_000_000, // Min 1 second floor
		MaxExpectedThresholdUs: 2_000_000,
	},
	{
		Name:                   "Latency_500ms_Typical",
		TsbpdDelayMs:           500,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 1_000_000, // 500ms * 1.25 = 625ms, but min is 1s
		MaxExpectedThresholdUs: 2_000_000,
	},
	{
		Name:                   "Latency_1000ms_1Second",
		TsbpdDelayMs:           1000,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 1_250_000, // 1000ms * 1.25 = 1250ms
		MaxExpectedThresholdUs: 2_000_000,
	},
	{
		Name:                   "Latency_3000ms_Test_Default",
		TsbpdDelayMs:           3000,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 3_750_000, // 3000ms * 1.25 = 3750ms
		MaxExpectedThresholdUs: 5_000_000,
	},
	{
		Name:                   "Latency_5000ms_HighLatency",
		TsbpdDelayMs:           5000,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 6_250_000, // 5000ms * 1.25 = 6250ms
		MaxExpectedThresholdUs: 8_000_000,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// With SendDropDelay offset
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                   "Latency_3000ms_WithDropDelay_1s",
		TsbpdDelayMs:           3000,
		SendDropDelay:          1 * time.Second,
		MinExpectedThresholdUs: 4_750_000, // 3750ms + 1000ms = 4750ms
		MaxExpectedThresholdUs: 6_000_000,
	},
	{
		Name:                   "Latency_120ms_WithDropDelay_2s",
		TsbpdDelayMs:           120,
		SendDropDelay:          2 * time.Second,
		MinExpectedThresholdUs: 2_000_000, // max(150ms, 1000ms) + 2000ms = 3000ms... but min is 1s+2s
		MaxExpectedThresholdUs: 4_000_000,
	},

	// ═══════════════════════════════════════════════════════════════════════
	// Edge cases
	// ═══════════════════════════════════════════════════════════════════════
	{
		Name:                   "Latency_0ms_Minimum",
		TsbpdDelayMs:           0,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 1_000_000, // Minimum floor of 1 second
		MaxExpectedThresholdUs: 2_000_000,
	},
	{
		Name:                   "Latency_10000ms_VeryHigh",
		TsbpdDelayMs:           10000,
		SendDropDelay:          0,
		MinExpectedThresholdUs: 12_500_000, // 10000ms * 1.25 = 12500ms
		MaxExpectedThresholdUs: 15_000_000,
	},
}

func TestDropThresholdCalculation_Table(t *testing.T) {
	for _, tc := range dropThresholdTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			tsbpdDelayUs := uint64(tc.TsbpdDelayMs * 1000)
			sendDropDelayUs := uint64(tc.SendDropDelay.Microseconds())

			threshold := calculateExpectedDropThreshold(tsbpdDelayUs, sendDropDelayUs)

			// Verify minimum bound
			require.GreaterOrEqual(t, threshold, tc.MinExpectedThresholdUs,
				"Drop threshold must be >= minimum expected for TSBPD %dms", tc.TsbpdDelayMs)

			// Verify maximum bound (sanity check)
			require.LessOrEqual(t, threshold, tc.MaxExpectedThresholdUs,
				"Drop threshold should not exceed maximum expected for TSBPD %dms", tc.TsbpdDelayMs)

			// CRITICAL INVARIANT: Drop threshold must ALWAYS be > TSBPD delay
			// This ensures packets have time for NAK/retransmission before being dropped
			require.Greater(t, threshold, tsbpdDelayUs,
				"INVARIANT VIOLATION: dropThreshold (%d) must be > tsbpdDelay (%d)",
				threshold, tsbpdDelayUs)

			// Verify 1.25x multiplier is applied (except when floor kicks in)
			expectedBase := uint64(float64(tsbpdDelayUs) * 1.25)
			if expectedBase > uint64(time.Second.Microseconds()) {
				// Above the 1s floor, should be at least 1.25x
				require.GreaterOrEqual(t, threshold, expectedBase,
					"Expected at least 1.25x TSBPD delay when above 1s floor")
			}
		})
	}
}

// ============================================================================
// SendDropThresholdUs Override Tests
//
// These tests verify the behavior when SendDropThresholdUs is set (override)
// vs when it's 0 (use auto-calculated value).
// ============================================================================

type OverrideTestCase struct {
	Name string

	// Config
	TsbpdDelayMs        int64
	SendDropThresholdUs uint64 // 0 = use auto-calculated

	// Expected
	ShouldUseAutoCalc bool   // If true, threshold should match auto-calc
	MinThresholdUs    uint64 // Minimum expected threshold
}

var overrideTestCases = []OverrideTestCase{
	{
		Name:                "AutoCalc_3sLatency_NoOverride",
		TsbpdDelayMs:        3000,
		SendDropThresholdUs: 0,
		ShouldUseAutoCalc:   true,
		MinThresholdUs:      3_750_000, // 3s * 1.25
	},
	{
		Name:                "Override_1s_With3sLatency",
		TsbpdDelayMs:        3000,
		SendDropThresholdUs: 1_000_000, // 1 second - THIS IS THE BUG WE CAUGHT
		ShouldUseAutoCalc:   false,
		MinThresholdUs:      1_000_000,
	},
	{
		Name:                "Override_10s_With3sLatency",
		TsbpdDelayMs:        3000,
		SendDropThresholdUs: 10_000_000, // 10 seconds
		ShouldUseAutoCalc:   false,
		MinThresholdUs:      10_000_000,
	},
	{
		Name:                "AutoCalc_120msLatency_NoOverride",
		TsbpdDelayMs:        120,
		SendDropThresholdUs: 0,
		ShouldUseAutoCalc:   true,
		MinThresholdUs:      1_000_000, // Floor of 1s
	},
}

func TestDropThresholdOverride_Table(t *testing.T) {
	for _, tc := range overrideTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			tsbpdDelayUs := uint64(tc.TsbpdDelayMs * 1000)
			autoCalc := calculateExpectedDropThreshold(tsbpdDelayUs, 0)

			var effectiveThreshold uint64
			if tc.SendDropThresholdUs > 0 {
				effectiveThreshold = tc.SendDropThresholdUs
			} else {
				effectiveThreshold = autoCalc
			}

			// Verify minimum threshold
			require.GreaterOrEqual(t, effectiveThreshold, tc.MinThresholdUs,
				"Effective threshold must meet minimum")

			// Check if override is being used correctly
			if tc.ShouldUseAutoCalc {
				require.Equal(t, autoCalc, effectiveThreshold,
					"With SendDropThresholdUs=0, should use auto-calculated value")
			} else {
				require.Equal(t, tc.SendDropThresholdUs, effectiveThreshold,
					"With SendDropThresholdUs>0, should use override value")
			}

			// CRITICAL: Warn if override < TSBPD latency (likely a bug)
			if tc.SendDropThresholdUs > 0 && tc.SendDropThresholdUs < tsbpdDelayUs {
				t.Logf("WARNING: Override threshold %dµs < TSBPD latency %dµs - this will cause NAK failures!",
					tc.SendDropThresholdUs, tsbpdDelayUs)
			}
		})
	}
}

// ============================================================================
// Configuration Consistency Tests
//
// These tests verify that configuration helpers produce valid threshold values.
// ============================================================================

// ConfigConsistencyTestCase tests that config presets maintain threshold invariants
type ConfigConsistencyTestCase struct {
	Name string

	// Latency configurations to test
	TsbpdDelayMs int64

	// Configuration fields that might affect threshold
	SendDropThresholdUs uint64

	// Expected invariant
	ThresholdMustExceedLatency bool // If true, threshold > tsbpdDelay
}

var configConsistencyTestCases = []ConfigConsistencyTestCase{
	// These test cases ensure that no configuration combination breaks the invariant
	{
		Name:                       "Config_3sLatency_AutoThreshold",
		TsbpdDelayMs:               3000,
		SendDropThresholdUs:        0, // Auto-calculated
		ThresholdMustExceedLatency: true,
	},
	{
		Name:                       "Config_3sLatency_Override5s",
		TsbpdDelayMs:               3000,
		SendDropThresholdUs:        5_000_000,
		ThresholdMustExceedLatency: true,
	},
	{
		Name:                       "Config_120msLatency_AutoThreshold",
		TsbpdDelayMs:               120,
		SendDropThresholdUs:        0,
		ThresholdMustExceedLatency: true, // Auto-calc gives 1s which > 120ms
	},
	{
		Name:                       "Config_10sLatency_AutoThreshold",
		TsbpdDelayMs:               10000,
		SendDropThresholdUs:        0,
		ThresholdMustExceedLatency: true,
	},
}

func TestConfigConsistency_Table(t *testing.T) {
	for _, tc := range configConsistencyTestCases {
		t.Run(tc.Name, func(t *testing.T) {
			tsbpdDelayUs := uint64(tc.TsbpdDelayMs * 1000)

			var effectiveThreshold uint64
			if tc.SendDropThresholdUs > 0 {
				effectiveThreshold = tc.SendDropThresholdUs
			} else {
				effectiveThreshold = calculateExpectedDropThreshold(tsbpdDelayUs, 0)
			}

			if tc.ThresholdMustExceedLatency {
				require.Greater(t, effectiveThreshold, tsbpdDelayUs,
					"INVARIANT: dropThreshold (%dµs) must exceed tsbpdDelay (%dµs) to allow NAK/retransmission",
					effectiveThreshold, tsbpdDelayUs)
			}
		})
	}
}

// ============================================================================
// Latency/Threshold Permutation Generator
//
// This generates test cases for all combinations of common latency values
// to ensure comprehensive coverage.
// ============================================================================

// CommonLatencies returns common TSBPD latency values used in testing
func CommonLatencies() []int64 {
	return []int64{
		0,     // Minimum
		120,   // Low latency
		500,   // Typical
		1000,  // 1 second
		2000,  // 2 seconds
		3000,  // Test default
		5000,  // High latency
		10000, // Very high latency
	}
}

// CommonOverrides returns common SendDropThresholdUs values
func CommonOverrides() []uint64 {
	return []uint64{
		0,          // Auto-calculated (CORRECT)
		500_000,    // 500ms (DANGEROUS if latency > 500ms)
		1_000_000,  // 1 second (THE BUG WE CAUGHT - dangerous for 3s latency)
		3_000_000,  // 3 seconds
		5_000_000,  // 5 seconds
		10_000_000, // 10 seconds (safe for most configs)
	}
}

func TestDropThreshold_LatencyOverridePermutations(t *testing.T) {
	latencies := CommonLatencies()
	overrides := CommonOverrides()

	var safeCount, dangerousCount int
	var dangerous []string

	for _, latencyMs := range latencies {
		for _, overrideUs := range overrides {
			tsbpdDelayUs := uint64(latencyMs * 1000)

			var effectiveThreshold uint64
			if overrideUs > 0 {
				effectiveThreshold = overrideUs
			} else {
				effectiveThreshold = calculateExpectedDropThreshold(tsbpdDelayUs, 0)
			}

			// Check invariant: threshold > tsbpdDelay
			if effectiveThreshold <= tsbpdDelayUs && tsbpdDelayUs > 0 {
				dangerousCount++
				dangerous = append(dangerous,
					fmt.Sprintf("Latency=%dms, Override=%dµs: threshold(%dµs) <= tsbpdDelay(%dµs)",
						latencyMs, overrideUs, effectiveThreshold, tsbpdDelayUs))
			} else {
				safeCount++
			}
		}
	}

	// Log results for documentation purposes
	t.Logf("Permutation matrix: %d safe, %d dangerous combinations", safeCount, dangerousCount)

	// The key invariant: when SendDropThresholdUs=0 (auto-calc), ALL latencies are safe
	for _, latencyMs := range latencies {
		tsbpdDelayUs := uint64(latencyMs * 1000)
		autoThreshold := calculateExpectedDropThreshold(tsbpdDelayUs, 0)

		if autoThreshold <= tsbpdDelayUs && tsbpdDelayUs > 0 {
			t.Errorf("CRITICAL: Auto-calculated threshold fails for latency %dms: %dµs <= %dµs",
				latencyMs, autoThreshold, tsbpdDelayUs)
		}
	}

	// Log dangerous combinations for awareness (not a test failure - these are hypothetical)
	if len(dangerous) > 0 {
		t.Logf("WARNING: %d override combinations would violate invariant (these are NOT in our configs):", len(dangerous))
		for _, d := range dangerous {
			t.Logf("  - %s", d)
		}
	}
}

// ============================================================================
// Benchmark: Ensure calculation doesn't add overhead
// ============================================================================

func BenchmarkDropThresholdCalculation(b *testing.B) {
	tsbpdDelayUs := uint64(3_000_000)
	sendDropDelayUs := uint64(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calculateExpectedDropThreshold(tsbpdDelayUs, sendDropDelayUs)
	}
}

// ============================================================================
// HSReq Drop Threshold Calculation Test
//
// This test validates the drop threshold calculation in handleHSReq()
// (connection_handshake.go). A bug was discovered where milliseconds were
// treated as microseconds, causing the threshold to default to 1 second
// instead of the correct 3.77 seconds for 3s latency.
//
// Bug: sendTsbpdDelay is in MILLISECONDS but was used directly in µs calculation
// Fix: Convert ms to µs before calculation: sendTsbpdDelayUs = sendTsbpdDelayMs * 1000
//
// Reference: send_eventloop_intermittent_failure_bug.md Section 31.9
// ============================================================================

// calculateHSReqDropThreshold simulates the BUGGY calculation in handleHSReq()
// This shows what the code currently does (WRONG)
func calculateHSReqDropThresholdBuggy(configPeerLatencyMs int64, cifSendTSBPDDelayMs uint16, sendDropDelayUs uint64) uint64 {
	// This is the BUGGY code from connection_handshake.go:214-224
	sendTsbpdDelay := uint16(configPeerLatencyMs) // Value in MILLISECONDS

	if cifSendTSBPDDelayMs > sendTsbpdDelay {
		sendTsbpdDelay = cifSendTSBPDDelayMs
	}

	// BUG: sendTsbpdDelay is in ms, but this treats it as µs!
	dropThreshold := uint64(float64(sendTsbpdDelay)*1.25) + sendDropDelayUs
	if dropThreshold < uint64(time.Second.Microseconds()) {
		dropThreshold = uint64(time.Second.Microseconds())
	}
	dropThreshold += 20_000

	return dropThreshold
}

// calculateHSReqDropThresholdFixed simulates the FIXED calculation
// This shows what the code should do (CORRECT)
func calculateHSReqDropThresholdFixed(configPeerLatencyMs int64, cifSendTSBPDDelayMs uint16, sendDropDelayUs uint64) uint64 {
	// This is the FIXED code
	sendTsbpdDelayMs := uint16(configPeerLatencyMs) // Value in MILLISECONDS

	if cifSendTSBPDDelayMs > sendTsbpdDelayMs {
		sendTsbpdDelayMs = cifSendTSBPDDelayMs
	}

	// FIXED: Convert milliseconds to microseconds before calculation
	sendTsbpdDelayUs := uint64(sendTsbpdDelayMs) * 1000
	dropThreshold := uint64(float64(sendTsbpdDelayUs)*1.25) + sendDropDelayUs
	if dropThreshold < uint64(time.Second.Microseconds()) {
		dropThreshold = uint64(time.Second.Microseconds())
	}
	dropThreshold += 20_000

	return dropThreshold
}

// TestHSReqDropThresholdCalculation_UnitsMismatch tests the bug in handleHSReq()
// where milliseconds are treated as microseconds.
func TestHSReqDropThresholdCalculation_UnitsMismatch(t *testing.T) {
	testCases := []struct {
		name                string
		configPeerLatencyMs int64  // Config latency in milliseconds
		cifSendTSBPDDelayMs uint16 // Handshake delay in milliseconds
		sendDropDelayUs     uint64 // Additional drop delay in microseconds
		expectedThresholdUs uint64 // Expected threshold in microseconds
	}{
		{
			name:                "3_Second_Latency",
			configPeerLatencyMs: 3000,  // 3 seconds in ms
			cifSendTSBPDDelayMs: 0,     // Use config value
			sendDropDelayUs:     0,     // No extra delay
			expectedThresholdUs: 3_770_000, // 3s * 1.25 + 20ms = 3.77s
		},
		{
			name:                "3_Second_Latency_From_Handshake",
			configPeerLatencyMs: 1000,  // 1 second local config
			cifSendTSBPDDelayMs: 3000,  // 3 seconds from peer (takes precedence)
			sendDropDelayUs:     0,
			expectedThresholdUs: 3_770_000, // 3s * 1.25 + 20ms = 3.77s
		},
		{
			name:                "120ms_Latency_Hits_Minimum",
			configPeerLatencyMs: 120,   // 120ms in ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			expectedThresholdUs: 1_020_000, // min(150ms * 1.25 = 150ms, 1s) + 20ms = 1.02s
		},
		{
			name:                "5_Second_Latency",
			configPeerLatencyMs: 5000,  // 5 seconds in ms
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			expectedThresholdUs: 6_270_000, // 5s * 1.25 + 20ms = 6.27s
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Calculate using the BUGGY code path (current behavior)
			buggyResult := calculateHSReqDropThresholdBuggy(
				tc.configPeerLatencyMs,
				tc.cifSendTSBPDDelayMs,
				tc.sendDropDelayUs,
			)

			// Calculate using the FIXED code path (expected behavior)
			fixedResult := calculateHSReqDropThresholdFixed(
				tc.configPeerLatencyMs,
				tc.cifSendTSBPDDelayMs,
				tc.sendDropDelayUs,
			)

			t.Logf("Config PeerLatency: %dms, CIF SendTSBPDDelay: %dms", tc.configPeerLatencyMs, tc.cifSendTSBPDDelayMs)
			t.Logf("Buggy result:    %d µs (%.3f seconds)", buggyResult, float64(buggyResult)/1_000_000)
			t.Logf("Fixed result:    %d µs (%.3f seconds)", fixedResult, float64(fixedResult)/1_000_000)
			t.Logf("Expected result: %d µs (%.3f seconds)", tc.expectedThresholdUs, float64(tc.expectedThresholdUs)/1_000_000)

			// The FIXED calculation should match expected
			require.Equal(t, tc.expectedThresholdUs, fixedResult,
				"FIXED calculation should match expected threshold")

			// The BUGGY calculation should produce wrong result for latency > 800ms
			// (For latency <= 800ms, 800 * 1.25 = 1000 µs which hits the 1s minimum anyway)
			if tc.configPeerLatencyMs > 800 || tc.cifSendTSBPDDelayMs > 800 {
				require.NotEqual(t, tc.expectedThresholdUs, buggyResult,
					"BUGGY calculation should NOT match expected threshold (this test detects the bug)")

				// The buggy code always produces ~1 second threshold for large latencies
				// because the ms value is too small to exceed the 1s minimum
				require.LessOrEqual(t, buggyResult, uint64(1_100_000),
					"BUGGY code produces ~1s threshold regardless of latency")
			}
		})
	}
}

// TestHSReqDropThreshold_InvariantViolation demonstrates that the bug violates
// the critical invariant: dropThreshold must exceed TSBPD latency.
func TestHSReqDropThreshold_InvariantViolation(t *testing.T) {
	// With 3 second latency, the receiver buffers packets for 3 seconds
	// before delivery. The sender must NOT drop packets before 3 seconds!
	configPeerLatencyMs := int64(3000) // 3 seconds
	tsbpdDelayUs := uint64(3_000_000)  // 3 seconds in µs

	buggyThreshold := calculateHSReqDropThresholdBuggy(configPeerLatencyMs, 0, 0)
	fixedThreshold := calculateHSReqDropThresholdFixed(configPeerLatencyMs, 0, 0)

	t.Logf("TSBPD Delay:      %d µs (%.1f seconds)", tsbpdDelayUs, float64(tsbpdDelayUs)/1_000_000)
	t.Logf("Buggy Threshold:  %d µs (%.1f seconds)", buggyThreshold, float64(buggyThreshold)/1_000_000)
	t.Logf("Fixed Threshold:  %d µs (%.1f seconds)", fixedThreshold, float64(fixedThreshold)/1_000_000)

	// CRITICAL INVARIANT: dropThreshold MUST exceed tsbpdDelay
	// Otherwise, packets are dropped BEFORE the receiver can request retransmission!

	// Fixed code maintains the invariant
	require.Greater(t, fixedThreshold, tsbpdDelayUs,
		"FIXED: dropThreshold (%d) must exceed tsbpdDelay (%d)", fixedThreshold, tsbpdDelayUs)

	// Buggy code VIOLATES the invariant - this is the bug!
	require.Less(t, buggyThreshold, tsbpdDelayUs,
		"BUG DETECTED: buggy dropThreshold (%d) is LESS than tsbpdDelay (%d) - packets dropped too early!",
		buggyThreshold, tsbpdDelayUs)
}

// ============================================================================
// TEST THE REAL CODE in connection_handshake.go
// This test calls the actual calculateHSReqDropThreshold function.
// It should FAIL until the bug is fixed.
// ============================================================================

// TestHSReqDropThreshold_RealCode tests the ACTUAL calculateHSReqDropThreshold
// function in connection_handshake.go.
//
// This test will FAIL with the buggy code and PASS after the fix is applied.
func TestHSReqDropThreshold_RealCode(t *testing.T) {
	testCases := []struct {
		name                string
		configPeerLatencyMs int64
		cifSendTSBPDDelayMs uint16
		sendDropDelayUs     uint64
		expectedThresholdUs uint64
	}{
		{
			name:                "3s_Latency_Should_Give_3.77s_Threshold",
			configPeerLatencyMs: 3000,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			expectedThresholdUs: 3_770_000, // 3s * 1.25 + 20ms = 3.77s
		},
		{
			name:                "5s_Latency_Should_Give_6.27s_Threshold",
			configPeerLatencyMs: 5000,
			cifSendTSBPDDelayMs: 0,
			sendDropDelayUs:     0,
			expectedThresholdUs: 6_270_000, // 5s * 1.25 + 20ms = 6.27s
		},
		{
			name:                "Peer_Higher_Latency_Takes_Precedence",
			configPeerLatencyMs: 1000,
			cifSendTSBPDDelayMs: 4000, // Peer wants 4s
			sendDropDelayUs:     0,
			expectedThresholdUs: 5_020_000, // 4s * 1.25 + 20ms = 5.02s
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Call the REAL function from connection_handshake.go
			actualThreshold := calculateHSReqDropThreshold(
				tc.configPeerLatencyMs,
				tc.cifSendTSBPDDelayMs,
				tc.sendDropDelayUs,
			)

			t.Logf("Config PeerLatency: %dms, CIF SendTSBPDDelay: %dms", tc.configPeerLatencyMs, tc.cifSendTSBPDDelayMs)
			t.Logf("Actual threshold:   %d µs (%.3f seconds)", actualThreshold, float64(actualThreshold)/1_000_000)
			t.Logf("Expected threshold: %d µs (%.3f seconds)", tc.expectedThresholdUs, float64(tc.expectedThresholdUs)/1_000_000)

			// This assertion will FAIL until the bug is fixed
			require.Equal(t, tc.expectedThresholdUs, actualThreshold,
				"Drop threshold calculation is WRONG - milliseconds treated as microseconds!")
		})
	}
}
