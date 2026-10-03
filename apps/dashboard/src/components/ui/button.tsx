import { Slot } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

/**
 * Buttons say the verb, the object and the number ("Apply 3 changes to shop").
 *   primary    brass: the one commitment on a screen
 *   secondary  paper with a rule: everything else that acts
 *   ghost      words only: cancel, dismiss, secondary navigation
 *   danger     red: only after an irreversible action is armed
 */
const variants = {
  primary:
    "bg-brass text-on-brass shadow-[inset_0_1px_0_oklch(1_0_0/0.2),0_1px_1px_oklch(0.3_0.05_70/0.2)] hover:bg-[color-mix(in_oklch,var(--brass)_92%,var(--ink))]",
  secondary: "bg-paper-raised text-ink border border-rule-2 shadow-[var(--top-light),0_1px_0_oklch(0.235_0.014_60/0.04)] hover:bg-paper-sunk",
  ghost: "text-ink-2 hover:text-ink hover:bg-paper-sunk",
  danger: "bg-danger text-on-danger shadow-[inset_0_1px_0_oklch(1_0_0/0.16)] hover:bg-[color-mix(in_oklch,var(--danger)_90%,var(--ink))]",
  "danger-quiet": "text-danger hover:bg-danger-wash",
} as const;

const sizes = {
  sm: "h-7 px-2.5 text-[0.8125rem] gap-1.5 rounded-[6px]",
  md: "h-8 px-3 text-[0.84375rem] gap-2 rounded-[7px]",
  lg: "h-[38px] px-4 text-[0.875rem] gap-2 rounded-[8px]",
  icon: "size-8 rounded-[7px] justify-center",
  "icon-sm": "size-7 rounded-[6px] justify-center",
} as const;

export type ButtonProps = ComponentProps<"button"> & {
  variant?: keyof typeof variants;
  size?: keyof typeof sizes;
  asChild?: boolean;
};

export function Button({ variant = "secondary", size = "md", asChild, className, ...props }: ButtonProps) {
  const Comp = asChild ? Slot.Root : "button";
  return (
    <Comp
      className={cn(
        "inline-flex shrink-0 items-center justify-center font-[550] whitespace-nowrap select-none",
        "transition-[background-color,border-color,color,transform,opacity] duration-[var(--dur-state)] ease-[var(--ease-out)] active:scale-[0.97] active:duration-[var(--dur-press)]",
        "disabled:pointer-events-none disabled:opacity-45 [&_svg]:size-4 [&_svg]:shrink-0",
        variants[variant],
        sizes[size],
        className,
      )}
      {...props}
    />
  );
}
