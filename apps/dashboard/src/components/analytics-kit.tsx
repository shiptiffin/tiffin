import { ListFilter, Search } from "lucide-react";
import { Tabs } from "radix-ui";
import { useId, useMemo, useState, type ReactNode } from "react";
import { CopyButton } from "@/components/copy";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { cn } from "@/lib/cn";
import { dec, int, MINUS, NNBSP, pct } from "@/lib/format";
import type { FilterKey } from "@/routes/analytics-search";

/**
 * The Analytics page's shared parts: how values read (countries by name,
 * visit lengths in minutes), a panel with tabs, the ranked rows that filter
 * the page, the "all rows" dialog with a search, and a code box.
 */

const regions = typeof Intl.DisplayNames === "function" ? new Intl.DisplayNames(["en-GB"], { type: "region" }) : null;
export const countryName = (code: string) => {
  try {
    return (code && regions?.of(code)) || code || "Unknown";
  } catch {
    return code;
  }
};

/** How a filter's value reads: countries by name, devices capitalised. */
export function shown(key: FilterKey, v: string) {
  if (key === "country") return countryName(v);
  if (key === "device") return v.charAt(0).toUpperCase() + v.slice(1);
  if (key === "os" && v === "Mac OS X") return "macOS";
  return v;
}
export const isPath = (key: FilterKey) => key === "page" || key === "entry" || key === "exit";

