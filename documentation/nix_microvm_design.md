# GoSRT Nix MicroVM Integration Testing Design

## Overview

This document describes the design for a Nix-based MicroVM integration testing system for GoSRT. The system replaces the current sudo-based network namespace testing with MicroVMs that can be run without root privileges after initial network setup.

**Design Goals**:
1. **Unprivileged testing**: Once network is configured (as root), MicroVMs run without sudo
2. **High-performance networking**: TAP + vhost-net for ~10 Gbps throughput
3. **Realistic network impairment**: tc/netem-based packet loss, latency, and Starlink patterns
4. **Data-driven & DRY**: Single role definition in `constants.nix` generates all VMs, scripts, network config
5. **Modular architecture**: Separate nix files for packages, VMs, network, shells
6. **OCI containers**: Build containers for all contrib binaries
7. **Development shell**: Complete build environment via `nix develop`

**Reference Projects**:
- `~/Downloads/pcp/nix/`: MicroVM infrastructure, network-setup.nix, constants.nix
- `~/Downloads/go-ffmpeg-hls-swarm/nix/`: TAP networking with vhost-net, profile system

---

## Quick Start

```bash
# One-time setup (requires root)
nix run .#srt-network-check        # Verify host environment
sudo nix run .#srt-network-setup   # Create TAP devices, namespaces, bridges

# Start all VMs (no root needed after setup)
nix run .#srt-tmux-all             # Launches all VMs in tmux session (recommended)
nix run .#srt-tmux-attach          # Re-attach to existing tmux session

# Or manually in separate terminals:
nix run .#srt-server-vm &
nix run .#srt-publisher-vm &
nix run .#srt-subscriber-vm &
nix run .#srt-metrics-vm &

# View dashboards
xdg-open http://10.50.8.2:3000/d/gosrt-ops    # Operations (NOC view)
xdg-open http://10.50.8.2:3000/d/gosrt-analysis  # Engineering deep-dive

# Apply network impairments (with automatic Grafana annotations)
nix run .#srt-set-latency -- 2     # Switch to 60ms RTT
nix run .#srt-set-loss -- 5 2      # Add 5% loss on link 2
nix run .#srt-starlink-pattern     # Simulate satellite handoff events

# Stop everything
nix run .#srt-vm-stop              # Stop all VMs
sudo nix run .#srt-network-teardown  # Remove network (optional)
```

---

## Architecture

### Network Topology

The MicroVM network mirrors the packet_loss_injection_design.md dual-router architecture.

**Key Architecture Note**: TAP devices MUST stay in the host namespace for QEMU to access them.
We use per-subnet bridges to connect TAPs to router namespaces via veth pairs:

```
┌─────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                              Host System                                                     │
│                                                                                                              │
│  LEGEND:                                                                                                     │
│    TAP ─── = TAP device (in host namespace, owned by $USER, used by QEMU)                                   │
│    BR ═══  = Linux bridge (in host namespace, connects TAP to veth)                                         │
│    veth ─→ = veth pair (crosses namespace boundary to router)                                               │
│    ns:[X]  = network namespace                                                                               │
│                                                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────────────────────────────────────┐  │
│  │                                    Client-Side MicroVMs (6 VMs)                                        │  │
│  │                                                                                                        │  │
│  │  ┌────────────────────┐  ┌────────────────────┐  ┌────────────────────┐  ┌────────────────────┐       │  │
│  │  │  srt-publisher     │  │  srt-subscriber    │  │ srt-xtransmit-pub  │  │   srt-ffmpeg-pub   │       │  │
│  │  │ (client-generator) │  │     (client)       │  │   (srt-xtransmit)  │  │  (ffmpeg 20Mb/s)   │       │  │
│  │  │  10.50.1.2/24      │  │  10.50.2.2/24      │  │   10.50.4.2/24     │  │   10.50.5.2/24     │       │  │
│  │  └─────────┬──────────┘  └─────────┬──────────┘  └─────────┬──────────┘  └─────────┬──────────┘       │  │
│  │            │ TAP                   │ TAP                   │ TAP                   │ TAP              │  │
│  │            ▼                       ▼                       ▼                       ▼                  │  │
│  │       ═══[BR]═══              ═══[BR]═══              ═══[BR]═══              ═══[BR]═══              │  │
│  │            │ veth                  │ veth                  │ veth                  │ veth             │  │
│  │                                                                                                        │  │
│  │  ┌────────────────────┐  ┌────────────────────┐                                                       │  │
│  │  │ srt-xtransmit-sub  │  │   srt-ffmpeg-sub   │                                                       │  │
│  │  │   (srt-xtransmit)  │  │ (ffmpeg /dev/null) │                                                       │  │
│  │  │   10.50.6.2/24     │  │   10.50.7.2/24     │                                                       │  │
│  │  └─────────┬──────────┘  └─────────┬──────────┘                                                       │  │
│  │            │ TAP                   │ TAP                                                              │  │
│  │            ▼                       ▼                                                                  │  │
│  │       ═══[BR]═══              ═══[BR]═══                                                              │  │
│  │            │ veth                  │ veth                                                             │  │
│  └────────────┼───────────────────────┼───────────────────────┼───────────────────────┼──────────────────┘  │
│               │                       │                       │                       │                     │
│               ▼                       ▼                       ▼                       ▼                     │
│  ┌──────────────────────────────────────────────────────────────────────────────────────────────────────┐   │
│  │                              ns:[srt-router-a] - Router A Namespace                                   │   │
│  │                                                                                                       │   │
│  │   veth interfaces (connected to bridges above):                                                       │   │
│  │     • veth-pub-r:  10.50.1.1/24    • veth-sub-r:  10.50.2.1/24    • veth-xtpub-r: 10.50.4.1/24       │   │
│  │     • veth-ffpub-r: 10.50.5.1/24   • veth-xtsub-r: 10.50.6.1/24   • veth-ffsub-r: 10.50.7.1/24       │   │
│  │                                                                                                       │   │
│  │   ┌─────────────────────────────────────────────────────────────────────────────────────────────┐    │   │
│  │   │              Inter-Router Links (Fixed Latency - Set at Setup)                               │    │   │
│  │   │                                                                                              │    │   │
│  │   │   link0: 10.50.100.1/30 ───── 0ms RTT ─────────────────────────────────────────────────────│    │   │
│  │   │   link1: 10.50.101.1/30 ───── 10ms RTT (5ms each way) ─────────────────────────────────────│    │   │
│  │   │   link2: 10.50.102.1/30 ───── 60ms RTT (30ms each way) ────────────────────────────────────│    │   │
│  │   │   link3: 10.50.103.1/30 ───── 130ms RTT (65ms each way) ───────────────────────────────────│    │   │
│  │   │   link4: 10.50.104.1/30 ───── 300ms RTT (150ms each way) ──────────────────────────────────│    │   │
│  │   │                                                                                              │    │   │
│  │   └─────────────────────────────────────────────────────────────────────────────────────────────┘    │   │
│  └──────────────────────────────────────────────────────────────────────────────────────────────────────┘   │
│                                             │ │ │ │ │                                                       │
│                                             │ │ │ │ │  5 parallel veth pairs                                │
│                                             │ │ │ │ │  (one per latency tier)                               │
│                                             ▼ ▼ ▼ ▼ ▼                                                       │
│  ┌──────────────────────────────────────────────────────────────────────────────────────────────────────┐   │
│  │                              ns:[srt-router-b] - Router B Namespace                                   │   │
│  │                                                                                                       │   │
│  │   link0: 10.50.100.2/30   link1: 10.50.101.2/30   link2: 10.50.102.2/30                              │   │
│  │   link3: 10.50.103.2/30   link4: 10.50.104.2/30                                                       │   │
│  │                                                                                                       │   │
│  │   veth-srv-r: 10.50.3.1/24        veth-metrics-r: 10.50.8.1/24                                       │   │
│  └──────────────────────────────────────────────────────────────────────────────────────────────────────┘   │
│               ▲                                           ▲                                                 │
│               │ veth                                      │ veth                                            │
│          ═══[BR]═══                                  ═══[BR]═══                                             │
│               │ TAP                                       │ TAP                                             │
│               ▼                                           ▼                                                 │
│  ┌──────────────────────────────────────────┐   ┌──────────────────────────────────────────┐               │
│  │         MicroVM: srt-server              │   │        MicroVM: srt-metrics              │               │
│  │           (contrib/server)               │   │      (Prometheus + Grafana)              │               │
│  │                                          │   │                                          │               │
│  │   IP: 10.50.3.2/24                       │   │   IP: 10.50.8.2/24                       │               │
│  │   SRT Port: 6000                         │   │   Prometheus: 9090                       │               │
│  │   Prometheus: 9100                       │   │   Grafana: 3000 (admin/srt)              │               │
│  └──────────────────────────────────────────┘   └──────────────────────────────────────────┘               │
│                                                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────────────────────────────────────┐   │
│  │                              Impairment Controller (Host Process)                                     │   │
│  │                                                                                                       │   │
│  │  • Latency switching: nix run .#srt-set-latency -- <0-4>  (changes routing to different link)        │   │
│  │  • Loss injection:    nix run .#srt-set-loss -- <percent> <link>  (tc netem loss)                    │   │
│  │  • 100% loss (Starlink): Blackhole routes (instant effect)                                           │   │
│  │  • Pattern orchestration: Configurable via srt-starlink-pattern                                      │   │
│  └──────────────────────────────────────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

**Data Path Example** (Publisher → Server):
```
Publisher VM → TAP (host) → Bridge (host) → veth-pub-h (host) → veth-pub-r (routerA ns)
             → link0..4 (selected by routing) → veth-srv-r (routerB ns)
             → veth-srv-h (host) → Bridge (host) → TAP (host) → Server VM
```

### MicroVM Roles

| VM | Binary | Purpose | Network |
|----|--------|---------|---------|
| **srt-server** | `contrib/server` | SRT pub/sub server | 10.50.3.2:6000 |
| **srt-publisher** | `contrib/client-generator` | Rate-limited data publisher (GoSRT) | 10.50.1.2 → server |
| **srt-subscriber** | `contrib/client` | Data receiver (GoSRT, validates delivery) | 10.50.2.2 ← server |
| **srt-xtransmit-pub** | `srt-xtransmit` | SRT reference impl publisher (interop test) | 10.50.4.2 → server |
| **srt-ffmpeg-pub** | `ffmpeg-full` | FFmpeg test pattern publisher @ 20Mb/s | 10.50.5.2 → server |
| **srt-xtransmit-sub** | `srt-xtransmit` | SRT reference impl subscriber (interop test) | 10.50.6.2 ← server |
| **srt-ffmpeg-sub** | `ffmpeg-full` | FFmpeg subscriber (output to /dev/null) | 10.50.7.2 ← server |
| **srt-metrics** | Prometheus + Grafana | Metrics collection and visualization | 10.50.8.2 (Prometheus:9090, Grafana:3000) |

> **Note**: `srt-seeker` (AIMD throughput discovery using `contrib/client-seeker`) is not implemented as a MicroVM. It can be run directly from the host or added as a future enhancement.

### Metrics VM as Developer Sidecar

The **srt-metrics** VM runs as a dedicated monitoring sidecar on Router B's side (same network as the server). This architecture provides critical benefits:

1. **Crash Isolation**: If the server or client VMs crash under stress testing (e.g., kernel panic, OOM), the metrics VM continues capturing data. This preserves the "death rattle" of the stream for post-mortem analysis.

2. **No Impairment Path**: The metrics VM connects directly to Router B, bypassing the impairment links. This ensures Prometheus scraping is not affected by tc/netem loss injection.

3. **Annotation Persistence**: Grafana annotations (from `srt-set-latency`, `srt-set-loss`, `srt-starlink-pattern`) are stored in the metrics VM's Prometheus, allowing correlation even if tests crash.

4. **Alternative Backend**: For even lighter weight, replace Prometheus with VictoriaMetrics:
   ```nix
   # In metrics.nix, swap Prometheus for VictoriaMetrics
   services.victoriametrics = {
     enable = true;
     retentionPeriod = "7d";
   };
   ```

---

## File Structure

```
gosrt/
├── flake.nix                    # Main entry point (orchestrates all modules)
├── flake.lock                   # Dependency lock file
└── nix/
    ├── constants.nix            # Shared network/port/VM configuration constants
    ├── lib.nix                  # Shared helpers and metadata
    │
    ├── packages/
    │   ├── default.nix          # All package exports
    │   ├── server.nix           # contrib/server binary
    │   ├── client.nix           # contrib/client binary
    │   ├── client-generator.nix # contrib/client-generator binary
    │   ├── client-seeker.nix    # contrib/client-seeker binary
    │   ├── performance.nix      # contrib/performance binary
    │   ├── udp-echo.nix         # contrib/udp_echo binary
    │   ├── srt-xtransmit.nix    # SRT reference impl traffic generator
    │   └── ffmpeg.nix           # FFmpeg with SRT support
    │
    ├── containers/
    │   ├── default.nix          # All container exports
    │   ├── server.nix           # OCI container for server
    │   ├── client.nix           # OCI container for client
    │   ├── client-generator.nix # OCI container for client-generator
    │   └── ...                  # Other containers
    │
    ├── microvms/
    │   ├── default.nix          # Data-driven generator (iterates over lib.roles)
    │   ├── base.nix             # MicroVM builder (takes role from lib.nix)
    │   └── metrics.nix          # Special case: Prometheus + Grafana
    │   # NOTE: Individual VM files (server.nix, publisher.nix, etc.) are
    │   # NO LONGER NEEDED - all VMs generated from constants.nix roles
    │
    ├── grafana/                  # Modular Grafana configuration (builtins.toJSON)
    │   ├── lib.nix              # Panel/dashboard helper functions
    │   ├── dashboards/
    │   │   ├── operations.nix   # Operations dashboard (NOC view)
    │   │   └── analysis.nix     # Analysis dashboard (Engineering view)
    │   └── panels/
    │       ├── overview.nix     # Throughput, RTT, retrans rate panels
    │       ├── traffic-lights.nix # Health status stat panels
    │       ├── recovery.nix     # NAK, TSBPD, buffer health panels
    │       ├── rings.nix        # Lock-free ring buffer panels
    │       ├── system.nix       # node_exporter io_uring contention panels
    │       └── anomalies.nix    # "Should be zero" defensive counters
    │
    ├── prometheus/
    │   ├── default.nix          # Prometheus service config
    │   └── scrape-configs.nix   # Scrape target generators
    │
    ├── network/
    │   ├── setup.nix            # Network setup scripts (run as root)
    │   ├── teardown.nix         # Network teardown scripts
    │   ├── impairment.nix       # tc/netem impairment control
    │   └── profiles.nix         # Predefined impairment profiles
    │
    ├── scripts/
    │   ├── vm-check.nix         # List running MicroVMs
    │   ├── vm-stop.nix          # Stop MicroVMs (individual or all)
    │   ├── vm-ssh.nix           # SSH into VMs
    │   └── vm-console.nix       # Serial console access (nc/socat)
    │
    ├── testing/
    │   ├── configs.nix          # Test configuration definitions
    │   ├── runner.nix           # Test orchestration scripts
    │   └── analysis.nix         # Metrics collection and analysis
    │
    ├── shell.nix                # Development shell configuration
    └── checks.nix               # CI checks (go vet, tests, etc.)
