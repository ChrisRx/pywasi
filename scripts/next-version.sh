#!/usr/bin/env bash
# Print the release tag the commits since a module's last one call for, by
# their Conventional Commits types, or nothing if they don't call for a
# release.
#
#   breaking change (type!: or BREAKING CHANGE:)   major (minor before 1.0)
#   feat                                           minor
#   fix, perf, refactor, revert                    patch
#   anything else (docs, ci, build, chore, test)   none
#
#   scripts/next-version.sh                    # the root module: v0.4.0
#   scripts/next-version.sh runtime/wasmtime   # a nested one: runtime/wasmtime/v0.2.0
#
# Only commits that change the module's files count: a nested module's
# directory, or for the root, everything but nested modules (directories
# with their own go.mod). A module's first release is v0.1.0. Used by
# .github/workflows/tag*.yml.
set -euo pipefail

module=${1:-}
module=${module%/}
prefix=""
pathspec=(.)
if [ -n "$module" ]; then
  [ -f "$module/go.mod" ] || { echo "next-version: $module has no go.mod" >&2; exit 2; }
  prefix="$module/"
  pathspec=("$module")
else
  while read -r gomod; do
    pathspec+=(":(exclude)$(dirname "$gomod")")
  done < <(git ls-files '*/go.mod')
fi

last=$(git describe --tags --abbrev=0 --match "${prefix}v[0-9]*" 2>/dev/null || true)
range=HEAD
[ -n "$last" ] && range="$last..HEAD"
subjects=$(git log --format=%s "$range" -- "${pathspec[@]}")
bodies=$(git log --format=%b "$range" -- "${pathspec[@]}")
[ -n "$subjects" ] || exit 0

bump=""
if grep -qE '^[a-z]+(\([^)]*\))?!:' <<<"$subjects" || grep -qE '^BREAKING[ -]CHANGE:' <<<"$bodies"; then
  bump=major
elif grep -qE '^feat(\([^)]*\))?:' <<<"$subjects"; then
  bump=minor
elif grep -qE '^(fix|perf|refactor|revert)(\([^)]*\))?:' <<<"$subjects"; then
  bump=patch
fi
[ -n "$bump" ] || exit 0

if [ -z "$last" ]; then
  echo "${prefix}v0.1.0"
  exit 0
fi
IFS=. read -r major minor patch <<<"${last#"$prefix"v}"
patch=${patch%%[-+]*}
# Before 1.0, a breaking change is a new minor version.
if [ "$major" = 0 ] && [ "$bump" = major ]; then
  bump=minor
fi
case $bump in
  major) echo "${prefix}v$((major + 1)).0.0" ;;
  minor) echo "${prefix}v$major.$((minor + 1)).0" ;;
  patch) echo "${prefix}v$major.$minor.$((patch + 1))" ;;
esac
