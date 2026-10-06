import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { request, type ManifestApp } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { BoxBar } from "@/components/box-bar";
import { useTitle } from "@/components/favicon";
import { Crumbs, Page, PageHeader, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { AppRow, Group, RESERVE_MB, Working } from "@/components/project-rows";
import { ReadOnlyBanner } from "@/components/read-only";
import { SegMeter } from "@/components/seg-meter";
import { toast } from "@/components/toast";
import { cn } from "@/lib/cn";
import { bytes, count, dec } from "@/lib/format";
import { useMe } from "@/lib/me";
import { rememberProject } from "@/lib/recent";
import { change, changeMany, pendingFor, usePending, type StagedEdit } from "@/lib/staged";
import { partName, partSub } from "@/lib/names";
import { Button } from "@/components/ui/button";
import { UsageCharts } from "@/components/usage-charts";
import { boxSettingsQuery, cpuWords, memWords, missing, shareMeans, shareWords, useBoxShares, usageQuery, type ProjectResources, type ProjectUsage } from "@/lib/usage";

const MB = 1048576;

/**
 * Usage: how much of the box this project takes (memory, CPU, disk; its
 * database, cache and builds against their limits), and the one control a
 * person needs: no limit, or a share of the box that holds all of it. Its
 * storage, cache and query time limits sit under the limit's Advanced;
 * per-app copies and exact numbers under Details. The limit is a manifest
 * edit (resources), applied when you let go.
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
  const storage = usage.data?.storage;

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

  // The limit as the config says it; a box that reports a budget the config doesn't show yet wins.
  const budget = usage.data?.budget;
  const live =
    (m.data?.manifest as { resources?: ProjectResources } | undefined)?.resources ??
    (budget && !budget.auto ? { maxSharePercent: budget.maxSharePercent, memoryMB: budget.memoryMB, cpus: budget.cpus } : undefined);
  const staged = pendingFor(pending, "set:resources");
  const resources = staged?.kind === "set" ? (staged.to as ProjectResources | undefined) : live;

  return (
    <Page wide>
      <PageHeader eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Usage" }]} />} title="Usage" />
      <div className="mt-3 max-w-[44rem] text-[1.0625rem] leading-7 text-ink">{sentence}</div>
      <ReadOnlyBanner project={project} className="mt-5" />
      {usage.data?.storage?.diskWarning && !usage.data.storage.readOnly && (
        <p role="status" className="mt-5 max-w-[46rem] rounded-[10px] bg-warn-wash px-4 py-3 text-[0.9375rem] text-ink">
          {usage.data.storage.diskWarning}
        </p>
      )}

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
        <Stat
          label="Disk"
          value={bytes(storage?.limitBytes ? storage.usedBytes : diskBytes)}
          of={storage?.limitBytes ? `of ${bytes(storage.limitBytes, 0)} allowed` : "databases and files"}
          bar={storage?.limitBytes ? { v: storage.usedBytes, max: storage.limitBytes } : undefined}
          full={!!storage?.readOnly}
        />
      </div>
      {usage.data?.memory.pressure === "oom" && totalMB && (
        <OutOfMemory project={project} live={live} source={usage.data.limitSource} />
      )}
      {usage.data && <SharedMeters usage={usage.data} />}

      {hasUsage && totalMB && (
        <Limit project={project} resources={resources} busy={!!staged} live={live} totalMB={totalMB} cpus={cpus} source={usage.data?.limitSource}>
          {usage.data && <LimitAdvanced project={project} usage={usage.data} services={m.data?.manifest.services as Services | undefined} />}
        </Limit>
      )}

      <UsageCharts project={project} apps={Object.keys(m.data?.manifest.apps ?? {}).sort()} usage={usage.data} />

      <Details
        project={project}
        free={res && res.memory.totalBytes > 0 ? res.memory.availableBytes / MB - RESERVE_MB : undefined}
        apps={(m.data?.manifest.apps ?? {}) as Record<string, ManifestApp>}
        status={usage.data}
        exact={hasUsage ? { live, cpus } : undefined}
      />
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

/** The one control: no limit, or a share of the box (for its apps, database, cache and builds alike). */
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
  resources?: ProjectResources;
  live?: ProjectResources;
  busy: boolean;
  totalMB: number;
  cpus: number;
  source?: string;
  children?: ReactNode;
}) {
  const settings = useQuery(boxSettingsQuery);
  const boxDefault = settings.data?.defaultMaxSharePercent;
  // A share is of the memory the box keeps for apps (not counting what Tiffin keeps for itself).
  const base = settings.data?.appMemoryMB || totalMB;
  const limited = !!resources && (!!resources.maxSharePercent || !!resources.memoryMB || !!resources.cpus);
  const pct = resources?.maxSharePercent ?? 25;
  const [drag, setDrag] = useState<number | null>(null);
  const shown = drag ?? pct;
  const set = (to: ProjectResources | undefined, what: string, undo: string) => change(project, { kind: "set", path: ["resources"], from: live, to, what, undo }, { immediate: true });
  const grow = () => set(undefined, `Let ${project} grow as it needs`, `${project} gets its limit back`);
  const limit = (p: number) => set({ maxSharePercent: p }, `Limit ${project} to ${p}% of the box`, live?.maxSharePercent ? `${project} goes back to ${live.maxSharePercent}%` : `${project} grows as it needs again`);

  return (
    <section className="mt-10 max-w-[46rem]" aria-label="Limit">
      <h2 className="text-[0.9375rem] font-[550] text-ink">Limit</h2>
      <div role="radiogroup" aria-label={`How much of the box ${project} may use`} className="mt-3 flex flex-col gap-2">
        <Choice checked={!limited} onSelect={grow} title="No limit" note={source === "box default" && boxDefault && boxDefault < 100 ? `It shares the box with the others, up to the box’s limit of ${boxDefault}% for every project.` : "It shares the box with the others and uses what it needs. The box keeps a safety margin."} />
        <Choice
          checked={limited}
          onSelect={() => !limited && limit(25)}
          title={limited && !resources?.maxSharePercent ? "Limited to exact numbers" : `Limit to ${shown}% of the box`}
          note={limited && !resources?.maxSharePercent ? `${resources?.memoryMB ? memWords(resources.memoryMB) : "no memory limit"}${resources?.cpus ? ` and ${cpuWords(resources.cpus)}` : ""}. Change it under Details.` : "Its apps, database, KV store and builds each get at most this share, so it can’t crowd out your other projects. At the limit it slows down first."}
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
                aria-valuetext={`${shown}%, ${shareMeans(shown, base, cpus)}`}
                className="range w-full max-w-[24rem]"
              />
              <p className="mt-1.5 text-sm text-ink-2">
                Right now that’s {shareMeans(shown, base, cpus)}.{busy && <Working> Saving…</Working>}
              </p>
            </div>
          )}
        </Choice>
      </div>
      {children}
    </section>
  );
}

