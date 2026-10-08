import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChartLine, Table2 } from "lucide-react";
import { useId, useMemo, useState } from "react";
import type { UsageHistory } from "@/api/modules";
import { Legend, StackedBars, type Bucket } from "@/components/charts/bars";
import type { XY } from "@/components/charts/core";
import { SeriesTable, type Series } from "@/components/charts/time-series";
import { Crosshair, ObserveChart, useCrosshairTime, valueAt, type ChartMarker, type ChartSeries } from "@/components/observe-chart";
import { buildBuckets, historyQuery, RANGE_MS, useDeployMarkers, usePerAppHistory, useStatusSplit, type Range } from "@/components/observe-data";
import { Skeleton } from "@/components/page";
import { cn } from "@/lib/cn";
import { bytes, dec, int, NNBSP, num, pct } from "@/lib/format";
import { partName } from "@/lib/names";
import { deploysQuery } from "@/lib/pulse";
import type { ProjectUsage } from "@/lib/usage";

/**
 * A project's charts over time, for its Observability page: traffic
 * (requests by status code, response time, failures) or resources (memory
 * and CPU by app against the limit, its data, builds). Every chart shares
 * one crosshair and shows the apps' deploys as markers. From the box's
 * metrics store, about 150 points per chart whatever the range.
 */

export type { Range } from "@/components/observe-data";
export const ranges: Array<{ value: Range; label: string; short: string }> = [
  { value: "1h", label: "1 hour", short: "1 h" },
  { value: "24h", label: "24 hours", short: "24 h" },
  { value: "7d", label: "7 days", short: "7 d" },
  { value: "30d", label: "30 days", short: "30 d" },
];

/** Which charts: traffic (requests, response time, errors) or resources (memory, CPU, data, builds). */
export type ChartSet = "traffic" | "resources";

const cpuFmt = (v: number) => `${dec(v, v < 10 ? 1 : 0)}%`;
const msFmt = (v: number) => (v >= 1000 ? `${dec(v / 1000, 2)}${NNBSP}s` : `${int(v)}${NNBSP}ms`);
const perMin = (v: number) => num(Math.round(v * 10) / 10);

/** Status classes: answered green, redirects blue, client errors amber, server errors red. */
const STATUS: Array<{ code: string; label: string; color: string }> = [
  { code: "2xx", label: "2xx", color: "var(--ok)" },
  { code: "3xx", label: "3xx", color: "var(--data-2)" },
  { code: "4xx", label: "4xx", color: "var(--warn)" },
  { code: "5xx", label: "5xx", color: "var(--danger)" },
];

