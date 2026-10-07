import { Check, ChevronDown, Clock, X } from "lucide-react";
import { DropdownMenu as M, Popover as P } from "radix-ui";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { absRange, durMs, PRESETS, rangeWords, type Range } from "@/components/logs-query";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";

const chip =
  "inline-flex h-8 max-w-full items-center rounded-[7px] border text-[0.8125rem] transition-colors duration-[var(--dur-state)] focus-visible:outline-offset-2";
const menu = "z-50 max-h-[min(60vh,26rem)] min-w-56 overflow-y-auto rounded-lg border border-rule bg-paper-raised p-1 shadow-raised data-[state=open]:animate-pop";
const item =
  "relative flex h-8 cursor-default items-center gap-2 rounded-md pr-2 pl-7 text-[0.84375rem] text-ink-2 outline-hidden select-none data-[disabled]:opacity-50 data-[highlighted]:bg-paper-hover data-[highlighted]:text-ink";

export type FacetOption = { v: string; label: ReactNode; hint?: string; mono?: boolean };

/**
 * One filter as a chip: "App" while it lets everything through, "App web,
 * api" once set, × clears it. The menu lists the choices with how many
 * lines each has in the range (loaded when it opens).
 */
export function FacetChip({
  label,
  options,
  value,
  onChange,
  single,
  counts,
  onOpenChange,
  allLabel,
}: {
  label: string;
  options: FacetOption[];
  value: string[];
  onChange: (v: string[]) => void;
  single?: boolean;
  counts?: Record<string, number> | null;
  onOpenChange?: (open: boolean) => void;
  allLabel: string;
}) {
  const on = value.length > 0;
  const names = value.map((v) => options.find((o) => o.v === v)?.label ?? v);
  const toggle = (v: string) => onChange(single ? (value[0] === v ? [] : [v]) : value.includes(v) ? value.filter((x) => x !== v) : [...value, v]);
  return (
    <span className={cn(chip, on ? "border-rule-3 bg-paper-raised text-ink shadow-[var(--top-light)]" : "border-dashed border-rule-2 text-ink-2 hover:border-rule-3 hover:text-ink")}>
      <M.Root onOpenChange={onOpenChange}>
        <M.Trigger asChild>
          <button type="button" className={cn("flex h-full min-w-0 items-center gap-1.5 pl-2.5 outline-hidden", on ? "pr-1" : "pr-2")} aria-label={on ? `${label}: ${value.join(", ")}. Change` : `Filter by ${label.toLowerCase()}`}>
            <span className={on ? "text-ink-3" : undefined}>{label}</span>
            {on && (
              <span className="max-w-[12rem] truncate font-[550]">
                {names.length > 2 ? (
                  <>
                    {names[0]} <span className="text-ink-3">+{names.length - 1}</span>
                  </>
                ) : (
                  names.map((n, i) => (
                    <span key={i}>
                      {i > 0 && <span className="text-ink-3">, </span>}
                      {n}
                    </span>
                  ))
                )}
              </span>
            )}
            {!on && <ChevronDown className="size-3.5 text-ink-3" aria-hidden />}
          </button>
        </M.Trigger>
        <M.Portal>
          <M.Content align="start" sideOffset={6} className={menu} onCloseAutoFocus={(e) => single || e.preventDefault()}>
            <M.Label className="flex items-center justify-between px-2 pt-1.5 pb-1 text-xs text-ink-3">
              <span>{label}</span>
              {counts === undefined ? null : counts === null ? <span>Counting…</span> : <span>Lines in range</span>}
            </M.Label>
            <M.Item className={item} onSelect={() => onChange([])}>
              {!on && <Check className="absolute left-2 size-3.5 text-ink" aria-hidden />}
              {allLabel}
            </M.Item>
            <M.Separator className="-mx-1 my-1 h-px bg-rule" />
            {options.map((o) => (
              <M.CheckboxItem
                key={o.v}
                checked={value.includes(o.v)}
                onCheckedChange={() => toggle(o.v)}
                onSelect={(e) => single || e.preventDefault()}
                className={item}
              >
                <M.ItemIndicator className="absolute left-2">
                  <Check className="size-3.5 text-ink" aria-hidden />
                </M.ItemIndicator>
                <span className={cn("min-w-0 flex-1 truncate", o.mono && "font-mono text-[0.8125rem]")}>
                  {o.label}
                  {o.hint && <span className="ml-2 text-xs text-ink-4 max-sm:hidden">{o.hint}</span>}
                </span>
                {counts && <span className={cn("text-xs tnum", counts[o.v] ? "text-ink-3" : "text-ink-4")}>{counts[o.v] === undefined && o.v === "build" ? "" : int(counts[o.v] ?? 0)}</span>}
              </M.CheckboxItem>
            ))}
          </M.Content>
        </M.Portal>
      </M.Root>
      {on && (
        <button
          type="button"
          aria-label={`Clear the ${label.toLowerCase()} filter`}
          onClick={() => onChange([])}
          className="grid h-full w-7 shrink-0 place-items-center rounded-r-[6px] text-ink-3 hover:bg-paper-hover hover:text-ink"
        >
          <X className="size-3.5" />
        </button>
      )}
    </span>
  );
}

