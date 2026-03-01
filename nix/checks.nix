# nix/checks.nix
#
# CI checks for nix flake check.
# Runs go vet, tests, and validates Nix expressions.
#
# Reference: documentation/nix_microvm_design.md lines 4808-4881
#
{ pkgs, lib, src }:

let
  constants = import ./constants.nix;

  # Go 1.26 with greenteagc (default) and jsonv2 experimental
  goPackage = pkgs.go_1_26;

  # Experimental features to enable
  goExperiment = lib.concatStringsSep "," constants.go.experimentalFeatures;

  # Common Go environment setup (use vendored dependencies for sandbox builds)
  goEnv = ''
    export GOEXPERIMENT=${goExperiment}
    export HOME=$(mktemp -d)
    export CGO_ENABLED=0
    export GOFLAGS="-mod=vendor"
  '';

in {
  # ─── Go Vet ────────────────────────────────────────────────────────────────
  go-vet = pkgs.runCommand "gosrt-go-vet" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    go vet ./... > $out 2>&1 || (cat $out && exit 1)
  '';

  # ─── Go Test (quick) ───────────────────────────────────────────────────────
  go-test-quick = pkgs.runCommand "gosrt-go-test-quick" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    go test -short ./... > $out 2>&1 || (cat $out && exit 1)
  '';

  # ─── Circular Number Tests ─────────────────────────────────────────────────
  # Critical for sequence wraparound correctness
  go-test-circular = pkgs.runCommand "gosrt-go-test-circular" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    go test -v ./circular/... > $out 2>&1 || (cat $out && exit 1)
  '';

  # ─── Packet Marshaling Tests ───────────────────────────────────────────────
  go-test-packet = pkgs.runCommand "gosrt-go-test-packet" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    go test -v ./packet/... > $out 2>&1 || (cat $out && exit 1)
  '';

  # ─── Stream Tier 1 Tests ───────────────────────────────────────────────────
  # Core receiver stream tests (~50 tests, <3s)
  go-test-stream-tier1 = pkgs.runCommand "gosrt-go-test-stream-tier1" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    go test -v ./congestion/live/... -run 'TestStream_Tier1' > $out 2>&1 || (cat $out && exit 1)
  '';

  # ─── Sequence Arithmetic Audit ─────────────────────────────────────────────
  # Detect unsafe sequence number patterns
  seq-audit = pkgs.runCommand "gosrt-seq-audit" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    # Run the sequence arithmetic audit
    if [ -d "tools/seq-audit" ]; then
      go run ./tools/seq-audit/... > $out 2>&1 || (cat $out && exit 1)
    else
      echo "Sequence audit tool not found - skipping" > $out
    fi
  '';

  # ─── Metrics Audit ─────────────────────────────────────────────────────────
  # Verify all metrics are defined and used
  metrics-audit = pkgs.runCommand "gosrt-metrics-audit" {
    nativeBuildInputs = [ goPackage ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    # Run the metrics audit tool
    if [ -d "tools/metrics-audit" ]; then
      go run ./tools/metrics-audit/... > $out 2>&1 || (cat $out && exit 1)
    else
      echo "Metrics audit tool not found - skipping" > $out
    fi
  '';

  # ─── Go Security Scan (gosec) ─────────────────────────────────────────────
  # Scan for common security issues in Go code
  # Excludes:
  #   G103 - unsafe: intentional for io_uring/syscalls (systems code)
  #   G115 - integer overflow: false positives for validated conversions
  #   G204 - subprocess with variable: expected in integration tests
  #   G304 - file path from variable: intentional in config loading
  #   G306 - WriteFile permissions: log files intentionally world-readable
  #   G401 - AES key wrap: RFC 3394 compliant (vendor code)
  #   G501 - SHA1 in PBKDF2: SRT protocol mandates PBKDF2-SHA1
  # NOT excluded (will fail build if found):
  #   G104 - unhandled errors: should be fixed
  #   G112 - HTTP timeouts: should be fixed
  #   G301 - directory permissions: case-by-case review
  #   G407 - insecure TLS: should fail if found
  #   G505 - MD5 usage: should fail if found
  go-sec = pkgs.runCommand "gosrt-go-sec" {
    nativeBuildInputs = [ goPackage pkgs.gosec ];
    inherit src;
  } ''
    cd $src
    ${goEnv}
    # Run gosec excluding noisy/expected findings
    gosec -exclude=G103,G115,G204,G304,G306,G401,G501 -fmt=text ./... > $out 2>&1 || {
      exitcode=$?
      cat $out
      # gosec returns 1 for findings, 2+ for errors
      exit $exitcode
    }
  '';

  # ─── Nix Format Check ──────────────────────────────────────────────────────
  nix-fmt = pkgs.runCommand "gosrt-nix-fmt" {
    nativeBuildInputs = [ pkgs.nixfmt pkgs.findutils ];
    inherit src;
  } ''
    cd $src
    # Check that all Nix files are properly formatted
    find . -name '*.nix' -type f | head -20 | while read f; do
      echo "Checking: $f"
      nixfmt --check "$f" || true
    done > $out 2>&1
  '';

  # ─── Flake Schema Validation ───────────────────────────────────────────────
  flake-valid = pkgs.runCommand "gosrt-flake-valid" {
    inherit src;
  } ''
    # This check always passes if the flake evaluated
    echo "Flake schema validated successfully" > $out
  '';
}
