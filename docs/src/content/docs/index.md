---
title: Mishmar docs
description: What Mishmar does, and where to start setting up a trading account and an agent.
---

Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone.

An agent proposes an order; Mishmar checks it against your limits and then signs it together with the agent's signer, sends it for your approval, or blocks it. The key the exchange knows is split in two, one half with the agent's signer and one with Mishmar, so neither can sign by itself.

These pages walk through each setup task step by step, with the screens and commands as they are today. Each page is also plain Markdown at the same address with `.md` on the end, and [llms.txt](/llms.txt) lists them all, so an agent can follow the same steps.

## Setting up

An agent trades on a **trading account**: your account at a venue, added to Mishmar once. Then each agent gets its own **trading key** on that account, split between its signer and Mishmar.

1. [Install the mishmar CLI](/start/install-the-cli/) on the machine your agent runs on.
2. Add a trading account: [on Hyperliquid](/hyperliquid/add-a-trading-account/) or [on Binance](/binance/add-a-trading-account/).
3. Add an agent: [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/).

- [Example bots](https://github.com/mishmar-io/mishmar/tree/main/examples): the smallest bot in Python and TypeScript (one limit order, the venue rules, waiting for an approval), and two walk-throughs that drive eight orders against seven rules on Hyperliquid testnet and Binance demo trading.
- [Set up your coding assistant](/assistants/set-up-your-coding-assistant/) so Claude Code, Cursor or any other assistant writes the bot against Mishmar correctly, and [give a model the tools](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) over MCP.

## How it works

- [How a trading key is split](/concepts/how-a-trading-key-is-split/): why neither your agent nor Mishmar can trade alone, and what each side checks before it signs.
- [Portfolios](/start/portfolios/): groups of agents that share limits and allocated capital.

## Stuck?

[Get help](/start/get-help/): ask in the Mishmar Discord, or write to support@mishmar.io for anything about your account.
