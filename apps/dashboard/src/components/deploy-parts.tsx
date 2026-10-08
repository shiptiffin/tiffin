import { queryOptions, useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowUpRight, Copy, FileCode2, FileText, GitBranch, GitCommitHorizontal, LayoutTemplate, MoreHorizontal, Package, RotateCw, Undo2, Upload } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { request } from "@/api/client";
import { deploysApi, mod3, type Deploy } from "@/api/modules";
import type { components } from "@/api/schema";
import { Command } from "@/components/copy";
import { GitHubMark } from "@/components/github-mark";
import { PilotLight, type PilotState } from "@/components/pilot";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { dec, NNBSP, withUnit } from "@/lib/format";
import { deployGitHub } from "@/lib/github";
import { mcpCommand } from "@/lib/mcp";
import { deployGit, deployTemplate, frameworkName } from "@/lib/starters";
import { full, liveSince, relative } from "@/lib/time";

// Shared by the Deployments page, an app's page, a deploy's page and the project overview.

export const inFlight = (s?: Deploy["status"]) => s === "queued" || s === "building" || s === "starting";

/** "4.2 s", "38 s", "2 min 5 s". */
export const secs = (n?: number) =>
  n === undefined ? "–" : n < 60 ? withUnit(dec(n, n < 10 ? 1 : 0), "s") : `${Math.floor(n / 60)}${NNBSP}min ${Math.round(n % 60)}${NNBSP}s`;

export const statusWord: Record<Deploy["status"], string> = {
  queued: "Queued",
  building: "Building",
  starting: "Starting",
  live: "Live",
  failed: "Failed",
  superseded: "Replaced",
  rolled_back: "Rolled back",
  stopped: "Stopped",
  skipped: "Skipped",
};

export const pilotOf = (s: Deploy["status"]): PilotState => (inFlight(s) ? "busy" : s === "failed" ? "fault" : s === "live" ? "on" : "off");

/** Production deploys oldest first get v1, v2…: the number people say ("roll back to v11"). */
export function versions(list: Deploy[]): Map<string, number> {
  const prod = list.filter((d) => !d.preview).sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  // The box numbers versions itself; an older box doesn't, so count them here.
  return new Map(prod.map((d, i) => [d.id, d.version ?? i + 1]));
}

/** One line for where a deploy came from, for sentences. */
export function sourceWords(d: Deploy, starters?: Array<{ id: string; name: string }>) {
  if (d.source === "template") return `the ${starters?.find((s) => s.id === d.template)?.name ?? d.template} starter`;
  if (d.source === "git" && d.message) return `“${d.message}” · ${d.commit?.slice(0, 7) ?? d.ref ?? ""}${d.pullRequest ? ` · pull request #${d.pullRequest}` : ""}`;
  if (d.source === "git") {
    const repo = repoShort(d.repo);
    return `${repo ?? "git"}${d.commit ? ` @ ${d.commit.slice(0, 7)}` : d.ref ? ` @ ${d.ref}` : ""}`;
  }
  if (d.source === "prebuilt") return "a prebuilt image";
  if (d.source === "files") return "files sent by an agent";
  return d.commit ? `a git push @ ${d.commit.slice(0, 7)}` : "an upload from the CLI";
}

const API_FRAMEWORKS = new Set(["hono", "fastapi", "express", "fastify", "elysia", "koa"]);
const PAGE_FRAMEWORKS = new Set(["next", "astro", "remix", "nuxt", "sveltekit", "vite", "react-router"]);

/** What an app is, by role and framework: "Web app · Next.js", "API · Hono", "Background worker · Bun", "Static site". */
export function appKind(framework?: string, role?: string): string {
  if (framework === "static") return "Static site";
  const fw = frameworkName(framework);
  if (role === "worker") return `Background worker · ${fw}`;
  if (framework && API_FRAMEWORKS.has(framework)) return `API · ${fw}`;
  if (framework && PAGE_FRAMEWORKS.has(framework)) return `Web app · ${fw}`;
  return `Web server · ${fw}`;
}

export const repoShort = (url?: string) => url?.replace(/^https?:\/\/(www\.)?/, "").replace(/\.git$/, "");

