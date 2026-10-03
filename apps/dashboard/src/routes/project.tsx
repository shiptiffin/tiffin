import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, ChevronRight, Trash2, Undo2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { ApiError, api, notOnBox, type Manifest, type ManifestApp, type SecretInfo } from "@/api/client";
import { mod3 } from "@/api/modules";
import { q } from "@/api/queries";
import { Breaker } from "@/components/breaker";
import { Confirm } from "@/components/confirm";
import { EnamelSwatch } from "@/components/enamel-swatch";
import { useTitle } from "@/components/favicon";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Rim } from "@/components/stack";
import { SignedEntry } from "@/components/signed-entry";
import { AddMenu } from "@/components/start-add-menu";
import { EnamelPicker } from "@/components/start-enamel-picker";
import { INSTANCE_STOPS, Throttle } from "@/components/throttle";
import {
  useAnalyticsStatus,
  useAppStatus,
  useAuthStatus,
  useEmailStatus,
  usePostgresStatus,
  useStorageStatus,
  useValkeyStatus,
} from "@/components/tier-status";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { asTier, intentWords, opCounts } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { appearanceQuery, enamelNames, useEnamel, type Enamel } from "@/lib/enamel";
import { count, countWords, cronWords, dec, int, withUnit, words } from "@/lib/format";
import { useMe } from "@/lib/me";
import { stage, stagedFor, useStaged, type StagedEdit } from "@/lib/staged";
import { frameworkName } from "@/lib/starters";
import { clock, dayLabel, relative } from "@/lib/time";


const MB = 1048576;
const RESERVE_MB = 512; // kept free for spikes, as the Box and the tray do
/** Memory per instance an app can snap to. */
export const MEMORY_STOPS = [256, 512, 1024, 2048, 4096];
const mbWords = (n: number) => (n >= 1024 ? withUnit(dec(n / 1024, 1), "GB") : withUnit(int(n), "MB"));

const SERVICES: Array<{ key: string; label: string; sub: string; to: string }> = [
  { key: "postgres", label: "Postgres", sub: "Database", to: "/projects/$project/data" },
  { key: "valkey", label: "Valkey", sub: "Cache and key-value", to: "/projects/$project/data/kv" },
  { key: "storage", label: "Storage", sub: "Buckets", to: "/projects/$project/storage" },
  { key: "email", label: "Email", sub: "Mail", to: "/projects/$project/email" },
  { key: "auth", label: "Sign-in", sub: "Users and sessions", to: "/projects/$project/users" },
  { key: "analytics", label: "Analytics", sub: "Visitors and events", to: "/projects/$project/analytics" },
];

type SetEdit = Extract<StagedEdit, { kind: "set" }>;

/**
 * A project in detail: every part of its tier with its lever (throttles for
 * instances and memory, breakers for services), the rest of its config as
 * rows, and Add. Levers and Add stage changes; the plan tray applies them.
 * On the right: its colour, its addresses, secrets, and its Ledger.
 */
