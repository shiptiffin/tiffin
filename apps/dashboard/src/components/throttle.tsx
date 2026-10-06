import { Minus, Plus } from "lucide-react";
import type { KeyboardEvent, ReactNode } from "react";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";

/** Instance counts an app can snap to (the manifest allows 1–16). */
export const INSTANCE_STOPS = [1, 2, 3, 4, 6, 8, 12, 16];

/**
 * A stepper for counts and sizes: − 2 +. It moves through meaningful stops
 * (1, 2, 3, 4, 6, 8… copies; 256 MB, 512 MB, 1 GB…) and stops at what fits
 * on the box (`maxFit`): past it, + is off and says why. Each step calls
 * `onChange` and `onCommit` (callers make the change there; rapid clicks are
 * batched by lib/staged.ts). While a change is on its way (`value` differs
 * from `applied`) it shows a small spinner.
 *
 *   <Throttle label="web copies" stops={INSTANCE_STOPS} value={3} applied={2} maxFit={6} onCommit={(n) => change(...)} />
 *
 * Keyboard: the value is a spinbutton: ↑/→ and ↓/← step, Home/End go to the ends.
 * (The name is from the detent throttle it replaced, so every caller changed at once.)
 */
export function Throttle({
  label,
  stops,
  value,
  applied,
  maxFit,
  onChange,
  onCommit,
  size = "full",
  unit = "copies",
  readout,
  format = int,
  printed,
  className,
}: {
  label: string;
  stops: number[];
  /** The value shown (on its way, or live). */
  value: number;
  /** The live value. When different from `value`, a change is applying. */
  applied?: number;
  /** The largest stop that fits on the box; + stops there. */
  maxFit?: number;
  onChange?: (n: number) => void;
  onCommit?: (n: number) => void;
  size?: "full" | "mini";
  unit?: string;
  /** Shown under the stepper. */
  readout?: ReactNode;
  /** How a stop is printed and read aloud (default: the number). */
  format?: (n: number) => string;
  /** A small legend under a mini stepper ("2 copies"). */
  printed?: (n: number) => string;
  className?: string;
}) {
  const n = stops.length;
  const indexOf = (v: number) => {
    let best = 0;
    stops.forEach((s, i) => {
      if (Math.abs(s - v) < Math.abs(stops[best] - v)) best = i;
    });
    return best;
  };
  // Never block the live value itself, even if the box is already over.
  const fitLimit = Math.max(maxFit ?? stops[n - 1], applied ?? stops[0]);
  const lastFit = stops.reduce((acc, s, i) => (s <= fitLimit ? i : acc), 0);
  const idx = indexOf(value);
  const busy = applied !== undefined && stops[idx] !== applied;
  const to = (i: number) => {
    const j = Math.max(0, Math.min(lastFit, i));
    if (j === idx) return;
    onChange?.(stops[j]);
    onCommit?.(stops[j]);
  };
  const onKey = (e: KeyboardEvent) => {
    const step: Record<string, number> = { ArrowRight: 1, ArrowUp: 1, ArrowLeft: -1, ArrowDown: -1 };
    if (e.key in step) to(idx + step[e.key]);
    else if (e.key === "Home") to(0);
    else if (e.key === "End") to(lastFit);
    else return;
    e.preventDefault();
    e.stopPropagation();
  };
  const full = idx >= lastFit && lastFit < n - 1;
  const btn =
    "grid h-full w-8 place-items-center text-ink-2 transition-colors hover:bg-paper-hover hover:text-ink disabled:pointer-events-none disabled:text-ink-4 [&_svg]:size-3.5";
  return (
    <div className={cn("inline-flex flex-col items-start gap-1", className)} onClick={(e) => e.stopPropagation()}>
      <div className={cn("inline-flex items-stretch overflow-hidden rounded-[8px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]", size === "mini" ? "h-7" : "h-8")}>
        <button type="button" tabIndex={-1} className={btn} aria-label={`Less: ${label}`} disabled={idx === 0} onClick={() => to(idx - 1)}>
          <Minus />
        </button>
        <span
          role="spinbutton"
          tabIndex={0}
          aria-label={label}
          aria-valuemin={stops[0]}
          aria-valuemax={stops[lastFit]}
          aria-valuenow={stops[idx]}
          aria-valuetext={`${format(stops[idx])} ${unit}${busy ? ", applying" : ""}`}
          aria-busy={busy || undefined}
          onKeyDown={onKey}
          className={cn(
            "flex min-w-[2.75rem] items-center justify-center gap-1.5 border-x border-rule px-2 text-[0.875rem] font-[550] text-ink tnum outline-none focus-visible:bg-brass-wash",
            size === "mini" && "text-[0.8125rem]",
          )}
        >
          {format(stops[idx])}
          {busy && <span className="spinner" aria-hidden />}
        </span>
        <button
          type="button"
          tabIndex={-1}
          className={btn}
          aria-label={`More: ${label}`}
          title={full ? "That’s all that fits on the box right now" : undefined}
          disabled={idx >= lastFit}
          onClick={() => to(idx + 1)}
        >
          <Plus />
        </button>
      </div>
      {printed && (
        <span aria-hidden className="text-xs text-ink-3">
          {printed(stops[idx])}
        </span>
      )}
      {full && size === "full" && <span className="text-xs text-ink-3">That’s all that fits on the box right now.</span>}
      {readout}
    </div>
  );
}
