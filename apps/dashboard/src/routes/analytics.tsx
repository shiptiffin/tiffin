import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Accordion } from "radix-ui";
import { ArrowDown, ArrowUp, ChevronRight, Monitor, Smartphone, Tablet } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod2, type AnalyticsCount, type AnalyticsEvent, type AnalyticsOverview, type AnalyticsQuery } from "@/api/modules";
import { q as api } from "@/api/queries";
import { AllRows, change, countryName, isPath, Panel, Rows, RowsHead, shown, visitLength, type Delta, type Row } from "@/components/analytics-kit";
import { SetupDialog, SetupGuide } from "@/components/analytics-setup";
import { AppPicker, customPer, DAY, FilterChips, FilterMenu, HOUR, isoDay, LiveNow, periods, RangePicker, type Per } from "@/components/analytics-toolbar";
import { WebVitals } from "@/components/analytics-vitals";
import { Legend } from "@/components/charts/bars";
import { SeriesTable, TimeSeries, type Series } from "@/components/charts/time-series";
import { WorldMap } from "@/components/charts/world-map";
import { useTitle } from "@/components/favicon";
import { InfoTip } from "@/components/info-tip";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { ViewToggle } from "@/components/usage-charts";
import { cn } from "@/lib/cn";
import { deploysQuery } from "@/lib/pulse";
import { dec, int, num, pct } from "@/lib/format";
import { analyticsSearch, FILTERS, type AnalyticsSearch, type FilterKey, type Metric, type Vital } from "@/routes/analytics-search";

/**
 * Analytics: one page, read top to bottom. The toolbar picks the app, the
 * days and filters; four numbers (each with its change against the days
 * before) choose what the big chart draws; then the breakdowns, where any
 * row filters the whole page; then Web Vitals. Everything lives in the URL,
 * so a filtered view can be shared or bookmarked.
 */

const titles: Record<FilterKey, string> = {
  page: "Pages",
  entry: "Entry pages",
  exit: "Exit pages",
  source: "Referrers",
  utmSource: "UTM sources",
  utmMedium: "UTM mediums",
  utmCampaign: "UTM campaigns",
  country: "Countries",
  device: "Devices",
  browser: "Browsers",
  os: "Systems",
};

const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", timeZone: "UTC" });

type Point = AnalyticsOverview["timeseries"]["points"] extends (infer P)[] | null ? P : never;
function valueOf(p: Point, m: Metric) {
  if (m === "visitors") return p.visitors;
  if (m === "pageviews") return p.pageviews;
  if (!p.sessions) return NaN;
  return m === "bounce" ? p.bounces / p.sessions : p.durationMs / p.sessions / 1000;
}
const shortLength = (s: number) => (s < 60 ? `${int(s)}s` : `${dec(s / 60, 1)}m`);
const metrics: Record<Metric, { label: string; format: (v: number) => string; axis: (v: number) => string; minTop?: number; lowerIsBetter?: boolean }> = {
  visitors: { label: "Visitors", format: int, axis: num },
  pageviews: { label: "Page views", format: int, axis: num },
  bounce: { label: "Bounce rate", format: (v) => pct(v), axis: (v) => `${int(v * 100)}%`, minTop: 0.1, lowerIsBetter: true },
  duration: { label: "Visit duration", format: visitLength, axis: shortLength },
};

