import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, ChevronRight, Trash2 } from "lucide-react";
import { type ReactNode } from "react";
import { notOnBox, type ManifestApp } from "@/api/client";
import { mod3 } from "@/api/modules";
import { q } from "@/api/queries";
import { Breaker } from "@/components/breaker";
import { PilotLight } from "@/components/pilot";
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
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { count, countWords, dec, int, withUnit } from "@/lib/format";
import { change, type StagedEdit } from "@/lib/staged";
import { frameworkName } from "@/lib/starters";
import { PARTS } from "@/lib/names";
import type { ProjectPage } from "@/lib/sections";

// The rows a project's deeper tabs are made of: an app with its instances
// throttle, a service with its breaker, a config entry with remove, and a
// part added but not applied yet. Moved from the old project page; the
// Usage tab's Advanced and the Settings tab use them.

export const RESERVE_MB = 512; // kept free for spikes, as the Box and the tray do
/** Memory per instance an app can snap to. */
export const MEMORY_STOPS = [256, 512, 1024, 2048, 4096];
export const mbWords = (n: number) => (n >= 1024 ? withUnit(dec(n / 1024, 1), "GB") : withUnit(int(n), "MB"));

export const SERVICES: Array<{ key: string; label: string; sub: string; to: ProjectPage }> = [
  { key: "postgres", label: PARTS.postgres.name, sub: PARTS.postgres.sub, to: "/projects/$project/data" },
  { key: "storage", label: PARTS.storage.name, sub: PARTS.storage.sub, to: "/projects/$project/storage" },
  { key: "auth", label: PARTS.auth.name, sub: PARTS.auth.sub, to: "/projects/$project/users" },
  { key: "email", label: PARTS.email.name, sub: PARTS.email.sub, to: "/projects/$project/email" },
  { key: "analytics", label: PARTS.analytics.name, sub: PARTS.analytics.sub, to: "/projects/$project/analytics" },
  { key: "valkey", label: PARTS.valkey.name, sub: PARTS.valkey.sub, to: "/projects/$project/data/kv" },
];

export type SetEdit = Extract<StagedEdit, { kind: "set" }>;


const rowGrid = "grid grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] items-center gap-x-5 max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3";

export function Group({ label, count: n, note, empty, children }: { label: string; count?: number; note?: string; empty?: string; children: ReactNode }) {
  return (
    <section className="mt-8 first-of-type:mt-6" aria-label={label}>
      <div className="mb-2 flex items-baseline gap-2">
        <h2 className="label">{label}</h2>
        {n !== undefined && n > 0 && <span className="text-xs text-ink-4 tnum">{n}</span>}
        {note && <span className="ml-auto text-xs text-ink-3 max-sm:hidden">{note}</span>}
      </div>
      <div className="divide-y divide-rule border-y border-rule">
        {children}
        {empty && <p className="py-4 text-sm text-ink-3">{empty}</p>}
      </div>
    </section>
  );
}

/** One row: name and kind, a status line, and its control on the right. */
export function Row({
  lever,
  name,
  sub,
  status,
  end,
  busy,
  fault,
  muted,
}: {
  lever?: ReactNode;
  name: ReactNode;
  sub?: ReactNode;
  status?: ReactNode;
  end?: ReactNode;
  /** A change on its way: the row says so in its status. */
  busy?: boolean;
  fault?: boolean;
  muted?: boolean;
}) {
  return (
    <div className={cn(rowGrid, "min-h-[56px] py-2.5 pr-1", fault && "shadow-[inset_2px_0_0_var(--danger)] pl-3", busy && "opacity-90")}>
      <div className="min-w-0">
        <div className={cn("truncate text-[0.875rem] leading-[1.125rem]", muted ? "text-ink-3" : "text-ink")}>{name}</div>
        {sub && <div className="truncate text-xs leading-4 text-ink-3">{sub}</div>}
      </div>
      <div className="min-w-0 text-[0.84375rem] leading-[1.1875rem] text-ink-2 max-sm:col-span-2 max-sm:row-start-2 max-sm:mt-1 max-sm:text-[0.8125rem]">{status}</div>
      <div className="flex items-center justify-end gap-2 max-sm:col-start-2 max-sm:row-start-1">
        {lever}
        {end}
      </div>
    </div>
  );
}

/** "Adding a database…" with a spinner: a change on its way, where it happens. */
export function Working({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-2 text-brass-ink">
      <span className="spinner" aria-hidden />
      {children}
    </span>
  );
}

