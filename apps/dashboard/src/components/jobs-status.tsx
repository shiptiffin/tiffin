// A run's status as a small mark and a word: one shape language for jobs,
// scheduled runs and workflow runs. Fine is quiet (a grey dot), waiting is
// hollow, running is the pilot light, retrying is amber and failed is red.
// Nothing else gets a colour.
import type { QueueJob, WorkflowRun } from "@/api/modules";
import { PilotLight } from "@/components/pilot";
import { cn } from "@/lib/cn";

export type Status = "running" | "waiting" | "scheduled" | "retrying" | "failed" | "done" | "cancelled";

/** The groups the Runs filter offers, in the order a person scans them. */
export type StatusGroup = "running" | "waiting" | "failed" | "done" | "cancelled";
export const groupOf: Record<Status, StatusGroup> = {
  running: "running",
  waiting: "waiting",
  scheduled: "waiting",
  retrying: "waiting",
  failed: "failed",
  done: "done",
  cancelled: "cancelled",
};
export const statusGroups: Array<{ state?: StatusGroup; label: string }> = [
  { label: "All" },
  { state: "running", label: "Running" },
  { state: "waiting", label: "Waiting" },
  { state: "failed", label: "Failed" },
  { state: "done", label: "Done" },
  { state: "cancelled", label: "Cancelled" },
];

export function jobStatus(j: Pick<QueueJob, "state">): Status {
  return ({ completed: "done", queued: "waiting", running: "running", retrying: "retrying", scheduled: "scheduled", dead: "failed", cancelled: "cancelled" } as const)[j.state];
}

export function runStatus(r: Pick<WorkflowRun, "state">): Status {
  return ({ completed: "done", waiting: "waiting", running: "running", failed: "failed", cancelled: "cancelled" } as const)[r.state];
}

export const statusWord: Record<Status, string> = {
  running: "Running",
  waiting: "Waiting",
  scheduled: "Scheduled",
  retrying: "Retrying",
  failed: "Failed",
  done: "Done",
  cancelled: "Cancelled",
};

export const statusText: Record<Status, string> = {
  running: "text-ink",
  waiting: "text-ink-2",
  scheduled: "text-ink-3",
  retrying: "text-warn-ink",
  failed: "text-danger",
  done: "text-ink-3",
  cancelled: "text-ink-3",
};

/** The mark alone, 8 px, centred in a 12 px box so rows line up. */
export function StatusMark({ status, className }: { status: Status; className?: string }) {
  return (
    <span aria-hidden className={cn("grid size-3 shrink-0 place-items-center", className)}>
      {status === "running" ? (
        <PilotLight state="busy" />
      ) : (
        <span
          className={cn(
            "block size-2 rounded-full",
            status === "failed" && "bg-danger",
            status === "retrying" && "bg-warn",
            status === "done" && "bg-ink-4",
            status === "cancelled" && "border border-ink-4",
            (status === "waiting" || status === "scheduled") && "border-[1.5px] border-ink-3",
          )}
        />
      )}
    </span>
  );
}

/** Mark and word together, e.g. in a table cell. `word` overrides the default ("Sleeping", "Retrying, 2 of 5"). */
export function StatusLabel({ status, word, className }: { status: Status; word?: string; className?: string }) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-2 text-[0.8125rem]", statusText[status], className)}>
      <StatusMark status={status} />
      <span className="truncate">{word ?? statusWord[status]}</span>
    </span>
  );
}