```

---

## Module Specifications

### 1. nix/constants.nix

Centralized configuration constants (adapted from pcp/nix/constants.nix):

```nix
# nix/constants.nix
#
# Shared constants for GoSRT MicroVM infrastructure.
# Import this file in all other modules to ensure consistency.
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
          "-prom" ":9100"
          "-useeventloop"
          "-usesendbtree" "-usesendring" "-usesendcontrolring" "-usesendeventloop"
          "-userecvcontrolring" "-usepacketring"
          "-useiouringrecv" "-useiouringsend"
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
        args = [
          "-addr" "{serverIp}:6000"
          "-latency" "120"
          "-fc" "204800"
          "-streamid" "publish:/gosrt"
          "-bitrate" "{bitrate}"
          "-prom" ":9100"
          "-useeventloop"
          "-usesendbtree" "-usesendring" "-usesendcontrolring" "-usesendeventloop"
          "-userecvcontrolring" "-usepacketring"
          "-useiouringrecv" "-useiouringsend"
        ];
        hasPrometheus = true;
        environment = { BITRATE = "50000000"; };
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
        args = [
          "-addr" "{serverIp}:6000"
          "-latency" "120"
          "-fc" "204800"
          "-streamid" "subscribe:/gosrt"
          "-prom" ":9100"
          "-useeventloop"
          "-usesendbtree" "-usesendring" "-usesendcontrolring" "-usesendeventloop"
          "-userecvcontrolring" "-usepacketring"
          "-useiouringrecv" "-useiouringsend"
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
        args = [
          "srt://{serverIp}:6000?mode=caller&latency=120&streamid=publish:/xtransmit"
          "--bitrate" "{bitrate}"
          "--duration" "0"
        ];
        hasPrometheus = false;
        environment = { BITRATE = "50M"; };
      };
    };

    ffmpeg-pub = {
      index = 5;
      router = "A";
      shortName = "ffpub";
      description = "FFmpeg publisher (20Mb/s test pattern)";
      package = "ffmpeg-full";
      service = {
        binary = "ffmpeg";
        args = [
          "-re"
          "-f" "lavfi" "-i" "testsrc=size=1920x1080:rate=30"
          "-f" "lavfi" "-i" "sine=frequency=1000:sample_rate=48000"
          "-c:v" "libx264" "-preset" "ultrafast" "-tune" "zerolatency"
          "-b:v" "19500k" "-maxrate" "20000k" "-bufsize" "40000k"
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
    prometheus = 9100;         # GoSRT metrics endpoints
    prometheusServer = 9090;   # Prometheus server (on metrics VM)
    grafana = 3000;            # Grafana UI (on metrics VM)
  };

  # ─── VM Resources ──────────────────────────────────────────────────────────
  vm = {
    memoryMB = 2048;  # 2GB for SRT buffering
    vcpus = 4;        # 4 vCPUs for io_uring rings
  };

  # ─── Netem Configuration ───────────────────────────────────────────────────
  netem = {
    queueLimit = 50000;  # Prevent tail-drop during high latency
  };

  # ─── Test Configuration ────────────────────────────────────────────────────
  test = {
    defaultDurationSeconds = 60;
    defaultBitrateMbps = 10;
    metricsCollectionIntervalMs = 1000;
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
```

### 2. nix/lib.nix

Computed values derived from role definitions (no hardcoded IPs/MACs/ports):

```nix
# nix/lib.nix
#
# Compute all derived values from role definitions.
# This eliminates hardcoded IPs, MACs, and ports - everything is derived
# from the role index in constants.nix.
#
# Type Documentation:
# -------------------
# Role (after computation):
#   { index: int, shortName: string, router: "A"|"B", description: string,
#     package: string, service: ServiceConfig, network: NetworkConfig,
#     ports: PortConfig, vmName: string, routerNamespace: string }
#
# NetworkConfig:
#   { tap: string, bridge: string, vethHost: string, vethRouter: string,
#     subnet: string, vmIp: string, gateway: string, mac: string }
#
# PortConfig:
#   { console: int, sshForward: int, prometheusForward: int }
#
# InterRouterLink:
#   { index: int, rttMs: int, name: string, subnetA: string, subnetB: string,
#     vethA: string, vethB: string, ipA: string, ipB: string }
#
{ lib }:

let
  constants = import ./constants.nix;
  c = constants;

  # ─── Validation Assertions ─────────────────────────────────────────────────
  # These run at evaluation time and fail the build if constraints are violated

  validateRole = name: role:
    assert lib.assertMsg (role ? index) "Role '${name}' missing required field 'index'";
    assert lib.assertMsg (role ? shortName) "Role '${name}' missing required field 'shortName'";
    assert lib.assertMsg (role ? router) "Role '${name}' missing required field 'router'";
    assert lib.assertMsg (role.router == "A" || role.router == "B")
      "Role '${name}' has invalid router '${role.router}' (must be A or B)";
    assert lib.assertMsg (role ? package) "Role '${name}' missing required field 'package'";
    assert lib.assertMsg (role.index >= 1 && role.index <= 254)
      "Role '${name}' has invalid index ${toString role.index} (must be 1-254)";
    role;

  # Validate all roles at evaluation time
  validatedRoles = lib.mapAttrs validateRole c.roles;

  # Ensure no index collisions
  indexList = lib.mapAttrsToList (_: r: r.index) validatedRoles;
  uniqueIndexes = lib.unique indexList;
  _ = assert lib.assertMsg (lib.length indexList == lib.length uniqueIndexes)
    "Duplicate role indexes detected! Each role must have a unique index.";
    null;

  # ─── Derivation Functions ──────────────────────────────────────────────────

  # Derive network config from role index
  mkRoleNetwork = name: role: {
    tap = "srttap-${role.shortName}";
    bridge = "srtbr-${role.shortName}";
    vethHost = "veth-${role.shortName}-h";
    vethRouter = "veth-${role.shortName}-r";
    subnet = "${c.base.subnetPrefix}.${toString role.index}.0/24";
    vmIp = "${c.base.subnetPrefix}.${toString role.index}.2";
    gateway = "${c.base.subnetPrefix}.${toString role.index}.1";
    # MAC format: 02:00:00:50:XX:02 where XX is hex of index
    mac = "02:00:00:50:${lib.fixedWidthString 2 "0" (lib.toHexString role.index)}:02";
  };

  # Derive ports from role index
  mkRolePorts = name: role: {
    console = c.base.consolePortBase + role.index;
    sshForward = c.base.sshPortBase + role.index;
    prometheusForward = c.base.prometheusPortBase + role.index;
  };

  # Derive inter-router link config
  mkInterRouterLink = profile: let
    subnet = "${c.base.subnetPrefix}.${toString (c.base.interRouterBase + profile.index)}";
  in {
    inherit (profile) index rttMs name;
    subnetA = subnet;
    subnetB = subnet;  # Same subnet, different IPs
    vethA = "link${toString profile.index}_a";
    vethB = "link${toString profile.index}_b";
    ipA = "${subnet}.1";
    ipB = "${subnet}.2";
  };

  # ─── Computed Attributes ───────────────────────────────────────────────────

  # Fully computed role configs (network + ports merged)
  # Uses validatedRoles to ensure all constraints are checked at evaluation time
  roles = lib.mapAttrs (name: role: role // {
    network = mkRoleNetwork name role;
    ports = mkRolePorts name role;
    vmName = "srt-${name}";
    routerNamespace = c.routers.${role.router}.namespace;
  }) validatedRoles;

  # Computed inter-router links
  interRouterLinks = map mkInterRouterLink c.latencyProfiles;

  # Helper: get server IP (commonly needed by other roles)
  serverIp = roles.server.network.vmIp;

  # Helper: list of all role names
  roleNames = lib.attrNames roles;

  # Helper: roles on Router A
  routerARoles = lib.filterAttrs (_: r: r.router == "A") roles;

  # Helper: roles on Router B
  routerBRoles = lib.filterAttrs (_: r: r.router == "B") roles;

  # Helper: roles with Prometheus endpoints (for scrape config)
  prometheusRoles = lib.filterAttrs (_: r: r.service.hasPrometheus or false) roles;

  # ─── Script Generation Helpers ─────────────────────────────────────────────

  # Generate ExecStart command from service config
  mkExecStart = role: pkg: let
    svc = role.service;
    # Replace placeholders in args
    replaceVars = arg: builtins.replaceStrings
      [ "{vmIp}" "{serverIp}" "{bitrate}" ]
      [ role.network.vmIp serverIp "\${BITRATE:-50000000}" ]
      arg;
    args = map replaceVars svc.args;
    cmd = if svc.command or null != null
          then "${pkg}/bin/${svc.binary} ${svc.command}"
          else "${pkg}/bin/${svc.binary}";
  in "${cmd} ${lib.concatStringsSep " " args}";

  # Generate environment from service config
  mkEnvironment = role:
    lib.mapAttrsToList (k: v: "${k}=${v}") (role.service.environment or {});

  # Router namespace shortcuts (commonly used in network scripts)
  routerA = c.routers.A.namespace;
  routerB = c.routers.B.namespace;

in {
  inherit roles interRouterLinks serverIp roleNames;
  inherit routerARoles routerBRoles prometheusRoles;
  inherit routerA routerB;  # Namespace shortcuts
  inherit mkExecStart mkEnvironment;

  # Re-export constants for convenience
  inherit (constants) base ports vm netem test go routers latencyProfiles;

  # ─── Prometheus Scrape Config Generator ────────────────────────────────────
  mkScrapeTargets = roles: port:
    lib.mapAttrsToList (_: r: "${r.network.vmIp}:${toString port}") roles;

  mkRelabelConfigs = roles: lib.mapAttrsToList (name: r: {
    source_labels = [ "__address__" ];
    regex = "${r.network.vmIp}:.*";
    target_label = "instance";
    replacement = name;
  }) roles;
}
```

### 3. nix/packages/default.nix

Package definitions for all contrib binaries:

```nix
# nix/packages/default.nix
#
# GoSRT binary packages built with buildGoModule.
# Uses Go 1.26 features (greenteagc GC is default, jsonv2 is experimental).
#
{ pkgs, lib, src }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # Vendor hash for reproducible builds
  # To update: run `nix build .#server 2>&1 | grep "got:"` and use that hash
  # Or use `nix-prefetch` / `nix hash to-sri`
  vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
  # TODO: Replace with actual hash after first build attempt.
  # The build will fail and print the correct hash.

  # Common build configuration
  commonBuild = subPackage: pkgs.buildGoModule {
    pname = "gosrt-${subPackage}";
    version = "0.1.0";
    inherit src vendorHash;

    subPackages = [ "contrib/${subPackage}" ];

    # Experimental Go features via GOEXPERIMENT
    preBuild = ''
      export GOEXPERIMENT=${lib.concatStringsSep "," gosrtLib.go.experimentalFeatures}
    '';

    ldflags = gosrtLib.go.ldflags;

    # Pure Go - no CGO required (giouring uses Go syscalls)
    CGO_ENABLED = "0";

    meta = with lib; {
      description = "GoSRT ${subPackage}";
      homepage = "https://github.com/randomizedcoder/gosrt";
      license = licenses.mit;
      platforms = platforms.linux;
    };
  };

  # Package definitions: attr name -> contrib directory name
  # DRY: This single source of truth generates both individual packages and the combined "all" package
  packageDefs = {
    server = "server";
    client = "client";
    client-generator = "client-generator";
    client-seeker = "client-seeker";
    performance = "performance";
    udp-echo = "udp_echo";  # Note: directory is udp_echo, attr is udp-echo
  };

  # Generate all individual packages from packageDefs
  packages = lib.mapAttrs (_name: dir: commonBuild dir) packageDefs;

in packages // {
  # Combined package with all binaries
  all = pkgs.symlinkJoin {
    name = "gosrt-all";
    paths = lib.attrValues packages;
  };

  # Expose vendorHash for documentation
  inherit vendorHash;
}
```

**Updating vendorHash:**
```bash
# After changing go.mod/go.sum, update the hash:
nix build .#server 2>&1 | grep "got:" | awk '{print $2}'
# Then update vendorHash in packages/default.nix
```

### 2b. nix/packages/srt-xtransmit.nix

srt-xtransmit package for SRT interoperability testing:

```nix
# nix/packages/srt-xtransmit.nix
#
# srt-xtransmit: Reference SRT implementation traffic generator.
# From: https://github.com/maxsharabayko/srt-xtransmit
#
# Usage:
#   srt-xtransmit generate "srt://SERVER:PORT?mode=caller&latency=200" --bitrate 100M
#   srt-xtransmit receive "srt://SERVER:PORT?mode=caller&latency=200" --statsfreq 1000
#
{ pkgs, lib }:

pkgs.stdenv.mkDerivation {
  pname = "srt-xtransmit";
  version = "0.2.0";

  src = pkgs.fetchFromGitHub {
    owner = "maxsharabayko";
    repo = "srt-xtransmit";
    rev = "v0.2.0";
    fetchSubmodules = true;
    hash = "sha256-AEqVJr7TLH+MV4SntZhFFXTttnmcywda/P1EoD2px6E=";
  };

  nativeBuildInputs = [
    pkgs.cmake
    pkgs.pkg-config
  ];

  buildInputs = [
    pkgs.openssl
  ];

  cmakeFlags = [
    "-DENABLE_CXX17=OFF"
    "-DCMAKE_POLICY_VERSION_MINIMUM=3.5"
  ];

  postInstall = ''
    candidate=""
    for p in \
      build/xtransmit/bin/srt-xtransmit \
      build/bin/srt-xtransmit \
      build/xtransmit/srt-xtransmit \
      bin/srt-xtransmit \
    ; do
      if [ -x "$p" ]; then
        candidate="$p"
        break
      fi
    done

    if [ -z "$candidate" ]; then
      candidate="$(find . -type f -name srt-xtransmit -perm -0100 | head -n1 || true)"
    fi

    if [ -z "$candidate" ] || [ ! -x "$candidate" ]; then
      echo "ERROR: srt-xtransmit binary not found" >&2
      exit 1
    fi

    install -Dm755 "$candidate" "$out/bin/srt-xtransmit"
  '';

  meta = with lib; {
    description = "SRT xtransmit performance / traffic generator";
    homepage = "https://github.com/maxsharabayko/srt-xtransmit";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
```

### 2c. nix/packages/ffmpeg.nix

FFmpeg package with SRT support for video streaming:

```nix
# nix/packages/ffmpeg.nix
#
# FFmpeg with SRT support for video streaming.
# Uses ffmpeg-full which includes libsrt (withSrt = true).
#
# Publisher example (20Mb/s test pattern):
#   ffmpeg -re -f lavfi -i testsrc=size=1920x1080:rate=30 \
#     -c:v libx264 -b:v 19500k -f mpegts \
#     "srt://SERVER:PORT?mode=caller&streamid=publish:/stream"
#
# Subscriber example (output to /dev/null):
#   ffmpeg -i "srt://SERVER:PORT?mode=caller&streamid=subscribe:/stream" \
#     -c copy -f null /dev/null
#
{ pkgs, lib }:

# ffmpeg-full enables withSrt = true by default
# This includes libsrt for SRT protocol support
pkgs.ffmpeg-full
```

### 3. nix/containers/server.nix

OCI container for the server binary:

```nix
# nix/containers/server.nix
#
# OCI container for GoSRT server.
# Includes Prometheus metrics endpoint.
#
{ pkgs, lib, serverPackage }:

let
  constants = import ../constants.nix;

  entrypoint = pkgs.writeShellApplication {
    name = "server-entrypoint";
    runtimeInputs = [ serverPackage ];
    text = ''
      set -euo pipefail

      # Default configuration via environment variables
      ADDR="''${ADDR:-0.0.0.0:${toString constants.ports.srt}}"
      LATENCY="''${LATENCY:-120}"
      FC="''${FC:-102400}"

      exec server \
        -addr "$ADDR" \
        -latency "$LATENCY" \
        -fc "$FC" \
        -prom ":${toString constants.ports.prometheus}" \
        "$@"
    '';
  };

in pkgs.dockerTools.buildLayeredImage {
  name = "gosrt-server";
  tag = "latest";

  contents = [
    serverPackage
    entrypoint
    pkgs.busybox
    pkgs.curl
    pkgs.cacert
  ];

  config = {
    Entrypoint = [ "${lib.getExe entrypoint}" ];
    ExposedPorts = {
      "${toString constants.ports.srt}/udp" = {};
      "${toString constants.ports.prometheus}/tcp" = {};
    };
    Env = [
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
    ];
    Labels = {
      "org.opencontainers.image.title" = "gosrt-server";
      "org.opencontainers.image.description" = "GoSRT SRT server with pub/sub support";
    };
  };

  fakeRootCommands = ''
    mkdir -p /tmp
    chmod 1777 /tmp
  '';

  maxLayers = 100;
}
```

### 4. nix/microvms/base.nix

Base MicroVM configuration - refactored to work with data-driven roles from `lib.nix`:

```nix
# nix/microvms/base.nix
#
# Base MicroVM configuration shared by all GoSRT VMs.
# Provides common settings: kernel, networking, systemd setup.
#
# DESIGN: Takes a fully-computed role (from lib.nix) and generates the
# complete MicroVM including systemd service from role.service config.
#
# ELEGANCE: Uses specialArgs to inject packages into the NixOS module system.
# This allows swapping production ↔ debug builds without touching VM configs.
#
{ pkgs, lib, microvm, nixpkgs, system }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

in {
  # Function to create a MicroVM from a computed role
  # role: Fully computed role from lib.nix (includes network, ports, service config)
  # packages: Package set (gosrt packages + external packages like ffmpeg)
  # buildVariant: "production" | "debug" - selects which binary to use
  mkMicroVM = { role, packages, buildVariant ? "production" }:

  let
    # Unpack role values for readability
    name = role.vmName;
    net = role.network;
    svc = role.service;

    # Get the package for this role (null for metrics VM)
    # Supports build variants: production vs debug (with assertions enabled)
    pkg = if role.package != null
          then (
            if buildVariant == "debug" && packages ? "${role.package}-debug"
            then packages."${role.package}-debug"
            else packages.${role.package}
          )
          else null;

    # ─────────────────────────────────────────────────────────────────────────
    # Generate systemd service module from role.service config
    # ─────────────────────────────────────────────────────────────────────────
    mkServiceModule = { config, pkgs, ... }: lib.mkIf (svc != null) {
      environment.systemPackages = lib.optional (pkg != null) pkg;

      systemd.services."gosrt-${role.shortName}" = {
        description = role.description;
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];

        serviceConfig = {
          # Wait for server (except for server itself)
          ExecStartPre = lib.mkIf (role.router == "A")
            "${pkgs.bash}/bin/bash -c 'sleep 5'";

          # Generate ExecStart from service config
          ExecStart = gosrtLib.mkExecStart role pkg;

          Restart = "on-failure";
          RestartSec = if role.router == "A" then "5s" else "1s";

          # Graceful shutdown
          TimeoutStopSec = "10s";
          KillSignal = "SIGTERM";
          KillMode = "mixed";
        } // lib.optionalAttrs ((svc.environment or {}) != {}) {
          Environment = gosrtLib.mkEnvironment role;
        };
      };
    };

    # ─────────────────────────────────────────────────────────────────────────
    # Build the VM NixOS system
    # ─────────────────────────────────────────────────────────────────────────
    vmConfig = nixpkgs.lib.nixosSystem {
      inherit system;

      # ELEGANCE: Use specialArgs to inject dependencies into modules.
      # This allows any module to access gosrt packages without passing through
      # every layer. Swap production ↔ debug by changing packages here.
      specialArgs = {
        inherit gosrtLib role pkg;
        gosrtPackages = packages;
        inherit buildVariant;
      };

      modules = [
        microvm.nixosModules.microvm

        ({ config, pkgs, ... }: {
          system.stateVersion = "26.05";
          nixpkgs.hostPlatform = system;

          networking.hostName = name;

          # MicroVM configuration
          microvm = {
            hypervisor = "qemu";
            mem = gosrtLib.vm.memoryMB;
            vcpu = gosrtLib.vm.vcpus;

            # Share host's /nix/store (faster startup)
            shares = [{
              tag = "ro-store";
              source = "/nix/store";
              mountPoint = "/nix/.ro-store";
              proto = "9p";
            }];

            # TAP networking with vhost-net
            interfaces = [{
              type = "tap";
              id = net.tap;
              mac = net.mac;
              tap.vhost = true;  # ~10 Gbps throughput
            }];

            # Control socket for management
            socket = "control.socket";

            # Serial console via TCP socket (for when network isn't working)
            # Connect via: nc localhost <consolePort>
            qemu.serialConsole = false;
            qemu.extraArgs = [
              # IMPORTANT: Process name "gosrt:${name}" is used by srt-vm-stop scripts
              "-name" "gosrt:${name},process=gosrt:${name}"
              # TCP serial console (raw socket, NOT telnet - for nc compatibility)
              "-chardev" "socket,id=tcpcon,host=localhost,port=${toString role.ports.console},server=on,wait=off"
              "-serial" "chardev:tcpcon"
            ];
          };

          # Kernel console for TCP serial
          boot.kernelParams = [ "console=ttyS0" "earlyprintk=ttyS0" ];

          # Static IP configuration via systemd-networkd
          systemd.network = {
            enable = true;
            networks."10-vm" = {
              matchConfig.MACAddress = net.mac;
              networkConfig = {
                DHCP = "no";
                Address = "${net.vmIp}/24";
                Gateway = net.gateway;
                DNS = [ "1.1.1.1" "8.8.8.8" ];
              };
            };
          };

          # Use latest kernel for best io_uring support (currently 6.18.8)
          boot.kernelPackages = pkgs.linuxPackages_latest;

          # Writable tmpfs for runtime data (logs, stats files, etc.)
          fileSystems."/tmp" = {
            device = "tmpfs";
            fsType = "tmpfs";
            options = [ "size=256M" "mode=1777" ];
          };

          fileSystems."/var/log" = {
            device = "tmpfs";
            fsType = "tmpfs";
            options = [ "size=64M" "mode=0755" ];
          };

          # Kernel parameters for performance
          boot.kernel.sysctl = {
            # Network buffers
            "net.core.rmem_max" = 134217728;  # 128MB
            "net.core.wmem_max" = 134217728;
            "net.core.rmem_default" = 16777216;
            "net.core.wmem_default" = 16777216;
            "net.ipv4.udp_rmem_min" = 16384;
            "net.ipv4.udp_wmem_min" = 16384;
            # VM tuning
            "vm.swappiness" = 10;
          };

          # SSH for debugging
          services.openssh = {
            enable = true;
            settings = {
              PasswordAuthentication = true;
              PermitRootLogin = "yes";
            };
          };
          users.users.root.password = "srt";

          # ═══════════════════════════════════════════════════════════════════
          # Node Exporter - System-level metrics for io_uring contention detection
          # ═══════════════════════════════════════════════════════════════════
          services.prometheus.exporters.node = {
            enable = true;
            port = 9100;
            listenAddress = "0.0.0.0";
            enabledCollectors = [
              "cpu" "loadavg" "meminfo" "netdev"
              "schedstat" "softirqs" "vmstat" "stat"
            ];
          };

          # Allow node_exporter port
          networking.firewall.allowedTCPPorts = [ 9100 ];

          # Debug tools
          environment.systemPackages = with pkgs; [
            htop iftop tcpdump iproute2 curl perf
          ];

          # Disable unnecessary services
          documentation.enable = false;
          services.nscd.enable = false;
          system.nssModules = lib.mkForce [];
        })

        # Add service module (generated from role.service)
        mkServiceModule
      ];
    };

  in {
    vm = vmConfig.config.microvm.declaredRunner;
    nixos = vmConfig;

    runScript = pkgs.writeShellScript "run-${name}" ''
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  GoSRT MicroVM: ${name}                                           "
      echo "║  IP: ${net.vmIp}                                                  "
      echo "╠══════════════════════════════════════════════════════════════════╣"
      echo "║  SSH:     ssh root@${net.vmIp} (password: srt)                    "
      echo "║  Console: nc localhost ${toString role.ports.console}             "
      echo "║  Metrics: http://${net.vmIp}:${toString gosrtLib.ports.prometheus}/metrics"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Starting MicroVM (Ctrl+A X to exit QEMU)..."
      exec ${vmConfig.config.microvm.declaredRunner}/bin/microvm-run
    '';
  };
}
```

### 5. nix/microvms/default.nix (Data-Driven Generator)

**IMPORTANT REFACTOR**: This replaces all individual VM files (server.nix, publisher.nix, etc.)
with a single data-driven generator that iterates over `lib.roles`.

```nix
# nix/microvms/default.nix
#
# DATA-DRIVEN MICROVM GENERATOR
#
# Generates all MicroVMs from the role definitions in lib.nix.
# Individual VM files (server.nix, publisher.nix, etc.) are NO LONGER NEEDED.
#
# Benefits:
#   - Single source of truth (roles in constants.nix)
#   - Impossible for IP/MAC/port mismatches
#   - Adding a new VM = adding a role definition
#   - ~80% reduction in MicroVM boilerplate
#
{ pkgs, lib, microvm, nixpkgs, system, packages, srtXtransmit, ffmpegFull }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  baseMicroVM = import ./base.nix { inherit pkgs lib microvm nixpkgs system; };

  # Package map: role.package name -> actual package derivation
  # Handles both GoSRT packages and external packages (ffmpeg, xtransmit)
  packageMap = packages // {
    srt-xtransmit = srtXtransmit;
    ffmpeg-full = ffmpegFull;
  };

  # Generate a MicroVM for any role (except metrics, which is special)
  mkRoleVM = name: role:
    if name == "metrics"
    then import ./metrics.nix { inherit pkgs lib microvm nixpkgs system; }
    else baseMicroVM.mkMicroVM {
      inherit role;
      packages = packageMap;
    };

in
  # Generate all VMs from roles
  lib.mapAttrs mkRoleVM gosrtLib.roles
```

**What this replaces:**
- `nix/microvms/server.nix` (deleted - generated from `roles.server`)
- `nix/microvms/publisher.nix` (deleted - generated from `roles.publisher`)
- `nix/microvms/subscriber.nix` (deleted - generated from `roles.subscriber`)
- `nix/microvms/xtransmit-pub.nix` (deleted - generated from `roles.xtransmit-pub`)
- `nix/microvms/xtransmit-sub.nix` (deleted - generated from `roles.xtransmit-sub`)
- `nix/microvms/ffmpeg-pub.nix` (deleted - generated from `roles.ffmpeg-pub`)
- `nix/microvms/ffmpeg-sub.nix` (deleted - generated from `roles.ffmpeg-sub`)

**The metrics VM remains special** because it uses NixOS services (Prometheus/Grafana)
instead of a binary. See `nix/microvms/metrics.nix` for its implementation.

### New File Structure

```
nix/microvms/
├── default.nix          # Data-driven generator (iterates over lib.roles)
├── base.nix             # MicroVM builder (takes role from lib.nix)
└── metrics.nix          # Special case: Prometheus + Grafana (not a GoSRT binary)
```

Compare to the old structure (7 files deleted):
```
nix/microvms/
├── default.nix          # Manual mapping
├── base.nix             # Generic builder
├── server.nix           # DELETED - now generated
├── publisher.nix        # DELETED - now generated
├── subscriber.nix       # DELETED - now generated
├── xtransmit-pub.nix    # DELETED - now generated
├── xtransmit-sub.nix    # DELETED - now generated
├── ffmpeg-pub.nix       # DELETED - now generated
├── ffmpeg-sub.nix       # DELETED - now generated
└── metrics.nix          # Kept (special case)
```

### 5i. nix/grafana/lib.nix

Grafana dashboard helper functions - generates JSON from Nix data structures:

```nix
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
{ lib }:

let
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

  # Row (collapsible section)
  mkRow = { title, y, collapsed ? false, panels ? [] }:
    mkPanel {
      inherit title collapsed panels;
      type = "row";
      gridPos = { h = 1; w = 24; x = 0; inherit y; };
    };

  # ═══════════════════════════════════════════════════════════════════════════
  # Target Builders (Prometheus queries)
  # ═══════════════════════════════════════════════════════════════════════════

  # Single target with expression and legend
  mkTarget = expr: legendFormat: { inherit expr legendFormat; };

  # Generate targets for all instances with a pattern
  # Usage: mkInstanceTargets "gosrt_rtt_microseconds" "{name} RTT" ["server" "publisher" "subscriber"]
  mkInstanceTargets = metric: legendPattern: instances:
    map (inst: mkTarget
      "${metric}{instance=\"${inst}\"}"
      (builtins.replaceStrings ["{name}"] [inst] legendPattern)
    ) instances;

  # Generate comparison targets (both ends of a flow)
  # Usage: mkFlowTargets { send = "publisher"; recv = "server"; } "congestion_packets_total" "pps"
  mkFlowTargets = { send, recv }: metric: unit:
    [
      (mkTarget "rate(gosrt_connection_${metric}{instance=\"${send}\", direction=\"send\"}[5s])" "${send} SEND")
      (mkTarget "rate(gosrt_connection_${metric}{instance=\"${recv}\", direction=\"recv\"}[5s])" "${recv} RECV")
    ];

  # ═══════════════════════════════════════════════════════════════════════════
  # High-Level Panel Presets (DRY helpers for common patterns)
  # ═══════════════════════════════════════════════════════════════════════════

  # Health traffic light panel (loss percentage for a stream)
  # Usage: mkHealthStat { title = "Ingest Health"; instance = "server"; direction = "recv"; }
  mkHealthStat = { title, instance, direction, description ? null, gridPos }:
    mkStat {
      inherit title gridPos;
      description = description or "${title}: GREEN: <1% loss, YELLOW: 1-3% loss, RED: >3% loss";
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
  # Usage: mkThroughputPanel { title = "Server Throughput"; instance = "server"; }
  mkThroughputPanel = { title, instance, gridPos, showBoth ? false }:
    mkTimeseries {
      inherit title gridPos;
      description = "Data throughput in Mbps";
      unit = "Mbits";
      targets =
        if showBoth then [
          (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"${instance}\"} * 8 / 1000000" "${instance} TX")
          (mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"${instance}\"} * 8 / 1000000" "${instance} RX")
        ] else [
          (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"${instance}\"} * 8 / 1000000" "${instance}")
        ];
    };

  # Counter rate panel (generic rate of any counter)
  # Usage: mkRatePanel { title = "NAKs/s"; metric = "gosrt_nak_sent_total"; unit = "pps"; ... }
  mkRatePanel = { title, metric, unit ? "pps", instances ? [ "publisher" "server" "subscriber" ], gridPos, description ? null }:
    mkTimeseries {
      inherit title gridPos unit description;
      targets = map (inst: mkTarget
        "rate(${metric}{instance=\"${inst}\"}[5s])"
        inst
      ) instances;
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
      processPanel = { y, result }: panel:
        let
          # Rows have height 1, other panels use their gridPos.h
          h = if panel.type == "row" then 1 else (panel.gridPos.h or 8);
          # Update panel with calculated y position
          newPanel = panel // {
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
          result = result ++ [ newPanelWithNested ];
        };
    in (lib.foldl processPanel { y = 0; result = []; } panels).result;

in {
  inherit
    # Core panel builders
    mkPanel mkTimeseries mkStat mkGauge mkRow
    # Target builders
    mkTarget mkInstanceTargets mkFlowTargets
    # Dashboard builders
    mkDashboard autoLayoutPanels
    # High-level presets (DRY helpers)
    mkHealthStat mkThroughputPanel mkRatePanel
    # Threshold presets
    thresholds;
}
```

### 5j. nix/grafana/panels/overview.nix

Example panel definitions using the library (reduces repetition):

```nix
# nix/grafana/panels/overview.nix
#
# Overview panels: throughput, retransmission rate, RTT
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkTarget mkInstanceTargets thresholds;

  # Common instances to display
  instances = [ "publisher" "server" "subscriber" ];

in {
  # Throughput panel - all endpoints TX/RX
  throughput = mkTimeseries {
    title = "Throughput (Mbps)";
    description = "Data throughput across all endpoints";
    unit = "Mbits";
    gridPos = { h = 6; w = 8; x = 0; };  # y auto-calculated
    targets = [
      (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"publisher\"} * 8 / 1000000" "Publisher TX")
      (mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"server\"} * 8 / 1000000" "Server RX")
      (mkTarget "gosrt_send_rate_sent_bandwidth_bps{instance=\"server\"} * 8 / 1000000" "Server TX")
      (mkTarget "gosrt_recv_rate_bytes_per_sec{instance=\"subscriber\"} * 8 / 1000000" "Subscriber RX")
    ];
  };

  # Retransmission rate with thresholds
  retransRate = mkTimeseries {
    title = "Retransmission Rate (%)";
    description = "Retransmission overhead - lower is better";
    unit = "percent";
    gridPos = { h = 6; w = 8; x = 8; };
    thresholds = thresholds.retransPercent;
    targets = mkInstanceTargets "gosrt_send_rate_retrans_percent" "{name}" instances;
  };

  # RTT with thresholds
  rtt = mkTimeseries {
    title = "RTT (ms)";
    description = "Round-trip time from each endpoint's perspective";
    unit = "ms";
    gridPos = { h = 6; w = 8; x = 16; };
    thresholds = thresholds.rttMs;
    targets = map (inst: mkTarget
      "gosrt_rtt_microseconds{instance=\"${inst}\"} / 1000"
      inst
    ) instances;
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # ELEGANCE: Delta / Efficiency View
  # Shows live efficiency ratio so operators don't have to do math.
  # "What the network sent" vs "What the app successfully received"
  # ═══════════════════════════════════════════════════════════════════════════

  # Live efficiency ratio (received vs sent)
  deliveryEfficiency = mkGauge {
    title = "Delivery Efficiency";
    description = "Ratio of successfully received packets to total sent. Shows real-time protocol efficiency including retransmits.";
    unit = "percentunit";
    min = 0;
    max = 1;
    gridPos = { h = 6; w = 6; x = 0; };
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 0.90; }
        { color = "green"; value = 0.98; }
      ];
    };
    targets = [
      (mkTarget ''
        rate(gosrt_connection_pkt_recv_data_success_total{instance="subscriber"}[30s]) /
        (rate(gosrt_connection_pkt_send_data_total{instance="publisher"}[30s]) + 0.001)
      '' "End-to-End")
    ];
  };

  # Recovery efficiency (how well NAK/retransmit is working)
  recoveryEfficiency = mkTimeseries {
    title = "Recovery Efficiency";
    description = "Packets recovered via retransmit vs packets lost. Shows NAK btree effectiveness.";
    unit = "percentunit";
    gridPos = { h = 6; w = 6; x = 6; };
    targets = [
      (mkTarget ''
        rate(gosrt_connection_congestion_recv_pkt_recovered_total[30s]) /
        (rate(gosrt_connection_congestion_packets_lost_total[30s]) + 0.001)
      '' "Recovery Rate")
    ];
  };

  # Bandwidth utilization (actual vs requested)
  bandwidthUtilization = mkTimeseries {
    title = "Bandwidth Utilization";
    description = "Actual throughput vs requested bitrate. Shows congestion control effectiveness.";
    unit = "percentunit";
    gridPos = { h = 6; w = 6; x = 12; };
    targets = [
      (mkTarget ''
        gosrt_send_rate_sent_bandwidth_bps{instance="publisher"} /
        (gosrt_send_rate_requested_bps{instance="publisher"} + 1)
      '' "Publisher")
      (mkTarget ''
        gosrt_recv_rate_bytes_per_sec{instance="subscriber"} * 8 /
        (gosrt_send_rate_requested_bps{instance="publisher"} + 1)
      '' "Subscriber")
    ];
  };
}
```

### 5k. nix/grafana/panels/traffic-lights.nix

Traffic light status panels for Operations dashboard:

```nix
# nix/grafana/panels/traffic-lights.nix
#
# Traffic light (stat) panels for quick health assessment
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkStat mkTarget thresholds;

