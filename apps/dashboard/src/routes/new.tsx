import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { ArrowUpRight, Check, GitBranch, Plus } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { api, ApiError, type Manifest, type Op } from "@/api/client";
import { mod3, type Deploy } from "@/api/modules";
import { q } from "@/api/queries";
import heroClosed from "@/assets/illustrations/carrier-hero.webp";
import heroOpen from "@/assets/illustrations/carrier-hero-open.webp";
import { Command, CopyButton } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { MorphLabel } from "@/components/morph-label";
import { Crumbs, Page } from "@/components/page";
import { PilotLight } from "@/components/pilot";
import { ProblemNote } from "@/components/problem";
import { Qty } from "@/components/qty";
import { BuildLogView, firstError, useBuildLog } from "@/components/start-build-log";
import { Button } from "@/components/ui/button";
import { splitAddress } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { partName, partSub } from "@/lib/names";
import { countWords, dec, int, NNBSP } from "@/lib/format";
import {
  appFor,
  checkGitUrl,
  checkName,
  deployGit,
  deployTemplate,
  frameworkName,
  nameFromGit,
  newProjectManifest,
  slugify,
  starterLine,
  starterOrder,
  pickable,
  starterTitle,
  startersQuery,
  starterThumb,
  suggestName,
  type Source,
  type Starter,
} from "@/lib/starters";

const MB = 1048576;
/** Two columns: the work on the left, the carrier and the plan on the right. */
const GRID = "grid items-start gap-x-12 gap-y-8 lg:grid-cols-[minmax(0,1fr)_360px]";

type Launched = { project: string; app?: string; started: number; source: Source };

type Phase = "compose" | "launching" | "live" | "failed";

/**
 * Start a project: the first minute of Tiffin. Pick a starter (or empty, or a
 * public git URL), name it and give it a colour; the plan on the right is the
 * real plan from the API, exactly what gets created. Create applies it (a
 * signed, undoable change), sets the colour, deploys the starter, and the
 * build streams in on this page until the app answers at its own address.
 */
