# nix/grafana/dashboards/default.nix
#
# Export all dashboard modules.
#
{ lib }:

{
  operations = import ./operations.nix { inherit lib; };
  analysis = import ./analysis.nix { inherit lib; };
  network = import ./network.nix { inherit lib; };
}
