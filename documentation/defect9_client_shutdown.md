# Defect 9: Client with io_uring Output Does Not Exit Gracefully

**Status**: ✅ Fixed
**Created**: 2024-12-09
**Updated**: 2024-12-10
**Related Documents**:
- `graceful_quiesce_design.md` - Quiesce phase design for accurate metrics
- `context_and_cancellation_design.md` - Graceful shutdown architecture
- `defect8_nak_imbalance_investigation.md` - NAK imbalance fix (related io_uring work)
- `integration_testing_with_network_impairment_defects.md` - Parent defect tracking

---

## Summary

When running network impairment integration tests with `io_uring` output enabled (`-iouringoutput` flag), the client process does not exit gracefully after receiving SIGINT. The SRT connection closes correctly, but the client process hangs, requiring manual termination.

### Observed Behavior

```
Sending SIGINT to client (subscriber)...
Shutdown signal received                       <-- Client received SIGINT
{...connection_closed...}                      <-- SRT connection closed correctly
[PUB] 21:52:36.53 |  0.0 pkt/s | ...           <-- Client-gen still running (expected)
[PUB] 21:52:37.53 |  0.0 pkt/s | ...
... (repeats) ...
Error: client did not exit within 5 seconds    <-- CLIENT PROCESS STUCK
^C                                              <-- Manual termination required
```

### Expected Behavior

The client should exit gracefully within 5 seconds of receiving SIGINT, similar to how it behaves without io_uring output:

```
Sending SIGINT to client (subscriber)...
Shutdown signal received
{...connection_closed...}
Graceful shutdown complete
✓ Client exited gracefully
```

---

## Root Cause Analysis (Preliminary)

### Component: `IoUringWriter` in `contrib/common/writer_iouring_linux.go`

The `IoUringWriter` has a `completionHandler()` goroutine that blocks on `w.ring.WaitCQE()`:

```go
// completionHandler processes io_uring completions in a dedicated goroutine.
func (w *IoUringWriter) completionHandler() {
    defer w.wg.Done()

    for {
        cqe, err := w.ring.WaitCQE()   // <-- BLOCKING CALL
        if err != nil {
            if w.closed.Load() {
                return // Normal shutdown
            }
            continue // Retry on transient errors
        }
        // ... process completion ...
    }
}
```

The `Close()` method attempts to shut down the writer:

```go
func (w *IoUringWriter) Close() error {
    w.closed.Store(true)      // Set closed flag
    w.ring.QueueExit()        // Tell io_uring to exit
    w.wg.Wait()               // Wait for completionHandler to exit <-- BLOCKS FOREVER
    // ...
}
```

### The Problem

1. `WaitCQE()` is a **blocking system call** that waits for an io_uring completion
2. When `QueueExit()` is called, it may not immediately unblock `WaitCQE()`
3. If there are no pending completions, the goroutine remains stuck
4. `w.wg.Wait()` blocks forever waiting for the stuck goroutine

This explains why:
- Non-io_uring tests pass (no `IoUringWriter`)
- io_uring tests hang on client shutdown

---

## Test Cases Affected

| Test Configuration | io_uring Output | Result |
|-------------------|-----------------|--------|
| `Network-Loss2pct-1Mbps-NoIoUring` | ✗ | ✓ PASSED |
| `Network-Loss2pct-1Mbps-HighPerf` | ✓ | ✗ Hangs on shutdown |
| `Network-Loss2pct-5Mbps-HighPerf` | ✓ | ✗ Hangs on shutdown |

---

## SIGUSR1 Clarification

### Current Design (from `graceful_quiesce_design.md`)

SIGUSR1 is used exclusively for pausing the **data source** (client-generator):

```
| Signal | client-generator | client | server |
|--------|------------------|--------|--------|
| SIGUSR1| PAUSE data       | N/A    | N/A    |
| SIGINT | Shutdown         | Shutdown| Shutdown|
```

### Why Client Doesn't Need SIGUSR1

1. **Client-generator** is the data **source** - it can pause/stop sending
2. **Client** is the data **consumer** - it just receives what's available
3. Pausing the source naturally "drains" the pipeline to the consumer
4. The client will become idle automatically once no more data flows

**Conclusion**: Adding SIGUSR1 to the client is not the solution. The issue is the io_uring writer not exiting cleanly.

---

## Investigation Plan

### Phase 1: Understand the giouring Library Behavior

1. **Review `giouring.Ring.WaitCQE()` documentation**
   - How does it behave when `QueueExit()` is called?
   - Is there a timeout variant (`WaitCQETimeout`)?
   - Is there an interruptible variant?

