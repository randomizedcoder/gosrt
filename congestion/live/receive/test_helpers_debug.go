//go:build debug

package receive

// test_helpers_debug.go - Test helpers for debug builds
//
// These helpers wrap EventLoop function calls with proper context setup.
// In debug builds, AssertEventLoopContext() will panic if called without context.
//
// Usage in tests:
//   runInEventLoopContext(r, func() {
//       r.periodicACK(now)
//   })

// runInEventLoopContext wraps a function call with EventLoop context.
// Use this in tests that call EventLoop-only functions like:
// - periodicACK()
// - periodicNakBtree()
// - processOnePacket()
// - drainRingByDelta()
func runInEventLoopContext(r *receiver, fn func()) {
	r.EnterEventLoop()
	defer r.ExitEventLoop()
	fn()
}

// runInTickContext wraps a function call with Tick context.
// Use this in tests that call Tick-only (locking) functions.
func runInTickContext(r *receiver, fn func()) {
	r.EnterTick()
	defer r.ExitTick()
	fn()
}


