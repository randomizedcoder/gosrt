//go:build !debug

package srt

// connDebugContext is a placeholder for release builds.
// All debug methods are no-ops.
type connDebugContext struct{}

// newConnDebugContext returns nil in release builds.
// This ensures zero overhead for debug context in production.
func newConnDebugContext() *connDebugContext {
	return nil
}

// EnterEventLoop is a no-op in release builds.
func (c *srtConn) EnterEventLoop() {}

// ExitEventLoop is a no-op in release builds.
func (c *srtConn) ExitEventLoop() {}

// EnterTick is a no-op in release builds.
func (c *srtConn) EnterTick() {}

// ExitTick is a no-op in release builds.
func (c *srtConn) ExitTick() {}

// AssertEventLoopContext is a no-op in release builds.
func (c *srtConn) AssertEventLoopContext() {}

// AssertTickContext is a no-op in release builds.
func (c *srtConn) AssertTickContext() {}

// InEventLoop always returns false in release builds.
func (c *srtConn) InEventLoop() bool { return false }

// InTick always returns false in release builds.
func (c *srtConn) InTick() bool { return false }


