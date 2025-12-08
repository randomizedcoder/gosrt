#!/usr/bin/env bash
#
# lib.sh - Shared functions and variables for SRT network impairment testing
#
# This library provides functions for creating network namespaces with
# controlled latency and packet loss for testing SRT's ARQ mechanisms.
#
# Architecture:
#   ns_publisher  ──┐
#                   ├── ns_router_a ═══════ ns_router_b ─── ns_server
#   ns_subscriber ──┘     (5 parallel links with fixed latency)
#
# Usage:
#   source lib.sh
#   setup_srt_network
#   set_latency_profile 2  # 60ms RTT
#   set_loss_percent 5     # 5% loss
#   cleanup_srt_network
#
# shellcheck shell=bash

set -euo pipefail

#=============================================================================
# CONFIGURATION - Human-readable variable names
#=============================================================================

# Unique suffix for this test run (allows parallel test runs)
# Can be overridden: TEST_ID=mytest ./setup.sh
readonly TEST_ID="${TEST_ID:-$$}"

# Namespace names - descriptive names for each component
readonly NAMESPACE_PUBLISHER="ns_publisher_${TEST_ID}"
readonly NAMESPACE_SUBSCRIBER="ns_subscriber_${TEST_ID}"
readonly NAMESPACE_SERVER="ns_server_${TEST_ID}"
readonly NAMESPACE_ROUTER_CLIENT="ns_router_a_${TEST_ID}"   # Client-side router
readonly NAMESPACE_ROUTER_SERVER="ns_router_b_${TEST_ID}"   # Server-side router

# IP Subnets - organized by network segment
readonly SUBNET_PUBLISHER="10.1.1"        # Publisher <-> Client Router
readonly SUBNET_SUBSCRIBER="10.1.2"       # Subscriber <-> Client Router
readonly SUBNET_SERVER="10.2.1"           # Server <-> Server Router
readonly SUBNET_INTERROUTER="10.100"      # Inter-router links (10.100.X.Y)

# Netem queue limit - prevents tail-drop during high latency
# At 10 Mb/s with 300ms latency, need ~250 packets minimum
# 50,000 provides large headroom for bursts
readonly NETEM_QUEUE_LIMIT=50000

# Latency profiles - RTT in milliseconds
# Array format: "link_index rtt_ms description"
declare -a LATENCY_PROFILES=(
    "0 0 no_delay"              # Link 0: 0ms RTT (baseline)
    "1 10 regional_datacenter"  # Link 1: 10ms RTT (5ms each way)
    "2 60 cross_continental"    # Link 2: 60ms RTT (30ms each way)
    "3 130 intercontinental"    # Link 3: 130ms RTT (65ms each way)
    "4 300 geo_satellite"       # Link 4: 300ms RTT (150ms each way)
)

# State file for tracking current configuration
readonly STATE_FILE="/tmp/srt_network_state_${TEST_ID}"

# Current latency profile (0 = no delay by default)
CURRENT_LATENCY_PROFILE=0

#=============================================================================
# LOGGING FUNCTIONS
#=============================================================================

log_info() {
    echo "[INFO] $(date '+%H:%M:%S') $*"
}

log_warn() {
    echo "[WARN] $(date '+%H:%M:%S') $*" >&2
}

log_error() {
    echo "[ERROR] $(date '+%H:%M:%S') $*" >&2
}

log_debug() {
    if [[ "${SRT_NETWORK_DEBUG:-0}" == "1" ]]; then
        echo "[DEBUG] $(date '+%H:%M:%S') $*" >&2
    fi
}

#=============================================================================
# NAMESPACE HELPER FUNCTIONS
#=============================================================================

# Run a command in a specific network namespace
# Usage: run_in_namespace <namespace_name> <command> [args...]
run_in_namespace() {
    local namespace_name="$1"
    shift
    ip netns exec "${namespace_name}" "$@"
}

# Check if a namespace exists
# Usage: namespace_exists <namespace_name>
namespace_exists() {
    local namespace_name="$1"
    ip netns list | grep -q "^${namespace_name}$" 2>/dev/null
}

