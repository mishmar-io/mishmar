---
title: Add a read agent
description: Give an assistant, a report or any program read access to your connected accounts with a Mishmar API key alone. No signer, no trading key, nothing that can trade. Add the agent, pick its accounts, then read them over MCP, from the Python or TypeScript SDK, or with the mishmar CLI.
sidebar:
  order: 2
lastUpdated: 2026-11-12
---

A **read agent** reads the connected accounts you give it, and nothing else: their balances, the venue's positions, their transactions and their value over time. It has a **Mishmar API key**, which signs every read so Mishmar knows each one comes from this agent, and no signer and no trading key, so it can never trade: Mishmar refuses its orders. Use one for an assistant you want to ask about your accounts, a report, a dashboard, or anything that only needs to look.

When you are done, the program you give the key to can read the accounts you picked, and every read it makes shows on the account's **Access** tab. To trade as well, add a trade agent instead: [on Hyperliquid](/hyperliquid/add-an-agent/) or [on Binance](/binance/add-an-agent/).

It takes about two minutes.

## What you need

- **A connected account**, [on Hyperliquid](/hyperliquid/connect-an-account/) or [on Binance](/binance/connect-an-account/). The dialog can connect one on the way.
- **The owner or admin role** on the Mishmar account. Approvers and viewers cannot add agents.
- **A passkey or a linked wallet on your Mishmar login.** Hosted Mishmar asks for a fresh proof of one before adding an agent. Add one under **Profile** in the console's sidebar: **Add passkey on this device** under **Passkeys**, or **Link this browser's wallet** under **Wallets**.
- **Room on your plan.** The free plan holds one agent, read and trade agents together.
- **Something to read with**: an MCP client and the [mishmar CLI](/start/install-the-cli/) (or `npx`/`uvx`, as in [the MCP setup](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/#without-installing-the-cli)), the Python or TypeScript SDK, or the CLI alone.

## 1. Add the agent

In the console, open **Agents** and click **Add agent**.

- **Name**: what you call the agent everywhere in Mishmar.
- **What it is for**: choose **Read only**. Nothing is picked until you choose.
- **Reads**: tick each connected account the agent may read (the only one there is is ticked for you), or click **Connect an account** to connect one without leaving the dialog. It reads each account whole: balances, positions, value and every transaction, who placed each one included.
- **Description** (optional): what it reads them for.

![Step 1 of 2, Add agent: the name Desk assistant, Read only chosen under What it is for, two connected accounts ticked under Reads with Connect an account below them, and a description](/screens/start/add-a-read-agent/add-agent.png)

Click **Add agent**. The console may ask you to confirm with your passkey or wallet first; the proof lasts ten minutes.

## 2. Copy its key

The dialog says the agent is ready and shows its **Mishmar API key**. **It is shown once.** Copy it before you close the dialog, with **Copy key**, or **Copy variable** for the line `MISHMAR_API_KEY=msk_…`. If you lose it, revoke it on the agent's **Keys** tab and issue another.

![Step 2 of 2: Desk assistant is ready, with its Mishmar API key (masked here) and the line MISHMAR_API_KEY=msk_…, each with a copy button, the accounts it may read, and the Finish button](/screens/start/add-a-read-agent/key.png)

Click **Finish**: the agent's page opens.

## 3. Read the accounts

Give the key to the program that reads, in its environment as `MISHMAR_API_KEY`. The key names the Mishmar it belongs to, so on hosted Mishmar that is all it needs; a self-hosted engine is named in `MISHMAR_ENGINE` as well. Treat the key like a password: it reads everything in the accounts you gave the agent.

### Over MCP, for an assistant

`mishmar mcp` is Mishmar's MCP server. Put the key in the server's `env`:

```json
{
  "mcpServers": {
    "mishmar": { "command": "mishmar", "args": ["mcp"], "env": { "MISHMAR_API_KEY": "msk_…" } }
  }
}
```

The first time it starts, the server checks the key with Mishmar and writes the agent's config to `~/.mishmar/agent.json` (mode 0600). If that file already belongs to another agent on this machine, give this one its own with `"args": ["mcp", "--config", "/home/you/.mishmar/agents/reader.json"]`. [Use Mishmar from Claude, Cursor or any MCP client](/assistants/use-mishmar-from-claude-cursor-or-any-mcp-client/) has the setup for each client.

These are the tools a read agent uses:

| Tool | What it reads |
| --- | --- |
| `get_identity` | Who the agent is: its id and name, its Mishmar account, whether it reads or trades, its capital when it has some, the id of its key. |
| `list_connected_accounts` | The connected accounts it was given, each with its name at the venue (`Spot`, `Perps`, `USDⓈ-M Futures`, or `Main` on a login with one account), its value in USD (and any asset with no price, in `unpricedAssets`), its status, and its connection: the venue login that holds it, which login it is there and how current its data is. |
| `get_connected_account` | One of them, with its connection's setup and no secret. |
| `get_account_balances` | The account's balances, each with its value in USD and the price used, and the account's value; now, or at a past moment (`at`). An asset with no price is named in `unpricedAssets` instead of being counted. |
| `get_account_positions` | The venue's net position in each market (a symbol with its market kind: `perp`, `spot`, …), whoever placed it; now, or at a past moment (`at`). |
| `list_account_transactions` | Trades, transfers, funding, fees and liquidations, newest first (`from`, `to`, `kind`, `limit`, `cursor`). A trade's `origin` says who placed it. A transfer to or from another account of the same connection names it in `counterpartConnectedAccountId` and shows on both: it moves each account's value by the amount and the connection's by nothing. |
| `get_account_equity` | The account's value in USD over time, oldest first (`from`, `to`, `resolution`: `minute`, `hour` or `day`). |

An account is one pool of money at a venue, and the venue login that holds it is its **connection**: a Binance login has a **Spot** and a **USDⓈ-M Futures** account, a Hyperliquid address has **Perps** and **Spot**. Every balance, position and transaction belongs to one account, and a connection's value is the sum of its accounts'. You give an agent accounts, so it may read one of a login's accounts and not the other: each account says how many its login has (`connection.accountCount`), and an agent that sees fewer adds up only the accounts it can see, not the login's value.

The server lists its other tools too. For a read agent each of them answers `… is not open to a read agent, and nothing was sent`, without calling Mishmar, and the signer inside the server opens no channel to Mishmar and syncs no keys: the reads above are the only calls it makes.

### From code

The SDKs' `Client` reads with the key alone, with no signer and no binary:

```python
from mishmar import Client

client = Client.from_api_key()  # MISHMAR_API_KEY
for account in client.connected_accounts():
    login = account["connection"]  # the venue login that holds it
    print(account["venue"], login["name"], account["name"], account.get("equityUsd"))
    snap = client.account_balances(account["id"])  # at="…" for a past moment

page = client.account_transactions("ta_…", since="2026-11-01T00:00:00Z", kind="trade", limit=200)
for t in page["items"]:
    origin = t.get("origin", {})  # who placed a trade
    print(t["at"], t.get("symbol"), "mine" if origin.get("own") else origin.get("agentName", "outside"))
```

In TypeScript it is the same in camelCase, with `from` and `to` where Python says `since` and `until`:

```ts
import { Client } from "mishmar";

const client = Client.fromApiKey(); // MISHMAR_API_KEY
for (const account of await client.connectedAccounts()) {
  const login = account.connection; // the venue login that holds it
  console.log(account.venue, login.name, account.name, account.equityUsd);
  const snap = await client.accountBalances(account.id); // { at } for a past moment
}
const page = await client.accountTransactions("ta_…", { from: "2026-11-01T00:00:00Z", kind: "trade", limit: 200 });
```

Each SDK also has `me()`, `connected_account(id)` (`connectedAccount`), `account_positions` (`accountPositions`) and `account_equity` (`accountEquity`). A page of transactions carries `nextCursor` when there is more; pass it back as `cursor`.

### From the command line

```sh
export MISHMAR_API_KEY=msk_…
mishmar read accounts
```

```text
ta_…  Hyperliquid · Main · Perps · 0x12ab…9f3c  790.12 USD  active  read only
ta_…  Hyperliquid · Main · Spot · 0x12ab…9f3c   41.5 USD    active  read only
      total                                     831.62 USD as of 2026-11-12T10:00:00Z
      balances current as of 2026-11-12T10:00:00Z; positions current as of 2026-11-12T10:00:00Z; equity current as of 2026-11-12T10:00:00Z; transactions current as of 2026-11-12T09:58:00Z
```

Each line is one account: its id, then the venue, the connection's name, the account's name and the venue's own identifier for the login, shortened, then its value. On a login with a single account the account's name is left out. The last word is `traded` instead of `read only` when an agent holds a trading key on the account. Under a login's accounts, `total` is their sum; when the agent was given fewer than all of them, that line says how many it sees (`2 of 3 accounts, total of these`) and the sum is of those alone. The last line says how current the connection's data is. The other reads take the account's id:

```sh
mishmar read balances ta_…                       # --at 2026-11-01 for a past moment
mishmar read positions ta_…
mishmar read transactions ta_… --kind trade --from 2026-11-01
mishmar read equity ta_… --from 2026-10-01 --resolution day
mishmar read agent                               # who the key belongs to
```

Times are RFC 3339 (`2026-11-12T09:30:00Z`) or a date. `--json` prints Mishmar's answer as it came.

## Check it worked

- **Agents** lists the agent with **Read only** under **Permission**. On its page, **Overview** says **For** **Read only**, and **Connected accounts** lists its accounts with **Reads** under **Access**.
- After the program has read, the agent's **Access** tab lists each read, and so does the **Access** tab of each account it read.

![The read agent's page: Overview with For Read only, and Connected accounts listing its two accounts, each with Reads under Access and a Remove button, and Give access above them](/screens/start/add-a-read-agent/agent-page.png)

![The agent's Access tab, What it read: each read with its time, the account, what it read (its list of accounts, balances, positions, transactions, value over time), the range and the key it used](/screens/start/add-a-read-agent/agent-access.png)

## Give it more accounts, or take one away

On the agent's page, under **Overview**, **Connected accounts** has **Give access**: pick the account and click **Give access**. **Remove** on an account's row takes it away (**Remove access** to confirm): its reads of that account are refused from then on. What it already read can't be taken back.

To stop the agent reading altogether, revoke its Mishmar API key on its **Keys** tab, or delete the agent.

## When something goes wrong

- **`Say what the agent is for`** under **What it is for**: choose **Read only** (or **Trade**).
- **`Pick at least one account`** under **Reads**: tick an account, or connect one.
- **`No connected account yet. Connect one first.`**: click **Connect an account**, or connect one [on Hyperliquid](/hyperliquid/connect-an-account/) or [on Binance](/binance/connect-an-account/) first.
- **`Agent … was not given this connected account`**: the agent reads only the accounts it was given. Give it the account on its page, or read one it has.
- **`Agent … is a read agent: it cannot propose orders`**, or from `mishmar propose`, **`agent … (…) is a read agent: it proposes nothing, and nothing was sent`**: a read agent never trades. Add a trade agent for that.
- **`no agent API key: pass api_key or set MISHMAR_API_KEY`** (Python), **`no agent API key: pass one or set MISHMAR_API_KEY`** (TypeScript), or **`no agent key: put the agent's Mishmar API key from the console in MISHMAR_API_KEY, …`** (CLI): the key is not in the environment of the program that reads. Export it there.
- **`… does not accept this key (…): it may have been revoked; issue a new one in the console`**: the key is not one Mishmar knows, or was revoked. Copy it again, or revoke it on the **Keys** tab and issue another.
- **`… holds another key than MISHMAR_API_KEY; remove the file to set the agent up again`**: the MCP server found another agent's config at the default path. Give this agent its own with `--config`, as above.
- **A read for a past moment comes back not found**: Mishmar has nothing that far back, either because it connected the account later or because the plan keeps 90 days. [What Mishmar keeps of your accounts](/concepts/what-mishmar-keeps-of-your-accounts/) has how far back each kind goes.
- **`The account's plan holds 1 agent; delete one or move to a larger plan`**, or **Add agent** is greyed out with the reason beside it: the free plan holds one agent, read and trade agents together. Delete one, or move to a larger plan.
