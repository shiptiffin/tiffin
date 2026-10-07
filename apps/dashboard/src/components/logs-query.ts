/**
 * The Logs page's model: the URL holds one LogsQL query, and the chips are
 * read out of it. `app:=web level:in(error,fatal) timeout` shows as an App
 * chip, a Level chip and "timeout" in the search box; changing a chip
 * rewrites the query. What you see in the box is what runs, and a link
 * carries every filter.
 */

export type Row = Record<string, unknown>;

// ---- facets ----

export type Facet = "source" | "app" | "env" | "level" | "unit";
export type Facets = Partial<Record<Facet, string[]>>;

export const LEVELS = [
  { v: "error", label: "Error", match: ["error", "err", "fatal", "panic", "critical", "crit", "alert", "emerg"] },
  { v: "warn", label: "Warning", match: ["warn", "warning"] },
  { v: "info", label: "Info", match: ["info", "notice"] },
  { v: "debug", label: "Debug", match: ["debug", "trace"] },
] as const;
export type Level = (typeof LEVELS)[number]["v"] | "";

/** Where a project's lines come from. Builds are listed from deploys; boxes that ship build output also keep its lines (source:build). */
export const SOURCES = [
  { v: "app", label: "Runtime", hint: "What your apps print" },
  { v: "edge", label: "Requests", hint: "Every request the edge served" },
  { v: "errors", label: "Errors", hint: "Errors your apps reported" },
  { v: "otlp", label: "OpenTelemetry", hint: "Logs sent over OTLP" },
  { v: "build", label: "Builds", hint: "Build output, per deploy" },
] as const;

export const sourceLabel = (v: string) => SOURCES.find((s) => s.v === v)?.label ?? v;
export const levelGroup = (raw: string): Level => {
  const l = raw.toLowerCase();
  return LEVELS.find((g) => (g.match as readonly string[]).includes(l))?.v ?? "";
};

const FACET_RE = /^(source|app|env|level|unit):(.+)$/;
const BARE = /^[A-Za-z0-9_]+$/;
const quote = (v: string) => (BARE.test(v) ? v : JSON.stringify(v));

