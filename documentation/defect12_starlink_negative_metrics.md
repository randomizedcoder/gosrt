# Defect 12: Starlink Test - Connection Death & Negative Metrics

**Status**: ✅ RESOLVED
**Priority**: High
**Discovered**: 2024-12-10
**Resolved**: 2024-12-10
**Related Documents**:
- `defect11_error_analysis_too_strict.md` - Parent defect document
- `packet_loss_injection_design.md` - Starlink pattern design
- `integration_testing_design.md` - Metrics collection design
- `graceful_quiesce_design.md` - Quiesce implementation

---

## Problem Statement

The `Network-Starlink-5Mbps` test has **two distinct failures**:

### Issue 1: Connection Death During Test (Primary)

The SRT connections die mid-test due to `peer_idle_timeout`:

```
[SUB] 17:32:00.36 |   424.0 pkt/s |    7.21 MB |    ← Last data received
[SUB] 17:32:01.36 |     0.0 pkt/s |    7.21 MB |    ← NEVER recovers
...                                                  ← 27 seconds of 0 pkt/s
connection_closed: peer_idle_timeout_remaining_seconds: 0   ← Connection dies
```

The subscriber stops receiving data at ~second 18 and **never recovers**, leading to:
- `peer_idle_timeout` after 30 seconds (as configured)
- Client process exits mid-test
- Test orchestrator continues running unaware
- User must Ctrl+C to stop

### Issue 2: Negative Packet Count (Secondary)

When connections die and reconnect, metrics can show negative deltas:

```
Metrics Summary:
  Server: recv'd 54932 packets, 6511 ACKs        ← CORRECT (positive)
  Client-Generator: recv'd 6901 ACKs              ← CORRECT (positive)
  Client: recv'd -244 packets, -221 ACKs          ← BUG: NEGATIVE!
```

This is impossible for monotonically increasing counters under normal operation.

---

## Latest Test Run Analysis (2024-12-10 17:31)

### Timeline of Failure

```
17:31:42  Test started
17:31:45  Starting impairment pattern: starlink
17:31:48  [SUB] receiving 244 pkt/s
17:31:49  [SUB] receiving 610 pkt/s (normal)
...
17:31:59  [SUB] receiving 612 pkt/s (still normal)
17:32:00  [SUB] receiving 424 pkt/s (degraded)
17:32:01  [SUB] receiving 0 pkt/s   ← STOPS HERE AND NEVER RECOVERS
...
17:32:27  connection_closed: peer_idle_timeout_remaining_seconds: 0
17:32:27  Server stats: pkt_send_drop: 15,624 (buffer overflow)
17:32:32  "Shutdown timed out after 5s" (from client binary)
...
17:33:15  User presses Ctrl+C to stop test
```

### Key Observations

1. **Pattern timing**: First Starlink event at second 57 (17:31:57) or 12 (17:31:54) didn't break connectivity
2. **Connectivity break at second ~18**: Not aligned with any Starlink event (12, 27, 42, 57)
3. **Server buffer overflow**: 15,624 packets dropped on Server→Subscriber path
4. **No quiesce phase**: Test never reached `--- Quiesce Phase ---` because connections died first
5. **Test orchestrator blind**: Didn't detect process death, kept running collect loop

### Test Context

### Starlink Pattern

The Starlink test simulates LEO satellite reconvergence events:
- **100% packet loss** for ~60ms
- Occurs at seconds **12, 27, 42, 57** of each minute
- Test duration: **90 seconds** (1.5 minutes = 6 total events)
- Uses **blackhole routes** (instant effect, no queue flush)

### Test Configuration

```go
{
    Name:        "Network-Starlink-5Mbps",
    Description: "Starlink reconvergence pattern (60ms 100% loss at 12,27,42,57s) at 5 Mb/s",
    Mode:        TestModeNetwork,
    Impairment: NetworkImpairment{
        Pattern:        "starlink",
        LatencyProfile: "regional", // LEO satellite has low latency normally
    },
    Bitrate:         5_000_000,
    TestDuration:    90 * time.Second, // Run for 1.5 minutes to see multiple events
    SharedSRT:       &LargeBuffersSRTConfig, // 3000ms latency/buffers
}
```

---

## Root Cause Hypotheses

### Hypothesis 1: Blackhole Route Not Being Cleared (CONFIRMED ✅ - FIXED)

