import { useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, Check, ChevronDown, ChevronRight, ChevronUp, Copy, Download, Search, WrapText } from "lucide-react";
import { Toggle } from "radix-ui";
import { useEffect, useEffectEvent, useImperativeHandle, useMemo, useRef, useState, type CSSProperties, type ReactNode, type Ref } from "react";
import { mod3 } from "@/api/modules";
import { inferLevel } from "@/components/logs-query";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { count, dec, int, NNBSP } from "@/lib/format";

/**
 * A deploy's build log, at the level of Vercel's Build Logs: line numbers,
 * elapsed time while it builds, ANSI colours, the box's phases as
 * collapsible steps with their timings, search with next/previous, error and
 * warning counts with "next error", follow-tail with a pill back to the
 * bottom, a wrap toggle, copy and download. Rows are virtualised, so a
 * 10,000-line log scrolls as smoothly as a short one.
 */

// ------------------------------------------------------------------ the data

export type RawLine = { raw: string; at: number | null };

/** The log as it stands, then followed over server-sent events, ANSI kept. */
export function useRawBuildLog(project: string, app: string, id: string | undefined) {
  const qc = useQueryClient();
  const [log, setLog] = useState<{ id?: string; lines: RawLine[]; live: boolean; done: boolean; error?: boolean }>({ lines: [], live: false, done: false });
  const partial = useRef("");

  useEffect(() => {
    if (!id) return;
    let es: EventSource | null = null;
    let cancelled = false;
    partial.current = "";
    type L = { lines: RawLine[]; live: boolean; done: boolean; error?: boolean };
    const update = (f: (l: L) => Partial<L>) =>
      setLog((prev) => {
        const base = prev.id === id ? prev : { id, lines: [], live: false, done: false };
        return { ...base, ...f(base), id };
      });
    const push = (text: string, at: number | null) => {
      const parts = (partial.current + text).split("\n");
      partial.current = parts.pop() ?? "";
      if (parts.length) update((l) => ({ lines: [...l.lines, ...parts.map((t) => ({ raw: t.replace(/\r/g, ""), at }))] }));
    };
    const flush = (at: number | null) => {
      const rest = partial.current;
      partial.current = "";
      if (rest) update((l) => ({ lines: [...l.lines, { raw: rest.replace(/\r/g, ""), at }] }));
    };
    mod3
      .buildLog(project, app, id)
      .then((b) => {
        if (cancelled) return;
        update(() => ({ lines: [] }));
        push(b.text, null);
        if (b.done) {
          flush(null);
          update(() => ({ done: true }));
          return;
        }
        es = new EventSource(mod3.buildLogStream(project, app, id, b.offset));
        es.onopen = () => update(() => ({ live: true }));
        es.addEventListener("log", (e) => push((JSON.parse((e as MessageEvent).data) as { text: string }).text, Date.now()));
        es.addEventListener("done", () => {
          flush(Date.now());
          update(() => ({ live: false, done: true }));
          es?.close();
          void qc.invalidateQueries({ queryKey: ["deploy", project, app, id] });
          void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
          void qc.invalidateQueries({ queryKey: ["project-deploys", project] });
        });
        es.onerror = () => update(() => ({ live: false }));
      })
      .catch(() => !cancelled && update(() => ({ error: true })));
    return () => {
      cancelled = true;
      es?.close();
    };
  }, [project, app, id, qc]);

  const cur = log.id === id ? log : { lines: [] as RawLine[], live: false, done: false, error: false };
  return cur;
}

// ------------------------------------------------------------------ ANSI

type Seg = { text: string; color?: string; bold?: boolean; dim?: boolean };

const FG: Record<number, string> = {
  31: "var(--danger)",
  32: "var(--ok)",
  33: "var(--warn-ink)",
  34: "var(--enamel-indigo)",
  35: "var(--enamel-plum)",
  36: "var(--enamel-teal)",
  90: "var(--ink-4)",
  91: "var(--danger)",
  92: "var(--ok)",
  93: "var(--warn-ink)",
  94: "var(--enamel-indigo)",
  95: "var(--enamel-plum)",
  96: "var(--enamel-teal)",
};

