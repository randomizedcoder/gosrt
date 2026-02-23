# nix/modules/srt-test.nix
#
# NixOS module for declarative network impairment scenarios.
#
# Reference: documentation/nix_microvm_implementation_plan.md Refinement 2
#
# Usage in VM config:
#   services.srt-test = {
#     enable = true;
#     scenario = "starlink-handoff";
#   };
#
# This replaces imperative tc/netem shell scripts with declarative NixOS options.
#
{ config, lib, pkgs, gosrtLib ? null, ... }:

with lib;

let
  cfg = config.services.srt-test;

  # Import lib if not passed via specialArgs
  libModule = if gosrtLib != null then gosrtLib else import ../lib.nix { inherit lib; };
  serverIp = libModule.serverIp;

  # Import constants for Starlink config
  c = import ../constants.nix;

  # ─── Scenario Definitions ────────────────────────────────────────────────
  # Each scenario defines loss, delay, and jitter parameters
  scenarios = {
    clean = {
      loss = 0;
      delay = 0;
      jitter = 0;
      description = "No impairment - baseline performance";
    };

    regional = {
      loss = 0;
      delay = 5;
      jitter = 2;
      description = "Regional datacenter ~10ms RTT";
    };

    continental = {
      loss = 0;
      delay = 30;
      jitter = 5;
      description = "Cross-continent ~60ms RTT";
    };

    intercontinental = {
      loss = 0;
      delay = 65;
      jitter = 10;
      description = "Intercontinental ~130ms RTT";
    };

    geo-satellite = {
      loss = 0.5;
      delay = 150;
      jitter = 20;
      description = "GEO satellite ~300ms RTT, 0.5% loss";
    };

    congested-wifi = {
      loss = 2;
      delay = 5;
      jitter = 10;
      description = "Congested WiFi 2% loss";
    };

    starlink-handoff = {
      loss = 0;
      delay = c.starlink.baselineDelayMs;
      jitter = c.starlink.baselineJitterMs;
      description = "Starlink satellite handoff - ${toString c.starlink.blackoutMs}ms blackouts";
      blackholePattern = {
        enable = true;
        times = c.starlink.minuteTimes;
        durationMs = c.starlink.blackoutMs;
      };
    };

    stress-5pct = {
      loss = 5;
      delay = 10;
      jitter = 5;
      description = "5% packet loss stress test";
    };
  };

