// Numbers and units, in one place. Every number the dashboard prints goes
// through here, so they all read the same way:
//   · thousands separators: 1,284
//   · a narrow no-break space before a unit, so it never wraps away: 512 MB
//   · a real minus (−) and plus in deltas: +148, −24
//   · humanised durations and bytes: "6 days", "3 h 12 min", "1.4 GB"
// For a number with a small unit in the UI use <Qty> (components/qty.tsx),
// which renders the parts from here.

/** Narrow no-break space (U+202F): between a number and its unit. */
export const NNBSP = " ";
/** The minus sign (U+2212), not a hyphen. */
export const MINUS = "−";

const intFmt = new Intl.NumberFormat("en-GB", { maximumFractionDigits: 0 });

/** 1284 → "1,284". */
export function int(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "–";
  return intFmt.format(Math.round(n)).replace("-", MINUS);
}

/** Up to `digits` decimals, separators, trailing zeros dropped: 1.65, 12.5, 1,204. */
export function dec(n: number | null | undefined, digits = 1): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "–";
  return new Intl.NumberFormat("en-GB", { maximumFractionDigits: digits }).format(n).replace("-", MINUS);
}

/** A general count: compact above 100,000 ("1.2M"). */
export function num(n: number | null | undefined): string {
  if (n === null || n === undefined) return "–";
  return new Intl.NumberFormat("en-GB", { maximumFractionDigits: 1, notation: Math.abs(n) >= 100_000 ? "compact" : "standard" })
    .format(n)
    .replace("-", MINUS);
}

/** A delta with its sign: +148, −24, ±0. */
export function signed(n: number, digits = 0): string {
  if (n === 0) return "±0";
  return `${n > 0 ? "+" : MINUS}${dec(Math.abs(n), digits)}`;
}

/** Joins a value and its unit with a narrow no-break space. */
export function withUnit(value: string, unit: string): string {
  return unit ? `${value}${NNBSP}${unit}` : value;
}

const units = ["B", "KB", "MB", "GB", "TB"] as const;
export type ByteUnit = (typeof units)[number];

/** 1536 → { value: "1.5", unit: "KB" }. Binary-based, as file managers show it. */
export function bytesParts(n: number | null | undefined, digits = 1): { value: string; unit: ByteUnit } {
  if (n === null || n === undefined || !Number.isFinite(n)) return { value: "–", unit: "B" };
  if (n < 1024) return { value: int(n), unit: "B" };
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return { value: v >= 100 ? int(v) : dec(v, digits), unit: units[i] };
}

/** 1536 → "1.5 KB" (narrow no-break space). */
export function bytes(n: number | null | undefined, digits = 1): string {
  const p = bytesParts(n, digits);
  return withUnit(p.value, p.unit);
}

/** Bytes as whole megabytes, the Stack's one unit: 1771094016 → "1,689". */
export function mb(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "–";
  return int(n / 1048576);
}

/** 0.313 → "31 %"; with digits 1 → "31.3 %". */
export function pct(ratio: number, digits = 0): string {
  return withUnit(dec(ratio * 100, digits), "%");
}

/** Milliseconds, humanised: "<1 ms", "240 ms", "2.4 s", "3 min". */
export function ms(n: number): string {
  if (n < 1) return withUnit("<1", "ms");
  if (n < 1000) return withUnit(int(n), "ms");
  if (n < 60_000) return withUnit(dec(n / 1000, n < 10_000 ? 1 : 0), "s");
  return withUnit(int(n / 60_000), "min");
}

/** Seconds, humanised for people: "40 s", "4 min", "3 h 12 min", "6 days". */
export function duration(s: number): string {
  if (!Number.isFinite(s) || s < 0) return "–";
  if (s < 60) return withUnit(int(s), "s");
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d >= 2) return `${d} days`;
  if (d === 1) return h > 0 ? `1 day ${withUnit(String(h), "h")}` : "1 day";
  if (h > 0) return m > 0 ? `${withUnit(String(h), "h")} ${withUnit(String(m), "min")}` : withUnit(String(h), "h");
  return withUnit(String(m), "min");
}

const smallWords = ["no", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"];

/** Numbers under 13 as words in sentences ("two apps"), larger ones as figures. */
export function words(n: number, capital = false): string {
  const w = n >= 0 && n < smallWords.length && Number.isInteger(n) ? smallWords[n] : int(n);
  return capital ? w.charAt(0).toUpperCase() + w.slice(1) : w;
}

/** "1 app", "3 apps", "1,204 people". */
export function count(n: number, one: string, many = `${one}s`): string {
  return `${int(n)} ${n === 1 ? one : many}`;
}

/** "two apps", "one service": count in words for sentences. */
export function countWords(n: number, one: string, many = `${one}s`, capital = false): string {
  return `${words(n, capital)} ${n === 1 ? one : many}`;
}

/** "*\/10 * * * *" → "every 10 minutes"; anything unusual stays as written. */
export function cronWords(s: string) {
  const f = s.trim().split(/\s+/);
  if (f.length !== 5) return s;
  const [m, h, dom, mon, dow] = f;
  const rest = dom === "*" && mon === "*" && dow === "*";
  if (rest && h === "*" && m === "*") return "every minute";
  if (rest && h === "*" && /^\*\/\d+$/.test(m)) return `every ${m.slice(2)} minutes`;
  if (rest && h === "*" && /^\d+$/.test(m)) return `hourly at :${m.padStart(2, "0")}`;
  if (rest && /^\d+$/.test(h) && /^\d+$/.test(m)) return `daily at ${h.padStart(2, "0")}:${m.padStart(2, "0")} UTC`;
  return s;
}

/**
 * Server status lines are written for terminals ("2 ban(s)", "[22 80 443]", "7m0s ago").
 * This turns the common shapes into plain words without changing what they say.
 */
export function plainWords(s: string): string {
  return s
    .replace(/\b(\d+)((?: [A-Za-z]+)*?) ([A-Za-z]+)\(s\)/g, (_, n: string, mid: string, w: string) => `${n}${mid} ${n === "1" ? w : `${w}s`}`)
    .replace(/\b1 (event|ban|error|job|run)s\b/g, "1 $1")
    .replace(/\[(\d+(?: \d+)+)\]/g, (_, list: string) => list.split(" ").join(", "))
    .replace(/\b(?:(\d+)h)?(\d+)m(\d+(?:\.\d+)?)s\b/g, (_, h?: string, m?: string) => (h && h !== "0" ? `${h} h ${Number(m)} min` : `${Number(m)} min`))
    .replace(/(\d) (GiB|MiB|KiB|GB|MB|KB|ms|%)/g, `$1${NNBSP}$2`);
}
