import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check } from "lucide-react";
import { useState } from "react";
import { mod2, type WorkflowApproval } from "@/api/modules";
import { cn } from "@/lib/cn";
import { useMe } from "@/lib/me";
import { clock, relative } from "@/lib/time";
import { ProblemNote } from "./problem";
import { toast } from "./toast";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

/**
 * A workflow step waiting for a person ("Ship order #1043?"), decided right
 * where it's shown: on the Ledger (compact, the note folded away) and on
 * Approvals. Workflow steps don't change the box, so no passkey is needed.
 */
export function WorkflowRow({ w, compact }: { w: WorkflowApproval & { project: string }; compact?: boolean }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const [note, setNote] = useState("");
  const [noting, setNoting] = useState(!compact);
  const title = w.title || w.step;
  const decide = useMutation({
    mutationFn: (d: "approve" | "reject") => mod2.decide(w.project, w.id, d, note.trim() || undefined),
    onSuccess: (_, d) => {
      qc.invalidateQueries({ queryKey: ["wf-approvals", w.project] });
      qc.invalidateQueries({ queryKey: ["runs", w.project] });
      qc.invalidateQueries({ queryKey: ["run", w.project, w.runId] });
      toast({
        title: d === "approve" ? `Approved: ${title.replace(/\?$/, "")}.` : `Rejected: ${title.replace(/\?$/, "")}.`,
        detail: d === "approve" ? `The ${w.workflow} run carries on.` : `The ${w.workflow} run takes its “rejected” path.`,
      });
    },
  });
  return (
    <div className={cn("grid grid-cols-[44px_minmax(0,1fr)] gap-x-3", compact ? "py-2.5" : "py-3.5")}>
      <div className="pt-px text-[0.78125rem] leading-5 text-ink-3 tnum">
        {clock(w.createdAt)}
        <div className="leading-4">asked</div>
      </div>
      <div className={cn("flex min-w-0 flex-col gap-2.5", compact ? "sm:flex-row sm:items-center sm:gap-6" : "md:flex-row md:items-start md:gap-6")}>
        <div className="min-w-0 flex-1">
          <p className="truncate text-[0.78125rem] leading-[1.125rem] text-ink-2">
            Workflow{" "}
            <Link to="/projects/$project/jobs/$id" params={{ project: w.project, id: w.runId }} className="ident text-[0.71875rem] text-ink-2 hover:text-ink">
              {w.workflow}
            </Link>{" "}
            in {w.project} · step “{w.step}”
          </p>
          <p className="entry mt-0.5 text-ink">{title}</p>
          {w.description && !compact && <p className="mt-1 text-[0.8125rem] text-ink-2">{w.description}</p>}
          <p className="mt-1 text-xs text-ink-3">
            {w.timeoutAt ? `Times out ${relative(w.timeoutAt)}` : "Waits until someone decides"}
            {w.humanOnly && " · people only"}
            {compact && w.description && <span className="max-sm:hidden"> · {w.description}</span>}
          </p>
        </div>
        {can("apply:reversible") && (
          <div className={cn("flex shrink-0 flex-col gap-2", compact ? "sm:w-auto" : "md:w-60")}>
            {noting && (
              <Input
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder="Note for the run (optional)"
                className="h-8 text-[0.8125rem]"
                aria-label="Note for the run"
                autoFocus={compact}
              />
            )}
            {/* The same order as every workflow approval (Workflows, a run): the note, Reject, then Approve last. */}
            <div className="flex justify-end gap-2">
              {!noting && (
                <Button size="md" variant="ghost" onClick={() => setNoting(true)}>
                  Note…
                </Button>
              )}
              <Button size={compact ? "md" : "sm"} variant="ghost" onClick={() => decide.mutate("reject")} disabled={decide.isPending}>
                Reject
              </Button>
              {/* In the Ledger's waiting card the agent's Review is the one brass button; this one stays quiet there. */}
              <Button size={compact ? "md" : "sm"} variant={compact ? "secondary" : "primary"} onClick={() => decide.mutate("approve")} disabled={decide.isPending}>
                <Check />
                Approve
              </Button>
            </div>
          </div>
        )}
        {decide.isError && <ProblemNote className="mt-1" error={decide.error} />}
      </div>
    </div>
  );
}
