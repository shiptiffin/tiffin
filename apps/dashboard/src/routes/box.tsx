import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { notOnBox, type Approval, type BoxResources, type Change, type ProjectState, type StatusReport } from "@/api/client";
import { mod, mod3, mq } from "@/api/modules";
import { q } from "@/api/queries";
import { Breaker, type BreakerState } from "@/components/breaker";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import { Nameplate } from "@/components/nameplate";
import { Page } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Qty } from "@/components/qty";
import { RiskDots } from "@/components/risk-dots";
import { SegMeter } from "@/components/seg-meter";
import { Counts, SignedEntry } from "@/components/signed-entry";
import { Carrier, Lid, Rim, TierColumns, TierHead, TierRow, useUnlatch } from "@/components/stack";
import { INSTANCE_STOPS, Throttle } from "@/components/throttle";
import {
  checkWords,
  useAnalyticsStatus,
  useAppStatus,
  useAuthStatus,
  useEmailStatus,
  usePostgresStatus,
  useStorageStatus,
  useValkeyStatus,
} from "@/components/tier-status";
import { EmptyBoxStart } from "@/components/start-empty-box";
import { NameAsk } from "@/components/name-ask";
import { Button } from "@/components/ui/button";
import heroClosed from "@/assets/illustrations/carrier-hero.webp";
import { Command } from "@/components/copy";
import { mcpCommand } from "@/lib/mcp";
import { boxName, domainFrom, versionLabel, whereItRuns } from "@/lib/box";
import { asTier, intentWords, opCounts, splitAddress, splitRequester } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { enamelVar, useEnamels, type Enamel } from "@/lib/enamel";
import { actorWords } from "@/lib/actors";
import { memoryModel, type MemoryModel } from "@/lib/memory";
import { bytes, bytesParts, count, countWords, dec, duration, int, words } from "@/lib/format";
import { stage, stagedFor, useAllStaged, type StagedEdit } from "@/lib/staged";
import { clock, dayKey, dayLabel, relative } from "@/lib/time";
import { useWaitingWorkflowApprovals } from "@/lib/wf";

const MB = 1048576;
const RESERVE_MB = 512; // kept free for spikes

type AppSpec = { framework?: string; role?: string; instances?: number; memoryMB?: number; path?: string };

/**
 * The Box: the landing page. The machine drawn as a tiffin carrier, with its
 * vitals on the lid, a tier per project (enamel rim) holding its apps
 * (throttles) and services (breakers), the platform tier, and room left.
 * Levers stage changes; nothing applies from here without the plan tray.
 * On the right: what is waiting for you, and the latest Ledger entries.
 */
export function BoxPage() {
  useTitle("Box");
  const status = useQuery(q.status());
  const resQ = useQuery(q.resources);
  // A laptop dev server answers 503, or zeros where it can't measure: treat both as "not measured".
  const res = { data: resQ.data && resQ.data.memory.totalBytes > 0 ? resQ.data : undefined, error: resQ.error ?? (resQ.data && resQ.data.memory.totalBytes === 0 ? new Error("not measured") : null) };
  const mem = useMemo(() => (res.data ? memoryModel(res.data) : undefined), [res.data]);
  const projects = useQuery(q.projects);
  const names = useMemo(() => (projects.data ?? []).map((p) => p.name), [projects.data]);
  const states = useQueries({ queries: names.map((n) => q.project(n)) });
  const enamels = useEnamels(names);
  const pending = useQuery({ ...q.pending, retry: false });
  const onBox = !notOnBox(pending.error);
  const wf = useWaitingWorkflowApprovals(onBox);
  const changes = useQuery(q.changes());
  const approvals = useQuery({ ...q.approvals, enabled: onBox, retry: false });

  const failing = states.flatMap((s) =>
    Object.values(s.data?.status ?? {})
      .filter((r) => r.state === "failed")
      .map((r) => ({ project: s.data!.name, address: r.address })),
  );
  const downServices = (res.data?.services ?? []).filter((s) => s.state === "failed");

  if (projects.isError) {
    return (
      <Page>
        <ProblemNote error={projects.error} title="Can’t read the box." />
      </Page>
    );
  }

  return (
    <Page full>
      <NameAsk />
      {projects.data && names.length === 0 ? (
        <EmptyHeader />
      ) : (
        <Header status={status.data} failing={failing.length + downServices.length} failingFirst={failing[0]} waiting={pending.data ?? []} workflows={wf.length} />
      )}
      <div className="mt-8 grid items-start gap-x-12 gap-y-8 xl:grid-cols-[minmax(0,1fr)_288px]">
        <div className="min-w-0">
          {(pending.data?.length ?? 0) + wf.length > 0 && (
            <div className="mb-8 xl:hidden">
              <Waiting approvals={pending.data ?? []} workflows={wf} />
            </div>
          )}
          <Carrier>
            <Lid>
              <Nameplate
                name={boxName(status.data)}
                where={whereItRuns(status.data)}
                version={versionLabel(status.data)}
                uptime={res.data ? `up ${duration(res.data.uptimeSeconds)}` : undefined}
                domain={<BoxDomain projects={names} />}
              />
              <Vitals res={res.data} mem={mem} unavailable={!!res.error} names={names} enamels={enamels} />
            </Lid>
            <Rim className="max-sm:hidden" />
            {names.length > 0 && <TierColumns />}
            {projects.isPending ? (
              <div className="h-40" />
            ) : names.length === 0 ? (
              <EmptyBoxStart headline={false} />
            ) : (
              states.map((s, i) =>
                s.data ? (
                  <ProjectTier key={names[i]} state={s.data} enamel={enamels[names[i]]} mem={mem} approvals={pending.data ?? []} />
                ) : (
                  <div key={names[i]}>
                    <Rim enamel={enamels[names[i]]} />
                    <div className="h-24" />
                  </div>
                ),
              )
            )}
            <Rim />
            <PlatformTier res={res.data} mem={mem} status={status.data} unavailable={!!res.error} />
            <RoomLeft res={res.data} mem={mem} states={states.map((s) => s.data)} empty={names.length === 0} />
          </Carrier>
        </div>
        <aside className="flex min-w-0 flex-col gap-9 xl:pt-0" aria-label="Waiting and latest">
          {(pending.data?.length ?? 0) + wf.length > 0 && (
            <div className="max-xl:hidden">
              <Waiting approvals={pending.data ?? []} workflows={wf} />
            </div>
          )}
          <Latest changes={changes.data} approvals={approvals.data ?? []} enamels={enamels} />
          {projects.data && names.length === 0 && (
            <section aria-label="Hand it to your agent">
              <h2 className="text-[0.9375rem] font-[550] text-ink">Or hand it to your agent</h2>
              <p className="mt-1 mb-3 text-sm text-ink-2">It plans changes on its own; anything risky waits here for you.</p>
              <Command cmd={mcpCommand()} />
            </section>
          )}
        </aside>
      </div>
    </Page>
  );
}

