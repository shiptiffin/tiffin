import { useQueries, useQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { ChevronDown, Clock, FolderPlus, Inbox, KeyRound, LayoutTemplate } from "lucide-react";
import { lazy, Suspense, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, request, type Manifest } from "@/api/client";
import { q, queryClient } from "@/api/queries";
import type { components } from "@/api/schema";
import { BuildSettings, buildNote } from "@/components/build-settings";
import { FrameworkSelect } from "@/components/framework-select";
import { checkPick, emptyPick, GitHubImport, unsupportedWhy, type GitHubPick } from "@/components/github-import";
import { GitHubMark } from "@/components/github-mark";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { addressesOf, defaultAddress } from "@/lib/addresses";
import { asTier, tierRank } from "@/lib/changes";
import { cn } from "@/lib/cn";
import { useDebounced } from "@/lib/debounced";
import { deployGitHub, nameFromRepo, setSecret, type RepoRoot } from "@/lib/github";
import { useMe } from "@/lib/me";
import { PARTS } from "@/lib/names";
import { applyPlan, change, planEdits, serviceWords, type StagedEdit } from "@/lib/staged";
import { pickBuild } from "@/lib/build-config";
import { buildFor, isTested, presetName, presetOf, PRESETS } from "@/lib/frameworks";
import { checkGitUrl, defaultOf, frameworkName, frameworksOf, isKind, KINDS, nameFromGit, rememberNextDeploy, slugify, starterFor, starterLine, startersQuery, type Starter, type StarterKind } from "@/lib/starters";

export type Kind = "app" | "bucket" | "queue" | "cron" | "env";

// A schedule or a queue: the Jobs area's own forms (they call apps or web addresses), loaded when opened.
const ScheduleForm = lazy(() => import("@/routes/jobs/forms").then((m) => ({ default: m.ScheduleForm })));
const QueueForm = lazy(() => import("@/routes/jobs/forms").then((m) => ({ default: m.QueueForm })));

/** The built-in parts that are added (Database, KV, Files, Email, Analytics and Jobs are always there). */
const FRIENDLY: Record<string, { label: string; icon: ReactNode }> = {
  auth: { label: PARTS.auth.name, icon: <KeyRound /> },
};

/**
 * "Add" on a project. Sign-in (Auth) is added the moment you pick it; an
 * app, bucket, queue, schedule or setting asks
 * for a name and the one or two things it needs, then is added. Each is one
 * change in History, with Undo in the toast.
 */
export function AddMenu({ project, manifest, className, trigger, only }: { project: string; manifest?: Manifest; className?: string; trigger?: ReactNode; /** Skip the menu: the trigger opens this one form. */ only?: Kind }) {
  const [open, setOpen] = useState<Kind | null>(null);
  const services = (manifest?.services ?? {}) as Record<string, unknown>;
  const off = ["auth"].filter((s) => !(s in services));
  const dialog = (
    <Dialog open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
      <DialogContent className={open === "cron" || open === "queue" || open === "app" ? "sm:max-w-2xl" : "sm:max-w-lg"}>
        {open === "app" && manifest && <AddApp project={project} manifest={manifest} done={() => setOpen(null)} />}
        {open === "bucket" && manifest && <AddBucket project={project} manifest={manifest} done={() => setOpen(null)} />}
        {(open === "queue" || open === "cron") && (
          <Suspense fallback={<div className="h-96" />}>
            {open === "queue" ? <QueueForm project={project} done={() => setOpen(null)} /> : <ScheduleForm project={project} done={() => setOpen(null)} />}
          </Suspense>
        )}
        {open === "env" && manifest && <AddEnv project={project} manifest={manifest} done={() => setOpen(null)} />}
      </DialogContent>
    </Dialog>
  );
  if (only)
    return (
      <>
        <span className="contents" onClickCapture={() => manifest && setOpen(only)}>
          {trigger}
        </span>
        {dialog}
      </>
    );
  return (
    <>
      <Menu>
        <MenuTrigger asChild>
          {trigger ?? (
            <Button variant="secondary" size="lg" className={className} disabled={!manifest}>
              Add <ChevronDown className="-mr-1 text-ink-3" />
            </Button>
          )}
        </MenuTrigger>
        <MenuContent align="start" className="min-w-60">
          {off.length > 0 && <MenuLabel>Built in, ready in seconds</MenuLabel>}
          {off.map((s) => (
            <MenuItem key={s} className="h-auto py-1.5" onSelect={() => change(project, { kind: "service", service: s, from: "off", to: "on" }, { immediate: true })}>
              {FRIENDLY[s].icon}
              <span className="flex min-w-0 flex-col leading-[1.15rem]">
                <span>{FRIENDLY[s].label}</span>
                <span className="text-xs text-ink-3">{PARTS[s as keyof typeof PARTS].sub}</span>
              </span>
            </MenuItem>
          ))}
          {off.length > 0 && <MenuSeparator />}
          <MenuItem onSelect={() => setOpen("app")}>
            <LayoutTemplate /> {Object.keys(manifest?.apps ?? {}).length ? "Another app…" : "An app…"}
          </MenuItem>
          <MenuItem onSelect={() => setOpen("bucket")}>
            <FolderPlus /> A bucket for files…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("cron")}>
            <Clock /> A scheduled job…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("queue")}>
            <Inbox /> A job queue…
          </MenuItem>
          <MenuItem onSelect={() => setOpen("env")}>
            <KeyRound /> A setting (env var)…
          </MenuItem>
        </MenuContent>
      </Menu>
      {dialog}
    </>
  );
}

