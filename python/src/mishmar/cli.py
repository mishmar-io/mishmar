"""``mishmar`` as a console script: ``uvx mishmar mcp``, or any other mishmar
command, without installing the CLI first.

It runs ``MISHMAR_BIN``, or a ``mishmar`` on the PATH less any wrapper such
as this one, and otherwise exactly the release this package pins,
``~/.mishmar/bin/<release>``, fetched first when it is not there; other
releases under ``~/.mishmar/bin`` are never used. It replaces itself with the
binary (``os.execv``; a subprocess on Windows), so the binary gets the same
arguments, stdin, stdout and stderr and stdout stays the MCP channel: this
script writes nothing there itself.
"""

from __future__ import annotations

import os
import re
import subprocess
import sys
from collections.abc import Callable

from .signer import (
    RELEASE,
    _executable,
    _exe,
    download_binary,
    find_binary,
    platform_name,
)

# Set on the binary the wrapper starts; a wrapper that finds it set was started
# by another wrapper, which means that one resolved to itself.
ENV_WRAPPED = "MISHMAR_WRAPPED"
INSTALL_DOCS = "https://docs.mishmar.io/start/install-the-cli/"
RELEASES_PAGE = "https://github.com/mishmar-io/mishmar/releases"


def _release_numbers(version: str) -> tuple[int, int, int] | None:
    """A release's three numbers from ``v1.2.3``, or None for anything else."""
    m = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", version)
    return (int(m[1]), int(m[2]), int(m[3])) if m else None


def older_release(version: str, than: str) -> bool:
    """Whether release ``version`` comes before release ``than``, number by
    number (v0.5.9 before v0.5.10); False when either is not a release."""
    a, b = _release_numbers(version), _release_numbers(than)
    return a is not None and b is not None and a < b


def _binary_version(binary: str) -> str | None:
    """The release ``binary version`` prints (``mishmar v0.5.3 (go1.26
    linux/amd64)``), or None when it fails, takes over 2 s or prints anything
    else."""
    try:
        run = subprocess.run(
            [binary, "version"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            timeout=2,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    if run.returncode != 0:
        return None
    m = re.match(rb"mishmar (v\d+\.\d+\.\d+) \(", run.stdout)
    return m[1].decode("ascii") if m else None


def resolve_binary(
    self_path: str, log: Callable[[str], None] = lambda line: None
) -> str:
    """The binary for the wrapper at ``self_path`` to run: ``MISHMAR_BIN`` or
    one on the PATH, else the pinned release, fetched into
    ``~/.mishmar/bin/<release>`` when it is not there. ``log`` gets one line
    when a download starts, and one when the binary found is an older release
    than the pinned one. Raises RuntimeError, saying how to retry, when the
    download fails."""
    exclude = {os.path.realpath(self_path)}
    try:
        found = find_binary(skip_scripts=True, exclude=exclude, skip_downloaded=True)
    except FileNotFoundError:
        found = None  # none in MISHMAR_BIN or on the PATH
    if found is not None:
        version = _binary_version(found)
        if version and older_release(version, RELEASE):
            log(
                f"mishmar: {found} is {version}, older than this package's {RELEASE}; "
                f"update it ({INSTALL_DOCS}) or remove it to use {RELEASE}"
            )
        return found
    pinned = os.path.join(
        os.path.expanduser("~"), ".mishmar", "bin", RELEASE, _exe("mishmar")
    )
    if _executable(pinned):
        return pinned
    log(
        f"mishmar: fetching release {RELEASE} for {platform_name()} "
        f"into ~/.mishmar/bin/{RELEASE}"
    )
    try:
        return download_binary(RELEASE)
    except Exception as err:  # noqa: BLE001 - any failure stops the wrapper the same way
        raise RuntimeError(
            f"could not fetch release {RELEASE} ({err}); run the same command again "
            f"to retry, or install {RELEASE} by hand from {RELEASES_PAGE} and put it "
            "on the PATH or in MISHMAR_BIN"
        ) from err


def main(argv: list[str] | None = None) -> int:
    """Run the binary with ``argv`` (default this process's arguments); the
    exit code, when the binary could not be started or, on Windows, when it
    ends. Elsewhere this process becomes the binary and never returns."""
    argv = sys.argv[1:] if argv is None else argv
    if os.environ.get(ENV_WRAPPED):
        print(
            "mishmar: this wrapper was started by another wrapper, so the PATH "
            "holds no mishmar binary; set MISHMAR_BIN to it",
            file=sys.stderr,
        )
        return 2
    try:
        binary = resolve_binary(sys.argv[0], lambda line: print(line, file=sys.stderr))
    except Exception as err:  # noqa: BLE001 - whatever stopped us is the message
        print(f"mishmar: {err}", file=sys.stderr)
        return 1
    os.environ[ENV_WRAPPED] = "1"
    if sys.platform == "win32":
        return subprocess.call([binary, *argv])
    os.execv(binary, [binary, *argv])
    return 1  # pragma: no cover - execv does not return


if __name__ == "__main__":
    sys.exit(main())
