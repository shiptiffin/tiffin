import { useQueries, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { Dialog as D } from "radix-ui";
import {
  Fingerprint,
  Stamp,
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
import { RiskMark } from "./risk";
import { useCurrentProject } from "@/lib/project";
import { mcpCommand } from "@/lib/mcp";
import { useEnamels } from "@/lib/enamel";
import { openTray, stage } from "@/lib/staged";
import { EnamelSwatch } from "./enamel-swatch";
import { INSTANCE_STOPS } from "./throttle";


// Box-wide pages and each project's pages, so every area is a keystroke away.
const box: Array<[string, string, string[]]> = [
  ["Metrics", "/metrics", ["cpu", "memory", "disk", "charts"]],
  ["Logs", "/logs", ["logsql", "search", "tail"]],
  ["Errors", "/errors", ["issues", "exceptions", "sentry"]],
  ["Alerts", "/alerts", ["rules", "notify"]],
  ["Backups", "/backups", ["restore", "snapshot"]],
  ["Protection", "/protect", ["under attack", "ban", "crowdsec", "firewall", "waf", "rate limit"]],
];
const projectPages: Array<[string, string, string[]]> = [
  ["Overview", "/projects/$project", ["resources"]],
  ["Apps and deploys", "/projects/$project/apps", ["deploy", "rollback", "preview", "restart"]],
  ["Tables", "/projects/$project/data", ["postgres", "database"]],
  ["Run SQL", "/projects/$project/data/sql", ["query", "postgres"]],
  ["Database branches", "/projects/$project/data/branches", ["clone", "preview"]],
  ["Key-value", "/projects/$project/data/kv", ["valkey", "redis", "cache"]],
  ["Storage", "/projects/$project/storage", ["buckets", "files", "s3", "upload"]],
  ["Email inbox", "/projects/$project/email", ["mail", "dev inbox", "relay"]],
  ["Queues", "/projects/$project/queues", ["jobs", "dead letter", "cron"]],
  ["Workflows", "/projects/$project/workflows", ["runs", "durable"]],
  ["Users", "/projects/$project/users", ["auth", "sign in", "ban"]],
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
  const enamels = useEnamels((projects ?? []).map((p) => p.name));
  // Levers have ⌘K twins: "scale web to 4" (or "scale web in shop to 4") stages the same change the throttle would.
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
                <Command.Group heading="Stage">
                  {scaleHits.map((h) => (
                    <Item
                      key={h.project + h.app}
                      value={search}
                      icon={<Gauge />}
                      onSelect={run(() => {
                        stage(h.project, { kind: "instances", app: h.app, from: h.from, to: h.to });
                        openTray(h.project);
                      })}
                    >
                      Scale {h.app} in {h.project} from {h.from} to {h.to} instances
                      <span className="ml-2 text-xs text-ink-3">opens the plan</span>
                    </Item>
                  ))}
                </Command.Group>
              )}
              <Command.Group heading="Go to">
                <Item icon={<ScrollText />} onSelect={run(() => navigate({ to: "/" }))} keywords={["home", "stack", "memory", "room"]}>
                  Box
                </Item>
                <Item icon={<ScrollText />} onSelect={run(() => navigate({ to: "/ledger", search: {} }))} keywords={["activity", "changes", "history"]}>
                  Ledger
                </Item>
                <Item icon={<Gauge />} onSelect={run(() => navigate({ to: "/status" }))} keywords={["status", "checks"]}>
                  Health
                </Item>
                <Item icon={<Gauge />} onSelect={run(() => navigate({ to: "/settings" }))} keywords={["box", "export", "import", "domain"]}>
                  Settings
                </Item>
                <Item icon={<Stamp />} onSelect={run(() => navigate({ to: "/approvals" }))} keywords={["approve", "passkey", "waiting"]}>
                  Approvals
                </Item>
                <Item icon={<KeyRound />} onSelect={run(() => navigate({ to: "/tokens", search: {} }))}>
                  Tokens
                </Item>
                <Item icon={<Users />} onSelect={run(() => navigate({ to: "/settings/people" }))} keywords={["team", "invite", "roles"]}>
                  People
                </Item>
                <Item icon={<Fingerprint />} onSelect={run(() => navigate({ to: "/settings/passkeys" }))} keywords={["webauthn", "security"]}>
                  Passkeys
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
                </Command.Group>
              )}
              <Command.Group heading="Do">
                <Item icon={<Plus />} onSelect={run(() => navigate({ to: "/tokens", search: { create: true } }))} keywords={["new", "agent", "key"]}>
                  Create a token
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
                      icon={<span className="grid size-4 place-items-center"><EnamelSwatch enamel={enamels[p.name]} /></span>}
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
