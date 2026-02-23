# nix/containers/server.nix
#
# OCI container image for GoSRT server.
#
# Reference: documentation/nix_microvm_design.md lines 877-945
#
# Usage:
#   nix build .#server-container
#   docker load < ./result
#   docker run --rm -p 6000:6000/udp -p 9100:9100 gosrt-server:latest
#
{ pkgs, lib, gosrtPackage }:

pkgs.dockerTools.buildLayeredImage {
  name = "gosrt-server";
  tag = "latest";

  contents = [
    gosrtPackage
    pkgs.busybox
    pkgs.cacert
    pkgs.curl
  ];

  extraCommands = ''
    # Create /tmp with correct permissions
    mkdir -p tmp
    chmod 1777 tmp

    # Create /var/run for runtime files
    mkdir -p var/run
  '';

  config = {
    Cmd = [ "/bin/server" ];

    Env = [
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
    ];

    ExposedPorts = {
      "6000/udp" = {};  # SRT
      "9100/tcp" = {};  # Prometheus
    };

    Labels = {
      "org.opencontainers.image.title" = "GoSRT Server";
      "org.opencontainers.image.description" = "Pure Go SRT implementation - Server";
      "org.opencontainers.image.source" = "https://github.com/your-org/gosrt";
    };

    WorkingDir = "/";
  };
}
