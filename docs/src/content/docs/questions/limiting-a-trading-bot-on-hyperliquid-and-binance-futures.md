---
title: "Limiting a trading bot on Hyperliquid and Binance futures: what the venue stops and what it leaves to you"
description: If a bot or an AI agent trades your account, what Hyperliquid and Binance USDⓈ-M futures stop by themselves (API wallets, sub-accounts, leverage, Binance's agent connection, API keys on a sub-account), what their docs do not offer (a limit per key on order size, position or loss), and what is left to you. Checked against the venues' own docs on 16 November 2026.
sidebar:
  order: 8
lastUpdated: 2026-11-19
---

If a bot or an AI agent trades your account, three things can go wrong: it can move money out, it can place an order far bigger than you meant, and it can keep losing all day. Here is what each venue stops by itself, and what it leaves to you. Checked against the venues' own docs on 16 November 2026.

## Hyperliquid

- *Give the bot an API wallet, not your main key.* Hyperliquid's docs: "[A master account can approve API wallets to sign on behalf of the master account or any of the sub-accounts.](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/nonces-and-api-wallets)" An account can have "[1 unnamed approved wallet and up to 3 named ones](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/exchange-endpoint)", and an approval can carry an expiry "[at most 180 days in the future](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/exchange-endpoint)".
- *Use a sub-account to wall off the money.* "[Up to 10 sub-accounts can be created after reaching $100,000 in volume.](https://hyperliquid.gitbook.io/hyperliquid-docs/trading/sub-accounts)"
- *Set leverage yourself.* "[Leverage can be set by a user to any integer between 1 and the max leverage.](https://hyperliquid.gitbook.io/hyperliquid-docs/trading/margining)" Note that "[Leverage is only checked upon opening a position. Afterwards, the user is responsible for monitoring the leverage usage](https://hyperliquid.gitbook.io/hyperliquid-docs/trading/margining)".
- *What the docs do not offer:* a limit per API wallet on order size, position size, daily loss or which assets it may trade.

## Binance USDⓈ-M futures

- *If the bot is an AI app, Binance has its own agent connection.* "[Your agent trades inside a dedicated sub-account, isolated from your main account.](https://developers.binance.com/en/docs/agent-native/mcp-server/agentic)" "[There is no withdrawal scope.](https://developers.binance.com/en/docs/agent-native/mcp-server/agentic)" An "[Emergency stop](https://developers.binance.com/en/docs/agent-native/mcp-server/agentic)" will "[in a single step, disconnect all connected agents and cancel all spot, margin, and futures positions and orders in this Agentic account](https://developers.binance.com/en/docs/agent-native/mcp-server/agentic)". The same page lists every trade and transfer as "[Confirmed by you first](https://developers.binance.com/en/docs/agent-native/mcp-server/agentic)", so it suits an assistant you sit next to, not a bot that runs while you sleep.
- *If the bot uses an API key, put it on a sub-account.* A sub-account has its own keys ("[Each sub-account can create up to 30 API Keys](https://binance.com/en/support/faq/detail/360020632811)") and its own balance.
- *Know what does not apply to bots.* "[Please note that orders placed via API won't be affected by the Price Protection function.](https://binance.com/en/support/faq/detail/2b9dc811ce7340469357867122b975dc)"
- *What the docs do not offer:* a limit per key on order size, position or loss.

## So what is left to you

On both venues the money can be walled off, but the size of an order and the loss in a day are whatever the bot decides. Most people write those checks into the bot. That works until the bug is in the bot.

The other way is to check each order outside the bot, before it is signed. That is what Mishmar does: the bot's trading key is split in two, and the second half signs only if the order is inside the limits you set (order size, gross exposure, leverage, drawdown, daily loss, [among eight](/questions/how-do-i-limit-what-my-trading-bot-can-do/)), and each limit can block the order or hold it for your approval. It works on Hyperliquid perps and Binance USDⓈ-M futures and is free for one trading agent and one exchange connection. A stolen bot key can still trade inside your limits; it cannot trade past them.
