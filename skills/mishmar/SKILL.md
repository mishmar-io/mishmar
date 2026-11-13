---
name: mishmar
description: Write or change a trading bot or agent that trades through Mishmar, the risk management layer for trading bots and AI agents; set up its connected account, agent, signer and rules; or connect a model to Mishmar's MCP server. Use whenever code or setup touches the mishmar SDK, CLI, signer config, decisions and verdicts, or MCP tools.
---

# Trading through Mishmar

Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone. The trading key is split in two, one half in the agent's signer on the bot's machine and one half with Mishmar, so neither can sign an order by itself.

## Before writing code

Read the docs index as Markdown, then the pages the task needs:

```sh
curl -s https://docs.mishmar.io/llms.txt
```

Every page in one file: https://docs.mishmar.io/llms-full.txt. Bots to copy from: https://github.com/mishmar-io/mishmar/tree/main/examples (a minimal Python and a minimal TypeScript bot, and two rule walk-throughs).

## Rules for bot code

- The bot holds no exchange key. Orders go through the SDK's `Signer` (Python: `from mishmar import Signer`; TypeScript: `import { Signer } from "mishmar"`), which runs `mishmar signer run --local` as a child process from `~/.mishmar/agent.json` (or `MISHMAR_AGENT_CONFIG`). Never put an exchange API key, an agent secret (`msk_…`) or the signer config into the repo, a log line or a prompt.
- An order returns a decision with a `verdict`: `allow`, `require_approval` or `block`. Handle all three. On `require_approval`, wait (`wait(decision_id)`) or carry on and read it later. A held order expires at its own deadline (`valid_for` in Python, `validForSeconds` in TypeScript; 60 seconds by default) or after 30 minutes, whichever is first, so give an order that may wait for a person a longer deadline. On `block`, read the rule evaluations on the decision and do not resend the same order.
- Sizes and prices are decimal strings, never floats. Take tick size, step size and minimums from the venue rules (`venue_rules` / `venueRules`) and round down to the step.
- Never call the exchange's order endpoints directly. Every order goes through the signer; a direct call could not be signed, since the bot has only half the key.
- Python: `Signer()` as a context manager; `order(connected_account_id, symbol, side, size, price=...)`; `wait`, `cancel`, `decision`, `decisions`, `approvals`, `positions`, `pnl`, `connected_account`, `venue_rules`, and the connected-account reads below. TypeScript: `await new Signer().start()`; `order({ connectedAccountId, symbol, side, size, price })`; the same reads in camelCase; `await s.stop()`.
- The agent reads each connected account it was given whole, whoever acted on it: `connected_accounts`, `account_balances`, `account_positions` (the venue's net positions; `positions` is only the agent's own book), `account_transactions` (a trade's `origin.own` is true on the agent's own; page with `cursor`) and `account_equity` (value over time), camelCase in TypeScript, on `Signer` and `Client` alike. Every read shows in the account's access log. A connected account is one pool of money at a venue (a Binance login's Spot and USDⓈ-M Futures are two accounts), and every balance, position and transaction belongs to exactly one: `connection` on it is the venue login that holds it (with `sync`, how current its data is), the accounts' `equityUsd` add up to the login's value only when the agent was given all `connection.accountCount` of them (otherwise the sum is of the accounts it can see), an asset with no price is named in `unpricedAssets` instead of being counted, and a move between two accounts of one connection is a `transfer_out` on one and a `transfer_in` on the other, each naming the other in `counterpartConnectedAccountId`, which moves each account's value by the amount and the connection's by nothing.
- A read agent (one given accounts only to read) has no signer and cannot propose: it reads with its Mishmar API key alone, `Client.from_api_key()` in Python or `Client.fromApiKey()` in TypeScript (from `MISHMAR_API_KEY`), or `mishmar read accounts|balances|positions|transactions|equity` from the shell.
- Several processes trading as one agent attach to one running signer (`Signer.connect(...)`) instead of each starting its own: one signer per config.

## Setup, in order

1. Install the CLI: https://docs.mishmar.io/start/install-the-cli/
2. Add a connected account: https://docs.mishmar.io/hyperliquid/add-a-connected-account/ or https://docs.mishmar.io/binance/add-a-connected-account/
3. Add an agent and make its split trading key: https://docs.mishmar.io/hyperliquid/add-an-agent/ or https://docs.mishmar.io/binance/add-an-agent/. The signer config lands in `~/.mishmar/agent.json`.
4. Set rules in the console before the bot trades. A first set: order size over a threshold held for approval, a larger one blocked, gross exposure, daily loss and leverage blocked. A rule sits on the whole account, a venue connection, a connected account or one agent; leverage and drawdown on an agent are measured against the capital set on that agent, which they need. https://docs.mishmar.io/questions/how-do-i-limit-what-my-trading-bot-can-do/ and https://docs.mishmar.io/start/where-rules-apply/
5. Run the bot through `Signer`, or give a model the tools with `mishmar mcp`.

## A model as the agent

`mishmar mcp` serves the signer's tools over MCP on stdio: `propose_order`, `cancel_order`, `get_decision`, `wait_for_decision`, `list_decisions`, `list_positions`, `get_pnl`, `list_approvals`, `get_venue_rules`, and the read-only connected-account tools `get_identity`, `list_connected_accounts`, `get_connected_account`, `get_account_balances`, `get_account_positions`, `list_account_transactions`, `get_account_equity`. The model never sees a key. For a read agent every tool but those reads answers that it is not open to it, without calling Mishmar, and the server makes no other call. Client config:

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

Setup for each client: https://docs.mishmar.io/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/

## When something is refused

A `block` names the rules that failed and by how much; change the order or the rules, not the check. A signing refusal on the decision (`message_mismatch`, an expired proposal) means the signer saw an order other than the one proposed; look at the proposal, not the venue. A stopped account still lets cancels through.
