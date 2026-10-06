import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { CalendarDays, ChevronDown, Table2, X } from "lucide-react";
import { Popover } from "radix-ui";
import { useMemo, useState, type ReactNode } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod2, mod3, type AnalyticsCount, type AnalyticsEvent, type AnalyticsOverview, type AnalyticsQuery, type AnalyticsVitals } from "@/api/modules";
import { q as api } from "@/api/queries";
import { StackedBars, Legend } from "@/components/charts/bars";
import { BarList, type BarRow } from "@/components/charts/bar-list";
import { Sparkline } from "@/components/charts/sparkline";
import { SeriesTable, TimeSeries, type Series } from "@/components/charts/time-series";
import { WorldMap } from "@/components/charts/world-map";
import { CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { StateSentence } from "@/components/jobs-words";
import { Crumbs, Empty, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuRadioGroup, MenuRadioItem, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { analyticsSearch, FILTERS, type AnalyticsSearch, type FilterKey, type Metric } from "@/routes/analytics-search";
import { dec, int, MINUS, ms, NNBSP, num, pct } from "@/lib/format";

/**
 * Analytics: how busy a project's apps are and where the visits come from.
 * Everything below the period row follows the URL: the period (or days),
 * one app, the step, and filters. Choosing a row anywhere (a page, a
 * country, a source) filters the whole page; the chips undo it.
 */

const periods = [
  { v: "24h", label: "24 hours", short: "24 h", words: "in the last 24 hours", before: "the 24 hours before", ms: 86_400_000 },
  { v: "7d", label: "7 days", short: "7 d", words: "in the last 7 days", before: "the 7 days before", ms: 7 * 86_400_000 },
  { v: "30d", label: "30 days", short: "30 d", words: "in the last 30 days", before: "the 30 days before", ms: 30 * 86_400_000 },
  { v: "90d", label: "90 days", short: "90 d", words: "in the last 90 days", before: "the 90 days before", ms: 90 * 86_400_000 },
] as const;
type Per = { v: string; label: string; words: string; before: string; ms: number };

/** Below this many visitors in the period before, a percentage change is noise, not news. */
const MIN_BASELINE = 20;
const HOUR = 3_600_000;
const DAY = 86_400_000;

const regions = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames(["en-GB"], { type: "region" }) : null;
export const countryName = (code: string) => {
  try {
    return (code && regions?.of(code)) || code || "Unknown";
  } catch {
    return code;
  }
};
const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", timeZone: "UTC" });
const shortDay = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
const isoDay = (t: number) => new Date(t).toISOString().slice(0, 10);

/** How a filter's value reads: countries by name, devices capitalised. */
function shown(key: FilterKey, v: string) {
  if (key === "country") return countryName(v);
  if (key === "device") return v.charAt(0).toUpperCase() + v.slice(1);
  if (key === "os" && v === "Mac OS X") return "macOS";
  return v;
}

/** 61.8 → "1 min 2 s", 9 → "9 s", 754 → "13 min". */
function visitLength(s: number) {
  if (!Number.isFinite(s)) return "–";
  if (!s) return `0${NNBSP}s`;
  if (s < 60) return `${int(s)}${NNBSP}s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m >= 10 || rest === 0) return `${int(s / 60)}${NNBSP}min`;
  return `${m}${NNBSP}min ${rest}${NNBSP}s`;
}
const shortLength = (s: number) => (s < 60 ? `${int(s)}s` : `${dec(s / 60, 1)}m`);

/** A change against a real baseline, or nothing at all. */
function change(v: number, prev: number, base: number, points?: boolean): string | null {
  if (base < MIN_BASELINE || Number.isNaN(base)) return null;
  if (points) {
    const d = Math.round((v - prev) * 100);
    return d === 0 ? "same as before" : `${d > 0 ? "+" : MINUS}${Math.abs(d)} pts`;
  }
  if (!prev) return null;
  const r = (v - prev) / prev;
  if (Math.abs(r) < 0.005) return "same as before";
  if (r >= 2) return `${dec(v / prev, 1)}×`;
  return `${r > 0 ? "+" : MINUS}${pct(Math.abs(r))}`;
}

type Point = AnalyticsOverview["timeseries"]["points"] extends (infer P)[] | null ? P : never;
function valueOf(p: Point, m: Metric) {
  if (m === "visitors") return p.visitors;
  if (m === "pageviews") return p.pageviews;
  if (!p.sessions) return NaN;
  return m === "bounce" ? p.bounces / p.sessions : p.durationMs / p.sessions / 1000;
}
const metrics: Record<Metric, { label: string; per: string; format: (v: number) => string; axis: (v: number) => string; minTop?: number }> = {
  visitors: { label: "Visitors", per: "Visitors", format: int, axis: num },
  pageviews: { label: "Page views", per: "Page views", format: int, axis: num },
  bounce: { label: "Bounce rate", per: "Bounce rate", format: (v) => pct(v), axis: (v) => `${int(v * 100)}%`, minTop: 0.1 },
  duration: { label: "Visit length", per: "Visit length", format: visitLength, axis: shortLength },
};

export function AnalyticsPage({ project, search }: { project: string; search: AnalyticsSearch }) {
  useTitle(`${project} · Analytics`);
  const navigate = useNavigate();
  const custom = !!search.from;
  const preset = periods.find((x) => x.v === search.period) ?? periods[1];
  const per: Per = custom ? customPer(search.from!, search.to) : preset;
  const filters = Object.fromEntries(FILTERS.map((f) => [f.key, search[f.key]]).filter(([, v]) => v)) as Partial<Record<FilterKey, string>>;
  const daily = custom || preset.v !== "24h";
  const interval = !daily ? "hour" : (search.interval ?? (per.ms <= 2 * DAY ? "hour" : "day"));
  const query: AnalyticsQuery = { ...(custom ? { from: search.from, to: search.to } : { period: preset.v }), app: search.app, interval, ...(filters as Partial<AnalyticsQuery>) };
  const metric: Metric = search.metric ?? "visitors";

  const go = (patch: Partial<AnalyticsSearch>) =>
    void navigate({
      to: "/projects/$project/analytics",
      params: { project },
      search: (prev: AnalyticsSearch) => analyticsSearch({ ...prev, ...patch }),
      replace: !!patch.metric || patch.interval !== undefined,
      resetScroll: false,
    } as never);
  const setFilter = (key: FilterKey, v: string | undefined) => go({ [key]: v } as Partial<AnalyticsSearch>);

  const o = useQuery({ queryKey: ["analytics", project, query], queryFn: () => mod2.analyticsView(project, query, 50), refetchInterval: 60_000, placeholderData: keepPreviousData });
  const rt = useQuery({ queryKey: ["analytics-rt", project, search.app], queryFn: () => mod2.realtime(project, search.app), refetchInterval: 15_000 });
  const ev = useQuery({ queryKey: ["analytics-ev", project, query], queryFn: () => mod2.events(project, query), placeholderData: keepPreviousData });
  const setup = useQuery({ queryKey: ["analytics-setup", project], queryFn: () => mod2.analyticsSetup(project), staleTime: Infinity });
  const vitals = useQuery({ queryKey: ["analytics-vitals", project, query.period, query.from, query.to, query.app, query.page], queryFn: () => mod2.vitals(project, query), refetchInterval: 60_000 });
  const m = useQuery({ ...api.manifest(project), retry: false, staleTime: 60_000 });
  const apps = Object.keys(m.data?.manifest.apps ?? {}).sort();
  const markers = useDeployMarkers(project, apps, search.app);
  const since = useFirstVisit(project);
  // The period before only counts as a baseline if visits were being counted for all of it.
  const [opened] = useState(() => Date.now());
  const end = custom ? Date.parse(`${search.to ?? isoDay(opened)}T00:00:00Z`) + DAY : opened;
  const partial = since !== undefined && since > end - 2 * per.ms;

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Analytics" />;
  const off = o.error instanceof ApiError && o.error.status === 409 && /not enabled/.test(o.error.message);
  const d = o.data;
  const now = rt.data?.visitorsNow ?? 0;
  const anyFilter = Object.keys(filters).length > 0;

  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }]} />} title="Analytics" />

      {off ? (
        <Off project={project} />
      ) : (
        <>
          <div className="mt-3 min-h-8">{d ? <StateSentence>{sentenceFor(d, per, partial ? since : undefined, Object.values(d.filters ?? {}).some(Boolean))}</StateSentence> : <Skeleton className="h-8 w-[28rem] max-w-full" />}</div>

          <div className="mt-5 flex flex-wrap items-center gap-x-3 gap-y-3">
            {apps.length > 1 && <AppPicker apps={apps} app={search.app} onChange={(a) => go({ app: a })} />}
            <Segmented
              label="Period"
              value={custom ? undefined : preset.v}
              options={periods.map((x) => ({ value: x.v as string, label: x.label, short: x.short }))}
              onChange={(v) => go({ period: v === "7d" ? undefined : v, from: undefined, to: undefined, interval: undefined })}
            />
            <CustomRange from={search.from} to={search.to} onApply={(from, to) => go({ from, to, period: undefined, interval: undefined })} />
            <a href="#now" className="ml-auto inline-flex items-center gap-2 text-[0.84375rem] text-ink-2 hover:text-ink" aria-live="polite">
              <span aria-hidden className={cn("size-2 rounded-full", now ? "bg-ok" : "bg-ink-4")} />
              {now ? `${int(now)} ${now === 1 ? "person" : "people"} on the site now` : "No one on the site right now"}
            </a>
          </div>

          {anyFilter && (
            <div className="mt-4 flex flex-wrap items-center gap-2" aria-label="Filters">
              {FILTERS.filter((f) => filters[f.key]).map((f) => (
                <button
                  key={f.key}
                  type="button"
                  onClick={() => setFilter(f.key, undefined)}
                  aria-label={`Remove filter: ${f.name} is ${shown(f.key, filters[f.key]!)}`}
                  className="inline-flex h-7 max-w-full items-center gap-1.5 rounded-full border border-brass/60 bg-brass-wash pr-2 pl-3 text-[0.8125rem] text-ink hover:border-brass"
                >
                  <span className="text-ink-2">{f.name} is</span>
                  <span className={cn("truncate font-[550]", (f.key === "page" || f.key === "entry" || f.key === "exit") && "font-mono text-[0.75rem]")}>{shown(f.key, filters[f.key]!)}</span>
                  <X aria-hidden className="size-3.5 text-ink-3" />
                </button>
              ))}
              {Object.keys(filters).length > 1 && (
                <button type="button" onClick={() => go(Object.fromEntries(FILTERS.map((f) => [f.key, undefined])))} className="h-7 px-2 text-[0.8125rem] text-ink-3 hover:text-ink">
                  Clear all
                </button>
              )}
            </div>
          )}

          {o.isError && <ProblemNote className="mt-6" error={o.error} />}
          {o.isPending && <Skeleton className="mt-8 h-96" />}
          {d && (
            <div className={cn("transition-opacity duration-200", o.isPlaceholderData && "opacity-60")} aria-busy={o.isFetching}>
              <Headline d={d} metric={metric} onMetric={(x) => go({ metric: x === "visitors" ? undefined : x })} partial={partial} per={per} />

              <section className="mt-8" aria-labelledby="trend">
                <MainChart d={d} metric={metric} per={per} markers={markers} now={rt.data ? Date.parse(rt.data.at) : undefined} interval={daily ? interval : undefined} onInterval={(i) => go({ interval: i })} />
              </section>

              {d.totals.pageviews === 0 && !anyFilter ? (
                <NoVisits words={per.words} snippet={setup.data?.snippet} />
              ) : (
                <div className="mt-14 grid gap-x-12 gap-y-12 lg:grid-cols-2">
                  <Panel
                    title="Pages"
                    total={d.totals.visitors}
                    mono
                    filters={filters}
                    onFilter={setFilter}
                    tabs={[
                      { label: "Top pages", key: "page", rows: d.pages },
                      { label: "Entry pages", key: "entry", rows: d.entryPages },
                      { label: "Exit pages", key: "exit", rows: d.exitPages },
                    ]}
                  />
                  <Panel
                    title="Sources"
                    total={d.totals.visitors}
                    filters={filters}
                    onFilter={setFilter}
                    empty="Nothing yet: visits came directly, or from sites that don’t say where from."
                    tabs={[
                      { label: "Referrers", key: "source", rows: d.sources },
                      { label: "UTM sources", key: "utmSource", rows: d.utmSources },
                      { label: "Mediums", key: "utmMedium", rows: d.utmMediums },
                      { label: "Campaigns", key: "utmCampaign", rows: d.utmCampaigns },
                    ]}
                  />
                  <Countries d={d} selected={filters.country} onFilter={(c) => setFilter("country", c === filters.country ? undefined : c)} />
                  <Panel
                    title="Devices"
                    total={d.totals.visitors}
                    filters={filters}
                    onFilter={setFilter}
                    tabs={[
                      { label: "Devices", key: "device", rows: d.devices },
                      { label: "Browsers", key: "browser", rows: d.browsers },
                      { label: "Systems", key: "os", rows: d.os },
                    ]}
                  />
                  <Events data={ev.data?.events ?? []} visitors={d.totals.visitors} />
                  <Realtime project={project} app={search.app} />
                </div>
              )}

              {vitals.data && <Speed v={vitals.data} code={setup.data?.vitals} selected={filters.page} onPage={(p) => setFilter("page", p === filters.page ? undefined : p)} />}
            </div>
          )}

          {setup.data && <SetupNotes snippet={setup.data.snippet} browser={setup.data.browser} track={setup.data.track} privacy={setup.data.privacy} />}
        </>
      )}
    </Page>
  );
}

function customPer(from: string, to?: string): Per {
  const a = Date.parse(`${from}T00:00:00Z`);
  const b = Date.parse(`${to ?? isoDay(Date.now())}T00:00:00Z`) + DAY;
  const n = Math.max(1, Math.round((b - a) / DAY));
  return { v: "custom", label: customLabel(from, to), words: `from ${dayFmt.format(a)} to ${dayFmt.format(b - DAY)}`, before: `the ${n} days before`, ms: b - a };
}
function customLabel(from: string, to?: string) {
  const a = Date.parse(`${from}T00:00:00Z`);
  const b = to ? Date.parse(`${to}T00:00:00Z`) : Date.now();
  return a === b ? shortDay.format(a) : `${shortDay.format(a)} – ${shortDay.format(b)}`;
}

/** The period's state in one sentence, honest about the baseline. */
function sentenceFor(d: AnalyticsOverview, per: Per, partialSince: number | undefined, filtered: boolean): string {
  const v = d.totals.visitors;
  const who = filtered ? "matching visitor" : "visitor";
  if (!v) return filtered ? `No visits match ${per.words}.` : `No visits ${per.words}.`;
  let s = `${int(v)} ${v === 1 ? who : `${who}s`} ${per.words}`;
  if (partialSince !== undefined && d.previous.visitors > 0) return `${s}. Counting started ${dayFmt.format(new Date(partialSince))}, so there’s no full ${per.before.replace(/^the /, "")} to compare with yet.`;
  if (d.previous.visitors >= MIN_BASELINE) {
    const r = (v - d.previous.visitors) / d.previous.visitors;
    if (Math.abs(r) < 0.01) s += `, about the same as ${per.before}`;
    else if (r >= 2) s += `, ${dec(v / d.previous.visitors, 1)} times ${per.before}`;
    else s += `, ${pct(Math.abs(r))} ${r > 0 ? "more" : "fewer"} than ${per.before}`;
    return `${s}.`;
  }
  return d.previous.visitors === 0 ? `${s}. Nothing to compare with yet.` : `${s}. Only ${int(d.previous.visitors)} ${per.before}: too few to compare.`;
}

function AppPicker({ apps, app, onChange }: { apps: string[]; app?: string; onChange: (a: string | undefined) => void }) {
  return (
    <Menu>
      <MenuTrigger asChild>
        <button type="button" className="inline-flex h-8 items-center gap-1.5 rounded-[8px] border border-rule-2 bg-paper-raised px-3 text-[0.8125rem] text-ink hover:border-rule-3">
          <span className="text-ink-3">App</span>
          <span className={cn("font-[550]", app && "font-mono text-[0.78rem]")}>{app ?? "All apps"}</span>
          <ChevronDown aria-hidden className="size-3.5 text-ink-3" />
        </button>
      </MenuTrigger>
      <MenuContent align="start">
        <MenuRadioGroup value={app ?? ""} onValueChange={(v) => onChange(v || undefined)}>
          <MenuRadioItem value="">All apps</MenuRadioItem>
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

/** Two dates and Apply: any range of whole UTC days, up to today. */
function CustomRange({ from, to, onApply }: { from?: string; to?: string; onApply: (from: string, to?: string) => void }) {
  const [today] = useState(() => isoDay(Date.now()));
  const [open, setOpen] = useState(false);
  const [a, setA] = useState(() => from ?? isoDay(Date.now() - 13 * DAY));
  const [b, setB] = useState(to ?? today);
  const ok = !!a && !!b && a <= b && b <= today;
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={from ? `Days shown: ${customLabel(from, to)}. Choose other days` : "Choose days"}
          className={cn(
            "inline-flex h-8 items-center gap-1.5 rounded-[8px] border px-3 text-[0.8125rem] transition-colors",
            from ? "border-brass bg-brass-wash font-[550] text-ink" : "border-transparent text-ink-3 hover:text-ink",
          )}
        >
          <CalendarDays aria-hidden className="size-4" />
          {from ? customLabel(from, to) : "Custom"}
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content sideOffset={6} align="start" className="z-50 w-[17rem] rounded-[10px] border border-rule-2 bg-paper-raised p-3 shadow-raised data-[state=open]:animate-pop">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (!ok) return;
              onApply(a, b === today ? undefined : b);
              setOpen(false);
            }}
          >
            <p className="text-[0.8125rem] font-[550] text-ink">Days to show</p>
            <div className="mt-2 grid grid-cols-2 gap-2">
              <label className="text-[0.75rem] text-ink-3">
                From
                <input type="date" value={a} max={today} onChange={(e) => setA(e.target.value)} className="mt-1 block h-8 w-full rounded-[7px] border border-rule-2 bg-paper px-1.5 text-[0.8125rem] text-ink tnum" />
              </label>
              <label className="text-[0.75rem] text-ink-3">
                To
                <input type="date" value={b} max={today} onChange={(e) => setB(e.target.value)} className="mt-1 block h-8 w-full rounded-[7px] border border-rule-2 bg-paper px-1.5 text-[0.8125rem] text-ink tnum" />
              </label>
            </div>
            <p className="mt-2 text-[0.75rem] text-ink-3">{ok ? "Whole days, in UTC." : "The first day must come before the last, and neither after today."}</p>
            <Button type="submit" size="md" variant="primary" className="mt-3 w-full" disabled={!ok}>
              Show these days
            </Button>
          </form>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** At most `max` points for a sparkline: neighbouring steps added up (rates follow from the sums). */
function coarse(pts: Point[], max = 32): Point[] {
  if (pts.length <= max) return pts;
  const k = Math.ceil(pts.length / max);
  const out: Point[] = [];
  for (let i = 0; i < pts.length; i += k) {
    const g = pts.slice(i, i + k);
    out.push({
      t: g[0].t,
      visitors: g.reduce((s, p) => s + p.visitors, 0),
      pageviews: g.reduce((s, p) => s + p.pageviews, 0),
      sessions: g.reduce((s, p) => s + p.sessions, 0),
      bounces: g.reduce((s, p) => s + p.bounces, 0),
      durationMs: g.reduce((s, p) => s + p.durationMs, 0),
    });
  }
  return out;
}

/** The four headline numbers, each with its change and a small trend; choosing one draws it below. */
function Headline({ d, metric, onMetric, partial, per }: { d: AnalyticsOverview; metric: Metric; onMetric: (m: Metric) => void; partial: boolean; per: Per }) {
  const t = d.totals;
  const p = d.previous;
  const base = partial ? NaN : p.visitors;
  const pts = d.timeseries.points ?? [];
  const prev = d.previousTimeseries.points ?? [];
  const spark = (m: Metric) => ({ values: coarse(pts).map((x) => valueOf(x, m)), before: coarse(prev).map((x) => valueOf(x, m)) });
  const tiles: Array<{ m: Metric; value: string; delta: string | null }> = [
    { m: "visitors", value: int(t.visitors), delta: change(t.visitors, p.visitors, base) },
    { m: "pageviews", value: int(t.pageviews), delta: change(t.pageviews, p.pageviews, base) },
    { m: "bounce", value: t.sessions ? pct(t.bounceRate) : "–", delta: change(t.bounceRate, p.bounceRate, base, true) },
    { m: "duration", value: t.sessions ? visitLength(t.avgSessionSeconds) : "–", delta: change(t.avgSessionSeconds, p.avgSessionSeconds, base) },
  ];
  return (
    <div
      role="radiogroup"
      aria-label="Chart"
      className="mt-6 grid grid-cols-2 border-y border-rule-2 lg:grid-cols-4"
      onKeyDown={(e) => {
        const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
        if (!d) return;
        e.preventDefault();
        const k = (tiles.findIndex((t) => t.m === metric) + d + tiles.length) % tiles.length;
        onMetric(tiles[k].m);
        e.currentTarget.querySelectorAll<HTMLElement>("[role=radio]")[k]?.focus();
      }}
    >
      {tiles.map((x, i) => {
        const on = metric === x.m;
        return (
          <button
            key={x.m}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            onClick={() => onMetric(x.m)}
            className={cn(
              "relative min-w-0 py-4 text-left transition-colors duration-[var(--dur-state)] max-lg:even:pl-4 lg:px-5 lg:first:pl-0",
              i > 0 && "lg:border-l lg:border-rule",
              i % 2 === 1 && "max-lg:border-l max-lg:border-rule",
              i > 1 && "max-lg:border-t max-lg:border-rule",
              !on && "hover:[&_.v]:text-ink",
            )}
          >
            {on && <span aria-hidden className={cn("absolute inset-x-0 -top-px h-[2px] bg-ink", i === 0 ? "lg:right-5" : "lg:inset-x-5", i % 2 === 1 && "max-lg:left-4")} />}
            <span className={cn("label block", on && "text-ink!")}>{metrics[x.m].label}</span>
            <span className={cn("v mt-1.5 block text-[1.625rem] leading-8 font-[500] tracking-[-0.02em]", on ? "text-ink" : "text-ink-2")}>{x.value}</span>
            <span className="mt-0.5 block h-4 text-[0.75rem] text-ink-3 tnum">{x.delta ? `${x.delta}${/before/.test(x.delta) ? "" : ` vs ${per.before.replace(/^the /, "")}`}` : ""}</span>
            <Sparkline {...spark(x.m)} fill={x.m === "visitors" || x.m === "pageviews"} className="mt-2.5 pr-2" />
          </button>
        );
      })}
    </div>
  );
}

function MainChart({
  d,
  metric,
  per,
  markers,
  now,
  interval,
  onInterval,
}: {
  d: AnalyticsOverview;
  metric: Metric;
  per: Per;
  markers: Array<{ t: number; label: string }>;
  now?: number;
  interval?: "hour" | "day";
  onInterval: (i: "hour" | "day") => void;
}) {
  const [table, setTable] = useState(false);
  const M = metrics[metric];
  const hourly = d.timeseries.granularity === "hour";
  const step = hourly ? HOUR : DAY;
  const series = useMemo<Series[]>(() => {
    const pts = d.timeseries.points ?? [];
    const prev = d.previousTimeseries.points ?? [];
    return [
      { id: "now", label: M.label, points: pts.map((p) => [Date.parse(p.t), valueOf(p, metric)]), area: true, tone: "ink" },
      { id: "before", label: "Before", points: prev.map((p) => [Date.parse(p.t), valueOf(p, metric)]), ghost: true },
    ];
  }, [d, metric, M.label]);
  const title = `${M.per} per ${hourly ? "hour" : "day"}`;
  return (
    <>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <h2 id="trend" className="label">
            {title}
          </h2>
          <Legend
            items={[
              { label: per.v === "custom" ? "These days" : `Last ${per.label}`, color: "var(--ink-2)", line: true },
              { label: per.before.replace(/^the /, "").replace(/^./, (c) => c.toUpperCase()), color: "var(--ink-4)", line: true, dashed: true },
              ...(markers.length ? [{ label: "A deploy", color: "var(--ink-4)", line: true }] : []),
            ]}
          />
        </div>
        <div className="flex items-center gap-2">
          {interval && (
            <Segmented
              label="Points"
              value={interval}
              options={[
                { value: "hour", label: "By hour", short: "Hours" },
                { value: "day", label: "By day", short: "Days" },
              ]}
              onChange={onInterval}
            />
          )}
          <button
            type="button"
            aria-pressed={table}
            onClick={() => setTable((x) => !x)}
            className={cn("inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-[0.8125rem] text-ink-3 hover:text-ink", table && "text-ink")}
          >
            <Table2 aria-hidden className="size-4" />
            <span className="max-sm:sr-only">Table</span>
          </button>
        </div>
      </div>
      {table ? (
        <SeriesTable series={series} step={step} utc format={M.format} caption={title} />
      ) : (
        <TimeSeries series={series} step={step} utc format={M.format} axisFormat={M.axis} label={title} height={260} markers={markers} now={now} minTop={M.minTop} />
      )}
    </>
  );
}

type Tab = { label: string; key: FilterKey; rows: AnalyticsCount[] | null };

/** A breakdown: tabs for its views, each a ranked list whose rows filter the page. */
function Panel({
  title,
  tabs,
  total,
  mono,
  filters,
  onFilter,
  empty = "Nothing yet.",
}: {
  title: string;
  tabs: Tab[];
  total: number;
  mono?: boolean;
  filters: Partial<Record<FilterKey, string>>;
  onFilter: (key: FilterKey, v: string | undefined) => void;
  empty?: string;
}) {
  const [i, setI] = useState(() => Math.max(0, tabs.findIndex((t) => filters[t.key])));
  const tab = tabs[i];
  const id = `panel-${title.toLowerCase()}`;
  const rows: BarRow[] = (tab.rows ?? []).map((c) => ({ key: c.value, label: shown(tab.key, c.value), value: c.visitors, title: c.value }));
  return (
    <section aria-labelledby={id} className="min-w-0">
      <PanelHead id={id} title={title} tabs={tabs.map((t) => t.label)} i={i} onTab={setI} />
      <BarList
        rows={rows}
        total={total}
        mono={mono}
        empty={empty}
        selected={filters[tab.key]}
        onSelect={(v) => onFilter(tab.key, filters[tab.key] === v ? undefined : v)}
        selectLabel={(r) => `Filter by ${tab.label.toLowerCase().replace(/s$/, "")} ${r.title}`}
      />
    </section>
  );
}

function PanelHead({ id, title, tabs, i, onTab, right = "Visitors" }: { id: string; title: string; tabs: string[]; i: number; onTab: (i: number) => void; right?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-1 border-b border-rule-2 pb-2">
      {tabs.length <= 1 ? (
        <h2 id={id} className="label">
          {title}
        </h2>
      ) : (
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-3">
          <h2 id={id} className="sr-only">
            {title}
          </h2>
          <div role="tablist" aria-label={title} className="flex flex-wrap gap-x-3">
            {tabs.map((label, k) => (
              <button key={label} type="button" role="tab" aria-selected={i === k} onClick={() => onTab(k)} className={cn("label transition-colors hover:text-ink!", i === k && "text-ink!")}>
                {label}
              </button>
            ))}
          </div>
        </div>
      )}
      <span className="text-[0.71875rem] text-ink-3">{right}</span>
    </div>
  );
}

/** Countries: the map and the ranked list beside it (the list is the way in by keyboard). */
function Countries({ d, selected, onFilter }: { d: AnalyticsOverview; selected?: string; onFilter: (c: string) => void }) {
  const list = d.countries ?? [];
  const values = Object.fromEntries(list.map((c) => [c.value, c.visitors]));
  const rows: BarRow[] = list.map((c) => ({
    key: c.value,
    label: countryName(c.value),
    title: countryName(c.value),
    value: c.visitors,
    icon: <span className="w-5 shrink-0 font-mono text-[0.6875rem] text-ink-3">{c.value}</span>,
  }));
  return (
    <section aria-labelledby="countries" className="min-w-0 lg:col-span-2">
      <PanelHead id="countries" title="Countries" tabs={[]} i={0} onTab={() => {}} />
      <div className="grid gap-x-12 gap-y-4 pt-3 lg:grid-cols-[minmax(0,1.45fr)_minmax(0,1fr)]">
        <WorldMap values={values} name={countryName} selected={selected} onSelect={onFilter} className="max-lg:hidden" />
        <div className="min-w-0">
          <BarList rows={rows} total={d.totals.visitors} selected={selected} onSelect={onFilter} selectLabel={(r) => `Filter by country ${r.title}`} empty="No countries yet. Countries need the box’s country database." />
        </div>
      </div>
    </section>
  );
}

function Events({ data, visitors }: { data: AnalyticsEvent[]; visitors: number }) {
  const most = Math.max(1, ...data.map((e) => e.count));
  return (
    <section aria-labelledby="events" className="min-w-0">
      <PanelHead id="events" title="Custom events" tabs={[]} i={0} onTab={() => {}} right={data.length > 0 ? <span className="grid grid-cols-[3.5rem_4.5rem] gap-x-3 text-right">
            <span>Count</span>
            <span>Of visitors</span>
          </span> : ""} />
      {data.length === 0 ? (
        <p className="py-6 text-[0.84375rem] text-ink-3">
          None yet. Call <code className="ident text-ink-2">tiffin.track("Signup")</code> in the browser or <code className="ident text-ink-2">track()</code> on the server and they show up here.
        </p>
      ) : (
        <ul className="flex flex-col gap-0.5 py-1.5">
          {data.map((e) => (
            <li key={e.name} className="flex items-start gap-3 pr-1">
              <span className="relative min-w-0 flex-1 py-1.5 pl-2.5">
                <span aria-hidden className="absolute inset-y-0 left-0 rounded-[5px] bg-ink/[0.07]" style={{ width: `${Math.max(1.5, (e.count / most) * 100)}%` }} />
                <span className="relative block truncate text-[0.84375rem] text-ink">{e.name}</span>
                {Object.entries(e.props ?? {})
                  .slice(0, 2)
                  .map(([k, vs]) => (
                    <span key={k} className="relative mt-0.5 block truncate text-[0.75rem] text-ink-3">
                      <span className="font-mono">{k}</span>:{" "}
                      {(vs ?? [])
                        .slice(0, 4)
                        .map((v) => `${v.value} ${int(v.count)}`)
                        .join(" · ")}
                    </span>
                  ))}
              </span>
              <span className="w-14 shrink-0 py-1.5 text-right text-[0.84375rem] text-ink tnum">{int(e.count)}</span>
              <span className="w-[4.5rem] shrink-0 py-1.5 text-right text-[0.84375rem] text-ink-2 tnum">{visitors ? pct(e.visitors / visitors, 1) : "–"}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Realtime({ project, app }: { project: string; app?: string }) {
  const rt = useQuery({ queryKey: ["analytics-rt", project, app], queryFn: () => mod2.realtime(project, app), refetchInterval: 15_000 });
  const d = rt.data;
  const per = d?.perMinute ?? [];
  const any = per.some((x) => x.pageviews > 0);
  return (
    <section id="now" aria-labelledby="rt" className="min-w-0 scroll-mt-8">
      <PanelHead
        id="rt"
        title="Right now"
        tabs={[]}
        i={0}
        onTab={() => {}}
        right={d && d.pageviews30m > 0 ? `${int(d.visitors30m)} ${d.visitors30m === 1 ? "visitor" : "visitors"} · ${int(d.pageviews30m)} ${d.pageviews30m === 1 ? "view" : "views"} in 30 min` : ""}
      />
      <div className="pt-4">
        {!d ? (
          <Skeleton className="h-28" />
        ) : !any ? (
          <p className="text-[0.84375rem] text-ink-3">Quiet for the last half hour. This refreshes every 15 seconds.</p>
        ) : (
          <>
            <StackedBars
              buckets={per.map((x) => ({ t: Date.parse(x.t), values: { views: x.pageviews } }))}
              keys={[{ id: "views", label: "Page views", color: "var(--part-2)" }]}
              step={60_000}
              label="Page views per minute, the last 30 minutes"
              format={int}
              height={120}
            />
            {(d.topPages ?? []).length > 0 && (
              <ul className="mt-3 divide-y divide-rule border-t border-rule">
                {(d.topPages ?? []).slice(0, 4).map((x) => (
                  <li key={x.value} className="flex justify-between gap-3 py-1.5">
                    <span className="truncate font-mono text-[0.75rem] text-ink-2">{x.value}</span>
                    <span className="text-[0.8125rem] text-ink-3 tnum">{int(x.pageviews)}</span>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </div>
    </section>
  );
}

const speedNames: Record<string, [string, string]> = {
  LCP: ["Main content shows", "Largest Contentful Paint"],
  INP: ["Responds to taps", "Interaction to Next Paint"],
  CLS: ["Layout stays put", "Cumulative Layout Shift"],
  FCP: ["First paint", "First Contentful Paint"],
  TTFB: ["Server answers", "Time to First Byte"],
};
const speedValue = (name: string, v: number) => (name === "CLS" ? dec(v, 2) : ms(v));
const ratings: Record<string, { word: string; color: string; shape: ReactNode }> = {
  good: { word: "good", color: "text-ok", shape: <circle cx="5" cy="5" r="4" /> },
  "needs-improvement": { word: "could be faster", color: "text-warn-ink", shape: <path d="M5 1 9 9H1Z" /> },
  poor: { word: "slow", color: "text-danger", shape: <rect x="1.5" y="1.5" width="7" height="7" rx="1" /> },
};
/** A rating as a shape and a colour: circle good, triangle could be faster, square slow. */
function Mark({ rating }: { rating?: string }) {
  const r = rating ? ratings[rating] : undefined;
  if (!r) return null;
  return (
    <svg viewBox="0 0 10 10" className={cn("inline-block size-2.5 shrink-0 fill-current", r.color)} role="img" aria-label={r.word}>
      {r.shape}
    </svg>
  );
}

/** Web Vitals: how fast the pages felt to three in four visits, overall and per page. */
function Speed({ v, code, selected, onPage }: { v: AnalyticsVitals; code?: string; selected?: string; onPage: (p: string) => void }) {
  const list = v.metrics ?? [];
  const pages = v.pages ?? [];
  const cols = ["LCP", "INP", "CLS"];
  return (
    <section className="mt-14" aria-labelledby="speed">
      <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-1 border-b border-rule-2 pb-2">
        <h2 id="speed" className="label">
          Page speed
        </h2>
        {list.length > 0 && (
          <span className="flex flex-wrap items-center gap-x-3 text-[0.75rem] text-ink-3">
            <span>What three in four visits were at least as fast as</span>
            {Object.entries(ratings).map(([k, r]) => (
              <span key={k} className="inline-flex items-center gap-1">
                <Mark rating={k} />
                {r.word}
              </span>
            ))}
          </span>
        )}
      </div>
      {list.length === 0 ? (
        <div className="py-6 text-[0.84375rem] text-ink-3">
          <p>No speed readings yet. Pages send them from visitors’ browsers, with one line in a Next.js root layout:</p>
          {code && <CodeBox className="mt-3 max-w-[40rem]" code={code.replace("; ", ";\n\n// in <body>\n")} name="app/layout.tsx" />}
        </div>
      ) : (
        <>
          <dl className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5">
            {list.map((m) => (
              <div key={m.name} className="py-4 sm:pr-6">
                <dt className="label" title={speedNames[m.name]?.[1]}>
                  {speedNames[m.name]?.[0] ?? m.name}
                </dt>
                <dd className="mt-1.5 text-[1.375rem] leading-7 font-[500] tracking-[-0.015em] text-ink">{speedValue(m.name, m.p75)}</dd>
                <dd className="mt-0.5 flex items-center gap-1.5 text-[0.75rem] text-ink-3">
                  <Mark rating={m.rating} />
                  <span className={cn(m.rating !== "good" && ratings[m.rating]?.color)}>{ratings[m.rating]?.word}</span>
                  <span>· {m.name}</span>
                </dd>
              </div>
            ))}
          </dl>
          {pages.length > 0 && (
            <div className="overflow-x-auto">
              <table className="w-full min-w-[30rem] border-y border-rule-2 text-[0.84375rem]">
                <caption className="sr-only">Page speed per page: the 75th percentile of each measure</caption>
                <thead>
                  <tr className="text-[0.71875rem] text-ink-3">
                    <th scope="col" className="py-1.5 text-left font-[450]">
                      Page
                    </th>
                    {cols.map((c) => (
                      <th key={c} scope="col" className="w-28 py-1.5 text-right font-[450]" title={speedNames[c][1]}>
                        {speedNames[c][0]}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-rule border-t border-rule">
                  {pages.map((pg) => (
                    <tr key={pg.path}>
                      <th scope="row" className="py-1.5 pr-3 text-left font-[400]">
                        <button
                          type="button"
                          onClick={() => onPage(pg.path)}
                          aria-pressed={selected === pg.path}
                          className={cn("max-w-full truncate rounded-[4px] font-mono text-[0.78rem] text-ink hover:underline hover:underline-offset-4", selected === pg.path && "bg-brass-wash px-1")}
                          title={`Filter by page ${pg.path}`}
                        >
                          {pg.path}
                        </button>
                      </th>
                      {cols.map((c) => (
                        <td key={c} className="py-1.5 text-right text-ink-2 tnum">
                          {pg.p75[c] === undefined ? (
                            "–"
                          ) : (
                            <span className="inline-flex items-center gap-1.5">
                              {speedValue(c, pg.p75[c])}
                              <Mark rating={pg.ratings?.[c]} />
                            </span>
                          )}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  );
}

/** Analytics is a part a project adds; until then, say how. */
function Off({ project }: { project: string }) {
  return (
    <Empty className="mt-10" title={`Analytics is off for ${project}`}>
      <p>Add it in the project’s config and every app’s page views are counted at the box’s edge, without cookies and without a script.</p>
      <pre tabIndex={0} className="mt-4 overflow-x-auto rounded-[8px] bg-paper-sunk px-4 py-3 text-left font-mono text-[0.78rem] text-ink-2">{`// tiffin.config.ts\nservices: { analytics: {} }`}</pre>
    </Empty>
  );
}

/** No page views at all: how they arrive, and the optional script. */
function NoVisits({ words, snippet }: { words: string; snippet?: string }) {
  return (
    <div className="mt-12 grid gap-x-12 gap-y-6 border-t border-rule pt-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <div>
        <h2 className="text-[0.9375rem] font-[550] text-ink">No visits {words}</h2>
        <p className="mt-2 text-[0.875rem] text-ink-2">
          Page views need nothing in your app: the box counts every page people open on your apps’ addresses, at its own edge, so ad blockers can’t hide them. They show up here within a few seconds of the first visit.
        </p>
      </div>
      {snippet && (
        <div>
          <p className="text-[0.875rem] text-ink-2">For single-page apps (navigations in the browser) and custom events, add the optional 1.3 KB script:</p>
          <CodeBox className="mt-3" code={snippet} name="index.html" />
        </div>
      )}
    </div>
  );
}

function SetupNotes({ snippet, browser, track, privacy }: { snippet: string; browser: string; track: string; privacy: string }) {
  return (
    <section className="mt-16 grid gap-x-12 gap-y-8 border-t border-rule pt-10 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]" aria-labelledby="setup">
      <div className="min-w-0">
        <h2 id="setup" className="label">
          Single-page apps and custom events
        </h2>
        <p className="mt-2 mb-4 text-[0.875rem] text-ink-2">Page views need nothing. Add the 1.3 KB script for navigations in the browser, outbound links and events.</p>
        <CodeBox code={snippet} name="index.html" />
        <CodeBox className="mt-3" code={`// in the browser\n${browser}\n\n// on the server\n${track.replace("; ", ";\n")}`} name="track.ts" />
      </div>
      <div>
        <h3 className="label">What’s stored, and what isn’t</h3>
        <p className="mt-2 text-[0.875rem] text-ink-2">{privacy}</p>
        <p className="mt-4 text-[0.8125rem] text-ink-3">
          Countries come from{" "}
          <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="text-brass-ink underline decoration-brass-ink/40 underline-offset-4 hover:decoration-brass-ink">
            IP Geolocation by DB-IP
          </a>{" "}
          (CC BY 4.0). Days are UTC.
        </p>
      </div>
    </section>
  );
}

function CodeBox({ code, name, className }: { code: string; name: string; className?: string }): ReactNode {
  return (
    <div className={cn("overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk", className)}>
      <div className="flex items-center justify-between border-b border-rule px-4 py-1">
        <span className="font-mono text-[0.75rem] text-ink-3">{name}</span>
        <CopyButton value={code} label={`Copy ${name}`} />
      </div>
      <pre tabIndex={0} aria-label={name} className="overflow-x-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">
        <code>{code}</code>
      </pre>
    </div>
  );
}

/** When each app (or the one in view) went live, for the chart's markers. */
function useDeployMarkers(project: string, apps: string[], only?: string): Array<{ t: number; label: string }> {
  const list = only ? apps.filter((a) => a === only) : apps;
  const res = useQueries({ queries: list.map((a) => ({ queryKey: ["deploys", project, a], queryFn: () => mod3.deploys(project, a), staleTime: 60_000, retry: false })) });
  const all = res.flatMap((r) => r.data ?? []);
  const key = all.map((x) => x.id).join(",");
  return useMemo(
    () =>
      all
        .filter((x) => !x.preview && (x.liveAt || (x.status === "live" && x.finishedAt)))
        .map((x) => ({ t: Date.parse(x.liveAt ?? x.finishedAt!), label: `${x.app} deployed` })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );
}

/** When this project's first visit was counted, from the last 90 days (one cheap, cached read). */
function useFirstVisit(project: string): number | undefined {
  const q = useQuery({ queryKey: ["analytics", project, "90d"], queryFn: () => mod2.analytics(project, "90d"), staleTime: 600_000, retry: false });
  const first = (q.data?.timeseries.points ?? []).find((x) => x.pageviews > 0);
  return first ? Date.parse(first.t) : undefined;
}

