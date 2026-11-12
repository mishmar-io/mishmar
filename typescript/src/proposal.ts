/**
 * Build and sign what an agent sends to POST /v1/proposals.
 *
 * The engine authenticates a proposal by its signature alone: the canonical
 * JSON of the submitted object without `signature`, signed with the agent's
 * key. The bytes are pinned in spec/vectors/proposal-signing.json, which the
 * tests here run.
 *
 * Rules: size, price and leverage are decimal strings signed as written (no
 * number ever touches them); times are RFC 3339 UTC with a Z suffix and the
 * fraction trimmed; optional fields are present only when set, never null.
 * A cancel (kind "cancel") carries targetClientOrderId and none of the order
 * fields; every other kind carries side, size, orderType and reduceOnly. A
 * venueMessage, when the signer built the venue request itself, is nested
 * and canonicalised the same way, keys sorted.
 */

import type { AgentKey } from "./agentkey.ts";
import { newNonce } from "./agentkey.ts";
import { canonicalBytes } from "./canon.ts";
import { parseTime, rfc3339 } from "./time.ts";

export { newNonce, rfc3339 };

export type Kind = "order" | "transaction" | "signed_message" | "cancel";
export type Side = "buy" | "sell";
export type OrderType = "limit" | "market";
export type Scheme = "frost-ed25519" | "ecdsa-2p";

/**
 * The venue request the agent's signer built, with its half of the
 * signature, for the one-shot path (the contract's ProposalVenueMessage).
 * Base64 throughout.
 */
export interface VenueMessage {
  venueKeyId: string;
  scheme: Scheme;
  message: string;
  engineCommitmentId: string;
  /** FROST only: the signer's own nonce commitment. */
  commitment?: string;
  share: string;
}

/**
 * A proposal as the agent means it. `sign` fills what is left out. A cancel
 * names the order to pull and leaves the order fields out:
 * `{ kind: "cancel", connectedAccountId, symbol, targetClientOrderId }`.
 */
export interface Proposal {
  connectedAccountId: string;
  symbol: string;
  /** Required for every kind but cancel. */
  side?: Side;
  /** Decimal string, e.g. "0.25". Required for every kind but cancel. */
  size?: string;
  /** Default "limit". */
  orderType?: OrderType;
  /** Decimal string; required for a limit order, absent for a market order. */
  price?: string;
  /** Decimal string. */
  leverage?: string;
  /** Default false; never sent on a cancel. */
  reduceOnly?: boolean;
  /** Default "order". */
  kind?: Kind;
  /** Cancels only: the clientOrderId of the order to pull. */
  targetClientOrderId?: string;
  venueMessage?: VenueMessage;
  /** The Client fills this in. */
  keyId?: string;
  nonce?: string;
  proposedAt?: Date;
  expiresAt?: Date;
  /** How long the agent gives the order, when expiresAt is not set. Default one minute. */
  validForMs?: number;
  signature?: string;
}

/** The request body as the contract names it. */
export interface WireProposal {
  keyId: string;
  kind: Kind;
  nonce: string;
  orderType?: OrderType;
  proposedAt: string;
  expiresAt: string;
  reduceOnly?: boolean;
  connectedAccountId: string;
  side?: Side;
  size?: string;
  symbol: string;
  price?: string;
  leverage?: string;
  targetClientOrderId?: string;
  venueMessage?: VenueMessage;
  signature?: string;
}

const DECIMAL = /^-?[0-9]+(\.[0-9]+)?$/;

/** Whether s is a decimal string the contract accepts ("0.5", "-12", "98000.25"). */
export function isDecimal(s: unknown): s is string {
  return typeof s === "string" && DECIMAL.test(s);
}

/** The proposal's times, defaulting to now and now plus validForMs. */
export function times(p: Proposal): { proposedAt: Date; expiresAt: Date } {
  const proposedAt = p.proposedAt ?? new Date();
  const expiresAt = p.expiresAt ?? new Date(proposedAt.getTime() + (p.validForMs ?? 60_000));
  return { proposedAt, expiresAt };
}