/** 61.8 → "1 min 2 s", 9 → "9 s", 754 → "13 min". */
export function visitLength(s: number) {
  if (!Number.isFinite(s)) return "–";
  if (!s) return `0${NNBSP}s`;
  if (s < 60) return `${int(s)}${NNBSP}s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m >= 10 || rest === 0) return `${int(s / 60)}${NNBSP}min`;
  return `${m}${NNBSP}min ${rest}${NNBSP}s`;
}

/** Below this many visitors in the period before, a percentage change is noise, not news. */
export const MIN_BASELINE = 20;

export type Delta = { text: string; dir: -1 | 0 | 1 };
/** A change against a real baseline, or nothing at all. Points for rates, percent for counts. */
export function change(v: number, prev: number, base: number, points?: boolean): Delta | null {
  if (!(base >= MIN_BASELINE)) return null;
  if (points) {
    const d = Math.round((v - prev) * 100);
    return d === 0 ? { text: "0 pts", dir: 0 } : { text: `${d > 0 ? "+" : MINUS}${Math.abs(d)} pts`, dir: d > 0 ? 1 : -1 };
  }
  if (!prev) return null;
  const r = (v - prev) / prev;
  if (Math.abs(r) < 0.005) return { text: "0%", dir: 0 };
  if (r >= 2) return { text: `${dec(v / prev, 1)}×`, dir: 1 };
  return { text: `${r > 0 ? "+" : MINUS}${pct(Math.abs(r))}`, dir: r > 0 ? 1 : -1 };
}

/** A bordered panel: a header row (title or tabs, a column name), then the body. */
export function Panel({ id, title, tabs, tab = 0, onTab, right, children, className }: {
  id: string;
  title: string;
  tabs?: string[];
  tab?: number;
  onTab?: (i: number) => void;
  right?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const box = cn("flex min-w-0 flex-col rounded-[12px] border border-rule-2 bg-paper-raised shadow-[var(--top-light)]", className);
  const aside = right !== undefined && <span className="flex h-11 shrink-0 items-center text-[0.75rem] text-ink-3">{right}</span>;
  if (!tabs || tabs.length < 2) {
    return (
      <section aria-labelledby={id} className={box}>
        <div className="flex min-h-11 items-end justify-between gap-x-4 border-b border-rule px-4">
          <h2 id={id} className="flex h-11 items-center text-[0.84375rem] font-[550] text-ink">
            {title}
          </h2>
          {aside}
        </div>
        <div className="flex min-h-0 flex-1 flex-col">{children}</div>
      </section>
    );
  }
  // Radix Tabs: arrow keys, Home and End move between the views; only the chosen view is rendered.
  return (
    <section aria-labelledby={id} className={box}>
      <Tabs.Root value={String(tab)} onValueChange={(v) => onTab?.(Number(v))} className="flex min-h-0 flex-1 flex-col">
        <div className="flex min-h-11 items-end justify-between gap-x-4 border-b border-rule px-4">
          <h2 id={id} className="sr-only">
            {title}
          </h2>
          <Tabs.List aria-label={title} className="-mb-px flex min-w-0 gap-x-4 overflow-x-auto [scrollbar-width:none]">
            {tabs.map((label, k) => (
              <Tabs.Trigger
                key={label}
                value={String(k)}
                className="relative h-11 shrink-0 text-[0.84375rem] whitespace-nowrap text-ink-3 transition-colors duration-[var(--dur-state)] after:absolute after:inset-x-0 after:-bottom-px after:h-[2px] after:rounded-full hover:text-ink data-[state=active]:font-[550] data-[state=active]:text-ink data-[state=active]:after:bg-ink"
              >
                {label}
              </Tabs.Trigger>
            ))}
          </Tabs.List>
          {aside}
        </div>
        <Tabs.Content value={String(tab)} className="flex min-h-0 flex-1 flex-col focus-visible:outline-offset-[-2px]">
          {children}
        </Tabs.Content>
      </Tabs.Root>
    </section>
  );
}

export type Row = { key: string; label: ReactNode; value: number; title: string; icon?: ReactNode; extra?: number };

/**
 * Ranked rows: a bar as long as the row's share of the largest, the count and
 * its share of the total. Each row filters the page; the chosen row is marked
 * in brass, and choosing it again removes the filter.
 */
export function Rows({
  rows,
  total,
  selected,
  onSelect,
  mono,
  show,
  what,
  extraName,
}: {
  rows: Row[];
  total: number;
  selected?: string;
  onSelect?: (key: string) => void;
  mono?: boolean;
  show?: number;
  /** What a row is, for its accessible name ("page", "country"). */
  what: string;
  /** A second number per row (page views), shown when given. */
  extraName?: string;
}) {
  const most = Math.max(1, ...rows.map((r) => r.value));
  const list = show ? rows.slice(0, show) : rows;
  return (
    <ul className="flex flex-col gap-px">
      {list.map((r) => {
        const on = selected === r.key;
        const body = (
          <>
            <span className="relative flex h-8 min-w-0 flex-1 items-center pl-2.5">
              <span
                aria-hidden
                className={cn("absolute inset-y-0.5 left-0 rounded-[5px] transition-[width,background-color] duration-[var(--dur-state)]", on ? "bg-data/25 ring-1 ring-data/70" : "bg-data-wash group-hover:bg-data/25")}
                style={{ width: `${Math.max(1.5, (r.value / most) * 100)}%` }}
              />
              <span className={cn("relative flex min-w-0 items-center gap-2 text-[0.84375rem] text-ink", mono && "font-mono text-[0.78rem]")}>
                {r.icon}
                <span className="truncate">{r.label}</span>
              </span>
              {onSelect && <ListFilter aria-hidden className="relative ml-1.5 size-3.5 shrink-0 text-ink-3 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100" />}
            </span>
            {extraName && <span className="w-16 shrink-0 text-right text-[0.84375rem] text-ink-2 tnum">{int(r.extra ?? 0)}</span>}
            <span className="w-14 shrink-0 text-right text-[0.84375rem] text-ink tnum">{int(r.value)}</span>
            <span className="w-10 shrink-0 text-right text-[0.75rem] text-ink-3 tnum">{total ? pct(r.value / total) : ""}</span>
          </>
        );
        return (
          <li key={r.key}>
            {onSelect ? (
              <button
                type="button"
                onClick={() => onSelect(r.key)}
                aria-pressed={on}
                title={on ? `Remove the filter on ${r.title}` : `Show only visits with ${what} ${r.title}`}
                aria-label={`${on ? "Remove filter" : "Filter by"} ${what} ${r.title}: ${int(r.value)} visitors`}
                className="group flex w-full items-center gap-3 rounded-[6px] pr-1 text-left outline-offset-0"
              >
                {body}
              </button>
            ) : (
              <div className="flex items-center gap-3 pr-1">{body}</div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** The column names over a list of rows. */
export function RowsHead({ name, extraName }: { name: string; extraName?: string }) {
  return (
    <div className="flex items-center gap-3 pr-1 pb-1 text-[0.71875rem] text-ink-3">
      <span className="min-w-0 flex-1 pl-2.5">{name}</span>
      {extraName && <span className="w-16 shrink-0 text-right">{extraName}</span>}
      <span className="w-14 shrink-0 text-right">Visitors</span>
      <span className="w-10 shrink-0" />
    </div>
  );
}

/** Every row of one breakdown, with a search: from a panel's "Show all" or the Filter menu. */
export function AllRows({
  open,
  onOpenChange,
  title,
  name,
  rows,
  total,
  selected,
  onSelect,
  mono,
  what,
  extraName,
  note,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  name: string;
  rows: Row[];
  total: number;
  selected?: string;
  onSelect: (key: string) => void;
  mono?: boolean;
  what: string;
  extraName?: string;
  note?: string;
}) {
  const [q, setQ] = useState("");
  const id = useId();
  const found = useMemo(() => {
    const s = q.trim().toLowerCase();
    return s ? rows.filter((r) => r.title.toLowerCase().includes(s) || r.key.toLowerCase().includes(s)) : rows;
  }, [rows, q]);
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) setQ("");
      }}
    >
      <DialogContent className="max-w-[40rem]">
        <DialogHeader className="pb-3">
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className="text-[0.875rem] text-ink-3">{note ?? "Choose one to show only those visits."}</DialogDescription>
          <label htmlFor={id} className="relative mt-4 block">
            <span className="sr-only">Search</span>
            <Search aria-hidden className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-ink-3" />
            <input
              id={id}
              type="search"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={`Search ${name.toLowerCase()}`}
              autoComplete="off"
              className="h-9 w-full rounded-[8px] border border-rule-2 bg-paper pr-3 pl-9 text-[0.875rem] text-ink placeholder:text-ink-3 focus:border-rule-3 focus:outline-hidden"
            />
          </label>
        </DialogHeader>
        <DialogBody className="pb-4">
          <RowsHead name={name} extraName={extraName} />
          {found.length === 0 ? (
            <p className="py-6 text-[0.875rem] text-ink-3">{rows.length === 0 ? "Nothing in this period." : `Nothing matches “${q.trim()}”.`}</p>
          ) : (
            <Rows
              rows={found}
              total={total}
              selected={selected}
              mono={mono}
              what={what}
              extraName={extraName}
              onSelect={(k) => {
                onSelect(k);
                onOpenChange(false);
                setQ("");
              }}
            />
          )}
          {rows.length >= 50 && <p className="mt-3 text-[0.75rem] text-ink-3">The 50 with the most visitors.</p>}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}

export function CodeBox({ code, name, className }: { code: string; name: string; className?: string }) {
  return (
    <div className={cn("min-w-0 overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk", className)}>
      <div className="flex items-center justify-between border-b border-rule py-1 pr-1.5 pl-4">
        <span className="font-mono text-[0.75rem] text-ink-3">{name}</span>
        <CopyButton value={code} label={`Copy ${name}`} />
      </div>
      <pre tabIndex={0} aria-label={name} className="overflow-x-auto px-4 py-3 font-mono text-[0.78rem] leading-5 text-ink-2">
        <code>{code}</code>
      </pre>
    </div>
  );
}
