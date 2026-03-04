//go:build debug

package srt

import (
	"fmt"
	"runtime"
	"sync/atomic"
)

// connDebugContext tracks execution context for lock-free path verification.
// Only active in debug builds (go build -tags debug).
type connDebugContext struct {
	inEventLoop atomic.Bool
	inTick      atomic.Bool
}

// newConnDebugContext creates a new debug context.
// Returns non-nil in debug builds.
func newConnDebugContext() *connDebugContext {
	return &connDebugContext{}
}

// EnterEventLoop marks entry into EventLoop context.
// Call at the start of EventLoop processing.
func (c *srtConn) EnterEventLoop() {
	if c.debugCtx != nil {
		c.debugCtx.inEventLoop.Store(true)
	}
}

// ExitEventLoop marks exit from EventLoop context.
// Call at the end of EventLoop processing.
func (c *srtConn) ExitEventLoop() {
	if c.debugCtx != nil {
		c.debugCtx.inEventLoop.Store(false)
	}
}

// EnterTick marks entry into Tick context.
// Call at the start of Tick processing.
func (c *srtConn) EnterTick() {
	if c.debugCtx != nil {
		c.debugCtx.inTick.Store(true)
	}
}

// ExitTick marks exit from Tick context.
// Call at the end of Tick processing.
func (c *srtConn) ExitTick() {
	if c.debugCtx != nil {
		c.debugCtx.inTick.Store(false)
	}
}

// AssertEventLoopContext panics if not currently in EventLoop context.
// Use at the start of functions that must only be called from EventLoop.
// This verifies that lock-free functions are only called from lock-free paths.
func (c *srtConn) AssertEventLoopContext() {
	if c.debugCtx == nil {
		return // No debug context (shouldn't happen in debug build)
	}
	if !c.debugCtx.inEventLoop.Load() {
		pc, _, _, _ := runtime.Caller(1)
		fn := runtime.FuncForPC(pc).Name()
		panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside EventLoop context (inEventLoop=%v, inTick=%v)",
			fn, c.debugCtx.inEventLoop.Load(), c.debugCtx.inTick.Load()))
	}
}

// AssertTickContext panics if not currently in Tick context.
// Use at the start of functions that must only be called from Tick.
// This verifies that locking wrapper functions are only called from legacy paths.
func (c *srtConn) AssertTickContext() {
	if c.debugCtx == nil {
		return // No debug context (shouldn't happen in debug build)
	}
	if !c.debugCtx.inTick.Load() {
		pc, _, _, _ := runtime.Caller(1)
		fn := runtime.FuncForPC(pc).Name()
		panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside Tick context (inEventLoop=%v, inTick=%v)",
			fn, c.debugCtx.inEventLoop.Load(), c.debugCtx.inTick.Load()))
	}
}

// InEventLoop returns true if currently in EventLoop context.
// Useful for conditional logic that depends on context.
func (c *srtConn) InEventLoop() bool {
	if c.debugCtx == nil {
		return false
	}
	return c.debugCtx.inEventLoop.Load()
}

// InTick returns true if currently in Tick context.
// Useful for conditional logic that depends on context.
func (c *srtConn) InTick() bool {
	if c.debugCtx == nil {
		return false
	}
	return c.debugCtx.inTick.Load()
}
