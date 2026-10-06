import { useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { Dialog as D } from "radix-ui";
import {
  Fingerprint,

  UserPlus,
  Users,
  CornerDownLeft,
  FolderClosed,
  Gauge,
  KeyRound,
  LogOut,
  Monitor,
  Moon,
  Plus,
  ScrollText,
  Search,
  Sun,
  Terminal,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { asTier } from "@/lib/changes";
import { copyText } from "@/lib/clipboard";
import { setTheme } from "@/lib/theme";
import { passkeyWords } from "@/lib/webauthn";
import { RiskMark } from "./risk";
import { useCurrentProject } from "@/lib/project";
import { mcpCommand } from "@/lib/mcp";
import { change } from "@/lib/staged";
import { INSTANCE_STOPS } from "./throttle";
import { ProjectIcon } from "@/components/project-icon";


// Box-wide pages and each project's pages, so every area is a keystroke away.
const box: Array<[string, string, string[]]> = [
  ["Usage", "/usage", ["box", "memory", "cpu", "disk", "room", "limits", "share"]],
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
/** The Jobs area's actions (they open its dialogs). */
const jobActions: Array<[string, "schedule" | "queue" | "send" | "run", string[]]> = [
  ["New schedule", "schedule", ["cron", "create", "every", "timer"]],
  ["New queue", "queue", ["create", "jobs"]],
  ["Send a test job", "send", ["queue", "job", "try"]],
  ["Start a workflow run", "run", ["workflow", "run"]],
];
const projectPages: Array<[string, string, string[]]> = [
  ["Overview", "/projects/$project", ["resources"]],
  ["Apps and deploys", "/projects/$project/apps", ["deploy", "rollback", "preview", "restart"]],
  ["Database tables", "/projects/$project/data", ["postgres", "database"]],
  ["Run SQL", "/projects/$project/data/sql", ["query", "postgres"]],
  ["Database branches", "/projects/$project/data/branches", ["clone", "preview"]],
  ["Cache", "/projects/$project/data/kv", ["valkey", "redis", "key-value"]],
  ["Files", "/projects/$project/storage", ["buckets", "storage", "s3", "upload"]],
  ["Email", "/projects/$project/email", ["mail", "inbox", "relay"]],
  ["Jobs: runs", "/projects/$project/jobs", ["jobs", "runs", "workflows", "progress", "live"]],
  ["Jobs: schedules", "/projects/$project/jobs/schedules", ["cron", "schedule", "timer", "pause"]],
  ["Jobs: queues", "/projects/$project/jobs/queues", ["queues", "topics", "concurrency", "rate limit"]],
  ["Jobs: failed", "/projects/$project/jobs/failed", ["dead letter", "dlq", "retry", "failed"]],
  ["Usage", "/projects/$project/usage", ["memory", "cpu", "limit", "resources", "copies", "scale"]],
  ["History", "/projects/$project/history", ["changes", "undo", "ledger"]],
  ["Settings", "/projects/$project/settings", ["env", "colour", "addresses"]],
  ["Domains", "/projects/$project/domains", ["domain", "dns", "https", "certificate", "www", "custom domain"]],
  ["Auth: users", "/projects/$project/users", ["users", "sign in", "passkeys", "ban"]],
  ["Organizations", "/projects/$project/orgs", ["teams", "members"]],
  ["Analytics", "/projects/$project/analytics", ["visitors", "pageviews", "traffic"]],
  ["Secrets", "/projects/$project/secrets", ["env", "api key"]],
];

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const navigate = useNavigate();
  const current = useCurrentProject();
  const { data: projects } = useQuery({ ...q.projects, enabled: open });
  const { data: changes } = useQuery({ ...q.changes(), enabled: open });
  const [toast, setToast] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  // "scale web to 4" (or "scale web in shop to 4") makes the same change the copies stepper would.
  const states = useQueries({ queries: (projects ?? []).map((p) => ({ ...q.project(p.name), enabled: open, refetchInterval: false as const })) });
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

  const run = (fn: () => void) => () => {
    onOpenChange(false);
    setSearch("");
    fn();
  };

  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-[var(--scrim)] data-[state=open]:animate-[fade_80ms_linear_both]" />
        <D.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-2rem)] max-w-[600px] -translate-x-1/2 overflow-hidden rounded-[12px] border border-rule-2 bg-paper-raised shadow-raised outline-none data-[state=open]:animate-[fade_80ms_linear_both]"
        >
          <D.Title className="sr-only">Command palette</D.Title>
          <Command label="Command palette" loop className="flex max-h-[min(70vh,520px)] flex-col">
            <div className="flex items-center gap-2.5 border-b border-rule px-4">
              <Search className="size-4 shrink-0 text-ink-3" />
              <Command.Input
                value={search}
                onValueChange={setSearch}
                placeholder="Jump to a page, project or change…"
                className="h-13 w-full bg-transparent text-md text-ink outline-none placeholder:text-ink-3"
              />
              <kbd className="kbd">esc</kbd>
            </div>
            <Command.List className="min-h-0 flex-1 overflow-y-auto p-2 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:pt-2 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-2xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-ink-3 [&_[cmdk-group-heading]]:uppercase">
              <Command.Empty className="px-3 py-8 text-center text-base text-ink-3">
                Nothing matches. Try a project name or part of an intent.
              </Command.Empty>
              {scaleHits.length > 0 && (
                <Command.Group heading="Do it now">
                  {scaleHits.map((h) => (
                    <Item
                      key={h.project + h.app}
                      value={search}
                      icon={<Gauge />}
                      onSelect={run(() => {
                        change(h.project, { kind: "instances", app: h.app, from: h.from, to: h.to }, { immediate: true });
                      })}
                    >
                      Run {h.app} in {h.project} on {h.to} copies (now {h.from})
                    </Item>
                  ))}
                </Command.Group>
              )}
              <Command.Group heading="Go to">
                <Item icon={<ScrollText />} onSelect={run(() => navigate({ to: "/" }))} keywords={["home", "all projects"]}>
                  Projects
                </Item>
                <Item icon={<Plus />} onSelect={run(() => navigate({ to: "/new" }))} keywords={["create", "start", "starter"]}>
                  New project
                </Item>
                <Item icon={<ScrollText />} onSelect={run(() => navigate({ to: "/ledger", search: {} }))} keywords={["activity", "changes", "ledger", "undo"]}>
                  Activity: every project’s changes
                </Item>
                <Item icon={<Gauge />} onSelect={run(() => navigate({ to: "/status" }))} keywords={["status", "checks"]}>
                  Health
                </Item>
                <Item icon={<Gauge />} onSelect={run(() => navigate({ to: "/settings" }))} keywords={["box", "export", "import", "domain"]}>
                  Settings
                </Item>
                <Item icon={<KeyRound />} onSelect={run(() => navigate({ to: "/settings/keys", search: {} }))} keywords={["tokens", "claude", "mcp", "approvals"]}>
                  API keys
                </Item>
                <Item icon={<Users />} onSelect={run(() => navigate({ to: "/settings/people" }))} keywords={["team", "invite", "roles"]}>
                  People
                </Item>
                <Item icon={<Fingerprint />} onSelect={run(() => navigate({ to: "/settings/passkeys" }))} keywords={["passkeys", "touch id", "face id", "windows hello", "fingerprint", "webauthn", "sign in"]}>
                  Sign in with {passkeyWords().name}
                </Item>
                {box.map(([label, to, kw]) => (
                  <Item key={to} icon={<Gauge />} onSelect={run(() => navigate({ to: to as "/" }))} keywords={kw}>
                    {label}
                  </Item>
                ))}
              </Command.Group>
              {current && !scale && (
                <Command.Group heading={`In ${current}`}>
                  {projectPages.map(([label, to, kw]) => (
                    <Item key={to} value={`${current} ${label}`} icon={<FolderClosed />} keywords={kw} onSelect={run(() => navigate({ to: to as "/", params: { project: current } as never }))}>
                      {label}
                    </Item>
                  ))}
                  {jobActions.map(([label, d, kw]) => (
                    <Item key={d} value={`${current} ${label}`} icon={<Plus />} keywords={kw} onSelect={run(() => navigate({ to: "/projects/$project/jobs", params: { project: current }, search: { do: d } }))}>
                      {label}
                    </Item>
                  ))}
                </Command.Group>
              )}
              <Command.Group heading="Do">
                <Item icon={<Plus />} onSelect={run(() => navigate({ to: "/settings/keys", search: { create: true } }))} keywords={["new", "agent", "token"]}>
                  Create a key
                </Item>
                <Item icon={<UserPlus />} onSelect={run(() => navigate({ to: "/settings/people" }))} keywords={["invite", "team", "person"]}>
                  Invite someone
                </Item>
                <Item
                  icon={<Terminal />}
                  keywords={["claude", "agent", "connect", "mcp"]}
                  onSelect={async () => {
                    const ok = await copyText(mcpCommand());
                    setToast(ok ? "MCP setup command copied" : "Couldn't copy");
                    setTimeout(() => {
                      setToast(null);
                      onOpenChange(false);
                    }, 900);
                  }}
                >
                  {toast ?? "Copy MCP setup command"}
                </Item>
                <Item icon={<Sun />} onSelect={run(() => setTheme("light"))} keywords={["theme"]}>
                  Light theme
                </Item>
                <Item icon={<Moon />} onSelect={run(() => setTheme("dark"))} keywords={["theme"]}>
                  Dark theme
                </Item>
                <Item icon={<Monitor />} onSelect={run(() => setTheme("system"))} keywords={["theme"]}>
                  Match system theme
                </Item>
                <Item
                  icon={<LogOut />}
                  onSelect={run(async () => {
                    try {
                      await api.logout();
                    } finally {
                      location.assign("/login?reason=signed-out");
                    }
                  })}
                >
                  Sign out
                </Item>
              </Command.Group>
              {projects && projects.length > 0 && (
                <Command.Group heading="Projects">
                  {projects.map((p) => (
                    <Item
                      key={p.name}
                      value={`project ${p.name}`}
                      icon={<span className="grid size-4 place-items-center"><ProjectIcon project={p.name} size={14} /></span>}
                      onSelect={run(() => navigate({ to: "/projects/$project", params: { project: p.name } }))}
                    >
                      {p.name}
                      <span className="ml-2 text-xs text-ink-3">version {p.version}</span>
                    </Item>
                  ))}
                </Command.Group>
              )}
              {search && changes && changes.length > 0 && (
                <Command.Group heading="Changes">
                  {changes.slice(0, 50).map((c) => (
                    <Item
                      key={c.id}
                      value={`${c.intent} ${c.project} ${c.actor.name ?? ""} ${c.id}`}
                      icon={
                        <span className="grid size-4 place-items-center">
                          <RiskMark tier={asTier(c.plan.risk)} />
                        </span>
                      }
                      onSelect={run(() => navigate({ to: "/changes/$id", params: { id: c.id } }))}
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
            </div>
          </Command>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

function Item({
  children,
  icon,
  onSelect,
  value,
  keywords,
}: {
  children: ReactNode;
  icon: ReactNode;
  onSelect: () => void;
  value?: string;
  keywords?: string[];
}) {
  return (
    <Command.Item
      value={value}
      keywords={keywords}
      onSelect={onSelect}
      className="flex h-10 cursor-default items-center gap-3 rounded-[7px] px-2.5 text-base text-ink-2 select-none data-[selected=true]:bg-paper-sunk data-[selected=true]:text-ink [&_svg]:size-4 [&>svg]:text-ink-3"
    >
      {icon}
      <span className="flex min-w-0 flex-1 items-center">{children}</span>
    </Command.Item>
  );
}
