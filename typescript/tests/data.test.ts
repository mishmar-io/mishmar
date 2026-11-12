// An agent's reads of connected-account data, and the API key they start from, against a stub engine that checks signatures the way the engine does.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { Server } from "node:http";
import { after, before, beforeEach, test } from "node:test";
import { AgentKey, Client, EngineError, engineUrlFor, newSecret, parseSecret, secret, verifyRequest } from "../src/index.ts";

const SEED = Uint8Array.from({ length: 32 }, (_, i) => i);
const KEY = AgentKey.fromSeed(SEED);
const KEY_ID = "key_k7f2q9x1m3n4_abcdefgh2345";
const API_KEY = "msk_testnet_k7f2q9x1m3n4_abcdefgh2345_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";
const PREFIX = "/v1";

test("a key of 0.19.0 names its engine and its key", () => {
  assert.equal(newSecret("testnet", KEY_ID, SEED), API_KEY);
  const p = parseSecret(API_KEY + "\n");
  assert.deepEqual([p.environment, p.accountId, p.keyId], ["testnet", "acc_k7f2q9x1m3n4", KEY_ID]);
  assert.equal(p.key.publicKeyB64, KEY.publicKeyB64);
  assert.equal(AgentKey.parse(API_KEY).publicKeyB64, KEY.publicKeyB64);
  assert.equal(engineUrlFor(p), "https://api.testnet.mishmar.io/a/acc_k7f2q9x1m3n4/v1");
  assert.equal(engineUrlFor(p, "http://localhost:8080/v1/"), "http://localhost:8080/v1");
  assert.throws(() => engineUrlFor(parseSecret(newSecret("self", "key_main_abcdefgh2345", SEED))), /"self"/);
});

test("older keys still parse and name nothing", () => {
  // A seed whose base64url is all underscores: the worst case for telling the formats apart.
  const seed = new Uint8Array(32).fill(0xff);
  const old = secret("msk_ab12cd", seed);
  assert.ok(old.includes("___"));
  const p = parseSecret(old);
  assert.deepEqual([p.environment, p.accountId, p.keyId], [undefined, undefined, undefined]);
  assert.equal(p.key.publicKeyB64, AgentKey.fromSeed(seed).publicKeyB64);
  assert.equal(parseSecret(newSecret("prod", "key_main_abcdefgh2345", seed)).keyId, "key_main_abcdefgh2345");
  assert.throws(() => engineUrlFor(p), /before 0\.19\.0/);
  assert.throws(() => Client.fromApiKey(old), /before 0\.19\.0/);
});

test("malformed secrets are refused", () => {
  const seedText = secret("", SEED).slice(1);
  for (const bad of ["", "msk_", "msk_a", "msk_a_b", "key_a_" + seedText, "msk_a_AAAA", "msk_a_" + seedText + "="]) {
    assert.throws(() => parseSecret(bad), /malformed agent secret/, bad);
  }
});

const AT = "2026-11-12T10:00:00Z";
const ACCOUNT = { id: "ta_1", name: "Main", venue: "hyperliquid", kind: "hyperliquid", status: "active", mode: "agents_only", traded: false, setup: { kind: "hyperliquid", network: "testnet" }, createdAt: AT, updatedAt: AT };
// The seven reads a hosted account's collector answers while its engine sleeps.
const ANSWERS: Record<string, unknown> = {
  "/agent/me": { agentId: "agt_1", agentName: "Reader", accountId: "acc_k7f2q9x1m3n4", keyId: KEY_ID, portfolioId: "pf_1" },
  "/agent/connected-accounts": [ACCOUNT],
  "/agent/connected-accounts/ta_1": ACCOUNT,
  "/agent/connected-accounts/ta_1/balances": { connectedAccountId: "ta_1", asOf: AT, source: "observed", balances: [{ asset: "USDC", wallet: "margin", amount: "1500.5", usdValue: "1500.5" }], equityUsd: "1500.5" },
  "/agent/connected-accounts/ta_1/positions": { connectedAccountId: "ta_1", asOf: AT, source: "observed", positions: [{ symbol: "BTC", side: "long", size: "0.01", entryPrice: "79500" }] },
  "/agent/connected-accounts/ta_1/transactions": {
    items: [
      { id: "tx_2", connectedAccountId: "ta_1", kind: "trade", at: AT, origin: { kind: "agent", agentName: "Reader", own: true } },
      { id: "tx_1", connectedAccountId: "ta_1", kind: "trade", at: AT, origin: { kind: "outside", own: false } },
    ],
    nextCursor: "c2",
  },
  "/agent/connected-accounts/ta_1/equity": { connectedAccountId: "ta_1", resolution: "hour", points: [{ at: AT, equityUsd: "1500.5", source: "observed" }] },
};

