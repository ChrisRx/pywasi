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
    exec wasmtime run \
      --wasm max-wasm-stack=16777216 \
      --dir "$dist::/" \
      --env PYTHONHOME=/ \
      --argv0 /bin/python \
      "$dist/bin/python.wasm" "$@"
    ;;
  wazero)
    exec wazero run \
      -cachedir="$root/build/wazero-cache" \
      -mount="$dist:/" \
      -env=PYTHONHOME=/ \
      "$dist/bin/python.wasm" "$@"
    ;;
  *)
    echo "unknown runtime: $runtime" >&2
    exit 2
    ;;
esac