export function ProjectPage({ project }: { project: string }) {
  useTitle(project);
  const p = useQuery(q.project(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000, refetchInterval: 10_000 });
  const projects = useQuery(q.projects);
  const others = (projects.data ?? []).map((x) => x.name).filter((n) => n !== project);
  const otherManifests = useQueries({ queries: others.map((n) => ({ ...q.manifest(n), staleTime: 60_000 })) });
  const routes = useMemo(
    () => otherManifests.flatMap((x) => Object.entries(x.data?.manifest.apps ?? {}).flatMap(([n, a]) => (a.role === "worker" ? [] : (a.routes?.length ? a.routes : [n]).map((r) => r.split(".")[0])))),
    [otherManifests],
  );
  const resQ = useQuery(q.resources);
  const free = resQ.data && resQ.data.memory.totalBytes > 0 ? resQ.data.memory.availableBytes / MB - RESERVE_MB : undefined;
  const edits = useStaged(project);
  const enamel = useEnamel(project);
  const changes = useQuery(q.changes(project));
  const lastChange = changes.data?.[0]?.at;
  const appNames = Object.entries(m.data?.manifest.apps ?? {}).map(([n]) => n);
  const deploys = useQueries({
    queries: appNames.map((a) => ({ queryKey: ["deploys", project, a, ""], queryFn: () => mod3.deploys(project, a), retry: false, refetchOnWindowFocus: false, staleTime: 15_000, refetchInterval: 10_000 })),
  });
  const brokenDeploys = appNames.filter((_, i) => deploys[i].data?.filter((d) => !d.preview)[0]?.status === "failed");

  if (p.isError) {
    return (
      <Page>
        <ProblemNote error={p.error} title={p.error instanceof ApiError && p.error.status === 404 ? `There’s no project called ${project}.` : "This project can’t be read right now."} />
      </Page>
    );
  }

  const man = m.data?.manifest;
  const apps = Object.entries(man?.apps ?? {}) as Array<[string, ManifestApp]>;
  const svc = (man?.services ?? {}) as Record<string, unknown>;
  const buckets = Object.entries(man?.services?.storage?.buckets ?? {});
  const crons = Object.entries(man?.crons ?? {});
  const queues = Object.entries(man?.queues ?? {});
  const env = Object.entries(man?.env ?? {});
  const status = p.data?.status ?? {};
  const failed = Object.values(status).filter((s) => s.state === "failed");
  const pending = Object.values(status).filter((s) => s.state === "pending");
  const setEdits = edits.filter((e): e is SetEdit => e.kind === "set");
  const stagedNew = (section: string, existing: string[]) => setEdits.filter((e) => e.path[0] === section && e.path.length === 2 && e.to !== undefined && !existing.includes(e.path[1]));
  const stagedBuckets = setEdits.filter((e) => e.path[0] === "services" && e.path[2] === "buckets" && e.to !== undefined && !buckets.some(([b]) => b === e.path[3]));
  const stagedSet = (path: string[]) => setEdits.find((e) => e.path.join("/") === path.join("/"));
  const on = SERVICES.filter((s) => s.key in svc);
  const offList = SERVICES.filter((s) => !(s.key in svc));

  const sentence =
    failed.length === 1
      ? `${failed[0].address.split("/")[1] ?? failed[0].address} is down. The rest is running.`
      : failed.length > 1
        ? `${words(failed.length, true)} parts need a look.`
        : pending.length > 0
          ? `${words(pending.length, true)} ${pending.length === 1 ? "part is" : "parts are"} starting.`
          : apps.length === 0 && on.length === 0
            ? "Nothing in it yet. Add an app or a service."
            : brokenDeploys.length === 1
              ? `${brokenDeploys[0]}’s last deploy failed. The version before it is still serving.`
              : brokenDeploys.length > 1
                ? `${words(brokenDeploys.length, true)} apps’ last deploys failed. Their earlier versions are serving.`
                : "Everything is running.";

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: "Box", to: "/" }, { label: project }]} />}
        title={
          <span className="inline-flex items-center gap-3">
            <EnamelSwatch enamel={enamel} size={14} className="rounded-[3px]" />
            {project}
          </span>
        }
        actions={<AddMenu project={project} manifest={man} routes={routes} />}
      />
      <div className="sentence mt-3 text-ink">
        {p.data ? <span className={cn(failed.length > 0 && "text-danger")}>{sentence}</span> : <Skeleton className="h-8 w-80" />}
      </div>
      <p className="mt-1.5 text-[0.9375rem] text-ink-2">
        {p.data && man ? (
          <>
            {countWords(apps.length, "app", "apps", true)}, {countWords(on.length, "service")}
            {buckets.length > 0 && `, ${countWords(buckets.length, "bucket")}`}. Version {int(p.data.version)}
            {lastChange && <>, changed {relative(lastChange)}</>}.
          </>
        ) : (
          " "
        )}
      </p>

      <div className="mt-9 grid items-start gap-x-12 gap-y-10 xl:grid-cols-[minmax(0,1fr)_288px]">
        <div className="min-w-0">
          <Rim enamel={enamel} className="mx-0" />
          {m.isError && <ProblemNote className="mt-4" error={m.error} title="The project’s config can’t be read." />}

          <Group label="Apps" count={apps.length} empty={apps.length === 0 && !stagedNew("apps", []).length ? "No apps yet. Add one from a starter or a git URL." : undefined}>
            {apps.map(([name, spec]) => (
              <AppRow
                key={name}
                project={project}
                app={name}
                spec={spec}
                free={free}
                instances={stagedFor(edits, `instances:${name}`)}
                memory={stagedSet(["apps", name, "memoryMB"])}
                fault={status[`app/${name}`]?.state === "failed" ? status[`app/${name}`].message : undefined}
              />
            ))}
            {stagedNew(
              "apps",
              apps.map(([n]) => n),
            ).map((e) => (
              <StagedRow key={e.path.join("/")} project={project} e={e} name={e.path[1]} sub={frameworkName(String((e.to as { framework?: string }).framework ?? ""))} />
            ))}
          </Group>

          <Group label="Services" count={on.length}>
            {on.map((s) => (
              <ServiceRow key={s.key} project={project} s={s} live={status[`service/${s.key}`]?.state === "failed" ? "tripped" : "on"} message={status[`service/${s.key}`]?.message} staged={stagedFor(edits, `service:${s.key}`)} />
            ))}
            {offList.map((s) => (
              <ServiceRow key={s.key} project={project} s={s} live="off" staged={stagedFor(edits, `service:${s.key}`)} />
            ))}
          </Group>

          {(buckets.length > 0 || stagedBuckets.length > 0) && (
            <Group label="Buckets" count={buckets.length}>
              {buckets.map(([b, spec]) => (
                <Row
                  key={b}
                  name={
                    <Link to="/projects/$project/storage/$bucket" params={{ project, bucket: b }} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
                      {b}
                    </Link>
                  }
                  sub={spec.public ? "Public" : "Private"}
                  status={spec.public ? "Anyone with a file’s address can read it." : "Files are served only through signed links."}
                  end={<ChevronRight className="size-4 text-ink-4" />}
                />
              ))}
              {stagedBuckets.map((e) => (
                <StagedRow key={e.path.join("/")} project={project} e={e} name={e.path[3]} sub={(e.to as { public?: boolean }).public ? "Public" : "Private"} />
              ))}
            </Group>
          )}

          {(queues.length > 0 || stagedNew("queues", []).length > 0) && (
            <Group label="Queues" count={queues.length}>
              {queues.map(([name, qq]) => (
                <ConfigRow key={name} project={project} path={["queues", name]} name={name} sub="Queue" status={<>Delivers to {qq.app} at <span className="ident text-[0.75rem]">{qq.path}</span>, up to {count(qq.maxAttempts || 8, "attempt")}.</>} value={qq} staged={stagedSet(["queues", name])} to="/projects/$project/queues" />
              ))}
              {stagedNew(
                "queues",
                queues.map(([n]) => n),
              ).map((e) => (
                <StagedRow key={e.path.join("/")} project={project} e={e} name={e.path[1]} sub="Queue" />
              ))}
            </Group>
          )}

          {(crons.length > 0 || stagedNew("crons", []).length > 0) && (
            <Group label="Schedules" count={crons.length}>
              {crons.map(([name, c]) => (
                <ConfigRow key={name} project={project} path={["crons", name]} name={name} sub="Schedule" status={<>Calls {c.app} at <span className="ident text-[0.75rem]">{c.path}</span> {cronWords(c.schedule)}.</>} value={c} staged={stagedSet(["crons", name])} to="/projects/$project/queues" />
              ))}
              {stagedNew(
                "crons",
                crons.map(([n]) => n),
              ).map((e) => (
                <StagedRow key={e.path.join("/")} project={project} e={e} name={e.path[1]} sub="Schedule" />
              ))}
            </Group>
          )}

          {(env.length > 0 || stagedNew("env", []).length > 0) && (
            <Group label="Environment" count={env.length} note="Plain settings every app reads. Secrets are separate, on the right.">
              {env.map(([k, v]) => (
                <ConfigRow key={k} project={project} path={["env", k]} name={<span className="ident text-[0.8125rem]">{k}</span>} status={<span className="ident text-[0.75rem] break-all text-ink-2">{v}</span>} value={v} staged={stagedSet(["env", k])} />
              ))}
              {stagedNew(
                "env",
                env.map(([n]) => n),
              ).map((e) => (
                <StagedRow key={e.path.join("/")} project={project} e={e} name={<span className="ident text-[0.8125rem]">{e.path[1]}</span>} sub={String(e.to)} />
              ))}
            </Group>
          )}
        </div>

        <aside className="flex min-w-0 flex-col gap-9" aria-label="About this project">
          <Colour project={project} enamel={enamel} />
          <Addresses project={project} apps={apps.filter(([, a]) => a.role !== "worker").map(([n]) => n)} />
          <SecretsLink project={project} />
          <Lately project={project} />
        </aside>
      </div>
    </Page>
  );
}

