import { ChevronsUpDown } from "lucide-react";
import type { ComponentProps } from "react";
import { useMe } from "@/lib/me";
import { useCurrentProject } from "@/lib/project";
import { ActorMark } from "./actor";

/** A click on a trigger before its menu has loaded is kept, and the menu opens when it arrives. */
const early = { who: false, project: false };
export function clickedEarly(which: keyof typeof early): boolean {
  const v = early[which];
  early[which] = false;
  return v;
}
export const rememberClick = (which: keyof typeof early) => () => {
  early[which] = true;
};

// The buttons that open the shell's menus. They render on the first paint on
// their own, and the menus (loaded a moment later) wrap these same buttons.

export function WhoTrigger(props: ComponentProps<"button">) {
  const { me, name, role } = useMe();
  if (!me) return <div className="size-9" />;
  const label = name ?? "You";
  return (
    <button
      type="button"
      aria-label="Account"
      className="flex h-9 items-center gap-2 rounded-lg px-2 text-base text-ink-2 transition-colors hover:bg-hover hover:text-ink data-[state=open]:bg-hover"
      {...props}
    >
      <ActorMark actor={{ kind: role === "owner" ? "owner" : "human", name: label, id: me.tokenId }} />
      <span className="hidden max-w-40 truncate sm:inline">{label}</span>
      <ChevronsUpDown className="hidden size-3.5 text-ink-4 sm:block" />
    </button>
  );
}

export function ProjectTrigger(props: ComponentProps<"button">) {
  const project = useCurrentProject();
  return (
    <button
      type="button"
      className="flex h-11 w-full items-center gap-2.5 rounded-lg border border-rule bg-raised/60 px-2.5 text-left transition-colors hover:border-rule-strong hover:bg-raised data-[state=open]:border-rule-strong"
      {...props}
    >
      <span className="grid size-6 place-items-center rounded-md bg-paper font-mono text-xs text-ink-2 ring-1 ring-rule">
        {project ? project.slice(0, 1).toUpperCase() : "*"}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-2xs font-medium tracking-wider text-ink-3 uppercase">Project</span>
        <span className="block truncate text-base text-ink">{project ?? "All projects"}</span>
      </span>
      <ChevronsUpDown className="size-3.5 text-ink-4" />
    </button>
  );
}