function unquote(s: string): string | null {
  const t = s.trim();
  if (!t) return null;
  if ((t.startsWith('"') && t.endsWith('"')) || (t.startsWith("'") && t.endsWith("'"))) {
    try {
      return t.startsWith('"') ? (JSON.parse(t) as string) : t.slice(1, -1);
    } catch {
      return null;
    }
  }
  return /^[^\s()*~"'`,|]+$/.test(t) ? t : null;
}

/** Splits on top-level whitespace, keeping quotes and parentheses whole. Null when unbalanced. */
function tokens(q: string): string[] | null {
  const out: string[] = [];
  let cur = "";
  let depth = 0;
  let quoteCh: string | null = null;
  for (let i = 0; i < q.length; i++) {
    const c = q[i];
    if (quoteCh) {
      cur += c;
      if (c === "\\" && i + 1 < q.length) cur += q[++i];
      else if (c === quoteCh) quoteCh = null;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") quoteCh = c;
    else if (c === "(") depth++;
    else if (c === ")") depth--;
    if (/\s/.test(c) && depth === 0) {
      if (cur) out.push(cur);
      cur = "";
      continue;
    }
    cur += c;
  }
  if (quoteCh || depth !== 0) return null;
  if (cur) out.push(cur);
  return out;
}

/** The filter before the first top-level pipe, and the pipes after it. */
function splitPipe(q: string): [string, string] {
  let depth = 0;
  let quoteCh: string | null = null;
  for (let i = 0; i < q.length; i++) {
    const c = q[i];
    if (quoteCh) {
      if (c === "\\") i++;
      else if (c === quoteCh) quoteCh = null;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") quoteCh = c;
    else if (c === "(") depth++;
    else if (c === ")") depth--;
    else if (c === "|" && depth === 0) return [q.slice(0, i).trim(), q.slice(i).trim()];
  }
  return [q.trim(), ""];
}

function values(raw: string): string[] | null {
  let v = raw;
  if (/^in\(.*\)$/s.test(v)) {
    const inner = v.slice(3, -1);
    const parts: string[] = [];
    let cur = "";
    let quoteCh: string | null = null;
    for (let i = 0; i < inner.length; i++) {
      const c = inner[i];
      if (quoteCh) {
        cur += c;
        if (c === "\\") cur += inner[++i] ?? "";
        else if (c === quoteCh) quoteCh = null;
      } else if (c === '"' || c === "'") {
        quoteCh = c;
        cur += c;
      } else if (c === ",") {
        parts.push(cur);
        cur = "";
      } else cur += c;
    }
    parts.push(cur);
    const out = parts.map(unquote);
    return out.every((x): x is string => x !== null) ? out : null;
  }
  if (v.startsWith("=")) v = v.slice(1);
  const one = unquote(v);
  return one === null ? null : [one];
}

export type Parsed = { facets: Facets; text: string; advanced: boolean };

/** Reads the chips out of a query. Queries with a top-level OR stay whole: their chips can't be read. */
export function parse(q: string | undefined): Parsed {
  const full = (q ?? "").trim();
  if (!full || full === "*") return { facets: {}, text: "", advanced: false };
  const [filter, pipes] = splitPipe(full);
  const toks = tokens(filter);
  if (!toks || toks.some((t) => /^or$/i.test(t))) return { facets: {}, text: full, advanced: true };
  const facets: Facets = {};
  const rest: string[] = [];
  for (const t of toks) {
    if (/^and$/i.test(t) || t === "*") continue;
    const m = FACET_RE.exec(t);
    const vs = m ? values(m[2]) : null;
    if (!m || !vs) {
      rest.push(t);
      continue;
    }
    const f = m[1] as Facet;
    const add = f === "level" ? [...new Set(vs.map(levelGroup).filter(Boolean))] : vs;
    if (f === "level" && add.length === 0) {
      rest.push(t);
      continue;
    }
    facets[f] = [...new Set([...(facets[f] ?? []), ...add])];
  }
  const text = [rest.join(" "), pipes].filter(Boolean).join(" ");
  return { facets, text, advanced: !!pipes };
}

/** One facet as LogsQL: exact matches, `in(…)` for several, every spelling of a level. */
export function facetQL(f: Facet, vs: string[]): string {
  if (!vs.length) return "";
  if (f === "level") {
    const all = vs.flatMap((v) => [...(LEVELS.find((g) => g.v === v)?.match ?? [v])]);
    return `level:in(${all.map(quote).join(",")})`;
  }
  return vs.length === 1 ? `${f}:=${quote(vs[0])}` : `${f}:in(${vs.map(quote).join(",")})`;
}

const ORDER: Facet[] = ["source", "app", "unit", "env", "level"];

/** The query for these chips and this text; "*" for everything. */
export function compose(facets: Facets, text: string): string {
  const parts = ORDER.map((f) => facetQL(f, facets[f] ?? [])).filter(Boolean);
  let t = text.trim();
  if (t) {
    const [filter, pipes] = splitPipe(t);
    const toks = tokens(filter);
    const or = !toks || toks.some((x) => /^or$/i.test(x));
    t = [parts.length && or && filter ? `(${filter})` : filter, pipes].filter(Boolean).join(" ");
  }
  return [...parts, t].filter(Boolean).join(" ") || "*";
}

/** The query without one facet (for that facet's counts). */
export const without = (p: Parsed, f: Facet) => compose({ ...p.facets, [f]: undefined }, p.text);
/** The query's filter part, without pipes (for stats). */
export const filterOf = (q: string) => splitPipe(q)[0] || "*";
export const hasPipes = (q: string) => !!splitPipe(q)[1];

// ---- time range ----

export type Range = { kind: "rel"; since: string; ms: number } | { kind: "abs"; from: number; to: number };

const UNIT: Record<string, number> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 };
export const durMs = (s: string) => {
  const m = /^(\d+)([smhd])$/.exec(s);
  return m ? Number(m[1]) * UNIT[m[2]] : NaN;
};

export const PRESETS = ["15m", "1h", "6h", "24h", "3d", "7d", "14d", "30d"];

/** `since` is a window ("1h") or an absolute range ("<iso>~<iso>", from a zoom). */
export function parseRange(since: string | undefined): Range {
  if (since?.includes("~")) {
    const [a, b] = since.split("~");
    const from = Date.parse(a);
    const to = Date.parse(b);
    if (Number.isFinite(from) && Number.isFinite(to) && from < to) return { kind: "abs", from, to };
  }
  const ms = durMs(since ?? "");
  return Number.isFinite(ms) && ms > 0 ? { kind: "rel", since: since!, ms } : { kind: "rel", since: "1h", ms: 3_600_000 };
}

export const absRange = (from: number, to: number) => `${new Date(Math.floor(from)).toISOString()}~${new Date(Math.ceil(to)).toISOString()}`;
export const rangeSpan = (r: Range) => (r.kind === "rel" ? r.ms : r.to - r.from);
export const rangeBody = (r: Range) =>
  r.kind === "rel" ? { since: r.since } : { start: new Date(r.from).toISOString(), end: new Date(r.to).toISOString() };

const day = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short" });
const hm = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const hms = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });

