import { Tooltip as T } from "radix-ui";
import type { ReactNode } from "react";

export const TooltipProvider = T.Provider;

export function Tip({ label, children, side = "top" }: { label: ReactNode; children: ReactNode; side?: "top" | "bottom" | "left" | "right" }) {
  return (
    <T.Provider delayDuration={300}>
      <T.Root>
      <T.Trigger asChild>{children}</T.Trigger>
      <T.Portal>
        <T.Content
          side={side}
          sideOffset={6}
          className="z-50 max-w-72 rounded-md bg-ink px-2 py-1 text-sm text-on-ink shadow-pop data-[state=delayed-open]:animate-fade"
        >
          {label}
        </T.Content>
      </T.Portal>
      </T.Root>
    </T.Provider>
  );
}
