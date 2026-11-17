"""Runs inside python.wasm (embedded by toolchain/support/pywasi_main.c).

snapshot_init() runs once at build time under Wizer. resume() runs at the
start of every process launched from the snapshot, in place of CPython's
normal command line handling.
"""

import importlib
import os
import sys


def snapshot_init(preload):
    """Import the preload modules, skipping any that fail.

    pywasi preloads the packages a run asked for, which may fail to import
    here (needing something only present at run time, say). A failed import
    leaves the module and what it imported half-initialized, so those are
    taken out of sys.modules again, and the run imports them itself.
    """
    for name in preload.split(","):
        if not (name := name.strip()):
            continue
        before = set(sys.modules)
        try:
            importlib.import_module(name)
        except BaseException as e:
            for added in set(sys.modules) - before:
                del sys.modules[added]
            print(f"pywasi: not preloading {name}: {type(e).__name__}: {e}", file=sys.stderr)


def run(argv, env):
    """Run a program from the snapshot (resume), then shut down, and return
    the exit status.

    Each run is a throwaway instance, so the interpreter isn't finalized:
    tearing down every module and collecting the heap takes most of a second
    with large packages preloaded, and its memory is discarded anyway. What
    programs rely on at exit still happens: atexit handlers run (which
    flushes logging and cleans up tempfile's directories), and files opened
    with open() and the standard streams are flushed.
    """
    import atexit

    opened = _track_open()
    try:
        resume(argv, env)
        code = 0
    except SystemExit as e:
        code = _exit_status(e)
    except BaseException:
        sys.excepthook(*sys.exc_info())
        code = 1
    atexit._run_exitfuncs()
    for f in [*opened, sys.stdout, sys.stderr]:
        try:
            if not f.closed:
                f.flush()
        except Exception:
            pass
    return code


def _exit_status(e):
    """The process exit status for SystemExit e, as CPython's."""
    if e.code is None:
        return 0
    if isinstance(e.code, int):
        return e.code & 0xFF
    print(e.code, file=sys.stderr)
    return 1


def _track_open():
    """Keep weak references to the files open() and io.open (pathlib, too)
    return from now on, to flush the ones still open at exit."""
    import builtins
    import functools
    import io
    import weakref

    opened = weakref.WeakSet()
    original = io.open

    @functools.wraps(original)
    def open(*args, **kwargs):
        f = original(*args, **kwargs)
        opened.add(f)
        return f

    builtins.open = io.open = open
    return opened


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
    elif not args and sys.stdin.isatty():
        sys.argv = [""]
        sys.path.insert(0, "")
        interact(main)
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


def banner():
    """The REPL's first line, as CPython's, with the version of pywasi
    running it ($PYWASI_VERSION). sys.version won't do as is: CPython cuts
    its compiler part at 80 characters, which wasi-sdk's clang, naming the
    LLVM repository and commit, runs past, and its build date is the
    reproducible build's (1980)."""
    import re

    version = sys.version.split()[0]
    m = re.search(r"\[Clang ([\w.]+)(-wasi-sdk)?", sys.version)
    compiler = f" [Clang {m[1]}{', wasi-sdk' if m[2] else ''}]" if m else ""
    pywasi = os.environ.get("PYWASI_VERSION")
    return f"Python {version}{compiler} on {sys.platform}" + (f" (pywasi {pywasi})" if pywasi else "")


def interact(main):
    """The interactive prompt, as python starts it on a terminal: CPython's
    own REPL (_pyrepl), which falls back to the basic one if it can't use
    the terminal, or the basic one with PYTHON_BASIC_REPL set."""
    print(banner(), file=sys.stderr)
    print('Type "help", "copyright", "credits" or "license" for more information.', file=sys.stderr)
    if os.environ.get("PYTHON_BASIC_REPL"):
        import code

        code.interact(banner="", local=main.__dict__, exitmsg="")
        return
    from _pyrepl.main import interactive_console

    interactive_console(main)
