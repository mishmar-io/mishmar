/**
 * A small client for the engine, on fetch.
 *
 * Proposals go to POST /proposals with the signature in the body. Everything
 * else an agent may call (its own decisions, approvals, positions, PnL,
 * trading accounts, and the signer channel) is under /agent and signed with the
 * agentAuth headers, so a bot can function without an operator token and
 * cannot act on the account.
 */

import type { AgentKey } from "./agentkey.ts";
import { sign, toWire } from "./proposal.ts";
import type { Proposal } from "./proposal.ts";

/** A decision as the engine returns it; the contract (spec/openapi.yaml) has the full shape. */
export type Decision = {
  id: string;
  verdict: "allow" | "require_approval" | "block";
  approval?: { status?: string } & Record<string, unknown>;
} & Record<string, unknown>;

/** A page of decisions. */
export type DecisionPage = { items: Decision[]; nextCursor?: string | null } & Record<string, unknown>;

/** A request the agent's signer must answer (SignRequest in the contract). */
export type SignRequest = { id: string; scheme: string } & Record<string, unknown>;

/** The signer's answer (SignerAnswer in the contract). */
export type SignerAnswer = { kind: "share" | "signature" | "venue_response" | "refusal" } & Record<string, unknown>;

const RESOLVED_APPROVAL = new Set(["approved", "rejected", "expired"]);

/** The engine answered with an error: the HTTP status and the code and message from its body. */
export class EngineError extends Error {
  readonly status: number;
  readonly code: string;
  /** The `Retry-After` header of a 429, when the engine sent one. */
  readonly retryAfter?: string;

  constructor(status: number, code: string, message: string, retryAfter?: string) {
    super(`${status} ${code}: ${message}`);
    this.name = "EngineError";
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

/**
 * How a call the engine did nothing with goes again: 503 `unavailable` while
 * the engine restarts (its drain is 28 s) and 429 `rate_limited`. The same
 * request is sent again after the seconds in `Retry-After` when the engine
 * names them, otherwise after a wait that doubles from `minWaitMs` to
 * `maxWaitMs` with jitter, until `budgetMs` have passed since the first
 * attempt; then the last answer is thrown. Nothing else is retried: a 413
 * `too_large` would come back the same, and any other answer means the
 * request was taken. The defaults cover a restart.
 */
export interface Retries {
  budgetMs?: number;
  minWaitMs?: number;
  maxWaitMs?: number;
}

export const DEFAULT_RETRIES: Required<Retries> = { budgetMs: 45_000, minWaitMs: 250, maxWaitMs: 8_000 };

/** The answers that mean the request did nothing and may go again. */
const RETRYABLE = new Set([503, 429]);

export interface ClientOptions {
  timeoutMs?: number;
  /** Retries of a call the engine did nothing with; on by default, `false` turns them off. */
  retries?: Retries | false;
  /** For tests: a fetch to use instead of the global one. */
  fetch?: typeof fetch;
  /** For tests: how to wait between attempts instead of a timer. */
  sleep?: (ms: number) => Promise<void>;
}

type Query = Record<string, string | number | undefined>;

/**
 * Milliseconds to wait before attempt `attempt + 1` (from 0) after an answer
 * with the status and Retry-After header, `elapsedMs` after the first attempt
 * began; `undefined` means stop and throw the answer.
 */
export function retryWait(retries: Retries, attempt: number, status: number, retryAfter: string | null, elapsedMs: number): number | undefined {
  if (!RETRYABLE.has(status)) return undefined;
  const budget = retries.budgetMs ?? DEFAULT_RETRIES.budgetMs;
  const minWait = retries.minWaitMs ?? DEFAULT_RETRIES.minWaitMs;
  const maxWait = Math.max(retries.maxWaitMs ?? DEFAULT_RETRIES.maxWaitMs, minWait);
  let wait = parseRetryAfter(retryAfter);
  if (wait === undefined) {
    const step = Math.min(minWait * 2 ** Math.min(attempt, 20), maxWait);
    wait = step / 2 + Math.random() * (step / 2);
  }
  return elapsedMs + wait > budget ? undefined : wait;
}

function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined;
  if (/^\d+$/.test(value.trim())) return Number(value) * 1000;
  const at = Date.parse(value);
  return Number.isNaN(at) ? undefined : Math.max(at - Date.now(), 0);
}

/**
 * Talks to one engine as one agent. The trading key's share lives in the
 * agent's signer, never here: this client proposes, reads, and relays the
 * signer channel.
 */
export class Client {
  readonly engineUrl: string;
  readonly agentKey: AgentKey;
  readonly keyId: string;
  readonly #options: ClientOptions;

  constructor(engineUrl: string, agentKey: AgentKey, keyId: string, options: ClientOptions = {}) {
    this.engineUrl = engineUrl.replace(/\/+$/, "");
    this.agentKey = agentKey;
    this.keyId = keyId;
    this.#options = options;
  }

  // Proposals

