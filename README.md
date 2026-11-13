# mishmar

[![Listed on mcpservers.org](https://mcpservers.org/badge.svg)](https://mcpservers.org/servers/mishmar-io/mishmar)
[![Mishmar MCP on mcprush](https://mcprush.com/badge/mishmar-mcp/score.svg)](https://mcprush.com/mishmar-io/mishmar-mcp)

[Mishmar](https://mishmar.io) is a risk management layer for trading bots
and AI agents: every order is checked against your limits before it reaches
the exchange, large orders wait for your approval, and no key can trade
alone. This is the client side: the API contract, the test vectors every SDK
must pass, the agent's signer, the `mishmar` CLI and MCP server, and the
SDKs themselves. The engine that enforces the rules is a separate, private
repo; this one is what runs next to your agent.

Status: testnet. Mishmar runs on Hyperliquid testnet and Binance demo
trading today; real money is not live yet. The contract, the Go packages, the
agent's signer and the `mishmar` CLI are here, and so are the Python and
TypeScript SDKs (`mishmar` on PyPI and npm). Binaries for Linux and macOS are
on the Releases page.

## Layout

```
spec/openapi.yaml     the API contract, the single source of truth for every client and the engine
spec/vectors/         cross-language test vectors: proposal signing, agent request signing (agentAuth), the engine's signature on a decision
api/                  the contract as Go types (generated; `make gen`)
canon/                canonical JSON: the bytes an agent signs
agentkey/             the agent's Ed25519 identity key: parse the secret, sign, verify, sign requests
proposal/             build, validate and sign a proposal for POST /v1/proposals
decision/             verify the engine's signature on a decision, per signing version, from the decision as served
frost/                two-party FROST over Ed25519: the split key behind a Binance connectedAccount
ecdsa2p/              two-party ECDSA over secp256k1: the split key behind a Hyperliquid API wallet (keygen, presigning, signing)
signer/               the agent's signer: config, engine client, the checks, the signing rounds (FROST and two-party ECDSA), the poll loop
cmd/mishmar/          the CLI: keygen, add-agent, signer init|add-key|keygen|run, propose, read, mcp
mcp/                  the MCP server behind `mishmar mcp`: the signer's tools for a language model, over stdio
python/               the Python SDK (`mishmar` on PyPI)
typescript/           the TypeScript SDK (`mishmar` on npm); no runtime dependencies
examples/             bots to copy from: two minimal ones (Python, TypeScript) and two rule walk-throughs (Hyperliquid testnet, Binance demo trading)
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
4. When the connected account's key is split, the engine asks the agent's signer to
   co-sign the venue request over the signer channel (`/v1/agent/signer/…`);
   the signer checks that it is the order it proposed before it signs.

The exact bytes signed are specified in `proposal` and `agentkey` and pinned
in `spec/vectors/`. The engine signs each decision as it writes it
(`engineSignature`); `decision.Verify` checks one with the engine's public
key (`GET /v1/ledger/head`), rebuilding the record its `signingVersion`
names: 3 since contract 0.21.0, without `portfolioId`; 2 with it; 1 under
the names from before 0.20.0. A version it does not know is refused.
`spec/vectors/decision-signing.json` pins the three.

## The signer

When a connected account's venue key is split, the engine holds one share and the
agent's signer the other, and a venue request is signed only when both sign
it. The signer is the `signer` package as a library and `mishmar signer run`
as a process. It long-polls `POST /v1/agent/signer/poll` with the agent's
key and, for each request, refuses unless, in this order:

- the scheme is `frost-ed25519` (Binance) or `ecdsa-2p` (Hyperliquid); RSA
  and the proxy are not built yet;
- the venue key is in its config, on the request's connected account and venue;
- the request has not expired and was not answered already in this session
  (a request handed out again after it was answered is a replay, not a
  second signature);
- the purpose is `order`, `cancel` or `verify`;
- an `order` carries the agent's own proposal: signed by this signer's key,
  verifying with it, for this connected account, not yet expired;
- the message, decoded, is that order and nothing more. On Binance the
  message is the parameter string the venue verifies: it may carry only the
  parameters the engine sends; `newClientOrderId` is the request's;
  `symbol` is the request's and starts with the proposal's; `side` is the
  proposal's; `type` is `LIMIT` with `GTC` for a limit proposal, and for a
  market proposal either `MARKET` (an exit, or an older engine) or `LIMIT`
  with `IOC` at the engine's bound; `quantity` is a decimal with
  `0 < quantity <= size` (the engine floors to the lot step, never more); a
  limit `price` is within 1 % of the proposal's price and on its passive
  side (at most the price for a buy, at least for a sell), a `MARKET` order
  carries none, and the bound of an `IOC` is a positive decimal the signer
  does not judge (the engine read it off the venue's order book; the same
  on Hyperliquid); `reduceOnly` is the proposal's. A `verify` message must decode to a read and not an order: no
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
(`venueMessage`), so an allowed order needs no round trip back. That is
limit orders, cancels and, on Binance, exits (reduce-only market orders);
any other market order is bounded by the engine from the venue's order book
and signed over the channel. A refusal is
`{"kind":"refusal","refusal":{"reason":…}}` with a stable code first
(`message_mismatch: side "SELL" is not the proposed BUY`), which the engine
records on the decision's `signing`. Decimals are compared exactly; no float
touches them.

### The config file

`~/.mishmar/agent.json`, or the file `MISHMAR_AGENT_CONFIG` names, written
with mode 0600:

```json
{
  "engine": "https://api.testnet.mishmar.io/a/acc_k7f2q9x1m3n4/v1",
  "keyId": "key_k7f2q9x1m3n4_abcdefghijkl",
  "secret": "msk_testnet_k7f2q9x1m3n4_abcdefghijkl_…",
  "agentId": "agt_…",
  "venueKeys": {
    "vk_…": {
      "connectedAccountId": "ta_…",
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

The secret is the agent's API key, and since contract 0.19.0 it names its
engine and key id: `msk_<environment>_<account suffix>_<key random part>_<seed>`.
`testnet`, `stage` and `prod` are hosted Mishmar's environments, mapped to
the API host (`signer.HostedAPI`); any other environment is a self-hosted
engine, whose URL `MISHMAR_ENGINE` gives. A key issued before 0.19.0
(`msk_<6 characters>_<seed>`) still works, with the engine URL and key id
given by hand (`mishmar signer init`).

### The commands

```
go build ./cmd/mishmar

# The usual way: the agent's API key from the console (the agent's page) is
# all the signer needs. With no config at the path yet, "signer run" writes
# one from the key (the engine and key id are in the key; the agent comes
# from GET /agent/me) and starts. While it runs it keeps its trading keys in
# line with the engine's: a key the console adds for the agent is generated
# here, two rounds with the engine's signer, and the log says what the venue
# needs next (the API wallet to approve at Hyperliquid, under the name
# mishmar-<last 8 of the agent id>; the public key to register at Binance).
# No restart, no second command. MISHMAR_ENGINE names a self-hosted engine.
export MISHMAR_API_KEY=msk_testnet_…
mishmar signer run

# Everything for a new agent in one go, with an operator token: create the
# agent, issue its key, make a venue key per connected account by two-party
# key generation with the engine's signer (FROST for Binance, ECDSA for
# Hyperliquid; no machine ever holds the whole key), write the config. It
# prints what to register or approve at the venue. --capital 5000 allocates
# the agent capital: a leverage or drawdown rule on the agent is measured
# against it, and is refused while the agent has none.
mishmar add-agent --engine http://localhost:8080/v1 --token "$MISHMAR_OPERATOR_TOKEN" \
  --name momentum-1 --connected-account ta_…

# Or step by step: write the config first (from MISHMAR_API_KEY, or the key
# piped in; a key from before 0.19.0 also takes --engine, --key-id and
# --agent), then make each trading key yourself instead of leaving it to the
# running signer. Binance prints the public key to register at the exchange,
# Hyperliquid the API wallet address to approve.
echo "$MSK_SECRET" | mishmar signer init --secret-stdin
mishmar signer keygen --venue-key vk_… --connected-account ta_… --venue binance
mishmar signer keygen --venue-key vk_… --connected-account ta_… --venue hyperliquid --network testnet

# The older way for Binance, which makes the whole key exist on this machine
# for the length of the call: deal a FROST key, paste the engine share when
# adding the venue key in the console, store the agent share under the id it
# returns.
mishmar keygen --scheme ed25519 --agent-share-out agent.share > engine.share
mishmar signer add-key --venue-key vk_… --connected-account ta_… --venue binance --share agent.share

# Run the signer; then register the key at the venue and verify it in the
# console (the verification is a read the signer co-signs). One signer runs
# per config: it holds <config>.lock while it runs, and a second one on the
# same config stops at once, naming the first one's pid. A key "signer
# keygen" or "signer add-key" writes while a signer runs is picked up by it
# within a few seconds.
mishmar signer run

# Drive one order end to end with the signer running.
mishmar propose --config ~/.mishmar/agent.json --connected-account ta_… --symbol BTCUSDT --size 0.01 --price 60000
```

### Restarts and rate limits

The engine answers `503 unavailable` while it restarts (it drains for up to
28 s, so an order already in flight finishes) and `429 rate_limited` with a
`Retry-After` when a caller sends too much. Both mean the request did
nothing, and every client here sends it again on its own: the Go client
(`signer.Client`, which the signer's poll loop, `mishmar propose`,
`mishmar read` and `mishmar mcp` use), the Python `Client` and the TypeScript `Client`. They
wait the seconds in `Retry-After` when there is one, otherwise 0.25 s
doubling to 8 s with jitter, for up to 45 s after the first attempt, then
raise the last answer. A signed `/agent` request is signed anew for each
attempt, so a retry is never a replay; a proposal goes again byte for byte.
Nothing else is retried: `413 too_large` would come back the same, and any
other answer means the request was taken. Turn retries off or tune them
with `Client.Retries` (Go), `Client(..., retries=None)` (Python) or
`new Client(..., { retries: false })` (TypeScript).

### From an SDK, or from a model

The Python and TypeScript SDKs can run the signer as a child process, so the
bot holds no secret at all: `Signer` starts `mishmar signer run --local …`
with the config and talks to it over a private Unix socket (localhost with
a token on Windows). An order is an intent; the child turns it into a
proposal signed with the agent's key, sends it one-shot where it can and
through the channel otherwise, and hands back the decision. Reads pass
through the child the same way. The binary is found on the PATH, in
`MISHMAR_BIN` or under `~/.mishmar/bin`, or fetched from a release with
`download_binary` / `downloadBinary`, checked against the release's
checksums.

```python
from mishmar import Signer

with Signer() as s:                                   # ~/.mishmar/agent.json
    d = s.order("ta_…", "BTC", "buy", "0.001", price="79500")
    if d["verdict"] == "require_approval":
        d = s.wait(d["id"])
```

```ts
import { Signer } from "mishmar";

const s = await new Signer().start();
const d = await s.order({ connectedAccountId: "ta_…", symbol: "BTC", side: "buy", size: "0.001", price: "79500" });
await s.stop();
```

The child lives and dies with the bot. The SDK starts it with
`--exit-on-stdin-close` and a pipe on its stdin that it never writes to; the
pipe closes when the bot's process ends, however it ends (a crash or a
SIGKILL included), and the signer stops as it would on SIGTERM. So a signer
never stays online for an agent whose bot is gone.

A signer can also run on its own, kept up by systemd, launchd or Docker,
with `mishmar signer run --local unix:/run/mishmar/signer.sock` (or a
loopback port with a token in `MISHMAR_LOCAL_TOKEN`), and the bot attaches to
it instead of starting one: `Signer.connect("unix:/run/mishmar/signer.sock")`
in Python, `await Signer.connect("unix:/run/mishmar/signer.sock", { token })`
in TypeScript. `connect` starts nothing and `stop` leaves the signer running.
That is the way when several processes trade as one agent (one signer per
config: a second `Signer()` on the same config fails, naming the process
that holds it), or to keep the signer connected and its presigned slots warm
while the bot restarts.

`mishmar mcp` is the same signer behind an MCP server on stdio: a language
model gets tools to propose orders and cancels, follow a decision parked
for a person, read positions, PnL, approvals and venue rules, and read the
connected accounts the agent was given (below). It never sees a key, and
every order it proposes passes the engine's rules before anything reaches a
venue. Point an MCP client at
the command, with `--config` when the config is not at the default path:

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

In Claude Code, `/plugin install mishmar --marketplace mishmar-io/mishmar`
adds the server together with a skill for writing bots against the SDK
(`skills/mishmar/SKILL.md`; `kit/` has the same guidance as an AGENTS.md
snippet and a Cursor rule).

The npm and PyPI packages put the same command on the path, so a client
needs no install step: `npx -y mishmar mcp` (Node 20 or later) or `uvx
mishmar mcp` runs the binary found through `MISHMAR_BIN`, the PATH or
`~/.mishmar/bin`, and when there is none fetches the pinned release into
`~/.mishmar/bin` first, checked against the release's checksums. The MCP
registry lists the server as `io.github.mishmar-io/mishmar` (`server.json`).

### Reading connected accounts

An agent is given connected accounts and reads each one whole: its
balances, the venue's positions, its transactions and its value over time,
other agents' and the owner's activity included. A connected account is one
pool of money at a venue, with its own value (`equityUsd`): a Binance login
has a Spot and a USDⓈ-M Futures account, a Hyperliquid address has Perps
and Spot. Each names its `connection`, the venue login that holds it (its
name, which login it is, how current its data is and how many accounts it
has, `accountCount`); the accounts' values add up to the login's only when
the agent was given all of them, and a move between two of its accounts is
a `transfer_out` on one and a `transfer_in` on the other, each naming the
other in `counterpartConnectedAccountId`. A trade's `origin` says who
placed it, with `own` true on the agent's own. Every read is signed with the
agent's key and written to the account's access log. A read agent
(`permission: read`) does only this: it has a Mishmar API key, no signer and
no trading key, and the engine refuses its proposals. On hosted Mishmar
these seven reads (`GET /agent/me`, `/agent/connected-accounts`, one
account, and its `/balances`, `/positions`, `/transactions`, `/equity`) are
answered while the account's engine sleeps; the SDK `Client` and `mishmar
read` call nothing else for them.

The API key alone is enough: it names the engine and the key id
(`MISHMAR_ENGINE` names a self-hosted engine).

```python
from mishmar import Client

client = Client.from_api_key()                       # $MISHMAR_API_KEY
for account in client.connected_accounts():          # each one pool, with its connection
    print(account["connection"]["name"], account["name"], account.get("equityUsd"))
page = client.account_transactions("ta_…", since="2026-11-01T00:00:00Z", kind="trade")
mine = [t for t in page["items"] if t.get("origin", {}).get("own")]
```

```ts
import { Client } from "mishmar";

const client = Client.fromApiKey();                  // $MISHMAR_API_KEY
const page = await client.accountTransactions("ta_…", { from: "2026-11-01T00:00:00Z", kind: "trade" });
```

The reads are `me`, `connected_accounts`, `connected_account`,
`account_balances` and `account_positions` (now or `at` a past moment),
`account_transactions` (a time range, a kind, `limit` and `cursor` pages)
and `account_equity` (a range and a `resolution`), camelCase in TypeScript,
on `Client` and `Signer` alike. From the shell, with the same key or the
signer's config, `mishmar read agent`, `read accounts` (by connection, each
account with its value, and the total of a login's accounts, or of those
shown when the agent was given fewer), `read account <id>`,
`read balances <id>`, `read positions <id>`, `read transactions <id>` and
`read equity <id>`; `--json` prints the engine's answer as it came.
`mishmar mcp` serves them as read-only tools: `get_identity`,
`list_connected_accounts`, `get_connected_account`, `get_account_balances`,
`get_account_positions`, `list_account_transactions` and
`get_account_equity`.

A signer started with a read agent's key, by `mishmar signer run`, `mishmar
mcp` or an SDK's `Signer`, learns that it reads from `GET /agent/me`
(`AgentIdentity.permission`) and starts no signer channel and no key sync;
its client then refuses, unsent, anything but the seven reads. Under `mishmar
mcp` every other tool answers that it is not open to a read agent and names
the reads to use. The signer asks again every minute, so an agent made a
trade agent starts polling, and one made a read agent stops, without a
restart. An engine that does not send the field is taken as trade.

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
`MISHMAR_SIGNER_KEY`, which must not live next to the store. On Google Cloud it
can be wrapped by a KMS key instead: `MISHMAR_KMS_KEY` names the key and
`MISHMAR_SIGNER_KEY_WRAPPED` holds the wrapped form (`mishmar signer wrap-key`
makes it, from the raw key or anew). It is unwrapped at startup with the
workload's identity, each time in the KMS audit log, and if KMS cannot unwrap,
the service does not start: there is no fallback, and a raw key left beside it
is refused. `--store` is
where the sealed shares are kept, a file by default (mode 0600, replaced
atomically) or object storage: `s3://bucket/prefix` on AWS S3 or any
S3-compatible store (`MISHMAR_S3_ENDPOINT`, `MISHMAR_S3_REGION`,
`MISHMAR_S3_ACCESS_KEY`, `MISHMAR_S3_SECRET_KEY`, or the AWS SDK's own
variables and roles), `gs://bucket/prefix` on Google Cloud Storage as the
workload itself, with no keys: its identity is granted the prefix, and the
token comes from the metadata server as it does for KMS. Each key is one
object, created conditionally so two replicas
can never both create it; turn bucket versioning on for the history. Every share is opened
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
no funds. `GET /metrics` (Prometheus text format) is on a plain HTTP
listener of its own, `MISHMAR_SIGNER_METRICS_LISTEN` (default `:9103`;
empty for none): `mishmar_signer_ready`, `mishmar_signer_pending_requests`,
`mishmar_signer_oldest_pending_seconds` and `mishmar_signer_build_info`,
never a key, which is what the hosted tier's cell alerts watch.

```
export MISHMAR_SIGNER_KEY=$(openssl rand -hex 32)   # keep it: it seals the shares
mishmar signer serve --listen unix:/var/run/mishmar/signer.sock --store /var/lib/mishmar/signer-keys.json --engine-key <base64>
# and on the engine: MISHMAR_SIGNER_URL=unix:/var/run/mishmar/signer.sock
```

## Audit status

The two-party ECDSA comes from Coinbase's [cb-mpc](https://github.com/coinbase/cb-mpc),
which has had independent security reviews. Our change to it, which runs the
first three of its four signing messages ahead of time so an order needs one
round trip, has not been audited yet;
[docs/ecdsa-presign-review.md](docs/ecdsa-presign-review.md) is the note for
its reviewer, and [SECURITY.md](SECURITY.md) says how to report a flaw.