**Root Cause Found**: The `clear_blackhole_loss()` function was NOT restoring the directly-connected
route for the Subscriber subnet on Router A.

**Network Topology** (from `defect10_high_loss_rate.md`):

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                   Host System                                            │
│                                                                                          │
│  ┌──────────────┐                                               ┌──────────────┐        │
│  │ ns_publisher │                                               │ns_subscriber │        │
│  │  (CG)        │                                               │  (Client)    │        │
│  │ 10.1.1.2     │                                               │ 10.1.2.2     │        │
│  └──────┬───────┘                                               └──────┬───────┘        │
│         │                                                              │                 │
│         │ veth                                                   veth  │                 │
│         ▼                                                              ▼                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                           ns_router_a (Client Router)                         │       │
│  │                                                                               │       │
│  │  eth_pub (10.1.1.1)                                    eth_sub (10.1.2.1)    │       │
│  │                                                                               │       │
│  │  link0_a ◄───────── netem loss 2% ─────────► link0_b (to Router B)          │       │
│  │                     + delay (profile)                                         │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth pair (link0)                              │
│                                        │                                                 │
│                                        ▼                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────────┐       │
│  │                          ns_router_b (Server Router)                          │       │
│  │                                                                               │       │
│  │  link0_b ◄───────── netem loss 2% ─────────► link0_a (to Router A)          │       │
│  │                     + delay (profile)                                         │       │
│  │                                                                               │       │
│  │  eth_srv (10.2.1.1)                                                          │       │
│  └──────────────────────────────────────────────────────────────────────────────┘       │
│                                        │                                                 │
│                                        │ veth                                            │
│                                        ▼                                                 │
│                               ┌──────────────┐                                          │
│                               │  ns_server   │                                          │
│                               │  (Server)    │                                          │
│                               │  10.2.1.2    │                                          │
│                               └──────────────┘                                          │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

**Original Bug** (using /24 blackhole routes):
1. `set_blackhole_loss()` runs: `ip route replace blackhole 10.1.2.0/24` on Router A
2. This **destroys** the kernel's directly-connected route (`10.1.2.0/24 dev eth_sub`)
3. `clear_blackhole_loss()` runs: `ip route del blackhole 10.1.2.0/24`
4. The blackhole is removed, but the **original route is NOT restored**!
5. Router A now has **no route to the Subscriber** (10.1.2.0/24)
6. Server → Router B → Router A → Subscriber path is broken

**Evidence from verbose route dump**:
```
After clearing loss (17:59:27.170):
Router A (ns_router_a):
   10.1.1.0/24 dev eth_pub  ← Publisher: OK
   10.2.1.0/24 via ...      ← Server: OK
   [MISSING: 10.1.2.0/24]   ← Subscriber: MISSING! ❌
```

**Fix Applied** (in `lib.sh`) - Use /32 host-specific blackhole routes:

The fix uses /32 host routes instead of /24 subnet routes:

```bash
# OLD (broken): /24 routes that destroy existing routes
ip route replace blackhole 10.1.2.0/24  # Destroys kernel route!

# NEW (correct): /32 host routes that shadow existing routes
ip route add blackhole 10.1.1.2/32  # Publisher
ip route add blackhole 10.1.2.2/32  # Subscriber
ip route add blackhole 10.2.1.2/32  # Server
```

**Why /32 routes are better**:
1. **Longest prefix match wins**: /32 is more specific than /24
2. **Original routes untouched**: No destruction of kernel routes
3. **Simple cleanup**: Just remove /32 routes, no restoration needed
4. **Same logic on both routers**: Symmetric and predictable

### Hypothesis 2: Connection Lifecycle Metrics Analysis Bug (CONFIRMED ✅ - FIXED)

After fixing the blackhole routing, the test showed a new failure:

```
Connection Lifecycle: ✗ FAILED
  ✗ Server: expected 2 connections established, got 0
  ✗ Client-Generator: expected 1 connections established, got 0
  ✗ Client: expected 1 connections established, got 0
```

**Root Cause**: The analysis code was using **deltas** between first and last snapshots:

```go
established = int64(getMetricValue(last, "gosrt_connections_established_total") -
    getMetricValue(first, "gosrt_connections_established_total"))
```

But the test flow collects "initial metrics" AFTER connections are already established:
1. Start server, client-generator, client
2. Wait for connections to establish (`ConnectionWait`)
3. Collect "initial" metrics ← connections already established! (`ConnectionsEstablished = 2`)
4. Run test
5. Collect "final" metrics (`ConnectionsEstablished = 2` - same value)
6. Delta = 0 ❌

