//go:build debug

package send

// test_helpers_debug.go - Test helpers for debug builds
//
// These helpers wrap EventLoop function calls with proper context setup.
// In debug builds, AssertEventLoopContext() will panic if called without context.
//
// Usage in tests:
//   runInEventLoopContext(s, func() {
//       s.deliverReadyPacketsEventLoop(now)
//   })

// runInEventLoopContext wraps a function call with EventLoop context.
// Use this in tests that call EventLoop-only functions like:
// - deliverReadyPacketsEventLoop()
// - drainRingToBtreeEventLoop()
// - dropOldPacketsEventLoop()
// - processControlPacketsDelta()
func runInEventLoopContext(s *sender, fn func()) {
	s.EnterEventLoop()
	defer s.ExitEventLoop()
	fn()
}

// runInTickContext wraps a function call with Tick context.
// Use this in tests that call Tick-only (locking) functions.
func runInTickContext(s *sender, fn func()) {
	s.EnterTick()
	defer s.ExitTick()
	fn()
}


