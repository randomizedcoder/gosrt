# nix/network/default.nix
#
# Export network infrastructure modules.
#
# Reference: documentation/nix_microvm_implementation_plan.md Phase 5
#
{ pkgs, lib }:

let
  setup = import ./setup.nix { inherit pkgs lib; };
  impairments = import ./impairments.nix { inherit pkgs lib; };
  profiles = import ./profiles.nix { inherit lib; };
  annotations = import ./impairment-annotations.nix { inherit pkgs lib; };

in {
  # Setup/teardown scripts
  inherit (setup) setupScript teardownScript;

  # Impairment scripts
  inherit (impairments) setLatencyScript setLossScript starlinkPatternScript;

  # Starlink reconvergence scripts (enhanced simulation)
  inherit (impairments) starlinkReconvergenceScript starlinkStartScript starlinkStopScript starlinkStatusScript;

  # Profiles and scenarios
  inherit profiles;

  # Annotation helpers
  inherit (annotations) mkAnnotationScript testAnnotation;
}
