---
title: You were given an agent key
description: For a contest entrant with an agent key from Mishmar. Install the mishmar CLI, start the signer with the key, wait for the trading key to be active, send a small test order on the account Mishmar runs, and read the decision. No sign-up, no passkey, no funds.
sidebar:
  order: 0
lastUpdated: 2026-11-15
---

You were sent a **Mishmar API key** (`msk_testnet_…`) for an agent on an account that Mishmar runs, on Hyperliquid testnet or Binance demo trading. The account has four rules: orders over $1,000 are blocked, orders over $500 wait for an approval that nobody gives, gross exposure is capped at $5,000, and the daily loss limit is $250. Your task is to get a trade past one of them. The [contest rules](https://mishmar.io/contest) say what counts. The contest ends 13 December 2026, 23:59 UTC.

Mishmar runs the connection and the account, so you do not sign up, add a passkey, put in funds or approve anything at the venue. You need no testnet funds of your own.

There is one wait. The first time your signer runs, it makes your half of the trading key on your machine. Mishmar then approves that key at the venue, and tells you when it is active. Until then no order is signed.

## What you need

- **The key from the email.** Keep it secret: it signs every order the agent sends.
- **macOS or Linux**, on amd64 or arm64. There is no Windows build; on Windows, use WSL.
- **The mishmar CLI, v0.5.1 or later.** If you installed earlier, install again the same way first: older versions do not work since 15 November.

## 1. Install the CLI

Follow [Install the mishmar CLI](/start/install-the-cli/), then check the version:

```sh
mishmar version
```

It prints `mishmar v0.5.1 (…)` or a later version.

## 2. Start the signer with the key

```sh
MISHMAR_API_KEY=msk_testnet_… mishmar signer run
```

The key says which Mishmar it belongs to (testnet), and the venue comes with the account, so there is nothing to choose. On first start the signer checks the key, writes its config to `~/.mishmar/agent.json` (mode 0600) and starts:

```text
level=INFO msg="signer: no config yet; setting the agent up from MISHMAR_API_KEY" path=/home/you/.mishmar/agent.json
level=INFO msg="signer: config written" path=/home/you/.mishmar/agent.json agent=agt_… name="…" keyId=key_… engine=https://api.testnet.mishmar.io/a/acc_…/v1
level=INFO msg="signer: polling" engine=https://api.testnet.mishmar.io/a/acc_…/v1 keyId=key_… venueKeys=0
```

Within a few seconds it makes your half of the trading key and logs one of these:

```text
level=INFO msg="signer: trading key made; approve its API wallet at the venue" …
level=INFO msg="signer: trading key made; register its public key at the venue" …
```

That line is written for the account's owner, which here is Mishmar. You do nothing at the venue. Your half of the key is now in `~/.mishmar/agent.json` and exists nowhere else, so keep that file: an agent set up again on another machine has no half of this key.

**Leave the signer running and wait for Mishmar's note that your key is active.** On Binance the check that makes the key active needs your signer running, and so does every order after that.

## 3. Find the account's id

When the key is active, in a second terminal:

```sh
mishmar read accounts
```

It prints one line for each account the agent was given. The `ta_…` at the start of the line is the account's id.

## 4. Send a test order

A small limit buy far below the market, so it rests on the book and does not fill. On Hyperliquid testnet:

```sh
mishmar propose --config ~/.mishmar/agent.json --connected-account ta_… --symbol BTC --side buy --size 0.0003 --price 50000
```

On Binance demo trading, use `--symbol BTCUSDT --size 0.001` and a price a few percent under the market, above $50,000. To send orders from Python or TypeScript instead, see [From your agent's code](/hyperliquid/add-an-agent/#from-your-agents-code); stop `mishmar signer run` first, since one signer runs per config.

## 5. Read the decision

The first line is the verdict. Then comes the order, one line for each rule that was checked, what the signers did, and what the venue answered. An allowed order looks something like this; the rules and numbers on your account differ:

```text
decision dec_…: allow
  buy 0.0003 BTC on hyperliquid, notional 15, latency 2.10 ms, revision 12
  passed  Max order size                       order_size observed 15, limit 150
  signing: signed (one-shot: the agent's half came with the proposal)
  venue: resting order 64462809897 at 2026-11-04T10:20:31Z
```

A refused order says `block` on the first line, with the reason in brackets. The line of each rule it broke starts with `BREACHED` and shows what was observed and the limit. Nothing is sent to the venue. An order that needs an approval says `require_approval` and ends with `parked for approval …`; on the contest account it stays there, because nobody approves it.

A refusal is Mishmar working. The contest is about an order that should have been refused and was not. When you find one, write to security@mishmar.io with the decision ids and what you sent. If you are stuck, see [Get help](/start/get-help/).
