import { area as d3area, curveMonotoneX, line as d3line } from "d3-shape";
import { useMemo } from "react";
import { cn } from "@/lib/cn";

/**
 * A small trend beside a number: the period in ink with a faint wash, the
 * period before as a dashed gray line on the same scale. No axis: the
 * number beside it carries the value. Decorative for screen readers (the
 * figure says the same in words).
 */
export function Sparkline({
  values,
  before,
  className,
  height = 28,
  fill = true,
}: {
  values: number[];
  before?: number[];
  className?: string;
  height?: number;
  /** A wash under counts; rates and lengths are a line alone. Both start at zero, so a small change looks small. */
  fill?: boolean;
}) {
  const W = 120;
  const d = useMemo(() => {
    const all = [...values, ...(before ?? [])].filter(Number.isFinite);
    const top = Math.max(...all, 0) || 1;
    const n = Math.max(values.length, 2);
    const x = (i: number) => (i / (n - 1)) * W;
    const y = (v: number) => 2 + (1 - v / top) * (height - 4);
    const ok = (arr: number[]) => (i: number) => Number.isFinite(arr[i]);
    const idx = (arr: number[]) => arr.map((_, i) => i).slice(0, n);
    const line = (arr: number[]) =>
      d3line<number>()
        .defined(ok(arr))
        .x(x)
        .y((i) => y(arr[i]))
        .curve(curveMonotoneX)(idx(arr)) ?? "";
    const area =
      d3area<number>()
        .defined(ok(values))
        .x(x)
        .y0(height)
        .y1((i) => y(values[i]))
        .curve(curveMonotoneX)(idx(values)) ?? "";
    return { now: line(values), before: before ? line(before) : "", fill: fill ? area : "" };
  }, [values, before, height, fill]);
  if (values.length < 2) return <span aria-hidden className={cn("block", className)} style={{ height }} />;
  return (
    <svg viewBox={`0 0 ${W} ${height}`} preserveAspectRatio="none" className={cn("block w-full overflow-visible", className)} style={{ height }} aria-hidden>
      {d.before && <path d={d.before} fill="none" stroke="var(--ink-4)" strokeWidth={1.25} strokeDasharray="2 3" vectorEffect="non-scaling-stroke" />}
      {d.fill && <path d={d.fill} fill="var(--ink-2)" fillOpacity={0.08} />}
      <path d={d.now} fill="none" stroke="var(--ink-2)" strokeWidth={1.5} strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