# Create a namespace if it doesn't exist
# Usage: create_namespace <namespace_name>
create_namespace() {
    local namespace_name="$1"

    if namespace_exists "${namespace_name}"; then
        log_warn "Namespace ${namespace_name} already exists"
        return 0
    fi

    log_debug "Creating namespace: ${namespace_name}"
    ip netns add "${namespace_name}"
}

# Delete a namespace if it exists
# Usage: delete_namespace <namespace_name>
delete_namespace() {
    local namespace_name="$1"

    if ! namespace_exists "${namespace_name}"; then
        log_debug "Namespace ${namespace_name} does not exist, skipping"
        return 0
    fi

    log_debug "Deleting namespace: ${namespace_name}"
    ip netns del "${namespace_name}"
}

#=============================================================================
# NETWORK INTERFACE FUNCTIONS
#=============================================================================

# Create a veth pair connecting an endpoint namespace to a router namespace
# Usage: create_endpoint_connection <endpoint_ns> <router_ns> <endpoint_iface> \
#            <router_iface> <endpoint_ip> <router_ip>
create_endpoint_connection() {
    local endpoint_namespace="$1"
    local router_namespace="$2"
    local endpoint_interface="$3"
    local router_interface="$4"
    local endpoint_ip="$5"
    local router_ip="$6"

    log_info "Creating connection: ${endpoint_namespace}/${endpoint_interface} <-> ${router_namespace}/${router_interface}"

    # Create veth pair in default namespace
    ip link add "${endpoint_interface}" type veth peer name "${router_interface}"

    # Move interfaces to their respective namespaces
    ip link set "${endpoint_interface}" netns "${endpoint_namespace}"
    ip link set "${router_interface}" netns "${router_namespace}"

    # Configure endpoint side
    run_in_namespace "${endpoint_namespace}" ip addr add "${endpoint_ip}/24" dev "${endpoint_interface}"
    run_in_namespace "${endpoint_namespace}" ip link set "${endpoint_interface}" up
    run_in_namespace "${endpoint_namespace}" ip link set lo up
    run_in_namespace "${endpoint_namespace}" ip route add default via "${router_ip}"

    # Configure router side
    run_in_namespace "${router_namespace}" ip addr add "${router_ip}/24" dev "${router_interface}"
    run_in_namespace "${router_namespace}" ip link set "${router_interface}" up
}

# Create an inter-router link with fixed latency
# Usage: create_interrouter_link <link_index> <rtt_milliseconds> <description>
create_interrouter_link() {
    local link_index="$1"
    local rtt_milliseconds="$2"
    local description="$3"

    local interface_client_router="link${link_index}_a"
    local interface_server_router="link${link_index}_b"
    local ip_client_router="${SUBNET_INTERROUTER}.${link_index}.1"
    local ip_server_router="${SUBNET_INTERROUTER}.${link_index}.2"
    local delay_milliseconds=$((rtt_milliseconds / 2))

    log_info "Creating inter-router link ${link_index}: ${rtt_milliseconds}ms RTT (${description})"

    # Create veth pair
    ip link add "${interface_client_router}" type veth peer name "${interface_server_router}"

    # Move to router namespaces
    ip link set "${interface_client_router}" netns "${NAMESPACE_ROUTER_CLIENT}"
    ip link set "${interface_server_router}" netns "${NAMESPACE_ROUTER_SERVER}"

    # Configure client-side router
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip addr add "${ip_client_router}/30" dev "${interface_client_router}"
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip link set "${interface_client_router}" up

    # Configure server-side router
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip addr add "${ip_server_router}/30" dev "${interface_server_router}"
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip link set "${interface_server_router}" up

    # Apply netem latency (only if RTT > 0)
    if [[ "${delay_milliseconds}" -gt 0 ]]; then
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" tc qdisc add dev "${interface_client_router}" \
            root netem delay "${delay_milliseconds}ms" limit "${NETEM_QUEUE_LIMIT}"
        run_in_namespace "${NAMESPACE_ROUTER_SERVER}" tc qdisc add dev "${interface_server_router}" \
            root netem delay "${delay_milliseconds}ms" limit "${NETEM_QUEUE_LIMIT}"
    fi
}

