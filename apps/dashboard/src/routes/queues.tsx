import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight } from "lucide-react";
import { useState, type ReactNode } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { mod2, mq, type QueueCron, type QueueJob, type QueueStats, type QueueTopic, type WorkflowApproval, type WorkflowRun, type WorkflowStep } from "@/api/modules";
import { q as api } from "@/api/queries";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import {
  EmptyJobs,
  ErrorText,
  FilterWords,
  jobError,
  jobFilters,
  jobState,
  runFilters,
  runWords,
  scheduleWords,
  StateSentence,
  toneClass,
  waitingFor,
} from "@/components/jobs-words";
import { Crumbs, Page, PageHeader, Skeleton, Tabs, Untrusted, NotOnBox } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote, sentence } from "@/components/problem";
import { Throttle } from "@/components/throttle";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { actorWords } from "@/lib/actors";
import { cn } from "@/lib/cn";
import { countWords, int, ms, pct, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { editKey, stage, unstage, useStaged } from "@/lib/staged";
import { clock, dayKey, dayLabel, full, relative } from "@/lib/time";

// ------------------------------------------------------------------ shared

function Header({ project, title, lede, actions, crumbs }: { project: string; title: ReactNode; lede?: ReactNode; actions?: ReactNode; crumbs?: Array<{ label: ReactNode; to?: string; params?: Record<string, string> }> }) {
  return (
    <PageHeader
      eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, ...(crumbs ?? [])]} />}
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

/** A small caps label over a group of rows. */
function Label({ children, id, action }: { children: ReactNode; id?: string; action?: ReactNode }) {
  return (
    <div className="mb-2.5 flex min-h-7 items-end justify-between gap-3">
      <h2 id={id} className="label">
        {children}
      </h2>
      {action}
    </div>
  );
}

const secFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
const clockSec = (iso: string) => secFmt.format(new Date(iso));

/** "owner (owner)" → "Owner", "claude-code (agent)" → "Claude Code", "workflow" → "a workflow". */
function whoWords(s?: string) {
  if (!s) return undefined;
  const m = s.match(/^(.*) \((\w+)\)$/);
  if (m) return actorWords({ kind: m[2], name: m[1] });
  if (s === "workflow") return "a workflow turn";
  if (s === "cron") return "a schedule";
  return s;
}

const elapsed = (a?: string, b?: string) => (a && b ? new Date(b).getTime() - new Date(a).getTime() : undefined);

// ------------------------------------------------------------------ queues

/** Concurrency detents. 0 means no limit; the throttle shows it as the last stop. */
const NO_LIMIT = 1000;
const STOPS = [1, 2, 4, 8, 16, 32, NO_LIMIT];
const toStop = (c: number) => (c <= 0 ? NO_LIMIT : c);
const fromStop = (s: number) => (s >= NO_LIMIT ? 0 : s);
const atOnce = (c: number) => (c <= 0 ? "no limit" : `${int(c)} at once`);
const letRun = (c: number) => (c <= 0 ? "any number of jobs" : `${int(c)} ${c === 1 ? "job" : "jobs"}`);

