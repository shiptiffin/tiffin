import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowLeft,
  CalendarClock,
  Check,
  CircleSlash,
  Clock,
  Hourglass,
  Link2,
  Moon,
  Pause,
  Play,
  Radio,
  RotateCcw,
  Send,
  Stamp,
  X,
  Zap,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod2, mq, type QueueJob, type QueueStats, type WorkflowApproval, type WorkflowRun, type WorkflowStep } from "@/api/modules";
import { useTitle } from "@/components/favicon";
import { Empty, Page, PageHeader, Skeleton, Tabs, Untrusted, NotOnBox } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { cronWords, ms, num, pct } from "@/lib/format";
import { useMe } from "@/lib/me";
import { clock, full, relative } from "@/lib/time";

// ------------------------------------------------------------------ shared

function Header({ project, title, lede, actions }: { project: string; title: ReactNode; lede?: ReactNode; actions?: ReactNode }) {
  return (
    <PageHeader
      eyebrow={
        <Link to="/projects/$project" params={{ project }} className="font-mono hover:text-ink">
          {project}
        </Link>
      }
      title={title}
      lede={lede}
      actions={actions}
    >
      <Tabs
        items={[
          { to: "/projects/$project/queues", params: { project }, label: "Queues", exact: true },
          { to: "/projects/$project/queues/jobs", params: { project }, label: "Jobs" },
          { to: "/projects/$project/workflows", params: { project }, label: "Workflows" },
        ]}
      />
    </PageHeader>
  );
}

const jobTone: Record<QueueJob["state"], string> = {
  scheduled: "text-ink-3",
  queued: "text-ink-2",
  running: "text-brass-ink",
  retrying: "text-out",
  completed: "text-rev",
  dead: "text-irr",
  cancelled: "text-ink-4",
};

function StateDot({ state, className }: { state: string; className?: string }) {
  const c =
    state === "completed"
      ? "bg-rev"
      : state === "dead" || state === "failed"
        ? "bg-irr"
        : state === "retrying" || state === "waiting" || state === "timed_out"
          ? "bg-out"
          : state === "running"
            ? "animate-pulse bg-brass"
            : "bg-ink-4";
  return <span className={cn("inline-block size-2 shrink-0 rounded-full", c, className)} aria-label={state} />;
}

/** "HTTP 489: …" is the SDK's "don't retry" answer: show the app's message, not the protocol. */
function jobError(e?: string): { text: string; gaveUp: boolean } | null {
  if (!e) return null;
  const m = e.match(/^HTTP 489:\s*(.*)$/s);
  return m ? { text: m[1], gaveUp: true } : { text: e, gaveUp: false };
}

function ErrorText({ e, className }: { e?: string; className?: string }) {
  const x = jobError(e);
  if (!x) return null;
  return (
    <span className={className}>
      {x.text}
      {x.gaveUp && <span className="text-ink-3"> · no retry</span>}
    </span>
  );
}

// ------------------------------------------------------------------ queues

