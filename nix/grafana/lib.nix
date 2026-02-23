# nix/grafana/lib.nix
#
# Helper functions to generate Grafana dashboards from Nix attribute sets.
# Uses builtins.toJSON for conversion - no hand-crafted JSON strings!
#
# Key benefits:
#   - Type checking at Nix evaluation time
#   - No JSON syntax errors possible
#   - Automatic deduplication via Nix's lazy evaluation
#   - Composable panel definitions
#
# Reference: documentation/nix_microvm_design.md lines 1277-1564
# Implementation: documentation/nix_microvm_implementation_plan.md Step 2.2
#
{ lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # ═══════════════════════════════════════════════════════════════════════════
  # Core Panel Builders
  # ═══════════════════════════════════════════════════════════════════════════

  # Base panel with common defaults
  mkPanel = {
    title,
    type,
    gridPos,
    targets ? [],
    description ? null,
    fieldConfig ? {},
    options ? {},
    ...
  }@args: {
    inherit title type gridPos targets;
  } // lib.optionalAttrs (description != null) { inherit description; }
    // lib.optionalAttrs (fieldConfig != {}) { inherit fieldConfig; }
    // lib.optionalAttrs (options != {}) { inherit options; }
    // (removeAttrs args [ "title" "type" "gridPos" "targets" "description" "fieldConfig" "options" ]);

  # Timeseries panel (most common)
  mkTimeseries = { title, targets, gridPos, unit ? "short", description ? null, thresholds ? null }:
    mkPanel {
      inherit title targets gridPos description;
      type = "timeseries";
      fieldConfig = {
        defaults = { inherit unit; }
          // lib.optionalAttrs (thresholds != null) { inherit thresholds; };
      };
    };

  # Stat panel (traffic light / single value)
  mkStat = { title, targets, gridPos, unit ? "short", thresholds, description ? null, graphMode ? "none", colorMode ? "background" }:
    mkPanel {
      inherit title targets gridPos description;
      type = "stat";
      fieldConfig.defaults = { inherit unit thresholds; };
      options = { inherit graphMode colorMode; textMode = "value_and_name"; };
    };

  # Gauge panel
  mkGauge = { title, targets, gridPos, unit ? "percentunit", min ? 0, max ? 1, thresholds, description ? null }:
    mkPanel {
      inherit title targets gridPos description;
      type = "gauge";
      fieldConfig.defaults = { inherit unit min max thresholds; };
    };

  # Heatmap panel (for NAK burst detection per Refinement 5)
  mkHeatmap = { title, targets, gridPos, description ? null }:
    mkPanel {
      inherit title targets gridPos description;
      type = "heatmap";
      options = {
        calculate = true;
        color = {
          mode = "spectrum";
          scheme = "Spectral";
        };
      };
    };

  # Row (collapsible section)
  mkRow = { title, y ? 0, collapsed ? false, panels ? [] }:
    mkPanel {
      inherit title collapsed panels;
      type = "row";
      gridPos = { h = 1; w = 24; x = 0; inherit y; };
    };

  # ═══════════════════════════════════════════════════════════════════════════
  # pprof-linked panels (Refinement 4)
  # ═══════════════════════════════════════════════════════════════════════════

  # Panel with pprof links (click to profile)
  mkTimeseriesWithPprof = { title, instance, pprofPort ? 6060, ... }@args:
    mkTimeseries (removeAttrs args [ "instance" "pprofPort" ] // {
      fieldConfig = {
        defaults = {
          unit = args.unit or "short";
          links = [
            {
              title = "CPU Profile (30s)";
              url = "http://${gosrtLib.roles.${instance}.network.vmIp}:${toString pprofPort}/debug/pprof/profile?seconds=30";
              targetBlank = true;
            }
            {
              title = "Heap Profile";
              url = "http://${gosrtLib.roles.${instance}.network.vmIp}:${toString pprofPort}/debug/pprof/heap";
              targetBlank = true;
            }
            {
              title = "Goroutine Profile";
              url = "http://${gosrtLib.roles.${instance}.network.vmIp}:${toString pprofPort}/debug/pprof/goroutine?debug=1";
              targetBlank = true;
            }
          ];
        };
      };
    });

  # ═══════════════════════════════════════════════════════════════════════════
  # Target Builders (Prometheus queries)
  # ═══════════════════════════════════════════════════════════════════════════

  # Single target with expression and legend
  mkTarget = expr: legendFormat: {
    inherit expr legendFormat;
    datasource = { type = "prometheus"; uid = "prometheus"; };
    refId = "A";  # Will be overwritten by auto-assign
  };

  # Generate targets for all instances with a pattern
  # Usage: mkInstanceTargets "gosrt_rtt_microseconds" "{{instance}} RTT" ["server" "publisher" "subscriber"]
  # Use Grafana template syntax: {{label_name}} to include metric labels in legend
  mkInstanceTargets = metric: legendPattern: instances:
    lib.imap0 (i: inst: (mkTarget
      "${metric}{instance=\"${inst}\"}"
      legendPattern
    ) // { refId = lib.elemAt [ "A" "B" "C" "D" "E" "F" "G" "H" ] i; }) instances;

  # Generate comparison targets (both ends of a flow)
  # Usage: mkFlowTargets { send = "publisher"; recv = "server"; } "congestion_packets_total"
  mkFlowTargets = { send, recv }: metric:
    [
      ((mkTarget "rate(gosrt_connection_${metric}{instance=\"${send}\", direction=\"send\"}[5s])" "${send} SEND") // { refId = "A"; })
      ((mkTarget "rate(gosrt_connection_${metric}{instance=\"${recv}\", direction=\"recv\"}[5s])" "${recv} RECV") // { refId = "B"; })
    ];

  # ═══════════════════════════════════════════════════════════════════════════
  # High-Level Panel Presets (DRY helpers for common patterns)
  # ═══════════════════════════════════════════════════════════════════════════

  # Health traffic light panel (loss percentage for a stream)
  mkHealthStat = { title, instance, direction, description ? null, gridPos }:
    mkStat {
      inherit title gridPos;
      description = if description != null then description
        else "${title}: GREEN: <1% loss, YELLOW: 1-3% loss, RED: >3% loss";
      unit = "percent";
      thresholds = thresholds.lossPercent;
      colorMode = "background";
      targets = [
        (mkTarget ''
          100 * rate(gosrt_connection_congestion_packets_lost_total{instance="${instance}", direction="${direction}"}[30s]) /
          (rate(gosrt_connection_congestion_packets_total{instance="${instance}", direction="${direction}"}[30s]) + 0.001)
        '' "Loss %")
      ];
    };

  # Throughput timeseries for an endpoint
  mkThroughputPanel = { title, instance, gridPos, showBoth ? false }:
    mkTimeseries {
      inherit title gridPos;
      description = "Data throughput in Mbps";
      unit = "Mbits";
      targets =
        if showBoth then [
          ((mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"${instance}\"} * 8 / 1000000" "${instance} TX") // { refId = "A"; })
          ((mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"${instance}\"} * 8 / 1000000" "${instance} RX") // { refId = "B"; })
        ] else [
          (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"${instance}\"} * 8 / 1000000" "${instance}")
        ];
    };

  # Counter rate panel (generic rate of any counter)
  # Uses Grafana label templates for richer legends: {{instance}} {{direction}} {{socket_id}}
  mkRatePanel = { title, metric, unit ? "pps", instances ? [ "publisher" "server" "subscriber" ], gridPos, description ? null }:
    mkTimeseries {
      inherit title gridPos unit description;
      targets = lib.imap0 (i: inst: (mkTarget
        "rate(${metric}{instance=\"${inst}\"}[5s])"
        "{{instance}} {{direction}}"
      ) // { refId = lib.elemAt [ "A" "B" "C" "D" "E" "F" "G" "H" ] i; }) instances;
    };

  # ═══════════════════════════════════════════════════════════════════════════
  # Threshold Presets
  # ═══════════════════════════════════════════════════════════════════════════

  thresholds = {
    # Loss percentage (green < yellow < red)
    lossPercent = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "yellow"; value = 1; }
        { color = "red"; value = 3; }
      ];
    };

    # Retransmission percentage
    retransPercent = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "yellow"; value = 2; }
        { color = "red"; value = 5; }
      ];
    };

    # Should be zero (anomaly counters)
    shouldBeZero = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "red"; value = 1; }
      ];
    };

    # Buffer margin (inverse - low is bad)
    bufferMargin = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 20; }
        { color = "green"; value = 50; }
      ];
    };

    # RTT thresholds (ms)
    rttMs = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "yellow"; value = 100; }
        { color = "red"; value = 200; }
      ];
    };

    # Efficiency (green is high)
    efficiency = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 0.90; }
        { color = "green"; value = 0.98; }
      ];
    };
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Dashboard Builder
  # ═══════════════════════════════════════════════════════════════════════════

  mkDashboard = {
    title,
    uid,
    panels,
    tags ? [ "gosrt" "srt" ],
    refresh ? "5s",
    timeFrom ? "now-15m",
    annotations ? true,  # Enable impairment annotations by default
    links ? []
  }: {
    inherit title uid tags refresh links;
    editable = true;
    fiscalYearStartMonth = 0;
    graphTooltip = 2;  # Shared crosshair
    id = null;
    schemaVersion = 38;
    templating = { list = []; };
    time = { from = timeFrom; to = "now"; };
    timepicker = {};
    timezone = "browser";
    version = 1;

    # Auto-add impairment annotations
    annotations = if annotations then {
      list = [{
        datasource = { type = "prometheus"; uid = "prometheus"; };
        enable = true;
        expr = "";
        iconColor = "red";
        name = "Impairments";
        tagKeys = "impairment";
        titleFormat = "{{text}}";
        type = "tags";
      }];
    } else { list = []; };

    # Auto-layout panels (calculate y positions)
    panels = autoLayoutPanels panels;
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Auto-Layout Helper
  # ═══════════════════════════════════════════════════════════════════════════

  # Automatically calculate y positions for panels
  # Rows reset y position, regular panels stack vertically
  autoLayoutPanels = panels:
    let
      processPanel = { y, id, result }: panel:
        let
          # Rows have height 1, other panels use their gridPos.h
          h = if panel.type == "row" then 1 else (panel.gridPos.h or 8);
          # Update panel with calculated y position and unique id
          newPanel = panel // {
            id = id;
            gridPos = (panel.gridPos or {}) // { y = y; };
          };
          # If it's a collapsed row with nested panels, process those too
          newPanelWithNested =
            if panel.type == "row" && (panel.panels or []) != [] then
              newPanel // {
                panels = autoLayoutPanels panel.panels;
              }
            else newPanel;
        in {
          y = y + h;
          id = id + 1;
          result = result ++ [ newPanelWithNested ];
        };
    in (lib.foldl processPanel { y = 0; id = 1; result = []; } panels).result;

in {
  inherit
    # Core panel builders
    mkPanel mkTimeseries mkStat mkGauge mkHeatmap mkRow
    # pprof-linked panels
    mkTimeseriesWithPprof
    # Target builders
    mkTarget mkInstanceTargets mkFlowTargets
    # Dashboard builders
    mkDashboard autoLayoutPanels
    # High-level presets (DRY helpers)
    mkHealthStat mkThroughputPanel mkRatePanel
    # Threshold presets
    thresholds;

  # Re-export gosrtLib for convenience
  inherit (gosrtLib) roles serverIp prometheusRoles;
}
