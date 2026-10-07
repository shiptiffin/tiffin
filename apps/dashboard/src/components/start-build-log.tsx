import { useEffect, useMemo, useRef, useState } from "react";
import type { Kind as ModelKind } from "@/components/build-log-model";
import { useBuildLog, type BuildLogState } from "@/components/build-log-stream";
import { cn } from "@/lib/cn";
import { dec, int, NNBSP } from "@/lib/format";

/**
 * A deploy's build log while it launches: the shared reader (read, then
 * followed over server-sent events, bounded), with the deploy's start time
 * so phase lines ("==> building the image") carry an honest elapsed time.
 */
export function useLaunchBuildLog(project: string, app: string, id: string | undefined, createdAt?: string) {
  const log = useBuildLog(project, app, id);
  return { ...log, t0: createdAt ? Date.parse(createdAt) : undefined };
}

/** Lines shown here; the deploy's own page has the full viewer. */
const TAIL = 1000;

type Kind = "phase" | "step" | "quiet" | "error" | "plain" | "blank";
const kindHere = (k: ModelKind): Kind => (k === "end" ? "phase" : k === "warn" ? "plain" : k);

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
  log,
  t0,
  waiting,
  className,
  maxHeight = "60vh",
}: {
  log: BuildLogState;
  t0?: number;
  waiting?: boolean;
  className?: string;
  maxHeight?: string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(true);
  const { live } = log;
  const rows = useMemo(() => {
    const all = log.model.lines;
    return all.slice(-TAIL).map((l) => {
      const k = kindHere(l.kind);
      let secs: number | undefined;
      if (k === "phase") secs = l.n === 1 ? 0 : (stated(l.text) ?? (l.at !== null && t0 ? (l.at - t0) / 1000 : undefined));
      return { text: l.text, k, secs, n: l.n };
    });
  }, [log, t0]);
  const hidden = log.model.dropped + Math.max(0, log.model.lines.length - TAIL);
  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [rows, follow]);

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
        {hidden > 0 && <p className="px-4 pb-1 font-sans text-xs text-ink-3">{int(hidden)} earlier lines are on the deploy’s page.</p>}
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
    const l = cause(raw);
    if (l) return l;
  }
  return null;
}

/** firstError over the lines of a build log as kept. */
export function firstErrorIn(log: BuildLogState): string | null {
  for (const line of log.model.lines) {
    const l = cause(line.text);
    if (l) return l;
  }
  return null;
}

function cause(raw: string): string | null {
  const l = raw.trim();
  if (!/^(error|Error|[A-Z][a-zA-Z]+Error|fatal|panic|ERR!?)\b[:\s]/.test(l)) return null;
  if (/script ".*" (exited|was terminated)|exited with code|Polite quit/.test(l)) return null;
  return l.length > 220 ? `${l.slice(0, 220)}…` : l;
}
