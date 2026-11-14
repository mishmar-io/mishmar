---
title: Where rules apply
description: The four places a Mishmar rule can sit (the whole account, a connection, a connected account, an agent), what each one covers and measures, how to give an agent capital for its leverage and drawdown rules, and how to put one limit on several agents.
sidebar:
  order: 3
lastUpdated: 2026-11-13
---

Every rule sits at one of four scopes, and every rule that covers an order runs on it. The order passes only if all of them pass.

```text
Whole account                       every agent on every connected account
├── Connection "Main" (Binance)     one login at the venue and everything under it
│   ├── Spot                        a connected account: one pool of money
│   ├── USDⓈ-M Futures              ← agent "Reversion" trades here
│   └── Connection "Carry"          a sub-account's connection, nested under its main login's
│       └── USDⓈ-M Futures
└── Connection "Main" (Hyperliquid)
    └── Perps                       ← agents "Trend follower" and "Market maker" trade here

Agent                               one agent's own orders, on every account it trades
```

An order from **Trend follower** on Hyperliquid Perps is checked against the whole account's rules, the Hyperliquid connection's, the Perps account's and Trend follower's own. An order from **Reversion** is checked against the whole account's, the Binance connection's, its USDⓈ-M Futures account's and Reversion's own. A rule on **Carry** does not cover Reversion's orders: a connection covers the connections nested under it, not the one above.

| Scope | What it covers | Leverage and drawdown are measured against |
| --- | --- | --- |
| **Whole account** | every agent on every connected account | the value of every connected account |
| **Connection** | one login at the venue: its accounts, and those of the sub-accounts' connections nested under it | the summed value of those accounts |
| **Connected account** | one pool of money, whichever agent places the order | that account's value |
| **Agent** | the agent's own positions, on every account it trades | the capital you allocate to the agent |

Positions no agent placed (trades you make by hand, or fills from before Mishmar) count toward the whole account, the connection and the connected account they are on. Agent rules do not see them.

Drawdown on the whole account, a connection or a connected account is the fall of that value from its own high, with the money you move in or out taken off, so a transfer is never read as a loss.

The value is that of the accounts agents trade on. An account Mishmar only reads, which today means Spot, is not counted: on a Binance login a connection's rule measures the USDⓈ-M Futures account, not Spot beside it, and a move from Futures to Spot is money leaving what the rule measures.

## Give an agent capital

A leverage or drawdown rule on one agent needs a number to measure against: the agent's **capital**, the dollars you set aside for it. It is a number you set, not money moved anywhere, and it caps nothing on its own. To cap exposure, add a gross exposure rule.

- When you add a trade agent, fill in **Capital (optional)** in the dialog.
- Later, open the agent's page and click **Set capital** (or **Edit**, once it has some) on its **Capital** card.
- Adding a leverage or drawdown rule on an agent without capital, the rule's dialog asks for the agent's capital and saves it with the rule.

:::caution[Leverage and drawdown on an agent need capital]
While an agent has no capital, Mishmar refuses to turn on a leverage or drawdown rule on it: you can save the rule turned off, and turn it on once the agent has capital. Mishmar also refuses to take the capital away while such a rule is on, and the message names the rules. Rules at the other three scopes measure what the accounts hold and need nothing set.
:::

## One limit for several agents

A limit that several agents share goes on what they trade on: their connected account, its connection, or the whole account.

Say three agents trade Hyperliquid perps from one account holding $50,000, and together they should stay under 3x leverage and open nothing new for the rest of the day after a $1,500 loss:

1. Open **Connected accounts**, open the Perps account, and go to its **Rules** tab.
2. Add a leverage rule with a limit of `3` and a daily loss rule with a limit of `1500`.

Leverage there is gross exposure over the account's value, so together the agents can hold up to about $150,000 of gross exposure. An order that would take the account past it is blocked, whichever agent sends it, or waits for your approval if you gave the rule that outcome. Positions you opened by hand count toward it too.

To give each agent its own share on top, add a gross exposure rule on each agent, or give each one capital and a leverage rule of its own. When the agents trade on several accounts of one login, put the shared rule on the connection instead: it covers all of them.

A limit on a few agents apart from the others on the same account has no scope of its own. Give those agents an account of their own at the venue, connect it, and put the rule on it.

## Stop a group of agents

**Stop all trading** and **Pause all trading** are on the whole account, a connected account and an agent. Stopping a connected account refuses every order from every agent on it, until you resume it. There is none on a connection: stop each of its accounts, or the whole account.

## Check it worked

- On a decision's page, each rule that covered the order is listed with its scope and what it measured.
- An agent's page shows its **Capital**, its leverage now (gross exposure ÷ capital) and the rules measured against it.

## If you used portfolios

Until November 2026 a portfolio was a named group of agents with capital, a stop and shared rules. Portfolios are gone, and the upgrade moved what they held. With one portfolio, each agent took the portfolio's capital, so its own rules measure what they measured, and the portfolio's rules moved to the whole account: a leverage rule as the gross exposure cap it came to (limit × capital), a drawdown rule measured on the accounts' value from then on. With several, each portfolio rule became a rule on each of its agents, with a dollar limit and the capital divided among them (an order size cap copied whole), so nothing got looser. A stopped portfolio left its agents stopped.
