"""Times as the contract writes them."""

from __future__ import annotations

from datetime import UTC, datetime
from typing import Any


def rfc3339(dt: datetime) -> str:
    """Format a time as the contract wants it: UTC, Z suffix, fraction trimmed."""
    if dt.tzinfo is None:
        raise ValueError("times must be timezone-aware")
    dt = dt.astimezone(UTC)
    text = dt.strftime("%Y-%m-%dT%H:%M:%S")
    if dt.microsecond:
        text += "." + f"{dt.microsecond:06d}".rstrip("0")
    return text + "Z"


def parse_time(value: Any) -> datetime:
    """A timezone-aware datetime from a datetime or an RFC 3339 string."""
    if isinstance(value, datetime):
        if value.tzinfo is None:
            raise ValueError("times must be timezone-aware")
        return value
    if isinstance(value, str):
        dt = datetime.fromisoformat(value)
        if dt.tzinfo is None:
            raise ValueError(f"time {value!r} has no offset")
        return dt
    raise TypeError(f"not a time: {value!r}")
