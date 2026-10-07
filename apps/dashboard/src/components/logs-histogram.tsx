import { useId, useMemo, useState, type KeyboardEvent, type PointerEvent } from "react";
import { stepLabel, tickLabel, timeTicks, useWidth } from "@/components/charts/core";
import { Readout } from "@/components/charts/time-series";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";

import type { VolumeBucket } from "@/components/logs-query";

const AXIS = 18;

/**
 * Volume over time above the lines: one column per step, errors in red at
 * the base, warnings in amber, the rest in steel. Click a column to zoom to
 * it; drag across columns to zoom to a stretch. Arrow keys read columns and
 * Enter zooms.
 */
export function LogsHistogram({
  buckets,
  step,
  noun,
  onZoom,
  loading,
  height = 76,
  className,
}: {
  buckets: VolumeBucket[];
  step: number;
  noun: [string, string];
  onZoom?: (from: number, to: number) => void;
  loading?: boolean;
  height?: number;
  className?: string;
}) {
  const [wrap, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const [drag, setDrag] = useState<{ a: number; b: number } | null>(null);
  const live = useId();
  const n = buckets.length;
  const H = height - AXIS;
  const geo = useMemo(() => {
    if (!n || width <= 0) return null;
    const max = Math.max(1, ...buckets.map((b) => b.error + b.warn + b.other));
    const slot = width / n;
    const gap = slot > 6 ? Math.min(2, slot * 0.18) : slot > 3 ? 1 : 0;
    const t0 = buckets[0].t;
    const t1 = buckets[n - 1].t + step;
    const ticks = timeTicks(t0, t1, Math.max(2, Math.floor(width / 110)), false);
    return { max, slot, gap, t0, t1, ticks };
  }, [buckets, n, width, step]);

  const at = (e: PointerEvent<HTMLDivElement>) => {
    if (!geo) return null;
    const r = e.currentTarget.getBoundingClientRect();
    const i = Math.floor((e.clientX - r.left) / geo.slot);
    return i >= 0 && i < n ? i : null;
  };
  const zoom = (a: number, b: number) => {
    const lo = Math.min(a, b);
    const hi = Math.max(a, b);
    onZoom?.(buckets[lo].t, buckets[hi].t + step);
  };
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
    else if (e.key === "Enter" && hover !== null && onZoom) {
      e.preventDefault();
      zoom(hover, hover);
    }
  };

  const h = hover !== null && hover < n ? buckets[hover] : null;
  const total = (b: VolumeBucket) => b.error + b.warn + b.other;
  const words = h ? `${stepLabel(h.t, step, false)}: ${int(total(h))} ${total(h) === 1 ? noun[0] : noun[1]}, ${int(h.error)} errors, ${int(h.warn)} warnings` : "";
  const sel = drag ? [Math.min(drag.a, drag.b), Math.max(drag.a, drag.b)] : null;

  return (
    <div ref={wrap} className={cn("relative select-none", className)} style={{ height }}>
      {loading && !n && (
        <div className="absolute inset-x-0 top-0 flex items-end gap-[2px]" style={{ height: H }} aria-hidden>
          {Array.from({ length: 48 }, (_, i) => (
            <span key={i} className="flex-1 animate-pulse rounded-t-[2px] bg-paper-hover" style={{ height: `${18 + ((i * 37) % 50)}%` }} />
          ))}
        </div>
      )}
      {geo && (
        <div
          role="group"
          tabIndex={0}
          aria-label={`${noun[1][0].toUpperCase()}${noun[1].slice(1)} over time.${onZoom ? " Arrow keys read each column; Enter zooms to it." : ""}`}
          aria-describedby={live}
          onKeyDown={key}
          onBlur={() => setHover(null)}
          onPointerDown={(e) => {
            const i = at(e);
            if (i === null || !onZoom || e.button !== 0) return;
            e.currentTarget.setPointerCapture(e.pointerId);
            setDrag({ a: i, b: i });
          }}
          onPointerMove={(e) => {
            const i = at(e);
            setHover(i);
            if (drag && i !== null) setDrag({ ...drag, b: i });
          }}
          onPointerUp={() => {
            if (drag) zoom(drag.a, drag.b);
            setDrag(null);
          }}
          onPointerCancel={() => setDrag(null)}
          onPointerLeave={(e) => e.pointerType === "mouse" && !drag && setHover(null)}
          className={cn("absolute inset-0 touch-pan-y rounded-[4px] focus-visible:outline-offset-4", onZoom && "cursor-zoom-in")}
        >
          <svg width={width} height={height} className="block overflow-visible" aria-hidden>
            <line x1={0} x2={width} y1={H + 0.5} y2={H + 0.5} stroke="var(--rule-2)" shapeRendering="crispEdges" />
            {sel && <rect x={sel[0] * geo.slot} y={0} width={(sel[1] - sel[0] + 1) * geo.slot} height={H} fill="var(--brass-wash)" />}
            {!sel && hover !== null && <rect x={hover * geo.slot} y={0} width={geo.slot} height={H} fill="var(--ink)" fillOpacity={0.05} />}
            {buckets.map((b, i) => {
              const x = i * geo.slot + geo.gap / 2;
              const w = Math.max(1, geo.slot - geo.gap);
              let y = H;
              return (["error", "warn", "other"] as const).map((k) => {
                const v = b[k];
                if (!v) return null;
                const hgt = Math.max(1.5, (v / geo.max) * (H - 4));
                y -= hgt;
                return (
                  <rect
                    key={k}
                    x={x}
                    y={y}
                    width={w}
                    height={hgt}
                    fill={k === "error" ? "var(--danger)" : k === "warn" ? "var(--warn)" : "var(--ink-4)"}
                    fillOpacity={(k === "other" ? 0.62 : 1) * (hover === null || hover === i || (sel && i >= sel[0] && i <= sel[1]) ? 1 : 0.7)}
                    shapeRendering="crispEdges"
                  />
                );
              });
            })}
            {geo.ticks.map((t) => {
              const x = ((t - geo.t0) / (geo.t1 - geo.t0)) * width;
              if (x < 18 || x > width - 18) return null;
              return (
                <g key={t}>
                  <line x1={x} x2={x} y1={H} y2={H + 3} stroke="var(--rule-2)" />
                  <text x={x} y={height - 3} textAnchor="middle" className="fill-ink-4 text-[0.6875rem] tnum">
                    {tickLabel(t, geo.t1 - geo.t0, false)}
                  </text>
                </g>
              );
            })}
          </svg>
          {h && hover !== null && !drag && (
            <Readout x={hover * geo.slot + geo.slot / 2} width={width}>
              <p className="text-[0.75rem] text-ink-3">{stepLabel(h.t, step, false)}</p>
              <p className="mt-0.5 text-[0.8125rem] font-[550] text-ink tnum">
                {int(total(h))} {total(h) === 1 ? noun[0] : noun[1]}
              </p>
              {(h.error > 0 || h.warn > 0) && (
                <p className="mt-0.5 flex gap-3 text-[0.75rem] tnum">
                  {h.error > 0 && <span className="text-danger">{int(h.error)} error{h.error === 1 ? "" : "s"}</span>}
                  {h.warn > 0 && <span className="text-warn-ink">{int(h.warn)} warning{h.warn === 1 ? "" : "s"}</span>}
                </p>
              )}
              {onZoom && <p className="mt-1 text-[0.6875rem] text-ink-4">Click to zoom in</p>}
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

