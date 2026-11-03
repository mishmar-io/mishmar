# Mishmar API contract

`openapi.yaml` is the OpenAPI 3.1 description of the engine's HTTP API. It is
the single source of truth for every client: the web app (mishmar-console), the
SDKs in this repo and the CLI. The Go engine (mishmar-engine) implements it.
It moved here from the engine repo on 26 Oct 2026 so that the public clients
and the private engine generate from one file.

Consumers regenerate from this file. mishmar-console does it with `npm run
api:gen` and the engine with `make gen`; both read
`../mishmar/spec/openapi.yaml` from the sibling checkout, or the path in
`MISHMAR_API_SPEC`. `info.version` moves with the contract.

## Open questions from the front end (2026-10-16)

Building the seven screens against contract v0 raised these. Settle them in
the YAML before the engine implements it.

- `RuleEvaluation` should carry the rule's `asset` filter and an `applied`
  flag. Today a rule whose asset does not match the proposal records
  `observed: 0`, which reads like a real zero.
- `Rule` has an implicit "at most" comparator and no time window. The first
  five rules fit, but floors ("equity above X") and windows ("loss over 4h")
  need `comparator` and `window` fields.
- `RuleUpdate.asset` and `PortfolioUpdate.description` cannot be cleared:
  neither is nullable. The mock treats an empty string as "clear"; the contract
  should say so or allow `null`.
- `Decision` records `agentName` but not `tradingAccountName` or `portfolioName`, so a
  deleted trading account shows as its id in the ledger. Record all three at decision time.
- `resumePortfolio` does not say whether the agents its kill switch killed
  come back. The mock leaves them killed.
- Deleting a portfolio or a trading account leaves rules at that scope orphaned. Say
  whether delete cascades or is refused while rules exist.
- `ExchangeProxySetup` never echoes `TradingAccountCreate.exchange.subaccount`, and an
  HMAC venue has no field saying what to register at the venue.
- `listRules` has no `outcome` filter; the UI filters client-side for now.
- PnL days have no stated timezone. The mock uses UTC.
- Money is a JSON number. Decide before the engine ships whether amounts move
  to decimal strings.
- Approvals have no `/approvals/{id}` page in the app yet; decisions link to
  the list. Add one if the hosted tier wants deep links in pager messages.

## Decided on 2026-10-19

- **Money and quantities are decimal strings** (schema `Decimal`). The bytes an
  agent signs are the bytes the engine verifies; sums stay exact. `latencyMs`
  is the only float.
- **Explicit modes, no fallbacks.** The web app's `config.json` is required and
  names `mode: mock | live`; the engine requires `MISHMAR_MODE=paper | live`.
  Live refuses fixtures and refuses to start without a live adapter for every
  trading account's venue. Nothing is defaulted and nothing is assumed: a market order
  with no mark is blocked, stablecoins included.
- **Named operator tokens.** `MISHMAR_OPERATOR_TOKENS=name:token,...` so an
  approval's `decidedBy` names the person. `GET /whoami` returns the caller's
  name and the engine mode. The hosted tier adds real user accounts.
- **One fixture file** (`fixtures/demo.yaml`) feeds both the engine's paper-mode
  import and the web mock. The `seed` command is replaced by `import`.
- `ExchangeProxySetup.engineEgressIp` is optional: present only when the engine
  runs live and knows its egress address. A database is bound to the mode it was
  created in, so a trading account's stored setup always matches the engine serving it.
- Degraded mode (state service down): a reduce-only order passes only when the
  trading account's adapter reports that the venue enforces the reduce-only flag itself.

## Decided on 2026-10-22

- **Exchange trading accounts (0.6.0): the venue key is split between agent and engine.**
  `TradingAccountCreate.exchange` takes `keyScheme` (default per venue; Binance ed25519)
  and, for HMAC venues, the venue-issued `apiKey` and `apiSecret`, which are
  split on arrival and never stored whole. `ExchangeProxySetup` gained
  `environment` (demo for a paper engine, live otherwise) and
  `apiKeyRegistered`; `POST /trading-accounts/{id}/exchange-key` records the API key
  identifier a venue issues for a self-generated key. The agent's share
  travels in the `X-Mishmar-Agent-Share` header of `POST /proposals`, is used
  once to sign the forwarded order and is neither logged nor stored. The
  engine holds the joined key in memory only: armed at creation and by every
  proposal, forgotten at restart until the next proposal (verify answers
  `unavailable` meanwhile).
