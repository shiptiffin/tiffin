import { useQuery } from "@tanstack/react-query";
import { curveMonotoneX, line } from "d3-shape";
import { useMemo } from "react";
import type { BoxResources } from "@/api/client";
import { mq } from "@/api/modules";
import { cn } from "@/lib/cn";
import { bytes, dec } from "@/lib/format";
import { cpuWords, memWords, type Shares } from "@/lib/usage";

/**
 * The box's memory, CPU and disk as a quiet row of numbers, each with the
 * last hour as a small trend (from the box's health overview). No bars: the
 * stacked bar above is the one picture of how full the box is. On a phone
 * the three stack, the trend beside each number.
 */
export function UsageVitals({ res, shares, className }: { res: BoxResources; shares: Shares; className?: string }) {
  // The overview polls every 15 s on Health; once a minute is plenty for an hour's trend.
  const o = useQuery({ ...mq.overview, refetchInterval: 60_000, retry: false });
  const series = (k: string) => (o.data?.series?.[k] ?? []).map((p) => Number(p?.[1])).filter(Number.isFinite);
  const disk = res.disks.data;
  const guard = res.guard;
  const diskTone = guard?.level === "stop" ? "text-danger" : guard?.level === "warn" ? "text-warn-ink" : "text-ink";
  return (
    <div role="group" aria-label="The box now" className={cn("grid divide-y divide-rule border-y border-rule sm:grid-cols-3 sm:divide-x sm:divide-y-0", className)}>
      <Vital label="Memory" value={memWords(shares.usedMB)} of={`of ${memWords(shares.totalMB)}`} trend={series("memory")} />
      <Vital label="CPU" value={`${dec(res.cpu.usedPercent, 0)}%`} of={`of ${cpuWords(res.cpu.count)}`} trend={series("cpu")} />
      <Vital label="Disk" value={bytes(disk.usedBytes)} of={`of ${bytes(disk.totalBytes, 0)}`} trend={series("disk")} tone={diskTone} />
    </div>
  );
}

function Vital({ label, value, of, trend, tone = "text-ink" }: { label: string; value: string; of: string; trend: number[]; tone?: string }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_7rem] items-center gap-x-4 py-3 sm:block sm:px-5 sm:py-3.5 sm:first:pl-0 sm:last:pr-0">
      <div className="min-w-0">
        <p className="text-[0.8125rem] text-ink-3">{label}</p>
        <p className="mt-0.5 flex items-baseline gap-1.5 whitespace-nowrap">
          <span className={cn("text-[1.25rem] leading-7 font-[500] tracking-[-0.015em] tnum", tone)}>{value}</span>
          <span className="truncate text-[0.8125rem] text-ink-3">{of}</span>
        </p>
      </div>
      {/* The shape of the last hour; the number beside it is the value. */}
      <div className="sm:mt-2.5" title={trend.length > 1 ? "The last hour" : undefined}>
        <Trend values={trend} />
      </div>
    </div>
  );
}

const W = 120;
const H = 24;

/**
 * A line for the last hour, no fill and no axis. The scale spans at least
 * 10 points (percent), so a steady box draws a steady line rather than its
 * noise blown up; a dot marks now.
 */
function Trend({ values }: { values: number[] }) {
  const d = useMemo(() => {
    if (values.length < 2) return null;
    let lo = Math.min(...values);
    let hi = Math.max(...values);
    if (hi - lo < 10) {
      const mid = (hi + lo) / 2;
      lo = Math.max(0, mid - 5);
      hi = lo + 10;
    }
    const x = (i: number) => (i / (values.length - 1)) * W;
    const y = (v: number) => 2 + (1 - (v - lo) / (hi - lo)) * (H - 4);
    const path = line<number>().x((_, i) => x(i)).y(y).curve(curveMonotoneX)(values) ?? "";
    return { path, end: [x(values.length - 1), y(values[values.length - 1])] as const };
  }, [values]);
  if (!d) return <span aria-hidden className="block" style={{ height: H }} />;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="block w-full overflow-visible" style={{ height: H }} aria-hidden>
      <path d={d.path} fill="none" stroke="var(--ink-3)" strokeWidth={1.25} strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
