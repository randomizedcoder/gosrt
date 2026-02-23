# nix/microvms/base.nix
#
# MicroVM builder function for GoSRT VMs.
# Creates VMs from role definitions in constants.nix.
#
# Reference: documentation/nix_microvm_design.md lines 948-1192
#
# Usage:
#   baseMicroVM.mkMicroVM {
#     role = gosrtLib.roles.server;
#     gosrtPackage = pkgs.gosrt.debug;
#   }
#
{ pkgs, lib, microvm, nixpkgs, system }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # ─── MicroVM Builder Function ─────────────────────────────────────────────────
  mkMicroVM = {
    role,
    gosrtPackage,
    extraPackages ? [],
    buildVariant ? "debug"
  }:
  let
    name = role.shortName;
    net = role.network;
    svc = role.service;

    # Generate ExecStart command from service config
    execStart = gosrtLib.mkExecStart role gosrtPackage;

    # Generate environment variables
    environment = gosrtLib.mkEnvironment role;

    # Service module (only for roles with service config)
    serviceModule = lib.optionalAttrs (svc != null) {
      systemd.services."gosrt-${name}" = {
        description = "GoSRT ${role.description}";
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];

        environment = lib.listToAttrs (map (e: let
          parts = lib.splitString "=" e;
        in {
          name = lib.head parts;
          value = lib.concatStringsSep "=" (lib.tail parts);
        }) environment);

        serviceConfig = {
          Type = "simple";
          ExecStart = execStart;
          Restart = "always";
          RestartSec = "5";

          # Security hardening
          NoNewPrivileges = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = true;
        };
      };
    };

  in (nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [
      microvm.nixosModules.microvm
      ({ config, pkgs, ... }: {
        system.stateVersion = "24.05";
        networking.hostName = "srt-${name}";

        # ─── MicroVM Configuration ────────────────────────────────────────────
        microvm = {
          hypervisor = "qemu";
          mem = gosrtLib.vm.memoryMB;
          vcpu = gosrtLib.vm.vcpus;

          interfaces = [{
            type = "tap";
            id = net.tap;
            mac = net.mac;
            # Multi-queue TAP is auto-enabled when vcpu > 1
            # (queues = vcpu, mq=on set by microvm's qemu runner)
          }];

          # Serial console on TCP socket for debugging
          qemu.extraArgs = [
            "-name" "gosrt:${name},process=gosrt:${name}"
            "-chardev" "socket,id=tcpcon,host=localhost,port=${toString role.ports.console},server=on,wait=off"
            "-serial" "chardev:tcpcon"
          ];
        };

        # ─── Network Configuration ────────────────────────────────────────────
        systemd.network = {
          enable = true;
          networks."10-vm" = {
            matchConfig.Name = "eth*";
            networkConfig = {
              DHCP = "no";
              Address = "${net.vmIp}/24";
              Gateway = net.gateway;
            };
          };
        };
        networking.useNetworkd = true;
        networking.useDHCP = false;
        networking.firewall.enable = false;

        # ─── Kernel Tuning ────────────────────────────────────────────────────
        boot.kernel.sysctl = {
          # Network buffer sizes for high throughput
          "net.core.rmem_max" = 26214400;
          "net.core.wmem_max" = 26214400;
          "net.core.rmem_default" = 1048576;
          "net.core.wmem_default" = 1048576;
          "net.ipv4.udp_rmem_min" = 8192;
          "net.ipv4.udp_wmem_min" = 8192;

          # Increase netdev budget for high packet rates
          "net.core.netdev_budget" = 600;
          "net.core.netdev_budget_usecs" = 8000;
        };

        # ─── Node Exporter ────────────────────────────────────────────────────
        # Standard port 9100 for node_exporter
        services.prometheus.exporters.node = {
          enable = true;
          port = 9100;
          enabledCollectors = [
            "cpu" "diskstats" "filesystem" "loadavg"
            "meminfo" "netdev" "stat" "time" "vmstat"
          ];
        };

        # ─── SSH for Debugging ────────────────────────────────────────────────
        services.openssh = {
          enable = true;
          settings = {
            PermitRootLogin = "yes";
            PasswordAuthentication = true;
          };
        };
        users.users.root.initialPassword = "srt";

        # ─── Packages ─────────────────────────────────────────────────────────
        environment.systemPackages = [
          gosrtPackage
          pkgs.curl
          pkgs.htop
          pkgs.iproute2
          pkgs.netcat-gnu
        ] ++ extraPackages;
      })

      # Service module (if role has a service)
      serviceModule
    ];
  }).config.microvm.declaredRunner;

in {
  inherit mkMicroVM;

  # Re-export gosrtLib for convenience
  inherit gosrtLib;
}
