---
title: Add an agent on Hyperliquid
description: Add an agent that trades on a connected Hyperliquid account. Give it its Mishmar API key, start it with that one key, and approve the API wallet its signer generates at Hyperliquid's own API page. Four steps in one dialog, one command on the agent's machine, nothing signed in the console.
sidebar:
  order: 2
lastUpdated: 2026-11-12
---

When you are done, the agent has two keys:

- **Its Mishmar API key**, which it signs every proposal with. Mishmar keeps only the public half.
- **A trading key on your Hyperliquid account**: a Hyperliquid API wallet that you approve at Hyperliquid. Its private key is never whole anywhere. One half lives with the agent's signer, the other with Mishmar. An order reaches Hyperliquid only when Mishmar's rules allow it and both halves sign it.

You approve the API wallet on Hyperliquid's own API page, with your own wallet, the way you would approve any bot's. The console never asks your master wallet to sign anything, and the API wallet cannot withdraw.

This page sets up a **trade agent**: one that trades on a connected account, within your rules. An agent that only reads your accounts, for an assistant or a report, needs no signer and no trading key: [Add a read agent](/start/add-a-read-agent/).

It takes about five minutes.

## What you need

- **Your Hyperliquid master account** with USDC in it: the account you trade from on Hyperliquid, not an API wallet. On mainnet ([app.hyperliquid.xyz](https://app.hyperliquid.xyz)) for real funds, or testnet ([app.hyperliquid-testnet.xyz](https://app.hyperliquid-testnet.xyz)) for the testnet console: the console sets the network from the Mishmar it is connected to, mainnet on app.mishmar.io and testnet on app.testnet.mishmar.io. You don't need to have connected it to Mishmar yet: the dialog can.
- **The machine the agent runs on**, with the [mishmar CLI installed](/start/install-the-cli/), or the Python or TypeScript SDK, which carries it.
- **The owner or admin role** on the Mishmar account. Approvers and viewers cannot set up agents.
- **A passkey or a linked wallet on your Mishmar login.** Hosted Mishmar asks for a fresh proof of one before adding an agent. Add one under **Profile** in the console's sidebar: **Add passkey on this device** under **Passkeys**, or **Link this browser's wallet** under **Wallets**.
- **Room on your plan.** The free plan holds one agent and one connected account.

:::note[Three agents per Hyperliquid account]
Hyperliquid lets a master account approve up to three named API wallets, and each Mishmar agent on that account uses one. Named API wallets you use for other bots count toward the three. Mishmar names every key of one agent the same (`mishmar-` and the last eight characters of the agent's id), so a replacement key takes over the slot of the one it replaces.
:::

## 1. Add the agent

In the console, open **Agents** and click **Add agent**. The dialog has four steps and moves on by itself as the agent and the venue do their part.

- **Name**: what you call the agent everywhere in Mishmar.
- **What it is for**: choose **Trade**. Nothing is picked until you choose.
- **Trades on**: the connected account the agent gets its trading key on. Pick your Hyperliquid account if you connected it already (the only one there is is picked for you), or click **Connect an account**, which opens [the same steps as connecting one](/hyperliquid/connect-an-account/) without leaving the dialog and picks the account when it is done.
- **Portfolio**: the portfolio's rules apply to the agent, alongside its own, and its capital to the agent's leverage and drawdown rules. The account's default portfolio is preselected.
- **Description** (optional): what it trades and how.

![Step 1 of 4, Add agent: the name Trend follower, Trade chosen under What it is for, Trades on set to Hyperliquid main with Connect an account beside it, the default portfolio, and a description](/screens/hyperliquid/add-an-agent/add-agent.png)

Click **Add agent**. The console may ask you to confirm with your passkey or wallet first; the proof lasts ten minutes. Mishmar creates the agent, issues its Mishmar API key, gives it the account and sets a trading key aside on it, all at once. The agent reads the account it trades on too: [What Mishmar keeps of your accounts](/concepts/what-mishmar-keeps-of-your-accounts/) says what it can read.

## 2. Start the agent

The dialog shows the **Mishmar API key**. **It is shown once.** Copy it before you close the dialog; if you lose it, revoke it on the agent's **Keys** tab and issue another.

On the machine the agent runs on, start the signer with the key. The dialog shows the command for your Mishmar; on hosted Mishmar it is just this, with your own key:

```sh
MISHMAR_API_KEY=msk_… mishmar signer run
```

![Step 2 of 4, Start the agent: the Mishmar API key (masked here) and the command that starts the agent, each with a copy button, and the dialog waiting for the agent to start, then for it to generate the trading key](/screens/hyperliquid/add-an-agent/start-the-agent.png)

A self-hosted engine is named in `MISHMAR_ENGINE` as well; the dialog includes it when it applies. The key names the engine and the key id, so the signer needs nothing else: with no config at `~/.mishmar/agent.json` yet, it checks the key with Mishmar, writes the config (mode 0600) and starts.

```text
level=INFO msg="signer: no config yet; setting the agent up from MISHMAR_API_KEY" path=/home/you/.mishmar/agent.json
level=INFO msg="signer: config written" path=/home/you/.mishmar/agent.json agent=agt_… name="Trend follower" keyId=key_… engine=https://api.mishmar.io/a/acc_…/v1
level=INFO msg="signer: polling" engine=https://api.mishmar.io/a/acc_…/v1 keyId=key_… venueKeys=0
level=INFO msg="signer: generating a trading key with the engine's signer" venueKey=vk_… connectedAccount=ta_… venue=hyperliquid scheme=ecdsa-2p
level=INFO msg="signer: trading key made; approve its API wallet at the venue" venueKey=vk_… connectedAccount=ta_… venue=hyperliquid network=mainnet apiWallet=0x… name=mishmar-… where="https://app.hyperliquid.xyz/API (paste the address, give it the name), or the console"
```

Within a few seconds the signer finds the trading key the dialog set aside and generates it with Mishmar's signer: each side makes a half and they run two rounds together, so neither ever sees the other's half. What comes out is an ordinary Hyperliquid address, the API wallet. The dialog shows **Agent connected**, then **Trading key generated**, and moves on.

Leave the signer running: it signs only orders this agent proposed and Mishmar allowed, and orders that wait for your approval need it running, because their second signature comes later.

:::tip[From your agent's code]
The Python and TypeScript SDKs start the signer for you as a child process, with the same key: set `MISHMAR_API_KEY` in the bot's environment, or pass it (`Signer(api_key=…)`, `new Signer({ apiKey })`). For an LLM agent, `mishmar mcp` does the same; put the key in the MCP server's env. More than one agent on the same machine: give each its own config file with `--config ~/.mishmar/agents/<name>.json` (or `MISHMAR_AGENT_CONFIG`), since a config holds one agent's key.
:::

## 3. Approve the API wallet at Hyperliquid

The dialog shows the **API wallet address** and the **name** to give it, each with a copy button, and a button that opens Hyperliquid's API page for the right network ([app.hyperliquid-testnet.xyz/API](https://app.hyperliquid-testnet.xyz/API) on testnet, [app.hyperliquid.xyz/API](https://app.hyperliquid.xyz/API) on mainnet).

![Step 3 of 4, Approve the API wallet at Hyperliquid: the API wallet address and the name mishmar-… to give it, each with a copy button, the button that opens Hyperliquid's API page on testnet, the dialog waiting for the approval, and the link to approve from this browser's wallet instead](/screens/hyperliquid/add-an-agent/approve-api-wallet.png)

On that page, connected with your master account:

1. Paste the address into **Address of API wallet**. Don't click **Generate**: that would make a different wallet, whose private key your browser would hold.
2. Paste the name into **The name for the API wallet**. Another name works too, but uses up another of the account's three named slots when the key is later replaced.
3. Click **Authorize API Wallet** and sign in your wallet.

Mishmar asks Hyperliquid about the wallet every ten seconds while the dialog is open, and once a minute otherwise. When Hyperliquid lists it under your account, the dialog says **Approved. The key is active.** A connected account that was still pending is verified at the same time (its three checks run, and it turns **Active**).

The key expires 100 days after it becomes active, or when Hyperliquid's own expiry for the wallet comes first. [When the key expires](#when-the-key-expires) has the replacement.

:::tip[Approving from the console instead]
The dialog's **Or approve from this browser's wallet instead** does the same approval from the console: it connects your browser wallet, checks it is the master account, asks it to sign Hyperliquid's `HyperliquidTransaction:ApproveAgent` typed data and sends the signature to Hyperliquid. It needs the connected account's master address to be known. Before you sign, check that `agentAddress` is the API wallet the dialog showed and `hyperliquidChain` is the account's network.
:::

## 4. Ready

The dialog says the agent is ready. Click **Finish**: the agent's page opens.

![Step 4 of 4: Trend follower is ready, with Agent connected and Trading key active on Hyperliquid main both ticked, and the Finish button](/screens/hyperliquid/add-an-agent/ready.png)

## Place a test order

With the signer running, in a second terminal on the agent's machine, propose a small limit buy far below the market, so it rests on the book and does not fill. Use your connected account's id (`ta_…`, on its page in the console):

```sh
mishmar propose --config ~/.mishmar/agent.json --connected-account ta_… --symbol BTC --side buy --size 0.0003 --price 50000
```

The output looks something like this. Your rules and numbers will differ.

```text
decision dec_…: allow
  buy 0.0003 BTC on hyperliquid, notional 15, latency 2.10 ms, revision 12
  passed  Max order size                       order_size observed 15, limit 150
  signing: signed (one-shot: the agent's half came with the proposal)
  venue: resting order 64462809897 at 2026-11-04T10:20:31Z
```

The order is on Hyperliquid's book, and the decision is on the console's **Decisions** page. To pull it, copy its **Client order id** (`msh-…`) from the decision's page and run:

```sh
mishmar propose --config ~/.mishmar/agent.json --connected-account ta_… --symbol BTC --cancel msh-…
```

Hyperliquid refuses orders worth less than $10. If BTC trades below $50,000, lower the price so the order still rests.

### From your agent's code

The SDKs start the signer and turn an order into a proposal; the bot itself never holds a key. Only one signer runs per config file, so stop the `mishmar signer run` from step 2 first.

```python
from mishmar import Signer

with Signer() as s:  # MISHMAR_API_KEY in the environment, or ~/.mishmar/agent.json from before
    d = s.order("ta_…", "BTC", "buy", "0.0003", price="50000")
    if d["verdict"] == "require_approval":
        d = s.wait(d["id"])
```

For an LLM agent, `mishmar mcp` serves the same actions as MCP tools. The [repository's README](https://github.com/mishmar-io/mishmar#readme) covers both.

## Check it worked

- The agent's page shows no **Setup isn't finished** and no **API wallet not approved** warning, and **Signer** reads **Connected** under **Overview**.
- On the agent's **Keys** tab, **Mishmar API keys** lists the `msk_…` key as Active, and **Trading keys** lists the Hyperliquid connected account as Active, **Approved** under **At the venue**, and the date the key expires under **Expires**.
- On Hyperliquid's API page, the API wallet is listed under the name `mishmar-…` with its **Valid Until**.
- On the agent's **Overview**, **Connected accounts** lists the account with **Reads and trades** under **Access**.
- **Connected accounts** lists the account as **Active**, with the agent counted under **Agents**.

![The agent's Keys tab: under Mishmar API keys, the msk_… key Active; under Trading keys, the key on Hyperliquid main, ECDSA, Active, Approved at the venue, and the date it expires](/screens/hyperliquid/add-an-agent/agent-keys.png)

## When something goes wrong

- **`no config at …: put the agent's API key from the console in MISHMAR_API_KEY and start again`**: the signer found neither a config nor the key. Export `MISHMAR_API_KEY` in the shell that runs it, or pass it on the command line as in step 2.
- **`… does not accept this key (…): it may have been revoked; issue a new one in the console`**: the key pasted is not one Mishmar knows, or was revoked. Copy it again, or revoke it on the **Keys** tab and issue another.
- **`the key names environment "…", which is not a hosted one; set MISHMAR_ENGINE`**: the key came from a self-hosted engine. Add `MISHMAR_ENGINE=<its API base URL, with /v1>` to the command; the dialog shows it when it applies.
- **`… holds another key than MISHMAR_API_KEY; remove the file to set the agent up again`**: the machine already has a config for another agent. Give this agent its own file with `--config`, or remove the old file.
- **The dialog stays at *Waiting for the agent to start…***: the signer is not reaching Mishmar. Check its output; a signer that started shows `signer: polling`. On hosted Mishmar the first call may take up to a minute while a stopped account wakes.
- **`This needs a passkey or a wallet. Add one under your profile, then try again.`**: your Mishmar login has neither. Open **Profile**, add a passkey or link a wallet, then try the step again.
- **The signer logs `the engine lists an active trading key this signer holds no share of`**: the config file with the agent's half is gone (a container without a persistent volume, a reinstalled machine). Restore it, or revoke the key on the **Keys** tab and add a new one, which the running signer generates.
- **Key generation stopped partway** (the network dropped, the machine slept): the signer runs it again on its next look, within a few seconds. Nothing to do.
- **The approval does not show up.** Check the address you pasted is the one the dialog showed, and that you approved on the account's network (testnet or mainnet). Mishmar looks once a minute; open the agent's **Keys** tab, click **⋯** on the key's row and choose **Verify** to look now.
- **You clicked Generate at Hyperliquid.** That made another wallet, which Mishmar does not hold half of. Remove it there and paste the dialog's address instead.
- **`Agent …'s key on connected account … has expired; give the agent a new key`**: see [When the key expires](#when-the-key-expires).
- **`The account's plan holds 1 agent; delete one or move to a larger plan`**, or the same for a connected account. The free plan holds one agent and one connected account, read and trade agents together, and **Add agent** is greyed out with the reason beside it. Delete one, or move to a larger plan.
- **An order comes back `rate_limited`: `The account's request budget for hyperliquid is spent for now; send it again in … s`.** Each plan has a share of what Hyperliquid lets one address send. The SDKs and `mishmar signer run` send the order again after the wait. Closing positions and cancels always go.
- **The connected account says New orders are blocked: Mishmar has lost the venue's stream of this account's fills and orders.** While Mishmar can't see the account's fills and orders, it blocks new risk. Closing positions still works. Trading starts again by itself once the stream is back; if it hasn't within a few minutes, tell us.

## When the key expires

Every trading key expires 100 days after it became active, on every venue, or earlier when Hyperliquid's own expiry for the wallet comes first. The date is under **Expires** on the agent's **Keys** tab. In the key's last week the date turns amber, the agent's page and its row in **Agents** warn, and Mishmar emails the account's owner and admins 7, 2 and 1 days before. Once the date passes, the key is **Expired**: Mishmar never signs with it again, and the agent can't trade on this account.

Replace the key before then:

1. On the agent's page, click **Replace key**, in the warning or under **⋯** on the key's row.
2. Click **Add new key**. The current key keeps working.
3. The agent's running signer generates the new key within a few seconds and logs its API wallet.
4. Approve the new API wallet at Hyperliquid as in step 3, under the same name: Hyperliquid then drops the old wallet from the slot.

The moment the new key is active, Mishmar revokes the old one, and the agent trades on through the change. An order the agent built with the old key in the seconds after can be refused; propose it again.

If the key has already expired, the warning offers **Make a new key**, with the same steps. The agent can't trade on this account until the new key is active.

A new key always means a new API wallet. Hyperliquid [strongly advises against approving an API wallet's address again](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/nonces-and-api-wallets): once a wallet expires its nonce history may be pruned, and orders it signed earlier could then be replayed. Mishmar refuses to approve a key a second time.

## Taking the key away

On the agent's **Keys** tab, click **⋯** on the trading key's row and choose **Revoke**. Mishmar stops signing with it at once, and without Mishmar's half the API wallet can sign nothing. Hyperliquid keeps listing it until its approval runs out; remove it on the API page to free the slot.
