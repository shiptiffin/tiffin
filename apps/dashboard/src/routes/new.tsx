import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Check, Plus } from "lucide-react";
import { useEffect, useId, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { api, ApiError, request, type Manifest, type Op } from "@/api/client";
import type { components } from "@/api/schema";
import { mod3, type Deploy } from "@/api/modules";
import { q } from "@/api/queries";
import { Command, CopyButton } from "@/components/copy";
import { appAddress } from "@/components/deploy-parts";
import { EditCodeButton } from "@/components/edit-code";
import { useTitle } from "@/components/favicon";
import { Mascot, type MascotState } from "@/components/mascot";
import { MorphLabel } from "@/components/morph-label";
import { Crumbs, Page } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Qty } from "@/components/qty";
import { BuildSettings, buildNote } from "@/components/build-settings";
import { FrameworkLabel, FrameworkSelect } from "@/components/framework-select";
import { BuildLogView, firstError, firstErrorIn, useLaunchBuildLog } from "@/components/start-build-log";
import type { BuildLogState } from "@/components/build-log-stream";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { GitHubMark } from "@/components/github-mark";
import { checkPick, emptyPick, GitHubImport, unsupportedWhy, type GitHubPick } from "@/components/github-import";
import { ImportFile, ImportPanel, ImportSteps, useProjectImport } from "@/components/project-import";
import { deployGitHub, nameFromRepo, setSecret, type RepoRoot } from "@/lib/github";
import { useMe } from "@/lib/me";
import { agentConnect, mcpCommand } from "@/lib/mcp";
import { boxDomainQuery } from "@/lib/domains";
import { splitAddress } from "@/lib/changes";
import { addressesOf } from "@/lib/addresses";
import { cn } from "@/lib/cn";
import { useDebounced } from "@/lib/debounced";
import { partName, partSub } from "@/lib/names";
import { countWords, dec, int, NNBSP } from "@/lib/format";
import { buildFor, isTested, presetName, presetOf, PRESETS } from "@/lib/frameworks";
import {
  appFor,
  checkGitUrl,
  checkName,
  defaultOf,
  deployGit,
  deployTemplate,
  frameworkName,
  frameworksOf,
  isKind,
  KINDS,
  nameFromGit,
  newProjectManifest,
  slugify,
  starterFor,
  starterLine,
  startersQuery,
  suggestName,
  freeName,
  thumbOf,
  type Source,
  type Starter,
  type StarterKind,
} from "@/lib/starters";

const MB = 1048576;
type GitInspect = components["schemas"]["RuntimeGitInspect"];
/** Two columns: the work on the left, the mascot and the plan on the right. */
const GRID = "grid items-start gap-x-12 gap-y-8 lg:grid-cols-[minmax(0,1fr)_360px]";

type Launched = { project: string; app?: string; started: number; source: Source };

type Phase = "compose" | "launching" | "live" | "failed";

/**
 * Start a project: the first minute of Tiffin. Two questions: what it's
 * called and where its code comes from (a starter, a GitHub repository, a
 * public git URL, or none yet). Every project has its Database, KV, Files,
 * Email, Analytics and Jobs, so there is nothing else to pick. The plan on the
 * right is the real plan from the API, exactly what gets created. Create
 * applies it (a signed, undoable change), deploys the code, and the build
 * streams in on this page until the app answers at its own address.
 */
/**
 * ?starter= preselects the code: a kind ("web", "static", "api"), a starter id or an older alias ("astro", "next-postgres"),
 * "github", "git", "none", "import", "empty" or "part:<part>" (the last two, from older links, are "none").
 */
export type NewSearch = { starter?: string };

