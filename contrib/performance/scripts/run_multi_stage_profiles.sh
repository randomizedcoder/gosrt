#!/bin/bash
# run_multi_stage_profiles.sh - Run all 3 stages of profiling
#
# Usage: ./run_multi_stage_profiles.sh [duration_per_stage]
#
# This script captures CPU profiles at 3 bitrate levels:
#   - Stage 1: 300 Mb/s (baseline - healthy operation)
#   - Stage 2: 370 Mb/s (near-ceiling - starting to struggle)
#   - Stage 3: 400 Mb/s (overload - failure mode)

set -e

DURATION="${1:-30}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BASE_OUTPUT="/tmp/srt_profiles/multi_stage_${TIMESTAMP}"

echo "╔══════════════════════════════════════════════════════════════════════════════╗"
echo "║  Multi-Stage SRT Library Profiling                                           ║"
echo "╠══════════════════════════════════════════════════════════════════════════════╣"
echo "║  Stage 1: 300 Mb/s (baseline)                                                ║"
echo "║  Stage 2: 370 Mb/s (near-ceiling)                                            ║"
echo "║  Stage 3: 400 Mb/s (overload)                                                ║"
echo "║                                                                              ║"
echo "║  Duration per stage: ${DURATION}s                                             ║"
echo "║  Output: ${BASE_OUTPUT}"
echo "╚══════════════════════════════════════════════════════════════════════════════╝"
echo

mkdir -p "${BASE_OUTPUT}"

# Build binaries once
echo "Building binaries..."
cd "$(dirname "$0")/../../.."
go build -o ./contrib/server/server ./contrib/server/
go build -o ./contrib/client-seeker/client-seeker ./contrib/client-seeker/
echo "✓ Binaries built"
echo

# Standard SRT flags
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

# Function to run a single stage
run_stage() {
    local stage=$1
    local bitrate_mbps=$2
    local label=$3
    local output_dir="${BASE_OUTPUT}/stage${stage}_${bitrate_mbps}M"
    local bitrate_bps=$((bitrate_mbps * 1000000))
    local port=$((6100 + stage))
    
    mkdir -p "${output_dir}/server"
    mkdir -p "${output_dir}/seeker"
    
    echo
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║  Stage ${stage}: ${bitrate_mbps} Mb/s (${label})"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo
    
    # Start server
    echo "  Starting server on port ${port}..."
    ./contrib/server/server \
        -addr "127.0.0.1:${port}" \
        -profile cpu \
        -profilepath "${output_dir}/server" \
        "${SRT_FLAGS[@]}" &
    local server_pid=$!
    sleep 1
    
    # Start seeker
    echo "  Starting seeker at ${bitrate_mbps} Mb/s..."
    ./contrib/client-seeker/client-seeker \
        -seeker-target "srt://127.0.0.1:${port}/stage${stage}" \
        -initial-bitrate ${bitrate_bps} \
        -refill-mode sleep \
        -watchdog=false \
        -profile cpu \
        -profilepath "${output_dir}/seeker" \
        "${SRT_FLAGS[@]}" &
    local seeker_pid=$!
    sleep 3
    
    # Profile for duration
    echo "  Profiling for ${DURATION}s..."
    sleep ${DURATION}
    
    # Graceful shutdown
    echo "  Stopping..."
    kill -INT ${seeker_pid} 2>/dev/null || true
    sleep 2
    kill -INT ${server_pid} 2>/dev/null || true
    sleep 2
    kill -9 ${seeker_pid} 2>/dev/null || true
    kill -9 ${server_pid} 2>/dev/null || true
    
    echo "  ✓ Stage ${stage} complete"
    
    # Quick analysis
    for profile in $(find "${output_dir}" -name "cpu.pprof" -o -name "cpu.prof" 2>/dev/null); do
        component=$(basename $(dirname "$profile"))
        echo
        echo "  === ${component} Top 5 ==="
        go tool pprof -top -nodecount=5 "$profile" 2>/dev/null | tail -7 || true
    done
}

# Run all stages
run_stage 1 300 "baseline"
run_stage 2 370 "near-ceiling"
run_stage 3 400 "overload"

echo
echo "╔══════════════════════════════════════════════════════════════════════════════╗"
echo "║  All Stages Complete                                                         ║"
echo "╚══════════════════════════════════════════════════════════════════════════════╝"
echo

# Generate comparative analysis
echo "Generating comparative analysis..."
echo

