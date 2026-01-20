#!/usr/bin/env python3
"""
Test script for client-seeker control socket.

This script tests the JSON control protocol over Unix domain socket.

Usage:
    # With default socket path
    ./test_control_socket.py

    # With custom socket path
    ./test_control_socket.py /tmp/custom_seeker.sock

Prerequisites:
    - client-seeker must be running
    - Control socket must be accessible

Exit codes:
    0 - All tests passed
    1 - One or more tests failed
"""

import socket
import json
import sys
import time
from typing import Optional, Tuple

DEFAULT_SOCKET_PATH = "/tmp/client_seeker.sock"


def send_command(sock_path: str, command: dict, timeout: float = 2.0) -> Tuple[bool, Optional[dict], str]:
    """
    Send a command to the control socket and return the response.

    Returns:
        (success, response_dict, error_message)
    """
    try:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        sock.connect(sock_path)

        # Send command
        data = json.dumps(command) + "\n"
        sock.sendall(data.encode())

        # Receive response
        response = sock.recv(4096).decode().strip()
        sock.close()

        if not response:
            return False, None, "Empty response"

        try:
            parsed = json.loads(response)
            return True, parsed, ""
        except json.JSONDecodeError as e:
            return False, None, f"Invalid JSON response: {e}"

    except socket.timeout:
        return False, None, "Connection timeout"
    except ConnectionRefusedError:
        return False, None, "Connection refused - is client-seeker running?"
    except FileNotFoundError:
        return False, None, f"Socket not found: {sock_path}"
    except Exception as e:
        return False, None, str(e)


def test_get_status(sock_path: str) -> bool:
    """Test get_status command."""
    print("  Testing get_status...", end=" ")

    success, resp, err = send_command(sock_path, {"command": "get_status"})
    if not success:
        print(f"FAILED: {err}")
        return False

    if resp.get("status") != "ok":
        print(f"FAILED: status != 'ok': {resp}")
        return False

    # Check required fields
    required = ["current_bitrate", "uptime_seconds"]
    for field in required:
        if field not in resp:
            print(f"FAILED: missing field '{field}'")
            return False

    print(f"OK (bitrate={resp['current_bitrate']}, uptime={resp['uptime_seconds']:.1f}s)")
    return True


def test_set_bitrate(sock_path: str) -> bool:
    """Test set_bitrate command."""
    print("  Testing set_bitrate...", end=" ")

    # Get current bitrate
    success, resp, err = send_command(sock_path, {"command": "get_status"})
    if not success:
        print(f"FAILED: could not get initial status: {err}")
        return False

    original_bitrate = resp.get("current_bitrate", 100_000_000)

    # Set new bitrate
    new_bitrate = 150_000_000
    success, resp, err = send_command(sock_path, {"command": "set_bitrate", "bitrate": new_bitrate})
    if not success:
        print(f"FAILED: {err}")
        return False

    if resp.get("status") != "ok":
        print(f"FAILED: status != 'ok': {resp}")
        return False

    if resp.get("current_bitrate") != new_bitrate:
        print(f"FAILED: bitrate not updated: got {resp.get('current_bitrate')}, want {new_bitrate}")
        return False

    # Restore original bitrate
    send_command(sock_path, {"command": "set_bitrate", "bitrate": original_bitrate})

    print(f"OK (changed to {new_bitrate}, restored to {original_bitrate})")
    return True


def test_heartbeat(sock_path: str) -> bool:
    """Test heartbeat command."""
    print("  Testing heartbeat...", end=" ")

    success, resp, err = send_command(sock_path, {"command": "heartbeat"})
    if not success:
        print(f"FAILED: {err}")
        return False

    if resp.get("status") != "ok":
        print(f"FAILED: status != 'ok': {resp}")
        return False

    print("OK")
    return True


def test_invalid_command(sock_path: str) -> bool:
    """Test that invalid commands return errors."""
    print("  Testing invalid command...", end=" ")

    success, resp, err = send_command(sock_path, {"command": "invalid_command_xyz"})
    if not success:
        print(f"FAILED: {err}")
        return False

    if resp.get("status") != "error":
        print(f"FAILED: expected error status, got: {resp}")
        return False

    if not resp.get("error"):
        print("FAILED: missing error message")
        return False

    print(f"OK (got expected error: {resp['error'][:50]}...)")
    return True


def test_invalid_json(sock_path: str) -> bool:
    """Test that invalid JSON returns an error."""
    print("  Testing invalid JSON...", end=" ")

    try:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(2.0)
        sock.connect(sock_path)
        sock.sendall(b"not valid json\n")
        response = sock.recv(4096).decode().strip()
        sock.close()

        resp = json.loads(response)
        if resp.get("status") != "error":
            print(f"FAILED: expected error status, got: {resp}")
            return False

        print("OK (got expected error)")
        return True

    except Exception as e:
        print(f"FAILED: {e}")
        return False


def test_multiple_commands(sock_path: str) -> bool:
    """Test sending multiple commands on the same connection."""
    print("  Testing multiple commands...", end=" ")

    try:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(2.0)
        sock.connect(sock_path)

        commands = [
            {"command": "get_status"},
            {"command": "heartbeat"},
            {"command": "get_status"},
        ]

        for i, cmd in enumerate(commands):
            sock.sendall((json.dumps(cmd) + "\n").encode())
            response = sock.recv(4096).decode().strip()
            resp = json.loads(response)
            if resp.get("status") != "ok":
                print(f"FAILED: command {i} failed: {resp}")
                sock.close()
                return False

        sock.close()
        print(f"OK (sent {len(commands)} commands)")
        return True

    except Exception as e:
        print(f"FAILED: {e}")
        return False


def main():
    sock_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_SOCKET_PATH

    print(f"Testing client-seeker control socket: {sock_path}")
    print("=" * 60)

    tests = [
        ("get_status", test_get_status),
        ("set_bitrate", test_set_bitrate),
        ("heartbeat", test_heartbeat),
        ("invalid_command", test_invalid_command),
        ("invalid_json", test_invalid_json),
        ("multiple_commands", test_multiple_commands),
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
