/**
 * The signer as a child process, or one already running.
 *
 * `Signer` starts `mishmar signer run --local …` with the agent's config and
 * talks to it over a Unix socket (or, on Windows, localhost with a token).
 * The child holds the agent's key and its halves of the venue keys; this
 * process holds no secret at all. An order is an intent: the child turns it
 * into a proposal signed with the agent's key, sends it one-shot where it can
 * build the venue's message, through the channel otherwise, and hands back
 * the decision as the engine recorded it. The agent's reads pass through the
 * child the same way.
 *
 *     const s = await new Signer().start();          // ~/.mishmar/agent.json, mishmar on PATH
 *     let d = await s.order({ tradingAccountId: "ta_…", symbol: "BTC", side: "buy", size: "0.001", price: "79500" });
 *     if (d.verdict === "require_approval") d = await s.wait(d.id);
 *     await s.stop();
 *
 * The child lives and dies with this process: it gets a pipe on stdin that
 * is never written to and stops when the pipe closes, so it does not outlive
 * a bot that crashed or was killed. A signer run on its own instead (systemd,
 * launchd, Docker: `mishmar signer run --local unix:/path`) is reached with
 * `Signer.connect`, which never starts or stops it; that is the way for
 * several processes of one agent, or to keep the signer connected across bot
 * restarts. One signer runs per config: a second one on the same config
 * stops at once and says which process holds it.
 *
 * The binary is found through `MISHMAR_BIN`, the PATH, or
 * `~/.mishmar/bin/<version>/mishmar`; `downloadBinary` fetches a release from
 * GitHub and checks it against the release's checksums.
 */

import { execFile, spawn } from "node:child_process";
import type { ChildProcess } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { constants as fsConstants } from "node:fs";
import { access, chmod, mkdir, mkdtemp, open, readdir, realpath, rm, writeFile } from "node:fs/promises";
import * as http from "node:http";
import * as net from "node:net";
import { arch, homedir, platform, tmpdir } from "node:os";
import * as path from "node:path";
import { EngineError, resolved } from "./client.ts";
import type { Decision, DecisionPage } from "./client.ts";

export const RELEASES = "https://github.com/mishmar-io/mishmar/releases/download";
/** The release the SDK fetches when it has to (the npm bin: `npx -y mishmar …`); bumped with each release. */
export const RELEASE = "v0.2.2";
const ENV_BINARY = "MISHMAR_BIN";
const ENV_LOCAL_TOKEN = "MISHMAR_LOCAL_TOKEN";

/** The release asset's os_arch for this machine: linux_amd64, darwin_arm64, … */
export function platformName(): string {
  const os = ({ linux: "linux", darwin: "darwin", win32: "windows" } as Record<string, string>)[platform()];
  const cpu = ({ x64: "amd64", arm64: "arm64" } as Record<string, string>)[arch()];
  if (!os || !cpu) throw new Error(`no mishmar release for ${platform()}/${arch()}`);
  return `${os}_${cpu}`;
}

function exe(name: string): string {
  return platform() === "win32" ? name + ".exe" : name;
}

