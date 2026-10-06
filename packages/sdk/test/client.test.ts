// @shiptiffin/sdk/client: subscribeRun against a scripted box (replay, live
// updates, a dropped stream resumed with Last-Event-ID, the end, a refused
// token) and the bot check against a real ALTCHA challenge.
import { describe, expect, test } from "bun:test";
import { createChallenge, verifySolution } from "altcha-lib";
import { deriveKey } from "altcha-lib/algorithms/sha";
import { authConfig, errorText, prepareCaptcha, solveCaptcha, subscribeRun, type LiveRun } from "../src/client";

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

const state = (s: Record<string, unknown>) =>
  `id: ${s.cursor ?? 0}\nevent: state\ndata: ${JSON.stringify({ id: "run_1", type: "run", name: "report", done: false, progress: null, output: null, ...s })}\n\n`;

async function until(ok: () => boolean, ms = 3000) {
  const end = Date.now() + ms;
  while (!ok()) {
    if (Date.now() > end) throw new Error("timed out waiting");
    await new Promise((r) => setTimeout(r, 5));
  }
}

describe("subscribeRun", () => {
  test("replay, live updates, resume after a drop, and the end", async () => {
    const box = fakeBox();
    let run: LiveRun<{ pct: number }, { rows: number }, string> | null = null;
    const stop = subscribeRun<{ pct: number }, { rows: number }, string>("run_1", "live1.tok", (s) => (run = s), { fetch: box.fetch, baseUrl: "https://app.example/" });
    const now = () => run!;
    await until(() => box.conns.length === 1);
    expect(box.conns[0]!.headers.authorization).toBe("Bearer live1.tok");
    expect(box.conns[0]!.headers["last-event-id"]).toBeUndefined();
    box.conns[0]!.send("retry: 2000\n\n" + `id: 7\nevent: output\ndata: "line 1"\n\n` + state({ cursor: 7, status: "running", progress: { pct: 10 } }));
    await until(() => now()?.progress?.pct === 10);
    expect(now().status).toBe("running");
    expect(now().chunks).toEqual(["line 1"]);
    expect(now().connected).toBe(true);
    // A heartbeat, then a progress update split across two reads.
    const next = state({ cursor: 7, status: "running", progress: { pct: 50 } });
    box.conns[0]!.send(": ping\n\n" + next.slice(0, 20));
    box.conns[0]!.send(next.slice(20));
    await until(() => now().progress?.pct === 50);
    // The stream drops: it reconnects with the last event ID.
    box.conns[0]!.close();
    await until(() => !now().connected);
    await until(() => box.conns.length === 2);
    expect(box.conns[1]!.headers["last-event-id"]).toBe("7");
    box.conns[1]!.send(
      `id: 9\nevent: output\ndata: "line 2"\n\n` +
        state({ cursor: 9, status: "completed", done: true, progress: { pct: 100 }, output: { rows: 3 } }) +
        `event: end\ndata: {"status":"completed"}\n\n`,
    );
    await until(() => now().done);
    expect(now().output).toEqual({ rows: 3 });
    expect(now().chunks).toEqual(["line 1", "line 2"]);
    await until(() => !now().connected);
    await new Promise((r) => setTimeout(r, 1200));
    expect(box.conns).toHaveLength(2); // finished: no reconnect
    stop();
  });

  test("a refused token is an error, not a retry loop", async () => {
    const box = fakeBox(403, JSON.stringify({ code: "forbidden", detail: "this token is for a different job or run" }));
    let run: LiveRun | null = null;
    subscribeRun("run_2", "live1.other", (s) => (run = s), { fetch: box.fetch });
    await until(() => !!run?.error);
    expect(run!.error).toContain("different job or run");
    await new Promise((r) => setTimeout(r, 1200));
    expect(box.conns).toHaveLength(1);
    expect(run!.status).toBeNull();
  });

  test("stopping closes the stream and reports nothing more", async () => {
    const box = fakeBox();
    const seen: LiveRun[] = [];
    const stop = subscribeRun("run_3", "live1.tok", (s) => seen.push(s), { fetch: box.fetch });
    await until(() => box.conns.length === 1);
    stop();
    const n = seen.length;
    expect(() => box.conns[0]!.send(state({ status: "running" }))).toThrow(); // aborted
    await new Promise((r) => setTimeout(r, 50));
    expect(seen.length).toBe(n);
  });
});

describe("bot check", () => {
  const secret = { hmacSignatureSecret: "s:challenge", hmacKeySignatureSecret: "s:key" };
  // A box with services.auth: its config and ALTCHA challenges.
  function authBox(captcha: boolean) {
    const calls: string[] = [];
    const f = (async (url: string) => {
      const path = new URL(url).pathname;
      calls.push(path);
      if (path === "/api/auth/tiffin/config") return Response.json({ appName: "Shop", methods: ["password"], organizations: false, captcha, social: { google: null, github: null } });
      if (path === "/api/auth/altcha/challenge")
        return Response.json(await createChallenge({ algorithm: "SHA-256", cost: 1, counter: 50, deriveKey, expiresAt: Math.floor(Date.now() / 1000) + 600, ...secret }));
      return new Response("not found", { status: 404 });
    }) as unknown as typeof fetch;
    return { calls, fetch: f };
  }
  const verify = async (header: string) => {
    const { challenge, solution } = JSON.parse(atob(header));
    return (await verifySolution({ challenge, solution, deriveKey, ...secret })).verified;
  };

  test("solveCaptcha's header verifies on the server", async () => {
    const box = authBox(true);
    expect(await verify(await solveCaptcha({ baseURL: "https://one.example", fetch: box.fetch }))).toBe(true);
  });

  test("prepareCaptcha keeps one solution ready and starts the next", async () => {
    const box = authBox(true);
    const captcha = prepareCaptcha({ baseURL: "https://two.example", fetch: box.fetch });
    const a = await captcha();
    const b = await captcha();
    expect(await verify(a["x-captcha-response"]!)).toBe(true);
    expect(await verify(b["x-captcha-response"]!)).toBe(true);
    expect(a["x-captcha-response"]).not.toBe(b["x-captcha-response"]);
    await until(() => box.calls.filter((c) => c.endsWith("/challenge")).length === 3); // the next one is on its way
    expect(box.calls.filter((c) => c.endsWith("/config"))).toHaveLength(1);
  });

  test("with captcha off it sends nothing", async () => {
    const box = authBox(false);
    expect(await prepareCaptcha({ baseURL: "https://three.example", fetch: box.fetch })()).toEqual({});
    expect(box.calls.some((c) => c.endsWith("/challenge"))).toBe(false);
    expect((await authConfig({ baseURL: "https://three.example", fetch: box.fetch })).appName).toBe("Shop");
  });

  test("errorText speaks plainly", () => {
    expect(errorText({ code: "INVALID_EMAIL_OR_PASSWORD" })).toContain("don't match");
    expect(errorText({ status: 429 })).toContain("Too many tries");
    expect(errorText(null)).toBe("Something went wrong. Try again.");
  });
});
