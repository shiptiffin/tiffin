import { Info } from "lucide-react";
import { Popover as P } from "radix-ui";
import type { ReactNode } from "react";

/**
 * A small (i) beside a setting's name that explains it. A popover, not a
 * hover tooltip, so it opens on a tap too and stays while you read.
 *
 *   <InfoTip label="About copies">More copies share the traffic…</InfoTip>
 */
export function InfoTip({ label, children }: { label: string; children: ReactNode }) {
  return (
    <P.Root>
      <P.Trigger
        aria-label={label}
        className="inline-grid size-5 place-items-center rounded-full text-ink-3 transition-colors hover:text-ink focus-visible:text-ink focus-visible:outline-hidden focus-visible:shadow-[0_0_0_2px_var(--brass-wash)]"
      >
        <Info className="size-3.5" />
      </P.Trigger>
      <P.Portal>
        <P.Content
          side="top"
          align="start"
          sideOffset={6}
          collisionPadding={12}
          className="z-50 max-w-[18rem] rounded-[10px] border border-rule-2 bg-paper-raised px-3.5 py-3 text-[0.8125rem] leading-5 text-ink-2 shadow-overlay outline-hidden data-[state=open]:animate-pop"
        >
          {children}
          <P.Arrow className="fill-paper-raised" />
        </P.Content>
      </P.Portal>
    </P.Root>
  );
}
