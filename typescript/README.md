# mishmar (TypeScript)

[Mishmar](https://mishmar.io) is a risk management layer for trading bots
and AI agents: every order is checked against your limits before it reaches
the exchange, large orders wait for your approval, and no key can trade
alone.

This is the TypeScript client: build a proposal, sign it with the agent's
key, send it to the engine, read the decision. No runtime dependencies; Node
20 or later.

```sh
npm install mishmar
```

The package also puts `mishmar` on the path, so `npx -y mishmar mcp` runs
the MCP server and `npx -y mishmar signer run` the signer without the CLI
installed: the command runs the binary found through `MISHMAR_BIN`, the
PATH or `~/.mishmar/bin`, and when there is none fetches the pinned release
(v0.2.2) into `~/.mishmar/bin` first, checked against the release's
checksums.

```ts
import { AgentKey, Client } from "mishmar";

const key = AgentKey.parse("msk_…"); // shown once when the engine issued the key
const client = new Client("http://localhost:8080/v1", key, "key_…");

const decision = await client.propose({
  tradingAccountId: "ta_…", symbol: "BTC", side: "buy", size: "0.25",
  orderType: "market", validForMs: 5 * 60_000,
});
console.log(decision.verdict); // allow | require_approval | block

// Pull a resting order this agent placed through Mishmar: a cancel names the
// order by the clientOrderId its decision carries, and has no side or size.
const { clientOrderId } = decision.proposal as { clientOrderId: string };
await client.propose({
  tradingAccountId: "ta_…", symbol: "BTC", kind: "cancel",
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
  let d = await s.order({ tradingAccountId: "ta_…", symbol: "BTC", side: "buy", size: "0.001", price: "79500" });
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
