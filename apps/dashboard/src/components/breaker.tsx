import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export type BreakerState = "on" | "off" | "tripped";

/**
 * An on/off toggle, the familiar kind: a horizontal switch, brass when on.
 * Its label sits beside it (pass `printed="beside"` to print the label, or
 * put your own text next to it). A crashed service is still "on": say what
 * happened in the row's status line, not on the switch.
 *
 *   <Breaker label="Database" state="on" onFlip={(next) => change(project, …)} />
 *
 * `staged` is the position on its way (the change is applying): the switch
 * shows it, with a small spinner. Keyboard: Space/Enter toggles it.
 * (The file keeps its old name so every caller changed at once.)
 */
export function Breaker({
  label,
  state,
  staged,
  onFlip,
  size = "sm",
  printed = false,
  className,
  ...rest
}: {
  /** What it switches ("Database"). */
  label: string;
  /** What is live now. */
  state: BreakerState;
  /** The position on its way, while the change applies. */
  staged?: "on" | "off";
  /** Called with the position the person asked for. */
  onFlip?: (next: "on" | "off") => void;
  size?: "sm" | "md";
  /** Print the label beside the switch. */
  printed?: "below" | "beside" | false;
} & Omit<ComponentProps<"button">, "onClick" | "children">) {
  const live = state === "tripped" ? "on" : state;
  const shown = staged ?? live;
  const busy = !!staged && staged !== live;
  const words = busy ? `${label}: turning ${staged}` : `${label}: ${live}`;
  const button = (
    <button
      type="button"
      role="switch"
      aria-checked={shown === "on"}
      aria-label={words}
      aria-busy={busy || undefined}
      data-size={size}
      className={cn("toggle", className)}
      onClick={(e) => {
        e.stopPropagation();
        onFlip?.(shown === "on" ? "off" : "on");
      }}
      {...rest}
    >
      <span className="toggle-thumb" aria-hidden>
        {busy && <span className="spinner" />}
      </span>
    </button>
  );
  if (!printed) return button;
  return (
    <span className="inline-flex items-center gap-2.5">
      {button}
      <span aria-hidden className="text-[0.875rem] text-ink">
        {label}
      </span>
    </span>
  );
}
