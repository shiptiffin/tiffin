import { area as d3area, curveMonotoneX, line as d3line } from "d3-shape";
import { createContext, useContext, useId, useMemo, useState, type KeyboardEvent, type ReactNode } from "react";
import { nearest, niceScale, stepLabel, tickLabel, timeTicks, useWidth, type XY } from "@/components/charts/core";
import { Readout } from "@/components/charts/time-series";
import { cn } from "@/lib/cn";

/**
 * The Observability page's charts: the dashboard's TimeSeries look (ink
 * hairlines on paper, gridlines on the axis's own ticks, a readout card) plus
 * what a page of charts needs: one crosshair shared by every chart in a
 * <Crosshair> (hover one, read the same moment on all), bands stacked by
 * status code or by app, deploys as markers named in the readout, and an
 * empty frame (axis and time, a sentence) instead of a blank box.
 */

export type ChartSeries = {
  id: string;
  label: string;
  points: XY[];
  /** A CSS colour: a token, e.g. var(--part-2). Defaults by position. */
  color?: string;
  /** A wash under the line (unstacked charts). */
  area?: boolean;
  dashed?: boolean;
};

export type ChartMarker = { t: number; label: string; failed?: boolean };

/** Line colours by position: ink first, then the neutral part shades. */
export const LINE_COLORS = ["var(--ink-2)", "var(--part-3)", "var(--ink-4)"];
/** Band colours by position, darkest at the bottom; the same shades as the box bar. */
export const BAND_COLORS = ["var(--part-1)", "var(--part-2)", "var(--part-3)", "var(--part-4)", "var(--rule-3)"];

type Shared = { t: number | null; src: string | null; set: (t: number | null, src: string | null) => void };
const Ctx = createContext<Shared | null>(null);

