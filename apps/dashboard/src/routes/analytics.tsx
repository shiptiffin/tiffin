import { useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod2, mod3, type AnalyticsCount, type AnalyticsEvent, type AnalyticsOverview, type AnalyticsVitals, type Period } from "@/api/modules";
import { q as api } from "@/api/queries";
import { VisitsChart, type Bucket, type Marker } from "@/components/analytics-chart";
import { CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { StateSentence } from "@/components/jobs-words";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { cn } from "@/lib/cn";
import { dec, int, MINUS, ms, NNBSP, pct } from "@/lib/format";

const periods: Array<{ v: Period; label: string; short: string; words: string; before: string }> = [
  { v: "today", label: "Today", short: "Today", words: "today so far", before: "yesterday" },
  { v: "24h", label: "24 hours", short: "24 h", words: "in the last 24 hours", before: "the 24 hours before" },
  { v: "7d", label: "7 days", short: "7 d", words: "in the last 7 days", before: "the 7 days before" },
  { v: "30d", label: "30 days", short: "30 d", words: "in the last 30 days", before: "the 30 days before" },
  { v: "90d", label: "90 days", short: "90 d", words: "in the last 90 days", before: "the 90 days before" },
  { v: "12mo", label: "12 months", short: "12 mo", words: "in the last 12 months", before: "the year before" },
];

type Metric = "visitors" | "pageviews";

/** Below this many visitors in the period before, a percentage change is noise, not news. */
const MIN_BASELINE = 20;

const regions = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames(["en-GB"], { type: "region" }) : null;
const country = (code: string) => {
  try {
    return (code && regions?.of(code)) || code || "Unknown";
  } catch {
    return code;
  }
};
const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", timeZone: "UTC" });

/** 61.8 → "1 min 2 s", 9 → "9 s", 754 → "13 min". */
function visitLength(s: number) {
  if (!s) return `0${NNBSP}s`;
  if (s < 60) return `${int(s)}${NNBSP}s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m >= 10 || rest === 0) return `${int(s / 60)}${NNBSP}min`;
  return `${m}${NNBSP}min ${rest}${NNBSP}s`;
}

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

export function AnalyticsPage({ project, period = "7d" }: { project: string; period?: string }) {
  useTitle(`${project} · Analytics`);
  const navigate = useNavigate();
  const per = periods.find((x) => x.v === period) ?? periods[2];
  const p = per.v;
  const [metric, setMetric] = useState<Metric>("visitors");
  const o = useQuery({ queryKey: ["analytics", project, p], queryFn: () => mod2.analytics(project, p), refetchInterval: 60_000, placeholderData: (d) => d });
  const rt = useQuery({ queryKey: ["analytics-rt", project], queryFn: () => mod2.realtime(project), refetchInterval: 15_000 });
  const ev = useQuery({ queryKey: ["analytics-ev", project, p], queryFn: () => mod2.events(project, p) });
  const setup = useQuery({ queryKey: ["analytics-setup", project], queryFn: () => mod2.analyticsSetup(project), staleTime: Infinity });
  const vitals = useQuery({ queryKey: ["analytics-vitals", project, p], queryFn: () => mod2.vitals(project, p), refetchInterval: 60_000 });
  const markers = useDeployMarkers(project);
  const since = useFirstVisit(project, p);
  // The previous window only counts as a baseline if visits were being counted for all of it.
  const partial = since !== undefined && since > prevStart(p);

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Analytics" />;
  const d = o.data;
  const now = rt.data?.visitorsNow ?? 0;

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }]} />}
        title="Analytics"
        lede="Counted at the box’s own edge: no cookies, nothing sent anywhere else, and ad blockers can’t hide a page view."
      />

      <div className="mt-8">{d ? <StateSentence>{sentenceFor(d, per, partial ? since : undefined)}</StateSentence> : <Skeleton className="h-8 w-[28rem] max-w-full" />}</div>
      <div className="mt-5 flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
        <div role="radiogroup" aria-label="Period" className="inline-flex shrink-0 rounded-[8px] border border-rule-2 bg-paper-sunk p-0.5">
          {periods.map((x) => (
            <button
              key={x.v}
              role="radio"
              aria-checked={p === x.v}
              aria-label={x.label}
              onClick={() => navigate({ to: "/projects/$project/analytics", params: { project }, search: x.v === "7d" ? {} : { period: x.v } })}
              className={cn(
                "h-7 rounded-[6px] px-2.5 text-[0.8125rem] whitespace-nowrap text-ink-3 transition-colors duration-[var(--dur-state)] hover:text-ink sm:px-3",
                p === x.v && "bg-paper-raised font-[550] text-ink shadow-[0_1px_2px_oklch(0.3_0.02_60/0.12)] ring-1 ring-rule-2",
              )}
            >
              <span className="sm:hidden">{x.short}</span>
              <span className="max-sm:hidden">{x.label}</span>
            </button>
          ))}
        </div>
        <span className="inline-flex items-center gap-2 text-[0.84375rem] text-ink-2" aria-live="polite">
          <span className={cn("relative size-2 rounded-full", now ? "bg-ok" : "bg-ink-4")}>
          </span>
          {now ? `${int(now)} ${now === 1 ? "person" : "people"} on the site now` : "No one on the site right now"}
        </span>
      </div>

      {o.isError && <ProblemNote className="mt-6" error={o.error} />}
      {o.isPending && <Skeleton className="mt-8 h-80" />}
      {d && (
        <div className={cn("transition-opacity", o.isPlaceholderData && "opacity-60")}>
          <dl className="mt-6 grid grid-cols-2 border-y border-rule-2 sm:grid-cols-3 lg:grid-cols-5">
            <Reading label="Visitors" value={int(d.totals.visitors)} delta={change(d.totals.visitors, d.previous.visitors, partial ? NaN : d.previous.visitors)} on={metric === "visitors"} onClick={() => setMetric("visitors")} />
            <Reading label="Page views" value={int(d.totals.pageviews)} delta={change(d.totals.pageviews, d.previous.pageviews, partial ? NaN : d.previous.visitors)} on={metric === "pageviews"} onClick={() => setMetric("pageviews")} />
            <Reading label="Views per visit" value={d.totals.visitors ? dec(d.totals.viewsPerVisit, 1) : "–"} delta={change(d.totals.viewsPerVisit, d.previous.viewsPerVisit, partial ? NaN : d.previous.visitors)} />
            <Reading label="Bounce rate" value={d.totals.visitors ? pct(d.totals.bounceRate) : "–"} delta={change(d.totals.bounceRate, d.previous.bounceRate, partial ? NaN : d.previous.visitors, true)} />
            <Reading label="Visit length" value={d.totals.visitors ? visitLength(d.totals.avgSessionSeconds) : "–"} delta={change(d.totals.avgSessionSeconds, d.previous.avgSessionSeconds, partial ? NaN : d.previous.visitors)} />
          </dl>

          <section className="mt-8" aria-label={metric === "visitors" ? "Visitors over time" : "Page views over time"}>
            <div className="mb-5 flex items-baseline justify-between gap-3">
              <h2 className="label">{metric === "visitors" ? "Visitors" : "Page views"} per {d.timeseries.granularity === "hour" ? "hour" : "day"}</h2>
              {markers.length > 0 && (
                <span className="flex items-center gap-1.5 text-[0.75rem] text-ink-3">
                  <span aria-hidden className="h-3 w-px bg-ink-3/60" /> a deploy
                </span>
              )}
            </div>
            {d.totals.pageviews === 0 ? (
              <p className="border-y border-rule py-10 text-center text-[0.875rem] text-ink-3">No visits {per.words}. Page views show up here as soon as people open your apps.</p>
            ) : (
              <VisitsChart buckets={bucketsOf(d)} step={d.timeseries.granularity === "hour" ? 3_600_000 : 86_400_000} metric={metric} markers={markers} now={rt.data ? new Date(rt.data.at).getTime() : undefined} />
            )}
          </section>

          <div className="mt-14 grid gap-x-12 gap-y-12 lg:grid-cols-2">
            <Breakdown
              title="Pages"
              total={d.totals.visitors}
              tabs={[
                ["Top pages", d.pages],
                ["Where visits start", d.entryPages],
              ]}
              mono
            />
            <Breakdown
              title="Sources"
              total={d.totals.visitors}
              tabs={[
                ["Referrers", d.sources],
                ["UTM source", d.utmSources],
                ["UTM campaign", d.utmCampaigns],
              ]}
              empty="Nothing yet: visits came directly, or from sites that don’t say where from."
              fallback="Direct"
            />
            <Breakdown title="Countries" total={d.totals.visitors} tabs={[["Countries", (d.countries ?? []).map((c) => ({ ...c, value: country(c.value) }))]]} />
            <Breakdown
              title="Devices"
              total={d.totals.visitors}
              tabs={[
                ["Devices", (d.devices ?? []).map((c) => ({ ...c, value: c.value.charAt(0).toUpperCase() + c.value.slice(1) }))],
                ["Browsers", d.browsers],
                ["Systems", (d.os ?? []).map((c) => ({ ...c, value: c.value === "Mac OS X" ? "macOS" : c.value }))],
              ]}
            />
          </div>

          <div className="mt-14 grid gap-x-12 gap-y-12 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
            <Events data={ev.data?.events ?? []} visitors={d.totals.visitors} />
            <Realtime project={project} />
          </div>

          {vitals.data && <Speed v={vitals.data} code={setup.data?.vitals} />}
        </div>
      )}

      {setup.data && (
        <section className="mt-16 grid gap-x-12 gap-y-8 border-t border-rule pt-10 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]" aria-labelledby="setup">
          <div className="min-w-0">
            <h2 id="setup" className="label">
              Single-page apps and custom events
            </h2>
            <p className="mt-2 mb-4 text-[0.875rem] text-ink-2">Page views need nothing. Add the 1.3 KB script for client-side navigation, outbound links and events.</p>
            <CodeBox code={setup.data.snippet} name="index.html" />
            <CodeBox className="mt-3" code={`// in the browser\n${setup.data.browser}\n\n// on the server\n${setup.data.track.replace("; ", ";\n")}`} name="track.ts" />
          </div>
          <div>
            <h3 className="label">What’s stored, and what isn’t</h3>
            <p className="mt-2 text-[0.875rem] text-ink-2">{setup.data.privacy}</p>
            <p className="mt-4 text-[0.8125rem] text-ink-3">
              Countries come from{" "}
              <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="text-brass-ink hover:underline hover:underline-offset-4">
                IP Geolocation by DB-IP
              </a>{" "}
              (CC BY 4.0). Days are UTC.
            </p>
          </div>
        </section>
      )}
      {!setup.data && (
        <p className="mt-10 text-xs text-ink-3">
          <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="hover:text-ink">
            IP Geolocation by DB-IP
          </a>
        </p>
      )}
    </Page>
  );
}

