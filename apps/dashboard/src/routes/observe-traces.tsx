import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mq, type TraceSpan, type TraceSummary } from "@/api/modules";
import { q as core } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { Facts, Group, Segmented, StateLine } from "@/components/health-kit";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton, Untrusted } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { ProjectIcon } from "@/components/project-icon";
import { Select } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { countWords, ms } from "@/lib/format";
import { full, relative } from "@/lib/time";

const windows = [
  { v: "1h", label: "Hour" },
  { v: "24h", label: "Day" },
  { v: "3d", label: "3 days" },
] as const;

/** Why the box kept a trace, as a person would say it. */
const keptWords: Record<TraceSummary["kept"], string> = { error: "failed", slow: "slow", sampled: "sample" };

function Status({ t }: { t: Pick<TraceSummary, "status" | "error"> }) {
  if (!t.status) return t.error ? <span className="text-danger">failed</span> : null;
  return <span className={cn("tnum", t.status >= 500 || t.error ? "text-danger" : t.status >= 400 ? "text-warn-ink" : "text-ink-3")}>{t.status}</span>;
}

/** Requests: the slowest and failing requests your apps traced, slowest first. */
export function TracesPage({ project, since = "24h", errors }: { project?: string; since?: string; errors?: boolean }) {
  useTitle("Requests");
  const navigate = useNavigate();
  const projects = useQuery(core.projects);
  const p = project ?? projects.data?.[0]?.name ?? "";
  const win = (windows.some((w) => w.v === since) ? since : "24h") as (typeof windows)[number]["v"];
  const list = useQuery(mq.traces(p, win, !!errors));
  const set = (o: { project?: string; since?: string; errors?: boolean }) =>
    navigate({ to: "/requests", search: { project: project, since: since === "24h" ? undefined : since, errors: errors || undefined, ...o } });
  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Request traces" />;
  const traces = list.data ?? [];
  const slow = traces.filter((t) => t.kept === "slow").length;
  const failed = traces.filter((t) => t.error).length;
  const line =
    traces.length === 0
      ? "No traced requests yet."
      : `${countWords(traces.length, "request", "requests", true)} kept${slow || failed ? `: ${[failed && countWords(failed, "failed"), slow && countWords(slow, "slow")].filter(Boolean).join(", ")}` : ""}. The slowest took ${ms(traces[0].durationMs)}.`;

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: "Health", to: "/status" }]} />}
        title="Requests"
        actions={
          (projects.data?.length ?? 0) > 1 && (
            <Select
              size="sm"
              value={p}
              onValueChange={(v) => set({ project: v })}
              aria-label="Project"
              className="w-48"
              options={(projects.data ?? []).map((x) => ({ value: x.name, label: x.name }))}
            />
          )
        }
      />
      {list.isSuccess && <StateLine>{line}</StateLine>}
      <p className="mt-2 max-w-[42rem] text-[0.875rem] text-ink-3">
        Each request your apps trace, step by step. The box keeps every request that failed or took a second or more, and one in ten of the rest, for three days.
        Next.js needs an <code className="ident text-ink-2">instrumentation.ts</code> with <code className="ident text-ink-2">registerOTel()</code>; the address and key are already set.
      </p>

      <div className="mt-7 flex flex-wrap items-center gap-3">
        <Segmented label="How far back" value={win} onChange={(v) => set({ since: v === "24h" ? undefined : v })} options={windows.map((w) => ({ v: w.v, label: w.label }))} />
        <Segmented
          label="Which requests"
          value={errors ? "failed" : "all"}
          onChange={(v) => set({ errors: v === "failed" || undefined })}
          options={[
            { v: "all", label: "Slowest" },
            { v: "failed", label: "Failed" },
          ]}
        />
      </div>

      <div className="mt-4">
        {projects.isSuccess && !p && <p className="border-y border-rule py-8 text-center text-[0.875rem] text-ink-3">No projects yet.</p>}
        {p && list.isPending && <Skeleton className="h-40" />}
        {list.isError && <ProblemNote className="mt-2" error={list.error} />}
        {list.isSuccess && traces.length === 0 && (
          <p className="border-y border-rule py-8 text-center text-[0.875rem] text-ink-3">
            {errors ? "No failed requests in this window." : `Nothing traced in ${p || "this project"} in this window.`}
          </p>
        )}
        {traces.length > 0 && (
          <ul className="divide-y divide-rule border-y border-rule">
            {traces.map((t) => (
              <li key={t.traceId}>
                <Link
                  to="/requests/$project/$id"
                  params={{ project: t.project, id: t.traceId }}
                  className="-mx-2 grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-6 rounded-[6px] px-2 py-3 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover sm:grid-cols-[minmax(0,1fr)_6rem_5.5rem]"
                >
                  <span className="min-w-0">
                    <span className="block font-mono text-[0.84375rem] leading-[1.375rem] text-ink [overflow-wrap:anywhere]">{t.name}</span>
                    <span className="mt-0.5 flex flex-wrap items-center gap-x-2.5 text-[0.8125rem] text-ink-3">
                      <span className="inline-flex items-center gap-1.5">
                        <ProjectIcon project={t.project} size={14} />
                        {t.app}
                      </span>
                      <span title={full(t.start)}>{relative(t.start)}</span>
                      <span>{countWords(t.spans, "step")}</span>
                      <span className={cn(t.kept === "error" && "text-danger")}>{keptWords[t.kept]}</span>
                    </span>
                  </span>
                  <span className="hidden pt-0.5 text-right text-[0.8125rem] sm:block">
                    <Status t={t} />
                  </span>
                  <span className={cn("pt-0.5 text-right text-[0.9375rem] tnum", t.durationMs >= 1000 ? "text-ink" : "text-ink-2")}>{ms(t.durationMs)}</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Page>
  );
}

/** One request: facts, then every step as a bar on one timeline. A step opens to its details. */
export function TracePage({ project, id }: { project: string; id: string }) {
  const d = useQuery(mq.trace(project, id));
  useTitle(d.data ? d.data.name : "Request");
  const [open, setOpen] = useState<string | null>(null);
  const crumbs = (
    <Crumbs
      items={[
        { label: "Health", to: "/status" },
        { label: "Requests", to: "/requests" },
      ]}
    />
  );
  if (d.isPending)
    return (
      <Page wide>
        <Skeleton className="h-4 w-32" />
        <Skeleton className="mt-3 h-8 w-2/3" />
        <Skeleton className="mt-10 h-60" />
      </Page>
    );
  if (d.isError)
    return (
      <Page wide>
        <PageHeader eyebrow={crumbs} title="Request" />
        <ProblemNote className="mt-8" error={d.error} />
      </Page>
    );
  const t = d.data;
  const spans = t.spans ?? [];
  const total = Math.max(t.durationMs, ...spans.map((s) => s.offsetMs + s.durationMs), 0.001);
  const ticks = [0, 0.25, 0.5, 0.75, 1];

  return (
    <Page wide>
      <header>
        <div className="mb-2 text-[0.8125rem] text-ink-3">{crumbs}</div>
        <h1 className="font-mono text-[1.25rem] leading-[1.875rem] font-[500] text-ink [overflow-wrap:anywhere] sm:text-[1.5rem] sm:leading-8">{t.name}</h1>
        <p className="mt-2 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[0.84375rem] text-ink-3">
          <span className="inline-flex items-center gap-1.5 text-ink-2">
            <ProjectIcon project={t.project} size={14} />
            {t.project} › {t.app}
          </span>
          <span title={full(t.start)}>{relative(t.start)}</span>
          <span className="text-ink">{ms(t.durationMs)}</span>
          <Status t={t} />
        </p>
      </header>

      <div className="mt-9 grid gap-x-12 gap-y-10 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <Group label="Steps" flush aside={countWords(spans.length, "step")}>
          <div className="border-y border-rule">
            <div className="hidden grid-cols-[minmax(0,2fr)_minmax(0,3fr)_4.5rem] gap-x-4 py-1.5 text-[0.6875rem] text-ink-4 sm:grid" aria-hidden>
              <span />
              <span className="relative h-3">
                {ticks.map((f) => (
                  <span key={f} className="absolute -translate-x-1/2 tnum first:translate-x-0 last:-translate-x-full" style={{ left: `${f * 100}%` }}>
                    {f === 0 ? "0" : ms(total * f)}
                  </span>
                ))}
              </span>
              <span />
            </div>
            <ol className="divide-y divide-rule">
              {spans.map((s) => (
                <SpanRow key={s.spanId} s={s} total={total} open={open === s.spanId} onToggle={() => setOpen(open === s.spanId ? null : s.spanId)} />
              ))}
            </ol>
          </div>
        </Group>
        <Group label="Facts" flush>
          <Facts
            narrow
            items={[
              ["Took", ms(t.durationMs)],
              !!t.status && ["Status", <Status t={t} />],
              !!t.route && ["Route", <span className="ident">{t.route}</span>],
              ["When", <span title={full(t.start)}>{relative(t.start)}</span>],
              ["Kept", t.kept === "error" ? "A step failed" : t.kept === "slow" ? "A step took a second or more" : "In the one-in-ten sample"],
              ["Trace", <span className="ident text-ink-2 [overflow-wrap:anywhere]">{t.traceId}</span>],
            ]}
          />
          <Link
            to="/logs"
            search={{ project: t.project, q: t.logsQuery, since: "3d" }}
            className="mt-3 inline-block text-[0.84375rem] text-brass-ink underline-offset-4 hover:underline"
          >
            This request in the logs →
          </Link>
        </Group>
      </div>
    </Page>
  );
}

function SpanRow({ s, total, open, onToggle }: { s: TraceSpan; total: number; open: boolean; onToggle: () => void }) {
  const left = (s.offsetMs / total) * 100;
  const width = Math.max((s.durationMs / total) * 100, 0.4);
  const attrs = Object.entries(s.attributes ?? {});
  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="grid w-full grid-cols-[minmax(0,1fr)_4.5rem] items-center gap-x-4 gap-y-1.5 py-2 text-left transition-colors duration-[var(--dur-state)] hover:bg-paper-hover sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_4.5rem]"
      >
        <span className="min-w-0 truncate text-[0.8125rem]" style={{ paddingLeft: `${Math.min(s.depth, 8) * 0.875}rem` }} title={s.name}>
          <span className={cn("font-mono", s.error ? "text-danger" : "text-ink")}>{s.name}</span>
          {s.kind === "client" && <span className="ml-2 text-ink-4">call</span>}
        </span>
        <span className="relative order-last col-span-2 h-2.5 sm:order-none sm:col-span-1" aria-hidden>
          <span className="absolute inset-y-0 w-full rounded-full bg-paper-sunk" />
          <span className={cn("absolute inset-y-0 rounded-full", s.error ? "bg-danger" : s.depth === 0 ? "bg-ink-2" : "bg-graphite")} style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }} />
        </span>
        <span className={cn("text-right text-[0.8125rem] tnum", s.error ? "text-danger" : "text-ink-2")}>{ms(s.durationMs)}</span>
      </button>
      {open && (
        <div className="pb-3">
          <Untrusted label="Recorded by your app. Shown as plain text.">
            <div className="px-3 py-2">
              <Facts
                narrow
                items={[
                  ["Starts at", ms(s.offsetMs)],
                  ["Kind", s.kind],
                  !!s.service && ["Service", <span className="ident">{s.service}</span>],
                  !!s.statusMessage && ["Message", <span className="text-danger [overflow-wrap:anywhere]">{s.statusMessage}</span>],
                  ...attrs.map(([k, v]) => [<span className="ident">{k}</span>, <Value v={v} />] as [ReactNode, ReactNode]),
                ]}
              />
              {(s.events ?? []).map((ev, i) => (
                <div key={i} className="mt-3">
                  <p className="text-[0.8125rem] text-ink-2">
                    <span className={cn("font-[550]", ev.name === "exception" && "text-danger")}>{ev.name}</span> at {ms(ev.offsetMs)}
                  </p>
                  {Object.entries(ev.attributes ?? {}).map(([k, v]) => (
                    <pre key={k} className="mt-1 overflow-x-auto font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2 [overflow-wrap:anywhere]">
                      <span className="text-ink-4">{k}: </span>
                      {typeof v === "string" ? v : JSON.stringify(v)}
                    </pre>
                  ))}
                </div>
              ))}
            </div>
          </Untrusted>
        </div>
      )}
    </li>
  );
}

function Value({ v }: { v: unknown }) {
  return <span className="ident text-ink-2 [overflow-wrap:anywhere]">{typeof v === "string" ? v : JSON.stringify(v)}</span>;
}
