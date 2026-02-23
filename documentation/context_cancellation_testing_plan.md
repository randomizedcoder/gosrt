# Context and Cancellation Testing Plan

## Overview

This document outlines the testing strategy for Phase 8: Testing and Validation of the context and cancellation implementation.

## Test Categories

### 1. Signal Handling Tests

#### Test 1.1: Graceful Shutdown on SIGINT
**Objective**: Verify that sending SIGINT to the server/client triggers graceful shutdown

**Steps**:
1. Start server/client
2. Establish one or more connections
3. Send SIGINT signal
4. Verify:
   - Root context is cancelled
   - All goroutines exit cleanly
   - All connections are closed
   - Shutdown packets are sent
   - Application exits within shutdown delay

**Expected Behavior**:
- Server/client detects signal and cancels root context
- All child contexts are cancelled (cascade)
- All goroutines exit (no goroutine leaks)
- Connections send shutdown packets
- Application exits gracefully

#### Test 1.2: Graceful Shutdown on SIGTERM
**Objective**: Verify that sending SIGTERM to the server/client triggers graceful shutdown

**Steps**: Same as Test 1.1, but use SIGTERM instead of SIGINT

**Expected Behavior**: Same as Test 1.1

### 2. Timeout Cancellation Tests

#### Test 2.1: Handshake Timeout Cancellation on Signal
**Objective**: Verify that handshake timeout is cancelled when signal is received

**Steps**:
1. Start server
2. Start client with very long handshake timeout (e.g., 30 seconds)
3. Send SIGINT before handshake completes
4. Verify:
   - Handshake timeout context is cancelled
   - Connection does not timeout (timeout cancelled by signal)
   - Shutdown proceeds normally

**Expected Behavior**:
- Handshake timeout context inherits from root context
- Signal cancellation cancels timeout before it expires
- Connection closes due to signal, not timeout

### 3. Connection Cleanup Tests

#### Test 3.1: Single Connection Cleanup
**Objective**: Verify that a single connection is cleaned up properly on shutdown

**Steps**:
1. Start server
2. Establish one connection
3. Send SIGINT
4. Verify:
   - Connection's `close()` is called
   - Connection context is cancelled
   - All connection goroutines exit
   - Connection is removed from listener's connection map
   - Metrics are unregistered

**Expected Behavior**:
- Connection cleanup completes before listener shutdown
- No resource leaks
- All goroutines exit

#### Test 3.2: Multiple Connections Cleanup
**Objective**: Verify that multiple connections are cleaned up properly on shutdown

**Steps**:
1. Start server
2. Establish multiple connections (e.g., 10 connections)
3. Send SIGINT
4. Verify:
   - All connections are closed
   - All connection goroutines exit
   - All connections are removed from listener's connection map
   - All metrics are unregistered

**Expected Behavior**:
- All connections cleaned up in parallel
- Shutdown completes within reasonable time
- No connection leaks

### 4. WaitGroup Tests

#### Test 4.1: WaitGroup Completion Before Shutdown Delay
**Objective**: Verify that shutdown completes immediately if waitgroups complete before delay expires

**Steps**:
1. Start server with short shutdown delay (e.g., 1 second)
2. Establish connections
3. Send SIGINT
4. Measure time from signal to exit
5. Verify:
   - Exit occurs before shutdown delay expires (if waitgroups complete quickly)
   - All cleanup is complete

**Expected Behavior**:
- If waitgroups complete quickly, shutdown proceeds immediately
- Application doesn't wait for full delay if cleanup is fast

#### Test 4.2: WaitGroup Timeout (Shutdown Delay Expires)
**Objective**: Verify that application exits after shutdown delay even if waitgroups don't complete

**Steps**:
1. Start server with short shutdown delay (e.g., 1 second)
2. Establish connections
3. Simulate slow cleanup (e.g., add delay in connection close)
4. Send SIGINT
5. Verify:
   - Application exits after shutdown delay expires
   - Some cleanup may still be in progress (acceptable)

**Expected Behavior**:
- Application exits after delay expires
- Timeout prevents indefinite blocking
- Remaining cleanup happens in background (acceptable)

### 5. Goroutine Exit Tests

#### Test 5.1: Verify All Goroutines Exit Cleanly
**Objective**: Verify that all goroutines exit when context is cancelled

**Steps**:
1. Start server
2. Establish connections
3. Record initial goroutine count
4. Send SIGINT
5. Wait for shutdown
6. Record final goroutine count
7. Verify:
   - Goroutine count decreases
   - No goroutine leaks
   - All goroutines exit cleanly

**Expected Behavior**:
- All goroutines exit
- No goroutine leaks
- Goroutine count returns to baseline

**Implementation**:
- Use `runtime.NumGoroutine()` to track goroutine count
- Compare before and after shutdown

### 6. Race Detector Tests