// ───────────────────────── header ─────────────────────────

const dateFmt = new Intl.DateTimeFormat("en-GB", { weekday: "long", day: "numeric", month: "long" });

function Header({
  status,
  failing,
  failingFirst,
  waiting,
  workflows,
}: {
  status?: StatusReport;
  failing: number;
  failingFirst?: { project: string; address: string };
  waiting: Approval[];
  workflows: number;
}) {
  const [now] = useState(() => new Date());
  const failedChecks = (status?.checks ?? []).filter((c) => !c.ok);
  let health: string;
  if (failing === 1 && failingFirst) health = `${splitAddress(failingFirst.address).name || failingFirst.address} in ${failingFirst.project} is down.`;
  else if (failing > 1) health = `${countWords(failing, "part", "parts", true)} of the box need a look.`;
  else if (failedChecks.length > 0) health = `${countWords(failedChecks.length, "check", "checks", true)} failing: ${failedChecks.map((c) => c.name).join(", ")}.`;
  else health = "Everything is running.";
  const people = [...new Set(waiting.map((a) => actorWords({ kind: "agent", name: splitRequester(a.requester).name })))];
  let wait = "";
  if (waiting.length === 1) wait = `${people[0]} is waiting for you on one change.`;
  else if (waiting.length > 1)
    wait = people.length === 1 ? `${people[0]} is waiting for you on ${words(waiting.length)} changes.` : `${words(people.length, true)} agents are waiting for you.`;
  else if (workflows > 0) wait = workflows === 1 ? "A workflow is waiting for a person." : `${words(workflows, true)} workflows are waiting for a person.`;
  return (
    <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <p className="label mb-2">
          {dateFmt.format(now)} · {clock(now.toISOString())}
        </p>
        <h1 className="sentence text-ink">
          {health}
          {wait && <> {wait}</>}
        </h1>
      </div>
      <Button asChild variant="secondary" size="lg" className="self-start sm:self-auto">
        <Link to="/new">New project</Link>
      </Button>
    </header>
  );
}

/** The first visit: the box is up and empty. The carrier as a picture, one sentence, and where to start. */
function EmptyHeader() {
  const [now] = useState(() => new Date());
  return (
    <header className="grid items-center gap-x-10 gap-y-4 sm:grid-cols-[minmax(0,1fr)_200px] lg:grid-cols-[minmax(0,1fr)_240px]">
      <div className="min-w-0">
        <p className="label mb-2">
          {dateFmt.format(now)} · {clock(now.toISOString())}
        </p>
        <h1 className="sentence text-ink">Your tiffin is packed. Nothing in it yet.</h1>
        <p className="mt-2 max-w-[38rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
          Every part of the box below passed its checks. Start a project and it gets its own address, a database and sign-in if it wants them, live in
          under a minute.
        </p>
      </div>
      <img src={heroClosed} alt="" width={240} height={240} className="mx-auto -my-6 w-[200px] max-sm:hidden lg:w-[240px]" />
    </header>
  );
}