/** The period's state in one sentence, honest about the baseline. */
function sentenceFor(d: AnalyticsOverview, per: (typeof periods)[number], partialSince?: number): string {
  const v = d.totals.visitors;
  if (!v) return `No visits ${per.words}.`;
  let s = `${int(v)} ${v === 1 ? "visitor" : "visitors"} ${per.words}`;
  if (partialSince !== undefined && d.previous.visitors > 0) return `${s}. Counting started ${dayFmt.format(new Date(partialSince))}, so there’s no full ${per.before.replace(/^the /, "")} to compare with yet.`;
  const pts = d.timeseries.points ?? [];
  const first = pts.findIndex((x) => x.pageviews > 0);
  if (d.previous.visitors === 0 && first > 0 && d.timeseries.granularity === "day") s += `, all since ${dayFmt.format(new Date(pts[first].t))}`;
  if (d.previous.visitors >= MIN_BASELINE) {
    const r = (v - d.previous.visitors) / d.previous.visitors;
    if (Math.abs(r) < 0.01) s += `, about the same as ${per.before}`;
    else if (r >= 2) s += `, ${dec(v / d.previous.visitors, 1)} times ${per.before}`;
    else s += `, ${pct(Math.abs(r))} ${r > 0 ? "more" : "fewer"} than ${per.before}`;
    return `${s}.`;
  }
  return d.previous.visitors === 0 ? `${s}. Nothing to compare with yet.` : `${s}. Only ${int(d.previous.visitors)} ${per.before}: too few to compare.`;
}

