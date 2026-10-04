import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { Activity as ActivityIcon, ArchiveRestore, ArrowLeft, ChevronsUpDown, Gauge, HeartPulse, KeyRound, LayoutGrid, Menu as MenuIcon, Search, Settings as SettingsIcon } from "lucide-react";
import { forwardRef, lazy, Suspense, useEffect, useState, type ComponentProps, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { boxName } from "@/lib/box";
import { cn } from "@/lib/cn";
import { setNavigator, useConfirmRequest } from "@/lib/staged";
import { useFavicon } from "./favicon";
import { Logo } from "./logo";
import { rememberClick, WhoTrigger } from "./shell-triggers";
import { Toaster } from "./toast";
import { ProjectIcon } from "@/components/project-icon";

// Menus, the switcher, the phone nav sheet, the command palette and the
// confirm dialog bring Radix and cmdk with them. They load after the first
// paint (or when first needed), so the shell itself stays small.
const LazyWhoMenu = lazy(() => import("./shell-menus").then((m) => ({ default: m.WhoMenu })));
const LazyNavSheet = lazy(() => import("./shell-menus").then((m) => ({ default: m.NavSheet })));
const LazySwitcher = lazy(() => import("./shell-switcher").then((m) => ({ default: m.SwitcherPopover })));
const LazyPalette = lazy(() => import("./palette").then((m) => ({ default: m.CommandPalette })));
const LazyConfirm = lazy(() => import("./plan-tray").then((m) => ({ default: m.ChangeConfirm })));

export const SIDEBAR_W = 232;

/** The project in view, from the path only (a Health page filtered to a project is still Settings). */
function useProjectInPath(): string | undefined {
  return useRouterState({ select: (s) => s.location.pathname.match(/^\/projects\/([^/]+)/)?.[1] });
}

/**
 * The frame every page renders in. The sidebar never grows with the number
 * of projects: a switcher at the top (the current project, or All projects),
 * then either Projects and Settings, or, inside a project, only that
 * project's sections under "All projects". Above every page: a banner while
 * under-attack mode is on.
 * Below 1024 px the sidebar becomes a sheet behind a top bar.
 */
export function Shell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [switcherOpen, setSwitcherOpen] = useState(false);
  const path = useRouterState({ select: (s) => s.location.pathname });
  const [navPath, setNavPath] = useState<string | null>(null);
  const navOpen = navPath === path;
  const setNavOpen = (o: boolean) => setNavPath(o ? path : null);
  const [paletteSeen, setPaletteSeen] = useState(false);
  const [navSeen, setNavSeen] = useState(false);
  if (paletteOpen && !paletteSeen) setPaletteSeen(true);
  if (navOpen && !navSeen) setNavSeen(true);
  const confirm = !!useConfirmRequest();
  const navigate = useNavigate();
  useFavicon();

  useEffect(() => setNavigator((to) => void navigate({ to: to as "/" })), [navigate]);
  useEffect(() => {
    const t = setTimeout(() => {
      void import("./palette");
      void import("./shell-switcher");
    }, 1500);
    return () => clearTimeout(t);
  }, []);
  useEffect(() => {
    let g = 0;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((o) => !o);
        return;
      }
      const t = e.target;
      if (e.metaKey || e.ctrlKey || e.altKey || (t instanceof Element && t.closest("input, textarea, select, [contenteditable]"))) return;
      // "g p": go to a project (opens the switcher).
      if (e.key === "g") g = Date.now();
      else if (e.key === "p" && Date.now() - g < 1000) {
        e.preventDefault();
        g = 0;
        setSwitcherOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // One open state, two triggers (the sidebar's and the phone bar's): only the visible one opens.
  const switcher = (where: "side" | "bar") => <Switcher open={switcherOpen && (where === "side") === isDesktop()} onOpenChange={setSwitcherOpen} compact={where === "bar"} />;

  return (
    <div className="min-h-dvh bg-paper lg:grid lg:grid-cols-[232px_minmax(0,1fr)]">
      <a href="#main" className="sr-only z-50 rounded-md bg-ink px-3 py-2 text-paper focus:not-sr-only focus:fixed focus:top-2 focus:left-2">
        Skip to content
      </a>
      <aside className="hidden border-r border-rule lg:block">
        <div className="sticky top-0 h-dvh">
          <Sidebar onSearch={() => setPaletteOpen(true)} switcher={switcher("side")} />
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
        <MobileBar onMenu={() => setNavOpen(true)} onSearch={() => setPaletteOpen(true)} switcher={switcher("bar")} />
        <main id="main" className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
      {paletteSeen && (
        <Suspense fallback={null}>
          <LazyPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
        </Suspense>
      )}
      {confirm && (
        <Suspense fallback={null}>
          <LazyConfirm />
        </Suspense>
      )}
      <Toaster />
    </div>
  );
}

const isDesktop = () => typeof window !== "undefined" && window.matchMedia("(min-width: 1024px)").matches;

// ───────────────────────── the switcher ─────────────────────────

const SwitcherButton = forwardRef<HTMLButtonElement, ComponentProps<"button"> & { project?: string; compact?: boolean }>(function SwitcherButton(
  { project, compact, ...props },
  ref,
) {
  const status = useQuery(q.status());
  return (
    <button
      ref={ref}
      type="button"
      aria-label={project ? `Project ${project}. Switch project` : "Switch project"}
      className={cn(
        "flex min-w-0 items-center gap-2.5 rounded-[8px] text-left transition-colors hover:bg-paper-sunk data-[state=open]:bg-paper-sunk",
        compact ? "h-9 px-2" : "h-11 w-full px-2",
      )}
      {...props}
    >
      {project ? (
        <span className="grid size-6 shrink-0 place-items-center rounded-[6px] bg-paper-sunk">
          <ProjectIcon project={project} size={14} />
        </span>
      ) : (
        <Logo className="size-6 shrink-0 text-ink" />
      )}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[0.875rem] leading-[1.125rem] font-[550] text-ink">{project ?? "All projects"}</span>
        {!compact && <span className="block truncate text-xs text-ink-3">{boxName(status.data)}</span>}
      </span>
      <ChevronsUpDown className="size-3.5 shrink-0 text-ink-3" />
    </button>
  );
});

function Switcher({ open, onOpenChange, compact }: { open: boolean; onOpenChange: (o: boolean) => void; compact?: boolean }) {
  const project = useProjectInPath();
  const button = <SwitcherButton project={project} compact={compact} />;
  return (
    <Suspense fallback={<SwitcherButton project={project} compact={compact} onClick={rememberClick("project")} />}>
      <LazySwitcher open={open} onOpenChange={onOpenChange} trigger={button} current={project} />
    </Suspense>
  );
}

/** In the phone sheet: a switcher with its own open state. */
function OwnSwitcher() {
  const [open, setOpen] = useState(false);
  return <Switcher open={open} onOpenChange={setOpen} />;
}

// ───────────────────────── top bar (phones) ─────────────────────────

function MobileBar({ onMenu, onSearch, switcher }: { onMenu: () => void; onSearch: () => void; switcher: ReactNode }) {
  return (
    <header className="sticky top-0 z-30 flex h-[52px] items-center gap-1 border-b border-rule bg-paper px-2 sm:px-4 lg:hidden">
      <button onClick={onMenu} className="grid size-10 shrink-0 place-items-center rounded-[8px] text-ink-2 hover:bg-paper-sunk" aria-label="Open navigation">
        <MenuIcon className="size-[18px]" />
      </button>
      <div className="min-w-0">{switcher}</div>
      <button
        onClick={onSearch}
        className="ml-auto grid size-10 shrink-0 place-items-center rounded-[8px] text-ink-2 hover:bg-paper-sunk"
        aria-label="Search and commands"
      >
        <Search className="size-[18px]" />
      </button>
    </header>
  );
}

// ───────────────────────── sidebar ─────────────────────────

const settingsPaths = ["/settings", "/settings/box", "/settings/git", "/settings/people", "/settings/passkeys", "/protect"];
const healthPaths = ["/status", "/metrics", "/logs", "/errors", "/alerts"];
const activityPaths = ["/ledger", "/changes"];

function Sidebar({ onSearch, switcher }: { onSearch: () => void; switcher?: ReactNode }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const project = useProjectInPath();
  const under = (list: string[]) => list.some((p) => path === p || path.startsWith(`${p}/`));
  const inSettings = !project && settingsPaths.some((p) => path === p || (p !== "/settings" && path.startsWith(`${p}/`)));
  // A laptop dev server (no --box) has no backups or shield.
  const passkeys = useQuery({ ...q.passkeys, retry: false });
  const onBox = !notOnBox(passkeys.error);
  const icon = "size-4 text-ink-3";

  return (
    <nav className="flex h-full flex-col gap-4 overflow-y-auto pt-3.5 pr-3.5 pb-4 pl-[14px] [&>*]:shrink-0" aria-label="Main">
      {switcher ?? <OwnSwitcher />}

      <button
        onClick={onSearch}
        className="mx-1 flex h-8 items-center gap-2 rounded-[7px] border border-rule-2 bg-paper-raised pr-2 pl-2.5 text-[0.8125rem] text-ink-3 shadow-[var(--top-light)] transition-colors hover:text-ink-2"
        aria-label="Search and commands"
      >
        <Search className="size-3.5" />
        Search…
        <span className="ml-auto flex gap-0.5">
          <kbd className="kbd">⌘K</kbd>
        </span>
      </button>

      {project ? (
        <div className="flex flex-col gap-px pl-1">
          <Link
            to="/"
            className="mb-1.5 flex h-7 items-center gap-1.5 rounded-[7px] px-2.5 text-[0.8125rem] text-ink-3 transition-colors hover:bg-paper-sunk hover:text-ink"
          >
            <ArrowLeft className="size-3.5" />
            All projects
          </Link>
          <ProjectNav project={project} path={path} />
        </div>
      ) : (
        <div className="flex flex-col gap-px pl-1">
          <NavItem to="/" exact label="Projects" lead={<LayoutGrid className={icon} />} />
          <NavItem to="/usage" label="Usage" lead={<Gauge className={icon} />} />
          <NavItem to="/ledger" label="Activity" active={under(activityPaths)} lead={<ActivityIcon className={icon} />} />
          <NavItem to="/status" label="Health" active={under(healthPaths)} lead={<HeartPulse className={icon} />} aside={<Trouble />} />
          {onBox && <NavItem to="/backups" label="Backups" lead={<ArchiveRestore className={icon} />} />}
          <NavItem to="/settings/keys" label="API keys" lead={<KeyRound className={icon} />} />
        </div>
      )}

      <div className="mt-auto flex flex-col gap-3 pt-2 pl-1">
        {project ? (
          <div className="flex flex-col gap-px">
            <p className="label px-2.5 pb-1">Your box</p>
            <NavItem to="/usage" label="Usage" lead={<Gauge className={icon} />} />
            <NavItem to="/ledger" label="Activity" lead={<ActivityIcon className={icon} />} />
            <NavItem to="/status" label="Health" lead={<HeartPulse className={icon} />} aside={<Trouble />} />
          </div>
        ) : (
          <div className="flex flex-col gap-px">
            <NavItem to="/settings" exact label="Settings" active={inSettings} lead={<SettingsIcon className={icon} />} />
            {inSettings && (
              <div className="relative mt-px mb-1 flex flex-col gap-px before:absolute before:top-0 before:bottom-0 before:left-[12px] before:w-px before:bg-rule-2">
                <NavItem to="/settings" exact sub label="Your box" />
                <NavItem to="/settings/box" sub label="Machine" />
                {onBox && <NavItem to="/settings/git" sub label="Git" />}
                <NavItem to="/settings/people" sub label="People" />
                {onBox && <NavItem to="/protect" sub label="Shield" aside={<AttackBadge />} />}
              </div>
            )}
          </div>
        )}
        <Suspense fallback={<WhoTrigger onClick={rememberClick("who")} />}>
          <LazyWhoMenu />
        </Suspense>
      </div>
    </nav>
  );
}

/** A project's sections: Overview, the parts it has, then Usage, History and Settings. */
function ProjectNav({ project, path }: { project: string; path: string }) {
  const p = useQuery(q.project(project));
  const res = p.data?.resources ?? [];
  const has = (a: string) => res.some((r) => r.address === a);
  const apps = res.filter((r) => r.address.startsWith("app/"));
  const jobs = res.some((r) => r.address.startsWith("cron/") || r.address.startsWith("queue/")) || apps.some((a) => (a.spec as { role?: string })?.role === "worker");
  const params = { project };
  const base = `/projects/${project}`;
  const at = (s: string) => path === `${base}/${s}` || path.startsWith(`${base}/${s}/`);
  return (
    <>
      <NavItem to="/projects/$project" params={params} exact label="Overview" aside={<Failing project={project} />} />
      {apps.length > 0 && <NavItem to="/projects/$project/apps" params={params} label={apps.length === 1 ? "App" : "Apps"} />}
      {has("service/postgres") ? (
        <NavItem to="/projects/$project/data" params={params} label="Database" active={at("data") && !at("data/kv")} />
      ) : null}
      {has("service/valkey") && <NavItem to="/projects/$project/data/kv" params={params} label="Cache" />}
      {has("service/storage") && <NavItem to="/projects/$project/storage" params={params} label="Files" />}
      {has("service/email") && <NavItem to="/projects/$project/email" params={params} label="Email" />}
      {has("service/auth") && <NavItem to="/projects/$project/users" params={params} label="Auth" active={at("users") || at("orgs")} />}
      {has("service/analytics") && <NavItem to="/projects/$project/analytics" params={params} label="Analytics" />}
      {jobs && <NavItem to="/projects/$project/queues" params={params} label="Jobs" active={at("queues") || at("workflows")} />}
      <div className="my-2 h-px bg-rule" aria-hidden />
      <NavItem to="/projects/$project/usage" params={params} label="Usage" />
      <NavItem to="/projects/$project/history" params={params} label="History" />
      <NavItem to="/projects/$project/settings" params={params} label="Settings" active={at("settings") || at("secrets")} />
    </>
  );
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
        active === false ? "" : "data-[status=active]:bg-paper-sunk data-[status=active]:text-ink",
        "data-[force]:bg-paper-sunk data-[force]:text-ink",
        !sub && "data-[force]:font-[550]",
        !sub && active !== false && "data-[status=active]:font-[550]",
      )}
    >
      {lead}
      <span className="truncate">{label}</span>
      {aside && <span className="ml-auto shrink-0 text-xs font-[400] text-ink-3">{aside}</span>}
    </Link>
  );
}

