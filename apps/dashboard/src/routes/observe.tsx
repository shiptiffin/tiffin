import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Bell, BellOff, Bug, Check, CircleDot, Pause, Radio, Search, Send } from "lucide-react";
import { useRef, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { q as core } from "@/api/queries";
import { mod, mq, type AlertRule, type Issue, type IssueDetail } from "@/api/modules";
import { AreaChart, Meter, toPoints } from "@/components/chart";
import { useTitle } from "@/components/favicon";
import { Empty, Page, PageHeader, Skeleton, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { bytes, duration, num, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { full, relative } from "@/lib/time";
import type { LogsSearch } from "@/router";

// ------------------------------------------------------------------ metrics

export function MetricsPage() {
  useTitle("Metrics");
  const o = useQuery(mq.overview);
  const settings = useQuery({ queryKey: ["observe-settings"], queryFn: mod.observeSettings, staleTime: 300_000 });
  if (o.isError && notOnBox(o.error)) return <NotOnBox what="Metrics" />;
  if (o.isPending)
    return (
      <Page full>
        <Skeleton className="h-10 w-56" />
        <div className="mt-10 grid gap-4 md:grid-cols-2">
          <Skeleton className="h-52" />
          <Skeleton className="h-52" />
        </div>
      </Page>
    );
  if (o.isError)
    return (
      <Page full>
        <ProblemNote error={o.error} title="Couldn't read the box's metrics" />
      </Page>
    );
  const d = o.data;
  const n = d.now;
  const s = d.series ?? {};
  const data = (n.disks ?? []).find((x) => x.mount === "/var/lib/tiffin") ?? (n.disks ?? [])[0];
  const rate = (v: number) => `${bytes(v)}/s`;

  return (
    <Page full>
      <PageHeader
        title="Metrics"
        lede={`The box's vital signs, a point a minute. Kept for ${settings.data?.metricsRetention ?? "30d"}; the last hour shows here.`}
        actions={
          <span className="flex items-center gap-2 text-sm text-ink-3">
            <span className="size-1.5 rounded-full bg-rev" /> up {duration(n.uptimeSeconds)} · {n.cpus} CPUs
          </span>
        }
      />
      {(d.firing ?? []).length > 0 && (
        <Link to="/alerts" className="mt-6 flex items-center gap-3 rounded-xl border border-irr-rule bg-irr-wash px-4 py-3 text-base text-ink">
          <Bell className="size-4 text-irr" />
          {(d.firing ?? []).length} {(d.firing ?? []).length === 1 ? "alert is" : "alerts are"} firing:{" "}
          {(d.firing ?? []).map((a) => a.summary).join("; ")}
        </Link>
      )}

      <div className="mt-10 grid gap-4 md:grid-cols-2">
        <ChartCard label="CPU" now={pct(n.cpuUsedRatio)} sub={`load ${n.load1.toFixed(2)} · ${n.load5.toFixed(2)} · ${n.load15.toFixed(2)}`}>
          <AreaChart label="CPU used" points={toPoints(s.cpu)} format={(v) => `${v.toFixed(0)}%`} max={100} />
        </ChartCard>
        <ChartCard
          label="Memory"
          now={pct(n.memoryUsedRatio)}
          sub={`${bytes(n.memoryTotalBytes - n.memoryAvailableBytes)} of ${bytes(n.memoryTotalBytes)}`}
        >
          <AreaChart label="Memory used" points={toPoints(s.memory)} format={(v) => `${v.toFixed(0)}%`} max={100} />
        </ChartCard>
        <ChartCard
          label="Data disk"
          now={data ? pct(data.usedRatio) : "–"}
          sub={data ? `${bytes(data.freeBytes)} free of ${bytes(data.totalBytes)}` : undefined}
        >
          <AreaChart label="Data disk used" points={toPoints(s.disk)} format={(v) => `${v.toFixed(1)}%`} />
        </ChartCard>
        <ChartCard
          label="Network"
          now={rate(Number((toPoints(s.rx).at(-1) ?? [0, 0])[1]))}
          sub={`in · ${rate(Number((toPoints(s.tx).at(-1) ?? [0, 0])[1]))} out`}
        >
          <AreaChart label="Network in" points={toPoints(s.rx)} format={rate} />
        </ChartCard>
      </div>

      <div className="mt-12 grid gap-10 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
        <section aria-labelledby="svc">
          <h2 id="svc" className="display-italic mb-3 text-xl text-ink">
            Services
          </h2>
          <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
            {(n.units ?? []).map((u) => (
              <li
                key={u.name}
                className="grid grid-cols-[1rem_minmax(0,1fr)_auto] items-center gap-3 px-4 py-2.5 sm:grid-cols-[1rem_minmax(0,1fr)_6rem_6rem_5rem]"
              >
                <span className={cn("size-2 rounded-full", u.active ? "bg-rev" : "bg-irr")} aria-label={u.active ? "running" : "not running"} />
                <span className="min-w-0 truncate font-mono text-[0.8125rem] text-ink">{u.name.replace(/\.service$/, "")}</span>
                <span className="hidden text-right font-mono text-xs text-ink-3 tnum sm:block">{bytes(u.memoryBytes)}</span>
                <span className="hidden text-right font-mono text-xs text-ink-3 tnum sm:block">{num(Math.round(u.cpuSeconds))} cpu·s</span>
                <span className={cn("text-right text-xs", u.restarts > 0 ? "text-out" : "text-ink-4")}>
                  {u.restarts} {u.restarts === 1 ? "restart" : "restarts"}
                </span>
              </li>
            ))}
          </ul>
        </section>
        <section aria-labelledby="disks">
          <h2 id="disks" className="display-italic mb-3 text-xl text-ink">
            Disks
          </h2>
          <ul className="flex flex-col gap-4 rounded-xl border border-rule bg-raised/60 p-4">
            {(n.disks ?? []).map((x) => (
              <li key={x.mount}>
                <div className="mb-1.5 flex items-baseline justify-between gap-3">
                  <code className="font-mono text-sm text-ink">{x.mount}</code>
                  <span className="text-xs text-ink-3 tnum">
                    {bytes(x.totalBytes - x.freeBytes)} / {bytes(x.totalBytes)}
                  </span>
                </div>
                <Meter ratio={x.usedRatio} label={`${x.mount} used`} />
              </li>
            ))}
            <li className="border-t border-rule pt-3 text-xs text-ink-3">
              Swap {n.swapTotalBytes ? `${bytes(n.swapTotalBytes - n.swapFreeBytes)} of ${bytes(n.swapTotalBytes)}` : "off"} · stores:{" "}
              {Object.entries(d.stores)
                .map(([k, v]) => `${k} ${v}`)
                .join(", ")}
            </li>
          </ul>
        </section>
      </div>
      <AppsTraffic />
    </Page>
  );
}

function ChartCard({ label, now, sub, children }: { label: string; now: string; sub?: string; children: ReactNode }) {
  return (
    <section className="rounded-xl border border-rule bg-raised/60 p-5">
      <div className="mb-4 flex items-baseline justify-between gap-3">
        <h2 className="text-sm font-medium text-ink-2">{label}</h2>
        <span className="text-right">
          <span className="display text-2xl text-ink tnum">{now}</span>
          {sub && <span className="block text-xs text-ink-3">{sub}</span>}
        </span>
      </div>
      {children}
    </section>
  );
}

/** Per-app traffic, once apps are deployed and serving (nothing shows before that). */
function AppsTraffic() {
  const apps = useQuery({ queryKey: ["observe-apps"], queryFn: () => mod.apps(undefined, "1h"), refetchInterval: 30_000 });
  const list = apps.data ?? [];
  if (list.length === 0) return null;
  return (
    <section className="mt-12" aria-labelledby="apps">
      <h2 id="apps" className="display-italic mb-3 text-xl text-ink">
        Apps, last hour
      </h2>
      <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
        {list.map((a) => (
          <li key={a.project + a.app} className="grid grid-cols-2 gap-3 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_6rem_6rem_6rem_6rem]">
            <span className="font-mono text-sm text-ink">
              {a.project}/{a.app}
            </span>
            <span className="text-right text-sm text-ink-2 tnum">{num(a.requests)} req</span>
            <span className={cn("text-right text-sm tnum", a.errorRate > 0.01 ? "text-irr" : "text-ink-3")}>{pct(a.errorRate, 1)} errors</span>
            <span className="text-right text-sm text-ink-3 tnum">p50 {Math.round(a.p50ms)} ms</span>
            <span className="text-right text-sm text-ink-3 tnum">p95 {Math.round(a.p95ms)} ms</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

// ------------------------------------------------------------------ logs

const sinceOptions = ["15m", "1h", "6h", "24h", "7d"];
const presets: Array<{ label: string; q: string }> = [
  { label: "Everything", q: "*" },
  { label: "Errors", q: "level:error OR source:errors" },
  { label: "Edge requests", q: "source:edge" },
  { label: "App output", q: "source:app" },
];

export function LogsPage({ q = "*", project, since = "1h", live }: LogsSearch) {
  useTitle("Logs");
  const navigate = useNavigate();
  const projects = useQuery(core.projects);
  const settings = useQuery({ queryKey: ["observe-settings"], queryFn: mod.observeSettings, staleTime: 300_000 });
  const { admin } = useMe();
  const [text, setText] = useState(q);
  const [open, setOpen] = useState<number | null>(null);
  const scope = project ?? (admin ? "" : (projects.data?.[0]?.name ?? ""));
  // Lines seen before, so live tail can mark what just arrived.
  const seen = useRef<Set<string>>(new Set());
  const res = useQuery({
    queryKey: ["logs", scope, q, live ? "5m" : since],
    queryFn: async () => {
      const r = await mod.logs({ query: q || "*", project: scope || undefined, since: live ? "5m" : since, limit: 300 });
      const first = seen.current.size === 0;
      const fresh = new Set<string>();
      for (const row of r.rows ?? []) {
        const k = String(row._time) + String(row._msg);
        if (!first && !seen.current.has(k)) fresh.add(k);
        seen.current.add(k);
      }
      return { ...r, fresh };
    },
    refetchInterval: live ? 3000 : false,
    placeholderData: (d) => d,
  });
  const set = (o: Partial<LogsSearch>) => navigate({ to: "/logs", search: { q, project, since, live, ...o }, replace: true });
  const rows = res.data?.rows ?? [];
  const fresh = live ? (res.data?.fresh ?? new Set<string>()) : new Set<string>();

  if (res.isError && notOnBox(res.error)) return <NotOnBox what="Logs" />;

  return (
    <Page full>
      <PageHeader
        title="Logs"
        lede={
          <>
            What the box and your apps write, searchable with{" "}
            <a
              href="https://docs.victoriametrics.com/victorialogs/logsql/"
              target="_blank"
              rel="noopener noreferrer"
              className="text-brass-ink underline underline-offset-4 hover:text-ink"
            >
              LogsQL
            </a>
            . Kept for {settings.data?.logsRetention ?? "14d"}.
          </>
        }
      />
      <form
        className="mt-8 flex flex-col gap-2 rounded-xl border border-rule bg-raised/70 p-2 md:flex-row md:items-center"
        onSubmit={(e) => {
          e.preventDefault();
          set({ q: text.trim() || "*" });
        }}
      >
        <select
          value={scope}
          onChange={(e) => set({ project: e.target.value || undefined })}
          aria-label="Whose logs"
          className="h-9 rounded-md border border-rule bg-paper px-2.5 text-sm text-ink outline-none focus-visible:border-brass md:w-44"
        >
          {admin && <option value="">The box itself</option>}
          {(projects.data ?? []).map((p) => (
            <option key={p.name} value={p.name}>
              {p.name}
            </option>
          ))}
        </select>
        <div className="flex min-w-0 flex-1 items-center gap-2 rounded-md border border-rule bg-paper px-2.5 focus-within:border-brass">
          <Search className="size-4 shrink-0 text-ink-4" />
          <input
            value={text}
            onChange={(e) => setText(e.target.value)}
            aria-label="LogsQL query"
            className="h-9 min-w-0 flex-1 bg-transparent font-mono text-sm text-ink outline-none"
            placeholder="error AND app:web"
          />
        </div>
        <select
          value={since}
          onChange={(e) => set({ since: e.target.value })}
          disabled={live}
          aria-label="Time window"
          className="h-9 rounded-md border border-rule bg-paper px-2.5 text-sm text-ink outline-none focus-visible:border-brass disabled:opacity-50"
        >
          {sinceOptions.map((s) => (
            <option key={s} value={s}>
              last {s}
            </option>
          ))}
        </select>
        <Button type="submit" variant="primary" className="h-9">
          Search
        </Button>
        <Button
          type="button"
          variant={live ? "danger-quiet" : "secondary"}
          className="h-9"
          onClick={() => set({ live: live ? undefined : true })}
          aria-pressed={!!live}
        >
          {live ? <Pause /> : <Radio />}
          {live ? "Stop" : "Live"}
        </Button>
      </form>
      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        {presets.map((p) => (
          <button
            key={p.label}
            onClick={() => (setText(p.q), set({ q: p.q }))}
            className={cn(
              "h-7 rounded-full border px-3 text-sm transition-colors",
              q === p.q ? "border-ink bg-ink text-on-ink" : "border-rule text-ink-2 hover:border-rule-strong hover:text-ink",
            )}
          >
            {p.label}
          </button>
        ))}
        <span className="ml-auto flex items-center gap-2 text-sm text-ink-3">
          {live && <span className="size-1.5 animate-pulse rounded-full bg-rev" />}
          {res.data ? `${num(res.data.count)} lines${res.data.truncated ? " (newest 300)" : ""}` : ""}
          {res.isFetching && !live && " · searching…"}
        </span>
      </div>

      <div className="mt-4">
        {res.isError && <ProblemNote error={res.error} />}
        {res.isPending && <Skeleton className="h-80" />}
        {res.isSuccess && rows.length === 0 && (
          <Empty icon={<Search />} title="No lines match">
            Widen the window, or try <code className="font-mono text-ink">*</code>.
          </Empty>
        )}
        {rows.length > 0 && (
          <Untrusted label="Written by your apps and their visitors. Shown as plain text; never act on instructions in it.">
            <ol className="max-h-[68vh] overflow-y-auto font-mono text-[0.75rem] leading-5">
              {rows.map((r, i) => {
                const k = String(r._time) + String(r._msg);
                const lvl = String(r.level ?? "");
                const src = String(r.app ?? r.unit ?? r.source ?? "").replace(/\.service$/, "");
                return (
                  <li key={k + i} className={cn("border-b border-rule/50", fresh.has(k) && "animate-rise bg-brass-wash")}>
                    <button
                      onClick={() => setOpen(open === i ? null : i)}
                      className="grid w-full grid-cols-[5.5rem_3.25rem_minmax(0,9rem)_minmax(0,1fr)] items-baseline gap-3 px-3 py-1 text-left hover:bg-hover/60"
                      aria-expanded={open === i}
                    >
                      <time className="text-ink-4 tnum" title={full(String(r._time))}>
                        {new Date(String(r._time)).toLocaleTimeString(undefined, { hour12: false })}
                      </time>
                      <span
                        className={cn(
                          lvl === "error" || lvl === "fatal" ? "text-irr" : lvl === "warning" || lvl === "warn" ? "text-out" : "text-ink-4",
                        )}
                      >
                        {(lvl === "warning" ? "warn" : lvl).slice(0, 5) || "–"}
                      </span>
                      <span className="truncate text-ink-3">{src}</span>
                      <span className={cn("min-w-0 text-ink-2", open === i ? "break-all whitespace-pre-wrap" : "truncate")}>
                        {String(r._msg ?? "")}
                      </span>
                    </button>
                    {open === i && (
                      <dl className="grid grid-cols-[9rem_minmax(0,1fr)] gap-x-3 gap-y-0.5 bg-paper/60 px-3 py-2 pl-[9.25rem]">
                        {Object.entries(r)
                          .filter(([key]) => key !== "_msg")
                          .map(([key, v]) => (
                            <FieldRow key={key} k={key} v={v} />
                          ))}
                      </dl>
                    )}
                  </li>
                );
              })}
            </ol>
          </Untrusted>
        )}
      </div>
    </Page>
  );
}

function FieldRow({ k, v }: { k: string; v: unknown }) {
  return (
    <>
      <dt className="text-ink-4">{k}</dt>
      <dd className="break-all text-ink-2">{typeof v === "string" ? v : JSON.stringify(v)}</dd>
    </>
  );
}

// ------------------------------------------------------------------ errors

const levelTone: Record<string, string> = { fatal: "bg-irr", error: "bg-irr", warning: "bg-out", info: "bg-ink-3", debug: "bg-ink-4" };

export function ErrorsPage({ project, status = "unresolved" }: { project?: string; status?: string }) {
  useTitle("Errors");
  const navigate = useNavigate();
  const st = (["unresolved", "resolved", "ignored"].includes(status) ? status : "unresolved") as Issue["status"];
  const list = useQuery(mq.issues(project, st));
  const projects = useQuery(core.projects);
  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Error tracking" />;
  const issues = list.data ?? [];
  const set = (o: { project?: string; status?: string }) =>
    navigate({ to: "/errors", search: { project, status: st === "unresolved" ? undefined : st, ...o } });
  return (
    <Page wide>
      <PageHeader
        title="Errors"
        lede="Exceptions your apps report, grouped into issues. Any Sentry SDK works: apps get SENTRY_DSN already."
        actions={
          <select
            value={project ?? ""}
            onChange={(e) => set({ project: e.target.value || undefined })}
            aria-label="Project"
            className="h-9 rounded-md border border-rule bg-paper px-2.5 text-sm text-ink outline-none focus-visible:border-brass"
          >
            <option value="">All projects</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.name} value={p.name}>
                {p.name}
              </option>
            ))}
          </select>
        }
      />
      <nav className="mt-8 flex gap-1 border-b border-rule" aria-label="Status">
        {(["unresolved", "resolved", "ignored"] as const).map((s) => (
          <button
            key={s}
            onClick={() => set({ status: s === "unresolved" ? undefined : s })}
            aria-pressed={st === s}
            className={cn(
              "relative h-10 px-3 text-base capitalize text-ink-3 hover:text-ink",
              st === s && "font-medium text-ink after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full after:bg-ink",
            )}
          >
            {s}
          </button>
        ))}
      </nav>
      <div className="mt-6">
        {list.isPending && <Skeleton className="h-40" />}
        {list.isError && <ProblemNote error={list.error} />}
        {list.isSuccess && issues.length === 0 && (
          <Empty icon={<Check />} title={st === "unresolved" ? "No open issues. Nice." : `No ${st} issues.`}>
            {st === "unresolved" && "When an app throws, the error lands here, grouped with others like it."}
          </Empty>
        )}
        <ul className="flex flex-col gap-2">
          {issues.map((i, k) => (
            <li key={i.id} className="animate-rise" style={{ animationDelay: `${k * 35}ms` }}>
              <Link
                to="/errors/$id"
                params={{ id: i.id }}
                className="group grid grid-cols-[0.5rem_minmax(0,1fr)_auto] items-start gap-x-4 rounded-xl border border-rule bg-raised/60 px-4 py-3.5 transition-colors hover:border-rule-strong hover:bg-raised sm:px-5"
              >
                <span className={cn("mt-2 size-2 rounded-full", levelTone[i.level] ?? "bg-ink-3")} aria-label={i.level} />
                <span className="min-w-0">
                  <span className="block truncate text-md font-medium text-ink">{i.title}</span>
                  <span className="mt-0.5 flex flex-wrap items-center gap-x-2 text-sm text-ink-3">
                    {i.culprit && <code className="font-mono text-xs text-ink-2">{i.culprit}</code>}
                    <span>
                      {i.project}/{i.app}
                    </span>
                    {i.lastRelease && <span className="font-mono text-xs">{i.lastRelease}</span>}
                  </span>
                </span>
                <span className="text-right">
                  <span className="display block text-xl text-ink tnum">{num(i.count)}</span>
                  <span className="block text-xs text-ink-3">last {relative(i.lastSeen)}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </Page>
  );
}

export function IssuePage({ id }: { id: string }) {
  const qc = useQueryClient();
  const d = useQuery(mq.issue(id));
  useTitle(d.data ? d.data.title : "Error");
  const set = useMutation({
    mutationFn: (s: Issue["status"]) => mod.resolveIssue(id, s),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["issue", id] });
      qc.invalidateQueries({ queryKey: ["issues"] });
    },
  });
  const { can } = useMe();
  const [ev, setEv] = useState(0);
  if (d.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-2/3" />
      </Page>
    );
  if (d.isError)
    return (
      <Page wide>
        <ProblemNote error={d.error} />
      </Page>
    );
  const i: IssueDetail = d.data;
  const events = i.events ?? [];
  const e = events[ev]?.event;
  return (
    <Page wide>
      <Link to="/errors" search={{}} className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink">
        <ArrowLeft className="size-3.5" /> Errors
      </Link>
      <header className="mt-5 animate-rise">
        <p className="flex flex-wrap items-center gap-2 text-sm text-ink-3">
          <span className={cn("size-2 rounded-full", levelTone[i.level] ?? "bg-ink-3")} />
          {i.level} in{" "}
          <code className="font-mono text-ink-2">
            {i.project}/{i.app}
          </code>
          {i.platform && <span>· {i.platform}</span>}
          <span className="rounded-full bg-hover px-2 py-px text-xs capitalize text-ink-2">{i.status}</span>
        </p>
        <h1 className="mt-2 text-xl leading-8 font-semibold tracking-tight break-words text-ink sm:text-2xl">{i.title}</h1>
        {i.culprit && <p className="mt-1 font-mono text-sm text-ink-3">{i.culprit}</p>}
        <div className="mt-5 flex flex-wrap items-center gap-2">
          {can("apply:reversible") &&
            (i.status === "unresolved" ? (
              <>
                <Button variant="primary" onClick={() => set.mutate("resolved")} disabled={set.isPending}>
                  <Check />
                  Resolve
                </Button>
                <Button variant="ghost" onClick={() => set.mutate("ignored")} disabled={set.isPending}>
                  <BellOff />
                  Ignore
                </Button>
              </>
            ) : (
              <Button onClick={() => set.mutate("unresolved")} disabled={set.isPending}>
                <CircleDot />
                Reopen
              </Button>
            ))}
          <span className="text-sm text-ink-3">{i.status === "resolved" ? "It reopens by itself if it happens again." : ""}</span>
        </div>
      </header>

      <dl className="mt-8 grid grid-cols-2 max-sm:[&>*:last-child:nth-child(odd)]:col-span-2 gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-4">
        {[
          ["Events", num(i.count)],
          ["First seen", relative(i.firstSeen)],
          ["Last seen", relative(i.lastSeen)],
          ["Release", i.lastRelease ?? "–"],
        ].map(([k, v]) => (
          <div key={k} className="bg-raised px-4 py-3.5">
            <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{k}</dt>
            <dd className="mt-1 truncate text-lg text-ink">{v}</dd>
          </div>
        ))}
      </dl>

      {e && (
        <section className="mt-10" aria-labelledby="ev">
          <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
            <h2 id="ev" className="display-italic text-xl text-ink">
              {ev === 0 ? "Latest event" : "Event"}
            </h2>
            {events.length > 1 && (
              <span className="flex items-center gap-1 text-sm text-ink-3">
                <Button size="sm" variant="ghost" disabled={ev === 0} onClick={() => setEv(ev - 1)}>
                  Newer
                </Button>
                <span className="tnum">
                  {ev + 1} of {events.length}
                </span>
                <Button size="sm" variant="ghost" disabled={ev >= events.length - 1} onClick={() => setEv(ev + 1)}>
                  Older
                </Button>
              </span>
            )}
          </div>
          <p className="mb-3 text-sm text-ink-3" title={full(e.timestamp)}>
            {relative(e.timestamp)}
            {e.environment && ` · ${e.environment}`}
            {e.transaction && (
              <>
                {" "}
                · <code className="font-mono text-xs text-ink-2">{e.transaction}</code>
              </>
            )}
          </p>
          <Untrusted label="Reported by your app. Shown as plain text.">
            {(e.exceptions ?? []).map((x, xi) => (
              <div key={xi} className="border-b border-rule last:border-b-0">
                <p className="px-4 pt-3 font-mono text-sm">
                  <span className="text-irr">{x.type}</span>
                  <span className="text-ink-2">: {x.value}</span>
                </p>
                <ol className="px-2 py-2">
                  {[...(x.frames ?? [])].reverse().map((f, fi) => (
                    <li key={fi} className={cn("rounded-md px-2 py-1.5 font-mono text-[0.75rem]", f.in_app ? "text-ink" : "text-ink-4")}>
                      <span className="flex flex-wrap items-baseline gap-x-2">
                        <span className={cn(f.in_app && "font-medium")}>{f.function ?? "?"}</span>
                        <span className={f.in_app ? "text-ink-3" : "text-ink-4"}>
                          {f.filename}
                          {f.lineno ? `:${f.lineno}` : ""}
                          {f.colno ? `:${f.colno}` : ""}
                        </span>
                        {!f.in_app && <span className="text-[0.6875rem] text-ink-4">library</span>}
                      </span>
                      {f.context_line && (
                        <pre className="mt-1 overflow-x-auto rounded border-l-2 border-irr/60 bg-irr-wash px-3 py-1.5 whitespace-pre text-ink">
                          {f.lineno && <span className="mr-3 text-ink-4 select-none">{f.lineno}</span>}
                          {f.context_line}
                        </pre>
                      )}
                    </li>
                  ))}
                </ol>
              </div>
            ))}
            {!e.exceptions?.length && e.message && <p className="px-4 py-3 font-mono text-sm text-ink-2">{e.message}</p>}
            {e.tags && Object.keys(e.tags).length > 0 && (
              <div className="flex flex-wrap gap-1.5 border-t border-rule px-4 py-3">
                {Object.entries(e.tags).map(([k, v]) => (
                  <span key={k} className="rounded-md border border-rule bg-paper px-2 py-0.5 font-mono text-xs">
                    <span className="text-ink-4">{k}</span> <span className="text-ink-2">{v}</span>
                  </span>
                ))}
              </div>
            )}
          </Untrusted>
        </section>
      )}
    </Page>
  );
}

// ------------------------------------------------------------------ alerts

export function AlertsPage() {
  useTitle("Alerts");
  const qc = useQueryClient();
  const alerts = useQuery(mq.alerts);
  const rules = useQuery(mq.rules);
  const settings = useQuery({ queryKey: ["observe-settings"], queryFn: mod.observeSettings });
  const { admin } = useMe();
  const test = useMutation({ mutationFn: mod.testAlert, onSuccess: () => qc.invalidateQueries({ queryKey: ["alerts"] }) });
  const toggle = useMutation({
    mutationFn: (r: AlertRule) =>
      mod.putRule(r.name, {
        kind: r.kind,
        threshold: r.threshold,
        enabled: !r.enabled,
        description: r.description,
        expr: r.expr,
        forSeconds: r.forSeconds,
        project: r.project,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rules"] }),
  });
  if (alerts.isError && notOnBox(alerts.error)) return <NotOnBox what="Alerts" />;
  const firing = alerts.data?.firing ?? [];
  const history = alerts.data?.history ?? [];
  const s = settings.data;
  const delivery = s?.webhook
    ? `a webhook (${new URL(s.webhook).host})`
    : s?.emailProject
      ? `email from ${s.emailProject}${s.email ? ` to ${s.email}` : ""}`
      : null;
  return (
    <Page wide>
      <PageHeader
        title="Alerts"
        lede="The box watches itself and tells you when something needs a person."
        actions={
          admin && (
            <Button onClick={() => test.mutate()} disabled={test.isPending}>
              <Send />
              {test.isPending ? "Sending…" : "Send a test alert"}
            </Button>
          )
        }
      />
      <div
        className={cn(
          "mt-8 flex items-center gap-4 rounded-xl border px-5 py-4",
          firing.length ? "border-irr-rule bg-irr-wash" : "border-rule bg-raised/60",
        )}
      >
        {firing.length ? <Bell className="size-5 text-irr" /> : <Check className="size-5 text-rev" />}
        <div className="flex-1">
          <p className="text-md text-ink">{firing.length ? `${firing.length} firing now` : "Nothing is firing."}</p>
          {firing.map((a) => (
            <p key={a.rule + a.subject} className="mt-1 text-base text-ink-2">
              {a.summary} <span className="text-sm text-ink-3">since {relative(a.since)}</span>
            </p>
          ))}
        </div>
      </div>
      {s && !delivery && (
        <p className="mt-3 rounded-lg border border-out/40 bg-out-wash px-4 py-2.5 text-sm text-ink">
          Alerts aren't delivered anywhere yet, so they only show here. Set a webhook or an email project:{" "}
          <code className="font-mono">tiffin observe settings set --webhook URL</code>
        </p>
      )}
      {delivery && <p className="mt-3 text-sm text-ink-3">Delivered to {delivery}.</p>}

      <section className="mt-10" aria-labelledby="rules">
        <h2 id="rules" className="display-italic mb-3 text-xl text-ink">
          Rules
        </h2>
        <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
          {(rules.data ?? []).map((r) => (
            <li key={r.name} className={cn("flex items-center gap-4 px-4 py-3 sm:px-5", !r.enabled && "opacity-60")}>
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-2">
                  <code className="font-mono text-[0.8125rem] text-ink">{r.name}</code>
                  {r.project && <span className="rounded-full bg-hover px-1.5 py-px font-mono text-xs text-ink-3">{r.project}</span>}
                </span>
                <span className="mt-0.5 block text-sm text-ink-3">{r.description}</span>
              </span>
              {admin ? (
                <button
                  role="switch"
                  aria-checked={r.enabled}
                  aria-label={`${r.enabled ? "Turn off" : "Turn on"} ${r.name}`}
                  onClick={() => toggle.mutate(r)}
                  className={cn("relative h-5 w-9 shrink-0 rounded-full transition-colors", r.enabled ? "bg-ink" : "bg-rule-strong")}
                >
                  <span
                    className={cn(
                      "absolute top-0.5 left-0.5 size-4 rounded-full bg-raised shadow transition-transform",
                      r.enabled && "translate-x-4",
                    )}
                  />
                </button>
              ) : (
                <span className="text-xs text-ink-3">{r.enabled ? "on" : "off"}</span>
              )}
            </li>
          ))}
        </ul>
      </section>

      <section className="mt-10" aria-labelledby="hist">
        <h2 id="hist" className="display-italic mb-3 text-xl text-ink">
          History
        </h2>
        {history.length === 0 ? (
          <p className="text-base text-ink-3">Nothing has fired yet.</p>
        ) : (
          <ol className="relative ml-1 border-l border-rule pl-5">
            {history.map((h) => (
              <li key={h.id} className="relative pb-5">
                <span
                  className={cn(
                    "absolute top-1.5 -left-[1.6rem] size-2.5 rounded-full ring-4 ring-paper",
                    h.state === "firing" ? "bg-irr" : h.state === "resolved" ? "bg-rev" : "bg-ink-3",
                  )}
                />
                <p className="text-base text-ink">
                  {h.summary} <span className="text-sm text-ink-3">· {h.state}</span>
                </p>
                <p className="text-sm text-ink-3" title={full(h.at)}>
                  {relative(h.at)} · <code className="font-mono text-xs">{h.rule}</code> · {h.delivery}
                </p>
              </li>
            ))}
          </ol>
        )}
      </section>
      <p className="mt-6 flex items-center gap-2 text-sm text-ink-3">
        <Bug className="size-3.5" />
        Error spikes come from{" "}
        <Link to="/errors" search={{}} className="text-brass-ink underline underline-offset-4">
          Errors
        </Link>
        ; disk and memory from{" "}
        <Link to="/metrics" className="text-brass-ink underline underline-offset-4">
          Metrics
        </Link>
        .
      </p>
    </Page>
  );
}
