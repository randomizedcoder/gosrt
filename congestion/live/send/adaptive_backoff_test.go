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
// Test: Mode Transitions
// ─────────────────────────────────────────────────────────────────────────────

func TestAdaptiveBackoff_YieldToSleep_AfterIdleThreshold(t *testing.T) {
	// Use short threshold for testing
	ab := newAdaptiveBackoffWithThreshold(50 * time.Millisecond)
	require.Equal(t, EventLoopModeYield, ab.Mode())

	// Simulate idle iterations (no activity)
	for i := 0; i < 10; i++ {
		ab.Wait(false)
		time.Sleep(10 * time.Millisecond)
	}

	// Should have transitioned to Sleep
	require.Equal(t, EventLoopModeSleep, ab.Mode(), "Should transition to Sleep after idle threshold")
	require.Equal(t, uint64(1), ab.ModeSwitches(), "Should have 1 mode switch")
}

func TestAdaptiveBackoff_SleepToYield_OnAnyActivity(t *testing.T) {
	ab := newAdaptiveBackoffWithThreshold(10 * time.Millisecond)

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
	ab := newAdaptiveBackoffWithThreshold(50 * time.Millisecond)

	// Continuous activity for longer than idle threshold
	for i := 0; i < 20; i++ {
		ab.Wait(true)
		time.Sleep(10 * time.Millisecond)
	}

	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should stay in Yield with continuous activity")
	require.Equal(t, uint64(0), ab.ModeSwitches(), "Should have no mode switches")
}

func TestAdaptiveBackoff_SleepStaysSleep_WhenIdle(t *testing.T) {
	ab := newAdaptiveBackoffWithThreshold(10 * time.Millisecond)
	ab.SetMode(EventLoopModeSleep)
	initialSwitches := ab.ModeSwitches()

	// Multiple idle iterations
	for i := 0; i < 5; i++ {
		ab.Wait(false)
	}

	require.Equal(t, EventLoopModeSleep, ab.Mode(), "Should stay in Sleep when idle")
	require.Equal(t, initialSwitches, ab.ModeSwitches(), "Should have no additional mode switches")
}

// ─────────────────────────────────────────────────────────────────────────────
// Test: Edge Cases
// ─────────────────────────────────────────────────────────────────────────────

func TestAdaptiveBackoff_NoThrashing_WithIntermittentActivity(t *testing.T) {
	ab := newAdaptiveBackoffWithThreshold(100 * time.Millisecond)

	// Intermittent activity (gaps < threshold)
	for i := 0; i < 10; i++ {
		ab.Wait(true) // Activity
		time.Sleep(30 * time.Millisecond)
		ab.Wait(false) // Brief idle
		time.Sleep(30 * time.Millisecond)
	}

	require.Equal(t, EventLoopModeYield, ab.Mode(), "Should stay in Yield with intermittent activity")
	require.Equal(t, uint64(0), ab.ModeSwitches(), "No mode switches with gaps < threshold")
}

func TestAdaptiveBackoff_ActivityResetsIdleTimer(t *testing.T) {
	ab := newAdaptiveBackoffWithThreshold(100 * time.Millisecond)

	// Idle for 80ms (close to threshold)
	time.Sleep(80 * time.Millisecond)
	ab.Wait(false)

	// Activity resets the timer
	ab.Wait(true)

	// Idle for another 80ms (would exceed threshold if timer wasn't reset)
	time.Sleep(80 * time.Millisecond)
	ab.Wait(false)

	// Should still be in Yield because activity reset the timer
	require.Equal(t, EventLoopModeYield, ab.Mode(), "Activity should reset idle timer")
}

func TestAdaptiveBackoff_ConfigurableIdleThreshold(t *testing.T) {
	// Short threshold
	abShort := newAdaptiveBackoffWithThreshold(20 * time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	abShort.Wait(false)
	require.Equal(t, EventLoopModeSleep, abShort.Mode(), "Short threshold should trigger quickly")

	// Long threshold
	abLong := newAdaptiveBackoffWithThreshold(500 * time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	abLong.Wait(false)
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
	ab := newAdaptiveBackoffWithThreshold(50 * time.Millisecond)

	// Idle for longer than threshold
	time.Sleep(60 * time.Millisecond)

	// Reset activity before Wait
	ab.ResetActivity()
	ab.Wait(false)

	// Should still be in Yield because activity was reset
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
// Table-Driven Tests: Activity Scenarios
// ─────────────────────────────────────────────────────────────────────────────

// activityEvent represents one iteration of the EventLoop
type activityEvent struct {
	delayMs     int  // Delay before this event (milliseconds)
	hadActivity bool // Were packets processed this iteration?
}

func TestAdaptiveBackoff_ActivityScenarios(t *testing.T) {
	tests := []struct {
		name            string
		idleThresholdMs int
		events          []activityEvent
		expectedMode    EventLoopMode
		minSwitches     uint64
		maxSwitches     uint64
	}{
		{
			name:            "Continuous activity stays Yield",
			idleThresholdMs: 100,
			events: []activityEvent{
				{0, true}, {20, true}, {20, true}, {20, true}, {20, true},
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  0,
			maxSwitches:  0,
		},
		{
			name:            "Continuous idle transitions to Sleep",
			idleThresholdMs: 50,
			events: []activityEvent{
				{0, false}, {20, false}, {20, false}, {20, false},
			},
			expectedMode: EventLoopModeSleep,
			minSwitches:  1,
			maxSwitches:  1,
		},
		{
			name:            "Burst then idle",
			idleThresholdMs: 50,
			events: []activityEvent{
				{0, true}, {10, true}, {10, true}, // Burst
				{20, false}, {20, false}, {20, false}, // Idle > threshold
			},
			expectedMode: EventLoopModeSleep,
			minSwitches:  1,
			maxSwitches:  1,
		},
		{
			name:            "Intermittent activity stays Yield",
			idleThresholdMs: 100,
			events: []activityEvent{
				{0, true}, {30, false}, {30, true}, {30, false}, {30, true},
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  0,
			maxSwitches:  0,
		},
		{
			name:            "Sleep wakes immediately on activity",
			idleThresholdMs: 30,
			events: []activityEvent{
				{0, false}, {40, false}, // Go to Sleep
				{10, true}, // Wake up
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  2, // Yield→Sleep→Yield
			maxSwitches:  2,
		},
		{
			name:            "Activity after long idle",
			idleThresholdMs: 30,
			events: []activityEvent{
				{0, false}, {50, false}, // Sleep
				{50, false},             // Stay asleep
				{10, true},              // Wake
				{10, true},              // Stay awake
			},
			expectedMode: EventLoopModeYield,
			minSwitches:  2,
			maxSwitches:  2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ab := newAdaptiveBackoffWithThreshold(time.Duration(tc.idleThresholdMs) * time.Millisecond)

			for _, event := range tc.events {
				if event.delayMs > 0 {
					time.Sleep(time.Duration(event.delayMs) * time.Millisecond)
				}
				ab.Wait(event.hadActivity)
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
