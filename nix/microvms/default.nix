# nix/microvms/default.nix
#
# Data-driven MicroVM generator.
# Creates all 8 VMs from role definitions in constants.nix.
#
# Reference: documentation/nix_microvm_design.md lines 1195-1239
#
# This single file replaces 7 individual VM files!
# Each VM is generated from its role definition.
#
{ pkgs, lib, microvm, nixpkgs, system, gosrtPackage, gosrtPackageDebug ? gosrtPackage,
  srtXtransmitPackage ? null, ffmpegPackage ? null }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  baseMicroVM = import ./base.nix { inherit pkgs lib microvm nixpkgs system; };

  # Import metrics VM separately (it has special config)
  metricsVM = import ./metrics.nix { inherit pkgs lib microvm nixpkgs system; };

  # Map role.package field to actual package
  getPackageForRole = role:
    if role.package == "server" || role.package == "client" || role.package == "client-generator" then
      gosrtPackage
    else if role.package == "srt-xtransmit" then
      if srtXtransmitPackage != null then srtXtransmitPackage
      else throw "srt-xtransmit package required for ${role.shortName} VM"
    else if role.package == "ffmpeg-full" then
      if ffmpegPackage != null then ffmpegPackage
      else throw "ffmpeg package required for ${role.shortName} VM"
    else
      gosrtPackage;  # Default to gosrt

  getPackageForRoleDebug = role:
    if role.package == "server" || role.package == "client" || role.package == "client-generator" then
      gosrtPackageDebug
    else
      getPackageForRole role;  # Non-gosrt packages don't have debug variants

  # ─── VM Generator ─────────────────────────────────────────────────────────────
  # Generate VM for each role
  mkRoleVM = name: role:
    if name == "metrics" then
      # Metrics VM has special configuration (Prometheus + Grafana)
      metricsVM.vm
    else if role.service == null then
      # Role without service (shouldn't happen except metrics)
      null
    else
      # Standard VM with appropriate package
      baseMicroVM.mkMicroVM {
        inherit role;
        gosrtPackage = getPackageForRole role;
      };

  # Generate debug variant VMs
  mkRoleVMDebug = name: role:
    if name == "metrics" then
      metricsVM.vm  # Metrics doesn't have debug variant
    else if role.service == null then
      null
    else
      baseMicroVM.mkMicroVM {
        inherit role;
        gosrtPackage = getPackageForRoleDebug role;
        buildVariant = "debug";
      };

  # Filter out null entries (roles without VMs)
  filterNull = lib.filterAttrs (_: v: v != null);

  # Generate all VMs
  vms = filterNull (lib.mapAttrs mkRoleVM gosrtLib.roles);
  vmsDebug = filterNull (lib.mapAttrs mkRoleVMDebug gosrtLib.roles);

in {
  # ─── VM Exports ───────────────────────────────────────────────────────────────

  # Production VMs (optimized builds)
  inherit vms;

  # Debug VMs (with context assertions)
  debug = vmsDebug;

  # Individual VM access (convenience)
  server = vms.server;
  publisher = vms.publisher;
  subscriber = vms.subscriber;
  xtransmit-pub = vms.xtransmit-pub or null;
  xtransmit-sub = vms.xtransmit-sub or null;
  ffmpeg-pub = vms.ffmpeg-pub or null;
  ffmpeg-sub = vms.ffmpeg-sub or null;
  metrics = vms.metrics;

  # All VM names
  vmNames = lib.attrNames vms;

  # ─── Helper Functions ─────────────────────────────────────────────────────────

  # Get VM by role name
  getVM = name: vms.${name} or (throw "Unknown VM: ${name}");
  getVMDebug = name: vmsDebug.${name} or (throw "Unknown debug VM: ${name}");

  # Re-export for convenience
  inherit gosrtLib baseMicroVM metricsVM;
}
