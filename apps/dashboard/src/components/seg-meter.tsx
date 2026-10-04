import { cn } from "@/lib/cn";

/**
 * A plain rounded progress bar, for usage and budgets: neutral until a
 * threshold (`warnAt`, amber), red at `fullAt`. `add` extends it in brass
 * for a change on its way; a negative `add` shows the part it frees as an
 * outline. Put the value as text next to it; the bar is the picture.
 *
 *   <SegMeter label="CPU" value={9} />                         0–100
 *   <SegMeter label="Memory" value={296} max={512} add={128} />
 *
 * (The name is from the segmented meter it replaced; `segments`, `scale`
 * and `size` are accepted and ignored, so every caller changed at once.)
 * Live values update without animation (they can change every second).
 */
export function SegMeter({
  value,
  max = 100,
  warnAt,
  fullAt,
  add = 0,
  label,
  valueText,
  size = "vital",
  className,
}: {
  value: number;
  max?: number;
  segments?: number;
  /** Share of max (0–1) where the bar turns amber. */
  warnAt?: number;
  /** Share of max (0–1) where the bar turns red. */
  fullAt?: number;
  /** A change on its way, in the same units as value (positive adds, negative frees). */
  add?: number;
  scale?: boolean | [string, string, string];
  label: string;
  valueText?: string;
  size?: "vital" | "row";
  className?: string;
}) {
  const pct = (v: number) => Math.max(0, Math.min(100, (v / max) * 100));
  const share = value / max;
  const after = value + add;
  const tone = fullAt !== undefined && share >= fullAt ? "bg-danger" : warnAt !== undefined && share >= warnAt ? "bg-warn" : "bg-ink-3";
  return (
    <div
      className={cn("relative overflow-hidden rounded-full bg-paper-sunk", size === "row" ? "h-1.5" : "h-2", className)}
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={Math.round(value)}
      aria-valuetext={valueText}
    >
      {add > 0 && <span className="absolute inset-y-0 left-0 rounded-full bg-brass" style={{ width: `${pct(after)}%` }} />}
      <span className={cn("absolute inset-y-0 left-0 rounded-full", tone, value > 0 && "min-w-[3px]")} style={{ width: `${pct(add < 0 ? after : value)}%` }} />
      {add < 0 && (
        <span
          className="absolute inset-y-0 rounded-r-full border border-l-0 border-dashed border-ink-4"
          style={{ left: `${pct(after)}%`, width: `${pct(value) - pct(after)}%` }}
        />
      )}
    </div>
  );
}
