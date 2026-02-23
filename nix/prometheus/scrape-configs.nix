# nix/prometheus/scrape-configs.nix
#
# Data-driven Prometheus scrape config generation.
# Auto-generates targets from role definitions in constants.nix.
#
# Reference: documentation/nix_microvm_implementation_plan.md Step 2.1
# Design: documentation/nix_microvm_design.md lines 1890-1957
#
{ lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # Filter roles that have Prometheus endpoints
  gosrtInstances = lib.filterAttrs
    (_: r: r.service.hasPrometheus or false)
    gosrtLib.roles;

  # All roles (for node_exporter metrics)
  allRoles = gosrtLib.roles;

  # Generate relabel configs to set instance label from IP
  mkRelabelConfigs = instances: lib.mapAttrsToList (name: role: {
    source_labels = [ "__address__" ];
    regex = "${role.network.vmIp}:.*";
    target_label = "instance";
    replacement = name;
  }) instances;

  # GoSRT application metrics (1s scrape for real-time dashboards)
  gosrt = {
    job_name = "gosrt";
    scrape_interval = "1s";
    scrape_timeout = "1s";
    static_configs = [{
      targets = lib.mapAttrsToList
        (_: r: "${r.network.vmIp}:${toString gosrtLib.ports.prometheus}")
        gosrtInstances;
    }];
    relabel_configs = mkRelabelConfigs gosrtInstances;
  };

  # Node exporter metrics (5s scrape for system metrics)
  # Port 9100 is the standard node_exporter port
  node = {
    job_name = "node";
    scrape_interval = "5s";
    static_configs = [{
      targets = lib.mapAttrsToList
        (_: r: "${r.network.vmIp}:${toString gosrtLib.ports.nodeExporter}")
        allRoles;
    }];
    relabel_configs = mkRelabelConfigs allRoles;
  };

  # Prometheus self-monitoring
  prometheus = {
    job_name = "prometheus";
    scrape_interval = "15s";
    static_configs = [{
      targets = [ "localhost:${toString gosrtLib.ports.prometheusServer}" ];
    }];
  };

in {
  inherit gosrt node prometheus;

  # All scrape configs combined (for services.prometheus.scrapeConfigs)
  all = [ gosrt node prometheus ];

  # Export helpers for external use
  inherit mkRelabelConfigs gosrtInstances;
}
