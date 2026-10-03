import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useParams, useRouterState, useSearch } from "@tanstack/react-router";
import { Dialog as D } from "radix-ui";
import {
  Activity,
  BarChart3,
  Layers,
  Shield,
  Archive,
  Bell,
  Boxes,
  Bug,
  Database,
  FolderOpen,
  Mail,
  ChevronsUpDown,
  Fingerprint,
  Gauge,
  KeyRound,
  Lock,
  LogOut,
  Logs,
  Menu as MenuIcon,
  Monitor,
  Moon,
  ScrollText,
  Search,
  Stamp,
  Sun,
  Terminal,
  Users,
} from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api, notOnBox } from "@/api/client";
import { roleCopy, useMe } from "@/lib/me";
import { q } from "@/api/queries";
import { mq } from "@/api/modules";
import { cn } from "@/lib/cn";
import { copyText } from "@/lib/clipboard";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";
import { ActorMark } from "./actor";
import { Wordmark } from "./logo";
import { CommandPalette, mcpCommand } from "./palette";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from "./ui/dropdown";
import { useFavicon } from "./favicon";
import { relative } from "@/lib/time";
import { useWaitingWorkflowApprovals } from "@/lib/wf";

export function Shell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const path = useRouterState({ select: (s) => s.location.pathname });
  // The mobile nav is open for the page it was opened on; navigating closes it.
  const [navPath, setNavPath] = useState<string | null>(null);
  const navOpen = navPath === path;
  const setNavOpen = (o: boolean) => setNavPath(o ? path : null);
  useFavicon();

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
    <div className="min-h-dvh bg-paper lg:grid lg:grid-cols-[248px_1fr]">
      <a href="#main" className="sr-only z-50 rounded-md bg-ink px-3 py-2 text-on-ink focus:not-sr-only focus:fixed focus:top-2 focus:left-2">
        Skip to content
      </a>
      <aside className="hidden border-r border-rule bg-paper-sunk lg:block">
        <div className="sticky top-0 h-dvh">
          <Sidebar onSearch={() => setPaletteOpen(true)} />
        </div>
      </aside>

      <D.Root open={navOpen} onOpenChange={setNavOpen}>
        <D.Portal>
          <D.Overlay className="fixed inset-0 z-40 bg-[oklch(0.15_0.01_60/0.45)] data-[state=open]:animate-fade lg:hidden" />
          <D.Content className="fixed inset-y-0 left-0 z-50 w-[min(84vw,300px)] border-r border-rule bg-paper-sunk shadow-pop outline-none data-[state=open]:animate-[rise_300ms_var(--ease-out-soft)] lg:hidden">
            <D.Title className="sr-only">Navigation</D.Title>
            <D.Description className="sr-only">Pages and projects</D.Description>
            <Sidebar
              onSearch={() => {
                setNavOpen(false);
                setPaletteOpen(true);
              }}
            />
          </D.Content>
        </D.Portal>
      </D.Root>

      <div className="flex min-w-0 flex-col">
        <AttackBanner />
        <TopBar onMenu={() => setNavOpen(true)} onSearch={() => setPaletteOpen(true)} />
        <main id="main" className="grain min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  );
}

function TopBar({ onMenu, onSearch }: { onMenu: () => void; onSearch: () => void }) {
  return (
    <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-rule bg-paper/85 px-3 backdrop-blur-md sm:px-6">
      <button onClick={onMenu} className="grid size-9 place-items-center rounded-md text-ink-2 hover:bg-hover lg:hidden" aria-label="Open navigation">
        <MenuIcon className="size-[18px]" />
      </button>
      <Link to="/" className="mr-1 lg:hidden" aria-label="Tiffin home">
        <Wordmark className="[&_span]:text-[1.15rem]" />
      </Link>
      <button
        onClick={onSearch}
        className="group ml-auto flex h-9 items-center gap-2 rounded-lg border border-rule bg-raised/70 px-2.5 text-base text-ink-3 transition-colors hover:border-rule-strong hover:text-ink-2 sm:w-72 lg:ml-0"
        aria-label="Search and commands"
      >
        <Search className="size-4" />
        <span className="hidden sm:inline">Jump to anything…</span>
        <span className="ml-auto hidden items-center gap-0.5 sm:flex">
          <kbd className="kbd">⌘</kbd>
          <kbd className="kbd">K</kbd>
        </span>
      </button>
      <div className="lg:ml-auto">
        <WhoMenu />
      </div>
    </header>
  );
}

