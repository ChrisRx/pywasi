# Apache Arrow C++ for wasm32-wasip1, for pyarrow: static libraries with
# compute (with RE2, for the regex functions pandas' Arrow strings use),
# Acero, datasets, CSV, JSON, IPC and Parquet (zstd, lz4, snappy, zlib),
# built without threads as for Emscripten. Left out: ORC, Substrait, Flight,
# Gandiva and the cloud filesystems.
#
# The dependencies Arrow builds itself are fetched here and passed to it
# as local files (ARROW_<NAME>_URL), at the versions and checksums of its
# cpp/thirdparty/versions.txt. arrow-wasi.patch builds Thrift without its
# socket transports, leaves out sigaction and posix_madvise, none of which
# WASI has, and finds time zones in TZDIR or pywasi's /share/zoneinfo.
{
  lib,
  fetchurl,
  writeShellScript,
  mkWasiLib,
  wasi-sdk,
  zlib,
  zstd,
}:
let
  version = "26.0.1";
  # Named with the archive's extension, which CMake goes by.
  dep =
    name: url: sha256:
    fetchurl {
      inherit url sha256;
      name = "arrow-dep-${name}${if lib.hasSuffix ".tar.bz2" url then ".tar.bz2" else ".tar.gz"}";
    };
  deps = {
    ABSL =
      dep "abseil-20250127.0" "https://github.com/abseil/abseil-cpp/archive/20250127.0.tar.gz"
        "16242f394245627e508ec6bb296b433c90f8d914f73b9c026fddb905e27276e8";
    RE2 =
      dep "re2-2023-03-01" "https://github.com/google/re2/archive/2023-03-01.tar.gz"
        "7a9a4824958586980926a300b4717202485c4b4115ac031822e29aa4ef207e48";
    BOOST =
      dep "boost-1.92.0" "https://github.com/boostorg/boost/releases/download/boost-1.92.0/boost-1.92.0-cmake.tar.gz"
        "f51707c27359a0df0cac1beada86de31bb5eed5e8285592dadec384df99c2984";
    LZ4 =
      dep "lz4-1.10.0" "https://github.com/lz4/lz4/archive/v1.10.0.tar.gz"
        "537512904744b35e232912055ccf8ec66d768639ff3abe5788d90d792ec5f48b";
    RAPIDJSON =
      dep "rapidjson-232389d"
        "https://github.com/miloyip/rapidjson/archive/232389d4f1012dddec4ef84861face2d2ba85709.tar.gz"
        "b9290a9a6d444c8e049bd589ab804e0ccf2b05dc5984a19ed5ae75d090064806";
    SIMDJSON =
      dep "simdjson-4.6.4" "https://github.com/simdjson/simdjson/archive/refs/tags/v4.6.4.tar.gz"
        "b091107844fe928158c5c2265c20360fff312889ddf7ebc4528a0f0f8f2ff9cd";
    URIPARSER =
      dep "uriparser-1.0.2"
        "https://github.com/uriparser/uriparser/releases/download/uriparser-1.0.2/uriparser-1.0.2.tar.bz2"
        "dd2e4843f43de6f5aedf430e530f1e2d159eba8ca4d64c2787af1c20f707a9e4";
    SNAPPY =
      dep "snappy-1.2.2" "https://github.com/google/snappy/archive/1.2.2.tar.gz"
        "90f74bc1fbf78a6c56b3c4a082a05103b3a56bb17bca1a27e052ea11723292dc";
    THRIFT =
      dep "thrift-0.24.0" "https://archive.apache.org/dist/thrift/0.24.0/thrift-0.24.0.tar.gz"
        "e0fa5839a4c5c1d631b0931cf2c554ebbfa4e2fee3a9fb3ffd4f82ce4396c6e4";
    UTF8PROC =
      dep "utf8proc-2.10.0" "https://github.com/JuliaStrings/utf8proc/archive/v2.10.0.tar.gz"
        "6f4f1b639daa6dca9f80bc5db1233e9cbaa31a67790887106160b33ef743f136";
    XSIMD =
      dep "xsimd-14.2.0" "https://github.com/xtensor-stack/xsimd/archive/14.2.0.tar.gz"
        "21e841ab684b05331e81e7f782431753a029ef7b7d9d6d3ddab837e7782a40ee";
  };
  # Arrow asks for position-independent code for its object libraries,
  # which pywasi's static links don't want (toolchain/wasi-cc drops it from
  # extensions too).
  noPic = writeShellScript "no-pic" ''
    args=()
    for a in "$@"; do
      case "$a" in -fPIC | -fpic | -fPIE | -fpie) ;; *) args+=("$a") ;; esac
    done
    exec "''${args[@]}"
  '';
  # As toolchain/wasi-cc compiles.
  flags = lib.concatStringsSep " " [
    "-msimd128"
    "-fwasm-exceptions"
    "-mllvm -wasm-enable-sjlj"
    "-mllvm -wasm-use-legacy-eh=false"
    "-D_WASI_EMULATED_MMAN"
    "-D_WASI_EMULATED_SIGNAL"
    "-D_WASI_EMULATED_GETPID"
    "-D_WASI_EMULATED_PROCESS_CLOCKS"
  ];
