# nix/validation/iperf-test.nix
#
# Minimal iperf2 test VMs to validate vhost-net throughput.
# Run BEFORE deploying GoSRT to establish baseline.
#
# Reference: documentation/nix_microvm_implementation_plan.md Phase 0
#
# ⚠️  SECURITY WARNING ⚠️
# These VMs use INSECURE empty-password SSH for test automation.
# DO NOT use this configuration in production environments!
# The VMs are intended for isolated local testing only.
#
# Usage:
#   sudo nix run .#iperf-network-setup-privileged -- "$USER"
#   nix run .#iperf-test-unprivileged
#   sudo nix run .#iperf-network-cleanup-privileged
#
{ pkgs, lib, microvm, nixpkgs, system }:

let
  # Network configuration
  serverIp = "10.99.0.1";
  clientIp = "10.99.0.2";
  serverTap = "ipftap-server";
  clientTap = "ipftap-client";
  bridge = "ipfbr0";

  # ⚠️ INSECURE: Static SSH key for test automation - DO NOT USE IN PRODUCTION!
  # This key is embedded directly so both VMs and test scripts share the same key.
  testSshPrivateKey = ''
    -----BEGIN OPENSSH PRIVATE KEY-----
    b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
    QyNTUxOQAAACBQVnzSVPwqPEEvGIbEJvjvOBWmm3MuKP8btL6LoxhCjgAAAJhH3TvCR907
    wgAAAAtzc2gtZWQyNTUxOQAAACBQVnzSVPwqPEEvGIbEJvjvOBWmm3MuKP8btL6LoxhCjg
    AAAEBzMUK6Rl4WXY2gKULXKDrNw7fW8CjWxY1bILwXGVNTe1BWfNJU/Co8QS8YhsQm+O84
    Faabcy4o/xu0voujGEKOAAAACmlwZXJmLXRlc3QBAgMEBQ==
    -----END OPENSSH PRIVATE KEY-----
  '';
  testSshPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFBWJRJU/Co8QS8YhsQm+O84FaabcyT4o/xu0voujGEKO iperf-test";

  # Write the key to a file in the nix store
  sshKeyFile = pkgs.writeText "iperf-test-key" testSshPrivateKey;

  # Create a minimal MicroVM for iperf testing
  mkIperfVM = { name, ip, role }:
    (nixpkgs.lib.nixosSystem {
      inherit system;
      modules = [
        microvm.nixosModules.microvm
        ({ config, pkgs, ... }: {
          system.stateVersion = "24.05";
          networking.hostName = "iperf-${name}";

          microvm = {
            hypervisor = "qemu";
            mem = 1024;
            vcpu = 2;
            interfaces = [{
              type = "tap";
              id = "ipftap-${name}";
              mac = "02:00:00:99:00:0${if role == "server" then "1" else "2"}";
            }];
          };

          systemd.network = {
            enable = true;
            networks."10-vm" = {
              matchConfig.Name = "eth*";
              networkConfig = {
                DHCP = "no";
                Address = "${ip}/24";
              };
            };
          };

          networking.useNetworkd = true;
          networking.useDHCP = false;

          environment.systemPackages = with pkgs; [ iperf ethtool iproute2 ];

          systemd.services.iperf-server = lib.mkIf (role == "server") {
            description = "iperf2 server";
            wantedBy = [ "multi-user.target" ];
            after = [ "network-online.target" ];
            wants = [ "network-online.target" ];
            serviceConfig = {
              ExecStart = "${pkgs.iperf}/bin/iperf -s";
              Restart = "always";
            };
          };

          # INSECURE: Simple password root login for test automation only
          services.openssh = {
            enable = true;
            settings = {
              PermitRootLogin = "yes";
              PasswordAuthentication = true;
              KbdInteractiveAuthentication = false;
            };
          };
          # Use a simple known password "test" for automation
          users.users.root.initialPassword = "test";

          networking.firewall.enable = false;
        })
      ];
    }).config.microvm.declaredRunner;

  # VM definitions
  serverVM = mkIperfVM { name = "server"; ip = serverIp; role = "server"; };
  clientVM = mkIperfVM { name = "client"; ip = clientIp; role = "client"; };

