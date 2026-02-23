# nix/grafana/panels/overview.nix
#
# Overview panels: throughput, retransmission rate, RTT, efficiency.
# These are the "at a glance" panels for operations dashboards.
#
# Reference: documentation/nix_microvm_design.md lines 1567-1676
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkGauge mkTarget mkInstanceTargets thresholds;

  # Common instances to display
  instances = [ "publisher" "server" "subscriber" ];

in {
  # Throughput panel - all endpoints TX/RX
  throughput = mkTimeseries {
    title = "Throughput (Mbps)";
    description = "Data throughput across all endpoints";
    unit = "Mbits";
    gridPos = { h = 6; w = 8; x = 0; };
    targets = [
      ((mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"publisher\"} * 8 / 1000000" "{{instance}} TX {{socket_id}}") // { refId = "A"; })
      ((mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"server\"} * 8 / 1000000" "{{instance}} RX {{socket_id}}") // { refId = "B"; })
      ((mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"server\"} * 8 / 1000000" "{{instance}} TX {{socket_id}}") // { refId = "C"; })
      ((mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"subscriber\"} * 8 / 1000000" "{{instance}} RX {{socket_id}}") // { refId = "D"; })
    ];
  };

  # Retransmission rate with thresholds
  retransRate = mkTimeseries {
    title = "Retransmission Rate (%)";
    description = "Retransmission overhead - lower is better";
    unit = "percent";
    gridPos = { h = 6; w = 8; x = 8; };
    thresholds = thresholds.retransPercent;
    targets = mkInstanceTargets "gosrt_send_rate_retrans_percent" "{{instance}} {{direction}}" instances;
  };

  # RTT with thresholds
  rtt = mkTimeseries {
    title = "RTT (ms)";
    description = "Round-trip time from each endpoint's perspective";
    unit = "ms";
    gridPos = { h = 6; w = 8; x = 16; };
    thresholds = thresholds.rttMs;
    targets = lib.imap0 (i: inst: (mkTarget
      "gosrt_rtt_microseconds{instance=\"${inst}\"} / 1000"
      "{{instance}} {{socket_id}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" "D" "E" "F" "G" "H" ] i; }) instances;
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Efficiency View - Shows live efficiency so operators don't have to do math
  # ═══════════════════════════════════════════════════════════════════════════

  # Live efficiency ratio (unique packets vs total)
  deliveryEfficiency = mkGauge {
    title = "Delivery Efficiency";
    description = "Ratio of unique packets to total packets (excluding retransmits).";
    unit = "percentunit";
    min = 0;
    max = 1;
    gridPos = { h = 6; w = 6; x = 0; };
    thresholds = thresholds.efficiency;
    targets = [
      (mkTarget ''
        sum(gosrt_connection_congestion_packets_unique_total{instance="server"}) /
        (sum(gosrt_connection_congestion_packets_total{instance="server"}) + 0.001)
      '' "End-to-End")
    ];
  };

  # Retransmit ratio (how much overhead from retransmits)
  recoveryEfficiency = mkTimeseries {
    title = "Retransmit Overhead";
    description = "Retransmissions as percentage of total packets sent.";
    unit = "percentunit";
    gridPos = { h = 6; w = 6; x = 6; };
    targets = [
      (mkTarget ''
        sum(rate(gosrt_connection_congestion_retransmissions_total[30s])) /
        (sum(rate(gosrt_connection_packets_sent_total[30s])) + 0.001)
      '' "Retransmit %")
    ];
  };

  # Bandwidth (actual throughput)
  bandwidthUtilization = mkTimeseries {
    title = "Send Bandwidth";
    description = "Actual send bandwidth in Mbps.";
    unit = "Mbits";
    gridPos = { h = 6; w = 6; x = 12; };
    targets = [
      (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"publisher\"} / 1000000" "{{instance}} {{socket_id}}")
      (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"server\"} / 1000000" "{{instance}} {{socket_id}}")
    ];
  };

  # Packets per second
  packetsPerSecond = mkTimeseries {
    title = "Packets/sec";
    description = "Data packets sent and received per second";
    unit = "pps";
    gridPos = { h = 6; w = 6; x = 18; };
    targets = [
      ((mkTarget "rate(gosrt_connection_packets_sent_total{instance=\"publisher\"}[5s])" "{{instance}} TX {{socket_id}}") // { refId = "A"; })
      ((mkTarget "rate(gosrt_connection_packets_received_total{instance=\"subscriber\"}[5s])" "{{instance}} RX {{socket_id}}") // { refId = "B"; })
    ];
  };
}