/** Who started it: the commit's author for GitHub, else the person or key behind the token. */
export function startedBy(d: Deploy, who: (id?: string) => string): string | undefined {
  if (d.createdBy?.startsWith("github:")) return d.author ?? d.createdBy.slice(7);
  return d.createdBy ? who(d.createdBy) : undefined;
}

/** How a deploy was started: GitHub, the CLI, a git push to the box, an agent, the dashboard. */
export function viaWords(d: Deploy): string {
  if (d.trigger === "push") return "GitHub push";
  if (d.trigger === "pull_request") return "pull request";
  if (d.trigger === "redeploy") return "redeploy";
  if (d.trigger === "env") return "settings change";
  if (d.source === "upload") return d.commit ? "git push" : "CLI";
  if (d.source === "files") return "agent";
  if (d.source === "prebuilt") return "CLI, prebuilt";
  return "dashboard";
}

/** Every deploy of one app, production and previews (the API's most, 200), polled fast while one builds. */
export const allDeploysQuery = (project: string, app: string) =>
  queryOptions({
    queryKey: ["deploys", project, app, "all"],
    queryFn: () =>
      request<components["schemas"]["RuntimeDeployList"]>("GET", `/v1/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/deploys?all=true&limit=200`).then(
        (r) => r.deploys ?? [],
      ),
    refetchInterval: (qq) => ((qq.state.data ?? []).some((d) => inFlight(d.status)) ? 1500 : 10_000),
  });

export function useAppDeploys(project: string, app: string) {
  return useQuery(allDeploysQuery(project, app));
}

/** What the project's deploy list narrows to on the box (kept in the page's address). */
export type DeployListFilter = { app?: string; env?: "production" | "preview"; status?: string; branch?: string };

const hasInFlight = (pages?: Array<{ deploys: Deploy[] }>) => (pages ?? []).some((pg) => pg.deploys.some((d) => inFlight(d.status)));

/** One filtered list of a project's deploys, newest first, 50 a page (the box pages it with a cursor). */
function useDeployPages(project: string, filter: DeployListFilter, enabled = true) {
  return useInfiniteQuery({
    queryKey: ["project-deploys", project, "list", filter],
    queryFn: ({ pageParam }) => deploysApi.projectDeploys(project, { ...filter, cursor: pageParam || undefined, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
    retry: false,
    enabled,
    refetchInterval: (qq) => (hasInFlight(qq.state.data?.pages) ? 1500 : 10_000),
  });
}

/**
 * Every deploy of every app, newest first, from the box's project-wide list:
 * filtered on the box, paged with "Show more". The apps at a glance (each
 * one's newest deploys and live version) come from the unfiltered newest
 * page and the live deploys, whatever the filter.
 */
export function useProjectDeploys(project: string, apps: string[], filter: DeployListFilter = {}) {
  const filtered = Object.values(filter).some(Boolean);
  const list = useDeployPages(project, filter);
  const recent = useDeployPages(project, {}, filtered); // the same query as the list when nothing is filtered
  const liveQ = useQuery({
    queryKey: ["project-deploys", project, "live"],
    queryFn: () => deploysApi.projectDeploys(project, { env: "production", status: "live", limit: 200 }),
    refetchInterval: 10_000,
  });
  const rows = (list.data?.pages ?? []).flatMap((pg) => pg.deploys);
  const newest = filtered ? (recent.data?.pages[0]?.deploys ?? []) : rows;
  const live = new Map<string, Deploy>();
  for (const d of [...(liveQ.data?.deploys ?? []), ...newest]) if (!d.preview && d.status === "live" && !live.has(d.app)) live.set(d.app, d);
  const byApp = new Map(apps.map((a) => [a, new Map<string, number>()]));
  for (const d of [...rows, ...newest, ...live.values()]) if (!d.preview && d.version) byApp.get(d.app)?.set(d.id, d.version);
  return {
    rows,
    byApp,
    /** Each app's live production deploy. */
    live,
    /** An app's newest deploys, whatever the filter. */
    of: (app: string) => newest.filter((d) => d.app === app),
    pending: apps.length > 0 && list.isPending,
    error: list.isError ? list.error : undefined,
    more: !!list.hasNextPage,
    loadingMore: list.isFetchingNextPage,
    loadMore: () => void list.fetchNextPage(),
  };
}

/**
 * Page state kept in the address (filters, the open tab), so a link or a
 * reload shows the same view. Unset values leave the address.
 */
export function useUrlState<K extends string>(keys: readonly K[]): [Partial<Record<K, string>>, (patch: Partial<Record<K, string | undefined>>) => void] {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const navigate = useNavigate();
  const now: Partial<Record<K, string>> = {};
  for (const k of keys) if (search[k] !== undefined && search[k] !== "") now[k] = String(search[k]);
  const set = (patch: Partial<Record<K, string | undefined>>) =>
    void navigate({
      to: ".",
      replace: true,
      search: ((prev: Record<string, unknown>) => {
        const next: Record<string, unknown> = { ...prev, ...patch };
        for (const k of Object.keys(patch)) if (next[k] === undefined || next[k] === "") delete next[k];
        return next;
      }) as never,
    });
  return [now, set];
}

/** A clock for "38 s so far" that ticks only while something runs. */
export function useNow(active: boolean, every = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [active, every]);
  return now;
}

export function refreshApp(qc: QueryClient, project: string, app: string) {
  void qc.invalidateQueries({ queryKey: ["runtime", project, app] });
  void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
  void qc.invalidateQueries({ queryKey: ["project-deploys", project] });
}

/** Rollback applies at once (it's reversible); the toast offers the way back. */
export function useMakeCurrent(project: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ app, to }: { app: string; to: Deploy; from?: Deploy; v?: number; fromV?: number }) => mod3.rollback(project, app, to.id),
    onSuccess: (_d, { app, from, v, fromV }) => {
      refreshApp(qc, project, app);
      toast({
        title: <>{app} v{v} is going live.</>,
        detail: "It starts the old image, waits for its health check, then switches traffic. Nothing is rebuilt.",
        action: from ? { label: `Undo, back to v${fromV}`, run: () => mod3.rollback(project, app, from.id).then(() => refreshApp(qc, project, app)) } : undefined,
      });
    },
    onError: (e) => toast({ title: "That version can’t be made current.", detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });
}

