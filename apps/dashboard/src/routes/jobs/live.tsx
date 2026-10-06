// Live progress for a job or a workflow run: one server-sent events stream
// from the box (the session cookie authorises it), shared by the split view
// and the full pages. While it is connected nothing polls; each state change
// refreshes the job or run once, so attempts and steps stay exact.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { jobsApi, type LiveState } from "@/api/jobs";
import type { QueueJob, WorkflowRun, WorkflowStep } from "@/api/modules";
import { Untrusted } from "@/components/page";
import { cn } from "@/lib/cn";
import { int, ms } from "@/lib/format";
import { relative } from "@/lib/time";

export type Chunk = { id: number; data: unknown };

/** Follows one job or run while it is unfinished. `connected` is true while the stream is open. */
export function useLive(project: string, id: string, active: boolean) {
  const qc = useQueryClient();
  const [state, setState] = useState<LiveState | null>(null);
  const [chunks, setChunks] = useState<Chunk[]>([]);
  const [connected, setConnected] = useState(false);
  const [at, setAt] = useState<number>(0);
  useEffect(() => {
    if (!active || typeof EventSource === "undefined") return;
    const es = new EventSource(jobsApi.liveStream(project, id));
    const key = id.startsWith("run_") ? ["run", project, id] : ["job", project, id];
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = () => {
      clearTimeout(timer);
      timer = setTimeout(() => void qc.invalidateQueries({ queryKey: key }), 250);
    };
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false); // EventSource reconnects with Last-Event-ID by itself
    es.addEventListener("state", (ev) => {
      setState(JSON.parse((ev as MessageEvent).data) as LiveState);
      setAt(Date.now());
      refresh();
    });
    es.addEventListener("output", (ev) => {
      const m = ev as MessageEvent;
      let data: unknown = m.data;
      try {
        data = JSON.parse(m.data);
      } catch {
        // keep the raw text
      }
      setChunks((c) => (c.some((x) => x.id === Number(m.lastEventId)) ? c : [...c.slice(-499), { id: Number(m.lastEventId), data }]));
      setAt(Date.now());
    });
    es.addEventListener("end", () => {
      es.close();
      setConnected(false);
      refresh();
      void qc.invalidateQueries({ queryKey: ["queue-stats", project] });
    });
    return () => {
      clearTimeout(timer);
      es.close();
    };
  }, [project, id, active, qc]);
  return { state, chunks, connected, at };
}

/** The time, moving once a second while `ticking` (the waterfall's "now" grows with it). */
export function useNow(ticking: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!ticking) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [ticking]);
  return now;
}

// ------------------------------------------------------------------ progress

/**
 * Whatever an app reports with job.progress / ctx.progress, read as a share
 * and a few words: 0.62, 62, { pct: 62 }, { done: 31, total: 50 },
 * { message: "Rendering pages", percent: 62 }.
 */
export function readProgress(v: unknown): { share?: number; text?: string } | null {
  if (v === null || v === undefined) return null;
  if (typeof v === "number") return { share: v <= 1 ? v : v / 100 };
  if (typeof v === "string") return { text: v };
  if (typeof v !== "object" || Array.isArray(v)) return { text: JSON.stringify(v) };
  const o = v as Record<string, unknown>;
  const n = (k: string) => (typeof o[k] === "number" ? (o[k] as number) : undefined);
  const s = (k: string) => (typeof o[k] === "string" ? (o[k] as string) : undefined);
  const total = n("total") ?? n("of");
  const done = n("done") ?? n("current") ?? n("completed") ?? n("count");
  let share: number | undefined;
  const pct = n("pct") ?? n("percent") ?? n("percentage");
  if (pct !== undefined) share = pct / 100;
  else if (total && done !== undefined) share = done / total;
  else if (n("progress") !== undefined) share = n("progress")! <= 1 ? n("progress") : n("progress")! / 100;
  let text = s("message") ?? s("label") ?? s("stage") ?? s("step") ?? s("status");
  if (!text && total && done !== undefined) text = `${int(done)} of ${int(total)}`;
  if (share === undefined && !text) text = JSON.stringify(v);
  return { share: share === undefined ? undefined : Math.max(0, Math.min(1, share)), text };
}

