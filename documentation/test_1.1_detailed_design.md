# Test 1.1: Graceful Shutdown on SIGINT - Detailed Design

## Overview

This document provides a detailed design for Test 1.1: Graceful Shutdown on SIGINT. This test verifies that all three GoSRT components (client-generator, server, client) can gracefully shutdown when receiving SIGINT signals, following the context and cancellation design principles.

## Test Architecture

### Three GoSRT Instances

#### 1. Client-Generator (Simulated Video Source)
**Role**: Simulated video source that publishes an SRT stream to the server

**Responsibilities**:
- Generate fake MPEG-TS packets at a configurable bitrate (e.g., 1 Mb/s, 5 Mb/s, 10 Mb/s)
- Connect to the server as a "caller" (publisher)
- Publish the generated stream to the server
- Handle shutdown signals gracefully
- Exit cleanly when context is cancelled

**Key Requirements**:
- Must generate MPEG-TS packets at the specified bitrate
- Must connect to server in "caller" mode with correct stream ID
- Must handle SIGINT/SIGTERM signals
- Must gracefully shutdown when context is cancelled
- Must close SRT connection cleanly
- Must exit within shutdown delay

#### 2. Server Instance
**Role**: SRT server that accepts connections and routes streams

**Responsibilities**:
- Start first and listen for incoming connections
- Accept connection from client-generator (publisher)
- Accept connection from client (subscriber)
- Route published stream to subscriber
- Handle shutdown signals gracefully
- Close all connections cleanly on shutdown

**Key Requirements**:
- Must start before client-generator and client
- Must accept publish connection from client-generator
- Must accept subscribe connection from client
- Must route published stream to subscriber
- Must handle SIGINT/SIGTERM signals (primary test focus)
- Must gracefully shutdown all connections
- Must exit within shutdown delay

#### 3. Client Instance (Subscriber)
**Role**: SRT client that subscribes to and receives the stream from the server

**Responsibilities**:
- Connect to the server as a "caller" (subscriber)
- Subscribe to the stream published by client-generator
- Receive and process packets from the server
- Handle shutdown signals gracefully
- Exit cleanly when context is cancelled

**Key Requirements**:
- Must connect to server in "caller" mode with correct stream ID
- Must receive packets from the published stream
- Must handle SIGINT/SIGTERM signals
- Must gracefully shutdown when context is cancelled
- Must close SRT connection cleanly
- Must exit within shutdown delay

---

## Test Phases

### Phase 1: Startup Phase

**Order**: Server → Client-Generator → Client

#### 1.1 Server Startup
1. Start server process
2. Server binds to address (e.g., `127.0.0.1:6000`)
3. Server creates root context and waitgroup
4. Server sets up signal handler
5. Server starts listening for connections
6. Server is ready to accept connections

**Verification**:
- Server process is running
- Server is listening on the configured address
- Server has root context and signal handler set up

#### 1.2 Client-Generator Startup
1. Start client-generator process
2. Client-generator creates root context and waitgroup
3. Client-generator sets up signal handler
4. Client-generator connects to server (caller mode, publisher)
5. Client-generator starts generating MPEG-TS packets at configured bitrate
6. Client-generator begins publishing stream to server

**Verification**:
- Client-generator process is running
- Client-generator has established SRT connection to server
- Client-generator is generating packets at correct bitrate
- Server has accepted the publish connection

#### 1.3 Client Startup
1. Start client process
2. Client creates root context and waitgroup
3. Client sets up signal handler
4. Client connects to server (caller mode, subscriber)
5. Client subscribes to the stream published by client-generator
6. Client begins receiving packets from server

**Verification**:
- Client process is running
- Client has established SRT connection to server
- Client is receiving packets from the published stream
- Server has accepted the subscribe connection

**Startup Complete Criteria**:
- All three processes are running
- Server has two active connections (publish and subscribe)
- Client-generator is publishing packets
- Client is receiving packets
- Stream is flowing: client-generator → server → client

---

### Phase 2: Run Phase

**Duration**: Configurable (e.g., 10 seconds)

**Activities**:
- Client-generator continues generating and publishing packets
- Server continues routing packets from publisher to subscriber
- Client continues receiving and processing packets
- All processes monitor for context cancellation

**Verification**:
- Packets are flowing through the system
- Statistics show active data transfer
- No errors or connection drops
- All processes remain responsive

**Run Phase Complete Criteria**:
- Stream has been active for the configured duration
- All processes are still running
- No connection errors or drops
- Ready to proceed to shutdown phase

---

### Phase 3: Shutdown Phase

**Order**: Client → Client-Generator → Server

**Rationale**: Shutdown in reverse order of startup to ensure:
1. Subscriber disconnects first (cleanest shutdown)
2. Publisher disconnects second (server can clean up publish connection)
3. Server shuts down last (can clean up all resources)

#### 3.1 Client Shutdown
1. Send SIGINT to client process
2. Client's signal handler receives SIGINT
3. Signal handler cancels root context
4. Client's read/write goroutines detect context cancellation
5. Read/write goroutines exit gracefully
6. Client closes SRT connection (reader and writer)
7. Dialer detects connection close and calls `shutdownWg.Done()`
8. Client's main goroutine exits
9. Client process exits

**Verification**:
- Client receives SIGINT signal
- Client's root context is cancelled
- Client's read/write goroutines exit
- Client's SRT connection is closed cleanly
- Client sends shutdown packet to server (if connection still active)
- Client process exits with code 0
- Client exits within shutdown delay

**Expected Behavior**:
- No "connection refused" errors (connection closed gracefully)
- No goroutine leaks
- Clean exit

#### 3.2 Client-Generator Shutdown
1. Send SIGINT to client-generator process
2. Client-generator's signal handler receives SIGINT
3. Signal handler cancels root context
4. Client-generator's write goroutine detects context cancellation
5. Write goroutine exits gracefully
6. Client-generator closes SRT connection (writer)
7. Dialer detects connection close and calls `shutdownWg.Done()`
8. Client-generator's main goroutine exits
9. Client-generator process exits

**Verification**:
- Client-generator receives SIGINT signal
- Client-generator's root context is cancelled
- Client-generator's write goroutine exits
- Client-generator's SRT connection is closed cleanly
- Client-generator sends shutdown packet to server (if connection still active)
- Client-generator process exits with code 0
- Client-generator exits within shutdown delay

**Expected Behavior**:
- No "connection refused" errors (connection closed gracefully)
- No goroutine leaks
- Clean exit

#### 3.3 Server Shutdown
1. Send SIGINT to server process
2. Server's signal handler receives SIGINT
3. Signal handler cancels root context
4. Server's `Serve()` detects context cancellation
5. Server calls `Shutdown()`
6. Server closes listener
7. Listener closes all connections (publish and subscribe)
8. All connections send shutdown packets
9. All connection goroutines exit
10. Listener waits for all connections to close
11. Listener notifies server waitgroup
12. Server's main goroutine exits
13. Server process exits

**Verification**:
- Server receives SIGINT signal
- Server's root context is cancelled
- Server's `Serve()` detects cancellation and calls `Shutdown()`
- All connections are closed cleanly
- All connections send shutdown packets
- All connection goroutines exit
- Server process exits with code 0
- Server exits within shutdown delay

**Expected Behavior**:
- All connections closed gracefully
- Shutdown packets sent to all peers
- No goroutine leaks
- Clean exit

---

## Component Requirements

### Client-Generator Requirements

#### Startup Requirements
1. **Root Context Creation**:
   ```go
   ctx, cancel := context.WithCancel(context.Background())
   defer cancel()
   ```

2. **Root WaitGroup Creation**:
   ```go
   var shutdownWg sync.WaitGroup
   ```

3. **Signal Handler Setup**:
   ```go
   setupSignalHandler(ctx, cancel)
   ```
   - Must handle SIGINT and SIGTERM
   - Must cancel root context when signal received
   - Must not block (runs in separate goroutine)

4. **SRT Connection Setup**:
   - Must use `srt.Dial()` to connect to server
   - Must pass root context and waitgroup to `Dial()`
   - Must set stream ID via config (from URL query parameter)
   - Must use "caller" mode (publisher)
   - Must increment waitgroup after connection is established

5. **MPEG-TS Packet Generation**:
   - Must generate fake MPEG-TS packets (188 bytes each)
   - Must generate at configurable bitrate (e.g., 1, 5, 10 Mb/s)
   - Must generate packets continuously until context is cancelled
   - Must use a data generator that respects context cancellation

#### Shutdown Requirements
1. **Signal Handling**:
   - Must receive SIGINT/SIGTERM signals
   - Must cancel root context when signal received
   - Must not block signal handler

2. **Goroutine Exit**:
   - Write goroutine must check `ctx.Done()` in main loop
   - Write goroutine must exit when context is cancelled
   - Write goroutine must handle connection errors gracefully
   - Stats ticker goroutine must exit when context is cancelled

3. **Connection Cleanup**:
   - Must close SRT connection when context is cancelled
   - Must not wait for waitgroup (exit immediately)
   - Must handle connection close errors gracefully

4. **Process Exit**:
   - Must exit when read/write goroutines exit
   - Must exit when context is cancelled
   - Must exit within shutdown delay
   - Must exit with code 0 (success)

#### Current Implementation Status
- ✅ Root context and waitgroup created
- ✅ Signal handler set up
- ✅ SRT connection established with context and waitgroup
- ✅ Write goroutine checks context cancellation
- ✅ Stats ticker respects context cancellation
- ⚠️ **Issue**: Main goroutine waits on waitgroup, which may block if connection doesn't close properly
- ⚠️ **Issue**: Connection close detection may be delayed (SetReadDeadline with checkConnection() should help)

#### Required Fixes
1. **Remove Waitgroup Wait from Main Shutdown Path**:
   - When read/write goroutines exit, main should exit immediately
   - Waitgroup is only for explicit graceful shutdown scenarios
   - Current implementation already does this, but verify it works correctly

2. **Ensure Connection Close Detection**:
   - `checkConnection()` should detect server closes (called periodically via Read() with SetReadDeadline)
   - Connection should close when server sends shutdown packet
   - Connection context should be cancelled when connection closes

### Server Requirements

#### Startup Requirements
1. **Root Context Creation**:
   ```go
   ctx, cancel := context.WithCancel(context.Background())
   defer cancel()
   ```

2. **Root WaitGroup Creation**:
   ```go
   var shutdownWg sync.WaitGroup
   shutdownWg.Add(1) // For server shutdown
   ```

3. **Signal Handler Setup**:
   ```go
   setupSignalHandler(ctx, cancel, &shutdownWg, config.ShutdownDelay)
   ```
   - Must handle SIGINT and SIGTERM
   - Must cancel root context when signal received
   - Must wait for waitgroups with timeout (shutdown delay)
   - Must not block signal handler (runs in separate goroutine)

4. **Server Initialization**:
   - Must create `srt.Server` with root context and waitgroup
   - Must set `Server.Context` and `Server.ShutdownWg` before `ListenAndServe()`
   - Must start server in separate goroutine

5. **Connection Acceptance**:
   - Must accept publish connection from client-generator
   - Must accept subscribe connection from client
   - Must route published stream to subscriber

#### Shutdown Requirements
1. **Signal Handling**:
   - Must receive SIGINT/SIGTERM signals
   - Must cancel root context when signal received
   - Must wait for waitgroups with timeout

2. **Server Shutdown**:
   - `Server.Serve()` must detect context cancellation
   - `Server.Serve()` must call `Shutdown()` when context cancelled
   - `Shutdown()` must close listener
   - Listener must close all connections
   - All connections must send shutdown packets

3. **Connection Cleanup**:
   - All connections must close cleanly
   - All connection goroutines must exit
   - All connections must send shutdown packets to peers
   - Listener must wait for all connections to close

4. **Process Exit**:
   - Must exit when all waitgroups complete
   - Must exit within shutdown delay
   - Must exit with code 0 (success)

#### Current Implementation Status
- ✅ Root context and waitgroup created
- ✅ Signal handler set up with waitgroup and timeout
- ✅ Server uses context-driven shutdown (Option 3)
- ✅ Server.Serve() detects context cancellation
- ✅ Listener closes all connections on shutdown
- ✅ All connections send shutdown packets

### Client Requirements

#### Startup Requirements
1. **Root Context Creation**:
   ```go
   ctx, cancel := context.WithCancel(context.Background())
   defer cancel()
   ```

2. **Root WaitGroup Creation**:
   ```go
   var shutdownWg sync.WaitGroup
   ```

3. **Signal Handler Setup**:
   ```go
   setupSignalHandler(ctx, cancel)
   ```
   - Must handle SIGINT and SIGTERM
   - Must cancel root context when signal received
   - Must not block (runs in separate goroutine)

4. **SRT Connection Setup**:
   - Must use `srt.Dial()` to connect to server (reader)
   - Must use `srt.Dial()` to connect to server (writer, if needed)
   - Must pass root context and waitgroup to `Dial()`
   - Must set stream ID via config (from URL query parameter)
   - Must use "caller" mode (subscriber)
   - Must increment waitgroup after connections are established

5. **Stream Reception**:
   - Must receive packets from the subscribed stream
   - Must process packets (or discard if using null output)
   - Must track statistics

