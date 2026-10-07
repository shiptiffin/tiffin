import { useNavigate } from "@tanstack/react-router";
import { Fingerprint, KeyRound, LogOut, MonitorSmartphone, Terminal } from "lucide-react";
import { Dialog as D } from "radix-ui";
import { useState, type ReactNode } from "react";
import { copyText } from "@/lib/clipboard";
import { mcpCommand } from "@/lib/mcp";
import { roleCopy, useMe } from "@/lib/me";
import { relative } from "@/lib/time";
import { passkeyWords } from "@/lib/webauthn";
import { signOut } from "@/lib/command-history";
import { clickedEarly, WhoTrigger } from "./shell-triggers";
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from "./ui/dropdown";

// The shell's menus and the phone nav sheet: loaded just after the first paint
// (see shell.tsx), so Radix stays out of the initial bundle.

export function NavSheet({ open, onOpenChange, children }: { open: boolean; onOpenChange: (o: boolean) => void; children: ReactNode }) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-[var(--scrim)] data-[state=open]:animate-fade lg:hidden" />
        <D.Content className="fixed inset-y-0 left-0 z-50 w-[min(84vw,300px)] border-r border-rule-2 bg-paper shadow-overlay outline-hidden data-[state=open]:animate-[sheet-in_var(--dur-tray)_var(--ease-tray)_both] lg:hidden">
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
      <MenuContent align="start" side="top" className="w-72">
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
          <MenuItem onSelect={() => navigate({ to: "/settings/keys", search: {} })}>
            <KeyRound />
            API keys
          </MenuItem>
        )}
        <MenuItem onSelect={() => navigate({ to: "/settings/sign-ins" })}>
          <MonitorSmartphone />
          Sign-ins
        </MenuItem>
        <MenuItem onSelect={() => navigate({ to: "/settings/passkeys" })} className="h-auto py-1.5">
          <Fingerprint />
          <span className="flex flex-col leading-[1.15rem]">
            <span>Sign in with {passkeyWords().name}</span>
            <span className="text-xs text-ink-3">{passkeyWords().how.replace(/^./, (c) => c.toUpperCase())} instead of a link</span>
          </span>
        </MenuItem>
        <MenuSeparator />
        <MenuItem
          onSelect={signOut}
        >
          <LogOut />
          Sign out
        </MenuItem>
      </MenuContent>
    </Menu>
  );
}
