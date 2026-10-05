import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, LayoutGrid, List, Plus, Search } from "lucide-react";
import { useMemo, useState } from "react";
import emptyCart from "@/assets/illustrations/empty-projects.webp";
import { q } from "@/api/queries";
import { BoxBar } from "@/components/box-bar";
import { Command } from "@/components/copy";
import { useTitle } from "@/components/favicon";
import { NameAsk } from "@/components/name-ask";
import { Page, Skeleton } from "@/components/page";
import { ProblemNote } from "@/components/problem";
import { EmptyBoxStart } from "@/components/start-empty-box";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { words } from "@/lib/format";
import { mcpCommand } from "@/lib/mcp";
import { toneClass, useProjectPulse } from "@/lib/pulse";
import { fullWords, memWords, useBoxShares, type Shares } from "@/lib/usage";
import { useRecentProjects } from "@/lib/recent";
import { PartGlyphs } from "@/components/part-glyph";
import { ProjectIcon } from "@/components/project-icon";

/**
 * Home: your projects. One line about the box and one bar split by project,
 * then a card per project (icon, name, live address, one status line, its
 * parts, its share of the box) and New project. Sort, a list view and (with
 * many projects) search keep it usable at any size. An empty box gets the
 * first run.
 * Everything about the machine itself lives in Settings.
 */
export function HomePage() {
  useTitle("Projects");
  const projects = useQuery(q.projects);
  const changes = useQuery({ ...q.changes(), staleTime: 30_000 });
  const recent = useRecentProjects();
  const names = useMemo(() => (projects.data ?? []).map((p) => p.name), [projects.data]);
  const { shares, pending } = useBoxShares();
  const [view, setView] = useStoredState<"grid" | "list">("tiffin.home-view", "grid");
  const [sort, setSort] = useStoredState<Sort>("tiffin.home-sort", "active");
  const [query, setQuery] = useState("");

  // Recently active: the newest change in each project, then the ones you opened last.
  const lastChange = useMemo(() => {
    const m = new Map<string, string>();
    for (const c of changes.data ?? []) if (!m.has(c.project)) m.set(c.project, c.at);
    return m;
  }, [changes.data]);
  const list = useMemo(() => {
    const l = names.filter((n) => n.includes(query.trim().toLowerCase()));
    if (sort === "name") return l.sort();
    if (sort === "size") return l.sort((a, b) => (shares?.projects[b] ?? 0) - (shares?.projects[a] ?? 0));
    const rank = (n: string) => lastChange.get(n) ?? "";
    return l.sort((a, b) => rank(b).localeCompare(rank(a)) || recent.indexOf(a) - recent.indexOf(b));
  }, [names, query, sort, shares, lastChange, recent]);

  if (projects.isError) {
    return (
      <Page>
        <ProblemNote error={projects.error} title="Can’t read the box." />
      </Page>
    );
  }
  if (projects.data && names.length === 0) return <FirstRun />;

  return (
    <Page wide>
      <NameAsk />
      <header className="flex items-end justify-between gap-4">
        <h1 className="title text-ink">Projects</h1>
        <Button asChild variant="primary" size="lg">
          <Link to="/new">
            <Plus /> New project
          </Link>
        </Button>
      </header>

      <BoxLine shares={shares} names={names} pending={pending} />

      {names.length > 0 && (
        <div className="mt-9 flex flex-wrap items-center gap-2">
          <p className="mr-2 text-[0.8125rem] text-ink-3">{names.length === 1 ? "One project" : `${words(names.length, true)} projects`}</p>
          {names.length > 6 && (
            <label className="relative min-w-0 flex-1 basis-56">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3" />
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Find a project"
                aria-label="Find a project"
                className="h-8 w-full max-w-[18rem] rounded-[7px] border border-rule-2 bg-paper-raised pr-2 pl-8 text-[0.8125rem] text-ink outline-none placeholder:text-ink-4 focus-visible:border-brass"
              />
            </label>
          )}
          <div className="ml-auto flex items-center gap-2">
            <select
              value={sort}
              onChange={(e) => setSort(e.target.value as Sort)}
              aria-label="Sort projects"
              className="h-8 rounded-[7px] border border-rule-2 bg-paper-raised px-2 text-[0.8125rem] text-ink-2 outline-none focus-visible:border-brass"
            >
              <option value="active">Recently active</option>
              <option value="name">Name</option>
              <option value="size">Most resources</option>
            </select>
            <div role="radiogroup" aria-label="View" className="flex rounded-[7px] border border-rule-2 bg-paper-raised p-0.5">
              {(
                [
                  ["grid", <LayoutGrid key="g" />, "Cards"],
                  ["list", <List key="l" />, "List"],
                ] as const
              ).map(([v, icon, label]) => (
                <button
                  key={v}
                  type="button"
                  role="radio"
                  aria-checked={view === v}
                  aria-label={label}
                  title={label}
                  onClick={() => setView(v)}
                  className={cn("grid size-7 place-items-center rounded-[5px] text-ink-3 [&_svg]:size-3.5", view === v && "bg-paper-sunk text-ink")}
                >
                  {icon}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}

      {view === "list" ? (
        <ul className="mt-3 divide-y divide-rule border-y border-rule" aria-label="Projects">
          {list.map((p) => (
            <ProjectRow key={p} project={p} shares={shares} />
          ))}
        </ul>
      ) : (
        <ul className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-label="Projects">
          {projects.isPending
            ? [0, 1, 2].map((i) => (
                <li key={i}>
                  <Skeleton className="h-[176px] rounded-[12px]" />
                </li>
              ))
            : list.map((p) => <ProjectCard key={p} project={p} shares={shares} />)}
        </ul>
      )}
      {query && list.length === 0 && <p className="mt-4 text-sm text-ink-3">No project called that.</p>}
    </Page>
  );
}

type Sort = "active" | "name" | "size";

/** A small preference kept in this browser (view, sort). */
function useStoredState<T extends string>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      return (localStorage.getItem(key) as T | null) ?? initial;
    } catch {
      return initial;
    }
  });
  return [
    v,
    (next: T) => {
      setV(next);
      try {
        localStorage.setItem(key, next);
      } catch {
        /* private window: for this page only */
      }
    },
  ];
}

