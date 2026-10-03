import { cn } from "@/lib/cn";
import { enamelNames, enamelVar, type Enamel } from "@/lib/enamel";

/**
 * A project's colour as a small square chip: in the sidebar, breadcrumbs,
 * ⌘K and legends. Decorative unless `label` is set.
 */
export function EnamelSwatch({ enamel, size = 8, label, className }: { enamel: Enamel; size?: number; label?: boolean; className?: string }) {
  return (
    <span
      role={label ? "img" : undefined}
      aria-label={label ? `${enamelNames[enamel]} enamel` : undefined}
      aria-hidden={label ? undefined : true}
      className={cn("inline-block shrink-0 rounded-[2px]", className)}
      style={{ width: size, height: size, background: enamelVar(enamel), boxShadow: "inset 0 1px 0 oklch(1 0 0 / 0.18)" }}
    />
  );
}
