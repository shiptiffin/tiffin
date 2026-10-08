import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod, mq, type AppMetrics, type Issue } from "@/api/modules";
import { q } from "@/api/queries";
import { Sparkline } from "@/components/charts/sparkline";
import { useTitle } from "@/components/favicon";
import { Calm, Group, Segmented, StateLine } from "@/components/health-kit";
import { historyQuery, useWindowTotals, type WindowTotals } from "@/components/observe-data";
import { IssueRows, TraceRows } from "@/components/observe-lists";
import { TopPaths } from "@/components/observe-paths";
import type { ObserveSearch, ObserveTab } from "@/components/observe-search";
import { Crumbs, Empty, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Segmented as RangePicker } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { ranges, UsageCharts, ViewToggle, type Range } from "@/components/usage-charts";
import { cn } from "@/lib/cn";
import { countWords, int, ms, num, pct } from "@/lib/format";
import { useProjectPulse } from "@/lib/pulse";
import { rememberProject } from "@/lib/recent";
import { relative } from "@/lib/time";
import { memWords, usageQuery } from "@/lib/usage";
import { ResourcesView } from "@/routes/project-usage";

const tabs: Array<{ v: ObserveTab; label: string }> = [
  { v: "overview", label: "Overview" },
  { v: "resources", label: "Resources" },
  { v: "errors", label: "Errors" },
  { v: "requests", label: "Requests" },
];

/**
 * Observability for one project, as a hosting dashboard has it: its traffic
 * at a glance (Overview: totals, requests by status, response time, routes,
 * each app), what it uses of the box and its limit (Resources), what went
 * wrong (Errors) and its traced requests (Requests). One time range and app
 * filter serve the charts on both of the first two; every chart shares a
 * crosshair and marks deploys. The tab is in the address (?tab=errors).
 */
export function ObservabilityPage({ project, search }: { project: string; search: ObserveSearch }) {
  useTitle(`${project} · Observability`);
  useEffect(() => rememberProject(project), [project]);
  const navigate = useNavigate();
  const tab: ObserveTab = search.tab ?? "overview";
  const range: Range = search.range ?? "24h";
  const app = search.app;
  const to = (next: ObserveSearch) => void navigate({ to: "/projects/$project/observability", params: { project }, search: next, replace: true });
  const go = (t: ObserveTab) => void navigate({ to: "/projects/$project/observability", params: { project }, search: { ...search, tab: t === "overview" ? undefined : t } });
  const setRange = (r: Range) => to({ ...search, range: r === "24h" ? undefined : (r as ObserveSearch["range"]) });
  const setApp = (a: string | undefined) => to({ ...search, app: a });
  const [tables, setTables] = useState(false);

  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
  const apps = Object.keys(m.data?.manifest.apps ?? {}).sort();
  // Undefined until the config is read, so an empty state doesn't flash before the apps arrive.
  const hasApps = m.data ? apps.length > 0 : undefined;
  const open = useQuery({ ...mq.issues(project, "unresolved"), retry: false });
  const openCount = open.data?.length ?? 0;
  const charted = tab === "overview" || tab === "resources";
  const one = app && apps.includes(app) ? app : undefined;

  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Observability" }]} />} title="Observability" />
      <nav className="mt-6 -mb-px flex gap-1 overflow-x-auto border-b border-rule [scrollbar-width:none]" aria-label="Observability">
        {tabs.map((t) => (
          <button
            key={t.v}
            type="button"
            onClick={() => go(t.v)}
            aria-current={tab === t.v ? "page" : undefined}
            className={cn(
              "relative flex h-10 shrink-0 items-center gap-2 px-3 text-[0.875rem] transition-colors first:pl-0 first:after:left-0 hover:text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full",
              tab === t.v ? "font-[550] text-ink after:bg-ink" : "text-ink-3 after:bg-transparent",
            )}
          >
            {t.label}
            {t.v === "errors" && openCount > 0 && <span className="text-xs font-normal text-ink-3 tnum">{openCount}</span>}
          </button>
        ))}
      </nav>

      {charted && hasApps !== false && (
        <div className="mt-5 flex flex-wrap items-center gap-2">
          {apps.length > 1 && (
            <Select
              size="sm"
              aria-label="App"
              className="w-32 sm:w-40"
              value={one ?? "__all"}
              onValueChange={(v) => setApp(v === "__all" ? undefined : v)}
              options={[{ value: "__all", label: "All apps" }, ...apps.map((a) => ({ value: a, label: <span className="font-mono text-[0.78rem]">{a}</span> }))]}
            />
          )}
          <RangePicker label="Time range" value={range} options={ranges} onChange={setRange} />
          <span className="ml-auto">
            <ViewToggle tables={tables} onToggle={() => setTables((x) => !x)} />
          </span>
        </div>
      )}

      {tab === "overview" && <Overview project={project} apps={apps} app={one} hasApps={hasApps} range={range} tables={tables} onTab={go} />}
      {tab === "resources" && <ResourcesView project={project} range={range} app={one} tables={tables} />}
      {tab === "errors" && <Errors project={project} hasApps={hasApps} />}
      {tab === "requests" && <Requests project={project} hasApps={hasApps} />}
    </Page>
  );
}

