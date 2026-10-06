import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check, Lock, Plus, RotateCw, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { GitHubMark } from "@/components/github-mark";
import { Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { connectGitHub, ENV_NAME, githubQuery, installGitHub, parseEnv, repoQuery, reposQuery, shortSha, type GitHubRepo, type RepoRoot } from "@/lib/github";
import { BuildSettings, buildNote, UNKNOWN_BUILD } from "@/components/build-settings";
import { frameworkName } from "@/lib/starters";
import { relative } from "@/lib/time";

/** What the person picked: a repository, its branch and folder, how to build it, its env. */
export type GitHubPick = {
  repo: string;
  branch: string;
  path: string;
  framework: string;
  env: Array<{ k: string; v: string }>;
};

export const emptyPick: GitHubPick = { repo: "", branch: "", path: "", framework: "next", env: [] };

/** Whether a pick can be created: a repository, a branch, and env names the box accepts. */
export function checkPick(p: GitHubPick): { ok: true } | { ok: false; why: string } {
  if (!p.repo) return { ok: false, why: "Pick a repository." };
  if (!p.branch) return { ok: false, why: "Pick a branch." };
  const bad = p.env.find((e) => (e.k || e.v) && !ENV_NAME.test(e.k));
  if (bad) return { ok: false, why: `${bad.k || "A variable"} isn’t a valid name: capital letters, digits and _, not starting with a digit.` };
  return { ok: true };
}

const field =
  "h-9 w-full rounded-[7px] border border-rule-2 bg-paper-raised px-2.5 text-[0.84375rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)]";

/**
 * Import from GitHub, inside New project: connect (if needed), pick a
 * repository, then its branch, folder, framework and env. Plain words; the
 * plan on the right shows exactly what gets made.
 */
export function GitHubImport({ value, onChange, admin }: { value: GitHubPick; onChange: (p: GitHubPick) => void; admin: boolean }) {
  const st = useQuery(githubQuery);
  const connect = useMutation({ mutationFn: () => connectGitHub() });
  const install = useMutation({ mutationFn: installGitHub });

  if (st.isPending) return <Skeleton className="mt-4 h-40" />;
  if (st.isError) return <ProblemNote className="mt-4" error={st.error} title="The box can’t say whether GitHub is connected." />;
  const s = st.data;

  if (!s.connected || (s.installations ?? []).length === 0) {
    const needsApp = !s.connected;
    return (
      <div className="mt-4 rounded-[12px] border border-rule-2 bg-paper-raised px-5 py-4">
        <div className="flex items-start gap-3">
          <GitHubMark className="mt-0.5 size-5 text-ink" />
          <div className="min-w-0 flex-1">
            <p className="text-[0.9375rem] font-[550] text-ink">{needsApp ? "Connect GitHub first." : "Choose the repositories the box may see."}</p>
            <p className="mt-1 text-sm text-ink-2">
              {needsApp
                ? "It takes a minute: GitHub asks you to confirm a small app for this box, then you pick repositories. You come back here after."
                : "The box’s GitHub app isn’t installed on any account yet."}
            </p>
            {!s.reachable && needsApp && <p className="mt-2 text-sm text-warn-ink">{s.reachableHint}</p>}
            <div className="mt-3 flex flex-wrap gap-2">
              {admin && s.reachable && needsApp && (
                <Button type="button" variant="primary" size="md" onClick={() => connect.mutate()} disabled={connect.isPending}>
                  <GitHubMark /> {connect.isPending ? "Opening GitHub…" : "Connect GitHub"}
                </Button>
              )}
              {admin && !needsApp && (
                <Button type="button" variant="primary" size="md" onClick={() => install.mutate()} disabled={install.isPending}>
                  {install.isPending ? "Opening GitHub…" : "Install on repositories"}
                </Button>
              )}
              <Button asChild size="md" variant={admin ? "ghost" : "secondary"}>
                <Link to="/settings/git" search={{}}>
                  {admin ? "Settings › Git" : "Ask an admin: Settings › Git"}
                </Link>
              </Button>
            </div>
            {(connect.isError || install.isError) && <ProblemNote className="mt-3" error={connect.error ?? install.error} />}
          </div>
        </div>
      </div>
    );
  }

  if (!value.repo) return <RepoPicker onPick={(r) => onChange({ ...emptyPick, repo: r.fullName, branch: r.defaultBranch })} admin={admin} />;
  return <RepoSetup value={value} onChange={onChange} />;
}

function RepoPicker({ onPick, admin }: { onPick: (r: GitHubRepo) => void; admin: boolean }) {
  const qc = useQueryClient();
  const repos = useQuery(reposQuery());
  const [q, setQ] = useState("");
  const [all, setAll] = useState(false);
  const install = useMutation({ mutationFn: installGitHub });
  const refresh = useMutation({
    mutationFn: () => qc.fetchQuery({ ...reposQuery(true), staleTime: 0 }),
  });
  const list = useMemo(() => repos.data?.repos ?? [], [repos.data]);
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const hits = needle ? list.filter((r) => r.fullName.toLowerCase().includes(needle) || (r.description ?? "").toLowerCase().includes(needle)) : list;
    return all || needle ? hits.slice(0, 200) : hits.slice(0, 6);
  }, [list, q, all]);

  return (
    <div className="mt-4">
      <div className="relative">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-ink-4" />
        <label htmlFor="repo-q" className="sr-only">
          Search your repositories
        </label>
        <input
          id="repo-q"
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={repos.data ? `Search ${repos.data.total} ${repos.data.total === 1 ? "repository" : "repositories"}` : "Search your repositories"}
          spellCheck={false}
          autoComplete="off"
          className={cn(field, "pl-8")}
        />
      </div>
      {repos.isError && <ProblemNote className="mt-3" error={repos.error} />}
      <ul className="mt-3 divide-y divide-rule border-y border-rule" role="listbox" aria-label="Repositories">
        {repos.isPending &&
          [0, 1, 2].map((i) => (
            <li key={i} className="py-3">
              <Skeleton className="h-4 w-56" />
            </li>
          ))}
        {repos.isSuccess && shown.length === 0 && <li className="py-4 text-sm text-ink-3">{q ? `No repository matches “${q}”.` : "The app can’t see any repositories yet."}</li>}
        {shown.map((r) => (
          <li key={r.fullName}>
            <button
              type="button"
              role="option"
              aria-selected={false}
              onClick={() => onPick(r)}
              className="group grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 px-1 py-2.5 text-left transition-colors hover:bg-[color-mix(in_oklch,var(--ink)_2.5%,transparent)]"
            >
              <span className="min-w-0">
                <span className="flex min-w-0 items-center gap-1.5 text-[0.875rem]">
                  <span className="truncate text-ink-3">{r.owner}/</span>
                  <span className="truncate font-[550] text-ink">{r.name}</span>
                  {r.private && <Lock className="size-3 shrink-0 text-ink-3" aria-label="Private" />}
                </span>
                <span className="block truncate text-xs text-ink-3">
                  {r.connected?.length ? `Already deploys to ${r.connected.join(", ")} · ` : ""}
                  {r.description ? `${r.description} · ` : ""}updated {relative(r.pushedAt)}
                </span>
              </span>
              <span className="text-[0.8125rem] font-[550] text-ink-3 group-hover:text-brass-ink">Import</span>
            </button>
          </li>
        ))}
      </ul>
      <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-[0.8125rem] text-ink-3">
        <span>
          {!q && !all && list.length > shown.length ? (
            <button type="button" className="text-ink-2 hover:text-ink" onClick={() => setAll(true)}>
              Show all {list.length}
            </button>
          ) : (
            <>Most recently updated first.</>
          )}
        </span>
        <span className="flex items-center gap-3">
          <button type="button" className="inline-flex items-center gap-1 hover:text-ink" onClick={() => refresh.mutate()} disabled={refresh.isPending}>
            <RotateCw className={cn("size-3.5", refresh.isPending && "animate-spin")} /> Refresh
          </button>
          {admin && (
            <button type="button" className="inline-flex items-center gap-1 text-ink-2 hover:text-ink" onClick={() => install.mutate()} disabled={install.isPending}>
              <Plus className="size-3.5" /> Missing one? Choose more on GitHub
            </button>
          )}
        </span>
      </div>
    </div>
  );
}