export function AnalyticsPage({ project, search }: { project: string; search: AnalyticsSearch }) {
  useTitle(`${project} · Analytics`);
  const navigate = useNavigate();
  const custom = !!search.from;
  const preset = periods.find((x) => x.v === (search.period ?? "7d")) ?? periods[3];
  const per: Per = custom ? customPer(search.from!, search.to) : preset;
  const filters = Object.fromEntries(FILTERS.map((f) => [f.key, search[f.key]]).filter(([, v]) => v)) as Partial<Record<FilterKey, string>>;
  const anyFilter = Object.keys(filters).length > 0;
  // Hours up to 92 days (the API's limit); days from two days up.
  const canChoose = per.ms > 2 * DAY && per.ms <= 92 * DAY;
  const interval: "hour" | "day" = per.ms <= 2 * DAY ? "hour" : !canChoose ? "day" : (search.interval ?? "day");
  const query: AnalyticsQuery = { ...(custom ? { from: search.from, to: search.to } : { period: preset.v as AnalyticsQuery["period"] }), app: search.app, interval, ...(filters as Partial<AnalyticsQuery>) };
  const metric: Metric = search.metric ?? "visitors";
  const compare = search.compare !== "off";

  const go = (patch: Partial<AnalyticsSearch>, replace = false) =>
    void navigate({
      to: "/projects/$project/analytics",
      params: { project },
      search: (prev: AnalyticsSearch) => analyticsSearch({ ...prev, ...patch }),
      replace,
      resetScroll: false,
    } as never);
  const setFilter = (key: FilterKey, v: string | undefined) => go({ [key]: v } as Partial<AnalyticsSearch>);
  const toggle = (key: FilterKey) => (v: string) => setFilter(key, filters[key] === v ? undefined : v);
  const clearFilters = () => go(Object.fromEntries(FILTERS.map((f) => [f.key, undefined])));

  const o = useQuery({ queryKey: ["analytics", project, query], queryFn: () => mod2.analyticsView(project, query, 50), refetchInterval: 60_000, placeholderData: keepPreviousData, retry: 1 });
  const rt = useQuery({ queryKey: ["analytics-rt", project, search.app], queryFn: () => mod2.realtime(project, search.app), refetchInterval: 15_000, retry: false });
  const ev = useQuery({ queryKey: ["analytics-ev", project, query], queryFn: () => mod2.events(project, query), placeholderData: keepPreviousData, retry: false });
  const setup = useQuery({ queryKey: ["analytics-setup", project], queryFn: () => mod2.analyticsSetup(project), staleTime: Infinity, retry: false });
  const vitals = useQuery({
    queryKey: ["analytics-vitals", project, query.period, query.from, query.to, query.app, query.page],
    queryFn: () => mod2.vitals(project, query),
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
  const m = useQuery({ ...api.manifest(project), retry: false, staleTime: 60_000 });
  const apps = Object.keys(m.data?.manifest.apps ?? {}).sort();
  const markers = useDeployMarkers(project, apps, search.app);
  const first = useFirstVisit(project);
  // The days before only count as a baseline if visits were being counted for all of them.
  const [opened] = useState(() => Date.now());
  const end = custom ? Date.parse(`${search.to ?? isoDay(opened)}T00:00:00Z`) + DAY : opened;
  const since = first.data !== undefined && first.data !== null && first.data > end - 2 * per.ms ? first.data : undefined;

  const [all, setAll] = useState<FilterKey | null>(null);
  const [guide, setGuide] = useState(false);

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Analytics" />;
  const d = o.data;
  const neverVisited = d && first.data === null && d.totals.pageviews === 0 && d.previous.pageviews === 0 && !anyFilter;

  const lists: Record<FilterKey, AnalyticsCount[]> = {
    page: d?.pages ?? [],
    entry: d?.entryPages ?? [],
    exit: d?.exitPages ?? [],
    source: d?.sources ?? [],
    utmSource: d?.utmSources ?? [],
    utmMedium: d?.utmMediums ?? [],
    utmCampaign: d?.utmCampaigns ?? [],
    country: d?.countries ?? [],
    device: d?.devices ?? [],
    browser: d?.browsers ?? [],
    os: d?.os ?? [],
  };
  const rowsOf = (key: FilterKey): Row[] => lists[key].map((c) => ({ key: c.value, label: shown(key, c.value), title: shown(key, c.value), value: c.visitors, extra: c.pageviews, icon: iconFor(key, c.value) }));
  const allName = all ? FILTERS.find((f) => f.key === all)!.name : "";

  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }]} />} title="Analytics" actions={setup.data ? <SetupDialog setup={setup.data} open={guide} onOpenChange={setGuide} /> : undefined} />

      <div className="mt-6 flex flex-wrap items-center gap-2">
        {apps.length > 1 && <AppPicker apps={apps} app={search.app} onChange={(a) => go({ app: a })} />}
        <RangePicker
          per={per}
          from={search.from}
          to={search.to}
          compare={compare}
          onPeriod={(v) => go({ period: v, from: undefined, to: undefined, interval: undefined })}
          onDays={(from, to) => go({ from, to, period: undefined, interval: undefined })}
          onCompare={(on) => go({ compare: on ? undefined : "off" }, true)}
        />
        <FilterMenu filters={filters} onPick={setAll} />
        <span className="ml-auto flex items-center gap-1">
          {rt.data && <LiveNow rt={rt.data} />}
          <InfoTip label="About these numbers">Days are UTC. Each visitor counts once a day, without cookies. This page updates every minute.</InfoTip>
        </span>
      </div>
      {anyFilter && (
        <div className="mt-3">
          <FilterChips filters={filters} onRemove={(k) => setFilter(k, undefined)} onClear={clearFilters} />
        </div>
      )}

      {o.isError && (
        <div className="mt-6 flex max-w-[46rem] flex-col items-start gap-3">
          <ProblemNote className="w-full" error={o.error} title="Couldn’t load analytics" />
          <Button size="md" onClick={() => void o.refetch()}>
            Try again
          </Button>
        </div>
      )}
      {o.isPending && <Loading />}

      {d && neverVisited && setup.data && (
        <section className="mt-6 grid gap-x-12 gap-y-6 rounded-[12px] border border-rule-2 bg-paper-raised px-5 py-6 shadow-[var(--top-light)] sm:px-7 lg:grid-cols-[minmax(0,0.75fr)_minmax(0,1.25fr)]" aria-labelledby="waiting">
          <div>
            <h2 id="waiting" className="text-[1.0625rem] font-[550] text-ink">
              Waiting for the first visit
            </h2>
            <p className="mt-2 text-[0.875rem] text-ink-2">Analytics is on for {project}. Open one of its pages and the visit shows here within a few seconds: visitors, pages, where they came from and how fast the pages loaded.</p>
            <p className="mt-3 text-[0.8125rem] text-ink-3">No cookies and no consent banner. Everything stays on this box.</p>
          </div>
          <SetupGuide setup={setup.data} />
        </section>
      )}

      {d && !neverVisited && (
        <div className={cn("transition-opacity duration-200", o.isPlaceholderData && "opacity-60")} aria-busy={o.isFetching}>
          <section aria-labelledby="trend" className={cn("rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]", anyFilter ? "mt-4" : "mt-6")}>
            <Headline d={d} metric={metric} onMetric={(x) => go({ metric: x === "visitors" ? undefined : x }, true)} compare={compare} partial={since !== undefined} per={per} />
            <MainChart
              d={d}
              metric={metric}
              per={per}
              compare={compare}
              since={since}
              markers={markers}
              now={rt.data ? Date.parse(rt.data.at) : undefined}
              interval={canChoose ? interval : undefined}
              onInterval={(i) => go({ interval: i === "day" ? undefined : i }, true)}
            />
          </section>

          {d.totals.visitors === 0 && anyFilter ? (
            <div className="mt-6 rounded-[12px] border border-dashed border-rule-3 px-6 py-8 text-center">
              <p className="text-[0.9375rem] font-[550] text-ink">No visits match these filters</p>
              <p className="mt-1 text-[0.875rem] text-ink-3">Try a longer period, or remove a filter.</p>
              <Button size="md" className="mt-4" onClick={clearFilters}>
                Clear filters
              </Button>
            </div>
          ) : d.totals.visitors === 0 ? (
            <div className="mt-6 rounded-[12px] border border-dashed border-rule-3 px-6 py-8 text-center">
              <p className="text-[0.9375rem] font-[550] text-ink">No visits {per.v === "custom" ? "on these days" : per.label.toLowerCase().replace(/^last/, "in the last")}</p>
              <p className="mt-1 text-[0.875rem] text-ink-3">Visits show here within seconds of the first one.</p>
              {per.v !== "30d" && per.ms < 30 * DAY && (
                <Button size="md" className="mt-4" onClick={() => go({ period: "30d", from: undefined, to: undefined, interval: undefined })}>
                  Show the last 30 days
                </Button>
              )}
            </div>
          ) : (
            <div className="mt-6 grid gap-6 lg:grid-cols-2">
              <Breakdown
                id="pages"
                title="Pages"
                mono
                total={d.totals.visitors}
                filters={filters}
                onFilter={setFilter}
                onAll={setAll}
                rowsOf={rowsOf}
                tabs={[
                  { label: "Top pages", key: "page", name: "Page" },
                  { label: "Entry pages", key: "entry", name: "Entry page" },
                  { label: "Exit pages", key: "exit", name: "Exit page" },
                ]}
              />
              <Breakdown
                id="sources"
                title="Sources"
                total={d.totals.visitors}
                filters={filters}
                onFilter={setFilter}
                onAll={setAll}
                rowsOf={rowsOf}
                tabs={[
                  { label: "Referrers", key: "source", name: "Referrer", empty: "No referrers: visits came directly, or from sites that don’t say where from." },
                  { label: "UTM source", key: "utmSource", name: "utm_source", empty: "No utm_source tags. Add ?utm_source=newsletter to links you share." },
                  { label: "Medium", key: "utmMedium", name: "utm_medium", empty: "No utm_medium tags, like ?utm_medium=email." },
                  { label: "Campaign", key: "utmCampaign", name: "utm_campaign", empty: "No utm_campaign tags, like ?utm_campaign=launch." },
                ]}
              />
              <Countries d={d} rows={rowsOf("country")} selected={filters.country} onFilter={toggle("country")} onAll={() => setAll("country")} />
              <Breakdown
                id="devices"
                title="Devices"
                total={d.totals.visitors}
                filters={filters}
                onFilter={setFilter}
                onAll={setAll}
                rowsOf={rowsOf}
                tabs={[
                  { label: "Devices", key: "device", name: "Device" },
                  { label: "Browsers", key: "browser", name: "Browser" },
                  { label: "Systems", key: "os", name: "System" },
                ]}
              />
              <Events data={ev.data?.events ?? []} pending={ev.isPending} visitors={d.totals.visitors} onSetup={setup.data ? () => setGuide(true) : undefined} />
            </div>
          )}

          {vitals.data && (
            <div className="mt-6">
              <WebVitals
                v={vitals.data}
                code={setup.data?.vitals}
                onSetup={setup.data ? () => setGuide(true) : undefined}
                vital={search.vital ?? "LCP"}
                onVital={(x: Vital) => go({ vital: x }, true)}
                selected={filters.page}
                onPage={toggle("page")}
                ignored={Object.keys(filters).some((k) => k !== "page")}
              />
            </div>
          )}
        </div>
      )}

      {all && (
        <AllRows
          open
          onOpenChange={(x) => !x && setAll(null)}
          title={titles[all]}
          name={allName}
          rows={rowsOf(all)}
          total={d?.totals.visitors ?? 0}
          selected={filters[all]}
          onSelect={toggle(all)}
          mono={isPath(all)}
          what={allName.toLowerCase()}
          extraName={isPath(all) ? (all === "page" ? "Views" : "Visits") : undefined}
        />
      )}
    </Page>
  );
}