export function NewProjectPage({ search }: { search: NewSearch }) {
  useTitle("New project");
  const qc = useQueryClient();
  const projects = useQuery(q.projects);
  const names = useMemo(() => (projects.data ?? []).map((p) => p.name), [projects.data]);
  const manifests = useQueries({ queries: names.map((n) => ({ ...q.manifest(n), staleTime: 60_000 })) });
  const routes = useMemo(
    () =>
      manifests.flatMap((m) => (m.data ? addressesOf(m.data.project, m.data.manifest.apps) : [])),
    [manifests],
  );
  const taken = useMemo(() => ({ projects: names, routes }), [names, routes]);
  const starters = useQuery(startersQuery);
  const list = useMemo(() => starters.data ?? [], [starters.data]);
  const firstRun = projects.isSuccess && names.length === 0;
  const probe = manifests
    .map((m, i) => ({ project: names[i], app: Object.entries(m.data?.manifest.apps ?? {}).find(([, a]) => a.role !== "worker")?.[0] }))
    .find((x) => x.app) as { project: string; app: string } | undefined;
  const dom = useBoxDomain(probe);

  // The code: a kind of starter ("web", "static", "api"), "github", "git", "none" or "import". A ?starter= link
  // preselects one ("part:postgres" and "empty" are no code); a starter's own id opens its kind with that framework picked.
  const asked = search.starter;
  const askedPart = asked?.startsWith("part:");
  const plainCode = !!asked && (isKind(asked) || CODE_WORDS.includes(asked));
  const [code, setCodeOnly] = useState<string>(askedPart || asked === "empty" ? "none" : plainCode ? asked : "web");
  // The framework picked in each kind (a starter id); a kind not in here uses its default.
  const [chosen, setChosen] = useState<Partial<Record<StarterKind, string>>>({});
  const [linked, setLinked] = useState(asked && !plainCode && !askedPart && asked !== "empty" ? asked : undefined);
  if (linked && starters.data) {
    const s = starterFor(starters.data, linked);
    setLinked(undefined);
    if (s) {
      setCodeOnly(s.kind);
      setChosen((c) => ({ ...c, [s.kind]: s.id }));
    }
  }
  const setCode = setCodeOnly;
  const setFramework = (kind: StarterKind, id: string) => setChosen((c) => ({ ...c, [kind]: id }));
  const [git, setGit] = useState({ url: "", ref: "", path: "", framework: "next", preset: "nextjs" });
  const [gh, setGh] = useState<GitHubPick>(emptyPick);
  const { admin } = useMe();
  const [typed, setTyped] = useState<string | null>(null);
  const imp = useProjectImport(taken);

  const starterIn = (kind: StarterKind) => starterFor(list, chosen[kind]) ?? defaultOf(list, kind);
  const starter = isKind(code) ? starterIn(code) : undefined;
  const source: Source | null =
    code === "none"
      ? { kind: "none" }
      : code === "git"
        ? { kind: "git", ...git }
        : code === "github"
          ? { kind: "github", ...gh, env: gh.env.filter((e) => e.k && e.v) }
          : starter
            ? { kind: "starter", starter }
            : null;
  const app = source ? appFor(source) : null;
  const suggested =
    code === "git"
      ? nameFromGit(git.url) || "web"
      : code === "github"
        ? nameFromRepo(gh.repo) || "web"
        : code === "none"
          ? freeName("project", taken)
          : suggestName(starter, taken) || "project";
  const name = typed ?? suggested;
  const check = checkName(name, taken);
  // A pasted URL is looked inside (the box fetches it as a deploy would) for the same framework guess GitHub gets.
  const gitUrl = useDebounced(code === "git" && checkGitUrl(git.url).ok ? git.url.trim() : "", 500);
  const gitRef = useDebounced(git.ref.trim(), 500);
  const inspect = useQuery({
    queryKey: ["inspect-git", gitUrl, gitRef],
    queryFn: () => request<GitInspect>("POST", "/v1/git/inspect", { url: gitUrl, ...(gitRef ? { ref: gitRef } : {}) }),
    enabled: !!gitUrl,
    retry: false,
    staleTime: 300_000,
  });
  const found = inspect.data?.roots?.find((r) => !r.workspace) ?? inspect.data?.roots?.[0];
  const [overridden, setOverridden] = useState(false);
  // A new answer fills in how it's built, once; picking another build afterwards wins.
  const [applied, setApplied] = useState<RepoRoot>();
  if (found && found !== applied) {
    setApplied(found);
    setOverridden(false);
    setGit((g) => ({ ...g, framework: found.framework, preset: presetOf(found), path: found.path }));
  }
  const gitUnsupported = code === "git" && !overridden ? found?.unsupported : undefined;
  const urlCheck = checkGitUrl(git.url);
  const gitCheck =
    code === "git"
      ? !urlCheck.ok
        ? urlCheck
        : gitUnsupported
          ? ({ ok: false, why: unsupportedWhy(gitUnsupported) } as const)
          : ({ ok: true } as const)
      : code === "github"
        ? checkPick(gh)
        : ({ ok: true } as const);

  const [stage, setPhase] = useState<Phase>("compose");
  const [L, setL] = useState<Launched | null>(null);

  const wanted = source && check.ok && gitCheck.ok ? newProjectManifest(name, source) : null;
  const desired = useDebounced(wanted, 220);
  const key = JSON.stringify(desired);
  // Only what is on screen can be created: never a plan for an earlier name or starter.
  const settled = !!wanted && key === JSON.stringify(wanted);
  const plan = useQuery({ queryKey: ["plan-new", key], queryFn: () => api.plan(desired!), enabled: !!desired && stage === "compose", retry: false, staleTime: 30_000 });
  const rendered = useQuery({ queryKey: ["render", key], queryFn: () => api.renderConfig(desired!), enabled: !!desired, retry: false, staleTime: Infinity });
  const res = useQuery(q.resources);

  const [deployId, setDeployId] = useState<string>();
  const create = useMutation({
    mutationFn: async () => {
      if (!desired || !settled || !plan.data || plan.data.project !== desired.project || !source) throw new Error("The plan isn’t ready yet. Try again in a moment.");
      const name = desired.project;
      const started = performance.now();
      setL({ project: name, app: app?.name, started, source });
      const intent =
        source.kind === "starter"
          ? `Start ${name} from the ${source.starter.name} starter`
          : source.kind === "git"
            ? `Start ${name} from ${shortRepo(git.url)}`
            : source.kind === "github"
              ? `Start ${name} from ${source.repo} on GitHub`
              : `Start ${name}`;
      await api.apply(desired, plan.data.hash, intent);
      void qc.invalidateQueries({ queryKey: ["projects"] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
      setPhase("launching");
      if (!app) return null;
      if (source.kind === "github") for (const e of source.env) await setSecret(name, e.k, e.v);
      const d =
        source.kind === "starter"
          ? await deployTemplate(name, app.name, source.starter.id)
          : source.kind === "github"
            ? await deployGitHub(name, app.name)
            : await deployGit(name, app.name, git);
      setDeployId(d.id);
      return d;
    },
    onSuccess: (d) => {
      if (!d) setPhase("live");
    },
    onError: () => {
      setPhase((p) => (p === "compose" ? p : "failed"));
    },
  });

  const [liveAfter, setLiveAfter] = useState<number>();
  const deploy = useQuery({
    queryKey: ["deploy", L?.project ?? "", L?.app ?? "", deployId ?? ""],
    queryFn: async () => {
      const d = await mod3.deploy(L!.project, L!.app!, deployId!);
      if (d.status === "live") {
        setLiveAfter((t) => t ?? (performance.now() - L!.started) / 1000);
        void qc.invalidateQueries({ queryKey: ["projects"] });
      }
      return d;
    },
    enabled: !!deployId && !!L?.app,
    refetchInterval: (qq) => (qq.state.data && ["live", "failed"].includes(qq.state.data.status) ? false : 1000),
  });
  // While launching, the deploy decides: live or failed.
  const ds = deploy.data?.id === deployId ? deploy.data?.status : undefined;
  const phase: Phase = stage === "launching" && ds === "live" ? "live" : stage === "launching" && ds === "failed" ? "failed" : stage;

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (code === "import") return imp.submit();
    const ops = plan.data?.ops ?? [];
    if (settled && plan.data?.project === name && ops.length > 0 && ops.every((o) => o.action === "create") && !create.isPending && phase === "compose") create.mutate();
  };

  const mood: MascotState = phase === "compose" ? "base" : phase === "launching" ? "deploying" : phase;
  const crumbs = <Crumbs items={[{ label: "Projects", to: "/" }, { label: "New project" }]} />;

  const header = (
    <header className="min-w-0">
      {phase !== "compose" && <Hero state={mood} small className="mb-1 -ml-3 lg:hidden" />}
          {firstRun && phase === "compose" ? <p className="label mb-2">{new Intl.DateTimeFormat("en-GB", { weekday: "long", day: "numeric", month: "long" }).format(new Date())}</p> : crumbs}
          <h1 className="sentence mt-2 text-ink" aria-live="polite">
            {phase === "compose" ? (firstRun ? "Your tiffin is packed. Nothing in it yet." : "Start a project.") : phase === "live" ? `${L?.project} is live.` : phase === "failed" ? `${L?.project} didn’t start.` : `Packing ${L?.project}…`}
          </h1>
          <p className="mt-2 max-w-[38rem] text-md text-ink-2">
            {phase === "compose"
              ? "Name it, bring its code and pick what it needs. Its app gets its own address with HTTPS, and you can watch it go live."
              : phase === "live"
                ? liveAfter !== undefined
                  ? `It answered its health check ${dec(liveAfter, 1)}${NNBSP}s after you pressed Create.`
                  : "Created, and ready to grow."
                : phase === "failed"
                  ? "The project exists, but its app didn’t come up. Here is why, and what to change."
                  : "The project is made. Now the box builds the app and waits for it to answer."}
          </p>
    </header>
  );
  const hero = <Hero state={mood} className="mx-auto mb-4 max-lg:hidden" />;

  return (
    <Page wide>
      {phase === "compose" ? (
        <form onSubmit={submit} className={GRID}>
          <div className="min-w-0">
            {header}
            <div className="mt-9">
              {code === "import" ? (
                <>
                  <Step n={1} label="Import a .tiffin file">
                    <p className="text-sm text-ink-3">A project exported from this box or another one, with its data.</p>
                    <ImportFile imp={imp} />
                    <button type="button" onClick={() => setCode("web")} className="mt-3 text-sm text-ink-3 hover:text-ink">
                      Start a new project instead
                    </button>
                  </Step>
                  <ImportSteps imp={imp} />
                </>
              ) : (
                <>
                  <Step n={1} label="Name">
                    <label htmlFor="pname" className="sr-only">
                      Project name
                    </label>
                    <input
                      id="pname"
                      value={name}
                      onChange={(e) => setTyped(slugify(e.target.value))}
                      autoComplete="off"
                      spellCheck={false}
                      aria-invalid={!check.ok}
                      aria-describedby="pname-note"
                      className="ident h-10 w-full rounded-[8px] border border-rule-2 bg-paper-raised px-3 text-[0.9375rem] text-ink outline-hidden transition-[border-color,box-shadow] focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger"
                    />
                    <p id="pname-note" className="mt-2 min-h-5 text-sm" aria-live="polite">
                      {check.ok ? (
                        app ? (
                          <span className="text-ink-3">
                            Lives at <span className="ident text-ink">https://{name}.{dom}</span>
                          </span>
                        ) : (
                          <span className="text-ink-3">No public address until you add an app. Only you and your keys reach it.</span>
                        )
                      ) : (
                        <span className="text-danger">{check.why}</span>
                      )}
                    </p>
                  </Step>

                  <Step n={2} label="Code">
                    <RadioGroup value={code === "git" ? "github" : code} onValueChange={setCode} aria-label="Where its code comes from" loop>
                      {starters.isError ? (
                        <ProblemNote error={starters.error} title="The starters can’t be listed right now." />
                      ) : (
                        // Side by side only when the column fits each tile's framework picker (TanStack Start, its longest
                        // name, needs ~185 px a tile); narrower (a phone, or 1024-1280 px beside the summary) they stack as rows.
                        <div className="@container">
                          <div className="grid grid-cols-1 gap-2 @min-[37rem]:grid-cols-3 @min-[37rem]:gap-3">
                            {KINDS.map((k) => (
                              <KindTile
                                key={k.kind}
                                kind={k}
                                picked={code === k.kind}
                                loading={!starters.data}
                                frameworks={frameworksOf(list, k.kind)}
                                value={starterIn(k.kind)}
                                onFramework={(id) => setFramework(k.kind, id)}
                                onPick={() => code !== k.kind && setCode(k.kind)}
                              />
                            ))}
                          </div>
                        </div>
                      )}
                      <div className="mt-4 divide-y divide-rule border-y border-rule">
                        <OptionRow
                          value="github"
                          picked={code === "github" || code === "git"}
                          icon={<GitHubMark />}
                          title="Your GitHub repository"
                          line="Every push deploys. Each pull request gets a preview."
                        />
                        <OptionRow value="none" picked={code === "none"} icon={<Plus />} title="No code yet" line={starterLine.none} />
                      </div>
                    </RadioGroup>
                    {code === "github" && (
                      <>
                        <GitHubImport value={gh} onChange={setGh} admin={admin} />
                        <SwitchLink onClick={() => setCodeOnly("git")}>Not on GitHub? Paste a public git URL</SwitchLink>
                      </>
                    )}
                    {code === "git" && (
                      <>
                        <p className="mt-4 text-sm text-ink-3">{starterLine.git} It doesn’t redeploy when the repository changes.</p>
                        <GitFields
                          git={git}
                          setGit={(g) => {
                            if (g.preset !== git.preset) setOverridden(true);
                            setGit(g);
                          }}
                          check={urlCheck}
                          inspect={{ loading: inspect.isFetching, error: inspect.error, found, unsupported: gitUnsupported }}
                        />
                        <SwitchLink onClick={() => setCodeOnly("github")}>Use one of your GitHub repositories instead</SwitchLink>
                      </>
                    )}
                  </Step>


                  <p className="border-t border-rule pt-4 text-sm text-ink-3">
                    Moving a project from another box?{" "}
                    <button type="button" onClick={() => setCode("import")} className="font-[550] text-brass-ink hover:underline hover:underline-offset-4">
                      Import a .tiffin file
                    </button>
                  </p>
                  {firstRun && (
                    <section aria-label="Hand it to your agent" className="mt-10">
                      <h2 className="text-[0.9375rem] font-[550] text-ink">Or hand it to your agent</h2>
                      <p className="mt-1 mb-3 text-sm text-ink-2">Claude Code can set up projects for you. It asks you before anything destructive, and every change can be undone in History.</p>
                      <Command cmd={mcpCommand()} />
                    </section>
                  )}
                </>
              )}
            </div>
          </div>

          <div className="min-w-0">
            {code === "import" ? (
              <ImportPanel imp={imp} />
            ) : (
              <PlanPanel
                name={name}
                ready={settled && !!plan.data && plan.data.project === name}
                plan={plan.data}
                planError={plan.error}
                pending={!settled || plan.isPending}
                blocked={!check.ok || !gitCheck.ok || !source}
                config={rendered.data?.config}
                freeMB={res.data && res.data.memory.totalBytes > 0 ? res.data.memory.availableBytes / MB : undefined}
                createError={create.error}
                creating={create.isPending}
                source={source}
                blockedWhy={gitUnsupported ? unsupportedWhy(gitUnsupported) : code === "github" && gh.unsupported ? unsupportedWhy(gh.unsupported) : undefined}
              />
            )}
          </div>
        </form>
      ) : (
        <Launch
          header={header}
          hero={hero}
          L={L!}
          phase={phase}
          deploy={deploy.data}
          deployId={deployId}
          error={create.error}
          retry={() => {
            if (!L?.app) return;
            setPhase("launching");
            const src = L.source;
            (src.kind === "starter"
              ? deployTemplate(L.project, L.app, src.starter.id)
              : src.kind === "git"
                ? deployGit(L.project, L.app, src)
                : src.kind === "github"
                  ? deployGitHub(L.project, L.app)
                  : Promise.reject())
              .then((d) => setDeployId(d.id))
              .catch(() => setPhase("failed"));
          }}
        />
      )}
    </Page>
  );
}

// ───────────────────────── pieces ─────────────────────────

/** The mascot: waiting, then packing once a project starts, then live (or the spill when it didn't start). Cross-fades in place. */
function Hero({ state, small, className }: { state: MascotState; small?: boolean; className?: string }) {
  return <Mascot state={state} size={small ? 112 : 212} className={cn("block", className)} />;
}

function Step({ n, label, children }: { n: number; label: string; children: ReactNode }) {
  return (
    <section className="mb-10 last:mb-0" aria-label={label}>
      <h2 className="label mb-3" data-step={n}>
        {label}
      </h2>
      {children}
    </section>
  );
}

/**
 * A kind of starter as a tile: its drawing over its name and line, then its
 * framework. The picked tile's framework is a quiet dropdown (a kind with one
 * framework just names it); the others show their default. In a narrow
 * column (a phone, or beside the summary at 1024-1280 px), a compact row
 * with a small drawing, the framework beneath.
 */
function KindTile({
  kind,
  picked,
  loading,
  frameworks,
  value,
  onFramework,
  onPick,
}: {
  kind: (typeof KINDS)[number];
  picked: boolean;
  loading: boolean;
  frameworks: Starter[];
  value?: Starter;
  onFramework: (id: string) => void;
  onPick: () => void;
}) {
  // A starter linked by id but not offered (an older one) still shows as the pick.
  const options = (value && !frameworks.includes(value) ? [...frameworks, value] : frameworks).map((s) => ({ id: s.id, preset: s.preset, name: s.presetName }));
  return (
    <div
      data-kind={kind.kind}
      className={cn(
        "group/tile flex flex-col overflow-hidden rounded-[12px] border bg-paper-raised transition-[border-color,box-shadow] duration-[var(--dur-state)] ease-[var(--ease-out)]",
        picked ? "border-brass shadow-[0_0_0_1px_var(--brass)]" : "border-rule-2 hover:border-rule-3",
      )}
    >
      <RadioItem value={kind.kind} className="group flex flex-1 text-left outline-hidden active:scale-[0.99] @min-[37rem]:flex-col">
        <span className="art-well block w-20 shrink-0 bg-paper-sunk @min-[37rem]:aspect-[16/10] @min-[37rem]:w-full">
          <img src={kind.thumb} alt="" width={320} height={200} className="size-full object-contain p-1.5 transition-transform duration-[var(--dur-enter)] ease-[var(--ease-out)] group-hover:scale-[1.03]" />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-1 border-l border-rule px-3 pt-2.5 pb-2.5 @min-[37rem]:border-t @min-[37rem]:border-l-0">
          <span className="flex items-center justify-between gap-2 text-[0.875rem] font-[550] text-ink">
            {kind.title}
            <span
              aria-hidden
              className={cn(
                "grid size-4 shrink-0 place-items-center rounded-full border transition-colors group-focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]",
                picked ? "border-brass bg-brass text-on-brass" : "border-rule-3 text-transparent",
              )}
            >
              <Check className="size-2.5" strokeWidth={3} />
            </span>
          </span>
          <span className="text-[0.78125rem] leading-[1.125rem] text-ink-3">{kind.line}</span>
        </span>
      </RadioItem>
      {/* The picker sizes to its framework's name. Side by side, every tile puts "Framework" on a line of its own so the
          tiles' footers line up; as rows, the label leads and the picker wraps below it only if the row is too narrow. */}
      <div
        className="flex min-h-11 flex-wrap items-center gap-x-2 gap-y-1 border-t border-rule px-3 py-1.5 @min-[37rem]:pt-2.5 @min-[37rem]:pb-2"
        onClick={picked ? undefined : onPick}
      >
        <span className="text-xs text-ink-3 @min-[37rem]:basis-full">Framework</span>
        {loading ? (
          <span className="flex h-8 items-center">
            <span className="h-3.5 w-20 rounded bg-paper-sunk" />
          </span>
        ) : picked && options.length > 1 ? (
          <FrameworkSelect
            size="sm"
            aria-label={`${kind.title} framework`}
            value={value?.id ?? ""}
            onChange={onFramework}
            options={options.map((o) => ({ id: o.id, logo: o.preset, name: o.name }))}
            className="w-auto max-w-full"
          />
        ) : value ? (
          <FrameworkLabel id={value.preset} name={value.presetName} className={cn("h-8 text-[0.8125rem]", picked ? "text-ink-2" : "text-ink-3")} />
        ) : null}
      </div>
    </div>
  );
}

/** Words in ?starter= that aren't a kind or a starter. */
const CODE_WORDS = ["github", "git", "none", "import"];

function SwitchLink({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={onClick} className="mt-3 text-sm font-[550] text-brass-ink hover:underline hover:underline-offset-4">
      {children}
    </button>
  );
}

function OptionRow({ value, picked, icon, title, line }: { value: string; picked: boolean; icon: ReactNode; title: string; line: string }) {
  return (
    <RadioItem
      value={value}
      className="grid w-full grid-cols-[28px_minmax(0,1fr)_16px] items-center gap-x-3 py-3 text-left transition-colors hover:bg-[color-mix(in_oklch,var(--ink)_2.5%,transparent)]"
    >
      <span className="grid size-7 place-items-center text-ink-3 [&_svg]:size-4">{icon}</span>
      <span className="min-w-0">
        <span className="block text-[0.875rem] font-[550] text-ink">{title}</span>
        <span className="block text-[0.8125rem] text-ink-3">{line}</span>
      </span>
      <span aria-hidden className={cn("grid size-4 place-items-center rounded-full border", picked ? "border-brass bg-brass text-on-brass" : "border-rule-3 text-transparent")}>
        <Check className="size-2.5" strokeWidth={3} />
      </span>
    </RadioItem>
  );
}


function GitFields({
  git,
  setGit,
  check,
  inspect,
}: {
  git: { url: string; ref: string; path: string; framework: string; preset: string };
  setGit: (g: typeof git) => void;
  check: { ok: boolean; why?: string };
  inspect: { loading: boolean; error: unknown; found?: RepoRoot; unsupported?: string };
}) {
  const field = "h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.84375rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";
  const uid = useId();
  return (
    <div className="mt-4 grid gap-3 sm:grid-cols-[minmax(0,1fr)_12rem]">
      <label>
        <span className="mb-1 block text-xs text-ink-3">Repository</span>
        <input
          autoFocus
          value={git.url}
          onChange={(e) => setGit({ ...git, url: e.target.value })}
          placeholder="https://github.com/owner/repo"
          spellCheck={false}
          className={cn(field, "ident")}
          aria-invalid={!!git.url && !check.ok}
        />
        {git.url && !check.ok && <span className="mt-1 block text-sm text-danger">{"why" in check ? check.why : ""}</span>}
      </label>
      <label>
        <span className="mb-1 block text-xs text-ink-3">Branch, tag or commit</span>
        <input value={git.ref} onChange={(e) => setGit({ ...git, ref: e.target.value })} placeholder="default branch" spellCheck={false} className={cn(field, "ident")} />
      </label>
      <div className="sm:col-span-2 sm:max-w-[22rem]">
        <span id={`${uid}-fw`} className="mb-1 block text-xs text-ink-3">
          Framework{inspect.found && !inspect.loading && git.preset === presetOf(inspect.found) ? " · detected" : ""}
        </span>
        <FrameworkSelect
          aria-labelledby={`${uid}-fw`}
          value={git.preset}
          onChange={(preset) => setGit({ ...git, preset, framework: buildFor(preset, inspect.found) })}
          options={PRESETS}
        />
      </div>
      <p className="text-sm text-ink-3 sm:col-span-2">
        {inspect.loading ? (
          "Looking inside the repository…"
        ) : inspect.unsupported ? (
          <span className="text-danger">{unsupportedWhy(inspect.unsupported)}</span>
        ) : inspect.found ? (
          <>
            Found {inspect.found.path ? <>in <span className="ident text-[0.75rem] text-ink-2">{inspect.found.path}</span></> : "at the top of the repository"}: {inspect.found.why}.{" "}
            {!isTested(git.preset) && buildNote(git.framework)}
          </>
        ) : inspect.error ? (
          <>Tiffin couldn’t look inside it ({inspect.error instanceof Error ? inspect.error.message : "no answer"}), so it will build it as {presetName(git.preset)}.</>
        ) : (
          <>Paste the address and Tiffin looks inside to pick the framework.</>
        )}
      </p>
      <BuildSettings className="sm:col-span-2" path={git.path} onPath={(path) => setGit({ ...git, path })} />
    </div>
  );
}

// ───────────────────────── the plan ─────────────────────────

function PlanPanel({
  name,
  ready,
  plan,
  planError,
  pending,
  blocked,
  config,
  freeMB,
  createError,
  creating,
  source,
  blockedWhy,
}: {
  name: string;
  ready: boolean;
  plan?: { ops?: Op[] | null; hash: string; risk: string };
  planError: unknown;
  pending: boolean;
  blocked: boolean;
  config?: string;
  freeMB?: number;
  createError: unknown;
  creating: boolean;
  source: Source | null;
  /** Why it can't be created, when the code says so (a framework the box can't run yet). */
  blockedWhy?: string;
}) {
  const ops = plan?.ops ?? [];
  const clash = !!plan && (ops.length === 0 || ops.some((o) => o.action !== "create"));
  const appMB = ops.reduce((t, o) => {
    const a = (o.after ?? {}) as { framework?: string; instances?: number; memoryMB?: number };
    return splitAddress(o.address).kind === "app" && o.action === "create" && a.framework !== "static" && a.memoryMB ? t + (a.instances ?? 1) * a.memoryMB : t;
  }, 0);
  const things = ops.length;
  const label = creating ? "Creating…" : ready ? `Create ${name}` : "Create project";
  return (
    <aside
      aria-label="The plan"
      className="min-w-0 overflow-hidden rounded-[14px] border border-rule-2 bg-paper-raised shadow-raised lg:sticky lg:top-8"
    >
      <div className="border-b border-rule px-5 pt-4 pb-3.5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="label">What you’ll get</h2>
        </div>
        <p className="mt-2 text-[0.9375rem] leading-[1.375rem] font-[550] tracking-[-0.01em] text-ink">
          {blocked
            ? source?.kind === "github" && !source.repo
              ? "Pick a repository to see the plan."
              : source?.kind === "git"
                ? (blockedWhy ?? "Paste a repository address to see the plan.")
                : (blockedWhy ?? "Pick a free name to see the plan.")
            : clash
              ? `There’s already a project called ${name}. Pick another name.`
              : planError
              ? "The box can’t plan this yet."
              : !plan
              ? "Planning…"
              : `${countWords(things, "thing", "things", true)}, ready in ${source?.kind === "none" ? "seconds" : "about a minute"}.`}
        </p>
      </div>
      <div className="px-5">
        {planError ? (
          <ProblemNote error={planError} className="my-4" title="This can’t be planned." />
        ) : (
          <ol className={cn("divide-y divide-rule transition-opacity duration-[var(--dur-state)]", (pending || blocked) && "opacity-50")}>
            {sortOps(plan ? ops : skeletonOps(source)).map((o, i) => (
              <OpRow key={o.address + i} op={o} project={name} framework={frameworkOfSource(source)} />
            ))}
          </ol>
        )}
      </div>
      <div className="mt-1 grid gap-3 border-t border-rule px-5 py-4 text-sm">
        <div>
          <div className="flex items-baseline justify-between gap-3">
            <span className="text-ink-3">Room left on your box</span>
            {freeMB !== undefined ? (
              <span className="tnum text-ink">
                {appMB > 0 && <span className="text-ink-3 line-through decoration-ink-4">{int(freeMB)}</span>}{" "}
                <Qty value={int(freeMB - appMB)} unit="MB" className="font-[550]" />
              </span>
            ) : (
              <span className="text-ink-3">measured on a running box</span>
            )}
          </div>
          {freeMB !== undefined && (
            <p className="mt-1 text-xs text-ink-3">
              {appMB > 0 ? `The app may use up to ${int(appMB)}\u202fMB; it starts much smaller.` : "It grows as it needs and shares the box with your other projects."}
            </p>
          )}
        </div>
        {config && (
          <details className="group">
            <summary className="cursor-pointer list-none text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
              <span className="inline-block transition-transform group-open:rotate-90">›</span> <span className="ident text-[0.75rem]">tiffin.config.ts</span>, {countWords(config.trim().split("\n").length, "line")}
            </summary>
            <pre className="ident mt-2 max-h-64 overflow-auto rounded-[8px] border border-rule bg-paper px-3 py-2.5 text-[0.71875rem] leading-[1.125rem] text-ink-2">{config.trim()}</pre>
          </details>
        )}
      </div>
      <div className="border-t border-rule bg-paper-raised px-5 py-4">
        {!!createError && <ProblemNote error={createError} className="mb-3" />}
        <Button type="submit" variant="primary" size="lg" className="w-full" disabled={!ready || creating || blocked || clash}>
          <MorphLabel text={label} />
          <span className="kbd border-current text-current opacity-55" aria-hidden>
            ↵
          </span>
        </Button>
        <p className="mt-2.5 text-center text-xs text-ink-3">Changed your mind later? Undo it from History.</p>
      </div>
    </aside>
  );
}

/** The project, then its app, then the services it uses. */
const sortOps = (ops: Op[]) => {
  const rank = (o: Op) => ["project", "app", "service"].indexOf(splitAddress(o.address).kind) + 1 || 9;
  return [...ops].sort((a, b) => rank(a) - rank(b));
};

/** Rows to hold the plan's place while it loads. */
function skeletonOps(source: Source | null): Op[] {
  const ops: Op[] = [{ action: "create", address: "project", risk: "reversible", reason: "" }];
  const app = source ? appFor(source) : null;
  if (app) ops.push({ action: "create", address: `app/${app.name}`, risk: "reversible", reason: "", after: { framework: app.framework } });
  for (const p of ["postgres", "valkey", "storage", "email", "analytics"]) ops.push({ action: "create", address: `service/${p}`, risk: "reversible", reason: "" });
  return ops;
}

/** The framework the app is made with, as people know it: the starter's, or the one picked for a repository. */
function frameworkOfSource(source: Source | null): string | undefined {
  if (source?.kind === "starter") return source.starter.presetName;
  if (source?.kind === "git" || source?.kind === "github") return presetName(source.preset);
  return undefined;
}

function OpRow({ op, project, framework }: { op: Op; project: string; framework?: string }) {
  const { kind, name } = splitAddress(op.address);
  const a = (op.after ?? {}) as Record<string, unknown>;
  let title: ReactNode = name || kind;
  let line: ReactNode = null;
  let amount: ReactNode = null;
  if (kind === "project") {
    title = (
      <>
        Project <b className="font-[550]">{project}</b>
      </>
    );
    line = "Its own address and settings";
  } else if (kind === "app") {
    const n = Number(a.instances ?? 1);
    title = (
      <>
        App <b className="font-[550]">{name}</b>
      </>
    );
    const git = a.git as { repo?: string; branch?: string } | undefined;
    line =
      a.framework === "static"
        ? framework
          ? `${framework}, built to static files and served instantly`
          : "Static files, served instantly"
        : `${framework ?? frameworkName(String(a.framework ?? ""))}${n > 1 ? `, ${countWords(n, "copy", "copies")}` : ""}`;
    if (git?.repo) line = `${line}, from ${git.repo} (deploys on every push to ${git.branch ?? "main"})`;
    amount = a.framework === "static" || !a.memoryMB ? null : <Qty value={`+${int(n * Number(a.memoryMB))}`} unit="MB" />;
  } else if (kind === "service") {
    title = partName(name);
    line =
      name === "postgres"
        ? "Postgres 18, its own database"
        : name === "valkey"
          ? `Fast key-value, up to ${int(Number(a.maxMemoryMB ?? 64))}${NNBSP}MB`
          : name === "analytics"
            ? `Cookieless, kept ${int(Number(a.retentionDays ?? 365))} days`
            : partSub(name) || null;
  } else if (kind === "bucket") {
    title = (
      <>
        Bucket <b className="font-[550]">{name}</b>
      </>
    );
    line = a.public ? "Anyone can read its files" : "Private: only your apps and links you make";
  }
  return (
    <li className="grid grid-cols-[14px_minmax(0,1fr)_auto] items-baseline gap-x-2.5 py-2.5">
      <span className="text-brass-ink" aria-hidden>
        +
      </span>
      <span className="min-w-0">
        <span className="block text-[0.84375rem] text-ink">{title}</span>
        {line && <span className="block text-xs text-ink-3">{line}</span>}
      </span>
      <span className="text-[0.8125rem] text-ink-2 tnum">{amount}</span>
    </li>
  );
}

// ───────────────────────── launch ─────────────────────────

function Launch({
  header,
  hero,
  L,
  phase,
  deploy,
  deployId,
  error,
  retry,
}: {
  header: ReactNode;
  hero: ReactNode;
  L: Launched;
  phase: Phase;
  deploy?: Deploy;
  deployId?: string;
  error: unknown;
  retry: () => void;
}) {
  const log = useLaunchBuildLog(L.project, L.app ?? "", deployId, deploy?.createdAt);
  const status = deploy?.status;
  const now = useNow(phase === "launching");
  const since = (now - L.started) / 1000;
  const url = appAddress(deploy);
  const host = url?.replace(/^https?:\/\//, "");
  const what =
    L.source.kind === "starter"
      ? `the ${L.source.starter.name} starter`
      : L.source.kind === "git"
        ? shortRepo(L.source.url)
        : L.source.kind === "github"
          ? `${L.source.repo} (${L.source.branch})`
          : "";

  const steps: Array<{ key: string; label: ReactNode; state: "done" | "busy" | "todo" | "fault"; note?: ReactNode }> = [
    { key: "apply", label: `Made ${L.project}`, state: "done" },
  ];
  if (L.app) {
    const built = status === "starting" || status === "live" || (status === "failed" && deploy?.buildSeconds !== undefined);
    steps.push({
      key: "build",
      label: `Building ${L.app} from ${what}`,
      state: built ? "done" : status === "failed" ? "fault" : "busy",
      note: deploy?.buildSeconds !== undefined ? `${dec(deploy.buildSeconds, 1)}${NNBSP}s` : status && !built ? `${int(since)}${NNBSP}s` : undefined,
    });
    steps.push({
      key: "start",
      label: "Starting it and waiting for its health check",
      state: status === "live" ? "done" : status === "starting" ? "busy" : status === "failed" && built ? "fault" : "todo",
    });
    steps.push({ key: "live", label: host ? `Live at ${host}` : "Live at its own address", state: status === "live" ? "done" : "todo" });
  }

  if (phase === "live") return <Live header={header} hero={hero} L={L} deploy={deploy} log={log} />;

  return (
    <div className={GRID}>
      <div className="min-w-0">
        {header}
        <ol className="mt-9 divide-y divide-rule border-y border-rule" aria-label="Progress">
          {steps.map((s) => (
            <li key={s.key} className="grid grid-cols-[24px_minmax(0,1fr)_auto] items-center gap-x-3 py-3">
              <span className="grid place-items-center">
                {s.state === "done" ? (
                  <Check className="size-4 text-ink-2" strokeWidth={2.25} aria-label="Done" />
                ) : s.state === "busy" ? (
                  <PilotLight state="busy" label="Working" />
                ) : s.state === "fault" ? (
                  <PilotLight state="fault" label="Failed" />
                ) : (
                  <span className="size-1.5 rounded-full bg-rule-3" aria-hidden />
                )}
              </span>
              <span className={cn("text-[0.875rem]", s.state === "todo" ? "text-ink-3" : s.state === "fault" ? "text-danger" : "text-ink")}>{s.label}</span>
              <span className="text-[0.8125rem] text-ink-3 tnum">{s.note}</span>
            </li>
          ))}
        </ol>

        {phase === "failed" && <Failed L={L} deploy={deploy} error={error} log={log} retry={retry} />}

        {L.app && deployId && (
          <section className="mt-8" aria-label="Build log">
            <div className="mb-2 flex items-baseline justify-between gap-3">
              <h2 className="label">Build log</h2>
              <span className="text-xs text-ink-3">{log.live ? "Following as it builds" : log.done ? `${countWords(log.model.dropped + log.model.lines.length, "line")}` : "Connecting…"}</span>
            </div>
            <BuildLogView log={log} t0={log.t0} waiting={phase === "launching"} maxHeight="22rem" />
          </section>
        )}
      </div>
      <aside className="min-w-0 text-sm text-ink-2 max-lg:hidden">
        {hero}
        <p className="label mt-6 mb-2">While it builds</p>
        <p>
          The box builds <b className="font-[550] text-ink">{L.app ?? L.project}</b> with Railpack and BuildKit, starts one instance, and only routes traffic to it once its health check passes.
        </p>
        <p className="mt-3">You can leave this page; the build carries on, and the project’s page shows it.</p>
      </aside>
    </div>
  );
}

function Failed({ L, deploy, error, log, retry }: { L: { project: string; app?: string }; deploy?: Deploy; error: unknown; log: BuildLogState; retry: () => void }) {
  const cause = firstError(deploy?.error ?? "") ?? firstErrorIn(log);
  return (
    <div role="alert" className="mt-6 rounded-[10px] border border-danger-rule bg-danger-wash px-5 py-4">
      {deploy ? (
        <>
          <p className="text-[0.9375rem] font-[550] text-ink">{deploy.hint ?? "The build or the start failed."}</p>
          {cause && (
            <p className="ident mt-2 text-[0.75rem] break-words text-ink">
              <span className="font-sans text-sm text-ink-3">It said: </span>
              {cause}
            </p>
          )}
        </>
      ) : (
        <ProblemNote error={error} className="border-0 bg-transparent p-0" />
      )}
      <p className="mt-2 text-sm text-ink-2">
        {L.project} exists and nothing else on the box changed. Try again, open the app to change it, or undo the project from History.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        {L.app && (
          <Button variant="primary" size="md" onClick={retry}>
            Build {L.app} again
          </Button>
        )}
        {L.app && (
          <Button asChild size="md">
            <Link to="/projects/$project/apps/$app" params={{ project: L.project, app: L.app }}>
              Open {L.app}
            </Link>
          </Button>
        )}
        <Button asChild variant="ghost" size="md">
          <Link to="/ledger" search={{ project: L.project }}>
            Undo from History
          </Link>
        </Button>
      </div>
      {error instanceof ApiError && deploy && <ProblemNote error={error} className="mt-3" />}
    </div>
  );
}

function Live({
  header,
  hero,
  L,
  deploy,
  log,
}: {
  header: ReactNode;
  hero: ReactNode;
  L: Launched;
  deploy?: Deploy;
  log: ReturnType<typeof useLaunchBuildLog>;
}) {
  const url = appAddress(deploy);
  const host = url?.replace(/^https?:\/\//, "");
  const thumb = L.source.kind === "starter" ? thumbOf(L.source.starter) : undefined;
  const { admin } = useMe();
  const connect = agentConnect();
  const agent = connect.needsKey ? (
    <NextStep
      n={2}
      title="Connect your agent"
      line="Create an API key, then run this with it. Claude Code asks you before anything destructive, and every change lands in History, where you can undo it."
    >
      <Command cmd={connect.cmd} />
      <Link to="/settings/keys" search={{ create: true }} className="mt-2 inline-block text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
        Create a key in Settings › API keys
      </Link>
    </NextStep>
  ) : (
    <NextStep
      n={2}
      title="Connect your agent"
      line="Claude Code gets the box’s agent key: full access to every project. It asks you before anything destructive, and every change lands in History, where you can undo it."
    >
      <Command cmd={connect.cmd} />
      {admin && (
        <Link to="/settings/keys" search={{ create: true }} className="mt-2 inline-block text-[0.8125rem] text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
          Or make a key for just {L.project}, or read only
        </Link>
      )}
    </NextStep>
  );
  return (
    <div className={GRID}>
      {url && L.app ? (
        <>
          <div className="min-w-0">
            {header}
            <p className="label mt-8 mb-2 flex items-center gap-2">
              <PilotLight state="on" /> Live
            </p>
            <div className="flex min-w-0 items-center gap-2">
              <a
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                className="group inline-flex min-w-0 items-center gap-1.5 text-[1.625rem] leading-9 font-[500] tracking-[-0.02em] text-brass-ink underline decoration-[color-mix(in_oklch,var(--brass)_35%,transparent)] decoration-1 underline-offset-[6px] hover:decoration-current max-sm:text-[1.25rem]"
              >
                <span className="truncate">{host}</span>
                <ArrowUpRight className="size-5 shrink-0 transition-transform duration-[var(--dur-state)] group-hover:translate-x-0.5 group-hover:-translate-y-0.5" />
              </a>
              <CopyButton value={url} label="Copy the address" />
            </div>
            <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-ink-3">
              <span className="inline-flex items-center gap-1.5">
                <Odometer to={1} /> of {L.app}
              </span>
              <span aria-hidden>·</span>
              <span>HTTPS from the box’s own certificate</span>
              {deploy?.buildSeconds !== undefined && (
                <>
                  <span aria-hidden>·</span>
                  <span>
                    built in {dec(deploy.buildSeconds, 1)}
                    {NNBSP}s
                  </span>
                </>
              )}
            </p>

            <h2 className="label mt-10 mb-1">Next</h2>
            <ol className="divide-y divide-rule border-y border-rule">
              {L.source.kind === "github" ? (
                <NextStep
                  n={1}
                  title={`Push to ${L.source.branch}`}
                  line={`Every push to ${L.source.branch} deploys ${L.app}. Each pull request gets a preview at its own address, and a comment with the link. Previews use this project’s live data. Email goes to the dev inbox.`}
                >
                  <Button asChild size="md">
                    <a href={`https://github.com/${L.source.repo}`} target="_blank" rel="noopener noreferrer">
                      <GitHubMark /> Open {L.source.repo}
                    </a>
                  </Button>
                </NextStep>
              ) : (
                <NextStep n={1} title="Make your first change" line={firstChange(L)}>
                  <EditCodeButton project={L.project} apps={[L.app]} app={L.app} size="md" command={false} />
                </NextStep>
              )}
              {agent}
              <NextStep n={3} title="Add a service" line="Files, Email, Auth or Jobs. Each one is a click, and History can undo it.">
                <Button asChild size="md">
                  <Link to="/projects/$project" params={{ project: L.project }}>
                    Open {L.project}
                  </Link>
                </Button>
              </NextStep>
            </ol>
            <details className="group mt-6">
              <summary className="cursor-pointer list-none text-sm text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
                <span className="inline-block transition-transform group-open:rotate-90">›</span> Build log, {countWords(log.model.dropped + log.model.lines.length, "line")}
              </summary>
              <BuildLogView log={log} t0={log.t0} className="mt-2" maxHeight="20rem" />
            </details>
          </div>
          <div className="min-w-0">
            {hero}
            <Preview url={url} host={host!} thumb={thumb} app={L.app} />
          </div>
        </>
      ) : (
        <div className="max-w-[40rem]">
          {header}
          <p className="mt-6 text-md text-ink-2">
            {L.project} is in the box with its config. Add an app or a service from its page; every change lands in History, where you can undo it.
          </p>
          <div className="mt-5 flex gap-2">
            <Button asChild variant="primary" size="lg">
              <Link to="/projects/$project" params={{ project: L.project }}>
                Open {L.project}
              </Link>
            </Button>
            <Button asChild variant="ghost" size="lg">
              <Link to="/">All projects</Link>
            </Button>
          </div>
          <h2 className="label mt-10 mb-1">Or from your terminal</h2>
          <Command cmd={`tiffin pull --project ${L.project}`} />
        </div>
      )}
    </div>
  );
}

/** What "Make your first change" says; Edit code has the steps, the same as on the project's page. */
function firstChange(L: Launched): string {
  if (L.source.kind === "git") return "Clone your code, change it and deploy it to the same address. The steps work for you or your coding agent.";
  return `Get ${L.app}’s code onto your computer, change it and deploy it to the same address. The steps work for you or your coding agent.`;
}

function NextStep({ n, title, line, children }: { n: number; title: string; line: string; children: ReactNode }) {
  return (
    <li className="grid gap-x-5 gap-y-2.5 py-4 sm:grid-cols-[28px_minmax(0,14rem)_minmax(0,1fr)] sm:items-center">
      <span className="ident text-[0.6875rem] text-ink-4 max-sm:hidden">{String(n).padStart(2, "0")}</span>
      <div className="min-w-0">
        <p className="text-[0.875rem] font-[550] text-ink">{title}</p>
        <p className="mt-0.5 text-[0.8125rem] text-ink-3">{line}</p>
      </div>
      <div className="min-w-0">{children}</div>
    </li>
  );
}

/**
 * The app in a window. Apps on the box refuse to be framed (frame-ancestors
 * 'none'), so this is a frame around the starter's drawing and the real
 * address, and the whole thing opens the live app.
 */
function Preview({ url, host, thumb, app }: { url: string; host: string; thumb?: string; app: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className="group block overflow-hidden rounded-[12px] border border-rule-2 bg-paper-raised shadow-raised transition-transform duration-[var(--dur-enter)] ease-[var(--ease-out)] hover:-translate-y-0.5"
      aria-label={`Open ${app} at ${host}`}
    >
      <div className="flex items-center gap-3 border-b border-rule px-3 py-2">
        <span className="flex gap-1.5" aria-hidden>
          {[0, 1, 2].map((i) => (
            <i key={i} className="size-2.5 rounded-full bg-rule-2" />
          ))}
        </span>
        <span className="ident flex min-w-0 flex-1 items-center justify-center gap-1.5 truncate rounded-[6px] bg-paper-sunk px-2 py-0.5 text-[0.71875rem] text-ink-2">
          <span className="text-ink-4">https://</span>
          {host}
        </span>
      </div>
      <div className={cn("relative grid aspect-[16/10] place-items-center bg-paper", thumb && "art-well")}>
        {thumb ? <img src={thumb} alt="" className="w-3/4" /> : <span className="text-ink-3">{app}</span>}
        <span className="absolute right-3 bottom-3 inline-flex items-center gap-1 rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 py-1 text-xs font-[550] text-ink shadow-raised transition-colors group-hover:border-brass">
          Open {app} <ArrowUpRight className="size-3.5" />
        </span>
      </div>
    </a>
  );
}

/** "v1": rolls up from the previous number once, like an odometer. */
export function Odometer({ to, prefix = "v" }: { to: number; prefix?: string }) {
  const [rolled, setRolled] = useState(false);
  useEffect(() => {
    const r = requestAnimationFrame(() => setRolled(true));
    return () => cancelAnimationFrame(r);
  }, []);
  return (
    <span className="inline-flex items-baseline font-[550] text-ink tnum" aria-label={`${prefix}${to}`}>
      {prefix}
      <span className="relative inline-block h-[1.2em] overflow-hidden align-bottom" aria-hidden>
        <span className={cn("flex flex-col transition-transform duration-[400ms] ease-[var(--ease-out)]", rolled ? "-translate-y-1/2" : "translate-y-0")}>
          <span className="h-[1.2em] leading-[1.2em]">{int(to - 1)}</span>
          <span className="h-[1.2em] leading-[1.2em]">{int(to)}</span>
        </span>
      </span>
    </span>
  );
}

// ───────────────────────── helpers ─────────────────────────

/**
 * The domain apps live under, with its port, as apps get it: "tiffin.localhost:8470". On a box, the
 * box's apps domain (it can differ from the dashboard's); on a local dev server, from any app the box
 * already serves.
 */
export function useBoxDomain(probe?: { project: string; app: string }) {
  const bd = useQuery({ ...boxDomainQuery, staleTime: 300_000 });
  const h = location.hostname;
  const onBox = bd.data ? h === bd.data.dashboard : h.startsWith("dashboard.");
  const port = location.port && location.port !== "443" ? `:${location.port}` : "";
  const own = onBox ? (bd.data?.appsDomain || h.slice(h.indexOf(".") + 1)) + port : undefined;
  const rt = useQuery({
    queryKey: ["runtime", probe?.project ?? "", probe?.app ?? ""],
    queryFn: () => mod3.runtime(probe!.project, probe!.app),
    enabled: !own && !!probe,
    retry: false,
    staleTime: 300_000,
  });
  if (own) return own;
  try {
    const u = new URL(rt.data?.production?.url ?? "");
    return u.hostname.split(".").slice(1).join(".") + (u.port ? `:${u.port}` : "");
  } catch {
    return "tiffin.localhost";
  }
}

function shortRepo(url: string) {
  try {
    const u = new URL(url.trim());
    return u.pathname.replace(/^\/|\.git$/g, "");
  } catch {
    return url;
  }
}

function useNow(running: boolean) {
  const [now, setNow] = useState(() => performance.now());
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => setNow(performance.now()), 250);
    return () => clearInterval(t);
  }, [running]);
  return now;
}

export type { Manifest };
