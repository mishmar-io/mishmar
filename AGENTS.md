# mishmar (public client repo)

The API contract, cross-language test vectors and SDKs for Mishmar. Public,
Apache-2.0. The engine (`mishmar-engine`, private) and the console
(`mishmar-console`, private) are sibling checkouts; both generate from the
contract here.

## Commands

```
go test ./...                       # every Go package, including the vector check and the signer's end-to-end test
go vet ./...                        # also `make lint` (vet + gofmt)
make gen                            # regenerate api/types.gen.go from spec/openapi.yaml (oapi-codegen, models only)
go build ./cmd/mishmar              # the CLI: keygen, add-agent, signer init|add-key|keygen|run, propose, read, mcp, version
go test ./proposal -update          # regenerate spec/vectors/proposal-signing.json after changing its cases
go test ./decision -update          # regenerate spec/vectors/decision-signing.json after changing its cases
go test ./agentkey -update          # regenerate the agentAuth, fingerprint and machine-proof vectors
go test ./appsetup -update          # regenerate spec/vectors/app-setup.json after changing the setup text
cd python && python -m pytest       # the Python SDK (needs `pip install -e .[test]`)
cd typescript && npm test           # the TypeScript SDK on node:test (needs `npm install`; Node 22+ runs the .ts files directly)
cd typescript && npm run typecheck
cd docs && npm run build            # the docs site (docs.mishmar.io; needs `npm install`); `npm run dev` to write, `npx astro check` for types
```

Run them before saying a change is done. `pre-commit install` once after
cloning: the hooks in `.pre-commit-config.yaml` run the same checks on every
commit (each language only when its files are staged), plus gofmt, ruff and a
secrets scan (gitleaks).

## The contract comes first

`spec/openapi.yaml` is the single source of truth for the API. The engine's
`make gen` and the console's `npm run api:gen` read it from this checkout
(`../mishmar/spec/openapi.yaml`, or `$MISHMAR_API_SPEC`); this repo's own
`make gen` writes `api/types.gen.go` from it (types only, the engine's enum
naming, so `api.SignerAnswerKindShare` is the same name on both sides). A
change to a path or schema is a change here first, then every generator,
then the code. `spec/README.md` keeps the open questions.

## Signing is pinned

What an agent signs is bytes, not a schema: `canon` defines the canonical JSON,
`proposal.SigningBytes` the shape, and `spec/vectors/proposal-signing.json`
the exact bytes and signatures for a fixed key. Every SDK, in every language,
passes the vectors; the engine pins the same bytes in its own tests. Never
hand-edit a vectors file: change the cases in `proposal_test.go` and
regenerate.

What the engine signs for a decision (`engineSignature`) is pinned the same
way: `decision.SigningBytes` rebuilds the record from a served decision by
its `signingVersion` (3: no `portfolioId`; 2: with it; 1: version 2's under
the names from before contract 0.20.0) and refuses a version it does not
know, and `spec/vectors/decision-signing.json` (`decision_test.go`, `go test
./decision -update`) holds one case per version. Only Go verifies decisions;
the SDKs do not.

## Layout

