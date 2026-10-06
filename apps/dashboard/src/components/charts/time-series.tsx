import { area as d3area, curveMonotoneX, line as d3line } from "d3-shape";
import { useId, useMemo, useState, type KeyboardEvent, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { nearest, niceScale, stepLabel, tickLabel, timeTicks, useWidth, type XY } from "./core";

/**
 * Values over time, as lines with a wash under the first: one hue (ink) and
 * grays, solid hairline gridlines on the axis's own ticks, a dashed line for
 * the period before (step for step), a reference line for a limit, deploys
 * as hairline markers. Hover or arrow keys move a crosshair that snaps to
 * the nearest step; the readout lists every series there. Paths are built
 * once per size and data, so the crosshair moves without redrawing them
 * (thousands of points stay smooth).
 */

export type Tone = "ink" | "soft" | "faint";
export type Series = {
  id: string;
  label: string;
  points: XY[];
  /** ink: the series the chart is about; soft and faint: others beside it. */
  tone?: Tone;
  /** A wash under the line (the first series usually). */
  area?: boolean;
  /** The period before: drawn dashed under the others, its points matched to the first series by position. */
  ghost?: boolean;
};

const stroke: Record<Tone, string> = { ink: "var(--ink-2)", soft: "var(--part-3)", faint: "var(--ink-4)" };
const PAD = { top: 10, right: 6, bottom: 22, left: 46 };

export type TimeSeriesProps = {
  series: Series[];
  /** Milliseconds per step (an hour, a day, 30 seconds). */
  step: number;
  /** Values as people read them: the readout and the axis. */
  format: (v: number) => string;
  /** Shorter values for the axis, if they differ. */
  axisFormat?: (v: number) => string;
  /** Accessible name: what the chart shows. */
  label: string;
  height?: number;
  /** Days and hours in UTC (analytics counts in UTC days). */
  utc?: boolean;
  /** A horizontal reference line, always in view. */
  limit?: { value: number; label: string };
  /** Moments to mark, e.g. deploys. */
  markers?: Array<{ t: number; label: string }>;
  /** The top of the scale at least this (e.g. 1 for a share). */
  minTop?: number;
  /** The step holding now is still filling: drawn dashed, read "so far". */
  now?: number;
  className?: string;
};

export function TimeSeries(props: TimeSeriesProps) {
  const { series, step, format, axisFormat = format, label, height = 220, utc = false, limit, markers = [], minTop = 0, now, className } = props;
  const [wrap, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const live = useId();
  const main = series.find((s) => !s.ghost) ?? series[0];
  const xs = useMemo(() => (main?.points ?? []).map((p) => p[0]), [main]);
  const n = xs.length;

  const geo = useMemo(() => {
    const W = Math.max(0, width - PAD.left - PAD.right);
    const H = height - PAD.top - PAD.bottom;
    if (!n || W <= 0) return null;
    const t0 = xs[0];
    const t1 = n > 1 ? xs[n - 1] : t0 + step;
    let peak = minTop;
    for (const s of series) for (const p of s.points) if (p[1] > peak) peak = p[1];
    if (limit && limit.value > peak) peak = limit.value;
    const { top, ticks } = niceScale(peak, height < 160 ? 2 : 3);
    const x = (t: number) => PAD.left + (t1 === t0 ? W / 2 : ((t - t0) / (t1 - t0)) * W);
    const y = (v: number) => PAD.top + H - (Math.max(0, v) / top) * H;
    const at = (s: Series, i: number) => (s.ghost ? xs[i] : s.points[i][0]);
    // A gap in the data (more than a step and a half) breaks the line.
    const gaps = (s: Series) => (i: number) => Number.isFinite(s.points[i][1]) && (i === 0 || s.ghost || s.points[i][0] - s.points[i - 1][0] <= step * 1.5);
    const paths = series.map((s) => {
      const pts = s.ghost ? s.points.slice(0, n) : s.points;
      const ok = gaps({ ...s, points: pts });
      // d3's defined() looks at a point; carry the index along.
      const idx = pts.map((_, i) => i);
      const ln = d3line<number>()
        .defined((i) => ok(i))
        .x((i) => x(at({ ...s, points: pts }, i)))
        .y((i) => y(pts[i][1]))
        .curve(curveMonotoneX);
      const partial = !s.ghost && now !== undefined && pts.length > 1 && pts[pts.length - 1][0] <= now && now < pts[pts.length - 1][0] + step;
      const solid = ln(partial ? idx.slice(0, -1) : idx) ?? "";
      const tail = partial ? (ln(idx.slice(-2)) ?? "") : "";
      const fill = s.area
        ? (d3area<number>()
            .defined((i) => ok(i))
            .x((i) => x(at({ ...s, points: pts }, i)))
            .y0(PAD.top + H)
            .y1((i) => y(pts[i][1]))
            .curve(curveMonotoneX)(idx) ?? "")
        : "";
      return { s, solid, tail, fill };
    });
    const tt = timeTicks(t0, t1, Math.max(2, Math.floor(W / 90)), utc);
    // Many points to a pixel: thinner lines, so the shape reads instead of a block of ink.
    const lw = n > W / 3 ? 1.25 : 2;
    return { W, H, t0, t1, top, ticks, x, y, paths, tt, lw };
  }, [width, height, n, xs, series, step, limit, minTop, utc, now]);

  const move = (clientX: number, rect: DOMRect) => {
    if (!geo) return;
    const t = geo.t0 + ((clientX - rect.left - PAD.left) / geo.W) * (geo.t1 - geo.t0);
    const i = nearest(xs, t);
    if (i !== hover) setHover(i);
  };
  const key = (e: KeyboardEvent) => {
    if (!n) return;
    const go = (i: number) => {
      e.preventDefault();
      setHover(Math.max(0, Math.min(n - 1, i)));
    };
    if (e.key === "ArrowRight") go((hover ?? n - 2) + 1);
    else if (e.key === "ArrowLeft") go((hover ?? n) - 1);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(n - 1);
    else if (e.key === "Escape") setHover(null);
  };

  const h = hover !== null && hover < n ? hover : null;
  const readout = h === null ? null : readoutAt(series, h, main, step, utc, format, now);
  const placed = geo ? markers.filter((m) => m.t >= geo.t0 && m.t <= geo.t1 + step) : [];

  return (
    <div ref={wrap} className={cn("relative select-none", className)} style={{ height }}>
      {geo && (
        <div
          role="group"
          tabIndex={0}
          aria-label={`${label}. Use the left and right arrow keys to read each ${stepWord(step)}.`}
          aria-describedby={live}
          onKeyDown={key}
          onBlur={() => setHover(null)}
          onPointerMove={(e) => move(e.clientX, e.currentTarget.getBoundingClientRect())}
          onPointerDown={(e) => move(e.clientX, e.currentTarget.getBoundingClientRect())}
          onPointerLeave={(e) => e.pointerType === "mouse" && setHover(null)}
          className="absolute inset-0 touch-pan-y rounded-[6px] focus-visible:outline-offset-4"
        >
          <svg width={width} height={height} className="block overflow-visible" aria-hidden>
            {geo.ticks.map((v) => (
              <g key={v}>
                <line x1={PAD.left} x2={PAD.left + geo.W} y1={geo.y(v)} y2={geo.y(v)} stroke={v === 0 ? "var(--rule-2)" : "var(--rule)"} strokeWidth={1} shapeRendering="crispEdges" />
                <text x={PAD.left - 8} y={geo.y(v)} dy="0.32em" textAnchor="end" className="fill-ink-3 text-[0.6875rem] tnum">
                  {axisFormat(v)}
                </text>
              </g>
            ))}
            {geo.tt.map((t, k) => (
              <text
                key={t}
                x={geo.x(t)}
                y={height - 6}
                textAnchor={geo.x(t) < PAD.left + 20 ? "start" : geo.x(t) > PAD.left + geo.W - 20 ? "end" : "middle"}
                className={cn("fill-ink-3 text-[0.6875rem] tnum", k % 2 === 1 && geo.W < 420 && "max-sm:hidden")}
              >
                {tickLabel(t, geo.t1 - geo.t0, utc)}
              </text>
            ))}
            {placed.map((m) => (
              <line key={`${m.t}${m.label}`} x1={geo.x(m.t)} x2={geo.x(m.t)} y1={PAD.top} y2={PAD.top + geo.H} stroke="var(--ink-4)" strokeWidth={1} strokeOpacity={0.7} shapeRendering="crispEdges" />
            ))}
            {geo.paths.map(({ s, fill }) => fill && <path key={`a${s.id}`} d={fill} fill={stroke[s.tone ?? "ink"]} fillOpacity={0.1} />)}
            {geo.paths
              .filter((p) => p.s.ghost)
              .map(({ s, solid }) => (
                <path key={s.id} d={solid} fill="none" stroke="var(--ink-4)" strokeWidth={geo.lw === 2 ? 1.5 : 1} strokeDasharray="3 4" strokeLinecap="round" />
              ))}
            {geo.paths
              .filter((p) => !p.s.ghost)
              .map(({ s, solid, tail }) => (
                <g key={s.id}>
                  <path d={solid} fill="none" stroke={stroke[s.tone ?? "ink"]} strokeWidth={geo.lw} strokeLinejoin="round" strokeLinecap="round" />
                  {tail && <path d={tail} fill="none" stroke={stroke[s.tone ?? "ink"]} strokeWidth={geo.lw} strokeDasharray="2 4" strokeLinecap="round" />}
                </g>
              ))}
            {limit && (
              <g>
                <line x1={PAD.left} x2={PAD.left + geo.W} y1={geo.y(limit.value)} y2={geo.y(limit.value)} stroke="var(--ink-3)" strokeWidth={1} strokeDasharray="5 4" shapeRendering="crispEdges" />
                <text x={PAD.left + geo.W} y={geo.y(limit.value) - 5} textAnchor="end" className="fill-ink-3 text-[0.6875rem]">
                  {limit.label}
                </text>
              </g>
            )}
            {h !== null && (
              <g>
                <line x1={geo.x(xs[h])} x2={geo.x(xs[h])} y1={PAD.top} y2={PAD.top + geo.H} stroke="var(--rule-3)" strokeWidth={1} shapeRendering="crispEdges" />
                {series.map((s) => {
                  const p = s.points[h];
                  if (!p || !Number.isFinite(p[1])) return null;
                  return (
                    <circle
                      key={s.id}
                      cx={geo.x(xs[h])}
                      cy={geo.y(p[1])}
                      r={s.ghost ? 3 : 4}
                      fill={s.ghost ? "var(--ink-4)" : stroke[s.tone ?? "ink"]}
                      stroke="var(--paper)"
                      strokeWidth={2}
                    />
                  );
                })}
              </g>
            )}
          </svg>
          {readout && h !== null && (
            <Readout x={geo.x(xs[h])} width={width}>
              {readout}
            </Readout>
          )}
        </div>
      )}
      <p id={live} className="sr-only" aria-live="polite">
        {readout ? readoutText(series, h!, main, step, utc, format, now) : ""}
      </p>
    </div>
  );
}

function stepWord(step: number) {
  return step >= 86_400_000 ? "day" : step >= 3_600_000 ? "hour" : "point";
}

function readoutAt(series: Series[], i: number, main: Series, step: number, utc: boolean, format: (v: number) => string, now?: number): ReactNode {
  const t = main.points[i]?.[0];
  if (t === undefined) return null;
  const partial = now !== undefined && t <= now && now < t + step;
  return (
    <>
      <p className="text-[0.75rem] text-ink-3">
        {stepLabel(t, step, utc)}
        {partial && " · so far"}
      </p>
      <dl className="mt-1 grid grid-cols-[auto_1fr_auto] items-center gap-x-2 gap-y-0.5">
        {series.map((s) => {
          const p = s.points[i];
          if (!p || !Number.isFinite(p[1])) return null;
          return (
            <div key={s.id} className="contents">
              <span aria-hidden className={cn("h-0 w-3 border-t-2", s.ghost && "border-dashed")} style={{ borderColor: s.ghost ? "var(--ink-4)" : stroke[s.tone ?? "ink"] }} />
              <dt className="truncate text-[0.75rem] text-ink-3">{s.ghost ? `${s.label}, ${stepLabel(p[0], step, utc)}` : s.label}</dt>
              <dd className={cn("text-right text-[0.8125rem] tnum", s.ghost ? "text-ink-3" : "font-[550] text-ink")}>{format(p[1])}</dd>
            </div>
          );
        })}
      </dl>
    </>
  );
}

function readoutText(series: Series[], i: number, main: Series, step: number, utc: boolean, format: (v: number) => string, now?: number) {
  const t = main.points[i]?.[0];
  if (t === undefined) return "";
  const partial = now !== undefined && t <= now && now < t + step ? ", so far" : "";
  return `${stepLabel(t, step, utc)}${partial}: ${series
    .filter((s) => s.points[i] && Number.isFinite(s.points[i][1]))
    .map((s) => `${s.label} ${format(s.points[i][1])}`)
    .join(", ")}`;
}

/** The hover card: beside the crosshair, flipping sides near the right edge, never past either edge. */
export function Readout({ x, width, children }: { x: number; width: number; children: ReactNode }) {
  const W = 216;
  const left = x + 12 + W > width ? Math.max(0, x - 12 - W) : x + 12;
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute top-1 z-10 rounded-[8px] border border-rule-2 bg-paper-raised px-3 py-2 shadow-raised"
      style={{ left, width: W }}
    >
      {children}
    </div>
  );
}

/** Every step as a table: the chart's data without hovering. */
export function SeriesTable({ series, step, utc, format, caption }: { series: Series[]; step: number; utc?: boolean; format: (v: number) => string; caption: string }) {
  const main = series.find((s) => !s.ghost) ?? series[0];
  if (!main) return null;
  return (
    <div className="max-h-[22rem] overflow-auto rounded-[8px] border border-rule-2">
      <table className="w-full text-[0.8125rem]">
        <caption className="sr-only">{caption}</caption>
        <thead className="sticky top-0 bg-paper-raised text-[0.75rem] text-ink-3">
          <tr>
            <th scope="col" className="px-3 py-1.5 text-left font-[450]">
              {step >= 86_400_000 ? "Day" : "Time"}
            </th>
            {series.map((s) => (
              <th key={s.id} scope="col" className="px-3 py-1.5 text-right font-[450]">
                {s.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-rule">
          {main.points.map((p, i) => (
            <tr key={p[0]}>
              <th scope="row" className="px-3 py-1 text-left font-[400] whitespace-nowrap text-ink-2 tnum">
                {stepLabel(p[0], step, !!utc)}
              </th>
              {series.map((s) => (
                <td key={s.id} className="px-3 py-1 text-right text-ink tnum">
                  {s.points[i] && Number.isFinite(s.points[i][1]) ? format(s.points[i][1]) : "–"}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
