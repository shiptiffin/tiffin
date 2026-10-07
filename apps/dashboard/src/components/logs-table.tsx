import { Link } from "@tanstack/react-router";
import { ArrowDown, Braces, Check, Copy, Filter, ListTree, SquareArrowOutUpRight } from "lucide-react";
import { memo, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { fieldQL, sourceLabel, type Line } from "@/components/logs-query";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { int } from "@/lib/format";
import { full } from "@/lib/time";

const timeFmt = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" });
const dateFmt = new Intl.DateTimeFormat("en-GB", { day: "2-digit", month: "short" });

/** The row grid: time, level, source, message. Phones put the message on its own line. */
export const ROW_GRID = "grid grid-cols-[auto_3rem_minmax(0,1fr)] sm:grid-cols-[var(--logs-time)_3.25rem_8.5rem_minmax(0,1fr)] gap-x-3";

/** "warn" in amber, "error" in red, quieter words for the rest, nothing for lines with no level. */
export function LevelTag({ line }: { line: Line }) {
  const l = line.level;
  const word = l === "warn" ? "warn" : l === "error" ? (line.raw && line.raw.length <= 5 ? line.raw.toLowerCase() : "error") : l;
  return <span className={cn("select-none", l === "error" ? "font-[550] text-danger" : l === "warn" ? "font-[550] text-warn-ink" : "text-ink-4")}>{word}</span>;
}

/** The time, to the millisecond (the milliseconds quieter); with the day when the range spans days. */
export function LineTime({ line, withDate }: { line: Line; withDate?: boolean }) {
  if (!Number.isFinite(line.t)) return <span />;
  const ms = String(new Date(line.t).getMilliseconds()).padStart(3, "0");
  return (
    <time className="whitespace-nowrap text-ink-3 tnum" dateTime={line.iso} title={full(line.iso)}>
      {withDate && <span className="text-ink-4">{dateFmt.format(line.t)} </span>}
      {timeFmt.format(line.t)}
      <span className="text-ink-4 max-sm:hidden">.{ms}</span>
    </time>
  );
}

function highlight(text: string, terms: RegExp | null): ReactNode {
  if (!terms || !text) return text;
  const parts = text.split(terms);
  if (parts.length === 1) return text;
  return parts.map((p, i) =>
    i % 2 === 1 ? (
      <mark key={i} className="rounded-[2px] bg-brass-wash text-ink [box-shadow:0_0_0_1px_var(--brass-wash)]">
        {p}
      </mark>
    ) : (
      p
    ),
  );
}

/** A line's message: requests as method, path, status and time; everything else as written. */
export function LineMessage({ line, terms }: { line: Line; terms: RegExp | null }) {
  const r = line.row;
  if (/^missing _msg field\b/.test(line.msg)) return <span className="text-ink-4">(a line with no message)</span>;
  if (line.kind === "edge" && r.method && r.path) {
    const status = Number(r.status);
    return (
      <>
        <span className="text-ink-3">{String(r.method)}</span> <span className="text-ink-2">{highlight(String(r.path), terms)}</span>{" "}
        <span className={cn("tnum", status >= 500 ? "font-[550] text-danger" : status >= 400 ? "text-warn-ink" : "text-ink-2")}>{String(r.status)}</span>{" "}
        <span className="text-ink-4 tnum">{r.duration_ms ? `${Math.round(Number(r.duration_ms))}ms` : ""}</span>
      </>
    );
  }
  if (!line.msg.trim()) return <span className="text-ink-4">(empty line)</span>;
  return <>{highlight(line.msg, terms)}</>;
}

/** Words to mark in messages: the plain words and phrases of the search, not field filters. */
export function searchTerms(text: string): RegExp | null {
  const words: string[] = [];
  for (const m of text.matchAll(/"((?:[^"\\]|\\.)+)"|(\S+)/g)) {
    const w = m[1] ?? m[2];
    if (!w || /^(and|or|not)$/i.test(w) || /[:|()*~=]/.test(w) || w.startsWith("-") || w.startsWith("!") || w.length < 2) continue;
    words.push(w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  }
  return words.length ? new RegExp(`(${words.join("|")})`, "gi") : null;
}

