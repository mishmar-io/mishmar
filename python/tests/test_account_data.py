"""An agent's reads of connected-account data, and the API key they start from, against a stub engine that checks signatures the way the engine does."""

import json
import threading
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlsplit

import pytest

from mishmar import (
    AgentKey,
    Client,
    EngineError,
    engine_url_for,
    parse_secret,
    verify_request,
)
from mishmar.agentkey import new_secret, secret

SEED = bytes(range(32))
KEY = AgentKey.from_seed(SEED)
KEY_ID = "key_k7f2q9x1m3n4_abcdefgh2345"
API_KEY = (
    "msk_testnet_k7f2q9x1m3n4_abcdefgh2345_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
)
PREFIX = "/v1"


def test_a_key_of_0_19_names_its_engine_and_key():
    assert new_secret("testnet", KEY_ID, SEED) == API_KEY
    p = parse_secret(API_KEY + "\n")
    assert (p.environment, p.account_id, p.key_id) == (
        "testnet",
        "acc_k7f2q9x1m3n4",
        KEY_ID,
    )
    assert p.key.public_key == KEY.public_key
    assert AgentKey.parse(API_KEY).public_key == KEY.public_key
    assert engine_url_for(p) == "https://api.testnet.mishmar.io/a/acc_k7f2q9x1m3n4/v1"
    assert engine_url_for(p, "http://localhost:8080/v1/") == "http://localhost:8080/v1"
    with pytest.raises(ValueError, match="'self'"):
        engine_url_for(parse_secret(new_secret("self", "key_main_abcdefgh2345", SEED)))


def test_older_keys_still_parse_and_name_nothing():
    # A seed whose base64url is all underscores: the worst case for telling the formats apart.
    seed = bytes([0xFF]) * 32
    old = secret("msk_ab12cd", seed)
    assert "___" in old
    p = parse_secret(old)
    assert (p.environment, p.account_id, p.key_id) == (None, None, None)
    assert p.key.public_key == AgentKey.from_seed(seed).public_key
    assert (
        parse_secret(new_secret("prod", "key_main_abcdefgh2345", seed)).key_id
        == "key_main_abcdefgh2345"
    )
    with pytest.raises(ValueError, match="before 0.19.0"):
        engine_url_for(p)
    with pytest.raises(ValueError, match="before 0.19.0"):
        Client.from_api_key(old)


@pytest.mark.parametrize(
    "bad",
    [
        "",
        "msk_",
        "msk_a",
        "msk_a_b",
        "key_a_" + secret("", SEED)[1:],
        "msk_a_AAAA",
        "msk_a_" + secret("", SEED)[1:] + "=",
    ],
)
def test_malformed_secrets_are_refused(bad):
    with pytest.raises(ValueError, match="malformed agent secret"):
        parse_secret(bad)


ACCOUNT = {
    "id": "ta_1",
    "name": "Main",
    "venue": "hyperliquid",
    "kind": "hyperliquid",
    "status": "active",
    "mode": "agents_only",
    "traded": False,
    "setup": {"kind": "hyperliquid", "network": "testnet"},
    "createdAt": "2026-11-01T00:00:00Z",
    "updatedAt": "2026-11-01T00:00:00Z",
}