const local = (t: number) => {
  const d = new Date(t - new Date(t).getTimezoneOffset() * 60_000);
  return d.toISOString().slice(0, 16);
};

/**
 * The time range: a quick pick of recent windows (up to what the box
 * keeps), or exact times. A zoomed range shows its times and offers to zoom
 * out or go back to now.
 */
export function RangePicker({
  range,
  onChange,
  maxDays,
  disabled,
}: {
  range: Range;
  onChange: (since: string) => void;
  maxDays: number;
  disabled?: boolean;
}) {
  const [custom, setCustom] = useState(false);
  const presets = PRESETS.filter((p) => durMs(p) <= maxDays * 86_400_000 || p === "1h");
  const words = rangeWords(range);
  const abs = range.kind === "abs";
  return (
    <P.Root open={custom} onOpenChange={setCustom}>
      <M.Root>
        <P.Anchor asChild>
          <span className={cn(chip, "border-rule-2 bg-paper-raised text-ink shadow-[var(--top-light)]", disabled && "opacity-50")}>
            <M.Trigger asChild disabled={disabled}>
              <button type="button" className={cn("flex h-full min-w-0 items-center gap-1.5 pl-2.5 outline-hidden", abs ? "pr-1" : "pr-2")} aria-label={`Time range: ${words}. Change`}>
                <Clock className="size-3.5 shrink-0 text-ink-3" aria-hidden />
                <span className="truncate font-[550] tnum">{disabled ? "Last 15 minutes" : words}</span>
                {!abs && <ChevronDown className="size-3.5 text-ink-3" aria-hidden />}
              </button>
            </M.Trigger>
            {abs && !disabled && (
              <button
                type="button"
                aria-label="Back to the last hour"
                onClick={() => onChange("1h")}
                className="grid h-full w-7 shrink-0 place-items-center rounded-r-[6px] text-ink-3 hover:bg-paper-hover hover:text-ink"
              >
                <X className="size-3.5" />
              </button>
            )}
          </span>
        </P.Anchor>
        <M.Portal>
          <M.Content align="start" sideOffset={6} className={menu}>
            {abs && (
              <>
                <M.Item
                  className={item}
                  onSelect={() => {
                    const span = range.to - range.from;
                    const to = Math.min(Date.now(), range.to + span);
                    onChange(absRange(Math.max(to - span * 3, Date.now() - maxDays * 86_400_000), to));
                  }}
                >
                  Zoom out
                </M.Item>
                <M.Separator className="-mx-1 my-1 h-px bg-rule" />
              </>
            )}
            {presets.map((p) => (
              <M.Item key={p} className={item} onSelect={() => onChange(p)}>
                {range.kind === "rel" && range.since === p && <Check className="absolute left-2 size-3.5 text-ink" aria-hidden />}
                <span className="flex-1">{rangeWords({ kind: "rel", since: p, ms: durMs(p) })}</span>
                <span className="font-mono text-xs text-ink-4">{p}</span>
              </M.Item>
            ))}
            <M.Separator className="-mx-1 my-1 h-px bg-rule" />
            <M.Item className={item} onSelect={() => setCustom(true)}>
              {abs && <Check className="absolute left-2 size-3.5 text-ink" aria-hidden />}
              Exact times…
            </M.Item>
          </M.Content>
        </M.Portal>
      </M.Root>
      <P.Portal>
        <P.Content align="start" sideOffset={6} className="z-50 w-[19rem] rounded-lg border border-rule bg-paper-raised p-4 shadow-raised data-[state=open]:animate-pop">
          <ExactTimes range={range} maxDays={maxDays} onDone={(s) => (setCustom(false), onChange(s))} />
        </P.Content>
      </P.Portal>
    </P.Root>
  );
}

function ExactTimes({ range, maxDays, onDone }: { range: Range; maxDays: number; onDone: (since: string) => void }) {
  const [now] = useState(() => Date.now());
  const [from, setFrom] = useState(() => local(range.kind === "abs" ? range.from : now - range.ms));
  const [to, setTo] = useState(() => local(range.kind === "abs" ? range.to : now));
  const a = new Date(from).getTime();
  const b = new Date(to).getTime();
  const bad = !(a < b) ? "The start must be before the end." : a < now - maxDays * 86_400_000 - 60_000 ? `Logs are kept for ${maxDays} days.` : "";
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!bad) onDone(absRange(a, b));
      }}
      className="grid gap-3"
    >
      <p className="text-[0.84375rem] font-[550] text-ink">Exact times</p>
      <div className="grid gap-1.5">
        <Label htmlFor="logs-from">From</Label>
        <Input id="logs-from" type="datetime-local" value={from} max={to} onChange={(e) => setFrom(e.target.value)} className="tnum" />
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="logs-to">To</Label>
        <Input id="logs-to" type="datetime-local" value={to} min={from} onChange={(e) => setTo(e.target.value)} className="tnum" />
      </div>
      {bad && <p className="text-xs text-danger">{bad}</p>}
      <div className="flex justify-end gap-2">
        <P.Close asChild>
          <Button type="button" variant="ghost" size="sm">
            Cancel
          </Button>
        </P.Close>
        <Button type="submit" variant="primary" size="sm" disabled={!!bad}>
          Show these times
        </Button>
      </div>
    </form>
  );
}