let server: Server;
let base: string;
let seen: string[] = [];
let others: string[] = [];

before(async () => {
  server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://x");
    const p = url.pathname.replace(PREFIX, "");
    const query = url.search.replace(/^\?/, "");
    seen.push(p + (query ? "?" + query : ""));
    const h = req.headers;
    const ok =
      h["x-mishmar-key-id"] === KEY_ID &&
      verifyRequest(KEY.publicKeyB64, { keyId: KEY_ID, method: req.method ?? "", path: p, query, nonce: String(h["x-mishmar-nonce"] ?? ""), signedAt: String(h["x-mishmar-signed-at"] ?? "") }, String(h["x-mishmar-signature"] ?? ""));
    const send = (status: number, body: unknown) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(body));
    };
    if (!ok) return send(401, { code: "unauthorized", message: "Invalid signature" });
    const answer = req.method === "GET" ? ANSWERS[p] : undefined;
    if (answer === undefined) {
      others.push(p);
      return send(404, { code: "not_found", message: "not a read the collector answers" });
    }
    send(200, answer);
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const addr = server.address();
  base = `http://127.0.0.1:${typeof addr === "object" && addr ? addr.port : 0}${PREFIX}`;
});

after(() => server.close());

beforeEach(() => {
  seen = [];
  others = [];
});

test("a read agent needs only its API key", async () => {
  process.env.MISHMAR_API_KEY = API_KEY;
  process.env.MISHMAR_ENGINE = base + "/";
  try {
    const client = Client.fromApiKey();
    assert.equal(client.keyId, KEY_ID);
    assert.equal(client.engineUrl, base);
    assert.equal((await client.me()).agentName, "Reader");
    assert.deepEqual((await client.connectedAccounts()).map((a) => a.id), ["ta_1"]);
    assert.equal((await client.connectedAccount("ta_1")).traded, false);
    const balances = await client.accountBalances("ta_1");
    assert.equal(balances.equityUsd, "1500.5");
    assert.equal(balances.balances[0]?.asset, "USDC");
    const at = new Date("2026-11-01T02:00:00+02:00");
    assert.equal((await client.accountBalances("ta_1", { at })).source, "observed");
    assert.equal((await client.accountPositions("ta_1", { at: "2026-11-01T00:00:00Z" })).positions[0]?.symbol, "BTC");
    const page = await client.accountTransactions("ta_1", { from: at, kind: "trade", limit: 2, cursor: "c1" });
    assert.equal(page.nextCursor, "c2");
    assert.deepEqual(page.items.map((t) => t.origin?.own), [true, false]);
    assert.equal((await client.accountEquity("ta_1", { from: "2026-11-01T00:00:00Z", resolution: "hour" })).points[0]?.equityUsd, "1500.5");
    assert.deepEqual(others, []);
    assert.deepEqual(seen, [
      "/agent/me",
      "/agent/connected-accounts",
      "/agent/connected-accounts/ta_1",
      "/agent/connected-accounts/ta_1/balances",
      "/agent/connected-accounts/ta_1/balances?at=2026-11-01T00%3A00%3A00Z",
      "/agent/connected-accounts/ta_1/positions?at=2026-11-01T00%3A00%3A00Z",
      "/agent/connected-accounts/ta_1/transactions?from=2026-11-01T00%3A00%3A00Z&kind=trade&cursor=c1&limit=2",
      "/agent/connected-accounts/ta_1/equity?from=2026-11-01T00%3A00%3A00Z&resolution=hour",
    ]);
  } finally {
    delete process.env.MISHMAR_API_KEY;
    delete process.env.MISHMAR_ENGINE;
  }
});

test("another key is refused", async () => {
  const other = newSecret("testnet", KEY_ID, Uint8Array.from({ length: 32 }, (_, i) => i + 1));
  await assert.rejects(Client.fromApiKey(other, { engineUrl: base }).connectedAccounts(), (err: unknown) => err instanceof EngineError && err.status === 401);
});

test("without a key the error says where it goes", () => {
  delete process.env.MISHMAR_API_KEY;
  assert.throws(() => Client.fromApiKey(), /MISHMAR_API_KEY/);
});
