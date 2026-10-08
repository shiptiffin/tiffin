import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Moon } from "lucide-react";
import { notOnBox, type ManifestApp } from "@/api/client";
import type { Deploy } from "@/api/modules";
import { q as core } from "@/api/queries";
import { AddAppButton, DeployButton } from "@/components/deploy-actions";
import { appKind, DeployHead, DeployRow, inFlight, startedBy, Terminal, useMakeCurrent, useNow, useProjectDeploys, useUrlState } from "@/components/deploy-parts";
import { useTitle } from "@/components/favicon";
import { Crumbs, Empty, NotOnBox, Page, PageHeader, Skeleton } from "@/components/page";
import { PilotLight, type PilotState } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/choice";
import { count } from "@/lib/format";
import { useMe, useWho } from "@/lib/me";
import { appPulse, runtimeQuery, type Tone } from "@/lib/pulse";
import { startersQuery } from "@/lib/starters";
import { relative } from "@/lib/time";

type Env = "all" | "production" | "preview";
type StatusFilter = "any" | "live" | "running" | "failed" | "past";

const statusOptions: Array<{ value: StatusFilter; label: string }> = [
  { value: "any", label: "Any status" },
  { value: "live", label: "Live" },
  { value: "running", label: "Building" },
  { value: "failed", label: "Failed" },
  { value: "past", label: "Replaced or stopped" },
];

function matches(f: StatusFilter, s: Deploy["status"]) {
  if (f === "any") return true;
  if (f === "live") return s === "live";
  if (f === "running") return inFlight(s);
  if (f === "failed") return s === "failed";
  return s === "superseded" || s === "rolled_back" || s === "stopped" || s === "skipped";
}

const KEYS = ["app", "env", "status", "branch"] as const;

/**
 * Every deploy of every app in a project, newest first, like Vercel's
 * Deployments: the apps at a glance on top, then one row per deploy with
 * filters (kept in the address) and a menu on each row.
 */
