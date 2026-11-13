# Static zlib for wasm32-wasip1, built with our wasi-sdk so it shares the
# exact wasi-libc that CPython and extension modules link against.
{
  stdenvNoCC,
  zlib,
  wasi-sdk,
}:
stdenvNoCC.mkDerivation {
  pname = "zlib-wasi";
  inherit (zlib) version src;

  env = {
    CC = "${wasi-sdk}/bin/clang --target=wasm32-wasip1 --sysroot=${wasi-sdk}/share/wasi-sysroot";
    AR = "${wasi-sdk}/bin/llvm-ar";
    RANLIB = "${wasi-sdk}/bin/llvm-ranlib";
    CFLAGS = "-O2";
  };

  configurePhase = ''
    runHook preConfigure
    ./configure --static --prefix=$out
    runHook postConfigure
  '';

  buildFlags = [ "libz.a" ];
  installTargets = [ "install" ];

  dontFixup = true;
}
