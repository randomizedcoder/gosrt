# nix/packages/ffmpeg.nix
#
# FFmpeg with SRT support for interoperability testing.
# Simply re-exports pkgs.ffmpeg-full which includes SRT.
#
# Reference: documentation/nix_microvm_design.md lines 851-875
#
# Verify SRT support: ffmpeg -protocols | grep srt
#
{ pkgs, lib }:

# ffmpeg-full includes SRT support out of the box
pkgs.ffmpeg-full.override {
  # Ensure SRT is explicitly enabled (should be by default in ffmpeg-full)
  withSrt = true;
}
