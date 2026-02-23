# nix/packages/gosrt.nix
#
# GoSRT package with integrated audit checks.
# Builds FAIL if audit-metrics or seq-audit detect issues.
#
# Reference: documentation/nix_microvm_implementation_plan.md Phase 3
#
# Usage:
#   nix build .#gosrt-prod    # Production build
#   nix build .#gosrt-debug   # Debug build with assertions
#   nix build .#gosrt-perf    # Performance build with pprof
#
{ pkgs
, lib
, src
, buildVariant ? "production"
, enableAudit ? true  # Set to false to skip audit (for faster dev iteration)
}:

let
  # Build configuration per variant
  variantConfig = {
    production = {
      ldflags = [ "-s" "-w" ];
      tags = [ ];
    };
    debug = {
      ldflags = [ ];  # Keep debug symbols
      tags = [ "debug" ];
    };
    perf = {
      ldflags = [ "-s" "-w" ];
      tags = [ "pprof" ];
    };
  };

  cfg = variantConfig.${buildVariant};

  # Audit check derivation (run before build)
  # This ensures unsafe code NEVER reaches a VM
  auditChecks = pkgs.runCommand "gosrt-audit-checks" {
    inherit src;
    nativeBuildInputs = [ pkgs.go_1_26 ];
  } ''
    cd $src
    export HOME=$(mktemp -d)
    export GOEXPERIMENT=jsonv2
    export GOCACHE=$(mktemp -d)
    export GOMODCACHE=$(mktemp -d)
    export CGO_ENABLED=0

    echo "=== Running Sequence Arithmetic Safety Audit ==="
    if [ -f tools/seq-audit/main.go ]; then
      go run ./tools/seq-audit/... ./... 2>&1 || {
        echo "FAIL: Unsafe sequence arithmetic detected!"
        echo "Fix patterns like 'int32(a-b) < 0' - use circular.Number instead"
        exit 1
      }
      echo "PASS: Sequence audit"
    else
      echo "SKIP: tools/seq-audit not found"
    fi

    echo ""
    echo "=== Running Prometheus Metrics Audit ==="
    if [ -f tools/metrics-audit/main.go ]; then
      go run ./tools/metrics-audit/... 2>&1 || {
        echo "FAIL: Metrics audit failed!"
        echo "Ensure all metrics are defined in metrics/metrics.go and exported in handler.go"
        exit 1
      }
      echo "PASS: Metrics audit"
    else
      echo "SKIP: tools/metrics-audit not found"
    fi

    echo ""
    echo "=== All audits passed ==="
    touch $out
  '';

in pkgs.buildGo126Module {
  pname = "gosrt-${buildVariant}";
  version = "0.1.0";
  inherit src;

  # Vendor hash - use vendor directory
  vendorHash = null;

  subPackages = [
    "contrib/server"
    "contrib/client"
    "contrib/client-generator"
  ];

  # Run audits BEFORE build (if enabled)
  preBuild = ''
    ${lib.optionalString enableAudit ''
      # Ensure audit checks passed (will fail build if they haven't)
      echo "Audit checks: ${auditChecks}"
    ''}
    export GOEXPERIMENT=jsonv2
    export CGO_ENABLED=0
  '';

  ldflags = cfg.ldflags;
  tags = cfg.tags;

  # Rename binaries for clarity
  postInstall = ''
    # Binaries are named after their directory (server, client, client-generator)
    # No renaming needed - they match the expected names
    echo "Installed binaries:"
    ls -la $out/bin/
  '';

  meta = with lib; {
    description = "GoSRT - Pure Go SRT implementation (${buildVariant})";
    homepage = "https://github.com/your-org/gosrt";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
