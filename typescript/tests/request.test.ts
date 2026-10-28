// The agentAuth vectors: the same bytes and signatures the Go package and the Python SDK pin.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { AgentKey, HEADER_KEY_ID, HEADER_NONCE, HEADER_SIGNATURE, HEADER_SIGNED_AT, requestSigningBytes, verify, verifyRequest } from "../src/index.ts";

type Vectors = {
  secret: string;
  publicKey: string;
  cases: { name: string; method: string; path: string; query?: string; body?: string; keyId: string; nonce: string; signedAt: string; signingBytes: string; signature: string }[];
};

const vectors = JSON.parse(readFileSync(new URL("../../spec/vectors/agent-request-signing.json", import.meta.url), "utf8")) as Vectors;
const key = AgentKey.parse(vectors.secret);

for (const c of vectors.cases) {
  test(c.name, () => {
    const req = { method: c.method, path: c.path, query: c.query ?? "", body: c.body ?? "", keyId: c.keyId, nonce: c.nonce, signedAt: c.signedAt };
    const bytes = requestSigningBytes(req);
    assert.equal(Buffer.from(bytes).toString("utf8"), c.signingBytes);
    assert.equal(key.sign(bytes), c.signature);
    assert.ok(verify(vectors.publicKey, bytes, c.signature));
    assert.ok(verifyRequest(vectors.publicKey, req, c.signature));
  });
}

test("signRequest makes the headers", () => {
  const k = AgentKey.fromSeed(Uint8Array.from({ length: 32 }, (_, i) => i));
  const h = k.signRequest({ keyId: "key_a", method: "get", path: "/agent/positions" });
  assert.equal(h[HEADER_KEY_ID], "key_a");
  assert.equal(h[HEADER_NONCE]?.length, 32);
  assert.ok(h[HEADER_SIGNED_AT]?.endsWith("Z"));
  assert.ok(verifyRequest(k.publicKeyB64, { keyId: "key_a", method: "GET", path: "/agent/positions", nonce: h[HEADER_NONCE]!, signedAt: h[HEADER_SIGNED_AT]! }, h[HEADER_SIGNATURE]!));
});