#=============================================================================
# LATENCY CONTROL - Via routing changes (no queue flush)
#=============================================================================

# Set the active latency profile by changing routes
# Usage: set_latency_profile <link_index>
#   0 = no delay, 1 = 10ms RTT, 2 = 60ms RTT, 3 = 130ms RTT, 4 = 300ms RTT
set_latency_profile() {
    local link_index="$1"
    local next_hop_server_router="${SUBNET_INTERROUTER}.${link_index}.2"
    local next_hop_client_router="${SUBNET_INTERROUTER}.${link_index}.1"

    log_info "Switching to latency profile: link${link_index}"

    # Client router: Route to server subnet via server router
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route replace "${SUBNET_SERVER}.0/24" \
        via "${next_hop_server_router}"

    # Server router: Route to publisher/subscriber subnets via client router
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route replace "${SUBNET_PUBLISHER}.0/24" \
        via "${next_hop_client_router}"
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip route replace "${SUBNET_SUBSCRIBER}.0/24" \
        via "${next_hop_client_router}"

    CURRENT_LATENCY_PROFILE="${link_index}"

    # Update state file
    echo "LATENCY_PROFILE=${link_index}" >> "${STATE_FILE}"
}

# Get the description of a latency profile
# Usage: get_latency_description <link_index>
get_latency_description() {
    local link_index="$1"
    local profile="${LATENCY_PROFILES[${link_index}]}"
    echo "${profile}" | cut -d' ' -f3
}

#=============================================================================
# LOSS CONTROL - Via blackhole routes (100%) or netem loss (probabilistic)
#=============================================================================

# Apply 100% packet loss using blackhole routes (instant effect)
# Usage: set_blackhole_loss
set_blackhole_loss() {
    log_info "Applying 100% loss via blackhole routes"

    # Add blackhole routes for server and subscriber subnets
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${SUBNET_SERVER}.0/24" 2>/dev/null || \
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route replace blackhole "${SUBNET_SERVER}.0/24"
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route add blackhole "${SUBNET_SUBSCRIBER}.0/24" 2>/dev/null || \
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route replace blackhole "${SUBNET_SUBSCRIBER}.0/24"
}

# Remove blackhole routes (restore normal routing)
# Usage: clear_blackhole_loss
clear_blackhole_loss() {
    log_info "Removing blackhole routes"

    # Remove blackhole routes (ignore errors if they don't exist)
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${SUBNET_SERVER}.0/24" 2>/dev/null || true
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route del blackhole "${SUBNET_SUBSCRIBER}.0/24" 2>/dev/null || true

    # Restore normal routing via current latency profile
    set_latency_profile "${CURRENT_LATENCY_PROFILE}"
}

# Apply probabilistic loss using netem
# Usage: set_netem_loss <percent>
set_netem_loss() {
    local loss_percent="$1"
    local link_index="${CURRENT_LATENCY_PROFILE}"
    local interface_name="link${link_index}_a"

    # Get current delay for this link
    local profile="${LATENCY_PROFILES[${link_index}]}"
    local rtt_milliseconds
    rtt_milliseconds=$(echo "${profile}" | cut -d' ' -f2)
    local delay_milliseconds=$((rtt_milliseconds / 2))

    log_info "Applying ${loss_percent}% loss on link${link_index}"

    if [[ "${delay_milliseconds}" -gt 0 ]]; then
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" tc qdisc change dev "${interface_name}" \
            root netem delay "${delay_milliseconds}ms" loss "${loss_percent}%" limit "${NETEM_QUEUE_LIMIT}"
    else
        # Link 0 has no delay, so we need to add qdisc first or change it
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" tc qdisc replace dev "${interface_name}" \
            root netem loss "${loss_percent}%" limit "${NETEM_QUEUE_LIMIT}"
    fi
}

