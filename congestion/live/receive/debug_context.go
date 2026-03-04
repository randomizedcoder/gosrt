//go:build debug

// Package receive implements the receiver-side congestion control for SRT live mode.
//
// # Debug Build Context Assertions
//
// This file provides runtime verification that functions are called from the
// correct context (EventLoop vs Tick). The lockless design requires strict
// separation:
//
//   - EventLoop: Single-threaded btree access, NO LOCKING
//   - Tick: Uses locking functions (xxxLocking variants)
//
// These assertions catch bugs where:
//   - Locking functions are called from EventLoop (defeats lockless design)
//   - Non-locking functions are called from Tick without holding lock
//   - Both EventLoop and Tick are active simultaneously
//
// Build with: go build -tags debug ./congestion/live/receive/...
// Test with:  go test -tags debug ./congestion/live/receive/... -v
//
// Reference: lockless_sender_implementation_plan.md Step 7.5.2
package receive

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Context Tracking (Per-Instance)
//
// Uses per-receiver atomics instead of globals because:
//   1. Multiple connections can exist simultaneously
//   2. Tests may run in parallel
//   3. Each receiver has independent lifecycle
// ═══════════════════════════════════════════════════════════════════════════════

// debugContext tracks execution context for a receiver instance.
// Embedded in receiver struct when debug build is enabled.
type debugContext struct {
	inEventLoop atomic.Bool  // True when inside EventLoop
	inTick      atomic.Bool  // True when inside Tick
	eventLoopGo atomic.Int64 // Goroutine ID running EventLoop (0 = none)
	tickGo      atomic.Int64 // Goroutine ID running Tick (0 = none)
}

// initDebugContext initializes debug tracking for a receiver.
// Called from New() in debug builds.
func (r *receiver) initDebugContext() {
	// Zero values are correct defaults
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Context Entry/Exit
// ═══════════════════════════════════════════════════════════════════════════════

// EnterEventLoop marks entry into EventLoop context.
// Panics if Tick is active (mutual exclusion violation).
func (r *receiver) EnterEventLoop() {
	if r.debugCtx.inTick.Load() {
		tickGo := r.debugCtx.tickGo.Load()
		panic(fmt.Sprintf("LOCKFREE VIOLATION: EnterEventLoop called while Tick is active (tick goroutine: %d)", tickGo))
	}
	r.debugCtx.inEventLoop.Store(true)
	r.debugCtx.eventLoopGo.Store(getGoroutineID())
}

// ExitEventLoop marks exit from EventLoop context.
func (r *receiver) ExitEventLoop() {
	r.debugCtx.inEventLoop.Store(false)
	r.debugCtx.eventLoopGo.Store(0)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Tick Context Entry/Exit
// ═══════════════════════════════════════════════════════════════════════════════

// EnterTick marks entry into Tick context.
// Panics if EventLoop is active (mutual exclusion violation).
func (r *receiver) EnterTick() {
	if r.debugCtx.inEventLoop.Load() {
		eventLoopGo := r.debugCtx.eventLoopGo.Load()
		panic(fmt.Sprintf("LOCKFREE VIOLATION: EnterTick called while EventLoop is active (eventloop goroutine: %d)", eventLoopGo))
	}
	r.debugCtx.inTick.Store(true)
	r.debugCtx.tickGo.Store(getGoroutineID())
}

// ExitTick marks exit from Tick context.
func (r *receiver) ExitTick() {
	r.debugCtx.inTick.Store(false)
	r.debugCtx.tickGo.Store(0)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Context Assertions
// ═══════════════════════════════════════════════════════════════════════════════

// AssertEventLoopContext panics if not currently in EventLoop context.
// Use at the start of functions that must only be called from EventLoop.
func (r *receiver) AssertEventLoopContext() {
	if !r.debugCtx.inEventLoop.Load() {
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside EventLoop context", caller))
	}
}

// AssertTickContext panics if not currently in Tick context.
// Use at the start of functions that must only be called from Tick.
func (r *receiver) AssertTickContext() {
	if !r.debugCtx.inTick.Load() {
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside Tick context", caller))
	}
}

// AssertNoLockHeld verifies the receiver mutex is NOT held.
// Use in EventLoop functions to catch accidental lock acquisition.
// This is a debug-only check - the overhead is acceptable.
func (r *receiver) AssertNoLockHeld() {
	if r.lock.TryLock() {
		r.lock.Unlock()
		// Good - lock was not held
	} else {
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: Lock held in EventLoop context at %s", caller))
	}
}

// AssertLockHeld verifies the receiver mutex IS held.
// Use in Tick functions that require the lock.
// Note: This uses TryLock to check - if lock is held by current goroutine,
// TryLock will return false (Go mutexes are not reentrant).
func (r *receiver) AssertLockHeld() {
	if r.lock.TryLock() {
		r.lock.Unlock()
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: Lock NOT held in Tick context at %s", caller))
	}
	// Good - lock is held (TryLock returned false)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Compound Assertions
// ═══════════════════════════════════════════════════════════════════════════════

// AssertEventLoopNoLock combines context and lock assertions.
// Use at the start of EventLoop-only functions.
func (r *receiver) AssertEventLoopNoLock() {
	r.AssertEventLoopContext()
	r.AssertNoLockHeld()
}

// AssertTickWithLock combines context and lock assertions.
// Use at the start of Tick-only functions that require the lock.
func (r *receiver) AssertTickWithLock() {
	r.AssertTickContext()
	r.AssertLockHeld()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Helper Functions
// ═══════════════════════════════════════════════════════════════════════════════

// getCallerName returns the name of the calling function at the given depth.
func getCallerName(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}
	name := fn.Name()
	// Extract just the function name (after last dot)
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// getGoroutineID returns the current goroutine ID.
// This is extracted from runtime.Stack output - not for production use.
func getGoroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Format: "goroutine 123 [running]:\n..."
	var id int64
	// Parse goroutine ID from stack trace; returns 0 if parsing fails
	if _, err := fmt.Sscanf(string(buf[:n]), "goroutine %d ", &id); err != nil {
		return 0
	}
	return id
}
