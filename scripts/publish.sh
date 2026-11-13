#!/usr/bin/env bash
# Upload a file to the pywasi R2 bucket under a content-addressed name and
# print its public URL and digest, ready for pywasi.ArchiveURL.
#
#   scripts/publish.sh FILE PREFIX [--keep-name]
#   scripts/publish.sh build/archives/python3.14-stdlib.tar.zst interpreters
#     -> https://pywasi.chrisrx.dev/interpreters/python3.14-stdlib-<sha12>.tar.zst
#
# --keep-name uploads as PREFIX/<file name>, for files that are already
# versioned (e.g. the uv mirror: uv/0.13.7/uv-x86_64-unknown-linux-musl.tar.gz).
# --replace uploads as PREFIX/<file name> and overwrites it, with a short
# cache lifetime, for the one mutable file: the catalog (index.json).
#
# Credentials come from the environment (R2_ACCOUNT_ID, R2_ACCESS_KEY_ID,
# R2_SECRET_ACCESS_KEY), or from the file named by $PYWASI_R2_ENV.
# PYWASI_BUCKET and PYWASI_PUBLIC_URL override the bucket and public URL.
# Existing objects are never overwritten.
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ] || { [ $# -eq 3 ] && [ "$3" != --keep-name ] && [ "$3" != --replace ]; }; then
  echo "usage: $0 FILE PREFIX [--keep-name|--replace]" >&2
  exit 2
fi
file=$1
prefix=${2%/}
mode=${3:-}

if [ -n "${PYWASI_R2_ENV:-}" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$PYWASI_R2_ENV"
  set +a
fi
: "${R2_ACCOUNT_ID:?not set}" "${R2_ACCESS_KEY_ID:?not set}" "${R2_SECRET_ACCESS_KEY:?not set}"
bucket=${PYWASI_BUCKET:-pywasi}
public=${PYWASI_PUBLIC_URL:-https://pywasi.chrisrx.dev}

digest=$(sha256sum "$file" | cut -d' ' -f1)
base=$(basename "$file")
case "$base" in
  *.tar.zst) stem=${base%.tar.zst} ext=.tar.zst ;;
  *.tar.gz) stem=${base%.tar.gz} ext=.tar.gz ;;
  *.wasm.zst) stem=${base%.wasm.zst} ext=.wasm.zst ;;
  *.*) stem=${base%.*} ext=.${base##*.} ;;
  *) stem=$base ext= ;;
esac
key="$prefix/$stem-${digest:0:12}$ext"
[ -z "$mode" ] || key="$prefix/$base"
key=${key#/}
cache_control="public, max-age=31536000, immutable"
[ "$mode" != --replace ] || cache_control="public, max-age=300"
endpoint="https://$R2_ACCOUNT_ID.r2.cloudflarestorage.com/$bucket/$key"
auth=(--aws-sigv4 "aws:amz:auto:s3" --user "$R2_ACCESS_KEY_ID:$R2_SECRET_ACCESS_KEY")

status=$(curl -sS -o /dev/null -w '%{http_code}' -I "${auth[@]}" "$endpoint")
if [ "$status" = 200 ] && [ "$mode" != --replace ]; then
  echo "already published: $key" >&2
else
  echo "uploading $file -> $key" >&2
  curl -sS --fail-with-body "${auth[@]}" \
    -H "x-amz-content-sha256: $digest" \
    -H "Content-Type: application/octet-stream" \
    -H "Cache-Control: $cache_control" \
    -T "$file" "$endpoint" >&2
fi

echo "$public/$key sha256:$digest"
