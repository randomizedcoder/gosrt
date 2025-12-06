# Test 1.1 Server Timeout Defect Analysis

## Issue Description

During the Test 1.1 Graceful Shutdown integration test, the server reports "Shutdown timed out after 5s" even though all processes exit with code 0. This indicates that `wg.Wait()` in `contrib/server/main.go` is not completing normally - something is not calling `wg.Done()`.

**Observed Behavior:**
```
Sending SIGINT to server...
Shutdown signal received
Shutdown timed out after 5s
✓ Server shutdown completed
```

**Expected Behavior:**
```
Sending SIGINT to server...
Shutdown signal received
Graceful shutdown complete
✓ Server shutdown completed
```

---

## WaitGroup Analysis

### Expected WaitGroup Add/Done Pairs (from context_and_cancellation_new_design.md)

| Component | Add(1) Location | Done() Location | Notes |
|-----------|-----------------|-----------------|-------|
| Prometheus HTTP Server | main.go:196 | main.go:198 (defer) | Returns when promSrv.Shutdown() called |
| Logger Goroutine | main.go:210 | main.go:212 (defer) | Returns when logger.Close() closes channel |
| Stats Ticker | main.go:224 | main.go:226 (defer) | Returns when ctx.Done() triggers |
| ListenAndServe Goroutine | main.go:257 | main.go:259 (defer) | Returns when ListenAndServe() returns |
| **Listener** | listen.go:195 | listen.go:498 | Returns when Close() is called |

### Test Configuration

The integration test uses:
```
Server: -addr 127.0.0.10:6000 -metricsenabled -metricslistenaddr 127.0.0.10:5101
```

- **Logger**: NOT enabled (no `-logtopics` flag) → wg.Add(1) not called
- **Stats Ticker**: NOT enabled (no `-statisticsinterval` flag) → wg.Add(1) not called
- **io_uring**: NOT enabled (no `-iouringrecvenabled` flag)

### Expected WaitGroup Counter

| Event | Counter |
|-------|---------|
| Initial | 0 |
| Prometheus wg.Add(1) | 1 |
| ListenAndServe wg.Add(1) | 2 |
| Listener wg.Add(1) | 3 |
| **Total at startup** | **3** |

---

## Hypotheses

### Hypothesis 1: Listener.Close() Never Called (MOST LIKELY)

**Root Cause Analysis:**

Looking at `server.go` `Server.Serve()`:

```go
func (s *Server) Serve() error {
    for {
        // Check for context cancellation first
        if s.Context != nil {
            select {
            case <-s.Context.Done():
                s.Shutdown()           // ← This calls ln.Close()
                return ErrServerClosed
            default:
            }
        }

        req, err := s.ln.Accept2()
        if err != nil {
            if err == ErrListenerClosed {
                return ErrServerClosed  // ← BUG: Does NOT call s.Shutdown()!
            }
            return err
        }
        // ...
    }
}
```

**The Bug:**

When `Accept2()` returns with `ErrListenerClosed` (triggered by the reader goroutine detecting `ctx.Done()`), the `Serve()` function returns `ErrServerClosed` **WITHOUT calling `s.Shutdown()`**.

This means `listener.Close()` is never called, which means `ln.shutdownWg.Done()` is never called!

**Shutdown Flow (Actual - Buggy):**

```
SIGINT received
    │
    ▼
ctx.Done() closes
    │
    ├─────────────────────────────────────┐
    │                                     │
    ▼                                     ▼
Prometheus shutdown watcher         Reader goroutine in listen.go
calls promSrv.Shutdown()            detects ctx.Done() at line 272
    │                                     │
    ▼                                     ▼
promSrv.ListenAndServe() returns    calls ln.markDone(ErrListenerClosed)
    │                                     │
    ▼                                     ▼
wg.Done() [counter: 3→2]            doneChan closes
    │                                     │
    │                                     ▼
    │                               Accept2() returns ErrListenerClosed
    │                                     │
    │                                     ▼
    │                               Serve() returns ErrServerClosed
    │                               (WITHOUT calling Shutdown()!) ← BUG
    │                                     │
    │                                     ▼
    │                               ListenAndServe() returns
    │                                     │
    │                                     ▼
    │                               wg.Done() [counter: 2→1]
    │                                     │
    ├─────────────────────────────────────┘
    │
    ▼
main: wg.Wait() blocks...
counter = 1 (Listener's Done() never called!)
    │
    ▼
Timeout after 5 seconds
```

### Hypothesis 2: Reader Goroutine Not Detecting ctx.Done() (UNLIKELY)

