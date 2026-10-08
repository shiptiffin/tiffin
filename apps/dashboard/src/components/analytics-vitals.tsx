import { useMemo, type ReactNode } from "react";
import type { AnalyticsVitals } from "@/api/modules";
import { CodeBox } from "@/components/analytics-kit";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { DAY } from "@/components/analytics-toolbar";
import { TimeSeries, type Series } from "@/components/charts/time-series";
import { cn } from "@/lib/cn";
import { dec, int, ms, pct } from "@/lib/format";
import { VITALS, type Vital } from "@/routes/analytics-search";

/**
 * Web Vitals the way Speed Insights shows them: the five measures with the
 * figure Google rates (the 75th percentile of real visits) in its rating's
 * colour, the chosen one per day against its "good" line, and the pages.
 */

const about: Record<Vital, { name: string; plain: string; good: number; poor: number }> = {
  LCP: { name: "Largest Contentful Paint", plain: "When the main content shows", good: 2500, poor: 4000 },
  INP: { name: "Interaction to Next Paint", plain: "How fast taps and clicks answer", good: 200, poor: 500 },
  CLS: { name: "Cumulative Layout Shift", plain: "How much the layout jumps", good: 0.1, poor: 0.25 },
  FCP: { name: "First Contentful Paint", plain: "When anything first shows", good: 1800, poor: 3000 },
  TTFB: { name: "Time to First Byte", plain: "How fast the server answers", good: 800, poor: 1800 },
};
export const vitalValue = (name: string, v: number) => (name === "CLS" ? dec(v, 2) : ms(v));

const ratings: Record<string, { word: string; color: string; shape: ReactNode }> = {
  good: { word: "Good", color: "text-ok", shape: <circle cx="5" cy="5" r="4" /> },
  "needs-improvement": { word: "Needs work", color: "text-warn-ink", shape: <path d="M5 1 9 9H1Z" /> },
  poor: { word: "Poor", color: "text-danger", shape: <rect x="1.5" y="1.5" width="7" height="7" rx="1" /> },
};
/** A rating as a shape and a colour: circle good, triangle needs work, square poor. */
function Mark({ rating, className }: { rating?: string; className?: string }) {
  const r = rating ? ratings[rating] : undefined;
  if (!r) return null;
  return (
    <svg viewBox="0 0 10 10" className={cn("inline-block size-2.5 shrink-0 fill-current", r.color, className)} role="img" aria-label={r.word}>
      {r.shape}
    </svg>
  );
}
const rate = (name: Vital, v: number) => (v <= about[name].good ? "good" : v <= about[name].poor ? "needs-improvement" : "poor");