export function UsageCharts({
  project,
  apps,
  usage,
  only,
  title,
  range,
  app,
  tables = false,
  className = "mt-12",
  quiet,
}: {
  project: string;
  apps: string[];
  usage?: ProjectUsage;
  only: ChartSet;
  title: string;
  range: Range;
  /** One app, or the whole project. */
  app?: string;
  tables?: boolean;
  className?: string;
  /** The page already says the metrics store is down: don't say it again here. */
  quiet?: boolean;
}) {
  const h = useQuery(historyQuery(project, range, app));
  const traffic = only === "traffic";
  const status = useStatusSplit(project, range, app, traffic && !h.isError);
  // Memory and CPU by app, when the project has a few and none is picked.
  const split = !traffic && !app && apps.length > 1;
  const per = usePerAppHistory(project, split ? apps : [], range, split && !h.isError);
  const [mounted] = useState(() => Date.now());
  const d = h.data;
  const step = (d?.stepSeconds ?? 600) * 1000;
  const to = d ? Date.parse(d.to) : mounted;
  const from = to - RANGE_MS[range];
  const markers = useDeployMarkers(project, app ? [app] : apps, from);
  const s = d?.series ?? {};
  const headId = useId();

  // A limit line from the series (its latest value), else from what the box reports now.
  const last = (k: string) => {
    const pts = s[k];
    return pts?.length ? (pts[pts.length - 1] as number[])[1] : undefined;
  };
  const memLimit = last("memoryLimit") ?? (!app && usage && usage.memory.limitSource !== "automatic" && usage.memory.limitBytes ? usage.memory.limitBytes : undefined);
  const cpuLimit = last("cpuLimit") ?? (!app && usage?.cpu.limitCpus ? usage.cpu.limitCpus * 100 : undefined);

  const charts: ChartSpec[] = [];
  if (traffic) {
    const codes = status.data ?? {};
    const present = STATUS.filter((x) => codes[x.code]?.some((p) => p[1] > 0));
    charts.push(
      present.length
        ? {
            id: "requests",
            title: "Requests",
            note: "per minute, by status",
            series: present.map((x) => ({ id: x.code, label: x.label, color: x.color, points: codes[x.code] })),
            stacked: true,
            format: perMin,
            legend: present.map((x) => ({ label: x.label, color: x.color })),
            empty: "No requests in this time.",
          }
        : { id: "requests", title: "Requests", note: "per minute", series: [line(s.requests, "Requests per minute", true)], format: perMin, empty: "No requests in this time." },
      {
        id: "latency",
        title: "Response time",
        note: "95% and half of requests within",
        series: [line(s.p95, "95% within"), { ...line(s.p50, "Half within"), color: "var(--data-2)" }],
        format: msFmt,
        legend: [
          { label: "95% within", color: "var(--data)", line: true },
          { label: "Half within", color: "var(--data-2)", line: true },
        ],
        empty: "No requests in this time.",
      },
      { id: "errors", title: "Failed requests", note: "share answered with a 5xx", series: [line(s.errors, "Failed", true)], format: (v) => pct(v, v < 0.1 ? 1 : 0), minTop: 0.01, empty: "No requests in this time." },
    );
  } else {
    const byApp = (k: "memory" | "cpu") => apps.map((a) => ({ id: a, label: a, points: per.byApp[a]?.[k] ?? [] })).filter((x) => x.points.length > 0);
    const memApps = split ? byApp("memory") : [];
    const cpuApps = split ? byApp("cpu") : [];
    charts.push(
      memApps.length > 1 ? { ...stackedBytes("memory", "Memory", memApps, memLimit), note: "by app", legend: legendFor(memApps), empty: "No readings yet." } : { ...byteChart("memory", "Memory", s.memory, memLimit), empty: "No readings yet." },
      cpuApps.length > 1
        ? { id: "cpu", title: "CPU", note: "by app · 100% is one core", series: cpuApps, stacked: true, format: cpuFmt, legend: legendFor(cpuApps), limit: cpuLimit ? { value: cpuLimit, label: `Limit ${cpuFmt(cpuLimit)}` } : undefined, empty: "No readings yet." }
        : { id: "cpu", title: "CPU", note: "100% is one core", series: [line(s.cpu, "CPU", true)], format: cpuFmt, limit: cpuLimit ? { value: cpuLimit, label: `Limit ${cpuFmt(cpuLimit)}` } : undefined, empty: "No readings yet." },
    );
    if (s.database) charts.push(byteChart("database", `${partName("postgres")} size`, s.database));
    if (s.connections)
      charts.push({
        id: "connections",
        title: `${partName("postgres")} connections`,
        series: [line(s.connections, "Open connections", true)],
        format: int,
        limit: usage?.database?.connectionLimit ? { value: usage.database.connectionLimit, label: `Limit ${usage.database.connectionLimit}` } : undefined,
      });
    if (s.kv) charts.push(byteChart("kv", `${partName("valkey")} memory`, s.kv, usage?.cache?.limitBytes || undefined));
    if (s.files) charts.push(byteChart("files", `${partName("storage")} stored`, s.files));
  }

  const down = h.isError;
  return (
    <section className={className} aria-labelledby={headId}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id={headId} className="text-[0.9375rem] font-[550] text-ink">
          {title}
        </h2>
        {markers.length > 0 && !down && (
          <p className="inline-flex items-center gap-1.5 text-[0.75rem] text-ink-3">
            <svg aria-hidden width="9" height="7" viewBox="0 0 9 7" className="shrink-0">
              <path d="M1,0.5 h7 l-3.5,5 z" fill="var(--ink-3)" />
            </svg>
            {markers.length === 1 ? "One deploy" : `${int(markers.length)} deploys`} in this time
          </p>
        )}
      </div>
      {down && !quiet && (
        <p className="mt-3 max-w-[46rem] text-[0.875rem] text-ink-2">
          {h.error instanceof Error && /not reachable/.test(h.error.message) ? "The box’s metrics store isn’t answering, so there’s nothing to draw yet. The charts fill in by themselves once it is. " : "Charts appear once the box has been measuring for a few minutes. "}
          <Link to="/status" className="text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink-3">
            Health
          </Link>{" "}
          shows how the box is doing.
        </p>
      )}
      {!d && !down ? (
        <div className="mt-5 grid gap-x-10 gap-y-8 md:grid-cols-2 xl:grid-cols-3">
          {charts.slice(0, 3).map((c) => (
            <Skeleton key={c.id} className="h-[212px]" />
          ))}
        </div>
      ) : (
        <Crosshair>
          <div className={cn("mt-5 grid gap-x-10 gap-y-9 md:grid-cols-2 xl:grid-cols-3 transition-opacity", h.isPlaceholderData && "opacity-60")}>
            {charts.map((c) => (
              <Chart key={c.id} c={c} step={step} tables={tables} range={range} from={from} to={to} markers={markers} waiting={down} />
            ))}
            {!traffic && !down && <Builds project={project} apps={app ? [app] : apps} range={range} tables={tables} />}
          </div>
        </Crosshair>
      )}
    </section>
  );
}