#### Shutdown Requirements
1. **Signal Handling**:
   - Must receive SIGINT/SIGTERM signals
   - Must cancel root context when signal received
   - Must not block signal handler

2. **Goroutine Exit**:
   - Read goroutine must check `ctx.Done()` in main loop
   - Write goroutine must check `ctx.Done()` in main loop (if used)
   - Read/write goroutines must exit when context is cancelled
   - Read/write goroutines must handle connection errors gracefully
   - Stats ticker goroutine must exit when context is cancelled

3. **Connection Cleanup**:
   - Must close SRT connections when context is cancelled
   - Must not wait for waitgroup (exit immediately)
   - Must handle connection close errors gracefully

4. **Process Exit**:
   - Must exit when read/write goroutines exit
   - Must exit when context is cancelled
   - Must exit within shutdown delay
   - Must exit with code 0 (success)

#### Current Implementation Status
- ✅ Root context and waitgroup created
- ✅ Signal handler set up
- ✅ SRT connections established with context and waitgroup
- ✅ Read/write goroutines check context cancellation
- ✅ Stats ticker respects context cancellation
- ✅ Context-aware read operation (wrapped in goroutine)
- ✅ `SetReadDeadline` approach implemented to allow periodic `checkConnection()` calls
- ⚠️ **Issue**: When server closes, client may not detect it immediately (checkConnection() called every 2 seconds should help)

#### Required Fixes
1. **Verify Connection Close Detection**:
   - `checkConnection()` should detect server closes via `doneChan` (called periodically via Read() with SetReadDeadline)
   - Connection should close when server sends shutdown packet
   - Connection context should be cancelled when connection closes
   - `ReadPacket()` should return `io.EOF` when connection context is cancelled

2. **Verify Read Operation Context Awareness**:
   - Read operation uses `SetReadDeadline(2 seconds)` to prevent indefinite blocking
   - Read loop checks context cancellation after each timeout
   - `checkConnection()` is called in read loop after timeouts to detect server closes
   - When client context is cancelled, read loop exits immediately
   - When connection context is cancelled, `ReadPacket()` should return `io.EOF`
   - Timeout errors are expected and trigger context checks (not reported as errors)

---

## Client Shutdown Signaling Design

### Current Implementation Analysis

#### Client (`contrib/client/main.go`)

**Current Shutdown Flow**:
1. Root context created: `ctx, cancel := context.WithCancel(context.Background())`
2. Root waitgroup created: `var shutdownWg sync.WaitGroup`
3. Signal handler set up: `setupSignalHandler(ctx, cancel)`
4. SRT connections created with context and waitgroup
5. Read/write goroutines check `ctx.Done()` in main loops
6. When context is cancelled:
   - Read/write goroutines exit
   - Main goroutine exits immediately (doesn't wait for waitgroup)
   - Connections are closed
   - Process exits

**Issues Identified**:
1. **Waitgroup Usage**: Waitgroup is incremented for each connection, but main doesn't wait for it. This is intentional (exit immediately), but we should verify connections close properly.
2. **Connection Close Detection**: When server closes, client may not detect it immediately. `checkConnection()` called periodically (every 2 seconds) via `SetReadDeadline` should help.
3. **Read Blocking**: `Read()` can block in `ReadPacket()` waiting on `c.readQueue`. Context-aware wrapper helps, but connection context cancellation is still needed.

**Required Improvements**:
1. **Verify Waitgroup Usage**: Ensure waitgroup is only used for tracking, not blocking main exit
2. **Verify Connection Close Detection**: Ensure `checkConnection()` is called in read loop after timeouts
3. **Verify Context Propagation**: Ensure connection context is cancelled when dialer closes
4. ✅ **Use SetReadDeadline for Periodic Context Checks**: Replaced goroutine wrapper with `SetReadDeadline` approach to prevent indefinite blocking

#### Client-Generator (`contrib/client-generator/main.go`)

**Current Shutdown Flow**:
1. Root context created: `ctx, cancel := context.WithCancel(context.Background())`
2. Root waitgroup created: `var shutdownWg sync.WaitGroup`
3. Signal handler set up: `setupSignalHandler(ctx, cancel)`
4. SRT connection created with context and waitgroup
5. Write goroutine checks `ctx.Done()` in main loop
6. When context is cancelled:
   - Write goroutine exits
   - Main goroutine exits immediately (doesn't wait for waitgroup)
   - Connection is closed
   - Process exits

**Issues Identified**:
1. **Same as Client**: Waitgroup usage and connection close detection issues

**Required Improvements**:
1. **Same as Client**: Verify waitgroup usage and connection close detection

### Design Alignment with `context_and_cancellation_design.md`

#### Context Hierarchy (Should Match Design)

**Design Specification**:
```
main.go (root context)
  └── Client
       └── Dialer
            └── Connection (srtConn)
```

**Current Implementation**:
- ✅ Root context created in `main.go`
- ✅ Root context passed to `srt.Dial()`
- ✅ Dialer stores context: `dl.ctx = ctx`
- ✅ Connection context inherits from dialer: `c.ctx, c.cancelCtx = context.WithCancel(config.parentCtx)`
- ✅ Connection context is cancelled when connection closes: `c.cancelCtx()`

**Status**: ✅ Aligned with design

#### WaitGroup Hierarchy (Should Match Design)

**Design Specification**:
```
main.go (root waitgroup)
  └── Dialer (shutdownWg)
       └── Connection (connWg)
```

**Current Implementation**:
- ✅ Root waitgroup created in `main.go`
- ✅ Root waitgroup passed to `srt.Dial()`
- ✅ Dialer stores waitgroup: `dl.shutdownWg = shutdownWg`
- ✅ Connection stores waitgroup: `c.shutdownWg = config.parentWg`
- ✅ Connection calls `shutdownWg.Done()` when it closes
- ⚠️ **Issue**: Main doesn't wait for waitgroup (exits immediately)

**Status**: ⚠️ Partially aligned - main doesn't wait, but this is intentional for fast exit

#### Signal Handling (Should Match Design)

**Design Specification**:
- Signal handler cancels root context
- Components detect context cancellation and shutdown automatically
- Signal handler waits for waitgroups with timeout

**Current Implementation**:
- ✅ Signal handler cancels root context
- ✅ Components detect context cancellation
- ⚠️ **Issue**: Signal handler doesn't wait for waitgroups (main does, but exits immediately)

**Status**: ⚠️ Partially aligned - signal handler doesn't wait, but main exits immediately

### Required Client Shutdown Design

#### Signal Handler Behavior

**Current**:
```go
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        select {
        case <-sigChan:
            cancel() // Cancel root context
        case <-ctx.Done():
            return
        }
    }()
}
```

**Required** (to match server design):
- Signal handler should only cancel context (current implementation is correct)
- Main should exit immediately when context is cancelled (current implementation is correct)
- Waitgroup is for tracking, not blocking (current implementation is correct)

**Status**: ✅ Already aligned with design

#### Main Goroutine Behavior

**Current**:
```go
// Wait for either error or context cancellation
select {
case err := <-doneChan:
    // Read/write goroutine exited - close connections and exit
    w.Close()
    r.Close()
    return
case <-ctx.Done():
    // Context cancelled - close connections and exit
    w.Close()
    r.Close()
    return
}
```

**Required**:
- Main should exit immediately when read/write goroutines exit
- Main should exit immediately when context is cancelled
- Main should not wait for waitgroup (connections will close asynchronously)
- Connections should close cleanly (dialer will call `shutdownWg.Done()`)

**Status**: ✅ Already aligned with design

#### Connection Close Detection

**Current**:
- `SetReadDeadline(2 seconds)` prevents indefinite blocking in read loop
- When timeout occurs, `checkConnection()` is called in `Read()`
- `checkConnection()` checks `doneChan` for server close errors
- When server closes, `ReadFrom()` goroutine sends error to `doneChan`
- `checkConnection()` detects error and calls `dl.Close()`
- `dl.Close()` closes connection and cancels connection context
- `ReadPacket()` returns `io.EOF` when connection context is cancelled

**Required**:
- Connection should detect server close promptly (within 2 seconds via timeout)
- Connection should close when server sends shutdown packet
- Connection context should be cancelled when connection closes
- `ReadPacket()` should return `io.EOF` when connection context is cancelled

**Status**: ✅ Already implemented - `SetReadDeadline` provides periodic checks without separate goroutine

---

## Test Execution Flow

### Step-by-Step Execution

1. **Start Server**:
   ```bash
   ./server -addr 127.0.0.1:6000
   ```
   - Server starts and listens on `127.0.0.1:6000`
   - Server creates root context and waitgroup
   - Server sets up signal handler
   - Server is ready

2. **Start Client-Generator**:
   ```bash
   ./client-generator -to srt://127.0.0.1:6000/test-stream -bitrate 2000000
   ```
   - Client-generator connects to server (publisher)
   - Client-generator starts generating MPEG-TS packets at 2 Mb/s
   - Client-generator begins publishing stream

3. **Start Client**:
   ```bash
   ./client -from srt://127.0.0.1:6000?streamid=subscribe:/test-stream -to null
   ```
   - Client connects to server (subscriber)
   - Client subscribes to `/test-stream`
   - Client begins receiving packets

4. **Wait for Stream to Stabilize**:
   - Wait 2-5 seconds for connections to establish
   - Verify packets are flowing
   - Verify statistics show active transfer

5. **Send SIGINT to Client**:
   ```bash
   kill -INT <client_pid>
   ```
   - Client receives SIGINT
   - Client's signal handler cancels root context
   - Client's read/write goroutines exit
   - Client closes connections
   - Client exits

6. **Verify Client Shutdown**:
   - Client process exits with code 0
   - Client exits within shutdown delay
   - No errors in client output

7. **Send SIGINT to Client-Generator**:
   ```bash
   kill -INT <client-generator_pid>
   ```
   - Client-generator receives SIGINT
   - Client-generator's signal handler cancels root context
   - Client-generator's write goroutine exits
   - Client-generator closes connection
   - Client-generator exits

8. **Verify Client-Generator Shutdown**:
   - Client-generator process exits with code 0
   - Client-generator exits within shutdown delay
   - No errors in client-generator output

9. **Send SIGINT to Server**:
   ```bash
   kill -INT <server_pid>
   ```
   - Server receives SIGINT
   - Server's signal handler cancels root context
   - Server's `Serve()` detects cancellation and calls `Shutdown()`
   - Server closes all connections
   - Server exits

10. **Verify Server Shutdown**:
    - Server process exits with code 0
    - Server exits within shutdown delay
    - No errors in server output

---

## Verification Criteria

### Client Shutdown Verification

**Must Verify**:
1. ✅ Client receives SIGINT signal
2. ✅ Client's root context is cancelled
3. ✅ Client's read/write goroutines exit
4. ✅ Client's SRT connection is closed cleanly
5. ✅ Client sends shutdown packet to server (if connection still active)
6. ✅ Client process exits with code 0
7. ✅ Client exits within shutdown delay (default 5 seconds)
8. ✅ No "connection refused" errors
9. ✅ No goroutine leaks

**Success Criteria**:
- All verification points pass
- Client exits cleanly without errors
- Client exits within 2 seconds (faster than shutdown delay)

### Client-Generator Shutdown Verification

**Must Verify**:
1. ✅ Client-generator receives SIGINT signal
2. ✅ Client-generator's root context is cancelled
3. ✅ Client-generator's write goroutine exits
4. ✅ Client-generator's SRT connection is closed cleanly
5. ✅ Client-generator sends shutdown packet to server (if connection still active)
6. ✅ Client-generator process exits with code 0
7. ✅ Client-generator exits within shutdown delay (default 5 seconds)
8. ✅ No "connection refused" errors
9. ✅ No goroutine leaks

**Success Criteria**:
- All verification points pass
- Client-generator exits cleanly without errors
- Client-generator exits within 2 seconds (faster than shutdown delay)

### Server Shutdown Verification

**Must Verify**:
1. ✅ Server receives SIGINT signal
2. ✅ Server's root context is cancelled
3. ✅ Server's `Serve()` detects cancellation and calls `Shutdown()`
4. ✅ All connections are closed cleanly
5. ✅ All connections send shutdown packets
6. ✅ All connection goroutines exit
7. ✅ Server process exits with code 0
8. ✅ Server exits within shutdown delay (default 5 seconds)
9. ✅ No goroutine leaks

**Success Criteria**:
- All verification points pass
- Server exits cleanly without errors
- Server exits within shutdown delay

---

## Implementation Checklist

### Client-Generator Implementation

- [x] Root context and waitgroup creation
- [x] Signal handler setup
- [x] SRT connection with context and waitgroup
- [x] MPEG-TS packet generation at configurable bitrate
- [x] Write goroutine with context cancellation check
- [x] Stats ticker with context cancellation
- [x] Connection close on context cancellation
- [x] Immediate exit when goroutines exit (no waitgroup wait)
- [ ] **TODO**: Verify connection close detection works correctly
- [ ] **TODO**: Verify graceful shutdown on SIGINT

### Client Implementation

- [x] Root context and waitgroup creation
- [x] Signal handler setup
- [x] SRT connections with context and waitgroup
- [x] Read/write goroutines with context cancellation check
- [x] Context-aware read operation with `SetReadDeadline` (2 second timeout)
- [x] Stats ticker with context cancellation
- [x] Connection close on context cancellation
- [x] `checkConnection()` called in read loop after timeouts (replaces `connectionChecker` goroutine)
- [x] Immediate exit when goroutines exit (no waitgroup wait)
- [x] Read loop handles timeout errors gracefully (continues to check context)
- [ ] **TODO**: Verify connection close detection works correctly
- [ ] **TODO**: Verify graceful shutdown on SIGINT

### Server Implementation

- [x] Root context and waitgroup creation
- [x] Signal handler setup with waitgroup and timeout
- [x] Server with context-driven shutdown
- [x] Listener with context and waitgroup
- [x] Connection cleanup on shutdown
- [x] Shutdown packet sending
- [x] Waitgroup completion before exit
- [x] Graceful shutdown on SIGINT

---

## Known Issues and Solutions

### Issue 1: Client Processes Don't Exit When Server Closes

**Problem**: When the server closes, the client processes may not detect it immediately and continue running.

**Root Cause**:
- `Read()` blocks in `ReadPacket()` waiting on `c.readQueue`
- `ReadPacket()` only returns `io.EOF` when `c.ctx` is cancelled
- Connection context is only cancelled when dialer detects server close
- Dialer detects server close via `checkConnection()`, but it's only called in `Read()`, which is blocked

**Solution Implemented**:
- **Added `SetReadDeadline` approach**: Prevents indefinite blocking in read loop
  - Read deadline set to 2 seconds before each read
  - When timeout occurs, loop continues and checks context
  - Allows periodic context checks without blocking indefinitely
  - Timeout errors are expected and not reported as errors
  - When server closes, `ReadFrom()` goroutine sends error to `doneChan`
  - `checkConnection()` is called in the read loop after each timeout
  - `checkConnection()` detects error and calls `dl.Close()`
  - `dl.Close()` closes connection and cancels connection context
  - `ReadPacket()` returns `io.EOF` when connection context is cancelled

**Status**: ✅ Implemented - `SetReadDeadline` approach eliminates need for separate `connectionChecker` goroutine

### Issue 2: Stats Ticker Keeps Running After Connection Closes

**Problem**: Stats ticker continues printing after connection closes, indicating context isn't cancelled.

**Root Cause**:
- Stats ticker checks client context, not connection context
- When server closes, connection context is cancelled, but client context isn't
- Client context is only cancelled when SIGINT is sent to client

**Solution**:
- Stats ticker should exit when connection closes (check connection state)
- Or, make connection close cancel client context (not recommended - breaks design)
- Current implementation is correct - stats ticker exits when client context is cancelled

**Status**: ✅ Working as designed

### Issue 3: Waitgroup Never Completes

**Problem**: Main goroutine waits on waitgroup, but waitgroup never completes because connection doesn't close.

**Solution Implemented**:
- Removed waitgroup wait from main shutdown path
- Main exits immediately when read/write goroutines exit
- Waitgroup is only for tracking, not blocking

**Status**: ✅ Fixed

---

## Testing Strategy

### Manual Testing

1. Start all three processes manually
2. Send SIGINT to each process in order (client → client-generator → server)
3. Verify each process exits cleanly
4. Check for errors in output

### Automated Testing

1. Use `contrib/integration_testing/test_graceful_shutdown.go`
2. Test orchestrator starts all processes
3. Test orchestrator sends SIGINT in order
4. Test orchestrator verifies exit codes and timing

### Success Criteria

- All processes exit with code 0
- All processes exit within shutdown delay
- No "connection refused" errors
- No goroutine leaks
- Clean shutdown logs

---

## Detailed Implementation Plan

### Overview

This section provides a step-by-step implementation plan for Test 1.1: Graceful Shutdown on SIGINT. The plan covers code changes, testing procedures, and verification steps.

### Prerequisites

1. **Codebase Status**:
   - Context and cancellation design implemented (Phases 1-7 complete)
   - `SetReadDeadline` approach implemented in client read loop
   - `checkConnection()` called periodically via `Read()` with timeout
   - All three components (server, client, client-generator) have context-based shutdown

2. **Build Requirements**:
   - Go 1.19+ installed
   - Linux environment (for io_uring support, if enabled)
   - Makefile targets available: `make server`, `make client`, `make client-generator`

3. **Test Infrastructure**:
   - Integration test orchestrator exists: `contrib/integration_testing/test_graceful_shutdown.go`
   - Test can be run manually or via `make test-integration`

### Phase 1: Code Review and Verification

This phase involves a detailed review of the implementation to ensure all components correctly implement the context and cancellation design. The focus is on verifying that the code matches the design document requirements and identifying any gaps or issues.

#### Step 1.1: Verify Client Implementation

**Overview**: The client (`contrib/client/main.go`) must correctly implement context-based shutdown, periodic connection checks via `SetReadDeadline`, and graceful exit on SIGINT. This step verifies all aspects of the client implementation.

##### 1.1.1: Root Context and WaitGroup Creation

**Location**: `contrib/client/main.go`, lines ~167-170

**Current Implementation**:
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

var shutdownWg sync.WaitGroup
```

**Verification Checklist**:
- [ ] Root context is created with `context.WithCancel(context.Background())`
- [ ] Context cancel function is deferred (ensures cleanup on exit)
- [ ] Root waitgroup is created as `sync.WaitGroup` (not pointer)
- [ ] Waitgroup is used for tracking, not blocking main exit

**Rationale**: The root context must be cancellable to propagate shutdown signals. The waitgroup tracks connection shutdown but doesn't block the main goroutine.

**Potential Issues**:
- If context is not deferred, cleanup may not occur on panic
- If waitgroup is a pointer, it may be nil and cause panics
- If main waits on waitgroup, it may block indefinitely

##### 1.1.2: Signal Handler Setup

**Location**: `contrib/client/main.go`, line ~173 and function `setupSignalHandler()` at lines ~397-414

**Current Implementation**:
```go
setupSignalHandler(ctx, cancel)
```

**Function Implementation**:
```go
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        select {
        case <-sigChan:
            cancel() // Cancel root context
        case <-ctx.Done():
            return
        }
    }()
}
```

**Verification Checklist**:
- [ ] Signal handler is set up before connections are created
- [ ] Signal handler handles both SIGINT and SIGTERM
- [ ] Signal handler only cancels context (doesn't wait for waitgroups)
- [ ] Signal handler runs in separate goroutine (non-blocking)
- [ ] Signal handler exits if context already cancelled

**Rationale**: Following Option 3 (Context-Driven Shutdown), the signal handler only cancels the root context. All components detect context cancellation and shutdown automatically.

**Potential Issues**:
- If signal handler blocks, it may prevent signal delivery
- If signal handler waits for waitgroups, it may cause deadlock
- If signal handler doesn't handle SIGTERM, systemd/k8s shutdown may fail

##### 1.1.3: SRT Connection Setup with Context and WaitGroup

**Location**: `contrib/client/main.go`, lines ~175-196

**Current Implementation**:
```go
r, err := openReader(*from, logger, ctx, &shutdownWg)
// ...
if _, ok := r.(srt.Conn); ok {
    shutdownWg.Add(1)
}

