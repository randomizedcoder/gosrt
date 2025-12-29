# Context and Cancellation - Improved Design

## Overview

This document describes an improved design for context and cancellation handling in the GoSRT library. This design supersedes the original `context_and_cancellation_design.md` with a cleaner, more idiomatic Go approach.

## Key Improvements Over Original Design

| Original Design | New Design |
|-----------------|------------|
| Manual signal handling with channels | `signal.NotifyContext` (Go 1.16+ idiom) |
| Separate `setupSignalHandler()` function | Not needed - built into `signal.NotifyContext` |
| Two waitgroups (main + library) | Single waitgroup for everything |
| Unclear HTTP server shutdown | Explicit shutdown watcher pattern |
| Complex shutdown coordination | Simple, linear shutdown flow |

---

## Design Principles

1. **Single Context**: Use `signal.NotifyContext` to create a context that cancels on OS signals
2. **Single WaitGroup**: Track all goroutines with one waitgroup
3. **Explicit Shutdown Watchers**: Each server (HTTP, SRT) has a goroutine that watches context and triggers shutdown
4. **Blocking Main Calls in Goroutines**: Run `ListenAndServe()` calls in goroutines with `wg.Add(1)` / `defer wg.Done()`
5. **Clean Exit**: Wait for all goroutines with a timeout safety net

---

## Core Pattern

### Signal Handling with `signal.NotifyContext`

```go
func main() {
    // Create context that cancels on SIGINT or SIGTERM
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()  // Deregister signal handling on exit

    // ... start servers and goroutines ...

    // Block until signal received
    <-ctx.Done()

    // ... cleanup and wait for goroutines ...
}
```

**Benefits:**
- Single line replaces 10+ lines of manual signal handling
- Automatic signal deregistration with `defer stop()`
- Context integrates naturally with Go's context pattern
- No need for separate signal channel management

### Prometheus HTTP Server Shutdown Pattern

```go
// Create Prometheus metrics HTTP server
promSrv := &http.Server{
    Addr:    addr,
    Handler: metrics.NewHTTPHandler(),
}

// Shutdown watcher - triggers clean shutdown when context cancelled
go func() {
    <-ctx.Done()
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := promSrv.Shutdown(shutdownCtx); err != nil {
        log.Printf("Prometheus server shutdown error: %v", err)
    }
}()

// Run Prometheus server in goroutine with waitgroup tracking
wg.Add(1)
go func() {
    defer wg.Done()
    if err := promSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        log.Printf("Prometheus server error: %v", err)
    }
}()
```

**Benefits:**
- Clean separation: watcher handles shutdown, goroutine runs server
- `ListenAndServe()` returns when `Shutdown()` is called
- Timeout on shutdown prevents hanging
- Waitgroup tracks goroutine completion

### SRT Server Pattern

The SRT server already implements Option 3 (Context-Driven Shutdown) where `Serve()` watches the context internally.

**Current Pattern (struct literal initialization):**
```go
s.server = &srt.Server{
    Addr:            addr,
    HandleConnect:   handleConnect,
    HandlePublish:   handlePublish,
    HandleSubscribe: handleSubscribe,
    Config:          &config,
    Context:         ctx,
    ShutdownWg:      &wg,
}
```

**New Pattern (constructor function with context first):**
```go
s.server = srt.NewServer(ctx, &wg, srt.ServerConfig{
    Addr:            addr,
    Config:          &config,
    HandleConnect:   handleConnect,
    HandlePublish:   handlePublish,
    HandleSubscribe: handleSubscribe,
})
```

---

### NewServer Constructor Function

**Rationale:**
- Follows Go idiom: constructor functions (`New*`) for struct initialization
- Context as first argument per Go convention
- Encapsulates required vs optional parameters
- Enables validation and default setting in one place
- Consistent with other parts of the codebase (`NewLogger`, `NewSender`, `NewReceiver`, etc.)

**Proposed API:**

```go
// ServerConfig contains configuration for creating a new SRT server.
type ServerConfig struct {
    // Addr is the address the SRT server should listen on, e.g. ":6001".
    Addr string

    // Config is the SRT connection configuration.
    Config *Config

    // HandleConnect will be called for each incoming connection.
    // If nil, all connections will be rejected.
    HandleConnect AcceptFunc

    // HandlePublish will be called for a publishing connection.
    // If nil, a default handler that closes the connection will be used.
    HandlePublish func(conn Conn)

    // HandleSubscribe will be called for a subscribing connection.
    // If nil, a default handler that closes the connection will be used.
    HandleSubscribe func(conn Conn)
}

// NewServer creates a new SRT server with the given context and configuration.
// The context should be the root context that, when cancelled, triggers graceful shutdown.
// The waitgroup is used to track when the server has fully shutdown.
func NewServer(ctx context.Context, wg *sync.WaitGroup, config ServerConfig) *Server {
    s := &Server{
        Addr:            config.Addr,
        Config:          config.Config,
        HandleConnect:   config.HandleConnect,
        HandlePublish:   config.HandlePublish,
        HandleSubscribe: config.HandleSubscribe,
        Context:         ctx,
        ShutdownWg:      wg,
    }

    // Set defaults
    if s.HandlePublish == nil {
        s.HandlePublish = s.defaultHandler
    }
    if s.HandleSubscribe == nil {
        s.HandleSubscribe = s.defaultHandler
    }
    if s.Config == nil {
        defaultConfig := DefaultConfig()
        s.Config = &defaultConfig
    }

    return s
}
```