  /** Sign the proposal with the agent's key (unless already signed) and submit it. */
  async propose(proposal: Proposal): Promise<Decision> {
    const withKey = proposal.keyId ? proposal : { ...proposal, keyId: this.keyId };
    const signed = withKey.signature ? withKey : sign(withKey, this.agentKey);
    return (await this.#call("POST", "/proposals", toWire(signed))) as Decision;
  }

  // Reads, scoped to this agent

  /** A page of this agent's decisions, newest first. */
  async decisions(query: { tradingAccountId?: string; verdict?: string; from?: string; to?: string; cursor?: string; limit?: number } = {}): Promise<DecisionPage> {
    return (await this.#agentCall("GET", "/agent/decisions", { query })) as DecisionPage;
  }

  async decision(decisionId: string): Promise<Decision> {
    return (await this.#agentCall("GET", `/agent/decisions/${encodeURIComponent(decisionId)}`)) as Decision;
  }

  async approvals(status?: string): Promise<Record<string, unknown>[]> {
    return (await this.#agentCall("GET", "/agent/approvals", { query: { status } })) as Record<string, unknown>[];
  }

  async positions(): Promise<Record<string, unknown>[]> {
    return (await this.#agentCall("GET", "/agent/positions")) as Record<string, unknown>[];
  }

  /** Daily PnL of this agent; `from` and `to` are dates (YYYY-MM-DD). */
  async pnl(query: { from?: string; to?: string } = {}): Promise<Record<string, unknown>> {
    return (await this.#agentCall("GET", "/agent/pnl", { query })) as Record<string, unknown>;
  }

  async tradingAccount(tradingAccountId: string): Promise<Record<string, unknown>> {
    return (await this.#agentCall("GET", `/agent/trading-accounts/${encodeURIComponent(tradingAccountId)}`)) as Record<string, unknown>;
  }

  /** Poll a decision until it no longer waits for a person, or until timeoutMs; returns the last read. */
  /** What the trading account's venue accepts per symbol (tick, step, minimums), as the engine last read it. */
  async venueRules(tradingAccountId: string, symbol?: string): Promise<Record<string, unknown>> {
    return (await this.#agentCall("GET", `/agent/trading-accounts/${encodeURIComponent(tradingAccountId)}/venue-rules`, { query: { symbol } })) as Record<string, unknown>;
  }

  async wait(decisionId: string, { timeoutMs = 60_000, intervalMs = 1_000 } = {}): Promise<Decision> {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const d = await this.decision(decisionId);
      if (resolved(d) || Date.now() >= deadline) return d;
      await new Promise((r) => setTimeout(r, Math.min(intervalMs, Math.max(0, deadline - Date.now()))));
    }
  }

  // The signer channel

  /** Wait up to waitSeconds for requests this agent's signer must sign. */
  async signerPoll(waitSeconds = 25): Promise<SignRequest[]> {
    const timeoutMs = (this.#options.timeoutMs ?? 10_000) + waitSeconds * 1000;
    return (await this.#agentCall("POST", "/agent/signer/poll", { body: { waitSeconds }, timeoutMs })) as SignRequest[];
  }

  /** Answer a sign request: a share, a signature, a venue response or a refusal. */
  /** Take up to `count` of the engine's single-use FROST commitments for one-shot signing with a venue key (CommitmentBatch). */
  async signerCommitments(venueKeyId: string, count?: number): Promise<Record<string, unknown>> {
    const body: Record<string, unknown> = { venueKeyId };
    if (count !== undefined) body.count = count;
    return (await this.#agentCall("POST", "/agent/signer/commitments", { body })) as Record<string, unknown>;
  }

  async signerAnswer(requestId: string, answer: SignerAnswer): Promise<void> {
    await this.#agentCall("POST", `/agent/signer/requests/${encodeURIComponent(requestId)}/answer`, { body: answer });
  }

  // Plumbing

  async #agentCall(method: string, path: string, { query, body, timeoutMs }: { query?: Query; body?: unknown; timeoutMs?: number } = {}): Promise<unknown> {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query ?? {})) if (v !== undefined) params.set(k, String(v));
    const queryText = params.toString();
    const bodyText = body === undefined ? undefined : JSON.stringify(body);
    // Each attempt is signed anew, with its own nonce and time, so a retry is never a replay.
    const sign = () => this.agentKey.signRequest({ keyId: this.keyId, method, path, query: queryText, body: bodyText });
    return this.#call(method, path + (queryText ? "?" + queryText : ""), body, sign, timeoutMs);
  }

  /** Sends the request, again after an answer that did nothing (`retries`), and returns the final answer decoded; `sign` makes each attempt's headers. */
  async #call(method: string, path: string, body?: unknown, sign?: () => Record<string, string>, timeoutMs?: number): Promise<unknown> {
    const retries = this.#options.retries ?? {};
    const start = Date.now();
    for (let attempt = 0; ; attempt++) {
      const { res, text } = await this.#once(method, path, body, sign?.() ?? {}, timeoutMs);
      if (res.ok) return text ? JSON.parse(text) : null;
      let code = "http_error";
      let message = res.statusText || text.slice(0, 200);
      try {
        const parsed = JSON.parse(text) as { code?: unknown; message?: unknown };
        if (typeof parsed.code === "string") code = parsed.code;
        if (typeof parsed.message === "string") message = parsed.message;
      } catch {
        // not JSON: keep the text
      }
      const err = new EngineError(res.status, code, message, res.headers.get("retry-after") ?? undefined);
      const wait = retries === false ? undefined : retryWait(retries, attempt, res.status, err.retryAfter ?? null, Date.now() - start);
      if (wait === undefined) throw err;
      await (this.#options.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms))))(wait);
    }
  }

  async #once(method: string, path: string, body: unknown, headers: Record<string, string>, timeoutMs?: number): Promise<{ res: Response; text: string }> {
    const h: Record<string, string> = { Accept: "application/json", ...headers };
    if (body !== undefined) h["Content-Type"] = "application/json";
    const doFetch = this.#options.fetch ?? fetch;
    const res = await doFetch(this.engineUrl + path, {
      method,
      headers: h,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs ?? this.#options.timeoutMs ?? 10_000),
    });
    return { res, text: await res.text() };
  }
}

/** Whether the decision no longer waits for a person. */
export function resolved(d: Decision): boolean {
  if (d.verdict !== "require_approval") return true;
  return RESOLVED_APPROVAL.has(d.approval?.status ?? "");
}
