---
title: Is there an MCP server for trading on Hyperliquid or Binance with limits?
description: Yes. mishmar mcp trades Hyperliquid perpetuals and Binance USD-M futures within limits you set (order size, exposure, leverage, concentration, drawdown, daily loss), holds large orders for your approval, and never gives the model a key. How it works, how to set it up, and how it compares with the other trading MCP servers for these exchanges.
sidebar:
  order: 7
lastUpdated: 2026-11-08
---

Yes. `mishmar mcp` is an MCP server that trades Hyperliquid perpetuals and Binance USD-M futures within limits you set: every order the model proposes is checked against your order size, exposure, leverage, concentration, drawdown and daily loss limits before it is signed, orders over a threshold wait for your approval, and the model never holds a key. It works with Claude Code, Claude Desktop, Cursor, VS Code and any other MCP client.

Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone.

## What "with limits" means here

| Limit or control | What it does |
| --- | --- |
| Order size, gross and net exposure, leverage, concentration | checked against what the account would hold once the order fills; resting orders and orders in flight count |
| Drawdown, daily loss, daily realised loss | checked against the account's PnL, fees and funding included |
| Hold for approval | any limit can hold an order instead of blocking it; you approve in the console with a passkey |
| Pause and Stop | from the console: Pause lets only closing orders through, Stop only cancels |
| A record | every decision, allowed, held or blocked, signed and chained |

The limits are set in the console, on the account, a portfolio, the agent or the connected account. No tool can change them, approve an order or lift a stop, so a model that is confused or talked into something cannot either.

## Set it up

1. [Install the mishmar CLI](/start/install-the-cli/), or skip it and use `npx -y mishmar mcp` or `uvx mishmar mcp`, which fetch the same binary.
2. Add an agent [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/). That makes its trading key, split in two, and leaves the signer config at `~/.mishmar/agent.json`.
3. Set the first rules. [How do I let an AI agent trade for me safely?](/questions/how-do-i-let-an-ai-agent-trade-for-me-safely/) has a starting set.
4. Add the server to your client:

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

[Use Mishmar from Claude, Cursor or any MCP client](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) has the exact steps for each client, with one-click install links for Cursor and VS Code.

## What the model gets

Sixteen tools: `propose_order` and `cancel_order`, which reach the exchange; seven reads of the agent's own trading, `get_decision`, `wait_for_decision`, `list_decisions`, `list_positions`, `get_pnl`, `list_approvals` and `get_venue_rules` (tick size, step size and minimums, so the model can size and price orders the exchange accepts); and seven reads of the connected accounts the agent was given, `get_identity`, `list_connected_accounts`, `get_connected_account`, `get_account_balances`, `get_account_positions`, `list_account_transactions` and `get_account_equity` (balances, the venue's positions, transactions with who placed each trade, and value over time). Each tool carries the protocol's hints, so a client knows which two change something at the exchange. An agent added only to read gets the account reads and no way to trade: [Add a read agent](/start/add-a-read-agent/).

