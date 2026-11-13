# Build a static C library for wasm32-wasip1 with CMake and our wasi-sdk.
#
#   mkWasiLib { pname = "foo"; inherit (pkgs.foo) version src; }
{
  lib,
  stdenvNoCC,
  cmake,
  ninja,
  wasi-sdk,
}:
{
  pname,
  version,
  src,
  cmakeFlags ? [ ],
  ...
}@args:
stdenvNoCC.mkDerivation (
  removeAttrs args [ "cmakeFlags" ]
  // {
    pname = "${pname}-wasi";
    inherit version src;

    nativeBuildInputs = [
      cmake
      ninja
    ]
    ++ (args.nativeBuildInputs or [ ]);

    cmakeFlags = [
      "-DCMAKE_TOOLCHAIN_FILE=${wasi-sdk}/share/cmake/wasi-sdk-p1.cmake"
      "-DWASI_SDK_PREFIX=${wasi-sdk}"
      # The Nix cmake hook blanks these, overriding the toolchain file.
      "-DCMAKE_AR=${wasi-sdk}/bin/llvm-ar"
      "-DCMAKE_RANLIB=${wasi-sdk}/bin/llvm-ranlib"
      "-DCMAKE_STRIP=${wasi-sdk}/bin/llvm-strip"
      "-DCMAKE_BUILD_TYPE=Release"
      "-DBUILD_SHARED_LIBS=OFF"
      "-DBUILD_TESTING=OFF"
      # Many C libraries still declare ancient minimum CMake versions.
      "-DCMAKE_POLICY_VERSION_MINIMUM=3.5"
    ]
    ++ cmakeFlags;

    dontFixup = true;

    meta = (args.meta or { }) // {
      platforms = lib.platforms.all;
    };
  }
)