// eslint-disable-next-line no-control-regex
const CSI = /\x1b\[([0-9;?]*)([A-Za-z])/g;
// eslint-disable-next-line no-control-regex
const ESC = /\x1b/;

/** Colours and weight from SGR codes; every other escape sequence is dropped. */
export function parseAnsi(raw: string): Seg[] {
  if (!ESC.test(raw)) return [{ text: raw }];
  const out: Seg[] = [];
  let st: Omit<Seg, "text"> = {};
  let at = 0;
  for (const m of raw.matchAll(CSI)) {
    if (m.index! > at) out.push({ text: raw.slice(at, m.index), ...st });
    at = m.index! + m[0].length;
    if (m[2] !== "m") continue;
    const codes = (m[1] || "0").split(";").map(Number);
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i];
      if (c === 0) st = {};
      else if (c === 1) st = { ...st, bold: true };
      else if (c === 2) st = { ...st, dim: true };
      else if (c === 22) st = { ...st, bold: false, dim: false };
      else if (c === 39) st = { ...st, color: undefined };
      else if (c === 38 || c === 48) i += codes[i + 1] === 5 ? 2 : codes[i + 1] === 2 ? 4 : 0;
      else if (FG[c]) st = { ...st, color: FG[c] };
      else if ((c >= 30 && c <= 37) || c === 97) st = { ...st, color: undefined };
    }
  }
  if (at < raw.length) out.push({ text: raw.slice(at), ...st });
  return out.filter((s) => s.text);
}

// eslint-disable-next-line no-control-regex
export const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, "").replace(/\x1b\][^\x07]*\x07/g, "");

// ------------------------------------------------------------------ the model

type Kind = "phase" | "end" | "step" | "quiet" | "error" | "warn" | "plain" | "blank";

/** Does this (plain) line read as an error, as the viewer paints it? */
export const isErrorLine = (t: string) => kindOf(t) === "error";

