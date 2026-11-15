# Static libbz2 for wasm32-wasip1, for CPython's bz2 module. bzip2's
# Makefile also builds its tools, so compile the library sources directly.
{
  stdenvNoCC,
  bzip2,
  wasi-sdk,
}:
stdenvNoCC.mkDerivation {
  pname = "bzip2-wasi";
  inherit (bzip2) version src;

  buildPhase = ''
    runHook preBuild
    for f in blocksort huffman crctable randtable compress decompress bzlib; do
      ${wasi-sdk}/bin/clang --target=wasm32-wasip1 --sysroot=${wasi-sdk}/share/wasi-sysroot \
        -O2 -D_FILE_OFFSET_BITS=64 -c $f.c -o $f.o
    done
    ${wasi-sdk}/bin/llvm-ar rcs libbz2.a *.o
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    install -Dm644 libbz2.a $out/lib/libbz2.a
    install -Dm644 bzlib.h $out/include/bzlib.h
    runHook postInstall
  '';

  dontFixup = true;
}