/** Builds the live production version's own source again, with the app's current settings (env, build settings). */
export const redeployLive = (project: string, app: string) =>
  request<Deploy>("POST", `/v1/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/deploys/redeploy`);

/** Can this deploy's source be built again from the dashboard (a starter, a git URL or GitHub)? */
export const canBuildAgain = (d?: Deploy) => !!d && (d.source === "template" || (d.source === "git" && !!d.repo));

/** Builds the same source again: the same starter, the same commit from GitHub, or the same git URL and ref. */
export const buildAgain = (project: string, app: string, d: Deploy) =>
  d.source === "template" ? deployTemplate(project, app, d.template!) : d.trigger ? deployGitHub(project, app, d.commit) : deployGit(project, app, { url: d.repo!, ref: d.ref, path: undefined });

/** Can this production version be made current again (built, replaced, and not cleaned up)? */
export const canRollBack = (d: Deploy) => !d.preview && !!d.digest && (d.status === "superseded" || d.status === "rolled_back") && d.retention !== "cleaned";

/** The production version a rollback goes to: the newest replaced one older than what's live. */
export function rollbackTarget(list: Deploy[], current?: Deploy): Deploy | undefined {
  if (!current) return undefined;
  return list
    .filter((d) => canRollBack(d) && d.createdAt < current.createdAt)
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))[0];
}

/**
 * Where a deploy can be opened: a production version the box still keeps
 * (each has an address of its own, d-<id>--<app>), or a live preview.
 */
export function visitURL(d: Deploy): string | undefined {
  if (!d.url) return undefined;
  if (d.preview) return d.status === "live" ? d.url : undefined;
  return d.retention === "kept" ? d.url : undefined;
}

// ------------------------------------------------------------------ the row

const statusTone = (s: Deploy["status"]) => (s === "failed" ? "text-danger" : s === "live" || inFlight(s) ? "text-ink" : "text-ink-3");