# Clear netem loss (restore delay-only)
# Usage: clear_netem_loss
clear_netem_loss() {
    local link_index="${CURRENT_LATENCY_PROFILE}"
    local interface_name="link${link_index}_a"

    # Get current delay for this link
    local profile="${LATENCY_PROFILES[${link_index}]}"
    local rtt_milliseconds
    rtt_milliseconds=$(echo "${profile}" | cut -d' ' -f2)
    local delay_milliseconds=$((rtt_milliseconds / 2))

    log_info "Clearing loss on link${link_index} (restoring delay-only)"

    if [[ "${delay_milliseconds}" -gt 0 ]]; then
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" tc qdisc change dev "${interface_name}" \
            root netem delay "${delay_milliseconds}ms" limit "${NETEM_QUEUE_LIMIT}"
    else
        # Link 0 - just remove any loss
        run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" tc qdisc replace dev "${interface_name}" \
            root netem limit "${NETEM_QUEUE_LIMIT}"
    fi
}

# Combined loss control - handles both 100% (blackhole) and probabilistic (netem)
# Usage: set_loss_percent <percent>
#   0 = no loss, 100 = blackhole, 1-99 = netem probabilistic
set_loss_percent() {
    local loss_percent="$1"

    if [[ "${loss_percent}" -eq 0 ]]; then
        clear_blackhole_loss
        clear_netem_loss
    elif [[ "${loss_percent}" -eq 100 ]]; then
        set_blackhole_loss
    else
        clear_blackhole_loss
        set_netem_loss "${loss_percent}"
    fi
}

#=============================================================================
# MAIN SETUP AND CLEANUP
#=============================================================================

# Setup the complete SRT test network
# Usage: setup_srt_network
setup_srt_network() {
    log_info "Creating SRT test network (ID: ${TEST_ID})"

    # Create all namespaces
    log_info "Creating namespaces..."
    create_namespace "${NAMESPACE_PUBLISHER}"
    create_namespace "${NAMESPACE_SUBSCRIBER}"
    create_namespace "${NAMESPACE_SERVER}"
    create_namespace "${NAMESPACE_ROUTER_CLIENT}"
    create_namespace "${NAMESPACE_ROUTER_SERVER}"

    # Enable IP forwarding on routers
    log_info "Enabling IP forwarding on routers..."
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" sysctl -qw net.ipv4.ip_forward=1
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" sysctl -qw net.ipv4.ip_forward=1

    # Bring up loopback on routers
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip link set lo up
    run_in_namespace "${NAMESPACE_ROUTER_SERVER}" ip link set lo up

    # Create endpoint connections to client router
    log_info "Creating endpoint connections..."
    create_endpoint_connection "${NAMESPACE_PUBLISHER}" "${NAMESPACE_ROUTER_CLIENT}" \
        "eth0" "eth_pub" \
        "${SUBNET_PUBLISHER}.2" "${SUBNET_PUBLISHER}.1"

    create_endpoint_connection "${NAMESPACE_SUBSCRIBER}" "${NAMESPACE_ROUTER_CLIENT}" \
        "eth0" "eth_sub" \
        "${SUBNET_SUBSCRIBER}.2" "${SUBNET_SUBSCRIBER}.1"

    # Create server connection to server router
    create_endpoint_connection "${NAMESPACE_SERVER}" "${NAMESPACE_ROUTER_SERVER}" \
        "eth0" "eth_srv" \
        "${SUBNET_SERVER}.2" "${SUBNET_SERVER}.1"

    # Create inter-router links with fixed latency
    log_info "Creating inter-router links..."
    for profile in "${LATENCY_PROFILES[@]}"; do
        local link_index rtt_ms description
        read -r link_index rtt_ms description <<< "${profile}"
        create_interrouter_link "${link_index}" "${rtt_ms}" "${description}"
    done

    # Set initial routing to use link 0 (no latency)
    set_latency_profile 0

    # Save state
    {
        echo "TEST_ID=${TEST_ID}"
        echo "NAMESPACE_PUBLISHER=${NAMESPACE_PUBLISHER}"
        echo "NAMESPACE_SUBSCRIBER=${NAMESPACE_SUBSCRIBER}"
        echo "NAMESPACE_SERVER=${NAMESPACE_SERVER}"
        echo "NAMESPACE_ROUTER_CLIENT=${NAMESPACE_ROUTER_CLIENT}"
        echo "NAMESPACE_ROUTER_SERVER=${NAMESPACE_ROUTER_SERVER}"
        echo "SUBNET_PUBLISHER=${SUBNET_PUBLISHER}"
        echo "SUBNET_SUBSCRIBER=${SUBNET_SUBSCRIBER}"
        echo "SUBNET_SERVER=${SUBNET_SERVER}"
    } > "${STATE_FILE}"

    log_info "Network setup complete"
    log_info "  Publisher:  ${NAMESPACE_PUBLISHER} (${SUBNET_PUBLISHER}.2)"
    log_info "  Subscriber: ${NAMESPACE_SUBSCRIBER} (${SUBNET_SUBSCRIBER}.2)"
    log_info "  Server:     ${NAMESPACE_SERVER} (${SUBNET_SERVER}.2)"
}