**Benefits:**
1. **Context first**: Follows Go idiom for context placement
2. **Validation**: Can validate config and set defaults in constructor
3. **Encapsulation**: Separates public config (`ServerConfig`) from internal fields
4. **Consistency**: Matches pattern used elsewhere in codebase
5. **Cleaner main.go**: Reduces boilerplate in application code

**Usage in contrib/server/main.go:**

```go
// Before (struct literal)
s.server = &srt.Server{
    Addr:            s.addr,
    HandleConnect:   s.handleConnect,
    HandlePublish:   s.handlePublish,
    HandleSubscribe: s.handleSubscribe,
    Config:          &config,
    Context:         ctx,
    ShutdownWg:      &shutdownWg,
}

// After (constructor)
s.server = srt.NewServer(ctx, &wg, srt.ServerConfig{
    Addr:            s.addr,
    Config:          &config,
    HandleConnect:   s.handleConnect,
    HandlePublish:   s.handlePublish,
    HandleSubscribe: s.handleSubscribe,
})
```

---

### Running the Server

```go
// Run in goroutine with waitgroup tracking
wg.Add(1)
go func() {
    defer wg.Done()
    if err := s.ListenAndServe(); err != nil && err != srt.ErrServerClosed {
        log.Printf("SRT server error: %v", err)
    }
}()
```

---

## Architecture

### Goroutine and WaitGroup Hierarchy

```
main()
│
├── ctx, stop := signal.NotifyContext(...)  // Cancels on signal
│
├── var wg sync.WaitGroup                   // Single waitgroup for all
│
├── Metrics HTTP Server (if enabled)
│   ├── Shutdown watcher goroutine (watches ctx.Done(), calls Shutdown())
│   └── wg.Add(1) → ListenAndServe() goroutine → wg.Done()
│
├── Logger goroutine (if enabled)
│   └── wg.Add(1) → for range Logger.Listen() → wg.Done()
│
├── Statistics ticker goroutine (if enabled)
│   └── wg.Add(1) → ticker loop with ctx.Done() check → wg.Done()
│
├── SRT Server
│   ├── Listener internally does wg.Add(1) on start, wg.Done() on close
│   └── wg.Add(1) → ListenAndServe() goroutine → wg.Done()
│
├── <-ctx.Done()  // Block until signal
│
├── Cleanup (close logger, etc.)
│
└── wg.Wait() with timeout  // Wait for all goroutines
```

### Shutdown Flow

```
Signal (SIGINT/SIGTERM) received
         │
         ▼
    ctx cancelled
    (ctx.Done() closes)
         │
         ├────────────────────────────────────────────┐
         │                                            │
         ▼                                            ▼
  Prometheus shutdown watcher            SRT Serve() detects ctx.Done()
  calls promSrv.Shutdown()              calls Shutdown() internally
         │                                            │
         ▼                                            ▼
  promSrv.ListenAndServe()              listener.Close()
  returns http.ErrServerClosed          - waits for connections (connWg)
         │                              - calls wg.Done()
         ▼                                            │
      wg.Done()                                       ▼
         │                              ListenAndServe() returns
         │                              ErrServerClosed
         │                                            │
         │                                            ▼
         │                                        wg.Done()
         │                                            │
         ├────────────────────────────────────────────┘
         │
         ▼
  main: <-ctx.Done() unblocks
         │
         ▼
  config.Logger.Close()
  (logger goroutine exits, wg.Done())
         │
         ▼
  Statistics ticker sees ctx.Done()
  (returns, wg.Done())
         │
         ▼
  wg.Wait() completes
  (or timeout after ShutdownDelay)
         │
         ▼
  main() exits cleanly
```

---

## WaitGroup Design for Graceful Shutdown

### Overview

WaitGroups are critical for ensuring proper shutdown order: **children must shutdown before parents**. This prevents resource leaks and ensures clean teardown. The design uses a **single shared waitgroup** that is passed through all layers.

### Core Principle: Single Shared WaitGroup

Unlike designs with hierarchical waitgroups (each layer having its own), this design uses a **single waitgroup created in `main.go`** that is passed to all components. Each component that starts a goroutine calls `wg.Add(1)` and each goroutine calls `defer wg.Done()` when it exits.

**Benefits:**
- Simple to reason about - one place to wait for everything
- No complex parent/child waitgroup coordination
- Clean exit: `wg.Wait()` blocks until all goroutines exit
- Timeout safety: wrap `wg.Wait()` with timeout to prevent indefinite blocking

### WaitGroup Rules

1. **Caller calls `Add(1)` before starting goroutine**: Always increment waitgroup BEFORE `go func()`, never inside the goroutine
2. **Goroutine calls `defer wg.Done()` first thing**: Ensures Done() is called even if goroutine panics
3. **Library components do their own Add/Done**: When Listener/Dialer starts, it calls `wg.Add(1)`. When it closes, it calls `wg.Done()`
4. **Never call Done() without corresponding Add()**: This causes panic with negative counter
5. **Add() happens at creation time, Done() at cleanup time**: Consistent pattern throughout

### Pattern Examples

**Pattern 1: Application-Level Goroutine (main.go)**
```go
// Correct: Add before goroutine, Done in defer
wg.Add(1)
go func() {
    defer wg.Done()  // First line inside goroutine

    for {
        select {
        case <-ctx.Done():
            return  // Done() called via defer
        case <-ticker.C:
            // ... do work ...
        }
    }
}()
```

