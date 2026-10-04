import { Check } from "lucide-react";
import { useState, type ReactNode } from "react";
import { CopyButton } from "@/components/copy";
import { cn } from "@/lib/cn";
import { HOSTS } from "@/lib/domains";
import { relative } from "@/lib/time";

export type Row = { type: string; name: string; host?: string; value: string; ok?: boolean };

/**
 * The records to add, exactly as a DNS panel wants them: type, name, value,
 * each value with a copy button. With `ok`, a row that DNS already answers
 * gets a tick. On a phone each record is a small block instead of a row.
 */
export function RecordsTable({ records, className }: { records: Row[]; className?: string }) {
  const showOk = records.some((r) => r.ok !== undefined);
  // The same name and type twice (an A and an AAAA under @) reads better adjacent; keep the box's order.
  return (
    <div className={cn("overflow-hidden rounded-[10px] border border-rule-2 bg-paper-raised", className)} role="table" aria-label="Records to add">
      <div role="row" className="hidden grid-cols-[3.75rem_6.5rem_minmax(0,1fr)_auto] gap-x-4 border-b border-rule bg-paper-sunk px-3.5 py-1.5 text-xs text-ink-3 sm:grid">
        <span role="columnheader">Type</span>
        <span role="columnheader">Name</span>
        <span role="columnheader">Value</span>
        <span role="columnheader" className="w-[4rem] text-right">{showOk ? "In DNS" : ""}</span>
      </div>
      <div className="divide-y divide-rule">
        {records.map((r, i) => (
          <div
            key={i}
            role="row"
            className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 px-3.5 py-2 sm:grid-cols-[3.75rem_6.5rem_minmax(0,1fr)_auto]"
          >
            <span role="cell" className="ident text-[0.8125rem] text-ink max-sm:col-start-1 max-sm:row-start-1">
              <span className="sm:hidden">
                {r.type} <span className="text-ink-4">·</span>{" "}
              </span>
              <span className="max-sm:hidden">{r.type}</span>
              <span className="sm:hidden">{r.host || r.name}</span>
            </span>
            <span role="cell" className="flex min-w-0 items-center gap-0.5 max-sm:hidden">
              <code className="ident truncate text-[0.8125rem] text-ink" title={r.name}>
                {r.host || r.name}
              </code>
              <CopyButton value={r.host || r.name} label={`Copy the name ${r.host || r.name}`} className="size-6" />
            </span>
            <span role="cell" className="flex min-w-0 items-center gap-0.5 max-sm:col-span-2 max-sm:row-start-2">
              <code className="ident min-w-0 text-[0.8125rem] break-all text-ink">{r.value}</code>
              <CopyButton value={r.value} label={`Copy the value ${r.value}`} className="size-6" />
            </span>
            <span role="cell" className="w-[4rem] text-right text-xs max-sm:col-start-2 max-sm:row-start-1">
              {r.ok === true ? (
                <span className="inline-flex items-center gap-1 text-ok">
                  <Check className="size-3.5" strokeWidth={2.5} /> Found
                </span>
              ) : r.ok === false ? (
                <span className="text-ink-3">Not yet</span>
              ) : null}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

/** Where to add them: pick your DNS host, get one line. */
export function HostHints({ className }: { className?: string }) {
  const [at, setAt] = useState<string | null>(null);
  const hint = HOSTS.find((h) => h.name === at);
  return (
    <div className={cn("text-[0.8125rem]", className)}>
      <div className="flex flex-wrap items-center gap-x-1 gap-y-1">
        <span className="mr-1 text-ink-3">Where to add them:</span>
        {HOSTS.map((h) => (
          <button
            key={h.name}
            type="button"
            aria-pressed={at === h.name}
            onClick={() => setAt(at === h.name ? null : h.name)}
            className={cn(
              "h-6 rounded-[6px] px-2 text-ink-2 transition-colors hover:bg-paper-sunk hover:text-ink",
              at === h.name && "bg-paper-sunk font-[550] text-ink",
            )}
          >
            {h.name}
          </button>
        ))}
      </div>
      {hint && <p className="mt-1.5 text-ink-2">{hint.how}</p>}
    </div>
  );
}

/**
 * The calm part while DNS catches up: a small spinner, one sentence, when it
 * last looked, and Check now.
 */
export function Watching({ checkedAt, checking, onCheck, children }: { checkedAt?: string; checking?: boolean; onCheck?: () => void; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[0.8125rem] text-ink-2" role="status">
      <span className="inline-flex items-center gap-2">
        <span className="spinner text-brass" aria-hidden />
        {children ?? "Watching for your records. New ones usually show up within a few minutes."}
      </span>
      <span className="inline-flex items-center gap-2 text-ink-3">
        {checkedAt && <span>Looked {relative(checkedAt)}</span>}
        {onCheck && (
          <button type="button" onClick={onCheck} disabled={checking} className="font-[550] text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink disabled:opacity-50">
            {checking ? "Checking…" : "Check now"}
          </button>
        )}
      </span>
    </div>
  );
}