function WhoMenu() {
  const { me, name, role, admin } = useMe();
  const navigate = useNavigate();
  const [copied, setCopied] = useState(false);
  if (!me) return <div className="size-9" />;
  const label = name ?? "You";
  return (
    <Menu>
      <MenuTrigger
        aria-label="Account"
        className="flex h-9 items-center gap-2 rounded-lg px-2 text-base text-ink-2 transition-colors hover:bg-hover hover:text-ink data-[state=open]:bg-hover"
      >
        <ActorMark actor={{ kind: role === "owner" ? "owner" : "human", name: label, id: me.tokenId }} />
        <span className="hidden max-w-40 truncate sm:inline">{label}</span>
        <ChevronsUpDown className="hidden size-3.5 text-ink-4 sm:block" />
      </MenuTrigger>
      <MenuContent align="end" className="w-72">
        <div className="px-2 pt-2 pb-2.5">
          <p className="text-base font-medium text-ink">{label}</p>
          <p className="mt-0.5 text-sm text-ink-3">
            {role ? roleCopy[role]?.label : me.kind}
            {me.expiresAt ? ` · session ends ${relative(me.expiresAt)}` : ""}
          </p>
          {role && <p className="mt-1 text-sm text-ink-3">{roleCopy[role]?.blurb}</p>}
        </div>
        <MenuSeparator />
        <MenuItem
          onSelect={async (e) => {
            e.preventDefault();
            if (await copyText(mcpCommand())) {
              setCopied(true);
              setTimeout(() => setCopied(false), 1400);
            }
          }}
        >
          <Terminal />
          {copied ? "Copied" : "Copy MCP setup command"}
        </MenuItem>
        {admin && (
          <MenuItem onSelect={() => navigate({ to: "/tokens", search: { create: true } })}>
            <KeyRound />
            Create a token
          </MenuItem>
        )}
        <MenuItem onSelect={() => navigate({ to: "/settings/passkeys" })}>
          <Fingerprint />
          Your passkeys
        </MenuItem>
        <MenuSeparator />
        <MenuItem
          onSelect={async () => {
            try {
              await api.logout();
            } finally {
              location.assign("/login?reason=signed-out");
            }
          }}
        >
          <LogOut />
          Sign out
        </MenuItem>
      </MenuContent>
    </Menu>
  );
}

/** The project in view: from /projects/$project or the activity filter. */
export function useCurrentProject() {
  const fromPath = useParams({ strict: false, select: (p: { project?: string }) => p.project });
  const fromSearch = useSearch({ strict: false, select: (s: { project?: string }) => s.project });
  return fromPath ?? fromSearch;
}

function Sidebar({ onSearch }: { onSearch: () => void }) {
  const project = useCurrentProject();
  const pending = useQuery(q.pending);
  const onBox = !notOnBox(pending.error);
  const wf = useWaitingWorkflowApprovals(onBox);
  const n = (pending.data?.length ?? 0) + wf.length;
  const { admin, can } = useMe();
  return (
    <nav className="flex h-full flex-col gap-5 overflow-y-auto px-3 pt-4 pb-3" aria-label="Main">
      <div className="flex items-center justify-between px-2">
        <Link to="/" search={{}} className="rounded-md" aria-label="Tiffin, activity">
          <Wordmark />
        </Link>
      </div>
      <ProjectSwitcher />
      <div className="flex flex-col gap-0.5">
        <NavItem to="/" search={project ? { project } : {}} exact icon={<ScrollText />} label="Activity" />
        {onBox && (
          <NavItem
            to="/approvals"
            icon={<Stamp />}
            label="Approvals"
            trailing={
              n > 0 ? (
                <span
                  className="ml-auto grid h-5 min-w-5 place-items-center rounded-full bg-brass px-1.5 font-mono text-[0.6875rem] font-medium text-on-ink tnum"
                  aria-label={`${n} waiting`}
                >
                  {n}
                </span>
              ) : undefined
            }
          />
        )}
        <button
          onClick={onSearch}
          className="flex h-8 items-center gap-2.5 rounded-md px-2 text-base text-ink-3 transition-colors hover:bg-hover hover:text-ink lg:hidden"
        >
          <Search className="size-4" />
          Search
        </button>
      </div>
      {project && <ProjectNav project={project} onBox={onBox} />}
      <NavSection title="Box">
        <NavItem to="/status" icon={<Gauge />} label="Status" status />
        {onBox && (
          <>
            <NavItem to="/metrics" icon={<Activity />} label="Metrics" />
            <NavItem to="/logs" icon={<Logs />} label="Logs" />
            <NavItem to="/errors" icon={<Bug />} label="Errors" trailing={<OpenIssues />} />
            <NavItem to="/alerts" icon={<Bell />} label="Alerts" trailing={<Firing />} />
            <NavItem to="/backups" icon={<Archive />} label="Backups" />
            <NavItem to="/protect" icon={<Shield />} label="Protection" trailing={<AttackBadge />} />
          </>
        )}
      </NavSection>
      <NavSection title="Access">
        <NavItem to="/settings/people" icon={<Users />} label="People" />
        {(admin || can("tokens")) && <NavItem to="/tokens" icon={<KeyRound />} label="Tokens" />}
        {onBox && <NavItem to="/settings/passkeys" icon={<Fingerprint />} label="Passkeys" />}
      </NavSection>
      <div className="mt-auto flex flex-col gap-3 pt-2">
        <BoxCard />
        <ThemeSwitch />
      </div>
    </nav>
  );
}