export function NewProjectPage() {
  useTitle("New project");
  const qc = useQueryClient();
  const search = useRouterState({ select: (s) => s.location.search as Record<string, unknown> });
  const projects = useQuery(q.projects);
  const names = useMemo(() => (projects.data ?? []).map((p) => p.name), [projects.data]);
  const manifests = useQueries({ queries: names.map((n) => ({ ...q.manifest(n), staleTime: 60_000 })) });
  const routes = useMemo(
    () =>
      manifests.flatMap((m) =>
        Object.entries(m.data?.manifest.apps ?? {}).flatMap(([name, a]) => (a.role === "worker" ? [] : (a.routes?.length ? a.routes : [name]).map((r) => r.split(".")[0]))),
      ),
    [manifests],
  );
  const taken = useMemo(() => ({ projects: names, routes }), [names, routes]);
  const starters = useQuery(startersQuery);
  const list = useMemo(() => pickable(starters.data ?? []), [starters.data]);
  const firstRun = projects.isSuccess && names.length === 0;
  const probe = manifests
    .map((m, i) => ({ project: names[i], app: Object.entries(m.data?.manifest.apps ?? {}).find(([, a]) => a.role !== "worker")?.[0] }))
    .find((x) => x.app) as { project: string; app: string } | undefined;
  const dom = useBoxDomain(probe);

  // The choice: a starter id, "empty" or "git". A ?starter= link from the Box preselects one.
  const asked = typeof search.starter === "string" ? search.starter : undefined;
  const [choice, setChoice] = useState<string>(asked ?? "next-postgres");
  const [git, setGit] = useState({ url: "", ref: "", path: "", framework: "next", postgres: true });
  const [typed, setTyped] = useState<string | null>(null);

  const starter = list.find((s) => s.id === choice);
  const source: Source | null =
    choice === "empty" ? { kind: "empty" } : choice === "git" ? { kind: "git", ...git } : starter ? { kind: "starter", starter } : null;
  const suggested = choice === "git" ? nameFromGit(git.url) || "web" : suggestName(starter, taken) || "project";
  const name = typed ?? suggested;
  const check = checkName(name, taken);
  const gitCheck = choice === "git" ? checkGitUrl(git.url) : ({ ok: true } as const);

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
      const app = appFor(source);
      setL({ project: name, app: app?.name, started, source });
      const intent =
        source.kind === "starter" ? `Start ${name} from the ${source.starter.name} starter` : source.kind === "git" ? `Start ${name} from ${shortRepo(git.url)}` : `Start ${name}`;
      await api.apply(desired, plan.data.hash, intent);
      setPhase("launching");
      void qc.invalidateQueries({ queryKey: ["projects"] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
      if (!app) return null;
      const d = source.kind === "starter" ? await deployTemplate(name, app.name, source.starter.id) : await deployGit(name, app.name, git);
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
    const ops = plan.data?.ops ?? [];
    if (settled && plan.data?.project === name && ops.length > 0 && ops.every((o) => o.action === "create") && !create.isPending && phase === "compose") create.mutate();
  };

  const open = phase !== "compose";
  const crumbs = <Crumbs items={[{ label: "Projects", to: "/" }, { label: "New project" }]} />;

  const header = (
    <header className="min-w-0">
      {(firstRun || phase !== "compose") && <Hero open={open} small className="mb-1 -ml-3 lg:hidden" />}
          {firstRun && phase === "compose" ? <p className="label mb-2">{new Intl.DateTimeFormat("en-GB", { weekday: "long", day: "numeric", month: "long" }).format(new Date())}</p> : crumbs}
          <h1 className="sentence mt-2 text-ink" aria-live="polite">
            {phase === "compose" ? (firstRun ? "Your tiffin is packed. Nothing in it yet." : "Start a project.") : phase === "live" ? `${L?.project} is live.` : phase === "failed" ? `${L?.project} didn’t start.` : `Packing ${L?.project}…`}
          </h1>
          <p className="mt-2 max-w-[38rem] text-md text-ink-2">
            {phase === "compose"
              ? "Pick what you’re making and give it a name. It gets its own address with HTTPS, and you can watch it go live."
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
  const hero = <Hero open={open} className="mx-auto -mt-2 -mb-3 max-lg:hidden" />;

  return (
    <Page wide>
      {phase === "compose" ? (
        <form onSubmit={submit} className={GRID}>
          <div className="min-w-0">
            {header}
            <div className="mt-9">
            <Step n={1} label="What are you making?">
              {starters.isError ? (
                <ProblemNote error={starters.error} title="The starters can’t be listed right now." />
              ) : (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Starter">
                  {(list.length ? list : placeholders).map((s) => (
                    <StarterTile key={s.id} s={s} picked={choice === s.id} onPick={() => setChoice(s.id)} loading={!list.length} />
                  ))}
                </div>
              )}
              <div className="mt-4 divide-y divide-rule border-y border-rule" role="radiogroup" aria-label="Or">
                <OptionRow picked={choice === "git"} onPick={() => setChoice("git")} icon={<GitBranch />} title="From a git repo" line={starterLine.git} />
                <OptionRow picked={choice === "empty"} onPick={() => setChoice("empty")} icon={<Plus />} title="Empty project" line={starterLine.empty} />
              </div>
              {choice === "git" && <GitFields git={git} setGit={setGit} check={gitCheck} />}
            </Step>

            <Step n={2} label="Name">
              <div className="flex flex-wrap items-start gap-x-5 gap-y-3">
                <div className="min-w-0 flex-1 basis-64">
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
                    className="ident h-10 w-full rounded-[8px] border border-rule-2 bg-paper-raised px-3 text-[0.9375rem] text-ink outline-none transition-[border-color,box-shadow] focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger"
                  />
                </div>
              </div>
              <p id="pname-note" className="mt-2 min-h-5 text-sm" aria-live="polite">
                {check.ok ? (
                  choice === "empty" ? (
                    <span className="text-ink-3">
                      Its apps will live at addresses like <span className="ident text-ink-2">{name}.{dom}</span>
                    </span>
                  ) : (
                    <span className="text-ink-3">
                      Lives at <span className="ident text-ink">https://{name}.{dom}</span>
                    </span>
                  )
                ) : (
                  <span className="text-danger">{check.why}</span>
                )}
              </p>
            </Step>
            </div>
          </div>

          <div className="min-w-0">
            {hero}
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
          />
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
            (src.kind === "starter" ? deployTemplate(L.project, L.app, src.starter.id) : src.kind === "git" ? deployGit(L.project, L.app, src) : Promise.reject())
              .then((d) => setDeployId(d.id))
              .catch(() => setPhase("failed"));
          }}
        />
      )}
    </Page>
  );
}

// ───────────────────────── pieces ─────────────────────────

/** The carrier, closed; it opens (cross-fade, the same registration) once a project starts. */
function Hero({ open, small, className }: { open: boolean; small?: boolean; className?: string }) {
  return (
    <div className={cn("relative shrink-0", small ? "size-[132px]" : "-my-6 size-[248px]", className)} aria-hidden>
      <img src={heroClosed} alt="" width={248} height={248} className={cn("absolute inset-0 size-full transition-opacity duration-[600ms] ease-[var(--ease-out)]", open && "opacity-0")} />
      <img src={heroOpen} alt="" width={248} height={248} className={cn("absolute inset-0 size-full opacity-0 transition-opacity duration-[600ms] ease-[var(--ease-out)]", open && "opacity-100")} />
    </div>
  );
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

const placeholders = starterOrder.map((id) => ({ id, name: "", app: "", framework: "static", services: [], description: "", fragment: { apps: {} }, files: 0, bytes: 0 })) as Starter[];

function StarterTile({ s, picked, onPick, loading }: { s: Starter; picked: boolean; onPick: () => void; loading?: boolean }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={picked}
      onClick={onPick}
      disabled={loading}
      className={cn(
        "group relative flex flex-col overflow-hidden rounded-[12px] border bg-paper-raised text-left transition-[border-color,box-shadow,transform] duration-[var(--dur-state)] ease-[var(--ease-out)] active:scale-[0.985]",
        picked ? "border-brass shadow-[0_0_0_1px_var(--brass)]" : "border-rule-2 hover:border-rule-3",
      )}
    >
      <span className="block aspect-[16/10] w-full bg-paper-sunk max-sm:aspect-[16/7]">
        {starterThumb[s.id] && <img src={starterThumb[s.id]} alt="" width={320} height={200} className="size-full object-contain p-1.5 transition-transform duration-[var(--dur-enter)] ease-[var(--ease-out)] group-hover:scale-[1.03]" />}
      </span>
      <span className="flex flex-col gap-1 border-t border-rule px-3 pt-2.5 pb-3">
        <span className="flex items-center justify-between gap-2 text-[0.875rem] font-[550] text-ink">
          {loading ? <span className="h-4 w-20 rounded bg-paper-sunk" /> : (starterTitle[s.id] ?? s.name)}
          <span
            aria-hidden
            className={cn(
              "grid size-4 shrink-0 place-items-center rounded-full border transition-colors",
              picked ? "border-brass bg-brass text-on-brass" : "border-rule-3 text-transparent",
            )}
          >
            <Check className="size-2.5" strokeWidth={3} />
          </span>
        </span>
        <span className="text-[0.78125rem] leading-[1.125rem] text-ink-3">{starterLine[s.id] ?? s.description}</span>
      </span>
    </button>
  );
}

function OptionRow({ picked, onPick, icon, title, line }: { picked: boolean; onPick: () => void; icon: ReactNode; title: string; line: string }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={picked}
      onClick={onPick}
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
    </button>
  );
}