- **The decision log is the audit ledger (0.9.0).** Every ledger write appends
  a hash-chained event (`GET /ledger/events`, `GET /ledger/head`); every
  decision is engine-signed as written over its full record (verdict,
  evaluations, state, proposal, submission; not the venue ack, the approval
  outcome, the reason or latency); `Decision.submission` keeps the key id,
  nonce, submission time and price as sent so the agent's signature verifies
  from the ledger alone; `Decision.approval` embeds the approval's outcome on
  reads. `mishmar-engine export-ledger` and `mishmar-engine verify-ledger` check a chain
  offline with the public keys only. See docs/ledger.md.
- **Split keys on both venues, sealed while an approval waits (0.8.0).**
  `HyperliquidAgentSetup.agentShare` is returned once at creation like the
  exchange one: the agent wallet is split, the engine keeps a share, and a
  proposal through the trading account carries the other in `X-Mishmar-Agent-Share`.
  The engine seals a trading account's whole key in the keystore only while one of its
  decisions waits for a person (`parked:<decision id>`), removes it when the
  approval is decided or expires, and restores it on start, so a restart no
  longer loses pending approvals on either venue. `mishmar-engine split-key` converts
  a trading account from before this change.
- **Every proposal carries a deadline (0.7.0).** `ProposalSubmit.expiresAt` is
  required and under the agent's signature: after `proposedAt`, at most 24
  hours later, refused with 422 when already past. It is stored on the
  decision's proposal, caps the approval window (`Approval.expiresAt` is the
  earlier of the engine's 30-minute window and the deadline), an approval after
  it is refused as expired, and `forward` records "Not forwarded: proposal
  expired" rather than sending past it. Venue requests are still built and
  signed at forward time, so the venues' own 60-second request windows never
  constrain a human approval; the deadline is the agent's intent, not a venue
  limit.