in
mkWasiLib {
  pname = "arrow-cpp";
  inherit version;
  src = fetchurl {
    url = "https://archive.apache.org/dist/arrow/arrow-${version}/apache-arrow-${version}.tar.gz";
    sha256 = "1dd1b6701d9de58a4ed82b107539682f46ef6b9e22747e18e03204125c05a987";
  };
  patches = [ ./arrow-wasi.patch ];
  cmakeDir = "../cpp";
  # CMake takes CFLAGS and CXXFLAGS from the environment (cmakeFlags are
  # split at spaces).
  env = {
    CFLAGS = flags;
    CXXFLAGS = flags;
  }
  // lib.mapAttrs' (name: src: lib.nameValuePair "ARROW_${name}_URL" "file://${src}") deps;
  cmakeFlags = [
    "-DCMAKE_C_COMPILER_LAUNCHER=${noPic}"
    "-DCMAKE_CXX_COMPILER_LAUNCHER=${noPic}"
    "-DCMAKE_C_BYTE_ORDER=LITTLE_ENDIAN"
    # Its CMake knows wasm32 only as Emscripten.
    "-DARROW_CPU_FLAG=wasm"
    "-DARROW_BUILD_SHARED=OFF"
    "-DARROW_BUILD_STATIC=ON"
    "-DARROW_DEPENDENCY_SOURCE=BUNDLED"
    "-DARROW_DEPENDENCY_USE_SHARED=OFF"
    "-DZLIB_SOURCE=SYSTEM"
    "-DZLIB_ROOT=${zlib}"
    "-Dzstd_SOURCE=SYSTEM"
    "-Dzstd_ROOT=${zstd}"
    "-DARROW_ZSTD_USE_SHARED=OFF"
    "-DARROW_ENABLE_THREADING=OFF"
    "-DARROW_JEMALLOC=OFF"
    "-DARROW_MIMALLOC=OFF"
    "-DARROW_SIMD_LEVEL=NONE"
    "-DARROW_RUNTIME_SIMD_LEVEL=NONE"
    "-DARROW_COMPUTE=ON"
    "-DARROW_CSV=ON"
    "-DARROW_JSON=ON"
    "-DARROW_IPC=ON"
    "-DARROW_FILESYSTEM=ON"
    "-DARROW_ACERO=ON"
    "-DARROW_DATASET=ON"
    "-DARROW_PARQUET=ON"
    "-DARROW_WITH_SNAPPY=ON"
    "-DARROW_WITH_LZ4=ON"
    "-DARROW_WITH_ZSTD=ON"
    "-DARROW_WITH_ZLIB=ON"
    "-DARROW_WITH_UTF8PROC=ON"
    "-DARROW_WITH_RE2=ON"
    "-DARROW_WITH_BROTLI=OFF"
    "-DARROW_WITH_BZ2=OFF"
    "-DARROW_ORC=OFF"
    "-DARROW_SUBSTRAIT=OFF"
    "-DARROW_FLIGHT=OFF"
    "-DARROW_GANDIVA=OFF"
    "-DARROW_S3=OFF"
    "-DARROW_GCS=OFF"
    "-DARROW_AZURE=OFF"
    "-DARROW_HDFS=OFF"
    "-DARROW_WITH_OPENTELEMETRY=OFF"
    "-DARROW_WITH_BACKTRACE=OFF"
    "-DARROW_USE_CCACHE=OFF"
    "-DARROW_BUILD_TESTS=OFF"
    "-DARROW_BUILD_BENCHMARKS=OFF"
    "-DARROW_BUILD_UTILITIES=OFF"
    "-DPARQUET_REQUIRE_ENCRYPTION=OFF"
    "-DPARQUET_BUILD_EXECUTABLES=OFF"
  ];
}