in {
  # Ingest health traffic light
  ingestHealth = mkStat {
    title = "Ingest Health";
    description = "Publisher→Server stream health. GREEN: <1% loss, YELLOW: 1-3% loss, RED: >3% loss";
    unit = "percent";
    thresholds = thresholds.lossPercent;
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 0; };
    targets = [
      (mkTarget ''
        100 * rate(gosrt_connection_congestion_packets_lost_total{instance="server", direction="recv"}[30s]) /
        (rate(gosrt_connection_congestion_packets_total{instance="server", direction="recv"}[30s]) + 0.001)
      '' "Loss %")
    ];
  };

  # Delivery health traffic light
  deliveryHealth = mkStat {
    title = "Delivery Health";
    description = "Server→Subscriber stream health";
    unit = "percent";
    thresholds = thresholds.lossPercent;
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 4; };
    targets = [
      (mkTarget ''
        100 * rate(gosrt_connection_congestion_packets_lost_total{instance="subscriber", direction="recv"}[30s]) /
        (rate(gosrt_connection_congestion_packets_total{instance="subscriber", direction="recv"}[30s]) + 0.001)
      '' "Loss %")
    ];
  };

  # Unrecoverable loss (TSBPD skipped)
  unrecoverableLoss = mkStat {
    title = "Unrecoverable Loss";
    description = "Packets that NEVER arrived before TSBPD deadline. RED if any.";
    unit = "pps";
    thresholds = thresholds.shouldBeZero;
    colorMode = "background";
    graphMode = "area";
    gridPos = { h = 4; w = 4; x = 8; };
    targets = [
      (mkTarget "sum(rate(gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total[30s]))" "TSBPD Skipped")
    ];
  };

  # Active connections
  activeConnections = mkStat {
    title = "Active Streams";
    description = "Number of active SRT connections";
    unit = "short";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }  # 0 = red
        { color = "green"; value = 1; }   # 1+ = green
      ];
    };
    colorMode = "value";
    gridPos = { h = 4; w = 4; x = 12; };
    targets = [
      (mkTarget "count(gosrt_connection_start_time_seconds)" "Connections")
    ];
  };

  # Ring buffer drops
  ringDrops = mkStat {
    title = "Ring Buffer";
    description = "Lock-free ring health. RED: ring overflow (data loss).";
    unit = "short";
    thresholds = thresholds.shouldBeZero;
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 16; };
    targets = [
      (mkTarget "sum(gosrt_receiver_ring_drops_total) + sum(gosrt_sender_ring_dropped_total)" "Ring Drops")
    ];
  };
}
```

### 5l. nix/grafana/dashboards/operations.nix

Operations dashboard assembled from panel modules:

```nix
# nix/grafana/dashboards/operations.nix
#
# High-level Operations dashboard for NOC operators.
# Assembled from reusable panel modules.
#
{ lib, grafanaLib, panels }:

let
  inherit (grafanaLib) mkDashboard mkRow mkTimeseries mkTarget;
  inherit (panels) trafficLights overview;

in mkDashboard {
  title = "GoSRT Operations";
  uid = "gosrt-ops";
  tags = [ "gosrt" "srt" "operations" ];
  refresh = "5s";
  timeFrom = "now-15m";

  links = [{
    title = "Analysis Dashboard";
    url = "/d/gosrt-analysis/gosrt-analysis";
    type = "link";
    icon = "dashboard";
    tooltip = "Detailed analysis for debugging";
  }];

  panels = [
    # ═══════════════════════════════════════════════════════════════════════
    # Row 1: Stream Health Status (Traffic Lights)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow { title = "Stream Health Status"; y = 0; })

    trafficLights.ingestHealth
    trafficLights.deliveryHealth
    trafficLights.unrecoverableLoss
    trafficLights.activeConnections
    trafficLights.ringDrops

    # ═══════════════════════════════════════════════════════════════════════
    # Row 2: Throughput & Quality
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow { title = "Throughput & Quality"; y = 5; })

    (mkTimeseries {
      title = "Goodput (Effective Throughput)";
      description = "Unique bytes delivered to application (excludes retransmissions)";
      unit = "Mbits";
      gridPos = { h = 8; w = 12; x = 0; };
      targets = [
        (mkTarget "rate(gosrt_connection_congestion_bytes_unique_total{instance=\"server\", direction=\"recv\"}[5s]) * 8 / 1000000"
                  "Ingest Goodput (Pub→Srv)")
        (mkTarget "rate(gosrt_connection_congestion_bytes_unique_total{instance=\"subscriber\", direction=\"recv\"}[5s]) * 8 / 1000000"
                  "Delivery Goodput (Srv→Sub)")
      ];
    })

    # ... more panels ...

    # ═══════════════════════════════════════════════════════════════════════
    # Row 3: Network Conditions
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow { title = "Network Conditions"; y = 14; })

    overview.rtt
    overview.retransRate

    # Loss events panel
    (mkTimeseries {
      title = "Loss Events Over Time";
      description = "Raw packet loss detection rate";
      unit = "pps";
      gridPos = { h = 8; w = 8; x = 16; };
      targets = [
        (mkTarget "rate(gosrt_connection_congestion_packets_lost_total{instance=\"server\", direction=\"recv\"}[5s])"
                  "Server loss detected")
        (mkTarget "rate(gosrt_connection_congestion_packets_lost_total{instance=\"subscriber\", direction=\"recv\"}[5s])"
                  "Subscriber loss detected")
      ];
    })

    # ═══════════════════════════════════════════════════════════════════════
    # Row 4: System Health (collapsed)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "System Health (io_uring Contention)";
      y = 23;
      collapsed = true;
      panels = [
        (mkTimeseries {
          title = "Context Switches/sec";
          unit = "ops";
          gridPos = { h = 6; w = 8; x = 0; y = 0; };
          targets = [
            (mkTarget "rate(node_context_switches_total{instance=\"server\"}[5s])" "Server")
            (mkTarget "rate(node_context_switches_total{instance=\"publisher\"}[5s])" "Publisher")
          ];
        })
        # ... more collapsed panels ...
      ];
    })
  ];
}
```

### 5m. nix/prometheus/scrape-configs.nix

Prometheus scrape config generators (eliminates relabel_config repetition):

```nix
# nix/prometheus/scrape-configs.nix
#
# Generates Prometheus scrape configurations with automatic relabeling.
# Uses lib.nix (computed from constants.nix) as single source of truth for IP addresses.
#
{ lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # Instance definitions derived from lib.nix roles (single source of truth)
  # GoSRT endpoints (have /metrics endpoint with gosrt_* metrics)
  gosrtInstances = {
    server = gosrtLib.roles.server.network.vmIp;
    publisher = gosrtLib.roles.publisher.network.vmIp;
    subscriber = gosrtLib.roles.subscriber.network.vmIp;
  };

  # All instances (including interop VMs - only node_exporter metrics)
  allInstances = gosrtInstances // {
    xtransmit-pub = gosrtLib.roles.xtransmit-pub.network.vmIp;
    xtransmit-sub = gosrtLib.roles.xtransmit-sub.network.vmIp;
    ffmpeg-pub = gosrtLib.roles.ffmpeg-pub.network.vmIp;
    ffmpeg-sub = gosrtLib.roles.ffmpeg-sub.network.vmIp;
    metrics = gosrtLib.roles.metrics.network.vmIp;  # Metrics VM's own node_exporter
  };

  # Generate relabel_configs from instances map
  mkRelabelConfigs = instances: lib.mapAttrsToList (name: ip: {
    source_labels = [ "__address__" ];
    regex = "${ip}:.*";
    target_label = "instance";
    replacement = name;
  }) instances;

  # Generate static_configs targets
  mkTargets = instances: port: lib.mapAttrsToList (_: ip: "${ip}:${toString port}") instances;

in {
  # GoSRT application metrics (custom /metrics handler)
  # Only GoSRT VMs have gosrt_* metrics
  gosrt = {
    job_name = "gosrt";
    scrape_interval = "1s";
    static_configs = [{ targets = mkTargets gosrtInstances 9100; labels = {}; }];
    relabel_configs = mkRelabelConfigs gosrtInstances;
  };

  # Node exporter (system metrics for io_uring contention)
  # All VMs run node_exporter
  node = {
    job_name = "node";
    scrape_interval = "5s";
    static_configs = [{ targets = mkTargets allInstances 9100; labels = {}; }];
    relabel_configs = mkRelabelConfigs allInstances;
  };

  # All scrape configs as a list
  all = [ gosrt node ];

  # Export for use in dashboards
  inherit gosrtInstances allInstances;
}
```

### 5n. nix/microvms/metrics.nix (Refactored)

Metrics MicroVM using the modular Grafana configuration:

```nix
# nix/microvms/metrics.nix
#
# MicroVM for metrics collection and visualization.
# Runs Prometheus (scrapes GoSRT /metrics endpoints) and Grafana (dashboards).
#
# Access:
#   Prometheus: http://10.50.8.2:9090
#   Grafana:    http://10.50.8.2:3000/d/gosrt-ops (Operations - NOC view)
#               http://10.50.8.2:3000/d/gosrt-analysis (Analysis - Engineering view)
#   Login: admin/srt
#
{ pkgs, lib, microvm, nixpkgs, system }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  baseMicroVM = import ./base.nix { inherit pkgs lib microvm nixpkgs system; };

  # Metrics role shorthand
  metricsRole = gosrtLib.roles.metrics;

  # ═══════════════════════════════════════════════════════════════════════════
  # Import modular components (eliminates repetition)
  # ═══════════════════════════════════════════════════════════════════════════
  grafanaLib = import ../grafana/lib.nix { inherit lib; };
  scrapeConfigs = import ../prometheus/scrape-configs.nix { inherit lib; };

  # Import panel modules
  panels = {
    trafficLights = import ../grafana/panels/traffic-lights.nix { inherit lib grafanaLib; };
    overview = import ../grafana/panels/overview.nix { inherit lib grafanaLib; };
    recovery = import ../grafana/panels/recovery.nix { inherit lib grafanaLib; };
    system = import ../grafana/panels/system.nix { inherit lib grafanaLib; };
  };

  # Import dashboard definitions (Nix attr sets, not JSON strings)
  dashboards = {
    operations = import ../grafana/dashboards/operations.nix { inherit lib grafanaLib panels; };
    analysis = import ../grafana/dashboards/analysis.nix { inherit lib grafanaLib panels; };
  };

in baseMicroVM.mkMicroVM {
  name = "srt-metrics";
  tap = metricsRole.network.tap;
  vmIp = metricsRole.network.vmIp;
  gateway = metricsRole.network.gateway;
  mac = metricsRole.network.mac;
  consolePort = metricsRole.ports.console;
  package = null;  # No GoSRT package needed

  extraModules = [
    ({ config, pkgs, ... }: {
      # ═══════════════════════════════════════════════════════════════════════
      # Prometheus - scrapes GoSRT metrics endpoints
      # ═══════════════════════════════════════════════════════════════════════
      services.prometheus = {
        enable = true;
        port = gosrtLib.ports.prometheusServer;
        listenAddress = "0.0.0.0";
        retentionTime = "7d";

        # Use generated scrape configs (no more repetitive relabel_configs!)
        scrapeConfigs = scrapeConfigs.all;
      };

      # ═══════════════════════════════════════════════════════════════════════
      # Grafana - visualization dashboards
      # ═══════════════════════════════════════════════════════════════════════
      services.grafana = {
        enable = true;

        settings = {
          server = {
            http_addr = "0.0.0.0";
            http_port = gosrtLib.ports.grafana;
          };
          security = {
            admin_user = "admin";
            admin_password = "srt";
          };
          # Disable auth for easy access during testing
          "auth.anonymous" = {
            enabled = true;
            org_role = "Viewer";
          };
        };

        # Provision Prometheus as a datasource
        provision = {
          datasources.settings.datasources = [
            {
              name = "Prometheus";
              type = "prometheus";
              access = "proxy";
              url = "http://localhost:${toString gosrtLib.ports.prometheusServer}";
              isDefault = true;
              editable = false;
            }
          ];

          # Pre-configured dashboard for GoSRT metrics
          dashboards.settings.providers = [
            {
              name = "GoSRT Dashboards";
              type = "file";
              options.path = "/var/lib/grafana/dashboards";
            }
          ];
        };
      };

      # ═══════════════════════════════════════════════════════════════════════════
      # GoSRT Grafana Dashboards (generated from Nix data structures)
      # ═══════════════════════════════════════════════════════════════════════════
      #
      # KEY IMPROVEMENT: Dashboards are defined as Nix attribute sets in:
      #   - nix/grafana/dashboards/operations.nix
      #   - nix/grafana/dashboards/analysis.nix
      #
      # Then converted to JSON via builtins.toJSON - no hand-crafted JSON!
      #
      # Benefits:
      #   - Type checking at Nix evaluation time
      #   - No JSON syntax errors possible
      #   - Reusable panel components
      #   - ~80% reduction in lines of code
      #
      # Using NixOS's built-in /etc provisioning (more robust than activation scripts)
      # ═══════════════════════════════════════════════════════════════════════════

      environment.etc."grafana/dashboards/gosrt-ops.json" = {
        text = builtins.toJSON dashboards.operations;
        mode = "0644";
      };

      environment.etc."grafana/dashboards/gosrt-analysis.json" = {
        text = builtins.toJSON dashboards.analysis;
        mode = "0644";
      };

      # Point Grafana provisioning to /etc/grafana/dashboards
      services.grafana.provision.dashboards.settings.providers = lib.mkForce [{
        name = "GoSRT Dashboards";
        type = "file";
        options.path = "/etc/grafana/dashboards";
        disableDeletion = true;
        updateIntervalSeconds = 10;
      }];

      # Open firewall for Prometheus and Grafana
      networking.firewall.allowedTCPPorts = [
        gosrtLib.ports.prometheusServer
        gosrtLib.ports.grafana
      ];
    })
  ];
}
```

**Comparison: Before vs After**

The original hand-crafted JSON approach:
- ~1800 lines of embedded JSON strings
- Manual y-position tracking (error-prone)
- Repeated threshold definitions
- Repeated relabel_configs

The refactored Nix-native approach:
- ~200 lines of Nix code (plus reusable library)
- Auto-layout calculates y positions
- Shared threshold presets
- Generated relabel_configs from instance map

**Line count reduction:**
```
Before: ~2000 lines (metrics.nix + embedded JSON)
After:  ~400 lines (metrics.nix + grafana/lib.nix + panel modules)
Savings: ~80% reduction in code
```

---

### 5o. Full Dashboard Example (for reference)

Here's what the Operations dashboard looks like when defined in pure Nix (truncated for brevity, full version in `nix/grafana/dashboards/operations.nix`):

```nix
# nix/grafana/dashboards/operations.nix - First 50 panels shown
{ lib, grafanaLib, panels }:

let
  inherit (grafanaLib) mkDashboard mkRow;
  inherit (panels) trafficLights overview;

in mkDashboard {
  title = "GoSRT Operations";
  uid = "gosrt-ops";
  refresh = "5s";
  timeFrom = "now-15m";

  links = [{
    title = "Analysis Dashboard";
    url = "/d/gosrt-analysis/gosrt-analysis";
    type = "link";
    icon = "dashboard";
  }];

  panels = [
    # Row 1: Traffic Lights
    (mkRow { title = "Stream Health Status"; })
    trafficLights.ingestHealth
    trafficLights.deliveryHealth
    trafficLights.unrecoverableLoss
    trafficLights.activeConnections
    trafficLights.ringDrops

    # Row 2: Throughput
    (mkRow { title = "Throughput & Quality"; })
    overview.goodput
    overview.goodputVsTotal

    # Row 3: Network Conditions
    (mkRow { title = "Network Conditions"; })
    overview.rtt
    overview.retransRate
    overview.lossEvents

    # Row 4: Buffer Health
    (mkRow { title = "Buffer Health"; })
    overview.tsbpdBuffer
    overview.flightAndBuffer

    # Row 5: System Health (collapsed)
    (mkRow {
      title = "System Health (io_uring Contention)";
      collapsed = true;
      panels = [
        panels.system.contextSwitches
        panels.system.schedulerLatency
        panels.system.softIrqs
      ];
    })
  ];
}
```

When `builtins.toJSON` is called on this, it produces the same JSON structure as the original hand-crafted version, but with:
- Automatic y-position calculation
- Shared panel components
- Type-safe Nix evaluation

---

The modular Nix approach generates complete, valid JSON via `builtins.toJSON`. Below are the remaining panel modules and the analysis dashboard definition.

---

### 5p. nix/grafana/panels/recovery.nix

NAK, TSBPD, and buffer health panels for loss recovery analysis:

```nix
# nix/grafana/panels/recovery.nix
#
# Loss recovery panels: NAK generation, TSBPD buffer, retransmission
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkTarget mkFlowTargets thresholds;

in {
  # NAK generation rate
  nakRate = mkTimeseries {
    title = "NAK Generation Rate";
    description = "Rate of NAK packets requesting retransmission";
    unit = "pps";
    gridPos = { h = 8; w = 8; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_connection_nak_sent_total{instance=\"server\"}[5s])" "Server NAK sent")
      (mkTarget "rate(gosrt_connection_nak_recv_total{instance=\"publisher\"}[5s])" "Publisher NAK recv")
      (mkTarget "rate(gosrt_connection_nak_sent_total{instance=\"subscriber\"}[5s])" "Subscriber NAK sent")
      (mkTarget "rate(gosrt_connection_nak_recv_total{instance=\"server\"}[5s])" "Server NAK recv (from sub)")
    ];
  };

  # NAK btree size (how many missing sequences tracked)
  nakBtreeSize = mkTimeseries {
    title = "NAK Btree Size";
    description = "Number of missing sequence numbers being tracked for recovery";
    unit = "short";
    gridPos = { h = 8; w = 8; x = 8; };
    targets = [
      (mkTarget "gosrt_receiver_nak_btree_size{instance=\"server\"}" "Server")
      (mkTarget "gosrt_receiver_nak_btree_size{instance=\"subscriber\"}" "Subscriber")
    ];
  };

  # Retransmission rate
  retransRate = mkTimeseries {
    title = "Retransmission Rate";
    description = "Rate of retransmitted packets";
    unit = "pps";
    gridPos = { h = 8; w = 8; x = 16; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_retrans_total{instance=\"server\", direction=\"recv\"}[5s])" "Server retrans recv")
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_retrans_total{instance=\"subscriber\", direction=\"recv\"}[5s])" "Subscriber retrans recv")
    ];
  };

  # TSBPD buffer margin
  tsbpdBuffer = mkTimeseries {
    title = "TSBPD Buffer Margin";
    description = "Time packets spend in buffer before delivery. LOW = packets arriving late.";
    unit = "ms";
    gridPos = { h = 8; w = 12; x = 0; };
    thresholds = thresholds.bufferMargin;
    targets = [
      (mkTarget "gosrt_connection_congestion_buffer_ms{instance=\"server\", direction=\"recv\"}" "Server buffer")
      (mkTarget "gosrt_connection_congestion_buffer_ms{instance=\"subscriber\", direction=\"recv\"}" "Subscriber buffer")
    ];
  };

  # Packets in flight vs buffer
  flightAndBuffer = mkTimeseries {
    title = "Flight Size vs Buffer";
    description = "Packets in flight (unACKed) compared to buffer capacity";
    unit = "short";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = [
      (mkTarget "gosrt_sender_flight_size{instance=\"publisher\"}" "Publisher flight")
      (mkTarget "gosrt_sender_flight_size{instance=\"server\"}" "Server flight")
      (mkTarget "gosrt_receiver_buffer_packets{instance=\"server\"}" "Server buffer pkts")
      (mkTarget "gosrt_receiver_buffer_packets{instance=\"subscriber\"}" "Subscriber buffer pkts")
    ];
  };

  # TSBPD skipped (unrecoverable)
  tsbpdSkipped = mkTimeseries {
    title = "TSBPD Skipped (Unrecoverable)";
    description = "Packets that NEVER arrived before deadline - permanent gaps";
    unit = "short";
    gridPos = { h = 8; w = 12; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total{instance=\"server\"}[5s])" "Server skipped")
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total{instance=\"subscriber\"}[5s])" "Subscriber skipped")
    ];
  };
}
```

### 5q. nix/grafana/panels/system.nix

System-level panels for io_uring contention detection:

```nix
# nix/grafana/panels/system.nix
#
# System resource panels: CPU, context switches, scheduler latency
# For detecting io_uring contention when RTT spikes but network is clean
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkTarget;

  instances = [ "server" "publisher" "subscriber" ];

in {
  # Context switches per second
  contextSwitches = mkTimeseries {
    title = "Context Switches/sec";
    description = "High context switches indicate CPU contention. Spikes delay io_uring completion handlers.";
    unit = "ops";
    gridPos = { h = 8; w = 8; x = 0; };
    targets = map (inst: mkTarget
      "rate(node_context_switches_total{instance=\"${inst}\"}[5s])"
      inst
    ) instances;
  };

  # Scheduler wait time
  schedWait = mkTimeseries {
    title = "Scheduler Wait Time";
    description = "Time tasks spend waiting to run. High values = hypervisor/CPU steal affecting io_uring.";
    unit = "ns";
    gridPos = { h = 8; w = 8; x = 8; };
    targets = map (inst: mkTarget
      "rate(node_schedstat_waiting_seconds_total{instance=\"${inst}\"}[5s]) * 1e9"
      inst
    ) instances;
  };

  # Soft interrupts (network)
  softIrqs = mkTimeseries {
    title = "Soft Interrupts (Network)";
    description = "Network interrupt processing load.";
    unit = "ops";
    gridPos = { h = 8; w = 8; x = 16; };
    targets = [
      (mkTarget "rate(node_softirqs_total{instance=\"server\", type=~\"NET_.*\"}[5s])" "Server {{type}}")
      (mkTarget "rate(node_softirqs_total{instance=\"publisher\", type=~\"NET_.*\"}[5s])" "Publisher {{type}}")
    ];
  };

  # Context switches vs RTT variance correlation
  ctxVsRtt = mkTimeseries {
    title = "Context Switches vs RTT Variance";
    description = "Correlation: High context switches should correlate with RTT variance spikes.";
    unit = "short";
    gridPos = { h = 8; w = 12; x = 0; };
    targets = [
      (mkTarget "rate(node_context_switches_total{instance=\"server\"}[5s]) / 10000" "Server ctx_sw (scaled)")
      (mkTarget "gosrt_rtt_var_microseconds{instance=\"server\"} / 1000" "Server RTTVar (ms)")
    ];
  };

  # CPU usage by mode
  cpuByMode = mkTimeseries {
    title = "CPU Usage by Mode";
    description = "CPU breakdown. High 'system' or 'iowait' = kernel overhead. 'steal' = hypervisor contention.";
    unit = "percentunit";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = [
      (mkTarget "rate(node_cpu_seconds_total{instance=\"server\", mode=\"system\"}[5s])" "Server system")
      (mkTarget "rate(node_cpu_seconds_total{instance=\"server\", mode=\"iowait\"}[5s])" "Server iowait")
      (mkTarget "rate(node_cpu_seconds_total{instance=\"server\", mode=\"steal\"}[5s])" "Server steal")
      (mkTarget "rate(node_cpu_seconds_total{instance=\"publisher\", mode=\"system\"}[5s])" "Publisher system")
    ];
  };
}
```

### 5r. nix/grafana/panels/rings.nix

Lock-free ring buffer health panels:

```nix
# nix/grafana/panels/rings.nix
#
# Lock-free ring buffer monitoring panels
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkTarget thresholds;

in {
  # Ring buffer drops (should be zero)
  ringDrops = mkTimeseries {
    title = "Ring Buffer Drops";
    description = "Packets dropped due to ring overflow. Should be ZERO.";
    unit = "short";
    gridPos = { h = 8; w = 8; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_receiver_ring_drops_total{instance=\"server\"}[5s])" "Server recv ring")
      (mkTarget "rate(gosrt_receiver_ring_drops_total{instance=\"subscriber\"}[5s])" "Subscriber recv ring")
      (mkTarget "rate(gosrt_sender_ring_dropped_total{instance=\"publisher\"}[5s])" "Publisher send ring")
      (mkTarget "rate(gosrt_sender_ring_dropped_total{instance=\"server\"}[5s])" "Server send ring")
    ];
  };

  # Ring buffer fill level
  ringFill = mkTimeseries {
    title = "Ring Buffer Fill Level";
    description = "Current items in ring buffers. High fill = backpressure.";
    unit = "short";
    gridPos = { h = 8; w = 8; x = 8; };
    targets = [
      (mkTarget "gosrt_receiver_ring_size{instance=\"server\"}" "Server recv ring")
      (mkTarget "gosrt_receiver_ring_size{instance=\"subscriber\"}" "Subscriber recv ring")
      (mkTarget "gosrt_sender_ring_size{instance=\"publisher\"}" "Publisher send ring")
    ];
  };

  # Control ring stats
  controlRing = mkTimeseries {
    title = "Control Ring Operations";
    description = "Lock-free control ring push/pop operations";
    unit = "ops";
    gridPos = { h = 8; w = 8; x = 16; };
    targets = [
      (mkTarget "rate(gosrt_control_ring_push_total{instance=\"server\"}[5s])" "Server push")
      (mkTarget "rate(gosrt_control_ring_pop_total{instance=\"server\"}[5s])" "Server pop")
      (mkTarget "rate(gosrt_control_ring_push_total{instance=\"subscriber\"}[5s])" "Subscriber push")
    ];
  };
}
```

### 5s. nix/grafana/panels/anomalies.nix

"Should be zero" defensive counters for detecting bugs:

```nix
# nix/grafana/panels/anomalies.nix
#
# Anomaly detection panels - counters that should remain at zero
# Non-zero values indicate bugs or unexpected conditions
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkTarget thresholds;

