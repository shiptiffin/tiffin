import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Navigate } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { useState, type ReactNode } from "react";
import { request, type ManifestApp } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { StateLine } from "@/components/health-kit";
import { InfoTip } from "@/components/info-tip";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { AppRow, Group, RESERVE_MB, Working } from "@/components/project-rows";
import { ReadOnlyBanner } from "@/components/read-only";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { cn } from "@/lib/cn";
import { bytes, count, dec } from "@/lib/format";
import { useMe } from "@/lib/me";
import { change, changeMany, pendingFor, usePending, type StagedEdit } from "@/lib/staged";
import { partName, partSub } from "@/lib/names";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { UsageCharts, type Range } from "@/components/usage-charts";
import { DiskBreakdown } from "@/components/usage-disk";
import { boxDiskQuery } from "@/lib/footprint";
import { boxSettingsQuery, cpuWords, memWords, missing, shareMeans, useBoxShares, usageQuery, type ProjectResources, type ProjectUsage } from "@/lib/usage";

const MB = 1048576;

/**
 * The old address of a project's usage: it lives on the Observability page's
 * Resources tab now. Keeps a #storage link (the read-only banner's) working.
 */
export function ProjectUsagePage({ project }: { project: string }) {
  const hash = typeof window !== "undefined" ? window.location.hash.replace(/^#/, "") : "";
  return <Navigate to="/projects/$project/observability" params={{ project }} search={{ tab: "resources" } as never} hash={hash || undefined} replace />;
}

/**
 * Resources: how much of the box this project takes (memory, CPU, disk; its
 * database, cache and builds against their limits), and the one control a
 * person needs: no limit, a share of the box, or a fixed amount, for all of
 * it. Its storage, cache and query time limits sit under the limit's
 * Advanced; per-app copies and exact numbers under Details. The limit is a
 * manifest edit (resources), applied on Save.
 */
export function ResourcesView({ project, range, app, tables }: { project: string; range: Range; app?: string; tables: boolean }) {
  const usage = useQuery(usageQuery(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000 });
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
  const storage = usage.data?.storage;

  // The Memory reading below says what it uses; the line says only how far it can grow, when the box knows.
  let sentence: ReactNode = <Skeleton className="mt-3 h-7 w-80" />;
  if (memMB !== undefined && totalMB) sentence = headroomMB !== undefined ? <StateLine>{project} can grow to about {memWords(memMB + headroomMB)} of memory.</StateLine> : null;
  else if (unmeasured) sentence = <StateLine>Memory and CPU are measured on a running box.</StateLine>;

  // The limit as the config says it; a box that reports a budget the config doesn't show yet wins.
  const budget = usage.data?.budget;
  const live =
    (m.data?.manifest as { resources?: ProjectResources } | undefined)?.resources ??
    (budget && !budget.auto ? { maxSharePercent: budget.maxSharePercent, memoryMB: budget.memoryMB, cpus: budget.cpus } : undefined);
  const staged = pendingFor(pending, "set:resources");
  const resources = staged?.kind === "set" ? (staged.to as ProjectResources | undefined) : live;

  return (
    <>
      <div className="mt-4">{sentence}</div>
      <ReadOnlyBanner project={project} className="mt-5" />
      {usage.data?.storage?.diskWarning && !usage.data.storage.readOnly && (
        <p role="status" className="mt-5 max-w-[46rem] rounded-[10px] bg-warn-wash px-4 py-3 text-[0.9375rem] text-ink">
          {usage.data.storage.diskWarning}
        </p>
      )}

      <div className="mt-9 grid max-w-[46rem] gap-3 sm:grid-cols-3">
        <Stat label="Memory" value={memMB !== undefined ? memWords(memMB) : undefined} of={limitMB ? `of ${memWords(limitMB)} allowed` : totalMB ? `of ${memWords(totalMB)} on the box` : undefined} bar={memMB !== undefined && (limitMB || totalMB) ? { v: memMB, max: limitMB ?? totalMB! } : undefined} warn={usage.data?.memory.pressure === "some"} full={usage.data?.memory.pressure === "oom"} />
        <Stat
          label="CPU"
          value={cpuPct !== undefined ? (cpuPct < 1 ? "idle" : `${dec(cpuPct, 0)}%`) : undefined}
          of={`of one CPU · the box has ${cpuWords(cpus).replace(/^one CPU$/, "one")}`}
          bar={cpuPct !== undefined ? { v: cpuPct, max: 100 } : undefined}
        />
        <Stat
          label="Disk"
          value={bytes(storage?.limitBytes ? storage.usedBytes : diskBytes)}
          of={storage?.limitBytes ? `of ${bytes(storage.limitBytes, 0)} allowed` : "databases and files"}
          bar={storage?.limitBytes ? { v: storage.usedBytes, max: storage.limitBytes } : undefined}
          full={!!storage?.readOnly}
        />
      </div>
      {(usage.data?.apps?.filter((a) => !a.preview).length ?? 0) > 1 && <AppsNow apps={usage.data!.apps!.filter((a) => !a.preview)} />}
      <UsageCharts project={project} apps={Object.keys(m.data?.manifest.apps ?? {}).sort()} app={app} usage={usage.data} only="resources" title={app ? `${app} over time` : "Over time"} range={range} tables={tables} className="mt-12" />
      {usage.data?.memory.pressure === "oom" && totalMB && (
        <OutOfMemory project={project} live={live} source={usage.data.limitSource} />
      )}
      {usage.data && <SharedMeters usage={usage.data} />}

      {hasUsage && totalMB && (
        <Limit project={project} resources={resources} busy={!!staged} live={live} totalMB={totalMB} cpus={cpus} source={usage.data?.limitSource}>
          {usage.data && <LimitAdvanced project={project} usage={usage.data} services={m.data?.manifest.services as Services | undefined} />}
        </Limit>
      )}


      <Details
        project={project}
        free={res && res.memory.totalBytes > 0 ? res.memory.availableBytes / MB - RESERVE_MB : undefined}
        apps={(m.data?.manifest.apps ?? {}) as Record<string, ManifestApp>}
        status={usage.data}
      />
    </>
  );
}

/** Each app right now: copies, memory and CPU, a bar of its share of the project's memory. */
function AppsNow({ apps }: { apps: NonNullable<ProjectUsage["apps"]> }) {
  const total = apps.reduce((t, a) => t + a.memoryBytes, 0) || 1;
  const sorted = [...apps].sort((a, b) => b.memoryBytes - a.memoryBytes);
  return (
    <section className="mt-8 max-w-[46rem]" aria-label="Each app now">
      <h2 className="label mb-1.5">Each app now</h2>
      <div className="divide-y divide-rule border-y border-rule">
        {sorted.map((a) => (
          <div key={a.app} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1.5 py-2.5 text-[0.875rem] sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)_5.5rem_4.5rem]">
            <span className="min-w-0">
              <span className="block truncate font-mono text-[0.84375rem] text-ink">{a.app}</span>
              <span className="block text-xs text-ink-3">{a.state === "running" ? (a.instances > 1 ? `${a.instances} copies running` : "Running") : a.state.charAt(0).toUpperCase() + a.state.slice(1)}</span>
            </span>
            <SegMeter size="row" className="col-span-2 row-start-2 sm:col-span-1 sm:row-start-auto" label={`${a.app}’s share of the project’s memory`} value={a.memoryBytes} max={total} warnAt={2} fullAt={2} />
            <span className="col-start-2 row-start-1 text-right text-ink tnum sm:col-start-auto sm:row-start-auto">{a.memoryBytes > 0 ? memWords(a.memoryBytes / MB) : "–"}</span>
            <span className="hidden text-right text-ink-2 tnum sm:block">{a.cpuPercent < 1 ? "idle" : `${dec(a.cpuPercent, 0)}%`}</span>
          </div>
        ))}
      </div>
    </section>
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

/** The shares OutOfMemory steps up through when it gives a project more. */
const STOPS = [5, 10, 15, 20, 25, 30, 40, 50, 60, 75, 90];
/** What the Limit offers: a few round shares, memory in doublings, whole CPUs. */
const SHARE_STOPS = [10, 25, 50, 75];
const MEMORY_STOPS = [256, 512, 1024, 2048, 4096, 8192];
const CPU_STOPS = [0.5, 1, 2, 4, 8];

type LimitKind = "none" | "share" | "fixed";
type LimitDraft = { kind: LimitKind; share: number; memoryMB: number; cpus: number };

/** The limit the project has, as the three choices say it. */
function draftOf(r: ProjectResources | undefined, totalMB: number): LimitDraft {
  const quarter = MEMORY_STOPS.filter((x) => x <= totalMB / 4).pop() ?? MEMORY_STOPS[0];
  if (r?.maxSharePercent) return { kind: "share", share: r.maxSharePercent, memoryMB: quarter, cpus: 0 };
  if (r?.memoryMB || r?.cpus) return { kind: "fixed", share: 25, memoryMB: r.memoryMB ?? 0, cpus: r.cpus ?? 0 };
  return { kind: "none", share: 25, memoryMB: quarter, cpus: 0 };
}

const same = (a: LimitDraft, b: LimitDraft) =>
  a.kind === b.kind && (a.kind === "none" || (a.kind === "share" ? a.share === b.share : a.memoryMB === b.memoryMB && a.cpus === b.cpus));

const mbLabel = (n: number) => (n >= 1024 ? `${dec(n / 1024, n % 1024 ? 1 : 0)} GB` : `${n} MB`);
const cpuLabel = (n: number) => (n === 0 ? "No CPU limit" : n === 0.5 ? "Half a CPU" : n === 1 ? "1 CPU" : `${dec(n, n % 1 ? 1 : 0)} CPUs`);

/**
 * The one control, chosen like an app's Scale: the kind of limit (none, a
 * share of the box, a fixed amount), then how much from a short list, a line
 * saying what that means, and nothing changes until Save.
 */
function Limit({
  project,
  resources,
  live,
  busy,
  totalMB,
  cpus,
  source,
  children,
}: {
  project: string;
  /** The limit as it is, or as it's about to be while a change applies. */
  resources?: ProjectResources;
  /** The limit as applied: what Undo goes back to. */
  live?: ProjectResources;
  busy: boolean;
  totalMB: number;
  cpus: number;
  source?: string;
  children?: ReactNode;
}) {
  const settings = useQuery(boxSettingsQuery);
  const { can } = useMe();
  const writer = can("apply:reversible");
  const boxDefault = settings.data?.defaultMaxSharePercent;
  // A share is of the memory the box keeps for apps (not counting what Tiffin keeps for itself).
  const base = settings.data?.appMemoryMB || totalMB;
  const applied = draftOf(resources, base);
  const appliedKey = JSON.stringify(applied);
  // A choice here is a draft until Save; when the limit changes underneath (saved, undone, elsewhere), start again from it.
  const [draft, setDraft] = useState(applied);
  const [was, setWas] = useState(appliedKey);
  if (was !== appliedKey) {
    setWas(appliedKey);
    setDraft(applied);
  }
  const dirty = !same(draft, applied);
  const edit = (d: Partial<LimitDraft>) => setDraft((x) => ({ ...x, ...d }));

  const shareOptions = [...new Set([...SHARE_STOPS, applied.share])].sort((a, b) => a - b);
  const memOptions = [...new Set([...MEMORY_STOPS.filter((x) => x <= base), ...(applied.kind === "fixed" && applied.memoryMB ? [applied.memoryMB] : [])])].sort((a, b) => a - b);
  const cpuOptions = [...new Set([0, ...CPU_STOPS.filter((x) => x <= cpus), ...(applied.kind === "fixed" ? [applied.cpus] : [])])].sort((a, b) => a - b);
  const byDefault = source === "box default" && !!boxDefault && boxDefault < 100;

  const save = () => {
    const was = live?.maxSharePercent ? `${project} goes back to ${live.maxSharePercent}% of the box` : live?.memoryMB || live?.cpus ? `${project}’s limit goes back as it was` : `${project} grows as it needs again`;
    const set = (to: ProjectResources | undefined, what: string) => change(project, { kind: "set", path: ["resources"], from: live, to, what, undo: was }, { immediate: true });
    if (draft.kind === "none") set(undefined, `Let ${project} grow as it needs`);
    else if (draft.kind === "share") set({ maxSharePercent: draft.share }, `Limit ${project} to ${draft.share}% of the box`);
    else {
      const to: ProjectResources = { ...(draft.memoryMB ? { memoryMB: draft.memoryMB } : {}), ...(draft.cpus ? { cpus: draft.cpus } : {}) };
      set(to, `Limit ${project} to ${[draft.memoryMB && memWords(draft.memoryMB), draft.cpus && cpuWords(draft.cpus)].filter(Boolean).join(" and ")}`);
    }
  };

  let means: ReactNode;
  if (draft.kind === "none")
    means = byDefault
      ? `No limit of its own, so the box’s limit for every project holds it: ${boxDefault}% of the box, ${shareMeans(boxDefault, base, cpus)} right now.`
      : "It shares the box with your other projects and takes what it needs. The box keeps a safety margin.";
  else if (draft.kind === "share") means = `Up to ${shareMeans(draft.share, base, cpus).replace(" and ", " of memory and ")} right now, for its apps, database, KV store and builds together. It grows with the box.`;
  else
    means = `Up to ${draft.memoryMB ? `${memWords(draft.memoryMB)} of memory` : "any amount of memory"}${draft.cpus ? ` and ${cpuWords(draft.cpus)}` : ", with no CPU limit"}, for its apps, database, KV store and builds together, whatever the size of the box.`;

  const field = "mb-1.5 flex items-center gap-1 text-xs text-ink-3";
  return (
    <section className="mt-10 max-w-[46rem]" aria-labelledby={`${project}-limit`}>
      <div className="flex items-baseline justify-between gap-3">
        <div className="flex items-center gap-1">
          <h2 id={`${project}-limit`} className="text-[0.9375rem] font-[550] text-ink">
            Limit
          </h2>
          <InfoTip label="About the limit">
            The most of the box {project} may use, so it can’t crowd out your other projects. Near the limit it slows down first; an app that still needs more memory is restarted.
          </InfoTip>
        </div>
        {busy && <Working>Saving…</Working>}
      </div>
      <div className={cn("mt-3 grid gap-3", draft.kind === "fixed" ? "sm:grid-cols-3" : "sm:grid-cols-2")}>
        <div>
          <p id={`${project}-limit-kind`} className={field}>
            Kind of limit
          </p>
          <Select
            aria-labelledby={`${project}-limit-kind`}
            disabled={!writer}
            value={draft.kind}
            onValueChange={(v) => edit({ kind: v as LimitKind })}
            options={[
              { value: "none", label: "No limit" },
              { value: "share", label: "A share of the box" },
              { value: "fixed", label: "A fixed amount" },
            ]}
          />
        </div>
        {draft.kind === "share" && (
          <div>
            <p id={`${project}-limit-share`} className={field}>
              Share of the box
            </p>
            <Select
              aria-labelledby={`${project}-limit-share`}
              disabled={!writer}
              value={String(draft.share)}
              onValueChange={(v) => edit({ share: Number(v) })}
              options={shareOptions.map((n) => ({ value: String(n), label: `${n}%` }))}
            />
          </div>
        )}
        {draft.kind === "fixed" && (
          <>
            <div>
              <p id={`${project}-limit-mem`} className={field}>
                Memory
              </p>
              <Select
                aria-labelledby={`${project}-limit-mem`}
                disabled={!writer}
                value={String(draft.memoryMB)}
                onValueChange={(v) => edit({ memoryMB: Number(v) })}
                options={[...(draft.memoryMB ? [] : [{ value: "0", label: "No memory limit" }]), ...memOptions.map((n) => ({ value: String(n), label: mbLabel(n) }))]}
              />
            </div>
            <div>
              <p id={`${project}-limit-cpu`} className={field}>
                CPU
              </p>
              <Select
                aria-labelledby={`${project}-limit-cpu`}
                disabled={!writer}
                value={String(draft.cpus)}
                onValueChange={(v) => edit({ cpus: Number(v) })}
                options={cpuOptions.map((n) => ({ value: String(n), label: cpuLabel(n) }))}
              />
            </div>
          </>
        )}
      </div>
      <p className="mt-3 border-t border-rule pt-3 text-sm text-ink-2">{means}</p>
      {writer && dirty && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Button variant="primary" size="sm" onClick={save} disabled={busy}>
            Save
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setDraft(applied)}>
            Cancel
          </Button>
          <span className="text-xs text-ink-3">Applies now; History can undo it.</span>
        </div>
      )}
      {children}
    </section>
  );
}

