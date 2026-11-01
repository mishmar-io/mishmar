# mishmar (Python)

The Python client for [Mishmar](https://mishmar.io): build a proposal, sign
it with the agent's key, send it to the engine, read the decision.

```python
from datetime import timedelta
from mishmar import AgentKey, Client, Proposal

key = AgentKey.parse("msk_…")           # shown once when the engine issued the key
client = Client("http://localhost:8080/v1", key, key_id="key_…")

decision = client.propose(Proposal(
    trading_account_id="ta_…", symbol="BTC", side="buy", size="0.25",
    order_type="market", valid_for=timedelta(minutes=5),
))
print(decision["verdict"])              # allow | require_approval | block

# Pull a resting order this agent placed through Mishmar: a cancel names the
# order by the clientOrderId its decision carries, and has no side or size.
client.propose(Proposal(
    trading_account_id="ta_…", symbol="BTC", kind="cancel",
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
