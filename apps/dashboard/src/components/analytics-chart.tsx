import { useMemo, useState } from "react";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";

export type Bucket = { t: number; visitors: number; pageviews: number };
export type Marker = { t: number; label: string };

// Days are UTC days (the box counts in UTC), so they're labelled as UTC days.
const dayFmt = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
const longDayFmt = new Intl.DateTimeFormat("en-GB", { weekday: "long", day: "numeric", month: "long", timeZone: "UTC" });
const hourFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const weekdayHourFmt = new Intl.DateTimeFormat("en-GB", { weekday: "short", hour: "2-digit", minute: "2-digit", hourCycle: "h23" });

/** A round top for the scale whose half is a whole number too: 4, 10, 12, 16, 20, 30, 250… */
function niceCeil(v: number) {
  if (v <= 10) return [2, 4, 6, 8, 10].find((x) => v <= x) ?? 10;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.6, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (v <= m * p && ((m * p) / 2) % 1 === 0) return m * p;
  return 10 * p;
}

/**
 * Visitors or page views over a period, as columns: one bucket per hour or
 * day, one hue (ink) plus gray, a printed scale (0, half, top) in the left
 * gutter, the time axis underneath, deploys as hairline markers, and a hover
 * card with both counts. Buckets after now are left empty; the bucket that
 * holds now is drawn lighter and says "so far".
 */
