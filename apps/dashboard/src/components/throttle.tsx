import { useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";

/** Instance counts an app can snap to (the manifest allows 1–16). */
export const INSTANCE_STOPS = [1, 2, 3, 4, 6, 8, 12, 16];

/**
 * A throttle with detents, for scale. It snaps to meaningful stops, shows
 * the live position dashed while a new one is staged, and can't enter the
 * "won't fit" zone past what the box has room for. Moving it never applies
 * anything: `onChange` fires while you drag (drive a live readout with it),
 * `onCommit` when you let go (stage the change there).
 *
 *   <Throttle label="web instances" stops={INSTANCE_STOPS} value={3} applied={2} maxFit={6}
 *     onChange={setPreview} onCommit={(n) => stage(...)} readout={<Readout n={preview} />} />
 *
 * Keyboard: ←/→ (or ↓/↑) move one detent, Home/End go to the ends that fit.
 * The mini size sits in a Stack row; the full size has a printed scale.
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
  unit = "instances",
  readout,
  format = int,
  printed,
  className,
}: {
  label: string;
  stops: number[];
  /** The position shown (staged or live). */
  value: number;
  /** The live value. When different from `value`, it is drawn dashed. */
  applied?: number;
  /** The largest stop that fits on the box; larger stops are the won't-fit zone. */
  maxFit?: number;
  onChange?: (n: number) => void;
  onCommit?: (n: number) => void;
  size?: "full" | "mini";
  unit?: string;
  /** Shown under a full throttle; update it from onChange. */
  readout?: ReactNode;
  /** How a stop is printed and read aloud (default: the number). E.g. a 0 stop that means "no limit". */
  format?: (n: number) => string;
  /**
   * A legend printed under a mini throttle, as on a panel ("2 instances"): what
   * the lever sets, at its current position. Brass while a move is staged.
   */
  printed?: (n: number) => string;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [drag, setDrag] = useState<number | null>(null);
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
  const idx = drag ?? indexOf(value);
  const pos = (i: number) => `${(i / (n - 1)) * 100}%`;
  const staged = applied !== undefined && stops[idx] !== applied;

  const fromX = (x: number) => {
    const r = ref.current!.getBoundingClientRect();
    const f = Math.max(0, Math.min(1, (x - r.left) / r.width));
    return Math.min(Math.round(f * (n - 1)), lastFit);
  };
  const move = (i: number) => {
    if (i === idx) return;
    setDrag(i);
    onChange?.(stops[i]);
  };
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.stopPropagation();
    if (e.button !== 0) return;
    ref.current!.setPointerCapture(e.pointerId);
    const i = fromX(e.clientX);
    setDrag(i);
    onChange?.(stops[i]);
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    if (drag === null || !ref.current!.hasPointerCapture(e.pointerId)) return;
    move(fromX(e.clientX));
  };
  const onPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    if (drag === null) return;
    ref.current!.releasePointerCapture(e.pointerId);
    const v = stops[drag];
    setDrag(null);
    onCommit?.(v);
  };
  const onKey = (e: KeyboardEvent) => {
    const step: Record<string, number> = { ArrowRight: 1, ArrowUp: 1, ArrowLeft: -1, ArrowDown: -1 };
    let i: number;
    if (e.key in step) i = Math.max(0, Math.min(lastFit, idx + step[e.key]));
    else if (e.key === "Home") i = 0;
    else if (e.key === "End") i = lastFit;
    else return;
    e.preventDefault();
    e.stopPropagation();
    if (i !== idx) {
      onChange?.(stops[i]);
      onCommit?.(stops[i]);
    }
  };

  const nofitFrom = lastFit < n - 1 ? ((lastFit + 0.5) / (n - 1)) * 100 : null;
  return (
    <div className={cn(printed && "flex flex-col items-start gap-[3px]", className)}>
      <div
        ref={ref}
        className="throttle"
        data-size={size}
        data-dragging={drag !== null ? "" : undefined}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={() => setDrag(null)}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="th-track" />
        <div className="th-fill" style={{ width: pos(idx) }} />
        {nofitFrom !== null && (
          <div className="th-nofit" style={{ left: `${nofitFrom}%` }} aria-hidden>
            <span>won’t fit</span>
          </div>
        )}
        {stops.map((s, i) => (
          <span
            key={s}
            className="th-stop"
            style={{ left: pos(i) }}
            data-active={i === idx ? "" : undefined}
            data-nofit={i > lastFit ? "" : undefined}
            aria-hidden
          >
            <i />
            <b>{format(s)}</b>
          </span>
        ))}
        {staged && <span className="th-was" style={{ left: pos(indexOf(applied!)) }} aria-hidden />}
        <span
          role="slider"
          tabIndex={0}
          aria-label={label}
          aria-valuemin={stops[0]}
          aria-valuemax={stops[lastFit]}
          aria-valuenow={stops[idx]}
          aria-valuetext={`${format(stops[idx])} ${unit}${staged ? `, staged (now ${format(applied!)})` : ""}`}
          className={cn("th-knob")}
          data-staged={staged ? "" : undefined}
          style={{ left: pos(idx) }}
          onKeyDown={onKey}
        />
      </div>
      {printed && (
        <span aria-hidden className="throttle-print" data-staged={staged ? "" : undefined}>
          {printed(stops[idx])}
        </span>
      )}
      {readout}
    </div>
  );
}