export function AppRow({
  project,
  app,
  spec,
  free,
  instances,
  fault,
}: {
  project: string;
  app: string;
  spec: ManifestApp;
  free?: number;
  instances?: StagedEdit;
  fault?: string;
}) {
  const applied = spec.instances ?? 1;
  // No per-copy cap by default: copies share the project's memory, so only a capped app has a "won't fit".
  const mem = spec.memoryMB;
  const n = instances?.kind === "instances" ? instances.to : applied;
  const live = useAppStatus(project, app, spec.role, spec.framework);
  const isStatic = spec.framework === "static";
  const maxInst = free === undefined || !mem ? undefined : applied + Math.max(0, Math.floor(free / mem));
  const sub = isStatic ? "Static site, served by the edge" : `${frameworkName(spec.framework)}${spec.role === "worker" ? " worker" : ""}${mem ? ` · up to ${mbWords(mem)} each` : ""}`;
  return (
    <Row
      lever={
        isStatic ? null : (
          <Throttle
            size="mini"
            label={`${app} copies`}
            stops={INSTANCE_STOPS}
            value={n}
            applied={applied}
            maxFit={maxInst}
            onCommit={(to) => change(project, { kind: "instances", app, from: applied, to })}
          />
        )
      }
      name={
        <span className="inline-flex items-center gap-2">
          <Link to="/projects/$project/apps/$app" params={{ project, app }} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {app}
          </Link>
          {live.pilot === "busy" && <PilotLight state="busy" label="Building" />}
        </span>
      }
      sub={sub}
      status={
        instances ? (
          <Working>
            {n > applied ? "Starting" : "Stopping"} {n > applied ? count(n - applied, "copy", "copies") : count(applied - n, "copy", "copies")}…
          </Working>
        ) : fault ? (
          <span className="text-danger">{fault}</span>
        ) : isStatic ? (
          live.sentence
        ) : (
          <>
            {count(applied, "copy", "copies")} running. {live.sentence}
          </>
        )
      }
      end={
        <Link
          to="/projects/$project/apps/$app"
          params={{ project, app }}
          aria-label={`Open ${app}`}
          className="flex items-center text-ink-4 hover:text-ink"
        >
          <ChevronRight className="size-4" />
        </Link>
      }
      busy={!!instances}
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

export function ServiceRow({
  project,
  s,
  live,
  message,
  staged,
}: {
  project: string;
  s: { key: string; label: string; sub: string; to: ProjectPage };
  live: "on" | "off" | "tripped";
  message?: string;
  staged?: StagedEdit;
}) {
  const st = staged?.kind === "service" ? staged.to : undefined;
  const off = live === "off";
  return (
    <Row
      lever={<Breaker label={s.label} state={live} staged={st} onFlip={(next) => change(project, { kind: "service", service: s.key, from: off ? "off" : "on", to: next }, { immediate: true })} />}
      name={
        off ? (
          s.label
        ) : (
          <Link to={s.to} params={{ project }} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">
            {s.label}
          </Link>
        )
      }
      sub={s.sub}
      muted={off && !st}
      status={
        st === "on" && off ? (
          <Working>Adding it to {project}…</Working>
        ) : st === "off" && !off ? (
          <Working>Removing it…</Working>
        ) : live === "tripped" ? (
          <span className="text-danger">Stopped unexpectedly{message ? `: ${message}` : ""}.</span>
        ) : off ? (
          <span className="text-ink-3">Not in {project}. Turn it on to add it.</span>
        ) : (
          <ServiceSentence project={project} service={s.key} />
        )
      }
      busy={!!staged}
      fault={live === "tripped"}
    />
  );
}

/** A queue, schedule or setting: its words, and remove (it can be undone from the toast). */
export function ConfigRow({
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
  to?: ProjectPage;
}) {
  const removing = staged && staged.to === undefined;
  const label = path[0] === "env" ? path[1] : `${path[0] === "crons" ? "the schedule" : "the queue"} ${path[1]}`;
  return (
    <Row
      name={to && !removing ? <Link to={to} params={{ project }} className="hover:underline hover:decoration-rule-3 hover:underline-offset-4">{name}</Link> : name}
      sub={sub}
      status={removing ? <Working>Removing…</Working> : status}
      busy={!!staged}
      end={
        !removing && (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove ${label}`}
            title={`Remove ${label}`}
            className="text-ink-3 hover:text-ink"
            onClick={() => change(project, { kind: "set", path, from: value, to: undefined, what: `Remove ${label} from ${project}`, undo: `${label} comes back as it was` }, { immediate: true })}
          >
            <Trash2 />
          </Button>
        )
      }
    />
  );
}

/** Something being added right now (an app, a bucket, a setting). */
export function StagedRow({ e, name, sub }: { project?: string; e: SetEdit; name: ReactNode; sub?: ReactNode }) {
  return <Row name={name} sub={sub} status={<Working>{e.what.replace(/^Add/, "Adding").replace(/^Set/, "Setting")}…</Working>} busy />;
}

// ───────────────────────── colour, addresses, secrets ─────────────────────────

export function Addresses({ project, apps }: { project: string; apps: string[] }) {
  const rts = useQueries({ queries: apps.map((a) => ({ queryKey: ["runtime", project, a], queryFn: () => mod3.runtime(project, a), retry: false, staleTime: 60_000 })) });
  if (apps.length === 0) return null;
  const urls = rts.map((r, i) => ({ app: apps[i], url: r.data?.production?.url, err: r.error }));
  if (urls.every((u) => notOnBox(u.err))) return null;
  return (
    <section aria-label="Addresses">
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

export function SecretsLink({ project }: { project: string }) {
  const s = useQuery({ ...q.secrets(project), retry: false });
  if (notOnBox(s.error)) return null;
  const n = s.data?.length;
  return (
    <section aria-label="Secrets">
      <h2 className="label mb-1.5">Secrets</h2>
      <Link to="/projects/$project/env" params={{ project }} className="group flex items-center justify-between gap-3 border-y border-rule py-2.5">
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

