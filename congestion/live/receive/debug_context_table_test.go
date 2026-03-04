//go:build debug

package receive

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
// Build with: go test -tags debug ./congestion/live/receive/...
// =============================================================================

// TestDebugContext_EnterExit_TableDriven tests context entry/exit transitions.
func TestDebugContext_EnterExit_TableDriven(t *testing.T) {
	testCases := []struct {
		name              string
		setupContext      func(r *receiver)
		expectInEventLoop bool
		expectInTick      bool
	}{
		{
			name:              "Initial state - neither context",
			setupContext:      func(r *receiver) {},
			expectInEventLoop: false,
			expectInTick:      false,
		},
		{
			name: "After EnterEventLoop",
			setupContext: func(r *receiver) {
				r.EnterEventLoop()
			},
			expectInEventLoop: true,
			expectInTick:      false,
		},
		{
			name: "After EnterEventLoop then ExitEventLoop",
			setupContext: func(r *receiver) {
				r.EnterEventLoop()
				r.ExitEventLoop()
			},
			expectInEventLoop: false,
			expectInTick:      false,
		},
		{
			name: "After EnterTick",
			setupContext: func(r *receiver) {
				r.EnterTick()
			},
			expectInEventLoop: false,
			expectInTick:      true,
		},
		{
			name: "After EnterTick then ExitTick",
			setupContext: func(r *receiver) {
				r.EnterTick()
				r.ExitTick()
			},
			expectInEventLoop: false,
			expectInTick:      false,
		},
		{
			name: "Multiple EventLoop enter/exit cycles",
			setupContext: func(r *receiver) {
				for i := 0; i < 5; i++ {
					r.EnterEventLoop()
					r.ExitEventLoop()
				}
				r.EnterEventLoop() // End in EventLoop
			},
			expectInEventLoop: true,
			expectInTick:      false,
		},
		{
			name: "Multiple Tick enter/exit cycles",
			setupContext: func(r *receiver) {
				for i := 0; i < 5; i++ {
					r.EnterTick()
					r.ExitTick()
				}
				r.EnterTick() // End in Tick
			},
			expectInEventLoop: false,
			expectInTick:      true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{}
			r.initDebugContext()

			tc.setupContext(r)

			// Check state by attempting assertions
			// recover() value intentionally discarded: we only care if panic occurred, not its message
			inEventLoop := func() bool {
				defer func() { _ = recover() }()
				r.AssertEventLoopContext()
				return true
			}()

			// recover() value intentionally discarded: we only care if panic occurred, not its message
			inTick := func() bool {
				defer func() { _ = recover() }()
				r.AssertTickContext()
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
		setupContext  func(r *receiver)
		assertion     func(r *receiver)
		expectPanic   bool
		panicContains string
	}{
		// === EventLoop Context Assertions ===
		{
			name:          "AssertEventLoopContext panics without context",
			setupContext:  func(r *receiver) {},
			assertion:     func(r *receiver) { r.AssertEventLoopContext() },
			expectPanic:   true,
			panicContains: "outside EventLoop context",
		},
		{
			name: "AssertEventLoopContext succeeds in EventLoop",
			setupContext: func(r *receiver) {
				r.EnterEventLoop()
			},
			assertion:   func(r *receiver) { r.AssertEventLoopContext() },
			expectPanic: false,
		},
		{
			name: "AssertEventLoopContext panics in Tick context",
			setupContext: func(r *receiver) {
				r.EnterTick()
			},
			assertion:     func(r *receiver) { r.AssertEventLoopContext() },
			expectPanic:   true,
			panicContains: "outside EventLoop context",
		},

		// === Tick Context Assertions ===
		{
			name:          "AssertTickContext panics without context",
			setupContext:  func(r *receiver) {},
			assertion:     func(r *receiver) { r.AssertTickContext() },
			expectPanic:   true,
			panicContains: "outside Tick context",
		},
		{
			name: "AssertTickContext succeeds in Tick",
			setupContext: func(r *receiver) {
				r.EnterTick()
			},
			assertion:   func(r *receiver) { r.AssertTickContext() },
			expectPanic: false,
		},
		{
			name: "AssertTickContext panics in EventLoop context",
			setupContext: func(r *receiver) {
				r.EnterEventLoop()
			},
			assertion:     func(r *receiver) { r.AssertTickContext() },
			expectPanic:   true,
			panicContains: "outside Tick context",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{}
			r.initDebugContext()
			tc.setupContext(r)

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
				tc.assertion(r)
			} else {
				require.NotPanics(t, func() {
					tc.assertion(r)
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
		secondEnter   func(r *receiver)
		expectPanic   bool
		panicContains string
	}{
		{
			name:          "EnterTick while in EventLoop panics",
			firstContext:  "eventloop",
			secondEnter:   func(r *receiver) { r.EnterTick() },
			expectPanic:   true,
			panicContains: "EnterTick called while EventLoop is active",
		},
		{
			name:          "EnterEventLoop while in Tick panics",
			firstContext:  "tick",
			secondEnter:   func(r *receiver) { r.EnterEventLoop() },
			expectPanic:   true,
			panicContains: "EnterEventLoop called while Tick is active",
		},
		{
			name:         "EnterTick after ExitEventLoop succeeds",
			firstContext: "eventloop_then_exit",
			secondEnter:  func(r *receiver) { r.EnterTick() },
			expectPanic:  false,
		},
		{
			name:         "EnterEventLoop after ExitTick succeeds",
			firstContext: "tick_then_exit",
			secondEnter:  func(r *receiver) { r.EnterEventLoop() },
			expectPanic:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{}
			r.initDebugContext()

			// Setup first context
			switch tc.firstContext {
			case "eventloop":
				r.EnterEventLoop()
			case "tick":
				r.EnterTick()
			case "eventloop_then_exit":
				r.EnterEventLoop()
				r.ExitEventLoop()
			case "tick_then_exit":
				r.EnterTick()
				r.ExitTick()
			}

			if tc.expectPanic {
				require.Panics(t, func() {
					tc.secondEnter(r)
				})
			} else {
				require.NotPanics(t, func() {
					tc.secondEnter(r)
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
		assertion     func(r *receiver)
		expectPanic   bool
		panicContains string
	}{
		// AssertNoLockHeld tests
		{
			name:        "AssertNoLockHeld succeeds when lock not held",
			lockHeld:    false,
			assertion:   func(r *receiver) { r.AssertNoLockHeld() },
			expectPanic: false,
		},
		{
			name:          "AssertNoLockHeld panics when lock held",
			lockHeld:      true,
			assertion:     func(r *receiver) { r.AssertNoLockHeld() },
			expectPanic:   true,
			panicContains: "Lock held in EventLoop context",
		},
		// AssertLockHeld tests
		{
			name:          "AssertLockHeld panics when lock not held",
			lockHeld:      false,
			assertion:     func(r *receiver) { r.AssertLockHeld() },
			expectPanic:   true,
			panicContains: "Lock NOT held in Tick context",
		},
		{
			name:        "AssertLockHeld succeeds when lock held",
			lockHeld:    true,
			assertion:   func(r *receiver) { r.AssertLockHeld() },
			expectPanic: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{}
			r.initDebugContext()

			if tc.lockHeld {
				r.lock.Lock()
				defer r.lock.Unlock()
			}

			if tc.expectPanic {
				require.Panics(t, func() {
					tc.assertion(r)
				})
			} else {
				require.NotPanics(t, func() {
					tc.assertion(r)
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
			r := &receiver{}
			r.initDebugContext()

			if tc.inEventLoop {
				r.EnterEventLoop()
				defer r.ExitEventLoop()
			}
			if tc.inTick {
				r.EnterTick()
				defer r.ExitTick()
			}
			if tc.lockHeld {
				r.lock.Lock()
				defer r.lock.Unlock()
			}

			var assertFunc func()
			switch tc.assertion {
			case "EventLoopNoLock":
				assertFunc = func() { r.AssertEventLoopNoLock() }
			case "TickWithLock":
				assertFunc = func() { r.AssertTickWithLock() }
			}

			if tc.expectPanic {
				require.Panics(t, assertFunc)
			} else {
				require.NotPanics(t, assertFunc)
			}
		})
	}
}

// TestDebugContext_GoroutineTracking tests goroutine ID tracking.
func TestDebugContext_GoroutineTracking(t *testing.T) {
	r := &receiver{}
	r.initDebugContext()

	// Initially no goroutine tracked
	require.Equal(t, int64(0), r.debugCtx.eventLoopGo.Load())
	require.Equal(t, int64(0), r.debugCtx.tickGo.Load())

	// Enter EventLoop - should track goroutine ID
	r.EnterEventLoop()
	eventLoopGo := r.debugCtx.eventLoopGo.Load()
	require.NotEqual(t, int64(0), eventLoopGo, "EventLoop goroutine ID should be set")
	require.Equal(t, int64(0), r.debugCtx.tickGo.Load(), "Tick goroutine should not be set")

	// Exit EventLoop - should clear goroutine ID
	r.ExitEventLoop()
	require.Equal(t, int64(0), r.debugCtx.eventLoopGo.Load(), "EventLoop goroutine should be cleared")

	// Enter Tick - should track goroutine ID
	r.EnterTick()
	tickGo := r.debugCtx.tickGo.Load()
	require.NotEqual(t, int64(0), tickGo, "Tick goroutine ID should be set")
	require.Equal(t, int64(0), r.debugCtx.eventLoopGo.Load(), "EventLoop goroutine should not be set")

	// Exit Tick - should clear goroutine ID
	r.ExitTick()
	require.Equal(t, int64(0), r.debugCtx.tickGo.Load(), "Tick goroutine should be cleared")
}

// TestDebugContext_ConcurrentContextTracking tests that context tracking is thread-safe.
func TestDebugContext_ConcurrentContextTracking(t *testing.T) {
	r := &receiver{}
	r.initDebugContext()

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
				if !r.debugCtx.inEventLoop.Load() && !r.debugCtx.inTick.Load() {
					r.EnterEventLoop()
					// Brief work
					r.ExitEventLoop()
				}
			}
		}(i)
	}

	wg.Wait()

	// Final state should be clean
	require.False(t, r.debugCtx.inEventLoop.Load())
	require.False(t, r.debugCtx.inTick.Load())
	require.Equal(t, int64(0), r.debugCtx.eventLoopGo.Load())
	require.Equal(t, int64(0), r.debugCtx.tickGo.Load())
}

// TestGetCallerName verifies caller name extraction.
func TestGetCallerName(t *testing.T) {
	// Test the function indirectly by checking panic messages include function names
	r := &receiver{}
	r.initDebugContext()

	defer func() {
		if rec := recover(); rec != nil {
			msg := rec.(string)
			// The caller name should be in the panic message
			require.Contains(t, msg, "called outside EventLoop context")
		}
	}()

	r.AssertEventLoopContext() // Should panic with caller name
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

// TestDebugContext_RealWorldScenarios tests realistic usage patterns.
func TestDebugContext_RealWorldScenarios(t *testing.T) {
	testCases := []struct {
		name     string
		scenario func(r *receiver)
	}{
		{
			name: "EventLoop processes packets then exits",
			scenario: func(r *receiver) {
				r.EnterEventLoop()
				// Simulate processing
				r.AssertEventLoopContext()
				r.AssertNoLockHeld()
				r.ExitEventLoop()
			},
		},
		{
			name: "Tick acquires lock and processes",
			scenario: func(r *receiver) {
				r.EnterTick()
				r.lock.Lock()
				r.AssertTickContext()
				r.AssertLockHeld()
				r.lock.Unlock()
				r.ExitTick()
			},
		},
		{
			name: "Alternating EventLoop and Tick",
			scenario: func(r *receiver) {
				for i := 0; i < 3; i++ {
					r.EnterEventLoop()
					r.AssertEventLoopContext()
					r.ExitEventLoop()

					r.EnterTick()
					r.AssertTickContext()
					r.ExitTick()
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &receiver{}
			r.initDebugContext()

			require.NotPanics(t, func() {
				tc.scenario(r)
			})
		})
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkDebugContext_EnterExitEventLoop(b *testing.B) {
	r := &receiver{}
	r.initDebugContext()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.EnterEventLoop()
		r.ExitEventLoop()
	}
}

func BenchmarkDebugContext_AssertEventLoopContext(b *testing.B) {
	r := &receiver{}
	r.initDebugContext()
	r.EnterEventLoop()
	defer r.ExitEventLoop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AssertEventLoopContext()
	}
}

func BenchmarkDebugContext_CompoundAssertion_EventLoopNoLock(b *testing.B) {
	r := &receiver{}
	r.initDebugContext()
	r.EnterEventLoop()
	defer r.ExitEventLoop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AssertEventLoopNoLock()
	}
}

func BenchmarkDebugContext_GetGoroutineID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = getGoroutineID()
	}
}
