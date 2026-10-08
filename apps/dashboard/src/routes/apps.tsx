import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Navigate, useNavigate } from "@tanstack/react-router";
import { ArrowUpRight, Check, Copy, Download, Moon, Pause, Play, RotateCw, Search, Trash2, Undo2 } from "lucide-react";
import { Tabs } from "radix-ui";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { notOnBox, type ManifestApp } from "@/api/client";
import { deploysApi, mod3, type Deploy, type LogLine } from "@/api/modules";
import { q as core } from "@/api/queries";
import { Confirm } from "@/components/confirm";
import { CopyButton } from "@/components/copy";
import {
  appKind,
  buildAgain,
  canBuildAgain,
  DeployHead,
  DeployRow,
  DeploySource,
  inFlight,
  refreshApp,
  rollbackTarget,
  secs,
  sourceWords,
  startedBy,
  useAppDeploys,
  useMakeCurrent,
  useNow,
  useUrlState,
  versions,
} from "@/components/deploy-parts";
import { useTitle } from "@/components/favicon";
import { lineLevel } from "@/components/logs-query";
import { Crumbs, NotOnBox, Page, PageHeader, Skeleton, Untrusted } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { firstError, firstErrorIn } from "@/components/start-build-log";
import { BuildLogViewer, saveText, type BuildLogHandle } from "@/components/build-log-viewer";
import { useBuildLog } from "@/components/build-log-stream";
import { followLogLines } from "@/lib/log-follow";
import { DeployTray } from "@/components/start-deploy-tray";
import { AppRepo, useRedeploy } from "@/components/app-github";
import { INSTANCE_STOPS } from "@/components/throttle";
import { toast } from "@/components/toast";
import { Segmented } from "@/components/segmented";
import { InfoTip } from "@/components/info-tip";
import { RadioGroup, RadioItem, Select } from "@/components/ui/choice";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { count, dec, int, withUnit, words } from "@/lib/format";
import { useMe, useWho } from "@/lib/me";
import { change, pendingFor, serviceNames, usePending, type StagedEdit } from "@/lib/staged";
import { nextDeployFor, startersQuery } from "@/lib/starters";
import { clock, full, liveSince, relative, windowLabel } from "@/lib/time";
import { MEMORY_STOPS } from "@/components/project-rows";
import { ProjectIcon } from "@/components/project-icon";

// ------------------------------------------------------------------ shared

const MB = 1048576;
const RESERVE_MB = 512;
const mbWords = (n: number) => (n >= 1024 ? withUnit(dec(n / 1024, 1), "GB") : withUnit(int(n), "MB"));

/** project › Deployments › …: apps live under Deployments in the sidebar. */
function ProjectCrumbs({ project, items }: { project: string; items: Array<{ label: ReactNode; to?: string; params?: Record<string, string> }> }) {
  return (
    <Crumbs
      items={[
        {
          label: (
            <span className="inline-flex items-center gap-1.5">
              <ProjectIcon project={project} size={14} />
              {project}
            </span>
          ),
          to: "/projects/$project",
          params: { project },
        },
        { label: "Deployments", to: "/projects/$project/deployments", params: { project } },
        ...items,
      ]}
    />
  );
}

