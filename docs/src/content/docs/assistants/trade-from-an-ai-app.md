---
title: Trade from Claude Code, Cursor or another AI app
description: Let an AI app on your own computer propose orders through Mishmar. Add Mishmar's trading server to the app, approve your computer in the browser after comparing a code, and authorise the trading key at the exchange. Your half of each trading key stays on your computer, and every order is checked against your limits.
sidebar:
  order: 3
lastUpdated: 2026-11-13
draft: true
---

When you are done, an AI app on your computer (Claude Code, Cursor, VS Code, Claude Desktop or any app that runs MCP servers) can propose orders on one of your connected accounts. Mishmar checks every order against your rules before it reaches the exchange; an order over your approval threshold waits for you in the console. The app never sees a key.

Trading needs Mishmar's signer on your computer. It holds your half of each trading key; Mishmar holds the other half and never sees yours, so an order goes out only when both have signed it. The app starts the signer itself the first time it needs it, and it keeps running in the background after you close the app, so an order you approve later still goes out.

## What stays on your computer

- **This computer's key**, which signs every proposal. Your computer makes it; Mishmar receives only its public half when you approve the computer, and never anything secret.
- **Your half of each trading key**, which Mishmar's signer on your computer makes together with Mishmar's half.

Both are files only you can read (mode 0600) in `~/.mishmar/apps/<app>`, a folder only you can open. Nothing is written before you approve the computer in the browser.

## What you need