#### Test 6.1: Run Race Detector on Shutdown
**Objective**: Verify no race conditions during shutdown

**Steps**:
1. Build with `-race` flag
2. Run server with multiple connections
3. Send SIGINT
4. Verify:
   - No race conditions detected
   - Shutdown completes successfully

**Expected Behavior**:
- No race conditions reported
- Clean shutdown

**Implementation**:
```bash
go test -race ./...
go build -race ./contrib/server
go build -race ./contrib/client
```

### 7. Handshake Timeout Validation Tests

#### Test 7.1: Handshake Timeout Validation
**Objective**: Verify that handshake timeout validation works correctly

**Steps**:
1. Create config with `HandshakeTimeout >= PeerIdleTimeout`
2. Call `config.Validate()`
3. Verify:
   - Validation fails with appropriate error
   - Error message is clear

**Expected Behavior**:
- Validation rejects invalid configurations
- Error message explains the issue

#### Test 7.2: Handshake Timeout Expiration
**Objective**: Verify that handshake timeout expires correctly

**Steps**:
1. Start server
2. Start client with very short handshake timeout (e.g., 100ms)
3. Don't complete handshake (e.g., block on network)
4. Verify:
   - Handshake timeout expires
   - Connection is closed
   - Error indicates timeout

**Expected Behavior**:
- Handshake timeout fires after configured duration
- Connection closes with timeout error

### 8. Peer Idle Timeout Tests

#### Test 8.1: Peer Idle Timeout with Atomic Counter
**Objective**: Verify that peer idle timeout works correctly with atomic counter approach

**Steps**:
1. Start server
2. Establish connection
3. Stop sending packets from client
4. Verify:
   - Peer idle timeout fires after configured duration
   - Connection is closed
   - `PktRecvSuccess` counter is checked correctly

**Expected Behavior**:
- Timeout fires when no packets received
- Atomic counter check works correctly
- Connection closes with timeout reason

#### Test 8.2: Peer Idle Timeout Reset on Packet
**Objective**: Verify that peer idle timeout resets when packets are received

**Steps**:
1. Start server
2. Establish connection
3. Send packets periodically (before timeout expires)
4. Verify:
   - Timeout is reset on each packet
   - Connection stays alive
   - Timer reset is lock-free

**Expected Behavior**:
- Timeout resets on each packet
- Connection remains active
- No lock contention

### 9. Crypto Error Counter Tests

#### Test 9.1: Verify Crypto Error Counters
**Objective**: Verify that crypto error counters are tracked correctly

**Steps**:
1. Start server with metrics enabled
2. Establish connection
3. Simulate crypto errors (if possible) or check counters are initialized
4. Query Prometheus metrics endpoint
5. Verify:
   - Crypto error counters exist
   - Counters are initialized to 0
   - Counters can be queried via Prometheus

**Expected Behavior**:
- Counters are exposed in Prometheus
- Counters start at 0
- Counters increment when errors occur (if testable)

## Test Implementation Strategy

### Unit Tests

Create test files:
- `context_cancellation_test.go` - Tests for context cancellation behavior
- `shutdown_test.go` - Tests for graceful shutdown
- `timeout_test.go` - Tests for timeout behavior

### Integration Tests

**Automated Integration Tests** (Implemented):
- `contrib/integration_testing/test_graceful_shutdown.go` - Go program that orchestrates server, client-generator, and client processes
- Uses `os/exec` to start processes
- Sends signals and verifies graceful shutdown
- Tests Test 1.1 (Graceful Shutdown on SIGINT)

**Components Created**:
- `contrib/client-generator/` - Publisher that generates data and sends to server
- Flow: `client-generator -> server -> client`
- All components use context-based cancellation

**Makefile Targets**:
- `make client-generator` - Build client-generator binary
- `make test-integration` - Run integration tests

### Manual Testing

Document manual testing procedures:
- How to test graceful shutdown
- How to verify goroutine exit
- How to test with multiple connections

## Test Execution

### Running Unit Tests
```bash
go test -v ./... -run TestContextCancellation
go test -v ./... -run TestShutdown
go test -v ./... -run TestTimeout
```

### Running Race Detector
```bash
go test -race ./...
go build -race ./contrib/server
go build -race ./contrib/client
```

### Running Integration Tests
```bash
./test_shutdown.sh
./test_signals.sh
./test_timeouts.sh
```

## Success Criteria

All tests should:
1. ✅ Pass consistently
2. ✅ No race conditions detected
3. ✅ No goroutine leaks
4. ✅ No resource leaks
5. ✅ Graceful shutdown completes within shutdown delay
6. ✅ All contexts are cancelled correctly
7. ✅ All waitgroups complete
8. ✅ All connections are cleaned up

## Next Steps

1. Create unit test file `context_cancellation_test.go`
2. Create integration test scripts
3. Run all tests
4. Document test results
5. Fix any issues found
6. Update implementation document with test results

