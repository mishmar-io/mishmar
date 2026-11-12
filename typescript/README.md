# mishmar (TypeScript)

[Mishmar](https://mishmar.io) is a risk management layer for trading bots
and AI agents: every order is checked against your limits before it reaches
the exchange, large orders wait for your approval, and no key can trade
alone.

This is the TypeScript client: build a proposal, sign it with the agent's
key, send it to the engine, read the decision; and read the connected
accounts the agent was given. No runtime dependencies; Node 20 or later.

```sh
npm install mishmar
```

The package also puts `mishmar` on the path, so `npx -y mishmar mcp` runs
the MCP server and `npx -y mishmar signer run` the signer without the CLI
installed: the command runs the binary found through `MISHMAR_BIN`, the
PATH or `~/.mishmar/bin`, and when there is none fetches the pinned release
(v0.3.0) into `~/.mishmar/bin` first, checked against the release's
checksums.

```ts
import { AgentKey, Client } from "mishmar";

const key = AgentKey.parse("msk_…"); // shown once when the engine issued the key
const client = new Client("http://localhost:8080/v1", key, "key_…");

const decision = await client.propose({
  connectedAccountId: "ta_…", symbol: "BTC", side: "buy", size: "0.25",
  orderType: "market", validForMs: 5 * 60_000,
});
console.log(decision.verdict); // allow | require_approval | block

// Pull a resting order this agent placed through Mishmar: a cancel names the
// order by the clientOrderId its decision carries, and has no side or size.
const { clientOrderId } = decision.proposal as { clientOrderId: string };
await client.propose({
  connectedAccountId: "ta_…", symbol: "BTC", kind: "cancel",
  targetClientOrderId: clientOrderId,
});
```

Sizes, prices and leverage are decimal **strings** and are signed exactly as
written: `"0.5"` and `"0.50"` are the same quantity but different signatures.
Never pass a number.

A call the engine did nothing with goes again by itself: `503 unavailable`
while the engine restarts and `429 rate_limited` (after its `Retry-After`),
for up to 45 s, then the last answer is thrown as `EngineError`. Nothing else
is retried. `new Client(url, key, keyId, { retries: false })` turns that off;
`{ retries: { budgetMs: 90_000 } }` tunes it.

## Reading connected accounts

An agent reads each connected account it was given whole: balances, the
venue's positions, transactions and value over time, whoever acted on the
account. Every read is signed with the agent's key and shows in the
account's access log. The agent's API key alone is enough: it names the
engine and the key id (`MISHMAR_ENGINE`, or `{ engineUrl }`, for a
self-hosted engine), so a read agent, which has no signer, needs nothing
else.

```ts
import { Client } from "mishmar";

const client = Client.fromApiKey(); // $MISHMAR_API_KEY
for (const account of await client.connectedAccounts()) {
  const snap = await client.accountBalances(account.id); // { at } for a past moment
  console.log(account.name, snap.equityUsd);
}

const page = await client.accountTransactions("ta_…", { from: "2026-11-01T00:00:00Z", kind: "trade", limit: 200 });
for (const t of page.items) console.log(t.at, t.symbol, t.origin?.own ? "mine" : (t.origin?.agentName ?? "outside"));
// page.nextCursor → { cursor } for the page after

const positions = await client.accountPositions("ta_…"); // the venue's net positions, whoever placed them
const series = await client.accountEquity("ta_…", { from: "2026-10-01T00:00:00Z", resolution: "day" });
```

`me()` says who the key belongs to and `connectedAccount(id)` reads one
account. Times are RFC 3339 strings or Dates. The answers are typed
(`BalanceSnapshot`, `AccountTransactionPage`, …, exported from the
package). `positions()` is something else: the agent's own book, what its
orders through Mishmar opened. `Signer` has the same reads, through its child.
Started with a read agent's key, the child polls nothing (it learns from
`GET /agent/me` that the agent reads) and refuses `order`, `cancel` and the
other calls a read agent does not make with a 403 `EngineError`, before
anything is sent.

Run the tests, which include the cross-language vectors in `../spec/vectors`:

```
npm install
npm test
```

## The signer as a child process

With the `mishmar` binary installed (or fetched with `downloadBinary`),
`Signer` runs it as a child process and the bot holds no secret: the child
has the agent's key and shares, signs each proposal, and sends it one-shot
where it can. Reads go through the child too.

```ts
import { Signer } from "mishmar";

const s = await new Signer().start(); // ~/.mishmar/agent.json
try {
  let d = await s.order({ connectedAccountId: "ta_…", symbol: "BTC", side: "buy", size: "0.001", price: "79500" });
  if (d.verdict === "require_approval") d = await s.wait(d.id);
  const { clientOrderId } = d.proposal as { clientOrderId: string };
  await s.cancel("ta_…", "BTC", clientOrderId);
} finally {
  await s.stop();
}
```

The child stops when this process ends, however it ends. To share one signer
among several processes, or keep it connected across restarts, run it on its
own (`mishmar signer run --local unix:/run/mishmar/signer.sock`) and attach
with `await Signer.connect("unix:/run/mishmar/signer.sock")`; `stop()` then
leaves it running. One signer runs per config, so a second `Signer` on a
config in use fails and names the process that holds it.

The guides at [docs.mishmar.io](https://docs.mishmar.io) set up the agent,
its trading keys and the signer.
