# nix/grafana/dashboards/network.nix
#
# Network throughput dashboard.
# Shows data rates from all VMs using node_exporter,
# with GoSRT application metrics for comparison.
#
{ lib }:

let
  panels = import ../panels { inherit lib; };

  # Dashboard metadata
  meta = {
    title = "SRT Network Throughput";
    uid = "srt-network";
    description = "Network interface and application throughput for all VMs";
    tags = [ "gosrt" "network" "throughput" ];
  };

  # Layout helper
  row = title: { type = "row"; title = title; gridPos = { h = 1; w = 24; x = 0; }; collapsed = false; panels = []; };

in {
  inherit meta;

  # Dashboard panels in order
  panels = [
    # Row 1: Network interface throughput (all VMs)
    (row "Network Interface Throughput (node_exporter)")
    panels.network.networkTxThroughput
    panels.network.networkRxThroughput

    # Row 2: GoSRT application throughput
    (row "GoSRT Application Throughput")
    panels.network.gosrtSendBandwidth
    panels.network.gosrtRecvRate

    # Row 3: Comparison views
    (row "Network vs Application Comparison")
    panels.network.publisherThroughputComparison
    panels.network.subscriberThroughputComparison
    panels.network.serverThroughputComparison
  ];
}
