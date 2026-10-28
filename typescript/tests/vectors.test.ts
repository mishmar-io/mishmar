// The cross-language vectors: the same bytes and signatures the Go package, the Python SDK and the engine pin.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { AgentKey, fromWire, sign, signingBytes, toWire, verify } from "../src/index.ts";
import type { WireProposal } from "../src/index.ts";

type Vectors = {
  secret: string;
  publicKey: string;
  cases: { name: string; proposal: WireProposal; signingBytes: string; signature: string }[];
};

const vectors = JSON.parse(readFileSync(new URL("../../spec/vectors/proposal-signing.json", import.meta.url), "utf8")) as Vectors;
const key = AgentKey.parse(vectors.secret);

test("the vector key", () => {
  assert.equal(key.publicKeyB64, vectors.publicKey);
});

for (const c of vectors.cases) {
  test(c.name, () => {
    const p = fromWire(c.proposal);
    assert.equal(Buffer.from(signingBytes(p)).toString("utf8"), c.signingBytes);
    const signed = sign(p, key);
    assert.equal(signed.signature, c.signature);
    assert.ok(verify(vectors.publicKey, signingBytes(signed), c.signature));
    // The signed wire body round-trips and still signs identically.
    const again = fromWire(toWire(signed));
    assert.deepEqual(signingBytes(again), signingBytes(p));
    assert.equal(again.signature, c.signature);
  });
}