2. **Check `QueueExit()` behavior**
   - Does it send a wakeup to blocked `WaitCQE()`?
   - Is there a race condition?

3. **Review similar patterns in GoSRT**
   - How does `connection_linux.go` handle io_uring shutdown?
   - The SRT layer uses io_uring successfully - what's different?

### Phase 2: Identify the Fix Strategy

**Option A: Use Non-Blocking Wait with Timeout**
```go
for {
    cqe, err := w.ring.WaitCQETimeout(100 * time.Millisecond)
    if err != nil {
        if w.closed.Load() {
            return // Normal shutdown
        }
        if isTimeout(err) {
            continue // Retry
        }
        continue // Other error
    }
    // ... process completion ...
}
```

**Option B: Use a Separate Context for Cancellation**
- Create a cancellable goroutine that exits on context cancellation
- Use `WaitCQEWithContext()` if available

**Option C: Submit a Dummy Operation to Unblock**
- Submit a NOP or small write to generate a completion
- This forces `WaitCQE()` to return

**Option D: Use EVENTFD for Wakeup**
- Register an eventfd with the io_uring ring
- Write to eventfd to wake up `WaitCQE()`

### Phase 3: Implement and Test

1. Implement chosen fix
2. Test with `Network-Loss2pct-1Mbps-HighPerf`
3. Verify graceful shutdown works
4. Run full test suite to ensure no regressions

### Phase 4: Document

1. Update this document with final solution
2. Update `graceful_quiesce_design.md` if needed
3. Add comments to `writer_iouring_linux.go`

---

## Files to Review

| File | Purpose |
|------|---------|
| `contrib/common/writer_iouring_linux.go` | `IoUringWriter` implementation |
| `contrib/client/main.go` | Client shutdown flow |
| `connection_linux.go` | GoSRT's io_uring usage (reference) |
| `listen_linux.go` | GoSRT's io_uring usage (reference) |

---

## Questions to Answer

1. Does `giouring.Ring.WaitCQE()` have a timeout variant?
2. How does GoSRT's main library handle io_uring shutdown? (It works correctly)
3. Is there a pattern we can copy from `connection_linux.go`?
4. Should we add a timeout to `IoUringWriter.Close()` to prevent indefinite blocking?

---

## Findings from GoSRT Library Review

### How `connection_linux.go` Handles io_uring Shutdown

The GoSRT library's `sendCompletionHandler()` has this pattern:

```go
func (c *srtConn) sendCompletionHandler(ctx context.Context) {
    defer c.sendCompWg.Done()

    ring, ok := c.sendRing.(*giouring.Ring)
    if !ok {
        return
    }

    for {
        // Check for context cancellation first
        select {
        case <-ctx.Done():
            c.drainCompletions()
            return
        default:
        }

        cqe, err := ring.WaitCQE()  // Blocking call
        if err != nil {
            // Check if context was cancelled while waiting
            select {
            case <-ctx.Done():
                c.drainCompletions()
                return
            default:
            }

            // Handle different error conditions
            if err == syscall.EBADF {
                // Ring closed - connection is shutting down
                return   // <-- CLEAN EXIT
            }

            if err != syscall.EAGAIN && err != syscall.EINTR {
                // Log error
            }
            continue // Retry WaitCQE
        }
        // ... process completion ...
    }
}
```

### Key Observations

1. **EBADF Handling**: When `ring.QueueExit()` is called, subsequent `WaitCQE()` calls return `syscall.EBADF`. The handler detects this and exits cleanly.

2. **Context Cancellation**: The handler checks `ctx.Done()` at two points:
   - Before calling `WaitCQE()`
   - After `WaitCQE()` returns an error

3. **Why GoSRT Works**:
   - During SRT communication, there are always pending completions (data packets)
   - When `QueueExit()` is called, there's likely a completion pending or `WaitCQE()` returns an error
   - The `EBADF` case handles the shutdown gracefully

### Why `IoUringWriter` Doesn't Work

The `IoUringWriter.completionHandler()` is simpler:

```go
func (w *IoUringWriter) completionHandler() {
    defer w.wg.Done()

    for {
        cqe, err := w.ring.WaitCQE()
        if err != nil {
            if w.closed.Load() {
                return // Normal shutdown
            }
            continue // Retry on transient errors
        }
        // ... process completion ...
    }
}
```

**Problems**:
1. **No EBADF handling**: When `QueueExit()` causes `WaitCQE()` to return `EBADF`, the handler doesn't recognize it as a shutdown signal
2. **`closed` flag timing**: The `closed` flag is checked only inside the error handling, but if `WaitCQE()` is blocked when `Close()` is called, it might not return an error at all
3. **No context**: Unlike GoSRT, the writer doesn't have a context to check for cancellation

