//go:build debug

package srt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConnectionDebugContext_EventLoop_MissingContext verifies that
// calling handleACKACK without setting EventLoop context panics.
// This is TDD: the test should FAIL initially, then pass after we
// properly set context in the EventLoop path.
func TestConnectionDebugContext_EventLoop_MissingContext(t *testing.T) {
	c := &srtConn{
		debugCtx: newConnDebugContext(),
	}

	// This should panic because we haven't called EnterEventLoop()
	require.Panics(t, func() {
		c.handleACKACK(1, time.Now())
	}, "handleACKACK should panic when called outside EventLoop context")
}

// TestConnectionDebugContext_Tick_MissingContext verifies that
// calling handleACKACKLocked without setting Tick context panics.
// This is TDD: the test should FAIL initially, then pass after we
// properly set context in the Tick path.
func TestConnectionDebugContext_Tick_MissingContext(t *testing.T) {
	c := &srtConn{
		debugCtx: newConnDebugContext(),
	}

	// This should panic because we haven't called EnterTick()
	require.Panics(t, func() {
		c.handleACKACKLocked(nil) // Will panic at AssertTickContext before nil check
	}, "handleACKACKLocked should panic when called outside Tick context")
}

// TestConnectionDebugContext_EventLoop_WithContext verifies that
// calling handleACKACK WITH proper EventLoop context does NOT panic at the assert.
func TestConnectionDebugContext_EventLoop_WithContext(t *testing.T) {
	c := &srtConn{
		debugCtx:   newConnDebugContext(),
		ackNumbers: newAckEntryBtree(4), // Need this to avoid nil panic
	}

	// Set EventLoop context
	c.EnterEventLoop()
	defer c.ExitEventLoop()

	// Verify the assert passes (InEventLoop returns true)
	require.True(t, c.InEventLoop(), "should be in EventLoop context")
	require.NotPanics(t, func() {
		c.AssertEventLoopContext()
	}, "AssertEventLoopContext should NOT panic when in EventLoop")

	// The full handleACKACK would need more setup (logger, metrics, etc.)
	// but the assert itself passes
}

// TestConnectionDebugContext_Tick_WithContext verifies that
// calling handleACKACKLocked WITH proper Tick context does NOT panic at the assert.
func TestConnectionDebugContext_Tick_WithContext(t *testing.T) {
	c := &srtConn{
		debugCtx:   newConnDebugContext(),
		ackNumbers: newAckEntryBtree(4), // Need this to avoid nil panic
	}

	// Set Tick context
	c.EnterTick()
	defer c.ExitTick()

	// Verify the assert passes (InTick returns true)
	require.True(t, c.InTick(), "should be in Tick context")
	require.NotPanics(t, func() {
		c.AssertTickContext()
	}, "AssertTickContext should NOT panic when in Tick")

	// The full handleACKACKLocked would need more setup (logger, metrics, etc.)
	// but the assert itself passes
}

// TestConnectionDebugContext_KeepAlive_EventLoop_MissingContext verifies that
// handleKeepAliveEventLoop panics without context.
func TestConnectionDebugContext_KeepAlive_EventLoop_MissingContext(t *testing.T) {
	c := &srtConn{
		debugCtx: newConnDebugContext(),
	}

	require.Panics(t, func() {
		c.handleKeepAliveEventLoop()
	}, "handleKeepAliveEventLoop should panic when called outside EventLoop context")
}

// TestConnectionDebugContext_KeepAlive_Tick_MissingContext verifies that
// handleKeepAlive panics without context.
func TestConnectionDebugContext_KeepAlive_Tick_MissingContext(t *testing.T) {
	c := &srtConn{
		debugCtx: newConnDebugContext(),
	}

	require.Panics(t, func() {
		c.handleKeepAlive(nil) // Will panic at AssertTickContext before nil check
	}, "handleKeepAlive should panic when called outside Tick context")
}

// TestConnectionDebugContext_ContextExclusion verifies that
// EventLoop and Tick contexts are mutually exclusive assertions.
func TestConnectionDebugContext_ContextExclusion(t *testing.T) {
	c := &srtConn{
		debugCtx:   newConnDebugContext(),
		ackNumbers: newAckEntryBtree(4),
	}

	// In EventLoop context, Tick functions should panic
	c.EnterEventLoop()
	require.Panics(t, func() {
		c.AssertTickContext()
	}, "AssertTickContext should panic when in EventLoop")
	c.ExitEventLoop()

	// In Tick context, EventLoop functions should panic
	c.EnterTick()
	require.Panics(t, func() {
		c.AssertEventLoopContext()
	}, "AssertEventLoopContext should panic when in Tick")
	c.ExitTick()
}
