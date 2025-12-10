#!/bin/bash
# test_shutdown.sh - Quick test for graceful shutdown of each component
# This test runs locally (no network namespaces) for fast iteration.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SERVER_BIN="$PROJECT_ROOT/contrib/server/server"
CLIENT_GEN_BIN="$PROJECT_ROOT/contrib/client-generator/client-generator"
CLIENT_BIN="$PROJECT_ROOT/contrib/client/client"

# Test configuration
STARTUP_WAIT=2      # Seconds to wait after starting each component
SHUTDOWN_TIMEOUT=10 # Max seconds to wait for graceful shutdown (server has 3s read deadline)
SERVER_ADDR="127.0.0.1:6789"
STREAM_ID="test-shutdown"

# Create unique temp directory for logs
TEMP_DIR=$(mktemp -d -t gosrt-shutdown-test.XXXXXX)
SERVER_LOG="$TEMP_DIR/server.log"
CLIENTGEN_LOG="$TEMP_DIR/clientgen.log"
CLIENT_LOG="$TEMP_DIR/client.log"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Track PIDs for cleanup
declare -a PIDS_TO_KILL=()

cleanup() {
    # Kill any remaining processes
    for pid in "${PIDS_TO_KILL[@]}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null || true
        fi
    done
    # Remove temp directory
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        rm -rf "$TEMP_DIR"
    fi
}

trap cleanup EXIT

log_pass() {
    echo -e "${GREEN}✓ PASS${NC}: $1"
}

log_fail() {
    echo -e "${RED}✗ FAIL${NC}: $1"
}

log_info() {
    echo -e "${YELLOW}→${NC} $1"
}

# Test if a process exits gracefully after SIGINT
# Args: $1=name, $2=pid, $3=timeout, $4=logfile (optional)
test_graceful_shutdown() {
    local name="$1"
    local pid="$2"
    local timeout="$3"
    local logfile="${4:-}"

    log_info "Sending SIGINT to $name (PID $pid)..."
    kill -INT "$pid" 2>/dev/null || true

    # Wait for process to exit
    local elapsed=0
    while kill -0 "$pid" 2>/dev/null && [ $elapsed -lt $timeout ]; do
        sleep 0.5
        elapsed=$((elapsed + 1))
    done

    if kill -0 "$pid" 2>/dev/null; then
        log_fail "$name did not exit within ${timeout}s"
        # Kill it forcefully for cleanup
        kill -9 "$pid" 2>/dev/null || true
        return 1
    else
        wait "$pid" 2>/dev/null || true
        # Extract shutdown timing from log if available
        local timing=""
        if [ -n "$logfile" ] && [ -f "$logfile" ]; then
            timing=$(grep -o "Graceful shutdown complete after [0-9]*ms" "$logfile" 2>/dev/null | tail -1 | sed 's/Graceful shutdown complete after //')
        fi
        if [ -n "$timing" ]; then
            log_pass "$name exited gracefully (${timing})"
        else
            log_pass "$name exited gracefully"
        fi
        return 0
    fi
}

# Build binaries if needed
build_if_needed() {
    log_info "Checking binaries..."

    if [ ! -x "$SERVER_BIN" ]; then
        log_info "Building server..."
        (cd "$PROJECT_ROOT/contrib/server" && go build -o server)
    fi

    if [ ! -x "$CLIENT_GEN_BIN" ]; then
        log_info "Building client-generator..."
        (cd "$PROJECT_ROOT/contrib/client-generator" && go build -o client-generator)
    fi

    if [ ! -x "$CLIENT_BIN" ]; then
        log_info "Building client..."
        (cd "$PROJECT_ROOT/contrib/client" && go build -o client)
    fi
}

# ====================
# Test 1: Server Standalone Shutdown
# ====================
test_server_shutdown() {
    echo ""
    echo "========================================"
    echo "Test 1: Server Standalone Shutdown"
    echo "========================================"

    log_info "Starting server on $SERVER_ADDR..."
    $SERVER_BIN -addr "$SERVER_ADDR" \
        -iouringenabled -iouringrecvenabled \
        -peeridletimeo 5000 \
        > "$SERVER_LOG" 2>&1 &
    local server_pid=$!
    PIDS_TO_KILL+=($server_pid)

    sleep $STARTUP_WAIT

    if ! kill -0 $server_pid 2>/dev/null; then
        log_fail "Server failed to start"
        cat "$SERVER_LOG"
        return 1
    fi

    log_pass "Server started (PID $server_pid)"

    if test_graceful_shutdown "Server" $server_pid $SHUTDOWN_TIMEOUT "$SERVER_LOG"; then
        return 0
    else
        echo "--- Server log ---"
        tail -20 "$SERVER_LOG"
        return 1
    fi
}

