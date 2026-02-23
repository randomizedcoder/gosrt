// Package live provides table-driven stream matrix tests for the receiver.
//
// This file contains the tiered test runners that execute the test matrix:
//   - Tier 1: Core validation (~50 tests, <3s) - every PR
//   - Tier 2: Extended coverage (~200 tests, <15s) - daily CI
//   - Tier 3: Comprehensive (~600 tests, <60s) - nightly CI
//
// The test infrastructure (types, helpers, generators) is in stream_test_helpers_test.go.
// See documentation/receiver_stream_tests_design.md for full design details.
package receive

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ============================================================================
// TIER TEST FUNCTIONS
// ============================================================================

// TestStream_Tier1 runs core validation tests.
// These tests must pass for every PR.
func TestStream_Tier1(t *testing.T) {
	cases := GenerateTestMatrix(Tier1Options)
	t.Logf("Tier 1: %d test cases", len(cases))
	RunTestMatrix(t, cases)
}

// TestStream_Tier2 runs extended coverage tests.
// These tests run in daily CI.
func TestStream_Tier2(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Tier 2 tests in short mode")
	}
	cases := GenerateTestMatrix(Tier2Options)
	t.Logf("Tier 2: %d test cases", len(cases))
	RunTestMatrix(t, cases)
}

// TestStream_Tier3 runs comprehensive tests.
// These tests run in nightly CI.
func TestStream_Tier3(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Tier 3 tests in short mode")
	}
	cases := GenerateTestMatrix(Tier3Options)
	t.Logf("Tier 3: %d test cases", len(cases))
	RunTestMatrix(t, cases)
}

// TestStream_Framework verifies the test framework itself works.
func TestStream_Framework(t *testing.T) {
	// Verify matrix generation
	tier1Cases := GenerateTestMatrix(Tier1Options)
	require.Greater(t, len(tier1Cases), 0, "Tier1 should generate test cases")
	t.Logf("Tier1 generates %d cases", len(tier1Cases))

	tier2Cases := GenerateTestMatrix(Tier2Options)
	require.Greater(t, len(tier2Cases), len(tier1Cases), "Tier2 should generate more cases than Tier1")
	t.Logf("Tier2 generates %d cases", len(tier2Cases))

	tier3Cases := GenerateTestMatrix(Tier3Options)
	require.Greater(t, len(tier3Cases), len(tier2Cases), "Tier3 should generate more cases than Tier2")
	t.Logf("Tier3 generates %d cases", len(tier3Cases))

	// Verify test naming
	for i, tc := range tier1Cases[:min(5, len(tier1Cases))] {
		t.Logf("Sample case %d: %s", i, tc.Name)
		require.NotEmpty(t, tc.Name, "Test case should have a name")
	}

	// Run a single simple test case to verify the runner works
	t.Run("SingleTest", func(t *testing.T) {
		tc := StreamTestCase{
			Name:           "Framework/Verify",
			ReceiverConfig: CfgNakBtree,
			StreamProfile:  Stream1MbpsShort,
			LossPattern:    NoLoss{},
			ReorderPattern: nil,
			StartSeq:       1,
			TimerProfile:   TimerDefault,
		}
		RunSingleTest(t, tc)
	})
}