const GB = 1073741824;

/** The manifest's services, as far as the limits go. */
type Services = { postgres?: { statementTimeoutSeconds?: number }; valkey?: { maxMemoryMB?: number } };

const DEFAULT_QUERY_SECONDS = 30;
const DEFAULT_CACHE_MB = 64;
const field = "ident mt-1 block h-8 w-28 rounded-[7px] border border-rule-2 bg-paper-raised px-2 text-[0.8125rem] text-ink outline-hidden focus-visible:border-brass";

/**
 * The limit's finer settings, closed by default: storage (databases and files,
 * set by the box owner), the cache and how long one query may run. Opens by
 * itself for a link to #storage (the read-only banner's "Raise its storage limit").
 */
function LimitAdvanced({ project, usage, services }: { project: string; usage: ProjectUsage; services?: Services }) {
  const [open, setOpen] = useState(() => typeof window !== "undefined" && window.location.hash === "#storage");
  const { admin } = useMe();
  const qc = useQueryClient();
  const storage = usage.storage;
  const ownStorage = !!storage && storage.limitSource === "project" && storage.limitBytes > 0;
  const live = {
    gb: ownStorage ? String(Math.round((storage.limitBytes / GB) * 10) / 10) : "",
    mb: String(services?.valkey?.maxMemoryMB ?? DEFAULT_CACHE_MB),
    secs: String(services?.postgres?.statementTimeoutSeconds ?? DEFAULT_QUERY_SECONDS),
  };
  // What the person typed; untouched fields follow the live values (the manifest may arrive after the page).
  const [typed, setTyped] = useState<Partial<typeof live>>({});
  const gb = typed.gb ?? live.gb;
  const mb = typed.mb ?? live.mb;
  const secs = typed.secs ?? live.secs;
  const setGb = (v: string) => setTyped((t) => ({ ...t, gb: v }));
  const setMb = (v: string) => setTyped((t) => ({ ...t, mb: v }));
  const setSecs = (v: string) => setTyped((t) => ({ ...t, secs: v }));
  const quota = useMutation({
    mutationFn: (maxBytes: number) => request("PUT", `/v1/projects/${encodeURIComponent(project)}/storage/quota`, { maxBytes }),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: usageQuery(project).queryKey });
      void qc.invalidateQueries({ queryKey: ["storage", project] });
    },
  });
  const nGb = Number(gb), nMb = Number(mb), nSecs = Number(secs);
  const okGb = gb === "" || (Number.isFinite(nGb) && nGb > 0);
  const okMb = !services?.valkey || (Number.isInteger(nMb) && nMb >= 1 && nMb <= 65536);
  const okSecs = !services?.postgres || (Number.isInteger(nSecs) && nSecs >= 1 && nSecs <= 3600);
  const storageChanged = admin && !!storage && gb !== live.gb;
  const dirty = storageChanged || (!!services?.valkey && mb !== live.mb) || (!!services?.postgres && secs !== live.secs);

  const save = () => {
    const edits: StagedEdit[] = [];
    if (services?.valkey && mb !== live.mb)
      edits.push({ kind: "set", path: ["services", "valkey", "maxMemoryMB"], from: services.valkey.maxMemoryMB, to: nMb, what: `Limit ${project}’s KV store to ${nMb} MB`, undo: `${project}’s KV limit goes back to ${live.mb} MB` });
    if (services?.postgres && secs !== live.secs)
      edits.push({
        kind: "set",
        path: ["services", "postgres", "statementTimeoutSeconds"],
        from: services.postgres.statementTimeoutSeconds,
        to: nSecs === DEFAULT_QUERY_SECONDS ? undefined : nSecs,
        what: `Stop ${project}’s queries after ${nSecs} seconds`,
        undo: `${project}’s queries stop after ${live.secs} seconds again`,
      });
    if (edits.length) changeMany(project, edits);
    if (storageChanged) {
      // Undo puts back this project's own limit, or the box's default.
      const before = storage.limitSource === "project" ? storage.limitBytes || -1 : 0;
      const to = gb === "" ? -1 : Math.round(nGb * GB);
      quota.mutate(to, {
        onSuccess: () => toast({ title: to < 0 ? `${project} has no storage limit now.` : `${project} may store up to ${bytes(to, 1)} now.`, action: { label: "Undo", run: () => quota.mutateAsync(before) } }),
      });
    }
  };

  return (
    <div id="storage" className="mt-4 scroll-mt-8">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="inline-flex items-center gap-1.5 text-sm font-[550] text-ink-2 hover:text-ink">
        Advanced
        <ChevronDown className={cn("size-4 text-ink-3 transition-transform duration-[var(--dur-state)]", open && "rotate-180")} />
      </button>
      {open && (
        <form
          className="mt-2 divide-y divide-rule border-y border-rule"
          onSubmit={(e) => {
            e.preventDefault();
            if (dirty && okGb && okMb && okSecs) save();
          }}
        >
          {storage && (
            <Setting
              label="Storage (GB)"
              note={`Databases and files together; they use ${bytes(storage.usedBytes)}. At the limit it becomes read-only until it’s under it again.${!ownStorage && storage.limitBytes > 0 ? ` The box’s default for every project is ${bytes(storage.limitBytes, 0)}.` : ""}`}
            >
              {admin ? (
                <input aria-label="Storage limit in GB" value={gb} onChange={(e) => setGb(e.target.value.replace(/[^0-9.]/g, ""))} inputMode="decimal" placeholder="no limit" className={field} />
              ) : (
                <p className="mt-1 text-sm text-ink-2">{storage.limitBytes > 0 ? `${bytes(storage.limitBytes, 0)}, set by the box owner` : "No limit"}</p>
              )}
            </Setting>
          )}
          {services?.valkey && (
            <Setting label="KV (MB)" note={usage.cache?.enforced ? "Over it, keys with an expiry are cleared first, then new writes wait until it’s under it." : "Held to this while it has a limit."}>
              <input aria-label="KV limit in MB" value={mb} onChange={(e) => setMb(e.target.value.replace(/[^0-9]/g, ""))} inputMode="numeric" className={field} />
            </Setting>
          )}
          {services?.postgres && (
            <Setting label="Query time limit (seconds)" note="A query that runs longer is stopped. A query can ask for longer for itself (SET LOCAL statement_timeout).">
              <input aria-label="Query time limit in seconds" value={secs} onChange={(e) => setSecs(e.target.value.replace(/[^0-9]/g, ""))} inputMode="numeric" className={field} />
            </Setting>
          )}
          <div className="py-3">
            <Button type="submit" size="md" disabled={!dirty || !okGb || !okMb || !okSecs || quota.isPending}>
              Save
            </Button>
            {!(okGb && okMb && okSecs) && <span className="ml-3 text-sm text-warn-ink">Storage above 0 GB, KV 1–65,536 MB, query time 1–3,600 seconds.</span>}
          </div>
          {quota.isError && <ProblemNote className="mb-3" error={quota.error} />}
        </form>
      )}
    </div>
  );
}

