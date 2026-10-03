import type { CSSProperties } from "react";
import { cn } from "@/lib/cn";

/**
 * A segmented meter on a printed scale, for vitals and budgets. Neutral
 * segments until a labelled threshold (`warnAt`, amber), red at `fullAt`.
 * Percent meters always run 0–100. `add` lights extra segments in brass for
 * a staged change ("+148 MB"); a negative `add` outlines the ones it frees.
 *
 *   <SegMeter label="CPU" value={9} scale />            20 segments, 0 · 50 · 100 printed
 *   <SegMeter label="share" value={296} max={512} segments={16} size="row" />
 *
 * Live values update without animation (they can change every second).
 */
export function SegMeter({
  value,
  max = 100,
  segments = 20,
  warnAt,
  fullAt,
  add = 0,
  scale,
  label,
  valueText,
  size = "vital",
  className,
}: {
  value: number;
  max?: number;
  segments?: number;
  /** Share of max (0–1) where segments turn amber. */
  warnAt?: number;
  /** Share of max (0–1) where segments turn red. */
  fullAt?: number;
  /** A staged change in the same units as value (positive adds, negative frees). */
  add?: number;
  /** Print the 0 · 50 · 100 scale under it (or your own three labels). */
  scale?: boolean | [string, string, string];
  label: string;
  valueText?: string;
  size?: "vital" | "row";
  className?: string;
}) {
  const per = max / segments;
  const lit = value > 0 ? Math.max(1, Math.round(value / per)) : 0;
  const after = Math.max(0, Math.min(segments, Math.round((value + add) / per)));
  const cells = Array.from({ length: segments }, (_, i) => {
    const share = (i + 1) / segments;
    const on = i < Math.min(lit, after);
    const isAdd = add > 0 && i >= lit && i < after;
    const isSub = add < 0 && i >= after && i < lit;
    const flag: Record<string, string> = {};
    if (on || isSub) {
      if (fullAt !== undefined && share > fullAt) flag["data-full"] = "";
      else if (warnAt !== undefined && share > warnAt) flag["data-warn"] = "";
      else flag["data-on"] = "";
    }
    if (isAdd) flag["data-add"] = "";
    if (isSub) flag["data-sub"] = "";
    return <i key={i} {...flag} />;
  });
  const labels = Array.isArray(scale) ? scale : ["0", "50", "100"];
  return (
    <div className={cn("min-w-0", className)}>
      <div
        className="seg"
        data-size={size}
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={max}
        aria-valuenow={Math.round(value)}
        aria-valuetext={valueText}
        style={{ "--n": segments } as CSSProperties}
      >
        {cells}
      </div>
      {scale && (
        <div className="seg-scale" aria-hidden>
          {labels.map((l) => (
            <span key={l}>{l}</span>
          ))}
        </div>
      )}
    </div>
  );
}
