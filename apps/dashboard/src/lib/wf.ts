import { useQueries, useQuery } from "@tanstack/react-query";
import { mq, type WorkflowApproval } from "@/api/modules";
import { q } from "@/api/queries";

/** Workflow steps waiting for a person, across every project this session can see. */
export function useWaitingWorkflowApprovals(enabled = true): Array<WorkflowApproval & { project: string }> {
  const projects = useQuery({ ...q.projects, enabled });
  const res = useQueries({
    queries: (projects.data ?? []).map((p) => ({ ...mq.wfApprovals(p.name), enabled, retry: false })),
  });
  return res.flatMap((r, i) => (r.data ?? []).filter((a) => a.state === "waiting").map((a) => ({ ...a, project: projects.data![i].name })));
}
