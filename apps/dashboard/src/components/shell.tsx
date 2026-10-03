import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { Menu as MenuIcon, Plus, Search } from "lucide-react";
import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";
import { useEnamels } from "@/lib/enamel";
import { int } from "@/lib/format";
import { memoryModel } from "@/lib/memory";
import { useCurrentProject } from "@/lib/project";
import { useAllStaged } from "@/lib/staged";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";
import { useWaitingWorkflowApprovals } from "@/lib/wf";
import { EnamelSwatch } from "./enamel-swatch";
import { useFavicon } from "./favicon";
import { Logo } from "./logo";
import { boxName, whereItRuns } from "@/lib/box";
import { rememberClick, WhoTrigger } from "./shell-triggers";
import { Toaster } from "./toast";

// Menus, the phone nav sheet, the command palette and the plan tray bring
// Radix and cmdk with them. They load after the first paint (or when first
// needed), so the shell itself stays small.
const LazyWhoMenu = lazy(() => import("./shell-menus").then((m) => ({ default: m.WhoMenu })));
const LazyNavSheet = lazy(() => import("./shell-menus").then((m) => ({ default: m.NavSheet })));
const LazyPalette = lazy(() => import("./palette").then((m) => ({ default: m.CommandPalette })));
const LazyStaged = lazy(() => import("./plan-tray").then((m) => ({ default: m.StagedChanges })));

export const SIDEBAR_W = 232;

/**
 * The frame every page renders in: the sidebar (box nameplate, ⌘K, Box,
 * Ledger, Health, Access, Settings, then Projects), one left edge for every
 * page, the staged-changes bar and the toasts. Below 1024 px the sidebar
 * becomes a sheet behind a top bar.
 */
export function Shell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [navPath, setNavPath] = useState<string | null>(null);
  const navOpen = navPath === path;
  const setNavOpen = (o: boolean) => setNavPath(o ? path : null);
  const [paletteSeen, setPaletteSeen] = useState(false);
  const [navSeen, setNavSeen] = useState(false);
  if (paletteOpen && !paletteSeen) setPaletteSeen(true);
  if (navOpen && !navSeen) setNavSeen(true);
  const staged = Object.keys(useAllStaged()).length > 0;
  useFavicon();

  useEffect(() => {
    const t = setTimeout(() => void import("./palette"), 1500);
    return () => clearTimeout(t);
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="min-h-dvh bg-paper lg:grid lg:grid-cols-[232px_minmax(0,1fr)]">
      <a href="#main" className="sr-only z-50 rounded-md bg-ink px-3 py-2 text-paper focus:not-sr-only focus:fixed focus:top-2 focus:left-2">
        Skip to content
      </a>
      <aside className="hidden border-r border-rule lg:block">
        <div className="sticky top-0 h-dvh">
          <Sidebar onSearch={() => setPaletteOpen(true)} />
        </div>
      </aside>

      {navSeen && (
        <Suspense fallback={null}>
          <LazyNavSheet open={navOpen} onOpenChange={setNavOpen}>
            <Sidebar
              onSearch={() => {
                setNavOpen(false);
                setPaletteOpen(true);
              }}
            />
          </LazyNavSheet>
        </Suspense>
      )}

      <div className="flex min-w-0 flex-col">
        <AttackBanner />
        <MobileBar onMenu={() => setNavOpen(true)} onSearch={() => setPaletteOpen(true)} />
        <main id="main" className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
      {paletteSeen && (
        <Suspense fallback={null}>
          <LazyPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
        </Suspense>
      )}
      {staged && (
        <Suspense fallback={null}>
          <LazyStaged />
        </Suspense>
      )}
      <Toaster />
    </div>
  );
}

function MobileBar({ onMenu, onSearch }: { onMenu: () => void; onSearch: () => void }) {
  const status = useQuery(q.status());
  const name = boxName(status.data);
  return (
    <header className="sticky top-0 z-30 flex h-[52px] items-center gap-2 border-b border-rule bg-paper px-2 sm:px-4 lg:hidden">
      <button onClick={onMenu} className="grid size-10 place-items-center rounded-[8px] text-ink-2 hover:bg-paper-sunk" aria-label="Open navigation">
        <MenuIcon className="size-[18px]" />
      </button>
      <Link to="/" className="flex min-w-0 items-center gap-2 rounded-[6px]" aria-label="Box">
        <Logo className="size-[20px] text-ink" />
        <span className="truncate text-[0.9375rem] font-[550]">{name}</span>
      </Link>
      <button
        onClick={onSearch}
        className="ml-auto grid size-10 place-items-center rounded-[8px] text-ink-2 hover:bg-paper-sunk"
        aria-label="Search and commands"
      >
        <Search className="size-[18px]" />
      </button>
    </header>
  );
}

