# nix/shell.nix
#
# Development shell for GoSRT.
# Provides Go toolchain, io_uring dependencies, and debugging tools.
#
# Reference: documentation/nix_microvm_design.md lines 4884-4955
#
{ pkgs }:

let
  lib = pkgs.lib;
  constants = import ./constants.nix;

  # Go 1.26 with greenteagc (default) and jsonv2 experimental
  goPackage = pkgs.go_1_26;

  # Experimental features to enable
  goExperiment = lib.concatStringsSep "," constants.go.experimentalFeatures;

in pkgs.mkShell {
  name = "gosrt-dev";

  packages = with pkgs; [
    # Go toolchain
    goPackage
    gopls
    gotools
    golangci-lint
    delve

    # Network testing tools
    iproute2
    iperf        # iperf2 for baseline testing
    ethtool
    nftables
    tcpdump
    nmap
    curl
    jq
    netcat-gnu

    # io_uring debugging
    strace
    ltrace

    # Performance analysis
    # Note: perf requires kernel support
    # linuxPackages_latest.perf  # Uncomment if running on matching kernel

    # Documentation and graphs
    graphviz     # For pprof graphs

    # Nix utilities
    nixfmt

    # MicroVM tooling
    tmux
    openssh
    sshpass
  ];

  shellHook = ''
    # Enable Go experimental features
    # Note: greenteagc is default in Go 1.26, only jsonv2 is experimental
    export GOEXPERIMENT=${goExperiment}

    # Pure Go builds - no CGO required
    export CGO_ENABLED=0

    echo ""
    echo "GoSRT Development Shell"
    echo "======================="
    echo "  Go:           $(go version | cut -d' ' -f3)"
    echo "  GOEXPERIMENT: $GOEXPERIMENT"
    echo "  CGO:          disabled (pure Go)"
    echo ""
    echo "Build Commands:"
    echo "  make build           Build all binaries"
    echo "  make test            Run full test suite"
    echo "  make test-quick      Quick tests (no static checks)"
    echo ""
    echo "Lint Commands:"
    echo "  make lint-quick      Tier 0: Fast feedback (~30s)"
    echo "  make lint            Tier 1: PR validation (~2min)"
    echo "  make lint-comprehensive  Tier 2: Full analysis (~10min)"
    echo "  make lint-fix        Auto-fix linting issues"
    echo ""
    echo "Nix Commands:"
    echo "  nix flake check --no-build   Validate flake"
    echo "  nix run .#srt-vm-check       Check VM status"
    echo "  nix run .#srt-tmux-all       Start all VMs in tmux"
    echo ""
  '';

  # Environment variables
  GOEXPERIMENT = goExperiment;
  CGO_ENABLED = "0";
}