/** Charts inside share one crosshair: the moment under the pointer on one shows on all. */
export function Crosshair({ children }: { children: ReactNode }) {
  const [s, setS] = useState<{ t: number | null; src: string | null }>({ t: null, src: null });
  const value = useMemo<Shared>(() => ({ ...s, set: (t, src) => setS((o) => (o.t === t && o.src === src ? o : { t, src })) }), [s]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

/** The shared moment (ms), when the pointer is over a chart. */
export function useCrosshairTime(): number | null {
  return useContext(Ctx)?.t ?? null;
}

const PAD = { top: 12, right: 6, bottom: 22, left: 48 };

export function ObserveChart({
  series,
  step,
  format,
  axisFormat = format,
  label,
  height = 168,
  stacked,
  limit,
  markers = [],
  minTop = 0,
  from,
  to,
  empty = "No readings yet.",
  className,
}: {
  series: ChartSeries[];
  /** Milliseconds per step. */
  step: number;
  format: (v: number) => string;
  axisFormat?: (v: number) => string;
  /** Accessible name: what the chart shows. */
  label: string;
  height?: number;
  /** Bands stacked bottom-up in series order; the readout adds a total. */
  stacked?: boolean;
  limit?: { value: number; label: string };
  markers?: ChartMarker[];
  minTop?: number;
  /** The window shown (ms), so every chart on a page lines up and an empty one still has its time axis. */
  from?: number;
  to?: number;
  /** What an empty chart says. */
  empty?: ReactNode;
  className?: string;
}) {
  const [wrap, width] = useWidth<HTMLDivElement>();
  const id = useId();
  const live = useId();
  const shared = useContext(Ctx);
  const [own, setOwn] = useState<number | null>(null);
  const hoverT = shared ? shared.t : own;
  const setHover = (t: number | null) => (shared ? shared.set(t, t === null ? null : id) : setOwn(t));
  const mine = shared ? shared.src === id : true;

  // Every series on one set of times (the union), so bands stack and the readout reads across.
  const { xs, cols } = useMemo(() => {
    const all = new Set<number>();
    for (const s of series) for (const p of s.points) all.add(p[0]);
    const xs = [...all].sort((a, b) => a - b);
    const cols = series.map((s) => {
      const by = new Map(s.points.map((p) => [p[0], p[1]]));
      return xs.map((t) => by.get(t) ?? (stacked ? 0 : NaN));
    });
    return { xs, cols };
  }, [series, stacked]);
  const n = xs.length;
  const has = n > 0 && cols.some((c) => c.some((v) => Number.isFinite(v)));

  const geo = useMemo(() => {
    const W = Math.max(0, width - PAD.left - PAD.right);
    const H = height - PAD.top - PAD.bottom;
    if (W <= 0) return null;
    const t0 = from ?? xs[0] ?? 0;
    const t1 = to ?? (n > 1 ? xs[n - 1] : t0 + step);
    // Stacked: each band sits on the ones below.
    const base = cols.map(() => new Array<number>(n).fill(0));
    const topv = cols.map(() => new Array<number>(n).fill(0));
    for (let i = 0; i < n; i++) {
      let acc = 0;
      cols.forEach((c, k) => {
        base[k][i] = acc;
        acc += stacked ? Math.max(0, c[i] || 0) : 0;
        topv[k][i] = stacked ? acc : c[i];
      });
    }
    let peak = minTop;
    for (const c of topv) for (const v of c) if (Number.isFinite(v) && v > peak) peak = v;
    if (limit && limit.value > peak) peak = limit.value;
    const { top, ticks } = niceScale(has ? peak : 0, height < 150 ? 2 : 3);
    const x = (t: number) => PAD.left + (t1 === t0 ? W / 2 : ((t - t0) / (t1 - t0)) * W);
    const y = (v: number) => PAD.top + H - (Math.max(0, v) / top) * H;
    const idx = xs.map((_, i) => i);
    const ok = (k: number) => (i: number) => Number.isFinite(topv[k][i]) && (i === 0 || xs[i] - xs[i - 1] <= step * 1.5 || stacked === true);
    const paths = series.map((s, k) => {
      const ln = d3line<number>()
        .defined(ok(k))
        .x((i) => x(xs[i]))
        .y((i) => y(topv[k][i]))
        .curve(curveMonotoneX)(idx);
      const fill =
        stacked || s.area
          ? d3area<number>()
              .defined(ok(k))
              .x((i) => x(xs[i]))
              .y0((i) => (stacked ? y(base[k][i]) : PAD.top + H))
              .y1((i) => y(topv[k][i]))
              .curve(curveMonotoneX)(idx)
          : null;
      return { s, k, line: ln ?? "", fill: fill ?? "" };
    });
    const tt = timeTicks(t0, t1, Math.max(2, Math.floor(W / 90)), false);
    const lw = n > W / 3 ? 1.25 : 2;
    return { W, H, t0, t1, top, ticks, x, y, paths, tt, lw, topv };
  }, [width, height, n, xs, cols, series, stacked, step, limit, minTop, from, to, has]);

  const color = (s: ChartSeries, k: number) => s.color ?? (stacked ? BAND_COLORS[k % BAND_COLORS.length] : LINE_COLORS[k % LINE_COLORS.length]);
  const h = hoverT !== null && has ? nearest(xs, hoverT) : -1;
  const hi = h >= 0 && Math.abs(xs[h] - (hoverT ?? 0)) <= step * 1.5 ? h : -1;
  const placed = geo ? markers.filter((m) => m.t >= geo.t0 && m.t <= geo.t1 + step) : [];
  const near = hi >= 0 ? placed.filter((m) => Math.abs(m.t - xs[hi]) <= step) : [];

  const move = (clientX: number, rect: DOMRect) => {
    if (!geo || !has) return;
    const t = geo.t0 + ((clientX - rect.left - PAD.left) / geo.W) * (geo.t1 - geo.t0);
    const i = nearest(xs, t);
    if (i >= 0 && (xs[i] !== hoverT || !mine)) setHover(xs[i]);
  };
  const key = (e: KeyboardEvent) => {
    if (!n) return;
    const go = (i: number) => {
      e.preventDefault();
      setHover(xs[Math.max(0, Math.min(n - 1, i))]);
    };
    if (e.key === "ArrowRight") go((hi < 0 ? n - 2 : hi) + 1);
    else if (e.key === "ArrowLeft") go((hi < 0 ? n : hi) - 1);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(n - 1);
    else if (e.key === "Escape") setHover(null);
  };

  const rows = hi < 0 ? [] : series.map((s, k) => ({ s, k, v: cols[k][hi] })).filter((r) => Number.isFinite(r.v));
  const total = stacked ? rows.reduce((t, r) => t + r.v, 0) : undefined;
  const text = hi < 0 ? "" : `${stepLabel(xs[hi], step, false)}: ${rows.map((r) => `${r.s.label} ${format(r.v)}`).join(", ")}${near.length ? `. ${near.map((m) => m.label).join(". ")}` : ""}`;

  return (
    <div ref={wrap} className={cn("relative select-none", className)} style={{ height }}>
      {geo && (
        <div
          role="group"
          tabIndex={has ? 0 : -1}
          aria-label={has ? `${label}. Use the left and right arrow keys to read each point.` : label}
          aria-describedby={live}
          onKeyDown={key}
          onBlur={() => setHover(null)}
          onPointerMove={(e) => move(e.clientX, e.currentTarget.getBoundingClientRect())}
          onPointerDown={(e) => move(e.clientX, e.currentTarget.getBoundingClientRect())}
          onPointerLeave={(e) => e.pointerType === "mouse" && setHover(null)}
          className="absolute inset-0 touch-pan-y rounded-[6px] focus-visible:outline-offset-4"
        >
          <svg width={width} height={height} className="block overflow-visible" aria-hidden>
            {(has ? geo.ticks : [0]).map((v) => (
              <g key={v}>
                <line x1={PAD.left} x2={PAD.left + geo.W} y1={geo.y(v)} y2={geo.y(v)} stroke={v === 0 ? "var(--rule-2)" : "var(--rule)"} strokeWidth={1} shapeRendering="crispEdges" />
                {has && (
                  <text x={PAD.left - 8} y={geo.y(v)} dy="0.32em" textAnchor="end" className="fill-ink-3 text-[0.6875rem] tnum">
                    {axisFormat(v)}
                  </text>
                )}
              </g>
            ))}
            {!has &&
              [0.33, 0.66].map((f) => (
                <line key={f} x1={PAD.left} x2={PAD.left + geo.W} y1={PAD.top + geo.H * f} y2={PAD.top + geo.H * f} stroke="var(--rule)" strokeWidth={1} strokeDasharray="2 4" shapeRendering="crispEdges" />
              ))}
            {geo.tt.map((t, k) => (
              <text
                key={t}
                x={geo.x(t)}
                y={height - 6}
                textAnchor={geo.x(t) < PAD.left + 20 ? "start" : geo.x(t) > PAD.left + geo.W - 20 ? "end" : "middle"}
                className={cn("fill-ink-3 text-[0.6875rem] tnum", k % 2 === 1 && geo.W < 420 && "max-sm:hidden")}
              >
                {tickLabel(t, geo.t1 - geo.t0, false)}
              </text>
            ))}
            {geo.paths.map(({ s, k, fill }) => fill && <path key={`a${s.id}`} d={fill} fill={color(s, k)} fillOpacity={stacked ? 0.72 : 0.1} />)}
            {geo.paths.map(({ s, k, line }) => (
              <path
                key={s.id}
                d={line}
                fill="none"
                stroke={stacked ? "var(--paper)" : color(s, k)}
                strokeOpacity={stacked ? 0.6 : 1}
                strokeWidth={stacked ? 1 : geo.lw}
                strokeDasharray={s.dashed ? "4 4" : undefined}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
            ))}
            {placed.map((m) => (
              <g key={`${m.t}${m.label}`}>
                <line x1={geo.x(m.t)} x2={geo.x(m.t)} y1={PAD.top} y2={PAD.top + geo.H} stroke={m.failed ? "var(--danger)" : "var(--ink-3)"} strokeWidth={1} strokeDasharray="2 3" strokeOpacity={0.8} shapeRendering="crispEdges" />
                <path d={`M${geo.x(m.t) - 3.5},${PAD.top - 6} h7 l-3.5,5 z`} fill={m.failed ? "var(--danger)" : "var(--ink-3)"} />
              </g>
            ))}
            {limit && has && (
              <g>
                <line x1={PAD.left} x2={PAD.left + geo.W} y1={geo.y(limit.value)} y2={geo.y(limit.value)} stroke="var(--ink-3)" strokeWidth={1} strokeDasharray="5 4" shapeRendering="crispEdges" />
                <text x={PAD.left + geo.W} y={geo.y(limit.value) - 5} textAnchor="end" className="fill-ink-3 text-[0.6875rem]">
                  {limit.label}
                </text>
              </g>
            )}
            {hi >= 0 && (
              <g>
                <line x1={geo.x(xs[hi])} x2={geo.x(xs[hi])} y1={PAD.top} y2={PAD.top + geo.H} stroke="var(--ink-4)" strokeWidth={1} shapeRendering="crispEdges" />
                {series.map((s, k) => {
                  const v = geo.topv[k][hi];
                  if (!Number.isFinite(v) || (stacked && !(cols[k][hi] > 0))) return null;
                  return <circle key={s.id} cx={geo.x(xs[hi])} cy={geo.y(v)} r={mine ? 4 : 3} fill={color(s, k)} stroke="var(--paper)" strokeWidth={2} />;
                })}
              </g>
            )}
          </svg>
          {!has && (
            <div className="pointer-events-none absolute inset-x-0 grid place-items-center px-6 text-center text-[0.8125rem] text-ink-3" style={{ left: PAD.left, top: PAD.top, height: height - PAD.top - PAD.bottom }}>
              {empty}
            </div>
          )}
          {hi >= 0 && mine && (
            <Readout x={geo.x(xs[hi])} width={width}>
              <p className="text-[0.75rem] text-ink-3">{stepLabel(xs[hi], step, false)}</p>
              <dl className="mt-1 grid grid-cols-[auto_1fr_auto] items-center gap-x-2 gap-y-0.5">
                {(stacked ? [...rows].reverse() : rows).map(({ s, k, v }) => (
                  <div key={s.id} className="contents">
                    <span aria-hidden className={stacked ? "size-2.5 rounded-[3px]" : "h-0 w-3 border-t-2"} style={stacked ? { background: color(s, k) } : { borderColor: color(s, k) }} />
                    <dt className="truncate text-[0.75rem] text-ink-3">{s.label}</dt>
                    <dd className="text-right text-[0.8125rem] font-[550] text-ink tnum">{format(v)}</dd>
                  </div>
                ))}
                {total !== undefined && rows.length > 1 && (
                  <div className="contents">
                    <span aria-hidden />
                    <dt className="border-t border-rule pt-0.5 text-[0.75rem] text-ink-3">Total</dt>
                    <dd className="border-t border-rule pt-0.5 text-right text-[0.8125rem] font-[550] text-ink tnum">{format(total)}</dd>
                  </div>
                )}
              </dl>
              {near.map((m) => (
                <p key={m.t + m.label} className={cn("mt-1.5 border-t border-rule pt-1.5 text-[0.75rem] leading-4", m.failed ? "text-danger" : "text-ink-2")}>
                  {m.label}
                </p>
              ))}
            </Readout>
          )}
        </div>
      )}
      <p id={live} className="sr-only" aria-live="polite">
        {mine ? text : ""}
      </p>
    </div>
  );
}

/** The value a chart's header shows: at the shared crosshair when there is one, else the latest. */
export function valueAt(points: XY[], t: number | null, step: number): number | undefined {
  if (!points.length) return undefined;
  if (t === null) return points[points.length - 1][1];
  const i = nearest(
    points.map((p) => p[0]),
    t,
  );
  return Math.abs(points[i][0] - t) <= step * 1.5 ? points[i][1] : undefined;
}
