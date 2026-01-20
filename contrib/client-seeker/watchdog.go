package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// WatchdogConfig configures the watchdog behavior.
type WatchdogConfig struct {
	Enabled     bool          // Enable watchdog (default: true)
	Timeout     time.Duration // Time without heartbeat before soft-landing (default: 5s)
	SafeBitrate int64         // Fallback bitrate on timeout (default: 10 Mb/s)
	StopTimeout time.Duration // Stop entirely after this (default: 30s, 0 = never)
}

// DefaultWatchdogConfig returns sensible defaults.
func DefaultWatchdogConfig() WatchdogConfig {
	return WatchdogConfig{
		Enabled:     true,
		Timeout:     5 * time.Second,
		SafeBitrate: 10_000_000, // 10 Mb/s
		StopTimeout: 30 * time.Second,
	}
}

// WatchdogState represents the current watchdog state.
type WatchdogStateEnum int

const (
	WatchdogStateNormal WatchdogStateEnum = iota
	WatchdogStateWarning
	WatchdogStateCritical
)

func (s WatchdogStateEnum) String() string {
	switch s {
	case WatchdogStateNormal:
		return WatchdogNormal
	case WatchdogStateWarning:
		return WatchdogWarning
	case WatchdogStateCritical:
		return WatchdogCritical
	default:
		return "unknown"
	}
}

// Watchdog monitors heartbeats and implements tiered soft-landing.
//
// Behavior:
// 1. Normal: Receiving heartbeats, full operation
// 2. Warning: No heartbeat for Timeout, drop to SafeBitrate
// 3. Critical: No heartbeat for StopTimeout, stop process
//
// The tiered approach allows recovery from GC pauses and temporary
// Orchestrator lag without requiring a full restart.
type Watchdog struct {
	config   WatchdogConfig
	control  *ControlServer
	bm       *BitrateManager
	stopFunc context.CancelFunc

	// Current state (atomic for thread-safe reads)
	state atomic.Int32

	// Saved bitrate before soft-landing (for logging)
	savedBitrate int64
}

// NewWatchdog creates a new watchdog.
//
// Parameters:
//   - config: Watchdog configuration
//   - cs: ControlServer to monitor heartbeats
//   - bm: BitrateManager to adjust on timeout
//   - stop: Function to call for critical stop
func NewWatchdog(config WatchdogConfig, cs *ControlServer, bm *BitrateManager, stop context.CancelFunc) *Watchdog {
	w := &Watchdog{
		config:   config,
		control:  cs,
		bm:       bm,
		stopFunc: stop,
	}
	w.state.Store(int32(WatchdogStateNormal))
	return w
}

// Run starts the watchdog monitoring loop.
// This should be called as a goroutine.
func (w *Watchdog) Run(ctx context.Context) {
	if !w.config.Enabled {
		return
	}

	// Check twice per second for responsive detection
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.check()
		}
	}
}

// check evaluates the current state and takes action if needed.
func (w *Watchdog) check() {
	elapsed := w.control.TimeSinceHeartbeat()
	currentState := WatchdogStateEnum(w.state.Load())

	switch currentState {
	case WatchdogStateNormal:
		if elapsed > w.config.Timeout {
			// === SOFT LANDING: Drop to safe rate ===
			w.savedBitrate = w.bm.Current()
			fmt.Fprintf(os.Stderr, "watchdog: WARNING - no heartbeat for %v, soft-landing from %s to %s\n",
				elapsed.Round(time.Millisecond),
				FormatBitrate(w.savedBitrate),
				FormatBitrate(w.config.SafeBitrate))

			w.bm.Set(w.config.SafeBitrate)
			w.state.Store(int32(WatchdogStateWarning))
		}

	case WatchdogStateWarning:
		if elapsed < w.config.Timeout {
			// === RECOVERY: Orchestrator is back ===
			fmt.Fprintf(os.Stderr, "watchdog: Orchestrator recovered after %v\n",
				elapsed.Round(time.Millisecond))
			w.state.Store(int32(WatchdogStateNormal))
			// Don't auto-restore bitrate - let Orchestrator set it
		} else if w.config.StopTimeout > 0 && elapsed > w.config.StopTimeout {
			// === CRITICAL: Prepare to stop ===
			fmt.Fprintf(os.Stderr, "watchdog: CRITICAL - no heartbeat for %v, entering critical state\n",
				elapsed.Round(time.Millisecond))
			w.state.Store(int32(WatchdogStateCritical))
		}

	case WatchdogStateCritical:
		if elapsed < w.config.Timeout {
			// === MIRACLE RECOVERY ===
			fmt.Fprintf(os.Stderr, "watchdog: Orchestrator recovered from critical state\n")
			w.state.Store(int32(WatchdogStateNormal))
		} else if w.config.StopTimeout > 0 {
			// === FINAL STOP ===
			fmt.Fprintf(os.Stderr, "watchdog: STOPPING - no heartbeat for %v\n",
				elapsed.Round(time.Millisecond))
			if w.stopFunc != nil {
				w.stopFunc()
			}
		}
	}
}

// State returns the current watchdog state.
func (w *Watchdog) State() WatchdogStateEnum {
	return WatchdogStateEnum(w.state.Load())
}

// StateString returns the current state as a string.
func (w *Watchdog) StateString() string {
	return w.State().String()
}

// IsHealthy returns true if the watchdog is in normal state.
func (w *Watchdog) IsHealthy() bool {
	return w.State() == WatchdogStateNormal
}

// Reset forces the watchdog back to normal state.
// This is useful for testing.
func (w *Watchdog) Reset() {
	w.state.Store(int32(WatchdogStateNormal))
}
