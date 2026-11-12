---
title: Add an agent on Binance
description: Add an agent that trades on a connected Binance account. Give it its Mishmar API key, start it with that one key, and register the Ed25519 trading key its signer generates at Binance. Four steps in one dialog, one command on the agent's machine.
sidebar:
  order: 2
lastUpdated: 2026-11-12
---

When you are done, the agent has two keys:

- **Its Mishmar API key**, which it signs every proposal with. Mishmar keeps only the public half.
- **A trading key on your Binance account**: a self-generated Ed25519 API key that you register at Binance. Its private key is never whole anywhere. One half lives with the agent's signer, the other with Mishmar. An order reaches Binance only when Mishmar's rules allow it and both halves sign it.

Binance sees an ordinary API key, and you decide at Binance what it may do: let it trade futures, and leave withdrawals off.

This page sets up a **trade agent**: one that trades on a connected account, within your rules. An agent that only reads your accounts, for an assistant or a report, needs no signer and no trading key: [Add a read agent](/start/add-a-read-agent/).

It takes about ten minutes.

## What you need

- **A Binance account**, on [binance.com](https://www.binance.com) for real funds (app.mishmar.io) or on Binance demo trading ([demo.binance.com](https://demo.binance.com)) for the testnet (app.testnet.mishmar.io), with its USDⓈ-M Futures account **opened and funded** (open it under Derivatives, then transfer USDT or USDC from the spot wallet into the futures wallet: verification needs margin there, and a futures account that was never opened or funded reads as empty) and the futures position mode set to **One-way**. You don't need to have connected it to Mishmar yet: the dialog can, with a read-only key you make at Binance first ([Connect a Binance account](/binance/connect-an-account/) has the steps).
- **The machine the agent runs on**, with the [mishmar CLI installed](/start/install-the-cli/), or the Python or TypeScript SDK, which carries it.
- **The owner or admin role** on the Mishmar account. Approvers and viewers cannot set up agents.
- **A passkey or a linked wallet on your Mishmar login.** Hosted Mishmar asks for a fresh proof of one before adding an agent. Add one under **Profile** in the console's sidebar: **Add passkey on this device** under **Passkeys**, or **Link this browser's wallet** under **Wallets**.
- **Room on your plan.** The free plan holds one agent and one connected account.

## 1. Add the agent

In the console, open **Agents** and click **Add agent**. The dialog has four steps and moves on by itself as the agent and the venue do their part.

- **Name**: what you call the agent everywhere in Mishmar.
- **What it is for**: choose **Trade**. Nothing is picked until you choose.
- **Trades on**: the connected account the agent gets its trading key on. Pick your Binance account if you connected it already (the only one there is is picked for you), or click **Connect an account**, which opens [the same steps as connecting one](/binance/connect-an-account/) without leaving the dialog, read-only key included, and picks the account when it is done.
- **Portfolio**: the portfolio's rules apply to the agent, alongside its own, and its capital to the agent's leverage and drawdown rules. The account's default portfolio is preselected.
- **Description** (optional): what it trades and how.

![Step 1 of 4, Add agent: the name Reversion, Trade chosen under What it is for, Trades on set to the Binance account with Connect an account beside it, the default portfolio, and a description](/screens/binance/add-an-agent/add-agent.png)

Click **Add agent**. The console may ask you to confirm with your passkey or wallet first; the proof lasts ten minutes. Mishmar creates the agent, issues its Mishmar API key, gives it the account and sets a trading key aside on it, all at once. The agent reads the account it trades on too: [What Mishmar keeps of your accounts](/concepts/what-mishmar-keeps-of-your-accounts/) says what it can read.

## 2. Start the agent

The dialog shows the **Mishmar API key**. **It is shown once.** Copy it before you close the dialog; if you lose it, revoke it on the agent's **Keys** tab and issue another.

On the machine the agent runs on, start the signer with the key. The dialog shows the command for your Mishmar; on hosted Mishmar it is just this, with your own key:

```sh
MISHMAR_API_KEY=msk_… mishmar signer run
```

![Step 2 of 4, Start the agent: the Mishmar API key (masked here) and the command that starts the agent, each with a copy button, and the dialog waiting for the agent to start, then for it to generate the trading key](/screens/binance/add-an-agent/start-the-agent.png)

A self-hosted engine is named in `MISHMAR_ENGINE` as well; the dialog includes it when it applies. The key names the engine and the key id, so the signer needs nothing else: with no config at `~/.mishmar/agent.json` yet, it checks the key with Mishmar, writes the config (mode 0600) and starts.

```text
level=INFO msg="signer: no config yet; setting the agent up from MISHMAR_API_KEY" path=/home/you/.mishmar/agent.json
level=INFO msg="signer: config written" path=/home/you/.mishmar/agent.json agent=agt_… name="Reversion" keyId=key_… engine=https://api.mishmar.io/a/acc_…/v1
level=INFO msg="signer: polling" engine=https://api.mishmar.io/a/acc_…/v1 keyId=key_… venueKeys=0
level=INFO msg="signer: generating a trading key with the engine's signer" venueKey=vk_… connectedAccount=ta_… venue=binance scheme=frost-ed25519
level=INFO msg="signer: trading key made; register its public key at the venue" venueKey=vk_… connectedAccount=ta_… venue=binance publicKey="-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA…\n-----END PUBLIC KEY-----\n" then="enter the key id the venue issues in the console (the agent's Keys tab), with this signer running"
```

Within a few seconds the signer finds the trading key the dialog set aside and generates it with Mishmar's signer (FROST, the IETF's threshold signing standard): each side makes a half and they run two rounds together, so neither ever sees the other's half. What comes out is an ordinary Ed25519 public key, whose private key exists only as the two halves. The dialog shows **Agent connected**, then **Trading key generated**, and moves on.

