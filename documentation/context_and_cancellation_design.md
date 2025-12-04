# Context and Cancellation Design

## Overview

This document describes the design for implementing Go `context.Context` with cancellation throughout the GoSRT library. The goal is to enable graceful shutdown by propagating a root context from `main.go` through all layers, allowing any goroutine to check for cancellation and respond appropriately.

## Design Principles

1. **Root Context**: A single root context with cancellation is created early in `main.go` (both server and client)
2. **Context Inheritance**: All contexts inherit from the root context, ensuring cancellation propagates
3. **Signal Handling**: OS signals (SIGINT, SIGTERM) cancel the root context
4. **Timeout Wrapping**: Timeout contexts wrap the root context, so signal cancellation also cancels timeouts
5. **Goroutine Cancellation**: All goroutines check for context cancellation in their main loops
6. **Graceful Shutdown**: All components can respond to cancellation and clean up resources
7. **Shutdown Delay**: Configurable delay (default 5 seconds) allows graceful shutdown to complete before application exit

---

## Architecture

### Context Hierarchy

```
main.go (root context)
  └── Server/Client
       └── Listener/Dialer
            └── Connection (srtConn)
                 ├── networkQueueReader goroutine
                 ├── writeQueueReader goroutine
                 ├── ticker goroutine
                 ├── sendHSRequests goroutine (HSv4 caller)
                 ├── sendKMRequests goroutine (HSv4 caller)
                 └── sendCompletionHandler goroutine (io_uring, Linux)
```

### Context Flow

1. **Root Context** (`main.go`)
   - Created with `context.WithCancel(context.Background())`
   - Cancelled by signal handler
   - Passed to Server/Client

2. **Server Context** (`server.go`)
   - Inherits from root context
   - Passed to Listener
   - Used for server-level goroutines

3. **Listener/Dialer Context** (`listen.go`, `dial.go`, `listen_linux.go`, `dial_linux.go`)
   - Inherits from Server/Client context
   - Passed to Connections
   - Used for listener/dialer-level goroutines

4. **Connection Context** (`connection.go`)
   - Inherits from Listener/Dialer context
   - Used for all connection-level goroutines
   - Wrapped with timeouts for specific operations

---

## Context Storage Rules

### When to Store Context in Struct

**Store context in struct when:**
1. **Long-lived objects** that need context for their entire lifetime
   - Examples: `Server`, `listener`, `dialer`, `srtConn`
   - Rationale: These objects exist for the duration of the application/connection and need context for multiple operations

2. **Objects that spawn multiple goroutines** that all need the same context
   - Examples: `listener` (spawns recvCompletionHandler, recvResubmitLoop), `srtConn` (spawns networkQueueReader, writeQueueReader, ticker)
   - Rationale: Storing context in struct avoids passing it to every goroutine function call

3. **Objects that need to create child contexts** (with timeout, cancellation, etc.)
   - Examples: `srtConn` (creates timeout contexts for peer idle timeout, handshake timeout)
   - Rationale: Need access to parent context to create child contexts

**Pattern**:
```go
type myStruct struct {
    // ... other fields ...
    ctx context.Context // Stored for long-lived object
}

func NewMyStruct(parentCtx context.Context) *myStruct {
    return &myStruct{
        ctx: parentCtx, // Store parent context
    }
}

func (s *myStruct) startGoroutine() {
    go s.worker(s.ctx) // Pass stored context
}
```

### When to Pass Context as Parameter

**Pass context as parameter when:**
1. **Short-lived operations** that don't need context beyond the function call
   - Examples: `handlePacket(ctx)`, `processRecvCompletion(ctx)`
   - Rationale: Context is only needed for the duration of the operation

2. **Functions that may be called with different contexts** depending on the caller
   - Examples: `Listen(network, address, config, ctx)`, `Dial(network, address, config, ctx)`
   - Rationale: Different callers may want different contexts (e.g., test contexts)

3. **Helper functions** that don't have their own struct
   - Examples: `setupSignalHandler(ctx, cancel)`, utility functions
   - Rationale: No struct to store context in

**Pattern**:
```go
func myFunction(ctx context.Context, otherParams ...) {
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
        // ... do work ...
    }
}
```

### General Rules Summary

1. **Long-lived structs** → Store context in struct field
2. **Short-lived operations** → Pass context as function parameter
3. **Goroutines** → Always pass context as parameter (even if stored in struct, pass it explicitly for clarity)
4. **Child contexts** → Always created from stored parent context (never from `context.Background()`)
5. **Timeouts** → Always wrap stored parent context (never wrap `context.Background()`)

### Examples

**✅ Correct: Store in struct (long-lived object)**
```go
type listener struct {
    ctx context.Context // Stored - listener lives for application lifetime
}

func (ln *listener) recvCompletionHandler(ctx context.Context) {
    // Passed as parameter - function needs context for this operation
}
```

**✅ Correct: Pass as parameter (short-lived operation)**
```go
func processPacket(ctx context.Context, p packet.Packet) {
    // No struct - context passed as parameter
    select {
    case <-ctx.Done():
        return
    default:
        // ... process ...
    }
}
```

**❌ Incorrect: Creating context from Background in long-lived object**
```go
type srtConn struct {
    ctx context.Context
}

func newSRTConn() *srtConn {
    c := &srtConn{}
    c.ctx, _ = context.WithCancel(context.Background()) // ❌ Should inherit from parent
    return c
}
```

**✅ Correct: Inheriting from parent**
```go
func newSRTConn(parentCtx context.Context) *srtConn {
    c := &srtConn{}
    c.ctx, _ = context.WithCancel(parentCtx) // ✅ Inherits from parent
    return c
}
```

---

## Implementation Details

### 1. Root Context Creation (`contrib/server/main.go` and `contrib/client/main.go`)

**Location**: Early in `main()` function, before any server/client initialization

**Implementation**:
```go
func main() {
    // Create root context with cancellation
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel() // Ensure cleanup on exit

    // Create root waitgroup for tracking all shutdown operations
    var shutdownWg sync.WaitGroup
    shutdownWg.Add(1) // Increment for server/client

    // Setup signal handler that cancels context and waits for waitgroup
    setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay)

    // ... rest of initialization ...

    // Pass context and waitgroup to server/client
    s.server = &srt.Server{
        // ... existing fields ...
        Context: ctx,     // NEW: Add context field
        ShutdownWg: &shutdownWg, // NEW: Add waitgroup field
    }
}
```

