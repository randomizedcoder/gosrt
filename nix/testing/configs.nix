# nix/testing/configs.nix
#
# Test configurations for GoSRT integration testing.
# Maps to the test matrix in contrib/integration_testing/test_configs.go
#
# Reference: documentation/nix_microvm_design.md lines 4397-4533
#
{ lib }:

let
  # Import profiles for reference
  profiles = import ../network/profiles.nix { inherit lib; };

in {
  # ═══════════════════════════════════════════════════════════════════════════
  # Clean Network Tests (baseline performance)
  # ═══════════════════════════════════════════════════════════════════════════

  clean-5M = {
    name = "Clean-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    profile = profiles.scenarios.clean;
    description = "Clean network at 5 Mb/s";
  };

  clean-10M = {
    name = "Clean-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    profile = profiles.scenarios.clean;
    description = "Clean network at 10 Mb/s";
  };

  clean-50M = {
    name = "Clean-50M";
    bitrateMbps = 50;
    durationSeconds = 60;
    profile = profiles.scenarios.clean;
    description = "Clean network at 50 Mb/s";
  };

  clean-100M = {
    name = "Clean-100M";
    bitrateMbps = 100;
    durationSeconds = 60;
    profile = profiles.scenarios.clean;
    description = "Clean network at 100 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Latency Tests
  # ═══════════════════════════════════════════════════════════════════════════

  regional-10M = {
    name = "Regional-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    latencyProfile = profiles.latency.regional;
    description = "Regional DC (10ms RTT) at 10 Mb/s";
  };

  continental-10M = {
    name = "Continental-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    latencyProfile = profiles.latency.continental;
    description = "Cross-continental (60ms RTT) at 10 Mb/s";
  };

  intercontinental-10M = {
    name = "Intercontinental-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    latencyProfile = profiles.latency.intercontinental;
    description = "Intercontinental (130ms RTT) at 10 Mb/s";
  };

  geo-5M = {
    name = "GEO-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    latencyProfile = profiles.latency.satellite;
    description = "GEO satellite (300ms RTT) at 5 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Loss Tests
  # ═══════════════════════════════════════════════════════════════════════════

  loss2pct-5M = {
    name = "Loss-2pct-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    lossProfile = profiles.loss.heavy;  # 2%
    description = "2% packet loss at 5 Mb/s";
  };

  loss5pct-5M = {
    name = "Loss-5pct-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    lossProfile = profiles.loss.severe;  # 5%
    description = "5% packet loss at 5 Mb/s";
  };

  loss2pct-10M = {
    name = "Loss-2pct-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    lossProfile = profiles.loss.heavy;  # 2%
    description = "2% packet loss at 10 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Combined Stress Tests
  # ═══════════════════════════════════════════════════════════════════════════

  tier3-loss-10M = {
    name = "Tier3-Loss-10M";
    bitrateMbps = 10;
    durationSeconds = 60;
    latencyProfile = profiles.latency.intercontinental;
    lossProfile = profiles.loss.heavy;  # 2%
    description = "Tier 3 stress: 130ms RTT + 2% loss at 10 Mb/s";
  };

  geo-loss-5M = {
    name = "GEO-Loss-5M";
    bitrateMbps = 5;
    durationSeconds = 60;
    latencyProfile = profiles.latency.satellite;
    lossProfile = profiles.loss.light;  # 0.1% ~ 0.5%
    description = "GEO satellite (300ms) + 0.5% loss at 5 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Starlink Pattern Tests
  # ═══════════════════════════════════════════════════════════════════════════

  starlink-5M = {
    name = "Starlink-5M";
    bitrateMbps = 5;
    durationSeconds = 120;  # Need full minute for pattern
    profile = profiles.scenarios.starlink;
    starlinkPattern = true;
    description = "Starlink with handoff events at 5 Mb/s";
  };

  starlink-10M = {
    name = "Starlink-10M";
    bitrateMbps = 10;
    durationSeconds = 120;
    profile = profiles.scenarios.starlink;
    starlinkPattern = true;
    description = "Starlink with handoff events at 10 Mb/s";
  };

  # ═══════════════════════════════════════════════════════════════════════════
  # Test Tiers (for CI)
  # ═══════════════════════════════════════════════════════════════════════════

  # Tier 1: Quick sanity check (~2 min)
  tier1 = [ "clean-5M" "loss2pct-5M" ];

  # Tier 2: Core coverage (~5 min)
  tier2 = [
    "clean-5M" "clean-10M"
    "regional-10M"
    "loss2pct-5M" "loss5pct-5M"
  ];

  # Tier 3: Comprehensive (~15 min)
  tier3 = [
    "clean-5M" "clean-10M" "clean-50M"
    "regional-10M" "continental-10M" "intercontinental-10M" "geo-5M"
    "loss2pct-5M" "loss5pct-5M"
    "tier3-loss-10M" "geo-loss-5M"
    "starlink-5M"
  ];

  # Config names (all individual test configs)
  configNames = [
    "clean-5M" "clean-10M" "clean-50M" "clean-100M"
    "regional-10M" "continental-10M" "intercontinental-10M" "geo-5M"
    "loss2pct-5M" "loss5pct-5M" "loss2pct-10M"
    "tier3-loss-10M" "geo-loss-5M"
    "starlink-5M" "starlink-10M"
  ];
}
