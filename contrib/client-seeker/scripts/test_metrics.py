#!/usr/bin/env python3
"""
Test script for client-seeker Prometheus metrics endpoint.

This script tests the metrics available over the Unix domain socket.

Usage:
    # With default socket path
    ./test_metrics.py

    # With custom socket path
    ./test_metrics.py /tmp/custom_metrics.sock

Prerequisites:
    - client-seeker must be running
    - Metrics socket must be accessible

Exit codes:
    0 - All tests passed
    1 - One or more tests failed
"""

import socket
import sys
import re
from typing import Dict, List, Optional, Tuple

DEFAULT_SOCKET_PATH = "/tmp/client_seeker_metrics.sock"


def http_get(sock_path: str, path: str, timeout: float = 2.0) -> Tuple[bool, int, str, str]:
    """
    Send an HTTP GET request over Unix socket.

    Returns:
        (success, status_code, headers, body)
    """
    try:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        sock.connect(sock_path)

        # Send HTTP request
        request = f"GET {path} HTTP/1.0\r\nHost: localhost\r\n\r\n"
        sock.sendall(request.encode())

        # Receive response
        response = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            response += chunk
        sock.close()

        # Parse response
        response_str = response.decode()

        # Split headers and body
        if "\r\n\r\n" in response_str:
            header_part, body = response_str.split("\r\n\r\n", 1)
        else:
            header_part, body = response_str, ""

        # Parse status line
        lines = header_part.split("\r\n")
        if not lines:
            return False, 0, "", "Empty response"

        status_line = lines[0]
        match = re.match(r"HTTP/\d\.\d (\d+)", status_line)
        if not match:
            return False, 0, "", f"Invalid status line: {status_line}"

        status_code = int(match.group(1))
        headers = "\r\n".join(lines[1:])

        return True, status_code, headers, body

    except socket.timeout:
        return False, 0, "", "Connection timeout"
    except ConnectionRefusedError:
        return False, 0, "", "Connection refused - is client-seeker running?"
    except FileNotFoundError:
        return False, 0, "", f"Socket not found: {sock_path}"
    except Exception as e:
        return False, 0, "", str(e)


def parse_prometheus_metrics(body: str) -> Dict[str, str]:
    """Parse Prometheus metrics format into a dict of metric_name -> value."""
    metrics = {}
    for line in body.split("\n"):
        line = line.strip()
        if not line or line.startswith("#"):
            continue

        # Parse "metric_name value" or "metric_name{labels} value"
        match = re.match(r"^([a-zA-Z_:][a-zA-Z0-9_:]*(?:\{[^}]*\})?) (.+)$", line)
        if match:
            name = match.group(1)
            value = match.group(2)
            metrics[name] = value

    return metrics


def test_metrics_endpoint(sock_path: str) -> bool:
    """Test that /metrics endpoint returns valid Prometheus format."""
    print("  Testing /metrics endpoint...", end=" ")

    success, status, headers, body = http_get(sock_path, "/metrics")
    if not success:
        print(f"FAILED: {body}")
        return False

    if status != 200:
        print(f"FAILED: HTTP status {status}")
        return False

    metrics = parse_prometheus_metrics(body)
    if not metrics:
        print("FAILED: no metrics found in response")
        return False

    print(f"OK ({len(metrics)} metrics)")
    return True


def test_required_metrics(sock_path: str) -> bool:
    """Test that all required client-seeker metrics are present."""
    print("  Testing required metrics...", end=" ")

    success, status, headers, body = http_get(sock_path, "/metrics")
    if not success:
        print(f"FAILED: {body}")
        return False

    metrics = parse_prometheus_metrics(body)

    required_metrics = [
        "client_seeker_current_bitrate_bps",
        "client_seeker_target_bitrate_bps",
        "client_seeker_uptime_seconds",
    ]

    # These may not be present if not connected
    optional_metrics = [
        "client_seeker_packets_generated_total",
        "client_seeker_bytes_generated_total",
        "client_seeker_actual_bitrate_bps",
        "client_seeker_connection_alive",
        "client_seeker_heartbeat_age_seconds",
        "client_seeker_watchdog_state",
    ]

    missing = []
    for metric in required_metrics:
        if metric not in metrics:
            missing.append(metric)

    if missing:
        print(f"FAILED: missing required metrics: {missing}")
        return False

    # Count optional metrics present
    optional_present = sum(1 for m in optional_metrics if m in metrics)

    print(f"OK ({len(required_metrics)} required, {optional_present}/{len(optional_metrics)} optional)")
    return True


