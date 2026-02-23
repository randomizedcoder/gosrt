# flake.nix
#
# GoSRT Nix Flake - MicroVM infrastructure for integration testing.
#
# Reference: documentation/nix_microvm_design.md
# Implementation: documentation/nix_microvm_implementation_plan.md
#
# Quick Start:
#   nix flake check --no-build   # Validate flake
#   nix eval .#lib               # Inspect library exports
#   nix build .#gosrt-debug      # Build debug binary
#
# VM Management Commands:
#
#   # Network setup (requires sudo, one-time)
#   sudo nix run .#srt-network-setup -- "$USER"
#
#   # Start VMs
#   nix run .#srt-vm-start-background      # Start all VMs in background (logs: /tmp/gosrt-vms/)
#   nix run .#srt-tmux-all                 # Start all VMs in tmux session (interactive)
#
#   # Check VM status
#   nix run .#srt-vm-is-running -- server  # Check single VM (exit 0=running, 1=not, 2=invalid)
#   nix run .#srt-vm-all-running           # Check all 4 core VMs (exit 0=all running, 1=some missing)
#   nix run .#srt-vm-check                 # Show status table for all VMs
#   nix run .#srt-vm-wait                  # Wait until VMs are SSH-accessible
#
#   # Stop VMs
#   nix run .#srt-vm-stop                  # Stop all VMs
#   nix run .#srt-vm-stop-and-clear-tmux   # Stop VMs and kill tmux session
#
#   # Access VMs
#   nix run .#srt-ssh-server               # SSH into server (password: srt)
#   nix run .#srt-ssh-metrics              # SSH into metrics VM
#
#   # Cleanup
#   sudo nix run .#srt-network-teardown    # Remove network resources
#
{
  description = "GoSRT - Pure Go SRT implementation with MicroVM test infrastructure";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";

    # MicroVM infrastructure
    microvm = {
      url = "github:astro/microvm.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  # Binary cache for MicroVM builds
  nixConfig = {
    extra-substituters = [ "https://microvm.cachix.org" ];
    extra-trusted-public-keys = [
      "microvm.cachix.org-1:oXnBc6hRE3eX5rSYdRyMYXnfzcCxC7yKPTbZXALsqys="
    ];
  };

  outputs = { self, nixpkgs, flake-utils, microvm }:
    let
      # Overlay for GoSRT binary flavors
      gosrtOverlay = import ./nix/overlays/gosrt.nix;

      # Supported systems
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];

    in flake-utils.lib.eachSystem supportedSystems (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ gosrtOverlay ];
        };

        lib = pkgs.lib;

        # Import GoSRT library with computed values
        gosrtLib = import ./nix/lib.nix { inherit lib; };

        # Import iperf validation VMs
        iperfVMs = import ./nix/validation/iperf-test.nix {
          inherit pkgs lib system;
          inherit microvm nixpkgs;
        };

        # Phase 2: Metrics VM (standalone for early testing)
        metricsVM = import ./nix/microvms/metrics.nix {
          inherit pkgs lib system;
          inherit microvm nixpkgs;
        };

        # Phase 6: All MicroVMs (data-driven generator)
        allVMs = import ./nix/microvms {
          inherit pkgs lib system;
          inherit microvm nixpkgs;
          gosrtPackage = pkgs.gosrt.prod;
          gosrtPackageDebug = pkgs.gosrt.debug;
          srtXtransmitPackage = gosrtPackages.srt-xtransmit;
          ffmpegPackage = gosrtPackages.ffmpeg-srt;
        };

        # Impairment annotations
        impairmentAnnotations = import ./nix/network/impairment-annotations.nix {
          inherit pkgs lib;
          metricsIp = gosrtLib.roles.metrics.network.vmIp;
        };

        # Metrics VM network scripts
        metricsNetwork = import ./nix/microvms/metrics-network.nix {
          inherit pkgs lib;
        };

        # Phase 3: Package exports with audit hooks
        gosrtPackages = import ./nix/packages {
          inherit pkgs lib;
          src = ./.;
        };

        # Phase 4: OCI Containers
        containers = import ./nix/containers {
          inherit pkgs lib;
          gosrtPackage = pkgs.gosrt.prod;
        };

        # Phase 5: Network Infrastructure
        networkInfra = import ./nix/network {
          inherit pkgs lib;
        };

        # Phase 7: VM Management Scripts
        vmScripts = import ./nix/scripts {
          inherit pkgs lib;
        };

        # Phase 8: Testing Infrastructure
        testing = import ./nix/testing {
          inherit pkgs lib;
        };

      in {
        # ─── Library Exports ─────────────────────────────────────────────────
        # Expose lib for debugging and external use
        lib = gosrtLib;

        # ─── Packages ────────────────────────────────────────────────────────
        packages = {
          # GoSRT binary flavors from overlay
          gosrt-prod = pkgs.gosrt.prod;
          gosrt-debug = pkgs.gosrt.debug;
          gosrt-perf = pkgs.gosrt.perf;

          # Phase 0: iperf validation
          iperf-server-vm = iperfVMs.server;
          iperf-client-vm = iperfVMs.client;

          # Privileged scripts (run with sudo)
          iperf-network-setup-privileged = iperfVMs.privilegedSetupScript;
          iperf-network-cleanup-privileged = iperfVMs.privilegedCleanupScript;

          # Unprivileged test (run after network setup)
          iperf-test-unprivileged = iperfVMs.unprivilegedTestScript;

          # Legacy scripts (kept for compatibility)
          iperf-network-setup = iperfVMs.setupScript;
          iperf-network-cleanup = iperfVMs.cleanupScript;
          iperf-test = iperfVMs.testScript;

          # Phase 2: Metrics VM
          srt-metrics-vm = metricsVM.vm;
          test-annotation = impairmentAnnotations.testAnnotation;
          metrics-network-setup-privileged = metricsNetwork.privilegedSetupScript;
          metrics-network-cleanup-privileged = metricsNetwork.privilegedCleanupScript;

          # Phase 3: Packages with audit hooks
          gosrt-prod-audited = gosrtPackages.gosrt-prod;
          gosrt-debug-audited = gosrtPackages.gosrt-debug;
          gosrt-perf-audited = gosrtPackages.gosrt-perf;
          gosrt-prod-fast = gosrtPackages.gosrt-prod-fast;
          gosrt-debug-fast = gosrtPackages.gosrt-debug-fast;
          ffmpeg-srt = gosrtPackages.ffmpeg-srt;
          srt-xtransmit = gosrtPackages.srt-xtransmit;

          # Phase 4: OCI Containers
          server-container = containers.server;
          client-container = containers.client;
          client-generator-container = containers.client-generator;

          # Phase 5: Network Infrastructure
          srt-network-setup = networkInfra.setupScript;
          srt-network-teardown = networkInfra.teardownScript;
          srt-set-latency = networkInfra.setLatencyScript;
          srt-set-loss = networkInfra.setLossScript;
          srt-starlink-pattern = networkInfra.starlinkPatternScript;
          srt-starlink-reconvergence = networkInfra.starlinkReconvergenceScript;
          srt-starlink-start = networkInfra.starlinkStartScript;
          srt-starlink-stop = networkInfra.starlinkStopScript;
          srt-starlink-status = networkInfra.starlinkStatusScript;

          # Phase 6: MicroVMs (production builds)
          srt-server-vm = allVMs.server;
          srt-publisher-vm = allVMs.publisher;
          srt-subscriber-vm = allVMs.subscriber;
          srt-xtransmit-pub-vm = allVMs.xtransmit-pub;
          srt-xtransmit-sub-vm = allVMs.xtransmit-sub;
          srt-ffmpeg-pub-vm = allVMs.ffmpeg-pub;
          srt-ffmpeg-sub-vm = allVMs.ffmpeg-sub;

          # Phase 6: MicroVMs (debug builds)
          srt-server-vm-debug = allVMs.debug.server;
          srt-publisher-vm-debug = allVMs.debug.publisher;
          srt-subscriber-vm-debug = allVMs.debug.subscriber;

          # Phase 7: VM Management Scripts
          srt-vm-is-running = vmScripts.vmIsRunning;
          srt-vm-all-running = vmScripts.vmAllRunning;
          srt-vm-check = vmScripts.vmCheck;
          srt-vm-check-json = vmScripts.vmCheckJson;
          srt-vm-stop = vmScripts.vmStopAll;
          srt-vm-wait = vmScripts.vmWait;
          srt-vm-start-background = vmScripts.vmStartBackground;
          srt-tmux-all = vmScripts.tmuxAll;
          srt-tmux-attach = vmScripts.tmuxAttach;
          srt-tmux-clear = vmScripts.tmuxClear;
          srt-vm-stop-and-clear-tmux = vmScripts.vmStopAndClearTmux;
          srt-vm-restart = vmScripts.vmRestart;

          # Phase 7: Per-role SSH scripts
          srt-ssh-server = vmScripts.ssh.server;
          srt-ssh-publisher = vmScripts.ssh.publisher;
          srt-ssh-subscriber = vmScripts.ssh.subscriber;
          srt-ssh-metrics = vmScripts.ssh.metrics;

          # Phase 7: Per-role console scripts
          srt-console-server = vmScripts.console.server;
          srt-console-publisher = vmScripts.console.publisher;
          srt-console-subscriber = vmScripts.console.subscriber;

          # Phase 7: Per-role stop scripts
          srt-vm-stop-server = vmScripts.stop.server;
          srt-vm-stop-publisher = vmScripts.stop.publisher;
          srt-vm-stop-subscriber = vmScripts.stop.subscriber;
          srt-vm-stop-metrics = vmScripts.stop.metrics;
          srt-vm-stop-xtransmit-pub = vmScripts.stop.xtransmit-pub;
          srt-vm-stop-xtransmit-sub = vmScripts.stop.xtransmit-sub;
          srt-vm-stop-ffmpeg-pub = vmScripts.stop.ffmpeg-pub;
          srt-vm-stop-ffmpeg-sub = vmScripts.stop.ffmpeg-sub;

          # Phase 7: Per-role status scripts
          srt-vm-status-server = vmScripts.status.server;
          srt-vm-status-publisher = vmScripts.status.publisher;
          srt-vm-status-subscriber = vmScripts.status.subscriber;
          srt-vm-status-metrics = vmScripts.status.metrics;
          srt-vm-status-xtransmit-pub = vmScripts.status.xtransmit-pub;
          srt-vm-status-xtransmit-sub = vmScripts.status.xtransmit-sub;
          srt-vm-status-ffmpeg-pub = vmScripts.status.ffmpeg-pub;
          srt-vm-status-ffmpeg-sub = vmScripts.status.ffmpeg-sub;

          # Phase 8: Testing Infrastructure
          srt-test-runner = testing.testRunner;
          srt-test-run-tier = testing.testRunTier;
          srt-start-all = testing.testStartAll;
          srt-wait-for-service = testing.testWaitService;
          srt-extract-metrics = testing.extractMetricsScript;
          srt-generate-report = testing.generateReportScript;
          srt-compare-runs = testing.compareRunsScript;
          srt-check-pass = testing.checkPassScript;

          # Phase 10: Integration Tests
          srt-integration-basic = testing.integrationBasic;
          srt-integration-latency = testing.integrationLatency;
          srt-integration-loss = testing.integrationLoss;
          srt-integration-full = testing.integrationFull;
          srt-integration-smoke = testing.integrationSmoke;

          # Default is debug for development
          default = pkgs.gosrt.debug;
        };

        # ─── Development Shell ───────────────────────────────────────────────
        # Phase 9: Use shell.nix for comprehensive dev environment
        devShells.default = import ./nix/shell.nix { inherit pkgs; };

        # ─── Checks ──────────────────────────────────────────────────────────
        # Phase 9: CI checks from checks.nix
        checks = let
          ciChecks = import ./nix/checks.nix {
            inherit pkgs lib;
            src = ./.;
          };
        in ciChecks // {
          # Additional: Validate constants and lib evaluation
          lib-eval = pkgs.runCommand "check-lib-eval" {} ''
            # This succeeds if lib.nix evaluates without errors
            echo "Roles: ${builtins.concatStringsSep ", " gosrtLib.roleNames}"
            echo "Server IP: ${gosrtLib.serverIp}"
            touch $out
          '';
        };

        # ─── Apps ────────────────────────────────────────────────────────────
        apps = let
          # Helper to create app with meta
          mkApp = program: desc: {
            type = "app";
            inherit program;
            meta.description = desc;
          };
        in {
          # Phase 0: iperf validation - Privileged workflow (recommended)
          # Usage:
          #   1. sudo nix run .#iperf-network-setup-privileged -- "$USER"
          #   2. nix run .#iperf-test-unprivileged
          #   3. sudo nix run .#iperf-network-cleanup-privileged
          iperf-network-setup-privileged = mkApp
            "${iperfVMs.privilegedSetupScript}/bin/iperf-network-setup-privileged"
            "Set up iperf network namespace (privileged)";
          iperf-network-cleanup-privileged = mkApp
            "${iperfVMs.privilegedCleanupScript}/bin/iperf-network-cleanup-privileged"
            "Clean up iperf network namespace (privileged)";
          iperf-test-unprivileged = mkApp
            "${iperfVMs.unprivilegedTestScript}/bin/iperf-test-unprivileged"
            "Run iperf bandwidth test";

          # Phase 0: Legacy apps (embedded sudo - kept for compatibility)
          iperf-test = mkApp "${iperfVMs.testScript}/bin/iperf-test" "Run iperf test (legacy)";
          iperf-server-vm = mkApp "${iperfVMs.server}/bin/microvm-run" "Run iperf server VM";
          iperf-client-vm = mkApp "${iperfVMs.client}/bin/microvm-run" "Run iperf client VM";
          iperf-network-setup = mkApp "${iperfVMs.setupScript}/bin/iperf-network-setup" "Set up iperf network (legacy)";
          iperf-network-cleanup = mkApp "${iperfVMs.cleanupScript}/bin/iperf-network-cleanup" "Clean up iperf network (legacy)";

          # Phase 2: Metrics VM
          metrics-network-setup-privileged = mkApp "${metricsNetwork.privilegedSetupScript}/bin/metrics-network-setup-privileged" "Set up metrics network (privileged)";
          metrics-network-cleanup-privileged = mkApp "${metricsNetwork.privilegedCleanupScript}/bin/metrics-network-cleanup-privileged" "Clean up metrics network (privileged)";
          srt-metrics-vm = mkApp "${metricsVM.vm}/bin/microvm-run" "Run Prometheus/Grafana metrics VM";
          test-annotation = mkApp "${impairmentAnnotations.testAnnotation}/bin/test-annotation" "Test Grafana annotation API";

          # Phase 5: Network Infrastructure
          srt-network-setup = mkApp "${networkInfra.setupScript}/bin/srt-network-setup" "Set up SRT network namespaces";
          srt-network-teardown = mkApp "${networkInfra.teardownScript}/bin/srt-network-teardown" "Tear down SRT network";
          srt-set-latency = mkApp "${networkInfra.setLatencyScript}/bin/srt-set-latency" "Set network latency profile";
          srt-set-loss = mkApp "${networkInfra.setLossScript}/bin/srt-set-loss" "Set packet loss percentage";
          srt-starlink-pattern = mkApp "${networkInfra.starlinkPatternScript}/bin/srt-starlink-pattern" "Simple Starlink blackout pattern";
          srt-starlink-reconvergence = mkApp "${networkInfra.starlinkReconvergenceScript}/bin/srt-starlink-reconvergence" "Starlink reconvergence simulation";
          srt-starlink-start = mkApp "${networkInfra.starlinkStartScript}/bin/srt-starlink-start" "Start Starlink simulation (background)";
          srt-starlink-stop = mkApp "${networkInfra.starlinkStopScript}/bin/srt-starlink-stop" "Stop Starlink simulation";
          srt-starlink-status = mkApp "${networkInfra.starlinkStatusScript}/bin/srt-starlink-status" "Check Starlink simulation status";

          # Phase 6: MicroVM apps (production)
          srt-server-vm = mkApp "${allVMs.server}/bin/microvm-run" "Run GoSRT server VM";
          srt-publisher-vm = mkApp "${allVMs.publisher}/bin/microvm-run" "Run GoSRT publisher VM";
          srt-subscriber-vm = mkApp "${allVMs.subscriber}/bin/microvm-run" "Run GoSRT subscriber VM";

          # Phase 6: MicroVM apps (debug)
          srt-server-vm-debug = mkApp "${allVMs.debug.server}/bin/microvm-run" "Run GoSRT server VM (debug)";
          srt-publisher-vm-debug = mkApp "${allVMs.debug.publisher}/bin/microvm-run" "Run GoSRT publisher VM (debug)";
          srt-subscriber-vm-debug = mkApp "${allVMs.debug.subscriber}/bin/microvm-run" "Run GoSRT subscriber VM (debug)";

          # Phase 7: VM Management apps
          srt-vm-is-running = mkApp "${vmScripts.vmIsRunning}/bin/srt-vm-is-running" "Check if a VM is running";
          srt-vm-all-running = mkApp "${vmScripts.vmAllRunning}/bin/srt-vm-all-running" "Check if all core VMs are running";
          srt-vm-check = mkApp "${vmScripts.vmCheck}/bin/srt-vm-check" "Show VM status table";
          srt-vm-check-json = mkApp "${vmScripts.vmCheckJson}/bin/srt-vm-check-json" "Show VM status as JSON";
          srt-vm-stop = mkApp "${vmScripts.vmStopAll}/bin/srt-vm-stop" "Stop all VMs";
          srt-vm-wait = mkApp "${vmScripts.vmWait}/bin/srt-vm-wait" "Wait for VMs to be ready";
          srt-vm-start-background = mkApp "${vmScripts.vmStartBackground}/bin/srt-vm-start-background" "Start all VMs in background";
          srt-tmux-all = mkApp "${vmScripts.tmuxAll}/bin/srt-tmux-all" "Start all VMs in tmux";
          srt-tmux-attach = mkApp "${vmScripts.tmuxAttach}/bin/srt-tmux-attach" "Attach to VM tmux session";
          srt-tmux-clear = mkApp "${vmScripts.tmuxClear}/bin/srt-tmux-clear" "Kill VM tmux session";
          srt-vm-stop-and-clear-tmux = mkApp "${vmScripts.vmStopAndClearTmux}/bin/srt-vm-stop-and-clear-tmux" "Stop VMs and clear tmux";
          srt-vm-restart = mkApp "${vmScripts.vmRestart}/bin/srt-vm-restart" "Restart VMs";

          # Phase 7: Per-role SSH apps
          srt-ssh-server = mkApp "${vmScripts.ssh.server}/bin/srt-ssh-server" "SSH into server VM";
          srt-ssh-publisher = mkApp "${vmScripts.ssh.publisher}/bin/srt-ssh-publisher" "SSH into publisher VM";
          srt-ssh-subscriber = mkApp "${vmScripts.ssh.subscriber}/bin/srt-ssh-subscriber" "SSH into subscriber VM";
          srt-ssh-metrics = mkApp "${vmScripts.ssh.metrics}/bin/srt-ssh-metrics" "SSH into metrics VM";

          # Phase 7: Per-role console apps
          srt-console-server = mkApp "${vmScripts.console.server}/bin/srt-console-server" "Serial console to server VM";
          srt-console-publisher = mkApp "${vmScripts.console.publisher}/bin/srt-console-publisher" "Serial console to publisher VM";
          srt-console-subscriber = mkApp "${vmScripts.console.subscriber}/bin/srt-console-subscriber" "Serial console to subscriber VM";

          # Phase 7: Per-role stop apps
          srt-vm-stop-server = mkApp "${vmScripts.stop.server}/bin/srt-vm-stop-server" "Stop server VM";
          srt-vm-stop-publisher = mkApp "${vmScripts.stop.publisher}/bin/srt-vm-stop-publisher" "Stop publisher VM";
          srt-vm-stop-subscriber = mkApp "${vmScripts.stop.subscriber}/bin/srt-vm-stop-subscriber" "Stop subscriber VM";
          srt-vm-stop-metrics = mkApp "${vmScripts.stop.metrics}/bin/srt-vm-stop-metrics" "Stop metrics VM";
          srt-vm-stop-xtransmit-pub = mkApp "${vmScripts.stop.xtransmit-pub}/bin/srt-vm-stop-xtransmit-pub" "Stop xtransmit publisher VM";
          srt-vm-stop-xtransmit-sub = mkApp "${vmScripts.stop.xtransmit-sub}/bin/srt-vm-stop-xtransmit-sub" "Stop xtransmit subscriber VM";
          srt-vm-stop-ffmpeg-pub = mkApp "${vmScripts.stop.ffmpeg-pub}/bin/srt-vm-stop-ffmpeg-pub" "Stop ffmpeg publisher VM";
          srt-vm-stop-ffmpeg-sub = mkApp "${vmScripts.stop.ffmpeg-sub}/bin/srt-vm-stop-ffmpeg-sub" "Stop ffmpeg subscriber VM";

          # Phase 7: Per-role status apps
          srt-vm-status-server = mkApp "${vmScripts.status.server}/bin/srt-vm-status-server" "Check server VM status";
          srt-vm-status-publisher = mkApp "${vmScripts.status.publisher}/bin/srt-vm-status-publisher" "Check publisher VM status";
          srt-vm-status-subscriber = mkApp "${vmScripts.status.subscriber}/bin/srt-vm-status-subscriber" "Check subscriber VM status";
          srt-vm-status-metrics = mkApp "${vmScripts.status.metrics}/bin/srt-vm-status-metrics" "Check metrics VM status";
          srt-vm-status-xtransmit-pub = mkApp "${vmScripts.status.xtransmit-pub}/bin/srt-vm-status-xtransmit-pub" "Check xtransmit publisher status";
          srt-vm-status-xtransmit-sub = mkApp "${vmScripts.status.xtransmit-sub}/bin/srt-vm-status-xtransmit-sub" "Check xtransmit subscriber status";
          srt-vm-status-ffmpeg-pub = mkApp "${vmScripts.status.ffmpeg-pub}/bin/srt-vm-status-ffmpeg-pub" "Check ffmpeg publisher status";
          srt-vm-status-ffmpeg-sub = mkApp "${vmScripts.status.ffmpeg-sub}/bin/srt-vm-status-ffmpeg-sub" "Check ffmpeg subscriber status";

          # Phase 8: Testing Infrastructure apps
          srt-test-runner = mkApp "${testing.testRunner}/bin/srt-test-runner" "Run SRT test suite";
          srt-test-run-tier = mkApp "${testing.testRunTier}/bin/srt-run-tier" "Run tests by tier";
          srt-start-all = mkApp "${testing.testStartAll}/bin/srt-start-all" "Start all test services";
          srt-wait-for-service = mkApp "${testing.testWaitService}/bin/srt-wait-for-service" "Wait for service to be ready";
          srt-extract-metrics = mkApp "${testing.extractMetricsScript}/bin/srt-extract-metrics" "Extract Prometheus metrics";
          srt-generate-report = mkApp "${testing.generateReportScript}/bin/srt-generate-report" "Generate test report";
          srt-compare-runs = mkApp "${testing.compareRunsScript}/bin/srt-compare-runs" "Compare test runs";
          srt-check-pass = mkApp "${testing.checkPassScript}/bin/srt-check-pass" "Check if test passed";

          # Phase 10: Integration Test apps
          srt-integration-basic = mkApp "${testing.integrationBasic}/bin/srt-integration-basic" "Basic integration test";
          srt-integration-latency = mkApp "${testing.integrationLatency}/bin/srt-integration-latency" "Latency integration test";
          srt-integration-loss = mkApp "${testing.integrationLoss}/bin/srt-integration-loss" "Loss integration test";
          srt-integration-full = mkApp "${testing.integrationFull}/bin/srt-integration-full" "Full integration test suite";
          srt-integration-smoke = mkApp "${testing.integrationSmoke}/bin/srt-integration-smoke" "Smoke test";
        };
      }
    ) // {
      # ─── Flake-wide exports (not per-system) ─────────────────────────────
      overlays.default = gosrtOverlay;

      # Library export (system-independent)
      # Access via: nix eval .#lib.serverIp
      lib = import ./nix/lib.nix { lib = nixpkgs.lib; };

      # NixOS modules for integration
      nixosModules = {
        srt-test = import ./nix/modules/srt-test.nix;
        srt-network = import ./nix/modules/srt-network.nix;
        srt-network-interfaces = import ./nix/modules/srt-network-interfaces.nix;
      };
    };
}
