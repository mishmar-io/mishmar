---
title: You were given an agent key
description: For a contest entrant with an agent key from Mishmar. Install the mishmar CLI, start the signer with the key, wait for the trading key to be active, send a small test order on the account Mishmar runs, and read the decision. No sign-up, no passkey, no funds.
sidebar:
  order: 0
lastUpdated: 2026-11-23
---

You were sent a **Mishmar API key** (`msk_testnet_…`) for an agent on an account that Mishmar runs, on Hyperliquid testnet or Binance demo trading. Your seat shares its testnet account with up to two other entrants, but the four rules apply to your agent alone: their trades never count toward your limits. Your agent has four rules: orders over $1,000 are blocked, orders over $500 wait for an approval that nobody gives, gross exposure is capped at $5,000, and the daily loss limit is $250. Your task is to get a trade past one of them. The [contest rules](https://mishmar.io/contest) say what counts. The contest ends 13 December 2026, 23:59 UTC.

Mishmar runs the connection and the account, so you do not sign up, add a passkey, put in funds or approve anything at the venue. You need no testnet funds of your own.

There is one wait. The first time your signer runs, it makes your half of the trading key on your machine. Mishmar then approves that key at the venue, and tells you when it is active. Until then no order is signed.

## What you need

- **The key from the email.** Keep it secret: it signs every order the agent sends.
- **macOS or Linux**, on amd64 or arm64. There is no Windows build; on Windows, use WSL.
- **The mishmar CLI, v0.6.0 or later.** If you installed earlier, update first ([Update an existing install](/start/install-the-cli/#update-an-existing-install)): older versions are refused.
- **HTTPS from the signer's machine to the venue.** On Hyperliquid testnet the signer must reach `api.hyperliquid-testnet.xyz`: it reads Hyperliquid's own list of perp markets from there and does not take it from Mishmar. When it cannot reach it, it signs no new orders. On Binance demo trading the signer needs to reach only Mishmar.

## 1. Install the CLI

Follow [Install the mishmar CLI](/start/install-the-cli/), then check the version:

```sh
mishmar version
```

It prints `mishmar v0.6.0 (…)` or a later version.

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

**Leave the signer running, and reply to your key email with "signer running".** Nothing tells Mishmar by itself that your half is made, so your reply is what starts our part. We switch the key on and write back when it is active, usually within a day. On Binance the check that makes the key active needs your signer running, and so does every order after that.

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

On Binance demo trading, use `--symbol BTCUSDT --size 0.001` and a price a few percent under the market, above $50,000. To send orders from Python instead, see [From your agent's code](#from-your-agents-code) below.

## 5. Read the decision

The first line is the verdict. Then comes the order, one line for each rule that was checked, what the signers did, and what the venue answered. An allowed order looks something like this; the rules and numbers on your account differ:

```text
decision dec_…: allow
  buy 0.0003 BTC on hyperliquid, notional 15, latency 2.10 ms, revision 12
  passed  Order size                           order_size observed 15, limit 1000
  passed  Order size, approval                 order_size observed 15, limit 500
  passed  Gross exposure                       gross_exposure observed 15, limit 5000
  passed  Daily loss                           daily_loss observed 0, limit 250
  signing: signed (one-shot: the agent's half came with the proposal)
  venue: resting order 64462809897 at 2026-11-04T10:20:31Z
```

`observed` is the order at the market when the market is above its price, so it can be more than `notional`.

A refused order says `block` on the first line, with the reason in brackets. The line of each rule it broke starts with `BREACHED` and shows what was observed and the limit. Nothing is sent to the venue. An order that needs an approval says `require_approval` and ends with `parked for approval …`. On the contest account nobody approves it, so it expires and is never sent. A held order waits up to 30 minutes, or until the order's own deadline if that is sooner (60 seconds unless you set a longer one).

A refusal is Mishmar working. The contest is about an order that should have been refused and was not. When you find one, write to security@mishmar.io with the decision ids and what you sent. If you are stuck, see [Get help](/start/get-help/).

## From your agent's code

Your agent can send the same orders from Python. Mishmar checks every order before it is signed, whichever way it is sent.

1. Stop `mishmar signer run` (Ctrl-C). The SDK starts the signer itself, from the same `~/.mishmar/agent.json`, and one signer runs per config. It uses the mishmar CLI you installed in step 1.
2. Install the SDK:

   ```sh
   pip install mishmar
   ```

3. Send one order and print what came back. Use your account's `ta_…` from step 3:

   ```python
   from mishmar import Signer

   with Signer() as s:  # ~/.mishmar/agent.json, from step 2
       d = s.order("ta_…", "BTC", "buy", "0.0003", price="50000")
       print(d["id"], d["verdict"])
       for e in d["evaluations"]:
           mark = "passed  " if e["passed"] else "BREACHED"
           print(mark, e["ruleName"], e["metric"], "observed", e["observed"], "limit", e["threshold"])
   ```

   On Binance demo trading, use `"BTCUSDT"` and `"0.001"`, with a price a few percent under the market, as in step 4.

It prints one line for each of the account's four rules. An order that passes looks something like this; the rule names and numbers on your account differ:

```text
dec_… allow
passed   Order size order_size observed 27 limit 1000
passed   Order size, approval order_size observed 27 limit 500
passed   Gross exposure gross_exposure observed 27 limit 5000
passed   Daily loss daily_loss observed 0 limit 250
```

The same order at size `"0.03"` is worth more than $1,000 and is refused. Nothing is sent to the venue:

```text
dec_… block
BREACHED Order size order_size observed 2700 limit 1000
BREACHED Order size, approval order_size observed 2700 limit 500
passed   Gross exposure gross_exposure observed 2700 limit 5000
passed   Daily loss daily_loss observed 0 limit 250
```

As in step 5, `observed` for an order size is the order at the market when the market is above its price.

An order over $500 comes back `require_approval`. On the contest account nobody approves it, so it waits until its deadline (at most 30 minutes) and is never sent.

If an order is blocked by an order-size rule, retry once at or under the limit. If it is blocked for any other reason, stop and ask the agent's owner.

The [repository's README](https://github.com/mishmar-io/mishmar#readme) has the TypeScript SDK, which works the same way, and `mishmar mcp`, which gives an LLM agent the same actions as tools.
