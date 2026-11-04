# Security

This repository holds the agent's side of Mishmar: the signer that holds the
agent's half of every trading key, the two-party FROST and ECDSA code, the
SDKs and the API contract. A flaw here can cost someone money, so please
report it privately.

## Reporting a vulnerability

Use GitHub's private reporting: the **Security** tab, then **Report a
vulnerability**. If you can't use GitHub, email ishai@mishmar.io.

Please include what you found, the commit or release it affects, and the
steps or code that show it. You will hear back within a few days. We will
tell you what we found, fix it, and credit you in the release notes unless
you ask us not to.

Please do not open a public issue, and test only against your own accounts
and keys, on testnet where you can.

## What is in scope

- `signer/`, `ecdsa2p/`, `frost/`, `agentkey/`, `proposal/`, `canon/`,
  `venues/`: anything that lets an order be signed that the agent didn't
  propose, or that its checks should have refused; anything that leaks a key
  share or a presigned nonce.
- The presigning change to Coinbase's cb-mpc in
  [mishmar-io/cb-mpc](https://github.com/mishmar-io/cb-mpc) (branch
  `presign`) and its binding in
  [mishmar-io/cb-mpc-go](https://github.com/mishmar-io/cb-mpc-go).
  [docs/ecdsa-presign-review.md](docs/ecdsa-presign-review.md) describes it.
- The Python and TypeScript SDKs and the `mishmar` binary as released.

Flaws in cb-mpc itself, outside our change, belong to Coinbase: see its
[security policy](https://github.com/coinbase/cb-mpc/blob/master/SECURITY.md).

## Supported versions

Mishmar is pre-release. Only the latest release gets fixes.
