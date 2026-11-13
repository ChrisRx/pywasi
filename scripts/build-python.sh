#!/usr/bin/env bash
# Build CPython for wasm32-wasip1 with wasi-sdk and install it into
# dist/python as a self-contained tree:
#
#   dist/python/bin/python.wasm
#   dist/python/lib/python3.X/...
#
# Run from the dev shell (`nix develop`), which provides WASI_SDK_PATH,
# wasmtime (used by CPython's build to run the WASI interpreter) and a
# native toolchain for the build Python.
set -euo pipefail

PY_VERSION="${PY_VERSION:-3.14.10}"
PY_SHA256="${PY_SHA256:-de414a57119abef52784d8993e341d53aed635fc2b05c2607d9f98a2c5b72032}"

root="$(cd "$(dirname "$0")/.." && pwd)"
src_dir="$root/build/src"
cpython="$src_dir/Python-$PY_VERSION"
log_dir="$root/build/logs"
dist="$root/dist/python"

: "${WASI_SDK_PATH:?run inside nix develop}"
: "${PYWASI_ZLIB:?run inside nix develop}"

mkdir -p "$src_dir" "$log_dir"

if [ ! -d "$cpython" ]; then
  tarball="$src_dir/Python-$PY_VERSION.tar.xz"
  if [ ! -f "$tarball" ]; then
    curl -fsSL -o "$tarball" "https://www.python.org/ftp/python/$PY_VERSION/Python-$PY_VERSION.tar.xz"
  fi
  echo "$PY_SHA256  $tarball" | sha256sum -c -
  tar -xJf "$tarball" -C "$src_dir"
fi

cd "$cpython"

# Native "build Python" first. It runs the build's own Python scripts and
# must not see the wasm C libraries below.
python3 Tools/wasm/wasi build-python --quiet --logdir "$log_dir"

# Then the WASI host Python. configure finds optional C libraries through
# pkg-config, which the WASI build points at the (read-only) wasi-sdk
# sysroot, so pass them in directly. --prefix=/ makes the installed tree
# relocatable to the guest root, and the test suite is left out. SIMD128
# lets clang vectorize loops (wasmtime and wazero both support it).
#
# Without clock_nanosleep (in libc or librt), time.sleep uses nanosleep.
# clock_nanosleep sleeps
# until an absolute deadline, which wasi-libc passes to poll_oneoff as an
# absolute clock subscription, and some runtimes (wazero) only support
# relative ones.
CFLAGS="-msimd128" \
ZLIB_CFLAGS="-I$PYWASI_ZLIB/include" \
ZLIB_LIBS="-L$PYWASI_ZLIB/lib -lz" \
  python3 Tools/wasm/wasi build-host --quiet --logdir "$log_dir" -- --prefix=/ --disable-test-modules \
  ac_cv_func_clock_nanosleep=no ac_cv_lib_rt_clock_nanosleep=no

host_dir="$cpython/cross-build/wasm32-wasip1"
rm -rf "$dist"
make -C "$host_dir" install DESTDIR="$dist" >"$log_dir/install.log" 2>&1

# Normalize the interpreter name so runners don't depend on the version.
wasm="$(find "$dist/bin" -name 'python3.*.wasm' -type f | head -n1)"
mv "$wasm" "$dist/bin/python.wasm"
find "$dist/bin" -type l -delete

echo "built $dist/bin/python.wasm ($(du -h "$dist/bin/python.wasm" | cut -f1))"
