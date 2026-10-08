import { Slot } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

/**
 * Buttons say the verb, the object and the number ("Apply 3 changes to shop").
 *   primary    brass: the one commitment on a screen
 *   secondary  paper with a rule: everything else that acts
 *   ghost      words only: cancel, dismiss, secondary navigation
 *   danger     red: only after an irreversible action is armed
 *   danger-outline  a red-outlined button: opens a destructive action's confirm (Settings › Danger zone)
 *
 * Disabled: primary turns to pressed paper; the others fade to 45%.
 *
 * Variant and size classes are zero-specificity ([:where(&)]:…), so a caller's
 * className (h-9, text-ink-3, hover:text-danger) always wins without
 * tailwind-merge. `bun run check:overrides` keeps it that way.
 */
const variants = {
  primary:
    "[:where(&)]:bg-brass [:where(&)]:text-on-brass [:where(&)]:shadow-[inset_0_1px_0_oklch(1_0_0/0.2),0_1px_1px_oklch(0.3_0.05_70/0.2)] [:where(&)]:hover:bg-[color-mix(in_oklch,var(--brass)_92%,var(--ink))] [:where(&)]:disabled:bg-paper-press [:where(&)]:disabled:text-ink-3 [:where(&)]:disabled:shadow-none",
  secondary: "[:where(&)]:bg-paper-raised [:where(&)]:text-ink [:where(&)]:border [:where(&)]:border-rule-2 [:where(&)]:shadow-[var(--top-light),0_1px_0_oklch(0.235_0.014_60/0.04)] [:where(&)]:hover:bg-paper-hover [:where(&)]:disabled:opacity-45",
  ghost: "[:where(&)]:text-ink-2 [:where(&)]:hover:text-ink [:where(&)]:hover:bg-paper-hover [:where(&)]:disabled:opacity-45",
  danger: "[:where(&)]:bg-danger [:where(&)]:text-on-danger [:where(&)]:shadow-[inset_0_1px_0_oklch(1_0_0/0.16)] [:where(&)]:hover:bg-[color-mix(in_oklch,var(--danger)_90%,var(--ink))] [:where(&)]:disabled:opacity-45",
  "danger-quiet": "[:where(&)]:text-danger [:where(&)]:hover:bg-danger-wash [:where(&)]:disabled:opacity-45",
  "danger-outline":
    "[:where(&)]:bg-paper-raised [:where(&)]:text-danger [:where(&)]:border [:where(&)]:border-danger-rule [:where(&)]:shadow-[var(--top-light)] [:where(&)]:hover:bg-danger-wash [:where(&)]:disabled:opacity-45",
} as const;

const sizes = {
  sm: "[:where(&)]:h-7 [:where(&)]:px-2.5 [:where(&)]:text-[0.8125rem] [:where(&)]:gap-1.5 [:where(&)]:rounded-[6px]",
  md: "[:where(&)]:h-8 [:where(&)]:px-3 [:where(&)]:text-[0.84375rem] [:where(&)]:gap-2 [:where(&)]:rounded-[7px]",
  lg: "[:where(&)]:h-[38px] [:where(&)]:px-4 [:where(&)]:text-[0.875rem] [:where(&)]:gap-2 [:where(&)]:rounded-[8px]",
  icon: "[:where(&)]:size-8 [:where(&)]:rounded-[7px] [:where(&)]:justify-center",
  "icon-sm": "[:where(&)]:size-7 [:where(&)]:rounded-[6px] [:where(&)]:justify-center",
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
      data-slot="button"
      data-variant={variant}
      data-size={size}
      className={cn(
        "inline-flex shrink-0 items-center justify-center font-[550] whitespace-nowrap select-none",
        "transition-[background-color,border-color,color,transform,opacity] duration-[var(--dur-state)] ease-[var(--ease-out)] active:scale-[0.97] active:duration-[var(--dur-press)]",
        "disabled:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
        variants[variant],
        sizes[size],
        className,
      )}
      {...props}
    />
  );
}