/** Where a deploy came from, in two short lines: what, then the detail (commit message, repo, starter). */
export function DeploySource({ d, starters }: { d: Deploy; starters?: Array<{ id: string; name: string }> }) {
  const sha = d.commit?.slice(0, 7);
  let icon: ReactNode;
  let head: ReactNode;
  let detail: ReactNode = null;
  if (d.source === "git" && (d.trigger || d.createdBy?.startsWith("github:"))) {
    icon = <GitHubMark className="size-3.5 text-ink-2" />;
    head = (
      <>
        <span className="truncate">{d.ref ?? "GitHub"}</span>
        {sha && <span className="ident shrink-0 text-[0.71875rem] text-ink-3">{sha}</span>}
      </>
    );
    detail = d.message ?? (d.pullRequest ? `Pull request #${d.pullRequest}` : null);
  } else if (d.source === "git") {
    icon = <GitBranch className="size-3.5 text-ink-3" />;
    head = <span className="ident truncate text-[0.75rem]">{repoShort(d.repo) ?? "A git repository"}</span>;
    detail = [d.ref, sha].filter(Boolean).join(" · ") || null;
  } else if (d.source === "template") {
    icon = <LayoutTemplate className="size-3.5 text-ink-3" />;
    head = <span>Starter</span>;
    detail = starters?.find((s) => s.id === d.template)?.name ?? d.template;
  } else if (d.source === "prebuilt") {
    icon = <Package className="size-3.5 text-ink-3" />;
    head = <span>Prebuilt image</span>;
  } else if (d.source === "files") {
    icon = <FileCode2 className="size-3.5 text-ink-3" />;
    head = <span>Files sent directly</span>;
  } else if (d.commit) {
    icon = <GitCommitHorizontal className="size-3.5 text-ink-3" />;
    head = (
      <>
        <span>Git push</span>
        <span className="ident shrink-0 text-[0.71875rem] text-ink-3">{sha}</span>
      </>
    );
  } else {
    icon = <Upload className="size-3.5 text-ink-3" />;
    head = <span>Upload</span>;
    detail = d.dir ? `From ${d.dir}` : null;
  }
  if (d.trigger === "env") detail = "Rebuilt because a setting it builds in changed";
  return (
    <div className="min-w-0">
      <p className="flex min-w-0 items-center gap-1.5 text-[0.8125rem] text-ink">
        <span className="grid size-4 shrink-0 place-items-center">{icon}</span>
        {head}
      </p>
      {detail && <p className="truncate pl-[1.375rem] text-xs text-ink-3">{detail}</p>}
    </div>
  );
}

/**
 * One deploy as a row, Vercel-style: status and how long it took, the app and
 * its version, where it came from, when, who and how. The whole row opens the
 * deploy's page; its menu has the rest.
 */
