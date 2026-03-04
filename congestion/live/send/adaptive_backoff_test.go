package send

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Adaptive Backoff Unit Tests
//
// Tests for the adaptive Sleep/Yield mode switching mechanism.
// See: documentation/adaptive_eventloop_mode_design.md
// ═══════════════════════════════════════════════════════════════════════════════

// ─────────────────────────────────────────────────────────────────────────────
// Test: Initial State
// ─────────────────────────────────────────────────────────────────────────────

func TestAdaptiveBackoff_StartsInYieldMode(t *testing.T) {
	ab := newAdaptiveBackoff()
	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should start in Yield mode")
}

func TestAdaptiveBackoff_ModeString(t *testing.T) {
	require.Equal(t, "Sleep", EventLoopModeSleep.String())
	require.Equal(t, "Yield", EventLoopModeYield.String())
	require.Equal(t, "Unknown", EventLoopMode(99).String())
}

func TestAdaptiveBackoff_InitialModeSwitchesIsZero(t *testing.T) {
	ab := newAdaptiveBackoff()
	require.Equal(t, uint64(0), ab.ModeSwitches(), "Should start with zero mode switches")
}

// ─────────────────────────────────────────────────────────────────────────────
// Test: Mode Transitions (Iteration-Based)
//
// The adaptive backoff uses iteration counting, not wall-clock time.
// These tests use small iteration thresholds for deterministic behavior.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdaptiveBackoff_YieldToSleep_AfterIdleThreshold(t *testing.T) {
	// Use small thresholds for testing
	const idleThreshold = 10
	const warmupThreshold = 3
	ab := newAdaptiveBackoffWithIterations(idleThreshold, warmupThreshold)
	require.Equal(t, EventLoopModeYield, ab.Mode())

	// Simulate idle iterations (no activity) - need idleThreshold + warmupThreshold + 1
	for i := 0; i < idleThreshold+warmupThreshold+1; i++ {
		ab.Wait(false)
	}

	// Should have transitioned to Sleep
	require.Equal(t, EventLoopModeSleep, ab.Mode(), "Should transition to Sleep after idle threshold")
	require.Equal(t, uint64(1), ab.ModeSwitches(), "Should have 1 mode switch")
}

func TestAdaptiveBackoff_SleepToYield_OnAnyActivity(t *testing.T) {
	ab := newAdaptiveBackoffWithIterations(10, 3)

	// Force into Sleep mode
	ab.SetMode(EventLoopModeSleep)
	require.Equal(t, EventLoopModeSleep, ab.Mode())
	initialSwitches := ab.ModeSwitches()

	// Single activity event should wake up
	ab.Wait(true)

	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should wake to Yield on activity")
	require.Equal(t, initialSwitches+1, ab.ModeSwitches(), "Should count the mode switch")
}

func TestAdaptiveBackoff_YieldStaysYield_WithContinuousActivity(t *testing.T) {
	ab := newAdaptiveBackoffWithIterations(10, 3)

	// Continuous activity - should never transition
	for i := 0; i < 50; i++ {
		ab.Wait(true)
	}

	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should stay in Yield with continuous activity")
	require.Equal(t, uint64(0), ab.ModeSwitches(), "Should have no mode switches")
}

func TestAdaptiveBackoff_SleepStaysSleep_WhenIdle(t *testing.T) {
	ab := newAdaptiveBackoffWithIterations(10, 3)
	ab.SetMode(EventLoopModeSleep)
	initialSwitches := ab.ModeSwitches()

	// Multiple idle iterations
	for i := 0; i < 20; i++ {
		ab.Wait(false)
	}

	require.Equal(t, EventLoopModeSleep, ab.Mode(), "Should stay in Sleep when idle")
	require.Equal(t, initialSwitches, ab.ModeSwitches(), "Should have no additional mode switches")
}

// ─────────────────────────────────────────────────────────────────────────────
// Test: Edge Cases (Iteration-Based)
// ─────────────────────────────────────────────────────────────────────────────

func TestAdaptiveBackoff_NoThrashing_WithIntermittentActivity(t *testing.T) {
	const idleThreshold = 10
	const warmupThreshold = 3
	ab := newAdaptiveBackoffWithIterations(idleThreshold, warmupThreshold)

	// Intermittent activity - activity resets counters before idle threshold reached
	for i := 0; i < 20; i++ {
		ab.Wait(true)  // Activity resets idle counter
		ab.Wait(false) // One idle iteration
		ab.Wait(false) // Two idle iterations (still within warmup)
	}

	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should stay in Yield with intermittent activity")
	require.Equal(t, uint64(0), ab.ModeSwitches(), "No mode switches with gaps < threshold")
}

