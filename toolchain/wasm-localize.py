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

--keep-weak REGEX keeps matching weak definitions too: an extension linked
against a library (toolchain/wasi-cc) shares the library's inline code and
type info, as it would with a real shared library, so dynamic_cast and
catch work across them.

--weaken-undefined REGEX makes matching undefined symbols weak: what's left
of definitions the link dropped (constructors of inline variables, which
are in a COMDAT group in every translation unit using them, toolchain/
wasm-ctors.py having named each copy), so the final link doesn't require
them.

Usage: wasm-localize.py OBJECT [--keep REGEX] [--keep-weak REGEX] [--weaken-undefined REGEX]
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
BINDING_WEAK = 0x1
BINDING_LOCAL = 0x2
# Mangled names in libc++'s namespace (std::__2): functions and methods,
# and vtables, type names and type info.
LIBCXX = re.compile(r"_Z(?:N[KVRO]*|T[VSI]N?)St3__2")
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


def localize_symtab(buf, pos, end, keep, keep_weak=None, weaken_undefined=None, func_imports=()):
    count, pos = read_leb(buf, pos)
    changed = kept = 0
    for _ in range(count):
        kind = buf[pos]
        pos += 1
        flags_pos = pos
        flags, pos = read_leb(buf, pos)
        undefined = flags & FLAG_UNDEFINED
        name = None
        index = None
        if kind in (KIND_FUNCTION, KIND_GLOBAL, KIND_TAG, KIND_TABLE):
            index, pos = read_leb(buf, pos)
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

        if undefined and name is None and kind == KIND_FUNCTION:
            name = func_imports[index] if index < len(func_imports) else None
        if undefined and name and weaken_undefined and weaken_undefined.search(name):
            patch_flags(buf, flags_pos, (flags & ~BINDING_MASK) | BINDING_WEAK)
            continue
        if undefined or name is None or (flags & BINDING_MASK) == BINDING_LOCAL:
            continue
        # Tags (__cpp_exception, __c_longjmp) identify what is thrown, so
        # every object must share them: a catch in an extension has to match
        # a throw from libc++ in the final link.
        if kind == KIND_TAG:
            continue
        # Weak definitions of libc++'s inline templates stay shared: libc++.a
        # in the final link refers to some of them (std::__pad_and_output)
        # and expects the program to provide them, as any user of its
        # headers does. They all come from the same wasi-sdk headers, so
        # which copy the linker keeps doesn't matter.
        if (flags & BINDING_MASK) == BINDING_WEAK and (LIBCXX.match(name) or (keep_weak and keep_weak.search(name))):
            continue
        if keep.search(name):
            kept += 1
            continue
        patch_flags(buf, flags_pos, (flags & ~BINDING_MASK) | BINDING_LOCAL)
        changed += 1
    if pos != end:
        raise ValueError("symbol table size mismatch")
    return changed, kept


def linking_section(buf):
    """(section start, body start, end, {subsection id: (body start, end)})."""
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
                subs = {}
                while p < end:
                    sub = buf[p]
                    sub_size, sub_body = read_leb(buf, p + 1)
                    subs[sub] = (p, sub_body, sub_body + sub_size)
                    p = sub_body + sub_size
                return pos, body, end, subs
        pos = end
    raise ValueError("no linking section (not a relocatable object?)")


def function_imports(buf):
    """Names of the imported functions, in order: an undefined function
    symbol without an explicit name has its import's."""
    names = []
    pos = 8
    while pos < len(buf):
        sec_id = buf[pos]
        size, body = read_leb(buf, pos + 1)
        if sec_id == 2:  # import
            count, p = read_leb(buf, body)
            for _ in range(count):
                _, p = read_name(buf, p)  # module
                field, p = read_name(buf, p)
                kind = buf[p]
                p += 1
                if kind == 0:  # function: type index
                    _, p = read_leb(buf, p)
                    names.append(field)
                elif kind == 1:  # table: type, limits
                    p += 1
                    lim = buf[p]
                    _, p = read_leb(buf, p + 1)
                    if lim & 1:
                        _, p = read_leb(buf, p)
                elif kind == 2:  # memory: limits
                    lim = buf[p]
                    _, p = read_leb(buf, p + 1)
                    if lim & 1:
                        _, p = read_leb(buf, p)
                elif kind == 3:  # global: type, mutability
                    p += 2
                elif kind == 4:  # tag: attribute, type index
                    _, p = read_leb(buf, p + 1)
                else:
                    raise ValueError(f"unknown import kind {kind}")
            return names
        pos = body + size
    return names


def localize(buf, keep, keep_weak=None, weaken_undefined=None):
    _, _, _, subs = linking_section(buf)
    if SYMTAB not in subs:
        raise ValueError("no symbol table (not a relocatable object?)")
    _, body, end = subs[SYMTAB]
    imports = function_imports(buf) if weaken_undefined else ()
    return localize_symtab(buf, body, end, keep, keep_weak, weaken_undefined, imports)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("object")
    ap.add_argument("--keep", default=r"^PyInit_")
    ap.add_argument("--keep-weak")
    ap.add_argument("--weaken-undefined")
    args = ap.parse_args()

    with open(args.object, "rb") as f:
        buf = bytearray(f.read())
    changed, kept = localize(
        buf,
        re.compile(args.keep),
        args.keep_weak and re.compile(args.keep_weak),
        args.weaken_undefined and re.compile(args.weaken_undefined),
    )
    if kept == 0:
        print(f"wasm-localize: warning: nothing matched {args.keep!r} in {args.object}", file=sys.stderr)
    with open(args.object, "wb") as f:
        f.write(buf)


if __name__ == "__main__":
    main()
