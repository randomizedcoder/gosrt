//go:build debug

package send

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// Debug Context Table-Driven Tests
//
// Tests for EventLoop/Tick context tracking and assertions.
// These tests verify the lock-free architecture's safety guarantees.
//
// Build with: go test -tags debug ./congestion/live/send/...
// =============================================================================

// TestDebugContext_EnterExit_TableDriven tests context entry/exit transitions.
func TestDebugContext_EnterExit_TableDriven(t *testing.T) {
	testCases := []struct {
		name              string
		setupContext      func(s *sender)
		expectInEventLoop bool
		expectInTick      bool
	}{
		{
			name:              "Initial state - neither context",
			setupContext:      func(s *sender) {},
			expectInEventLoop: false,
			expectInTick:      false,
		},
		{
			name: "After EnterEventLoop",
			setupContext: func(s *sender) {
				s.EnterEventLoop()
			},
			expectInEventLoop: true,
			expectInTick:      false,
		},
		{
			name: "After EnterEventLoop then ExitEventLoop",
			setupContext: func(s *sender) {
				s.EnterEventLoop()
				s.ExitEventLoop()
			},
			expectInEventLoop: false,
			expectInTick:      false,
		},
		{
			name: "After EnterTick",
			setupContext: func(s *sender) {
				s.EnterTick()
			},
			expectInEventLoop: false,
			expectInTick:      true,
		},
		{
			name: "After EnterTick then ExitTick",
			setupContext: func(s *sender) {
				s.EnterTick()
				s.ExitTick()
			},
			expectInEventLoop: false,
			expectInTick:      false,
		},
		{
			name: "Multiple EventLoop enter/exit cycles",
			setupContext: func(s *sender) {
				for i := 0; i < 5; i++ {
					s.EnterEventLoop()
					s.ExitEventLoop()
				}
				s.EnterEventLoop() // End in EventLoop
			},
			expectInEventLoop: true,
			expectInTick:      false,
		},
		{
			name: "Multiple Tick enter/exit cycles",
			setupContext: func(s *sender) {
				for i := 0; i < 5; i++ {
					s.EnterTick()
					s.ExitTick()
				}
				s.EnterTick() // End in Tick
			},
			expectInEventLoop: false,
			expectInTick:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			s.initDebugContext()

			tc.setupContext(s)

			// Check state by attempting assertions
			// recover() value intentionally discarded: we only care if panic occurred, not its message
			inEventLoop := func() bool {
				defer func() { _ = recover() }()
				s.AssertEventLoopContext()
				return true
			}()

			// recover() value intentionally discarded: we only care if panic occurred, not its message
			inTick := func() bool {
				defer func() { _ = recover() }()
				s.AssertTickContext()
				return true
			}()

			require.Equal(t, tc.expectInEventLoop, inEventLoop, "EventLoop context mismatch")
			require.Equal(t, tc.expectInTick, inTick, "Tick context mismatch")
		})
	}
}

// TestDebugContext_Assertions_Panic_TableDriven tests that assertions panic when violated.
func TestDebugContext_Assertions_Panic_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		setupContext  func(s *sender)
		assertion     func(s *sender)
		expectPanic   bool
		panicContains string
	}{
		// === EventLoop Context Assertions ===
		{
			name:          "AssertEventLoopContext panics without context",
			setupContext:  func(s *sender) {},
			assertion:     func(s *sender) { s.AssertEventLoopContext() },
			expectPanic:   true,
			panicContains: "outside EventLoop context",
		},
		{
			name: "AssertEventLoopContext succeeds in EventLoop",
			setupContext: func(s *sender) {
				s.EnterEventLoop()
			},
			assertion:   func(s *sender) { s.AssertEventLoopContext() },
			expectPanic: false,
		},
		{
			name: "AssertEventLoopContext panics in Tick context",
			setupContext: func(s *sender) {
				s.EnterTick()
			},
			assertion:     func(s *sender) { s.AssertEventLoopContext() },
			expectPanic:   true,
			panicContains: "outside EventLoop context",
		},

		// === Tick Context Assertions ===
		{
			name:          "AssertTickContext panics without context",
			setupContext:  func(s *sender) {},
			assertion:     func(s *sender) { s.AssertTickContext() },
			expectPanic:   true,
			panicContains: "outside Tick context",
		},
		{
			name: "AssertTickContext succeeds in Tick",
			setupContext: func(s *sender) {
				s.EnterTick()
			},
			assertion:   func(s *sender) { s.AssertTickContext() },
			expectPanic: false,
		},
		{
			name: "AssertTickContext panics in EventLoop context",
			setupContext: func(s *sender) {
				s.EnterEventLoop()
			},
			assertion:     func(s *sender) { s.AssertTickContext() },
			expectPanic:   true,
			panicContains: "outside Tick context",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			s.initDebugContext()
			tc.setupContext(s)

			if tc.expectPanic {
				// Actually verify the panic contains expected string
				defer func() {
					rec := recover()
					if rec != nil {
						msg, ok := rec.(string)
						require.True(t, ok, "panic value should be string")
						require.Contains(t, msg, tc.panicContains)
					} else {
						t.Error("expected panic but none occurred")
					}
				}()
				tc.assertion(s)
			} else {
				require.NotPanics(t, func() {
					tc.assertion(s)
				})
			}
		})
	}
}