const field =
  "h-9 w-full rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.84375rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger";

function Field({ label, note, error, group, children }: { label: string; note?: ReactNode; error?: string | false; group?: boolean; children: ReactNode }) {
  const Tag = group ? "div" : "label";
  return (
    <Tag className="block" role={group ? "group" : undefined} aria-label={group ? label : undefined}>
      <span className="mb-1 block text-xs font-[550] text-ink-2">{label}</span>
      {children}
      {error ? <span className="mt-1 block text-xs text-danger">{error}</span> : note ? <span className="mt-1 block text-xs text-ink-3">{note}</span> : null}
    </Tag>
  );
}

function Shell({
  title,
  lede,
  children,
  submit,
  ok,
  done,
  onSubmit,
}: {
  title: string;
  lede: ReactNode;
  children: ReactNode;
  submit: string;
  ok: boolean;
  done: () => void;
  onSubmit: () => void;
}) {
  return (
    <form
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        if (!ok) return;
        onSubmit();
        done();
      }}
      className="flex min-h-0 flex-col"
    >
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{lede}</DialogDescription>
      </DialogHeader>
      <DialogBody className="grid gap-4">{children}</DialogBody>
      <DialogFooter>
        <span className="mr-auto text-xs text-ink-3 max-sm:hidden">You can undo it afterwards.</span>
        <Button type="button" variant="ghost" onClick={done}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" disabled={!ok}>
          {submit}
        </Button>
      </DialogFooter>
    </form>
  );
}

const slugOk = (s: string) => /^[a-z](-?[a-z0-9]){0,39}$/.test(s) && s.length <= 40;
const say = (what: string) => void what; // the change's own toast says what happened
const set = (project: string, e: Omit<Extract<StagedEdit, { kind: "set" }>, "kind">) => change(project, { kind: "set", ...e }, { immediate: true });

// ───────────────────────── app ─────────────────────────

type GitInspect = components["schemas"]["RuntimeGitInspect"];

/**
 * Where a new app's code comes from: one of your GitHub repositories (first,
 * and the default), a starter (a web app, a static site or an API, each with
 * its framework), or, for code that isn't on GitHub, a public git URL.
 * Nothing is made until Add.
 */
