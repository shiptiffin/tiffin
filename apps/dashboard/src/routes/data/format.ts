// How values from the database read in the grid, and how what a person
// types becomes a value again.
import { jsonLine } from "@/components/data-parts";

export const NUMERIC = /^(int|numeric|float|real|double|decimal|bigint|smallint|serial|money|oid)/;

const TS = /^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(\.\d+)?([+-]\d{2}(?::?\d{2})?|Z)$/;
const tsFmt = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hourCycle: "h23",
});

/** A timestamptz as a Date, or null when the text isn't one. */
export function tsDate(v: string): Date | null {
  const m = v.match(TS);
  if (!m) return null;
  const off = m[4] === "Z" ? "Z" : m[4].length === 3 ? `${m[4]}:00` : m[4].replace(/^([+-]\d{2})(\d{2})$/, "$1:$2");
  const d = new Date(`${m[1]}T${m[2]}${m[3] ?? ""}${off}`);
  return Number.isNaN(d.getTime()) ? null : d;
}

export const isTimestamp = (v: unknown) => typeof v === "string" && TS.test(v);

/** One line of text for a value: a timestamptz in the viewer's clock, JSON on one line. */
export function cellText(v: unknown, expanded = false): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "object") return expanded ? JSON.stringify(v, null, 2) : jsonLine(v);
  if (!expanded && typeof v === "string") {
    const d = tsDate(v);
    if (d) return tsFmt.format(d);
  }
  return String(v);
}

/** The text a value is copied (and pasted) as: what is stored, not how it reads. */
export function rawText(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

const pad = (n: number, w = 2) => String(n).padStart(w, "0");

/** The parts of a column the editors need: its kind, and its type to tell timestamp from timestamptz. */
export type EditCol = { category: string; name: string; nullable?: boolean; baseType?: string; type?: string };

/** A timestamptz (an instant, shown in the viewer's clock) rather than a timestamp (a wall time, shown as stored). */
const zoned = (c: EditCol) => (c.baseType ?? c.type) === "timestamptz";

/** A wall time a datetime-local input can hold: seconds, and up to milliseconds. */
const LOCAL = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,3})?)?$/;

/**
 * A stored timestamp as the wall time an editor starts with. A timestamptz
 * is moved to the viewer's clock; a timestamp is its stored wall time, untouched.
 * Fractional seconds are kept, all six digits.
 */
function tsDraft(v: string, c: EditCol): string {
  if (zoned(c)) {
    const m = v.match(TS);
    const d = m && tsDate(`${m[1]} ${m[2]}${m[4]}`); // to the second; the fraction is carried over as written
    if (!m || !d) return v;
    return `${pad(d.getFullYear(), 4)}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}${m[3] ?? ""}`;
  }
  const w = v.match(/^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}(\.\d+)?)$/);
  return w ? `${w[1]}T${w[2]}` : v;
}

/**
 * A wall time typed for a timestamptz, read in the viewer's clock, as the
 * instant to store (UTC, fraction kept). Text that names its own zone, or
 * isn't a plain date and time (infinity, BC dates), goes to Postgres as typed.
 */
function tsInstant(t: string): string {
  const m = t.match(/^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2})(:\d{2})?(\.\d+)?$/);
  if (!m) return t;
  const d = new Date(`${m[1]}T${m[2]}${m[3] ?? ":00"}`);
  if (Number.isNaN(d.getTime())) return t;
  const iso = d.toISOString();
  return /^\d{4}-/.test(iso) ? `${iso.slice(0, 19)}${m[4] ?? ""}Z` : t;
}

/** The input a column's value is edited in. A stored timestamp too precise for the browser's picker gets plain text. */
export function inputFor(c: EditCol, v: unknown): { type: "date" | "time" | "datetime-local" | "text"; step?: number } {
  if (c.category === "date") return { type: "date" };
  if (c.category === "time") return (c.baseType ?? c.type) === "timetz" ? { type: "text" } : { type: "time", step: 1 };
  if (c.category !== "timestamp") return { type: "text" };
  const d = v === null || v === undefined ? "" : toDraft(v, c);
  if (d !== "" && !LOCAL.test(d)) return { type: "text" };
  return { type: "datetime-local", step: d.includes(".") ? 0.001 : 1 };
}

/** A stored value as the text an editor starts with. */
export function toDraft(v: unknown, c: EditCol): string {
  if (v === null || v === undefined) return "";
  if (c.category === "timestamp" && typeof v === "string") return tsDraft(v, c);
  if (c.category === "json") return JSON.stringify(v, null, 2);
  if (c.category === "array" && typeof v === "string") return arrayToLines(v);
  return rawText(v);
}

export class DraftError extends Error {}

/**
 * What a person typed, as the value to send. An empty box is NULL when the
 * column may be empty, and empty text otherwise.
 */
export function fromDraft(draft: string, c: EditCol): unknown {
  const t = draft.trim();
  if (c.category === "json") {
    if (!t) return null;
    try {
      return JSON.parse(t);
    } catch {
      throw new DraftError(`${c.name} isn't valid JSON: check the quotes and commas`);
    }
  }
  if (c.category === "array") {
    const items = draft.split("\n").map((s) => s.trim()).filter(Boolean);
    return items;
  }
  if (t === "" && (c.nullable || c.category !== "text")) return null;
  if (c.category === "timestamp") return zoned(c) ? tsInstant(t) : t; // a timestamp keeps its wall time
  if (c.category === "bool") return t === "true" || t === "t" || t === "yes";
  if (c.category === "number") return t.replace(/[,\s_]/g, "");
  return c.category === "text" ? draft : t;
}

/** A Postgres array literal, {a,"b c"}, as one item per line. */
export function arrayToLines(v: string): string {
  return parseArray(v).join("\n");
}

export function parseArray(v: string): string[] {
  const s = v.trim();
  if (!s.startsWith("{") || !s.endsWith("}")) return [s];
  const out: string[] = [];
  let cur = "";
  let quoted = false;
  let wasQuoted = false;
  for (let i = 1; i < s.length - 1; i++) {
    const ch = s[i];
    if (quoted) {
      if (ch === "\\") cur += s[++i];
      else if (ch === '"') quoted = false;
      else cur += ch;
    } else if (ch === '"') {
      quoted = wasQuoted = true;
    } else if (ch === ",") {
      out.push(cur);
      cur = "";
      wasQuoted = false;
    } else cur += ch;
  }
  if (cur !== "" || wasQuoted || out.length > 0) out.push(cur);
  return out.map((x) => (x === "NULL" ? "" : x));
}

/** Postgres's long type names, as people write them: timestamptz, varchar, float8. */
export function shortType(t: string): string {
  return t
    .replace("timestamp with time zone", "timestamptz")
    .replace("timestamp without time zone", "timestamp")
    .replace("time with time zone", "timetz")
    .replace("time without time zone", "time")
    .replace("character varying", "varchar")
    .replace("double precision", "float8")
    .replace(/^character\b/, "char");
}

/** Tab-separated rows (from a spreadsheet's copy) as cells. */
export function parseTSV(text: string): string[][] {
  const lines = text.replace(/\r\n?/g, "\n").replace(/\n$/, "").split("\n");
  return lines.map((l) => l.split("\t"));
}

/** Rows as CSV (RFC 4180). */
export function toCSV(header: string[], rows: unknown[][]): string {
  const q = (s: string) => (/[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s);
  return [header.map(q).join(","), ...rows.map((r) => r.map((v) => q(rawText(v))).join(","))].join("\r\n") + "\r\n";
}
