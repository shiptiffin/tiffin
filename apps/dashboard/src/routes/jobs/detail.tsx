// One job or one workflow run: what it is (status, where, how long, tries),
// why it failed (message first, stack folded), live progress and logs, the
// tries or steps over time, and its payload and output as copyable JSON.
// Retry, Cancel, Discard and Replay sit in the header. The same view sits,
// shorter, in the Runs tab's right half.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowUpRight, MoreHorizontal } from "lucide-react";
import { useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { jobsApi, jq } from "@/api/jobs";
import { mod2, type QueueJob, type WorkflowRun, type WorkflowStep } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { ErrorBlock } from "@/components/jobs-error";
import { JsonView } from "@/components/jobs-json";
import { jobStatus, runStatus, StatusLabel } from "@/components/jobs-status";
import { ErrorText, StateSentence, waitingFor } from "@/components/jobs-words";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote, sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { countWords, int, ms, words } from "@/lib/format";
import { jobsSearch } from "@/lib/jobs-search";
import { useMe } from "@/lib/me";
import { clock, full, relative } from "@/lib/time";
import { ApprovalCard, SendEvent } from "./approvals";
import { jobRows, Output, Progress, runRows, useLive, useNow, Waterfall, type Chunk } from "./live";
import { jobDuration, runDuration, took } from "./model";
import { clockSec, Label, timelineWords, useOwnerName, whoWords } from "./shared";
import { WorkersTab } from "./workers";

const finishedJob = (s?: string) => s === "completed" || s === "dead" || s === "cancelled";
const finishedRun = (s?: string) => s === "completed" || s === "failed" || s === "cancelled";
const problem = (e: unknown) => (e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e));

/** A job or a run, by its ID. /jobs/workers lands here too until Workers has a route of its own. */
export function JobOrRunPage({ project, id }: { project: string; id: string }) {
  if (id === "workers") return <WorkersRoute project={project} />;
  return id.startsWith("run_") ? <RunPage project={project} id={id} /> : <JobPage project={project} id={id} />;
}

function WorkersRoute({ project }: { project: string }) {
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  return <WorkersTab project={project} search={jobsSearch(raw)} />;
}

function Crumbed({ project, title, lede, actions }: { project: string; title: ReactNode; lede?: ReactNode; actions?: ReactNode }) {
  return (
    <PageHeader
      eyebrow={
        <Crumbs
          items={[
            { label: project, to: "/projects/$project", params: { project } },
            { label: "Jobs", to: "/projects/$project/jobs", params: { project } },
          ]}
        />
      }
      title={title}
      lede={lede}
      actions={actions}
    />
  );
}

