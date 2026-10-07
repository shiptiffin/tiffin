import { CalendarDays, Check, ChevronDown, ListFilter, X } from "lucide-react";
import { DropdownMenu as M, Popover } from "radix-ui";
import { useState, type ReactNode } from "react";
import type { AnalyticsRealtime } from "@/api/modules";
import { countryName, isPath, shown } from "@/components/analytics-kit";
import { Button } from "@/components/ui/button";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from "@/components/ui/dropdown";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { FILTERS, type FilterKey } from "@/routes/analytics-search";

/**
 * The row above the numbers: which app, which days (and whether to compare
 * with the days before), filters to add, the active filters as chips, and
 * how many people are on the site right now.
 */

export const HOUR = 3_600_000;
export const DAY = 86_400_000;

export type Per = {
  v: string;
  /** "Last 7 days", "1 Oct – 6 Oct". */
  label: string;
  /** "the 7 days before". */
  before: string;
  ms: number;
};
export const periods: Per[] = [
  { v: "today", label: "Today", before: "yesterday", ms: DAY },
  { v: "yesterday", label: "Yesterday", before: "the day before", ms: DAY },
  { v: "24h", label: "Last 24 hours", before: "the 24 hours before", ms: DAY },
  { v: "7d", label: "Last 7 days", before: "the 7 days before", ms: 7 * DAY },
  { v: "30d", label: "Last 30 days", before: "the 30 days before", ms: 30 * DAY },
  { v: "90d", label: "Last 90 days", before: "the 90 days before", ms: 90 * DAY },
  { v: "12mo", label: "Last 12 months", before: "the 12 months before", ms: 365 * DAY },
];

const shortDay = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
export const isoDay = (t: number) => new Date(t).toISOString().slice(0, 10);

export function customPer(from: string, to?: string): Per {
  const a = Date.parse(`${from}T00:00:00Z`);
  const b = Date.parse(`${to ?? isoDay(Date.now())}T00:00:00Z`) + DAY;
  const n = Math.max(1, Math.round((b - a) / DAY));
  return { v: "custom", label: a === b - DAY ? shortDay.format(a) : `${shortDay.format(a)} – ${shortDay.format(b - DAY)}`, before: n === 1 ? "the day before" : `the ${n} days before`, ms: b - a };
}

const trigger =
  "inline-flex h-8 max-w-full items-center gap-1.5 rounded-[8px] border border-rule-2 bg-paper-raised px-2.5 text-[0.8125rem] text-ink shadow-[var(--top-light)] transition-colors duration-[var(--dur-state)] hover:border-rule-3 data-[state=open]:border-rule-3";

