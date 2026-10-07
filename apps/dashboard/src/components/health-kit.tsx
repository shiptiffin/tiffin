import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import emptyBackups from "@/assets/illustrations/empty-backups.webp";
import { cn } from "@/lib/cn";
import { Mascot } from "./mascot";
import { RadioGroup, RadioItem } from "./ui/choice";
import { Crumbs } from "./page";

// Small page-local pieces shared by the Health, Access and Settings pages
// (status, metrics, logs, errors, alerts, protection, backups, tokens,
// people, passkeys, settings). Composed from the Fusion primitives; nothing
// here is a new visual language.

export const healthCrumbs = <Crumbs items={[{ label: "Health", to: "/status" }]} />;
export const accessCrumbs = <Crumbs items={[{ label: "Settings", to: "/settings" }]} />;

/**
 * The state of things in one sentence, under a page title: Newsreader, a
 * size down from the Box's headline so the title stays the page's name.
 */
export function StateLine({ children, danger, className }: { children: ReactNode; danger?: boolean; className?: string }) {
  return (
    <p className={cn("state-sentence mt-3 max-w-[46rem]", danger ? "text-danger" : "text-ink", className)}>
      {children}
    </p>
  );
}

/** A group of rows on the page: a small label (and something on the right), then the rows between hairlines. */
export function Group({
  label,
  aside,
  children,
  className,
  id,
  flush,
}: {
  label: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
  id?: string;
  /** No space above (the first thing in a grid cell). */
  flush?: boolean;
}) {
  return (
    <section className={cn(flush ? "" : "[:where(&)]:mt-11", className)} aria-labelledby={id}>
      <div className="mb-2.5 flex min-h-5 flex-wrap items-center justify-between gap-x-4 gap-y-1">
        <h2 id={id} className="label">
          {label}
        </h2>
        {aside && <div className="text-[0.8125rem] text-ink-3">{aside}</div>}
      </div>
      {children}
    </section>
  );
}

/** Rows between hairlines, with a rule above and below the group. */
export function Rows({ children, className }: { children: ReactNode; className?: string }) {
  return <ul className={cn("divide-y divide-rule border-y border-rule", className)}>{children}</ul>;
}

/** Facts as label/value pairs in two columns (one on a phone). */
export function Facts({ items, className, narrow }: { items: Array<[ReactNode, ReactNode] | false | null | undefined>; className?: string; narrow?: boolean }) {
  return (
    <dl className={cn("grid gap-x-6 text-[0.875rem]", narrow ? "grid-cols-[6.5rem_minmax(0,1fr)]" : "grid-cols-[minmax(7rem,11rem)_minmax(0,1fr)]", className)}>
      {items.filter(Boolean).map((it, i) => {
        const [k, v] = it as [ReactNode, ReactNode];
        return (
          <div key={i} className="col-span-2 grid grid-cols-subgrid border-b border-rule py-2.5 first:border-t">
            <dt className="text-ink-3">{k}</dt>
            <dd className="min-w-0 text-ink">{v}</dd>
          </div>
        );
      })}
    </dl>
  );
}

/** A small set of choices in one control: theme, sounds, time window. A Radix radio group: arrow keys move and choose. */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  label,
  className,
  disabled,
}: {
  value: T;
  onChange: (v: T) => void;
  options: Array<{ v: T; label: ReactNode }>;
  label: string;
  className?: string;
  disabled?: boolean;
}) {
  return (
    <RadioGroup
      aria-label={label}
      orientation="horizontal"
      value={value}
      onValueChange={(v) => onChange(v as T)}
      disabled={disabled}
      className={cn("inline-flex w-max gap-0.5 rounded-[8px] border border-rule-2 p-0.5", disabled && "opacity-50", className)}
    >
      {options.map((o) => (
        <RadioItem
          key={o.v}
          value={o.v}
          className="h-7 rounded-[6px] px-2.5 text-[0.8125rem] whitespace-nowrap text-ink-3 transition-colors duration-[var(--dur-state)] hover:text-ink data-[state=checked]:bg-paper-select data-[state=checked]:font-[550] data-[state=checked]:text-ink"
        >
          {o.label}
        </RadioItem>
      ))}
    </RadioGroup>
  );
}

/** A level as a quiet word: nothing for info, "warn" in amber ink, "error" in red. */
export function LevelWord({ level, className }: { level: string; className?: string }) {
  const l = level.toLowerCase();
  const tone = l === "error" || l === "fatal" || l === "panic" || l === "critical" ? "text-danger" : l === "warning" || l === "warn" ? "text-warn-ink" : "text-ink-4";
  const word = l === "warning" ? "warn" : l === "information" ? "info" : l;
  return <span className={cn(tone, className)}>{word}</span>;
}

/**
 * A calm empty state with a drawing, a sentence and what happens next.
 * errors: the mascot, live and content (nothing is wrong). backups: the
 * mascot beside a pantry shelf of jars (nothing put by yet).
 */
export function Calm({ art: a, title, children, className }: { art: "errors" | "backups"; title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("grid justify-items-center px-4 pt-6 pb-10 text-center", className)}>
      {a === "errors" ? (
        <Mascot state="live" size={144} className="my-2" />
      ) : (
        <span className="art-plate my-3 block" data-plate="tile">
          <img src={emptyBackups} alt="" width={240} height={140} className="block h-[140px] w-60 select-none" draggable={false} />
        </span>
      )}
      <p className="mt-2 text-[0.9375rem] font-[550] text-ink">{title}</p>
      {children && <div className="mt-1 max-w-[30rem] text-[0.875rem] text-ink-3">{children}</div>}
    </div>
  );
}

/** A row that speaks up: something isn't fine. Red for broken, amber for a look. Links to where to fix it. */
export function Alarm({
  tone = "danger",
  title,
  detail,
  to,
  search,
  action,
}: {
  tone?: "danger" | "warn";
  title: ReactNode;
  detail?: ReactNode;
  to?: string;
  search?: Record<string, unknown>;
  action?: ReactNode;
}) {
  const body = (
    <>
      <span aria-hidden className={cn("mt-[7px] size-1.5 shrink-0 rounded-full", tone === "danger" ? "bg-danger" : "bg-warn")} />
      <span className="min-w-0 flex-1">
        <span className={cn("block text-[0.9375rem] leading-[1.375rem]", tone === "danger" ? "text-danger" : "text-warn-ink")}>{title}</span>
        {detail && <span className="mt-0.5 block text-[0.84375rem] text-ink-2">{detail}</span>}
      </span>
      {to && (
        <span aria-hidden className="self-center text-ink-3 transition-colors group-hover:text-ink">
          →
        </span>
      )}
    </>
  );
  return (
    <li className="flex items-start gap-3">
      {to ? (
        <Link to={to as "/"} search={search as never} className="group flex flex-1 items-start gap-3 py-3 -mx-2 px-2 rounded-[6px] transition-colors hover:bg-paper-hover">
          {body}
        </Link>
      ) : (
        <div className="flex flex-1 items-start gap-3 py-3">{body}</div>
      )}
      {action && <div className="self-center">{action}</div>}
    </li>
  );
}
