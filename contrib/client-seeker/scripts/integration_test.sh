#!/bin/bash
#
# Integration test for client-seeker.
#
# This script:
#   1. Builds client-seeker and server
#   2. Starts the server
#   3. Starts client-seeker
#   4. Runs all test scripts
#   5. Cleans up
#
# Usage:
#   ./integration_test.sh                    # Run all tests
#   ./integration_test.sh --skip-build       # Skip build step
#   ./integration_test.sh --keep-running     # Don't stop processes after tests
#
# Exit codes:
#   0 - All tests passed
#   1 - Build failed
#   2 - Server failed to start
#   3 - Client-seeker failed to start
#   4 - Tests failed
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLIENT_SEEKER_DIR="$(dirname "$SCRIPT_DIR")"
SERVER_DIR="$(dirname "$CLIENT_SEEKER_DIR")/server"
GOSRT_DIR="$(dirname "$(dirname "$CLIENT_SEEKER_DIR")")"

# Configuration
SERVER_ADDR="127.0.0.1:6000"
CONTROL_SOCKET="/tmp/client_seeker_test.sock"
METRICS_SOCKET="/tmp/client_seeker_metrics_test.sock"
INITIAL_BITRATE="100M"

# Process PIDs
SERVER_PID=""
SEEKER_PID=""

# Flags
SKIP_BUILD=false
KEEP_RUNNING=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

cleanup() {
    if [ "$KEEP_RUNNING" = false ]; then
        log_info "Cleaning up..."

        if [ -n "$SEEKER_PID" ] && kill -0 "$SEEKER_PID" 2>/dev/null; then
            kill "$SEEKER_PID" 2>/dev/null || true
            wait "$SEEKER_PID" 2>/dev/null || true
        fi

        if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
            kill "$SERVER_PID" 2>/dev/null || true
            wait "$SERVER_PID" 2>/dev/null || true
        fi

        rm -f "$CONTROL_SOCKET" "$METRICS_SOCKET"
    else
        log_info "Keeping processes running (--keep-running)"
        log_info "  Server PID: $SERVER_PID"
        log_info "  Seeker PID: $SEEKER_PID"
        log_info "  Control socket: $CONTROL_SOCKET"
        log_info "  Metrics socket: $METRICS_SOCKET"
    fi
}

trap cleanup EXIT

# Parse arguments
for arg in "$@"; do
    case $arg in
        --skip-build)
            SKIP_BUILD=true
            ;;
        --keep-running)
            KEEP_RUNNING=true
            ;;
        --help)
            echo "Usage: $0 [--skip-build] [--keep-running]"
            exit 0
            ;;
    esac
done

echo "========================================"
echo "Client-Seeker Integration Test"
echo "========================================"
echo ""

# Step 1: Build
if [ "$SKIP_BUILD" = false ]; then
    log_info "Building binaries..."

    cd "$CLIENT_SEEKER_DIR"
    if ! go build -o client-seeker .; then
        log_error "Failed to build client-seeker"
        exit 1
    fi

    cd "$SERVER_DIR"
    if ! go build -o server .; then
        log_error "Failed to build server"
        exit 1
    fi

    log_info "Build successful"
else
    log_info "Skipping build (--skip-build)"
fi

echo ""

# Step 2: Start server
log_info "Starting server on $SERVER_ADDR..."
cd "$SERVER_DIR"
./server -addr "$SERVER_ADDR" > /tmp/integration_test_server.log 2>&1 &
SERVER_PID=$!
sleep 1

if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    log_error "Server failed to start. Log:"
    cat /tmp/integration_test_server.log
    exit 2
fi

log_info "Server started (PID: $SERVER_PID)"

echo ""

# Step 3: Start client-seeker
log_info "Starting client-seeker..."
cd "$CLIENT_SEEKER_DIR"
./client-seeker \
    -to "srt://$SERVER_ADDR/test-stream" \
    -initial "$INITIAL_BITRATE" \
    -control "$CONTROL_SOCKET" \
    -seeker-metrics "$METRICS_SOCKET" \
    -watchdog=false \
    > /tmp/integration_test_seeker.log 2>&1 &
SEEKER_PID=$!
sleep 2

if ! kill -0 "$SEEKER_PID" 2>/dev/null; then
    log_error "Client-seeker failed to start. Log:"
    cat /tmp/integration_test_seeker.log
    exit 3
fi

log_info "Client-seeker started (PID: $SEEKER_PID)"

# Check connection
if grep -q "Connected!" /tmp/integration_test_seeker.log; then
    log_info "SRT connection established"
else
    log_warn "SRT connection status unclear - check log"
fi

echo ""

# Step 4: Run tests
log_info "Running control socket tests..."
CONTROL_RESULT=0
python3 "$SCRIPT_DIR/test_control_socket.py" "$CONTROL_SOCKET" || CONTROL_RESULT=$?

echo ""

log_info "Running metrics tests..."
METRICS_RESULT=0
python3 "$SCRIPT_DIR/test_metrics.py" "$METRICS_SOCKET" || METRICS_RESULT=$?

echo ""

# Step 5: Summary
echo "========================================"
echo "Test Summary"
echo "========================================"

TOTAL_FAILED=0

if [ $CONTROL_RESULT -eq 0 ]; then
    echo -e "Control socket tests: ${GREEN}PASSED${NC}"
else
    echo -e "Control socket tests: ${RED}FAILED${NC}"
    TOTAL_FAILED=$((TOTAL_FAILED + 1))
fi

if [ $METRICS_RESULT -eq 0 ]; then
    echo -e "Metrics tests: ${GREEN}PASSED${NC}"
else
    echo -e "Metrics tests: ${RED}FAILED${NC}"
    TOTAL_FAILED=$((TOTAL_FAILED + 1))
fi

echo ""

if [ $TOTAL_FAILED -eq 0 ]; then
    log_info "All tests passed!"
    exit 0
else
    log_error "$TOTAL_FAILED test suite(s) failed"
    exit 4
fi
