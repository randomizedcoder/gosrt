# nix/microvms/metrics.nix
#
# Metrics VM with Prometheus and Grafana.
# Deployed FIRST per Observer Pattern - you can't debug what you can't see.
#
# Reference: documentation/nix_microvm_design.md lines 1960-2121
# Implementation: documentation/nix_microvm_implementation_plan.md Step 2.4
#
{ pkgs, lib, microvm, nixpkgs, system }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  scrapeConfigs = import ../prometheus/scrape-configs.nix { inherit lib; };
  dashboards = import ../grafana/dashboards/default.nix { inherit lib; };

  # Metrics role config
  metricsRole = gosrtLib.roles.metrics;

  # ⚠️ INSECURE: Test credentials - DO NOT USE IN PRODUCTION!
  grafanaPassword = "srt";

  # ─── Node Exporter Full dashboard ─────────────────────────────────────
  # Popular Prometheus dashboard for system metrics (Grafana ID: 1860)
  # Source: https://github.com/rfmoz/grafana-dashboards
  nodeExporterDashboard = pkgs.fetchurl {
    url = "https://raw.githubusercontent.com/rfmoz/grafana-dashboards/741b1b3878d920439e413c7a7a3ff9cfa8ab2a20/prometheus/node-exporter-full.json";
    sha256 = "1x6r6vrif259zjjzh8m1cdhxr7hnr57ija76vgipyaryh8pyrv33";
  };

  # Directory structure for community dashboards
  communityDashboards = pkgs.linkFarm "community-dashboards" [
    { name = "node-exporter-full.json"; path = nodeExporterDashboard; }
  ];

in {
  # Export the VM runner
  vm = (nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [
      microvm.nixosModules.microvm
      ({ config, pkgs, ... }: {
        system.stateVersion = "24.05";
        networking.hostName = "srt-metrics";

        # ─── MicroVM Configuration ──────────────────────────────────────────────
        microvm = {
          hypervisor = "qemu";
          mem = gosrtLib.vm.memoryMB;
          vcpu = gosrtLib.vm.vcpus;
          interfaces = [{
            type = "tap";
            id = metricsRole.network.tap;
            mac = metricsRole.network.mac;
            # Multi-queue TAP is auto-enabled when vcpu > 1
          }];
          # Persistent storage for Prometheus data
          volumes = [{
            image = "prometheus-data.img";
            mountPoint = "/var/lib/prometheus2";
            size = 10240;  # 10GB
          }];

          # Serial console and naming (for vm-check compatibility)
          qemu.extraArgs = [
            "-name" "gosrt:${metricsRole.shortName},process=gosrt:${metricsRole.shortName}"
          ];
        };

        # ─── Network Configuration ──────────────────────────────────────────────
        systemd.network = {
          enable = true;
          networks."10-vm" = {
            matchConfig.Name = "eth*";
            networkConfig = {
              DHCP = "no";
              Address = "${metricsRole.network.vmIp}/24";
              Gateway = metricsRole.network.gateway;
            };
          };
        };
        networking.useNetworkd = true;
        networking.useDHCP = false;
        networking.firewall.enable = false;

        # ─── Prometheus ─────────────────────────────────────────────────────────
        services.prometheus = {
          enable = true;
          port = gosrtLib.ports.prometheusServer;
          retentionTime = "7d";

          # Scrape configs generated from role definitions
          scrapeConfigs = [
            scrapeConfigs.gosrt
            scrapeConfigs.node
            scrapeConfigs.prometheus
          ];

          # Recording rules for common calculations
          rules = [
            ''
              groups:
                - name: gosrt_recording
                  interval: 5s
                  rules:
                    - record: gosrt:loss_percent:rate30s
                      expr: |
                        100 * rate(gosrt_connection_congestion_packets_lost_total[30s]) /
                        (rate(gosrt_connection_congestion_packets_total[30s]) + 0.001)

                    - record: gosrt:throughput_mbps:instant
                      expr: |
                        gosrt_send_rate_sent_bandwidth_bps * 8 / 1000000

                    - record: gosrt:rtt_ms:instant
                      expr: |
                        gosrt_rtt_microseconds / 1000
            ''
          ];
        };

        # ─── Grafana ────────────────────────────────────────────────────────────
        services.grafana = {
          enable = true;

          settings = {
            server = {
              http_addr = "0.0.0.0";
              http_port = gosrtLib.ports.grafana;
            };
            security = {
              admin_user = "admin";
              admin_password = grafanaPassword;
            };
            # Disable login for easier testing (already have password)
            "auth.anonymous" = {
              enabled = true;
              org_role = "Viewer";
            };
          };

          # ─── Datasources ────────────────────────────────────────────────────────
          provision = {
            datasources.settings.datasources = [
              {
                name = "Prometheus";
                type = "prometheus";
                access = "proxy";
                url = "http://localhost:${toString gosrtLib.ports.prometheusServer}";
                isDefault = true;
                uid = "prometheus";
                jsonData = {
                  timeInterval = "1s";
                  httpMethod = "POST";
                };
              }
              # Annotation datasource for impairment events
              {
                name = "Annotations";
                type = "prometheus";
                access = "proxy";
                url = "http://localhost:${toString gosrtLib.ports.prometheusServer}";
                uid = "annotations";
                jsonData = {
                  httpMethod = "POST";
                };
              }
            ];

            # ─── Dashboard-as-Code ──────────────────────────────────────────────────
            dashboards.settings.providers = [
              {
                name = "GoSRT Dashboards";
                type = "file";
                folder = "GoSRT";
                options.path = "/etc/grafana/dashboards";
                disableDeletion = true;
                updateIntervalSeconds = 10;
              }
              {
                name = "Node Exporter";
                type = "file";
                folder = "System";
                options.path = communityDashboards;
                disableDeletion = true;
              }
            ];
          };
        };

        # Write dashboards to /etc/grafana/dashboards
        environment.etc."grafana/dashboards/gosrt-ops.json".text =
          builtins.toJSON dashboards.operations;
        environment.etc."grafana/dashboards/gosrt-analysis.json".text =
          builtins.toJSON dashboards.analysis;
        environment.etc."grafana/dashboards/gosrt-network.json".text =
          builtins.toJSON dashboards.network;

        # ─── Node Exporter (for system metrics) ─────────────────────────────────
        services.prometheus.exporters.node = {
          enable = true;
          port = 9100;
          enabledCollectors = [ "cpu" "diskstats" "filesystem" "loadavg" "meminfo" "netdev" "stat" "time" "vmstat" ];
        };

        # ─── SSH for debugging ──────────────────────────────────────────────────
        services.openssh = {
          enable = true;
          settings = {
            PermitRootLogin = "yes";
            PasswordAuthentication = true;
          };
        };
        users.users.root.initialPassword = "test";

        # ─── Useful packages ────────────────────────────────────────────────────
        environment.systemPackages = with pkgs; [
          curl
          jq
          htop
          iproute2
        ];
      })
    ];
  }).config.microvm.declaredRunner;

  # Export scrape configs for external use
  inherit scrapeConfigs dashboards;
}