/** The reported progress: words, a share and a brass bar, said politely to screen readers. */
export function Progress({ value, live, at, source }: { value: unknown; live: boolean; at?: number; source: string }) {
  const p = readProgress(value);
  const [, tick] = useState(0);
  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => tick((n) => n + 1), 15_000);
    return () => clearInterval(t);
  }, [live]);
  if (!p) return null;
  const share = p.share;
  return (
    <div className="grid gap-1.5">
      <div className="flex items-baseline justify-between gap-3 text-[0.875rem]">
        <span className="min-w-0 truncate text-ink" aria-live="polite">
          {p.text ?? (live ? "Working" : "Last reported")}
        </span>
        {share !== undefined && <span className="text-ink tnum">{Math.round(share * 100)}%</span>}
      </div>
      {share !== undefined && (
        <div
          role="progressbar"
          aria-label="Progress"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(share * 100)}
          className="h-2 overflow-hidden rounded-full bg-paper-sunk"
        >
          <span className={cn("block h-full rounded-full", live ? "bg-brass" : "bg-ink-3")} style={{ width: `${share * 100}%` }} />
        </div>
      )}
      <span className="text-xs text-ink-3">
        From <code className="ident text-[0.7rem]">{source}</code>
        {live ? <> · live{at ? `, updated ${relative(new Date(at).toISOString())}` : ""}</> : ""}
      </span>
    </div>
  );
}