// ───────────────────────── rows ─────────────────────────

const rowGrid = "grid grid-cols-[56px_minmax(0,11rem)_minmax(0,1fr)_auto] items-center gap-x-4 max-sm:grid-cols-[48px_minmax(0,1fr)_auto] max-sm:gap-x-3";

function Group({ label, count: n, note, empty, children }: { label: string; count?: number; note?: string; empty?: string; children: ReactNode }) {
  return (
    <section className="mt-8 first-of-type:mt-6" aria-label={label}>
      <div className="mb-2 flex items-baseline gap-2">
        <h2 className="label">{label}</h2>
        {n !== undefined && n > 0 && <span className="text-xs text-ink-4 tnum">{n}</span>}
        {note && <span className="ml-auto text-xs text-ink-3 max-sm:hidden">{note}</span>}
      </div>
      <div className="divide-y divide-rule border-y border-rule">
        {children}
        {empty && <p className="py-4 pl-[72px] text-sm text-ink-3 max-sm:pl-0">{empty}</p>}
      </div>
    </section>
  );
}

function Row({
  lever,
  name,
  sub,
  status,
  end,
  staged,
  fault,
  muted,
}: {
  lever?: ReactNode;
  name: ReactNode;
  sub?: ReactNode;
  status?: ReactNode;
  end?: ReactNode;
  staged?: boolean;
  fault?: boolean;
  muted?: boolean;
}) {
  return (
    <div
      className={cn(
        rowGrid,
        "min-h-[52px] py-2 pr-1",
        staged && "bg-[color-mix(in_oklch,var(--brass)_5%,transparent)] shadow-[inset_2px_0_0_var(--brass)]",
        fault && !staged && "shadow-[inset_2px_0_0_var(--danger)]",
      )}
    >
      <div className="flex items-center justify-center max-sm:row-span-2 max-sm:self-start max-sm:pt-1">{lever}</div>
      <div className="min-w-0">
        <div className={cn("truncate text-[0.875rem] leading-[1.125rem]", muted ? "text-ink-3" : "text-ink")}>{name}</div>
        {sub && <div className="truncate text-xs leading-4 text-ink-3">{sub}</div>}
      </div>
      <div className="min-w-0 text-[0.84375rem] leading-[1.1875rem] text-ink-2 max-sm:col-span-2 max-sm:col-start-2 max-sm:row-start-2 max-sm:text-[0.8125rem]">{status}</div>
      <div className="flex items-center justify-end gap-2 max-sm:col-start-3 max-sm:row-start-1">{end}</div>
    </div>
  );
}