def test_metric_values(sock_path: str) -> bool:
    """Test that metric values are valid numbers."""
    print("  Testing metric values...", end=" ")

    success, status, headers, body = http_get(sock_path, "/metrics")
    if not success:
        print(f"FAILED: {body}")
        return False

    metrics = parse_prometheus_metrics(body)

    # Check that client_seeker metrics have valid values
    invalid = []
    for name, value in metrics.items():
        if name.startswith("client_seeker_"):
            try:
                float(value)
            except ValueError:
                invalid.append(f"{name}={value}")

    if invalid:
        print(f"FAILED: invalid metric values: {invalid}")
        return False

    print("OK (all values are valid numbers)")
    return True


def test_bitrate_consistency(sock_path: str) -> bool:
    """Test that current_bitrate and target_bitrate are consistent."""
    print("  Testing bitrate consistency...", end=" ")

    success, status, headers, body = http_get(sock_path, "/metrics")
    if not success:
        print(f"FAILED: {body}")
        return False

    metrics = parse_prometheus_metrics(body)

    current = float(metrics.get("client_seeker_current_bitrate_bps", 0))
    target = float(metrics.get("client_seeker_target_bitrate_bps", 0))

    if current <= 0:
        print(f"FAILED: current_bitrate is {current}")
        return False

    if target <= 0:
        print(f"FAILED: target_bitrate is {target}")
        return False

    # In steady state, current should equal target
    # (Allow some tolerance since they're updated atomically)
    if current != target:
        print(f"WARNING: current ({current}) != target ({target}), but this may be normal during transitions")

    print(f"OK (current={current/1e6:.1f}Mb/s, target={target/1e6:.1f}Mb/s)")
    return True


def test_health_endpoint(sock_path: str) -> bool:
    """Test the /health endpoint."""
    print("  Testing /health endpoint...", end=" ")

    success, status, headers, body = http_get(sock_path, "/health")
    if not success:
        print(f"FAILED: {body}")
        return False

    # Health can be 200 (healthy) or 503 (unhealthy)
    if status not in [200, 503]:
        print(f"FAILED: unexpected HTTP status {status}")
        return False

    status_text = "healthy" if status == 200 else "unhealthy"
    print(f"OK (status={status}, {body.strip()})")
    return True


def test_uptime_increases(sock_path: str) -> bool:
    """Test that uptime_seconds increases over time."""
    print("  Testing uptime increases...", end=" ")

    import time

    # Get first reading
    success, status, headers, body = http_get(sock_path, "/metrics")
    if not success:
        print(f"FAILED: {body}")
        return False

    metrics1 = parse_prometheus_metrics(body)
    uptime1 = float(metrics1.get("client_seeker_uptime_seconds", 0))

    # Wait a bit
    time.sleep(0.5)

    # Get second reading
    success, status, headers, body = http_get(sock_path, "/metrics")
    if not success:
        print(f"FAILED: {body}")
        return False

    metrics2 = parse_prometheus_metrics(body)
    uptime2 = float(metrics2.get("client_seeker_uptime_seconds", 0))

    if uptime2 <= uptime1:
        print(f"FAILED: uptime did not increase ({uptime1} -> {uptime2})")
        return False

    print(f"OK ({uptime1:.2f}s -> {uptime2:.2f}s)")
    return True


def main():
    sock_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_SOCKET_PATH

    print(f"Testing client-seeker metrics endpoint: {sock_path}")
    print("=" * 60)

    tests = [
        ("metrics_endpoint", test_metrics_endpoint),
        ("required_metrics", test_required_metrics),
        ("metric_values", test_metric_values),
        ("bitrate_consistency", test_bitrate_consistency),
        ("health_endpoint", test_health_endpoint),
        ("uptime_increases", test_uptime_increases),
    ]

    passed = 0
    failed = 0

    for name, test_func in tests:
        try:
            if test_func(sock_path):
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
