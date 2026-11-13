#!/usr/bin/env python3
"""Publish linker, link kits and bundles to R2 and add them to the catalog.

The catalog (index.json at the bucket root) is what pywasi.WithPackages
links packages with C extensions from (see linker.go):

    {"version": 1,
     "linker": {"url", "sha256"},
     "kits": [{"url", "sha256", "abi", "python"}],
     "bundles": [{"url", "sha256", "name", "version", "abi", "requires"}]}

Kits and bundles come from `scripts/build.sh ... --kit FILE --bundles DIR`,
and the linker from `nix build .#lld-wasi`. Their metadata is read from
inside the archives. Run in the dev shell (Python 3.14 for zstd):

    scripts/catalog.py --linker result/bin/lld.wasm --kit kit.tar.zst --bundle numpy-*.tar.zst

Uploads go through scripts/publish.sh (credentials as documented there).
Entries already in the catalog are kept; a kit or bundle with the same
sha256 isn't added twice.
"""

import argparse
import json
import subprocess
import sys
import tarfile
import tempfile
import urllib.error
import urllib.request
from compression import zstd
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PUBLIC = "https://pywasi.chrisrx.dev"


def publish(path: Path, prefix: str, *flags) -> tuple[str, str]:
    out = subprocess.run(
        [ROOT / "scripts" / "publish.sh", path, prefix, *flags], check=True, stdout=subprocess.PIPE, text=True
    ).stdout.split()
    url, digest = out[-2], out[-1]
    return url, digest.removeprefix("sha256:")


def member_json(archive: Path, name: str) -> dict:
    with tarfile.open(archive, "r:zst") as t:
        return json.load(t.extractfile(name))


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--linker", type=Path, help="lld.wasm (compressed with zstd before upload)")
    ap.add_argument("--kit", type=Path, action="append", default=[])
    ap.add_argument("--bundle", type=Path, action="append", default=[])
    ap.add_argument("--dry-run", action="store_true", help="print the new catalog without publishing it")
    args = ap.parse_args()

    try:
        # Cloudflare turns away urllib's default User-Agent.
        req = urllib.request.Request(f"{PUBLIC}/index.json", headers={"User-Agent": "pywasi-catalog"})
        with urllib.request.urlopen(req) as resp:
            index = json.load(resp)
    except urllib.error.HTTPError as e:
        if e.code != 404:
            raise
        index = {"version": 1, "linker": None, "kits": [], "bundles": []}

    if args.linker:
        with tempfile.TemporaryDirectory() as tmp:
            packed = Path(tmp) / "lld.wasm.zst"
            packed.write_bytes(zstd.compress(args.linker.read_bytes(), level=19))
            url, digest = publish(packed, "linker")
        index["linker"] = {"url": url, "sha256": digest}

    for kit in args.kit:
        meta = member_json(kit, "link.json")
        url, digest = publish(kit, "kits")
        if not any(k["sha256"] == digest for k in index["kits"]):
            index["kits"].append({"url": url, "sha256": digest, "abi": meta["abi"], "python": meta["python"]})

    for bundle in args.bundle:
        meta = member_json(bundle, "bundle.json")
        url, digest = publish(bundle, "bundles")
        if not any(b["sha256"] == digest for b in index["bundles"]):
            index["bundles"].append(
                {
                    "url": url,
                    "sha256": digest,
                    "name": meta["name"],
                    "version": meta["package_version"],
                    "abi": meta["abi"],
                    "requires": meta["requires"],
                }
            )

    text = json.dumps(index, indent=2) + "\n"
    if args.dry_run:
        print(text)
        return
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "index.json"
        path.write_text(text)
        publish(path, "/", "--replace")
    print(f"published {PUBLIC}/index.json", file=sys.stderr)


if __name__ == "__main__":
    main()
