"""Canonical JSON: the bytes an agent signs and the engine verifies.

The rules match Go's encoding/json, which the engine uses (see canon/ in the
repo root and spec/vectors): one object, keys sorted, no whitespace, keys
whose value is None left out at every depth, strings with Go's escapes.
Python's json module agrees on everything but the HTML-safe escapes, which
are added here. Numbers never appear in what Mishmar signs, so they are
refused rather than formatted differently from Go.
"""

import json
from typing import Any

# What Go escapes beyond the JSON minimum: HTML-significant characters and
# the two line separators. Python with ensure_ascii=False writes them raw.
_GO_ESCAPES = {
    "<": "\\u003c",
    ">": "\\u003e",
    "&": "\\u0026",
    " ": "\\u2028",
    " ": "\\u2029",
}


def _strip(value: Any) -> Any:
    if isinstance(value, bool) or value is None or isinstance(value, str):
        return value
    if isinstance(value, dict):
        return {k: _strip(v) for k, v in value.items() if v is not None}
    if isinstance(value, (list, tuple)):
        return [_strip(v) for v in value]
    if isinstance(value, (int, float)):
        raise TypeError(
            f"canonical_json: numbers are not signed by Mishmar; send {value!r} as a decimal string"
        )
    raise TypeError(f"canonical_json: cannot encode {type(value).__name__}")


def canonical_json(value: Any) -> bytes:
    """Return the canonical UTF-8 encoding of value."""
    text = json.dumps(
        _strip(value), sort_keys=True, separators=(",", ":"), ensure_ascii=False
    )
    for raw, escaped in _GO_ESCAPES.items():
        text = text.replace(raw, escaped)
    return text.encode("utf-8")
