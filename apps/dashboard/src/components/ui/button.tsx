import { Slot } from "radix-ui";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

const variants = {
  primary: "bg-ink text-on-ink hover:bg-ink/88 active:bg-ink/80 shadow-[0_1px_0_oklch(1_0_0/0.12)_inset,0_1px_2px_oklch(0_0_0/0.2)]",
  secondary: "bg-raised text-ink border border-rule hover:bg-hover hover:border-rule-strong active:bg-press",
  ghost: "text-ink-2 hover:text-ink hover:bg-hover active:bg-press",
  danger: "bg-irr text-white hover:bg-irr/90 active:bg-irr/80 shadow-[0_1px_0_oklch(1_0_0/0.18)_inset,0_1px_2px_oklch(0_0_0/0.25)]",
  "danger-quiet": "text-irr hover:bg-irr-wash active:bg-irr-wash",
} as const;

const sizes = {
  sm: "h-7 px-2.5 text-sm gap-1.5 rounded-md",
  md: "h-8 px-3 text-base gap-2 rounded-md",
  lg: "h-10 px-4 text-md gap-2 rounded-lg",
  icon: "size-8 rounded-md justify-center",
  "icon-sm": "size-7 rounded-md justify-center",
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
        "inline-flex shrink-0 select-none items-center justify-center font-medium whitespace-nowrap transition-[background-color,border-color,color,transform,opacity] duration-150 ease-out active:scale-[0.98] disabled:pointer-events-none disabled:opacity-45 [&_svg]:size-4 [&_svg]:shrink-0",
        variants[variant],
        sizes[size],
        className,
      )}
      {...props}
    />
  );
}