/** Output chunks (job.log / ctx.log), oldest first, as plain text the app wrote. */
export function Output({ chunks, live }: { chunks: Chunk[]; live: boolean }) {
  const end = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  useEffect(() => {
    const el = end.current?.parentElement;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [chunks.length]);
  if (chunks.length === 0) return null;
  return (
    <Untrusted label={live ? "Output, as your app writes it. Shown as plain text." : "Output your app wrote. Shown as plain text."}>
      <div
        className="max-h-64 overflow-auto px-4 py-3 font-mono text-[0.75rem] leading-5 text-ink-2"
        tabIndex={0}
        aria-label="Output"
        onScroll={(e) => {
          const el = e.currentTarget;
          follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {chunks.map((c) => (
          <div key={c.id} className="break-words whitespace-pre-wrap">
            {typeof c.data === "string" ? c.data : JSON.stringify(c.data)}
          </div>
        ))}
        <div ref={end} />
      </div>
    </Untrusted>
  );
}

// ------------------------------------------------------------------ waterfall

/** Sleeps are hatched, as in the mockup (a pattern, not a fill). */
const HATCH = "bg-[repeating-linear-gradient(90deg,var(--rule-3)_0_3px,transparent_3px_7px)]";

type Seg = { from: number; to: number; kind: "wait" | "run" | "now" | "sleep" | "fail" };
type Row = { label: ReactNode; segs: Seg[]; note: string; title?: string };

const segClass: Record<Seg["kind"], string> = {
  wait: "bg-[var(--seg-off)]",
  run: "bg-ink-2",
  now: "bg-brass",
  fail: "bg-danger",
  sleep: HATCH,
};

const keyWords: Record<Seg["kind"], string> = { wait: "waiting", run: "running", now: "running now", sleep: "sleeping", fail: "a try that failed" };

const t = (iso?: string | null) => (iso ? new Date(iso).getTime() : undefined);

/**
 * Time across, one row per step or try: grey is waiting (in the queue or for
 * an event), ink is running, brass is running now, red is a try that failed,
 * hatching is a sleep. The axis runs from the first moment to now (or the end).
 */
export function Waterfall({ rows, start, end, label }: { rows: Row[]; start: number; end: number; label: string }) {
  const span = Math.max(1, end - start);
  const x = (v: number) => `${((Math.max(start, Math.min(end, v)) - start) / span) * 100}%`;
  const w = (a: number, b: number) => `${Math.max(0.6, ((Math.min(end, b) - Math.max(start, a)) / span) * 100)}%`;
  if (rows.length === 0) return null;
  return (
    <figure aria-label={label} className="m-0">
      <ol className="grid grid-cols-[minmax(0,9.5rem)_minmax(0,1fr)_4.5rem] items-center gap-x-3 gap-y-2 sm:grid-cols-[minmax(0,11rem)_minmax(0,1fr)_5rem]">
        {rows.map((r, i) => (
          <li key={i} className="contents">
            <span className="truncate text-[0.8125rem] text-ink" title={r.title}>
              {r.label}
            </span>
            <span className="relative h-3.5" aria-hidden>
              {r.segs.map((s, k) => (
                <i key={k} className={cn("absolute inset-y-0 rounded-[4px]", segClass[s.kind])} style={{ left: x(s.from), width: w(s.from, s.to) }} />
              ))}
            </span>
            <span className="text-right text-[0.78125rem] text-ink-3 tnum">{r.note}</span>
          </li>
        ))}
      </ol>
      <figcaption className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
        {(Object.keys(keyWords) as Array<Seg["kind"]>)
          .filter((k) => rows.some((r) => r.segs.some((s) => s.kind === k)))
          .map((k) => (
            <Key key={k} c={segClass[k]}>
              {keyWords[k]}
            </Key>
          ))}
      </figcaption>
    </figure>
  );
}

function Key({ c, children }: { c: string; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <i aria-hidden className={cn("inline-block h-2.5 w-4 rounded-[3px]", c)} />
      {children}
    </span>
  );
}

/** A job's tries: the wait before each (queue or backoff), then the try itself. */
export function jobRows(j: QueueJob, now: number): { rows: Row[]; start: number; end: number } {
  const atts = j.attempts ?? [];
  const start = Math.min(t(j.enqueuedAt)!, t(j.runAt)!);
  let prev = t(j.runAt)!;
  const rows: Row[] = atts.map((a) => {
    const s = t(a.startedAt)!;
    const e = s + a.durationMs;
    const segs: Seg[] = [];
    if (s - prev > 50) segs.push({ from: prev, to: s, kind: "wait" });
    segs.push({ from: s, to: e, kind: a.outcome === "ok" ? "run" : "fail" });
    prev = e;
    return { label: `Try ${int(a.attempt)}`, segs, note: ms(a.durationMs), title: a.error };
  });
  if (j.state === "running" && j.startedAt) {
    const s = t(j.startedAt)!;
    rows.push({ label: `Try ${int(j.attempt)}`, segs: [...(s - prev > 50 ? [{ from: prev, to: s, kind: "wait" as const }] : []), { from: s, to: now, kind: "now" }], note: "now" });
  } else if (["queued", "retrying", "scheduled"].includes(j.state)) {
    rows.push({ label: atts.length ? `Try ${int(atts.length + 1)}` : "First try", segs: [{ from: prev, to: Math.max(now, t(j.runAt)!), kind: "wait" }], note: "next" });
  }
  const end = Math.max(now, ...rows.flatMap((r) => r.segs.map((s) => s.to)));
  return { rows, start, end: j.finishedAt ? Math.max(t(j.finishedAt)!, ...rows.flatMap((r) => r.segs.map((s) => s.to))) : end };
}

/**
 * A run's steps in call order. A turn's wait in the queue shows before its
 * first step; sleeps are hatched up to their wake time; event and approval
 * waits are grey; the step running now is brass.
 */
export function runRows(run: WorkflowRun, steps: WorkflowStep[], now: number): { rows: Row[]; start: number; end: number } {
  const start = t(run.createdAt)!;
  const turns = (run.turnJobs ?? [])
    .flatMap((j) => (j.attempts ?? []).map((a) => ({ queued: t(j.runAt)!, start: t(a.startedAt)!, end: t(a.startedAt)! + a.durationMs })))
    .sort((a, b) => a.start - b.start);
  const used = new Set<number>();
  const rows: Row[] = steps
    .filter((s) => s.kind !== "patch")
    .map((s) => {
      const from = t(s.startedAt)!;
      const to = t(s.finishedAt) ?? (s.state === "waiting" ? Math.max(now, from) : from + (s.durationMs ?? 0));
      const segs: Seg[] = [];
      const turn = turns.findIndex((x, i) => !used.has(i) && x.start <= from + 1000 && from <= x.end + 1000);
      if (turn >= 0 && turns[turn].start - turns[turn].queued > 50) {
        used.add(turn);
        segs.push({ from: turns[turn].queued, to: turns[turn].start, kind: "wait" });
      }
      if (s.kind === "sleep") segs.push({ from, to: t(s.waitUntil) && s.state === "waiting" ? t(s.waitUntil)! : to, kind: "sleep" });
      else if (s.kind !== "step") segs.push({ from, to, kind: "wait" });
      else segs.push({ from, to, kind: s.state === "failed" ? "fail" : "run" });
      const dur = to - from;
      const note =
        s.state === "waiting"
          ? s.kind === "sleep" && s.waitUntil
            ? `until ${relative(s.waitUntil).replace(/^in /, "")}`
            : "waiting"
          : s.attempts > 1
            ? `try ${int(s.attempts)}`
            : ms(Math.max(0, dur));
      return { label: s.kind === "sleep" ? `wait ${s.title ?? s.name}` : (s.title ?? s.name), segs, note, title: s.error };
    });
  if (run.state === "running") {
    const last = Math.max(start, ...rows.flatMap((r) => r.segs.map((s) => s.to)));
    const cur = turns.filter((x) => x.start >= last - 1000).pop();
    rows.push({ label: "next step", segs: [{ from: cur?.start ?? last, to: now, kind: "now" }], note: "now" });
  }
  const finished = t(run.finishedAt);
  const sleeping = rows.flatMap((r) => r.segs.filter((s) => s.kind === "sleep").map((s) => s.to));
  const end = finished ?? Math.max(now, ...sleeping, ...rows.flatMap((r) => r.segs.map((s) => s.to)));
  return { rows, start, end: Math.max(end, start + 1000) };
}