function AddApp({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const router = useRouter();
  const routes = useOtherRoutes(project);
  const { admin } = useMe();
  const starters = useQuery(startersQuery);
  const list = starters.data ?? [];
  const [pick, setPick] = useState<string>("github");
  // The framework picked in each kind (a starter id); a kind not in here uses its default.
  const [chosen, setChosen] = useState<Partial<Record<StarterKind, string>>>({});
  const [gh, setGh] = useState<GitHubPick>(emptyPick);
  const [git, setGit] = useState({ url: "", ref: "", path: "", framework: "next", preset: "nextjs" });
  const fromGitHub = pick === "github";
  const fromGit = pick === "git";
  const kind = isKind(pick) ? pick : undefined;
  const starter: Starter | undefined = kind ? (starterFor(list, chosen[kind]) ?? defaultOf(list, kind)) : undefined;

  // A pasted URL is looked inside (the box fetches it as a deploy would) for the same framework guess GitHub gets.
  const gitUrl = useDebounced(fromGit && checkGitUrl(git.url).ok ? git.url.trim() : "", 500);
  const gitRef = useDebounced(git.ref.trim(), 500);
  const inspect = useQuery({
    queryKey: ["inspect-git", gitUrl, gitRef],
    queryFn: () => request<GitInspect>("POST", "/v1/git/inspect", { url: gitUrl, ...(gitRef ? { ref: gitRef } : {}) }),
    enabled: !!gitUrl,
    retry: false,
    staleTime: 300_000,
  });
  const found = inspect.data?.roots?.find((r) => !r.workspace) ?? inspect.data?.roots?.[0];
  // A new answer fills in how it's built, once; choosing another build afterwards wins.
  const [applied, setApplied] = useState<RepoRoot>();
  const [overridden, setOverridden] = useState(false);
  if (found && found !== applied) {
    setApplied(found);
    setOverridden(false);
    setGit((g) => ({ ...g, framework: found.framework, preset: presetOf(found), path: found.path }));
  }
  const gitUnsupported = fromGit && !overridden ? found?.unsupported : undefined;

  const apps = Object.keys(manifest.apps ?? {});
  const base = fromGitHub ? nameFromRepo(gh.repo) || "web" : fromGit ? nameFromGit(git.url) || "web" : (starter?.app ?? "web");
  const free = (n: string) => !apps.includes(n);
  const suggestion = free(base) ? base : ([2, 3, 4, 5].map((i) => `${base}-${i}`).find(free) ?? "");
  const [typed, setTyped] = useState<string | null>(null);
  const name = typed ?? suggestion;
  const nameErr = !name ? "Give it a name." : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : !free(name) ? `${project} already has an app called ${name}.` : false;
  const urlCheck = checkGitUrl(git.url);
  const ghCheck = checkPick(gh);
  const sourceErr = fromGitHub ? (ghCheck.ok ? false : ghCheck.why) : fromGit ? (!urlCheck.ok ? urlCheck.why : gitUnsupported ? unsupportedWhy(gitUnsupported) : false) : starter ? false : "Pick a starter.";
  // A new app answers at <project>-<app> (or <project> if it is the main
  // app); if another project already answers there, it gets a free name.
  const addr = name ? defaultAddress(project, name, { ...manifest.apps, [name]: {} } as NonNullable<Manifest["apps"]>) : "";
  const route = !routes.includes(addr) ? undefined : addr === `${project}-${name}` ? `${addr}-2` : `${project}-${name}`;
  const framework = fromGitHub ? gh.framework : fromGit ? git.framework : (starter?.framework ?? "bun");
  const needs = fromGitHub || fromGit ? [] : (starter?.services ?? []).filter((s) => !((manifest.services ?? {}) as Record<string, unknown>)[s]);
  // While a repository is still being picked, the list is the whole form.
  const naming = !fromGitHub || !!gh.repo;

  return (
    <Shell
      title={`Add an app to ${project}`}
      lede={
        fromGitHub
          ? "Its first build starts as soon as it’s added; after that, every push to its branch deploys it."
          : "Built on the box from a starter or a public repository. Its page offers the first deploy as soon as it’s added."
      }
      submit={naming && name ? `Add ${name}` : "Add the app"}
      ok={!nameErr && !sourceErr}
      done={done}
      onSubmit={() => {
        const ghPath = gh.path.trim().replace(/^\/+|\/+$/g, "");
        const spec: Record<string, unknown> = fromGitHub
          ? { framework, git: { repo: gh.repo, branch: gh.branch, ...(ghPath ? { path: ghPath } : {}) }, ...pickBuild(gh.build, framework) }
          : fromGit
            ? { framework }
            : { ...(structuredClone(Object.values(starter!.fragment.apps)[0] ?? {}) as Record<string, unknown>), framework };
        if (route) spec.routes = [route];
        const edit: StagedEdit = {
          kind: "set",
          path: ["apps", name],
          to: spec,
          what: fromGitHub
            ? `Add the ${name} app from ${gh.repo} to ${project}`
            : `Add the ${name} app (${starter ? starter.presetName : fromGit ? presetName(git.preset) : frameworkName(framework)}) to ${project}`,
          undo: `${name} is removed again`,
        };
        if (fromGitHub) {
          const app = name;
          const env = gh.env.filter((e) => e.k && e.v);
          void addFromGitHub(project, app, edit, env, gh, (id) => void router.navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app, id } }));
          return;
        }
        change(project, edit, { immediate: true });
        for (const s of needs) change(project, { kind: "service", service: s, from: "off", to: "on" }, { immediate: true });
        if (fromGit) rememberNextDeploy(project, name, { git: { url: git.url.trim(), ref: git.ref, path: git.path } });
        else rememberNextDeploy(project, name, { template: starter!.id });
        say(`add ${name} to ${project}${needs.length ? ` with ${needs.map((s) => serviceWords[s] ?? s).join(", ")}` : ""}`);
      }}
    >
      <RadioGroup aria-label="Build it from" value={fromGit ? "github" : pick} onValueChange={setPick} className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {/* A public git URL is a variant of the GitHub tile: picking the tile again goes back to GitHub. */}
        <PickTile value="github" onClick={() => setPick("github")} icon={<GitHubMark />} title="Your GitHub repository" line="Every push deploys it." />
        {KINDS.map((k) => (
          <PickTile key={k.kind} value={k.kind} thumb={k.thumb} title={k.title} line={k.line} />
        ))}
      </RadioGroup>
      {kind && (
        <div className="grid items-center gap-x-3 gap-y-1 sm:grid-cols-[auto_minmax(0,15rem)_minmax(0,1fr)]">
          <span className="text-xs font-[550] text-ink-2">Framework</span>
          {starter ? (
            <FrameworkSelect
              aria-label="Framework"
              value={starter.id}
              onChange={(id) => setChosen((c) => ({ ...c, [kind]: id }))}
              options={frameworksOf(list, kind).map((s) => ({ id: s.id, logo: s.preset, name: s.presetName }))}
            />
          ) : (
            <span className="h-9" />
          )}
          <span className="text-xs text-ink-3 sm:pl-1">{starter?.description}</span>
        </div>
      )}
      {fromGitHub && (
        <div>
          <GitHubImport value={gh} onChange={setGh} admin={admin} />
          {gh.repo && !ghCheck.ok && !gh.unsupported && <p className="mt-2 text-sm text-danger">{ghCheck.why}</p>}
          <SwitchLink onClick={() => setPick("git")}>Not on GitHub? Paste a public git URL</SwitchLink>
        </div>
      )}
      {fromGit && (
        <div>
          <p className="text-sm text-ink-3">{starterLine.git} It doesn’t redeploy when the repository changes.</p>
          <div className="mt-3 grid gap-3 sm:grid-cols-[minmax(0,1fr)_11rem]">
            <Field label="Repository" error={!!git.url && !urlCheck.ok && urlCheck.why}>
              <input
                autoFocus
                value={git.url}
                onChange={(e) => setGit({ ...git, url: e.target.value })}
                placeholder="https://gitlab.com/owner/repo"
                spellCheck={false}
                aria-invalid={!!git.url && !urlCheck.ok}
                className={cn(field, "ident")}
              />
            </Field>
            <Field label="Branch, tag or commit">
              <input value={git.ref} onChange={(e) => setGit({ ...git, ref: e.target.value })} placeholder="default branch" spellCheck={false} className={cn(field, "ident")} />
            </Field>
          </div>
          <div className="mt-3 max-w-[22rem]">
            <Field label={found && !inspect.isFetching && git.preset === presetOf(found) ? "Framework · detected" : "Framework"} group>
              <FrameworkSelect
                aria-label="Framework"
                value={git.preset}
                onChange={(preset) => {
                  setOverridden(true);
                  setGit({ ...git, preset, framework: buildFor(preset, found) });
                }}
                options={PRESETS}
              />
            </Field>
          </div>
          <p className="mt-2 text-sm text-ink-3">
            {inspect.isFetching ? (
              "Looking inside the repository…"
            ) : gitUnsupported ? (
              <span className="text-danger">{unsupportedWhy(gitUnsupported)}</span>
            ) : found ? (
              <>
                Found {found.path ? <>in <span className="ident text-[0.75rem] text-ink-2">{found.path}</span></> : "at the top of the repository"}: {found.why}. {!isTested(git.preset) && buildNote(git.framework)}
              </>
            ) : (
              <>Paste the address and Tiffin looks inside to pick the framework.</>
            )}
          </p>
          <BuildSettings className="mt-2" path={git.path} onPath={(path) => setGit({ ...git, path })} />
          <SwitchLink onClick={() => setPick("github")}>Use one of your GitHub repositories instead</SwitchLink>
        </div>
      )}
      {naming && (
        <Field
          label="App name"
          error={nameErr}
          note={
            <>
              Answers at <span className="ident text-ink-2">{route ?? addr}.…</span>
              {needs.length > 0 && <> · also adds {needs.map((s) => serviceWords[s] ?? s).join(", ")}</>}
            </>
          }
        >
          <input value={name} onChange={(e) => setTyped(slugify(e.target.value))} spellCheck={false} autoComplete="off" aria-invalid={!!nameErr} className={cn(field, "ident")} />
        </Field>
      )}
    </Shell>
  );
}

