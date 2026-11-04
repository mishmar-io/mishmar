"""The signer as a child process, or one already running.

``Signer`` starts ``mishmar signer run --local …`` with the agent's config
and talks to it over a Unix socket (or, on Windows, localhost with a token).
The child holds the agent's key and its halves of the venue keys; this
process holds no secret at all. An order is an intent: the child turns it
into a proposal signed with the agent's key, sends it one-shot where it can
build the venue's message, through the channel otherwise, and hands back the
decision as the engine recorded it. The agent's reads pass through the child
the same way.

    with Signer() as s:                       # ~/.mishmar/agent.json, mishmar on PATH
        d = s.order("ta_…", "BTC", "buy", "0.001", price="79500")
        if d["verdict"] == "require_approval":
            d = s.wait(d["id"])

The child lives and dies with this process: it gets a pipe on stdin that is
never written to and stops when the pipe closes, so it does not outlive a
bot that crashed or was killed. A signer run on its own instead (systemd,
launchd, Docker: ``mishmar signer run --local unix:/path``) is reached with
``Signer.connect``, which never starts or stops it; that is the way for
several processes of one agent, or to keep the signer connected across bot
restarts. One signer runs per config: a second one on the same config stops
at once and says which process holds it.

The binary is found through ``MISHMAR_BIN``, the PATH, or
``~/.mishmar/bin/<version>/mishmar``; ``download_binary`` fetches a release
from GitHub and checks it against the release's checksums.
"""

from __future__ import annotations

import hashlib
import http.client
import io
import json
import logging
import os
import platform
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.parse
import urllib.request
from collections import deque
from typing import Any

from .client import EngineError, resolved

log = logging.getLogger("mishmar.signer")

RELEASES = "https://github.com/mishmar-io/mishmar/releases/download"
ENV_BINARY = "MISHMAR_BIN"
ENV_LOCAL_TOKEN = "MISHMAR_LOCAL_TOKEN"


def find_binary(explicit: str | None = None) -> str:
    """Where the ``mishmar`` binary is: the argument, ``MISHMAR_BIN``, the
    PATH, or a release ``download_binary`` fetched. Raises FileNotFoundError
    with what to do when there is none."""
    candidates = [explicit, os.environ.get(ENV_BINARY), shutil.which("mishmar")]
    home = os.path.join(os.path.expanduser("~"), ".mishmar", "bin")
    if os.path.isdir(home):
        for version in sorted(os.listdir(home), reverse=True):
            candidates.append(os.path.join(home, version, _exe("mishmar")))
    for c in candidates:
        if c and os.path.isfile(c) and os.access(c, os.X_OK):
            return c
    raise FileNotFoundError(
        "no mishmar binary: install it from https://github.com/mishmar-io/mishmar/releases "
        "(or call mishmar.download_binary(version)), put it on the PATH or in MISHMAR_BIN"
    )


def platform_name() -> str:
    """The release asset's os_arch for this machine: linux_amd64, darwin_arm64, …"""
    os_name = {"linux": "linux", "darwin": "darwin", "win32": "windows"}.get(
        sys.platform
    )
    arch = {
        "x86_64": "amd64",
        "amd64": "amd64",
        "arm64": "arm64",
        "aarch64": "arm64",
    }.get(platform.machine().lower())
    if not os_name or not arch:
        raise RuntimeError(
            f"no mishmar release for {sys.platform}/{platform.machine()}"
        )
    return f"{os_name}_{arch}"


