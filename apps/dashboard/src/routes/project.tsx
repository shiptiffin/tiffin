import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, BarChart3, Clock, Database, FolderOpen, Mail, Moon, Plus, UserRound, Zap } from "lucide-react";
import { useMemo, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { mod, mod2, mod3, mq } from "@/api/modules";
import { q } from "@/api/queries";
import { allDeploysQuery, appKind, DeployRow, inFlight, sourceWords, startedBy, useNow, useProjectDeploys, versions } from "@/components/deploy-parts";
import { useTitle } from "@/components/favicon";
import { Empty, Page, Skeleton } from "@/components/page";
import { PilotLight, type PilotState } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { ReadOnlyBanner } from "@/components/read-only";
import { EditCodeButton } from "@/components/edit-code";
import { AddMenu, APP_WORDS } from "@/components/start-add-menu";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { bytes, count, cronWords, dec, int } from "@/lib/format";
import { useMe, useWho } from "@/lib/me";
import { appPulse, runtimeQuery, toneClass, useProjectPulse } from "@/lib/pulse";
import { useArrival } from "@/lib/switch";
import { change, progressWords, usePending } from "@/lib/staged";
import { startersQuery } from "@/lib/starters";
import { PARTS } from "@/lib/names";
import { relative, sinceWhen } from "@/lib/time";
import { ProjectIcon } from "@/components/project-icon";
import { DomainsSummary } from "@/components/project-domains";

/**
 * A project's overview, like Vercel's: its name, one status line and live
 * address; then Production (each app: what's live and where, its last
 * deploy, requests and errors) and the latest deployments; on the side its
 * services with a fact and a way straight in, and its domains.
 */
export function ProjectPage({ project }: { project: string }) {
  useTitle(project);
  const arrived = useArrival(project);
  const p = useQuery(q.project(project));
  const m = useQuery({ ...q.manifest(project), staleTime: 5_000, refetchInterval: 10_000 });
  const pulse = useProjectPulse(project);
  const staged = usePending(project);
  const { can } = useMe();

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
  const services = (["postgres", "valkey", "storage", "email", "auth", "analytics"] as const).filter((x) => has(`service/${x}`));
  const addingApps = staged.flatMap((e) => (e.kind === "set" && e.path[0] === "apps" && e.path.length === 2 && e.to !== undefined && !apps.some((a) => a.name === e.path[1]) ? [e.path[1] as string] : []));
  const addingAuth = staged.some((e) => e.kind === "service" && e.service === "auth" && e.to === "on");
  const writer = can("apply:reversible");
  const addingParts = staged.flatMap((e) => (e.kind === "service" && e.to === "on" && !has(`service/${e.service}`) ? [progressWords(e, project)] : []));

  return (
    <Page wide>
      <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex min-w-0 items-center gap-4">
          <div className="min-w-0">
            <h1 className="title flex items-center gap-3 text-ink">
              <ProjectIcon project={project} size={32} />
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
        {(pulse.url || apps.length > 0) && (
          <div className="flex flex-col items-start gap-1.5 sm:items-end">
            <div className="flex flex-wrap items-center gap-2 sm:justify-end">
              {apps.length > 0 && <EditCodeButton project={project} apps={apps.map((a) => a.name)} />}
              {pulse.url && (
                <Button asChild variant="secondary" size="lg">
                  <a href={pulse.url} target="_blank" rel="noopener noreferrer">
                    {hostOf(pulse.url)}
                    <ArrowUpRight className="text-ink-3" />
                  </a>
                </Button>
              )}
            </div>
            {pulse.otherUrls.length > 0 && (
              <p className="text-[0.8125rem] text-ink-3">
                also at{" "}
                {pulse.otherUrls.map((u, i) => (
                  <span key={u}>
                    {i > 0 && ", "}
                    <a href={u} target="_blank" rel="noopener noreferrer" className="ident text-[0.75rem] hover:text-ink">
                      {hostOf(u)}
                    </a>
                  </span>
                ))}
              </p>
            )}
          </div>
        )}
      </header>
      <ReadOnlyBanner project={project} className="mt-6" />
      {arrived && (
        <p role="status" className="mt-5 text-[0.9375rem] text-ink-3">
          {project} doesn’t have {arrived}, so you’re on its Overview.
        </p>
      )}


      {m.isError && <ProblemNote className="mt-8" error={m.error} title="The project’s config can’t be read." />}
      {!p.data ? (
        <div className="mt-10 grid gap-3">
          <Skeleton className="h-6 w-40" />
          <Skeleton className="h-20" />
          <Skeleton className="h-20" />
        </div>
      ) : empty && addingApps.length === 0 && addingParts.length === 0 ? (
        <Empty className="mt-10" title={`Nothing in ${project} yet.`}>
          <p>{APP_WORDS}</p>
          <div className="mt-4 flex justify-center">
            <AddMenu project={project} manifest={man} only="app" trigger={<Button variant="primary" size="lg" disabled={!man}><Plus />Add an app</Button>} />
          </div>
        </Empty>
      ) : (
        <div className="mt-10 grid items-start gap-x-12 gap-y-12 xl:grid-cols-[minmax(0,1fr)_340px]">
          <div className="min-w-0">
            <section aria-labelledby="prod-h">
              <SectionHead id="prod-h" title="Production">
                {writer && (apps.length > 0 || addingApps.length > 0) && (
                  <AddMenu
                    project={project}
                    manifest={man}
                    only="app"
                    trigger={
                      <button type="button" disabled={!man} className="inline-flex items-center gap-1 text-[0.8125rem] text-ink-3 hover:text-ink disabled:opacity-50">
                        <Plus className="size-3.5" />
                        Add app
                      </button>
                    }
                  />
                )}
              </SectionHead>
              {apps.length === 0 && addingApps.length === 0 ? (
                <div className="flex flex-wrap items-center justify-between gap-3 border-y border-rule py-4">
                  <p className="text-sm text-ink-2">No app yet. {APP_WORDS}</p>
                  {writer && <AddMenu project={project} manifest={man} only="app" trigger={<Button size="md" disabled={!man}><Plus />Add app</Button>} />}
                </div>
              ) : (
                <ul className="divide-y divide-rule border-y border-rule">
                  {apps.map((a) => (
                    <AppRow key={a.name} project={project} app={a.name} role={a.spec?.role} framework={a.spec?.framework} headUrl={pulse.url} />
                  ))}
                  {addingApps.map((a) => (
                    <PendingRow key={a} words={`Adding the ${a} app…`} />
                  ))}
                </ul>
              )}
            </section>
            {apps.length > 0 && <RecentDeploys project={project} apps={apps.map((a) => a.name)} />}
          </div>

          <aside className="flex min-w-0 flex-col gap-10">
            <section aria-labelledby="svc-h">
              <SectionHead id="svc-h" title="Services">
                {/* Database, KV, Files, Email, Analytics and Jobs are always there; Auth is the one part that's added. */}
                {writer && man && !services.includes("auth") && !addingAuth && (
                  <button
                    type="button"
                    onClick={() => change(project, { kind: "service", service: "auth", from: "off", to: "on" }, { immediate: true })}
                    className="inline-flex items-center gap-1 text-[0.8125rem] text-ink-3 hover:text-ink"
                    title={PARTS.auth.sub}
                  >
                    <Plus className="size-3.5" />
                    Add {PARTS.auth.name}
                  </button>
                )}
              </SectionHead>
              <ul className="divide-y divide-rule border-y border-rule">
                  {services.includes("postgres") && <DatabaseRow project={project} />}
                  {services.includes("valkey") && <CacheRow project={project} />}
                  {services.includes("storage") && <FilesRow project={project} />}
                  {services.includes("email") && <EmailRow project={project} />}
                  {services.includes("auth") && <SignInRow project={project} />}
                  {services.includes("analytics") && <AnalyticsRow project={project} />}
                  <JobsRow project={project} crons={crons.map((c) => c.spec as { schedule?: string })} queues={queues.length} />
                  {addingParts.map((w) => (
                    <PendingRow key={w} words={w} />
                  ))}
              </ul>
            </section>
            {apps.some((a) => a.spec?.role !== "worker") && <DomainsSummary project={project} />}
          </aside>
        </div>
      )}
    </Page>
  );
}

// ───────────────────────── parts ─────────────────────────

const quiet = { retry: false, refetchOnWindowFocus: false, staleTime: 15_000 } as const;

function SectionHead({ id, title, children }: { id: string; title: string; children?: ReactNode }) {
  return (
    <div className="mb-2 flex items-baseline justify-between gap-4">
      <h2 id={id} className="label">
        {title}
      </h2>
      {children}
    </div>
  );
}

/**
 * One app in production: what's live and where, its last deploy, and how
 * it's doing (requests and errors in the last hour). The row opens the app.
 */
function AppRow({ project, app, role, framework, headUrl }: { project: string; app: string; role?: string; framework?: string; headUrl?: string }) {
  const who = useWho();
  const writer = useMe().can("apply:reversible");
  const d = useQuery(allDeploysQuery(project, app));
  const rt = useQuery(runtimeQuery(project, app));
  const metrics = useQuery({ queryKey: ["observe-apps", project], queryFn: () => mod.apps(project, "1h"), ...quiet, refetchInterval: 30_000 });
  const list = useMemo(() => [...(d.data ?? [])].sort((a, b) => b.createdAt.localeCompare(a.createdAt)), [d.data]);
  const pulse = appPulse(d.data, role);
  const vs = versions(list);
  const prod = list.filter((x) => !x.preview);
  const live = prod.find((x) => x.status === "live");
  const latest = prod[0];
  // The header already shows the project's live address; the row shows its own only when it's another one.
  const url = rt.data?.production?.url === headUrl ? undefined : rt.data?.production?.url;
  const asleep = !!rt.data?.production?.sleeping;
  const mm = (metrics.data ?? []).find((x) => x.app === app);
  const pilot: PilotState = asleep ? "off" : pulse?.tone === "ok" ? "on" : pulse?.tone === "busy" ? "busy" : pulse?.tone === "bad" ? "fault" : "off";
  const by = latest ? startedBy(latest, who) : undefined;

  let state: ReactNode = <Skeleton className="h-4 w-32" />;
  if (d.isError) state = <span className="text-ink-3">Its deploys can’t be read here.</span>;
  else if (pulse) {
    if (pulse.tone === "bad")
      state = <span className="text-danger">{pulse.servingOld ? `v${vs.get(latest!.id)} didn’t start. v${live ? vs.get(live.id) : "?"} is still serving.` : `It didn’t start${pulse.failedWhy ? `: ${pulse.failedWhy}` : "."}`}</span>;
    else if (pulse.tone === "busy") state = <span className="text-brass-ink">{pulse.words} v{vs.get(latest!.id)}…</span>;
    else if (pulse.tone === "quiet")
      state = (
        <span className="text-ink-3">
          Not live yet
          {writer && (
            <>
              {" · "}
              <Link to="/projects/$project/apps/$app" params={{ project, app }} search={{ deploy: true }} className="relative z-[1] font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                Deploy it
              </Link>
            </>
          )}
        </span>
      );
    else if (asleep) state = `v${live ? vs.get(live.id) : "?"} · asleep${rt.data?.production?.sleepingSince ? ` since ${sinceWhen(rt.data.production.sleepingSince)}` : ""}`;
    else state = `v${live ? vs.get(live.id) : "?"} live ${relative(pulse.since!)}`;
  }

  let health: ReactNode = null;
  if (role === "worker") health = <span className="text-ink-3">Background worker</span>;
  else if (framework === "static") health = <span className="text-ink-3">Served by the edge</span>;
  else if (asleep) health = <span className="text-ink-3">Wakes on the next visit</span>;
  else if (live && mm && mm.requests > 0)
    health = (
      <>
        <span className="tnum">{mm.rps * 60 >= 1 ? `${int(mm.rps * 60)} requests a minute` : `${count(mm.requests, "request")} this hour`}</span>
        <span className={cn("block text-xs tnum", mm.errors > 0 ? "text-danger" : "text-ink-3")}>{mm.errors > 0 ? `${count(mm.errors, "error")} (${dec(mm.errorRate * 100, 1)}%)` : "No errors"}</span>
      </>
    );
  else if (live) health = <span className="text-ink-3">No requests this hour</span>;

  return (
    <li className="group relative grid grid-cols-[1rem_minmax(0,1fr)_auto] items-start gap-x-3 gap-y-2 py-3.5 pr-1 transition-colors hover:bg-paper-hover md:grid-cols-[1rem_minmax(0,14rem)_minmax(0,1fr)_minmax(0,10rem)] md:gap-x-5">
      <span className="grid h-5 place-items-center">{asleep ? <Moon className="size-3 text-ink-3" aria-label="Asleep" /> : <PilotLight state={pilot} label={pulse?.words} />}</span>
      <div className="min-w-0">
        <Link
          to="/projects/$project/apps/$app"
          params={{ project, app }}
          className="block truncate text-[0.9375rem] leading-5 font-[550] text-ink outline-hidden after:absolute after:inset-0 focus-visible:after:shadow-[inset_0_0_0_2px_var(--brass)]"
        >
          {app}
        </Link>
        <p className="truncate text-xs text-ink-3">{appKind(framework, role)}</p>
        {url && (
          <a href={url} target="_blank" rel="noopener noreferrer" className="ident relative z-[1] mt-1 inline-flex max-w-full items-center gap-1 text-[0.75rem] text-brass-ink hover:text-ink">
            <span className="truncate">{url.replace(/^https?:\/\//, "")}</span>
            <ArrowUpRight className="size-3 shrink-0" />
          </a>
        )}
      </div>
      <div className="min-w-0 text-[0.8125rem] text-ink max-md:col-span-2 max-md:col-start-2 max-md:row-start-2">
        <p className="truncate">{state}</p>
        {latest && (
          <Link
            to="/projects/$project/apps/$app/deploys/$id"
            params={{ project, app, id: latest.id }}
            className="relative z-[1] mt-0.5 flex min-w-0 items-center gap-1.5 text-xs text-ink-3 hover:text-ink"
          >
            <span className="shrink-0">Last deploy</span>
            <span className="truncate">
              {latest.message ? `“${latest.message}”` : sourceWords(latest)}
              {latest.commit && !latest.message ? "" : latest.commit ? ` · ${latest.commit.slice(0, 7)}` : ""} · {relative(latest.createdAt)}
              {by ? ` by ${by}` : ""}
            </span>
          </Link>
        )}
      </div>
      <div className="min-w-0 text-right text-[0.8125rem] text-ink max-md:col-start-3 max-md:row-start-1">{health}</div>
    </li>
  );
}

/** The project's last few deploys, every app, with the Deployments rows. */
function RecentDeploys({ project, apps }: { project: string; apps: string[] }) {
  const { can } = useMe();
  const who = useWho();
  const all = useProjectDeploys(project, apps);
  const starters = useQuery(startersQuery);
  const now = useNow(all.rows.some((d) => inFlight(d.status)));
  const rows = all.rows.slice(0, 5);
  if (!all.pending && rows.length === 0) return null;
  return (
    <section className="mt-12" aria-labelledby="recent-h">
      <SectionHead id="recent-h" title="Recent deployments">
        <Link to="/projects/$project/deployments" params={{ project }} className="text-[0.8125rem] text-ink-3 hover:text-ink">
          All deployments
        </Link>
      </SectionHead>
      {all.pending ? (
        <Skeleton className="h-40" />
      ) : (
        <ol className="divide-y divide-rule border-y border-rule">
          {rows.map((d) => (
            <DeployRow
              key={d.id}
              project={project}
              d={d}
              v={all.byApp.get(d.app)?.get(d.id)}
              showApp={apps.length > 1}
              current={all.live.get(d.app)?.id === d.id}
              who={startedBy(d, who)}
              starters={starters.data}
              now={now}
              writer={can("apply:reversible")}
            />
          ))}
        </ol>
      )}
    </section>
  );
}

/** A service in the side list: its name, one fact, and a link or two straight in. */
function ServiceRow({ icon, title, sub, to, fact, links }: { icon: ReactNode; title: string; sub?: string; to: string; fact: ReactNode; links?: Array<{ label: string; to: string }> }) {
  return (
    <li className="group relative grid grid-cols-[1.75rem_minmax(0,1fr)_auto] items-start gap-x-3 py-3 pr-1 transition-colors hover:bg-paper-hover">
      <span className="grid size-7 place-items-center rounded-[7px] bg-paper-sunk text-ink-2 [&_svg]:size-[15px]">{icon}</span>
      <div className="min-w-0">
        <Link to={to as "/"} className="block truncate text-[0.875rem] leading-5 font-[550] text-ink outline-hidden after:absolute after:inset-0 focus-visible:after:shadow-[inset_0_0_0_2px_var(--brass)]">
          {title}
          {sub && <span className="ml-1.5 text-xs font-[400] text-ink-3">{sub}</span>}
        </Link>
        <div className="truncate text-[0.8125rem] text-ink-2">{fact}</div>
      </div>
      {links && (
        <span className="relative z-[1] flex items-center gap-2 pt-0.5">
          {links.map((l) => (
            <Link key={l.label} to={l.to as "/"} className="text-xs text-ink-3 hover:text-ink">
              {l.label}
            </Link>
          ))}
        </span>
      )}
    </li>
  );
}

const pth = (project: string, rest = "") => `/projects/${encodeURIComponent(project)}${rest}`;

function DatabaseRow({ project }: { project: string }) {
  const tables = useQuery({ ...mq.tables(project), ...quiet });
  const pg = useQuery({ ...mq.pg(project), ...quiet });
  const n = tables.data?.length;
  return (
    <ServiceRow
      icon={<Database />}
      title={PARTS.postgres.name}
      sub={PARTS.postgres.sub}
      to={pth(project, "/data")}
      links={[{ label: "SQL", to: pth(project, "/data/sql") }]}
      fact={tables.isError ? <span className="text-danger">Not answering</span> : n === undefined ? <Skeleton className="mt-1 h-3.5 w-24" /> : n === 0 ? "No tables yet" : <>{count(n, "table")}{pg.data && <> · {bytes(pg.data.sizeBytes)}</>}</>}
    />
  );
}

function CacheRow({ project }: { project: string }) {
  const kv = useQuery({ queryKey: ["kv-stats", project], queryFn: () => mod.kvStats(project), ...quiet });
  return (
    <ServiceRow
      icon={<Zap />}
      title={PARTS.valkey.name}
      to={pth(project, "/data/kv")}
      links={[{ label: "Console", to: pth(project, "/data/kv/console") }]}
      fact={kv.isError ? <span className="text-danger">Not answering</span> : kv.data ? `${count(kv.data.keys, "key")} · ${bytes(kv.data.memoryBytes)}` : <Skeleton className="mt-1 h-3.5 w-24" />}
    />
  );
}

function FilesRow({ project }: { project: string }) {
  const st = useQuery({ ...mq.storage(project), ...quiet });
  const b = st.data?.buckets ?? [];
  const files = b.reduce((n, x) => n + (x.objects ?? 0), 0);
  return (
    <ServiceRow
      icon={<FolderOpen />}
      title={PARTS.storage.name}
      to={pth(project, "/storage")}
      fact={st.isError ? <span className="text-danger">Not answering</span> : st.data ? `${count(files, "file")} · ${bytes(st.data.filesBytes)} · ${count(b.length, "bucket")}` : <Skeleton className="mt-1 h-3.5 w-24" />}
    />
  );
}

function EmailRow({ project }: { project: string }) {
  const status = useQuery({ ...mq.emailStatus, ...quiet });
  const msgs = useQuery({ queryKey: ["messages", project, ""], queryFn: () => mod.recentMail(project), ...quiet });
  const n = msgs.data?.length;
  const inbox = status.data?.mode === "inbox";
  return (
    <ServiceRow
      icon={<Mail />}
      title={PARTS.email.name}
      sub={status.data ? (inbox ? "catching, not sending" : "sending") : undefined}
      to={pth(project, "/email")}
      fact={msgs.isError ? <span className="text-danger">Not answering</span> : n === undefined ? <Skeleton className="mt-1 h-3.5 w-24" /> : inbox ? (n === 0 ? "No mail caught yet" : `${n >= 200 ? "200+" : int(n)} caught`) : `${count(n, "message")} recently`}
    />
  );
}

function SignInRow({ project }: { project: string }) {
  const a = useQuery({ queryKey: ["auth", project], queryFn: () => mod3.auth(project), ...quiet });
  const s = a.data?.stats;
  return (
    <ServiceRow
      icon={<UserRound />}
      title={PARTS.auth.name}
      to={pth(project, "/users")}
      fact={a.isError ? <span className="text-danger">Not answering</span> : !s ? <Skeleton className="mt-1 h-3.5 w-24" /> : s.users === 0 ? "No one has signed up yet" : <>{count(s.users, "person", "people")}{s.signups7d > 0 && <> · {int(s.signups7d)} new this week</>}</>}
    />
  );
}

function AnalyticsRow({ project }: { project: string }) {
  const a = useQuery({ queryKey: ["analytics", project, "24h", "box"], queryFn: () => mod2.analytics(project, "24h"), ...quiet });
  const t = a.data?.totals;
  return (
    <ServiceRow
      icon={<BarChart3 />}
      title={PARTS.analytics.name}
      sub="last 24 hours"
      to={pth(project, "/analytics")}
      fact={a.isError ? <span className="text-danger">Not answering</span> : !t ? <Skeleton className="mt-1 h-3.5 w-24" /> : t.pageviews === 0 ? "No visits yet today" : <>{count(t.visitors, "visitor")} · {count(t.pageviews, "page view")}</>}
    />
  );
}

function JobsRow({ project, crons, queues }: { project: string; crons: Array<{ schedule?: string }>; queues: number }) {
  const bits = [crons.length === 1 && crons[0].schedule ? `Runs ${cronWords(crons[0].schedule)}` : crons.length > 1 ? count(crons.length, "schedule") : "", queues > 0 ? count(queues, "queue") : ""].filter(Boolean);
  return <ServiceRow icon={<Clock />} title={PARTS.jobs.name} to={pth(project, "/jobs")} fact={bits.join(" · ") || "No schedules or queues yet"} />;
}

/** Something being added right now, where its row will be. */
function PendingRow({ words }: { words: string }) {
  return (
    <li className="flex items-center gap-2.5 py-3.5 text-[0.875rem] text-brass-ink" role="status">
      <span className="spinner" aria-hidden />
      {words}
    </li>
  );
}

const hostOf = (u: string) => u.replace(/^https?:\/\//, "").replace(/:\d+$/, "");
