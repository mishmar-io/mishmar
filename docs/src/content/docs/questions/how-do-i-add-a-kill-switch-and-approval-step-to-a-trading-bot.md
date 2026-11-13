---
title: How do I add a kill switch and approval step to a trading bot?
description: Put both outside the bot, where every order has to pass before it is signed. Pause lets a bot close positions and nothing else, Stop refuses everything but cancels, and an approval rule holds large orders for a person with a passkey. What each does, what the bot sees, and the code to handle it.
sidebar:
  order: 5
lastUpdated: 2026-11-08
---

Put both outside the bot, at the point every order has to pass before it is signed. A kill switch written into the bot is a flag the bot can ignore, and an approval step written into the bot is a prompt the bot can skip: the bug or the bad prompt that sends the wrong order is the same one that skips the check. With Mishmar the kill switch and the approval step sit with the half of the trading key the bot does not have, so the bot only has to handle the answer.

## The kill switch: Pause and Stop

Two levels, set from the console, each lifted with **Resume trading**:

| Level | What still passes | Where you can set it | Use it when |
| --- | --- | --- | --- |
| **Pause all trading** | cancels, and reduce-only orders that close a position | the account, an agent, a connected account | you want the bot to wind down: it can close what it holds, and open nothing new |
| **Stop all trading** | cancels only | the account, an agent, a connected account | something is wrong now: nothing new goes out, closing orders included |

A stop outranks a pause. Neither closes anything for you: Mishmar holds only its half of each trading key and never trades on its own, so open positions and resting orders stay at the exchange. Under a stop, the bot can still pull its resting orders, and you close positions from your own exchange account (your master wallet on Hyperliquid, your login on Binance), which Mishmar never touches. If you want the bot to do the closing, pause instead.

What the bot sees: an order sent while paused or stopped comes back as a decision with the verdict `block`, and its `reason` names the switch. Nothing reaches the exchange.

Two more ways to stop, outside the console: stop the bot's signer process (without its half, nothing can be signed at all), or revoke the agent's trading key in the console, after which even orders your rules allow stop.

## The approval step: a rule that holds

An approval step is a rule whose outcome is **hold for approval** instead of block. Any of the [eight kinds of limit](/questions/how-do-i-limit-what-my-trading-bot-can-do/) can hold, and the same limit can appear twice with two thresholds:

| Rule | Threshold | Outcome |
| --- | --- | --- |
| Order size | over $500 | hold for approval |
| Order size | over $5,000 | block |
| Daily loss | over $300 | hold for approval |

What happens to a held order:

1. The bot gets a decision with the verdict `require_approval`, every rule's evaluation and an approval id. Nothing is signed yet.
2. The order shows in the console and the account's owner gets an email.
3. You approve or reject it in the console, and approving takes a passkey, so an injected "approve it" in a model's context goes nowhere.
4. At the moment you approve, the rules run again against the account as it is then. If a rule now blocks it, or a pause or stop is on, the approval is refused and the order stays held. If the numbers moved enough to change a rule's outcome since you looked, you are asked to confirm again.
5. Approved, the order is signed by both halves and sent. Rejected or expired, it is dropped.

A held order expires at the bot's own deadline for it or after 30 minutes, whichever comes first. The SDKs set that deadline to 60 seconds unless you say otherwise, so an order that may wait for a person needs a longer one: `valid_for` in Python, `validForSeconds` in TypeScript, and in the MCP server's `propose_order`.

## In the bot

The bot handles three verdicts. In Python, with the SDK's `Signer` (TypeScript is the same in camelCase):

```python
from mishmar import Signer

with Signer() as s:  # runs the agent's signer from ~/.mishmar/agent.json; this process holds no key
    d = s.order("ta_…", "BTC", "buy", "0.05", price="64000", valid_for=900)

    if d["verdict"] == "require_approval":
        # Held for a person. Wait up to 15 minutes, or carry on and read it later with s.decision(d["id"]).
        d = s.wait(d["id"], timeout=900, interval=2)
        if (d.get("approval") or {}).get("status") != "approved":
            print("not approved:", (d.get("approval") or {}).get("status"))  # rejected, expired, or still waiting

    elif d["verdict"] == "block":
        # A rule, a pause or a stop refused it. Do not resend the same order.
        print("blocked:", d.get("reason"))
        for e in d.get("evaluations", []):
            if not e["passed"]:
                print(f"  {e['ruleName']}: {e['observed']} vs {e['threshold']} {e['unit']}")

    if d.get("venueAck"):
        print("exchange:", d["venueAck"]["status"])
```

A language-model agent gets the same through MCP: `propose_order` returns the decision, and `wait_for_decision` follows a held one. There is no tool to approve, to change a rule or to lift a stop. [Use Mishmar from Claude, Cursor or any MCP client](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) has the setup.

## Why not in the bot

- **The bot holds the key.** If the bot can sign, every check inside it is advisory: one code path that forgets the check, and the order goes out. With Mishmar the bot's signer holds half the trading key, and the other half signs only what passed.
- **The switch has to work when the bot does not.** A bot stuck in a loop, or a model that decided it is not done, does not read its own kill flag. Pause and Stop do not ask the bot.
- **The approval has to be yours.** An approval the bot asks for in its own chat, or a model asks for in its own context, can be answered by whatever has taken over the bot. A passkey in the console cannot.

## Where the idea comes from

In regulated markets the kill switch belongs to the firm that sends the orders, not to the strategy. The EU's rules for algorithmic trading require that "An investment firm shall be able to cancel immediately, as an emergency measure, any or all of its unexecuted orders submitted to any or all trading venues" ([Regulation (EU) 2017/589, Article 12](https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=CELEX:32017R0589)). FIA's 2024 best practices for automated trading describe a kill switch as a control that immediately disables all trading for a participant, and place it as "a final backstop for the pre-trade risk functionality" ([FIA, July 2024](https://fia.org/sites/default/files/2024-07/FIA_WP_AUTOMATED%20TRADING%20RISK%20CONTROLS_FINAL_0.pdf)). Mishmar's Stop is the disabling kind: nothing new is signed, and pulling what rests is left to cancels, which still pass.

## Set it up

1. [Install the mishmar CLI](/start/install-the-cli/) and add an agent [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/).
2. In the console, add a hold-for-approval rule next to your block rules, on the agent, the connected account it trades on or the whole account.
3. Give orders that may be held a deadline long enough for you to answer, and handle all three verdicts in the bot.
4. Try Pause and Stop once on the hosted testnet before you need them, and watch what the bot does.

The hosted testnet is free for one agent and one venue connection, on Hyperliquid testnet or Binance demo trading.