w, err := openWriter(*to, logger, ctx, &shutdownWg)
// ...
if _, ok := w.(srt.Conn); ok {
    shutdownWg.Add(1)
}
```

**Verification Checklist**:
- [ ] `openReader()` receives root context and waitgroup
- [ ] `openWriter()` receives root context and waitgroup
- [ ] Waitgroup is incremented for each SRT connection (reader and writer)
- [ ] Waitgroup increment happens after connection is established
- [ ] Non-SRT connections (stdin, file, etc.) don't increment waitgroup

**Rationale**: SRT connections need context for cancellation and waitgroup for tracking. The dialer/listener will call `shutdownWg.Done()` when the connection closes.

**Potential Issues**:
- If waitgroup is incremented before connection is established, it may never be decremented
- If non-SRT connections increment waitgroup, it may cause waitgroup imbalance
- If context is not passed, connections won't respond to cancellation

##### 1.1.4: SetReadDeadline Implementation in Read Loop

**Location**: `contrib/client/main.go`, lines ~253-326

**Current Implementation**:
```go
// Check if reader is an SRT connection (supports SetReadDeadline)
var srtConn srt.Conn
if conn, ok := r.(srt.Conn); ok {
    srtConn = conn
}

for {
    // Check context cancellation first
    select {
    case <-ctx.Done():
        return
    default:
    }

    // Set read deadline to allow periodic context checks
    // This prevents Read() from blocking indefinitely
    if srtConn != nil {
        srtConn.SetReadDeadline(time.Now().Add(2 * time.Second))
    }

    // Perform the read operation
    n, err := r.Read(buffer)

    // Handle read result
    if err != nil {
        // Check if context was cancelled
        select {
        case <-ctx.Done():
            return
        default:
        }

        // Check if error is a timeout (expected - allows context check)
        if errors.Is(err, os.ErrDeadlineExceeded) {
            continue // Timeout occurred - continue loop to check context again
        }
        // ... handle other errors
    }
    // ... process successful read
}
```

**Verification Checklist**:
- [ ] Reader type is checked for `SetReadDeadline` support (SRT connections support it)
- [ ] Read deadline is set to 2 seconds before each read
- [ ] Deadline is reset on each iteration (allows periodic checks)
- [ ] Context is checked before setting deadline (early exit if cancelled)
- [ ] Context is checked after read error (early exit if cancelled)
- [ ] Timeout errors (`os.ErrDeadlineExceeded`) are handled gracefully (continue loop)
- [ ] Timeout errors are not reported as errors (expected behavior)
- [ ] Connection close errors are handled gracefully (exit without error)

**Rationale**: `SetReadDeadline` prevents indefinite blocking in `Read()`. When timeout occurs, the loop continues and checks context, allowing periodic connection close detection via `checkConnection()`.

**Potential Issues**:
- If deadline is not reset, subsequent reads may fail immediately
- If timeout errors are reported, they may cause false error messages
- If context is not checked after timeout, shutdown may be delayed
- If deadline is too short (< 1 second), it may cause excessive timeouts
- If deadline is too long (> 5 seconds), shutdown may be delayed

**Key Code Sections to Review**:
1. **Deadline Setting** (line ~275): Must be called before each read
2. **Timeout Error Handling** (lines ~292-295): Must continue loop, not report error
3. **Context Checks** (lines ~265-270, 284-288): Must check context before and after read

##### 1.1.5: checkConnection() Call in Read() Method

**Location**: `dial.go`, lines ~898-911

**Current Implementation**:
```go
func (dl *dialer) Read(p []byte) (n int, err error) {
    if err := dl.checkConnection(); err != nil {
        return 0, err
    }
    // ... rest of Read implementation
}
```

**checkConnection() Implementation** (lines ~304-313):
```go
func (dl *dialer) checkConnection() error {
    select {
    case err := <-dl.doneChan:
        dl.Close()
        return err
    default:
    }
    return nil
}
```

**Verification Checklist**:
- [ ] `checkConnection()` is called at the start of `Read()`
- [ ] `checkConnection()` is called at the start of `ReadPacket()`
- [ ] `checkConnection()` is called at the start of `Write()`
- [ ] `checkConnection()` is called at the start of `WritePacket()`
- [ ] `checkConnection()` uses non-blocking select (doesn't block if no error)
- [ ] `checkConnection()` calls `dl.Close()` when error detected
- [ ] `dl.Close()` cancels connection context and closes connection

**Rationale**: `checkConnection()` is called periodically (every 2 seconds when read timeout occurs) to detect server closes. When server closes, `ReadFrom()` goroutine sends error to `doneChan`, and `checkConnection()` detects it and closes the connection.

**Potential Issues**:
- If `checkConnection()` is not called in all I/O methods, server close may not be detected
- If `checkConnection()` blocks, it may cause delays
- If `dl.Close()` is not called, connection may not close properly

**Key Code Sections to Review**:
1. **Read() method** (line ~899): Must call `checkConnection()` first
2. **ReadPacket() method** (line ~914): Must call `checkConnection()` first
3. **Write() method** (line ~929): Must call `checkConnection()` first
4. **WritePacket() method** (line ~944): Must call `checkConnection()` first

##### 1.1.6: Context Cancellation Checks in Read/Write Goroutines

**Location**: `contrib/client/main.go`, lines ~263-270, 330-336, 338-362

**Current Implementation**:
```go
// In read loop
for {
    // Check context cancellation first
    select {
    case <-ctx.Done():
        return
    default:
    }
    // ... read operation
}

// Before write
select {
case <-ctx.Done():
    return
default:
}
// ... write operation
```

**Verification Checklist**:
- [ ] Context is checked at the start of read loop iteration
- [ ] Context is checked before write operation
- [ ] Context is checked after read error (before handling error)
- [ ] Context is checked after write error (before handling error)
- [ ] All context checks use non-blocking select (don't block if not cancelled)
- [ ] When context is cancelled, goroutines exit immediately (return, not break)

**Rationale**: Context checks allow goroutines to exit immediately when shutdown is initiated. Non-blocking select ensures checks don't block the main loop.

**Potential Issues**:
- If context is not checked frequently, shutdown may be delayed
- If context check blocks, it may prevent timely shutdown
- If goroutines don't exit on context cancellation, they may leak

##### 1.1.7: Immediate Exit When Goroutines Exit (No WaitGroup Wait)

**Location**: `contrib/client/main.go`, lines ~370-390

**Current Implementation**:
```go
// Wait for either error or context cancellation
select {
case err := <-doneChan:
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
    }
    // Read/write goroutine exited - close connections and exit
    w.Close()
    r.Close()
    return
