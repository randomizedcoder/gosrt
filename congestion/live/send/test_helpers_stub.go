//go:build !debug

package send

// test_helpers_stub.go - Test helpers for release builds
//
// In release builds, context tracking is disabled, so we just call the function directly.
// This ensures tests work the same in both debug and release builds.

// runInEventLoopContext is a no-op wrapper in release builds.
// Just calls the function directly (context tracking is disabled).
func runInEventLoopContext(s *sender, fn func()) {
	fn()
}

// runInTickContext is a no-op wrapper in release builds.
// Just calls the function directly (context tracking is disabled).
func runInTickContext(s *sender, fn func()) {
	fn()
}
