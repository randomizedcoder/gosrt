#!/bin/bash
# run_profile.sh - Capture profiles at a specific bitrate
#
# Usage: ./run_profile.sh <bitrate_mbps> <profile_type> [duration_seconds]
#
# Examples:
#   ./run_profile.sh 300 cpu 30       # CPU profile at 300 Mb/s for 30s
#   ./run_profile.sh 370 all 30       # All profiles at 370 Mb/s for 30s
#   ./run_profile.sh 400 cpu,heap 20  # CPU + heap at 400 Mb/s for 20s

set -e

BITRATE_MBPS="${1:-300}"
PROFILE_TYPE="${2:-cpu}"
DURATION="${3:-30}"

# Convert to bps
BITRATE_BPS=$((BITRATE_MBPS * 1000000))

# Create output directory
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
OUTPUT_DIR="/tmp/srt_profiles/stage_${BITRATE_MBPS}M_${TIMESTAMP}"
mkdir -p "${OUTPUT_DIR}/server"
mkdir -p "${OUTPUT_DIR}/seeker"

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  SRT Library Profiling Run                                   ║"
echo "╠══════════════════════════════════════════════════════════════╣"
echo "║  Bitrate: ${BITRATE_MBPS} Mb/s"
echo "║  Profile: ${PROFILE_TYPE}"
echo "║  Duration: ${DURATION}s"
echo "║  Output: ${OUTPUT_DIR}"
echo "╚══════════════════════════════════════════════════════════════╝"
echo

# Build binaries
echo "Building binaries..."
cd "$(dirname "$0")/../../.."
go build -o ./contrib/server/server ./contrib/server/
go build -o ./contrib/client-seeker/client-seeker ./contrib/client-seeker/
echo "✓ Binaries built"
echo

# Standard SRT flags for high-throughput testing
SRT_FLAGS=(
    -conntimeo 3000
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
    -iouringenabled
    -iouringrecvenabled
    -iouringrecvringsize 16384
    -iouringrecvbatchsize 1024
    -iouringrecvringcount 2
    -usenakbtree
    -fastnakenabled
    -fastnakrecentenabled
    -honornakorder
    -nakrecentpercent 0.10
    -usepacketring
    -packetringsize 16384
    -packetringshards 8
    -packetringmaxretries 100
    -packetringbackoffduration 50µs
    -useeventloop
    -eventlooprateinterval 1s
    -backoffcoldstartpkts 1000
    -backoffminsleep 10µs
    -backoffmaxsleep 1ms
    -usesendbtree
    -sendbtreesize 32
    -usesendring
    -sendringsize 8192
    -sendringshards 4
    -usesendcontrolring
    -sendcontrolringsize 2048
    -sendcontrolringshards 2
    -usesendeventloop
    -sendeventloopbackoffminsleep 100µs
    -sendeventloopbackoffmaxsleep 1ms
    -sendtsbpdsleepfactor 0.90
    -userecvcontrolring
    -recvcontrolringsize 1024
    -recvcontrolringshards 1
)

# Determine profile flag
if [[ "${PROFILE_TYPE}" == "all" ]]; then
    PROFILE_FLAG="cpu"
    ALL_PROFILES=true
else
    PROFILE_FLAG="${PROFILE_TYPE%%,*}"  # Take first if comma-separated
    ALL_PROFILES=false
fi

# Start server with profiling
echo "Starting server with ${PROFILE_FLAG} profiling..."
./contrib/server/server \
    -addr 127.0.0.1:6100 \
    -profile "${PROFILE_FLAG}" \
    -profilepath "${OUTPUT_DIR}/server" \
    "${SRT_FLAGS[@]}" &
SERVER_PID=$!
sleep 1

# Start seeker with profiling
echo "Starting client-seeker at ${BITRATE_MBPS} Mb/s..."
./contrib/client-seeker/client-seeker \
    -seeker-target "srt://127.0.0.1:6100/profile-test" \
    -initial-bitrate ${BITRATE_BPS} \
    -refill-mode sleep \
    -watchdog=false \
    -profile "${PROFILE_FLAG}" \
    -profilepath "${OUTPUT_DIR}/seeker" \
    "${SRT_FLAGS[@]}" &
SEEKER_PID=$!
sleep 2

# Wait for connection
echo "Waiting for connection..."
sleep 3

echo
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  Profiling in progress... (${DURATION} seconds)               ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo

# Show progress
for ((i=0; i<DURATION; i+=5)); do
    sleep 5
    remaining=$((DURATION - i - 5))
    if [[ $remaining -gt 0 ]]; then
        echo "  [$(date +%H:%M:%S)] ${remaining}s remaining..."
    fi
done

echo
echo "Stopping processes..."

# Send SIGINT for graceful shutdown (triggers profile dump)
kill -INT ${SEEKER_PID} 2>/dev/null || true
sleep 2
kill -INT ${SERVER_PID} 2>/dev/null || true
sleep 2

# Force kill if still running
kill -9 ${SEEKER_PID} 2>/dev/null || true
kill -9 ${SERVER_PID} 2>/dev/null || true

echo "✓ Processes stopped"
echo

# List captured profiles
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  Captured Profiles                                           ║"
echo "╠══════════════════════════════════════════════════════════════╣"
find "${OUTPUT_DIR}" -name "*.pprof" -o -name "*.prof" | while read -r f; do
    size=$(du -h "$f" | cut -f1)
    echo "║  ${size}  $(basename "$f")"
done
echo "╚══════════════════════════════════════════════════════════════╝"
echo

# Quick analysis
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║  Quick Analysis                                              ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo

for profile in $(find "${OUTPUT_DIR}" -name "cpu.pprof" -o -name "cpu.prof" 2>/dev/null); do
    component=$(basename $(dirname "$profile"))
    echo "=== ${component} CPU Profile (Top 15) ==="
    go tool pprof -top -nodecount=15 "$profile" 2>/dev/null || true
    echo
done

echo "═══════════════════════════════════════════════════════════════"
echo "  Full Analysis Commands:"
echo "═══════════════════════════════════════════════════════════════"
echo
echo "  # Interactive analysis"
echo "  go tool pprof ${OUTPUT_DIR}/server/cpu.pprof"
echo
echo "  # Generate flame graph"
echo "  go tool pprof -svg ${OUTPUT_DIR}/server/cpu.pprof > server_cpu.svg"
echo
echo "  # Compare with another run"
echo "  go tool pprof -diff_base=<baseline.pprof> ${OUTPUT_DIR}/server/cpu.pprof"
echo
echo "  # Web UI"
echo "  go tool pprof -http=:8080 ${OUTPUT_DIR}/server/cpu.pprof"
echo
echo "  Output directory: ${OUTPUT_DIR}"
