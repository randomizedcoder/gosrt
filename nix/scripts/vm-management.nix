# nix/scripts/vm-management.nix
#
# Data-driven VM management scripts.
# Generated from role definitions in constants.nix.
#
# Reference: documentation/nix_microvm_design.md lines 4082-4356
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # SSH options for VM connections
  sshOpts = "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR";

  # ─── Per-Role Script Generators ─────────────────────────────────────────────

  # Generate SSH script for a role
  mkSshScript = name: role: pkgs.writeShellApplication {
    name = "srt-ssh-${name}";
    runtimeInputs = with pkgs; [ openssh sshpass ];
    text = ''
      export SSHPASS="srt"
      exec sshpass -e ssh ${sshOpts} "root@${role.network.vmIp}" "$@"
    '';
  };

  # Generate console script for a role
  mkConsoleScript = name: role: pkgs.writeShellApplication {
    name = "srt-console-${name}";
    runtimeInputs = with pkgs; [ netcat-gnu ];
    text = ''
      echo "Connecting to ${name} console on port ${toString role.ports.console}..."
      echo "Press Ctrl+C to disconnect"
      exec nc localhost ${toString role.ports.console}
    '';
  };

  # Generate stop script for a role
  mkStopScript = name: role: pkgs.writeShellApplication {
    name = "srt-vm-stop-${name}";
    runtimeInputs = with pkgs; [ procps coreutils ];
    text = ''
      echo "Stopping ${name} VM..."
      pkill -f "gosrt:${role.shortName}" 2>/dev/null || true
      sleep 1
      # Force kill if still running
      pkill -9 -f "gosrt:${role.shortName}" 2>/dev/null || true
      echo "Done."
    '';
  };

  # Generate status script for a role
  mkStatusScript = name: role: pkgs.writeShellApplication {
    name = "srt-vm-status-${name}";
    runtimeInputs = with pkgs; [ procps coreutils netcat-gnu ];
    text = ''
      echo "Checking ${name} VM status..."
      echo ""

      # Check if process is running
      if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
        PROC_STATUS="RUNNING"
        PID=$(pgrep -f "gosrt:${role.shortName}" | head -1)
      else
        PROC_STATUS="stopped"
        PID="-"
      fi

      # Check if SSH is accessible
      if nc -z -w1 "${role.network.vmIp}" 22 2>/dev/null; then
        SSH_STATUS="accessible"
      else
        SSH_STATUS="not accessible"
      fi

      # Check if service port is accessible (for GoSRT VMs with prometheus)
      ${if role.service.hasPrometheus or false then ''
      if nc -z -w1 "${role.network.vmIp}" ${toString gosrtLib.ports.prometheus} 2>/dev/null; then
        SVC_STATUS="healthy"
      else
        SVC_STATUS="not responding"
      fi
      '' else ''
      SVC_STATUS="n/a"
      ''}

      printf "  %-15s %s\n" "VM:" "${name}"
      printf "  %-15s %s\n" "IP:" "${role.network.vmIp}"
      printf "  %-15s %s\n" "Process:" "$PROC_STATUS (PID: $PID)"
      printf "  %-15s %s\n" "SSH:" "$SSH_STATUS"
      printf "  %-15s %s\n" "Service:" "$SVC_STATUS"
      printf "  %-15s %s\n" "Console port:" "${toString role.ports.console}"
    '';
  };

  # Generate all per-role scripts
  sshScripts = lib.mapAttrs mkSshScript gosrtLib.roles;
  consoleScripts = lib.mapAttrs mkConsoleScript gosrtLib.roles;
  stopScripts = lib.mapAttrs mkStopScript gosrtLib.roles;
  statusScripts = lib.mapAttrs mkStatusScript gosrtLib.roles;

  # ─── Global Management Scripts ──────────────────────────────────────────────

  # Check if a single VM is running (by name argument)
  # Exit 0 if running, exit 1 if not
  vmIsRunningScript = pkgs.writeShellApplication {
    name = "srt-vm-is-running";
    runtimeInputs = with pkgs; [ procps coreutils gnugrep ];
    text = ''
      if [ $# -lt 1 ]; then
        echo "Usage: srt-vm-is-running <vm-name>"
        echo ""
        echo "VM names: ${lib.concatStringsSep ", " gosrtLib.roleNames}"
        exit 2
      fi

      VM_NAME="$1"

      # Map VM name to short name for process matching
      case "$VM_NAME" in
        ${lib.concatMapStringsSep "\n        " (name: let
          role = gosrtLib.roles.${name};
        in ''${name}) SHORT_NAME="${role.shortName}" ;;'') gosrtLib.roleNames}
        *)
          echo "Unknown VM: $VM_NAME"
          echo "Valid names: ${lib.concatStringsSep ", " gosrtLib.roleNames}"
          exit 2
          ;;
      esac

      # Check for QEMU process with matching name
      if pgrep -f "gosrt:$SHORT_NAME" >/dev/null 2>&1; then
        echo "$VM_NAME: running"
        exit 0
      else
        echo "$VM_NAME: not running"
        exit 1
      fi
    '';
  };

  # Check if all core VMs are running
  # Exit 0 if all running, exit 1 if any not running
  vmAllRunningScript = pkgs.writeShellApplication {
    name = "srt-vm-all-running";
    runtimeInputs = with pkgs; [ procps coreutils gnugrep ];
    text = ''
      FAILED=0
      RUNNING=0
      TOTAL=4

      for vm in metrics server publisher subscriber; do
        # Map VM name to short name
        case "$vm" in
          ${lib.concatMapStringsSep "\n          " (name: let
            role = gosrtLib.roles.${name};
          in ''${name}) SHORT_NAME="${role.shortName}" ;;'') [ "metrics" "server" "publisher" "subscriber" ]}
        esac

        if pgrep -f "gosrt:$SHORT_NAME" >/dev/null 2>&1; then
          printf "  %-15s %s\n" "$vm" "running"
          RUNNING=$((RUNNING + 1))
        else
          printf "  %-15s %s\n" "$vm" "NOT RUNNING"
          FAILED=$((FAILED + 1))
        fi
      done

      echo ""
      echo "Running: $RUNNING / $TOTAL"

      if [ "$FAILED" -gt 0 ]; then
        exit 1
      else
        exit 0
      fi
    '';
  };

  # Check running VMs
  vmCheckScript = pkgs.writeShellApplication {
    name = "srt-vm-check";
    runtimeInputs = with pkgs; [ procps coreutils gnugrep ];
    text = ''
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       GoSRT VM Status                                            ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""

      RUNNING=0
      TOTAL=${toString (builtins.length gosrtLib.roleNames)}

      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
          printf "  %-20s %-15s %s\n" "${name}" "${role.network.vmIp}" "RUNNING"
          RUNNING=$((RUNNING + 1))
        else
          printf "  %-20s %-15s %s\n" "${name}" "${role.network.vmIp}" "stopped"
        fi
      '') gosrtLib.roleNames}

      echo ""
      echo "Running: $RUNNING / $TOTAL VMs"
    '';
  };

  # Check running VMs (JSON output)
  vmCheckJsonScript = pkgs.writeShellApplication {
    name = "srt-vm-check-json";
    runtimeInputs = with pkgs; [ procps coreutils jq ];
    text = ''
      echo "{"
      echo "  \"vms\": {"

      FIRST=true
      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        if [ "$FIRST" = "true" ]; then
          FIRST=false
        else
          echo ","
        fi
        if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
          printf '    "%s": {"ip": "%s", "status": "running", "console_port": %d}' \
            "${name}" "${role.network.vmIp}" ${toString role.ports.console}
        else
          printf '    "%s": {"ip": "%s", "status": "stopped", "console_port": %d}' \
            "${name}" "${role.network.vmIp}" ${toString role.ports.console}
        fi
      '') gosrtLib.roleNames}

      echo ""
      echo "  }"
      echo "}"
    '';
  };

  # Stop all VMs
  vmStopAllScript = pkgs.writeShellApplication {
    name = "srt-vm-stop";
    runtimeInputs = with pkgs; [ procps coreutils ];
    text = ''
      echo "Stopping all GoSRT VMs..."

      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
          echo "  Stopping ${name}..."
          pkill -f "gosrt:${role.shortName}" 2>/dev/null || true
        fi
      '') gosrtLib.roleNames}

      sleep 2

      # Force kill any remaining
      pkill -9 -f "gosrt:srt-" 2>/dev/null || true

      echo "All VMs stopped."
    '';
  };

  # Start all VMs in tmux
  tmuxAllScript = pkgs.writeShellApplication {
    name = "srt-tmux-all";
    runtimeInputs = with pkgs; [ tmux coreutils ];
    text = ''
      SESSION="gosrt-vms"

      # Check if session exists
      if tmux has-session -t "$SESSION" 2>/dev/null; then
        echo "Session '$SESSION' already exists. Use 'srt-tmux-attach' to attach."
        exit 1
      fi

      echo "Starting all VMs in tmux session '$SESSION'..."

      # Create new session with first VM
      tmux new-session -d -s "$SESSION" -n "vms"

      # Start metrics VM first (observer pattern)
      tmux send-keys -t "$SESSION" "echo 'Starting metrics VM...' && nix run .#srt-metrics-vm" Enter
      tmux split-window -t "$SESSION"
      tmux select-layout -t "$SESSION" tiled

      # Start core VMs
      ${lib.concatMapStringsSep "\n" (name: ''
        tmux send-keys -t "$SESSION" "echo 'Starting ${name} VM...' && nix run .#srt-${name}-vm" Enter
        tmux split-window -t "$SESSION"
        tmux select-layout -t "$SESSION" tiled
      '') [ "server" "publisher" "subscriber" ]}

      # Final layout adjustment
      tmux select-layout -t "$SESSION" tiled

      echo ""
      echo "VMs starting in tmux session '$SESSION'"
      echo "  Attach with: tmux attach -t $SESSION"
      echo "  Or run: nix run .#srt-tmux-attach"
    '';
  };

  # Attach to tmux session
  tmuxAttachScript = pkgs.writeShellApplication {
    name = "srt-tmux-attach";
    runtimeInputs = with pkgs; [ tmux ];
    text = ''
      SESSION="gosrt-vms"

      if ! tmux has-session -t "$SESSION" 2>/dev/null; then
        echo "Session '$SESSION' does not exist."
        echo "Start VMs first with: nix run .#srt-tmux-all"
        exit 1
      fi

      exec tmux attach -t "$SESSION"
    '';
  };

  # Wait for VMs to be ready
  vmWaitScript = pkgs.writeShellApplication {
    name = "srt-vm-wait";
    runtimeInputs = with pkgs; [ netcat-gnu coreutils ];
    text = ''
      TIMEOUT="''${1:-120}"
      echo "Waiting for VMs to be ready (timeout: ''${TIMEOUT}s)..."

      START=$(date +%s)

      wait_for_vm() {
        local name="$1"
        local ip="$2"
        local port="$3"

        while true; do
          ELAPSED=$(($(date +%s) - START))
          if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
            echo "TIMEOUT waiting for $name"
            return 1
          fi

          if nc -z -w1 "$ip" "$port" 2>/dev/null; then
            echo "  $name ready"
            return 0
          fi

          sleep 1
        done
      }

      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        wait_for_vm "${name}" "${role.network.vmIp}" "22" || exit 1
      '') [ "server" "publisher" "subscriber" "metrics" ]}

      echo ""
      echo "All VMs ready!"
    '';
  };

  # Clear tmux session
  tmuxClearScript = pkgs.writeShellApplication {
    name = "srt-tmux-clear";
    runtimeInputs = with pkgs; [ tmux coreutils ];
    text = ''
      SESSION="gosrt-vms"

      if tmux has-session -t "$SESSION" 2>/dev/null; then
        echo "Killing tmux session '$SESSION'..."
        tmux kill-session -t "$SESSION"
        echo "Session cleared."
      else
        echo "No session '$SESSION' to clear."
      fi
    '';
  };

  # Stop all VMs and clear tmux session
  vmStopAndClearTmuxScript = pkgs.writeShellApplication {
    name = "srt-vm-stop-and-clear-tmux";
    runtimeInputs = with pkgs; [ procps tmux coreutils ];
    text = ''
      echo "Stopping all GoSRT VMs and clearing tmux session..."
      echo ""

      # Stop all VMs
      echo "=== Stopping VMs ==="
      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
          echo "  Stopping ${name}..."
          pkill -f "gosrt:${role.shortName}" 2>/dev/null || true
        fi
      '') gosrtLib.roleNames}

      sleep 2

      # Force kill any remaining
      pkill -9 -f "gosrt:" 2>/dev/null || true

      echo "All VMs stopped."
      echo ""

      # Clear tmux session
      echo "=== Clearing tmux session ==="
      SESSION="gosrt-vms"
      if tmux has-session -t "$SESSION" 2>/dev/null; then
        tmux kill-session -t "$SESSION"
        echo "Session '$SESSION' cleared."
      else
        echo "No session to clear."
      fi

      echo ""
      echo "Done. Ready for fresh start with: nix run .#srt-tmux-all"
    '';
  };

  # Restart all VMs (stop, clear tmux, start, wait)
  vmRestartScript = pkgs.writeShellApplication {
    name = "srt-vm-restart";
    runtimeInputs = with pkgs; [ procps tmux coreutils netcat-gnu ];
    text = ''
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       GoSRT VM Restart                                           ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""

      # Step 1: Stop all VMs
      echo "=== Step 1/3: Stopping VMs ==="
      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
          echo "  Stopping ${name}..."
          pkill -f "gosrt:${role.shortName}" 2>/dev/null || true
        fi
      '') gosrtLib.roleNames}

      sleep 2
      pkill -9 -f "gosrt:" 2>/dev/null || true
      echo "  All VMs stopped."
      echo ""

      # Step 2: Clear and start tmux session
      echo "=== Step 2/3: Starting VMs in tmux ==="
      SESSION="gosrt-vms"
      tmux kill-session -t "$SESSION" 2>/dev/null || true
      sleep 1

      # Create new session with server VM
      tmux new-session -d -s "$SESSION" -n vms \
        "echo 'Starting server VM...' && nix run .#srt-server-vm"

      # Split and add other VMs
      tmux split-window -t "$SESSION:vms" -h \
        "echo 'Starting publisher VM...' && nix run .#srt-publisher-vm"
      tmux split-window -t "$SESSION:vms.0" -v \
        "echo 'Starting subscriber VM...' && nix run .#srt-subscriber-vm"
      tmux split-window -t "$SESSION:vms.1" -v \
        "echo 'Starting metrics VM...' && nix run .#srt-metrics-vm"

      # Add status pane at bottom
      tmux split-window -t "$SESSION:vms" -v -l 15 \
        "echo 'VM status pane - run srt-vm-check here'; exec bash"

      tmux select-layout -t "$SESSION:vms" tiled

      echo "  tmux session '$SESSION' started."
      echo ""

      # Step 3: Wait for VMs
      echo "=== Step 3/3: Waiting for VMs ==="
      TIMEOUT=120
      START_TIME=$(date +%s)

      echo "Waiting for VMs to be ready (timeout: ''${TIMEOUT}s)..."

      wait_for_vm() {
        local name="$1"
        local ip="$2"

        while true; do
          ELAPSED=$(($(date +%s) - START_TIME))
          if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
            echo "  TIMEOUT waiting for $name"
            return 1
          fi

          if nc -z -w1 "$ip" 22 2>/dev/null; then
            echo "  $name ready"
            return 0
          fi

          sleep 1
        done
      }

      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        wait_for_vm "${name}" "${role.network.vmIp}" || exit 1
      '') [ "server" "publisher" "subscriber" "metrics" ]}

      echo ""
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  All VMs ready! Attach with: nix run .#srt-tmux-attach           ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
    '';
  };

  # Start all VMs in background (no tmux)
  vmStartBackgroundScript = pkgs.writeShellApplication {
    name = "srt-vm-start-background";
    runtimeInputs = with pkgs; [ coreutils procps ];
    text = ''
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║       GoSRT VM Background Start                                  ║"
      echo "╚══════════════════════════════════════════════════════════════════╝"
      echo ""

      LOG_DIR="''${1:-/tmp/gosrt-vms}"
      mkdir -p "$LOG_DIR"

      echo "Log directory: $LOG_DIR"
      echo ""

      # Check if any VMs are already running
      RUNNING=0
      ${lib.concatMapStringsSep "\n" (name: let
        role = gosrtLib.roles.${name};
      in ''
        if pgrep -f "gosrt:${role.shortName}" >/dev/null 2>&1; then
          echo "WARNING: ${name} VM already running"
          RUNNING=$((RUNNING + 1))
        fi
      '') [ "metrics" "server" "publisher" "subscriber" ]}

      if [ "$RUNNING" -gt 0 ]; then
        echo ""
        echo "Some VMs already running. Stop them first with: nix run .#srt-vm-stop"
        exit 1
      fi

      # Start metrics VM first (observer pattern)
      echo "Starting metrics VM..."
      nix run .#srt-metrics-vm > "$LOG_DIR/metrics.log" 2>&1 &
      echo "  PID: $! -> $LOG_DIR/metrics.log"

      # Start core VMs
      ${lib.concatMapStringsSep "\n" (name: ''
        echo "Starting ${name} VM..."
        nix run .#srt-${name}-vm > "$LOG_DIR/${name}.log" 2>&1 &
        echo "  PID: $! -> $LOG_DIR/${name}.log"
      '') [ "server" "publisher" "subscriber" ]}

      echo ""
      echo "╔══════════════════════════════════════════════════════════════════╗"
      echo "║  All VMs started in background                                   ║"
      echo "╠══════════════════════════════════════════════════════════════════╣"
      echo "║  Logs:   $LOG_DIR/*.log"
      echo "║  Status: nix run .#srt-vm-check"
      echo "║  Wait:   nix run .#srt-vm-wait"
      echo "║  Stop:   nix run .#srt-vm-stop"
      echo "╚══════════════════════════════════════════════════════════════════╝"
    '';
  };