REPORT_FILE="${BASE_OUTPUT}/analysis_report.md"

cat > "${REPORT_FILE}" << 'EOF'
# Multi-Stage Profile Analysis Report

## Summary

This report compares CPU profiles across 3 bitrate levels to identify bottlenecks.

EOF

echo "## Profile Locations" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"
echo '```' >> "${REPORT_FILE}"
find "${BASE_OUTPUT}" -name "*.pprof" -o -name "*.prof" >> "${REPORT_FILE}" 2>/dev/null || true
echo '```' >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"

# Add top functions for each stage
for stage in 1 2 3; do
    case $stage in
        1) bitrate=300; label="Baseline" ;;
        2) bitrate=370; label="Near-Ceiling" ;;
        3) bitrate=400; label="Overload" ;;
    esac
    
    echo "## Stage ${stage}: ${bitrate} Mb/s (${label})" >> "${REPORT_FILE}"
    echo >> "${REPORT_FILE}"
    
    stage_dir="${BASE_OUTPUT}/stage${stage}_${bitrate}M"
    
    for profile in $(find "${stage_dir}" -name "cpu.pprof" -o -name "cpu.prof" 2>/dev/null); do
        component=$(basename $(dirname "$profile"))
        echo "### ${component}" >> "${REPORT_FILE}"
        echo >> "${REPORT_FILE}"
        echo '```' >> "${REPORT_FILE}"
        go tool pprof -top -nodecount=15 "$profile" 2>/dev/null >> "${REPORT_FILE}" || echo "Failed to analyze" >> "${REPORT_FILE}"
        echo '```' >> "${REPORT_FILE}"
        echo >> "${REPORT_FILE}"
    done
done

# Add comparison section
echo "## Cross-Stage Comparison" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"
echo "### Key Observations" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"
echo "Compare the following across stages:" >> "${REPORT_FILE}"
echo "1. **syscall.Syscall6 %** - Should stay high (kernel I/O)" >> "${REPORT_FILE}"
echo "2. **runtime.futex %** - May increase under load (blocking)" >> "${REPORT_FILE}"
echo "3. **deliverReadyPacketsEventLoop %** - Key bottleneck candidate" >> "${REPORT_FILE}"
echo "4. **btree.iterate %** - May increase with packet volume" >> "${REPORT_FILE}"
echo "5. **packet.Header %** - Repeated access overhead" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"

echo "## Analysis Commands" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"
echo '```bash' >> "${REPORT_FILE}"
echo "# Interactive analysis" >> "${REPORT_FILE}"
echo "go tool pprof ${BASE_OUTPUT}/stage1_300M/server/cpu.pprof" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"
echo "# Diff baseline vs near-ceiling" >> "${REPORT_FILE}"
echo "go tool pprof -diff_base=${BASE_OUTPUT}/stage1_300M/server/cpu.pprof ${BASE_OUTPUT}/stage2_370M/server/cpu.pprof" >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"
echo "# Generate flame graphs" >> "${REPORT_FILE}"
echo "go tool pprof -svg ${BASE_OUTPUT}/stage1_300M/server/cpu.pprof > stage1_server.svg" >> "${REPORT_FILE}"
echo "go tool pprof -svg ${BASE_OUTPUT}/stage2_370M/server/cpu.pprof > stage2_server.svg" >> "${REPORT_FILE}"
echo "go tool pprof -svg ${BASE_OUTPUT}/stage3_400M/server/cpu.pprof > stage3_server.svg" >> "${REPORT_FILE}"
echo '```' >> "${REPORT_FILE}"
echo >> "${REPORT_FILE}"

echo "═══════════════════════════════════════════════════════════════"
echo "  Report generated: ${REPORT_FILE}"
echo "═══════════════════════════════════════════════════════════════"
echo
echo "  Quick Commands:"
echo
echo "  # View report"
echo "  cat ${REPORT_FILE}"
echo
echo "  # Interactive analysis (Stage 1 server)"
echo "  go tool pprof ${BASE_OUTPUT}/stage1_300M/server/cpu.pprof"
echo
echo "  # Web UI (Stage 2 server)"
echo "  go tool pprof -http=:8080 ${BASE_OUTPUT}/stage2_370M/server/cpu.pprof"
echo
echo "  # Diff baseline vs near-ceiling"
echo "  go tool pprof -diff_base=${BASE_OUTPUT}/stage1_300M/server/cpu.pprof ${BASE_OUTPUT}/stage2_370M/server/cpu.pprof"
echo