function iconFor(key: FilterKey, v: string): ReactNode {
  if (key === "country") return <span className="w-5 shrink-0 font-mono text-[0.6875rem] text-ink-2">{v}</span>;
  if (key === "device") {
    const I = v === "mobile" ? Smartphone : v === "tablet" ? Tablet : Monitor;
    return <I aria-hidden className="size-3.5 shrink-0 text-ink-3" />;
  }
  return undefined;
}

function Loading() {
  return (
    <div className="mt-6" aria-busy aria-label="Loading analytics">
      <div className="rounded-[12px] border border-rule-2 bg-paper-raised p-4">
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          {[0, 1, 2, 3].map((i) => (
            <div key={i}>
              <Skeleton className="h-3.5 w-20" />
              <Skeleton className="mt-2.5 h-7 w-24" />
            </div>
          ))}
        </div>
        <Skeleton className="mt-6 h-[17rem]" />
      </div>
      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-72 rounded-[12px]" />
        <Skeleton className="h-72 rounded-[12px]" />
      </div>
    </div>
  );
}

/** A change as a small badge: green when it's good news, red when it isn't. */
function DeltaBadge({ delta, lowerIsBetter }: { delta: Delta; lowerIsBetter?: boolean }) {
  const good = delta.dir === 0 ? 0 : (delta.dir > 0) !== !!lowerIsBetter ? 1 : -1;
  const Icon = delta.dir > 0 ? ArrowUp : ArrowDown;
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-0.5 rounded-[5px] px-1.5 text-[0.71875rem] font-[550] tnum",
        // The green deepened toward the ink: small text on a tinted, selected tab keeps ≥ 4.5:1 in both themes.
        good > 0 ? "bg-ok-wash text-[color-mix(in_oklab,var(--ok),var(--ink)_30%)]" : good < 0 ? "bg-danger-wash text-danger" : "bg-paper-sunk text-ink-3",
      )}
    >
      {delta.dir !== 0 && <Icon aria-hidden className="size-3" strokeWidth={2.5} />}
      {delta.text.replace(/^[+−]/, "")}
    </span>
  );
}

