#!/usr/bin/env bash
#
# cleanup.sh - Remove SRT test network namespaces
#
# This script removes all network namespaces and resources created by setup.sh.
# It can be run at any time to clean up after a test.
#
# Usage:
#   sudo ./cleanup.sh                  # Cleanup network with default ID (PID of setup.sh)
#   sudo TEST_ID=mytest ./cleanup.sh   # Cleanup network with custom ID
#
# shellcheck shell=bash

set -euo pipefail

# Get script directory for sourcing lib.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source the library
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

#=============================================================================
# MAIN
#=============================================================================

main() {
    # Check for root privileges
    if [[ "${EUID}" -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi

    # Check if state file exists
    if [[ ! -f "${STATE_FILE}" ]]; then
        log_warn "State file not found: ${STATE_FILE}"
        log_info "Attempting cleanup anyway..."
    fi

    # Cleanup the network
    cleanup_srt_network

    log_info "Network cleanup complete"
}

# Run main
main "$@"

