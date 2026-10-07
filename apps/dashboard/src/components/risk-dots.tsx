import type { Tier } from "@/api/client";
import { cn } from "@/lib/cn";

const words: Record<Tier, string> = { read: "only reads", reversible: "can be undone", outbound: "reaches outside the box", irreversible: "can’t be undone" };

/**
 * What a change risks, in words: "can be undone", "reaches outside the box",
 * "can’t be undone" (in red, the only one with colour). Used on the deeper
 * History pages; primary screens say it in their own sentences.
 *
 *   <RiskDots tier="irreversible" />     can’t be undone
 *   <RiskDots tier="outbound" label={false} />   announced, not shown
 */
export function RiskDots({ tier, label, className }: { tier: Tier; label?: string | false; className?: string }) {
  const text = label === "Low" ? words.reversible : (label ?? words[tier]);
  if (label === false) return <span className="sr-only">{words[tier]}</span>;
  return (
    <span className={cn("[:where(&)]:text-[0.8125rem]", tier === "irreversible" ? "text-danger" : tier === "outbound" ? "text-warn-ink" : "text-ink-3", className)}>{text}</span>
  );
}