/** The four headline numbers as tabs: each with its change; choosing one draws it below. */
function Headline({ d, metric, onMetric, compare, partial, per }: { d: AnalyticsOverview; metric: Metric; onMetric: (m: Metric) => void; compare: boolean; partial: boolean; per: Per }) {
  const t = d.totals;
  const p = d.previous;
  const base = partial || !compare ? NaN : p.visitors;
  const before = per.before;
  const tiles: Array<{ m: Metric; value: string; was: string; delta: Delta | null }> = [
    { m: "visitors", value: int(t.visitors), was: int(p.visitors), delta: change(t.visitors, p.visitors, base) },
    { m: "pageviews", value: int(t.pageviews), was: int(p.pageviews), delta: change(t.pageviews, p.pageviews, base) },
    { m: "bounce", value: t.sessions ? pct(t.bounceRate) : "–", was: p.sessions ? pct(p.bounceRate) : "–", delta: change(t.bounceRate, p.bounceRate, base, true) },
    { m: "duration", value: t.sessions ? visitLength(t.avgSessionSeconds) : "–", was: p.sessions ? visitLength(p.avgSessionSeconds) : "–", delta: change(t.avgSessionSeconds, p.avgSessionSeconds, base) },
  ];
  return (
    // A Radix radio group: arrow keys move between the numbers and choose one.
    <RadioGroup value={metric} onValueChange={(v) => onMetric(v as Metric)} aria-label="What the chart shows" className="grid grid-cols-2 border-b border-rule lg:grid-cols-4">
      {tiles.map((x, i) => {
        const on = metric === x.m;
        return (
          <RadioItem
            key={x.m}
            value={x.m}
            title={compare ? `${x.value} against ${x.was} ${before}` : undefined}
            className={cn(
              "relative flex min-w-0 flex-col justify-start px-4 pt-3.5 pb-4 text-left transition-colors duration-[var(--dur-state)] sm:px-5",
              i === 0 && "rounded-tl-[11px]",
              i === 3 && "lg:rounded-tr-[11px]",
              i === 1 && "max-lg:rounded-tr-[11px]",
              on ? "bg-paper-raised" : "bg-paper-sunk/60 hover:bg-paper-hover",
              i % 2 === 1 && "border-l border-rule",
              i === 2 && "lg:border-l lg:border-rule",
              i > 1 && "max-lg:border-t max-lg:border-rule",
            )}
          >
            {on && <span aria-hidden className="absolute inset-x-0 -bottom-px h-[2px] bg-ink" />}
            <span className={cn("block text-[0.8125rem]", on ? "font-[550] text-ink" : "text-ink-2")}>{metrics[x.m].label}</span>
            <span className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className={cn("text-[1.625rem] leading-8 font-[500] tracking-[-0.02em] tnum", on ? "text-ink" : "text-ink-2")}>{x.value}</span>
              {x.delta && <DeltaBadge delta={x.delta} lowerIsBetter={metrics[x.m].lowerIsBetter} />}
            </span>
            {compare && <span className="sr-only">{`, against ${x.was} ${before}`}</span>}
          </RadioItem>
        );
      })}
    </RadioGroup>
  );
}

