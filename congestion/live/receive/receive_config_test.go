//go:build testing
// +build testing

package receive

import (
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/metrics"
	"github.com/randomizedcoder/gosrt/packet"
)

// TestReceiverCreationWithNakBtreeConfig verifies that New() correctly
// initializes the receiver with NAK btree configuration when passed directly.
//
// This test should PASS - it tests the receiver creation logic itself,
// not the connection-to-receiver wiring.
func TestReceiverCreationWithNakBtreeConfig(t *testing.T) {
	testMetrics := metrics.NewConnectionMetrics()
	testMetrics.HeaderSize.Store(44)

	testCases := []struct {
		name   string
		config Config
		expect TestReceiverInternals
	}{
		{
			name: "NAK btree disabled (default)",
			config: Config{
				InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
				PeriodicACKInterval:    10_000,
				PeriodicNAKInterval:    20_000,
				ConnectionMetrics:      testMetrics,
				PacketReorderAlgorithm: "list",
				UseNakBtree:            false,
			},
			expect: TestReceiverInternals{
				UseNakBtree:     false,
				NakBtreeCreated: false,
			},
		},
		{
			name: "NAK btree enabled with full config",
			config: Config{
				InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
				PeriodicACKInterval:    10_000,
				PeriodicNAKInterval:    20_000,
				ConnectionMetrics:      testMetrics,
				PacketReorderAlgorithm: "btree",
				UseNakBtree:            true,
				SuppressImmediateNak:   true,
				TsbpdDelay:             3_000_000, // 3 seconds in µs
				NakRecentPercent:       0.15,
				NakMergeGap:            5,
				FastNakEnabled:         true,
				FastNakRecentEnabled:   true,
			},
			expect: TestReceiverInternals{
				UseNakBtree:          true,
				SuppressImmediateNak: true,
				TsbpdDelay:           3_000_000,
				NakRecentPercent:     0.15,
				NakBtreeCreated:      true,
				FastNakEnabled:       true,
				FastNakRecentEnabled: true,
				NakMergeGap:          5,
			},
		},
		{
			name: "NAK btree enabled with partial config",
			config: Config{
				InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
				PeriodicACKInterval:    10_000,
				PeriodicNAKInterval:    20_000,
				ConnectionMetrics:      testMetrics,
				PacketReorderAlgorithm: "btree",
				UseNakBtree:            true,
				NakRecentPercent:       0.10,
				// Other fields left as defaults
			},
			expect: TestReceiverInternals{
				UseNakBtree:          true,
				SuppressImmediateNak: false, // Not set
				TsbpdDelay:           0,     // Not set
				NakRecentPercent:     0.10,
				NakBtreeCreated:      true,
				FastNakEnabled:       false, // Not set
				FastNakRecentEnabled: false, // Not set
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			recv := New(tc.config)
			r := recv.(*receiver)
			internals := r.GetTestInternals()

			// Check UseNakBtree
			if internals.UseNakBtree != tc.expect.UseNakBtree {
				t.Errorf("UseNakBtree: expected %v, got %v", tc.expect.UseNakBtree, internals.UseNakBtree)
			}

			// Check SuppressImmediateNak
			if internals.SuppressImmediateNak != tc.expect.SuppressImmediateNak {
				t.Errorf("SuppressImmediateNak: expected %v, got %v", tc.expect.SuppressImmediateNak, internals.SuppressImmediateNak)
			}

			// Check TsbpdDelay
			if internals.TsbpdDelay != tc.expect.TsbpdDelay {
				t.Errorf("TsbpdDelay: expected %d, got %d", tc.expect.TsbpdDelay, internals.TsbpdDelay)
			}

			// Check NakRecentPercent
			if internals.NakRecentPercent != tc.expect.NakRecentPercent {
				t.Errorf("NakRecentPercent: expected %f, got %f", tc.expect.NakRecentPercent, internals.NakRecentPercent)
			}

			// Check NakBtreeCreated
			if internals.NakBtreeCreated != tc.expect.NakBtreeCreated {
				t.Errorf("NakBtreeCreated: expected %v, got %v", tc.expect.NakBtreeCreated, internals.NakBtreeCreated)
			}

			// Check FastNakEnabled
			if internals.FastNakEnabled != tc.expect.FastNakEnabled {
				t.Errorf("FastNakEnabled: expected %v, got %v", tc.expect.FastNakEnabled, internals.FastNakEnabled)
			}

			// Check FastNakRecentEnabled
			if internals.FastNakRecentEnabled != tc.expect.FastNakRecentEnabled {
				t.Errorf("FastNakRecentEnabled: expected %v, got %v", tc.expect.FastNakRecentEnabled, internals.FastNakRecentEnabled)
			}

			// Check NakMergeGap
			if internals.NakMergeGap != tc.expect.NakMergeGap {
				t.Errorf("NakMergeGap: expected %d, got %d", tc.expect.NakMergeGap, internals.NakMergeGap)
			}
		})
	}
}

// TestPeriodicNakDispatchesToCorrectImplementation verifies that periodicNAK()
// dispatches to periodicNakBtree() when UseNakBtree=true, and to
// periodicNakOriginal() when UseNakBtree=false.
//
// This test verifies the dispatch logic via metrics.
func TestPeriodicNakDispatchesToCorrectImplementation(t *testing.T) {
	testCases := []struct {
		name               string
		useNakBtree        bool
		expectBtreeRuns    bool
		expectOriginalRuns bool
	}{
		{
			name:               "UseNakBtree=true dispatches to periodicNakBtree",
			useNakBtree:        true,
			expectBtreeRuns:    true,
			expectOriginalRuns: false,
		},
		{
			name:               "UseNakBtree=false dispatches to periodicNakOriginal",
			useNakBtree:        false,
			expectBtreeRuns:    false,
			expectOriginalRuns: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testMetrics := metrics.NewConnectionMetrics()
			testMetrics.HeaderSize.Store(44)

			config := Config{
				InitialSequenceNumber:  circular.New(0, packet.MAX_SEQUENCENUMBER),
				PeriodicACKInterval:    10_000, // 10ms in µs
				PeriodicNAKInterval:    20_000, // 20ms in µs
				ConnectionMetrics:      testMetrics,
				PacketReorderAlgorithm: "btree",
				UseNakBtree:            tc.useNakBtree,
				TsbpdDelay:             3_000_000, // 3 seconds in µs
				NakRecentPercent:       0.10,
			}

			recv := New(config)
			r := recv.(*receiver)

			// Trigger periodic NAK by calling Tick with enough time elapsed
			// periodicNAKInterval is 20ms = 20_000µs, so we call at 100ms = 100_000µs
			r.Tick(100_000)

			// Check metrics
			btreeRuns := testMetrics.NakPeriodicBtreeRuns.Load()
			originalRuns := testMetrics.NakPeriodicOriginalRuns.Load()

			if tc.expectBtreeRuns && btreeRuns == 0 {
				t.Error("Expected NakPeriodicBtreeRuns > 0, got 0")
			}
			if !tc.expectBtreeRuns && btreeRuns > 0 {
				t.Errorf("Expected NakPeriodicBtreeRuns == 0, got %d", btreeRuns)
			}

			if tc.expectOriginalRuns && originalRuns == 0 {
				t.Error("Expected NakPeriodicOriginalRuns > 0, got 0")
			}
			if !tc.expectOriginalRuns && originalRuns > 0 {
				t.Errorf("Expected NakPeriodicOriginalRuns == 0, got %d", originalRuns)
			}
		})
	}
}
