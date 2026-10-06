import { useQueries, useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowDown, ArrowUp } from "lucide-react";
import { Fragment, useState, type ReactNode } from "react";
import type { BoxResources } from "@/api/client";
import { q } from "@/api/queries";
import { BoxBar } from "@/components/box-bar";
import { useTitle } from "@/components/favicon";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { ProjectIcon } from "@/components/project-icon";
import { SegMeter } from "@/components/seg-meter";
import { cn } from "@/lib/cn";
import { bytes, dec } from "@/lib/format";
import { useMe } from "@/lib/me";
import { cpuWords, fullWords, memWords, usageQuery, useBoxShares, type ProjectUsage } from "@/lib/usage";
import { ShareLimit } from "./box-settings";

const MB = 1048576;

/**
 * Usage (the box): how full it is, how it's divided between projects, the
 * room left, and the box-wide limit. A project's own Usage page has its
 * detail; Settings › Machine has the platform's parts.
 */
export function BoxUsagePage() {
  useTitle("Usage");
  const { res, shares, unmeasured } = useBoxShares();
  const projects = useQuery(q.projects);
  const names = (projects.data ?? []).map((p) => p.name);
  const { admin } = useMe();
  const byProject = new Map((res?.projects ?? []).map((p) => [p.project, p]));
  const disk = res?.disks.data;
  const guard = res?.guard;
  const held = new Set((guard?.readOnly ?? []).map((h) => h.project));

  return (
    <Page wide>
      <PageHeader title="Usage" />
      {unmeasured ? (
        <p className="mt-3 text-[0.9375rem] text-ink-2">Memory, CPU and disk are measured on a running box.</p>
      ) : !shares || !res ? (
        <Skeleton className="mt-4 h-24 max-w-[46rem]" />
      ) : (
        <>
          <p className="mt-3 max-w-[44rem] text-[1.0625rem] leading-7 text-ink">
            Your box is {fullWords(shares.full)}. {memWords(shares.freeMB)} of memory and {disk ? bytes(disk.freeBytes) : "–"} of disk are free.
          </p>
          <BoxBar className="mt-4 max-w-[46rem]" shares={shares} order={names} legend />

          <div className="mt-8 grid max-w-[46rem] gap-3 sm:grid-cols-3">
            <Stat label="Memory" value={memWords(shares.usedMB)} of={`of ${memWords(shares.totalMB)}`} bar={{ v: shares.usedMB, max: shares.totalMB }} />
            <Stat label="CPU" value={`${dec(res.cpu.usedPercent, 0)}%`} of={`of ${cpuWords(res.cpu.count)}`} bar={{ v: res.cpu.usedPercent, max: 100 }} />
            {disk && (
              <Stat
                label="Disk"
                value={bytes(disk.usedBytes)}
                of={`of ${bytes(disk.totalBytes)}`}
                bar={{ v: disk.usedBytes, max: disk.totalBytes }}
                warnAt={guard ? guard.warnPercent / 100 : undefined}
                fullAt={guard && guard.stopPercent < 100 ? guard.stopPercent / 100 : undefined}
              />
            )}
          </div>
          {guard && (guard.level !== "ok" || held.size > 0) && <DiskNote guard={guard} />}

          <ByProject names={names} totals={byProject} held={held} />

          <div className="max-w-[46rem]">
            <ShareLimit admin={admin} Wrap={Plain} />
          </div>

          <p className="mt-10 text-sm text-ink-3">
            What Tiffin itself runs (databases, files, sign-in, logs) is in{" "}
            <Link to="/settings/box" className="text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
              Settings › Machine
            </Link>
            .
          </p>
        </>
      )}
    </Page>
  );
}

/** Apps' memory and CPU: nothing running is a dash, like a part the project doesn't have. */
const some = (n: number | undefined) => (n && n > 0 ? n : undefined);

type Total = NonNullable<BoxResources["projects"]>[number];
type Col = { key: string; label: string; of: (u: ProjectUsage | undefined, t: Total | undefined) => number | undefined; show: (n: number) => string };