- **Fill attribution after a restart (0.5.1).** `VenueAck.clientOrderId` keeps
  the id the venue indexed the order under (Hyperliquid's 16-byte cloid). A
  fill that arrives once the process that placed the order is gone carries
  only that id, and the ledger matches it against the ack (both ids indexed),
  so resting orders that fill after a restart, and the fills snapshot at
  reconnect, attribute correctly.

## Decided on 2026-11-03 (0.14.0)

Every account has a default portfolio, so an agent always has somewhere to go.

- An agent is still always in a portfolio: rules, the kill switch and PnL work
  at the portfolio's scope, and a portfolio is where agents share a budget. On
  a new account that was a dead end, since the agent form had no portfolio to
  offer until one was made elsewhere.
- A new account starts with a portfolio named Main, without a budget, as its
  default (`Account.defaultPortfolioId`, `Portfolio.isDefault`). An engine
  that already holds an account gives it a default at start: its oldest
  portfolio, or a new Main if it has none.
- `AgentCreate.portfolioId` is optional: absent, the agent joins the default.
- The default cannot be deleted (409). `setDefaultPortfolio` moves the default
  to another portfolio, after which the old one can be deleted once empty. The
  protection follows the default, not the name, so Main can be renamed and a
  fund can restructure without keeping it.
- A portfolio's budget is optional (`budgetUsd` absent means none), on create
  and on the portfolio itself; `PortfolioUpdate.removeBudget` takes one away,
  since the generators read a null as absent.

## Decided on 2026-10-30 (0.13.0)

Two-party ECDSA, single-hop, on Hyperliquid, and the signer leaves the
engine.

- Keys by key generation. A venue key on an ECDSA venue (Hyperliquid,
  Coinbase) is created without `engineShare` and carries `keygen: pending`;
  the agent's signer runs `signerKeygen` (two rounds over the agent API,
  the engine relaying between the two signers), after which `publicKey` is
  the API wallet's address and the key is approved and verified as before.
  A dealt ECDSA share is refused. Ed25519 (FROST) keys are made the same
  way (`curve: ed25519` at the service, FROST's distributed key generation),
  and a share the owner dealt with `mishmar keygen` is still accepted:
  the engine imports it into the signer service and keeps nothing.
- Presigning. `signerPresign` runs the three order-independent messages of
  the protocol for a batch of nonces (two rounds); each nonce is a slot,
  returned as an `EngineCommitment` without `commitment` bytes (now
  optional), under the same entitlement cap and 24-hour expiry as FROST
  commitments. `requestCommitments` answers 409 for ECDSA keys.
- One-shot Hyperliquid orders. `ProposalSubmit.venueMessage` with scheme
  `ecdsa-2p`: `message` is the exchange request's `{"action", "nonce"}`
  JSON built by the signer from the venue rules, `engineCommitmentId` the
  slot, `share` the protocol's last message, which proves itself against
  the slot and the digest. The engine rebuilds the digest, checks the
  action against the proposal, decides, and finishes the signature through
  its signer service. Parked orders go through the channel like Binance:
  the request carries the digest as `message` and the action in
  `venueRequest.body`; the answer names the slot in
  `SignerAnswer.engineCommitmentId`.
- The signer service (`signer-service.yaml`, a second document). The
  engine's shares, Paillier keys and presigned nonces live in one process
  on Mishmar's side, the public `mishmar signer` in the engine's role,
  reached by the engine alone over mTLS or a Unix socket. It signs only an
  order or a cancel it decoded itself and that matches an engine-signed
  decision, rebuilding the venue digest from the action. The engine
  relays key generation and presigning steps to it and calls `finish`
  once per order. FROST moves behind the same API (`importKey` for dealt
  shares, `issueCommitments`, `frostSign`).
- The session id of every protocol run is joint: SHA-256 of 16 random
  bytes from the agent's signer (sent in round 1) and 16 from the engine
  (returned with its first message), so no round is spent agreeing one.

## Renamed on 2026-10-28 (still 0.12.0, before it was published)

Routes are **trading accounts**: the schemas, paths, operation ids, the
`trading_account` scope kind and the `tradingAccountId` field everywhere.
The name says what the thing is to someone who has never seen Mishmar, and
"trading account" fits a broker such as Alpaca as well as an exchange or a
Hyperliquid wallet. Plain `account` stays the Mishmar account (`/account`,
the account scope, key ids). The mode `exclusive` is now `agents_only`, and
the house book is the **unassigned** book (`Position.book` and
`DiscrepancyAssignTo` take `unassigned`). `tradingAccountId` is signed, so the
vectors changed: proposals and decisions signed before the rename no longer
verify. The engine's database keeps its table and column names (`routes`,
`route_id`); existing ids keep their `rt_` prefix and new ones get `ta_`.

Also decided: only a venue that enforces reduce-only itself can have exits
(described on `submitProposal`). On any other venue a reduce-only order runs
every rule.

Added the same day, still 0.12.0:

- **Two PnL figures.** `PnlTotals.netUsd` is the PnL with open positions at
  their marks (realized + unrealized + fees + funding); the new
  `realizedNetUsd` leaves the open positions' moves out (realized + fees +
  funding). `daily_loss` reads the first, the new metric
  `daily_realized_loss` the second. A fee is the book's whose order it was;
  a funding payment is shared among the books by each one's own position,
  the unassigned book taking what they do not explain (on `PnlTotals`).
- **The engine instance.** `CommitmentBatch.engineInstance` and the
  `Mishmar-Engine-Instance` header on every signer poll name the engine run.
  The engine keeps its commitments' secret halves in memory only (a restored
  backup must never sign twice with one nonce), so a signer that sees a new
  instance drops its batch and takes a fresh one before it proposes with a
  stale one.

## Decided on 2026-10-27 (0.12.0)

From the plan doc's "Client-built venue messages and one-shot signing" and
"Positions, capital and drift" sections. Every field added inside a signed
record (the proposal, the decision) is optional and left out when absent, so
the bytes of everything signed before 0.12.0 are unchanged: the existing
vectors and every decision already in a ledger still verify.

