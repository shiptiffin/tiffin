import type { Tier } from "@/api/client";
import { cn } from "@/lib/cn";

/**
 * The risk language. Shape carries the meaning as much as colour, so it
 * survives colour blindness and grayscale screenshots:
 *   reversible   — a loop: it comes back around
 *   outbound     — an arrow leaving the box
 *   irreversible — a solid diamond: one way, sharp edges
 */
export function RiskMark({ tier, className }: { tier: Tier; className?: string }) {
  const common = { viewBox: "0 0 16 16", "aria-hidden": true, className: cn("size-3.5 shrink-0", toneText[tier], className) } as const;
  switch (tier) {
    case "read":
      return (
        <svg {...common} fill="none" stroke="currentColor" strokeWidth={1.5}>
          <circle cx="8" cy="8" r="4.75" />
        </svg>
      );
    case "reversible":
      return (
        <svg {...common} fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
          <path d="M3.4 9.2A4.75 4.75 0 1 0 4.6 4.6" />
          <path d="M3.6 2.4v2.9h2.9" />
        </svg>
      );
    case "outbound":
      return (
        <svg {...common} fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
          <path d="M7 3.25H4.25a1 1 0 0 0-1 1v7.5a1 1 0 0 0 1 1h7.5a1 1 0 0 0 1-1V9" />
          <path d="M9.5 2.75h3.75V6.5M13.25 2.75 7.75 8.25" />
        </svg>
      );
    case "irreversible":
      return (
        <svg {...common} fill="currentColor">
          <path d="M8 1.6 14.4 8 8 14.4 1.6 8Z" />
          <path d="M8 5v3.6" stroke="var(--paper-raised)" strokeWidth={1.6} strokeLinecap="round" />
          <circle cx="8" cy="10.9" r="0.95" fill="var(--paper-raised)" />
        </svg>
      );
  }
}

const toneText: Record<Tier, string> = {
  read: "text-ink-3",
  reversible: "text-ok",
  outbound: "text-warn-ink",
  irreversible: "text-danger",
};