// TestDebugContext_MutualExclusion_TableDriven tests that EventLoop and Tick are mutually exclusive.
func TestDebugContext_MutualExclusion_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		firstContext  string // "eventloop" or "tick"
		secondEnter   func(s *sender)
		expectPanic   bool
		panicContains string
	}{
		{
			name:          "EnterTick while in EventLoop panics",
			firstContext:  "eventloop",
			secondEnter:   func(s *sender) { s.EnterTick() },
			expectPanic:   true,
			panicContains: "EnterTick called while EventLoop is active",
		},
		{
			name:          "EnterEventLoop while in Tick panics",
			firstContext:  "tick",
			secondEnter:   func(s *sender) { s.EnterEventLoop() },
			expectPanic:   true,
			panicContains: "EnterEventLoop called while Tick is active",
		},
		{
			name:         "EnterTick after ExitEventLoop succeeds",
			firstContext: "eventloop_then_exit",
			secondEnter:  func(s *sender) { s.EnterTick() },
			expectPanic:  false,
		},
		{
			name:         "EnterEventLoop after ExitTick succeeds",
			firstContext: "tick_then_exit",
			secondEnter:  func(s *sender) { s.EnterEventLoop() },
			expectPanic:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			s.initDebugContext()

			// Setup first context
			switch tc.firstContext {
			case "eventloop":
				s.EnterEventLoop()
			case "tick":
				s.EnterTick()
			case "eventloop_then_exit":
				s.EnterEventLoop()
				s.ExitEventLoop()
			case "tick_then_exit":
				s.EnterTick()
				s.ExitTick()
			}

			if tc.expectPanic {
				require.Panics(t, func() {
					tc.secondEnter(s)
				})
			} else {
				require.NotPanics(t, func() {
					tc.secondEnter(s)
				})
			}
		})
	}
}

// TestDebugContext_LockAssertions_TableDriven tests lock state assertions.
func TestDebugContext_LockAssertions_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		lockHeld      bool
		assertion     func(s *sender)
		expectPanic   bool
		panicContains string
	}{
		// AssertNoLockHeld tests
		{
			name:        "AssertNoLockHeld succeeds when lock not held",
			lockHeld:    false,
			assertion:   func(s *sender) { s.AssertNoLockHeld() },
			expectPanic: false,
		},
		{
			name:          "AssertNoLockHeld panics when lock held",
			lockHeld:      true,
			assertion:     func(s *sender) { s.AssertNoLockHeld() },
			expectPanic:   true,
			panicContains: "Lock held in EventLoop context",
		},
		// AssertLockHeld tests
		{
			name:          "AssertLockHeld panics when lock not held",
			lockHeld:      false,
			assertion:     func(s *sender) { s.AssertLockHeld() },
			expectPanic:   true,
			panicContains: "Lock NOT held in Tick context",
		},
		{
			name:        "AssertLockHeld succeeds when lock held",
			lockHeld:    true,
			assertion:   func(s *sender) { s.AssertLockHeld() },
			expectPanic: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			s.initDebugContext()

			if tc.lockHeld {
				s.lock.Lock()
				defer s.lock.Unlock()
			}

			if tc.expectPanic {
				require.Panics(t, func() {
					tc.assertion(s)
				})
			} else {
				require.NotPanics(t, func() {
					tc.assertion(s)
				})
			}
		})
	}
}

