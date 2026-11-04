---
title: Mishmar docs
description: What Mishmar does, and where to start setting up a trading account and an agent.
---

Mishmar sits between your trading agents and the venue. An agent proposes an order; Mishmar checks it against your limits and then signs it together with the agent's signer, sends it for your approval, or blocks it. The key the venue knows is split in two, one half with the agent's signer and one with Mishmar, so neither can trade alone.

These pages walk through each setup task step by step, with the screens and commands as they are today. Each page is also plain Markdown at the same address with `.md` on the end, and [llms.txt](/llms.txt) lists them all, so an agent can follow the same steps.

## Setting up

An agent trades on a **trading account**: your account at a venue, added to Mishmar once. Then each agent gets its own **trading key** on that account, split between its signer and Mishmar.

1. [Install the mishmar CLI](/start/install-the-cli/) on the machine your agent runs on.
2. Add a trading account.
3. [Add an agent on Hyperliquid](/hyperliquid/add-an-agent/).

## How it works

- [How a trading key is split](/concepts/how-a-trading-key-is-split/): why neither your agent nor Mishmar can trade alone, and what each side checks before it signs.
- [Portfolios](/start/portfolios/): groups of agents that share limits and allocated capital.