case <-ctx.Done():
    // Context cancelled - graceful shutdown
    w.Close()
    r.Close()
    return
}
```

**Verification Checklist**:
- [ ] Main goroutine does NOT wait for `shutdownWg` (exits immediately)
- [ ] Main goroutine exits when read/write goroutines exit (via `doneChan`)
- [ ] Main goroutine exits when context is cancelled (via `ctx.Done()`)
- [ ] Connections are closed before exit (`w.Close()`, `r.Close()`)
- [ ] No `shutdownWg.Wait()` call in main shutdown path

**Rationale**: The waitgroup is for tracking connection shutdown, not blocking main exit. Main should exit immediately when goroutines exit or context is cancelled. Connections will close asynchronously, and dialer/listener will call `shutdownWg.Done()`.

**Potential Issues**:
- If main waits on waitgroup, it may block indefinitely if connection doesn't close
- If connections are not closed, they may leak
- If main doesn't exit immediately, shutdown may be delayed

##### 1.1.8: Review dial.go Implementation

**Location**: `dial.go`

**Key Functions to Review**:

1. **checkConnection()** (lines ~304-313):
   - [ ] Uses non-blocking select on `doneChan`
   - [ ] Calls `dl.Close()` when error detected
   - [ ] Returns error to caller

2. **ReadFrom() goroutine** (lines ~176-242):
   - [ ] Checks dialer context cancellation
   - [ ] Checks `dl.isShutdown()`
   - [ ] Sets read deadline (3 seconds) before `ReadFrom()`
   - [ ] Handles timeout errors (continues loop)
   - [ ] Sends error to `doneChan` when connection error occurs
   - [ ] Sends `ErrClientClosed` when context cancelled or shutdown

3. **dl.Close()** (lines ~857-896):
   - [ ] Closes connection if exists
   - [ ] Waits for connection waitgroup with timeout
   - [ ] Waits for receive completion handler with timeout
   - [ ] Closes socket
   - [ ] Calls `shutdownWg.Done()` to notify parent

**Verification Checklist**:
- [ ] `doneChan` is populated when `ReadFrom()` goroutine detects errors
- [ ] `doneChan` is populated when context is cancelled
- [ ] `dl.Close()` is called when `checkConnection()` detects server close
- [ ] `dl.Close()` cancels connection context
- [ ] Connection context cancellation causes `ReadPacket()` to return `io.EOF`

**Expected Outcome**: All client code aligns with design document requirements. Client can detect server closes via `checkConnection()` called periodically (every 2 seconds), and exits gracefully on SIGINT.

---

#### Step 1.2: Verify Client-Generator Implementation

**Overview**: The client-generator (`contrib/client-generator/main.go`) must correctly implement context-based shutdown, graceful write handling, and exit on SIGINT. This step verifies all aspects of the client-generator implementation.

##### 1.2.1: Root Context and WaitGroup Creation

**Location**: `contrib/client-generator/main.go`, lines ~74-81

**Current Implementation**:
```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

var shutdownWg sync.WaitGroup
```

**Verification Checklist**:
- [ ] Root context is created with `context.WithCancel(context.Background())`
- [ ] Context cancel function is deferred
- [ ] Root waitgroup is created as `sync.WaitGroup` (not pointer)
- [ ] Waitgroup is used for tracking, not blocking main exit

**Rationale**: Same as client - root context must be cancellable, waitgroup tracks connection shutdown.

**Potential Issues**: Same as client (see Step 1.1.1).

##### 1.2.2: Signal Handler Setup

**Location**: `contrib/client-generator/main.go`, line ~84 and function `setupSignalHandler()` at lines ~233-250

**Current Implementation**:
```go
setupSignalHandler(ctx, cancel)
```

**Function Implementation** (same as client):
```go
func setupSignalHandler(ctx context.Context, cancel context.CancelFunc) {
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        select {
        case <-sigChan:
            cancel()
        case <-ctx.Done():
            return
        }
    }()
}
```

**Verification Checklist**:
- [ ] Signal handler is set up before connection is created
- [ ] Signal handler handles both SIGINT and SIGTERM
- [ ] Signal handler only cancels context (doesn't wait for waitgroups)
- [ ] Signal handler runs in separate goroutine (non-blocking)
- [ ] Signal handler exits if context already cancelled

**Rationale**: Same as client - Option 3 (Context-Driven Shutdown).

**Potential Issues**: Same as client (see Step 1.1.2).

##### 1.2.3: SRT Connection Setup with Context and WaitGroup

**Location**: `contrib/client-generator/main.go`, lines ~86-98

**Current Implementation**:
```go
w, err := openWriter(*to, logger, ctx, &shutdownWg)
// ...
if _, ok := w.(srt.Conn); ok {
    shutdownWg.Add(1)
}
```

**Verification Checklist**:
- [ ] `openWriter()` receives root context and waitgroup
- [ ] Waitgroup is incremented for SRT connection (writer only)
- [ ] Waitgroup increment happens after connection is established
- [ ] Non-SRT connections (file, stdout, etc.) don't increment waitgroup

**Rationale**: Client-generator only has a writer (publisher), so only one waitgroup increment needed.

**Potential Issues**:
- If waitgroup is incremented before connection is established, it may never be decremented
- If non-SRT connections increment waitgroup, it may cause waitgroup imbalance

##### 1.2.4: Write Goroutine Context Cancellation Checks

**Location**: `contrib/client-generator/main.go`, lines ~131-203

**Current Implementation**:
```go
go func() {
    generator := newDataGenerator(*bitrate)
    buffer := make([]byte, CHANNEL_SIZE)

    for {
        // Check context cancellation first
        select {
        case <-ctx.Done():
            return
        default:
        }

        n, err := generator.Read(buffer)
        if err != nil {
            // Check if context was cancelled during read
            select {
            case <-ctx.Done():
                return
            default:
                doneChan <- fmt.Errorf("generator read: %w", err)
                return
            }
        }

        // Check context cancellation before write
        select {
        case <-ctx.Done():
            return
        default:
        }

        if _, err := w.Write(buffer[:n]); err != nil {
            // Check if context was cancelled or connection closed
            select {
            case <-ctx.Done():
                return
            default:
                // Handle connection close errors gracefully
                // ...
                doneChan <- fmt.Errorf("write: %w", err)
                return
            }
        }
    }
}()
```

**Verification Checklist**:
- [ ] Context is checked at the start of loop iteration
- [ ] Context is checked after generator read error (before handling error)
- [ ] Context is checked before write operation
- [ ] Context is checked after write error (before handling error)
- [ ] All context checks use non-blocking select
- [ ] When context is cancelled, goroutine exits immediately (return)
- [ ] Connection close errors are handled gracefully (exit without error)
- [ ] Other errors are reported via `doneChan`

**Rationale**: Write goroutine must exit immediately when context is cancelled. Connection close errors during shutdown are expected and should not be reported as errors.

**Potential Issues**:
- If context is not checked frequently, shutdown may be delayed
- If connection close errors are reported, they may cause false error messages
- If goroutine doesn't exit on context cancellation, it may leak

**Key Code Sections to Review**:
1. **Loop Start** (lines ~140-146): Must check context before each iteration
2. **Generator Read Error** (lines ~154-162): Must check context before reporting error
3. **Before Write** (lines ~165-171): Must check context before write
4. **Write Error** (lines ~174-201): Must check context and handle connection close errors gracefully

##### 1.2.5: Stats Ticker Context Cancellation

**Location**: `contrib/client-generator/main.go`, lines ~105-126

**Current Implementation**:
```go
if config.StatisticsPrintInterval > 0 {
    go func() {
        ticker := time.NewTicker(config.StatisticsPrintInterval)
        defer ticker.Stop()

        for range ticker.C {
            // ... print statistics
        }
    }()
}
```

**Verification Checklist**:
- [ ] Stats ticker is started in separate goroutine
- [ ] Stats ticker uses `time.NewTicker` (not `time.Tick`)
- [ ] Stats ticker is stopped with `defer ticker.Stop()`
- [ ] Stats ticker loop checks context cancellation (if implemented)
- [ ] Stats ticker exits when context is cancelled (if implemented)

**Current Issue**: The stats ticker does NOT check context cancellation. It will continue running until the process exits.

**Proposed Change**: Add context cancellation check to stats ticker:
```go
if config.StatisticsPrintInterval > 0 {
    go func() {
        ticker := time.NewTicker(config.StatisticsPrintInterval)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                // ... print statistics
            }
        }
    }()
}
```

**Rationale**: Stats ticker should exit when context is cancelled to avoid unnecessary work during shutdown.

**Potential Issues**:
- If stats ticker doesn't check context, it may continue printing after shutdown is initiated
- If stats ticker uses `time.Tick`, it may leak (no way to stop it)

##### 1.2.6: Immediate Exit When Goroutines Exit (No WaitGroup Wait)

**Location**: `contrib/client-generator/main.go`, lines ~206-227

**Current Implementation**:
```go
// Wait for either error or context cancellation
select {
case err := <-doneChan:
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
    }
    // Write goroutine exited - close connection and exit
    w.Close()
    return