const GB = 1073741824;

/** The manifest's services, as far as the limits go. */
type Services = { postgres?: { statementTimeoutSeconds?: number }; valkey?: { maxMemoryMB?: number } };

const DEFAULT_QUERY_SECONDS = 30;
const DEFAULT_CACHE_MB = 64;
const field = "ident mt-1 block h-8 w-28 rounded-[7px] border border-rule-2 bg-paper-raised px-2 text-[0.8125rem] text-ink outline-none focus-visible:border-brass";

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
function Details({
  project,
  apps,
  free,
  status,
  exact,
}: {
  project: string;
  apps: Record<string, ManifestApp>;
  free?: number;
  status?: ProjectUsage;
  exact?: { live?: ProjectResources; cpus: number };
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
          {exact && <ExactLimit project={project} live={exact.live} cpus={exact.cpus} />}
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
        </>
      )}
    </section>
  );
}

/** The exact form of a limit: memory in MB and CPUs, instead of a share of the box. */
function ExactLimit({ project, live, cpus }: { project: string; live?: ProjectResources; cpus: number }) {
  const [mem, setMem] = useState(String(live?.memoryMB ?? ""));
  const [cpu, setCpu] = useState(String(live?.cpus ?? ""));
  const m = Number(mem);
  const c = Number(cpu);
  const ok = (mem === "" || (m >= 64 && Number.isFinite(m))) && (cpu === "" || (c > 0 && c <= cpus)) && (mem !== "" || cpu !== "");
  const field = "ident h-8 w-28 rounded-[7px] border border-rule-2 bg-paper-raised px-2 text-[0.8125rem] text-ink outline-none focus-visible:border-brass";
  return (
    <Group label="An exact limit" note="Instead of a share of the box.">
      <form
        className="flex flex-wrap items-end gap-3 py-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!ok) return;
          const to: ProjectResources = { ...(mem ? { memoryMB: m } : {}), ...(cpu ? { cpus: c } : {}) };
          change(project, { kind: "set", path: ["resources"], from: live, to, what: `Limit ${project} to ${[mem && memWords(m), cpu && cpuWords(c)].filter(Boolean).join(" and ")}`, undo: `${project}’s limit goes back as it was` }, { immediate: true });
        }}
      >
        <label className="text-xs text-ink-3">
          Memory (MB)
          <input value={mem} onChange={(e) => setMem(e.target.value.replace(/[^0-9]/g, ""))} inputMode="numeric" placeholder="no limit" className={cn(field, "mt-1 block")} />
        </label>
        <label className="text-xs text-ink-3">
          CPUs
          <input value={cpu} onChange={(e) => setCpu(e.target.value.replace(/[^0-9.]/g, ""))} inputMode="decimal" placeholder="no limit" className={cn(field, "mt-1 block")} />
        </label>
        <Button type="submit" size="md" disabled={!ok}>
          Set this limit
        </Button>
      </form>
    </Group>
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
