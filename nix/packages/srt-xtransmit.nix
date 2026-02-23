# nix/packages/srt-xtransmit.nix
#
# srt-xtransmit - SRT test and debug tool.
# Used for interoperability testing with GoSRT.
#
# Reference: documentation/nix_microvm_design.md lines 778-848
# Based on: /home/das/Downloads/srt-xtransmit/flake.nix
#
{ pkgs, lib }:

pkgs.stdenv.mkDerivation {
  pname = "srt-xtransmit";
  version = "0.2.0";

  src = pkgs.fetchFromGitHub {
    owner = "maxsharabayko";
    repo = "srt-xtransmit";
    rev = "v0.2.0";
    fetchSubmodules = true;
    hash = "sha256-AEqVJr7TLH+MV4SntZhFFXTttnmcywda/P1EoD2px6E=";
  };

  nativeBuildInputs = with pkgs; [
    cmake
    pkg-config
  ];

  buildInputs = with pkgs; [
    openssl
  ];

  cmakeFlags = [
    "-DENABLE_CXX17=OFF"
    "-DCMAKE_POLICY_VERSION_MINIMUM=3.5"
  ];

  # Upstream installs the SRT apps, but not srt-xtransmit.
  # CMake builds it at build/xtransmit/bin/srt-xtransmit
  postInstall = ''
    candidate=""
    for p in \
      build/xtransmit/bin/srt-xtransmit \
      build/bin/srt-xtransmit \
      build/xtransmit/srt-xtransmit \
      bin/srt-xtransmit \
    ; do
      if [ -x "$p" ]; then
        candidate="$p"
        break
      fi
    done

    if [ -z "$candidate" ]; then
      # Fallback: locate it
      candidate="$(find . -type f -name srt-xtransmit -perm -0100 | head -n1 || true)"
    fi

    if [ -z "$candidate" ] || [ ! -x "$candidate" ]; then
      echo "ERROR: srt-xtransmit binary not found in build tree" >&2
      find . -type f -name srt-xtransmit -print >&2 || true
      exit 1
    fi

    install -Dm755 "$candidate" "$out/bin/srt-xtransmit"
  '';

  # Nix fixup fails if .pc files contain ${prefix}//nix/store/...
  postFixup = ''
    for pc in "$out"/lib/pkgconfig/*.pc; do
      [ -f "$pc" ] || continue
      sed -i 's#//#/#g' "$pc"
    done
  '';

  meta = with lib; {
    description = "SRT xtransmit performance / traffic generator";
    homepage = "https://github.com/maxsharabayko/srt-xtransmit";
    license = licenses.mit;
    platforms = platforms.linux;
  };
}
