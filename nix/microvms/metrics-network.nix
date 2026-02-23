# nix/microvms/metrics-network.nix
#
# Network setup scripts for the metrics VM.
# Similar pattern to iperf-test.nix but for the metrics infrastructure.
#
# Usage:
#   sudo nix run .#metrics-network-setup-privileged -- "$USER"
#   nix run .#srt-metrics-vm
#   sudo nix run .#metrics-network-cleanup-privileged
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  metricsRole = gosrtLib.roles.metrics;

  # Network configuration from computed values
  tap = metricsRole.network.tap;
  bridge = metricsRole.network.bridge;
  vmIp = metricsRole.network.vmIp;
  gateway = metricsRole.network.gateway;
  subnet = metricsRole.network.subnet;

in {
  # ─── Privileged Network Setup ─────────────────────────────────────────────────
  privilegedSetupScript = pkgs.writeShellApplication {
    name = "metrics-network-setup-privileged";
    runtimeInputs = with pkgs; [ iproute2 coreutils ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root (with sudo)"
        exit 1
      fi

      if [ $# -lt 1 ]; then
        echo "Usage: $0 <username>"
        exit 1
      fi

      TARGET_USER="$1"

      if ! id "$TARGET_USER" >/dev/null 2>&1; then
        echo "ERROR: User '$TARGET_USER' does not exist"
        exit 1
      fi

      echo "Setting up metrics VM network for user: $TARGET_USER"
      echo ""
      echo "Configuration:"
      echo "  TAP:     ${tap}"
      echo "  Bridge:  ${bridge}"
      echo "  VM IP:   ${vmIp}"
      echo "  Gateway: ${gateway}"
      echo ""

      # Create bridge
      if ! ip link show ${bridge} >/dev/null 2>&1; then
        echo "Creating bridge ${bridge}..."
        ip link add ${bridge} type bridge
      fi
      ip link set ${bridge} up

      # Add gateway IP to bridge (so host can reach VM)
      if ! ip addr show ${bridge} | grep -q "${gateway}"; then
        echo "Adding gateway ${gateway} to ${bridge}..."
        ip addr add ${gateway}/24 dev ${bridge}
      fi

      # Create TAP device
      if ! ip link show ${tap} >/dev/null 2>&1; then
        echo "Creating TAP device ${tap} for user $TARGET_USER..."
        ip tuntap add dev ${tap} mode tap multi_queue user "$TARGET_USER"
      fi
      ip link set ${tap} master ${bridge}
      ip link set ${tap} up

      # Ensure /dev/net/tun and /dev/vhost-net are accessible
      if [ -c /dev/net/tun ]; then
        chmod 0666 /dev/net/tun
      fi
      if [ -c /dev/vhost-net ]; then
        chmod 0666 /dev/vhost-net
      fi

      echo ""
      echo "Network setup complete."
      echo ""
      echo "Now run the metrics VM:"
      echo "  nix run .#srt-metrics-vm"
      echo ""
      echo "Access:"
      echo "  Prometheus: http://${vmIp}:${toString gosrtLib.ports.prometheusServer}"
      echo "  Grafana:    http://${vmIp}:${toString gosrtLib.ports.grafana} (admin/srt)"
    '';
  };

  # ─── Privileged Network Cleanup ───────────────────────────────────────────────
  privilegedCleanupScript = pkgs.writeShellApplication {
    name = "metrics-network-cleanup-privileged";
    runtimeInputs = with pkgs; [ iproute2 coreutils procps ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root (with sudo)"
        exit 1
      fi

      echo "Cleaning up metrics VM network..."

      # Stop any running VMs
      pkill -f "qemu.*srt-metrics" 2>/dev/null || true
      sleep 1

      # Remove TAP device
      if ip link show ${tap} >/dev/null 2>&1; then
        echo "Removing TAP ${tap}..."
        ip link del ${tap}
      fi

      # Remove bridge
      if ip link show ${bridge} >/dev/null 2>&1; then
        echo "Removing bridge ${bridge}..."
        ip link del ${bridge}
      fi

      echo "Cleanup complete."
    '';
  };
}
