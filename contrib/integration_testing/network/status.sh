#!/usr/bin/env bash
#
# status.sh - Display human-friendly status of SRT test network
#
# This script shows a summary of all network namespaces, interfaces, and routes
# created by setup.sh. Use it to verify the network is configured correctly.
#
# Usage:
#   sudo ./status.sh                  # Show status for default ID
#   sudo TEST_ID=mytest ./status.sh   # Show status for custom ID
#
# shellcheck shell=bash

set -euo pipefail

# Get script directory for sourcing lib.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source the library
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

#=============================================================================
# DISPLAY FUNCTIONS
#=============================================================================

# Print a section header
print_header() {
    local title="$1"
    local width=70
    echo ""
    printf '=%.0s' $(seq 1 ${width})
    echo ""
    echo "  ${title}"
    printf '=%.0s' $(seq 1 ${width})
    echo ""
}

# Print a subsection header
print_subheader() {
    local title="$1"
    echo ""
    echo "--- ${title} ---"
}

# Print namespace info with interfaces and routes
# Usage: print_namespace_info <namespace> <expected_ip>
print_namespace_info() {
    local namespace="$1"
    local expected_ip="$2"

    if ! namespace_exists "${namespace}"; then
        echo "  ⚠ NOT FOUND"
        return
    fi

    echo "  Namespace: ${namespace}"
    echo "  Expected IP: ${expected_ip}"
    echo ""

    # Interfaces
    echo "  Interfaces:"
    run_in_namespace "${namespace}" ip -brief addr show 2>/dev/null | while read -r line; do
        echo "    ${line}"
    done

    # Routes
    echo ""
    echo "  Routes:"
    run_in_namespace "${namespace}" ip route show 2>/dev/null | while read -r line; do
        echo "    ${line}"
    done
}

# Print router info with all links
print_router_info() {
    local namespace="$1"
    # Note: friendly_name parameter removed as it's not used in current output

    if ! namespace_exists "${namespace}"; then
        echo "  ⚠ NOT FOUND"
        return
    fi

    echo "  Namespace: ${namespace}"
    echo ""

    # Interfaces (brief format for routers - they have many)
    echo "  Interfaces:"
    run_in_namespace "${namespace}" ip -brief addr show 2>/dev/null | while read -r line; do
        echo "    ${line}"
    done

    # Routes
    echo ""
    echo "  Routes:"
    run_in_namespace "${namespace}" ip route show 2>/dev/null | while read -r line; do
        echo "    ${line}"
    done

    # TC qdisc (netem) configuration
    echo ""
    echo "  Traffic Control (netem):"
    local has_qdisc=false
    run_in_namespace "${namespace}" tc qdisc show 2>/dev/null | grep -v "pfifo_fast\|noqueue" | while read -r line; do
        if [[ -n "${line}" ]]; then
            echo "    ${line}"
            has_qdisc=true
        fi
    done
    if [[ "${has_qdisc}" == "false" ]]; then
        echo "    (no netem configured)"
    fi
}

# Print connectivity test results
print_connectivity() {
    print_header "Connectivity Tests"

    echo ""
    echo "Testing ping from Publisher to Server..."
    if run_in_namespace "${NAMESPACE_PUBLISHER}" ping -c 1 -W 1 "${SUBNET_SERVER}.2" &>/dev/null; then
        echo "  ✓ Publisher -> Server: OK"
    else
        echo "  ✗ Publisher -> Server: FAILED"
    fi

    echo ""
    echo "Testing ping from Subscriber to Server..."
    if run_in_namespace "${NAMESPACE_SUBSCRIBER}" ping -c 1 -W 1 "${SUBNET_SERVER}.2" &>/dev/null; then
        echo "  ✓ Subscriber -> Server: OK"
    else
        echo "  ✗ Subscriber -> Server: FAILED"
    fi

    echo ""
    echo "Testing ping from Server to Publisher..."
    if run_in_namespace "${NAMESPACE_SERVER}" ping -c 1 -W 1 "${SUBNET_PUBLISHER}.2" &>/dev/null; then
        echo "  ✓ Server -> Publisher: OK"
    else
        echo "  ✗ Server -> Publisher: FAILED"
    fi

    echo ""
    echo "Testing ping from Server to Subscriber..."
    if run_in_namespace "${NAMESPACE_SERVER}" ping -c 1 -W 1 "${SUBNET_SUBSCRIBER}.2" &>/dev/null; then
        echo "  ✓ Server -> Subscriber: OK"
    else
        echo "  ✗ Server -> Subscriber: FAILED"
    fi
}