Leave the signer running: it signs only orders this agent proposed and Mishmar allowed, the read that proves the key in step 3, and the orders that wait for your approval, whose second signature comes later.

:::tip[From your agent's code]
The Python and TypeScript SDKs start the signer for you as a child process, with the same key: set `MISHMAR_API_KEY` in the bot's environment, or pass it (`Signer(api_key=…)`, `new Signer({ apiKey })`). For an LLM agent, `mishmar mcp` does the same; put the key in the MCP server's env. More than one agent on the same machine: give each its own config file with `--config ~/.mishmar/agents/<name>.json` (or `MISHMAR_AGENT_CONFIG`), since a config holds one agent's key.
:::

## 3. Register the key at Binance

The dialog lists what to do at Binance, each step with the field it fills in right under it. Both keys you make there are used from Mishmar's address only, which the dialog shows: on binance.com restrict each of them to it (**Restrict access to trusted IPs only**); on demo trading the restriction is optional. At Binance ([binance.com](https://www.binance.com), or [demo.binance.com](https://demo.binance.com) for the testnet), signed in to the account (or subaccount) the agent trades:

1. **The trading key**: create an API key, **Self-generated**, and paste the dialog's public key, all of it, from `-----BEGIN PUBLIC KEY-----` to `-----END PUBLIC KEY-----`. Turn on **Enable Futures**. Leave withdrawals off; Mishmar does not check that setting yet, so check it yourself.
2. Binance shows one **API key** for the new key and no secret key: a self-generated key has no secret at Binance, because its private key is the split one. That string of letters and digits is the key id Mishmar needs. Paste it into **Key id issued by Binance**.

![Step 3 of 4, Register the key at Binance, on the testnet console: on demo.binance.com, create a self-generated API key with the public key shown in full, from BEGIN to END, and Enable Futures on; then paste the API key Binance shows into Key id issued by Binance; then Save and verify](/screens/binance/add-an-agent/register-key.png)

A connected account that has no read-only key yet (one connected before the console asked for it) gets three steps first, in the same visit: open and fund the USDⓈ-M Futures account, set the position mode to One-way, and make the read-only key as in [Connect a Binance account](/binance/connect-an-account/), pasted as **Read-only API key** and **Its secret key**.

Click **Save and verify**. Mishmar proves the trading key end to end: it asks the agent's signer to co-sign a read of the account and sends it to Binance under the new key. It also checks what trading on the account needs, which connecting it did not. Four checks:

- **Trading key accepted by venue**: `Accepted by Binance, co-signed by the agent's signer`.
- **Account has margin**: the amount available, for example `$4891.37 available`.
- **Position mode is one-way**: `One-way`.
- **Environment matches**: `Binance live` on app.mishmar.io, `Binance demo trading` on the testnet.

When all four pass, the key is **Active**.

The dialog says **Start the agent first** while the agent's signer is not connected: the verification needs it. Start the agent as in step 2, then click **Save and verify** again.

## 4. Ready

The dialog says the agent is ready. Click **Finish**: the agent's page opens.

![Step 4 of 4: Reversion is ready, with Agent connected and Trading key active on the Binance account both ticked, and the Finish button](/screens/binance/add-an-agent/ready.png)

## Place a test order

With the signer running, in a second terminal on the agent's machine, propose a small limit buy a few percent under the market, so it rests on the book and does not fill. Use your connected account's id (`ta_…`, on its page in the console) and a price about 3% under BTCUSDT's current price:

```sh
mishmar propose --config ~/.mishmar/agent.json --connected-account ta_… --symbol BTCUSDT --side buy --size 0.001 --price 75000
```

The output looks something like this. Your rules and numbers will differ.

```text
decision dec_…: allow
  buy 0.001 BTCUSDT on binance, notional 75, latency 2.10 ms, revision 12
  passed  Max order size                       order_size observed 75, limit 150
  signing: signed (one-shot: the agent's half came with the proposal)
  venue: resting order 28665991418 at 2026-11-04T16:26:54Z
```

The order is on Binance's book, and the decision is on the console's **Decisions** page. To pull it, copy its **Client order id** (`msh-…`) from the decision's page and run:

```sh
mishmar propose --config ~/.mishmar/agent.json --connected-account ta_… --symbol BTCUSDT --cancel msh-…
```

Binance refuses an order worth less than its minimum for the symbol, $50 on BTCUSDT, live and on demo trading, at the time of writing. At 0.001 BTC, keep the price above $50,000.

### From your agent's code

The SDKs start the signer and turn an order into a proposal; the bot itself never holds a key. Only one signer runs per config file, so stop the `mishmar signer run` from step 2 first.

```python
from mishmar import Signer

with Signer() as s:  # MISHMAR_API_KEY in the environment, or ~/.mishmar/agent.json from before
    d = s.order("ta_…", "BTCUSDT", "buy", "0.001", price="75000")
    if d["verdict"] == "require_approval":
        d = s.wait(d["id"])
```

For an LLM agent, `mishmar mcp` serves the same actions as MCP tools. The [repository's README](https://github.com/mishmar-io/mishmar#readme) covers both.

## Check it worked

- The agent's page shows no **Setup isn't finished** warning, and **Signer** reads **Connected** under **Overview**.
- On the agent's **Keys** tab, **Mishmar API keys** lists the `msk_…` key as Active, and **Trading keys** lists the Binance connected account with key type **Ed25519**, **Active**, **Key id entered** under **At the venue**, and the date the key expires under **Expires**.
- On Binance, API Management lists both keys: the read-only one and the self-generated one.
- On the agent's **Overview**, **Connected accounts** lists the account with **Reads and trades** under **Access**.
- **Connected accounts** lists the account as **Active**; on its page, **Setup** shows **Read-only key** **On file**.

![The agent's Keys tab: under Mishmar API keys, the msk_… key Active; under Trading keys, the key on the Binance account, Ed25519, Active, Key id entered at the venue, and the date it expires](/screens/binance/add-an-agent/agent-keys.png)

## When something goes wrong

- **`no config at …: put the agent's API key from the console in MISHMAR_API_KEY and start again`**: the signer found neither a config nor the key. Export `MISHMAR_API_KEY` in the shell that runs it, or pass it on the command line as in step 2.
- **`… does not accept this key (…): it may have been revoked; issue a new one in the console`**: the key pasted is not one Mishmar knows, or was revoked. Copy it again, or revoke it on the **Keys** tab and issue another.
- **`the key names environment "…", which is not a hosted one; set MISHMAR_ENGINE`**: the key came from a self-hosted engine. Add `MISHMAR_ENGINE=<its API base URL, with /v1>` to the command; the dialog shows it when it applies.
- **`… holds another key than MISHMAR_API_KEY; remove the file to set the agent up again`**: the machine already has a config for another agent. Give this agent its own file with `--config`, or remove the old file.
- **The dialog stays at *Waiting for the agent to start…***: the signer is not reaching Mishmar. Check its output; a signer that started shows `signer: polling`. On hosted Mishmar the first call may take up to a minute while a stopped account wakes.
- **`This needs a passkey or a wallet. Add one under your profile, then try again.`**: your Mishmar login has neither. Open **Profile**, add a passkey or link a wallet, then try the step again.
- **The signer logs `the engine lists an active trading key this signer holds no share of`**: the config file with the agent's half is gone (a container without a persistent volume, a reinstalled machine). Restore it, or revoke the key on the **Keys** tab and add a new one, which the running signer generates.
- **Key generation stopped partway** (the network dropped, the machine slept): the signer runs it again on its next look, within a few seconds. Nothing to do.
- **`The agent's signer is not connected; start it and verify again`**: the agent was not running when you clicked **Save and verify**. Everything you typed was saved; start the agent, then click it again, or on the agent's **Keys** tab click **⋯** on the key's row and choose **Verify**.
- **Read-only key accepted by venue fails, with `Refused by Binance: …`**: Binance did not accept the read-only key. Check that you made it on the Binance that matches your console (binance.com for app.mishmar.io, demo.binance.com for the testnet: a key from one does not work on the other), copied the key and its secret whole, and, on binance.com, restricted it to the address Mishmar shows. While connecting, **Use another key** takes another one.
- **The key's check says `Refused by Binance: …`**: check at Binance that the key is the self-generated one with this public key, on the same account as the read-only key, and, on binance.com, that its IP restriction includes the address Mishmar shows. If the key id was pasted wrong, on the **Keys** tab click **⋯** on the row, choose **Change key id**, paste the right one and click **Save and verify**: it can be changed until the key is active.
- **The check says `Accepted by Binance, co-signed by the agent's signer; the account cannot trade`**: the key works but Binance says it can't trade. Give it permission to trade futures at Binance, then verify again.
- **Position mode is one-way fails, with `Hedge mode: switch the account to one-way position mode`**: do that at Binance, then click **Save and verify** again, or on the agent's **Keys** tab click **⋯** on the key's row and choose **Verify**.
- **Account has margin fails, with `$0 available; Binance shows no USDⓈ-M Futures account behind this API key`**: the Binance account has never opened its USDⓈ-M Futures account. Open it at Binance (Derivatives, USDⓈ-M Futures, agree to the terms), transfer a USD stablecoin from the spot wallet into the futures wallet, then verify the key again.
- **Account has margin fails, with `$0 available`**: the futures wallet has no available balance in a USD stablecoin. Mishmar counts USDT, USDC and the other USD stablecoins at a dollar each; a balance in BNB, BTC or another coin is named in the check but not counted, unless the account is in Multi-Assets Mode, where Binance's own USD total is used. Add a stablecoin to the USDⓈ-M futures wallet at Binance, then verify the key again.
- **An order comes back `allow` with `(Not forwarded: …)`**: Mishmar's rules allowed it, but Binance refused it, in the words after `Not forwarded:`. A size under the symbol's minimum is the usual reason. Change the order and propose again.
- **`Agent …'s key on connected account … has expired; give the agent a new key`**: see [When the key expires](#when-the-key-expires).
- **`The account's plan holds 1 agent; delete one or move to a larger plan`**. The free plan holds one agent and one connected account, read and trade agents together, and **Add agent** is greyed out with the reason beside it. Delete one, or move to a larger plan.
- **An order comes back `rate_limited`: `The account's request budget for binance is spent for now; send it again in … s. Exits and cancels still go`.** Each plan has a share of what Binance lets one address send. The SDKs and `mishmar signer run` send the order again after the wait.
- **The connected account says New orders are blocked: Mishmar has lost the venue's stream of this account's fills and orders.** While Mishmar can't see the account's fills and orders, it blocks new risk. Closing positions still works. Trading starts again by itself once the stream is back; if it hasn't within a few minutes, tell us.

## When the key expires

Every trading key expires 100 days after it became active, on every venue. The date is under **Expires** on the agent's **Keys** tab. In the key's last week the date turns amber, the agent's page and its row in **Agents** warn, and Mishmar emails the account's owner and admins 7, 2 and 1 days before. Once the date passes, the key is **Expired**: Mishmar never signs with it again, and the agent can't trade on this account. Binance still lists the API key, but it can sign nothing without Mishmar's half.

Replace the key before then:

1. On the agent's page, click **Replace key**, in the warning or under **⋯** on the key's row.
2. Click **Add new key**. The current key keeps working.
3. The agent's running signer generates the new key within a few seconds and logs its public key.
4. Register the new public key at Binance as in step 3, paste the key id Binance issues into **Key id issued by Binance**, and click **Save and verify**. The agent's signer must be running.

The moment the new key is active, Mishmar revokes the old one, and the agent trades on through the change. An order the agent built with the old key in the seconds after can be refused; propose it again. Then delete the old key at Binance.

If the key has already expired, the warning offers **Make a new key**, with the same steps. The agent can't trade on this account until the new key is active.

## Taking the key away

On the agent's **Keys** tab, click **⋯** on the trading key's row and choose **Revoke**. Mishmar stops signing with it at once, and without Mishmar's half the key can sign nothing. Delete it at Binance as well, so it no longer appears there.
