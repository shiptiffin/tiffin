import { duration } from "@/lib/format";

/** Every key of a project starts with this on the server ("p_my_shop:"); apps don't see it. */
export const kvPrefix = (project: string) => `p_${project.replace(/-/g, "_")}:`;

/** The six value types, in plain words; Valkey's own word stays the quiet type label. */
export const TYPES = [
  { type: "string", name: "Text", sub: "Text or JSON", one: "byte", many: "bytes" },
  { type: "hash", name: "Hash", sub: "Fields and their values", one: "field", many: "fields" },
  { type: "list", name: "List", sub: "Items in order", one: "item", many: "items" },
  { type: "set", name: "Set", sub: "Unique members", one: "member", many: "members" },
  { type: "zset", name: "Sorted set", sub: "Members ranked by score", one: "member", many: "members" },
  { type: "stream", name: "Stream", sub: "Entries added over time", one: "entry", many: "entries" },
] as const;

export type KVType = (typeof TYPES)[number]["type"];
export const typeInfo = (t: string) => TYPES.find((x) => x.type === t) ?? { type: t, name: t, sub: "", one: "item", many: "items" };

/** "in 23 h 41 min", or null for a key kept until it's deleted. */
export function expiresIn(ms: number): string | null {
  if (ms < 0) return null;
  return `in ${duration(Math.max(1, Math.round(ms / 1000)))}`;
}

/** Valkey's MATCH needs * ? [ ] \ quoted to mean themselves. */
export const globQuote = (s: string) => s.replace(/[*?[\]\\]/g, "\\$&");

/** A plain search ("u_20") finds keys containing it; anything with a wildcard is used as written. */
export const toGlob = (q: string) => (!q ? "" : /[*?[]/.test(q) ? q : `*${globQuote(q)}*`);

/** Expiry choices for the expiry control and the New key dialog, in seconds. */
export const UNITS = [
  { unit: "minutes", s: 60 },
  { unit: "hours", s: 3600 },
  { unit: "days", s: 86400 },
] as const;

/** A whole number of the largest unit that fits: 5400 s → [90, "minutes"]. */
export function splitSeconds(s: number): [number, (typeof UNITS)[number]["unit"]] {
  for (const u of [...UNITS].reverse()) if (s >= u.s && s % u.s === 0) return [s / u.s, u.unit];
  return [Math.max(1, Math.round(s / 60)), "minutes"];
}

/** Text that parses as a JSON object or array, or null. */
export function asJSON(text: string): unknown {
  const t = text.trim();
  if (!(t.startsWith("{") || t.startsWith("["))) return null;
  try {
    return JSON.parse(t);
  } catch {
    return null;
  }
}

/** The reason JSON-looking text doesn't parse, or null. */
export function jsonProblem(text: string): string | null {
  const t = text.trim();
  if (!(t.startsWith("{") || t.startsWith("["))) return null;
  try {
    JSON.parse(t);
    return null;
  } catch (e) {
    return (e as Error).message.replace(/^JSON\.parse: /, "");
  }
}

/** A 10-digit number that reads as a Unix time this century, as a short date. */
export function unixHint(s: string): string | null {
  if (!/^\d{10}$/.test(s)) return null;
  const t = Number(s) * 1000;
  if (t < Date.UTC(2001, 0) || t > Date.UTC(2100, 0)) return null;
  return new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(new Date(t));
}

/** A stream id's time ("1759741964123-0"). */
export function streamTime(id: string): string | null {
  const ms = Number(id.split("-")[0]);
  if (!ms || ms < Date.UTC(2001, 0)) return null;
  return new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(
    new Date(ms),
  );
}
