---
title: Mishmar docs
description: What Mishmar does, and where to start. Connect an account, then add an agent that reads it or trades on it.
---

Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone.

An agent proposes an order; Mishmar checks it against your limits and then signs it together with the agent's signer, sends it for your approval, or blocks it. The key the exchange knows is split in two, one half with the agent's signer and one with Mishmar, so neither can sign by itself.

These pages walk through each setup task step by step, with the screens and commands as they are today. Each page is also plain Markdown at the same address with `.md` on the end, and [llms.txt](/llms.txt) lists them all, so an agent can follow the same steps.

## Setting up

Accounts come first. You connect a venue login to Mishmar with read access, a Hyperliquid address or a Binance login with a read-only API key, and each pool of money the venue keeps under it is a **connected account**: Perps and Spot on Hyperliquid, Spot and USDⓈ-M Futures on Binance. The login is the account's **connection**. Mishmar reads every account every minute from then on and keeps its balances, positions, value and transactions, with or without an agent.

Then you add **agents** and give each the connected accounts it may use. You choose what each agent is for:

- A **read agent** reads the accounts it was given with its Mishmar API key, and can never trade. For an assistant, a report, or anything that only needs to look.
- A **trade agent** also gets its own **trading key** on an account, split between its signer and Mishmar, and trades within your rules.

1. Connect an account: [on Hyperliquid](/hyperliquid/connect-an-account/) or [on Binance](/binance/connect-an-account/).
2. Add an agent. To trade, [install the mishmar CLI](/start/install-the-cli/) on the machine your agent runs on, then add it [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/). To read only, [add a read agent](/start/add-a-read-agent/).

- [Example bots](https://github.com/mishmar-io/mishmar/tree/main/examples): the smallest bot in Python and TypeScript (one limit order, the venue rules, waiting for an approval), and two walk-throughs that drive eight orders against seven rules on Hyperliquid testnet and Binance demo trading.
- [Set up your coding assistant](/assistants/set-up-your-coding-assistant/) so Claude Code, Cursor or any other assistant writes the bot against Mishmar correctly, and [give a model the tools](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) over MCP.

## How it works

- [How a trading key is split](/concepts/how-a-trading-key-is-split/): why neither your agent nor Mishmar can trade alone, and what each side checks before it signs.
- [What Mishmar keeps of your accounts](/concepts/what-mishmar-keeps-of-your-accounts/): what it reads, how often, for how long, and who can read it.
- [Portfolios](/start/portfolios/): groups of agents that share limits and allocated capital.

## Stuck?

[Get help](/start/get-help/): ask in the Mishmar Discord, or write to support@mishmar.io for anything about your account.
