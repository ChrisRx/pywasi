"""Long-lived request loop for pywasi's Go Session, run inside python.wasm.

    python -m _pywasi_session

Reads one JSON request per line from stdin:

    {"mode": "eval", "code": ...}
    {"mode": "call", "module": ..., "name": ..., "args": [...], "kwargs": {...}}

and answers each on stdout with a record separator (0x1e), the same JSON
as _pywasi's response.json, and a newline. Everything else on stdout is the
code's own output, which the host passes through. State (imports, globals
in __main__) carries over between requests. EOF on stdin ends the loop.

Value encoding and errors come from the interpreter's _pywasi module, so
this works with any pywasi interpreter.
"""

import json
import os
import sys

import _pywasi

RS = b"\x1e"


def write_all(data: bytes):
    while data:
        n = os.write(1, data)
        data = data[n:]


def handle(request):
    match request["mode"]:
        case "eval":
            return _pywasi._evaluate(request["code"], "<eval>")
        case "call":
            return _pywasi._call(
                request["module"], request["name"], request.get("args") or [], request.get("kwargs") or {}
            )
        case mode:
            raise ValueError(f"unknown pywasi request mode {mode!r}")


def serve():
    while line := sys.stdin.buffer.readline():
        try:
            value = handle(json.loads(line))
            body = '{"ok": true, "value": ' + _pywasi._encode(value) + "}"
        except BaseException as exc:  # SystemExit too: the session keeps going.
            tb = exc.__traceback__
            while tb is not None and tb.tb_frame.f_code.co_filename == __file__:
                tb = tb.tb_next
            exc.__traceback__ = tb
            body = json.dumps({"ok": False, "error": _pywasi._error(exc)})
        # The code's output first, so it arrives before its result.
        sys.stdout.flush()
        sys.stderr.flush()
        write_all(RS + body.encode() + b"\n")


if __name__ == "__main__":
    serve()
