# Signal Handler Design Options

## Problem Statement

Currently, there's duplication in the shutdown logic:
1. `setupSignalHandler()` waits for `shutdownWg` with timeout
2. `main()` also waits for `shutdownWg` with timeout after calling `Shutdown()`

Additionally, `Server.Shutdown()` currently just calls `s.ln.Close()`, but with context/waitgroup propagation (Phase 2+), the shutdown will be driven by context cancellation and waitgroups.

## Current Implementation

**Server main.go:**
```go
// Signal handler waits for shutdownWg
setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay)

// Main also waits for shutdownWg
<-ctx.Done()
s.Shutdown()
done := make(chan struct{})
go func() {
    shutdownWg.Wait()
    close(done)
}()
select {
case <-done:
case <-time.After(config.ShutdownDelay):
}
```

**Client main.go:**
```go
// Similar duplication
setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay)
// ... later ...
done := make(chan struct{})
go func() {
    shutdownWg.Wait()
    close(done)
}()
select {
case <-done:
case <-time.After(config.ShutdownDelay):
}
```

## Design Options

### Option 1: Signal Handler Calls Shutdown(), Main Just Waits

**Design:**
- Signal handler receives signal → cancels context → calls `s.Shutdown()` → waits for `shutdownWg`
- Main just blocks waiting for `shutdownWg` to complete

**Implementation:**
```go
// Signal handler
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc,
    shutdownWg *sync.WaitGroup, shutdownDelay time.Duration, server *server) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        cancel()
        server.Shutdown() // Call shutdown here
        // Wait for shutdownWg with timeout
        done := make(chan struct{})
        go func() {
            shutdownWg.Wait()
            close(done)
        }()
        select {
        case <-done:
        case <-time.After(shutdownDelay):
        }
    }()
}

// Main
func main() {
    // ... setup ...
    setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay, &s)

    // Just wait for shutdown to complete
    shutdownWg.Wait()

    if config.Logger != nil {
        config.Logger.Close()
    }
}
```

**Pros:**
- ✅ Single place for shutdown logic
- ✅ Main is very simple
- ✅ Clear separation: signal handler owns shutdown orchestration

**Cons:**
- ❌ Signal handler needs server reference (coupling)
- ❌ Less flexible - harder to add other shutdown triggers
- ❌ Client doesn't have a "Shutdown()" method, so pattern doesn't apply

**Go Idiomatic Score**: 6/10 (coupling concern)

---

### Option 2: Main Handles Everything, Signal Handler Just Cancels

**Design:**
- Signal handler just cancels context
- Main waits for context cancellation → calls `Shutdown()` → waits for `shutdownWg`

**Implementation:**
```go
// Signal handler
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        cancel() // Just cancel context
    }()
}

// Main
func main() {
    // ... setup ...
    setupSignalHandler(ctx, cancel)

    // Wait for context cancellation
    <-ctx.Done()

    // Shutdown server
    s.Shutdown()

    // Wait for graceful shutdown
    done := make(chan struct{})
    go func() {
        shutdownWg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // All shutdown operations completed
    case <-time.After(config.ShutdownDelay):
        // Timeout - proceed with exit
    }

    if config.Logger != nil {
        config.Logger.Close()
    }
}
```

**Pros:**
- ✅ Clean separation: signal handler only handles signals
- ✅ Main has full control over shutdown sequence
- ✅ Works for both server and client
- ✅ Flexible - easy to add other shutdown triggers
- ✅ No coupling between signal handler and server