**Fix** (in `analysis.go`):

1. **Use ABSOLUTE values** from the final snapshot instead of deltas. Since each test
   runs fresh processes that start with 0 connections, the absolute count IS the test result.

2. **Change lifecycle check from "imbalance" to "premature closure"**. Per `graceful_quiesce_design.md`,
   we collect pre-shutdown metrics while connections are still OPEN. The correct check is:

   - ✅ `established == expected` (verify connections were made)
   - ✅ `closed == 0` (verify connections survived the test - especially important for Starlink!)
   - ✅ `peer_idle_timeout == 0` (verify 30s timeout wasn't triggered)

```go
// OLD: Expected established == closed (wrong - connections are still open during pre-shutdown!)
if result.ServerEstablished != result.ServerClosed { ... }

// NEW: Verify connections survived the test (should still be open)
if result.ServerClosed != 0 {
    // premature_closure violation - connections closed during test!
}
```

This aligns with the graceful quiesce design and explicitly verifies that Starlink outages
don't cause connection failures.

### Hypothesis 2: Go Pattern Goroutine Race Condition

**Theory**: The `runPattern()` goroutine in `network_controller.go` has timing issues:
```go
// Apply the loss
_ = nc.runScriptUnlocked(ctx, "set_loss.sh", "100")  // Blackhole

// Wait for event duration (60ms)
time.After(60 * time.Millisecond)

// Clear the loss
_ = nc.runScriptUnlocked(ctx, "set_loss.sh", "0")    // Clear
```

If the context is cancelled or the script errors, the clear may not happen.

**Investigation**:
1. Check if script errors are being silently swallowed (underscore return value)
2. Add error logging to pattern execution

### Hypothesis 3: Bidirectional Route Issue

**Theory**: The blackhole routes are applied/cleared, but only in one direction:
- Publisher→Server path recovers
- Server→Subscriber path stays broken

**Evidence from lib.sh**:
```bash
# Router A: Block traffic TO server and subscriber subnets
# Router B: Block traffic TO publisher and subscriber subnets
```

If `clear_blackhole_loss()` only clears Router A, Server→Subscriber stays broken.

### Hypothesis 4: Test Orchestrator Doesn't Detect Dead Processes

**Separate bug**: The test orchestrator's `collectLoop` doesn't monitor process health:
```go
for {
    select {
    case <-collectTicker.C:
        testMetrics.CollectAllMetrics("mid-test")
    case <-testTimer.C:
        break collectLoop
    }
}
```

When client process dies, the loop continues running until test duration expires.

**Fix Needed**: Add process health monitoring to detect early exits.

---

## Investigation Plan

### Phase 0: Debug Starlink Pattern Execution (HIGHEST PRIORITY)

**Status**: ✅ Implemented

**Goal**: Determine why connectivity breaks and doesn't recover.

**Implementation** (committed 2024-12-10):

1. **Added `Verbose` flag to `NetworkController`** (`network_controller.go`):
   - `NetworkControllerConfig.Verbose` - enables detailed logging
   - Logs all pattern events with timestamps
   - Dumps route tables from both routers after each loss change

2. **Fixed silent error swallowing**:
   - Changed `_ = nc.runScriptUnlocked(...)` to actually log errors
   - Errors now printed to stderr with `[PATTERN] ERROR:` prefix

3. **Added route table dumping** (`dumpRouteTables()`):
   - Prints routes from both Router A and Router B
   - Highlights blackhole routes with `>>> ... <<<`
   - Called after every loss apply/clear operation

4. **Enabled `SRT_NETWORK_DEBUG=1`** when verbose mode is on:
   - Shell scripts in `network/` use `log_debug` for additional output
   - Automatically passed via `buildScriptEnv()` helper

5. **Wired to test config**:
   - Added `VerboseNetwork` field to `TestConfig`
   - `-v` or `--verbose` flag now enables both metrics and network logging

**Usage**:
```bash
# Run Starlink test with verbose network logging
sudo make test-network CONFIG=Network-Starlink-5Mbps VERBOSE=1
```

**Expected Output** (when verbose):
```
[PATTERN] Starting pattern "starlink" with 4 events, repeat=1m0s
[PATTERN] Next event: 100% loss for 60ms, waiting 12s
[PATTERN] Event #1: Applying 100% loss at 17:31:54.123
[ROUTES] Route tables at 17:31:54.123:
[ROUTES] Router A (ns_router_a_test_123):
[ROUTES]   >>> blackhole 10.2.1.0/24 <<<
[ROUTES]   >>> blackhole 10.1.2.0/24 <<<
[ROUTES] Router B (ns_router_b_test_123):
[ROUTES]   >>> blackhole 10.1.1.0/24 <<<
[ROUTES]   >>> blackhole 10.1.2.0/24 <<<
[PATTERN] Event #1: Clearing loss at 17:31:54.183 (after 60ms)
[ROUTES] Route tables at 17:31:54.183:
[ROUTES] Router A (ns_router_a_test_123):
[ROUTES]   10.2.1.0/24 via 10.0.1.2 dev link1_a
[ROUTES] Router B (ns_router_b_test_123):
[ROUTES]   10.1.1.0/24 via 10.0.1.1 dev link1_b
```

### Phase 1: Add Process Health Monitoring (QUICK WIN)

**Goal**: Detect when processes die mid-test.

**Changes to `test_network_mode.go`**:
```go
// In collectLoop, add process health check
case <-collectTicker.C:
    // Check if processes are still alive
    if !isProcessRunning(clientCmd) {
        fmt.Println("ERROR: Client process died unexpectedly!")
        break collectLoop
    }
    if !isProcessRunning(serverCmd) {
        fmt.Println("ERROR: Server process died unexpectedly!")
        break collectLoop
    }
    testMetrics.CollectAllMetrics("mid-test")
```

### Phase 2: Connection Lifecycle Metrics (ALREADY IMPLEMENTED)

**Status**: ✅ Completed in `defect12_phase0_connection_lifecycle_metrics.md`

**Metrics Available**:
```
gosrt_connections_established_total      # Total connections established
gosrt_connections_closed_total           # Total connections closed
gosrt_connections_closed_by_reason_total{reason=X} # By reason
gosrt_connections_active                 # Current active connections
```

**Verification**: Run Starlink test and check:
- `gosrt_connections_closed_by_reason_total{reason="peer_idle_timeout"}` should show the closures

### Phase 3: Defensive Metrics Handling

**Goal**: Prevent negative deltas from causing confusing output.

**Changes**:
1. In `ComputeDerivedMetrics()`, detect negative deltas
2. Log warning with specific reason
3. Flag test as having connection issues

```go
if dm.TotalPacketsRecv < 0 {
    log.Printf("WARNING: Negative packet count (%d) - connection replacement detected", dm.TotalPacketsRecv)
    dm.ConnectionReplacementDetected = true
}
```

### Phase 4: Alternative Starlink Pattern Implementation

**If Go pattern is buggy**: Use the shell script implementation instead:

```bash
# In contrib/integration_testing/network/starlink_pattern.sh
sudo ./starlink_pattern.sh start   # Run in background
# ... test runs ...
sudo ./starlink_pattern.sh stop    # Cleanup
```

This avoids the Go goroutine pattern controller entirely.

---

## Expected Behavior

For the Starlink test to pass:

1. **Connection should survive** 60ms outages (SRT buffers can hold 3000ms of data)
2. **Packets should be recovered** via NAK/retransmission after each event
3. **Recovery rate should be high** (≥85% per config)
4. **No negative metrics** - deltas should always be positive or zero

---

## Workaround Options

### Option A: Skip Client Metrics for Pattern Tests

For pattern-based tests, only validate Server and Client-Generator metrics (which work correctly).

```go
// In ValidatePositiveSignals()
if config.Impairment.Pattern != "" {
    // Skip Client validation for pattern tests
    clientDataFlowOK = true // Assume OK
}
```

### Option B: Extend Test Duration with Stable Periods

Ensure initial and final metrics are collected during stable periods (not during loss events):
- Wait 15+ seconds after test start before collecting initial metrics
- Collect final metrics 15+ seconds before test end

### Option C: Use Absolute Values

For pattern tests, use absolute final values instead of deltas:
- `TotalPacketsRecv = getSumByPrefix(last, ...)` (no subtraction)
- This captures total activity regardless of connection changes

---

## Acceptance Criteria

1. `Network-Starlink-5Mbps` test passes consistently
2. No negative metric values in any test output
3. If connection replacement occurs, it's detected and reported clearly
4. Recovery rate calculation is accurate despite connection changes

