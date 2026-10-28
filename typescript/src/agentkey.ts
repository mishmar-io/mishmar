/**
 * The agent's identity: an Ed25519 key whose public half the engine stores.
 *
 * The engine shows the secret once, as `msk_<prefix>_<seed>`: a label, then
 * the 32-byte seed in base64url without padding. The key id (`key_…`) is
 * separate and public.
 *
 * The key signs two things: proposals (proposal.ts) and, under the
 * contract's agentAuth scheme, every other agent-facing request: four headers
 * carry the key id, the time, a nonce and an Ed25519 signature over the
 * canonical JSON of `{bodySha256, keyId, method, nonce, path, query,
 * signedAt}`. The bytes are pinned in spec/vectors/agent-request-signing.json.
 */

import { createHash, createPrivateKey, createPublicKey, randomBytes, sign as edSign, verify as edVerify } from "node:crypto";
import type { KeyObject } from "node:crypto";
import { canonicalBytes } from "./canon.ts";
import { rfc3339 } from "./time.ts";

const SEED_SIZE = 32;
// DER wrappers for a raw Ed25519 seed and public key, so node:crypto can load them.
const PKCS8_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex");
const SPKI_PREFIX = Buffer.from("302a300506032b6570032100", "hex");

export const HEADER_KEY_ID = "X-Mishmar-Key-Id";
export const HEADER_SIGNED_AT = "X-Mishmar-Signed-At";
export const HEADER_NONCE = "X-Mishmar-Nonce";
export const HEADER_SIGNATURE = "X-Mishmar-Signature";

/** A fresh nonce: 16 random bytes, hex. */
export function newNonce(): string {
  return randomBytes(16).toString("hex");
}

/** One HTTP request as the agentAuth scheme sees it. */
export interface RequestToSign {
  /** In any case; signed upper case. */
  method: string;
  /** Without the server prefix: "/agent/decisions", not "/v1/agent/decisions". */
  path: string;
  /** The raw query string without the "?"; empty or absent when none. */
  query?: string;
  /** The request body; absent when none. */
  body?: Uint8Array | string;
  keyId: string;
  nonce: string;
  /** Exactly as sent in X-Mishmar-Signed-At. */
  signedAt: string;
}

/** What the agent signs for a request. */
export function requestSigningBytes(r: RequestToSign): Uint8Array {
  const fields: Record<string, string> = {
    keyId: r.keyId,
    method: r.method.toUpperCase(),
    nonce: r.nonce,
    path: r.path,
    signedAt: r.signedAt,
  };
  if (r.query) fields.query = r.query;
  const body = typeof r.body === "string" ? Buffer.from(r.body, "utf8") : r.body;
  if (body && body.length > 0) fields.bodySha256 = createHash("sha256").update(body).digest("hex");
  return canonicalBytes(fields);
}

/** The agent's private key. */
export class AgentKey {
  readonly #key: KeyObject;

  private constructor(key: KeyObject) {
    this.#key = key;
  }

  /** Read the secret the engine showed when the key was issued. */
  static parse(secret: string): AgentKey {
    // msk_<prefix>_<seed>: the seed is base64url and may itself contain '_',
    // so split on the first two underscores only.
    const s = secret.trim();
    const first = s.indexOf("_");
    const second = first < 0 ? -1 : s.indexOf("_", first + 1);
    if (first < 0 || second < 0 || s.slice(0, first) !== "msk") {
      throw new Error("malformed agent secret: expected msk_<prefix>_<seed>");
    }
    const seed = Buffer.from(s.slice(second + 1), "base64url");
    if (seed.length !== SEED_SIZE || seed.toString("base64url") !== s.slice(second + 1)) {
      throw new Error("malformed agent secret: the seed is not 32 base64url bytes");
    }
    return AgentKey.fromSeed(seed);
  }

  static fromSeed(seed: Uint8Array): AgentKey {
    if (seed.length !== SEED_SIZE) throw new Error("an Ed25519 seed is 32 bytes");
    const der = Buffer.concat([PKCS8_PREFIX, Buffer.from(seed)]);
    return new AgentKey(createPrivateKey({ key: der, format: "der", type: "pkcs8" }));
  }

  /** A fresh key, for tests and tools; the engine issues the real ones. */
  static generate(): AgentKey {
    return AgentKey.fromSeed(randomBytes(SEED_SIZE));
  }

  /** The base64 (standard, padded) Ed25519 signature of message. */
  sign(message: Uint8Array): string {
    return edSign(null, message, this.#key).toString("base64");
  }

  /** The four agentAuth headers for a request; the nonce and the time are made here when not given. */
  signRequest(r: Omit<RequestToSign, "nonce" | "signedAt"> & { nonce?: string; signedAt?: Date }): Record<string, string> {
    const nonce = r.nonce ?? newNonce();
    const signedAt = rfc3339(r.signedAt ?? new Date());
    const signature = this.sign(requestSigningBytes({ ...r, nonce, signedAt }));
    return { [HEADER_KEY_ID]: r.keyId, [HEADER_SIGNED_AT]: signedAt, [HEADER_NONCE]: nonce, [HEADER_SIGNATURE]: signature };
  }

  /** The public half, raw 32 bytes. */
  get publicKey(): Uint8Array {
    const spki = createPublicKey(this.#key).export({ format: "der", type: "spki" });
    return new Uint8Array(spki.subarray(spki.length - SEED_SIZE));
  }

  /** The public half as the API stores and returns it. */
  get publicKeyB64(): string {
    return Buffer.from(this.publicKey).toString("base64");
  }
}

/** Render a seed the way the engine shows it, with the given prefix. */
export function secret(prefix: string, seed: Uint8Array): string {
  return prefix + "_" + Buffer.from(seed).toString("base64url");
}

/** Check a base64 signature over message with a public key in the API's form. */
export function verify(publicKeyB64: string, message: Uint8Array, signatureB64: string): boolean {
  try {
    const raw = Buffer.from(publicKeyB64, "base64");
    if (raw.length !== SEED_SIZE) return false;
    const key = createPublicKey({ key: Buffer.concat([SPKI_PREFIX, raw]), format: "der", type: "spki" });
    return edVerify(null, message, key, Buffer.from(signatureB64, "base64"));
  } catch {
    return false;
  }
}

/** The engine's side of agentAuth. Freshness and nonce replay are the caller's checks. */
export function verifyRequest(publicKeyB64: string, r: RequestToSign, signatureB64: string): boolean {
  return verify(publicKeyB64, requestSigningBytes(r), signatureB64);
}
