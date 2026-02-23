# nix/grafana/panels/health.nix
#
# Traffic light health indicators - at-a-glance stream health.
# GREEN = good, YELLOW = warning, RED = problem.
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkHealthStat mkStat mkTarget thresholds;

in {
  # Ingest health (publisher -> server)
  ingestHealth = mkHealthStat {
    title = "Ingest Health";
    instance = "server";
    direction = "recv";
    gridPos = { h = 4; w = 4; x = 0; };
  };

  # Egress health (server -> subscriber)
  egressHealth = mkHealthStat {
    title = "Egress Health";
    instance = "subscriber";
    direction = "recv";
    gridPos = { h = 4; w = 4; x = 4; };
  };

  # Publisher send health
  publisherHealth = mkHealthStat {
    title = "Publisher";
    instance = "publisher";
    direction = "send";
    gridPos = { h = 4; w = 4; x = 8; };
  };

  # Server relay health (combined)
  serverHealth = mkStat {
    title = "Server Relay";
    description = "Server combined send/receive loss";
    unit = "percent";
    thresholds = thresholds.lossPercent;
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 12; };
    targets = [
      (mkTarget ''
        50 * (
          rate(gosrt_connection_congestion_packets_lost_total{instance="server", direction="recv"}[30s]) /
          (rate(gosrt_connection_congestion_packets_total{instance="server", direction="recv"}[30s]) + 0.001) +
          rate(gosrt_connection_congestion_packets_lost_total{instance="server", direction="send"}[30s]) /
          (rate(gosrt_connection_congestion_packets_total{instance="server", direction="send"}[30s]) + 0.001)
        )
      '' "Loss %")
    ];
  };

  # Connection state indicator
  connectionState = mkStat {
    title = "Connections";
    description = "Number of active SRT connections";
    unit = "short";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "green"; value = 1; }
      ];
    };
    colorMode = "value";
    gridPos = { h = 4; w = 4; x = 16; };
    targets = [
      (mkTarget "sum(gosrt_connections_active)" "Connections")
    ];
  };

  # Buffer margin indicator
  bufferMargin = mkStat {
    title = "Buffer Margin";
    description = "Congestion buffer packet count";
    unit = "short";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "yellow"; value = 1000; }
        { color = "red"; value = 5000; }
      ];
    };
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 20; };
    targets = [
      (mkTarget "sum(gosrt_connection_congestion_buffer_packets{instance=\"server\"})" "Buffer Pkts")
    ];
  };
}