### Proposed Fix (to be implemented)

Add `syscall.EBADF` handling to match GoSRT's pattern:

```go
func (w *IoUringWriter) completionHandler() {
    defer w.wg.Done()

    for {
        cqe, err := w.ring.WaitCQE()
        if err != nil {
            // Check for ring closed (QueueExit called)
            if err == syscall.EBADF {
                return // Ring closed - normal shutdown
            }

            // Check closed flag for other shutdown scenarios
            if w.closed.Load() {
                return // Normal shutdown
            }

            // Ignore EINTR (signal interruption)
            if err == syscall.EINTR {
                continue
            }

            continue // Retry on other transient errors
        }
        // ... process completion ...
    }
}
```

---

## Relationship to Other Defects

- **Defect 8** (NAK imbalance) - Fixed. The NAK counting issues are resolved.
- **Defect 8a** - This document. Client doesn't exit gracefully with io_uring output.
- **Defect 9** = **Defect 8a** (renamed for clarity)

---

## Priority

**Medium-High**: This blocks reliable testing of io_uring-enabled configurations. The test framework must be able to run without manual intervention.

---

## Next Steps

1. ✅ Document created (this file)
2. ✅ Review `giouring` library behavior (see findings above)
3. ✅ Compare with `connection_linux.go` shutdown handling (see findings above)
4. ✅ **Implement fix**: Added `syscall.EBADF`, `EINTR`, `EAGAIN` handling to `IoUringWriter.completionHandler()`
5. ⬜ **Test**: Run `Network-Loss2pct-1Mbps-HighPerf` to verify graceful shutdown
6. ⬜ **Verify**: Ensure non-io_uring tests still pass

---

## Implementation Details

### Fix 1: `contrib/common/writer_iouring_linux.go`