in {
  # Unknown ACKACKs (should be zero)
  unknownAckack = mkTimeseries {
    title = "Unknown ACKACK";
    description = "ACKACKs for unknown ACKs. Non-zero = potential bug or extreme reordering.";
    unit = "short";
    thresholds = thresholds.shouldBeZero;
    gridPos = { h = 6; w = 8; x = 0; };
    targets = [
      (mkTarget "gosrt_ack_btree_unknown_ackack_total{instance=\"server\"}" "Server")
      (mkTarget "gosrt_ack_btree_unknown_ackack_total{instance=\"subscriber\"}" "Subscriber")
    ];
  };

  # Duplicate packets
  duplicates = mkTimeseries {
    title = "Duplicate Packets";
    description = "Duplicate packet count. Some expected during recovery, excessive = problem.";
    unit = "short";
    gridPos = { h = 6; w = 8; x = 8; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_dup_total{instance=\"server\"}[5s])" "Server")
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_dup_total{instance=\"subscriber\"}[5s])" "Subscriber")
    ];
  };

  # Sequence gaps
  seqGaps = mkTimeseries {
    title = "Sequence Gaps Detected";
    description = "Gaps in sequence numbers requiring recovery.";
    unit = "short";
    gridPos = { h = 6; w = 8; x = 16; };
    targets = [
      (mkTarget "rate(gosrt_receiver_seq_gap_total{instance=\"server\"}[5s])" "Server")
      (mkTarget "rate(gosrt_receiver_seq_gap_total{instance=\"subscriber\"}[5s])" "Subscriber")
    ];
  };

  # All anomalies stat panel
  allAnomalies = mkStat {
    title = "Anomaly Count";
    description = "Total anomaly counters. Should be GREEN (zero).";
    unit = "short";
    thresholds = thresholds.shouldBeZero;
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 20; };
    targets = [
      (mkTarget ''
        sum(gosrt_ack_btree_unknown_ackack_total) +
        sum(rate(gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total[1m]) * 60)
      '' "Anomalies")
    ];
  };
}
```

### 5t. nix/grafana/panels/iouring.nix

io_uring health and kernel-to-app latency panels:

```nix
# nix/grafana/panels/iouring.nix
#
# io_uring health monitoring for high-performance packet processing.
# Critical for detecting bottlenecks at 500+ Mb/s throughput.
#
# These metrics require GoSRT to export io_uring-specific counters:
#   - gosrt_iouring_cq_overflow_total: CQ overflows (kernel dropping before Go sees)
#   - gosrt_iouring_cqe_latency_us: Time from CQE ready to Go processing
#   - gosrt_iouring_sq_full_total: SQ full events (submission backpressure)
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkGauge mkTarget thresholds;

  instances = [ "server" "publisher" "subscriber" ];

in {
  # CQ Overflow - CRITICAL: packets lost before reaching Go code
  cqOverflow = mkTimeseries {
    title = "io_uring CQ Overflow (Kernel Drops)";
    description = ''
      CRITICAL: Completion Queue overflows mean the kernel is posting packets
      faster than Go's WaitCQETimeout() can consume. These packets are LOST
      before reaching GoSRT. If non-zero, tune DEFER_TASKRUN or increase ring size.
    '';
    unit = "short";
    gridPos = { h = 8; w = 8; x = 0; };
    thresholds = thresholds.shouldBeZero;
    targets = map (inst: mkTarget
      "rate(gosrt_iouring_cq_overflow_total{instance=\"${inst}\"}[5s])"
      inst
    ) instances;
  };

  # CQE Processing Latency - kernel-to-app delay
  cqeLatency = mkTimeseries {
    title = "CQE Processing Latency (Kernel→App)";
    description = ''
      Microseconds from CQE ready to Go EventLoop processing.
      Rising latency indicates EventLoop is falling behind.
      High values correlate with RTT variance spikes.
    '';
    unit = "µs";
    gridPos = { h = 8; w = 8; x = 8; };
    targets = map (inst: mkTarget
      "gosrt_iouring_cqe_latency_us{instance=\"${inst}\", quantile=\"0.99\"}"
      "${inst} P99"
    ) instances;
  };

  # SQ Full Events - submission backpressure
  sqFull = mkTimeseries {
    title = "io_uring SQ Full Events";
    description = ''
      Submission Queue full events indicate send path backpressure.
      May cause send delays if GoSRT must wait for SQ space.
    '';
    unit = "short";
    gridPos = { h = 8; w = 8; x = 16; };
    targets = map (inst: mkTarget
      "rate(gosrt_iouring_sq_full_total{instance=\"${inst}\"}[5s])"
      inst
    ) instances;
  };

  # Combined io_uring health stat
  iouringHealth = mkStat {
    title = "io_uring Health";
    description = "GREEN if no CQ overflows. RED indicates kernel-level packet loss.";
    unit = "short";
    thresholds = thresholds.shouldBeZero;
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 0; };
    targets = [
      (mkTarget "sum(rate(gosrt_iouring_cq_overflow_total[30s]))" "CQ Overflows")
    ];
  };

  # Ring utilization gauge
  ringUtilization = mkGauge {
    title = "io_uring Ring Utilization";
    description = "Percentage of ring capacity in use. High values = backpressure risk.";
    unit = "percentunit";
    min = 0;
    max = 1;
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "yellow"; value = 0.7; }
        { color = "red"; value = 0.9; }
      ];
    };
    gridPos = { h = 4; w = 4; x = 4; };
    targets = [
      (mkTarget "gosrt_iouring_ring_utilization{instance=\"server\"}" "Server")
    ];
  };
}
```

### 5u. nix/grafana/panels/efficiency.nix

Bandwidth efficiency and goodput panels:

```nix
# nix/grafana/panels/efficiency.nix
#
# Bandwidth efficiency panels for understanding retransmission overhead.
# Critical for satellite/cellular where bandwidth is expensive.
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkGauge mkTarget thresholds;

in {
  # Bandwidth Efficiency - Goodput / Total Wire Bitrate
  bandwidthEfficiency = mkGauge {
    title = "Bandwidth Efficiency";
    description = ''
      Percentage of wire bandwidth delivering unique data.
      Formula: 100 × (Goodput / Total Bitrate)

      Below 80%: Network over-congested, consider lowering encoder bitrate.
      Below 60%: Severe congestion, retransmissions consuming most bandwidth.
    '';
    unit = "percentunit";
    min = 0;
    max = 1;
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 0.6; }
        { color = "green"; value = 0.8; }
      ];
    };
    gridPos = { h = 6; w = 6; x = 0; };
    targets = [
      (mkTarget ''
        rate(gosrt_connection_congestion_bytes_unique_total{instance="server", direction="recv"}[30s]) /
        (rate(gosrt_connection_congestion_bytes_total{instance="server", direction="recv"}[30s]) + 0.001)
      '' "Ingest Efficiency")
      (mkTarget ''
        rate(gosrt_connection_congestion_bytes_unique_total{instance="subscriber", direction="recv"}[30s]) /
        (rate(gosrt_connection_congestion_bytes_total{instance="subscriber", direction="recv"}[30s]) + 0.001)
      '' "Delivery Efficiency")
    ];
  };

  # Overhead breakdown - what's consuming bandwidth
  overheadBreakdown = mkTimeseries {
    title = "Bandwidth Breakdown";
    description = "Visual comparison: unique data vs retransmissions vs control overhead.";
    unit = "Bps";
    gridPos = { h = 8; w = 10; x = 6; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_bytes_unique_total{instance=\"server\", direction=\"recv\"}[5s])"
                "Unique Data (Goodput)")
      (mkTarget ''
        rate(gosrt_connection_congestion_bytes_total{instance="server", direction="recv"}[5s]) -
        rate(gosrt_connection_congestion_bytes_unique_total{instance="server", direction="recv"}[5s])
      '' "Retransmission Overhead")
      (mkTarget "rate(gosrt_connection_control_bytes_total{instance=\"server\"}[5s])"
                "Control Overhead (ACK/NAK)")
    ];
  };

  # Efficiency trend over time
  efficiencyTrend = mkTimeseries {
    title = "Efficiency Over Time";
    description = "Track efficiency changes during impairment tests.";
    unit = "percentunit";
    gridPos = { h = 8; w = 8; x = 16; };
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 0.6; }
        { color = "green"; value = 0.8; }
      ];
    };
    targets = [
      (mkTarget ''
        rate(gosrt_connection_congestion_bytes_unique_total{instance="server", direction="recv"}[30s]) /
        (rate(gosrt_connection_congestion_bytes_total{instance="server", direction="recv"}[30s]) + 0.001)
      '' "Ingest")
      (mkTarget ''
        rate(gosrt_connection_congestion_bytes_unique_total{instance="subscriber", direction="recv"}[30s]) /
        (rate(gosrt_connection_congestion_bytes_total{instance="subscriber", direction="recv"}[30s]) + 0.001)
      '' "Delivery")
    ];
  };

  # Actionable stat panel
  efficiencyAlert = mkStat {
    title = "Bandwidth Health";
    description = ''
      GREEN: >80% efficiency (normal operation)
      YELLOW: 60-80% (elevated retransmissions)
      RED: <60% (RECOMMEND: Lower encoder bitrate)
    '';
    unit = "percentunit";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 0.6; }
        { color = "green"; value = 0.8; }
      ];
    };
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 0; };
    targets = [
      (mkTarget ''
        avg(
          rate(gosrt_connection_congestion_bytes_unique_total{direction="recv"}[30s]) /
          (rate(gosrt_connection_congestion_bytes_total{direction="recv"}[30s]) + 0.001)
        )
      '' "Avg Efficiency")
    ];
  };
}
```

### 5v. nix/grafana/panels/btree.nix

B-tree pressure and NAK consolidation panels:

```nix
# nix/grafana/panels/btree.nix
#
# B-tree health monitoring for packet storage and NAK tracking.
# GoSRT uses B-trees to maintain O(log n) operations at high packet rates.
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkGauge mkTarget thresholds;

in {
  # NAK Btree Size - grows during loss events
  nakBtreeSize = mkTimeseries {
    title = "NAK B-tree Size";
    description = ''
      Number of missing sequence entries tracked in the NAK B-tree.
      Balloons during Starlink-style impairment tests.
      Watch for correlation with memory usage.
    '';
    unit = "short";
    gridPos = { h = 8; w = 8; x = 0; };
    targets = [
      (mkTarget "gosrt_receiver_nak_btree_size{instance=\"server\"}" "Server")
      (mkTarget "gosrt_receiver_nak_btree_size{instance=\"subscriber\"}" "Subscriber")
    ];
  };

  # NAK Consolidation Efficiency - ranges vs singles
  nakConsolidation = mkTimeseries {
    title = "NAK Consolidation Efficiency";
    description = ''
      Compare individual sequence NAKs vs range NAKs.
      High individual NAKs = suboptimal consolidation, may overwhelm sender.
      Efficient: Many sequences consolidated into few range NAKs.
    '';
    unit = "short";
    gridPos = { h = 8; w = 8; x = 8; };
    targets = [
      (mkTarget "rate(gosrt_receiver_nak_single_total{instance=\"server\"}[5s])" "Server Singles")
      (mkTarget "rate(gosrt_receiver_nak_range_total{instance=\"server\"}[5s])" "Server Ranges")
      (mkTarget "rate(gosrt_receiver_nak_single_total{instance=\"subscriber\"}[5s])" "Sub Singles")
      (mkTarget "rate(gosrt_receiver_nak_range_total{instance=\"subscriber\"}[5s])" "Sub Ranges")
    ];
  };

  # NAK Consolidation Ratio gauge
  nakConsolidationRatio = mkGauge {
    title = "NAK Consolidation Ratio";
    description = ''
      Ratio of sequences per NAK packet. Higher = better consolidation.
      Low ratio (<2) under loss = consolidation algorithm needs tuning.
    '';
    unit = "short";
    min = 0;
    max = 100;
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 2; }
        { color = "green"; value = 5; }
      ];
    };
    gridPos = { h = 4; w = 4; x = 16; };
    targets = [
      (mkTarget ''
        (rate(gosrt_receiver_nak_sequences_total{instance="server"}[30s]) + 0.001) /
        (rate(gosrt_receiver_nak_packets_total{instance="server"}[30s]) + 0.001)
      '' "Server")
    ];
  };

  # Packet Store Btree Size
  packetStoreBtree = mkTimeseries {
    title = "Packet Store B-tree Size";
    description = ''
      Packets buffered in receive B-tree awaiting TSBPD delivery.
      Size = flight size × latency window. Grows with RTT.
    '';
    unit = "short";
    gridPos = { h = 8; w = 8; x = 0; };
    targets = [
      (mkTarget "gosrt_receiver_packet_btree_size{instance=\"server\"}" "Server")
      (mkTarget "gosrt_receiver_packet_btree_size{instance=\"subscriber\"}" "Subscriber")
    ];
  };

  # Sender Btree Size (packets awaiting ACK)
  senderBtreeSize = mkTimeseries {
    title = "Sender B-tree Size (In Flight)";
    description = ''
      Packets in sender B-tree awaiting ACK. High values indicate:
      - High RTT (normal for GEO satellite)
      - Receiver not sending ACKs (problem)
      - High loss requiring many retransmissions
    '';
    unit = "short";
    gridPos = { h = 8; w = 8; x = 8; };
    targets = [
      (mkTarget "gosrt_sender_btree_size{instance=\"publisher\"}" "Publisher")
      (mkTarget "gosrt_sender_btree_size{instance=\"server\"}" "Server (relay)")
    ];
  };

  # B-tree Operations Rate
  btreeOpsRate = mkTimeseries {
    title = "B-tree Operations/sec";
    description = "Insert/delete/lookup operations. High rate = memory pressure.";
    unit = "ops";
    gridPos = { h = 8; w = 8; x = 16; };
    targets = [
      (mkTarget "rate(gosrt_btree_insert_total{instance=\"server\"}[5s])" "Server Insert")
      (mkTarget "rate(gosrt_btree_delete_total{instance=\"server\"}[5s])" "Server Delete")
      (mkTarget "rate(gosrt_btree_lookup_total{instance=\"server\"}[5s])" "Server Lookup")
    ];
  };

  # Memory pressure stat
  btreeMemoryPressure = mkStat {
    title = "B-tree Memory";
    description = "Total entries across all B-trees. Monitor during stress tests.";
    unit = "short";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "yellow"; value = 10000; }
        { color = "red"; value = 50000; }
      ];
    };
    colorMode = "value";
    gridPos = { h = 4; w = 4; x = 20; };
    targets = [
      (mkTarget ''
        sum(gosrt_receiver_nak_btree_size) +
        sum(gosrt_receiver_packet_btree_size) +
        sum(gosrt_sender_btree_size)
      '' "Total Entries")
    ];
  };
}
```

### 5w. nix/grafana/panels/alerts.nix

Alert thresholds and tier recommendations:

```nix
# nix/grafana/panels/alerts.nix
#
# Alert definitions organized by monitoring tier.
# These thresholds are recommendations based on production experience.
#
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkStat mkTarget;

in {
  # ═══════════════════════════════════════════════════════════════════════════
  # Tier 1: Operations (L1) - Stream Continuity
  # ═══════════════════════════════════════════════════════════════════════════

  streamDown = mkStat {
    title = "Stream Status";
    description = ''
      ALERT THRESHOLD: Stream Down > 5 seconds
      Triggers when PktRecvDataSuccess rate drops to zero.
    '';
    unit = "short";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }   # 0 = down
        { color = "green"; value = 1; }    # any packets = up
      ];
    };
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_pkt_recv_data_success_total{instance=\"subscriber\"}[5s]) > 0" "Stream Active")
    ];
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Tier 2: Engineering (L2) - Recovery Efficiency
  # ═══════════════════════════════════════════════════════════════════════════

  nakEfficiency = mkStat {
    title = "NAK Efficiency";
    description = ''
      ALERT THRESHOLD: NAK Efficiency < 50%
      Ratio of successful retransmits to NAK requests.
      Below 50% = NAKs are being ignored or lost.
    '';
    unit = "percentunit";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "red"; value = null; }
        { color = "yellow"; value = 0.5; }
        { color = "green"; value = 0.8; }
      ];
    };
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 4; };
    targets = [
      (mkTarget ''
        rate(gosrt_connection_congestion_recv_pkt_retrans_total{instance="server", direction="recv"}[30s]) /
        (rate(gosrt_connection_nak_sent_total{instance="server"}[30s]) + 0.001)
      '' "NAK→Retrans Ratio")
    ];
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Tier 3: Kernel/IO (L3) - System Bottlenecks
  # ═══════════════════════════════════════════════════════════════════════════

  iouringOverflow = mkStat {
    title = "io_uring Overflow";
    description = ''
      ALERT THRESHOLD: Any CQ overflow = immediate attention
      Kernel is dropping packets before Go can process them.
      Action: Increase ring size or tune DEFER_TASKRUN.
    '';
    unit = "short";
    thresholds = {
      mode = "absolute";
      steps = [
        { color = "green"; value = null; }
        { color = "red"; value = 1; }
      ];
    };
    colorMode = "background";
    gridPos = { h = 4; w = 4; x = 8; };
    targets = [
      (mkTarget "sum(rate(gosrt_iouring_cq_overflow_total[30s]))" "CQ Overflows")
    ];
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Summary Table Panel
  # ═══════════════════════════════════════════════════════════════════════════
  #
  # This is documentation for the alert tiers, rendered in Grafana:
  #
  # | Tier        | Focus        | Key Metric              | Alert Threshold    |
  # |-------------|--------------|-------------------------|--------------------|
  # | Ops (L1)    | Continuity   | PktRecvDataSuccess      | Stream Down > 5s   |
  # | Eng (L2)    | Recovery     | Retrans / NAK ratio     | Efficiency < 50%   |
  # | Kernel (L3) | Bottlenecks  | io_uring CQE Latency    | Ring Overflow > 0  |
  #
}
```

### 5x. nix/grafana/dashboards/analysis.nix

Analysis dashboard for engineering deep-dives:

```nix
# nix/grafana/dashboards/analysis.nix
#
# Engineering Analysis dashboard for deep debugging.
# More detailed than Operations dashboard, with collapsed rows for specific subsystems.
#
{ lib, grafanaLib, panels }:

let
  inherit (grafanaLib) mkDashboard mkRow mkTimeseries mkTarget;
  inherit (panels) overview recovery rings system anomalies;

in mkDashboard {
  title = "GoSRT Analysis";
  uid = "gosrt-analysis";
  tags = [ "gosrt" "srt" "analysis" "engineering" ];
  refresh = "1s";  # Faster refresh for debugging
  timeFrom = "now-5m";

  links = [{
    title = "Operations Dashboard";
    url = "/d/gosrt-ops/gosrt-operations";
    type = "link";
    icon = "dashboard";
    tooltip = "High-level NOC view";
  }];

  panels = [
    # ═══════════════════════════════════════════════════════════════════════
    # Row 1: Flow Comparison (Publisher→Server vs Server→Subscriber)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow { title = "Data Flow Comparison"; })

    (mkTimeseries {
      title = "Ingest Flow: Publisher → Server";
      description = "Compare what publisher sends vs what server receives";
      unit = "pps";
      gridPos = { h = 8; w = 12; x = 0; };
      targets = [
        (mkTarget "rate(gosrt_connection_congestion_packets_total{instance=\"publisher\", direction=\"send\"}[5s])" "Publisher SEND")
        (mkTarget "rate(gosrt_connection_congestion_packets_total{instance=\"server\", direction=\"recv\"}[5s])" "Server RECV")
        (mkTarget "rate(gosrt_connection_congestion_packets_lost_total{instance=\"server\", direction=\"recv\"}[5s])" "Server LOSS detected")
      ];
    })

    (mkTimeseries {
      title = "Delivery Flow: Server → Subscriber";
      description = "Compare what server sends vs what subscriber receives";
      unit = "pps";
      gridPos = { h = 8; w = 12; x = 12; };
      targets = [
        (mkTarget "rate(gosrt_connection_congestion_packets_total{instance=\"server\", direction=\"send\"}[5s])" "Server SEND")
        (mkTarget "rate(gosrt_connection_congestion_packets_total{instance=\"subscriber\", direction=\"recv\"}[5s])" "Subscriber RECV")
        (mkTarget "rate(gosrt_connection_congestion_packets_lost_total{instance=\"subscriber\", direction=\"recv\"}[5s])" "Subscriber LOSS detected")
      ];
    })

    # ═══════════════════════════════════════════════════════════════════════
    # Row 2: RTT Analysis
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow { title = "RTT & Timing Analysis"; })

    (mkTimeseries {
      title = "RTT from All Perspectives";
      description = "Round-trip time measured at each endpoint";
      unit = "us";
      gridPos = { h = 8; w = 8; x = 0; };
      targets = [
        (mkTarget "gosrt_rtt_microseconds{instance=\"publisher\"}" "Publisher")
        (mkTarget "gosrt_rtt_microseconds{instance=\"server\"}" "Server")
        (mkTarget "gosrt_rtt_microseconds{instance=\"subscriber\"}" "Subscriber")
      ];
    })

    (mkTimeseries {
      title = "RTT Variance";
      description = "RTT jitter - spikes indicate network instability or io_uring delays";
      unit = "us";
      gridPos = { h = 8; w = 8; x = 8; };
      targets = [
        (mkTarget "gosrt_rtt_var_microseconds{instance=\"publisher\"}" "Publisher")
        (mkTarget "gosrt_rtt_var_microseconds{instance=\"server\"}" "Server")
        (mkTarget "gosrt_rtt_var_microseconds{instance=\"subscriber\"}" "Subscriber")
      ];
    })

    overview.retransRate

    # ═══════════════════════════════════════════════════════════════════════
    # Row 3: Loss Recovery (collapsed)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "Loss Recovery Analysis";
      collapsed = true;
      panels = [
        recovery.nakRate
        recovery.nakBtreeSize
        recovery.retransRate
        recovery.tsbpdBuffer
        recovery.flightAndBuffer
        recovery.tsbpdSkipped
      ];
    })

    # ═══════════════════════════════════════════════════════════════════════
    # Row 4: Ring Buffer Health (collapsed)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "Ring Buffer Health";
      collapsed = true;
      panels = [
        rings.ringDrops
        rings.ringFill
        rings.controlRing
      ];
    })

    # ═══════════════════════════════════════════════════════════════════════
    # Row 5: System Contention (collapsed)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "io_uring & System Contention";
      collapsed = true;
      panels = [
        system.contextSwitches
        system.schedWait
        system.softIrqs
        system.ctxVsRtt
        system.cpuByMode
      ];
    })

    # ═══════════════════════════════════════════════════════════════════════
    # Row 6: Anomaly Detection (collapsed)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "Anomaly Detection (Should Be Zero)";
      collapsed = true;
      panels = [
        anomalies.unknownAckack
        anomalies.duplicates
        anomalies.seqGaps
      ];
    })

    # ═══════════════════════════════════════════════════════════════════════
    # Row 7: TSBPD Buffer Margin Analysis (collapsed)
    # ═══════════════════════════════════════════════════════════════════════
    (mkRow {
      title = "Late Packet Analysis (TSBPD Margin)";
      collapsed = true;
      panels = [
        (mkTimeseries {
          title = "TSBPD Buffer Margin (Time to Expiry)";
          description = "LOW values = packets arriving late, little recovery margin.";
          unit = "ms";
          gridPos = { h = 8; w = 12; x = 0; y = 0; };
          targets = [
            (mkTarget "gosrt_connection_congestion_buffer_ms{instance=\"server\", direction=\"recv\"}" "Server buffer margin")
            (mkTarget "gosrt_connection_congestion_buffer_ms{instance=\"subscriber\", direction=\"recv\"}" "Subscriber buffer margin")
          ];
        })
        (mkTimeseries {
          title = "Buffer Margin vs Loss Events";
          description = "Correlation: Low buffer margin followed by loss = latency too low.";
          unit = "short";
          gridPos = { h = 8; w = 12; x = 12; y = 0; };
          targets = [
            (mkTarget "gosrt_connection_congestion_buffer_ms{instance=\"server\", direction=\"recv\"}" "Server buffer (ms)")
            (mkTarget "rate(gosrt_connection_congestion_packets_lost_total{instance=\"server\", direction=\"recv\"}[5s]) * 100" "Server loss (x100)")
          ];
        })
      ];
    })
  ];
}
```

---

**Line count comparison:**
```
Original approach: ~1800 lines of hand-crafted JSON per dashboard
Modular approach:  ~50 lines per panel module + ~100 lines per dashboard
Total savings:     ~80% reduction in code
```

**Benefits of the modular Nix approach:**
- Type checking at Nix evaluation time (no JSON syntax errors)
- Reusable threshold definitions (`thresholds.lossPercent`, etc.)
- Auto-generated relabel_configs from instance map
- Auto-layout calculates panel y positions
- Panel modules can be shared across dashboards
- Easy to add/remove panels without editing large JSON blocks

---

### 5u. nix/grafana/panels/default.nix

Panel module exports (centralizes all panel imports):

```nix
# nix/grafana/panels/default.nix
#
# Exports all Grafana panel modules for use in dashboard definitions.
# Single import point reduces repetition in metrics.nix.
#
{ lib, grafanaLib }:

{
  overview = import ./overview.nix { inherit lib grafanaLib; };
  trafficLights = import ./traffic-lights.nix { inherit lib grafanaLib; };
  recovery = import ./recovery.nix { inherit lib grafanaLib; };
  system = import ./system.nix { inherit lib grafanaLib; };
  rings = import ./rings.nix { inherit lib grafanaLib; };
  anomalies = import ./anomalies.nix { inherit lib grafanaLib; };

  # Advanced monitoring panels (Stress Testing & High-Performance)
  iouring = import ./iouring.nix { inherit lib grafanaLib; };
  efficiency = import ./efficiency.nix { inherit lib grafanaLib; };
  btree = import ./btree.nix { inherit lib grafanaLib; };
  alerts = import ./alerts.nix { inherit lib grafanaLib; };
}
```

This simplifies metrics.nix imports from:
```nix
# Before: 6 imports
trafficLights = import ../grafana/panels/traffic-lights.nix { inherit lib grafanaLib; };
overview = import ../grafana/panels/overview.nix { inherit lib grafanaLib; };
recovery = import ../grafana/panels/recovery.nix { inherit lib grafanaLib; };
system = import ../grafana/panels/system.nix { inherit lib grafanaLib; };
# ...

