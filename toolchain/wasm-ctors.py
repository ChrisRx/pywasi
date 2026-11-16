#!/usr/bin/env python3
"""Defer the static constructors of a Python extension's link inputs.

Linked into python.wasm, an extension's static constructors (C++ globals,
__attribute__((constructor))) would run at process start, before Python is
initialized; ones that use the Python C API (pybind11 globals, say) then
corrupt the interpreter. Loaded as a shared library, they would run when
the module is imported. toolchain/wasi-cc restores that: this takes each
object's constructors out of its startup list (the linking section's
INIT_FUNCS), gives each a unique global name, and prints them for the
extension's entry point to call before PyInit_*.

It rewrites the inputs, not the linked extension, since wasm-ld can't
relink its own relocatable output reliably (it names functions twice).

Usage: wasm-ctors.py PREFIX INPUT OUTPUT

INPUT is an object or a static library (.a, .rlib), written to OUTPUT with
its constructors renamed to PREFIX<n>. Prints "<priority> <name>" for each,
in the order they would have run.
"""

import importlib.util
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

_spec = importlib.util.spec_from_file_location("wasm_localize", Path(__file__).with_name("wasm-localize.py"))
wl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(wl)

INIT_FUNCS = 6
COMDAT_INFO = 7
COMDAT_FUNCTION = 1


