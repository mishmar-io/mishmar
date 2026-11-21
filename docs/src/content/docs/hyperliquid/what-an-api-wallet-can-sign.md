---
title: What a Hyperliquid API wallet can sign
description: What a Hyperliquid API wallet can sign, action by action, and which of those actions Mishmar's half of a split trading key signs.
sidebar:
  order: 3
lastUpdated: 2026-11-20
---

Checked against Hyperliquid's exchange endpoint documentation on 18 November 2026, and against Mishmar v0.5.3 and later.

An API wallet (Hyperliquid also calls it an agent wallet) is a key that a Hyperliquid account approves to sign on its behalf. Most trading bots and AI agents trade with one. Hyperliquid's documentation does not list what an API wallet may and may not sign. This page puts it together, action by action, from that documentation.

## How to read the table

Hyperliquid has two signing schemes. A **user-signed** action is signed by the account's own wallet, in a form a wallet app can show to a person; its request carries the `signatureChainId` and `hyperliquidChain` fields. Every other action is an **L1 action**, which, by this rule, an approved API wallet can sign for the account. Where we know the rule is not settled for an action, the table says "unclear". Where Hyperliquid has made a separate version of an action "that can be signed by an agent", we list it.

The "API wallet" column is our reading of the documentation by that rule. Hyperliquid does not state it action by action. Where it states something outright, we quote it.

The last column says whether Mishmar's half of a split trading key will sign the action.

## Trading

| Action | API wallet can sign it | Mishmar's half signs it (v0.5.3 and later) |
|---|---|---|
| Place an order | Yes | Yes: one plain limit or immediate-or-cancel order, with no builder fee |
| Cancel orders | Yes | No (cancel by client order id only) |
| Cancel orders by client order id | Yes | Yes |
| Schedule a cancel of all orders (dead man's switch) | Yes, by the rule above | No |
| Modify one or several orders | Yes, by the rule above | No |
| Change leverage | Yes | No |
| Add or remove isolated margin | Yes, by the rule above | No |
| Place or cancel a TWAP order | Yes, by the rule above | No |
| Place a trailing stop order | Yes, by the rule above | No |

## Moving money

| Action | API wallet can sign it | Mishmar's half signs it (v0.5.3 and later) |
|---|---|---|
| Withdraw to another chain | No, user-signed | No |
| Send USDC or spot tokens to another Hyperliquid address | No, user-signed | No |
| Send an asset ("Send Asset") | No, user-signed | No |
| Send an asset, agent version ("Agent Send Asset") | Yes. Hyperliquid: "can be signed by an agent. Destination must match the source address." | No |
| Move funds between spot and perps | No, user-signed | No |
| Send to the EVM side with data | No, user-signed | No |
| Deposit into or withdraw from a vault | Unclear. The request has no user-signed fields, but we have not seen the venue accept an API wallet's signature on it, and one developer guide says API wallets cannot sign transfers | No |
| Deposit into or withdraw from an HIP-3 DEX's backstop liquidator | Unclear, as for vaults | No |

## Staking

| Action | API wallet can sign it | Mishmar's half signs it (v0.5.3 and later) |
|---|---|---|
| Deposit into staking | No, user-signed | No |
| Deposit into staking, agent version | Yes. Hyperliquid: "Similar to deposit into staking, but can be signed by an agent." | No |
| Withdraw from staking | No, user-signed | No |
| Delegate or undelegate to a validator | No, user-signed | No |
| Claim rewards | Yes, by the rule above | No |

## The account itself

| Action | API wallet can sign it | Mishmar's half signs it (v0.5.3 and later) |
|---|---|---|
| Approve another API wallet | No, user-signed | No |
| Approve a builder fee | No, user-signed | No |
| Set user abstraction | No, user-signed; an agent version exists | No |
| Enable HIP-3 DEX abstraction | No, user-signed; an agent version exists, which "only works if setting the value from null to true" | No |
| Reserve additional actions (rate limit) | Yes, by the rule above | No |
| Invalidate a pending nonce (noop) | Yes, by the rule above | No |

## Newer actions

Hyperliquid's page also lists outcome actions (split, merge, negate an outcome; merge a question), a validator vote, and an AQAv2 role authorisation. None of their requests carry user-signed fields. We have not worked out what each does for an ordinary trading account, so we do not describe them here. Mishmar's half signs none of them.

## Limits on the API wallet itself

- "An account can have 1 unnamed approved wallet and up to 3 named ones. And additional 2 named agents are allowed per subaccount."
- An approval can be given an expiry: "The expiration can be at most 180 days in the future".
- An API wallet stops working when it is replaced, when it expires, or when "The account that registered the agent no longer has funds."
- Hyperliquid's documentation sets no size, leverage, market or daily-loss limit on an API wallet. What it can sign, it can sign for any amount the account holds.
- For sub-accounts and vaults, Hyperliquid says "signing should be done by the master account" with the vault or sub-account address set; an API wallet approved by the master can sign for its sub-accounts.

## What this means

An API wallet cannot withdraw, cannot send funds to another address, and cannot approve another key. It can do much more than place orders: change leverage, move margin, place any order type at any size, and, in Hyperliquid's own words, deposit the account's funds into staking ("can be signed by an agent"). If a bot's server is taken over, all of that is open to whoever holds the key, with no limit on size.

On Mishmar's hosted service, a Mishmar trading key is split in two and is never whole in one place. On Hyperliquid, Mishmar's half is built to sign only orders and cancels. The last column of the tables above shows which actions that covers.

Sources: Hyperliquid docs: Exchange endpoint, Nonces and API wallets, Signing (hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/…), read 18 November 2026.