function Setting({ label, note, children }: { label: string; note: string; children: ReactNode }) {
  return (
    <div className="grid gap-x-6 gap-y-1 py-3 sm:grid-cols-[minmax(0,1fr)_8rem]">
      <div className="min-w-0">
        <p className="text-[0.875rem] text-ink">{label}</p>
        <p className="text-xs text-ink-3">{note}</p>
      </div>
      <div className="sm:justify-self-end">{children}</div>
    </div>
  );
}

/** Its database, cache and builds against their limits: one row each, a bar where there is a limit. */
function SharedMeters({ usage }: { usage: ProjectUsage }) {
  const db = usage.database;
  const cache = usage.cache;
  const builds = usage.builds;
  const stopped = db?.queriesStoppedToday ?? 0;
  return (
    <section className="mt-8 max-w-[46rem]" aria-label="Database, KV and builds">
      <h2 className="label mb-1.5">Database, KV and builds</h2>
      <div className="divide-y divide-rule border-y border-rule">
        {db?.limitCpus && (
          <Meter
            name="Database work"
            value={db.cpuPercent < 1 ? "idle" : `${dec(db.cpuPercent, 0)}% of one CPU`}
            sub={`Its queries may use ${cpuWords(db.limitCpus)}; past that they slow down.`}
            bar={{ v: db.cpuPercent, max: db.limitCpus * 100 }}
          />
        )}
        {db && (
          <Meter
            name="Database connections"
            value={`${db.connections} of ${db.connectionLimit}`}
            sub={db.connections >= db.connectionLimit ? "All in use: new connections are refused until one closes." : "Connections its apps may keep open at once."}
            bar={{ v: db.connections, max: db.connectionLimit }}
          />
        )}
        {db && (
          <Meter
            name="Query time limit"
            value={`${db.queryTimeLimitSeconds} seconds`}
            sub={stopped > 0 ? `${count(stopped, "query", "queries")} stopped at it today.` : "No query has run that long today."}
            warn={stopped > 0}
          />
        )}
        {cache && (
          <Meter
            name="KV"
            value={cache.limitBytes > 0 ? `${bytes(cache.usedBytes)} of ${bytes(cache.limitBytes, 0)}` : bytes(cache.usedBytes)}
            sub={
              cache.writesRefused
                ? "Full: new writes wait until it’s under its limit. Reads and deletes still work."
                : cache.enforced
                  ? "Held to its limit: keys with an expiry are cleared first."
                  : "Its limit applies while the project is limited."
            }
            bar={cache.enforced && cache.limitBytes > 0 ? { v: cache.usedBytes, max: cache.limitBytes } : undefined}
            full={cache.writesRefused}
          />
        )}
        <Meter
          name="Builds"
          value={builds.limitCpus ? `up to ${cpuWords(builds.limitCpus)}` : "no limit"}
          sub={builds.limitCpus && builds.slowDownBytes ? `They slow down past ${memWords(builds.slowDownBytes / MB)} of memory instead of failing.` : "They use what the box has free, one at a time."}
        />
      </div>
    </section>
  );
}