function Trouble() {
  const status = useQuery(q.status());
  return status.data && !status.data.ok ? (
    <span className="flex items-center gap-1.5 font-[550] text-danger">
      <span className="size-1.5 rounded-full bg-danger" />
      needs a look
    </span>
  ) : null;
}

function Failing({ project }: { project: string }) {
  const p = useQuery(q.project(project));
  const n = Object.values(p.data?.status ?? {}).filter((s) => s.state === "failed").length;
  return n > 0 ? <span className="size-1.5 rounded-full bg-danger" aria-label={`${n} not working`} /> : null;
}

function AttackBadge() {
  const on = useQuery(mq.protect).data?.underAttack.on;
  return on ? <span className="font-[550] text-danger">on</span> : null;
}

// ───────────────────────── banners ─────────────────────────

/** The alarm state: while under-attack mode is on, every page says so. */
function AttackBanner() {
  const a = useQuery(mq.protect).data?.underAttack;
  if (!a?.on) return null;
  return (
    <div role="status" className="relative z-20 border-b border-danger bg-danger-wash">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 text-sm sm:px-8 lg:px-12">
        <span className="size-2 rounded-full bg-danger" />
        <span className="font-[550] text-ink">Under-attack mode is on.</span>
        <span className="text-ink-2">Visitors solve a challenge first and limits are tight. Ends by itself in {a.minutesLeft} min.</span>
        <Link to="/protect" className="ml-auto font-[550] text-danger underline underline-offset-4">
          Shield
        </Link>
      </div>
    </div>
  );
}
