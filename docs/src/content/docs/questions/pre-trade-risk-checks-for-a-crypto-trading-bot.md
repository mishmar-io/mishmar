---
title: "Pre-trade risk checks for a crypto trading bot: what are the options?"
description: Four places a pre-trade check can live (in the bot or its framework, at the exchange, in a risk proxy you run, or in a service that holds half the key), compared on the question that matters, whether the bot can send an order without passing the check, plus what any check has to get right.
sidebar:
  order: 6
lastUpdated: 2026-11-08
---

A pre-trade check can live in four places: in the bot's own code or framework, at the exchange, in a risk proxy you run between the bot and the exchange, or in a service that holds half of the trading key. They differ on one question: can the bot, or whoever controls it, send an order without passing the check? Inside the bot it can. At the exchange it cannot, but the limits are the exchange's, not yours. A proxy closes the gap but holds the whole key in one place. A split key closes it without that.

## The four options

| | Examples | Your limits? | Can the bot go around it? | Approval and kill switch | If it is broken into |
| --- | --- | --- | --- | --- | --- |
| **In the bot** | your own code; Freqtrade's trade caps and Protections; Hummingbot's kill switch | yes, what you coded | yes: same process, holds the key | what you build; a stop flag the bot reads itself | the attacker has the exchange key |
| **At the exchange** | margin and leverage brackets, price bands, rate limits, key permissions, sub-accounts | no: the exchange's | no | no approval; you can revoke the key | not applicable |
| **A risk proxy you run** | your service holds the key and forwards what passes | yes | not if the bot never sees the key | what you build | the attacker has the whole exchange key |
| **A split-key risk layer** | Mishmar | yes: eight kinds of limit | no: neither half signs alone | hold rules approved with a passkey; Pause and Stop | one half signs nothing; a stolen bot half gets only orders your rules allow |

## In the bot or its framework

The cheapest checks, and worth keeping for strategy hygiene. In Freqtrade, `max_open_trades` and `stake_amount` cap how many trades are open and how much each one uses, and Protections (StoplossGuard, MaxDrawdown, LowProfitPairs, CooldownPeriod) pause trading on one pair or all of them after losses ([Freqtrade: configuration](https://www.freqtrade.io/en/stable/configuration/), [Protections](https://www.freqtrade.io/en/stable/plugins/#protections)). Hummingbot's kill switch "Automatically stops the bot when it reaches a certain performance threshold", set as `kill_switch_rate` in `conf_client.yml` ([Hummingbot: kill switch](https://hummingbot.org/client/global-configs/kill-switch/)). CCXT, the library many bots use to reach exchanges, connects and trades; the limits in its manual are each market's own minimums and maximums, not yours ([CCXT](https://github.com/ccxt/ccxt)).

What they share: they run in the bot's process, next to the key. A sizing bug, a loop, a bad prompt or a stolen machine reaches the check and the key together.

## At the exchange

The exchange checks that the account can carry the order: margin, leverage brackets, price bands that reject orders far from the market, and limits on how many orders a key may send. On the key itself you can turn withdrawals off, allow only your IP addresses, trade from a sub-account with a small balance, or on Hyperliquid trade through an API wallet that cannot withdraw. None of these caps what one key may trade: no order-size limit, no exposure or daily-loss limit, no approval. [Is it safe to give a trading bot my exchange API key?](/questions/is-it-safe-to-give-a-trading-bot-my-exchange-api-key/) goes through each restriction and what it leaves open.

## A risk proxy you run

The bot sends orders to a service of yours, which checks them and signs with the exchange key; the bot never sees the key. Done well, this is a real limit. The work is in the details: valuing each order against live marks, counting resting orders and orders in flight, approvals, a record, and keeping the proxy up, because when it is down nothing trades. And the proxy now holds the whole key, so its machine is the one an attacker wants.

## A split-key risk layer

Mishmar does what the proxy does without holding the whole key. The trading key is split in two: one half in the bot's signer on the bot's machine, one with Mishmar. The exchange sees an ordinary API wallet or API key and ordinary signatures, but neither half can sign alone, and Mishmar adds its half only to orders that passed your rules or that you approved. Like a proxy, it has to be up for the bot to trade; your own access to the exchange is never touched, so you can always close from your exchange account. On Binance the halves sign with FROST ([RFC 9591](https://www.rfc-editor.org/rfc/rfc9591)); on Hyperliquid with two-party ECDSA from Coinbase's [cb-mpc](https://github.com/coinbase/cb-mpc). The limits are order size, gross and net exposure, leverage, concentration, drawdown, daily loss and daily realised loss, on the account, a portfolio, an agent or a connected account; [How do I limit what my trading bot can do?](/questions/how-do-i-limit-what-my-trading-bot-can-do/) goes through them.

## What any pre-trade check has to get right

Whichever you choose, check it does these:

- **Values the order before it is sent.** Exposure, leverage and concentration as they would be once the order fills, not as they were before it.
- **Counts what is already working.** Resting orders and orders sent but not yet answered. A check that counts only filled positions lets two orders sent at the same moment through together. Mishmar counts both.
- **Prices market orders it cannot see.** A market order has no price of its own. Mishmar values it at the mark, and blocks it when there is no mark or the mark is stale, instead of letting an order it cannot value through.
- **Lets the bot close.** A limit that blocks reducing a position traps you in it. In Mishmar a reduce-only order that closes a position skips every rule not marked to apply to exits, and still passes when trading is paused.
- **Has a person in the loop for the large ones,** and a kill switch the bot does not control. [How do I add a kill switch and approval step to a trading bot?](/questions/how-do-i-add-a-kill-switch-and-approval-step-to-a-trading-bot/) has both, with code.
- **Keeps a record you can check.** In Mishmar every decision, allowed, held or blocked, is signed and chained to the one before it, and the ledger exports and verifies.

Mishmar works with Hyperliquid perpetuals and Binance USD-M futures, from Python, TypeScript, a CLI or any MCP client. The hosted testnet is free for one agent and one connected account.
