import { keepPreviousData, queryOptions, useQueries, useQuery } from "@tanstack/react-query";
import { request } from "@/api/client";
import { mod, mod3, type Deploy, type LogsResult } from "@/api/modules";
import type { Bucket } from "@/components/charts/bars";
import type { XY } from "@/components/charts/core";
import { merge, toLine, type Line, type Row } from "@/components/logs-query";
import { deploysQuery } from "@/lib/pulse";

/**
 * What one project's Observability page reads, beyond the box's ready-made
 * summaries: requests by status code, the window's totals and response
 * times, the busiest paths (from the edge's access log), each app's memory
 * and CPU over time, and its deploys as markers. Every query is restricted
 * to the project by the API (a project-scoped token can run them).
 */

export type Range = "1h" | "24h" | "7d" | "30d";
export const RANGE_MS: Record<Range, number> = { "1h": 3_600_000, "24h": 86_400_000, "7d": 7 * 86_400_000, "30d": 30 * 86_400_000 };
/** The usage history's step for each range (so every chart on the page shares its x positions). */
export const RANGE_STEP_S: Record<Range, number> = { "1h": 30, "24h": 600, "7d": 3600, "30d": 4 * 3600 };

const sel = (app?: string) => (app ? `{app=${JSON.stringify(app)}}` : "");
const win = (r: Range) => `${Math.max(60, RANGE_STEP_S[r])}s`;

type Matrix = Array<{ metric: Record<string, string>; values: Array<[number, string]> }>;
type Vector = Array<{ metric: Record<string, string>; value: [number, string] }>;

function metrics(project: string, query: string, range?: Range) {
  return request<{ resultType: string; result: unknown }>("POST", "/v1/observe/metrics/query", {
    project,
    query,
    ...(range ? { since: range, step: `${RANGE_STEP_S[range]}s` } : {}),
  });
}

/** Points per series, keyed by one label: [ms, value], oldest first. */
function byLabel(result: unknown, label: string): Record<string, XY[]> {
  const out: Record<string, XY[]> = {};
  for (const s of (result as Matrix) ?? []) {
    const k = s.metric?.[label] ?? "";
    out[k] = s.values.map(([t, v]) => [t * 1000, Number(v)] as XY).filter((p) => Number.isFinite(p[1]));
  }
  return out;
}

const scalar = (result: unknown) => {
  const v = (result as Vector)?.[0]?.value?.[1];
  const n = v === undefined ? NaN : Number(v);
  return Number.isFinite(n) ? n : undefined;
};

export const historyQuery = (project: string, range: Range, app?: string) =>
  queryOptions({
    queryKey: ["usage-history", project, range, app],
    queryFn: () => mod3.usageHistory(project, range, app),
    refetchInterval: range === "1h" ? 30_000 : 120_000,
    placeholderData: keepPreviousData,
    retry: false,
  });

