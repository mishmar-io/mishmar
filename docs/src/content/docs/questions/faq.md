---
title: Frequently asked questions
description: Short answers about Mishmar, the risk management layer for trading bots and AI agents. What it is, which exchanges, who holds the keys, what happens when something fails, what it costs, and what is open source.
sidebar:
  order: 9
lastUpdated: 2026-11-06
head:
  - tag: script
    attrs:
      type: application/ld+json
    content: |
      {"@context":"https://schema.org","@type":"FAQPage","mainEntity":[
      {"@type":"Question","name":"What is Mishmar?","acceptedAnswer":{"@type":"Answer","text":"Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone. The trading key is split in two, one half with the bot's signer and one with Mishmar, so neither can sign an order by itself."}},
      {"@type":"Question","name":"Which exchanges does Mishmar work with?","acceptedAnswer":{"@type":"Answer","text":"Hyperliquid (perpetuals) and Binance (USD-margined futures) today, on the hosted testnet: Hyperliquid testnet and Binance demo trading. Real money on both is coming; every user is emailed when it opens."}},
      {"@type":"Question","name":"Does Mishmar hold my funds or my keys?","acceptedAnswer":{"@type":"Answer","text":"No. Your funds stay at the exchange. Mishmar holds one half of each trading key, which signs nothing on its own, and never touches your master wallet or your exchange login."}},
      {"@type":"Question","name":"What happens if Mishmar is down?","acceptedAnswer":{"@type":"Answer","text":"The bot cannot place or cancel orders through its trading key, because every signature needs both halves. Your own access to the exchange is untouched: close positions from your master wallet or your exchange account."}},
      {"@type":"Question","name":"What happens if the bot's machine is hacked?","acceptedAnswer":{"@type":"Answer","text":"The attacker holds the bot's half of the key, which signs nothing alone. They can propose orders as the bot, but only orders your rules allow get Mishmar's half. Revoke the trading key in the console and nothing more is signed. A trading key cannot withdraw on Hyperliquid, and on Binance you leave withdrawals off."}},
      {"@type":"Question","name":"Can I use Mishmar with a language-model agent?","acceptedAnswer":{"@type":"Answer","text":"Yes. mishmar mcp is an MCP server with tools to propose orders and cancels, follow a held decision and read positions, PnL, approvals and the exchange's rules. The model never sees a key, and every order it proposes passes your rules. Python and TypeScript agents use the SDK's Signer instead."}},
      {"@type":"Question","name":"Which languages are supported?","acceptedAnswer":{"@type":"Answer","text":"Python (mishmar on PyPI), TypeScript (mishmar on npm), Go (the packages in the public repo), a CLI, and any MCP client. The SDKs run the signer as a child process, so the bot's own process holds no key."}},
      {"@type":"Question","name":"Is Mishmar open source?","acceptedAnswer":{"@type":"Answer","text":"The client side is: the API contract, the signer, both signing schemes, the CLI, the MCP server and the SDKs, under Apache-2.0 at github.com/mishmar-io/mishmar. The engine that enforces the rules is a separate, private repo."}},
      {"@type":"Question","name":"What does Mishmar cost?","acceptedAnswer":{"@type":"Answer","text":"The hosted testnet is free: one agent and one trading account on Hyperliquid testnet or Binance demo trading. Real-money plans are announced when real money opens."}},
      {"@type":"Question","name":"Has Mishmar been audited?","acceptedAnswer":{"@type":"Answer","text":"The two-party ECDSA comes from Coinbase's cb-mpc, which has had independent security reviews. Mishmar's change to it, presigning so that an order needs one round trip, has not been audited yet; the note for its reviewer is public."}},
      {"@type":"Question","name":"How are decisions recorded?","acceptedAnswer":{"@type":"Answer","text":"Every decision, allowed, held or blocked, is signed and chained to the one before it as it is written, and the ledger can be exported and verified."}},
      {"@type":"Question","name":"Does the exchange see anything different?","acceptedAnswer":{"@type":"Answer","text":"No. The exchange sees an ordinary API wallet or API key and ordinary signatures. The split is invisible to it."}}
      ]}
---

## What is Mishmar?

Mishmar is a risk management layer for trading bots and AI agents: every order is checked against your limits before it reaches the exchange, large orders wait for your approval, and no key can trade alone. The trading key is split in two, one half with the bot's signer and one with Mishmar, so neither can sign an order by itself.

## Which exchanges does Mishmar work with?

Hyperliquid (perpetuals) and Binance (USD-margined futures) today, on the hosted testnet: Hyperliquid testnet and Binance demo trading. Real money on both is coming; every user is emailed when it opens.

## Does Mishmar hold my funds or my keys?

No. Your funds stay at the exchange. Mishmar holds one half of each trading key, which signs nothing on its own, and never touches your master wallet or your exchange login.

## What happens if Mishmar is down?

The bot cannot place or cancel orders through its trading key, because every signature needs both halves. Your own access to the exchange is untouched: close positions from your master wallet or your exchange account.

## What happens if the bot's machine is hacked?

The attacker holds the bot's half of the key, which signs nothing alone. They can propose orders as the bot, but only orders your rules allow get Mishmar's half. Revoke the trading key in the console and nothing more is signed. A trading key cannot withdraw on Hyperliquid, and on Binance you leave withdrawals off. [If something goes wrong](/concepts/how-a-trading-key-is-split/#if-something-goes-wrong) has every case.

## Can I use Mishmar with a language-model agent?

Yes. `mishmar mcp` is an MCP server with tools to propose orders and cancels, follow a held decision and read positions, PnL, approvals and the exchange's rules. The model never sees a key, and every order it proposes passes your rules. Python and TypeScript agents use the SDK's `Signer` instead. [How do I let an AI agent trade for me safely?](/questions/how-do-i-let-an-ai-agent-trade-for-me-safely/) has the setup.

## Which languages are supported?

Python (`mishmar` on PyPI), TypeScript (`mishmar` on npm), Go (the packages in the public repo), a CLI, and any MCP client. The SDKs run the signer as a child process, so the bot's own process holds no key.

## Is Mishmar open source?

The client side is: the API contract, the signer, both signing schemes, the CLI, the MCP server and the SDKs, under Apache-2.0 at [github.com/mishmar-io/mishmar](https://github.com/mishmar-io/mishmar). The engine that enforces the rules is a separate, private repo.

## What does Mishmar cost?

The hosted testnet is free: one agent and one trading account on Hyperliquid testnet or Binance demo trading. Real-money plans are announced when real money opens.

## Has Mishmar been audited?

The two-party ECDSA comes from Coinbase's [cb-mpc](https://github.com/coinbase/cb-mpc), which has had independent security reviews. Mishmar's change to it, presigning so that an order needs one round trip, has not been audited yet; [the note for its reviewer](https://github.com/mishmar-io/mishmar/blob/main/docs/ecdsa-presign-review.md) is public.

## How are decisions recorded?

Every decision, allowed, held or blocked, is signed and chained to the one before it as it is written, and the ledger can be exported and verified.

## Does the exchange see anything different?

No. The exchange sees an ordinary API wallet or API key and ordinary signatures. The split is invisible to it.
