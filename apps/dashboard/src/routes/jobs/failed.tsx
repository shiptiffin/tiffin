// Failed: every job that ran out of tries (by queue, each with Retry all)
// and every workflow run that stopped, with Retry from the failed step.
// Retrying is safe to click; emptying a queue's failed jobs asks first.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError, notOnBox } from "@/api/client";
import { jq } from "@/api/jobs";
import { mod2, type QueueJob, type WorkflowRun } from "@/api/modules";
import { useTitle } from "@/components/favicon";
import { HazardDialog } from "@/components/hazard";
import { EmptyJobs, jobError, StateSentence } from "@/components/jobs-words";
import { NotOnBox, Skeleton } from "@/components/page";
import { ProblemNote, sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { countWords, int } from "@/lib/format";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";
import { JobsArea, Label, type JobsSearch } from "./shared";

export function FailedTab({ project, search }: { project: string; search: JobsSearch }) {
  useTitle(`${project} · Failed jobs`);
  const qc = useQueryClient();
  const { can } = useMe();
  const dead = useQuery({ ...jq.jobs(project, undefined, "dead"), refetchInterval: 15_000 });
  const runs = useQuery(jq.runs(project, "failed"));
  const [preview, setPreview] = useState<{ count: number; message: string } | null>(null);
  const [emptying, setEmptying] = useState<string | null>(null);
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["jobs", project] });
    void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
    void qc.invalidateQueries({ queryKey: ["runs", project] });
  };
  const dry = useMutation({ mutationFn: () => mod2.replay(project, true), onSuccess: (r) => setPreview(r) });
  const all = useMutation({
    // A schedule's runs share one system queue with the other schedules: retry them one by one.
    mutationFn: async (g?: { queue: string; jobs: QueueJob[] }) =>
      g && g.jobs[0].cron ? Promise.all(g.jobs.map((j) => mod2.retryJob(project, j.id))).then((r) => ({ count: r.length })) : mod2.replay(project, false, g?.queue),
    onSuccess: (r, g) => {
      const queue = g?.jobs[0].cron ?? g?.queue;
      setPreview(null);
      refresh();
      toast({ title: `Retrying ${countWords(r.count, "job")}${queue ? ` on ${queue}` : ""}.`, detail: "Each one gets a fresh set of tries." });
    },
    onError: (e) => toast({ title: "Couldn’t retry them.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  if (dead.isError && notOnBox(dead.error)) return <NotOnBox what="Jobs" />;
  const jobs = (dead.data ?? []).filter((j) => j.kind !== "workflow");
  const byQueue = new Map<string, QueueJob[]>();
  for (const j of jobs) byQueue.set(j.cron ? `schedule ${j.cron}` : j.queue, [...(byQueue.get(j.cron ? `schedule ${j.cron}` : j.queue) ?? []), j]);
  const failedRuns = runs.data ?? [];
  const total = jobs.length + failedRuns.length;
  return (
    <JobsArea
      project={project}
      tab="failed"
      search={search}
      actions={
        can("apply:reversible") && byQueue.size > 1 && !preview ? (
          <Button variant="primary" onClick={() => dry.mutate()} disabled={dry.isPending}>
            Retry all…
          </Button>
        ) : (
          <span />
        )
      }
    >
      {dead.isSuccess && runs.isSuccess && (
        <StateSentence className="mt-8">
          {total === 0 ? "Nothing failed. A job lands here after its last try fails." : `${countWords(total, "run", "runs", true)} failed for good. Fix the cause, then retry.`}
        </StateSentence>
      )}
      {preview && (
        <div className="mt-6 max-w-[44rem] animate-pop rounded-[10px] border border-rule-2 bg-paper-raised px-4 py-3.5 shadow-raised">
          <p className="text-[0.9375rem] text-ink">{sentence(preview.message)}</p>
          <p className="mt-1 text-sm text-ink-3">Nothing was sent yet. Each job gets a fresh set of tries.</p>
          <div className="mt-3 flex justify-end gap-2">
            <Button size="md" variant="ghost" onClick={() => setPreview(null)}>
              Not now
            </Button>
            <Button size="md" variant="primary" onClick={() => all.mutate(undefined)} disabled={all.isPending || preview.count === 0}>
              Retry {countWords(preview.count, "job")}
            </Button>
          </div>
        </div>
      )}
      {(dry.isError || dead.isError || runs.isError) && <ProblemNote className="mt-4" error={dry.error ?? dead.error ?? runs.error} />}
      {dead.isPending && <Skeleton className="mt-8 h-32" />}
      {dead.isSuccess && runs.isSuccess && total === 0 && <EmptyJobs title="Nothing failed." className="mt-4" />}

      <div className="mt-8 grid gap-10">
        {[...byQueue.entries()].map(([queue, list]) => {
          const real = list[0].cron ? list[0].queue : queue;
          return (
            <section key={queue} aria-labelledby={`f-${queue}`}>
              <Label
                id={`f-${queue}`}
                action={
                  can("apply:reversible") ? (
                    <span className="flex gap-1.5">
                      {can("apply:irreversible") && !list[0].cron && (
                        <Button size="sm" variant="danger-quiet" onClick={() => setEmptying(real)}>
                          {list.length === 1 ? "Delete it…" : "Delete them…"}
                        </Button>
                      )}
                      {list.length > 1 && (
                        <Button size="sm" onClick={() => all.mutate({ queue: real, jobs: list })} disabled={all.isPending}>
                          Retry all {int(list.length)}
                        </Button>
                      )}
                    </span>
                  ) : undefined
                }
              >
                <span className="normal-case">
                  <span className="ident">{queue}</span> <span className="text-danger tnum">{int(list.length)}</span>
                </span>
              </Label>
              <ul className="divide-y divide-rule border-y border-rule">
                {list.slice(0, 20).map((j) => (
                  <DeadRow key={j.id} project={project} j={j} onDone={refresh} />
                ))}
              </ul>
              {list.length > 20 && <p className="mt-2 text-[0.8125rem] text-ink-3">And {int(list.length - 20)} more; Retry all sends every one.</p>}
            </section>
          );
        })}
        {failedRuns.length > 0 && (
          <section aria-labelledby="f-runs">
            <Label id="f-runs">Workflow runs</Label>
            <ul className="divide-y divide-rule border-y border-rule">
              {failedRuns.map((r) => (
                <FailedRun key={r.id} project={project} r={r} onDone={refresh} />
              ))}
            </ul>
          </section>
        )}
      </div>

      <HazardDialog<{ count: number; message: string }, { count: number }>
        open={!!emptying}
        onOpenChange={(o) => !o && setEmptying(null)}
        title={`Delete the failed jobs on ${emptying ?? ""}`}
        word={emptying ?? ""}
        action={`Delete the jobs on ${emptying ?? ""}`}
        run={async (confirm) => {
          const r = await mod2.purge(project, emptying!, confirm, true);
          if (!r.purged) throw new ApiError({ status: 428, code: "confirm", title: "Confirm", confirm: r.confirm, preview: { count: r.count, message: r.message } } as never);
          return r;
        }}
        renderPreview={(p) => (
          <p className="text-[0.9375rem] text-ink">
            Deletes the {countWords(p.count, "job")} waiting or failed on <code className="ident">{emptying}</code>, for good. Jobs already running finish. The queue itself stays.
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

function DeadRow({ project, j, onDone }: { project: string; j: QueueJob; onDone: () => void }) {
  const { can } = useMe();
  const retry = useMutation({
    mutationFn: () => mod2.retryJob(project, j.id),
    onSuccess: () => {
      onDone();
      toast({ title: `Sent ${j.id} back to ${j.queue}.`, detail: "It gets a fresh set of tries.", action: { label: "Undo", run: async () => mod2.cancelJob(project, j.id).then(onDone) } });
    },
    onError: (e) => toast({ title: "Couldn’t retry that job.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const err = jobError(j.lastError);
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 py-3">
      <Link to="/projects/$project/jobs/$id" params={{ project, id: j.id }} className="group min-w-0">
        <span className="flex flex-wrap items-baseline gap-x-2 text-[0.8125rem] text-ink-3">
          <span className="ident text-ink group-hover:underline group-hover:underline-offset-4">{j.id}</span>
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
        <Button size="sm" variant="ghost" onClick={() => retry.mutate()} disabled={retry.isPending}>
          Retry
        </Button>
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
    onError: (e) => toast({ title: "Couldn’t retry it.", detail: e instanceof ApiError ? (e.problem.detail ?? e.message) : String(e), tone: "danger" }),
  });
  const err = jobError(r.error);
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3 py-3">
      <Link to="/projects/$project/jobs/$id" params={{ project, id: r.id }} className="group min-w-0">
        <span className="flex flex-wrap items-baseline gap-x-2 text-[0.8125rem] text-ink-3">
          <span className="ident text-ink group-hover:underline group-hover:underline-offset-4">{r.workflow}</span>
          <span>{relative(r.finishedAt ?? r.updatedAt)}</span>
        </span>
        {err && <span className="mt-1 block truncate font-mono text-[0.75rem] text-ink-2">{err.text}</span>}
      </Link>
      {can("apply:reversible") && (
        <Button size="sm" variant="ghost" onClick={() => retry.mutate()} disabled={retry.isPending}>
          Retry from the failed step
        </Button>
      )}
    </li>
  );
}