```
api/         the contract as Go types, generated (oapi-codegen.yaml, doc.go's go:generate); never edited by hand
canon/       canonical JSON (Go's encoding/json rules, nulls dropped)
agentkey/    the agent's Ed25519 key: msk_ secret, base64 signatures, agentAuth request signing, the key's
             fingerprint (AgentKey.fingerprint) and the trade sign-in's machine proof, each pinned in spec/vectors
proposal/    Submit (mirrors ProposalSubmit), Validate, SigningBytes, Sign, Verify, NewNonce
decision/    the engine's signature on a decision: SigningBytes per signing version, Verify; refuses an unknown version
frost/       two-party FROST over Ed25519 (RFC 9591): Keygen (FROST's DKG on bytemare/dkg, two rounds), Deal (the
             older dealer), Share, Party, Aggregate, PublicKeyPEM; one global lock
             because the library hashes through package-level state. The engine's Binance adapter and
             the signer run this round; the share file format is what the engine stores and the config holds
ecdsa2p/     two-party ECDSA over secp256k1 on the mishmar-io fork of Coinbase's cb-mpc-go (cgo, see the README's
             cgo section): Keygen, Presign (slots, single use), Complete/Finish, Key, Address
venues/      per-venue request building and decoding, shared by the signer and the engine:
             binance/ (an order's and a cancel's parameter string; BuildOrder, DecodeOrder, CheckOrder,
             CheckRules, CheckFresh, BuildCancel, DecodeCancel, CheckRead, the client order id shape)
signer/      the agent's signer as a library: config.go (the 0600 file, validation, AddVenueKey; a replacement
             sits beside the key it replaces, PruneVenueKeys drops what the engine reports revoked or expired),
             setup.go (Setup: the config from the agent's API key alone, MISHMAR_API_KEY, the engine from the
             key's environment via HostedAPI or MISHMAR_ENGINE, the agent from GET /agent/me),
             sync.go (SyncKeys, run by Run every KeySyncInterval when ConfigPath is set: generates the keys
             the console added without a share, adopts keys another command wrote into the file, drops
             revoked ones, all without a restart; the map is replaced whole under the mutex, readers snapshot it),
             client.go (the agent-facing API, every /agent call signed), permission.go (Run asks GET /agent/me
             whether the agent reads or trades; a read agent's signer polls and syncs nothing, and its client
             sends only DataRead, the seven reads a hosted collector answers), checks.go (Policy.Check: the
             refusals in order, the Binance order, cancel and read checks), hook.go, signer.go (Run, Handle,
             the FROST round), oneshot.go (Prepare and Propose: the venue request built here and the agent's
             half signed against a pre-issued engine commitment, with the channel as the fallback; with two
             keys on a connected account, the one GET /agent/venue-keys reports active),
             local.go (the local API an SDK or `mishmar mcp` calls: Submit turns an intent into a signed
             proposal; `signer run --local`), service/ (the engine's signer service, `signer serve`),
             configlock/ (one long-running signer per config: `signer run` and `mcp` hold <config>.lock,
             flock or LockFileEx, no cgo), signin/ (the trade sign-in's client: loopback redirect, PKCE,
             the key on the authorize request, the machine proof at the token step, the answer checked;
             remote-machine detection; signintest/ is the stand-in control plane and engine its tests and
             cmd/mishmar's run against; docs/trade-sign-in.md settles its bytes), daemon/ (the background
             signer of `mcp --app`: one per user and home, apps' keys in ~/.mishmar/apps/<app>, the local
             API v2 on ~/.mishmar/run/signer.sock with the peer's uid checked, the home's lock, the upgrade
             handshake; no login item, so it does not survive a restart; on Windows the Manager runs in the
             MCP server's process)
appsetup/    the setup text for trading from an AI app (what the hosted set_up_trading tool answers), the
             package pin mishmar@0.5 as one constant; stdlib only, pinned in spec/vectors/app-setup.json
mcp/         the MCP server (JSON-RPC 2.0 over stdio, no dependency): sixteen tools over a Backend the signer implements,
             seven of them the reads of connected-account data (the calls a hosted collector answers; accountdata_test.go
             checks they call nothing else); app.go is `mcp --app`'s Backend over the background signer, which signs the
             computer in before a tool runs, adds get_trading_status and the app's words (the account wording constants
             are the hosted server's byte for byte: leave them)
cmd/mishmar/ the CLI on cobra: keygen, add-agent (operator client lives here, not in signer/),
             signer add-key, signer run, signer daemon, propose (one-shot with --config where it can, --cancel,
             --channel), read (the agent's reads of connected-account data, from MISHMAR_API_KEY or the config),
             mcp (--app for an AI app's trading), daemon status|stop, version
python/      the Python SDK: src/mishmar/{canon,agentkey,proposal,client,signer,data}.py (signer.py runs the binary as a
             child process and speaks its local API), tests against spec/vectors and a fake binary
typescript/  the TypeScript SDK: src/{canon,agentkey,proposal,client,data,signer,index}.ts on node:crypto and fetch,
             tests/*.test.ts on node:test against spec/vectors; `.ts` imports, erasable syntax only
kms/         Cloud KMS encrypt and decrypt over REST with the workload's metadata-server token, no client
             library and no proxy; kmstest/ is a stand-in server for tests, used by the engine's too
spec/        openapi.yaml, README.md (open questions), vectors/
docs/        docs.mishmar.io: Starlight over plain-Markdown guides (src/content/docs), each page also served
             as .md with llms.txt and llms-full.txt for agents; screenshots in public/screens; docs/README.md
             has the page template. ecdsa-presign-review.md beside it is a design review, not a page
```

## The docs follow the product

A change to a command, a flag, its output or a console screen that a page in
`docs/src/content/docs` shows is a change to that page too, in the same change:
the commands and the output on the page are copied from the code. Walk through
the steps again before moving a page's `lastUpdated`.

## Conventions

- Money, sizes and prices are decimal strings everywhere, signed as written;
  no float ever touches them. The canonicalisers refuse numbers outright.
- Times on the wire are RFC 3339 UTC with a Z suffix.
- Errors name the field and the fix (`proposal: a limit order needs a decimal price`).
- Dependencies stay few: Go has the FROST and secp256k1 libraries, cobra for
  the CLI and the oapi-codegen runtime the generated types need; Python has
  `cryptography` only; TypeScript has none at runtime.
- The signer refuses before it signs, and every refusal has a stable code
  (`signer.Refuse…`) first in its reason; add a code, not a free-text reason.
  The message is checked as the bytes the venue verifies, never the `order`
  summary. Decimals are compared with `math/big`, never floats.
- Every SDK offers the same surface: parse the agent key, build a proposal,
  validate, sign, submit, read a decision, wait for a parked one. A behaviour
  added to one SDK is added to the others, with a vector when it touches bytes.
- Every engine client retries only what the contract says did nothing (503,
  429 after `Retry-After`), re-signing each attempt, under one policy
  (`signer.Retries`, `mishmar.Retries`, `Retries` in TypeScript) that is on by
  default and can be turned off. Never retry anything else; never retry
  inside the local-API wrappers (`Signer` in Python and TypeScript), whose
  child process already does.
- `\uXXXX` escapes in source get rewritten by editing tools (a `&` in a
  test literal became `&`, a ` ` in a regex became a raw line break).
  Build such strings from pieces (`"\\" + "u0026"`, `String.fromCharCode`)
  and pin bytes in the vectors files, which are generated, not typed.

## Git

Do not commit, branch or push unless explicitly asked.
