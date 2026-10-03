import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowDownRight, ArrowUpRight, BarChart3 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mod2, type AnalyticsCount, type AnalyticsEvent, type Period } from "@/api/modules";
import { AreaChart, type Point } from "@/components/chart";
import { CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { Empty, Page, PageHeader, Skeleton, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { cn } from "@/lib/cn";
import { num } from "@/lib/format";

const periods: Array<{ v: Period; label: string }> = [
  { v: "today", label: "Today" },
  { v: "24h", label: "24 hours" },
  { v: "7d", label: "7 days" },
  { v: "30d", label: "30 days" },
  { v: "90d", label: "90 days" },
  { v: "12mo", label: "12 months" },
];

type Metric = "visitors" | "pageviews";

const regions = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames(undefined, { type: "region" }) : null;
const country = (code: string) => {
  try {
    return (code && regions?.of(code)) || code || "Unknown";
  } catch {
    return code;
  }
};

function secs(s: number) {
  if (!s) return "0 s";
  const m = Math.floor(s / 60);
  return m ? `${m} min ${Math.round(s % 60)} s` : `${Math.round(s)} s`;
}

export function AnalyticsPage({ project, period = "7d" }: { project: string; period?: string }) {
  useTitle(`${project} · Analytics`);
  const navigate = useNavigate();
  const p = (periods.some((x) => x.v === period) ? period : "7d") as Period;
  const [metric, setMetric] = useState<Metric>("visitors");
  const o = useQuery({
    queryKey: ["analytics", project, p],
    queryFn: () => mod2.analytics(project, p),
    refetchInterval: 60_000,
    placeholderData: (d) => d,
  });
  const rt = useQuery({ queryKey: ["analytics-rt", project], queryFn: () => mod2.realtime(project), refetchInterval: 15_000 });
  const ev = useQuery({ queryKey: ["analytics-ev", project, p], queryFn: () => mod2.events(project, p) });
  const setup = useQuery({ queryKey: ["analytics-setup", project], queryFn: () => mod2.analyticsSetup(project), staleTime: Infinity });

  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Analytics" />;
  const d = o.data;
  const now = rt.data?.visitorsNow ?? 0;

  return (
    <Page full>
      <PageHeader
        eyebrow={
          <Link to="/projects/$project" params={{ project }} className="font-mono hover:text-ink">
            {project}
          </Link>
        }
        title="Analytics"
        lede="Counted at the box's own edge: no cookies, nothing sent anywhere else, and ad blockers can't hide a page view."
        actions={
          <span
            className={cn(
              "inline-flex h-9 items-center gap-2 rounded-full border px-3.5 text-sm",
              now ? "border-rev/40 bg-rev-wash text-ink" : "border-rule text-ink-3",
            )}
          >
            <span className={cn("size-2 rounded-full", now ? "animate-pulse bg-rev" : "bg-ink-4")} />
            {num(now)} {now === 1 ? "visitor" : "visitors"} now
          </span>
        }
      />

      <div className="mt-8 flex flex-wrap gap-1 rounded-lg border border-rule bg-paper-sunk p-1 sm:inline-flex" role="radiogroup" aria-label="Period">
        {periods.map((x) => (
          <button
            key={x.v}
            role="radio"
            aria-checked={p === x.v}
            onClick={() => navigate({ to: "/projects/$project/analytics", params: { project }, search: x.v === "7d" ? {} : { period: x.v } })}
            className={cn(
              "h-8 rounded-md px-3 text-sm text-ink-3 transition-colors hover:text-ink",
              p === x.v && "bg-raised text-ink shadow-[0_1px_2px_oklch(0_0_0/0.12)] ring-1 ring-rule",
            )}
          >
            {x.label}
          </button>
        ))}
      </div>

      {o.isError && <ProblemNote className="mt-6" error={o.error} />}
      {o.isPending && <Skeleton className="mt-6 h-72" />}
      {d && (
        <>
          <section className="mt-6 overflow-hidden rounded-xl border border-rule bg-raised/60">
            <dl className="grid grid-cols-2 max-sm:[&>*:last-child:nth-child(odd)]:col-span-2 border-b border-rule sm:grid-cols-5">
              <Kpi
                label="Visitors"
                v={d.totals.visitors}
                prev={d.previous.visitors}
                fmt={num}
                on={metric === "visitors"}
                onClick={() => setMetric("visitors")}
              />
              <Kpi
                label="Page views"
                v={d.totals.pageviews}
                prev={d.previous.pageviews}
                fmt={num}
                on={metric === "pageviews"}
                onClick={() => setMetric("pageviews")}
              />
              <Kpi label="Views per visit" v={d.totals.viewsPerVisit} prev={d.previous.viewsPerVisit} fmt={(v) => v.toFixed(1)} />
              <Kpi label="Bounce rate" v={d.totals.bounceRate} prev={d.previous.bounceRate} fmt={(v) => `${Math.round(v * 100)}%`} lowerIsBetter />
              <Kpi label="Visit duration" v={d.totals.avgSessionSeconds} prev={d.previous.avgSessionSeconds} fmt={secs} />
            </dl>
            <div className="p-5">
              {d.totals.pageviews === 0 ? (
                <Empty icon={<BarChart3 />} title="No visits in this period" className="border-0">
                  Page views show up here as soon as people open your apps.
                </Empty>
              ) : (
                <AreaChart
                  label={metric === "visitors" ? "Visitors" : "Page views"}
                  points={(d.timeseries.points ?? []).map((x) => [new Date(x.t).getTime() / 1000, x[metric]] as Point)}
                  format={(v) => num(Math.round(v))}
                  height={220}
                  tone="ink"
                />
              )}
            </div>
          </section>

          <div className="mt-6 grid gap-6 lg:grid-cols-2">
            <Breakdown
              title="Pages"
              tabs={[
                ["Top pages", d.pages],
                ["Entry pages", d.entryPages],
              ]}
              mono
            />
            <Breakdown
              title="Sources"
              tabs={[
                ["Referrers", d.sources],
                ["UTM source", d.utmSources],
                ["UTM campaign", d.utmCampaigns],
              ]}
              empty="Direct visits, or none yet"
            />
            <Breakdown title="Countries" tabs={[["Countries", (d.countries ?? []).map((c) => ({ ...c, value: country(c.value) }))]]} />
            <Breakdown
              title="Devices"
              tabs={[
                ["Devices", d.devices],
                ["Browsers", d.browsers],
                ["Systems", d.os],
              ]}
            />
          </div>

          <div className="mt-6 grid gap-6 lg:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]">
            <Events data={ev.data?.events ?? []} visitors={d.totals.visitors} />
            <Realtime project={project} />
          </div>
        </>
      )}

      {setup.data && (
        <section className="mt-10 grid gap-6 lg:grid-cols-2" aria-labelledby="setup">
          <div>
            <h2 id="setup" className="display-italic mb-1 text-xl text-ink">
              For single-page apps and custom events
            </h2>
            <p className="mb-3 text-sm text-ink-3">
              Page views need nothing. Add the 1.3 KB script for client-side navigation, outbound links and events.
            </p>
            <CodeBox code={setup.data.snippet} name="index.html" />
            <CodeBox
              className="mt-3"
              code={`// in the browser\n${setup.data.browser}\n\n// on the server\n${setup.data.track.replace("; ", ";\n")}`}
              name="track.ts"
            />
          </div>
          <div className="rounded-xl border border-rule bg-raised/60 p-5">
            <h3 className="text-base font-medium text-ink">What's stored, and what isn't</h3>
            <p className="mt-2 text-sm text-ink-2">{setup.data.privacy}</p>
            <p className="mt-4 text-sm text-ink-3">
              Countries come from{" "}
              <a
                href="https://db-ip.com"
                target="_blank"
                rel="noopener noreferrer"
                className="text-brass-ink underline underline-offset-4 hover:text-ink"
              >
                IP Geolocation by DB-IP
              </a>{" "}
              (CC BY 4.0). Days are UTC.
            </p>
          </div>
        </section>
      )}
      <p className="mt-8 text-xs text-ink-4">
        <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="hover:text-ink">
          IP Geolocation by DB-IP
        </a>
      </p>
    </Page>
  );
}

function Kpi({
  label,
  v,
  prev,
  fmt,
  on,
  onClick,
  lowerIsBetter,
}: {
  label: string;
  v: number;
  prev: number;
  fmt: (v: number) => string;
  on?: boolean;
  onClick?: () => void;
  lowerIsBetter?: boolean;
}) {
  const change = prev ? (v - prev) / prev : v ? 1 : 0;
  const better = lowerIsBetter ? change < 0 : change > 0;
  const Tag = onClick ? "button" : "div";
  return (
    <Tag
      onClick={onClick}
      aria-pressed={onClick ? on : undefined}
      className={cn("relative px-5 py-4 text-left", onClick && "transition-colors hover:bg-hover/50", on && "bg-hover/60")}
    >
      {on && <span aria-hidden className="absolute inset-x-5 bottom-0 h-0.5 rounded-full bg-ink" />}
      <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{label}</dt>
      <dd className="display mt-1.5 text-2xl text-ink tnum">{fmt(v)}</dd>
      <dd className="mt-0.5 flex items-center gap-1 text-xs text-ink-3 tnum">
        {prev === 0 && v === 0 ? (
          "–"
        ) : prev === 0 ? (
          "new this period"
        ) : (
          <>
            {change >= 0 ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
            <span className={cn(Math.abs(change) >= 0.05 && (better ? "text-ink" : "text-ink-2"))}>{Math.abs(Math.round(change * 100))}%</span> vs
            before
          </>
        )}
      </dd>
    </Tag>
  );
}

function Breakdown({
  title,
  tabs,
  mono,
  empty = "Nothing yet",
}: {
  title: string;
  tabs: Array<[string, AnalyticsCount[] | null]>;
  mono?: boolean;
  empty?: string;
}) {
  const [i, setI] = useState(0);
  const rows = tabs[i][1] ?? [];
  const max = Math.max(1, ...rows.map((r) => r.visitors));
  return (
    <section className="overflow-hidden rounded-xl border border-rule bg-raised/60" aria-label={title}>
      <div className="flex items-center gap-1 border-b border-rule px-3 pt-2">
        {tabs.map(([label], k) => (
          <button
            key={label}
            onClick={() => setI(k)}
            aria-pressed={i === k}
            className={cn(
              "relative h-9 px-2 text-sm text-ink-3 hover:text-ink",
              i === k && "font-medium text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-ink",
            )}
          >
            {label}
          </button>
        ))}
        <span className="ml-auto pr-1 text-2xs tracking-wider text-ink-4 uppercase">Visitors</span>
      </div>
      <ul className="p-2">
        {rows.length === 0 && <li className="px-3 py-6 text-center text-sm text-ink-3">{empty}</li>}
        {rows.map((r) => (
          <li key={r.value} className="relative flex items-center gap-3 rounded-md px-3 py-1.5 text-sm">
            <span aria-hidden className="absolute inset-y-0.5 left-0 rounded-md bg-ink/[0.07]" style={{ width: `${(r.visitors / max) * 100}%` }} />
            <span className={cn("relative min-w-0 flex-1 truncate text-ink", mono && "font-mono text-[0.8125rem]")} title={r.value}>
              {r.value || (title === "Sources" ? "Direct" : "(none)")}
            </span>
            <span className="relative font-mono text-xs text-ink-2 tnum">{num(r.visitors)}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Events({ data, visitors }: { data: AnalyticsEvent[]; visitors: number }) {
  return (
    <section className="overflow-hidden rounded-xl border border-rule bg-raised/60" aria-labelledby="events">
      <h2 id="events" className="border-b border-rule px-5 py-3 text-sm font-medium text-ink">
        Custom events
      </h2>
      {data.length === 0 ? (
        <p className="px-5 py-8 text-center text-sm text-ink-3">
          None yet. <code className="font-mono text-ink-2">track("Signup")</code> from your app and they show up here.
        </p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-2xs tracking-wider text-ink-4 uppercase">
              <th className="px-5 py-2 font-medium">Event</th>
              <th className="px-3 py-2 text-right font-medium">Visitors</th>
              <th className="px-3 py-2 text-right font-medium">Count</th>
              <th className="px-5 py-2 text-right font-medium">Conversion</th>
            </tr>
          </thead>
          <tbody>
            {data.map((e) => (
              <tr key={e.name} className="border-t border-rule/60 align-top">
                <td className="px-5 py-2.5">
                  <span className="text-ink">{e.name}</span>
                  {Object.entries(e.props ?? {}).map(([k, vs]) => (
                    <span key={k} className="mt-1 block text-xs text-ink-3">
                      {k}:{" "}
                      {(vs ?? [])
                        .slice(0, 4)
                        .map((v) => `${v.value} (${v.count})`)
                        .join(", ")}
                    </span>
                  ))}
                </td>
                <td className="px-3 py-2.5 text-right font-mono text-xs text-ink-2 tnum">{num(e.visitors)}</td>
                <td className="px-3 py-2.5 text-right font-mono text-xs text-ink-2 tnum">{num(e.count)}</td>
                <td className="px-5 py-2.5 text-right font-mono text-xs text-ink tnum">
                  {visitors ? `${((e.visitors / visitors) * 100).toFixed(1)}%` : "–"}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function Realtime({ project }: { project: string }) {
  const rt = useQuery({ queryKey: ["analytics-rt", project], queryFn: () => mod2.realtime(project), refetchInterval: 15_000 });
  const d = rt.data;
  const per = d?.perMinute ?? [];
  const max = Math.max(1, ...per.map((x) => x.pageviews));
  return (
    <section className="overflow-hidden rounded-xl border border-rule bg-raised/60 p-5" aria-labelledby="rt">
      <div className="flex items-baseline justify-between">
        <h2 id="rt" className="text-sm font-medium text-ink">
          Last 30 minutes
        </h2>
        <span className="text-xs text-ink-3">{d ? `${num(d.visitors30m)} visitors · ${num(d.pageviews30m)} views` : ""}</span>
      </div>
      <div className="mt-4 flex h-20 items-end gap-[3px]" aria-hidden>
        {per.map((x) => (
          <span
            key={x.t}
            className="flex-1 rounded-t-[2px] bg-ink-2/70"
            style={{ height: `${Math.max(2, (x.pageviews / max) * 100)}%` }}
            title={`${x.pageviews} views`}
          />
        ))}
        {per.length === 0 && <span className="w-full self-center text-center text-sm text-ink-3">Quiet right now.</span>}
      </div>
      {(d?.topPages ?? []).length > 0 && (
        <ul className="mt-4 flex flex-col gap-1 border-t border-rule pt-3">
          {(d?.topPages ?? []).slice(0, 4).map((x) => (
            <li key={x.value} className="flex justify-between gap-3 text-sm">
              <span className="truncate font-mono text-xs text-ink-2">{x.value}</span>
              <span className="font-mono text-xs text-ink-3 tnum">{x.pageviews}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function CodeBox({ code, name, className }: { code: string; name: string; className?: string }): ReactNode {
  return (
    <div className={cn("overflow-hidden rounded-xl border border-rule bg-paper-sunk", className)}>
      <div className="flex items-center justify-between border-b border-rule px-4 py-1.5">
        <span className="font-mono text-xs text-ink-3">{name}</span>
        <CopyButton value={code} label={`Copy ${name}`} />
      </div>
      <pre className="overflow-x-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">
        <code>{code}</code>
      </pre>
    </div>
  );
}
