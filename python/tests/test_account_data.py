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


CONNECTION = {
    "id": "hl_main",
    "name": "Main",
    "accountCount": 2,
    "kind": "hyperliquid",
    "venueAccount": {"id": "0x12ab", "role": "main", "label": "0x12ab…9f3c"},
    "setup": {"kind": "hyperliquid", "network": "testnet"},
    "sync": [{"kind": "balances", "status": "current", "asOf": "2026-11-12T10:00:00Z"}],
}
ACCOUNT = {
    "id": "ta_1",
    "venueConnectionId": "hl_main",
    "connection": CONNECTION,
    "name": "Perps",
    "type": "derivatives",
    "venueRef": "perps",
    "venue": "hyperliquid",
    "status": "active",
    "mode": "agents_only",
    "traded": False,
    "equityUsd": "1500.5",
    "equityAsOf": "2026-11-12T10:00:00Z",
    "createdAt": "2026-11-01T00:00:00Z",
    "updatedAt": "2026-11-01T00:00:00Z",
}
SPOT = {
    **ACCOUNT,
    "id": "ta_2",
    "name": "Spot",
    "type": "spot",
    "venueRef": "spot",
    "equityUsd": "0",
    "unpricedAssets": ["HYPE"],
    "tradable": False,
    "untradableReason": "Mishmar does not trade spot on Hyperliquid yet",
}


class Collector(BaseHTTPRequestHandler):
    """The seven reads a hosted account's collector answers while its engine
    sleeps; any other path is 404 and recorded in ``others``."""

    seen: list = []
    others: list = []
    #: Merged into the /agent/me answer: another agent than the reader.
    identity: dict = {}

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
                "permission": "read",
                **Collector.identity,
            },
            "/agent/connected-accounts": [ACCOUNT, SPOT],
            "/agent/connected-accounts/ta_1": ACCOUNT,
            "/agent/connected-accounts/ta_1/balances": {
                "connectedAccountId": "ta_1",
                "asOf": at,
                "source": "observed",
                "balances": [
                    {
                        "asset": "USDC",
                        "amount": "1500.5",
                        "priceUsd": "1",
                        "usdValue": "1500.5",
                    },
                    {"asset": "PURR", "amount": "20"},
                ],
                "equityUsd": "1500.5",
                "unpricedAssets": ["PURR"],
            },
            "/agent/connected-accounts/ta_1/positions": {
                "connectedAccountId": "ta_1",
                "asOf": at,
                "source": "observed",
                "positions": [
                    {
                        "market": "perp",
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
                    {
                        "id": "tx_0",
                        "connectedAccountId": "ta_1",
                        "kind": "transfer_in",
                        "at": at,
                        "asset": "USDC",
                        "amount": "500",
                        "counterpartConnectedAccountId": "ta_2",
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
    Collector.seen, Collector.others, Collector.identity = [], [], {}
    server = HTTPServer(("127.0.0.1", 0), Collector)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    monkeypatch.setenv("MISHMAR_API_KEY", API_KEY)
    monkeypatch.setenv(
        "MISHMAR_ENGINE", f"http://127.0.0.1:{server.server_port}{PREFIX}/"
    )
    yield
    server.shutdown()


def test_the_identity_carries_the_agents_capital(collector):
    # Contract 0.21.0: capital is the agent's; absent when none is set.
    assert "capitalUsd" not in Client.from_api_key().me()
    Collector.identity = {
        "agentName": "Trend",
        "permission": "trade",
        "capitalUsd": "5000",
    }
    me = Client.from_api_key().me()
    assert (me["permission"], me.get("capitalUsd")) == ("trade", "5000")
    assert "portfolioId" not in me


def test_a_read_agent_needs_only_its_api_key(collector):
    client = Client.from_api_key()
    assert client.key_id == KEY_ID and client.engine_url.endswith(PREFIX)
    me = client.me()
    assert (me["agentName"], me.get("permission")) == ("Reader", "read")
    # Each account names its connection, so one list tells them apart.
    accounts = client.connected_accounts()
    assert [(a["id"], a["name"], a["connection"]["id"]) for a in accounts] == [
        ("ta_1", "Perps", "hl_main"),
        ("ta_2", "Spot", "hl_main"),
    ]
    assert accounts[1]["unpricedAssets"] == ["HYPE"]
    # Read only, with the reason for a person; the tradable one has none.
    assert (accounts[1]["tradable"], accounts[1]["untradableReason"]) == (
        False,
        "Mishmar does not trade spot on Hyperliquid yet",
    )
    assert "untradableReason" not in accounts[0]
    assert "wallets" not in accounts[0] and "kind" not in accounts[0]
    # How fresh an account's data is comes with its connection.
    assert accounts[0]["connection"]["sync"][0]["status"] == "current"
    # The login has two accounts and the agent sees both: their sum is its value.
    assert accounts[0]["connection"]["accountCount"] == len(accounts)
    assert client.connected_account("ta_1")["traded"] is False
    balances = client.account_balances("ta_1")
    assert balances["equityUsd"] == "1500.5" and balances["unpricedAssets"] == ["PURR"]
    assert [(b["asset"], b.get("priceUsd")) for b in balances["balances"]] == [
        ("USDC", "1"),
        ("PURR", None),
    ]
    at = datetime(2026, 11, 1, 2, 0, tzinfo=timezone(timedelta(hours=2)))
    assert client.account_balances("ta_1", at=at)["source"] == "observed"
    position = client.account_positions("ta_1", at="2026-11-01T00:00:00Z")["positions"][
        0
    ]
    assert (position["symbol"], position["market"]) == ("BTC", "perp")
    page = client.account_transactions(
        "ta_1", since=at, kind="trade", limit=2, cursor="c1"
    )
    assert page["nextCursor"] == "c2"
    assert [t["origin"]["own"] for t in page["items"][:2]] == [True, False]
    assert page["items"][2]["counterpartConnectedAccountId"] == "ta_2"
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


def test_the_wallet_argument_is_gone():
    """Contract 0.21.0 took the wallet level out: an account is the pool, so
    the reads take no ``wallet_id``."""
    client = Client("http://127.0.0.1:9/v1", KEY, KEY_ID)
    for read in (
        client.account_balances,
        client.account_positions,
        client.account_transactions,
        client.account_equity,
    ):
        with pytest.raises(TypeError, match="wallet_id"):
            read("ta_1", wallet_id="spot")
