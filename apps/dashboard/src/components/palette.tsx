import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { Dialog as D } from "radix-ui";
import { CornerDownLeft, FolderClosed, Gauge, KeyRound, LogOut, Monitor, Moon, Plus, ScrollText, Search, Sun, Terminal } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { asTier } from "@/lib/changes";
import { copyText } from "@/lib/clipboard";
import { setTheme } from "@/lib/theme";
import { RiskMark } from "./risk";

export function mcpCommand() {
  return `claude mcp add --transport http tiffin ${location.origin}/mcp --header "Authorization: Bearer <agent token>"`;
}

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const navigate = useNavigate();
  const { data: projects } = useQuery({ ...q.projects, enabled: open });
  const { data: changes } = useQuery({ ...q.changes(), enabled: open });
  const [toast, setToast] = useState<string | null>(null);
  const [search, setSearch] = useState("");

  const run = (fn: () => void) => () => {
    onOpenChange(false);
    setSearch("");
    fn();
  };

  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-50 bg-[oklch(0.15_0.01_60/0.4)] backdrop-blur-[2px] data-[state=open]:animate-fade" />
        <D.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[calc(100vw-2rem)] max-w-[600px] -translate-x-1/2 overflow-hidden rounded-xl border border-rule bg-raised shadow-pop outline-none data-[state=open]:animate-pop"
        >
          <D.Title className="sr-only">Command palette</D.Title>
          <Command label="Command palette" loop className="flex max-h-[min(70vh,520px)] flex-col">
            <div className="flex items-center gap-2.5 border-b border-rule px-4">
              <Search className="size-4 shrink-0 text-ink-3" />
              <Command.Input
                value={search}
                onValueChange={setSearch}
                placeholder="Jump to a page, project or change…"
                className="h-13 w-full bg-transparent text-md text-ink outline-none placeholder:text-ink-4"
              />
              <kbd className="kbd">esc</kbd>
            </div>
            <Command.List className="min-h-0 flex-1 overflow-y-auto p-2 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:pt-2 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-2xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-ink-3 [&_[cmdk-group-heading]]:uppercase">
              <Command.Empty className="px-3 py-8 text-center text-base text-ink-3">
                Nothing matches. Try a project name or part of an intent.
              </Command.Empty>
              <Command.Group heading="Go to">
                <Item icon={<ScrollText />} onSelect={run(() => navigate({ to: "/", search: {} }))}>
                  Activity
                </Item>
                <Item icon={<Gauge />} onSelect={run(() => navigate({ to: "/status" }))}>
                  Status
                </Item>
                <Item icon={<KeyRound />} onSelect={run(() => navigate({ to: "/tokens", search: {} }))}>
                  Tokens
                </Item>
              </Command.Group>
              <Command.Group heading="Do">
                <Item icon={<Plus />} onSelect={run(() => navigate({ to: "/tokens", search: { create: true } }))} keywords={["new", "agent", "key"]}>
                  Create a token
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
                      icon={<FolderClosed />}
                      onSelect={run(() => navigate({ to: "/", search: { project: p.name } }))}
                    >
                      {p.name}
                      <span className="ml-2 font-mono text-xs text-ink-4">v{p.version}</span>
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
                      <span className="ml-2 shrink-0 font-mono text-xs text-ink-4">{c.project}</span>
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
      className="flex h-10 cursor-default items-center gap-3 rounded-lg px-2.5 text-base text-ink-2 select-none data-[selected=true]:bg-hover data-[selected=true]:text-ink [&_svg]:size-4 [&>svg]:text-ink-3"
    >
      {icon}
      <span className="flex min-w-0 flex-1 items-center">{children}</span>
    </Command.Item>
  );
}