/**
 * Addresses other projects already use, so a new app's name can't clash with
 * them. Read when the Add app form opens, not on every page that offers it.
 */
function useOtherRoutes(project: string) {
  const projects = useQuery(q.projects);
  const others = (projects.data ?? []).map((x) => x.name).filter((n) => n !== project);
  const om = useQueries({ queries: others.map((n) => ({ ...q.manifest(n), staleTime: 60_000 })) });
  return om.flatMap((x) => (x.data ? addressesOf(x.data.project, x.data.manifest.apps) : []));
}

/**
 * Adds an app built from a GitHub repository, then starts its first build:
 * the same path a change takes (plan, then apply, with Undo in the toast),
 * then its env saved as secrets, then a deploy of its branch. A plan that has
 * to ask first goes through the confirm dialog instead; the app's page then
 * offers the first deploy.
 */
async function addFromGitHub(project: string, app: string, edit: StagedEdit, env: GitHubPick["env"], gh: GitHubPick, watch: (id: string) => void) {
  const why = (err: unknown) => (err instanceof ApiError ? (err.problem.detail ?? err.message) : err instanceof Error ? err.message : String(err));
  try {
    const { desired, plan } = await planEdits(project, [edit]);
    if (tierRank[asTier(plan.risk)] > tierRank.reversible) {
      change(project, edit, { immediate: true });
      return;
    }
    await applyPlan({ project, edits: [edit], desired, plan });
  } catch (err) {
    toast({ title: `Couldn’t add ${app} to ${project}.`, detail: why(err), tone: "danger" });
    return;
  }
  try {
    for (const e of env) await setSecret(project, e.k, e.v);
    const d = await deployGitHub(project, app);
    void queryClient.invalidateQueries({ queryKey: ["deploys", project, app] });
    toast({ title: `Building ${app} from ${gh.repo}.`, detail: `Every push to ${gh.branch} deploys it from now on.`, action: { label: "Watch", run: () => watch(d.id) } });
  } catch (err) {
    toast({ title: `${app} is added, but its first build didn’t start.`, detail: `${why(err)} Deploy it from its page.`, tone: "danger" });
  }
}