function Meter({ name, value, sub, bar, warn, full }: { name: string; value: string; sub: string; bar?: { v: number; max: number }; warn?: boolean; full?: boolean }) {
  return (
    <div className="py-2.5">
      <div className="flex items-baseline justify-between gap-4 text-[0.875rem]">
        <span className="text-ink">{name}</span>
        <span className={cn("tnum", warn || full ? "text-warn-ink" : "text-ink-2")}>{value}</span>
      </div>
      <p className="text-xs text-ink-3">{sub}</p>
      {bar && bar.max > 0 && <SegMeter size="row" className="mt-1.5" label={`${name} in use`} value={bar.v} max={bar.max} warnAt={0.8} fullAt={full ? 0 : 0.95} />}
    </div>
  );
}

/** Per-app copies and what each part uses: one click away, closed by default. */
function Details({
  project,
  apps,
  free,
  status,
}: {
  project: string;
  apps: Record<string, ManifestApp>;
  free?: number;
  status?: ProjectUsage;
}) {
  const [open, setOpen] = useState(false);
  const pending = usePending(project);
  const p = useQuery(q.project(project));
  const list = Object.entries(apps);
  return (
    <section className="mt-10 max-w-[56rem]">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="inline-flex items-center gap-1.5 text-[0.9375rem] font-[550] text-ink hover:text-ink-2">
        Details
        <ChevronDown className={cn("size-4 text-ink-3 transition-transform duration-[var(--dur-state)]", open && "rotate-180")} />
      </button>
      {!open && <p className="mt-1 text-sm text-ink-3">How many copies of each app run, what each part uses, and what it keeps on disk.</p>}
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
          {status?.services && (status.services.postgres || status.services.valkey || status.services.storage) && (
            <Group label="Its data">
              {status.services.postgres && (
                <Line name={partName("postgres")} sub={partSub("postgres")} value={`${bytes(status.services.postgres.databaseBytes)} · ${count(status.services.postgres.connections, "connection")}`} />
              )}
              {status.services.storage && (
                <Line name={partName("storage")} sub={partSub("storage")} value={`${count(status.services.storage.objects, "file")} in ${count(status.services.storage.buckets, "bucket")} · ${bytes(status.services.storage.bytes)}`} />
              )}
              {status.services.valkey && <Line name={partName("valkey")} sub={partSub("valkey")} value={`${count(status.services.valkey.keys, "key")} · ${bytes(status.services.valkey.memoryBytes)}`} />}
            </Group>
          )}
          <OnDisk project={project} />
        </>
      )}
    </section>
  );
}

