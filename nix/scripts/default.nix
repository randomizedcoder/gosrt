# nix/scripts/default.nix
#
# Export all management scripts.
#
{ pkgs, lib }:

let
  vmManagement = import ./vm-management.nix { inherit pkgs lib; };

in {
  inherit (vmManagement) ssh console stop status scripts;
  inherit (vmManagement) vmIsRunning vmAllRunning;
  inherit (vmManagement) vmCheck vmCheckJson vmStopAll vmWait vmStartBackground;
  inherit (vmManagement) tmuxAll tmuxAttach tmuxClear vmStopAndClearTmux vmRestart;
}