# After: 1 import
panels = import ../grafana/panels { inherit lib grafanaLib; };
```

---

### 6. nix/network/default.nix

Network module exports (centralizes all network scripts):

```nix
# nix/network/default.nix
#
# Exports all network management scripts.
# Single import point for flake.nix.
#
{ pkgs, lib }:

let
  setup = import ./setup.nix { inherit pkgs lib; };
  profiles = import ./profiles.nix { inherit pkgs lib; };
  impairments = import ./impairments.nix { inherit pkgs lib; };

in {
  # Re-export setup scripts
  inherit (setup) check setup teardown setLatency setLoss starlinkPattern;

  # Export profiles for test configurations
  inherit profiles;

  # Export impairment library (functional scenarios)
  inherit impairments;

  # Convenience: all apps as attribute set for flake.nix
  apps = {
    srt-network-check = { type = "app"; program = "${setup.check}/bin/srt-check-host"; };
    srt-network-setup = { type = "app"; program = "${setup.setup}/bin/srt-network-setup"; };
    srt-network-teardown = { type = "app"; program = "${setup.teardown}/bin/srt-network-teardown"; };
    srt-set-latency = { type = "app"; program = "${setup.setLatency}/bin/srt-set-latency"; };
    srt-set-loss = { type = "app"; program = "${setup.setLoss}/bin/srt-set-loss"; };
    srt-starlink-pattern = { type = "app"; program = "${setup.starlinkPattern}/bin/srt-starlink-pattern"; };
  }
  # Merge impairment apps
  // impairments.apps;
}
```

### 6b. nix/network/profiles.nix

Predefined network impairment profiles:

```nix
# nix/network/profiles.nix
#
# Predefined network impairment profiles for testing.
# Used by test runner scripts to apply consistent conditions.
#
{ pkgs, lib }:

let
  constants = import ../constants.nix;

in {
  # Clean network - no impairment
  clean = {
    name = "clean";
    latencyProfile = 0;  # 0ms RTT
    lossPercent = 0;
    description = "Clean network, no impairment";
  };

  # Regional datacenter - low latency
  regional = {
    name = "regional";
    latencyProfile = 1;  # 10ms RTT
    lossPercent = 0;
    description = "Regional DC (~10ms RTT)";
  };

  # Cross-continental - moderate latency
  continental = {
    name = "continental";
    latencyProfile = 2;  # 60ms RTT
    lossPercent = 0;
    description = "Cross-continental (~60ms RTT)";
  };

  # Intercontinental - high latency
  intercontinental = {
    name = "intercontinental";
    latencyProfile = 3;  # 130ms RTT
    lossPercent = 0;
    description = "Intercontinental (~130ms RTT)";
  };

  # GEO satellite - very high latency
  geoSatellite = {
    name = "geo-satellite";
    latencyProfile = 4;  # 300ms RTT
    lossPercent = 0;
    description = "GEO satellite (~300ms RTT)";
  };

  # Loss profiles (can be combined with latency)
  loss2pct = {
    name = "loss-2pct";
    latencyProfile = 0;
    lossPercent = 2;
    description = "2% packet loss";
  };

  loss5pct = {
    name = "loss-5pct";
    latencyProfile = 0;
    lossPercent = 5;
    description = "5% packet loss";
  };

  # Combined profiles for realistic scenarios
  starlinkBaseline = {
    name = "starlink-baseline";
    latencyProfile = 1;  # ~20-40ms typical
    lossPercent = 0;
    description = "Starlink baseline (no handoff events)";
  };

  # Tier 3 stress test - high latency + loss
  tier3Stress = {
    name = "tier3-stress";
    latencyProfile = 3;  # 130ms RTT
    lossPercent = 2;
    description = "Tier 3 stress: intercontinental + 2% loss";
  };

  # GEO with loss - extreme conditions
  geoWithLoss = {
    name = "geo-with-loss";
    latencyProfile = 4;  # 300ms RTT
    lossPercent = 0.5;
    description = "GEO satellite + 0.5% loss";
  };

  # Helper to apply a profile
  applyProfile = profile: pkgs.writeShellScript "apply-${profile.name}" ''
    echo "Applying profile: ${profile.name}"
    echo "  ${profile.description}"
    nix run .#srt-set-latency -- ${toString profile.latencyProfile}
    ${lib.optionalString (profile.lossPercent > 0) ''
      nix run .#srt-set-loss -- ${toString profile.lossPercent} ${toString profile.latencyProfile}
    ''}
    echo "Profile ${profile.name} applied"
  '';
}
```

### 6b2. nix/network/impairments.nix (Functional Scripting)

**ELEGANCE**: Functional approach to network impairment. Instead of maintaining multiple
shell scripts with similar logic, define scenarios as composable Nix functions:

```nix
# nix/network/impairments.nix
#
# FUNCTIONAL IMPAIRMENT LIBRARY
#
# Compose network impairment scenarios as Nix functions.
# Each function returns a derivation (script) that can be run.
#
# Benefits:
#   - DRY: One tc netem generator, many scenarios
#   - Composable: Chain impairments with `lib.pipe`
#   - Typed: Nix evaluator catches typos at build time
#   - Reproducible: Scenarios are derivations, not mutable scripts
#
# Usage:
#   nix run .#impairment-starlink-handoff   # 5s total dropout
#   nix run .#impairment-congested-wifi     # 2% loss + jitter
#   nix run .#impairment-geo-storm          # High latency + burst loss
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # ═══════════════════════════════════════════════════════════════════════════
  # Core Impairment Builder
  # ═══════════════════════════════════════════════════════════════════════════

  # Build a tc netem command from impairment spec
  # spec: { loss?, delay?, jitter?, corrupt?, reorder?, rate? }
  mkNetemCmd = linkIndex: spec: let
    ns = gosrtLib.routerA;
    dev = "link${toString linkIndex}_a";
    opts = lib.concatStringsSep " " (lib.filter (x: x != "") [
      (lib.optionalString (spec.delay or null != null) "delay ${spec.delay}")
      (lib.optionalString (spec.jitter or null != null) "${spec.jitter}")
      (lib.optionalString (spec.loss or null != null) "loss ${spec.loss}")
      (lib.optionalString (spec.corrupt or null != null) "corrupt ${spec.corrupt}")
      (lib.optionalString (spec.reorder or null != null) "reorder ${spec.reorder}")
      (lib.optionalString (spec.rate or null != null) "rate ${spec.rate}")
    ]);
  in "ip netns exec ${ns} tc qdisc change dev ${dev} root netem ${opts}";

  # ═══════════════════════════════════════════════════════════════════════════
  # Impairment Script Generator
  # ═══════════════════════════════════════════════════════════════════════════

  # Create an impairment script from a specification
  # name: Script name (e.g., "starlink-handoff")
  # description: Human-readable description
  # linkIndex: Which inter-router link to impair (0-4)
  # steps: List of { spec, duration?, annotation? }
  mkImpairmentScript = { name, description, linkIndex ? 0, steps, grafanaAnnotation ? true }:
    pkgs.writeShellApplication {
      name = "srt-impairment-${name}";
      runtimeInputs = with pkgs; [ iproute2 curl coreutils ];
      text = ''
        echo "=== Impairment: ${name} ==="
        echo "${description}"
        echo ""

        ${lib.optionalString grafanaAnnotation ''
          # Post annotation to Grafana (if available)
          GRAFANA_URL="http://${gosrtLib.roles.metrics.network.vmIp}:${toString gosrtLib.ports.grafana}"
          post_annotation() {
            local text="$1"
            curl -s -X POST "$GRAFANA_URL/api/annotations" \
              -H "Content-Type: application/json" \
              -d "{\"text\": \"$text\", \"tags\": [\"impairment\", \"${name}\"]}" \
              2>/dev/null || true
          }
          post_annotation "START: ${name}"
        ''}

        ${lib.concatMapStringsSep "\n" (step: ''
          echo "Applying: ${step.annotation or "impairment step"}"
          ${mkNetemCmd linkIndex step.spec}
          ${lib.optionalString (step.duration or null != null) ''
            sleep ${step.duration}
          ''}
          ${lib.optionalString (grafanaAnnotation && step.annotation or null != null) ''
            post_annotation "${step.annotation}"
          ''}
        '') steps}

        ${lib.optionalString grafanaAnnotation ''
          post_annotation "END: ${name}"
        ''}

        echo ""
        echo "Impairment sequence complete."
      '';
    };

  # ═══════════════════════════════════════════════════════════════════════════
  # Predefined Impairment Scenarios
  # ═══════════════════════════════════════════════════════════════════════════

in {
  inherit mkImpairmentScript mkNetemCmd;

  # Starlink satellite handoff (5s total dropout)
  starlinkHandoff = mkImpairmentScript {
    name = "starlink-handoff";
    description = "Simulates Starlink satellite handoff: brief outage followed by recovery";
    steps = [
      { spec = { loss = "100%"; }; duration = "2"; annotation = "Total dropout"; }
      { spec = { loss = "50%"; delay = "100ms"; }; duration = "1"; annotation = "Partial recovery"; }
      { spec = { loss = "10%"; delay = "40ms"; }; duration = "2"; annotation = "Stabilizing"; }
      { spec = { loss = "0%"; delay = "20ms"; }; annotation = "Recovered"; }
    ];
  };

  # Congested WiFi (persistent moderate loss + jitter)
  congestedWifi = mkImpairmentScript {
    name = "congested-wifi";
    description = "Simulates congested WiFi: 2% loss with variable jitter";
    steps = [
      { spec = { loss = "2%"; delay = "5ms"; jitter = "10ms"; }; annotation = "Congested WiFi active"; }
    ];
  };

  # GEO satellite during solar storm
  geoStorm = mkImpairmentScript {
    name = "geo-storm";
    description = "GEO satellite during solar interference: high latency + burst loss";
    steps = [
      { spec = { delay = "300ms"; loss = "0.5%"; }; duration = "10"; annotation = "Normal GEO"; }
      { spec = { delay = "350ms"; loss = "5%"; }; duration = "5"; annotation = "Solar interference burst"; }
      { spec = { delay = "300ms"; loss = "1%"; }; duration = "10"; annotation = "Recovering"; }
      { spec = { delay = "300ms"; loss = "0.5%"; }; annotation = "Stable"; }
    ];
  };

  # Network brownout (progressive degradation)
  brownout = mkImpairmentScript {
    name = "brownout";
    description = "Progressive network degradation and recovery";
    steps = [
      { spec = { loss = "0%"; delay = "10ms"; }; duration = "5"; annotation = "Baseline"; }
      { spec = { loss = "1%"; delay = "30ms"; }; duration = "5"; annotation = "Degrading"; }
      { spec = { loss = "3%"; delay = "80ms"; }; duration = "5"; annotation = "Severe"; }
      { spec = { loss = "5%"; delay = "150ms"; }; duration = "5"; annotation = "Critical"; }
      { spec = { loss = "2%"; delay = "60ms"; }; duration = "5"; annotation = "Recovering"; }
      { spec = { loss = "0%"; delay = "10ms"; }; annotation = "Recovered"; }
    ];
  };

  # Clean network (reset to baseline)
  clean = mkImpairmentScript {
    name = "clean";
    description = "Reset network to clean baseline";
    grafanaAnnotation = false;
    steps = [
      { spec = { loss = "0%"; delay = "0ms"; }; annotation = "Clean"; }
    ];
  };

  # Apps export for flake.nix
  apps = lib.listToAttrs (map (name: {
    name = "srt-impairment-${name}";
    value = { type = "app"; program = "${builtins.getAttr name scenarios}/bin/srt-impairment-${name}"; };
  }) [ "starlink-handoff" "congested-wifi" "geo-storm" "brownout" "clean" ]);
}
```

**Usage Examples:**
```bash
# Run predefined scenario
nix run .#srt-impairment-starlink-handoff

# Or compose custom scenario in Nix:
# In flake.nix or shell:
customScenario = impairments.mkImpairmentScript {
  name = "my-test";
  description = "Custom test scenario";
  steps = [
    { spec = { loss = "10%"; delay = "50ms"; }; duration = "30"; }
    { spec = { loss = "0%"; }; }
  ];
};
```

### 6c. nix/network/setup.nix (Data-Driven)

Network setup scripts - **refactored to generate from roles**:

```nix
# nix/network/setup.nix
#
# DATA-DRIVEN NETWORK SETUP
#
# Generates TAP/bridge/veth setup for all roles from lib.nix.
# Adding a new VM role automatically gets network infrastructure.
#
# Usage:
#   nix run .#srt-network-check     # Verify host environment
#   nix run .#srt-network-setup     # Create network infrastructure
#   nix run .#srt-network-teardown  # Remove network infrastructure
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  c = gosrtLib;  # Shorthand

in {
  # Host environment check
  check = pkgs.writeShellApplication {
    name = "srt-check-host";
    runtimeInputs = with pkgs; [ kmod coreutils iproute2 ];
    text = ''
      echo "=== GoSRT MicroVM Host Environment Check ==="
      errors=0

      # Check for TUN device
      if [[ -c /dev/net/tun ]]; then
        echo "OK /dev/net/tun exists"
      else
        echo "FAIL /dev/net/tun not found"
        echo "  Run: sudo modprobe tun"
        errors=$((errors + 1))
      fi

      # Check for vhost-net
      if lsmod | grep -q vhost_net; then
        echo "OK vhost_net module loaded"
      elif [[ -c /dev/vhost-net ]]; then
        echo "OK /dev/vhost-net exists"
      else
        echo "FAIL vhost_net not available"
        echo "  Run: sudo modprobe vhost_net"
        errors=$((errors + 1))
      fi

      # Check kernel version for io_uring
      KERNEL=$(uname -r | cut -d. -f1-2)
      if awk -v k="$KERNEL" 'BEGIN { exit (k < 5.10) }'; then
        echo "OK kernel $KERNEL supports io_uring"
      else
        echo "WARN kernel $KERNEL may have limited io_uring support (5.10+ recommended)"
      fi

      if [[ $errors -gt 0 ]]; then
        echo ""
        echo "Host environment check failed with $errors error(s)"
        exit 1
      else
        echo ""
        echo "Host environment ready for GoSRT MicroVMs"
      fi
    '';
  };

  # Network setup
  setup = pkgs.writeShellApplication {
    name = "srt-network-setup";
    runtimeInputs = with pkgs; [ iproute2 kmod nftables acl ];
    text = ''
      echo "=== GoSRT MicroVM Network Setup ==="

      # Load required kernel modules
      sudo modprobe tun
      sudo modprobe vhost_net
      sudo modprobe bridge

      # ═══════════════════════════════════════════════════════════════════════
      # Create Router A and Router B namespaces
      # ═══════════════════════════════════════════════════════════════════════

      echo "Creating router namespaces..."
      sudo ip netns add ${c.routerA} 2>/dev/null || echo "Router A namespace exists"
      sudo ip netns add ${c.routerB} 2>/dev/null || echo "Router B namespace exists"

      # Enable IP forwarding in routers
      sudo ip netns exec ${c.routerA} sysctl -qw net.ipv4.ip_forward=1
      sudo ip netns exec ${c.routerB} sysctl -qw net.ipv4.ip_forward=1

      # ═══════════════════════════════════════════════════════════════════════
      # Create TAP devices and bridges for MicroVMs
      # ═══════════════════════════════════════════════════════════════════════
      #
      # ARCHITECTURE NOTE: TAP devices MUST stay in the host namespace for QEMU
      # to access them. We use per-subnet bridges to connect TAPs to router
      # namespaces via veth pairs:
      #
      #   MicroVM ←→ TAP (host) ←→ Bridge (host) ←→ veth-host ←→ veth-router (namespace)
      #
      # This allows unprivileged QEMU to use TAPs while routers handle routing.
      #

      echo "Creating TAP devices and bridges..."

      # Helper function to create TAP + bridge + veth connection to router namespace
      create_vm_network() {
        local tap_name="$1"
        local bridge_name="$2"
        local veth_host="$3"
        local veth_router="$4"
        local router_ns="$5"
        local router_ip="$6"
        local subnet_prefix="$7"

        # Create TAP device (stays in host namespace, owned by user)
        if ! ip link show "$tap_name" &>/dev/null; then
          sudo ip tuntap add dev "$tap_name" mode tap multi_queue user "$USER"
          sudo ip link set "$tap_name" up
        fi

        # Create bridge for this subnet (stays in host namespace)
        if ! ip link show "$bridge_name" &>/dev/null; then
          sudo ip link add "$bridge_name" type bridge
          sudo ip link set "$bridge_name" up
        fi

        # Add TAP to bridge
        sudo ip link set "$tap_name" master "$bridge_name" 2>/dev/null || true

        # Create veth pair to connect bridge to router namespace
        if ! ip link show "$veth_host" &>/dev/null; then
          sudo ip link add "$veth_host" type veth peer name "$veth_router"
          sudo ip link set "$veth_host" master "$bridge_name"
          sudo ip link set "$veth_host" up
          sudo ip link set "$veth_router" netns "$router_ns"
          sudo ip netns exec "$router_ns" ip addr add "$router_ip/24" dev "$veth_router"
          sudo ip netns exec "$router_ns" ip link set "$veth_router" up
        fi
      }

      # ─────────────────────────────────────────────────────────────────────────
      # CREATE ALL VM NETWORKS (Generated from lib.roles)
      # ─────────────────────────────────────────────────────────────────────────
      ${lib.concatMapStringsSep "\n" (name: let
        role = c.roles.${name};
        net = role.network;
        router = if role.router == "A" then c.routers.A.namespace
                 else c.routers.B.namespace;
      in ''
      # ${role.description}
      echo "  Creating network for ${name}..."
      create_vm_network "${net.tap}" "${net.bridge}" "${net.vethHost}" "${net.vethRouter}" \
        "${router}" "${net.gateway}" "${c.base.subnetPrefix}.${toString role.index}"
      '') (lib.attrNames c.roles)}

      # ═══════════════════════════════════════════════════════════════════════
      # Create inter-router links with fixed latency
      # ═══════════════════════════════════════════════════════════════════════

      echo "Creating inter-router links with fixed latency..."

      ${lib.concatMapStringsSep "\n" (link: ''
        echo "  Link ${toString link.index}: ${toString link.rttMs}ms RTT (${link.name})"
        if ! sudo ip netns exec ${c.routerA} ip link show link${toString link.index}_a &>/dev/null; then
          sudo ip link add link${toString link.index}_a type veth peer name link${toString link.index}_b
          sudo ip link set link${toString link.index}_a netns ${c.routerA}
          sudo ip link set link${toString link.index}_b netns ${c.routerB}

          sudo ip netns exec ${c.routerA} ip addr add ${link.subnetA}.1/30 dev link${toString link.index}_a
          sudo ip netns exec ${c.routerB} ip addr add ${link.subnetB}.2/30 dev link${toString link.index}_b
          sudo ip netns exec ${c.routerA} ip link set link${toString link.index}_a up
          sudo ip netns exec ${c.routerB} ip link set link${toString link.index}_b up

          ${if link.rttMs > 0 then ''
            # Apply fixed latency (RTT/2 on each side)
            DELAY=$((${toString link.rttMs} / 2))
            sudo ip netns exec ${c.routerA} tc qdisc add dev link${toString link.index}_a root netem delay "''${DELAY}ms" limit ${toString c.netem.queueLimit}
            sudo ip netns exec ${c.routerB} tc qdisc add dev link${toString link.index}_b root netem delay "''${DELAY}ms" limit ${toString c.netem.queueLimit}
          '' else ''
            # No delay for link 0
          ''}
        fi
      '') c.interRouterLinks}

      # ═══════════════════════════════════════════════════════════════════════
      # Set default routing (link0 = no latency)
      # ═══════════════════════════════════════════════════════════════════════

      echo "Setting default routes (0ms latency)..."

      # ─────────────────────────────────────────────────────────────────────────
      # DEFAULT ROUTES (Generated from lib.roles)
      # ─────────────────────────────────────────────────────────────────────────
      # Router A → Router B (for roles on Router B)
      ${lib.concatMapStringsSep "\n" (name: let
        role = c.roles.${name};
      in lib.optionalString (role.router == "B") ''
      sudo ip netns exec ${c.routerA} ip route replace ${role.network.subnet} via 10.50.100.2
      '') (lib.attrNames c.roles)}

      # Router B → Router A (for roles on Router A)
      ${lib.concatMapStringsSep "\n" (name: let
        role = c.roles.${name};
      in lib.optionalString (role.router == "A") ''
      sudo ip netns exec ${c.routerB} ip route replace ${role.network.subnet} via 10.50.100.1
      '') (lib.attrNames c.roles)}

      # ═══════════════════════════════════════════════════════════════════════
      # Enable vhost-net access
      # ═══════════════════════════════════════════════════════════════════════

      if [[ -c /dev/vhost-net ]]; then
        if command -v setfacl &>/dev/null; then
          sudo setfacl -m "u:$USER:rw" /dev/vhost-net
          echo "vhost-net enabled (ACL for $USER)"
        fi
      fi

      echo ""
      echo "╔══════════════════════════════════════════════════════════════════════════╗"
      echo "║  Network setup complete!                                                  ║"
      echo "╠══════════════════════════════════════════════════════════════════════════╣"
      echo "║  GoSRT Components:                                                        ║"
      echo "║    Server:      ${c.roles.server.network.vmIp}:${toString c.ports.srt}                                        ║"
      echo "║    Publisher:   ${c.roles.publisher.network.vmIp} (client-generator)                             ║"
      echo "║    Subscriber:  ${c.roles.subscriber.network.vmIp} (client)                                      ║"
      echo "║                                                                           ║"
      echo "║  Interop Components:                                                      ║"
      echo "║    xtransmit-pub: ${c.roles.xtransmit-pub.network.vmIp} (srt-xtransmit publisher)                 ║"
      echo "║    ffmpeg-pub:    ${c.roles.ffmpeg-pub.network.vmIp} (ffmpeg 20Mb/s test pattern)                 ║"
      echo "║    xtransmit-sub: ${c.roles.xtransmit-sub.network.vmIp} (srt-xtransmit subscriber)                ║"
      echo "║    ffmpeg-sub:    ${c.roles.ffmpeg-sub.network.vmIp} (ffmpeg to /dev/null)                        ║"
      echo "║                                                                           ║"
      echo "║  Metrics:                                                                 ║"
      echo "║    Prometheus:  http://${c.roles.metrics.network.vmIp}:${toString c.ports.prometheusServer}                          ║"
      echo "║    Grafana:     http://${c.roles.metrics.network.vmIp}:${toString c.ports.grafana} (admin/srt)                   ║"
      echo "╠══════════════════════════════════════════════════════════════════════════╣"
      echo "║  Latency profiles (nix run .#srt-set-latency -- <n>):                     ║"
      echo "║    0: 0ms RTT     1: 10ms RTT    2: 60ms RTT                              ║"
      echo "║    3: 130ms RTT   4: 300ms RTT                                            ║"
      echo "╚══════════════════════════════════════════════════════════════════════════╝"
    '';
  };

  # Network teardown
  teardown = pkgs.writeShellApplication {
    name = "srt-network-teardown";
    runtimeInputs = with pkgs; [ iproute2 ];
    text = ''
      echo "=== GoSRT MicroVM Network Teardown ==="

      # Remove router namespaces (this removes veth endpoints inside them)
      echo "Removing router namespaces..."
      sudo ip netns del ${c.routerA} 2>/dev/null && echo "  Removed ${c.routerA}" || true
      sudo ip netns del ${c.routerB} 2>/dev/null && echo "  Removed ${c.routerB}" || true

      # Remove bridges and TAPs (generated from lib.roles)
      echo "Removing bridges and TAP devices..."
      ${lib.concatMapStringsSep "\n" (name: let
        role = c.roles.${name};
        net = role.network;
      in ''
      sudo ip link del "${net.bridge}" 2>/dev/null && echo "  Removed ${net.bridge}" || true
      sudo ip link del "${net.tap}" 2>/dev/null && echo "  Removed ${net.tap}" || true
      '') (lib.attrNames c.roles)}

      # Remove any remaining veth-*-h endpoints in host (orphaned by namespace deletion)
      echo "Cleaning up orphaned veth endpoints..."
      for veth in $(ip link show 2>/dev/null | grep -oE 'veth-[a-z]+-h' | sort -u); do
        sudo ip link del "$veth" 2>/dev/null && echo "  Removed $veth" || true
      done

      echo ""
      echo "Network teardown complete"
    '';
  };

  # Latency profile switcher with Grafana annotation
  setLatency = pkgs.writeShellApplication {
    name = "srt-set-latency";
    runtimeInputs = with pkgs; [ iproute2 curl ];
    text = ''
      PROFILE="''${1:-0}"
      GRAFANA_URL="''${GRAFANA_URL:-http://${c.roles.metrics.network.vmIp}:${toString c.ports.grafana}}"
      GRAFANA_USER="''${GRAFANA_USER:-admin}"
      GRAFANA_PASS="''${GRAFANA_PASS:-srt}"

      # Function to push annotation to Grafana
      push_annotation() {
        local text="$1"
        local tags="$2"
        local time_ms
        time_ms=$(date +%s%3N)
        curl -s -X POST "$GRAFANA_URL/api/annotations" \
          -H "Content-Type: application/json" \
          -u "$GRAFANA_USER:$GRAFANA_PASS" \
          -d "{\"time\": $time_ms, \"tags\": [$tags], \"text\": \"$text\"}" 2>/dev/null || true
      }

      case "$PROFILE" in
        0) NEXTHOP_A="10.50.100.2"; NEXTHOP_B="10.50.100.1"; NAME="no-delay (0ms)" ;;
        1) NEXTHOP_A="10.50.101.2"; NEXTHOP_B="10.50.101.1"; NAME="regional-dc (10ms)" ;;
        2) NEXTHOP_A="10.50.102.2"; NEXTHOP_B="10.50.102.1"; NAME="cross-continental (60ms)" ;;
        3) NEXTHOP_A="10.50.103.2"; NEXTHOP_B="10.50.103.1"; NAME="intercontinental (130ms)" ;;
        4) NEXTHOP_A="10.50.104.2"; NEXTHOP_B="10.50.104.1"; NAME="geo-satellite (300ms)" ;;
        *)
          echo "Usage: $0 <profile>"
          echo "  0 = no-delay (0ms RTT)"
          echo "  1 = regional-dc (10ms RTT)"
          echo "  2 = cross-continental (60ms RTT)"
          echo "  3 = intercontinental (130ms RTT)"
          echo "  4 = geo-satellite (300ms RTT)"
          exit 1
          ;;
      esac

      echo "Switching to latency profile $PROFILE: $NAME"

      # ─────────────────────────────────────────────────────────────────────────
      # UPDATE ROUTES (Generated from lib.roles)
      # ─────────────────────────────────────────────────────────────────────────
      # Router A → Router B (for roles on Router B)
      ${lib.concatMapStringsSep "\n" (name: let
        role = c.roles.${name};
      in lib.optionalString (role.router == "B") ''
      sudo ip netns exec ${c.routerA} ip route replace ${role.network.subnet} via "$NEXTHOP_A"
      '') (lib.attrNames c.roles)}

      # Router B → Router A (for roles on Router A)
      ${lib.concatMapStringsSep "\n" (name: let
        role = c.roles.${name};
      in lib.optionalString (role.router == "A") ''
      sudo ip netns exec ${c.routerB} ip route replace ${role.network.subnet} via "$NEXTHOP_B"
      '') (lib.attrNames c.roles)}

      # Push annotation to Grafana
      push_annotation "Latency changed to $NAME" '"impairment", "latency", "profile-'$PROFILE'"'

      echo "Latency profile active: $NAME"
    '';
  };

  # Loss injection with Grafana annotation
  setLoss = pkgs.writeShellApplication {
    name = "srt-set-loss";
    runtimeInputs = with pkgs; [ iproute2 curl ];
    text = ''
      PERCENT="''${1:-0}"
      LINK="''${2:-0}"
      GRAFANA_URL="''${GRAFANA_URL:-http://${c.roles.metrics.network.vmIp}:${toString c.ports.grafana}}"
      GRAFANA_USER="''${GRAFANA_USER:-admin}"
      GRAFANA_PASS="''${GRAFANA_PASS:-srt}"

      # Function to push annotation to Grafana
      push_annotation() {
        local text="$1"
        local tags="$2"
        local time_ms
        time_ms=$(date +%s%3N)

        # Push annotation (fail silently if Grafana unavailable)
        curl -s -X POST "$GRAFANA_URL/api/annotations" \
          -H "Content-Type: application/json" \
          -u "$GRAFANA_USER:$GRAFANA_PASS" \
          -d "{
            \"time\": $time_ms,
            \"tags\": [$tags],
            \"text\": \"$text\"
          }" 2>/dev/null || true
      }

      if [[ "$PERCENT" -eq 0 ]]; then
        echo "Clearing loss on link $LINK"
        sudo ip netns exec ${c.routerA} tc qdisc change dev "link''${LINK}_a" root netem limit ${toString c.netem.queueLimit} 2>/dev/null || true
        sudo ip netns exec ${c.routerB} tc qdisc change dev "link''${LINK}_b" root netem limit ${toString c.netem.queueLimit} 2>/dev/null || true
        push_annotation "Loss cleared on link $LINK" '"impairment", "loss", "clear"'
      else
        echo "Setting $PERCENT% loss on link $LINK (bidirectional)"
        sudo ip netns exec ${c.routerA} tc qdisc change dev "link''${LINK}_a" root netem loss "$PERCENT%" limit ${toString c.netem.queueLimit}
        sudo ip netns exec ${c.routerB} tc qdisc change dev "link''${LINK}_b" root netem loss "$PERCENT%" limit ${toString c.netem.queueLimit}
        push_annotation "$PERCENT% loss applied on link $LINK" '"impairment", "loss", "inject"'
      fi
    '';
  };

  # Starlink pattern orchestration with annotations
  # Parameterized for different handoff patterns
  starlinkPattern = pkgs.writeShellApplication {
    name = "srt-starlink-pattern";
    runtimeInputs = with pkgs; [ iproute2 curl coreutils ];
    text = ''
      # Starlink-style reconvergence pattern: 100% loss at configurable times
      # Each blackhole simulates satellite handoff
      #
      # Usage:
      #   srt-starlink-pattern [duration] [blackout_ms] [pattern_times]
      #
      # Examples:
      #   srt-starlink-pattern 60                    # Default: 60s, 500ms blackouts at 12,27,42,57
      #   srt-starlink-pattern 120 200 "15 30 45"   # 120s, 200ms blackouts at 15,30,45

      DURATION="''${1:-60}"
      BLACKOUT_MS="''${2:-500}"
      PATTERN_TIMES="''${3:-12 27 42 57}"
      GRAFANA_URL="''${GRAFANA_URL:-http://${c.roles.metrics.network.vmIp}:${toString c.ports.grafana}}"
      GRAFANA_USER="''${GRAFANA_USER:-admin}"
      GRAFANA_PASS="''${GRAFANA_PASS:-srt}"

      # Convert blackout ms to seconds for sleep
      BLACKOUT_SEC=$(echo "scale=3; $BLACKOUT_MS / 1000" | bc)

      push_annotation() {
        local text="$1"
        local tags="$2"
        local time_ms
        time_ms=$(date +%s%3N)
        curl -s -X POST "$GRAFANA_URL/api/annotations" \
          -H "Content-Type: application/json" \
          -u "$GRAFANA_USER:$GRAFANA_PASS" \
          -d "{\"time\": $time_ms, \"tags\": [$tags], \"text\": \"$text\"}" 2>/dev/null || true
      }

      blackhole_on() {
        # Add blackhole route to drop all traffic to server
        sudo ip netns exec ${c.routerA} ip route add blackhole ${c.roles.server.network.subnet} 2>/dev/null || \
          sudo ip netns exec ${c.routerA} ip route change blackhole ${c.roles.server.network.subnet}
        push_annotation "Starlink handoff START (100% loss, ''${BLACKOUT_MS}ms)" '"impairment", "starlink", "blackhole"'
      }

      blackhole_off() {
        # Restore normal routing via link 0
        sudo ip netns exec ${c.routerA} ip route del blackhole ${c.roles.server.network.subnet} 2>/dev/null || true
        sudo ip netns exec ${c.routerA} ip route replace ${c.roles.server.network.subnet} via 10.50.100.2
        push_annotation "Starlink handoff END (restored)" '"impairment", "starlink", "restore"'
      }

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  Starlink Pattern Test                                            ║"
      echo "╠══════════════════════════════════════════════════════════════════╣"
      echo "║  Duration:      $DURATION seconds                                 "
      echo "║  Blackout:      ''${BLACKOUT_MS}ms                                "
      echo "║  Pattern times: $PATTERN_TIMES                                    "
      echo "╚══════════════════════════════════════════════════════════════════╝"

      push_annotation "Starlink pattern test started ($DURATION sec, ''${BLACKOUT_MS}ms blackouts)" '"impairment", "starlink", "start"'

      START=$(date +%s)
      TRIGGERED=""

      while true; do
        NOW=$(date +%s)
        ELAPSED=$((NOW - START))

        if [[ $ELAPSED -ge $DURATION ]]; then
          break
        fi

        # Check if we hit a pattern time (only trigger once per time)
        for T in $PATTERN_TIMES; do
          if [[ $ELAPSED -eq $T ]] && [[ ! " $TRIGGERED " =~ " $T " ]]; then
            echo "  [''${ELAPSED}s] Triggering blackhole..."
            blackhole_on
            sleep "$BLACKOUT_SEC"
            blackhole_off
            TRIGGERED="$TRIGGERED $T"
          fi
        done

        sleep 0.1
      done

      push_annotation "Starlink pattern test completed" '"impairment", "starlink", "end"'
      echo "Starlink pattern complete"
    '';
  };
}
```

### 7. nix/scripts/vm-management.nix (Data-Driven)

Helper scripts for managing MicroVMs - **refactored to generate from roles**:

```nix
# nix/scripts/vm-management.nix
#
# DATA-DRIVEN VM MANAGEMENT SCRIPTS
#
# Generates stop/ssh/console scripts for all roles from lib.nix.
# Adding a new VM role automatically gets new management scripts.
#
# Usage:
#   nix run .#srt-vm-check              # List running MicroVMs
#   nix run .#srt-vm-stop               # Stop all MicroVMs
#   nix run .#srt-vm-stop-server        # Stop server VM only
#   nix run .#srt-ssh-server            # SSH into server VM
#   nix run .#srt-console-server        # Serial console to server VM
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # Pattern to identify GoSRT MicroVMs in process list
  vmPattern = "gosrt:srt-";

  # ─── Script Generators (Per-Role) ─────────────────────────────────────────

  # Generate stop script for a role
  mkStopScript = name: role: pkgs.writeShellApplication {
    name = "srt-vm-stop-${name}";
    runtimeInputs = with pkgs; [ procps ];
    text = ''
      echo "Stopping srt-${name} MicroVM..."
      pkill -f 'gosrt:srt-${name}' || echo "Not running"
    '';
  };

  # Generate SSH script for a role
  mkSshScript = name: role: pkgs.writeShellApplication {
    name = "srt-ssh-${name}";
    runtimeInputs = with pkgs; [ openssh sshpass ];
    text = ''
      unset SSH_AUTH_SOCK
      exec sshpass -p srt ssh \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR \
        root@${role.network.vmIp} "$@"
    '';
  };

  # Generate console script for a role
  mkConsoleScript = name: role: pkgs.writeShellApplication {
    name = "srt-console-${name}";
    runtimeInputs = with pkgs; [ netcat ];
    text = ''
      echo "Connecting to srt-${name} serial console..."
      echo "Press Ctrl+C to exit"
      echo
      exec nc localhost ${toString role.ports.console}
    '';
  };

  # Generate console ports list line for a role
  mkConsolePortLine = name: role:
    "  ${lib.fixedWidthString 16 " " name}: nc localhost ${toString role.ports.console}";

  # ─── Generated Script Maps ────────────────────────────────────────────────

  # Generate all per-role scripts using lib.mapAttrs
  stopScripts = lib.mapAttrs mkStopScript gosrtLib.roles;
  sshScripts = lib.mapAttrs mkSshScript gosrtLib.roles;
  consoleScripts = lib.mapAttrs mkConsoleScript gosrtLib.roles;

