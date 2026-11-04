"""The Signer against a fake mishmar binary: a script that serves the local API the way the real signer does."""

import hashlib
import io
import os
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import time

import pytest

from mishmar import EngineError, Signer, download_binary, find_binary

FAKE = textwrap.dedent(
    """\
    #!/usr/bin/env python3
    # A stand-in for `mishmar signer run --local ADDR`: serves the local API.
    import json, os, socketserver, sys, time
    from http.server import BaseHTTPRequestHandler, HTTPServer

    args = sys.argv[1:]
    addr = args[args.index("--local") + 1]
    config = args[args.index("--config") + 1] if "--config" in args else ""
    token = os.environ.get("MISHMAR_LOCAL_TOKEN", "")
    if os.environ.get("FAKE_EXIT"):
        sys.stderr.write("fake signer: refusing to start\\n")
        sys.exit(3)
    if "--exit-on-stdin-close" in args:
        import threading
        def watch():
            try:
                while sys.stdin.buffer.read(65536):
                    pass
            except Exception:
                pass
            os._exit(0)
        threading.Thread(target=watch, daemon=True).start()

    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass
        def _send(self, status, body):
            data = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(data)
        def _ok_auth(self):
            if not addr.startswith("unix:") and self.headers.get("Authorization") != "Bearer " + token:
                self._send(401, {"code": "unauthorized", "message": "token"})
                return False
            return True
        def do_GET(self):
            if not self._ok_auth():
                return
            path = self.path.split("?")[0]
            if path == "/health":
                self._send(200, {"agentId": "agt_1", "keyId": "key_1", "engine": "http://e/v1", "venueKeys": ["vk_1"], "config": config, "args": args})
            elif path == "/positions":
                self._send(200, [{"symbol": "BTC", "size": "0.001"}])
            elif path == "/decisions/dec_bad":
                self._send(422, {"code": "validation", "message": "no such decision"})
            elif path.startswith("/decisions/"):
                self._send(200, {"id": path.rsplit("/", 1)[1], "verdict": "allow", "query": self.path})
            elif path == "/decisions":
                self._send(200, {"items": [], "query": self.path})
            else:
                self._send(404, {"code": "not_found", "message": path})
        def do_POST(self):
            if not self._ok_auth():
                return
            n = int(self.headers.get("Content-Length", "0"))
            body = json.loads(self.rfile.read(n) or b"{}")
            if self.path == "/propose":
                self._send(200, {"id": "dec_1", "verdict": "allow", "intent": body})
            else:
                self._send(404, {"code": "not_found", "message": self.path})

    sys.stderr.write("fake signer: local API listening on " + addr + "\\n")
    sys.stderr.flush()
    if addr.startswith("unix:"):
        class Srv(socketserver.UnixStreamServer):
            def server_bind(self):
                socketserver.UnixStreamServer.server_bind(self)
        srv = Srv(addr[len("unix:"):], H)
    else:
        host, _, port = addr.rpartition(":")
        srv = HTTPServer((host, int(port)), H)
    srv.serve_forever()
    """
)


@pytest.fixture
def fake_binary(tmp_path):
    path = tmp_path / "mishmar"
    path.write_text(FAKE)
    path.chmod(path.stat().st_mode | stat.S_IXUSR)
    return str(path)


