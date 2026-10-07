import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { Dialog as D } from "radix-ui";
import {
  Cable,
  Clock,
  CornerDownLeft,
  Fingerprint,
  FolderClosed,
  Gauge,
  Keyboard,
  KeyRound,
  LogOut,
  Monitor,
  MonitorSmartphone,
  Moon,
  Play,
  Plus,
  ScrollText,
  Search,
  Square,
  Sun,
  Terminal,
  Upload,
  UserPlus,
  Users,
} from "lucide-react";
import { Fragment, useState, type ReactNode } from "react";
import { api, type ProjectState } from "@/api/client";
import { q } from "@/api/queries";
import { asTier } from "@/lib/changes";
import { copyText } from "@/lib/clipboard";
import { setTheme } from "@/lib/theme";
import { passkeyWords } from "@/lib/webauthn";
import { RiskMark } from "./risk";
import { useCurrentProject } from "@/lib/project";
import { mcpCommand } from "@/lib/mcp";
import { change, undoChange } from "@/lib/staged";
import { INSTANCE_STOPS } from "./throttle";
import { ProjectIcon } from "@/components/project-icon";
import { useRecentProjects } from "@/lib/recent";
import { partsOf, PART_PAGE, projectHome, type Part } from "@/lib/sections";
import { keyCaps, requestCommand, useCommands } from "@/lib/shortcuts";
import { useSwitchToProject } from "@/lib/switch";
import { useMe } from "@/lib/me";
import { soloParts } from "@/lib/starters";
import { signOut } from "@/lib/command-history";
import { toast } from "./toast";

// Box-wide pages, so every area is a keystroke away.
const box: Array<[string, string, string[]]> = [
  ["Usage", "/usage", ["box", "memory", "cpu", "disk", "room", "limits", "share", "sizes"]],
  ["Machine", "/settings/box", ["box", "platform", "services", "carrier"]],
  ["Metrics", "/metrics", ["cpu", "memory", "disk", "charts"]],
  ["Logs", "/logs", ["logsql", "search", "tail"]],
  ["Errors", "/errors", ["issues", "exceptions", "sentry"]],
  ["Requests", "/requests", ["traces", "tracing", "slow", "spans", "opentelemetry", "waterfall"]],
  ["Alerts", "/alerts", ["rules", "notify"]],
  ["Backups", "/backups", ["restore", "snapshot"]],
  ["Connect DNS", "/settings/dns", ["cloudflare", "dns", "records", "token", "wildcard", "certificate"]],
  ["Your box’s domain", "/settings", ["domain", "address", "sslip", "dashboard address", "own domain"]],
  ["Shield", "/protect", ["protection", "under attack", "ban", "crowdsec", "firewall", "waf", "rate limit", "bots"]],
];