function BoxDomain({ projects }: { projects: string[] }) {
  // Any project's storage endpoint names the box's domain (s3.<domain>); on the box itself the dashboard's own host does too.
  const st = useQueries({ queries: projects.map((p) => ({ ...mq.storage(p), retry: false, staleTime: 300_000 })) });
  const endpoint = st.map((x) => x.data?.endpoint).find(Boolean);
  const d = domainFrom(endpoint) ?? location.hostname.replace(/^dashboard\./, "");
  return <>{d}</>;
}

// ───────────────────────── vitals ─────────────────────────

function Vitals({
  res,
  mem,
  unavailable,
  names,
  enamels,
}: {
  res?: BoxResources;
  mem?: MemoryModel;
  unavailable: boolean;
  names: string[];
  enamels: Record<string, Enamel>;
}) {
  if (unavailable) return <p className="mt-4 text-sm text-ink-3">Memory, CPU and disk are measured on a running box. This server runs without one.</p>;
  if (!res || !mem) return <div className="mt-4 h-[88px]" />;
  const used = bytesParts(mem.usedMB * MB, 2);
  const total = bytesParts(mem.totalMB * MB, 1);
  const disk = res.disks.data;
  const dUsed = bytesParts(disk.usedBytes, 1);
  const dTotal = bytesParts(disk.totalBytes, 0);
  const partsMB = mem.platformMB - mem.systemMB;
  const seg = (v: number) => `${Math.max(0, (v / mem.totalMB) * 100)}%`;
  const withApps = names.filter((n) => (mem.projects[n] ?? 0) > 0);
  return (
    <div className="mt-4 grid grid-cols-2 gap-x-7 gap-y-5 sm:grid-cols-[1.9fr_1fr_1fr] max-sm:gap-y-4">
      <div className="min-w-0 max-sm:col-span-full">
        <p className="label">Memory</p>
        <p className="reading mt-0.5">
          {used.value}
          <span className="u text-[0.8125rem]">&#8239;{used.unit} in use of {total.value}&#8239;{total.unit}</span>
        </p>
        <div
          className="mt-2.5 flex h-2.5 gap-0.5"
          role="img"
          aria-label={`Memory: ${int(mem.usedMB)} MB in use of ${int(mem.totalMB)} MB; ${int(mem.freeMB)} MB room left`}
        >
          {withApps.map((n) => (
            <span key={n} className="h-full min-w-[3px] rounded-[2px]" style={{ width: seg(mem.projects[n]), background: enamelVar(enamels[n]) }} />
          ))}
          {partsMB > 0 && <span className="h-full min-w-[3px] rounded-[2px] bg-[var(--part-3)]" style={{ width: seg(partsMB) }} />}
          {mem.systemMB > 0 && <span className="h-full min-w-[3px] rounded-[2px] bg-[var(--part-4)]" style={{ width: seg(mem.systemMB) }} />}
          <span className="h-full flex-1 rounded-[2px] border border-dashed border-rule-3" />
        </div>
        <p className="mt-2 flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-ink-3">
          {withApps.map((n) => (
            <span key={n} className="inline-flex items-center gap-1.5">
              <EnamelSwatch enamel={enamels[n]} size={7} />
              {n}
            </span>
          ))}
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px] bg-[var(--part-3)]" />
            platform
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px] bg-[var(--part-4)]" />
            Linux and builds
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px] border border-dashed border-rule-3" />
            room left
          </span>
        </p>
      </div>
      <div className="min-w-0">
        <p className="label">CPU</p>
        <p className="reading mt-0.5">
          {dec(res.cpu.usedPercent, 0)}
          <span className="u text-[0.8125rem]">&#8239;% of {countWords(res.cpu.count, "CPU")}</span>
        </p>
        <SegMeter className="mt-2.5" label="CPU in use" value={res.cpu.usedPercent} scale warnAt={0.8} fullAt={0.95} valueText={`${dec(res.cpu.usedPercent, 0)} percent`} />
      </div>
      <div className="min-w-0">
        <p className="label">Disk</p>
        <p className="reading mt-0.5">
          {dUsed.value}
          <span className="u text-[0.8125rem]">
            &#8239;{dUsed.unit} of {dTotal.value}&#8239;{dTotal.unit}
          </span>
        </p>
        <SegMeter className="mt-2.5" label="Data disk in use" value={disk.usedPercent} scale warnAt={0.8} fullAt={0.95} valueText={`${dec(disk.usedPercent, 0)} percent`} />
      </div>
    </div>
  );
}

// ───────────────────────── project tiers ─────────────────────────

const servicePages: Record<string, { label: string; sub: string; to: string }> = {
  postgres: { label: "Postgres", sub: "Database", to: "/projects/$project/data" },
  valkey: { label: "Valkey", sub: "Cache and key-value", to: "/projects/$project/data/kv" },
  storage: { label: "Storage", sub: "Buckets", to: "/projects/$project/storage" },
  email: { label: "Email", sub: "Mail", to: "/projects/$project/email" },
  auth: { label: "Sign-in", sub: "Users and sessions", to: "/projects/$project/users" },
  analytics: { label: "Analytics", sub: "Visitors and events", to: "/projects/$project/analytics" },
};
const serviceOrder = ["postgres", "valkey", "storage", "email", "auth", "analytics"];

