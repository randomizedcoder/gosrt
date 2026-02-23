# Connection Age and Process Uptime Metrics Design

## Status: IMPLEMENTED ✅

Both process start time and connection start time metrics are now exposed as Prometheus metrics.

## Implemented Metrics

### 1. Process Start Time Metric

**Metric**: `gosrt_process_start_time_seconds`
**Type**: Gauge (constant value)
**Location**: `metrics/runtime.go`
**Description**: Unix timestamp of when the gosrt library was initialized

```
gosrt_process_start_time_seconds 1735590123
```

**Usage**: Calculate process uptime as `current_time - gosrt_process_start_time_seconds`

### 2. Connection Start Time Metric

**Metric**: `gosrt_connection_start_time_seconds`
**Type**: Gauge (per-connection)
**Labels**: `socket_id`, `instance`, `remote_addr`, `stream_id`, `peer_type`
**Location**: `metrics/handler.go`
**Description**: Unix timestamp of when each connection was established

```
gosrt_connection_start_time_seconds{socket_id="0x12345678",instance="baseline-server",remote_addr="10.1.1.2:45678",stream_id="publish:/test-stream",peer_type="publisher"} 1735590125
```

**Usage**: Calculate connection age as `current_time - gosrt_connection_start_time_seconds`

## Implementation Details

### Files Modified

1. **`metrics/runtime.go`**
   - Added `GetProgramStartTime()` exported function
   - Added `gosrt_process_start_time_seconds` gauge output

2. **`metrics/registry.go`**
   - Added `StartTime time.Time` field to `ConnectionInfo` struct

3. **`metrics/handler.go`**
   - Added `gosrt_connection_start_time_seconds` gauge output with labels

4. **`connection.go`**
   - Updated `createConnectionMetrics()` to accept `startTime time.Time` parameter

5. **`conn_request.go`**
   - Pass `req.start` to `createConnectionMetrics()`

6. **`dial_handshake.go`**
   - Pass `dl.start` to `createConnectionMetrics()`

7. **Test files updated**:
   - `metrics/handler_test.go` - Added `TestProcessStartTimeMetric` and `TestConnectionStartTimeMetric`
   - `metrics/listener_metrics_test.go` - Updated `newTestConnectionInfoForListener`
   - `metrics/stabilization_test.go` - Updated `newTestConnectionInfoForStab`
   - `connection_nakbtree_test.go` - Updated `createConnectionMetrics` call
   - `connection_io_uring_bench_test.go` - Updated `createConnectionMetrics` call

## Integration Test Usage

### Verify Process Stability

```go
// In integration test, after collecting final metrics
func verifyProcessStability(metricsData string, testDuration time.Duration, tolerance time.Duration) error {
    // Parse gosrt_process_start_time_seconds from metrics
    processStartTime := parseMetric(metricsData, "gosrt_process_start_time_seconds")

    now := time.Now().Unix()
    expectedUptime := testDuration.Seconds()
    actualUptime := float64(now) - processStartTime

    if math.Abs(actualUptime - expectedUptime) > tolerance.Seconds() {
        return fmt.Errorf("process may have restarted: uptime=%.0fs, expected≈%.0fs (±%.0fs)",
            actualUptime, expectedUptime, tolerance.Seconds())
    }
    return nil
}
```

### Verify Connection Stability

```go
// Verify connections haven't been replaced during test
func verifyConnectionStability(metricsData string, testDuration time.Duration, tolerance time.Duration) error {
    // Parse all gosrt_connection_start_time_seconds metrics
    connectionStartTimes := parseAllConnectionStartTimes(metricsData)

    now := time.Now().Unix()
    expectedMinAge := testDuration.Seconds() - tolerance.Seconds()

    for socketId, startTime := range connectionStartTimes {
        connectionAge := float64(now) - startTime

        if connectionAge < expectedMinAge {
            return fmt.Errorf("connection %s may have reconnected: age=%.0fs, expected≥%.0fs",
                socketId, connectionAge, expectedMinAge)
        }
    }
    return nil
}
```

### What This Detects

| Issue | Detection Method |
|-------|------------------|
| Process crash/restart | `process_start_time` will be recent, not matching test start |
| Connection drop/reconnect | `connection_start_time` will show reconnection time |
| Socket ID collision | New connection's start time differs from expected |

## Example Prometheus Output

```
# Process-level metric
gosrt_process_start_time_seconds 1735590123

# Connection-level metrics (one per active connection)
gosrt_connection_start_time_seconds{socket_id="0x12345678",instance="baseline-cg",remote_addr="10.2.1.2:6000",stream_id="publish:/test-stream-baseline",peer_type="unknown"} 1735590125
gosrt_connection_start_time_seconds{socket_id="0x87654321",instance="baseline-server",remote_addr="10.1.1.2:45678",stream_id="publish:/test-stream-baseline",peer_type="publisher"} 1735590125
gosrt_connection_start_time_seconds{socket_id="0xabcdef01",instance="baseline-client",remote_addr="10.2.1.2:6000",stream_id="subscribe:/test-stream-baseline",peer_type="subscriber"} 1735590128
```

## Testing

Unit tests verify:
1. `TestProcessStartTimeMetric` - Process start time is exported and within reasonable range
2. `TestConnectionStartTimeMetric` - Connection start time is exported with all expected labels

Run tests:
```bash
go test ./metrics/... -v -run "TestProcess|TestConnection.*Start"
```
