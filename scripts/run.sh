#!/usr/bin/env bash
# Run dist/python/bin/python.wasm under a WASI runtime, with dist/python
# mounted as the guest root so the stdlib is found at /lib/python3.X.
#
#   scripts/run.sh wasmtime -c 'print(1 + 1)'
#   scripts/run.sh wazero   -c 'print(1 + 1)'
#
# Set PYWASI_HOME to run a project build instead (e.g. examples/numpy-app/.pywasi).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dist="${PYWASI_HOME:-$root/dist/python}"
runtime="${1:?usage: run.sh wasmtime|wazero [python args...]}"
shift

case "$runtime" in
  wasmtime)
    # A linked interpreter imports pywasi's host functions (sockets); the
    # stub fails them (toolchain/pywasi_build.py builds it).
    preload=()
    stub="$root/build/pywasi-host-stub.wasm"
    [ -f "$stub" ] && preload=(--preload "pywasi=$stub")
    exec wasmtime run \
      "${preload[@]}" \
      --wasm max-wasm-stack=16777216 \
      --wasm exceptions=y \
      --dir "$dist::/" \
      --env PYTHONHOME=/ \
      --argv0 /bin/python \
      "$dist/bin/python.wasm" "$@"
    ;;
  wazero)
    # pywasi's own command: its wazero has exception handling turned on,
    # which extension modules use and the wazero CLI can't enable.
    cd "$root"
    exec go run ./cmd/pywasi -home "$dist" "$@"
    ;;
  *)
    echo "unknown runtime: $runtime" >&2
    exit 2
    ;;
esac
