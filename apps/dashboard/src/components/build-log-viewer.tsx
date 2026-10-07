import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, Check, ChevronDown, ChevronRight, ChevronUp, Copy, Download, Search, WrapText } from "lucide-react";
import { Toggle } from "radix-ui";
import { useEffect, useEffectEvent, useImperativeHandle, useMemo, useRef, useState, type CSSProperties, type ReactNode, type Ref } from "react";
import { stripAnsi, type Group, type Kind, type Line, type Seg } from "@/components/build-log-model";
import { buildLogText, type BuildLogState } from "@/components/build-log-stream";
import { toast } from "@/components/toast";
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
 * 10,000-line log scrolls as smoothly as a short one; the log itself is
 * parsed as it arrives and bounded (components/build-log-model.ts), with
 * Download fetching the whole of it from the box.
 */

type Row = { type: "group"; g: Group } | { type: "line"; l: Line };

// ------------------------------------------------------------------ the view

export type BuildLogHandle = { jumpToFirstError: () => void };

export function BuildLogViewer({
  log,
  source,
  running,
  t0,
  file,
  failed,
  summary,
  handle,
  initialQuery,
  initialLine,
}: {
  log: BuildLogState;
  /** The deploy whose log this is, for Download (the whole log, from the box). */
  source: { project: string; app: string; id: string };
  /** The deploy is still queued, building or starting. */
  running: boolean;
  t0?: number;
  /** The download's file name. */
  file: string;
  /** The deploy failed: errors are pointed at from the start. */
  failed?: boolean;
  /** Timings from the deploy, shown on the toolbar. */
  summary?: ReactNode;
  handle?: Ref<BuildLogHandle>;
  /** Opens with this search filled in (from the Logs page's search). */
  initialQuery?: string;
  /** Opens at this line (its text, as the log store keeps it), else at the first match. */
  initialLine?: string;
}) {
  const { live, error: loadError } = log;
  // The model grows in place; each published version is a new `log`. Fresh array identities per version
  // (a copy of references, not a re-parse) let everything below memoise on them.
  const { lines, groups, timed, dropped, errors, warns } = useMemo(() => {
    const m = log.model;
    return { lines: m.lines.slice(), groups: m.groups.slice(), timed: m.timed, dropped: m.dropped, errors: m.errors.slice(), warns: m.warns.slice() };
  }, [log]);
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
  const matches = useMemo(() => (needle ? lines.flatMap((l, i) => (l.text.toLowerCase().includes(needle) ? [i] : [])) : []), [lines, needle]);
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

  const empty = lines.length === 0;
  const [saving, setSaving] = useState(false);
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
            disabled={empty}
            aria-label="Copy the log"
            onClick={async () => {
              if (await copyText(log.model.text())) {
                setCopied(true);
                setTimeout(() => setCopied(false), 1400);
              }
            }}
          >
            {copied ? <Check className="text-ok" /> : <Copy />}
            <span className="max-sm:hidden">{copied ? "Copied" : "Copy"}</span>
          </Button>
          <Button
            size="sm"
            variant="ghost"
            disabled={empty || saving}
            onClick={async () => {
              setSaving(true);
              try {
                saveText(file, await buildLogText(source.project, source.app, source.id));
              } catch {
                toast({ title: "Couldn’t download the build log.", tone: "danger" });
              } finally {
                setSaving(false);
              }
            }}
            aria-label="Download the log"
          >
            <Download />
            <span className="max-sm:hidden">Download</span>
          </Button>
        </span>
      </div>
      {summary && <div className="border-b border-rule px-3.5 py-1.5 text-xs text-ink-3">{summary}</div>}
      {dropped > 0 && (
        <div className="border-b border-rule px-3.5 py-1.5 text-xs text-ink-3">
          The first {count(dropped, "line")} aren’t shown here, to keep this page quick. Download has the whole log.
        </div>
      )}

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
                      flash === l.n - 1 - dropped
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
                      {l.cut !== undefined && <span className="ml-2 font-sans text-[0.6875rem] text-ink-4">… {count(l.cut, "more character")} (Download has them)</span>}
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
          {count(dropped + lines.length, "line")}
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
