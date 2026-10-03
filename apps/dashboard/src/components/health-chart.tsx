import { useId, useMemo, useRef, useState, type ReactNode } from "react";
import type { Point } from "@/components/chart";
import { cn } from "@/lib/cn";
import { clock } from "@/lib/time";

export type Marker = { t: number; label: string; who?: string };

/** A round ceiling for a scale: 0.37 → 0.5, 7 → 10, 1,840 → 2,000. */
export function niceCeil(v: number) {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p + 1e-9) return m * p;
  return 10 * p;
}

const at = (t: number) => clock(new Date(t * 1000).toISOString());

/**
 * A time chart with a printed scale: three labelled gridlines (0, half, top),
 * times along the bottom, and the box's changes as brass ticks so a jump can
 * be read against what was done ("12:19 · Raise Valkey's cap"). Percent
 * charts pass max={100}; everything else gets a round ceiling over its peak.
 * One hue plus gray; the 16 % fade under the line is the only gradient.
 */
export function TimeChart({
  points,
  max,
  format,
  label,
  markers = [],
  height = 132,
  tone = "ink",
  from,
  to,
  empty = "Collecting: a point a minute.",
}: {
  points: Point[];
  max?: number;
  format: (v: number) => string;
  label: string;
  markers?: Marker[];
  height?: number;
  tone?: "ink" | "danger" | "warn";
  /** The window (unix seconds); defaults to the data's own span. */
  from?: number;
  to?: number;
  empty?: ReactNode;
}) {
  const id = useId();
  const ref = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const W = 600;
  const H = height;
  const t0 = from ?? points[0]?.[0] ?? 0;
  const t1 = to ?? points[points.length - 1]?.[0] ?? t0 + 1;
  const span = Math.max(1, t1 - t0);
  const peak = points.reduce((m, p) => Math.max(m, p[1]), 0);
  const top = max ?? niceCeil(peak * 1.1);
  const x = (t: number) => ((t - t0) / span) * W;
  const y = (v: number) => 2 + (1 - Math.min(v, top) / top) * (H - 4);

  const { line, area } = useMemo(() => {
    if (points.length < 2) return { line: "", area: "" };
    let d = `M${x(points[0][0]).toFixed(1)},${y(points[0][1]).toFixed(1)}`;
    for (let i = 1; i < points.length; i++) {
      const mx = (x(points[i - 1][0]) + x(points[i][0])) / 2;
      d += ` C${mx.toFixed(1)},${y(points[i - 1][1]).toFixed(1)} ${mx.toFixed(1)},${y(points[i][1]).toFixed(1)} ${x(points[i][0]).toFixed(1)},${y(points[i][1]).toFixed(1)}`;
    }
    const last = x(points[points.length - 1][0]).toFixed(1);
    return { line: d, area: `${d} L${last},${H} L${x(points[0][0]).toFixed(1)},${H} Z` };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [points, top, t0, span, H]);

  const color = tone === "danger" ? "var(--danger)" : tone === "warn" ? "var(--warn)" : "var(--ink-2)";
  const shown = markers.filter((m) => m.t >= t0 && m.t <= t1);
  const hp = hover !== null ? points[hover] : null;
  const near = hp ? shown.find((m) => Math.abs(x(m.t) - x(hp[0])) < 12) : undefined;

  if (points.length < 2)
    return (
      <div className="grid place-items-center rounded-[8px] border border-dashed border-rule-2 text-sm text-ink-3" style={{ height: height + 22 }}>
        {empty}
      </div>
    );

  const grid = [1, 0.5, 0];
  return (
    <figure className="m-0" aria-label={`${label}: latest ${format(points[points.length - 1][1])}, scale 0 to ${format(top)}`}>
      <div className="grid grid-cols-[2.75rem_minmax(0,1fr)] gap-x-2">
        <div className="relative" style={{ height }} aria-hidden>
          {grid.map((f) => (
            <span key={f} className="absolute right-0 -translate-y-1/2 text-[0.6875rem] leading-none text-ink-3 tnum" style={{ top: y(top * f) }}>
              {format(top * f)}
            </span>
          ))}
        </div>
        <div
          ref={ref}
          className="relative touch-none"
          style={{ height }}
          onPointerMove={(e) => {
            const r = ref.current!.getBoundingClientRect();
            const px = ((e.clientX - r.left) / r.width) * W;
            let best = 0;
            for (let i = 1; i < points.length; i++) if (Math.abs(x(points[i][0]) - px) < Math.abs(x(points[best][0]) - px)) best = i;
            setHover(best);
          }}
          onPointerLeave={() => setHover(null)}
        >
          <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="absolute inset-0 block size-full overflow-visible" role="img" aria-hidden>
            <defs>
              <linearGradient id={`g${id}`} x1="0" x2="0" y1="0" y2="1">
                <stop offset="0" stopColor={color} stopOpacity="0.16" />
                <stop offset="1" stopColor={color} stopOpacity="0" />
              </linearGradient>
            </defs>
            {grid.map((f) => (
              <line
                key={f}
                x1="0"
                x2={W}
                y1={y(top * f)}
                y2={y(top * f)}
                stroke={f === 0 ? "var(--rule-2)" : "var(--rule)"}
                strokeWidth="1"
                vectorEffect="non-scaling-stroke"
                strokeDasharray={f === 0 ? undefined : "2 4"}
              />
            ))}
            {shown.map((m, i) => (
              <line key={i} x1={x(m.t)} x2={x(m.t)} y1="0" y2={H} stroke="var(--brass)" strokeOpacity="0.7" strokeWidth="1" vectorEffect="non-scaling-stroke" strokeDasharray="3 3" />
            ))}
            <path d={area} fill={`url(#g${id})`} />
            <path d={line} fill="none" stroke={color} strokeWidth="1.6" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
            {hp && <line x1={x(hp[0])} x2={x(hp[0])} y1="0" y2={H} stroke="var(--rule-3)" strokeWidth="1" vectorEffect="non-scaling-stroke" />}
          </svg>
          {shown.map((m, i) => (
            <span
              key={i}
              aria-hidden
              title={`${at(m.t)} · ${m.label}`}
              className="absolute -top-1 size-[7px] -translate-x-1/2 rotate-45 rounded-[1px] bg-brass"
              style={{ left: `${(x(m.t) / W) * 100}%` }}
            />
          ))}
          {hp && (
            <span
              aria-hidden
              className="pointer-events-none absolute size-[7px] -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-paper"
              style={{ left: `${(x(hp[0]) / W) * 100}%`, top: `${(y(hp[1]) / H) * 100}%`, background: color }}
            />
          )}
        </div>
      </div>
      <figcaption className="mt-1.5 grid grid-cols-[2.75rem_minmax(0,1fr)] gap-x-2 text-[0.6875rem] leading-4 text-ink-3 tnum">
        <span />
        <span className="relative flex justify-between gap-3">
          <span className={cn(hp && "invisible")}>{at(t0)}</span>
          {hp ? (
            <span className="absolute inset-x-0 truncate text-center text-ink">
              {at(hp[0])} · {format(hp[1])}
              {near && <span className="text-brass-ink"> · {near.label}</span>}
            </span>
          ) : (
            <span>{at(t0 + span / 2)}</span>
          )}
          <span className={cn(hp && "invisible")}>now</span>
        </span>
      </figcaption>
    </figure>
  );
}

/**
 * Counts in buckets as bars on a printed scale (occurrences of an error).
 * Empty buckets keep a hairline so the time axis reads.
 */
export function BarStrip({
  buckets,
  from,
  to,
  label,
  height = 64,
}: {
  buckets: number[];
  from: number;
  to: number;
  label: string;
  height?: number;
}) {
  const peak = Math.max(0, ...buckets);
  const top = peak <= 1 ? 1 : niceCeil(peak);
  return (
    <figure className="m-0" aria-label={`${label}: at most ${peak} in one bucket`}>
      <div className="grid grid-cols-[2.75rem_minmax(0,1fr)] gap-x-2">
        <div className="relative" style={{ height }} aria-hidden>
          <span className="absolute top-0 right-0 -translate-y-1/2 text-[0.6875rem] leading-none text-ink-3 tnum">{top}</span>
          <span className="absolute right-0 bottom-0 translate-y-1/2 text-[0.6875rem] leading-none text-ink-3 tnum">0</span>
        </div>
        <div className="relative flex items-end gap-[2px] border-b border-rule-2" style={{ height }}>
          <span aria-hidden className="absolute inset-x-0 top-0 border-t border-dashed border-rule" />
          {buckets.map((n, i) => (
            <span
              key={i}
              className={cn("min-w-0 flex-1 rounded-t-[1.5px]", n > 0 ? "bg-ink-2" : "bg-transparent")}
              style={{ height: n > 0 ? `${Math.max(6, (n / top) * 100)}%` : 0 }}
              title={n > 0 ? `${n} between ${at(from + ((to - from) / buckets.length) * i)} and ${at(from + ((to - from) / buckets.length) * (i + 1))}` : undefined}
            />
          ))}
        </div>
      </div>
      <figcaption className="mt-1.5 grid grid-cols-[2.75rem_minmax(0,1fr)] gap-x-2 text-[0.6875rem] leading-4 text-ink-3 tnum">
        <span />
        <span className="flex justify-between">
          <span>{at(from)}</span>
          <span>{at(from + (to - from) / 2)}</span>
          <span>now</span>
        </span>
      </figcaption>
    </figure>
  );
}
