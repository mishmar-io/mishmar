// Retries against an engine that answers what it was told to: 503 and 429 go
// again, 413 does not, and the budget ends them.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { IncomingMessage, Server } from "node:http";
import { after, before, beforeEach, test } from "node:test";
import { AgentKey, Client, DEFAULT_RETRIES, EngineError, retryWait } from "../src/index.ts";
import type { Retries } from "../src/index.ts";

const KEY = AgentKey.fromSeed(Uint8Array.from({ length: 32 }, (_, i) => i));
const CODES: Record<number, string> = { 503: "unavailable", 429: "rate_limited", 413: "too_large" };

let script: number[] = []; // the status of each attempt in turn; past the end, 200 with []
let retryAfter: Record<number, string> = {};
let attempts = 0;
let nonces: string[] = [];
let bodies: string[] = [];
let server: Server;
let base: string;

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  return Buffer.concat(chunks).toString("utf8");
}

before(async () => {
  server = createServer(async (req, res) => {
    bodies.push(await readBody(req));
    const i = attempts++;
    nonces.push(String(req.headers["x-mishmar-nonce"] ?? ""));
    const status = script[i];
    if (status !== undefined) {
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (retryAfter[i] !== undefined) headers["Retry-After"] = retryAfter[i];
      res.writeHead(status, headers);
      return res.end(JSON.stringify({ code: CODES[status] ?? "other", message: "scripted" }));
    }
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end("[]");
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const addr = server.address();
  base = `http://127.0.0.1:${typeof addr === "object" && addr ? addr.port : 0}/v1`;
});

after(() => server.close());

beforeEach(() => {
  script = [];
  retryAfter = {};
  attempts = 0;
  nonces = [];
  bodies = [];
});

function client(retries?: Retries | false): { c: Client; waits: number[] } {
  const waits: number[] = [];
  const c = new Client(base, KEY, "key_a", {
    retries,
    sleep: async (ms) => {
      waits.push(ms);
    },
  });
  return { c, waits };
}

async function refused(p: Promise<unknown>): Promise<EngineError> {
  try {
    await p;
  } catch (err) {
    assert.ok(err instanceof EngineError);
    return err;
  }
  assert.fail("the call was not refused");
}

test("503 twice then 200, each attempt signed anew", async () => {
  script = [503, 503];
  const { c, waits } = client();
  assert.deepEqual(await c.positions(), []);
  assert.equal(attempts, 3);
  assert.equal(waits.length, 2);
  for (const w of waits) assert.ok(w >= DEFAULT_RETRIES.minWaitMs / 2 && w <= DEFAULT_RETRIES.maxWaitMs, `wait ${w}`);
  assert.ok((waits[1] ?? 0) >= (waits[0] ?? 0) / 2, `waits ${waits} do not grow`);
  assert.equal(new Set(nonces).size, 3, "a retry reused a nonce");
});

test("429 waits the seconds in Retry-After", async () => {
  script = [429];
  retryAfter = { 0: "3" };
  const { c, waits } = client();
  assert.deepEqual(await c.positions(), []);
  assert.deepEqual(waits, [3000]);
});

test("a Retry-After past the budget is not waited for", async () => {
  script = [429];
  retryAfter = { 0: "120" };
  const { c, waits } = client();
  const err = await refused(c.positions());
  assert.equal(err.status, 429);
  assert.equal(err.code, "rate_limited");
  assert.equal(err.retryAfter, "120");
  assert.equal(attempts, 1);
  assert.deepEqual(waits, []);
});

test("413 is not retried", async () => {
  script = [413];
  const { c, waits } = client();
  const err = await refused(c.positions());
  assert.equal(err.status, 413);
  assert.equal(err.code, "too_large");
  assert.equal(attempts, 1);
  assert.deepEqual(waits, []);
});

test("the budget runs out and the last answer is thrown", async () => {
  script = [503, 503, 503, 503, 503, 503, 503, 503];
  const { c, waits } = client({ budgetMs: 1, minWaitMs: 10, maxWaitMs: 20 });
  const err = await refused(c.positions());
  assert.equal(err.status, 503);
  assert.equal(err.code, "unavailable");
  assert.equal(attempts, 1);
  assert.deepEqual(waits, []);
});

test("false turns retries off", async () => {
  script = [503];
  const { c, waits } = client(false);
  const err = await refused(c.positions());
  assert.equal(err.status, 503);
  assert.equal(attempts, 1);
  assert.deepEqual(waits, []);
});

test("a proposal goes again too, the same bytes", async () => {
  script = [503];
  const { c, waits } = client();
  // The flaky engine answers [] to the retried POST, which is not a decision; the retry is the point.
  await c.propose({ tradingAccountId: "rt_1", symbol: "BTC", side: "buy", size: "0.5", price: "1" });
  assert.equal(attempts, 2);
  assert.equal(waits.length, 1);
  assert.ok(bodies[0]);
  assert.equal(bodies[0], bodies[1]);
});

test("the wait policy", () => {
  assert.equal(retryWait({}, 0, 500, null, 0), undefined);
  assert.equal(retryWait({}, 0, 503, null, DEFAULT_RETRIES.budgetMs), undefined);
  const first = retryWait({}, 0, 503, null, 0);
  assert.ok(first !== undefined && first >= 125 && first <= 250, `first ${first}`);
  const late = retryWait({}, 30, 503, null, 0);
  assert.ok(late !== undefined && late >= 4000 && late <= 8000, `late ${late}`);
  assert.ok(retryWait({}, 0, 429, "garbage", 0) !== undefined);
  assert.equal(retryWait({}, 0, 429, "Thu, 01 Jan 1970 00:00:00 GMT", 0), 0);
  const dated = retryWait({}, 0, 429, new Date(Date.now() + 2000).toUTCString(), 0);
  assert.ok(dated !== undefined && dated > 0 && dated <= 2000, `dated ${dated}`);
});