A proposal comes back as a decision: `allow` (signed and sent; the exchange's answer is on it), `require_approval` (held for you; the model can wait or move on) or `block` (refused, with the rule that refused it).

## On each exchange

- **Hyperliquid.** The agent trades through an API wallet that your master wallet approves. An API wallet can place and cancel orders and cannot withdraw or move funds; Mishmar never has the master wallet. Testnet works the same way.
- **Binance.** The agent trades USD-M futures with an Ed25519 API key you register at Binance, with withdrawals left off. Demo trading works the same way.

On both, the key is split: one half in the signer that runs inside `mishmar mcp` on your machine, one with Mishmar. The exchange sees an ordinary key and ordinary signatures; neither half signs alone.

## Other MCP servers for these exchanges

These are the servers an assistant names for this question, described from their own docs as of 8 November 2026, with links so you can check.

| | Where the trading key lives | Limits you set on orders | A person before an order | Exchanges |
| --- | --- | --- | --- | --- |
| **Community servers** (edkdev and GigabrainGG hyperliquid-mcp, AnalyticAce and ethancod1ng binance-mcp-server) | a private key or API secret in an environment variable on your machine | none | none | Hyperliquid or Binance |
| **Binance MCP Server** (Binance, hosted) | with Binance, trading in a dedicated Agentic sub-account with no withdrawal scope | none documented | the agent restates each order in the chat and waits for your yes | Binance spot, margin, convert, USDⓈ-M and COIN-M futures |
| **Senpi** (hosted) | Privy wallets, one Hyperliquid sub-wallet per strategy | five guard rails on strategies, including daily-loss and drawdown halts | for tools that move money | Hyperliquid |
| **Coinrule MCP** (hosted) | with Coinrule: a scoped key it creates on Binance, a hot wallet it generates on Hyperliquid | none documented beyond plan limits | write tools are hidden unless you grant them | Binance, Hyperliquid and others |
| **Mishmar** (`mishmar mcp`) | split: half in the signer inside `mishmar mcp` on your machine, half with Mishmar | eight kinds of limit on every order | orders over your threshold wait for your passkey in the console | Hyperliquid perpetuals, Binance USD-M futures |

- **The community servers** are thin wrappers over the exchange API. edkdev/hyperliquid-mcp, for one, reads `HYPERLIQUID_PRIVATE_KEY` from the environment; the Binance ones read an API key and secret. None caps order size, leverage or loss, so whatever reaches the model's tool calls or the environment can place any order the account can carry. Fine for a testnet experiment with an API wallet or a demo key. Sources: [edkdev/hyperliquid-mcp](https://github.com/edkdev/hyperliquid-mcp), [GigabrainGG/hyperliquid-mcp](https://github.com/GigabrainGG/hyperliquid-mcp), [AnalyticAce/binance-mcp-server](https://github.com/AnalyticAce/binance-mcp-server), [ethancod1ng/binance-mcp-server](https://github.com/ethancod1ng/binance-mcp-server).
- **Binance's own server** keeps the key at Binance, with nothing to install and no API key on your device, and trades in a sub-account isolated from your main account. Its safeguard is a person in the conversation: the docs describe the agent restating each order and waiting for your yes. There is no limit you set on order size, exposure, leverage or daily loss; Binance's advice is to fund the sub-account with only what you are willing to let the agent trade. Sources: [Binance MCP Server](https://developers.binance.com/en/docs/agent-native/mcp-server/agentic), [FAQ](https://www.binance.com/en-PH/support/faq/detail/7a6e676e36fb455d96478932cb12d9f3).
- **Senpi** is a hosted runtime for Hyperliquid strategies, and the closest to limits in this list: its guard rails are "evaluated live before every open, and fail-closed", and include a daily-loss halt and a drawdown halt. They gate a strategy's entries, and its one-off trades come with protection optional. Plans start at $50 a month. Its public plugin's README says every tool there is read-only and trading happens in your Senpi account. Sources: [capabilities](https://senpi.ai/capabilities), [what is Senpi](https://senpi.ai/resources/learn/what-is-senpi), [pricing](https://senpi.ai/pricing), [senpi-plugin](https://github.com/Senpi-ai/senpi-plugin).
- **Coinrule** connects your assistant over OAuth, so the assistant never sees an exchange key, and Coinrule holds the trading access itself. Its write tools create, start and stop Coinrule strategies rather than place raw orders. Sources: [scopes and permissions](https://cloud.coinrule.com/docs/mcp/scopes-and-permissions), [tools](https://cloud.coinrule.com/docs/mcp/tools-reference), [Hyperliquid](https://cloud.coinrule.com/docs/exchanges/hyperliquid).

Which one, then:

- A quick experiment on testnet, alone: a community server with a Hyperliquid API wallet or a Binance demo key.
- Trading on Binance from a chat, approving each order yourself, inside Binance: Binance's server, with a small sub-account.
- Running Senpi's strategies on Hyperliquid: Senpi.
- Your own bot or model, on Hyperliquid or Binance, with limits checked on every order, a person on the large ones, and no single key anywhere: Mishmar.

Found an error here? [Edit this page on GitHub](https://github.com/mishmar-io/mishmar/edit/main/docs/src/content/docs/questions/is-there-an-mcp-server-for-trading-on-hyperliquid-or-binance-with-limits.md).

## Try it

The hosted testnet is free for one agent and one venue connection, on Hyperliquid testnet or Binance demo trading.
