# nix/network/profiles.nix
#
# Network impairment profiles for testing.
# Defines latency, loss, and jitter parameters.
#
# Reference: documentation/nix_microvm_design.md lines 3302-3407
#
{ lib }:

{
  # ─── Latency Profiles ───────────────────────────────────────────────────────
  # These match the inter-router links in constants.nix
  latency = {
    clean = { index = 0; rttMs = 0; name = "no-delay"; };
    regional = { index = 1; rttMs = 10; name = "regional-dc"; };
    continental = { index = 2; rttMs = 60; name = "cross-continental"; };
    intercontinental = { index = 3; rttMs = 130; name = "intercontinental"; };
    satellite = { index = 4; rttMs = 300; name = "geo-satellite"; };
  };

  # ─── Loss Profiles ──────────────────────────────────────────────────────────
  loss = {
    clean = { percent = 0; name = "no-loss"; };
    light = { percent = 0.1; name = "light-loss"; };
    moderate = { percent = 1; name = "moderate-loss"; };
    heavy = { percent = 2; name = "heavy-loss"; };
    severe = { percent = 5; name = "severe-loss"; };
    extreme = { percent = 10; name = "extreme-loss"; };
  };

  # ─── Jitter Profiles ────────────────────────────────────────────────────────
  jitter = {
    none = { ms = 0; name = "no-jitter"; };
    low = { ms = 5; name = "low-jitter"; };
    moderate = { ms = 15; name = "moderate-jitter"; };
    high = { ms = 30; name = "high-jitter"; };
    extreme = { ms = 50; name = "extreme-jitter"; };
  };

  # ─── Combined Scenarios ─────────────────────────────────────────────────────
  # Pre-defined combinations for common test cases
  scenarios = {
    # Clean network - baseline testing
    clean = {
      name = "clean";
      description = "No impairment - baseline";
      latencyIndex = 0;
      lossPercent = 0;
      jitterMs = 0;
    };

    # Starlink-style satellite with handoffs
    starlink = {
      name = "starlink";
      description = "Starlink satellite - 20ms RTT, periodic blackouts";
      latencyIndex = 1;  # 10ms RTT base
      lossPercent = 0;
      jitterMs = 10;
      # Blackout pattern handled separately
      blackholePattern = {
        duration = 60;  # Total duration in seconds
        blackoutMs = 500;  # Blackout duration
        intervalSec = 15;  # Time between blackouts
      };
    };

    # Congested WiFi
    congested-wifi = {
      name = "congested-wifi";
      description = "Congested WiFi - 5ms latency, 2% loss, 10ms jitter";
      latencyIndex = 0;
      extraDelayMs = 5;
      lossPercent = 2;
      jitterMs = 10;
    };

    # Geo-satellite (high latency, stable)
    geo-satellite = {
      name = "geo-satellite";
      description = "Geostationary satellite - 300ms RTT, 0.5% loss";
      latencyIndex = 4;  # 300ms RTT
      lossPercent = 0.5;
      jitterMs = 20;
    };

    # Mobile 4G/LTE
    mobile-lte = {
      name = "mobile-lte";
      description = "Mobile LTE - 30ms latency, 1% loss, 15ms jitter";
      latencyIndex = 0;
      extraDelayMs = 30;
      lossPercent = 1;
      jitterMs = 15;
    };

    # Transatlantic cable
    transatlantic = {
      name = "transatlantic";
      description = "Transatlantic - 80ms RTT, 0.1% loss";
      latencyIndex = 2;  # 60ms base
      extraDelayMs = 20;
      lossPercent = 0.1;
      jitterMs = 5;
    };
  };

  # ─── Scenario Names List ────────────────────────────────────────────────────
  scenarioNames = lib.attrNames scenarios;
}
