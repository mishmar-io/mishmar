"""The client against a stub engine on localhost, which checks signatures the way the engine does."""

import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlsplit

import pytest

from mishmar import AgentKey, Client, EngineError, Proposal, verify, verify_request

KEY = AgentKey.from_seed(bytes(range(32)))
PREFIX = "/v1"


class Stub(BaseHTTPRequestHandler):
    requests: list = []
    decisions: list = []  # what GET /agent/decisions/{id} answers, in order

    def log_message(self, *args):  # quiet
        pass

    def _send(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(data)

    def _agent_authenticated(self, body: bytes) -> bool:
        h = self.headers
        parts = urlsplit(self.path)
        return verify_request(
            KEY.public_key_b64,
            key_id=h.get("X-Mishmar-Key-Id", ""),
            method=self.command,
            path=parts.path.removeprefix(PREFIX),
            query=parts.query,
            body=body,
            nonce=h.get("X-Mishmar-Nonce", ""),
            signed_at=h.get("X-Mishmar-Signed-At", ""),
            signature_b64=h.get("X-Mishmar-Signature", ""),
        )

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length)
        body = json.loads(raw) if raw else None
        Stub.requests.append(("POST", self.path, dict(self.headers), body))
        if self.path == PREFIX + "/proposals":
            # The engine's check: the signature over the canonical body without `signature`.
            unsigned = dict(body)
            sig = unsigned.pop("signature")
            if not verify(
                KEY.public_key_b64, Proposal.from_wire(unsigned).signing_bytes(), sig
            ):
                return self._send(
                    401, {"code": "unauthorized", "message": "Invalid signature"}
                )
            return self._send(
                200, {"id": "dec_1", "verdict": "require_approval", "proposal": body}
            )
        if not self._agent_authenticated(raw):
            return self._send(
                401, {"code": "unauthorized", "message": "Invalid signature"}
            )
        if self.path == PREFIX + "/agent/signer/poll":
            return self._send(200, [{"id": "sr_1", "scheme": "frost-ed25519"}])
        if self.path == PREFIX + "/agent/signer/commitments":
            return self._send(
                200,
                {
                    "venueKeyId": body["venueKeyId"],
                    "scheme": "frost-ed25519",
                    "batchSize": 8,
                    "outstanding": body.get("count", 8),
                    "items": [],
                },
            )
        if self.path.endswith("/answer"):
            self.send_response(204)
            self.end_headers()
            return None
        return self._send(404, {"code": "not_found", "message": "no"})

    def do_GET(self):
        Stub.requests.append(("GET", self.path, dict(self.headers), None))
        if not self._agent_authenticated(b""):
            return self._send(
                401, {"code": "unauthorized", "message": "Invalid signature"}
            )
        if self.path.startswith(PREFIX + "/agent/decisions/"):
            return self._send(
                200,
                Stub.decisions.pop(0) if len(Stub.decisions) > 1 else Stub.decisions[0],
            )
        if self.path.startswith(PREFIX + "/agent/decisions?"):
            return self._send(200, {"items": Stub.decisions, "nextCursor": None})
        if self.path == PREFIX + "/agent/positions":
            return self._send(200, [])
        if self.path.startswith(PREFIX + "/agent/connected-accounts/ta_1/venue-rules"):
            return self._send(
                200, {"connectedAccountId": "ta_1", "venue": "binance", "symbols": []}
            )
        return self._send(404, {"code": "not_found", "message": "no"})


@pytest.fixture
def engine():
    Stub.requests, Stub.decisions = [], [{"id": "dec_1", "verdict": "require_approval"}]
    server = HTTPServer(("127.0.0.1", 0), Stub)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{server.server_port}{PREFIX}"
    server.shutdown()


def test_propose_signs_and_sends(engine):
    client = Client(engine, KEY, "key_a")
    d = client.propose(
        Proposal(
            connected_account_id="rt_1",
            symbol="BTC",
            side="buy",
            size="0.5",
            order_type="market",
        )
    )
    assert d["verdict"] == "require_approval"
    method, path, _, body = Stub.requests[0]
    assert (method, path) == ("POST", PREFIX + "/proposals")
    assert body["keyId"] == "key_a" and "signature" in body


def test_a_tampered_proposal_is_refused(engine):
    client = Client(engine, KEY, "key_a")
    p = Proposal(
        connected_account_id="rt_1",
        symbol="BTC",
        side="buy",
        size="0.5",
        price="1",
        key_id="key_a",
    ).sign(KEY)
    p.size = "0.50"  # changed after signing
    with pytest.raises(EngineError) as err:
        client.propose(p)
    assert err.value.status == 401 and err.value.code == "unauthorized"


def test_reads_are_signed_with_the_agent_key(engine):
    client = Client(engine, KEY, "key_a")
    assert client.positions() == []
    page = client.decisions(verdict="require_approval", limit=20)
    assert page["items"][0]["id"] == "dec_1"
    _, path, headers, _ = Stub.requests[-1]
    assert path == PREFIX + "/agent/decisions?verdict=require_approval&limit=20"
    assert headers["X-Mishmar-Key-Id"] == "key_a" and "X-Mishmar-Signature" in headers


def test_wait_polls_until_resolved(engine):
    Stub.decisions = [
        {"id": "dec_1", "verdict": "require_approval"},
        {
            "id": "dec_1",
            "verdict": "require_approval",
            "approval": {"status": "approved"},
        },
    ]
    client = Client(engine, KEY, "key_a")
    d = client.wait("dec_1", timeout=5, interval=0.01)
    assert d["approval"]["status"] == "approved"
    assert sum(1 for r in Stub.requests if r[0] == "GET") == 2


def test_another_key_is_refused(engine):
    other = AgentKey.from_seed(bytes(range(1, 33)))
    with pytest.raises(EngineError) as err:
        Client(engine, other, "key_a").decision("dec_1")
    assert err.value.status == 401


def test_signer_channel(engine):
    client = Client(engine, KEY, "key_a")
    assert client.signer_poll(wait_seconds=1)[0]["id"] == "sr_1"
    client.signer_answer(
        "sr_1", {"kind": "refusal", "refusal": {"reason": "not my order"}}
    )
    method, path, _, body = Stub.requests[-1]
    assert (method, path) == ("POST", PREFIX + "/agent/signer/requests/sr_1/answer")
    assert body["kind"] == "refusal"


def test_one_shot_reads(engine):
    client = Client(engine, KEY, "key_a")
    assert client.venue_rules("ta_1", symbol="BTCUSDT")["venue"] == "binance"
    _, path, _, _ = Stub.requests[-1]
    assert path == PREFIX + "/agent/connected-accounts/ta_1/venue-rules?symbol=BTCUSDT"
    batch = client.signer_commitments("vk_1", count=2)
    assert batch["outstanding"] == 2
    method, path, _, body = Stub.requests[-1]
    assert (method, path, body) == (
        "POST",
        PREFIX + "/agent/signer/commitments",
        {"venueKeyId": "vk_1", "count": 2},
    )