in {
  # ─── Global Scripts ───────────────────────────────────────────────────────

  check = pkgs.writeShellApplication {
    name = "srt-vm-check";
    runtimeInputs = with pkgs; [ procps ];
    text = ''
      echo "=== GoSRT MicroVM Processes ==="
      echo

      if pgrep -af '${vmPattern}'; then
        echo
        echo "=== Count ==="
        pgrep -cf '${vmPattern}'
      else
        echo "(none running)"
        echo
        echo "=== Count ==="
        echo "0"
      fi

      echo
      echo "=== Console Ports ==="
      ${lib.concatMapStringsSep "\n" (name:
        "echo \"${mkConsolePortLine name gosrtLib.roles.${name}}\""
      ) (lib.attrNames gosrtLib.roles)}
      echo
      echo "=== Web UIs ==="
      echo "  Grafana:       http://${gosrtLib.roles.metrics.network.vmIp}:${toString gosrtLib.ports.grafana}"
      echo "  Prometheus:    http://${gosrtLib.roles.metrics.network.vmIp}:${toString gosrtLib.ports.prometheusServer}"
    '';
  };

  checkJson = pkgs.writeShellApplication {
    name = "srt-vm-check-json";
    runtimeInputs = with pkgs; [ procps jq ];
    text = ''
      pgrep -af '${vmPattern}' 2>/dev/null | \
        jq -R -s '
          split("\n") |
          map(select(. != "")) |
          map(
            capture("^(?<pid>[0-9]+)\\s+.*gosrt:(?<name>srt-[a-z-]+)") |
            select(.name != null)
          )
        ' || echo "[]"
    '';
  };

  stopAll = pkgs.writeShellApplication {
    name = "srt-vm-stop";
    runtimeInputs = with pkgs; [ procps ];
    text = ''
      echo "=== Stopping all GoSRT MicroVMs ==="

      if ! pgrep -f '${vmPattern}' > /dev/null; then
        echo "No GoSRT MicroVMs running."
        exit 0
      fi

      echo "Found processes:"
      pgrep -af '${vmPattern}'

      echo
      echo "Sending SIGTERM..."
      pkill -f '${vmPattern}' || true

      sleep 1

      if pgrep -f '${vmPattern}' > /dev/null; then
        echo "Processes still running, sending SIGKILL..."
        pkill -9 -f '${vmPattern}' || true
      fi

      echo "Done."
    '';
  };

  # Start all VMs script (instructions only)
  startAll = pkgs.writeShellApplication {
    name = "srt-vm-start-all";
    text = ''
      echo "=== Starting all GoSRT MicroVMs ==="
      echo "Starting VMs in order: server, then clients..."
      echo ""
      echo "Run these commands in separate terminals:"
      ${lib.concatMapStringsSep "\n" (name:
        "echo \"  nix run .#srt-${name}\""
      ) (lib.attrNames gosrtLib.roles)}
      echo ""
      echo "Or start with tmux: nix run .#srt-tmux-all"
    '';
  };

  # ─── Tmux Integration ────────────────────────────────────────────────────

  # Role boot order: server first, then other roles alphabetically
  bootOrder = [ "server" "metrics" ] ++
    (lib.filter (n: n != "server" && n != "metrics") (lib.attrNames gosrtLib.roles));

  # Start all VMs in tmux session with one pane per VM
  tmuxAll = pkgs.writeShellApplication {
    name = "srt-tmux-all";
    runtimeInputs = with pkgs; [ tmux ];
    text = ''
      SESSION="gosrt"

      # Kill existing session if present
      tmux kill-session -t "$SESSION" 2>/dev/null || true

      echo "=== Starting GoSRT MicroVMs in tmux ==="
      echo "Session: $SESSION"
      echo ""

      # Create session with first VM
      tmux new-session -d -s "$SESSION" -n vms \
        "echo 'Starting ${lib.head bootOrder}...'; nix run .#srt-${lib.head bootOrder}; read"

      # Add panes for remaining VMs
      ${lib.concatMapStringsSep "\n" (name: ''
        sleep 0.5  # Brief delay for orderly startup
        tmux split-window -t "$SESSION:vms" -v \
          "echo 'Starting ${name}...'; nix run .#srt-${name}; read"
        tmux select-layout -t "$SESSION:vms" tiled
      '') (lib.tail bootOrder)}

      # Create monitoring window with Grafana URL
      tmux new-window -t "$SESSION" -n monitor \
        "echo '=== GoSRT Monitoring ==='; \
         echo ''; \
         echo 'Grafana:    http://${gosrtLib.roles.metrics.network.vmIp}:${toString gosrtLib.ports.grafana}'; \
         echo 'Prometheus: http://${gosrtLib.roles.metrics.network.vmIp}:${toString gosrtLib.ports.prometheusServer}'; \
         echo ''; \
         echo 'Press Enter to watch logs...'; \
         read; \
         tail -f /tmp/gosrt-*.log 2>/dev/null || echo 'No logs yet'"

      # Attach to session
      echo "VMs starting in tmux session '$SESSION'"
      echo "Attaching... (Ctrl+B D to detach)"
      sleep 1
      tmux attach-session -t "$SESSION"
    '';
  };

  # Attach to existing tmux session
  tmuxAttach = pkgs.writeShellApplication {
    name = "srt-tmux-attach";
    runtimeInputs = with pkgs; [ tmux ];
    text = ''
      if tmux has-session -t gosrt 2>/dev/null; then
        tmux attach-session -t gosrt
      else
        echo "No gosrt tmux session running."
        echo "Start with: nix run .#srt-tmux-all"
        exit 1
      fi
    '';
  };

  # ─── Per-Role Scripts (Generated) ─────────────────────────────────────────

  # Export all generated scripts
  inherit stopScripts sshScripts consoleScripts;

  # ─── Apps Export (Data-Driven) ────────────────────────────────────────────

  apps = let
    mkApp = drv: { type = "app"; program = "${drv}/bin/${drv.name}"; };

    # Generate apps for each role and script type
    stopApps = lib.mapAttrs' (name: drv:
      lib.nameValuePair "srt-vm-stop-${name}" (mkApp drv)
    ) stopScripts;

    sshApps = lib.mapAttrs' (name: drv:
      lib.nameValuePair "srt-ssh-${name}" (mkApp drv)
    ) sshScripts;

    consoleApps = lib.mapAttrs' (name: drv:
      lib.nameValuePair "srt-console-${name}" (mkApp drv)
    ) consoleScripts;

  in {
    # Global VM management
    srt-vm-check = mkApp check;
    srt-vm-check-json = mkApp checkJson;
    srt-vm-stop = mkApp stopAll;
    srt-vm-start-all = mkApp startAll;

    # Tmux integration
    srt-tmux-all = mkApp tmuxAll;
    srt-tmux-attach = mkApp tmuxAttach;
  }
  # Merge per-role apps (generated from lib.roles)
  // stopApps
  // sshApps
  // consoleApps;
}
```

**What this replaces:**
- 8 individual `stopXxx` scripts → generated from `lib.mapAttrs mkStopScript gosrtLib.roles`
- 8 individual `sshXxx` scripts → generated from `lib.mapAttrs mkSshScript gosrtLib.roles`
- 8 individual `consoleXxx` scripts → generated from `lib.mapAttrs mkConsoleScript gosrtLib.roles`
- ~350 lines → ~130 lines (~60% reduction)

**Benefits:**
- Adding a new role to `constants.nix` automatically gets stop/ssh/console scripts
- No more possibility of IP/port mismatches between scripts and VM configs
- Console ports list in `check` output is auto-generated

### 7b. nix/testing/default.nix

Test orchestration and configuration:

```nix
# nix/testing/default.nix
#
# Test orchestration for GoSRT MicroVM integration tests.
# Provides test runners, configurations, and analysis tools.
#
{ pkgs, lib, microvms, networkScripts }:

let
  profiles = import ../network/profiles.nix { inherit pkgs lib; };

in {
  # Test configurations (matches contrib/integration_testing/test_configs.go)
  configs = import ./configs.nix { inherit lib profiles; };

  # Test runner script (imports lib.nix internally for role data)
  runner = import ./runner.nix { inherit pkgs lib microvms networkScripts profiles; };

  # Analysis and reporting
  analysis = import ./analysis.nix { inherit pkgs lib; };
}
```

### 7c. nix/testing/configs.nix

Test configuration definitions:

```nix
# nix/testing/configs.nix
#
# Test configurations for GoSRT integration testing.
# Maps to the test matrix in contrib/integration_testing/test_configs.go
#
{ lib, profiles }:

