"""``mishmar`` as a console script: ``uvx mishmar mcp``, or any other mishmar
command, without installing the CLI first.

It finds the real binary (``MISHMAR_BIN``, the PATH less any wrapper such as
this one, then ``~/.mishmar/bin``), fetches the pinned release when there is
none, and replaces itself with it (``os.execv``; a subprocess on Windows), so
the binary gets the same arguments, stdin, stdout and stderr and stdout stays
the MCP channel: this script writes nothing there itself.
"""

from __future__ import annotations

import os
import subprocess
import sys
from collections.abc import Callable

from .signer import RELEASE, download_binary, find_binary, platform_name

# Set on the binary the wrapper starts; a wrapper that finds it set was started
# by another wrapper, which means that one resolved to itself.
ENV_WRAPPED = "MISHMAR_WRAPPED"


def resolve_binary(
    self_path: str, log: Callable[[str], None] = lambda line: None
) -> str:
    """The binary for the wrapper at ``self_path`` to run: found, or fetched
    into ``~/.mishmar/bin/<release>``. ``log`` gets one line when a download
    starts."""
    exclude = {os.path.realpath(self_path)}
    try:
        return find_binary(skip_scripts=True, exclude=exclude)
    except FileNotFoundError:
        log(
            f"mishmar: fetching release {RELEASE} for {platform_name()} "
            f"into ~/.mishmar/bin/{RELEASE}"
        )
        return download_binary(RELEASE)


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
