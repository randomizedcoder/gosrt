#!/usr/bin/env python3
"""
Test script for performance orchestrator timing model validation.

This script tests that the timing contracts are properly enforced.

Usage:
    ./test_timing_model.py [path_to_performance_binary]

Exit codes:
    0 - All tests passed
    1 - One or more tests failed
"""

import subprocess
import sys
import os

DEFAULT_BINARY = "../performance"


def run_performance(binary: str, args: list, expect_success: bool = True) -> tuple:
    """
    Run the performance binary with given arguments.

    Returns:
        (success, stdout, stderr)
    """
    cmd = [binary, "-dry-run"] + args
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=5
        )
        success = result.returncode == 0
        return success, result.stdout, result.stderr
    except subprocess.TimeoutExpired:
        return False, "", "Timeout"
    except FileNotFoundError:
        return False, "", f"Binary not found: {binary}"


def test_default_config(binary: str) -> bool:
    """Test that default configuration is valid."""
    print("  Testing default config...", end=" ")

    success, stdout, stderr = run_performance(binary, [])

    if not success:
        print(f"FAILED: {stderr}")
        return False

    if "Configuration valid" not in stdout:
        print(f"FAILED: expected 'Configuration valid' in output")
        return False

    print("OK")
    return True


def test_custom_valid_config(binary: str) -> bool:
    """Test that custom valid configuration passes."""
    print("  Testing custom valid config...", end=" ")

    args = ["INITIAL=100M", "MAX=500M", "STEP=20M", "FC=204800"]
    success, stdout, stderr = run_performance(binary, args)

    if not success:
        print(f"FAILED: {stderr}")
        return False

    print("OK")
    return True


def test_invalid_warmup(binary: str) -> bool:
    """Test that invalid warm-up duration is rejected."""
    print("  Testing invalid warm-up...", end=" ")

    # WarmUp must be > 2 × RampUpdateInterval (100ms default)
    # So WarmUp=100ms should fail
    args = ["WARMUP=100ms"]
    success, stdout, stderr = run_performance(binary, args)

    if success:
        print("FAILED: expected contract violation")
        return False

    if "WARMUP_TOO_SHORT" not in stderr:
        print(f"FAILED: expected WARMUP_TOO_SHORT in error, got: {stderr}")
        return False

    print("OK (got expected violation)")
    return True


def test_invalid_stability_window(binary: str) -> bool:
    """Test that invalid stability window is rejected."""
    print("  Testing invalid stability window...", end=" ")

    # StabilityWindow must be > 3 × SampleInterval (500ms default)
    # So StabilityWindow=1s with SampleInterval=500ms should fail (only 2 samples)
    args = ["STABILITY=1s", "SAMPLE_INTERVAL=500ms"]
    success, stdout, stderr = run_performance(binary, args)

    if success:
        print("FAILED: expected contract violation")
        return False

    if "STABILITY_TOO_SHORT" not in stderr:
        print(f"FAILED: expected STABILITY_TOO_SHORT in error, got: {stderr}")
        return False

    print("OK (got expected violation)")
    return True


def test_config_parsing(binary: str) -> bool:
    """Test that configuration values are parsed correctly."""
    print("  Testing config parsing...", end=" ")

    args = ["INITIAL=300M", "MAX=800M"]
    success, stdout, stderr = run_performance(binary, args)

    if not success:
        print(f"FAILED: {stderr}")
        return False

    # Check that values appear in output
    if "300.00 Mb/s" not in stdout:
        print(f"FAILED: expected '300.00 Mb/s' in output")
        return False

    if "800.00 Mb/s" not in stdout:
        print(f"FAILED: expected '800.00 Mb/s' in output")
        return False

    print("OK")
    return True


def test_invalid_bitrate(binary: str) -> bool:
    """Test that invalid bitrate values are rejected."""
    print("  Testing invalid bitrate...", end=" ")

    args = ["INITIAL=not-a-number"]
    success, stdout, stderr = run_performance(binary, args)

    if success:
        print("FAILED: expected error for invalid bitrate")
        return False

    if "Configuration error" not in stderr:
        print(f"FAILED: expected 'Configuration error' in stderr, got: {stderr}")
        return False

    print("OK (got expected error)")
    return True


def test_help_flag(binary: str) -> bool:
    """Test that -help flag works."""
    print("  Testing -help flag...", end=" ")

    cmd = [binary, "-help"]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=5)
        if result.returncode != 0:
            print(f"FAILED: non-zero exit code")
            return False

        if "Usage:" not in result.stdout:
            print(f"FAILED: expected 'Usage:' in help output")
            return False

        print("OK")
        return True
    except Exception as e:
        print(f"FAILED: {e}")
        return False


def test_version_flag(binary: str) -> bool:
    """Test that -version flag works."""
    print("  Testing -version flag...", end=" ")

    cmd = [binary, "-version"]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=5)
        if result.returncode != 0:
            print(f"FAILED: non-zero exit code")
            return False

        if "performance" not in result.stdout.lower():
            print(f"FAILED: expected 'performance' in version output")
            return False

        print("OK")
        return True
    except Exception as e:
        print(f"FAILED: {e}")
        return False


def main():
    binary = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_BINARY

    # Resolve path
    if not os.path.isabs(binary):
        # First try relative to current directory
        if os.path.exists(binary):
            binary = os.path.abspath(binary)
        else:
            # Then try relative to script directory
            script_dir = os.path.dirname(os.path.abspath(__file__))
            binary = os.path.join(script_dir, binary)

    print(f"Testing performance orchestrator: {binary}")
    print("=" * 60)

    # Check binary exists
    if not os.path.exists(binary):
        print(f"ERROR: Binary not found: {binary}")
        print("Build with: cd contrib/performance && go build -o performance")
        return 1

    tests = [
        ("help_flag", test_help_flag),
        ("version_flag", test_version_flag),
        ("default_config", test_default_config),
        ("custom_valid_config", test_custom_valid_config),
        ("config_parsing", test_config_parsing),
        ("invalid_bitrate", test_invalid_bitrate),
        ("invalid_warmup", test_invalid_warmup),
        ("invalid_stability_window", test_invalid_stability_window),
    ]

    passed = 0
    failed = 0

    for name, test_func in tests:
        try:
            if test_func(binary):
                passed += 1
            else:
                failed += 1
        except Exception as e:
            print(f"  Testing {name}... EXCEPTION: {e}")
            failed += 1

    print("=" * 60)
    print(f"Results: {passed} passed, {failed} failed")

    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
