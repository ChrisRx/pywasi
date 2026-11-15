"""Run pytest on numpy's tests inside a pywasi interpreter.

    run.py --deps DIR [pytest args...]

Test paths relative to the numpy package (e.g. _core/tests/test_api.py)
are resolved against the installed numpy. DIR holds pytest and
hypothesis. Used by scripts/test-numpy.sh.
"""

import os
import sys

here = os.path.dirname(os.path.abspath(__file__))
args = sys.argv[1:]
if args[:1] != ["--deps"] or len(args) < 2:
    sys.exit("usage: run.py --deps DIR [pytest args...]")
deps, args = args[1], args[2:]
sys.path[:0] = [os.path.join(here, "shims"), deps]

# pytest's tmp_path checks the directory's owner. WASI has no users.
if not hasattr(os, "getuid"):
    os.getuid = lambda: 0

import numpy  # noqa: E402
import pytest  # noqa: E402

tests = os.path.dirname(numpy.__file__)
args = [os.path.join(tests, a) if a.endswith(".py") and not a.startswith("-") else a for a in args]
sys.exit(pytest.main([
    # No dup (fd capture, faulthandler) or /dev/null (logging) under WASI.
    "-s", "-p", "no:faulthandler", "-p", "no:logging",
    "-p", "no:cacheprovider", "-o", "addopts=", "-q", "-rfE",
    *args,
]))