export function DeploymentsPage({ project }: { project: string }) {
  useTitle(`${project} · Deployments`);
  const { can } = useMe();
  const writer = can("apply:reversible");
  const who = useWho();
  const m = useQuery(core.manifest(project));
  const apps = Object.entries(m.data?.manifest.apps ?? {}) as Array<[string, ManifestApp]>;
  const names = apps.map(([a]) => a);
  const all = useProjectDeploys(project, names);
  const starters = useQuery(startersQuery);
  const makeCurrent = useMakeCurrent(project);
  const [f, setF] = useUrlState(KEYS);
  const now = useNow(all.rows.some((d) => inFlight(d.status)));

  if (all.error && notOnBox(all.error)) return <NotOnBox what="Deployments" />;

  const app = f.app && names.includes(f.app) ? f.app : "all";
  const env: Env = f.env === "production" || f.env === "preview" ? f.env : "all";
  const status: StatusFilter = statusOptions.some((o) => o.value === f.status) ? (f.status as StatusFilter) : "any";
  const branches = [...new Set(all.rows.filter((d) => app === "all" || d.app === app).map((d) => d.ref).filter((r): r is string => !!r))].sort();
  const branch = f.branch && branches.includes(f.branch) ? f.branch : "all";
  const rows = all.rows.filter(
    (d) =>
      (app === "all" || d.app === app) &&
      (env === "all" || (env === "production" ? !d.preview : !!d.preview)) &&
      matches(status, d.status) &&
      (branch === "all" || d.ref === branch),
  );
  const filtered = app !== "all" || env !== "all" || status !== "any" || branch !== "all";
  const building = all.rows.filter((d) => inFlight(d.status)).length;

  return (
    <Page wide>
      <PageHeader
        eyebrow={<Crumbs items={[{ label: project, to: "/projects/$project", params: { project } }, { label: "Deployments" }]} />}
        title="Deployments"
        lede={apps.length > 0 ? `Every version of every app in ${project}, newest first. A new version takes traffic only once it’s healthy.` : undefined}
        actions={
          writer && m.data ? (
            <>
              <DeployButton project={project} apps={apps} hasVersions={(a) => (all.byApp.get(a)?.size ?? 0) > 0} />
              <AddAppButton project={project} manifest={m.data.manifest} variant={apps.length ? "secondary" : "primary"} />
            </>
          ) : null
        }
      />
      {m.isError && <ProblemNote className="mt-8" error={m.error} />}
      {m.isPending ? (
        <Skeleton className="mt-9 h-40" />
      ) : apps.length === 0 ? (
        <Empty className="mt-9" title={`No apps in ${project} yet.`}>
          <p>An app is code the box builds and runs: a website, an API or a background worker. Add one from a starter, a GitHub repository or a git URL, and its deployments show up here.</p>
          {writer && m.data ? (
            <div className="mt-4 flex justify-center">
              <AddAppButton project={project} manifest={m.data.manifest} variant="primary" />
            </div>
          ) : (
            <p className="mt-2">You can look, but adding an app needs write access to {project}.</p>
          )}
        </Empty>
      ) : (
        <>
          <section className="mt-9" aria-labelledby="apps-strip">
            <h2 id="apps-strip" className="label mb-2">
              Apps
            </h2>
            <ul className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
              {apps.map(([a, spec]) => (
                <AppCard key={a} project={project} app={a} spec={spec} list={all.pending ? undefined : all.of(a)} v={all.live.get(a) ? all.byApp.get(a)?.get(all.live.get(a)!.id) : undefined} />
              ))}
            </ul>
          </section>

          <section className="mt-10" aria-label="Deployments">
            {all.error ? <ProblemNote className="mb-4" error={all.error} title="Deployments can’t be read right now." /> : null}
            {all.pending ? (
              <div className="grid gap-2">
                {[0, 1, 2, 3].map((i) => (
                  <Skeleton key={i} className="h-14" />
                ))}
              </div>
            ) : all.rows.length === 0 && !all.error ? (
              <>
                <div className="border-y border-rule py-6">
                  <p className="text-md text-ink">Nothing deployed yet.</p>
                  <p className="mt-1 max-w-[40rem] text-sm text-ink-2">{writer ? "Use Deploy above to ship your first version." : "Deploys show up here."}</p>
                </div>
                <Terminal project={project} className="mt-6" />
              </>
            ) : (
              <>
                <div className="mb-3 grid grid-cols-2 items-center gap-2 sm:flex sm:flex-wrap">
                  {names.length > 1 && (
                    <Select
                      size="sm"
                      className="sm:w-40"
                      aria-label="App"
                      value={app}
                      onValueChange={(v) => setF({ app: v === "all" ? undefined : v, branch: undefined })}
                      options={[{ value: "all", label: "All apps" }, ...names.map((n) => ({ value: n, label: n }))]}
                    />
                  )}
                  <Select
                    size="sm"
                    className="sm:w-44"
                    aria-label="Status"
                    value={status}
                    onValueChange={(v) => setF({ status: v === "any" ? undefined : v })}
                    options={statusOptions.map((o) => ({ ...o, label: o.value === "running" && building ? `${o.label} (${building})` : o.label }))}
                  />
                  {branches.length > 1 && (
                    <Select
                      size="sm"
                      className="sm:w-44"
                      aria-label="Branch"
                      value={branch}
                      onValueChange={(v) => setF({ branch: v === "all" ? undefined : v })}
                      options={[{ value: "all", label: "All branches" }, ...branches.map((b) => ({ value: b, label: <span className="ident text-[0.75rem]">{b}</span> }))]}
                    />
                  )}
                  <Segmented<Env>
                    className="max-sm:col-span-2 max-sm:justify-self-start"
                    label="Production or previews"
                    value={env}
                    onChange={(v) => setF({ env: v === "all" ? undefined : v })}
                    options={[
                      { value: "all", label: "All" },
                      { value: "production", label: "Production" },
                      { value: "preview", label: "Previews" },
                    ]}
                  />
                  <span className="text-xs text-ink-3 tnum max-sm:col-span-2 sm:ml-auto">
                    {count(rows.length, "deployment")}
                    {filtered && (
                      <button type="button" onClick={() => setF({ app: undefined, env: undefined, status: undefined, branch: undefined })} className="ml-2 font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
                        Clear filters
                      </button>
                    )}
                  </span>
                </div>
                <DeployHead />
                {rows.length === 0 ? (
                  <div className="border-y border-rule py-8 text-center">
                    <p className="text-sm text-ink-2">No deployments match.</p>
                    {filtered && (
                      <Button size="sm" variant="ghost" className="mt-2" onClick={() => setF({ app: undefined, env: undefined, status: undefined, branch: undefined })}>
                        Show all
                      </Button>
                    )}
                  </div>
                ) : (
                  <ol className="divide-y divide-rule border-y border-rule">
                    {rows.map((d) => {
                      const vs = all.byApp.get(d.app);
                      const cur = all.live.get(d.app);
                      const canSwitch = !d.preview && !!d.digest && (d.status === "superseded" || d.status === "rolled_back");
                      return (
                        <DeployRow
                          key={d.id}
                          project={project}
                          d={d}
                          v={vs?.get(d.id)}
                          showApp={names.length > 1}
                          current={cur?.id === d.id}
                          who={startedBy(d, who)}
                          starters={starters.data}
                          now={now}
                          writer={writer}
                          rollback={!!cur && d.createdAt < cur.createdAt}
                          onMakeCurrent={canSwitch ? () => makeCurrent.mutate({ app: d.app, to: d, from: cur, v: vs?.get(d.id), fromV: cur ? vs?.get(cur.id) : undefined }) : undefined}
                        />
                      );
                    })}
                  </ol>
                )}
                {all.more && (
                  <div className="mt-4 flex justify-center">
                    <Button size="md" onClick={all.loadMore} disabled={all.loadingMore}>
                      {all.loadingMore ? "Loading…" : "Show older deployments"}
                    </Button>
                  </div>
                )}
              </>
            )}
          </section>
        </>
      )}
    </Page>
  );
}

