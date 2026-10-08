import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError, request, type ApplyResult, type Plan } from "@/api/client";
import { asTier } from "@/lib/changes";
import { lossEmpty, lossParts } from "./loss";
import { ProblemNote } from "./problem";
import { Skeleton } from "./page";
import { toast } from "./toast";
import { Button } from "./ui/button";
import {
  AlertDialog,
  AlertDialogBody,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "./ui/alert-dialog";

const destroy = (project: string, confirm?: string) =>
  request<ApplyResult>("POST", `/v1/projects/${encodeURIComponent(project)}/destroy`, confirm ? { confirm, intent: `Delete ${project}` } : {});

/**
 * Delete a project: the box plans removing everything in it (POST destroy
 * without confirm answers 428 with that plan), the dialog says exactly what
 * goes, the person types the name, and Delete applies that plan's hash.
 */
export function DeleteProject({
  project,
  label,
  variant = "danger-quiet",
  onDeleted,
}: {
  project: string;
  label?: string;
  variant?: "danger-quiet" | "danger-outline";
  onDeleted?: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant={variant} size="md" onClick={() => setOpen(true)}>
        {label ?? `Delete ${project}…`}
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent tone="danger" className="max-w-md">
          {open && <Body project={project} onClose={() => setOpen(false)} onDeleted={onDeleted} />}
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

/** Without onDeleted, deleting goes back to Projects. */
function Body({ project, onClose, onDeleted }: { project: string; onClose: () => void; onDeleted?: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [typed, setTyped] = useState("");
  const plan = useQuery({
    queryKey: ["destroy-plan", project],
    queryFn: async () => {
      try {
        await destroy(project);
        return null;
      } catch (e) {
        if (e instanceof ApiError && e.status === 428 && e.problem.plan) return e.problem.plan as Plan;
        throw e;
      }
    },
    retry: false,
    staleTime: 0,
  });
  const run = useMutation({
    mutationFn: () => destroy(project, plan.data!.hash),
    onSuccess: () => {
      void qc.invalidateQueries();
      onClose();
      toast({ title: `Deleted ${project}.`, detail: "Its databases keep a snapshot and its files sit in the trash for 7 days." });
      if (onDeleted) onDeleted();
      else void navigate({ to: "/" });
    },
  });
  const losses = (plan.data?.ops ?? []).filter((o) => asTier(o.risk) === "irreversible" && o.loss && !lossEmpty(o.loss)).flatMap((o) => lossParts(o.loss!));
  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>Delete {project}?</AlertDialogTitle>
        <AlertDialogDescription>Its apps stop and its address goes away. Everything in it is deleted.</AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogBody className="flex flex-col gap-4">
        {plan.isPending ? (
          <Skeleton className="h-14" />
        ) : plan.isError ? (
          <ProblemNote error={plan.error} />
        ) : (
          <div className="rounded-[10px] bg-danger-wash px-3.5 py-3 text-[0.9375rem] text-ink">
            {losses.length > 0 ? (
              <p>
                <b className="font-[550]">{losses.join(" · ")}</b> will be gone.
              </p>
            ) : (
              <p>There’s no data in it yet.</p>
            )}
            <p className="mt-1 text-sm text-ink-2">Databases keep a snapshot and files sit in the trash for 7 days, then they’re gone for good.</p>
          </div>
        )}
        <label className="block text-sm text-ink-2">
          Type <b className="ident font-[550] text-ink">{project}</b> to confirm
          <input
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-label={`Type ${project} to confirm`}
            className="ident mt-1.5 h-9 w-full rounded-[8px] border border-rule-2 bg-paper px-2.5 text-ink focus-visible:border-danger focus-visible:outline-hidden"
          />
        </label>
        {run.isError && <ProblemNote error={run.error} />}
      </AlertDialogBody>
      <AlertDialogFooter>
        <AlertDialogCancel asChild>
          <Button variant="ghost">Cancel</Button>
        </AlertDialogCancel>
        <Button variant="danger" disabled={typed.trim() !== project || !plan.data || run.isPending} onClick={() => run.mutate()}>
          {run.isPending ? "Deleting…" : `Delete ${project}`}
        </Button>
      </AlertDialogFooter>
    </>
  );
}