export function DeployRow({
  project,
  d,
  v,
  showApp,
  current,
  who,
  starters,
  now,
  writer,
  onMakeCurrent,
}: {
  project: string;
  d: Deploy;
  v?: number;
  showApp?: boolean;
  /** The live production version of its app. */
  current?: boolean;
  who?: string;
  starters?: Array<{ id: string; name: string }>;
  now: number;
  writer?: boolean;
  onMakeCurrent?: () => void;
}) {
  const running = inFlight(d.status);
  const took = running ? `${secs(Math.max(0, (now - Date.parse(d.createdAt)) / 1000))} so far` : d.durationSeconds !== undefined ? secs(d.durationSeconds) : d.buildSeconds !== undefined ? secs(d.buildSeconds) : "";
  const reason = d.status === "failed" ? (d.hint ?? d.error?.split("\n")[0]) : undefined;
  const when = d.status === "live" ? liveSince(d) : d.createdAt;
  const env = d.preview ? (d.pullRequest ? `Preview · pull request #${d.pullRequest}` : "Preview") : "Production";
  const visit = visitURL(d);
  return (
    <li className="group relative grid grid-cols-[minmax(0,1fr)_auto_2rem] gap-x-3 gap-y-1.5 py-3 pr-1 pl-2 transition-colors hover:bg-paper-hover md:grid-cols-[8rem_minmax(0,11rem)_minmax(0,1fr)_minmax(0,10rem)_2rem] md:items-start md:gap-x-4 md:gap-y-1">
      <div className="min-w-0 max-md:row-start-1">
        <p className="flex items-center gap-2 text-[0.84375rem]">
          <PilotLight state={pilotOf(d.status)} label={statusWord[d.status]} />
          <span className={statusTone(d.status)}>{statusWord[d.status]}</span>
        </p>
        {took && <p className={cn("pl-4 text-xs tnum", running ? "text-brass-ink" : "text-ink-3")}>{took}</p>}
      </div>
      <div className="min-w-0 max-md:col-span-3 max-md:row-start-2">
        <Link
          to="/projects/$project/apps/$app/deploys/$id"
          params={{ project, app: d.app, id: d.id }}
          className="flex min-w-0 items-baseline gap-1.5 text-[0.875rem] outline-hidden after:absolute after:inset-0 focus-visible:after:shadow-[inset_0_0_0_2px_var(--brass)]"
        >
          {showApp && <span className="truncate font-[550] text-ink">{d.app}</span>}
          {d.preview ? (
            <span className={cn("ident truncate text-[0.75rem]", showApp ? "text-ink-3" : "text-ink")}>{d.preview}</span>
          ) : (
            <span className={cn("shrink-0 tnum", showApp ? "text-ink-3" : "font-[550] text-ink")}>{v ? `v${v}` : ""}</span>
          )}
          {current && <span className="shrink-0 rounded-[4px] border border-rule-2 px-1 text-[0.6875rem] leading-4 font-[550] text-ink-2">Current</span>}
        </Link>
        <p className="truncate text-xs text-ink-3">
          {env}
          {visit ? (
            <>
              {" · "}
              <a href={visit} target="_blank" rel="noopener noreferrer" className="relative z-[1] text-brass-ink hover:text-ink" aria-label={`Visit ${d.app} ${d.preview ? `preview ${d.preview}` : v ? `v${v}` : "this version"}`}>
                Visit
              </a>
            </>
          ) : d.retention === "cleaned" ? (
            " · Cleaned up"
          ) : null}
        </p>
      </div>
      <div className="min-w-0 max-md:col-span-3 max-md:row-start-3">
        <DeploySource d={d} starters={starters} />
      </div>
      <div className="flex min-w-0 flex-col items-end text-right max-md:col-start-2 max-md:row-start-1">
        <p className="text-[0.8125rem] text-ink-2" title={d.status === "live" ? `Live since ${full(when)}; deployed ${full(d.createdAt)}` : full(d.createdAt)}>
          {relative(when)}
        </p>
        <p className="max-w-full truncate text-xs text-ink-3">
          {who ? `${who} · ` : ""}
          {viaWords(d)}
        </p>
      </div>
      <div className="relative z-[1] flex justify-end max-md:col-start-3 max-md:row-start-1">
        <DeployMenu project={project} d={d} writer={writer} current={current} onMakeCurrent={onMakeCurrent} v={v} />
      </div>
      {reason && <p className="text-xs text-ink-2 max-md:col-span-3 md:col-span-3 md:col-start-2">{reason}</p>}
    </li>
  );
}

/**
 * The … on a deploy. Production: Redeploy (the same source built again, say
 * after changing environment variables) and Make live (a kept older or newer
 * version, back in seconds, with Undo). Every kept version: Visit and Copy
 * address. Previews only open; nothing promotes them.
 */
