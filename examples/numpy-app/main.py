import sys

import numpy as np

print("python", sys.version.split()[0], "on", sys.platform)
print("numpy", np.__version__)

a = np.arange(12, dtype=np.float64).reshape(3, 4)
print("a.sum(axis=0) =", a.sum(axis=0))
print("a @ a.T =\n", a @ a.T)

m = np.array([[4.0, 1.0], [2.0, 3.0]])
print("eigvals =", np.sort(np.linalg.eigvals(m)))
print("solve =", np.linalg.solve(m, np.array([1.0, 2.0])))

rng = np.random.default_rng(42)
x = rng.standard_normal(100_000)
print(f"normal: mean={x.mean():.4f} std={x.std():.4f}")

sig = np.sin(2 * np.pi * 5 * np.linspace(0, 1, 64, endpoint=False))
print("fft peak bin =", int(np.argmax(np.abs(np.fft.rfft(sig)))))

import markupsafe
import markupsafe._speedups
from tabulate import tabulate

# markupsafe's C speedups are statically linked: a built-in module.
print("markupsafe escape:", markupsafe.escape("<b>&</b>"), "speedups:", markupsafe._speedups.__spec__.origin)
print(tabulate([["sum", a.sum()], ["max", a.max()]], headers=["stat", "value"]))
