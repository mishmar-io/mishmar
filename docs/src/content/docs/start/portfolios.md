---
title: Portfolios
description: What a portfolio is in Mishmar (a group of agents that share rules and allocated capital), how to add one, give it capital, move agents into it, and stop or delete it.
sidebar:
  order: 3
lastUpdated: 2026-11-04
---

A **portfolio** is a group of agents that share limits. Every agent belongs to exactly one portfolio. A portfolio rule looks at all of its agents' positions together, so three agents in one portfolio can share one exposure cap, one daily loss limit, or one leverage limit.

A portfolio can also have **allocated capital**: the money you set aside for it. Leverage and drawdown rules on the portfolio, and on each agent in it, are measured against that capital. The capital is not a cap on its own. To cap exposure, add a gross exposure rule.

## Where portfolios fit

Mishmar checks every order against the rules at four scopes. An order passes only if every rule at every scope passes.

```text
Account                      rules on all trading
├── Portfolio "Perp sleeve"  rules on its agents together, capital $50,000
│   ├── Agent "HL trend bot"   rules on this agent alone
│   └── Agent "HL market maker"
└── Portfolio "Main" (default)
    └── Agent "Binance basis"

Connected account            rules on everything through one venue account
```

An order from **HL trend bot** on a Hyperliquid connected account is checked against the account's rules, Perp sleeve's rules, HL trend bot's own rules and the connected account's rules.

Positions that no agent placed (trades you make by hand, or fills from before Mishmar) belong to no portfolio. Account and connected account rules see them; portfolio and agent rules do not.

## The default portfolio

Every account starts with one portfolio, **Main**, which is the default. An agent added without a portfolio joins the default. The default is an ordinary portfolio otherwise: you can rename it, give it capital, and stop it. It cannot be deleted. To delete it, make another portfolio the default first (**Make default** in that portfolio's **⋯** menu).

## Add a portfolio

1. Open **Portfolios** and click **Add portfolio**.
2. Fill in:
   - **Name**: what you call it, for example "Perp sleeve".
   - **Allocated capital** (optional): the dollars allocated to it. Leave it empty if you will not use leverage or drawdown rules on it or its agents.
   - **Description** (optional): what it holds.
3. Click **Add portfolio**.

To give it rules, open the portfolio, go to its **Rules** tab and click **Add rule**. Rules added there apply to the portfolio's agents together, on top of the account's rules and each agent's own.

:::caution[Leverage and drawdown need capital]
A leverage or drawdown rule on a portfolio, or on an agent in it, is measured against the portfolio's capital. While the portfolio has no capital, Mishmar refuses to turn such a rule on: you can save it turned off and turn it on once the portfolio has capital. Mishmar also refuses to remove the capital while such a rule is on.
:::

## Example: three agents, one budget of risk

You run three agents on perps and want them, together, to stay under 3x leverage on $50,000 and to open nothing new for the rest of the day after a $1,500 loss.

1. Add a portfolio "Perp sleeve" with capital `50000`.
2. On its **Rules** tab, add a leverage rule with a limit of `3` and a daily loss rule with a limit of `1500`.
3. Put the three agents in it, when you add them or by editing each one.

Together the agents can now hold up to $150,000 of gross exposure. An order that would take them past it is blocked, whichever agent sends it, or waits for your approval if you gave the rule that outcome.

## Move an agent to another portfolio

Open the agent, click **Edit**, pick the new **Portfolio** and save. From its next order on, the new portfolio's rules apply to it, and its positions count toward the new portfolio.

If the agent has a leverage or drawdown rule of its own that is turned on, the new portfolio needs allocated capital: the move is refused otherwise, and the message names the rules. Give the portfolio capital first, or turn those rules off.

## Stop a portfolio

In the portfolio's **⋯** menu, **Stop** blocks every order from its agents, exits included, until you **Resume** it. Stopping closes nothing: open orders and positions stay at the venue.

## Delete a portfolio

In the portfolio's **⋯** menu, choose **Delete**. A portfolio that still holds agents cannot be deleted: move or delete them first. Its past decisions are kept.

## Check it worked

- **Portfolios** lists the portfolio with its agent count, its capital, its gross exposure and its leverage (gross exposure ÷ capital).
- An order from one of its agents shows the portfolio's rules among its evaluations on the decision's page.