/**
 * Charts or a table of the same numbers. The button names what a click
 * gives you: "Table" while the charts show, "Charts" while the table does.
 */
export function ViewToggle({ tables, onToggle }: { tables: boolean; onToggle: () => void }) {
  const Icon = tables ? ChartLine : Table2;
  const word = tables ? "Charts" : "Table";
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-label={tables ? "Show as charts" : "Show as a table"}
      title={tables ? "Show as charts" : "Show as a table"}
      className="inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-[0.8125rem] text-ink-3 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover hover:text-ink"
    >
      <Icon aria-hidden className="size-4" />
      <span className="max-sm:sr-only">{word}</span>
    </button>
  );
}

type ChartSpec = {
  id: string;
  title: string;
  note?: string;
  series: ChartSeries[];
  stacked?: boolean;
  format: (v: number) => string;
  limit?: { value: number; label: string };
  legend?: Array<{ label: string; color: string; line?: boolean }>;
  minTop?: number;
  empty?: string;
};

const MB = 1048576;
const GB = 1073741824;
const BAND = ["var(--data)", "var(--data-2)", "var(--chart-3)", "var(--chart-4)", "var(--rule-3)"];

function legendFor(series: ChartSeries[]) {
  return series.map((x, i) => ({ label: x.label, color: BAND[i % BAND.length] }));
}

/** Bytes drawn in MB or GB, so the axis reads in round numbers (0 / 200 / 400 MB). */
function byteUnit(most: number): { unit: number; format: (v: number) => string } {
  const [unit, name, digits] = most >= GB ? [GB, "GB", 2] : [MB, "MB", 0];
  return { unit, format: (v: number) => `${dec(v, v && v < 10 ? Math.max(digits, 1) : digits)}${NNBSP}${name}` };
}

function byteChart(id: string, title: string, pts: UsageHistory["series"][string] | undefined, limit?: number): ChartSpec {
  const raw = line(pts, title, true);
  const { unit, format } = byteUnit(Math.max(limit ?? 0, ...raw.points.map((p) => p[1])));
  return {
    id,
    title,
    series: [{ ...raw, points: raw.points.map((p) => [p[0], p[1] / unit] as XY) }],
    format,
    limit: limit ? { value: limit / unit, label: `Limit ${bytes(limit, 0)}` } : undefined,
  };
}

function stackedBytes(id: string, title: string, series: ChartSeries[], limit?: number): ChartSpec {
  const totals = new Map<number, number>();
  for (const x of series) for (const p of x.points) totals.set(p[0], (totals.get(p[0]) ?? 0) + p[1]);
  const { unit, format } = byteUnit(Math.max(limit ?? 0, ...totals.values()));
  return {
    id,
    title,
    series: series.map((x, i) => ({ ...x, color: BAND[i % BAND.length], points: x.points.map((p) => [p[0], p[1] / unit] as XY) })),
    stacked: true,
    format,
    limit: limit ? { value: limit / unit, label: `Limit ${bytes(limit, 0)}` } : undefined,
  };
}

function line(pts: UsageHistory["series"][string] | undefined, label: string, area = false): ChartSeries {
  return { id: label, label, area, points: (pts ?? []).filter((p): p is number[] => !!p && p.length === 2).map((p) => [p[0] * 1000, p[1]] as XY) };
}