/** Throw naming the first field the engine would refuse. */
export function validate(p: Proposal): void {
  const orderType = p.orderType ?? "limit";
  const kind = p.kind ?? "order";
  if (!p.keyId) throw new Error("keyId is required (the Client fills it in)");
  if (!p.connectedAccountId) throw new Error("connectedAccountId is required");
  if (!p.symbol) throw new Error("symbol is required");
  if (!p.nonce) throw new Error("nonce is required");
  if (!["order", "transaction", "signed_message", "cancel"].includes(kind)) throw new Error("kind must be order, transaction, signed_message or cancel");
  if (p.venueMessage !== undefined) validateVenueMessage(p.venueMessage);
  const { proposedAt, expiresAt } = times(p);
  if (expiresAt.getTime() <= proposedAt.getTime()) throw new Error("expiresAt must be after proposedAt");
  if (kind === "cancel") {
    if (!p.targetClientOrderId) throw new Error("a cancel needs targetClientOrderId, the clientOrderId of the order to pull");
    if (p.side !== undefined || p.size !== undefined || p.price !== undefined || p.orderType !== undefined || p.leverage !== undefined || p.reduceOnly !== undefined) {
      throw new Error("a cancel carries no order fields (side, size, price, orderType, leverage, reduceOnly)");
    }
    return;
  }
  if (p.targetClientOrderId !== undefined) throw new Error("targetClientOrderId is for cancels only");
  if (p.side !== "buy" && p.side !== "sell") throw new Error("side must be buy or sell");
  if (orderType !== "limit" && orderType !== "market") throw new Error("orderType must be limit or market");
  if (!isDecimal(p.size)) throw new Error('size must be a decimal string such as "0.5"');
  if (orderType === "limit" && !isDecimal(p.price)) throw new Error("a limit order needs a decimal price string");
  if (orderType === "market" && p.price !== undefined) throw new Error("a market order carries no price");
  if (p.leverage !== undefined && !isDecimal(p.leverage)) throw new Error("leverage must be a decimal string");
  if (p.reduceOnly !== undefined && typeof p.reduceOnly !== "boolean") throw new Error("reduceOnly must be a boolean");
}

function validateVenueMessage(m: VenueMessage): void {
  if (!m.venueKeyId || !m.scheme || !m.message || !m.engineCommitmentId || !m.share) {
    throw new Error("venueMessage needs venueKeyId, scheme, message, engineCommitmentId and share");
  }
  if (m.scheme !== "frost-ed25519" && m.scheme !== "ecdsa-2p") throw new Error("venueMessage.scheme must be frost-ed25519 or ecdsa-2p");
  if (m.scheme === "frost-ed25519" && !m.commitment) throw new Error("a FROST venueMessage needs the signer's commitment");
}

/** The request body for the proposal (the signature included once signed). */
export function toWire(p: Proposal): WireProposal {
  const { proposedAt, expiresAt } = times(p);
  const kind = p.kind ?? "order";
  const w: WireProposal = {
    keyId: p.keyId ?? "",
    kind,
    nonce: p.nonce ?? "",
    proposedAt: rfc3339(proposedAt),
    expiresAt: rfc3339(expiresAt),
    connectedAccountId: p.connectedAccountId,
    symbol: p.symbol,
  };
  if (kind !== "cancel") {
    w.orderType = p.orderType ?? "limit";
    w.reduceOnly = p.reduceOnly ?? false;
    if (p.side !== undefined) w.side = p.side;
    if (p.size !== undefined) w.size = p.size;
    if (p.price !== undefined) w.price = p.price;
    if (p.leverage !== undefined) w.leverage = p.leverage;
  }
  if (p.targetClientOrderId !== undefined) w.targetClientOrderId = p.targetClientOrderId;
  if (p.venueMessage !== undefined) w.venueMessage = { ...p.venueMessage };
  if (p.signature !== undefined) w.signature = p.signature;
  return w;
}

/** The inverse of toWire, for vectors and for replaying a stored proposal. */
export function fromWire(w: WireProposal): Proposal {
  const p: Proposal = {
    keyId: w.keyId,
    kind: w.kind,
    nonce: w.nonce,
    proposedAt: parseTime(w.proposedAt),
    expiresAt: parseTime(w.expiresAt),
    connectedAccountId: w.connectedAccountId,
    symbol: w.symbol,
  };
  if (w.orderType !== undefined) p.orderType = w.orderType;
  if (w.reduceOnly !== undefined) p.reduceOnly = w.reduceOnly;
  if (w.side !== undefined) p.side = w.side;
  if (w.size !== undefined) p.size = w.size;
  if (w.price !== undefined) p.price = w.price;
  if (w.leverage !== undefined) p.leverage = w.leverage;
  if (w.targetClientOrderId !== undefined) p.targetClientOrderId = w.targetClientOrderId;
  if (w.venueMessage !== undefined) p.venueMessage = { ...w.venueMessage };
  if (w.signature !== undefined) p.signature = w.signature;
  return p;
}

/** What the agent signs: the canonical JSON of the proposal without the signature. */
export function signingBytes(p: Proposal): Uint8Array {
  const { signature: _signature, ...unsigned } = toWire(p);
  return canonicalBytes(unsigned);
}

/** Pin the times, validate, and return the proposal with its signature set. */
export function sign(p: Proposal, key: AgentKey): Proposal {
  const pinned: Proposal = { ...p, nonce: p.nonce ?? newNonce(), ...times(p) };
  validate(pinned);
  return { ...pinned, signature: key.sign(signingBytes(pinned)) };
}
