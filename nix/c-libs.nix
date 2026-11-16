# C libraries built for wasm32-wasip1. They are merged into one sysroot
# (PYWASI_SYSROOT in the dev shell) that package builds compile and link
# against. To make another library available, add an entry here.
{ pkgs, wasi-sdk }:
let
  mkWasiLib = pkgs.callPackage ./wasi-lib.nix { inherit wasi-sdk; };
  zlib = pkgs.callPackage ./zlib-wasi.nix { inherit wasi-sdk; };
  zlibFlags = [
    "-DZLIB_INCLUDE_DIR=${zlib}/include"
    "-DZLIB_LIBRARY=${zlib}/lib/libz.a"
  ];
  # For lxml.
  libxml2 = mkWasiLib {
    pname = "libxml2";
    inherit (pkgs.libxml2) version src;
    cmakeFlags = [
      # No threads in wasip1, no dlopen for modules, and no HTTP client.
      "-DLIBXML2_WITH_THREADS=OFF"
      "-DLIBXML2_WITH_MODULES=OFF"
      "-DLIBXML2_WITH_HTTP=OFF"
      "-DLIBXML2_WITH_PYTHON=OFF"
      "-DLIBXML2_WITH_PROGRAMS=OFF"
      "-DLIBXML2_WITH_TESTS=OFF"
      "-DLIBXML2_WITH_DOCS=OFF"
      "-DLIBXML2_WITH_LZMA=OFF"
      "-DLIBXML2_WITH_ICU=OFF"
      "-DLIBXML2_WITH_ZLIB=ON"
      # iconv is part of wasi-libc.
      "-DIconv_IS_BUILT_IN=ON"
    ]
    ++ zlibFlags;
  };
in
rec {
  inherit zlib libxml2;

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

  libxslt = mkWasiLib {
    pname = "libxslt";
    inherit (pkgs.libxslt) version src;
    cmakeFlags = [
      "-DLibXml2_DIR=${libxml2}/lib/cmake/libxml2"
    ]
    ++ zlibFlags
    ++ [
      "-DLIBXSLT_WITH_THREADS=OFF"
      "-DLIBXSLT_WITH_MODULES=OFF"
      "-DLIBXSLT_WITH_PYTHON=OFF"
      "-DLIBXSLT_WITH_PROGRAMS=OFF"
      "-DLIBXSLT_WITH_TESTS=OFF"
      "-DLIBXSLT_WITH_CRYPTO=OFF"
      "-DIconv_IS_BUILT_IN=ON"
    ];
  };

  # For Pillow (with zlib). Pillow reads PNG itself.
  # FreeType (Pillow's ImageFont, matplotlib's text) recovers from errors
  # with setjmp/longjmp, which need the wasm exception-handling proposal,
  # as for extension modules (toolchain/wasi-cc).
  freetype = mkWasiLib {
    pname = "freetype";
    inherit (pkgs.freetype) version src;
    env.CFLAGS = "-fwasm-exceptions -mllvm -wasm-enable-sjlj -mllvm -wasm-use-legacy-eh=false";
    cmakeFlags = [
      "-DFT_DISABLE_HARFBUZZ=ON"
      "-DFT_DISABLE_BROTLI=ON"
      "-DFT_DISABLE_BZIP2=ON"
      "-DFT_DISABLE_PNG=ON"
      "-DFT_DISABLE_ZLIB=ON"
    ];
  };
  libjpeg = mkWasiLib {
    pname = "libjpeg-turbo";
    inherit (pkgs.libjpeg_turbo) version src;
    cmakeFlags = [
      "-DENABLE_SHARED=OFF"
      "-DENABLE_STATIC=ON"
      "-DWITH_SIMD=OFF"
      "-DWITH_TURBOJPEG=OFF"
      "-DWITH_TOOLS=OFF"
    ];
  };

  # For CPython's compression.zstd (_zstd). zstd's CMake project lives in
  # build/cmake.
  zstd = mkWasiLib {
    pname = "zstd";
    inherit (pkgs.zstd) version src;
    cmakeDir = "../build/cmake";
    cmakeFlags = [
      "-DZSTD_MULTITHREAD_SUPPORT=OFF"
      "-DZSTD_BUILD_PROGRAMS=OFF"
      "-DZSTD_BUILD_TESTS=OFF"
      "-DZSTD_BUILD_SHARED=OFF"
      "-DZSTD_BUILD_STATIC=ON"
      "-DZSTD_LEGACY_SUPPORT=OFF"
    ];
  };

  # For PyYAML's C accelerator (yaml._yaml).
  libyaml = mkWasiLib {
    pname = "libyaml";
    inherit (pkgs.libyaml) version src;
  };

  # Apache Arrow C++, for pyarrow.
  arrow = pkgs.callPackage ./arrow-wasi.nix { inherit wasi-sdk mkWasiLib zlib zstd; };
}