- **Cancels.** `ProposalKind` gains `cancel`, with
  `ProposalSubmit.targetClientOrderId` naming the order to pull. A cancel
  carries no order fields, so `side`, `size`, `orderType` and `reduceOnly` are
  no longer required on `ProposalSubmit`; every other kind still needs them, and
  the engine refuses either mistake (422). A cancel's decision describes the
  target on `Proposal` (its fields, `targetClientOrderId`, `targetDecisionId`)
  with notional 0. Pinned in the vectors.
- **Exits.** An exit is a reduce-only order that moves both the agent's
  position and the trading account's net position toward zero without crossing zero, at
  most the smaller of the two (described on `submitProposal`).
  `Rule.appliesToExits` (default false) keeps a rule on for exits; every other
  rule is evaluated for the record and marked `RuleEvaluation.waived` with a
  `waivedReason`; `Decision.exit` says the engine judged the order an exit and
  is under the engine's signature.
- **One-shot signing.** `ProposalSubmit.venueMessage` (`ProposalVenueMessage`)
  carries the venue request the signer built and its half of the signature,
  under the agent's signature and kept on `DecisionSubmission` so the ledger
  can verify it again. The plan called it `venueRequest`; it is `venueMessage`
  here because `SignRequest.venueRequest` already names the method and URL.
  `POST /agent/signer/commitments` hands out the engine's single-use FROST
  commitments, capped per venue key by the account's entitlement. Venue rules
  are `GET /agent/trading-accounts/{id}/venue-rules` rather than a field on the trading account
  read, so the console's trading account reads do not carry every symbol of a venue.
  Pinned in the vectors.
- **Entitlements.** `Account.entitlements`: tier, signer transports, commitment
  batch, dedicated cell, failover cell, self-hosted. Read-only; billing or a
  signed licence will write it.
- **Positions, capital and drift.** `TradingAccount.mode` (`agents_only`, the default, or
  `shared`); `TradingAccount.restrictions` (what the engine is blocking for new risk, and
  why) and `TradingAccount.maintenance`; `openMaintenance` and `closeMaintenance`;
  `Position.book` (`agent` or `unassigned`) and `reassignPosition`, which moves a
  holding between books at the mark; `Discrepancy` with `listDiscrepancies`,
  `getDiscrepancy` and `resolveDiscrepancy` (assign to the unassigned book, an agent,
  or record a transfer). Deleting an agent moves its positions to the unassigned
  book. New ledger event kinds for all of it.
- **`not_implemented` (501)** joins the error codes, for what the contract has
  and an engine does not run yet.

## Decided on 2026-10-27 (0.11.0)

- **Two account-wide levels: pause and stop.** `Account.paused` and
  `POST /account/pause`: only reduce-only orders pass, so agents can close
  their own positions. `Account.killSwitch` (`POST /account/kill`) refuses
  everything, reduce-only included, and outranks a pause; `POST
  /account/resume` clears both. Neither closes or cancels anything: the
  engine holds only half of each trading key and never trades on its own.
  The console calls these "Pause all trading" and "Stop all trading", and an
  agent's, portfolio's or trading account's `killed` status "Stopped"; the API keeps its
  names.

## Decided on 2026-10-26 (0.10.0)

- **Agents authenticate reads and the signer channel with their key
  (`agentAuth`).** Four headers (`X-Mishmar-Key-Id`, `X-Mishmar-Signed-At`,
  `X-Mishmar-Nonce`, `X-Mishmar-Signature`) sign the method, path, query and
  body hash; the bytes are pinned in `vectors/agent-request-signing.json`.
  `POST /proposals` keeps its signature in the body.