type RowProps = {
  line: Line;
  open: boolean;
  selected: boolean;
  tabbable: boolean;
  fresh: boolean;
  withDate: boolean;
  terms: RegExp | null;
  onToggle: (key: string) => void;
  detail: ReactNode;
};

const LogRow = memo(function LogRow({ line, open, selected, tabbable, fresh, withDate, terms, onToggle, detail }: RowProps) {
  return (
    <li
      data-log-line
      data-key={line.key}
      className={cn("relative transition-[background-color] duration-[1200ms] ease-[var(--ease-out)]", fresh && "bg-brass-wash")}
    >
      {line.level === "error" || line.level === "warn" ? (
        <span aria-hidden className={cn("absolute inset-y-0 left-0 w-[2px]", line.level === "error" ? "bg-danger" : "bg-warn")} />
      ) : null}
      <button
        type="button"
        onClick={() => onToggle(line.key)}
        aria-expanded={open}
        // The keyboard cursor (arrows, j/k). A list item can't carry aria-selected
        // (it isn't an option, and its open detail holds buttons), so per the APG
        // the cursor row is marked as the current one.
        aria-current={selected ? "true" : undefined}
        tabIndex={tabbable ? 0 : -1}
        className={cn(
          ROW_GRID,
          "w-full items-baseline px-3 py-[1px] text-left outline-hidden focus-visible:shadow-[inset_0_0_0_2px_var(--focus)]",
          open ? "bg-paper-select" : selected ? "bg-paper-select" : line.level === "error" ? "bg-danger-wash hover:bg-paper-hover" : "hover:bg-paper-hover",
          selected && "shadow-[inset_0_0_0_1px_var(--rule-3)]",
        )}
      >
        <LineTime line={line} withDate={withDate} />
        <LevelTag line={line} />
        <span className="hidden truncate text-ink-3 sm:block" title={line.kind ? `${line.source} · ${sourceLabel(line.kind)}` : line.source}>
          {line.source}
        </span>
        <span
          className={cn(
            "col-span-3 min-w-0 pb-1 sm:col-span-1 sm:pb-0",
            line.level === "error" ? "text-ink" : "text-ink-2",
            open ? "break-all whitespace-pre-wrap" : "truncate max-sm:line-clamp-2 max-sm:whitespace-normal max-sm:break-all",
          )}
        >
          <span className="text-ink-3 sm:hidden">{line.source} </span>
          <LineMessage line={line} terms={terms} />
        </span>
      </button>
      {open && detail}
    </li>
  );
});

/**
 * The lines, oldest at the top and newest at the bottom, the way a terminal
 * reads. While live it stays pinned to the bottom and pauses the moment you
 * scroll up, with a count of what arrived meanwhile.
 */