function ProjectTier({ state, enamel, mem, approvals }: { state: ProjectState; enamel: Enamel; mem?: MemoryModel; approvals: Approval[] }) {
  const p = state.name;
  const edits = useAllStaged()[p] ?? [];
  const { lifting, open } = useUnlatch();
  const resources = state.resources ?? [];
  const apps = resources.filter((r) => r.address.startsWith("app/")).map((r) => ({ name: r.address.slice(4), spec: (r.spec ?? {}) as AppSpec }));
  const services = serviceOrder.filter((s) => resources.some((r) => r.address === `service/${s}`));
  const total = mem?.projects[p] ?? 0;
  const free = mem ? mem.freeMB - RESERVE_MB : undefined;
  const bound = new Set(approvals.filter((a) => a.project === p).flatMap((a) => (a.plan.ops ?? []).map((o) => o.address)));
  const host = useHost(p, apps[0]?.name);
  return (
    <section aria-label={`Project ${p}`}>
      <Rim enamel={enamel} />
      <TierHead
        href={
          <Link to="/projects/$project" params={{ project: p }} className="inline-flex items-center gap-2 hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {p}
          </Link>
        }
        about={
          <>
            {countWords(apps.length, "app")}, {countWords(services.length, "service")}
            {host && (
              <>
                {" "}
                · <span className="ident text-[0.75rem] text-ink-2">{host}</span>
              </>
            )}
          </>
        }
        total={mem ? apps.length > 0 ? int(total) : <span className="font-[400] text-ink-3" title="No apps, so nothing of its own in memory">–</span> : undefined}
      />
      <ProjectNote project={p} apps={apps.length} services={services} />
      {apps.map((a) => (
        <AppRow
          key={a.name}
          project={p}
          app={a.name}
          spec={a.spec}
          memory={mem ? mem.app(p, a.name) : undefined}
          free={free}
          staged={stagedFor(edits, `instances:${a.name}`)}
          fault={state.status?.[`app/${a.name}`]?.state === "failed" ? state.status[`app/${a.name}`].message : undefined}
          bound={bound.has(`app/${a.name}`)}
          unlatching={lifting === `app/${a.name}`}
          onOpen={() => open(`app/${a.name}`, "/projects/$project/apps/$app", { project: p, app: a.name })}
        />
      ))}
      {services.map((s) => (
        <ServiceRow
          key={s}
          project={p}
          service={s}
          state={state.status?.[`service/${s}`]?.state === "failed" ? "tripped" : "on"}
          message={state.status?.[`service/${s}`]?.message}
          staged={stagedFor(edits, `service:${s}`)}
          bound={bound.has(`service/${s}`)}
          unlatching={lifting === `service/${s}`}
          onOpen={() => open(`service/${s}`, servicePages[s].to, { project: p })}
        />
      ))}
    </section>
  );
}

/** What the project's MB column does and doesn't count, with what can honestly be attributed to it. */
function ProjectNote({ project, apps, services }: { project: string; apps: number; services: string[] }) {
  const pg = useQuery({ queryKey: ["pg", project], queryFn: () => mod.pg(project), enabled: services.includes("postgres"), retry: false, staleTime: 15_000 });
  const st = useQuery({ ...mq.storage(project), enabled: services.includes("storage"), retry: false, staleTime: 15_000 });
  const held = [
    pg.data ? `its database (${bytes(pg.data.sizeBytes)} on disk)` : null,
    st.data && st.data.usedBytes > 0 ? `its files (${bytes(st.data.usedBytes)})` : null,
  ].filter(Boolean) as string[];
  if (services.length === 0) return null;
  const lead = apps > 0 ? "Memory is its apps’ own." : "No apps yet, so nothing of its own in memory.";
  const rest = held.length > 0 ? ` ${held.join(" and ").replace(/^./, (c) => c.toUpperCase())} ${held.length > 1 ? "live" : "lives"} in the shared platform below.` : " Its services run in the shared platform below.";
  return (
    <div className="tier-grid -mt-1.5 pb-2 max-sm:block max-sm:px-3.5">
      <p className="col-span-4 col-start-2 text-xs leading-4 text-ink-3">
        {lead}
        {rest}
      </p>
    </div>
  );
}

function useHost(project: string, app?: string) {
  const rt = useQuery({
    queryKey: ["runtime", project, app ?? ""],
    queryFn: () => mod3.runtime(project, app!),
    enabled: !!app,
    retry: false,
    staleTime: 300_000,
  });
  const url = rt.data?.production?.url;
  if (!url) return undefined;
  try {
    return new URL(url).hostname;
  } catch {
    return undefined;
  }
}