function MainChart({
  d,
  metric,
  per,
  compare,
  since,
  markers,
  now,
  interval,
  onInterval,
}: {
  d: AnalyticsOverview;
  metric: Metric;
  per: Per;
  compare: boolean;
  since?: number;
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
    const out: Series[] = [{ id: "now", label: M.label, points: pts.map((p) => [Date.parse(p.t), valueOf(p, metric)]), area: metric === "visitors" || metric === "pageviews", tone: "ink" }];
    if (compare) out.push({ id: "before", label: per.before.replace(/^./, (c) => c.toUpperCase()), points: prev.map((p) => [Date.parse(p.t), valueOf(p, metric)]), ghost: true });
    return out;
  }, [d, metric, M.label, compare, per.before]);
  const title = `${M.label} per ${hourly ? "hour" : "day"}`;
  return (
    <div className="px-4 pt-3 pb-4 sm:px-5">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
          <h2 id="trend" className="sr-only">
            {title}
          </h2>
          <Legend
            items={[
              { label: per.label, color: "var(--data)", line: true },
              ...(compare ? [{ label: per.before.replace(/^./, (c) => c.toUpperCase()), color: "var(--ink-4)", line: true, dashed: true }] : []),
              ...(markers.length ? [{ label: "Deploy", color: "var(--ink-4)", line: true }] : []),
            ]}
          />
          {since !== undefined && compare && <span className="text-[0.75rem] text-ink-3">Counting started {dayFmt.format(new Date(since))}, so changes are left out.</span>}
        </div>
        <div className="flex items-center gap-1.5">
          {interval && (
            <Segmented
              label="Points"
              value={interval}
              options={[
                { value: "hour", label: "Hourly", short: "Hours" },
                { value: "day", label: "Daily", short: "Days" },
              ]}
              onChange={onInterval}
            />
          )}
          <ViewToggle tables={table} onToggle={() => setTable((x) => !x)} />
        </div>
      </div>
      {table ? (
        <div className="max-h-[22rem] overflow-y-auto">
          <SeriesTable series={series} step={step} utc format={M.format} caption={title} />
        </div>
      ) : (
        <TimeSeries series={series} step={step} utc format={M.format} axisFormat={M.axis} label={title} height={280} markers={markers} now={now} minTop={M.minTop} />
      )}
    </div>
  );
}

