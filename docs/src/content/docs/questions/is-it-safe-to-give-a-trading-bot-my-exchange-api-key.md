---
title: Is it safe to give a trading bot my exchange API key?
description: What an exchange API key lets a bot do, what the exchanges' own restrictions cover and what they leave open, and how a split key with limits in front of the exchange changes the answer.
sidebar:
  order: 1
lastUpdated: 2026-11-08
---

Not as the exchange hands it out. An API key with trading enabled lets whoever holds it place any order the account can carry: the whole balance, at the highest leverage the exchange allows, on any market. The restrictions the exchanges offer protect the balance from being withdrawn and the key from being used from another machine. None of them limits what the key trades.

## What the exchange restrictions cover

| Restriction | Protects against | Leaves open |
| --- | --- | --- |
| Withdrawals off | the balance leaving by transfer | the balance leaving by trading |
| IP allowlist | the key used from another machine | anything that runs on the bot's machine |
| Sub-account with a small balance | losing more than that balance | losing all of it, and topping it up by hand |
| Hyperliquid API wallet | withdrawals and transfers (it can only place and cancel orders) | any order, at any size |
| Read-only key | everything, including trading | a bot that trades |

On Hyperliquid a bot trades with an **API wallet**, an address your master wallet approves. It can sign orders and cancels and nothing else: it cannot withdraw or move funds. It has no limit on order size, leverage or exposure of its own. On Binance a key is given its permissions when you create it (enable trading, leave withdrawals off, restrict it to your IPs), and there is no per-key cap on what it trades either.

## What goes wrong in practice

- **A bug in sizing.** A decimal in the wrong place sends an order ten or a hundred times the size meant. The order is well-formed and the key is valid, so the exchange fills it.
- **A loop.** A bot that retries on an error it misreads, or re-enters a position it just closed, keeps sending orders until someone notices.
- **A leaked key.** The key sits in the bot's config, on a machine that runs other software, in a repo, in a log line. Whoever gets it trades as the bot, from anywhere the IP allowlist admits.
- **An agent that is talked into it.** A language-model agent reads market data, news and web pages. Text that says "sell everything" is an instruction to a model that does not tell data from commands, and no exchange restriction knows the difference.

## Where the limit has to be

A limit the bot enforces on itself is code the bot can skip, and so can whoever controls the bot. A limit protects you only if it sits where the bot cannot go around it: between the bot and the exchange, with the power to refuse to sign.

That is what Mishmar is: a risk management layer for trading bots and AI agents. Every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone.

- **The trading key is split in two.** One half is in the bot's signer, on the bot's machine; the other is with Mishmar. The exchange sees an ordinary key and ordinary signatures, but the private key never exists in one place. On Binance the two halves sign with FROST, the IETF's threshold signing standard ([RFC 9591](https://www.rfc-editor.org/rfc/rfc9591)); on Hyperliquid with two-party ECDSA from Coinbase's open-source [cb-mpc](https://github.com/coinbase/cb-mpc). [How a trading key is split](/concepts/how-a-trading-key-is-split/) has the details.
- **Every order is checked first.** Eight kinds of limit: order size, gross exposure, net exposure, leverage, concentration, drawdown, daily loss and daily realised loss. Each one either blocks the order or holds it for you. [How do I limit what my trading bot can do?](/questions/how-do-i-limit-what-my-trading-bot-can-do/) goes through them.
- **Large orders wait for you.** A held order shows in the console and you get an email; approving it takes a passkey.
- **There is a stop button.** Stop all trading on the account, a portfolio, one agent or one trading account, and nothing more is signed but cancels, so a bot can still pull its resting orders. Pause instead, and orders that close a position still pass.
- **Keys expire.** Every trading key expires 100 days after it becomes active, and you replace it from the console before then.
- **Every decision is recorded.** Each one is signed and chained to the one before it, and the ledger can be exported and verified.

A stolen bot's half is useless on its own: it signs nothing without Mishmar's half, and Mishmar's half signs only orders your rules allowed. Revoke the key in the console and even allowed orders stop.

## What is still your risk

- If the bot's machine and Mishmar are both compromised at the same time, whoever holds both halves can trade on that trading account within what the exchange lets the key do. That is why trading keys cannot withdraw on Hyperliquid, why you leave withdrawals off on Binance, and why keys expire.
- Mishmar limits size, exposure, leverage, concentration and loss. It does not judge the strategy. A bot that loses slowly, inside its limits, loses.
- Mishmar's change to cb-mpc, which lets an order be signed in one round trip, has not been audited yet. [What has been reviewed](/concepts/how-a-trading-key-is-split/#what-has-been-reviewed) says what has.

## Set it up

1. [Install the mishmar CLI](/start/install-the-cli/) on the bot's machine.
2. Add a trading account [on Hyperliquid](/hyperliquid/add-a-trading-account/) or [on Binance](/binance/add-a-trading-account/).
3. Add an agent [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/). Its trading key is made split, and you approve or register the public half at the exchange.
4. Set the limits, then point the bot at the SDK's `Signer`, so the bot's process holds no key at all.

The hosted testnet is free: one agent and one trading account on Hyperliquid testnet or Binance demo trading.