def download_binary(
    version: str, dest_dir: str | None = None, *, opener: Any = None
) -> str:
    """Fetch the release ``version`` (a tag, v0.1.0) for this machine into
    ``dest_dir`` (default ``~/.mishmar/bin/<version>``), after checking its
    SHA-256 against the release's SHA256SUMS. Returns the binary's path."""
    opener = opener or urllib.request.urlopen
    name = f"mishmar_{version}_{platform_name()}"
    base = f"{RELEASES}/{version}"
    with opener(f"{base}/SHA256SUMS", timeout=60) as res:
        sums = res.read().decode("utf-8")
    want = None
    for line in sums.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[1].lstrip("*") == name + ".tar.gz":
            want = parts[0].lower()
    if want is None:
        raise RuntimeError(f"{name}.tar.gz is not in the release's SHA256SUMS")
    with opener(f"{base}/{name}.tar.gz", timeout=600) as res:
        archive = res.read()
    got = hashlib.sha256(archive).hexdigest()
    if got != want:
        raise RuntimeError(f"{name}.tar.gz: checksum {got} is not the release's {want}")
    dest_dir = dest_dir or os.path.join(
        os.path.expanduser("~"), ".mishmar", "bin", version
    )
    os.makedirs(dest_dir, mode=0o700, exist_ok=True)
    path = os.path.join(dest_dir, _exe("mishmar"))
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as tar:
        member = tar.getmember(_exe("mishmar"))
        extracted = tar.extractfile(member)
        if extracted is None:
            raise RuntimeError("the archive holds no mishmar binary")
        with open(path, "wb") as f:
            shutil.copyfileobj(extracted, f)
    os.chmod(
        path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH
    )
    return path


def _exe(name: str) -> str:
    return name + ".exe" if sys.platform == "win32" else name


class _UnixConnection(http.client.HTTPConnection):
    """An HTTP connection over a Unix socket."""

    def __init__(self, path: str, timeout: float):
        super().__init__("localhost", timeout=timeout)
        self._path = path

    def connect(self) -> None:
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self._path)


