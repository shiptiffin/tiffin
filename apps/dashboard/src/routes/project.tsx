import { useQueries, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AppWindow, ArrowUpRight, BarChart3, Clock, Database, FolderOpen, Mail, Plus, UserRound, Zap } from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { ApiError, type Manifest } from "@/api/client";
import { mod, mod2, mod3, mq } from "@/api/modules";
import { q } from "@/api/queries";
import { useTitle } from "@/components/favicon";
import { Page, Skeleton } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { ReadOnlyBanner } from "@/components/read-only";
import { AddMenu } from "@/components/start-add-menu";
import { Button } from "@/components/ui/button";
import { Mascot, type MascotState } from "@/components/mascot";
import { addressesOf } from "@/lib/addresses";
import { cn } from "@/lib/cn";
import { useEnamel } from "@/lib/enamel";
import { bytes, count, cronWords, int } from "@/lib/format";
import { appPulse, deploysQuery, runtimeQuery, toneClass, useProjectPulse } from "@/lib/pulse";
import { useArrival } from "@/lib/switch";
import { progressWords, usePending } from "@/lib/staged";
import { frameworkName } from "@/lib/starters";
import { PARTS } from "@/lib/names";
import { relative, sinceWhen } from "@/lib/time";
import { ProjectIcon } from "@/components/project-icon";
import { DomainsSummary } from "@/components/project-domains";

/**
 * A project's overview: its name, live address and one status line, then
 * what's in it as tiles (apps, database, files, email, sign-in, analytics,
 * jobs; only the ones it has) and a quiet Add. Each tile opens its own page;
 * the levers (instances, memory, limits) live in Usage, the config in Settings.
 */