function AppRow({
  project,
  app,
  spec,
  memory,
  free,
  staged,
  fault,
  bound,
  unlatching,
  onOpen,
}: {
  project: string;
  app: string;
  spec: AppSpec;
  memory?: number;
  free?: number;
  staged?: StagedEdit;
  fault?: string;
  bound: boolean;
  unlatching: boolean;
  onOpen: () => void;
}) {
  const applied = spec.instances ?? 1;
  const per = spec.memoryMB ?? 512;
  const shown = staged?.kind === "instances" ? staged.to : applied;
  const [preview, setPreview] = useState<number | null>(null);
  const live = useAppStatus(project, app, spec.role, spec.framework);
  const maxFit = free === undefined ? undefined : applied + Math.max(0, Math.floor(free / per));
  const n = preview ?? shown;
  const memMB = memory;
  const sub = `${frameworkName(spec.framework)} · ${count(shown, "instance")}`;
  const readout =
    preview !== null && preview !== applied ? (
      <span>
        <b className="font-[550] text-ink">{count(n, "instance")}</b>, up to {int(n * per)}&#8239;MB.{" "}
        {free !== undefined && <>Room left after {int(Math.max(0, free + RESERVE_MB - (n - applied) * per))}&#8239;MB.</>}
      </span>
    ) : null;
  return (
    <TierRow
      lever={
        <Throttle
          size="mini"
          label={`${app} instances`}
          stops={INSTANCE_STOPS}
          value={shown}
          applied={applied}
          maxFit={maxFit}
          onChange={setPreview}
          onCommit={(to) => {
            setPreview(null);
            stage(project, { kind: "instances", app, from: applied, to });
          }}
        />
      }
      nameLink={
        <span className="inline-flex items-center gap-2">
          <Link to="/projects/$project/apps/$app" params={{ project, app }} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {app}
          </Link>
          {live.pilot && <PilotLight state={live.pilot} label={live.pilot === "busy" ? "Building" : "Deploy failed"} />}
          {staged && <span className="label text-brass-ink">staged</span>}
        </span>
      }
      name={app}
      sub={sub}
      status={readout ?? (fault ? <span className="text-danger">{fault}</span> : live.sentence)}
      share={
        memMB !== undefined &&
        memMB >= 1 && (
          <SegMeter
            size="row"
            segments={16}
            max={512}
            value={Math.min(512, memMB)}
            add={staged?.kind === "instances" ? (memMB / Math.max(1, applied)) * (staged.to - applied) : 0}
            label={`${app} memory`}
            valueText={`${int(memMB)} MB of a 512 MB scale`}
          />
        )
      }
      amount={
        memMB === undefined ? null : staged?.kind === "instances" ? (
          <span className="whitespace-nowrap">
            <span className="text-ink-3">{int(memMB)} →</span> {int((memMB / Math.max(1, applied)) * staged.to)}
          </span>
        ) : (
          <span className={cn(memMB < 0.5 && "text-ink-3")}>{int(memMB)}</span>
        )
      }
      staged={!!staged}
      fault={!!fault || live.fault}
      bound={bound}
      unlatching={unlatching}
      onOpen={onOpen}
    />
  );
}

function frameworkName(f?: string) {
  const names: Record<string, string> = { next: "Next.js", hono: "Hono", bun: "Bun", static: "Static site", node: "Node", astro: "Astro", vite: "Vite", sveltekit: "SvelteKit", remix: "Remix" };
  return f ? (names[f] ?? f) : "App";
}

function ServiceRow({
  project,
  service,
  state,
  message,
  staged,
  bound,
  unlatching,
  onOpen,
}: {
  project: string;
  service: string;
  state: BreakerState;
  message?: string;
  staged?: StagedEdit;
  bound: boolean;
  unlatching: boolean;
  onOpen: () => void;
}) {
  const meta = servicePages[service];
  const sentence = useServiceSentence(project, service);
  const st = staged?.kind === "service" ? staged.to : undefined;
  return (
    <TierRow
      lever={
        <Breaker
          label={meta.label}
          state={state}
          staged={st}
          onFlip={(next) => (state === "tripped" ? onOpen() : stage(project, { kind: "service", service, from: "on", to: next }))}
        />
      }
      nameLink={
        <span className="inline-flex items-center gap-2">
          <Link to={meta.to as "/"} params={{ project } as never} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {meta.label}
          </Link>
          {staged && <span className="label text-brass-ink">staged</span>}
        </span>
      }
      name={meta.label}
      sub={<span className="max-sm:hidden">{sentence.sub ?? meta.sub}</span>}
      status={
        st === "off" ? (
          <span className="text-brass-ink">Comes out of {project} when you apply. The plan says what that costs.</span>
        ) : state === "tripped" ? (
          <span className="text-danger">Tripped{message ? `: ${message}` : ""}. Open it to see why.</span>
        ) : (
          sentence.text
        )
      }
      staged={!!staged}
      fault={state === "tripped"}
      bound={bound}
      unlatching={unlatching}
      onOpen={onOpen}
    />
  );
}

function useServiceSentence(project: string, service: string): { text: ReactNode; sub?: string } {
  // Each hook runs only for its own service (the rows are keyed by service, so hook order is stable).
  switch (service) {
    case "postgres":
      return { text: <PgSentence project={project} /> };
    case "valkey":
      return { text: <KvSentence project={project} /> };
    case "storage":
      return { text: <StorageSentence project={project} /> };
    case "email":
      return { text: <EmailSentence project={project} /> };
    case "auth":
      return { text: <AuthSentence project={project} /> };
    case "analytics":
      return { text: <AnalyticsSentence project={project} /> };
    default:
      return { text: null };
  }
}
const PgSentence = ({ project }: { project: string }) => <>{usePostgresStatus(project)}</>;
const KvSentence = ({ project }: { project: string }) => <>{useValkeyStatus(project)}</>;
const StorageSentence = ({ project }: { project: string }) => <>{useStorageStatus(project).sentence}</>;
const EmailSentence = ({ project }: { project: string }) => <>{useEmailStatus(project).sentence}</>;
const AuthSentence = ({ project }: { project: string }) => <>{useAuthStatus(project)}</>;
const AnalyticsSentence = ({ project }: { project: string }) => <>{useAnalyticsStatus(project)}</>;

// ───────────────────────── platform ─────────────────────────

const platformRows: Array<{ key: string; name: string; sub: string; units: string[]; check?: string[]; to?: string; say?: (s: StatusReport | undefined) => string }> = [
  {
    key: "tiffin",
    name: "Tiffin",
    sub: "API, dashboard, edge",
    units: ["tiffin"],
    check: ["edge"],
    to: "/status",
    say: (s) => `Serves the dashboard, the API and every app’s HTTPS. Running for ${uptimeWords(s?.uptime)}.`,
  },
  {
    key: "postgres",
    name: "Postgres",
    sub: "Every project’s database",
    units: ["postgres"],
    check: ["postgres"],
    to: "/backups",
    say: (s) => checkWords(detail(s, "postgres").replace(/^Postgres [\d.]+(?: \([^)]*\))? up, /, "")),
  },
  { key: "valkey", name: "Valkey", sub: "Caches and key-value", units: ["valkey"], check: ["valkey"], say: (s) => checkWords(detail(s, "valkey").replace(/^Valkey [\d.]+ up, /, "").replace(/\.0 MB/g, " MB")) },
  { key: "auth", name: "Sign-in engine", sub: "Users and sessions", units: ["auth"], check: ["auth"] },
  { key: "storage", name: "Storage", sub: "S3-compatible buckets", units: ["storage"], check: ["storage"], say: (s) => checkWords(detail(s, "storage").replace(/^versitygw v[\d.]+ on [\d.:]+,\s*/i, "")) },
  { key: "observe", name: "Metrics and logs", sub: "Kept on the box", units: ["victoria-metrics", "victoria-logs"], check: ["observe.metrics"], to: "/metrics", say: () => "Every app’s metrics and logs, stored here. Nothing leaves the box." },
  {
    key: "protect",
    name: "Protection",
    sub: "CrowdSec, firewall",
    units: ["crowdsec", "firewall", "app-firewall"],
    check: ["protection"],
    to: "/protect",
    say: (s) => {
      const d = detail(s, "protection");
      const mode = d.match(/^(\w+) mode/)?.[1] ?? "normal";
      const bans = Number(d.match(/CrowdSec (\d+) ban/)?.[1] ?? 0);
      return `${mode.charAt(0).toUpperCase()}${mode.slice(1)} mode. ${bans === 0 ? "Nobody banned right now." : `${countWords(bans, "address", "addresses", true)} banned right now.`}`;
    },
  },
];

