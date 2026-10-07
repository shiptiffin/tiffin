import { queryOptions } from "@tanstack/react-query";
import { request, type BoxResources } from "@/api/client";
import type { components } from "@/api/schema";
import { partName } from "@/lib/names";

/**
 * A project's footprint on the box: the memory its apps hold and everything
 * it keeps on the data disk, as a share of each. One number sorts projects
 * (the larger of the two shares), however many services they use.
 *
 *   GET /v1/box/disk        each project's disk by part, leftovers, the sweep
 *   GET /v1/box/resources   each project's memory and CPU now
 *
 * A box from before the disk report (404), or a token that can't read it,
 * still has each project's databases and files from /v1/box/resources.
 */

const MB = 1048576;

export type DiskReport = components["schemas"]["BoxDiskReport"];
export type ProjectDisk = components["schemas"]["BoxProjectDisk"];
type Total = NonNullable<BoxResources["projects"]>[number];

export const boxDiskQuery = queryOptions({
  queryKey: ["box-disk"],
  queryFn: () => request<DiskReport>("GET", "/v1/box/disk"),
  // The box measures it once a minute (it walks the data folders).
  refetchInterval: (qq) => (qq.state.error ? false : 60_000),
  staleTime: 55_000,
  retry: false,
});

/** One part of a project's disk, for the breakdown. */
export type Part = { key: string; name: string; bytes: number; note: string };

/** The parts of a project's disk, biggest first, the empty ones left out. */
export function partsOf(d: ProjectDisk): Part[] {
  const parts: Part[] = [
    { key: "images", name: "Images", bytes: d.imageBytes, note: d.images === 1 ? "Its live build" : `${d.images} builds: the live ones and rollback targets` },
    { key: "db", name: partName("postgres"), bytes: d.databaseBytes, note: "Its Postgres databases, branches included" },
    { key: "files", name: partName("storage"), bytes: d.filesBytes, note: "Buckets and its apps’ disk folders" },
    { key: "kv", name: partName("valkey"), bytes: d.kvBytes, note: "Held in memory, saved to disk" },
    { key: "logs", name: "Logs", bytes: d.logBytes, note: "Its apps’ output and build logs" },
    { key: "build", name: "Build files", bytes: d.buildBytes, note: "Source kept for rebuilds, static sites and assets" },
    { key: "snapshots", name: "Snapshots", bytes: d.backupBytes, note: "Database copies from restores and deletes, kept 7 days" },
  ];
  return parts.filter((p) => p.bytes > 0).sort((a, b) => b.bytes - a.bytes);
}

export type Row = {
  name: string;
  /** Memory its apps hold now (no file cache), MB. */
  memMB: number;
  cpuPercent?: number;
  /** Everything on the data disk: the disk report's total, else its databases and files. */
  diskBytes: number;
  /** Shares of the box, 0–1. */
  memShare: number;
  diskShare: number;
  /** The larger share: what sorts the list. */
  share: number;
  disk?: ProjectDisk;
  total?: Total;
};

/** Every project's row, from what the box reports now. */
export function rowsOf(names: string[], res: BoxResources, totalMB: number, report?: DiskReport): Row[] {
  const totals = new Map((res.projects ?? []).map((p) => [p.project, p]));
  const disks = new Map((report?.projects ?? []).map((p) => [p.project, p]));
  const diskTotal = report?.disk.totalBytes || res.disks.data.totalBytes || 0;
  return names.map((name) => {
    const t = totals.get(name);
    const d = disks.get(name);
    const memMB = t ? Math.max(0, (t.memoryBytes - (t.cacheBytes ?? 0)) / MB) : 0;
    const diskBytes = d ? d.totalBytes : (t?.diskBytes ?? 0);
    const memShare = totalMB > 0 ? memMB / totalMB : 0;
    const diskShare = diskTotal > 0 ? diskBytes / diskTotal : 0;
    return { name, memMB, cpuPercent: t?.cpuPercent, diskBytes, memShare, diskShare, share: Math.max(memShare, diskShare), disk: d, total: t };
  });
}

export type SortKey = "footprint" | "memory" | "disk" | "name";

export function sortRows(rows: Row[], key: SortKey): Row[] {
  const by: Record<SortKey, (a: Row, b: Row) => number> = {
    footprint: (a, b) => b.share - a.share || b.diskBytes - a.diskBytes,
    memory: (a, b) => b.memMB - a.memMB,
    disk: (a, b) => b.diskBytes - a.diskBytes,
    name: () => 0,
  };
  return [...rows].sort((a, b) => by[key](a, b) || a.name.localeCompare(b.name));
}

/** A share as a whole percent, or "<1%" for a sliver (never "0%" for something there). */
export function sharePct(f: number): string {
  if (f <= 0) return "0%";
  if (f < 0.01) return "<1%";
  return `${Math.round(f * 100)}%`;
}
