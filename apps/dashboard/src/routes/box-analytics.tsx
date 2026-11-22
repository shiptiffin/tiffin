import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { notOnBox } from "@/api/client";
import { mod2, type AnalyticsBox } from "@/api/modules";
import { change, DeltaBadge } from "@/components/analytics-kit";
import { DAY, HOUR } from "@/components/analytics-toolbar";
import { Sparkline } from "@/components/charts/sparkline";
import { TimeSeries, type Series } from "@/components/charts/time-series";
import { useTitle } from "@/components/favicon";
import { InfoTip } from "@/components/info-tip";
import { Empty, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { ProjectIcon } from "@/components/project-icon";
import { ProjectName, useProjectTitles } from "@/components/project-name";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { int, num } from "@/lib/format";
import type { BoxPeriod } from "@/routes/box-analytics-search";

const periods: Array<{ value: BoxPeriod; label: string; short: string; before: string }> = [
  { value: "24h", label: "24 hours", short: "24h", before: "the 24 hours before" },
  { value: "7d", label: "7 days", short: "7d", before: "the 7 days before" },
  { value: "30d", label: "30 days", short: "30d", before: "the 30 days before" },
];

type Metric = "visitors" | "pageviews";
const labels: Record<Metric, string> = { visitors: "Visitors", pageviews: "Page views" };
type Traffic = NonNullable<AnalyticsBox["projects"]>[number];

/** Project · trend · Visitors · Page views · Change · Top source. */
const COLS = "sm:grid-cols-[minmax(0,1fr)_6.5rem_5rem_5.5rem_4.5rem_minmax(0,9rem)]";

/**
 * Analytics (the box): every project's visits side by side. Two numbers and
 * a chart for the box, then one row per project, most visitors first; a row
 * opens that project's own Analytics page, which has everything else.
 */
export function BoxAnalyticsPage({ period = "7d" }: { period?: BoxPeriod }) {
  useTitle("Analytics");
  const navigate = useNavigate();
  const [metric, setMetric] = useState<Metric>("visitors");
  const o = useQuery({ queryKey: ["analytics-box", period], queryFn: () => mod2.analyticsBox(period), refetchInterval: 60_000, placeholderData: keepPreviousData, retry: 1 });
  const per = periods.find((p) => p.value === period)!;

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Analytics" />;
  const d = o.data;
  const rows = d?.projects ?? [];

  return (
    <Page wide>
      <PageHeader title="Analytics" />
      <div className="mt-5 flex flex-wrap items-center gap-2">
        <Segmented
          label="Period"
          value={period}
          options={periods.map((p) => ({ value: p.value, label: p.label, short: p.short, name: `Last ${p.label}` }))}
          onChange={(v) => void navigate({ to: "/analytics", search: v === "7d" ? {} : { period: v }, replace: true, resetScroll: false })}
        />
        <span className="ml-auto">
          <InfoTip label="About these numbers">Days are UTC. Each visitor counts once a day in each project, without cookies. This page updates every minute.</InfoTip>
        </span>
      </div>

      {o.isError && (
        <div className="mt-6 flex max-w-[46rem] flex-col items-start gap-3">
          <ProblemNote className="w-full" error={o.error} title="Couldn’t load analytics" />
          <Button size="md" onClick={() => void o.refetch()}>
            Try again
          </Button>
        </div>
      )}
      {o.isPending && (
        <div className="mt-6" aria-busy aria-label="Loading analytics">
          <Skeleton className="h-[22rem] rounded-[12px]" />
          <Skeleton className="mt-11 h-48" />
        </div>
      )}

      {d && (
        <div className={cn("transition-opacity duration-200", o.isPlaceholderData && "opacity-60")} aria-busy={o.isFetching}>
          <section aria-labelledby="box-trend" className="mt-6 rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
            <Headline d={d} metric={metric} onMetric={setMetric} before={per.before} />
            <BoxChart d={d} metric={metric} />
          </section>

          <section className="mt-11" aria-labelledby="by-project">
            <h2 id="by-project" className="text-[0.9375rem] font-[550] text-ink">
              By project
            </h2>
            <p className="mt-0.5 text-[0.8125rem] text-ink-3">Most visitors first. Open one for its pages, sources and countries.</p>
            {rows.length === 0 ? (
              <Empty className="mt-4" title="No projects yet.">
                Once a project runs on the box, its visits show here.{" "}
                <Link to="/new" className="text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                  Start one
                </Link>
                .
              </Empty>
            ) : (
              <div className="mt-3">
                <div aria-hidden className={cn("label hidden gap-x-4 border-b border-rule pb-2 text-ink-3 sm:grid", COLS)}>
                  <span>Project</span>
                  <span />
                  <span className="text-right">Visitors</span>
                  <span className="text-right">Page views</span>
                  <span className="text-right">Change</span>
                  <span>Top source</span>
                </div>
                <ul aria-label="Each project’s visits" className="border-rule max-sm:border-t">
                  {rows.map((r) => (
                    <ProjectRow key={r.project} r={r} period={period} before={per.before} />
                  ))}
                </ul>
              </div>
            )}
          </section>
        </div>
      )}
    </Page>
  );
}

/** The box's two numbers as tabs, each with its change; choosing one draws it below. */
function Headline({ d, metric, onMetric, before }: { d: AnalyticsBox; metric: Metric; onMetric: (m: Metric) => void; before: string }) {
  const base = d.previous.visitors;
  const tiles: Metric[] = ["visitors", "pageviews"];
  return (
    <RadioGroup value={metric} onValueChange={(v) => onMetric(v as Metric)} aria-label="What the chart shows" className="grid grid-cols-2 border-b border-rule">
      {tiles.map((m, i) => {
        const on = metric === m;
        const delta = change(d.totals[m], d.previous[m], base);
        return (
          <RadioItem
            key={m}
            value={m}
            title={`${int(d.totals[m])} against ${int(d.previous[m])} ${before}`}
            className={cn(
              "relative flex min-w-0 flex-col justify-start px-4 pt-3.5 pb-4 text-left transition-colors duration-[var(--dur-state)] sm:px-5",
              i === 0 ? "rounded-tl-[11px]" : "rounded-tr-[11px] border-l border-rule",
              on ? "bg-paper-raised" : "bg-paper-sunk/60 hover:bg-paper-hover",
            )}
          >
            {on && <span aria-hidden className="absolute inset-x-0 -bottom-px h-[2px] bg-ink" />}
            <span className={cn("block text-[0.8125rem]", on ? "font-[550] text-ink" : "text-ink-2")}>{labels[m]}</span>
            <span className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className={cn("text-[1.625rem] leading-8 font-[500] tracking-[-0.02em] tnum", on ? "text-ink" : "text-ink-2")}>{int(d.totals[m])}</span>
              {delta && <DeltaBadge delta={delta} />}
            </span>
            <span className="sr-only">{`, against ${int(d.previous[m])} ${before}`}</span>
          </RadioItem>
        );
      })}
    </RadioGroup>
  );
}

function BoxChart({ d, metric }: { d: AnalyticsBox; metric: Metric }) {
  const hourly = d.timeseries.granularity === "hour";
  const series = useMemo<Series[]>(
    () => [{ id: "now", label: labels[metric], points: (d.timeseries.points ?? []).map((p) => [Date.parse(p.t), p[metric]]), area: true, tone: "ink" }],
    [d, metric],
  );
  const title = `${labels[metric]} per ${hourly ? "hour" : "day"}, every project`;
  return (
    <div className="px-4 pt-4 pb-4 sm:px-5">
      <h2 id="box-trend" className="sr-only">
        {title}
      </h2>
      <TimeSeries series={series} step={hourly ? HOUR : DAY} utc format={int} axisFormat={num} label={title} height={240} />
    </div>
  );
}

function ProjectRow({ r, period, before }: { r: Traffic; period: BoxPeriod; before: string }) {
  const delta = change(r.visitors, r.previous.visitors, r.previous.visitors);
  const quiet = r.visitors === 0;
  const title = useProjectTitles()(r.project);
  return (
    <li className="border-b border-rule">
      <Link
        to="/projects/$project/analytics"
        params={{ project: r.project }}
        search={period === "7d" ? {} : { period }}
        title={`${int(r.visitors)} visitors against ${int(r.previous.visitors)} ${before}`}
        className={cn(
          "-mx-2 grid w-[calc(100%+1rem)] grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 rounded-[8px] px-2 py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover focus-visible:outline-offset-[-2px]",
          COLS,
        )}
      >
        <span className="flex min-w-0 items-center gap-2.5">
          <ProjectIcon project={r.project} size={20} />
          <span className="min-w-0">
            <ProjectName name={r.project} title={title} className="text-[0.875rem] font-[550] text-ink" />
            {/* A phone has no columns: page views and the source go under the name. */}
            <span className="block truncate text-xs text-ink-3 sm:hidden">
              {int(r.pageviews)} page views{r.topSource ? ` · ${r.topSource}` : ""}
            </span>
          </span>
        </span>
        <Sparkline values={r.series ?? []} height={24} className={cn("max-sm:hidden", quiet && "opacity-40")} />
        <span className={cn("text-right text-[0.875rem] tnum", quiet ? "text-ink-3" : "text-ink")}>
          {int(r.visitors)}
          <span className="sr-only"> visitors</span>
        </span>
        <span className={cn("text-right text-[0.875rem] tnum max-sm:hidden", quiet ? "text-ink-3" : "text-ink-2")}>
          {int(r.pageviews)}
          <span className="sr-only"> page views</span>
        </span>
        <span className="flex justify-end max-sm:hidden">{delta ? <DeltaBadge delta={delta} /> : <span className="text-[0.8125rem] text-ink-4">–</span>}</span>
        <span className="truncate text-[0.8125rem] text-ink-2 max-sm:hidden">{r.topSource || <span className="text-ink-4">–</span>}</span>
      </Link>
    </li>
  );
}