function PlatformTier({ res, mem, status, unavailable }: { res?: BoxResources; mem?: MemoryModel; status?: StatusReport; unavailable: boolean }) {
  const { lifting, open } = useUnlatch();
  const [shown, setShown] = useState(false); // phones: collapsed until asked
  const svc = (names: string[]) => (res?.services ?? []).filter((s) => names.includes(s.name));
  // A sample can come back without the service list (systemd busy); then show the parts without numbers.
  const measured = !!mem && (res?.services ?? []).length > 0;
  const rows = platformRows.filter((r) => !measured || svc(r.units).length > 0);
  const runtime = detail(status, "runtime");
  const hide = shown ? undefined : "max-sm:hidden";
  return (
    <section aria-label="Platform">
      <TierHead
        name="Platform"
        about="What keeps the box running, shared by every project."
        total={measured ? int(mem!.platformMB) : undefined}
      />
      {unavailable && <p className="px-5 pb-4 text-sm text-ink-3 max-sm:px-3.5">Measured on a running box.</p>}
      {!unavailable && (
        <button
          type="button"
          onClick={() => setShown((x) => !x)}
          aria-expanded={shown}
          className="mx-3.5 mb-2 text-[0.8125rem] font-[550] text-brass-ink sm:hidden"
        >
          {shown ? "Hide its parts" : `Show its ${words(rows.length + 1)} parts`}
        </button>
      )}
      {!unavailable &&
        rows.map((r) => {
          const units = svc(r.units);
          const mbv = measured ? (mem!.parts[r.key] ?? 0) : undefined;
          const failed = units.find((s) => s.state === "failed");
          const check = status?.checks?.find((c) => r.check?.includes(c.name));
          const sentence = failed ? (
            <span className="text-danger">{failed.name} has stopped. Restarts so far: {failed.restarts}.</span>
          ) : r.say ? (
            r.say(status)
          ) : check ? (
            <span className={cn(!check.ok && "text-danger")}>{checkWords(check.detail)}</span>
          ) : (
            "Running."
          );
          return (
            <div key={r.name} className={hide}>
              <TierRow
                lever={failed ? <PilotLight state="fault" label="Stopped" /> : undefined}
                name={r.name}
                sub={r.sub}
                status={sentence}
                share={mbv !== undefined && mbv >= 1 && <SegMeter size="row" segments={16} max={512} value={Math.min(512, mbv)} label={`${r.name} memory`} valueText={`${int(mbv)} MB`} />}
                amount={mbv !== undefined ? int(mbv) : undefined}
                fault={!!failed}
                unlatching={lifting === r.name}
                onOpen={r.to ? () => open(r.name, r.to!) : undefined}
              />
            </div>
          );
        })}
      {!unavailable && (
        <div className={hide}>
          <TierRow
            name="Linux and builds"
            sub="Kernel, containers, BuildKit"
            status={
              <>
                {runtime ? `${checkWords(runtime).replace(/\.$/, "")}. ` : ""}
                {measured && mem!.cacheMB > 0 && <span className="text-ink-3">Not counted: about {int(mem!.cacheMB)}&#8239;MB of cache Linux hands back when apps need it.</span>}
              </>
            }
            share={measured && mem!.systemMB >= 1 && <SegMeter size="row" segments={16} max={512} value={Math.min(512, mem!.systemMB)} label="Linux and builds memory" valueText={`${int(mem!.systemMB)} MB`} />}
            amount={measured ? int(mem!.systemMB) : undefined}
          />
        </div>
      )}
    </section>
  );
}

// ───────────────────────── room left ─────────────────────────

function RoomLeft({ res, mem, states, empty }: { res?: BoxResources; mem?: MemoryModel; states: Array<ProjectState | undefined>; empty: boolean }) {
  if (!res || !mem) return <div className="m-3 h-16" />;
  const free = mem.freeMB;
  // "The size of web": web's memory cap, or the first app's, or 512 MB.
  const caps = states.flatMap((s) => (s?.resources ?? []).filter((r) => r.address.startsWith("app/")).map((r) => ({ app: r.address.slice(4), mb: ((r.spec ?? {}) as AppSpec).memoryMB ?? 512 })));
  const like = caps.find((c) => c.app === "web") ?? caps[0];
  const per = like?.mb ?? 512;
  const fits = Math.max(0, Math.floor((free - RESERVE_MB) / per));
  const say = empty
    ? `Room for about ${words(fits)} apps of ${int(per)}\u202FMB each, keeping ${int(RESERVE_MB)}\u202FMB spare.`
    : `Enough for about ${words(fits)} more ${fits === 1 ? "app" : "apps"} the size of ${like?.app ?? "an app"} (up to ${int(per)}\u202FMB each), keeping ${int(RESERVE_MB)}\u202FMB spare.`;
  const disk = bytesParts(res.disks.data.freeBytes, 0);
  return (
    <>
      <div className="rim mt-1" data-thin aria-hidden />
      <div className="tier-grid min-h-[44px] max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:px-3.5">
        <div className="col-start-2 text-[0.875rem] max-sm:col-start-1">In use</div>
        <div className="col-span-2 col-start-3 text-xs text-ink-3 max-sm:hidden">
          The projects’ apps and the platform, as Linux counts it, of {int(mem.totalMB)}&#8239;MB.
        </div>
        <div className="col-start-5 text-right text-[0.875rem] font-[550] tnum max-sm:col-start-2">{int(mem.usedMB)}</div>
      </div>
      <div className="room tier-grid m-3 mt-1 min-h-16 py-3.5 max-sm:mx-2 max-sm:flex max-sm:flex-col max-sm:items-start max-sm:gap-1.5 max-sm:px-3.5">
        <div className="col-start-2 flex w-full items-baseline justify-between text-[0.875rem] font-[550]">
          Room left
          <span className="tnum sm:hidden">{int(free)}&#8239;MB</span>
        </div>
        <p className="col-span-2 col-start-3 text-[0.84375rem] leading-5 text-ink-2">
          {say} {disk.value}&#8239;{disk.unit} of disk free.{" "}
          <Link to="/new" className="font-[550] whitespace-nowrap text-brass-ink hover:underline hover:underline-offset-4">
            Start a project
          </Link>
        </p>
        <div className="col-start-5 text-right max-sm:hidden">
          <Qty value={int(free)} className="text-[0.9375rem] font-[550]" />
        </div>
      </div>
    </>
  );
}

// ───────────────────────── rail ─────────────────────────

function Waiting({ approvals, workflows }: { approvals: Approval[]; workflows: ReturnType<typeof useWaitingWorkflowApprovals> }) {
  return (
    <section aria-label="Waiting for you" className="flex flex-col gap-3">
      {approvals.map((a) => {
        const tier = asTier(a.plan.risk);
        const c = opCounts(a.plan.ops);
        return (
          <article key={a.id} className="rounded-[10px] border border-rule bg-paper-raised px-4 pt-3.5 pb-4 shadow-raised">
            <p className="label text-brass-ink">Waiting for you</p>
            <p className="mt-2 text-[0.78125rem] text-ink-3">
              <b className="font-[550] text-graphite">{actorWords({ kind: "agent", name: splitRequester(a.requester).name })}</b>
              {splitRequester(a.requester).session && <span className="ident ml-1.5 text-[0.71875rem]">session {splitRequester(a.requester).session}</span>} asks
              to change <span className="text-ink-2">{a.project}</span>
            </p>
            <p className="entry mt-1 text-graphite">{intentWords({ intent: a.intent, plan: a.plan })}</p>
            <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-3">
              <RiskDots tier={tier} />
              <span>expires {relative(a.expiresAt)}</span>
            </p>
            <div className="mt-3 flex items-center justify-between">
              <Counts {...c} />
              <Button asChild variant="primary" size="md">
                <Link to="/approvals/$id" params={{ id: a.id }}>
                  Review
                </Link>
              </Button>
            </div>
          </article>
        );
      })}
      {workflows.map((w) => (
        <article key={w.id} className="rounded-[10px] border border-rule bg-paper-raised px-4 pt-3.5 pb-4 shadow-raised">
          <p className="label text-brass-ink">Waiting for a person</p>
          <p className="mt-2 text-[0.78125rem] text-ink-3">
            Workflow <span className="ident text-ink-2">{w.workflow}</span> in {w.project}
          </p>
          <p className="entry mt-1 text-ink">{w.title}</p>
          {w.description && <p className="mt-1 text-sm text-ink-2">{w.description}</p>}
          <div className="mt-3 flex items-center justify-end">
            <Button asChild variant="secondary" size="md">
              <Link to="/projects/$project/workflows" params={{ project: w.project }}>
                Decide
              </Link>
            </Button>
          </div>
        </article>
      ))}
    </section>
  );
}

function Latest({ changes, approvals, enamels }: { changes?: Change[]; approvals: Approval[]; enamels: Record<string, Enamel> }) {
  const list = (changes ?? []).slice(0, 6);
  const byChange = new Map(approvals.filter((a) => a.usedBy).map((a) => [a.usedBy!, a]));
  const days: Array<{ key: string; label: string; first: string; items: Change[] }> = [];
  for (const c of list) {
    const k = dayKey(c.at);
    const last = days[days.length - 1];
    if (last?.key === k) last.items.push(c);
    else days.push({ key: k, label: dayLabel(c.at), first: c.at, items: [c] });
  }
  return (
    <section aria-label="Latest in the Ledger">
      <div className="flex items-baseline justify-between">
        <h2 className="text-[0.9375rem] font-[550] text-ink">Ledger</h2>
        <Link to="/ledger" search={{}} className="text-[0.8125rem] font-[550] text-brass-ink hover:underline hover:underline-offset-4">
          All entries
        </Link>
      </div>
      {!changes ? (
        <div className="mt-4 h-40" />
      ) : list.length === 0 ? (
        <p className="mt-3 text-sm text-ink-3">Nothing has changed yet. Every change to the box will be written down here, signed and undoable.</p>
      ) : (
        days.map((d) => (
          <div key={d.key} className="mt-5">
            <h3 className="flex items-baseline gap-2">
              <span className="day text-ink">{d.label.split(",")[0]}</span>
              {d.label === "Today" || d.label === "Yesterday" ? null : <span className="text-xs text-ink-3">{d.label.split(", ")[1]}</span>}
            </h3>
            <div className="divide-y divide-rule">
              {d.items.map((c) => {
                const ap = byChange.get(c.id);
                return (
                  <SignedEntry
                    key={c.id}
                    time={clock(c.at)}
                    actor={{ kind: c.actor.kind, name: c.actor.name || c.actor.id, session: c.actor.session }}
                    intent={intentWords(c)}
                    to="/changes/$id"
                    params={{ id: c.id }}
                    counts={opCounts(c.plan.ops)}
                    tier={asTier(c.plan.risk)}
                    muted={!!c.undoneBy}
                    signature={ap ? `approved by ${ap.decidedBy ?? "a person"} · passkey` : c.actor.kind === "agent" ? "within its grant" : c.undoneBy ? "undone" : undefined}
                    extra={
                      <span className="inline-flex items-center gap-1.5">
                        <EnamelSwatch enamel={enamels[c.project] ?? "indigo"} size={6} />
                        {c.project}
                      </span>
                    }
                  />
                );
              })}
            </div>
          </div>
        ))
      )}
    </section>
  );
}


const detail = (s: StatusReport | undefined, name: string) => s?.checks?.find((c) => c.name === name)?.detail ?? "";
const uptimeWords = (go: string | undefined) => {
  if (!go) return "a while";
  const m = go.match(/(?:(\d+)h)?(?:(\d+)m(?!s))?(?:([\d.]+)s)?/);
  const secs = m ? Number(m[1] ?? 0) * 3600 + Number(m[2] ?? 0) * 60 + Number(m[3] ?? 0) : 0;
  return duration(secs);
};
