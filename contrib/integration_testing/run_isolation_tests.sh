#!/bin/bash
# run_isolation_tests.sh - Runs all isolation tests and captures output
#
# Usage: sudo ./run_isolation_tests.sh
#
# This script runs all 11 isolation tests sequentially, capturing output
# to temporary files for later review.
#
# Tests 0-6: Original io_uring and packet store btree isolation
# Tests 7-10: NAK btree isolation tests

# Don't use set -e as tee can cause false failures

# Check for root
if [[ $EUID -ne 0 ]]; then
    echo "Error: This script must be run as root"
    echo "Usage: sudo $0"
    exit 1
fi

# Test configurations in order
TESTS=(
    # Original isolation tests (0-6)
    "Isolation-Control"
    "Isolation-CG-IoUringSend"
    "Isolation-CG-IoUringRecv"
    "Isolation-CG-Btree"
    "Isolation-Server-IoUringSend"
    "Isolation-Server-IoUringRecv"
    "Isolation-Server-Btree"
    # NAK btree isolation tests (7-10)
    "Isolation-Server-NakBtree"
    "Isolation-Server-NakBtree-IoUringRecv"
    "Isolation-CG-HonorNakOrder"
    "Isolation-FullNakBtree"
)

# Create output directory with timestamp
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
OUTPUT_DIR=$(mktemp -d "/tmp/isolation_tests_${TIMESTAMP}_XXXXXX")
echo "═══════════════════════════════════════════════════════════════"
echo " Isolation Test Batch Runner"
echo " Output directory: $OUTPUT_DIR"
echo " Tests: ${#TESTS[@]}"
echo " Estimated time: ~$((${#TESTS[@]} * 35)) seconds"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/../.."  # Go to project root

echo ""

# Results tracking
declare -a RESULTS
PASSED=0
FAILED=0

# Run all tests sequentially
for i in "${!TESTS[@]}"; do
    TEST="${TESTS[$i]}"
    TEST_NUM=$((i))
    OUTPUT_FILE="$OUTPUT_DIR/test${TEST_NUM}_${TEST}.log"

    echo ""
    echo "╔════════════════════════════════════════════════════════════════╗"
    echo "║ Test $TEST_NUM: $TEST"
    echo "╚════════════════════════════════════════════════════════════════╝"
    echo "Output: $OUTPUT_FILE"
    echo ""

    START_TIME=$(date +%s)

    # Run the test using go run, capturing output
    if (cd contrib/integration_testing && go run . isolation-test "$TEST") 2>&1 | tee "$OUTPUT_FILE"; then
        END_TIME=$(date +%s)
        ELAPSED=$((END_TIME - START_TIME))
        echo ""
        echo "✓ Test $TEST_NUM ($TEST) completed in ${ELAPSED}s"
        RESULTS[$i]="PASS"
        ((PASSED++))
    else
        END_TIME=$(date +%s)
        ELAPSED=$((END_TIME - START_TIME))
        echo ""
        echo "✗ Test $TEST_NUM ($TEST) failed after ${ELAPSED}s"
        RESULTS[$i]="FAIL"
        ((FAILED++))
    fi
done

# Generate summary
echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " SUMMARY"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# Create summary file
SUMMARY_FILE="$OUTPUT_DIR/SUMMARY.txt"
{
    echo "Isolation Test Results - $(date)"
    echo "Output directory: $OUTPUT_DIR"
    echo ""
    echo "Results: $PASSED passed, $FAILED failed"
    echo ""
    echo "Individual test results:"
    for i in "${!TESTS[@]}"; do
        TEST="${TESTS[$i]}"
        RESULT="${RESULTS[$i]}"
        printf "  Test %d: %-30s %s\n" "$i" "$TEST" "$RESULT"
    done
    echo ""
    echo "============================================"
    echo "Gap counts by test:"
    echo ""
    for i in "${!TESTS[@]}"; do
        TEST="${TESTS[$i]}"
        OUTPUT_FILE="$OUTPUT_DIR/test${i}_${TEST}.log"
        echo "--- Test $i: $TEST ---"
        # Extract the comparison table from output
        grep -A 20 "ISOLATION TEST RESULTS" "$OUTPUT_FILE" 2>/dev/null | head -20 || echo "(no results found)"
        echo ""
    done
} | tee "$SUMMARY_FILE"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " All tests complete!"
echo " Results: $PASSED passed, $FAILED failed"
echo " Output directory: $OUTPUT_DIR"
echo " Summary: $SUMMARY_FILE"
echo "═══════════════════════════════════════════════════════════════"

# Exit with failure if any tests failed
if [[ $FAILED -gt 0 ]]; then
    exit 1
fi