- **A Mac or a Linux computer that you sit at, with a browser.** The approval happens in the browser on the same computer.
- **Node.js 20 or later**, for `npx`. Without it, [uv](https://docs.astral.sh/uv/) works too (`uvx`).
- **The owner or admin role** on the Mishmar account, and **a passkey or a linked wallet** on your Mishmar login: approving a computer asks for a fresh proof of one.
- **A connected account** to trade on, [at Hyperliquid](/hyperliquid/connect-an-account/) or [at Binance](/binance/connect-an-account/).
- **Your rules, set first.** [How do I let an AI agent trade for me safely?](/questions/how-do-i-let-an-ai-agent-trade-for-me-safely/) has a first set.

## 1. Add Mishmar's trading server to the app

Ask the app to set up trading with Mishmar, and it walks you through this step. Or do it yourself:

**Claude Code.** In a terminal:

```sh
claude mcp add --scope user mishmar-trade -- npx -y mishmar@0.5 mcp --app claude-code
```

Claude Code starts new servers only when it starts. Type `/exit`, then run `claude --continue` in the same folder to pick the conversation up again. If `claude` is not a command where you are (Claude Code inside an editor), put this in `.mcp.json` in the project folder instead, then reload the editor window and allow the new server when Claude Code asks:

```json
{"mcpServers":{"mishmar-trade":{"command":"npx","args":["-y","mishmar@0.5","mcp","--app","claude-code"]}}}
```

**Cursor.** Add the server to `~/.cursor/mcp.json` (keep any servers already there), then reload Cursor's servers under **Settings → MCP**, or restart Cursor, and start a new chat:

```json
{"mcpServers":{"mishmar-trade":{"command":"npx","args":["-y","mishmar@0.5","mcp","--app","cursor"]}}}
```

**VS Code.** In a terminal:

```sh
code --add-mcp '{"name":"mishmar-trade","type":"stdio","command":"npx","args":["-y","mishmar@0.5","mcp","--app","vscode"]}'
```

VS Code may ask you to trust the server. The server is there from your next message, not in the one that added it.

**Claude Desktop.** Open **Settings → Developer → Edit Config**, add this under `mcpServers`, save, then quit Claude completely (not only the window) and open it again:

```json
"mishmar-trade": {"command":"npx","args":["-y","mishmar@0.5","mcp","--app","claude-desktop"]}
```

Claude in a browser or on a phone can only read your accounts through Mishmar: trading needs the signer on a computer you own.

**Any other app.** Add a local (stdio) MCP server named `mishmar-trade` that runs `npx -y mishmar@0.5 mcp --app <your-app>`, with a short lowercase name for the app (`windsurf`). Restart the app if it does not load new servers by itself.

On Mishmar's testnet, add `--env testnet` at the end of the arguments (`"--env","testnet"` in JSON). Without Node.js, use `uvx --from "mishmar>=0.5,<0.6" mishmar mcp --app <your-app>` in the same place.

In every app, the new server can be used only after the app has restarted, or the conversation continues in a new session: tools added during a conversation never reach it.

## 2. Ask the app to set up trading

Say **set up trading with Mishmar**. The app calls one of Mishmar's tools, which starts Mishmar's signer on your computer, makes this computer's key and opens Mishmar in your browser. In the app you see something like:

```text
This computer is not signed in to Mishmar yet. I opened Mishmar in your browser to approve it: https://api.mishmar.io/oauth/authorize?…. Check the page shows this code: AS3N-N9TM. Approve only if it matches and you started this just now. Then ask me again. The link works until 09:40 UTC.
```

If no browser opened, open the link yourself. It works for ten minutes, and only in this computer's browser.

## 3. Approve this computer

Mishmar's page shows the app's name and your computer's name, as the program gave them, and a code. **Approve only if the code is the one the app showed, and you started this just now.** A code that differs means the request did not come from your app: decline it.

On the same page you choose the agent the app trades as (or a new one) and the connected account it trades on, and confirm with your passkey or wallet. Approving a computer for an app that already trades from another computer replaces that computer: its key stops working, and so do the trading keys it made.

Back in the app, ask again. The app is signed in, and Mishmar's signer on your computer makes the trading key with Mishmar within a few seconds.

## 4. Authorise the trading key at the exchange

The console then shows the one step left at the exchange, the same as for any agent:

- **Hyperliquid:** approve the API wallet the signer made, under the name the console shows, on Hyperliquid's API page with your master wallet. [Add an agent on Hyperliquid](/hyperliquid/add-an-agent/#3-approve-the-api-wallet-at-hyperliquid) has the details.
- **Binance:** register the public key the signer made as a self-generated Ed25519 API key, with trading on and withdrawals off, and enter the API key Binance gives in the console. [Add an agent on Binance](/binance/add-an-agent/) has the details.

You can also ask the app **what is my Mishmar trading status?**: it says what is left, with the address or the public key.

## Check it worked

- Ask the app for its Mishmar trading status. It answers `Ready to trade on …`, names the agent, this computer's key code and when the key expires, and says the signer runs in the background.
- In a terminal, `npx -y mishmar@0.5 daemon status` prints the background signer's version and each app's state.

## What you see when you trade

- The app proposes an order; Mishmar answers with a decision: sent, blocked by a rule (the rule is named), or waiting for your approval.
- A proposal from an app stands 15 minutes unless the app sets another time (up to a day). That is how long you have to approve one that waits for you.
- Once you approve, the order goes out as soon as the signer on your computer signs it, even if the app is closed by then. If the signer is not running (the computer restarted), the order waits for it until the proposal's deadline, and goes out when it is back, if your rules still allow it then.

## Stop or disconnect

- **Stop the signer for now:** `npx -y mishmar@0.5 daemon stop`. It finishes what it is signing, then exits. The next Mishmar tool any app uses starts it again.
- **End the app's trading for good:** disconnect this computer in the console. Its key stops working at once, and its trading keys with it. The app's next Mishmar tool says Mishmar no longer takes this computer's key; signing in again makes a new key, which you approve again.

## Not supported yet

- **Remote machines.** A machine you reach over SSH, a container, a dev container or a cloud workspace (Codespaces, Gitpod, a cloud shell) cannot sign in to trade yet: the app says so and starts nothing. Signing in by typing a code on another device will come later. You can still read your accounts from anywhere through Mishmar's hosted address.
- **Windows.** Mishmar's signer has no Windows release yet. Inside WSL, the Linux steps work.
- **Surviving a restart.** Nothing starts the signer when the computer starts. After a restart it runs again with the next Mishmar tool any app uses; orders you approve in between wait for it until their deadline.
- **The system keychain.** Keys are files only you can read, as for a bot's signer.

## When something goes wrong

**`Trading through Mishmar from this machine is not supported yet: it looks like an SSH session (SSH_CONNECTION).`** (or a container, or a cloud machine): the app runs on a machine you are not sitting at. Trade from an app on your own computer.

**`The person declined this computer's sign-in to Mishmar, so nothing was set up.`**: the request was declined on the page. Ask the app again to start a new one.

**`Nobody approved this computer's sign-in to Mishmar in time, so nothing was set up.`**: the link's ten minutes ran out. Ask the app again.

**`Mishmar no longer takes this computer's key for claude-code (Key revoked)`**: the computer was disconnected in the console, or its key expired. Ask again to sign in with a new key.

**`Mishmar's signer for this app is not running: another signer is running with …`**: a `mishmar signer run` was started by hand on the app's key file. Stop it; the background signer takes over.
