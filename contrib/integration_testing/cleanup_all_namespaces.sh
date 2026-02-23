#!/bin/bash
# cleanup_all_namespaces.sh - Emergency cleanup of all stale SRT test network namespaces
#
# This script forcefully removes ALL network namespaces matching ns_* pattern.
# Use this when normal cleanup fails and stale namespaces prevent tests from running.
#
# Usage: sudo ./cleanup_all_namespaces.sh
#
# The script will:
# 1. List all existing namespaces
# 2. Delete any starting with "ns_" (SRT test namespaces)
# 3. Report how many were cleaned up

set -e

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "ERROR: This script must be run as root (sudo)"
    exit 1
fi

echo "═══════════════════════════════════════════════════════════════"
echo " SRT Network Namespace Emergency Cleanup"
echo "═══════════════════════════════════════════════════════════════"

# Function to count SRT namespaces
count_srt_namespaces() {
    local count
    count=$(ip netns list 2>/dev/null | awk '{print $1}' | grep -cE '^ns_' 2>/dev/null) || count=0
    echo "$count"
}

# Count namespaces before cleanup
BEFORE=$(count_srt_namespaces)
echo ""
echo "Found $BEFORE stale SRT namespaces (ns_*)"

if [ "$BEFORE" -eq 0 ]; then
    echo "✓ No stale namespaces to clean up"
    exit 0
fi

echo ""
echo "Cleaning up..."

# Delete all ns_* namespaces
# Note: ip netns list may include "(id: N)" suffix, so we extract just first field
DELETED=0
FAILED=0

for ns in $(ip netns list 2>/dev/null | awk '{print $1}' | grep -E '^ns_'); do
    if ip netns del "$ns" 2>/dev/null; then
        echo "  ✓ Deleted: $ns"
        DELETED=$((DELETED + 1))
    else
        echo "  ✗ Failed to delete: $ns"
        FAILED=$((FAILED + 1))
    fi
done

# Count namespaces after cleanup
AFTER=$(count_srt_namespaces)

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo " Cleanup Complete"
echo "═══════════════════════════════════════════════════════════════"
echo "  Before:  $BEFORE namespaces"
echo "  Deleted: $DELETED namespaces"
echo "  Failed:  $FAILED namespaces"
echo "  After:   $AFTER namespaces"

if [ "$AFTER" -gt 0 ]; then
    echo ""
    echo "WARNING: $AFTER namespaces could not be removed."
    echo "Remaining namespaces:"
    ip netns list | awk '{print $1}' | grep -E '^ns_' | head -10
    exit 1
fi

echo ""
echo "✓ All SRT test namespaces cleaned up successfully"