# Cleanup all namespaces and resources
# Usage: cleanup_srt_network
cleanup_srt_network() {
    log_info "Cleaning up SRT test network (ID: ${TEST_ID})"

    # Delete namespaces (this automatically removes their interfaces)
    delete_namespace "${NAMESPACE_PUBLISHER}"
    delete_namespace "${NAMESPACE_SUBSCRIBER}"
    delete_namespace "${NAMESPACE_SERVER}"
    delete_namespace "${NAMESPACE_ROUTER_CLIENT}"
    delete_namespace "${NAMESPACE_ROUTER_SERVER}"

    # Remove state file
    rm -f "${STATE_FILE}"

    log_info "Cleanup complete"
}

#=============================================================================
# UTILITY FUNCTIONS
#=============================================================================

# Get the IP address for a component
# Usage: get_ip <component>
#   component: publisher, subscriber, server
get_ip() {
    local component="$1"
    case "${component}" in
        publisher)  echo "${SUBNET_PUBLISHER}.2" ;;
        subscriber) echo "${SUBNET_SUBSCRIBER}.2" ;;
        server)     echo "${SUBNET_SERVER}.2" ;;
        *) log_error "Unknown component: ${component}"; return 1 ;;
    esac
}

# Get the namespace for a component
# Usage: get_namespace <component>
get_namespace() {
    local component="$1"
    case "${component}" in
        publisher)  echo "${NAMESPACE_PUBLISHER}" ;;
        subscriber) echo "${NAMESPACE_SUBSCRIBER}" ;;
        server)     echo "${NAMESPACE_SERVER}" ;;
        *) log_error "Unknown component: ${component}"; return 1 ;;
    esac
}

# Run a command in a component's namespace
# Usage: run_in <component> <command> [args...]
run_in() {
    local component="$1"
    shift
    local namespace
    namespace=$(get_namespace "${component}")
    run_in_namespace "${namespace}" "$@"
}

# Print network status
# Usage: print_network_status
print_network_status() {
    echo "=== SRT Test Network Status (ID: ${TEST_ID}) ==="
    echo ""
    echo "Namespaces:"
    ip netns list | grep "_${TEST_ID}$" | while read -r ns; do
        echo "  - ${ns}"
    done
    echo ""
    echo "Current latency profile: ${CURRENT_LATENCY_PROFILE}"
    echo ""
    echo "Publisher (${SUBNET_PUBLISHER}.2):"
    run_in_namespace "${NAMESPACE_PUBLISHER}" ip addr show eth0 2>/dev/null | grep inet || echo "  (not configured)"
    echo ""
    echo "Subscriber (${SUBNET_SUBSCRIBER}.2):"
    run_in_namespace "${NAMESPACE_SUBSCRIBER}" ip addr show eth0 2>/dev/null | grep inet || echo "  (not configured)"
    echo ""
    echo "Server (${SUBNET_SERVER}.2):"
    run_in_namespace "${NAMESPACE_SERVER}" ip addr show eth0 2>/dev/null | grep inet || echo "  (not configured)"
    echo ""
    echo "Routing (Client Router -> Server):"
    run_in_namespace "${NAMESPACE_ROUTER_CLIENT}" ip route show "${SUBNET_SERVER}.0/24" 2>/dev/null || echo "  (not configured)"
}