export function LogsTable({
  lines,
  fresh,
  live,
  openKey,
  selKey,
  withDate,
  terms,
  onToggle,
  onMove,
  renderDetail,
  older,
  footer,
}: {
  lines: Line[];
  fresh: Set<string>;
  live: boolean;
  openKey: string | null;
  selKey: string | null;
  withDate: boolean;
  terms: RegExp | null;
  onToggle: (key: string) => void;
  onMove: (d: 1 | -1) => void;
  renderDetail: (line: Line) => ReactNode;
  older?: ReactNode;
  footer?: ReactNode;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [pinned, setPinned] = useState(true);
  const [behind, setBehind] = useState(0);
  const first = lines[0]?.key;
  const last = lines[lines.length - 1]?.key;
  const prev = useRef<{ first?: string; last?: string; height: number }>({ height: 0 });

  const [prevLines, setPrevLines] = useState(lines);
  if (lines !== prevLines) {
    setPrevLines(lines);
    if (!pinned && live && fresh.size) setBehind(behind + fresh.size);
  }

  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const p = prev.current;
    if (pinned && p.last !== last) el.scrollTop = el.scrollHeight;
    else if (p.first !== first && p.last === last && p.height) el.scrollTop += el.scrollHeight - p.height; // older lines arrived above: stay put
    prev.current = { first, last, height: el.scrollHeight };
  }, [first, last, lines.length, pinned]);

  useLayoutEffect(() => {
    if (!selKey) return;
    const el = box.current?.querySelector<HTMLElement>(`[data-key="${CSS.escape(selKey)}"]`);
    el?.scrollIntoView({ block: "nearest" });
    const b = el?.querySelector<HTMLElement>("button");
    const a = document.activeElement;
    if (b && (!a || a === document.body || a.closest("[data-log-list]"))) b.focus({ preventScroll: true });
  }, [selKey]);

  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (atEnd !== pinned) setPinned(atEnd);
    if (atEnd && behind) setBehind(0);
  };
  const jump = () => {
    const el = box.current;
    if (el) el.scrollTop = el.scrollHeight;
    setPinned(true);
    setBehind(0);
  };

  return (
    <div className="relative">
      <div
        ref={box}
        onScroll={onScroll}
        data-log-list
        className="max-h-[max(22rem,calc(100dvh-25rem))] overflow-y-auto overscroll-contain [overflow-anchor:none] max-sm:max-h-[68dvh]"
      >
        <div
          className={cn(ROW_GRID, "sticky top-0 z-[1] border-b border-rule bg-paper-sunk px-3 py-1.5 text-[0.6875rem] text-ink-3 max-sm:hidden")}
          aria-hidden
        >
          <span>Time</span>
          <span>Level</span>
          <span>Source</span>
          <span>Message</span>
        </div>
        {older}
        <ol
          className="py-1 font-mono text-[0.75rem] leading-5 [font-variant-ligatures:none]"
          aria-live={live ? "polite" : undefined}
          aria-relevant="additions"
          aria-label="Log lines"
          onKeyDown={(e) => {
            if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
            e.preventDefault();
            onMove(e.key === "ArrowDown" ? 1 : -1);
          }}
        >
          {lines.map((l) => (
            <LogRow
              key={l.key}
              line={l}
              open={openKey === l.key}
              selected={selKey === l.key}
              tabbable={(selKey ?? last) === l.key}
              fresh={live && fresh.has(l.key)}
              withDate={withDate}
              terms={terms}
              onToggle={onToggle}
              detail={openKey === l.key ? renderDetail(l) : null}
            />
          ))}
        </ol>
        {footer}
      </div>
      {!pinned && lines.length > 0 && (live || behind > 0) && (
        <button
          type="button"
          onClick={jump}
          className="absolute bottom-4 left-1/2 inline-flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-rule-2 bg-paper-raised px-3.5 py-1.5 text-[0.8125rem] font-[550] text-ink shadow-raised transition-colors hover:bg-paper-hover"
        >
          <ArrowDown className="size-3.5" aria-hidden />
          {behind > 0 ? `${int(behind)} new ${behind === 1 ? "line" : "lines"}` : "Back to the newest"}
        </button>
      )}
    </div>
  );
}

function ActionButton({ icon, children, onClick, done }: { icon: ReactNode; children: ReactNode; onClick: () => void; done?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex h-7 items-center gap-1.5 rounded-[6px] px-2 font-sans text-[0.78125rem] text-ink-2 transition-colors hover:bg-paper-hover hover:text-ink [&_svg]:size-3.5 [&_svg]:text-ink-3"
    >
      {done ? <Check className="!text-ok" aria-hidden /> : icon}
      {children}
    </button>
  );
}

const HIDDEN = new Set(["_msg", "_stream", "_time"]);

/**
 * An opened line: the whole message, every field (click one to filter by
 * it), and what to do next: copy, see the lines around it, open its trace or
 * error.
 */
