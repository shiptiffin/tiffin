import type { BoxResources } from "@/api/client";

/**
 * One honest account of the box's memory, on one basis: what Linux counts as
 * in use (total − available). Every memory number on the Box page, the
 * sidebar and the plan tray comes from here, so they add up:
 *
 *   Σ projects (their apps' containers) + platform (its parts + "Linux and builds") = in use
 *   in use + room left = total
 *
 * Each service's and app's cgroup reading includes file cache the kernel
 * hands back on demand; /v1/box/resources reports that part as cacheBytes,
 * so every part here counts only what it really holds (memory − cache). The
 * remainder of "in use" is "Linux and builds" (the kernel, the container
 * runtime, BuildKit, the VM agent), and the cache shows quietly beside it.
 * (Older boxes without cacheBytes fall back to scaling the parts to fit.)
 */

const MB = 1048576;

/** The platform's parts as the Box draws them, by systemd unit. */
export const PLATFORM_PARTS: Array<{ key: string; units: string[] }> = [
  { key: "tiffin", units: ["tiffin"] },
  { key: "postgres", units: ["postgres"] },
  { key: "valkey", units: ["valkey"] },
  { key: "auth", units: ["auth"] },
  { key: "storage", units: ["storage"] },
  { key: "observe", units: ["victoria-metrics", "victoria-logs"] },
  { key: "protect", units: ["crowdsec", "firewall", "app-firewall"] },
];

export type MemoryModel = {
  totalMB: number;
  usedMB: number;
  /** Room left: total − in use, the same basis the throttles and the plan tray use. */
  freeMB: number;
  /** Each project's apps (production and previews), in MB. */
  projects: Record<string, number>;
  /** One app's containers, in MB (rounded; a project's rows sum to its total). */
  app: (project: string, app: string) => number;
  /** Each platform part, in MB, scaled to fit what Linux counts. */
  parts: Record<string, number>;
  /** The kernel, container runtime and BuildKit: the rest of "in use". */
  systemMB: number;
  /** The platform tier's total: Σ parts + system. */
  platformMB: number;
  /** File cache the services and apps hold, which the kernel hands back on demand (not counted as in use). */
  cacheMB: number;
};

export function memoryModel(r: BoxResources): MemoryModel {
  const totalMB = Math.round(r.memory.totalBytes / MB);
  const usedMB = Math.round((r.memory.totalBytes - r.memory.availableBytes) / MB);
  const freeMB = totalMB - usedMB;
  const apps = r.apps ?? [];
  const held = (x: { memoryBytes: number; cacheBytes?: number }) => Math.max(0, x.memoryBytes - (x.cacheBytes ?? 0)) / MB;
  const rawApps = apps.reduce((t, a) => t + held(a), 0);
  const appScale = rawApps > usedMB && rawApps > 0 ? usedMB / rawApps : 1;
  // Per app, rounded once; a project's total is the sum of its rows, so the column adds up.
  const perApp: Record<string, Record<string, number>> = {};
  for (const a of apps) {
    const p = (perApp[a.project] ??= {});
    p[a.app] = (p[a.app] ?? 0) + held(a) * appScale;
  }
  for (const p of Object.values(perApp)) for (const k of Object.keys(p)) p[k] = Math.round(p[k]);
  const rounded: Record<string, number> = Object.fromEntries(Object.entries(perApp).map(([p, v]) => [p, Object.values(v).reduce((t, x) => t + x, 0)]));
  const appsRounded = Object.values(rounded).reduce((t, v) => t + v, 0);
  const appsMB = rawApps * appScale;
  const platformMB = Math.max(0, usedMB - appsRounded);

  const services = r.services ?? [];
  const reading = (units: string[]) => services.filter((s) => units.includes(s.name)).reduce((t, s) => t + held(s), 0);
  const raw = Object.fromEntries(PLATFORM_PARTS.map((p) => [p.key, reading(p.units)]));
  const rawSum = Object.values(raw).reduce((t, v) => t + v, 0);
  const scale = rawSum > platformMB && rawSum > 0 ? platformMB / rawSum : 1;
  const parts = Object.fromEntries(Object.entries(raw).map(([k, v]) => [k, Math.round(v * scale)]));
  let partsSum = Object.values(parts).reduce((t, v) => t + v, 0);
  if (partsSum > platformMB) {
    // Rounding after scaling can overshoot by a few MB: take it off the largest part.
    const big = Object.keys(parts).reduce((x, y) => (parts[y] > parts[x] ? y : x));
    parts[big] -= partsSum - platformMB;
    partsSum = platformMB;
  }
  const systemMB = Math.max(0, platformMB - partsSum);
  const reported = [...services, ...apps].some((x) => x.cacheBytes !== undefined);
  const cacheMB = reported
    ? Math.round([...services, ...apps].reduce((t, x) => t + (x.cacheBytes ?? 0), 0) / MB)
    : Math.max(0, Math.round(services.reduce((t, s) => t + s.memoryBytes / MB, 0) + appsMB - usedMB));

  return {
    totalMB,
    usedMB,
    freeMB,
    projects: rounded,
    app: (project, app) => perApp[project]?.[app] ?? 0,
    parts,
    systemMB,
    platformMB,
    cacheMB,
  };
}
