import { ChevronDown } from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/lib/cn";

/**
 * Small pieces shared by the data tiers (Data, Storage, Email, Key-value).
 * Composed from the Fusion rules: a label above a group, rows on the page
 * with hairlines, readings with a small unit.
 */

/** A group on the page: a small-caps label (and an optional aside on the right), then its rows. */
export function Section({
  id,
  label,
  aside,
  children,
  className,
}: {
  id?: string;
  label: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("min-w-0", className)} aria-labelledby={id}>
      <div className="mb-2.5 flex min-h-5 items-baseline justify-between gap-4">
        <h2 id={id} className="label">
          {label}
        </h2>
        {aside && <div className="min-w-0 truncate text-sm text-ink-3">{aside}</div>}
      </div>
      {children}
    </section>
  );
}

/** Rows on the page: hairline above, between and below. No box. */
export function Rows({ children, className, as: As = "ul" }: { children: ReactNode; className?: string; as?: "ul" | "div" | "ol" }) {
  return <As className={cn("divide-y divide-rule border-y border-rule", className)}>{children}</As>;
}

/** A labelled reading: LABEL, then a big tabular value with a small unit, then a quiet line. */
export function Reading({
  label,
  value,
  unit,
  sub,
  children,
  className,
}: {
  label: ReactNode;
  value: ReactNode;
  unit?: ReactNode;
  sub?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("min-w-0", className)}>
      <p className="label">{label}</p>
      <p className="reading mt-0.5 text-ink">
        {value}
        {unit && <span className="u text-[0.8125rem] text-ink-3">&#8239;{unit}</span>}
      </p>
      {children}
      {sub && <p className="mt-2 text-xs text-ink-3">{sub}</p>}
    </div>
  );
}

/** A row of readings under a page header, with a hairline under it. */
export function Readings({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("mt-8 grid gap-x-10 gap-y-6 border-b border-rule pb-7", className)}>{children}</div>;
}

/** A proportional bar of parts (like the Box's memory bar): each part a share of the whole, the rest dashed. */
export function PartsBar({
  parts,
  total,
  label,
  className,
}: {
  parts: Array<{ key: string; value: number; tone: string; name: string }>;
  total: number;
  label: string;
  className?: string;
}) {
  const shown = parts.filter((p) => p.value > 0);
  return (
    <div className={cn("min-w-0", className)}>
      <div className="flex h-2.5 gap-0.5" role="img" aria-label={label}>
        {shown.map((p) => (
          <span
            key={p.key}
            className="h-full min-w-[3px] rounded-[2px]"
            style={{ width: `${(p.value / Math.max(total, 1)) * 100}%`, background: p.tone }}
          />
        ))}
        {shown.reduce((n, p) => n + p.value, 0) < total * 0.995 && (
          <span className="h-full flex-1 rounded-[2px] border border-dashed border-rule-3" />
        )}
      </div>
      <p className="mt-2 flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-ink-3">
        {shown.map((p) => (
          <span key={p.key} className="inline-flex items-center gap-1.5">
            <i className="inline-block size-[7px] rounded-[1.5px]" style={{ background: p.tone }} />
            {p.name}
          </span>
        ))}
      </p>
    </div>
  );
}

/** A quiet type word in mono, no fill: "hash", "zset", "jsonb". */
export function TypeWord({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[0.71875rem] tracking-[-0.01em] text-ink-3", className)}>{children}</span>;
}

/** Turns a JSON value into readable one-line text: {"sku": "BWL-02", "qty": 2}. */
export function jsonLine(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(jsonLine).join(", ")}]`;
  if (v && typeof v === "object") {
    return `{${Object.entries(v as Record<string, unknown>)
      .map(([k, x]) => `${JSON.stringify(k)}: ${jsonLine(x)}`)
      .join(", ")}}`;
  }
  return v === undefined ? "null" : JSON.stringify(v);
}

/** A compact native select with a quiet chevron (keeps the platform's own menu, keyboard and a11y). */
export function MiniSelect({ className, children, ...props }: ComponentProps<"select">) {
  return (
    <span className={cn("relative inline-flex", className)}>
      <select
        {...props}
        className="h-7 w-full appearance-none rounded-[6px] border border-rule-2 bg-paper-raised pr-7 pl-2 font-mono text-xs text-ink shadow-[var(--top-light)] outline-none transition-colors hover:border-rule-3 focus-visible:border-brass"
      >
        {children}
      </select>
      <ChevronDown aria-hidden className="pointer-events-none absolute top-1/2 right-2 size-3.5 -translate-y-1/2 text-ink-3" />
    </span>
  );
}
