# nix/modules/srt-network.nix
#
# NixOS module for declarative nftables networking.
#
# Reference: documentation/nix_microvm_implementation_plan.md Refinement 3
#
# This module provides:
#   - nftables ruleset for SRT testing
#   - Blackhole sets with timeout for Starlink simulation
#   - Helper scripts for blackhole management
#
# Usage in VM config:
#   services.srt-network.enable = true;
#
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.srt-network;
in {
  # ─── Module Options ──────────────────────────────────────────────────────
  options.services.srt-network = {
    enable = mkEnableOption "SRT declarative networking with nftables";

    blackholeTargets = mkOption {
      type = types.listOf types.str;
      default = [ ];
      description = "IP addresses to blackhole during pattern tests";
    };
  };

  # ─── Module Config ───────────────────────────────────────────────────────
  config = mkIf cfg.enable {
    # ─── nftables Configuration ────────────────────────────────────────────
    networking.nftables = {
      enable = true;
      ruleset = ''
        table inet srt-test {
          # ─── Blackhole set with auto-expiring entries ─────────────────────
          # Used by Starlink pattern simulation
          # Entries are added with timeout and auto-expire
          set blackhole-addrs {
            type ipv4_addr
            flags timeout
            # Entries added dynamically:
            #   nft add element inet srt-test blackhole-addrs { 10.50.1.2 timeout 500ms }
          }

          # ─── Forward chain - apply blackhole ──────────────────────────────
          chain forward {
            type filter hook forward priority 0; policy accept;

            # Drop packets to blackholed addresses
            ip daddr @blackhole-addrs drop
          }

          # ─── Input chain - allow management ───────────────────────────────
          chain input {
            type filter hook input priority 0; policy accept;

            # Allow established connections
            ct state established,related accept

            # Allow loopback
            iifname "lo" accept

            # Allow SSH for management
            tcp dport 22 accept

            # Allow Prometheus scraping
            tcp dport 9100 accept

            # Allow SRT traffic
            udp dport 6000 accept

            # Allow ICMP for debugging
            icmp type echo-request accept
          }
        }
      '';
    };

    # ─── Helper Scripts ────────────────────────────────────────────────────
    environment.systemPackages = [
      # Add blackhole entry with timeout
      (pkgs.writeShellScriptBin "srt-blackhole-add" ''
        set -euo pipefail
        if [ $# -lt 1 ]; then
          echo "Usage: srt-blackhole-add <ip> [timeout_ms]"
          echo "Example: srt-blackhole-add 10.50.1.2 500"
          exit 1
        fi
        IP="$1"
        TIMEOUT="''${2:-500}ms"
        ${pkgs.nftables}/bin/nft add element inet srt-test blackhole-addrs { "$IP" timeout "$TIMEOUT" }
        echo "Added blackhole for $IP with timeout $TIMEOUT"
      '')

      # Clear all blackhole entries
      (pkgs.writeShellScriptBin "srt-blackhole-clear" ''
        set -euo pipefail
        ${pkgs.nftables}/bin/nft flush set inet srt-test blackhole-addrs
        echo "Cleared all blackhole entries"
      '')

      # List current blackhole entries
      (pkgs.writeShellScriptBin "srt-blackhole-list" ''
        ${pkgs.nftables}/bin/nft list set inet srt-test blackhole-addrs
      '')

      # Show full ruleset
      (pkgs.writeShellScriptBin "srt-rules-show" ''
        ${pkgs.nftables}/bin/nft list ruleset
      '')
    ];

    # ─── Firewall Integration ──────────────────────────────────────────────
    # Disable iptables-based firewall (using nftables instead)
    networking.firewall.enable = false;
  };
}
