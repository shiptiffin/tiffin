import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { mod3 } from "@/api/modules";
import { BuildLogModel, stripAnsi } from "@/components/build-log-model";

/**
 * Reads a deploy's build log and follows it while it builds.
 *
 * The log is read by byte offset. A finished deploy's log is read page by
 * page until a page comes back empty: `done` on a page means the deploy is
 * over, not that this page is the end of its log. A running one is then
 * followed over server-sent events, resuming from the last offset received
 * when the stream drops or times out, so nothing is shown twice or skipped.
 * Output is parsed as it comes and the view is told at most every 100 ms.
 */

export type BuildLogState = { model: BuildLogModel; version: number; live: boolean; done: boolean; error: boolean };
export type BuildLogPage = { text: string; offset: number; done: boolean };
type Source = { addEventListener(type: string, f: (e: MessageEvent) => void): void; onopen: unknown; onerror: unknown; close(): void };
export type FollowDeps = {
  read: (offset: number) => Promise<BuildLogPage>;
  stream: (offset: number) => Source;
  onDone?: () => void;
  /** How long output collects before the view is told (ms). */
  batch?: number;
  /** The first wait before reconnecting a dropped stream (ms); it doubles up to 15 s. */
  retry?: number;
};

export function followBuildLog(deps: FollowDeps, publish: (s: BuildLogState) => void): () => void {
  const model = new BuildLogModel();
  const batch = deps.batch ?? 100;
  let offset = 0;
  let live = false;
  let done = false;
  let error = false;
  let stopped = false;
  let version = 0;
  let es: Source | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  const firstRetry = deps.retry ?? 1000;
  let backoff = firstRetry;

  const now = () => {
    if (timer) clearTimeout(timer);
    timer = null;
    if (!stopped) publish({ model, version: ++version, live, done, error });
  };
  const soon = () => {
    timer ??= setTimeout(now, batch);
  };
  /** The log is complete; `followed` when it was watched to the end (the deploy just finished). */
  const finish = (at: number | null, followed: boolean) => {
    model.end(at);
    done = true;
    live = false;
    es?.close();
    es = null;
    now();
    if (followed) deps.onDone?.();
  };

  const connect = () => {
    retry = null;
    if (stopped || done) return;
    const s = deps.stream(offset);
    es = s;
    const again = () => {
      if (es !== s) return;
      s.close();
      es = null;
      live = false;
      now();
      if (!stopped && !done) retry = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, 15_000);
    };
    s.onopen = () => {
      live = true;
      backoff = firstRetry;
      soon();
    };
    s.addEventListener("log", (e) => {
      const d = JSON.parse(e.data) as { text: string; offset?: number };
      model.push(d.text, Date.now());
      if (typeof d.offset === "number") offset = d.offset;
      soon();
    });
    s.addEventListener("done", () => finish(Date.now(), true));
    // The server ends a stream after a while ("timeout"); a dropped one errors. Either way, carry on from the offset.
    s.addEventListener("timeout", again);
    s.onerror = again;
  };

  void (async () => {
    try {
      for (;;) {
        const page = await deps.read(offset);
        if (stopped) return;
        model.push(page.text, null);
        offset = page.offset;
        if (!page.done) break;
        if (!page.text) return finish(null, false);
        soon();
      }
    } catch {
      if (!stopped) {
        error = true;
        now();
      }
      return;
    }
    now();
    connect();
  })();

  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    if (retry) clearTimeout(retry);
    es?.close();
  };
}

/** The whole log from the box (not just what the view keeps), ANSI stripped, for Download. */
export async function wholeBuildLog(read: (offset: number) => Promise<BuildLogPage>): Promise<string> {
  const parts: string[] = [];
  for (let offset = 0; ; ) {
    const page = await read(offset);
    if (!page.text || page.offset <= offset) break;
    parts.push(page.text);
    offset = page.offset;
  }
  return stripAnsi(parts.join("")).replace(/\r/g, "");
}

const EMPTY: BuildLogState = { model: new BuildLogModel(), version: 0, live: false, done: false, error: false };

/** A deploy's build log, read and then followed (see followBuildLog). */
export function useBuildLog(project: string, app: string, id: string | undefined): BuildLogState {
  const qc = useQueryClient();
  const [state, setState] = useState<BuildLogState & { id?: string }>(EMPTY);
  useEffect(() => {
    if (!id) return;
    return followBuildLog(
      {
        read: (offset) => mod3.buildLog(project, app, id, offset),
        stream: (offset) => new EventSource(mod3.buildLogStream(project, app, id, offset)) as unknown as Source,
        onDone: () => {
          void qc.invalidateQueries({ queryKey: ["deploy", project, app, id] });
          void qc.invalidateQueries({ queryKey: ["deploys", project, app] });
          void qc.invalidateQueries({ queryKey: ["project-deploys", project] });
        },
      },
      (s) => setState({ ...s, id }),
    );
  }, [project, app, id, qc]);
  return state.id === id && id ? state : EMPTY;
}

/** Download for a deploy's build log: the whole of it, from the box. */
export const buildLogText = (project: string, app: string, id: string) => wholeBuildLog((offset) => mod3.buildLog(project, app, id, offset));
