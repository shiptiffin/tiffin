import { ChevronRight } from "lucide-react";
import { Collapsible } from "radix-ui";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";
import { count } from "@/lib/format";

/**
 * A JSON value as a tree you can fold: keys in ink, strings quieter, numbers
 * in brass, true/false/null in graphite. The first two levels start open.
 * Each object or array is a Radix Collapsible (the disclosure pattern).
 */
export function JsonTree({ value, label, className }: { value: unknown; label: string; className?: string }) {
  return (
    <div role="group" aria-label={label} className={cn("overflow-auto px-3.5 py-3 font-mono text-[0.78125rem] leading-5", className)}>
      <Node value={value} depth={0} last />
    </div>
  );
}

function Node({ name, label, value, depth, last }: { name?: ReactNode; label?: string; value: unknown; depth: number; last: boolean }) {
  const comma = last ? "" : ",";
  const key = name !== undefined && (
    <>
      {name}
      <span className="text-ink-3">: </span>
    </>
  );
  const nested = value !== null && typeof value === "object";
  if (!nested)
    return (
      <div className="pl-[18px] break-all whitespace-pre-wrap">
        {key}
        <Leaf v={value} />
        <span className="text-ink-3">{comma}</span>
      </div>
    );
  const arr = Array.isArray(value);
  const entries: Array<[string, unknown]> = arr ? (value as unknown[]).map((v, i) => [String(i), v]) : Object.entries(value as Record<string, unknown>);
  const [o, c] = arr ? ["[", "]"] : ["{", "}"];
  const size = arr ? count(entries.length, "item") : count(entries.length, "key");
  if (entries.length === 0)
    return (
      <div className="pl-[18px]">
        {key}
        <span className="text-ink-3">
          {o}
          {c}
          {comma}
        </span>
      </div>
    );
  return (
    <Collapsible.Root defaultOpen={depth < 2}>
      <Collapsible.Trigger
        className="group/node -ml-0.5 flex max-w-full items-center gap-1 rounded-[4px] pr-1 text-left outline-hidden hover:bg-paper-hover focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]"
        aria-label={`${label ?? "Value"}, ${size}`}
      >
        <ChevronRight aria-hidden className="size-3.5 shrink-0 text-ink-3 transition-transform duration-[var(--dur-state)] group-data-[state=open]/node:rotate-90" />
        <span className="min-w-0 truncate">
          {key}
          <span className="text-ink-3">{o}</span>
          <span className="text-ink-3 group-data-[state=open]/node:hidden">
            {" "}
            <span className="font-sans text-xs">{size}</span> {c}
            {comma}
          </span>
        </span>
      </Collapsible.Trigger>
      <Collapsible.Content>
        <div className="ml-[7px] border-l border-rule pl-[10px]">
          {entries.map(([k, v], i) => (
            <Node key={k} name={arr ? undefined : <span className="text-ink">{JSON.stringify(k)}</span>} label={arr ? `Item ${k}` : k} value={v} depth={depth + 1} last={i === entries.length - 1} />
          ))}
        </div>
        <div className="pl-[18px] text-ink-3">
          {c}
          {comma}
        </div>
      </Collapsible.Content>
    </Collapsible.Root>
  );
}

function Leaf({ v }: { v: unknown }) {
  if (typeof v === "string") return <span className="text-ink-2">{JSON.stringify(v)}</span>;
  if (typeof v === "number") return <span className="text-brass-ink">{String(v)}</span>;
  return <span className="text-graphite">{JSON.stringify(v)}</span>;
}
