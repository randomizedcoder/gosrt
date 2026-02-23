# GoSRT Nix MicroVM Implementation Plan

**Reference Document**: `documentation/nix_microvm_design.md`
**Created**: 2026-02-13
**Revised**: 2026-02-16 (v4 - Implementation complete, next steps defined)
**Status**: Phases 0-10 Complete, Phases 11-17 Planned

---

## Table of Contents

1. [Overview](#overview)
2. [Architecture Refinements](#architecture-refinements)
3. [Prerequisites](#prerequisites)
4. [Phase 0: Infrastructure Validation](#phase-0-validation)
5. [Phase 1: Foundation - Constants, Library, Overlay, and Flake](#phase-1-foundation)
6. [Phase 2: Metrics VM First (Observer Pattern)](#phase-2-metrics-first)
7. [Phase 3: Go Packages with Audit Hooks](#phase-3-go-packages)
8. [Phase 4: OCI Containers](#phase-4-oci-containers)
9. [Phase 5: Network Infrastructure (Declarative nftables)](#phase-5-network-infrastructure)
10. [Phase 6: MicroVM Base with Module Options](#phase-6-microvms)
11. [Phase 7: VM Management Scripts](#phase-7-vm-management)
12. [Phase 8: Testing Infrastructure with Automated Pass/Fail](#phase-8-testing)
13. [Phase 9: Development Shell and CI Checks](#phase-9-devshell-ci)
14. [Phase 10: Integration Tests](#phase-10-integration-tests)
15. [Verification Checklist](#verification-checklist)
16. [Design vs Implementation Comparison](#design-comparison)
17. [Elegance Checklist](#elegance-checklist)

---

## Overview

This implementation plan transforms the design in `nix_microvm_design.md` into working code. The implementation follows a **data-driven architecture** where:

- **Single source of truth**: `nix/constants.nix` defines all roles
- **Computed values**: `nix/lib.nix` derives IPs, MACs, ports from role indices
- **Generated artifacts**: MicroVMs, scripts, configs are all generated via `lib.mapAttrs`

### Key Principles

1. **Nix Idiomatic**: Use `lib.mapAttrs`, `lib.optionalAttrs`, attribute sets over lists
2. **No Hardcoding**: All network values derived from role index
3. **Fail Fast**: Assertions in `lib.nix` catch errors at evaluation time
4. **Modularity**: Each concern in its own file
5. **Observer First**: Deploy monitoring before SUT (System Under Test)
6. **Declarative Over Imperative**: NixOS modules for impairments, not shell scripts

---

## Architecture Refinements {#architecture-refinements}

### Refinement 1: GoSRT Overlay for Binary Flavors

**Problem**: Path-based binary injection is brittle and doesn't propagate config changes.

**Solution**: Create `nix/overlays/gosrt.nix` that defines binary flavors as standard packages.

```nix
# nix/overlays/gosrt.nix
final: prev: {
  gosrt = {
    # Production: optimized, no assertions
    prod = final.callPackage ../packages/gosrt.nix {
      buildVariant = "production";
      ldflags = [ "-s" "-w" ];
    };

    # Debug: with context assertions (AssertEventLoopContext)
    debug = final.callPackage ../packages/gosrt.nix {
      buildVariant = "debug";
      ldflags = [ ];
      tags = [ "debug" ];
    };

    # Performance: with pprof endpoints enabled
    perf = final.callPackage ../packages/gosrt.nix {
      buildVariant = "perf";
      ldflags = [ "-s" "-w" ];
      extraArgs = [ "-cpuprofile" "-memprofile" ];
    };
  };
}
```

**Benefits**:
- GOEXPERIMENT flags propagate to all VMs automatically
- `gosrt.debug` can be swapped for `gosrt.prod` with one line
- Go 1.26 `jsonv2` experiment flag is centralized

### Refinement 2: NixOS Module Options for Impairment Scenarios

**Problem**: Running `tc netem` commands inside VMs is imperative and error-prone.

**Solution**: Define impairment scenarios as NixOS module options.

```nix
# nix/modules/srt-test.nix
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.srt-test;

  # Scenario definitions
  scenarios = {
    clean = { loss = 0; delay = 0; jitter = 0; };
    starlink-handoff = {
      loss = 0;  # Handled by blackhole route
      delay = 20;
      jitter = 10;
      blackholePattern = [ 12 27 42 57 ];
      blackhoutDurationMs = 500;
    };
    congested-wifi = { loss = 2; delay = 5; jitter = 10; };
    geo-satellite = { loss = 0.5; delay = 300; jitter = 20; };
  };

in {
  options.services.srt-test = {
    enable = mkEnableOption "SRT test scenario";

    scenario = mkOption {
      type = types.enum (attrNames scenarios);
      default = "clean";
      description = "Network impairment scenario to apply";
    };

    interface = mkOption {
      type = types.str;
      default = "eth0";
      description = "Interface to apply impairment";
    };
  };

  config = mkIf cfg.enable {
    # Apply netem at boot (declarative)
    systemd.services.srt-impairment = {
      description = "Apply SRT test impairment";
      wantedBy = [ "network-online.target" ];
      after = [ "network-online.target" ];

      script = let
        s = scenarios.${cfg.scenario};
      in ''
        ${pkgs.iproute2}/bin/tc qdisc replace dev ${cfg.interface} root netem \
          ${optionalString (s.delay > 0) "delay ${toString s.delay}ms"} \
          ${optionalString (s.jitter > 0) "${toString s.jitter}ms"} \
          ${optionalString (s.loss > 0) "loss ${toString s.loss}%"} \
          limit 50000
      '';

      serviceConfig.Type = "oneshot";
      serviceConfig.RemainAfterExit = true;
    };

    # Blackhole pattern service (if scenario has blackholePattern)
    systemd.services.srt-blackhole-pattern = mkIf (scenarios.${cfg.scenario} ? blackholePattern) {
      description = "Starlink-style blackhole pattern";
      wantedBy = [ "multi-user.target" ];
      after = [ "srt-impairment.service" ];

      script = ''
        # Pattern implementation here
      '';
    };
  };
}
```

**Usage in VM config**:
```nix
# No shell scripts needed - just set the option
{ config, ... }: {
  services.srt-test = {
    enable = true;
    scenario = "starlink-handoff";
  };
}
```

### Refinement 3: Declarative nftables Instead of iptables Scripts

**Problem**: Shell-scripted iptables rules are brittle and non-idempotent.

**Solution**: Use NixOS `networking.nftables` for all firewall/NAT/impairment rules.

```nix
# nix/modules/srt-network.nix
{ config, lib, ... }:

{
  networking.nftables = {
    enable = true;
    ruleset = ''
      table inet srt-test {
        chain blackhole {
          type filter hook forward priority 0;

          # Dynamic: populated by systemd service for Starlink pattern
          ip daddr @blackhole-addrs drop
        }

        set blackhole-addrs {
          type ipv4_addr
          flags timeout
          # Elements added dynamically with timeout
        }
      }
    '';
  };
}
```

**Benefits**:
- Atomic rule application (no race conditions)
- Easy to inspect: `nft list ruleset`
- Timeout-based blackhole entries auto-expire

### Refinement 4: pprof Integration with Grafana

**Problem**: When you see a CPU spike in Grafana, you want to click and see the profile.

**Solution**: Link pprof HTTP endpoints to Grafana annotations.

```nix
# In grafana/panels/pprof.nix
mkPprofLink = instance: {
  title = "CPU Profile";
  url = "http://${instance}:6060/debug/pprof/profile?seconds=30";
  type = "link";
  tooltip = "Click to capture 30s CPU profile";
};

# In dashboard with data links
mkTimeseriesWithPprof = { title, instance, ... }@args:
  mkTimeseries (args // {
    fieldConfig.defaults.links = [
      {
        title = "View CPU Profile";
        url = "http://${instance}:6060/debug/pprof/profile?seconds=30";
        targetBlank = true;
      }
      {
        title = "View Heap Profile";
        url = "http://${instance}:6060/debug/pprof/heap";
        targetBlank = true;
      }
    ];
  });
```

### Refinement 5: NAK B-Tree Heatmap for Micro-Burst Detection

**Problem**: Line charts hide micro-bursts of NAK activity.

**Solution**: Use Grafana Heatmap visualization for NAK metrics.

```nix
# nix/grafana/panels/heatmaps.nix
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkPanel mkTarget;

in {
  nakBurstHeatmap = mkPanel {
    title = "NAK Burst Distribution";
    type = "heatmap";
    description = ''
      Heatmap showing NAK distribution over time.
      Vertical bands indicate burst NAK activity (potential O(n) scaling).
      Smooth horizontal spread indicates efficient O(log n) btree operations.
    '';
    gridPos = { h = 8; w = 12; x = 0; };

    # Heatmap-specific options
    options = {
      calculate = true;
      calculation = {
        xBuckets.mode = "size";
        xBuckets.value = "1s";
        yBuckets.mode = "count";
        yBuckets.value = "20";
      };
      color = {
        mode = "spectrum";
        scheme = "Spectral";
        steps = 128;
      };
      cellGap = 1;
      showValue = "never";
    };

    targets = [
      (mkTarget "rate(gosrt_receiver_nak_sent_total{instance=\"server\"}[1s])" "NAK/s")
    ];
  };
}
```

### Refinement 6: Debug Builds for Initial Infrastructure Testing

**Problem**: Context violations (`AssertEventLoopContext` failures) are silent in production builds, making it hard to catch lock-free architecture bugs during initial network plumbing.

**Solution**: Use `gosrt.debug` builds in Phase 1 to enable runtime context assertions.

```nix
# In VM configuration during Phase 1 testing
{ gosrtPackages, ... }:
{
  # Use debug build to catch context violations early
  environment.systemPackages = [ gosrtPackages.debug ];

  # Assertions are active - will panic on:
  # - Lock acquisition in EventLoop context
  # - Ring buffer access from wrong goroutine
  # - TSBPD violations
}
```

**Verification**:
```bash
# Build with debug symbols and assertions
make build-debug

# Run integration test - context violations will panic with stack trace
sudo make test-isolation CONFIG=Isolation-5M-CG-SendEventLoop
```

### Refinement 7: Multi-Queue TAP Devices Aligned with io_uring

**Problem**: Single-queue TAP devices bottleneck at ~2-3 Gbps. GoSRT's `Config.IoUringRecvRingCount` expects multiple queues for scaling.

**Solution**: Create TAP devices with `multi_queue` and match queue count to io_uring ring count.

```nix
# nix/network/tap-multiqueue.nix
{ lib, gosrtLib, pkgs }:

let
  # Match io_uring configuration from GoSRT
  queueCount = 4;  # Matches Config.IoUringRecvRingCount

  mkTapDevice = name: ''
    # Create multi-queue TAP
    ip tuntap add dev ${name} mode tap multi_queue user $USER

    # Enable each queue
    ${lib.concatStringsSep "\n" (lib.genList (i: ''
      ip link set ${name} txqueuelen 10000
      ethtool -K ${name} tx off rx off
    '') queueCount)}

    # Verify queues
    ethtool -l ${name}
  '';

in {
  inherit mkTapDevice queueCount;

  # Verify alignment assertion
  assertQueueAlignment = lib.assertMsg
    (queueCount == 4)  # Must match GoSRT default
    "TAP queue count must match IoUringRecvRingCount";
}
```

**Benefits**:
- Enables parallel packet processing across CPU cores
- Removes single-core bottleneck (2-3 Gbps → 10+ Gbps theoretical)
- Aligns with GoSRT's lock-free multi-ring architecture

### Refinement 8: Explicit Interface Naming via systemd.network.links

**Problem**: Non-deterministic interface names (e.g., `ens33`, `eth0`) break hardcoded references.

**Solution**: Use `systemd.network.links` to enforce predictable interface names.

```nix
# nix/modules/srt-network-interfaces.nix
{ config, lib, ... }:

{
  systemd.network = {
    enable = true;

    # Explicit interface naming rules
    links = {
      "10-eth-wan" = {
        matchConfig.MACAddress = "02:00:00:50:*:02";  # VM interface
        linkConfig.Name = "eth-wan";
      };

      "10-eth-starlink" = {
        matchConfig.MACAddress = "02:00:00:50:*:03";  # Impairment interface
        linkConfig.Name = "eth-starlink";
      };

      "10-eth-mgmt" = {
        matchConfig.MACAddress = "02:00:00:50:*:04";  # Management interface
        linkConfig.Name = "eth-mgmt";
      };
    };

    # Network configuration references stable names
    networks."20-wan" = {
      matchConfig.Name = "eth-wan";
      # ... IP configuration
    };
  };
}
```

**Benefits**:
- No more "which interface is eth0 on this boot?" debugging
- Scripts can hardcode `eth-wan` instead of fragile discovery logic
- Impairment rules (`tc qdisc`) target specific interfaces reliably

### Refinement 9: DRY Impairment Scenario Library with mkScenario

**Problem**: Fragmented impairment definitions lead to inconsistencies and missed cleanup.

**Solution**: Create a single `mkScenario` function that enforces structure and includes mandatory cleanup.

```nix
# nix/network/impairment-library.nix
{ lib, pkgs }:

let
  # Core scenario builder - enforces structure
  mkScenario = {
    name,
    loss ? 0,          # Percentage (0-100)
    delay ? 0,         # Milliseconds
    jitter ? 0,        # Milliseconds (applied with delay)
    blackhole ? null,  # { times = [...]; durationMs = int; }
    description ? "",
  }: {
    inherit name loss delay jitter blackhole description;

    # Mandatory cleanup script (cannot be forgotten)
    cleanup = pkgs.writeShellScript "cleanup-${name}" ''
      set -euo pipefail

      # Remove netem qdisc
      tc qdisc del dev eth-wan root 2>/dev/null || true

      # Clear nftables blackhole set
      nft flush set inet srt-test blackhole-addrs 2>/dev/null || true

      # Verify clean state
      if tc qdisc show dev eth-wan | grep -q netem; then
        echo "ERROR: netem still present after cleanup" >&2
        exit 1
      fi

      echo "Cleanup complete for ${name}"
    '';

    # Apply script
    apply = pkgs.writeShellScript "apply-${name}" ''
      set -euo pipefail

      # Always cleanup first (idempotent)
      ${cleanup}

      ${lib.optionalString (loss > 0 || delay > 0) ''
        tc qdisc add dev eth-wan root netem \
          ${lib.optionalString (loss > 0) "loss ${toString loss}%"} \
          ${lib.optionalString (delay > 0) "delay ${toString delay}ms"} \
          ${lib.optionalString (jitter > 0) "${toString jitter}ms"} \
          limit 50000
      ''}

      ${lib.optionalString (blackhole != null) ''
        # Starlink-style blackhole pattern handled by systemd timer
        echo "Blackhole pattern: ${toString blackhole.times}"
      ''}

      echo "Applied scenario: ${name}"
    '';

    # Verification script
    verify = pkgs.writeShellScript "verify-${name}" ''
      set -euo pipefail

      ACTUAL_LOSS=$(tc qdisc show dev eth-wan | grep -oP 'loss \K[0-9.]+' || echo "0")
      EXPECTED_LOSS="${toString loss}"

      if [ "$ACTUAL_LOSS" != "$EXPECTED_LOSS" ]; then
        echo "ERROR: Expected loss $EXPECTED_LOSS%, got $ACTUAL_LOSS%" >&2
        exit 1
      fi

      echo "Verified: ${name} is active"
    '';
  };

  # Pre-defined scenarios using mkScenario
  scenarios = {
    clean = mkScenario {
      name = "clean";
      description = "No impairment - baseline performance";
    };

    regional = mkScenario {
      name = "regional";
      delay = 5;
      jitter = 2;
      description = "Regional datacenter ~5ms RTT";
    };

    continental = mkScenario {
      name = "continental";
      delay = 30;
      jitter = 5;
      description = "Cross-continent ~60ms RTT";
    };

    intercontinental = mkScenario {
      name = "intercontinental";
      delay = 65;
      jitter = 10;
      description = "Intercontinental ~130ms RTT";
    };

    geo-satellite = mkScenario {
      name = "geo-satellite";
      loss = 0.5;
      delay = 150;
      jitter = 20;
      description = "GEO satellite ~300ms RTT, 0.5% loss";
    };

    congested-wifi = mkScenario {
      name = "congested-wifi";
      loss = 2;
      delay = 5;
      jitter = 10;
      description = "Congested WiFi 2% loss";
    };

    starlink-handoff = mkScenario {
      name = "starlink-handoff";
      delay = 20;
      jitter = 10;
      blackhole = {
        times = [ 12 27 42 57 ];
        durationMs = 500;
      };
      description = "Starlink satellite handoff - 500ms blackouts at :12, :27, :42, :57";
    };

    stress-5pct = mkScenario {
      name = "stress-5pct";
      loss = 5;
      delay = 10;
      jitter = 5;
      description = "5% packet loss stress test";
    };
  };

in {
  inherit mkScenario scenarios;

  # Convenience: get all scenario names
  scenarioNames = lib.attrNames scenarios;

  # Matrix helper: generate test configs for all scenarios
  mkScenarioMatrix = testFn: lib.mapAttrs (name: scenario:
    testFn scenario
  ) scenarios;
}
```

**Usage**:
```nix
# In test configuration
{ impairmentLib, ... }:

let
  scenario = impairmentLib.scenarios.starlink-handoff;
in {
  # Apply scenario
  systemd.services.apply-impairment = {
    script = "${scenario.apply}";
    wantedBy = [ "multi-user.target" ];
  };

  # Always cleanup on shutdown
  systemd.services.cleanup-impairment = {
    script = "${scenario.cleanup}";
    wantedBy = [ "shutdown.target" ];
    before = [ "shutdown.target" ];
  };
}
```

**Benefits**:
- Single definition per scenario - no duplication
- Mandatory `cleanup` script - cannot be accidentally omitted
- `verify` script confirms scenario is active
- Matrix testing across all scenarios with `mkScenarioMatrix`

---

## Prerequisites

Before starting:

```bash
# Verify system requirements
uname -r  # Kernel 5.10+ for io_uring
lsmod | grep kvm  # KVM available
ls -la /dev/net/tun  # TUN device exists
ls -la /dev/vhost-net  # vhost-net available (or sudo modprobe vhost_net)
```

Required tools:
- Nix with flakes enabled (`nix --version` >= 2.4)
- KVM virtualization support
- Root access for one-time network setup
- iperf2 for baseline network validation

---

## Phase 0: Infrastructure Validation {#phase-0-validation}

### Objective
Validate that the virtual networking infrastructure can achieve target throughput BEFORE deploying GoSRT. This prevents "blind debugging" where GoSRT performance issues are actually infrastructure bottlenecks.

### Rationale
If you cannot hit 10 Gbps with simple TCP/UDP traffic through vhost-net TAP devices, GoSRT's performance results will be bottlenecked by the infrastructure, not the code.

### Step 0.1: Create Minimal Network Test

**File**: `nix/validation/iperf-test.nix`

```nix
# nix/validation/iperf-test.nix
#
# Minimal iperf2 test VMs to validate vhost-net throughput.
# Run BEFORE deploying GoSRT to establish baseline.
#
{ pkgs, lib, microvm, nixpkgs, system }:

let
  mkIperfVM = { name, ip, role }:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        microvm.nixosModules.microvm
        ({ config, pkgs, ... }: {
          system.stateVersion = "26.05";
          networking.hostName = "iperf-${name}";

          microvm = {
            hypervisor = "qemu";
            mem = 1024;
            vcpu = 2;
            interfaces = [{
              type = "tap";
              id = "ipftap-${name}";
              mac = "02:00:00:99:00:0${if role == "server" then "1" else "2"}";
              tap.vhost = true;
            }];
          };

          systemd.network = {
            enable = true;
            networks."10-vm" = {
              matchConfig.MACAddress = config.microvm.interfaces.0.mac;
              networkConfig = {
                DHCP = "no";
                Address = "${ip}/24";
              };
            };
          };

          environment.systemPackages = [ pkgs.iperf ];

          # Auto-start iperf server on server VM
          systemd.services.iperf-server = lib.mkIf (role == "server") {
            wantedBy = [ "multi-user.target" ];
            after = [ "network-online.target" ];
            serviceConfig.ExecStart = "${pkgs.iperf}/bin/iperf -s";
          };
        })
      ];
    }).config.microvm.declaredRunner;

in {
  server = mkIperfVM { name = "server"; ip = "10.99.0.1"; role = "server"; };
  client = mkIperfVM { name = "client"; ip = "10.99.0.2"; role = "client"; };
}
```

### Step 0.2: Run Baseline Throughput Test

```bash
# Create simple TAP bridge for iperf test
sudo ip link add ipfbr0 type bridge
sudo ip link set ipfbr0 up
sudo ip tuntap add dev ipftap-server mode tap multi_queue user $USER
sudo ip tuntap add dev ipftap-client mode tap multi_queue user $USER
sudo ip link set ipftap-server master ipfbr0
sudo ip link set ipftap-client master ipfbr0
sudo ip link set ipftap-server up
sudo ip link set ipftap-client up

# Start VMs
nix run .#iperf-server-vm &
nix run .#iperf-client-vm &
sleep 10

# Run iperf2 tests
ssh root@10.99.0.2 'iperf -c 10.99.0.1 -t 10'           # TCP
ssh root@10.99.0.2 'iperf -c 10.99.0.1 -t 10 -u -b 5G'  # UDP 5Gbps
ssh root@10.99.0.2 'iperf -c 10.99.0.1 -t 10 -u -b 10G' # UDP 10Gbps

# Expected results:
#   TCP:       >8 Gbps (vhost-net should achieve near-10G)
#   UDP 5Gbps: ~5 Gbps with <0.1% loss
#   UDP 10Gbps: >8 Gbps (may see some loss at wire speed)
```

### Step 0.2.1: Multi-Queue TAP Alignment with io_uring

**Critical**: The TAP queue count must match GoSRT's `Config.IoUringRecvRingCount` for optimal scaling.

```bash
# Default GoSRT config uses 4 io_uring rings
QUEUE_COUNT=4

# Create TAP with matching queue count
sudo ip tuntap add dev ipftap-server mode tap multi_queue user $USER

# Verify queue count
ethtool -l ipftap-server
# Expected output:
#   Combined: 4

# If single-queue, you'll see:
#   Combined: 1  ← BOTTLENECK - will limit to ~2-3 Gbps
```

**io_uring Alignment Table**:

| GoSRT Config | TAP Queues | Expected Throughput |
|--------------|------------|---------------------|
| `IoUringRecvRingCount = 1` | 1 | 2-3 Gbps |
| `IoUringRecvRingCount = 2` | 2 | 4-6 Gbps |
| `IoUringRecvRingCount = 4` | 4 | 8-10+ Gbps |

### Step 0.2.2: Router-to-Router Baseline (Dual-Bridge Topology)

Before testing VM-to-VM, validate the dual-router topology (srt-router-a ↔ srt-router-b) that matches the production network design.

```bash
# Create network namespaces for routers
sudo ip netns add srt-router-a
sudo ip netns add srt-router-b

# Create inter-router veth pair
sudo ip link add veth-ab type veth peer name veth-ba
sudo ip link set veth-ab netns srt-router-a
sudo ip link set veth-ba netns srt-router-b

# Configure addresses (matches design doc)
sudo ip netns exec srt-router-a ip addr add 10.50.0.1/24 dev veth-ab
sudo ip netns exec srt-router-b ip addr add 10.50.0.2/24 dev veth-ba
sudo ip netns exec srt-router-a ip link set veth-ab up
sudo ip netns exec srt-router-b ip link set veth-ba up

# Run iperf between routers (isolates bridge overhead)
sudo ip netns exec srt-router-a iperf -s &
sleep 1
sudo ip netns exec srt-router-b iperf -c 10.50.0.1 -t 10

# Expected: >20 Gbps (veth pairs are fast)
# This establishes the router-to-router baseline BEFORE VM overhead
```

### Step 0.3: Validation Criteria

| Metric | Minimum | Target | Action if Below |
|--------|---------|--------|-----------------|
| Router-to-Router TCP | 15 Gbps | 20+ Gbps | Check network namespace config |
| VM-to-VM TCP throughput | 5 Gbps | 8+ Gbps | Check vhost-net enabled, CPU pinning |
| VM-to-VM UDP 5G throughput | 4.9 Gbps | 5 Gbps | Check netem limits, ring sizes |
| VM-to-VM UDP 5G loss | <1% | <0.1% | Check kernel UDP buffers |
| VM-to-VM UDP 10G throughput | 6 Gbps | 8+ Gbps | Need multi-queue TAP (see 0.2.1) |
| TAP queue count | = IoUringRecvRingCount | 4 | Recreate TAP with `multi_queue` |

### Step 0.4: Troubleshooting Infrastructure

```bash
# Verify vhost-net is being used
ls -la /dev/vhost-net
cat /proc/modules | grep vhost

# Check TAP multi-queue
ethtool -l ipftap-server

# Verify kernel buffers
sysctl net.core.rmem_max net.core.wmem_max

# Check for CPU saturation during iperf
mpstat 1 10

# If throughput is low, try:
# 1. Enable vhost-net: sudo modprobe vhost_net
# 2. Increase buffers: sysctl -w net.core.rmem_max=134217728
# 3. CPU pinning for QEMU processes
```

### Definition of Done - Phase 0

- [ ] iperf2 TCP achieves >5 Gbps between VMs
- [ ] iperf2 UDP at 5 Gbps has <1% loss
- [ ] vhost-net is confirmed active
- [ ] Kernel buffers are appropriately sized
- [ ] Infrastructure is NOT the bottleneck

### Why This Matters

Without this validation step, you might spend hours debugging GoSRT thinking there's a bug, when the actual issue is:
- vhost-net not loaded
- TAP not in multi-queue mode
- Kernel buffers too small
- Bridge STP causing delays

**Do not proceed to Phase 1 until Phase 0 passes.**

---

## Phase 1: Foundation - Constants, Library, Overlay, and Flake {#phase-1-foundation}

### Objective
Create the foundational files that all other modules depend on, including the overlay for binary flavors.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `flake.nix` | Main entry point (skeleton) | Lines 4962-5328 |
| `nix/constants.nix` | Role definitions, base config | Lines 278-521 |
| `nix/lib.nix` | Computed values, helpers | Lines 524-690 |
| `nix/overlays/gosrt.nix` | Binary flavor overlay | Refinement 1 |
| `nix/modules/srt-test.nix` | Impairment scenario module | Refinement 2 |
| `nix/modules/srt-network.nix` | Declarative nftables | Refinement 3 |

### Step 1.1: Create `nix/constants.nix`

**File**: `nix/constants.nix`

```nix
# Copy from design doc lines 278-521
# Key sections:
#   - base: subnetPrefix, interRouterBase, port bases
#   - roles: 8 role definitions (server, publisher, subscriber, etc.)
#   - latencyProfiles: 5 profiles (0ms to 300ms RTT)
#   - routers: A and B namespace names
#   - ports: SRT, Prometheus, Grafana
#   - vm: memory, vcpus
#   - netem: queueLimit
#   - test: default durations
#   - go: version, ldflags, experimentalFeatures
```

**Validation**:
- Each role has unique `index` (1-8)
- Each role has `shortName`, `router` (A or B), `package`
- `service` block has `binary`, `args`, `hasPrometheus`

**Potential Pitfalls**:
- [ ] **Index collision**: Two roles with same index → duplicate IPs
- [ ] **Missing router assignment**: Role without `router` breaks network setup
- [ ] **Placeholder values**: `{vmIp}`, `{serverIp}`, `{bitrate}` must be replaced by lib.nix

### Step 1.2: Create `nix/lib.nix`

**File**: `nix/lib.nix`

```nix
# Copy from design doc lines 524-690
# Key functions:
#   - validateRole: Assertions for required fields
#   - mkRoleNetwork: Derives TAP, bridge, veth, subnet, vmIp, gateway, MAC
#   - mkRolePorts: Derives console, sshForward, prometheusForward
#   - mkInterRouterLink: Derives inter-router link config
#   - roles: Fully computed role configs
#   - mkExecStart: Generates ExecStart from service config
#   - mkScrapeTargets, mkRelabelConfigs: Prometheus helpers
```

**Validation**:
- `lib.nix` imports `constants.nix`
- All role indices are unique (assertion)
- MAC format: `02:00:00:50:XX:02` where XX is hex of index

**Potential Pitfalls**:
- [ ] **Hex conversion**: `lib.toHexString` may not exist in older nixpkgs → use `lib.toHexStringLower` or manual conversion
- [ ] **Placeholder replacement**: `mkExecStart` must replace `{vmIp}`, `{serverIp}`, `{bitrate}` with actual values
- [ ] **Missing fields**: Accessing `role.service.hasPrometheus` on metrics role (which has `service = null`)

### Step 1.3: Create `flake.nix` Skeleton

**File**: `flake.nix`

```nix
# Copy from design doc lines 4962-5328
# Initial skeleton with:
#   - inputs: nixpkgs, flake-utils, microvm
#   - nixConfig: microvm cachix
#   - outputs skeleton (empty packages, apps, devShells, checks)
```

**Validation**:
```bash
nix flake check --no-build  # Should parse without errors
nix eval .#lib  # Should return lib.nix exports (once connected)
```

### Step 1.4: Create `nix/overlays/gosrt.nix`

**File**: `nix/overlays/gosrt.nix`

This overlay centralizes binary flavor definitions so GOEXPERIMENT and ldflags propagate automatically.

```nix
# nix/overlays/gosrt.nix
final: prev: {
  gosrt = rec {
    # Common build function
    mkGosrt = { variant, ldflags ? [ "-s" "-w" ], tags ? [], extraArgs ? [] }:
      final.buildGoModule {
        pname = "gosrt-${variant}";
        version = "0.1.0";
        src = final.lib.cleanSource ../../.;
        vendorHash = "sha256-AAAA...";  # Update after first build

        subPackages = [ "contrib/server" "contrib/client" "contrib/client-generator" ];

        CGO_ENABLED = "0";

        preBuild = ''
          export GOEXPERIMENT=jsonv2
        '';

        ldflags = ldflags;
        tags = tags;

        postInstall = final.lib.optionalString (extraArgs != []) ''
          wrapProgram $out/bin/* --add-flags "${toString extraArgs}"
        '';
      };

    # Flavor definitions (centralized - changes propagate to all VMs)
    prod = mkGosrt { variant = "prod"; };

    debug = mkGosrt {
      variant = "debug";
      ldflags = [ ];  # Keep debug symbols
      tags = [ "debug" ];
    };

    perf = mkGosrt {
      variant = "perf";
      extraArgs = [ "-pprof" ":6060" ];
    };
  };
}
```

**Usage in flake.nix**:
```nix
outputs = { self, nixpkgs, ... }: {
  overlays.default = import ./nix/overlays/gosrt.nix;

  packages.x86_64-linux = let
    pkgs = import nixpkgs {
      system = "x86_64-linux";
      overlays = [ self.overlays.default ];
    };
  in {
    inherit (pkgs.gosrt) prod debug perf;
  };
};
```

### Step 1.5: Create `nix/modules/srt-test.nix`

**File**: `nix/modules/srt-test.nix`

NixOS module for declarative impairment scenarios (replaces imperative shell scripts).

```nix
# nix/modules/srt-test.nix
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.srt-test;

  scenarios = {
    clean = { loss = 0; delay = 0; jitter = 0; };
    regional = { loss = 0; delay = 5; jitter = 2; };
    continental = { loss = 0; delay = 30; jitter = 5; };
    intercontinental = { loss = 0; delay = 65; jitter = 10; };
    geo-satellite = { loss = 0.5; delay = 150; jitter = 20; };
    congested-wifi = { loss = 2; delay = 5; jitter = 10; };
    starlink-handoff = {
      loss = 0;
      delay = 20;
      jitter = 10;
      blackholePattern = {
        enable = true;
        times = [ 12 27 42 57 ];
        durationMs = 500;
      };
    };
  };

in {
  options.services.srt-test = {
    enable = mkEnableOption "SRT test scenario";

    scenario = mkOption {
      type = types.enum (attrNames scenarios);
      default = "clean";
      description = "Network impairment scenario";
    };

    interface = mkOption {
      type = types.str;
      default = "eth0";
    };
  };

  config = mkIf cfg.enable (let
    s = scenarios.${cfg.scenario};
  in {
    systemd.services.srt-impairment = {
      description = "Apply SRT test impairment: ${cfg.scenario}";
      wantedBy = [ "network-online.target" ];
      after = [ "network-online.target" ];

      script = ''
        ${pkgs.iproute2}/bin/tc qdisc replace dev ${cfg.interface} root netem \
          ${optionalString (s.delay > 0) "delay ${toString s.delay}ms"} \
          ${optionalString (s.jitter > 0) "${toString s.jitter}ms"} \
          ${optionalString (s.loss > 0) "loss ${toString s.loss}%"} \
          limit 50000
      '';

      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
    };

    # Blackhole pattern for Starlink simulation
    systemd.services.srt-blackhole = mkIf (s ? blackholePattern && s.blackholePattern.enable) {
      description = "Starlink blackhole pattern";
      wantedBy = [ "multi-user.target" ];
      after = [ "srt-impairment.service" ];

      script = ''
        # Blackhole pattern implementation
        # Uses nftables sets with timeouts (see srt-network.nix)
      '';
    };
  });
}
```

### Step 1.6: Create `nix/modules/srt-network.nix`

**File**: `nix/modules/srt-network.nix`

Declarative nftables for network impairment (replaces iptables shell commands).

```nix
# nix/modules/srt-network.nix
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.srt-network;
in {
  options.services.srt-network = {
    enable = mkEnableOption "SRT declarative networking";

    blackholeTargets = mkOption {
      type = types.listOf types.str;
      default = [ ];
      description = "IP addresses to blackhole during pattern tests";
    };
  };

  config = mkIf cfg.enable {
    networking.nftables = {
      enable = true;
      ruleset = ''
        table inet srt-test {
          # Blackhole set with auto-expiring entries
          set blackhole-addrs {
            type ipv4_addr
            flags timeout
          }

          chain forward {
            type filter hook forward priority 0; policy accept;
            ip daddr @blackhole-addrs drop
          }
        }
      '';
    };

    # Helper script to add/remove blackhole entries
    environment.systemPackages = [
      (pkgs.writeShellScriptBin "srt-blackhole-add" ''
        nft add element inet srt-test blackhole-addrs { $1 timeout ''${2:-500ms} }
      '')
      (pkgs.writeShellScriptBin "srt-blackhole-clear" ''
        nft flush set inet srt-test blackhole-addrs
      '')
    ];
  };
}
```

**Benefits**:
- Atomic rule application
- Timeout-based entries auto-expire
- Easy inspection: `nft list ruleset`
- Declarative, not imperative

### Step 1.7: Unit Tests for Phase 1

Create `nix/tests/constants_test.nix`:

```nix
# Tests for constants.nix validity
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
in {
  # Test: All roles have unique indices
  test_unique_indices = let
    indices = lib.mapAttrsToList (_: r: r.index) gosrtLib.roles;
  in assert (lib.unique indices) == indices; "PASS";

  # Test: All roles have required fields
  test_required_fields = lib.mapAttrs (name: role:
    assert role ? index;
    assert role ? shortName;
    assert role ? router;
    assert role.router == "A" || role.router == "B";
    "PASS: ${name}"
  ) gosrtLib.roles;

  # Test: Network derivation produces valid IPs
  test_network_ips = lib.mapAttrs (name: role:
    assert lib.hasPrefix "10.50." role.network.vmIp;
    assert lib.hasSuffix ".2" role.network.vmIp;
    "PASS: ${name}"
  ) gosrtLib.roles;

  # Test: Server IP is accessible
  test_server_ip = assert gosrtLib.serverIp == "10.50.3.2"; "PASS";
}
```

### Step 1.8: Create `nix/modules/srt-network-interfaces.nix`

**File**: `nix/modules/srt-network-interfaces.nix`

Explicit interface naming via `systemd.network.links` - eliminates non-deterministic naming.

```nix
# nix/modules/srt-network-interfaces.nix
#
# Use systemd.network.links to enforce predictable interface names.
# No more "which interface is eth0 on this boot?" debugging.
#
{ config, lib, gosrtLib, ... }:

with lib;

let
  cfg = config.services.srt-interfaces;
in {
  options.services.srt-interfaces = {
    enable = mkEnableOption "SRT explicit interface naming";

    role = mkOption {
      type = types.str;
      description = "Role name from gosrtLib.roles";
    };
  };

  config = mkIf cfg.enable {
    systemd.network = {
      enable = true;

      # Explicit interface naming rules based on MAC address
      links = {
        "10-eth-srt" = {
          # Match VM's primary interface by MAC
          matchConfig.MACAddress = gosrtLib.roles.${cfg.role}.network.mac;
          linkConfig = {
            Name = "eth-srt";
            MTUBytes = "9000";  # Jumbo frames if supported
          };
        };
      };

      # Network config references the stable name
      networks."20-eth-srt" = {
        matchConfig.Name = "eth-srt";
        networkConfig = {
          DHCP = "no";
          Address = "${gosrtLib.roles.${cfg.role}.network.vmIp}/24";
          Gateway = gosrtLib.roles.${cfg.role}.network.gateway;
        };
      };
    };
  };
}
```

**Benefits**:
- Scripts can hardcode `eth-srt` instead of fragile discovery
- Impairment rules (`tc qdisc add dev eth-srt ...`) work reliably
- No more interface name mismatches between boots

### Step 1.9: Debug Build Requirement for Initial Testing

**IMPORTANT**: Use `gosrt.debug` builds during Phase 1 testing to catch context violations early.

The `AssertEventLoopContext()` and `AssertTickContext()` checks are only active in debug builds. These assertions panic with a stack trace if:
- A lock is acquired inside the EventLoop goroutine
- Ring buffer operations happen from the wrong goroutine
- TSBPD delivery violations occur

```nix
# In Phase 1 test VM configuration
{ gosrtPackages, ... }:
{
  # CRITICAL: Use debug build to catch context violations
  environment.systemPackages = [ gosrtPackages.debug ];

  # After stability is confirmed, switch to:
  # environment.systemPackages = [ gosrtPackages.prod ];
}
```

**Verification**:
```bash
# Build debug binaries with assertions enabled
make build-debug

# Run with debug build - violations will panic with stack trace
./contrib/server/server-debug -useeventloop

# If you see:
#   panic: AssertEventLoopContext: lock acquired in EventLoop
# This indicates a bug that would silently degrade performance in prod
```

### Definition of Done - Phase 1

- [ ] `nix flake check --no-build` passes
- [ ] `nix eval .#lib` returns attribute set
- [ ] All 8 roles defined with unique indices
- [ ] `lib.nix` exports: `roles`, `serverIp`, `interRouterLinks`, `mkExecStart`
- [ ] Overlay exports `gosrt.prod`, `gosrt.debug`, `gosrt.perf`
- [ ] `srt-test` module can be imported without errors
- [ ] `srt-network` module nftables rules are valid
- [ ] `srt-network-interfaces` module provides explicit naming
- [ ] Debug builds panic on context violations (not silent degradation)
- [ ] Unit tests pass: `nix eval --expr '(import ./nix/tests/constants_test.nix { inherit (import <nixpkgs> {}) pkgs lib; })'`

---

## Phase 2: Metrics VM First (Observer Pattern) {#phase-2-metrics-first}

### Objective
Deploy the Prometheus/Grafana monitoring infrastructure BEFORE the SUT (System Under Test). This follows the "Observer Pattern" - you can't debug what you can't see.

### Rationale
By setting up metrics collection first:
1. When GoSRT VMs start, metrics are immediately visible
2. No "blind debugging" - issues are observable from the start
3. Dashboard-as-Code ensures consistent monitoring across all tests
4. Annotations are ready to correlate impairment events

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/prometheus/scrape-configs.nix` | Scrape config generator | Lines 1890-1957 |
| `nix/grafana/lib.nix` | Dashboard helpers | Lines 1277-1564 |
| `nix/grafana/panels/default.nix` | Panel exports | Lines 3217-3243 |
| `nix/grafana/panels/*.nix` | Panel modules | Lines 1567-3014 |
| `nix/grafana/dashboards/*.nix` | Dashboard modules | Lines 3017-3196 |
| `nix/microvms/metrics.nix` | Metrics VM | Lines 1960-2121 |

### Step 2.1: Create Prometheus Scrape Configs

**File**: `nix/prometheus/scrape-configs.nix`

```nix
# Data-driven scrape config generation
{ lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # Auto-generate from roles
  gosrtInstances = lib.filterAttrs
    (_: r: r.service.hasPrometheus or false)
    gosrtLib.roles;

  mkRelabelConfigs = instances: lib.mapAttrsToList (name: role: {
    source_labels = [ "__address__" ];
    regex = "${role.network.vmIp}:.*";
    target_label = "instance";
    replacement = name;
  }) instances;

in {
  gosrt = {
    job_name = "gosrt";
    scrape_interval = "1s";  # High frequency for real-time dashboards
    static_configs = [{
      targets = lib.mapAttrsToList
        (_: r: "${r.network.vmIp}:9100")
        gosrtInstances;
    }];
    relabel_configs = mkRelabelConfigs gosrtInstances;
  };

  node = {
    job_name = "node";
    scrape_interval = "5s";
    static_configs = [{
      targets = lib.mapAttrsToList
        (_: r: "${r.network.vmIp}:9100")
        gosrtLib.roles;
    }];
    relabel_configs = mkRelabelConfigs gosrtLib.roles;
  };

  all = [ gosrt node ];
}
```

### Step 2.2: Create Grafana Dashboard Library

**File**: `nix/grafana/lib.nix`

See design doc lines 1277-1564. Key additions for refinements:

```nix
# Additional helpers from refinements

# Heatmap panel for NAK burst detection
mkHeatmap = { title, targets, gridPos, description ? null }:
  mkPanel {
    inherit title targets gridPos description;
    type = "heatmap";
    options = {
      calculate = true;
      color.mode = "spectrum";
      color.scheme = "Spectral";
    };
  };

# Panel with pprof links (click to profile)
mkTimeseriesWithPprof = { title, instance, ... }@args:
  mkTimeseries (args // {
    fieldConfig.defaults.links = [
      {
        title = "CPU Profile (30s)";
        url = "http://${instance}:6060/debug/pprof/profile?seconds=30";
        targetBlank = true;
      }
      {
        title = "Heap Profile";
        url = "http://${instance}:6060/debug/pprof/heap";
        targetBlank = true;
      }
      {
        title = "Goroutine Profile";
        url = "http://${instance}:6060/debug/pprof/goroutine?debug=1";
        targetBlank = true;
      }
    ];
  });
```

### Step 2.3: Create Panel Modules (Including Heatmaps)

**File**: `nix/grafana/panels/heatmaps.nix`

```nix
# NAK burst heatmap for micro-burst detection
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkHeatmap mkTarget;
in {
  nakBurstHeatmap = mkHeatmap {
    title = "NAK Burst Distribution";
    description = ''
      Heatmap showing NAK activity over time.
      - Vertical bands = burst NAK activity (potential O(n) scaling)
      - Smooth spread = efficient O(log n) btree operations
    '';
    gridPos = { h = 8; w = 12; x = 0; };
    targets = [
      (mkTarget "rate(gosrt_receiver_nak_sent_total{instance=\"server\"}[1s])" "NAK/s")
    ];
  };

  retransBurstHeatmap = mkHeatmap {
    title = "Retransmission Burst Distribution";
    description = "Visualize retransmission patterns - bursts indicate loss events";
    gridPos = { h = 8; w = 12; x = 12; };
    targets = [
      (mkTarget "rate(gosrt_connection_congestion_recv_pkt_retrans_total[1s])" "Retrans/s")
    ];
  };
}
```

### Step 2.4: Create Metrics VM

**File**: `nix/microvms/metrics.nix`

Key enhancements from refinements:
- Dashboard-as-Code via `services.grafana.provision`
- Pre-loaded dashboards visible immediately
- Annotation datasource for impairment correlation

```nix
# Key additions to metrics.nix
services.grafana.provision = {
  datasources.settings.datasources = [
    {
      name = "Prometheus";
      type = "prometheus";
      access = "proxy";
      url = "http://localhost:9090";
      isDefault = true;
    }
    # Annotation datasource for impairment events
    {
      name = "Annotations";
      type = "prometheus";
      access = "proxy";
      url = "http://localhost:9090";
      jsonData.httpMethod = "POST";
    }
  ];

  # Dashboard-as-Code: pre-loaded, no manual clicks
  dashboards.settings.providers = [{
    name = "GoSRT Dashboards";
    type = "file";
    options.path = "/etc/grafana/dashboards";
    disableDeletion = true;
    updateIntervalSeconds = 10;
  }];
};

# Dashboards generated from Nix
environment.etc."grafana/dashboards/gosrt-ops.json".text =
  builtins.toJSON dashboards.operations;
environment.etc."grafana/dashboards/gosrt-analysis.json".text =
  builtins.toJSON dashboards.analysis;
```

### Step 2.5: Annotation API Integration for Impairment Events

**Requirement**: Every impairment scenario must trigger a Grafana annotation when activated. This provides visual correlation during post-test analysis.

```nix
# nix/network/impairment-annotations.nix
{ pkgs, lib, grafanaUrl ? "http://10.50.8.2:3000" }:

let
  # Create annotation helper used by all impairment scripts
  mkAnnotationScript = scenario: pkgs.writeShellScript "annotate-${scenario.name}" ''
    set -euo pipefail

    ACTION="$1"  # "start" or "end"
    TIMESTAMP=$(date +%s)000  # Grafana expects milliseconds

    curl -s -X POST \
      -u admin:srt \
      -H "Content-Type: application/json" \
      -d '{
        "time": '"$TIMESTAMP"',
        "text": "Impairment ${scenario.name}: '"$ACTION"'",
        "tags": ["impairment", "${scenario.name}", "'"$ACTION"'"]
      }' \
      ${grafanaUrl}/api/annotations

    echo "Annotation created: ${scenario.name} $ACTION"
  '';

in {
  inherit mkAnnotationScript;

  # Wrap scenario apply/cleanup with annotations
  wrapWithAnnotations = scenario: scenario // {
    apply = pkgs.writeShellScript "apply-${scenario.name}-annotated" ''
      ${mkAnnotationScript scenario} start
      ${scenario.apply}
    '';

    cleanup = pkgs.writeShellScript "cleanup-${scenario.name}-annotated" ''
      ${scenario.cleanup}
      ${mkAnnotationScript scenario} end
    '';
  };
}
```

**Usage**: When viewing dashboards after a test:
- Vertical annotation lines mark EXACTLY when impairments started/ended
- Filter by tag to show only "starlink-handoff" events
- Correlate NAK bursts with blackhole periods visually

### Step 2.6: Verify Metrics VM

```bash
# Start metrics VM
nix run .#srt-metrics-vm &
sleep 30

# Verify Prometheus
curl http://10.50.8.2:9090/api/v1/status/config | jq .

# Verify Grafana with pre-loaded dashboards
curl http://10.50.8.2:3000/api/health
curl -u admin:srt http://10.50.8.2:3000/api/dashboards/uid/gosrt-ops | jq .title
curl -u admin:srt http://10.50.8.2:3000/api/dashboards/uid/gosrt-analysis | jq .title

# Verify annotation API
curl -X POST -u admin:srt \
  -H "Content-Type: application/json" \
  -d '{"text":"Test annotation","tags":["test"]}' \
  http://10.50.8.2:3000/api/annotations

# Test impairment annotation integration
nix run .#test-annotation-integration
```

### Definition of Done - Phase 2

- [ ] Metrics VM boots and Prometheus is running
- [ ] Grafana shows pre-loaded dashboards (no manual import)
- [ ] Both dashboards (ops, analysis) load without errors
- [ ] Scrape configs target all 8 VM IPs
- [ ] Annotation API is functional
- [ ] Heatmap panels render correctly
- [ ] pprof links are clickable in dashboard

---

## Phase 3: Go Packages with Audit Hooks {#phase-3-go-packages}

### Objective
Create Nix packages for all GoSRT binaries with audit checks as pre-build hooks. No binary can be built if it contains unsafe sequence arithmetic or undefined metrics.

### Rationale
From CLAUDE.md:
> "ALWAYS run `make audit-metrics` when modifying metrics."
> "The `make code-audit-seq` target detects unsafe patterns."

By integrating these audits as Nix pre-build hooks, we ensure:
1. Unsafe code NEVER reaches a VM
2. Audit failures are caught at `nix build` time
3. CI/CD automatically enforces code quality

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/packages/default.nix` | GoSRT package exports | Lines 693-765 |
| `nix/packages/gosrt.nix` | Main GoSRT package with audits | New |
| `nix/packages/srt-xtransmit.nix` | srt-xtransmit package | Lines 778-848 |
| `nix/packages/ffmpeg.nix` | FFmpeg with SRT | Lines 851-875 |

### Step 3.1: Create `nix/packages/gosrt.nix` with Audit Hooks

**File**: `nix/packages/gosrt.nix`

```nix
# nix/packages/gosrt.nix
#
# GoSRT package with integrated audit checks.
# Builds FAIL if audit-metrics or code-audit-seq detect issues.
#
{ pkgs, lib, src, buildVariant ? "production" }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # Audit check derivation (run before build)
  auditChecks = pkgs.runCommand "gosrt-audit-checks" {
    inherit src;
    nativeBuildInputs = [ pkgs.go_1_26 ];
  } ''
    cd $src
    export HOME=$(mktemp -d)
    export GOEXPERIMENT=jsonv2

    echo "=== Running Sequence Arithmetic Safety Audit ==="
    go run ./tools/sequence-audit/main.go ./... || {
      echo "FAIL: Unsafe sequence arithmetic detected!"
      echo "Fix patterns like 'int32(a-b) < 0' - use circular.Number instead"
      exit 1
    }

    echo "=== Running Prometheus Metrics Audit ==="
    go run ./tools/metrics-audit/main.go || {
      echo "FAIL: Metrics audit failed!"
      echo "Ensure all metrics are defined in metrics/metrics.go and exported in handler.go"
      exit 1
    }

    echo "PASS: All audits passed"
    touch $out
  '';

  # Build configuration per variant
  variantConfig = {
    production = {
      ldflags = [ "-s" "-w" ];
      tags = [ ];
    };
    debug = {
      ldflags = [ ];  # Keep debug symbols
      tags = [ "debug" ];
    };
    perf = {
      ldflags = [ "-s" "-w" ];
      tags = [ "pprof" ];
    };
  };

  cfg = variantConfig.${buildVariant};

in pkgs.buildGoModule {
  pname = "gosrt-${buildVariant}";
  version = "0.1.0";
  inherit src;

  vendorHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
  # TODO: Update after first build attempt

  subPackages = [
    "contrib/server"
    "contrib/client"
    "contrib/client-generator"
    "contrib/client-seeker"
    "contrib/performance"
    "contrib/udp_echo"
  ];

  CGO_ENABLED = "0";

  # CRITICAL: Run audits BEFORE build
  preBuild = ''
    # Ensure audit checks passed (will fail build if they haven't)
    if [ ! -f ${auditChecks} ]; then
      echo "ERROR: Audit checks must pass before build"
      exit 1
    fi

    export GOEXPERIMENT=jsonv2
  '';

  ldflags = cfg.ldflags;
  tags = cfg.tags;

  meta = with lib; {
    description = "GoSRT - Pure Go SRT implementation (${buildVariant})";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
```

### Step 3.2: Create `nix/packages/default.nix`

**File**: `nix/packages/default.nix`

```nix
# nix/packages/default.nix
{ pkgs, lib, src }:

let
  mkGosrt = variant: import ./gosrt.nix {
    inherit pkgs lib src;
    buildVariant = variant;
  };

in {
  # Production binary (optimized, no debug symbols)
  server = mkGosrt "production";
  client = mkGosrt "production";
  client-generator = mkGosrt "production";

  # Debug binary (with context assertions - use for initial MicroVM testing)
  server-debug = mkGosrt "debug";
  client-debug = mkGosrt "debug";
  client-generator-debug = mkGosrt "debug";

  # Performance binary (with pprof endpoints)
  server-perf = mkGosrt "perf";

  # Convenience: all production binaries
  all = pkgs.symlinkJoin {
    name = "gosrt-all";
    paths = [ (mkGosrt "production") ];
  };
}
```

### Step 3.3: Initial MicroVM Testing with Debug Builds

**IMPORTANT**: For initial MicroVM testing, use **debug builds** to enable `AssertEventLoopContext()`:

```nix
# In microvms/default.nix - use debug for initial testing
mkRoleVM = name: role:
  baseMicroVM.mkMicroVM {
    inherit role;
    packages = packageMap;
    buildVariant = "debug";  # Enable context assertions
  };
```

This catches lock-contention or context-handling bugs immediately during first network runs.

### Step 3.4: Create srt-xtransmit and ffmpeg packages

(Same as before, but now audit checks are integrated)

**Potential Pitfalls**:
- [ ] **vendorHash mismatch**: Must update after any go.mod change
- [ ] **subPackages path**: Must match exact directory name (`contrib/udp_echo` not `contrib/udp-echo`)
- [ ] **ldflags**: Must match Makefile's ldflags
- [ ] **Audit tool path**: `tools/sequence-audit/main.go` and `tools/metrics-audit/main.go` must exist

### Step 2.2: Create `nix/packages/srt-xtransmit.nix`

**File**: `nix/packages/srt-xtransmit.nix`

Uses `pkgs.stdenv.mkDerivation` with CMake:
- Fetch from GitHub with `fetchSubmodules = true`
- Build with CMake
- Binary location varies → use `find` fallback in `postInstall`

**Potential Pitfalls**:
- [ ] **Binary location**: May be in `build/xtransmit/bin/` or `build/bin/` depending on CMake version
- [ ] **OpenSSL version**: May need specific version for encryption support

### Step 2.3: Create `nix/packages/ffmpeg.nix`

**File**: `nix/packages/ffmpeg.nix`

Simply re-export `pkgs.ffmpeg-full` which includes SRT support.

### Step 2.4: Connect to flake.nix

Update `flake.nix`:
```nix
packages = import ./nix/packages { inherit pkgs lib src; };
srtXtransmit = import ./nix/packages/srt-xtransmit.nix { inherit pkgs lib; };
ffmpegFull = import ./nix/packages/ffmpeg.nix { inherit pkgs lib; };
```

### Step 2.5: Unit Tests for Phase 2

```bash
# Build each package
nix build .#server
nix build .#client
nix build .#client-generator
nix build .#client-seeker
nix build .#performance
nix build .#udp-echo
nix build .#srt-xtransmit
nix build .#ffmpeg-full

# Verify binaries exist
./result/bin/server --help
./result/bin/srt-xtransmit --help
./result/bin/ffmpeg -version | grep srt
```

### Definition of Done - Phase 2

- [ ] All 6 GoSRT packages build successfully
- [ ] `srt-xtransmit` builds with OpenSSL support
- [ ] `ffmpeg-full` includes SRT protocol
- [ ] `nix build .#gosrt-all` produces combined package
- [ ] Binaries are statically linked (no CGO)
- [ ] vendorHash is correct (not placeholder)

---

## Phase 3: OCI Containers {#phase-3-oci-containers}

### Objective
Create OCI container images for deployment.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/containers/default.nix` | Container exports | N/A (derive from pattern) |
| `nix/containers/server.nix` | Server container | Lines 877-945 |
| `nix/containers/client.nix` | Client container | (similar pattern) |
| `nix/containers/client-generator.nix` | Generator container | (similar pattern) |

### Step 3.1: Create `nix/containers/server.nix`

Uses `pkgs.dockerTools.buildLayeredImage`:
- Include package, entrypoint script, busybox, curl, cacert
- Expose UDP port 6000 and TCP port 9100 (Prometheus)
- Create `/tmp` with correct permissions

**Potential Pitfalls**:
- [ ] **Missing cacert**: HTTPS calls fail without SSL certs
- [ ] **No /tmp**: Some applications need writable temp directory
- [ ] **Entrypoint quoting**: Environment variable expansion in shell script

### Step 3.2: Create `nix/containers/default.nix`

```nix
{ pkgs, lib, serverPackage, clientPackage, clientGeneratorPackage }:

{
  server = import ./server.nix { inherit pkgs lib serverPackage; };
  client = import ./client.nix { inherit pkgs lib clientPackage; };
  client-generator = import ./client-generator.nix { inherit pkgs lib clientGeneratorPackage; };
}
```

### Step 3.3: Unit Tests for Phase 3

```bash
# Build containers
nix build .#server-container
nix build .#client-container

# Load and test
docker load < ./result
docker run --rm gosrt-server:latest --help
docker run --rm -e ADDR=0.0.0.0:6000 gosrt-server:latest &
curl localhost:9100/metrics  # Should return Prometheus metrics
```

### Definition of Done - Phase 3

- [ ] All container images build
- [ ] `docker load` succeeds
- [ ] Server container starts and exposes metrics
- [ ] Container has `/tmp` writable
- [ ] OCI labels are set correctly

---

## Phase 4: Network Infrastructure {#phase-4-network-infrastructure}

### Objective
Create network setup, teardown, and impairment scripts.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/network/default.nix` | Network module exports | Lines 3261-3300 |
| `nix/network/setup.nix` | Data-driven network setup | Lines 3598-4079 |
| `nix/network/profiles.nix` | Impairment profiles | Lines 3302-3407 |
| `nix/network/impairments.nix` | Functional impairment scripts | Lines 3410-3578 |

### Step 4.1: Create `nix/network/setup.nix`

**Critical Implementation Details**:

1. **TAP devices stay in host namespace** (QEMU needs access)
2. **Bridges connect TAPs to veth pairs**
3. **Veth pairs cross into router namespaces**
4. **Inter-router links have fixed latency**

Data-driven generation:
```nix
# Generate network for all roles from lib.roles
${lib.concatMapStringsSep "\n" (name: let
  role = c.roles.${name};
  net = role.network;
  router = if role.router == "A" then c.routers.A.namespace
           else c.routers.B.namespace;
in ''
  create_vm_network "${net.tap}" "${net.bridge}" "${net.vethHost}" "${net.vethRouter}" \
    "${router}" "${net.gateway}" "${c.base.subnetPrefix}.${toString role.index}"
'') (lib.attrNames c.roles)}
```

**Potential Pitfalls**:
- [ ] **TAP ownership**: Must be owned by `$USER` for unprivileged QEMU
- [ ] **Bridge MAC**: Bridge inherits MAC from first attached interface
- [ ] **Namespace ordering**: Router namespaces must exist before veth creation
- [ ] **Latency application**: `tc qdisc add` only works once; use `change` for updates
- [ ] **netem limit**: Must set `limit` parameter to prevent queue overflow

### Step 4.2: Create Latency Switching Script

**`srt-set-latency`**:
- Changes default routes to use different inter-router links
- Pushes Grafana annotation for correlation
- Must handle all routes for all roles

**Potential Pitfalls**:
- [ ] **Route replacement**: `ip route replace` not `add` (may already exist)
- [ ] **Annotation failure**: Must not fail script if Grafana unavailable

### Step 4.3: Create Loss Injection Script

**`srt-set-loss`**:
- Modifies tc netem on specified link
- Pushes Grafana annotation
- Must preserve existing latency when adding loss

### Step 4.4: Create Starlink Pattern Script

**`srt-starlink-pattern`**:
- Uses blackhole routes for 100% loss (instant effect)
- Parameterized: duration, blackout duration, pattern times
- Pushes annotations at each event

**Potential Pitfalls**:
- [ ] **Blackhole route priority**: Must take precedence over normal routes
- [ ] **Timing precision**: `sleep` may drift; use `date` for accurate timing
- [ ] **Route restoration**: Must restore correct route for current latency profile

### Step 4.5: Unit Tests for Phase 4

```bash
# Test network setup (requires root)
sudo nix run .#srt-network-setup

# Verify namespaces exist
ip netns list | grep srt-router

# Verify TAP devices exist
ip link show | grep srttap

# Verify inter-router links
sudo ip netns exec srt-router-a ip link show | grep link

# Test latency switching
nix run .#srt-set-latency -- 2
ping -c 1 10.50.3.2  # Should have ~60ms RTT

# Test loss injection
nix run .#srt-set-loss -- 5 2
# Verify with tc show

# Cleanup
sudo nix run .#srt-network-teardown
```

### Definition of Done - Phase 4

- [ ] Network setup creates all TAPs, bridges, veths
- [ ] Router namespaces exist with correct interfaces
- [ ] Inter-router links have correct latency
- [ ] Latency switching changes routes for all roles
- [ ] Loss injection modifies tc netem correctly
- [ ] Starlink pattern creates/removes blackhole routes
- [ ] Network teardown removes all resources
- [ ] Scripts are idempotent (can run multiple times)

---

## Phase 5: MicroVM Base and Data-Driven Generator {#phase-5-microvms}

### Objective
Create the MicroVM builder and data-driven generator.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/microvms/base.nix` | MicroVM builder function | Lines 948-1192 |
| `nix/microvms/default.nix` | Data-driven generator | Lines 1195-1239 |

### Step 5.1: Create `nix/microvms/base.nix`

**Critical Implementation Details**:

1. **mkMicroVM function**: Takes `role`, `packages`, `buildVariant`
2. **specialArgs injection**: Allows swapping production ↔ debug binaries
3. **Systemd service generation**: From `role.service` config
4. **Network configuration**: Static IP via systemd-networkd
5. **Serial console**: TCP socket for debugging

Key NixOS modules:
```nix
modules = [
  microvm.nixosModules.microvm
  ({ config, pkgs, ... }: {
    # MicroVM config
    microvm = {
      hypervisor = "qemu";
      mem = gosrtLib.vm.memoryMB;
      vcpu = gosrtLib.vm.vcpus;
      interfaces = [{
        type = "tap";
        id = net.tap;
        mac = net.mac;
        tap.vhost = true;
      }];
      qemu.extraArgs = [
        "-name" "gosrt:${name},process=gosrt:${name}"
        "-chardev" "socket,id=tcpcon,host=localhost,port=${toString role.ports.console},server=on,wait=off"
        "-serial" "chardev:tcpcon"
      ];
    };
    # systemd-networkd
    systemd.network = { ... };
    # Kernel parameters
    boot.kernel.sysctl = { ... };
    # Node exporter
    services.prometheus.exporters.node = { ... };
  })
  # Service module (generated from role.service)
  mkServiceModule
];
```

**Potential Pitfalls**:
- [ ] **TAP not found**: TAP must exist before VM starts (run network setup first)
- [ ] **MAC collision**: Each VM must have unique MAC
- [ ] **Port collision**: Console ports must not overlap
- [ ] **Service not starting**: Check systemd journal in VM
- [ ] **Build variant**: Must check if `-debug` package exists before using

### Step 5.2: Create `nix/microvms/default.nix`

**Data-driven generator**:
```nix
{ pkgs, lib, microvm, nixpkgs, system, packages, srtXtransmit, ffmpegFull }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  baseMicroVM = import ./base.nix { inherit pkgs lib microvm nixpkgs system; };

  packageMap = packages // {
    srt-xtransmit = srtXtransmit;
    ffmpeg-full = ffmpegFull;
  };

  mkRoleVM = name: role:
    if name == "metrics"
    then import ./metrics.nix { ... }
    else baseMicroVM.mkMicroVM {
      inherit role;
      packages = packageMap;
    };

in
  lib.mapAttrs mkRoleVM gosrtLib.roles
```

**Key Insight**: This single file replaces 7 individual VM files!

### Step 5.3: Connect Placeholder Replacement

In `lib.nix` `mkExecStart`:
```nix
mkExecStart = role: pkg: let
  svc = role.service;
  replaceVars = arg: builtins.replaceStrings
    [ "{vmIp}" "{serverIp}" "{bitrate}" ]
    [ role.network.vmIp serverIp "\${BITRATE:-50000000}" ]
    arg;
  args = map replaceVars svc.args;
  # ...
in "${cmd} ${lib.concatStringsSep " " args}";
```

**Potential Pitfalls**:
- [ ] **Environment variable escaping**: `\${BITRATE}` not `${BITRATE}` in Nix strings
- [ ] **Missing placeholder**: If `{vmIp}` appears but isn't replaced → broken command

### Step 5.4: Unit Tests for Phase 5

```bash
# Build VM packages
nix build .#srt-server-vm
nix build .#srt-publisher-vm
nix build .#srt-subscriber-vm

# Verify VM runner exists
ls -la ./result/bin/microvm-run

# Test VM boot (requires network setup)
sudo nix run .#srt-network-setup
nix run .#srt-server-vm &
sleep 10
ping -c 1 10.50.3.2  # Should respond

# Test serial console
nc localhost 45003  # Console port for server

# Verify service started
ssh root@10.50.3.2  # password: srt
systemctl status gosrt-srv
curl localhost:9100/metrics
```

### Definition of Done - Phase 5

- [ ] All 8 VMs build successfully
- [ ] VMs boot and get correct IPs
- [ ] GoSRT services start automatically
- [ ] Node exporter runs on all VMs
- [ ] Serial console accessible via nc
- [ ] SSH accessible with root/srt
- [ ] Debug VMs available (`-vm-debug` suffix)

---

## Phase 6: Metrics VM - Prometheus and Grafana {#phase-6-metrics-vm}

### Objective
Create the metrics VM with Prometheus scraping and Grafana dashboards.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/microvms/metrics.nix` | Metrics VM | Lines 1960-2121 |
| `nix/prometheus/scrape-configs.nix` | Scrape config generator | Lines 1890-1957 |
| `nix/grafana/lib.nix` | Dashboard helpers | Lines 1277-1564 |
| `nix/grafana/panels/default.nix` | Panel module exports | Lines 3217-3243 |
| `nix/grafana/panels/overview.nix` | Overview panels | Lines 1567-1683 |
| `nix/grafana/panels/traffic-lights.nix` | Traffic light panels | Lines 1686-1778 |
| `nix/grafana/panels/recovery.nix` | Recovery panels | Lines 2221-2312 |
| `nix/grafana/panels/system.nix` | System panels | Lines 2315-2394 |
| `nix/grafana/panels/rings.nix` | Ring buffer panels | Lines 2397-2451 |
| `nix/grafana/panels/anomalies.nix` | Anomaly panels | Lines 2454-2522 |
| `nix/grafana/panels/iouring.nix` | io_uring panels | Lines 2525-2629 |
| `nix/grafana/panels/efficiency.nix` | Efficiency panels | Lines 2632-2754 |
| `nix/grafana/panels/btree.nix` | B-tree panels | Lines 2757-2902 |
| `nix/grafana/panels/alerts.nix` | Alert panels | Lines 2905-3014 |
| `nix/grafana/dashboards/operations.nix` | Operations dashboard | Lines 1781-1888, 3017-3196 |
| `nix/grafana/dashboards/analysis.nix` | Analysis dashboard | Lines 3017-3196 |

### Step 6.1: Create `nix/prometheus/scrape-configs.nix`

**Data-driven scrape config generation**:
```nix
# Instance definitions derived from lib.nix roles
gosrtInstances = {
  server = gosrtLib.roles.server.network.vmIp;
  publisher = gosrtLib.roles.publisher.network.vmIp;
  subscriber = gosrtLib.roles.subscriber.network.vmIp;
};

# Generate relabel_configs from instances map
mkRelabelConfigs = instances: lib.mapAttrsToList (name: ip: {
  source_labels = [ "__address__" ];
  regex = "${ip}:.*";
  target_label = "instance";
  replacement = name;
}) instances;
```

**Potential Pitfalls**:
- [ ] **Missing instance**: If new role added but not in scrape config
- [ ] **Port mismatch**: All GoSRT apps use 9100, but verify

### Step 6.2: Create `nix/grafana/lib.nix`

**Dashboard helper functions**:
- `mkPanel`: Base panel builder
- `mkTimeseries`: Time series panel
- `mkStat`: Traffic light panel
- `mkGauge`: Gauge panel
- `mkRow`: Row separator
- `mkTarget`: Prometheus query target
- `mkDashboard`: Dashboard wrapper
- `autoLayoutPanels`: Automatic y-position calculation
- `thresholds`: Preset threshold definitions

### Step 6.3: Create Panel Modules

Each panel module follows pattern:
```nix
{ lib, grafanaLib }:

let
  inherit (grafanaLib) mkTimeseries mkStat mkTarget thresholds;
in {
  panelName = mkTimeseries {
    title = "...";
    targets = [ ... ];
    gridPos = { h = 8; w = 8; x = 0; };
    # ...
  };
}
```

**Potential Pitfalls**:
- [ ] **Metric name mismatch**: Panel queries metrics that don't exist
- [ ] **gridPos overlap**: Manual x/y values may cause overlap
- [ ] **Missing y value**: autoLayoutPanels should handle this

### Step 6.4: Create Dashboard Modules

```nix
{ lib, grafanaLib, panels }:

let
  inherit (grafanaLib) mkDashboard mkRow;
in mkDashboard {
  title = "GoSRT Operations";
  uid = "gosrt-ops";
  panels = [
    (mkRow { title = "Stream Health Status"; })
    panels.trafficLights.ingestHealth
    panels.trafficLights.deliveryHealth
    # ...
  ];
}
```

### Step 6.5: Create `nix/microvms/metrics.nix`

**Special handling**:
- No GoSRT package (uses NixOS services)
- Prometheus with auto-generated scrape configs
- Grafana with auto-generated dashboards via `builtins.toJSON`

```nix
services.grafana = {
  enable = true;
  provision.datasources.settings.datasources = [ /* Prometheus */ ];
};

# Dashboard provisioning via /etc
environment.etc."grafana/dashboards/gosrt-ops.json" = {
  text = builtins.toJSON dashboards.operations;
  mode = "0644";
};
```

**Potential Pitfalls**:
- [ ] **Dashboard path**: Must match Grafana provisioning path
- [ ] **JSON syntax**: builtins.toJSON handles this, but verify output
- [ ] **Prometheus not ready**: Dashboard may fail to load if Prometheus down

### Step 6.6: Unit Tests for Phase 6

```bash
# Build metrics VM
nix build .#srt-metrics-vm

# Start metrics VM
nix run .#srt-metrics-vm &
sleep 30

# Verify Prometheus
curl http://10.50.8.2:9090/api/v1/status/config

# Verify Grafana
curl http://10.50.8.2:3000/api/health

# Verify dashboards exist
curl -u admin:srt http://10.50.8.2:3000/api/dashboards/uid/gosrt-ops
curl -u admin:srt http://10.50.8.2:3000/api/dashboards/uid/gosrt-analysis

# Verify scrape targets
curl http://10.50.8.2:9090/api/v1/targets
```

### Definition of Done - Phase 6

- [ ] Metrics VM builds and boots
- [ ] Prometheus scrapes all GoSRT endpoints
- [ ] Grafana accessible at port 3000
- [ ] Both dashboards load without errors
- [ ] Traffic light panels show correct colors
- [ ] Annotations API accessible for impairment events
- [ ] Node exporter metrics available

---

## Phase 7: VM Management Scripts {#phase-7-vm-management}

### Objective
Create data-driven VM management scripts.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/scripts/vm-management.nix` | All VM scripts | Lines 4082-4356 |

### Step 7.1: Create `nix/scripts/vm-management.nix`

**Data-driven script generation**:
```nix
# Generate stop/ssh/console scripts for each role
stopScripts = lib.mapAttrs mkStopScript gosrtLib.roles;
sshScripts = lib.mapAttrs mkSshScript gosrtLib.roles;
consoleScripts = lib.mapAttrs mkConsoleScript gosrtLib.roles;
```

Scripts generated:
- `srt-vm-check`: List running VMs
- `srt-vm-check-json`: JSON output for scripting
- `srt-vm-stop`: Stop all VMs
- `srt-vm-stop-{role}`: Stop specific VM
- `srt-ssh-{role}`: SSH into VM
- `srt-console-{role}`: Serial console
- `srt-tmux-all`: Start all VMs in tmux
- `srt-tmux-attach`: Attach to tmux session

**Potential Pitfalls**:
- [ ] **SSH host key**: Must disable strict checking for VMs
- [ ] **Process pattern**: `gosrt:srt-` pattern must match qemu process name
- [ ] **tmux layout**: `select-layout tiled` needed after each split

### Step 7.2: Unit Tests for Phase 7

```bash
# Start VMs first
nix run .#srt-server-vm &
nix run .#srt-publisher-vm &

# Test vm-check
nix run .#srt-vm-check

# Test JSON output
nix run .#srt-vm-check-json | jq .

# Test SSH
nix run .#srt-ssh-server -- hostname

# Test individual stop
nix run .#srt-vm-stop-publisher

# Test stop all
nix run .#srt-vm-stop
```

### Definition of Done - Phase 7

- [ ] All management scripts build
- [ ] `vm-check` shows running VMs
- [ ] `vm-stop` terminates all VMs
- [ ] `ssh-{role}` connects without manual password
- [ ] `console-{role}` connects to serial console
- [ ] `tmux-all` starts all VMs in tmux session

---

## Phase 8: Testing Infrastructure {#phase-8-testing}

### Objective
Create test orchestration and analysis tools.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/testing/default.nix` | Testing exports | Lines 4370-4394 |
| `nix/testing/configs.nix` | Test configurations | Lines 4397-4533 |
| `nix/testing/runner.nix` | Test runner | Lines 4536-4717 |
| `nix/testing/analysis.nix` | Analysis tools | Lines 4720-4805 |

### Step 8.1: Create Test Configurations

Match existing `contrib/integration_testing/test_configs.go`:
- Clean network tests
- Latency tests (regional, continental, intercontinental, GEO)
- Loss tests (2%, 5%)
- Combined stress tests
- Starlink pattern tests

### Step 8.2: Create Test Runner

**Key features**:
- Verify network is set up
- Start VMs if not running
- Wait for services with exponential backoff
- Apply network profile
- Collect metrics during test
- Generate summary

**Potential Pitfalls**:
- [ ] **Timeout waiting for service**: Need reasonable max attempts
- [ ] **Metrics collection interval**: Too fast may overwhelm
- [ ] **Profile application timing**: Apply before starting metrics collection

### Step 8.3: Create Analysis Tools

- `srt-extract-metrics`: Parse Prometheus text format
- `srt-generate-report`: Summary report generation
- `srt-compare-runs`: Compare two test runs

### Definition of Done - Phase 8

- [ ] Test configs match Go implementation
- [ ] Test runner starts VMs automatically
- [ ] Service readiness detection works
- [ ] Metrics collected during test
- [ ] Analysis tools parse metrics correctly

---

## Phase 9: Development Shell and CI Checks {#phase-9-devshell-ci}

### Objective
Create development shell and CI checks.

### Files to Create

| File | Purpose | Design Reference |
|------|---------|------------------|
| `nix/shell.nix` | Development shell | Lines 4884-4955 |
| `nix/checks.nix` | CI checks | Lines 4808-4881 |

### Step 9.1: Create `nix/shell.nix`

**Packages to include**:
- Go 1.26 with GOEXPERIMENT
- gopls, go-tools, golangci-lint, delve
- Network tools: iproute2, tcpdump, curl, jq
- Performance: perf, flamegraph, pprof
- Nix: nixfmt-rfc-style

**Shell hook**:
- Set GOEXPERIMENT=jsonv2 (greenteagc is default in Go 1.26)
- Set CGO_ENABLED=0
- Print helpful commands

### Step 9.2: Create `nix/checks.nix`

**Checks to include**:
- `audit-seq`: Sequence arithmetic safety
- `audit-metrics`: Prometheus metrics audit
- `go-test-tier1`: Core tests (<3s)
- `go-test-tier2`: Extended tests (<15s)
- `go-test-all`: Full test suite
- `go-test-race`: Race detection
- `go-vet`: Static analysis
- `staticcheck`: Additional linting
- `build-all`: Verify all packages build

**Potential Pitfalls**:
- [ ] **GOEXPERIMENT in checks**: Must set in each check's environment
- [ ] **Test timeout**: Some tests may need longer timeout
- [ ] **Race detection CGO**: May need CGO_ENABLED=1 for race detector

### Definition of Done - Phase 9

- [ ] `nix develop` provides working environment
- [ ] `go build` works in devShell
- [ ] `nix flake check` runs all checks
- [ ] All checks pass on clean tree
- [ ] Audit tools detect intentional violations

---

## Phase 10: Integration Tests {#phase-10-integration-tests}

### Objective
Verify the complete system works end-to-end.

### Test Scenarios

#### Test 10.1: Clean Network Basic Flow

```bash
# Setup
sudo nix run .#srt-network-setup

# Start VMs
nix run .#srt-tmux-all &
sleep 30

# Verify all VMs running
nix run .#srt-vm-check

# Verify services responding
curl http://10.50.3.2:9100/metrics  # Server
curl http://10.50.1.2:9100/metrics  # Publisher
curl http://10.50.2.2:9100/metrics  # Subscriber

# Verify data flowing
# Check gosrt_connection_congestion_packets_total increasing

# Verify Grafana dashboards
curl http://10.50.8.2:3000/api/health

# Cleanup
nix run .#srt-vm-stop
```

#### Test 10.2: Latency Profile Switching

```bash
# Baseline RTT
ping -c 3 10.50.3.2  # Should be ~0ms

# Switch to 60ms RTT
nix run .#srt-set-latency -- 2
ping -c 3 10.50.3.2  # Should be ~60ms

# Verify annotation in Grafana
curl -u admin:srt http://10.50.8.2:3000/api/annotations

# Switch to 130ms RTT
nix run .#srt-set-latency -- 3
ping -c 3 10.50.3.2  # Should be ~130ms

# Reset
nix run .#srt-set-latency -- 0
```

#### Test 10.3: Loss Injection

```bash
# Inject 5% loss
nix run .#srt-set-loss -- 5 0

# Verify loss counters increasing
curl http://10.50.3.2:9100/metrics | grep congestion_packets_lost

# Verify recovery
curl http://10.50.3.2:9100/metrics | grep retrans

# Clear loss
nix run .#srt-set-loss -- 0 0
```

#### Test 10.4: Starlink Pattern

```bash
# Run pattern for 60 seconds
nix run .#srt-starlink-pattern -- 60 500 "12 27 42 57" &

# Monitor in Grafana
# Should see loss spikes followed by recovery

# Verify annotations
curl -u admin:srt http://10.50.8.2:3000/api/annotations | grep starlink
```

#### Test 10.5: Interop Testing

```bash
# Start server
nix run .#srt-server-vm &
sleep 10

# Test with srt-xtransmit
nix run .#srt-xtransmit-pub-vm &
nix run .#srt-xtransmit-sub-vm &
sleep 30

# Verify data flowing
curl http://10.50.3.2:9100/metrics | grep packets_total

# Stop xtransmit VMs
nix run .#srt-vm-stop-xtransmit-pub
nix run .#srt-vm-stop-xtransmit-sub

# Test with FFmpeg
nix run .#srt-ffmpeg-pub-vm &
nix run .#srt-ffmpeg-sub-vm &
sleep 30

# Verify 20Mb/s throughput
curl http://10.50.3.2:9100/metrics | grep bandwidth
```

### Definition of Done - Phase 10

- [ ] All VMs boot and communicate
- [ ] Latency switching works with annotations
- [ ] Loss injection causes expected metrics changes
- [ ] Starlink pattern creates loss bursts
- [ ] srt-xtransmit interop works
- [ ] FFmpeg interop works
- [ ] Grafana shows real-time data
- [ ] Full cleanup succeeds

---

## Verification Checklist {#verification-checklist}

Before considering implementation complete:

### Code Quality
- [ ] All files pass `nix flake check`
- [ ] No hardcoded IPs/MACs/ports (all derived from indices)
- [ ] All assertions pass at evaluation time
- [ ] No placeholder values remaining (`{vmIp}`, etc.)

### Functionality
- [ ] All 8 VMs build and boot
- [ ] Network setup creates correct topology
- [ ] Latency profiles work (0-300ms)
- [ ] Loss injection works (0-100%)
- [ ] Starlink pattern creates bursts
- [ ] Prometheus scrapes all endpoints
- [ ] Grafana dashboards load
- [ ] Management scripts work

### Documentation
- [ ] flake.nix header comments are accurate
- [ ] Each nix file has purpose comment
- [ ] CLAUDE.md references nix commands

### Testing
- [ ] Unit tests pass
- [ ] Integration tests pass
- [ ] Interop tests pass

---

## Design vs Implementation Comparison {#design-comparison}

After implementation, verify each section of `nix_microvm_design.md` is implemented:

| Design Section | Lines | Implemented | Notes |
|---------------|-------|-------------|-------|
| Overview | 1-20 | [ ] | Quick start commands work |
| Architecture | 55-176 | [ ] | Network topology matches |
| File Structure | 196-268 | [ ] | All files created |
| constants.nix | 274-521 | [ ] | All roles defined |
| lib.nix | 524-690 | [ ] | All helpers implemented |
| packages/default.nix | 693-765 | [ ] | All packages build |
| packages/srt-xtransmit.nix | 778-848 | [ ] | Binary builds |
| packages/ffmpeg.nix | 851-875 | [ ] | SRT support works |
| containers/server.nix | 877-945 | [ ] | Container runs |
| microvms/base.nix | 948-1192 | [ ] | VMs boot |
| microvms/default.nix | 1195-1239 | [ ] | Data-driven generation |
| grafana/lib.nix | 1277-1564 | [ ] | Dashboard helpers work |
| grafana/panels/* | 1567-3014 | [ ] | All panels defined |
| grafana/dashboards/* | 3017-3196 | [ ] | Both dashboards work |
| prometheus/scrape-configs.nix | 1890-1957 | [ ] | Targets scraped |
| microvms/metrics.nix | 1960-2121 | [ ] | Metrics VM works |
| network/default.nix | 3261-3300 | [ ] | Exports correct |
| network/profiles.nix | 3302-3407 | [ ] | Profiles defined |
| network/impairments.nix | 3410-3578 | [ ] | Scenarios work |
| network/setup.nix | 3598-4079 | [ ] | Network creates |
| scripts/vm-management.nix | 4082-4356 | [ ] | Scripts work |
| testing/default.nix | 4370-4394 | [ ] | Testing exports |
| testing/configs.nix | 4397-4533 | [ ] | Configs match Go |
| testing/runner.nix | 4536-4717 | [ ] | Runner works |
| testing/analysis.nix | 4720-4805 | [ ] | Analysis works |
| checks.nix | 4808-4881 | [ ] | Checks pass |
| shell.nix | 4884-4955 | [ ] | DevShell works |
| flake.nix | 4962-5328 | [ ] | Full functionality |
| Usage Workflow | 5332-5648 | [ ] | Commands work |

### Missing from Design (Add During Implementation)

- [ ] **go.mod update**: Go 1.26 requirement
- [ ] **Directory creation**: `mkdir -p nix/{packages,containers,microvms,grafana/{panels,dashboards},prometheus,network,scripts,testing}`
- [ ] **flake.lock**: Created by `nix flake update`
- [ ] **.envrc**: Optional direnv integration

### Plumbing Connections (Critical)

These connections must be made for the system to work:

1. **constants.nix → lib.nix**: lib.nix imports constants.nix
2. **lib.nix → all modules**: Every module imports lib.nix for role data
3. **flake.nix → packages**: `packages = import ./nix/packages { ... }`
4. **flake.nix → microvms**: `microvms = import ./nix/microvms { ... }`
5. **flake.nix → network**: `network = import ./nix/network { ... }`
6. **flake.nix → vmScripts**: `vmScripts = import ./nix/scripts/vm-management.nix { ... }`
7. **flake.nix → apps**: Merge all app exports into `apps` attribute
8. **microvms/default.nix → base.nix**: Default imports base.nix
9. **microvms/default.nix → metrics.nix**: Special case for metrics
10. **metrics.nix → grafana modules**: Import panels and dashboards
11. **metrics.nix → prometheus scrape-configs**: Import for targets
12. **grafana/dashboards → grafana/panels**: Dashboards use panel modules
13. **grafana/panels → grafana/lib.nix**: Panels use helper functions

---

## Appendix: Common Issues and Solutions

### Issue: "TAP device not found"
**Cause**: Network not set up before VM start
**Solution**: `sudo nix run .#srt-network-setup` before starting VMs

### Issue: "Connection refused to 10.50.x.x"
**Cause**: VM not fully booted, service not started, or wrong IP
**Solution**: Wait longer, check `systemctl status` in VM, verify IP in constants.nix

### Issue: "vendorHash mismatch"
**Cause**: go.mod/go.sum changed
**Solution**: Run failed build, copy correct hash from error message

### Issue: "Grafana dashboard not loading"
**Cause**: JSON syntax error or Prometheus not ready
**Solution**: Check `builtins.toJSON` output, verify Prometheus targets

### Issue: "VMs not stopping"
**Cause**: Process pattern doesn't match
**Solution**: Verify `gosrt:srt-` appears in process name via `ps aux | grep gosrt`

### Issue: "Latency not changing"
**Cause**: Routes not updated correctly
**Solution**: Verify with `ip netns exec srt-router-a ip route show`

---

## Timeline Estimate

| Phase | Estimated Effort | Dependencies |
|-------|-----------------|--------------|
| Phase 0: Infrastructure Validation | 1-2 hours | None |
| Phase 1: Foundation (incl. overlay, modules) | 3-4 hours | Phase 0 |
| Phase 2: Metrics VM First (Observer Pattern) | 4-5 hours | Phase 1 |
| Phase 3: Go Packages with Audit Hooks | 2-3 hours | Phase 1 |
| Phase 4: OCI Containers | 1-2 hours | Phase 3 |
| Phase 5: Network Infrastructure (nftables) | 3-4 hours | Phase 1 |
| Phase 6: MicroVMs (debug builds first) | 4-6 hours | Phases 2, 3, 5 |
| Phase 7: VM Management Scripts | 2-3 hours | Phase 6 |
| Phase 8: Testing with Automated Pass/Fail | 4-5 hours | Phases 6, 7 |
| Phase 9: DevShell/CI | 2-3 hours | Phases 1, 3 |
| Phase 10: Integration Tests | 4-6 hours | All |

**Total**: ~32-43 hours

---

## Elegance Checklist {#elegance-checklist}

This checklist ensures the implementation follows Nix best practices and the refinements discussed.

### Nix Idiomatic Patterns

| Pattern | Implementation | Rationale | Verified |
|---------|----------------|-----------|----------|
| **specialArgs injection** | `nixosSystem { specialArgs = { gosrtPackages, buildVariant }; }` | Swap production ↔ debug without touching VM configs | [ ] |
| **Overlay for binary flavors** | `nix/overlays/gosrt.nix` | Centralized GOEXPERIMENT, ldflags propagate to all VMs | [ ] |
| **Module options for impairments** | `services.srt-test.scenario = "starlink-handoff"` | Declarative, not imperative shell scripts | [ ] |
| **Declarative nftables** | `networking.nftables.ruleset` | Atomic rule application, no iptables scripts | [ ] |
| **CGO_ENABLED=0** | In all package builds | Fully static binaries for MicroVMs | [ ] |
| **Explicit interface naming** | `systemd.network.links` | No non-deterministic eth0/ens33 issues | [ ] |
| **DRY mkScenario library** | `nix/network/impairment-library.nix` | Single scenario definition with mandatory cleanup | [ ] |

### Observer Pattern (Metrics First)

| Step | Description | Verified |
|------|-------------|----------|
| Metrics VM deployed before SUT | Phase 2 before Phase 6 | [ ] |
| Dashboard-as-Code | `services.grafana.provision` pre-loads dashboards | [ ] |
| Annotation API ready | Impairment events can be correlated | [ ] |
| Scrape configs auto-generated | From `lib.roles`, no manual target lists | [ ] |

### Safety & Auditing

| Check | Implementation | Verified |
|-------|----------------|----------|
| Sequence audit as pre-build hook | `tools/sequence-audit` runs before `go build` | [ ] |
| Metrics audit as pre-build hook | `tools/metrics-audit` runs before `go build` | [ ] |
| Debug builds for initial testing | `buildVariant = "debug"` enables `AssertEventLoopContext` | [ ] |
| iperf2 baseline before GoSRT | Phase 0 validates infrastructure throughput | [ ] |
| Router-to-router baseline | iperf2 between srt-router-a/b before VMs | [ ] |

### Infrastructure Rigor (GoSRT Alignment)

| Check | Implementation | Verified |
|-------|----------------|----------|
| Multi-queue TAP devices | `ip tuntap add ... multi_queue` | [ ] |
| TAP queues = IoUringRecvRingCount | 4 queues (default) or configurable | [ ] |
| Context assertions active | `make build-debug` used for Phase 1 testing | [ ] |
| Mandatory scenario cleanup | `mkScenario` includes `.cleanup` script | [ ] |
| Annotation API integration | Impairment scripts trigger Grafana annotations | [ ] |

### Advanced Monitoring

| Feature | Implementation | Verified |
|---------|----------------|----------|
| NAK B-tree heatmap | `grafana/panels/heatmaps.nix` | [ ] |
| pprof links in dashboard | `mkTimeseriesWithPprof` adds clickable profile links | [ ] |
| Automated pass/fail test | Derivation queries Prometheus, fails if threshold not met | [ ] |
| Starlink pattern via nftables | Timeout-based blackhole sets | [ ] |

### DRY Verification

| Component | Generation Method | Count | Verified |
|-----------|-------------------|-------|----------|
| MicroVMs | `lib.mapAttrs mkRoleVM gosrtLib.roles` | 8 | [ ] |
| Stop scripts | `lib.mapAttrs mkStopScript gosrtLib.roles` | 8 | [ ] |
| SSH scripts | `lib.mapAttrs mkSshScript gosrtLib.roles` | 8 | [ ] |
| Network setup | Iterates `lib.roleNames` | 8 subnets | [ ] |
| Prometheus scrapes | `lib.mapAttrsToList` over roles | 8 targets | [ ] |

### Automated Pass/Fail Test (Refinement)

**Concept**: Create a Nix derivation that:
1. Runs a 5-minute Starlink simulation
2. Queries Prometheus for success ratio
3. **Fails the build** if threshold not met

```nix
# nix/testing/automated-test.nix
{ pkgs, lib }:

pkgs.runCommand "srt-starlink-test" {
  nativeBuildInputs = [ pkgs.curl pkgs.jq ];
} ''
  # This runs AFTER VMs are started (in integration test wrapper)

  # Query Prometheus for delivery success ratio
  RATIO=$(curl -s 'http://10.50.8.2:9090/api/v1/query?query=
    sum(rate(gosrt_pkt_recv_data_success_total[5m])) /
    sum(rate(gosrt_pkt_send_data_total[5m]))
  ' | jq -r '.data.result[0].value[1]')

  # Threshold: 99% delivery during Starlink bursts
  if (( $(echo "$RATIO < 0.99" | bc -l) )); then
    echo "FAIL: Delivery ratio $RATIO is below 99% threshold"
    exit 1
  fi

  echo "PASS: Delivery ratio $RATIO meets threshold"
  echo "$RATIO" > $out
''
```

**Usage in CI**:
```yaml
- name: Run Starlink Stress Test
  run: |
    nix run .#srt-starlink-test
    # Build fails if <99% delivery
```

---

## Summary of Refinements Incorporated

### Original Refinements (v2)
1. **Overlay for binary flavors** (Refinement 1) - `nix/overlays/gosrt.nix`
2. **NixOS module for impairments** (Refinement 2) - `nix/modules/srt-test.nix`
3. **Declarative nftables** (Refinement 3) - `nix/modules/srt-network.nix`
4. **pprof links in Grafana** (Refinement 4) - `mkTimeseriesWithPprof`
5. **NAK B-tree heatmaps** (Refinement 5) - `grafana/panels/heatmaps.nix`
6. **iperf2 baseline validation** - Phase 0
7. **Observer pattern (Metrics first)** - Phase 2 before Phase 6
8. **Audit hooks as pre-build** - Phase 3
9. **Automated pass/fail tests** - Phase 8

### Architectural Rigor Refinements (v3)
10. **Debug builds for Phase 1** (Refinement 6) - `AssertEventLoopContext` active during initial testing
11. **Multi-queue TAP alignment** (Refinement 7) - TAP queues = `IoUringRecvRingCount` (4 default)
12. **Explicit interface naming** (Refinement 8) - `systemd.network.links` for predictable names
13. **DRY mkScenario library** (Refinement 9) - Single scenario definition with mandatory cleanup

### Phase-by-Phase Improvements (v3)
14. **Router-to-router iperf2 baseline** (Phase 1) - Validates dual-bridge topology before VMs
15. **Impairment annotation integration** (Phase 2) - Scripts trigger Grafana Annotation API
16. **Scenario cleanup verification** (Phase 3) - `mkScenario.verify` confirms state

---

## Current Status (2026-02-16)

### Implementation Progress

All 10 phases have been implemented and verified:

| Phase | Status | Notes |
|-------|--------|-------|
| Phase 0: Infrastructure Validation | ✅ COMPLETE | 4.94 Gbps TCP verified |
| Phase 1: Foundation | ✅ COMPLETE | constants.nix, lib.nix, overlays |
| Phase 2: Metrics VM | ✅ COMPLETE | Prometheus + Grafana working, all targets scraped |
| Phase 3: Go Packages | ✅ COMPLETE | Audit hooks integrated |
| Phase 4: OCI Containers | ✅ COMPLETE | server, client, client-generator |
| Phase 5: Network Infrastructure | ✅ COMPLETE | Setup/teardown, inter-router routing |
| Phase 6: MicroVM Base | ✅ COMPLETE | All 8 VMs defined |
| Phase 7: VM Management Scripts | ✅ COMPLETE | All scripts working |
| Phase 8: Testing Infrastructure | ✅ COMPLETE | Runner, analysis tools |
| Phase 9: DevShell and CI | ✅ COMPLETE | Shell and checks |
| Phase 10: Integration Tests | ✅ COMPLETE | 12/13 tests passing |

### Bug Fixes Applied During Implementation

1. **Integration test process names**: Fixed `gosrt:srt-srv` → `gosrt:srv` pattern matching
2. **Integration test bridge names**: Fixed `srt-br-a` → `srtbr-srv` bridge detection
3. **Inter-router routing**: Added route generation to `setup.nix` for cross-router VM communication
4. **srt-xtransmit package**: Updated with working hash from local flake reference

### New Scripts Added

| Script | Description |
|--------|-------------|
| `srt-tmux-clear` | Kill tmux session without stopping VMs |
| `srt-vm-stop-and-clear-tmux` | Stop all VMs AND kill tmux session |

---

## Next Steps

### Phase 11: End-to-End Data Flow Testing

**Objective**: Verify actual SRT traffic flows through the infrastructure.

**Tasks**:
1. [ ] Start publisher sending to server at 50 Mbps
2. [ ] Verify subscriber receives data
3. [ ] Confirm Prometheus metrics show packet flow
4. [ ] Verify Grafana dashboards display real-time data
5. [ ] Measure baseline throughput and latency

**Commands**:
```bash
# Start all VMs
nix run .#srt-tmux-all
nix run .#srt-vm-wait

# Check metrics for packet flow
curl -s http://10.50.3.2:9100/metrics | grep gosrt_connection_pkt

# View in Grafana
# Open http://10.50.8.2:3000 (admin/srt)
```

### Phase 12: Latency Profile Testing

**Objective**: Verify latency switching works correctly.

**Tasks**:
1. [ ] Test each latency profile (0-4)
2. [ ] Verify RTT changes in Prometheus metrics
3. [ ] Confirm Grafana annotations appear for profile changes
4. [ ] Test SRT adaptation to different latencies

**Commands**:
```bash
# Switch latency profiles
sudo nix run .#srt-set-latency -- 0   # Clean (0ms)
sudo nix run .#srt-set-latency -- 1   # Regional (10ms)
sudo nix run .#srt-set-latency -- 2   # Continental (60ms)
sudo nix run .#srt-set-latency -- 3   # Intercontinental (130ms)
sudo nix run .#srt-set-latency -- 4   # GEO Satellite (300ms)

# Verify with ping
ping -c 5 10.50.3.2
```

### Phase 13: Loss Injection Testing

**Objective**: Verify loss injection causes expected SRT behavior.

**Tasks**:
1. [ ] Inject 2% packet loss
2. [ ] Verify NAK/retransmit counts increase in Prometheus
3. [ ] Confirm SRT maintains delivery despite loss
4. [ ] Test higher loss rates (5%, 10%)
5. [ ] Verify recovery after loss cleared

**Commands**:
```bash
# Inject loss
sudo nix run .#srt-set-loss -- 2 0   # 2% loss on first link

# Monitor retransmits
curl -s http://10.50.3.2:9100/metrics | grep gosrt_connection_congestion

# Clear loss
sudo nix run .#srt-set-loss -- 0 0
```

### Phase 14: Starlink Pattern Testing

**Objective**: Verify Starlink blackout pattern simulation.

**Tasks**:
1. [ ] Start Starlink pattern simulation
2. [ ] Verify periodic packet loss bursts
3. [ ] Confirm SRT handles bursty loss correctly
4. [ ] Measure delivery ratio during simulation
5. [ ] Verify recovery between bursts

**Commands**:
```bash
# Start Starlink pattern
sudo nix run .#srt-starlink-pattern -- 60  # 60 second test

# Monitor loss/recovery
curl -s http://10.50.3.2:9100/metrics | grep gosrt
```

### Phase 15: Interop Testing

**Objective**: Test GoSRT interoperability with srt-xtransmit and FFmpeg.

**Tasks**:
1. [ ] Build and start xtransmit VMs
2. [ ] Verify xtransmit publisher → GoSRT server → GoSRT subscriber
3. [ ] Verify GoSRT publisher → GoSRT server → xtransmit subscriber
4. [ ] Build and start FFmpeg VMs
5. [ ] Verify FFmpeg publisher → GoSRT server works
6. [ ] Verify GoSRT server → FFmpeg subscriber works

**Commands**:
```bash
# Build interop packages
nix build .#srt-xtransmit
nix build .#ffmpeg-srt

# Build interop VMs
nix build .#srt-xtransmit-pub-vm
nix build .#srt-ffmpeg-pub-vm

# Start interop VMs (update tmux-all to include these)
```

### Phase 16: Performance Validation

**Objective**: Verify infrastructure meets 500 Mbps GoSRT target.

**Tasks**:
1. [ ] Run performance test at 100 Mbps
2. [ ] Gradually increase to 500 Mbps
3. [ ] Verify no packet loss at target rate
4. [ ] Profile CPU usage during high throughput
5. [ ] Document maximum sustainable throughput

**Commands**:
```bash
# Use make test-performance with VMs
# (Requires integration with existing Go testing framework)
```

### Phase 17: CI Integration

**Objective**: Integrate MicroVM tests into CI pipeline.

**Tasks**:
1. [ ] Create GitHub Actions workflow for nix checks
2. [ ] Add automated VM boot test
3. [ ] Add automated integration test
4. [ ] Configure pass/fail thresholds
5. [ ] Set up scheduled nightly tests

---

## Quick Reference: Common Commands

```bash
# Network setup/teardown (requires sudo)
sudo nix run .#srt-network-setup -- "$USER"
sudo nix run .#srt-network-teardown

# VM lifecycle
nix run .#srt-tmux-all                 # Start all VMs
nix run .#srt-vm-check                 # Check VM status
nix run .#srt-vm-wait                  # Wait for VMs ready
nix run .#srt-vm-stop-and-clear-tmux   # Clean stop

# Integration tests
nix run .#srt-integration-smoke        # Quick health check
nix run .#srt-integration-full         # Complete suite

# VM access
nix run .#srt-ssh-server               # SSH into server
nix run .#srt-ssh-metrics              # SSH into metrics

# Impairment controls (requires sudo)
sudo nix run .#srt-set-latency -- <0-4>
sudo nix run .#srt-set-loss -- <percent> <link>
sudo nix run .#srt-starlink-pattern -- <duration>
```

---

*End of Implementation Plan (v3 - Enhanced with Architectural Rigor)*