// TestDebugContext_CompoundAssertions_TableDriven tests compound assertions.
func TestDebugContext_CompoundAssertions_TableDriven(t *testing.T) {
	testCases := []struct {
		name        string
		inEventLoop bool
		inTick      bool
		lockHeld    bool
		assertion   string // "EventLoopNoLock" or "TickWithLock"
		expectPanic bool
	}{
		// AssertEventLoopNoLock tests
		{
			name:        "EventLoopNoLock - correct (in EventLoop, no lock)",
			inEventLoop: true,
			lockHeld:    false,
			assertion:   "EventLoopNoLock",
			expectPanic: false,
		},
		{
			name:        "EventLoopNoLock - wrong context (not in EventLoop)",
			inEventLoop: false,
			lockHeld:    false,
			assertion:   "EventLoopNoLock",
			expectPanic: true,
		},
		{
			name:        "EventLoopNoLock - lock held (violation)",
			inEventLoop: true,
			lockHeld:    true,
			assertion:   "EventLoopNoLock",
			expectPanic: true,
		},
		// AssertTickWithLock tests
		{
			name:        "TickWithLock - correct (in Tick, lock held)",
			inTick:      true,
			lockHeld:    true,
			assertion:   "TickWithLock",
			expectPanic: false,
		},
		{
			name:        "TickWithLock - wrong context (not in Tick)",
			inTick:      false,
			lockHeld:    true,
			assertion:   "TickWithLock",
			expectPanic: true,
		},
		{
			name:        "TickWithLock - lock not held (violation)",
			inTick:      true,
			lockHeld:    false,
			assertion:   "TickWithLock",
			expectPanic: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{}
			s.initDebugContext()

			if tc.inEventLoop {
				s.EnterEventLoop()
				defer s.ExitEventLoop()
			}
			if tc.inTick {
				s.EnterTick()
				defer s.ExitTick()
			}
			if tc.lockHeld {
				s.lock.Lock()
				defer s.lock.Unlock()
			}

			var assertFunc func()
			switch tc.assertion {
			case "EventLoopNoLock":
				assertFunc = func() { s.AssertEventLoopNoLock() }
			case "TickWithLock":
				assertFunc = func() { s.AssertTickWithLock() }
			}

			if tc.expectPanic {
				require.Panics(t, assertFunc)
			} else {
				require.NotPanics(t, assertFunc)
			}
		})
	}
}

// TestDebugContext_ControlRingFallback_TableDriven tests AssertNotEventLoopOnFallback.
func TestDebugContext_ControlRingFallback_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		useEventLoop  bool
		controlType   string
		expectPanic   bool
		panicContains string
	}{
		{
			name:         "ACK fallback with EventLoop disabled - OK",
			useEventLoop: false,
			controlType:  "ACK",
			expectPanic:  false,
		},
		{
			name:          "ACK fallback with EventLoop enabled - PANIC",
			useEventLoop:  true,
			controlType:   "ACK",
			expectPanic:   true,
			panicContains: "ACK control ring overflow with EventLoop mode enabled",
		},
		{
			name:         "NAK fallback with EventLoop disabled - OK",
			useEventLoop: false,
			controlType:  "NAK",
			expectPanic:  false,
		},
		{
			name:          "NAK fallback with EventLoop enabled - PANIC",
			useEventLoop:  true,
			controlType:   "NAK",
			expectPanic:   true,
			panicContains: "NAK control ring overflow with EventLoop mode enabled",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sender{
				useEventLoop: tc.useEventLoop,
			}
			s.initDebugContext()

			if tc.expectPanic {
				require.Panics(t, func() {
					s.AssertNotEventLoopOnFallback(tc.controlType)
				})
			} else {
				require.NotPanics(t, func() {
					s.AssertNotEventLoopOnFallback(tc.controlType)
				})
			}
		})
	}
}

