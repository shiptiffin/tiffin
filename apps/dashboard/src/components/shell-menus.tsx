import { useQuery } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { Fingerprint, KeyRound, LogOut, Terminal } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { q } from "@/api/queries";
import { copyText } from "@/lib/clipboard";
import { mcpCommand } from "@/lib/mcp";
import { roleCopy, useMe } from "@/lib/me";
import { useCurrentProject } from "@/lib/project";
import { relative } from "@/lib/time";
import { clickedEarly, ProjectTrigger, WhoTrigger } from "./shell-triggers";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from "./ui/dropdown";

// The shell's menus and the phone nav sheet: loaded just after the first paint
// (see shell.tsx), so Radix stays out of the initial bundle.

export function NavSheet({ open, onOpenChange, children }: { open: boolean; onOpenChange: (o: boolean) => void; children: ReactNode }) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-[oklch(0.15_0.01_60/0.45)] data-[state=open]:animate-fade lg:hidden" />
        <D.Content className="fixed inset-y-0 left-0 z-50 w-[min(84vw,300px)] border-r border-rule bg-paper-sunk shadow-pop outline-none data-[state=open]:animate-[rise_300ms_var(--ease-out-soft)] lg:hidden">
          <D.Title className="sr-only">Navigation</D.Title>
          <D.Description className="sr-only">Pages and projects</D.Description>
          {children}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}

export function WhoMenu() {
  const { me, name, role, admin } = useMe();
  const navigate = useNavigate();
  const [copied, setCopied] = useState(false);
  const [early] = useState(() => clickedEarly("who"));
  if (!me) return <div className="size-9" />;
  const label = name ?? "You";
  return (
    <Menu defaultOpen={early}>
      <MenuTrigger asChild>
        <WhoTrigger />
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

export function ProjectSwitcher() {
  const { data: projects } = useQuery(q.projects);
  const project = useCurrentProject();
  const navigate = useNavigate();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const onActivity = path === "/";
  const [early] = useState(() => clickedEarly("project"));
  const pick = (v: string) => {
    if (!v) navigate({ to: "/", search: {} });
    else if (onActivity) navigate({ to: "/", search: { project: v } });
    else if (path.endsWith("/secrets")) navigate({ to: "/projects/$project/secrets", params: { project: v } });
    else navigate({ to: "/projects/$project", params: { project: v } });
  };
  return (
    <Menu defaultOpen={early}>
      <MenuTrigger asChild>
        <ProjectTrigger />
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
