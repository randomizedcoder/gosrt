# nix/containers/default.nix
#
# Export OCI container images for GoSRT.
#
# Reference: documentation/nix_microvm_implementation_plan.md Phase 4 (OCI Containers)
#
# Usage:
#   nix build .#server-container
#   docker load < ./result
#   docker run --rm -p 6000:6000/udp -p 9100:9100 gosrt-server:latest
#
{ pkgs, lib, gosrtPackage }:

{
  server = import ./server.nix {
    inherit pkgs lib gosrtPackage;
  };

  client = import ./client.nix {
    inherit pkgs lib gosrtPackage;
  };

  client-generator = import ./client-generator.nix {
    inherit pkgs lib gosrtPackage;
  };
}
