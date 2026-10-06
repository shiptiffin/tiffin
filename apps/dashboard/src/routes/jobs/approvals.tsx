// A workflow's questions for people: decide an approval inline, or send the
// event a run is waiting for.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { mod2, type WorkflowApproval } from "@/api/modules";
import { ProblemNote, sentence } from "@/components/problem";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/cn";
import { useMe } from "@/lib/me";
import { relative } from "@/lib/time";

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
        {!inRun && (
          <Link to="/projects/$project/jobs/$id" params={{ project, id: a.runId }} className="ident text-[0.75rem] text-ink-2 hover:text-ink hover:underline">
            {a.workflow}
          </Link>
        )}
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

export function SendEvent({ project, runId, event }: { project: string; runId: string; event: string }) {
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
