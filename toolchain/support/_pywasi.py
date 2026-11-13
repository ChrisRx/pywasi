"""Request/response entry point for pywasi's Go Eval and Call.

The host mounts a directory, writes request.json into it and runs
`python -m _pywasi DIR`. This module evaluates the request and writes
response.json next to it:

    {"ok": true, "value": ...}
    {"ok": false, "error": {"type": ..., "message": ..., "traceback": ...}}

Only WASI files are used, so it works the same under any runtime. Shipped in
the stdlib directory of every interpreter built by toolchain/pywasi_build.py.
"""

import ast
import base64
import datetime
import importlib
import json
import linecache
import math
import os
import sys
import traceback


def _default(obj):
    """JSON encoding for common non-JSON types (numpy, complex, bytes...)."""
    tolist = getattr(obj, "tolist", None)
    if callable(tolist) and type(obj).__module__.split(".")[0] == "numpy":
        # ndarray -> nested lists, numpy scalar -> Python scalar.
        return tolist()
    if isinstance(obj, complex):
        return {"real": obj.real, "imag": obj.imag}
    if isinstance(obj, (set, frozenset)):
        return list(obj)
    if isinstance(obj, (bytes, bytearray, memoryview)):
        # Decodes into a Go []byte.
        return base64.b64encode(bytes(obj)).decode()
    if isinstance(obj, (datetime.datetime, datetime.date, datetime.time)):
        # Decodes into a Go time.Time when it has a timezone.
        return obj.isoformat()
    raise TypeError(f"{type(obj).__module__}.{type(obj).__qualname__} is not JSON serializable")


def _check_finite(value):
    # JSON (and Go's encoding/json) has no NaN or Infinity. Report it clearly
    # instead of json's generic "Out of range float values".
    if isinstance(value, float) and not math.isfinite(value):
        raise ValueError(f"result contains {value!r}, which JSON cannot represent")


def _encode(value):
    try:
        return json.dumps(value, default=_default, allow_nan=False)
    except ValueError:
        # Find a non-finite float for a better message, else re-raise.
        _walk(json.loads(json.dumps(value, default=_default)))
        raise


def _walk(value):
    if isinstance(value, list):
        for v in value:
            _walk(v)
    elif isinstance(value, dict):
        for v in value.values():
            _walk(v)
    else:
        _check_finite(value)


def _evaluate(code, filename):
    """Run code like a script and return the value of a final expression."""
    linecache.cache[filename] = (len(code), None, code.splitlines(True), filename)
    tree = ast.parse(code, filename, "exec")
    last = None
    if tree.body and isinstance(tree.body[-1], ast.Expr):
        last = ast.Expression(tree.body.pop().value)
    namespace = sys.modules["__main__"].__dict__
    exec(compile(tree, filename, "exec"), namespace)
    if last is None:
        return None
    return eval(compile(last, filename, "eval"), namespace)


def _call(module, name, args, kwargs):
    target = importlib.import_module(module)
    for part in name.split("."):
        target = getattr(target, part)
    return target(*args, **kwargs)


def _error(exc):
    cls = type(exc)
    qualname = cls.__qualname__ if cls.__module__ == "builtins" else f"{cls.__module__}.{cls.__qualname__}"
    # Drop this module's frames from the traceback.
    tb = exc.__traceback__
    while tb is not None and tb.tb_frame.f_code.co_filename == __file__:
        tb = tb.tb_next
    return {
        "type": qualname,
        "message": str(exc),
        "traceback": "".join(traceback.format_exception(cls, exc, tb)),
    }


def main(directory):
    with open(os.path.join(directory, "request.json")) as f:
        request = json.load(f)

    try:
        match request["mode"]:
            case "eval":
                value = _evaluate(request["code"], request.get("filename") or "<eval>")
            case "call":
                value = _call(request["module"], request["name"], request.get("args") or [], request.get("kwargs") or {})
            case mode:
                raise ValueError(f"unknown pywasi request mode {mode!r}")
        response = '{"ok": true, "value": ' + _encode(value) + "}"
    except Exception as exc:
        response = json.dumps({"ok": False, "error": _error(exc)})

    sys.stdout.flush()
    path = os.path.join(directory, "response.json")
    with open(path + ".tmp", "w") as f:
        f.write(response)
    os.replace(path + ".tmp", path)


if __name__ == "__main__":
    main(sys.argv[1])
