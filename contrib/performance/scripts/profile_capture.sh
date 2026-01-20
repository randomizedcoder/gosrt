#!/bin/bash
# profile_capture.sh - Capture CPU profiles at a stable bitrate
#
# Usage:
#   ./profile_capture.sh [OPTIONS]
#
# Options:
#   -b, --bitrate MBPS   Target bitrate in Mb/s (default: 350)
#   -d, --duration SECS  Profile duration in seconds (default: 60)
#   -o, --output DIR     Output directory (default: /tmp/profiles_$(date +%Y%m%d_%H%M%S))
#   -p, --profile TYPE   Profile type: cpu, mem, heap, allocs, mutex, block (default: cpu)
#   -h, --help           Show this help
#
# Example:
#   ./profile_capture.sh -b 350 -d 60 -p cpu
#   ./profile_capture.sh --bitrate 400 --duration 30 --profile heap
#
# Output:
#   Creates profile files in OUTPUT_DIR/server/ and OUTPUT_DIR/seeker/
#   Prints analysis summary at the end

set -euo pipefail

# Default configuration
BITRATE_MBPS=350
DURATION_SECS=60
OUTPUT_DIR=""
PROFILE_TYPE="cpu"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

usage() {
    grep '^#' "$0" | grep -v '#!/' | sed 's/^# *//'
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -b|--bitrate) BITRATE_MBPS="$2"; shift 2 ;;
        -d|--duration) DURATION_SECS="$2"; shift 2 ;;
        -o|--output) OUTPUT_DIR="$2"; shift 2 ;;
        -p|--profile) PROFILE_TYPE="$2"; shift 2 ;;
        -h|--help) usage ;;
        *) log_error "Unknown option: $1"; usage ;;
    esac
done

# Set default output directory if not specified
if [[ -z "$OUTPUT_DIR" ]]; then
    OUTPUT_DIR="/tmp/profiles_${BITRATE_MBPS}M_$(date +%Y%m%d_%H%M%S)"
fi

# Derived values
BITRATE_BPS=$((BITRATE_MBPS * 1000000))
SERVER_BIN="$PROJECT_ROOT/contrib/server/server"
SEEKER_BIN="$PROJECT_ROOT/contrib/client-seeker/client-seeker"

# Validate binaries exist
if [[ ! -x "$SERVER_BIN" ]]; then
    log_error "Server binary not found: $SERVER_BIN"
    log_info "Run: cd $PROJECT_ROOT && go build -o contrib/server/server ./contrib/server/"
    exit 1
fi

if [[ ! -x "$SEEKER_BIN" ]]; then
    log_error "Seeker binary not found: $SEEKER_BIN"
    log_info "Run: cd $PROJECT_ROOT && go build -o contrib/client-seeker/client-seeker ./contrib/client-seeker/"
    exit 1
fi

# Create output directories
mkdir -p "$OUTPUT_DIR/server" "$OUTPUT_DIR/seeker"

log_info "═══════════════════════════════════════════════════════════════"
log_info "Profile Capture Configuration"
log_info "═══════════════════════════════════════════════════════════════"
log_info "  Bitrate:      ${BITRATE_MBPS} Mb/s (${BITRATE_BPS} bps)"
log_info "  Duration:     ${DURATION_SECS} seconds"
log_info "  Profile Type: ${PROFILE_TYPE}"
log_info "  Output:       ${OUTPUT_DIR}"
log_info "═══════════════════════════════════════════════════════════════"

# Cleanup function
cleanup() {
    log_info "Cleaning up..."
    if [[ -n "${SERVER_PID:-}" ]]; then
        kill "$SERVER_PID" 2>/dev/null || true
    fi
    if [[ -n "${SEEKER_PID:-}" ]]; then
        kill "$SEEKER_PID" 2>/dev/null || true
    fi
    # Wait for profile files to be written
    sleep 2
}
trap cleanup EXIT

# Kill any existing processes
pkill -f "contrib/server/server" 2>/dev/null || true
pkill -f "contrib/client-seeker/client-seeker" 2>/dev/null || true
sleep 1

# ═══════════════════════════════════════════════════════════════════════════
# Common SRT flags for high-performance operation
# These are the same flags used in the Isolation-300M tests
# ═══════════════════════════════════════════════════════════════════════════
COMMON_FLAGS=(
    -conntimeo 5000
    -peeridletimeo 30000
    -latency 5000
    -rcvlatency 5000
    -peerlatency 5000
    -fc 102400
    -rcvbuf 67108864
    -sndbuf 67108864
    -tlpktdrop
    -packetreorderalgorithm btree
    -btreedegree 32
    # io_uring receive
    -iouringenabled
    -iouringrecvenabled
    -iouringrecvringsize 16384
    -iouringrecvbatchsize 1024
    -iouringrecvringcount 2
    # NAK handling
    -usenakbtree
    -fastnakenabled
    -fastnakrecentenabled
    -honornakorder
    -nakrecentpercent 0.10
    # Packet ring
    -usepacketring
    -packetringsize 16384
    -packetringshards 8
    -packetringmaxretries 100
    -packetringbackoffduration 50µs
    # Receiver EventLoop
    -useeventloop
    -backoffcoldstartpkts 1000
    -backoffminsleep 10µs
    -backoffmaxsleep 1ms
    -userecvcontrolring
    -recvcontrolringsize 2048
    -recvcontrolringshards 2
    # Sender EventLoop
    -usesendbtree
    -sendbtreesize 32
    -usesendring
    -sendringsize 8192
    -sendringshards 4
    -usesendcontrolring
    -sendcontrolringsize 4096
    -sendcontrolringshards 4
    -usesendeventloop
    -sendeventloopbackoffminsleep 100µs
    -sendeventloopbackoffmaxsleep 1ms
    -sendtsbpdsleepfactor 0.90
)

