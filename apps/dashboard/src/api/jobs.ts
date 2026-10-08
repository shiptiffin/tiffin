// Jobs: the operations the Jobs area adds on top of mod2's queue and
// workflow calls (schedules that pause, a schedule preview, test jobs,
// starting runs, live streams). Typed from the generated schema.
import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import { pagedQuery } from "@/lib/paged";
import { mod2, type JobFilter, type QueueCron, type QueueJob, type QueueStats, type RunFilter, type WorkflowRun } from "./modules";
import type { components } from "./schema";

type S = components["schemas"];
export type LiveState = S["QueueLiveState"];
export type LiveStep = S["QueueLiveStep"];
export type CronRun = S["QueueCronRun"];
export type SendBody = S["QueueSendBody"];
export type SendResult = S["QueueSendResult"];
export type QueueConfig = S["QueueConfig"];
export type { QueueCron, QueueJob, QueueStats, WorkflowRun };

const e = encodeURIComponent;
const P = (project: string) => `/v1/projects/${e(project)}`;

export const jobsApi = {
  pauseCron: (p: string, name: string) => request<QueueCron>("POST", `${P(p)}/queue/crons/${e(name)}/pause`, {}),
  resumeCron: (p: string, name: string) => request<QueueCron>("POST", `${P(p)}/queue/crons/${e(name)}/resume`, {}),
  triggerCron: (p: string, name: string) => request<{ job: string; message: string }>("POST", `${P(p)}/queue/crons/${e(name)}/trigger`, {}),
  preview: (p: string, schedule: string, timezone: string) =>
    request<S["QueuePreview"]>(
      "GET",
      `${P(p)}/queue/schedule-preview?schedule=${e(schedule)}${timezone ? `&timezone=${e(timezone)}` : ""}`,
    ),
  send: (p: string, body: SendBody) => request<SendResult>("POST", `${P(p)}/queue/send`, body),
  startRun: (p: string, body: { workflow: string; app?: string; input?: unknown; id?: string }) => request<WorkflowRun>("POST", `${P(p)}/workflows/runs`, body),
  live: (p: string, id: string) => request<LiveState>("GET", `${P(p)}/queue/live/${e(id)}`),
  /** The server-sent events stream of one job or run (EventSource; the session cookie authorises it). */
  liveStream: (p: string, id: string) => `${P(p)}/queue/live/${e(id)}`,
  subscribe: (p: string, topic: string, name: string, body: { app?: string; path?: string; url?: string }) =>
    request<unknown>("PUT", `${P(p)}/queue/topics/${e(topic)}/subscriptions/${e(name)}`, body),
  unsubscribe: (p: string, topic: string, name: string) => request<void>("DELETE", `${P(p)}/queue/topics/${e(topic)}/subscriptions/${e(name)}`),
  signingSecret: (p: string) => request<S["QueueSigning"]>("GET", `${P(p)}/queue/signing-secret`),
};

/** Shared query keys: the overview, the tabs and the detail pages read the same entries. */
export const jq = {
  stats: (p: string) => queryOptions({ queryKey: ["queue-stats", p], queryFn: () => mod2.queueStats(p), refetchInterval: 10_000 }),
  crons: (p: string) => queryOptions({ queryKey: ["crons", p], queryFn: () => mod2.crons(p), refetchInterval: 15_000 }),
  topics: (p: string) => queryOptions({ queryKey: ["topics", p], queryFn: () => mod2.topics(p) }),
  /** The latest runs (one page), for summaries: workers, tab counts. Lists page with runPages. */
  runs: (p: string, state?: WorkflowRun["state"]) =>
    queryOptions({ queryKey: ["runs", p, state ?? ""], queryFn: async () => (await mod2.runs(p, { state: state ? [state] : undefined, limit: 100 })).items, refetchInterval: 10_000 }),
  /** Jobs a page at a time, narrowed on the box. */
  jobPages: (p: string, f: JobFilter, o: { enabled?: boolean } = {}) =>
    pagedQuery(["jobs", p, "pages", f], (cursor, signal) => mod2.jobs(p, { ...f, cursor }, signal), { refetchInterval: 10_000, ...o }),
  /** Workflow runs a page at a time, narrowed on the box. */
  runPages: (p: string, f: RunFilter, o: { enabled?: boolean } = {}) =>
    pagedQuery(["runs", p, "pages", f], (cursor, signal) => mod2.runs(p, { ...f, cursor }, signal), { refetchInterval: 10_000, ...o }),
  job: (p: string, id: string) => queryOptions({ queryKey: ["job", p, id], queryFn: () => mod2.job(p, id) }),
  run: (p: string, id: string) => queryOptions({ queryKey: ["run", p, id], queryFn: () => mod2.run(p, id) }),
  approvals: (p: string) => queryOptions({ queryKey: ["wf-approvals", p], queryFn: () => mod2.wfApprovals(p), refetchInterval: 15_000 }),
};
