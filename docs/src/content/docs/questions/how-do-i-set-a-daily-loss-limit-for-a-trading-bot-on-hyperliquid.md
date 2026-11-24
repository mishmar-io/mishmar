---
title: How do I set a daily loss limit for a trading bot on Hyperliquid?
description: Hyperliquid has no daily loss limit that you can put on a bot's API wallet, so the limit has to live somewhere else. How to set it outside the bot, as a Daily loss rule with Block in Mishmar's console, what the rule does and does not do, and what your bot sees when an order is refused.
sidebar:
  order: 10
lastUpdated: 2026-11-22
---

Hyperliquid has no daily loss limit that you can put on a bot's API wallet. An API wallet can place any order the account's margin allows ([what a Hyperliquid API wallet can sign](/hyperliquid/what-an-api-wallet-can-sign/)). So the limit has to live somewhere else. There are two places.

**Inside the bot.** Some bots have a setting for it, and the bot stops itself when the day's loss passes the number. This costs nothing and you should keep it. Its weakness is that the limit runs in the same code as the thing it limits. A bug, a bad update, or an AI agent that rewrites its own plan can pass it without anyone noticing.

**Outside the bot, with Mishmar.** You set the limit on the agent in Mishmar's console. Mishmar checks every order the bot sends against it, and refuses an order the limit stops. The bot's key cannot change or turn off the rule. Only an owner or admin of your Mishmar account can, in the console or with an API token they made. Don't give the bot such a token.

## What the rule does

This describes the rule as set below: the **Daily loss** metric, with **Block**.

- Once the agent's loss today passes your limit, Mishmar refuses the agent's new orders.
- An order that only closes part or all of this agent's own position still goes through, unless you turned on "Applies to exits". This rule never stops a cancel.
- It closes nothing. A position that is already open can go on losing after the limit is passed. Keep the logic that closes positions in your bot.
- The loss is the agent's whole result for the day: its closed trades, fees, funding, and its open positions at current prices. It is measured again on every order. If prices recover and the day's loss falls back under the limit, the agent's orders pass again the same day.
- Trades you make by hand at Hyperliquid do not count toward it.
- The day starts around 00:00 UTC, not at your local midnight.

## Set it

You need a Mishmar account with [a Hyperliquid connection](/hyperliquid/connect-an-account/), [an agent that has a trading key](/hyperliquid/add-an-agent/), and a passkey or wallet under your profile.

When you add a trade agent, the **Daily loss limit ($)** field in Add agent makes this rule for you, with these settings.

1. In the console, open **Rules** (under **Policy**) and press **Add rule**.
2. **Name:** anything you will recognise, for example "Daily loss 250".
3. **Scope:** choose **Agent**, then pick the agent your bot uses. The dialog says: "The agent's own orders, on every account it trades."
4. **Metric:** choose one of two.
   - **Daily loss**: "Realised plus unrealised loss today, a UTC day". An open losing position counts.
   - **Daily realized loss**: "Realised loss today, a UTC day, with fees and funding". Open positions do not count, so a bot that holds a losing position open does not reach it.

   If you are not sure, use **Daily loss**.

5. **Threshold:** the amount in USD, for example 250.
6. **On breach:** leave **Block** selected ("Rejects the proposal."). The other two choices do something else: with **Require approval**, a person must approve each such order first, which suits someone at a desk and not a bot that runs while you sleep. **Allow** records the breach and does not stop the order.
7. Leave **Enabled** on. Leave **Applies to exits** off: when it is off, the rule does not judge orders that close a position.
8. Check the line under **Reads as**. It should say: "Block when \<your agent>'s daily loss (UTC day) exceeds $250". Press **Add rule**. If you have not used your passkey or wallet in the last ten minutes, the console asks for it first.

## What your bot sees

When an order is refused, Mishmar's answer lists each rule that was checked, the value it saw and the threshold. Your bot can read that and stop opening positions for the day. See [what an agent sees when a limit says no](/questions/how-do-i-add-a-kill-switch-and-approval-step-to-a-trading-bot/).

## What it is not

It is not a stop-loss on a position, and it does not limit what an open position can lose. For what Hyperliquid and Binance stop by themselves, see [what the venues stop a bot doing, and what they leave to you](/questions/limiting-a-trading-bot-on-hyperliquid-and-binance-futures/). Through Mishmar, an agent sends limit and market orders only. It cannot place a stop or trigger order. If you rely on a stop resting at Hyperliquid, place it there yourself.
