// A payload, an output or an input, as pretty JSON: keys in ink, values a
// step quieter, numbers and literals in brass. One accent, no rainbow. It
// copies, says its size, and folds when it's long. Apps wrote it, so it is
// plain text inside an Untrusted frame.
import { useMemo, useState, type ReactNode } from "react";
import { CopyButton } from "@/components/copy";
import { bytes } from "@/lib/format";
import { cn } from "@/lib/cn";

const TOKEN = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|([{}[\],])/g;

/** Colours one pretty-printed JSON text: returns React nodes, never HTML. */
function paint(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let k = 0;
  for (const m of text.matchAll(TOKEN)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    if (m[1] && m[2]) {
      out.push(
        <span key={k++} className="text-ink">
          {m[1]}
        </span>,
        <span key={k++} className="text-ink-4">
          {m[2]}
        </span>,
      );
    } else if (m[1]) {
      out.push(
        <span key={k++} className="text-ink-2">
          {m[1]}
        </span>,
      );
    } else if (m[3] || m[4]) {
      out.push(
        <span key={k++} className="text-brass-ink">
          {m[3] ?? m[4]}
        </span>,
      );
    } else {
      out.push(
        <span key={k++} className="text-ink-4">
          {m[5]}
        </span>,
      );
    }
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

const FOLD = 24; // lines shown before "Show all"

/**
 * Pretty JSON with a header: a title, its size, and Copy. `value` undefined
 * reads "Nothing", which is different from null.
 */
export function JsonView({ title, value, note, className, empty = "Nothing was sent." }: { title: string; value: unknown; note?: string; className?: string; empty?: string }) {
  const text = useMemo(() => (value === undefined ? "" : typeof value === "string" ? value : JSON.stringify(value, null, 2)), [value]);
  const lines = text ? text.split("\n").length : 0;
  const [open, setOpen] = useState(false);
  const folded = lines > FOLD && !open;
  const shown = folded ? text.split("\n").slice(0, FOLD).join("\n") : text;
  const painted = useMemo(() => (typeof value === "string" ? [shown] : paint(shown)), [shown, value]);
  return (
    <section className={cn("min-w-0 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk", className)} aria-label={title}>
      <header className="flex items-center gap-2 border-b border-rule py-1 pr-1 pl-3.5">
        <h3 className="text-[0.8125rem] font-[550] text-ink">{title}</h3>
        {text && <span className="text-xs text-ink-3 tnum">{bytes(new TextEncoder().encode(text).length, 1)}</span>}
        {note && <span className="hidden min-w-0 truncate text-xs text-ink-3 sm:inline">· {note}</span>}
        <span className="ml-auto" />
        {text && <CopyButton value={text} label={`Copy the ${title.toLowerCase()}`} />}
      </header>
      {text ? (
        <div className="relative">
          <pre className="max-h-[28rem] overflow-auto px-3.5 py-3 font-mono text-[0.75rem] leading-5 text-ink-2" tabIndex={0}>
            <code>{painted}</code>
          </pre>
          {lines > FOLD && (
            <div className={cn("flex justify-center border-t border-rule py-1.5", folded && "bg-paper-sunk")}>
              <button type="button" aria-expanded={!folded} onClick={() => setOpen((o) => !o)} className="h-6 rounded-[6px] px-2 text-xs font-[550] text-ink-3 hover:bg-paper-hover hover:text-ink">
                {folded ? `Show all ${lines} lines` : "Show less"}
              </button>
            </div>
          )}
        </div>
      ) : (
        <p className="px-3.5 py-3 text-[0.8125rem] text-ink-3">{empty}</p>
      )}
    </section>
  );
}
