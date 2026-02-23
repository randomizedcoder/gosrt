package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchdog_NormalOperation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-sensitive watchdog test in short mode")
	}
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	config := WatchdogConfig{
		Enabled:     true,
		Timeout:     100 * time.Millisecond,
		SafeBitrate: 10_000_000,
		StopTimeout: 500 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, cancel)

	// Start watchdog
	go w.Run(ctx)

	// Send heartbeats to keep it healthy
	for i := 0; i < 5; i++ {
		cs.recordHeartbeat()
		time.Sleep(50 * time.Millisecond)
	}

	// Should still be in normal state
	if w.State() != WatchdogStateNormal {
		t.Errorf("State = %v, want WatchdogStateNormal", w.State())
	}

	// Bitrate should be unchanged
	if bm.Current() != 100_000_000 {
		t.Errorf("Bitrate = %d, want 100000000", bm.Current())
	}
}

func TestWatchdog_SoftLanding(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-sensitive watchdog test in short mode")
	}
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	config := WatchdogConfig{
		Enabled:     true,
		Timeout:     100 * time.Millisecond,
		SafeBitrate: 10_000_000,
		StopTimeout: 0, // Disable stop for this test
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, cancel)

	// Set last heartbeat to past to trigger immediate timeout
	cs.mu.Lock()
	cs.lastHeartbeat = time.Now().Add(-200 * time.Millisecond)
	cs.mu.Unlock()

	// Start watchdog
	go w.Run(ctx)

	// Wait for watchdog to detect timeout (checks every 500ms)
	time.Sleep(600 * time.Millisecond)

	// Should be in warning state
	if w.State() != WatchdogStateWarning {
		t.Errorf("State = %v, want WatchdogStateWarning", w.State())
	}

	// Bitrate should have dropped to safe rate
	if bm.Current() != 10_000_000 {
		t.Errorf("Bitrate = %d, want 10000000 (safe rate)", bm.Current())
	}
}

func TestWatchdog_Recovery(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-sensitive watchdog test in short mode")
	}
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	// Use a timeout longer than the watchdog check interval (500ms)
	// so that a fresh heartbeat will still be valid when checked
	config := WatchdogConfig{
		Enabled:     true,
		Timeout:     800 * time.Millisecond, // > 500ms check interval
		SafeBitrate: 10_000_000,
		StopTimeout: 0, // Disable stop
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, cancel)

	// Set last heartbeat to past to trigger immediate timeout
	cs.mu.Lock()
	cs.lastHeartbeat = time.Now().Add(-1 * time.Second)
	cs.mu.Unlock()

	// Start watchdog
	go w.Run(ctx)

	// Wait for watchdog to detect timeout
	time.Sleep(600 * time.Millisecond)

	if w.State() != WatchdogStateWarning {
		t.Fatalf("Expected warning state, got %v", w.State())
	}

	// Now send a heartbeat to recover - this resets the timer
	cs.recordHeartbeat()

	// Wait for next watchdog check cycle
	// The watchdog checks every 500ms and will see elapsed < Timeout (800ms)
	time.Sleep(600 * time.Millisecond)

	// Should recover to normal state since elapsed (~600ms) is now < Timeout (800ms)
	state := w.State()
	if state != WatchdogStateNormal {
		elapsed := cs.TimeSinceHeartbeat()
		t.Errorf("State = %v, want WatchdogStateNormal after recovery (elapsed since heartbeat: %v, timeout: %v)",
			state, elapsed, config.Timeout)
	}

	// Note: Bitrate is NOT auto-restored - Orchestrator must set it
	// This is intentional per the design doc
}

func TestWatchdog_CriticalStop(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping time-sensitive watchdog test in short mode")
	}
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	config := WatchdogConfig{
		Enabled:     true,
		Timeout:     100 * time.Millisecond,
		SafeBitrate: 10_000_000,
		StopTimeout: 600 * time.Millisecond, // Must be > Timeout + watchdog check interval
	}

	stopped := make(chan struct{})
	stopFunc := func() {
		close(stopped)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, stopFunc)

	// Set last heartbeat to past to trigger immediate timeout
	cs.mu.Lock()
	cs.lastHeartbeat = time.Now().Add(-700 * time.Millisecond)
	cs.mu.Unlock()

	// Start watchdog
	go w.Run(ctx)

	// Wait for critical stop
	select {
	case <-stopped:
		// Expected - watchdog triggered stop
	case <-time.After(2 * time.Second):
		t.Errorf("Watchdog should have triggered stop, state=%v", w.State())
	}
}

func TestWatchdog_Disabled(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	config := WatchdogConfig{
		Enabled:     false, // Disabled
		Timeout:     50 * time.Millisecond,
		SafeBitrate: 10_000_000,
		StopTimeout: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, cancel)

	// Start watchdog (should return immediately)
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Should exit immediately since disabled
	select {
	case <-done:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Disabled watchdog should return immediately")
	}

	// Bitrate should be unchanged
	if bm.Current() != 100_000_000 {
		t.Errorf("Bitrate = %d, want 100000000", bm.Current())
	}
}

func TestWatchdog_StateString(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	config := DefaultWatchdogConfig()

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, cancel)

	// Test state strings
	if w.StateString() != WatchdogNormal {
		t.Errorf("StateString() = %q, want %q", w.StateString(), WatchdogNormal)
	}

	// Force warning state
	w.state.Store(int32(WatchdogStateWarning))
	if w.StateString() != WatchdogWarning {
		t.Errorf("StateString() = %q, want %q", w.StateString(), WatchdogWarning)
	}

	// Force critical state
	w.state.Store(int32(WatchdogStateCritical))
	if w.StateString() != WatchdogCritical {
		t.Errorf("StateString() = %q, want %q", w.StateString(), WatchdogCritical)
	}
}

func TestWatchdog_IsHealthy(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "test.sock")
	bm := NewBitrateManager(100_000_000, 1_000_000, 1_000_000_000)
	cs := NewControlServer(socketPath, bm, nil)

	config := DefaultWatchdogConfig()

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatchdog(config, cs, bm, cancel)

	// Normal state is healthy
	if !w.IsHealthy() {
		t.Error("IsHealthy() = false, want true for normal state")
	}

	// Warning state is not healthy
	w.state.Store(int32(WatchdogStateWarning))
	if w.IsHealthy() {
		t.Error("IsHealthy() = true, want false for warning state")
	}

	// Critical state is not healthy
	w.state.Store(int32(WatchdogStateCritical))
	if w.IsHealthy() {
		t.Error("IsHealthy() = true, want false for critical state")
	}
}
