# nix/testing/default.nix
#
# Test orchestration for GoSRT MicroVM integration tests.
# Provides test runners, configurations, and analysis tools.
#
# Reference: documentation/nix_microvm_design.md lines 4370-4394
#
{ pkgs, lib }:

let
  # Test configurations
  configs = import ./configs.nix { inherit lib; };

  # Test runner scripts
  runner = import ./runner.nix { inherit pkgs lib; };

  # Analysis and reporting tools
  analysis = import ./analysis.nix { inherit pkgs lib; };

  # Phase 10: Integration tests
  integration = import ./integration.nix { inherit pkgs lib; };

in {
  # ─── Test Configurations ─────────────────────────────────────────────────
  inherit configs;

  # Configuration helpers
  getConfig = name: configs.${name} or (throw "Unknown config: ${name}");
  configNames = configs.configNames;
  tier1 = configs.tier1;
  tier2 = configs.tier2;
  tier3 = configs.tier3;

  # ─── Test Runner Scripts ─────────────────────────────────────────────────
  inherit (runner) waitForService startAll runner runTier;

  # Script derivations (for flake export)
  testRunner = runner.runner;
  testRunTier = runner.runTier;
  testStartAll = runner.startAll;
  testWaitService = runner.waitForService;

  # ─── Analysis Tools ──────────────────────────────────────────────────────
  inherit (analysis) extractMetrics generateReport compareRuns checkPass;

  # Tool derivations (for flake export)
  extractMetricsScript = analysis.extractMetrics;
  generateReportScript = analysis.generateReport;
  compareRunsScript = analysis.compareRuns;
  checkPassScript = analysis.checkPass;

  # ─── Phase 10: Integration Tests ─────────────────────────────────────────
  inherit (integration) basicFlow latencySwitching lossInjection fullSuite smokeTest;

  # Integration test derivations (for flake export)
  integrationBasic = integration.basicFlow;
  integrationLatency = integration.latencySwitching;
  integrationLoss = integration.lossInjection;
  integrationFull = integration.fullSuite;
  integrationSmoke = integration.smokeTest;
}
