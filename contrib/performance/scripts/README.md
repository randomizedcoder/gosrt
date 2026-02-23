# Performance Orchestrator Test Scripts

This directory contains test scripts for verifying the performance orchestrator functionality.

## Available Scripts

### `test_timing_model.py` ✅ (Phase 4)
Tests the timing model and contract validation.

**Tests:**
- `help_flag` - Verify -help flag works
- `version_flag` - Verify -version flag works
- `default_config` - Default configuration is valid
- `custom_valid_config` - Custom valid configurations pass
- `config_parsing` - Configuration values are parsed correctly
- `invalid_bitrate` - Invalid bitrate values are rejected
- `invalid_warmup` - Invalid warm-up duration triggers contract violation
- `invalid_stability_window` - Invalid stability window triggers contract violation

**Usage:**
```bash
./test_timing_model.py                    # Use default binary path
./test_timing_model.py /path/to/performance  # Custom binary path
```

### `integration_test.sh` ✅ (Phase 4)
Full integration test that builds and runs all tests.

**Steps:**
1. Build performance, client-seeker, and server binaries
2. Run Go unit tests
3. Run timing model validation tests
4. Run integration tests (start/stop processes)

**Usage:**
```bash
./integration_test.sh                # Full test
./integration_test.sh --skip-build   # Skip build step
./integration_test.sh --quick        # Skip long-running tests
```

## Planned Scripts (Future Phases)

### `test_process_manager.py` (Phase 4+)
- Process startup/shutdown
- Readiness barrier
- Prometheus scraping

### `test_stability_gate.py` (Phase 5)
- Warm-up period handling
- Stability evaluation
- Critical threshold detection
- Profile capture triggers

### `test_search_loop.py` (Phase 6)
- Monotonicity invariants
- Binary search convergence
- AIMD behavior
- Ceiling proof logic

## Directory Structure

```
scripts/
├── README.md               # This file
├── test_timing_model.py    # ✅ Phase 4 (implemented)
├── integration_test.sh     # ✅ Phase 4 (implemented)
├── test_process_manager.py # Phase 4+ (planned)
├── test_stability_gate.py  # Phase 5 (planned)
└── test_search_loop.py     # Phase 6 (planned)
```

## Exit Codes

All test scripts use standard exit codes:
- `0` - All tests passed
- `1` - Build failed / general failure
- `2` - Unit tests failed
- `3` - Timing model tests failed
- `4` - Integration test failed
