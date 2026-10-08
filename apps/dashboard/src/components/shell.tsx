import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { Activity as ActivityIcon, ArchiveRestore, ArrowLeft, ChevronsUpDown, Gauge, HeartPulse, KeyRound, LayoutGrid, Menu as MenuIcon, Search, Settings as SettingsIcon } from "lucide-react";
import { lazy, Suspense, useEffect, useState, type ComponentProps, type ReactNode } from "react";
import { notOnBox } from "@/api/client";
import { mq } from "@/api/modules";
import { q } from "@/api/queries";
import { boxName } from "@/lib/box";
import { cn } from "@/lib/cn";
import { setNavigator, useConfirmRequest } from "@/lib/staged";
import { rememberProject } from "@/lib/recent";
import { PART_PAGE, partsOf, standalonePart, type Part, type ProjectPage } from "@/lib/sections";
import { listen, useShortcut } from "@/lib/shortcuts";
import { passkeyWords } from "@/lib/webauthn";
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
const LazyKeysSheet = lazy(() => import("./keys-sheet").then((m) => ({ default: m.KeysSheet })));

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
  const [paletteSearch, setPaletteSearch] = useState("");
  const [keysOpen, setKeysOpen] = useState(false);
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
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteSearch("");
        setPaletteOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    const off = listen();
    return () => {
      window.removeEventListener("keydown", onKey);
      off();
    };
  }, []);
  const project = useProjectInPath();
  useEffect(() => {
    if (project) rememberProject(project);
  }, [project]);
  const openPalette = (search = "") => {
    setPaletteSearch(search);
    setPaletteOpen(true);
  };
  useShortcut("?", "Show keyboard shortcuts", () => setKeysOpen(true), "Anywhere");
  useShortcut("g p", "Switch project", () => setSwitcherOpen(true), "Go to");
  useGoKeys(project, openPalette);

  // One open state, two triggers (the sidebar's and the phone bar's): only the visible one opens.
  const switcher = (where: "side" | "bar") => <Switcher open={switcherOpen && (where === "side") === isDesktop()} onOpenChange={setSwitcherOpen} compact={where === "bar"} />;

  return (
    <div className="min-h-dvh bg-paper lg:grid lg:grid-cols-[232px_minmax(0,1fr)] lg:bg-side">
      <a href="#main" className="sr-only z-50 rounded-md bg-ink px-3 py-2 text-paper focus:not-sr-only focus:fixed focus:top-2 focus:left-2">
        Skip to content
      </a>
      <aside className="hidden lg:block">
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

      {/* The page: a panel inset on the sidebar's ground (a phone gets the plain page). */}
      <div className="flex min-w-0 flex-col lg:my-2 lg:mr-2 lg:min-h-[calc(100dvh-16px)] lg:rounded-[12px] lg:bg-paper lg:shadow-[0_0_0_1px_var(--rule-2),0_1px_3px_oklch(0_0_0/0.05)]">
        <AttackBanner />
        <MobileBar onMenu={() => setNavOpen(true)} onSearch={() => setPaletteOpen(true)} switcher={switcher("bar")} />
        <main id="main" className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
      {paletteSeen && (
        <Suspense fallback={null}>
          <LazyPalette open={paletteOpen} onOpenChange={setPaletteOpen} initialSearch={paletteSearch} onShortcuts={() => setKeysOpen(true)} />
        </Suspense>
      )}
      {keysOpen && (
        <Suspense fallback={null}>
          <LazyKeysSheet open={keysOpen} onOpenChange={setKeysOpen} />
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

/**
 * "g" then a letter: a section of the project in view. Outside a project (or
 * in one without that part) it opens ⌘K on that part, to pick the project.
 */
function useGoKeys(project: string | undefined, openPalette: (search: string) => void) {
  const navigate = useNavigate();
  const state = useQuery({ ...q.project(project ?? ""), enabled: !!project }).data;
  const parts = partsOf(state);
  const go = (to: ProjectPage, fallback: () => void) => () => (project ? void navigate({ to, params: { project } }) : fallback());
  const part = (p: Part, word: string) => () =>
    project && parts.has(p) ? void navigate({ to: PART_PAGE[p].to, params: { project } }) : openPalette(`${word} `);
  useShortcut("g n", "New project", () => void navigate({ to: "/new" }), "Go to");
  useShortcut("g o", "Overview", go("/projects/$project", () => void navigate({ to: "/" })), "Go to");
  useShortcut("g d", "Database", part("postgres", "database"), "Go to");
  useShortcut("g k", "KV", part("valkey", "kv"), "Go to");
  useShortcut("g f", "Files", part("storage", "files"), "Go to");
  useShortcut("g j", "Jobs", part("jobs", "jobs"), "Go to");
  useShortcut("g l", "Logs", go("/projects/$project/logs", () => void navigate({ to: "/logs", search: {} })), "Go to");
  useShortcut("g u", "Observability", go("/projects/$project/observability", () => void navigate({ to: "/usage" })), "Go to");
  useShortcut("g h", "Activity", go("/projects/$project/history", () => void navigate({ to: "/ledger", search: {} })), "Go to");
  useShortcut("g s", "Settings", go("/projects/$project/settings", () => void navigate({ to: "/settings" })), "Go to");
}

const isDesktop = () => typeof window !== "undefined" && window.matchMedia("(min-width: 1024px)").matches;

// ───────────────────────── the switcher ─────────────────────────

// React 19: ref is a plain prop (Radix's asChild trigger passes one).
function SwitcherButton({ project, compact, ref, ...props }: ComponentProps<"button"> & { project?: string; compact?: boolean }) {
  const status = useQuery(q.status());
  return (
    <button
      ref={ref}
      type="button"
      aria-label={project ? `Project ${project}. Switch project` : "Switch project"}
      className={cn(
        "flex min-w-0 items-center gap-2.5 rounded-[8px] text-left transition-colors hover:bg-paper-hover data-[state=open]:bg-paper-select lg:hover:bg-side-hover lg:data-[state=open]:bg-side-select",
        compact ? "h-9 px-2" : "h-11 w-full px-2",
      )}
      {...props}
    >
      {project ? (
        <span className="grid size-6 shrink-0 place-items-center rounded-[6px] bg-paper-sunk">
          <ProjectIcon project={project} size={16} />
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
}

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
      <button onClick={onMenu} className="grid size-10 shrink-0 place-items-center rounded-[8px] text-ink-2 hover:bg-paper-hover" aria-label="Open navigation">
        <MenuIcon className="size-[18px]" />
      </button>
      <div className="min-w-0">{switcher}</div>
      <button
        onClick={onSearch}
        className="ml-auto grid size-10 shrink-0 place-items-center rounded-[8px] text-ink-2 hover:bg-paper-hover"
        aria-label="Search and commands"
      >
        <Search className="size-[18px]" />
      </button>
    </header>
  );
}

// ───────────────────────── sidebar ─────────────────────────

// Settings has its own sidebar (Back, then its pages), so the main one never grows a third level.
const settingsPaths = ["/settings", "/settings/box", "/settings/git", "/settings/dns", "/settings/people", "/settings/sign-ins", "/settings/passkeys", "/protect"];
const healthPaths = ["/status", "/metrics", "/logs", "/errors", "/requests", "/alerts"];
const activityPaths = ["/ledger", "/changes"];

function Sidebar({ onSearch, switcher }: { onSearch: () => void; switcher?: ReactNode }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  const project = useProjectInPath();
  const under = (list: string[]) => list.some((p) => path === p || path.startsWith(`${p}/`));
  const inSettings = !project && settingsPaths.some((p) => path === p || (p !== "/settings" && path.startsWith(`${p}/`)));
  const back = (to: string, label: string) => (
    <Link
      to={to as "/"}
      className="mb-1.5 flex h-7 items-center gap-1.5 rounded-[7px] px-2.5 text-[0.8125rem] text-ink-3 transition-colors hover:bg-paper-hover hover:text-ink lg:hover:bg-side-hover"
    >
      <ArrowLeft className="size-3.5" />
      {label}
    </Link>
  );
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
          {back("/", "All projects")}
          <ProjectNav project={project} path={path} />
        </div>
      ) : inSettings ? (
        <div className="flex flex-col gap-px pl-1">
          {back("/", "Back")}
          <p className="px-2.5 pb-1 text-[0.9375rem] font-[600] text-ink">Settings</p>
          <NavHeading>Your box</NavHeading>
          <NavItem to="/settings" exact label="General" />
          <NavItem to="/settings/box" label="Machine" />
          {onBox && <NavItem to="/settings/git" label="Git" />}
          {onBox && <NavItem to="/settings/dns" label="DNS" />}
          {onBox && <NavItem to="/protect" label="Shield" aside={<AttackBadge />} />}
          <NavItem to="/settings/people" label="People" />
          <NavHeading>You</NavHeading>
          <NavItem to="/settings/sign-ins" label="Sign-ins" />
          <NavItem to="/settings/passkeys" label={passkeyWords().title} />
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
        {!project && !inSettings && (
          <div className="flex flex-col gap-px">
            <NavItem to="/settings" exact label="Settings" lead={<SettingsIcon className={icon} />} />
          </div>
        )}
        <Suspense fallback={<WhoTrigger onClick={rememberClick("who")} />}>
          <LazyWhoMenu />
        </Suspense>
      </div>
    </nav>
  );
}

/**
 * A project's sections, the way a hosting dashboard reads: what's running
 * (Overview, Deployments, Logs), how it's doing (Analytics, Observability),
 * how it's reached and configured (Domains, Environment Variables), then
 * every built-in service, then History and Settings. Every service shows,
 * added or not: one that isn't added yet opens on what it is and Add. A
 * standalone project (just a database, say) shows only its part, so it
 * reads like that part's console.
 */
function ProjectNav({ project, path }: { project: string; path: string }) {
  const p = useQuery(q.project(project));
  const one = standalonePart(p.data);
  const params = { project };
  const base = `/projects/${project}`;
  const at = (s: string) => path === `${base}/${s}` || path.startsWith(`${base}/${s}/`);
  const item = (part: Part, active?: boolean, label = PART_PAGE[part].label) =>
    (!one || one === part) && <NavItem to={PART_PAGE[part].to} params={params} label={label} active={active} />;
  return (
    <>
      {!one && (
        <>
          <NavItem to="/projects/$project" params={params} exact label="Overview" aside={<Failing project={project} />} />
          <NavItem to="/projects/$project/deployments" params={params} label="Deployments" active={at("deployments") || at("apps")} />
          <NavItem to="/projects/$project/logs" params={params} label="Logs" />
          {item("analytics")}
          <NavItem to="/projects/$project/observability" params={params} label="Observability" active={at("observability") || at("usage")} />
          <NavItem to="/projects/$project/domains" params={params} label="Domains" />
          <NavItem to="/projects/$project/env" params={params} label="Environment Variables" active={at("env") || at("secrets")} />
          <NavHeading>Services</NavHeading>
        </>
      )}
      {item("postgres", at("data") && !at("data/kv"))}
      {item("valkey")}
      {item("storage")}
      {item("email")}
      {item("auth", at("users") || at("orgs"))}
      {item("jobs", at("queues") || at("workflows") || at("jobs") || at("schedules"))}
      <div className="my-2 h-px bg-rule" aria-hidden />
      {one && <NavItem to="/projects/$project/observability" params={params} label="Observability" active={at("observability") || at("usage")} />}
      <NavItem to="/projects/$project/history" params={params} label="Activity" />
      <NavItem to="/projects/$project/settings" params={params} label="Settings" active={at("settings")} />
    </>
  );
}

function NavHeading({ children }: { children: ReactNode }) {
  return <p className="mt-4 mb-1 px-2.5 text-[0.6875rem] font-[550] tracking-[0.06em] text-ink-3 uppercase">{children}</p>;
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
        "group relative flex items-center gap-2 rounded-[7px] text-ink-2 transition-colors duration-[var(--dur-state)] hover:bg-paper-hover hover:text-ink lg:hover:bg-side-hover",
        sub ? "h-7 pr-2 pl-[26px] text-[0.8125rem]" : "h-[30px] px-2.5 text-[0.875rem]",
        active === false ? "" : "data-[status=active]:bg-paper-select data-[status=active]:text-ink lg:data-[status=active]:bg-side-select",
        "data-[force]:bg-paper-select data-[force]:text-ink lg:data-[force]:bg-side-select",
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
