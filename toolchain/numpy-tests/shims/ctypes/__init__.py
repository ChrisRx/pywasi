"""Stand-in for ctypes, which WASI can't have (no _ctypes).

Test modules use ctypes at import time (ctypes.c_long, POINTER(...),
c_int * 3), so every attribute is a placeholder that importing survives.
Tests that really use ctypes fail.
"""


class _Placeholder:
    def __init__(self, name="ctypes"):
        self._name = name

    def __getattr__(self, name):
        return _Placeholder(f"{self._name}.{name}")

    def __call__(self, *args, **kwargs):
        return _Placeholder(f"{self._name}()")

    def __mul__(self, other):
        return _Placeholder(f"{self._name}*")

    __rmul__ = __mul__

    def __repr__(self):
        return f"<ctypes placeholder {self._name}>"


def __getattr__(name):
    if name.startswith("__"):
        raise AttributeError(name)
    return _Placeholder(f"ctypes.{name}")
