---
title: How do I limit what my trading bot can do?
description: The eight limits you can put on a bot or agent (order size, gross and net exposure, leverage, concentration, drawdown, daily loss), what each does when crossed, where it applies, and why it has to be enforced outside the bot.
sidebar:
  order: 2
lastUpdated: 2026-11-08
---

Put the limits outside the bot, in front of the exchange, where the bot cannot skip them, and give each limit one of two outcomes: block the order, or hold it until a person approves it. A limit inside the bot's own code is a suggestion: a bug, a bad prompt or whoever controls the machine can go around it. A limit enforced by the party that holds half of the trading key cannot be gone around, because without that half nothing gets signed.

Mishmar is that party. It checks every order a bot or agent proposes before anything reaches the exchange, and it signs the order only when the check passes or you approve it.

## The eight limits

| Limit | What it measures | When it is crossed |
| --- | --- | --- |
| Order size | the notional of one order, in USD | block, or hold for approval |
| Gross exposure | the sum of absolute position notional | block, or hold |
| Net exposure | long minus short notional, for one asset or for all | block, or hold |
| Leverage | position notional over capital | block, or hold |
| Concentration | the share of gross exposure in one asset | block, or hold |
| Drawdown | the fall from the peak, as a share of capital | block, or hold |
| Daily loss | realised plus unrealised loss today, in USD | block, or hold |
| Daily realised loss | realised loss today, with fees and funding, in USD | block, or hold |

An order is valued before it is sent, and what the limits see is its effect: the exposure, leverage and concentration the account would have once it fills. The outcome is the rule's, so the same kind of limit can appear twice with two thresholds: "hold any order over $500 for me" and "block any order over $5,000". A typical first set for a new bot:

| Rule | Threshold | Outcome |
| --- | --- | --- |
| Order size | over $500 | hold for approval |
| Order size | over $5,000 | block |
| Gross exposure | over $10,000 | block |
| Daily loss | over $1,000 | block |
| Leverage | over 2 | block |

## Where a limit applies

A limit has a scope, and every rule in scope runs on every order:

- **The account**: everything you run.
- **A portfolio**: a group of agents that share limits and [allocated capital](/start/portfolios/). Drawdown and leverage on a portfolio are measured against its capital.
- **An agent**: one bot or model, across every connected account it trades on.
- **A connected account**: one pool of money at one exchange (a Binance login's USDⓈ-M Futures account, say), whatever trades on it.

So a portfolio can cap the total, each agent inside it its own share, and a single connected account the leverage the exchange account can bear.

## What happens when a limit is crossed

- **Block.** The order is refused. The decision records which rules failed and by how much, and the bot reads it in the response. Nothing reaches the exchange.
- **Hold for approval.** The order is parked. It shows in the console, you get an email, and you approve or reject it with a passkey. Approved, it is signed and sent; rejected or expired, it is dropped. The bot can wait on the decision or carry on and check later.
- **Allow.** Inside every limit, the order is signed by both halves of the key and sent. On Hyperliquid most of the signing work is done ahead of time, so an allowed order costs one round trip.

## Stopping everything

Two levels, both outside the bot:

- **Pause all trading**, on the account, an agent or a connected account: only reduce-only orders that close a position pass, so a bot can wind down. Everything else is refused.
- **Stop all trading** is the kill switch, on the account, a portfolio, an agent or a connected account: every order is refused, reduce-only included. Cancels still go through, so a bot can pull its resting orders. Nothing is closed for you: open positions stay at the exchange, and you close them from your own exchange account or pause instead of stopping.

[How do I add a kill switch and approval step to a trading bot?](/questions/how-do-i-add-a-kill-switch-and-approval-step-to-a-trading-bot/) goes through both.

## Beyond the limits

- **A check of your own.** The bot's signer can run a program of yours before every signature, with the order on its stdin; if it exits non-zero, the signer refuses. Use it for anything the eight limits do not express: trading hours, a symbol allowlist, a sanity check against your own price feed.
- **One key per agent per account.** Each bot has its own trading key on each connected account, so stopping or revoking one touches nothing else.
- **Keys expire** 100 days after they become active.
- **A record.** Every decision, allowed, held or blocked, is signed and chained in a ledger you can export and verify.

## With a language-model agent

The same limits apply to a model's orders. The `mishmar mcp` server gives a model tools to propose orders, follow a held decision and read positions and PnL, and never a key: every proposal passes the rules, and nothing reaches the exchange without both signatures. [Use Mishmar from Claude, Cursor or any MCP client](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) has the setup.

## Set it up

Rules live in the console, on the account, a portfolio, an agent or a connected account. [Add an agent on Hyperliquid](/hyperliquid/add-an-agent/) and [on Binance](/binance/add-an-agent/) walk through the first rules, and [Portfolios](/start/portfolios/) through shared limits and capital.
