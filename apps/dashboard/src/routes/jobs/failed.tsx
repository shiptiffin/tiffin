// Failed: the dead-letter list. Every job that ran out of tries, by queue,
// with its error's first line; tick some (or a whole queue) to retry or
// discard them together. Workflow runs that stopped sit below, each with
// Retry from the failed step. Retrying is safe to click; discarding asks
// first (it keeps the record, so a discarded job can still be replayed).
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, Minus, MoreHorizontal } from "lucide-react";
import { Checkbox as C } from "radix-ui";
import { useMemo, useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { jq } from "@/api/jobs";
import { mod2, type QueueJob, type WorkflowRun } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { JobsTrouble } from "@/components/jobs-start";
import { EmptyJobs, jobError, StateSentence } from "@/components/jobs-words";
import { ShowMore } from "@/components/more";
import { NotOnBox, Skeleton } from "@/components/page";
import { sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { countWords, int } from "@/lib/format";
import { useMe } from "@/lib/me";
import { countShown, pagedRows } from "@/lib/paged";
import { full, relative } from "@/lib/time";
import { retryUnlessDown } from "./model";
import { JobsArea, type JobsSearch } from "./shared";

const problem = (e: unknown) => (e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e));
const firstLine = (e?: string) => (jobError(e)?.text ?? "").split("\n")[0];

/** Runs `f` for each ID, a few at a time; says how many worked and the first reason one didn't. */
async function each(ids: string[], f: (id: string) => Promise<unknown>): Promise<{ ok: number; failed: number; reason?: string }> {
  let ok = 0;
  let failed = 0;
  let reason: string | undefined;
  for (let i = 0; i < ids.length; i += 8) {
    const r = await Promise.allSettled(ids.slice(i, i + 8).map(f));
    for (const x of r) {
      if (x.status === "fulfilled") ok++;
      else {
        failed++;
        reason ??= problem(x.reason);
      }
    }
  }
  return { ok, failed, reason };
}

/** A checkbox that can be half-ticked (some of a group): Radix's own, drawn like ui/choice's, with a dash for "some". */
function Checkbox({ checked, onCheckedChange, ...rest }: { checked: boolean | "indeterminate"; onCheckedChange: (v: boolean | "indeterminate") => void; "aria-label": string }) {
  return (
    <C.Root
      checked={checked}
      onCheckedChange={onCheckedChange}
      {...rest}
      className="grid size-4 shrink-0 place-items-center rounded-[4px] border border-rule-2 bg-paper transition-colors hover:border-ink-3 data-[state=checked]:border-ink data-[state=checked]:bg-ink data-[state=indeterminate]:border-ink data-[state=indeterminate]:bg-ink"
    >
      <C.Indicator className="animate-pop text-paper">{checked === "indeterminate" ? <Minus className="size-3" strokeWidth={3} /> : <Check className="size-3" strokeWidth={3} />}</C.Indicator>
    </C.Root>
  );
}

export function FailedTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Failed jobs`);
  const qc = useQueryClient();
  const { can } = useMe();
  const editable = can("apply:reversible");
  const dead = useInfiniteQuery({ ...jq.jobPages(project, { state: ["dead"], kind: ["job", "cron"] }), refetchInterval: 15_000, retry: retryUnlessDown });
  const runs = useInfiniteQuery({ ...jq.runPages(project, { state: ["failed"] }), retry: retryUnlessDown });
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [discarding, setDiscarding] = useState<string[] | null>(null);
  const [emptying, setEmptying] = useState<string | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["jobs", project] });
    void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
    void qc.invalidateQueries({ queryKey: ["runs", project] });
  };
  const jobs = useMemo(() => pagedRows(dead.data, (j) => j.id), [dead.data]);
  const groups = useMemo(() => {
    const by = new Map<string, QueueJob[]>();
    for (const j of jobs) {
      const k = j.cron ? `schedule ${j.cron}` : j.queue;
      by.set(k, [...(by.get(k) ?? []), j]);
    }
    return [...by.entries()];
  }, [jobs]);
  // Only what's still on the list stays ticked.
  const live = new Set(jobs.map((j) => j.id));
  const chosen = [...picked].filter((id) => live.has(id));
  const toggle = (ids: string[], on: boolean) =>
    setPicked((p) => {
      const n = new Set(p);
      for (const id of ids) {
        if (on) n.add(id);
        else n.delete(id);
      }
      return n;
    });

  const retry = useMutation({
    mutationFn: (ids: string[]) => each(ids, (id) => mod2.retryJob(project, id)),
    onSuccess: (r, ids) => {
      refresh();
      toggle(ids, false);
      if (r.failed) toast({ title: `Retrying ${countWords(r.ok, "job")}; ${countWords(r.failed, "job")} didn’t go.`, detail: r.reason ? sentence(r.reason) : undefined, tone: "danger" });
      else toast({ title: `Retrying ${countWords(r.ok, "job")}.`, detail: "Each one gets a fresh set of tries." });
    },
  });
  const discard = useMutation({
    mutationFn: (ids: string[]) => each(ids, (id) => mod2.cancelJob(project, id)),
    onSuccess: (r, ids) => {
      refresh();
      toggle(ids, false);
      if (r.failed) toast({ title: `Discarded ${countWords(r.ok, "job")}; ${countWords(r.failed, "job")} stayed.`, detail: r.reason ? sentence(r.reason) : undefined, tone: "danger" });
      else toast({ title: `Discarded ${countWords(r.ok, "job")}.`, detail: "They left Failed. Each keeps its record and can be replayed from its page." });
    },
  });

  if (dead.isError && notOnBox(dead.error)) return <NotOnBox what="Jobs" />;
  const failedRuns = pagedRows(runs.data, (r) => r.id);
  const total = jobs.length + failedRuns.length;
  const partial = dead.hasNextPage || runs.hasNextPage;
  const busy = retry.isPending || discard.isPending;
  return (
    <JobsArea project={project} tab="failed" search={search}>
      {dead.isSuccess && runs.isSuccess && total > 0 && (
        <StateSentence className="mt-8">
          {`${partial ? "At least " : ""}${countWords(total, "run", "runs", !partial)} failed for good${groups.length > 1 ? `, across ${countWords(groups.length + (failedRuns.length ? 1 : 0), "place")}` : ""}. Fix the cause, then retry.`}
        </StateSentence>
      )}
      {(dead.isError || runs.isError) && (
        <JobsTrouble
          className="mt-8"
          error={dead.error ?? runs.error}
          retry={() => {
            void dead.refetch();
            void runs.refetch();
          }}
        />
      )}
      {dead.isPending && <Skeleton className="mt-8 h-40" />}
      {dead.isSuccess && runs.isSuccess && total === 0 && (
        <EmptyJobs title="Nothing failed." className="mt-6">
          A job lands here after its last try fails, or when its handler throws <code className="ident text-ink">NonRetryableError</code>. You can retry it, or discard it once it no longer matters.
        </EmptyJobs>
      )}

      {/* the bulk bar: what's ticked, and what to do with it */}
      {editable && jobs.length > 0 && (
        <div
          className={cn(
            "sticky top-0 z-10 mt-8 -mx-2 flex min-h-11 flex-wrap items-center gap-x-3 gap-y-2 rounded-[10px] px-2 py-1.5 transition-colors",
            chosen.length ? "border border-rule-2 bg-paper-raised shadow-raised" : "border border-transparent",
          )}
          aria-live="polite"
        >
          <label className="flex items-center gap-2.5 text-[0.8125rem] text-ink-2">
            <Checkbox
              checked={chosen.length === 0 ? false : chosen.length === jobs.length ? true : "indeterminate"}
              onCheckedChange={(v) => (v === true ? toggle(jobs.map((j) => j.id), true) : setPicked(new Set()))}
              aria-label="Pick every failed job"
            />
            {chosen.length ? `${countWords(chosen.length, "job")} picked` : `Pick jobs to retry or discard them together`}
          </label>
          {chosen.length > 0 && (
            <span className="ml-auto flex gap-1.5">
              <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())}>
                Clear
              </Button>
              <Button size="sm" variant="danger-quiet" onClick={() => setDiscarding(chosen)} disabled={busy}>
                Discard…
              </Button>
              <Button size="sm" variant="primary" onClick={() => retry.mutate(chosen)} disabled={busy}>
                Retry {int(chosen.length)}
              </Button>
            </span>
          )}
        </div>
      )}

      <div className={cn("grid gap-10", editable && jobs.length > 0 ? "mt-4" : "mt-8")}>
        {groups.map(([queue, list]) => {
          const real = list[0].cron ? list[0].queue : queue;
          const ids = list.map((j) => j.id);
          const on = ids.filter((id) => chosen.includes(id)).length;
          return (
            <section key={queue} aria-labelledby={`f-${queue}`}>
              <div className="mb-2 flex min-h-8 items-center gap-2.5 px-2">
                {editable && (
                  <Checkbox checked={on === 0 ? false : on === ids.length ? true : "indeterminate"} onCheckedChange={(v) => toggle(ids, v === true)} aria-label={`Pick every failed job on ${queue}`} />
                )}
                <h2 id={`f-${queue}`} className="flex min-w-0 items-baseline gap-2 text-[0.875rem]">
                  {list[0].cron ? (
                    <>
                      <span className="text-ink-3">schedule</span>
                      <Link to="/projects/$project/jobs/schedules" params={{ project }} className="ident truncate text-ink hover:underline">
                        {list[0].cron}
                      </Link>
                    </>
                  ) : (
                    <Link to="/projects/$project/jobs" params={{ project }} search={{ queue }} className="ident truncate text-ink hover:underline">
                      {queue}
                    </Link>
                  )}
                  <span className="text-[0.8125rem] text-danger tnum">{int(list.length)}</span>
                </h2>
                {editable && (
                  <span className="ml-auto flex items-center gap-1">
                    <Button size="sm" variant="ghost" onClick={() => retry.mutate(ids)} disabled={busy}>
                      {list.length === 1 ? "Retry it" : `Retry all ${int(list.length)}`}
                    </Button>
                    <Menu>
                      <MenuTrigger asChild>
                        <Button size="icon-sm" variant="ghost" aria-label={`More for ${queue}`}>
                          <MoreHorizontal />
                        </Button>
                      </MenuTrigger>
                      <MenuContent align="end" className="min-w-64">
                        <MenuItem onSelect={() => setDiscarding(ids)}>{list.length === 1 ? "Discard it…" : `Discard all ${int(list.length)}…`}</MenuItem>
                        {can("apply:irreversible") && !list[0].cron && (
                          <MenuItem onSelect={() => setEmptying(real)} variant="danger">
                            Delete every waiting and failed job…
                          </MenuItem>
                        )}
                      </MenuContent>
                    </Menu>
                  </span>
                )}
              </div>
              <ul className="divide-y divide-rule border-y border-rule">
                {list.slice(0, 50).map((j) => (
                  <DeadRow key={j.id} project={project} j={j} picked={chosen.includes(j.id)} onPick={editable ? (v) => toggle([j.id], v) : undefined} onRetry={() => retry.mutate([j.id])} onDiscard={() => discard.mutate([j.id])} busy={busy} />
                ))}
              </ul>
              {list.length > 50 && <p className="mt-2 px-2 text-[0.8125rem] text-ink-3">And {int(list.length - 50)} more; Retry all sends every one.</p>}
            </section>
          );
        })}
        {failedRuns.length > 0 && (
          <section aria-labelledby="f-runs">
            <h2 id="f-runs" className="mb-2 flex min-h-8 items-center gap-2 px-2 text-[0.875rem] text-ink">
              Workflow runs <span className="text-[0.8125rem] text-danger tnum">{countShown(failedRuns.length, runs.hasNextPage)}</span>
            </h2>
            <ul className="divide-y divide-rule border-y border-rule">
              {failedRuns.map((r) => (
                <FailedRun key={r.id} project={project} r={r} onDone={refresh} />
              ))}
            </ul>
            <ShowMore query={runs} label="Show more failed runs" />
          </section>
        )}
      </div>
      {dead.isSuccess && <ShowMore query={dead} label="Show more failed jobs" />}

      <Confirm
        open={!!discarding}
        onClose={() => setDiscarding(null)}
        tone="normal"
        title={discarding?.length === 1 ? `Discard ${discarding[0]}?` : `Discard ${countWords(discarding?.length ?? 0, "failed job")}?`}
        body="They leave Failed and won’t be retried. Each keeps its tries and payload, so you can still replay one from its page."
        action={discarding?.length === 1 ? "Discard it" : `Discard ${int(discarding?.length ?? 0)}`}
        run={async () => {
          const r = await discard.mutateAsync(discarding ?? []);
          if (r.ok === 0 && r.failed) throw new Error(r.reason ?? "None of them could be discarded.");
        }}
        done={() => setDiscarding(null)}
      />

      <HazardDialog<{ count: number; message: string }, { count: number }>
        open={!!emptying}
        onOpenChange={(o) => !o && setEmptying(null)}
        title={`Delete the jobs on ${emptying ?? ""}`}
        word={emptying ?? ""}
        action={`Delete the jobs on ${emptying ?? ""}`}
        run={async (confirm) => {
          const r = await mod2.purge(project, emptying!, confirm, true);
          if (!r.purged) throw new ApiError({ status: 428, code: "confirm", title: "Confirm", confirm: r.confirm, preview: { count: r.count, message: r.message } } as never);
          return r;
        }}
        renderPreview={(p) => (
          <p className="text-[0.9375rem] text-ink">
            Deletes the {countWords(p.count, "job")} waiting or failed on <code className="ident">{emptying}</code>, for good: no record stays. Jobs already running finish. The queue itself stays.
          </p>
        )}
        onDone={(r) => {
          refresh();
          toast({ title: `Deleted ${countWords(r.count, "job")} from ${emptying}.` });
        }}
      />
    </JobsArea>
  );
}

function DeadRow({
  project,
  j,
  picked,
  onPick,
  onRetry,
  onDiscard,
  busy,
}: {
  project: string;
  j: QueueJob;
  picked: boolean;
  onPick?: (v: boolean) => void;
  onRetry: () => void;
  onDiscard: () => void;
  busy: boolean;
}) {
  const err = firstLine(j.lastError);
  const gaveUp = jobError(j.lastError)?.gaveUp;
  return (
    <li className={cn("group grid items-center gap-x-3 px-2 py-2.5 transition-colors", onPick ? "grid-cols-[1rem_minmax(0,1fr)_auto]" : "grid-cols-[minmax(0,1fr)_auto]", picked && "bg-paper-select")}>
      {onPick && <Checkbox checked={picked} onCheckedChange={(v) => onPick(v === true)} aria-label={`Pick ${j.id}`} />}
      <Link to="/projects/$project/jobs/$id" params={{ project, id: j.id }} className="min-w-0">
        <span className="flex flex-wrap items-baseline gap-x-2 text-[0.8125rem] text-ink-3">
          <span className="font-mono text-[0.75rem] text-ink group-hover:underline group-hover:underline-offset-4">{j.id}</span>
          <span className="tnum">
            {countWords(j.attempt, "try", "tries")}
            {j.lastStatus && j.lastStatus !== 489 ? ` · HTTP ${j.lastStatus}` : gaveUp ? " · not retried" : ""} ·{" "}
            <time dateTime={j.finishedAt ?? j.enqueuedAt} title={full(j.finishedAt ?? j.enqueuedAt)}>
              {relative(j.finishedAt ?? j.enqueuedAt)}
            </time>
          </span>
        </span>
        {err && (
          <span className="mt-0.5 block truncate font-mono text-[0.75rem] text-ink-2" title={err}>
            {err}
          </span>
        )}
      </Link>
      {onPick && (
        <span className="flex items-center gap-0.5">
          <Button size="sm" variant="ghost" onClick={onRetry} disabled={busy}>
            Retry
          </Button>
          <Menu>
            <MenuTrigger asChild>
              <Button size="icon-sm" variant="ghost" aria-label={`More for ${j.id}`}>
                <MoreHorizontal />
              </Button>
            </MenuTrigger>
            <MenuContent align="end">
              <MenuItem asChild>
                <Link to="/projects/$project/jobs/$id" params={{ project, id: j.id }}>
                  Open it
                </Link>
              </MenuItem>
              <MenuItem onSelect={onDiscard}>Discard</MenuItem>
            </MenuContent>
          </Menu>
        </span>
      )}
    </li>
  );
}

function FailedRun({ project, r, onDone }: { project: string; r: WorkflowRun; onDone: () => void }) {
  const { can } = useMe();
  const retry = useMutation({
    mutationFn: () => mod2.retryRun(project, r.id),
    onSuccess: () => {
      onDone();
      toast({ title: `Retrying ${r.workflow} from the step that failed.`, detail: "Steps that finished keep their results." });
    },
    onError: (e) => toast({ title: "Couldn’t retry it.", detail: problem(e), tone: "danger" }),
  });
  const err = firstLine(r.error);
  const step = (r.steps ?? []).find((s) => s.state === "failed");
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 px-2 py-2.5">
      <Link to="/projects/$project/jobs/$id" params={{ project, id: r.id }} className="group min-w-0">
        <span className="flex flex-wrap items-baseline gap-x-2 text-[0.8125rem] text-ink-3">
          <span className="ident text-ink group-hover:underline group-hover:underline-offset-4">{r.workflow}</span>
          {step && <span>at “{step.title ?? step.name}”</span>}
          <span>· {relative(r.finishedAt ?? r.updatedAt)}</span>
        </span>
        {err && <span className="mt-0.5 block truncate font-mono text-[0.75rem] text-ink-2">{err}</span>}
      </Link>
      {can("apply:reversible") && (
        <Button size="sm" variant="ghost" onClick={() => retry.mutate()} disabled={retry.isPending}>
          Retry<span className="max-sm:hidden"> from the failed step</span>
        </Button>
      )}
    </li>
  );
}