**Changes Required**:
- Add `Context context.Context` field to `srt.Server` struct
- Add `ShutdownWg *sync.WaitGroup` field to `srt.Server` struct
- Add `Context context.Context` parameter to `srt.Listen()` and `srt.Dial()` functions
- Add `ShutdownWg *sync.WaitGroup` parameter to `srt.Listen()` and `srt.Dial()` functions
- Update `contrib/server/main.go` to create root context and waitgroup, pass to server
- Update `contrib/client/main.go` to create root context and waitgroup, pass to dial/listen

---

### 2. Signal Handler Function with Shutdown Delay

**Location**: New function in `contrib/server/main.go` and `contrib/client/main.go`

**Implementation**:
```go
// setupSignalHandler sets up OS signal handling to cancel the root context
// After cancellation, waits for graceful shutdown (via waitgroups) before allowing application exit
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc, shutdownWg *sync.WaitGroup, shutdownDelay time.Duration) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        select {
        case <-sigChan:
            // Signal received - cancel root context to initiate graceful shutdown
            cancel()

            // Wait for graceful shutdown to complete (via waitgroups with timeout)
            // This ensures all goroutines exit, connections close, resources are cleaned up
            done := make(chan struct{})
            go func() {
                shutdownWg.Wait() // Wait for all waitgroups to complete
                close(done)
            }()

            select {
            case <-done:
                // All waitgroups completed - graceful shutdown successful
                // No need to wait for delay, shutdown is complete
            case <-time.After(shutdownDelay):
                // Timeout - proceed with exit even if waitgroups not complete
                // This prevents indefinite blocking if something is stuck
            }
        case <-ctx.Done():
            // Context already cancelled - exit
            return
        }
    }()
}
```

**Shutdown Delay Configuration**:
- **Default**: 5 seconds
- **Rationale**:
  - Maximum time to wait for waitgroups to complete
  - Allows time for connections to send shutdown packets
  - Allows time for goroutines to exit gracefully
  - Allows time for io_uring completions to drain
  - Balances between graceful shutdown and quick exit
  - **If waitgroups complete before delay expires, shutdown proceeds immediately**
- **Configurable**: Add `ShutdownDelay time.Duration` to `Config` struct (default: 5 seconds)

