"""pywasi: fixes for pure-Python packages that assume threads, shared
libraries or file descriptors WASI doesn't have.

WASI preview 1 has no threads, so threading.Thread.start raises. CPython's
own executors run work inline here (toolchain/patches/cpython-*). This does
the same for packages installed as-is from wheels, by patching them when
they're first imported:

- anyio runs blocking calls in worker threads (to_thread.run_sync, which
  Starlette and FastAPI use for sync endpoints and dependencies). They run
  in the event loop's thread instead, blocking it while they do, in a copy
  of the context as a worker would.
- threadpoolctl looks for the BLAS and OpenMP shared libraries loaded in
  the process (through ctypes and dlopen) to limit their threads. There
  are none (extensions are linked into python.wasm, single-threaded), so
  it finds nothing, as on a platform it doesn't know.
- tokenizers (Hugging Face) parallelizes with a thread pool unless
  TOKENIZERS_PARALLELISM says not to, so that defaults to false.
- pytest captures output by duplicating file descriptors, which WASI can't
  (os.dup fails), so it captures sys.stdout and sys.stderr instead (as
  --capture=sys), and its faulthandler plugin, which duplicates stderr
  for a handler WASI has no signals to run, does nothing.

os.getuid and friends, which WASI leaves out, return 0, the owner WASI
reports for every file (pytest's tmp_path checks that its directory is the
user's). os.umask, also missing, keeps the mask it's given, which nothing
applies: WASI has no permission bits (pytest's cache sets one).

Shipped in the standard library directory, so a sitecustomize earlier on
sys.path takes its place.
"""

import importlib.abc
import importlib.machinery
import sys


def _patch_anyio_asyncio(module):
    import contextvars

    backend = module.AsyncIOBackend

    async def run_sync_in_worker_thread(cls, func, args, abandon_on_cancel=False, limiter=None):
        await cls.checkpoint()
        return contextvars.copy_context().run(func, *args)

    backend.run_sync_in_worker_thread = classmethod(run_sync_in_worker_thread)


def _patch_threadpoolctl(module):
    module.ThreadpoolController._load_libraries = lambda self: None


def _patch_pytest_capture(module):
    get = module._get_multicapture
    module._get_multicapture = lambda method: get("sys" if method == "fd" else method)


def _patch_pytest_faulthandler(module):
    module.pytest_configure = lambda config: None
    module.pytest_unconfigure = lambda config: None


_PATCHES = {
    "anyio._backends._asyncio": _patch_anyio_asyncio,
    "threadpoolctl": _patch_threadpoolctl,
    "_pytest.capture": _patch_pytest_capture,
    "_pytest.faulthandler": _patch_pytest_faulthandler,
}


class _PatchingLoader(importlib.abc.Loader):
    def __init__(self, loader, patch):
        self._loader = loader
        self._patch = patch

    def create_module(self, spec):
        return self._loader.create_module(spec)

    def exec_module(self, module):
        self._loader.exec_module(module)
        self._patch(module)


class _PatchingFinder(importlib.abc.MetaPathFinder):
    """Finds the modules in _PATCHES as usual, and patches them once loaded."""

    def find_spec(self, name, path, target=None):
        patch = _PATCHES.get(name)
        if patch is None:
            return None
        spec = importlib.machinery.PathFinder.find_spec(name, path, target)
        if spec is not None and spec.loader is not None:
            spec.loader = _PatchingLoader(spec.loader, patch)
        return spec


if sys.platform == "wasi":
    import os

    sys.meta_path.insert(0, _PatchingFinder())
    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    for _name in ("getuid", "geteuid", "getgid", "getegid"):
        if not hasattr(os, _name):
            setattr(os, _name, lambda: 0)

    if not hasattr(os, "umask"):
        _umask = [0o022]

        def umask(mask, /):
            old, _umask[0] = _umask[0], mask
            return old

        os.umask = umask
