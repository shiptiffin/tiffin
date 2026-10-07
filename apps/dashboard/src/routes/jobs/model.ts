// The Jobs area's shared reading of the box's data: which app processes
// which queues, schedules and workflows; a run's duration and tries; the
// queue stats' hour in 5-minute windows (newer boxes send it).
import { ApiError } from "@/api/client";
import type { QueueCron, QueueJob, QueueStats, WorkflowRun } from "@/api/modules";
import { duration, ms } from "@/lib/format";

/** A run's length: "640 ms", "2.6 s", "14 min", "5 h 2 min". */
export const took = (n: number) => (n < 3_600_000 ? ms(n) : duration(n / 1000));

/** Where a job was delivered, short: the app ("worker") or the host ("hooks.example.com"). */
export function deliveredTo(target: string): string {
  if (/^https?:\/\//.test(target)) {
    try {
      return new URL(target).host;
    } catch {
      return target;
    }
  }
  return target.split(" ")[0] || target;
}

/** QueueStats from a box that sends the hour's series (doneByFiveMinutes, failedByFiveMinutes). */
export type QueueStatsX = QueueStats & { doneByFiveMinutes?: number[] | null; failedByFiveMinutes?: number[] | null };

/** The queues a person made (not topics, not the box's own _cron and _workflow). */
export const ownQueues = (all: QueueStats[]) => all.filter((x) => !x.name.startsWith("_") && !x.topic);

export type Handler = {
  app: string;
  role: string;
  /** In tiffin.config.ts (false: only named by a queue or a run). */
  declared: boolean;
  queues: QueueStats[];
  crons: QueueCron[];
  workflows: WorkflowLoad[];
};

/** A workflow's recent runs on one app: how many, how many going, how many failed. */
export type WorkflowLoad = { name: string; runs: number; active: number; failed: number; lastAt: string };

/**
 * Every app that processes jobs: worker apps, and web apps that a queue, a
 * schedule or a workflow run delivers to. Workers first, then by name.
 */
export function handlersOf(apps: Record<string, { role?: string }> | undefined, stats: QueueStats[], crons: QueueCron[], runs: WorkflowRun[]): Handler[] {
  const by = new Map<string, Handler>();
  const get = (app: string) => {
    let h = by.get(app);
    if (!h) {
      h = { app, role: apps?.[app]?.role ?? "web", declared: !!apps?.[app], queues: [], crons: [], workflows: [] };
      by.set(app, h);
    }
    return h;
  };
  for (const [name, a] of Object.entries(apps ?? {})) if (a.role === "worker") get(name);
  for (const q of ownQueues(stats)) if (q.app) get(q.app).queues.push(q);
  for (const c of crons) if (c.app) get(c.app).crons.push(c);
  for (const r of runs) {
    const h = get(r.app);
    let w = h.workflows.find((x) => x.name === r.workflow);
    if (!w) {
      w = { name: r.workflow, runs: 0, active: 0, failed: 0, lastAt: r.createdAt };
      h.workflows.push(w);
    }
    w.runs++;
    if (r.state === "running" || r.state === "waiting") w.active++;
    if (r.state === "failed") w.failed++;
    if (r.createdAt > w.lastAt) w.lastAt = r.createdAt;
  }
  for (const h of by.values()) h.workflows.sort((a, b) => a.name.localeCompare(b.name));
  return [...by.values()].sort((a, b) => (a.role === "worker" ? 0 : 1) - (b.role === "worker" ? 0 : 1) || a.app.localeCompare(b.app));
}

/** Queues and schedules that call an address outside the box, by host. */
export function outsideOf(stats: QueueStats[], crons: QueueCron[]): Array<{ host: string; queues: QueueStats[]; crons: QueueCron[] }> {
  const by = new Map<string, { host: string; queues: QueueStats[]; crons: QueueCron[] }>();
  const host = (u: string) => {
    try {
      return new URL(u).host;
    } catch {
      return u;
    }
  };
  const get = (u: string) => {
    const h = host(u);
    if (!by.has(h)) by.set(h, { host: h, queues: [], crons: [] });
    return by.get(h)!;
  };
  for (const q of ownQueues(stats)) if (q.url) get(q.url).queues.push(q);
  for (const c of crons) if (c.url) get(c.url).crons.push(c);
  return [...by.values()].sort((a, b) => a.host.localeCompare(b.host));
}

/** How long a job ran (or has run so far); undefined before it starts. */
export function jobDuration(j: Pick<QueueJob, "startedAt" | "finishedAt" | "state" | "attempts">, now = Date.now()): number | undefined {
  if (j.attempts && j.attempts.length > 0 && j.state !== "running") return j.attempts.reduce((n, a) => n + a.durationMs, 0);
  if (!j.startedAt) return undefined;
  const end = j.finishedAt ? new Date(j.finishedAt).getTime() : j.state === "running" ? now : undefined;
  return end === undefined ? undefined : Math.max(0, end - new Date(j.startedAt).getTime());
}

/** A workflow run's wall time from start to finish (or now). */
export function runDuration(r: Pick<WorkflowRun, "createdAt" | "finishedAt" | "updatedAt" | "state">, now = Date.now()): number {
  const end = r.finishedAt ? new Date(r.finishedAt).getTime() : r.state === "running" || r.state === "waiting" ? now : new Date(r.updatedAt).getTime();
  return Math.max(0, end - new Date(r.createdAt).getTime());
}

/** The hour's done and failed counts in 5-minute windows, when the box sends them. */
export function series(x: QueueStats): { done: number[]; failed: number[] } | null {
  const s = x as QueueStatsX;
  if (!s.doneByFiveMinutes || s.doneByFiveMinutes.length < 2) return null;
  return { done: s.doneByFiveMinutes, failed: s.failedByFiveMinutes ?? s.doneByFiveMinutes.map(() => 0) };
}

/** Retry a read twice, but not when the box says the job runner is down (503) or there's nothing there (404): say so at once. */
export const retryUnlessDown = (n: number, e: unknown) => !(e instanceof ApiError && (e.status === 503 || e.status === 404)) && n < 2;
