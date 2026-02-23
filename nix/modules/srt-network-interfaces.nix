# nix/modules/srt-network-interfaces.nix
#
# NixOS module for explicit interface naming via systemd.network.links.
#
# Reference: documentation/nix_microvm_implementation_plan.md Refinement 8
#
# This module solves the problem of non-deterministic interface names
# (e.g., eth0, ens33, enp0s3) by enforcing predictable names based on MAC address.
#
# Usage in VM config:
#   services.srt-interfaces = {
#     enable = true;
#     role = "server";
#   };
#
# The interface will be named "eth-srt" regardless of kernel enumeration order.
#
{ config, lib, pkgs, gosrtLib ? null, ... }:

with lib;

let
  cfg = config.services.srt-interfaces;

  # Import gosrtLib if not passed as argument
  # This allows the module to be used standalone or with specialArgs
  actualGosrtLib = if gosrtLib != null then gosrtLib else
    import ../lib.nix { inherit lib; };

in {
  # ─── Module Options ──────────────────────────────────────────────────────
  options.services.srt-interfaces = {
    enable = mkEnableOption "SRT explicit interface naming";

    role = mkOption {
      type = types.str;
      description = "Role name from gosrtLib.roles (e.g., 'server', 'publisher')";
      example = "server";
    };

    interfaceName = mkOption {
      type = types.str;
      default = "eth-srt";
      description = "Name to assign to the SRT interface";
    };

    mtu = mkOption {
      type = types.int;
      default = 1500;
      description = "MTU for the interface (use 9000 for jumbo frames if supported)";
    };
  };

  # ─── Module Config ───────────────────────────────────────────────────────
  config = mkIf cfg.enable (let
    roleConfig = actualGosrtLib.roles.${cfg.role};
  in {
    # ─── Assertions ────────────────────────────────────────────────────────
    assertions = [
      {
        assertion = actualGosrtLib.roles ? ${cfg.role};
        message = "Role '${cfg.role}' not found in gosrtLib.roles. Available: ${concatStringsSep ", " actualGosrtLib.roleNames}";
      }
    ];

    # ─── systemd-networkd Configuration ────────────────────────────────────
    systemd.network = {
      enable = true;

      # ─── Link rules: Match by MAC, assign predictable name ───────────────
      links = {
        "10-${cfg.interfaceName}" = {
          matchConfig = {
            MACAddress = roleConfig.network.mac;
          };
          linkConfig = {
            Name = cfg.interfaceName;
            MTUBytes = toString cfg.mtu;
          };
        };
      };

      # ─── Network rules: Configure IP based on role ───────────────────────
      networks = {
        "20-${cfg.interfaceName}" = {
          matchConfig = {
            Name = cfg.interfaceName;
          };
          networkConfig = {
            DHCP = "no";
            Address = "${roleConfig.network.vmIp}/24";
            Gateway = roleConfig.network.gateway;
          };
          # Enable IP forwarding for router functionality
          networkConfig.IPForward = mkDefault false;
        };
      };
    };

    # ─── Networking defaults ───────────────────────────────────────────────
    # Use systemd-networkd instead of scripted networking
    networking = {
      useDHCP = false;
      useNetworkd = true;

      # Set hostname based on role
      hostName = mkDefault "srt-${cfg.role}";
    };

    # ─── Wait for network to be online ─────────────────────────────────────
    systemd.services.systemd-networkd-wait-online = {
      serviceConfig = {
        # Only wait for the SRT interface, not all interfaces
        ExecStart = [
          ""  # Clear default
          "${pkgs.systemd}/lib/systemd/systemd-networkd-wait-online --interface=${cfg.interfaceName}"
        ];
      };
    };
  });
}