/** A label over a value, for the facts under a page's title. */
function Fact({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  return (
    <div className={cn("min-w-0", className)}>
      <dt className="text-xs text-ink-3">{label}</dt>
      <dd className="mt-0.5 min-w-0 text-[0.8125rem] text-ink">{children}</dd>
    </div>
  );
}

// ------------------------------------------------------------------ the list

/** The apps list moved into Deployments; old links land there. */
export function AppsPage({ project }: { project: string }) {
  return <Navigate to="/projects/$project/deployments" params={{ project }} replace />;
}

// ------------------------------------------------------------------ one app

type Env = "all" | "production" | "preview";

export function AppPage({ project, app, deploy }: { project: string; app: string; deploy?: boolean }) {
  useTitle(`${app} · ${project}`);
  const qc = useQueryClient();
  const { can } = useMe();
  const who = useWho();
  const rt = useQuery({ queryKey: ["runtime", project, app], queryFn: () => mod3.runtime(project, app), refetchInterval: 4000 });
  const deploys = useAppDeploys(project, app);
  const m = useQuery(core.manifest(project));
  const spec = m.data?.manifest.apps?.[app] as ManifestApp | undefined;
  const starters = useQuery(startersQuery);
  const resQ = useQuery(core.resources);
  const free = resQ.data && resQ.data.memory.totalBytes > 0 ? resQ.data.memory.availableBytes / MB - RESERVE_MB : undefined;
  const edits = usePending(project);
  const [tray, setTray] = useState(!!deploy);
  const [env, setEnv] = useState<Env>("all");
  const [restarting, setRestarting] = useState(false);
  const [deletePreview, setDeletePreview] = useState<string | null>(null);
  const makeCurrent = useMakeCurrent(project);
  const redeploy = useRedeploy(project, app, spec?.git);
  const list = useMemo(() => [...(deploys.data ?? [])].sort((a, b) => b.createdAt.localeCompare(a.createdAt)), [deploys.data]);
  const now = useNow(list.some((d) => inFlight(d.status)));
  const sleep = useMutation({
    mutationFn: (name: string) => mod3.sleepPreview(project, app, name),
    onSuccess: (_r, name) => {
      refreshApp(qc, project, app);
      toast({ title: <>Preview {name} is asleep.</>, detail: "It wakes on its next visit." });
    },
  });
  const restart = useMutation({
    mutationFn: () => mod3.restart(project, app),
    onMutate: () => setRestarting(true),
    onSettled: () => setRestarting(false),
    onSuccess: () => {
      refreshApp(qc, project, app);
      toast({ title: <>Restarting {app}.</>, detail: "One instance at a time, so it keeps answering." });
    },
    onError: (e) => toast({ title: <>{app} didn’t restart.</>, detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });

  if (rt.isError && notOnBox(rt.error)) return <NotOnBox what="Apps" />;
  const r = rt.data;
  const prod = r?.production;
  const vs = versions(list);
  const prodList = list.filter((d) => !d.preview);
  const latest = prodList[0];
  const current = prodList.find((d) => d.status === "live");
  const back = rollbackTarget(list, current);
  const writer = can("apply:reversible");
  const isStatic = (r?.framework ?? spec?.framework) === "static";
  const next = nextDeployFor(project, app);
  const instances = prod?.instances ?? [];
  const running = instances.filter((i) => i.running).length;
  const hasPreviews = list.some((d) => d.preview);
  const rows = list.filter((d) => env === "all" || (env === "production" ? !d.preview : !!d.preview));

  let sentence: ReactNode = null;
  // A failed check never leaves an older "is live" on screen.
  if (rt.isError || deploys.isError)
    sentence = (
      <span className="text-ink-2">
        Couldn’t check {app} right now.{" "}
        <button
          type="button"
          onClick={() => void Promise.all([rt.refetch(), deploys.refetch()])}
          className="font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink"
        >
          Retry
        </button>
      </span>
    );
  else if (r && deploys.data) {
    if (inFlight(latest?.status)) sentence = `${latest!.status === "starting" ? "Starting" : "Building"} v${vs.get(latest!.id)}, from ${sourceWords(latest!, starters.data)}.`;
    else if (latest?.status === "failed")
      sentence = <span className="text-danger">{current ? `v${vs.get(latest.id)} failed. v${vs.get(current.id)} is still serving.` : `The first deploy failed.`}</span>;
    else if (current)
      sentence = isStatic
        ? `v${vs.get(current.id)} is live, served by the edge.`
        : r.role === "worker"
          ? `v${vs.get(current.id)} is working on ${words(running)} ${running === 1 ? "instance" : "instances"}.`
          : `v${vs.get(current.id)} is live on ${words(running)} ${running === 1 ? "instance" : "instances"}.`;
    else if (prod?.stopped) sentence = "Stopped. Undo the change that removed it to bring it back.";
    else sentence = "Not deployed yet.";
  }

  return (
    <Page wide>
      <PageHeader
        eyebrow={<ProjectCrumbs project={project} items={[{ label: app }]} />}
        title={
          <span className="inline-flex items-center gap-3">
            {app}
            {inFlight(latest?.status) && <PilotLight state="busy" label="Building" />}
          </span>
        }
        actions={
          <>
            {writer && spec?.git ? (
              <Button variant="primary" size="lg" onClick={() => redeploy.mutate()} disabled={redeploy.isPending}>
                {redeploy.isPending ? "Starting…" : prodList.length ? "Redeploy" : `Deploy ${spec.git.branch ?? "main"}`}
              </Button>
            ) : writer ? (
              <Button variant="primary" size="lg" onClick={() => setTray(true)}>
                Deploy
              </Button>
            ) : null}
            {writer && back && current && (
              <Button
                size="lg"
                disabled={makeCurrent.isPending}
                onClick={() => makeCurrent.mutate({ app, to: back, from: current, v: vs.get(back.id), fromV: vs.get(current.id) })}
              >
                <Undo2 />
                {makeCurrent.isPending ? "Rolling back…" : `Roll back to v${vs.get(back.id)}`}
              </Button>
            )}
            <Button asChild size="lg">
              <Link to="/projects/$project/apps/$app/logs" params={{ project, app }}>
                Logs
              </Link>
            </Button>
            {writer && !isStatic && current && (
              <Button variant="ghost" size="lg" onClick={() => restart.mutate()} disabled={restarting}>
                <RotateCw className={cn(restarting && "animate-spin")} />
                Restart
              </Button>
            )}
          </>
        }
      />
      <div className="state-sentence mt-3 max-w-[46rem] text-ink">{sentence ?? <Skeleton className="h-8 w-72" />}</div>
      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[0.875rem] text-ink-3">
        {prod?.url && (
          <span className="inline-flex min-w-0 items-center gap-1">
            <a href={prod.url} target="_blank" rel="noopener noreferrer" className="ident inline-flex min-w-0 items-center gap-1 text-[0.8125rem] text-brass-ink hover:text-ink">
              <span className="truncate">{prod.url.replace(/^https?:\/\//, "")}</span>
              <ArrowUpRight className="size-3.5 shrink-0" />
            </a>
            <CopyButton value={prod.url} label="Copy the address" className="size-6" />
          </span>
        )}
        {(r || spec) && (
          <span>
            {appKind(r?.framework ?? spec?.framework, r?.role ?? spec?.role)}
            {(r?.role ?? spec?.role) === "worker" ? ", no public address" : ""}
          </span>
        )}
      </div>
      {r?.hint && <p className="mt-4 max-w-[44rem] rounded-[8px] bg-warn-wash px-3.5 py-2.5 text-sm text-ink">{r.hint}</p>}

      {deploys.isSuccess && prodList.length === 0 && writer && (
        <div className="mt-8 flex max-w-[44rem] flex-wrap items-center justify-between gap-4 rounded-[12px] border border-rule-2 bg-paper-raised px-5 py-4 shadow-raised">
          <div className="min-w-0">
            <p className="text-[0.9375rem] font-[550] text-ink">Nothing deployed yet.</p>
            <p className="text-sm text-ink-2">
              {spec?.git ? `It deploys from ${spec.git.repo} on every push to ${spec.git.branch ?? "main"}. Build the latest commit now.` : next ? ("template" in next ? `It was added from the ${starters.data?.find((s) => s.id === next.template)?.name ?? next.template} starter. Build it now.` : `It was added from ${next.git.url.replace(/^https:\/\//, "")}. Build it now.`) : "Deploy a starter or a git URL from here, or push from your terminal."}
            </p>
          </div>
          <Button variant="primary" size="lg" onClick={() => (spec?.git ? redeploy.mutate() : setTray(true))} disabled={redeploy.isPending}>
            {spec?.git ? `Deploy ${spec.git.branch ?? "main"}` : `Deploy ${app}`}
          </Button>
        </div>
      )}

      {current && (
        <section className="mt-8" aria-labelledby="production">
          <h2 id="production" className="label mb-2">
            Production
          </h2>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-4 border-y border-rule py-4 lg:grid-cols-4 lg:gap-x-8">
            <Fact label="Version">
              <Link to="/projects/$project/apps/$app/deploys/$id" params={{ project, app, id: current.id }} className="inline-flex items-center gap-2 font-[550] hover:text-brass-ink">
                <PilotLight state="on" />v{vs.get(current.id)}
              </Link>
            </Fact>
            <Fact label="Source">
              <DeploySource d={current} starters={starters.data} />
            </Fact>
            <Fact label="Went live">
              <span title={full(liveSince(current))}>{relative(liveSince(current))}</span>
              {startedBy(current, who) && <span className="block truncate text-xs text-ink-3">by {startedBy(current, who)}</span>}
            </Fact>
            <Fact label="Took">
              {current.durationSeconds !== undefined ? secs(current.durationSeconds) : "–"}
              {current.buildSeconds !== undefined && <span className="block text-xs text-ink-3">{secs(current.buildSeconds)} to build</span>}
            </Fact>
          </dl>
        </section>
      )}

      <div className="mt-10 grid items-start gap-x-12 gap-y-10 xl:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0">
          <section aria-labelledby="deployments">
            <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2">
              <h2 id="deployments" className="label">
                Deployments
              </h2>
              {list.length > 0 && <span className="text-xs text-ink-4 tnum">{list.length}</span>}
              {hasPreviews && (
                <Segmented<Env>
                  className="ml-auto"
                  label="Production or previews"
                  value={env}
                  onChange={setEnv}
                  options={[
                    { value: "all", label: "All" },
                    { value: "production", label: "Production" },
                    { value: "preview", label: "Previews" },
                  ]}
                />
              )}
            </div>
            {deploys.isPending ? (
              <Skeleton className="h-32" />
            ) : rows.length === 0 ? (
              <p className="border-y border-rule py-4 text-sm text-ink-3">{list.length ? "None of these yet." : "No deployments yet."}</p>
            ) : (
              <>
                <DeployHead />
                <ol className="divide-y divide-rule border-y border-rule">
                  {rows.map((d) => (
                    <DeployRow
                      key={d.id}
                      project={project}
                      d={d}
                      v={vs.get(d.id)}
                      current={d.id === current?.id}
                      who={startedBy(d, who)}
                      starters={starters.data}
                      now={now}
                      writer={writer}
                      rollback={!!current && d.createdAt < current.createdAt}
                      onMakeCurrent={
                        !d.preview && d.digest && (d.status === "superseded" || d.status === "rolled_back")
                          ? () => makeCurrent.mutate({ app, to: d, from: current, v: vs.get(d.id), fromV: current ? vs.get(current.id) : undefined })
                          : undefined
                      }
                    />
                  ))}
                </ol>
              </>
            )}
          </section>

          {(r?.previews ?? []).length > 0 && (
            <section className="mt-10" aria-labelledby="previews">
              <div className="mb-2 flex items-baseline gap-2">
                <h2 id="previews" className="label">
                  Previews
                </h2>
                <InfoTip label="About previews">Each preview has its own address, and idle ones sleep. Previews use this project’s live data; their email goes to the dev inbox.</InfoTip>
              </div>
              <ul className="divide-y divide-rule border-y border-rule">
                {(r?.previews ?? []).map((pv) => (
                  <li key={pv.preview} className="grid grid-cols-[20px_minmax(0,1fr)_auto] items-center gap-x-4 py-2.5">
                    <span className="grid place-items-center text-ink-3">{pv.sleeping ? <Moon className="size-3.5" aria-label="Asleep" /> : <PilotLight state="on" label="Awake" />}</span>
                    <span className="min-w-0">
                      <span className="ident text-[0.8125rem] text-ink">{pv.preview}</span>
                      <span className="ml-2 text-xs text-ink-3">
                        {pv.sleeping ? "asleep" : "awake"} · updated {relative(pv.updatedAt)}
                      </span>
                      {pv.url && (
                        <a href={pv.url} target="_blank" rel="noopener noreferrer" className="ident block truncate text-[0.71875rem] text-brass-ink hover:text-ink">
                          {pv.url.replace(/^https?:\/\//, "")}
                        </a>
                      )}
                    </span>
                    <span className="flex gap-1">
                      {writer && !pv.sleeping && (
                        <Button size="sm" variant="ghost" onClick={() => sleep.mutate(pv.preview!)}>
                          <Pause /> Sleep
                        </Button>
                      )}
                      {writer && (
                        <Button size="icon-sm" variant="ghost" aria-label={`Delete preview ${pv.preview}`} onClick={() => setDeletePreview(pv.preview!)} className="text-ink-3 hover:text-danger">
                          <Trash2 />
                        </Button>
                      )}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>

        <aside className="flex min-w-0 flex-col gap-9" aria-label="Settings">
          {spec?.git && <AppRepo project={project} app={app} git={spec.git} writer={writer} />}
          {spec && !isStatic && (
            <Scale project={project} app={app} spec={spec} free={free} instances={pendingFor(edits, `instances:${app}`)} memory={edits.find((e: StagedEdit) => e.kind === "set" && e.path.join("/") === `apps/${app}/memoryMB`)} writer={writer} />
          )}
          {spec && !isStatic && !["hono", "fastapi", "python"].includes(spec.framework ?? "") && <RuntimeSetting project={project} app={app} spec={spec} writer={writer} />}
          {!isStatic && instances.length > 0 && (
            <section aria-label="Instances">
              <h2 className="label mb-1.5">Instances</h2>
              <ul className="divide-y divide-rule border-y border-rule">
                {instances.map((i) => (
                  <li key={i.name} className="flex items-center gap-2.5 py-2 text-[0.8125rem]">
                    <PilotLight state={i.running ? "on" : "fault"} label={i.state} />
                    <span className="ident min-w-0 flex-1 truncate text-[0.75rem] text-ink">{i.name.split(".").slice(-2).join(".")}</span>
                    <span className="ident text-[0.71875rem] text-ink-3">:{i.port}</span>
                    {!i.running && <span className="text-xs text-danger">{i.state}</span>}
                    <span className="text-xs text-ink-3">v{vs.get(i.deploy) ?? "?"}</span>
                  </li>
                ))}
              </ul>
              {(prod?.draining ?? []).map((dset) => (
                <p key={dset.release} className="mt-2 text-xs text-ink-3">
                  {count(dset.instances?.length ?? 0, "old instance")} of v{vs.get(dset.release) ?? "?"} draining since {relative(dset.since)}.
                </p>
              ))}
            </section>
          )}
          {isStatic && <p className="text-sm text-ink-3">A static site: the edge serves its files directly. No instances, no memory of its own.</p>}
        </aside>
      </div>

      <DeployTray
        project={project}
        app={app}
        framework={r?.framework ?? spec?.framework}
        open={tray}
        onOpenChange={setTray}
        suggest={next}
        hasVersions={prodList.length > 0}
      />
      <Confirm
        open={!!deletePreview}
        onClose={() => setDeletePreview(null)}
        title={`Delete preview ${deletePreview}?`}
        body="Its instances stop and its address stops answering. Production isn’t touched."
        action="Delete preview"
        run={() => mod3.deletePreview(project, app, deletePreview!)}
        done={() => refreshApp(qc, project, app)}
      />
    </Page>
  );
}

/** Copies and memory, as steppers with what they add up to. Each step is a change (rapid clicks go as one). */
export function Scale({
  project,
  app,
  spec,
  free,
  instances,
  memory,
  writer,
}: {
  project: string;
  app: string;
  spec: ManifestApp;
  free?: number;
  instances?: StagedEdit;
  memory?: StagedEdit;
  writer: boolean;
}) {
  const applied = spec.instances ?? 1;
  // No limit (0) is the default: an app's copies share the project's memory.
  const appliedMem = spec.memoryMB ?? 0;
  // A choice here is a draft until Save: picking from a list never changes the app by itself.
  const [copies, setCopies] = useState(applied);
  const [mem, setMem] = useState(appliedMem);
  const [was, setWas] = useState(`${applied}/${appliedMem}`);
  if (was !== `${applied}/${appliedMem}`) {
    // Saved, undone or changed elsewhere: start from what the app has now.
    setWas(`${applied}/${appliedMem}`);
    setCopies(applied);
    setMem(appliedMem);
  }
  const saving = !!(instances || memory);
  const dirty = copies !== applied || mem !== appliedMem;
  const fits = free === undefined || !mem || copies <= applied || (copies - applied) * mem <= free;
  const limitWords = (n: number) => (n ? mbWords(n) : "No limit");
  const copyOptions = [...new Set([...INSTANCE_STOPS, applied])].sort((x, y) => x - y);
  const memOptions = [...new Set([0, ...MEMORY_STOPS, appliedMem])].sort((x, y) => x - y);
  const save = () => {
    if (copies !== applied) change(project, { kind: "instances", app, from: applied, to: copies }, { immediate: true });
    if (mem !== appliedMem)
      change(
        project,
        {
          kind: "set",
          path: ["apps", app, "memoryMB"],
          from: spec.memoryMB,
          to: mem || undefined,
          what: mem ? `Limit each copy of ${app} to ${mbWords(mem)}` : `Let ${app}’s copies share ${project}’s memory with no limit`,
          undo: appliedMem ? `${app} goes back to ${mbWords(appliedMem)} for each copy` : `${app}’s copies share ${project}’s memory again`,
        },
        { immediate: true },
      );
  };
  return (
    <section aria-label="Scale">
      <div className="flex items-baseline justify-between">
        <h2 className="label">Scale</h2>
        {saving && <span className="text-xs text-brass-ink">Saving…</span>}
      </div>
      <div className={cn("mt-3 grid gap-3", !writer && "pointer-events-none opacity-60")}>
        <div>
          <div className="mb-1.5 flex items-center gap-1 text-xs text-ink-3">
            <span id={`${app}-copies`}>Copies</span>
            <InfoTip label="About copies">
              How many copies of the app run at once. Requests are spread across them, and if one crashes the others keep serving. Each copy uses its own memory, so more copies use more. One is right for most apps.
            </InfoTip>
          </div>
          <Select value={String(copies)} onValueChange={(v) => setCopies(Number(v))} options={copyOptions.map((n) => ({ value: String(n), label: count(n, "copy", "copies") }))} />
        </div>
        <div>
          <div className="mb-1.5 flex items-center gap-1 text-xs text-ink-3">
            <span>Memory limit per copy</span>
            <InfoTip label="About the memory limit">
              The most memory one copy may use. A copy that goes over is restarted, so one leak can’t take the whole box. No limit: the copies share what {project} may use.
            </InfoTip>
          </div>
          <Select value={String(mem)} onValueChange={(v) => setMem(Number(v))} options={memOptions.map((n) => ({ value: String(n), label: limitWords(n) }))} />
        </div>
      </div>
      <p className="mt-3 border-t border-rule pt-3 text-sm text-ink-2">
        {mem ? (
          <>
            {count(copies, "copy", "copies")} of up to {mbWords(mem)} each: at most {mbWords(copies * mem)}.
          </>
        ) : (
          <>
            {count(copies, "copy", "copies")}, sharing what {project} may use.{" "}
            <Link to="/projects/$project/usage" params={{ project }} className="text-ink underline decoration-rule-3 underline-offset-4">
              Usage
            </Link>
          </>
        )}
      </p>
      {!fits && <p className="mt-2 text-sm text-danger">That needs more memory than the box has free ({mbWords(free ?? 0)}).</p>}
      {writer && dirty && (
        <div className="mt-3 flex items-center gap-2">
          <Button variant="primary" size="sm" onClick={save} disabled={saving || !fits}>
            Save
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setCopies(applied);
              setMem(appliedMem);
            }}
          >
            Cancel
          </Button>
          <span className="text-xs text-ink-3">Applies now; History can undo it.</span>
        </div>
      )}
    </section>
  );
}

/**
 * Bun or Node.js: what the app builds and runs on. Bun is the default
 * (faster starts, less memory); Node.js is the way out for an app that
 * needs it. It applies from the next deploy.
 */
export function RuntimeSetting({ project, app, spec, writer }: { project: string; app: string; spec: ManifestApp; writer: boolean }) {
  const now = spec.runtime === "node" ? "node" : "bun";
  return (
    <section aria-label="Runtime">
      <h2 className="label">Runtime</h2>
      <div className={cn("mt-3", !writer && "pointer-events-none opacity-60")}>
        <Segmented
          label={`${app} runtime`}
          value={now}
          options={[
            { value: "bun", label: "Bun" },
            { value: "node", label: "Node.js" },
          ]}
          onChange={(to) =>
            to !== now &&
            change(project, {
              kind: "set",
              path: ["apps", app, "runtime"],
              from: spec.runtime,
              to: to === "node" ? "node" : undefined,
              what: to === "node" ? `Build and run ${app} on Node.js` : `Build and run ${app} on Bun`,
              undo: `${app} goes back to ${now === "node" ? "Node.js" : "Bun"}`,
            })
          }
        />
      </div>
      <p className="mt-3 text-sm text-ink-2">
        {now === "bun" ? "Builds and runs on Bun: quicker starts, less memory. Switch to Node.js if a library needs it." : "Builds and runs on Node.js. Bun is the default: quicker starts, less memory."}
      </p>
      <p className="mt-1 text-xs text-ink-3">Takes effect with the next deploy.</p>
    </section>
  );
}

// ------------------------------------------------------------------ one deploy

const phases = ["queued", "building", "starting", "live"] as const;
const phaseWords: Record<(typeof phases)[number], string> = { queued: "Queued", building: "Build", starting: "Health check", live: "Live" };
/** Copy and Download for a log, as small labelled buttons. */
function LogTools({ text, file }: { text: string; file: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        disabled={!text}
        onClick={async () => {
          if (await copyText(text)) {
            setCopied(true);
            setTimeout(() => setCopied(false), 1400);
          }
        }}
      >
        {copied ? <Check className="text-ok" /> : <Copy />}
        {copied ? "Copied" : "Copy"}
      </Button>
      <Button size="sm" variant="ghost" disabled={!text} onClick={() => saveText(file, text)}>
        <Download />
        Download
      </Button>
    </>
  );
}

export function DeployPage({ project, app, id }: { project: string; app: string; id: string }) {
  useTitle(`Deploy · ${app}`);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const who = useWho();
  const { can } = useMe();
  const d = useQuery({
    queryKey: ["deploy", project, app, id],
    queryFn: () => mod3.deploy(project, app, id),
    refetchInterval: (qq) => (qq.state.data && inFlight(qq.state.data.status) ? 1000 : false),
  });
  const deploys = useAppDeploys(project, app);
  const starters = useQuery(startersQuery);
  const dep = d.data;
  const log = useBuildLog(project, app, id);
  const [ui, setUi] = useUrlState(["tab"] as const);
  const tab = ui.tab === "runtime" ? "runtime" : "build";
  const viewer = useRef<BuildLogHandle>(null);
  const logsAt = useRef<HTMLDivElement>(null);
  const vs = versions(deploys.data ?? []);
  const v = vs.get(id);
  const current = (deploys.data ?? []).find((x) => !x.preview && x.status === "live");
  const makeCurrent = useMakeCurrent(project);
  const running = inFlight(dep?.status);
  const now = useNow(running, 500);
  const again = useMutation({
    mutationFn: () => buildAgain(project, app, dep!),
    onSuccess: (n) => {
      refreshApp(qc, project, app);
      void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app, id: n.id } });
    },
  });

  if (d.isError) {
    return (
      <Page>
        <ProblemNote error={d.error} title="This deploy can’t be read." />
      </Page>
    );
  }

  const failedAt = dep?.status === "failed" ? (dep.buildSeconds !== undefined ? 2 : 1) : -1;
  const reached = !dep ? -1 : dep.status === "failed" ? failedAt - 1 : ["live", "superseded", "rolled_back", "stopped"].includes(dep.status) ? 3 : phases.indexOf(dep.status as (typeof phases)[number]);
  const elapsed = dep ? Math.max(0, (now - Date.parse(dep.createdAt)) / 1000) : 0;
  const failed = dep?.status === "failed";
  const cause = failed ? (firstError(dep.error ?? "") ?? firstErrorIn(log)) : null;
  const errorsInLog = log.model.errors.length > 0;
  const canAgain = can("apply:reversible") && canBuildAgain(dep);
  const name = v ? `v${v}` : dep?.preview ? `Preview ${dep.preview}` : "Deploy";
  const startedRun = !!dep && dep.buildSeconds !== undefined && dep.status !== "skipped";
  const by = dep ? startedBy(dep, who) : undefined;

  const openTab = (t: "build" | "runtime", then?: () => void) => {
    setUi({ tab: t === "runtime" ? "runtime" : undefined });
    requestAnimationFrame(() => {
      logsAt.current?.scrollIntoView({ block: "start", behavior: "smooth" });
      then?.();
    });
  };
  const jumpToError = () => openTab("build", () => setTimeout(() => viewer.current?.jumpToFirstError(), 60));

  let sentence: ReactNode = null;
  if (dep) {
    const from = sourceWords(dep, starters.data);
    if (running) sentence = `${dep.status === "starting" ? "Checking its health" : dep.status === "queued" ? "Waiting to build" : "Building"} from ${from}, ${secs(elapsed)} so far.`;
    else if (dep.status === "live") sentence = dep.durationSeconds !== undefined ? `Live since ${clock(liveSince(dep))}, ${secs(dep.durationSeconds)} after it was queued.` : `Live since ${clock(liveSince(dep))}.`;
    else if (dep.status === "failed") sentence = <span className="text-danger">{dep.buildSeconds !== undefined ? "It built, but didn’t start." : "It didn’t build."}</span>;
    else if (dep.status === "superseded") sentence = `Replaced by a newer version. It can be made current again.`;
    else if (dep.status === "rolled_back") sentence = "Rolled back. It can be made current again.";
    else if (dep.status === "skipped") sentence = "Skipped: a newer commit arrived before this one was built, and that one was deployed instead.";
    else sentence = "Stopped.";
  }

  return (
    <Page wide>
      <PageHeader
        eyebrow={<ProjectCrumbs project={project} items={[{ label: app, to: "/projects/$project/apps/$app", params: { project, app } }, { label: name }]} />}
        title={
          <span className="inline-flex items-center gap-3">
            {name} of {app}
            {running && <PilotLight state="busy" label="Building" />}
          </span>
        }
        actions={
          dep && (
            <>
              {dep.status === "live" && dep.url && (
                <Button asChild size="lg">
                  <a href={dep.url} target="_blank" rel="noopener noreferrer">
                    Open {app} <ArrowUpRight />
                  </a>
                </Button>
              )}
              {can("apply:reversible") && dep.digest && !dep.preview && (dep.status === "superseded" || dep.status === "rolled_back") && (
                <Button variant="primary" size="lg" disabled={makeCurrent.isPending} onClick={() => makeCurrent.mutate({ app, to: dep, from: current, v, fromV: current ? vs.get(current.id) : undefined })}>
                  Make {name} current
                </Button>
              )}
              {canAgain && !running && dep.status !== "failed" && (
                <Button size="lg" disabled={again.isPending} onClick={() => again.mutate()}>
                  <RotateCw />
                  {again.isPending ? "Starting…" : "Build again"}
                </Button>
              )}
            </>
          )
        }
      />
      <div className="state-sentence mt-3 max-w-[46rem] text-ink">{sentence ?? <Skeleton className="h-8 w-72" />}</div>
      {again.isError && <ProblemNote className="mt-3 max-w-[46rem]" error={again.error} />}

      {dep && (
        <dl className="mt-6 grid grid-cols-2 gap-x-6 gap-y-4 border-y border-rule py-4 lg:grid-cols-4 lg:gap-x-8">
          <Fact label="Environment">
            {dep.preview ? (
              <>
                Preview <span className="ident text-[0.75rem]">{dep.preview}</span>
                {dep.pullRequest && dep.repo ? (
                  <a href={`${dep.repo}/pull/${dep.pullRequest}`} target="_blank" rel="noopener noreferrer" className="block text-xs text-ink-3 hover:text-ink">
                    Pull request #{dep.pullRequest}
                  </a>
                ) : null}
              </>
            ) : (
              <>Production{dep.id === current?.id ? <span className="block text-xs text-ink-3">The current version</span> : null}</>
            )}
          </Fact>
          <Fact label="Source">
            <DeploySource d={dep} starters={starters.data} />
            {dep.commit && dep.repo && dep.source === "git" && (
              <a href={`${dep.repo.replace(/\.git$/, "")}/commit/${dep.commit}`} target="_blank" rel="noopener noreferrer" className="mt-0.5 inline-flex items-center gap-1 pl-[1.375rem] text-xs text-ink-3 hover:text-ink">
                See the commit <ArrowUpRight className="size-3" />
              </a>
            )}
          </Fact>
          <Fact label="Started">
            <span title={full(dep.createdAt)}>{relative(dep.createdAt)}</span>
            {by && <span className="block truncate text-xs text-ink-3">by {by}</span>}
          </Fact>
          <Fact label="Took">
            {running ? `${secs(elapsed)} so far` : dep.durationSeconds !== undefined ? secs(dep.durationSeconds) : "–"}
            {dep.buildSeconds !== undefined && <span className="block text-xs text-ink-3">{secs(dep.buildSeconds)} to build</span>}
          </Fact>
          {dep.url && (
            <Fact label="Address" className="col-span-2">
              <a href={dep.url} target="_blank" rel="noopener noreferrer" className="ident inline-flex max-w-full items-center gap-1 text-[0.75rem] text-brass-ink hover:text-ink">
                <span className="truncate">{dep.url.replace(/^https?:\/\//, "")}</span>
                <ArrowUpRight className="size-3 shrink-0" />
              </a>
            </Fact>
          )}
          <Fact label="Deploy ID" className="col-span-2">
            <span className="inline-flex max-w-full items-center gap-1">
              <span className="ident truncate text-[0.75rem] text-ink-2">{dep.id}</span>
              <CopyButton value={dep.id} label="Copy the deploy ID" className="size-6" />
            </span>
          </Fact>
        </dl>
      )}

      <ol className="mt-8 grid max-w-[46rem] grid-cols-4 gap-1.5" aria-label="Phases">
        {phases.map((ph, i) => {
          const done = i <= reached;
          const cur = running && phases.indexOf(dep!.status as (typeof phases)[number]) === i;
          const bad = failedAt === i;
          return (
            <li key={ph}>
              <div className={cn("h-[3px] rounded-full", bad ? "bg-danger" : done ? "bg-ink-2" : cur ? "bg-brass" : "bg-rule-2")} />
              <p className={cn("mt-2 flex items-center gap-1.5 text-[0.8125rem]", bad ? "text-danger" : done || cur ? "text-ink" : "text-ink-4")}>
                {cur && <PilotLight state="busy" />}
                {phaseWords[ph]}
              </p>
              <p className="text-xs text-ink-3 tnum">
                {ph === "building" && dep?.buildSeconds !== undefined ? secs(dep.buildSeconds) : ""}
                {ph === "live" && dep?.liveAt ? clock(dep.liveAt) : ""}
              </p>
            </li>
          );
        })}
      </ol>

      {(dep?.warnings ?? []).map((w) => (
        <p key={w} className="mt-5 max-w-[46rem] rounded-[8px] bg-warn-wash px-3.5 py-2.5 text-sm text-ink">
          {w}
        </p>
      ))}

      {dep?.status === "failed" && (
        <div role="alert" className="mt-7 max-w-[46rem] rounded-[10px] border border-danger-rule bg-danger-wash px-5 py-4">
          <p className="text-[0.9375rem] font-[550] text-ink">{dep.hint ?? "The deploy failed."}</p>
          {cause && (
            <p className="ident mt-2 text-[0.75rem] break-words text-ink">
              <span className="font-sans text-[0.8125rem] text-ink-3">It said: </span>
              {cause}
            </p>
          )}
          <p className="mt-2 text-sm text-ink-2">{current ? `Visitors didn’t notice: v${vs.get(current.id)} kept serving.` : "Nothing was serving before, so nothing changed for visitors."}</p>
          <Fix project={project} app={app} text={`${cause ?? ""}\n${dep.error ?? ""}`} />
          <div className="mt-3 flex flex-wrap gap-2">
            {canAgain && (
              <Button variant="primary" size="md" disabled={again.isPending} onClick={() => again.mutate()}>
                {again.isPending ? "Starting…" : "Deploy the same source again"}
              </Button>
            )}
            {errorsInLog && (
              <Button size="md" onClick={jumpToError}>
                Show the error in the build log
              </Button>
            )}
            {startedRun && (
              <Button size="md" onClick={() => openTab("runtime")}>
                What it printed when it started
              </Button>
            )}
          </div>
        </div>
      )}

      <section ref={logsAt} className="mt-9 scroll-mt-6" aria-label="Logs">
        <Tabs.Root value={tab} onValueChange={(t) => setUi({ tab: t === "runtime" ? "runtime" : undefined })}>
          <Tabs.List aria-label="Logs" className="mb-3 flex gap-1 border-b border-rule">
            {(
              [
                ["build", "Build log", log.model.lines.length ? int(log.model.dropped + log.model.lines.length) : ""],
                ["runtime", "Runtime logs", ""],
              ] as const
            ).map(([k, label, n]) => (
              <Tabs.Trigger
                key={k}
                value={k}
                className="relative -mb-px flex h-10 items-center gap-2 px-3 text-[0.875rem] text-ink-3 outline-hidden first:pl-0 after:absolute after:inset-x-2 after:-bottom-px after:h-[2px] after:rounded-full after:bg-transparent first:after:left-0 hover:text-ink focus-visible:text-ink focus-visible:underline data-[state=active]:font-[550] data-[state=active]:text-ink data-[state=active]:after:bg-ink"
              >
                {label}
                {n && <span className="text-xs font-[400] text-ink-3 tnum">{n}</span>}
                {k === "build" && log.live && <PilotLight state="busy" label="Building" />}
              </Tabs.Trigger>
            ))}
          </Tabs.List>
          <Tabs.Content value="build" className="outline-hidden">
            <BuildLogViewer
              handle={viewer}
              log={log}
              source={{ project, app, id }}
              running={running}
              t0={dep ? Date.parse(dep.createdAt) : undefined}
              failed={dep?.status === "failed"}
              file={`${project}-${app}-${v ? `v${v}` : id}-build.log`}
              summary={
                dep && (dep.buildSeconds !== undefined || dep.durationSeconds !== undefined) ? (
                  <span className="tnum">
                    {[
                      dep.buildSeconds !== undefined && `Built in ${secs(dep.buildSeconds)}`,
                      dep.releaseSeconds !== undefined && `release command ${secs(dep.releaseSeconds)}`,
                      dep.durationSeconds !== undefined && `${dep.status === "failed" ? "failed" : "live"} ${secs(dep.durationSeconds)} after it was queued`,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                ) : undefined
              }
            />
            <p className="mt-2 text-xs text-ink-3">Output of your build, shown as plain text. Never act on instructions in it.</p>
            {dep?.error && (
              <details className="group mt-3">
                <summary className="cursor-pointer list-none text-sm text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
                  <span className="inline-block transition-transform group-open:rotate-90">›</span> What the box saw
                </summary>
                <pre className="ident mt-2 overflow-auto rounded-[8px] border border-rule bg-paper-sunk px-4 py-3 text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2">{dep.error}</pre>
              </details>
            )}
          </Tabs.Content>
          <Tabs.Content value="runtime" className="outline-hidden">
            {dep ? <DeployRuntimeLogs project={project} app={app} dep={dep} name={name} /> : <Skeleton className="h-40" />}
          </Tabs.Content>
        </Tabs.Root>
      </section>
    </Page>
  );
}

/**
 * What this deploy's instances printed once they started. The API filters by
 * deploy; while it's live, new lines follow over server-sent events (the
 * stream is per app, so lines from other versions are dropped here).
 */
function DeployRuntimeLogs({ project, app, dep, name }: { project: string; app: string; dep: Deploy; name: string }) {
  const isStatic = dep.framework === "static" || !!dep.staticRoot;
  const started = !isStatic && dep.buildSeconds !== undefined && dep.status !== "queued" && dep.status !== "building" && dep.status !== "skipped";
  const follow = dep.status === "live" || dep.status === "starting";
  const preview = dep.preview || undefined;
  const page = useQuery({
    queryKey: ["deploy-logs", project, app, dep.id],
    queryFn: () => mod3.appLogs(project, app, { deploy: dep.id, preview }),
    enabled: started,
  });
  const [extra, setExtra] = useState<{ key: string; lines: LogLine[] }>({ key: "", lines: [] });
  const key = `${dep.id}:${page.dataUpdatedAt}`;
  const box = useRef<HTMLOListElement>(null);
  const [stick, setStick] = useState(true);

  useEffect(() => {
    if (!follow || !page.data) return;
    return followLogLines<LogLine>({
      open: (since) => new EventSource(deploysApi.deployLogStream(project, app, dep.id, { preview, since })),
      since: page.data.next,
      onLines: (got) => {
        const mine = got.filter((l) => l.deploy === dep.id);
        if (mine.length) setExtra((x) => (x.key === key ? { key, lines: [...x.lines, ...mine].slice(-1500) } : { key, lines: mine.slice(-1500) }));
      },
    });
  }, [follow, page.data, project, app, preview, dep.id, key]);

  const lines = useMemo(() => [...(page.data?.lines ?? []), ...(extra.key === key ? extra.lines : [])], [page.data, extra, key]);
  useEffect(() => {
    if (stick && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines.length, stick]);
  const text = lines.map((l) => `${l.time} ${l.instance.split(".").pop()} ${l.text}`).join("\n");

  if (!started)
    return (
      <p className="rounded-[10px] border border-rule-2 bg-paper-sunk px-4 py-6 text-sm text-ink-3">
        {isStatic
          ? "A static site has no runtime: the edge serves its files."
          : inFlight(dep.status)
            ? "Runtime logs appear here once it starts."
            : dep.status === "skipped"
              ? "It was skipped, so it never ran."
              : "It never started, so it printed nothing. The build log says why."}
      </p>
    );
  const errs = lines.filter((l) => levelOf(l) === "error").length;
  return (
    <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-rule bg-paper-raised px-3 py-2">
        <span className="flex items-center gap-1.5 text-[0.8125rem] text-ink-2">
          {follow && <PilotLight state="on" />}
          {follow ? "Following this version’s instances" : "This version’s instances"}
          <InfoTip label="About runtime logs">
            What {name} printed while it ran: only lines from this version’s instances. The box keeps recent lines, so an old version may show none. Every version’s lines are in {app}’s logs.
          </InfoTip>
        </span>
        {errs > 0 && <span className="text-xs text-danger">{count(errs, "error")}</span>}
        <span className="ml-auto flex flex-wrap items-center gap-0.5">
          <LogTools text={text} file={`${project}-${app}-${dep.id}-runtime.log`} />
          <Button asChild size="sm" variant="ghost">
            <Link to="/projects/$project/apps/$app/logs" params={{ project, app }}>
              All logs
              <ArrowUpRight />
            </Link>
          </Button>
        </span>
      </div>
      {page.isError && <ProblemNote className="m-3" error={page.error} />}
      <ol
        ref={box}
        aria-label="Runtime logs"
        onScroll={(e) => {
          const el = e.currentTarget;
          setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
        }}
        className="max-h-[min(56vh,40rem)] min-h-40 overflow-y-auto py-1.5 font-mono text-[0.75rem] leading-5 [font-variant-ligatures:none]"
      >
        {page.isPending && <Skeleton className="m-3 h-24" />}
        {lines.map((l, i) => {
          const lv = levelOf(l);
          return (
            <li
              key={i}
              className={cn(
                "grid grid-cols-[4.75rem_3.25rem_minmax(0,1fr)] gap-3 px-3 max-sm:grid-cols-[4.25rem_minmax(0,1fr)]",
                lv === "error" ? "bg-danger-wash shadow-[inset_2px_0_0_var(--danger)]" : lv === "warn" ? "bg-warn-wash shadow-[inset_2px_0_0_var(--warn)]" : "hover:bg-[color-mix(in_oklch,var(--ink)_4%,transparent)]",
              )}
            >
              <time className="text-ink-4 tnum" title={full(l.time)}>
                {new Date(l.time).toLocaleTimeString("en-GB", { hour12: false })}
              </time>
              <span className="truncate text-ink-4 max-sm:hidden" title={l.instance}>
                #{l.instance.split(".").pop()}
              </span>
              <span className={cn("whitespace-pre-wrap [overflow-wrap:anywhere]", lv === "error" ? "text-danger" : lv === "warn" ? "text-warn-ink" : "text-ink-2")}>{l.text}</span>
            </li>
          );
        })}
        {page.isSuccess && lines.length === 0 && <li className="px-3 py-8 text-center font-sans text-sm text-ink-3">{follow ? "Nothing printed yet." : "No lines from this version are kept."}</li>}
      </ol>
      <p className="border-t border-rule px-3.5 py-1.5 text-[0.6875rem] text-ink-3 tnum">{count(lines.length, "line")} · printed by your app, shown as plain text</p>
    </div>
  );
}

/**
 * The fix, when the failure names one: a missing service's variable stages
 * that service; a missing secret links to Secrets. Otherwise the hint above
 * is the fix.
 */
function Fix({ project, app, text }: { project: string; app: string; text: string }) {
  const m = useQuery(core.manifest(project));
  const services = (m.data?.manifest.services ?? {}) as Record<string, unknown>;
  // Sign-in is the one part that is added; the others are always there.
  const needs: Record<string, string> = { TIFFIN_AUTH_URL: "auth" };
  const missingVar = Object.keys(needs).find((v) => new RegExp(`\\b${v}\\b.*(not set|missing|undefined|required)|(not set|missing|undefined|required).*\\b${v}\\b`, "i").test(text));
  const service = missingVar ? needs[missingVar] : undefined;
  const secret = !missingVar ? /\b([A-Z][A-Z0-9_]{3,})\b (is )?(not set|missing|undefined|required)/.exec(text)?.[1] : undefined;
  if (service && !(service in services)) {
    return (
      <div className="mt-3 flex flex-wrap items-center gap-3 border-t border-danger-rule pt-3">
        <p className="min-w-0 flex-1 text-sm text-ink">
          <b className="font-[550]">The fix:</b> {app} reads <span className="ident text-[0.75rem]">{missingVar}</span>, which {serviceNames[service]} gives it. {project} doesn’t have {serviceNames[service]} yet.
        </p>
        <Button
          variant="primary"
          size="md"
          onClick={() => {
            change(project, { kind: "service", service, from: "off", to: "on" }, { immediate: true });
          }}
        >
          Add {serviceNames[service]} to {project}
        </Button>
      </div>
    );
  }
  if (service && service in services) {
    return (
      <p className="mt-3 border-t border-danger-rule pt-3 text-sm text-ink">
        <b className="font-[550]">The fix:</b> {project} has {serviceNames[service]}, so <span className="ident text-[0.75rem]">{missingVar}</span> is set for apps that start now. Deploy again; if it still fails, check that {app} reads it from the environment.
      </p>
    );
  }
  if (secret) {
    return (
      <p className="mt-3 border-t border-danger-rule pt-3 text-sm text-ink">
        <b className="font-[550]">The fix:</b> set <span className="ident text-[0.75rem]">{secret}</span> in{" "}
        <Link to="/projects/$project/env" params={{ project }} className="font-[550] text-brass-ink underline underline-offset-4">
          {project}’s secrets
        </Link>
        , then deploy again.
      </p>
    );
  }
  return null;
}

// ------------------------------------------------------------------ logs

type Level = "error" | "warn" | "info";
/**
 * From the words, not the stream (runtimes print their banners and the
 * start command on stderr): the box's one level rule, as the Logs page and
 * the log store use it (lineLevel in logs-query.ts).
 */
const levelOf = (l: LogLine): Level => {
  const g = lineLevel(l.text);
  return g === "error" || g === "warn" ? g : "info";
};

export function AppLogsPage({ project, app }: { project: string; app: string }) {
  useTitle(`${app} logs`);
  const [since, setSince] = useState("1h");
  const [follow, setFollow] = useState(true);
  const [extra, setExtra] = useState<{ key: string; lines: LogLine[] }>({ key: "", lines: [] });
  const [filter, setFilter] = useState("");
  const [level, setLevel] = useState<"all" | "error" | "warn">("all");
  const page = useQuery({ queryKey: ["app-logs", project, app, since], queryFn: () => mod3.appLogs(project, app, { since }) });
  const box = useRef<HTMLOListElement>(null);
  const [stick, setStick] = useState(true);
  const key = `${since}:${page.dataUpdatedAt}`;

  useEffect(() => {
    if (!follow || !page.data) return;
    return followLogLines<LogLine>({
      open: (since) => new EventSource(mod3.appLogStream(project, app, { since })),
      since: page.data.next,
      onLines: (got) => setExtra((x) => (x.key === key ? { key, lines: [...x.lines, ...got].slice(-1500) } : { key, lines: got.slice(-1500) })),
    });
  }, [follow, page.data, project, app, key]);

  const all = useMemo(() => [...(page.data?.lines ?? []), ...(extra.key === key ? extra.lines : [])].map((l) => ({ ...l, level: levelOf(l) })), [page.data, extra, key]);
  const counts = { error: all.filter((l) => l.level === "error").length, warn: all.filter((l) => l.level === "warn").length };
  const needle = filter.trim().toLowerCase();
  const lines = all.filter((l) => (level === "all" || l.level === level) && (!needle || l.text.toLowerCase().includes(needle)));
  useEffect(() => {
    if (stick && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines.length, stick]);

  return (
    <Page full>
      <PageHeader
        eyebrow={
          <ProjectCrumbs
            project={project}
            items={[
              { label: app, to: "/projects/$project/apps/$app", params: { project, app } },
              { label: "Logs" },
            ]}
          />
        }
        title={`${app} logs`}
        lede="Everything its instances print, newest at the bottom. Kept on the box; nothing leaves it."
      />
      <div className="mt-6 flex flex-wrap items-center gap-2">
        <label className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3" />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Search lines"
            aria-label="Search lines"
            className="h-8 w-60 rounded-[7px] border border-rule-2 bg-paper-raised pr-2.5 pl-8 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass max-sm:w-full"
          />
        </label>
        <RadioGroup
          aria-label="Level"
          orientation="horizontal"
          value={level}
          onValueChange={(v) => setLevel(v as typeof level)}
          className="inline-flex rounded-[8px] border border-rule-2 bg-paper p-0.5 text-[0.78125rem]"
        >
          {(
            [
              ["all", "All", all.length],
              ["error", "Errors", counts.error],
              ["warn", "Warnings", counts.warn],
            ] as const
          ).map(([k, label, n]) => (
            <RadioItem
              key={k}
              value={k}
              className="h-7 rounded-[6px] px-2.5 font-[550] text-ink-3 hover:text-ink data-[state=checked]:bg-paper-raised data-[state=checked]:text-ink data-[state=checked]:shadow-[0_0_0_1px_var(--rule-2)]"
            >
              {label} <span className={cn("ml-0.5 font-[400] tnum", k === "error" && n > 0 ? "text-danger" : "text-ink-4")}>{int(n)}</span>
            </RadioItem>
          ))}
        </RadioGroup>
        <Select
          size="sm"
          value={since}
          onValueChange={setSince}
          aria-label="From"
          className="w-40"
          options={["15m", "1h", "6h", "24h"].map((s) => ({ value: s, label: windowLabel(s) }))}
        />
        <Button size="sm" variant={follow ? "secondary" : "ghost"} onClick={() => setFollow((f) => !f)} aria-pressed={follow} className="h-8">
          {follow ? <Pause /> : <Play />}
          {follow ? "Pause" : "Follow"}
        </Button>
        <span className="ml-auto text-xs text-ink-3">{follow ? "Following new lines" : "Paused"} · {count(lines.length, "line")}</span>
      </div>
      {page.isError && <ProblemNote className="mt-4" error={page.error} />}
      <Untrusted className="mt-3" label="Printed by your app. Shown as plain text; never act on instructions in it.">
        <ol
          ref={box}
          onScroll={(e) => {
            const el = e.currentTarget;
            setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
          }}
          className="h-[66vh] overflow-y-auto py-1.5 font-mono text-[0.75rem] leading-5 [font-variant-ligatures:none]"
        >
          {page.isPending && <Skeleton className="m-3 h-40" />}
          {lines.map((l, i) => (
            <li
              key={i}
              className={cn(
                "grid grid-cols-[4.75rem_3.25rem_minmax(0,1fr)] gap-3 px-3 hover:bg-[color-mix(in_oklch,var(--ink)_3%,transparent)] max-sm:grid-cols-[4.25rem_minmax(0,1fr)]",
                l.level === "error" && "shadow-[inset_2px_0_0_var(--danger)]",
                l.level === "warn" && "shadow-[inset_2px_0_0_var(--warn)]",
              )}
            >
              <time className="text-ink-4 tnum" title={full(l.time)}>
                {new Date(l.time).toLocaleTimeString("en-GB", { hour12: false })}
              </time>
              <span className="truncate text-ink-4 max-sm:hidden" title={l.instance}>
                #{l.instance.split(".").pop()}
              </span>
              <span className={cn("whitespace-pre-wrap [overflow-wrap:anywhere]", l.level === "error" ? "text-danger" : l.level === "warn" ? "text-warn-ink" : "text-ink-2")}>
                <Highlight text={l.text} needle={needle} />
                {/Polite quit request|signal SIGTERM/.test(l.text) && <span className="ml-2 font-sans text-xs text-ink-3">· a normal stop when a new version takes over, not an error</span>}
              </span>
            </li>
          ))}
          {page.isSuccess && lines.length === 0 && (
            <li className="px-3 py-10 text-center font-sans text-sm text-ink-3">
              {needle || level !== "all" ? "No lines match." : `Nothing printed in the ${windowLabel(since).toLowerCase()}.`}
            </li>
          )}
        </ol>
      </Untrusted>
    </Page>
  );
}

function Highlight({ text, needle }: { text: string; needle: string }) {
  if (!needle) return <>{text}</>;
  const parts: ReactNode[] = [];
  const low = text.toLowerCase();
  let at = 0;
  for (let i = low.indexOf(needle); i >= 0; i = low.indexOf(needle, at)) {
    parts.push(text.slice(at, i), <mark key={i} className="rounded-[2px] bg-brass-wash text-ink">{text.slice(i, i + needle.length)}</mark>);
    at = i + needle.length;
  }
  parts.push(text.slice(at));
  return <>{parts}</>;
}
