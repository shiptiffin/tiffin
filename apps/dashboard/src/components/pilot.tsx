import { cn } from "@/lib/cn";

export type PilotState = "off" | "on" | "busy" | "fault";

/**
 * A pilot light. Steady means on, nothing more. It blinks (1 Hz) only while
 * something is actually building or applying: the one blinking thing in the
 * product. With reduced motion it holds steady.
 */
export function PilotLight({ state, label, className }: { state: PilotState; label?: string; className?: string }) {
  return (
    <span
      className={cn("pilot", className)}
      data-state={state}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    />
  );
}