export function LineDetail({
  line,
  project,
  onFilter,
  onContext,
}: {
  line: Line;
  project?: string;
  onFilter: (field: string, value: string) => void;
  onContext?: (line: Line) => void;
}) {
  const [copied, setCopied] = useState<"" | "line" | "json">("");
  const copy = async (what: "line" | "json") => {
    if (await copyText(what === "line" ? line.msg : JSON.stringify(line.row, null, 2))) {
      setCopied(what);
      setTimeout(() => setCopied(""), 1400);
    }
  };
  const r = line.row;
  const fields = Object.entries(r).filter(([k]) => !HIDDEN.has(k));
  const trace = typeof r.trace_id === "string" && project ? r.trace_id : null;
  const issue = typeof r.issue === "string" && /^iss_/.test(r.issue) ? r.issue : null;
  return (
    <div className="border-y border-rule bg-paper-raised px-3 pt-1.5 pb-3 sm:pl-[calc(var(--logs-time)+3.25rem+1.5rem+0.75rem)]">
      <div className="-ml-2 flex flex-wrap items-center gap-0.5">
        <ActionButton icon={<Copy />} onClick={() => void copy("line")} done={copied === "line"}>
          Copy line
        </ActionButton>
        <ActionButton icon={<Braces />} onClick={() => void copy("json")} done={copied === "json"}>
          Copy as JSON
        </ActionButton>
        {onContext && (
          <ActionButton icon={<ListTree />} onClick={() => onContext(line)}>
            Show surrounding lines
          </ActionButton>
        )}
        {trace && (
          <Link
            to="/requests/$project/$id"
            params={{ project: project!, id: trace }}
            className="inline-flex h-7 items-center gap-1.5 rounded-[6px] px-2 font-sans text-[0.78125rem] text-ink-2 hover:bg-paper-hover hover:text-ink"
          >
            <SquareArrowOutUpRight className="size-3.5 text-ink-3" aria-hidden />
            Open the request
          </Link>
        )}
        {issue && (
          <Link
            to="/errors/$id"
            params={{ id: issue }}
            className="inline-flex h-7 items-center gap-1.5 rounded-[6px] px-2 font-sans text-[0.78125rem] text-ink-2 hover:bg-paper-hover hover:text-ink"
          >
            <SquareArrowOutUpRight className="size-3.5 text-ink-3" aria-hidden />
            Open the error
          </Link>
        )}
      </div>
      <dl className="mt-1.5 grid grid-cols-[minmax(6rem,max-content)_minmax(0,1fr)] gap-x-4 text-[0.75rem] leading-5">
        <div className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-ink-3">time</dt>
          <dd className="text-ink-2 tnum">
            {line.iso} <span className="font-sans text-ink-4">· {full(line.iso)}</span>
          </dd>
        </div>
        {fields.map(([k, v]) => {
          const s = typeof v === "string" ? v : JSON.stringify(v);
          return (
            <div key={k} className="group col-span-2 grid grid-cols-subgrid">
              <dt className="truncate text-ink-3" title={k}>
                {k}
              </dt>
              <dd className="flex min-w-0 items-start gap-1">
                <span className="min-w-0 break-all text-ink-2">{s}</span>
                {s.length <= 200 && (
                  <button
                    type="button"
                    onClick={() => onFilter(k, s)}
                    aria-label={`Show only lines where ${k} is ${s}`}
                    title={`Only ${fieldQL(k, s)}`}
                    className="grid size-5 shrink-0 place-items-center rounded-[4px] text-ink-4 opacity-0 transition-opacity group-hover:opacity-100 hover:bg-paper-hover hover:text-ink focus-visible:opacity-100 max-sm:opacity-100"
                  >
                    <Filter className="size-3" aria-hidden />
                  </button>
                )}
              </dd>
            </div>
          );
        })}
      </dl>
    </div>
  );
}
