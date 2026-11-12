/**
 * The agent's identity: an Ed25519 key whose public half the engine stores.
 *
 * The engine shows the secret once, the agent's API key:
 * `msk_<environment>_<account suffix>_<key random part>_<seed>`. The seed is
 * the 32-byte Ed25519 seed in base64url without padding; before it comes
 * what names the engine and the key, so an agent needs nothing else: the
 * environment (testnet, prod, … for hosted Mishmar; anything else means the
 * engine URL is given by hand) and the two parts of the public key id,
 * `key_<account suffix>_<random part>`. Keys issued before contract 0.19.0
 * are `msk_<six characters>_<seed>` and still parse; they name neither engine
 * nor key id, which then come separately.
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

  /** Read the key out of the secret the engine showed when it was issued, in either format (see `parseSecret`). */
  static parse(secret: string): AgentKey {
    return parseSecret(secret).key;
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

/**
 * An agent's API key taken apart: the key, and what the secret says about
 * where it belongs. `environment`, `accountId` and `keyId` are set for a key
 * issued at contract 0.19.0 or later and absent for an older one.
 */
export interface ParsedSecret {
  key: AgentKey;
  environment?: string;
  accountId?: string;
  keyId?: string;
}

// An environment is a short lowercase word (testnet, stage, prod, self); the
// parts of an id are lowercase letters and digits, the random part twelve of them.
const ENVIRONMENT = /^[a-z][a-z0-9-]{0,31}$/;
const ID_PART = /^[a-z0-9]+$/;
const RANDOM_PART_LEN = 12;

/** The 32-byte seed a secret ends with, or undefined: strict base64url without padding. */
function seedOf(text: string): Buffer | undefined {
  const seed = Buffer.from(text, "base64url");
  return seed.length === SEED_SIZE && seed.toString("base64url") === text ? seed : undefined;
}

/**
 * Read the secret the engine showed when the key was issued, in either
 * format: `msk_<environment>_<account>_<key>_<seed>` (0.19.0 and later), which
 * also names the engine and the key id, or `msk_<prefix>_<seed>`.
 */
export function parseSecret(secret: string): ParsedSecret {
  const s = secret.trim();
  if (!s.startsWith("msk_")) throw new Error("malformed agent secret: it starts with msk_");
  // 0.19.0: five parts, the seed last. The seed is base64url and may itself
  // contain '_', so split for at most five. An older secret cannot pass here:
  // its seed would have to be split across the parts, leaving too few
  // characters for the last one to be a whole seed.
  const parts = splitN(s, "_", 5);
  const [, env, account, random, rest] = parts;
  if (parts.length === 5 && env && account && random && rest && ENVIRONMENT.test(env) && ID_PART.test(account) && random.length === RANDOM_PART_LEN && ID_PART.test(random)) {
    const seed = seedOf(rest);
    if (seed) return { key: AgentKey.fromSeed(seed), environment: env, accountId: `acc_${account}`, keyId: `key_${account}_${random}` };
  }
  // Before 0.19.0: msk_<prefix>_<seed>, split on the first two underscores only.
  const [, prefix, tail] = splitN(s, "_", 3);
  if (!prefix || tail === undefined) {
    throw new Error("malformed agent secret: expected msk_<environment>_<account>_<key>_<seed>, or msk_<prefix>_<seed>");
  }
  const seed = seedOf(tail);
  if (!seed) throw new Error("malformed agent secret: the seed is not 32 base64url bytes");
  return { key: AgentKey.fromSeed(seed) };
}

/**
 * Render the secret of a key issued at contract 0.19.0 or later: the
 * environment, the key id's account and random parts, and the seed. For tests
 * and tools; the engine issues the real ones.
 */
export function newSecret(environment: string, keyId: string, seed: Uint8Array): string {
  if (!ENVIRONMENT.test(environment)) throw new Error(`environment ${JSON.stringify(environment)} is not a lowercase word of up to 32 letters, digits and dashes`);
  const parts = keyId.split("_");
  const [kind, account, random] = parts;
  if (parts.length !== 3 || kind !== "key" || !account || !ID_PART.test(account) || !random || random.length !== RANDOM_PART_LEN || !ID_PART.test(random)) {
    throw new Error(`key id ${JSON.stringify(keyId)} is not key_<account suffix>_<12 characters>`);
  }
  if (seed.length !== SEED_SIZE) throw new Error("a seed is 32 bytes");
  return secret(`msk_${environment}_${account}_${random}`, seed);
}

/** `s` split on `sep` into at most `n` parts, the last holding the rest. */
function splitN(s: string, sep: string, n: number): string[] {
  const out: string[] = [];
  let rest = s;
  while (out.length < n - 1) {
    const i = rest.indexOf(sep);
    if (i < 0) break;
    out.push(rest.slice(0, i));
    rest = rest.slice(i + sep.length);
  }
  out.push(rest);
  return out;
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
