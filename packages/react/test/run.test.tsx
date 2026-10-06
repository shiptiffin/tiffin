// useRun against a scripted box: replay, live updates, a dropped stream
// resumed with Last-Event-ID, the end of the run, and a refused token.
import { describe, expect, test } from "bun:test";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useRun } from "../src";

type Conn = { headers: Record<string, string>; send(text: string): void; close(): void };

/** A fake box: each request opens a stream the test writes to. */
function fakeBox(status = 200, body = "") {
  const conns: Conn[] = [];
  const fetchFn = (async (_url: string, init: RequestInit) => {
    const headers = init.headers as Record<string, string>;
    if (status !== 200) {
      conns.push({ headers, send() {}, close() {} });
      return Response.json(JSON.parse(body), { status });
    }
    let ctl!: ReadableStreamDefaultController<Uint8Array>;
    const stream = new ReadableStream<Uint8Array>({ start: (c) => void (ctl = c) });
    const enc = new TextEncoder();
    init.signal?.addEventListener("abort", () => {
      try {
        ctl.error(new DOMException("aborted", "AbortError"));
      } catch {}
    });
    conns.push({ headers, send: (t) => ctl.enqueue(enc.encode(t)), close: () => ctl.close() });
    return new Response(stream, { headers: { "content-type": "text/event-stream" } });
  }) as unknown as typeof globalThis.fetch;
  return { conns, fetch: fetchFn };
}

const state = (s: object) => `id: ${(s as any).cursor ?? 0}\nevent: state\ndata: ${JSON.stringify({ id: "run_1", type: "run", name: "report", done: false, progress: null, output: null, ...s })}\n\n`;

describe("useRun", () => {
  test("replay, live updates, resume after a drop, and the end", async () => {
    const box = fakeBox();
    const { result, unmount } = renderHook(() => useRun<{ pct: number }, { rows: number }, string>("run_1", "live1.tok", { fetch: box.fetch }));
    await waitFor(() => expect(box.conns).toHaveLength(1));
    expect(box.conns[0]!.headers.authorization).toBe("Bearer live1.tok");
    expect(box.conns[0]!.headers["last-event-id"]).toBeUndefined();
    act(() => box.conns[0]!.send("retry: 2000\n\n" + `id: 7\nevent: output\ndata: "line 1"\n\n` + state({ cursor: 7, status: "running", progress: { pct: 10 } })));
    await waitFor(() => expect(result.current.progress?.pct).toBe(10));
    expect(result.current.status).toBe("running");
    expect(result.current.chunks).toEqual(["line 1"]);
    expect(result.current.connected).toBe(true);
    // A heartbeat, then a progress update split across two reads.
    act(() => box.conns[0]!.send(": ping\n\n" + state({ cursor: 7, status: "running", progress: { pct: 50 } }).slice(0, 20)));
    act(() => box.conns[0]!.send(state({ cursor: 7, status: "running", progress: { pct: 50 } }).slice(20)));
    await waitFor(() => expect(result.current.progress?.pct).toBe(50));
    // The stream drops: the hook reconnects with the last event ID.
    act(() => box.conns[0]!.close());
    await waitFor(() => expect(result.current.connected).toBe(false));
    await waitFor(() => expect(box.conns).toHaveLength(2), { timeout: 3000 });
    expect(box.conns[1]!.headers["last-event-id"]).toBe("7");
    act(() =>
      box.conns[1]!.send(
        `id: 9\nevent: output\ndata: "line 2"\n\n` +
          state({ cursor: 9, status: "completed", done: true, progress: { pct: 100 }, output: { rows: 3 } }) +
          `event: end\ndata: {"status":"completed"}\n\n`,
      ),
    );
    await waitFor(() => expect(result.current.done).toBe(true));
    expect(result.current.output).toEqual({ rows: 3 });
    expect(result.current.chunks).toEqual(["line 1", "line 2"]);
    await waitFor(() => expect(result.current.connected).toBe(false));
    await new Promise((r) => setTimeout(r, 1200));
    expect(box.conns).toHaveLength(2); // finished: no reconnect
    unmount();
  });

  test("a refused token is an error, not a retry loop", async () => {
    const box = fakeBox(403, JSON.stringify({ code: "forbidden", detail: "this token is for a different job or run" }));
    const { result } = renderHook(() => useRun("run_2", "live1.other", { fetch: box.fetch }));
    await waitFor(() => expect(result.current.error).toContain("different job or run"));
    await new Promise((r) => setTimeout(r, 1200));
    expect(box.conns).toHaveLength(1);
    expect(result.current.status).toBeNull();
  });

  test("no id or token: waits", async () => {
    const box = fakeBox();
    const { result } = renderHook(() => useRun(null, undefined, { fetch: box.fetch }));
    await new Promise((r) => setTimeout(r, 50));
    expect(box.conns).toHaveLength(0);
    expect(result.current.status).toBeNull();
  });
});
