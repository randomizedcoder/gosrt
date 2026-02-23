# nix/containers/client.nix
#
# OCI container image for GoSRT client.
#
{ pkgs, lib, gosrtPackage }:

pkgs.dockerTools.buildLayeredImage {
  name = "gosrt-client";
  tag = "latest";

  contents = [
    gosrtPackage
    pkgs.busybox
    pkgs.cacert
    pkgs.curl
  ];

  extraCommands = ''
    mkdir -p tmp
    chmod 1777 tmp
    mkdir -p var/run
  '';

  config = {
    Cmd = [ "/bin/client" ];

    Env = [
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
    ];

    ExposedPorts = {
      "9100/tcp" = {};  # Prometheus
    };

    Labels = {
      "org.opencontainers.image.title" = "GoSRT Client";
      "org.opencontainers.image.description" = "Pure Go SRT implementation - Client (Subscriber)";
      "org.opencontainers.image.source" = "https://github.com/your-org/gosrt";
    };

    WorkingDir = "/";
  };
}
