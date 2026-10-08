// Runs: everything that ran or is running (jobs, scheduled runs, workflow
// runs), newest first, as a table: status, what ran and where, tries,
// duration and when. Filters live in the URL. Picking a run opens it live
// beside the table (on a wide screen) or in its place (narrower); j and k
// move the selection, / finds.
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Search, X } from "lucide-react";
import { useEffect, useEffectEvent, useMemo, useRef, useState } from "react";
import { notOnBox } from "@/api/client";
import { jq } from "@/api/jobs";
import type { QueueJob, QueueStats, WorkflowRun } from "@/api/modules";
import { q as api } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { FilterWords, StateSentence, waitingFor } from "@/components/jobs-words";
import { JobsStart, JobsTrouble, likelyApp } from "@/components/jobs-start";
import { jobStatus, runStatus, StatusLabel, statusGroups, statusWord, type Status, type StatusGroup } from "@/components/jobs-status";
import { ShowMore } from "@/components/more";
import { NotOnBox, Skeleton } from "@/components/page";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { countWords, int, ms, pct, words } from "@/lib/format";
import { useDebounced } from "@/lib/debounced";
import { useMe } from "@/lib/me";
import { pagedRows } from "@/lib/paged";
import { useShortcut } from "@/lib/shortcuts";
import { full, relative } from "@/lib/time";
import { ApprovalCard } from "./approvals";
import { JobPane, RunPane } from "./detail";
import { useNow } from "./live";
import { deliveredTo, jobDuration, ownQueues, runDuration, took as tookWords, retryUnlessDown } from "./model";
import { JobsArea, Label, type JobsSearch } from "./shared";

type Kind = "job" | "cron" | "workflow";
type Item = {
  id: string;
  name: string;
  kind: Kind;
  /** Where it ran, in a few words: "emails", "schedule", "workflow on worker". */
  source: string;
  status: Status;
  word: string;
  at: string;
  duration?: number;
  tries: string;
  /** More than one try: worth a second look. */
  retried: boolean;
};

/** The states each status group stands for, for jobs and for workflow runs. */
const jobStates: Record<StatusGroup, QueueJob["state"][]> = { running: ["running"], waiting: ["queued", "scheduled", "retrying"], failed: ["dead"], done: ["completed"], cancelled: ["cancelled"] };
const runStates: Record<StatusGroup, WorkflowRun["state"][]> = { running: ["running"], waiting: ["waiting"], failed: ["failed"], done: ["completed"], cancelled: ["cancelled"] };

const kinds: Array<{ value: string; label: string }> = [
  { value: "all", label: "Everything" },
  { value: "job", label: "Jobs" },
  { value: "cron", label: "Scheduled runs" },
  { value: "workflow", label: "Workflow runs" },
];

function fromJob(j: QueueJob, now: number): Item {
  const status = jobStatus(j);
  const word = status === "retrying" ? `Retrying, ${int(j.attempt)} of ${int(j.maxAttempts)}` : status === "scheduled" ? `Due ${relative(j.runAt, now)}` : status === "failed" ? "Gave up" : undefined;
  return {
    id: j.id,
    name: j.cron ?? j.queue,
    kind: j.kind === "cron" ? "cron" : "job",
    source: j.kind === "cron" ? `schedule, on ${deliveredTo(j.target)}` : j.topic ? `${deliveredTo(j.target)}, from ${j.topic}` : deliveredTo(j.target),
    status,
    word: word ?? "",
    at: j.enqueuedAt,
    duration: jobDuration(j, now),
    tries: j.attempt > 0 ? (j.maxAttempts > 1 && (j.attempt > 1 || status === "failed") ? `${int(j.attempt)} of ${int(j.maxAttempts)}` : int(j.attempt)) : "–",
    retried: j.attempt > 1,
  };
}