**Pattern 2: Library Component (Listener/Dialer)**
```go
// In Listen() function:
func Listen(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup) (Listener, error) {
    // ... setup code ...

    // Add to waitgroup early, before any code that might call Close()
    if shutdownWg != nil {
        shutdownWg.Add(1)  // Listener claims a spot in the waitgroup
    }

    // ... rest of initialization ...
    return ln, nil
}

// In listener.Close():
func (ln *listener) Close() {
    ln.shutdownOnce.Do(func() {
        // ... cleanup code ...

        // Notify waitgroup - listener is done
        if ln.shutdownWg != nil {
            ln.shutdownWg.Done()
        }
    })
}
```

**Pattern 3: Nested Components (Connection inside Listener)**
```go
// When Listener creates a Connection:
func (ln *listener) acceptConnection(...) {
    // Increment waitgroup for the new connection
    ln.connWg.Add(1)  // Internal waitgroup for connections

    conn := newSRTConn(...)
    // Connection will call ln.connWg.Done() when it closes
}

// In listener.Close():
func (ln *listener) Close() {
    // First, close all connections
    ln.conns.Range(func(key, value interface{}) bool {
        if value != nil {
            conn := value.(*srtConn)
            conn.close()  // Each calls connWg.Done()
        }
        return true
    })

    // Wait for all connections to finish
    ln.connWg.Wait()

    // Now notify parent waitgroup
    if ln.shutdownWg != nil {
        ln.shutdownWg.Done()
    }
}
```

### WaitGroup Flow: Startup to Shutdown

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              STARTUP PHASE                                   │
└─────────────────────────────────────────────────────────────────────────────┘

main()
  │
  ├── var wg sync.WaitGroup                    // Create single waitgroup
  │
  ├── Prometheus HTTP Server (if enabled)
  │     ├── wg.Add(1)                          // [wg counter: 1]
  │     └── go func() { defer wg.Done(); promSrv.ListenAndServe() }
  │
  ├── Logger goroutine (if enabled)
  │     ├── wg.Add(1)                          // [wg counter: 2]
  │     └── go func() { defer wg.Done(); for m := range Logger.Listen() {...} }
  │
  ├── Statistics ticker (if enabled)
  │     ├── wg.Add(1)                          // [wg counter: 3]
  │     └── go func() { defer wg.Done(); ticker loop with ctx.Done() check }
  │
  ├── SRT Server
  │     │
  │     ├── wg.Add(1)                          // [wg counter: 4]
  │     └── go func() { defer wg.Done(); server.ListenAndServe() }
  │                │
  │                └── server.Listen()
  │                      │
  │                      └── srt.Listen(ctx, ..., &wg)
  │                            │
  │                            └── Listener starts
  │                                  ├── wg.Add(1)           // [wg counter: 5]
  │                                  └── Starts internal goroutines
  │                                        ├── recvCompletionHandler (io_uring)
  │                                        └── reader goroutine
  │
  └── <-ctx.Done()                             // Block until signal


┌─────────────────────────────────────────────────────────────────────────────┐
│                              RUNTIME PHASE                                   │
│                    (Connections come and go)                                 │
└─────────────────────────────────────────────────────────────────────────────┘

When a client connects:
  │
  └── Listener.Accept() creates new connection
        │
        └── Connection starts
              ├── ln.connWg.Add(1)             // Connection tracked by listener
              └── Connection starts goroutines:
                    ├── networkQueueReader
                    ├── writeQueueReader
                    ├── ticker
                    └── sendCompletionHandler (io_uring)

When a client disconnects normally:
  │
  └── connection.close()
        ├── Cancel connection context
        ├── Send shutdown packet
        ├── Wait for connection goroutines (internal connWg)
        └── ln.connWg.Done()                   // Notify listener


┌─────────────────────────────────────────────────────────────────────────────┐
│                              SHUTDOWN PHASE                                  │
│                    (Signal received - ctx.Done() closes)                     │
└─────────────────────────────────────────────────────────────────────────────┘

Signal (SIGINT/SIGTERM) received
  │
  ▼
ctx.Done() closes (all goroutines detect this)
  │
  ├─────────────────────────────────────────────────────┐
  │                                                     │
  ▼                                                     ▼
Prometheus shutdown watcher                   SRT Serve() detects ctx.Done()
sees <-ctx.Done()                             │
  │                                           ▼
  ▼                                         calls server.Shutdown()
calls promSrv.Shutdown()                      │
  │                                           ▼
  ▼                                         listener.Close()
promSrv.ListenAndServe() returns              │
http.ErrServerClosed                          ├── Close all connections
  │                                           │     └── Each conn.close():
  ▼                                           │           ├── Send shutdown packet
wg.Done()  [wg counter: 4]                    │           ├── Wait for conn goroutines
  │                                           │           └── connWg.Done()
  │                                           │
  │                                           ├── Wait for connWg (all connections)
  │                                           │
  │                                           ├── Wait for recvCompWg (io_uring)
  │                                           │
  │                                           └── wg.Done()  [wg counter: 3]
  │                                                     │
  │                                                     ▼
  │                                           server.ListenAndServe() returns
  │                                           ErrServerClosed
  │                                                     │
  │                                                     ▼
  │                                           wg.Done()  [wg counter: 2]
  │
  ├─────────────────────────────────────────────────────┘
  │
  ▼
