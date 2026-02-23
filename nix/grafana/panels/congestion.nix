# nix/grafana/panels/congestion.nix
#
# Congestion control panels - NAK, ACK, retransmission, buffer levels.
# For deep analysis of SRT protocol behavior.
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkRatePanel mkTarget thresholds;
  instances = [ "publisher" "server" "subscriber" ];

in {
  # NAK rate
  nakRate = mkRatePanel {
    title = "NAK Rate";
    description = "Negative acknowledgements requested per second - indicates loss detection";
    metric = "gosrt_connection_nak_packets_requested_total";
    unit = "pps";
    gridPos = { h = 6; w = 8; x = 0; };
  };

  # ACK rate
  ackRate = mkRatePanel {
    title = "ACK Rate";
    description = "ACKs processed via control ring per second";
    metric = "gosrt_send_control_ring_pushed_ack_total";
    unit = "pps";
    gridPos = { h = 6; w = 8; x = 8; };
  };

  # Retransmission rate
  retransRate = mkRatePanel {
    title = "Retransmission Rate";
    description = "Packets retransmitted per second";
    metric = "gosrt_connection_congestion_retransmissions_total";
    unit = "pps";
    gridPos = { h = 6; w = 8; x = 16; };
  };

  # Send buffer level (btree size)
  sendBufferLevel = mkTimeseries {
    title = "Send Buffer Level";
    description = "Packets in send btree";
    unit = "short";
    gridPos = { h = 6; w = 8; x = 0; };
    targets = lib.imap0 (i: inst: (mkTarget
      "gosrt_send_btree_len{instance=\"${inst}\"}"
      "{{instance}} {{socket_id}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) instances;
  };

  # Receive buffer level (congestion buffer)
  recvBufferLevel = mkTimeseries {
    title = "Receive Buffer Level";
    description = "Packets in congestion buffer";
    unit = "short";
    gridPos = { h = 6; w = 8; x = 8; };
    targets = lib.imap0 (i: inst: (mkTarget
      "gosrt_connection_congestion_buffer_packets{instance=\"${inst}\"}"
      "{{instance}} {{direction}} {{socket_id}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) instances;
  };

  # Flight size (ring backlog as proxy)
  flightSize = mkTimeseries {
    title = "Flight Size";
    description = "Packets in ring backlog (in-flight proxy)";
    unit = "short";
    gridPos = { h = 6; w = 8; x = 16; };
    targets = lib.imap0 (i: inst: (mkTarget
      "gosrt_ring_backlog_packets{instance=\"${inst}\"}"
      "{{instance}} {{socket_id}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) instances;
  };

  # EventLoop iterations (proxy for processing activity)
  eventloopIterations = mkTimeseries {
    title = "EventLoop Activity";
    description = "EventLoop iterations per second";
    unit = "ops";
    gridPos = { h = 6; w = 12; x = 0; };
    targets = lib.imap0 (i: inst: (mkTarget
      "rate(gosrt_eventloop_iterations_total{instance=\"${inst}\"}[5s])"
      "{{instance}}"
    ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) instances;
  };

  # Packets lost - dual axis: cumulative (left) + rate (right)
  packetsLost = {
    title = "Packets Lost";
    description = "Cumulative packets lost (left axis) and loss rate per minute (right axis)";
    type = "timeseries";
    gridPos = { h = 6; w = 12; x = 12; };
    targets =
      # Cumulative counts (left axis) - refIds A, B, C
      (lib.imap0 (i: inst: (mkTarget
        "gosrt_connection_congestion_packets_lost_total{instance=\"${inst}\"}"
        "{{instance}} {{direction}} total"
      ) // { refId = lib.elemAt [ "A" "B" "C" ] i; }) instances)
      ++
      # Rate per minute (right axis) - refIds D, E, F
      (lib.imap0 (i: inst: (mkTarget
        "rate(gosrt_connection_congestion_packets_lost_total{instance=\"${inst}\"}[1m]) * 60"
        "{{instance}} {{direction}} /min"
      ) // { refId = lib.elemAt [ "D" "E" "F" ] i; }) instances);
    fieldConfig = {
      defaults = {
        unit = "short";
        custom = {
          axisPlacement = "left";
        };
      };
      overrides = [
        # Put rate series (D, E, F) on right axis
        {
          matcher = { id = "byFrameRefID"; options = "D"; };
          properties = [
            { id = "custom.axisPlacement"; value = "right"; }
            { id = "unit"; value = "pps"; }
            { id = "custom.lineStyle"; value = { fill = "dash"; dash = [ 10 10 ]; }; }
          ];
        }
        {
          matcher = { id = "byFrameRefID"; options = "E"; };
          properties = [
            { id = "custom.axisPlacement"; value = "right"; }
            { id = "unit"; value = "pps"; }
            { id = "custom.lineStyle"; value = { fill = "dash"; dash = [ 10 10 ]; }; }
          ];
        }
        {
          matcher = { id = "byFrameRefID"; options = "F"; };
          properties = [
            { id = "custom.axisPlacement"; value = "right"; }
            { id = "unit"; value = "pps"; }
            { id = "custom.lineStyle"; value = { fill = "dash"; dash = [ 10 10 ]; }; }
          ];
        }
      ];
    };
  };

  # Belated packets
  belatedPackets = mkRatePanel {
    title = "Belated Packets";
    description = "Packets that arrived late - should be near zero";
    metric = "gosrt_connection_congestion_packets_belated_total";
    unit = "pps";
    gridPos = { h = 6; w = 8; x = 0; };
  };

  # Dropped packets
  droppedPackets = mkRatePanel {
    title = "Dropped Packets";
    description = "Packets dropped due to congestion";
    metric = "gosrt_connection_congestion_packets_drop_total";
    unit = "pps";
    gridPos = { h = 6; w = 8; x = 8; };
  };
}
