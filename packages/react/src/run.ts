"use client";
// Live progress of a background job or workflow run, streamed by the box on
// this app's own host (GET /_tiffin/runs/{id}/events, server-sent events).
// The server mints the token: sendWithToken / startWithToken (tiffin-sdk/queue,
// tiffin-sdk/workflow) return { id, token } from a server action.
//
//   const run = useRun<{ pct: number }>(id, token);
//   run.status, run.progress?.pct, run.output, run.error, run.chunks
import { useEffect, useRef, useState } from "react";

export interface RunStep {
  name: string;
  kind: "step" | "sleep" | "event" | "approval" | "webhook";
  state: "completed" | "failed" | "waiting" | "timed_out" | "cancelled";
  startedAt: string;
  finishedAt?: string;
  waitUntil?: string;
}

/** The job or run as the box reports it. */
export interface RunSnapshot<P = unknown, O = unknown> {
  id: string;
  type: "job" | "run";
  /** Queue or workflow name. */
  name: string;
  /** Jobs: scheduled, queued, running, retrying, completed, dead, cancelled. Runs: running, waiting, completed, failed, cancelled. */
  status: string;
  done: boolean;
  progress: P | null;
  output: O | null;
  error?: string;
  attempt?: number;
  waitingFor?: string;
  steps?: RunStep[];
}

export interface LiveRun<P = unknown, O = unknown, C = unknown> {
  /** null until the first state arrives. */
  status: string | null;
  /** The latest value the job or run reported (job.progress, ctx.progress). */
  progress: P | null;
  /** The result, once it completed. */
  output: O | null;
  /** Why it failed, or why the browser cannot watch it (a bad or expired token). */
  error: string | null;
  /** Output chunks (job.log, ctx.stream), in order. */
  chunks: C[];
  done: boolean;
  /** True while the stream is open (it reconnects by itself). */
  connected: boolean;
  run: RunSnapshot<P, O> | null;
}

export interface SubscribeOptions {
  /** Origin of the app; default this page's. */
  baseUrl?: string;
  fetch?: typeof fetch;
}

const empty: LiveRun<any, any, any> = { status: null, progress: null, output: null, error: null, chunks: [], done: false, connected: false, run: null };

type SSE = { id?: string; event: string; data: string };

async function* events(body: ReadableStream<Uint8Array>): AsyncGenerator<SSE> {
  const reader = body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  let ev: SSE = { event: "message", data: "" };
  let hasData = false;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      buf += dec.decode(value, { stream: true });
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).replace(/\r$/, "");
        buf = buf.slice(nl + 1);
        if (line === "") {
          if (hasData) yield ev;
          ev = { event: "message", data: "" };
          hasData = false;
          continue;
        }
        if (line.startsWith(":")) continue;
        const i = line.indexOf(":");
        const field = i < 0 ? line : line.slice(0, i);
        const value = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
        if (field === "id") ev.id = value;
        else if (field === "event") ev.event = value;
        else if (field === "data") {
          ev.data = hasData ? ev.data + "\n" + value : value;
          hasData = true;
        }
      }
    }
  } finally {
    reader.releaseLock();
  }
}

/**
 * Watches a job or run; onChange gets every new state. Reconnects with
 * Last-Event-ID (backing off up to 15 s) until the job or run finishes or the
 * returned function is called.
 */
export function subscribeRun<P = unknown, O = unknown, C = unknown>(
  id: string,
  token: string,
  onChange: (s: LiveRun<P, O, C>) => void,
  opts: SubscribeOptions = {},
): () => void {
  const f = opts.fetch ?? fetch;
  const base = (opts.baseUrl ?? "").replace(/\/$/, "");
  let state: LiveRun<P, O, C> = empty;
  let stopped = false;
  let lastId = "";
  let ctrl: AbortController | undefined;
  const set = (patch: Partial<LiveRun<P, O, C>>) => {
    state = { ...state, ...patch };
    if (!stopped) onChange(state);
  };
  const run = async () => {
    for (let attempt = 0; !stopped; attempt++) {
      ctrl = new AbortController();
      try {
        const headers: Record<string, string> = { accept: "text/event-stream", authorization: `Bearer ${token}` };
        if (lastId) headers["last-event-id"] = lastId;
        const res = await f(`${base}/_tiffin/runs/${encodeURIComponent(id)}/events`, { headers, signal: ctrl.signal, cache: "no-store" });
        if (!res.ok || !res.body) {
          const p = (await res.json().catch(() => ({}))) as { detail?: string; hint?: string };
          const msg = p.detail ? (p.hint ? `${p.detail} (${p.hint})` : p.detail) : `HTTP ${res.status}`;
          if (res.status === 401 || res.status === 403 || res.status === 404) return set({ error: msg, connected: false });
          throw new Error(msg);
        }
        attempt = 0;
        set({ connected: true });
        for await (const ev of events(res.body)) {
          if (ev.id !== undefined) lastId = ev.id;
          if (ev.event === "state") {
            const s = JSON.parse(ev.data) as RunSnapshot<P, O>;
            set({ run: s, status: s.status, progress: s.progress ?? null, output: s.output ?? null, error: s.error || null, done: s.done });
          } else if (ev.event === "output") {
            set({ chunks: [...state.chunks, JSON.parse(ev.data) as C] });
          } else if (ev.event === "end") {
            ctrl.abort();
            return set({ connected: false });
          }
        }
      } catch {
        if (stopped) return;
      }
      if (state.done) return set({ connected: false });
      set({ connected: false });
      await new Promise((r) => setTimeout(r, Math.min(1000 * 2 ** attempt, 15_000)));
    }
  };
  void run();
  return () => {
    stopped = true;
    ctrl?.abort();
  };
}

/**
 * Live state of a job or workflow run: status, progress, output, error and
 * output chunks. Pass the id and token a server action returned; null or
 * undefined waits.
 */
export function useRun<P = unknown, O = unknown, C = unknown>(
  id: string | null | undefined,
  token: string | null | undefined,
  opts: SubscribeOptions = {},
): LiveRun<P, O, C> {
  const [state, setState] = useState<LiveRun<P, O, C>>(empty);
  const fetchRef = useRef(opts.fetch);
  fetchRef.current = opts.fetch;
  useEffect(() => {
    setState(empty);
    if (!id || !token) return;
    const o: SubscribeOptions = {};
    if (opts.baseUrl) o.baseUrl = opts.baseUrl;
    if (fetchRef.current) o.fetch = fetchRef.current;
    return subscribeRun<P, O, C>(id, token, setState, o);
  }, [id, token, opts.baseUrl]);
  return state;
}

/** useRun for a queue job (job_...). */
export const useJob = useRun;
