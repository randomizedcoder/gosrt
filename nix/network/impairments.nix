# nix/network/impairments.nix
#
# Impairment application scripts.
# Modifies tc netem rules and pushes Grafana annotations.
#
# Reference: documentation/nix_microvm_design.md lines 3410-3578
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  profiles = import ./profiles.nix { inherit lib; };
  c = import ../constants.nix;
  metricsIp = gosrtLib.roles.metrics.network.vmIp;

in rec {
  # ─── Set Latency Script ─────────────────────────────────────────────────────
  # Changes which inter-router link is used (switches latency profile)
  setLatencyScript = pkgs.writeShellApplication {
    name = "srt-set-latency";
    runtimeInputs = with pkgs; [ iproute2 coreutils curl ];
    text = ''
      PROFILE_INDEX="''${1:-0}"

      echo "Setting latency profile index: $PROFILE_INDEX"

      # Get the link IPs for this profile
      case "$PROFILE_INDEX" in
        0) LINK_NAME="no-delay"; RTT=0 ;;
        1) LINK_NAME="regional-dc"; RTT=10 ;;
        2) LINK_NAME="cross-continental"; RTT=60 ;;
        3) LINK_NAME="intercontinental"; RTT=130 ;;
        4) LINK_NAME="geo-satellite"; RTT=300 ;;
        *)
          echo "ERROR: Unknown profile index $PROFILE_INDEX"
          exit 1
          ;;
      esac

      echo "Switching to: $LINK_NAME (RTT: ''${RTT}ms)"

      # Update routes in both router namespaces
      # Routes go via the selected inter-router link
      LINK_SUBNET="${c.base.subnetPrefix}.$((${toString c.base.interRouterBase} + PROFILE_INDEX))"

      # Router A routes to Router B subnets via selected link
      for subnet in 3 8; do  # server (3), metrics (8) are on Router B
        sudo ip netns exec ${gosrtLib.routerA} ip route replace "${c.base.subnetPrefix}.$subnet.0/24" \
          via "$LINK_SUBNET.2" 2>/dev/null || true
      done

      # Router B routes to Router A subnets via selected link
      for subnet in 1 2 4 5 6 7; do  # publisher, subscriber, xtransmit-*, ffmpeg-* on Router A
        sudo ip netns exec ${gosrtLib.routerB} ip route replace "${c.base.subnetPrefix}.$subnet.0/24" \
          via "$LINK_SUBNET.1" 2>/dev/null || true
      done

      # Push annotation (ignore failures - Grafana may not be running)
      curl -s -X POST \
        -u "admin:srt" \
        -H "Content-Type: application/json" \
        -d "{\"text\": \"Latency: $LINK_NAME (''${RTT}ms)\", \"tags\": [\"latency\", \"$LINK_NAME\"]}" \
        "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

      echo "Latency profile set to: $LINK_NAME"
    '';
  };

  # ─── Set Loss Script ────────────────────────────────────────────────────────
  # Applies packet loss to inter-router links
  setLossScript = pkgs.writeShellApplication {
    name = "srt-set-loss";
    runtimeInputs = with pkgs; [ iproute2 coreutils curl ];
    text = ''
      LOSS_PERCENT="''${1:-0}"
      LINK_INDEX="''${2:-0}"  # Which inter-router link to modify

      echo "Setting loss: ''${LOSS_PERCENT}% on link index $LINK_INDEX"

      VETH_A="link''${LINK_INDEX}_a"
      VETH_B="link''${LINK_INDEX}_b"

      # Apply loss while preserving existing latency
      # We need to get current delay first, then reapply with loss
      if [ "$LOSS_PERCENT" = "0" ]; then
        # Remove loss - just apply delay
        sudo ip netns exec ${gosrtLib.routerA} tc qdisc change dev "$VETH_A" root netem \
          limit ${toString c.netem.queueLimit} 2>/dev/null || true
        sudo ip netns exec ${gosrtLib.routerB} tc qdisc change dev "$VETH_B" root netem \
          limit ${toString c.netem.queueLimit} 2>/dev/null || true
      else
        # Apply loss
        sudo ip netns exec ${gosrtLib.routerA} tc qdisc change dev "$VETH_A" root netem \
          loss "''${LOSS_PERCENT}%" limit ${toString c.netem.queueLimit} 2>/dev/null || true
        sudo ip netns exec ${gosrtLib.routerB} tc qdisc change dev "$VETH_B" root netem \
          loss "''${LOSS_PERCENT}%" limit ${toString c.netem.queueLimit} 2>/dev/null || true
      fi

      # Push annotation
      curl -s -X POST \
        -u "admin:srt" \
        -H "Content-Type: application/json" \
        -d "{\"text\": \"Loss: ''${LOSS_PERCENT}%\", \"tags\": [\"loss\", \"impairment\"]}" \
        "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

      echo "Loss set to: ''${LOSS_PERCENT}%"
    '';
  };

  # ─── Starlink Pattern Script ────────────────────────────────────────────────
  # Simulates Starlink handoff blackouts using blackhole routes
  starlinkPatternScript = pkgs.writeShellApplication {
    name = "srt-starlink-pattern";
    runtimeInputs = with pkgs; [ iproute2 coreutils curl ];
    text = ''
      DURATION="''${1:-60}"        # Total duration in seconds
      BLACKOUT_MS="''${2:-500}"    # Blackout duration in ms
      INTERVAL="''${3:-15}"        # Seconds between blackouts

      echo "Starting Starlink pattern simulation..."
      echo "  Duration: ''${DURATION}s"
      echo "  Blackout: ''${BLACKOUT_MS}ms every ''${INTERVAL}s"

      # Convert ms to seconds for sleep
      BLACKOUT_SEC=$(echo "scale=3; $BLACKOUT_MS / 1000" | bc)

      START_TIME=$(date +%s)
      END_TIME=$((START_TIME + DURATION))

      BLACKOUT_COUNT=0

      while [ "$(date +%s)" -lt "$END_TIME" ]; do
        BLACKOUT_COUNT=$((BLACKOUT_COUNT + 1))
        CURRENT_TIME=$(date +%s)
        ELAPSED=$((CURRENT_TIME - START_TIME))

        echo "[''${ELAPSED}s] Blackout #$BLACKOUT_COUNT starting..."

        # Push annotation for blackout start
        curl -s -X POST \
          -u "admin:srt" \
          -H "Content-Type: application/json" \
          -d "{\"text\": \"Starlink blackout #$BLACKOUT_COUNT START\", \"tags\": [\"starlink\", \"blackout\", \"start\"]}" \
          "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

        # Add blackhole routes (drops all traffic)
        for subnet in 1 2 4 5 6 7; do
          sudo ip netns exec ${gosrtLib.routerB} ip route add blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
        for subnet in 3 8; do
          sudo ip netns exec ${gosrtLib.routerA} ip route add blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done

        # Wait for blackout duration
        sleep "$BLACKOUT_SEC"

        # Remove blackhole routes (restore traffic)
        for subnet in 1 2 4 5 6 7; do
          sudo ip netns exec ${gosrtLib.routerB} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
        for subnet in 3 8; do
          sudo ip netns exec ${gosrtLib.routerA} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done

        echo "[''${ELAPSED}s] Blackout #$BLACKOUT_COUNT ended"

        # Push annotation for blackout end
        curl -s -X POST \
          -u "admin:srt" \
          -H "Content-Type: application/json" \
          -d "{\"text\": \"Starlink blackout #$BLACKOUT_COUNT END\", \"tags\": [\"starlink\", \"blackout\", \"end\"]}" \
          "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

        # Wait until next interval
        if [ "$(date +%s)" -lt "$END_TIME" ]; then
          sleep "$INTERVAL"
        fi
      done

      echo "Starlink pattern simulation complete ($BLACKOUT_COUNT blackouts)"
    '';
  };

  # ─── Starlink Reconvergence Script ─────────────────────────────────────────
  # Advanced Starlink simulation with minute-based patterns and baseline latency.
  # More realistic than starlinkPatternScript - simulates actual handoff behavior.
  starlinkReconvergenceScript = pkgs.writeShellApplication {
    name = "srt-starlink-reconvergence";
    runtimeInputs = with pkgs; [ iproute2 coreutils curl bc gnused ];
    text = ''
      # Default values from constants.nix
      DURATION=${toString c.starlink.defaultDurationSeconds}
      PATTERN_MODE="minute"
      BLACKOUT_MS=${toString c.starlink.blackoutMs}
      INTERVAL=${toString c.starlink.defaultInterval}
      TIMES="${lib.concatMapStringsSep " " toString c.starlink.minuteTimes}"
      BASELINE_DELAY=${toString c.starlink.baselineDelayMs}
      BASELINE_JITTER=${toString c.starlink.baselineJitterMs}
      DRY_RUN=false

      # Parse arguments
      while [[ $# -gt 0 ]]; do
        case "$1" in
          --duration)
            DURATION="$2"
            shift 2
            ;;
          --pattern)
            PATTERN_MODE="$2"
            shift 2
            ;;
          --blackout-ms)
            BLACKOUT_MS="$2"
            shift 2
            ;;
          --interval)
            INTERVAL="$2"
            shift 2
            ;;
          --times)
            TIMES="$2"
            shift 2
            ;;
          --baseline-delay)
            BASELINE_DELAY="$2"
            shift 2
            ;;
          --baseline-jitter)
            BASELINE_JITTER="$2"
            shift 2
            ;;
          --dry-run)
            DRY_RUN=true
            shift
            ;;
          --help|-h)
            echo "Usage: srt-starlink-reconvergence [OPTIONS]"
            echo ""
            echo "Simulates Starlink satellite handoff with realistic blackout patterns."
            echo ""
            echo "Options:"
            echo "  --duration <seconds>     Total simulation duration (default: ${toString c.starlink.defaultDurationSeconds})"
            echo "  --pattern <mode>         Pattern mode: 'minute' or 'interval' (default: minute)"
            echo "  --blackout-ms <ms>       Blackout duration (default: ${toString c.starlink.blackoutMs})"
            echo "  --interval <seconds>     Interval between blackouts (interval mode only, default: ${toString c.starlink.defaultInterval})"
            echo "  --times <list>           Seconds within minute (minute mode only, default: \"${lib.concatMapStringsSep " " toString c.starlink.minuteTimes}\")"
            echo "  --baseline-delay <ms>    Baseline latency (default: ${toString c.starlink.baselineDelayMs})"
            echo "  --baseline-jitter <ms>   Baseline jitter (default: ${toString c.starlink.baselineJitterMs})"
            echo "  --dry-run                Print what would happen, don't execute"
            echo "  --help                   Show this help"
            exit 0
            ;;
          *)
            echo "Unknown option: $1"
            exit 1
            ;;
        esac
      done

      echo "=== Starlink Reconvergence Simulation ==="
      echo "  Duration: ''${DURATION}s"
      echo "  Pattern mode: $PATTERN_MODE"
      echo "  Blackout: ''${BLACKOUT_MS}ms"
      if [ "$PATTERN_MODE" = "minute" ]; then
        echo "  Trigger times: $TIMES (seconds within minute)"
      else
        echo "  Interval: ''${INTERVAL}s"
      fi
      echo "  Baseline: ''${BASELINE_DELAY}ms delay, ''${BASELINE_JITTER}ms jitter"
      echo ""

      if [ "$DRY_RUN" = "true" ]; then
        echo "[DRY RUN] Would apply:"
        echo "  - Baseline netem: delay ''${BASELINE_DELAY}ms ''${BASELINE_JITTER}ms on inter-router links"
        if [ "$PATTERN_MODE" = "minute" ]; then
          echo "  - Blackouts at seconds $TIMES of each minute"
        else
          echo "  - Blackouts every ''${INTERVAL}s"
        fi
        echo "  - Each blackout lasts ''${BLACKOUT_MS}ms"
        echo ""
        echo "[DRY RUN] Example blackout events for first 60 seconds:"
        if [ "$PATTERN_MODE" = "minute" ]; then
          for T in $TIMES; do
            if [ "$T" -lt 60 ]; then
              echo "  - t=''${T}s: blackout for ''${BLACKOUT_MS}ms"
            fi
          done
        else
          T=0
          while [ "$T" -lt 60 ] && [ "$T" -lt "$DURATION" ]; do
            echo "  - t=''${T}s: blackout for ''${BLACKOUT_MS}ms"
            T=$((T + INTERVAL))
          done
        fi
        exit 0
      fi

      # Convert ms to seconds for sleep
      BLACKOUT_SEC=$(echo "scale=3; $BLACKOUT_MS / 1000" | bc)

      # Apply baseline netem (delay + jitter) to inter-router links
      echo "Applying baseline netem: ''${BASELINE_DELAY}ms delay, ''${BASELINE_JITTER}ms jitter..."
      for i in 0 1 2 3 4; do
        VETH_A="link''${i}_a"
        VETH_B="link''${i}_b"
        sudo ip netns exec ${gosrtLib.routerA} tc qdisc replace dev "$VETH_A" root netem \
          delay "''${BASELINE_DELAY}ms" "''${BASELINE_JITTER}ms" limit ${toString c.netem.queueLimit} 2>/dev/null || true
        sudo ip netns exec ${gosrtLib.routerB} tc qdisc replace dev "$VETH_B" root netem \
          delay "''${BASELINE_DELAY}ms" "''${BASELINE_JITTER}ms" limit ${toString c.netem.queueLimit} 2>/dev/null || true
      done

      # Push start annotation
      curl -s -X POST \
        -u "admin:srt" \
        -H "Content-Type: application/json" \
        -d '{"text": "Starlink reconvergence simulation started ('"$PATTERN_MODE"' mode)", "tags": ["starlink", "reconvergence", "start"]}' \
        "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

      START_TIME=$(date +%s)
      END_TIME=$((START_TIME + DURATION))
      BLACKOUT_COUNT=0
      LAST_TRIGGER_SEC=-1

      cleanup() {
        echo ""
        echo "Cleaning up..."
        # Remove blackhole routes
        for subnet in 1 2 4 5 6 7; do
          sudo ip netns exec ${gosrtLib.routerB} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
        for subnet in 3 8; do
          sudo ip netns exec ${gosrtLib.routerA} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
        # Remove baseline netem
        for i in 0 1 2 3 4; do
          VETH_A="link''${i}_a"
          VETH_B="link''${i}_b"
          sudo ip netns exec ${gosrtLib.routerA} tc qdisc del dev "$VETH_A" root 2>/dev/null || true
          sudo ip netns exec ${gosrtLib.routerB} tc qdisc del dev "$VETH_B" root 2>/dev/null || true
        done
        # Push end annotation
        curl -s -X POST \
          -u "admin:srt" \
          -H "Content-Type: application/json" \
          -d '{"text": "Starlink reconvergence simulation ended ('"$BLACKOUT_COUNT"' blackouts)", "tags": ["starlink", "reconvergence", "end"]}' \
          "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true
        echo "Cleanup complete."
      }

      trap cleanup EXIT

      trigger_blackout() {
        BLACKOUT_COUNT=$((BLACKOUT_COUNT + 1))
        CURRENT_TIME=$(date +%s)
        ELAPSED=$((CURRENT_TIME - START_TIME))
        echo "[''${ELAPSED}s] Blackout #$BLACKOUT_COUNT (''${BLACKOUT_MS}ms)..."

        # Push blackout annotation
        curl -s -X POST \
          -u "admin:srt" \
          -H "Content-Type: application/json" \
          -d '{"text": "Starlink blackout #'"$BLACKOUT_COUNT"' ('"$BLACKOUT_MS"'ms)", "tags": ["starlink", "reconvergence", "blackout"]}' \
          "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

        # Add blackhole routes
        for subnet in 1 2 4 5 6 7; do
          sudo ip netns exec ${gosrtLib.routerB} ip route add blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
        for subnet in 3 8; do
          sudo ip netns exec ${gosrtLib.routerA} ip route add blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done

        # Wait for blackout duration
        sleep "$BLACKOUT_SEC"

        # Remove blackhole routes
        for subnet in 1 2 4 5 6 7; do
          sudo ip netns exec ${gosrtLib.routerB} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
        for subnet in 3 8; do
          sudo ip netns exec ${gosrtLib.routerA} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
        done
      }

      echo "Starting simulation (Ctrl+C to stop early)..."

      if [ "$PATTERN_MODE" = "minute" ]; then
        # Minute-based pattern: trigger at specific seconds within each minute
        while [ "$(date +%s)" -lt "$END_TIME" ]; do
          CURRENT_SEC=$(date +%S | sed 's/^0//')  # Remove leading zero

          for T in $TIMES; do
            if [ "$CURRENT_SEC" = "$T" ] && [ "$LAST_TRIGGER_SEC" != "$T" ]; then
              LAST_TRIGGER_SEC="$T"
              trigger_blackout
            fi
          done

          # Reset trigger tracking on new minute
          if [ "$CURRENT_SEC" = "0" ]; then
            LAST_TRIGGER_SEC=-1
          fi

          sleep 0.2
        done
      else
        # Interval-based pattern: trigger every N seconds
        NEXT_TRIGGER=$START_TIME
        while [ "$(date +%s)" -lt "$END_TIME" ]; do
          CURRENT=$(date +%s)
          if [ "$CURRENT" -ge "$NEXT_TRIGGER" ]; then
            trigger_blackout
            NEXT_TRIGGER=$((CURRENT + INTERVAL))
          fi
          sleep 0.2
        done
      fi

      echo ""
      echo "=== Simulation Complete ==="
      echo "  Total blackouts: $BLACKOUT_COUNT"
      echo "  Duration: ''${DURATION}s"
    '';
  };

  # ─── Starlink Background Control Scripts ───────────────────────────────────
  # Start/stop/status scripts for running simulation in background

  starlinkStartScript = pkgs.writeShellApplication {
    name = "srt-starlink-start";
    runtimeInputs = with pkgs; [ coreutils procps ];
    text = ''
      PID_FILE="/tmp/srt-starlink-reconvergence.pid"
      LOG_FILE="/tmp/srt-starlink-reconvergence.log"

      # Check if already running
      if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
          echo "Starlink simulation already running (PID: $PID)"
          echo "Use 'srt-starlink-stop' to stop it first"
          exit 1
        else
          rm -f "$PID_FILE"
        fi
      fi

      echo "Starting Starlink simulation in background..."
      echo "  Arguments: $*"
      echo "  Log file: $LOG_FILE"
      echo ""

      # Start in background, redirecting output to log
      nohup ${starlinkReconvergenceScript}/bin/srt-starlink-reconvergence "$@" > "$LOG_FILE" 2>&1 &
      BG_PID=$!

      echo "$BG_PID" > "$PID_FILE"
      echo "Started with PID: $BG_PID"
      echo "Use 'srt-starlink-status' to check progress"
      echo "Use 'srt-starlink-stop' to stop"
    '';
  };

  starlinkStopScript = pkgs.writeShellApplication {
    name = "srt-starlink-stop";
    runtimeInputs = with pkgs; [ coreutils procps iproute2 curl ];
    text = ''
      PID_FILE="/tmp/srt-starlink-reconvergence.pid"

      if [ ! -f "$PID_FILE" ]; then
        echo "No running simulation found (no PID file)"
        exit 0
      fi

      PID=$(cat "$PID_FILE")
      echo "Stopping Starlink simulation (PID: $PID)..."

      if kill -0 "$PID" 2>/dev/null; then
        # Send SIGTERM to trigger cleanup
        kill "$PID" 2>/dev/null || true
        sleep 1
        # Force kill if still running
        if kill -0 "$PID" 2>/dev/null; then
          kill -9 "$PID" 2>/dev/null || true
        fi
        echo "Process stopped"
      else
        echo "Process was not running"
      fi

      rm -f "$PID_FILE"

      # Ensure cleanup (in case process didn't clean up properly)
      echo "Ensuring cleanup..."
      # Remove any leftover blackhole routes
      for subnet in 1 2 4 5 6 7; do
        sudo ip netns exec ${gosrtLib.routerB} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
      done
      for subnet in 3 8; do
        sudo ip netns exec ${gosrtLib.routerA} ip route del blackhole "${c.base.subnetPrefix}.$subnet.0/24" 2>/dev/null || true
      done
      # Remove baseline netem from inter-router links
      for i in 0 1 2 3 4; do
        VETH_A="link''${i}_a"
        VETH_B="link''${i}_b"
        sudo ip netns exec ${gosrtLib.routerA} tc qdisc del dev "$VETH_A" root 2>/dev/null || true
        sudo ip netns exec ${gosrtLib.routerB} tc qdisc del dev "$VETH_B" root 2>/dev/null || true
      done

      # Push stop annotation
      curl -s -X POST \
        -u "admin:srt" \
        -H "Content-Type: application/json" \
        -d '{"text": "Starlink simulation stopped manually", "tags": ["starlink", "reconvergence", "stopped"]}' \
        "http://${metricsIp}:3000/api/annotations" 2>/dev/null || true

      echo "Done"
    '';
  };

  starlinkStatusScript = pkgs.writeShellApplication {
    name = "srt-starlink-status";
    runtimeInputs = with pkgs; [ coreutils procps gnugrep ];
    text = ''
      PID_FILE="/tmp/srt-starlink-reconvergence.pid"
      LOG_FILE="/tmp/srt-starlink-reconvergence.log"

      echo "=== Starlink Simulation Status ==="
      echo ""

      if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
          echo "Status: RUNNING"
          echo "PID: $PID"
          echo ""
          if [ -f "$LOG_FILE" ]; then
            echo "Recent log output:"
            echo "─────────────────────────────────────"
            tail -15 "$LOG_FILE"
            echo "─────────────────────────────────────"
            echo ""
            # Extract blackout count from log
            BLACKOUT_COUNT=$(grep -c "Blackout #" "$LOG_FILE" 2>/dev/null || echo "0")
            echo "Blackouts triggered so far: $BLACKOUT_COUNT"
          fi
        else
          echo "Status: STOPPED (stale PID file)"
          rm -f "$PID_FILE"
        fi
      else
        echo "Status: NOT RUNNING"
      fi

      echo ""
      if [ -f "$LOG_FILE" ]; then
        echo "Full log available at: $LOG_FILE"
      fi
    '';
  };
}
