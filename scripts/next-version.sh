#!/usr/bin/env bash
# Print the release tag the commits since the last one call for, by their
# Conventional Commits types, or nothing if they don't call for a release.
#
#   breaking change (type!: or BREAKING CHANGE:)   major (minor before 1.0)
#   feat                                           minor
#   fix, perf, refactor, revert                    patch
#   anything else (docs, ci, build, chore, test)   none
#
# The first release is v0.1.0. Used by .github/workflows/tag.yml.
set -euo pipefail

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
range=HEAD
[ -n "$last" ] && range="$last..HEAD"
subjects=$(git log --format=%s "$range")
bodies=$(git log --format=%b "$range")
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
  echo v0.1.0
  exit 0
fi
IFS=. read -r major minor patch <<<"${last#v}"
patch=${patch%%[-+]*}
# Before 1.0, a breaking change is a new minor version.
if [ "$major" = 0 ] && [ "$bump" = major ]; then
  bump=minor
fi
case $bump in
  major) echo "v$((major + 1)).0.0" ;;
  minor) echo "v$major.$((minor + 1)).0" ;;
  patch) echo "v$major.$minor.$((patch + 1))" ;;
esac