export function DeployMenu({ project, d, writer, current, onMakeCurrent, v }: { project: string; d: Deploy; writer?: boolean; current?: boolean; onMakeCurrent?: () => void; v?: number }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  // The live version rebuilds its kept source (any source but a prebuilt image); another version rebuilds from where it came from.
  const fromLive = !!current && d.source !== "prebuilt";
  const canRedeploy = !!writer && !d.preview && (fromLive || canBuildAgain(d));
  const again = useMutation({
    mutationFn: () => (fromLive ? redeployLive(project, d.app) : buildAgain(project, d.app, d)),
    onSuccess: (n) => {
      refreshApp(qc, project, d.app);
      toast({
        title: <>Redeploying {d.app}.</>,
        detail: "The same source, built again with its current settings. What’s live keeps serving until the new build is healthy.",
        action: { label: "Watch the build", run: () => void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app: d.app, id: n.id } }) },
      });
    },
    onError: (e) => toast({ title: <>{d.app} didn’t start redeploying.</>, detail: e instanceof Error ? e.message : undefined, tone: "danger" }),
  });
  const copy = async (value: string, what: string) => {
    if (await copyText(value)) toast({ title: `${what} copied.` });
  };
  const commitUrl = d.commit && d.repo && /github\.com/.test(d.repo) ? `${d.repo.replace(/\.git$/, "")}/commit/${d.commit}` : undefined;
  const name = d.preview ? `preview ${d.preview}` : v ? `v${v}` : "this version";
  const visit = visitURL(d);
  const makeLive = !!writer && !d.preview && !!onMakeCurrent;
  return (
    <Menu>
      <MenuTrigger asChild>
        <Button size="icon-sm" variant="ghost" aria-label={`Actions for ${d.app} ${name}`} className="text-ink-3">
          <MoreHorizontal />
        </Button>
      </MenuTrigger>
      <MenuContent align="end" className="min-w-56">
        {canRedeploy && (
          <MenuItem disabled={again.isPending} onSelect={() => again.mutate()}>
            <RotateCw /> Redeploy
          </MenuItem>
        )}
        {makeLive && (
          <MenuItem onSelect={onMakeCurrent}>
            <Undo2 /> Make live
          </MenuItem>
        )}
        {(canRedeploy || makeLive) && <MenuSeparator />}
        {visit && (
          <MenuItem onSelect={() => window.open(visit, "_blank", "noopener,noreferrer")}>
            <ArrowUpRight /> Visit
          </MenuItem>
        )}
        {visit && (
          <MenuItem onSelect={() => void copy(visit, "Address")}>
            <Copy /> Copy address
          </MenuItem>
        )}
        <MenuItem onSelect={() => void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app: d.app, id: d.id } })}>
          <FileText /> View build log
        </MenuItem>
        <MenuSeparator />
        <MenuItem onSelect={() => void copy(d.id, "Deploy ID")}>
          <Copy /> Copy deploy ID
        </MenuItem>
        {commitUrl && (
          <MenuItem onSelect={() => window.open(commitUrl, "_blank", "noopener,noreferrer")}>
            <ArrowUpRight /> Open commit on GitHub
          </MenuItem>
        )}
      </MenuContent>
    </Menu>
  );
}

/** Column names over the rows, on wide screens only. */
export function DeployHead() {
  return (
    <div className="grid grid-cols-[8rem_minmax(0,11rem)_minmax(0,1fr)_minmax(0,10rem)_2rem] gap-x-4 pr-1 pb-2 pl-2 text-xs text-ink-3 max-md:hidden" aria-hidden>
      <span>Status</span>
      <span>Version</span>
      <span>Source</span>
      <span className="text-right">When</span>
      <span />
    </div>
  );
}

// ------------------------------------------------------------------ terminal

/** The CLI, git-push and agent ways in, closed until asked for. */
export function Terminal({ project, className }: { project: string; className?: string }) {
  const git = useQuery({ queryKey: ["git", project], queryFn: () => mod3.git(project), staleTime: Infinity, retry: false });
  return (
    <details className={cn("group max-w-[46rem]", className)}>
      <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="inline-block transition-transform group-open:rotate-90">›</span> Other ways to deploy
      </summary>
      <div className="mt-2 divide-y divide-rule border-y border-rule text-[0.8125rem]">
        <TermRow title="From the app’s folder">
          <Command cmd="tiffin deploy" />
        </TermRow>
        <TermRow title="With git push" note={git.data?.url ? <span className="ident break-all">{git.data.url}</span> : "adds a remote called tiffin"}>
          <Command cmd="tiffin git-remote --add" />
          <Command className="mt-1.5" cmd="git push tiffin main" />
        </TermRow>
        <TermRow title="Let an agent do it" note="then ask it to deploy">
          <Command cmd={mcpCommand()} wrap />
        </TermRow>
      </div>
    </details>
  );
}

function TermRow({ title, note, children }: { title: string; note?: ReactNode; children: ReactNode }) {
  return (
    <div className="grid gap-x-6 gap-y-1.5 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
      <div className="min-w-0">
        <p className="font-[550] text-ink">{title}</p>
        {note && <p className="mt-0.5 text-xs text-ink-3">{note}</p>}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  );
}