/** Errors and warnings by the box's one level rule (inferLevel), as the Logs page and the log store read them. */
function kindOf(t: string): Kind {
  const s = t.trim();
  if (!s) return "blank";
  const level = inferLevel(t);
  if (level === "error") return "error";
  if (level === "warn") return "warn";
  if (t.startsWith("==>")) return /^==> (built in|live in|release done in)\b/.test(t) ? "end" : "phase";
  if (/^#\d+ (CACHED|DONE [\d.]+s|\.\.\.)$/.test(t) || /^#\d+ (resolve|transferring|sha256:)/.test(t)) return "quiet";
  if (/^#\d+ \[/.test(t)) return "step";
  return "plain";
}

type Line = { n: number; text: string; segs: Seg[]; kind: Kind; at: number | null; group: number; stepSecs?: number };
type Group = { i: number; title: string; first: number; last: number; at: number | null; secs?: number; errors: number; warns: number; /** A phase line with nothing under it: shown as a note, not a step. */ note: boolean };

function build(raw: RawLine[]) {
  const lines: Line[] = [];
  const groups: Group[] = [];
  const stepDone = new Map<string, number>();
  for (const r of raw) {
    const m = /^#(\d+) DONE ([\d.]+)s/.exec(stripAnsi(r.raw));
    if (m) stepDone.set(m[1], Number(m[2]));
  }
  const firstStep = new Set<string>();
  raw.forEach((r, i) => {
    const text = stripAnsi(r.raw);
    const kind = kindOf(text);
    if (kind === "phase" || groups.length === 0) {
      groups.push({ i: groups.length, title: kind === "phase" ? text.replace(/^==>\s*/, "") : "Output", first: i, last: i, at: r.at, errors: 0, warns: 0, note: false });
    }
    const g = groups[groups.length - 1];
    g.last = i;
    if (kind === "error") g.errors++;
    if (kind === "warn") g.warns++;
    const line: Line = { n: i + 1, text, segs: parseAnsi(r.raw), kind, at: r.at, group: g.i };
    if (kind === "step") {
      const id = /^#(\d+)/.exec(text)![1];
      if (!firstStep.has(id)) {
        firstStep.add(id);
        line.stepSecs = stepDone.get(id);
      }
    }
    lines.push(line);
  });
  // A step's time: from its first line to the next step's, when the lines arrived live.
  groups.forEach((g, k) => {
    g.note = g.first === g.last && lines[g.first]?.kind === "phase";
    const next = groups[k + 1];
    if (g.at !== null && next && next.at !== null) g.secs = (next.at - g.at) / 1000;
  });
  return { lines, groups, timed: raw.some((r) => r.at !== null) };
}

type Row = { type: "group"; g: Group } | { type: "line"; l: Line };

// ------------------------------------------------------------------ the view

export type BuildLogHandle = { jumpToFirstError: () => void };

export function BuildLogViewer({
  lines: raw,
  live,
  running,
  t0,
  file,
  failed,
  loadError,
  summary,
  handle,
  initialQuery,
  initialLine,
}: {
  lines: RawLine[];
  live: boolean;
  /** The deploy is still queued, building or starting. */
  running: boolean;
  t0?: number;
  /** The download's file name. */
  file: string;
  /** The deploy failed: errors are pointed at from the start. */
  failed?: boolean;
  loadError?: boolean;
  /** Timings from the deploy, shown on the toolbar. */
  summary?: ReactNode;
  handle?: Ref<BuildLogHandle>;
  /** Opens with this search filled in (from the Logs page's search). */
  initialQuery?: string;
  /** Opens at this line (its text, as the log store keeps it), else at the first match. */
  initialLine?: string;
}) {
  const { lines, groups, timed } = useMemo(() => build(raw), [raw]);
  const [closed, setClosed] = useState<Set<number>>(() => new Set());
  const [wrap, setWrap] = useState(true);
  const [query, setQuery] = useState(initialQuery ?? "");
  const [hit, setHit] = useState(0);
  const [follow, setFollow] = useState(true);
  const [flash, setFlash] = useState<number | null>(null);
  const [copied, setCopied] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  const rows = useMemo(() => {
    const out: Row[] = [];
    for (const g of groups) {
      out.push({ type: "group", g });
      if (closed.has(g.i)) continue;
      for (let i = g.first + (lines[g.first]?.kind === "phase" ? 1 : 0); i <= g.last; i++) out.push({ type: "line", l: lines[i] });
    }
    return out;
  }, [groups, lines, closed]);

  const needle = query.trim().toLowerCase();
  const matches = useMemo(() => (needle ? lines.filter((l) => l.text.toLowerCase().includes(needle)).map((l) => l.n - 1) : []), [lines, needle]);
  const errors = useMemo(() => lines.filter((l) => l.kind === "error").map((l) => l.n - 1), [lines]);
  const warns = useMemo(() => lines.filter((l) => l.kind === "warn").map((l) => l.n - 1), [lines]);
  const cur = matches.length ? Math.min(hit, matches.length - 1) : -1;

  // eslint-disable-next-line react-hooks/incompatible-library
  const v = useVirtualizer({
    count: rows.length,
    getScrollElement: () => box.current,
    estimateSize: (i) => (rows[i]?.type === "group" ? 30 : 20),
    overscan: 24,
  });

  // Follow the bottom while it builds and you're at the bottom.
  useEffect(() => {
    if (follow && live && rows.length) v.scrollToIndex(rows.length - 1, { align: "end" });
  }, [rows.length, follow, live, v]);

  /** Shows line i (0-based): opens its step, scrolls it to the middle, flashes it. */
  const show = (i: number) => {
    const g = lines[i]?.group;
    if (g === undefined) return;
    setFollow(false);
    const open = closed.has(g) ? new Set([...closed].filter((x) => x !== g)) : closed;
    if (open !== closed) setClosed(open);
    requestAnimationFrame(() => {
      const idx = rowIndexOf(groups, lines, open, i);
      if (idx >= 0) v.scrollToIndex(idx, { align: "center" });
      setFlash(i);
      setTimeout(() => setFlash((f) => (f === i ? null : f)), 1600);
    });
  };
  const nextOf = (list: number[]) => {
    if (!list.length) return;
    const from = flash ?? -1;
    show(list.find((x) => x > from) ?? list[0]);
  };
  const step = (d: 1 | -1) => {
    if (!matches.length) return;
    const k = (cur + d + matches.length) % matches.length;
    setHit(k);
    show(matches[k]);
  };

  useImperativeHandle(handle, () => ({ jumpToFirstError: () => errors.length && show(errors[0]) }));

  // Opened from a matching line: land on it once it has loaded (or on the
  // first match when the log is complete and the line isn't in it).
  const landed = useRef(!initialQuery && !initialLine);
  const land = useEffectEvent(() => {
    if (landed.current || !lines.length) return;
    const want = initialLine ? stripAnsi(initialLine).trim() : "";
    let i = want ? lines.findIndex((l) => l.text === want || l.text.trimEnd().endsWith(want)) : -1;
    if (i < 0) {
      if (live || !matches.length) return;
      i = matches[0];
    }
    landed.current = true;
    const k = matches.indexOf(i);
    if (k >= 0) setHit(k);
    show(i);
  });
  // Try again whenever more of the log (or its matches) arrives.
  useEffect(() => land(), [lines, matches, live]);

  const text = useMemo(() => lines.map((l) => l.text).join("\n"), [lines]);
  const steps = groups.filter((g) => !g.note);
  const allClosed = steps.length > 0 && steps.every((g) => closed.has(g.i));
  const items = v.getVirtualItems();

  return (
    <div className="overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-2 border-b border-rule bg-paper-raised px-2.5 py-2">
        <label className="relative min-w-0 flex-1 max-sm:basis-full sm:max-w-72">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-ink-3" />
          <input
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setHit(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                step(e.shiftKey ? -1 : 1);
              }
              if (e.key === "Escape") setQuery("");
            }}
            placeholder="Search the log"
            aria-label="Search the log"
            className="h-8 w-full rounded-[7px] border border-rule-2 bg-paper pr-20 pl-8 text-[0.8125rem] text-ink outline-hidden placeholder:text-ink-4 focus-visible:border-brass"
          />
          {needle && (
            <span className="absolute top-1/2 right-1 flex -translate-y-1/2 items-center gap-0.5">
              <span className="px-1 text-[0.6875rem] text-ink-3 tnum">{matches.length ? `${cur + 1}/${int(matches.length)}` : "0"}</span>
              <button type="button" aria-label="Previous match" onClick={() => step(-1)} className="grid size-6 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-hover hover:text-ink">
                <ChevronUp className="size-3.5" />
              </button>
              <button type="button" aria-label="Next match" onClick={() => step(1)} className="grid size-6 place-items-center rounded-[5px] text-ink-3 hover:bg-paper-hover hover:text-ink">
                <ChevronDown className="size-3.5" />
              </button>
            </span>
          )}
        </label>
        {errors.length > 0 && (
          <Button size="sm" variant="ghost" onClick={() => nextOf(errors)} aria-label={`Next error, ${count(errors.length, "error")}`}>
            <span className="size-1.5 rounded-full bg-danger" aria-hidden />
            <span className="text-danger">{count(errors.length, "error")}</span>
          </Button>
        )}
        {warns.length > 0 && (
          <Button size="sm" variant="ghost" onClick={() => nextOf(warns)} aria-label={`Next warning, ${count(warns.length, "warning")}`}>
            <span className="size-1.5 rounded-full bg-warn" aria-hidden />
            <span className="text-warn-ink">{count(warns.length, "warning")}</span>
          </Button>
        )}
        <span className="ml-auto flex items-center gap-0.5">
          <Toggle.Root pressed={wrap} onPressedChange={setWrap} asChild>
            <Button size="sm" variant="ghost" title={wrap ? "Long lines wrap" : "Long lines scroll sideways"} aria-label="Wrap long lines">
              <WrapText className={wrap ? "text-ink" : "text-ink-4"} />
              <span className="max-sm:hidden">Wrap</span>
            </Button>
          </Toggle.Root>
          <Button size="sm" variant="ghost" onClick={() => setClosed(allClosed ? new Set() : new Set(steps.map((g) => g.i)))} disabled={!steps.length}>
            <span className="max-sm:hidden">{allClosed ? "Expand all" : "Collapse all"}</span>
            <span className="sm:hidden">{allClosed ? "Expand" : "Collapse"}</span>
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={!text}
            aria-label="Copy the log"
            onClick={async () => {
              if (await copyText(text)) {
                setCopied(true);
                setTimeout(() => setCopied(false), 1400);
              }
            }}
          >
            {copied ? <Check className="text-ok" /> : <Copy />}
            <span className="max-sm:hidden">{copied ? "Copied" : "Copy"}</span>
          </Button>
          <Button size="sm" variant="ghost" disabled={!text} onClick={() => saveText(file, text)} aria-label="Download the log">
            <Download />
            <span className="max-sm:hidden">Download</span>
          </Button>
        </span>
      </div>
      {summary && <div className="border-b border-rule px-3.5 py-1.5 text-xs text-ink-3">{summary}</div>}

      <div className="relative">
        <div
          ref={box}
          role="log"
          aria-label="Build log"
          aria-live="off"
          tabIndex={0}
          onScroll={(e) => {
            const el = e.currentTarget;
            const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
            if (bottom !== follow) setFollow(bottom);
          }}
          className="h-[min(64vh,44rem)] min-h-72 overflow-auto font-mono text-[0.75rem] leading-5 [font-variant-ligatures:none] focus-visible:outline-offset-[-2px]"
        >
          {rows.length === 0 ? (
            <p className="px-4 py-4 font-sans text-sm text-ink-3">
              {loadError ? "The build log can’t be read right now." : running ? "Waiting for the first line…" : "No output."}
            </p>
          ) : (
            <div style={{ height: v.getTotalSize(), position: "relative", minWidth: wrap ? undefined : "max-content" }}>
              {items.map((it) => {
                const row = rows[it.index];
                const style: CSSProperties = { position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${it.start}px)` };
                if (row.type === "group")
                  return (
                    <GroupRow
                      key={`g${row.g.i}`}
                      ref={v.measureElement}
                      index={it.index}
                      style={style}
                      g={row.g}
                      open={!closed.has(row.g.i)}
                      elapsed={row.g.at !== null && t0 ? (row.g.at - t0) / 1000 : undefined}
                      toggle={() =>
                        setClosed((c) => {
                          const n = new Set(c);
                          if (n.has(row.g.i)) n.delete(row.g.i);
                          else n.add(row.g.i);
                          return n;
                        })
                      }
                    />
                  );
                const l = row.l;
                return (
                  <div
                    key={`l${l.n}`}
                    ref={v.measureElement}
                    data-index={it.index}
                    data-k={l.kind}
                    style={style}
                    className={cn(
                      timed ? "grid grid-cols-[3.25rem_3.5rem_minmax(0,1fr)] pr-4 max-sm:grid-cols-[2.75rem_minmax(0,1fr)]" : "grid grid-cols-[3.25rem_minmax(0,1fr)] pr-4 max-sm:grid-cols-[2.75rem_minmax(0,1fr)]",
                      flash === l.n - 1
                        ? "bg-brass-wash"
                        : l.kind === "error"
                          ? "bg-danger-wash shadow-[inset_2px_0_0_var(--danger)]"
                          : l.kind === "warn"
                            ? "bg-warn-wash shadow-[inset_2px_0_0_var(--warn)]"
                            : "hover:bg-[color-mix(in_oklch,var(--ink)_4%,transparent)]",
                    )}
                  >
                    <span className="pr-3 text-right text-[0.6875rem] text-ink-4 tnum select-none">{l.n}</span>
                    {timed && <span className="text-[0.6875rem] text-ink-4 tnum select-none max-sm:hidden">{l.at !== null && t0 ? `${dec((l.at - t0) / 1000, 1)}${NNBSP}s` : ""}</span>}
                    <span className={cn(wrap ? "break-words whitespace-pre-wrap [overflow-wrap:anywhere]" : "whitespace-pre", lineTone(l.kind))}>
                      {l.kind === "end" ? (
                        <span className="font-sans text-[0.8125rem] font-[550] text-ink">{l.text.replace(/^==>\s*/, "")}</span>
                      ) : l.text.startsWith("==> ") ? (
                        <span className="font-sans text-[0.8125rem] font-[550]">{l.text.slice(4)}</span>
                      ) : (
                        <Segs segs={l.segs} needle={needle} />
                      )}
                      {l.stepSecs !== undefined && <span className="ml-3 font-sans text-[0.6875rem] text-ink-4 tnum">{dec(l.stepSecs, 1)}{NNBSP}s</span>}
                    </span>
                  </div>
                );
              })}
            </div>
          )}
        </div>
        {!follow && live && (
          <button
            type="button"
            onClick={() => {
              setFollow(true);
              v.scrollToIndex(rows.length - 1, { align: "end" });
            }}
            className="absolute bottom-3 left-1/2 inline-flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-rule-2 bg-paper-raised px-3 py-1 text-xs font-[550] text-ink shadow-raised"
          >
            <ArrowDown className="size-3.5" /> Jump to the newest line
          </button>
        )}
      </div>
      <div className="flex items-center justify-between border-t border-rule px-3.5 py-1.5 text-[0.6875rem] text-ink-3">
        <span className="tnum">
          {count(lines.length, "line")}
          {failed && errors.length === 0 ? " · no line reads as an error; see what the box saw below" : ""}
        </span>
        <span>{live ? "Following as it builds" : running ? "Connecting…" : ""}</span>
      </div>
    </div>
  );
}

function rowIndexOf(groups: Group[], lines: Line[], closed: Set<number>, i: number) {
  let idx = 0;
  for (const g of groups) {
    idx++;
    if (closed.has(g.i)) continue;
    const start = g.first + (lines[g.first]?.kind === "phase" ? 1 : 0);
    if (i >= start && i <= g.last) return idx + (i - start);
    if (i === g.first && lines[g.first]?.kind === "phase") return idx - 1;
    idx += g.last - start + 1;
  }
  return -1;
}

const lineTone = (k: Kind) => (k === "error" ? "text-danger" : k === "warn" ? "text-warn-ink" : k === "quiet" ? "text-ink-4" : k === "step" ? "text-ink" : "text-ink-2");

function GroupRow({ g, open, toggle, elapsed, style, index, ref }: { g: Group; open: boolean; toggle: () => void; elapsed?: number; style: CSSProperties; index: number; ref: (el: HTMLElement | null) => void }) {
  const size = g.last - g.first;
  if (g.note)
    return (
      <div ref={ref} data-index={index} style={style} className="grid grid-cols-[3.25rem_minmax(0,1fr)_auto] items-center pr-3 font-sans max-sm:grid-cols-[2.75rem_minmax(0,1fr)_auto]">
        <span className="pr-3 text-right font-mono text-[0.6875rem] text-ink-4 tnum select-none">{g.first + 1}</span>
        <span className="truncate py-0.5 text-[0.8125rem] text-ink-2">{g.title}</span>
        {elapsed !== undefined && <span className="text-[0.6875rem] text-ink-4 tnum">at {dec(elapsed, 1)}{NNBSP}s</span>}
      </div>
    );
  return (
    <div ref={ref} data-index={index} style={style} className="pt-1.5">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        className={cn(
          "grid w-full grid-cols-[3.25rem_minmax(0,1fr)_auto] items-center pr-3 text-left font-sans hover:bg-[color-mix(in_oklch,var(--ink)_4%,transparent)] max-sm:grid-cols-[2.75rem_minmax(0,1fr)_auto]",
          g.errors > 0 && "shadow-[inset_2px_0_0_var(--danger)]",
        )}
      >
        <span className="grid place-items-center text-ink-3">{open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}</span>
        <span className="flex min-w-0 items-baseline gap-2 py-0.5">
          <span className={cn("truncate text-[0.8125rem] font-[550]", g.errors > 0 ? "text-danger" : "text-ink")}>{g.title}</span>
          {!open && size > 0 && <span className="shrink-0 text-[0.6875rem] text-ink-4">{count(size, "line")}</span>}
        </span>
        <span className="flex items-center gap-2.5 text-[0.6875rem] text-ink-3 tnum">
          {g.errors > 0 && <span className="text-danger">{count(g.errors, "error")}</span>}
          {g.warns > 0 && <span className="text-warn-ink">{count(g.warns, "warning")}</span>}
          {g.secs !== undefined ? <span>{dec(g.secs, 1)}{NNBSP}s</span> : elapsed !== undefined ? <span className="text-ink-4">at {dec(elapsed, 1)}{NNBSP}s</span> : null}
        </span>
      </button>
    </div>
  );
}

function Segs({ segs, needle }: { segs: Seg[]; needle: string }) {
  return (
    <>
      {segs.map((s, i) => {
        const style: CSSProperties | undefined = s.color ? { color: s.color } : undefined;
        const cls = cn(s.bold && "font-[600]", s.dim && "opacity-60");
        return (
          <span key={i} style={style} className={cls || undefined}>
            {needle ? <Mark text={s.text} needle={needle} /> : s.text}
          </span>
        );
      })}
    </>
  );
}

function Mark({ text, needle }: { text: string; needle: string }) {
  const low = text.toLowerCase();
  if (!low.includes(needle)) return <>{text}</>;
  const parts: ReactNode[] = [];
  let at = 0;
  for (let i = low.indexOf(needle); i >= 0; i = low.indexOf(needle, at)) {
    parts.push(text.slice(at, i), <mark key={i} className="rounded-[2px] bg-brass-wash text-ink shadow-[0_0_0_1px_var(--brass)]">{text.slice(i, i + needle.length)}</mark>);
    at = i + needle.length;
  }
  parts.push(text.slice(at));
  return <>{parts}</>;
}

export function saveText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