The reader goroutine in `listen.go` lines 262-325 has multiple checks for `ctx.Done()`:
- Line 272: Before attempting read
- Line 283: Sets 3-second read deadline, so ReadFrom() will timeout
- Line 292: After read error
- Line 315: Before queueing packet

This should work correctly.

### Hypothesis 3: accept2() Blocking Indefinitely (UNLIKELY)

`Accept2()` uses a select:
```go
select {
case <-ln.doneChan:
    return nil, ln.error()
case p := <-ln.backlog:
    // ...
}
```

When `markDone()` is called, it closes `doneChan`, which should unblock `Accept2()`. This should work correctly.

---

## Diagnosis Steps

### Step 1: Verify the Hypothesis

Add debug logging to `server.go` `Serve()` to confirm `Shutdown()` is not being called when `Accept2()` returns `ErrListenerClosed`:

```go
req, err := s.ln.Accept2()
if err != nil {
    if err == ErrListenerClosed {
        fmt.Fprintf(os.Stderr, "DEBUG: Accept2 returned ErrListenerClosed, Shutdown() NOT called\n")
        return ErrServerClosed
    }
    return err
}
```

### Step 2: Verify WaitGroup Counter

Add logging to track wg.Add() and wg.Done() calls:
- In `listen.go:195`: Log when wg.Add(1) is called
- In `listen.go:498`: Log when wg.Done() is called
- Check if the Done() log message ever appears

### Step 3: Code Review Against Design

Compare the implementation against `context_and_cancellation_new_design.md`:

**Design says:**
> When Listener closes: calls shutdownWg.Done()

**Implementation:**
- `listener.Close()` at line 498 DOES call `shutdownWg.Done()`
- BUT `listener.Close()` is only called by `Server.Shutdown()`
- AND `Server.Shutdown()` is NOT called when `Accept2()` returns `ErrListenerClosed`

---

## Proposed Fix

### Option A: Always call Shutdown() before returning (RECOMMENDED)

```go
// In server.go Serve()
req, err := s.ln.Accept2()
if err != nil {
    if err == ErrListenerClosed {
        s.Shutdown()  // Add this line to ensure listener.Close() is called
        return ErrServerClosed
    }
    return err
}
```

**Pros:**
- Simple, minimal change
- Ensures `listener.Close()` is always called
- `Shutdown()` uses `sync.Once` internally, so calling it multiple times is safe

**Cons:**
- None

### Option B: Call Shutdown() at the end of Serve() (ALTERNATIVE)

```go
func (s *Server) Serve() error {
    defer s.Shutdown()  // Always cleanup on exit

    for {
        // ... existing code ...
    }
}
```

**Pros:**
- Guarantees cleanup regardless of exit path

**Cons:**
- `defer` adds overhead (minor)
- May hide the actual issue

### Option C: Have listener.Close() call markDone() (ALTERNATIVE)

Ensure `listener.Close()` calls `markDone()` to close `doneChan`, which would trigger `Accept2()` to return, which would then trigger the top-of-loop context check.

**Cons:**
- More complex
- Doesn't address the root cause

---

## Verification After Fix

1. Run `make test-integration` and verify:
   - "Graceful shutdown complete" appears instead of "Shutdown timed out after 5s"
   - All processes exit with code 0

2. Run `make test` to ensure no regressions in unit tests

3. Verify WaitGroup counter reaches 0 before timeout

---

## Related Documents

- `context_and_cancellation_new_design.md` - WaitGroup design specification
- `context_and_cancellation_implementation.md` - Implementation progress
- `test_1.1_detailed_design.md` - Test design
- `test_1.1_implementation.md` - Test implementation progress

---

## Status

**Status:** ✅ FIXED
**Priority:** Medium (processes exit cleanly, but not gracefully)
**Estimated Fix Time:** 15 minutes

---

## Fix Applied

**Date:** 2024-12-05

**Change:** Added `s.Shutdown()` call in `server.go` `Serve()` when `Accept2()` returns `ErrListenerClosed`:

```go
req, err := s.ln.Accept2()
if err != nil {
    if err == ErrListenerClosed {
        // Ensure listener is properly closed and shutdownWg.Done() is called
        // This can happen when the reader goroutine detects ctx.Done() and
        // closes doneChan before the top-of-loop context check runs
        s.Shutdown()
        return ErrServerClosed
    }
    return err
}
```

**Verification:**
- ✅ Integration test now shows "Graceful shutdown complete" (was "Shutdown timed out after 5s")
- ✅ All unit tests pass (26 tests)
- ✅ All three components (client, client-generator, server) exit gracefully