case <-ctx.Done():
    // Context cancelled - graceful shutdown
    w.Close()
    return
}
```

**Verification Checklist**:
- [ ] Main goroutine does NOT wait for `shutdownWg` (exits immediately)
- [ ] Main goroutine exits when write goroutine exits (via `doneChan`)
- [ ] Main goroutine exits when context is cancelled (via `ctx.Done()`)
- [ ] Connection is closed before exit (`w.Close()`)
- [ ] No `shutdownWg.Wait()` call in main shutdown path

**Rationale**: Same as client - waitgroup is for tracking, not blocking. Main should exit immediately.

**Potential Issues**: Same as client (see Step 1.1.7).

##### 1.2.7: Connection Close Error Handling

**Location**: `contrib/client-generator/main.go`, lines ~173-201

**Current Implementation**:
```go
if _, err := w.Write(buffer[:n]); err != nil {
    // Check if context was cancelled or connection closed during shutdown
    select {
    case <-ctx.Done():
        return
    default:
        // Check if error is due to connection being closed (expected during shutdown)
        errStr := err.Error()
        if strings.Contains(errStr, "connection refused") ||
            strings.Contains(errStr, "use of closed network connection") ||
            strings.Contains(errStr, "broken pipe") {
            return // Exit gracefully (don't report error)
        }
        // Check for net.OpError
        if opErr, ok := err.(*net.OpError); ok {
            // ... similar checks
        }
        doneChan <- fmt.Errorf("write: %w", err)
        return
    }
}
```

**Verification Checklist**:
- [ ] Connection close errors are detected (string matching or type checking)
- [ ] Connection close errors cause graceful exit (return, not error report)
- [ ] Connection close errors are not reported via `doneChan`
- [ ] Other errors are reported via `doneChan`
- [ ] Context is checked before error handling

**Rationale**: Connection close errors during shutdown are expected and should not be reported as errors. They indicate the server closed the connection, which is normal during shutdown.

**Potential Issues**:
- If connection close errors are reported, they may cause false error messages
- If error detection is too broad, real errors may be ignored
- If error detection is too narrow, connection close errors may be reported

**Expected Outcome**: All client-generator code aligns with design document requirements. Client-generator exits gracefully on SIGINT, and stats ticker respects context cancellation (if updated).

#### Step 1.3: Verify Server Implementation

**Tasks**:
1. Review `contrib/server/main.go`:
   - [ ] Verify root context and waitgroup creation
   - [ ] Verify signal handler setup with waitgroup and timeout
   - [ ] Verify server context-driven shutdown (Option 3)

2. Review `server.go`:
   - [ ] Verify `Server.Serve()` detects context cancellation
   - [ ] Verify `Server.Shutdown()` waits for listener waitgroup
   - [ ] Verify listener closes all connections on shutdown

**Expected Outcome**: Server code aligns with design document requirements.

### Phase 2: Manual Testing

#### Step 2.1: Build All Components

**Tasks**:
```bash
cd /home/das/Downloads/srt/gosrt
make server
make client
make client-generator
```

**Verification**:
- [ ] All binaries build successfully
- [ ] Binaries are in expected locations:
  - `contrib/server/server`
  - `contrib/client/client`
  - `contrib/client-generator/client-generator`

#### Step 2.2: Manual Test - Startup Phase

**Tasks**:
1. **Start Server**:
   ```bash
   ./contrib/server/server -addr 127.0.0.1:6000
   ```
   - [ ] Server starts without errors
   - [ ] Server listens on `127.0.0.1:6000`
   - [ ] Server logs show it's ready to accept connections

2. **Start Client-Generator** (in separate terminal):
   ```bash
   ./contrib/client-generator/client-generator -to srt://127.0.0.1:6000/test-stream -bitrate 2000000
   ```
   - [ ] Client-generator starts without errors
   - [ ] Client-generator connects to server
   - [ ] Client-generator begins generating packets
   - [ ] Server logs show publish connection accepted

3. **Start Client** (in separate terminal):
   ```bash
   ./contrib/client/client -from srt://127.0.0.1:6000?streamid=subscribe:/test-stream -to null
   ```
   - [ ] Client starts without errors
   - [ ] Client connects to server
   - [ ] Client begins receiving packets
   - [ ] Server logs show subscribe connection accepted
   - [ ] Statistics show active data transfer

**Expected Outcome**: All three processes start successfully and stream is flowing.

#### Step 2.3: Manual Test - Run Phase

**Tasks**:
1. **Monitor Stream Activity** (wait 10 seconds):
   - [ ] Client-generator statistics show packets being sent
   - [ ] Client statistics show packets being received
   - [ ] Server logs show no errors
   - [ ] All processes remain responsive

**Expected Outcome**: Stream is active and stable.

#### Step 2.4: Manual Test - Shutdown Phase (Client First)

**Tasks**:
1. **Send SIGINT to Client**:
   ```bash
   kill -INT <client_pid>
   ```
   - [ ] Client receives SIGINT
   - [ ] Client's signal handler cancels root context
   - [ ] Client's read/write goroutines exit
   - [ ] Client closes SRT connection
   - [ ] Client process exits with code 0
   - [ ] Client exits within 2 seconds (faster than shutdown delay)
   - [ ] No "connection refused" errors in client output

2. **Verify Server State**:
   - [ ] Server logs show client connection closed
   - [ ] Server continues running (publish connection still active)

**Expected Outcome**: Client exits gracefully without errors.

#### Step 2.5: Manual Test - Shutdown Phase (Client-Generator Second)

**Tasks**:
1. **Send SIGINT to Client-Generator**:
   ```bash
   kill -INT <client-generator_pid>
   ```
   - [ ] Client-generator receives SIGINT
   - [ ] Client-generator's signal handler cancels root context
   - [ ] Client-generator's write goroutine exits
   - [ ] Client-generator closes SRT connection
   - [ ] Client-generator process exits with code 0
   - [ ] Client-generator exits within 2 seconds
   - [ ] No "connection refused" errors in client-generator output

2. **Verify Server State**:
   - [ ] Server logs show client-generator connection closed
   - [ ] Server has no active connections

**Expected Outcome**: Client-generator exits gracefully without errors.

#### Step 2.6: Manual Test - Shutdown Phase (Server Last)

**Tasks**:
1. **Send SIGINT to Server**:
   ```bash
   kill -INT <server_pid>
   ```
   - [ ] Server receives SIGINT
   - [ ] Server's signal handler cancels root context
   - [ ] Server's `Serve()` detects cancellation and calls `Shutdown()`
   - [ ] Server closes all connections (if any remain)
   - [ ] Server sends shutdown packets to all peers
   - [ ] Server process exits with code 0
   - [ ] Server exits within shutdown delay (default 5 seconds)
   - [ ] No errors in server output

**Expected Outcome**: Server exits gracefully without errors.

### Phase 3: Automated Testing

#### Step 3.1: Update Integration Test

**Tasks**:
1. Review `contrib/integration_testing/test_graceful_shutdown.go`:
   - [ ] Verify test orchestrator starts all three processes
   - [ ] Verify test sends SIGINT in correct order (client → client-generator → server)
   - [ ] Verify test waits for each process to exit
   - [ ] Verify test checks exit codes
   - [ ] Verify test checks exit timing (within shutdown delay)

2. **Update Test if Needed**:
   - [ ] Ensure shutdown order matches design (client → client-generator → server)
   - [ ] Ensure proper wait times between shutdowns
   - [ ] Ensure proper verification of exit codes and timing

**Expected Outcome**: Integration test correctly implements Test 1.1.

#### Step 3.2: Run Integration Test

**Tasks**:
```bash
cd /home/das/Downloads/srt/gosrt
make test-integration
```

**Verification**:
- [ ] Test builds successfully
- [ ] Test starts all three processes
- [ ] Test sends SIGINT in correct order
- [ ] All processes exit with code 0
- [ ] All processes exit within shutdown delay
- [ ] Test reports success

**Expected Outcome**: Integration test passes.

#### Step 3.3: Run Integration Test Multiple Times

**Tasks**:
1. Run test with multiple bandwidth configurations to check for flakiness and performance:
   - Test with 1 Mb/s bandwidth
   - Test with 2 Mb/s bandwidth
   - Test with 5 Mb/s bandwidth
   - Test with 10 Mb/s bandwidth

**Verification**:
- [ ] All bandwidth configurations pass
- [ ] No intermittent failures
- [ ] Consistent exit timing across all bandwidths
- [ ] Metrics collected and error counters verified for each run

**Expected Outcome**: Test is stable and reliable across different bandwidth configurations.

---

### Step 3.4: Enhanced Integration Test with Metrics Collection

**Status**: ⏳ Design Phase (Not Yet Implemented)

**Overview**: Enhance the integration test to collect metrics snapshots from the server during test execution and verify error counters do not increment unexpectedly.

#### 3.4.1: Metrics Collection Design

**Requirements**:
1. **Enable Metrics Server**: Start server with metrics enabled on a configurable port (e.g., `:9090`)
2. **Collect Metrics Snapshots**: Periodically fetch `/metrics` endpoint during test execution
3. **Verify Error Counters**: Check that error counters (e.g., `PktDrop*`, `PktRecvError*`, `CryptoError*`) do not increment unexpectedly
4. **Store Snapshots**: Store metrics snapshots at key points:
   - After connections established
   - During active stream (mid-test)
   - Before shutdown
   - After shutdown (if possible)

**Implementation Approach**:
```go
// Metrics collection configuration
type MetricsConfig struct {
    Enabled      bool   // Enable metrics collection
    ListenAddr   string // Server metrics listen address (e.g., ":9090")
    CollectInterval time.Duration // How often to collect metrics (e.g., 2 seconds)
    SnapshotPoints []string // Key points to take snapshots: "startup", "mid-test", "pre-shutdown", "post-shutdown"
}

// Metrics snapshot structure
type MetricsSnapshot struct {
    Timestamp time.Time
    Point     string // "startup", "mid-test", "pre-shutdown", "post-shutdown"
    Metrics   map[string]float64 // Parsed metrics (counter values)
    Raw       string // Raw Prometheus format for debugging
}

// Error counter names to monitor
var ErrorCounters = []string{
    "gosrt_pkt_drop_total",
    "gosrt_pkt_recv_error_total",
    "gosrt_pkt_sent_error_total",
    "gosrt_crypto_error_encrypt_total",
    "gosrt_crypto_error_generate_sek_total",
    "gosrt_crypto_error_marshal_km_total",
    // Add other error counters as needed
}
```

**Metrics Collection Function**:
```go
// collectMetrics fetches metrics from the server and returns a snapshot
func collectMetrics(metricsURL string) (*MetricsSnapshot, error) {
    resp, err := http.Get(metricsURL)
    if err != nil {
        return nil, fmt.Errorf("failed to fetch metrics: %w", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("failed to read metrics: %w", err)
    }

    // Parse Prometheus format and extract counter values
    metrics := parsePrometheusMetrics(string(body))

    return &MetricsSnapshot{
        Timestamp: time.Now(),
        Metrics:   metrics,
        Raw:       string(body),
    }, nil
}

// parsePrometheusMetrics parses Prometheus format and extracts counter values
func parsePrometheusMetrics(raw string) map[string]float64 {
    // Implementation: parse lines like "gosrt_pkt_drop_total{connection="0x123"} 42"
    // Extract metric name and value, handle labels
    // Return map of metric_name -> value
}
```

**Integration into Test Flow**:
1. Start server with `-metricsenabled -metricslistenaddr :9090`
2. Start client-generator with `-metricsenabled -metricslistenaddr :9091`
3. Start client with `-metricsenabled -metricslistenaddr :9092`
4. Start metrics collection goroutines that periodically fetch metrics from all three components
5. Take snapshots at key points (startup, mid-test, pre-shutdown)
6. After test completion, verify error counters have not incremented unexpectedly
7. Compare metrics across components (e.g., packets sent by publisher == packets received by subscriber)
8. Report any discrepancies or error counter increments as test failures

---

#### 3.4.1.1: Client and Client-Generator Metrics Endpoint Requirement

**Status**: Design Only (Not Yet Implemented)

**Requirement**: Both the client (`contrib/client`) and client-generator (`contrib/client-generator`) need to expose a `/metrics` Prometheus endpoint, similar to the server.

**Rationale**:
- Allow integration tests to gather statistics from all three components
- Enable comparison of metrics across components (e.g., packets sent vs packets received)
- Verify error counters on all components, not just the server
- Provide visibility into client-side performance and issues

**Current State**:
- Server: Already has `/metrics` endpoint via `MetricsEnabled` and `MetricsListenAddr` config options
- Client: Does NOT have `/metrics` endpoint
- Client-Generator: Does NOT have `/metrics` endpoint

**Required Changes**:

##### Client (`contrib/client/main.go`)

```go
// Add metrics server support (similar to server)
func startMetricsServer(addr string, connections []srt.Conn) {
    if addr == "" {
        return
    }

    mux := http.NewServeMux()
    mux.Handle("/metrics", metrics.MetricsHandler())

    server := &http.Server{
        Addr:    addr,
        Handler: mux,
    }

    go func() {
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
        }
    }()
}
```

**CLI Flags to Add**:
```go
// In contrib/client/main.go
var (
    metricsEnabled    = flag.Bool("metricsenabled", false, "Enable metrics endpoint")
    metricsListenAddr = flag.String("metricslistenaddr", "", "Address for metrics endpoint (e.g., :9092)")
)
```

**Integration**:
```go
// In main(), after connections are established
if *metricsEnabled && *metricsListenAddr != "" {
    // Collect SRT connections
    var connections []srt.Conn
    if srtConn, ok := r.(srt.Conn); ok {
        connections = append(connections, srtConn)
    }
    if srtConn, ok := w.(srt.Conn); ok {
        connections = append(connections, srtConn)
    }

    startMetricsServer(*metricsListenAddr, connections)
}
```

##### Client-Generator (`contrib/client-generator/main.go`)

Same pattern as client:
```go
// Add metrics server support
func startMetricsServer(addr string, connections []srt.Conn) {
    if addr == "" {
        return
    }

    mux := http.NewServeMux()
    mux.Handle("/metrics", metrics.MetricsHandler())

    server := &http.Server{
        Addr:    addr,
        Handler: mux,
    }

    go func() {
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            fmt.Fprintf(os.Stderr, "Metrics server error: %v\n", err)
        }
    }()
}
```

**CLI Flags to Add**:
```go
// In contrib/client-generator/main.go
var (
    metricsEnabled    = flag.Bool("metricsenabled", false, "Enable metrics endpoint")
    metricsListenAddr = flag.String("metricslistenaddr", "", "Address for metrics endpoint (e.g., :9091)")
)
```

##### Integration Test Metrics Collection

**Updated Metrics Collection Design**:
```go
// ComponentMetrics stores metrics for a single component
type ComponentMetrics struct {
    Component  string             // "server", "client-generator", "client"
    Addr       string             // Metrics endpoint address (e.g., ":9090")
    Snapshots  []*MetricsSnapshot // Collected snapshots
}

// TestMetrics stores metrics for all components in a test
type TestMetrics struct {
    Server          ComponentMetrics
    ClientGenerator ComponentMetrics
    Client          ComponentMetrics
}