# ====================
# Test 2: Client-Generator Shutdown (requires server)
# ====================
test_client_generator_shutdown() {
    echo ""
    echo "========================================"
    echo "Test 2: Client-Generator Shutdown"
    echo "========================================"

    # Start server first
    log_info "Starting server on $SERVER_ADDR..."
    $SERVER_BIN -addr "$SERVER_ADDR" \
        -iouringenabled -iouringrecvenabled \
        -peeridletimeo 30000 \
        > "$SERVER_LOG" 2>&1 &
    local server_pid=$!
    PIDS_TO_KILL+=($server_pid)

    sleep 1

    if ! kill -0 $server_pid 2>/dev/null; then
        log_fail "Server failed to start"
        return 1
    fi
    log_pass "Server started (PID $server_pid)"

    # Start client-generator
    log_info "Starting client-generator..."
    $CLIENT_GEN_BIN -to "srt://$SERVER_ADDR/$STREAM_ID" \
        -bitrate 1000000 \
        -iouringenabled -iouringrecvenabled \
        > "$CLIENTGEN_LOG" 2>&1 &
    local cg_pid=$!
    PIDS_TO_KILL+=($cg_pid)

    sleep $STARTUP_WAIT

    if ! kill -0 $cg_pid 2>/dev/null; then
        log_fail "Client-generator failed to start"
        cat "$CLIENTGEN_LOG"
        kill -INT $server_pid 2>/dev/null || true
        return 1
    fi
    log_pass "Client-generator started (PID $cg_pid)"

    # Test client-generator shutdown
    local result=0
    if ! test_graceful_shutdown "Client-generator" $cg_pid $SHUTDOWN_TIMEOUT "$CLIENTGEN_LOG"; then
        echo "--- Client-generator log ---"
        tail -20 "$CLIENTGEN_LOG"
        result=1
    fi

    # Clean up server
    kill -INT $server_pid 2>/dev/null || true
    sleep 1
    kill -9 $server_pid 2>/dev/null || true

    return $result
}

# ====================
# Test 3: Client Shutdown (requires server + client-generator)
# ====================
test_client_shutdown() {
    echo ""
    echo "========================================"
    echo "Test 3: Client Shutdown (with io_uring output)"
    echo "========================================"

    # Start server
    log_info "Starting server on $SERVER_ADDR..."
    $SERVER_BIN -addr "$SERVER_ADDR" \
        -iouringenabled -iouringrecvenabled \
        -peeridletimeo 30000 \
        > "$SERVER_LOG" 2>&1 &
    local server_pid=$!
    PIDS_TO_KILL+=($server_pid)

    sleep 1

    if ! kill -0 $server_pid 2>/dev/null; then
        log_fail "Server failed to start"
        return 1
    fi
    log_pass "Server started (PID $server_pid)"

    # Start client-generator
    log_info "Starting client-generator..."
    $CLIENT_GEN_BIN -to "srt://$SERVER_ADDR/$STREAM_ID" \
        -bitrate 1000000 \
        -iouringenabled -iouringrecvenabled \
        > "$CLIENTGEN_LOG" 2>&1 &
    local cg_pid=$!
    PIDS_TO_KILL+=($cg_pid)

    sleep 1

    if ! kill -0 $cg_pid 2>/dev/null; then
        log_fail "Client-generator failed to start"
        kill -INT $server_pid 2>/dev/null || true
        return 1
    fi
    log_pass "Client-generator started (PID $cg_pid)"

    # Start client with io_uring output
    # Note: -iouringoutput only works with stdout (-to -), not with -to null
    log_info "Starting client with -iouringoutput (stdout to /dev/null)..."
    $CLIENT_BIN \
        -from "srt://$SERVER_ADDR?streamid=subscribe:/$STREAM_ID&mode=caller" \
        -to - \
        -iouringoutput \
        -iouringenabled -iouringrecvenabled \
        > /dev/null 2> "$CLIENT_LOG" &
    local client_pid=$!
    PIDS_TO_KILL+=($client_pid)

    sleep $STARTUP_WAIT

    if ! kill -0 $client_pid 2>/dev/null; then
        log_fail "Client failed to start"
        cat "$CLIENT_LOG"
        kill -INT $cg_pid 2>/dev/null || true
        kill -INT $server_pid 2>/dev/null || true
        return 1
    fi
    log_pass "Client started (PID $client_pid)"

    # Test client shutdown
    local result=0
    if ! test_graceful_shutdown "Client" $client_pid $SHUTDOWN_TIMEOUT "$CLIENT_LOG"; then
        echo "--- Client log ---"
        tail -30 "$CLIENT_LOG"
        result=1
    fi

    # Clean up
    kill -INT $cg_pid 2>/dev/null || true
    sleep 1
    kill -INT $server_pid 2>/dev/null || true
    sleep 1
    kill -9 $cg_pid 2>/dev/null || true
    kill -9 $server_pid 2>/dev/null || true

    return $result
}

