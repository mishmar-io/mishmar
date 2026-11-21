---
title: What Mishmar keeps of your accounts
description: Mishmar reads every account you connect every minute, with or without an agent, and keeps its balances, positions, value over time and transactions, each trade marked with who placed it. How often, how far back, how long, who can read it and how every agent read is logged, and what happens while a free Mishmar account sleeps.
sidebar:
  order: 2
lastUpdated: 2026-11-18
---

Once you connect an account ([on Hyperliquid](/hyperliquid/connect-an-account/) or [on Binance](/binance/connect-an-account/)), Mishmar reads it on its own and keeps what it reads. You see it in the console, on the account's page, and the agents you give the account read it with their Mishmar API key. None of it needs an agent or a trading key: Mishmar reads a Hyperliquid account by its address and a Binance account with a read-only key.

## What it keeps

| Kind | What it is | On the account's page |
| --- | --- | --- |
| Balances | every asset the account holds, valued in USD at the price shown | **Balances** |
| Positions | the venue's own net position in each market (a symbol with its market kind), whoever placed the orders behind it: size, entry, mark, notional, unrealised PnL, leverage | **Positions**, under **At the venue** |
| Value | the account's value in USD over time: its balances plus the unrealised PnL of its positions. Deposits, withdrawals and transfers to or from another account move it | **Value** |
| Transactions | trades, deposits, withdrawals, transfers to and from other accounts, funding, fees, liquidations and rebates, as the venue reports them | **Transactions** |

An account is one pool of money at a venue, and what holds it there is its **connection**. Making a connection brings in each of its accounts: a Binance connection has a **Spot** and a **USDⓈ-M Futures** account, a Hyperliquid address has **Perps** and **Spot**. Every balance, position and transaction belongs to one account, and a connection's value is the sum of its accounts'. A move between two accounts of one connection shows on both, out of one and into the other, each naming the other: it moves each account's value by the amount and the connection's by nothing. An asset Mishmar has no price for is listed with its amount and no value, and named, rather than counted as nothing. A sub-account at the venue is a connection of its own: a main connection's value does not include its sub-accounts'. Agents trade on Hyperliquid Perps and Binance USDⓈ-M Futures. Hyperliquid Spot, Binance Spot and both accounts of a Hyperliquid sub-account are read like any other, but agents can't trade on them yet, and each says why.

![A Hyperliquid Perps account's Balances tab: each asset with its amount, price, value in USD and share, as of the last read, then the unrealized PnL of its open positions and the account's value under them](/screens/concepts/what-mishmar-keeps-of-your-accounts/balances.png)

