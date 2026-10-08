import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { BoxResources } from "@/api/client";
import { q } from "@/api/queries";
import { BoxBar } from "@/components/box-bar";
import { useTitle } from "@/components/favicon";
import { StateLine } from "@/components/health-kit";
import { Page, PageHeader, Skeleton } from "@/components/page";
import { DiskSweep } from "@/components/usage-disk";
import { UsageProjects } from "@/components/usage-projects";
import { UsageVitals } from "@/components/usage-vitals";
import { cn } from "@/lib/cn";
import { bytes } from "@/lib/format";
import { boxDiskQuery } from "@/lib/footprint";
import { useMe } from "@/lib/me";
import { fullWords, memWords, useBoxShares } from "@/lib/usage";
import { ShareLimit } from "./box-settings";

/**
 * Usage (the box): one sentence and one bar for how full it is; memory, CPU
 * and disk as numbers with the last hour's trend; each project's footprint
 * (memory, disk, its share of the box), opening to what it keeps on the
 * disk; what the box clears by itself; and the box-wide limit. A project's
 * own resources page has its charts and limits; Settings › Machine has the
 * platform's parts.
 */
export function BoxUsagePage() {
  useTitle("Usage");
  const { res, shares, unmeasured } = useBoxShares();
  const projects = useQuery(q.projects);
  const report = useQuery({ ...boxDiskQuery, enabled: !!res });
  const names = (projects.data ?? []).map((p) => p.name);
  const { admin } = useMe();
  const disk = res?.disks.data;
  const guard = res?.guard;
  const held = new Set((guard?.readOnly ?? []).map((h) => h.project));

  return (
    <Page>
      <PageHeader title="Usage" />
      {unmeasured ? (
        <StateLine>Memory, CPU and disk are measured on a running box.</StateLine>
      ) : !shares || !res ? (
        <>
          <Skeleton className="mt-4 h-7 w-[min(36rem,100%)]" />
          <Skeleton className="mt-5 h-3" />
          <Skeleton className="mt-8 h-20" />
        </>
      ) : (
        <>
          <StateLine>
            Your box is {fullWords(shares.full)}. {memWords(shares.freeMB)} of memory and {disk ? bytes(disk.freeBytes) : "–"} of disk are free.
          </StateLine>
          <BoxBar className="mt-5" shares={shares} order={names} legend />

          <UsageVitals className="mt-8" res={res} shares={shares} />
          {guard && (guard.level !== "ok" || held.size > 0) && <DiskNote guard={guard} />}

          <UsageProjects className="mt-11" names={names} res={res} shares={shares} report={report.data} reportPending={report.isPending} held={held} />

          {report.data && <DiskSweep className="mt-11" report={report.data} />}

          <ShareLimit admin={admin} Wrap={Plain} />

          <p className="mt-11 text-[0.8125rem] text-ink-3">
            System is Linux plus the shared services every project uses (databases, files, sign-in, logs). Each one is in{" "}
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
    <section className="mt-11" aria-label={title}>
      <h2 className="text-[0.9375rem] font-[550] text-ink">{title}</h2>
      {note && <p className="mt-0.5 mb-3 text-[0.8125rem] text-ink-3">{note}</p>}
      {children}
    </section>
  );
}

/** The disk guard speaking up: the disk is filling (and who's filling it), or a project is read-only. */
function DiskNote({ guard }: { guard: NonNullable<BoxResources["guard"]> }) {
  const holds = guard.readOnly ?? [];
  return (
    <div role="status" className={cn("mt-4 rounded-[10px] px-4 py-3 text-[0.9375rem] text-ink", holds.length ? "bg-danger-wash" : "bg-warn-wash")}>
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
