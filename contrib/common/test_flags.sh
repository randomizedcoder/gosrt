#!/bin/bash
# Test script for validating CLI flags functionality
# This script tests various flag combinations and validates the output

CLIENT_BIN="./contrib/client/client"
SERVER_BIN="./contrib/server/server"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test counter
TESTS_PASSED=0
TESTS_FAILED=0

# Initialize counters (needed for arithmetic in set -e mode)
: ${TESTS_PASSED:=0}
: ${TESTS_FAILED:=0}

# Function to run a test
run_test() {
	local name="$1"
	local flags="$2"
	local expected_pattern="$3"
	local binary="$4"

	echo -n "Testing: $name ... "

	# Run the command and capture output
	local output
	if ! output=$($binary -testflags $flags 2>&1); then
		echo -e "${RED}FAILED${NC} - Command failed"
		TESTS_FAILED=$((TESTS_FAILED + 1))
		return 1
	fi

	# Check if output contains expected pattern
	# For patterns with .*, split and check each part separately
	if echo "$expected_pattern" | grep -q '\.\*'; then
		# Multiple patterns separated by .* - check each one
		local all_match=true
		# Use sed to split by .* and extract patterns
		local temp_file=$(mktemp)
		echo "$expected_pattern" | sed 's/\.\*/\
/g' > "$temp_file"
		while IFS= read -r pattern; do
			if [ -n "$pattern" ]; then
				if ! echo "$output" | grep -qE "$pattern"; then
					all_match=false
					break
				fi
			fi
		done < "$temp_file"
		rm -f "$temp_file"
		if [ "$all_match" = true ]; then
			echo -e "${GREEN}PASSED${NC}"
			((TESTS_PASSED++))
			return 0
		fi
	else
		# Single pattern - simple check
		if echo "$output" | grep -qE "$expected_pattern"; then
			echo -e "${GREEN}PASSED${NC}"
			((TESTS_PASSED++))
			return 0
		fi
	fi
	# If we get here, the test failed
	echo -e "${RED}FAILED${NC}"
	echo "  Expected pattern: $expected_pattern"
	echo "  Got output:"
	echo "$output" | head -10 | sed 's/^/    /'
	((TESTS_FAILED++))
	return 1
}

# Check if binaries exist
if [ ! -f "$CLIENT_BIN" ]; then
	echo -e "${YELLOW}Warning: $CLIENT_BIN not found. Building...${NC}"
	cd "$(dirname "$CLIENT_BIN")" && make client && cd - > /dev/null
fi

if [ ! -f "$SERVER_BIN" ]; then
	echo -e "${YELLOW}Warning: $SERVER_BIN not found. Building...${NC}"
	cd "$(dirname "$SERVER_BIN")" && make server && cd - > /dev/null
fi

echo "Testing CLI flags functionality"
echo "================================"
echo ""

# Test 1: Default config (no flags)
run_test "Default config" "" '"Congestion" *: *"live"' "$CLIENT_BIN"

# Test 2: String flag
run_test "String flag (congestion)" "-congestion file" '"Congestion" *: *"file"' "$CLIENT_BIN"

# Test 3: Int flag (latency in milliseconds)
run_test "Int flag (latency)" "-latency 200" '"Latency" *: *200000000' "$CLIENT_BIN"

# Test 4: Uint64 flag
run_test "Uint64 flag (fc)" "-fc 51200" '"FC" *: *51200' "$CLIENT_BIN"

# Test 5: Int64 flag
run_test "Int64 flag (maxbw)" "-maxbw 100000000" '"MaxBW" *: *100000000' "$CLIENT_BIN"

# Test 6: Bool flag (true)
run_test "Bool flag (enforcedencryption=true)" "-enforcedencryption true" '"EnforcedEncryption" *: *true' "$CLIENT_BIN"

# Test 7: Bool flag (false)
run_test "Bool flag (drifttracer=false)" "-drifttracer false" '"DriftTracer" *: *false' "$CLIENT_BIN"

# Test 8: Multiple flags
run_test "Multiple flags" "-congestion file -latency 300 -fc 25600" '"Congestion" *: *"file".*"Latency" *: *300000000.*"FC" *: *25600' "$CLIENT_BIN"

# Test 9: Zero value flag (should override default)
run_test "Zero value flag (conntimeo=0)" "-conntimeo 0" '"ConnectionTimeout" *: *0' "$CLIENT_BIN"

# Test 10: Negative value flag
run_test "Negative value flag (maxbw=-1)" "-maxbw -1" '"MaxBW" *: *-1' "$CLIENT_BIN"

# Test 11: Server-specific passphrase flag
run_test "Server passphrase flag" "-passphrase secret123" '"Passphrase" *: *"secret123"' "$SERVER_BIN"

# Test 12: Server with shared flags
run_test "Server with shared flags" "-latency 150 -fc 25600" '"Latency" *: *150000000.*"FC" *: *25600' "$SERVER_BIN"

# Test 13: All flag types combined
run_test "All flag types" "-congestion file -latency 200 -fc 51200 -maxbw 100000000 -enforcedencryption true -drifttracer false" '"Congestion" *: *"file".*"Latency" *: *200000000.*"FC" *: *51200.*"MaxBW" *: *100000000.*"EnforcedEncryption" *: *true.*"DriftTracer" *: *false' "$CLIENT_BIN"

echo ""
echo "================================"
echo "Results: $TESTS_PASSED passed, $TESTS_FAILED failed"

if [ $TESTS_FAILED -eq 0 ]; then
	echo -e "${GREEN}All tests passed!${NC}"
	exit 0
else
	echo -e "${RED}Some tests failed!${NC}"
	exit 1
fi

