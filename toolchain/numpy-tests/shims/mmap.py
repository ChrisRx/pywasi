"""Stand-in for mmap, which WASI doesn't have, so test modules that import
it load. Tests that use it fail."""


def __getattr__(name):
    raise ImportError(f"mmap.{name}: no mmap under WASI")
