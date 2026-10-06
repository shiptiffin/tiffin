import { useQueries, useQuery, keepPreviousData } from "@tanstack/react-query";
import { ChevronDown, Table2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { mod3, type UsageHistory } from "@/api/modules";
import { Legend, StackedBars, type Bucket } from "@/components/charts/bars";
import type { XY } from "@/components/charts/core";
import { SeriesTable, TimeSeries, type Series } from "@/components/charts/time-series";
import { Skeleton } from "@/components/page";
import { Segmented } from "@/components/segmented";
import { Menu, MenuContent, MenuRadioGroup, MenuRadioItem, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { bytes, dec, int, NNBSP, num, pct } from "@/lib/format";
import { partName } from "@/lib/names";
import type { ProjectUsage } from "@/lib/usage";

/**
 * Usage over time: how busy the project (or one app) has been and how it is
 * faring, each measure against its limit where it has one. From the box's
 * metrics store, about 150 points per chart whatever the range.
 */

type Range = "1h" | "24h" | "7d" | "30d";
const ranges: Array<{ value: Range; label: string; short: string; ms: number }> = [
  { value: "1h", label: "1 hour", short: "1 h", ms: 3_600_000 },
  { value: "24h", label: "24 hours", short: "24 h", ms: 86_400_000 },
  { value: "7d", label: "7 days", short: "7 d", ms: 7 * 86_400_000 },
  { value: "30d", label: "30 days", short: "30 d", ms: 30 * 86_400_000 },
];

const cpuWords = (v: number) => `${dec(v, v < 10 ? 1 : 0)}%`;
const msWords = (v: number) => (v >= 1000 ? `${dec(v / 1000, 2)} s` : `${int(v)} ms`);

export function UsageCharts({ project, apps, usage }: { project: string; apps: string[]; usage?: ProjectUsage }) {
  const [range, setRange] = useState<Range>("24h");
  const [app, setApp] = useState<string | undefined>();
  const [tables, setTables] = useState(false);
  const h = useQuery({
    queryKey: ["usage-history", project, range, app],
    queryFn: () => mod3.usageHistory(project, range, app),
    refetchInterval: range === "1h" ? 30_000 : 120_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
  const d = h.data;
  const step = (d?.stepSeconds ?? 600) * 1000;
  const s = d?.series ?? {};
  const kv = partName("valkey");

  // A limit line from the series (its latest value), else from what the box reports now.
  const last = (k: string) => {
    const pts = s[k];
    return pts?.length ? (pts[pts.length - 1] as number[])[1] : undefined;
  };
  const memLimit = last("memoryLimit") ?? (!app && usage && usage.memory.limitSource !== "automatic" && usage.memory.limitBytes ? usage.memory.limitBytes : undefined);
  const cpuLimit = last("cpuLimit") ?? (!app && usage?.cpu.limitCpus ? usage.cpu.limitCpus * 100 : undefined);

  const charts: Array<ChartSpec | null> = [
    { ...byteChart("memory", "Memory", s.memory, memLimit), empty: "No readings yet." },
    { id: "cpu", title: "CPU", note: "100% is one full core", series: [line(s.cpu, "CPU")], format: cpuWords, limit: cpuLimit ? { value: cpuLimit, label: `Limit ${cpuWords(cpuLimit)}` } : undefined, empty: "No readings yet." },
    { id: "requests", title: "Requests", note: "per minute", series: [line(s.requests, "Requests per minute")], format: (v) => num(Math.round(v * 10) / 10), empty: "No requests in this time." },
    {
      id: "latency",
      title: "Response time",
      series: align([line(s.p95, "95% of requests within", "ink", false), line(s.p50, "Half of requests within", "soft", false)]),
      format: msWords,
      legend: [
        { label: "95% within", color: "var(--ink-2)", line: true },
        { label: "Half within", color: "var(--part-3)", line: true },
      ],
      empty: "No requests in this time.",
    },
    { id: "errors", title: "Errors", note: "share of requests that failed (5xx)", series: [line(s.errors, "Failed")], format: (v) => pct(v, v < 0.1 ? 1 : 0), minTop: 0.01, empty: "No requests in this time." },
    s.database ? byteChart("database", `${partName("postgres")} size`, s.database) : null,
    s.connections
      ? {
          id: "connections",
          title: `${partName("postgres")} connections`,
          series: [line(s.connections, "Open connections")],
          format: int,
          limit: usage?.database?.connectionLimit ? { value: usage.database.connectionLimit, label: `Limit ${usage.database.connectionLimit}` } : undefined,
        }
      : null,
    s.kv ? byteChart("kv", `${kv} memory`, s.kv, usage?.cache?.limitBytes || undefined) : null,
    s.files ? byteChart("files", `${partName("storage")} stored`, s.files) : null,
  ];

  return (
    <section className="mt-12" aria-labelledby="over-time">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-3">
        <h2 id="over-time" className="mr-auto text-[0.9375rem] font-[550] text-ink">
          Over time
        </h2>
        {apps.length > 1 && <AppMenu apps={apps} app={app} onChange={setApp} />}
        <Segmented label="Range" value={range} options={ranges} onChange={setRange} />
        <button
          type="button"
          aria-pressed={tables}
          onClick={() => setTables((x) => !x)}
          className={cn("inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-[0.8125rem] text-ink-3 hover:text-ink", tables && "text-ink")}
        >
          <Table2 aria-hidden className="size-4" />
          <span className="max-sm:sr-only">Tables</span>
        </button>
      </div>
      {h.isError ? (
        <p className="mt-4 max-w-[46rem] rounded-[10px] bg-paper-sunk px-4 py-3 text-[0.875rem] text-ink-2">
          Charts appear once the box has been measuring for a few minutes. {h.error instanceof Error && /not reachable/.test(h.error.message) ? "Its metrics store isn’t answering right now." : ""}
        </p>
      ) : !d ? (
        <div className="mt-5 grid gap-x-10 gap-y-8 md:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-44" />
          ))}
        </div>
      ) : (
        <div className={cn("mt-5 grid gap-x-10 gap-y-9 md:grid-cols-2 xl:grid-cols-3 transition-opacity", h.isPlaceholderData && "opacity-60")}>
          {charts.map((c) => c && <Chart key={c.id} c={c} step={step} tables={tables} range={range} />)}
          <Builds project={project} apps={app ? [app] : apps} range={range} tables={tables} />
        </div>
      )}
    </section>
  );
}

type ChartSpec = {
  id: string;
  title: string;
  note?: string;
  series: Series[];
  format: (v: number) => string;
  limit?: { value: number; label: string };
  legend?: Array<{ label: string; color: string; line?: boolean }>;
  minTop?: number;
  empty?: string;
};

const MB = 1048576;
const GB = 1073741824;

/** A chart of bytes, drawn in MB or GB so the axis reads in round numbers (0 / 200 / 400 MB). */
function byteChart(id: string, title: string, pts: UsageHistory["series"][string] | undefined, limit?: number): ChartSpec {
  const raw = line(pts, title);
  const most = Math.max(limit ?? 0, ...raw.points.map((p) => p[1]));
  const [unit, name, digits] = most >= GB ? [GB, "GB", 2] : [MB, "MB", 0];
  const format = (v: number) => `${dec(v, v && v < 10 ? Math.max(digits, 1) : digits)}${NNBSP}${name}`;
  return {
    id,
    title,
    series: [{ ...raw, points: raw.points.map((p) => [p[0], p[1] / unit] as XY) }],
    format,
    limit: limit ? { value: limit / unit, label: `Limit ${bytes(limit, 0)}` } : undefined,
  };
}

function line(pts: UsageHistory["series"][string] | undefined, label: string, tone: Series["tone"] = "ink", area = true): Series {
  return { id: label, label, tone, area, points: (pts ?? []).filter((p): p is number[] => !!p && p.length === 2).map((p) => [p[0] * 1000, p[1]] as XY) };
}

/** Puts every series on the first one's steps (missing steps read as no value). */
function align(series: Series[]): Series[] {
  const [first, ...rest] = series;
  if (!first.points.length) return series;
  return [first, ...rest.map((s) => {
    const by = new Map(s.points.map((p) => [p[0], p[1]]));
    return { ...s, points: first.points.map((p) => [p[0], by.get(p[0]) ?? NaN] as XY) };
  })];
}

function Chart({ c, step, tables, range }: { c: ChartSpec; step: number; tables: boolean; range: Range }) {
  const main = c.series[0];
  const now = main.points.length ? main.points[main.points.length - 1][1] : undefined;
  const span = ranges.find((r) => r.value === range)!.label;
  return (
    <figure className="min-w-0">
      <div className="flex items-baseline justify-between gap-3 border-b border-rule pb-1.5">
        <figcaption className="min-w-0">
          <span className="block text-[0.875rem] text-ink">{c.title}</span>
          {c.note && <span className="block truncate text-[0.75rem] text-ink-3">{c.note}</span>}
        </figcaption>
        {now !== undefined && Number.isFinite(now) && <span className="shrink-0 text-[0.9375rem] font-[500] text-ink tnum">{c.format(now)}</span>}
      </div>
      {c.legend && main.points.length > 0 && <Legend className="mt-2" items={c.legend} />}
      <div className="mt-3">
        {main.points.length === 0 ? (
          <p className="grid h-[150px] place-items-center rounded-[8px] bg-paper-sunk/60 text-[0.8125rem] text-ink-3">{c.empty ?? "No readings yet."}</p>
        ) : tables ? (
          <SeriesTable series={c.series} step={step} format={c.format} caption={`${c.title}, the last ${span}`} />
        ) : (
          <TimeSeries series={c.series} step={step} format={c.format} label={`${c.title}, the last ${span}`} height={150} limit={c.limit} minTop={c.minTop} />
        )}
      </div>
    </figure>
  );
}

/** Builds per hour or day from the deploy history: finished, and failed. */
function Builds({ project, apps, range, tables }: { project: string; apps: string[]; range: Range; tables: boolean }) {
  const res = useQueries({ queries: apps.map((a) => ({ queryKey: ["deploys", project, a], queryFn: () => mod3.deploys(project, a), staleTime: 60_000, retry: false })) });
  const all = res.flatMap((r) => r.data ?? []);
  const key = all.map((x) => x.id).join(",");
  const r = ranges.find((x) => x.value === range)!;
  const step = range === "1h" ? 300_000 : range === "24h" ? 3_600_000 : 86_400_000;
  const [now] = useState(() => Date.now());
  const buckets = useMemo<Bucket[]>(() => {
    const start = Math.floor((now - r.ms) / step) * step + step;
    const n = Math.round(r.ms / step);
    const out: Bucket[] = Array.from({ length: n }, (_, i) => ({ t: start + i * step, values: { ok: 0, failed: 0 } }));
    for (const dpl of all) {
      if (!dpl.buildSeconds && dpl.status !== "failed") continue;
      const t = Date.parse(dpl.createdAt);
      const i = Math.floor((t - start) / step);
      if (i < 0 || i >= n) continue;
      out[i].values[dpl.status === "failed" ? "failed" : "ok"]++;
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, range, now]);
  if (apps.length === 0) return null;
  const total = buckets.reduce((s, b) => s + b.values.ok + b.values.failed, 0);
  const failed = buckets.reduce((s, b) => s + b.values.failed, 0);
  const keys = [
    { id: "ok", label: "Built", color: "var(--part-2)" },
    { id: "failed", label: "Failed", color: "var(--danger)" },
  ];
  return (
    <figure className="min-w-0">
      <div className="flex items-baseline justify-between gap-3 border-b border-rule pb-1.5">
        <figcaption>
          <span className="block text-[0.875rem] text-ink">Builds</span>
          <span className="block text-[0.75rem] text-ink-3">per {step === 86_400_000 ? "day" : step === 3_600_000 ? "hour" : "5 minutes"}</span>
        </figcaption>
        <span className="shrink-0 text-[0.9375rem] font-[500] text-ink tnum">
          {int(total)}
          {failed > 0 && <span className="ml-1.5 text-[0.75rem] font-[400] text-danger">{int(failed)} failed</span>}
        </span>
      </div>
      {total > 0 && <Legend className="mt-2" items={keys} />}
      <div className="mt-3">
        {total === 0 ? (
          <p className="grid h-[150px] place-items-center rounded-[8px] bg-paper-sunk/60 text-[0.8125rem] text-ink-3">No builds in this time.</p>
        ) : tables ? (
          <BuildTable buckets={buckets} step={step} />
        ) : (
          <StackedBars buckets={buckets} keys={keys} step={step} label={`Builds, the last ${r.label}`} format={int} height={150} />
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

function AppMenu({ apps, app, onChange }: { apps: string[]; app?: string; onChange: (a: string | undefined) => void }): ReactNode {
  return (
    <Menu>
      <MenuTrigger asChild>
        <button type="button" className="inline-flex h-8 items-center gap-1.5 rounded-[8px] border border-rule-2 bg-paper-raised px-3 text-[0.8125rem] text-ink hover:border-rule-3">
          <span className={cn("font-[550]", app && "font-mono text-[0.78rem]")}>{app ?? "Whole project"}</span>
          <ChevronDown aria-hidden className="size-3.5 text-ink-3" />
        </button>
      </MenuTrigger>
      <MenuContent align="end">
        <MenuRadioGroup value={app ?? ""} onValueChange={(v) => onChange(v || undefined)}>
          <MenuRadioItem value="">Whole project</MenuRadioItem>
          {apps.map((a) => (
            <MenuRadioItem key={a} value={a} className="font-mono text-[0.8125rem]">
              {a}
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>
      </MenuContent>
    </Menu>
  );
}
