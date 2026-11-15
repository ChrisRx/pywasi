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
: "${PYWASI_OPENSSL:?run inside nix develop}"
: "${PYWASI_SQLITE:?run inside nix develop}"
: "${PYWASI_BZIP2:?run inside nix develop}"
: "${PYWASI_XZ:?run inside nix develop}"

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

# Patches to CPython (toolchain/patches/cpython-*.patch), skipping any that
# are already applied.
for p in "$root"/toolchain/patches/cpython-*.patch; do
  if patch -p1 -N --dry-run --silent <"$p" >/dev/null 2>&1; then
    patch -p1 -N --silent <"$p"
  fi
done

# Sockets: wasi-libc's wasm32-wasip1 build leaves out socket, bind, listen,
# connect and name resolution. toolchain/support/pywasi_socket.c provides
# them through pywasi's host functions, and its headers declare them, so
# configure finds them and _socket gets the full API. CPython is linked
# with the stub build, which fails with ENOSYS and needs no host functions,
# so this interpreter still runs under wasmtime. pywasi_build.py links the
# real one.
support="$root/toolchain/support"
socket_stub="$root/build/pywasi_socket_stub.o"
"$WASI_SDK_PATH/bin/clang" --target=wasm32-wasip1 --sysroot="$WASI_SDK_PATH/share/wasi-sysroot" \
  -O2 -I"$support/include" -DPYWASI_SOCKET_STUB -c "$support/pywasi_socket.c" -o "$socket_stub"

# Native "build Python" first. It runs the build's own Python scripts and
# must not see the wasm C libraries below.
python3 Tools/wasm/wasi build-python --quiet --logdir "$log_dir"

# Then the WASI host Python, with the C libraries from nix/c-libs.nix
# (zlib, OpenSSL for ssl and hashlib, SQLite, bzip2 and xz). configure finds
# optional C libraries through pkg-config, which the WASI build points at
# the (read-only) wasi-sdk sysroot, so pass them in directly. --prefix=/
# makes the installed tree relocatable to the guest root, and the test
# suite is left out. SIMD128 lets clang vectorize loops (wasmtime and wazero
# both support it). zoneinfo looks for time zones in share/zoneinfo of the
# home, where pywasi_build.py installs them. configure turns IPv6 off for
# WASI, which the socket shim supports, so turn it back on.
#
# Without clock_nanosleep (in libc or librt), time.sleep uses nanosleep.
# clock_nanosleep sleeps until an absolute deadline, which wasi-libc passes
# to poll_oneoff as an absolute clock subscription, and some runtimes
# (wazero) only support relative ones.
CFLAGS="-msimd128 -I$support/include" \
ZLIB_CFLAGS="-I$PYWASI_ZLIB/include" \
ZLIB_LIBS="-L$PYWASI_ZLIB/lib -lz" \
LIBSQLITE3_CFLAGS="-I$PYWASI_SQLITE/include" \
LIBSQLITE3_LIBS="-L$PYWASI_SQLITE/lib -lsqlite3" \
BZIP2_CFLAGS="-I$PYWASI_BZIP2/include" \
BZIP2_LIBS="-L$PYWASI_BZIP2/lib -lbz2" \
LIBLZMA_CFLAGS="-I$PYWASI_XZ/include" \
LIBLZMA_LIBS="-L$PYWASI_XZ/lib -llzma" \
  python3 Tools/wasm/wasi build-host --quiet --logdir "$log_dir" -- --prefix=/ --disable-test-modules \
  --with-openssl="$PYWASI_OPENSSL" --with-tzpath=/share/zoneinfo --enable-ipv6 \
  ac_cv_func_clock_nanosleep=no ac_cv_lib_rt_clock_nanosleep=no \
  LIBS="$socket_stub"

host_dir="$cpython/cross-build/wasm32-wasip1"

# configure quietly leaves out a module whose library it can't find or
# compile against, so check that every one pywasi ships was built.
missing=()
for m in _bz2 _hashlib _lzma _socket _sqlite3 _ssl _zoneinfo zlib; do
  grep -Eq "^$m( |\$)" "$host_dir/Modules/Setup.stdlib" "$host_dir/Modules/Setup.bootstrap" 2>/dev/null ||
    missing+=("$m")
done
if [ ${#missing[@]} -gt 0 ]; then
  echo "error: modules not built: ${missing[*]} (see $host_dir/config.log)" >&2
  exit 1
fi
rm -rf "$dist"
make -C "$host_dir" install DESTDIR="$dist" >"$log_dir/install.log" 2>&1

# Normalize the interpreter name so runners don't depend on the version.
wasm="$(find "$dist/bin" -name 'python3.*.wasm' -type f | head -n1)"
mv "$wasm" "$dist/bin/python.wasm"
find "$dist/bin" -type l -delete

echo "built $dist/bin/python.wasm ($(du -h "$dist/bin/python.wasm" | cut -f1))"
