# nix/grafana/dashboards/analysis.nix
#
# Analysis dashboard - deep dive into SRT protocol behavior.
# Shows heatmaps, congestion control details, and btree performance.
#
# Reference: documentation/nix_microvm_design.md lines 3017-3196
#
{ lib }:

let
  panels = import ../panels/default.nix { inherit lib; };
  grafanaLib = panels.grafanaLib;
  inherit (grafanaLib) mkDashboard mkRow;

in mkDashboard {
  title = "GoSRT Analysis";
  uid = "gosrt-analysis";
  tags = [ "gosrt" "srt" "analysis" ];
  refresh = "5s";
  timeFrom = "now-30m";  # Longer default time range for analysis

  panels = [
    # ═══════════════════════════════════════════════════════════════════════════
    # Row 1: Heatmaps (NAK btree performance visualization)
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Burst Analysis (Heatmaps)"; })

    panels.heatmaps.nakBurstHeatmap
    panels.heatmaps.retransBurstHeatmap

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 2: More Heatmaps
    # ═══════════════════════════════════════════════════════════════════════════
    panels.heatmaps.lossHeatmap
    panels.heatmaps.rttHeatmap

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 3: Congestion Control Detail
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Congestion Control"; })

    panels.congestion.nakRate
    panels.congestion.ackRate
    panels.congestion.retransRate

    panels.congestion.sendBufferLevel
    panels.congestion.recvBufferLevel
    panels.congestion.flightSize

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 4: Activity & Loss
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Activity & Loss"; })

    panels.congestion.eventloopIterations
    panels.congestion.packetsLost

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 5: Anomalies
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Anomalies (should be near zero)"; })

    panels.congestion.belatedPackets
    panels.congestion.droppedPackets

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 6: Performance Overview (for context)
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "Performance Context";
      collapsed = true;
      panels = [
        panels.overview.throughput
        panels.overview.retransRate
        panels.overview.rtt
        panels.overview.deliveryEfficiency
      ];
    })
  ];
}
