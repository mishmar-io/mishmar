---
title: Set up your coding assistant
description: "Give Claude Code, Cursor, Codex or any other coding assistant what it needs to write a bot against Mishmar correctly, in one line each: a skill, a rule file or an AGENTS.md snippet, and a link that opens Claude with the docs loaded."
sidebar:
  order: 1
lastUpdated: 2026-11-08
---

An assistant that has never seen Mishmar will write a bot that holds an exchange key, treats a decision as a fill and sends prices as floats. The files on this page tell it the three things that matter (the bot holds no key, an order comes back with one of three verdicts, numbers are decimal strings) and where the docs are as Markdown. Each is a few lines. Add the one for your assistant to the bot's repo.

## Claude Code

The skill, which Claude Code loads on its own when a task touches Mishmar:

```sh
mkdir -p .claude/skills/mishmar && curl -fsSL https://raw.githubusercontent.com/mishmar-io/mishmar/main/skills/mishmar/SKILL.md -o .claude/skills/mishmar/SKILL.md
```

Or the plugin, which brings the same skill and the [MCP server](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) together, from inside Claude Code:

```text
/plugin install mishmar --marketplace mishmar-io/mishmar
```

On a Claude Code older than 2.1.275 it is two steps: `/plugin marketplace add mishmar-io/mishmar`, then `/plugin install mishmar@mishmar`.

Either way, the snippet below also belongs in the repo's `CLAUDE.md`, so it is read on every task, not only when the skill triggers.

## Cursor

A rule that applies to every file:

```sh
mkdir -p .cursor/rules && curl -fsSL https://raw.githubusercontent.com/mishmar-io/mishmar/main/kit/mishmar.mdc -o .cursor/rules/mishmar.mdc
```

## Codex, Copilot, Gemini CLI, Windsurf, Aider and the rest

They read `AGENTS.md`. Append the snippet:

```sh
curl -fsSL https://raw.githubusercontent.com/mishmar-io/mishmar/main/kit/AGENTS.md >> AGENTS.md
```

This is what it says:

```markdown
## Trading through Mishmar

This bot trades through [Mishmar](https://mishmar.io), a risk management layer for trading bots and AI agents: every order is checked against the owner's limits before it reaches the exchange, large orders wait for the owner's approval, and no key can trade alone.

- The bot holds no exchange key. Orders go through the Mishmar SDK's `Signer` (Python: `from mishmar import Signer`; TypeScript: `import { Signer } from "mishmar"`), which runs the agent's signer as a child process from `~/.mishmar/agent.json` (or the file named in `MISHMAR_AGENT_CONFIG`). Never put an exchange API key, an agent secret (`msk_…`) or the signer config into this repo, a log line or a prompt.
- An order comes back as a decision with a `verdict`: `allow`, `require_approval` or `block`. Handle all three. On `require_approval`, wait for the decision (`wait`) or carry on and read it later. A held order expires at its own deadline (`valid_for` in Python, `validForSeconds` in TypeScript; 60 seconds by default) or after 30 minutes, whichever is first, so give an order that may wait for a person a longer deadline. On `block`, read the rule evaluations on the decision and do not resend the same order.
- Sizes and prices are decimal strings, never floats. Size and price from the venue rules the signer returns (`venue_rules` / `venueRules`: tick size, step size, minimums).
- Never call the exchange's order endpoints directly; every order goes through the signer. The signer and Mishmar each hold half of the trading key, so a direct call could not be signed anyway.
- `mishmar mcp` is the same signer as an MCP server, for an agent that is a language model.
- The setup steps, the rules and the API, as Markdown: https://docs.mishmar.io/llms.txt (all pages in one file: https://docs.mishmar.io/llms-full.txt). Example bots: https://github.com/mishmar-io/mishmar/tree/main/examples
```

## Open Claude with the docs loaded

If the Claude desktop app is installed, this link opens a new chat with the prompt filled in (nothing is sent until you press enter): [Open in Claude](claude://claude.ai/new?q=Read%20https%3A%2F%2Fdocs.mishmar.io%2Fllms.txt%2C%20then%20help%20me%20set%20up%20a%20trading%20bot%20that%20trades%20through%20Mishmar%20on%20the%20hosted%20testnet%3A%20install%20the%20mishmar%20CLI%2C%20add%20a%20trading%20account%20and%20an%20agent%2C%20set%20the%20first%20rules%2C%20and%20write%20the%20bot%20with%20the%20Python%20SDK%20Signer.).

The same prompt, for any assistant:

```text
Read https://docs.mishmar.io/llms.txt, then help me set up a trading bot that trades through Mishmar on the hosted testnet: install the mishmar CLI, add a trading account and an agent, set the first rules, and write the bot with the Python SDK Signer.
```

## For the agent itself

A bot written in Python or TypeScript trades through the SDK's `Signer`; [the example bots](https://github.com/mishmar-io/mishmar/tree/main/examples) are the place to start. An agent that is a language model gets its tools from the MCP server instead: [Use Mishmar from Claude, Cursor or any MCP client](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/).
