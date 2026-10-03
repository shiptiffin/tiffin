import type { Tier } from "@/api/client";
import { cn } from "@/lib/cn";
import { tierCopy } from "@/lib/changes";

const filled: Record<Tier, number> = { read: 0, reversible: 1, outbound: 2, irreversible: 3 };

/**
 * Risk as three dots, so it reads in grayscale: ●○○ reversible, ●●○ outbound,
 * ●●● irreversible (in red, the only tier with colour).
 *
 *   <RiskDots tier="irreversible" />            ●●● Irreversible
 *   <RiskDots tier="reversible" label="Low" />  ●○○ Low
 *   <RiskDots tier="outbound" label={false} />  dots only (still announced)
 */
export function RiskDots({ tier, label, className }: { tier: Tier; label?: string | false; className?: string }) {
  const words = label === false ? undefined : (label ?? tierCopy[tier].short);
  return (
    <span className={cn("riskdots", className)} data-tier={tier}>
      <span className="dots" role="img" aria-label={`Risk: ${tierCopy[tier].label}`}>
        {[0, 1, 2].map((i) => (
          <i key={i} data-f={i < filled[tier] ? "" : undefined} />
        ))}
      </span>
      {words && <span>{words}</span>}
    </span>
  );
}
