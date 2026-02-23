#!/usr/bin/env bash
#
# setup.sh - Create SRT test network with namespace isolation
#
# This script creates an isolated network environment for testing SRT's
# packet loss recovery mechanisms. It creates:
#   - 5 namespaces (publisher, subscriber, server, 2 routers)
#   - 5 inter-router links with fixed latency (0ms, 10ms, 60ms, 130ms, 300ms RTT)
#   - Routing-based latency switching (no queue flush)
#
# Usage:
#   sudo ./setup.sh                    # Create network with default ID (PID)
#   sudo TEST_ID=mytest ./setup.sh     # Create network with custom ID
#
# After setup, use:
#   ./set_latency.sh <0-4>             # Switch latency profile
#   ./set_loss.sh <0-100>              # Set loss percentage
#
# Cleanup:
#   sudo ./cleanup.sh                  # Remove network
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

    # Check for required commands
    for cmd in ip tc sysctl; do
        if ! command -v "${cmd}" &>/dev/null; then
            log_error "Required command not found: ${cmd}"
            exit 1
        fi
    done

    # Setup the network
    setup_srt_network

    # Print status
    echo ""
    print_network_status

    # Print usage instructions
    echo ""
    echo "=== Next Steps ==="
    echo ""
    echo "Run processes in namespaces:"
    echo "  sudo ip netns exec ${NAMESPACE_SERVER} ./server -addr ${SUBNET_SERVER}.2:6000"
    echo "  sudo ip netns exec ${NAMESPACE_PUBLISHER} ./client-generator -to srt://${SUBNET_SERVER}.2:6000/stream"
    echo "  sudo ip netns exec ${NAMESPACE_SUBSCRIBER} ./client -from srt://${SUBNET_SERVER}.2:6000/stream"
    echo ""
    echo "Control latency (0=none, 1=10ms, 2=60ms, 3=130ms, 4=300ms RTT):"
    echo "  sudo ${SCRIPT_DIR}/set_latency.sh 2"
    echo ""
    echo "Control loss (0-100%):"
    echo "  sudo ${SCRIPT_DIR}/set_loss.sh 5"
    echo ""
    echo "Cleanup:"
    echo "  sudo ${SCRIPT_DIR}/cleanup.sh"
    echo ""
    echo "State file: ${STATE_FILE}"
}

# Run main
main "$@"

