import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export type BreakerState = "on" | "off" | "tripped";

/**
 * A circuit breaker, for services: paddle up is on, down is off, and
 * mid-travel in red is tripped (crashed). It never applies anything itself:
 * flipping it stages a change (see lib/staged.ts), drawn in brass with the
 * old position dashed, until the plan tray applies it.
 *
 *   <Breaker label="Postgres" state="on" staged="off" onFlip={(next) => stage(...)} />
 *
 * Keyboard: it is a switch (Space/Enter). A tripped breaker flips to on (a reset).
 */
export function Breaker({
  label,
  state,
  staged,
  onFlip,
  size = "sm",
  printed = true,
  className,
  ...rest
}: {
  /** What it switches, for screen readers ("Postgres"). */
  label: string;
  /** What is live now. */
  state: BreakerState;
  /** A staged position, if a change is waiting in the tray. */
  staged?: "on" | "off";
  /** Called with the position the person asked for. */
  onFlip?: (next: "on" | "off") => void;
  size?: "sm" | "md";
  /** Print the position beside the switch in small caps (ON / OFF / TRIPPED), as on a panel. */
  printed?: boolean;
} & Omit<ComponentProps<"button">, "onClick" | "children">) {
  const shown = staged ?? state;
  const next: "on" | "off" = shown === "on" ? "off" : "on";
  const words = staged
    ? `${label}: ${state} now, ${staged} staged`
    : state === "tripped"
      ? `${label}: tripped. Reset to on`
      : `${label}: ${state}`;
  const button = (
    <button
      type="button"
      role="switch"
      aria-checked={shown === "on"}
      aria-label={words}
      title={words}
      data-pos={shown}
      data-size={size}
      data-staged={staged ? "" : undefined}
      className={cn("breaker", className)}
      onClick={(e) => {
        e.stopPropagation();
        onFlip?.(next);
      }}
      {...rest}
    >
      {staged && staged !== state && state !== "tripped" && <span className="was" data-at={state} aria-hidden />}
    </button>
  );
  if (!printed) return button;
  return (
    <span className="inline-flex items-center gap-2">
      {button}
      <span aria-hidden className="breaker-print" data-pos={shown} data-staged={staged && staged !== state ? "" : undefined}>
        {shown === "tripped" ? "trip" : shown}
      </span>
    </span>
  );
}