in rec {
  # Export VMs for individual use
  server = serverVM;
  client = clientVM;

  # ─── Privileged Network Setup (run with sudo) ──────────────────────────────
  # This script must be run as root. It creates network infrastructure and
  # sets ownership so the calling user can run VMs without sudo afterwards.
  #
  # Usage: sudo $(nix build .#iperf-network-setup-privileged --print-out-paths)/bin/iperf-network-setup-privileged <username>
  #    or: sudo nix run .#iperf-network-setup-privileged -- "$USER"
  #
  privilegedSetupScript = pkgs.writeShellApplication {
    name = "iperf-network-setup-privileged";
    runtimeInputs = with pkgs; [ iproute2 coreutils ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root (with sudo)"
        exit 1
      fi

      if [ $# -lt 1 ]; then
        echo "Usage: $0 <username>"
        echo "  username: The user who will run the VMs"
        exit 1
      fi

      TARGET_USER="$1"

      # Validate user exists
      if ! id "$TARGET_USER" >/dev/null 2>&1; then
        echo "ERROR: User '$TARGET_USER' does not exist"
        exit 1
      fi

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       iperf Network Setup (Privileged)                           ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "⚠️  WARNING: VMs use INSECURE password 'test' for SSH"
      echo "⚠️  For local testing only - DO NOT expose to network!"
      echo ""
      echo "Creating network for user: $TARGET_USER"
      echo ""

      # Create bridge if it doesn't exist
      if ! ip link show ${bridge} >/dev/null 2>&1; then
        echo "Creating bridge ${bridge}..."
        ip link add ${bridge} type bridge
      else
        echo "Bridge ${bridge} already exists"
      fi
      ip link set ${bridge} up

      # Add IP to bridge so host can reach VMs
      if ! ip addr show ${bridge} | grep -q "10.99.0.254"; then
        echo "Adding IP 10.99.0.254/24 to ${bridge}..."
        ip addr add 10.99.0.254/24 dev ${bridge}
      else
        echo "Bridge ${bridge} already has IP 10.99.0.254"
      fi

      # Create TAP devices with multi_queue, owned by target user
      for tap in ${serverTap} ${clientTap}; do
        if ! ip link show "$tap" >/dev/null 2>&1; then
          echo "Creating TAP device $tap for user $TARGET_USER..."
          ip tuntap add dev "$tap" mode tap multi_queue user "$TARGET_USER"
        else
          echo "TAP device $tap already exists"
        fi
        ip link set "$tap" master ${bridge}
        ip link set "$tap" up
      done

      # Ensure /dev/net/tun is accessible
      if [ -c /dev/net/tun ]; then
        chmod 0666 /dev/net/tun
        echo "Set /dev/net/tun permissions to 0666"
      fi

      # Ensure /dev/vhost-net is accessible (for vhost acceleration)
      if [ -c /dev/vhost-net ]; then
        chmod 0666 /dev/vhost-net
        echo "Set /dev/vhost-net permissions to 0666"
      else
        echo "WARNING: /dev/vhost-net not found - vhost acceleration unavailable"
        echo "  Load module with: modprobe vhost_net"
      fi

      echo ""
      echo "✓ Network setup complete"
      echo ""
      echo "Devices created:"
      echo "  Bridge:     ${bridge}"
      echo "  Server TAP: ${serverTap} (owner: $TARGET_USER)"
      echo "  Client TAP: ${clientTap} (owner: $TARGET_USER)"
      echo ""
      echo "User '$TARGET_USER' can now run VMs without sudo:"
      echo "  nix run .#iperf-test-unprivileged"
    '';
  };

  # ─── Privileged Network Cleanup (run with sudo) ────────────────────────────
  privilegedCleanupScript = pkgs.writeShellApplication {
    name = "iperf-network-cleanup-privileged";
    runtimeInputs = with pkgs; [ iproute2 coreutils procps ];
    text = ''
      if [ "$(id -u)" -ne 0 ]; then
        echo "ERROR: This script must be run as root (with sudo)"
        exit 1
      fi

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       iperf Network Cleanup (Privileged)                         ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""

      # Kill any running VMs
      echo "Stopping any running iperf VMs..."
      pkill -f "qemu.*iperf" 2>/dev/null || true
      sleep 1

      # Remove TAP devices
      for tap in ${serverTap} ${clientTap}; do
        if ip link show "$tap" >/dev/null 2>&1; then
          echo "Removing TAP device $tap..."
          ip link del "$tap"
        fi
      done

      # Remove bridge
      if ip link show ${bridge} >/dev/null 2>&1; then
        echo "Removing bridge ${bridge}..."
        ip link del ${bridge}
      fi

      echo ""
      echo "✓ Cleanup complete"
    '';
  };

  # ─── Legacy Network Setup (kept for compatibility) ─────────────────────────
  setupScript = pkgs.writeShellApplication {
    name = "iperf-network-setup";
    runtimeInputs = with pkgs; [ iproute2 coreutils ];
    text = ''
      echo "=== Setting up iperf test network ==="

      # Create bridge
      sudo ip link add ${bridge} type bridge 2>/dev/null || true
      sudo ip link set ${bridge} up

      # Create TAP devices with multi_queue
      sudo ip tuntap add dev ${serverTap} mode tap multi_queue user "$USER" 2>/dev/null || true
      sudo ip tuntap add dev ${clientTap} mode tap multi_queue user "$USER" 2>/dev/null || true

      # Add to bridge and bring up
      sudo ip link set ${serverTap} master ${bridge}
      sudo ip link set ${clientTap} master ${bridge}
      sudo ip link set ${serverTap} up
      sudo ip link set ${clientTap} up

      echo "Network ready: ${bridge} with ${serverTap}, ${clientTap}"
    '';
  };

  # ─── Legacy Network Cleanup ────────────────────────────────────────────────
  cleanupScript = pkgs.writeShellApplication {
    name = "iperf-network-cleanup";
    runtimeInputs = with pkgs; [ iproute2 coreutils procps ];
    text = ''
      echo "=== Cleaning up iperf test network ==="

      # Kill any running VMs
      pkill -f "microvm-run.*iperf" 2>/dev/null || true

      # Remove network devices
      sudo ip link del ${serverTap} 2>/dev/null || true
      sudo ip link del ${clientTap} 2>/dev/null || true
      sudo ip link del ${bridge} 2>/dev/null || true

      echo "Cleanup complete."
    '';
  };

  # ─── Unprivileged Test (after network setup) ─────────────────────────────
  # Run this AFTER running iperf-network-setup-privileged with sudo.
  # This script runs entirely as the current user.
  #
  # Usage:
  #   1. sudo nix run .#iperf-network-setup-privileged -- "$USER"
  #   2. nix run .#iperf-test-unprivileged
  #   3. sudo nix run .#iperf-network-cleanup-privileged
  #
  unprivilegedTestScript = pkgs.writeShellApplication {
    name = "iperf-test-unprivileged";
    runtimeInputs = with pkgs; [ iproute2 coreutils openssh procps netcat-gnu sshpass ];
    text = ''
      DURATION="''${1:-10}"
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       Phase 0: iperf2 Infrastructure Validation (Unprivileged)   ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "⚠️  WARNING: VMs use INSECURE password 'test' for SSH automation"
      echo "⚠️  DO NOT expose these VMs to any network - local testing only!"
      echo ""
      echo "Test duration: ''${DURATION}s per test"
      echo "Server: ${serverIp}"
      echo "Client: ${clientIp}"
      echo ""

      # Verify network is set up
      echo "=== Verifying network setup ==="
      if ! ip link show ${bridge} >/dev/null 2>&1; then
        echo "ERROR: Bridge ${bridge} not found"
        echo ""
        echo "Run network setup first:"
        echo "  sudo nix run .#iperf-network-setup-privileged -- \"\$USER\""
        exit 1
      fi
      if ! ip link show ${serverTap} >/dev/null 2>&1; then
        echo "ERROR: TAP device ${serverTap} not found"
        echo ""
        echo "Run network setup first:"
        echo "  sudo nix run .#iperf-network-setup-privileged -- \"\$USER\""
        exit 1
      fi
      if ! ip link show ${clientTap} >/dev/null 2>&1; then
        echo "ERROR: TAP device ${clientTap} not found"
        echo ""
        echo "Run network setup first:"
        echo "  sudo nix run .#iperf-network-setup-privileged -- \"\$USER\""
        exit 1
      fi
      echo "✓ Network ready"
      echo ""

      # Cleanup function (VMs only - network stays)
      cleanup() {
        echo ""
        echo "=== Stopping VMs ==="
        pkill -f "qemu.*iperf" 2>/dev/null || true
        sleep 1
        echo "Done. (Network left intact - use iperf-network-cleanup-privileged to remove)"
      }
      trap cleanup EXIT

      # Start VMs
      echo "=== Step 1: Starting VMs ==="
      ${serverVM}/bin/microvm-run &
      SERVER_PID=$!
      sleep 1
      ${clientVM}/bin/microvm-run &
      CLIENT_PID=$!
      echo "✓ VMs started (server=$SERVER_PID, client=$CLIENT_PID)"
      echo ""

      # Wait for VMs to boot
      echo "=== Step 2: Waiting for VMs ==="
      printf "Waiting for SSH..."
      for i in $(seq 1 60); do
        if nc -z -w1 ${serverIp} 22 2>/dev/null && nc -z -w1 ${clientIp} 22 2>/dev/null; then
          echo " ready after ''${i}s"
          break
        fi
        if [ "$i" -eq 60 ]; then
          echo ""
          echo "ERROR: VMs did not come up in 60s"
          exit 1
        fi
        printf "."
        sleep 1
      done

      # Wait for iperf server to start
      echo "Waiting for iperf server..."
      sleep 5
      echo "✓ VMs ready"
      echo ""

      # SSH with sshpass for password auth (non-interactive)
      # Password is "test" - INSECURE, for testing only!
      export SSHPASS="test"
      ssh_base_opts=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o PubkeyAuthentication=no -o PreferredAuthentications=password)

      # Helper to run SSH with password
      ssh_run() {
        sshpass -e ssh "''${ssh_base_opts[@]}" "root@$1" "$2"
      }

      # Test SSH connectivity first
      echo "=== Step 3: Testing SSH ==="
      if ssh_run "${clientIp}" "echo SSH OK"; then
        echo "✓ SSH working"
      else
        echo "ERROR: SSH to client failed"
        exit 1
      fi
      echo ""

      # Helper function to run iperf via SSH
      # Passes command through printf to stdin to satisfy SC2029
      run_iperf() {
        printf '%s\n' "$1" | sshpass -e ssh "''${ssh_base_opts[@]}" "root@${clientIp}" /bin/sh
      }

      # Run TCP test
      echo "=== Step 4: TCP Throughput Test ==="
      echo "Running: iperf -c ${serverIp} -t $DURATION -f g"
      run_iperf "iperf -c ${serverIp} -t $DURATION -f g" || {
        echo "WARNING: TCP test failed (exit code: $?)"
      }
      echo ""

      # Run UDP test at 1Gbps
      echo "=== Step 5: UDP 1Gbps Test ==="
      echo "Running: iperf -c ${serverIp} -t $DURATION -u -b 1G -f g"
      run_iperf "iperf -c ${serverIp} -t $DURATION -u -b 1G -f g" || {
        echo "WARNING: UDP 1G test failed (exit code: $?)"
      }
      echo ""

      # Run UDP test at 5Gbps
      echo "=== Step 6: UDP 5Gbps Test ==="
      echo "Running: iperf -c ${serverIp} -t $DURATION -u -b 5G -f g"
      run_iperf "iperf -c ${serverIp} -t $DURATION -u -b 5G -f g" || {
        echo "WARNING: UDP 5G test failed (exit code: $?)"
      }
      echo ""

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║                     Tests Complete                               ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Expected baselines:"
      echo "  TCP:     > 5 Gbps (target: 8+ Gbps)"
      echo "  UDP 1G:  ~1 Gbps, <0.1% loss"
      echo "  UDP 5G:  ~5 Gbps, <1% loss"
      echo ""
      echo "If results are below expectations, check:"
      echo "  1. vhost-net module loaded: lsmod | grep vhost"
      echo "  2. TAP multi_queue: ethtool -l ${serverTap}"
      echo "  3. Kernel buffers: sysctl net.core.rmem_max"
      echo ""
      echo "To cleanup network:"
      echo "  sudo nix run .#iperf-network-cleanup-privileged"
    '';
  };

  # ─── Full Automated Test (legacy - uses embedded sudo) ────────────────────
  testScript = pkgs.writeShellApplication {
    name = "iperf-test";
    runtimeInputs = with pkgs; [ iproute2 coreutils openssh procps netcat-gnu ];
    text = ''
      DURATION="''${1:-10}"
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       Phase 0: iperf2 Infrastructure Validation                  ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Test duration: ''${DURATION}s per test"
      echo "Server: ${serverIp}"
      echo "Client: ${clientIp}"
      echo ""

      # Cleanup function
      cleanup() {
        echo ""
        echo "=== Cleanup ==="
        pkill -f "qemu.*iperf" 2>/dev/null || true
        sleep 2
        sudo ip link del ${serverTap} 2>/dev/null || true
        sudo ip link del ${clientTap} 2>/dev/null || true
        sudo ip link del ${bridge} 2>/dev/null || true
        echo "Done."
      }
      trap cleanup EXIT

      # Setup network
      echo "=== Step 1: Network Setup ==="
      sudo ip link add ${bridge} type bridge 2>/dev/null || true
      sudo ip link set ${bridge} up
      sudo ip tuntap add dev ${serverTap} mode tap multi_queue user "$USER" 2>/dev/null || true
      sudo ip tuntap add dev ${clientTap} mode tap multi_queue user "$USER" 2>/dev/null || true
      sudo ip link set ${serverTap} master ${bridge}
      sudo ip link set ${clientTap} master ${bridge}
      sudo ip link set ${serverTap} up
      sudo ip link set ${clientTap} up
      echo "✓ Network ready"
      echo ""

      # Start VMs
      echo "=== Step 2: Starting VMs ==="
      ${serverVM}/bin/microvm-run &
      SERVER_PID=$!
      sleep 1
      ${clientVM}/bin/microvm-run &
      CLIENT_PID=$!
      echo "✓ VMs started (server=$SERVER_PID, client=$CLIENT_PID)"
      echo ""

      # Wait for VMs to boot
      echo "=== Step 3: Waiting for VMs ==="
      printf "Waiting for SSH..."
      for i in $(seq 1 60); do
        if nc -z -w1 ${serverIp} 22 2>/dev/null && nc -z -w1 ${clientIp} 22 2>/dev/null; then
          echo " ready after ''${i}s"
          break
        fi
        if [ "$i" -eq 60 ]; then
          echo ""
          echo "ERROR: VMs did not come up in 60s"
          exit 1
        fi
        printf "."
        sleep 1
      done

      # Wait for iperf server to start
      echo "Waiting for iperf server..."
      sleep 5
      echo "✓ VMs ready"
      echo ""

      # SSH options as array to avoid word splitting issues (SC2086)
      ssh_opts=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)

      # Run TCP test
      echo "=== Step 4: TCP Throughput Test ==="
      echo "Running: iperf -c ${serverIp} -t $DURATION -f g"
      ssh "''${ssh_opts[@]}" "root@${clientIp}" \
        'iperf -c ${serverIp} -t '"$DURATION"' -f g' 2>/dev/null || {
        echo "WARNING: TCP test had issues"
      }
      echo ""

      # Run UDP test at 1Gbps
      echo "=== Step 5: UDP 1Gbps Test ==="
      echo "Running: iperf -c ${serverIp} -t $DURATION -u -b 1G -f g"
      ssh "''${ssh_opts[@]}" "root@${clientIp}" \
        'iperf -c ${serverIp} -t '"$DURATION"' -u -b 1G -f g' 2>/dev/null || {
        echo "WARNING: UDP test had issues"
      }
      echo ""

      # Run UDP test at 5Gbps
      echo "=== Step 6: UDP 5Gbps Test ==="
      echo "Running: iperf -c ${serverIp} -t $DURATION -u -b 5G -f g"
      ssh "''${ssh_opts[@]}" "root@${clientIp}" \
        'iperf -c ${serverIp} -t '"$DURATION"' -u -b 5G -f g' 2>/dev/null || {
        echo "WARNING: UDP test had issues"
      }
      echo ""

      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║                     Tests Complete                               ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""
      echo "Expected baselines:"
      echo "  TCP:     > 5 Gbps (target: 8+ Gbps)"
      echo "  UDP 1G:  ~1 Gbps, <0.1% loss"
      echo "  UDP 5G:  ~5 Gbps, <1% loss"
      echo ""
      echo "If results are below expectations, check:"
      echo "  1. vhost-net module loaded: lsmod | grep vhost"
      echo "  2. TAP multi_queue: ethtool -l ${serverTap}"
      echo "  3. Kernel buffers: sysctl net.core.rmem_max"
    '';
  };
}
