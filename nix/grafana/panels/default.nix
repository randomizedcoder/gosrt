# nix/grafana/panels/default.nix
#
# Export all panel modules for dashboard composition.
#
# Reference: documentation/nix_microvm_implementation_plan.md Step 2.3
#
{ lib }:

let
  grafanaLib = import ../lib.nix { inherit lib; };

in {
  overview = import ./overview.nix { inherit lib grafanaLib; };
  heatmaps = import ./heatmaps.nix { inherit lib grafanaLib; };
  health = import ./health.nix { inherit lib grafanaLib; };
  congestion = import ./congestion.nix { inherit lib grafanaLib; };
  network = import ./network.nix { inherit lib grafanaLib; };

  # Re-export grafanaLib for convenience
  inherit grafanaLib;
}