- **Agent reads, scoped to the agent (tag `agent`).** `/agent/decisions`,
  `/agent/decisions/{id}` (another agent's decision is 404), `/agent/approvals`,
  `/agent/positions`, `/agent/pnl`, `/agent/trading-accounts/{id}`. A bot can function,
  not act: no writes.
- **The signer channel.** `POST /agent/signer/poll` (long-poll, the first
  transport; the messages do not change when a WebSocket or a webhook carries
  them) hands out `SignRequest`s once each, with `sessionId` and `round` so a
  multi-round scheme fits, `message` as the exact bytes the venue verifies, the
  agent's own signed `proposal` so a signer that did not propose can verify it,
  and the engine's commitment or signature. `POST
  /agent/signer/requests/{id}/answer` takes a share, a signature, a forwarded
  venue response (proxy) or a refusal. A request taken and not answered is
  handed out again with a higher `attempt` until it expires.
- **Trading accounts are venue accounts; trading keys belong to agents.** A trading account is an
  exchange account with a read-only key (`readApiKey`, `readApiSecret`, held
  whole, for margin, positions and fills), a Hyperliquid master account (reads
  need no key), or a Fireblocks callback. Trading account kinds are now `exchange`,
  `hyperliquid`, `cosigner_callback`. Each agent gets its own trading key per
  trading account, a `VenueKey` under `/agents/{agentId}/venue-keys`: created with the
  engine's share from `mishmar keygen` (Ed25519, ECDSA, RSA venues; the public
  key is derived from it) or with nothing for HMAC venues, where the signer the
  customer runs holds the whole key; registered at an exchange (`register` with
  the key id the venue issues) or approved by the master wallet on Hyperliquid
  (`approval`, `approval/signature`, moved here from trading accounts); verified by a
  co-signed read through the signer (`verify`); revoked with `DELETE`. The
  venue's key type alone decides the signing scheme; nothing is chosen by the
  user. Gone: the XOR split, `X-Mishmar-Agent-Share`, `agentShare`, the
  "split on arrival" `apiSecret`, `setAgentRoutes`, `registerExchangeKey`.
- **`Decision.signing`** records where an order stands with the agent's signer
  (waiting, signed, refused, unreachable); it is outside the engine signature
  like `venueAck` and `approval`.

## Still open

- What the engine runs of 0.12.0 (updated 2026-10-28): it stores
  `TradingAccount.mode` and `Rule.appliesToExits` and returns default
  entitlements (free tier, long-poll, a batch of 8). Cancels run on Binance,
  through the channel or one-shot; one-shot orders run on Binance (the
  engine checks the message with the public `venues/binance` package, adopts
  the signer's client order id and completes the FROST signature). Venue
  rules are served for Binance and Hyperliquid. Engine commitments live in
  memory only: the signer drops them when a poll names a new engine
  instance, and a one-shot proposal sent before it sees one is refused 409
  and goes again through the channel. Hyperliquid takes neither one-shot nor cancels until two-party
  ECDSA. Discrepancies are detected for fills Mishmar did not place and for
  venue positions the books do not explain (after a one-minute grace), with
  the resolve flow, maintenance windows and `reassignPosition`; equity is read
  from the venues, and so are their non-trade ledger entries: transfers move
  the high-water mark as capital; funding and rebates are recorded in the
  audit ledger (kinds `capital`, `venue_event`) and booked to the books'
  daily PnL, which the engine computes live from fills, funding and marks;
  liquidations are recorded and their fills arrive as fills Mishmar did not
  place. `unexplained_balance`,
  `ambiguous_transfer` and `stream_gap` are never raised yet. Exits are
  waived on venues that enforce reduce-only (Binance, Hyperliquid), with the
  trading account's net taken from the engine's books until the venue's own
  position is read; exits still working at the venue are not reserved yet.
- An ambiguous transfer resolves to one agent; a withdrawal that came from
  several agents' holdings needs `reassignPosition` first. Allow a split on the
  resolution if that turns out to be common.
- Two-party ECDSA (0.13.0) runs in the engine as of 30 Oct 2026: key
  generation, presigning, one-shot Hyperliquid limit orders and cancels,
  and market orders over the channel, all through the signer service
  (`mishmar signer serve`, MISHMAR_SIGNER_URL). Without a signer service
  configured these answer 503. FROST is behind the same service
  (`importKey` for a dealt share, `issueCommitments`, `frostSign`, key
  generation with `curve: ed25519`): the engine process holds no signing
  secret of any scheme, and `mishmar-engine migrate-shares` moves the
  shares an older engine kept in its keystore.
- A signer service restart without an engine restart leaves the agents'
  signers holding slots the service no longer knows: their next one-shot
  proposal is refused 409, they drop the key's slots and presign again.
  `engineInstance` does not yet fold the service's instance in.

- Exchange trading accounts on mainnet: withdrawal permission and the IP allowlist are
  readable only through Binance's main-site `sapi`
  (`/sapi/v1/account/apiRestrictions`), not the futures API, so
  `withdrawalsDisabled` and `ipAllowlisted` stay false until that check lands.
  (The read-only key is the trading account since 0.10.0, so verifying, positions and
  fill recovery no longer wait for a proposal; done 2026-10-26.) Bybit, OKX
  and Coinbase trading accounts are still simulated in paper mode and refused in live
  mode.
- Account equity and the high-water mark come only from the development
  import (`PUT /positions`) or a fixture: a fresh account blocks every order
  as "State unavailable" until someone types them in. The engine already reads
  each trading account's margin with its read-only key (Binance) or by address
  (Hyperliquid); equity should be the sum of those, refreshed on the venue
  streams, with the high-water mark kept by the engine.
- An exchange trading account's read-only key cannot be rotated: today that means
  revoking every agent's key on the trading account, deleting it and creating it again.
  Add an operation that replaces the read key in place (the trading account id, its
  agents' keys and its rules stay).
- `TradingAccountCreate.exchange` is optional in the schema but the engine requires
  `readApiKey` and `readApiSecret` for every exchange venue (422 otherwise).
  Say so in the schema, per venue, rather than in the engine alone.
- Agent-request nonces (agentAuth) live in the engine's memory only, unlike
  proposal nonces, which the ledger spends: a restart forgets them within the
  60 s freshness window. A replayed poll only takes a lease on the agent's
  open requests and a replayed answer is refused as unknown, so the exposure
  is small; persist them like proposal nonces or document the window.
- `SigningStatus.attempts` is never populated: the hub knows how many times a
  request was handed out, the adapter does not record it. Record it or drop it.
- A `cosigner_callback` trading account takes no venue keys, so nothing in the contract
  says "every agent may use it"; the console shows 0 agents on it and the
  agent's `tradingAccountIds` never lists it. Either an explicit grant for callback
  trading accounts or a field on the trading account saying it is open to all agents.

- Deleting an agent removes its keys (schema cascade) rather than keeping them as
  revoked, so the audit trail loses which key signed older decisions. Keep keys
  and mark them revoked instead.
- `TradingAccountCreate.hyperliquid.network` is required in the YAML; the engine defaults
  an empty value to `mainnet`. Make it optional with a default, or reject empty.
- `ExchangeProxySetup` does not echo `TradingAccountCreate.exchange.subaccount`, so a
  trading account cannot say which venue sub-account its key belongs to. Add `subaccount`.
- `PnlSummary` has no trading account rows; decide whether trading accounts belong there.
- How the agent's half of a split venue key signs was decided on 26 Oct 2026
  (plan doc, "Signing decisions"); the contract changes to follow are: a signer
  channel (an agent-authenticated long-poll for sign requests and a call to
  answer one, with the transport replaceable), agent-key-authenticated reads
  of the agent's own decisions, approvals, positions and PnL, a FROST trading account
  setup with the engine's share and a read-only venue key, and the removal of
  `X-Mishmar-Agent-Share` once no trading account needs it. Done: all of it landed in
  0.10.0, and the signer that answers the channel is `signer/` and `mishmar
  signer run` in this repo (FROST for Binance; two-party ECDSA, RSA and the
  proxy still to build), 2026-10-26.
- `/v1/proposals` is the only agent-facing write. The venue-shaped endpoints
  (build steps 3 and 4) will wrap it; the canonical signing rule is documented
  on `ProposalSubmit` and implemented in `internal/engine/sign.go`. (The agent
  reads and the signer channel under `/v1/agent` are agent-facing since
  0.10.0; done for those, 2026-10-26.)
