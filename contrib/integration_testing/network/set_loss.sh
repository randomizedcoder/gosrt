#!/usr/bin/env bash
#
# set_loss.sh - Set packet loss in SRT test network
#
# This script controls packet loss injection using two mechanisms:
#   - 100% loss: Uses blackhole routes (instant effect)
#   - Probabilistic loss (1-99%): Uses netem loss parameter
#
# Usage:
#   sudo ./set_loss.sh <percent>
#   sudo TEST_ID=mytest ./set_loss.sh 5
#
# Examples:
#   sudo ./set_loss.sh 0      # No loss (clear all loss)
#   sudo ./set_loss.sh 2      # 2% probabilistic loss
#   sudo ./set_loss.sh 5      # 5% probabilistic loss
#   sudo ./set_loss.sh 100    # 100% loss (blackhole routes)
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
    echo "Usage: $0 <percent>"
    echo ""
    echo "Set packet loss percentage (0-100)"
    echo ""
    echo "  0   - No loss (clear all loss)"
    echo "  1-99 - Probabilistic loss (via netem)"
    echo "  100 - Complete outage (via blackhole routes)"
    echo ""
    echo "Examples:"
    echo "  sudo $0 0      # Clear loss"
    echo "  sudo $0 2      # 2% loss"
    echo "  sudo $0 100    # 100% loss (simulates outage)"
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

    local loss_percent="$1"

    # Validate percent
    if ! [[ "${loss_percent}" =~ ^[0-9]+$ ]]; then
        log_error "Invalid loss percent: ${loss_percent} (must be 0-100)"
        usage
    fi

    if [[ "${loss_percent}" -gt 100 ]]; then
        log_error "Invalid loss percent: ${loss_percent} (must be 0-100)"
        usage
    fi

    # Check if network exists
    if ! namespace_exists "${NAMESPACE_ROUTER_CLIENT}"; then
        log_error "Network not found. Run setup.sh first."
        exit 1
    fi

    # Set loss
    set_loss_percent "${loss_percent}"

    # Show result
    if [[ "${loss_percent}" -eq 0 ]]; then
        log_info "Loss cleared"
    elif [[ "${loss_percent}" -eq 100 ]]; then
        log_info "100% loss set (blackhole routes)"
    else
        log_info "${loss_percent}% loss set (netem)"
    fi
}

# Run main
main "$@"