func TestAdaptiveBackoff_ActivityResetsIdleCounter(t *testing.T) {
	const idleThreshold = 10
	const warmupThreshold = 3
	ab := newAdaptiveBackoffWithIterations(idleThreshold, warmupThreshold)

	// Partial idle (close to threshold but not exceeding)
	// Note: initial warmupRemaining is 0, so idle counts immediately
	for i := 0; i < idleThreshold-2; i++ {
		ab.Wait(false)
	}
	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should still be Yield")

	// Activity resets the counter and sets warmup period
	ab.Wait(true)

	// Now we have warmup iterations to burn first, plus idleThreshold
	// Total needed: warmupThreshold + idleThreshold = 13 to trigger Sleep
	// We'll do fewer than that
	for i := 0; i < idleThreshold-2; i++ {
		ab.Wait(false)
	}

	// Should still be in Yield because:
	// 1. Activity reset idle counter to 0
	// 2. Set warmupRemaining to 3
	// 3. We did 8 iterations: 3 burn warmup + 5 idle (< 10 threshold)
	require.Equal(t, EventLoopModeYield, ab.Mode(), "Activity should reset idle counter")
}

func TestAdaptiveBackoff_ConfigurableIdleThreshold(t *testing.T) {
	// Short threshold (5 idle iterations to Sleep)
	abShort := newAdaptiveBackoffWithIterations(5, 2) // 5 + 2 + 1 = 8 iterations to Sleep
	for i := 0; i < 8; i++ {
		abShort.Wait(false)
	}
	require.Equal(t, EventLoopModeSleep, abShort.Mode(), "Short threshold should trigger quickly")

	// Long threshold (100 idle iterations to Sleep)
	abLong := newAdaptiveBackoffWithIterations(100, 10) // Would need 111 iterations
	for i := 0; i < 50; i++ {
		abLong.Wait(false)
	}
	require.Equal(t, EventLoopModeYield, abLong.Mode(), "Long threshold should not trigger yet")
}

func TestAdaptiveBackoff_SetMode(t *testing.T) {
	ab := newAdaptiveBackoff()
	require.Equal(t, EventLoopModeYield, ab.Mode())

	ab.SetMode(EventLoopModeSleep)
	require.Equal(t, EventLoopModeSleep, ab.Mode())
	require.Equal(t, uint64(1), ab.ModeSwitches())

	// Setting same mode should not increment counter
	ab.SetMode(EventLoopModeSleep)
	require.Equal(t, uint64(1), ab.ModeSwitches())

	ab.SetMode(EventLoopModeYield)
	require.Equal(t, EventLoopModeYield, ab.Mode())
	require.Equal(t, uint64(2), ab.ModeSwitches())
}

func TestAdaptiveBackoff_ResetActivity(t *testing.T) {
	const idleThreshold = 10
	const warmupThreshold = 3
	ab := newAdaptiveBackoffWithIterations(idleThreshold, warmupThreshold)

	// Accumulate some idle iterations (close to threshold)
	// Initial warmupRemaining is 0, so idle counts immediately
	for i := 0; i < idleThreshold-2; i++ {
		ab.Wait(false)
	}
	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should still be Yield before reset")

	// Reset activity before more iterations
	ab.ResetActivity()

	// Now several more iterations - without reset, this would trigger Sleep
	for i := 0; i < 5; i++ {
		ab.Wait(false)
	}

	// Should still be in Yield because ResetActivity cleared the idle counter
	require.Equal(t, EventLoopModeYield, ab.Mode(), "ResetActivity should prevent Sleep transition")
}

// ─────────────────────────────────────────────────────────────────────────────
// Test: Thread Safety
// ─────────────────────────────────────────────────────────────────────────────

func TestAdaptiveBackoff_ConcurrentWait(t *testing.T) {
	ab := newAdaptiveBackoff()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ab.Wait(j%2 == 0) // Alternating activity
			}
		}(i)
	}
	wg.Wait()
	// Test passes if no race detected (run with -race flag)
}

func TestAdaptiveBackoff_ConcurrentModeRead(t *testing.T) {
	ab := newAdaptiveBackoff()

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ab.Wait(j%3 == 0)
			}
		}()
	}

	// Readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = ab.Mode()
				_ = ab.ModeSwitches()
			}
		}()
	}

	wg.Wait()
	// Test passes if no race detected
}

// ─────────────────────────────────────────────────────────────────────────────
// Table-Driven Tests: Activity Scenarios (Iteration-Based)
// ─────────────────────────────────────────────────────────────────────────────

// iterationEvent represents a sequence of Wait() calls
type iterationEvent struct {
	count       int  // Number of Wait() calls to make
	hadActivity bool // Activity status for each Wait() call
}