in {
  # ─── Module Options ──────────────────────────────────────────────────────
  options.services.srt-test = {
    enable = mkEnableOption "SRT test scenario impairment";

    scenario = mkOption {
      type = types.enum (attrNames scenarios);
      default = "clean";
      description = "Network impairment scenario to apply";
    };

    interface = mkOption {
      type = types.str;
      default = "eth-srt";
      description = "Interface to apply impairment (should match srt-network-interfaces)";
    };

    grafanaUrl = mkOption {
      type = types.str;
      default = "http://10.50.8.2:3000";
      description = "Grafana URL for annotation API";
    };
  };

  # ─── Module Config ───────────────────────────────────────────────────────
  config = mkIf cfg.enable (let
    s = scenarios.${cfg.scenario};
    hasNetem = s.delay > 0 || s.loss > 0 || s.jitter > 0;
    hasBlackhole = s.blackholePattern.enable or false;
  in {
    # ─── Netem Impairment Service ──────────────────────────────────────────
    systemd.services.srt-impairment = mkIf hasNetem {
      description = "Apply SRT test impairment: ${cfg.scenario}";
      wantedBy = [ "network-online.target" ];
      after = [ "network-online.target" ];

      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "apply-impairment-${cfg.scenario}" ''
          set -euo pipefail

          # Remove any existing qdisc
          ${pkgs.iproute2}/bin/tc qdisc del dev ${cfg.interface} root 2>/dev/null || true

          # Apply netem with scenario parameters
          ${pkgs.iproute2}/bin/tc qdisc add dev ${cfg.interface} root netem \
            ${optionalString (s.delay > 0) "delay ${toString s.delay}ms"} \
            ${optionalString (s.jitter > 0) "${toString s.jitter}ms"} \
            ${optionalString (s.loss > 0) "loss ${toString s.loss}%"} \
            limit 50000

          echo "Applied scenario: ${cfg.scenario}"
          echo "  Delay: ${toString s.delay}ms +/- ${toString s.jitter}ms"
          echo "  Loss: ${toString s.loss}%"

          # Create Grafana annotation
          ${pkgs.curl}/bin/curl -s -X POST \
            -u admin:srt \
            -H "Content-Type: application/json" \
            -d '{"text":"Impairment: ${cfg.scenario} started","tags":["impairment","${cfg.scenario}","start"]}' \
            ${cfg.grafanaUrl}/api/annotations || true
        '';

        ExecStop = pkgs.writeShellScript "cleanup-impairment-${cfg.scenario}" ''
          set -euo pipefail

          # Remove netem qdisc
          ${pkgs.iproute2}/bin/tc qdisc del dev ${cfg.interface} root 2>/dev/null || true

          echo "Cleaned up scenario: ${cfg.scenario}"

          # Create Grafana annotation
          ${pkgs.curl}/bin/curl -s -X POST \
            -u admin:srt \
            -H "Content-Type: application/json" \
            -d '{"text":"Impairment: ${cfg.scenario} stopped","tags":["impairment","${cfg.scenario}","stop"]}' \
            ${cfg.grafanaUrl}/api/annotations || true
        '';
      };
    };

    # ─── Blackhole Pattern Service (for Starlink simulation) ───────────────
    systemd.services.srt-blackhole-pattern = mkIf hasBlackhole {
      description = "Starlink-style blackhole pattern for ${cfg.scenario}";
      wantedBy = [ "multi-user.target" ];
      after = [ "srt-impairment.service" "network-online.target" ];
      requires = [ "network-online.target" ];

      serviceConfig = {
        Type = "simple";
        Restart = "always";
        RestartSec = 1;
        ExecStart = pkgs.writeShellScript "blackhole-pattern-${cfg.scenario}" ''
          set -euo pipefail

          TIMES="${concatStringsSep " " (map toString s.blackholePattern.times)}"
          DURATION_MS=${toString s.blackholePattern.durationMs}

          echo "Starting blackhole pattern: times=$TIMES, duration=$DURATION_MS ms"

          while true; do
            CURRENT_SECOND=$(date +%S | sed 's/^0//')  # Remove leading zero

            for T in $TIMES; do
              if [ "$CURRENT_SECOND" = "$T" ]; then
                # Add blackhole via nftables (uses srt-network module)
                # Note: blackhole-addrs is an ipv4_addr set, so we add the server IP
                ${pkgs.nftables}/bin/nft add element inet srt-test blackhole-addrs \
                  { ${serverIp} timeout ''${DURATION_MS}ms } 2>/dev/null || true

                # Grafana annotation for blackhole event
                ${pkgs.curl}/bin/curl -s -X POST \
                  -u admin:srt \
                  -H "Content-Type: application/json" \
                  -d '{"text":"Blackhole: ${toString s.blackholePattern.durationMs}ms","tags":["blackhole","${cfg.scenario}"]}' \
                  ${cfg.grafanaUrl}/api/annotations || true

                echo "Blackhole triggered at second $T for ''${DURATION_MS}ms"
              fi
            done

            sleep 0.5
          done
        '';
      };
    };

    # ─── Cleanup on shutdown ───────────────────────────────────────────────
    systemd.services.srt-impairment-cleanup = {
      description = "Cleanup SRT impairments on shutdown";
      wantedBy = [ "shutdown.target" ];
      before = [ "shutdown.target" ];

      serviceConfig = {
        Type = "oneshot";
        ExecStart = pkgs.writeShellScript "cleanup-all-impairments" ''
          ${pkgs.iproute2}/bin/tc qdisc del dev ${cfg.interface} root 2>/dev/null || true
          ${pkgs.nftables}/bin/nft flush set inet srt-test blackhole-addrs 2>/dev/null || true
          echo "All impairments cleaned up"
        '';
      };
    };
  });
}
