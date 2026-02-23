#!/usr/bin/env bash
#
# analyze_srt_pcap.bash - Extract SRT sequence numbers and NAKs from pcap files
#
# Usage: ./analyze_srt_pcap.bash <input.pcap> [port]
#
# This script analyzes SRT packet captures to identify:
# - Data packet sequence numbers and any gaps
# - NAK packets and the sequences they request
# - Control packet types
#
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <input.pcap> [port]" >&2
  echo "" >&2
  echo "Arguments:" >&2
  echo "  input.pcap  - The pcap file to analyze" >&2
  echo "  port        - UDP port to decode as SRT (default: auto-detect from 6000-6010)" >&2
  echo "" >&2
  echo "Output files (created in same directory as input):" >&2
  echo "  <input>_srt_all.csv      - All SRT packets" >&2
  echo "  <input>_data_seqs.txt    - Data packet sequence numbers only" >&2
  echo "  <input>_nak_seqs.txt     - NAK'd sequence numbers" >&2
  echo "  <input>_gaps.txt         - Detected sequence gaps" >&2
  exit 1
fi

PCAP="$1"
PORT="${2:-}"

if [[ ! -f "$PCAP" ]]; then
  echo "Error: File not found: $PCAP" >&2
  exit 1
fi

# Derive output filenames
BASENAME=$(basename "$PCAP" .pcap)
DIRNAME=$(dirname "$PCAP")
ALL_CSV="${DIRNAME}/${BASENAME}_srt_all.csv"
DATA_SEQS="${DIRNAME}/${BASENAME}_data_seqs.txt"
NAK_SEQS="${DIRNAME}/${BASENAME}_nak_seqs.txt"
GAPS_FILE="${DIRNAME}/${BASENAME}_gaps.txt"

echo "[$(date '+%F %T')] Analyzing SRT packets in: $PCAP"

# Auto-detect port if not specified
if [[ -z "$PORT" ]]; then
  echo "[$(date '+%F %T')] Auto-detecting SRT port..."
  for p in 6000 6001 6002 6003 6004 6005 6010; do
    count=$(tshark -r "$PCAP" -Y "udp.port == $p" -T fields -e frame.number 2>/dev/null | head -10 | wc -l)
    if [[ $count -gt 5 ]]; then
      PORT=$p
      echo "[$(date '+%F %T')] Detected SRT traffic on port $PORT"
      break
    fi
  done
  if [[ -z "$PORT" ]]; then
    echo "Error: Could not auto-detect SRT port. Please specify port manually." >&2
    exit 1
  fi
fi

echo "[$(date '+%F %T')] Using port: $PORT"

# Export all SRT packets to CSV
echo "[$(date '+%F %T')] Exporting all SRT packets to: $ALL_CSV"
tshark \
  -r "$PCAP" \
  -d "udp.port==$PORT,srt" \
  -Y "srt" \
  -T fields \
  -E header=y \
  -E separator=, \
  -E quote=d \
  -E occurrence=a \
  -E aggregator=';' \
  -e frame.number \
  -e frame.time_relative \
  -e ip.src \
  -e ip.dst \
  -e udp.srcport \
  -e udp.dstport \
  -e srt.iscontrol \
  -e srt.type \
  -e srt.seqno \
  -e srt.msgno \
  -e srt.timestamp \
  -e srt.ackno \
  -e srt.ack_seqno \
  -e srt.nak_seqno \
  2>/dev/null > "$ALL_CSV"

TOTAL_PKTS=$(wc -l < "$ALL_CSV")
echo "[$(date '+%F %T')] Exported $((TOTAL_PKTS - 1)) SRT packets"

# Extract data packet sequence numbers (iscontrol=False)
echo "[$(date '+%F %T')] Extracting data packet sequences to: $DATA_SEQS"
awk -F, 'NR>1 && $7=="\"False\"" && $9!="" {gsub(/"/, "", $9); print $9}' "$ALL_CSV" > "$DATA_SEQS"
DATA_COUNT=$(wc -l < "$DATA_SEQS")
echo "[$(date '+%F %T')] Found $DATA_COUNT data packets"

