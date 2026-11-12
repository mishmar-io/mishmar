// The client against a stub engine on localhost, which checks signatures the way the engine does.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { IncomingMessage, Server, ServerResponse } from "node:http";
import { after, before, beforeEach, test } from "node:test";
import { AgentKey, Client, EngineError, fromWire, sign, signingBytes, verify, verifyRequest } from "../src/index.ts";
import type { WireProposal } from "../src/index.ts";

const KEY = AgentKey.fromSeed(Uint8Array.from({ length: 32 }, (_, i) => i));
const PREFIX = "/v1";

type Seen = { method: string; path: string; headers: Record<string, string>; body: unknown };
let seen: Seen[] = [];
let decisions: unknown[] = [];
let server: Server;
let base: string;

function send(res: ServerResponse, status: number, body?: unknown) {
  if (body === undefined) {
    res.writeHead(status);
    return res.end();
  }
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

async function readBody(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  return Buffer.concat(chunks);
}

function agentAuthenticated(req: IncomingMessage, headers: Record<string, string>, raw: Buffer): boolean {
  const url = new URL(req.url ?? "/", "http://x");
  return verifyRequest(
    KEY.publicKeyB64,
    {
      keyId: headers["x-mishmar-key-id"] ?? "",
      method: req.method ?? "",
      path: url.pathname.replace(PREFIX, ""),
      query: url.search.replace(/^\?/, ""),
      body: raw,
      nonce: headers["x-mishmar-nonce"] ?? "",
      signedAt: headers["x-mishmar-signed-at"] ?? "",
    },
    headers["x-mishmar-signature"] ?? "",
  );
}

before(async () => {
  server = createServer(async (req, res) => {
    const raw = await readBody(req);
    const body = raw.length ? (JSON.parse(raw.toString("utf8")) as unknown) : null;
    const headers = Object.fromEntries(Object.entries(req.headers).map(([k, v]) => [k, String(v)]));
    seen.push({ method: req.method ?? "", path: req.url ?? "", headers, body });
    const path = req.url ?? "";
    if (req.method === "POST" && path === `${PREFIX}/proposals`) {
      const { signature, ...unsigned } = body as WireProposal;
      if (!verify(KEY.publicKeyB64, signingBytes(fromWire(unsigned as WireProposal)), signature ?? "")) {
        return send(res, 401, { code: "unauthorized", message: "Invalid signature" });
      }
      return send(res, 200, { id: "dec_1", verdict: "require_approval", proposal: body });
    }
    if (!agentAuthenticated(req, headers, raw)) return send(res, 401, { code: "unauthorized", message: "Invalid signature" });
    if (path === `${PREFIX}/agent/signer/poll`) return send(res, 200, [{ id: "sr_1", scheme: "frost-ed25519" }]);
    if (path === `${PREFIX}/agent/signer/commitments`) {
      const { venueKeyId, count } = body as { venueKeyId: string; count?: number };
      return send(res, 200, { venueKeyId, scheme: "frost-ed25519", batchSize: 8, outstanding: count ?? 8, items: [] });
    }
    if (path.startsWith(`${PREFIX}/agent/connected-accounts/ta_1/venue-rules`)) return send(res, 200, { connectedAccountId: "ta_1", venue: "binance", symbols: [] });
    if (path.endsWith("/answer")) return send(res, 204);
    if (path.startsWith(`${PREFIX}/agent/decisions/`)) return send(res, 200, decisions.length > 1 ? decisions.shift() : decisions[0]);
    if (path.startsWith(`${PREFIX}/agent/decisions?`)) return send(res, 200, { items: decisions, nextCursor: null });
    if (path === `${PREFIX}/agent/positions`) return send(res, 200, []);
    send(res, 404, { code: "not_found", message: "no" });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const addr = server.address();
  base = `http://127.0.0.1:${typeof addr === "object" && addr ? addr.port : 0}${PREFIX}`;
});

after(() => server.close());

beforeEach(() => {
  seen = [];
  decisions = [{ id: "dec_1", verdict: "require_approval" }];
});

test("propose signs and sends", async () => {
  const client = new Client(base, KEY, "key_a");
  const d = await client.propose({ connectedAccountId: "rt_1", symbol: "BTC", side: "buy", size: "0.5", orderType: "market" });
  assert.equal(d.verdict, "require_approval");
  const [req] = seen;
  assert.equal(req?.method, "POST");
  assert.equal(req?.path, `${PREFIX}/proposals`);
  const body = req?.body as WireProposal;
  assert.equal(body.keyId, "key_a");
  assert.ok(body.signature);
});

test("a tampered proposal is refused", async () => {
  const client = new Client(base, KEY, "key_a");
  const p = sign({ connectedAccountId: "rt_1", symbol: "BTC", side: "buy", size: "0.5", price: "1", keyId: "key_a" }, KEY);
  await assert.rejects(client.propose({ ...p, size: "0.50" }), (err: unknown) => {
    assert.ok(err instanceof EngineError);
    assert.equal(err.status, 401);
    assert.equal(err.code, "unauthorized");
    return true;
  });
});

test("reads are signed with the agent key", async () => {
  const client = new Client(base, KEY, "key_a");
  assert.deepEqual(await client.positions(), []);
  const page = await client.decisions({ verdict: "require_approval", limit: 20 });
  assert.equal(page.items[0]?.id, "dec_1");
  const last = seen.at(-1);
  assert.equal(last?.path, `${PREFIX}/agent/decisions?verdict=require_approval&limit=20`);
  assert.equal(last?.headers["x-mishmar-key-id"], "key_a");
  assert.ok(last?.headers["x-mishmar-signature"]);
});

test("wait polls until resolved", async () => {
  decisions = [
    { id: "dec_1", verdict: "require_approval" },
    { id: "dec_1", verdict: "require_approval", approval: { status: "approved" } },
  ];
  const client = new Client(base, KEY, "key_a");
  const d = await client.wait("dec_1", { timeoutMs: 5_000, intervalMs: 10 });
  assert.equal(d.approval?.status, "approved");
  assert.equal(seen.filter((r) => r.method === "GET").length, 2);
});

test("another key is refused", async () => {
  const other = AgentKey.fromSeed(Uint8Array.from({ length: 32 }, (_, i) => i + 1));
  await assert.rejects(new Client(base, other, "key_a").decision("dec_1"), (err: unknown) => err instanceof EngineError && err.status === 401);
});

test("the signer channel", async () => {
  const client = new Client(base, KEY, "key_a");
  const requests = await client.signerPoll(1);
  assert.equal(requests[0]?.id, "sr_1");
  await client.signerAnswer("sr_1", { kind: "refusal", refusal: { reason: "not my order" } });
  const last = seen.at(-1);
  assert.equal(last?.path, `${PREFIX}/agent/signer/requests/sr_1/answer`);
  assert.equal((last?.body as { kind: string }).kind, "refusal");
});

test("one-shot reads", async () => {
  const client = new Client(base, KEY, "key_a");
  assert.equal((await client.venueRules("ta_1", "BTCUSDT")).venue, "binance");
  assert.equal(seen.at(-1)?.path, `${PREFIX}/agent/connected-accounts/ta_1/venue-rules?symbol=BTCUSDT`);
  assert.equal((await client.signerCommitments("vk_1", 2)).outstanding, 2);
  const last = seen.at(-1);
  assert.equal(last?.path, `${PREFIX}/agent/signer/commitments`);
  assert.deepEqual(last?.body, { venueKeyId: "vk_1", count: 2 });
});