const gitFrameworks = [
  { value: "next", label: "Next.js" },
  { value: "hono", label: "Hono" },
  { value: "bun", label: "Bun" },
  { value: "static", label: "Static site" },
];

function GitFields({
  git,
  setGit,
  check,
}: {
  git: { url: string; ref: string; path: string; framework: string; postgres: boolean };
  setGit: (g: typeof git) => void;
  check: { ok: boolean; why?: string };
}) {
  const field = "h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.84375rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";
  return (
    <div className="mt-4 grid gap-3 sm:grid-cols-[minmax(0,1fr)_9rem_9rem]">
      <label className="sm:col-span-3">
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
      <label>
        <span className="mb-1 block text-xs text-ink-3">Folder</span>
        <input value={git.path} onChange={(e) => setGit({ ...git, path: e.target.value })} placeholder="the top" spellCheck={false} className={cn(field, "ident")} />
      </label>
      <label>
        <span className="mb-1 block text-xs text-ink-3">Framework</span>
        <select value={git.framework} onChange={(e) => setGit({ ...git, framework: e.target.value })} className={field}>
          {gitFrameworks.map((f) => (
            <option key={f.value} value={f.value}>
              {f.label}
            </option>
          ))}
        </select>
      </label>
      <label className="flex items-center gap-2 text-sm text-ink-2 sm:col-span-3">
        <input type="checkbox" checked={git.postgres} onChange={(e) => setGit({ ...git, postgres: e.target.checked })} className="size-4 accent-[var(--brass)]" />
        Give it a Postgres database; it reads <span className="ident text-[0.75rem]">DATABASE_URL</span>
      </label>
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
            ? "Pick a starter and a free name to see the plan."
            : clash
              ? `There’s already a project called ${name}. Pick another name.`
              : planError
              ? "The box can’t plan this yet."
              : !plan
              ? "Planning…"
              : `${countWords(things, "thing", "things", true)}, ready in about a minute.`}
        </p>
      </div>
      <div className="px-5">
        {planError ? (
          <ProblemNote error={planError} className="my-4" title="This can’t be planned." />
        ) : (
          <ol className={cn("divide-y divide-rule transition-opacity duration-[var(--dur-state)]", (pending || blocked) && "opacity-50")}>
            {sortOps(plan ? ops : skeletonOps(source)).map((o, i) => (
              <OpRow key={o.address + i} op={o} project={name} />
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
  if (source?.kind === "starter") for (const s of source.starter.services ?? []) ops.push({ action: "create", address: `service/${s}`, risk: "reversible", reason: "" });
  return ops;
}

function OpRow({ op, project }: { op: Op; project: string }) {
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
    line = a.framework === "static" ? "Static files, served instantly" : `${frameworkName(String(a.framework ?? ""))}${n > 1 ? `, ${countWords(n, "copy", "copies")}` : ""}`;
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
  const log = useBuildLog(L.project, L.app ?? "", deployId, deploy?.createdAt);
  const status = deploy?.status;
  const now = useNow(phase === "launching");
  const since = (now - L.started) / 1000;
  const url = deploy?.url;
  const host = url?.replace(/^https?:\/\//, "");
  const what = L.source.kind === "starter" ? `the ${L.source.starter.name} starter` : L.source.kind === "git" ? shortRepo(L.source.url) : "";

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

        {phase === "failed" && <Failed L={L} deploy={deploy} error={error} logText={log.lines.map((l) => l.text).join("\n")} retry={retry} />}

        {L.app && deployId && (
          <section className="mt-8" aria-label="Build log">
            <div className="mb-2 flex items-baseline justify-between gap-3">
              <h2 className="label">Build log</h2>
              <span className="text-xs text-ink-3">{log.live ? "Following as it builds" : log.done ? `${countWords(log.lines.length, "line")}` : "Connecting…"}</span>
            </div>
            <BuildLogView lines={log.lines} live={log.live} t0={log.t0} waiting={phase === "launching"} maxHeight="22rem" />
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

function Failed({ L, deploy, error, logText, retry }: { L: { project: string; app?: string }; deploy?: Deploy; error: unknown; logText: string; retry: () => void }) {
  const cause = firstError(deploy?.error ?? "") ?? firstError(logText);
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
        {L.project} exists and nothing else on the box changed. Try again, open the app to change it, or undo the project from the Ledger.
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
            Undo from the Ledger
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
  log: ReturnType<typeof useBuildLog>;
}) {
  const url = deploy?.url;
  const host = url?.replace(/^https?:\/\//, "");
  const thumb = L.source.kind === "starter" ? starterThumb[L.source.starter.id] : undefined;
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
              <NextStep n={1} title="Connect your agent" line="Claude Code can plan changes to this box. Anything risky waits for you to approve it with a passkey.">
                <Command cmd="claude mcp add tiffin -- tiffin mcp" />
              </NextStep>
              <NextStep n={2} title="Edit it locally" line={`Pull ${L.project}’s config into a folder, change it, and tiffin apply shows the plan first.`}>
                <Command cmd={`tiffin pull --project ${L.project}`} />
              </NextStep>
              <NextStep n={3} title="Add a service" line="Storage, email, sign-in or a schedule. It stages a change you review before anything happens.">
                <Button asChild size="md">
                  <Link to="/projects/$project" params={{ project: L.project }}>
                    Open {L.project}
                  </Link>
                </Button>
              </NextStep>
            </ol>
            <details className="group mt-6">
              <summary className="cursor-pointer list-none text-sm text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
                <span className="inline-block transition-transform group-open:rotate-90">›</span> Build log, {countWords(log.lines.length, "line")}
              </summary>
              <BuildLogView lines={log.lines} live={log.live} t0={log.t0} className="mt-2" maxHeight="20rem" />
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
            {L.project} is in the box with its config. Add an app or a service from its page; each one is planned and shown to you first.
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
      <div className="relative grid aspect-[16/10] place-items-center bg-paper">
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
 * The box's domain with its port, as apps get it: "tiffin.localhost:8470". From the dashboard's own
 * address on a box; on a local dev server, from any app the box already serves.
 */
export function useBoxDomain(probe?: { project: string; app: string }) {
  const h = location.hostname;
  const own = h.startsWith("dashboard.") ? h.slice("dashboard.".length) + (location.port && location.port !== "443" ? `:${location.port}` : "") : undefined;
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

function useDebounced<T>(value: T, ms: number): T {
  const key = JSON.stringify(value);
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, ms]);
  return v;
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
