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

  constructor(status: number, code: string, message: string) {
    super(`${status} ${code}: ${message}`);
    this.name = "EngineError";
    this.status = status;
    this.code = code;
  }
}

export interface ClientOptions {
  timeoutMs?: number;
  /** For tests: a fetch to use instead of the global one. */
  fetch?: typeof fetch;
}

type Query = Record<string, string | number | undefined>;

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
    const headers = this.agentKey.signRequest({ keyId: this.keyId, method, path, query: queryText, body: bodyText });
    return this.#call(method, path + (queryText ? "?" + queryText : ""), body, headers, timeoutMs);
  }

  async #call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}, timeoutMs?: number): Promise<unknown> {
    const h: Record<string, string> = { Accept: "application/json", ...headers };
    if (body !== undefined) h["Content-Type"] = "application/json";
    const doFetch = this.#options.fetch ?? fetch;
    const res = await doFetch(this.engineUrl + path, {
      method,
      headers: h,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs ?? this.#options.timeoutMs ?? 10_000),
    });
    const text = await res.text();
    if (!res.ok) {
      let code = "http_error";
      let message = res.statusText || text.slice(0, 200);
      try {
        const parsed = JSON.parse(text) as { code?: unknown; message?: unknown };
        if (typeof parsed.code === "string") code = parsed.code;
        if (typeof parsed.message === "string") message = parsed.message;
      } catch {
        // not JSON: keep the text
      }
      throw new EngineError(res.status, code, message);
    }
    return text ? JSON.parse(text) : null;
  }
}

/** Whether the decision no longer waits for a person. */
export function resolved(d: Decision): boolean {
  if (d.verdict !== "require_approval") return true;
  return RESOLVED_APPROVAL.has(d.approval?.status ?? "");
}
