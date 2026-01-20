#!/bin/bash
# profile_compare.sh - Compare profiles from two different runs
#
# Usage:
#   ./profile_compare.sh BASELINE_DIR TEST_DIR [OPTIONS]
#
# Options:
#   -c, --component NAME  Component to compare: server, seeker (default: both)
#   -p, --profile TYPE    Profile type to compare (default: cpu)
#   -h, --help            Show this help
#
# Example:
#   ./profile_compare.sh /tmp/profiles_300M /tmp/profiles_400M
#   ./profile_compare.sh /tmp/profiles_300M /tmp/profiles_400M -c seeker -p heap
#
# This script helps identify what changed between a stable run and a failure.

set -euo pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
    grep '^#' "$0" | grep -v '#!/' | sed 's/^# *//'
    exit 0
}

# Defaults
COMPONENT="both"
PROFILE_TYPE="cpu"

# Parse positional args
if [[ $# -lt 2 ]]; then
    log_error "Missing required arguments: BASELINE_DIR and TEST_DIR"
    usage
fi

BASELINE_DIR="$1"
TEST_DIR="$2"
shift 2

# Parse options
while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--component) COMPONENT="$2"; shift 2 ;;
        -p|--profile) PROFILE_TYPE="$2"; shift 2 ;;
        -h|--help) usage ;;
        *) log_error "Unknown option: $1"; usage ;;
    esac
done

# Validate directories
for dir in "$BASELINE_DIR" "$TEST_DIR"; do
    if [[ ! -d "$dir" ]]; then
        log_error "Directory not found: $dir"
        exit 1
    fi
done

log_info "═══════════════════════════════════════════════════════════════"
log_info "Profile Comparison"
log_info "═══════════════════════════════════════════════════════════════"
log_info "  Baseline: $BASELINE_DIR"
log_info "  Test:     $TEST_DIR"
log_info "  Profile:  $PROFILE_TYPE"
log_info "═══════════════════════════════════════════════════════════════"

compare_component() {
    local comp="$1"
    local base_profile="$BASELINE_DIR/$comp/${PROFILE_TYPE}.pprof"
    local test_profile="$TEST_DIR/$comp/${PROFILE_TYPE}.pprof"
    
    echo ""
    log_info "═══════════════════════════════════════════════════════════════"
    log_info "Component: $comp"
    log_info "═══════════════════════════════════════════════════════════════"
    
    if [[ ! -f "$base_profile" ]]; then
        log_warn "Baseline profile not found: $base_profile"
        return
    fi
    
    if [[ ! -f "$test_profile" ]]; then
        log_warn "Test profile not found: $test_profile"
        return
    fi
    
    echo ""
    echo "=== Baseline Top 10 ($comp) ==="
    go tool pprof -top -nodecount=10 "$base_profile" 2>/dev/null || log_warn "Could not analyze baseline"
    
    echo ""
    echo "=== Test Top 10 ($comp) ==="
    go tool pprof -top -nodecount=10 "$test_profile" 2>/dev/null || log_warn "Could not analyze test"
    
    echo ""
    echo "=== Differential (base vs test) ==="
    echo "Functions with increased time in TEST vs BASELINE:"
    # Use pprof diff to show what got worse
    go tool pprof -top -nodecount=10 -diff_base="$base_profile" "$test_profile" 2>/dev/null || log_warn "Could not generate diff"
}

# Compare components
if [[ "$COMPONENT" == "both" ]]; then
    compare_component "server"
    compare_component "seeker"
else
    compare_component "$COMPONENT"
fi

echo ""
log_info "═══════════════════════════════════════════════════════════════"
log_info "Interactive Analysis"
log_info "═══════════════════════════════════════════════════════════════"
log_info ""
log_info "To analyze baseline interactively:"
log_info "  go tool pprof -http=:8080 $BASELINE_DIR/server/${PROFILE_TYPE}.pprof"
log_info ""
log_info "To analyze test with diff highlighting:"
log_info "  go tool pprof -http=:8080 -diff_base=$BASELINE_DIR/server/${PROFILE_TYPE}.pprof $TEST_DIR/server/${PROFILE_TYPE}.pprof"
