# nix/overlays/gosrt.nix
#
# GoSRT package overlay - defines binary flavors (prod, debug, perf).
#
# Reference: documentation/nix_microvm_implementation_plan.md Refinement 1
#
# This overlay centralizes:
#   - GOEXPERIMENT flags (propagate to all VMs)
#   - ldflags for stripping/debug symbols
#   - Build tags for debug assertions
#
# Usage in flake.nix:
#   overlays = [ (import ./nix/overlays/gosrt.nix) ];
#   packages = { inherit (pkgs.gosrt) prod debug perf; };
#
final: prev: {
  gosrt = rec {
    # Common build function for all variants
    mkGosrt = {
      variant,
      ldflags ? [ "-s" "-w" ],
      tags ? [],
      extraArgs ? [],
      enablePprof ? false
    }: let
      # Determine subPackages based on variant
      subPackages = [ "contrib/server" "contrib/client" "contrib/client-generator" ];
    # Use Go 1.26 explicitly
    in final.buildGo126Module {
      pname = "gosrt-${variant}";
      version = "0.1.0";

      # Source is the gosrt repository root
      src = final.lib.cleanSource ../..;

      # Vendor hash - update after first build attempt
      # The build will fail and print the correct hash
      vendorHash = null;  # null = use vendor directory if exists, or calculate

      inherit subPackages;

      # Go 1.26 experimental features and CGO disabled
      preBuild = ''
        export GOEXPERIMENT=jsonv2
        export CGO_ENABLED=0
      '';

      # Build flags
      inherit ldflags tags;

      # Post-install wrapper for pprof-enabled builds
      nativeBuildInputs = final.lib.optionals (extraArgs != []) [ final.makeWrapper ];

      postInstall = final.lib.optionalString (extraArgs != []) ''
        for bin in $out/bin/*; do
          wrapProgram "$bin" --add-flags "${final.lib.concatStringsSep " " extraArgs}"
        done
      '';

      meta = with final.lib; {
        description = "GoSRT - Pure Go SRT implementation (${variant} build)";
        homepage = "https://github.com/your-org/gosrt";
        license = licenses.mit;
        platforms = platforms.linux;
      };
    };

    # ─── Flavor Definitions ──────────────────────────────────────────────────

    # Production: optimized, no debug symbols
    prod = mkGosrt {
      variant = "prod";
      ldflags = [ "-s" "-w" ];  # Strip debug info
    };

    # Debug: with context assertions (AssertEventLoopContext)
    # Use this for Phase 1 testing to catch lock-free violations
    debug = mkGosrt {
      variant = "debug";
      ldflags = [ ];  # Keep debug symbols
      tags = [ "debug" ];  # Enable debug assertions
    };

    # Performance: with pprof endpoints enabled
    perf = mkGosrt {
      variant = "perf";
      ldflags = [ "-s" "-w" ];
      extraArgs = [ "-pprof" ":6060" ];
      enablePprof = true;
    };
  };
}