**Cons:**
- ⚠️ Main has more logic (but it's the right place for it)
- ⚠️ Still has the "done channel" pattern (but it's necessary for timeout)

**Go Idiomatic Score**: 8/10 (clean separation, context-driven)

---

### Option 3: Context-Driven Shutdown (Most Idiomatic)

**Design:**
- Signal handler just cancels context
- `Server.Serve()` watches context and automatically calls `Shutdown()` when cancelled
- Main just waits for `shutdownWg`

**Implementation:**
```go
// Signal handler (same as Option 2)
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        cancel()
    }()
}

// Server.Serve() watches context
func (s *Server) Serve() error {
    for {
        select {
        case <-s.ctx.Done():
            // Context cancelled - shutdown automatically
            s.ln.Close() // This will trigger listener shutdown
            return ErrServerClosed
        default:
        }

        req, err := s.ln.Accept2()
        // ... handle connection ...
    }
}

// Main
func main() {
    // ... setup ...
    setupSignalHandler(ctx, cancel)

    // Start server (will shutdown automatically when context cancelled)
    go func() {
        if err := s.ListenAndServe(); err != nil && err != srt.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "SRT Server: %s\n", err)
            os.Exit(2)
        }
    }()

    // Just wait for shutdown to complete
    shutdownWg.Wait()

    if config.Logger != nil {
        config.Logger.Close()
    }
}
```

**Pros:**
- ✅ Most idiomatic Go - context drives everything
- ✅ Main is very simple
- ✅ Server automatically responds to context cancellation
- ✅ No explicit Shutdown() call needed
- ✅ Works naturally with context propagation

**Cons:**
- ⚠️ Requires changes to `Server.Serve()` (but we need to do this anyway in Phase 2)
- ⚠️ Need to ensure `Server.Serve()` properly decrements waitgroup

**Go Idiomatic Score**: 10/10 (most idiomatic, context-driven)

---

### Option 4: Signal Handler Does Everything, Main Just Blocks

**Design:**
- Signal handler receives signal → cancels context → calls `Shutdown()` → waits for `shutdownWg`
- Main just blocks waiting for `shutdownWg`

**Implementation:**
```go
// Signal handler
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc,
    shutdownWg *sync.WaitGroup, shutdownDelay time.Duration, shutdownFunc func()) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        cancel()
        shutdownFunc() // Call provided shutdown function
        // Wait for shutdownWg with timeout
        done := make(chan struct{})
        go func() {
            shutdownWg.Wait()
            close(done)
        }()
        select {
        case <-done:
        case <-time.After(shutdownDelay):
        }
    }()
}

// Main
func main() {
    // ... setup ...
    setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay, s.Shutdown)

    // Just block waiting for shutdown
    shutdownWg.Wait()

    if config.Logger != nil {
        config.Logger.Close()
    }
}
```

**Pros:**
- ✅ Main is very simple
- ✅ Flexible shutdown function (works for server and client)
- ✅ Single place for shutdown orchestration

**Cons:**
- ❌ Signal handler has shutdown logic (mixing concerns)
- ❌ Less clear what's happening
- ❌ Client doesn't have a Shutdown() method

**Go Idiomatic Score**: 5/10 (mixing concerns)

---

## Recommendation: Option 3 (Context-Driven Shutdown)

**Rationale:**
1. **Most Idiomatic Go**: Context cancellation should drive shutdown, not explicit function calls
2. **Clean Separation**: Signal handler only handles signals, Server responds to context
3. **Natural Flow**: With context propagation (Phase 2+), everything will respond to context cancellation automatically
4. **Simple Main**: Main just waits for shutdown to complete
5. **Future-Proof**: Works well with the planned context/waitgroup propagation

**Implementation Plan:**
1. Signal handler just cancels context (remove waitgroup logic)
2. `Server.Serve()` watches context and calls `Shutdown()` when cancelled
3. Main just waits for `shutdownWg` (no timeout needed - waitgroup will complete)
4. Client: signal handler cancels context, main waits for `shutdownWg`

**Note**: The timeout in `setupSignalHandler` is actually not needed if we trust the waitgroups. The timeout should be at the main level as a safety net, not in the signal handler.

---

## Alternative: Hybrid Approach (Option 2 + Simplified)

**Design:**
- Signal handler just cancels context (no waitgroup logic)
- Main waits for context → calls `Shutdown()` → waits for `shutdownWg` with timeout

**Implementation:**
```go
// Signal handler - just cancels context
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigChan
        cancel()
    }()
}

// Main
func main() {
    // ... setup ...
    setupSignalHandler(ctx, cancel)

    // Wait for context cancellation
    <-ctx.Done()

    // Shutdown server
    s.Shutdown()

    // Wait for graceful shutdown (with timeout as safety net)
    done := make(chan struct{})
    go func() {
        shutdownWg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // All shutdown operations completed
    case <-time.After(config.ShutdownDelay):
        // Timeout - proceed with exit (safety net)
    }

    if config.Logger != nil {
        config.Logger.Close()
    }
}
```

**Pros:**
- ✅ Clean separation
- ✅ Works immediately (doesn't require Phase 2 changes)
- ✅ Simple signal handler
- ✅ Main has timeout as safety net

**Cons:**
- ⚠️ Main has more logic (but it's the right place)

**Go Idiomatic Score**: 8/10

---

## Decision Matrix

| Option | Idiomatic | Simple | Flexible | Works Now | Future-Proof |
|--------|-----------|--------|----------|-----------|--------------|
| Option 1 | 6/10 | 9/10 | 5/10 | 7/10 | 6/10 |
| Option 2 | 8/10 | 7/10 | 9/10 | 9/10 | 8/10 |
| **Option 3** | **10/10** | **9/10** | **9/10** | **6/10** | **10/10** |
| Option 4 | 5/10 | 8/10 | 7/10 | 7/10 | 6/10 |
| Hybrid | 8/10 | 8/10 | 9/10 | 10/10 | 8/10 |

**Recommendation**: **Option 3** for long-term, **Hybrid (Option 2 + Simplified)** for immediate implementation.

The hybrid approach can be easily migrated to Option 3 when Phase 2 is implemented.