function fromRun(r: WorkflowRun, now: number): Item {
  const status = runStatus(r);
  const w = waitingFor(r.waitingFor);
  const word = r.state === "waiting" ? (w?.kind === "sleep" ? "Sleeping" : w?.kind === "approval" ? "Needs a person" : w?.kind === "event" ? "Waiting for an event" : "Waiting") : "";
  return {
    id: r.id,
    name: r.workflow,
    kind: "workflow",
    source: `workflow, on ${r.app}`,
    status,
    word,
    at: r.createdAt,
    duration: runDuration(r, now),
    tries: r.turns === 1 ? "1 turn" : `${int(r.turns)} turns`,
    retried: false,
  };
}

/** The page's state in one sentence: how much ran, and anything stuck. */
export function jobsSentence(all: QueueStats[], failedRuns: number): string {
  const done = all.reduce((n, x) => n + x.completedLastHour, 0);
  const waiting = all.reduce((n, x) => n + x.queued + x.retrying, 0);
  const running = all.reduce((n, x) => n + x.running, 0);
  const dead = all.reduce((n, x) => n + x.dead, 0);
  const named = ownQueues(all);
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

// The table's columns: all of them when it has the width, three beside an open run.
const wideCols = "md:grid-cols-[8.5rem_minmax(0,1.5fr)_minmax(0,1fr)_4.75rem_5rem_6.5rem]";
const narrowCols = "md:grid-cols-[8.5rem_minmax(0,1fr)_6.5rem]";

export function RunsTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Jobs`);
  const navigate = useNavigate();
  const { can } = useMe();
  const findRef = useRef<HTMLInputElement>(null);
  const kind = kinds.some((k) => k.value === search.kind && k.value !== "all") ? (search.kind as Kind) : undefined;
  const state = statusGroups.some((g) => g.state === search.state) ? (search.state as StatusGroup) : undefined;
  // Finding narrows on the box: the words reach the URL (and the query) a moment after you stop typing.
  const [find, setFind] = useState(search.q ?? "");
  const settled = useDebounced(find.trim(), 200);
  const wantJobs = kind !== "workflow";
  const wantRuns = !search.queue && (!kind || kind === "workflow");
  const jobs = useInfiniteQuery({
    ...jq.jobPages(
      project,
      { queue: search.queue, kind: kind === "job" || kind === "cron" ? [kind] : ["job", "cron"], state: state ? jobStates[state] : undefined, q: search.q },
      { enabled: wantJobs },
    ),
    retry: retryUnlessDown,
    placeholderData: keepPreviousData,
  });
  const runs = useInfiniteQuery({
    ...jq.runPages(project, { state: state ? runStates[state] : undefined, q: search.q }, { enabled: wantRuns }),
    retry: retryUnlessDown,
    placeholderData: keepPreviousData,
  });
  const recentRuns = useQuery({ ...jq.runs(project), retry: retryUnlessDown });
  const stats = useQuery({ ...jq.stats(project), retry: retryUnlessDown });
  const approvals = useQuery(jq.approvals(project));
  const manifest = useQuery({ ...api.manifest(project), retry: false });
  const jobRows = useMemo(() => (wantJobs ? pagedRows(jobs.data, (j) => j.id) : []), [wantJobs, jobs.data]);
  const runRows = useMemo(() => (wantRuns ? pagedRows(runs.data, (r) => r.id) : []), [wantRuns, runs.data]);
  const live = jobRows.some((j) => j.state === "running") || runRows.some((r) => r.state === "running");
  const now = useNow(live);

  // Jobs and workflow runs in one list, each as far back as it has loaded: the one with more to read sets where the list stops for now.
  const items = useMemo(() => {
    const horizon = [wantJobs && jobs.hasNextPage ? jobRows.at(-1)?.enqueuedAt : "", wantRuns && runs.hasNextPage ? runRows.at(-1)?.createdAt : ""].reduce<string>((h, x) => (x && x > h ? x : h), "");
    return [...jobRows.map((j) => fromJob(j, now)), ...runRows.map((r) => fromRun(r, now))].filter((i) => i.at >= horizon).sort((a, b) => b.at.localeCompare(a.at));
  }, [jobRows, runRows, wantJobs, wantRuns, jobs.hasNextPage, runs.hasNextPage, now]);
  const more = {
    hasNextPage: (wantJobs && jobs.hasNextPage) || (wantRuns && runs.hasNextPage),
    isFetchingNextPage: jobs.isFetchingNextPage || runs.isFetchingNextPage,
    isFetchNextPageError: jobs.isFetchNextPageError || runs.isFetchNextPageError,
    fetchNextPage: () => Promise.all([wantJobs && jobs.hasNextPage && jobs.fetchNextPage(), wantRuns && runs.hasNextPage && runs.fetchNextPage()]),
  };
  const selected = search.id;
  const set = (patch: Partial<JobsSearch>) => void navigate({ to: "/projects/$project/jobs", params: { project }, search: { ...search, ...patch }, replace: true });

  // j / k move through the list, like a mail client; / finds.
  const step = (by: number) => {
    const i = items.findIndex((x) => x.id === selected);
    const next = items[Math.max(0, Math.min(items.length - 1, i + by))];
    if (next && next.id !== selected) {
      set({ id: next.id });
      document.getElementById(`run-${next.id}`)?.focus();
    }
  };
  // The search box's settled text goes to the URL; only a new settled value triggers it.
  const syncQuery = useEffectEvent((text: string) => {
    if ((text || undefined) !== search.q) set({ q: text || undefined });
  });
  useEffect(() => syncQuery(settled), [settled]);
  useShortcut("j", "Next run", () => step(1));
  useShortcut("k", "Previous run", () => step(-1));
  useShortcut("/", "Find a run", () => findRef.current?.focus());
  useShortcut("Escape", "Close the run", () => set({ id: undefined }), "On this page", !!selected);

  if (jobs.isError && notOnBox(jobs.error)) return <NotOnBox what="Jobs" />;
  const waiting = (approvals.data ?? []).filter((a) => a.state === "waiting");
  const failedRuns = (recentRuns.data ?? []).filter((r) => r.state === "failed").length;
  const filtered = !!(kind || state || search.q || search.queue);
  const pending = (wantJobs && jobs.isPending) || (wantRuns && runs.isPending);
  const nothingAtAll = !filtered && !pending && !jobs.isError && !runs.isError && jobRows.length === 0 && runRows.length === 0;
  const queues = ownQueues(stats.data ?? []).map((x) => x.name);
  const editable = can("apply:reversible");

  return (
    <JobsArea project={project} tab="runs" search={search}>
      {stats.isSuccess && !nothingAtAll && <StateSentence className="mt-8">{jobsSentence(stats.data, failedRuns)}</StateSentence>}
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
      {(jobs.isError || runs.isError) && (
        <JobsTrouble
          className="mt-8"
          error={jobs.error ?? runs.error}
          retry={() => {
            if (wantJobs) void jobs.refetch();
            if (wantRuns) void runs.refetch();
            void stats.refetch();
          }}
        />
      )}
      {nothingAtAll ? (
        <JobsStart
          className="mt-8"
          title="Nothing has run yet."
          app={likelyApp(manifest.data?.manifest.apps)}
          actions={
            editable ? (
              <>
                <Button size="md" onClick={() => void navigate({ to: ".", search: (s: JobsSearch) => ({ ...s, do: "queue" }) } as never)}>
                  New queue
                </Button>
                <Button size="md" variant="ghost" onClick={() => void navigate({ to: ".", search: (s: JobsSearch) => ({ ...s, do: "schedule" }) } as never)}>
                  New schedule
                </Button>
              </>
            ) : undefined
          }
        />
      ) : jobs.isError && runs.isError ? null : (
        <div className={cn("mt-8 grid gap-x-8 gap-y-6", selected && "xl:grid-cols-[minmax(0,1fr)_minmax(0,30rem)]")}>
          <section aria-labelledby="runs-h" className={cn("min-w-0", selected && "max-xl:hidden")}>
            <h2 id="runs-h" className="sr-only">
              Latest runs
            </h2>
            {/* filters: status words with counts; kind, queue and words on the right */}
            <div className={cn("mb-3 flex flex-col gap-2", selected ? "" : "lg:flex-row lg:items-center lg:justify-between")}>
              <FilterWords label="Status" items={statusGroups} value={state} onPick={(s) => set({ state: s, id: undefined })} />
              <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
                <Select size="sm" aria-label="Kind" value={kind ?? "all"} onValueChange={(v) => set({ kind: v === "all" ? undefined : v, id: undefined })} options={kinds} className="sm:w-[9.5rem]" />
                {queues.length > 0 && (
                  <Select
                    size="sm"
                    aria-label="Queue"
                    value={search.queue ?? "all"}
                    onValueChange={(v) => set({ queue: v === "all" ? undefined : v, id: undefined })}
                    options={[{ value: "all", label: "Every queue" }, ...queues.map((x) => ({ value: x, label: <span className="font-mono text-[0.78rem]">{x}</span> }))]}
                    className="sm:w-[9.5rem]"
                  />
                )}
                <label className={cn("relative flex h-8 min-w-0 items-center sm:w-52", queues.length > 0 ? "col-span-2" : "")}>
                  <Search aria-hidden className="pointer-events-none absolute left-2.5 size-3.5 text-ink-3" />
                  <input
                    ref={findRef}
                    value={find}
                    onChange={(e) => setFind(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Escape") {
                        setFind("");
                        e.currentTarget.blur();
                      }
                    }}
                    placeholder="Find by name or ID"
                    aria-label="Find runs by name or ID"
                    aria-keyshortcuts="/"
                    spellCheck={false}
                    className="h-8 w-full rounded-[7px] border border-rule-2 bg-paper-raised pr-7 pl-8 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass"
                  />
                  {find ? (
                    <button type="button" onClick={() => setFind("")} aria-label="Clear" className="absolute right-1.5 grid size-5 place-items-center rounded-[4px] text-ink-3 hover:bg-paper-hover hover:text-ink">
                      <X className="size-3" />
                    </button>
                  ) : (
                    <kbd className="kbd pointer-events-none absolute right-2 hidden sm:block">/</kbd>
                  )}
                </label>
              </div>
            </div>
            {search.queue && (
              <p className="mb-2 flex items-center gap-1.5 text-[0.8125rem] text-ink-3">
                Only <span className="ident text-ink-2">{search.queue}</span>
                <button type="button" onClick={() => set({ queue: undefined })} aria-label="Show every queue" className="grid size-5 place-items-center rounded-[4px] hover:bg-paper-hover hover:text-ink">
                  <X className="size-3" />
                </button>
              </p>
            )}

            <div aria-hidden className={cn("label hidden gap-x-4 border-b border-rule-2 px-2 pb-2 md:grid", selected ? narrowCols : wideCols)}>
              <span>Status</span>
              <span>Run</span>
              {!selected && <span>Ran on</span>}
              {!selected && <span className="text-right">Tries</span>}
              {!selected && <span className="text-right">Took</span>}
              <span className="text-right">Started</span>
            </div>
            {pending && <Skeleton className="mt-3 h-48" />}
            <ol aria-labelledby="runs-h" aria-describedby="runs-keys" className="max-md:border-t max-md:border-rule-2">
              {items.map((it) => (
                <li key={it.id} className="[contain-intrinsic-size:auto_58px] [content-visibility:auto]">
                  <Row it={it} project={project} search={search} on={it.id === selected} compact={!!selected} now={now} />
                </li>
              ))}
            </ol>
            <p id="runs-keys" className="sr-only">
              j and k move through the runs; slash finds one.
            </p>
            {!pending && items.length === 0 && (
              <div className="border-b border-rule py-10 text-center">
                <p className="text-[0.9375rem] text-ink">{filtered ? "No runs match." : "No runs yet."}</p>
                {filtered && (
                  <button type="button" onClick={() => {
                      setFind("");
                      set({ kind: undefined, state: undefined, q: undefined, queue: undefined });
                    }} className="mt-1 text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                    Clear the filters
                  </button>
                )}
              </div>
            )}
            {!pending && <ShowMore query={more} label="Show earlier runs" />}
          </section>

          {selected && (
            <div className="min-w-0">
              <div className="xl:sticky xl:top-6">
                <div className="mb-4 flex items-center justify-between">
                  <button type="button" onClick={() => set({ id: undefined })} className="inline-flex items-center gap-1.5 text-[0.8125rem] text-ink-3 hover:text-ink xl:hidden">
                    <ArrowLeft className="size-3.5" /> All runs
                  </button>
                  <button type="button" onClick={() => set({ id: undefined })} aria-label="Close the run (Esc)" title="Close (Esc)" className="ml-auto hidden size-7 place-items-center rounded-[6px] text-ink-3 hover:bg-paper-hover hover:text-ink xl:grid">
                    <X className="size-4" />
                  </button>
                </div>
                {selected.startsWith("run_") ? <RunPane key={selected} project={project} id={selected} /> : <JobPane key={selected} project={project} id={selected} />}
              </div>
            </div>
          )}
        </div>
      )}
    </JobsArea>
  );
}

function Row({ it, project, search, on, compact, now }: { it: Item; project: string; search: JobsSearch; on: boolean; compact: boolean; now: number }) {
  const took = it.duration !== undefined ? tookWords(it.duration) : "–";
  return (
    <Link
      id={`run-${it.id}`}
      to="/projects/$project/jobs"
      params={{ project }}
      search={{ ...search, id: it.id }}
      replace
      aria-current={on ? "true" : undefined}
      className={cn(
        "grid items-center gap-x-4 border-b border-rule px-2 py-2.5 transition-colors duration-[var(--dur-state)] outline-hidden hover:bg-paper-hover/60 focus-visible:bg-paper-hover",
        "grid-cols-[minmax(0,1fr)_auto]",
        compact ? narrowCols : wideCols,
        on && "bg-paper-select hover:bg-paper-select",
      )}
    >
      {/* phone: two lines; desktop: the cells */}
      <span className="hidden min-w-0 md:block">
        <StatusLabel status={it.status} word={it.word || undefined} />
      </span>
      <span className="min-w-0">
        <span className="flex min-w-0 items-center gap-2">
          <StatusLabel status={it.status} word="" className="md:hidden" />
          <span className="ident truncate text-[0.84375rem] text-ink">{it.name}</span>
        </span>
        <span className="mt-0.5 block truncate font-mono text-[0.6875rem] text-ink-3">
          {it.id}
          <span className="font-sans md:hidden">
            {" "}
            · {it.word || statusWord[it.status]} · {took}
          </span>
        </span>
      </span>
      {!compact && (
        <span className="hidden min-w-0 truncate text-[0.8125rem] text-ink-2 md:block">
          {it.source}
        </span>
      )}
      {!compact && (
        <span className={cn("hidden text-right text-[0.8125rem] tnum md:block", it.retried ? "text-warn-ink" : "text-ink-3")}>
          {it.tries}
        </span>
      )}
      {!compact && (
        <span className={cn("hidden text-right text-[0.8125rem] tnum md:block", it.status === "running" ? "text-ink" : "text-ink-2")}>
          {took}
        </span>
      )}
      <span className="text-right text-[0.8125rem] whitespace-nowrap text-ink-3 tnum">
        <time dateTime={it.at} title={full(it.at)}>
          {relative(it.at, now)}
        </time>
      </span>
    </Link>
  );
}
