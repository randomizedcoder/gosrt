//go:build debug

// Package send implements the sender-side congestion control for SRT live mode.
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
// Build with: go build -tags debug ./congestion/live/send/...
// Test with:  go test -tags debug ./congestion/live/send/... -v
//
// Reference: lockless_sender_implementation_plan.md Step 7.5.2
package send

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
)

// ═══════════════════════════════════════════════════════════════════════════════
// Context Tracking (Per-Instance)
//
// Uses per-sender atomics instead of globals because:
//   1. Multiple connections can exist simultaneously
//   2. Tests may run in parallel
//   3. Each sender has independent lifecycle
// ═══════════════════════════════════════════════════════════════════════════════

// debugContext tracks execution context for a sender instance.
// Embedded in sender struct when debug build is enabled.
type debugContext struct {
	inEventLoop atomic.Bool  // True when inside EventLoop
	inTick      atomic.Bool  // True when inside Tick
	eventLoopGo atomic.Int64 // Goroutine ID running EventLoop (0 = none)
	tickGo      atomic.Int64 // Goroutine ID running Tick (0 = none)
}

// initDebugContext initializes debug tracking for a sender.
// Called from NewSender in debug builds.
func (s *sender) initDebugContext() {
	// Zero values are correct defaults
}

// ═══════════════════════════════════════════════════════════════════════════════
// EventLoop Context Entry/Exit
// ═══════════════════════════════════════════════════════════════════════════════

// EnterEventLoop marks entry into EventLoop context.
// Panics if Tick is active (mutual exclusion violation).
func (s *sender) EnterEventLoop() {
	if s.debug.inTick.Load() {
		tickGo := s.debug.tickGo.Load()
		panic(fmt.Sprintf("LOCKFREE VIOLATION: EnterEventLoop called while Tick is active (tick goroutine: %d)", tickGo))
	}
	s.debug.inEventLoop.Store(true)
	s.debug.eventLoopGo.Store(getGoroutineID())
}

// ExitEventLoop marks exit from EventLoop context.
func (s *sender) ExitEventLoop() {
	s.debug.inEventLoop.Store(false)
	s.debug.eventLoopGo.Store(0)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Tick Context Entry/Exit
// ═══════════════════════════════════════════════════════════════════════════════

// EnterTick marks entry into Tick context.
// Panics if EventLoop is active (mutual exclusion violation).
func (s *sender) EnterTick() {
	if s.debug.inEventLoop.Load() {
		eventLoopGo := s.debug.eventLoopGo.Load()
		panic(fmt.Sprintf("LOCKFREE VIOLATION: EnterTick called while EventLoop is active (eventloop goroutine: %d)", eventLoopGo))
	}
	s.debug.inTick.Store(true)
	s.debug.tickGo.Store(getGoroutineID())
}

// ExitTick marks exit from Tick context.
func (s *sender) ExitTick() {
	s.debug.inTick.Store(false)
	s.debug.tickGo.Store(0)
}

// ═══════════════════════════════════════════════════════════════════════════════
// Context Assertions
// ═══════════════════════════════════════════════════════════════════════════════

// AssertEventLoopContext panics if not currently in EventLoop context.
// Use at the start of functions that must only be called from EventLoop.
func (s *sender) AssertEventLoopContext() {
	if !s.debug.inEventLoop.Load() {
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside EventLoop context", caller))
	}
}

// AssertTickContext panics if not currently in Tick context.
// Use at the start of functions that must only be called from Tick.
func (s *sender) AssertTickContext() {
	if !s.debug.inTick.Load() {
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: %s called outside Tick context", caller))
	}
}

// AssertNoLockHeld verifies the sender mutex is NOT held.
// Use in EventLoop functions to catch accidental lock acquisition.
// This is a debug-only check - the overhead is acceptable.
func (s *sender) AssertNoLockHeld() {
	if s.lock.TryLock() {
		s.lock.Unlock()
		// Good - lock was not held
	} else {
		caller := getCallerName(2)
		panic(fmt.Sprintf("LOCKFREE VIOLATION: Lock held in EventLoop context at %s", caller))
	}
}

// AssertLockHeld verifies the sender mutex IS held.
// Use in Tick functions that require the lock.
// Note: This uses TryLock to check - if lock is held by current goroutine,
// TryLock will return false (Go mutexes are not reentrant).
func (s *sender) AssertLockHeld() {
	if s.lock.TryLock() {
		s.lock.Unlock()
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
func (s *sender) AssertEventLoopNoLock() {
	s.AssertEventLoopContext()
	s.AssertNoLockHeld()
}

// AssertTickWithLock combines context and lock assertions.
// Use at the start of Tick-only functions that require the lock.
func (s *sender) AssertTickWithLock() {
	s.AssertTickContext()
	s.AssertLockHeld()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Control Ring Fallback Assertions
//
// These catch the bug where control ring overflow falls back to the locked path
// while EventLoop is running. EventLoop is lock-free - it doesn't check the lock,
// so the fallback "locked" path will race with EventLoop's btree iteration.
//
// Bug discovered: 2026-01-17 (4 recv rings + 350 Mb/s → btree panic)
// See: sender_control_ring_overflow_test.go, performance_testing_implementation_log.md
// ═══════════════════════════════════════════════════════════════════════════════

// AssertNotEventLoopOnFallback panics if EventLoop MODE is enabled when falling back
// to locked path due to control ring overflow. This is a design violation:
// EventLoop doesn't hold the lock, so the "locked" fallback path will race.
//
// Note: We check useEventLoop (config flag), not inEventLoop (runtime state).
// If useEventLoop is true, the EventLoop goroutine exists and can race with us,
// even if it's currently in a select/sleep at this exact microsecond.
//
// Use in ACK()/NAK() fallback paths when control ring is full.
func (s *sender) AssertNotEventLoopOnFallback(controlType string) {
	if s.useEventLoop {
		eventLoopGo := s.debug.eventLoopGo.Load()
		currentGo := getGoroutineID()
		panic(fmt.Sprintf(
			"LOCKFREE VIOLATION: %s control ring overflow with EventLoop mode enabled!\n"+
				"  useEventLoop: true (config flag)\n"+
				"  EventLoop goroutine: %d (0 = not tracked yet)\n"+
				"  Current goroutine: %d\n"+
				"  This is a RACE CONDITION: EventLoop iterates btree without lock,\n"+
				"  but fallback path will call ackBtree/nakBtree which modifies btree.\n"+
				"  FIX: Drop the %s instead of falling back when useEventLoop=true.",
			controlType, eventLoopGo, currentGo, controlType))
	}
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
	fmt.Sscanf(string(buf[:n]), "goroutine %d ", &id)
	return id
}
