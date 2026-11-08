---
title: How a trading key is split
description: Why every trading key in Mishmar is split in two between the agent's signer and Mishmar, how the halves are made and sign an order together, what each side checks before it signs, and what happens if either side is lost or stolen.
sidebar:
  order: 1
lastUpdated: 2026-11-08
---

Every agent trades with its own **trading key** on each trading account: on Hyperliquid an API wallet, on Binance an Ed25519 API key. The venue sees an ordinary key and checks ordinary signatures. But the private key behind it never exists in one place. It is split in two: one half lives with the agent's signer, on the machine your agent runs on, and the other with Mishmar's signer. The venue accepts a signature only if both halves made it.

## Why split it

A limit protects you only if nothing can go around it.

- **If the agent held the whole key**, Mishmar's rules would be advice. An agent with a bug or a prompt injection, or whoever took over its machine, could send orders straight to the venue without asking Mishmar.
- **If Mishmar held the whole key**, you would be trusting Mishmar with your trading. A breach at Mishmar, or a mistake in it, could place orders your agent never asked for.

With the key split, neither side can trade alone. An order reaches the venue only if your agent proposed it **and** your rules allowed it, or you approved it.

```text
Agent's machine                       Mishmar
  your agent                            the rules: allow, ask you, or block
    │ proposes, signed with its API key     │
    ▼                                       ▼
  agent's signer  ── agent's half ──▶  Mishmar's signer  ── Mishmar's half
                                            │
                                one signature, from both halves
                                            ▼
                                        the venue
```

## Two keys per agent

An agent has two kinds of key, for two different jobs:

- **Its Mishmar API key** (`msk_…`) is its identity with Mishmar. It signs every proposal the agent sends and every read it makes. Mishmar keeps only its public half. It can sign nothing for a venue. It is also all the agent's signer needs to set itself up: the key names the engine and the key id.
- **A trading key** on each trading account is what the venue knows. This is the key that is split.

## How the key is made

You never see or handle the trading key's private key. When the agent's signer finds a trading key waiting for it (within seconds of the console setting one aside, step 2 of [Add an agent on Hyperliquid](/hyperliquid/add-an-agent/); or when you run `mishmar signer keygen` by hand), the agent's signer and Mishmar's signer each pick a random half of their own, then exchange two rounds of messages. Neither sees the other's half. What comes out is only the public side:

- on Hyperliquid, an address, which you approve as an API wallet at Hyperliquid;
- on Binance, an Ed25519 public key, which you register at Binance as an API key.

The private key that matches it is never put together, not while it is made and not afterwards.

The two venues check different kinds of signature, so each uses its own scheme:

| Venue | What the venue checks | How the two halves sign |
| --- | --- | --- |
| Hyperliquid | ECDSA over secp256k1, as Ethereum uses | Two-party ECDSA, from Coinbase's open-source cb-mpc library |
| Binance | Ed25519 | FROST, the IETF's threshold signing standard (RFC 9591), with two parties |

## How an order is signed

1. Your agent builds a proposal (the market, side, size, price, a deadline), signs it with its API key and sends it to Mishmar.
2. Mishmar checks it against every rule that applies. It blocks it, holds it for your approval, or allows it.
3. Once it is allowed or approved, each signer makes its part of the signature over the exact bytes the venue will check. Mishmar puts them together and sends the order.

Most of the exchange between the signers happens ahead of time, so an allowed order costs no extra round trip:

- **On Hyperliquid**, the two signers run the first three of the protocol's four messages for a batch of future orders in advance. An order then carries only the agent's last message, and Mishmar's signer finishes the signature. Each prepared slot is used once and then thrown away, because using one twice would give the key away. Slots live only in memory.
- **On Binance**, Mishmar hands out its first-round FROST values in advance, so the agent's half of the signature travels inside the proposal.

When the agent's signer can't prepare its half up front, for example a market order, which Mishmar bounds from the venue's order book before it goes out (an exit is the exception: it leaves as a true market order), Mishmar asks the agent's signer for its half once the order is allowed.

## What each side checks before it signs

Each signer checks the order for itself, so neither has to trust the other.

**The agent's signer** signs only:

- an order your agent proposed itself: signed with its own API key, for this trading account, before the proposal's deadline;
- that order as proposed, read from the bytes the venue will verify, not from a summary: the same market and side, a size no larger, a limit price within 1% of the proposal's and never on the wrong side of it, reduce-only if the proposal said so;
- or a cancel of the agent's own order.

It answers each request once. Anything else it refuses, and the reason shows on the decision in the console. You can add a check of your own: a program the signer runs before every signature, which can refuse it.

**Mishmar's signer** signs only an order or a cancel that it decoded itself and that matches a decision Mishmar's engine signed: one your rules allowed or you approved. The engine itself holds no signing secret. Mishmar's halves are kept in a separate signer service.

## Where the halves are kept

- **The agent's half** is in the agent's signer config, `~/.mishmar/agent.json` by default, readable only by the user it belongs to. Run the agent on a machine you control.
- **Mishmar's half** is in Mishmar's signer service, encrypted at rest (AES-256-GCM) under a master key that is stored apart from it. On Google Cloud the master key is itself wrapped by Cloud KMS, and every unwrap is in the KMS audit log.

## What a trading key can't do

A trading key places and cancels orders. On Hyperliquid, an API wallet cannot withdraw or move funds: only your master wallet can, and Mishmar never has its key. On Binance, a key can do what you allow when you register it: allow trading and leave withdrawals off. Mishmar does not check that setting for you yet.

## If something goes wrong

| What happened | What it means | What to do |
| --- | --- | --- |
| Someone takes over the agent's machine | They hold the agent's API key and its half. They can propose orders as the agent, but only orders your rules allow get Mishmar's half. | **Revoke** the trading key on the agent's **Keys** tab, and the agent's API key. Mishmar stops signing at once, and the venue key can then sign nothing. |
| Mishmar is breached | Mishmar's half alone signs nothing, and the agent's signer refuses any order your agent didn't propose. The worst case is that an order your agent did propose gets through when your rules would have blocked it. | To stop all trading at once, stop the agent's signer: without its half, nothing can be signed. |
| Both, at the same time | Whoever has both halves can trade on that trading account, within what the key allows. | This is why trading keys can't withdraw on Hyperliquid, why each agent has its own key on each trading account, and why every key expires. |
| The agent's half is lost (the machine is wiped) | The key can no longer sign. No funds are at risk. | Make a new trading key for the agent and approve or register it again. |
| Mishmar loses its half | The same: the key can no longer sign. No funds are at risk. | Each agent makes a new trading key, and you approve or register it again. |

## Keys expire

Every trading key expires 100 days after it becomes active. Before then you replace it from the agent's **Keys** tab, and the old key keeps working until the new one is active. On Hyperliquid a replacement is always a new API wallet, never the old address approved again. [When the key expires](/hyperliquid/add-an-agent/#when-the-key-expires) has the steps.

## What has been reviewed

The two-party ECDSA comes from Coinbase's [cb-mpc](https://github.com/coinbase/cb-mpc), which has had independent security reviews. Mishmar's change to it, the presigning that lets an order be signed with one round trip, has not been audited yet; [the note for its reviewer](https://github.com/mishmar-io/mishmar/blob/main/docs/ecdsa-presign-review.md) is public. The agent's side, the signer and both signing schemes are open source in [mishmar-io/mishmar](https://github.com/mishmar-io/mishmar), and [SECURITY.md](https://github.com/mishmar-io/mishmar/blob/main/SECURITY.md) says how to report a flaw.
