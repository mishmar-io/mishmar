# mishmar (public client repo)

The API contract, cross-language test vectors and SDKs for Mishmar. Public,
Apache-2.0. The engine (`mishmar-engine`, private) and the console
(`mishmar-web`, private) are sibling checkouts; both generate from the
contract here.

## Commands

```
go test ./...                       # every Go package, including the vector check and the signer's end-to-end test
go vet ./...                        # also `make lint` (vet + gofmt)
make gen                            # regenerate api/types.gen.go from spec/openapi.yaml (oapi-codegen, models only)
go build ./cmd/mishmar              # the CLI: keygen, add-agent, signer add-key, signer run, propose, version
go test ./proposal -update          # regenerate spec/vectors/proposal-signing.json after changing its cases
cd python && python -m pytest       # the Python SDK (needs `pip install -e .[test]`)
cd typescript && npm test           # the TypeScript SDK on node:test (needs `npm install`; Node 22+ runs the .ts files directly)
cd typescript && npm run typecheck
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

## Layout

```
api/         the contract as Go types, generated (oapi-codegen.yaml, doc.go's go:generate); never edited by hand
canon/       canonical JSON (Go's encoding/json rules, nulls dropped)
agentkey/    the agent's Ed25519 key: msk_ secret, base64 signatures, agentAuth request signing
proposal/    Submit (mirrors ProposalSubmit), Validate, SigningBytes, Sign, Verify, NewNonce
frost/       two-party FROST over Ed25519 (RFC 9591): Deal, Share, Party, Aggregate; one global lock
             because the library hashes through package-level state. The engine's Binance adapter and
             the signer run this round; the share file format is what the engine stores and the config holds
ecdsa2p/     two-party ECDSA over secp256k1: Deal, Share, Address; signing is not built (ErrNotImplemented)
venues/      per-venue request building and decoding, shared by the signer and the engine:
             binance/ (an order's and a cancel's parameter string; BuildOrder, DecodeOrder, CheckOrder,
             CheckRules, CheckFresh, BuildCancel, DecodeCancel, CheckRead, the client order id shape)
signer/      the agent's signer as a library: config.go (the 0600 file, validation, AddVenueKey),
             client.go (the agent-facing API, every /agent call signed), checks.go (Policy.Check: the
             refusals in order, the Binance order, cancel and read checks), hook.go, signer.go (Run, Handle,
             the FROST round), oneshot.go (Prepare and Propose: the venue request built here and the agent's
             half signed against a pre-issued engine commitment, with the channel as the fallback)
cmd/mishmar/ the CLI on cobra: keygen, add-agent (operator client lives here, not in signer/),
             signer add-key, signer run, propose (one-shot with --config where it can, --cancel, --channel), version
python/      the Python SDK: src/mishmar/{canon,agentkey,proposal,client}.py, tests against spec/vectors
typescript/  the TypeScript SDK: src/{canon,agentkey,proposal,client,index}.ts on node:crypto and fetch,
             tests/*.test.ts on node:test against spec/vectors; `.ts` imports, erasable syntax only
spec/        openapi.yaml, README.md (open questions), vectors/
```

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
- `\uXXXX` escapes in source get rewritten by editing tools (a `&` in a
  test literal became `&`, a ` ` in a regex became a raw line break).
  Build such strings from pieces (`"\\" + "u0026"`, `String.fromCharCode`)
  and pin bytes in the vectors files, which are generated, not typed.

## Git

Do not commit, branch or push unless explicitly asked.