in {
  # ─── Per-Role Scripts ─────────────────────────────────────────────────────────
  ssh = sshScripts;
  console = consoleScripts;
  stop = stopScripts;
  status = statusScripts;

  # ─── Global Scripts ───────────────────────────────────────────────────────────
  vmIsRunning = vmIsRunningScript;
  vmAllRunning = vmAllRunningScript;
  vmCheck = vmCheckScript;
  vmCheckJson = vmCheckJsonScript;
  vmStopAll = vmStopAllScript;
  vmWait = vmWaitScript;
  vmStartBackground = vmStartBackgroundScript;
  tmuxAll = tmuxAllScript;
  tmuxAttach = tmuxAttachScript;
  tmuxClear = tmuxClearScript;
  vmStopAndClearTmux = vmStopAndClearTmuxScript;
  vmRestart = vmRestartScript;

  # ─── Flattened exports for flake ──────────────────────────────────────────────
  # These are the individual script derivations
  scripts = {
    inherit vmIsRunningScript vmAllRunningScript;
    inherit vmCheckScript vmCheckJsonScript vmStopAllScript vmWaitScript vmStartBackgroundScript;
    inherit tmuxAllScript tmuxAttachScript tmuxClearScript vmStopAndClearTmuxScript vmRestartScript;
  } // sshScripts // consoleScripts // stopScripts // statusScripts;
}
