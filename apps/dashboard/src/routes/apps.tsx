import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ArrowLeft,
  ArrowUpRight,
  Box,
  ChevronRight,
  GitBranch,
  Moon,
  Pause,
  Radio,
  RotateCcw,
  RotateCw,
  Rocket,
  Terminal,
  Trash2,
} from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { q as core } from "@/api/queries";
import { mod3, type Deploy, type EnvStatus, type LogLine } from "@/api/modules";
import { Confirm } from "@/components/confirm";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { mcpCommand } from "@/lib/mcp";
import { NotOnBox, Page, PageHeader, Skeleton, Untrusted } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { useMe, useWho } from "@/lib/me";
import { clock, full, relative, windowLabel } from "@/lib/time";

// ------------------------------------------------------------------ shared

const phases = ["queued", "building", "starting", "live"] as const;

const statusCopy: Record<Deploy["status"], string> = {
  queued: "Queued",
  building: "Building",
  starting: "Starting",
  live: "Live",
  failed: "Failed",
  superseded: "Replaced",
  rolled_back: "Rolled back",
  stopped: "Stopped",
};

function DeployDot({ status, className }: { status: Deploy["status"]; className?: string }) {
  const c =
    status === "live"
      ? "bg-rev"
      : status === "failed"
        ? "bg-irr"
        : status === "queued" || status === "building" || status === "starting"
          ? "animate-pulse bg-brass"
          : "bg-ink-4";
  return <span className={cn("inline-block size-2 shrink-0 rounded-full", c, className)} aria-label={statusCopy[status]} />;
}

const inFlight = (s: Deploy["status"]) => s === "queued" || s === "building" || s === "starting";

function secs(n?: number) {
  if (n === undefined) return "–";
  return n < 60 ? `${n.toFixed(n < 10 ? 1 : 0)} s` : `${Math.floor(n / 60)} min ${Math.round(n % 60)} s`;
}

function useApps(project: string) {
  const p = useQuery(core.project(project));
  return (p.data?.resources ?? []).filter((r) => r.address.startsWith("app/")).map((r) => r.address.slice(4));
}

// ------------------------------------------------------------------ overview

