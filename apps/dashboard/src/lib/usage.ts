import { queryOptions, useQuery } from "@tanstack/react-query";
import { ApiError, request, type BoxResources } from "@/api/client";
import type { components } from "@/api/schema";
import { q } from "@/api/queries";
import { memoryModel } from "./memory";

/**
 * How much of the box each project uses, in words a person would say.
 *
 * Two sources, newest first:
 *   GET /v1/projects/{p}/usage         one project's memory, CPU, disk and limit
 *   GET /v1/box/resources  projects[]  every project's share, for the home page
 * A box from before per-project usage answers 404 / leaves projects[] out:
 * then a project's share is its apps' containers (memoryModel), and the
 * limit control stays hidden because the box couldn't enforce it.
 */

const MB = 1048576;

type S = components["schemas"];
/** A project's limit as the manifest says it (absent = grows as it needs). */
export type ProjectResources = S["ManifestResources"];
/** One project's share of the box, live: GET /v1/projects/{p}/usage. */
export type ProjectUsage = S["BoxUsage"];
export type BoxResourcesWithProjects = BoxResources;
/**
 * Box-wide settings: defaultMaxSharePercent is "no project may use more than N%
 * unless it says otherwise" (100 = elastic); the rest explain the box's memory.
 */
export type BoxSettings = S["BudgetBoxSettings"];

export const usageQuery = (project: string) =>
  queryOptions({
    queryKey: ["usage", project],
    queryFn: () => request<ProjectUsage>("GET", `/v1/projects/${encodeURIComponent(project)}/usage`),
    retry: false,
    refetchInterval: (qq) => (qq.state.error ? false : 5_000),
    staleTime: 4_000,
  });

export const boxSettingsQuery = queryOptions({
  queryKey: ["box-settings"],
  queryFn: () => request<BoxSettings>("GET", "/v1/box/settings"),
  retry: false,
  staleTime: 60_000,
});
export const setBoxSettings = (b: S["BudgetSettingsBody"]) => request<BoxSettings>("PUT", "/v1/box/settings", b);

/** True when the box doesn't have this endpoint yet (older box, or a dev server). */
export const missing = (e: unknown) => e instanceof ApiError && (e.status === 404 || e.status === 405 || e.status === 501);

export type Shares = {
  totalMB: number;
  usedMB: number;
  freeMB: number;
  /** Each project's memory in MB (apps, and its databases where the box reports them). */
  projects: Record<string, number>;
  /** Each project's limit as a share of the box (0–1), when it has one. */
  caps: Record<string, number>;
  /** Tiffin itself: the platform, Linux and builds. */
  tiffinMB: number;
  cpuCount: number;
  /** Used / total, 0–1. */
  full: number;
};

/** The box split by project, on the same basis Linux counts "in use". */
export function shares(r: BoxResourcesWithProjects): Shares {
  const m = memoryModel(r);
  const projects: Record<string, number> = {};
  const caps: Record<string, number> = {};
  if (r.projects?.length) {
    for (const p of r.projects) {
      projects[p.project] = Math.max(0, (p.memoryBytes - (p.cacheBytes ?? 0)) / MB);
      // A project with a limit of its own (or the box's default share) shows it as room it may grow into.
      if (p.limitSource !== "automatic" && p.limitBytes > 0) caps[p.project] = Math.min(1, p.limitBytes / MB / m.totalMB);
    }
  } else Object.assign(projects, m.projects);
  const inProjects = Object.values(projects).reduce((t, v) => t + v, 0);
  return {
    totalMB: m.totalMB,
    usedMB: m.usedMB,
    freeMB: m.freeMB,
    projects,
    caps,
    tiffinMB: Math.max(0, m.usedMB - inProjects),
    cpuCount: r.cpu.count,
    full: m.totalMB > 0 ? m.usedMB / m.totalMB : 0,
  };
}

/** The box's resources when they can be measured (a laptop dev server can't). */
export function useBoxShares() {
  const r = useQuery(q.resources);
  const data = r.data && r.data.memory.totalBytes > 0 ? (r.data as BoxResourcesWithProjects) : undefined;
  return { res: data, shares: data ? shares(data) : undefined, unmeasured: !!r.error || (!!r.data && !data), pending: r.isPending };
}

const FRACTIONS: Array<[number, string]> = [
  [0, "nearly empty"],
  [0.1, "about a tenth full"],
  [0.2, "about a fifth full"],
  [0.25, "about a quarter full"],
  [1 / 3, "about a third full"],
  [0.4, "about two-fifths full"],
  [0.5, "about half full"],
  [0.6, "about three-fifths full"],
  [2 / 3, "about two-thirds full"],
  [0.75, "about three-quarters full"],
  [0.85, "mostly full"],
  [0.95, "nearly full"],
];

/** 0.38 → "about two-fifths full". */
export function fullWords(f: number): string {
  let best = FRACTIONS[0];
  for (const x of FRACTIONS) if (Math.abs(x[0] - f) < Math.abs(best[0] - f)) best = x;
  return best[1];
}

/** A share of the box as a person says it: "a sliver", "about 1%", "about a quarter". */
export function shareWords(f: number): string {
  if (f <= 0) return "nothing yet";
  if (f < 0.005) return "a sliver";
  if (f < 0.095) return `about ${Math.max(1, Math.round(f * 100))}%`;
  return `about ${Math.round(f * 20) * 5}%`;
}

/** 0.5 → "half a CPU", 1 → "one CPU", 2.5 → "2.5 CPUs". */
export function cpuWords(n: number): string {
  if (n <= 0) return "no CPU";
  if (n < 0.2) return "a sliver of a CPU";
  if (Math.abs(n - 0.25) < 0.07) return "a quarter of a CPU";
  if (Math.abs(n - 0.5) < 0.1) return "half a CPU";
  if (Math.abs(n - 0.75) < 0.07) return "three-quarters of a CPU";
  if (Math.abs(n - 1) < 0.1) return "one CPU";
  if (Math.abs(n - 1.5) < 0.1) return "one and a half CPUs";
  if (Math.abs(n - Math.round(n)) < 0.1) return `${["", "one", "two", "three", "four", "five", "six", "seven", "eight"][Math.round(n)] ?? Math.round(n)} CPUs`;
  return `${(Math.round(n * 10) / 10).toString()} CPUs`;
}

/** Memory in the unit a person would pick: "420 MB", "1.9 GB". */
export function memWords(mbValue: number): string {
  if (mbValue >= 1000) return `${(Math.round((mbValue / 1024) * 10) / 10).toString()}\u202fGB`;
  if (mbValue >= 10) return `${Math.round(mbValue / 10) * 10}\u202fMB`;
  return `${Math.max(0, Math.round(mbValue))}\u202fMB`;
}

/** "about 950 MB and half a CPU": what a share of this box is right now. */
export function shareMeans(percent: number, totalMB: number, cpus: number): string {
  return `about ${memWords((percent / 100) * totalMB)} and ${cpuWords((percent / 100) * cpus)}`;
}