/** Requests per minute by status class (2xx, 3xx, 4xx, 5xx) over the range. */
export function useStatusSplit(project: string, range: Range, app?: string, enabled = true) {
  return useQuery({
    queryKey: ["observe-status", project, range, app ?? ""],
    queryFn: async () => byLabel((await metrics(project, `sum by (code) (rate(tiffin_http_requests_total${sel(app)}[${win(range)}])) * 60`, range)).result, "code"),
    enabled,
    refetchInterval: range === "1h" ? 30_000 : 120_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export type WindowTotals = { requests: number; byCode: Record<string, number>; failed: number; p50?: number; p95?: number };

/** The whole window's totals: requests by status class, and response times over every request in it. */
export function useWindowTotals(project: string, range: Range, app?: string, enabled = true) {
  return useQuery({
    queryKey: ["observe-totals", project, range, app ?? ""],
    queryFn: async (): Promise<WindowTotals> => {
      const s = sel(app);
      const [codes, p50, p95] = await Promise.all([
        metrics(project, `sum by (code) (increase(tiffin_http_requests_total${s}[${range}]))`),
        metrics(project, `histogram_quantile(0.5, sum by (le) (increase(tiffin_http_request_duration_seconds_bucket${s}[${range}]))) * 1000`),
        metrics(project, `histogram_quantile(0.95, sum by (le) (increase(tiffin_http_request_duration_seconds_bucket${s}[${range}]))) * 1000`),
      ]);
      const byCode: Record<string, number> = {};
      for (const v of (codes.result as Vector) ?? []) {
        const n = Number(v.value?.[1]);
        if (Number.isFinite(n)) byCode[v.metric?.code ?? "other"] = Math.round(n);
      }
      const requests = Object.values(byCode).reduce((t, n) => t + n, 0);
      return { requests, byCode, failed: byCode["5xx"] ?? 0, p50: scalar(p50.result), p95: scalar(p95.result) };
    },
    enabled,
    refetchInterval: range === "1h" ? 30_000 : 120_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

/**
 * A route's requests in the window. p50 and p95 are exact for a route of one
 * address. For a folded route (paths > 1, `bound`) they are the slowest of
 * its addresses' own: quantiles don't add up, so that is all the per-path
 * numbers can honestly say. It is a true upper bound (a mix of groups never
 * has a quantile above its slowest group's).
 */
export type PathRow = { path: string; app?: string; requests: number; failed: number; p50: number; p95: number; paths: number; bound: boolean };

/**
 * /orders/1842 and /orders/1843 are one route: numbers, ids and hashes in a
 * path become ":id", so the table shows routes rather than every address.
 */
export function routeOf(path: string): string {
  return (
    path
      .split("/")
      .map((seg) => (/^\d+$/.test(seg) || /^[0-9a-f]{8}-[0-9a-f]{4}-/i.test(seg) || /^[0-9a-f]{16,}$/i.test(seg) || (/\d/.test(seg) && seg.length >= 12 && /^[\w-]+$/.test(seg)) ? ":id" : seg))
      .join("/") || "/"
  );
}

/** Per-path stats rows (path, requests, failed, p50, p95) folded into routes, busiest first. */
export function foldPaths(rows: Array<Record<string, unknown>>): PathRow[] {
  const groups = new Map<string, PathRow>();
  for (const row of rows) {
    const path = String(row.path ?? "");
    if (!path) continue;
    const key = routeOf(path);
    const g = groups.get(key) ?? { path: key, requests: 0, failed: 0, p50: 0, p95: 0, paths: 0, bound: false };
    g.requests += Number(row.requests) || 0;
    g.failed += Number(row.failed) || 0;
    g.p50 = Math.max(g.p50, Number(row.p50) || 0);
    g.p95 = Math.max(g.p95, Number(row.p95) || 0);
    g.paths++;
    g.bound = g.paths > 1;
    groups.set(key, g);
  }
  return [...groups.values()].sort((a, b) => b.requests - a.requests);
}

/**
 * The busiest paths in the window, from the edge's access log: requests,
 * 5xx answers, and p50/p95 response time, folded into routes (foldPaths).
 */
export function useTopPaths(project: string, range: Range, app?: string, enabled = true) {
  return useQuery({
    queryKey: ["observe-paths", project, range, app ?? ""],
    queryFn: async (): Promise<PathRow[]> => {
      const q = [
        "source:edge",
        app ? `app:=${JSON.stringify(app)}` : "",
        `-path:"/_tiffin/"*`,
        "| stats by (path) count() as requests, count() if (status:>=500) as failed, quantile(0.5, duration_ms) as p50, quantile(0.95, duration_ms) as p95",
        "| sort by (requests desc) limit 400",
      ]
        .filter(Boolean)
        .join(" ");
      const r: LogsResult = await mod.logs({ project, query: q, since: range, limit: 400 });
      return foldPaths(r.rows ?? []);
    },
    enabled,
    refetchInterval: 120_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

/** Each app's history over the range (memory and CPU by app), one query per app; shares the charts' cache. */
export function usePerAppHistory(project: string, apps: string[], range: Range, enabled = true) {
  const res = useQueries({ queries: apps.map((a) => ({ ...historyQuery(project, range, a), enabled })) });
  const out: Record<string, Record<string, XY[]>> = {};
  apps.forEach((a, i) => {
    const s = res[i].data?.series ?? {};
    out[a] = Object.fromEntries(
      Object.entries(s).map(([k, pts]) => [k, (pts ?? []).filter((p): p is number[] => !!p && p.length === 2).map((p) => [p[0] * 1000, p[1]] as XY)]),
    );
  });
  return { byApp: out, pending: res.some((r) => r.isPending && r.fetchStatus !== "idle") };
}

export type Marker = { t: number; label: string; failed?: boolean };

/** Each app's deploys in the range as markers: when it went live (or failed). */
export function useDeployMarkers(project: string, apps: string[], since: number): Marker[] {
  const res = useQueries({ queries: apps.map((a) => deploysQuery(project, a)) });
  const out: Marker[] = [];
  apps.forEach((a, i) => {
    for (const d of res[i].data ?? []) {
      if (d.preview) continue;
      const failed = d.status === "failed";
      const at = d.liveAt ?? (failed ? d.finishedAt : undefined);
      if (!at || (!failed && !["live", "superseded", "rolled_back", "stopped"].includes(d.status))) continue;
      const t = Date.parse(at);
      if (t < since) continue;
      const what = d.message ? `: ${d.message.length > 48 ? `${d.message.slice(0, 47)}…` : d.message}` : "";
      out.push({ t, label: failed ? `${a} deploy failed${what}` : `${a} deployed${what}`, failed });
    }
  });
  return out.sort((x, y) => x.t - y.t);
}

/**
 * Builds per step over the span ending now, from the deploy history: built
 * (it has a build time) and failed. Counted from each deploy's current
 * status, so a build that finishes or fails moves to its column on the next
 * poll.
 */
export function buildBuckets(deploys: Array<Pick<Deploy, "status" | "buildSeconds" | "createdAt">>, now: number, span: number, step: number): Bucket[] {
  const start = Math.floor((now - span) / step) * step + step;
  const n = Math.round(span / step);
  const out: Bucket[] = Array.from({ length: n }, (_, i) => ({ t: start + i * step, values: { ok: 0, failed: 0 } }));
  for (const d of deploys) {
    if (!d.buildSeconds && d.status !== "failed") continue;
    const i = Math.floor((Date.parse(d.createdAt) - start) / step);
    if (i < 0 || i >= n) continue;
    out[i].values[d.status === "failed" ? "failed" : "ok"]++;
  }
  return out;
}

// ---- live logs ----

/** One page of a logs query: newest first, and whether the limit cut it. */
export type LogPage = { rows?: Row[] | null; truncated: boolean };

/**
 * A stretch of a live tail that came too fast to read in full: some lines
 * logged after the line `after` (from) and before `to` were never fetched.
 * `skipped` is how many, when the store could count them.
 */
export type LogGap = { after: string; from: string; to: string; skipped?: number };

/**
 * Everything logged since the checkpoint line (the start is inclusive),
 * newest page first: while a page comes back full, asks again for the lines
 * up to its oldest one, at most maxPages times. What it couldn't reach is
 * returned as a gap, never dropped silently.
 */
export async function drainSince(
  page: (end?: string) => Promise<LogPage>,
  since: Line,
  maxPages: number,
): Promise<{ lines: Line[]; gap?: { from: string; to: string } }> {
  const got: Line[] = [];
  let end: string | undefined;
  for (let i = 0; i < maxPages; i++) {
    const r = await page(end);
    const rows = (r.rows ?? []).map(toLine);
    got.push(...rows);
    if (!r.truncated) return { lines: merge(got) };
    const oldest = rows.reduce<Line | undefined>((o, l) => (Number.isFinite(l.t) && (!o || l.t < o.t) ? l : o), undefined);
    if (!oldest || oldest.t <= since.t) return { lines: merge(got) }; // back at the checkpoint (or rows with no time to page by)
    if (oldest.iso === end) break; // a whole page from one instant: there's no paging past it
    end = oldest.iso;
  }
  return { lines: merge(got), gap: end ? { from: since.iso, to: end } : undefined };
}

/** A live tail's state: its lines (oldest first), the ones that just arrived, and its gaps. */
export type LiveTail = { lines: Line[]; fresh: Set<string>; gaps: LogGap[] };

/**
 * Adds what a poll read to a live tail: new lines marked fresh, at most cap
 * lines kept (the oldest go first), and a gap kept only while the line it
 * follows is still shown.
 */
export function appendLive<T extends LiveTail>(prev: T, add: Line[], gap: LogGap | undefined, cap: number): T {
  const before = new Set(prev.lines.map((l) => l.key));
  const fresh = new Set(add.filter((l) => !before.has(l.key)).map((l) => l.key));
  if (!fresh.size && !gap) return { ...prev, fresh };
  const lines = merge(prev.lines, add).slice(-cap);
  const shown = new Set(lines.map((l) => l.key));
  const gaps = [...prev.gaps, ...(gap ? [gap] : [])].filter((g) => shown.has(g.after));
  return { ...prev, lines, fresh, gaps };
}

/** A row of the logs list: a line, or the marker of a gap after one. */
export type LogItem = { type: "line"; key: string; line: Line; pos: number } | { type: "gap"; key: string; gap: LogGap };

/** The logs list's rows: every line (numbered), and a marker after each line a live tail lost lines after. */
export function logItems(lines: Line[], gaps?: LogGap[]): LogItem[] {
  const after = new Map<string, LogGap[]>();
  for (const g of gaps ?? []) after.set(g.after, [...(after.get(g.after) ?? []), g]);
  const out: LogItem[] = [];
  lines.forEach((line, i) => {
    out.push({ type: "line", key: line.key, line, pos: i + 1 });
    for (const gap of after.get(line.key) ?? []) out.push({ type: "gap", key: `\u0000gap|${gap.after}|${gap.to}`, gap });
  });
  return out;
}
