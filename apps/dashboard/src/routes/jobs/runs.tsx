// Runs: everything that ran or is running (jobs, scheduled runs, workflow
// runs), newest first, with the selected one live beside the list. On a
// phone the list and the one run take turns. j and k move the selection.
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useMemo } from "react";
import { notOnBox } from "@/api/client";
import { jq } from "@/api/jobs";
import type { QueueJob, QueueStats, WorkflowRun } from "@/api/modules";
import { EmptyJobs, FilterWords, jobState, runWords, StateSentence, toneClass, type Tone } from "@/components/jobs-words";
import { NotOnBox, Skeleton } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { useTitle } from "@/components/favicon";
import { cn } from "@/lib/cn";
import { countWords, int, ms, pct, words } from "@/lib/format";
import { useShortcut } from "@/lib/shortcuts";
import { relative } from "@/lib/time";
import { ApprovalCard } from "./approvals";
import { JobPane, RunPane } from "./detail";
import { JobsArea, Label, type JobsSearch } from "./shared";

type Kind = "job" | "cron" | "workflow";
type Item = { id: string; name: string; kind: Kind; at: string; word: string; tone: Tone; live: boolean; sub: string; failed: boolean; done: boolean };

const kinds: Array<{ state?: Kind; label: string }> = [
  { label: "Everything" },
  { state: "job", label: "Jobs" },
  { state: "cron", label: "Schedules" },
  { state: "workflow", label: "Workflows" },
];

function fromJob(j: QueueJob): Item {
  const s = jobState(j);
  return {
    id: j.id,
    name: j.cron ?? j.queue,
    kind: j.kind === "cron" ? "cron" : "job",
    at: j.enqueuedAt,
    word: s.word,
    tone: s.tone,
    live: j.state === "running",
    sub: j.kind === "cron" ? "scheduled run" : j.topic ? `from ${j.topic}` : "job",
    failed: j.state === "dead",
    done: j.state === "completed" || j.state === "cancelled",
  };
}

function fromRun(r: WorkflowRun): Item {
  const w = runWords(r);
  const word = r.state === "waiting" ? (r.waitingFor?.startsWith("sleep") ? "Sleeping" : "Waiting") : r.state === "failed" ? "Failed" : r.state === "running" ? "Running" : r.state === "completed" ? "Done" : "Cancelled";
  return { id: r.id, name: r.workflow, kind: "workflow", at: r.createdAt, word, tone: w.tone, live: r.state === "running", sub: "workflow run", failed: r.state === "failed", done: r.state === "completed" || r.state === "cancelled" };
}

/** The page's state in one sentence: how much ran, and anything stuck. */
export function jobsSentence(all: QueueStats[], failedRuns: number): string {
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
  const issues: Array<[string, boolean]> = [];
  if (failing[0]) issues.push([`${failing[0].name} is failing ${pct(failing[0].failureRate)} of its tries`, true]);
  if (stale[0]) issues.push([`the oldest job on ${stale[0].name} has waited ${ms(stale[0].oldestQueuedSeconds * 1000)}`, false]);
  if (dead + failedRuns) issues.push([`${countWords(dead + failedRuns, "run")} failed for good and ${dead + failedRuns === 1 ? "waits" : "wait"} in Failed`, false]);
  if (paused.length) issues.push([`${paused.map((x) => x.name).join(" and ")} ${paused.length === 1 ? "is" : "are"} paused`, true]);
  if (!issues.length) return `${first} Nothing is stuck.`;
  const t = issues.map((i) => i[0]);
  let s = t.length === 1 ? t[0] : `${t.slice(0, -1).join(", ")}, and ${t[t.length - 1]}`;
  if (!issues[0][1]) s = s.charAt(0).toUpperCase() + s.slice(1);
  return `${first} ${s}.`;
}