/** No app yet: nothing can have traffic, errors or traces. Says what to do. */
function NoApps({ project, what }: { project: string; what: string }) {
  return (
    <Empty title={`Deploy an app to see ${what} here.`} className="mt-8 max-w-[46rem]">
      <p>{project} has no app yet. Once one is live, the box measures every request at its edge: no code changes needed.</p>
      <Button asChild size="md" className="mt-4">
        <Link to="/projects/$project/apps" params={{ project }}>
          Go to Apps
        </Link>
      </Button>
    </Empty>
  );
}

const lastWords = (r: Range) => (r === "1h" ? "the last hour" : `the last ${ranges.find((x) => x.value === r)!.label}`);
const MINUTES: Record<Range, number> = { "1h": 60, "24h": 1440, "7d": 10080, "30d": 43200 };

// ------------------------------------------------------------------ overview

/** Totals, requests by status and response time over time, routes, each app, and what needs a look. */
function Overview({ project, apps, app, hasApps, range, tables, onTab }: { project: string; apps: string[]; app?: string; hasApps?: boolean; range: Range; tables: boolean; onTab: (t: ObserveTab) => void }) {
  const totals = useWindowTotals(project, range, app, !!hasApps);
  const history = useQuery({ ...historyQuery(project, range, app), enabled: !!hasApps });
  const traffic = useQuery({
    queryKey: ["observe-apps", project, range],
    queryFn: () => mod.apps(project, range),
    enabled: !!hasApps,
    refetchInterval: range === "1h" ? 30_000 : 120_000,
    placeholderData: keepPreviousData,
    retry: false,
  });
  const usage = useQuery({ ...usageQuery(project), enabled: !!hasApps });
  const issues = useQuery({ ...mq.issues(project, "unresolved"), enabled: !!hasApps, retry: false });
  const slow = useQuery({ ...mq.traces(project, "24h", false), enabled: !!hasApps, retry: false });
  const pulse = useProjectPulse(project);

  if (hasApps === undefined) return <Skeleton className="mt-8 h-40 max-w-[46rem]" />;
  if (!hasApps) return <NoApps project={project} what="requests, errors and response times" />;

  const t = totals.data;
  const down = totals.isError;
  const where = (traffic.data ?? []).filter((a) => a.errors > 0 && (!app || a.app === app)).sort((a, b) => b.errors - a.errors);
  let sentence: ReactNode;
  if (down) sentence = notOnBox(totals.error) ? "Traffic is measured on a running box." : (
        <>
          Nothing to show yet: the box’s metrics store isn’t answering. This page fills in by itself once it is;{" "}
          <Link to="/status" className="underline decoration-rule-3 underline-offset-4 hover:decoration-ink-3">
            Health
          </Link>{" "}
          shows what’s wrong.
        </>
      );
  else if (!t) sentence = null;
  else if (t.requests === 0)
    sentence = (
      <>
        No requests in {lastWords(range)}.
        {pulse.url && (
          <>
            {" "}
            Open{" "}
            <a href={pulse.url} target="_blank" rel="noreferrer" className="underline decoration-rule-3 underline-offset-4 hover:decoration-ink-3">
              {pulse.url.replace(/^https?:\/\//, "")}
            </a>{" "}
            to send the first.
          </>
        )}
      </>
    );
  else if (t.failed === 0) sentence = `${int(t.requests)} requests in ${lastWords(range)}, and not one server error.`;
  else
    sentence = (
      <>
        <span className={t.failed / t.requests >= 0.01 ? "text-danger" : undefined}>
          {int(t.failed)} of {int(t.requests)} requests failed
        </span>{" "}
        in {lastWords(range)}
        {where.length === 1 && !app ? `, all in ${where[0].app}.` : where.length > 1 && !app ? `, most in ${where[0].app}.` : "."}
      </>
    );

  const issueList = [...(issues.data ?? [])].sort((a, b) => Date.parse(b.lastSeen) - Date.parse(a.lastSeen)).slice(0, 3);
  const slowList = (slow.data ?? []).filter((x) => !app || x.app === app).slice(0, 3);
  const series = history.data?.series ?? {};

  return (
    <>
      <div className="mt-3">{totals.isPending ? <Skeleton className="mt-4 h-7 w-96 max-w-full" /> : <StateLine>{sentence}</StateLine>}</div>

      <Vitals totals={t} pending={totals.isPending} down={down} range={range} series={series} />

      <UsageCharts project={project} apps={apps} app={app} usage={usage.data} only="traffic" title="Traffic" range={range} tables={tables} className="mt-12" quiet={down} />

      <TopPaths project={project} range={range} app={app} enabled={!down} />

      {apps.length > 1 && !app && <ByApp apps={apps} rows={traffic.data ?? []} pending={traffic.isPending} down={traffic.isError} usage={usage.data?.apps ?? []} range={range} />}

      <div className="mt-12 grid gap-x-12 gap-y-10 lg:grid-cols-2">
        <Group
          flush
          label="Open errors"
          aside={
            <button type="button" onClick={() => onTab("errors")} className="text-ink-2 underline-offset-4 hover:text-ink hover:underline">
              All errors
            </button>
          }
        >
          {issues.isPending ? (
            <Skeleton className="h-24" />
          ) : issues.isError ? (
            <Quiet>{notOnBox(issues.error) ? "Errors are collected on a running box." : "Errors can’t be read right now."}</Quiet>
          ) : issueList.length === 0 ? (
            <Quiet>None. When an app throws, the error shows here, grouped with others like it.</Quiet>
          ) : (
            <IssueRows issues={issueList} compact />
          )}
        </Group>
        <Group
          flush
          label="Slowest requests, last 24 hours"
          aside={
            <button type="button" onClick={() => onTab("requests")} className="text-ink-2 underline-offset-4 hover:text-ink hover:underline">
              All requests
            </button>
          }
        >
          {slow.isPending ? (
            <Skeleton className="h-24" />
          ) : slow.isError ? (
            <Quiet>{notOnBox(slow.error) ? "Requests are traced on a running box." : "Requests can’t be read right now."}</Quiet>
          ) : slowList.length === 0 ? (
            <Quiet>Nothing traced yet. Requests an app traces (OpenTelemetry) show here step by step.</Quiet>
          ) : (
            <TraceRows traces={slowList} compact />
          )}
        </Group>
      </div>
    </>
  );
}

function Quiet({ children }: { children: ReactNode }) {
  return <p className="rounded-[10px] border border-dashed border-rule-2 px-5 py-6 text-center text-[0.875rem] text-ink-3">{children}</p>;
}

const STATUS_TONE: Record<string, string> = { "2xx": "bg-[var(--part-3)]", "3xx": "bg-[var(--part-4)]", "4xx": "bg-warn", "5xx": "bg-danger" };

/**
 * The window in four numbers, hairlines between them (a table, not cards),
 * each with its trend; then every answer by status class on one bar.
 */
function Vitals({ totals: t, pending, down, range, series }: { totals?: WindowTotals; pending: boolean; down: boolean; range: Range; series: Record<string, (number[] | null)[] | null> }) {
  const vals = (k: string) => (series[k] ?? []).filter((p): p is number[] => !!p && p.length === 2).map((p) => p[1]);
  const rate = t && t.requests ? t.failed / t.requests : 0;
  const dash = <span className="text-ink-4">–</span>;
  const cells: Array<{ label: string; value: ReactNode; sub: ReactNode; spark: number[]; fill?: boolean; tone?: string }> = [
    { label: "Requests", value: t ? int(t.requests) : dash, sub: t ? `${num(Math.round((t.requests / MINUTES[range]) * 10) / 10)} a minute` : " ", spark: vals("requests"), fill: true },
    {
      label: "Failed",
      value: t ? (t.failed ? pct(rate, rate < 0.001 ? 2 : 1) : "none") : dash,
      sub: t ? (t.failed ? `${int(t.failed)} answered with a 5xx` : "no 5xx answers") : " ",
      spark: vals("errors"),
      tone: rate >= 0.01 ? "text-danger" : undefined,
    },
    { label: "Half within", value: t?.p50 !== undefined ? ms(t.p50) : dash, sub: t?.p50 !== undefined ? "the median response" : " ", spark: vals("p50") },
    { label: "95% within", value: t?.p95 !== undefined ? ms(t.p95) : dash, sub: t?.p95 !== undefined ? "the slowest 5% take longer" : " ", spark: vals("p95"), tone: (t?.p95 ?? 0) >= 1000 ? "text-warn-ink" : undefined },
  ];
  const codes = Object.entries(t?.byCode ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => a[0].localeCompare(b[0]));
  return (
    <section aria-label={`The ${ranges.find((r) => r.value === range)!.label} in numbers`} className="mt-6">
      <div className="grid grid-cols-2 gap-px overflow-hidden border-y border-rule bg-rule sm:grid-cols-4">
        {cells.map((c) => (
          <div key={c.label} className="min-w-0 bg-paper py-4 pr-4 pl-0 sm:px-5 sm:first:pl-0 max-sm:even:pl-4">
            <p className="text-[0.8125rem] text-ink-3">{c.label}</p>
            <div className={cn("mt-1 text-[1.625rem] leading-8 font-[500] tracking-[-0.02em] tnum", c.tone ?? "text-ink")}>{pending ? <Skeleton className="mt-1 h-7 w-20" /> : c.value}</div>
            <p className="mt-0.5 truncate text-xs text-ink-3">{pending ? " " : down ? "no readings yet" : c.sub}</p>
            <div className="mt-3 h-7">{!down && c.spark.length > 1 && <Sparkline values={c.spark} fill={c.fill} height={28} />}</div>
          </div>
        ))}
      </div>
      {codes.length > 0 && t && t.requests > 0 && (
        <details className="group mt-4">
          <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
            <span className="inline-block transition-transform group-open:rotate-90">›</span> Requests by status code
          </summary>
          <div className="mt-3 flex h-1.5 gap-0.5 overflow-hidden rounded-full" role="img" aria-label={`Answers by status: ${codes.map(([c, n]) => `${c} ${int(n)}`).join(", ")}`}>
            {codes.map(([c, n]) => (
              <span key={c} className={cn("h-full min-w-[3px] first:rounded-l-full last:rounded-r-full", STATUS_TONE[c] ?? "bg-ink-4")} style={{ width: `${(n / t.requests) * 100}%` }} />
            ))}
          </div>
          <p className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
            {codes.map(([c, n]) => (
              <span key={c} className="inline-flex items-center gap-1.5">
                <i aria-hidden className={cn("inline-block size-2 rounded-[2px]", STATUS_TONE[c] ?? "bg-ink-4")} />
                <span className="text-ink-2">{c}</span>
                <span className="tnum">{int(n)}</span>
                <span className="tnum">({pct(n / t.requests, n / t.requests < 0.01 ? 1 : 0)})</span>
              </span>
            ))}
          </p>
        </details>
      )}
    </section>
  );
}

type UsageApp = { app: string; cpuPercent: number; instances: number; memoryBytes: number; state: string; preview?: string };

/** Each app's traffic in the range beside what it uses now: requests, failures, p95, memory, CPU. */
function ByApp({ apps, rows, pending, down, usage, range }: { apps: string[]; rows: AppMetrics[]; pending: boolean; down: boolean; usage: UsageApp[]; range: Range }) {
  const by = new Map(rows.map((r) => [r.app, r]));
  const now = new Map<string, UsageApp>();
  for (const u of usage) if (!u.preview) now.set(u.app, u);
  const cols = "sm:grid-cols-[minmax(0,1fr)_6rem_5rem_6rem_5.5rem_4.5rem]";
  return (
    <Group label="By app" className="mt-12" aside={`Traffic in ${lastWords(range)}; memory and CPU now`}>
      <div className={cn("hidden gap-x-4 pb-1.5 text-xs text-ink-3 sm:grid", cols)} aria-hidden>
        <span />
        <span className="text-right">Requests</span>
        <span className="text-right">Failed</span>
        <span className="text-right">95% within</span>
        <span className="text-right">Memory</span>
        <span className="text-right">CPU</span>
      </div>
      <ul className="divide-y divide-rule border-y border-rule">
        {apps.map((name) => {
          const a = by.get(name);
          const u = now.get(name);
          const has = !!a && a.requests > 0;
          const fail = has ? <span className={a.errorRate >= 0.01 ? "text-danger" : undefined}>{a.errors ? pct(a.errorRate, a.errorRate < 0.001 ? 2 : 1) : "none"}</span> : "–";
          const mem = u && u.memoryBytes > 0 ? memWords(u.memoryBytes / 1048576) : "–";
          const cpu = u ? (u.cpuPercent < 1 ? "idle" : `${Math.round(u.cpuPercent)}%`) : "–";
          const asleep = u && (u.state === "asleep" || u.state === "stopped");
          return (
            <li key={name} className={cn("grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 py-2.5 text-[0.875rem]", cols)}>
              <span className="min-w-0">
                <span className="flex items-baseline gap-2">
                  <span className="truncate font-mono text-[0.84375rem] text-ink">{name}</span>
                  {u && u.instances > 1 && <span className="text-xs text-ink-3">{countWords(u.instances, "copy", "copies")}</span>}
                  {asleep && <span className="text-xs text-ink-3">{u.state}</span>}
                </span>
                <span className="block text-xs text-ink-3 sm:hidden">
                  {has ? (
                    <>
                      {fail} failed · 95% within {ms(a.p95ms)} ·{" "}
                    </>
                  ) : null}
                  {mem} · CPU {cpu}
                </span>
              </span>
              <span className="text-right text-ink tnum">{pending && !down ? <Skeleton className="ml-auto h-4 w-10" /> : has ? int(Math.round(a.requests)) : "–"}</span>
              <span className="hidden text-right text-ink-2 tnum sm:block">{fail}</span>
              <span className={cn("hidden text-right tnum sm:block", has && a.p95ms >= 1000 ? "text-warn-ink" : "text-ink-2")}>{has ? ms(a.p95ms) : "–"}</span>
              <span className="hidden text-right text-ink-2 tnum sm:block">{mem}</span>
              <span className="hidden text-right text-ink-2 tnum sm:block">{cpu}</span>
            </li>
          );
        })}
      </ul>
    </Group>
  );
}

// ------------------------------------------------------------------ errors

const states = [
  { v: "unresolved", label: "Open" },
  { v: "resolved", label: "Resolved" },
  { v: "ignored", label: "Ignored" },
] as const;

/** The project's error issues: open first; resolved and ignored a click away. Each opens its issue page. */
function Errors({ project, hasApps }: { project: string; hasApps?: boolean }) {
  const [st, setSt] = useState<Issue["status"]>("unresolved");
  const list = useQuery({ ...mq.issues(project, st), retry: false });
  const open = useQuery({ ...mq.issues(project, "unresolved"), retry: false });
  if (hasApps === false && !list.data?.length) return <NoApps project={project} what="its errors" />;
  const issues = list.data ?? [];
  const o = open.data ?? [];
  const worst = [...o].sort((a, b) => b.count - a.count)[0];
  const appsWith = new Set(o.map((i) => i.app));
  const line =
    o.length === 0
      ? "Nothing is going wrong."
      : `${countWords(o.length, "open issue", "open issues", true)}${appsWith.size === 1 ? `, ${o.length > 1 ? "all " : ""}in ${[...appsWith][0]}` : ` across ${countWords(appsWith.size, "app")}`}. ${
          o.length > 1 ? "The worst has" : "It has"
        } happened ${countWords(worst.count, "time")}, last seen ${relative(worst.lastSeen)}.`;
  // With nothing open, the empty state below carries the setup itself.
  const calm = list.isSuccess && issues.length === 0 && st === "unresolved";

  return (
    <>
      <div className="mt-4">{open.isSuccess && <StateLine>{line}</StateLine>}</div>
      {!calm && (
        <>
          <p className="mt-2 max-w-[42rem] text-[0.875rem] text-ink-3">Errors are grouped by where they happen.</p>
          <SetUp>
            Any Sentry SDK works: every app already has <code className="ident text-ink-2">SENTRY_DSN</code>.
          </SetUp>
        </>
      )}
      <div className="mt-6 flex flex-wrap items-center justify-between gap-3">
        <Segmented label="Which issues" value={st} onChange={setSt} options={states.map((s) => ({ v: s.v, label: s.label }))} />
        <Link to="/errors" search={{ project }} className="text-[0.8125rem] text-ink-3 underline-offset-4 hover:text-ink hover:underline">
          Every project’s errors
        </Link>
      </div>
      <div className="mt-4">
        {list.isPending && <Skeleton className="h-40" />}
        {list.isError &&
          (notOnBox(list.error) ? (
            <p className="border-y border-rule py-8 text-center text-[0.875rem] text-ink-3">Errors are collected on a running box.</p>
          ) : (
            <ProblemNote error={list.error} />
          ))}
        {list.isSuccess &&
          issues.length === 0 &&
          (st === "unresolved" ? (
            <Calm art="errors" title={`No open errors in ${project}.`}>
              When an app throws, the error lands here, grouped with others like it. Any Sentry SDK works: every app already has{" "}
              <code className="ident text-ink-2">SENTRY_DSN</code>.
            </Calm>
          ) : (
            <p className="border-y border-rule py-8 text-center text-[0.875rem] text-ink-3">{st === "resolved" ? "Nothing resolved yet." : "Nothing ignored."}</p>
          ))}
        {issues.length > 0 && <IssueRows issues={issues} resolved={st === "resolved"} />}
      </div>
    </>
  );
}

// ------------------------------------------------------------------ requests

const windows = [
  { v: "1h", label: "Hour" },
  { v: "24h", label: "Day" },
  { v: "3d", label: "3 days" },
] as const;
type Window = (typeof windows)[number]["v"];
type Which = "slowest" | "recent" | "failed";

/** The project's traced requests: the slowest, the latest or the failed ones. Each opens its steps. */
function Requests({ project, hasApps }: { project: string; hasApps?: boolean }) {
  const [since, setSince] = useState<Window>("24h");
  const [which, setWhich] = useState<Which>("slowest");
  const list = useQuery(
    which === "recent"
      ? { queryKey: ["traces", project, since, "recent"], queryFn: () => mod.traces(project, { since, sort: "recent" }), refetchInterval: 30_000, retry: false }
      : { ...mq.traces(project, since, which === "failed"), retry: false },
  );
  if (hasApps === false && !list.data?.length) return <NoApps project={project} what="requests" />;
  const traces = list.data ?? [];
  const slow = traces.filter((t) => t.kept === "slow").length;
  const failed = traces.filter((t) => t.error).length;
  const slowest = traces.reduce((m, t) => Math.max(m, t.durationMs), 0);
  const line =
    traces.length === 0
      ? "No traced requests in this window."
      : `${countWords(traces.length, "request", "requests", true)} kept${slow || failed ? `: ${[failed && countWords(failed, "failed", "failed"), slow && countWords(slow, "slow", "slow")].filter(Boolean).join(", ")}` : ""}. The slowest took ${ms(slowest)}.`;

  return (
    <>
      <div className="mt-4">{list.isSuccess && <StateLine>{line}</StateLine>}</div>
      {!(list.isSuccess && traces.length === 0) && (
        <>
          <p className="mt-2 max-w-[42rem] text-[0.875rem] text-ink-3">Every failed or slow request is kept for three days, and one in ten of the rest.</p>
          <SetUp>{otelHow}</SetUp>
        </>
      )}
      <div className="mt-6 flex flex-wrap items-center gap-3">
        <Segmented label="How far back" value={since} onChange={setSince} options={windows.map((w) => ({ v: w.v, label: w.label }))} />
        <Segmented
          label="Which requests"
          value={which}
          onChange={setWhich}
          options={[
            { v: "slowest", label: "Slowest" },
            { v: "recent", label: "Latest" },
            { v: "failed", label: "Failed" },
          ]}
        />
      </div>
      <div className="mt-4">
        {list.isPending && <Skeleton className="h-40" />}
        {list.isError &&
          (notOnBox(list.error) ? (
            <p className="border-y border-rule py-8 text-center text-[0.875rem] text-ink-3">Requests are traced on a running box.</p>
          ) : (
            <ProblemNote error={list.error} />
          ))}
        {list.isSuccess && traces.length === 0 && (
          <div className="border-y border-rule py-8 text-center text-[0.875rem] text-ink-3">
            <p>{which === "failed" ? "No failed requests in this window." : "Nothing traced in this window."}</p>
            <p className="mx-auto mt-1 max-w-[36rem]">
              The box keeps every request that failed or took a second or more, and one in ten of the rest, for three days. {otelHow}
            </p>
          </div>
        )}
        {traces.length > 0 && <TraceRows traces={traces} />}
      </div>
    </>
  );
}

const otelHow = (
  <>
    Next.js needs an <code className="ident text-ink-2">instrumentation.ts</code> with <code className="ident text-ink-2">registerOTel()</code>; the address and key are already set.
  </>
);

/** Setup steps, closed: people who already report errors or traces don't need them on every visit. */
function SetUp({ children }: { children: ReactNode }) {
  return (
    <details className="group mt-1.5 max-w-[42rem]">
      <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="inline-block transition-transform group-open:rotate-90">›</span> How to set it up
      </summary>
      <p className="mt-2 text-[0.875rem] text-ink-2">{children}</p>
    </details>
  );
}
