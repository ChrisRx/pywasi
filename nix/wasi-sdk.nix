# Prebuilt wasi-sdk (clang/LLVM + wasi-libc sysroot) from the upstream
# releases. nixpkgs does not package wasi-sdk itself, and CPython's WASI
# build tooling expects the upstream layout (WASI_SDK_PATH).
{
  lib,
  stdenv,
  fetchurl,
  autoPatchelfHook,
  zlib,
  zstd,
  libxml2,
  ncurses,
}:
let
  version = "34";
  sources = {
    x86_64-linux = {
      arch = "x86_64-linux";
      hash = "sha256-t2HjoHIduunAmgBZ5f2yv5F9G0qKe0MPs7Wq+wmEssQ=";
    };
    aarch64-linux = {
      arch = "arm64-linux";
      hash = "sha256-9+JD3/VNYLzFdulNYWa2n0EPJQCuSpzu80MVvhDneXE=";
    };
    x86_64-darwin = {
      arch = "x86_64-macos";
      hash = "sha256-h9J/qK3Gje5Zv78uIqbTTvcXw01r8divKlb8kp2c4Os=";
    };
    aarch64-darwin = {
      arch = "arm64-macos";
      hash = "sha256-nFk5gQa0F/jxSRM4D98Al6jMD/SvnrPOAGWoWeiNSek=";
    };
  };
  src = sources.${stdenv.hostPlatform.system} or (throw "wasi-sdk: unsupported system");
in
stdenv.mkDerivation {
  pname = "wasi-sdk";
  inherit version;

  src = fetchurl {
    url = "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${version}/wasi-sdk-${version}.0-${src.arch}.tar.gz";
    inherit (src) hash;
  };

  nativeBuildInputs = lib.optionals stdenv.hostPlatform.isLinux [ autoPatchelfHook ];
  buildInputs = lib.optionals stdenv.hostPlatform.isLinux [
    stdenv.cc.cc.lib
    zlib
    zstd
    libxml2
    ncurses
  ];

  dontConfigure = true;
  dontBuild = true;
  dontStrip = true;

  installPhase = ''
    runHook preInstall
    mkdir -p $out
    cp -r . $out/
    runHook postInstall
  '';

  meta = {
    description = "WASI-enabled WebAssembly C/C++ toolchain";
    homepage = "https://github.com/WebAssembly/wasi-sdk";
    license = lib.licenses.asl20;
    platforms = builtins.attrNames sources;
  };
}