/** "Last hour", or "6 Oct, 10:00 – 10:15" for a zoomed range. */
export function rangeWords(r: Range): string {
  if (r.kind === "rel") {
    const n = durMs(r.since);
    const m = /^(\d+)([smhd])$/.exec(r.since)!;
    const k = Number(m[1]);
    if (n === 86_400_000) return "Last 24 hours";
    const unit = { s: "seconds", m: "minutes", h: k === 1 ? "hour" : "hours", d: k === 1 ? "day" : "days" }[m[2]];
    return k === 1 ? `Last ${unit}` : `Last ${k} ${unit}`;
  }
  const short = r.to - r.from < 120_000 ? hms : hm;
  const sameDay = day.format(r.from) === day.format(r.to);
  return sameDay ? `${day.format(r.from)}, ${short.format(r.from)} – ${short.format(r.to)}` : `${day.format(r.from)} ${short.format(r.from)} – ${day.format(r.to)} ${short.format(r.to)}`;
}
export const presetShort = (s: string) => s.replace(/^(\d+)([mhd])$/, "$1$2");

// ---- histogram steps ----

const STEPS = ["1s", "2s", "5s", "10s", "15s", "30s", "1m", "2m", "5m", "10m", "15m", "30m", "1h", "2h", "3h", "6h", "12h", "1d"];
/** A round bucket size that gives at most `want` bars over `span`. */
export function stepFor(span: number, want = 72): { step: string; ms: number } {
  for (const s of STEPS) if (span / durMs(s) <= want) return { step: s, ms: durMs(s) };
  return { step: "1d", ms: 86_400_000 };
}

// ---- lines ----

export type Line = {
  key: string;
  t: number;
  iso: string;
  level: Level;
  raw: string;
  source: string; // the app (project) or service (box) that wrote it
  kind: string; // app, edge, errors, otlp, or "" for box lines
  msg: string;
  row: Row;
};