type Tab = { label: string; key: FilterKey; name: string; empty?: string };

/** A breakdown panel: tabs for its views, the top rows, and "Show all". */
function Breakdown({
  id,
  title,
  tabs,
  total,
  mono,
  filters,
  onFilter,
  onAll,
  rowsOf,
}: {
  id: string;
  title: string;
  tabs: Tab[];
  total: number;
  mono?: boolean;
  filters: Partial<Record<FilterKey, string>>;
  onFilter: (key: FilterKey, v: string | undefined) => void;
  onAll: (key: FilterKey) => void;
  rowsOf: (key: FilterKey) => Row[];
}) {
  const [i, setI] = useState(() => Math.max(0, tabs.findIndex((t) => filters[t.key])));
  const tab = tabs[i];
  const rows = rowsOf(tab.key);
  return (
    <Panel id={id} title={title} tabs={tabs.map((t) => t.label)} tab={i} onTab={setI}>
      <div className="flex flex-1 flex-col px-2 pt-2.5 pb-2">
        <RowsHead name={tab.name} />
        {rows.length === 0 ? (
          <p className="px-2.5 py-6 text-[0.84375rem] text-ink-3">{tab.empty ?? "Nothing in this period."}</p>
        ) : (
          <Rows rows={rows} total={total} mono={mono} show={8} what={tab.name.toLowerCase()} selected={filters[tab.key]} onSelect={(v) => onFilter(tab.key, filters[tab.key] === v ? undefined : v)} />
        )}
        {rows.length > 8 && (
          <button type="button" onClick={() => onAll(tab.key)} className="mt-auto ml-1 inline-flex h-8 items-center gap-1 self-start rounded-[6px] px-1.5 pt-1 text-[0.8125rem] text-ink-3 hover:text-ink">
            Show all {rows.length >= 50 ? "50" : rows.length}
            <ChevronRight aria-hidden className="size-3.5" />
          </button>
        )}
      </div>
    </Panel>
  );
}