# Extract NAK'd sequence numbers (type=3 for NAK, field 14 is nak_seqno)
echo "[$(date '+%F %T')] Extracting NAK'd sequences to: $NAK_SEQS"
awk -F, 'NR>1 && $8=="\"3\"" && $14!="" {gsub(/"/, "", $14); gsub(/;/, "\n", $14); print $14}' "$ALL_CSV" | sort -n | uniq > "$NAK_SEQS"
NAK_COUNT=$(wc -l < "$NAK_SEQS")
echo "[$(date '+%F %T')] Found $NAK_COUNT unique NAK'd sequences"

# Find gaps in data sequence numbers
echo "[$(date '+%F %T')] Analyzing sequence gaps..."
{
  echo "# Sequence Gap Analysis"
  echo "# Format: gap_start gap_end gap_size"
  echo "#"

  prev_seq=""
  gap_count=0
  total_missing=0

  while read -r seq; do
    if [[ -n "$prev_seq" ]]; then
      expected=$((prev_seq + 1))
      # Handle wraparound at 2^31
      if [[ $seq -lt $prev_seq && $prev_seq -gt 2000000000 ]]; then
        # Likely wraparound, skip
        :
      elif [[ $seq -ne $expected ]]; then
        gap_size=$((seq - expected))
        if [[ $gap_size -gt 0 && $gap_size -lt 1000000 ]]; then
          echo "$expected $((seq - 1)) $gap_size"
          ((gap_count++)) || true
          ((total_missing += gap_size)) || true
        fi
      fi
    fi
    prev_seq=$seq
  done < "$DATA_SEQS"

  echo "#"
  echo "# Summary: $gap_count gaps, $total_missing total missing sequences"
} > "$GAPS_FILE"

GAP_SUMMARY=$(tail -1 "$GAPS_FILE")
echo "[$(date '+%F %T')] $GAP_SUMMARY"

# Print summary
echo ""
echo "=========================================="
echo "ANALYSIS SUMMARY"
echo "=========================================="
echo "Input file:     $PCAP"
echo "SRT port:       $PORT"
echo "Total packets:  $((TOTAL_PKTS - 1))"
echo "Data packets:   $DATA_COUNT"
echo "NAK'd seqs:     $NAK_COUNT"
echo ""
echo "Output files:"
echo "  All packets:  $ALL_CSV"
echo "  Data seqs:    $DATA_SEQS"
echo "  NAK seqs:     $NAK_SEQS"
echo "  Gaps:         $GAPS_FILE"
echo ""

# Show first few gaps if any
if [[ -s "$GAPS_FILE" ]]; then
  echo "First 10 sequence gaps:"
  grep -v "^#" "$GAPS_FILE" | head -10
  echo ""
fi

# Show NAK'd sequences if any
if [[ -s "$NAK_SEQS" ]]; then
  echo "NAK'd sequences (first 20):"
  head -20 "$NAK_SEQS"
  echo ""
fi

# Cross-reference: which NAK'd seqs are in gaps?
if [[ -s "$NAK_SEQS" && -s "$GAPS_FILE" ]]; then
  echo "Checking if NAK'd sequences correspond to gaps..."
  matched=0
  unmatched=0
  while read -r nak_seq; do
    found=0
    while read -r gap_line; do
      [[ "$gap_line" =~ ^# ]] && continue
      gap_start=$(echo "$gap_line" | awk '{print $1}')
      gap_end=$(echo "$gap_line" | awk '{print $2}')
      if [[ -n "$gap_start" && -n "$gap_end" ]]; then
        if [[ $nak_seq -ge $gap_start && $nak_seq -le $gap_end ]]; then
          found=1
          break
        fi
      fi
    done < "$GAPS_FILE"
    if [[ $found -eq 1 ]]; then
      ((matched++)) || true
    else
      ((unmatched++)) || true
      if [[ $unmatched -le 5 ]]; then
        echo "  NAK seq $nak_seq NOT found in any gap (phantom NAK?)"
      fi
    fi
  done < "$NAK_SEQS"
  echo ""
  echo "NAK correlation: $matched in gaps, $unmatched NOT in gaps (phantom)"
fi

echo ""
echo "[$(date '+%F %T')] Analysis complete"

