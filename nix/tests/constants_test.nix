# nix/tests/constants_test.nix
#
# Unit tests for constants.nix and lib.nix
#
# Run with:
#   nix eval --expr '(import ./nix/tests/constants_test.nix { inherit (import <nixpkgs> {}) pkgs lib; })'
#
# Or via flake:
#   nix eval .#checks.x86_64-linux.lib-eval
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  constants = import ../constants.nix;

in {
  # ─── Constants Tests ─────────────────────────────────────────────────────

  # Test: All roles have unique indices
  test_unique_indices = let
    indices = lib.mapAttrsToList (_: r: r.index) gosrtLib.roles;
    uniqueIndices = lib.unique indices;
  in
    assert (builtins.length indices) == (builtins.length uniqueIndices);
    "PASS: All ${toString (builtins.length indices)} roles have unique indices";

  # Test: All roles have required fields
  test_required_fields = lib.mapAttrs (name: role:
    assert role ? index;
    assert role ? shortName;
    assert role ? router;
    assert role.router == "A" || role.router == "B";
    assert role ? package;
    "PASS: ${name} has all required fields"
  ) gosrtLib.roles;

  # Test: Role indices are valid (1-254)
  test_index_range = lib.mapAttrs (name: role:
    assert role.index >= 1 && role.index <= 254;
    "PASS: ${name} index ${toString role.index} is in valid range"
  ) gosrtLib.roles;

  # ─── Lib Tests ───────────────────────────────────────────────────────────

  # Test: Network derivation produces valid IPs
  test_network_ips = lib.mapAttrs (name: role:
    assert lib.hasPrefix "10.50." role.network.vmIp;
    assert lib.hasSuffix ".2" role.network.vmIp;
    "PASS: ${name} has valid IP ${role.network.vmIp}"
  ) gosrtLib.roles;

  # Test: Server IP is correct (index 3 -> 10.50.3.2)
  test_server_ip =
    assert gosrtLib.serverIp == "10.50.3.2";
    "PASS: Server IP is 10.50.3.2";

  # Test: MAC addresses are properly formatted
  test_mac_format = lib.mapAttrs (name: role:
    assert lib.hasPrefix "02:00:00:50:" role.network.mac;
    assert lib.hasSuffix ":02" role.network.mac;
    "PASS: ${name} has valid MAC ${role.network.mac}"
  ) gosrtLib.roles;

  # Test: Gateway is derived correctly
  test_gateway = lib.mapAttrs (name: role:
    let
      expectedGateway = "10.50.${toString role.index}.1";
    in
      assert role.network.gateway == expectedGateway;
      "PASS: ${name} gateway ${role.network.gateway}"
  ) gosrtLib.roles;

  # Test: Port derivation
  test_ports = lib.mapAttrs (name: role:
    assert role.ports.console == (45000 + role.index);
    assert role.ports.sshForward == (22000 + role.index);
    assert role.ports.prometheusForward == (19000 + role.index);
    "PASS: ${name} ports are correct"
  ) gosrtLib.roles;

  # Test: Role count is 8
  test_role_count =
    assert (builtins.length gosrtLib.roleNames) == 8;
    "PASS: 8 roles defined";

  # Test: Router A roles
  test_router_a_roles =
    let
      routerARoleNames = builtins.attrNames gosrtLib.routerARoles;
      expectedA = [ "publisher" "subscriber" "xtransmit-pub" "ffmpeg-pub" "xtransmit-sub" "ffmpeg-sub" ];
    in
      assert (builtins.length routerARoleNames) == 6;
      "PASS: 6 roles on Router A";

  # Test: Router B roles
  test_router_b_roles =
    let
      routerBRoleNames = builtins.attrNames gosrtLib.routerBRoles;
    in
      assert (builtins.length routerBRoleNames) == 2;
      "PASS: 2 roles on Router B (server, metrics)";

  # Test: Prometheus roles (only those with hasPrometheus = true)
  test_prometheus_roles =
    let
      promRoleNames = builtins.attrNames gosrtLib.prometheusRoles;
    in
      assert (builtins.length promRoleNames) == 3;  # server, publisher, subscriber
      "PASS: 3 roles have Prometheus endpoints";

  # Test: Inter-router links
  test_inter_router_links =
    assert (builtins.length gosrtLib.interRouterLinks) == 5;
    assert (builtins.head gosrtLib.interRouterLinks).name == "no-delay";
    assert (builtins.elemAt gosrtLib.interRouterLinks 4).name == "geo-satellite";
    "PASS: 5 inter-router latency profiles defined";

  # Test: vmName derivation
  test_vm_names = lib.mapAttrs (name: role:
    assert role.vmName == "srt-${name}";
    "PASS: ${name} vmName is srt-${name}"
  ) gosrtLib.roles;

  # Test: Router namespace derivation
  test_router_namespaces = lib.mapAttrs (name: role:
    let
      expectedNs = if role.router == "A" then "srt-router-a" else "srt-router-b";
    in
      assert role.routerNamespace == expectedNs;
      "PASS: ${name} namespace is ${expectedNs}"
  ) gosrtLib.roles;

  # ─── Summary ─────────────────────────────────────────────────────────────
  summary = {
    roles_tested = builtins.length gosrtLib.roleNames;
    server_ip = gosrtLib.serverIp;
    latency_profiles = builtins.length gosrtLib.interRouterLinks;
    router_a_roles = builtins.length (builtins.attrNames gosrtLib.routerARoles);
    router_b_roles = builtins.length (builtins.attrNames gosrtLib.routerBRoles);
    prometheus_roles = builtins.length (builtins.attrNames gosrtLib.prometheusRoles);
    status = "ALL TESTS PASSED";
  };
}
