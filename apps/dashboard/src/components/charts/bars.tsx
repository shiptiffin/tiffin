import { useId, useMemo, useState, type KeyboardEvent } from "react";
import { cn } from "@/lib/cn";
import { niceScale, stepLabel, tickLabel, timeTicks, useWidth } from "./core";
import { Readout } from "./time-series";

/**
 * Counts per step, stacked: columns at most 24 px wide from one baseline,
 * a 2 px gap of paper between segments, the top one rounded. The whole
 * column slot is the hover target; arrow keys move between columns.
 */

export type BarKey = { id: string; label: string; color: string };
export type Bucket = { t: number; values: Record<string, number> };

const PAD = { top: 10, right: 4, bottom: 22, left: 46 };

export function StackedBars({
  buckets,
  keys,
  step,
  label,
  format,
  height = 180,
  utc = false,
  className,
}: {
  buckets: Bucket[];
  keys: BarKey[];
  step: number;
  label: string;
  format: (v: number) => string;
  height?: number;
  utc?: boolean;
  className?: string;
}) {
  const [wrap, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const live = useId();
  const n = buckets.length;
  const geo = useMemo(() => {
    const W = Math.max(0, width - PAD.left - PAD.right);
    const H = height - PAD.top - PAD.bottom;
    if (!n || W <= 0) return null;
    const totals = buckets.map((b) => keys.reduce((s, k) => s + (b.values[k.id] ?? 0), 0));
    const { top, ticks } = niceScale(Math.max(...totals, 1), 2);
    const slot = W / n;
    const bw = Math.max(1, Math.min(24, slot * 0.64));
    const x = (i: number) => PAD.left + i * slot + (slot - bw) / 2;
    const y = (v: number) => PAD.top + H - (v / top) * H;
    const t0 = buckets[0].t;
    const tt = timeTicks(t0, buckets[n - 1].t, Math.max(2, Math.floor(W / 90)), utc);
    return { W, H, slot, bw, x, y, top, ticks, totals, tt, t0 };
  }, [width, height, n, buckets, keys, utc]);

  const key = (e: KeyboardEvent) => {
    const go = (i: number) => {
      e.preventDefault();
      setHover(Math.max(0, Math.min(n - 1, i)));
    };
    if (e.key === "ArrowRight") go((hover ?? -1) + 1);
    else if (e.key === "ArrowLeft") go((hover ?? n) - 1);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(n - 1);
    else if (e.key === "Escape") setHover(null);
  };
  const h = hover !== null && hover < n ? buckets[hover] : null;
  const words = h ? `${stepLabel(h.t, step, utc)}: ${keys.map((k) => `${k.label} ${format(h.values[k.id] ?? 0)}`).join(", ")}` : "";

  return (
    <div ref={wrap} className={cn("relative select-none", className)} style={{ height }}>
      {geo && (
        <div
          role="group"
          tabIndex={0}
          aria-label={`${label}. Use the left and right arrow keys to read each column.`}
          aria-describedby={live}
          onKeyDown={key}
          onBlur={() => setHover(null)}
          onPointerMove={(e) => {
            const r = e.currentTarget.getBoundingClientRect();
            const i = Math.floor((e.clientX - r.left - PAD.left) / geo.slot);
            setHover(i >= 0 && i < n ? i : null);
          }}
          onPointerLeave={(e) => e.pointerType === "mouse" && setHover(null)}
          className="absolute inset-0 touch-pan-y rounded-[6px] focus-visible:outline-offset-4"
        >
          <svg width={width} height={height} className="block" aria-hidden>
            {geo.ticks.map((v) => (
              <g key={v}>
                <line x1={PAD.left} x2={PAD.left + geo.W} y1={geo.y(v)} y2={geo.y(v)} stroke={v === 0 ? "var(--rule-2)" : "var(--rule)"} shapeRendering="crispEdges" />
                <text x={PAD.left - 8} y={geo.y(v)} dy="0.32em" textAnchor="end" className="fill-ink-3 text-[0.6875rem] tnum">
                  {format(v)}
                </text>
              </g>
            ))}
            {geo.tt.map((t) => {
              const i = Math.round((t - geo.t0) / step);
              if (i < 0 || i >= n) return null;
              return (
                <text key={t} x={geo.x(i) + geo.bw / 2} y={height - 6} textAnchor="middle" className="fill-ink-3 text-[0.6875rem] tnum">
                  {tickLabel(t, buckets[n - 1].t - geo.t0, utc)}
                </text>
              );
            })}
            {hover !== null && <rect x={PAD.left + hover * geo.slot} y={PAD.top} width={geo.slot} height={geo.H} fill="var(--ink)" fillOpacity={0.04} />}
            {buckets.map((b, i) => {
              let base = 0;
              const segs = keys.filter((k) => (b.values[k.id] ?? 0) > 0);
              return segs.map((k, j) => {
                const v = b.values[k.id];
                const y0 = geo.y(base);
                base += v;
                const y1 = geo.y(base);
                const gap = j > 0 ? 2 : 0;
                const hgt = Math.max(1, y0 - y1 - gap);
                const topSeg = j === segs.length - 1;
                const r = topSeg ? Math.min(4, geo.bw / 2, hgt) : 0;
                const x = geo.x(i);
                const yTop = y0 - gap - hgt;
                // Rounded at the data end, square at the baseline.
                const d = `M${x},${y0 - gap}V${yTop + r}Q${x},${yTop} ${x + r},${yTop}H${x + geo.bw - r}Q${x + geo.bw},${yTop} ${x + geo.bw},${yTop + r}V${y0 - gap}Z`;
                return <path key={k.id} d={d} fill={k.color} />;
              });
            })}
          </svg>
          {h && hover !== null && (
            <Readout x={geo.x(hover) + geo.bw / 2} width={width}>
              <p className="text-[0.75rem] text-ink-3">{stepLabel(h.t, step, utc)}</p>
              <dl className="mt-1 grid grid-cols-[auto_1fr_auto] items-center gap-x-2 gap-y-0.5">
                {keys.map((k) => (
                  <div key={k.id} className="contents">
                    <span aria-hidden className="size-2 rounded-[2px]" style={{ background: k.color }} />
                    <dt className="text-[0.75rem] text-ink-3">{k.label}</dt>
                    <dd className="text-right text-[0.8125rem] font-[550] text-ink tnum">{format(h.values[k.id] ?? 0)}</dd>
                  </div>
                ))}
              </dl>
            </Readout>
          )}
        </div>
      )}
      <p id={live} className="sr-only" aria-live="polite">
        {words}
      </p>
    </div>
  );
}

/** A legend: a swatch shaped like the mark, then the name. */
export function Legend({ items, className }: { items: Array<{ label: string; color: string; line?: boolean; dashed?: boolean }>; className?: string }) {
  return (
    <ul className={cn("flex flex-wrap items-center gap-x-4 gap-y-1 text-[0.75rem] text-ink-3", className)}>
      {items.map((it) => (
        <li key={it.label} className="inline-flex items-center gap-1.5">
          {it.line ? (
            <span aria-hidden className={cn("w-3.5 border-t-2", it.dashed && "border-dashed")} style={{ borderColor: it.color }} />
          ) : (
            <span aria-hidden className="size-2.5 rounded-[3px]" style={{ background: it.color }} />
          )}
          {it.label}
        </li>
      ))}
    </ul>
  );
}