export function QueuesPage({ project }: { project: string }) {
  useTitle(`${project} · Queues`);
  const qc = useQueryClient();
  const { can } = useMe();
  const stats = useQuery({ queryKey: ["queue-stats", project], queryFn: () => mod2.queueStats(project), refetchInterval: 4000 });
  const crons = useQuery(mq.crons(project));
  const topics = useQuery(mq.topics(project));
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const edits = useStaged(project);
  const refresh = () => qc.invalidateQueries({ queryKey: ["queue-stats", project] });
  const toggle = useMutation({
    mutationFn: (x: QueueStats) => (x.paused ? mod2.resume(project, x.name) : mod2.pause(project, x.name)),
    onSuccess: (_, x) => {
      void refresh();
      toast({
        title: x.paused ? `Resumed ${x.name}.` : `Paused ${x.name}. New jobs wait until you resume it.`,
        detail: x.paused ? undefined : "Jobs already running finish where they are.",
        action: {
          label: "Undo",
          run: async () => {
            await (x.paused ? mod2.pause(project, x.name) : mod2.resume(project, x.name));
            await refresh();
          },
        },
      });
    },
    onError: (e) => toast({ title: "That didn’t work.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });

  if (stats.isError && notOnBox(stats.error)) return <NotOnBox what="Queues" />;
  const all = stats.data ?? [];
  const visible = all.filter((x) => !x.name.startsWith("_"));
  const system = all.filter((x) => x.name.startsWith("_"));
  const declared = manifest.data?.manifest.queues ?? {};

  const stageConcurrency = (x: QueueStats, stop: number) => {
    const c = fromStop(stop);
    if (declared[x.name]) {
      stage(project, {
        kind: "set",
        path: ["queues", x.name, "concurrency"],
        from: x.concurrency,
        to: c,
        what: `Let ${x.name} run ${letRun(c)} at once`,
        undo: `${x.name} goes back to ${letRun(x.concurrency)} at once`,
      });
      return;
    }
    const key = `set:queues/${x.name}`;
    if (c === x.concurrency) return unstage(project, key);
    stage(project, {
      kind: "set",
      path: ["queues", x.name],
      from: undefined,
      to: {
        app: x.app ?? "",
        path: x.path ?? `/queues/${x.name}`,
        concurrency: c,
        keyConcurrency: x.keyConcurrency,
        rateLimit: x.rateLimit,
        ratePeriodSeconds: x.ratePeriodSeconds,
        maxAttempts: x.maxAttempts,
        leaseSeconds: x.leaseSeconds,
      },
      what: `Declare ${x.name} in tiffin.config.ts and let it run ${letRun(c)} at once`,
      undo: `${x.name} leaves tiffin.config.ts again, which empties its waiting jobs, so the undo asks you first`,
    });
  };
  const stagedConcurrency = (name: string): number | undefined => {
    for (const e of edits) {
      if (e.kind !== "set") continue;
      const k = editKey(e);
      if (k === `set:queues/${name}/concurrency`) return e.to as number;
      if (k === `set:queues/${name}`) return (e.to as { concurrency: number }).concurrency;
    }
    return undefined;
  };

  return (
    <Page wide>
      <Header project={project} title="Queues" lede="Jobs your apps send, delivered to them with retries and backoff. A job that runs out of tries waits in dead letters." />
      {stats.isError && <ProblemNote className="mt-6" error={stats.error} />}
      {stats.isSuccess && visible.length > 0 && <StateSentence className="mt-8">{queuesSentence(all)}</StateSentence>}

      <section className="mt-8" aria-labelledby="queues">
        <h2 id="queues" className="label mb-2.5 md:sr-only">
          Queues
        </h2>
        <div aria-hidden className={cn("label hidden grid-cols-[minmax(0,1fr)_4.5rem_4.5rem_5.5rem_6rem_9.5rem_5.5rem] gap-x-4 pb-2", visible.length > 0 && "md:grid")}>
          <span>Queue</span>
          <span className="text-right">Waiting</span>
          <span className="text-right">Running</span>
          <span className="text-right">Done, hour</span>
          <span className="text-right">Failing</span>
          <span className="pl-1">At once</span>
          <span />
        </div>
        <ul className="divide-y divide-rule border-y border-rule-2">
          {stats.isPending && (
            <li className="py-3">
              <Skeleton className="h-24" />
            </li>
          )}
          {visible.map((x) => (
            <QueueRow
              key={x.name}
              project={project}
              x={x}
              topicSubs={(topics.data ?? []).find((t) => t.name === x.name)?.subscriptions?.length}
              staged={stagedConcurrency(x.name)}
              declared={!!declared[x.name]}
              canStage={can("apply:reversible") && manifest.isSuccess}
              canPause={can("apply:reversible")}
              onStage={(s) => stageConcurrency(x, s)}
              onToggle={() => toggle.mutate(x)}
              busy={toggle.isPending && toggle.variables?.name === x.name}
            />
          ))}
          {stats.isSuccess && visible.length === 0 && (
            <li>
              <EmptyJobs title="No queues yet.">
                A queue appears the first time an app sends a job: <code className="ident text-ink">queue.send("emails", …)</code>. Or declare one under{" "}
                <code className="ident text-ink">queues</code> in tiffin.config.ts.
              </EmptyJobs>
            </li>
          )}
        </ul>
        {system.length > 0 && <SystemLine project={project} system={system} />}
      </section>

      <div className="mt-14 grid gap-x-12 gap-y-12 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
        <DeadLetters project={project} stats={all} />
        <div className="flex flex-col gap-12">
          <Schedules project={project} list={crons.data} />
          <Topics list={topics.data} />
        </div>
      </div>
    </Page>
  );
}

function queuesSentence(all: QueueStats[]): string {
  const done = all.reduce((n, x) => n + x.completedLastHour, 0);
  const waiting = all.reduce((n, x) => n + x.queued + x.retrying, 0);
  const running = all.reduce((n, x) => n + x.running, 0);
  const dead = all.reduce((n, x) => n + x.dead, 0);
  const named = all.filter((x) => !x.name.startsWith("_"));
  const failing = named.filter((x) => x.failedAttemptsLastHour > 0 && x.failureRate >= 0.2).sort((a, b) => b.failureRate - a.failureRate);
  const paused = named.filter((x) => x.paused);
  const stale = named.filter((x) => x.oldestQueuedSeconds > 300).sort((a, b) => b.oldestQueuedSeconds - a.oldestQueuedSeconds);

  let first = done ? `${int(done)} ${done === 1 ? "job" : "jobs"} done in the last hour` : "Nothing ran in the last hour";
  if (running && waiting) first += `, ${words(running)} running and ${int(waiting)} waiting`;
  else if (running) first += `, ${words(running)} running now`;
  else if (waiting) first += `, ${int(waiting)} waiting`;
  first += ".";

  // [words, starts with a queue name (keeps its lower case)]
  const issues: Array<[string, boolean]> = [];
  if (failing[0]) issues.push([`${failing[0].name} is failing ${pct(failing[0].failureRate)} of its tries`, true]);
  if (stale[0]) issues.push([`the oldest job on ${stale[0].name} has waited ${ms(stale[0].oldestQueuedSeconds * 1000)}`, false]);
  if (dead) issues.push([`${countWords(dead, "job")} ${dead === 1 ? "is" : "are"} waiting in dead letters`, false]);
  if (paused.length) issues.push([`${paused.map((x) => x.name).join(" and ")} ${paused.length === 1 ? "is" : "are"} paused`, true]);
  if (!issues.length) return `${first} Nothing is stuck.`;
  const t = issues.map((i) => i[0]);
  let s = t.length === 1 ? t[0] : `${t.slice(0, -1).join(", ")}, and ${t[t.length - 1]}`;
  if (!issues[0][1]) s = s.charAt(0).toUpperCase() + s.slice(1);
  return `${first} ${s}.`;
}

function Count({ n, tone, className }: { n: number; tone?: "warn" | "danger"; className?: string }) {
  return (
    <span className={cn("text-right text-[0.875rem] tnum", n === 0 ? "text-ink-4" : tone === "danger" ? "text-danger" : tone === "warn" ? "text-warn-ink" : "text-ink", className)}>
      {int(n)}
    </span>
  );
}

function QueueRow({
  project,
  x,
  topicSubs,
  staged,
  declared,
  canStage,
  canPause,
  onStage,
  onToggle,
  busy,
}: {
  project: string;
  x: QueueStats;
  topicSubs?: number;
  staged?: number;
  declared: boolean;
  canStage: boolean;
  canPause: boolean;
  onStage: (stop: number) => void;
  onToggle: () => void;
  busy: boolean;
}) {
  const [preview, setPreview] = useState<number | null>(null);
  const waiting = x.queued + x.retrying;
  const shown = preview !== null ? fromStop(preview) : (staged ?? x.concurrency);
  const isStaged = staged !== undefined && staged !== x.concurrency;
  const failing = x.failedAttemptsLastHour > 0;
  const where = x.topic ? `topic, fans out to ${countWords(topicSubs ?? 0, "queue")}` : x.app ? `→ ${x.app} ${x.path ?? ""}` : "no app set";
  const lever =
    x.topic || !x.app ? (
      <span className="text-sm text-ink-4">–</span>
    ) : (
      <span className="flex items-center gap-3">
        {canStage ? (
          <span className="[&_.throttle]:w-[64px]">
            <Throttle
              size="mini"
              label={`${x.name}: jobs at once`}
              unit="at once"
              stops={STOPS}
              value={toStop(staged ?? x.concurrency)}
              applied={toStop(x.concurrency)}
              format={(n) => (n >= NO_LIMIT ? "no limit" : int(n))}
              onChange={setPreview}
              onCommit={(n) => {
                setPreview(null);
                onStage(n);
              }}
            />
          </span>
        ) : null}
        <span
          className={cn("text-[0.8125rem] whitespace-nowrap tnum", isStaged || preview !== null ? "font-[550] text-brass-ink" : "text-ink-2")}
          title={declared ? "Set in tiffin.config.ts" : "Not in tiffin.config.ts yet: changing it declares the queue there"}
        >
          {atOnce(shown)}
        </span>
      </span>
    );
  const pause =
    canPause && !x.topic ? (
      <Button size="sm" variant="ghost" onClick={onToggle} disabled={busy} aria-label={x.paused ? `Resume ${x.name}` : `Pause ${x.name}`}>
        {x.paused ? "Resume" : "Pause"}
      </Button>
    ) : null;

  return (
    <li className={cn("relative", isStaged && "before:absolute before:inset-y-0 before:-left-3 before:w-[2px] before:rounded-full before:bg-brass")}>
      {/* desktop: an instrument row */}
      <div className="hidden grid-cols-[minmax(0,1fr)_4.5rem_4.5rem_5.5rem_6rem_9.5rem_5.5rem] items-center gap-x-4 py-3 md:grid">
        <div className="min-w-0">
          <Link
            to="/projects/$project/queues/jobs"
            params={{ project }}
            search={{ queue: x.name }}
            className="group inline-flex max-w-full items-baseline gap-2"
            aria-label={`${x.name}: its jobs`}
          >
            <span className="ident truncate text-ink group-hover:underline group-hover:underline-offset-4">{x.name}</span>
            {x.paused && <span className="text-[0.8125rem] font-[550] text-warn-ink">paused</span>}
          </Link>
          <p className="mt-0.5 truncate text-xs text-ink-3">
            <span className={cn(!x.topic && x.app && "font-mono text-[0.7rem]")}>{where}</span>
            {x.p95Ms > 0 && <span> · p95 {ms(x.p95Ms)}</span>}
            {x.scheduled > 0 && <span> · {int(x.scheduled)} due later</span>}
          </p>
        </div>
        <span className="text-right">
          <Count n={waiting} tone={x.oldestQueuedSeconds > 300 ? "warn" : undefined} />
          {x.oldestQueuedSeconds > 300 && <span className="block text-xs text-warn-ink">oldest {ms(x.oldestQueuedSeconds * 1000)}</span>}
        </span>
        <Count n={x.running} />
        <Count n={x.completedLastHour} />
        <span className="text-right">
          {failing ? (
            <span className={cn("text-[0.875rem] tnum", x.failureRate >= 0.2 ? "text-danger" : "text-ink-2")}>{pct(x.failureRate)}</span>
          ) : (
            <span className="text-[0.875rem] text-ink-4">–</span>
          )}
          {x.dead > 0 && <span className="block text-xs text-danger">{int(x.dead)} gave up</span>}
        </span>
        <div className="pl-1">{lever}</div>
        <div className="flex justify-end">{pause}</div>
      </div>

      {/* phone: the same row, stacked; nothing scrolls sideways */}
      <div className="py-3 md:hidden">
        <div className="flex items-center gap-3">
          <Link to="/projects/$project/queues/jobs" params={{ project }} search={{ queue: x.name }} className="min-w-0 flex-1">
            <span className="flex items-baseline gap-2">
              <span className="ident truncate text-ink">{x.name}</span>
              {x.paused && <span className="text-[0.8125rem] font-[550] text-warn-ink">paused</span>}
            </span>
            <span className="mt-0.5 block truncate text-xs text-ink-3">{where}</span>
          </Link>
          {pause}
        </div>
        <p className="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[0.8125rem] text-ink-2 tnum">
          <span className={x.completedLastHour ? "" : "text-ink-3"}>{x.completedLastHour ? `${int(x.completedLastHour)} done this hour` : "Quiet this hour"}</span>
          {waiting > 0 && <span className={x.oldestQueuedSeconds > 300 ? "text-warn-ink" : ""}>{int(waiting)} waiting</span>}
          {x.running > 0 && <span>{int(x.running)} running</span>}
          {failing && <span className={x.failureRate >= 0.2 ? "text-danger" : ""}>{pct(x.failureRate)} of tries failing</span>}
          {x.dead > 0 && <span className="text-danger">{int(x.dead)} gave up</span>}
        </p>
        {!x.topic && x.app && <div className="mt-2">{lever}</div>}
      </div>
    </li>
  );
}

function SystemLine({ project, system }: { project: string; system: QueueStats[] }) {
  const done = system.reduce((n, x) => n + x.completedLastHour, 0);
  const dead = system.reduce((n, x) => n + x.dead, 0);
  return (
    <p className="mt-3 text-[0.8125rem] text-ink-3">
      Behind the scenes, schedules and workflow turns run on{" "}
      {system.map((x, i) => (
        <span key={x.name}>
          {i > 0 && " and "}
          <Link to="/projects/$project/queues/jobs" params={{ project }} search={{ queue: x.name }} className="ident text-[0.75rem] text-ink-2 hover:text-ink">
            {x.name}
          </Link>
        </span>
      ))}
      : {int(done)} done this hour{dead ? <span className="text-danger">, {int(dead)} gave up</span> : null}.
    </p>
  );
}

// ------------------------------------------------------------------ dead letters

function DeadLetters({ project, stats }: { project: string; stats: QueueStats[] }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const dead = stats.reduce((n, x) => n + x.dead, 0);
  const list = useQuery({ queryKey: ["jobs", project, "", "dead"], queryFn: () => mod2.jobs(project, { state: "dead" }), refetchInterval: 15_000 });
  const [preview, setPreview] = useState<{ count: number; message: string } | null>(null);
  const [emptying, setEmptying] = useState<string | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["jobs", project] });
    void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
  };
  const dry = useMutation({ mutationFn: () => mod2.replay(project, true), onSuccess: (r) => setPreview(r) });
  const real = useMutation({
    mutationFn: () => mod2.replay(project, false),
    onSuccess: (r) => {
      setPreview(null);
      refresh();
      toast({ title: `Replayed ${countWords(r.count, "job")}.`, detail: "Each one gets a fresh set of tries." });
    },
  });
  const retry = useMutation({
    mutationFn: (j: QueueJob) => mod2.retryJob(project, j.id),
    onSuccess: (_, j) => {
      refresh();
      toast({
        title: `Sent ${j.id} back to ${j.queue}.`,
        detail: "It gets a fresh set of tries.",
        action: {
          label: "Undo",
          run: async () => {
            await mod2.cancelJob(project, j.id);
            refresh();
          },
        },
      });
    },
    onError: (e) => toast({ title: "Couldn’t retry that job.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const jobs = list.data ?? [];
  const withDead = stats.filter((x) => x.dead > 0 && !x.name.startsWith("_"));
  return (
    <section aria-labelledby="dlq">
      <Label
        id="dlq"
        action={
          can("apply:reversible") && dead > 0 && !preview ? (
            <Button size="sm" onClick={() => dry.mutate()} disabled={dry.isPending}>
              Replay all…
            </Button>
          ) : undefined
        }
      >
        Dead letters{dead > 0 && <span className="ml-1.5 text-danger tnum">{int(dead)}</span>}
      </Label>
      {preview && (
        <div className="mb-4 animate-pop rounded-[10px] border border-rule-2 bg-paper-raised px-4 py-3.5 shadow-raised">
          <p className="text-[0.9375rem] text-ink">{sentence(preview.message)}</p>
          <p className="mt-1 text-sm text-ink-3">A dry run: nothing was sent yet. Each job gets a fresh set of tries.</p>
          <div className="mt-3 flex justify-end gap-2">
            <Button size="md" variant="ghost" onClick={() => setPreview(null)}>
              Not now
            </Button>
            <Button size="md" variant="primary" onClick={() => real.mutate()} disabled={real.isPending || preview.count === 0}>
              Replay {countWords(preview.count, "job")}
            </Button>
          </div>
        </div>
      )}
      {(dry.isError || real.isError) && <ProblemNote className="mb-3" error={dry.error ?? real.error} />}
      {jobs.length === 0 ? (
        <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">None. A job lands here after its last try fails.</p>
      ) : (
        <ul className="divide-y divide-rule border-y border-rule">
          {jobs.slice(0, 8).map((j) => {
            const err = jobError(j.lastError);
            return (
              <li key={j.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 py-3">
                <Link to="/projects/$project/queues/jobs/$id" params={{ project, id: j.id }} className="group min-w-0">
                  <span className="flex flex-wrap items-baseline gap-x-2 text-[0.8125rem] text-ink-3">
                    <span className="ident text-ink group-hover:underline group-hover:underline-offset-4">{j.runId ? "workflow run" : j.queue}</span>
                    <span>
                      {countWords(j.attempt, "try", "tries")} · {relative(j.finishedAt ?? j.enqueuedAt)}
                    </span>
                  </span>
                  {err && (
                    <span className="mt-1 block truncate font-mono text-[0.75rem] text-ink-2" title={err.text}>
                      {err.text}
                    </span>
                  )}
                </Link>
                {can("apply:reversible") && (
                  <Button size="sm" variant="ghost" onClick={() => retry.mutate(j)} disabled={retry.isPending && retry.variables?.id === j.id}>
                    Retry
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {jobs.length > 8 && (
        <Link to="/projects/$project/queues/jobs" params={{ project }} search={{ state: "dead" }} className="mt-2 inline-block text-[0.8125rem] font-[550] text-brass-ink hover:underline">
          All {int(jobs.length)} that gave up
        </Link>
      )}
      {can("apply:irreversible") && withDead.length > 0 && (
        <p className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-[0.8125rem] text-ink-3">
          {withDead.map((x) => (
            <button key={x.name} onClick={() => setEmptying(x.name)} className="text-danger hover:underline hover:underline-offset-4">
              Empty {x.name}…
            </button>
          ))}
        </p>
      )}
      <HazardDialog<{ count: number; message: string }, { count: number }>
        open={!!emptying}
        onOpenChange={(o) => !o && setEmptying(null)}
        title={`Empty ${emptying ?? ""}`}
        word={emptying ?? ""}
        action={`Delete the jobs on ${emptying ?? ""}`}
        run={async (confirm) => {
          const r = await mod2.purge(project, emptying!, confirm, true);
          if (!r.purged) throw new ApiError({ status: 428, code: "confirm", title: "Confirm", confirm: r.confirm, preview: { count: r.count, message: r.message } } as never);
          return r;
        }}
        renderPreview={(p) => (
          <p className="text-[0.9375rem] text-ink">
            Deletes the {countWords(p.count, "job")} waiting or dead on <code className="ident">{emptying}</code>, for good. Jobs already running finish. The queue
            itself stays.
          </p>
        )}
        onDone={(r) => {
          refresh();
          toast({ title: `Deleted ${countWords(r.count, "job")} from ${emptying}.` });
        }}
      />
    </section>
  );
}

// ------------------------------------------------------------------ schedules and topics

function Schedules({ project, list }: { project: string; list?: QueueCron[] }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const trigger = useMutation({
    mutationFn: (name: string) => mod2.triggerCron(project, name),
    onSuccess: (_, name) => {
      void qc.invalidateQueries({ queryKey: ["crons", project] });
      void qc.invalidateQueries({ queryKey: ["jobs", project] });
      toast({ title: `Started ${name} now.`, detail: "Its next scheduled run stays as it was." });
    },
    onError: (e) => toast({ title: "Couldn’t start it.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  return (
    <section aria-labelledby="crons">
      <Label id="crons">Schedules</Label>
      {!list ? (
        <Skeleton className="h-14" />
      ) : list.length === 0 ? (
        <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">
          None. Add one under <code className="ident text-ink">crons</code> in tiffin.config.ts, like{" "}
          <code className="ident text-ink">{`nightly: { schedule: "0 2 * * *", app: "worker" }`}</code>.
        </p>
      ) : (
        <ul className="divide-y divide-rule border-y border-rule">
          {list.map((c) => {
            const failed = c.lastState && c.lastState !== "completed" && c.lastState !== "running" && c.lastState !== "queued";
            return (
              <li key={c.name} className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 py-3">
                <div className="min-w-0">
                  <p className="text-[0.875rem] text-ink">
                    <span className="ident">{c.name}</span> <span className="text-ink-2">runs {scheduleWords(c.schedule, c.nextAt)}.</span>
                  </p>
                  <p className="mt-0.5 text-[0.8125rem] text-ink-3">
                    Next {relative(c.nextAt)}
                    {c.lastAt ? (
                      <>
                        {" · "}
                        {c.lastJob ? (
                          <Link to="/projects/$project/queues/jobs/$id" params={{ project, id: c.lastJob }} className={cn("hover:underline", failed && "text-danger")}>
                            {failed ? `last run ${c.lastState}` : "last ran"} {relative(c.lastAt)}
                          </Link>
                        ) : (
                          <>last ran {relative(c.lastAt)}</>
                        )}
                      </>
                    ) : (
                      " · hasn’t run yet"
                    )}
                    {" · "}
                    <code className="font-mono text-[0.7rem]" title={`${c.schedule} → ${c.target}`}>
                      {c.target}
                    </code>
                  </p>
                </div>
                {can("apply:reversible") && (
                  <Button size="sm" variant="ghost" onClick={() => trigger.mutate(c.name)} disabled={trigger.isPending}>
                    Run now
                  </Button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function Topics({ list }: { list?: QueueTopic[] }) {
  return (
    <section aria-labelledby="topics">
      <Label id="topics">Topics</Label>
      {(list ?? []).length === 0 ? (
        <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">None. A topic sends one message to every queue that subscribes.</p>
      ) : (
        <ul className="divide-y divide-rule border-y border-rule">
          {(list ?? []).map((t) => (
            <li key={t.name} className="py-3">
              <p className="ident text-ink">{t.name}</p>
              <ul className="mt-1 flex flex-col gap-0.5">
                {(t.subscriptions ?? []).map((s) => (
                  <li key={s.name} className="text-[0.8125rem] text-ink-3">
                    → <span className="text-ink-2">{s.name}</span> <code className="font-mono text-[0.7rem]">{s.app ? `${s.app} ${s.path}` : s.url}</code>
                  </li>
                ))}
                {(t.subscriptions ?? []).length === 0 && <li className="text-[0.8125rem] text-ink-3">No subscribers yet: messages go nowhere.</li>}
              </ul>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// ------------------------------------------------------------------ jobs

export function JobsPage({ project, queue, state }: { project: string; queue?: string; state?: string }) {
  useTitle(`${project} · Jobs`);
  const navigate = useNavigate();
  const st = jobFilters.some((f) => f.state === state) ? (state as QueueJob["state"]) : undefined;
  const list = useQuery({
    queryKey: ["jobs", project, queue ?? "", st ?? ""],
    queryFn: () => mod2.jobs(project, { queue, state: st }),
    refetchInterval: 5000,
  });
  const stats = useQuery({ queryKey: ["queue-stats", project], queryFn: () => mod2.queueStats(project), refetchInterval: 4000 });
  const [more, setMore] = useState(false);
  const set = (o: { queue?: string; state?: string }) => navigate({ to: "/projects/$project/queues/jobs", params: { project }, search: { queue, state: st, ...o } });
  if (list.isError && notOnBox(list.error)) return <NotOnBox what="Queues" />;
  const fetched = list.data ?? [];
  const jobs = more ? fetched : fetched.slice(0, 40);
  const qs = (stats.data ?? []).filter((x) => !queue || x.name === queue);
  const sum = (k: "queued" | "running" | "retrying" | "scheduled" | "dead") => qs.reduce((n, x) => n + x[k], 0);
  const counts = { queued: sum("queued"), running: sum("running"), retrying: sum("retrying"), scheduled: sum("scheduled"), dead: sum("dead") };
  const names = (stats.data ?? []).map((x) => x.name).sort((a, b) => Number(a.startsWith("_")) - Number(b.startsWith("_")) || a.localeCompare(b));

  return (
    <Page wide>
      <Header project={project} title="Jobs" lede={queue ? <>Every job on <code className="ident text-ink">{queue}</code>, newest first.</> : "Every job your apps sent, newest first."} />
      <div className="mt-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <FilterWords label="Show jobs that are" items={jobFilters} value={st} onPick={(s) => set({ state: s })} counts={counts} />
        <label className="flex items-center gap-2 text-[0.8125rem] text-ink-3">
          On
          <select
            value={queue ?? ""}
            onChange={(e) => set({ queue: e.target.value || undefined })}
            aria-label="Queue"
            className="h-8 rounded-[7px] border border-rule-2 bg-paper-raised px-2 pr-7 font-mono text-[0.78rem] text-ink"
          >
            <option value="">every queue</option>
            {names.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
      </div>
      {list.isError && <ProblemNote className="mt-4" error={list.error} />}
      <div className="mt-4 border-t border-rule-2">
        {list.isPending && <Skeleton className="mt-3 h-40" />}
        <ol>
          {jobs.map((j, i) => {
            const head = i === 0 || dayKey(j.enqueuedAt) !== dayKey(jobs[i - 1].enqueuedAt);
            const s = jobState(j);
            const ran = elapsed(j.startedAt, j.finishedAt);
            const err = jobError(j.lastError);
            const detail = err
              ? err.text
              : [j.runId ? "a workflow turn" : whoWords(j.enqueuedBy) && `sent by ${whoWords(j.enqueuedBy)}`, j.key && `key ${j.key}`, j.priority !== "normal" && `${j.priority} priority`]
                  .filter(Boolean)
                  .join(" · ");
            return (
              <li key={j.id}>
                {head && <p className="label pt-4 pb-1.5">{dayLabel(j.enqueuedAt)}</p>}
                <Link
                  to="/projects/$project/queues/jobs/$id"
                  params={{ project, id: j.id }}
                  className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 border-t border-rule py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk/60 sm:grid-cols-[4.75rem_9rem_minmax(0,1fr)_10rem_4.5rem] sm:px-2"
                >
                  <time className="hidden text-[0.8125rem] text-ink-3 tnum sm:block" dateTime={j.enqueuedAt} title={full(j.enqueuedAt)}>
                    {clockSec(j.enqueuedAt)}
                  </time>
                  <span className="min-w-0">
                    <span className="ident block truncate text-ink">{j.queue}</span>
                    <span className="block truncate text-xs text-ink-3 sm:hidden">
                      {j.id} · {clockSec(j.enqueuedAt)}
                      {ran !== undefined && ` · ran ${ms(ran)}`}
                    </span>
                  </span>
                  <span className={cn("hidden min-w-0 truncate text-[0.8125rem] sm:block", err ? "text-danger" : "text-ink-3")}>
                    <span className="ident mr-2 text-[0.75rem] text-ink-3">{j.id}</span>
                    {detail}
                  </span>
                  {/* Status only when it isn't fine: a finished job's row says nothing here. */}
                  <span className={cn("text-right text-[0.8125rem]", toneClass[s.tone])}>{j.state === "completed" ? <span className="sr-only">{s.word}</span> : s.word}</span>
                  <span className="hidden text-right text-[0.8125rem] text-ink-3 tnum sm:block">{ran !== undefined ? ms(ran) : ""}</span>
                </Link>
              </li>
            );
          })}
        </ol>
        {list.isSuccess && jobs.length === 0 && (
          <EmptyJobs title={st || queue ? "No jobs match." : "No jobs yet."} className="border-t border-rule">
            {st || queue ? (
              <>
                Nothing {jobFilters.find((f) => f.state === st)?.label.toLowerCase() ?? ""} {queue ? <>on <code className="ident">{queue}</code></> : null} right now.
              </>
            ) : (
              <>
                They show up the moment an app sends one: <code className="ident text-ink">queue.send("emails", …)</code>.
              </>
            )}
          </EmptyJobs>
        )}
        {fetched.length > jobs.length && (
          <div className="border-t border-rule py-3">
            <Button size="sm" variant="ghost" onClick={() => setMore(true)}>
              Show {int(fetched.length - jobs.length)} older
            </Button>
          </div>
        )}
        {more && fetched.length >= 100 && <p className="border-t border-rule py-3 text-[0.8125rem] text-ink-3">The newest 100. Narrow it with a state or a queue.</p>}
      </div>
    </Page>
  );
}

// ------------------------------------------------------------------ one job

export function JobPage({ project, id }: { project: string; id: string }) {
  useTitle(`${id} · Jobs`);
  const qc = useQueryClient();
  const { can } = useMe();
  const j = useQuery({
    queryKey: ["job", project, id],
    queryFn: () => mod2.job(project, id),
    refetchInterval: (q) => (["completed", "dead", "cancelled"].includes(q.state.data?.state ?? "") ? false : 3000),
  });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["job", project, id] });
    void qc.invalidateQueries({ queryKey: ["jobs", project] });
    void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
  };
  const act = useMutation({
    mutationFn: (a: "retry" | "cancel") => (a === "retry" ? mod2.retryJob(project, id) : mod2.cancelJob(project, id)),
    onSuccess: (_, a) => {
      refresh();
      toast({
        title: a === "retry" ? `Sent ${id} back to its queue.` : `Cancelled ${id}.`,
        detail: a === "retry" ? "It gets a fresh set of tries." : "It won’t run unless you send it again.",
        action: {
          label: "Undo",
          run: async () => {
            await (a === "retry" ? mod2.cancelJob(project, id) : mod2.retryJob(project, id));
            refresh();
          },
        },
      });
    },
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
        <ProblemNote error={j.error} title={j.error instanceof ApiError && j.error.status === 404 ? "There’s no job with that ID" : undefined} />
      </Page>
    );
  const d = j.data;
  const ran = elapsed(d.startedAt, d.finishedAt);
  const err = jobError(d.lastError);
  const tries = (n: number) => countWords(n, "try", "tries");
  let state: ReactNode;
  switch (d.state) {
    case "completed":
      state = `Done in ${ran !== undefined ? ms(ran) : "no time"} ${d.attempt > 1 ? `on the ${["", "first", "second", "third", "fourth", "fifth"][d.attempt] ?? `${d.attempt}th`} try` : "on the first try"}, ${relative(d.finishedAt ?? d.enqueuedAt)}.`;
      break;
    case "dead":
      state = `Gave up after ${tries(d.attempt)}, ${relative(d.finishedAt ?? d.enqueuedAt)}.`;
      break;
    case "retrying":
      state = `Failed ${words(d.attempt)} of ${int(d.maxAttempts)} tries so far; the next one is ${relative(d.runAt)}.`;
      break;
    case "scheduled":
      state = `Due at ${clock(d.runAt)}, ${relative(d.runAt)}.`;
      break;
    case "queued":
      state = `Waiting for a free slot; sent ${relative(d.enqueuedAt)}.`;
      break;
    case "running":
      state = (
        <span className="inline-flex items-center gap-3">
          <PilotLight state="busy" label="running" />
          Running now, try {int(d.attempt)} of {int(d.maxAttempts)}.
        </span>
      );
      break;
    case "cancelled":
      state = `Cancelled ${relative(d.finishedAt ?? d.enqueuedAt)}.`;
      break;
  }
  const attempts = d.attempts ?? [];
  const facts: Array<[string, ReactNode]> = [
    [
      "Queue",
      <Link key="q" to="/projects/$project/queues/jobs" params={{ project }} search={{ queue: d.queue }} className="ident hover:underline">
        {d.queue}
      </Link>,
    ],
    ["Delivered to", <code key="t" className="ident">{d.target}</code>],
    ...(d.enqueuedBy ? ([["Sent by", whoWords(d.enqueuedBy)]] as Array<[string, ReactNode]>) : []),
    ["Sent", <time key="s" dateTime={d.enqueuedAt} title={full(d.enqueuedAt)}>{`${clockSec(d.enqueuedAt)}, ${relative(d.enqueuedAt)}`}</time>],
    ...(d.runId
      ? ([
          [
            "Workflow run",
            <Link key="r" to="/projects/$project/workflows/$id" params={{ project, id: d.runId }} className="ident inline-flex items-center gap-1 text-brass-ink hover:underline">
              {d.runId.slice(0, 16)}… <ArrowUpRight className="size-3.5" />
            </Link>,
          ],
        ] as Array<[string, ReactNode]>)
      : []),
    ...(d.key ? ([["Key", <code key="k" className="ident">{d.key}</code>]] as Array<[string, ReactNode]>) : []),
    ...(d.priority !== "normal" ? ([["Priority", d.priority]] as Array<[string, ReactNode]>) : []),
    ["Tries", `${int(d.attempt)} of ${int(d.maxAttempts)}`],
    ...(d.release ? ([["Release", <code key="rel" className="ident text-ink-2">{d.release}</code>]] as Array<[string, ReactNode]>) : []),
  ];
  return (
    <Page wide>
      <PageHeader
        eyebrow={
          <Crumbs
            items={[
              { label: project, to: "/projects/$project", params: { project } },
              { label: "Jobs", to: "/projects/$project/queues/jobs", params: { project } },
            ]}
          />
        }
        title={<>A job on {d.queue}<span className="ident ml-2.5 align-middle text-[0.8125rem] font-normal tracking-normal text-ink-3">{d.id}</span></>}
        actions={
          can("apply:reversible") ? (
            <>
              {["queued", "scheduled", "retrying", "running"].includes(d.state) && (
                <Button variant="ghost" onClick={() => act.mutate("cancel")} disabled={act.isPending}>
                  Cancel job
                </Button>
              )}
              {["dead", "completed", "cancelled", "retrying", "scheduled"].includes(d.state) && (
                <Button onClick={() => act.mutate("retry")} disabled={act.isPending}>
                  {d.state === "completed" ? "Run it again" : d.state === "scheduled" ? "Run it now" : "Retry now"}
                </Button>
              )}
            </>
          ) : undefined
        }
      />
      <StateSentence className="mt-5">{state}</StateSentence>
      {act.isError && <ProblemNote className="mt-4" error={act.error} />}
      {err && (
        <div className="mt-4 max-w-[48rem] rounded-[10px] border border-danger-rule bg-danger-wash px-4 py-3">
          <p className="font-mono text-[0.8125rem] break-words text-ink">{err.text}</p>
          {err.gaveUp && <p className="mt-1 text-[0.8125rem] text-ink-2">The app answered “don’t retry”, so Tiffin stopped there.</p>}
        </div>
      )}

      <div className="mt-10 grid gap-x-12 gap-y-10 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="flex flex-col gap-10">
          <section aria-labelledby="facts">
            <Label id="facts">About this job</Label>
            <dl className="divide-y divide-rule border-y border-rule">
              {facts.map(([k, v]) => (
                <div key={k} className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-x-4 py-2 text-[0.84375rem]">
                  <dt className="text-ink-3">{k}</dt>
                  <dd className="min-w-0 truncate text-ink">{v}</dd>
                </div>
              ))}
            </dl>
          </section>
          <section aria-labelledby="attempts">
            <Label id="attempts">Tries</Label>
            {attempts.length === 0 ? (
              <p className="border-y border-rule py-4 text-[0.875rem] text-ink-3">{d.state === "scheduled" ? `None yet. Due ${relative(d.runAt)}.` : "None yet."}</p>
            ) : (
              <ol className="border-y border-rule">
                {attempts.map((a, i) => {
                  const prev = attempts[i - 1];
                  const gap = prev ? new Date(a.startedAt).getTime() - (new Date(prev.startedAt).getTime() + prev.durationMs) : 0;
                  const bad = a.outcome !== "ok";
                  return (
                    <li key={a.attempt}>
                      {prev && gap > 1000 && (
                        <p className="grid grid-cols-[4.75rem_1rem_minmax(0,1fr)] gap-x-3 text-xs text-ink-3">
                          <span />
                          <span aria-hidden className="mx-auto h-6 w-px bg-rule-2" />
                          <span className="self-center">waited {ms(gap)}, then tried again</span>
                        </p>
                      )}
                      <div className={cn("grid grid-cols-[4.75rem_1rem_minmax(0,1fr)] items-baseline gap-x-3 py-2.5", i > 0 && gap <= 1000 && "border-t border-rule")}>
                        <time className="text-[0.8125rem] text-ink-3 tnum" title={full(a.startedAt)}>
                          {clockSec(a.startedAt)}
                        </time>
                        <span aria-hidden className={cn("mx-auto size-2 translate-y-[-1px] rounded-full", bad ? (a.outcome === "dead" ? "bg-danger" : "bg-warn") : "bg-ink-3")} />
                        <div className="min-w-0">
                          <p className="text-[0.875rem] text-ink">
                            Try {int(a.attempt)}:{" "}
                            <span className={bad ? (a.outcome === "dead" ? "text-danger" : "text-warn-ink") : ""}>
                              {{ ok: "done", retry: "failed, will retry", dead: "failed, gave up", interrupted: "interrupted", cancelled: "cancelled" }[a.outcome]}
                            </span>
                            <span className="text-ink-3">
                              {" "}
                              in {ms(a.durationMs)}
                              {a.status && (a.status < 200 || a.status > 299) ? ` · answered ${a.status === 489 ? "don’t retry" : `HTTP ${a.status}`}` : ""}
                            </span>
                          </p>
                          {a.error && a.error !== d.lastError && <ErrorText e={a.error} className="mt-0.5 block font-mono text-[0.75rem] break-words text-ink-2" />}
                        </div>
                      </div>
                    </li>
                  );
                })}
              </ol>
            )}
          </section>
        </div>
        <section className="flex flex-col gap-8">
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
      <Label>{title}</Label>
      <Untrusted label={label}>
        <pre className="max-h-80 overflow-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">{value === undefined ? "(nothing)" : JSON.stringify(value, null, 2)}</pre>
      </Untrusted>
    </div>
  );
}

// ------------------------------------------------------------------ workflows

export function WorkflowsPage({ project, state }: { project: string; state?: string }) {
  useTitle(`${project} · Workflows`);
  const navigate = useNavigate();
  const st = runFilters.some((f) => f.state === state) ? (state as WorkflowRun["state"]) : undefined;
  const runs = useQuery(mq.runs(project, st));
  const everything = useQuery(mq.runs(project));
  const approvals = useQuery(mq.wfApprovals(project));
  if (runs.isError && notOnBox(runs.error)) return <NotOnBox what="Workflows" />;
  const waiting = (approvals.data ?? []).filter((a) => a.state === "waiting");
  const list = runs.data ?? [];
  const allRuns = everything.data ?? [];
  const counts = runFilters.reduce<Partial<Record<WorkflowRun["state"], number>>>((acc, f) => {
    if (f.state) acc[f.state] = allRuns.filter((r) => r.state === f.state).length;
    return acc;
  }, {});
  const failed = counts.failed ?? 0;
  let said = "";
  if (everything.isSuccess) {
    const live = (counts.running ?? 0) + (counts.waiting ?? 0);
    said = allRuns.length === 0 ? "No runs yet." : `${live ? `${words(live, true)} ${live === 1 ? "run is" : "runs are"} in progress` : "Nothing is in progress"}`;
    if (allRuns.length) {
      const extras: string[] = [];
      if (waiting.length) extras.push(`${words(waiting.length)} ${waiting.length === 1 ? "waits" : "wait"} for a person`);
      if (failed) extras.push(`${words(failed)} failed and can retry from the step that broke`);
      said += extras.length ? `; ${extras.join(", and ")}.` : ".";
    }
  }
  return (
    <Page wide>
      <Header project={project} title="Workflows" lede="Durable runs that sleep for days, wait for events and ask people, then pick up exactly where they left off." />
      {said && <StateSentence className="mt-8">{said}</StateSentence>}
      {waiting.length > 0 && (
        <section className="mt-8" aria-labelledby="wa">
          <Label id="wa">Waiting for a person</Label>
          <ul className="flex max-w-[44rem] flex-col gap-3">
            {waiting.map((a) => (
              <li key={a.id}>
                <ApprovalCard project={project} a={a} />
              </li>
            ))}
          </ul>
        </section>
      )}
      <section className="mt-10" aria-labelledby="runs">
        <div className="mb-2 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
          <h2 id="runs" className="label">
            Runs
          </h2>
          <FilterWords
            label="Show runs that are"
            items={runFilters}
            value={st}
            counts={counts}
            onPick={(s) => navigate({ to: "/projects/$project/workflows", params: { project }, search: s ? { state: s } : {} })}
          />
        </div>
        <ol className="border-t border-rule-2">
          {runs.isPending && <Skeleton className="mt-3 h-32" />}
          {list.map((r, i) => {
            const head = i === 0 || dayKey(r.createdAt) !== dayKey(list[i - 1].createdAt);
            const w = runWords(r);
            return (
              <li key={r.id}>
                {head && <p className="label pt-4 pb-1.5">{dayLabel(r.createdAt)}</p>}
                <Link
                  to="/projects/$project/workflows/$id"
                  params={{ project, id: r.id }}
                  className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 border-t border-rule py-3 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk/60 sm:grid-cols-[4.75rem_minmax(0,14rem)_minmax(0,1fr)] sm:px-2"
                >
                  <time className="hidden text-[0.8125rem] text-ink-3 tnum sm:block" title={full(r.createdAt)}>
                    {clock(r.createdAt)}
                  </time>
                  <span className="min-w-0">
                    <span className="ident block truncate text-ink">{r.workflow}</span>
                    <span className="block truncate font-mono text-[0.7rem] text-ink-3">{r.idempotencyKey ?? r.id}</span>
                  </span>
                  <span className={cn("col-span-2 row-start-2 min-w-0 truncate text-[0.84375rem] sm:col-span-1 sm:row-start-auto", toneClass[w.tone])}>
                    {r.state === "running" && <PilotLight state="busy" className="mr-2" />}
                    {w.text}
                  </span>
                  <span className="text-right text-[0.8125rem] text-ink-3 sm:hidden">{relative(r.createdAt)}</span>
                </Link>
              </li>
            );
          })}
        </ol>
        {runs.isSuccess && list.length === 0 && (
          <EmptyJobs title={st ? `No ${runFilters.find((f) => f.state === st)?.label.toLowerCase()} runs.` : "No runs yet."} className="border-t border-rule">
            {!st && (
              <>
                Start one from your app with <code className="ident text-ink">workflow.start("onboard", …)</code>, or <code className="ident text-ink">tiffin workflows start</code>.
              </>
            )}
          </EmptyJobs>
        )}
      </section>
    </Page>
  );
}

/** Decide a workflow's human step inline: the step's own words, then Reject and Approve. Human-only steps refuse agent tokens. */
export function ApprovalCard({ project, a, compact, inRun }: { project: string; a: WorkflowApproval; compact?: boolean; /** On its own run's page: the step already names it. */ inRun?: boolean }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [noting, setNoting] = useState(false);
  const [comment, setComment] = useState("");
  const decide = useMutation({
    mutationFn: (d: "approve" | "reject") => mod2.decide(project, a.id, d, comment.trim() || undefined),
    onSuccess: (_, d) => {
      void qc.invalidateQueries({ queryKey: ["wf-approvals", project] });
      void qc.invalidateQueries({ queryKey: ["runs", project] });
      void qc.invalidateQueries({ queryKey: ["run", project, a.runId] });
      toast({ title: d === "approve" ? `Approved. ${a.workflow} carries on.` : `Rejected. ${a.workflow} hears “no” and decides what’s next.` });
    },
  });
  return (
    <article className="rounded-[10px] border border-rule-2 bg-paper-raised px-4 pt-3.5 pb-3.5 shadow-raised">
      {!inRun && <p className="entry text-ink">{a.title || a.step}</p>}
      {a.description && <p className={cn("text-[0.84375rem] text-ink-2", !inRun && "mt-1")}>{a.description}</p>}
      <p className="mt-1.5 flex flex-wrap gap-x-1.5 text-[0.78125rem] text-ink-3">
        {!inRun && <Link to="/projects/$project/workflows/$id" params={{ project, id: a.runId }} className="ident text-[0.75rem] text-ink-2 hover:text-ink hover:underline">
          {a.workflow}
        </Link>}
        <span>{inRun ? "Step" : "· step"} “{a.step}”</span>
        <span>· asked {relative(a.createdAt)}</span>
        {a.timeoutAt && <span>· times out {relative(a.timeoutAt)}</span>}
        {a.humanOnly && <span>· people only</span>}
      </p>
      {can("apply:reversible") && (
        <div className="mt-3 flex flex-wrap items-center justify-end gap-2">
          {!compact && !noting && (
            <button type="button" onClick={() => setNoting(true)} className="mr-auto text-[0.8125rem] text-ink-3 hover:text-ink">
              Add a note
            </button>
          )}
          {!compact && noting && (
            <Input
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder="A note the run can read (optional)"
              className="mr-auto h-8 min-w-0 flex-1 basis-56 text-[0.84375rem]"
              aria-label="Note for the run"
              autoFocus
            />
          )}
          <Button size="md" variant="ghost" onClick={() => decide.mutate("reject")} disabled={decide.isPending}>
            Reject
          </Button>
          <Button size="md" variant="primary" onClick={() => decide.mutate("approve")} disabled={decide.isPending}>
            Approve
          </Button>
        </div>
      )}
      {decide.isError && <ProblemNote className="mt-2" error={decide.error} />}
    </article>
  );
}

// ------------------------------------------------------------------ one run

const kindWords: Record<WorkflowStep["kind"], string> = {
  step: "step",
  sleep: "sleep",
  event: "waits for an event",
  approval: "asks a person",
  webhook: "waits for a webhook",
  patch: "patch",
};

const wdFmt = new Intl.DateTimeFormat("en-GB", { weekday: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const weekdayClock = (iso: string) => wdFmt.format(new Date(iso));

const offset = (n: number) => `+${ms(Math.max(0, n))}`;

export function RunPage({ project, id }: { project: string; id: string }) {
  const qc = useQueryClient();
  const r = useQuery(mq.run(project, id));
  const approvals = useQuery(mq.wfApprovals(project));
  const { can } = useMe();
  const [cancelling, setCancelling] = useState(false);
  useTitle(r.data ? `${r.data.workflow} · Workflows` : "Workflow run");
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["run", project, id] });
    void qc.invalidateQueries({ queryKey: ["runs", project] });
  };
  const retry = useMutation({
    mutationFn: () => mod2.retryRun(project, id),
    onSuccess: () => {
      refresh();
      toast({ title: "Retrying from the step that failed.", detail: "Steps that finished keep their results." });
    },
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
        <ProblemNote error={r.error} title={r.error instanceof ApiError && r.error.status === 404 ? "There’s no run with that ID" : undefined} />
      </Page>
    );
  const run = r.data;
  const steps = run.steps ?? [];
  const t0 = new Date(run.createdAt).getTime();
  const waitingApproval = (approvals.data ?? []).find((a) => a.runId === id && a.state === "waiting");
  const waitingStep = steps.find((s) => s.state === "waiting");
  const failedStep = steps.find((s) => s.state === "failed");
  const total = (run.finishedAt ? new Date(run.finishedAt).getTime() : new Date(run.updatedAt).getTime()) - t0;

  let said: string;
  switch (run.state) {
    case "completed":
      said = `Done in ${ms(total)}, ${relative(run.finishedAt ?? run.updatedAt)}, over ${run.turns === 1 ? "one turn" : `${int(run.turns)} turns`}.`;
      break;
    case "failed":
      said = failedStep ? `Stopped at “${failedStep.title ?? failedStep.name}”. Retrying picks up from there.` : "Failed. Retrying picks up from the step that broke.";
      break;
    case "waiting":
      if (waitingStep?.kind === "approval") said = `Waiting for a person to decide “${waitingStep.title ?? waitingStep.name}”`;
      else if (waitingStep?.kind === "sleep") said = `Sleeping until ${clock(waitingStep.waitUntil ?? run.updatedAt)}, ${relative(waitingStep.waitUntil ?? run.updatedAt)}.`;
      else if (waitingStep?.kind === "event") said = `Waiting for the event “${waitingStep.event}”.`;
      else said = `Waiting for ${waitingFor(run.waitingFor)?.what ?? "something"}.`;
      break;
    case "cancelled":
      said = `Cancelled ${relative(run.finishedAt ?? run.updatedAt)}. Steps that finished keep their results.`;
      break;
    default:
      said = "Running a step now.";
  }

  return (
    <Page wide>
      <PageHeader
        eyebrow={
          <Crumbs
            items={[
              { label: project, to: "/projects/$project", params: { project } },
              { label: "Workflows", to: "/projects/$project/workflows", params: { project } },
            ]}
          />
        }
        title={run.workflow}
        lede={
          <span className="font-mono text-[0.75rem] text-ink-3">
            {run.idempotencyKey ? `${run.idempotencyKey} · ` : ""}
            {run.id} · on {run.app}
          </span>
        }
        actions={
          can("apply:reversible") ? (
            <>
              {(run.state === "running" || run.state === "waiting") && (
                <Button variant="ghost" onClick={() => setCancelling(true)}>
                  Cancel run…
                </Button>
              )}
              {run.state === "failed" && (
                <Button onClick={() => retry.mutate()} disabled={retry.isPending}>
                  Retry from the failed step
                </Button>
              )}
            </>
          ) : undefined
        }
      />
      <StateSentence className="mt-5">{/[.?!]”?$/.test(said) ? said : `${said}.`}</StateSentence>
      {retry.isError && <ProblemNote className="mt-4" error={retry.error} />}

      <section className="mt-10" aria-labelledby="tl">
        <div className="mb-2.5 flex items-end justify-between">
          <h2 id="tl" className="label">
            Steps
          </h2>
          <span className="text-[0.8125rem] text-ink-3 tnum">{run.state === "completed" ? `${ms(total)} in all` : `${ms(total)} so far`}</span>
        </div>
        <ol className="border-y border-rule">
          {steps.map((s, k) => {
            const start = new Date(s.startedAt).getTime() - t0;
            const dur = s.durationMs ?? (s.finishedAt ? new Date(s.finishedAt).getTime() - new Date(s.startedAt).getTime() : undefined);
            const last = k === steps.length - 1 && run.state !== "running" && run.state !== "completed";
            const approval = s.kind === "approval" && s.state === "waiting" ? waitingApproval : undefined;
            return (
              <Step key={s.seq} at={offset(start)} last={last} marker={<StepMarker s={s} />}>
                <div className="flex flex-wrap items-baseline gap-x-2">
                  <span className="text-[0.9375rem] text-ink">{s.title ?? s.name}</span>
                  <span className="text-[0.8125rem] text-ink-3">{kindWords[s.kind]}</span>
                  {s.attempts > 1 && <span className="text-[0.8125rem] text-warn-ink">took {words(s.attempts)} tries</span>}
                  <span className="ml-auto text-[0.8125rem] text-ink-3 tnum">
                    {s.state === "waiting"
                      ? s.waitUntil
                        ? s.kind === "sleep"
                          ? `until ${weekdayClock(s.waitUntil)}`
                          : `times out ${relative(s.waitUntil)}`
                        : "waiting"
                      : dur !== undefined
                        ? ms(dur)
                        : ""}
                  </span>
                </div>
                {s.description && s.kind !== "approval" && <p className="mt-0.5 text-[0.84375rem] text-ink-2">{s.description}</p>}
                {s.kind === "approval" && s.state === "waiting" && (
                  <div className="mt-3 max-w-[40rem]">
                    {approval ? <ApprovalCard project={project} a={approval} inRun /> : <p className="text-[0.84375rem] text-ink-2">{s.description}</p>}
                    <p className="mt-2 text-[0.8125rem] text-ink-3">
                      It’s also in{" "}
                      <Link to="/approvals" className="text-brass-ink hover:underline">
                        Ledger › Approvals
                      </Link>
                      , with everything else waiting for you.
                    </p>
                  </div>
                )}
                {s.kind === "approval" && s.decidedBy && (
                  <p className="mt-1 text-[0.84375rem] text-ink-2">
                    {(s.output as { approved?: boolean })?.approved ? "Approved" : "Rejected"} by {whoWords(s.decidedBy) ?? s.decidedBy}
                    {(s.output as { comment?: string })?.comment ? <span className="text-ink-3">: “{(s.output as { comment?: string }).comment}”</span> : ""}
                  </p>
                )}
                {s.kind === "event" && s.state === "waiting" && can("apply:reversible") && <SendEvent project={project} runId={id} event={s.event ?? ""} />}
                {s.kind === "event" && s.state !== "waiting" && s.event && <p className="mt-0.5 font-mono text-[0.75rem] text-ink-3">event {s.event}</p>}
                {s.error && <ErrorText e={s.error} className="mt-1 block font-mono text-[0.75rem] break-words text-danger" />}
                {s.output !== undefined && s.kind === "step" && (
                  <p className="mt-0.5 truncate font-mono text-[0.75rem] text-ink-3" title={JSON.stringify(s.output)}>
                    → {JSON.stringify(s.output)}
                  </p>
                )}
              </Step>
            );
          })}
          {run.state === "running" && (
            <Step at="now" last marker={<PilotLight state="busy" label="running" />}>
              <p className="text-[0.9375rem] text-ink">Running the next step</p>
            </Step>
          )}
          {run.state === "completed" && (
            <Step at={offset(total)} last marker={<span className="size-2 rounded-full bg-ok" />}>
              <p className="text-[0.9375rem] text-ink">
                Finished
                {run.output !== undefined && <code className="ml-2 font-mono text-[0.75rem] text-ink-3">→ {JSON.stringify(run.output)}</code>}
              </p>
            </Step>
          )}
          {steps.length === 0 && run.state !== "running" && run.state !== "completed" && <li className="py-4 text-[0.875rem] text-ink-3">No steps yet.</li>}
        </ol>
      </section>

      {run.state === "waiting" && !waitingStep && can("apply:reversible") && (
        <section className="mt-8" aria-labelledby="ev">
          <Label id="ev">Send an event</Label>
          <SendEvent project={project} runId={id} event="" />
        </section>
      )}

      <div className="mt-12 grid gap-x-12 gap-y-10 lg:grid-cols-2">
        <section aria-labelledby="hist">
          <Label id="hist">History</Label>
          <ol className="divide-y divide-rule border-y border-rule">
            {(run.timeline ?? []).map((t, i) => (
              <li key={i} className="grid grid-cols-[4.75rem_minmax(0,1fr)] gap-x-3 py-2 text-[0.84375rem]">
                <time className="text-[0.8125rem] text-ink-3 tnum" title={full(t.at)}>
                  {clockSec(t.at)}
                </time>
                <span className="text-ink-2">
                  {sentence(t.message.replace(/ on release dep_\w+/, "").replace(/^HTTP 489:\s*/, ""))}
                  {t.actor && <span className="text-ink-3"> · {whoWords(t.actor)}</span>}
                </span>
              </li>
            ))}
          </ol>
          {(run.turnJobs ?? []).length > 0 && (
            <p className="mt-3 text-[0.8125rem] text-ink-3">
              Ran as{" "}
              {(run.turnJobs ?? []).map((j, i) => (
                <span key={j.id}>
                  {i > 0 && ", "}
                  <Link to="/projects/$project/queues/jobs/$id" params={{ project, id: j.id }} className="ident text-[0.75rem] text-ink-2 hover:text-ink hover:underline">
                    {j.id}
                  </Link>
                </span>
              ))}{" "}
              on <code className="ident text-[0.75rem]">_workflows</code>
              {run.release && (
                <>
                  , pinned to <code className="ident text-[0.75rem]">{run.release}</code>
                </>
              )}
              .
            </p>
          )}
        </section>
        <Json title="Input" value={run.input} label="Sent by whoever started the run. Shown as plain text." />
      </div>
      <Confirm
        open={cancelling}
        onClose={() => setCancelling(false)}
        title={`Cancel this ${run.workflow} run?`}
        body="It stops where it is and can’t be resumed. Steps that already finished keep their results; nothing is rolled back."
        action="Cancel the run"
        run={() => mod2.cancelRun(project, id)}
        done={() => {
          refresh();
          toast({ title: `Cancelled the ${run.workflow} run.` });
        }}
      />
    </Page>
  );
}

function Step({ at, marker, last, children }: { at: string; marker: ReactNode; last?: boolean; children: ReactNode }) {
  return (
    <li className="relative grid grid-cols-[4.75rem_1rem_minmax(0,1fr)] gap-x-3 py-3">
      <span className="text-[0.8125rem] text-ink-3 tnum">{at}</span>
      <span className="relative flex justify-center pt-[0.4rem]">
        {marker}
        {!last && <span aria-hidden className="absolute top-[1.15rem] -bottom-[1.1rem] left-1/2 w-px -translate-x-1/2 bg-rule-2" />}
      </span>
      <div className="min-w-0">{children}</div>
    </li>
  );
}

function StepMarker({ s }: { s: WorkflowStep }) {
  if (s.state === "failed") return <span aria-label="failed" className="relative z-[1] size-2 rounded-full bg-danger" />;
  if (s.state === "waiting") return <span aria-label="waiting" className="relative z-[1] size-2.5 -translate-y-px rounded-full border-[1.5px] border-ink-2 bg-paper" />;
  if (s.state === "timed_out") return <span aria-label="timed out" className="relative z-[1] size-2 rounded-full bg-warn" />;
  if (s.state === "cancelled") return <span aria-label="cancelled" className="relative z-[1] size-2 rounded-full border border-rule-3 bg-paper" />;
  return <span aria-label="done" className="relative z-[1] size-2 rounded-full bg-ink-3" />;
}

function SendEvent({ project, runId, event }: { project: string; runId: string; event: string }) {
  const qc = useQueryClient();
  const [name, setName] = useState(event);
  const [payload, setPayload] = useState("");
  const [bad, setBad] = useState(false);
  const send = useMutation({
    mutationFn: () => {
      let p: unknown = undefined;
      if (payload.trim()) p = JSON.parse(payload);
      return mod2.sendEvent(project, name.trim(), p);
    },
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["run", project, runId] });
      toast({ title: sentence(res.message) });
    },
  });
  return (
    <form
      className="mt-2.5 flex max-w-[40rem] flex-col gap-2 sm:flex-row"
      onSubmit={(e) => {
        e.preventDefault();
        try {
          if (payload.trim()) JSON.parse(payload);
          setBad(false);
        } catch {
          setBad(true);
          return;
        }
        if (name.trim()) send.mutate();
      }}
    >
      {!event && <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="payment-received" className="h-8 font-mono text-[0.78rem] sm:w-56" aria-label="Event name" />}
      <Input
        value={payload}
        onChange={(e) => setPayload(e.target.value)}
        placeholder='Payload, if any: {"verified": true}'
        className={cn("h-8 min-w-0 flex-1 font-mono text-[0.78rem]", bad && "border-danger")}
        aria-label="Payload (JSON)"
        aria-invalid={bad}
      />
      <Button type="submit" size="md" disabled={!name.trim() || send.isPending}>
        {event ? "Send it" : "Send"}
      </Button>
      {bad && <p className="text-[0.8125rem] text-danger sm:hidden">That isn’t JSON.</p>}
      {send.isError && <ProblemNote className="mt-1" error={send.error} />}
    </form>
  );
}