main: <-ctx.Done() unblocks
  │
  ▼
config.Logger.Close()
(closes channel, logger goroutine exits)
  │
  ▼
Logger goroutine: for range ends
wg.Done()  [wg counter: 1]
  │
  ▼
Statistics ticker sees ctx.Done()
returns from loop
wg.Done()  [wg counter: 0]
  │
  ▼
wg.Wait() returns (counter is 0)
  │
  ▼
main() exits cleanly
```

### WaitGroup Counter Timeline (Example with All Components)

| Event | wg Counter | Notes |
|-------|------------|-------|
| `var wg sync.WaitGroup` | 0 | Initial state |
| Prometheus: `wg.Add(1)` | 1 | Before starting goroutine |
| Logger: `wg.Add(1)` | 2 | Before starting goroutine |
| Stats ticker: `wg.Add(1)` | 3 | Before starting goroutine |
| SRT Server: `wg.Add(1)` | 4 | Before starting ListenAndServe goroutine |
| Listener: `wg.Add(1)` | 5 | Inside srt.Listen() |
| **Signal received** | 5 | ctx.Done() closes |
| Prometheus goroutine exits: `wg.Done()` | 4 | After Shutdown() completes |
| Listener closes: `wg.Done()` | 3 | After all connections close |
| SRT Server goroutine exits: `wg.Done()` | 2 | After ListenAndServe returns |
| Logger goroutine exits: `wg.Done()` | 1 | After Logger.Close() closes channel |
| Stats ticker exits: `wg.Done()` | 0 | After detecting ctx.Done() |
| `wg.Wait()` returns | 0 | All goroutines exited |

### Library Internal WaitGroups

The SRT library uses **internal waitgroups** for tracking child components that are NOT exposed to the caller. These are separate from the shared `shutdownWg`:

```
Listener (internal waitgroups)
├── shutdownWg (*sync.WaitGroup)  // Passed from caller (main.go's wg)
├── connWg (sync.WaitGroup)       // Internal: tracks all connections
└── recvCompWg (sync.WaitGroup)   // Internal: tracks io_uring recv handler

