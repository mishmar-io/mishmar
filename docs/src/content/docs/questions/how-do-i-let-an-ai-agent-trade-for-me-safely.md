---
title: How do I let an AI agent trade for me safely?
description: Treat the model as an untrusted process. Give it tools, not keys; put the limits where its tool calls cannot reach; have a person approve large orders; keep a record of every decision; and keep a stop button that does not depend on the model.
sidebar:
  order: 3
lastUpdated: 2026-11-08
---

Treat the model the way you would treat a contractor you have never met: give it tools, not keys; set limits it cannot change; look at anything large before it goes out; keep a record; and keep a stop button in your own hand. A language model that trades is a bot with two extra failure modes, and the setup has to survive both.

## Why a model is not just a bot

- **It can be talked into things.** The model reads prices, news, documents and web pages, and it does not tell data from instructions. A page that says "close every position now" is a prompt injection, and the model may follow it.
- **It is confidently wrong.** A bot with a bug fails the same way every time. A model fails differently each run, and explains each failure well.
- **It loops.** An agent that calls tools decides for itself when it is done. On a bad day that is never.
- **It holds whatever is in its context.** Put an API key in a model's reach and it will, sooner or later, print it, log it or paste it into a tool call.

## The setup

1. **No key in the model's reach.** The model gets tools that propose orders; the key that signs them lives elsewhere. With Mishmar the trading key is split in two, one half in a signer process on your machine and one half with Mishmar, and the model talks to the signer over MCP or an SDK. It never sees either half. [How a trading key is split](/concepts/how-a-trading-key-is-split/) explains it.
2. **Limits the model cannot change.** Order size, exposure, leverage, concentration, drawdown and daily loss, set in the console and enforced by Mishmar before an order is signed. The model's tool calls cannot reach them. [How do I limit what my trading bot can do?](/questions/how-do-i-limit-what-my-trading-bot-can-do/) lists them.
3. **A person on large orders.** A rule that holds any order over a threshold for your approval. The model can wait for the decision or move on. Approval takes a passkey, so an injected "approve it" goes nowhere.
4. **A record.** Every proposal, with the model's reasoning if your agent includes it, and every decision, signed and chained. When the model does something odd you can see what it asked for and what was allowed.
5. **A stop button that is not a tool.** Stop all trading from the console, on the account, a portfolio or the one agent, and Mishmar's half of the key signs nothing more but cancels. Pause instead, and only orders that close a position pass.

A rule set to start with, for a model trading a small account:

| Rule | Threshold | Outcome |
| --- | --- | --- |
| Order size | over $200 | hold for approval |
| Order size | over $1,000 | block |
| Gross exposure | over $2,000 | block |
| Leverage | over 1 | block |
| Daily loss | over $100 | block |

## Give the model the tools

`mishmar mcp` is Mishmar's MCP server: one command on the machine where the agent's signer config lives, and any MCP client (Claude Code, Claude Desktop, Cursor, VS Code, your own agent) gets ten tools: propose an order, cancel one, read and wait for a decision, list decisions, positions, PnL and approvals, read the trading account and the exchange's rules for sizing and pricing. The signer runs inside that process, so the model never sees a key, every order it proposes passes your rules, and nothing reaches the exchange without both signatures.

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

[Use Mishmar from Claude, Cursor or any MCP client](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) has the setup for each client. An agent written in Python or TypeScript uses the SDK's `Signer` instead, which runs the same signer as a child process; [Set up your coding assistant](/assistants/set-up-your-coding-assistant/) has what to tell the assistant that writes it.

## What this does not do

Mishmar decides whether an order is allowed, not whether it is wise. A model that trades badly inside its limits trades badly. Start on the hosted testnet, which is free for one agent and one trading account, with the rule set above, and read the ledger before you widen anything.