function SwitchLink({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" onClick={onClick} className="mt-3 text-sm font-[550] text-brass-ink hover:underline hover:underline-offset-4">
      {children}
    </button>
  );
}

function PickTile({ value, onClick, thumb, icon, title, line }: { value: string; onClick?: () => void; thumb?: string; icon?: ReactNode; title: string; line?: string }) {
  return (
    <RadioItem
      value={value}
      onClick={onClick}
      className="flex flex-col overflow-hidden rounded-[9px] border border-rule-2 bg-paper text-left transition-[border-color] duration-[var(--dur-state)] hover:border-rule-3 data-[state=checked]:border-brass data-[state=checked]:shadow-[0_0_0_1px_var(--brass)]"
    >
      <span className={cn("grid aspect-[16/9] place-items-center bg-paper-sunk text-ink-3 [&_svg]:size-5", thumb && "art-well")}>{thumb ? <img src={thumb} alt="" className="size-full object-contain p-1" /> : icon}</span>
      <span className="border-t border-rule px-2.5 py-2">
        <span className="block text-[0.8125rem] font-[550] text-ink">{title}</span>
        {line && <span className="line-clamp-2 block text-[0.71875rem] leading-4 text-ink-3">{line}</span>}
      </span>
    </RadioItem>
  );
}

