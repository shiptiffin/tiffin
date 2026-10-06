"use client";
// Live progress of a background job or workflow run, streamed by the box on
// this app's own host (GET /_tiffin/runs/{id}/events, server-sent events).
// The server mints the token: sendWithToken / startWithToken (tiffin-sdk/queue,
// tiffin-sdk/workflow) return { id, token } from a server action.
//
//   const run = useRun<{ pct: number }>(id, token);
//   run.status, run.progress?.pct, run.output, run.error, run.chunks
import { useEffect, useRef, useState } from "react";
const empty = { status: null, progress: null, output: null, error: null, chunks: [], done: false, connected: false, run: null };
async function* events(body) {
    const reader = body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    let ev = { event: "message", data: "" };
    let hasData = false;
    try {
        for (;;) {
            const { value, done } = await reader.read();
            if (done)
                return;
            buf += dec.decode(value, { stream: true });
            let nl;
            while ((nl = buf.indexOf("\n")) >= 0) {
                const line = buf.slice(0, nl).replace(/\r$/, "");
                buf = buf.slice(nl + 1);
                if (line === "") {
                    if (hasData)
                        yield ev;
                    ev = { event: "message", data: "" };
                    hasData = false;
                    continue;
                }
                if (line.startsWith(":"))
                    continue;
                const i = line.indexOf(":");
                const field = i < 0 ? line : line.slice(0, i);
                const value = i < 0 ? "" : line.slice(i + 1).replace(/^ /, "");
                if (field === "id")
                    ev.id = value;
                else if (field === "event")
                    ev.event = value;
                else if (field === "data") {
                    ev.data = hasData ? ev.data + "\n" + value : value;
                    hasData = true;
                }
            }
        }
    }
    finally {
        reader.releaseLock();
    }
}
/**
 * Watches a job or run; onChange gets every new state. Reconnects with
 * Last-Event-ID (backing off up to 15 s) until the job or run finishes or the
 * returned function is called.
 */
export function subscribeRun(id, token, onChange, opts = {}) {
    const f = opts.fetch ?? fetch;
    const base = (opts.baseUrl ?? "").replace(/\/$/, "");
    let state = empty;
    let stopped = false;
    let lastId = "";
    let ctrl;
    const set = (patch) => {
        state = { ...state, ...patch };
        if (!stopped)
            onChange(state);
    };
    const run = async () => {
        for (let attempt = 0; !stopped; attempt++) {
            ctrl = new AbortController();
            try {
                const headers = { accept: "text/event-stream", authorization: `Bearer ${token}` };
                if (lastId)
                    headers["last-event-id"] = lastId;
                const res = await f(`${base}/_tiffin/runs/${encodeURIComponent(id)}/events`, { headers, signal: ctrl.signal, cache: "no-store" });
                if (!res.ok || !res.body) {
                    const p = (await res.json().catch(() => ({})));
                    const msg = p.detail ? (p.hint ? `${p.detail} (${p.hint})` : p.detail) : `HTTP ${res.status}`;
                    if (res.status === 401 || res.status === 403 || res.status === 404)
                        return set({ error: msg, connected: false });
                    throw new Error(msg);
                }
                attempt = 0;
                set({ connected: true });
                for await (const ev of events(res.body)) {
                    if (ev.id !== undefined)
                        lastId = ev.id;
                    if (ev.event === "state") {
                        const s = JSON.parse(ev.data);
                        set({ run: s, status: s.status, progress: s.progress ?? null, output: s.output ?? null, error: s.error || null, done: s.done });
                    }
                    else if (ev.event === "output") {
                        set({ chunks: [...state.chunks, JSON.parse(ev.data)] });
                    }
                    else if (ev.event === "end") {
                        ctrl.abort();
                        return set({ connected: false });
                    }
                }
            }
            catch {
                if (stopped)
                    return;
            }
            if (state.done)
                return set({ connected: false });
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
export function useRun(id, token, opts = {}) {
    const [state, setState] = useState(empty);
    const fetchRef = useRef(opts.fetch);
    fetchRef.current = opts.fetch;
    useEffect(() => {
        setState(empty);
        if (!id || !token)
            return;
        const o = {};
        if (opts.baseUrl)
            o.baseUrl = opts.baseUrl;
        if (fetchRef.current)
            o.fetch = fetchRef.current;
        return subscribeRun(id, token, setState, o);
    }, [id, token, opts.baseUrl]);
    return state;
}
/** useRun for a queue job (job_...). */
export const useJob = useRun;
