// Words and small pieces for the Queues, Jobs and Workflows pages: schedules
// in plain words, job and run states as words (not rainbow pills), the
// "gave up" message without the SDK's protocol detail, the empty-jobs drawing.
// (The drawing is the mascot's night pose; it isn't a <Mascot> state.)
import type { ReactNode } from "react";
import type { QueueJob, WorkflowRun } from "@/api/modules";
import emptyJobs from "@/assets/illustrations/mascot-night.webp";
import { RadioGroup, RadioItem } from "@/components/ui/choice";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { relative } from "@/lib/time";

// ------------------------------------------------------------------ schedules

const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const hhmm = (h: number, m: number) => `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
const ordinal = (n: number) => `${n}${n % 10 === 1 && n !== 11 ? "st" : n % 10 === 2 && n !== 12 ? "nd" : n % 10 === 3 && n !== 13 ? "rd" : "th"}`;
const isNum = (s: string) => /^\d+$/.test(s);
const and = (xs: string[]) => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

function partOfDay(h: number) {
  if (h < 5 || h >= 22) return "night";
  if (h < 12) return "morning";
  if (h < 18) return "afternoon";
  return "evening";
}

function dayList(dow: string): string | null {
  if (dow === "1-5") return "weekday";
  if (dow === "0,6" || dow === "6,0" || dow === "6-7" || dow === "sat,sun") return "Saturday and Sunday";
  const parts = dow.split(",");
  if (!parts.every((p) => isNum(p) && Number(p) <= 7)) return null;
  return and(parts.map((p) => days[Number(p) % 7]));
}

/**
 * A cron schedule as a person would say it: "every night at 02:00",
 * "every 10 minutes", "every weekday at 09:00". `timeOfDay` is true when
 * the words name a clock time (which is the box's time, so say so).
 * Anything unusual comes back as written, with `exact: false`.
 */
export function cronHuman(schedule: string): { words: string; exact: boolean; timeOfDay: boolean } {
  const s = schedule.trim();
  const macros: Record<string, [string, boolean]> = {
    "@yearly": ["every 1 January at 00:00", true],
    "@annually": ["every 1 January at 00:00", true],
    "@monthly": ["on the 1st of every month at 00:00", true],
    "@weekly": ["every Sunday at 00:00", true],
    "@daily": ["every night at midnight", true],
    "@midnight": ["every night at midnight", true],
    "@hourly": ["every hour, on the hour", false],
  };
  if (macros[s]) return { words: macros[s][0], exact: true, timeOfDay: macros[s][1] };
  const every = s.match(/^@every\s+(\d+)([smh])$/);
  if (every) {
    const n = Number(every[1]);
    const unit = { s: "second", m: "minute", h: "hour" }[every[2] as "s" | "m" | "h"];
    return { words: n === 1 ? `every ${unit}` : `every ${n} ${unit}s`, exact: true, timeOfDay: false };
  }
  const f = s.split(/\s+/);
  if (f.length !== 5) return { words: s, exact: false, timeOfDay: false };
  const [m, h, dom, mon, dow] = f;
  const anyDay = dom === "*" && mon === "*" && dow === "*";
  if (anyDay && h === "*" && m === "*") return { words: "every minute", exact: true, timeOfDay: false };
  if (anyDay && h === "*" && /^\*\/\d+$/.test(m)) {
    const n = Number(m.slice(2));
    return { words: n === 1 ? "every minute" : `every ${n} minutes`, exact: true, timeOfDay: false };
  }
  if (anyDay && h === "*" && isNum(m)) return { words: Number(m) === 0 ? "every hour, on the hour" : `every hour at :${m.padStart(2, "0")}`, exact: true, timeOfDay: false };
  if (anyDay && /^\*\/\d+$/.test(h) && isNum(m)) {
    const n = Number(h.slice(2));
    return { words: `every ${n} hours${Number(m) ? ` at :${m.padStart(2, "0")}` : ""}`, exact: true, timeOfDay: false };
  }
  if (!isNum(m)) return { words: s, exact: false, timeOfDay: false };
  const hours = h.split(",");
  if (!hours.every(isNum)) return { words: s, exact: false, timeOfDay: false };
  const times = and(hours.map((x) => hhmm(Number(x), Number(m))));
  if (anyDay) {
    if (hours.length > 1) return { words: `every day at ${times}`, exact: true, timeOfDay: true };
    const hh = Number(hours[0]);
    const when = hh === 0 && Number(m) === 0 ? "every night at midnight" : `every ${partOfDay(hh)} at ${times}`;
    return { words: when, exact: true, timeOfDay: true };
  }
  if (dom === "*" && mon === "*") {
    const d = dayList(dow);
    if (d) return { words: d === "weekday" ? `every weekday at ${times}` : `every ${d} at ${times}`, exact: true, timeOfDay: true };
  }
  if (isNum(dom) && mon === "*" && dow === "*") return { words: `on the ${ordinal(Number(dom))} of every month at ${times}`, exact: true, timeOfDay: true };
  return { words: s, exact: false, timeOfDay: false };
}

const localClock = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const boxClock = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" });

/**
 * "every night at 02:00, box time (04:00 for you)": the schedule in words, and
 * the reader's own clock when it differs. The box keeps UTC.
 */
export function scheduleWords(schedule: string, nextAt?: string): string {
  const c = cronHuman(schedule);
  if (!c.timeOfDay) return c.words;
  let mine = "";
  if (nextAt) {
    const d = new Date(nextAt);
    const local = localClock.format(d);
    if (local !== boxClock.format(d)) mine = ` (${local} for you)`;
  }
  return `${c.words}, box time${mine}`;
}

// ------------------------------------------------------------------ jobs

/** "HTTP 489: …" is the SDK's "don't retry" answer: show the app's message, not the protocol. */
export function jobError(e?: string): { text: string; gaveUp: boolean } | null {
  if (!e) return null;
  const m = e.match(/^(?:HTTP 489:|NonRetryableError:)\s*(.*)$/s);
  return m ? { text: m[1].replace(/^NonRetryableError:\s*/, ""), gaveUp: true } : { text: e, gaveUp: false };
}

export function ErrorText({ e, className }: { e?: string; className?: string }) {
  const x = jobError(e);
  if (!x) return null;
  return (
    <span className={className}>
      {x.text}
      {x.gaveUp && <span className="font-sans text-ink-3"> · the app said not to retry</span>}
    </span>
  );
}

export type Tone = "quiet" | "plain" | "warn" | "danger";

export const toneClass: Record<Tone, string> = {
  quiet: "text-ink-3",
  plain: "text-ink",
  warn: "text-warn-ink",
  danger: "text-danger",
};

/** A job's state as one or two words, and how loud it should be. Status only speaks up when it isn't fine. */
export function jobState(j: Pick<QueueJob, "state" | "attempt" | "maxAttempts" | "runAt">): { word: string; tone: Tone } {
  switch (j.state) {
    case "completed":
      return { word: "Done", tone: "quiet" };
    case "queued":
      return { word: "Waiting", tone: "plain" };
    case "running":
      return { word: j.attempt > 1 ? `Running, try ${int(j.attempt)}` : "Running", tone: "plain" };
    case "retrying":
      return { word: `Retrying, ${int(j.attempt)} of ${int(j.maxAttempts)} tries`, tone: "warn" };
    case "scheduled":
      return { word: `Due ${relative(j.runAt)}`, tone: "quiet" };
    case "dead":
      return { word: "Gave up", tone: "danger" };
    case "cancelled":
      return { word: "Cancelled", tone: "quiet" };
  }
}

/** The filter words, in the order a person scans them. */
export const jobFilters: Array<{ state?: QueueJob["state"]; label: string }> = [
  { label: "All" },
  { state: "queued", label: "Waiting" },
  { state: "running", label: "Running" },
  { state: "retrying", label: "Retrying" },
  { state: "scheduled", label: "Scheduled" },
  { state: "completed", label: "Done" },
  { state: "dead", label: "Gave up" },
  { state: "cancelled", label: "Cancelled" },
];

export const runFilters: Array<{ state?: WorkflowRun["state"]; label: string }> = [
  { label: "All" },
  { state: "running", label: "Running" },
  { state: "waiting", label: "Waiting" },
  { state: "completed", label: "Done" },
  { state: "failed", label: "Failed" },
  { state: "cancelled", label: "Cancelled" },
];

/** 'approval "Ship order #1043?"' → { kind: "approval", what: "Ship order #1043?" }. */
export function waitingFor(s?: string): { kind: string; what: string } | null {
  if (!s) return null;
  const m = s.match(/^(\w+)\s+"(.*)"$/s);
  return m ? { kind: m[1], what: m[2] } : { kind: "", what: s };
}

/** A run's state as a sentence fragment for a list row. */
export function runWords(r: WorkflowRun): { text: ReactNode; tone: Tone } {
  const w = waitingFor(r.waitingFor);
  switch (r.state) {
    case "waiting":
      if (w?.kind === "approval") return { text: <>Waiting for a person: {w.what}</>, tone: "plain" };
      if (w?.kind === "event") return { text: <>Waiting for the event <code className="ident text-[0.78rem]">{w.what}</code></>, tone: "plain" };
      if (w?.kind === "sleep") return { text: <>Sleeping until {w.what}</>, tone: "plain" };
      return { text: <>Waiting for {r.waitingFor}</>, tone: "plain" };
    case "failed":
      return { text: <>Failed: {jobError(r.error)?.text ?? "no reason given"}</>, tone: "danger" };
    case "completed":
      return { text: <>Done in {r.turns === 1 ? "one turn" : `${int(r.turns)} turns`}</>, tone: "quiet" };
    case "cancelled":
      return { text: "Cancelled", tone: "quiet" };
    case "running":
      return { text: "Running a step now", tone: "plain" };
  }
}

// ------------------------------------------------------------------ filter words

/** Filter words in one row: the chosen one in ink with an underline, the rest quiet. Counts when known. */
export function FilterWords<T extends string | undefined>({
  items,
  value,
  onPick,
  counts,
  label,
}: {
  items: Array<{ state?: T; label: string }>;
  value: T | undefined;
  onPick: (s: T | undefined) => void;
  counts?: Partial<Record<NonNullable<T>, number>>;
  label: string;
}) {
  return (
    // A Radix radio group: arrow keys move and choose, one tab stop. "" stands for "all" (no state).
    <RadioGroup
      aria-label={label}
      orientation="horizontal"
      value={value ?? ""}
      onValueChange={(v) => onPick(items.find((it) => (it.state ?? "") === v)?.state)}
      className="flex flex-wrap items-center gap-x-1 gap-y-1"
    >
      {items.map((it) => {
        const n = it.state ? counts?.[it.state as NonNullable<T>] : undefined;
        return (
          <RadioItem
            key={it.label}
            value={it.state ?? ""}
            className="relative inline-flex h-8 items-center gap-1.5 rounded-[6px] px-2.5 text-[0.84375rem] text-ink-3 transition-colors duration-[var(--dur-state)] hover:text-ink data-[state=checked]:bg-paper-select data-[state=checked]:font-[550] data-[state=checked]:text-ink"
          >
            {it.label}
            {n !== undefined && n > 0 && <span className={cn("text-xs tnum", it.state === "dead" ? "text-danger" : "text-ink-3")}>{int(n)}</span>}
          </RadioItem>
        );
      })}
    </RadioGroup>
  );
}

// ------------------------------------------------------------------ empty

/** The empty-jobs drawing (the mascot asleep under a moon: nothing to do tonight), a sentence, and what to do. */
export function EmptyJobs({ title, children, className }: { title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-col items-center px-6 py-10 text-center", className)}>
      <span className="art-plate block size-[128px]">
        <img src={emptyJobs} alt="" width={128} height={128} className="block size-full select-none" draggable={false} />
      </span>
      <p className="mt-3 text-md text-ink">{title}</p>
      {children && <div className="mx-auto mt-1.5 max-w-[30rem] text-base text-ink-3">{children}</div>}
    </div>
  );
}

/** The page's state in one sentence: Newsreader, a step below the Box's. */
export function StateSentence({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cn("state-sentence text-ink", className)}>{children}</p>;
}