/** Countries: the map and the ranked list beside it (the list is the way in by keyboard). */
function Countries({ d, rows, selected, onFilter, onAll }: { d: AnalyticsOverview; rows: Row[]; selected?: string; onFilter: (c: string) => void; onAll: () => void }) {
  const values = Object.fromEntries((d.countries ?? []).map((c) => [c.value, c.visitors]));
  return (
    <Panel id="countries" title="Countries" className="lg:col-span-2">
      <div className="grid gap-x-10 gap-y-2 px-2 pt-2.5 pb-2 lg:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)] lg:pl-4">
        <WorldMap values={values} name={countryName} selected={selected} onSelect={onFilter} className="self-center max-lg:hidden" />
        <div className="flex min-w-0 flex-col">
          <RowsHead name="Country" />
          {rows.length === 0 ? (
            <p className="px-2.5 py-6 text-[0.84375rem] text-ink-3">No countries yet. They need the box’s country database.</p>
          ) : (
            <Rows rows={rows} total={d.totals.visitors} show={8} what="country" selected={selected} onSelect={onFilter} />
          )}
          {rows.length > 8 && (
            <button type="button" onClick={onAll} className="mt-auto ml-1 inline-flex h-8 items-center gap-1 self-start rounded-[6px] px-1.5 pt-1 text-[0.8125rem] text-ink-3 hover:text-ink">
              Show all {rows.length}
              <ChevronRight aria-hidden className="size-3.5" />
            </button>
          )}
        </div>
      </div>
    </Panel>
  );
}