# ====================
# Test 4: Client Shutdown WITHOUT io_uring output (control test)
# ====================
test_client_shutdown_no_iouring() {
    echo ""
    echo "========================================"
    echo "Test 4: Client Shutdown (WITHOUT io_uring output)"
    echo "========================================"

    # Start server
    log_info "Starting server on $SERVER_ADDR..."
    $SERVER_BIN -addr "$SERVER_ADDR" \
        -peeridletimeo 30000 \
        > "$SERVER_LOG" 2>&1 &
    local server_pid=$!
    PIDS_TO_KILL+=($server_pid)

    sleep 1

    if ! kill -0 $server_pid 2>/dev/null; then
        log_fail "Server failed to start"
        return 1
    fi
    log_pass "Server started (PID $server_pid)"

    # Start client-generator (no io_uring)
    log_info "Starting client-generator..."
    $CLIENT_GEN_BIN -to "srt://$SERVER_ADDR/$STREAM_ID" \
        -bitrate 1000000 \
        > "$CLIENTGEN_LOG" 2>&1 &
    local cg_pid=$!
    PIDS_TO_KILL+=($cg_pid)

    sleep 1

    if ! kill -0 $cg_pid 2>/dev/null; then
        log_fail "Client-generator failed to start"
        kill -INT $server_pid 2>/dev/null || true
        return 1
    fi
    log_pass "Client-generator started (PID $cg_pid)"

    # Start client WITHOUT io_uring output
    log_info "Starting client WITHOUT -iouringoutput..."
    $CLIENT_BIN \
        -from "srt://$SERVER_ADDR?streamid=subscribe:/$STREAM_ID&mode=caller" \
        -to null \
        > "$CLIENT_LOG" 2>&1 &
    local client_pid=$!
    PIDS_TO_KILL+=($client_pid)

    sleep $STARTUP_WAIT

    if ! kill -0 $client_pid 2>/dev/null; then
        log_fail "Client failed to start"
        cat "$CLIENT_LOG"
        kill -INT $cg_pid 2>/dev/null || true
        kill -INT $server_pid 2>/dev/null || true
        return 1
    fi
    log_pass "Client started (PID $client_pid)"

    # Test client shutdown
    local result=0
    if ! test_graceful_shutdown "Client (no io_uring)" $client_pid $SHUTDOWN_TIMEOUT "$CLIENT_LOG"; then
        echo "--- Client log ---"
        tail -30 "$CLIENT_LOG"
        result=1
    fi

    # Clean up
    kill -INT $cg_pid 2>/dev/null || true
    sleep 1
    kill -INT $server_pid 2>/dev/null || true
    sleep 1
    kill -9 $cg_pid 2>/dev/null || true
    kill -9 $server_pid 2>/dev/null || true

    return $result
}

# ====================
# Main
# ====================
main() {
    echo "=========================================="
    echo " GoSRT Graceful Shutdown Test"
    echo "=========================================="

    build_if_needed

    local failures=0

    # Run tests
    test_server_shutdown || ((failures++))
    test_client_generator_shutdown || ((failures++))
    test_client_shutdown || ((failures++))
    test_client_shutdown_no_iouring || ((failures++))

    echo ""
    echo "=========================================="
    if [ $failures -eq 0 ]; then
        echo -e "${GREEN}All shutdown tests PASSED${NC}"
        exit 0
    else
        echo -e "${RED}$failures shutdown test(s) FAILED${NC}"
        exit 1
    fi
}

# Run specific test if provided
case "${1:-all}" in
    server)
        build_if_needed
        test_server_shutdown
        ;;
    client-generator|cg)
        build_if_needed
        test_client_generator_shutdown
        ;;
    client)
        build_if_needed
        test_client_shutdown
        ;;
    client-no-iouring)
        build_if_needed
        test_client_shutdown_no_iouring
        ;;
    all|"")
        main
        ;;
    *)
        echo "Usage: $0 [server|client-generator|client|client-no-iouring|all]"
        exit 1
        ;;
esac

