import { useState, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { int, pct } from "@/lib/format";

export type BarRow = { key: string; label: ReactNode; value: number; title?: string; icon?: ReactNode };

/**
 * A ranked list: each row's label sits on a bar as long as its share of the
 * largest, the count and the share of the total at the right. Rows are
 * buttons when choosing one filters the page; the chosen row is marked.
 */
export function BarList({
  rows,
  total,
  onSelect,
  selected,
  mono,
  empty = "Nothing yet.",
  selectLabel = (r) => `Show only ${r.title ?? r.key}`,
  show = 10,
}: {
  rows: BarRow[];
  total: number;
  onSelect?: (key: string) => void;
  selected?: string;
  mono?: boolean;
  empty?: ReactNode;
  selectLabel?: (r: BarRow) => string;
  /** Rows shown before "Show all". */
  show?: number;
}) {
  const [all, setAll] = useState(false);
  if (rows.length === 0) return <p className="py-6 text-[0.84375rem] text-ink-3">{empty}</p>;
  const most = Math.max(...rows.map((r) => r.value), 1);
  return (
    <>
      <ul className="flex flex-col gap-0.5 py-1.5">
      {(all ? rows : rows.slice(0, show)).map((r) => {
        const on = selected === r.key;
        const inner = (
          <>
            <span className="relative min-w-0 flex-1 py-1.5 pl-2.5">
              <span
                aria-hidden
                className={cn("absolute inset-y-0 left-0 rounded-[5px] transition-[width,background-color] duration-[var(--dur-state)]", on ? "bg-data/30" : "bg-data-wash")}
                style={{ width: `${Math.max(1.5, (r.value / most) * 100)}%` }}
              />
              <span className={cn("relative flex min-w-0 items-center gap-2 text-[0.84375rem] text-ink", mono && "font-mono text-[0.78rem]")}>
                {r.icon}
                <span className="truncate" title={r.title}>
                  {r.label}
                </span>
              </span>
            </span>
            <span className="w-14 shrink-0 text-right text-[0.84375rem] text-ink tnum">{int(r.value)}</span>
            <span className="w-11 shrink-0 text-right text-[0.75rem] text-ink-3 tnum">{total ? pct(r.value / total) : ""}</span>
          </>
        );
        return (
          <li key={r.key}>
            {onSelect ? (
              <button
                type="button"
                onClick={() => onSelect(r.key)}
                aria-pressed={on}
                aria-label={`${selectLabel(r)}: ${int(r.value)}`}
                className="group flex w-full items-center gap-3 rounded-[6px] pr-1 text-left hover:bg-paper-hover"
              >
                {inner}
              </button>
            ) : (
              <div className="flex items-center gap-3 pr-1">{inner}</div>
            )}
          </li>
        );
      })}
      </ul>
      {rows.length > show && (
        <button type="button" onClick={() => setAll((x) => !x)} aria-expanded={all} className="h-7 rounded-[6px] px-2.5 text-[0.8125rem] text-ink-3 hover:bg-paper-hover hover:text-ink">
          {all ? "Show fewer" : `Show all ${rows.length}`}
        </button>
      )}
    </>
  );
}