// ───────────────────────── bucket, env ─────────────────────────

function AddBucket({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const [name, setName] = useState("");
  const [pub, setPub] = useState(false);
  const existing = Object.keys(manifest.services?.storage?.buckets ?? {});
  const err = !name ? false : !slugOk(name) ? "Lowercase letters, digits and dashes, starting with a letter." : existing.includes(name) ? `${project} already has a bucket called ${name}.` : false;
  return (
    <Shell
      title={`Add a bucket to ${project}`}
      lede="S3-compatible storage for files your apps write. Private buckets serve files only through signed links."
      submit={name ? `Add ${name}` : "Add the bucket"}
      ok={!!name && !err}
      done={done}
      onSubmit={() => {
        set(project, { path: ["services", "storage", "buckets", name], to: { public: pub }, what: `Add the ${name} bucket (${pub ? "public" : "private"}) to ${project}`, undo: `${name} goes to the trash` });
        say(`add the ${name} bucket`);
      }}
    >
      <Field label="Name" error={err}>
        <input autoFocus value={name} onChange={(e) => setName(slugify(e.target.value))} placeholder="uploads" spellCheck={false} className={cn(field, "ident")} />
      </Field>
      <label className="flex items-start gap-2.5 text-sm text-ink-2">
        <input type="checkbox" checked={pub} onChange={(e) => setPub(e.target.checked)} className="mt-0.5 size-4 accent-[var(--brass)]" />
        <span>
          <b className="font-[550] text-ink">Public.</b> Anyone with a file’s address can read it. Use it for images and downloads, never for people’s uploads.
        </span>
      </label>
    </Shell>
  );
}

function AddEnv({ project, manifest, done }: { project: string; manifest: Manifest; done: () => void }) {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const existing = manifest.env ?? {};
  const valid = /^[A-Z_][A-Z0-9_]{0,127}$/.test(key);
  const replacing = key in existing;
  return (
    <Shell
      title={`Add an environment variable to ${project}`}
      lede={
        <>
          Plain settings every app in {project} reads, kept in <span className="ident">tiffin.config.ts</span>. Keys and passwords go on Environment Variables, as secrets.
        </>
      }
      submit={replacing ? `Change ${key}` : key ? `Add ${key}` : "Add the setting"}
      ok={valid}
      done={done}
      onSubmit={() => {
        set(project, {
          path: ["env", key],
          from: existing[key],
          to: value,
          what: replacing ? `Set ${key} to “${value}” in ${project}` : `Add ${key}=“${value}” to ${project}`,
          undo: replacing ? `${key} goes back to “${existing[key]}”` : `${key} is removed`,
        });
        say(`${replacing ? "change" : "add"} ${key}`);
      }}
    >
      <div className="grid gap-3 sm:grid-cols-[minmax(0,13rem)_minmax(0,1fr)]">
        <Field label="Name" error={!!key && !valid && "Capitals, digits and underscores, like LOG_LEVEL."}>
          <input
            autoFocus
            value={key}
            onChange={(e) => setKey(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
            placeholder="LOG_LEVEL"
            spellCheck={false}
            className={cn(field, "ident")}
          />
        </Field>
        <Field label="Value" note={replacing ? `Now “${existing[key]}”.` : undefined}>
          <input value={value} onChange={(e) => setValue(e.target.value)} placeholder="info" spellCheck={false} className={cn(field, "ident")} />
        </Field>
      </div>
    </Shell>
  );
}