export function ProjectPage({ project }: { project: string }) {
  useTitle(project);
  const arrived = useArrival(project);
  const p = useQuery(q.project(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000, refetchInterval: 10_000 });
  const pulse = useProjectPulse(project);
  const enamel = useEnamel(project);
  const staged = usePending(project);
  const projects = useQuery(q.projects);
  const others = (projects.data ?? []).map((x) => x.name).filter((n) => n !== project);
  const otherManifests = useQueries({ queries: others.map((n) => ({ ...q.manifest(n), staleTime: 60_000 })) });
  const routes = useMemo(
    () => otherManifests.flatMap((x) => (x.data ? addressesOf(x.data.project, x.data.manifest.apps) : [])),
    [otherManifests],
  );

  if (p.isError) {
    return (
      <Page>
        <ProblemNote error={p.error} title={p.error instanceof ApiError && p.error.status === 404 ? `There’s no project called ${project}.` : "This project can’t be read right now."} />
      </Page>
    );
  }

  const man = m.data?.manifest;
  const res = p.data?.resources ?? [];
  const apps = res.filter((r) => r.address.startsWith("app/")).map((r) => ({ name: r.address.slice(4), spec: r.spec as { role?: string; framework?: string } }));
  const has = (a: string) => res.some((r) => r.address === a);
  const crons = res.filter((r) => r.address.startsWith("cron/"));
  const queues = res.filter((r) => r.address.startsWith("queue/"));
  const empty = p.data && res.filter((r) => r.address !== "project").length === 0;

  return (
    <Page wide>
      <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex min-w-0 items-center gap-4">
          {/* The project's own mascot: its colour on the middle tier, its state in the face (steam, packing, a spill, asleep). */}
          <Mascot state={pulse.loading ? "base" : mood[pulse.tone]} enamel={enamel} size={64} className="-my-2 -ml-1 max-sm:hidden" />
          <div className="min-w-0">
            <h1 className="title flex items-center gap-3 text-ink">
              <ProjectIcon project={project} size={14} />
              {project}
            </h1>
            <div className="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[0.9375rem] text-ink-2">
              {pulse.loading ? (
                <Skeleton className="h-5 w-56" />
              ) : (
                <>
                  {pulse.tone === "busy" ? <PilotLight state="busy" /> : <span className={cn("size-2 rounded-full", toneClass[pulse.tone])} aria-hidden />}
                  <span className={cn(pulse.tone === "bad" && "text-danger")}>{pulse.words}</span>
                  {pulse.why && (
                    <Link to="/projects/$project/apps/$app" params={{ project, app: pulse.why.app }} className="font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                      See why
                    </Link>
                  )}
                  {pulse.retry && (
                    <button type="button" onClick={pulse.retry} className="font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                      Retry
                    </button>
                  )}
                  {pulse.wake && (
                    <button type="button" onClick={pulse.wake} className="font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
                      Wake
                    </button>
                  )}
                </>
              )}
            </div>
          </div>
        </div>
        {pulse.url && (
          <Button asChild variant="secondary" size="lg" className="self-start">
            <a href={pulse.url} target="_blank" rel="noopener noreferrer">
              {pulse.url.replace(/^https?:\/\//, "").replace(/:\d+$/, "")}
              <ArrowUpRight className="text-ink-3" />
            </a>
          </Button>
        )}
      </header>
      <ReadOnlyBanner project={project} className="mt-6" />
      {arrived && (
        <p role="status" className="mt-5 text-[0.9375rem] text-ink-3">
          {project} doesn’t have {arrived}, so you’re on its Overview.
        </p>
      )}

      <h2 className="label mt-10 mb-3">What’s in it</h2>
      {m.isError && <ProblemNote className="mb-4" error={m.error} title="The project’s config can’t be read." />}
      <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-label={`What’s in ${project}`}>
        {!p.data &&
          [0, 1, 2].map((i) => (
            <li key={i}>
              <Skeleton className="h-[148px] rounded-[12px]" />
            </li>
          ))}
        {apps.map((a) => (
          <AppTile key={a.name} project={project} app={a.name} role={a.spec?.role} framework={a.spec?.framework} />
        ))}
        {has("service/postgres") && <DatabaseTile project={project} />}
        {has("service/valkey") && <CacheTile project={project} />}
        {has("service/storage") && <FilesTile project={project} />}
        {has("service/email") && <EmailTile project={project} />}
        {has("service/auth") && <SignInTile project={project} />}
        {has("service/analytics") && <AnalyticsTile project={project} />}
        {(crons.length > 0 || queues.length > 0) && <JobsTile project={project} crons={crons.map((c) => c.spec as { schedule?: string })} queues={queues.length} />}
        {staged.map((e) =>
          e.kind === "service" && e.to === "on" && !has(`service/${e.service}`) ? (
            <PendingTile key={e.service} words={progressWords(e, project)} />
          ) : e.kind === "set" && e.path[0] === "apps" && e.path.length === 2 && e.to !== undefined && !apps.some((a) => a.name === e.path[1]) ? (
            <PendingTile key={e.path.join("/")} words={`Adding the ${e.path[1]} app…`} />
          ) : null,
        )}
        {p.data && <AddTile project={project} manifest={man} routes={routes} empty={!!empty} />}
      </ul>
      {apps.some((a) => a.spec?.role !== "worker") && <DomainsSummary project={project} className="mt-10 max-w-[44rem]" />}
    </Page>
  );
}

/** The mascot's face for a project's status: live, packing, a spill, asleep (not live yet), or plain when it can't tell. */
const mood: Record<ReturnType<typeof useProjectPulse>["tone"], MascotState> = { ok: "live", busy: "deploying", bad: "failed", quiet: "idle", unknown: "base" };

// ───────────────────────── tiles ─────────────────────────

const quiet = { retry: false, refetchOnWindowFocus: false, staleTime: 15_000 } as const;

function Tile({
  icon,
  title,
  kind,
  to,
  params,
  fact,
  tone,
  actions,
}: {
  icon: ReactNode;
  title: string;
  kind?: string;
  to: string;
  params: Record<string, string>;
  fact: ReactNode;
  tone?: "bad" | "busy";
  actions?: ReactNode;
}) {
  return (
    <li
      className={cn(
        "group relative flex flex-col rounded-[12px] border bg-paper-raised p-4 pb-3 sm:min-h-[148px] shadow-[var(--top-light)] transition-[border-color,box-shadow] duration-[var(--dur-state)] hover:shadow-raised",
        tone === "bad" ? "border-danger-rule" : "border-rule-2 hover:border-rule-3",
      )}
    >
      <div className="flex items-center gap-2.5">
        <span className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-paper-sunk text-ink-2 [&_svg]:size-[17px]">{icon}</span>
        <div className="min-w-0">
          <h3 className="truncate text-[0.9375rem] leading-5 font-[550] text-ink">
            <Link to={to as "/"} params={params as never} className="outline-none after:absolute after:inset-0 after:rounded-[12px] focus-visible:after:shadow-[0_0_0_2px_var(--brass)]">
              {title}
            </Link>
          </h3>
          {kind && <p className="truncate text-xs text-ink-3">{kind}</p>}
        </div>
      </div>
      <div className={cn("mt-3 text-[0.875rem] leading-5 text-ink-2 sm:min-h-10", tone === "bad" && "text-danger")}>{fact}</div>
      {actions && <div className="relative z-10 mt-auto flex flex-wrap items-center gap-1 pt-2 -ml-2">{actions}</div>}
    </li>
  );
}

function TileAction({ children, ...rest }: { children: ReactNode } & ({ href: string } | { to: string; params: Record<string, string>; search?: Record<string, unknown> })) {
  const cls = "inline-flex h-7 items-center gap-1 rounded-[6px] px-2 text-[0.8125rem] font-[550] text-ink-2 transition-colors hover:bg-paper-sunk hover:text-ink [&_svg]:size-3.5";
  if ("href" in rest)
    return (
      <a href={rest.href} target="_blank" rel="noopener noreferrer" className={cls}>
        {children}
      </a>
    );
  return (
    <Link to={rest.to as "/"} params={rest.params as never} search={rest.search as never} className={cls}>
      {children}
    </Link>
  );
}

function AppTile({ project, app, role, framework }: { project: string; app: string; role?: string; framework?: string }) {
  const d = useQuery(deploysQuery(project, app));
  const rt = useQuery(runtimeQuery(project, app));
  const pulse = appPulse(d.data, role);
  const url = rt.data?.production?.url;
  const kind = framework === "static" ? "Website, static" : role === "worker" ? `Background worker · ${frameworkName(framework)}` : `Web app · ${frameworkName(framework)}`;
  let fact: ReactNode = <Skeleton className="h-4 w-40" />;
  if (d.isError) fact = <span className="text-ink-3">Its versions can’t be read here.</span>;
  else if (pulse) {
    if (pulse.tone === "bad")
      fact = pulse.servingOld ? (
        <>
          The new version didn’t start{pulse.failedWhy ? `: ${pulse.failedWhy}` : ""}. <span className="text-ink-2">The one before it is still up.</span>
        </>
      ) : (
        <>It didn’t start{pulse.failedWhy ? `: ${pulse.failedWhy}` : ""}.</>
      );
    else if (pulse.tone === "busy") fact = <span className="text-brass-ink">{pulse.words} a new version…</span>;
    else if (pulse.tone === "quiet") fact = "Not live yet. Deploy it to put it online.";
    else if (rt.data?.production?.sleeping) {
      const since = rt.data.production.sleepingSince;
      fact = `Asleep${since ? ` since ${sinceWhen(since)}` : ""} · wakes on ${role === "worker" ? "its next job" : "the next visit"}`;
    } else fact = `${pulse.words} · updated ${relative(pulse.since!)}`;
  }
  return (
    <Tile
      icon={<AppWindow />}
      title={app}
      kind={kind}
      to="/projects/$project/apps/$app"
      params={{ project, app }}
      fact={fact}
      tone={pulse?.tone === "bad" ? "bad" : pulse?.tone === "busy" ? "busy" : undefined}
      actions={
        <>
          {url && (
            <TileAction href={url}>
              Open <ArrowUpRight />
            </TileAction>
          )}
          <TileAction to="/projects/$project/apps/$app" params={{ project, app }} search={{ deploy: true }}>
            Deploy
          </TileAction>
          <TileAction to="/projects/$project/apps/$app/logs" params={{ project, app }}>
            Logs
          </TileAction>
        </>
      }
    />
  );
}

function DatabaseTile({ project }: { project: string }) {
  const tables = useQuery({ ...mq.tables(project), ...quiet });
  const pg = useQuery({ ...mq.pg(project), ...quiet });
  const n = tables.data?.length;
  return (
    <Tile
      icon={<Database />}
      title={PARTS.postgres.name}
      kind={PARTS.postgres.sub}
      to="/projects/$project/data"
      params={{ project }}
      fact={
        tables.isError ? (
          <span className="text-ink-3">The database isn’t answering.</span>
        ) : n === undefined ? (
          <Skeleton className="h-4 w-32" />
        ) : n === 0 ? (
          "No tables yet."
        ) : (
          <>
            {count(n, "table")}
            {pg.data && <> · {bytes(pg.data.sizeBytes)}</>}
          </>
        )
      }
      actions={
        <>
          <TileAction to="/projects/$project/data" params={{ project }}>
            Tables
          </TileAction>
          <TileAction to="/projects/$project/data/sql" params={{ project }}>
            SQL
          </TileAction>
        </>
      }
    />
  );
}

function CacheTile({ project }: { project: string }) {
  const kv = useQuery({ queryKey: ["kv-stats", project], queryFn: () => mod.kvStats(project), ...quiet });
  return (
    <Tile
      icon={<Zap />}
      title={PARTS.valkey.name}
      kind={PARTS.valkey.sub}
      to="/projects/$project/data/kv"
      params={{ project }}
      fact={kv.isError ? <span className="text-ink-3">The KV store isn’t answering.</span> : kv.data ? `${count(kv.data.keys, "key")} · ${bytes(kv.data.memoryBytes)}` : <Skeleton className="h-4 w-28" />}
    />
  );
}

function FilesTile({ project }: { project: string }) {
  const st = useQuery({ ...mq.storage(project), ...quiet });
  const b = st.data?.buckets ?? [];
  const files = b.reduce((s, x) => s + (x.objects ?? 0), 0);
  return (
    <Tile
      icon={<FolderOpen />}
      title={PARTS.storage.name}
      kind={st.data ? `${PARTS.storage.sub} · ${count(b.length, "bucket")}` : PARTS.storage.sub}
      to="/projects/$project/storage"
      params={{ project }}
      fact={st.isError ? <span className="text-ink-3">Files aren’t answering.</span> : st.data ? `${count(files, "file")} · ${bytes(st.data.filesBytes)}` : <Skeleton className="h-4 w-28" />}
    />
  );
}

function EmailTile({ project }: { project: string }) {
  const status = useQuery({ ...mq.emailStatus, ...quiet });
  const msgs = useQuery({ queryKey: ["messages", project, ""], queryFn: () => mod.messages(project, undefined, true), ...quiet });
  const n = msgs.data?.length;
  const inbox = status.data?.mode === "inbox";
  return (
    <Tile
      icon={<Mail />}
      title={PARTS.email.name}
      kind={status.data ? (inbox ? "Catching email: nothing is sent yet" : "Sending for real") : PARTS.email.sub}
      to="/projects/$project/email"
      params={{ project }}
      fact={
        msgs.isError ? (
          <span className="text-ink-3">Email isn’t answering.</span>
        ) : n === undefined ? (
          <Skeleton className="h-4 w-28" />
        ) : inbox ? (
          n === 0 ? "No mail caught yet." : `${n >= 200 ? "200+" : int(n)} ${n === 1 ? "message" : "messages"} caught`
        ) : (
          `${count(n, "message")} recently`
        )
      }
    />
  );
}

function SignInTile({ project }: { project: string }) {
  const a = useQuery({ queryKey: ["auth", project], queryFn: () => mod3.auth(project), ...quiet });
  const s = a.data?.stats;
  return (
    <Tile
      icon={<UserRound />}
      title={PARTS.auth.name}
      kind={PARTS.auth.sub}
      to="/projects/$project/users"
      params={{ project }}
      fact={
        a.isError ? (
          <span className="text-ink-3">Sign-in isn’t answering.</span>
        ) : !s ? (
          <Skeleton className="h-4 w-28" />
        ) : s.users === 0 ? (
          "No one has signed up yet."
        ) : (
          <>
            {count(s.users, "person", "people")}
            {s.signups7d > 0 && <> · {int(s.signups7d)} new this week</>}
          </>
        )
      }
    />
  );
}

function AnalyticsTile({ project }: { project: string }) {
  const a = useQuery({ queryKey: ["analytics", project, "24h", "box"], queryFn: () => mod2.analytics(project, "24h"), ...quiet });
  const t = a.data?.totals;
  return (
    <Tile
      icon={<BarChart3 />}
      title={PARTS.analytics.name}
      kind={`${PARTS.analytics.sub} · last 24 hours`}
      to="/projects/$project/analytics"
      params={{ project }}
      fact={
        a.isError ? (
          <span className="text-ink-3">Analytics isn’t answering.</span>
        ) : !t ? (
          <Skeleton className="h-4 w-28" />
        ) : t.pageviews === 0 ? (
          "No visits yet today."
        ) : (
          <>
            {count(t.visitors, "visitor")} · {count(t.pageviews, "page view")}
          </>
        )
      }
    />
  );
}

function JobsTile({ project, crons, queues }: { project: string; crons: Array<{ schedule?: string }>; queues: number }) {
  const bits = [crons.length === 1 && crons[0].schedule ? `Runs ${cronWords(crons[0].schedule)}` : crons.length > 1 ? count(crons.length, "schedule") : "", queues > 0 ? count(queues, "queue") : ""].filter(Boolean);
  return <Tile icon={<Clock />} title={PARTS.jobs.name} kind={PARTS.jobs.sub} to="/projects/$project/queues" params={{ project }} fact={bits.join(" · ")} />;
}

/** Something being added right now: where its tile will be, with a spinner. */
function PendingTile({ words }: { words: string }) {
  return (
    <li className="flex min-h-[148px] items-center justify-center gap-2.5 rounded-[12px] border border-rule-2 bg-paper-raised text-[0.875rem] text-brass-ink" role="status">
      <span className="spinner" aria-hidden />
      {words}
    </li>
  );
}

function AddTile({ project, manifest, routes, empty }: { project: string; manifest?: Manifest; routes: string[]; empty: boolean }) {
  return (
    <li className="flex min-h-[112px] sm:min-h-[148px]">
      <AddMenu
        project={project}
        manifest={manifest}
        routes={routes}
        trigger={
          <button
            disabled={!manifest}
            className="flex w-full flex-col items-center justify-center gap-1.5 rounded-[12px] border border-dashed border-rule-3 px-4 text-center text-ink-3 transition-colors duration-[var(--dur-state)] hover:border-ink-4 hover:bg-paper-sunk hover:text-ink disabled:opacity-50"
          >
            <Plus className="size-5" />
            <span className="text-[0.9375rem] font-[550]">{empty ? "Add the first part" : "Add"}</span>
            <span className="text-xs">An app, a database, files, email, auth…</span>
          </button>
        }
      />
    </li>
  );
}
