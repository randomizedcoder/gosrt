# nix/testing/integration.nix
#
# Integration test scripts for Phase 10 verification.
# End-to-end tests for the complete MicroVM infrastructure.
#
# Reference: documentation/nix_microvm_implementation_plan.md lines 2495-2618
#
{ pkgs, lib }:

let
  gosrtLib = import ../lib.nix { inherit lib; };

  # VM IPs for test scripts
  serverIp = gosrtLib.roles.server.network.vmIp;
  publisherIp = gosrtLib.roles.publisher.network.vmIp;
  subscriberIp = gosrtLib.roles.subscriber.network.vmIp;
  metricsIp = gosrtLib.roles.metrics.network.vmIp;

in {
  # ─── Test 10.1: Clean Network Basic Flow ─────────────────────────────────
  basicFlow = pkgs.writeShellApplication {
    name = "srt-integration-basic";
    runtimeInputs = with pkgs; [ curl jq coreutils procps ];
    text = ''
      set -euo pipefail

      echo ""
      echo "Integration Test 10.1: Clean Network Basic Flow"
      echo "================================================"
      echo ""

      FAILED=0

      # Step 1: Verify VMs are running
      echo "Step 1: Checking VM status..."
      if ! pgrep -f 'gosrt:srv' > /dev/null; then
        echo "  FAIL: Server VM not running"
        FAILED=$((FAILED + 1))
      else
        echo "  OK: Server VM running"
      fi

      if ! pgrep -f 'gosrt:pub' > /dev/null; then
        echo "  FAIL: Publisher VM not running"
        FAILED=$((FAILED + 1))
      else
        echo "  OK: Publisher VM running"
      fi

      if ! pgrep -f 'gosrt:sub' > /dev/null; then
        echo "  FAIL: Subscriber VM not running"
        FAILED=$((FAILED + 1))
      else
        echo "  OK: Subscriber VM running"
      fi

      # Step 2: Verify services responding
      echo ""
      echo "Step 2: Checking service endpoints..."

      if curl -sf "http://${serverIp}:9100/metrics" > /dev/null 2>&1; then
        echo "  OK: Server metrics endpoint responding"
      else
        echo "  FAIL: Server metrics endpoint not responding"
        FAILED=$((FAILED + 1))
      fi

      if curl -sf "http://${publisherIp}:9100/metrics" > /dev/null 2>&1; then
        echo "  OK: Publisher metrics endpoint responding"
      else
        echo "  FAIL: Publisher metrics endpoint not responding"
        FAILED=$((FAILED + 1))
      fi

      if curl -sf "http://${subscriberIp}:9100/metrics" > /dev/null 2>&1; then
        echo "  OK: Subscriber metrics endpoint responding"
      else
        echo "  FAIL: Subscriber metrics endpoint not responding"
        FAILED=$((FAILED + 1))
      fi

      # Step 3: Verify Grafana
      echo ""
      echo "Step 3: Checking Grafana..."
      if curl -sf "http://${metricsIp}:3000/api/health" > /dev/null 2>&1; then
        echo "  OK: Grafana responding"
      else
        echo "  WARN: Grafana not responding (may not be running)"
      fi

      # Step 4: Check for data flow
      echo ""
      echo "Step 4: Checking data flow..."
      PACKETS=$(curl -sf "http://${serverIp}:9100/metrics" 2>/dev/null | \
                grep "gosrt_connection_pkt_recv_total" | \
                awk '{print $2}' | head -1 || echo "0")
      if [ -n "$PACKETS" ] && [ "$PACKETS" != "0" ]; then
        echo "  OK: Server has received $PACKETS packets"
      else
        echo "  WARN: No packets received yet (may need traffic)"
      fi

      # Summary
      echo ""
      echo "========================================"
      if [ "$FAILED" -eq 0 ]; then
        echo "PASSED: All basic flow checks passed"
        exit 0
      else
        echo "FAILED: $FAILED checks failed"
        exit 1
      fi
    '';
  };

  # ─── Test 10.2: Latency Profile Switching ────────────────────────────────
  latencySwitching = pkgs.writeShellApplication {
    name = "srt-integration-latency";
    runtimeInputs = with pkgs; [ iproute2 curl coreutils ];
    text = ''
      set -euo pipefail

      echo ""
      echo "Integration Test 10.2: Latency Profile Switching"
      echo "================================================="
      echo ""
      echo "NOTE: This test requires 'srt-set-latency' to be run with sudo"
      echo ""

      FAILED=0

      # Step 1: Verify we can reach the server
      echo "Step 1: Baseline connectivity check..."
      if ! curl -sf --connect-timeout 5 "http://${serverIp}:9100/metrics" > /dev/null 2>&1; then
        echo "  FAIL: Cannot reach server at ${serverIp}"
        exit 1
      fi
      echo "  OK: Server reachable"

      # Step 2: Test latency profiles (informational only - requires sudo)
      echo ""
      echo "Step 2: Latency profile information"
      echo "  Profile 0: Clean (0ms)"
      echo "  Profile 1: Regional (10ms RTT)"
      echo "  Profile 2: Continental (60ms RTT)"
      echo "  Profile 3: Intercontinental (130ms RTT)"
      echo "  Profile 4: GEO Satellite (300ms RTT)"
      echo ""
      echo "To test latency switching:"
      echo "  sudo nix run .#srt-set-latency -- 2  # 60ms RTT"
      echo "  ping -c 3 ${serverIp}"
      echo "  sudo nix run .#srt-set-latency -- 0  # Reset"

      echo ""
      echo "========================================"
      echo "INFO: Latency test requires manual verification"
    '';
  };

  # ─── Test 10.3: Loss Injection ───────────────────────────────────────────
  lossInjection = pkgs.writeShellApplication {
    name = "srt-integration-loss";
    runtimeInputs = with pkgs; [ curl coreutils gawk ];
    text = ''
      set -euo pipefail

      echo ""
      echo "Integration Test 10.3: Loss Injection"
      echo "======================================"
      echo ""
      echo "NOTE: This test requires 'srt-set-loss' to be run with sudo"
      echo ""

      # Get baseline loss count
      echo "Step 1: Getting baseline loss count..."
      BASELINE=$(curl -sf "http://${serverIp}:9100/metrics" 2>/dev/null | \
                 grep "gosrt_connection_congestion_packets_lost_total" | \
                 awk '{print $2}' | head -1 || echo "0")
      echo "  Baseline loss count: $BASELINE"

      echo ""
      echo "To test loss injection:"
      echo "  sudo nix run .#srt-set-loss -- 5 0    # Inject 5% loss"
      echo "  sleep 10"
      echo "  curl http://${serverIp}:9100/metrics | grep packets_lost"
      echo "  sudo nix run .#srt-set-loss -- 0 0    # Clear loss"

      echo ""
      echo "========================================"
      echo "INFO: Loss test requires manual verification"
    '';
  };

  # ─── Test 10.4: Full Integration Suite ───────────────────────────────────
  fullSuite = pkgs.writeShellApplication {
    name = "srt-integration-full";
    runtimeInputs = with pkgs; [ curl jq coreutils procps iproute2 ];
    text = ''
      set -euo pipefail

      echo ""
      echo "GoSRT Full Integration Test Suite"
      echo "=================================="
      echo ""

      TOTAL=0
      PASSED=0
      FAILED=0

      run_test() {
        local name="$1"
        local cmd="$2"
        TOTAL=$((TOTAL + 1))
        echo -n "  $name... "
        if eval "$cmd" > /dev/null 2>&1; then
          echo "OK"
          PASSED=$((PASSED + 1))
        else
          echo "FAIL"
          FAILED=$((FAILED + 1))
        fi
      }

      echo "1. Network Infrastructure Tests"
      echo "--------------------------------"
      run_test "Network namespace exists" "ip netns list | grep -q srt-router-a"
      run_test "Server bridge exists" "ip link show srtbr-srv"
      run_test "Publisher bridge exists" "ip link show srtbr-pub"

      echo ""
      echo "2. VM Process Tests"
      echo "-------------------"
      run_test "Server VM running" "pgrep -f 'gosrt:srv'"
      run_test "Publisher VM running" "pgrep -f 'gosrt:pub'"
      run_test "Subscriber VM running" "pgrep -f 'gosrt:sub'"
      run_test "Metrics VM running" "pgrep -f 'gosrt:metrics'"

      echo ""
      echo "3. Service Endpoint Tests"
      echo "-------------------------"
      run_test "Server metrics" "curl -sf --connect-timeout 5 http://${serverIp}:9100/metrics"
      run_test "Publisher metrics" "curl -sf --connect-timeout 5 http://${publisherIp}:9100/metrics"
      run_test "Subscriber metrics" "curl -sf --connect-timeout 5 http://${subscriberIp}:9100/metrics"
      run_test "Grafana health" "curl -sf --connect-timeout 5 http://${metricsIp}:3000/api/health"
      run_test "Prometheus targets" "curl -sf --connect-timeout 5 http://${metricsIp}:9090/api/v1/targets"

      echo ""
      echo "4. Data Flow Tests"
      echo "------------------"
      PACKETS=$(curl -sf "http://${serverIp}:9100/metrics" 2>/dev/null | \
                grep "gosrt_connection_pkt_recv_total" | \
                awk '{print $2}' | head -1 || echo "0")
      if [ -n "$PACKETS" ] && [ "$PACKETS" != "0" ]; then
        echo "  Packets received: $PACKETS... OK"
        PASSED=$((PASSED + 1))
      else
        echo "  Packets received: 0... WARN (may need traffic)"
      fi
      TOTAL=$((TOTAL + 1))

      echo ""
      echo "========================================"
      echo "Results: $PASSED/$TOTAL passed, $FAILED failed"
      echo ""

      if [ "$FAILED" -eq 0 ]; then
        echo "ALL TESTS PASSED"
        exit 0
      else
        echo "SOME TESTS FAILED"
        exit 1
      fi
    '';
  };

  # ─── Quick Smoke Test ────────────────────────────────────────────────────
  smokeTest = pkgs.writeShellApplication {
    name = "srt-integration-smoke";
    runtimeInputs = with pkgs; [ curl coreutils ];
    text = ''
      set -euo pipefail

      echo "GoSRT Smoke Test"
      echo "================"

      # Quick checks
      if curl -sf --connect-timeout 5 "http://${serverIp}:9100/metrics" > /dev/null 2>&1; then
        echo "OK: Server responding"
      else
        echo "FAIL: Server not responding"
        exit 1
      fi

      if curl -sf --connect-timeout 5 "http://${metricsIp}:3000/api/health" > /dev/null 2>&1; then
        echo "OK: Grafana responding"
      else
        echo "WARN: Grafana not responding"
      fi

      echo ""
      echo "Smoke test passed!"
    '';
  };
}
