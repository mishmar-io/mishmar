---
title: Mishmar compared with exchange key restrictions, agent wallets and policy engines
description: Where Mishmar sits next to what Hyperliquid and Binance offer on a key, next to agent wallets (Cobo Agentic Wallet, Turnkey, Coinbase AgentKit, Privy, Crossmint) and next to an institutional policy engine (Fireblocks), in each product's own words, with links. As of 6 November 2026.
sidebar:
  order: 4
lastUpdated: 2026-11-08
---

Most of the products a search for "wallet for AI agents" turns up are on-chain wallets with policies on transactions: how much a transfer may move, to which addresses, on which contracts. Mishmar is a different layer. It puts limits on **exchange orders**, on Hyperliquid and Binance accounts, where the question is not "may this transfer leave" but "may this order be placed: at this size, with this leverage, given what is already open and what was lost today". The two can sit side by side. This page says what each product does in its own words, as of 6 November 2026, with the source linked, so you can check.

## In one table

| | Controls | Where the limits apply | Orders on an exchange account | Who holds the key |
| --- | --- | --- | --- | --- |
| **Exchange key restrictions** (Hyperliquid, Binance) | withdrawals off, IP allowlist, sub-accounts | the key | yes, but with no cap on size, exposure or leverage | the bot |
| **Mishmar** | order size, gross and net exposure, leverage, concentration, drawdown, daily loss; hold for approval; stop | every order, before the exchange | yes: Hyperliquid perpetuals, Binance USD-M futures | split: half the bot's signer, half Mishmar |
| **Cobo Agentic Wallet** | per-operation USD cap, rolling windows, review threshold, allowlists | on-chain transactions, 80+ chains | no | MPC, 2-of-2 groups with Cobo |
| **Turnkey** | policies on transaction fields, deny by default | on-chain transactions and raw payloads | no | Turnkey's enclaves |
| **Coinbase AgentKit / CDP wallets** | value and address rules | on-chain transactions | no | Coinbase CDP |
| **Privy** | transfer limits, allowlists, calldata, typed data | on-chain transactions; a Hyperliquid recipe denies withdrawals | signing only; no order conditions | Privy's TEEs |
| **Crossmint agent wallets** | transfer cap, recipients, expiry | on-chain payments | no | Crossmint |
| **Fireblocks policy engine** | allow, block or two-tier approval; amount per transaction or per timeframe | transfers; an exchange is a source or destination | not as order checks | Fireblocks MPC |

## What the exchanges give you on a key

**Hyperliquid.** A bot trades through an API wallet, which the docs say is "only used to sign". An account "can have 1 unnamed approved wallet and up to 3 named ones", each approved for at most 180 days, and the docs are "strongly" against reusing an address once it is deregistered, because "previously signed actions can be replayed once the nonce set is pruned". There is no per-wallet cap on order size, notional, exposure or leverage; the limits in the docs are rate limits and open-order counts. Sources: [Nonces and API wallets](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/nonces-and-api-wallets), [Exchange endpoint](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/exchange-endpoint), [Rate limits and user limits](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/rate-limits-and-user-limits).

