import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Check, GitBranch, X } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { useState, type FormEvent, type ReactNode } from "react";
import { mod3 } from "@/api/modules";
import { cn } from "@/lib/cn";
import { mcpCommand } from "@/lib/mcp";
import { checkGitUrl, deployGit, deployTemplate, frameworkName, rememberNextDeploy, starterLine, pickable, startersQuery, starterThumb, type NextDeploy } from "@/lib/starters";
import { Command } from "./copy";
import { MorphLabel } from "./morph-label";
import { ProblemNote } from "./problem";
import { Button } from "./ui/button";

/**
 * Deploy an app without a terminal: a starter that matches its framework, or
 * one commit of a public git repository. It rises like the plan tray; the
 * terminal ways stay below as a quiet block. A deploy is reversible (the
 * previous version keeps serving until the new one is healthy), so it starts
 * at once and opens the deploy's live build log.
 */
export function DeployTray({
  project,
  app,
  framework,
  open,
  onOpenChange,
  suggest,
  hasVersions = false,
}: {
  project: string;
  app: string;
  framework?: string;
  open: boolean;
  onOpenChange: (o: boolean) => void;
  suggest?: NextDeploy;
  /** The app already has versions: a starter would replace its code, so nothing is preselected. */
  hasVersions?: boolean;
}) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="tray-scrim fixed inset-0 z-40 bg-[var(--scrim)]" />
        <D.Content
          aria-describedby={undefined}
          className="tray fixed inset-x-0 bottom-0 z-50 mx-auto flex max-h-[92dvh] w-full max-w-[760px] flex-col overflow-hidden rounded-t-[16px] border border-rule-2 bg-paper-raised shadow-overlay outline-none sm:bottom-5 sm:w-[calc(100%-2.5rem)] sm:rounded-[16px] lg:left-[232px] lg:w-[calc(100%-232px-5rem)]"
        >
          {open && <Body project={project} app={app} framework={framework} suggest={suggest} hasVersions={hasVersions} close={() => onOpenChange(false)} />}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

