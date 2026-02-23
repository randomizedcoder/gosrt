# nix/testing/runner.nix
#
# Test runner for GoSRT MicroVM integration tests.
# Orchestrates VM startup, impairment application, and metrics collection.
#
# Reference: documentation/nix_microvm_design.md lines 4536-4717
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };
  configs = import ./configs.nix { inherit lib; };

  # Server IP for metrics collection
  serverIp = gosrtLib.roles.server.network.vmIp;
  publisherIp = gosrtLib.roles.publisher.network.vmIp;
  subscriberIp = gosrtLib.roles.subscriber.network.vmIp;
  metricsIp = gosrtLib.roles.metrics.network.vmIp;

in rec {
  # ─── Health Check Helper ─────────────────────────────────────────────────
  # Robust service readiness detection with exponential backoff
  waitForService = pkgs.writeShellApplication {
    name = "srt-wait-for-service";
    runtimeInputs = with pkgs; [ curl coreutils ];
    text = ''
      URL="''${1:-}"
      NAME="''${2:-service}"
      MAX_ATTEMPTS="''${3:-30}"

      if [ -z "$URL" ]; then
        echo "Usage: srt-wait-for-service <url> [name] [max_attempts]"
        exit 1
      fi

      attempt=1
      wait_time=1

      while [ $attempt -le "$MAX_ATTEMPTS" ]; do
        if curl -sf "$URL" > /dev/null 2>&1; then
          echo "  OK $NAME ready (attempt $attempt)"
          exit 0
        fi
        echo "  Waiting for $NAME... (attempt $attempt/$MAX_ATTEMPTS)"
        sleep $wait_time
        attempt=$((attempt + 1))
        # Exponential backoff, max 5 seconds
        if [ $wait_time -lt 5 ]; then
          wait_time=$((wait_time + 1))
        fi
      done

      echo "  FAIL $NAME failed to become ready after $MAX_ATTEMPTS attempts"
      exit 1
    '';
  };

  # ─── Start All VMs ───────────────────────────────────────────────────────
  # Non-interactive VM startup for automated testing
  startAll = pkgs.writeShellApplication {
    name = "srt-start-all";
    runtimeInputs = with pkgs; [ coreutils procps ];
    text = ''
      echo "Starting all GoSRT MicroVMs..."

      # Check if already running
      if pgrep -f 'gosrt:srt-' > /dev/null; then
        echo "WARNING: Some MicroVMs are already running"
        pgrep -af 'gosrt:srt-' || true
        echo "Use 'nix run .#srt-vm-stop' to stop them first"
        exit 1
      fi

      # Start server first (others connect to it)
      echo "Starting server..."
      nix run .#srt-server-vm &
      SERVER_PID=$!
      sleep 3

      # Start metrics VM (for scraping)
      echo "Starting metrics VM..."
      nix run .#srt-metrics-vm &
      METRICS_PID=$!
      sleep 2

      # Start clients in parallel
      echo "Starting publisher and subscriber..."
      nix run .#srt-publisher-vm &
      nix run .#srt-subscriber-vm &

      echo ""
      echo "All VMs started. Use 'nix run .#srt-vm-check' to verify."
      echo "Grafana: http://${metricsIp}:3000"

      # Wait for all background jobs
      wait
    '';
  };

  # ─── Test Runner ─────────────────────────────────────────────────────────
  runner = pkgs.writeShellApplication {
    name = "srt-test-runner";
    runtimeInputs = with pkgs; [ curl jq coreutils procps iproute2 ];
    text = ''
      set -euo pipefail

      CONFIG="''${1:-clean-5M}"
      DURATION="''${2:-60}"
      OUTPUT_DIR="''${3:-/tmp/srt-test-results}"

      echo ""
      echo "GoSRT Integration Test Runner"
      echo "=============================="
      echo "  Config:   $CONFIG"
      echo "  Duration: $DURATION seconds"
      echo "  Output:   $OUTPUT_DIR"
      echo ""

      mkdir -p "$OUTPUT_DIR"

      # Step 1: Verify network is set up
      echo "Step 1: Verifying network..."
      if ! ip netns list 2>/dev/null | grep -q "srt-router-a"; then
        echo "ERROR: Network not set up. Run: sudo nix run .#srt-network-setup"
        exit 1
      fi
      echo "  OK Network ready"

      # Step 2: Check if VMs are running
      echo "Step 2: Checking MicroVMs..."
      if ! pgrep -f 'gosrt:srt-srv' > /dev/null; then
        echo "  ERROR: Server VM not running"
        echo "  Start VMs with: nix run .#srt-tmux-all"
        exit 1
      fi
      echo "  OK Server running"

      # Step 3: Wait for services to be ready with exponential backoff
      echo "Step 3: Waiting for services..."
      ${waitForService}/bin/srt-wait-for-service \
        "http://${serverIp}:9100/metrics" "Server" 30
      ${waitForService}/bin/srt-wait-for-service \
        "http://${publisherIp}:9100/metrics" "Publisher" 30
      ${waitForService}/bin/srt-wait-for-service \
        "http://${subscriberIp}:9100/metrics" "Subscriber" 30

      # Step 4: Apply network impairment profile
      echo "Step 4: Applying network profile..."
      # TODO: Parse CONFIG and apply profile via srt-set-latency/srt-set-loss
      echo "  (Profile application not yet implemented)"

      # Step 5: Collect initial metrics
      echo "Step 5: Collecting baseline metrics..."
      curl -sf "http://${serverIp}:9100/metrics" > "$OUTPUT_DIR/server_baseline.txt" 2>/dev/null || true
      curl -sf "http://${publisherIp}:9100/metrics" > "$OUTPUT_DIR/publisher_baseline.txt" 2>/dev/null || true
      curl -sf "http://${subscriberIp}:9100/metrics" > "$OUTPUT_DIR/subscriber_baseline.txt" 2>/dev/null || true

      # Step 6: Run test for specified duration
      echo "Step 6: Running test for $DURATION seconds..."
      START_TIME=$(date +%s)
      END_TIME=$((START_TIME + DURATION))

      SAMPLE=0
      while [ "$(date +%s)" -lt "$END_TIME" ]; do
        SAMPLE=$((SAMPLE + 1))
        REMAINING=$((END_TIME - $(date +%s)))

        # Collect metrics periodically (every 5 seconds)
        if [ $((SAMPLE % 5)) -eq 0 ]; then
          curl -sf "http://${serverIp}:9100/metrics" >> "$OUTPUT_DIR/server_metrics.txt" 2>/dev/null || true
          curl -sf "http://${publisherIp}:9100/metrics" >> "$OUTPUT_DIR/publisher_metrics.txt" 2>/dev/null || true
          curl -sf "http://${subscriberIp}:9100/metrics" >> "$OUTPUT_DIR/subscriber_metrics.txt" 2>/dev/null || true
        fi

        printf "\r  Progress: %d/%d seconds remaining...  " "$REMAINING" "$DURATION"
        sleep 1
      done

      echo ""
      echo ""

      # Step 7: Collect final metrics
      echo "Step 7: Collecting final metrics..."
      curl -sf "http://${serverIp}:9100/metrics" > "$OUTPUT_DIR/server_final.txt" 2>/dev/null || true
      curl -sf "http://${publisherIp}:9100/metrics" > "$OUTPUT_DIR/publisher_final.txt" 2>/dev/null || true
      curl -sf "http://${subscriberIp}:9100/metrics" > "$OUTPUT_DIR/subscriber_final.txt" 2>/dev/null || true

      # Step 8: Generate summary
      echo ""
      echo "Test Complete: $CONFIG"
      echo "====================="
      echo "  Results: $OUTPUT_DIR"
      echo "  Grafana: http://${metricsIp}:3000"
      echo ""
      echo "Next steps:"
      echo "  nix run .#srt-extract-metrics -- $OUTPUT_DIR/server_final.txt"
      echo "  nix run .#srt-generate-report -- $OUTPUT_DIR"
    '';
  };

  # ─── Run Test Tier ───────────────────────────────────────────────────────
  # Run all tests in a tier sequentially
  runTier = pkgs.writeShellApplication {
    name = "srt-run-tier";
    runtimeInputs = with pkgs; [ coreutils ];
    text = ''
      TIER="''${1:-tier1}"
      OUTPUT_BASE="''${2:-/tmp/srt-test-results}"

      echo "Running test tier: $TIER"
      echo ""

      case "$TIER" in
        tier1)
          TESTS="clean-5M loss2pct-5M"
          ;;
        tier2)
          TESTS="clean-5M clean-10M regional-10M loss2pct-5M loss5pct-5M"
          ;;
        tier3)
          TESTS="clean-5M clean-10M clean-50M regional-10M continental-10M intercontinental-10M geo-5M loss2pct-5M loss5pct-5M tier3-loss-10M geo-loss-5M starlink-5M"
          ;;
        *)
          echo "Unknown tier: $TIER"
          echo "Available tiers: tier1, tier2, tier3"
          exit 1
          ;;
      esac

      TIMESTAMP=$(date +%Y%m%d_%H%M%S)
      FAILED=0
      PASSED=0

      for TEST in $TESTS; do
        echo ""
        echo "Running: $TEST"
        OUTPUT_DIR="$OUTPUT_BASE/$TIER/$TIMESTAMP/$TEST"
        mkdir -p "$OUTPUT_DIR"

        if ${runner}/bin/srt-test-runner "$TEST" 60 "$OUTPUT_DIR"; then
          PASSED=$((PASSED + 1))
          echo "  PASSED: $TEST"
        else
          FAILED=$((FAILED + 1))
          echo "  FAILED: $TEST"
        fi
      done

      echo ""
      echo "Tier $TIER Complete"
      echo "==================="
      echo "  Passed: $PASSED"
      echo "  Failed: $FAILED"
      echo "  Results: $OUTPUT_BASE/$TIER/$TIMESTAMP"
    '';
  };
}