# Print current impairment settings
print_impairment_status() {
    print_header "Current Impairment Settings"

    echo ""
    echo "Latency Profile: ${CURRENT_LATENCY_PROFILE}"

    # Get description
    local profile="${LATENCY_PROFILES[${CURRENT_LATENCY_PROFILE}]}"
    local rtt_ms description
    read -r _ rtt_ms description <<< "${profile}"
    echo "  RTT: ${rtt_ms}ms (${description})"

    # Check for blackhole routes
    echo ""
    echo "Blackhole Routes (100% loss):"
    local has_blackhole=false
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route show type blackhole 2>/dev/null | while read -r line; do
        if [[ -n "${line}" ]]; then
            echo "  ⚠ ${line}"
            has_blackhole=true
        fi
    done
    if [[ "${has_blackhole}" == "false" ]]; then
        echo "  (none - normal routing)"
    fi

    # Check for netem loss
    echo ""
    echo "Netem Loss Configuration:"
    local interface_name="link${CURRENT_LATENCY_PROFILE}_a"
    local tc_output
    tc_output=$(run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" tc qdisc show dev "${interface_name}" 2>/dev/null || echo "")
    if echo "${tc_output}" | grep -q "loss"; then
        echo "  ${tc_output}" | grep "netem"
    else
        echo "  (no probabilistic loss configured)"
    fi
}

# Print usage instructions
print_usage_instructions() {
    print_header "Usage Instructions"

    echo ""
    echo "Run processes in namespaces:"
    echo ""
    echo "  # Server"
    echo "  sudo ip netns exec ${NAMESPACE_SERVER} \\"
    echo "      ./server -addr ${SUBNET_SERVER}.2:6000 -promuds /tmp/server.sock"
    echo ""
    echo "  # Publisher (client-generator)"
    echo "  sudo ip netns exec ${NAMESPACE_PUBLISHER} \\"
    echo "      ./client-generator -to srt://${SUBNET_SERVER}.2:6000/stream -promuds /tmp/pub.sock"
    echo ""
    echo "  # Subscriber (client)"
    echo "  sudo ip netns exec ${NAMESPACE_SUBSCRIBER} \\"
    echo "      ./client -from srt://${SUBNET_SERVER}.2:6000/stream -promuds /tmp/sub.sock"
    echo ""
    echo "Control impairment:"
    echo ""
    echo "  sudo ${SCRIPT_DIR}/set_latency.sh <0-4>    # 0=0ms, 1=10ms, 2=60ms, 3=130ms, 4=300ms RTT"
    echo "  sudo ${SCRIPT_DIR}/set_loss.sh <0-100>    # 0=none, 100=blackhole, 1-99=netem"
    echo "  sudo ${SCRIPT_DIR}/starlink_pattern.sh start|stop"
    echo ""
    echo "Cleanup:"
    echo ""
    echo "  sudo ${SCRIPT_DIR}/cleanup.sh"
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

    # Check if network exists
    local network_exists=true
    for ns in "${NAMESPACE_PUBLISHER}" "${NAMESPACE_SUBSCRIBER}" "${NAMESPACE_SERVER}" \
              "${NAMESPACE_ROUTER_CLIENT}" "${NAMESPACE_ROUTER_SERVER}"; do
        if ! namespace_exists "${ns}"; then
            network_exists=false
            break
        fi
    done

    if [[ "${network_exists}" == "false" ]]; then
        echo ""
        echo "⚠ SRT Test Network not found (ID: ${TEST_ID})"
        echo ""
        echo "Run setup first:"
        echo "  sudo ${SCRIPT_DIR}/setup.sh"
        echo ""
        echo "Or specify a different TEST_ID:"
        echo "  sudo TEST_ID=<id> ${SCRIPT_DIR}/status.sh"
        exit 1
    fi

    # Print summary
    print_header "SRT Test Network Status (ID: ${TEST_ID})"

    echo ""
    echo "Network topology:"
    echo ""
    echo "  ┌─────────────────┐                    ┌─────────────────┐"
    echo "  │   Publisher     │                    │   Subscriber    │"
    echo "  │  ${SUBNET_PUBLISHER}.2       │                    │  ${SUBNET_SUBSCRIBER}.2       │"
    echo "  └────────┬────────┘                    └────────┬────────┘"
    echo "           │                                      │"
    echo "           └──────────────┬───────────────────────┘"
    echo "                          │"
    echo "                 ┌────────▼────────┐"
    echo "                 │  Router Client  │"
    echo "                 │  (ns_router_a)  │"
    echo "                 └────────┬────────┘"
    echo "                          │"
    echo "               5 parallel links (0-300ms RTT)"
    echo "                          │"
    echo "                 ┌────────▼────────┐"
    echo "                 │  Router Server  │"
    echo "                 │  (ns_router_b)  │"
    echo "                 └────────┬────────┘"
    echo "                          │"
    echo "                 ┌────────▼────────┐"
    echo "                 │     Server      │"
    echo "                 │  ${SUBNET_SERVER}.2        │"
    echo "                 └─────────────────┘"

    # Endpoint namespaces
    print_header "Endpoint Namespaces"

    print_subheader "Publisher (Client-Generator)"
    print_namespace_info "${NAMESPACE_PUBLISHER}" "${SUBNET_PUBLISHER}.2"

    print_subheader "Subscriber (Client)"
    print_namespace_info "${NAMESPACE_SUBSCRIBER}" "${SUBNET_SUBSCRIBER}.2"

    print_subheader "Server"
    print_namespace_info "${NAMESPACE_SERVER}" "${SUBNET_SERVER}.2"

    # Router namespaces
    print_header "Router Namespaces"

    print_subheader "Client-side Router (ns_router_a)"
    print_router_info "${NAMESPACE_ROUTER_CLIENT}"

    print_subheader "Server-side Router (ns_router_b)"
    print_router_info "${NAMESPACE_ROUTER_SERVER}"

    # Impairment status
    print_impairment_status

    # Connectivity tests
    print_connectivity

    # Usage instructions
    print_usage_instructions

    echo ""
}

# Run main
main "$@"