async function executable(p: string | undefined): Promise<boolean> {
  if (!p) return false;
  try {
    await access(p, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}

/** Whether the file starts with `#!`: a script, never the mishmar binary itself. */
async function isScript(p: string): Promise<boolean> {
  const f = await open(p, "r");
  try {
    const { bytesRead, buffer } = await f.read(Buffer.alloc(2), 0, 2, 0);
    return bytesRead === 2 && buffer[0] === 0x23 && buffer[1] === 0x21;
  } finally {
    await f.close();
  }
}

/** What `findBinary` passes over on the PATH; the argument and `MISHMAR_BIN` are always taken as given. */
export interface FindBinaryOptions {
  /** Skip a `mishmar` that is a script (starts with `#!`): the binary is native, and a script by that name is a wrapper such as the npm or PyPI one. */
  skipScripts?: boolean;
  /** Real paths never to return: a wrapper's own, so it does not run itself. */
  exclude?: string[];
}

/**
 * Where the mishmar binary is: the argument, `MISHMAR_BIN`, the PATH, or a
 * release `downloadBinary` fetched. Throws with what to do when there is none.
 */
export async function findBinary(explicit?: string, options: FindBinaryOptions = {}): Promise<string> {
  for (const c of [explicit, process.env[ENV_BINARY]]) if (await executable(c)) return c as string;
  const name = exe("mishmar");
  for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
    if (!dir) continue;
    const c = path.join(dir, name);
    if (!(await executable(c))) continue;
    if (options.skipScripts && (await isScript(c))) continue;
    if (options.exclude?.length && options.exclude.includes(await realpath(c).catch(() => c))) continue;
    return c;
  }
  const home = path.join(homedir(), ".mishmar", "bin");
  try {
    for (const version of (await readdir(home)).sort().reverse()) {
      const c = path.join(home, version, name);
      if (await executable(c)) return c;
    }
  } catch {
    // no downloads yet
  }
  throw new Error(
    "no mishmar binary: install it from https://github.com/mishmar-io/mishmar/releases (or call downloadBinary(version)), put it on the PATH or in MISHMAR_BIN",
  );
}

/**
 * Fetch the release `version` (a tag, v0.1.0) for this machine into `destDir`
 * (default `~/.mishmar/bin/<version>`), after checking its SHA-256 against the
 * release's SHA256SUMS. Returns the binary's path.
 */
export async function downloadBinary(version: string, destDir?: string, fetchImpl: typeof fetch = fetch): Promise<string> {
  const name = `mishmar_${version}_${platformName()}`;
  const base = `${RELEASES}/${version}`;
  const sumsRes = await fetchImpl(`${base}/SHA256SUMS`);
  if (!sumsRes.ok) throw new Error(`${base}/SHA256SUMS: HTTP ${sumsRes.status}`);
  let want: string | undefined;
  for (const line of (await sumsRes.text()).split("\n")) {
    const parts = line.trim().split(/\s+/);
    if (parts.length === 2 && parts[1]?.replace(/^\*/, "") === `${name}.tar.gz`) want = parts[0]?.toLowerCase();
  }
  if (!want) throw new Error(`${name}.tar.gz is not in the release's SHA256SUMS`);
  const res = await fetchImpl(`${base}/${name}.tar.gz`);
  if (!res.ok) throw new Error(`${base}/${name}.tar.gz: HTTP ${res.status}`);
  const archive = Buffer.from(await res.arrayBuffer());
  const got = createHash("sha256").update(archive).digest("hex");
  if (got !== want) throw new Error(`${name}.tar.gz: checksum ${got} is not the release's ${want}`);
  const dir = destDir ?? path.join(homedir(), ".mishmar", "bin", version);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  const work = await mkdtemp(path.join(tmpdir(), "mishmar-"));
  try {
    const file = path.join(work, `${name}.tar.gz`);
    await writeFile(file, archive);
    await new Promise<void>((resolve, reject) =>
      execFile("tar", ["-xzf", file, "-C", dir, exe("mishmar")], (err) => (err ? reject(err) : resolve())),
    );
  } finally {
    await rm(work, { recursive: true, force: true });
  }
  const out = path.join(dir, exe("mishmar"));
  await chmod(out, 0o755);
  return out;
}

/** An order to propose. Sizes and prices are decimal strings as the venue quotes them. */
export interface OrderIntent {
  tradingAccountId: string;
  symbol: string;
  side: "buy" | "sell";
  size: string;
  /** limit (the default; needs price) or market (priced at the engine's mark). */
  orderType?: "limit" | "market";
  price?: string;
  reduceOnly?: boolean;
  leverage?: string;
  /** How long the proposal stands, at most 86400; default 60. */
  validForSeconds?: number;
  /** Leave the venue request to the engine and the signer's poller even where one-shot is possible. */
  channel?: boolean;
}

export interface SignerOptions {
  /** The signer's config file; default $MISHMAR_AGENT_CONFIG or ~/.mishmar/agent.json. */
  config?: string;
  /** The mishmar binary; default what findBinary finds. */
  binary?: string;
  /** Where the child listens: unix:/path, or host:port on a loopback address (with a token); default a private socket. */
  address?: string;
  startTimeoutMs?: number;
  timeoutMs?: number;
  logLevel?: "debug" | "info" | "warn" | "error";
  /** Where the child's log lines go; default stderr. */
  onLog?: (line: string) => void;
}

/** How to reach a signer that runs on its own (`Signer.connect`). */
export interface ConnectOptions {
  /** The signer's token, for a loopback host:port; default $MISHMAR_LOCAL_TOKEN. */
  token?: string;
  startTimeoutMs?: number;
  timeoutMs?: number;
}

type Query = Record<string, string | number | undefined>;

/**
 * The agent's signer, run as a child process and spoken to over its local API.
 * The child stops when this process exits, however it exits. `Signer.connect`
 * speaks to a signer that runs on its own instead.
 */
export class Signer {
  readonly options: SignerOptions;
  address: string | undefined;
  token: string | undefined;
  process: ChildProcess | undefined;
  #dir: string | undefined;
  #stderrTail: string[] = [];
  #attached = false;
  #ready = false;

  constructor(options: SignerOptions = {}) {
    this.options = options;
    this.address = options.address;
  }

  /**
   * Speak to a signer that already runs on its own (`mishmar signer run
   * --local ADDRESS`, kept up by systemd, launchd or Docker), and wait until it
   * answers. `address` is `unix:/path`, or a loopback host:port with the
   * signer's token. Nothing is started, and `stop` leaves the signer running.
   */
  static async connect(address: string, options: ConnectOptions = {}): Promise<Signer> {
    const s = new Signer({ address, startTimeoutMs: options.startTimeoutMs, timeoutMs: options.timeoutMs });
    s.#attached = true;
    if (!address.startsWith("unix:")) {
      s.token = options.token ?? process.env[ENV_LOCAL_TOKEN];
      if (!s.token) throw new Error(`${address}: a signer on a TCP port needs its token: pass { token } or set ${ENV_LOCAL_TOKEN}`);
    }
    return s.start();
  }

  /** Start the child and wait until its local API answers; for a signer from `connect`, only wait until it answers. */
  async start(): Promise<Signer> {
    if (this.#attached) {
      if (!this.#ready) {
        await this.#waitReady(() => undefined);
        this.#ready = true;
      }
      return this;
    }
    if (this.process) return this;
    const binary = await findBinary(this.options.binary);
    const env = { ...process.env };
    if (!this.address) {
      if (platform() === "win32") {
        this.address = `127.0.0.1:${await freePort()}`;
      } else {
        this.#dir = await mkdtemp(path.join(tmpdir(), "mishmar-"));
        this.address = "unix:" + path.join(this.#dir, "signer.sock");
      }
    }
    if (!this.address.startsWith("unix:")) {
      this.token ??= randomBytes(32).toString("base64url");
      env[ENV_LOCAL_TOKEN] = this.token;
    }
    // The child stops when its stdin closes: when this process exits, however
    // it exits. Nothing is ever written to it.
    const args = ["signer", "run", "--local", this.address, "--log-level", this.options.logLevel ?? "info", "--exit-on-stdin-close"];
    if (this.options.config) args.push("--config", this.options.config);
    const child = spawn(binary, args, { env, stdio: ["pipe", "ignore", "pipe"] });
    child.stdin?.on("error", () => {}); // a child that exits first may close it under us
    this.process = child;
    let exited: { code: number | null; signal: NodeJS.Signals | null } | undefined;
    child.on("exit", (code, signal) => {
      exited = { code, signal };
    });
    const onLog = this.options.onLog ?? ((line: string) => process.stderr.write(line + "\n"));
    let pending = "";
    child.stderr?.on("data", (chunk: Buffer) => {
      pending += chunk.toString("utf8");
      const lines = pending.split("\n");
      pending = lines.pop() ?? "";
      for (const line of lines) {
        this.#stderrTail.push(line);
        if (this.#stderrTail.length > 40) this.#stderrTail.shift();
        onLog(line);
      }
    });
    await this.#waitReady(() => exited);
    return this;
  }

  async #waitReady(exitedOf: () => { code: number | null; signal: NodeJS.Signals | null } | undefined): Promise<void> {
    const deadline = Date.now() + (this.options.startTimeoutMs ?? 15_000);
    for (;;) {
      const exited = exitedOf();
      if (exited) {
        const tail = this.#stderrTail.join("\n");
        await this.#cleanup();
        throw new Error(`the signer exited with status ${exited.code ?? exited.signal} before it was ready:\n${tail}`);
      }
      try {
        await this.#request("GET", "/health", { timeoutMs: 2_000 });
        return;
      } catch (err) {
        if (err instanceof EngineError) throw err;
        if (Date.now() >= deadline) {
          await this.stop();
          throw new Error(`the signer did not answer on ${this.address} within ${this.options.startTimeoutMs ?? 15_000} ms`);
        }
        await new Promise((r) => setTimeout(r, 100));
      }
    }
  }

  /**
   * Stop the child, gently then not: its stdin closes, which stops it, and it
   * gets SIGTERM, then SIGKILL after 5 s. A signer from `connect` keeps
   * running; this only forgets it was ready.
   */
  async stop(): Promise<void> {
    if (this.#attached) {
      this.#ready = false;
      return;
    }
    const child = this.process;
    if (!child) return;
    try {
      child.stdin?.destroy();
      if (child.exitCode === null && child.signalCode === null) {
        const gone = new Promise<void>((resolve) => child.once("exit", () => resolve()));
        child.kill("SIGTERM");
        const timer = new Promise<"timeout">((resolve) => setTimeout(() => resolve("timeout"), 5_000));
        if ((await Promise.race([gone, timer])) === "timeout") {
          child.kill("SIGKILL");
          await gone;
        }
      }
    } finally {
      await this.#cleanup();
    }
  }

  async #cleanup(): Promise<void> {
    this.process = undefined;
    if (this.#dir) {
      await rm(this.#dir, { recursive: true, force: true });
      this.#dir = undefined;
      this.address = undefined;
    }
  }

  // Proposals

  /** Send an intent (the local API's ProposeRequest) and get the decision back. */
  async propose(intent: Record<string, unknown>): Promise<Decision> {
    return (await this.#request("POST", "/propose", { body: intent })) as Decision;
  }

  /** Propose an order. A limit order needs a price; a market order is priced at the engine's mark. */
  async order(intent: OrderIntent): Promise<Decision> {
    return this.propose({ orderType: "limit", reduceOnly: false, validForSeconds: 60, channel: false, ...intent });
  }

  /** Cancel a resting order this agent placed, by the clientOrderId its decision carries. */
  async cancel(tradingAccountId: string, symbol: string, clientOrderId: string, validForSeconds = 60): Promise<Decision> {
    return this.propose({ tradingAccountId, symbol, kind: "cancel", targetClientOrderId: clientOrderId, validForSeconds });
  }

  // Reads, through the child

  async health(): Promise<Record<string, unknown>> {
    return (await this.#request("GET", "/health")) as Record<string, unknown>;
  }

  async decision(decisionId: string): Promise<Decision> {
    return (await this.#request("GET", `/decisions/${encodeURIComponent(decisionId)}`)) as Decision;
  }

  async decisions(query: { tradingAccountId?: string; verdict?: string; from?: string; to?: string; cursor?: string; limit?: number } = {}): Promise<DecisionPage> {
    return (await this.#request("GET", "/decisions", { query })) as DecisionPage;
  }

  async approvals(status?: string): Promise<Record<string, unknown>[]> {
    return (await this.#request("GET", "/approvals", { query: { status } })) as Record<string, unknown>[];
  }

  async positions(): Promise<Record<string, unknown>[]> {
    return (await this.#request("GET", "/positions")) as Record<string, unknown>[];
  }

  async pnl(query: { from?: string; to?: string } = {}): Promise<Record<string, unknown>> {
    return (await this.#request("GET", "/pnl", { query })) as Record<string, unknown>;
  }

  async tradingAccount(tradingAccountId: string): Promise<Record<string, unknown>> {
    return (await this.#request("GET", `/trading-accounts/${encodeURIComponent(tradingAccountId)}`)) as Record<string, unknown>;
  }

  async venueRules(tradingAccountId: string, symbol?: string): Promise<Record<string, unknown>> {
    return (await this.#request("GET", `/trading-accounts/${encodeURIComponent(tradingAccountId)}/venue-rules`, { query: { symbol } })) as Record<string, unknown>;
  }

  /** Poll a decision until it no longer waits for a person, or until timeoutMs. */
  async wait(decisionId: string, { timeoutMs = 60_000, intervalMs = 1_000 } = {}): Promise<Decision> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const d = await this.decision(decisionId);
      if (resolved(d) || Date.now() >= deadline) return d;
      await new Promise((r) => setTimeout(r, Math.min(intervalMs, Math.max(0, deadline - Date.now()))));
    }
  }

  // Plumbing

  #request(method: string, p: string, { body, query, timeoutMs }: { body?: unknown; query?: Query; timeoutMs?: number } = {}): Promise<unknown> {
    const address = this.address;
    if (!address) return Promise.reject(new Error("the signer is not started"));
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined) params.set(k, String(v));
    const queryText = params.toString();
    const data = body === undefined ? undefined : JSON.stringify(body);
    const headers: Record<string, string> = { Accept: "application/json" };
    if (data !== undefined) headers["Content-Type"] = "application/json";
    if (this.token) headers["Authorization"] = "Bearer " + this.token;
    const target: http.RequestOptions = address.startsWith("unix:")
      ? { socketPath: address.slice("unix:".length) }
      : { host: address.slice(0, address.lastIndexOf(":")), port: Number(address.slice(address.lastIndexOf(":") + 1)) };
    return new Promise((resolve, reject) => {
      const req = http.request(
        { ...target, method, path: p + (queryText ? "?" + queryText : ""), headers, timeout: timeoutMs ?? this.options.timeoutMs ?? 40_000 },
        (res) => {
          const chunks: Buffer[] = [];
          res.on("data", (c: Buffer) => chunks.push(c));
          res.on("end", () => {
            const text = Buffer.concat(chunks).toString("utf8");
            if ((res.statusCode ?? 0) >= 400) {
              let code = "http_error";
              let message = res.statusMessage || text.slice(0, 200);
              try {
                const parsed = JSON.parse(text) as { code?: unknown; message?: unknown };
                if (typeof parsed.code === "string") code = parsed.code;
                if (typeof parsed.message === "string") message = parsed.message;
              } catch {
                // not JSON: keep the text
              }
              reject(new EngineError(res.statusCode ?? 0, code, message));
              return;
            }
            try {
              resolve(text ? JSON.parse(text) : null);
            } catch (err) {
              reject(err);
            }
          });
        },
      );
      req.on("timeout", () => req.destroy(new Error("the signer did not answer in time")));
      req.on("error", reject);
      if (data !== undefined) req.write(data);
      req.end();
    });
  }
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, "127.0.0.1", () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
    srv.on("error", reject);
  });
}
