# Client-Seeker Test Scripts

This directory contains test scripts for verifying the client-seeker functionality.

## Scripts

### `test_control_socket.py`
Tests the JSON control protocol over Unix domain socket.

**Tests:**
- `get_status` - Get current bitrate and status
- `set_bitrate` - Change bitrate dynamically
- `heartbeat` - Keep-alive mechanism
- `invalid_command` - Error handling for unknown commands
- `invalid_json` - Error handling for malformed input
- `multiple_commands` - Multiple commands on single connection

**Usage:**
```bash
# With running client-seeker
./test_control_socket.py

# Custom socket path
./test_control_socket.py /tmp/custom.sock
```

### `test_metrics.py`
Tests the Prometheus metrics endpoint over Unix domain socket.

**Tests:**
- `metrics_endpoint` - Verify /metrics returns valid Prometheus format
- `required_metrics` - Check all required metrics are present
- `metric_values` - Verify all values are valid numbers
- `bitrate_consistency` - Check current and target bitrate match
- `health_endpoint` - Test /health endpoint
- `uptime_increases` - Verify uptime counter increments

**Usage:**
```bash
# With running client-seeker
./test_metrics.py

# Custom socket path
./test_metrics.py /tmp/custom_metrics.sock
```

### `integration_test.sh`
Full integration test that builds and runs all components.

**Steps:**
1. Build client-seeker and server binaries
2. Start server on localhost:6000
3. Start client-seeker connected to server
4. Run all Python test scripts
5. Clean up processes

**Usage:**
```bash
# Full test
./integration_test.sh

# Skip build step (use existing binaries)
./integration_test.sh --skip-build

# Keep processes running after tests (for debugging)
./integration_test.sh --keep-running
```

## Running Tests Manually

If you want to run tests against an already-running client-seeker:

```bash
# Start server (terminal 1)
cd ../../../contrib/server
./server -addr 127.0.0.1:6000

# Start client-seeker (terminal 2)
cd ../../../contrib/client-seeker
./client-seeker \
    -to srt://127.0.0.1:6000/test \
    -initial 100M \
    -control /tmp/seeker.sock \
    -seeker-metrics /tmp/seeker_metrics.sock

# Run tests (terminal 3)
./test_control_socket.py /tmp/seeker.sock
./test_metrics.py /tmp/seeker_metrics.sock
```

## Exit Codes

All test scripts use standard exit codes:
- `0` - All tests passed
- `1` - One or more tests failed (general)
- `2` - Server startup failure (integration_test.sh)
- `3` - Client-seeker startup failure (integration_test.sh)
- `4` - Test execution failure (integration_test.sh)

## Adding New Tests

To add new tests:

1. Add a test function to the appropriate Python script
2. Add the function to the `tests` list in `main()`
3. Follow the pattern: return `True` for pass, `False` for fail
4. Print progress with `print("  Testing X...", end=" ")`

Example:
```python
def test_new_feature(sock_path: str) -> bool:
    """Test description."""
    print("  Testing new_feature...", end=" ")

    # Test logic here
    if some_condition:
        print("OK")
        return True
    else:
        print("FAILED: reason")
        return False
```