export function RunsTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Jobs`);
  const navigate = useNavigate();
  const jobs = useQuery(jq.jobs(project, search.queue));
  const runs = useQuery(jq.runs(project));
  const stats = useQuery(jq.stats(project));
  const approvals = useQuery(jq.approvals(project));
  const kind = kinds.some((k) => k.state === search.kind) ? (search.kind as Kind) : undefined;
  const items = useMemo(() => {
    const all = [...(jobs.data ?? []).filter((j) => j.kind !== "workflow").map(fromJob), ...(search.queue ? [] : (runs.data ?? []).map(fromRun))];
    return all.filter((i) => !kind || i.kind === kind).sort((a, b) => b.at.localeCompare(a.at));
  }, [jobs.data, runs.data, kind, search.queue]);
  const selected = search.id;
  const go = (id?: string) => void navigate({ to: "/projects/$project/jobs", params: { project }, search: { ...search, id }, replace: true });

  // j / k move through the list, like a mail client.
  const step = (by: number) => {
    const i = items.findIndex((x) => x.id === selected);
    const next = items[Math.max(0, Math.min(items.length - 1, i + by))];
    if (next && next.id !== selected) {
      go(next.id);
      document.getElementById(`run-${next.id}`)?.focus();
    }
  };
  useShortcut("j", "Next run", () => step(1));
  useShortcut("k", "Previous run", () => step(-1));

  if (jobs.isError && notOnBox(jobs.error)) return <NotOnBox what="Jobs" />;
  const waiting = (approvals.data ?? []).filter((a) => a.state === "waiting");
  const failedRuns = (runs.data ?? []).filter((r) => r.state === "failed").length;
  const empty = jobs.isSuccess && runs.isSuccess && items.length === 0 && !kind && !search.queue;

  return (
    <JobsArea project={project} tab="runs" search={search}>
      {stats.isSuccess && !empty && <StateSentence className="mt-8">{jobsSentence(stats.data, failedRuns)}</StateSentence>}
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
      {(jobs.isError || runs.isError) && <ProblemNote className="mt-6" error={jobs.error ?? runs.error} />}
      {empty ? (
        <EmptyJobs title="Nothing has run yet." className="mt-6">
          Make a schedule or a queue with the buttons above, or send a job from an app with <code className="ident text-ink">queue.send("emails", …)</code>. Every run shows up here, live.
        </EmptyJobs>
      ) : (
        <div className="mt-8 grid gap-x-10 gap-y-6 lg:grid-cols-[minmax(0,21rem)_minmax(0,1fr)]">
          <section aria-labelledby="runs-h" className={cn(selected && "max-lg:hidden")}>
            <div className="mb-2 flex flex-col gap-2">
              <h2 id="runs-h" className="label">
                {search.queue ? (
                  <>
                    Runs on <span className="ident normal-case">{search.queue}</span>
                  </>
                ) : (
                  "Latest runs"
                )}
              </h2>
              <FilterWords
                label="Show"
                items={kinds}
                value={kind}
                onPick={(k) => void navigate({ to: "/projects/$project/jobs", params: { project }, search: { ...search, kind: k, id: undefined }, replace: true })}
              />
            </div>
            {search.queue && (
              <Link to="/projects/$project/jobs" params={{ project }} search={{}} className="mb-2 inline-block text-[0.8125rem] text-ink-3 hover:text-ink">
                Show every queue
              </Link>
            )}
            <ol className="border-t border-rule-2" aria-label="Runs (j and k move)">
              {(jobs.isPending || runs.isPending) && <Skeleton className="mt-3 h-40" />}
              {items.map((it) => (
                <li key={it.id} className="[contain-intrinsic-size:auto_52px] [content-visibility:auto]">
                  <Link
                    id={`run-${it.id}`}
                    to="/projects/$project/jobs"
                    params={{ project }}
                    search={{ ...search, id: it.id }}
                    replace
                    aria-current={it.id === selected ? "true" : undefined}
                    className={cn(
                      "grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-3 border-b border-rule px-2 py-2.5 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk/60",
                      it.id === selected && "bg-paper-sunk",
                    )}
                  >
                    <span className="min-w-0">
                      <span className="flex items-center gap-2">
                        {it.live && <PilotLight state="busy" />}
                        <span className="ident truncate text-ink">{it.name}</span>
                      </span>
                      <span className="block truncate text-xs text-ink-3">
                        {it.sub} · {relative(it.at)}
                      </span>
                    </span>
                    <span className={cn("text-right text-[0.8125rem]", toneClass[it.tone])}>{it.word}</span>
                  </Link>
                </li>
              ))}
            </ol>
            {jobs.isSuccess && items.length === 0 && <p className="py-6 text-[0.875rem] text-ink-3">None of these yet.</p>}
            {(jobs.data?.length ?? 0) >= 100 && <p className="pt-3 text-[0.8125rem] text-ink-3">The newest 100 jobs. Pick a queue on Queues to see only its runs.</p>}
          </section>
          <div className={cn("min-w-0", !selected && "max-lg:hidden")}>
            {selected ? (
              <div className="lg:sticky lg:top-6">
                <button type="button" onClick={() => go(undefined)} className="mb-4 inline-flex items-center gap-1.5 text-[0.8125rem] text-ink-3 hover:text-ink lg:hidden">
                  <ArrowLeft className="size-3.5" /> All runs
                </button>
                {selected.startsWith("run_") ? <RunPane key={selected} project={project} id={selected} /> : <JobPane key={selected} project={project} id={selected} />}
              </div>
            ) : (
              <p className="rounded-[10px] border border-dashed border-rule-3 px-6 py-10 text-center text-[0.9375rem] text-ink-3">Pick a run to watch it: progress, output and every step as it happens.</p>
            )}
          </div>
        </div>
      )}
    </JobsArea>
  );
}
