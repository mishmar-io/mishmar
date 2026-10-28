/** Times as the contract writes them. */

/** Format a time as the contract wants it: UTC, Z suffix, fraction trimmed. */
export function rfc3339(d: Date): string {
  const iso = d.toISOString(); // 2026-10-18T10:00:00.250Z
  const [whole, rest] = iso.split(".");
  const fraction = (rest ?? "").replace(/Z$/, "").replace(/0+$/, "");
  return fraction ? `${whole}.${fraction}Z` : `${whole}Z`;
}

/** A Date from an RFC 3339 string that carries an offset. */
export function parseTime(s: string): Date {
  if (!/(Z|[+-]\d\d:\d\d)$/.test(s)) throw new Error(`time ${JSON.stringify(s)} has no offset`);
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) throw new Error(`not a time: ${JSON.stringify(s)}`);
  return d;
}