/** "Your box is about two-fifths full. Room for about four more apps." and the bar. */
function BoxLine({ shares, names, pending }: { shares?: Shares; names: string[]; pending: boolean }) {
  // Hold the line's space while it loads (no jump); a box that can't measure itself shows nothing.
  if (!shares) return pending ? <div className="mt-6 h-[52px]" aria-hidden /> : null;
  return (
    <section className="mt-5 max-w-[46rem]" aria-label="Your box">
      <p className="text-[0.9375rem] text-ink-2">
        <span className="text-ink">Your box is {fullWords(shares.full)}.</span>{" "}
        {shares.full < 0.85 ? `${memWords(shares.freeMB)} free.` : "It’s getting full: limit a project, or move to a bigger machine."}{" "}
        <Link to="/usage" className="text-ink-3 underline decoration-rule-3 underline-offset-4 hover:text-ink">
          Details
        </Link>
      </p>
      <BoxBar className="mt-3" shares={shares} order={names} legend />
    </section>
  );
}

/** The app's og:image, when the box has one for this project; nothing otherwise. */
function usePreview(project: string, live: boolean) {
  const r = useQuery({
    queryKey: ["project-preview", project],
    queryFn: async () => {
      const url = `/v1/projects/${encodeURIComponent(project)}/preview`;
      const res = await fetch(url, { method: "HEAD", credentials: "same-origin" });
      return res.ok ? url : null;
    },
    enabled: live,
    staleTime: 10 * 60_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
  return r.data ?? undefined;
}

function Status({ project, pulse }: { project: string; pulse: ReturnType<typeof useProjectPulse> }) {
  if (pulse.loading) return <Skeleton className="h-4 w-40" />;
  return (
    <>
      {pulse.tone === "busy" ? <span className="spinner text-brass-ink" aria-hidden /> : <span className={cn("size-2 shrink-0 rounded-full", toneClass[pulse.tone])} aria-hidden />}
      <span className={cn("min-w-0 truncate", pulse.tone === "bad" && "text-danger")}>{pulse.words}</span>
      {pulse.why && (
        <Link
          to="/projects/$project/apps/$app"
          params={{ project, app: pulse.why.app }}
          className="relative z-10 shrink-0 font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink"
        >
          See why
        </Link>
      )}
      {pulse.retry && (
        <button type="button" onClick={pulse.retry} className="relative z-10 shrink-0 font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
          Retry
        </button>
      )}
    </>
  );
}

const host = (url: string) => url.replace(/^https?:\/\//, "").replace(/:\d+$/, "");

function ProjectCard({ project, shares }: { project: string; shares?: Shares }) {
  const pulse = useProjectPulse(project);
  const preview = usePreview(project, pulse.tone === "ok" && !!pulse.url);
  const share = shares ? (shares.projects[project] ?? 0) / shares.totalMB : undefined;
  const cap = shares?.caps[project];
  return (
    <li className="group relative flex min-h-[176px] flex-col overflow-hidden rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)] transition-[border-color,box-shadow] duration-[var(--dur-state)] hover:border-rule-3 hover:shadow-raised">
      {preview && <img src={preview} alt="" className="aspect-[1200/630] w-full border-b border-rule object-cover" loading="lazy" />}
      <div className="flex flex-1 flex-col p-4 pb-3.5">
        <div className="flex items-center gap-2.5">
          <ProjectIcon project={project} size={22} />
          <h2 className="min-w-0 truncate text-[1.0625rem] leading-6 font-[550] tracking-[-0.01em] text-ink">
            <Link to="/projects/$project" params={{ project }} className="outline-none after:absolute after:inset-0 after:rounded-[12px] focus-visible:after:shadow-[0_0_0_2px_var(--brass)]">
              {project}
            </Link>
          </h2>
        </div>
        <div className="mt-1 min-h-5 pl-[32px] text-[0.8125rem]">
          {pulse.url ? (
            <a href={pulse.url} target="_blank" rel="noopener noreferrer" className="ident relative z-10 inline-flex max-w-full items-center gap-1 text-[0.75rem] text-ink-3 hover:text-brass-ink">
              <span className="truncate">{host(pulse.url)}</span>
              <ArrowUpRight className="size-3 shrink-0" />
            </a>
          ) : (
            <span className="text-ink-4">{pulse.loading ? " " : "No address yet"}</span>
          )}
        </div>
        <div className="mt-4 flex min-h-5 items-center gap-2 text-[0.875rem] text-ink-2">
          <Status project={project} pulse={pulse} />
        </div>
        <div className="mt-auto flex items-center justify-between gap-3 pt-5">
          <PartGlyphs apps={pulse.apps.length} services={pulse.services} />
          {share !== undefined && (
            <span className="shrink-0 text-xs text-ink-3" title={cap ? `Limited to ${Math.round(cap * 100)}% of the box` : undefined}>
              {shareOfBox(share)}
            </span>
          )}
        </div>
      </div>
    </li>
  );
}

/** One project as a list row, for boxes with many projects. */
function ProjectRow({ project, shares }: { project: string; shares?: Shares }) {
  const pulse = useProjectPulse(project);
  const share = shares ? (shares.projects[project] ?? 0) / shares.totalMB : undefined;
  return (
    <li className="relative grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 py-3 transition-colors hover:bg-paper-sunk sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_auto_5rem] sm:px-2">
      <span className="flex min-w-0 items-center gap-2.5">
        <ProjectIcon project={project} size={18} />
        <Link to="/projects/$project" params={{ project }} className="truncate text-[0.9375rem] font-[550] text-ink outline-none after:absolute after:inset-0 focus-visible:underline">
          {project}
        </Link>
      </span>
      <span className="col-span-2 row-start-2 flex min-w-0 items-center gap-2 text-[0.8125rem] text-ink-2 sm:col-span-1 sm:row-start-auto">
        <Status project={project} pulse={pulse} />
      </span>
      <PartGlyphs apps={pulse.apps.length} services={pulse.services} className="max-sm:hidden" />
      <span className="text-right text-xs text-ink-3">{share !== undefined ? shareOfBox(share) : ""}</span>
    </li>
  );
}

/** 0.12 → "12% of your box"; under one percent says so. */
function shareOfBox(f: number) {
  if (f <= 0) return "nothing running";
  if (f < 0.01) return "under 1% of your box";
  return `${Math.round(f * 100)}% of your box`;
}

/** The first visit: the empty cart (the mascot riding alone), one sentence, the starters and the agent line. */
function FirstRun() {
  return (
    <Page wide>
      <NameAsk />
      <header className="grid items-center gap-x-10 gap-y-2 sm:grid-cols-[minmax(0,1fr)_260px] lg:grid-cols-[minmax(0,1fr)_300px]">
        <div className="min-w-0">
          <h1 className="sentence text-ink">Your tiffin is packed. Nothing in it yet.</h1>
          <p className="mt-2 max-w-[36rem] text-[0.9375rem] leading-[1.375rem] text-ink-2">
            Start a project and it gets its own address, and a database and sign-in if it wants them. It’s live in under a minute.
          </p>
        </div>
        <span className="art-plate mx-auto block w-[260px] max-sm:hidden lg:w-[300px]" data-plate="tile">
          <img src={emptyCart} alt="" width={300} height={150} className="block w-full select-none" draggable={false} />
        </span>
      </header>
      <div className="mt-6 rounded-[12px] border border-rule-2 bg-paper-raised">
        <EmptyBoxStart headline={false} />
      </div>
      <section aria-label="Hand it to your agent" className="mt-10 max-w-[40rem]">
        <h2 className="text-[0.9375rem] font-[550] text-ink">Or hand it to your agent</h2>
        <p className="mt-1 mb-3 text-sm text-ink-2">Claude Code can set up projects for you. It asks you before anything destructive, and every change can be undone in History.</p>
        <Command cmd={mcpCommand()} />
      </section>
    </Page>
  );
}
