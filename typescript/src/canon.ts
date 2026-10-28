/**
 * Canonical JSON: the bytes an agent signs and the engine verifies.
 *
 * The rules match Go's encoding/json, which the engine uses (see canon/ in
 * the repo root and spec/vectors): one object, keys sorted by code point, no
 * whitespace, keys whose value is null or undefined left out at every depth,
 * strings with Go's escapes. JSON.stringify agrees on everything but the
 * HTML-safe escapes, which are added here. Numbers never appear in what
 * Mishmar signs, so they are refused rather than formatted differently.
 */

const GO_ESCAPES: ReadonlyArray<readonly [RegExp, string]> = [
  [/</g, "\\u003c"],
  [/>/g, "\\u003e"],
  [/&/g, "\\u0026"],
  // Built from char codes: a literal U+2028/9 in a regex is a line break to JavaScript.
  [new RegExp(String.fromCharCode(0x2028), "g"), "\\u2028"],
  [new RegExp(String.fromCharCode(0x2029), "g"), "\\u2029"],
];

function quote(s: string): string {
  let out = JSON.stringify(s);
  for (const [re, esc] of GO_ESCAPES) out = out.replace(re, esc);
  return out;
}

/** Go sorts keys as bytes, which for UTF-8 is code-point order; JS's default sort is UTF-16 order. */
function byCodePoint(a: string, b: string): number {
  const x = Array.from(a, (c) => c.codePointAt(0) ?? 0);
  const y = Array.from(b, (c) => c.codePointAt(0) ?? 0);
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = (x[i] ?? 0) - (y[i] ?? 0);
    if (d !== 0) return d;
  }
  return x.length - y.length;
}

function encode(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "string") return quote(v);
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "number" || typeof v === "bigint") {
    throw new TypeError(`canonicalJson: numbers are not signed by Mishmar; send ${String(v)} as a decimal string`);
  }
  if (Array.isArray(v)) return "[" + v.map(encode).join(",") + "]";
  if (typeof v === "object") {
    const o = v as Record<string, unknown>;
    const keys = Object.keys(o)
      .filter((k) => o[k] !== null && o[k] !== undefined)
      .sort(byCodePoint);
    return "{" + keys.map((k) => quote(k) + ":" + encode(o[k])).join(",") + "}";
  }
  throw new TypeError(`canonicalJson: cannot encode a ${typeof v}`);
}

/** The canonical encoding of value, as a string (UTF-8 it before signing). */
export function canonicalJson(value: unknown): string {
  return encode(value);
}

/** The canonical encoding of value as the bytes to sign. */
export function canonicalBytes(value: unknown): Uint8Array {
  return new TextEncoder().encode(encode(value));
}