const healthPaths = ["/status", "/metrics", "/logs", "/errors", "/alerts", "/backups", "/protect"];
const accessPaths = ["/settings/people", "/tokens", "/settings/passkeys"];
const ledgerPaths = ["/ledger", "/changes", "/approvals"];

function Sidebar({ onSearch }: { onSearch: () => void }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const status = useQuery(q.status());
  const pending = useQuery({ ...q.pending, retry: false });
  const onBox = !notOnBox(pending.error);
  const wf = useWaitingWorkflowApprovals(onBox);
  const waiting = (pending.data?.length ?? 0) + wf.length;
  const under = (list: string[]) => list.some((p) => path === p || path.startsWith(`${p}/`));
  const inHealth = under(healthPaths);
  const inAccess = under(accessPaths);
  const inLedger = under(ledgerPaths);

  return (
    <nav className="flex h-full flex-col gap-5 overflow-y-auto pt-[18px] pr-3.5 pb-4 pl-[18px] [&>*]:shrink-0" aria-label="Main">
      <Link to="/" className="flex items-center gap-2.5 rounded-[8px] px-1 py-0.5" aria-label={`${boxName(status.data)}, the Box`}>
        <Logo className="size-[24px] text-ink" />
        <span className="min-w-0">
          <span className="block truncate text-[0.875rem] leading-[1.125rem] font-[550] text-ink">{boxName(status.data)}</span>
          <span className="block truncate text-xs text-ink-3">{whereItRuns(status.data) ?? " "}</span>
        </span>
      </Link>

      <button
        onClick={onSearch}
        className="flex h-8 w-full items-center gap-2 rounded-[7px] border border-rule-2 bg-paper-raised pr-2 pl-2.5 text-[0.8125rem] text-ink-3 shadow-[var(--top-light)] transition-colors hover:text-ink-2"
        aria-label="Search and commands"
      >
        <Search className="size-3.5" />
        Search or run…
        <span className="ml-auto flex gap-0.5">
          <kbd className="kbd">⌘K</kbd>
        </span>
      </button>

      <div className="flex flex-col gap-px">
        <NavItem to="/" exact label="Box" />
        <NavItem
          to="/ledger"
          label="Ledger"
          active={inLedger}
          aside={waiting > 0 ? <span className="font-[550] text-brass-ink">{waiting} waiting</span> : undefined}
        />
        {inLedger && (
          <SubNav>
            <NavItem to="/ledger" sub label="Changes" />
            {onBox && <NavItem to="/approvals" sub label="Approvals" aside={waiting > 0 ? <Count n={waiting} /> : undefined} />}
          </SubNav>
        )}
        <NavItem to="/status" label="Health" active={inHealth} aside={status.data && !status.data.ok ? <Trouble /> : <HealthAside />} />
        {inHealth && (
          <SubNav>
            <NavItem to="/status" sub label="Status" />
            {onBox && (
              <>
                <NavItem to="/metrics" sub label="Metrics" />
                <NavItem to="/logs" sub label="Logs" />
                <NavItem to="/errors" sub label="Errors" aside={<OpenIssues />} />
                <NavItem to="/alerts" sub label="Alerts" aside={<Firing />} />
                <NavItem to="/backups" sub label="Backups" />
                <NavItem to="/protect" sub label="Protection" aside={<AttackBadge />} />
              </>
            )}
          </SubNav>
        )}
        <NavItem to="/settings/people" label="Access" active={inAccess} />
        {inAccess && (
          <SubNav>
            <NavItem to="/settings/people" sub label="People" />
            <NavItem to="/tokens" sub label="Agents and tokens" />
            {onBox && <NavItem to="/settings/passkeys" sub label="Passkeys" />}
          </SubNav>
        )}
        <NavItem to="/settings" exact label="Settings" />
      </div>

      <Projects />

      <div className="mt-auto flex flex-col gap-3 pt-2">
        <Suspense fallback={<WhoTrigger onClick={rememberClick("who")} />}>
          <LazyWhoMenu />
        </Suspense>
        <ThemeSwitch />
      </div>
    </nav>
  );
}