export function QueuesPage({ project }: { project: string }) {
  useTitle(`${project} · Queues`);
  const qc = useQueryClient();
  const { can } = useMe();
  const stats = useQuery({ queryKey: ["queue-stats", project], queryFn: () => mod2.queueStats(project), refetchInterval: 4000 });
  const crons = useQuery(mq.crons(project));
  const topics = useQuery(mq.topics(project));
  const toggle = useMutation({
    mutationFn: (q: QueueStats) => (q.paused ? mod2.resume(project, q.name) : mod2.pause(project, q.name)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["queue-stats", project] }),
  });
  const trigger = useMutation({
    mutationFn: (name: string) => mod2.triggerCron(project, name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["crons", project] }),
  });

  if (stats.isError && notOnBox(stats.error)) return <NotOnBox what="Queues" />;
  const all = stats.data ?? [];
  const visible = all.filter((q) => !q.name.startsWith("_"));
  const system = all.filter((q) => q.name.startsWith("_"));
  const dead = all.reduce((n, q) => n + q.dead, 0);
  const waiting = all.reduce((n, q) => n + q.queued + q.retrying, 0);
  const done = all.reduce((n, q) => n + q.completedLastHour, 0);

  return (
    <Page full>
      <Header
        project={project}
        title="Queues"
        lede="Jobs your apps send, pushed to them with retries, backoff and a dead-letter queue. Live: this page refreshes every few seconds."
      />
      {stats.isError && <ProblemNote className="mt-6" error={stats.error} />}

      <dl className="mt-8 grid grid-cols-2 max-sm:[&>*:last-child:nth-child(odd)]:col-span-2 gap-px overflow-hidden rounded-xl border border-rule bg-rule sm:grid-cols-4">
        <Fact label="Done, last hour" value={num(done)} />
        <Fact label="Waiting" value={num(waiting)} sub={waiting ? "queued or retrying" : "nothing behind"} />
        <Fact label="Running" value={num(all.reduce((n, q) => n + q.running, 0))} />
        <Fact label="Dead letters" value={num(dead)} tone={dead ? "irr" : undefined} sub={dead ? "gave up; replay below" : "none"} />
      </dl>

      <section className="mt-8 overflow-hidden rounded-xl border border-rule bg-raised/60" aria-label="Queues">
        {/* Phones get one stacked row per queue instead of a table that scrolls sideways. */}
        <ul className="divide-y divide-rule/60 sm:hidden">
          {stats.isPending && <Skeleton className="m-4 h-24" />}
          {visible.map((q) => (
            <li key={q.name}>
              <Link to="/projects/$project/queues/jobs" params={{ project }} search={{ queue: q.name }} className="block px-4 py-3 active:bg-hover/50">
                <span className="flex items-center gap-2">
                  {q.topic && <Radio className="size-3.5 text-ink-3" aria-label="topic" />}
                  <span className="font-mono text-[0.8125rem] text-ink">{q.name}</span>
                  {q.paused && <span className="rounded-full bg-out-wash px-1.5 py-px text-xs text-out">paused</span>}
                  <span className="ml-auto font-mono text-xs text-ink-2 tnum">{num(q.completedLastHour)} / h</span>
                </span>
                <span className="mt-1 flex flex-wrap gap-x-3 text-xs text-ink-3 tnum">
                  {q.queued + q.running + q.retrying === 0 && q.dead === 0 && <span>Nothing waiting</span>}
                  {q.queued > 0 && <span className="text-ink-2">{num(q.queued)} queued</span>}
                  {q.running > 0 && <span className="text-brass-ink">{num(q.running)} running</span>}
                  {q.retrying > 0 && <span className="text-out">{num(q.retrying)} retrying</span>}
                  {q.dead > 0 && <span className="font-medium text-irr">{num(q.dead)} dead</span>}
                  {q.p95Ms ? <span>p95 {ms(q.p95Ms)}</span> : null}
                  {q.failureRate > 0 && <span>{pct(q.failureRate)} failing</span>}
                </span>
              </Link>
            </li>
          ))}
        </ul>
        <div className="hidden overflow-x-auto sm:block">
        <table className="w-full min-w-[46rem] text-sm">
          <thead>
            <tr className="text-left text-2xs font-medium tracking-wider text-ink-3 uppercase">
              {["Queue", "Queued", "Running", "Retrying", "Dead", "Done / h", "p50", "p95", "Oldest", ""].map((h, i) => (
                <th key={i} className={cn("border-b border-rule px-4 py-2.5 font-medium", i > 0 && i < 9 && "text-right")}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {stats.isPending && (
              <tr>
                <td colSpan={10} className="p-4">
                  <Skeleton className="h-24" />
                </td>
              </tr>
            )}
            {visible.map((q) => (
              <tr key={q.name} className="group">
                <td className="border-b border-rule/60 px-4 py-3">
                  <Link
                    to="/projects/$project/queues/jobs"
                    params={{ project }}
                    search={{ queue: q.name }}
                    className="flex items-center gap-2 hover:underline hover:underline-offset-4"
                  >
                    {q.topic ? <Radio className="size-3.5 text-ink-3" aria-label="topic" /> : <span className="size-3.5" />}
                    <span className="font-mono text-[0.8125rem] text-ink">{q.name}</span>
                    {q.paused && <span className="rounded-full bg-out-wash px-1.5 py-px text-xs text-out">paused</span>}
                  </Link>
                  <span className="mt-0.5 block pl-5.5 text-xs text-ink-4">
                    {q.topic ? "topic" : q.app ? `→ ${q.app}${q.path ?? ""}` : "not configured"}
                    {q.failureRate > 0 && ` · ${pct(q.failureRate)} failing`}
                  </span>
                </td>
                <Num v={q.queued} />
                <Num v={q.running} tone={q.running ? "brass" : undefined} />
                <Num v={q.retrying} tone={q.retrying ? "out" : undefined} />
                <Num v={q.dead} tone={q.dead ? "irr" : undefined} />
                <Num v={q.completedLastHour} />
                <td className="border-b border-rule/60 px-4 py-3 text-right font-mono text-xs text-ink-3 tnum">{q.p50Ms ? ms(q.p50Ms) : "–"}</td>
                <td className="border-b border-rule/60 px-4 py-3 text-right font-mono text-xs text-ink-3 tnum">{q.p95Ms ? ms(q.p95Ms) : "–"}</td>
                <td
                  className={cn(
                    "border-b border-rule/60 px-4 py-3 text-right font-mono text-xs tnum",
                    q.oldestQueuedSeconds > 300 ? "text-out" : "text-ink-3",
                  )}
                >
                  {q.oldestQueuedSeconds ? ms(q.oldestQueuedSeconds * 1000) : "–"}
                </td>
                <td className="border-b border-rule/60 px-3 py-3 text-right">
                  {can("apply:reversible") && !q.topic && (
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={q.paused ? `Resume ${q.name}` : `Pause ${q.name}`}
                      title={q.paused ? "Resume" : "Pause"}
                      onClick={() => toggle.mutate(q)}
                    >
                      {q.paused ? <Play /> : <Pause />}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
        {stats.isSuccess && visible.length === 0 && (
          <p className="px-5 py-8 text-center text-base text-ink-3">No queues yet. They appear the first time an app sends a job.</p>
        )}
        {system.length > 0 && (
          <p className="border-t border-rule px-4 py-2.5 text-xs text-ink-3">
            Behind the scenes:{" "}
            {system.map((q) => (
              <span key={q.name} className="mr-3 font-mono">
                {q.name} {num(q.completedLastHour)} done{q.dead ? `, ${q.dead} dead` : ""}
              </span>
            ))}
          </p>
        )}
      </section>

      <div className="mt-12 grid gap-10 lg:grid-cols-2">
        <DeadLetters project={project} dead={dead} />
        <div className="flex flex-col gap-10">
          <section aria-labelledby="crons">
            <h2 id="crons" className="display-italic mb-3 text-xl text-ink">
              Schedules
            </h2>
            {(crons.data ?? []).length === 0 ? (
              <p className="text-base text-ink-3">
                No crons. Add them under <code className="font-mono text-ink">crons</code> in tiffin.config.ts.
              </p>
            ) : (
              <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
                {(crons.data ?? []).map((c) => (
                  <li key={c.name} className="flex items-center gap-3 px-4 py-3">
                    <CalendarClock className="size-4 shrink-0 text-ink-3" />
                    <span className="min-w-0 flex-1">
                      <span className="block text-base text-ink">
                        <code className="font-mono text-[0.8125rem]">{c.name}</code> <span className="text-ink-3">· {cronWords(c.schedule)}</span>
                      </span>
                      <span className="block truncate text-xs text-ink-3">
                        next {relative(c.nextAt)} · {c.lastAt ? `last ${relative(c.lastAt)}, ${c.lastState}` : "never ran"} ·{" "}
                        <code className="font-mono">{c.target}</code>
                      </span>
                    </span>
                    {can("apply:reversible") && (
                      <Button size="sm" variant="ghost" onClick={() => trigger.mutate(c.name)} disabled={trigger.isPending}>
                        <Zap />
                        Run now
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section aria-labelledby="topics">
            <h2 id="topics" className="display-italic mb-3 text-xl text-ink">
              Topics
            </h2>
            {(topics.data ?? []).length === 0 ? (
              <p className="text-base text-ink-3">No topics. A topic sends one message to every app that subscribes.</p>
            ) : (
              <ul className="flex flex-col gap-3">
                {(topics.data ?? []).map((t) => (
                  <li key={t.name} className="rounded-xl border border-rule bg-raised/60 px-4 py-3">
                    <p className="flex items-center gap-2 font-mono text-[0.8125rem] text-ink">
                      <Radio className="size-3.5 text-ink-3" />
                      {t.name}
                    </p>
                    <ul className="mt-2 flex flex-col gap-1 pl-5.5">
                      {(t.subscriptions ?? []).map((s) => (
                        <li key={s.name} className="text-sm text-ink-3">
                          → <span className="text-ink-2">{s.name}</span>{" "}
                          <code className="font-mono text-xs">{s.app ? `${s.app}${s.path}` : s.url}</code>
                        </li>
                      ))}
                    </ul>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>
    </Page>
  );
}

function Fact({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "irr" }) {
  return (
    <div className="bg-raised px-4 py-4">
      <dt className="text-2xs font-medium tracking-wider text-ink-3 uppercase">{label}</dt>
      <dd className={cn("display mt-1.5 text-2xl tnum", tone === "irr" ? "text-irr" : "text-ink")}>{value}</dd>
      {sub && <dd className="mt-0.5 text-xs text-ink-3">{sub}</dd>}
    </div>
  );
}

function Num({ v, tone }: { v: number; tone?: "brass" | "out" | "irr" }) {
  return (
    <td
      className={cn(
        "border-b border-rule/60 px-4 py-3 text-right font-mono text-[0.8125rem] tnum",
        v === 0
          ? "text-ink-4"
          : tone === "irr"
            ? "font-medium text-irr"
            : tone === "out"
              ? "text-out"
              : tone === "brass"
                ? "text-brass-ink"
                : "text-ink",
      )}
    >
      {num(v)}
    </td>
  );
}

function DeadLetters({ project, dead }: { project: string; dead: number }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const list = useQuery({ queryKey: ["jobs", project, "", "dead"], queryFn: () => mod2.jobs(project, { state: "dead" }), refetchInterval: 15_000 });
  const [preview, setPreview] = useState<{ count: number; message: string } | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const dry = useMutation({ mutationFn: () => mod2.replay(project, true), onSuccess: (r) => setPreview(r) });
  const real = useMutation({
    mutationFn: () => mod2.replay(project, false),
    onSuccess: (r) => {
      setPreview(null);
      setDone(r.message);
      qc.invalidateQueries({ queryKey: ["jobs", project] });
      qc.invalidateQueries({ queryKey: ["queue-stats", project] });
    },
  });
  const jobs = list.data ?? [];
  return (
    <section aria-labelledby="dlq">
      <div className="mb-3 flex items-baseline justify-between gap-3">
        <h2 id="dlq" className="display-italic text-xl text-ink">
          Dead letters
        </h2>
        {can("apply:reversible") && dead > 0 && !preview && (
          <Button size="sm" onClick={() => dry.mutate()} disabled={dry.isPending}>
            <RotateCcw />
            Replay…
          </Button>
        )}
      </div>
      {preview && (
        <div className="mb-3 animate-pop rounded-xl border border-rule bg-raised p-4">
          <p className="text-base text-ink">{sentence(preview.message)}</p>
          <p className="mt-1 text-sm text-ink-3">A dry run: nothing was sent. Replaying gives each job a fresh set of attempts.</p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="primary" onClick={() => real.mutate()} disabled={real.isPending || preview.count === 0}>
              Replay {num(preview.count)} {preview.count === 1 ? "job" : "jobs"}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setPreview(null)}>
              Not now
            </Button>
          </div>
        </div>
      )}
      {done && <p className="mb-3 rounded-lg bg-rev-wash px-3 py-2 text-sm text-ink">{sentence(done)}</p>}
      {(dry.isError || real.isError) && <ProblemNote className="mb-3" error={dry.error ?? real.error} />}
      {jobs.length === 0 ? (
        <p className="rounded-xl border border-dashed border-rule-strong px-4 py-5 text-base text-ink-3">
          None. Jobs land here after their last attempt fails.
        </p>
      ) : (
        <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-irr-rule/60 bg-raised/60">
          {jobs.slice(0, 8).map((j) => (
            <li key={j.id}>
              <Link to="/projects/$project/queues/jobs/$id" params={{ project, id: j.id }} className="block px-4 py-3 hover:bg-hover/50">
                <span className="flex items-center gap-2 text-sm">
                  <StateDot state="dead" />
                  <code className="font-mono text-xs text-ink">{j.queue}</code>
                  <span className="text-ink-3">
                    · {j.attempt} of {j.maxAttempts} attempts · {relative(j.finishedAt ?? j.enqueuedAt)}
                  </span>
                </span>
                <ErrorText e={j.lastError} className="mt-1 block truncate pl-4 font-mono text-xs text-irr" />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// ------------------------------------------------------------------ jobs

const jobStates: QueueJob["state"][] = ["queued", "running", "retrying", "scheduled", "completed", "dead", "cancelled"];

export function JobsPage({ project, queue, state }: { project: string; queue?: string; state?: string }) {
  useTitle(`${project} · Jobs`);
  const navigate = useNavigate();
  const st = jobStates.includes(state as QueueJob["state"]) ? (state as QueueJob["state"]) : undefined;
  const list = useQuery({
    queryKey: ["jobs", project, queue ?? "", st ?? ""],
    queryFn: () => mod2.jobs(project, { queue, state: st }),
    refetchInterval: 5000,
  });
  const set = (o: { queue?: string; state?: string }) =>
    navigate({ to: "/projects/$project/queues/jobs", params: { project }, search: { queue, state: st, ...o } });
  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Queues" />;
  const jobs = list.data ?? [];
  return (
    <Page full>
      <Header
        project={project}
        title="Jobs"
        lede={
          queue ? (
            <>
              Jobs on <code className="font-mono text-ink">{queue}</code>, newest first.
            </>
          ) : (
            "Every job, newest first."
          )
        }
      />
      <div className="mt-6 flex flex-wrap items-center gap-1.5">
        <Chip on={!st} onClick={() => set({ state: undefined })}>
          All
        </Chip>
        {jobStates.map((s) => (
          <Chip key={s} on={st === s} onClick={() => set({ state: s })}>
            <StateDot state={s} />
            {s}
          </Chip>
        ))}
        {queue && (
          <button
            onClick={() => set({ queue: undefined })}
            className="ml-2 inline-flex h-7 items-center gap-1.5 rounded-full border border-dashed border-rule-strong px-3 font-mono text-xs text-ink-2 hover:text-ink"
          >
            {queue} <X className="size-3" aria-label="Show every queue" />
          </button>
        )}
      </div>
      <div className="mt-4 overflow-hidden rounded-xl border border-rule bg-raised/60">
        {list.isPending && <Skeleton className="m-4 h-40" />}
        {list.isError && <ProblemNote className="m-4" error={list.error} />}
        <ul className="divide-y divide-rule/70">
          {jobs.map((j) => (
            <li key={j.id}>
              <Link
                to="/projects/$project/queues/jobs/$id"
                params={{ project, id: j.id }}
                className="grid grid-cols-[1rem_minmax(0,1fr)_auto] items-center gap-x-3 px-4 py-2.5 hover:bg-hover/50 sm:grid-cols-[1rem_minmax(0,16rem)_minmax(0,1fr)_7rem_7rem]"
              >
                <StateDot state={j.state} />
                <code className="font-mono text-xs text-ink">{queue ? j.id : j.queue}</code>
                <span className={cn("hidden min-w-0 truncate font-mono text-xs sm:block", j.state === "dead" ? "text-irr" : "text-ink-3")}>
                  {j.lastError ? (
                    <ErrorText e={j.lastError} />
                  ) : (
                    [queue ? "" : j.id, j.key && `key ${j.key}`, j.startedAt && j.finishedAt && `ran ${ms(new Date(j.finishedAt).getTime() - new Date(j.startedAt).getTime())}`]
                      .filter(Boolean)
                      .join(" · ")
                  )}
                </span>
                <span className={cn("text-right text-xs", jobTone[j.state])}>
                  {j.state}
                  {j.attempt > 1 ? ` · try ${j.attempt}` : ""}
                </span>
                <span className="hidden text-right text-xs text-ink-3 sm:block" title={full(j.enqueuedAt)}>
                  {relative(j.enqueuedAt)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
        {list.isSuccess && jobs.length === 0 && <p className="px-5 py-10 text-center text-base text-ink-3">No jobs match.</p>}
      </div>
    </Page>
  );
}

function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      onClick={onClick}
      aria-pressed={on}
      className={cn(
        "inline-flex h-7 items-center gap-1.5 rounded-full border px-3 text-sm capitalize transition-colors",
        on ? "border-ink bg-ink text-on-ink" : "border-rule text-ink-2 hover:border-rule-strong hover:text-ink",
      )}
    >
      {children}
    </button>
  );
}

export function JobPage({ project, id }: { project: string; id: string }) {
  useTitle(`${id} · Jobs`);
  const qc = useQueryClient();
  const { can } = useMe();
  const j = useQuery({
    queryKey: ["job", project, id],
    queryFn: () => mod2.job(project, id),
    refetchInterval: (q) => (["completed", "dead", "cancelled"].includes(q.state.data?.state ?? "") ? false : 3000),
  });
  const act = useMutation({
    mutationFn: (a: "retry" | "cancel") => (a === "retry" ? mod2.retryJob(project, id) : mod2.cancelJob(project, id)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["job", project, id] }),
  });
  if (j.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-80" />
      </Page>
    );
  if (j.isError)
    return (
      <Page wide>
        <ProblemNote error={j.error} />
      </Page>
    );
  const d = j.data;
  return (
    <Page wide>
      <Link
        to="/projects/$project/queues/jobs"
        params={{ project }}
        search={{ queue: d.queue }}
        className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink"
      >
        <ArrowLeft className="size-3.5" /> {d.queue}
      </Link>
      <header className="mt-5 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className={cn("flex items-center gap-2 text-sm capitalize", jobTone[d.state])}>
            <StateDot state={d.state} /> {d.state}
            {d.kind !== "job" && <span className="text-ink-3 normal-case">· {d.kind}</span>}
          </p>
          <h1 className="mt-2 font-mono text-2xl text-ink">{d.id}</h1>
          <p className="mt-1 text-sm text-ink-3">
            to <code className="font-mono">{d.target}</code> · sent {relative(d.enqueuedAt)}
            {d.enqueuedBy ? ` by ${d.enqueuedBy}` : ""} · {d.priority} priority{d.key ? ` · key ${d.key}` : ""}
          </p>
        </div>
        {can("apply:reversible") && (
          <div className="flex gap-2">
            {["dead", "completed", "cancelled", "retrying", "scheduled"].includes(d.state) && (
              <Button onClick={() => act.mutate("retry")} disabled={act.isPending}>
                <RotateCcw />
                {d.state === "completed" ? "Run again" : "Retry now"}
              </Button>
            )}
            {["queued", "scheduled", "retrying", "running"].includes(d.state) && (
              <Button variant="ghost" onClick={() => act.mutate("cancel")} disabled={act.isPending}>
                <CircleSlash />
                Cancel
              </Button>
            )}
          </div>
        )}
      </header>
      {act.isError && <ProblemNote className="mt-4" error={act.error} />}
      {d.lastError && (
        <p className="mt-6 rounded-lg border border-irr-rule bg-irr-wash px-4 py-3 font-mono text-sm break-words text-ink"><ErrorText e={d.lastError} /></p>
      )}
      <div className="mt-8 grid gap-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <section>
          <h2 className="display-italic mb-3 text-xl text-ink">Attempts</h2>
          {(d.attempts ?? []).length === 0 ? (
            <p className="text-base text-ink-3">{d.state === "scheduled" ? `Due ${relative(d.runAt)}.` : "None yet."}</p>
          ) : (
            <ol className="relative ml-1 border-l border-rule pl-5">
              {(d.attempts ?? []).map((a) => (
                <li key={a.attempt} className="relative pb-5">
                  <span
                    className={cn(
                      "absolute top-1.5 -left-[1.6rem] size-2.5 rounded-full ring-4 ring-paper",
                      a.outcome === "ok" ? "bg-rev" : a.outcome === "dead" ? "bg-irr" : "bg-out",
                    )}
                  />
                  <p className="text-base text-ink">
                    Attempt {a.attempt}: {a.outcome === "ok" ? "done" : a.outcome}
                    <span className="text-sm text-ink-3">
                      {" "}
                      · {ms(a.durationMs)}
                      {a.status ? ` · HTTP ${a.status}` : ""}
                    </span>
                  </p>
                  <p className="text-xs text-ink-3" title={full(a.startedAt)}>
                    {clock(a.startedAt)} · {relative(a.startedAt)}
                    {a.release && <> · {a.release.slice(0, 14)}…</>}
                  </p>
                  {a.error && <p className="mt-1 font-mono text-xs break-words text-irr"><ErrorText e={a.error} /></p>}
                </li>
              ))}
            </ol>
          )}
        </section>
        <section className="flex flex-col gap-6">
          <Json title="Payload" value={d.payload} label="Sent by your app. Shown as plain text." />
          {d.output !== undefined && <Json title="Output" value={d.output} label="Returned by your app. Shown as plain text." />}
        </section>
      </div>
    </Page>
  );
}

function Json({ title, value, label }: { title: string; value: unknown; label: string }) {
  return (
    <div>
      <h2 className="display-italic mb-3 text-xl text-ink">{title}</h2>
      <Untrusted label={label}>
        <pre className="max-h-80 overflow-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">
          {value === undefined ? "(none)" : JSON.stringify(value, null, 2)}
        </pre>
      </Untrusted>
    </div>
  );
}

// ------------------------------------------------------------------ workflows

const runStates: WorkflowRun["state"][] = ["running", "waiting", "completed", "failed", "cancelled"];

export function WorkflowsPage({ project, state }: { project: string; state?: string }) {
  useTitle(`${project} · Workflows`);
  const navigate = useNavigate();
  const st = runStates.includes(state as WorkflowRun["state"]) ? (state as WorkflowRun["state"]) : undefined;
  const runs = useQuery(mq.runs(project, st));
  const approvals = useQuery(mq.wfApprovals(project));
  if (runs.isError && notOnBox(runs.error)) return <NotOnBox what="Workflows" />;
  const waiting = (approvals.data ?? []).filter((a) => a.state === "waiting");
  const list = runs.data ?? [];
  return (
    <Page full>
      <Header
        project={project}
        title="Workflows"
        lede="Durable runs that sleep for days, wait for events and ask people, picking up exactly where they left off."
      />
      {waiting.length > 0 && <WaitingApprovals project={project} list={waiting} />}
      <div className="mt-6 flex flex-wrap items-center gap-1.5">
        <Chip on={!st} onClick={() => navigate({ to: "/projects/$project/workflows", params: { project }, search: {} })}>
          All
        </Chip>
        {runStates.map((s) => (
          <Chip key={s} on={st === s} onClick={() => navigate({ to: "/projects/$project/workflows", params: { project }, search: { state: s } })}>
            <StateDot state={s} />
            {s}
          </Chip>
        ))}
      </div>
      <ul className="mt-4 divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
        {runs.isPending && <Skeleton className="m-4 h-32" />}
        {list.map((r) => (
          <li key={r.id}>
            <Link
              to="/projects/$project/workflows/$id"
              params={{ project, id: r.id }}
              className="grid grid-cols-[1rem_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-0.5 px-4 py-3 hover:bg-hover/50 sm:px-5"
            >
              <StateDot state={r.state} />
              <span className="min-w-0">
                <span className="flex items-baseline gap-2">
                  <span className="font-mono text-[0.8125rem] text-ink">{r.workflow}</span>
                  <span className="truncate font-mono text-xs text-ink-4">{r.idempotencyKey ?? r.id}</span>
                </span>
                <span className={cn("block truncate text-sm", r.state === "failed" ? "text-irr" : r.state === "waiting" ? "text-out" : "text-ink-3")}>
                  {r.state === "waiting"
                    ? `Waiting for ${r.waitingFor}`
                    : r.state === "failed"
                      ? jobError(r.error)?.text
                      : r.state === "completed"
                        ? `Finished ${relative(r.finishedAt ?? r.updatedAt)} in ${r.turns} turns`
                        : r.state}
                </span>
              </span>
              <span className="text-right text-xs text-ink-3" title={full(r.createdAt)}>
                started {relative(r.createdAt)}
              </span>
            </Link>
          </li>
        ))}
      </ul>
      {runs.isSuccess && list.length === 0 && (
        <Empty className="mt-4" icon={<Hourglass />} title="No runs">
          Start one from your app with <code className="font-mono text-ink">workflow.start()</code>, or{" "}
          <code className="font-mono text-ink">tiffin workflows start</code>.
        </Empty>
      )}
    </Page>
  );
}

function WaitingApprovals({ project, list }: { project: string; list: WorkflowApproval[] }) {
  return (
    <section className="mt-8 rounded-xl border border-out/40 bg-out-wash p-4" aria-labelledby="wa">
      <h2 id="wa" className="flex items-center gap-2 text-base font-medium text-ink">
        <Stamp className="size-4 text-out" />
        {list.length === 1 ? "A run is waiting for a person" : `${list.length} runs are waiting for a person`}
      </h2>
      <ul className="mt-3 flex flex-col gap-2">
        {list.map((a) => (
          <li key={a.id}>
            <ApprovalCard project={project} a={a} />
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Decide a workflow's human approval inline. Human-only steps refuse agent tokens. */
export function ApprovalCard({ project, a, compact }: { project: string; a: WorkflowApproval; compact?: boolean }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [comment, setComment] = useState("");
  const decide = useMutation({
    mutationFn: (d: "approve" | "reject") => mod2.decide(project, a.id, d, comment.trim() || undefined),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["wf-approvals", project] });
      qc.invalidateQueries({ queryKey: ["runs", project] });
      qc.invalidateQueries({ queryKey: ["run", project, a.runId] });
    },
  });
  return (
    <div className="rounded-lg border border-rule bg-raised px-4 py-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start">
        <div className="min-w-0 flex-1">
          <p className="text-md font-medium text-ink">{a.title || a.step}</p>
          {a.description && <p className="mt-0.5 text-sm text-ink-2">{a.description}</p>}
          <p className="mt-1 text-xs text-ink-3">
            <Link to="/projects/$project/workflows/$id" params={{ project, id: a.runId }} className="font-mono hover:text-ink">
              {a.workflow}
            </Link>{" "}
            · step “{a.step}” · asked {relative(a.createdAt)}
            {a.timeoutAt && ` · times out ${relative(a.timeoutAt)}`}
            {a.humanOnly && " · people only"}
          </p>
        </div>
        {can("apply:reversible") && (
          <div className="flex shrink-0 flex-col gap-2 sm:w-64">
            {!compact && (
              <Input
                value={comment}
                onChange={(e) => setComment(e.target.value)}
                placeholder="Note (optional)"
                className="h-8 text-sm"
                aria-label="Note for the run"
              />
            )}
            <div className="flex gap-2">
              <Button size="sm" variant="primary" className="flex-1" onClick={() => decide.mutate("approve")} disabled={decide.isPending}>
                <Check />
                Approve
              </Button>
              <Button size="sm" variant="ghost" onClick={() => decide.mutate("reject")} disabled={decide.isPending}>
                Reject
              </Button>
            </div>
          </div>
        )}
      </div>
      {decide.isError && <ProblemNote className="mt-2" error={decide.error} />}
    </div>
  );
}

const stepIcon: Record<WorkflowStep["kind"], ReactNode> = {
  step: <Check className="size-3.5" />,
  sleep: <Moon className="size-3.5" />,
  event: <Radio className="size-3.5" />,
  approval: <Stamp className="size-3.5" />,
  webhook: <Link2 className="size-3.5" />,
  patch: <Zap className="size-3.5" />,
};

export function RunPage({ project, id }: { project: string; id: string }) {
  const qc = useQueryClient();
  const r = useQuery(mq.run(project, id));
  const approvals = useQuery(mq.wfApprovals(project));
  const { can } = useMe();
  const [eventName, setEventName] = useState("");
  const [payload, setPayload] = useState("");
  useTitle(r.data ? `${r.data.workflow} · Workflows` : "Workflow run");
  const send = useMutation({
    mutationFn: () => {
      let p: unknown = undefined;
      if (payload.trim()) p = JSON.parse(payload);
      return mod2.sendEvent(project, eventName.trim(), p);
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["run", project, id] }),
  });
  const act = useMutation({
    mutationFn: (a: "cancel" | "retry") => (a === "cancel" ? mod2.cancelRun(project, id) : mod2.retryRun(project, id)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["run", project, id] }),
  });
  if (r.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-80" />
      </Page>
    );
  if (r.isError)
    return (
      <Page wide>
        <ProblemNote error={r.error} title={r.error instanceof ApiError && r.error.status === 404 ? "There's no run with that ID" : undefined} />
      </Page>
    );
  const run = r.data;
  const steps = run.steps ?? [];
  const t0 = new Date(run.createdAt).getTime();
  const tEnd = run.finishedAt
    ? new Date(run.finishedAt).getTime()
    : Math.max(t0 + 1, ...steps.map((s) => new Date(s.finishedAt ?? s.startedAt).getTime()), new Date(run.updatedAt).getTime());
  const span = Math.max(1, tEnd - t0);
  const waitingApproval = (approvals.data ?? []).find((a) => a.runId === id && a.state === "waiting");
  const waitingEvent = steps.find((s) => s.kind === "event" && s.state === "waiting")?.event;

  return (
    <Page wide>
      <Link
        to="/projects/$project/workflows"
        params={{ project }}
        search={{}}
        className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink"
      >
        <ArrowLeft className="size-3.5" /> Workflows
      </Link>
      <header className="mt-5 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <p className="flex items-center gap-2 text-sm text-ink-3 capitalize">
            <StateDot state={run.state} /> {run.state}
            <span className="normal-case">
              · {run.turns} {run.turns === 1 ? "turn" : "turns"} · on {run.app}
            </span>
          </p>
          <h1 className="display mt-2 text-3xl text-ink">{run.workflow}</h1>
          <p className="mt-1 truncate font-mono text-xs text-ink-3">
            {run.id}
            {run.release && ` · pinned to ${run.release}`}
          </p>
        </div>
        {can("apply:reversible") && (
          <div className="flex gap-2">
            {run.state === "failed" && (
              <Button onClick={() => act.mutate("retry")} disabled={act.isPending}>
                <RotateCcw />
                Retry from the failed step
              </Button>
            )}
            {(run.state === "running" || run.state === "waiting") && (
              <Button variant="ghost" onClick={() => act.mutate("cancel")} disabled={act.isPending}>
                <CircleSlash />
                Cancel run
              </Button>
            )}
          </div>
        )}
      </header>
      {act.isError && <ProblemNote className="mt-4" error={act.error} />}
      {run.state === "failed" && run.error && (
        <p className="mt-6 rounded-lg border border-irr-rule bg-irr-wash px-4 py-3 font-mono text-sm text-ink"><ErrorText e={run.error} /></p>
      )}
      {waitingApproval && (
        <div className="mt-6">
          <ApprovalCard project={project} a={waitingApproval} />
        </div>
      )}

      <section className="mt-10" aria-labelledby="tl">
        <div className="mb-4 flex items-baseline justify-between">
          <h2 id="tl" className="display-italic text-xl text-ink">
            Steps
          </h2>
          <span className="font-mono text-xs text-ink-3 tnum">{ms(span)} so far</span>
        </div>
        <ol className="relative">
          {steps.map((s, k) => {
            const start = new Date(s.startedAt).getTime() - t0;
            const dur =
              s.durationMs ??
              (s.finishedAt ? new Date(s.finishedAt).getTime() - new Date(s.startedAt).getTime() : r.dataUpdatedAt - new Date(s.startedAt).getTime());
            const left = (start / span) * 100;
            const width = Math.max(0.6, (dur / span) * 100);
            const waiting = s.state === "waiting";
            return (
              <li
                key={s.seq}
                className="group relative grid animate-rise grid-cols-[1.75rem_minmax(0,1fr)] gap-x-3 pb-5"
                style={{ animationDelay: `${k * 50}ms` }}
              >
                {k < steps.length - 1 && <span aria-hidden className="absolute top-7 bottom-0 left-[0.8125rem] w-px bg-rule" />}
                <span
                  className={cn(
                    "relative z-[1] grid size-7 place-items-center rounded-full border",
                    s.state === "completed"
                      ? "border-rule bg-raised text-ink-2"
                      : s.state === "failed"
                        ? "border-irr-rule bg-irr-wash text-irr"
                        : waiting
                          ? "border-out/50 bg-out-wash text-out"
                          : "border-rule bg-raised text-ink-4",
                  )}
                  aria-label={`${s.kind}, ${s.state}`}
                >
                  {s.state === "failed" ? <X className="size-3.5" /> : stepIcon[s.kind]}
                </span>
                <div className="min-w-0">
                  <div className="flex flex-wrap items-baseline gap-x-2">
                    <span className="text-md font-medium text-ink">{s.title ?? s.name}</span>
                    <span className="text-xs text-ink-3">
                      {s.kind}
                      {s.attempts > 1 ? ` · ${s.attempts} attempts` : ""}
                    </span>
                    <span className={cn("ml-auto font-mono text-xs tnum", waiting ? "text-out" : "text-ink-3")}>
                      +{ms(start)} · {waiting ? (s.waitUntil ? `until ${relative(s.waitUntil)}` : "waiting") : ms(dur)}
                    </span>
                  </div>
                  <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-hover" aria-hidden>
                    <div
                      className={cn(
                        "h-full rounded-full",
                        s.state === "failed"
                          ? "bg-irr"
                          : waiting
                            ? "animate-pulse bg-out"
                            : s.kind === "sleep" || s.kind === "approval" || s.kind === "event"
                              ? "bg-ink-4"
                              : "bg-ink-2",
                      )}
                      style={{ marginLeft: `${left}%`, width: `${Math.min(width, 100 - left)}%` }}
                    />
                  </div>
                  {s.kind === "approval" && s.decidedBy && (
                    <p className="mt-1.5 text-sm text-ink-2">
                      {(s.output as { approved?: boolean })?.approved ? "Approved" : "Rejected"} by {s.decidedBy}
                      {(s.output as { comment?: string })?.comment ? `: “${(s.output as { comment?: string }).comment}”` : ""}
                    </p>
                  )}
                  {s.kind === "event" && <p className="mt-1.5 font-mono text-xs text-ink-3">event {s.event}</p>}
                  {s.error && <p className="mt-1.5 font-mono text-xs break-words text-irr"><ErrorText e={s.error} /></p>}
                  {s.output !== undefined && s.kind === "step" && (
                    <pre className="mt-1.5 truncate font-mono text-xs text-ink-3" title={JSON.stringify(s.output)}>
                      → {JSON.stringify(s.output)}
                    </pre>
                  )}
                </div>
              </li>
            );
          })}
          {run.state === "completed" && (
            <li className="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-x-3">
              <span className="grid size-7 place-items-center rounded-full bg-rev-wash text-rev">
                <Check className="size-3.5" />
              </span>
              <p className="pt-1 text-base text-ink">
                Finished {relative(run.finishedAt ?? run.updatedAt)}
                {run.output !== undefined && <code className="ml-2 font-mono text-xs text-ink-3">{JSON.stringify(run.output)}</code>}
              </p>
            </li>
          )}
        </ol>
      </section>

      {(waitingEvent || run.state === "waiting") && can("apply:reversible") && (
        <form
          className="mt-6 rounded-xl border border-rule bg-raised/60 p-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (eventName.trim()) send.mutate();
          }}
        >
          <p className="text-base font-medium text-ink">Send an event</p>
          <p className="text-sm text-ink-3">Wakes every run waiting for this name.{waitingEvent ? ` This one waits for ${waitingEvent}.` : ""}</p>
          <div className="mt-3 flex flex-col gap-2 sm:flex-row">
            <Input
              value={eventName}
              onChange={(e) => setEventName(e.target.value)}
              placeholder={waitingEvent ?? "payment-received"}
              className="font-mono sm:w-64"
              aria-label="Event name"
            />
            <Input
              value={payload}
              onChange={(e) => setPayload(e.target.value)}
              placeholder='{"amount": 1200}'
              className="font-mono"
              aria-label="Payload (JSON)"
            />
            <Button type="submit" disabled={!eventName.trim() || send.isPending}>
              <Send />
              Send
            </Button>
          </div>
          {send.data && <p className="mt-2 text-sm text-ink-2">{sentence(send.data.message)}</p>}
          {send.isError && <ProblemNote className="mt-2" error={send.error} />}
        </form>
      )}

      <div className="mt-10 grid gap-8 lg:grid-cols-2">
        <section>
          <h2 className="display-italic mb-3 text-xl text-ink">History</h2>
          <ol className="flex flex-col gap-2">
            {(run.timeline ?? []).map((t, i) => (
              <li key={i} className="grid grid-cols-[4.5rem_minmax(0,1fr)] gap-3 text-sm">
                <time className="font-mono text-xs text-ink-4 tnum" title={full(t.at)}>
                  {clock(t.at)}
                </time>
                <span className="text-ink-2">
                  {sentence(t.message)}
                  {t.actor && <span className="text-ink-3"> · {t.actor}</span>}
                </span>
              </li>
            ))}
          </ol>
        </section>
        <section className="flex flex-col gap-6">
          <Json title="Input" value={run.input} label="Sent by whoever started the run. Shown as plain text." />
        </section>
      </div>
      <p className="mt-8 flex items-center gap-2 text-xs text-ink-4">
        <Clock className="size-3.5" /> Refreshes every few seconds while you watch.
      </p>
    </Page>
  );
}