/** Every bucket from the period's start to its end, filled from the series (missing ones are zero). */
function bucketsOf(d: AnalyticsOverview): Bucket[] {
  const step = d.timeseries.granularity === "hour" ? 3_600_000 : 86_400_000;
  const pts = d.timeseries.points ?? [];
  const byT = new Map(pts.map((x) => [new Date(x.t).getTime(), x]));
  const start = pts.length ? Math.min(new Date(d.from).getTime(), new Date(pts[0].t).getTime()) : new Date(d.from).getTime();
  const alignedStart = pts.length ? new Date(pts[0].t).getTime() - Math.ceil((new Date(pts[0].t).getTime() - start) / step) * step : start;
  const end = new Date(d.to).getTime();
  const out: Bucket[] = [];
  for (let t = alignedStart; t < end && out.length < 400; t += step) {
    const x = byT.get(t);
    out.push({ t, visitors: x?.visitors ?? 0, pageviews: x?.pageviews ?? 0 });
  }
  return out;
}

/** When each app went live, for the chart's markers. */
function useDeployMarkers(project: string): Marker[] {
  const m = useQuery({ ...api.manifest(project), retry: false, staleTime: 60_000 });
  const apps = Object.keys(m.data?.manifest.apps ?? {});
  const res = useQueries({ queries: apps.map((a) => ({ queryKey: ["deploys", project, a], queryFn: () => mod3.deploys(project, a), staleTime: 60_000, retry: false })) });
  const all = res.flatMap((r) => r.data ?? []);
  const key = all.map((x) => x.id).join(",");
  return useMemo(
    () =>
      all
        .filter((x) => !x.preview && (x.liveAt || (x.status === "live" && x.finishedAt)))
        .map((x) => ({ t: new Date(x.liveAt ?? x.finishedAt!).getTime(), label: `${x.app} deployed` })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  );
}

function Reading({ label, value, delta, on, onClick }: { label: string; value: string; delta: string | null; on?: boolean; onClick?: () => void }) {
  const Tag = onClick ? "button" : "div";
  return (
    <Tag
      onClick={onClick}
      aria-pressed={onClick ? on : undefined}
      className={cn(
        "relative px-0 py-4 text-left sm:pr-6 [&:not(:first-child)]:max-sm:pl-0",
        onClick && "transition-colors duration-[var(--dur-state)] hover:[&_dd]:text-ink",
      )}
    >
      {on && <span aria-hidden className="absolute inset-x-0 -top-px h-[2px] bg-ink sm:right-6" />}
      <dt className={on ? "label text-ink!" : "label"}>{label}</dt>
      <dd className={cn("reading mt-1.5", on || !onClick ? "text-ink" : "text-ink-2")}>{value}</dd>
      <dd className="mt-0.5 h-4 text-[0.75rem] text-ink-3 tnum">{delta ? `${delta}${/before/.test(delta) ? "" : " vs before"}` : ""}</dd>
    </Tag>
  );
}

function Breakdown({
  title,
  tabs,
  total,
  mono,
  empty = "Nothing yet.",
  fallback = "(none)",
}: {
  title: string;
  tabs: Array<[string, AnalyticsCount[] | null]>;
  total: number;
  mono?: boolean;
  empty?: string;
  fallback?: string;
}) {
  const [i, setI] = useState(0);
  const rows = tabs[i][1] ?? [];
  const id = `bd-${title.toLowerCase()}`;
  return (
    <section aria-labelledby={id} className="min-w-0">
      <div className="mb-2 flex flex-wrap items-end justify-between gap-x-4 gap-y-1">
        {tabs.length === 1 ? (
          <h2 id={id} className="label">
            {title}
          </h2>
        ) : (
          <div role="tablist" aria-label={title} className="flex flex-wrap gap-x-3">
            <h2 id={id} className="sr-only">
              {title}
            </h2>
            {tabs.map(([label], k) => (
              <button
                key={label}
                role="tab"
                aria-selected={i === k}
                onClick={() => setI(k)}
                className={cn("label transition-colors hover:text-ink!", i === k && "text-ink!")}
              >
                {label}
              </button>
            ))}
          </div>
        )}
        <span className="text-[0.71875rem] text-ink-3">Visitors</span>
      </div>
      <ul className="divide-y divide-rule border-y border-rule-2">
        {rows.length === 0 && <li className="py-6 text-[0.84375rem] text-ink-3">{empty}</li>}
        {rows.map((r) => {
          const share = total ? r.visitors / total : 0;
          return (
            <li key={r.value} className="grid grid-cols-[minmax(0,1fr)_minmax(3rem,7rem)_3.25rem_2.75rem] items-center gap-x-3 py-2">
              <span className={cn("min-w-0 truncate text-[0.875rem] text-ink", mono && "font-mono text-[0.78rem]")} title={r.value}>
                {r.value || fallback}
              </span>
              <span aria-hidden className="h-1.5 overflow-hidden rounded-full bg-paper-sunk">
                <span className="block h-full rounded-full bg-ink-2/70" style={{ width: `${Math.max(2, share * 100)}%` }} />
              </span>
              <span className="text-right text-[0.84375rem] text-ink tnum">{int(r.visitors)}</span>
              <span className="text-right text-[0.75rem] text-ink-3 tnum">{pct(share)}</span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function Events({ data, visitors }: { data: AnalyticsEvent[]; visitors: number }) {
  return (
    <section aria-labelledby="events" className="min-w-0">
      <div className="mb-2 flex items-end justify-between gap-3">
        <h2 id="events" className="label">
          Custom events
        </h2>
        {data.length > 0 && (
          <span className="grid grid-cols-[3.25rem_3.25rem_4.5rem] gap-x-3 text-right text-[0.71875rem] text-ink-3">
            <span>Visitors</span>
            <span>Count</span>
            <span>Of visitors</span>
          </span>
        )}
      </div>
      {data.length === 0 ? (
        <p className="border-y border-rule-2 py-6 text-[0.84375rem] text-ink-3">
          None yet. Call <code className="ident text-ink-2">track("Signup")</code> from your app and they show up here.
        </p>
      ) : (
        <ul className="divide-y divide-rule border-y border-rule-2">
          {data.map((e) => (
            <li key={e.name} className="grid grid-cols-[minmax(0,1fr)_3.25rem_3.25rem_4.5rem] items-baseline gap-x-3 py-2.5">
              <span className="min-w-0">
                <span className="block text-[0.875rem] text-ink">{e.name}</span>
                {Object.entries(e.props ?? {}).map(([k, vs]) => (
                  <span key={k} className="mt-0.5 block truncate text-[0.75rem] text-ink-3">
                    <span className="font-mono">{k}</span>:{" "}
                    {(vs ?? [])
                      .slice(0, 4)
                      .map((v) => `${v.value} ${int(v.count)}`)
                      .join(" · ")}
                  </span>
                ))}
              </span>
              <span className="text-right text-[0.84375rem] text-ink-2 tnum">{int(e.visitors)}</span>
              <span className="text-right text-[0.84375rem] text-ink-2 tnum">{int(e.count)}</span>
              <span className="text-right text-[0.84375rem] text-ink tnum">{visitors ? pct(e.visitors / visitors, 1) : "–"}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Realtime({ project }: { project: string }) {
  const rt = useQuery({ queryKey: ["analytics-rt", project], queryFn: () => mod2.realtime(project), refetchInterval: 15_000 });
  const d = rt.data;
  const per = d?.perMinute ?? [];
  const max = Math.max(1, ...per.map((x) => x.pageviews));
  const any = per.some((x) => x.pageviews > 0);
  return (
    <section aria-labelledby="rt" className="min-w-0">
      <div className="mb-2 flex items-end justify-between gap-3">
        <h2 id="rt" className="label">
          Last 30 minutes
        </h2>
        {d && (
          <span className="text-[0.75rem] text-ink-3 tnum">
            {int(d.visitors30m)} {d.visitors30m === 1 ? "visitor" : "visitors"} · {int(d.pageviews30m)} {d.pageviews30m === 1 ? "view" : "views"}
          </span>
        )}
      </div>
      <div className="border-y border-rule-2 py-4">
        {!d ? (
          <Skeleton className="h-16" />
        ) : !any ? (
          <p className="text-[0.84375rem] text-ink-3">Quiet for the last half hour. Refreshes every 15 seconds.</p>
        ) : (
          <>
            <div className="flex h-16 items-end gap-[2px]" aria-label={`Page views per minute, at most ${int(max)}`} role="img">
              {per.map((x) => (
                <span key={x.t} className="flex-1 rounded-t-[2px] bg-ink-2/70" style={{ height: x.pageviews ? `${Math.max(4, (x.pageviews / max) * 100)}%` : "1px" }} title={`${int(x.pageviews)} views`} />
              ))}
            </div>
            <p className="mt-1.5 flex justify-between text-[0.6875rem] text-ink-3">
              <span>30 min ago</span>
              <span>now</span>
            </p>
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
const ratingWord: Record<string, string> = { good: "good", "needs-improvement": "could be faster", poor: "slow" };

/** Web Vitals: how fast the pages felt to three in four visitors, overall and per page. */
function Speed({ v, code }: { v: AnalyticsVitals; code?: string }) {
  const metrics = v.metrics ?? [];
  const pages = v.pages ?? [];
  const cols = ["LCP", "INP", "CLS"];
  return (
    <section className="mt-14" aria-labelledby="speed">
      <div className="mb-2 flex flex-wrap items-end justify-between gap-x-4 gap-y-1">
        <h2 id="speed" className="label">
          Page speed
        </h2>
        {metrics.length > 0 && <span className="text-[0.75rem] text-ink-3">What three in four visits were at least as fast as</span>}
      </div>
      {metrics.length === 0 ? (
        <div className="border-y border-rule-2 py-6 text-[0.84375rem] text-ink-3">
          <p>No speed readings yet. Pages send them from visitors’ browsers, with one line in a Next.js root layout:</p>
          {code && <CodeBox className="mt-3 max-w-[40rem]" code={code.replace("; ", ";\n\n// in <body>\n")} name="app/layout.tsx" />}
        </div>
      ) : (
        <>
          <dl className="grid grid-cols-2 border-y border-rule-2 sm:grid-cols-3 lg:grid-cols-5">
            {metrics.map((m) => (
              <div key={m.name} className="py-4 sm:pr-6">
                <dt className="label" title={speedNames[m.name]?.[1]}>
                  {speedNames[m.name]?.[0] ?? m.name}
                </dt>
                <dd className="reading mt-1.5 text-ink">{speedValue(m.name, m.p75)}</dd>
                <dd className="mt-0.5 text-[0.75rem] text-ink-3">
                  <span className={cn(m.rating === "poor" ? "text-danger" : m.rating === "needs-improvement" ? "text-warn-ink" : undefined)}>{ratingWord[m.rating]}</span>
                  {" · "}
                  {m.name} · {pct(m.good)} good
                </dd>
              </div>
            ))}
          </dl>
          {pages.length > 0 && (
            <ul className="mt-6 divide-y divide-rule border-y border-rule-2">
              <li className="grid grid-cols-[minmax(0,1fr)_repeat(3,4.5rem)] gap-x-3 py-1.5 text-right text-[0.71875rem] text-ink-3">
                <span className="text-left">Page</span>
                {cols.map((c) => (
                  <span key={c} title={speedNames[c][0]}>
                    {c}
                  </span>
                ))}
              </li>
              {pages.map((pg) => (
                <li key={pg.path} className="grid grid-cols-[minmax(0,1fr)_repeat(3,4.5rem)] items-baseline gap-x-3 py-2">
                  <span className="min-w-0 truncate font-mono text-[0.78rem] text-ink" title={pg.path}>
                    {pg.path}
                  </span>
                  {cols.map((c) => (
                    <span key={c} className="text-right text-[0.84375rem] text-ink-2 tnum">
                      {pg.p75[c] === undefined ? "–" : speedValue(c, pg.p75[c])}
                    </span>
                  ))}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
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
      <pre className="overflow-x-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">
        <code>{code}</code>
      </pre>
    </div>
  );
}


const DAY = 86_400_000;
/** Where the period before the shown one starts. */
function prevStart(p: Period): number {
  const now = Date.now();
  if (p === "today") {
    const d = new Date();
    d.setHours(0, 0, 0, 0);
    return d.getTime() - DAY;
  }
  const len: Record<string, number> = { yesterday: DAY, "24h": DAY, "7d": 7 * DAY, "30d": 30 * DAY, "90d": 90 * DAY, "12mo": 365 * DAY };
  return now - 2 * (len[p] ?? DAY);
}

/** When this project's first visit was counted, from a longer daily series (cached; one cheap read). */
function useFirstVisit(project: string, p: Period): number | undefined {
  const longer: Period = p === "90d" || p === "12mo" ? "12mo" : "90d";
  const q = useQuery({ queryKey: ["analytics", project, longer], queryFn: () => mod2.analytics(project, longer), staleTime: 600_000, retry: false });
  const first = (q.data?.timeseries.points ?? []).find((x) => x.pageviews > 0);
  return first ? new Date(first.t).getTime() : undefined;
}
