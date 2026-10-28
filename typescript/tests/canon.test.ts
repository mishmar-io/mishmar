import assert from "node:assert/strict";
import { test } from "node:test";
import { canonicalJson } from "../src/index.ts";

const cases: [string, unknown, string][] = [
  ["keys sorted, nulls and undefined dropped, nested", { z: "1", a: null, u: undefined, m: { b: null, a: "v" } }, '{"m":{"a":"v"},"z":"1"}'],
  ["nulls dropped inside arrays' objects, kept as array elements", [{ k: null, j: "x" }, null], '[{"j":"x"},null]'],
  ["html-safe escapes as Go writes them", { s: "a<b>&c " }, '{"s":"a\\u003cb\\u003e\\u0026c\\u2028"}'],
  ["control characters", { s: 'tab\tnl\nquote"bs\\nul\u0000bf\b\f\u001f' }, '{"s":"tab\\tnl\\nquote\\"bs\\\\nul\\u0000bf\\b\\f\\u001f"}'],
  ["non-ascii, slash and DEL stay raw", { s: "ביטקוין €/\u007f" }, '{"s":"ביטקוין €/\u007f"}'],
  ["booleans and empty containers", { t: true, f: false, e: {}, l: [] }, '{"e":{},"f":false,"l":[],"t":true}'],
  ["keys sorted by code point, not UTF-16 unit", { "\u{1F600}": "a", "～": "b" }, '{"～":"b","\u{1F600}":"a"}'],
];

for (const [name, value, want] of cases) {
  test(name, () => assert.equal(canonicalJson(value), want));
}

test("numbers are refused", () => {
  assert.throws(() => canonicalJson({ size: 0.5 }), TypeError);
  assert.throws(() => canonicalJson({ n: 1 }), TypeError);
});
