import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";

const R = 8;
const C = 2 * Math.PI * R;

/**
 * Hold to commit: for irreversible changes when no passkey ceremony is
 * available. A ring fills over 1.2 s while you hold; let go early and it
 * drains back (interruptible), nothing happens. Keyboard: hold Enter or
 * Space. Screen-reader and switch users get "Confirm without holding",
 * which asks once more in words instead of timing a press.
 *
 *   <HoldToCommit label="Hold to drop imports" onCommit={apply} />
 *
 * Never use it for reversible changes: ceremony matches risk.
 */
export function HoldToCommit({
  label,
  doneLabel = "Committed",
  onCommit,
  duration = 1200,
  disabled,
  className,
}: {
  label: string;
  doneLabel?: string;
  onCommit: () => void;
  duration?: number;
  disabled?: boolean;
  className?: string;
}) {
  const [p, setP] = useState(0);
  const [done, setDone] = useState(false);
  const [asking, setAsking] = useState(false);
  const raf = useRef(0);
  const holding = useRef(false);
  const progress = useRef(0);
  useEffect(() => () => cancelAnimationFrame(raf.current), []);

  const finish = () => {
    setDone(true);
    holding.current = false;
    onCommit();
  };
  const start = () => {
    if (disabled || done || holding.current) return;
    holding.current = true;
    const t0 = performance.now() - progress.current * duration;
    const tick = (now: number) => {
      if (!holding.current) return;
      progress.current = Math.min(1, (now - t0) / duration);
      setP(progress.current);
      if (progress.current >= 1) return finish();
      raf.current = requestAnimationFrame(tick);
    };
    cancelAnimationFrame(raf.current);
    raf.current = requestAnimationFrame(tick);
  };
  const stop = () => {
    if (!holding.current || done) return;
    holding.current = false;
    cancelAnimationFrame(raf.current);
    const p0 = progress.current;
    const s = performance.now();
    const drain = (now: number) => {
      progress.current = Math.max(0, p0 - (now - s) / 300);
      setP(progress.current);
      if (progress.current > 0 && !holding.current) raf.current = requestAnimationFrame(drain);
    };
    raf.current = requestAnimationFrame(drain);
  };

  return (
    <span className={cn("inline-flex flex-wrap items-center gap-2", className)}>
      <button
        type="button"
        disabled={disabled}
        aria-describedby={undefined}
        className={cn(
          "inline-flex h-[38px] items-center gap-2.5 rounded-[8px] border pr-4 pl-2.5 text-[0.875rem] font-[550] whitespace-nowrap select-none",
          "transition-[background-color,border-color,color,transform] duration-[var(--dur-state)] ease-[var(--ease-out)] active:scale-[0.98]",
          "disabled:cursor-not-allowed disabled:opacity-45",
          done ? "border-danger bg-danger text-on-danger" : "border-danger-rule bg-paper-raised text-danger hover:border-danger",
        )}
        onPointerDown={(e) => {
          if (e.button === 0) {
            e.preventDefault();
            start();
          }
        }}
        onPointerUp={stop}
        onPointerLeave={stop}
        onPointerCancel={stop}
        onKeyDown={(e) => {
          if ((e.key === "Enter" || e.key === " ") && !e.repeat) {
            e.preventDefault();
            start();
          }
        }}
        onKeyUp={(e) => {
          if (e.key === "Enter" || e.key === " ") stop();
        }}
        onContextMenu={(e) => e.preventDefault()}
      >
        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" className="hold-ring" aria-hidden>
          <circle className="track" cx="10" cy="10" r={R} strokeWidth="2" />
          <circle className="fill" cx="10" cy="10" r={R} strokeWidth="2" strokeLinecap="round" strokeDasharray={C} strokeDashoffset={C * (1 - p)} />
        </svg>
        <span>{done ? doneLabel : label}</span>
        <span className="sr-only">: press and hold for {(duration / 1000).toFixed(1)} seconds</span>
      </button>
      {!done && !asking && (
        <button
          type="button"
          disabled={disabled}
          className="sr-only text-sm text-ink-3 underline underline-offset-4 focus:not-sr-only"
          onClick={() => setAsking(true)}
        >
          Confirm without holding
        </button>
      )}
      {!done && asking && (
        <span role="group" aria-label="Confirm" className="inline-flex items-center gap-2 text-sm text-ink-2">
          Sure? This can’t be undone.
          <button type="button" className="font-[550] text-danger underline underline-offset-4" onClick={finish} autoFocus>
            Yes, do it
          </button>
          <button type="button" className="text-ink-3 underline underline-offset-4" onClick={() => setAsking(false)}>
            No
          </button>
        </span>
      )}
    </span>
  );
}
