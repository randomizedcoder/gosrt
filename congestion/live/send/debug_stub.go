//go:build !debug

// Package send implements the sender-side congestion control for SRT live mode.
//
// Debug Stub (Release Build)
//
// This file provides no-op implementations of debug assertions for release builds.
// All functions compile to nothing, incurring zero runtime overhead.
//
// For debug builds with actual assertions, build with: go build -tags debug
//
// Reference: lockless_sender_implementation_plan.md Step 7.5.2
package send

// debugContext is empty in release builds (zero size).
// Used in debug builds for lock-free context assertions.
type debugContext struct{}

// initDebugContext is a no-op in release builds.
func (s *sender) initDebugContext() {
	_ = s.debug // field used in debug builds
}

// EnterEventLoop is a no-op in release builds.
func (s *sender) EnterEventLoop() {}

// ExitEventLoop is a no-op in release builds.
func (s *sender) ExitEventLoop() {}

// EnterTick is a no-op in release builds.
func (s *sender) EnterTick() {}

// ExitTick is a no-op in release builds.
func (s *sender) ExitTick() {}

// AssertEventLoopContext is a no-op in release builds.
func (s *sender) AssertEventLoopContext() {}

// AssertTickContext is a no-op in release builds.
func (s *sender) AssertTickContext() {}

// AssertNoLockHeld is a no-op in release builds.
func (s *sender) AssertNoLockHeld() {}

// AssertLockHeld is a no-op in release builds.
func (s *sender) AssertLockHeld() {}

// AssertEventLoopNoLock is a no-op in release builds.
func (s *sender) AssertEventLoopNoLock() {}

// AssertTickWithLock is a no-op in release builds.
func (s *sender) AssertTickWithLock() {}

// AssertNotEventLoopOnFallback is a no-op in release builds.
// In debug builds, this panics if EventLoop is active during control ring fallback.
func (s *sender) AssertNotEventLoopOnFallback(controlType string) {}
