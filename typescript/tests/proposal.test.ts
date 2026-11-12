import assert from "node:assert/strict";
import { test } from "node:test";
import { AgentKey, newNonce, rfc3339, sign, toWire, validate } from "../src/index.ts";
import type { Proposal } from "../src/index.ts";

const KEY = AgentKey.fromSeed(Uint8Array.from({ length: 32 }, (_, i) => i));

const good = (): Proposal => ({ connectedAccountId: "rt_1", symbol: "BTC", side: "buy", size: "0.5", price: "98000.5", keyId: "key_a" });

test("sign fills the nonce and the times and the body carries the signature", () => {
  const p = sign(good(), KEY);
  assert.ok(p.nonce && p.proposedAt && p.expiresAt);
  assert.equal(p.expiresAt!.getTime() - p.proposedAt!.getTime(), 60_000);
  const body = toWire(p);
  assert.equal(body.signature, p.signature);
  assert.ok(body.proposedAt.endsWith("Z") && "price" in body && !("leverage" in body));
});

test("rfc3339", () => {
  const t = new Date("2026-10-18T11:00:00.250+01:00");
  assert.equal(rfc3339(t), "2026-10-18T10:00:00.25Z");
  assert.equal(rfc3339(new Date("2026-10-18T10:00:00Z")), "2026-10-18T10:00:00Z");
  assert.equal(rfc3339(new Date("2026-10-18T10:00:00.001Z")), "2026-10-18T10:00:00.001Z");
});

const bad: [string, Partial<Proposal>, RegExp][] = [
  ["no key", { keyId: "" }, /keyId/],
  ["float-looking size", { size: "1e3" }, /size/],
  ["signed size", { size: "+1" }, /size/],
  ["limit without price", { price: undefined }, /limit order/],
  ["market with price", { orderType: "market" }, /market order/],
  ["odd side", { side: "long" as never }, /side/],
  ["bad leverage", { leverage: "2x" }, /leverage/],
  ["expires first", { expiresAt: new Date("2020-01-01T00:00:00Z") }, /expiresAt/],
];
for (const [name, change, message] of bad) {
  test(`validate names the field: ${name}`, () => {
    assert.throws(() => validate({ ...good(), nonce: "n", ...change }), message);
  });
}

test("a number for the size is refused at signing", () => {
  assert.throws(() => sign({ ...good(), size: 0.5 as never }, KEY), /size/);
});

test("nonce", () => {
  assert.equal(newNonce().length, 32);
  assert.notEqual(newNonce(), newNonce());
});

const cancel = (): Proposal => ({ kind: "cancel", connectedAccountId: "rt_1", symbol: "BTCUSDT", targetClientOrderId: "msh-a1b2c3-k3j9xq", keyId: "key_a" });

test("a cancel carries the target and no order fields", () => {
  const body = toWire(sign(cancel(), KEY));
  assert.equal(body.targetClientOrderId, "msh-a1b2c3-k3j9xq");
  for (const k of ["side", "size", "price", "orderType", "reduceOnly", "leverage"]) assert.ok(!(k in body), k);
});

const badCancel: [string, Partial<Proposal>, RegExp][] = [
  ["no target", { targetClientOrderId: undefined }, /targetClientOrderId/],
  ["with a side", { side: "buy" }, /no order fields/],
  ["with a size", { size: "1" }, /no order fields/],
  ["reduce-only", { reduceOnly: false }, /no order fields/],
];
for (const [name, change, message] of badCancel) {
  test(`cancel validation: ${name}`, () => {
    assert.throws(() => validate({ ...cancel(), nonce: "n", ...change }), message);
  });
}

test("an order with a target is refused", () => {
  assert.throws(() => validate({ ...good(), nonce: "n", targetClientOrderId: "msh-x" }), /cancels only/);
});