**Binance.** A key gets permissions when it is made, and Binance recommends Ed25519 keys and IP restriction: "Unrestricted-IP-Access HMAC API Key(s) won't have any permission other than reading", and "We strongly recommend against enabling API keys for permissions other than reading." Sub-accounts exist "to separate their assets across different accounts for risk management purposes", up to five for a regular user. No page sets a per-key limit on order size or exposure. Sources: [API key security](https://www.binance.com/en/support/faq/detail/360002502072), [API key types](https://developers.binance.com/docs/binance-spot-api-docs/faqs/api_key_types), [Sub-accounts](https://www.binance.com/en/support/faq/360020632811).

**What that leaves.** The balance cannot be withdrawn and the key cannot be used from another machine, but any order the account can carry goes through. [Is it safe to give a trading bot my exchange API key?](/questions/is-it-safe-to-give-a-trading-bot-my-exchange-api-key/) goes through the cases. Mishmar keeps these restrictions (it never has the master wallet, and you leave withdrawals off on Binance) and adds the order checks in front of them.

## Agent wallets

**Cobo Agentic Wallet.** Launched 20 April 2026 as "the world's first MPC-secured wallet purpose-built for AI agents", with the line "Give your agent a Pact, not your private key." It covers "80+ Chains" and its recipes are on-chain (Uniswap, Jupiter, Aave, Polymarket). Policies: "A hard limit on the value of any single operation", rolling windows of "1 hour, 24 hours, 7 days, 30 days", a review threshold after which the agent "receives a `PENDING_APPROVAL` response and can poll", and allowlists per chain. One caveat from its own docs: "if the price feed is unavailable for a token, the USD limit is not evaluated and the operation proceeds." Free during early access, usage-based later. No exchange accounts. Sources: [product page](https://www.cobo.com/agentic-wallet), [launch post](https://www.cobo.com/agentic-wallet/news/cobo-launches-agentic-wallet-how-ai-agents-interact-on-chain), [spend limits](https://www.cobo.com/products/agentic-wallet/manual/security/spend-limits), [pact mechanism](https://www.cobo.com/products/agentic-wallet/manual/security/pact-mechanism).

**Turnkey.** "Give your AI agents non-custodial wallets with scoped permissions"; "Almost all actions on Turnkey are implicitly denied by default." Policies are written over transaction fields (`eth.tx.to`, `eth.tx.value`, function signatures, Solana instructions) and raw-payload metadata; the agent examples trade on a DEX. Pay as you go is "$0.10 / signature" after 25 free a month; Pro is "$99/mo." Sources: [agentic wallets](https://turnkey.com/company-wallets/agentic-wallets), [policy language](https://docs.turnkey.com/concepts/policies/language), [pricing](https://www.turnkey.com/pricing).

**Coinbase AgentKit.** "Coinbase Developer Platform's toolkit for giving AI agents a crypto wallet and onchain interactions." Its README is plain that "AgentKit does not gate transfers behind human approval, enforce spend caps, or allowlist destinations"; CDP wallet policies add value and address rules on signing. Sources: [AgentKit](https://github.com/coinbase/agentkit), [wallet policies](https://docs.cdp.coinbase.com/server-wallets/v2/using-the-wallet-api/policies/overview).

**Privy.** Policies on transfers, allowlists, calldata and typed data, defaulting to deny. Its Hyperliquid recipe uses them to deny the `Withdraw`, `SendAsset` and `ApproveAgent` actions by type, and notes that "Only the master account for a Hyperliquid account can initiate withdrawal attempts"; it has no condition on order size, asset or leverage, and its trading guide says loss and position limits "should be added" by the developer. Sources: [policies](https://docs.privy.io/controls/policies/overview), [Hyperliquid recipe](https://docs.privy.io/recipes/hyperliquid/policies-and-offline-actions).

**Crossmint.** "Non-custodial stablecoin wallets that users fund with a card, for x402, MPP, and onchain payments", with a transfer cap, a recipients allowlist and an expiry on each signer, "checked before the transaction is broadcast onchain". Sources: [agent wallets](https://docs.crossmint.com/solutions/ai-agents/agent-wallets/overview), [signer scopes](https://docs.crossmint.com/wallets/guides/signers/scopes).

## An institutional policy engine

**Fireblocks.** "The Fireblocks Policy Engine is the most robust policy framework in digital assets", with rules that `ALLOW`, `BLOCK` or require two-tier approval, amounts per transaction or per timeframe in USD, EUR or the native asset, first match wins. An exchange appears in the rules as the source or destination of a transfer, not as a venue whose orders are checked. Sources: [policy engine](https://www.fireblocks.com/platforms/policy-engine/), [transaction authorization policy](https://developers.fireblocks.com/reference/configure-transaction-authorization-policy).

## The regulated analogue

In US equities the check Mishmar makes is required by rule. SEC Rule 15c3-5, the market access rule, requires controls "reasonably designed to systematically limit the financial exposure", including to "Prevent the entry of orders that exceed appropriate pre-set credit or capital thresholds" and to "Prevent the entry of erroneous orders, by rejecting orders that exceed appropriate price or size parameters". The SEC's 2010 announcement said broker-dealers must have "risk controls in place before providing their customers with access to the market". Sources: [17 CFR 240.15c3-5](https://www.law.cornell.edu/cfr/text/17/240.15c3-5), [SEC press release 2010-210](https://www.sec.gov/news/press/2010/2010-210.htm). A bot trading a crypto exchange account has no broker in front of it; Mishmar is that check.

## Which one, then

- The agent **pays, swaps or moves tokens on-chain**: an agent wallet with policies, from the list above.
- The bot **trades perpetuals on an exchange account**, and you want limits on what it trades, a person on large orders and a stop: Mishmar.
- Both: an agent wallet for the on-chain side and Mishmar for the exchange side. Nothing in either depends on the other.

For MCP servers that trade on Hyperliquid and Binance (Binance's own, Senpi, Coinrule and the community servers), see [Is there an MCP server for trading on Hyperliquid or Binance with limits?](/questions/is-there-an-mcp-server-for-trading-on-hyperliquid-or-binance-with-limits/)

Mishmar's client side is open source under Apache-2.0 ([github.com/mishmar-io/mishmar](https://github.com/mishmar-io/mishmar)); the hosted testnet is free for one agent and one trading account. Found an error on this page? [Edit it on GitHub](https://github.com/mishmar-io/mishmar/edit/main/docs/src/content/docs/questions/mishmar-compared.md).
