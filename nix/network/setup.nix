# nix/network/setup.nix
#
# Data-driven network setup for GoSRT MicroVM infrastructure.
# Creates TAPs, bridges, veth pairs, and router namespaces.
#
# Reference: documentation/nix_microvm_design.md lines 3598-4079
#
# Architecture:
#   - TAP devices stay in host namespace (QEMU needs access)
#   - Bridges connect TAPs to veth pairs
#   - Veth pairs cross into router namespaces
#   - Inter-router links have configurable latency
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  c = import ../constants.nix;

  # Generate network setup commands for all roles
  roleSetupCommands = lib.concatMapStringsSep "\n" (name: let
    role = gosrtLib.roles.${name};
    net = role.network;
    router = gosrtLib.routers.${role.router}.namespace;
  in ''
    echo "Setting up network for ${name} (${net.vmIp})..."
    create_vm_network "${net.tap}" "${net.bridge}" "${net.vethHost}" "${net.vethRouter}" \
      "${router}" "${net.gateway}"
  '') gosrtLib.roleNames;

  # Generate inter-router link setup
  interRouterSetupCommands = lib.concatMapStringsSep "\n" (link: ''
    echo "Creating inter-router link ${link.name} (${toString link.rttMs}ms RTT)..."
    create_inter_router_link "${link.vethA}" "${link.vethB}" "${link.ipA}" "${link.ipB}" \
      "${gosrtLib.routerA}" "${gosrtLib.routerB}" "${toString link.rttMs}"
  '') gosrtLib.interRouterLinks;

  # Generate teardown commands for all roles
  roleTeardownCommands = lib.concatMapStringsSep "\n" (name: let
    role = gosrtLib.roles.${name};
    net = role.network;
  in ''
    teardown_vm_network "${net.tap}" "${net.bridge}" "${net.vethHost}"
  '') gosrtLib.roleNames;

  # Get the first inter-router link (no-delay) for routing
  defaultLink = builtins.head gosrtLib.interRouterLinks;

  # Generate routes from Router A to Router B subnets
  # Router A uses defaultLink.ipB as gateway to reach Router B subnets
  routerARoutes = lib.concatMapStringsSep "\n" (name: let
    role = gosrtLib.roles.${name};
  in lib.optionalString (role.router == "B") ''
    ip netns exec "${gosrtLib.routerA}" ip route add "${role.network.subnet}" via "${defaultLink.ipB}" 2>/dev/null || true
  '') gosrtLib.roleNames;

  # Generate routes from Router B to Router A subnets
  # Router B uses defaultLink.ipA as gateway to reach Router A subnets
  routerBRoutes = lib.concatMapStringsSep "\n" (name: let
    role = gosrtLib.roles.${name};
  in lib.optionalString (role.router == "A") ''
    ip netns exec "${gosrtLib.routerB}" ip route add "${role.network.subnet}" via "${defaultLink.ipA}" 2>/dev/null || true
  '') gosrtLib.roleNames;

