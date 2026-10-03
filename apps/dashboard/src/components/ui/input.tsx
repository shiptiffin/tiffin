import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export function Input({ className, ...props }: ComponentProps<"input">) {
  return (
    <input
      className={cn(
        "h-9 w-full rounded-md border border-rule bg-paper px-3 text-base text-ink placeholder:text-ink-4 transition-[border-color,box-shadow] outline-none hover:border-rule-strong focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] focus-visible:outline-none aria-invalid:border-irr",
        className,
      )}
      {...props}
    />
  );
}

export function Label({ className, ...props }: ComponentProps<"label">) {
  return <label className={cn("text-sm font-medium text-ink", className)} {...props} />;
}
