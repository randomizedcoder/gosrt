# nix/constants.nix
#
# Shared constants for GoSRT MicroVM infrastructure.
# Import this file in all other modules to ensure consistency.
#
# Reference: documentation/nix_microvm_design.md lines 278-521
#
# DESIGN PRINCIPLE: Declare minimal facts, compute the rest.
# All per-role values (IPs, MACs, ports) are derived from role index.
#
{
  # ─── Base Configuration (Declared) ─────────────────────────────────────────
  base = {
    subnetPrefix = "10.50";       # All VM subnets are 10.50.X.0/24
    interRouterBase = 100;        # Inter-router links start at 10.50.100.0/30
    sshPortBase = 22000;          # SSH forward ports: 22001, 22002, ...
    consolePortBase = 45000;      # Console ports: 45001, 45002, ...
    prometheusPortBase = 19000;   # Prometheus forward: 19001, 19002, ...
  };

  # ─── Role Definitions (Single Source of Truth) ─────────────────────────────
  #
  # Each role has an index (1-8) that determines all derived values:
  #   - Subnet:      10.50.<index>.0/24
  #   - VM IP:       10.50.<index>.2
  #   - Gateway:     10.50.<index>.1
  #   - MAC:         02:00:00:50:<hex(index)>:02
  #   - Console:     45000 + index
  #   - SSH Forward: 22000 + index
  #
  roles = {
    # ─── GoSRT Core VMs (Router A side: publisher/subscriber, Router B side: server) ───
    server = {
      index = 3;
      router = "B";
      shortName = "srv";
      description = "GoSRT server (pub/sub relay)";
      package = "server";
      # Service configuration (used by mkMicroVM)
      service = {
        binary = "server";
        args = [
          "-addr" "{vmIp}:6000"
          "-latency" "120"
          "-fc" "204800"
          "-promhttp" ":{promhttpPort}"
          "-useeventloop"
          "-usesendbtree" "-usesendring" "-usesendcontrolring" "-usesendeventloop"
          "-userecvcontrolring" "-usepacketring"
          "-iouringrecvenabled" "-iouringenabled"
        ];
        hasPrometheus = true;
      };
    };

    publisher = {
      index = 1;
      router = "A";
      shortName = "pub";
      description = "GoSRT publisher (client-generator)";
      package = "client-generator";
      service = {
        binary = "client-generator";
        # URL format: srt://host:port/stream-name (path becomes stream ID)
        # {bitrate} is replaced with publishBitrateBps from test config
        args = [
          "-to" "srt://{serverIp}:6000/gosrt"
          "-bitrate" "{bitrate}"
          "-latency" "120"
          "-fc" "204800"
          "-promhttp" ":{promhttpPort}"
          "-useeventloop"
          "-usesendbtree" "-usesendring" "-usesendcontrolring" "-usesendeventloop"
          "-userecvcontrolring" "-usepacketring"
          "-iouringrecvenabled" "-iouringenabled"
        ];
        hasPrometheus = true;
      };
    };

    subscriber = {
      index = 2;
      router = "A";
      shortName = "sub";
      description = "GoSRT subscriber (client)";
      package = "client";
      service = {
        binary = "client";
        # URL format: srt://host:port?streamid=subscribe:/stream-name&mode=caller
        args = [
          "-from" "srt://{serverIp}:6000?streamid=subscribe:/gosrt&mode=caller"
          "-to" "null"
          "-latency" "120"
          "-fc" "204800"
          "-promhttp" ":{promhttpPort}"
          "-useeventloop"
          "-usesendbtree" "-usesendring" "-usesendcontrolring" "-usesendeventloop"
          "-userecvcontrolring" "-usepacketring"
          "-iouringrecvenabled" "-iouringenabled"
        ];
        hasPrometheus = true;
      };
    };

    # ─── Interop Testing VMs (Router A side) ─────────────────────────────────
    xtransmit-pub = {
      index = 4;
      router = "A";
      shortName = "xtpub";
      description = "srt-xtransmit publisher (interop)";
      package = "srt-xtransmit";
      service = {
        binary = "srt-xtransmit";
        command = "generate";
        # {bitrateMbps} is replaced with "10Mbps" format from publishBitrateBps
        args = [
          "srt://{serverIp}:6000?mode=caller&latency=120&streamid=publish:/xtransmit"
          "--sendrate" "{bitrateMbps}"
          "--duration" "0"
          "--reconnect"
        ];
        hasPrometheus = false;
      };
    };

    ffmpeg-pub = {
      index = 5;
      router = "A";
      shortName = "ffpub";
      description = "FFmpeg publisher (test pattern)";
      package = "ffmpeg-full";
      service = {
        binary = "ffmpeg";
        # {bitrateKbps} is replaced with "10000k" format from publishBitrateBps
        # Note: maxrate and bufsize are set relative to bitrate for CBR-like behavior
        args = [
          "-re"
          "-f" "lavfi" "-i" "testsrc=size=1920x1080:rate=30"
          "-f" "lavfi" "-i" "sine=frequency=1000:sample_rate=48000"
          "-c:v" "libx264" "-preset" "ultrafast" "-tune" "zerolatency"
          "-b:v" "{bitrateKbps}" "-maxrate" "{bitrateKbps}" "-bufsize" "{bitrateKbps}"
          "-g" "60" "-keyint_min" "60"
          "-c:a" "aac" "-b:a" "128k"
          "-f" "mpegts"
          "srt://{serverIp}:6000?mode=caller&latency=120&streamid=publish:/ffmpeg"
        ];
        hasPrometheus = false;
      };
    };

    xtransmit-sub = {
      index = 6;
      router = "A";
      shortName = "xtsub";
      description = "srt-xtransmit subscriber (interop)";
      package = "srt-xtransmit";
      service = {
        binary = "srt-xtransmit";
        command = "receive";
        args = [
          "srt://{serverIp}:6000?mode=caller&latency=120&streamid=subscribe:/xtransmit"
          "--statsfreq" "1000"
          "--statsfile" "/tmp/xtransmit-stats.csv"
        ];
        hasPrometheus = false;
      };
    };

    ffmpeg-sub = {
      index = 7;
      router = "A";
      shortName = "ffsub";
      description = "FFmpeg subscriber (to /dev/null)";
      package = "ffmpeg-full";
      service = {
        binary = "ffmpeg";
        args = [
          "-i" "srt://{serverIp}:6000?mode=caller&latency=120&streamid=subscribe:/ffmpeg"
          "-c" "copy"
          "-f" "null"
          "/dev/null"
        ];
        hasPrometheus = false;
      };
    };

    # ─── Metrics VM (Router B side - same as server) ─────────────────────────
    metrics = {
      index = 8;
      router = "B";
      shortName = "metrics";
      description = "Prometheus + Grafana metrics collection";
      package = null;  # Uses NixOS services, not a GoSRT package
      service = null;  # Custom configuration in metrics module
    };
  };

  # ─── Inter-Router Links (Fixed Latency) ────────────────────────────────────
  # RTT/2 applied on each side
  latencyProfiles = [
    { index = 0; rttMs = 0;   name = "no-delay"; }
    { index = 1; rttMs = 10;  name = "regional-dc"; }
    { index = 2; rttMs = 60;  name = "cross-continental"; }
    { index = 3; rttMs = 130; name = "intercontinental"; }
    { index = 4; rttMs = 300; name = "geo-satellite"; }
  ];

  # ─── Router Configuration ──────────────────────────────────────────────────
  routers = {
    A = { namespace = "srt-router-a"; };
    B = { namespace = "srt-router-b"; };
  };

  # ─── Fixed Ports (Not per-role) ────────────────────────────────────────────
  ports = {
    srt = 6000;
    nodeExporter = 9100;       # node_exporter system metrics (standard port)
    prometheus = 9101;         # GoSRT application metrics (-promhttp)
    prometheusServer = 9090;   # Prometheus server (on metrics VM)
    grafana = 3000;            # Grafana UI (on metrics VM)
  };

  # ─── VM Resources ──────────────────────────────────────────────────────────
  # Note: Avoid exactly 2048MB due to QEMU bug
  # https://github.com/microvm-nix/microvm.nix/issues/171
  vm = {
    memoryMB = 2049;  # 2GB+ for SRT buffering (avoid exact 2048)
    vcpus = 8;        # 8 vCPUs for io_uring rings (doubled for performance)
    # Multi-queue TAP is auto-enabled by microvm when vcpu > 1
    # (queues = vcpu automatically via lib/runners/qemu.nix)
  };

  # ─── Netem Configuration ───────────────────────────────────────────────────
  netem = {
    queueLimit = 50000;  # Prevent tail-drop during high latency
  };

  # ─── Test Configuration ────────────────────────────────────────────────────
  test = {
    defaultDurationSeconds = 60;
    metricsCollectionIntervalMs = 1000;

    # Single source of truth for publisher bitrate (all 3 publishers)
    # 10 Mbps = 10,000,000 bits per second
    publishBitrateBps = 10000000;
  };

  # ─── Starlink Simulation Configuration ────────────────────────────────────
  # Simulates Starlink satellite handoff behavior
  starlink = {
    blackoutMs = 70;              # Blackout duration during handoff
    baselineDelayMs = 20;         # Normal Starlink latency
    baselineJitterMs = 10;        # Latency variation
    defaultDurationSeconds = 300; # Default simulation duration
    defaultInterval = 15;         # Seconds between blackouts (interval mode)
    # Seconds within each minute when handoffs occur (minute mode)
    minuteTimes = [ 12 27 42 57 ];
  };

  # ─── Go Build Configuration ────────────────────────────────────────────────
  # IMPORTANT: Must match CLAUDE.md build commands
  go = {
    version = "1.26";
    ldflags = [ "-s" "-w" ];
    # Note: greenteagc is now DEFAULT in Go 1.26 (no longer experimental)
    # Only jsonv2 remains experimental
    experimentalFeatures = [ "jsonv2" ];
  };
}
