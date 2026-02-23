# nix/testing/analysis.nix
#
# Analysis tools for GoSRT test results.
# Extracts key metrics and generates reports.
#
# Reference: documentation/nix_microvm_design.md lines 4720-4805
#
{ pkgs, lib }:

{
  # ─── Extract Key Metrics ─────────────────────────────────────────────────
  # Parse Prometheus text format and extract SRT-specific metrics
  extractMetrics = pkgs.writeShellApplication {
    name = "srt-extract-metrics";
    runtimeInputs = with pkgs; [ gawk gnugrep coreutils ];
    text = ''
      FILE="''${1:-}"
      if [ -z "$FILE" ]; then
        echo "Usage: srt-extract-metrics <metrics-file>"
        exit 1
      fi

      if [ ! -f "$FILE" ]; then
        echo "ERROR: File not found: $FILE"
        exit 1
      fi

      echo ""
      echo "Key Metrics from: $FILE"
      echo "========================"
      echo ""

      echo "RTT (microseconds):"
      grep "gosrt_rtt_microseconds" "$FILE" | tail -1 || echo "  Not found"
      echo ""

      echo "Packets Sent/Received:"
      grep -E "gosrt_connection_pkt_send_total|gosrt_connection_pkt_recv_total" "$FILE" | tail -2 || echo "  Not found"
      echo ""

      echo "Packets Lost:"
      grep "gosrt_connection_congestion_packets_lost_total" "$FILE" | tail -1 || echo "  Not found"
      echo ""

      echo "Retransmissions:"
      grep "gosrt_connection_congestion_recv_pkt_retrans_total" "$FILE" | tail -1 || echo "  Not found"
      echo ""

      echo "TSBPD Skipped (unrecoverable):"
      grep "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total" "$FILE" | tail -1 || echo "  Not found"
      echo ""

      echo "NAK Packets:"
      grep -E "gosrt_connection_nak_sent_total|gosrt_connection_nak_received_total" "$FILE" | tail -2 || echo "  Not found"
      echo ""

      echo "Ring Drops:"
      grep "gosrt_receiver_ring_drops_total" "$FILE" | tail -1 || echo "  Not found"
      echo ""

      echo "Buffer Usage:"
      grep -E "gosrt_connection_send_buffer|gosrt_connection_recv_buffer" "$FILE" | tail -4 || echo "  Not found"
    '';
  };

  # ─── Generate Test Report ────────────────────────────────────────────────
  # Create a summary report from collected metrics
  generateReport = pkgs.writeShellApplication {
    name = "srt-generate-report";
    runtimeInputs = with pkgs; [ jq coreutils gnugrep gawk ];
    text = ''
      OUTPUT_DIR="''${1:-/tmp/srt-test-results}"

      if [ ! -d "$OUTPUT_DIR" ]; then
        echo "ERROR: Directory not found: $OUTPUT_DIR"
        exit 1
      fi

      echo ""
      echo "GoSRT Test Report"
      echo "================="
      echo "Results Directory: $OUTPUT_DIR"
      echo "Generated: $(date)"
      echo ""

      # Process each endpoint's final metrics
      for role in server publisher subscriber; do
        FINAL="$OUTPUT_DIR/''${role}_final.txt"
        if [ -f "$FINAL" ]; then
          echo "=== $role ==="

          # Extract key counters
          SENT=$(grep "gosrt_connection_pkt_send_total" "$FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
          RECV=$(grep "gosrt_connection_pkt_recv_total" "$FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
          LOST=$(grep "gosrt_connection_congestion_packets_lost_total" "$FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
          RETRANS=$(grep "gosrt_connection_congestion_recv_pkt_retrans_total" "$FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
          SKIPPED=$(grep "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total" "$FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
          RTT=$(grep "gosrt_rtt_microseconds" "$FINAL" 2>/dev/null | awk '{print $2}' | tail -1)

          printf "  Packets Sent:    %s\n" "''${SENT:-N/A}"
          printf "  Packets Recv:    %s\n" "''${RECV:-N/A}"
          printf "  Packets Lost:    %s\n" "''${LOST:-N/A}"
          printf "  Retransmissions: %s\n" "''${RETRANS:-N/A}"
          printf "  TSBPD Skipped:   %s\n" "''${SKIPPED:-N/A}"
          printf "  RTT (us):        %s\n" "''${RTT:-N/A}"

          # Calculate loss percentage if we have the data
          if [ -n "$RECV" ] && [ -n "$LOST" ] && [ "$RECV" != "0" ]; then
            LOSS_PCT=$(awk "BEGIN {printf \"%.4f\", ($LOST / $RECV) * 100}")
            printf "  Loss %%:          %s%%\n" "$LOSS_PCT"
          fi

          echo ""
        else
          echo "=== $role ==="
          echo "  No data collected"
          echo ""
        fi
      done

      # Summary
      echo "=== Test Summary ==="
      echo "  Baseline files: $(ls -1 "$OUTPUT_DIR"/*_baseline.txt 2>/dev/null | wc -l)"
      echo "  Metrics files:  $(ls -1 "$OUTPUT_DIR"/*_metrics.txt 2>/dev/null | wc -l)"
      echo "  Final files:    $(ls -1 "$OUTPUT_DIR"/*_final.txt 2>/dev/null | wc -l)"
    '';
  };

  # ─── Compare Two Test Runs ───────────────────────────────────────────────
  # Side-by-side comparison of two test runs
  compareRuns = pkgs.writeShellApplication {
    name = "srt-compare-runs";
    runtimeInputs = with pkgs; [ gawk gnugrep coreutils ];
    text = ''
      RUN_A="''${1:-}"
      RUN_B="''${2:-}"

      if [ -z "$RUN_A" ] || [ -z "$RUN_B" ]; then
        echo "Usage: srt-compare-runs <run-a-dir> <run-b-dir>"
        exit 1
      fi

      if [ ! -d "$RUN_A" ]; then
        echo "ERROR: Directory not found: $RUN_A"
        exit 1
      fi

      if [ ! -d "$RUN_B" ]; then
        echo "ERROR: Directory not found: $RUN_B"
        exit 1
      fi

      echo ""
      echo "GoSRT Test Comparison"
      echo "====================="
      echo "Run A: $RUN_A"
      echo "Run B: $RUN_B"
      echo ""

      # Compare each endpoint
      for role in server publisher subscriber; do
        FINAL_A="$RUN_A/''${role}_final.txt"
        FINAL_B="$RUN_B/''${role}_final.txt"

        if [ -f "$FINAL_A" ] && [ -f "$FINAL_B" ]; then
          echo "=== $role ==="

          # Extract metrics from both runs
          LOST_A=$(grep "gosrt_connection_congestion_packets_lost_total" "$FINAL_A" 2>/dev/null | awk '{print $2}' | tail -1)
          LOST_B=$(grep "gosrt_connection_congestion_packets_lost_total" "$FINAL_B" 2>/dev/null | awk '{print $2}' | tail -1)

          RTT_A=$(grep "gosrt_rtt_microseconds" "$FINAL_A" 2>/dev/null | awk '{print $2}' | tail -1)
          RTT_B=$(grep "gosrt_rtt_microseconds" "$FINAL_B" 2>/dev/null | awk '{print $2}' | tail -1)

          RETRANS_A=$(grep "gosrt_connection_congestion_recv_pkt_retrans_total" "$FINAL_A" 2>/dev/null | awk '{print $2}' | tail -1)
          RETRANS_B=$(grep "gosrt_connection_congestion_recv_pkt_retrans_total" "$FINAL_B" 2>/dev/null | awk '{print $2}' | tail -1)

          printf "  %-20s %15s %15s\n" "Metric" "Run A" "Run B"
          printf "  %-20s %15s %15s\n" "--------------------" "---------------" "---------------"
          printf "  %-20s %15s %15s\n" "Packets Lost" "''${LOST_A:-N/A}" "''${LOST_B:-N/A}"
          printf "  %-20s %15s %15s\n" "RTT (us)" "''${RTT_A:-N/A}" "''${RTT_B:-N/A}"
          printf "  %-20s %15s %15s\n" "Retransmissions" "''${RETRANS_A:-N/A}" "''${RETRANS_B:-N/A}"
          echo ""
        else
          echo "=== $role ==="
          echo "  Missing data in one or both runs"
          echo ""
        fi
      done
    '';
  };

  # ─── Pass/Fail Checker ───────────────────────────────────────────────────
  # Determine if a test passed based on metrics thresholds
  checkPass = pkgs.writeShellApplication {
    name = "srt-check-pass";
    runtimeInputs = with pkgs; [ gnugrep gawk coreutils ];
    text = ''
      OUTPUT_DIR="''${1:-/tmp/srt-test-results}"
      MAX_LOSS_PCT="''${2:-1.0}"      # Default: 1% max loss
      MAX_SKIPPED="''${3:-0}"         # Default: 0 unrecoverable packets

      if [ ! -d "$OUTPUT_DIR" ]; then
        echo "ERROR: Directory not found: $OUTPUT_DIR"
        exit 1
      fi

      PASS=true

      # Check subscriber (most important - this is the receiver)
      SUB_FINAL="$OUTPUT_DIR/subscriber_final.txt"
      if [ -f "$SUB_FINAL" ]; then
        RECV=$(grep "gosrt_connection_pkt_recv_total" "$SUB_FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
        LOST=$(grep "gosrt_connection_congestion_packets_lost_total" "$SUB_FINAL" 2>/dev/null | awk '{print $2}' | tail -1)
        SKIPPED=$(grep "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total" "$SUB_FINAL" 2>/dev/null | awk '{print $2}' | tail -1)

        # Check loss percentage
        if [ -n "$RECV" ] && [ -n "$LOST" ] && [ "$RECV" != "0" ]; then
          LOSS_PCT=$(awk "BEGIN {printf \"%.4f\", ($LOST / $RECV) * 100}")
          EXCEEDS=$(awk "BEGIN {print ($LOSS_PCT > $MAX_LOSS_PCT) ? \"yes\" : \"no\"}")
          if [ "$EXCEEDS" = "yes" ]; then
            echo "FAIL: Loss $LOSS_PCT% exceeds threshold $MAX_LOSS_PCT%"
            PASS=false
          fi
        fi

        # Check unrecoverable packets
        if [ -n "$SKIPPED" ] && [ "$SKIPPED" != "0" ]; then
          if [ "$SKIPPED" -gt "$MAX_SKIPPED" ]; then
            echo "FAIL: $SKIPPED unrecoverable packets (max: $MAX_SKIPPED)"
            PASS=false
          fi
        fi
      else
        echo "FAIL: No subscriber metrics found"
        PASS=false
      fi

      if [ "$PASS" = "true" ]; then
        echo "PASS: All thresholds met"
        exit 0
      else
        exit 1
      fi
    '';
  };
}
