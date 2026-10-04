import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import type { ReactNode } from "react";
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
import { cpuWords, fullWords, memWords, useBoxShares } from "@/lib/usage";
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
  const sorted = [...names].sort((a, b) => (shares?.projects[b] ?? 0) - (shares?.projects[a] ?? 0));
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

          <section className="mt-10 max-w-[46rem]" aria-label="By project">
            <h2 className="text-[0.9375rem] font-[550] text-ink">By project</h2>
            <ul className="mt-3 divide-y divide-rule border-y border-rule">
              {sorted.map((p) => {
                const t = byProject.get(p);
                const used = shares.projects[p] ?? 0;
                const limit =
                  !t || t.limitSource === "automatic"
                    ? "Grows as it needs"
                    : t.budget.maxSharePercent
                      ? `Limited to ${t.budget.maxSharePercent}% of the box`
                      : t.limitSource === "box default"
                        ? `Up to ${memWords(t.limitBytes / MB)} (the box’s default)`
                        : `Limited to ${memWords(t.limitBytes / MB)}`;
                return (
                  <li key={p} className="relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 py-3 hover:bg-paper-sunk sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_7rem_1rem] sm:px-2">
                    <span className="flex min-w-0 items-center gap-2.5">
                      <ProjectIcon project={p} size={18} />
                      <Link to="/projects/$project/usage" params={{ project: p }} className="truncate text-[0.9375rem] font-[550] text-ink outline-none after:absolute after:inset-0">
                        {p}
                      </Link>
                    </span>
                    <span className="col-span-2 row-start-2 text-[0.8125rem] text-ink-3 sm:col-span-1 sm:row-start-auto">
                      {limit}
                      {t?.pressure === "oom" && <span className="text-danger"> · ran out of memory</span>}
                      {held.has(p) && <span className="text-danger"> · read-only</span>}
                    </span>
                    <span className="text-right text-[0.8125rem] text-ink-2 tnum">{used > 0 ? memWords(used) : "nothing running"}</span>
                    <ChevronRight className="size-4 text-ink-4 max-sm:hidden" aria-hidden />
                  </li>
                );
              })}
            </ul>
          </section>

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