def test_signer_runs_the_binary_and_proposes_through_it(fake_binary, tmp_path):
    config = tmp_path / "agent.json"
    config.write_text("{}")
    with Signer(str(config), binary=fake_binary, start_timeout=10) as s:
        assert s.process is not None and s.process.poll() is None
        assert s.address.startswith("unix:")
        h = s.health()
        assert h["agentId"] == "agt_1" and h["config"] == str(config)
        d = s.order("ta_1", "BTC", "buy", "0.001", price="79500", valid_for=30)
        assert d["verdict"] == "allow"
        assert d["intent"] == {
            "tradingAccountId": "ta_1",
            "symbol": "BTC",
            "side": "buy",
            "size": "0.001",
            "orderType": "limit",
            "reduceOnly": False,
            "validForSeconds": 30,
            "channel": False,
            "price": "79500",
        }
        c = s.cancel("ta_1", "BTC", "msh-1-abc")
        assert (
            c["intent"]["kind"] == "cancel"
            and c["intent"]["targetClientOrderId"] == "msh-1-abc"
        )
        assert s.positions() == [{"symbol": "BTC", "size": "0.001"}]
        assert s.decision("dec_7")["id"] == "dec_7"
        assert (
            "tradingAccountId=ta_1"
            in s.decisions(trading_account_id="ta_1", limit=5)["query"]
        )
        with pytest.raises(EngineError) as err:
            s.decision("dec_bad")
        assert err.value.status == 422 and err.value.code == "validation"
        # wait() returns at once for a decision that is not parked.
        assert s.wait("dec_7", timeout=1)["id"] == "dec_7"
        process = s.process
    assert process.poll() is not None, "the child was stopped"
    assert s.process is None and s.address is None


def test_signer_over_localhost_with_a_token(fake_binary):
    s = Signer(binary=fake_binary, address="127.0.0.1:0")
    # The fake binds the port the SDK chose.
    import socket

    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        s.address = f"127.0.0.1:{sock.getsockname()[1]}"
    with s:
        assert s.token and len(s.token) > 20
        assert s.health()["agentId"] == "agt_1"
        # Without the token the child refuses.
        s.token, saved = "wrong", s.token
        with pytest.raises(EngineError) as err:
            s.health()
        assert err.value.status == 401
        s.token = saved


def test_signer_reports_a_child_that_exits(fake_binary, monkeypatch):
    monkeypatch.setenv("FAKE_EXIT", "1")
    with pytest.raises(RuntimeError) as err:
        Signer(binary=fake_binary).start()
    assert "status 3" in str(err.value) and "refusing to start" in str(err.value)


def test_find_binary_order(fake_binary, tmp_path, monkeypatch):
    monkeypatch.delenv("MISHMAR_BIN", raising=False)
    monkeypatch.setenv("PATH", str(tmp_path / "nowhere"))
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    assert find_binary(fake_binary) == fake_binary
    with pytest.raises(FileNotFoundError):
        find_binary()
    monkeypatch.setenv("MISHMAR_BIN", fake_binary)
    assert find_binary() == fake_binary
    monkeypatch.delenv("MISHMAR_BIN")
    home_bin = tmp_path / "home" / ".mishmar" / "bin" / "v0.1.0"
    home_bin.mkdir(parents=True)
    installed = home_bin / "mishmar"
    installed.write_text(FAKE)
    installed.chmod(0o755)
    assert find_binary() == str(installed)


def test_download_binary_checks_the_release_checksum(tmp_path):
    archive = io.BytesIO()
    with tarfile.open(fileobj=archive, mode="w:gz") as tar:
        data = b"#!/bin/sh\necho mishmar\n"
        info = tarfile.TarInfo("mishmar")
        info.size = len(data)
        info.mode = 0o755
        tar.addfile(info, io.BytesIO(data))
    blob = archive.getvalue()
    from mishmar.signer import platform_name

    name = f"mishmar_v0.1.0_{platform_name()}.tar.gz"
    sums = f"{hashlib.sha256(blob).hexdigest()}  {name}\n0000  other.tar.gz\n"

    class Res(io.BytesIO):
        def __enter__(self):
            return self

        def __exit__(self, *a):
            return None

    fetched = []

    def opener(url, timeout=0):
        fetched.append(url)
        if url.endswith("SHA256SUMS"):
            return Res(sums.encode())
        if url.endswith(name):
            return Res(blob)
        raise AssertionError(url)

    path = download_binary("v0.1.0", str(tmp_path / "bin"), opener=opener)
    assert os.path.isfile(path) and os.access(path, os.X_OK)
    assert fetched == [
        "https://github.com/mishmar-io/mishmar/releases/download/v0.1.0/SHA256SUMS",
        f"https://github.com/mishmar-io/mishmar/releases/download/v0.1.0/{name}",
    ]

    # A tampered archive is refused.
    def bad_opener(url, timeout=0):
        return Res(sums.encode()) if url.endswith("SHA256SUMS") else Res(blob + b"x")

    with pytest.raises(RuntimeError, match="checksum"):
        download_binary("v0.1.0", str(tmp_path / "bin2"), opener=bad_opener)


