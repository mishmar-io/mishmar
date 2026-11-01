"""Retries against an engine that answers what it was told to: 503 and 429 go
again, 413 does not, and the budget ends them."""

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import ClassVar

import pytest

from mishmar import DEFAULT_RETRIES, AgentKey, Client, EngineError, Proposal, Retries

KEY = AgentKey.from_seed(bytes(range(32)))
CODES = {503: "unavailable", 429: "rate_limited", 413: "too_large"}


class Flaky(BaseHTTPRequestHandler):
    """Answers the scripted status for each attempt in turn, then 200 with []."""

    script: ClassVar[list[int]] = []
    retry_after: ClassVar[dict[int, str]] = {}  # attempt index -> Retry-After header
    attempts: ClassVar[int] = 0
    nonces: ClassVar[list[str]] = []
    bodies: ClassVar[list[bytes]] = []

    def log_message(self, *args):
        pass

    def _serve(self):
        length = int(self.headers.get("Content-Length", 0))
        Flaky.bodies.append(self.rfile.read(length))
        i = Flaky.attempts
        Flaky.attempts += 1
        Flaky.nonces.append(self.headers.get("X-Mishmar-Nonce", ""))
        if i < len(Flaky.script):
            status = Flaky.script[i]
            self.send_response(status)
            if i in Flaky.retry_after:
                self.send_header("Retry-After", Flaky.retry_after[i])
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            body = {"code": CODES.get(status, "other"), "message": "scripted"}
            self.wfile.write(json.dumps(body).encode())
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b"[]")

    do_GET = do_POST = _serve


@pytest.fixture
def engine():
    Flaky.script, Flaky.retry_after, Flaky.attempts = [], {}, 0
    Flaky.nonces, Flaky.bodies = [], []
    server = HTTPServer(("127.0.0.1", 0), Flaky)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{server.server_port}/v1"
    server.shutdown()


def client(engine, retries=DEFAULT_RETRIES):
    c = Client(engine, KEY, "key_a", retries=retries)
    waits = []
    c._sleep = waits.append
    return c, waits


def test_503_twice_then_200_each_attempt_signed_anew(engine):
    Flaky.script = [503, 503]
    c, waits = client(engine)
    assert c.positions() == []
    assert Flaky.attempts == 3 and len(waits) == 2
    assert all(0.125 <= w <= 8.0 for w in waits)
    assert waits[1] >= waits[0] / 2
    assert len(set(Flaky.nonces)) == 3, "a retry reused a nonce"


def test_429_waits_the_seconds_in_retry_after(engine):
    Flaky.script, Flaky.retry_after = [429], {0: "3"}
    c, waits = client(engine)
    assert c.positions() == []
    assert waits == [3.0]


def test_a_retry_after_past_the_budget_is_not_waited_for(engine):
    Flaky.script, Flaky.retry_after = [429], {0: "120"}
    c, waits = client(engine)
    with pytest.raises(EngineError) as err:
        c.positions()
    assert (err.value.status, err.value.code) == (429, "rate_limited")
    assert err.value.retry_after == "120"
    assert Flaky.attempts == 1 and waits == []


def test_413_is_not_retried(engine):
    Flaky.script = [413]
    c, waits = client(engine)
    with pytest.raises(EngineError) as err:
        c.positions()
    assert (err.value.status, err.value.code) == (413, "too_large")
    assert Flaky.attempts == 1 and waits == []


def test_the_budget_runs_out_and_the_last_answer_is_raised(engine):
    Flaky.script = [503] * 8
    c, waits = client(engine, Retries(budget=0.001, min_wait=0.01, max_wait=0.02))
    with pytest.raises(EngineError) as err:
        c.positions()
    assert (err.value.status, err.value.code) == (503, "unavailable")
    assert Flaky.attempts == 1 and waits == []


def test_none_turns_retries_off(engine):
    Flaky.script = [503]
    c, waits = client(engine, retries=None)
    with pytest.raises(EngineError) as err:
        c.positions()
    assert err.value.status == 503
    assert Flaky.attempts == 1 and waits == []


def test_a_proposal_goes_again_with_the_same_bytes(engine):
    Flaky.script = [503]
    c, waits = client(engine)
    p = Proposal(
        trading_account_id="rt_1", symbol="BTC", side="buy", size="0.5", price="1"
    )
    c.propose(
        p
    )  # the flaky engine answers [], which is not a decision; the retry is the point
    assert Flaky.attempts == 2 and len(waits) == 1
    assert Flaky.bodies[0] == Flaky.bodies[1] and Flaky.bodies[0]


def test_wait_policy():
    r = Retries()
    assert r.wait(0, 500, None, 0.0) is None
    assert r.wait(0, 503, None, 45.0) is None
    first = r.wait(0, 503, None, 0.0)
    assert first is not None and 0.125 <= first <= 0.25
    late = r.wait(30, 503, None, 0.0)
    assert late is not None and 4.0 <= late <= 8.0
    assert r.wait(0, 429, "garbage", 0.0) is not None
    assert r.wait(0, 429, "Thu, 01 Jan 1970 00:00:00 GMT", 0.0) == 0.0