// collectAllMetrics fetches metrics from all three components
func collectAllMetrics(testMetrics *TestMetrics, point string) error {
    var wg sync.WaitGroup
    var errs []error
    var errLock sync.Mutex

    // Collect from server
    wg.Add(1)
    go func() {
        defer wg.Done()
        snapshot, err := collectMetrics("http://127.0.0.1" + testMetrics.Server.Addr + "/metrics")
        if err != nil {
            errLock.Lock()
            errs = append(errs, fmt.Errorf("server metrics: %w", err))
            errLock.Unlock()
            return
        }
        snapshot.Point = point
        testMetrics.Server.Snapshots = append(testMetrics.Server.Snapshots, snapshot)
    }()

    // Collect from client-generator
    wg.Add(1)
    go func() {
        defer wg.Done()
        snapshot, err := collectMetrics("http://127.0.0.1" + testMetrics.ClientGenerator.Addr + "/metrics")
        if err != nil {
            errLock.Lock()
            errs = append(errs, fmt.Errorf("client-generator metrics: %w", err))
            errLock.Unlock()
            return
        }
        snapshot.Point = point
        testMetrics.ClientGenerator.Snapshots = append(testMetrics.ClientGenerator.Snapshots, snapshot)
    }()

    // Collect from client
    wg.Add(1)
    go func() {
        defer wg.Done()
        snapshot, err := collectMetrics("http://127.0.0.1" + testMetrics.Client.Addr + "/metrics")
        if err != nil {
            errLock.Lock()
            errs = append(errs, fmt.Errorf("client metrics: %w", err))
            errLock.Unlock()
            return
        }
        snapshot.Point = point
        testMetrics.Client.Snapshots = append(testMetrics.Client.Snapshots, snapshot)
    }()

    wg.Wait()

    if len(errs) > 0 {
        return fmt.Errorf("metrics collection errors: %v", errs)
    }
    return nil
}
```

##### Metrics Comparison Verification

```go
// verifyMetricsConsistency compares metrics across components
func verifyMetricsConsistency(testMetrics *TestMetrics) error {
    // Get final snapshots
    serverSnapshot := testMetrics.Server.Snapshots[len(testMetrics.Server.Snapshots)-1]
    clientGenSnapshot := testMetrics.ClientGenerator.Snapshots[len(testMetrics.ClientGenerator.Snapshots)-1]
    clientSnapshot := testMetrics.Client.Snapshots[len(testMetrics.Client.Snapshots)-1]

    var errors []string

    // Verify: Packets sent by client-generator should approximately equal packets received by server
    clientGenSent := clientGenSnapshot.Metrics["gosrt_pkt_sent_data_total"]
    serverRecv := serverSnapshot.Metrics["gosrt_pkt_recv_data_total"]
    if clientGenSent > 0 && serverRecv < clientGenSent*0.95 { // Allow 5% tolerance
        errors = append(errors, fmt.Sprintf(
            "packet mismatch: client-generator sent %d but server received %d (%.2f%%)",
            int64(clientGenSent), int64(serverRecv), serverRecv/clientGenSent*100))
    }

    // Verify: Packets sent by server to client should approximately equal packets received by client
    // (This requires tracking per-connection metrics which may need additional work)

    // Verify: No unexpected error counters on any component
    for _, component := range []struct {
        name     string
        snapshot *MetricsSnapshot
    }{
        {"server", serverSnapshot},
        {"client-generator", clientGenSnapshot},
        {"client", clientSnapshot},
    } {
        for _, counter := range ErrorCounters {
            if value, ok := component.snapshot.Metrics[counter]; ok && value > 0 {
                errors = append(errors, fmt.Sprintf(
                    "%s: unexpected error counter %s = %d", component.name, counter, int64(value)))
            }
        }
    }

    if len(errors) > 0 {
        return fmt.Errorf("metrics verification failed:\n  - %s", strings.Join(errors, "\n  - "))
    }

    return nil
}
```

##### Port Allocation

To avoid port conflicts, each component uses a different metrics port:
- **Server**: `:9090`
- **Client-Generator**: `:9091`
- **Client**: `:9092`

This can be configured via `SRTConfig.MetricsListenAddr` in the test configuration.

##### Updated SRTConfig

The `SRTConfig` struct already includes `MetricsEnabled` and `MetricsListenAddr`. When implementing this feature, ensure these fields are applied to all three components, not just the server.

##### Implementation Priority

This is a lower-priority enhancement. The basic integration test works without metrics comparison. Implementing this provides:
1. Better visibility into test execution
2. Ability to detect subtle issues (e.g., packet loss between components)
3. Verification that metrics are consistent across all components

---

#### 3.4.2: Multiple Test Runs with Different Configurations

**Requirements**:
1. **Flexible Test Configuration Table**: Define test configurations with component-specific CLI flags
2. **Support Various Scenarios**: Small buffers, large buffers, btree, io_uring, different latencies, etc.
3. **Iterate Through Configurations**: Run the test once for each configuration
4. **Collect Metrics for Each Run**: Collect metrics snapshots for each configuration
5. **Compare Results**: Compare metrics across different configurations to identify issues

**Design Philosophy**:
- Store SRT configuration values in a struct that mirrors `config.go`
- Automatically convert configuration to CLI flags when starting components
- Support component-specific configurations (server, client-generator, client)
- Make it easy to add new test scenarios

---

##### SRT Configuration Structure

```go
// SRTConfig represents SRT connection configuration parameters
// This mirrors the srt.Config struct and can be converted to CLI flags
type SRTConfig struct {
    // Connection timeouts
    ConnectionTimeout time.Duration // -conntimeo (milliseconds)
    PeerIdleTimeout   time.Duration // -peeridletimeo (milliseconds)
    HandshakeTimeout  time.Duration // -handshaketimeout
    ShutdownDelay     time.Duration // -shutdowndelay

    // Latency settings
    Latency      time.Duration // -latency (milliseconds)
    RecvLatency  time.Duration // -rcvlatency (milliseconds)
    PeerLatency  time.Duration // -peerlatency (milliseconds)

    // Buffer sizes
    FC       uint32 // -fc (flow control window, packets)
    RecvBuf  uint32 // -rcvbuf (receive buffer, bytes)
    SendBuf  uint32 // -sndbuf (send buffer, bytes)

    // Packet handling
    TLPktDrop               bool   // -tlpktdrop (too-late packet drop)
    PacketReorderAlgorithm  string // -packetreorderalgorithm (list, btree)
    BTreeDegree             int    // -btreedegree (b-tree degree)

    // io_uring settings
    IoUringEnabled         bool // -iouringenabled
    IoUringRecvEnabled     bool // -iouringrecvenabled
    IoUringSendRingSize    int  // -iouringsendringsize
    IoUringRecvRingSize    int  // -iouringrecvringsize
    IoUringRecvBatchSize   int  // -iouringrecvbatchsize

    // Congestion control
    Congestion string // -congestion (live, file)
    MaxBW      int64  // -maxbw (bytes/s, -1 for unlimited)
    InputBW    int64  // -inputbw (bytes/s)

    // Encryption
    Passphrase string // -passphrase
    PBKeyLen   int    // -pbkeylen (16, 24, 32)

    // Message mode
    MessageAPI bool // -messageapi

    // NAK reports
    NAKReport bool // -nakreport

    // Metrics
    MetricsEnabled    bool   // -metricsenabled
    MetricsListenAddr string // -metricslistenaddr
}

// ToCliFlags converts SRTConfig to CLI flag arguments
// Only includes flags that have non-zero/non-default values
func (c *SRTConfig) ToCliFlags() []string {
    var flags []string

    // Connection timeouts (convert to milliseconds for CLI)
    if c.ConnectionTimeout > 0 {
        flags = append(flags, "-conntimeo", strconv.Itoa(int(c.ConnectionTimeout.Milliseconds())))
    }
    if c.PeerIdleTimeout > 0 {
        flags = append(flags, "-peeridletimeo", strconv.Itoa(int(c.PeerIdleTimeout.Milliseconds())))
    }
    if c.HandshakeTimeout > 0 {
        flags = append(flags, "-handshaketimeout", c.HandshakeTimeout.String())
    }
    if c.ShutdownDelay > 0 {
        flags = append(flags, "-shutdowndelay", c.ShutdownDelay.String())
    }

    // Latency settings (convert to milliseconds for CLI)
    if c.Latency > 0 {
        flags = append(flags, "-latency", strconv.Itoa(int(c.Latency.Milliseconds())))
    }
    if c.RecvLatency > 0 {
        flags = append(flags, "-rcvlatency", strconv.Itoa(int(c.RecvLatency.Milliseconds())))
    }
    if c.PeerLatency > 0 {
        flags = append(flags, "-peerlatency", strconv.Itoa(int(c.PeerLatency.Milliseconds())))
    }

    // Buffer sizes
    if c.FC > 0 {
        flags = append(flags, "-fc", strconv.Itoa(int(c.FC)))
    }
    if c.RecvBuf > 0 {
        flags = append(flags, "-rcvbuf", strconv.Itoa(int(c.RecvBuf)))
    }
    if c.SendBuf > 0 {
        flags = append(flags, "-sndbuf", strconv.Itoa(int(c.SendBuf)))
    }

    // Packet handling
    if c.TLPktDrop {
        flags = append(flags, "-tlpktdrop")
    }
    if c.PacketReorderAlgorithm != "" {
        flags = append(flags, "-packetreorderalgorithm", c.PacketReorderAlgorithm)
    }
    if c.BTreeDegree > 0 {
        flags = append(flags, "-btreedegree", strconv.Itoa(c.BTreeDegree))
    }

    // io_uring settings
    if c.IoUringEnabled {
        flags = append(flags, "-iouringenabled")
    }
    if c.IoUringRecvEnabled {
        flags = append(flags, "-iouringrecvenabled")
    }
    if c.IoUringSendRingSize > 0 {
        flags = append(flags, "-iouringsendringsize", strconv.Itoa(c.IoUringSendRingSize))
    }
    if c.IoUringRecvRingSize > 0 {
        flags = append(flags, "-iouringrecvringsize", strconv.Itoa(c.IoUringRecvRingSize))
    }
    if c.IoUringRecvBatchSize > 0 {
        flags = append(flags, "-iouringrecvbatchsize", strconv.Itoa(c.IoUringRecvBatchSize))
    }

    // Congestion control
    if c.Congestion != "" {
        flags = append(flags, "-congestion", c.Congestion)
    }
    if c.MaxBW != 0 {
        flags = append(flags, "-maxbw", strconv.FormatInt(c.MaxBW, 10))
    }
    if c.InputBW > 0 {
        flags = append(flags, "-inputbw", strconv.FormatInt(c.InputBW, 10))
    }

    // Encryption
    if c.Passphrase != "" {
        flags = append(flags, "-passphrase", c.Passphrase)
    }
    if c.PBKeyLen > 0 {
        flags = append(flags, "-pbkeylen", strconv.Itoa(c.PBKeyLen))
    }

    // Message mode
    if c.MessageAPI {
        flags = append(flags, "-messageapi")
    }

    // NAK reports
    if c.NAKReport {
        flags = append(flags, "-nakreport")
    }

    // Metrics
    if c.MetricsEnabled {
        flags = append(flags, "-metricsenabled")
    }
    if c.MetricsListenAddr != "" {
        flags = append(flags, "-metricslistenaddr", c.MetricsListenAddr)
    }

    return flags
}
```

---

##### Component-Specific Configuration

```go
// ComponentConfig represents configuration specific to one component
type ComponentConfig struct {
    SRT          SRTConfig // SRT configuration (converted to CLI flags)
    ExtraFlags   []string  // Additional CLI flags not covered by SRTConfig
}

// ToCliFlags converts ComponentConfig to CLI flag arguments
func (c *ComponentConfig) ToCliFlags() []string {
    flags := c.SRT.ToCliFlags()
    flags = append(flags, c.ExtraFlags...)
    return flags
}
```

---

##### Network Address Configuration

Each component uses a distinct loopback IP address to make packet observation (e.g., with tcpdump/Wireshark) clear and unambiguous:

| Component | IP Address | SRT Port | Metrics Port |
|-----------|------------|----------|--------------|
| Server | 127.0.0.10 | 6000 | 9090 |
| Client-Generator | 127.0.0.20 | (ephemeral) | 9091 |
| Client | 127.0.0.30 | (ephemeral) | 9092 |

**Benefits**:
- Easy to filter traffic by component in packet captures
- Clear source/destination identification
- No port conflicts between components
- Consistent addressing across all tests

**Note**: The loopback addresses 127.0.0.10, 127.0.0.20, and 127.0.0.30 are valid on Linux. The entire 127.0.0.0/8 range is reserved for loopback.

---

##### Network Configuration Structure

```go
// NetworkConfig represents network address configuration for a component
type NetworkConfig struct {
    IP          string // IP address for the component (e.g., "127.0.0.10")
    SRTPort     int    // SRT port (server only, clients use ephemeral)
    MetricsPort int    // Metrics HTTP port (e.g., 9090, 9091, 9092)
}

// SRTAddr returns the SRT address string (e.g., "127.0.0.10:6000")
func (n *NetworkConfig) SRTAddr() string {
    if n.SRTPort > 0 {
        return fmt.Sprintf("%s:%d", n.IP, n.SRTPort)
    }
    return n.IP
}

// MetricsAddr returns the metrics address string (e.g., "127.0.0.10:9090")
func (n *NetworkConfig) MetricsAddr() string {
    if n.MetricsPort > 0 {
        return fmt.Sprintf("%s:%d", n.IP, n.MetricsPort)
    }
    return ""
}

// MetricsURL returns the full metrics URL (e.g., "http://127.0.0.10:9090/metrics")
func (n *NetworkConfig) MetricsURL() string {
    if n.MetricsPort > 0 {
        return fmt.Sprintf("http://%s:%d/metrics", n.IP, n.MetricsPort)
    }
    return ""
}

