"""Fixture for call_test.go: a module mounted from the host."""

import numpy as np


def summarize(values, ddof=0):
    a = np.asarray(values, dtype=float)
    return {"mean": a.mean(), "std": a.std(ddof=ddof), "n": len(a)}


class Matrix:
    @staticmethod
    def identity(n):
        return np.eye(n)


def fail():
    raise np.linalg.LinAlgError("Singular matrix")
