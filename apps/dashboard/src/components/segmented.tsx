import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * A horizontal choice of a few options (periods, ranges): a radio group,
 * arrow keys move and choose, the chosen one raised. A short label on
 * phones when one is given.
 */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
  className,
}: {
  label: string;
  value: T | undefined;
  options: Array<{ value: T; label: ReactNode; short?: ReactNode; name?: string }>;
  onChange: (v: T) => void;
  className?: string;
}) {
  const i = options.findIndex((o) => o.value === value);
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn("inline-flex shrink-0 rounded-[8px] border border-rule-2 bg-paper-sunk p-0.5", className)}
      onKeyDown={(e) => {
        const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
        if (!d) return;
        e.preventDefault();
        const next = options[(Math.max(0, i) + d + options.length) % options.length];
        onChange(next.value);
        const el = e.currentTarget.querySelector<HTMLElement>(`[data-value="${CSS.escape(next.value)}"]`);
        el?.focus();
      }}
    >
      {options.map((o, k) => {
        const on = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={on}
            aria-label={o.name}
            data-value={o.value}
            tabIndex={on || (i < 0 && k === 0) ? 0 : -1}
            onClick={() => onChange(o.value)}
            className={cn(
              "h-7 rounded-[6px] px-2.5 text-[0.8125rem] whitespace-nowrap text-ink-3 transition-colors duration-[var(--dur-state)] hover:text-ink sm:px-3",
              on && "bg-paper-raised font-[550] text-ink shadow-[0_1px_2px_oklch(0.3_0.02_60/0.12)] ring-1 ring-rule-2",
            )}
          >
            {o.short ? (
              <>
                <span className="sm:hidden">{o.short}</span>
                <span className="max-sm:hidden">{o.label}</span>
              </>
            ) : (
              o.label
            )}
          </button>
        );
      })}
    </div>
  );
}
