#!/usr/bin/env bash
# Build a WASI Python for a uv project (default: examples/numpy-app), or
# for a list of requirements. Builds the base CPython first if needed.
# Prints the interpreter directory on stdout.
#
#   scripts/build.sh [PROJECT_DIR] [-o OUT_DIR]
#   scripts/build.sh --with numpy --with pyyaml==6.0.3 [--preload numpy]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
if [ ! -f "$root/dist/python/bin/python.wasm" ]; then
  "$root/scripts/build-python.sh" >&2
fi
[ $# -gt 0 ] || set -- "$root/examples/numpy-app"
exec uv run --quiet --script "$root/toolchain/pywasi_build.py" "$@"