![The Value tab: the account's value in USD now and its change over the last 7 days, charted one point per hour, with Minute, Hour and Day to choose from](/screens/concepts/what-mishmar-keeps-of-your-accounts/value.png)

Each trade says who placed it: an agent through Mishmar, named, with the decision that allowed it; or **Outside Mishmar**, meaning you by hand, another program, or the venue itself.

![The Transactions tab, newest first: each trade's time, what was bought or sold at what price, its value and fee, and who placed it, an agent with a link to its decision or Outside Mishmar](/screens/concepts/what-mishmar-keeps-of-your-accounts/transactions.png)

The positions here are the venue's, for the account as a whole. Each agent's own book, built from its own fills, is on its page under **Agents**; that is what rules on an agent measure. Rules on the account, its connection or the whole Mishmar account see every position on the account, the ones no agent placed included ([Where rules apply](/start/where-rules-apply/)). The record of Mishmar's decisions on orders is separate again, under **Decisions**.

![The Positions tab, At the venue: the venue's net position in each market, a symbol with its market kind, with its side, size, entry, mark, notional, unrealised PnL and leverage](/screens/concepts/what-mishmar-keeps-of-your-accounts/positions.png)

## How often, and how far back

- **Balances, positions and value** are read every minute, and each reading is kept, so the value has a point for every minute.
- **Transactions** are read at most every five minutes.
- **The first time**, transactions go back 90 days on Hyperliquid, and a week on Binance, which answers no further back.
- Mishmar reads every account whose connection it has verified, whether or not an agent uses it, and whether trading on it is running, paused or stopped. An account stays **Pending**, and is not read, until its connection's checks pass.
- On Binance it reads only while the read-only key can do nothing but read, which it checks every hour.

Mishmar reads a connection's accounts together, so the connection's page says how current each kind is, under **Data** on its **Setup** tab: **Up to date**, **Catching up**, **Reading its history** (with how far it has got), or **Failing**, with the time it is as of. While reads of a kind fail, the connection's page and each of its accounts' pages say so at the top, with the reason.

![The Data panel on a connection's Setup tab: balances, positions, value and transactions each Up to date, with how long ago they were read](/screens/concepts/what-mishmar-keeps-of-your-accounts/data.png)

![The Data panel of a connection made a moment ago: balances, positions and value up to date, read seconds ago, and transactions still Reading its history, 47% of the way, with no time as of yet](/screens/concepts/what-mishmar-keeps-of-your-accounts/data-reading-history.png)

## How long it is kept

The free plan keeps 90 days of readings and transactions.

## Who can read it

- **People on the Mishmar account**, in the console.
- **The agents you give the account to.** An agent reads each account it was given whole: balances, positions, value and every transaction, your own trades and other agents' included. On an agent's reads, each trade also says whether that agent placed it (`own`). An agent reads only the accounts it was given; any other is refused with `Agent … was not given this connected account`.

You give an agent an account when you add it, or later on the agent's page: under **Overview**, **Accounts** has **Give access**, and **Remove** on each row. A trade agent gets the account it trades on with its trading key, and an account can't be taken from it while it holds a trading key there (`Revoke its trading key first`).

Every read an agent makes is written down before the agent gets its answer. The account's **Access** tab lists who read it (**Time**, **Agent**, **Read**, **Range**, **With key**), and the agent's **Access** tab lists what it read. What an agent has read can't be taken back: to stop it reading, remove its access, or revoke its Mishmar API key on its **Keys** tab.

![An account's Access tab, Who read it: each read by two agents, newest first, with its time, the agent, what it read, the range and the key it used](/screens/concepts/what-mishmar-keeps-of-your-accounts/access.png)

## How an agent reads it

An agent reads with its Mishmar API key alone: no signer and no trading key. Each read is a request signed with that key, like every agent call. There are seven:

| Read | `GET` | Takes |
| --- | --- | --- |
| Who the key belongs to: the agent, its Mishmar account and its permission | `/v1/agent/me` | |
| The accounts the agent was given | `/v1/agent/connected-accounts` | |
| One of them, with no secret | `/v1/agent/connected-accounts/{id}` | |
| Its balances | `…/{id}/balances` | `at`, a past moment; absent means now |
| Its positions at the venue | `…/{id}/positions` | `at` |
| Its transactions, newest first | `…/{id}/transactions` | `from`, `to`, `kind`, `limit`, `cursor` |
| Its value over time | `…/{id}/equity` | `from`, `to`, `resolution` |

The Python and TypeScript SDKs, the `mishmar` CLI and the MCP server make these calls for you. [Add a read agent](/start/add-a-read-agent/) has the setup and the names to use in each.

## While a free Mishmar account sleeps

On the free plan, a Mishmar account that has been quiet for a few hours (three, today) is stopped, as long as it holds no position and no approval waits, and it starts again on the next call that needs it, which can take up to a minute. Pro is always on.

Reading goes on while it is stopped. Mishmar keeps reading its connections' accounts on the same timer, so nothing it shows is stale when it wakes. It answers an agent's reads of this data straight away, without waking the Mishmar account, and those reads don't keep it awake.

A free Mishmar account not used for 90 days is removed, with its agents, keys, rules, ledger and what Mishmar kept of its accounts, and its owner and admins are emailed 7, 2 and 1 days before. On testnet it is 14 days, with one email the day before. An agent reading its accounts counts as use: a Mishmar account whose agents still read them is not removed.