class Collector(BaseHTTPRequestHandler):
    """The seven reads a hosted account's collector answers while its engine
    sleeps; any other path is 404 and recorded in ``others``."""

    seen: list = []
    others: list = []

    def log_message(self, *args):
        pass

    def _send(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        parts = urlsplit(self.path)
        path = parts.path.removeprefix(PREFIX)
        Collector.seen.append(path + ("?" + parts.query if parts.query else ""))
        h = self.headers
        if (
            not verify_request(
                KEY.public_key_b64,
                key_id=h.get("X-Mishmar-Key-Id", ""),
                method="GET",
                path=path,
                query=parts.query,
                body=b"",
                nonce=h.get("X-Mishmar-Nonce", ""),
                signed_at=h.get("X-Mishmar-Signed-At", ""),
                signature_b64=h.get("X-Mishmar-Signature", ""),
            )
            or h.get("X-Mishmar-Key-Id") != KEY_ID
        ):
            return self._send(
                401, {"code": "unauthorized", "message": "Invalid signature"}
            )
        at = "2026-11-12T10:00:00Z"
        answers = {
            "/agent/me": {
                "agentId": "agt_1",
                "agentName": "Reader",
                "accountId": "acc_k7f2q9x1m3n4",
                "keyId": KEY_ID,
                "portfolioId": "pf_1",
            },
            "/agent/connected-accounts": [ACCOUNT],
            "/agent/connected-accounts/ta_1": ACCOUNT,
            "/agent/connected-accounts/ta_1/balances": {
                "connectedAccountId": "ta_1",
                "asOf": at,
                "source": "observed",
                "balances": [
                    {
                        "asset": "USDC",
                        "wallet": "margin",
                        "amount": "1500.5",
                        "usdValue": "1500.5",
                    }
                ],
                "equityUsd": "1500.5",
            },
            "/agent/connected-accounts/ta_1/positions": {
                "connectedAccountId": "ta_1",
                "asOf": at,
                "source": "observed",
                "positions": [
                    {
                        "symbol": "BTC",
                        "side": "long",
                        "size": "0.01",
                        "entryPrice": "79500",
                    }
                ],
            },
            "/agent/connected-accounts/ta_1/transactions": {
                "items": [
                    {
                        "id": "tx_2",
                        "connectedAccountId": "ta_1",
                        "kind": "trade",
                        "at": at,
                        "origin": {"kind": "agent", "agentName": "Reader", "own": True},
                    },
                    {
                        "id": "tx_1",
                        "connectedAccountId": "ta_1",
                        "kind": "trade",
                        "at": at,
                        "origin": {"kind": "outside", "own": False},
                    },
                ],
                "nextCursor": "c2",
            },
            "/agent/connected-accounts/ta_1/equity": {
                "connectedAccountId": "ta_1",
                "resolution": "hour",
                "points": [{"at": at, "equityUsd": "1500.5", "source": "observed"}],
            },
        }
        if path not in answers:
            Collector.others.append(path)
            return self._send(
                404,
                {"code": "not_found", "message": "not a read the collector answers"},
            )
        return self._send(200, answers[path])


@pytest.fixture
def collector(monkeypatch):
    Collector.seen, Collector.others = [], []
    server = HTTPServer(("127.0.0.1", 0), Collector)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    monkeypatch.setenv("MISHMAR_API_KEY", API_KEY)
    monkeypatch.setenv(
        "MISHMAR_ENGINE", f"http://127.0.0.1:{server.server_port}{PREFIX}/"
    )
    yield
    server.shutdown()


def test_a_read_agent_needs_only_its_api_key(collector):
    client = Client.from_api_key()
    assert client.key_id == KEY_ID and client.engine_url.endswith(PREFIX)
    assert client.me()["agentName"] == "Reader"
    assert [a["id"] for a in client.connected_accounts()] == ["ta_1"]
    assert client.connected_account("ta_1")["traded"] is False
    balances = client.account_balances("ta_1")
    assert (
        balances["equityUsd"] == "1500.5" and balances["balances"][0]["asset"] == "USDC"
    )
    at = datetime(2026, 11, 1, 2, 0, tzinfo=timezone(timedelta(hours=2)))
    assert client.account_balances("ta_1", at=at)["source"] == "observed"
    assert (
        client.account_positions("ta_1", at="2026-11-01T00:00:00Z")["positions"][0][
            "symbol"
        ]
        == "BTC"
    )
    page = client.account_transactions(
        "ta_1", since=at, kind="trade", limit=2, cursor="c1"
    )
    assert page["nextCursor"] == "c2"
    assert [t["origin"]["own"] for t in page["items"]] == [True, False]
    assert (
        client.account_equity("ta_1", since="2026-11-01T00:00:00Z", resolution="hour")[
            "points"
        ][0]["equityUsd"]
        == "1500.5"
    )
    assert Collector.others == []
    assert Collector.seen == [
        "/agent/me",
        "/agent/connected-accounts",
        "/agent/connected-accounts/ta_1",
        "/agent/connected-accounts/ta_1/balances",
        "/agent/connected-accounts/ta_1/balances?at=2026-11-01T00%3A00%3A00Z",
        "/agent/connected-accounts/ta_1/positions?at=2026-11-01T00%3A00%3A00Z",
        "/agent/connected-accounts/ta_1/transactions?from=2026-11-01T00%3A00%3A00Z&kind=trade&cursor=c1&limit=2",
        "/agent/connected-accounts/ta_1/equity?from=2026-11-01T00%3A00%3A00Z&resolution=hour",
    ]


def test_another_key_is_refused(collector):
    other = new_secret("testnet", KEY_ID, bytes(range(1, 33)))
    with pytest.raises(EngineError) as err:
        Client.from_api_key(other).connected_accounts()
    assert err.value.status == 401


def test_without_a_key_the_error_says_where_it_goes(monkeypatch):
    monkeypatch.delenv("MISHMAR_API_KEY", raising=False)
    with pytest.raises(ValueError, match="MISHMAR_API_KEY"):
        Client.from_api_key()
