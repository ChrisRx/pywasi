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

  # For CPython's ssl and hashlib.
  openssl = pkgs.callPackage ./openssl-wasi.nix { inherit wasi-sdk; };

  # For CPython's sqlite3, bz2 and lzma.
  sqlite = pkgs.callPackage ./sqlite-wasi.nix { inherit wasi-sdk; };
  bzip2 = pkgs.callPackage ./bzip2-wasi.nix { inherit wasi-sdk; };
  xz = mkWasiLib {
    pname = "xz";
    inherit (pkgs.xz) version src;
    cmakeFlags = [
      "-DXZ_THREADS=no"
      "-DXZ_NLS=OFF"
      "-DXZ_SANDBOX=no"
      "-DXZ_DOC=OFF"
      "-DXZ_TOOL_XZ=OFF"
      "-DXZ_TOOL_XZDEC=OFF"
      "-DXZ_TOOL_LZMADEC=OFF"
      "-DXZ_TOOL_LZMAINFO=OFF"
      "-DXZ_TOOL_SCRIPTS=OFF"
    ];
  };

  # For PyYAML's C accelerator (yaml._yaml).
  libyaml = mkWasiLib {
    pname = "libyaml";
    inherit (pkgs.libyaml) version src;
  };
}