/** What each column reads: undefined when the project doesn't have that part (shown as a dash). */
const cols: Col[] = [
  { key: "db", label: "Database", of: (u) => (u?.database || u?.services.postgres ? u.disk.databaseBytes : undefined), show: (n) => bytes(n) },
  { key: "kv", label: "KV", of: (u) => u?.cache?.usedBytes ?? u?.services.valkey?.memoryBytes, show: (n) => bytes(n) },
  { key: "files", label: "Files", of: (u) => u?.services.storage?.bytes, show: (n) => bytes(n) },
  { key: "folders", label: "Folders", of: (u) => (u?.disk.folders?.length ? u.disk.folders.reduce((t, f) => t + f.usedBytes, 0) : undefined), show: (n) => bytes(n) },
  { key: "memory", label: "Memory", of: (u, t) => some(u ? u.memory.usedBytes - u.memory.cacheBytes : t ? t.memoryBytes - t.cacheBytes : undefined), show: (n) => bytes(n) },
  { key: "cpu", label: "CPU", of: (u, t) => some(u ? u.cpu.percent : t?.cpuPercent), show: (n) => `${dec(n / 100, n < 100 ? 2 : 1)} CPU` },
];

/**
 * Every project's sizes in one table: its database, KV, files, its apps'
 * folders, and its apps' memory and CPU, sortable by any of them. Each row
 * opens the project's own Usage page. Read-only: limits live there.
 */