function Projects() {
  const projects = useQuery(q.projects);
  const res = useQuery(q.resources);
  // A project is "where you are" only on its own pages; a Health or Ledger page filtered to it keeps one active rail.
  const inProject = useRouterState({ select: (s) => s.location.pathname.startsWith("/projects/") });
  const picked = useCurrentProject();
  const current = inProject ? picked : undefined;
  const staged = useAllStaged();
  const names = (projects.data ?? []).map((p) => p.name);
  const enamels = useEnamels(names);
  const mem = res.data && res.data.memory.totalBytes > 0 ? memoryModel(res.data) : undefined;
  return (
    <div className="flex flex-col gap-px">
      <p className="label px-2.5 pb-1.5">Projects</p>
      {names.map((p) => (
        <div key={p}>
          <NavItem
            to="/projects/$project"
            params={{ project: p }}
            label={p}
            active={current === p}
            lead={<EnamelSwatch enamel={enamels[p]} />}
            aside={
              staged[p] ? (
                <span className="font-[550] text-brass-ink">{staged[p].length} staged</span>
              ) : mem?.projects[p] !== undefined ? (
                // Only apps have memory of their own; a project without running apps shows nothing here.
                <span className="tnum">{int(mem.projects[p])}&#8239;MB</span>
              ) : undefined
            }
          />
          {current === p && <ProjectNav project={p} />}
        </div>
      ))}
      <Link
        to="/new"
        className="flex h-[30px] items-center gap-2 rounded-[7px] px-2.5 text-[0.84375rem] text-ink-3 transition-colors hover:bg-paper-sunk hover:text-ink"
      >
        <Plus className="size-3.5" />
        New project
      </Link>
    </div>
  );
}

/** A project's pages, only for the parts it actually has. */
function ProjectNav({ project }: { project: string }) {
  const p = useQuery(q.project(project));
  const res = p.data?.resources ?? [];
  const has = (a: string) => res.some((r) => r.address === a);
  const apps = res.some((r) => r.address.startsWith("app/"));
  return (
    <SubNav>
      <NavItem to="/projects/$project" params={{ project }} exact sub label="Overview" aside={<FailingResources project={project} />} />
      {apps && <NavItem to="/projects/$project/apps" params={{ project }} sub label="Apps" />}
      {(has("service/postgres") || has("service/valkey")) && (
        <NavItem to={has("service/postgres") ? "/projects/$project/data" : "/projects/$project/data/kv"} params={{ project }} sub label="Data" />
      )}
      {has("service/storage") && <NavItem to="/projects/$project/storage" params={{ project }} sub label="Storage" />}
      {has("service/email") && <NavItem to="/projects/$project/email" params={{ project }} sub label="Email" />}
      {(apps || res.some((r) => r.address.startsWith("cron/"))) && <NavItem to="/projects/$project/queues" params={{ project }} sub label="Queues" />}
      {has("service/auth") && <NavItem to="/projects/$project/users" params={{ project }} sub label="Users" />}
      {has("service/analytics") && <NavItem to="/projects/$project/analytics" params={{ project }} sub label="Analytics" />}
      <NavItem to="/projects/$project/secrets" params={{ project }} sub label="Secrets" />
    </SubNav>
  );
}

function SubNav({ children }: { children: ReactNode }) {
  return <div className="relative mt-px mb-1 flex flex-col gap-px before:absolute before:top-0 before:bottom-0 before:left-[12px] before:w-px before:bg-rule-2">{children}</div>;
}