// ---- level inference ----
// The box sets `level` on lines that say what they are (observe's
// inferLevel in internal/mod/observe/level.go); this is the same rule, for
// lines stored before it did. Keep the two identical: the Go test cases in
// internal/mod/observe/testdata/levels.json must pass here too ("warning"
// there is "warn" here). Conservative on purpose: "0 errors" and
// "error_count=0" stay unmarked.

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[A-Za-z]/g;
const STEP_PREFIX = /^(?:#[0-9]+ (?:[0-9]+(?:\.[0-9]+)? )?|==> )/; // BuildKit "#8 12.3 ", the box's "==> "
const LOGFMT = /(?:^|[ \t])level=["']?([A-Za-z]+)/;
const ERROR_RES = [
  /^(?:(?:npm|pnpm|yarn) )?\[?(?:error|fatal|panic|critical|err!)\]?(?:[ \t:]|$)/i,
  /\[(?:error|fatal|panic|critical)\]/i,
  /\b(?:PANIC|FATAL|ERROR):|\bFATAL\b|\bUnhandledPromiseRejection|^Uncaught\b|^(?:[A-Z][A-Za-z]*)?(?:Error|Exception): |^(?:[a-z_][a-z0-9_]*\.)+[A-Z]\w*: |^FAILED:|exited with code [1-9]/,
];
const WARN_RES = [/^(?:(?:npm|pnpm|yarn) )?\[?(?:warn|warning)\]?(?:[ \t:]|$)/i, /\[(?:warn|warning)\]/i, /\bWARNING:/];

/** pino's numeric levels: 10 trace … 60 fatal. */
function pinoLevel(raw: string): Level {
  if (!/^[0-9]+$/.test(raw)) return "";
  const n = Number(raw);
  if (n < 10 || n > 60) return "";
  return n >= 50 ? "error" : n >= 40 ? "warn" : n >= 30 ? "info" : "debug";
}

/** What a line with no level field says about itself: error, warn, a logfmt level, or nothing. */
export function inferLevel(msg: string): Level {
  const s = msg.replace(ANSI, "").replace(/^[ \t]+/, "").replace(STEP_PREFIX, "");
  const m = LOGFMT.exec(s);
  if (m) {
    const g = levelGroup(m[1]);
    if (g) return g;
  }
  if (ERROR_RES.some((re) => re.test(s))) return "error";
  if (WARN_RES.some((re) => re.test(s))) return "warn";
  return "";
}

/** A level field's group: a named level, or pino's numbers. */
const fieldLevel = (raw: string): Level => levelGroup(raw) || pinoLevel(raw);

/**
 * The level of one raw line an app printed (not yet a stored record), as
 * the box would store it: a JSON line's level field (level, severity or
 * lvl) when it has one, else what its message, or the line, says.
 */
export function lineLevel(text: string): Level {
  const t = text.trim();
  if (t.startsWith("{")) {
    try {
      const j = JSON.parse(t) as Record<string, unknown>;
      if (j && typeof j === "object" && !Array.isArray(j)) {
        for (const k of ["level", "severity", "lvl"]) if (j[k] !== undefined && j[k] !== null && j[k] !== "") return fieldLevel(String(j[k]));
        const msg = j.msg ?? j.message;
        return inferLevel(msg === undefined ? t : String(msg));
      }
    } catch {
      // not JSON: a plain line
    }
  }
  return inferLevel(text);
}

/** A level for every line: the field when there is one, else what the line itself says. */
function levelOf(r: Row): { level: Level; raw: string } {
  const raw = String(r.level ?? "");
  const status = Number(r.status);
  if (status >= 500) return { level: "error", raw: raw || "error" };
  if (raw) {
    const g = levelGroup(raw);
    if (g) return { level: g, raw };
    const p = fieldLevel(raw);
    return { level: p, raw: p || raw };
  }
  const level = inferLevel(String(r._msg ?? ""));
  return { level, raw: level };
}

export function sourceOf(r: Row): string {
  if (r.app) return String(r.app);
  if (r.unit) return String(r.unit).replace(/\.service$/, "").replace(/^tiffin-/, "");
  if (r.source === "edge" && r.host) return String(r.host);
  return String(r.source ?? "");
}

export function toLine(r: Row): Line {
  const iso = String(r._time ?? "");
  const t = Date.parse(iso);
  const msg = String(r._msg ?? "");
  const { level, raw } = levelOf(r);
  const key = `${iso}|${String(r.app ?? r.unit ?? "")}|${String(r.instance ?? "")}|${msg}`;
  return { key, t, iso, level, raw, source: sourceOf(r), kind: String(r.source ?? ""), msg, row: r };
}

/** Merges batches of lines into one list, oldest first, without repeats. */
export function merge(...lists: Line[][]): Line[] {
  const seen = new Set<string>();
  const out: Line[] = [];
  for (const l of lists.flat()) {
    if (seen.has(l.key)) continue;
    seen.add(l.key);
    out.push(l);
  }
  return out.sort((a, b) => a.t - b.t || (a.iso < b.iso ? -1 : a.iso > b.iso ? 1 : 0));
}

/** The fields that say which stream a line belongs to, for "show surrounding lines". */
export function streamOf(r: Row): string {
  const parts: string[] = [];
  for (const f of ["unit", "source", "app", "env"] as const) {
    const v = r[f];
    if (typeof v === "string" && v) parts.push(`${f}:=${quote(v)}`);
  }
  if (r.source === "edge" && !r.app && typeof r.host === "string") parts.push(`host:=${quote(r.host)}`);
  return parts.join(" ") || "*";
}

/** A field as a filter you can add to the query. */
export const fieldQL = (k: string, v: string) => `${/^[A-Za-z_][A-Za-z0-9_.]*$/.test(k) ? k : JSON.stringify(k)}:=${quote(v)}`;

// ---- export ----

export const asText = (lines: Line[]) => lines.map((l) => [l.iso, (l.raw || l.level || "-").padEnd(5), l.source || "-", l.msg].join("  ")).join("\n");
export const asNdjson = (lines: Line[]) => lines.map((l) => JSON.stringify(l.row)).join("\n");

// ---- volume ----

export type VolumeBucket = { t: number; error: number; warn: number; other: number };

/** Empty columns from `from` to `to`, epoch-aligned like the store's `_time:<step>` buckets. */
export function emptyBuckets(from: number, to: number, step: number): VolumeBucket[] {
  const out: VolumeBucket[] = [];
  const start = Math.floor(from / step) * step;
  for (let t = start; t < to && out.length < 400; t += step) out.push({ t, error: 0, warn: 0, other: 0 });
  return out;
}

/** Fills columns from `stats by (_time:<step>, level) count() hits` rows. */
export function fillBuckets(buckets: VolumeBucket[], step: number, rows: Row[]): VolumeBucket[] {
  if (!buckets.length) return buckets;
  const out = buckets.map((b) => ({ ...b }));
  const t0 = out[0].t;
  for (const r of rows) {
    const t = Date.parse(String(r._time ?? ""));
    const i = Math.floor((t - t0) / step);
    if (!(i >= 0 && i < out.length)) continue;
    const n = Number(r.hits ?? 0) || 0;
    const g = levelGroup(String(r.level ?? ""));
    if (g === "error") out[i].error += n;
    else if (g === "warn") out[i].warn += n;
    else out[i].other += n;
  }
  return out;
}