const pilotFor: Record<Tone, PilotState> = { ok: "on", busy: "busy", bad: "fault", quiet: "off", unknown: "off" };

/** One app at a glance: name, what it is, what's live, its address. Opens the app. */
function AppCard({ project, app, spec, list, v }: { project: string; app: string; spec: ManifestApp; list?: Deploy[]; v?: number }) {
  const rt = useQuery(runtimeQuery(project, app));
  const pulse = appPulse(list, spec.role);
  const url = rt.data?.production?.url;
  const asleep = !!rt.data?.production?.sleeping;
  return (
    <li className="relative min-w-0 rounded-[10px] border border-rule-2 bg-paper-raised px-4 py-3 shadow-[var(--top-light)] transition-colors hover:border-rule-3">
      <div className="flex min-w-0 items-center gap-2">
        {asleep ? <Moon className="size-3 shrink-0 text-ink-3" aria-label="Asleep" /> : pulse ? <PilotLight state={pilotFor[pulse.tone]} label={pulse.words} /> : <Skeleton className="size-2 rounded-full" />}
        <Link
          to="/projects/$project/apps/$app"
          params={{ project, app }}
          className="min-w-0 truncate text-[0.9375rem] font-[550] text-ink outline-hidden after:absolute after:inset-0 after:rounded-[10px] focus-visible:after:shadow-[0_0_0_2px_var(--brass)]"
        >
          {app}
        </Link>
        {v !== undefined && <span className="ml-auto shrink-0 text-[0.8125rem] text-ink-2 tnum">v{v}</span>}
      </div>
      <p className="mt-0.5 truncate text-xs text-ink-3">
        {appKind(spec.framework, spec.role)}
        {pulse && (
          <>
            {" · "}
            <span className={pulse.tone === "bad" ? "text-danger" : pulse.tone === "busy" ? "text-brass-ink" : undefined}>
              {asleep ? "Asleep" : pulse.words}
              {pulse.tone === "ok" && pulse.since && !asleep ? ` ${relative(pulse.since)}` : ""}
            </span>
          </>
        )}
      </p>
      <div className="mt-2 min-w-0">
        {url ? (
          <a href={url} target="_blank" rel="noopener noreferrer" className="ident relative z-[1] inline-flex max-w-full items-center gap-1 text-[0.75rem] text-brass-ink hover:text-ink">
            <span className="truncate">{url.replace(/^https?:\/\//, "")}</span>
            <ArrowUpRight className="size-3 shrink-0" />
          </a>
        ) : (
          <span className="text-xs text-ink-4">{spec.role === "worker" ? "Runs in the background, no address" : "No address until it’s live"}</span>
        )}
      </div>
    </li>
  );
}
