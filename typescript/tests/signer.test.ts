// The Signer against a fake mishmar binary: a script that serves the local API the way the real signer does.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { after, before, test } from "node:test";
import { EngineError, Signer, downloadBinary, findBinary, platformName } from "../src/index.ts";

const FAKE = `#!/usr/bin/env node
// A stand-in for \`mishmar signer run --local ADDR\`: serves the local API.
const http = require("node:http");
const args = process.argv.slice(2);
const addr = args[args.indexOf("--local") + 1];
const config = args.includes("--config") ? args[args.indexOf("--config") + 1] : "";
const token = process.env.MISHMAR_LOCAL_TOKEN || "";
if (process.env.FAKE_EXIT) { process.stderr.write("fake signer: refusing to start\\n"); process.exit(3); }
const send = (res, status, body) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(body)); };
const srv = http.createServer((req, res) => {
  if (!addr.startsWith("unix:") && req.headers.authorization !== "Bearer " + token) return send(res, 401, { code: "unauthorized", message: "token" });
  const p = req.url.split("?")[0];
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    if (req.method === "GET" && p === "/health") return send(res, 200, { agentId: "agt_1", keyId: "key_1", engine: "http://e/v1", venueKeys: ["vk_1"], config });
    if (req.method === "GET" && p === "/positions") return send(res, 200, [{ symbol: "BTC", size: "0.001" }]);
    if (req.method === "GET" && p === "/decisions/dec_bad") return send(res, 422, { code: "validation", message: "no such decision" });
    if (req.method === "GET" && p.startsWith("/decisions/")) return send(res, 200, { id: p.slice("/decisions/".length), verdict: "allow", query: req.url });
    if (req.method === "GET" && p === "/decisions") return send(res, 200, { items: [], query: req.url });
    if (req.method === "POST" && p === "/propose") return send(res, 200, { id: "dec_1", verdict: "allow", intent: JSON.parse(body || "{}") });
    send(res, 404, { code: "not_found", message: p });
  });
});
if (addr.startsWith("unix:")) srv.listen(addr.slice(5)); else srv.listen(Number(addr.slice(addr.lastIndexOf(":") + 1)), addr.slice(0, addr.lastIndexOf(":")));
process.stderr.write("fake signer: local API listening on " + addr + "\\n");
`;

let dir: string;
let fake: string;

before(async () => {
  dir = await mkdtemp(path.join(tmpdir(), "mishmar-signer-test-"));
  fake = path.join(dir, "mishmar");
  await writeFile(fake, FAKE);
  await chmod(fake, 0o755);
});

after(async () => {
  await rm(dir, { recursive: true, force: true });
});

test("the Signer runs the binary and proposes through it", async () => {
  const config = path.join(dir, "agent.json");
  await writeFile(config, "{}");
  const logs: string[] = [];
  const s = new Signer({ config, binary: fake, startTimeoutMs: 10_000, onLog: (l) => logs.push(l) });
  await s.start();
  try {
    assert.ok(s.address?.startsWith("unix:"));
    const h = await s.health();
    assert.equal(h.agentId, "agt_1");
    assert.equal(h.config, config);
    const d = (await s.order({ tradingAccountId: "ta_1", symbol: "BTC", side: "buy", size: "0.001", price: "79500", validForSeconds: 30 })) as unknown as { intent: unknown; verdict: string };
    assert.equal(d.verdict, "allow");
    assert.deepEqual(d.intent, { orderType: "limit", reduceOnly: false, validForSeconds: 30, channel: false, tradingAccountId: "ta_1", symbol: "BTC", side: "buy", size: "0.001", price: "79500" });
    const c = (await s.cancel("ta_1", "BTC", "msh-1-abc")) as unknown as { intent: { kind: string; targetClientOrderId: string } };
    assert.equal(c.intent.kind, "cancel");
    assert.equal(c.intent.targetClientOrderId, "msh-1-abc");
    assert.deepEqual(await s.positions(), [{ symbol: "BTC", size: "0.001" }]);
    assert.equal((await s.decision("dec_7")).id, "dec_7");
    assert.match(String((await s.decisions({ tradingAccountId: "ta_1", limit: 5 })).query), /tradingAccountId=ta_1/);
    await assert.rejects(s.decision("dec_bad"), (err: unknown) => err instanceof EngineError && err.status === 422 && err.code === "validation");
    assert.equal((await s.wait("dec_7", { timeoutMs: 1000 })).id, "dec_7");
    assert.ok(logs.some((l) => l.includes("local API listening")));
  } finally {
    const child = s.process;
    await s.stop();
    assert.ok(child && (child.exitCode !== null || child.signalCode !== null), "the child was stopped");
    assert.equal(s.process, undefined);
    assert.equal(s.address, undefined);
  }
});