function ByProject({ names, totals, held }: { names: string[]; totals: Map<string, Total>; held: Set<string> }) {
  const [sort, setSort] = useState<{ key: string; desc: boolean }>({ key: "memory", desc: true });
  const navigate = useNavigate();
  const usage = useQueries({ queries: names.map((n) => ({ ...usageQuery(n), refetchInterval: 15_000 })) });
  const rows = names.map((n, i) => {
    const u = usage[i]?.data;
    const t = totals.get(n);
    return { name: n, t, v: Object.fromEntries(cols.map((c) => [c.key, c.of(u, t)])) as Record<string, number | undefined> };
  });
  const sorted = [...rows].sort((a, b) => {
    if (sort.key === "name") return sort.desc ? b.name.localeCompare(a.name) : a.name.localeCompare(b.name);
    const x = a.v[sort.key] ?? -1;
    const y = b.v[sort.key] ?? -1;
    return (sort.desc ? y - x : x - y) || a.name.localeCompare(b.name);
  });
  const head = (key: string, label: string, right = true) => {
    const on = sort.key === key;
    // The arrow sits on the side away from the numbers, so labels line up with their column.
    const arrow = on ? sort.desc ? <ArrowDown className="size-3" aria-hidden /> : <ArrowUp className="size-3" aria-hidden /> : <span className="size-3" aria-hidden />;
    return (
      <th scope="col" aria-sort={on ? (sort.desc ? "descending" : "ascending") : undefined} className={cn("border-b border-rule-2 py-2 font-[450] whitespace-nowrap", right ? "px-3 text-right" : "sticky left-0 z-[1] bg-paper pr-3 text-left")}>
        <button
          type="button"
          onClick={() => setSort({ key, desc: on ? !sort.desc : key !== "name" })}
          className={cn("inline-flex items-center gap-1 rounded-[5px] text-[0.8125rem] transition-colors hover:text-ink", on ? "text-ink" : "text-ink-3")}
        >
          {right && arrow}
          {label}
          {!right && arrow}
        </button>
      </th>
    );
  };
  return (
    <section className="mt-10" aria-labelledby="by-project">
      <h2 id="by-project" className="text-[0.9375rem] font-[550] text-ink">
        By project
      </h2>
      <p className="mt-0.5 text-[0.8125rem] text-ink-3">What each one holds and uses. Open a project for its limits and details.</p>
      <div className="mt-3 overflow-x-auto [scrollbar-width:thin]" role="region" aria-label="Every project’s usage" tabIndex={0}>
        <table className="w-full min-w-[46rem] border-separate border-spacing-0 text-[0.875rem]">
          <thead>
            <tr>
              {head("name", "Project", false)}
              {cols.map((c) => (
                <Fragment key={c.key}>{head(c.key, c.label)}</Fragment>
              ))}
            </tr>
          </thead>
          <tbody>
            {sorted.map((r) => {
              const t = r.t;
              const limit =
                !t || t.limitSource === "automatic"
                  ? "No limit"
                  : t.budget.maxSharePercent
                    ? `Limited to ${t.budget.maxSharePercent}% of the box`
                    : t.limitSource === "box default"
                      ? `Up to ${memWords(t.limitBytes / MB)} (the box’s default)`
                      : `Limited to ${memWords(t.limitBytes / MB)}`;
              return (
                // The whole row opens the project (the name is the link for keyboards and screen readers).
                <tr key={r.name} className="group cursor-pointer" onClick={() => void navigate({ to: "/projects/$project/usage", params: { project: r.name } })}>
                  <td className="sticky left-0 z-[1] border-b border-rule bg-paper py-2.5 pr-3 group-hover:bg-paper-hover">
                    <span className="flex min-w-0 items-center gap-2.5">
                      <ProjectIcon project={r.name} size={18} />
                      <span className="min-w-0">
                        <Link to="/projects/$project/usage" params={{ project: r.name }} className="block truncate font-[550] text-ink outline-none focus-visible:underline">
                          {r.name}
                        </Link>
                        <span className="block truncate text-xs text-ink-3">
                          {limit}
                          {t?.pressure === "oom" && <span className="text-danger"> · ran out of memory</span>}
                          {held.has(r.name) && <span className="text-danger"> · read-only</span>}
                        </span>
                      </span>
                    </span>
                  </td>
                  {cols.map((c) => {
                    const v = r.v[c.key];
                    return (
                      <td key={c.key} className="border-b border-rule px-3 py-2.5 text-right whitespace-nowrap text-ink-2 tnum group-hover:bg-paper-hover">
                        {v === undefined ? <span className="text-ink-4">–</span> : c.show(v)}
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Plain({ title, note, children }: { id?: string; title: string; note?: string; children: ReactNode }) {
  return (
    <section className="mt-10" aria-label={title}>
      <h2 className="text-[0.9375rem] font-[550] text-ink">{title}</h2>
      {note && <p className="mt-0.5 mb-3 text-[0.8125rem] text-ink-3">{note}</p>}
      {children}
    </section>
  );
}

function Stat({ label, value, of, bar, warnAt = 0.8, fullAt = 0.95 }: { label: string; value: string; of: string; bar: { v: number; max: number }; warnAt?: number; fullAt?: number }) {
  return (
    <div className="rounded-[12px] border border-rule-2 bg-paper-raised px-4 pt-3.5 pb-4 shadow-[var(--top-light)]">
      <p className="text-[0.8125rem] text-ink-3">{label}</p>
      <p className="mt-0.5 text-[1.5rem] leading-8 font-[500] tracking-[-0.02em] text-ink tnum">{value}</p>
      <p className="text-xs text-ink-3">{of}</p>
      <SegMeter className="mt-3" label={`${label} in use`} value={bar.v} max={bar.max} warnAt={warnAt} fullAt={fullAt} />
    </div>
  );
}

/** The disk guard speaking up: the disk is filling (and who's filling it), or a project is read-only. */
function DiskNote({ guard }: { guard: NonNullable<BoxResources["guard"]> }) {
  const holds = guard.readOnly ?? [];
  return (
    <div role="status" className={cn("mt-4 max-w-[46rem] rounded-[10px] px-4 py-3 text-[0.9375rem] text-ink", holds.length ? "bg-danger-wash" : "bg-warn-wash")}>
      {guard.message && <p>{guard.message}</p>}
      {holds.map((h) => (
        <p key={h.project} className="mt-1.5 text-sm text-ink-2">
          <Link to="/projects/$project/usage" params={{ project: h.project }} className="font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
            {h.project}
          </Link>{" "}
          is read-only. {h.message}
        </p>
      ))}
    </div>
  );
}
