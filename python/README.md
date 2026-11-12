# mishmar (Python)

<!-- mcp-name: io.github.mishmar-io/mishmar -->

[Mishmar](https://mishmar.io) is a risk management layer for trading bots
and AI agents: every order is checked against your limits before it reaches
the exchange, large orders wait for your approval, and no key can trade
alone.

This is the Python client: build a proposal, sign it with the agent's key,
send it to the engine, read the decision; and read the connected accounts
the agent was given.

The package also puts `mishmar` on the path, so `uvx mishmar mcp` runs the
MCP server and `uvx mishmar signer run` the signer without the CLI
installed: the command runs the binary found through `MISHMAR_BIN`, the
PATH or `~/.mishmar/bin`, and when there is none fetches the pinned release
(v0.4.0) into `~/.mishmar/bin` first, checked against the release's
checksums.

```python
from datetime import timedelta
from mishmar import AgentKey, Client, Proposal

key = AgentKey.parse("msk_…")           # shown once when the engine issued the key
client = Client("http://localhost:8080/v1", key, key_id="key_…")

decision = client.propose(Proposal(
    connected_account_id="ta_…", symbol="BTC", side="buy", size="0.25",
    order_type="market", valid_for=timedelta(minutes=5),
))
print(decision["verdict"])              # allow | require_approval | block

# Pull a resting order this agent placed through Mishmar: a cancel names the
# order by the clientOrderId its decision carries, and has no side or size.
client.propose(Proposal(
    connected_account_id="ta_…", symbol="BTC", kind="cancel",
    target_client_order_id=decision["proposal"]["clientOrderId"],
))
```

Sizes, prices and leverage are decimal **strings** and are signed exactly as
written: `"0.5"` and `"0.50"` are the same quantity but different signatures.
Never pass a float.

A call the engine did nothing with goes again by itself: `503 unavailable`
while the engine restarts and `429 rate_limited` (after its `Retry-After`),
for up to 45 s, then the last answer is raised as `EngineError`. Nothing else
is retried. `Client(..., retries=None)` turns that off; `retries=Retries(budget=90)`
tunes it.

## Reading connected accounts

An agent reads each connected account it was given whole: balances, the
venue's positions, transactions and value over time, whoever acted on the
account. Every read is signed with the agent's key and shows in the
account's access log. The agent's API key alone is enough: it names the
engine and the key id (`MISHMAR_ENGINE`, or `engine_url=`, for a
self-hosted engine), so a read agent, which has no signer, needs nothing
else.

```python
from mishmar import Client

client = Client.from_api_key()                    # $MISHMAR_API_KEY
for account in client.connected_accounts():
    snap = client.account_balances(account["id"])  # at=… for a past moment
    print(account["name"], snap.get("equityUsd"))

page = client.account_transactions("ta_…", since="2026-11-01T00:00:00Z", kind="trade", limit=200)
for t in page["items"]:
    origin = t.get("origin", {})                   # who placed a trade
    print(t["at"], t.get("symbol"), "mine" if origin.get("own") else origin.get("agentName", "outside"))
# page.get("nextCursor") → cursor=… for the page after

positions = client.account_positions("ta_…")       # the venue's net positions, whoever placed them
series = client.account_equity("ta_…", since="2026-10-01T00:00:00Z", resolution="day")
```

`me()` says who the key belongs to and `connected_account(id)` reads one
account. Times are RFC 3339 strings or timezone-aware datetimes. The answers
are plain dicts, typed for a checker in `mishmar.data` (`BalanceSnapshot`,
`AccountTransactionPage`, …). `positions()` is something else: the agent's own
book, what its orders through Mishmar opened. `Signer` has the same reads,
through its child. Started with a read agent's key, the child polls nothing
(it learns from `GET /agent/me` that the agent reads) and refuses `order`,
`cancel` and the other calls a read agent does not make with a 403
`EngineError`, before anything is sent.

Run the tests, which include the cross-language vectors in `../spec/vectors`:

```
pip install -e ".[test]"
python -m pytest
```

## The signer as a child process

With the `mishmar` binary installed (or fetched with `download_binary`),
`Signer` runs it as a child process and the bot holds no secret: the child
has the agent's key and shares, signs each proposal, and sends it one-shot
where it can. Reads go through the child too.

```python
from mishmar import Signer

with Signer() as s:                                   # ~/.mishmar/agent.json
    d = s.order("ta_…", "BTC", "buy", "0.001", price="79500")
    if d["verdict"] == "require_approval":
        d = s.wait(d["id"])
    s.cancel("ta_…", "BTC", d["proposal"]["clientOrderId"])
```

The child stops when this process ends, however it ends: it runs with
`--exit-on-stdin-close` and a pipe on its stdin. To share one signer among
several processes, or keep it connected across restarts, run it on its own
(`mishmar signer run --local unix:/run/mishmar/signer.sock`) and attach with
`Signer.connect("unix:/run/mishmar/signer.sock")`; `stop()` then leaves it
running. One signer runs per config, so a second `Signer()` on a config in
use fails and names the process that holds it.