test("the Signer over localhost needs its token", async () => {
  const port = await new Promise<number>((resolve) => {
    const srv = createServer();
    srv.listen(0, "127.0.0.1", () => {
      const p = (srv.address() as { port: number }).port;
      srv.close(() => resolve(p));
    });
  });
  const s = new Signer({ binary: fake, address: `127.0.0.1:${port}` });
  await s.start();
  try {
    assert.ok(s.token && s.token.length > 20);
    assert.equal((await s.health()).agentId, "agt_1");
    const saved = s.token;
    s.token = "wrong";
    await assert.rejects(s.health(), (err: unknown) => err instanceof EngineError && err.status === 401);
    s.token = saved;
  } finally {
    await s.stop();
  }
});

test("the Signer reports a child that exits", async () => {
  const saved = process.env.FAKE_EXIT;
  process.env.FAKE_EXIT = "1";
  try {
    await assert.rejects(new Signer({ binary: fake }).start(), /status 3[\s\S]*refusing to start/);
  } finally {
    if (saved === undefined) delete process.env.FAKE_EXIT;
    else process.env.FAKE_EXIT = saved;
  }
});

test("findBinary looks in the right places", async () => {
  const savedBin = process.env.MISHMAR_BIN;
  const savedPath = process.env.PATH;
  try {
    delete process.env.MISHMAR_BIN;
    process.env.PATH = path.join(dir, "nowhere");
    assert.equal(await findBinary(fake), fake);
    process.env.MISHMAR_BIN = fake;
    assert.equal(await findBinary(), fake);
    delete process.env.MISHMAR_BIN;
    process.env.PATH = dir;
    assert.equal(await findBinary(), fake);
  } finally {
    if (savedBin === undefined) delete process.env.MISHMAR_BIN;
    else process.env.MISHMAR_BIN = savedBin;
    process.env.PATH = savedPath;
  }
});

test("downloadBinary checks the release checksum", async () => {
  const src = path.join(dir, "release");
  await mkdir(src, { recursive: true });
  await writeFile(path.join(src, "mishmar"), "#!/bin/sh\necho mishmar\n");
  await chmod(path.join(src, "mishmar"), 0o755);
  const name = `mishmar_v0.1.0_${platformName()}`;
  const archivePath = path.join(dir, `${name}.tar.gz`);
  await new Promise<void>((resolve, reject) => execFile("tar", ["-czf", archivePath, "-C", src, "mishmar"], (err) => (err ? reject(err) : resolve())));
  const archive = await readFile(archivePath);
  const sums = `${createHash("sha256").update(archive).digest("hex")}  ${name}.tar.gz\n0000  other.tar.gz\n`;
  const fetched: string[] = [];
  const fakeFetch = (async (url: string | URL | Request) => {
    const u = String(url);
    fetched.push(u);
    if (u.endsWith("SHA256SUMS")) return new Response(sums);
    if (u.endsWith(`${name}.tar.gz`)) return new Response(archive);
    return new Response("nope", { status: 404 });
  }) as typeof fetch;
  const out = await downloadBinary("v0.1.0", path.join(dir, "bin"), fakeFetch);
  assert.equal(await readFile(out, "utf8"), "#!/bin/sh\necho mishmar\n");
  assert.deepEqual(fetched, [
    "https://github.com/mishmar-io/mishmar/releases/download/v0.1.0/SHA256SUMS",
    `https://github.com/mishmar-io/mishmar/releases/download/v0.1.0/${name}.tar.gz`,
  ]);
  const tampered = (async (url: string | URL | Request) =>
    String(url).endsWith("SHA256SUMS") ? new Response(sums) : new Response(Buffer.concat([archive, Buffer.from("x")]))) as typeof fetch;
  await assert.rejects(downloadBinary("v0.1.0", path.join(dir, "bin2"), tampered), /checksum/);
});