function Body({
  project,
  app,
  framework,
  suggest,
  hasVersions,
  close,
}: {
  project: string;
  app: string;
  framework?: string;
  suggest?: NextDeploy;
  hasVersions: boolean;
  close: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const starters = useQuery(startersQuery);
  const fits = pickable(starters.data ?? []).filter((s) => !framework || s.framework === framework);
  const [mode, setMode] = useState<"starter" | "git">(suggest && "git" in suggest ? "git" : "starter");
  const [pick, setPick] = useState<string | undefined>(suggest && "template" in suggest ? suggest.template : undefined);
  // An empty app may start from the first starter; one with versions never gets a sample preselected over its code.
  const chosen = fits.find((s) => s.id === pick) ?? (hasVersions ? undefined : fits[0]);
  const [git, setGit] = useState(suggest && "git" in suggest ? { url: suggest.git.url, ref: suggest.git.ref ?? "", path: suggest.git.path ?? "" } : { url: "", ref: "", path: "" });
  const gitCheck = checkGitUrl(git.url);
  const gitInfo = useQuery({ queryKey: ["git", project], queryFn: () => mod3.git(project), staleTime: Infinity, retry: false });

  const go = useMutation({
    mutationFn: () => (mode === "git" ? deployGit(project, app, git) : deployTemplate(project, app, chosen!.id)),
    onSuccess: (d) => {
      rememberNextDeploy(project, app, null);
      void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
      void qc.invalidateQueries({ queryKey: ["runtime", project, app] });
      close();
      void navigate({ to: "/projects/$project/apps/$app/deploys/$id", params: { project, app, id: d.id } });
    },
  });
  const ok = mode === "git" ? gitCheck.ok : !!chosen;
  const label = go.isPending
    ? "Starting the build…"
    : mode === "git"
      ? `Deploy ${shortRepo(git.url) || "the repository"} to ${app}`
      : chosen
        ? hasVersions
          ? `Replace ${app} with the ${chosen.name} starter`
          : `Deploy the ${chosen.name} starter to ${app}`
        : "Pick a starter";

  return (
    <form
      className="flex min-h-0 flex-col"
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        if (ok && !go.isPending) go.mutate();
      }}
    >
      <div className="mx-auto mt-2 h-1 w-9 shrink-0 rounded-full bg-rule-2" aria-hidden />
      <header className="flex shrink-0 items-start gap-3 border-b border-rule px-5 pt-2 pb-4 sm:px-7">
        <div className="min-w-0 flex-1">
          <D.Title className="text-[1.375rem] leading-7 font-[500] tracking-[-0.02em] text-ink">Deploy {app}</D.Title>
          <p className="mt-1 text-[0.8125rem] text-ink-3">
            Builds on the box. The version that’s live keeps serving until the new one passes its health check.
          </p>
        </div>
        <D.Close asChild>
          <Button variant="ghost" size="icon-sm" aria-label="Close">
            <X />
          </Button>
        </D.Close>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-5 sm:px-7">
        <div role="tablist" aria-label="Build it from" className="mb-4 inline-flex rounded-[8px] border border-rule-2 bg-paper p-0.5 text-[0.8125rem]">
          <Tab on={mode === "starter"} onClick={() => setMode("starter")}>
            A starter
          </Tab>
          <Tab on={mode === "git"} onClick={() => setMode("git")}>
            A git URL
          </Tab>
        </div>

        {mode === "starter" && hasVersions && fits.length > 0 && (
          <p className="mb-3 max-w-[36rem] text-[0.8125rem] text-ink-2">
            A starter replaces {app}’s code with a sample. To ship your own code, deploy from its folder or with git push (below).
          </p>
        )}
        {mode === "starter" ? (
          fits.length === 0 ? (
            <p className="text-sm text-ink-2">
              No starter is built with {frameworkName(framework)}, which is what {app} runs. Deploy it from a git URL or from your terminal.
            </p>
          ) : (
            <div role="radiogroup" aria-label="Starter" className="grid gap-2.5 sm:grid-cols-2">
              {fits.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  role="radio"
                  aria-checked={chosen?.id === s.id}
                  onClick={() => setPick(s.id)}
                  className={cn(
                    "grid grid-cols-[96px_minmax(0,1fr)_16px] items-center gap-3 rounded-[10px] border bg-paper p-2 pr-3 text-left transition-[border-color]",
                    chosen?.id === s.id ? "border-brass shadow-[0_0_0_1px_var(--brass)]" : "border-rule-2 hover:border-rule-3",
                  )}
                >
                  <img src={starterThumb[s.id]} alt="" className="aspect-[16/10] w-full rounded-[6px] bg-paper-sunk object-contain" />
                  <span className="min-w-0">
                    <span className="block text-[0.875rem] font-[550] text-ink">{s.name}</span>
                    <span className="block text-xs text-ink-3">{starterLine[s.id]}</span>
                  </span>
                  <span aria-hidden className={cn("grid size-4 place-items-center rounded-full border", chosen?.id === s.id ? "border-brass bg-brass text-on-brass" : "border-rule-3 text-transparent")}>
                    <Check className="size-2.5" strokeWidth={3} />
                  </span>
                </button>
              ))}
            </div>
          )
        ) : (
          <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_10rem_10rem]">
            <label className="sm:col-span-3">
              <span className="mb-1 block text-xs font-[550] text-ink-2">Repository</span>
              <input
                autoFocus
                value={git.url}
                onChange={(e) => setGit({ ...git, url: e.target.value })}
                placeholder="https://github.com/owner/repo"
                spellCheck={false}
                className={input}
              />
              {git.url && !gitCheck.ok && <span className="mt-1 block text-xs text-danger">{"why" in gitCheck ? gitCheck.why : ""}</span>}
            </label>
            <label>
              <span className="mb-1 block text-xs font-[550] text-ink-2">Branch, tag or commit</span>
              <input value={git.ref} onChange={(e) => setGit({ ...git, ref: e.target.value })} placeholder="default branch" spellCheck={false} className={input} />
            </label>
            <label>
              <span className="mb-1 block text-xs font-[550] text-ink-2">Folder</span>
              <input value={git.path} onChange={(e) => setGit({ ...git, path: e.target.value })} placeholder="the top" spellCheck={false} className={input} />
            </label>
            <p className="self-end pb-2 text-xs text-ink-3">Public https only. The clone shows in the build log.</p>
          </div>
        )}

        {go.isError && <ProblemNote className="mt-4" error={go.error} />}

        <details className="group mt-6 border-t border-rule pt-3" open={hasVersions}>
          <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
            <span className="inline-block transition-transform group-open:rotate-90">›</span> Or from your terminal, or an agent
          </summary>
          <div className="mt-3 grid gap-3 text-[0.8125rem] text-ink-2">
            <Way title="From the app’s folder">
              <Command cmd="tiffin deploy" />
            </Way>
            <Way title="With git push" note={gitInfo.data?.url ? <span className="ident break-all">{gitInfo.data.url}</span> : "adds a remote called tiffin"}>
              <Command cmd="tiffin git-remote --add" />
              <Command className="mt-1.5" cmd="git push tiffin main" />
            </Way>
            <Way title="Let an agent do it">
              <Command cmd={mcpCommand()} wrap />
            </Way>
          </div>
        </details>
      </div>

      <footer className="flex shrink-0 items-center justify-end gap-3 border-t border-rule px-5 py-3.5 sm:px-7">
        <span className="mr-auto flex items-center gap-1.5 text-xs text-ink-3 max-sm:hidden">
          <GitBranch className="size-3.5" /> Reversible: make any earlier version current again.
        </span>
        <D.Close asChild>
          <Button type="button" variant="ghost">
            Cancel
          </Button>
        </D.Close>
        <Button type="submit" variant="primary" size="lg" disabled={!ok || go.isPending} className="max-w-full">
          <span className="truncate">
            <MorphLabel text={label} />
          </span>
        </Button>
      </footer>
    </form>
  );
}

const input =
  "ident h-9 w-full rounded-[7px] border border-rule-2 bg-paper px-2.5 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";

function Tab({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={on}
      onClick={onClick}
      className={cn("h-7 rounded-[6px] px-3 font-[550] transition-colors", on ? "bg-paper-raised text-ink shadow-[var(--top-light),0_0_0_1px_var(--rule-2)]" : "text-ink-3 hover:text-ink")}
    >
      {children}
    </button>
  );
}

function Way({ title, note, children }: { title: string; note?: ReactNode; children: ReactNode }) {
  return (
    <div className="grid gap-x-5 gap-y-1.5 sm:grid-cols-[11rem_minmax(0,1fr)]">
      <div>
        <p className="font-[550] text-ink">{title}</p>
        {note && <p className="text-xs text-ink-3">{note}</p>}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

function shortRepo(url: string) {
  try {
    return new URL(url.trim()).pathname.replace(/^\/|\.git$|\/$/g, "");
  } catch {
    return "";
  }
}