/** A project's pages: label, path, the part it needs (none: every project has it), words it answers to, its shortcut. */
const pages: Array<{ label: string; to: string; search?: Record<string, string>; part?: Part; kw: string[]; keys?: string }> = [
  { label: "Overview", to: "/projects/$project", kw: ["resources", "home"], keys: "g o" },
  { label: "Deployments", to: "/projects/$project/deployments", part: "apps", kw: ["app", "deploy", "build", "build log", "rollback", "preview", "restart"] },
  { label: "Logs", to: "/projects/$project/logs", kw: ["logs", "tail", "errors", "output", "stdout"], keys: "g l" },
  { label: "Environment Variables", to: "/projects/$project/env", kw: ["env", "secrets", "api key", ".env", "config"] },
  { label: "Database", to: "/projects/$project/data", part: "postgres", kw: ["tables", "postgres", "db", "rows"], keys: "g d" },
  { label: "Database: SQL", to: "/projects/$project/data/sql", part: "postgres", kw: ["query", "postgres", "db"] },
  { label: "Database: copies", to: "/projects/$project/data/branches", part: "postgres", kw: ["branches", "clone", "preview", "db"] },
  { label: "Database: schema", to: "/projects/$project/data/schema", part: "postgres", kw: ["diagram", "foreign keys", "relations", "erd", "db"] },
  { label: "Database: restore points", to: "/projects/$project/data/restore", part: "postgres", kw: ["snapshots", "undo", "restore", "db"] },
  { label: "KV", to: PART_PAGE.valkey.to, part: "valkey", kw: ["valkey", "redis", "key-value", "cache", "keys"], keys: "g k" },
  { label: "KV: console", to: "/projects/$project/data/kv/console", part: "valkey", kw: ["valkey", "redis", "commands", "cli", "cache"] },
  { label: "Files", to: "/projects/$project/storage", part: "storage", kw: ["buckets", "storage", "s3", "upload"], keys: "g f" },
  { label: "Email", to: "/projects/$project/email", part: "email", kw: ["mail", "inbox", "relay"] },
  { label: "Auth: users", to: "/projects/$project/users", part: "auth", kw: ["users", "sign in", "passkeys", "ban"] },
  { label: "Auth: organizations", to: "/projects/$project/orgs", part: "auth", kw: ["teams", "members", "orgs"] },
  { label: "Analytics", to: "/projects/$project/analytics", part: "analytics", kw: ["visitors", "pageviews", "traffic"] },
  { label: "Jobs", to: "/projects/$project/jobs", part: "jobs", kw: ["runs", "workflows", "progress", "live"], keys: "g j" },
  { label: "Jobs: schedules", to: "/projects/$project/jobs/schedules", part: "jobs", kw: ["cron", "schedule", "timer", "pause"] },
  { label: "Jobs: queues", to: "/projects/$project/jobs/queues", part: "jobs", kw: ["queues", "topics", "concurrency", "rate limit"] },
  { label: "Jobs: failed", to: "/projects/$project/jobs/failed", part: "jobs", kw: ["dead letter", "dlq", "retry", "failed"] },
  { label: "Jobs: workers", to: "/projects/$project/jobs/workers", part: "jobs", kw: ["workers", "apps", "alive", "instances"] },
  { label: "Observability", to: "/projects/$project/observability", kw: ["metrics", "charts", "requests", "latency", "errors", "traces", "usage"], keys: "g u" },
  { label: "Observability: resources", to: "/projects/$project/observability", search: { tab: "resources" }, kw: ["memory", "cpu", "limit", "resources", "usage", "disk"] },
  { label: "History", to: "/projects/$project/history", kw: ["changes", "undo", "ledger"], keys: "g h" },
  { label: "Settings", to: "/projects/$project/settings", kw: ["colour", "addresses", "delete", "rename", "copy", "move"], keys: "g s" },
  { label: "Domains", to: "/projects/$project/domains", kw: ["domain", "dns", "https", "certificate", "www", "custom domain"] },
];

/**
 * A part page's main actions. Each opens the part's page and runs the
 * page's command of the same id (lib/shortcuts.ts), so the page decides
 * what "New table" looks like.
 */
const actions: Array<{ id: string; label: string; part: Part; kw: string[]; icon: ReactNode }> = [
  { id: "new-table", label: "New table", part: "postgres", kw: ["create", "database", "add"], icon: <Plus /> },
  { id: "new-key", label: "New key", part: "valkey", kw: ["create", "kv", "set", "add"], icon: <Plus /> },
  { id: "upload-file", label: "Upload a file", part: "storage", kw: ["files", "put", "add"], icon: <Upload /> },
  { id: "new-bucket", label: "New bucket", part: "storage", kw: ["create", "files", "s3", "add"], icon: <Plus /> },
  { id: "new-schedule", label: "New schedule", part: "jobs", kw: ["cron", "every", "create", "jobs"], icon: <Clock /> },
  { id: "new-queue", label: "New queue", part: "jobs", kw: ["create", "jobs", "queue"], icon: <Plus /> },
  { id: "send-test-job", label: "Send a test job", part: "jobs", kw: ["queue", "job", "try", "payload"], icon: <Plus /> },
  { id: "start-run", label: "Start a workflow run", part: "jobs", kw: ["workflow", "run", "start"], icon: <Plus /> },
  { id: "connect-postgres", label: "Connect to the database", part: "postgres", kw: ["connection", "url", "psql", "tunnel", "drizzle", "prisma"], icon: <Cable /> },
  { id: "connect-valkey", label: "Connect to KV", part: "valkey", kw: ["connection", "redis", "url", "tunnel"], icon: <Cable /> },
  { id: "connect-storage", label: "Connect to files", part: "storage", kw: ["connection", "s3", "keys", "bun.s3"], icon: <Cable /> },
  { id: "connect-jobs", label: "Connect to jobs", part: "jobs", kw: ["connection", "queue", "send", "signing"], icon: <Cable /> },
];

const RECENT_KEY = "tiffin.recent-commands";
const readRecent = (): string[] => {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
};

type Entry = { id: string; label: ReactNode; text: string; icon: ReactNode; kw?: string[]; keys?: string; aside?: ReactNode; run: () => void; stay?: boolean };