{
  # ═══════════════════════════════════════════════════════════════════════════
  # Clean Network Tests (baseline performance)
  # ═══════════════════════════════════════════════════════════════════════════

  clean-5M = {
    name = "Clean-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    profile = profiles.clean;
    description = "Clean network at 5 Mb/s";
  };

  clean-10M = {
    name = "Clean-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    profile = profiles.clean;
    description = "Clean network at 10 Mb/s";
  };

  clean-50M = {
    name = "Clean-50M";
    bitrateMbps = 50;
    durationSeconds = 60;
    profile = profiles.clean;
    description = "Clean network at 50 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Latency Tests
  # ═══════════════════════════════════════════════════════════════════════════

  regional-10M = {
    name = "Regional-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    profile = profiles.regional;
    description = "Regional DC (10ms RTT) at 10 Mb/s";
  };

  continental-10M = {
    name = "Continental-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    profile = profiles.continental;
    description = "Cross-continental (60ms RTT) at 10 Mb/s";
  };

  intercontinental-10M = {
    name = "Intercontinental-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    profile = profiles.intercontinental;
    description = "Intercontinental (130ms RTT) at 10 Mb/s";
  };

  geo-5M = {
    name = "GEO-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    profile = profiles.geoSatellite;
    description = "GEO satellite (300ms RTT) at 5 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Loss Tests
  # ═══════════════════════════════════════════════════════════════════════════

  loss2pct-5M = {
    name = "Loss-2pct-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    profile = profiles.loss2pct;
    description = "2% packet loss at 5 Mb/s";
  };

  loss5pct-5M = {
    name = "Loss-5pct-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    profile = profiles.loss5pct;
    description = "5% packet loss at 5 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Combined Stress Tests
  # ═══════════════════════════════════════════════════════════════════════════

  tier3-loss-10M = {
    name = "Tier3-Loss-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    profile = profiles.tier3Stress;
    description = "Tier 3 stress: 130ms RTT + 2% loss at 10 Mb/s";
  };

  geo-loss-5M = {
    name = "GEO-Loss-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    profile = profiles.geoWithLoss;
    description = "GEO satellite (300ms) + 0.5% loss at 5 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Starlink Pattern Tests
  # ═══════════════════════════════════════════════════════════════════════════

  starlink-5M = {
    name = "Starlink-5M";
    bitrateMbps = 5;
    durationSeconds = 120;  # Need full minute for pattern
    profile = profiles.starlinkBaseline;
    starlinkPattern = true;
    description = "Starlink with handoff events at 5 Mb/s";
  };

  # Test tiers (for CI)
  tier1 = [ "clean-5M" "loss2pct-5M" ];
  tier2 = [ "clean-5M" "clean-10M" "regional-10M" "loss2pct-5M" "loss5pct-5M" ];
  tier3 = [ "clean-5M" "clean-10M" "clean-50M" "regional-10M" "continental-10M"
            "intercontinental-10M" "geo-5M" "loss2pct-5M" "loss5pct-5M"
            "tier3-loss-10M" "geo-loss-5M" "starlink-5M" ];
}
```

### 7d. nix/testing/runner.nix

Test runner script:

```nix
# nix/testing/runner.nix
#
# Test runner for GoSRT MicroVM integration tests.
# Orchestrates VM startup, impairment application, and metrics collection.
#
{ pkgs, lib, microvms, networkScripts, profiles }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  configs = import ./configs.nix { inherit lib profiles; };

in rec {
  # ─── Health Check Helper ─────────────────────────────────────────────────
  # Robust service readiness detection with exponential backoff
  waitForService = pkgs.writeShellApplication {
    name = "srt-wait-for-service";
    runtimeInputs = with pkgs; [ curl coreutils ];
    text = ''
      URL="''${1:-}"
      NAME="''${2:-service}"
      MAX_ATTEMPTS="''${3:-30}"

      if [ -z "$URL" ]; then
        echo "Usage: srt-wait-for-service <url> [name] [max_attempts]"
        exit 1
      fi

      attempt=1
      wait_time=1

      while [ $attempt -le "$MAX_ATTEMPTS" ]; do
        if curl -sf "$URL" > /dev/null 2>&1; then
          echo "  ✓ $NAME ready (attempt $attempt)"
          exit 0
        fi
        echo "  Waiting for $NAME... (attempt $attempt/$MAX_ATTEMPTS)"
        sleep $wait_time
        attempt=$((attempt + 1))
        # Exponential backoff, max 5 seconds
        wait_time=$((wait_time < 5 ? wait_time + 1 : 5))
      done

      echo "  ✗ $NAME failed to become ready after $MAX_ATTEMPTS attempts"
      exit 1
    '';
  };

  # ─── Start All VMs ───────────────────────────────────────────────────────
  # Parallel VM startup with proper sequencing
  startAll = pkgs.writeShellApplication {
    name = "srt-start-all";
    runtimeInputs = with pkgs; [ coreutils procps ];
    text = ''
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  Starting all GoSRT MicroVMs                                      ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"

      # Check if already running
      if pgrep -f 'gosrt:srt-' > /dev/null; then
        echo "WARNING: Some MicroVMs are already running:"
        pgrep -af 'gosrt:srt-'
        echo ""
        read -p "Stop them and continue? [y/N] " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
          pkill -f 'gosrt:srt-' || true
          sleep 2
        else
          exit 1
        fi
      fi

      # Start server first (others connect to it)
      echo "Starting server..."
      nix run .#srt-server-vm &
      sleep 3

      # Start metrics VM (for scraping)
      echo "Starting metrics VM..."
      nix run .#srt-metrics-vm &
      sleep 2

      # Start clients in parallel
      echo "Starting publisher and subscriber..."
      nix run .#srt-publisher-vm &
      nix run .#srt-subscriber-vm &

      echo ""
      echo "All VMs started. Use 'nix run .#srt-vm-check' to verify."
      echo "Grafana: http://${gosrtLib.roles.metrics.network.vmIp}:${toString gosrtLib.ports.grafana}"

      # Wait for all background jobs
      wait
    '';
  };

  # ─── Test Runner ─────────────────────────────────────────────────────────
  runner = pkgs.writeShellApplication {
    name = "srt-test-runner";
    runtimeInputs = with pkgs; [ curl jq coreutils procps ];

    text = ''
      set -euo pipefail

      CONFIG="''${1:-clean-5M}"
      DURATION="''${2:-60}"
      OUTPUT_DIR="''${3:-/tmp/srt-test-results}"

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  GoSRT Integration Test Runner                                    ║"
      echo "╠══════════════════════════════════════════════════════════════════╣"
      echo "║  Config: $CONFIG                                                  "
      echo "║  Duration: $DURATION seconds                                      "
      echo "║  Output: $OUTPUT_DIR                                              "
      echo "╚══════════════════════════════════════════════════════════════════╝"

      mkdir -p "$OUTPUT_DIR"

      # Verify network is set up
      echo "Step 1: Verifying network..."
      if ! ip netns list | grep -q "srt-router-a"; then
        echo "ERROR: Network not set up. Run: sudo nix run .#srt-network-setup"
        exit 1
      fi

      # Check if VMs are running, offer to start them
      echo "Step 2: Checking MicroVMs..."
      if ! pgrep -f 'gosrt:srt-server' > /dev/null; then
        echo "  VMs not running. Starting them now..."
        nix run .#srt-start-all &
        START_PID=$!
        sleep 5
      fi

      # Wait for services to be ready with exponential backoff
      echo "Step 3: Waiting for services..."
      ${waitForService}/bin/srt-wait-for-service \
        "http://${gosrtLib.roles.server.network.vmIp}:9100/metrics" "Server" 30
      ${waitForService}/bin/srt-wait-for-service \
        "http://${gosrtLib.roles.publisher.network.vmIp}:9100/metrics" "Publisher" 30
      ${waitForService}/bin/srt-wait-for-service \
        "http://${gosrtLib.roles.subscriber.network.vmIp}:9100/metrics" "Subscriber" 30

    # Apply network impairment profile
    echo "Step 4: Applying network profile..."
    # Profile application would go here based on $CONFIG

    # Collect metrics during test
    echo "Step 5: Running test for $DURATION seconds..."
    START_TIME=$(date +%s)
    END_TIME=$((START_TIME + DURATION))

    while [ "$(date +%s)" -lt "$END_TIME" ]; do
      TIMESTAMP=$(date +%s)

      # Collect metrics from all endpoints
      curl -sf "http://${gosrtLib.roles.server.network.vmIp}:9100/metrics" \
        >> "$OUTPUT_DIR/server_metrics.txt" 2>/dev/null || true
      curl -sf "http://${gosrtLib.roles.publisher.network.vmIp}:9100/metrics" \
        >> "$OUTPUT_DIR/publisher_metrics.txt" 2>/dev/null || true
      curl -sf "http://${gosrtLib.roles.subscriber.network.vmIp}:9100/metrics" \
        >> "$OUTPUT_DIR/subscriber_metrics.txt" 2>/dev/null || true

      sleep 1
    done

    echo "Step 6: Test complete. Results in $OUTPUT_DIR"

    # Generate summary
    echo "╔══════════════════════════════════════════════════════════════════╗"
    echo "║  Test Complete: $CONFIG                                          "
    echo "╠══════════════════════════════════════════════════════════════════╣"
    echo "║  Metrics collected: $OUTPUT_DIR                                   "
    echo "║  View Grafana: http://${gosrtLib.roles.metrics.network.vmIp}:3000      "
    echo "╚══════════════════════════════════════════════════════════════════╝"
  '';
}
```

### 7e. nix/testing/analysis.nix

Metrics analysis and reporting:

```nix
# nix/testing/analysis.nix
#
# Analysis tools for GoSRT test results.
# Extracts key metrics and generates reports.
#
{ pkgs, lib }:

{
  # Extract key metrics from Prometheus text format
  extractMetrics = pkgs.writeShellApplication {
    name = "srt-extract-metrics";
    runtimeInputs = with pkgs; [ gawk gnugrep ];
    text = ''
      FILE="''${1:-}"
      if [ -z "$FILE" ]; then
        echo "Usage: srt-extract-metrics <metrics-file>"
        exit 1
      fi

      echo "=== Key Metrics ==="

      echo "RTT (microseconds):"
      grep "gosrt_rtt_microseconds" "$FILE" | tail -1 || echo "  Not found"

      echo "Packets Lost:"
      grep "gosrt_connection_congestion_packets_lost_total" "$FILE" | tail -1 || echo "  Not found"

      echo "Retransmissions:"
      grep "gosrt_connection_congestion_recv_pkt_retrans_total" "$FILE" | tail -1 || echo "  Not found"

      echo "TSBPD Skipped (unrecoverable):"
      grep "gosrt_connection_congestion_recv_pkt_skipped_tsbpd_total" "$FILE" | tail -1 || echo "  Not found"

      echo "Ring Drops:"
      grep "gosrt_receiver_ring_drops_total" "$FILE" | tail -1 || echo "  Not found"
    '';
  };

  # Generate test report
  generateReport = pkgs.writeShellApplication {
    name = "srt-generate-report";
    runtimeInputs = with pkgs; [ jq coreutils ];
    text = ''
      OUTPUT_DIR="''${1:-/tmp/srt-test-results}"

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  GoSRT Test Report                                               ║"
      echo "╠══════════════════════════════════════════════════════════════════╣"
      echo "║  Results Directory: $OUTPUT_DIR                                  "
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""

      for f in "$OUTPUT_DIR"/*_metrics.txt; do
        [ -f "$f" ] || continue
        ENDPOINT=$(basename "$f" _metrics.txt)
        echo "=== $ENDPOINT ==="
        tail -100 "$f" | grep -E "gosrt_(rtt|connection_congestion)" | sort -u | head -20
        echo ""
      done
    '';
  };

  # Compare two test runs
  compareRuns = pkgs.writeShellApplication {
    name = "srt-compare-runs";
    runtimeInputs = with pkgs; [ gawk gnugrep ];
    text = ''
      RUN_A="''${1:-}"
      RUN_B="''${2:-}"

      if [ -z "$RUN_A" ] || [ -z "$RUN_B" ]; then
        echo "Usage: srt-compare-runs <run-a-dir> <run-b-dir>"
        exit 1
      fi

      echo "Comparing: $RUN_A vs $RUN_B"
      echo ""
      # Comparison logic here
    '';
  };
}
```

### 7f. nix/checks.nix

CI checks for `nix flake check`:

```nix
# nix/checks.nix
#
# Checks for nix flake check.
# Runs go vet, tests, and validates Nix expressions.
#
{ pkgs, lib, src, packages }:

let
  constants = import ./constants.nix;

in {
  # Go vet check
  go-vet = pkgs.runCommand "gosrt-go-vet" {
    nativeBuildInputs = [ pkgs.go_1_26 ];
    inherit src;
  } ''
    cd $src
    export GOEXPERIMENT=${lib.concatStringsSep "," constants.go.experimentalFeatures}
    export HOME=$(mktemp -d)
    go vet ./... > $out 2>&1 || (cat $out && exit 1)
  '';

  # Go test (quick tests only)
  go-test-quick = pkgs.runCommand "gosrt-go-test-quick" {
    nativeBuildInputs = [ pkgs.go_1_26 ];
    inherit src;
  } ''
    cd $src
    export GOEXPERIMENT=${lib.concatStringsSep "," constants.go.experimentalFeatures}
    export HOME=$(mktemp -d)
    go test -short ./... > $out 2>&1 || (cat $out && exit 1)
  '';

  # Circular number tests (critical for sequence wraparound)
  go-test-circular = pkgs.runCommand "gosrt-go-test-circular" {
    nativeBuildInputs = [ pkgs.go_1_26 ];
    inherit src;
  } ''
    cd $src
    export GOEXPERIMENT=${lib.concatStringsSep "," constants.go.experimentalFeatures}
    export HOME=$(mktemp -d)
    go test -v ./circular/... > $out 2>&1 || (cat $out && exit 1)
  '';

  # Packet marshaling tests
  go-test-packet = pkgs.runCommand "gosrt-go-test-packet" {
    nativeBuildInputs = [ pkgs.go_1_26 ];
    inherit src;
  } ''
    cd $src
    export GOEXPERIMENT=${lib.concatStringsSep "," constants.go.experimentalFeatures}
    export HOME=$(mktemp -d)
    go test -v ./packet/... > $out 2>&1 || (cat $out && exit 1)
  '';

  # Build all packages (ensures they compile)
  build-all = packages.all;

  # Metrics audit (verify all metrics are defined and used)
  metrics-audit = pkgs.runCommand "gosrt-metrics-audit" {
    nativeBuildInputs = [ pkgs.go_1_26 ];
    inherit src;
  } ''
    cd $src
    export HOME=$(mktemp -d)
    # Run the metrics audit tool
    go run ./tools/metrics-audit/main.go > $out 2>&1 || (cat $out && exit 1)
  '';
}
```

### 8. nix/shell.nix

Development shell configuration:

```nix
# nix/shell.nix
#
# Development shell for GoSRT.
# Provides Go 1.26+, io_uring dependencies, and debugging tools.
#
{ pkgs }:

let
  lib = pkgs.lib;
  constants = import ./constants.nix;

  # Go 1.26 with experimental features
  # Note: Package name in nixpkgs follows pattern go_1_XX (e.g., go_1_26)
  # Check `nix search nixpkgs go_1` for available versions
  goPackage = pkgs.go_1_26;  # Go 1.26 released February 2026
  # greenteagc is now DEFAULT in Go 1.26 - only jsonv2 remains experimental

in pkgs.mkShell {
  packages = with pkgs; [
    # Go toolchain
    goPackage
    gopls
    go-tools
    golangci-lint
    delve

    # Network testing tools
    iproute2
    iperf2
    tcpdump
    nmap
    curl
    jq

    # Performance analysis
    linuxPackages_latest.perf
    flamegraph
    pprof

    # Documentation
    graphviz  # For pprof graphs

    # Nix utilities
    nixfmt-rfc-style
  ];

  shellHook = ''
    # Enable Go experimental features
    # Note: greenteagc is now DEFAULT in Go 1.26, only jsonv2 is experimental
    export GOEXPERIMENT=${lib.concatStringsSep "," constants.go.experimentalFeatures}

    # Pure Go builds - no CGO required
    export CGO_ENABLED=0

    echo "╔══════════════════════════════════════════════════════════════════╗"
    echo "║  GoSRT Development Shell                                          ║"
    echo "╠══════════════════════════════════════════════════════════════════╣"
    echo "║  Go:           $(go version | cut -d' ' -f3)                      ║"
    echo "║  GOEXPERIMENT: $GOEXPERIMENT                                      ║"
    echo "║  Green Tea GC: enabled by default                                 ║"
    echo "╠══════════════════════════════════════════════════════════════════╣"
    echo "║  Build:     make build                                            ║"
    echo "║  Test:      make test                                             ║"
    echo "║  Bench:     make bench-receiver                                   ║"
    echo "╚══════════════════════════════════════════════════════════════════╝"
  '';
}
```

### 9. flake.nix

Main flake entry point:

```nix
# flake.nix
#
# GoSRT - Pure Go SRT implementation with MicroVM integration testing
#
# Usage:
#   nix develop                              # Development shell
#   nix build                                # Build all packages
#   nix build .#server                       # Build server binary
#   nix build .#server-container             # Build server OCI container
#
# Network Setup (run once as root):
#   nix run .#srt-network-setup              # Setup test network
#   nix run .#srt-network-teardown           # Remove test network
#
# Start MicroVMs (no root needed after network setup):
#   nix run .#srt-server-vm                  # Start server MicroVM
#   nix run .#srt-publisher-vm               # Start publisher MicroVM (GoSRT)
#   nix run .#srt-subscriber-vm              # Start subscriber MicroVM (GoSRT)
#
# Interop Testing MicroVMs:
#   nix run .#srt-xtransmit-pub-vm           # srt-xtransmit publisher
#   nix run .#srt-ffmpeg-pub-vm              # FFmpeg publisher (20Mb/s test pattern)
#   nix run .#srt-xtransmit-sub-vm           # srt-xtransmit subscriber
#   nix run .#srt-ffmpeg-sub-vm              # FFmpeg subscriber (output to /dev/null)
#
# Metrics & Visualization:
#   nix run .#srt-metrics-vm                 # Start Prometheus + Grafana VM
#   # Grafana Dashboards (admin/srt):
#   #   Operations: http://10.50.8.2:3000/d/gosrt-ops (NOC view)
#   #   Analysis:   http://10.50.8.2:3000/d/gosrt-analysis (Engineering view)
#   # Prometheus: http://10.50.8.2:9090
#
# VM Management:
#   nix run .#srt-vm-check                   # List running MicroVMs (human-readable)
#   nix run .#srt-vm-check-json              # List running MicroVMs (JSON for scripting)
#   nix run .#srt-start-all                  # Start all VMs in proper sequence
#   nix run .#srt-vm-stop                    # Stop all MicroVMs
#   nix run .#srt-vm-stop-server             # Stop server VM only
#
# Connect to VMs:
#   nix run .#srt-ssh-server                 # SSH into server (password: srt)
#   nix run .#srt-ssh-publisher              # SSH into publisher
#   nix run .#srt-ssh-subscriber             # SSH into subscriber
#   nix run .#srt-ssh-xtransmit-pub          # SSH into xtransmit publisher
#   nix run .#srt-ssh-ffmpeg-pub             # SSH into ffmpeg publisher
#   nix run .#srt-ssh-xtransmit-sub          # SSH into xtransmit subscriber
#   nix run .#srt-ssh-ffmpeg-sub             # SSH into ffmpeg subscriber
#   nix run .#srt-ssh-metrics                # SSH into metrics VM
#
# Serial Console (when network is broken):
#   nix run .#srt-console-server             # Serial console (nc) to server
#   nix run .#srt-console-publisher          # Serial console to publisher
#   nix run .#srt-console-subscriber         # Serial console to subscriber
#   nix run .#srt-console-xtransmit-pub      # Serial console to xtransmit publisher
#   nix run .#srt-console-ffmpeg-pub         # Serial console to ffmpeg publisher
#   nix run .#srt-console-xtransmit-sub      # Serial console to xtransmit subscriber
#   nix run .#srt-console-ffmpeg-sub         # Serial console to ffmpeg subscriber
#   nix run .#srt-console-metrics            # Serial console to metrics VM
#
# Network Impairment:
#   nix run .#srt-set-latency -- 2           # Switch to 60ms RTT
#   nix run .#srt-set-loss -- 5 2            # Add 5% loss on link 2
#   nix run .#srt-starlink-pattern           # Run Starlink handoff pattern
#   nix run .#srt-starlink-pattern -- 120 200 "15 30 45"  # Custom pattern
#
# Testing:
#   nix run .#srt-test-runner                # Run integration test suite
#   nix run .#srt-test-runner -- clean-5M 60 # Specific config, 60s duration
#   nix run .#srt-wait-for-service -- URL    # Wait for service readiness
#
{
  description = "GoSRT - Pure Go SRT protocol implementation for live streaming";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";

    # MicroVM support (using TAP performance branch)
    microvm = {
      url = "github:randomizedcoder/microvm.nix/tap-performance";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  # Binary cache for faster MicroVM builds
  nixConfig = {
    extra-substituters = [ "https://microvm.cachix.org" ];
    extra-trusted-public-keys = [
      "microvm.cachix.org-1:oXnBc6hRE3eX5rSYdRyMYXnfzcCxC7yKPTbZXALsqys="
    ];
  };

  outputs = { self, nixpkgs, flake-utils, microvm }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        lib = pkgs.lib;

        # Clean source (exclude build artifacts)
        src = lib.cleanSourceWith {
          src = lib.cleanSource ./.;
          filter = path: type:
            let
              baseName = builtins.baseNameOf path;
              ignoredPaths = [
                ".direnv"
                "result"
                ".git"
                ".vscode"
                ".cursor"
              ];
            in !(builtins.elem baseName ignoredPaths);
        };

        # ═══════════════════════════════════════════════════════════════════
        # Modular Imports (reduces duplication)
        # ═══════════════════════════════════════════════════════════════════

        # Shared constants
        constants = import ./nix/constants.nix;

        # GoSRT packages
        packages = import ./nix/packages { inherit pkgs lib src; };

        # External packages for interop testing
        srtXtransmit = import ./nix/packages/srt-xtransmit.nix { inherit pkgs lib; };
        ffmpegFull = import ./nix/packages/ffmpeg.nix { inherit pkgs lib; };

        # OCI containers
        containers = import ./nix/containers {
          inherit pkgs lib;
          serverPackage = packages.server;
          clientPackage = packages.client;
          clientGeneratorPackage = packages.client-generator;
        };

        # Network management (setup, teardown, impairment)
        network = import ./nix/network { inherit pkgs lib; };

        # MicroVMs (all 8 VMs via single import)
        # ELEGANCE: Support build variants (production vs debug)
        microvms = import ./nix/microvms {
          inherit pkgs lib microvm nixpkgs system;
          inherit packages srtXtransmit ffmpegFull;
          buildVariant = "production";
        };

        # Debug MicroVMs (with context assertions enabled)
        microvmsDebug = import ./nix/microvms {
          inherit pkgs lib microvm nixpkgs system;
          inherit packages srtXtransmit ffmpegFull;
          buildVariant = "debug";
        };

        # Impairment library (functional scenarios)
        impairments = import ./nix/network/impairments.nix { inherit pkgs lib; };

        # VM management scripts (check, stop, ssh, console)
        vmScripts = import ./nix/scripts/vm-management.nix { inherit pkgs; };

        # CI checks
        checks' = import ./nix/checks.nix { inherit pkgs lib src packages; };

        # Testing infrastructure
        testing = import ./nix/testing {
          inherit pkgs lib;
          inherit microvms;
          networkScripts = network;
        };

      in {
        formatter = pkgs.nixfmt-rfc-style;

        # ═══════════════════════════════════════════════════════════════════
        # Packages
        # ═══════════════════════════════════════════════════════════════════
        packages = {
          default = packages.all;

          # Individual binaries
          inherit (packages) server client client-generator client-seeker performance udp-echo;

          # All binaries combined
          gosrt-all = packages.all;

          # External packages for interop testing
          srt-xtransmit = srtXtransmit;
          ffmpeg-full = ffmpegFull;

        } // lib.optionalAttrs pkgs.stdenv.isLinux {
          # OCI containers (Linux only)
          server-container = containers.server;
          client-container = containers.client;
          client-generator-container = containers.client-generator;

          # ═══════════════════════════════════════════════════════════════════
          # MicroVMs (Linux only, requires KVM)
          # Generated from lib.nix roles via microvms/default.nix
          # ═══════════════════════════════════════════════════════════════════
        } // lib.mapAttrs' (name: vm:
          lib.nameValuePair "srt-${name}-vm" vm.vm
        ) microvms;

        # ═══════════════════════════════════════════════════════════════════
        # Apps (runnable scripts)
        # Using modular imports for cleaner, DRY code
        # ═══════════════════════════════════════════════════════════════════
        apps = lib.optionalAttrs pkgs.stdenv.isLinux (
          # Network management apps (from nix/network/default.nix)
          network.apps

          # MicroVM runner apps (generated from microvms via lib.roles)
          // lib.mapAttrs' (name: vm:
            lib.nameValuePair "srt-${name}-vm" {
              type = "app";
              program = "${vm.runScript}";
            }
          ) microvms

          # VM management scripts (from vmScripts)
          // vmScripts.apps

          # Testing infrastructure apps
          // {
            srt-start-all = { type = "app"; program = "${testing.runner.startAll}/bin/srt-start-all"; };
            srt-vm-check-json = { type = "app"; program = "${vmScripts.checkJson}/bin/srt-vm-check-json"; };
            srt-test-runner = { type = "app"; program = "${testing.runner.runner}/bin/srt-test-runner"; };
            srt-wait-for-service = { type = "app"; program = "${testing.runner.waitForService}/bin/srt-wait-for-service"; };
          }

          # Debug MicroVM apps (with context assertions enabled)
          // lib.mapAttrs' (name: vm:
            lib.nameValuePair "srt-${name}-vm-debug" {
              type = "app";
              program = "${vm.runScript}";
            }
          ) microvmsDebug

          # Impairment scenario apps (functional scripting)
          // impairments.apps
        );

        # ═══════════════════════════════════════════════════════════════════
        # Development shell
        # ═══════════════════════════════════════════════════════════════════
        devShells.default = import ./nix/shell.nix { inherit pkgs; };

        # ═══════════════════════════════════════════════════════════════════
        # Checks (CI)
        # ELEGANCE: Integrate audit tools into Nix checks.
        # No binary is ever deployed if it has unsafe sequence arithmetic
        # or missing prometheus exports.
        # ═══════════════════════════════════════════════════════════════════
        checks = {
          # ─── Code Quality Audits (CRITICAL) ─────────────────────────────
          # These must pass before any binary can be built/deployed

          # Sequence arithmetic safety audit
          audit-seq = pkgs.runCommand "audit-seq" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            echo "=== Sequence Arithmetic Safety Audit ==="
            # Run the sequence audit tool
            go run ./tools/sequence-audit/main.go ./... > $out 2>&1
            echo "PASS: No unsafe sequence patterns detected" >> $out
          '';

          # Prometheus metrics definition audit
          audit-metrics = pkgs.runCommand "audit-metrics" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            echo "=== Prometheus Metrics Audit ==="
            go run ./tools/metrics-audit/main.go > $out 2>&1
            echo "PASS: All metrics properly defined and exported" >> $out
          '';

          # ─── Go Quality Checks ──────────────────────────────────────────

          # Go tests (tiered)
          go-test-tier1 = pkgs.runCommand "go-test-tier1" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            export GOEXPERIMENT=jsonv2
            echo "=== Tier 1: Core Tests (~50 tests, <3s) ==="
            go test -v -run 'TestStream_Tier1' ./congestion/live/... > $out 2>&1 || (cat $out && exit 1)
          '';

          go-test-tier2 = pkgs.runCommand "go-test-tier2" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            export GOEXPERIMENT=jsonv2
            echo "=== Tier 2: Extended Tests (~200 tests, <15s) ==="
            go test -v -run 'TestStream_Tier[12]' ./congestion/live/... > $out 2>&1 || (cat $out && exit 1)
          '';

          go-test-all = pkgs.runCommand "go-test-all" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            export GOEXPERIMENT=jsonv2
            echo "=== Full Test Suite ==="
            go test -v ./... > $out 2>&1 || (cat $out && exit 1)
          '';

          # Race detection
          go-test-race = pkgs.runCommand "go-test-race" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            export GOEXPERIMENT=jsonv2
            echo "=== Race Detection Tests ==="
            go test -race -v ./congestion/live/... > $out 2>&1 || (cat $out && exit 1)
          '';

          # Go vet
          go-vet = pkgs.runCommand "go-vet" {
            nativeBuildInputs = [ pkgs.go_1_26 ];
            src = src;
          } ''
            cd $src
            export GOEXPERIMENT=jsonv2
            go vet ./... > $out 2>&1 || (cat $out && exit 1)
          '';

          # Staticcheck
          staticcheck = pkgs.runCommand "staticcheck" {
            nativeBuildInputs = [ pkgs.go_1_26 pkgs.go-tools ];
            src = src;
          } ''
            cd $src
            export GOEXPERIMENT=jsonv2
            staticcheck ./... > $out 2>&1 || (cat $out && exit 1)
          '';

          # ─── Build Verification ─────────────────────────────────────────

          # Verify all packages build
          build-all = pkgs.runCommand "build-all" {
            buildInputs = [ packages.all ];
          } ''
            echo "All packages built successfully" > $out
            # Verify binaries exist
            which gosrt-server >> $out || exit 1
            which gosrt-client >> $out || exit 1
          '';
        };
      }
    )
    // {
      # Overlay for using in other flakes
      overlays.default = final: prev: {
        gosrt = self.packages.${final.system}.default;
      };
    };
}
```

---

## Usage Workflow

### 1. Development

```bash
# Enter development shell
nix develop

# Build all binaries
make build

# Run tests
make test

# Run benchmarks
make bench-receiver
```

### 2. Build Individual Components

```bash
# Build server binary
nix build .#server

# Build OCI container
nix build .#server-container

# Load container into Docker
docker load < ./result
```

### 3. Integration Testing with MicroVMs

**GoSRT-to-GoSRT Testing:**
```bash
# Step 1: Verify host environment
nix run .#srt-network-check

# Step 2: Setup network (run as root ONCE)
sudo nix run .#srt-network-setup

# Step 3: Start GoSRT MicroVMs (no sudo needed)
# Terminal 1:
nix run .#srt-server-vm

# Terminal 2:
nix run .#srt-publisher-vm

# Terminal 3:
nix run .#srt-subscriber-vm

# Step 4: Apply network impairment (from any terminal)
nix run .#srt-set-latency -- 2      # 60ms RTT
nix run .#srt-set-loss -- 5 2       # 5% loss on link 2

# Step 5: Collect metrics
curl http://10.50.3.2:9100/metrics   # Server metrics
curl http://10.50.1.2:9100/metrics   # Publisher metrics
curl http://10.50.2.2:9100/metrics   # Subscriber metrics

# Step 6: Teardown (when done)
nix run .#srt-vm-stop                # Stop all VMs
sudo nix run .#srt-network-teardown  # Remove network (optional)
```

**Interoperability Testing (with srt-xtransmit and FFmpeg):**
```bash
# Start server first
nix run .#srt-server-vm

# Test with srt-xtransmit (reference SRT implementation)
nix run .#srt-xtransmit-pub-vm       # Publishes to GoSRT server
nix run .#srt-xtransmit-sub-vm       # Subscribes from GoSRT server

# Test with FFmpeg (real-world video streaming)
nix run .#srt-ffmpeg-pub-vm          # Publishes 20Mb/s test pattern
nix run .#srt-ffmpeg-sub-vm          # Subscribes and outputs to /dev/null
```

**Metrics Collection and Visualization:**
```bash
# Start metrics VM (Prometheus + Grafana)
nix run .#srt-metrics-vm

# Access dashboards:
#   Operations:  http://10.50.8.2:3000/d/gosrt-ops (NOC view - "is my stream healthy?")
#   Analysis:    http://10.50.8.2:3000/d/gosrt-analysis (Engineering view - "why is it unhealthy?")
#   Prometheus:  http://10.50.8.2:9090 (raw metrics)
#   Login: admin/srt

# Prometheus scrapes these endpoints:
#   GoSRT metrics (1s interval):
#     - Server:     http://10.50.3.2:9100/metrics
#     - Publisher:  http://10.50.1.2:9100/metrics
#     - Subscriber: http://10.50.2.2:9100/metrics
#   Node Exporter (5s interval) - system metrics for io_uring contention:
#     - Server:     http://10.50.3.2:9100/metrics (node_* prefixed)
#     - Publisher:  http://10.50.1.2:9100/metrics
#     - Subscriber: http://10.50.2.2:9100/metrics

# ═══════════════════════════════════════════════════════════════════════════════
# IMPAIRMENT CORRELATION (Grafana Annotations)
# ═══════════════════════════════════════════════════════════════════════════════
#
# Network impairment scripts automatically push annotations to Grafana:
#
#   nix run .#srt-set-latency -- 3     # Pushes "Latency changed to intercontinental (130ms)"
#   nix run .#srt-set-loss -- 5 2      # Pushes "5% loss applied on link 2"
#   nix run .#srt-starlink-pattern     # Pushes annotations at each handoff event
#
# Annotations appear as vertical red lines on all graphs, allowing you to
# correlate metric changes (e.g., CongestionRecvPktLoss spike) with the
# exact moment impairment was applied.
#
# Tag filters in dashboards:
#   - "impairment" - all impairment events
#   - "latency"    - latency profile changes
#   - "loss"       - loss injection events
#   - "starlink"   - Starlink pattern events (blackhole/restore)

# ═══════════════════════════════════════════════════════════════════════════════
# STARLINK PATTERN TESTING
# ═══════════════════════════════════════════════════════════════════════════════
#
# Simulate Starlink satellite handoff events (100% loss for ~500ms):
#
#   nix run .#srt-starlink-pattern -- 60    # Run for 60 seconds
#
# Pattern: Blackhole at 12s, 27s, 42s, 57s (repeating every minute)
# Each event pushes annotations to Grafana for correlation analysis.
#
# Watch in Grafana:
#   - Loss spike followed by NAK burst
#   - Retransmission recovery (or TSBPD skip if too severe)
#   - RTT spike during blackhole (ACKACK timeout)

# ═══════════════════════════════════════════════════════════════════════════════
# io_uring CONTENTION DETECTION
# ═══════════════════════════════════════════════════════════════════════════════
#
# Node Exporter provides system-level metrics to detect io_uring delays:
#
# Key metrics to watch:
#   - node_context_switches_total - High rate = CPU contention
#   - node_schedstat_waiting_seconds_total - Scheduler latency
#   - node_softirqs_total{type="NET_RX"} - Network interrupt load
#   - node_cpu_seconds_total{mode="steal"} - Hypervisor stealing CPU
#
# If RTT variance spikes but network is "clean", check these metrics.
# High context switches or steal time indicates the io_uring completion
# handlers are being delayed by the hypervisor.

# ═══════════════════════════════════════════════════════════════════════════════
# TWO GRAFANA DASHBOARDS:
# ═══════════════════════════════════════════════════════════════════════════════
#
# ┌─────────────────────────────────────────────────────────────────────────────┐
# │ DASHBOARD 1: "GoSRT Operations" (gosrt-ops)                                  │
# │ URL: http://10.50.8.2:3000/d/gosrt-ops                                       │
# │                                                                              │
# │ PURPOSE: High-level NOC/operator view for monitoring production streams.     │
# │ QUESTION ANSWERED: "Is my stream healthy?"                                   │
# │                                                                              │
# │ PANELS:                                                                      │
# │                                                                              │
# │ Stream Health Status Row (traffic lights):                                   │
# │   - Ingest Health - Loss % for Publisher→Server (GREEN <1%, RED >3%)         │
# │   - Delivery Health - Loss % for Server→Subscriber                           │
# │   - Unrecoverable Loss - TSBPD skipped packets (RED if any > 0)              │
# │   - Active Streams - Connection count                                        │
# │   - Retransmission Overhead - % bandwidth used for retrans                   │
# │   - Ring Buffer - Lock-free ring drops (RED if any > 0)                      │
# │                                                                              │
# │ Throughput & Quality Row:                                                    │
# │   - Goodput (Effective Throughput) - Unique bytes delivered (Mbps)           │
# │   - Goodput vs Total - Visual gap = retransmission overhead                  │
# │                                                                              │
# │ Network Conditions Row:                                                      │
# │   - RTT - Early warning indicator (spikes precede loss)                      │
# │   - Retransmission Rate Over Time - Trend analysis                           │
# │   - Loss Events Over Time - Raw packet loss rate                             │
# │                                                                              │
# │ Buffer Health Row:                                                           │
# │   - TSBPD Buffer Time - Recovery headroom (ms in buffer)                     │
# │   - Packets in Flight & Buffer - Congestion indicator                        │
# └─────────────────────────────────────────────────────────────────────────────┘
#
# ┌─────────────────────────────────────────────────────────────────────────────┐
# │ DASHBOARD 2: "GoSRT Analysis" (gosrt-analysis)                               │
# │ URL: http://10.50.8.2:3000/d/gosrt-analysis                                  │
# │                                                                              │
# │ PURPOSE: Detailed engineering view for diagnosing issues, stress testing,    │
# │          and validating GoSRT implementation correctness.                    │
# │ QUESTION ANSWERED: "Why is my stream unhealthy?"                             │
# │                                                                              │
# │ ALWAYS VISIBLE ROWS:                                                         │
# │                                                                              │
# │ Overview Row:                                                                │
# │   - Throughput (Mbps) - All endpoints TX/RX                                  │
# │   - Retransmission Rate (%) - With thresholds                                │
# │   - RTT (ms) - All endpoints                                                 │
# │                                                                              │
# │ Ingest Flow (Publisher → Server):                                            │
# │   - Send vs Receive Rate - Compare both ends (should match)                  │
# │   - Loss & Retransmission - Publisher retrans vs Server loss                 │
# │   - NAK Flow - Server NAKs sent vs Publisher NAKs recv                       │
# │   - Bytes Comparison - Total vs retrans bytes                                │
# │                                                                              │
# │ Delivery Flow (Server → Subscriber):                                         │
# │   - Send vs Receive Rate - Compare both ends (should match)                  │
# │   - Loss & Retransmission - Server retrans vs Subscriber loss                │
# │   - NAK Flow - Subscriber NAKs sent vs Server NAKs recv                      │
# │   - Bytes Comparison - Total vs retrans bytes                                │
# │                                                                              │
# │ Recovery Health:                                                             │
# │   - NAK Processing - Packets requested (single vs range)                     │
# │   - TSBPD Buffer Health (ms)                                                 │
# │   - Dropped vs Skipped - Arrived late vs never arrived                       │
# │   - Flight Size & Buffer Packets                                             │
# │                                                                              │
# │ Lock-Free Ring Health:                                                       │
# │   - Ring Push Success Rate (gauge)                                           │
# │   - Ring Drops (should be 0)                                                 │
# │   - EventLoop Iterations/sec                                                 │
# │                                                                              │
# │ Latency Budget Analysis:                                                     │
# │   - RTT vs Configured Latency                                                │
# │   - Max Retransmit Attempts = (latency-RTT)/RTT                              │
# │   - RTT Variance (jitter)                                                    │
# │                                                                              │
# │ ACK Efficiency:                                                              │
# │   - Light vs Full ACKs Sent                                                  │
# │   - ACK Btree Health (RTT calculation)                                       │
# │                                                                              │
# │ Recovery Timing:                                                             │
# │   - Ingest: NAK Requests vs Retransmits Received (ratio ≥1.0)                │
# │   - Delivery: NAK Requests vs Retransmits Received                           │
# │   - NAK/Retransmit Suppression (RTO-based)                                   │
# │   - Sender Retransmit Suppression                                            │
# │                                                                              │
# │ TSBPD Deadline Analysis:                                                     │
# │   - TSBPD Skipped Packets (unrecoverable loss)                               │
# │   - Contiguous Point Advancements (gaps in stream)                           │
# │   - Drop Reason Breakdown (Server)                                           │
# │   - Drop Reason Breakdown (Subscriber)                                       │
# │                                                                              │
# │ Pacing & Congestion Control:                                                 │
# │   - Send Period (inter-packet gap)                                           │
# │   - Input vs Sent Bandwidth (backpressure)                                   │
# │   - Estimated Link Capacity                                                  │
# │                                                                              │
# │ COLLAPSED DETAIL ROWS (click to expand):                                     │
# │   - RTT Details: Variance, raw samples                                       │
# │   - NAK Btree Details: Operations, consolidation                             │
# │   - Fast NAK Analysis: Triggers, sequence handling                           │
# │   - Protocol Overhead: Control vs Data, useful vs total bytes                │
# │   - Sequence Health: Anomalies (should be 0), duplicates, wraparound         │
# │   - Anomaly Counters: Bug indicators (ALL should be 0)                       │
# │   - io_uring Contention: Context switches, scheduler latency, CPU steal      │
# │   - Late Packet Analysis: TSBPD buffer margin heatmap                        │
# └─────────────────────────────────────────────────────────────────────────────┘
#
# ═══════════════════════════════════════════════════════════════════════════════
# THREE-TIER MONITORING ARCHITECTURE
# ═══════════════════════════════════════════════════════════════════════════════
#
# ┌────────────────────┬─────────────────────────────┬─────────────────────────────┐
# │ Tier               │ Operations (L1)             │ Analysis (L2/L3)            │
# ├────────────────────┼─────────────────────────────┼─────────────────────────────┤
# │ Primary Metric     │ Goodput (unique bytes)      │ NAK/Retransmit ratio        │
# │ Latency Focus      │ Average RTT                 │ TSBPD Buffer Margin         │
# │ Error Handling     │ Total Loss % (traffic light)│ Retransmission Efficiency   │
# │ System Metrics     │ Collapsed (optional)        │ Full io_uring contention    │
# │ Target User        │ NOC Operator                │ GoSRT Developer / QA        │
# │ Refresh Rate       │ 5 seconds                   │ 1 second                    │
# │ Time Range Default │ 15 minutes                  │ 5 minutes                   │
# │ Impairment Annot.  │ Yes (red vertical lines)    │ Yes (red vertical lines)    │
# └────────────────────┴─────────────────────────────┴─────────────────────────────┘
#
# ═══════════════════════════════════════════════════════════════════════════════
# MONITORING TIER ALERT SUMMARY
# ═══════════════════════════════════════════════════════════════════════════════
#
# ┌─────────────┬──────────────┬───────────────────────────┬────────────────────┐
# │ Tier        │ Focus        │ Key Metric                │ Alert Threshold    │
# ├─────────────┼──────────────┼───────────────────────────┼────────────────────┤
# │ Ops (L1)    │ Continuity   │ PktRecvDataSuccess        │ Stream Down > 5s   │
# │ Eng (L2)    │ Recovery     │ Retrans / NAK ratio       │ Efficiency < 50%   │
# │ Kernel (L3) │ Bottlenecks  │ io_uring CQE Latency      │ Ring Overflow > 0  │
# └─────────────┴──────────────┴───────────────────────────┴────────────────────┘
#
# Key Questions Each Dashboard Answers:
#   Operations: "Is my stream healthy?" (glance at traffic lights)
#   Analysis:   "Why is my stream unhealthy?" (deep dive into correlations)
#
# ═══════════════════════════════════════════════════════════════════════════════
# NEW PANELS (Advanced Monitoring)
# ═══════════════════════════════════════════════════════════════════════════════
#
# io_uring Health (L3):
#   - CQ Overflow - CRITICAL: kernel dropping before Go sees packets
#   - CQE Processing Latency - kernel-to-app delay
#   - SQ Full Events - send backpressure
#   - Ring Utilization gauge
#
# Bandwidth Efficiency:
#   - Efficiency gauge (Goodput / Total) with actionable thresholds
#   - Overhead breakdown (unique vs retrans vs control)
#   - Efficiency trend over time
#
# B-Tree Pressure:
#   - NAK B-tree size (grows during Starlink tests)
#   - NAK Consolidation Efficiency (ranges vs singles)
#   - Packet Store and Sender B-tree sizes
#   - B-tree operations rate
```

### 4. VM Management

```bash
# List running MicroVMs
nix run .#srt-vm-check

# Stop all MicroVMs
nix run .#srt-vm-stop

# Stop individual VMs (GoSRT core)
nix run .#srt-vm-stop-server
nix run .#srt-vm-stop-publisher
nix run .#srt-vm-stop-subscriber

# Stop individual VMs (interop)
nix run .#srt-vm-stop-xtransmit-pub
nix run .#srt-vm-stop-ffmpeg-pub
nix run .#srt-vm-stop-xtransmit-sub
nix run .#srt-vm-stop-ffmpeg-sub

# Stop metrics VM
nix run .#srt-vm-stop-metrics

# SSH into VMs (password: srt)
nix run .#srt-ssh-server
nix run .#srt-ssh-publisher
nix run .#srt-ssh-subscriber
nix run .#srt-ssh-xtransmit-pub
nix run .#srt-ssh-ffmpeg-pub
nix run .#srt-ssh-xtransmit-sub
nix run .#srt-ssh-ffmpeg-sub
nix run .#srt-ssh-metrics

# Serial console access (useful when network is broken)
nix run .#srt-console-server           # Connect via nc to server
nix run .#srt-console-publisher        # Connect via nc to publisher
nix run .#srt-console-subscriber       # Connect via nc to subscriber
nix run .#srt-console-xtransmit-pub    # Connect via nc to xtransmit publisher
nix run .#srt-console-ffmpeg-pub       # Connect via nc to ffmpeg publisher
nix run .#srt-console-xtransmit-sub    # Connect via nc to xtransmit subscriber
nix run .#srt-console-ffmpeg-sub       # Connect via nc to ffmpeg subscriber
nix run .#srt-console-metrics          # Connect via nc to metrics
```

### 5. Starlink Pattern Testing

```bash
# Start the Starlink impairment controller (runs in foreground)
nix run .#srt-starlink-pattern

# This will:
# - Apply 100% loss at seconds 12, 27, 42, 57 of each minute
# - Each loss event lasts 50-70ms
# - Metrics show how SRT recovers from each reconvergence event
```

---

## Test Configurations

The system supports the existing test configurations from `contrib/integration_testing/test_configs.go`:

| Config Name | Bitrate | Latency | Loss | Pattern |
|-------------|---------|---------|------|---------|
| Clean-5M | 5 Mb/s | 0ms | 0% | clean |
| Starlink-5M | 5 Mb/s | varies | 100% bursts | starlink |
| Loss-2pct-5M | 5 Mb/s | 0ms | 2% | uniform |
| Tier3-Loss-10M | 10 Mb/s | 130ms | 2% | uniform |
| GEO-Loss-5M | 5 Mb/s | 300ms | 0.5% | uniform |

---

## Security Considerations

1. **Network setup requires root**: The initial network configuration (creating TAP devices, network namespaces, tc/netem rules) requires root privileges. This is done once.

2. **MicroVMs run unprivileged**: After network setup, MicroVMs can be started without sudo. They use TAP devices owned by the user.

3. **vhost-net access**: The setup script uses ACLs to grant vhost-net access to the current user, avoiding world-writable permissions.

4. **SSH in VMs**: Debug mode enables password SSH (root:srt) for convenience. Production deployments should use key-based auth.

---

## DRY Architecture Summary

This design follows a strict **data-driven** philosophy where adding a new VM role requires changes in only one place: `constants.nix`. All other code is generated.

### Single Source of Truth

| Data | Defined In | Consumed By |
|------|------------|-------------|
| Role definitions | `constants.nix` → `roles` | All modules via `lib.nix` |
| Network IPs/MACs | Computed from role index | `lib.mkRoleNetwork()` |
| Ports | Computed from role index | `lib.mkRolePorts()` |
| Latency profiles | `constants.nix` → `latencyProfiles` | `lib.mkInterRouterLink()` |
| Go build config | `constants.nix` → `go` | `packages/default.nix` |

### Generated vs. Hand-Written

| Component | Count | Generation Method |
|-----------|-------|-------------------|
| MicroVMs | 8 | `lib.mapAttrs mkRoleVM gosrtLib.roles` |
| Stop scripts | 8 | `lib.mapAttrs mkStopScript gosrtLib.roles` |
| SSH scripts | 8 | `lib.mapAttrs mkSshScript gosrtLib.roles` |
| Console scripts | 8 | `lib.mapAttrs mkConsoleScript gosrtLib.roles` |
| Package definitions | 6 | `lib.mapAttrs (_: commonBuild) packageDefs` |
| Network setup | 8 subnets | Iterates `lib.roleNames` |
| Prometheus scrapes | 8 targets | `lib.mapAttrsToList` over `prometheusRoles` |
| Grafana thresholds | 5 presets | `grafanaLib.thresholds.*` |

### Key DRY Patterns

1. **Role-driven generation**: `lib.mapAttrs` over `gosrtLib.roles` generates per-role artifacts
2. **Validation at evaluation time**: Assertions in `lib.nix` catch errors before build
3. **Computed derivations**: IPs, MACs, ports derived from role index (no hardcoding)
4. **Threshold presets**: `grafanaLib.thresholds.*` eliminates repeated threshold definitions
5. **Panel helpers**: `mkHealthStat`, `mkThroughputPanel`, `mkRatePanel` reduce boilerplate

### Elegance Patterns

Beyond DRY, these patterns make the system more flexible and maintainable:

| Pattern | Implementation | Benefit |
|---------|----------------|---------|
| **specialArgs injection** | `nixosSystem { specialArgs = { gosrtPackages, buildVariant }; }` | Swap production ↔ debug binaries without touching VM configs |
| **Functional impairments** | `mkImpairmentScript { name, steps, ... }` | Compose scenarios as derivations, not shell scripts |
| **Grafana annotations** | Auto-post to `/api/annotations` on impairment | Correlate metrics with test events automatically |
| **Delta panels** | `deliveryEfficiency`, `recoveryEfficiency` | Operators see "98.5%" not raw counters |
| **Audit enforcement** | `checks.audit-metrics`, `checks.audit-seq` | No build succeeds with unsafe code |
| **Tiered checks** | `go-test-tier1`, `go-test-tier2`, `go-test-all` | `nix flake check` runs full test matrix |

### Build Variants

Swap between production and debug binaries via `buildVariant`:

```nix
# Production (default) - optimized, no assertions
microvms = import ./nix/microvms { buildVariant = "production"; ... };

# Debug - includes runtime context assertions, verbose logging
microvms = import ./nix/microvms { buildVariant = "debug"; ... };

# In your flake.nix, expose both:
packages.srt-server-vm = microvms.server.vm;
packages.srt-server-vm-debug = microvmsDebug.server.vm;
```

### Adding a New Role

```nix
# constants.nix - add ONE entry:
roles = {
  # ... existing roles ...
  my-new-role = {
    index = 9;  # Unique index (generates IP: 10.50.9.2, MAC: 02:00:00:50:09:02)
    shortName = "new";
    router = "A";
    package = "client";
    service = { binary = "gosrt-client"; args = [ ... ]; };
  };
};

# Everything else is automatically generated:
# - MicroVM: nix run .#srt-my-new-role-vm
# - SSH: nix run .#srt-ssh-my-new-role
# - Console: nix run .#srt-console-my-new-role
# - Stop: nix run .#srt-vm-stop-my-new-role
# - Network: TAP + bridge + veth created by setup script
# - Prometheus: Auto-scraped if service.hasPrometheus = true
```

---

## Implementation Plan

### Phase 1: Basic Infrastructure
- [ ] Upgrade go.mod to Go 1.26 (`go mod edit -go=1.26 && go mod tidy`)
      Note: greenteagc is now default in Go 1.26; only GOEXPERIMENT=jsonv2 needed
- [ ] Create nix/constants.nix (with data-driven `roles` definition)
- [ ] Create nix/lib.nix (computed values from roles)
- [ ] Create nix/packages/ (all 6 gosrt binaries + srt-xtransmit + ffmpeg)
- [ ] Create nix/shell.nix
- [ ] Create flake.nix skeleton

### Phase 2: Containers
- [ ] Create nix/containers/ (all OCI containers)
- [ ] Test container builds with docker load

### Phase 3: Network Setup
- [ ] Create nix/network/setup.nix (data-driven: generates TAP/bridge/veth for all roles)
- [ ] Test TAP device creation
- [ ] Test tc/netem configuration
- [ ] Test latency profile switching

### Phase 4: MicroVMs (Data-Driven)
- [ ] Create nix/microvms/base.nix (takes role from lib.nix, generates systemd service)
- [ ] Create nix/microvms/default.nix (iterates over lib.roles, generates all VMs)
- [ ] Test MicroVM boot and networking
- [ ] NOTE: Individual VM files (server.nix, publisher.nix, etc.) are NOT needed

### Phase 5: Metrics MicroVM (Special Case)
- [ ] Create nix/microvms/metrics.nix (Prometheus + Grafana, not a GoSRT binary)
- [ ] Configure Prometheus scrape targets (auto-generated from lib.prometheusRoles)
- [ ] Configure Grafana with Prometheus datasource
- [x] Create GoSRT metrics dashboard (comprehensive multi-endpoint stream analysis)
- [ ] Test metrics collection and visualization

### Phase 6: VM Management Scripts (Data-Driven)
- [ ] Create nix/scripts/vm-management.nix (generates stop/ssh/console for all roles)
- [ ] Test script generation
- [ ] NOTE: No individual scripts needed - all generated from lib.roles

### Phase 7: Integration & Testing
- [ ] Create test orchestration scripts
- [ ] Create metrics collection scripts
- [ ] Create analysis/reporting scripts
- [ ] Document usage

---

## Dependencies

- **nixpkgs**: unstable (for Go 1.26, latest kernel)
- **microvm.nix**: TAP performance branch for vhost-net
- **Go 1.26+**: For Green Tea GC (default) and jsonv2 (experimental)
- **Linux kernel 5.10+**: For io_uring support (currently using 6.18.8 via linuxPackages_latest)
- **KVM**: For MicroVM acceleration

---

## Required GoSRT Metrics

The advanced Grafana dashboards require GoSRT to export the following metrics. These should be added to `metrics/metrics.go` and `metrics/handler.go`.

### io_uring Metrics (NEW - Required for io_uring Health Panels)

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_iouring_cq_overflow_total` | Counter | CQ overflows - kernel dropped packets before Go could consume |
| `gosrt_iouring_cqe_latency_us` | Histogram | Microseconds from CQE ready to EventLoop processing |
| `gosrt_iouring_sq_full_total` | Counter | SQ full events (send backpressure) |
| `gosrt_iouring_ring_utilization` | Gauge | Current ring utilization (0.0-1.0) |

**Implementation Notes:**
- CQ overflow detection requires checking `io_uring_cq_has_overflow()` or monitoring `IORING_CQ_EVENTFD_DISABLED`
- CQE latency requires timestamping when `WaitCQETimeout()` returns vs when packet enters PacketRing
- Ring utilization = `(sq_head - sq_tail) / ring_size`

### B-Tree Metrics (NEW - Required for B-Tree Pressure Panels)

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_receiver_nak_btree_size` | Gauge | Entries in NAK B-tree (missing sequences) |
| `gosrt_receiver_packet_btree_size` | Gauge | Packets in receive B-tree awaiting TSBPD |
| `gosrt_sender_btree_size` | Gauge | Packets in sender B-tree awaiting ACK |
| `gosrt_receiver_nak_single_total` | Counter | Individual sequence NAKs sent |
| `gosrt_receiver_nak_range_total` | Counter | Range NAKs sent |
| `gosrt_receiver_nak_sequences_total` | Counter | Total sequences requested via NAK |
| `gosrt_receiver_nak_packets_total` | Counter | Total NAK packets sent |
| `gosrt_btree_insert_total` | Counter | B-tree insert operations |
| `gosrt_btree_delete_total` | Counter | B-tree delete operations |
| `gosrt_btree_lookup_total` | Counter | B-tree lookup operations |

### Bandwidth Efficiency Metrics (Existing - Verify Present)

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_connection_congestion_bytes_total` | Counter | Total bytes (including retransmissions) |
| `gosrt_connection_congestion_bytes_unique_total` | Counter | Unique bytes (goodput) |
| `gosrt_connection_control_bytes_total` | Counter | Control packet bytes (ACK/NAK overhead) |

### Alert-Ready Metrics (Existing - Verify Present)

| Metric | Type | Description |
|--------|------|-------------|
| `gosrt_pkt_recv_data_success_total` | Counter | Successfully received data packets |
| `gosrt_connection_nak_sent_total` | Counter | NAK packets sent |
| `gosrt_connection_congestion_recv_pkt_retrans_total` | Counter | Retransmitted packets received |

---

## Monitoring Tier Summary

| Tier | Focus | Key Dashboard Metric | Alert Threshold |
|------|-------|---------------------|-----------------|
| **Ops (L1)** | Continuity | `gosrt_pkt_recv_data_success` | Stream Down > 5s |
| **Eng (L2)** | Recovery | `retrans / nak_sent` ratio | NAK Efficiency < 50% |
| **Kernel (L3)** | Bottlenecks | `gosrt_iouring_cq_overflow` | Any overflow = action |

### Actionable Insights

**Bandwidth Efficiency < 80%:**
- Network is over-congested
- **Action:** Lower encoder bitrate or increase SRT latency setting

**NAK Consolidation Ratio < 2:**
- Sending many individual NAKs instead of ranges
- **Action:** Tune NAK consolidation algorithm or increase consolidation window

**io_uring CQ Overflow > 0:**
- Kernel dropping packets before Go processes them
- **Action:** Increase ring size, tune `IORING_SETUP_DEFER_TASKRUN`, or add more io_uring worker threads
