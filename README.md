# mishmar

The client side of [Mishmar](https://mishmar.io), guardrails for trading
agents and bots: the API contract, the test vectors every SDK must pass, and
the SDKs themselves. The engine that enforces the rules is a separate,
private repo; this one is what runs next to your agent.

Status: pre-release. The contract, the Go packages, the agent's signer and
the `mishmar` CLI are here, and so are the Python and TypeScript SDKs;
nothing is published to a package index yet.

## Layout

```
spec/openapi.yaml     the API contract, the single source of truth for every client and the engine
spec/vectors/         cross-language test vectors: proposal signing, agent request signing (agentAuth)
api/                  the contract as Go types (generated; `make gen`)
canon/                canonical JSON: the bytes an agent signs
agentkey/             the agent's Ed25519 identity key: parse the secret, sign, verify, sign requests
proposal/             build, validate and sign a proposal for POST /v1/proposals
frost/                two-party FROST over Ed25519: the split key behind a Binance tradingAccount
ecdsa2p/              two-party ECDSA over secp256k1: the split key behind a Hyperliquid API wallet (dealing only; signing not built yet)
signer/               the agent's signer: config, engine client, the checks, the FROST round, the poll loop
cmd/mishmar/          the CLI: keygen, add-agent, signer add-key, signer run, propose
python/               the Python SDK (`mishmar` on PyPI, when released)
typescript/           the TypeScript SDK (`mishmar` on npm, when released); no runtime dependencies
```

## Run the checks

```
go test ./...                                   # Go packages, including the vectors and the signer
cd python && python -m pytest                   # the Python SDK against the same vectors
cd typescript && npm test                       # the TypeScript SDK against the same vectors (Node 22+)
go test ./proposal -update                      # rewrite spec/vectors after changing the cases
make gen                                        # regenerate api/ from spec/openapi.yaml
```

## How an agent talks to the engine

1. The engine issues the agent a key and shows its secret once (`msk_…`).
   The engine keeps the public half.
2. The agent builds a proposal (symbol, side, size, price or market, a
   deadline, a nonce), signs the canonical JSON of it with the key, and
   POSTs it to `/v1/proposals`.
3. The engine checks the signature, evaluates every rule in scope, records
   the decision, and either forwards the order, parks it for a person, or
   blocks it. The response is the decision; a parked one resolves later and
   is read back from `/v1/agent/decisions/{id}`, a request the agent signs
   with the same key (the `agentAuth` headers).
4. When the trading account's key is split, the engine asks the agent's signer to
   co-sign the venue request over the signer channel (`/v1/agent/signer/…`);
   the signer checks that it is the order it proposed before it signs.

The exact bytes signed are specified in `proposal` and `agentkey` and pinned
in `spec/vectors/`.

## The signer

When a trading account's venue key is split, the engine holds one share and the
agent's signer the other, and a venue request is signed only when both sign
it. The signer is the `signer` package as a library and `mishmar signer run`
as a process. It long-polls `POST /v1/agent/signer/poll` with the agent's
key and, for each request, refuses unless, in this order:

- the scheme is `frost-ed25519` (two-party ECDSA, RSA and the proxy are not
  built yet);
- the venue key is in its config, on the request's trading account and venue;
- the request has not expired and was not answered already in this session
  (a request handed out again after it was answered is a replay, not a
  second signature);
- the purpose is `order` or `verify` (`cancel` is not produced yet);
- an `order` carries the agent's own proposal: signed by this signer's key,
  verifying with it, for this trading account, not yet expired;
- the message, decoded, is that order and nothing more. On Binance the
  message is the parameter string the venue verifies: it may carry only the
  parameters the engine sends; `newClientOrderId` is the request's;
  `symbol` is the request's and starts with the proposal's; `side` is the
  proposal's; `type` is `MARKET` for a market proposal and `LIMIT` for a
  limit one; `quantity` is a decimal with `0 < quantity <= size` (the engine
  floors to the lot step, never more); a limit `price` is within 1 % of the
  proposal's price and on its passive side (at most the price for a buy, at
  least for a sell), and a market order carries none; `reduceOnly` is the
  proposal's. A `verify` message must decode to a read and not an order: no
  symbol, side, type, quantity, price or client order id;
- the hook, when the config names one, exits 0 (it gets the request's JSON
  on stdin and two seconds; its first line of output is the refusal reason).

Then it runs its half of the FROST round: parses the engine's commitment,
commits, makes its share of the signature over the exact bytes it checked,
and answers `{"kind":"share","commitment":…,"share":…}`. A refusal is
`{"kind":"refusal","refusal":{"reason":…}}` with a stable code first
(`message_mismatch: side "SELL" is not the proposed BUY`), which the engine
records on the decision's `signing`. Decimals are compared exactly; no float
touches them.

### The config file

`~/.mishmar/agent.json`, or the file `MISHMAR_AGENT_CONFIG` names, written
with mode 0600:

```json
{
  "engine": "http://localhost:8080/v1",
  "keyId": "key_acc1_abcdefghijkl",
  "secret": "msk_abcdef_…",
  "venueKeys": {
    "vk_…": {
      "tradingAccountId": "ta_…",
      "venue": "binance",
      "scheme": "frost-ed25519",
      "share": {"scheme": "frost-ed25519", "id": 1, "keyShare": "…", "group": "…"}
    }
  },
  "hook": "/usr/local/bin/check-order"
}
```

`share` is the agent's share exactly as `mishmar keygen` wrote it. The file
holds the identity secret and the agent's shares only; an engine share in it
is refused on load.

### The commands

```
go build ./cmd/mishmar

# Everything for a new agent in one go, with an operator token: create the
# agent, issue its key, deal a venue key per trading account, send the engine its
# share, write the config. It prints what to register or approve at the venue.
mishmar add-agent --engine http://localhost:8080/v1 --token "$MISHMAR_OPERATOR_TOKEN" \
  --name momentum-1 --portfolio pf_… --trading-account ta_…

# Or by hand: deal a key, paste the engine share when adding the venue key
# to the agent in the console, store the agent share under the id it returns.
mishmar keygen --scheme ed25519 --agent-share-out agent.share > engine.share
mishmar signer add-key --venue-key vk_… --trading-account ta_… --venue binance --share agent.share

# Run the signer; then register the key at the venue and verify it in the
# console (the verification is a read the signer co-signs).
mishmar signer run

# Drive one order end to end with the signer running.
mishmar propose --config ~/.mishmar/agent.json --trading-account ta_… --symbol BTCUSDT --size 0.01 --price 60000
```
