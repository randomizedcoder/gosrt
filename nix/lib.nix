# nix/lib.nix
#
# Compute all derived values from role definitions.
# This eliminates hardcoded IPs, MACs, and ports - everything is derived
# from the role index in constants.nix.
#
# Reference: documentation/nix_microvm_design.md lines 524-690
#
# Type Documentation:
# -------------------
# Role (after computation):
#   { index: int, shortName: string, router: "A"|"B", description: string,
#     package: string, service: ServiceConfig, network: NetworkConfig,
#     ports: PortConfig, vmName: string, routerNamespace: string }
#
# NetworkConfig:
#   { tap: string, bridge: string, vethHost: string, vethRouter: string,
#     subnet: string, vmIp: string, gateway: string, mac: string }
#
# PortConfig:
#   { console: int, sshForward: int, prometheusForward: int }
#
# InterRouterLink:
#   { index: int, rttMs: int, name: string, subnetA: string, subnetB: string,
#     vethA: string, vethB: string, ipA: string, ipB: string }
#
{ lib }:

let
  constants = import ./constants.nix;
  c = constants;

  # ─── Validation Assertions ─────────────────────────────────────────────────
  # These run at evaluation time and fail the build if constraints are violated

  validateRole = name: role:
    assert lib.assertMsg (role ? index) "Role '${name}' missing required field 'index'";
    assert lib.assertMsg (role ? shortName) "Role '${name}' missing required field 'shortName'";
    assert lib.assertMsg (role ? router) "Role '${name}' missing required field 'router'";
    assert lib.assertMsg (role.router == "A" || role.router == "B")
      "Role '${name}' has invalid router '${role.router}' (must be A or B)";
    assert lib.assertMsg (role ? package) "Role '${name}' missing required field 'package'";
    assert lib.assertMsg (role.index >= 1 && role.index <= 254)
      "Role '${name}' has invalid index ${toString role.index} (must be 1-254)";
    role;

  # Validate all roles at evaluation time
  validatedRoles = lib.mapAttrs validateRole c.roles;

  # Ensure no index collisions
  indexList = lib.mapAttrsToList (_: r: r.index) validatedRoles;
  uniqueIndexes = lib.unique indexList;
  indexValidation = assert lib.assertMsg (builtins.length indexList == builtins.length uniqueIndexes)
    "Duplicate role indexes detected! Each role must have a unique index.";
    null;

  # ─── Derivation Functions ──────────────────────────────────────────────────

  # Helper: convert integer to 2-digit hex string
  # Note: lib.toHexString may not exist in older nixpkgs, so we define our own
  toHex2 = n: let
    hexChars = "0123456789abcdef";
    high = n / 16;
    low = n - (high * 16);
  in "${builtins.substring high 1 hexChars}${builtins.substring low 1 hexChars}";

  # Derive network config from role index
  mkRoleNetwork = name: role: {
    tap = "srttap-${role.shortName}";
    bridge = "srtbr-${role.shortName}";
    vethHost = "veth-${role.shortName}-h";
    vethRouter = "veth-${role.shortName}-r";
    subnet = "${c.base.subnetPrefix}.${toString role.index}.0/24";
    vmIp = "${c.base.subnetPrefix}.${toString role.index}.2";
    gateway = "${c.base.subnetPrefix}.${toString role.index}.1";
    # MAC format: 02:00:00:50:XX:02 where XX is hex of index
    mac = "02:00:00:50:${toHex2 role.index}:02";
  };

  # Derive ports from role index
  mkRolePorts = name: role: {
    console = c.base.consolePortBase + role.index;
    sshForward = c.base.sshPortBase + role.index;
    prometheusForward = c.base.prometheusPortBase + role.index;
  };

  # Derive inter-router link config
  mkInterRouterLink = profile: let
    subnet = "${c.base.subnetPrefix}.${toString (c.base.interRouterBase + profile.index)}";
  in {
    inherit (profile) index rttMs name;
    subnetA = subnet;
    subnetB = subnet;  # Same subnet, different IPs
    vethA = "link${toString profile.index}_a";
    vethB = "link${toString profile.index}_b";
    ipA = "${subnet}.1";
    ipB = "${subnet}.2";
  };

  # ─── Computed Attributes ───────────────────────────────────────────────────

  # Fully computed role configs (network + ports merged)
  # Uses validatedRoles to ensure all constraints are checked at evaluation time
  roles = lib.mapAttrs (name: role: role // {
    network = mkRoleNetwork name role;
    ports = mkRolePorts name role;
    vmName = "srt-${name}";
    routerNamespace = c.routers.${role.router}.namespace;
  }) validatedRoles;

  # Computed inter-router links
  interRouterLinks = map mkInterRouterLink c.latencyProfiles;

  # Helper: get server IP (commonly needed by other roles)
  serverIp = roles.server.network.vmIp;

  # Helper: list of all role names
  roleNames = builtins.attrNames roles;

  # Helper: roles on Router A
  routerARoles = lib.filterAttrs (_: r: r.router == "A") roles;

  # Helper: roles on Router B
  routerBRoles = lib.filterAttrs (_: r: r.router == "B") roles;

  # Helper: roles with Prometheus endpoints (for scrape config)
  prometheusRoles = lib.filterAttrs (_: r: r.service.hasPrometheus or false) roles;

  # ─── Script Generation Helpers ─────────────────────────────────────────────

  # ─── Bitrate Format Helpers ─────────────────────────────────────────────────
  # All derived from single source: c.test.publishBitrateBps
  bitrate = {
    bps = toString c.test.publishBitrateBps;                          # "10000000" (gosrt)
    kbps = "${toString (c.test.publishBitrateBps / 1000)}k";          # "10000k" (ffmpeg -b:v)
    mbps = "${toString (c.test.publishBitrateBps / 1000000)}Mbps";    # "10Mbps" (xtransmit)
  };

  # Generate ExecStart command from service config
  mkExecStart = role: pkg: let
    svc = role.service;
    # Replace placeholders in args
    # All bitrate formats derived from c.test.publishBitrateBps
    replaceVars = arg: builtins.replaceStrings
      [ "{vmIp}" "{serverIp}" "{bitrate}" "{bitrateMbps}" "{bitrateKbps}" "{promhttpPort}" ]
      [ role.network.vmIp serverIp bitrate.bps bitrate.mbps bitrate.kbps (toString c.ports.prometheus) ]
      arg;
    args = map replaceVars svc.args;
    cmd = if svc.command or null != null
          then "${pkg}/bin/${svc.binary} ${svc.command}"
          else "${pkg}/bin/${svc.binary}";
  in "${cmd} ${lib.concatStringsSep " " args}";

  # Generate environment from service config
  mkEnvironment = role:
    lib.mapAttrsToList (k: v: "${k}=${v}") (role.service.environment or {});

  # Router namespace shortcuts (commonly used in network scripts)
  routerA = c.routers.A.namespace;
  routerB = c.routers.B.namespace;

in {
  inherit roles interRouterLinks serverIp roleNames;
  inherit routerARoles routerBRoles prometheusRoles;
  inherit routerA routerB;  # Namespace shortcuts
  inherit mkExecStart mkEnvironment;
  inherit bitrate;  # Bitrate in multiple formats (bps, kbps, mbps)

  # Re-export constants for convenience
  inherit (constants) base ports vm netem test go routers latencyProfiles;

  # ─── Prometheus Scrape Config Generator ────────────────────────────────────
  mkScrapeTargets = roles': port:
    lib.mapAttrsToList (_: r: "${r.network.vmIp}:${toString port}") roles';

  mkRelabelConfigs = roles': lib.mapAttrsToList (name: r: {
    source_labels = [ "__address__" ];
    regex = "${r.network.vmIp}:.*";
    target_label = "instance";
    replacement = name;
  }) roles';

  # Force evaluation of index validation
  _indexValidation = indexValidation;
}