def write_leb(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        out.append(b | 0x80 if n else b)
        if not n:
            return bytes(out)


def subsection(sub_id, content):
    return bytes([sub_id]) + write_leb(len(content)) + content


def symbols(buf, pos):
    """[(start, end, kind, flags, index, name)] for the symbol table at pos."""
    count, pos = wl.read_leb(buf, pos)
    out = []
    for _ in range(count):
        start = pos
        kind = buf[pos]
        pos += 1
        flags, pos = wl.read_leb(buf, pos)
        index = name = None
        if kind in (wl.KIND_FUNCTION, wl.KIND_GLOBAL, wl.KIND_TAG, wl.KIND_TABLE):
            index, pos = wl.read_leb(buf, pos)
            if not flags & wl.FLAG_UNDEFINED or flags & wl.FLAG_EXPLICIT_NAME:
                name, pos = wl.read_name(buf, pos)
        elif kind == wl.KIND_DATA:
            name, pos = wl.read_name(buf, pos)
            if not flags & wl.FLAG_UNDEFINED:
                for _ in range(3):
                    _, pos = wl.read_leb(buf, pos)
        elif kind == wl.KIND_SECTION:
            _, pos = wl.read_leb(buf, pos)
        else:
            raise ValueError(f"unknown symbol kind {kind}")
        out.append((start, pos, kind, flags, index, name))
    return out


def comdat_functions(buf, subs):
    """Indices of the functions in COMDAT groups."""
    out = set()
    if COMDAT_INFO not in subs:
        return out
    _, p, _ = subs[COMDAT_INFO]
    count, p = wl.read_leb(buf, p)
    for _ in range(count):
        _, p = wl.read_name(buf, p)
        _, p = wl.read_leb(buf, p)  # flags
        n, p = wl.read_leb(buf, p)
        for _ in range(n):
            kind = buf[p]
            index, p = wl.read_leb(buf, p + 1)
            if kind == COMDAT_FUNCTION:
                out.add(index)
    return out


def defer(buf, prefix):
    """(object without INIT_FUNCS, [(priority, name)] in run order)."""
    start, body, end, subs = wl.linking_section(buf)
    if INIT_FUNCS not in subs:
        return buf, []
    _, p, _ = subs[INIT_FUNCS]
    count, p = wl.read_leb(buf, p)
    entries = []
    for i in range(count):
        priority, p = wl.read_leb(buf, p)
        index, p = wl.read_leb(buf, p)
        entries.append((priority, i, index))

    # Constructors in a COMDAT group (initializers of C++ inline variables
    # and template static members, in every translation unit using them)
    # stay at process start: the final link keeps one copy of the group,
    # and only a constructor it keeps can run. Deferred, the copy called
    # could be one it dropped, leaving the variable uninitialized.
    syms = symbols(buf, subs[wl.SYMTAB][1])
    in_comdat = comdat_functions(buf, subs)
    stay = [e for e in entries if syms[e[2]][2] == wl.KIND_FUNCTION and syms[e[2]][4] in in_comdat]
    inits = sorted(e for e in entries if e not in stay)
    if not inits:
        return buf, []
    renamed = {index: f"{prefix}{n}" for n, (_, _, index) in enumerate(inits)}

    # The symbol table, with the constructors renamed and made global.
    table = bytearray(write_leb(len(syms)))
    for i, (s_start, s_end, kind, flags, index, name) in enumerate(syms):
        if i not in renamed:
            table += buf[s_start:s_end]
            continue
        if kind != wl.KIND_FUNCTION or flags & wl.FLAG_UNDEFINED:
            raise ValueError(f"constructor symbol {i} ({name}) isn't a defined function")
        new = renamed[i].encode()
        table += bytes([kind]) + write_leb(flags & ~wl.BINDING_MASK) + write_leb(index)
        table += write_leb(len(new)) + new

    # The linking section with that table, and INIT_FUNCS with only the
    # constructors that stay. Relocations refer to symbols and sections by
    # index, so nothing they point at moves.
    _, p = wl.read_name(buf, body)
    _, p = wl.read_leb(buf, p)  # version
    content = bytearray(buf[body:p])
    for sub_id, (sub_start, _, sub_end) in sorted(subs.items(), key=lambda kv: kv[1][0]):
        if sub_id == wl.SYMTAB:
            content += subsection(wl.SYMTAB, bytes(table))
        elif sub_id == INIT_FUNCS:
            if stay:
                kept = bytearray(write_leb(len(stay)))
                for priority, _, index in stay:
                    kept += write_leb(priority) + write_leb(index)
                content += subsection(INIT_FUNCS, bytes(kept))
        else:
            content += buf[sub_start:sub_end]
    section = b"\x00" + write_leb(len(content)) + bytes(content)
    return buf[:start] + section + buf[end:], [(prio, renamed[index]) for prio, _, index in inits]


def defer_archive(path, out, prefix, ar):
    """Write the archive to out with its members' constructors deferred."""
    members = subprocess.run([ar, "t", path], check=True, capture_output=True, text=True).stdout.splitlines()
    # A thin archive (meson makes them) only names its members' files.
    with open(path, "rb") as f:
        thin = f.read(8) == b"!<thin>\n"
    found = []
    with tempfile.TemporaryDirectory() as tmp:
        files = []
        seen = {}
        for i, name in enumerate(members):
            d = Path(tmp) / str(i)
            d.mkdir()
            member = d / Path(name).name
            if thin:
                # llvm-ar lists them relative to the current directory.
                source = Path(name) if os.path.exists(name) else Path(path).parent / name
                member.write_bytes(source.read_bytes())
            else:
                # Members can share a name: extract the n-th one.
                seen[name] = seen.get(name, 0) + 1
                subprocess.run([ar, "xN", str(seen[name]), os.path.abspath(path), name], cwd=d, check=True)
            data = member.read_bytes()
            if data[:4] == wl.WASM_MAGIC:
                data, ctors = defer(bytearray(data), f"{prefix}{i}_")
                if ctors:
                    member.write_bytes(data)
                    found += ctors
            files.append(str(member))
        if not found and not thin:
            shutil.copyfile(path, out)
            return []
        if os.path.exists(out):
            os.unlink(out)
        subprocess.run([ar, "qcs", out, *files], check=True)
    return found


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__.strip().splitlines()[-6])
    prefix, path, out = sys.argv[1:]
    data = Path(path).read_bytes()
    if data[:4] == wl.WASM_MAGIC:
        data, ctors = defer(bytearray(data), prefix)
        Path(out).write_bytes(data)
    else:
        ar = os.path.join(os.environ["WASI_SDK_PATH"], "bin", "llvm-ar")
        ctors = defer_archive(path, out, prefix, ar)
    for prio, name in ctors:
        print(prio, name)


if __name__ == "__main__":
    main()