export function WebVitals({
  v,
  code,
  onSetup,
  vital,
  onVital,
  selected,
  onPage,
  ignored,
}: {
  v: AnalyticsVitals;
  code?: string;
  /** Opens the page's setup guide; without it, the code folds out here instead. */
  onSetup?: () => void;
  vital: Vital;
  onVital: (x: Vital) => void;
  selected?: string;
  onPage: (p: string) => void;
  /** Filters other than page are on: vitals are kept per page, not per visit, so they don't apply. */
  ignored?: boolean;
}) {
  const list = v.metrics ?? [];
  const pages = v.pages ?? [];
  const days = useMemo(() => v.days ?? [], [v.days]);
  const have = new Set(list.map((m) => m.name));
  const cur: Vital = have.has(vital) ? vital : ((VITALS.find((x) => have.has(x)) ?? "LCP") as Vital);
  const A = about[cur];
  const series = useMemo<Series[]>(
    () => [{ id: cur, label: `${cur} (75th percentile)`, points: days.map((d) => [Date.parse(`${d.day}T00:00:00Z`), d.p75?.[cur] ?? NaN]), area: true, tone: "ink" }],
    [days, cur],
  );
  const cols = Array.from(new Set<Vital>(["LCP", "INP", "CLS", cur]));

  return (
    <section aria-labelledby="speed" className="rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-b border-rule px-4 py-3">
        <h2 id="speed" className="text-[0.84375rem] font-[550] text-ink">
          Web Vitals
          {ignored && <span className="ml-2 font-[400] text-ink-3">All visits: only the page filter applies here</span>}
        </h2>
        {list.length > 0 && (
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[0.75rem] text-ink-3">
            {Object.entries(ratings).map(([k, r]) => (
              <span key={k} className="inline-flex items-center gap-1">
                <Mark rating={k} />
                {r.word}
              </span>
            ))}
          </span>
        )}
      </div>

      {list.length === 0 ? (
        <div className="px-4 py-5 text-[0.875rem] text-ink-2">
          <p className="font-[550] text-ink">No speed readings yet</p>
          <p className="mt-1.5">They come from your visitors’ browsers once your app reports them.</p>
          {onSetup ? (
            <button type="button" onClick={onSetup} className="mt-2 text-[0.8125rem] font-[550] text-ink underline decoration-rule-3 underline-offset-4 hover:decoration-ink">
              See how
            </button>
          ) : (
            <details className="group mt-2">
              <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
                <span className="inline-block transition-transform group-open:rotate-90">›</span> Show the code for a Next.js app
              </summary>
              <CodeBox className="mt-2 max-w-[40rem]" name="app/layout.tsx" code={nextVitals(code)} />
              <p className="mt-2 text-[0.8125rem] text-ink-3">Elsewhere, call reportWebVitals() from @shiptiffin/sdk/vitals in browser code.</p>
            </details>
          )}
        </div>
      ) : (
        <>
          <RadioGroup value={cur} onValueChange={(x) => onVital(x as Vital)} aria-label="Measure" className="flex overflow-x-auto border-b border-rule [scrollbar-width:none] lg:grid lg:grid-cols-5">
            {list.map((m, i) => {
              const on = m.name === cur;
              const r = ratings[m.rating];
              return (
                <RadioItem
                  key={m.name}
                  value={m.name}
                  className={cn(
                    "relative min-w-[10.5rem] flex-1 px-4 py-3.5 text-left transition-colors duration-[var(--dur-state)] lg:min-w-0",
                    on ? "bg-paper" : "hover:bg-paper-hover",
                    i > 0 && "border-l border-rule",
                  )}
                >
                  {on && <span aria-hidden className="absolute inset-x-0 -bottom-px h-[2px] bg-ink" />}
                  <span className="flex items-baseline justify-between gap-2">
                    <span className={cn("text-[0.8125rem] font-[550]", on ? "text-ink" : "text-ink-2")} title={about[m.name as Vital]?.name}>
                      {m.name}
                    </span>
                    <span className={cn("inline-flex items-center gap-1 text-[0.71875rem]", r?.color)}>
                      <Mark rating={m.rating} />
                      {r?.word}
                    </span>
                  </span>
                  <span className={cn("mt-1 block text-[1.5rem] leading-8 font-[500] tracking-[-0.02em] tnum", r?.color ?? "text-ink")}>{vitalValue(m.name, m.p75)}</span>
                  <span className="mt-1.5 block h-1 overflow-hidden rounded-full bg-rule" aria-hidden>
                    <span className="block h-full rounded-full bg-ok" style={{ width: `${m.good * 100}%` }} />
                  </span>
                  <span className="mt-1 block text-[0.71875rem] text-ink-3 tnum">
                    {pct(m.good)} good · {int(m.samples)} {m.samples === 1 ? "visit" : "visits"}
                  </span>
                </RadioItem>
              );
            })}
          </RadioGroup>

          <div className="grid gap-x-8 gap-y-6 px-4 pt-4 pb-4 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]">
            <div className="min-w-0">
              <p className="text-[0.8125rem] text-ink-2">
                <span className="font-[550] text-ink">{A.name}</span> · {A.plain.toLowerCase()}
              </p>
              <p className="mt-0.5 text-[0.75rem] text-ink-3">
                Three in four visits were at least this fast (the 75th percentile). Good is under {vitalValue(cur, A.good)} (the dashed line), poor is over {vitalValue(cur, A.poor)}.
              </p>
              {days.length > 1 ? (
                <TimeSeries
                  className="mt-3"
                  series={series}
                  step={DAY}
                  utc
                  height={200}
                  format={(x) => vitalValue(cur, x)}
                  axisFormat={(x) => (cur === "CLS" ? dec(x, 2) : !x ? "0" : x >= 1000 ? `${dec(x / 1000, 1)}s` : `${int(x)}ms`)}
                  label={`${cur} per day, the 75th percentile`}
                  limit={{ value: A.good, label: "" }}
                />
              ) : (
                <p className="mt-6 text-[0.8125rem] text-ink-3">A chart per day appears once there are two days of readings.</p>
              )}
            </div>
            {pages.length > 0 && (
              <div className="min-w-0 overflow-x-auto">
                <table className="w-full text-[0.84375rem] sm:min-w-[22rem]">
                  <caption className="sr-only">Web Vitals per page: the 75th percentile of each measure</caption>
                  <thead>
                    <tr className="border-b border-rule-2 text-[0.71875rem] text-ink-3">
                      <th scope="col" className="py-1.5 text-left font-[450]">
                        Page
                      </th>
                      {cols.map((c) => (
                        <th key={c} scope="col" className={cn("w-[4.75rem] py-1.5 text-right font-[450]", c === cur ? "font-[550] text-ink" : "max-sm:hidden")} title={about[c].name}>
                          {c}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-rule">
                    {pages.map((pg) => (
                      <tr key={pg.path}>
                        <th scope="row" className="max-w-0 py-1.5 pr-3 text-left font-[400]">
                          <button
                            type="button"
                            onClick={() => onPage(pg.path)}
                            aria-pressed={selected === pg.path}
                            className={cn(
                              "block max-w-full truncate rounded-[4px] text-left font-mono text-[0.75rem] hover:underline hover:underline-offset-4",
                              selected === pg.path ? "bg-brass-wash px-1 text-ink" : "text-ink",
                            )}
                            title={selected === pg.path ? `Remove the filter on ${pg.path}` : `Show only visits to ${pg.path}`}
                          >
                            {pg.path}
                          </button>
                        </th>
                        {cols.map((c) => {
                          const x = pg.p75?.[c];
                          return (
                            <td key={c} className={cn("py-1.5 text-right tnum", c === cur ? "text-ink" : "text-ink-2 max-sm:hidden")}>
                              {x === undefined ? (
                                <span className="text-ink-4">–</span>
                              ) : (
                                <span className="inline-flex items-center gap-1.5">
                                  {vitalValue(c, x)}
                                  <Mark rating={pg.ratings?.[c] ?? rate(c, x)} />
                                </span>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}
    </section>
  );
}

/** The documented root layout with <WebVitals /> (docs/guide/analytics.md). */
export function nextVitals(code?: string) {
  const imp = code?.split(";")[0]?.trim() || `import { WebVitals } from "@shiptiffin/sdk/next/vitals"`;
  return `${imp};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <WebVitals />
        {children}
      </body>
    </html>
  );
}`;
}