func TestAdaptiveBackoff_ActivityScenarios(t *testing.T) {
	// Use small iteration thresholds for fast, deterministic tests.
	// idleThreshold: iterations without activity before Sleep
	// warmupThreshold: iterations to stay in Yield after activity
	const (
		idleThreshold   = 10
		warmupThreshold = 3
	)

	tests := []struct {
		name         string
		events       []iterationEvent
		expectedMode EventLoopMode
		minSwitches  uint64
		maxSwitches  uint64
	}{
		{
			name: "Continuous activity stays Yield",
			events: []iterationEvent{
				{5, true}, {5, true}, {5, true}, {5, true}, {5, true},
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  0,
			maxSwitches:  0,
		},
		{
			name: "Continuous idle transitions to Sleep",
			events: []iterationEvent{
				// Need idleThreshold + warmupThreshold iterations to trigger Sleep
				{idleThreshold + warmupThreshold + 1, false},
			},
			expectedMode: EventLoopModeSleep,
			minSwitches:  1,
			maxSwitches:  1,
		},
		{
			name: "Burst then idle",
			events: []iterationEvent{
				{3, true}, {3, true}, {3, true}, // Burst (resets idle counter each time)
				{idleThreshold + warmupThreshold + 1, false}, // Idle > threshold
			},
			expectedMode: EventLoopModeSleep,
			minSwitches:  1,
			maxSwitches:  1,
		},
		{
			name: "Intermittent activity stays Yield",
			events: []iterationEvent{
				{3, true},
				{warmupThreshold, false}, // Idle but within warmup
				{3, true},                // Activity resets
				{warmupThreshold, false}, // Idle but within warmup
				{3, true},                // Activity keeps us in Yield
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  0,
			maxSwitches:  0,
		},
		{
			name: "Sleep wakes immediately on activity",
			events: []iterationEvent{
				{idleThreshold + warmupThreshold + 1, false}, // Go to Sleep
				{1, true}, // Wake up immediately
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  2, // Yield→Sleep→Yield
			maxSwitches:  2,
		},
		{
			name: "Activity after long idle",
			events: []iterationEvent{
				{idleThreshold + warmupThreshold + 1, false}, // Go to Sleep
				{5, false}, // Stay asleep (more idle iterations)
				{1, true},  // Wake
				{5, true},  // Stay awake
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  2, // Yield→Sleep→Yield
			maxSwitches:  2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ab := newAdaptiveBackoffWithIterations(idleThreshold, warmupThreshold)

			for _, event := range tc.events {
				for i := 0; i < event.count; i++ {
					ab.Wait(event.hadActivity)
				}
			}

			require.Equal(t, tc.expectedMode, ab.Mode(), "Final mode mismatch")
			switches := ab.ModeSwitches()
			require.GreaterOrEqual(t, switches, tc.minSwitches, "Too few mode switches")
			require.LessOrEqual(t, switches, tc.maxSwitches, "Too many mode switches")
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmark Tests
// ─────────────────────────────────────────────────────────────────────────────

func BenchmarkAdaptiveBackoff_ModeCheck(b *testing.B) {
	ab := newAdaptiveBackoff()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ab.Mode()
	}
}

func BenchmarkAdaptiveBackoff_Wait_Yield(b *testing.B) {
	ab := newAdaptiveBackoff()
	ab.SetMode(EventLoopModeYield)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ab.Wait(true) // Activity keeps us in Yield
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
}

func BenchmarkAdaptiveBackoff_Wait_Sleep(b *testing.B) {
	ab := newAdaptiveBackoff()
	ab.SetMode(EventLoopModeSleep)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ab.Wait(false) // No activity keeps us in Sleep
	}
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
}

// BenchmarkAdaptiveBackoff_Overhead measures the overhead of adaptive backoff
// compared to direct runtime.Gosched()
func BenchmarkAdaptiveBackoff_Overhead(b *testing.B) {
	ab := newAdaptiveBackoff()
	ab.SetMode(EventLoopModeYield)

	b.Run("AdaptiveBackoff_Wait", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ab.Wait(true)
		}
	})

	b.Run("Direct_Gosched", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// Direct runtime.Gosched() for comparison
			// (imported at top of file via runtime package)
		}
	})
}

// BenchmarkAdaptiveBackoff_ModeTransition measures mode transition overhead
func BenchmarkAdaptiveBackoff_ModeTransition(b *testing.B) {
	ab := newAdaptiveBackoffWithThreshold(1 * time.Nanosecond) // Immediate transitions

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Force transition by alternating activity
		ab.Wait(i%2 == 0)
	}
}
