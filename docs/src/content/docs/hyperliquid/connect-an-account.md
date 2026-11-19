---
title: Connect a Hyperliquid account
description: Connect your Hyperliquid master account to Mishmar by its address, which is all Mishmar needs to read it. Mishmar checks it, reads the balances, positions, value and the last 90 days of transactions of its Perps and Spot accounts, and keeps reading them every minute. Nothing here can trade or move funds; agents are given the accounts afterwards.
sidebar:
  order: 1
lastUpdated: 2026-11-18
---

When you are done, your Hyperliquid master account's address is a **connection** in Mishmar, and it holds two **accounts**, **Perps** and **Spot**, as Hyperliquid keeps them: each one pool of money that Mishmar can read. Mishmar reads them by the address, which takes no key, so nothing you enter here can place an order or move funds. From then on it reads both accounts every minute and keeps what it reads: each one's balances, positions, value over time and transactions. The connection's value is the sum of the two, and a transfer between them shows on both. [What Mishmar keeps of your accounts](/concepts/what-mishmar-keeps-of-your-accounts/) has the details.

An account needs no agent. Agents come afterwards, and you give each one the accounts it may use: a **read agent** only reads them, and a **trade agent** also gets its own key to trade on one (on Hyperliquid, the Perps account: Spot is read, not traded, for now, and so are a sub-account's accounts). [Add an agent on Hyperliquid](/hyperliquid/add-an-agent/) sets up a trade agent, and its dialog can connect the account on the way; [Add a read agent](/start/add-a-read-agent/) sets up one that only reads.

The console sets the network from the Mishmar it is connected to: mainnet on app.mishmar.io, which trades real funds, and testnet on app.testnet.mishmar.io. You don't choose it.

It takes about two minutes.

## What you need

- **Your Hyperliquid master address**, or the browser wallet that holds it. It is the address you trade from on Hyperliquid, not an API wallet. On mainnet ([app.hyperliquid.xyz](https://app.hyperliquid.xyz)) for app.mishmar.io, or testnet ([app.hyperliquid-testnet.xyz](https://app.hyperliquid-testnet.xyz)) for the testnet console. An account that Mishmar only reads needs no USDC in it; one an agent will trade on does, and that is checked when the agent's trading key is verified, not here. On testnet the USDC is mock USDC from Hyperliquid's faucet at [app.hyperliquid-testnet.xyz/drip](https://app.hyperliquid-testnet.xyz/drip). Hyperliquid gives it only to an address that has deposited on mainnet. If you sign in to Hyperliquid with a browser wallet (Rabby or MetaMask), use the same address on both networks: the faucet needs nothing more than that address having deposited on mainnet. If you sign in to Hyperliquid by email, your mainnet and testnet addresses differ, so the faucet does not know your testnet address. The simplest way round is a browser wallet of your own that has made one deposit on mainnet. Hyperliquid's page [Testnet faucet](https://hyperliquid.gitbook.io/hyperliquid-docs/onboarding/testnet-faucet) describes another way, which involves your mainnet wallet's private key. That key controls real funds: follow Hyperliquid's own steps only, and never paste it into Mishmar, an AI app, a chat or an email. Mishmar never asks for it.
- **The owner or admin role** on the Mishmar account. Approvers and viewers cannot connect accounts.
- **A passkey or a linked wallet on your Mishmar login.** Hosted Mishmar asks for a fresh proof of one before a change like this one. Add one under **Profile** in the console's sidebar: **Add passkey on this device** under **Passkeys**, or **Link this browser's wallet** under **Wallets**.
- **Room on your plan.** The free plan holds one connection, on any venue, with all its accounts.

## 1. Start connecting

In the console, open **Connected accounts** and click **Connect account**. Click **Hyperliquid**.

![The Connect an account dialog at step 1 of 4, Pick the venue, with Hyperliquid and Binance to choose from](/screens/hyperliquid/connect-an-account/pick-venue.png)

## 2. Enter the address

- **Type**: leave **Master account**. (**Sub-account** connects a Hyperliquid sub-account, by its own address and its master's.)
- **Master account address**: paste the master address, or click **Use my wallet's address** to take the address your browser wallet is on. Nothing is signed.
- **Network**: set by Mishmar. **Mainnet** on app.mishmar.io, **Testnet** on the testnet.
- **Your name for it**: what you call the connection everywhere in Mishmar, shown before each of its accounts (`Hyperliquid · Main · Perps`). It starts as `Main`.

![Step 2 of 4, Connect Hyperliquid: Master account chosen under Type, the master account address filled in from the browser wallet, the network set to Testnet, the name Main, and the Connect button](/screens/hyperliquid/connect-an-account/address.png)

Click **Connect**. The console may ask you to confirm with your passkey or wallet first; the proof lasts ten minutes.

## 3. Mishmar checks it

The dialog asks Hyperliquid about the master account at once and shows two checks:

- **Master account found on the network**, with the address.
- **Network matches**: `mainnet` on app.mishmar.io, `testnet` on the testnet.

Margin is not checked here: **Master account has margin** is a check on an agent's trading key, made when you add an agent that trades on the account.

When both pass, the connection is verified, its accounts turn **Active** and the dialog moves on by itself. If one fails, fix it at Hyperliquid and click **Check again**. The connection is saved either way: **Finish later** closes the dialog, and **Verify** on the connection's page runs the checks again. Until every check passes, the connection and its accounts stay **Pending**: Mishmar does not read them yet, and an agent's orders on one are blocked with `The connection of … is not verified`.

## 4. The first read

Mishmar reads the connection for the first time. The dialog lists the accounts it found, **Perps** and **Spot**, with their values, and shows each kind of data as it arrives: **Balances**, **Positions**, **Value** and **Transactions**, each with the time it is as of. Transactions go back 90 days. When all four are in, the dialog says **Main is connected** (with your name for it). Click **Done**: the connection's page opens.

![Step 4 of 4: Main is connected, with the two accounts found at Hyperliquid, Perps and Spot (read only), and their values, then Balances, Positions, Value and Transactions each ticked with the time they are as of, and the transactions read from 90 days back](/screens/hyperliquid/connect-an-account/first-read.png)

From now on Mishmar reads both accounts every minute, whether or not an agent uses them.

## Check it worked

- **Connected accounts** lists the connection with its two accounts under it, **Perps** and **Spot**, each **Active**. The connection's row says **Verified** under **Status**, and the time of its last read under **Data**.
- On the connection's page, under its **Setup** tab, **Setup** shows the **Master address** and the **Network**, **Verification** lists the two checks, both passed, and **Data** lists balances, positions, value and transactions as **Up to date**, each with how far back its history goes. **Connection**, beside them, says **Master account** under **Type** and **Its address** under **Reached with**.
- Open an account from the connection's **Overview**, under **Accounts**. Its **Balances**, **Positions**, **Transactions** and **Value** tabs show what Mishmar read.
- Copy the Perps account's **Id** (`ta_…`) from its **Overview**: an agent proposes orders to it, and reads it by it.

![The connection's Setup tab: Setup with the master address and the network, Verification with its three checks passed, and Data with balances, positions, value and transactions up to date and the date each one's history starts](/screens/hyperliquid/connect-an-account/account-page.png)

## Who trades here

Once an agent trades on the Perps account, Mishmar needs to know whether anything else trades there too. Answer it on the account's page, under **Who trades here**: **Does anything other than your agents trade on this account?** Choose **No, only my agents** (the default) if Mishmar's agents are the only thing that trades here: an outside trade then blocks new orders until you say whose it is. Choose **Yes, I or other tools trade here too** if you or another bot also trade on it: outside trades then count toward this account's limits, not any agent's. While no agent holds a trading key on the account, the answer changes nothing: Mishmar only records what happens there.

![The Who trades here panel: Does anything other than your agents trade on this account?, with No, only my agents chosen and Yes, I or other tools trade here too below it](/screens/hyperliquid/connect-an-account/who-trades-here.png)

Next, give the account to an agent: [add an agent on Hyperliquid](/hyperliquid/add-an-agent/) to trade on it, or [add a read agent](/start/add-a-read-agent/) to read it.

## When something goes wrong

- **`0x followed by 40 hex characters`** under **Master account address**: the address is missing, cut short or has a typo. Paste the whole address, or click **Use my wallet's address**.
- **Use my wallet's address is greyed out**: the console found no browser wallet in this browser. Paste the address instead, or install Rabby or MetaMask and reload the page.
- **`Couldn't tell which network. Reload and try again.`** next to **Network**: the console couldn't ask Mishmar which network it trades, and **Connect** stays greyed out. Reload the page.
- **`This needs a passkey or a wallet. Add one under your profile, then try again.`**: your Mishmar login has neither. Open **Profile**, add a passkey or link a wallet, then click **Connect** again.
- **Master account has margin fails, with `$0 available`**, when you add an agent that trades on the account: the account holds no USDC on this network. Deposit some on that network at Hyperliquid, then run the agent's check again. On testnet that means the faucet; see "What you need" above.
- **The toast says `… of 2 checks still failing`**: the failing checks are marked in red with what Hyperliquid answered. Fix them at Hyperliquid, then click **Verify** again.
- **The account's page says `Mishmar can't read this account's … right now`**: the last reads of that kind failed, with the reason after it, and what you see is as of the time it gives. Mishmar keeps trying every minute; tell us if it doesn't clear.
- **You entered the wrong address.** A connection's address can't be changed. On the connection's page, click the bin icon (**Delete connection**), then connect it again. Deleting removes the connection with all its accounts: Mishmar stops reading them, and the agents given them lose access. Mishmar refuses with `Agents still hold venue keys on this connection's accounts; revoke them first` while an agent has a trading key on one of them: revoke those keys on each agent's **Keys** tab first.
- **`The Mishmar account's plan holds 1 connection; delete one or move to a larger plan`**, or **Connect account** is greyed out with the reason beside it: the free plan holds one connection, on Hyperliquid and Binance together, whatever the number of accounts under it. Delete one, or move to a larger plan.