// Default network configurations
var (
    DefaultServerNetwork = NetworkConfig{
        IP:          "127.0.0.10",
        SRTPort:     6000,
        MetricsPort: 9090,
    }

    DefaultClientGeneratorNetwork = NetworkConfig{
        IP:          "127.0.0.20",
        SRTPort:     0, // Ephemeral port (client connects to server)
        MetricsPort: 9091,
    }

    DefaultClientNetwork = NetworkConfig{
        IP:          "127.0.0.30",
        SRTPort:     0, // Ephemeral port (client connects to server)
        MetricsPort: 9092,
    }
)
```

---

##### Test Configuration Structure

```go
// TestConfig represents a complete test configuration
type TestConfig struct {
    // Test identification
    Name        string // Human-readable name (e.g., "SmallBuffers-1Mbps")
    Description string // Detailed description of what this test validates

    // Network configuration (IP addresses and ports for each component)
    ServerNetwork          NetworkConfig // Server network config (default: 127.0.0.10:6000)
    ClientGeneratorNetwork NetworkConfig // Client-generator network config (default: 127.0.0.20)
    ClientNetwork          NetworkConfig // Client network config (default: 127.0.0.30)

    // Test parameters
    Bitrate         int64         // Bitrate in bits per second for client-generator
    TestDuration    time.Duration // How long to run before shutdown
    ConnectionWait  time.Duration // Time to wait for connections to establish

    // Component-specific configurations
    Server          ComponentConfig // Server configuration
    ClientGenerator ComponentConfig // Client-generator configuration
    Client          ComponentConfig // Client configuration

    // Shared SRT configuration (applied to all components)
    // If set, this is merged with component-specific configs (component takes precedence)
    SharedSRT *SRTConfig

    // Metrics collection
    MetricsEnabled  bool          // Enable metrics collection for this test
    CollectInterval time.Duration // How often to collect metrics

    // Expected results (for validation)
    ExpectedErrors       []string // List of expected error counters (e.g., "gosrt_pkt_drop_total")
    MaxExpectedDrops     int64    // Maximum expected packet drops (0 = none expected)
    MaxExpectedRetrans   int64    // Maximum expected retransmissions
}

// GetEffectiveNetworkConfig returns the network config for each component,
// using defaults if not specified in the test config
func (c *TestConfig) GetEffectiveNetworkConfig() (server, clientGen, client NetworkConfig) {
    // Use test-specific config or fall back to defaults
    server = c.ServerNetwork
    if server.IP == "" {
        server = DefaultServerNetwork
    }

    clientGen = c.ClientGeneratorNetwork
    if clientGen.IP == "" {
        clientGen = DefaultClientGeneratorNetwork
    }

    client = c.ClientNetwork
    if client.IP == "" {
        client = DefaultClientNetwork
    }

    return server, clientGen, client
}

// GetServerFlags returns CLI flags for the server component
func (c *TestConfig) GetServerFlags() []string {
    serverNet, _, _ := c.GetEffectiveNetworkConfig()

    flags := []string{"-addr", serverNet.SRTAddr()}

    // Add metrics address if port is configured
    if serverNet.MetricsPort > 0 {
        flags = append(flags, "-metricsenabled")
        flags = append(flags, "-metricslistenaddr", serverNet.MetricsAddr())
    }

    // Apply shared config first (if any)
    if c.SharedSRT != nil {
        flags = append(flags, c.SharedSRT.ToCliFlags()...)
    }

    // Apply component-specific config (overrides shared)
    flags = append(flags, c.Server.ToCliFlags()...)

    return flags
}

// GetClientGeneratorFlags returns CLI flags for the client-generator component
func (c *TestConfig) GetClientGeneratorFlags() []string {
    serverNet, clientGenNet, _ := c.GetEffectiveNetworkConfig()

    // Build the publisher URL using the server's SRT address
    publisherURL := fmt.Sprintf("srt://%s/test-stream", serverNet.SRTAddr())

    flags := []string{
        "-to", publisherURL,
        "-bitrate", strconv.FormatInt(c.Bitrate, 10),
    }

    // Add local IP binding (to use specific source IP)
    // Note: This may require adding a -localaddr flag to client-generator
    // For now, document the requirement
    // flags = append(flags, "-localaddr", clientGenNet.IP)

    // Add metrics address if port is configured
    if clientGenNet.MetricsPort > 0 {
        flags = append(flags, "-metricsenabled")
        flags = append(flags, "-metricslistenaddr", clientGenNet.MetricsAddr())
    }

    // Apply shared config first (if any)
    if c.SharedSRT != nil {
        flags = append(flags, c.SharedSRT.ToCliFlags()...)
    }

    // Apply component-specific config (overrides shared)
    flags = append(flags, c.ClientGenerator.ToCliFlags()...)

    return flags
}

// GetClientFlags returns CLI flags for the client component
func (c *TestConfig) GetClientFlags() []string {
    serverNet, _, clientNet := c.GetEffectiveNetworkConfig()

    // Build the subscriber URL using the server's SRT address
    subscriberURL := fmt.Sprintf("srt://%s?streamid=subscribe:/test-stream", serverNet.SRTAddr())

    flags := []string{
        "-from", subscriberURL,
        "-to", "null",
    }

    // Add local IP binding (to use specific source IP)
    // Note: This may require adding a -localaddr flag to client
    // For now, document the requirement
    // flags = append(flags, "-localaddr", clientNet.IP)

    // Add metrics address if port is configured
    if clientNet.MetricsPort > 0 {
        flags = append(flags, "-metricsenabled")
        flags = append(flags, "-metricslistenaddr", clientNet.MetricsAddr())
    }

    // Apply shared config first (if any)
    if c.SharedSRT != nil {
        flags = append(flags, c.SharedSRT.ToCliFlags()...)
    }

    // Apply component-specific config (overrides shared)
    flags = append(flags, c.Client.ToCliFlags()...)

    return flags
}

// GetAllMetricsURLs returns the metrics URLs for all components
func (c *TestConfig) GetAllMetricsURLs() (server, clientGen, client string) {
    serverNet, clientGenNet, clientNet := c.GetEffectiveNetworkConfig()
    return serverNet.MetricsURL(), clientGenNet.MetricsURL(), clientNet.MetricsURL()
}
```

---

##### Predefined Configuration Presets

```go
// Predefined SRT configuration presets for common scenarios
var (
    // DefaultSRTConfig - default settings, no special configuration
    DefaultSRTConfig = SRTConfig{}

    // SmallBuffersSRTConfig - minimal latency, small buffers
    SmallBuffersSRTConfig = SRTConfig{
        ConnectionTimeout: 1000 * time.Millisecond,
        PeerIdleTimeout:   2000 * time.Millisecond,
        Latency:           120 * time.Millisecond,
        RecvLatency:       120 * time.Millisecond,
        PeerLatency:       120 * time.Millisecond,
        TLPktDrop:         true,
    }

    // LargeBuffersSRTConfig - larger latency, larger buffers for high-loss networks
    LargeBuffersSRTConfig = SRTConfig{
        ConnectionTimeout: 3000 * time.Millisecond,
        PeerIdleTimeout:   30000 * time.Millisecond,
        Latency:           3000 * time.Millisecond,
        RecvLatency:       3000 * time.Millisecond,
        PeerLatency:       3000 * time.Millisecond,
        TLPktDrop:         true,
    }

    // IoUringSRTConfig - io_uring enabled with btree
    IoUringSRTConfig = SRTConfig{
        IoUringEnabled:         true,
        IoUringRecvEnabled:     true,
        PacketReorderAlgorithm: "btree",
        TLPktDrop:              true,
    }

    // IoUringLargeBuffersSRTConfig - io_uring with large buffers
    IoUringLargeBuffersSRTConfig = SRTConfig{
        ConnectionTimeout:      3000 * time.Millisecond,
        PeerIdleTimeout:        30000 * time.Millisecond,
        Latency:                3000 * time.Millisecond,
        RecvLatency:            3000 * time.Millisecond,
        PeerLatency:            3000 * time.Millisecond,
        IoUringEnabled:         true,
        IoUringRecvEnabled:     true,
        PacketReorderAlgorithm: "btree",
        TLPktDrop:              true,
    }

    // BTreeSRTConfig - btree packet reordering
    BTreeSRTConfig = SRTConfig{
        PacketReorderAlgorithm: "btree",
        BTreeDegree:            32,
        TLPktDrop:              true,
    }

    // ListSRTConfig - list packet reordering (default)
    ListSRTConfig = SRTConfig{
        PacketReorderAlgorithm: "list",
        TLPktDrop:              true,
    }
)
```

---

##### Test Configuration Table

```go
// TestConfigs is a table of test configurations
// Each configuration tests a specific scenario
var TestConfigs = []TestConfig{
    // ========== Basic Bandwidth Tests ==========
    {
        Name:            "Default-1Mbps",
        Description:     "Default configuration at 1 Mb/s",
        Bitrate:         1_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT:       &SRTConfig{MetricsEnabled: true, MetricsListenAddr: ":9090"},
    },
    {
        Name:            "Default-2Mbps",
        Description:     "Default configuration at 2 Mb/s",
        Bitrate:         2_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT:       &SRTConfig{MetricsEnabled: true, MetricsListenAddr: ":9090"},
    },
    {
        Name:            "Default-5Mbps",
        Description:     "Default configuration at 5 Mb/s",
        Bitrate:         5_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT:       &SRTConfig{MetricsEnabled: true, MetricsListenAddr: ":9090"},
    },
    {
        Name:            "Default-10Mbps",
        Description:     "Default configuration at 10 Mb/s",
        Bitrate:         10_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT:       &SRTConfig{MetricsEnabled: true, MetricsListenAddr: ":9090"},
    },

    // ========== Buffer Size Tests ==========
    {
        Name:            "SmallBuffers-2Mbps",
        Description:     "Small buffers (120ms latency) at 2 Mb/s - tests minimal latency",
        Bitrate:         2_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            ConnectionTimeout: 1000 * time.Millisecond,
            PeerIdleTimeout:   2000 * time.Millisecond,
            RecvLatency:       120 * time.Millisecond,
            PeerLatency:       120 * time.Millisecond,
            TLPktDrop:         true,
            MetricsEnabled:    true,
            MetricsListenAddr: ":9090",
        },
    },
    {
        Name:            "LargeBuffers-2Mbps",
        Description:     "Large buffers (3s latency) at 2 Mb/s - tests high-loss resilience",
        Bitrate:         2_000_000,
        TestDuration:    15 * time.Second, // Longer duration for larger buffers
        ConnectionWait:  3 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            ConnectionTimeout: 3000 * time.Millisecond,
            PeerIdleTimeout:   30000 * time.Millisecond,
            RecvLatency:       3000 * time.Millisecond,
            PeerLatency:       3000 * time.Millisecond,
            TLPktDrop:         true,
            MetricsEnabled:    true,
            MetricsListenAddr: ":9090",
        },
    },

    // ========== Packet Reordering Algorithm Tests ==========
    {
        Name:            "BTree-2Mbps",
        Description:     "B-tree packet reordering at 2 Mb/s",
        Bitrate:         2_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            PacketReorderAlgorithm: "btree",
            BTreeDegree:            32,
            TLPktDrop:              true,
            MetricsEnabled:         true,
            MetricsListenAddr:      ":9090",
        },
    },
    {
        Name:            "List-2Mbps",
        Description:     "List packet reordering at 2 Mb/s (default)",
        Bitrate:         2_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            PacketReorderAlgorithm: "list",
            TLPktDrop:              true,
            MetricsEnabled:         true,
            MetricsListenAddr:      ":9090",
        },
    },

    // ========== io_uring Tests ==========
    {
        Name:            "IoUring-2Mbps",
        Description:     "io_uring enabled with btree at 2 Mb/s",
        Bitrate:         2_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            IoUringEnabled:         true,
            IoUringRecvEnabled:     true,
            PacketReorderAlgorithm: "btree",
            TLPktDrop:              true,
            MetricsEnabled:         true,
            MetricsListenAddr:      ":9090",
        },
    },
    {
        Name:            "IoUring-10Mbps",
        Description:     "io_uring enabled with btree at 10 Mb/s - high throughput test",
        Bitrate:         10_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            IoUringEnabled:         true,
            IoUringRecvEnabled:     true,
            PacketReorderAlgorithm: "btree",
            TLPktDrop:              true,
            MetricsEnabled:         true,
            MetricsListenAddr:      ":9090",
        },
    },

    // ========== Combined Configuration Tests ==========
    {
        Name:            "IoUring-LargeBuffers-10Mbps",
        Description:     "io_uring with large buffers at 10 Mb/s - production-like configuration",
        Bitrate:         10_000_000,
        TestDuration:    15 * time.Second,
        ConnectionWait:  3 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        SharedSRT: &SRTConfig{
            ConnectionTimeout:      3000 * time.Millisecond,
            PeerIdleTimeout:        30000 * time.Millisecond,
            RecvLatency:            3000 * time.Millisecond,
            PeerLatency:            3000 * time.Millisecond,
            IoUringEnabled:         true,
            IoUringRecvEnabled:     true,
            PacketReorderAlgorithm: "btree",
            TLPktDrop:              true,
            MetricsEnabled:         true,
            MetricsListenAddr:      ":9090",
        },
    },

    // ========== Component-Specific Configuration Tests ==========
    {
        Name:            "AsymmetricLatency-2Mbps",
        Description:     "Server and client with different latency settings",
        Bitrate:         2_000_000,
        TestDuration:    10 * time.Second,
        ConnectionWait:  2 * time.Second,
        MetricsEnabled:  true,
        CollectInterval: 2 * time.Second,
        Server: ComponentConfig{
            SRT: SRTConfig{
                RecvLatency:       500 * time.Millisecond,
                PeerLatency:       500 * time.Millisecond,
                TLPktDrop:         true,
                MetricsEnabled:    true,
                MetricsListenAddr: ":9090",
            },
        },
        ClientGenerator: ComponentConfig{
            SRT: SRTConfig{
                RecvLatency: 1000 * time.Millisecond,
                PeerLatency: 1000 * time.Millisecond,
                TLPktDrop:   true,
            },
        },
        Client: ComponentConfig{
            SRT: SRTConfig{
                RecvLatency: 1000 * time.Millisecond,
                PeerLatency: 1000 * time.Millisecond,
                TLPktDrop:   true,
            },
        },
    },
}
```

---

##### Test Execution Flow

```go
func testGracefulShutdownSIGINTWithConfigs() {
    fmt.Println("=== Test 1.1: Graceful Shutdown on SIGINT (Multiple Configurations) ===")
    fmt.Println()
    fmt.Printf("Total configurations to test: %d\n", len(TestConfigs))
    fmt.Println()

    passed := 0
    failed := 0

    for i, config := range TestConfigs {
        fmt.Printf("--- Test Configuration %d/%d: %s ---\n", i+1, len(TestConfigs), config.Name)
        fmt.Printf("Description: %s\n", config.Description)
        fmt.Printf("Bitrate: %d bps (%.2f Mb/s)\n", config.Bitrate, float64(config.Bitrate)/1_000_000)
        fmt.Println()

        // Print network configuration
        serverNet, clientGenNet, clientNet := config.GetEffectiveNetworkConfig()
        fmt.Println("Network Configuration:")
        fmt.Printf("  Server:           %s (metrics: %s)\n", serverNet.SRTAddr(), serverNet.MetricsAddr())
        fmt.Printf("  Client-Generator: %s (metrics: %s)\n", clientGenNet.IP, clientGenNet.MetricsAddr())
        fmt.Printf("  Client:           %s (metrics: %s)\n", clientNet.IP, clientNet.MetricsAddr())
        fmt.Println()

        // Print CLI flags for debugging
        fmt.Println("Server flags:", config.GetServerFlags())
        fmt.Println("Client-generator flags:", config.GetClientGeneratorFlags())
        fmt.Println("Client flags:", config.GetClientFlags())
        fmt.Println()

        // Run test with this configuration
        if err := runTestWithConfig(config); err != nil {
            fmt.Fprintf(os.Stderr, "✗ Test failed for configuration %s: %v\n", config.Name, err)
            failed++
            // Continue with next test (don't exit immediately)
            continue
        }

        fmt.Printf("✓ Configuration %s passed\n", config.Name)
        fmt.Println()
        passed++
    }

    fmt.Println()
    fmt.Printf("=== Test Summary: %d/%d passed, %d failed ===\n", passed, len(TestConfigs), failed)

    if failed > 0 {
        os.Exit(1)
    }
}

