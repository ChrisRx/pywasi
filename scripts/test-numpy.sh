#!/usr/bin/env bash
# Run numpy's own test suite under wazero, against numpy built with this
# repo's recipe and patches, plus the test-only extension modules that
# published bundles leave out. Needs the dev shell (nix develop).
#
#   scripts/test-numpy.sh                         # every test file
#   scripts/test-numpy.sh _core/tests/test_multiarray.py -k sort
#
# With no arguments each test file runs in its own interpreter, so a crash
# only loses that file. Arguments are passed to a single pytest run instead.
#
# Fails if a test file crashes the interpreter (a wasm trap) or times out.
# Other failures are listed but expected: WASI has no ctypes, mmap, dup,
# processes or floating-point exception flags.
#
# NUMPY_VERSION (default 2.5.4) picks the numpy to build.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
version=${NUMPY_VERSION:-2.5.4}
work="$root/build/numpy-tests"
home="$work/home-$version"
deps="$work/deps"
logs="$work/logs"
pytest_req=(pytest==9.2.0 hypothesis==6.150.0) # newer hypothesis has a native module

mkdir -p "$work"
echo "building numpy $version with test extensions..." >&2
"$root/scripts/build.sh" --with "numpy==$version" --all-extensions -o "$home" >/dev/null

if [ "$(cat "$deps/.installed" 2>/dev/null)" != "${pytest_req[*]}" ]; then
  rm -rf "$deps"
  uv pip install --quiet --target "$deps" --python-version 3.14 \
    --python-platform linux --only-binary :all: "${pytest_req[@]}"
  echo "${pytest_req[*]}" >"$deps/.installed"
fi

(cd "$root" && go build -o "$work/pywasi" ./cmd/pywasi)

# The CLI mounts the current directory at the same path, so run from the
# work directory, holding the runner and deps. Tests write temporary files
# there too: the guest has no /tmp, so tempfile uses the current directory.
cd "$work"
rm -rf runner .hypothesis pytest-of-* tmp*
cp -r "$root/toolchain/numpy-tests" runner
if [ $# -gt 0 ]; then
  exec "$work/pywasi" --home "$home" runner/run.py --deps deps "$@"
fi

numpy="$home/lib/python3.14/site-packages/numpy"
rm -rf "$logs"
mkdir -p "$logs"
crashed=()
while read -r file; do
  log="$logs/$(echo "$file" | tr / _).log"
  status=0
  timeout 900 "$work/pywasi" --home "$home" runner/run.py --deps deps "$file" >"$log" 2>&1 || status=$?
  if [ "$status" = 124 ]; then
    crashed+=("$file (timed out)")
  elif grep -q 'wasm error' "$log"; then
    crashed+=("$file ($(grep -o 'wasm error: .*' "$log" | head -1))")
  fi
  printf '%3s %s %s\n' "$status" "$file" "$(tail -1 "$log" | cut -c1-100)"
done < <(cd "$numpy" && find . -path '*/tests/test_*.py' | sed 's|^\./||' | sort)

python3 - "$logs" <<'EOF'
import pathlib, re, sys
totals = {}
for log in pathlib.Path(sys.argv[1]).glob("*.log"):
    lines = log.read_text(errors="replace").splitlines()
    for n, kind in re.findall(r"(\d+) (passed|failed|errors?|skipped|xfailed|xpassed)", lines[-1] if lines else ""):
        kind = "errors" if kind.startswith("error") else kind
        totals[kind] = totals.get(kind, 0) + int(n)
print("\ntotal: " + ", ".join(f"{totals[k]} {k}" for k in sorted(totals)))
EOF
echo "logs: $logs"

if [ ${#crashed[@]} -gt 0 ]; then
  printf '\ncrashed:\n' >&2
  printf '  %s\n' "${crashed[@]}" >&2
  exit 1
fi