function RepoSetup({ value, onChange }: { value: GitHubPick; onChange: (p: GitHubPick) => void }) {
  const det = useQuery(repoQuery(value.repo, value.branch || undefined));
  const d = det.data;
  const roots = d?.roots ?? [];
  // First look at a repository: its suggested folder and framework.
  const seeded = useRef("");
  useEffect(() => {
    if (!d || seeded.current === value.repo) return;
    seeded.current = value.repo;
    const root = (d.roots ?? []).find((r) => r.path === d.suggested) ?? d.roots?.[0];
    onChange({ ...value, branch: value.branch || d.defaultBranch, path: root?.path ?? "", framework: root?.framework ?? value.framework });
  }, [d, value, onChange]);

  const set = (patch: Partial<GitHubPick>) => onChange({ ...value, ...patch });
  const pickRoot = (r: RepoRoot) => set({ path: r.path, framework: r.framework });
  const appRoots = roots.filter((r) => !r.workspace);

  return (
    <div className="mt-4">
      <div className="flex items-center gap-3 rounded-[10px] border border-rule-2 bg-paper-raised px-3.5 py-2.5">
        <GitHubMark className="text-ink" />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[0.875rem] font-[550] text-ink">{value.repo}</span>
          <span className="block truncate text-xs text-ink-3">
            {d?.commit ? (
              <>
                Latest on {value.branch}: <span className="ident text-[0.6875rem]">{shortSha(d.commit.sha)}</span> {d.commit.message}
                {d.commit.author ? ` · ${d.commit.author}` : ""}
              </>
            ) : det.isPending ? (
              "Looking inside…"
            ) : (
              "No commits on this branch yet."
            )}
          </span>
        </span>
        <Button type="button" size="sm" variant="ghost" onClick={() => onChange(emptyPick)}>
          Change
        </Button>
      </div>
      {det.isError && <ProblemNote className="mt-3" error={det.error} />}
      {d && (d.connected ?? []).length > 0 && <p className="mt-2 text-sm text-warn-ink">It already deploys to {d.connected!.join(", ")}. Importing again makes a second copy.</p>}

      <div className="mt-4 max-w-[22rem]">
        <label>
          <span className="mb-1 block text-xs text-ink-3">Production branch · every push to it goes live</span>
          <select value={value.branch} onChange={(e) => set({ branch: e.target.value })} className={cn(field, "ident")} disabled={!d}>
            {(d?.branches?.length ? d.branches : [value.branch]).map((b) => (
              <option key={b} value={b}>
                {b}
                {b === d?.defaultBranch ? " (default)" : ""}
              </option>
            ))}
          </select>
        </label>
      </div>

      {appRoots.length > 1 && (
        <fieldset className="mt-4">
          <legend className="mb-1 text-xs text-ink-3">Which app? This repository has several.</legend>
          <div className="divide-y divide-rule border-y border-rule" role="radiogroup">
            {appRoots.map((r) => {
              const on = r.path === value.path;
              return (
                <button
                  type="button"
                  role="radio"
                  aria-checked={on}
                  key={r.path || "."}
                  onClick={() => pickRoot(r)}
                  className="grid w-full grid-cols-[minmax(0,1fr)_16px] items-center gap-x-3 py-2 text-left hover:bg-[color-mix(in_oklch,var(--ink)_2.5%,transparent)]"
                >
                  <span className="min-w-0">
                    <span className="ident block truncate text-[0.8125rem] text-ink">{r.path || "the top of the repository"}</span>
                    <span className="block truncate text-xs text-ink-3">
                      {r.name && r.name !== r.path.split("/").pop() ? `${r.name} · ` : ""}
                      {r.why}
                    </span>
                  </span>
                  <span aria-hidden className={cn("grid size-4 place-items-center rounded-full border", on ? "border-brass bg-brass text-on-brass" : "border-rule-3 text-transparent")}>
                    <Check className="size-2.5" strokeWidth={3} />
                  </span>
                </button>
              );
            })}
          </div>
        </fieldset>
      )}
      {appRoots.length <= 1 && d && (
        <p className="mt-3 text-sm text-ink-3">
          {appRoots[0] ? (
            <>
              Found {frameworkName(appRoots[0].framework)} {appRoots[0].path ? <>in <span className="ident text-[0.75rem] text-ink-2">{appRoots[0].path}</span></> : "at the top"}: {appRoots[0].why}.
            </>
          ) : (
            UNKNOWN_BUILD
          )}
        </p>
      )}
      {appRoots.length > 0 && buildNote(value.framework) && <p className="mt-2 text-sm text-ink-3">{buildNote(value.framework)}</p>}
      <BuildSettings className="mt-3" framework={value.framework} onFramework={(framework) => set({ framework })} path={value.path} onPath={(path) => set({ path })} />

      <EnvRows env={value.env} onChange={(env) => set({ env })} />

    </div>
  );
}

