# nix/grafana/panels/heatmaps.nix
#
# NAK burst heatmaps for micro-burst detection.
# Shows whether NAK activity is smooth (O(log n) btree) or bursty (potential issues).
#
# Reference: documentation/nix_microvm_implementation_plan.md Refinement 5
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkHeatmap mkTarget;

in {
  # NAK burst heatmap
  nakBurstHeatmap = mkHeatmap {
    title = "NAK Burst Distribution";
    description = ''
      Heatmap showing NAK activity over time.
      - Vertical bands = burst NAK activity (potential O(n) scaling)
      - Smooth spread = efficient O(log n) btree operations
    '';
    gridPos = { h = 8; w = 12; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_connection_nak_packets_requested_total{instance=\"server\"}[1s])" "{{instance}} {{direction}} {{socket_id}}")
    ];
  };

  # Retransmission burst heatmap
  retransBurstHeatmap = mkHeatmap {
    title = "Retransmission Burst Distribution";
    description = "Visualize retransmission patterns - bursts indicate loss events";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_retransmissions_total[1s])" "{{instance}} {{direction}} {{socket_id}}")
    ];
  };

  # Packet loss heatmap (useful for identifying patterns)
  lossHeatmap = mkHeatmap {
    title = "Packet Loss Distribution";
    description = "Shows packet loss patterns over time - useful for identifying periodic loss";
    gridPos = { h = 8; w = 12; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_packets_lost_total[1s])" "{{instance}} {{direction}} {{socket_id}}")
    ];
  };

  # RTT distribution heatmap
  rttHeatmap = mkHeatmap {
    title = "RTT Distribution";
    description = "RTT variations over time - useful for identifying jitter patterns";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = [
      (mkTarget "gosrt_rtt_microseconds{instance=\"server\"} / 1000" "{{instance}} {{socket_id}}")
    ];
  };
}