Connection (internal waitgroups)
├── connWg (sync.WaitGroup)       // Internal: tracks connection goroutines
└── sendCompWg (sync.WaitGroup)   // Internal: tracks io_uring send handler
```

**Key Point**: Internal waitgroups ensure child components finish before parent calls `shutdownWg.Done()`.

### Connection Shutdown Sequence

When a connection closes (either from peer disconnect or signal):

```
conn.close() called
  │
  ├── c.shutdownOnce.Do(func() {         // Ensure single execution
  │
  ├── 1. Unregister from metrics
  │
  ├── 2. Print final statistics
  │
  ├── 3. Stop peer idle timeout
  │
  ├── 4. Send SRT shutdown packet to peer
  │       └── Creates CTRLTYPE_SHUTDOWN control packet
  │
  ├── 5. Cancel connection context (c.cancelCtx())
  │       └── All connection goroutines see <-ctx.Done()
  │             ├── networkQueueReader exits
  │             ├── writeQueueReader exits
  │             └── ticker exits
  │
  ├── 6. Wait for connection goroutines
  │       └── c.connWg.Wait() with timeout
  │
  ├── 7. Cleanup io_uring (if enabled)
  │       ├── Cancel send completion handler
  │       ├── c.sendCompWg.Wait()
  │       └── Drain remaining completions
  │
  ├── 8. Flush congestion control
  │       ├── c.snd.Flush()
  │       └── c.recv.Flush()
  │
  └── 9. Notify parent (callback or waitgroup)
          └── Listener's connWg.Done()
```

### Anti-Patterns to Avoid

**❌ Incorrect: Add() inside goroutine**
```go
go func() {
    wg.Add(1)      // WRONG: Race condition - main might call Wait() before this runs
    defer wg.Done()
    // ...
}()
```

**❌ Incorrect: Calling Done() without Add()**
```go
// If ShutdownWg is nil or Add() was never called...
func (s *Server) Shutdown() {
    s.ShutdownWg.Done()  // PANIC: negative WaitGroup counter
}
```

**❌ Incorrect: Not waiting for children before Done()**
```go
func (ln *listener) Close() {
    ln.shutdownWg.Done()  // WRONG: Called before connections finish
    // Now wait for connections...
    ln.connWg.Wait()
}
```

**✅ Correct: Proper order**
```go
func (ln *listener) Close() {
    // First: close and wait for all children
    ln.conns.Range(func(k, v interface{}) bool {
        v.(*srtConn).close()
        return true
    })
    ln.connWg.Wait()

    // Then: notify parent
    if ln.shutdownWg != nil {
        ln.shutdownWg.Done()
    }
}
```

### Timeout Safety Net

The final `wg.Wait()` is wrapped with a timeout to prevent indefinite blocking if something is stuck:

```go
done := make(chan struct{})
go func() {
    wg.Wait()
    close(done)
}()

select {
case <-done:
    fmt.Fprintf(os.Stderr, "Graceful shutdown complete\n")
case <-time.After(config.ShutdownDelay):
    fmt.Fprintf(os.Stderr, "Shutdown timed out after %s\n", config.ShutdownDelay)
    // Exit anyway - some goroutines may still be running
}
```

**Timeout Behavior**:
- Default: 5 seconds (`config.ShutdownDelay`)
- If all goroutines exit before timeout: exits immediately with success message
- If timeout expires: exits with warning (some goroutines may still be running)

---

## Implementation: Server (`contrib/server/main.go`)

### Complete Implementation

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    "os"
    "os/signal"
    "sync"
    "syscall"
    "time"

    srt "github.com/datarhei/gosrt"
    "github.com/datarhei/gosrt/contrib/common"
    "github.com/datarhei/gosrt/metrics"
)

func main() {
    // ... flag parsing and config setup ...

    // Create context that cancels on signal (replaces setupSignalHandler)
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    // Single waitgroup for all goroutines
    var wg sync.WaitGroup

    // ============================================================
    // Start Prometheus Metrics HTTP Server (if enabled)
    // ============================================================
    if *metricsEnabled {
        promSrv := &http.Server{
            Addr:    *metricsListenAddr,
            Handler: metrics.NewHTTPHandler(),
        }

        // Shutdown watcher - cleanly shuts down Prometheus server when context cancelled
        go func() {
            <-ctx.Done()
            shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            if err := promSrv.Shutdown(shutdownCtx); err != nil {
                fmt.Fprintf(os.Stderr, "Prometheus server shutdown error: %v\n", err)
            }
        }()

        // Run Prometheus server
        wg.Add(1)
        go func() {
            defer wg.Done()
            fmt.Fprintf(os.Stderr, "Prometheus server started on %s\n", *metricsListenAddr)
            if err := promSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                fmt.Fprintf(os.Stderr, "Prometheus server error: %v\n", err)
            }
        }()
    }

    // ============================================================
    // Start Logger Goroutine (if enabled)
    // ============================================================
    if config.Logger != nil {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for m := range config.Logger.Listen() {
                fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n",
                    m.SocketId, m.Topic, m.File, m.Line, m.Message)
            }
        }()
    }

    // ============================================================
    // Start Statistics Ticker (if enabled)
    // ============================================================
    if config.StatisticsPrintInterval > 0 {
        wg.Add(1)
        go func() {
            defer wg.Done()
            ticker := time.NewTicker(config.StatisticsPrintInterval)
            defer ticker.Stop()

            for {
                select {
                case <-ctx.Done():
                    return
                case <-ticker.C:
                    connections := s.server.GetConnections()
                    common.PrintConnectionStatistics(connections,
                        config.StatisticsPrintInterval.String(), nil)
                }
            }
        }()
    }

    // ============================================================
    // Setup and Start SRT Server
    // ============================================================
    s.server = &srt.Server{
        Addr:            s.addr,
        HandleConnect:   s.handleConnect,
        HandlePublish:   s.handlePublish,
        HandleSubscribe: s.handleSubscribe,
        Config:          &config,
        Context:         ctx,  // Serve() watches this and calls Shutdown() when cancelled
        ShutdownWg:      &wg,  // Listener adds/decrements to this
    }

    // Run SRT server
    wg.Add(1)
    go func() {
        defer wg.Done()
        fmt.Fprintf(os.Stderr, "Listening on %s\n", s.addr)
        if err := s.ListenAndServe(); err != nil && err != srt.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "SRT Server: %s\n", err)
            os.Exit(2)
        }
    }()

    // ============================================================
    // Wait for Shutdown Signal
    // ============================================================
    <-ctx.Done()
    fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")

    // ============================================================
    // Cleanup
    // ============================================================
    // Close logger so its goroutine can exit (channel will close)
    if config.Logger != nil {
        config.Logger.Close()
    }

    // ============================================================
    // Wait for All Goroutines with Timeout
    // ============================================================
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        fmt.Fprintf(os.Stderr, "Graceful shutdown complete\n")
    case <-time.After(config.ShutdownDelay):
        fmt.Fprintf(os.Stderr, "Shutdown timed out after %s\n", config.ShutdownDelay)
    }
}
```

### Changes from Current Implementation

| Current | New |
|---------|-----|
| `setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay)` | `ctx, stop := signal.NotifyContext(...)` |
| `startMetricsServer(*metricsListenAddr, ctx)` | Inline HTTP server with shutdown watcher |
| `shutdownWg.Add(1)` (spurious, never decremented) | Removed |
| Logger goroutine not tracked | `wg.Add(1)` + `defer wg.Done()` |
| Statistics ticker not tracked, no ctx check | `wg.Add(1)` + `defer wg.Done()` + ctx check |
| ListenAndServe goroutine not tracked | `wg.Add(1)` + `defer wg.Done()` |
| Wait for shutdownWg immediately | Wait for `<-ctx.Done()` first |

---

## Implementation: Client (`contrib/client/main.go`)

### Key Changes

```go
func main() {
    // ... flag parsing and config setup ...

    // Create context that cancels on signal
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    // Single waitgroup for all goroutines
    var wg sync.WaitGroup

    // Start Prometheus server if enabled (same pattern as server)
    if *metricsEnabled {
        promSrv := &http.Server{
            Addr:    *metricsListenAddr,
            Handler: metrics.NewHTTPHandler(),
        }

        go func() {
            <-ctx.Done()
            shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            promSrv.Shutdown(shutdownCtx)
        }()

        wg.Add(1)
        go func() {
            defer wg.Done()
            if err := promSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
                fmt.Fprintf(os.Stderr, "Prometheus server error: %v\n", err)
            }
        }()
    }

    // Open reader and writer with context (ctx is first argument per Go idiom)
    r, err := openReader(ctx, *from, logger, &wg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: from: %s\n", err)
        os.Exit(1)
    }
    defer r.Close()

    w, err := openWriter(ctx, *to, logger, &wg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: to: %s\n", err)
        os.Exit(1)
    }
    defer w.Close()

    // Statistics ticker (if enabled)
    if config.StatisticsPrintInterval > 0 {
        wg.Add(1)
        go func() {
            defer wg.Done()
            ticker := time.NewTicker(config.StatisticsPrintInterval)
            defer ticker.Stop()
            for {
                select {
                case <-ctx.Done():
                    return
                case <-ticker.C:
                    // Print stats
                }
            }
        }()
    }

    // Main read/write loop with context checking
    buffer := make([]byte, 2048)
    doneChan := make(chan error, 1)

    wg.Add(1)
    go func() {
        defer wg.Done()
        for {
            select {
            case <-ctx.Done():
                doneChan <- nil
                return
            default:
            }

            // Set read deadline for periodic context checks
            if conn, ok := r.(interface{ SetReadDeadline(time.Time) error }); ok {
                conn.SetReadDeadline(time.Now().Add(2 * time.Second))
            }

            n, err := r.Read(buffer)
            if err != nil {
                if errors.Is(err, os.ErrDeadlineExceeded) {
                    continue // Timeout - check context and retry
                }
                if errors.Is(err, io.EOF) || isConnectionClosed(err) {
                    doneChan <- nil
                    return
                }
                doneChan <- err
                return
            }

            if _, err := w.Write(buffer[:n]); err != nil {
                doneChan <- err
                return
            }
        }
    }()

    // Wait for completion or context cancellation
    select {
    case err := <-doneChan:
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        }
    case <-ctx.Done():
        fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")
    }

    // Wait for all goroutines with timeout
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Graceful shutdown complete
    case <-time.After(config.ShutdownDelay):
        fmt.Fprintf(os.Stderr, "Shutdown timed out\n")
    }
}
```

---

## Implementation: Client-Generator (`contrib/client-generator/main.go`)

### Key Changes

Same pattern as client, but simpler (only writes, no reads):

```go
func main() {
    // Create context that cancels on signal
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    var wg sync.WaitGroup

    // Start metrics server if enabled (same pattern)
    // ...

    // Open writer with context (ctx is first argument per Go idiom)
    w, err := openWriter(ctx, *to, logger, &wg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %s\n", err)
        os.Exit(1)
    }
    defer w.Close()

    // Create data generator
    generator := newDataGenerator(*bitrate)

    // Statistics ticker (if enabled)
    // ...

    // Main write loop
    buffer := make([]byte, 1316)
    doneChan := make(chan error, 1)

    wg.Add(1)
    go func() {
        defer wg.Done()
        for {
            select {
            case <-ctx.Done():
                doneChan <- nil
                return
            default:
            }

            n, err := generator.Read(buffer)
            if err != nil {
                doneChan <- err
                return
            }

            if _, err := w.Write(buffer[:n]); err != nil {
                if isConnectionClosed(err) {
                    doneChan <- nil
                    return
                }
                doneChan <- err
                return
            }
        }
    }()

    // Wait for completion or context cancellation
    select {
    case err := <-doneChan:
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        }
    case <-ctx.Done():
        fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")
    }

    // Wait for all goroutines with timeout
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // Graceful shutdown complete
    case <-time.After(config.ShutdownDelay):
        fmt.Fprintf(os.Stderr, "Shutdown timed out\n")
    }
}
```

---

## Implementation Plan

### Phase 1: Server Updates

**Files to modify:** `contrib/server/main.go`

**Tasks:**
1. [ ] Replace `setupSignalHandler()` with `signal.NotifyContext`
2. [ ] Remove `setupSignalHandler()` function
3. [ ] Refactor `startMetricsServer()` to inline HTTP server with shutdown watcher pattern
4. [ ] Remove spurious `shutdownWg.Add(1)` that was never decremented
5. [ ] Add `wg.Add(1)` + `defer wg.Done()` to logger goroutine
6. [ ] Add `wg.Add(1)` + `defer wg.Done()` to statistics ticker goroutine
7. [ ] Add context cancellation check to statistics ticker loop
8. [ ] Add `wg.Add(1)` + `defer wg.Done()` to ListenAndServe goroutine
9. [ ] Add `<-ctx.Done()` before waitgroup wait
10. [ ] Move `config.Logger.Close()` before waitgroup wait
11. [ ] Update final wait to use single waitgroup with timeout

**Estimated effort:** 2-3 hours

### Phase 2: Client Updates

**Files to modify:** `contrib/client/main.go`

**Tasks:**
1. [ ] Replace `setupSignalHandler()` with `signal.NotifyContext`
2. [ ] Remove `setupSignalHandler()` function
3. [ ] Refactor metrics server to use shutdown watcher pattern
4. [ ] Add `wg.Add(1)` + `defer wg.Done()` to statistics ticker goroutine
5. [ ] Add context cancellation check to statistics ticker loop
6. [ ] Add `wg.Add(1)` + `defer wg.Done()` to main read/write loop goroutine
7. [ ] Update final wait to use single waitgroup with timeout
8. [ ] Ensure `SetReadDeadline` pattern is used for context checking in read loop

**Estimated effort:** 2-3 hours

### Phase 3: Client-Generator Updates

**Files to modify:** `contrib/client-generator/main.go`

**Tasks:**
1. [ ] Replace `setupSignalHandler()` with `signal.NotifyContext`
2. [ ] Remove `setupSignalHandler()` function
3. [ ] Refactor metrics server to use shutdown watcher pattern
4. [ ] Add `wg.Add(1)` + `defer wg.Done()` to statistics ticker goroutine
5. [ ] Add context cancellation check to statistics ticker loop
6. [ ] Add `wg.Add(1)` + `defer wg.Done()` to main write loop goroutine
7. [ ] Update final wait to use single waitgroup with timeout

**Estimated effort:** 1-2 hours

### Phase 4: NewServer Constructor Function

**Files to modify:** `server.go`, `contrib/server/main.go`

**Tasks:**

1. [ ] Create `ServerConfig` struct in `server.go`
2. [ ] Create `NewServer(ctx context.Context, wg *sync.WaitGroup, config ServerConfig) *Server` function
3. [ ] Move default handler setup from `Listen()` to `NewServer()`
4. [ ] Move default config setup from `Listen()` to `NewServer()`
5. [ ] Update `contrib/server/main.go` to use `srt.NewServer()`
6. [ ] Remove direct struct literal initialization in `contrib/server/main.go`
7. [ ] Update `doc.go` example to use `NewServer()`
8. [ ] Verify existing tests still pass (server struct can still be created directly for backward compat)

**Notes:**
- Keep the existing `Server` struct fields public for backward compatibility
- The `NewServer` function is the preferred way to create a server
- Direct struct literal initialization still works but is not recommended

**Estimated effort:** 1-2 hours

---

### Phase 5: Context Argument Ordering (Go Idiom)

**Go Convention**: When a function accepts a `context.Context`, it should be the **first argument**.

> "Do not store Contexts inside a struct type; instead, pass a Context explicitly to each function that needs it. The Context should be the first parameter, typically named ctx" - [Go Blog: Context](https://go.dev/blog/context)

**Functions that need context moved to first argument:**

| File | Current Signature | New Signature |
|------|-------------------|---------------|
| `dial.go:96` | `Dial(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup)` | `Dial(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup)` |
| `listen.go:179` | `Listen(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup)` | `Listen(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup)` |

**Call sites that need updating for `Dial()`:**

| File | Line | Notes |
|------|------|-------|
| `dial.go` | 93 | Doc comment example |
| `dial_test.go` | 22 | `testDial` helper function |
| `dial_test.go` | 458 | Direct call |
| `listen_test.go` | 117 | Direct call |
| `server_test.go` | 49, 58, 67 | Direct calls |
| `doc.go` | 12 | Package documentation example |
| `contrib/client/main.go` | 540, 638 | `openReader`/`openWriter` functions |
| `contrib/client-generator/main.go` | 324 | `openWriter` function |

**Call sites that need updating for `Listen()`:**

| File | Line | Notes |
|------|------|-------|
| `listen.go` | 176 | Doc comment example |
| `listen_test.go` | 23 | `testListen` helper function |
| `doc.go` | 37 | Package documentation example |
| `server.go` | 82 | `Server.Listen()` method |
| `contrib/client/main.go` | 509, 607 | `openReader`/`openWriter` functions |

**Tasks:**
1. [ ] Update `Dial()` signature in `dial.go` - move `ctx` to first argument
2. [ ] Update `Dial()` doc comment example in `dial.go`
3. [ ] Update `Listen()` signature in `listen.go` - move `ctx` to first argument
4. [ ] Update `Listen()` doc comment example in `listen.go`
5. [ ] Update `testDial()` helper in `dial_test.go`
6. [ ] Update all `Dial()` calls in `dial_test.go`
7. [ ] Update `testListen()` helper in `listen_test.go`
8. [ ] Update all `Dial()` and `Listen()` calls in `listen_test.go`
9. [ ] Update all `Dial()` calls in `server_test.go`
10. [ ] Update `Server.Listen()` call in `server.go`
11. [ ] Update `srt.Dial()` example in `doc.go`
12. [ ] Update `srt.Listen()` example in `doc.go`
13. [ ] Update `openReader()` function signature in `contrib/client/main.go` (ctx first)
14. [ ] Update `openWriter()` function signature in `contrib/client/main.go` (ctx first)
15. [ ] Update all `srt.Dial()` and `srt.Listen()` calls in `contrib/client/main.go`
16. [ ] Update `openWriter()` function signature in `contrib/client-generator/main.go` (ctx first)
17. [ ] Update `srt.Dial()` call in `contrib/client-generator/main.go`

**Estimated effort:** 1-2 hours

---

### Phase 6: Testing and Verification

**Tasks:**
1. [ ] Run `make build` to verify all components compile
2. [ ] Run `make test` to verify unit tests pass
3. [ ] Run `make test-integration` to verify graceful shutdown works
4. [ ] Verify metrics endpoint stays alive during entire test
5. [ ] Verify all goroutines exit cleanly (no timeout messages)
6. [ ] Verify exit codes are 0 for all components

**Estimated effort:** 1-2 hours

### Phase 7: Documentation Updates

**Tasks:**
1. [ ] Update `context_and_cancellation_implementation.md` with new implementation notes
2. [ ] Update `test_1.1_implementation.md` with verification results
3. [ ] Archive or note deprecation of original design in `context_and_cancellation_design.md`

**Estimated effort:** 1 hour

---

## Total Estimated Effort

| Phase | Effort |
|-------|--------|
| Phase 1: Server Updates | 2-3 hours |
| Phase 2: Client Updates | 2-3 hours |
| Phase 3: Client-Generator Updates | 1-2 hours |
| Phase 4: NewServer Constructor | 1-2 hours |
| Phase 5: Context Argument Ordering | 1-2 hours |
| Phase 6: Testing | 1-2 hours |
| Phase 7: Documentation | 1 hour |
| **Total** | **10-16 hours** |

---

## Backward Compatibility

### Library API Changes (Breaking)

**1. Function Signature Changes (context first):**

```go
// Before
func Dial(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup) (Conn, error)
func Listen(network, address string, config Config, ctx context.Context, shutdownWg *sync.WaitGroup) (Listener, error)

// After
func Dial(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup) (Conn, error)
func Listen(ctx context.Context, network, address string, config Config, shutdownWg *sync.WaitGroup) (Listener, error)
```

**Impact**: Any external code using `srt.Dial()` or `srt.Listen()` will need to update the argument order.

**Migration**: Move the `ctx` argument from 4th position to 1st position.

**2. New Server Constructor:**

```go
// New (recommended)
func NewServer(ctx context.Context, wg *sync.WaitGroup, config ServerConfig) *Server

// Old (still works for backward compatibility)
s := &srt.Server{
    Addr: addr,
    // ... other fields
}
```

**Impact**: None - existing code using struct literal initialization still works.

**Migration**: Recommended to use `NewServer()` for cleaner code and proper defaults.

### Internal Library Changes

- `server.go`: Add `ServerConfig` struct and `NewServer()` constructor
- `server.go`: Update `Listen()` call in `Server.Listen()` method
- `doc.go`: Update package documentation examples

### Config Changes

No changes to `config.go`. The `ShutdownDelay` field is still used for the final timeout safety net.

### CLI Flag Changes

No changes to CLI flags. The `--shutdowndelay` flag still works as before.

---

---

## Bug Found: Write After Close Race Condition (BUG-001)

**Date Found:** 2025-12-29
**Severity:** HIGH
**File:** `connection.go`

### The Problem

A race condition was discovered where `conn.Write()` could return success (`nil`) even after `conn.Close()` was called.

### Root Cause

Go's `select` statement is **non-deterministic** when multiple channels are ready. The original code checked `ctx.Done()` and the write channel in the same select:

```go
// BUGGY CODE - DO NOT USE
select {
case <-c.ctx.Done():       // (A) Context cancelled
    return 0, io.EOF
case c.writeQueue <- p:    // (B) Write to queue
default:
    return 0, io.EOF
}
```

**Race scenario:**
1. Thread 1 calls `Write()`
2. Thread 2 calls `Close()` → cancels `c.ctx`
3. Both `ctx.Done()` and `writeQueue` become ready
4. Go randomly chooses (B) → Write succeeds after Close!

### The Fix

Check context cancellation **FIRST**, before any other operations:

```go
// CORRECT - Check context FIRST
func (c *srtConn) Write(b []byte) (int, error) {
    // Check context cancellation FIRST, before any operations.
    // This follows the context_and_cancellation_new_design.md pattern where
    // context cancellation signals shutdown.
    select {
    case <-c.ctx.Done():
        return 0, io.EOF
    default:
    }

    // Now proceed with write operations...
    c.writeBuffer.Write(b)
    // ...
}
```

### Why This Aligns With The Design

This fix follows the design principles in this document:

1. **Uses context for cancellation** - No additional atomics or mutexes needed
2. **Respects shutdown hierarchy** - When context cancels, operations return errors
3. **Clean shutdown flow** - Write returns `io.EOF`, caller knows connection is closed

### Shutdown Flow With Fix

```
Close() called
    │
    ▼
c.cancel() cancels context
    │
    ▼
c.ctx.Done() channel closes
    │
    ├──► Write() checks ctx.Done() FIRST → returns io.EOF immediately
    │
    ├──► writeLoop checks ctx.Done() → stops sending packets
    │
    └──► Other goroutines detect ctx.Done() → clean exit via waitgroup
```

### Verification

The fix was verified by running the test 10 times:
- **Before fix:** 5/10 passes (flaky)
- **After fix:** 10/10 passes (consistent)

### Key Lesson

**Never combine context cancellation check with other channel operations in a single select when the context check must take priority.** Always check `ctx.Done()` first in a separate select with a `default` case.

---

## Summary

This improved design simplifies context and cancellation handling by:

1. **Using `signal.NotifyContext`** - One line replaces complex manual signal handling
2. **Using explicit shutdown watchers** - Each HTTP server has a goroutine that triggers clean shutdown
3. **Using a single waitgroup** - All goroutines tracked in one place
4. **Clear shutdown flow** - Linear, easy-to-follow shutdown sequence
5. **Context as first argument** - Follows Go convention for all functions accepting context
6. **NewServer constructor** - Follows Go idiom of `New*` functions for object creation
7. **Check ctx.Done() first** - Always check context cancellation before other channel operations

The result is cleaner, more maintainable code that follows Go idioms and ensures all goroutines are properly tracked and shut down gracefully.