# ═══════════════════════════════════════════════════════════════════════════
# Start Server
# ═══════════════════════════════════════════════════════════════════════════
log_info "Starting server with ${PROFILE_TYPE} profiling..."
"$SERVER_BIN" \
    -addr 127.0.0.1:6000 \
    -profile "$PROFILE_TYPE" \
    -profilepath "$OUTPUT_DIR/server" \
    "${COMMON_FLAGS[@]}" \
    > "$OUTPUT_DIR/server.log" 2>&1 &
SERVER_PID=$!
log_success "Server started (PID: $SERVER_PID)"
sleep 2

# Verify server is running
if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    log_error "Server failed to start. Check $OUTPUT_DIR/server.log"
    cat "$OUTPUT_DIR/server.log"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════════════
# Start Client-Seeker
# ═══════════════════════════════════════════════════════════════════════════
log_info "Starting client-seeker at ${BITRATE_MBPS} Mb/s with ${PROFILE_TYPE} profiling..."
"$SEEKER_BIN" \
    -target "srt://127.0.0.1:6000/profile-test" \
    -initial "$BITRATE_BPS" \
    -profile "$PROFILE_TYPE" \
    -profilepath "$OUTPUT_DIR/seeker" \
    -watchdog=false \
    "${COMMON_FLAGS[@]}" \
    > "$OUTPUT_DIR/seeker.log" 2>&1 &
SEEKER_PID=$!
log_success "Seeker started (PID: $SEEKER_PID)"
sleep 3

# Verify seeker is running
if ! kill -0 "$SEEKER_PID" 2>/dev/null; then
    log_error "Seeker failed to start. Check $OUTPUT_DIR/seeker.log"
    cat "$OUTPUT_DIR/seeker.log"
    exit 1
fi

# ═══════════════════════════════════════════════════════════════════════════
# Wait for profiling duration
# ═══════════════════════════════════════════════════════════════════════════
log_info "Collecting profile for ${DURATION_SECS} seconds..."
for ((i=0; i<DURATION_SECS; i+=10)); do
    remaining=$((DURATION_SECS - i))
    if [[ $remaining -gt 0 ]]; then
        echo -ne "\r  Progress: ${i}/${DURATION_SECS}s (${remaining}s remaining)    "
        sleep $((remaining < 10 ? remaining : 10))
    fi
done
echo -e "\r  Progress: ${DURATION_SECS}/${DURATION_SECS}s (done)          "

# ═══════════════════════════════════════════════════════════════════════════
# Stop processes (triggers profile write)
# ═══════════════════════════════════════════════════════════════════════════
log_info "Stopping processes to write profiles..."
kill "$SEEKER_PID" 2>/dev/null || true
kill "$SERVER_PID" 2>/dev/null || true
sleep 3

# ═══════════════════════════════════════════════════════════════════════════
# Analyze profiles
# ═══════════════════════════════════════════════════════════════════════════
log_info "═══════════════════════════════════════════════════════════════"
log_info "Profile Analysis"
log_info "═══════════════════════════════════════════════════════════════"

for component in server seeker; do
    profile_file="$OUTPUT_DIR/$component/${PROFILE_TYPE}.pprof"
    if [[ -f "$profile_file" && -s "$profile_file" ]]; then
        log_success "$component profile: $profile_file"
        echo ""
        echo "=== Top 15 functions in $component ==="
        go tool pprof -top -nodecount=15 "$profile_file" 2>/dev/null || log_warn "Could not analyze $component profile"
        echo ""
    else
        log_warn "$component profile not found or empty: $profile_file"
    fi
done

# ═══════════════════════════════════════════════════════════════════════════
# Summary
# ═══════════════════════════════════════════════════════════════════════════
log_info "═══════════════════════════════════════════════════════════════"
log_info "Summary"
log_info "═══════════════════════════════════════════════════════════════"
log_info "  Output directory: $OUTPUT_DIR"
log_info "  Server log:       $OUTPUT_DIR/server.log"
log_info "  Seeker log:       $OUTPUT_DIR/seeker.log"
log_info ""
log_info "To analyze interactively:"
log_info "  go tool pprof -http=:8080 $OUTPUT_DIR/server/${PROFILE_TYPE}.pprof"
log_info "  go tool pprof -http=:8081 $OUTPUT_DIR/seeker/${PROFILE_TYPE}.pprof"
log_info ""
log_info "To generate flame graphs:"
log_info "  go tool pprof -svg $OUTPUT_DIR/server/${PROFILE_TYPE}.pprof > server_flame.svg"
log_info "  go tool pprof -svg $OUTPUT_DIR/seeker/${PROFILE_TYPE}.pprof > seeker_flame.svg"
