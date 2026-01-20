#!/usr/bin/env python3
"""
Test script for performance orchestrator stability gate.

This script tests the StabilityGate's verdict logic using Go unit tests
and validates the hypothesis analysis output.

Usage:
    ./test_stability_gate.py [path_to_performance_binary]

Exit codes:
    0 - All tests passed
    1 - One or more tests failed
"""

import subprocess
import sys
import os

DEFAULT_BINARY = "../performance"


def run_go_tests(test_pattern: str) -> tuple:
    """
    Run Go tests matching the pattern.

    Returns:
        (success, output)
    """
    cmd = ["go", "test", "-v", "-run", test_pattern]
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=30,
            cwd=os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        )
        return result.returncode == 0, result.stdout + result.stderr
    except subprocess.TimeoutExpired:
        return False, "Timeout"
    except Exception as e:
        return False, str(e)


def test_gate_critical_detection() -> bool:
    """Test that critical thresholds are detected."""
    print("  Testing critical threshold detection...", end=" ")

    success, output = run_go_tests("TestStabilityGate_IsCritical")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    print("OK")
    return True


def test_gate_evaluate_samples() -> bool:
    """Test sample evaluation logic."""
    print("  Testing sample evaluation...", end=" ")

    success, output = run_go_tests("TestStabilityGate_EvaluateSamples")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    # Count passed tests
    passed = output.count("--- PASS:")
    print(f"OK ({passed} sub-tests)")
    return True


def test_gate_aggregate() -> bool:
    """Test metrics aggregation."""
    print("  Testing metrics aggregation...", end=" ")

    success, output = run_go_tests("TestStabilityGate_Aggregate")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    print("OK")
    return True


def test_gate_with_config() -> bool:
    """Test WithConfig returns new gate with modified config."""
    print("  Testing WithConfig...", end=" ")

    success, output = run_go_tests("TestStabilityGate_WithConfig")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    print("OK")
    return True


def test_hypothesis_model() -> bool:
    """Test hypothesis model defaults."""
    print("  Testing hypothesis model...", end=" ")

    success, output = run_go_tests("TestDefaultHypothesisModel")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    print("OK")
    return True


def test_metrics_parsing() -> bool:
    """Test Prometheus metrics parsing."""
    print("  Testing Prometheus parsing...", end=" ")

    success, output = run_go_tests("TestParsePrometheus")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    passed = output.count("--- PASS:")
    print(f"OK ({passed} sub-tests)")
    return True


def test_metrics_collector() -> bool:
    """Test metrics collector."""
    print("  Testing metrics collector...", end=" ")

    success, output = run_go_tests("TestMetricsCollector")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    passed = output.count("--- PASS:")
    print(f"OK ({passed} sub-tests)")
    return True


def test_probe_result() -> bool:
    """Test ProbeResult fields."""
    print("  Testing ProbeResult...", end=" ")

    success, output = run_go_tests("TestProbeResult")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    print("OK")
    return True


def test_diagnostic_capture() -> bool:
    """Test DiagnosticCapture."""
    print("  Testing DiagnosticCapture...", end=" ")

    success, output = run_go_tests("TestDiagnosticCapture")

    if not success:
        print(f"FAILED")
        print(output)
        return False

    passed = output.count("--- PASS:")
    print(f"OK ({passed} sub-tests)")
    return True


def main():
    print("Testing Stability Gate (Phase 5)")
    print("=" * 60)

    tests = [
        ("gate_critical_detection", test_gate_critical_detection),
        ("gate_evaluate_samples", test_gate_evaluate_samples),
        ("gate_aggregate", test_gate_aggregate),
        ("gate_with_config", test_gate_with_config),
        ("hypothesis_model", test_hypothesis_model),
        ("metrics_parsing", test_metrics_parsing),
        ("metrics_collector", test_metrics_collector),
        ("probe_result", test_probe_result),
        ("diagnostic_capture", test_diagnostic_capture),
    ]

    passed = 0
    failed = 0

    for name, test_func in tests:
        try:
            if test_func():
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
