# nix/grafana/panels/network.nix
#
# Network throughput panels using node_exporter metrics.
# Shows actual network interface data rates for all VMs,
# compared with GoSRT application-reported rates.
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkTarget;

  # All VMs we want to monitor
  allVMs = [ "publisher" "server" "subscriber" "xtransmit-pub" "xtransmit-sub" "ffmpeg-pub" "ffmpeg-sub" ];
  gosrtVMs = [ "publisher" "server" "subscriber" ];

in {
  # ═══════════════════════════════════════════════════════════════════════════
  # Network Interface Throughput (node_exporter)
  # ═══════════════════════════════════════════════════════════════════════════

  # TX throughput from node_exporter (actual bytes sent)
  networkTxThroughput = mkTimeseries {
    title = "Network TX (Mbps) - All VMs";
    description = "Actual network interface transmit rate from node_exporter";
    unit = "Mbits";
    gridPos = { h = 8; w = 12; x = 0; };
    targets = lib.imap0 (i: vm: (mkTarget
      "rate(node_network_transmit_bytes_total{instance=\"${vm}\", device=\"eth0\"}[5s]) * 8 / 1000000"
      "{{instance}} TX"
    ) // { refId = lib.elemAt [ "A" "B" "C" "D" "E" "F" "G" "H" ] i; }) allVMs;
  };

  # RX throughput from node_exporter (actual bytes received)
  networkRxThroughput = mkTimeseries {
    title = "Network RX (Mbps) - All VMs";
    description = "Actual network interface receive rate from node_exporter";
    unit = "Mbits";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = lib.imap0 (i: vm: (mkTarget
      "rate(node_network_receive_bytes_total{instance=\"${vm}\", device=\"eth0\"}[5s]) * 8 / 1000000"
      "{{instance}} RX"
    ) // { refId = lib.elemAt [ "A" "B" "C" "D" "E" "F" "G" "H" ] i; }) allVMs;
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # GoSRT Application Throughput (gosrt metrics on port 9101)
  # ═══════════════════════════════════════════════════════════════════════════

  # GoSRT send bandwidth (application-reported)
  gosrtSendBandwidth = mkTimeseries {
    title = "GoSRT Send Bandwidth (Mbps)";
    description = "Application-reported send bandwidth from GoSRT metrics";
    unit = "Mbits";
    gridPos = { h = 8; w = 12; x = 0; };
    targets = lib.imap0 (i: vm: (mkTarget
      "gosrt_send_rate_sent_bandwidth_bps{instance=\"${vm}\"} / 1000000"
      "{{instance}} {{socket_id}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) gosrtVMs;
  };

  # GoSRT receive rate (application-reported)
  gosrtRecvRate = mkTimeseries {
    title = "GoSRT Receive Rate (Mbps)";
    description = "Application-reported receive rate from GoSRT metrics";
    unit = "Mbits";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = lib.imap0 (i: vm: (mkTarget
      "gosrt_recv_rate_bytes_per_sec{instance=\"${vm}\"} * 8 / 1000000"
      "{{instance}} {{socket_id}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) gosrtVMs;
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Comparison View - node_exporter vs GoSRT
  # ═══════════════════════════════════════════════════════════════════════════

  # Combined view: Network TX vs GoSRT Send for publishers
  publisherThroughputComparison = mkTimeseries {
    title = "Publisher: Network vs App (Mbps)";
    description = "Compare node_exporter network TX with GoSRT send rate";
    unit = "Mbits";
    gridPos = { h = 8; w = 12; x = 0; };
    targets = [
      ((mkTarget "rate(node_network_transmit_bytes_total{instance=\"publisher\", device=\"eth0\"}[5s]) * 8 / 1000000" "Network TX") // { refId = "A"; })
      ((mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"publisher\"} / 1000000" "GoSRT Send {{socket_id}}") // { refId = "B"; })
    ];
  };

  # Combined view: Network RX vs GoSRT Recv for subscribers
  subscriberThroughputComparison = mkTimeseries {
    title = "Subscriber: Network vs App (Mbps)";
    description = "Compare node_exporter network RX with GoSRT receive rate";
    unit = "Mbits";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = [
      ((mkTarget "rate(node_network_receive_bytes_total{instance=\"subscriber\", device=\"eth0\"}[5s]) * 8 / 1000000" "Network RX") // { refId = "A"; })
      ((mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"subscriber\"} * 8 / 1000000" "GoSRT Recv {{socket_id}}") // { refId = "B"; })
    ];
  };

  # Server bidirectional throughput
  serverThroughputComparison = mkTimeseries {
    title = "Server: Network vs App (Mbps)";
    description = "Server network and GoSRT bidirectional throughput";
    unit = "Mbits";
    gridPos = { h = 8; w = 24; x = 0; };
    targets = [
      ((mkTarget "rate(node_network_receive_bytes_total{instance=\"server\", device=\"eth0\"}[5s]) * 8 / 1000000" "Network RX") // { refId = "A"; })
      ((mkTarget "rate(node_network_transmit_bytes_total{instance=\"server\", device=\"eth0\"}[5s]) * 8 / 1000000" "Network TX") // { refId = "B"; })
      ((mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"server\"} * 8 / 1000000" "GoSRT Recv {{socket_id}}") // { refId = "C"; })
      ((mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"server\"} / 1000000" "GoSRT Send {{socket_id}}") // { refId = "D"; })
    ];
  };
}