function EnvRows({ env, onChange }: { env: GitHubPick["env"]; onChange: (e: GitHubPick["env"]) => void }) {
  const rows = env.length ? env : [];
  const setRow = (i: number, patch: Partial<{ k: string; v: string }>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="mt-5">
      <p className="text-xs text-ink-3">Environment variables · kept encrypted on the box, as secrets</p>
      {rows.length > 0 && (
        <div className="mt-1.5 flex flex-col gap-1.5">
          {rows.map((r, i) => (
            <div key={i} className="grid grid-cols-[minmax(0,11rem)_minmax(0,1fr)_28px] gap-1.5">
              <input
                value={r.k}
                onChange={(e) => setRow(i, { k: e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_") })}
                onPaste={(e) => {
                  const text = e.clipboardData.getData("text");
                  const parsed = parseEnv(text);
                  if (parsed.length > 1 || (parsed.length === 1 && text.includes("="))) {
                    e.preventDefault();
                    onChange([...rows.slice(0, i), ...parsed, ...rows.slice(i + 1)].filter((x) => x.k || x.v));
                  }
                }}
                placeholder="NAME"
                spellCheck={false}
                aria-label="Name"
                aria-invalid={!!r.k && !ENV_NAME.test(r.k)}
                className={cn(field, "ident aria-invalid:border-danger")}
              />
              <input value={r.v} onChange={(e) => setRow(i, { v: e.target.value })} placeholder="value" spellCheck={false} aria-label={`Value of ${r.k || "the variable"}`} type="password" autoComplete="off" className={cn(field, "ident")} />
              <Button type="button" size="icon-sm" variant="ghost" aria-label={`Remove ${r.k || "the variable"}`} onClick={() => onChange(rows.filter((_, j) => j !== i))} className="mt-1">
                <X />
              </Button>
            </div>
          ))}
        </div>
      )}
      <button type="button" onClick={() => onChange([...rows, { k: "", v: "" }])} className="mt-2 inline-flex items-center gap-1 text-[0.8125rem] text-ink-2 hover:text-ink">
        <Plus className="size-3.5" /> Add a variable <span className="text-ink-4">(or paste a .env file into a name)</span>
      </button>
    </div>
  );
}