function AppRow({
  project,
  app,
  spec,
  free,
  instances,
  memory,
  fault,
}: {
  project: string;
  app: string;
  spec: ManifestApp;
  free?: number;
  instances?: StagedEdit;
  memory?: SetEdit;
  fault?: string;
}) {
  const applied = spec.instances ?? 1;
  const appliedMem = spec.memoryMB ?? 512;
  const nInst = instances?.kind === "instances" ? instances.to : applied;
  const nMem = memory ? Number(memory.to) : appliedMem;
  const [previewInst, setPreviewInst] = useState<number | null>(null);
  const [previewMem, setPreviewMem] = useState<number | null>(null);
  const live = useAppStatus(project, app, spec.role, spec.framework);
  const isStatic = spec.framework === "static";
  const maxInst = free === undefined ? undefined : applied + Math.max(0, Math.floor(free / appliedMem));
  const maxMem = free === undefined ? undefined : appliedMem + Math.max(0, Math.floor(free / Math.max(1, nInst)));
  const pi = previewInst ?? nInst;
  const pm = previewMem ?? nMem;
  const moving = previewInst !== null || previewMem !== null;
  const readout = moving ? (
    <span>
      <b className="font-[550] text-ink">{count(pi, "instance")}</b> of {mbWords(pm)}, up to {mbWords(pi * pm)}.{" "}
      {free !== undefined && <>Room left after {int(Math.max(0, free + RESERVE_MB - (pi * pm - applied * appliedMem)))}&#8239;MB.</>}
    </span>
  ) : null;
  const sub = isStatic ? "Static site, served by the edge" : `${frameworkName(spec.framework)}${spec.role === "worker" ? " worker" : ""} · ${int(nInst)} \u00d7 ${mbWords(nMem)}`;
  return (
    <Row
      lever={
        isStatic ? null : (
          <Throttle
            size="mini"
            label={`${app} instances`}
            stops={INSTANCE_STOPS}
            value={nInst}
            applied={applied}
            maxFit={maxInst}
            onChange={setPreviewInst}
            onCommit={(to) => {
              setPreviewInst(null);
              stage(project, { kind: "instances", app, from: applied, to });
            }}
          />
        )
      }
      name={
        <span className="inline-flex items-center gap-2">
          <Link to="/projects/$project/apps/$app" params={{ project, app }} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {app}
          </Link>
          {live.pilot && <PilotLight state={live.pilot} label={live.pilot === "busy" ? "Building" : "Deploy failed"} />}
          {(instances || memory) && <span className="label text-brass-ink">staged</span>}
        </span>
      }
      sub={sub}
      status={readout ?? (fault ? <span className="text-danger">{fault}</span> : live.sentence)}
      end={
        isStatic ? (
          <ChevronRight className="size-4 text-ink-4" />
        ) : (
          <span className="flex items-center gap-2.5" title="Memory per instance">
            <span className="w-12 text-right text-xs text-ink-3 tnum max-sm:hidden">{mbWords(pm)}</span>
            <Throttle
              size="mini"
              label={`${app} memory per instance`}
              unit="MB"
              stops={MEMORY_STOPS}
              value={nMem}
              applied={appliedMem}
              maxFit={maxMem}
              onChange={setPreviewMem}
              onCommit={(to) => {
                setPreviewMem(null);
                stage(project, {
                  kind: "set",
                  path: ["apps", app, "memoryMB"],
                  from: appliedMem,
                  to,
                  what: `Give ${app} ${mbWords(to)} per instance (now ${mbWords(appliedMem)})`,
                  undo: `${app} goes back to ${mbWords(appliedMem)} per instance`,
                });
              }}
            />
          </span>
        )
      }
      staged={!!instances || !!memory}
      fault={!!fault || live.fault}
    />
  );
}

function PostgresSentence({ project }: { project: string }) {
  return <>{usePostgresStatus(project)}</>;
}
function ValkeySentence({ project }: { project: string }) {
  return <>{useValkeyStatus(project)}</>;
}
function StorageSentence({ project }: { project: string }) {
  return <>{useStorageStatus(project).sentence}</>;
}
function EmailSentence({ project }: { project: string }) {
  return <>{useEmailStatus(project).sentence}</>;
}
function AuthSentence({ project }: { project: string }) {
  return <>{useAuthStatus(project)}</>;
}
function AnalyticsSentence({ project }: { project: string }) {
  return <>{useAnalyticsStatus(project)}</>;
}
const sentences: Record<string, (p: { project: string }) => ReactNode> = {
  postgres: PostgresSentence,
  valkey: ValkeySentence,
  storage: StorageSentence,
  email: EmailSentence,
  auth: AuthSentence,
  analytics: AnalyticsSentence,
};

function ServiceSentence({ project, service }: { project: string; service: string }) {
  const S = sentences[service];
  return S ? <S project={project} /> : null;
}

function ServiceRow({
  project,
  s,
  live,
  message,
  staged,
}: {
  project: string;
  s: { key: string; label: string; sub: string; to: string };
  live: "on" | "off" | "tripped";
  message?: string;
  staged?: StagedEdit;
}) {
  const st = staged?.kind === "service" ? staged.to : undefined;
  const off = live === "off";
  return (
    <Row
      lever={<Breaker label={s.label} state={live} staged={st} onFlip={(next) => stage(project, { kind: "service", service: s.key, from: off ? "off" : "on", to: next })} />}
      name={
        <span className="inline-flex items-center gap-2">
          {off ? (
            s.label
          ) : (
            <Link to={s.to as "/"} params={{ project } as never} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
              {s.label}
            </Link>
          )}
          {staged && <span className="label text-brass-ink">staged</span>}
        </span>
      }
      sub={s.sub}
      muted={off && !st}
      status={
        st === "on" && off ? (
          <span className="text-brass-ink">Comes into {project} when you apply.</span>
        ) : st === "off" ? (
          <span className="text-brass-ink">Comes out of {project} when you apply. The plan says what that costs.</span>
        ) : live === "tripped" ? (
          <span className="text-danger">Tripped{message ? `: ${message}` : ""}.</span>
        ) : off ? (
          <span className="text-ink-3">Not in {project}. Flip it on to add it.</span>
        ) : (
          <ServiceSentence project={project} service={s.key} />
        )
      }
      end={off ? null : <ChevronRight className="size-4 text-ink-4" />}
      staged={!!staged}
      fault={live === "tripped"}
    />
  );
}

/** A queue, schedule or env var: its words, and a remove that stages (and can be taken back). */
function ConfigRow({
  project,
  path,
  name,
  sub,
  status,
  value,
  staged,
  to,
}: {
  project: string;
  path: string[];
  name: ReactNode;
  sub?: ReactNode;
  status: ReactNode;
  value: unknown;
  staged?: SetEdit;
  to?: string;
}) {
  const removing = staged && staged.to === undefined;
  const label = path[0] === "env" ? path[1] : `${path[0] === "crons" ? "the schedule" : "the queue"} ${path[1]}`;
  return (
    <Row
      name={to && !removing ? <Link to={to as "/"} params={{ project } as never} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">{name}</Link> : name}
      sub={removing ? <span className="text-brass-ink">Removed when you apply</span> : sub}
      status={<span className={cn(removing && "line-through decoration-ink-4")}>{status}</span>}
      staged={!!staged}
      end={
        removing ? (
          <Button variant="ghost" size="sm" onClick={() => stage(project, { ...staged, to: staged.from })}>
            <Undo2 /> Keep
          </Button>
        ) : (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove ${label}`}
            title={`Remove ${label}`}
            className="text-ink-3 hover:text-ink"
            onClick={() => stage(project, { kind: "set", path, from: value, to: undefined, what: `Remove ${label} from ${project}`, undo: `${label} comes back as it was` })}
          >
            <Trash2 />
          </Button>
        )
      }
    />
  );
}

/** Something added through Add and not applied yet. */
function StagedRow({ project, e, name, sub }: { project: string; e: SetEdit; name: ReactNode; sub?: ReactNode }) {
  return (
    <Row
      lever={<span className="size-1.5 rounded-full bg-brass" aria-hidden />}
      name={
        <span className="inline-flex items-center gap-2">
          {name} <span className="label text-brass-ink">staged</span>
        </span>
      }
      sub={sub}
      status={<span className="text-brass-ink">{e.what}. Comes in when you apply.</span>}
      staged
      end={
        <Button variant="ghost" size="sm" onClick={() => stage(project, { ...e, to: e.from })}>
          Unstage
        </Button>
      }
    />
  );
}

// ───────────────────────── the rail ─────────────────────────

function Colour({ project, enamel }: { project: string; enamel: Enamel }) {
  const qc = useQueryClient();
  const { can } = useMe();
  const set = useMutation({
    mutationFn: (e: Enamel) => api.setAppearance(project, e),
    onSuccess: (_r, e, ctx) => {
      void qc.invalidateQueries({ queryKey: appearanceQuery(project).queryKey });
      const was = (ctx as { was?: Enamel } | undefined)?.was;
      toast({
        title: <>{project} is {enamelNames[e].toLowerCase()} now.</>,
        action: was && was !== e ? { label: "Undo", run: () => api.setAppearance(project, was).then(() => qc.invalidateQueries({ queryKey: appearanceQuery(project).queryKey })) } : undefined,
      });
    },
    onMutate: () => ({ was: enamel }),
    onError: (e) => toast({ title: "The colour didn’t change.", detail: e instanceof ApiError ? e.problem.detail : undefined, tone: "danger" }),
  });
  return (
    <section aria-label="Colour">
      <h2 className="label mb-2.5">Colour</h2>
      {can("apply:reversible") ? (
        <EnamelPicker value={set.isPending && set.variables ? set.variables : enamel} onChange={(e) => e !== enamel && set.mutate(e)} size={20} />
      ) : (
        <span className="inline-flex items-center gap-2 text-sm text-ink-2">
          <EnamelSwatch enamel={enamel} size={12} /> {enamelNames[enamel]}
        </span>
      )}
      <p className="mt-2 text-xs text-ink-3">Its rim in the Box, its swatch everywhere else. Only you see it; it isn’t part of the config.</p>
    </section>
  );
}

function Addresses({ project, apps }: { project: string; apps: string[] }) {
  const rts = useQueries({ queries: apps.map((a) => ({ queryKey: ["runtime", project, a], queryFn: () => mod3.runtime(project, a), retry: false, staleTime: 60_000 })) });
  if (apps.length === 0) return null;
  const urls = rts.map((r, i) => ({ app: apps[i], url: r.data?.production?.url, err: r.error }));
  if (urls.every((u) => notOnBox(u.err))) return null;
  return (
    <section aria-label="Addresses">
      <h2 className="label mb-1.5">Addresses</h2>
      <ul className="divide-y divide-rule">
        {urls.map((u) => (
          <li key={u.app} className="flex items-baseline justify-between gap-3 py-2">
            {u.url ? (
              <a href={u.url} target="_blank" rel="noopener noreferrer" className="group ident flex min-w-0 items-center gap-1 text-[0.75rem] text-ink hover:text-brass-ink">
                <span className="truncate">{u.url.replace(/^https?:\/\//, "")}</span>
                <ArrowUpRight className="size-3 shrink-0 text-ink-3 group-hover:text-brass-ink" />
              </a>
            ) : (
              <span className="text-xs text-ink-3">not live yet</span>
            )}
            <span className="shrink-0 text-xs text-ink-3">{u.app}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function SecretsLink({ project }: { project: string }) {
  const s = useQuery({ ...q.secrets(project), retry: false });
  if (notOnBox(s.error)) return null;
  const n = s.data?.length;
  return (
    <section aria-label="Secrets">
      <h2 className="label mb-1.5">Secrets</h2>
      <Link to="/projects/$project/secrets" params={{ project }} className="group flex items-center justify-between gap-3 border-y border-rule py-2.5">
        <span className="min-w-0">
          <span className="block text-[0.875rem] text-ink group-hover:underline group-hover:decoration-rule-3 group-hover:underline-offset-4">
            {n === undefined ? "Secrets" : n === 0 ? "None set" : countWords(n, "secret", "secrets", true)}
          </span>
          <span className="block text-xs text-ink-3">Keys and passwords apps read as env vars. Never shown back.</span>
        </span>
        <ChevronRight className="size-4 shrink-0 text-ink-4" />
      </Link>
    </section>
  );
}

function Lately({ project }: { project: string }) {
  const changes = useQuery(q.changes(project));
  const list = (changes.data ?? []).slice(0, 5);
  return (
    <section aria-label={`Latest in ${project}`}>
      <div className="flex items-baseline justify-between">
        <h2 className="label">Ledger</h2>
        <Link to="/ledger" search={{ project }} className="text-[0.8125rem] font-[550] text-brass-ink hover:underline hover:underline-offset-4">
          All of {project}
        </Link>
      </div>
      {changes.isPending ? (
        <Skeleton className="mt-3 h-32" />
      ) : list.length === 0 ? (
        <p className="mt-2 text-sm text-ink-3">Nothing has changed in {project} yet.</p>
      ) : (
        <div className="mt-1 divide-y divide-rule">
          {list.map((c) => {
            const day = dayLabel(c.at);
            return (
              <SignedEntry
                key={c.id}
                time={day === "Today" ? clock(c.at) : day.split(",")[0].slice(0, 3)}
                actor={{ kind: c.actor.kind, name: c.actor.name || c.actor.id, session: c.actor.session }}
                intent={intentWords(c)}
                to="/changes/$id"
                params={{ id: c.id }}
                counts={opCounts(c.plan.ops)}
                tier={asTier(c.plan.risk)}
                muted={!!c.undoneBy}
                signature={c.undoneBy ? "undone" : c.actor.kind === "agent" ? "within its grant" : undefined}
              />
            );
          })}
        </div>
      )}
    </section>
  );
}

// ───────────────────────── secrets ─────────────────────────

export function SecretsPage({ project }: { project: string }) {
  useTitle(`${project} · Secrets`);
  const qc = useQueryClient();
  const secrets = useQuery(q.secrets(project));
  const names = useQuery({ ...q.tokenNames, retry: false });
  const enamel = useEnamel(project);
  const { can } = useMe();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [deleting, setDeleting] = useState<SecretInfo | null>(null);
  const valid = /^[A-Z_][A-Z0-9_]{0,127}$/.test(name);
  const writer = can("apply:reversible");

  const save = useMutation({
    mutationFn: () => api.setSecret(project, name, value),
    onSuccess: () => {
      const n = name;
      const replaced = (secrets.data ?? []).some((s) => s.name === n);
      setName("");
      setValue("");
      void qc.invalidateQueries({ queryKey: ["secrets", project] });
      toast({ title: <>{replaced ? "Replaced" : "Saved"} {n}.</>, detail: `Apps in ${project} restart with it, one instance at a time.` });
    },
  });

  if (secrets.isError && notOnBox(secrets.error)) return <NotOnBox what="Secrets" />;
  const list = secrets.data ?? [];
  const replacing = list.some((s) => s.name === name);

  return (
    <Page>
      <PageHeader
        eyebrow={
          <Crumbs
            items={[
              {
                label: (
                  <span className="inline-flex items-center gap-1.5">
                    <EnamelSwatch enamel={enamel} size={7} />
                    {project}
                  </span>
                ),
                to: "/projects/$project",
                params: { project },
              },
              { label: "Secrets" },
            ]}
          />
        }
        title="Secrets"
        lede={
          <>
            Apps in {project} read these as environment variables. Values are write-only: once saved nobody can read them here, you included. Saving or removing one restarts the apps.
          </>
        }
      />

      {secrets.isError && <ProblemNote className="mt-8" error={secrets.error} />}

      <div className="mt-9 mb-2 flex items-baseline gap-2">
        <h2 className="label">Saved</h2>
        {list.length > 0 && <span className="text-xs text-ink-4 tnum">{list.length}</span>}
      </div>
      <ul className="divide-y divide-rule border-y border-rule">
        {secrets.isPending && <Skeleton className="my-3 h-10" />}
        {list.length === 0 && secrets.isSuccess && <li className="py-5 text-sm text-ink-3">No secrets yet. Add the first one below.</li>}
        {list.map((s) => (
          <li key={s.name} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 py-2.5 sm:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_auto]">
            <code className="ident truncate text-[0.8125rem] text-ink">{s.name}</code>
            <span className="text-[0.8125rem] text-ink-3 max-sm:col-start-1 max-sm:row-start-2">
              set {relative(s.updatedAt)} by {names.data?.get(s.updatedBy)?.name ?? "someone"}
            </span>
            {writer && (
              <span className="flex gap-1 max-sm:row-span-2">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setName(s.name);
                    setValue("");
                    document.getElementById("s-value")?.focus();
                  }}
                >
                  Replace
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`Delete ${s.name}`} onClick={() => setDeleting(s)} className="text-ink-3 hover:text-danger">
                  <Trash2 />
                </Button>
              </span>
            )}
          </li>
        ))}
      </ul>

      {writer && (
        <form
          className="mt-10"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid && value) save.mutate();
          }}
        >
          <h2 className="label mb-3">{replacing ? `Replace ${name}` : "Add a secret"}</h2>
          <div className="grid gap-3 sm:grid-cols-[15rem_minmax(0,1fr)_auto] sm:items-end">
            <label className="block">
              <span className="mb-1 block text-xs font-[550] text-ink-2">Name</span>
              <input
                value={name}
                onChange={(e) => setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
                placeholder="STRIPE_SECRET_KEY"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={name !== "" && !valid}
                className="ident h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger"
              />
            </label>
            <label className="block">
              <span className="mb-1 block text-xs font-[550] text-ink-2">Value</span>
              <input
                id="s-value"
                type="password"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                placeholder="Paste it here"
                autoComplete="new-password"
                spellCheck={false}
                className="ident h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]"
              />
            </label>
            <Button type="submit" variant="primary" size="lg" disabled={!valid || !value || save.isPending}>
              {save.isPending ? "Saving…" : replacing ? `Replace ${name}` : name && valid ? `Save ${name}` : "Save secret"}
            </Button>
          </div>
          {save.isError && <ProblemNote className="mt-4" error={save.error} />}
          <p className="mt-2.5 text-xs text-ink-3">
            {replacing ? "The old value is replaced and can’t be recovered." : "Stored encrypted with the box’s own key. It never leaves the box, and this page never shows it again."}
          </p>
        </form>
      )}

      <Confirm
        open={!!deleting}
        onClose={() => setDeleting(null)}
        title={`Delete ${deleting?.name ?? "this secret"}?`}
        body={`Apps in ${project} restart without it. The value can’t be recovered.`}
        action={`Delete ${deleting?.name ?? "secret"}`}
        run={() => api.deleteSecret(project, deleting!.name)}
        done={() => {
          void qc.invalidateQueries({ queryKey: ["secrets", project] });
          toast({ title: <>Deleted {deleting?.name}.</>, detail: `Apps in ${project} restart without it.` });
        }}
      />
    </Page>
  );
}

export type { Manifest };
