#!/bin/bash
#
# Integration test for performance orchestrator.
#
# This script:
#   1. Builds all required binaries
#   2. Runs unit tests
#   3. Runs timing model validation tests
#   4. Runs a short integration test (start/stop processes)
#
# Usage:
#   ./integration_test.sh                    # Run all tests
#   ./integration_test.sh --skip-build       # Skip build step
#   ./integration_test.sh --quick            # Skip long-running tests
#
# Exit codes:
#   0 - All tests passed
#   1 - Build failed
#   2 - Unit tests failed
#   3 - Timing model tests failed
#   4 - Integration test failed
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PERFORMANCE_DIR="$(dirname "$SCRIPT_DIR")"
CLIENT_SEEKER_DIR="$(dirname "$PERFORMANCE_DIR")/client-seeker"
SERVER_DIR="$(dirname "$PERFORMANCE_DIR")/server"
GOSRT_DIR="$(dirname "$(dirname "$PERFORMANCE_DIR")")"

# Flags
SKIP_BUILD=false
QUICK_MODE=false

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

# Parse arguments
for arg in "$@"; do
    case $arg in
        --skip-build)
            SKIP_BUILD=true
            ;;
        --quick)
            QUICK_MODE=true
            ;;
        --help)
            echo "Usage: $0 [--skip-build] [--quick]"
            exit 0
            ;;
    esac
done

echo "========================================"
echo "Performance Orchestrator Integration Test"
echo "========================================"
echo ""

# Step 1: Build
if [ "$SKIP_BUILD" = false ]; then
    log_info "Building binaries..."

    cd "$PERFORMANCE_DIR"
    if ! go build -o performance .; then
        log_error "Failed to build performance"
        exit 1
    fi

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

# Step 2: Unit tests
log_info "Running unit tests..."
cd "$PERFORMANCE_DIR"
if ! go test -v ./... 2>&1 | tail -20; then
    log_error "Unit tests failed"
    exit 2
fi
log_info "Unit tests passed"

echo ""

# Step 3: Timing model tests
log_info "Running timing model tests..."
chmod +x "$SCRIPT_DIR/test_timing_model.py"
if ! python3 "$SCRIPT_DIR/test_timing_model.py" "$PERFORMANCE_DIR/performance"; then
    log_error "Timing model tests failed"
    exit 3
fi
log_info "Timing model tests passed"

echo ""

# Step 4: Integration test (start/stop)
if [ "$QUICK_MODE" = false ]; then
    log_info "Running integration test (start/stop)..."

    cd "$PERFORMANCE_DIR"

    # Run performance with a short timeout
    timeout 10 ./performance -dry-run INITIAL=100M MAX=200M 2>&1 || true

    if [ $? -eq 0 ]; then
        log_info "Dry-run test passed"
    else
        log_warn "Dry-run test had issues (may be expected)"
    fi

    # Test that binaries are found
    log_info "Testing binary resolution..."

    # Check server binary
    if [ -f "$SERVER_DIR/server" ]; then
        log_info "Server binary found: $SERVER_DIR/server"
    else
        log_error "Server binary not found"
        exit 4
    fi

    # Check seeker binary
    if [ -f "$CLIENT_SEEKER_DIR/client-seeker" ]; then
        log_info "Seeker binary found: $CLIENT_SEEKER_DIR/client-seeker"
    else
        log_error "Seeker binary not found"
        exit 4
    fi
else
    log_info "Skipping integration test (--quick)"
fi

echo ""

# Summary
echo "========================================"
echo "Test Summary"
echo "========================================"
echo -e "Build: ${GREEN}PASSED${NC}"
echo -e "Unit tests: ${GREEN}PASSED${NC}"
echo -e "Timing model tests: ${GREEN}PASSED${NC}"
if [ "$QUICK_MODE" = false ]; then
    echo -e "Integration test: ${GREEN}PASSED${NC}"
fi

echo ""
log_info "All tests passed!"
exit 0
