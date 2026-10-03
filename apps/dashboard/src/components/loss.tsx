// What an irreversible step destroys, as the box measured it when the plan
// was made: "18,204 rows in 12 tables · 41 MB". Absent when the box couldn't
// measure it (old plans, a slow disk): then only the reason is shown.
import type { Op } from "@/api/client";
import { cn } from "@/lib/cn";
import { bytes, int } from "@/lib/format";

type Loss = NonNullable<Op["loss"]>;

const plurals: Record<string, string> = { row: "rows", table: "tables", file: "files", event: "events", job: "jobs", user: "people", database: "databases" };
const singular: Record<string, string> = { user: "person" };

function countWords(n: number, unit: string, approx?: boolean) {
  const noun = n === 1 ? (singular[unit] ?? unit) : (plurals[unit] ?? `${unit}s`);
  return `${approx ? "about " : ""}${int(n)} ${noun}`;
}

/** True when nothing at all would go (an empty bucket, a queue with no jobs). */
export function lossEmpty(l: Loss) {
  return l.bytes <= 0 && (l.counts ?? []).every((c) => c.n <= 0);
}

/** The parts of a loss, each one phrase: ["18,204 rows in 12 tables", "41 MB"]. */
export function lossParts(l: Loss): string[] {
  const counts = l.counts ?? [];
  const rows = counts.find((c) => c.unit === "row");
  const tables = counts.find((c) => c.unit === "table");
  const out: string[] = [];
  for (const c of counts) {
    if (c === tables && rows) continue;
    if (c === rows && tables) out.push(`${countWords(rows.n, "row", rows.approx)} in ${countWords(tables.n, "table")}`);
    else out.push(countWords(c.n, c.unit, c.approx));
  }
  if (l.bytes > 0) out.push(bytes(l.bytes));
  return out;
}

/** "18,204 rows in 12 tables · 41 MB" for running text. */
export function lossWords(l: Loss) {
  return lossEmpty(l) ? "nothing: it’s empty" : lossParts(l).join(" · ");
}

/**
 * One irreversible step's stake: the measured quantity large, then the
 * machine's sentence for what goes. Without a measurement, just the sentence.
 */
export function LossLine({ op, reason, className }: { op: Op; reason: string; className?: string }) {
  const l = op.loss;
  return (
    <div className={cn("py-0.5", className)} data-loss={l ? "" : undefined}>
      {l &&
        (lossEmpty(l) ? (
          <p className="text-[0.9375rem] leading-[1.375rem] font-[550] text-ink">Empty: nothing in it yet.</p>
        ) : (
          <p className="text-[1.0625rem] leading-[1.5rem] font-[550] tracking-[-0.01em] text-ink tabular-nums">
            {lossParts(l).map((part, i) => (
              <span key={i}>
                {i > 0 && <span className="px-[0.45em] font-normal text-ink-3">·</span>}
                <span className="whitespace-nowrap">{part}</span>
              </span>
            ))}
          </p>
        ))}
      <p className={cn("text-[0.875rem] leading-[1.3125rem]", l ? "text-ink-2" : "text-ink")}>{reason}</p>
    </div>
  );
}