class Signer:
    """The agent's signer, run as a child process and spoken to over its
    local API. ``config`` is the signer's config file (default
    ``$MISHMAR_AGENT_CONFIG`` or ``~/.mishmar/agent.json``); ``binary`` the
    mishmar binary (see ``find_binary``); ``address`` where the child listens
    (default: a private Unix socket, or localhost with a token on Windows).
    Use it as a context manager, or call ``start`` and ``stop``. The child
    stops when this process exits, however it exits. ``Signer.connect``
    speaks to a signer that runs on its own instead."""

    def __init__(
        self,
        config: str | None = None,
        *,
        binary: str | None = None,
        address: str | None = None,
        start_timeout: float = 15.0,
        timeout: float = 40.0,
        log_level: str = "info",
    ):
        self.config = config
        self.binary = binary
        self.address = address
        self.start_timeout = start_timeout
        self.timeout = timeout
        self.log_level = log_level
        self.token: str | None = None
        self.process: subprocess.Popen[bytes] | None = None
        self._dir: str | None = None
        self._stderr: deque[str] = deque(maxlen=40)
        self._reader: threading.Thread | None = None
        self._attached = False
        self._ready = False

    @classmethod
    def connect(
        cls,
        address: str,
        token: str | None = None,
        *,
        start_timeout: float = 15.0,
        timeout: float = 40.0,
    ) -> Signer:
        """Speak to a signer that already runs on its own (``mishmar signer
        run --local ADDRESS``, kept up by systemd, launchd or Docker), and
        wait until it answers. ``address`` is ``unix:/path``, or a loopback
        ``host:port`` with the signer's token (``token``, default
        ``$MISHMAR_LOCAL_TOKEN``). Nothing is started, and ``stop`` leaves the
        signer running."""
        s = cls(address=address, start_timeout=start_timeout, timeout=timeout)
        s._attached = True
        if not address.startswith("unix:"):
            s.token = token if token is not None else os.environ.get(ENV_LOCAL_TOKEN)
            if not s.token:
                raise ValueError(
                    f"{address}: a signer on a TCP port needs its token: pass token= or set {ENV_LOCAL_TOKEN}"
                )
        return s.start()

    # Lifecycle

    def __enter__(self) -> Signer:
        self.start()
        return self

    def __exit__(self, *exc: object) -> None:
        self.stop()

    def start(self) -> Signer:
        """Start the child and wait until its local API answers; for a signer
        from ``connect``, only wait until it answers."""
        if self._attached:
            if not self._ready:
                self._wait_ready(time.monotonic() + self.start_timeout)
                self._ready = True
            return self
        if self.process is not None:
            return self
        binary = find_binary(self.binary)
        env = dict(os.environ)
        if self.address is None:
            if sys.platform == "win32":
                self.address = f"127.0.0.1:{_free_port()}"
            else:
                self._dir = tempfile.mkdtemp(prefix="mishmar-")
                self.address = "unix:" + os.path.join(self._dir, "signer.sock")
        if not self.address.startswith("unix:"):
            self.token = self.token or secrets.token_urlsafe(32)
            env[ENV_LOCAL_TOKEN] = self.token
        args = [
            binary,
            "signer",
            "run",
            "--local",
            self.address,
            "--log-level",
            self.log_level,
            # The child stops when this pipe closes: when this process exits,
            # however it exits. Nothing is ever written to it.
            "--exit-on-stdin-close",
        ]
        if self.config:
            args += ["--config", self.config]
        self.process = subprocess.Popen(
            args,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        self._reader = threading.Thread(target=self._forward_stderr, daemon=True)
        self._reader.start()
        self._wait_ready(time.monotonic() + self.start_timeout)
        return self

    def _wait_ready(self, deadline: float) -> None:
        while True:
            if self.process is not None and self.process.poll() is not None:
                status = self.process.returncode
                if self._reader is not None:
                    self._reader.join(timeout=1)
                tail = "\n".join(self._stderr)
                self._cleanup()
                raise RuntimeError(
                    f"the signer exited with status {status} before it was ready:\n{tail}"
                )
            try:
                self._request("GET", "/health", timeout=2.0)
                return
            except (OSError, http.client.HTTPException, EngineError) as err:
                # A running signer that answers with an error (a wrong token)
                # will not answer differently in a moment.
                if self._attached and isinstance(err, EngineError):
                    raise
                if time.monotonic() >= deadline:
                    self.stop()
                    raise TimeoutError(
                        f"the signer did not answer on {self.address} within {self.start_timeout}s"
                    )
                time.sleep(0.1)

    def stop(self) -> None:
        """Stop the child, gently then not: its stdin closes, which stops it,
        and it gets SIGTERM, then SIGKILL after 5 s. A signer from ``connect``
        keeps running; this only forgets it was ready."""
        if self._attached:
            self._ready = False
            return
        p = self.process
        if p is None:
            return
        try:
            if p.stdin is not None:
                try:
                    p.stdin.close()
                except OSError:
                    pass
            if p.poll() is None:
                p.terminate()
                try:
                    p.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait(timeout=5)
        finally:
            self._cleanup()

    def _cleanup(self) -> None:
        if self.process is not None:
            for pipe in (self.process.stdin, self.process.stderr):
                if pipe is not None:
                    try:
                        pipe.close()
                    except OSError:
                        pass
        self.process = None
        if self._dir:
            shutil.rmtree(self._dir, ignore_errors=True)
            self._dir = None
            self.address = None

    def _forward_stderr(self) -> None:
        p = self.process
        if p is None or p.stderr is None:
            return
        for raw in p.stderr:
            line = raw.decode("utf-8", "replace").rstrip()
            self._stderr.append(line)
            log.info("%s", line)

    # Proposals

    def propose(self, intent: dict[str, Any]) -> dict[str, Any]:
        """Send an intent (the local API's ProposeRequest: tradingAccountId,
        symbol, side, size, orderType, price, reduceOnly, leverage,
        validForSeconds; or kind cancel with targetClientOrderId) and get
        the decision back."""
        return self._request("POST", "/propose", body=intent)

    def order(
        self,
        trading_account_id: str,
        symbol: str,
        side: str,
        size: str,
        *,
        order_type: str = "limit",
        price: str | None = None,
        reduce_only: bool = False,
        leverage: str | None = None,
        valid_for: int = 60,
        channel: bool = False,
    ) -> dict[str, Any]:
        """Propose an order. A limit order needs ``price``; a market order is
        priced at the engine's mark. Sizes and prices are decimal strings."""
        intent: dict[str, Any] = {
            "tradingAccountId": trading_account_id,
            "symbol": symbol,
            "side": side,
            "size": size,
            "orderType": order_type,
            "reduceOnly": reduce_only,
            "validForSeconds": valid_for,
            "channel": channel,
        }
        if price is not None:
            intent["price"] = price
        if leverage is not None:
            intent["leverage"] = leverage
        return self.propose(intent)

    def cancel(
        self,
        trading_account_id: str,
        symbol: str,
        client_order_id: str,
        *,
        valid_for: int = 60,
    ) -> dict[str, Any]:
        """Cancel a resting order this agent placed, by the clientOrderId its decision carries."""
        return self.propose(
            {
                "tradingAccountId": trading_account_id,
                "symbol": symbol,
                "kind": "cancel",
                "targetClientOrderId": client_order_id,
                "validForSeconds": valid_for,
            }
        )

    # Reads, through the child

    def health(self) -> dict[str, Any]:
        return self._request("GET", "/health")

    def decision(self, decision_id: str) -> dict[str, Any]:
        return self._request(
            "GET", f"/decisions/{urllib.parse.quote(decision_id, safe='')}"
        )

    def decisions(
        self,
        *,
        trading_account_id: str | None = None,
        verdict: str | None = None,
        start: str | None = None,
        end: str | None = None,
        cursor: str | None = None,
        limit: int | None = None,
    ) -> dict[str, Any]:
        return self._request(
            "GET",
            "/decisions",
            query={
                "tradingAccountId": trading_account_id,
                "verdict": verdict,
                "from": start,
                "to": end,
                "cursor": cursor,
                "limit": limit,
            },
        )

    def approvals(self, status: str | None = None) -> list[dict[str, Any]]:
        return self._request("GET", "/approvals", query={"status": status})

    def positions(self) -> list[dict[str, Any]]:
        return self._request("GET", "/positions")

    def pnl(self, start: str | None = None, end: str | None = None) -> dict[str, Any]:
        return self._request("GET", "/pnl", query={"from": start, "to": end})

    def trading_account(self, trading_account_id: str) -> dict[str, Any]:
        return self._request(
            "GET",
            f"/trading-accounts/{urllib.parse.quote(trading_account_id, safe='')}",
        )

    def venue_rules(
        self, trading_account_id: str, symbol: str | None = None
    ) -> dict[str, Any]:
        return self._request(
            "GET",
            f"/trading-accounts/{urllib.parse.quote(trading_account_id, safe='')}/venue-rules",
            query={"symbol": symbol},
        )

    def wait(
        self, decision_id: str, timeout: float = 60.0, interval: float = 1.0
    ) -> dict[str, Any]:
        """Poll a decision until it no longer waits for a person, or until timeout."""
        deadline = time.monotonic() + timeout
        while True:
            d = self.decision(decision_id)
            if resolved(d) or time.monotonic() >= deadline:
                return d
            time.sleep(min(interval, max(0.0, deadline - time.monotonic())))

    # Plumbing

    def _connection(self, timeout: float) -> http.client.HTTPConnection:
        if self.address is None:
            raise RuntimeError("the signer is not started")
        if self.address.startswith("unix:"):
            return _UnixConnection(self.address[len("unix:") :], timeout)
        host, _, port = self.address.rpartition(":")
        return http.client.HTTPConnection(host, int(port), timeout=timeout)

    def _request(
        self,
        method: str,
        path: str,
        *,
        body: Any = None,
        query: dict[str, Any] | None = None,
        timeout: float | None = None,
    ) -> Any:
        params = {k: v for k, v in (query or {}).items() if v is not None}
        if params:
            path += "?" + urllib.parse.urlencode(params)
        data = None if body is None else json.dumps(body).encode("utf-8")
        headers = {"Accept": "application/json"}
        if data is not None:
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        conn = self._connection(timeout or self.timeout)
        try:
            conn.request(method, path, body=data, headers=headers)
            res = conn.getresponse()
            raw = res.read()
        finally:
            conn.close()
        if res.status >= 400:
            code, message = "http_error", res.reason or ""
            try:
                parsed = json.loads(raw)
                code = parsed.get("code", code)
                message = parsed.get("message", message)
            except (ValueError, AttributeError):
                if raw:
                    message = raw.decode("utf-8", "replace")[:200]
            raise EngineError(res.status, str(code), str(message))
        return json.loads(raw) if raw else None


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])
