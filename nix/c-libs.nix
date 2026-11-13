# C libraries built for wasm32-wasip1. They are merged into one sysroot
# (PYWASI_SYSROOT in the dev shell) that package builds compile and link
# against. To make another library available, add an entry here.
{ pkgs, wasi-sdk }:
let
  mkWasiLib = pkgs.callPackage ./wasi-lib.nix { inherit wasi-sdk; };
in
{
  zlib = pkgs.callPackage ./zlib-wasi.nix { inherit wasi-sdk; };

  # BLAS/LAPACK for numpy (and anything else using CBLAS/LAPACK).
  openblas = pkgs.callPackage ./openblas-wasi.nix { inherit wasi-sdk; };

  # For PyYAML's C accelerator (yaml._yaml).
  libyaml = mkWasiLib {
    pname = "libyaml";
    inherit (pkgs.libyaml) version src;
  };
}