/** The facts strip under a title: label over value, in a row that wraps. */
function Facts({ items, className, compact }: { items: Array<[string, ReactNode]>; className?: string; /** In the Runs pane: three across at most. */ compact?: boolean }) {
  return (
    <dl className={cn("grid grid-cols-2 gap-x-6 gap-y-3 border-y border-rule py-3.5 sm:grid-cols-3", !compact && "lg:flex lg:flex-wrap lg:gap-x-10", className)}>
      {items.map(([k, v]) => (
        <div key={k} className="min-w-0">
          <dt className="text-xs text-ink-3">{k}</dt>
          <dd className="mt-0.5 truncate text-[0.875rem] text-ink tnum">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** The ID in mono with a copy button. */
function IdLine({ id, extra }: { id: string; extra?: ReactNode }) {
  return (
    <span className="inline-flex max-w-full items-center gap-1 font-mono text-[0.75rem] text-ink-3">
      <span className="truncate">{id}</span>
      <CopyButton value={id} label="Copy the ID" className="size-6" />
      {extra}
    </span>
  );
}

/** Logs: what the app wrote with job.log / ctx.stream, or how to write some. */
function Logs({ chunks, live, call }: { chunks: Chunk[]; live: boolean; call: string }) {
  return (
    <section aria-labelledby="logs-h">
      <Label id="logs-h">Logs</Label>
      {chunks.length > 0 ? (
        <Output chunks={chunks} live={live} />
      ) : (
        <p className="rounded-[10px] border border-dashed border-rule-3 px-4 py-3 text-[0.8125rem] text-ink-3">
          {live ? "Nothing written yet." : "This one wrote no logs."} Lines written with <code className="ident text-[0.75rem] text-ink-2">{call}</code> show here as they happen.
        </p>
      )}
    </section>
  );
}

// ------------------------------------------------------------------ a job

function useJob(project: string, id: string) {
  const live = useLive(project, id, true);
  const j = useQuery({
    ...jq.job(project, id),
    refetchInterval: (q) => (finishedJob(q.state.data?.state) || live.connected ? false : 3000),
  });
  return { j, live };
}

function JobActions({ project, d, compact }: { project: string; d: QueueJob; compact?: boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useMe();
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["job", project, d.id] });
    void qc.invalidateQueries({ queryKey: ["jobs", project] });
    void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
  };
  const act = useMutation({
    mutationFn: (a: "retry" | "cancel" | "discard") => (a === "retry" ? mod2.retryJob(project, d.id) : mod2.cancelJob(project, d.id)),
    onSuccess: (_, a) => {
      refresh();
      toast({
        title: a === "retry" ? `Sent ${d.id} back to its queue.` : a === "discard" ? `Discarded ${d.id}.` : `Cancelled ${d.id}.`,
        detail: a === "retry" ? "It gets a fresh set of tries." : a === "discard" ? "It left Failed. Its tries stay on record." : "It won’t run unless you send it again.",
        action: {
          label: "Undo",
          run: async () => {
            await (a === "retry" ? mod2.cancelJob(project, d.id) : mod2.retryJob(project, d.id));
            refresh();
          },
        },
      });
    },
    onError: (e) => toast({ title: "That didn’t work.", detail: problem(e), tone: "danger" }),
  });
  // Replay: a new job with the same payload and options, so this one stays as it was.
  const replay = useMutation({
    mutationFn: () =>
      d.cron
        ? jobsApi.triggerCron(project, d.cron).then((r) => r.job)
        : jobsApi.send(project, { name: d.topic ?? d.queue, payload: d.payload, key: d.key, groupKey: d.groupKey, priority: d.priority }).then((r) => r.jobs?.[0] ?? ""),
    onSuccess: (id) => {
      refresh();
      toast({
        title: d.cron ? `Started ${d.cron} now.` : `Sent a copy of ${d.id} to ${d.topic ?? d.queue}.`,
        detail: d.cron ? "Its regular runs stay as they are." : "Same payload, a new job.",
        action: id ? { label: "Open it", run: () => void navigate({ to: "/projects/$project/jobs/$id", params: { project, id } }) } : undefined,
      });
    },
    onError: (e) => toast({ title: "Couldn’t replay it.", detail: problem(e), tone: "danger" }),
  });
  if (!can("apply:reversible")) return null;
  const size = compact ? "sm" : "md";
  const cancellable = ["queued", "scheduled", "retrying", "running"].includes(d.state);
  const retriable = ["dead", "cancelled", "retrying", "scheduled"].includes(d.state);
  const replayable = d.kind !== "workflow" && finishedJob(d.state);
  return (
    <>
      {cancellable && (
        <Button variant="ghost" size={size} onClick={() => act.mutate("cancel")} disabled={act.isPending}>
          Cancel
        </Button>
      )}
      {retriable && (
        <Button size={size} variant={d.state === "dead" ? "primary" : "secondary"} onClick={() => act.mutate("retry")} disabled={act.isPending}>
          {d.state === "scheduled" ? "Run it now" : d.state === "retrying" ? "Retry now" : "Retry"}
        </Button>
      )}
      {replayable && d.state === "completed" && (
        <Button size={size} onClick={() => replay.mutate()} disabled={replay.isPending} title={d.cron ? "Run the schedule once more" : "Send a new job with the same payload"}>
          Replay
        </Button>
      )}
      {(d.state === "dead" || (replayable && d.state !== "completed")) && (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size={compact ? "icon-sm" : "icon"} aria-label={`More for ${d.id}`}>
              <MoreHorizontal />
            </Button>
          </MenuTrigger>
          <MenuContent align="end" className="min-w-56">
            {replayable && <MenuItem onSelect={() => replay.mutate()}>{d.cron ? "Run the schedule now" : "Replay as a new job"}</MenuItem>}
            {d.state === "dead" && (
              <MenuItem onSelect={() => act.mutate("discard")} variant="danger">
                Discard
              </MenuItem>
            )}
          </MenuContent>
        </Menu>
      )}
    </>
  );
}

function jobSentence(d: QueueJob): ReactNode {
  const ran = jobDuration(d);
  const tries = (n: number) => countWords(n, "try", "tries");
  switch (d.state) {
    case "completed":
      return `Done in ${ran !== undefined ? ms(ran) : "no time"} ${d.attempt > 1 ? `on the ${["", "first", "second", "third", "fourth", "fifth"][d.attempt] ?? `${d.attempt}th`} try` : "on the first try"}, ${relative(d.finishedAt ?? d.enqueuedAt)}.`;
    case "dead":
      return `Gave up after ${tries(d.attempt)}, ${relative(d.finishedAt ?? d.enqueuedAt)}.`;
    case "retrying":
      return `Failed ${words(d.attempt)} of ${int(d.maxAttempts)} tries so far; the next one is ${relative(d.runAt)}.`;
    case "scheduled":
      return `Due at ${clock(d.runAt)}, ${relative(d.runAt)}.`;
    case "queued":
      return d.waitingFor ? `Waiting for ${d.waitingFor}; sent ${relative(d.enqueuedAt)}.` : `Waiting for a free slot; sent ${relative(d.enqueuedAt)}.`;
    case "running":
      return (
        <span className="inline-flex items-center gap-3">
          <PilotLight state="busy" label="running" />
          Running now, try {int(d.attempt)} of {int(d.maxAttempts)}.
        </span>
      );
    case "cancelled":
      return `Cancelled ${relative(d.finishedAt ?? d.enqueuedAt)}.`;
  }
}

/** Progress and the tries over time, as they happen. */
function JobLive({ d, live }: { d: QueueJob; live: ReturnType<typeof useLive> }) {
  const running = !finishedJob(d.state);
  const progress = live.state?.progress ?? d.progress;
  const now = useNow(running);
  const { rows, start, end } = jobRows(d, Math.max(now, live.at));
  return (
    <div className="grid gap-6">
      {progress !== undefined && progress !== null && <Progress value={progress} live={running && live.connected} at={live.at} source="job.progress()" />}
      <Waterfall rows={rows} start={start} end={end} label={`${d.id}: its tries over time`} />
    </div>
  );
}

const jobName = (d: QueueJob) => (d.cron ? d.cron : d.queue);
const lastStatus = (d: QueueJob) => d.lastStatus ?? d.attempts?.[d.attempts.length - 1]?.status;

function jobFacts(project: string, d: QueueJob, short?: boolean): Array<[string, ReactNode]> {
  const ran = jobDuration(d);
  const out: Array<[string, ReactNode]> = [
    ["Status", <StatusLabel key="s" status={jobStatus(d)} />],
    [
      d.cron ? "Schedule" : "Queue",
      d.cron ? (
        <Link key="c" to="/projects/$project/jobs/schedules" params={{ project }} className="ident hover:underline">
          {d.cron}
        </Link>
      ) : (
        <Link key="q" to="/projects/$project/jobs" params={{ project }} search={{ queue: d.queue }} className="ident hover:underline">
          {d.queue}
        </Link>
      ),
    ],
    ["Took", ran !== undefined ? took(ran) : "–"],
    ["Tries", `${int(d.attempt)} of ${int(d.maxAttempts)}`],
    [
      "Sent",
      <time key="t" dateTime={d.enqueuedAt} title={full(d.enqueuedAt)}>
        {relative(d.enqueuedAt)}
      </time>,
    ],
  ];
  if (!short && d.enqueuedBy) out.push(["Sent by", whoWords(d.enqueuedBy)]);
  return out;
}

/** The Runs tab's right half for a job. */
export function JobPane({ project, id }: { project: string; id: string }) {
  const { j, live } = useJob(project, id);
  if (j.isPending) return <Skeleton className="h-64" />;
  if (j.isError) return <ProblemNote error={j.error} title={j.error instanceof ApiError && j.error.status === 404 ? "There’s no job with that ID" : undefined} />;
  const d = j.data;
  return (
    <section aria-label={`${jobName(d)} ${d.id}`} className="grid gap-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="ident truncate text-[1.0625rem] font-[550] text-ink">{jobName(d)}</h2>
          <IdLine id={d.id} />
        </div>
        <div className="flex gap-1.5">
          <JobActions project={project} d={d} compact />
        </div>
      </div>
      <Facts items={jobFacts(project, d, true)} compact />
      <p className="text-[0.875rem] text-ink-2">{jobSentence(d)}</p>
      {d.lastError && <ErrorBlock error={d.lastError} status={lastStatus(d)} title={d.state === "completed" ? "An earlier try failed" : "Why it failed"} />}
      <JobLive d={d} live={live} />
      {live.chunks.length > 0 && <Output chunks={live.chunks} live={!finishedJob(d.state)} />}
      <JsonView title="Payload" value={d.payload} />
      {d.output !== undefined && <JsonView title="Output" value={d.output} />}
      <Link to="/projects/$project/jobs/$id" params={{ project, id }} className="inline-flex items-center gap-1 text-[0.8125rem] font-[550] text-brass-ink hover:underline">
        Payload, output and every try <ArrowUpRight className="size-3.5" />
      </Link>
    </section>
  );
}

export function JobPage({ project, id }: { project: string; id: string }) {
  useOwnerName();
  useTitle(`${id} · Jobs`);
  const { j, live } = useJob(project, id);
  if (j.isPending)
    return (
      <Page wide>
        <Skeleton className="h-10 w-80" />
        <Skeleton className="mt-6 h-16" />
        <Skeleton className="mt-6 h-48" />
      </Page>
    );
  if (j.isError)
    return (
      <Page wide>
        <Crumbed project={project} title={id} />
        <ProblemNote className="mt-6" error={j.error} title={j.error instanceof ApiError && j.error.status === 404 ? "There’s no job with that ID" : undefined} />
      </Page>
    );
  const d = j.data;
  const attempts = d.attempts ?? [];
  const about: Array<[string, ReactNode]> = [
    ["Delivered to", <code key="t" className="ident break-all">{d.target}</code>],
    ...(d.topic ? ([["Topic", <code key="tp" className="ident">{d.topic}</code>]] as Array<[string, ReactNode]>) : []),
    ...(d.runId
      ? ([
          [
            "Workflow run",
            <Link key="r" to="/projects/$project/jobs/$id" params={{ project, id: d.runId }} className="ident inline-flex items-center gap-1 text-brass-ink hover:underline">
              {d.runId.slice(0, 16)}… <ArrowUpRight className="size-3.5" />
            </Link>,
          ],
        ] as Array<[string, ReactNode]>)
      : []),
    ...(d.key ? ([["Key", <code key="k" className="ident">{d.key}</code>]] as Array<[string, ReactNode]>) : []),
    ...(d.groupKey ? ([["In order with", <code key="g" className="ident">{d.groupKey}</code>]] as Array<[string, ReactNode]>) : []),
    ...(d.dedupe ? ([["Dedupe", <code key="dd" className="ident">{d.dedupe}</code>]] as Array<[string, ReactNode]>) : []),
    ...(d.priority !== "normal" ? ([["Priority", d.priority]] as Array<[string, ReactNode]>) : []),
    ["Sent at", <time key="s" dateTime={d.enqueuedAt} title={full(d.enqueuedAt)}>{clockSec(d.enqueuedAt)}</time>],
    ...(d.release ? ([["Release", <code key="rel" className="ident text-ink-2">{d.release}</code>]] as Array<[string, ReactNode]>) : []),
  ];
  return (
    <Page wide>
      <Crumbed project={project} title={<span className="font-mono tracking-[-0.03em]">{jobName(d)}</span>} lede={<IdLine id={d.id} extra={<span className="font-sans">· {d.cron ? "a scheduled run" : d.topic ? `from topic ${d.topic}` : "a job"}</span>} />} actions={<JobActions project={project} d={d} />} />
      <Facts className="mt-6" items={jobFacts(project, d)} />
      <StateSentence className="mt-6">{jobSentence(d)}</StateSentence>
      {d.lastError && <ErrorBlock className="mt-5 max-w-[52rem]" error={d.lastError} status={lastStatus(d)} title={d.state === "completed" ? "An earlier try failed" : "Why it failed"} />}

      <div className="mt-10 grid gap-x-12 gap-y-10 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-10">
          <section aria-labelledby="attempts">
            <Label id="attempts">Tries</Label>
            <JobLive d={d} live={live} />
            {attempts.length === 0 ? (
              <p className="mt-4 border-y border-rule py-4 text-[0.875rem] text-ink-3">{d.state === "scheduled" ? `No tries yet. Due ${relative(d.runAt)}.` : "No tries yet."}</p>
            ) : (
              <ol className="mt-4 divide-y divide-rule border-y border-rule">
                {attempts.map((a) => {
                  const bad = a.outcome !== "ok";
                  return (
                    <li key={a.attempt} className="grid grid-cols-[4.75rem_1rem_minmax(0,1fr)] items-baseline gap-x-3 py-2.5">
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
                        {a.error && a.error !== d.lastError && <ErrorText e={a.error.split("\n")[0]} className="mt-0.5 block truncate font-mono text-[0.75rem] text-ink-2" />}
                      </div>
                    </li>
                  );
                })}
              </ol>
            )}
          </section>
          <Logs chunks={live.chunks} live={!finishedJob(d.state)} call="job.log()" />
          <section aria-labelledby="facts">
            <Label id="facts">Details</Label>
            <dl className="divide-y divide-rule border-y border-rule">
              {about.map(([k, v]) => (
                <div key={k} className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-x-4 py-2 text-[0.84375rem]">
                  <dt className="text-ink-3">{k}</dt>
                  <dd className="min-w-0 truncate text-ink">{v}</dd>
                </div>
              ))}
            </dl>
          </section>
        </div>
        <div className="flex min-w-0 flex-col gap-6">
          <JsonView title="Payload" value={d.payload} note="sent with the job, shown as plain text" />
          <JsonView title="Output" value={d.output} note="what the handler returned" empty={finishedJob(d.state) ? "The handler returned nothing." : "Shows when the job finishes."} />
        </div>
      </div>
    </Page>
  );
}

// ------------------------------------------------------------------ a run

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

function useRun(project: string, id: string) {
  const live = useLive(project, id, true);
  const r = useQuery({ ...jq.run(project, id), refetchInterval: (q) => (finishedRun(q.state.data?.state) || live.connected ? false : 5000) });
  return { r, live };
}

function runSentence(run: WorkflowRun): string {
  const steps = run.steps ?? [];
  const waitingStep = steps.find((s) => s.state === "waiting");
  const failedStep = steps.find((s) => s.state === "failed");
  const total = (run.finishedAt ? new Date(run.finishedAt).getTime() : new Date(run.updatedAt).getTime()) - new Date(run.createdAt).getTime();
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
  return /[.?!]”?$/.test(said) ? said : `${said}.`;
}

function RunActions({ project, run, compact }: { project: string; run: WorkflowRun; compact?: boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useMe();
  const [cancelling, setCancelling] = useState(false);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["run", project, run.id] });
    void qc.invalidateQueries({ queryKey: ["runs", project] });
  };
  const retry = useMutation({
    mutationFn: () => mod2.retryRun(project, run.id),
    onSuccess: () => {
      refresh();
      toast({ title: "Retrying from the step that failed.", detail: "Steps that finished keep their results." });
    },
    onError: (e) => toast({ title: "Couldn’t retry it.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  // Replay: a new run of the same workflow with the same input; this one stays as it was.
  const replay = useMutation({
    mutationFn: () => jobsApi.startRun(project, { workflow: run.workflow, app: run.app, input: run.input }),
    onSuccess: (r) => {
      refresh();
      toast({ title: `Started a new ${run.workflow} run.`, detail: "Same input, from the first step.", action: { label: "Open it", run: () => void navigate({ to: "/projects/$project/jobs/$id", params: { project, id: r.id } }) } });
    },
    onError: (e) => toast({ title: "Couldn’t start it.", detail: problem(e), tone: "danger" }),
  });
  if (!can("apply:reversible")) return null;
  return (
    <>
      {finishedRun(run.state) && (
        <Button size={compact ? "sm" : "md"} variant={run.state === "failed" ? "ghost" : "secondary"} onClick={() => replay.mutate()} disabled={replay.isPending} title="Start a new run with the same input">
          Replay
        </Button>
      )}
      {(run.state === "running" || run.state === "waiting") && (
        <Button variant="ghost" size={compact ? "sm" : "md"} onClick={() => setCancelling(true)}>
          Cancel run…
        </Button>
      )}
      {run.state === "failed" && (
        <Button size={compact ? "sm" : "md"} variant="primary" onClick={() => retry.mutate()} disabled={retry.isPending}>
          Retry from the failed step
        </Button>
      )}
      <Confirm
        open={cancelling}
        onClose={() => setCancelling(false)}
        title={`Cancel this ${run.workflow} run?`}
        body="It stops where it is and can’t be resumed. Steps that already finished keep their results; nothing is rolled back."
        action="Cancel the run"
        run={() => mod2.cancelRun(project, run.id)}
        done={() => {
          refresh();
          toast({ title: `Cancelled the ${run.workflow} run.` });
        }}
      />
    </>
  );
}

function RunLive({ run, live }: { run: WorkflowRun; live: ReturnType<typeof useLive> }) {
  const going = !finishedRun(run.state);
  const progress = live.state?.progress ?? run.progress;
  const now = useNow(going);
  const { rows, start, end } = runRows(run, run.steps ?? [], Math.max(now, live.at));
  return (
    <div className="grid gap-6">
      {progress !== undefined && progress !== null && <Progress value={progress} live={going && live.connected} at={live.at} source="ctx.progress()" />}
      <Waterfall rows={rows} start={start} end={end} label={`${run.workflow}: its steps over time`} />
    </div>
  );
}

function runFacts(run: WorkflowRun, short?: boolean): Array<[string, ReactNode]> {
  const w = waitingFor(run.waitingFor);
  const out: Array<[string, ReactNode]> = [
    ["Status", <StatusLabel key="s" status={runStatus(run)} word={run.state === "waiting" && w?.kind === "sleep" ? "Sleeping" : undefined} />],
    ["Took", `${took(runDuration(run))}${finishedRun(run.state) ? "" : " so far"}`],
    ["Steps", `${int((run.steps ?? []).filter((x) => x.kind !== "patch").length)} in ${run.turns === 1 ? "one turn" : `${int(run.turns)} turns`}`],
    [
      "Started",
      <time key="t" dateTime={run.createdAt} title={full(run.createdAt)}>
        {relative(run.createdAt)}
      </time>,
    ],
  ];
  if (!short) out.push(["App", <span key="a" className="ident">{run.app}</span>]);
  if (!short && run.startedBy) out.push(["Started by", whoWords(run.startedBy)]);
  return out;
}

/** The Runs tab's right half for a workflow run. */
export function RunPane({ project, id }: { project: string; id: string }) {
  const { r, live } = useRun(project, id);
  if (r.isPending) return <Skeleton className="h-64" />;
  if (r.isError) return <ProblemNote error={r.error} title={r.error instanceof ApiError && r.error.status === 404 ? "There’s no run with that ID" : undefined} />;
  const run = r.data;
  return (
    <section aria-label={`${run.workflow} ${run.id}`} className="grid gap-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="ident truncate text-[1.0625rem] font-[550] text-ink">{run.workflow}</h2>
          <IdLine id={run.idempotencyKey ?? run.id} />
        </div>
        <div className="flex gap-1.5">
          <RunActions project={project} run={run} compact />
        </div>
      </div>
      <Facts items={runFacts(run, true)} compact />
      <p className="text-[0.875rem] text-ink-2">{runSentence(run)}</p>
      {run.error && <ErrorBlock error={run.error} title="Why it failed" />}
      <RunLive run={run} live={live} />
      {live.chunks.length > 0 && <Output chunks={live.chunks} live={!finishedRun(run.state)} />}
      <JsonView title="Input" value={run.input} />
      {run.output !== undefined && <JsonView title="Output" value={run.output} />}
      <Link to="/projects/$project/jobs/$id" params={{ project, id }} className="inline-flex items-center gap-1 text-[0.8125rem] font-[550] text-brass-ink hover:underline">
        Every step, its history and input <ArrowUpRight className="size-3.5" />
      </Link>
    </section>
  );
}

export function RunPage({ project, id }: { project: string; id: string }) {
  useOwnerName();
  const { r, live } = useRun(project, id);
  const approvals = useQuery(jq.approvals(project));
  const { can } = useMe();
  useTitle(r.data ? `${r.data.workflow} · Jobs` : "Workflow run");
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
  const total = (run.finishedAt ? new Date(run.finishedAt).getTime() : new Date(run.updatedAt).getTime()) - t0;
  return (
    <Page wide>
      <Crumbed
        project={project}
        title={<span className="font-mono tracking-[-0.03em]">{run.workflow}</span>}
        lede={<IdLine id={run.id} extra={run.idempotencyKey ? <span className="truncate">· {run.idempotencyKey}</span> : undefined} />}
        actions={<RunActions project={project} run={run} />}
      />
      <Facts className="mt-6" items={runFacts(run)} />
      <StateSentence className="mt-6">{runSentence(run)}</StateSentence>
      {run.error && <ErrorBlock className="mt-5 max-w-[52rem]" error={run.error} title="Why it failed" />}
      <section className="mt-8 max-w-[52rem]" aria-label="Live">
        <RunLive run={run} live={live} />
      </section>

      <section className="mt-10" aria-labelledby="tl">
        <div className="mb-2.5 flex items-end justify-between">
          <h2 id="tl" className="label">
            Steps
          </h2>
          <span className="text-[0.8125rem] text-ink-3 tnum">{finishedRun(run.state) ? `${took(total)} in all` : `${took(total)} so far`}</span>
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
                    {s.state === "waiting" ? (s.waitUntil ? (s.kind === "sleep" ? `until ${weekdayClock(s.waitUntil)}` : `times out ${relative(s.waitUntil)}`) : "waiting") : dur !== undefined ? ms(dur) : ""}
                  </span>
                </div>
                {s.description && s.kind !== "approval" && <p className="mt-0.5 text-[0.84375rem] text-ink-2">{s.description}</p>}
                {s.kind === "approval" && s.state === "waiting" && (
                  <div className="mt-3 max-w-[40rem]">{approval ? <ApprovalCard project={project} a={approval} inRun /> : <p className="text-[0.84375rem] text-ink-2">{s.description}</p>}</div>
                )}
                {s.kind === "approval" && s.decidedBy && (
                  <p className="mt-1 text-[0.84375rem] text-ink-2">
                    {(s.output as { approved?: boolean })?.approved ? "Approved" : "Rejected"} by {whoWords(s.decidedBy) ?? s.decidedBy}
                    {(s.output as { comment?: string })?.comment ? <span className="text-ink-3">: “{(s.output as { comment?: string }).comment}”</span> : ""}
                  </p>
                )}
                {s.kind === "event" && s.state === "waiting" && can("apply:reversible") && <SendEvent project={project} runId={id} event={s.event ?? ""} />}
                {s.kind === "event" && s.state !== "waiting" && s.event && <p className="mt-0.5 font-mono text-[0.75rem] text-ink-3">event {s.event}</p>}
                {s.error && (s.state === "failed" && s.error !== run.error ? <ErrorBlock className="mt-2 max-w-[44rem]" error={s.error} title="This step failed" /> : <ErrorText e={s.error} className="mt-1 block font-mono text-[0.75rem] break-words text-danger" />)}
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
                  {sentence(timelineWords(t.message.replace(/ on release dep_\w+/, "").replace(/^HTTP 489:\s*/, "")))}
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
                  <Link to="/projects/$project/jobs/$id" params={{ project, id: j.id }} className="ident text-[0.75rem] text-ink-2 hover:text-ink hover:underline">
                    {j.id}
                  </Link>
                </span>
              ))}
              {run.release && (
                <>
                  , pinned to <code className="ident text-[0.75rem]">{run.release}</code>
                </>
              )}
              .
            </p>
          )}
        </section>
        <div className="flex min-w-0 flex-col gap-6">
          <JsonView title="Input" value={run.input} note="sent by whoever started the run" />
          <JsonView title="Output" value={run.output} note="what the workflow returned" empty={run.state === "completed" ? "The workflow returned nothing." : finishedRun(run.state) ? "It stopped before returning anything." : "Shows when the run finishes."} />
          <Logs chunks={live.chunks} live={!finishedRun(run.state)} call="ctx.stream()" />
        </div>
      </div>
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
  if (s.state === "failed") return <span role="img" aria-label="failed" className="relative z-[1] size-2 rounded-full bg-danger" />;
  if (s.state === "waiting") return <span role="img" aria-label="waiting" className="relative z-[1] size-2.5 -translate-y-px rounded-full border-[1.5px] border-ink-2 bg-paper" />;
  if (s.state === "timed_out") return <span role="img" aria-label="timed out" className="relative z-[1] size-2 rounded-full bg-warn" />;
  if (s.state === "cancelled") return <span role="img" aria-label="cancelled" className="relative z-[1] size-2 rounded-full border border-rule-3 bg-paper" />;
  return <span role="img" aria-label="done" className="relative z-[1] size-2 rounded-full bg-ink-3" />;
}