func runTestWithConfig(config TestConfig) error {
    // Get network configuration
    serverNet, clientGenNet, clientNet := config.GetEffectiveNetworkConfig()

    // Start server with configuration
    serverFlags := config.GetServerFlags()
    serverCmd := exec.Command(serverBin, serverFlags...)

    // Start client-generator with configuration
    clientGenFlags := config.GetClientGeneratorFlags()
    clientGenCmd := exec.Command(clientGenBin, clientGenFlags...)

    // Start client with configuration
    clientFlags := config.GetClientFlags()
    clientCmd := exec.Command(clientBin, clientFlags...)

    // Collect metrics from all components if enabled
    if config.MetricsEnabled {
        serverMetricsURL, clientGenMetricsURL, clientMetricsURL := config.GetAllMetricsURLs()
        // Start metrics collection goroutines for each component
        // serverMetricsURL: http://127.0.0.10:9090/metrics
        // clientGenMetricsURL: http://127.0.0.20:9091/metrics
        // clientMetricsURL: http://127.0.0.30:9092/metrics
        _, _, _ = serverMetricsURL, clientGenMetricsURL, clientMetricsURL
    }

    // ... rest of test execution ...

    // After test, compare metrics across components
    // verifyMetricsConsistency(serverNet, clientGenNet, clientNet, snapshots)
    _, _, _ = serverNet, clientGenNet, clientNet

    return nil
}
```

**Configuration Table Benefits**:
1. **Easy Adjustment**: All test parameters in one place
2. **Consistent Structure**: Same structure for all configurations
3. **Easy to Add/Remove**: Simple to add new bandwidths or modify existing ones
4. **Clear Documentation**: Each configuration is self-documenting

**Expected Enhancements**:
- Metrics collection at key points during test execution
- Error counter verification to catch unexpected errors
- Multiple bandwidth configurations to test different load scenarios
- Comparison of metrics across bandwidths to identify performance issues
- Clear test configuration table for easy maintenance

**Implementation Notes**:
- Metrics collection should be non-blocking (goroutine)
- Metrics parsing should handle Prometheus format correctly
- Error counter verification should account for expected errors (e.g., connection close errors during shutdown)
- Test should continue even if metrics collection fails (log warning, don't fail test)
- Metrics snapshots should be stored for post-test analysis

### Phase 4: Verification and Validation

#### Step 4.1: Verify No Goroutine Leaks

**Tasks**:
1. **Run with Race Detector**:
   ```bash
   go test -race -run TestGracefulShutdown ./contrib/integration_testing/...
   ```

2. **Check for Goroutine Leaks**:
   - [ ] No goroutine leaks detected
   - [ ] All goroutines exit cleanly
   - [ ] No race conditions detected

**Expected Outcome**: No goroutine leaks or race conditions.

#### Step 4.2: Verify Clean Shutdown Logs

**Tasks**:
1. **Review Logs from Manual Test**:
   - [ ] Client logs show clean shutdown (no errors)
   - [ ] Client-generator logs show clean shutdown (no errors)
   - [ ] Server logs show clean shutdown (no errors)
   - [ ] No "connection refused" errors
   - [ ] No "use of closed network connection" errors
   - [ ] No panic messages

**Expected Outcome**: All logs show clean shutdown.

#### Step 4.3: Verify Statistics

**Tasks**:
1. **Check Final Statistics** (if printed on shutdown):
   - [ ] Client shows final statistics before exit
   - [ ] Client-generator shows final statistics before exit
   - [ ] Server shows connection statistics before exit
   - [ ] Statistics are accurate (packets sent/received match)

**Expected Outcome**: Statistics are printed and accurate.

### Phase 5: Edge Case Testing

#### Step 5.1: Test with Different Bitrates

**Tasks**:
1. **Test with 1 Mb/s**:
   ```bash
   ./contrib/client-generator/client-generator -to srt://127.0.0.1:6000/test-stream -bitrate 1000000
   ```
   - [ ] Test passes with 1 Mb/s
   - [ ] Shutdown is still graceful

2. **Test with 10 Mb/s**:
   ```bash
   ./contrib/client-generator/client-generator -to srt://127.0.0.1:6000/test-stream -bitrate 10000000
   ```
   - [ ] Test passes with 10 Mb/s
   - [ ] Shutdown is still graceful

**Expected Outcome**: Test works with different bitrates.

#### Step 5.2: Test with Different Shutdown Delays

**Tasks**:
1. **Test with 1 second shutdown delay**:
   ```bash
   ./contrib/server/server -addr 127.0.0.1:6000 -shutdowndelay 1s
   ```
   - [ ] Server exits within 1 second
   - [ ] Shutdown is still graceful

2. **Test with 10 second shutdown delay**:
   ```bash
   ./contrib/server/server -addr 127.0.0.1:6000 -shutdowndelay 10s
   ```
   - [ ] Server exits within 10 seconds
   - [ ] Shutdown is still graceful

**Expected Outcome**: Test works with different shutdown delays.

#### Step 5.3: Test Abrupt Server Close

**Tasks**:
1. **Kill Server Abruptly** (without SIGINT):
   ```bash
   kill -9 <server_pid>
   ```
   - [ ] Client detects server close via `checkConnection()`
   - [ ] Client exits gracefully (within 2 seconds)
   - [ ] Client-generator detects server close
   - [ ] Client-generator exits gracefully

**Expected Outcome**: Clients detect abrupt server close and exit gracefully.

### Phase 6: Documentation Updates

#### Step 6.1: Update Implementation Progress

**Tasks**:
1. Update `context_and_cancellation_implementation.md`:
   - [ ] Mark Phase 8 (Testing) as complete
   - [ ] Document Test 1.1 results
   - [ ] Document any issues encountered and fixes

**Expected Outcome**: Implementation progress is documented.

#### Step 6.2: Update Test Design Document

**Tasks**:
1. Update `test_1.1_detailed_design.md`:
   - [ ] Mark implementation checklist items as complete
   - [ ] Document test results
   - [ ] Document any deviations from design

**Expected Outcome**: Design document reflects actual implementation.

### Phase 7: Rollback Plan

#### Step 7.1: Identify Rollback Triggers

**Rollback if**:
- Integration test fails consistently
- Goroutine leaks detected
- Race conditions detected
- Processes don't exit within shutdown delay
- "Connection refused" errors occur
- Any panic or crash occurs

#### Step 7.2: Rollback Procedure

**Tasks**:
1. **Revert Code Changes**:
   ```bash
   git checkout <previous-commit>
   ```

2. **Verify Previous Behavior**:
   - [ ] Previous implementation still works
   - [ ] Previous tests still pass

3. **Document Issues**:
   - [ ] Document what went wrong
   - [ ] Document why rollback was necessary
   - [ ] Create issues/tickets for fixes

**Expected Outcome**: Codebase is restored to previous working state.

### Phase 8: Success Criteria

#### All Criteria Must Pass

1. **Code Quality**:
   - [ ] All code changes reviewed and approved
   - [ ] No lint errors
   - [ ] No race conditions
   - [ ] No goroutine leaks

2. **Functionality**:
   - [ ] All three components start successfully
   - [ ] Stream flows correctly (client-generator → server → client)
   - [ ] All components shutdown gracefully on SIGINT
   - [ ] All components exit within shutdown delay
   - [ ] No "connection refused" errors

3. **Testing**:
   - [ ] Manual test passes
   - [ ] Automated integration test passes
   - [ ] Test passes 5 times in a row (no flakiness)
   - [ ] Edge cases tested and pass

4. **Documentation**:
   - [ ] Implementation progress documented
   - [ ] Test results documented
   - [ ] Design document updated

### Estimated Timeline

- **Phase 1 (Code Review)**: 1-2 hours
- **Phase 2 (Manual Testing)**: 2-3 hours
- **Phase 3 (Automated Testing)**: 1-2 hours
- **Phase 4 (Verification)**: 1-2 hours
- **Phase 5 (Edge Cases)**: 2-3 hours
- **Phase 6 (Documentation)**: 1 hour
- **Phase 7 (Rollback Plan)**: 0.5 hours (preparation only)

**Total Estimated Time**: 8.5-13.5 hours

### Risk Assessment

**Low Risk**:
- Code changes are already implemented
- Design is well-documented
- Test infrastructure exists

**Medium Risk**:
- Edge cases may reveal issues
- Different bitrates may expose performance issues
- Timing-sensitive shutdown may be flaky

**Mitigation**:
- Thorough testing in Phase 2-5
- Multiple test runs to check for flakiness
- Rollback plan ready if issues occur

---

## Future Improvements

1. **Connection Close Detection Tuning**:
   - Adjust `SetReadDeadline` timeout (currently 2 seconds)
   - Consider making it adaptive based on connection state

2. **Shutdown Packet Handling**:
   - Verify shutdown packets are sent and received correctly
   - Add logging for shutdown packet exchange

3. **Statistics on Shutdown**:
   - Print final statistics when connection closes
   - Include shutdown reason in statistics

4. **Error Handling**:
   - Improve error messages for connection close scenarios
   - Add defensive checks for edge cases

---

## Enhanced Integration Test Design (Future Implementation)

### Overview

This section describes planned enhancements to the integration test that are not yet implemented. These enhancements will provide better visibility into test execution and allow testing across multiple bandwidth configurations.

### Metrics Collection Enhancement

**Purpose**: Collect metrics snapshots from the server during test execution to verify error counters do not increment unexpectedly.

**Key Features**:
1. Enable metrics server on the SRT server during test
2. Periodically collect metrics from `/metrics` endpoint
3. Take snapshots at key points: startup, mid-test, pre-shutdown
4. Verify error counters (e.g., `PktDrop*`, `PktRecvError*`, `CryptoError*`) do not increment
5. Store snapshots for post-test analysis

**Implementation Details**: See Step 3.4.1 above.

### Multiple Bandwidth Configurations

**Purpose**: Test graceful shutdown across different bandwidth scenarios to ensure stability and performance.

**Key Features**:
1. Test configuration table with different bandwidths (1, 2, 5, 10 Mb/s)
2. Iterate through configurations, running test once per configuration
3. Collect metrics for each bandwidth configuration
4. Compare results across bandwidths

**Configuration Table**: See Step 3.4.2 above for the detailed table structure.

**Benefits**:
- Easy to adjust test parameters (all in one table)
- Consistent structure across configurations
- Easy to add new bandwidths or modify existing ones
- Clear documentation of test scenarios

---

## Related Documents

- `context_and_cancellation_design.md`: Overall design for context and cancellation
- `context_and_cancellation_implementation.md`: Implementation progress tracking
- `context_cancellation_testing_plan.md`: Overall testing strategy
- `peer_idle_timeout_design.md`: Peer idle timeout design (related to connection close detection)

