package send

import (
	"runtime"
	"sync/atomic"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Adaptive Backoff for EventLoop
//
// Automatically switches between Sleep and Yield modes based on connection activity:
// - YIELD mode (default): Uses runtime.Gosched() - ~6.2M iterations/sec
// - SLEEP mode: Uses time.Sleep() - ~945 iterations/sec, saves CPU when idle
//
// Strategy: Start in Yield (ready for any throughput), relax to Sleep when idle.
//
// See: documentation/adaptive_eventloop_mode_design.md
// ═══════════════════════════════════════════════════════════════════════════════

// EventLoopMode represents the current backoff strategy
type EventLoopMode int32

const (
	// EventLoopModeSleep uses time.Sleep() - CPU friendly but slow (~945 iter/sec)
	EventLoopModeSleep EventLoopMode = iota

	// EventLoopModeYield uses runtime.Gosched() - high throughput (~6.2M iter/sec)
	EventLoopModeYield
)

// String returns a human-readable name for the mode
func (m EventLoopMode) String() string {
	switch m {
	case EventLoopModeSleep:
		return "Sleep"
	case EventLoopModeYield:
		return "Yield"
	default:
		return "Unknown"
	}
}

// Default configuration
const (
	// DefaultIdleThreshold is the time without activity before switching to Sleep mode
	// NOTE: This is used for initialization but the actual switching now uses
	// iteration counting (like go-lock-free-ring AutoAdaptive) for faster wake-up.
	DefaultIdleThreshold = 1 * time.Second

	// SleepDuration is how long to sleep in Sleep mode
	// NOTE: Linux kernel has ~1ms minimum granularity, so this will be ~1ms actual.
	SleepDuration = 100 * time.Microsecond

	// DefaultIdleIterations is the number of consecutive idle iterations before
	// switching from Yield to Sleep mode. This is used instead of time-based
	// threshold for faster wake-up response.
	// At ~46M iterations/sec in Yield mode, 100000 iterations = ~2ms of idle time.
	DefaultIdleIterations = 100000

	// WarmupIterations is how many iterations to stay in Yield after activity
	// before counting idle iterations again. Prevents thrashing.
	WarmupIterations = 1000
)

// adaptiveBackoff manages automatic switching between Sleep and Yield modes
// based on connection activity. Thread-safe via atomic operations.
//
// State Machine:
//
//	[YIELD] (default start)
//	   │
//	   ├── idle for N iterations → [SLEEP]
//	   │
//	   └── any activity → stays [YIELD], reset idle counter
//
//	[SLEEP]
//	   │
//	   └── any activity → [YIELD] (immediate wake)
//
// Why iteration counting instead of time?
// - time.Sleep() has ~1ms minimum granularity on Linux
// - Time-based approach: wake-up latency = up to 1ms (bad for high throughput!)
// - Iteration-based: wake-up latency = 0 (instant transition to Yield)
type adaptiveBackoff struct {
	// mode is the current EventLoopMode (atomic for thread safety)
	mode atomic.Int32

	// idleIterations counts consecutive iterations without activity
	// Reset to 0 on any activity. When it reaches idleThreshold, switch to Sleep.
	idleIterations atomic.Int64

	// warmupRemaining counts iterations after activity before idle counting starts
	// This prevents thrashing when bursts are followed by brief gaps.
	warmupRemaining atomic.Int64

	// Configuration
	idleThreshold   int64 // Iterations without activity before Sleep
	warmupThreshold int64 // Iterations to stay in Yield after activity

	// Metrics (optional, can be nil)
	modeSwitches atomic.Uint64
}

// newAdaptiveBackoff creates a new adaptive backoff starting in Yield mode.
// This is the recommended starting mode as users connect for a reason - they have data.
func newAdaptiveBackoff() *adaptiveBackoff {
	return newAdaptiveBackoffWithIterations(DefaultIdleIterations, WarmupIterations)
}

// newAdaptiveBackoffWithThreshold creates a new adaptive backoff with custom idle threshold.
//
// Deprecated: Use newAdaptiveBackoffWithIterations for better wake-up latency.
func newAdaptiveBackoffWithThreshold(idleThreshold time.Duration) *adaptiveBackoff {
	// Convert time to approximate iterations (assuming ~46M iter/sec in Yield mode)
	iterations := int64(idleThreshold.Seconds() * 46_000_000)
	if iterations < 1000 {
		iterations = 1000 // Minimum
	}
	return newAdaptiveBackoffWithIterations(iterations, WarmupIterations)
}

// newAdaptiveBackoffWithIterations creates a new adaptive backoff with custom iteration thresholds.
func newAdaptiveBackoffWithIterations(idleThreshold, warmupThreshold int64) *adaptiveBackoff {
	ab := &adaptiveBackoff{
		idleThreshold:   idleThreshold,
		warmupThreshold: warmupThreshold,
	}
	// Start in Yield mode - ready for immediate high throughput
	ab.mode.Store(int32(EventLoopModeYield))
	// Start with warmup complete (no delay on first activity)
	ab.warmupRemaining.Store(0)
	ab.idleIterations.Store(0)
	return ab
}

// Mode returns the current EventLoopMode (for metrics/debugging)
func (ab *adaptiveBackoff) Mode() EventLoopMode {
	return EventLoopMode(ab.mode.Load())
}

// ModeSwitches returns the total number of mode switches (for metrics)
func (ab *adaptiveBackoff) ModeSwitches() uint64 {
	return ab.modeSwitches.Load()
}

// Wait performs the appropriate wait based on current mode and activity.
// Called once per EventLoop iteration.
//
// Parameters:
//   - hadActivity: true if any packets were processed this iteration
//
// Behavior:
//   - YIELD mode: runtime.Gosched(), check for idle→Sleep transition via iteration count
//   - SLEEP mode: time.Sleep(), IMMEDIATE wake on any activity (no sleep delay!)
//
// Key Insight: Using iteration counting instead of time.Sleep() for mode transitions
// gives us instant wake-up latency when data arrives after idle period.
func (ab *adaptiveBackoff) Wait(hadActivity bool) {
	mode := EventLoopMode(ab.mode.Load())

	switch mode {
	case EventLoopModeYield:
		if hadActivity {
			// Activity! Reset idle counter and set warmup period.
			ab.idleIterations.Store(0)
			ab.warmupRemaining.Store(ab.warmupThreshold)
		} else {
			// No activity this iteration
			warmup := ab.warmupRemaining.Load()
			if warmup > 0 {
				// Still in warmup period - don't count as idle
				ab.warmupRemaining.Add(-1)
			} else {
				// Warmup complete - count idle iterations
				idle := ab.idleIterations.Add(1)
				if idle >= ab.idleThreshold {
					// Too many idle iterations - switch to Sleep mode
					ab.mode.Store(int32(EventLoopModeSleep))
					ab.modeSwitches.Add(1)
					ab.idleIterations.Store(0)
				}
			}
		}
		runtime.Gosched()

	case EventLoopModeSleep:
		// Any activity IMMEDIATELY wakes us to Yield - no sleep delay!
		// This is critical for high-throughput: we can't afford 1ms wake-up latency.
		if hadActivity {
			ab.mode.Store(int32(EventLoopModeYield))
			ab.modeSwitches.Add(1)
			ab.warmupRemaining.Store(ab.warmupThreshold)
			ab.idleIterations.Store(0)
			runtime.Gosched() // Don't sleep, we have work!
			return
		}
		// Actually idle - sleep to save CPU
		time.Sleep(SleepDuration)
	}
}

// SetMode forces a specific mode (for testing and manual override)
func (ab *adaptiveBackoff) SetMode(mode EventLoopMode) {
	oldMode := EventLoopMode(ab.mode.Load())
	if oldMode != mode {
		ab.mode.Store(int32(mode))
		ab.modeSwitches.Add(1)
	}
}

// ResetActivity resets idle counters to prevent immediate Sleep transition.
// Useful for preventing immediate Sleep transition after mode override.
func (ab *adaptiveBackoff) ResetActivity() {
	ab.idleIterations.Store(0)
	ab.warmupRemaining.Store(ab.warmupThreshold)
}
