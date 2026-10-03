import { useId, useMemo, useRef, useState } from "react";
import { cn } from "@/lib/cn";

export type Point = [number, number]; // [unix seconds, value]

/** Normalises the API's [[ts, value]] arrays (values may arrive as strings). */
export function toPoints(series: unknown): Point[] {
  if (!Array.isArray(series)) return [];
  return series
    .map((p) => (Array.isArray(p) ? ([Number(p[0]), Number(p[1])] as Point) : null))
    .filter((p): p is Point => !!p && Number.isFinite(p[0]) && Number.isFinite(p[1]));
}

const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });

/**
 * A small area chart: one hue plus gray, hairline grid, a crosshair on hover.
 * No library: the whole chart is one SVG path.
 */
export function AreaChart({
  points,
  format = (v) => v.toFixed(1),
  height = 120,
  max,
  tone = "ink",
  className,
  label,
}: {
  points: Point[];
  format?: (v: number) => string;
  height?: number;
  max?: number;
  tone?: "ink" | "rev" | "out" | "irr";
  className?: string;
  label: string;
}) {
  const id = useId();
  const ref = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const W = 600;
  const H = height;
  const pad = { t: 8, b: 4 };

  const { path, area, xs, ys, top } = useMemo(() => {
    if (points.length === 0) return { path: "", area: "", xs: [], ys: [], top: 1 };
    const t0 = points[0][0];
    const t1 = points[points.length - 1][0] || t0 + 1;
    const peak = Math.max(...points.map((p) => p[1]));
    const top = max ?? (peak <= 0 ? 1 : niceCeil(peak * 1.15));
    const xs = points.map((p) => (t1 === t0 ? W : ((p[0] - t0) / (t1 - t0)) * W));
    const ys = points.map((p) => pad.t + (1 - Math.min(p[1], top) / top) * (H - pad.t - pad.b));
    let d = `M${xs[0].toFixed(1)},${ys[0].toFixed(1)}`;
    for (let i = 1; i < xs.length; i++) {
      // gentle monotone-ish curve: midpoint control points
      const mx = (xs[i - 1] + xs[i]) / 2;
      d += ` C${mx.toFixed(1)},${ys[i - 1].toFixed(1)} ${mx.toFixed(1)},${ys[i].toFixed(1)} ${xs[i].toFixed(1)},${ys[i].toFixed(1)}`;
    }
    return { path: d, area: `${d} L${xs[xs.length - 1].toFixed(1)},${H} L${xs[0].toFixed(1)},${H} Z`, xs, ys, top };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [points, H, max]);

  const color = { ink: "var(--ink-2)", rev: "var(--rev)", out: "var(--out)", irr: "var(--irr)" }[tone];
  const h = hover !== null ? points[hover] : points[points.length - 1];

  if (points.length < 2)
    return (
      <div className={cn("grid place-items-center rounded-lg border border-dashed border-rule text-sm text-ink-4", className)} style={{ height }}>
        Collecting… a point a minute
      </div>
    );

  return (
    <figure className={cn("relative", className)} aria-label={`${label}: latest ${format(points[points.length - 1][1])}`}>
      <svg
        ref={ref}
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        className="block w-full touch-none"
        style={{ height }}
        onPointerMove={(e) => {
          const r = ref.current!.getBoundingClientRect();
          const x = ((e.clientX - r.left) / r.width) * W;
          let best = 0;
          for (let i = 1; i < xs.length; i++) if (Math.abs(xs[i] - x) < Math.abs(xs[best] - x)) best = i;
          setHover(best);
        }}
        onPointerLeave={() => setHover(null)}
        role="img"
      >
        <defs>
          <linearGradient id={`f${id}`} x1="0" x2="0" y1="0" y2="1">
            <stop offset="0" stopColor={color} stopOpacity="0.16" />
            <stop offset="1" stopColor={color} stopOpacity="0" />
          </linearGradient>
        </defs>
        {[0.25, 0.5, 0.75].map((f) => (
          <line
            key={f}
            x1="0"
            x2={W}
            y1={pad.t + f * (H - pad.t - pad.b)}
            y2={pad.t + f * (H - pad.t - pad.b)}
            stroke="var(--rule)"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
            strokeDasharray="2 4"
          />
        ))}
        <path d={area} fill={`url(#f${id})`} />
        <path d={path} fill="none" stroke={color} strokeWidth="1.75" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
        {hover !== null && (
          <>
            <line x1={xs[hover]} x2={xs[hover]} y1="0" y2={H} stroke="var(--rule-strong)" strokeWidth="1" vectorEffect="non-scaling-stroke" />
          </>
        )}
      </svg>
      {hover !== null && (
        <span
          aria-hidden
          className="pointer-events-none absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-paper"
          style={{ left: `${(xs[hover] / W) * 100}%`, top: ys[hover], background: color }}
        />
      )}
      <figcaption className="mt-1.5 flex items-center justify-between font-mono text-[0.6875rem] text-ink-4 tnum">
        <span>{timeFmt.format(new Date(points[0][0] * 1000))}</span>
        <span className={cn("transition-opacity", hover === null && "opacity-0")}>
          {h && `${timeFmt.format(new Date(h[0] * 1000))} · ${format(h[1])}`}
        </span>
        <span>
          {hover === null ? "now" : ""} <span className="text-ink-4/70">max {format(top)}</span>
        </span>
      </figcaption>
    </figure>
  );
}

/** A tiny line, for table rows. */
export function Sparkline({ points, className, tone = "ink" }: { points: Point[]; className?: string; tone?: "ink" | "irr" }) {
  if (points.length < 2) return <span className={cn("inline-block h-5 w-20", className)} />;
  const vals = points.map((p) => p[1]);
  const lo = Math.min(...vals);
  const hi = Math.max(...vals);
  const d = vals
    .map((v, i) => `${i === 0 ? "M" : "L"}${((i / (vals.length - 1)) * 80).toFixed(1)},${(18 - ((v - lo) / (hi - lo || 1)) * 16).toFixed(1)}`)
    .join(" ");
  return (
    <svg viewBox="0 0 80 20" className={cn("inline-block h-5 w-20", className)} aria-hidden>
      <path d={d} fill="none" stroke={tone === "irr" ? "var(--irr)" : "var(--ink-3)"} strokeWidth="1.25" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

function niceCeil(v: number) {
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

/** A thin usage bar; turns amber past 80 % and red past 95 %. */
export function Meter({ ratio, className, label }: { ratio: number; className?: string; label?: string }) {
  const r = Math.max(0, Math.min(1, ratio));
  return (
    <div
      className={cn("h-1.5 w-full overflow-hidden rounded-full bg-hover", className)}
      role="meter"
      aria-valuenow={Math.round(r * 100)}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={label}
    >
      <div
        className={cn("h-full rounded-full transition-[width] duration-500 ease-out", r > 0.95 ? "bg-irr" : r > 0.8 ? "bg-out" : "bg-ink-2")}
        style={{ width: `${Math.max(r * 100, r > 0 ? 1.5 : 0)}%` }}
      />
    </div>
  );
}