export function CommandPalette({ open, onOpenChange, initialSearch = "", onShortcuts }: { open: boolean; onOpenChange: (o: boolean) => void; initialSearch?: string; onShortcuts?: () => void }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const current = useCurrentProject();
  const { can } = useMe();
  const switchTo = useSwitchToProject();
  const recentProjects = useRecentProjects();
  const pageCommands = useCommands();
  const { data: projects } = useQuery({ ...q.projects, enabled: open });
  const { data: changes } = useQuery({ ...q.changes(), enabled: open });
  const [copied, setCopied] = useState<string | null>(null);
  const [search, setSearch] = useState(initialSearch);
  const [recent, setRecent] = useState<string[]>([]);
  // Each opening starts from what opened it ("g d" outside a project opens it on "database ").
  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setSearch(initialSearch);
      setRecent(readRecent());
    }
  }

  const names = [...recentProjects.filter((p) => projects?.some((x) => x.name === p)), ...(projects ?? []).map((p) => p.name).filter((p) => !recentProjects.includes(p))];
  const states = useQueries({ queries: names.map((n) => ({ ...q.project(n), enabled: open, refetchInterval: false as const })) });
  const stateOf = (n: string): ProjectState | undefined => states[names.indexOf(n)]?.data;

  // "scale web to 4" (or "scale web in shop to 4") makes the same change the copies stepper would.
  const scale = search.trim().toLowerCase().match(/^scale\s+([a-z0-9-]+)(?:\s+in\s+([a-z0-9-]+))?\s+to\s+(\d+)$/);
  const scaleHits = scale
    ? states.flatMap((st) => {
        const proj = st.data;
        if (!proj || (scale[2] && proj.name !== scale[2])) return [];
        const r = (proj.resources ?? []).find((x) => x.address === `app/${scale[1]}`);
        if (!r) return [];
        const from = Number((r.spec as { instances?: number })?.instances ?? 1);
        const to = Number(scale[3]);
        if (!INSTANCE_STOPS.includes(to) || to === from) return [];
        return [{ project: proj.name, app: scale[1], from, to }];
      })
    : [];

  const close = () => {
    onOpenChange(false);
    setSearch("");
  };
  const remember = (id: string) => {
    const next = [id, ...readRecent().filter((x) => x !== id)].slice(0, 8);
    try {
      localStorage.setItem(RECENT_KEY, JSON.stringify(next));
    } catch {
      /* storage blocked: no recent list */
    }
  };
  const run = (e: Entry) => () => {
    remember(e.id);
    if (!e.stay) close();
    e.run();
  };
  const go = (to: string, params?: Record<string, string>, search?: Record<string, string>) => () => void navigate({ to: to as "/", params: params as never, search: search as never });

  /** Everything one project offers: its pages and its parts' actions. */
  const projectEntries = (p: string, here: boolean): Entry[] => {
    const st = stateOf(p);
    const parts = partsOf(st);
    const out: Entry[] = pages
      .filter((x) => !x.part || parts.has(x.part))
      .map((x) => ({
        id: `page:${p}:${x.to}${x.search ? `?${new URLSearchParams(x.search)}` : ""}`,
        label: here ? x.label : <>{x.label}<span className="ml-2 text-xs text-ink-3">{p}</span></>,
        text: `${p} ${x.label}`,
        icon: here ? <FolderClosed /> : <ProjectGlyph project={p} />,
        kw: x.kw,
        keys: here ? x.keys : undefined,
        run: go(x.to, { project: p }, x.search),
      }));
    // Each bucket is a page of its own.
    for (const r of st?.resources ?? []) {
      if (!r.address.startsWith("bucket/")) continue;
      const b = r.address.slice("bucket/".length);
      out.push({
        id: `page:${p}:bucket:${b}`,
        label: here ? <>Files: {b}</> : <>Files: {b}<span className="ml-2 text-xs text-ink-3">{p}</span></>,
        text: `${p} Files ${b} bucket`,
        icon: here ? <FolderClosed /> : <ProjectGlyph project={p} />,
        kw: ["bucket", "files", "s3", "upload"],
        run: go("/projects/$project/storage/$bucket", { project: p, bucket: b }),
      });
    }
    for (const a of actions.filter((x) => parts.has(x.part))) {
      out.push({
        id: `do:${p}:${a.id}`,
        label: here ? a.label : <>{a.label}<span className="ml-2 text-xs text-ink-3">in {p}</span></>,
        text: `${a.label} ${p}`,
        icon: a.icon,
        kw: a.kw,
        run: () => {
          requestCommand(a.id);
          void navigate({ to: PART_PAGE[a.part].to, params: { project: p } });
        },
      });
    }
    if (st && parts.has("apps") && can("apply:reversible")) {
      const stopped = st.resources?.some((r) => r.address === "stopped");
      out.push({
        id: `do:${p}:${stopped ? "start" : "stop"}`,
        label: <>{stopped ? "Start" : "Stop"} {p}<span className="ml-2 text-xs text-ink-3">{stopped ? "wake its apps" : "put its apps to sleep; data stays"}</span></>,
        text: `${stopped ? "Start" : "Stop"} ${p}`,
        icon: stopped ? <Play /> : <Square />,
        kw: stopped ? ["wake", "resume", "start"] : ["sleep", "pause", "stop", "down"],
        run: () => void startStop(p, !stopped),
      });
    }
    return out;
  };

  const startStop = async (p: string, stop: boolean) => {
    try {
      const r = await (stop ? api.stopProject(p) : api.startProject(p));
      void qc.invalidateQueries({ queryKey: ["project", p] });
      void qc.invalidateQueries({ queryKey: ["changes"] });
      const id = r.change?.id;
      toast({
        title: stop ? `Stopped ${p}.` : `Started ${p}.`,
        detail: stop ? "Its apps are down and its data is kept." : "Its apps come back from their live deploys.",
        action: id ? { label: "Undo", run: () => undoChange(id) } : undefined,
      });
    } catch (e) {
      toast({ title: `Couldn’t ${stop ? "stop" : "start"} ${p}.`, detail: e instanceof Error ? e.message : undefined, tone: "danger" });
    }
  };

  const here = current && names.includes(current) ? projectEntries(current, true) : [];
  const elsewhere = search ? names.filter((p) => p !== current).flatMap((p) => projectEntries(p, false)) : [];
  const onPage: Entry[] = pageCommands.map((c) => ({ id: `cmd:${c.id}`, label: c.label, text: c.label, icon: <CornerDownLeft />, kw: c.keywords, keys: c.keys, run: c.run }));
  const projectRows: Entry[] = names.map((p) => ({
    id: `project:${p}`,
    label: p,
    text: `project ${p}`,
    icon: <ProjectGlyph project={p} />,
    aside: p === current ? "here" : undefined,
    run: () => void (current ? switchTo(p) : navigate(projectHome(p, stateOf(p)))),
  }));
  const general: Entry[] = [
    { id: "go:projects", label: "Projects", text: "Projects", icon: <ScrollText />, kw: ["home", "all projects"], run: go("/") },
    { id: "go:new", label: "New project", text: "New project", icon: <Plus />, kw: ["create", "start", "starter", "database", "kv", "files", "schedule"], keys: "g n", run: go("/new") },
    ...soloParts.map((s): Entry => ({
      id: `go:new:${s.part}`,
      label: <>New project: {s.title.toLowerCase()}</>,
      text: `New project ${s.title}`,
      icon: <Plus />,
      kw: ["create", "standalone", "only", "console"],
      run: () => void navigate({ to: "/new", search: { starter: `part:${s.part}` } }),
    })),
    { id: "go:activity", label: "Activity: every project’s changes", text: "Activity", icon: <ScrollText />, kw: ["activity", "changes", "ledger", "undo"], run: () => void navigate({ to: "/ledger", search: {} }) },
    { id: "go:health", label: "Health", text: "Health", icon: <Gauge />, kw: ["status", "checks"], run: go("/status") },
    { id: "go:settings", label: "Settings", text: "Settings", icon: <Gauge />, kw: ["box", "export", "import", "domain"], run: go("/settings") },
    { id: "go:keys", label: "API keys", text: "API keys", icon: <KeyRound />, kw: ["tokens", "claude", "mcp", "approvals"], run: () => void navigate({ to: "/settings/keys", search: {} }) },
    { id: "go:people", label: "People", text: "People", icon: <Users />, kw: ["team", "invite", "roles"], run: go("/settings/people") },
    { id: "go:sign-ins", label: "Sign-ins", text: "Sign-ins", icon: <MonitorSmartphone />, kw: ["sessions", "signed in", "sign out", "sign out everywhere", "devices", "browsers", "security"], run: go("/settings/sign-ins") },
    { id: "go:passkeys", label: `Sign in with ${passkeyWords().name}`, text: "Passkeys", icon: <Fingerprint />, kw: ["passkeys", "touch id", "face id", "windows hello", "fingerprint", "webauthn", "sign in"], run: go("/settings/passkeys") },
    ...box.map(([label, to, kw]): Entry => ({ id: `go:${to}`, label, text: label, icon: <Gauge />, kw, run: go(to) })),
  ];
  const doThings: Entry[] = [
    { id: "do:key", label: "Create a key", text: "Create a key", icon: <Plus />, kw: ["new", "agent", "token"], run: () => void navigate({ to: "/settings/keys", search: { create: true } }) },
    { id: "do:invite", label: "Invite someone", text: "Invite someone", icon: <UserPlus />, kw: ["invite", "team", "person"], run: go("/settings/people") },
    {
      id: "do:mcp",
      label: copied ?? "Copy MCP setup command",
      text: "Copy MCP setup command",
      icon: <Terminal />,
      kw: ["claude", "agent", "connect", "mcp"],
      stay: true,
      run: async () => {
        const ok = await copyText(mcpCommand());
        setCopied(ok ? "MCP setup command copied" : "Couldn’t copy");
        setTimeout(() => setCopied(null), 1500);
      },
    },
    { id: "do:shortcuts", label: "Keyboard shortcuts", text: "Keyboard shortcuts", icon: <Keyboard />, kw: ["keys", "help", "?"], keys: "?", run: () => onShortcuts?.() },
    { id: "do:light", label: "Light theme", text: "Light theme", icon: <Sun />, kw: ["theme"], run: () => setTheme("light") },
    { id: "do:dark", label: "Dark theme", text: "Dark theme", icon: <Moon />, kw: ["theme"], run: () => setTheme("dark") },
    { id: "do:system", label: "Match system theme", text: "Match system theme", icon: <Monitor />, kw: ["theme"], run: () => setTheme("system") },
    {
      id: "do:signout",
      label: "Sign out",
      text: "Sign out",
      icon: <LogOut />,
      run: signOut,
    },
  ];
  const all = [...here, ...elsewhere, ...onPage, ...projectRows, ...general, ...doThings];
  const recentEntries = search ? [] : recent.map((id) => all.find((e) => e.id === id)).filter((e): e is Entry => !!e).slice(0, 5);

  const group = (heading: string, list: Entry[], prefix = "") =>
    list.length > 0 && (
      <Command.Group heading={heading}>
        {list.map((e) => (
          <Item key={prefix + e.id} value={prefix + e.id + " " + e.text} keywords={e.kw} icon={e.icon} keys={e.keys} aside={e.aside} onSelect={run(e)}>
            {e.label}
          </Item>
        ))}
      </Command.Group>
    );

  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-[var(--scrim)] data-[state=open]:animate-[fade_80ms_linear_both]" />
        <D.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-2rem)] max-w-[600px] -translate-x-1/2 overflow-hidden rounded-[12px] border border-rule-2 bg-paper-raised shadow-raised outline-hidden data-[state=open]:animate-[fade_80ms_linear_both]"
        >
          <D.Title className="sr-only">Command palette</D.Title>
          <Command label="Command palette" loop className="flex max-h-[min(70vh,520px)] flex-col" filter={score}>
            <div className="flex items-center gap-2.5 border-b border-rule px-4">
              <Search className="size-4 shrink-0 text-ink-3" />
              <Command.Input
                value={search}
                onValueChange={setSearch}
                placeholder={current ? `Jump to a page, project or action… try “${current} database”` : "Jump to a page, project or action…"}
                className="h-13 w-full bg-transparent text-md text-ink outline-hidden placeholder:text-ink-3"
              />
              <kbd className="kbd">esc</kbd>
            </div>
            <Command.List className="min-h-0 flex-1 overflow-y-auto p-2 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:pt-2 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-2xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-ink-3 [&_[cmdk-group-heading]]:uppercase">
              <Command.Empty className="px-3 py-8 text-center text-base text-ink-3">Nothing matches. Try a project’s name and a part, like “shop files”.</Command.Empty>
              {scaleHits.length > 0 && (
                <Command.Group heading="Do it now">
                  {scaleHits.map((h) => (
                    <Item
                      key={h.project + h.app}
                      value={`do:scale ${search}`}
                      icon={<Gauge />}
                      onSelect={() => {
                        close();
                        change(h.project, { kind: "instances", app: h.app, from: h.from, to: h.to }, { immediate: true });
                      }}
                    >
                      Run {h.app} in {h.project} on {h.to} copies (now {h.from})
                    </Item>
                  ))}
                </Command.Group>
              )}
              {group("Recent", recentEntries, "recent:")}
              {!scale && group("On this page", onPage)}
              {!scale && current && group(`In ${current}`, here)}
              {group("Go to", general)}
              {group("Projects", projectRows)}
              {group("Other projects", elsewhere)}
              {group("Do", doThings)}
              {search && changes && changes.length > 0 && (
                <Command.Group heading="Changes">
                  {changes.slice(0, 50).map((c) => (
                    <Item
                      key={c.id}
                      value={`change ${c.id} ${c.intent} ${c.project} ${c.actor.name ?? ""}`}
                      icon={
                        <span className="grid size-4 place-items-center">
                          <RiskMark tier={asTier(c.plan.risk)} />
                        </span>
                      }
                      onSelect={() => {
                        close();
                        void navigate({ to: "/changes/$id", params: { id: c.id } });
                      }}
                    >
                      <span className="truncate">{c.intent || "(no intent)"}</span>
                      <span className="ml-2 shrink-0 text-xs text-ink-3">{c.project}</span>
                    </Item>
                  ))}
                </Command.Group>
              )}
            </Command.List>
            <div className="flex items-center gap-3 border-t border-rule bg-paper-sunk/60 px-4 py-2 text-xs text-ink-3">
              <span className="flex items-center gap-1">
                <kbd className="kbd">↑</kbd>
                <kbd className="kbd">↓</kbd> move
              </span>
              <span className="flex items-center gap-1">
                <kbd className="kbd">
                  <CornerDownLeft className="inline size-2.5" />
                </kbd>{" "}
                open
              </span>
              <button
                type="button"
                onClick={() => {
                  close();
                  onShortcuts?.();
                }}
                className="ml-auto flex items-center gap-1 rounded-[5px] px-1 hover:text-ink"
              >
                <kbd className="kbd">?</kbd> all shortcuts
              </button>
            </div>
          </Command>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