SRC = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src")


def _gone(pid: int, within: float) -> bool:
    deadline = time.monotonic() + within
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        time.sleep(0.05)
    return False


def test_the_child_is_told_to_stop_with_this_process(fake_binary):
    with Signer(binary=fake_binary) as s:
        assert "--exit-on-stdin-close" in s.health()["args"]
        assert s.process.stdin is not None


@pytest.mark.skipif(sys.platform == "win32", reason="SIGKILL")
def test_the_child_dies_with_a_killed_parent(fake_binary, tmp_path):
    parent = subprocess.Popen(
        [
            sys.executable,
            "-c",
            textwrap.dedent(
                f"""\
                import sys, time
                sys.path.insert(0, {SRC!r})
                from mishmar import Signer
                s = Signer(binary={fake_binary!r}).start()
                print(s.process.pid, flush=True)
                time.sleep(60)
                """
            ),
        ],
        stdout=subprocess.PIPE,
        text=True,
    )
    child = None
    try:
        child = int(parent.stdout.readline())
        os.kill(child, 0)  # running
        parent.kill()  # SIGKILL: no cleanup runs in the parent
        parent.wait(timeout=5)
        assert _gone(child, 10), "the signer outlived the process that started it"
    finally:
        parent.kill()
        parent.stdout.close()
        if child is not None:
            try:
                os.kill(child, signal.SIGKILL)
            except ProcessLookupError:
                pass


@pytest.mark.skipif(sys.platform == "win32", reason="Unix socket")
def test_connect_speaks_to_a_running_signer_and_leaves_it_running(fake_binary):
    # A short path: a Unix socket's path is limited to about a hundred bytes.
    short = tempfile.mkdtemp(prefix="mishmar-")
    sock = os.path.join(short, "signer.sock")
    running = subprocess.Popen(
        [fake_binary, "signer", "run", "--local", f"unix:{sock}"],
        stdin=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        with Signer.connect(f"unix:{sock}", start_timeout=10) as s:
            assert s.process is None, "connect starts nothing"
            h = s.health()
            assert h["agentId"] == "agt_1" and "--exit-on-stdin-close" not in h["args"]
            assert (
                s.order("ta_1", "BTC", "buy", "0.001", price="1")["verdict"] == "allow"
            )
        assert running.poll() is None, "stop() left the signer running"
        # It can be spoken to again.
        assert Signer.connect(f"unix:{sock}").positions() == [
            {"symbol": "BTC", "size": "0.001"}
        ]
    finally:
        running.kill()
        running.wait(timeout=5)
        shutil.rmtree(short, ignore_errors=True)


def test_connect_over_tcp_needs_the_token(fake_binary, monkeypatch):
    monkeypatch.delenv("MISHMAR_LOCAL_TOKEN", raising=False)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    with pytest.raises(ValueError, match="token"):
        Signer.connect(f"127.0.0.1:{port}")
    env = dict(os.environ, MISHMAR_LOCAL_TOKEN="t0ken")
    running = subprocess.Popen(
        [fake_binary, "signer", "run", "--local", f"127.0.0.1:{port}"],
        env=env,
        stdin=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        s = Signer.connect(f"127.0.0.1:{port}", token="t0ken", start_timeout=10)
        assert s.health()["agentId"] == "agt_1"
        # A wrong token is refused at once, not retried until the timeout.
        start = time.monotonic()
        with pytest.raises(EngineError) as err:
            Signer.connect(f"127.0.0.1:{port}", token="wrong", start_timeout=10)
        assert err.value.status == 401 and time.monotonic() - start < 5
    finally:
        running.kill()
        running.wait(timeout=5)
