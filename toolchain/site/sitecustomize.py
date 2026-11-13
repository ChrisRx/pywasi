# WASI has no inherited working directory, so toolchain/wasi-python passes
# the host one through and we apply it here, before any script runs.
import os

_cwd = os.environ.get("PYWASI_CWD")
if _cwd:
    os.chdir(_cwd)