/**
 * Words in any order, each matched as a fuzzy run of letters ("blog db",
 * "kv bookshop", "bkshp files"), so "project part" and "part project" both
 * find a page. Whole-word hits rank above scattered letters.
 */
function score(value: string, search: string, keywords?: string[]): number {
  const hay = `${value.replace(/^\S+\s/, "")} ${(keywords ?? []).join(" ")}`.toLowerCase();
  const words = search.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return 1;
  let total = 0;
  for (const w of words) {
    const at = hay.indexOf(w);
    if (at >= 0) {
      total += at === 0 || /[\s:·-]/.test(hay[at - 1]) ? 1 : 0.8;
      continue;
    }
    // Letters in order inside one word that starts the same: "kv" in "key-value", "bkshp" in "bookshop".
    const inWord = hay.split(/[\s:·/]+/).some((t) => {
      if (t[0] !== w[0]) return false;
      let i = 0;
      for (const ch of t) if (ch === w[i]) i++;
      return i === w.length;
    });
    if (!inWord) return 0;
    total += 0.3;
  }
  return total / words.length;
}

function ProjectGlyph({ project }: { project: string }) {
  return (
    <span className="grid size-4 place-items-center">
      <ProjectIcon project={project} size={16} />
    </span>
  );
}

function Item({
  children,
  icon,
  onSelect,
  value,
  keywords,
  keys,
  aside,
}: {
  children: ReactNode;
  icon: ReactNode;
  onSelect: () => void;
  value?: string;
  keywords?: string[];
  keys?: string;
  aside?: ReactNode;
}) {
  return (
    <Command.Item
      value={value}
      keywords={keywords}
      onSelect={onSelect}
      className="flex h-10 cursor-default items-center gap-3 rounded-[7px] px-2.5 text-base text-ink-2 select-none data-[selected=true]:bg-paper-select data-[selected=true]:text-ink [&_svg]:size-4 [&>svg]:text-ink-3"
    >
      {icon}
      <span className="flex min-w-0 flex-1 items-center truncate">{children}</span>
      {aside && <span className="shrink-0 text-xs text-ink-3">{aside}</span>}
      {keys && (
        <span className="flex shrink-0 items-center gap-0.5" aria-label={`Shortcut ${keys}`}>
          {keyCaps(keys).map((k, i) => (
            <Fragment key={i}>
              <kbd className="kbd">{k}</kbd>
            </Fragment>
          ))}
        </span>
      )}
    </Command.Item>
  );
}
