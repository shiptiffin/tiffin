import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

/** Size, padding, type and border colour are zero-specificity: a caller's h-8, w-36 or text-sm wins. */
export function Input({ className, ...props }: ComponentProps<"input">) {
  return (
    <input
      data-slot="input"
      className={cn(
        "[:where(&)]:h-9 [:where(&)]:w-full rounded-md border [:where(&)]:border-rule bg-paper [:where(&)]:px-3 [:where(&)]:text-base text-ink placeholder:text-ink-4 transition-[border-color,box-shadow] outline-hidden hover:border-rule-2 focus-visible:border-brass focus-visible:shadow-[0_0_0_3px_var(--brass-wash)] aria-invalid:border-danger",
        className,
      )}
      {...props}
    />
  );
}

export function Label({ className, ...props }: ComponentProps<"label">) {
  return <label data-slot="label" className={cn("[:where(&)]:text-sm font-medium [:where(&)]:text-ink", className)} {...props} />;
}
