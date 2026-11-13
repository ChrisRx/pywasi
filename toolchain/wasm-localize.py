#!/usr/bin/env python3
"""Make every defined symbol in a relocatable wasm object local, except
those matching a keep pattern (by default the PyInit_* entry points).

Each Python extension module is linked into python.wasm as one relocatable
object. Extensions routinely carry private copies of the same helper code
(numpy compiles lapack_lite into two modules), which would collide in a
static link. Shared-library builds avoid this because only PyInit_ is
exported, so this reproduces that: everything else becomes LOCAL binding
and no longer participates in cross-object symbol resolution.

llvm-objcopy can't change symbol bindings in wasm objects, so this edits
the symbol table in the "linking" custom section directly. Binding lives
in the low two bits of the flags LEB128, so the patch never changes its
encoded length and is done in place.

Usage: wasm-localize.py OBJECT [--keep REGEX]
"""

import argparse
import re
import sys

WASM_MAGIC = b"\x00asm"

SYMTAB = 8

KIND_FUNCTION = 0
KIND_DATA = 1
KIND_GLOBAL = 2
KIND_SECTION = 3
KIND_TAG = 4
KIND_TABLE = 5

BINDING_MASK = 0x3
BINDING_LOCAL = 0x2
FLAG_UNDEFINED = 0x10
FLAG_EXPLICIT_NAME = 0x40


def read_leb(buf, pos):
    result = shift = 0
    while True:
        b = buf[pos]
        pos += 1
        result |= (b & 0x7F) << shift
        shift += 7
        if not b & 0x80:
            return result, pos


def read_name(buf, pos):
    n, pos = read_leb(buf, pos)
    return bytes(buf[pos : pos + n]).decode("utf-8"), pos + n


def patch_flags(buf, pos, flags):
    """Rewrite the LEB128 at pos with flags, keeping its encoded length."""
    while True:
        more = buf[pos] & 0x80
        buf[pos] = (flags & 0x7F) | more
        flags >>= 7
        pos += 1
        if not more:
            return


def localize_symtab(buf, pos, end, keep):
    count, pos = read_leb(buf, pos)
    changed = kept = 0
    for _ in range(count):
        kind = buf[pos]
        pos += 1
        flags_pos = pos
        flags, pos = read_leb(buf, pos)
        undefined = flags & FLAG_UNDEFINED
        name = None
        if kind in (KIND_FUNCTION, KIND_GLOBAL, KIND_TAG, KIND_TABLE):
            _, pos = read_leb(buf, pos)
            if not undefined or flags & FLAG_EXPLICIT_NAME:
                name, pos = read_name(buf, pos)
        elif kind == KIND_DATA:
            name, pos = read_name(buf, pos)
            if not undefined:
                for _ in range(3):  # segment index, offset, size
                    _, pos = read_leb(buf, pos)
        elif kind == KIND_SECTION:
            _, pos = read_leb(buf, pos)
            continue
        else:
            raise ValueError(f"unknown symbol kind {kind}")

        if undefined or name is None or (flags & BINDING_MASK) == BINDING_LOCAL:
            continue
        if keep.search(name):
            kept += 1
            continue
        patch_flags(buf, flags_pos, (flags & ~BINDING_MASK) | BINDING_LOCAL)
        changed += 1
    if pos != end:
        raise ValueError("symbol table size mismatch")
    return changed, kept


def localize(buf, keep):
    if buf[:4] != WASM_MAGIC:
        raise ValueError("not a wasm object")
    pos = 8
    while pos < len(buf):
        sec_id = buf[pos]
        size, body = read_leb(buf, pos + 1)
        end = body + size
        if sec_id == 0:
            name, p = read_name(buf, body)
            if name == "linking":
                _version, p = read_leb(buf, p)
                while p < end:
                    sub = buf[p]
                    sub_size, sub_body = read_leb(buf, p + 1)
                    if sub == SYMTAB:
                        return localize_symtab(buf, sub_body, sub_body + sub_size, keep)
                    p = sub_body + sub_size
        pos = end
    raise ValueError("no linking/symbol table section (not a relocatable object?)")


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("object")
    ap.add_argument("--keep", default=r"^PyInit_")
    args = ap.parse_args()

    with open(args.object, "rb") as f:
        buf = bytearray(f.read())
    changed, kept = localize(buf, re.compile(args.keep))
    if kept == 0:
        print(f"wasm-localize: warning: nothing matched {args.keep!r} in {args.object}", file=sys.stderr)
    with open(args.object, "wb") as f:
        f.write(buf)


if __name__ == "__main__":
    main()
