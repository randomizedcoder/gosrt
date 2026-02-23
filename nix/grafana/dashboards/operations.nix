# nix/grafana/dashboards/operations.nix
#
# Operations dashboard - "at a glance" view for operators.
# Shows health traffic lights, throughput, and key metrics.
#
# Reference: documentation/nix_microvm_design.md lines 3017-3196
#
{ lib }:

let
  panels = import ../panels/default.nix { inherit lib; };
  grafanaLib = panels.grafanaLib;
  inherit (grafanaLib) mkDashboard mkRow;

in mkDashboard {
  title = "GoSRT Operations";
  uid = "gosrt-ops";
  tags = [ "gosrt" "srt" "operations" ];
  refresh = "5s";

  panels = [
    # ═══════════════════════════════════════════════════════════════════════════
    # Row 1: Health Traffic Lights
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Stream Health"; })

    panels.health.ingestHealth
    panels.health.egressHealth
    panels.health.publisherHealth
    panels.health.serverHealth
    panels.health.connectionState
    panels.health.bufferMargin

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 2: Key Performance Indicators
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Performance"; })

    panels.overview.throughput
    panels.overview.retransRate
    panels.overview.rtt

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 3: Efficiency Metrics
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow { title = "Efficiency"; })

    panels.overview.deliveryEfficiency
    panels.overview.recoveryEfficiency
    panels.overview.bandwidthUtilization
    panels.overview.packetsPerSecond

    # ═══════════════════════════════════════════════════════════════════════════
    # Row 4: Congestion Control (collapsed by default)
    # ═══════════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "Congestion Control";
      collapsed = true;
      panels = [
        panels.congestion.nakRate
        panels.congestion.ackRate
        panels.congestion.retransRate
        panels.congestion.sendBufferLevel
        panels.congestion.recvBufferLevel
        panels.congestion.flightSize
      ];
    })
  ];
}
