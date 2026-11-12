---
title: Use Mishmar from Claude, Cursor or any MCP client
description: mishmar mcp is Mishmar's MCP server, sixteen tools to propose orders and cancels, follow a held decision, read positions, PnL, approvals and venue rules, and read the connected accounts the agent was given, with the agent's signer built in so the model never sees a key. Setup for Claude Code, Claude Desktop, Cursor, VS Code and any other client.
sidebar:
  order: 2
lastUpdated: 2026-11-12
---

`mishmar mcp` is Mishmar's MCP server. It runs on the machine where the agent's signer config lives, speaks the Model Context Protocol on stdin and stdout, and gives the model sixteen tools: propose an order or a cancel, follow a decision that waits for a person, read decisions, positions, PnL, approvals and the exchange's rules for sizing and pricing, and read the connected accounts the agent was given: their balances, the venue's positions, their transactions and their value over time. The agent's signer runs inside the same process with the config's key and shares, so the model never sees a key, every order it proposes is checked against your rules, and nothing reaches the exchange without both halves of the trading key.

## Before you start

1. [Install the mishmar CLI](/start/install-the-cli/) on the machine the model's client runs on.
2. Add an agent and make its trading key, [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/). That leaves the signer config at `~/.mishmar/agent.json`.
3. Set the rules first. [How do I let an AI agent trade for me safely?](/questions/how-do-i-let-an-ai-agent-trade-for-me-safely/) has a first set.

For a model that should only read your accounts, [add a read agent](/start/add-a-read-agent/) instead and put its Mishmar API key in the server's `env` as `MISHMAR_API_KEY`: the first time the server starts, it checks the key and writes the config itself. No rules are needed for an agent that cannot trade.

The server holds the config's lock while it runs, like `mishmar signer run`, so it does not start next to a signer on the same config: stop that signer first, or give the model its own agent. With a config elsewhere, add `--config /path/to/agent.json` after `mcp` in every snippet below. Without a config at the path yet, the agent's Mishmar API key in `MISHMAR_API_KEY` (the client's `env` block) sets the agent up; without that too, the server still starts and lists its tools, and every call returns an error that names the path it looked for.

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
| `propose_order` | Propose an order on a connected account. Mishmar checks it against the rules and, when allowed, signs and sends it; the decision says what happened. A limit order needs a price; a market order is priced at the mark. Refused for a read agent. |
| `cancel_order` | Cancel a resting order this agent placed, by the client order id its decision carries. No rule runs on a cancel, and a stopped account still lets it through. |
| `get_decision` | One decision: its verdict, every rule's evaluation, the signing outcome, the exchange's acknowledgement and, if it was held, the approval's state. |
| `wait_for_decision` | Follow a held decision until it is approved, rejected or expired, or until the timeout. |
| `list_decisions` | This agent's recent decisions, newest first. |
| `list_positions` | This agent's own book of open positions, what its orders through Mishmar opened, with the mark price and unrealised PnL as Mishmar last saw them. |
| `get_pnl` | Daily PnL (realised, unrealised, fees, funding), by day. |
| `list_approvals` | Proposals that wait for a person, or were decided by one. |
| `get_venue_rules` | What the exchange accepts per symbol: tick size, step size, minimums. The model uses it to size and price orders. |
| `get_identity` | Who this agent is: its id and name, the Mishmar account and portfolio it belongs to, and the id of the key it signs with. |
| `list_connected_accounts` | The connected accounts this agent was given, each with its venue, status, whether any agent trades on it, and how current its data is. |
| `get_connected_account` | One of them: venue, kind, status, how current its data is, and its setup, with no secret. |
| `get_account_balances` | A connected account's balances at the venue, spot and margin, each with its value in USD, and the account's whole value, now or at a past moment. |
| `get_account_positions` | The venue's net position per symbol on a connected account, whoever placed the orders behind it, now or at a past moment. |
| `list_account_transactions` | Trades, transfers, funding, fees and liquidations on a connected account, newest first. A trade's origin says who placed it, and `origin.own` marks this agent's own. |
| `get_account_equity` | A connected account's value in USD over time, oldest point first. |

Each tool carries the protocol's hints, which a client uses to decide what to confirm with you: `propose_order` and `cancel_order` are marked as reaching the exchange and changing what is there, and the other fourteen as read-only.

## What the model cannot do

- See a key. Neither half of the trading key, nor the agent's own secret, is in any tool's input or output.
- Read an account the agent was not given. Every read it makes shows on that account's **Access** tab in the console.
- Send an order the rules block, or one over the approval threshold without a person approving it in the console with a passkey.
- Approve its own order. There is no approval tool.
- Change a rule. There is no rule tool; rules live in the console.
- Withdraw. On Hyperliquid an API wallet cannot; on Binance you leave withdrawals off the key.
- Keep going after you stop it. Stop the agent, the portfolio or the account in the console, and nothing more is signed; cancels still go through.
