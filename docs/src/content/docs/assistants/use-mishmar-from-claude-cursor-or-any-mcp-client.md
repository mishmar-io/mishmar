---
title: Use Mishmar from Claude, Cursor or any MCP client
description: mishmar mcp is Mishmar's MCP server, ten tools to propose orders and cancels, follow a held decision and read positions, PnL, approvals and venue rules, with the agent's signer built in so the model never sees a key. Setup for Claude Code, Claude Desktop, Cursor, VS Code and any other client.
sidebar:
  order: 2
lastUpdated: 2026-11-06
---

`mishmar mcp` is Mishmar's MCP server. It runs on the machine where the agent's signer config lives, speaks the Model Context Protocol on stdin and stdout, and gives the model ten tools: propose an order or a cancel, follow a decision that waits for a person, and read decisions, positions, PnL, approvals, the trading account and the exchange's rules for sizing and pricing. The agent's signer runs inside the same process with the config's key and shares, so the model never sees a key, every order it proposes is checked against your rules, and nothing reaches the exchange without both halves of the trading key.

## Before you start

1. [Install the mishmar CLI](/start/install-the-cli/) on the machine the model's client runs on.
2. Add an agent and make its trading key, [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/). That leaves the signer config at `~/.mishmar/agent.json`.
3. Set the rules first. [How do I let an AI agent trade for me safely?](/questions/how-do-i-let-an-ai-agent-trade-for-me-safely/) has a first set.

The server holds the config's lock while it runs, like `mishmar signer run`, so it does not start next to a signer on the same config: stop that signer first, or give the model its own agent. With a config elsewhere, add `--config /path/to/agent.json` after `mcp` in every snippet below.

## Claude Code

```sh
claude mcp add --transport stdio mishmar -- mishmar mcp
```

Or, for everyone who opens the repo, a `.mcp.json` at its root:

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

## Claude Desktop

Add the same `mcpServers` entry to the config file and restart the app:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "mishmar": { "command": "mishmar", "args": ["mcp"] }
  }
}
```

## Cursor

[![Install in Cursor](https://cursor.com/deeplink/mcp-install-dark.svg)](cursor://anysphere.cursor-deeplink/mcp/install?name=mishmar&config=eyJjb21tYW5kIjoibWlzaG1hciIsImFyZ3MiOlsibWNwIl19)

Or by hand, in `.cursor/mcp.json` in the project (or `~/.cursor/mcp.json` for every project):

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

## VS Code

[Install in VS Code](vscode:mcp/install?%7B%22name%22%3A%22mishmar%22%2C%22type%22%3A%22stdio%22%2C%22command%22%3A%22mishmar%22%2C%22args%22%3A%5B%22mcp%22%5D%7D) opens the server for review before it is added. By hand, in `.vscode/mcp.json`:

```json
{ "servers": { "mishmar": { "type": "stdio", "command": "mishmar", "args": ["mcp"] } } }
```

Or from a terminal:

```sh
code --add-mcp '{"name":"mishmar","type":"stdio","command":"mishmar","args":["mcp"]}'
```

## Any other client

Every MCP client that runs a local server takes a command and its arguments. The command is `mishmar`, the arguments are `["mcp"]`, the transport is stdio. The server logs to stderr only.

## Without installing the CLI

From version 0.3.0 of the SDKs, `npx -y mishmar mcp` and `uvx mishmar mcp` do the same thing: on first run they fetch the release binary the SDK pins into `~/.mishmar/bin`, checked against the release's checksums, and run it. So a client config can be:

```json
{ "mcpServers": { "mishmar": { "command": "npx", "args": ["-y", "mishmar", "mcp"] } } }
```

## The tools

| Tool | What it does |
| --- | --- |
| `propose_order` | Propose an order on a trading account. Mishmar checks it against the rules and, when allowed, signs and sends it; the decision says what happened. A limit order needs a price; a market order is priced at the mark. |
| `cancel_order` | Cancel a resting order this agent placed, by the client order id its decision carries. No rule runs on a cancel, and a stopped account still lets it through. |
| `get_decision` | One decision: its verdict, every rule's evaluation, the signing outcome, the exchange's acknowledgement and, if it was held, the approval's state. |
| `wait_for_decision` | Follow a held decision until it is approved, rejected or expired, or until the timeout. |
| `list_decisions` | This agent's recent decisions, newest first. |
| `list_positions` | Open positions, with the mark price and unrealised PnL as Mishmar last saw them. |
| `get_pnl` | Daily PnL (realised, unrealised, fees, funding), by day. |
| `list_approvals` | Proposals that wait for a person, or were decided by one. |
| `get_trading_account` | A trading account this agent may trade on: venue, kind, status and setup, with no secret. |
| `get_venue_rules` | What the exchange accepts per symbol: tick size, step size, minimums. The model uses it to size and price orders. |

Each tool carries the protocol's hints, which a client uses to decide what to confirm with you: `propose_order` and `cancel_order` are marked as reaching the exchange and changing what is there, and the other eight as read-only.

## What the model cannot do

- See a key. Neither half of the trading key, nor the agent's own secret, is in any tool's input or output.
- Send an order the rules block, or one over the approval threshold without a person approving it in the console with a passkey.
- Approve its own order. There is no approval tool.
- Change a rule. There is no rule tool; rules live in the console.
- Withdraw. On Hyperliquid an API wallet cannot; on Binance you leave withdrawals off the key.
- Keep going after you stop it. Stop the agent, the portfolio or the account in the console, and nothing more is signed; cancels still go through.