function NavItem({
  to,
  params,
  exact,
  label,
  aside,
  lead,
  sub,
  active,
}: {
  to: string;
  params?: Record<string, string>;
  exact?: boolean;
  label: string;
  aside?: ReactNode;
  lead?: ReactNode;
  sub?: boolean;
  /** Force the active look (a section whose pages live under several paths). */
  active?: boolean;
}) {
  return (
    <Link
      to={to as "/"}
      params={params as never}
      activeOptions={{ exact: !!exact, includeSearch: false }}
      data-force={active ? "" : undefined}
      className={cn(
        "group relative flex items-center gap-2 rounded-[7px] text-ink-2 transition-colors duration-[var(--dur-state)] hover:bg-paper-sunk hover:text-ink",
        sub ? "h-7 pr-2 pl-[26px] text-[0.8125rem]" : "h-[30px] px-2.5 text-[0.875rem]",
        "data-[status=active]:text-ink data-[force]:text-ink",
        !sub && "data-[status=active]:font-[550] data-[force]:font-[550]",
        sub && "data-[status=active]:bg-paper-sunk",
      )}
    >
      {!sub && (
        <span
          aria-hidden
          className="absolute top-[7px] bottom-[7px] -left-[18px] w-[2px] rounded-r-[2px] bg-brass opacity-0 group-data-[force]:opacity-100 group-data-[status=active]:opacity-100"
        />
      )}
      {lead}
      <span className="truncate">{label}</span>
      {aside && <span className="ml-auto shrink-0 text-xs font-[400] text-ink-3">{aside}</span>}
    </Link>
  );
}

function Count({ n }: { n: number }) {
  return <span className="font-[550] text-brass-ink tnum">{n}</span>;
}

function HealthAside() {
  const backups = useQuery({ ...mq.backups, retry: false, refetchInterval: false });
  return backups.data && !backups.data.lastOkAt ? <span>no backup yet</span> : null;
}

function Trouble() {
  return (
    <span className="flex items-center gap-1.5 font-[550] text-danger">
      <span className="size-1.5 rounded-full bg-danger" />
      needs a look
    </span>
  );
}

function FailingResources({ project }: { project: string }) {
  const p = useQuery(q.project(project));
  const n = Object.values(p.data?.status ?? {}).filter((s) => s.state === "failed").length;
  return n > 0 ? (
    <span className="flex items-center gap-1.5 font-[550] text-danger" aria-label={`${n} failing`}>
      <span className="size-1.5 rounded-full bg-danger" />
      {n}
    </span>
  ) : null;
}

function AttackBadge() {
  const on = useQuery(mq.protect).data?.underAttack.on;
  return on ? <span className="font-[550] text-danger">under attack</span> : null;
}

/** The alarm state: while under-attack mode is on, every page says so. */
function AttackBanner() {
  const a = useQuery(mq.protect).data?.underAttack;
  if (!a?.on) return null;
  return (
    <div role="status" className="relative z-20 border-b border-danger bg-danger-wash">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 text-sm sm:px-6 lg:px-12">
        <span className="size-2 rounded-full bg-danger" />
        <span className="font-[550] text-ink">Under-attack mode is on.</span>
        <span className="text-ink-2">Visitors solve a challenge first and limits are tight. Ends by itself in {a.minutesLeft} min.</span>
        <Link to="/protect" className="ml-auto font-[550] text-danger underline underline-offset-4">
          Protection
        </Link>
      </div>
    </div>
  );
}

function OpenIssues() {
  const { data } = useQuery(mq.issues(undefined, "unresolved"));
  const n = data?.length ?? 0;
  return n > 0 ? <span className="tnum">{n}</span> : null;
}

function Firing() {
  const { data } = useQuery(mq.alerts);
  const n = data?.firing?.length ?? 0;
  return n > 0 ? <span className="font-[550] text-danger">{n} firing</span> : null;
}

function ThemeSwitch() {
  const { pref } = useTheme();
  const opts: Array<{ v: ThemePref; label: string }> = [
    { v: "system", label: "Auto" },
    { v: "light", label: "Light" },
    { v: "dark", label: "Dark" },
  ];
  return (
    <div role="radiogroup" aria-label="Theme" className="ml-1 flex w-max gap-0.5 rounded-[7px] border border-rule p-0.5">
      {opts.map((o) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={pref === o.v}
          onClick={() => setTheme(o.v)}
          className={cn(
            "rounded-[5px] px-2 py-[3px] text-[0.71875rem] text-ink-3 transition-colors hover:text-ink",
            pref === o.v && "bg-paper-sunk text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