function Chart({ c, step, tables, range, from, to, markers, waiting }: { c: ChartSpec; step: number; tables: boolean; range: Range; from: number; to: number; markers: ChartMarker[]; waiting?: boolean }) {
  const t = useCrosshairTime();
  // The headline value: the stack's total or the first line, at the crosshair or now.
  const head = useMemo<XY[]>(() => {
    if (!c.stacked) return c.series[0]?.points ?? [];
    const sum = new Map<number, number>();
    for (const x of c.series) for (const p of x.points) sum.set(p[0], (sum.get(p[0]) ?? 0) + p[1]);
    return [...sum.entries()].sort((a, b) => a[0] - b[0]);
  }, [c]);
  const v = valueAt(head, t, step);
  const span = ranges.find((r) => r.value === range)!.label;
  const id = useId();
  const has = c.series.some((x) => x.points.length > 0);
  const table: Series[] = c.series.map((x) => ({ id: x.id, label: x.label, points: x.points }));
  return (
    <figure className="min-w-0" aria-labelledby={id}>
      <div className="flex items-baseline justify-between gap-3 border-b border-rule pb-1.5">
        <div className="min-w-0">
          <span id={id} className="block text-[0.875rem] text-ink">
            {c.title}
          </span>
          {c.note && <span className="block truncate text-[0.75rem] text-ink-3">{c.note}</span>}
        </div>
        {v !== undefined && Number.isFinite(v) && (
          <span className={cn("shrink-0 text-[0.9375rem] font-[500] tnum", t === null ? "text-ink" : "text-ink-2")}>{c.format(v)}</span>
        )}
      </div>
      <div className="mt-2 min-h-[18px]">{c.legend && has && <Legend items={c.legend} />}</div>
      <div className="mt-2">
        {tables && has ? (
          <SeriesTable series={table} step={step} format={c.format} caption={`${c.title}, the last ${span}`} />
        ) : (
          <ObserveChart
            series={c.series}
            stacked={c.stacked}
            step={step}
            format={c.format}
            label={`${c.title}, the last ${span}`}
            limit={c.limit}
            minTop={c.minTop}
            markers={markers}
            from={from}
            to={to}
            empty={waiting ? "Waiting for the first readings" : (c.empty ?? "No readings yet.")}
          />
        )}
      </div>
    </figure>
  );
}

/** Builds per hour or day from the deploy history: finished, and failed. */
function Builds({ project, apps, range, tables }: { project: string; apps: string[]; range: Range; tables: boolean }) {
  // The same deploy history (and query key) as the deploy markers and the status line.
  const res = useQueries({ queries: apps.map((a) => deploysQuery(project, a)) });
  const span = RANGE_MS[range];
  const step = range === "1h" ? 300_000 : range === "24h" ? 3_600_000 : 86_400_000;
  const [now] = useState(() => Date.now());
  // A few dozen buckets: cheap enough to count every render, so a build that
  // finishes or fails (same deploy id, new status) is never shown stale.
  const buckets = buildBuckets(res.flatMap((r) => r.data ?? []), now, span, step);
  const id = useId();
  if (apps.length === 0) return null;
  const total = buckets.reduce((s, b) => s + b.values.ok + b.values.failed, 0);
  const failed = buckets.reduce((s, b) => s + b.values.failed, 0);
  const keys = [
    { id: "ok", label: "Built", color: "var(--ok)" },
    { id: "failed", label: "Failed", color: "var(--danger)" },
  ];
  const label = ranges.find((x) => x.value === range)!.label;
  return (
    <figure className="min-w-0" aria-labelledby={id}>
      <div className="flex items-baseline justify-between gap-3 border-b border-rule pb-1.5">
        <div>
          <span id={id} className="block text-[0.875rem] text-ink">
            Builds
          </span>
          <span className="block text-[0.75rem] text-ink-3">per {step === 86_400_000 ? "day" : step === 3_600_000 ? "hour" : "5 minutes"}</span>
        </div>
        <span className="shrink-0 text-[0.9375rem] font-[500] text-ink tnum">
          {int(total)}
          {failed > 0 && <span className="ml-1.5 text-[0.75rem] font-[400] text-danger">{int(failed)} failed</span>}
        </span>
      </div>
      <div className="mt-2 min-h-[18px]">{total > 0 && <Legend items={keys} />}</div>
      <div className="mt-2">
        {total === 0 ? (
          <p className="grid h-[168px] place-items-center rounded-[8px] border border-dashed border-rule-2 text-[0.8125rem] text-ink-3">No builds in this time.</p>
        ) : tables ? (
          <BuildTable buckets={buckets} step={step} />
        ) : (
          <StackedBars buckets={buckets} keys={keys} step={step} label={`Builds, the last ${label}`} format={int} height={168} />
        )}
      </div>
    </figure>
  );
}

function BuildTable({ buckets, step }: { buckets: Bucket[]; step: number }) {
  const series: Series[] = [
    { id: "ok", label: "Built", points: buckets.map((b) => [b.t, b.values.ok]) },
    { id: "failed", label: "Failed", points: buckets.map((b) => [b.t, b.values.failed]) },
  ];
  return <SeriesTable series={series} step={step} format={int} caption="Builds" />;
}
