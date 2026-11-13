# wasm-ld (lld) compiled to wasm32-wasip1, so pywasi can link interpreters
# at runtime under wazero without a native toolchain.
#
# Built from the same LLVM commit as our wasi-sdk, with YoWASP's patch for
# WASI support in LLVM's Support library (github.com/YoWASP/llvm-project,
# commit 97196c8eeb, by Catherine "whitequark") adapted to LLVM 23 in
# patches/llvm-wasi.patch. Run it as `lld.wasm -flavor wasm ...`.
{
  lib,
  stdenv,
  fetchurl,
  cmake,
  ninja,
  python3,
  wasi-sdk,
}:
let
  rev = "895aa2c896ada719451be2e3673c83da8ddf1141";
  # Emulated POSIX pieces from wasi-libc that LLVM's Support library uses.
  emulated = [
    "mman"
    "signal"
    "process-clocks"
    "getpid"
  ];
  defines = lib.concatMapStringsSep " " (e: "-D_WASI_EMULATED_${lib.toUpper (lib.replaceStrings [ "-" ] [ "_" ] e)}") emulated;
  libs = lib.concatMapStringsSep " " (e: "-lwasi-emulated-${e}") emulated;
in
stdenv.mkDerivation {
  pname = "lld-wasi";
  version = "23.1.0-wasi-sdk-34";

  src = fetchurl {
    url = "https://github.com/llvm/llvm-project/archive/${rev}.tar.gz";
    hash = "sha256-qkKK3SGTPK0ihvNE6hTctzHgXmxxEnh8QcFapfRQjAo=";
  };

  # The monorepo is large; only these directories are needed.
  unpackPhase = ''
    mkdir src
    tar -xzf $src -C src --strip-components=1 \
      llvm-project-${rev}/llvm llvm-project-${rev}/lld llvm-project-${rev}/cmake \
      llvm-project-${rev}/third-party llvm-project-${rev}/libc llvm-project-${rev}/libunwind/include
    cd src
  '';

  patches = [ ./patches/llvm-wasi.patch ];

  nativeBuildInputs = [
    cmake
    ninja
    python3
  ];

  dontUseCmakeConfigure = true;

  buildPhase = ''
    runHook preBuild

    # Host tools that generate code during the cross build.
    cmake -G Ninja -S llvm -B native \
      -DCMAKE_BUILD_TYPE=Release \
      -DLLVM_TARGETS_TO_BUILD=WebAssembly \
      -DLLVM_INCLUDE_TESTS=OFF -DLLVM_INCLUDE_BENCHMARKS=OFF -DLLVM_INCLUDE_EXAMPLES=OFF \
      -DLLVM_ENABLE_ZLIB=OFF -DLLVM_ENABLE_ZSTD=OFF -DLLVM_ENABLE_LIBXML2=OFF
    ninja -C native llvm-tblgen llvm-min-tblgen

    flags="${defines} -flto"
    ldflags="${libs} -flto -Wl,--strip-all -Wl,--max-memory=4294967296 -Wl,-z,stack-size=8388608,--stack-first"
    cmake -G Ninja -S llvm -B wasi \
      -DCMAKE_TOOLCHAIN_FILE=${wasi-sdk}/share/cmake/wasi-sdk-p1.cmake \
      -DWASI_SDK_PREFIX=${wasi-sdk} \
      -DCMAKE_AR=${wasi-sdk}/bin/llvm-ar \
      -DCMAKE_RANLIB=${wasi-sdk}/bin/llvm-ranlib \
      -DCMAKE_BUILD_TYPE=MinSizeRel \
      -DCMAKE_C_FLAGS="$flags" -DCMAKE_CXX_FLAGS="$flags" -DCMAKE_EXE_LINKER_FLAGS="$ldflags" \
      -DLLVM_HOST_TRIPLE=wasm32-wasip1 -DLLVM_DEFAULT_TARGET_TRIPLE=wasm32-wasip1 \
      -DLLVM_TARGETS_TO_BUILD=WebAssembly -DLLVM_ENABLE_PROJECTS=lld \
      -DLLVM_ENABLE_THREADS=OFF -DLLVM_ENABLE_PIC=OFF -DLLVM_BUILD_STATIC=ON \
      -DLLVM_ENABLE_BACKTRACES=OFF -DLLVM_ENABLE_CRASH_OVERRIDES=OFF \
      -DLLVM_ENABLE_UNWIND_TABLES=OFF -DLLVM_ENABLE_PLUGINS=OFF \
      -DLLVM_ENABLE_ZLIB=OFF -DLLVM_ENABLE_ZSTD=OFF -DLLVM_ENABLE_LIBXML2=OFF \
      -DLLVM_ENABLE_LIBEDIT=OFF -DLLVM_ENABLE_TERMINFO=OFF \
      -DLLVM_INCLUDE_TESTS=OFF -DLLVM_INCLUDE_BENCHMARKS=OFF -DLLVM_INCLUDE_EXAMPLES=OFF \
      -DLLVM_INCLUDE_UTILS=OFF -DLLVM_INCLUDE_DOCS=OFF \
      -DLLVM_NATIVE_TOOL_DIR=$PWD/native/bin -DLLVM_TABLEGEN=$PWD/native/bin/llvm-tblgen
    ninja -C wasi lld

    runHook postBuild
  '';

  installPhase = ''
    install -Dm644 wasi/bin/lld $out/bin/lld.wasm
  '';

  dontFixup = true;

  meta = {
    description = "LLVM's wasm-ld built for wasm32-wasip1";
    license = with lib.licenses; [
      asl20
      llvm-exception
    ];
    platforms = lib.platforms.unix;
  };
}
