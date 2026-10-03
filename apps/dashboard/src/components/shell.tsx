import { useQuery } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState, useSearch } from "@tanstack/react-router";
import { Dialog as D } from "radix-ui";
import { ChevronsUpDown, Gauge, KeyRound, LogOut, Menu as MenuIcon, Monitor, Moon, ScrollText, Search, Sun, Terminal } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { cn } from "@/lib/cn";
import { copyText } from "@/lib/clipboard";
import { setTheme, useTheme, type ThemePref } from "@/lib/theme";
import { ActorMark } from "./actor";
import { Wordmark } from "./logo";
import { CommandPalette, mcpCommand } from "./palette";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from "./ui/dropdown";
import { useFavicon } from "./favicon";
import { relative } from "@/lib/time";

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
  const { data: me } = useQuery(q.whoami);
  const navigate = useNavigate();
  const [copied, setCopied] = useState(false);
  if (!me) return <div className="size-9" />;
  const everything = (me.scopes ?? []).includes("*");
  const session = me.name === "dashboard session";
  const label = session ? "You" : me.name;
  return (
    <Menu>
      <MenuTrigger className="flex h-9 items-center gap-2 rounded-lg px-2 text-base text-ink-2 transition-colors hover:bg-hover hover:text-ink data-[state=open]:bg-hover">
        <ActorMark actor={{ kind: everything ? "owner" : me.kind, name: label, id: me.tokenId }} />
        <span className="hidden max-w-40 truncate sm:inline">{label}</span>
        <ChevronsUpDown className="hidden size-3.5 text-ink-4 sm:block" />
      </MenuTrigger>
      <MenuContent align="end" className="w-72">
        <div className="px-2 pt-2 pb-2.5">
          <p className="text-base font-medium text-ink">{session ? "Dashboard session" : me.name}</p>
          <p className="mt-0.5 text-sm text-ink-3">
            {everything ? "Full owner access" : (me.scopes ?? []).join(", ")}
            {me.expiresAt ? ` · ends ${relative(me.expiresAt)}` : ""}
          </p>
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
        <MenuItem onSelect={() => navigate({ to: "/tokens", search: { create: true } })}>
          <KeyRound />
          Create a token
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

function Sidebar({ onSearch }: { onSearch: () => void }) {
  return (
    <nav className="flex h-full flex-col gap-6 px-3 pt-4 pb-3" aria-label="Main">
      <div className="flex items-center justify-between px-2">
        <Link to="/" search={{}} className="rounded-md" aria-label="Tiffin, activity">
          <Wordmark />
        </Link>
      </div>
      <ProjectSwitcher />
      <div className="flex flex-col gap-0.5">
        <NavItem to="/" icon={<ScrollText />} label="Activity" />
        <NavItem to="/status" icon={<Gauge />} label="Status" status />
        <NavItem to="/tokens" icon={<KeyRound />} label="Tokens" />
        <button
          onClick={onSearch}
          className="flex h-8 items-center gap-2.5 rounded-md px-2 text-base text-ink-3 transition-colors hover:bg-hover hover:text-ink lg:hidden"
        >
          <Search className="size-4" />
          Search
        </button>
      </div>
      <div className="mt-auto flex flex-col gap-3">
        <BoxCard />
        <ThemeSwitch />
      </div>
    </nav>
  );
}

function NavItem({ to, icon, label, status }: { to: "/" | "/status" | "/tokens"; icon: ReactNode; label: string; status?: boolean }) {
  const { data } = useQuery({ ...q.status(), enabled: !!status });
  const degraded = status && data && !data.ok;
  const project = useSearch({ strict: false, select: (s: { project?: string }) => s.project });
  return (
    <Link
      to={to}
      search={to === "/" && project ? { project } : {}}
      activeOptions={{ exact: to === "/", includeSearch: false }}
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
    </Link>
  );
}

function ProjectSwitcher() {
  const { data: projects } = useQuery(q.projects);
  const project = useSearch({ strict: false, select: (s: { project?: string }) => s.project });
  const navigate = useNavigate();
  const current = projects?.find((p) => p.name === project);
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
        <MenuLabel>Show activity for</MenuLabel>
        <MenuRadioGroup value={project ?? ""} onValueChange={(v) => navigate({ to: "/", search: v ? { project: v } : {} })}>
          <MenuRadioItem value="">All projects</MenuRadioItem>
          {(projects ?? []).map((p) => (
            <MenuRadioItem key={p.name} value={p.name}>
              <span className="flex-1 truncate">{p.name}</span>
              <span className="font-mono text-xs text-ink-4">v{p.version}</span>
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>
        {projects && projects.length === 0 && <p className="px-2 py-1.5 text-sm text-ink-3">No projects yet.</p>}
        {current && (
          <>
            <MenuSeparator />
            <p className="px-2 py-1.5 text-sm text-ink-3">
              {current.resources} resources · version {current.version}
            </p>
          </>
        )}
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