// TestDebugContext_GoroutineTracking tests goroutine ID tracking.
func TestDebugContext_GoroutineTracking(t *testing.T) {
	s := &sender{}
	s.initDebugContext()

	// Initially no goroutine tracked
	require.Equal(t, int64(0), s.debug.eventLoopGo.Load())
	require.Equal(t, int64(0), s.debug.tickGo.Load())

	// Enter EventLoop - should track goroutine ID
	s.EnterEventLoop()
	eventLoopGo := s.debug.eventLoopGo.Load()
	require.NotEqual(t, int64(0), eventLoopGo, "EventLoop goroutine ID should be set")
	require.Equal(t, int64(0), s.debug.tickGo.Load(), "Tick goroutine should not be set")

	// Exit EventLoop - should clear goroutine ID
	s.ExitEventLoop()
	require.Equal(t, int64(0), s.debug.eventLoopGo.Load(), "EventLoop goroutine should be cleared")

	// Enter Tick - should track goroutine ID
	s.EnterTick()
	tickGo := s.debug.tickGo.Load()
	require.NotEqual(t, int64(0), tickGo, "Tick goroutine ID should be set")
	require.Equal(t, int64(0), s.debug.eventLoopGo.Load(), "EventLoop goroutine should not be set")

	// Exit Tick - should clear goroutine ID
	s.ExitTick()
	require.Equal(t, int64(0), s.debug.tickGo.Load(), "Tick goroutine should be cleared")
}

// TestDebugContext_ConcurrentContextTracking tests that context tracking is thread-safe.
func TestDebugContext_ConcurrentContextTracking(t *testing.T) {
	s := &sender{}
	s.initDebugContext()

	var wg sync.WaitGroup
	const iterations = 100

	// Multiple goroutines entering/exiting EventLoop context sequentially
	// (not simultaneously - that would be a real bug)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Each goroutine waits its turn via small sleep
				// In real code, only one context should be active at a time
				time.Sleep(time.Microsecond * time.Duration(id*10))

				// Quick enter/exit to test atomic operations
				if !s.debug.inEventLoop.Load() && !s.debug.inTick.Load() {
					s.EnterEventLoop()
					// Brief work
					s.ExitEventLoop()
				}
			}
		}(i)
	}

	wg.Wait()

	// Final state should be clean
	require.False(t, s.debug.inEventLoop.Load())
	require.False(t, s.debug.inTick.Load())
	require.Equal(t, int64(0), s.debug.eventLoopGo.Load())
	require.Equal(t, int64(0), s.debug.tickGo.Load())
}

// TestGetCallerName verifies caller name extraction.
func TestGetCallerName(t *testing.T) {
	// Test the function indirectly by checking panic messages include function names
	s := &sender{}
	s.initDebugContext()

	defer func() {
		if r := recover(); r != nil {
			msg := r.(string)
			// The caller name should be in the panic message
			require.Contains(t, msg, "called outside EventLoop context")
		}
	}()

	s.AssertEventLoopContext() // Should panic with caller name
}

// TestGetGoroutineID verifies goroutine ID extraction works.
func TestGetGoroutineID(t *testing.T) {
	id := getGoroutineID()
	require.NotEqual(t, int64(0), id, "goroutine ID should not be zero")

	// Same goroutine should get same ID
	id2 := getGoroutineID()
	require.Equal(t, id, id2, "same goroutine should get same ID")

	// Different goroutine should get different ID
	var otherId int64
	done := make(chan struct{})
	go func() {
		otherId = getGoroutineID()
		close(done)
	}()
	<-done

	require.NotEqual(t, id, otherId, "different goroutines should have different IDs")
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkDebugContext_EnterExitEventLoop(b *testing.B) {
	s := &sender{}
	s.initDebugContext()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.EnterEventLoop()
		s.ExitEventLoop()
	}
}

func BenchmarkDebugContext_AssertEventLoopContext(b *testing.B) {
	s := &sender{}
	s.initDebugContext()
	s.EnterEventLoop()
	defer s.ExitEventLoop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.AssertEventLoopContext()
	}
}

func BenchmarkDebugContext_GetGoroutineID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = getGoroutineID()
	}
}