export function AppPicker({ apps, app, onChange }: { apps: string[]; app?: string; onChange: (a: string | undefined) => void }) {
  return (
    <Menu>
      <MenuTrigger asChild>
        <button type="button" className={trigger} aria-label={`App: ${app ?? "all apps"}`}>
          <span className={cn("truncate font-[550]", app ? "font-mono text-[0.78rem]" : "")}>{app ?? "All apps"}</span>
          <ChevronDown aria-hidden className="size-3.5 shrink-0 text-ink-3" />
        </button>
      </MenuTrigger>
      <MenuContent align="start">
        <MenuRadioGroup value={app ?? ""} onValueChange={(v) => onChange(v || undefined)}>
          <MenuRadioItem value="">All apps</MenuRadioItem>
          {apps.map((a) => (
            <MenuRadioItem key={a} value={a} className="font-mono text-[0.8125rem]">
              {a}
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>
      </MenuContent>
    </Menu>
  );
}

/** One button for the days: the presets, any days you choose, and the comparison. */
export function RangePicker({
  per,
  from,
  to,
  compare,
  onPeriod,
  onDays,
  onCompare,
}: {
  per: Per;
  from?: string;
  to?: string;
  compare: boolean;
  onPeriod: (v: string) => void;
  onDays: (from: string, to?: string) => void;
  onCompare: (on: boolean) => void;
}) {
  const [days, setDays] = useState(false);
  return (
    <Popover.Root open={days} onOpenChange={setDays}>
      <Menu>
        <Popover.Anchor asChild>
          <MenuTrigger asChild>
            <button type="button" className={trigger} aria-label={`Days: ${per.label}${compare ? `, compared with ${per.before}` : ""}. Change`}>
              <CalendarDays aria-hidden className="size-4 shrink-0 text-ink-3" />
              <span className="truncate font-[550]">{per.label}</span>
              {compare && <span className="truncate text-ink-3 max-sm:hidden">vs {per.before.replace(/^the /, "")}</span>}
              <ChevronDown aria-hidden className="size-3.5 shrink-0 text-ink-3" />
            </button>
          </MenuTrigger>
        </Popover.Anchor>
        <MenuContent align="start" className="w-64">
          <MenuRadioGroup value={per.v} onValueChange={onPeriod}>
            {periods.map((p, i) => (
              <div key={p.v}>
                {i === 3 && <MenuSeparator />}
                <MenuRadioItem value={p.v}>{p.label}</MenuRadioItem>
              </div>
            ))}
          </MenuRadioGroup>
          <MenuSeparator />
          <MenuItem onSelect={() => setTimeout(() => setDays(true), 0)}>
            <CalendarDays aria-hidden />
            {from ? "Change days…" : "Choose days…"}
          </MenuItem>
          <MenuSeparator />
          <M.CheckboxItem
            checked={compare}
            onCheckedChange={(c) => onCompare(c === true)}
            className="relative flex h-8 cursor-default items-center gap-2 rounded-md pr-2 pl-8 text-base text-ink-2 outline-hidden select-none data-[highlighted]:bg-paper-hover data-[highlighted]:text-ink"
          >
            <M.ItemIndicator className="absolute left-2 grid size-4 place-items-center">
              <Check aria-hidden className="size-4 text-ink" />
            </M.ItemIndicator>
            Compare with the days before
          </M.CheckboxItem>
        </MenuContent>
      </Menu>
      <Popover.Portal>
        <Popover.Content sideOffset={6} align="start" collisionPadding={12} className="z-50 w-[17.5rem] rounded-[10px] border border-rule-2 bg-paper-raised p-3.5 shadow-raised outline-hidden data-[state=open]:animate-pop">
          <DaysForm
            from={from}
            to={to}
            onApply={(a, b) => {
              onDays(a, b);
              setDays(false);
            }}
          />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function DaysForm({ from, to, onApply }: { from?: string; to?: string; onApply: (from: string, to?: string) => void }) {
  const [today] = useState(() => isoDay(Date.now()));
  const [a, setA] = useState(() => from ?? isoDay(Date.now() - 13 * DAY));
  const [b, setB] = useState(to ?? today);
  const ok = !!a && !!b && a <= b && b <= today;
  const input = "mt-1 block h-8 w-full rounded-[7px] border border-rule-2 bg-paper px-1.5 text-[0.8125rem] text-ink tnum focus:border-rule-3 focus:outline-hidden";
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (ok) onApply(a, b === today ? undefined : b);
      }}
    >
      <p className="text-[0.84375rem] font-[550] text-ink">Days to show</p>
      <div className="mt-2.5 grid grid-cols-2 gap-2">
        <label className="text-[0.75rem] text-ink-3">
          First day
          <input type="date" value={a} max={today} onChange={(e) => setA(e.target.value)} className={input} />
        </label>
        <label className="text-[0.75rem] text-ink-3">
          Last day
          <input type="date" value={b} max={today} onChange={(e) => setB(e.target.value)} className={input} />
        </label>
      </div>
      <p className={cn("mt-2 text-[0.75rem]", ok ? "text-ink-3" : "text-danger")}>{ok ? "Whole days, in UTC." : "The first day must come before the last, and neither after today."}</p>
      <Button type="submit" size="md" variant="primary" className="mt-3 w-full" disabled={!ok}>
        Show these days
      </Button>
    </form>
  );
}

const groups: Array<{ label: string; keys: FilterKey[] }> = [
  { label: "Pages", keys: ["page", "entry", "exit"] },
  { label: "Sources", keys: ["source", "utmSource", "utmMedium", "utmCampaign"] },
  { label: "Visitors", keys: ["country", "device", "browser", "os"] },
];

/** "Filter": every dimension; choosing one opens its values. */
export function FilterMenu({ filters, onPick }: { filters: Partial<Record<FilterKey, string>>; onPick: (k: FilterKey) => void }) {
  return (
    <Menu>
      <MenuTrigger asChild>
        <button type="button" className={trigger} aria-label="Filter">
          <ListFilter aria-hidden className="size-4 shrink-0 text-ink-3" />
          <span className="font-[550] max-sm:sr-only">Filter</span>
        </button>
      </MenuTrigger>
      <MenuContent align="start" className="w-64">
        {groups.map((g, i) => (
          <div key={g.label}>
            {i > 0 && <MenuSeparator />}
            <MenuLabel>{g.label}</MenuLabel>
            {g.keys.map((k) => {
              const v = filters[k];
              return (
                <MenuItem key={k} onSelect={() => setTimeout(() => onPick(k), 0)}>
                  <span className="flex-1">{FILTERS.find((f) => f.key === k)!.name}</span>
                  {v && <span className={cn("max-w-[7rem] truncate text-[0.75rem] text-ink-3", isPath(k) && "font-mono")}>{shown(k, v)}</span>}
                </MenuItem>
              );
            })}
          </div>
        ))}
      </MenuContent>
    </Menu>
  );
}

export function FilterChips({ filters, onRemove, onClear }: { filters: Partial<Record<FilterKey, string>>; onRemove: (k: FilterKey) => void; onClear: () => void }) {
  const on = FILTERS.filter((f) => filters[f.key]);
  if (!on.length) return null;
  return (
    <ul className="flex flex-wrap items-center gap-2" aria-label="Filters">
      {on.map((f) => (
        <li key={f.key} className="min-w-0 max-w-full">
          <span className="inline-flex h-7 max-w-full items-center rounded-full border border-brass/50 bg-brass-wash text-[0.8125rem] text-ink">
            <span className="flex min-w-0 items-center gap-1.5 pl-3">
              <span className="shrink-0 text-ink-2">{f.name}</span>
              <span className="shrink-0 text-ink-3">is</span>
              <span className={cn("truncate font-[550]", isPath(f.key) && "font-mono text-[0.75rem]")} title={filters[f.key]}>
                {shown(f.key, filters[f.key]!)}
              </span>
            </span>
            <button
              type="button"
              onClick={() => onRemove(f.key)}
              aria-label={`Remove filter: ${f.name} is ${shown(f.key, filters[f.key]!)}`}
              className="ml-1 grid size-7 shrink-0 place-items-center rounded-full text-ink-3 hover:text-ink"
            >
              <X aria-hidden className="size-3.5" />
            </button>
          </span>
        </li>
      ))}
      {on.length > 1 && (
        <li>
          <button type="button" onClick={onClear} className="h-7 rounded-[6px] px-2 text-[0.8125rem] text-ink-3 hover:bg-paper-hover hover:text-ink">
            Clear all
          </button>
        </li>
      )}
    </ul>
  );
}

const minute = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit" });

/** "3 online": people in the last 5 minutes; opens the last half hour. */
export function LiveNow({ rt }: { rt: AnalyticsRealtime }) {
  const now = rt.visitorsNow;
  const per = rt.perMinute ?? [];
  const most = Math.max(1, ...per.map((x) => x.pageviews));
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          className="inline-flex h-8 items-center gap-2 rounded-[8px] px-2 text-[0.8125rem] whitespace-nowrap text-ink-2 transition-colors hover:bg-paper-hover hover:text-ink data-[state=open]:bg-paper-hover"
          aria-label={`${int(now)} ${now === 1 ? "visitor" : "visitors"} in the last 5 minutes. Show the last half hour`}
        >
          <span aria-hidden className={cn("size-2 rounded-full", now ? "bg-ok" : "bg-ink-4")} />
          <span className="tnum">
            <span className="font-[550] text-ink">{int(now)}</span> online
          </span>
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} collisionPadding={12} className="z-50 w-[22rem] max-w-[calc(100vw-1.5rem)] rounded-[12px] border border-rule-2 bg-paper-raised p-4 shadow-raised outline-hidden data-[state=open]:animate-pop">
          <p className="text-[0.84375rem] font-[550] text-ink">Right now</p>
          <dl className="mt-3 grid grid-cols-3 gap-3">
            <Stat label="Last 5 min" value={int(rt.visitorsNow)} sub={rt.visitorsNow === 1 ? "visitor" : "visitors"} />
            <Stat label="Last 30 min" value={int(rt.visitors30m)} sub={rt.visitors30m === 1 ? "visitor" : "visitors"} />
            <Stat label="Page views" value={int(rt.pageviews30m)} sub="in 30 min" />
          </dl>
          <div role="img" aria-label={`Page views per minute, the last 30 minutes: ${int(rt.pageviews30m)} in all`} className="mt-4 flex h-14 items-end gap-[2px] border-b border-rule-2">
            {per.map((x) => (
              <span
                key={x.t}
                title={`${minute.format(new Date(x.t))}: ${int(x.pageviews)} ${x.pageviews === 1 ? "view" : "views"}`}
                className={cn("min-w-0 flex-1 rounded-t-[2px]", x.pageviews ? "bg-ink-3" : "bg-rule")}
                style={{ height: x.pageviews ? `${Math.max(8, (x.pageviews / most) * 100)}%` : "2px" }}
              />
            ))}
          </div>
          <div className="mt-1 flex justify-between text-[0.6875rem] text-ink-3">
            <span>30 min ago</span>
            <span>now</span>
          </div>
          {rt.pageviews30m === 0 ? (
            <p className="mt-3 text-[0.8125rem] text-ink-3">Quiet for the last half hour.</p>
          ) : (
            <div className="mt-3 grid gap-3">
              <Top title="Pages" rows={rt.topPages} mono />
              <Top title="Referrers" rows={rt.topSources} />
              <Top title="Countries" rows={rt.topCountries} name={countryName} />
            </div>
          )}
          <p className="mt-3 text-[0.71875rem] text-ink-3">Updates every 15 seconds.</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <div>
      <dt className="text-[0.71875rem] text-ink-3">{label}</dt>
      <dd className="mt-0.5 text-[1.125rem] leading-6 font-[550] text-ink tnum">{value}</dd>
      <dd className="text-[0.71875rem] text-ink-3">{sub}</dd>
    </div>
  );
}

function Top({ title, rows, mono, name = (v) => v }: { title: string; rows: AnalyticsRealtime["topPages"]; mono?: boolean; name?: (v: string) => ReactNode }) {
  const list = (rows ?? []).slice(0, 3);
  if (!list.length) return null;
  return (
    <div>
      <p className="text-[0.71875rem] text-ink-3">{title}</p>
      <ul className="mt-1 divide-y divide-rule">
        {list.map((x) => (
          <li key={x.value} className="flex justify-between gap-3 py-1">
            <span className={cn("truncate text-[0.8125rem] text-ink-2", mono && "font-mono text-[0.75rem]")}>{name(x.value)}</span>
            <span className="text-[0.8125rem] text-ink-3 tnum">{int(x.pageviews)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
