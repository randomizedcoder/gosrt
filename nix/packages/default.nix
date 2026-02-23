# nix/packages/default.nix
#
# Export all packages for GoSRT MicroVM infrastructure.
#
# Reference: documentation/nix_microvm_implementation_plan.md Phase 3
#
{ pkgs, lib, src }:

let
  # GoSRT package builder
  mkGosrt = variant: import ./gosrt.nix {
    inherit pkgs lib src;
    buildVariant = variant;
    enableAudit = true;
  };

  # GoSRT without audit (for faster dev iteration)
  mkGosrtNoAudit = variant: import ./gosrt.nix {
    inherit pkgs lib src;
    buildVariant = variant;
    enableAudit = false;
  };

in {
  # ─── GoSRT Packages (with audits) ───────────────────────────────────────────

  # Production: optimized, no debug symbols
  gosrt-prod = mkGosrt "production";

  # Debug: with context assertions (AssertEventLoopContext)
  # Use for initial MicroVM testing to catch lock-free violations
  gosrt-debug = mkGosrt "debug";

  # Performance: with pprof endpoints
  gosrt-perf = mkGosrt "perf";

  # ─── GoSRT Packages (without audits - faster builds) ────────────────────────

  gosrt-prod-fast = mkGosrtNoAudit "production";
  gosrt-debug-fast = mkGosrtNoAudit "debug";

  # ─── Interop Tools ──────────────────────────────────────────────────────────

  # srt-xtransmit for interoperability testing
  # NOTE: Hash needs to be updated after first build attempt
  srt-xtransmit = import ./srt-xtransmit.nix { inherit pkgs lib; };

  # FFmpeg with SRT support
  ffmpeg-srt = import ./ffmpeg.nix { inherit pkgs lib; };

  # ─── Convenience Exports ────────────────────────────────────────────────────

  # All production binaries combined
  all = pkgs.symlinkJoin {
    name = "gosrt-all";
    paths = [ (mkGosrt "production") ];
  };

  # Debug binaries for initial testing
  all-debug = pkgs.symlinkJoin {
    name = "gosrt-all-debug";
    paths = [ (mkGosrt "debug") ];
  };
}
