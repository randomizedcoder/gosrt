#!/usr/bin/env bash
#
# set_latency.sh - Switch latency profile in SRT test network
#
# This script changes the routing to use a different inter-router link,
# effectively changing the network latency. Unlike modifying netem directly,
# this approach doesn't flush the netem queue, preserving packets in-flight.
#
# Latency profiles (RTT):
#   0 - No delay (0ms RTT) - baseline testing
#   1 - Regional datacenter (10ms RTT)
#   2 - Cross-continental (60ms RTT)
#   3 - Intercontinental (130ms RTT)
#   4 - GEO satellite (300ms RTT)
#
# Usage:
#   sudo ./set_latency.sh <profile>
#   sudo TEST_ID=mytest ./set_latency.sh 2
#
# shellcheck shell=bash

set -euo pipefail

# Get script directory for sourcing lib.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source the library
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

#=============================================================================
# USAGE
#=============================================================================

usage() {
    echo "Usage: $0 <profile>"
    echo ""
    echo "Latency profiles:"
    echo "  0 - No delay (0ms RTT)"
    echo "  1 - Regional datacenter (10ms RTT)"
    echo "  2 - Cross-continental (60ms RTT)"
    echo "  3 - Intercontinental (130ms RTT)"
    echo "  4 - GEO satellite (300ms RTT)"
    echo ""
    echo "Example:"
    echo "  sudo $0 2    # Set 60ms RTT latency"
    exit 1
}

#=============================================================================
# MAIN
#=============================================================================

main() {
    # Check for root privileges
    if [[ "${EUID}" -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi

    # Check arguments
    if [[ $# -lt 1 ]]; then
        usage
    fi

    local profile="$1"

    # Validate profile
    if ! [[ "${profile}" =~ ^[0-4]$ ]]; then
        log_error "Invalid profile: ${profile} (must be 0-4)"
        usage
    fi

    # Check if network exists
    if ! namespace_exists "${NAMESPACE_ROUTER_CLIENT}"; then
        log_error "Network not found. Run setup.sh first."
        exit 1
    fi

    # Set latency profile
    set_latency_profile "${profile}"

    # Show result
    local description
    description=$(get_latency_description "${profile}")
    log_info "Latency profile set to ${profile} (${description})"
}

# Run main
main "$@"