/** A project's pages, only for the services it actually has. */
function ProjectNav({ project, onBox }: { project: string; onBox: boolean }) {
  const p = useQuery(q.project(project));
  const has = (a: string) => (p.data?.resources ?? []).some((r) => r.address === a);
  return (
    <NavSection title={project} mono>
      <NavItem to="/projects/$project" params={{ project }} exact icon={<Boxes />} label="Overview" />
      {onBox && (has("service/postgres") || has("service/valkey")) && (
        <NavItem
          to={has("service/postgres") ? "/projects/$project/data" : "/projects/$project/data/kv"}
          params={{ project }}
          icon={<Database />}
          label="Data"
        />
      )}
      {onBox && has("service/storage") && <NavItem to="/projects/$project/storage" params={{ project }} icon={<FolderOpen />} label="Storage" />}
      {onBox && has("service/email") && <NavItem to="/projects/$project/email" params={{ project }} icon={<Mail />} label="Email" />}
      {onBox && (p.data?.resources ?? []).some((r) => r.address.startsWith("app/") || r.address.startsWith("cron/")) && (
        <NavItem to="/projects/$project/queues" params={{ project }} icon={<Layers />} label="Queues" />
      )}
      {onBox && has("service/analytics") && <NavItem to="/projects/$project/analytics" params={{ project }} icon={<BarChart3 />} label="Analytics" />}
      {onBox && <NavItem to="/projects/$project/secrets" params={{ project }} icon={<Lock />} label="Secrets" />}
    </NavSection>
  );
}

function AttackBadge() {
  const on = useQuery(mq.protect).data?.underAttack.on;
  return on ? (
    <span className="ml-auto flex items-center gap-1.5 text-xs font-medium text-irr">
      <span className="size-1.5 animate-pulse rounded-full bg-irr" />
      under attack
    </span>
  ) : null;
}

/** The alarm state: while under-attack mode is on, every page says so. */
function AttackBanner() {
  const a = useQuery(mq.protect).data?.underAttack;
  if (!a?.on) return null;
  return (
    <div role="status" className="relative z-20 overflow-hidden border-b border-irr-rule bg-irr-wash">
      <div aria-hidden className="h-1 bg-[repeating-linear-gradient(135deg,var(--irr)_0_10px,transparent_10px_18px)] opacity-80" />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 text-sm sm:px-6">
        <span className="size-2 animate-pulse rounded-full bg-irr" />
        <span className="font-medium text-ink">Under-attack mode is on.</span>
        <span className="text-ink-2">Visitors solve a challenge first and limits are tight. Ends by itself in {a.minutesLeft} min.</span>
        <Link to="/protect" className="ml-auto font-medium text-irr underline underline-offset-4">
          Protection
        </Link>
      </div>
    </div>
  );
}

function OpenIssues() {
  const { data } = useQuery(mq.issues(undefined, "unresolved"));
  const n = data?.length ?? 0;
  return n > 0 ? <span className="ml-auto font-mono text-xs text-ink-3 tnum">{n}</span> : null;
}

function Firing() {
  const { data } = useQuery(mq.alerts);
  const n = data?.firing?.length ?? 0;
  return n > 0 ? (
    <span className="ml-auto flex items-center gap-1.5 text-xs font-medium text-irr">
      <span className="size-1.5 rounded-full bg-irr" />
      {n} firing
    </span>
  ) : null;
}

function NavSection({ title, mono, children }: { title: string; mono?: boolean; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <p
        className={cn(
          "mb-1 px-2 text-2xs font-medium tracking-wider text-ink-4 uppercase",
          mono && "font-mono tracking-normal normal-case text-ink-3",
        )}
      >
        {title}
      </p>
      {children}
    </div>
  );
}

