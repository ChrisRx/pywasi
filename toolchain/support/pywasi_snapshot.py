"""Runs inside python.wasm (embedded by toolchain/support/pywasi_main.c).

snapshot_init() runs once at build time under Wizer. resume() runs at the
start of every process launched from the snapshot, in place of CPython's
normal command line handling.
"""

import importlib
import os
import sys


def snapshot_init(preload):
    for name in preload.split(","):
        if name := name.strip():
            importlib.import_module(name)


def resume(argv, env):
    import runpy

    # Replace process state that was captured in the snapshot.
    os.environ._data.clear()
    os.environ._data.update(env)
    sys.orig_argv = list(argv)
    # PYTHONPATH was read when the interpreter started at build time.
    extra = os.environ.get("PYTHONPATH", "")
    for path in reversed([p for p in extra.split(os.pathsep) if p]):
        if path not in sys.path:
            sys.path.insert(0, path)
    sys.stdout.reconfigure(line_buffering=sys.stdout.isatty())
    if "random" in sys.modules:
        sys.modules["random"].seed()

    args = argv[1:]
    main = sys.modules["__main__"]
    if args[:1] == ["-c"] and len(args) >= 2:
        sys.argv = ["-c", *args[2:]]
        sys.path.insert(0, "")
        exec(compile(args[1], "<string>", "exec"), main.__dict__)
    elif args[:1] == ["-m"] and len(args) >= 2:
        sys.argv = ["-m", *args[2:]]
        sys.path.insert(0, "")
        runpy._run_module_as_main(args[1])
    elif not args or args[0] == "-":
        sys.argv = ["-", *args[1:]] if args else [""]
        sys.path.insert(0, "")
        exec(compile(sys.stdin.read(), "<stdin>", "exec"), main.__dict__)
    elif args[0].startswith("-"):
        sys.exit(
            f"pywasi: option {args[0]} is not supported by the pre-initialized "
            "interpreter (only -c, -m, a script or -); build with snapshot = false"
        )
    else:
        sys.argv = list(args)
        sys.path.insert(0, os.path.dirname(os.path.abspath(args[0])))
        runpy.run_path(args[0], run_name="__main__")
