import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { LayoutGrid, Plus } from "lucide-react";
import { Popover as P } from "radix-ui";
import { useState, type ReactNode } from "react";
import { q } from "@/api/queries";
import { useRecentProjects } from "@/lib/recent";
import { useSwitchToProject } from "@/lib/switch";
import { ProjectIcon } from "@/components/project-icon";

/**
 * The project switcher's popover: search, the five projects you opened last,
 * All projects and New project. Picking a project keeps your place: from
 * bookshop › Database › SQL to blog › Database › SQL when blog has a
 * database, else blog's Overview with a quiet note. The trigger (shell.tsx)
 * renders on the first paint; this part loads a moment later. ⌘K and "g p"
 * reach it too.
 */
export function SwitcherPopover({ open, onOpenChange, trigger, current }: { open: boolean; onOpenChange: (o: boolean) => void; trigger: ReactNode; current?: string }) {
  const projects = useQuery(q.projects);
  const recent = useRecentProjects();
  const names = (projects.data ?? []).map((p) => p.name);
  const navigate = useNavigate();
  const switchTo = useSwitchToProject();
  const [query, setQuery] = useState("");
  const shortList = [...recent.filter((p) => names.includes(p)), ...names.filter((p) => !recent.includes(p))].slice(0, 5);
  const list = query ? [...recent.filter((p) => names.includes(p)), ...names.filter((p) => !recent.includes(p))] : shortList;
  const go = (to: () => void) => {
    onOpenChange(false);
    setQuery("");
    to();
  };
  return (
    <P.Root open={open} onOpenChange={onOpenChange}>
      <P.Trigger asChild>{trigger}</P.Trigger>
      <P.Portal>
        <P.Content
          align="start"
          sideOffset={6}
          className="z-50 w-[min(18rem,calc(100vw-2rem))] overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised shadow-overlay outline-hidden data-[state=open]:animate-pop"
        >
          <Command label="Switch project" loop>
            <Command.Input
              value={query}
              onValueChange={setQuery}
              placeholder="Find a project…"
              className="h-10 w-full border-b border-rule bg-transparent px-3 text-[0.875rem] text-ink outline-hidden placeholder:text-ink-4"
            />
            <Command.List className="max-h-[320px] overflow-y-auto p-1.5">
              <Command.Empty className="px-2.5 py-3 text-sm text-ink-3">No project called that.</Command.Empty>
              <Command.Group heading={query ? undefined : "Recent"} className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pt-1 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:text-ink-3">
                {list.map((p) => (
                  <Row key={p} value={p} onSelect={() => go(() => switchTo(p))}>
                    <ProjectIcon project={p} size={16} />
                    <span className="truncate">{p}</span>
                    {p === current && <span className="ml-auto text-xs text-ink-3">here</span>}
                  </Row>
                ))}
              </Command.Group>
              <Command.Separator className="my-1 h-px bg-rule" />
              <Row value="all projects" onSelect={() => go(() => navigate({ to: "/" }))}>
                <LayoutGrid className="size-4 text-ink-3" />
                All projects
              </Row>
              <Row value="new project" onSelect={() => go(() => navigate({ to: "/new" }))}>
                <Plus className="size-4 text-ink-3" />
                New project
                <span className="ml-auto flex gap-0.5" aria-hidden>
                  <kbd className="kbd">G</kbd>
                  <kbd className="kbd">N</kbd>
                </span>
              </Row>
            </Command.List>
          </Command>
        </P.Content>
      </P.Portal>
    </P.Root>
  );
}

function Row({ value, onSelect, children }: { value: string; onSelect: () => void; children: ReactNode }) {
  return (
    <Command.Item
      value={value}
      onSelect={onSelect}
      className="flex h-8 cursor-pointer items-center gap-2.5 rounded-[6px] px-2.5 text-[0.875rem] text-ink-2 data-[selected=true]:bg-paper-select data-[selected=true]:text-ink"
    >
      {children}
    </Command.Item>
  );
}