**Benefits**:
- Centralized signal handling
- Reusable across server and client
- Respects context cancellation (won't block if context already cancelled)
- Allows graceful shutdown to complete before application exit

**Changes Required**:
- Add `ShutdownDelay time.Duration` to `Config` struct (default: 5 seconds)
- Create root-level `sync.WaitGroup` in `main.go` to track all shutdown operations
- Create `setupSignalHandler()` function in both `contrib/server/main.go` and `contrib/client/main.go`
- Replace existing signal handling code with call to `setupSignalHandler(ctx, cancel, shutdownWg, config.ShutdownDelay)`
- Pass root waitgroup to Server/Client for aggregation

---

### 3. Server Context Propagation

**Location**: `server.go`

**Current State**: No context field

**Changes Required**:
```go
type Server struct {
    // ... existing fields ...
    ctx        context.Context      // NEW: Root context for server
    shutdownWg *sync.WaitGroup      // NEW: Root waitgroup (from main.go)
    listenerWg sync.WaitGroup       // NEW: Waitgroup for listener shutdown
}

// Listen opens the server listener.
func (s *Server) Listen() error {
    // ... existing code ...

    // Pass context and waitgroup to listener
    s.listenerWg.Add(1) // Increment for listener
    ln, err := Listen("srt", s.Addr, *s.Config, s.ctx, &s.listenerWg) // NEW: Pass context and waitgroup
    if err != nil {
        s.listenerWg.Done() // Decrement on error
        return err
    }

    // ... rest of code ...
}

// Serve starts accepting connections.
func (s *Server) Serve() error {
    for {
        select {
        case <-s.ctx.Done():
            // Context cancelled - shutdown gracefully
            return ErrServerClosed
        default:
        }

        req, err := s.ln.Accept2()
        // ... existing code ...
    }
}
```

**Changes Required**:
- Add `ctx context.Context` field to `Server` struct
- Add `shutdownWg *sync.WaitGroup` field to `Server` struct (root waitgroup)
- Add `listenerWg sync.WaitGroup` field to `Server` struct
- Update `Listen()` to pass context and waitgroup to `Listen()` function
- Update `Serve()` to check for context cancellation
- Update `Shutdown()` to:
  - Wait for `listenerWg` to complete
  - Call `shutdownWg.Done()` to notify root waitgroup

---

### 4. Listener Context Propagation

**Location**: `listen.go`, `listen_linux.go`

**Current State**: No context field, uses `context.Background()` in some places

**Changes Required**:
```go
type listener struct {
    // ... existing fields ...
    ctx        context.Context      // NEW: Context for listener
    shutdownWg *sync.WaitGroup      // NEW: Server waitgroup (from Server)
    connWg     sync.WaitGroup       // NEW: Waitgroup for all connections
    recvCompWg sync.WaitGroup       // Existing: Waitgroup for receive completion handler
}

// Listen creates a new SRT listener
func Listen(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup) (Listener, error) {
    // ... existing code ...

    ln := &listener{
        // ... existing fields ...
        ctx:        ctx,        // NEW: Store context
        shutdownWg: shutdownWg, // NEW: Store waitgroup
    }

    // ... rest of initialization ...

    // Start goroutines with context and waitgroup
    ln.recvCompWg.Add(1)
    go func() {
        defer ln.recvCompWg.Done()
        ln.recvCompletionHandler(ctx)
    }()

    // Note: recvResubmitLoop may not exist - verify actual goroutines

    return ln, nil
}
```

**Goroutine Updates**:
```go
// recvCompletionHandler processes io_uring receive completions
func (ln *listener) recvCompletionHandler(ctx context.Context) {
    defer ln.recvCompWg.Done()

    for {
        select {
        case <-ctx.Done():
            // Context cancelled - exit gracefully
            return
        default:
        }

        // ... existing completion handling code ...
    }
}
```

**Changes Required**:
- Add `ctx context.Context` field to `listener` struct
- Add `shutdownWg *sync.WaitGroup` field to `listener` struct (server waitgroup)
- Add `connWg sync.WaitGroup` field to `listener` struct
- Update `Listen()` function signature to accept context and waitgroup
- Pass context to all goroutines
- Update all goroutines to:
  - Call `recvCompWg.Add(1)` before starting (or appropriate waitgroup)
  - Call `recvCompWg.Done()` in defer when exiting
  - Check for context cancellation
- Update `Close()` to:
  - Cancel context (if we add a cancel function)
  - Wait for `connWg` (all connections closed)
  - Wait for `recvCompWg` (receive handler exited)
  - Call `shutdownWg.Done()` to notify server

---

### 5. Dialer Context Propagation

**Location**: `dial.go`, `dial_linux.go`

**Current State**: No context field, uses `context.Background()` in some places

**Changes Required**:
```go
type dialer struct {
    // ... existing fields ...
    ctx        context.Context      // NEW: Context for dialer
    shutdownWg *sync.WaitGroup      // NEW: Root waitgroup (from main.go)
    connWg     sync.WaitGroup       // NEW: Waitgroup for connection
    recvCompWg sync.WaitGroup       // Existing: Waitgroup for receive completion handler
}

// Dial creates a new SRT connection
func Dial(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup) (Conn, error) {
    // ... existing code ...

    dl := &dialer{
        // ... existing fields ...
        ctx:        ctx,        // NEW: Store context
        shutdownWg: shutdownWg, // NEW: Store waitgroup
    }

    // ... rest of initialization ...

    // Start goroutines with context and waitgroup
    dl.recvCompWg.Add(1)
    go func() {
        defer dl.recvCompWg.Done()
        dl.recvCompletionHandler(ctx)
    }()

    // Note: recvResubmitLoop may not exist - verify actual goroutines

    return conn, nil
}
```

**Changes Required**:
- Add `ctx context.Context` field to `dialer` struct
- Add `shutdownWg *sync.WaitGroup` field to `dialer` struct (root waitgroup)
- Add `connWg sync.WaitGroup` field to `dialer` struct
- Update `Dial()` function signature to accept context and waitgroup
- Pass context to all goroutines
- Update all goroutines to:
  - Call `recvCompWg.Add(1)` before starting (or appropriate waitgroup)
  - Call `recvCompWg.Done()` in defer when exiting
  - Check for context cancellation
- Update `Close()` to:
  - Wait for `connWg` (connection closed)
  - Wait for `recvCompWg` (receive handler exited)
  - Call `shutdownWg.Done()` to notify root

---

### 6. Connection Context Propagation

**Location**: `connection.go`

**Current State**: Uses `context.WithCancel(context.Background())` (line 424)

**Changes Required**:
```go
type srtConn struct {
    // ... existing fields ...
    ctx       context.Context  // CHANGED: Inherit from parent context
    cancelCtx context.CancelFunc // NEW: Cancel function for connection
}

// newSRTConn creates a new SRT connection
func newSRTConn(/* ... existing params ... */, parentCtx context.Context, parentWg *sync.WaitGroup) *srtConn {
    c := &srtConn{
        // ... existing fields ...
        shutdownWg: parentWg, // NEW: Store parent waitgroup
    }

    // Create connection context from parent context
    c.ctx, c.cancelCtx = context.WithCancel(parentCtx) // CHANGED: Inherit from parent

    // ... rest of initialization ...

    // Start goroutines with connection context and waitgroup
    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.networkQueueReader(c.ctx)
    }()

    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.writeQueueReader(c.ctx)
    }()

    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.ticker(c.ctx)
    }()

    // HSv4 caller contexts also inherit from connection context
    if c.version == 4 && c.isCaller {
        hsrequestsCtx, c.stopHSRequests := context.WithCancel(c.ctx) // CHANGED: Inherit from c.ctx
        c.connWg.Add(1)
        go func() {
            defer c.connWg.Done()
            c.sendHSRequests(hsrequestsCtx)
        }()

        if c.crypto != nil {
            kmrequestsCtx, c.stopKMRequests := context.WithCancel(c.ctx) // CHANGED: Inherit from c.ctx
            c.connWg.Add(1)
            go func() {
                defer c.connWg.Done()
                c.sendKMRequests(kmrequestsCtx)
            }()
        }
    }

    return c
}
```

**Goroutine Updates**:
```go
// networkQueueReader reads packets from the network queue
func (c *srtConn) networkQueueReader(ctx context.Context) {
    defer func() {
        c.log("connection:close", func() string { return "left networkQueueReader loop" })
    }()

    for {
        select {
        case <-ctx.Done():
            // Context cancelled - exit gracefully
            return
        case p := <-c.networkQueue:
            // ... existing packet processing ...
        }
    }
}

// writeQueueReader reads packets from the write queue
func (c *srtConn) writeQueueReader(ctx context.Context) {
    defer func() {
        c.log("connection:close", func() string { return "left writeQueueReader loop" })
    }()

    for {
        select {
        case <-ctx.Done():
            // Context cancelled - exit gracefully
            return
        case p := <-c.writeQueue:
            // ... existing packet processing ...
        }
    }
}

// ticker invokes congestion control at regular intervals
func (c *srtConn) ticker(ctx context.Context) {
    ticker := time.NewTicker(c.tick)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            // Context cancelled - exit gracefully
            return
        case t := <-ticker.C:
            // ... existing tick processing ...
        }
    }
}
```

**Changes Required**:
- Update `newSRTConn()` to accept `parentCtx context.Context` and `parentWg *sync.WaitGroup` parameters
- Add `shutdownWg *sync.WaitGroup` field to `srtConn` struct
- Add `connWg sync.WaitGroup` field to `srtConn` struct
- Change connection context creation to inherit from parent context
- Update HSv4 caller contexts to inherit from connection context
- Update all goroutines to:
  - Call `connWg.Add(1)` before starting
  - Call `connWg.Done()` in defer when exiting
  - Check for context cancellation
- Update `close()` to:
  - Call `c.cancelCtx()` to cancel connection context
  - Wait for `connWg` (all connection goroutines)
  - Wait for `sendCompWg` (io_uring send handler)
  - Call `shutdownWg.Done()` to notify parent

---

### 7. io_uring Send Completion Handler Context

**Location**: `connection_linux.go`

**Current State**: Uses `context.WithCancel(context.Background())` (line 66)

**Changes Required**:
```go
// initializeIoUring initializes the io_uring send ring
func (c *srtConn) initializeIoUring(config srtConnConfig) {
    // ... existing code ...

    // Create completion handler context from connection context
    c.sendCompCtx, c.sendCompCancel = context.WithCancel(c.ctx) // CHANGED: Inherit from c.ctx

    // Start completion handler goroutine
    c.sendCompWg.Add(1)
    go c.sendCompletionHandler(c.sendCompCtx)
}

// sendCompletionHandler processes io_uring send completions
func (c *srtConn) sendCompletionHandler(ctx context.Context) {
    defer c.sendCompWg.Done()

    for {
        select {
        case <-ctx.Done():
            // Context cancelled - exit gracefully
            return
        default:
        }

        // ... existing completion handling code ...
    }
}
```

**Changes Required**:
- Update `initializeIoUring()` to create context from connection context
- Update `sendCompletionHandler()` to check for context cancellation

---

### 8. Timeout Context Wrapping

**Location**: Throughout codebase where timeouts are used

**Principle**: All timeout contexts should wrap the root context (or appropriate parent context) so that signal cancellation also cancels timeouts.

**Current Timeout Usage**:
- `peerIdleTimeout`: Uses `time.Timer` (not context-based)
- `ConnectionTimeout`: Uses `time.AfterFunc` (not context-based)
- Handshake timeout: Hardcoded or implicit (not configurable)
- Various timeout operations in handshake, encryption, etc.

#### Timeout Configuration Review

**Current Timeouts**:

| Timeout | Current Value | Location | Type | Proposed Change |
|---------|--------------|----------|------|----------------|
| **ConnectionTimeout** | 3 seconds (default) | `config.go:244` | `time.Duration` | ✅ Keep as config option, wrap with context |
| **PeerIdleTimeout** | 2 seconds (default) | `config.go:268` | `time.Duration` | ✅ Keep as config option, convert to context-based |
| **HandshakeTimeout** | None (implicit) | `dial.go:222` | Uses `ConnectionTimeout` | ✅ Add as new config option, default: 1.5 seconds |
| **ReadDeadline** (fallback) | 3 seconds | `listen.go:258`, `dial.go:178` | `time.Duration` | ✅ Keep, but wrap with context |
| **io_uring drain timeout** | 5 seconds | `connection_linux.go:1706`, `listen_linux.go:771`, `dial_linux.go:539` | Hardcoded | ✅ Keep as hardcoded (internal operation) |
| **io_uring completion wait** | 5 seconds | `connection_linux.go:97`, `listen_linux.go:184`, `dial_linux.go:96` | Hardcoded | ✅ Keep as hardcoded (internal operation) |

**Proposed Timeouts**:

| Timeout | Proposed Value | Location | Rationale |
|---------|---------------|----------|-----------|
| **ConnectionTimeout** | 3 seconds (default) | `config.go` | Time for initial connection establishment (handshake) |
| **HandshakeTimeout** | 1.5 seconds (default) | `config.go` (NEW) | Time for complete handshake exchange (induction + conclusion). Must be < PeerIdleTimeout. SRT is designed for low loss/low RTT networks. |
| **PeerIdleTimeout** | 2 seconds (default, configurable) | `config.go` | Time before closing connection if no packets received. SRT default for low loss/low RTT networks. |
| **ShutdownDelay** | 5 seconds (default) | `config.go` (NEW) | Time to wait for graceful shutdown after signal |
| **ReadDeadline** (fallback) | 3 seconds | `listen.go`, `dial.go` | Timeout for blocking ReadFrom operations |

**Validation Rules**:
- `HandshakeTimeout` must be **less than** `PeerIdleTimeout`
  - Rationale: Handshake should complete before peer idle timeout fires
  - Validation: Add to `Config.Validate()` function
- `ConnectionTimeout` should be **less than or equal to** `HandshakeTimeout`
  - Rationale: Connection timeout is for initial response, handshake timeout is for complete exchange
  - Validation: Add to `Config.Validate()` function

**Changes Required**:
1. Add `HandshakeTimeout time.Duration` to `Config` struct (default: 30 seconds)
2. Add `ShutdownDelay time.Duration` to `Config` struct (default: 5 seconds)
3. Add validation to `Config.Validate()`:
   ```go
   if c.HandshakeTimeout >= c.PeerIdleTimeout {
       return fmt.Errorf("config: HandshakeTimeout (%v) must be less than PeerIdleTimeout (%v)",
           c.HandshakeTimeout, c.PeerIdleTimeout)
   }
   ```
4. Update `dial.go` to use `config.HandshakeTimeout` instead of `config.ConnectionTimeout` for handshake
5. Convert all timeouts to context-based (see examples below)

**Changes Required**:

**Example 1: Peer Idle Timeout**
```go
// Instead of time.Timer, use context.WithTimeout
func (c *srtConn) startPeerIdleTimeout() {
    // Create timeout context from connection context
    timeoutCtx, cancel := context.WithTimeout(c.ctx, c.config.PeerIdleTimeout)
    defer cancel()

    go func() {
        select {
        case <-timeoutCtx.Done():
            if timeoutCtx.Err() == context.DeadlineExceeded {
                // Timeout expired - close connection
                c.close()
            } else {
                // Context cancelled (signal or other) - exit gracefully
                return
            }
        }
    }()
}
```

**Example 2: Handshake Timeout (Dialer)**
```go
// In dial.go, use config.HandshakeTimeout instead of ConnectionTimeout
// Default: 1.5 seconds (for low loss/low RTT networks)
func (dl *dialer) Dial(...) {
    // Create timeout context from dialer context
    timeoutCtx, cancel := context.WithTimeout(dl.ctx, dl.config.HandshakeTimeout)
    defer cancel()

    // Send induction
    dl.sendInduction()

    // Wait for handshake to conclude with timeout
    select {
    case <-timeoutCtx.Done():
        if timeoutCtx.Err() == context.DeadlineExceeded {
            // Timeout - connection failed
            dl.log("connection:close:reason", func() string {
                return fmt.Sprintf("handshake timeout: server didn't respond within %s", dl.config.HandshakeTimeout)
            })
            dl.Close()
            return nil, fmt.Errorf("handshake timeout: server didn't respond within %s", dl.config.HandshakeTimeout)
        } else {
            // Context cancelled (signal) - exit gracefully
            return nil, ctx.Err()
        }
    case response := <-dl.connChan:
        // Handshake completed
        cancel() // Cancel timeout context
        // ... process response ...
    }
}
```

**Example 3: Connection Timeout (Initial Response)**
```go
// ConnectionTimeout is for initial response, not full handshake
// This can be shorter than HandshakeTimeout
func (dl *dialer) waitForInitialResponse() error {
    timeoutCtx, cancel := context.WithTimeout(dl.ctx, dl.config.ConnectionTimeout)
    defer cancel()

    // Wait for initial response
    select {
    case <-timeoutCtx.Done():
        if timeoutCtx.Err() == context.DeadlineExceeded {
            return fmt.Errorf("connection timeout: no initial response within %s", dl.config.ConnectionTimeout)
        }
        return timeoutCtx.Err()
    case response := <-dl.initialResponseChan:
        // ... process initial response ...
    }
}
```

**Benefits**:
- Signal cancellation immediately cancels all timeouts
- No need to manually cancel timers on shutdown
- Consistent timeout handling across codebase

---

### 9. Existing Context Creations

**Location**: Throughout codebase

**Current Issues**:
- Many contexts created with `context.Background()` instead of inheriting from parent
- Some contexts created but not properly cancelled

**Changes Required**:

**Before**:
```go
ctx, cancel := context.WithCancel(context.Background())
```

**After**:
```go
ctx, cancel := context.WithCancel(parentCtx) // Inherit from parent
```

**Files to Update**:
- `connection.go`: Line 424 (connection context)
- `connection_linux.go`: Line 66 (send completion context)
- `listen_linux.go`: Any context creations
- `dial_linux.go`: Any context creations
- Any other files creating contexts

---

## WaitGroup Design for Graceful Shutdown

### Overview

WaitGroups are critical for ensuring proper shutdown order: **children must shutdown before parents**. This prevents resource leaks and ensures clean teardown. The design uses a hierarchical waitgroup structure that mirrors the context hierarchy.

### WaitGroup Hierarchy

```
main.go (root shutdownWg)
  └── Server (serverShutdownWg)
       └── Listener (listenerShutdownWg)
            ├── recvCompletionHandler goroutine (io_uring)
            └── Connection (connectionShutdownWg)
                 ├── networkQueueReader goroutine
                 ├── writeQueueReader goroutine
                 ├── ticker goroutine
                 ├── sendHSRequests goroutine (HSv4 caller)
                 ├── sendKMRequests goroutine (HSv4 caller)
                 └── sendCompletionHandler goroutine (io_uring, Linux)
```

### WaitGroup Rules

1. **Each level has its own WaitGroup**: Server, Listener, Dialer, Connection each have their own waitgroup
2. **Goroutines call `Done()` on their parent's waitgroup**: When a goroutine exits, it decrements the parent's waitgroup
3. **Parents wait for children**: Before a parent can shutdown, it waits for its waitgroup to reach zero
4. **Root waitgroup aggregates all**: The root waitgroup in `main.go` waits for all server/client shutdowns

### Implementation Pattern

**Pattern for Goroutines**:
```go
func (parent *parentStruct) startGoroutine(ctx context.Context) {
    parent.wg.Add(1) // Increment parent's waitgroup
    go func() {
        defer parent.wg.Done() // Decrement when goroutine exits

        for {
            select {
            case <-ctx.Done():
                // Context cancelled - exit gracefully
                return
            default:
                // ... do work ...
            }
        }
    }()
}
```

**Pattern for Shutdown**:
```go
func (parent *parentStruct) shutdown() {
    // 1. Cancel context (triggers all child goroutines to exit)
    parent.cancelCtx()

    // 2. Wait for all child goroutines to exit
    done := make(chan struct{})
    go func() {
        parent.wg.Wait() // Wait for waitgroup
        close(done)
    }()

    select {
    case <-done:
        // All goroutines exited - proceed with cleanup
    case <-time.After(5 * time.Second):
        // Timeout - log warning but continue
        log("shutdown:warning", "timeout waiting for goroutines")
    }

    // 3. Clean up resources
    // 4. Notify parent (decrement parent's waitgroup)
    parent.parentWg.Done()
}
```

### WaitGroup Structure by Component

#### 1. Root WaitGroup (`main.go`)

**Purpose**: Track all server/client shutdown operations

**Implementation**:
```go
func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Root waitgroup for tracking all shutdown operations
    var shutdownWg sync.WaitGroup

    // Setup signal handler (waits for shutdownWg)
    setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay)

    // Create server/client with root waitgroup
    s.server = &srt.Server{
        // ... existing fields ...
        Context: ctx,
        ShutdownWg: &shutdownWg, // NEW: Pass root waitgroup
    }

    // ... rest of initialization ...
}
```

#### 2. Server WaitGroup (`server.go`)

**Purpose**: Track listener shutdown

**Implementation**:
```go
type Server struct {
    // ... existing fields ...
    ctx        context.Context
    shutdownWg *sync.WaitGroup // Root waitgroup (from main.go)
    listenerWg sync.WaitGroup  // NEW: Waitgroup for listener
}

func (s *Server) Shutdown() {
    // Close listener (triggers listener shutdown)
    if s.ln != nil {
        s.ln.Close()
    }

    // Wait for listener to shutdown
    done := make(chan struct{})
    go func() {
        s.listenerWg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Listener shutdown complete
    case <-time.After(5 * time.Second):
        // Timeout - log warning
    }

    // Notify root waitgroup
    s.shutdownWg.Done()
}
```

#### 3. Listener WaitGroup (`listen.go`, `listen_linux.go`)

**Purpose**: Track receive goroutines and connection shutdowns

**Current State**: Has `recvCompWg` for io_uring receive completion handler

**Changes Required**:
```go
type listener struct {
    // ... existing fields ...
    ctx        context.Context
    shutdownWg *sync.WaitGroup // Server waitgroup (from Server)

    // Existing waitgroups
    recvCompWg sync.WaitGroup // io_uring receive completion handler

    // NEW: Waitgroup for all connections
    connWg sync.WaitGroup // Tracks all connection shutdowns
}

func (ln *listener) Close() {
    ln.shutdownOnce.Do(func() {
        // 1. Cancel context (triggers goroutines to exit)
        ln.cancelCtx()

        // 2. Close all connections (triggers connection shutdowns)
        ln.conns.Range(func(key, value interface{}) bool {
            conn := value.(*srtConn)
            if conn != nil {
                conn.close() // Connection will call connWg.Done() when done
            }
            return true
        })

        // 3. Wait for all connections to shutdown
        done := make(chan struct{})
        go func() {
            ln.connWg.Wait()
            close(done)
        }()

        select {
        case <-done:
            // All connections closed
        case <-time.After(5 * time.Second):
            // Timeout - log warning
        }

        // 4. Wait for receive completion handler
        done = make(chan struct{})
        go func() {
            ln.recvCompWg.Wait()
            close(done)
        }()

        select {
        case <-done:
            // Receive handler exited
        case <-time.After(5 * time.Second):
            // Timeout - log warning
        }

        // 5. Clean up resources
        ln.cleanupIoUringRecv()
        ln.pc.Close()

        // 6. Notify server waitgroup
        ln.shutdownWg.Done()
    })
}
```

#### 4. Connection WaitGroup (`connection.go`)

**Purpose**: Track all connection-level goroutines

**Current State**: Has `sendCompWg` for io_uring send completion handler

**Changes Required**:
```go
type srtConn struct {
    // ... existing fields ...
    ctx        context.Context
    cancelCtx  context.CancelFunc
    shutdownWg *sync.WaitGroup // Listener/Dialer waitgroup (from parent)

    // NEW: Waitgroup for all connection goroutines
    connWg sync.WaitGroup

    // Existing waitgroups
    sendCompWg sync.WaitGroup // io_uring send completion handler
}

func newSRTConn(/* ... params ... */, parentCtx context.Context, parentWg *sync.WaitGroup) *srtConn {
    c := &srtConn{
        // ... existing fields ...
        shutdownWg: parentWg, // Store parent waitgroup
    }

    // Create connection context
    c.ctx, c.cancelCtx = context.WithCancel(parentCtx)

    // Start goroutines with waitgroup tracking
    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.networkQueueReader(c.ctx)
    }()

    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.writeQueueReader(c.ctx)
    }()

    c.connWg.Add(1)
    go func() {
        defer c.connWg.Done()
        c.ticker(c.ctx)
    }()

    // HSv4 caller goroutines
    if c.version == 4 && c.isCaller {
        c.connWg.Add(1)
        go func() {
            defer c.connWg.Done()
            c.sendHSRequests(hsrequestsCtx)
        }()

        if c.crypto != nil {
            c.connWg.Add(1)
            go func() {
                defer c.connWg.Done()
                c.sendKMRequests(kmrequestsCtx)
            }()
        }
    }

    // io_uring send completion handler (already has sendCompWg)
    // This is a sub-waitgroup, not part of connWg

    return c
}

func (c *srtConn) close() {
    c.shutdownOnce.Do(func() {
        // 1. Cancel context (triggers all goroutines to exit)
        c.cancelCtx()

        // 2. Send shutdown packet
        c.sendShutdown()

        // 3. Wait for all connection goroutines to exit
        done := make(chan struct{})
        go func() {
            c.connWg.Wait()
            close(done)
        }()

        select {
        case <-done:
            // All goroutines exited
        case <-time.After(5 * time.Second):
            // Timeout - log warning
        }

        // 4. Wait for io_uring send completion handler
        if c.sendRing != nil {
            done = make(chan struct{})
            go func() {
                c.sendCompWg.Wait()
                close(done)
            }()

            select {
            case <-done:
                // Send handler exited
            case <-time.After(5 * time.Second):
                // Timeout - log warning
            }

            c.drainCompletions()
            c.cleanupIoUring()
        }

        // 5. Flush congestion control
        c.snd.Flush()
        c.recv.Flush()

        // 6. Notify parent waitgroup (listener/dialer)
        c.shutdownWg.Done()
    })
}
```

#### 5. Dialer WaitGroup (`dial.go`, `dial_linux.go`)

**Purpose**: Track receive goroutines and connection shutdown

**Current State**: Has `recvCompWg` for io_uring receive completion handler

**Changes Required**: Similar to listener - add `connWg` for connection, wait for all goroutines before notifying parent.

### WaitGroup Shutdown Sequence

**When signal is received**:

1. **Signal handler cancels root context** → All child contexts cancelled
2. **Signal handler waits for root waitgroup** (with timeout)
3. **Server detects cancellation** → Calls `Shutdown()`
4. **Server waits for listener waitgroup** → Listener shutdown begins
5. **Listener cancels context** → All connection contexts cancelled
6. **Listener closes all connections** → Each connection calls `close()`
7. **Each connection**:
   - Cancels connection context
   - Sends shutdown packet
   - Waits for `connWg` (all connection goroutines exit)
   - Waits for `sendCompWg` (io_uring send handler exits)
   - Calls `parentWg.Done()` (notifies listener)
8. **Listener waits for `connWg`** → All connections closed
9. **Listener waits for `recvCompWg`** → Receive handler exited
10. **Listener calls `parentWg.Done()`** → Notifies server
11. **Server calls `shutdownWg.Done()`** → Notifies root
12. **Signal handler detects root waitgroup complete** → Application exits

**If timeout expires before waitgroups complete**:
- Application exits anyway (after `ShutdownDelay`)
- Remaining cleanup happens in background
- This is acceptable - delay is "best effort" graceful shutdown

### Benefits

1. **Ordered Shutdown**: Children always shutdown before parents
2. **No Resource Leaks**: All goroutines guaranteed to exit before parent cleanup
3. **Timeout Protection**: Shutdown delay prevents indefinite blocking
4. **Graceful**: All connections send shutdown packets before exit
5. **Predictable**: Clear shutdown sequence, easy to debug

---

## Connection Shutdown Process

### Overview

When context cancellation is triggered (via signal or explicit cancel), the following shutdown process occurs:

### Shutdown Sequence

1. **Root Context Cancelled** (`main.go`)
   - Signal handler calls `cancel()` on root context
   - All child contexts are immediately cancelled (cascade effect)

2. **Server Shutdown** (`server.go`)
   - `Serve()` detects context cancellation via `<-s.ctx.Done()`
   - Returns `ErrServerClosed`
   - `Shutdown()` is called (if not already called)

3. **Listener Shutdown** (`listen.go`, `listen_linux.go`)
   - `Close()` is called
   - All active connections are closed (iterates through `conns` map)
   - Receive goroutines exit (detect context cancellation)
   - Socket is closed

4. **Connection Shutdown** (`connection.go`)
   - `close()` is called (via `sync.Once` to ensure single execution)
   - **Step 4.1**: Unregister from metrics registry
   - **Step 4.2**: Print final statistics (if logger available)
   - **Step 4.3**: Stop peer idle timeout timer
   - **Step 4.4**: **Send SRT shutdown packet to peer** (`sendShutdown()`)
     - Creates `CTRLTYPE_SHUTDOWN` control packet
     - Marshals shutdown CIF
     - Sends via `pop()` (congestion control send path)
     - **Note**: Shutdown packet is always sent (unless connection already closed)
   - **Step 4.5**: Cancel connection context (`c.cancelCtx()`)
     - This causes all connection goroutines to exit:
       - `networkQueueReader` exits
       - `writeQueueReader` exits
       - `ticker` exits
       - `sendHSRequests` exits (HSv4 caller)
       - `sendKMRequests` exits (HSv4 caller)
   - **Step 4.6**: Clean up io_uring resources (if enabled)
     - Cancel send completion handler context
     - Wait for completion handler to finish (with 5 second timeout)
     - Drain remaining completions
     - Clean up completion tracking structures
   - **Step 4.7**: Flush congestion control
     - `c.snd.Flush()` - flush sender
     - `c.recv.Flush()` - flush receiver
   - **Step 4.8**: Call `onShutdown` callback (notifies listener/dialer)

5. **Dialer Shutdown** (`dial.go`, `dial_linux.go`)
   - `Close()` is called
   - Connection is closed (if exists)
   - Reader goroutine exits (detects context cancellation)
   - Receive goroutines exit
   - Socket is closed

### Shutdown Packet Behavior

**When is shutdown packet sent?**
- **Always sent** when `close()` is called (unless connection already closed)
- Sent via `sendShutdown()` which creates and sends `CTRLTYPE_SHUTDOWN` packet
- Uses congestion control send path (`pop()`) to ensure proper delivery

**What if shutdown packet fails to send?**
- Shutdown packet uses normal send path, so it may fail if:
  - Socket is already closed
  - io_uring ring is full (for io_uring path)
  - Network error
- **Behavior**: Shutdown continues regardless - packet send failure is logged but doesn't block shutdown

**Shutdown packet reception**:
- When peer receives shutdown packet, `handleShutdown()` is called
- This triggers `close()` on the receiving side
- Creates graceful bidirectional shutdown

### Graceful Shutdown Guarantees

1. **All goroutines exit**: Context cancellation ensures all goroutines detect cancellation and exit
2. **Shutdown packet sent**: Connection always attempts to send shutdown packet
3. **Resources cleaned up**:
   - io_uring rings closed
   - Channels closed
   - Timers stopped
   - Metrics unregistered
4. **No packet loss**: Existing packets in queues are processed before shutdown
5. **Timeout protection**: Shutdown delay (5 seconds default) ensures shutdown completes

### Shutdown Delay

**Purpose**: Allow time for graceful shutdown to complete before application exit

**Default**: 5 seconds

**What happens during shutdown delay**:
- Connections send shutdown packets
- Goroutines exit
- io_uring completions drain
- Resources are cleaned up

**If shutdown takes longer than delay**:
- Application exits anyway (after delay expires)
- Remaining cleanup happens in background (goroutines may still be running)
- **Note**: This is acceptable - the delay is a "best effort" graceful shutdown

**Configuration**:
- Add `ShutdownDelay time.Duration` to `Config` struct
- Default: 5 seconds
- Can be increased for large deployments with many connections

---

## Migration Plan

### Phase 1: Root Context, Signal Handling, and WaitGroups
1. Add `ShutdownDelay` to `Config` struct (default: 5 seconds)
2. Add root context creation in `contrib/server/main.go`
3. Add root context creation in `contrib/client/main.go`
4. Create root `sync.WaitGroup` in `main.go`
5. Create `setupSignalHandler()` function with waitgroup and shutdown delay
6. Replace existing signal handling with context-based approach
7. **Estimated Effort**: 3-4 hours

### Phase 2: Server Context Propagation and WaitGroups
1. Add `Context` field to `Server` struct
2. Add `ShutdownWg *sync.WaitGroup` field to `Server` struct (root waitgroup)
3. Add `listenerWg sync.WaitGroup` field to `Server` struct
4. Update `Listen()` to accept and store context
5. Update `Serve()` to check for context cancellation
6. Update `Shutdown()` to wait for listener waitgroup
7. Pass context to `Listen()` function
8. **Estimated Effort**: 2-3 hours

### Phase 3: Listener Context Propagation and WaitGroups
1. Add `ctx` field to `listener` struct
2. Add `shutdownWg *sync.WaitGroup` field to `listener` struct (server waitgroup)
3. Add `connWg sync.WaitGroup` field to `listener` struct
4. Update `Listen()` function signature to accept context and waitgroup
5. Pass context to all listener goroutines
6. Update all listener goroutines to check for cancellation and call `Done()` on waitgroup
7. Update `Close()` to wait for `connWg` and `recvCompWg` before notifying parent
8. **Estimated Effort**: 4-5 hours

### Phase 4: Dialer Context Propagation and WaitGroups
1. Add `ctx` field to `dialer` struct
2. Add `shutdownWg *sync.WaitGroup` field to `dialer` struct (root waitgroup)
3. Add `connWg sync.WaitGroup` field to `dialer` struct
4. Update `Dial()` function signature to accept context and waitgroup
5. Pass context to all dialer goroutines
6. Update all dialer goroutines to check for cancellation and call `Done()` on waitgroup
7. Update `Close()` to wait for `connWg` and `recvCompWg` before notifying parent
8. **Estimated Effort**: 3-4 hours

### Phase 5: Connection Context Propagation and WaitGroups
1. Update `newSRTConn()` to accept parent context and parent waitgroup
2. Add `shutdownWg *sync.WaitGroup` field to `srtConn` struct
3. Add `connWg sync.WaitGroup` field to `srtConn` struct
4. Change connection context to inherit from parent
5. Update HSv4 caller contexts to inherit from connection context
6. Update all connection goroutines to:
   - Call `connWg.Add(1)` before starting
   - Call `connWg.Done()` in defer when exiting
   - Check for context cancellation
7. Update `close()` to:
   - Cancel connection context
   - Wait for `connWg` (all connection goroutines)
   - Wait for `sendCompWg` (io_uring send handler)
   - Call `shutdownWg.Done()` to notify parent
8. **Estimated Effort**: 4-5 hours

### Phase 6: io_uring Context Updates
1. Update `initializeIoUring()` to use connection context
2. Update `sendCompletionHandler()` to check for cancellation
3. **Estimated Effort**: 1 hour

### Phase 7: Timeout Context Wrapping and Configuration
1. Add `HandshakeTimeout` to `Config` struct (default: 1.5 seconds)
2. Add validation: `HandshakeTimeout < PeerIdleTimeout`
3. Update `dial.go` to use `HandshakeTimeout` instead of `ConnectionTimeout` for handshake
4. Identify all timeout operations
5. Replace `time.Timer` with `context.WithTimeout` where appropriate
6. Ensure all timeout contexts wrap parent contexts
7. **Estimated Effort**: 5-7 hours

### Phase 8: Testing and Validation
1. Test graceful shutdown on SIGINT
2. Test graceful shutdown on SIGTERM
3. Test timeout cancellation on signal
4. Test connection cleanup on shutdown
5. **Estimated Effort**: 4-6 hours

**Total Estimated Effort**: 26-36 hours

---

## Benefits

1. **Graceful Shutdown**: All components can respond to cancellation signals
2. **Resource Cleanup**: Context cancellation ensures goroutines exit and resources are freed
3. **Consistent Behavior**: All timeout operations respect signal cancellation
4. **Better Testing**: Can test cancellation behavior without signals
5. **Idiomatic Go**: Follows Go best practices for context usage

---

## Potential Issues and Solutions

### Issue 1: Blocking Operations
**Problem**: Some operations (e.g., `Accept2()`, `Read()`) may block and not check context.

**Solution**: Use context-aware versions or wrap with timeout:
```go
// For Accept2(), check context before blocking
select {
case <-ctx.Done():
    return nil, ctx.Err()
default:
    req, err := s.ln.Accept2()
    // ... handle result ...
}
```

### Issue 2: Channel Operations
**Problem**: Channel sends/receives may block indefinitely.

**Solution**: Use `select` with context:
```go
select {
case <-ctx.Done():
    return ctx.Err()
case p := <-c.networkQueue:
    // ... process packet ...
}
```

### Issue 3: Existing Timeouts
**Problem**: Some timeouts use `time.Timer` which doesn't respect context cancellation.

**Solution**: Replace with `context.WithTimeout` wrapping parent context.

---

## Testing Strategy

### Test Cases

1. **Signal Cancellation**
   - Send SIGINT to server/client
   - Verify all goroutines exit
   - Verify resources are cleaned up

2. **Timeout Cancellation**
   - Start operation with timeout
   - Send SIGINT before timeout expires
   - Verify timeout is cancelled (not fired)

3. **Connection Cleanup**
   - Create multiple connections
   - Send SIGINT
   - Verify all connections are closed

4. **Goroutine Exit**
   - Monitor goroutine count
   - Send SIGINT
   - Verify goroutine count decreases

5. **Resource Leaks**
   - Run with race detector
   - Send SIGINT multiple times
   - Verify no resource leaks

---

## Configuration Changes Summary

### New Config Fields

1. **HandshakeTimeout** (`time.Duration`)
   - **Default**: 1.5 seconds
   - **Purpose**: Maximum time allowed for complete handshake exchange (induction + conclusion)
   - **Rationale**: SRT is designed for low loss/low RTT networks. Handshake should complete quickly. Must be less than `PeerIdleTimeout` (2 seconds default).
   - **Validation**: Must be less than `PeerIdleTimeout`
   - **Location**: `config.go`
   - **CLI Flag**: `-handshaketimeout` (e.g., "1.5s")

2. **ShutdownDelay** (`time.Duration`)
   - **Default**: 5 seconds
   - **Purpose**: Time to wait for graceful shutdown after signal before application exit
   - **Validation**: Must be greater than 0
   - **Location**: `config.go`
   - **CLI Flag**: `-shutdowndelay` (e.g., "5s")

### Updated Config Validation

Add to `Config.Validate()`:
```go
// Validate HandshakeTimeout
if c.HandshakeTimeout <= 0 {
    return fmt.Errorf("config: HandshakeTimeout must be greater than 0")
}

// Validate HandshakeTimeout < PeerIdleTimeout
if c.HandshakeTimeout >= c.PeerIdleTimeout {
    return fmt.Errorf("config: HandshakeTimeout (%v) must be less than PeerIdleTimeout (%v)",
        c.HandshakeTimeout, c.PeerIdleTimeout)
}

// Validate ShutdownDelay
if c.ShutdownDelay <= 0 {
    return fmt.Errorf("config: ShutdownDelay must be greater than 0")
}
```

### CLI Flag Updates

#### 1. Add Flag Definitions (`contrib/common/flags.go`)

Add to the flag variable declarations section:
```go
// Timeout and shutdown configuration flags
HandshakeTimeout = flag.Duration("handshaketimeout", 0, "Maximum time allowed for complete handshake exchange (e.g., 1.5s). Must be less than peeridletimeo")
ShutdownDelay    = flag.Duration("shutdowndelay", 0, "Time to wait for graceful shutdown after signal (e.g., 5s)")
```

#### 2. Add Flag Application Logic (`contrib/common/flags.go`)

Add to `ApplyFlagsToConfig()` function:
```go
if FlagSet["handshaketimeout"] {
    config.HandshakeTimeout = *HandshakeTimeout
}
if FlagSet["shutdowndelay"] {
    config.ShutdownDelay = *ShutdownDelay
}
```

#### 3. Add Test Cases (`contrib/common/test_flags.sh`)

Add the following test cases to validate the new flags:

```bash
# Test 21: HandshakeTimeout flag (1.5 seconds)
run_test "HandshakeTimeout flag (1.5s)" "-handshaketimeout 1.5s" '"HandshakeTimeout" *: *1500000000' "$CLIENT_BIN"

# Test 22: HandshakeTimeout flag (2 seconds)
run_test "HandshakeTimeout flag (2s)" "-handshaketimeout 2s" '"HandshakeTimeout" *: *2000000000' "$CLIENT_BIN"

# Test 23: HandshakeTimeout flag (500 milliseconds)
run_test "HandshakeTimeout flag (500ms)" "-handshaketimeout 500ms" '"HandshakeTimeout" *: *500000000' "$CLIENT_BIN"

# Test 24: ShutdownDelay flag (5 seconds)
run_test "ShutdownDelay flag (5s)" "-shutdowndelay 5s" '"ShutdownDelay" *: *5000000000' "$CLIENT_BIN"

# Test 25: ShutdownDelay flag (10 seconds)
run_test "ShutdownDelay flag (10s)" "-shutdowndelay 10s" '"ShutdownDelay" *: *10000000000' "$CLIENT_BIN"

# Test 26: ShutdownDelay flag (1 second)
run_test "ShutdownDelay flag (1s)" "-shutdowndelay 1s" '"ShutdownDelay" *: *1000000000' "$CLIENT_BIN"

# Test 27: HandshakeTimeout and ShutdownDelay together
run_test "HandshakeTimeout and ShutdownDelay together" "-handshaketimeout 1.5s -shutdowndelay 5s" '"HandshakeTimeout" *: *1500000000.*"ShutdownDelay" *: *5000000000' "$CLIENT_BIN"
```

**Note**: The expected patterns use nanoseconds (e.g., `1500000000` for 1.5 seconds) because Go's `time.Duration` is stored as nanoseconds internally and JSON marshaling outputs nanoseconds.

**Test Pattern Format**:
- Duration flags are tested similar to `StatisticsPrintInterval` (see tests 17-19 in `test_flags.sh`)
- The pattern matches the JSON output from the `-testflags` command
- Use `.*` to match multiple fields in a single test (as shown in test 27)

---

## Conclusion

This design provides a comprehensive approach to context and cancellation throughout the GoSRT library. By propagating a root context from `main.go` through all layers, we enable graceful shutdown and ensure all components can respond to cancellation signals. The design includes:

- **Clear rules** for when to store context vs pass as parameter
- **Configurable shutdown delay** (default 5 seconds) for graceful shutdown
- **Comprehensive timeout review** with proposed values and validation
- **Detailed shutdown process** documentation showing step-by-step connection closure
- **New config options** for handshake timeout and shutdown delay with validation rules

The migration can be done incrementally, phase by phase, minimizing risk and allowing for testing at each stage.

