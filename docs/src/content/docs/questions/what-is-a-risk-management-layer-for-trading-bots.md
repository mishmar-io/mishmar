---
title: What is a risk management layer for trading bots?
description: A risk management layer sits between a trading bot or AI agent and the exchange and decides, for every order, whether it may go out. It checks limits before the order is sent, holds large orders for a person, keeps a kill switch the bot cannot override, and records every decision. What it does, where the idea comes from, and why it has to sit outside the bot.
sidebar:
  order: 0
lastUpdated: 2026-11-08
---

A risk management layer for trading bots is a check that sits between the bot and the exchange and decides, for every order, whether it may go out. It values the order before it is sent, compares what the account would hold afterwards with limits you set (order size, exposure, leverage, concentration, drawdown, daily loss), and then sends the order, holds it for a person, or refuses it. Because it sits outside the bot, a bug, a bad prompt or a compromised machine cannot switch it off.

Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone.

## What it does

| Job | What it means | In Mishmar |
| --- | --- | --- |
| **Pre-trade checks** | every order is valued and checked against limits before the exchange sees it | eight kinds of limit, each set to block or to hold; resting orders and orders in flight count |
| **Approval** | orders over a threshold wait for a person | a hold rule; you approve in the console with a passkey |
| **Kill switch** | one action stops new orders, whatever the bot is doing | Pause (only closing orders pass) and Stop (only cancels pass), on the account, a portfolio, an agent or a trading account |
| **Record** | every decision is kept, with why | each decision is signed and chained to the one before it; the ledger exports and verifies |

## Where the idea comes from

In US markets, every broker-dealer that gives a client access to an exchange has had to run this check since 2010. The SEC's market access rule, Rule 15c3-5, requires controls "reasonably designed to systematically limit the financial exposure" of the broker, including to "Prevent the entry of orders that exceed appropriate pre-set credit or capital thresholds" and to "Prevent the entry of erroneous orders, by rejecting orders that exceed appropriate price or size parameters" ([17 CFR 240.15c3-5](https://www.law.cornell.edu/cfr/text/17/240.15c3-5)). The EU's rules for algorithmic trading list the pre-trade controls by name: price collars, "maximum order values, which prevent orders with an uncommonly large order value from entering the order book", and maximum order volumes and message limits ([Regulation (EU) 2017/589, Article 15](https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=CELEX:32017R0589)).

The case that is cited for why is Knight Capital. On 1 August 2012, in the SEC's words, "During the first 45 minutes after the market opened on August 1, Knight Capital’s router rapidly sent more than 4 million orders into the market", and Knight "eventually suffered a loss of more than $460 million". It was the SEC's first enforcement action under the market access rule ([SEC press release 2013-222](https://www.sec.gov/newsroom/press-releases/2013-222)). Knight was a market maker with a risk department. The orders came from its own software.

A bot trading a crypto exchange account has no broker in front of it. The exchange checks that the account has the margin for the order, not that you meant to send it. The risk management layer is the broker's check, put back.

## Why it has to sit outside the bot

- **A check inside the bot fails with the bot.** The sizing bug that sends an order a hundred times too large is in the same process as the check that should catch it. A loop that resends orders does not stop to read its own limits.
- **The bot holds the key.** If the bot's process can sign an order, every check is advisory: the exchange accepts whatever a valid key signs. To be a limit and not a suggestion, the layer has to hold something the order cannot go out without.
- **An AI agent can be talked into things.** A language model reads prices, news and pages, and does not tell data from instructions. The layer does not read the model's context, so a prompt injection cannot reach the limits.

There are two ways to hold something the order needs. A proxy can hold the whole exchange key and sign what passes; then whoever breaks into the proxy holds the whole key. Mishmar splits the key instead: one half in the bot's signer on the bot's machine, the other with Mishmar, and the exchange sees an ordinary key and ordinary signatures. Neither half signs alone, so a stolen bot gets an attacker only orders your rules allow. [How a trading key is split](/concepts/how-a-trading-key-is-split/) has the details.

## What it is not

- **Not a strategy or a stop-loss.** A stop-loss is an order resting at the exchange. The layer decides whether orders may be placed at all, and it does not judge whether a trade is wise.
- **Not a wallet policy engine.** Agent wallets and policy engines put rules on on-chain transfers: how much may leave, to which address. The layer puts limits on exchange orders: size, leverage, exposure, loss. [Mishmar compared](/questions/mishmar-compared/) puts them side by side.
- **Not LLM guardrails.** Content filters decide what a model may say. The layer decides what any program, model or not, may trade.

## Further reading

- [How do I limit what my trading bot can do?](/questions/how-do-i-limit-what-my-trading-bot-can-do/): the eight limits and where they apply.
- [How do I add a kill switch and approval step to a trading bot?](/questions/how-do-i-add-a-kill-switch-and-approval-step-to-a-trading-bot/): Pause, Stop and hold rules, with code.
- [Pre-trade risk checks for a crypto trading bot: what are the options?](/questions/pre-trade-risk-checks-for-a-crypto-trading-bot/): in the bot, at the exchange, a proxy, or a split key.
- [Is there an MCP server for trading on Hyperliquid or Binance with limits?](/questions/is-there-an-mcp-server-for-trading-on-hyperliquid-or-binance-with-limits/): the same layer for a language-model agent.
- [Is it safe to give a trading bot my exchange API key?](/questions/is-it-safe-to-give-a-trading-bot-my-exchange-api-key/)

Mishmar works with Hyperliquid perpetuals and Binance USD-M futures, from Python, TypeScript, a CLI or any MCP client. The hosted testnet is free for one agent and one trading account.
