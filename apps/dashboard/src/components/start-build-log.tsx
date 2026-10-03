import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { mod3 } from "@/api/modules";
import { cn } from "@/lib/cn";
import { dec, NNBSP } from "@/lib/format";

/**
 * A deploy's build log: what's there so far, then followed over server-sent
 * events while the deploy runs. Each line remembers when it arrived, so the
 * phase lines ("==> building the image") carry an honest elapsed time.
 */
export function useBuildLog(project: string, app: string, id: string | undefined, createdAt?: string) {
  const qc = useQueryClient();
  // Keyed by deploy, so a new deploy starts from an empty log without resetting state in the effect.
  const [log, setLog] = useState<{ id?: string; lines: Line[]; live: boolean; done: boolean }>({ lines: [], live: false, done: false });
  const partial = useRef("");

  useEffect(() => {
    if (!id) return;
    let es: EventSource | null = null;
    let cancelled = false;
    partial.current = "";
    const update = (f: (l: { lines: Line[]; live: boolean; done: boolean }) => Partial<{ lines: Line[]; live: boolean; done: boolean }>) =>
      setLog((prev) => {
        const base = prev.id === id ? prev : { id, lines: [], live: false, done: false };
        return { ...base, ...f(base), id };
      });
    const push = (text: string, at: number | null) => {
      const all = partial.current + text;
      const parts = all.split("\n");
      partial.current = parts.pop() ?? "";
      if (parts.length) update((l) => ({ lines: [...l.lines, ...parts.map((t) => ({ text: clean(t), at }))] }));
    };
    const flush = () => {
      const rest = partial.current;
      partial.current = "";
      if (rest) update((l) => ({ lines: [...l.lines, { text: clean(rest), at: Date.now() }] }));
    };
    mod3
      .buildLog(project, app, id)
      .then((b) => {
        if (cancelled) return;
        update(() => ({ lines: [] }));
        push(b.text, null);
        if (b.done) {
          flush();
          update(() => ({ done: true }));
          return;
        }
        es = new EventSource(mod3.buildLogStream(project, app, id, b.offset));
        es.onopen = () => update(() => ({ live: true }));
        es.addEventListener("log", (e) => push((JSON.parse((e as MessageEvent).data) as { text: string }).text, Date.now()));
        es.addEventListener("done", () => {
          flush();
          update(() => ({ live: false, done: true }));
          es?.close();
          void qc.invalidateQueries({ queryKey: ["deploy", project, app, id] });
          void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
        });
        es.onerror = () => update(() => ({ live: false }));
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
      es?.close();
    };
  }, [project, app, id, qc]);

  const cur = log.id === id ? log : { lines: [] as Line[], live: false, done: false };
  const { lines, live, done } = cur;
  const t0 = createdAt ? Date.parse(createdAt) : undefined;
  return { lines, live, done, t0 };
}

type Line = { text: string; at: number | null };

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[A-Za-z]/g;
const clean = (s: string) => s.replace(ANSI, "").replace(/\r/g, "");

type Kind = "phase" | "step" | "quiet" | "error" | "plain" | "blank";
function kindOf(t: string): Kind {
  if (!t.trim()) return "blank";
  if (t.startsWith("==>")) return "phase";
  if (/^(error|Error|[A-Z][a-zA-Z]+Error|fatal|panic|ERR!?)\b[:\s]/.test(t.trim()) || /exited with code [1-9]/.test(t)) return "error";
  if (/^#\d+ (CACHED|DONE [\d.]+s|\.\.\.)$/.test(t) || /^#\d+ (resolve|transferring)/.test(t)) return "quiet";
  if (/^#\d+ \[?[a-z]/i.test(t) && !/^#\d+ \d/.test(t)) return "step";
  return "plain";
}

/** Seconds a phase line states itself ("built in 7.3s", "live in 7.6s total"). */
function stated(t: string): number | undefined {
  const m = /\b(?:built|live) in ([\d.]+)s/.exec(t);
  return m ? Number(m[1]) : undefined;
}

/**
 * The log itself, in Commit Mono: phase lines as headings with their elapsed
 * time, BuildKit's bookkeeping quieter, errors in red. Follows the bottom
 * while you're at the bottom; scroll up and it stays put until you come back.
 */
export function BuildLogView({
  lines,
  live,
  t0,
  waiting,
  className,
  maxHeight = "60vh",
}: {
  lines: Line[];
  live: boolean;
  t0?: number;
  waiting?: boolean;
  className?: string;
  maxHeight?: string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines.length, follow]);

  const rows = useMemo(
    () =>
      lines.map((l, i) => {
        const k = kindOf(l.text);
        let secs: number | undefined;
        if (k === "phase") secs = i === 0 ? 0 : (stated(l.text) ?? (l.at !== null && t0 ? (l.at - t0) / 1000 : undefined));
        return { ...l, k, secs, n: i + 1 };
      }),
    [lines, t0],
  );

  return (
    <div className={cn("relative overflow-hidden rounded-[10px] border border-rule-2 bg-paper-sunk", className)}>
      <div
        ref={box}
        role="log"
        aria-live="off"
        aria-label="Build log"
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget;
          setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
        }}
        style={{ maxHeight }}
        className="overflow-auto py-2 font-mono text-[0.75rem] leading-[1.25rem] [font-variant-ligatures:none] focus-visible:outline-offset-[-2px]"
      >
        {rows.length === 0 ? (
          <p className="px-4 py-3 font-sans text-sm text-ink-3">{waiting ? "Waiting for the first line…" : "No output."}</p>
        ) : (
          rows.map((r) =>
            r.k === "blank" ? (
              <div key={r.n} className="h-2" aria-hidden />
            ) : (
              <div
                key={r.n}
                data-k={r.k}
                className={cn(
                  "grid grid-cols-[3.25rem_minmax(0,1fr)] gap-x-3 pr-4 hover:bg-[color-mix(in_oklch,var(--ink)_3%,transparent)]",
                  r.k === "phase" && "mt-1.5 first:mt-0",
                )}
              >
                <span className="text-right text-[0.6875rem] text-ink-4 tnum select-none">
                  {r.k === "phase" && r.secs !== undefined ? `${dec(r.secs, 1)}${NNBSP}s` : ""}
                </span>
                <span
                  className={cn(
                    "break-words whitespace-pre-wrap",
                    r.k === "phase" && "font-sans text-[0.8125rem] font-[550] text-ink",
                    r.k === "step" && "text-ink-2",
                    r.k === "plain" && "text-ink-2",
                    r.k === "quiet" && "text-ink-4",
                    r.k === "error" && "text-danger",
                  )}
                >
                  {r.k === "phase" ? r.text.replace(/^==>\s*/, "") : r.text}
                </span>
              </div>
            ),
          )
        )}
      </div>
      {!follow && live && (
        <button
          type="button"
          onClick={() => setFollow(true)}
          className="absolute right-3 bottom-3 rounded-full border border-rule-2 bg-paper-raised px-3 py-1 text-xs font-[550] text-ink shadow-raised"
        >
          Jump to the newest line
        </button>
      )}
    </div>
  );
}

/** The first line in build or start output that reads like the cause, not the runner's echo of it. */
export function firstError(text: string): string | null {
  for (const raw of text.split("\n")) {
    const l = raw.trim();
    if (!/^(error|Error|[A-Z][a-zA-Z]+Error|fatal|panic|ERR!?)\b[:\s]/.test(l)) continue;
    if (/script ".*" (exited|was terminated)|exited with code|Polite quit/.test(l)) continue;
    return l.length > 220 ? `${l.slice(0, 220)}…` : l;
  }
  return null;
}
