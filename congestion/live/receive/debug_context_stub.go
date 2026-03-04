//go:build !debug

// Package receive implements the receiver-side congestion control for SRT live mode.
//
// Debug Stub (Release Build)
//
// This file provides no-op implementations of debug assertions for release builds.
// All functions compile to nothing, incurring zero runtime overhead.
//
// For debug builds with actual assertions, build with: go build -tags debug
//
// Reference: lockless_sender_implementation_plan.md Step 7.5.2
package receive

// debugContext is empty in release builds (zero size).
type debugContext struct{}

// initDebugContext is a no-op in release builds.
func (r *receiver) initDebugContext() {}

// EnterEventLoop is a no-op in release builds.
func (r *receiver) EnterEventLoop() {}

// ExitEventLoop is a no-op in release builds.
func (r *receiver) ExitEventLoop() {}

// EnterTick is a no-op in release builds.
func (r *receiver) EnterTick() {}

// ExitTick is a no-op in release builds.
func (r *receiver) ExitTick() {}

// AssertEventLoopContext is a no-op in release builds.
func (r *receiver) AssertEventLoopContext() {}

// AssertTickContext is a no-op in release builds.
func (r *receiver) AssertTickContext() {}

// AssertNoLockHeld is a no-op in release builds.
func (r *receiver) AssertNoLockHeld() {}

// AssertLockHeld is a no-op in release builds.
func (r *receiver) AssertLockHeld() {}

// AssertEventLoopNoLock is a no-op in release builds.
func (r *receiver) AssertEventLoopNoLock() {}

// AssertTickWithLock is a no-op in release builds.
func (r *receiver) AssertTickWithLock() {}
