import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, RefreshCw, TriangleAlert } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { mod2, type AnalyticsQuery } from "@/api/modules";
import { q as api } from "@/api/queries";
import { change, DeltaBadge, Panel, useDeployMarkers, type Delta } from "@/components/analytics-kit";
import { AppPicker, customPer, DAY, periods, RangePicker, type Per } from "@/components/analytics-toolbar";
import { Legend } from "@/components/charts/bars";
import { SeriesTable, TimeSeries, type Series } from "@/components/charts/time-series";
import { InfoTip } from "@/components/info-tip";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { StatusDot } from "@/components/project-domains";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { ViewToggle } from "@/components/usage-charts";
import { cn } from "@/lib/cn";
import { dec, int, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { pingSearch, purposeWords, reasonWords, reportQuery, type SearchReport } from "@/lib/search";
import { relative } from "@/lib/time";
import type { AnalyticsSearch } from "@/routes/analytics-search";

/**
 * Analytics › Search: how people find the project through search. Google's
 * clicks, impressions, click rate and position for the days chosen (the
 * four numbers pick what the chart draws, with deploys marked), the top
 * searches and pages; then Found (is it set up, what Google indexed),
 * Crawlers (which search engines read the pages) and AI (visits from AI
 * apps, and AI companies' bots). Google's numbers run 2 to 3 days behind,
 * so its days end on the last one Google has; the crawler log and the
 * visits are live.
 */

/** The periods that make sense for search: no hours, Google is days behind. */
export const SEARCH_PERIODS: Per[] = periods.filter((p) => ["7d", "30d", "90d", "12mo"].includes(p.v));

type Metric = "clicks" | "impressions" | "ctr" | "position";
type Day = SearchReport extends { searches?: infer S } ? (S extends { daily: (infer D)[] | null } ? D : never) : never;

const metrics: Record<
  Metric,
  { label: string; format: (v: number) => string; axis?: (v: number) => string; lowerIsBetter?: boolean; minTop?: number }
> = {
  clicks: { label: "Clicks", format: int },
  impressions: { label: "Impressions", format: int },
  ctr: { label: "Click rate", format: (v) => pct(v, 1), axis: (v) => pct(v), minTop: 0.05 },
  position: { label: "Average position", format: (v) => dec(v, 1), lowerIsBetter: true },
};

const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
const dayWords = (d: string) => dayFmt.format(Date.parse(`${d}T00:00:00Z`));

/** The AI apps analytics names as referrers (internal/mod/analytics/enrich). */
const AI_APPS = new Set([
  "ChatGPT",
  "Claude",
  "Perplexity",
  "Gemini",
  "Copilot",
  "Grok",
  "DeepSeek",
  "Le Chat",
  "chatgpt.com",
  "perplexity",
  "claude.ai",
]);

export function SearchView({
  project,
  search,
  go,
}: {
  project: string;
  search: AnalyticsSearch;
  go: (patch: Partial<AnalyticsSearch>, replace?: boolean) => void;
}) {
  const custom = !!search.from;
  const preset = SEARCH_PERIODS.find((x) => x.v === (search.period ?? "30d")) ?? SEARCH_PERIODS[1];
  const per: Per = custom ? customPer(search.from!, search.to) : preset;
  const compare = search.compare !== "off";
  const r = useQuery({
    ...reportQuery(project, custom ? { from: search.from, to: search.to, host: search.host } : { period: preset.v, host: search.host }),
    placeholderData: keepPreviousData,
  });
  const m = useQuery({ ...api.manifest(project), retry: false, staleTime: 60_000 });
  const markers = useDeployMarkers(project, Object.keys(m.data?.manifest.apps ?? {}).sort());
  const d = r.data;
  const findable = (d?.hosts ?? []).filter((h) => h.findable);

  return (
    <>
      <div className="mt-5 flex flex-wrap items-center gap-2">
        {findable.length > 1 && (
          <AppPicker apps={findable.map((h) => h.host)} app={search.host} onChange={(h) => go({ host: h })} label="All addresses" />
        )}
        <RangePicker
          per={per}
          from={search.from}
          to={search.to}
          compare={compare}
          presets={SEARCH_PERIODS}
          onPeriod={(v) => go({ period: v, from: undefined, to: undefined })}
          onDays={(from, to) => go({ from, to, period: undefined })}
          onCompare={(on) => go({ compare: on ? undefined : "off" }, true)}
        />
        <span className="ml-auto">
          <InfoTip label="About these numbers">
            Google’s numbers come from Search Console, read twice a day and kept on this box for 16 months. They run 2 to 3 days behind, on Pacific
            time, so a period ends on the last day Google has. Crawlers and AI visits are live and end today.
          </InfoTip>
        </span>
      </div>

      {r.isError && (
        <div className="mt-6 flex max-w-[46rem] flex-col items-start gap-3">
          <ProblemNote className="w-full" error={r.error} title="Couldn’t load search" />
          <Button size="md" onClick={() => void r.refetch()}>
            Try again
          </Button>
        </div>
      )}
      {r.isPending && <Loading />}

      {d && findable.length === 0 && <NotFindable project={project} d={d} />}

      {d && findable.length > 0 && (
        <div className={cn("transition-opacity duration-200", r.isPlaceholderData && "opacity-60")} aria-busy={r.isFetching}>
          <Searches
            project={project}
            d={d}
            per={per}
            compare={compare}
            markers={markers}
            onLonger={per.v !== "12mo" ? () => go({ period: "12mo", from: undefined, to: undefined }) : undefined}
          />
          <div className="mt-6 grid items-start gap-6 lg:grid-cols-2">
            <Found project={project} d={d} />
            <Crawlers d={d} />
            <AI project={project} d={d} query={custom ? { from: search.from, to: search.to } : { period: preset.v as AnalyticsQuery["period"] }} />
          </div>
        </div>
      )}
    </>
  );
}

function Loading() {
  return (
    <div className="mt-6" aria-busy aria-label="Loading search">
      <Skeleton className="h-[25rem] rounded-[12px]" />
      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-64 rounded-[12px]" />
        <Skeleton className="h-64 rounded-[12px]" />
      </div>
    </div>
  );
}

/** No address search engines may find: why, and the one thing to do. */
function NotFindable({ project, d }: { project: string; d: SearchReport }) {
  const hosts = d.hosts ?? [];
  const hidden = hosts.find((h) => h.hidden);
  const why = hidden ? "The project is kept out of search results." : (hosts[0]?.why ?? "No app is live yet.");
  const fix = hidden ? undefined : hosts.find((h) => h.fix)?.fix;
  return (
    <section className="mt-6 rounded-[12px] border border-dashed border-rule-3 px-6 py-9 text-center" aria-labelledby="not-findable">
      <h2 id="not-findable" className="text-[0.9375rem] font-[550] text-ink">
        Not in search results yet
      </h2>
      <p className="mx-auto mt-1 max-w-[34rem] text-[0.875rem] text-ink-3">
        {why} {fix ?? (hidden ? "Its apps still open for anyone with the link." : "")}
      </p>
      {hidden ? (
        <Button asChild size="md" className="mt-4">
          <Link to="/projects/$project/settings" params={{ project }}>
            Change in Settings
          </Link>
        </Button>
      ) : (
        hosts.some((h) => h.fix) && (
          <Button asChild size="md" variant="primary" className="mt-4">
            <Link to="/projects/$project/domains" params={{ project }}>
              Add a domain
            </Link>
          </Button>
        )
      )}
    </section>
  );
}

/** Every day from..to, with no searches where Google had none (a gap for position and rate). */
function fillDays(days: Day[], from: string, to: string, through?: string): Day[] {
  const by = new Map(days.map((x) => [x.date, x]));
  const out: Day[] = [];
  const end = Date.parse(`${through && through < to ? through : to}T00:00:00Z`);
  for (let t = Date.parse(`${from}T00:00:00Z`); t <= end; t += DAY) {
    const k = new Date(t).toISOString().slice(0, 10);
    out.push(by.get(k) ?? { date: k, clicks: 0, impressions: 0, position: 0 });
  }
  return out;
}

function valueOf(x: Day, m: Metric) {
  if (m === "clicks") return x.clicks;
  if (m === "impressions") return x.impressions;
  if (!x.impressions) return NaN;
  return m === "ctr" ? x.clicks / x.impressions : x.position;
}

function Searches({
  project,
  d,
  per,
  compare,
  markers,
  onLonger,
}: {
  project: string;
  d: SearchReport;
  per: Per;
  compare: boolean;
  markers: Array<{ t: number; label: string }>;
  onLonger?: () => void;
}) {
  const [metric, setMetric] = useState<Metric>("clicks");
  const [table, setTable] = useState(false);
  const { admin } = useMe();
  const g = d.google;
  const s = d.searches;
  const series = useMemo<Series[]>(() => {
    if (!s) return [];
    const now = fillDays(s.daily ?? [], d.from, d.to, s.through);
    const before = fillDays(s.prevDaily ?? [], d.prevFrom, d.prevTo);
    const M = metrics[metric];
    const out: Series[] = [
      {
        id: "now",
        label: M.label,
        points: now.map((x) => [Date.parse(`${x.date}T00:00:00Z`), valueOf(x, metric)]),
        area: metric === "clicks" || metric === "impressions",
        tone: "ink",
      },
    ];
    if (compare)
      out.push({
        id: "before",
        label: per.before.replace(/^./, (c) => c.toUpperCase()),
        points: before.map((x) => [Date.parse(`${x.date}T00:00:00Z`), valueOf(x, metric)]),
        ghost: true,
      });
    return out;
  }, [s, d.from, d.to, d.prevFrom, d.prevTo, metric, compare, per.before]);

  const frame = (body: ReactNode) => (
    <section aria-labelledby="searches" className="mt-6 rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
      <h2 id="searches" className="sr-only">
        Searches on Google
      </h2>
      {body}
    </section>
  );
  const note = (title: string, body: ReactNode, action?: ReactNode) =>
    frame(
      <div className="px-6 py-9 text-center">
        <p className="text-[0.9375rem] font-[550] text-ink">{title}</p>
        <div className="mx-auto mt-1 max-w-[34rem] text-[0.875rem] text-ink-3">{body}</div>
        {action}
      </div>,
    );

  if (g.state === "not_connected")
    return note(
      `See what people search on Google to find ${project}`,
      admin
        ? "Connect Google Search Console once for the whole box. The box verifies each domain for you and reads the numbers back twice a day."
        : "The box’s owner connects Google Search Console once, in Settings › Search engines.",
      admin && (
        <Button asChild size="md" variant="primary" className="mt-4">
          <Link to="/settings/search">Connect Google</Link>
        </Button>
      ),
    );
  if (g.state === "verifying") return note(`Verifying ${g.property ?? "the domain"} with Google`, g.detail ?? "This takes a minute or two.");
  if (g.state === "failed")
    return note(
      `Google couldn’t verify ${g.property ?? "the domain"}`,
      g.detail,
      admin && (
        <Button asChild size="md" className="mt-4">
          <Link to="/settings/search">Open Search engines</Link>
        </Button>
      ),
    );
  if (!s) return null;
  if (!(s.daily ?? []).length && g.filling)
    return note("Reading Google’s numbers", "The first read takes a few minutes. Google keeps 16 months, and they fill in over the next hour.");

  const t = s.totals;
  const p = s.previous;
  const base = compare ? p.impressions : NaN;
  const tiles: Array<{ m: Metric; value: string; was: string; delta: Delta | null }> = [
    { m: "clicks", value: int(t.clicks), was: int(p.clicks), delta: change(t.clicks, p.clicks, base) },
    { m: "impressions", value: int(t.impressions), was: int(p.impressions), delta: change(t.impressions, p.impressions, base) },
    { m: "ctr", value: t.impressions ? pct(t.ctr, 1) : "–", was: p.impressions ? pct(p.ctr, 1) : "–", delta: change(t.ctr, p.ctr, base, true) },
    {
      m: "position",
      value: t.impressions ? dec(t.position, 1) : "–",
      was: p.impressions ? dec(p.position, 1) : "–",
      delta: p.position && t.position ? positionChange(t.position, p.position, base) : null,
    },
  ];
  const M = metrics[metric];
  const title = `${M.label} per day`;
  const empty = t.impressions === 0;

  return frame(
    <>
      <RadioGroup
        value={metric}
        onValueChange={(v) => setMetric(v as Metric)}
        aria-label="What the chart shows"
        className="grid grid-cols-2 border-b border-rule lg:grid-cols-4"
      >
        {tiles.map((x, i) => {
          const on = metric === x.m;
          return (
            <RadioItem
              key={x.m}
              value={x.m}
              title={compare ? `${x.value} against ${x.was} ${per.before}` : undefined}
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
              {compare && <span className="sr-only">{`, against ${x.was} ${per.before}`}</span>}
            </RadioItem>
          );
        })}
      </RadioGroup>
      <div className="px-4 pt-3 pb-4 sm:px-5">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
            <Legend
              items={[
                { label: `${dayWords(d.from)} – ${dayWords(s.through && s.through < d.to ? s.through : d.to)}`, color: "var(--data)", line: true },
                ...(compare ? [{ label: per.before.replace(/^./, (c) => c.toUpperCase()), color: "var(--ink-4)", line: true, dashed: true }] : []),
                ...(markers.length ? [{ label: "Deploy", color: "var(--ink-4)", line: true }] : []),
              ]}
            />
            {metric === "position" && <span className="text-[0.75rem] text-ink-3">Lower is better: 1 is the top of the page.</span>}
            {g.filling && <span className="text-[0.75rem] text-ink-3">Still reading Google’s earlier months.</span>}
          </div>
          <ViewToggle tables={table} onToggle={() => setTable((x) => !x)} />
        </div>
        {table ? (
          <div className="max-h-[22rem] overflow-y-auto">
            <SeriesTable series={series} step={DAY} utc format={M.format} caption={title} />
          </div>
        ) : (
          <TimeSeries
            series={series}
            step={DAY}
            utc
            format={M.format}
            axisFormat={M.axis}
            label={title}
            height={260}
            markers={markers}
            minTop={M.minTop}
          />
        )}
        {g.error && (
          <p className="mt-3 flex items-start gap-2 text-[0.8125rem] text-warn-ink">
            <TriangleAlert aria-hidden className="mt-0.5 size-3.5 shrink-0" />
            <span>
              {g.pulledAt ? `Last read ${relative(g.pulledAt)}. ` : ""}
              {g.error}
            </span>
          </p>
        )}
      </div>
      {empty ? (
        <div className="border-t border-rule px-6 py-8 text-center">
          <p className="text-[0.9375rem] font-[550] text-ink">No searches in these days</p>
          <p className="mx-auto mt-1 max-w-[34rem] text-[0.875rem] text-ink-3">
            Google shows a page in its results once it has indexed it. A new site takes days to weeks; Found below says how far it has got.
          </p>
          {onLonger && (
            <Button size="md" className="mt-4" onClick={onLonger}>
              Show the last 12 months
            </Button>
          )}
        </div>
      ) : (
        <div className="grid border-t border-rule lg:grid-cols-2">
          <TopList
            id="queries"
            title="Searches"
            rows={s.queries ?? []}
            what="search"
            note="Google leaves out searches only a few people made, so these add up to less than the total."
          />
          <TopList id="pages" title="Pages" rows={s.pages ?? []} what="page" mono className="max-lg:border-t lg:border-l border-rule" />
        </div>
      )}
    </>,
  );
}

/** A position change in places: up the page is good news. */
function positionChange(now: number, before: number, base: number): Delta | null {
  if (!(base >= 20)) return null;
  const x = Math.round((now - before) * 10) / 10;
  if (x === 0) return { text: "0", dir: 0 };
  return { text: dec(Math.abs(x), 1), dir: x > 0 ? 1 : -1 };
}

type Top = { key: string; clicks: number; impressions: number; position: number };

/** Ranked searches or pages: a bar for the clicks, then clicks, impressions and position. */
function TopList({
  id,
  title,
  rows,
  what,
  note,
  mono,
  className,
}: {
  id: string;
  title: string;
  rows: Top[];
  what: string;
  note?: string;
  mono?: boolean;
  className?: string;
}) {
  const [all, setAll] = useState(false);
  const most = Math.max(1, ...rows.map((r) => r.clicks || r.impressions / 100));
  const list = all ? rows : rows.slice(0, 10);
  return (
    <section aria-labelledby={id} className={cn("flex min-w-0 flex-col px-2 pt-1 pb-2", className)}>
      <div className="flex h-11 items-center justify-between gap-3 px-2">
        <h3 id={id} className="text-[0.84375rem] font-[550] text-ink">
          {title}
        </h3>
        {note && <InfoTip label={`About ${title.toLowerCase()}`}>{note}</InfoTip>}
      </div>
      {rows.length === 0 ? (
        <p className="px-2.5 py-6 text-[0.84375rem] text-ink-3">Nothing in these days.</p>
      ) : (
        <>
          <div className="flex items-center gap-3 pr-1 pb-1 text-[0.71875rem] text-ink-3">
            <span className="min-w-0 flex-1 pl-2.5">{what === "page" ? "Page" : "Search"}</span>
            <span className="w-12 shrink-0 text-right">Clicks</span>
            <span className="w-16 shrink-0 text-right max-sm:hidden">Impressions</span>
            <span className="w-14 shrink-0 text-right" title="Average position (1 is the top)">
              Position
            </span>
          </div>
          <ul className="flex flex-col gap-px">
            {list.map((r) => (
              <li key={r.key} className="flex items-center gap-3 pr-1">
                <span className="relative flex h-8 min-w-0 flex-1 items-center pl-2.5" title={r.key}>
                  <span
                    aria-hidden
                    className="absolute inset-y-0.5 left-0 rounded-[5px] bg-data-wash"
                    style={{ width: `${Math.max(1.5, ((r.clicks || r.impressions / 100) / most) * 100)}%` }}
                  />
                  <span className={cn("relative truncate text-[0.84375rem] text-ink", mono && "font-mono text-[0.78rem]")}>{r.key}</span>
                </span>
                <span className="w-12 shrink-0 text-right text-[0.84375rem] text-ink tnum">{int(r.clicks)}</span>
                <span className="w-16 shrink-0 text-right text-[0.84375rem] text-ink-2 tnum max-sm:hidden">{int(r.impressions)}</span>
                <span className="w-14 shrink-0 text-right text-[0.8125rem] text-ink-2 tnum">{dec(r.position, 1)}</span>
              </li>
            ))}
          </ul>
          {rows.length > 10 && (
            <button
              type="button"
              onClick={() => setAll((x) => !x)}
              className="mt-1 ml-1 inline-flex h-8 items-center gap-1 self-start rounded-[6px] px-1.5 text-[0.8125rem] text-ink-3 hover:text-ink"
            >
              {all ? "Show fewer" : `Show all ${rows.length}`}
              <ChevronRight aria-hidden className={cn("size-3.5 transition-transform", all && "-rotate-90")} />
            </button>
          )}
        </>
      )}
    </section>
  );
}

/** What Google says about a page it hasn't indexed, in plain words. */
function coverageWords(c: string): string {
  const s = c.toLowerCase();
  if (s.includes("crawled") && s.includes("not indexed"))
    return "Google read it and chose not to list it yet. Make it more useful, or merge it with a similar page.";
  if (s.includes("discovered")) return "Google knows it but hasn’t read it yet. Usual for a new site; links from your other pages help.";
  if (s.includes("unknown")) return "Google hasn’t come across it yet.";
  if (s.includes("noindex")) return "The page says noindex.";
  if (s.includes("robots")) return "robots.txt keeps Google out.";
  if (s.includes("duplicate")) return "Google picked another page with the same content.";
  if (s.includes("canonical")) return "It names another page as the main one. Fine if that’s intended.";
  if (s.includes("redirect")) return "It redirects. Fine.";
  if (s.includes("404")) return "It answers 404.";
  return c;
}

/** Found: is the project set up to be found, and how far Google has got. */
function Found({ project, d }: { project: string; d: SearchReport }) {
  const qc = useQueryClient();
  const { can, admin } = useMe();
  const [all, setAll] = useState(false);
  const ix = d.indexNow;
  const g = d.google;
  const idx = g.index;
  const notYet = idx?.notIndexed ?? [];
  const ping = useMutation({
    mutationFn: () => pingSearch(project, d.host ?? ""),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["search", project] });
      toast({ title: "Told search engines what changed.", detail: g.state === "verified" ? "The sitemap went to Google again too." : undefined });
    },
  });
  // A path when the report covers one address; host and path when several.
  const single = !!d.host || (d.hosts ?? []).filter((h) => h.findable).length === 1;
  const short = (u: string) => {
    const x = u.replace(/^https:\/\//, "");
    const slash = x.indexOf("/");
    return single && slash >= 0 ? x.slice(slash) : x;
  };

  return (
    <Panel id="found" title="Found">
      <dl className="flex flex-1 flex-col divide-y divide-rule px-4">
        <Fact
          label="IndexNow"
          tone={ix.last && !ix.last.ok ? "warn" : ix.last ? "ok" : "busy"}
          value={
            ix.last ? (
              ix.last.ok ? (
                <>
                  Told {relative(ix.last.at)} {reasonWords[ix.last.reason] ?? ""}
                  <span className="text-ink-3">
                    {" "}
                    · {ix.last.pages ? `${int(ix.last.pages)} ${ix.last.pages === 1 ? "page" : "pages"}` : "nothing new"}
                  </span>
                </>
              ) : (
                `Refused ${relative(ix.last.at)}: ${ix.last.detail ?? `status ${ix.last.status}`}`
              )
            ) : (
              "Telling Bing and the others in a moment."
            )
          }
        />
        <Fact
          label="Sitemap"
          tone={ix.pages ? "ok" : "warn"}
          value={
            ix.pages
              ? `${int(ix.pages)} ${ix.pages === 1 ? "page" : "pages"}${g.sitemap ? `, Google found ${int(g.sitemap.pages)}` : ""}`
              : "No pages found in a sitemap."
          }
        />
        <Fact
          label="Google"
          tone={g.state === "verified" ? "ok" : g.state === "failed" ? "bad" : g.state === "verifying" ? "busy" : "off"}
          value={
            g.state === "verified" ? (
              <>
                Verified<span className="text-ink-3"> · {g.property}</span>
              </>
            ) : g.state === "not_connected" ? (
              admin ? (
                <Link to="/settings/search" className="underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                  Not connected
                </Link>
              ) : (
                "Not connected"
              )
            ) : (
              (g.detail ?? g.state)
            )
          }
        />
        {idx && (
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 py-3">
            <dt className="text-[0.8125rem] text-ink-2">In Google’s index</dt>
            <dd className="text-[0.8125rem] text-ink tnum">
              {int(idx.indexed)} of {int(idx.checked)} pages checked
            </dd>
            <dd className="w-full">
              <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-paper-sunk" aria-hidden>
                <div className="h-full rounded-full bg-data" style={{ width: `${(idx.indexed / Math.max(1, idx.checked)) * 100}%` }} />
              </div>
              {notYet.length > 0 && (
                <ul className="mt-3 flex flex-col gap-2">
                  {(all ? notYet : notYet.slice(0, 4)).map((n) => (
                    <li key={n.url} className="text-[0.8125rem]">
                      <span className="block truncate font-mono text-[0.75rem] text-ink" title={n.url}>
                        {short(n.url)}
                      </span>
                      <span className="block text-ink-3">{coverageWords(n.coverage)}</span>
                    </li>
                  ))}
                </ul>
              )}
              {notYet.length > 4 && (
                <button
                  type="button"
                  onClick={() => setAll((x) => !x)}
                  className="mt-1.5 inline-flex h-7 items-center gap-1 text-[0.8125rem] text-ink-3 hover:text-ink"
                >
                  {all ? "Show fewer" : `${notYet.length - 4} more not indexed`}
                </button>
              )}
              <p className="mt-2 text-[0.75rem] text-ink-3">The box asks Google about 20 pages a day.</p>
            </dd>
          </div>
        )}
        {(ix.problems ?? []).length > 0 && (
          <div className="py-3">
            <dt className="flex items-center gap-2 text-[0.8125rem] text-warn-ink">
              <TriangleAlert aria-hidden className="size-3.5" />
              Needs a look
            </dt>
            <dd>
              <ul className="mt-1 flex flex-col gap-1 text-[0.8125rem] text-ink-2">
                {(ix.problems ?? []).map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            </dd>
          </div>
        )}
      </dl>
      {can("apply:outbound") && (
        <div className="border-t border-rule px-4 py-2.5">
          <Button
            size="sm"
            variant="ghost"
            onClick={() => ping.mutate()}
            disabled={ping.isPending}
            title="Read the sitemap now, send IndexNow what changed, and submit the sitemap to Google again"
          >
            <RefreshCw className={cn("size-3.5!", ping.isPending && "animate-spin")} />
            Tell search engines now
          </Button>
          {ping.isError && <ProblemNote className="mt-2" error={ping.error} />}
        </div>
      )}
    </Panel>
  );
}

/** One line of Found: a dot (green, waiting, a ring for off, red), what, and its state. */
function Fact({ label, value, tone }: { label: string; value: ReactNode; tone: "ok" | "warn" | "bad" | "busy" | "off" }) {
  return (
    <div className="flex items-start justify-between gap-4 py-3">
      <dt className="flex shrink-0 items-center gap-2 text-[0.8125rem] text-ink-2">
        <StatusDot tone={tone === "warn" ? "bad" : tone === "off" ? "wait" : tone} />
        {label}
      </dt>
      <dd className="min-w-0 text-right text-[0.8125rem] text-ink">{value}</dd>
    </div>
  );
}

type Bot = NonNullable<SearchReport["crawlers"]>[number];

function BotRows({ bots, purpose }: { bots: Bot[]; purpose?: boolean }) {
  return (
    <ul className="flex flex-col divide-y divide-rule">
      {bots.map((b) => (
        <li key={b.bot} className="flex items-baseline justify-between gap-4 py-2.5">
          <span className="min-w-0">
            <span className="block text-[0.84375rem] text-ink">{b.bot}</span>
            <span className="block text-[0.75rem] text-ink-3">
              {purpose ? purposeWords[b.purpose] : b.operator}
              {b.lastAt ? ` · last ${relative(b.lastAt)}` : ""}
            </span>
          </span>
          <span className="shrink-0 text-right">
            <span className="block text-[0.84375rem] text-ink tnum">{int(b.hits)}</span>
            {b.errors > 0 && (
              <span className="block text-[0.75rem] text-warn-ink tnum" title="Requests answered 4xx or 5xx">
                {int(b.errors)} {b.errors === 1 ? "error" : "errors"}
              </span>
            )}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** Crawlers: the search engines that read the pages (verified by address). */
function Crawlers({ d }: { d: SearchReport }) {
  const bots = (d.crawlers ?? []).filter((b) => !b.ai);
  return (
    <Panel id="crawlers" title="Search engine crawlers" right="Requests">
      <div className="flex flex-1 flex-col px-4 pb-2">
        {bots.length === 0 ? (
          <p className="py-6 text-[0.84375rem] text-ink-3">
            No search engine read the pages in these days. A new domain usually sees Googlebot and Bingbot within a few days.
          </p>
        ) : (
          <BotRows bots={bots} />
        )}
        {d.fakes > 0 && (
          <p className="mt-auto border-t border-rule pt-2.5 pb-1 text-[0.75rem] text-ink-3">
            {int(d.fakes)} {d.fakes === 1 ? "request" : "requests"} claimed to be a search engine from an address that isn’t theirs. Not counted.
          </p>
        )}
      </div>
    </Panel>
  );
}

/** AI: visits from AI apps (analytics) and the AI companies' bots (the crawler log). */
function AI({ project, d, query }: { project: string; d: SearchReport; query: AnalyticsQuery }) {
  const a = useQuery({
    queryKey: ["analytics", project, "sources", query],
    queryFn: () => mod2.analyticsView(project, query, 50),
    retry: false,
    staleTime: 60_000,
  });
  const visits = (a.data?.sources ?? []).filter((s) => AI_APPS.has(s.value));
  const bots = (d.crawlers ?? []).filter((b) => b.ai);
  const most = Math.max(1, ...visits.map((v) => v.visitors));
  return (
    <Panel id="ai" title="AI" className="lg:col-span-2">
      <div className="grid gap-x-10 px-4 pb-3 lg:grid-cols-2">
        <section aria-labelledby="ai-visits" className="min-w-0">
          <div className="flex h-10 items-center justify-between">
            <h3 id="ai-visits" className="text-[0.8125rem] font-[550] text-ink">
              Visits from AI apps
            </h3>
            <span className="text-[0.75rem] text-ink-3">Visitors</span>
          </div>
          {a.isPending ? (
            <Skeleton className="h-20" />
          ) : visits.length === 0 ? (
            <p className="py-3 text-[0.84375rem] text-ink-3">No visits from ChatGPT, Claude, Perplexity or Gemini in these days.</p>
          ) : (
            <ul className="flex flex-col gap-px">
              {visits.map((v) => (
                <li key={v.value} className="flex items-center gap-3 pr-1">
                  <span className="relative flex h-8 min-w-0 flex-1 items-center pl-2.5">
                    <span
                      aria-hidden
                      className="absolute inset-y-0.5 left-0 rounded-[5px] bg-data-wash"
                      style={{ width: `${Math.max(1.5, (v.visitors / most) * 100)}%` }}
                    />
                    <span className="relative truncate text-[0.84375rem] text-ink">{v.value}</span>
                  </span>
                  <span className="w-14 shrink-0 text-right text-[0.84375rem] text-ink tnum">{int(v.visitors)}</span>
                </li>
              ))}
            </ul>
          )}
          <p className="mt-2 text-[0.75rem] text-ink-3">From your analytics. Apps that hide where a visit came from count as direct.</p>
        </section>
        <section aria-labelledby="ai-bots" className="min-w-0 max-lg:mt-4">
          <div className="flex h-10 items-center justify-between">
            <h3 id="ai-bots" className="text-[0.8125rem] font-[550] text-ink">
              AI bots
            </h3>
            <span className="text-[0.75rem] text-ink-3">Requests</span>
          </div>
          {bots.length === 0 ? (
            <p className="py-3 text-[0.84375rem] text-ink-3">No AI company’s bot read the pages in these days.</p>
          ) : (
            <BotRows bots={bots} purpose />
          )}
        </section>
      </div>
    </Panel>
  );
}