/** What the project keeps on the data disk, part by part. Its total is the Disk reading above. */
function OnDisk({ project }: { project: string }) {
  const report = useQuery(boxDiskQuery);
  const d = report.data?.projects?.find((p) => p.project === project && p.exists);
  if (!d) return null;
  return (
    <section className="mt-8" aria-labelledby="on-disk">
      <h2 id="on-disk" className="label mb-1.5">
        On disk
      </h2>
      <DiskBreakdown disk={d} className="border-t border-rule" />
      <p className="mt-2 text-xs text-ink-3">Its storage limit counts its databases and files. Builds older than its rollback targets are cleared by the box each hour.</p>
    </section>
  );
}

function Line({ name, sub, value }: { name: string; sub: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-4 py-2.5 text-[0.875rem]">
      <span className="text-ink">
        {name} <span className="text-xs text-ink-3">{sub}</span>
      </span>
      <span className="text-ink-2 tnum">{value}</span>
    </div>
  );
}

/** "shop ran out of the memory it's allowed": say so, and offer more in one click. */
function OutOfMemory({ project, live, source }: { project: string; live?: ProjectResources; source: ProjectUsage["limitSource"] }) {
  const share = live?.maxSharePercent;
  const next = share ? STOPS.find((x) => x > share) : undefined;
  const give = () => {
    if (source === "project" && share && next)
      change(project, { kind: "set", path: ["resources"], from: live, to: { ...live, maxSharePercent: next }, what: `Give ${project} ${next}% of the box`, undo: `${project} goes back to ${share}%` }, { immediate: true });
    else if (source === "project")
      change(project, { kind: "set", path: ["resources"], from: live, to: undefined, what: `Let ${project} grow as it needs`, undo: `${project} gets its limit back` }, { immediate: true });
    else change(project, { kind: "set", path: ["resources"], from: live, to: { maxSharePercent: 50 }, what: `Give ${project} up to 50% of the box`, undo: `${project} follows the box’s default again` }, { immediate: true });
  };
  return (
    <div role="status" className="mt-4 flex max-w-[46rem] flex-wrap items-center gap-x-4 gap-y-2 rounded-[10px] bg-danger-wash px-4 py-3 text-[0.9375rem] text-ink">
      <span className="min-w-0 flex-1">
        <b className="font-[550]">{project} ran out of the memory it’s allowed.</b> An app was restarted in the last hour.
      </span>
      {source !== "automatic" ? (
        <Button size="md" variant="primary" onClick={give}>
          Give it more
        </Button>
      ) : (
        <span className="text-sm text-ink-2">The whole box is full: give another project less, or move to a bigger machine.</span>
      )}
    </div>
  );
}
