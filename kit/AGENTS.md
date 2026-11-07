## Trading through Mishmar

This bot trades through [Mishmar](https://mishmar.io), a risk management layer for trading bots and AI agents: every order is checked against the owner's limits before it reaches the exchange, large orders wait for the owner's approval, and no key can trade alone.

- The bot holds no exchange key. Orders go through the Mishmar SDK's `Signer` (Python: `from mishmar import Signer`; TypeScript: `import { Signer } from "mishmar"`), which runs the agent's signer as a child process from `~/.mishmar/agent.json` (or the file named in `MISHMAR_AGENT_CONFIG`). Never put an exchange API key, an agent secret (`msk_…`) or the signer config into this repo, a log line or a prompt.
- An order comes back as a decision with a `verdict`: `allow`, `require_approval` or `block`. Handle all three. On `require_approval`, wait for the decision (`wait`) or carry on and read it later. On `block`, read the rule evaluations on the decision and do not resend the same order.
- Sizes and prices are decimal strings, never floats. Size and price from the venue rules the signer returns (`venue_rules` / `venueRules`: tick size, step size, minimums).
- Never call the exchange's order endpoints directly; every order goes through the signer. The signer and Mishmar each hold half of the trading key, so a direct call could not be signed anyway.
- `mishmar mcp` is the same signer as an MCP server, for an agent that is a language model.
- The setup steps, the rules and the API, as Markdown: https://docs.mishmar.io/llms.txt (all pages in one file: https://docs.mishmar.io/llms-full.txt). Example bots: https://github.com/mishmar-io/mishmar/tree/main/examples
