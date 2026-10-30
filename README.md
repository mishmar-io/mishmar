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

Two-party ECDSA (`ecdsa2p`, Hyperliquid API wallets) is Coinbase's cb-mpc, C++
behind cgo, through the [mishmar-io fork of cb-mpc-go](https://github.com/mishmar-io/cb-mpc-go)
checked out next to this repo at the ref in `cbmpc.ref` (`go.mod` replaces
the module with `../cb-mpc-go`). Its `scripts/build_cpp.sh` builds the
library once (CMake, a static OpenSSL 3.6.1 from its `scripts/openssl`), and
`make` runs every Go command through its `scripts/go_with_cpp.sh`, which sets
the cgo flags. Linux and macOS; Windows through WSL or Docker.

```
make test                                       # Go packages, including the vectors and both signers
make lint
cd python && python -m pytest                   # the Python SDK against the same vectors
cd python && python -m pytest                   # the Python SDK against the same vectors
cd typescript && npm test                       # the TypeScript SDK against the same vectors (Node 22+)
go test ./proposal -update                      # rewrite spec/vectors after changing the cases
make gen                                        # regenerate api/ and signerapi/ from spec/
```

A tag `vX.Y.Z` builds the binary on Linux and macOS, both architectures,
and attaches it to the release (`.github/workflows/release.yml`); the
`Dockerfile` builds the same binary for Linux, running the signer service
by default.

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

- the scheme is `frost-ed25519` (Binance) or `ecdsa-2p` (Hyperliquid); RSA
  and the proxy are not built yet;
- the venue key is in its config, on the request's trading account and venue;
- the request has not expired and was not answered already in this session
  (a request handed out again after it was answered is a replay, not a
  second signature);
- the purpose is `order`, `cancel` or `verify`;
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
  symbol, side, type, quantity, price or client order id. On Hyperliquid the
  message is the EIP-712 digest, which cannot be read, so the request's body
  carries the action and nonce it came from: the signer decodes them with
  `venues/hyperliquid`, recomputes the digest for the key's network and
  compares, checks the nonce is fresh, and checks the action is the
  proposal's order (asset, side, size, a limit price on the tick rules within
  1 % on the passive side, reduce-only, the cloid of the request's client
  order id) or the cancel of its target;
- the hook, when the config names one, exits 0 (it gets the request's JSON
  on stdin and two seconds; its first line of output is the refusal reason).

Then it makes its half. FROST: it parses the engine's commitment, commits,
makes its share of the signature over the exact bytes it checked, and
answers `{"kind":"share","commitment":…,"share":…}`. Two-party ECDSA: it
spends one of its presigned slots (a batch of nonces it ran the protocol's
first three messages for with the engine's signer ahead of time, through
`POST /v1/agent/signer/presign`), computes its last message over the digest,
and answers `{"kind":"share","share":…,"engineCommitmentId":<the slot>}`;
the engine's signer verifies that message and finishes the signature alone.
On both venues an order the signer can build itself goes one-shot instead:
the venue request and the signer's half travel inside the proposal
(`venueMessage`), so an allowed order needs no round trip back. On
Hyperliquid that is limit orders and cancels; a market order is priced by
the engine at its mark and signed over the channel. A refusal is
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

`share` is the agent's share as key generation left it (or as `mishmar
keygen` wrote it). The file holds the identity secret and the agent's shares
only; an engine share in it is refused on load.

### The commands

```
go build ./cmd/mishmar

# Everything for a new agent in one go, with an operator token: create the
# agent, issue its key, make a venue key per trading account by two-party
# key generation with the engine's signer (FROST for Binance, ECDSA for
# Hyperliquid; no machine ever holds the whole key), write the config. It
# prints what to register or approve at the venue.
mishmar add-agent --engine http://localhost:8080/v1 --token "$MISHMAR_OPERATOR_TOKEN" \
  --name momentum-1 --portfolio pf_… --trading-account ta_…

# Or by hand: add the venue key in the console without a share, then run
# the key generation from this signer. Binance prints the public key to
# register at the exchange, Hyperliquid the API wallet address to approve
# from the master wallet.
mishmar signer keygen --venue-key vk_… --trading-account ta_… --venue binance
mishmar signer keygen --venue-key vk_… --trading-account ta_… --venue hyperliquid --network testnet

# The older way for Binance, which makes the whole key exist on this machine
# for the length of the call: deal a FROST key, paste the engine share when
# adding the venue key in the console, store the agent share under the id it
# returns.
mishmar keygen --scheme ed25519 --agent-share-out agent.share > engine.share
mishmar signer add-key --venue-key vk_… --trading-account ta_… --venue binance --share agent.share

# Run the signer; then register the key at the venue and verify it in the
# console (the verification is a read the signer co-signs).
mishmar signer run

# Drive one order end to end with the signer running.
mishmar propose --config ~/.mishmar/agent.json --trading-account ta_… --symbol BTCUSDT --size 0.01 --price 60000
```

## The engine's signer

`mishmar signer serve` is the same program in the engine's role
(`spec/signer-service.yaml`, the `signer/service` package): the one process
on Mishmar's side that holds the engine's share of every two-party ECDSA
key, the Paillier keys and the presigned nonces. The engine holds no signing
secret; it relays key generation and presigning between the agent's signer
and this service, and asks it to finish one signature per order. The service
signs only an order or a cancel it decoded itself and that matches a
decision the engine signed with its own key (`--engine-key`, the public key
`GET /v1/ledger/head` shows), rebuilding the venue digest from the action.
Reach it from the engine alone: a Unix socket when the two share a machine,
else mTLS; plain HTTP is for localhost. The shares are sealed at rest
(AES-256-GCM, the key id bound as associated data) under the master key in
`MISHMAR_SIGNER_KEY`, which must not live next to the store; `--store` is
where the sealed shares are kept, a file by default (mode 0600, replaced
atomically) and object storage on the hosted tier. Every share is opened
into memory at startup, so no order waits on the store, and a wrong master
key fails there rather than on the first order; replicas sharing a backend
reconcile on `--sync`, and a key not yet in memory is fetched from the
backend on first use, so a cell that takes on an account finds its shares
at once. Slots and sessions live in memory and go with a restart; never
snapshot, clone or live-migrate a running signer, since two copies of one
presigned nonce would sign two messages and give the key away. Back up
the store and the master key separately: losing either
means every agent generates its key again and every owner approves again,
nothing more, since the agent's half is useless alone and these keys hold
no funds.

```
export MISHMAR_SIGNER_KEY=$(openssl rand -hex 32)   # keep it: it seals the shares
mishmar signer serve --listen unix:/var/run/mishmar/signer.sock --store /var/lib/mishmar/signer-keys.json --engine-key <base64>
# and on the engine: MISHMAR_SIGNER_URL=unix:/var/run/mishmar/signer.sock
```