export function AppsPage({ project }: { project: string }) {
  useTitle(`${project} · Apps`);
  const p = useQuery(core.project(project));
  const apps = useApps(project);
  const rts = useQueries({
    queries: apps.map((a) => ({ queryKey: ["runtime", project, a], queryFn: () => mod3.runtime(project, a), refetchInterval: 5000 })),
  });
  if (rts.some((r) => notOnBox(r.error))) return <NotOnBox what="Apps" />;

  return (
    <Page wide>
      <PageHeader
        eyebrow={
          <Link to="/projects/$project" params={{ project }} className="font-mono hover:text-ink">
            {project}
          </Link>
        }
        title="Apps"
        lede="What runs, where it answers, and every deploy that got it there. Deploys build on the box and only switch traffic once the new version is healthy."
      />
      {p.isPending && <Skeleton className="mt-8 h-40" />}
      {p.isSuccess && apps.length === 0 && <HowToDeploy project={project} />}
      <ul className="mt-8 divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60 empty:hidden">
        {apps.map((a, k) => {
          const rt = rts[k]?.data;
          const prod = rt?.production;
          const live = prod?.live;
          const running = (prod?.instances ?? []).filter((i) => i.running).length;
          return (
            <li key={a} className="animate-rise" style={{ animationDelay: `${k * 50}ms` }}>
              <Link
                to="/projects/$project/apps/$app"
                params={{ project, app: a }}
                className="group grid grid-cols-[2rem_minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-4 py-3.5 transition-colors hover:bg-hover/50 sm:px-5"
              >
                <span className="grid size-8 place-items-center rounded-lg bg-hover text-ink-2">
                  {rt?.role === "worker" ? <Rocket className="size-4" /> : <Box className="size-4" />}
                </span>
                <span className="min-w-0">
                  <span className="flex flex-wrap items-baseline gap-x-2">
                    <span className="text-md font-medium text-ink">{a}</span>
                    <span className="text-sm text-ink-3">
                      {rt?.framework ?? "…"} · {rt?.role === "worker" ? "worker, no public routes" : "web"}
                    </span>
                  </span>
                  <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-ink-3">
                    {live ? (
                      <>
                        <DeployDot status={live.status} />
                        <span className="text-ink-2">live {relative(live.liveAt ?? live.createdAt)}</span>
                        {live.commit && <code className="font-mono text-xs">{live.commit.slice(0, 7)}</code>}
                        {rt?.framework !== "static" && (
                          <span>
                            · {running} of {(prod?.instances ?? []).length} {(prod?.instances ?? []).length === 1 ? "instance" : "instances"} running
                          </span>
                        )}
                        {(rt?.previews ?? []).length > 0 && <span>· {(rt?.previews ?? []).length} preview</span>}
                      </>
                    ) : rt ? (
                      <span>Not deployed yet</span>
                    ) : (
                      <Skeleton className="h-4 w-48" />
                    )}
                  </span>
                </span>
                <span className="flex items-center gap-3">
                  {prod?.url && <span className="hidden font-mono text-xs text-ink-3 md:block">{prod.url.replace(/^https?:\/\//, "")}</span>}
                  <ChevronRight className="size-4 text-ink-4 transition-transform group-hover:translate-x-0.5" />
                </span>
              </Link>
            </li>
          );
        })}
      </ul>
      {apps.length > 0 && <HowToDeploy project={project} compact />}
    </Page>
  );
}

function HowToDeploy({ project, compact }: { project: string; compact?: boolean }) {
  const git = useQuery({ queryKey: ["git", project], queryFn: () => mod3.git(project), staleTime: Infinity, retry: false });
  return (
    <section className={cn(compact ? "mt-12" : "mt-10")} aria-labelledby="how">
      <h2 id="how" className="display-italic mb-1 text-xl text-ink">
        {compact ? "Ship a new version" : "Nothing deployed yet"}
      </h2>
      <p className="mb-4 text-sm text-ink-3">
        Three ways in. Each builds on the box and switches traffic only when the new version answers its health check.
      </p>
      <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
        <Way icon={<Terminal />} title="From this folder">
          <Command cmd="tiffin deploy" />
        </Way>
        <Way icon={<GitBranch />} title="With git" hint="adds a remote called tiffin">
          <Command cmd="tiffin git-remote --add" />
          <Command className="mt-2" cmd="git push tiffin main" />
          {git.data?.url && <p className="mt-2 font-mono text-xs break-all text-ink-3">{git.data.url}</p>}
        </Way>
        <Way icon={<Radio />} title="Let an agent do it" hint="then ask it to deploy">
          <Command cmd={mcpCommand()} wrap />
        </Way>
      </ul>
    </section>
  );
}

function Way({ icon, title, hint, children }: { icon: ReactNode; title: string; hint?: string; children: ReactNode }) {
  return (
    <li className="grid gap-x-6 gap-y-2 px-4 py-4 sm:grid-cols-[12rem_minmax(0,1fr)] sm:px-5">
      <div className="min-w-0">
        <p className="flex items-center gap-2 text-sm font-medium text-ink [&_svg]:size-4 [&_svg]:text-ink-3">
          {icon}
          {title}
        </p>
        {hint && <p className="mt-1 text-xs break-all text-ink-3 sm:pl-6">{hint}</p>}
      </div>
      <div className="min-w-0">{children}</div>
    </li>
  );
}

// ------------------------------------------------------------------ one app

export function AppPage({ project, app }: { project: string; app: string }) {
  useTitle(`${app} · ${project}`);
  const qc = useQueryClient();
  const { can } = useMe();
  const who = useWho();
  const rt = useQuery({ queryKey: ["runtime", project, app], queryFn: () => mod3.runtime(project, app), refetchInterval: 4000 });
  const deploys = useQuery({
    queryKey: ["deploys", project, app],
    queryFn: () => mod3.deploys(project, app),
    refetchInterval: (q) => ((q.state.data ?? []).some((d) => inFlight(d.status)) ? 2000 : 10_000),
  });
  const [rollTo, setRollTo] = useState<Deploy | null>(null);
  const [restarting, setRestarting] = useState(false);
  const [deletePreview, setDeletePreview] = useState<string | null>(null);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["runtime", project, app] });
    qc.invalidateQueries({ queryKey: ["deploys", project, app] });
  };
  const sleep = useMutation({ mutationFn: (name: string) => mod3.sleepPreview(project, app, name), onSuccess: refresh });

  if (rt.isError && notOnBox(rt.error)) return <NotOnBox what="Apps" />;
  const r = rt.data;
  const prod = r?.production;
  const list = deploys.data ?? [];
  const writer = can("apply:reversible");

  return (
    <Page wide>
      <Link to="/projects/$project/apps" params={{ project }} className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink">
        <ArrowLeft className="size-3.5" /> Apps
      </Link>
      <header className="mt-5 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <p className="text-sm text-ink-3">
            {r ? `${r.framework} · ${r.role}` : "…"}
            {(r?.routes ?? []).length > 0 && ` · routes ${(r?.routes ?? []).join(", ")}`}
          </p>
          <h1 className="display mt-1 text-3xl text-ink">{app}</h1>
          {prod?.url && (
            <a
              href={prod.url}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-1 inline-flex items-center gap-1 font-mono text-sm text-brass-ink hover:text-ink"
            >
              {prod.url.replace(/^https?:\/\//, "")}
              <ArrowUpRight className="size-3.5" />
            </a>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button asChild>
            <Link to="/projects/$project/apps/$app/logs" params={{ project, app }}>
              <Terminal />
              Logs
            </Link>
          </Button>
          {writer && r?.framework !== "static" && prod?.live && (
            <Button variant="ghost" onClick={() => setRestarting(true)}>
              <RotateCw />
              Restart
            </Button>
          )}
        </div>
      </header>
      {r?.hint && <p className="mt-4 rounded-lg border border-out/40 bg-out-wash px-4 py-2.5 text-sm text-ink">{r.hint}</p>}

      {prod && <EnvCard env={prod} title="Production" static={r?.framework === "static"} />}

      <section className="mt-10" aria-labelledby="deploys">
        <h2 id="deploys" className="display-italic mb-3 text-xl text-ink">
          Deploys
        </h2>
        {deploys.isPending && <Skeleton className="h-32" />}
        {deploys.isSuccess && list.length === 0 && <HowToDeploy project={project} />}
        <ol className="relative">
          {list.map((d, k) => (
            <li key={d.id} className="group relative grid grid-cols-[1.25rem_minmax(0,1fr)] gap-x-3 pb-1">
              {k < list.length - 1 && <span aria-hidden className="absolute top-5 bottom-0 left-[0.55rem] w-px bg-rule" />}
              <span className="relative z-[1] mt-[1.15rem] flex justify-center">
                <DeployDot status={d.status} className="ring-4 ring-paper" />
              </span>
              <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-lg px-2 py-2.5 hover:bg-hover/50">
                <Link to="/projects/$project/apps/$app/deploys/$id" params={{ project, app, id: d.id }} className="min-w-0 flex-1">
                  <span className="flex flex-wrap items-baseline gap-x-2">
                    <span className={cn("text-base font-medium", d.status === "failed" ? "text-irr" : "text-ink")}>{statusCopy[d.status]}</span>
                    <span className="text-sm text-ink-3" title={full(d.createdAt)}>
                      {relative(d.createdAt)}
                      {d.createdBy ? ` by ${who(d.createdBy)}` : ""} · from {d.source}
                      {d.commit ? ` ${d.commit.slice(0, 7)}` : ""}
                    </span>
                  </span>
                  <span className="mt-0.5 block truncate text-xs text-ink-3">
                    {d.status === "failed"
                      ? (d.hint ?? d.error?.split("\n")[0])
                      : `built in ${secs(d.buildSeconds)} · live after ${secs(d.durationSeconds)}`}
                    <span className="font-mono text-ink-4"> · {d.id}</span>
                  </span>
                </Link>
                {writer && (d.status === "superseded" || d.status === "rolled_back") && d.digest && (
                  <Button
                    size="sm"
                    variant="ghost"
                    className="opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
                    onClick={() => setRollTo(d)}
                  >
                    <RotateCcw />
                    Roll back to this
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ol>
      </section>

      {(r?.previews ?? []).length > 0 && (
        <section className="mt-10" aria-labelledby="previews">
          <h2 id="previews" className="display-italic mb-1 text-xl text-ink">
            Previews
          </h2>
          <p className="mb-3 text-sm text-ink-3">Separate copies at their own address. Idle previews go to sleep and wake on the next visit.</p>
          <ul className="divide-y divide-rule overflow-hidden rounded-xl border border-rule bg-raised/60">
            {(r?.previews ?? []).map((pv) => (
              <li key={pv.preview} className="flex flex-wrap items-center gap-3 px-4 py-3">
                {pv.sleeping ? <Moon className="size-4 text-ink-3" /> : <span className="size-2 rounded-full bg-rev" />}
                <span className="min-w-0 flex-1">
                  <span className="font-mono text-[0.8125rem] text-ink">{pv.preview}</span>
                  <span className="ml-2 text-sm text-ink-3">
                    {pv.sleeping ? "asleep" : "awake"} · updated {relative(pv.updatedAt)}
                  </span>
                  {pv.url && (
                    <a
                      href={pv.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="block truncate font-mono text-xs text-brass-ink hover:text-ink"
                    >
                      {pv.url.replace(/^https?:\/\//, "")}
                    </a>
                  )}
                </span>
                {writer && !pv.sleeping && (
                  <Button size="sm" variant="ghost" onClick={() => sleep.mutate(pv.preview!)}>
                    <Pause />
                    Sleep
                  </Button>
                )}
                {writer && (
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`Delete preview ${pv.preview}`}
                    onClick={() => setDeletePreview(pv.preview!)}
                    className="hover:text-irr"
                  >
                    <Trash2 />
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      <Confirm
        open={!!rollTo}
        onClose={() => setRollTo(null)}
        title="Roll back to this deploy?"
        body={
          rollTo
            ? `${app} goes back to the version from ${relative(rollTo.createdAt)}. It starts the old image, waits for health, then switches traffic; nothing is rebuilt.`
            : ""
        }
        action="Roll back"
        tone="normal"
        run={() => mod3.rollback(project, app, rollTo!.id)}
        done={refresh}
      />
      <Confirm
        open={restarting}
        onClose={() => setRestarting(false)}
        title={`Restart ${app}?`}
        body="Instances restart one at a time, so the app keeps answering."
        action="Restart"
        tone="normal"
        run={() => mod3.restart(project, app)}
        done={refresh}
      />
      <Confirm
        open={!!deletePreview}
        onClose={() => setDeletePreview(null)}
        title={`Delete preview ${deletePreview}?`}
        body="Its instances stop and its address stops answering. Production isn't touched."
        action="Delete preview"
        run={() => mod3.deletePreview(project, app, deletePreview!)}
        done={refresh}
      />
    </Page>
  );
}

function EnvCard({ env, title, static: isStatic }: { env: EnvStatus; title: string; static?: boolean }) {
  const inst = env.instances ?? [];
  return (
    <section className="mt-8 rounded-xl border border-rule bg-raised/60 p-5" aria-label={title}>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-sm font-medium text-ink-2">{title}</h2>
        <span className="text-xs text-ink-3">updated {relative(env.updatedAt)}</span>
      </div>
      {env.live ? (
        <p className="mt-2 flex flex-wrap items-center gap-x-2 text-base text-ink">
          <DeployDot status={env.live.status} />
          {statusCopy[env.live.status]} since {relative(env.live.liveAt ?? env.live.createdAt)}
          <span className="font-mono text-xs text-ink-3">{env.live.id}</span>
          {env.live.digest && <span className="font-mono text-xs text-ink-4">{env.live.digest.slice(0, 19)}</span>}
        </p>
      ) : (
        <p className="mt-2 text-base text-ink-3">
          {env.stopped ? "Stopped." : env.sleeping ? "Asleep: wakes on the next visit." : "Nothing live yet."}
        </p>
      )}
      {!isStatic && inst.length > 0 && (
        <ul className="mt-4 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {inst.map((i) => (
            <li key={i.name} className="flex items-center gap-2.5 rounded-lg border border-rule bg-paper px-3 py-2">
              <span className={cn("size-2 rounded-full", i.running ? "bg-rev" : "bg-irr")} />
              <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink">{i.name.split(".").slice(-2).join(".")}</span>
              <span className="font-mono text-xs text-ink-3">:{i.port}</span>
              <span className="text-xs text-ink-3">{i.state}</span>
            </li>
          ))}
        </ul>
      )}
      {isStatic && <p className="mt-2 text-sm text-ink-3">A static site: the edge serves its files directly, no instances to run.</p>}
      {(env.draining ?? []).map((dset) => (
        <p key={dset.release} className="mt-3 text-sm text-ink-3">
          Draining {dset.instances?.length ?? 0} old {dset.instances?.length === 1 ? "instance" : "instances"} of {dset.release} since{" "}
          {relative(dset.since)}.
        </p>
      ))}
    </section>
  );
}

// ------------------------------------------------------------------ one deploy

export function DeployPage({ project, app, id }: { project: string; app: string; id: string }) {
  useTitle(`Deploy · ${app}`);
  const qc = useQueryClient();
  const who = useWho();
  const d = useQuery({
    queryKey: ["deploy", project, app, id],
    queryFn: () => mod3.deploy(project, app, id),
    refetchInterval: (q) => (q.state.data && inFlight(q.state.data.status) ? 1500 : false),
  });
  const [log, setLog] = useState("");
  const [live, setLive] = useState(false);
  const box = useRef<HTMLPreElement>(null);
  const stick = useRef(true);
  const status = d.data?.status;
  const running = !!status && inFlight(status);

  // Read the log so far, then follow it over server-sent events while the deploy runs.
  useEffect(() => {
    let es: EventSource | null = null;
    let cancelled = false;
    mod3
      .buildLog(project, app, id)
      .then((b) => {
        if (cancelled) return;
        setLog(b.text);
        if (b.done) return;
        es = new EventSource(mod3.buildLogStream(project, app, id, b.offset));
        es.onopen = () => setLive(true);
        es.addEventListener("log", (e) => {
          const m = JSON.parse((e as MessageEvent).data) as { text: string };
          setLog((t) => t + m.text);
        });
        es.addEventListener("done", () => {
          setLive(false);
          es?.close();
          qc.invalidateQueries({ queryKey: ["deploy", project, app, id] });
          qc.invalidateQueries({ queryKey: ["deploys", project, app] });
        });
        es.onerror = () => setLive(false);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      es?.close();
    };
  }, [project, app, id, qc]);

  useEffect(() => {
    if (stick.current && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [log]);

  if (d.isError) return <ProblemNote className="m-8" error={d.error} />;
  const dep = d.data;
  const reached = dep
    ? dep.status === "failed"
      ? -1
      : dep.status === "live" || dep.status === "superseded" || dep.status === "rolled_back"
        ? 3
        : phases.indexOf(dep.status as (typeof phases)[number])
    : 0;
  const failedAt = dep?.status === "failed" ? (dep.buildSeconds ? 2 : 1) : -1;

  return (
    <Page wide>
      <Link
        to="/projects/$project/apps/$app"
        params={{ project, app }}
        className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink"
      >
        <ArrowLeft className="size-3.5" /> {app}
      </Link>
      <header className="mt-5">
        <h1 className="display text-3xl text-ink">{dep ? (running ? `${statusCopy[dep.status]}…` : statusCopy[dep.status]) : "Deploy"}</h1>
        {dep && (
          <p className="mt-1 text-sm text-ink-3">
            <span className="font-mono">{dep.id}</span> · {relative(dep.createdAt)}
            {dep.createdBy ? ` by ${who(dep.createdBy)}` : ""} · {dep.source}
            {dep.preview ? ` · preview ${dep.preview}` : ""}
          </p>
        )}
      </header>

      {/* The four phases; the current one breathes while it runs. */}
      <ol className="mt-8 grid grid-cols-4 gap-2" aria-label="Phases">
        {phases.map((ph, i) => {
          const done = reached >= i && failedAt < 0;
          const current = running && phases.indexOf(dep!.status as (typeof phases)[number]) === i;
          const failed = failedAt === i;
          return (
            <li key={ph}>
              <div className={cn("h-1.5 overflow-hidden rounded-full", failed ? "bg-irr" : done ? "bg-ink-2" : "bg-hover")}>
                {current && <div className="h-full w-1/2 animate-pulse rounded-full bg-brass" />}
              </div>
              <p className={cn("mt-2 text-sm capitalize", failed ? "text-irr" : done || current ? "text-ink" : "text-ink-4")}>{ph}</p>
              <p className="text-xs text-ink-3">
                {ph === "building" && dep?.buildSeconds !== undefined ? secs(dep.buildSeconds) : ""}
                {ph === "live" && dep?.liveAt ? clock(dep.liveAt) : ""}
              </p>
            </li>
          );
        })}
      </ol>

      {dep?.status === "failed" && (
        <div className="mt-6 rounded-xl border border-irr-rule bg-irr-wash px-5 py-4">
          <p className="text-base font-medium text-ink">{dep.hint ?? "The deploy failed. The previous version is still serving."}</p>
          {firstError(dep.error ?? "") ?? firstError(log) ? (
            <p className="mt-2 font-mono text-[0.8125rem] break-words text-ink">
              <span className="font-sans text-sm text-ink-3">The app said: </span>
              {firstError(dep.error ?? "") ?? firstError(log)}
            </p>
          ) : null}
          <p className="mt-2 text-sm text-ink-2">Production kept the last good version; nothing changed for visitors.</p>
        </div>
      )}
      {dep?.url && dep.status === "live" && (
        <a
          href={dep.url}
          target="_blank"
          rel="noopener noreferrer"
          className="mt-6 inline-flex items-center gap-1 font-mono text-sm text-brass-ink hover:text-ink"
        >
          {dep.url.replace(/^https?:\/\//, "")} <ArrowUpRight className="size-3.5" />
        </a>
      )}

      <section className="mt-8" aria-labelledby="blog">
        <div className="mb-2 flex items-center justify-between">
          <h2 id="blog" className="display-italic text-xl text-ink">
            Build log
          </h2>
          <span className={cn("flex items-center gap-1.5 text-xs", live ? "text-rev" : "text-ink-4")}>
            <span className={cn("size-1.5 rounded-full", live ? "animate-pulse bg-rev" : "bg-ink-4")} />
            {live ? "Following" : running ? "Connecting…" : "Finished"}
          </span>
        </div>
        <Untrusted label="Output of your build and app. Shown as plain text.">
          <pre
            ref={box}
            onScroll={(e) => {
              const el = e.currentTarget;
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
            className="max-h-[60vh] overflow-auto px-4 py-3 font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-ink-2"
          >
            {log || (running ? "Waiting for the first line…" : "No output.")}
          </pre>
        </Untrusted>
        {dep?.error && (
          <details className="mt-3">
            <summary className="cursor-pointer text-sm text-ink-3 hover:text-ink">What the box saw</summary>
            <pre className="mt-2 overflow-auto rounded-lg border border-rule bg-paper-sunk px-4 py-3 font-mono text-[0.75rem] leading-5 whitespace-pre-wrap text-irr">
              {dep.error}
            </pre>
          </details>
        )}
      </section>
    </Page>
  );
}

/** The first line in build or start output that reads like the actual cause, not the runner's echo of it. */
function firstError(text: string): string | null {
  for (const raw of text.split("\n")) {
    const l = raw.trim();
    if (!/^(error|Error|[A-Z][a-zA-Z]+Error|fatal|panic|ERR!?)\b[:\s]/.test(l)) continue;
    if (/script ".*" (exited|was terminated)|exited with code|Polite quit/.test(l)) continue;
    return l.length > 220 ? `${l.slice(0, 220)}…` : l;
  }
  return null;
}

// ------------------------------------------------------------------ logs

export function AppLogsPage({ project, app }: { project: string; app: string }) {
  useTitle(`${app} logs`);
  const [since, setSince] = useState("1h");
  const [follow, setFollow] = useState(true);
  const [extra, setExtra] = useState<LogLine[]>([]);
  const [filter, setFilter] = useState("");
  const page = useQuery({ queryKey: ["app-logs", project, app, since], queryFn: () => mod3.appLogs(project, app, { since }) });
  const box = useRef<HTMLOListElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    if (!follow || !page.data) return;
    const es = new EventSource(mod3.appLogStream(project, app, { since: page.data.next }));
    es.addEventListener("log", (e) => {
      const l = JSON.parse((e as MessageEvent).data) as LogLine;
      setExtra((x) => [...x.slice(-1500), l]);
    });
    return () => es.close();
  }, [follow, page.data, project, app]);

  const lines = [...(page.data?.lines ?? []), ...extra].filter((l) => !filter || l.text.toLowerCase().includes(filter.toLowerCase()));
  useEffect(() => {
    if (stick.current && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines.length]);

  return (
    <Page full>
      <Link
        to="/projects/$project/apps/$app"
        params={{ project, app }}
        className="inline-flex items-center gap-1.5 text-sm text-ink-3 hover:text-ink"
      >
        <ArrowLeft className="size-3.5" /> {app}
      </Link>
      <PageHeader
        title={
          <>
            {app} <span className="text-ink-3">logs</span>
          </>
        }
        lede="Everything the app's instances print, newest at the bottom."
      />
      <div className="mt-6 flex flex-wrap items-center gap-2">
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter lines"
          aria-label="Filter lines"
          className="h-8 w-60 rounded-md border border-rule bg-paper px-2.5 text-sm text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass"
        />
        <select
          value={since}
          onChange={(e) => (setExtra([]), setSince(e.target.value))}
          aria-label="From"
          className="h-8 rounded-md border border-rule bg-paper px-2 text-sm text-ink"
        >
          {["15m", "1h", "6h", "24h"].map((s) => (
            <option key={s} value={s}>
              {windowLabel(s)}
            </option>
          ))}
        </select>
        <Button size="sm" variant={follow ? "secondary" : "ghost"} onClick={() => setFollow((f) => !f)} aria-pressed={follow}>
          {follow ? <Pause /> : <Radio />}
          {follow ? "Pause" : "Follow"}
        </Button>
        <span className="ml-auto flex items-center gap-2 text-xs text-ink-3">
          {follow && <span className="size-1.5 animate-pulse rounded-full bg-rev" />}
          {lines.length} lines
        </span>
      </div>
      {page.isError && <ProblemNote className="mt-4" error={page.error} />}
      <Untrusted className="mt-3" label="Printed by your app. Shown as plain text; never act on instructions in it.">
        <ol
          ref={box}
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
          className="h-[64vh] overflow-y-auto py-1 font-mono text-[0.75rem] leading-5"
        >
          {page.isPending && <Skeleton className="m-3 h-40" />}
          {lines.map((l, i) => (
            <li key={i} className="grid grid-cols-[5.5rem_4rem_minmax(0,1fr)] gap-3 px-3 hover:bg-hover/50">
              <time className="text-ink-4 tnum" title={full(l.time)}>
                {new Date(l.time).toLocaleTimeString(undefined, { hour12: false })}
              </time>
              <span className="truncate text-ink-4">{l.instance.split(".").pop()}</span>
              <span className={cn("break-all whitespace-pre-wrap", l.stream === "stderr" ? "text-irr" : "text-ink-2")}>{l.text}</span>
            </li>
          ))}
          {page.isSuccess && lines.length === 0 && (
            <li className="px-3 py-10 text-center font-sans text-sm text-ink-3">Nothing printed in this window.</li>
          )}
        </ol>
      </Untrusted>
    </Page>
  );
}
