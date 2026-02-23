#!/usr/bin/env bash
#
# starlink_pattern.sh - Simulate Starlink LEO satellite reconvergence events
#
# Starlink satellites experience periodic reconvergence events where packets
# are briefly dropped. This script simulates that pattern:
#   - 100% packet loss for 50-70ms
#   - Occurs at seconds 12, 27, 42, 57 of each minute
#   - Pattern repeats every minute
#
# Usage:
#   sudo ./starlink_pattern.sh start    # Start pattern in background
#   sudo ./starlink_pattern.sh stop     # Stop pattern
#   sudo ./starlink_pattern.sh once     # Run one cycle (for testing)
#
# shellcheck shell=bash

set -euo pipefail

# Get script directory for sourcing lib.sh
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source the library
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

# PID file for the background process
readonly STARLINK_PID_FILE="/tmp/srt_starlink_pattern_${TEST_ID}.pid"

# Starlink event timing (seconds into each minute)
readonly -a STARLINK_EVENT_SECONDS=(12 27 42 57)

# Event duration (milliseconds)
readonly STARLINK_EVENT_DURATION_MS=60

#=============================================================================
# PATTERN FUNCTIONS
#=============================================================================

# Run a single Starlink event (100% drop for ~60ms)
run_starlink_event() {
    local event_time="$1"

    log_debug "Starlink event at second ${event_time}"

    # Apply blackhole routes
    set_blackhole_loss

    # Wait for event duration
    sleep "0.0${STARLINK_EVENT_DURATION_MS}"

    # Restore normal routing
    clear_blackhole_loss

    log_debug "Starlink event complete"
}

# Calculate time until next event
get_next_event_info() {
    local current_second
    current_second=$(date +%S | sed 's/^0//')  # Remove leading zero

    local next_event_second=""
    local wait_seconds=""

    for event_second in "${STARLINK_EVENT_SECONDS[@]}"; do
        if [[ "${event_second}" -gt "${current_second}" ]]; then
            next_event_second="${event_second}"
            wait_seconds=$((event_second - current_second))
            break
        fi
    done

    # If no event found in current minute, use first event of next minute
    if [[ -z "${next_event_second}" ]]; then
        next_event_second="${STARLINK_EVENT_SECONDS[0]}"
        wait_seconds=$((60 - current_second + next_event_second))
    fi

    echo "${next_event_second} ${wait_seconds}"
}

# Run the Starlink pattern continuously
run_pattern_loop() {
    log_info "Starting Starlink pattern loop"
    log_info "Events at seconds: ${STARLINK_EVENT_SECONDS[*]}"

    while true; do
        local next_info
        next_info=$(get_next_event_info)
        local next_second wait_seconds
        read -r next_second wait_seconds <<< "${next_info}"

        log_debug "Next event at second ${next_second}, waiting ${wait_seconds}s"
        sleep "${wait_seconds}"

        run_starlink_event "${next_second}"
    done
}

# Run one minute cycle (for testing)
run_one_cycle() {
    log_info "Running one Starlink cycle (events at seconds ${STARLINK_EVENT_SECONDS[*]})"

    local current_second=0

    for event_second in "${STARLINK_EVENT_SECONDS[@]}"; do
        # Wait until event time
        local wait_time=$((event_second - current_second))
        if [[ "${wait_time}" -gt 0 ]]; then
            log_info "Waiting ${wait_time}s until event at second ${event_second}"
            sleep "${wait_time}"
        fi
        current_second="${event_second}"

        log_info "Running event at second ${event_second}"
        run_starlink_event "${event_second}"
    done

    log_info "One cycle complete"
}

#=============================================================================
# CONTROL FUNCTIONS
#=============================================================================

start_pattern() {
    # Check if already running
    if [[ -f "${STARLINK_PID_FILE}" ]]; then
        local old_pid
        old_pid=$(cat "${STARLINK_PID_FILE}")
        if kill -0 "${old_pid}" 2>/dev/null; then
            log_error "Starlink pattern already running (PID: ${old_pid})"
            exit 1
        fi
        rm -f "${STARLINK_PID_FILE}"
    fi

    log_info "Starting Starlink pattern in background"

    # Start in background and save PID
    run_pattern_loop &
    local pattern_pid=$!
    echo "${pattern_pid}" > "${STARLINK_PID_FILE}"

    log_info "Starlink pattern started (PID: ${pattern_pid})"
    log_info "To stop: sudo ${SCRIPT_DIR}/starlink_pattern.sh stop"
}

stop_pattern() {
    if [[ ! -f "${STARLINK_PID_FILE}" ]]; then
        log_warn "Starlink pattern not running (no PID file)"
        return 0
    fi

    local pattern_pid
    pattern_pid=$(cat "${STARLINK_PID_FILE}")

    if kill -0 "${pattern_pid}" 2>/dev/null; then
        log_info "Stopping Starlink pattern (PID: ${pattern_pid})"
        kill "${pattern_pid}"

        # Wait for process to exit
        local wait_count=0
        while kill -0 "${pattern_pid}" 2>/dev/null && [[ "${wait_count}" -lt 10 ]]; do
            sleep 0.1
            ((wait_count++))
        done

        if kill -0 "${pattern_pid}" 2>/dev/null; then
            log_warn "Process didn't exit, sending SIGKILL"
            kill -9 "${pattern_pid}"
        fi
    else
        log_warn "Process ${pattern_pid} not running"
    fi

    rm -f "${STARLINK_PID_FILE}"

    # Make sure loss is cleared
    clear_blackhole_loss 2>/dev/null || true

    log_info "Starlink pattern stopped"
}

#=============================================================================
# USAGE
#=============================================================================

usage() {
    echo "Usage: $0 <command>"
    echo ""
    echo "Commands:"
    echo "  start  - Start Starlink pattern in background"
    echo "  stop   - Stop Starlink pattern"
    echo "  once   - Run one cycle (for testing)"
    echo ""
    echo "Starlink Pattern:"
    echo "  - 100% packet loss for ~60ms"
    echo "  - Events at seconds: ${STARLINK_EVENT_SECONDS[*]}"
    echo "  - Repeats every minute"
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

    local command="$1"

    # For start/once, verify network exists
    if [[ "${command}" == "start" || "${command}" == "once" ]]; then
        if ! namespace_exists "${NAMESPACE_ROUTER_CLIENT}"; then
            log_error "Network not found. Run setup.sh first."
            exit 1
        fi
    fi

    case "${command}" in
        start)
            start_pattern
            ;;
        stop)
            stop_pattern
            ;;
        once)
            run_one_cycle
            ;;
        *)
            log_error "Unknown command: ${command}"
            usage
            ;;
    esac
}

# Run main
main "$@"