function NavItem({
  to,
  params,
  search,
  exact,
  icon,
  label,
  status,
  trailing,
}: {
  to: string;
  params?: Record<string, string>;
  search?: Record<string, string>;
  exact?: boolean;
  icon: ReactNode;
  label: string;
  status?: boolean;
  trailing?: ReactNode;
}) {
  const { data } = useQuery({ ...q.status(), enabled: !!status });
  const degraded = status && data && !data.ok;
  return (
    <Link
      // Routes are typed elsewhere; the nav is a plain list of them.
      to={to as "/"}
      params={params as never}
      search={(search ?? {}) as never}
      activeOptions={{ exact: !!exact, includeSearch: false }}
      className="group relative flex h-8 items-center gap-2.5 rounded-md px-2 text-base text-ink-2 transition-colors hover:bg-hover hover:text-ink data-[status=active]:bg-hover data-[status=active]:font-medium data-[status=active]:text-ink [&_svg]:size-4 [&_svg]:text-ink-3 data-[status=active]:[&_svg]:text-ink"
    >
      <span
        aria-hidden
        className="absolute top-1.5 bottom-1.5 -left-3 w-[3px] rounded-r-full bg-brass opacity-0 transition-opacity group-data-[status=active]:opacity-100"
      />
      {icon}
      {label}
      {degraded && (
        <span className="ml-auto flex items-center gap-1.5 text-xs font-medium text-irr">
          <span className="size-1.5 rounded-full bg-irr" />
          Degraded
        </span>
      )}
      {trailing}
    </Link>
  );
}

function ProjectSwitcher() {
  const { data: projects } = useQuery(q.projects);
  const project = useCurrentProject();
  const navigate = useNavigate();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const onActivity = path === "/";
  const pick = (v: string) => {
    if (!v) navigate({ to: "/", search: {} });
    else if (onActivity) navigate({ to: "/", search: { project: v } });
    else if (path.endsWith("/secrets")) navigate({ to: "/projects/$project/secrets", params: { project: v } });
    else navigate({ to: "/projects/$project", params: { project: v } });
  };
  return (
    <Menu>
      <MenuTrigger className="flex h-11 items-center gap-2.5 rounded-lg border border-rule bg-raised/60 px-2.5 text-left transition-colors hover:border-rule-strong hover:bg-raised data-[state=open]:border-rule-strong">
        <span className="grid size-6 place-items-center rounded-md bg-paper font-mono text-xs text-ink-2 ring-1 ring-rule">
          {project ? project.slice(0, 1).toUpperCase() : "*"}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block text-2xs font-medium tracking-wider text-ink-3 uppercase">Project</span>
          <span className="block truncate text-base text-ink">{project ?? "All projects"}</span>
        </span>
        <ChevronsUpDown className="size-3.5 text-ink-4" />
      </MenuTrigger>
      <MenuContent align="start" className="w-[var(--radix-dropdown-menu-trigger-width)] min-w-56">
        <MenuLabel>Projects on this box</MenuLabel>
        <MenuRadioGroup value={project ?? ""} onValueChange={pick}>
          <MenuRadioItem value="">All projects</MenuRadioItem>
          {(projects ?? []).map((p) => (
            <MenuRadioItem key={p.name} value={p.name}>
              <span className="flex-1 truncate">{p.name}</span>
              <span className="font-mono text-xs text-ink-4">v{p.version}</span>
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>
        {projects && projects.length === 0 && <p className="px-2 py-1.5 text-sm text-ink-3">No projects yet. `tiffin init` makes one.</p>}
      </MenuContent>
    </Menu>
  );
}

function BoxCard() {
  const { data } = useQuery(q.status());
  if (!data) return null;
  return (
    <Link to="/status" className="group block rounded-lg px-2 py-2 transition-colors hover:bg-hover">
      <div className="flex items-center gap-2 text-sm">
        <span className={cn("size-1.5 rounded-full", data.ok ? "bg-rev" : "bg-irr")} aria-hidden />
        <span className="text-ink-2">{data.ok ? "Box is healthy" : "Box needs a look"}</span>
      </div>
      <p className="mt-0.5 truncate pl-3.5 font-mono text-xs text-ink-4">
        {data.host.hostname} · {data.version}
      </p>
    </Link>
  );
}

function ThemeSwitch() {
  const { pref } = useTheme();
  const opts: Array<{ v: ThemePref; icon: ReactNode; label: string }> = [
    { v: "system", icon: <Monitor />, label: "Match system" },
    { v: "light", icon: <Sun />, label: "Light" },
    { v: "dark", icon: <Moon />, label: "Dark" },
  ];
  return (
    <div role="radiogroup" aria-label="Theme" className="flex rounded-lg border border-rule bg-paper p-0.5">
      {opts.map((o) => (
        <button
          key={o.v}
          role="radio"
          aria-checked={pref === o.v}
          aria-label={o.label}
          title={o.label}
          onClick={() => setTheme(o.v)}
          className={cn(
            "grid h-7 flex-1 place-items-center rounded-md text-ink-3 transition-colors hover:text-ink [&_svg]:size-3.5",
            pref === o.v && "bg-raised text-ink shadow-[0_1px_2px_oklch(0_0_0/0.12)] ring-1 ring-rule",
          )}
        >
          {o.icon}
        </button>
      ))}
    </div>
  );
}