export function VisitsChart({
  buckets,
  step,
  metric,
  markers = [],
  height = 220,
  now: nowProp,
  className,
}: {
  buckets: Bucket[];
  /** Bucket length in ms (an hour or a day). */
  step: number;
  metric: "visitors" | "pageviews";
  markers?: Marker[];
  height?: number;
  now?: number;
  className?: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const [mounted] = useState(() => Date.now());
  const now = nowProp ?? mounted;
  const n = buckets.length;
  const t0 = buckets[0]?.t ?? 0;
  const t1 = t0 + n * step;
  const peak = Math.max(0, ...buckets.map((b) => b[metric]));
  const top = niceCeil(peak);
  const daily = step >= 86_400_000;
  const gap = n > 120 ? 0 : n > 60 ? 1 : 2;

  // Axis labels: at most ~7, on round positions.
  const ticks = useMemo(() => {
    if (!n) return [] as number[];
    const want = 7;
    let every = Math.max(1, Math.ceil(n / want));
    if (!daily) every = [1, 2, 3, 4, 6, 8, 12].find((h) => n / h <= want) ?? every;
    const out: number[] = [];
    for (let i = 0; i < n; i++) {
      const d = new Date(buckets[i].t);
      const round = daily ? i % every === 0 : d.getHours() % every === 0;
      if (round) out.push(i);
    }
    return out;
  }, [buckets, n, daily]);

  const placed = markers
    .filter((m) => m.t >= t0 && m.t <= Math.min(t1, now))
    .map((m) => ({ ...m, x: (m.t - t0) / (t1 - t0), i: Math.min(n - 1, Math.floor((m.t - t0) / step)) }));
  // Deploys close together share one line.
  const lines: Array<{ x: number; i: number; labels: string[] }> = [];
  for (const m of placed.sort((a, b) => a.x - b.x)) {
    const last = lines[lines.length - 1];
    if (last && m.x - last.x < 0.04) last.labels.push(m.label);
    else lines.push({ x: m.x, i: m.i, labels: [m.label] });
  }

  const h = hover !== null ? buckets[hover] : null;
  const label = (b: Bucket) =>
    daily ? longDayFmt.format(new Date(b.t)) : `${weekdayHourFmt.format(new Date(b.t))}–${hourFmt.format(new Date(b.t + step))}`;
  const partial = (b: Bucket) => b.t <= now && now < b.t + step;

  return (
    <figure className={cn("relative select-none", className)} aria-label={`${metric === "visitors" ? "Visitors" : "Page views"} per ${daily ? "day" : "hour"}, highest ${int(peak)}`}>
      <div className="grid grid-cols-[2.5rem_minmax(0,1fr)] gap-x-2">
        {/* the printed scale */}
        <div className="relative" style={{ height }} aria-hidden>
          {[1, 0.5, 0].map((f) => (
            <span key={f} className="absolute right-0 -translate-y-1/2 text-[0.6875rem] leading-none text-ink-3 tnum" style={{ top: `${(1 - f) * 100}%` }}>
              {int(top * f)}
            </span>
          ))}
        </div>
        <div className="relative" style={{ height }} onPointerLeave={() => setHover(null)}>
          {[1, 0.5].map((f) => (
            <span key={f} aria-hidden className="absolute inset-x-0 border-t border-dashed border-rule-2" style={{ top: `${(1 - f) * 100}%` }} />
          ))}
          <span aria-hidden className="absolute inset-x-0 bottom-0 border-t border-rule-3" />
          {/* deploys */}
          {lines.map((l) => (
            <span key={l.x} aria-hidden className="absolute top-0 bottom-0 w-px bg-ink-3/45" style={{ left: `${l.x * 100}%` }}>
              <span className={cn("absolute -top-[1.05rem] text-[0.65625rem] whitespace-nowrap text-ink-3 max-sm:hidden", l.x > 0.8 ? "right-1" : "left-1")}>{l.labels.length > 1 ? `${l.labels.length} deploys` : l.labels[0]}</span>
            </span>
          ))}
          {/* the columns */}
          <div className="absolute inset-0 flex items-end" style={{ gap }}>
            {buckets.map((b, i) => {
              const v = b[metric];
              const future = b.t > now;
              const on = hover === i;
              return (
                <div
                  key={b.t}
                  className="relative flex h-full min-w-0 flex-1 items-end"
                  onPointerEnter={() => !future && setHover(i)}
                  onPointerDown={() => !future && setHover(i)}
                >
                  {!future &&
                    (v > 0 ? (
                      <span
                        className={cn(
                          "mx-auto block rounded-t-[3px]",
                          n <= 31 ? "w-[64%]" : "w-full",
                          " transition-[background-color] duration-[var(--dur-state)]",
                          on ? "bg-ink" : partial(b) ? "bg-ink-2/55" : "bg-ink-2/80",
                        )}
                        style={{ height: `${Math.max(1.5, (v / top) * 100)}%` }}
                      />
                    ) : (
                      <span className="block h-px w-full bg-rule-3" />
                    ))}
                </div>
              );
            })}
          </div>
          {/* the hover card */}
          {h && hover !== null && (
            <div
              role="status"
              className="pointer-events-none absolute top-2 z-10 w-[12.5rem] rounded-[8px] border border-rule-2 bg-paper-raised px-3 py-2 shadow-raised"
              style={{
                // Beside the hovered column, and never past either edge of the plot.
                left:
                  hover / n > 0.6
                    ? `clamp(0px, calc(${(hover / n) * 100}% - 12.5rem - 6px), calc(100% - 12.5rem))`
                    : `clamp(0px, calc(${((hover + 1) / n) * 100}% + 6px), calc(100% - 12.5rem))`,
              }}
            >
              <p className="text-[0.75rem] text-ink-3">
                {label(h)}
                {partial(h) && " · so far"}
              </p>
              <dl className="mt-1 grid grid-cols-[1fr_auto] gap-x-6 text-[0.8125rem]">
                <dt className={metric === "visitors" ? "text-ink" : "text-ink-3"}>Visitors</dt>
                <dd className={cn("text-right tnum", metric === "visitors" ? "text-ink" : "text-ink-3")}>{int(h.visitors)}</dd>
                <dt className={metric === "pageviews" ? "text-ink" : "text-ink-3"}>Page views</dt>
                <dd className={cn("text-right tnum", metric === "pageviews" ? "text-ink" : "text-ink-3")}>{int(h.pageviews)}</dd>
              </dl>
              {lines
                .filter((l) => l.i === hover)
                .flatMap((l) => l.labels)
                .map((x) => (
                  <p key={x} className="mt-1 border-t border-rule pt-1 text-[0.75rem] text-ink-2">
                    {x}
                  </p>
                ))}
            </div>
          )}
        </div>
      </div>
      {/* the time axis */}
      <div className="relative mt-1.5 ml-[3rem] h-4" aria-hidden>
        {ticks.map((i, k) => (
          <span
            key={i}
            className={cn("absolute -translate-x-1/2 text-[0.6875rem] whitespace-nowrap text-ink-3 tnum", k % 2 === 1 && "max-sm:hidden")}
            style={{ left: `${((i + 0.5) / n) * 100}%` }}
          >
            {daily ? dayFmt.format(new Date(buckets[i].t)) : hourFmt.format(new Date(buckets[i].t))}
          </span>
        ))}
      </div>
    </figure>
  );
}