/** Custom events: how many visitors did each, and the values they sent. */
function Events({ data, pending, visitors, onSetup }: { data: AnalyticsEvent[]; pending: boolean; visitors: number; onSetup?: () => void }) {
  const [open, setOpen] = useState("");
  const most = Math.max(1, ...data.map((e) => e.visitors));
  return (
    <Panel id="events" title="Events">
      <div className="flex flex-1 flex-col px-2 pt-2.5 pb-2">
        {pending ? (
          <Skeleton className="mx-2 h-40" />
        ) : data.length === 0 ? (
          <div className="px-2.5 py-5 text-[0.84375rem] text-ink-3">
            <p>
              No events yet. Count sign-ups, checkouts or anything else{" "}
              {onSetup ? "from your app." : <>with <code className="font-mono text-[0.78rem] text-ink-2">tiffin.track("Signup")</code>.</>}
            </p>
            {onSetup && (
              <button type="button" onClick={onSetup} className="mt-2 text-[0.8125rem] font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                See how
              </button>
            )}
          </div>
        ) : (
          <>
            <div className="flex items-center gap-3 pr-1 pb-1 text-[0.71875rem] text-ink-3">
              <span className="min-w-0 flex-1 pl-2.5">Event</span>
              <span className="w-14 shrink-0 text-right">Visitors</span>
              <span className="w-14 shrink-0 text-right">Count</span>
              <span className="w-12 shrink-0 text-right" title="Share of visitors who did it">
                Rate
              </span>
            </div>
            {/* A Radix accordion: an event with properties opens to show their top values. */}
            <Accordion.Root type="single" collapsible value={open} onValueChange={setOpen} className="flex flex-col gap-px">
              {data.map((e) => {
                const props = Object.entries(e.props ?? {});
                const body = (
                  <>
                    <span className="relative flex h-8 min-w-0 flex-1 items-center pl-2.5">
                      <span aria-hidden className="absolute inset-y-0.5 left-0 rounded-[5px] bg-data-wash" style={{ width: `${Math.max(1.5, (e.visitors / most) * 100)}%` }} />
                      <span className="relative truncate text-[0.84375rem] text-ink">{e.name}</span>
                      {props.length > 0 && <ChevronRight aria-hidden className="relative ml-1 size-3.5 shrink-0 text-ink-3 transition-transform group-data-[state=open]:rotate-90" />}
                    </span>
                    <span className="w-14 shrink-0 text-right text-[0.84375rem] text-ink tnum">{int(e.visitors)}</span>
                    <span className="w-14 shrink-0 text-right text-[0.84375rem] text-ink-2 tnum">{int(e.count)}</span>
                    <span className="w-12 shrink-0 text-right text-[0.75rem] text-ink-3 tnum">{visitors ? pct(e.visitors / visitors, 1) : "–"}</span>
                  </>
                );
                if (!props.length)
                  return (
                    <div key={e.name} className="flex items-center gap-3 pr-1">
                      {body}
                    </div>
                  );
                return (
                  <Accordion.Item key={e.name} value={e.name}>
                    <Accordion.Header asChild>
                      <div>
                        <Accordion.Trigger className="group flex w-full items-center gap-3 rounded-[6px] pr-1 text-left">{body}</Accordion.Trigger>
                      </div>
                    </Accordion.Header>
                    <Accordion.Content className="mt-1 mb-2 ml-2.5 border-l border-rule-2 pl-3">
                      {props.map(([k, vs]) => {
                        const top = Math.max(1, ...(vs ?? []).map((v) => v.count));
                        return (
                          <div key={k} className="py-1">
                            <p className="font-mono text-[0.71875rem] text-ink-3">{k}</p>
                            <ul className="mt-0.5">
                              {(vs ?? []).slice(0, 6).map((v) => (
                                <li key={v.value} className="flex items-center gap-3 py-0.5 pr-1 text-[0.8125rem]">
                                  <span className="relative min-w-0 flex-1 py-0.5 pl-2">
                                    <span aria-hidden className="absolute inset-y-0 left-0 rounded-[4px] bg-data-wash" style={{ width: `${Math.max(2, (v.count / top) * 100)}%` }} />
                                    <span className="relative block truncate text-ink-2" title={v.value}>
                                      {v.value}
                                    </span>
                                  </span>
                                  <span className="w-14 shrink-0 text-right text-ink-2 tnum">{int(v.count)}</span>
                                  <span className="w-12 shrink-0" />
                                </li>
                              ))}
                            </ul>
                          </div>
                        );
                      })}
                    </Accordion.Content>
                  </Accordion.Item>
                );
              })}
            </Accordion.Root>
          </>
        )}
      </div>
    </Panel>
  );
}

/** When each app (or the one in view) went live, for the chart's markers. */
function useDeployMarkers(project: string, apps: string[], only?: string): Array<{ t: number; label: string }> {
  const list = only ? apps.filter((a) => a === only) : apps;
  const res = useQueries({ queries: list.map((a) => deploysQuery(project, a)) });
  const all = res.flatMap((r) => r.data ?? []);
  // Everything the markers read: a deploy that goes live after the first read gets its marker.
  const key = all.map((x) => `${x.id}:${x.status}:${x.liveAt ?? ""}:${x.finishedAt ?? ""}`).join(",");
  return useMemo(
    () =>
      all
        .filter((x) => !x.preview && (x.liveAt || (x.status === "live" && x.finishedAt)))
        .map((x) => ({ t: Date.parse(x.liveAt ?? x.finishedAt!), label: `${x.app} deployed` })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );
}

/**
 * When this project's first visit was counted, from the last 12 months (one
 * cheap, cached read of daily rollups): a time, null for no visits at all, or
 * undefined while loading. A first visit on the window's first day means
 * "before that", so it isn't treated as the start.
 */
function useFirstVisit(project: string) {
  return useQuery({
    queryKey: ["analytics", project, "12mo"],
    queryFn: () => mod2.analytics(project, "12mo"),
    staleTime: 600_000,
    retry: false,
    select: (r): number | null => {
      const pts = r.timeseries.points ?? [];
      const first = pts.find((x) => x.pageviews > 0);
      if (!first) return null;
      const t = Date.parse(first.t);
      return pts.length && t <= Date.parse(pts[0].t) ? 0 : t;
    },
  });
}