**Changes made:**
1. Added `syscall` import
2. Updated `completionHandler()` with comprehensive error handling:
   - **EBADF**: Ring closed via `QueueExit()` → clean exit (primary shutdown signal)
   - **EINTR**: Interrupted by signal → retry (normal during shutdown)
   - **EAGAIN**: No completions available → retry (defensive, shouldn't occur with WaitCQE)
   - Added `closed` flag check after EBADF check as secondary shutdown detection

### Fix 2 (CRITICAL): `connection_linux.go` - Reorder cleanup steps

**Root Cause Found:**
The `cleanupIoUring()` function was calling `QueueExit()` AFTER waiting 5 seconds for the completion handler:

```go
// BROKEN ORDER:
1. sendCompCancel()        // Cancel context
2. sendCompWg.Wait()       // Wait 5 seconds (handler is blocked in WaitCQE!)
3. ring.QueueExit()        // This would wake up WaitCQE, but we already timed out
```

The completion handler is blocked in `WaitCQE()` and can't check `ctx.Done()` until the syscall returns. But `QueueExit()` (which makes `WaitCQE()` return `EBADF`) wasn't called until AFTER the timeout!

**Fix:** Call `QueueExit()` FIRST to wake up the blocked `WaitCQE()`:

```go
// FIXED ORDER:
1. sendCompCancel()        // Cancel context
2. ring.QueueExit()        // Wake up blocked WaitCQE() (returns EBADF)
3. sendCompWg.Wait()       // Handler exits quickly now
```

### Fix 3: `listen_linux.go` - Same issue in listener

Applied the same fix to `cleanupIoUringRecv()` - moved `QueueExit()` before the wait.

### Summary of Changes (Iteration 1)

| File | Change |
|------|--------|
| `contrib/common/writer_iouring_linux.go` | Added EBADF/EINTR/EAGAIN error handling |
| `connection_linux.go` | Moved `QueueExit()` before `sendCompWg.Wait()` |
| `listen_linux.go` | Moved `QueueExit()` before `recvCompWg.Wait()` |

---

## Iteration 2: Receive Path Polling (2024-12-10)

After fix 1, the client still hung. Root cause: the **receive** paths in `dial_linux.go` and `listen_linux.go` used blocking `WaitCQE()` calls in `getRecvCompletion()`.

### Problem

When ctx is cancelled, the completion handler is blocked in `WaitCQE()` and can't exit. `QueueExit()` isn't called until 5+ seconds later (after internal timeouts cascade).

### Fix

Changed `getRecvCompletion()` in both `dial_linux.go` and `listen_linux.go` to use **polling with `PeekCQE()`** instead of blocking `WaitCQE()`:

```go
// BEFORE (blocking):
cqe, err := ring.WaitCQE()  // Blocks forever if no I/O pending

// AFTER (polling):
for {
    select {
    case <-ctx.Done():
        return nil, nil
    default:
    }
    cqe, err := ring.PeekCQE()
    if err == syscall.EAGAIN {
        time.Sleep(1 * time.Millisecond)
        continue
    }
    // ...
}
```

---

## Iteration 3: Send Path Polling + drainCompletions SIGSEGV (2024-12-10)

After fix 2, we got **SIGSEGV crashes**. Two issues found:

### Problem 1: `sendCompletionHandler` uses blocking `WaitCQE()`

The SEND path in `connection_linux.go:sendCompletionHandler()` was still using blocking `WaitCQE()`, unlike the receive paths we already fixed.

### Problem 2: `drainCompletions()` called after `QueueExit()`

When `sendCompletionHandler()` detected context cancellation, it called `drainCompletions()`. But by then, `cleanupIoUring()` had already called `QueueExit()`, which unmapped the ring memory. Calling `PeekCQE()` on a closed ring causes SIGSEGV.

Stack trace:
```
github.com/randomizedcoder/giouring.internalPeekCQE(...)
github.com/datarhei/gosrt.(*srtConn).drainCompletions(...)
github.com/datarhei/gosrt.(*srtConn).sendCompletionHandler(...)
```

### Fixes Applied

1. **Changed `sendCompletionHandler()` to use polling** like receive paths
2. **Removed `drainCompletions()` calls** from `sendCompletionHandler()` - it's dangerous after QueueExit()
3. **Added EBADF handling to `drainCompletions()`** for safety if called elsewhere

| File | Change |
|------|--------|
| `connection_linux.go:sendCompletionHandler` | Use polling `PeekCQE()` instead of blocking `WaitCQE()` |
| `connection_linux.go:sendCompletionHandler` | Remove `drainCompletions()` calls (SIGSEGV risk) |
| `connection_linux.go:drainCompletions` | Added EBADF handling for safety |

---

## Complete Fix Summary

| File | Issue | Fix |
|------|-------|-----|
| `writer_iouring_linux.go:completionHandler` | Blocking `WaitCQE()` | Use polling `PeekCQE()` |
| `connection_linux.go:cleanupIoUring` | `QueueExit()` after wait | Call `QueueExit()` FIRST |
| `connection_linux.go:sendCompletionHandler` | Blocking `WaitCQE()` | Use polling `PeekCQE()` |
| `connection_linux.go:sendCompletionHandler` | `drainCompletions()` on closed ring | Remove call |
| `connection_linux.go:drainCompletions` | No EBADF handling | Add EBADF check |
| `dial_linux.go:cleanupIoUringRecv` | `QueueExit()` after wait | Call `QueueExit()` FIRST |
| `dial_linux.go:getRecvCompletion` | Blocking `WaitCQE()` | Use polling `PeekCQE()` |
| `listen_linux.go:cleanupIoUringRecv` | `QueueExit()` after wait | Call `QueueExit()` FIRST |
| `listen_linux.go:getRecvCompletion` | Blocking `WaitCQE()` | Use polling `PeekCQE()` |

**Key Insight**: ALL io_uring completion handlers must use **polling** (PeekCQE + short sleep), never **blocking** (WaitCQE), because:
1. Blocking calls can't be interrupted by context cancellation
2. `QueueExit()` unmaps memory - calling ring functions after that causes SIGSEGV
3. Polling allows handlers to exit within ~1ms of context cancellation

---

## Iteration 4: Remove drainRecvCompletions() from Handlers (2024-12-10)

After fixes 1-3, a dedicated shutdown test revealed the client was STILL hanging. Debug logging showed it was stuck in `recvCompWg.Wait()`.

### Problem

The `recvCompletionHandler` in `dial_linux.go` and `listen_linux.go` were calling `drainRecvCompletions()` when `ctx.Done()` fired. But `drainRecvCompletions()` has a **5-second timeout** - it tries to drain all pending completions before exiting.

### Fix

Removed `drainRecvCompletions()` calls from both handlers. The ring will be closed by `cleanupIoUringRecv()` anyway, and the completion handler should exit immediately when ctx is cancelled.

| File | Change |
|------|--------|
| `dial_linux.go:recvCompletionHandler` | Removed `drainRecvCompletions()` call |
| `listen_linux.go:recvCompletionHandler` | Removed `drainRecvCompletions()` call |

---

## Verification

Created `contrib/integration_testing/test_shutdown.sh` - a fast, isolated shutdown test that verifies graceful exit of:
- Server standalone
- Client-generator (with server)
- Client with io_uring output (with server + client-generator)
- Client without io_uring output (control test)

All tests pass with 10-second timeout (server has 3-second read deadline in listener).

