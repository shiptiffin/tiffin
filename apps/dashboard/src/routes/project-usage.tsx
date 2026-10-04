import { useQueries, useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import type { ManifestApp } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { BoxBar } from "@/components/box-bar";
import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { AppRow, Group, RESERVE_MB, Working } from "@/components/project-rows";
import { SegMeter } from "@/components/seg-meter";
import { cn } from "@/lib/cn";
import { bytes, dec } from "@/lib/format";
import { rememberProject } from "@/lib/recent";
import { change, pendingFor, usePending } from "@/lib/staged";
import { boxSettingsQuery, cpuWords, memWords, missing, shareMeans, shareWords, useBoxShares, usageQuery, type ProjectResources } from "@/lib/usage";

const MB = 1048576;

/**
 * Usage: how much of the box this project takes (memory, CPU, disk), and the
 * one control a person needs: let it grow as it needs, or limit it to a
 * share of the box. Per-app copies and exact numbers sit under Advanced.
 * The limit is a manifest edit (resources), applied when you let go.
 */
export function ProjectUsagePage({ project }: { project: string }) {
  useTitle(`${project} · Usage`);
  useEffect(() => rememberProject(project), [project]);
  const usage = useQuery(usageQuery(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
  const projects = useQuery(q.projects);
  const names = (projects.data ?? []).map((p) => p.name);
  const { res, shares, unmeasured } = useBoxShares();
  const pending = usePending(project);
  const hasUsage = !!usage.data;
  const noUsageApi = missing(usage.error);
  const has = (s: string) => !!(m.data?.manifest.services as Record<string, unknown> | undefined)?.[s];
  const disk = useQueries({
    queries: [
      { ...mq.pg(project), enabled: noUsageApi && has("postgres"), retry: false },
      { ...mq.storage(project), enabled: noUsageApi && has("storage"), retry: false },
    ],
  });

  // Memory, CPU and disk: from /usage when the box has it, else from the box's own readings.
  const memMB = usage.data ? Math.max(0, usage.data.memory.usedBytes - usage.data.memory.cacheBytes) / MB : shares?.projects[project];
  const cpuPct = usage.data ? usage.data.cpu.percent : res ? (res.apps ?? []).filter((a) => a.project === project).reduce((t, a) => t + a.cpuPercent, 0) : undefined;
  const diskBytes = usage.data ? usage.data.disk.totalBytes : (disk[0].data?.sizeBytes ?? 0) + (disk[1].data?.usedBytes ?? 0);
  const headroomMB = usage.data?.memory.headroomBytes !== undefined ? usage.data.memory.headroomBytes / MB : undefined;
  const limitMB = usage.data?.memory.limitBytes ? usage.data.memory.limitBytes / MB : undefined;
  const totalMB = shares?.totalMB;
  const cpus = shares?.cpuCount ?? res?.cpu.count ?? 1;

  let sentence: ReactNode = <Skeleton className="h-7 w-80" />;
  if (memMB !== undefined && totalMB) {
    const grow = headroomMB !== undefined ? memMB + headroomMB : limitMB;
    sentence = (
      <>
        {project} is using {memWords(memMB)} of memory
        {grow ? <> and can grow to about {memWords(grow)}.</> : <>, {shareWords(memMB / totalMB)} of the box.</>}
      </>
    );
  } else if (unmeasured) sentence = "Memory and CPU are measured on a running box.";

  const live = (m.data?.manifest as { resources?: ProjectResources } | undefined)?.resources;
  const staged = pendingFor(pending, "set:resources");
  const resources = staged?.kind === "set" ? (staged.to as ProjectResources | undefined) : live;

  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Usage" }]} />} title="Usage" />
      <div className="mt-3 max-w-[44rem] text-[1.0625rem] leading-7 text-ink">{sentence}</div>

      {shares && (
        <div className="mt-5 max-w-[46rem]">
          <BoxBar shares={shares} order={names} focus={project} />
          <p className="mt-2 text-xs text-ink-3">
            {project}’s share of the box, beside everything else on it.
          </p>
        </div>
      )}

      <div className="mt-9 grid max-w-[46rem] gap-3 sm:grid-cols-3">
        <Stat label="Memory" value={memMB !== undefined ? memWords(memMB) : undefined} of={limitMB ? `of ${memWords(limitMB)} allowed` : totalMB ? `of ${memWords(totalMB)} on the box` : undefined} bar={memMB !== undefined && (limitMB || totalMB) ? { v: memMB, max: limitMB ?? totalMB! } : undefined} warn={usage.data?.memory.pressure === "some"} full={usage.data?.memory.pressure === "oom"} />
        <Stat
          label="CPU"
          value={cpuPct !== undefined ? (cpuPct < 1 ? "idle" : `${dec(cpuPct, 0)}%`) : undefined}
          of={`of one CPU · the box has ${cpuWords(cpus).replace(/^one CPU$/, "one")}`}
          bar={cpuPct !== undefined ? { v: cpuPct, max: 100 } : undefined}
        />
        <Stat label="Disk" value={bytes(diskBytes)} of="databases and files" />
      </div>
      {usage.data?.memory.pressure === "oom" && (
        <p className="mt-3 max-w-[46rem] text-sm text-danger">It ran out of memory recently and something was restarted. Give it a bigger share, or let it grow.</p>
      )}

      {hasUsage && totalMB && (
        <Limit project={project} resources={resources} busy={!!staged} live={live} totalMB={totalMB} cpus={cpus} source={usage.data?.limitSource} />
      )}

      <Advanced project={project} free={res && res.memory.totalBytes > 0 ? res.memory.availableBytes / MB - RESERVE_MB : undefined} apps={(m.data?.manifest.apps ?? {}) as Record<string, ManifestApp>} status={usage.data} />
    </Page>
  );
}

function Stat({ label, value, of, bar, warn, full }: { label: string; value?: string; of?: string; bar?: { v: number; max: number }; warn?: boolean; full?: boolean }) {
  return (
    <div className="rounded-[12px] border border-rule-2 bg-paper-raised px-4 pt-3.5 pb-4 shadow-[var(--top-light)]">
      <p className="text-[0.8125rem] text-ink-3">{label}</p>
      <div className="mt-0.5 text-[1.5rem] leading-8 font-[500] tracking-[-0.02em] text-ink tnum">{value ?? <Skeleton className="mt-1 h-6 w-20" />}</div>
      {of && <p className="text-xs text-ink-3">{of}</p>}
      {bar && <SegMeter className="mt-3" label={`${label} in use`} value={bar.v} max={bar.max} warnAt={warn ? 0 : 0.8} fullAt={full ? 0 : 0.95} />}
    </div>
  );
}

const STOPS = [5, 10, 15, 20, 25, 30, 40, 50, 60, 75, 90];

/** The one control: grow as it needs, or a share of the box. */
function Limit({
  project,
  resources,
  live,
  busy,
  totalMB,
  cpus,
  source,
}: {
  project: string;
  resources?: ProjectResources;
  live?: ProjectResources;
  busy: boolean;
  totalMB: number;
  cpus: number;
  source?: string;
}) {
  const settings = useQuery(boxSettingsQuery);
  const boxDefault = settings.data?.defaultMaxSharePercent;
  const limited = !!resources && (!!resources.maxSharePercent || !!resources.memoryMB || !!resources.cpus);
  const pct = resources?.maxSharePercent ?? 25;
  const [drag, setDrag] = useState<number | null>(null);
  const shown = drag ?? pct;
  const set = (to: ProjectResources | undefined, what: string, undo: string) => change(project, { kind: "set", path: ["resources"], from: live, to, what, undo }, { immediate: true });
  const grow = () => set(undefined, `Let ${project} grow as it needs`, `${project} gets its limit back`);
  const limit = (p: number) => set({ maxSharePercent: p }, `Limit ${project} to ${p}% of the box`, live?.maxSharePercent ? `${project} goes back to ${live.maxSharePercent}%` : `${project} grows as it needs again`);

  return (
    <section className="mt-10 max-w-[46rem]" aria-label="Resources">
      <h2 className="text-[0.9375rem] font-[550] text-ink">Resources</h2>
      <div role="radiogroup" aria-label={`How much of the box ${project} may use`} className="mt-3 flex flex-col gap-2">
        <Choice checked={!limited} onSelect={grow} title="Grows as it needs" note={source === "box default" && boxDefault && boxDefault < 100 ? `It shares the box with the others, up to the box’s limit of ${boxDefault}% for every project.` : "It shares the box with the others and uses what it needs. The box keeps a safety margin."} />
        <Choice
          checked={limited}
          onSelect={() => !limited && limit(25)}
          title={limited && !resources?.maxSharePercent ? "Limited to exact numbers" : `Limit to ${shown}% of the box`}
          note={limited && !resources?.maxSharePercent ? `${resources?.memoryMB ? memWords(resources.memoryMB) : "no memory limit"}${resources?.cpus ? ` and ${cpuWords(resources.cpus)}` : ""}. Change it under Advanced.` : "So it can’t crowd out your other projects."}
        >
          {limited && !!resources?.maxSharePercent && (
            <div className="mt-3">
              <input
                type="range"
                min={0}
                max={STOPS.length - 1}
                step={1}
                value={STOPS.indexOf(STOPS.reduce((a, b) => (Math.abs(b - shown) < Math.abs(a - shown) ? b : a)))}
                onChange={(e) => setDrag(STOPS[Number(e.target.value)])}
                onPointerUp={() => drag !== null && drag !== pct && (limit(drag), setDrag(null))}
                onKeyUp={() => drag !== null && drag !== pct && (limit(drag), setDrag(null))}
                aria-label={`${project}’s share of the box`}
                aria-valuetext={`${shown}%, ${shareMeans(shown, totalMB, cpus)}`}
                className="range w-full max-w-[24rem]"
              />
              <p className="mt-1.5 text-sm text-ink-2">
                Right now that’s {shareMeans(shown, totalMB, cpus)}.{busy && <Working> Saving…</Working>}
              </p>
            </div>
          )}
        </Choice>
      </div>
    </section>
  );
}

function Choice({ checked, onSelect, title, note, children }: { checked: boolean; onSelect: () => void; title: string; note: string; children?: ReactNode }) {
  return (
    <div
      className={cn(
        "rounded-[12px] border px-4 py-3.5 transition-colors duration-[var(--dur-state)]",
        checked ? "border-brass bg-[color-mix(in_oklch,var(--brass)_5%,var(--paper-raised))]" : "border-rule-2 bg-paper-raised hover:border-rule-3",
      )}
    >
      <button type="button" role="radio" aria-checked={checked} onClick={onSelect} className="flex w-full items-start gap-3 text-left">
        <span className={cn("mt-0.5 grid size-[18px] shrink-0 place-items-center rounded-full border", checked ? "border-brass" : "border-rule-3")} aria-hidden>
          {checked && <span className="size-2.5 rounded-full bg-brass" />}
        </span>
        <span className="min-w-0">
          <span className="block text-[0.9375rem] font-[550] text-ink">{title}</span>
          <span className="block text-sm text-ink-3">{note}</span>
        </span>
      </button>
      {children && <div className="pl-[30px]">{children}</div>}
    </div>
  );
}

/** Per-app copies, exact limits and what each part uses: one click away, closed by default. */
function Advanced({ project, apps, free, status }: { project: string; apps: Record<string, ManifestApp>; free?: number; status?: { services?: Record<string, { memoryBytes?: number }>; apps: Array<{ app: string; memoryBytes: number }> } }) {
  const [open, setOpen] = useState(false);
  const pending = usePending(project);
  const p = useQuery(q.project(project));
  const list = Object.entries(apps);
  return (
    <section className="mt-10 max-w-[56rem]">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="inline-flex items-center gap-1.5 text-[0.9375rem] font-[550] text-ink hover:text-ink-2">
        Advanced
        <ChevronDown className={cn("size-4 text-ink-3 transition-transform duration-[var(--dur-state)]", open && "rotate-180")} />
      </button>
      {!open && <p className="mt-1 text-sm text-ink-3">How many copies of each app run, and what each part uses.</p>}
      {open && (
        <>
          {list.length > 0 && (
            <Group label="Copies of each app" note="More copies share the visitors; each can use up to its memory.">
              {list.map(([name, spec]) => (
                <AppRow
                  key={name}
                  project={project}
                  app={name}
                  spec={spec}
                  free={free}
                  instances={pendingFor(pending, `instances:${name}`)}
                  fault={p.data?.status?.[`app/${name}`]?.state === "failed" ? p.data.status[`app/${name}`].message : undefined}
                />
              ))}
            </Group>
          )}
          {status?.services && Object.keys(status.services).length > 0 && (
            <Group label="Built-in parts">
              {Object.entries(status.services).map(([k, v]) => (
                <div key={k} className="flex items-center justify-between py-2.5 text-[0.875rem]">
                  <span className="text-ink">{k}</span>
                  <span className="text-ink-2 tnum">{v.memoryBytes !== undefined ? bytes(v.memoryBytes) : "–"}</span>
                </div>
              ))}
            </Group>
          )}
        </>
      )}
    </section>
  );
}
