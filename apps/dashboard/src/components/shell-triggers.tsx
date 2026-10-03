import type { ComponentProps } from "react";
import { roleCopy, useMe } from "@/lib/me";

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
  if (!me) return <div className="h-8" />;
  const label = name ?? "You";
  return (
    <button
      type="button"
      aria-label="Account"
      className="flex h-8 w-full items-center gap-2.5 rounded-[7px] px-1 text-[0.8125rem] text-ink transition-colors hover:bg-paper-sunk data-[state=open]:bg-paper-sunk"
      {...props}
    >
      <span aria-hidden className="grid size-6 shrink-0 place-items-center rounded-full bg-ink text-[0.6875rem] font-[550] text-paper">
        {label.replace(/[^a-z0-9]/gi, "").slice(0, 1).toUpperCase() || "?"}
      </span>
      <span className="min-w-0 flex-1 truncate text-left">{label}</span>
      {role && roleCopy[role]?.label.toLowerCase() !== label.toLowerCase() && <span className="text-xs text-ink-3">{roleCopy[role]?.label.toLowerCase()}</span>}
    </button>
  );
}
