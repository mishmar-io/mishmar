---
name: mishmar
description: Write or change a trading bot or agent that trades through Mishmar, the risk management layer for trading bots and AI agents; set up its trading account, agent, signer and rules; or connect a model to Mishmar's MCP server. Use whenever code or setup touches the mishmar SDK, CLI, signer config, decisions and verdicts, or MCP tools.
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
- Python: `Signer()` as a context manager; `order(trading_account_id, symbol, side, size, price=...)`; `wait`, `cancel`, `decision`, `decisions`, `approvals`, `positions`, `pnl`, `trading_account`, `venue_rules`. TypeScript: `await new Signer().start()`; `order({ tradingAccountId, symbol, side, size, price })`; the same reads in camelCase; `await s.stop()`.
- Several processes trading as one agent attach to one running signer (`Signer.connect(...)`) instead of each starting its own: one signer per config.

## Setup, in order

1. Install the CLI: https://docs.mishmar.io/start/install-the-cli/
2. Add a trading account: https://docs.mishmar.io/hyperliquid/add-a-trading-account/ or https://docs.mishmar.io/binance/add-a-trading-account/
3. Add an agent and make its split trading key: https://docs.mishmar.io/hyperliquid/add-an-agent/ or https://docs.mishmar.io/binance/add-an-agent/. The signer config lands in `~/.mishmar/agent.json`.
4. Set rules in the console before the bot trades. A first set: order size over a threshold held for approval, a larger one blocked, gross exposure, daily loss and leverage blocked. https://docs.mishmar.io/questions/how-do-i-limit-what-my-trading-bot-can-do/
5. Run the bot through `Signer`, or give a model the tools with `mishmar mcp`.

## A model as the agent

`mishmar mcp` serves the signer's tools over MCP on stdio: `propose_order`, `cancel_order`, `get_decision`, `wait_for_decision`, `list_decisions`, `list_positions`, `get_pnl`, `list_approvals`, `get_trading_account`, `get_venue_rules`. The model never sees a key. Client config:

```json
{ "mcpServers": { "mishmar": { "command": "mishmar", "args": ["mcp"] } } }
```

Setup for each client: https://docs.mishmar.io/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/

## When something is refused

A `block` names the rules that failed and by how much; change the order or the rules, not the check. A signing refusal on the decision (`message_mismatch`, an expired proposal) means the signer saw an order other than the one proposed; look at the proposal, not the venue. A stopped account still lets cancels through.