in {
  # ─── Network Setup Script ─────────────────────────────────────────────────────
  setupScript = pkgs.writeShellApplication {
    name = "srt-network-setup";
    runtimeInputs = with pkgs; [ iproute2 coreutils ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root"
        exit 1
      fi

      TARGET_USER="''${1:-$SUDO_USER}"
      if [ -z "$TARGET_USER" ]; then
        echo "Usage: $0 <username>"
        exit 1
      fi

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       GoSRT Network Setup                                        ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Creating network for user: $TARGET_USER"
      echo ""

      # ─── Helper Functions ───────────────────────────────────────────────────

      create_router_namespace() {
        local ns="$1"
        if ! ip netns list | grep -q "^$ns"; then
          echo "Creating namespace $ns..."
          ip netns add "$ns"
        fi
        # Enable forwarding in namespace
        ip netns exec "$ns" sysctl -w net.ipv4.ip_forward=1 >/dev/null
      }

      create_vm_network() {
        local tap="$1"
        local bridge="$2"
        local veth_host="$3"
        local veth_router="$4"
        local router_ns="$5"
        local gateway="$6"

        # Create bridge
        if ! ip link show "$bridge" >/dev/null 2>&1; then
          ip link add "$bridge" type bridge
        fi
        ip link set "$bridge" up

        # Create TAP device (owned by target user for unprivileged QEMU)
        if ! ip link show "$tap" >/dev/null 2>&1; then
          ip tuntap add dev "$tap" mode tap multi_queue user "$TARGET_USER"
        fi
        ip link set "$tap" master "$bridge"
        ip link set "$tap" up

        # Create veth pair
        if ! ip link show "$veth_host" >/dev/null 2>&1; then
          ip link add "$veth_host" type veth peer name "$veth_router"
        fi

        # Connect host side to bridge
        ip link set "$veth_host" master "$bridge"
        ip link set "$veth_host" up

        # Move router side to namespace
        ip link set "$veth_router" netns "$router_ns"
        ip netns exec "$router_ns" ip link set "$veth_router" up
        ip netns exec "$router_ns" ip addr add "$gateway/24" dev "$veth_router" 2>/dev/null || true

        # Add host IP to bridge for direct VM access (gateway + 252 to avoid conflicts)
        # E.g., for 10.50.3.1 gateway, host gets 10.50.3.254
        local host_ip
        host_ip="''${gateway%.1}.254"
        ip addr add "$host_ip/24" dev "$bridge" 2>/dev/null || true
      }

      create_inter_router_link() {
        local veth_a="$1"
        local veth_b="$2"
        local ip_a="$3"
        local ip_b="$4"
        local ns_a="$5"
        local ns_b="$6"
        local rtt_ms="$7"

        # Create veth pair if it doesn't exist
        if ! ip link show "$veth_a" >/dev/null 2>&1; then
          ip link add "$veth_a" type veth peer name "$veth_b"
        fi

        # Move to namespaces
        ip link set "$veth_a" netns "$ns_a" 2>/dev/null || true
        ip link set "$veth_b" netns "$ns_b" 2>/dev/null || true

        # Configure IPs and bring up
        ip netns exec "$ns_a" ip addr add "$ip_a/30" dev "$veth_a" 2>/dev/null || true
        ip netns exec "$ns_a" ip link set "$veth_a" up

        ip netns exec "$ns_b" ip addr add "$ip_b/30" dev "$veth_b" 2>/dev/null || true
        ip netns exec "$ns_b" ip link set "$veth_b" up

        # Apply latency (RTT/2 on each side)
        if [ "$rtt_ms" -gt 0 ]; then
          local half_rtt=$((rtt_ms / 2))
          ip netns exec "$ns_a" tc qdisc replace dev "$veth_a" root netem delay "''${half_rtt}ms" limit ${toString c.netem.queueLimit}
          ip netns exec "$ns_b" tc qdisc replace dev "$veth_b" root netem delay "''${half_rtt}ms" limit ${toString c.netem.queueLimit}
        fi
      }

      # ─── Create Router Namespaces ───────────────────────────────────────────
      echo "=== Creating Router Namespaces ==="
      create_router_namespace "${gosrtLib.routerA}"
      create_router_namespace "${gosrtLib.routerB}"

      # ─── Create VM Networks ─────────────────────────────────────────────────
      echo ""
      echo "=== Creating VM Networks ==="
      ${roleSetupCommands}

      # ─── Create Inter-Router Links ──────────────────────────────────────────
      echo ""
      echo "=== Creating Inter-Router Links ==="
      ${interRouterSetupCommands}

      # ─── Add Inter-Router Routes ─────────────────────────────────────────────
      echo ""
      echo "=== Adding Inter-Router Routes ==="
      echo "Router A -> Router B subnets via ${defaultLink.ipB}..."
      ${routerARoutes}
      echo "Router B -> Router A subnets via ${defaultLink.ipA}..."
      ${routerBRoutes}

      # ─── Set Device Permissions ─────────────────────────────────────────────
      echo ""
      echo "=== Setting Device Permissions ==="
      [ -c /dev/net/tun ] && chmod 0666 /dev/net/tun && echo "Set /dev/net/tun to 0666"
      [ -c /dev/vhost-net ] && chmod 0666 /dev/vhost-net && echo "Set /dev/vhost-net to 0666"

      echo ""
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       Network Setup Complete                                     ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Namespaces: ${gosrtLib.routerA}, ${gosrtLib.routerB}"
      echo "VM Networks: ${toString (builtins.length gosrtLib.roleNames)} roles configured"
      echo ""
      echo "User '$TARGET_USER' can now run VMs without sudo."
    '';
  };

  # ─── Network Teardown Script ──────────────────────────────────────────────────
  teardownScript = pkgs.writeShellApplication {
    name = "srt-network-teardown";
    runtimeInputs = with pkgs; [ iproute2 coreutils procps ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root"
        exit 1
      fi

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       GoSRT Network Teardown                                     ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""

      # Stop any running VMs
      echo "Stopping VMs..."
      pkill -f "qemu.*srt-" 2>/dev/null || true
      sleep 1

      teardown_vm_network() {
        local tap="$1"
        local bridge="$2"
        local veth_host="$3"

        ip link del "$tap" 2>/dev/null || true
        ip link del "$veth_host" 2>/dev/null || true
        ip link del "$bridge" 2>/dev/null || true
      }

      # Teardown VM networks
      echo "Removing VM networks..."
      ${roleTeardownCommands}

      # Remove inter-router links (will be deleted with namespaces)

      # Remove namespaces
      echo "Removing namespaces..."
      ip netns del "${gosrtLib.routerA}" 2>/dev/null || true
      ip netns del "${gosrtLib.routerB}" 2>/dev/null || true

      echo ""
      echo "Teardown complete."
    '';
  };
}
