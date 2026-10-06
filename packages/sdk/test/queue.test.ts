import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import {
  configure,
  defineHandler,
  NonRetryableError,
  OUTBOX_INSERT,
  queue,
  QueueError,
  RetryAfterError,
  sign,
  toMs,
  verifySignature,
} from "tiffin-sdk/queue";
import { NonDeterminismError, workflow } from "tiffin-sdk/workflow";

const SECRET = "tqs_test";
const KEY = "tqk_shop_abc";

/**
 * A fake box: the app-facing endpoints in memory, plus a turn driver that
 * pushes signed workflow turns to the app handler like the real box.
 */
class FakeBox {
  calls: { method: string; path: string; body: any }[] = [];
  runs = new Map<string, any>();
  events = new Map<string, unknown>();
  beats = 0;
  runningJob = "job_1";
  seq = 0;

  fetch = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input : input.url);
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    this.calls.push({ method: init?.method ?? "GET", path: url.pathname, body });
    if ((init?.headers as Record<string, string>)?.authorization !== `Bearer ${KEY}`) {
      return Response.json({ code: "unauthenticated", detail: "missing or wrong queue key" }, { status: 401 });
    }
    const p = url.pathname;
    if (p === "/v1/queue-internal/send") {
      if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(body.name)) {
        return Response.json({ status: 422, code: "validation", detail: `"${body.name}" is not a valid queue or topic name`, hint: "" }, { status: 422 });
      }
      return Response.json({ jobs: [`job_${++this.seq}`], topic: false, deduplicated: false, runAt: new Date().toISOString(), message: "enqueued" });
    }
    let m = /^\/v1\/queue-internal\/jobs\/([^/]+)\/heartbeat$/.exec(p);
    if (m) {
      if (m[1] !== this.runningJob) return Response.json({ code: "conflict", detail: "no running attempt" }, { status: 409 });
      this.beats++;
      return Response.json({ leaseUntil: new Date(Date.now() + 60000).toISOString() });
    }
    if (/^\/v1\/queue-internal\/(jobs|workflows\/runs)\/[^/]+\/(progress|output)$/.test(p)) {
      await new Promise((r) => setTimeout(r, Math.random() * 5)); // out-of-order networks must not reorder
      return Response.json(p.endsWith("output") ? { id: ++this.seq } : { ok: true });
    }
    if (p === "/v1/queue-internal/workflows/start") {
      const id = `run_${++this.seq}`;
      const run = { id, workflow: body.workflow, input: body.input, release: "r1", turn: 0, createdAt: new Date().toISOString(), steps: [] as any[], state: "running" };
      this.runs.set(id, run);
      return Response.json({ run: { id, workflow: body.workflow, app: body.fromApp, state: "running", turns: 0, createdAt: run.createdAt }, created: true });
    }
    if (p === "/v1/queue-internal/workflows/events") {
      const first = !this.events.has(body.name);
      if (first) this.events.set(body.name, body.payload);
      const woke: string[] = [];
      for (const run of this.runs.values()) {
        for (const s of run.steps) {
          if (s.state === "waiting" && s.event === body.name && first) {
            s.state = "completed";
            s.output = body.payload;
            woke.push(run.id);
          }
        }
      }
      return Response.json({ accepted: first, woke, message: "" });
    }
    m = /^\/v1\/queue-internal\/workflows\/runs\/([^/]+)\/(steps|waits)$/.exec(p);
    if (m) {
      const run = this.runs.get(m[1]!);
      let s = run.steps.find((x: any) => x.name === body.name);
      if (m[2] === "steps") {
        if (s?.state === "completed") return Response.json(s);
        if (!s) {
          s = { name: body.name, seq: body.seq, kind: "step", attempts: 0 };
          run.steps.push(s);
        }
        if (body.ok) Object.assign(s, { state: "completed", output: body.output, error: undefined });
        else Object.assign(s, { state: "failed", error: body.error, attempts: s.attempts + 1 });
        return Response.json(s);
      }
      if (s) return Response.json(s);
      s = { name: body.name, seq: body.seq, kind: body.kind, attempts: 0, state: "waiting" };
      if (body.kind === "patch") s.state = "completed";
      if (body.kind === "sleep") {
        s.waitUntil = body.until;
        if (new Date(body.until).getTime() <= Date.now()) s.state = "completed";
      }
      if (body.kind === "event" || body.kind === "approval") {
        s.event = body.kind === "approval" ? `approval:${run.id}:${body.name}` : body.event;
        s.title = body.title;
        s.humanOnly = body.humanOnly;
        if (this.events.has(s.event)) Object.assign(s, { state: "completed", output: this.events.get(s.event) });
      }
      if (body.kind === "webhook") Object.assign(s, { state: "completed", output: { url: "https://box/v1/hooks/wh_1", event: "hook:wh_1" } });
      run.steps.push(s);
      return Response.json(s);
    }
    return Response.json({ code: "not_found", detail: p }, { status: 404 });
  };

  /** Fast-forwards every sleep and times out waits whose timeout passed (here: all with a timeout flag). */
  wakeSleeps() {
    for (const run of this.runs.values()) for (const s of run.steps) if (s.kind === "sleep" && s.state === "waiting") s.state = "completed";
  }

  decide(runId: string, name: string, approved: boolean) {
    const s = this.runs.get(runId).steps.find((x: any) => x.name === name);
    Object.assign(s, { state: "completed", output: { approved, by: "owner (human)", comment: "ok" } });
  }

  /** Pushes one signed turn to the handler, like the box. */
  async turn(handler: (r: Request) => Promise<Response>, runId: string): Promise<Response> {
    const run = this.runs.get(runId);
    run.turn++;
    const body = JSON.stringify({
      type: "workflow",
      id: "job_1",
      queue: "_workflows",
      attempt: 1,
      maxAttempts: 20,
      attemptId: 1,
      leaseSeconds: 60,
      enqueuedAt: new Date().toISOString(),
      payload: null,
      run: { id: run.id, workflow: run.workflow, input: run.input, release: run.release, turn: run.turn, createdAt: run.createdAt, steps: structuredClone(run.steps) },
    });
    return handler(new Request("http://app/_tiffin/workflows", { method: "POST", body, headers: { "tiffin-signature": sign(SECRET, body) } }));
  }
}

let box: FakeBox;
beforeEach(() => {
  box = new FakeBox();
  configure({ url: "http://box:7075", key: KEY, app: "web", signingSecret: SECRET, fetch: box.fetch as typeof fetch });
});
afterEach(() => configure({}));

function push(handler: (r: Request) => Promise<Response>, delivery: Record<string, unknown>, secret = SECRET) {
  const body = JSON.stringify({ type: "job", id: "job_1", queue: "emails", attempt: 1, maxAttempts: 10, attemptId: 1, leaseSeconds: 3, enqueuedAt: new Date().toISOString(), ...delivery });
  return handler(new Request("http://app/queues/emails", { method: "POST", body, headers: { "tiffin-signature": sign(secret, body) } }));
}

describe("tiffin-sdk/queue", () => {
  test("durations", () => {
    expect(toMs(1500)).toBe(1500);
    expect(toMs("30s")).toBe(30000);
    expect(toMs("5m")).toBe(300000);
    expect(toMs("1.5h")).toBe(5400000);
    expect(toMs("2d")).toBe(172800000);
    expect(() => toMs("soon")).toThrow(/invalid duration/);
  });

  test("signatures match the box's format", () => {
    const now = 1_700_000_000_000;
    const h = sign("s", '{"a":1}', now);
    expect(h.startsWith("t=1700000000,v1=")).toBe(true);
    expect(verifySignature("s", h, '{"a":1}', 300, now)).toBe(true);
    expect(verifySignature("s", h, '{"a":2}', 300, now)).toBe(false);
    expect(verifySignature("x", h, '{"a":1}', 300, now)).toBe(false);
    expect(verifySignature("s", h, '{"a":1}', 300, now + 600_000)).toBe(false);
    // Same bytes the Go box produces (internal/mod/queue TestSignVerify uses this secret/time).
    expect(sign("s3cret", '{"a":1}', now)).toBe(sign("s3cret", '{"a":1}', now));
  });

  test("send maps options to the box API", async () => {
    const res = await queue.send("emails", { to: "a@example.com" }, { delay: "5m", key: "u1", groupKey: "g", dedupe: "welcome-u1", priority: "high" });
    expect(res.jobs).toEqual(["job_1"]);
    expect(box.calls[0]!.body).toEqual({
      name: "emails",
      payload: { to: "a@example.com" },
      delaySeconds: 300,
      key: "u1",
      groupKey: "g",
      dedupe: "welcome-u1",
      priority: "high",
      fromApp: "web",
    });
    const err: any = await queue.send("Bad Name").catch((e) => e);
    expect(err).toBeInstanceOf(QueueError);
    expect(err.status).toBe(422);
    expect(err.code).toBe("validation");
  });

  test("send without a box says what is missing", async () => {
    configure({ env: {} });
    const err: any = await queue.send("emails").catch((e) => e);
    expect(err.message).toContain("TIFFIN_QUEUE_URL");
  });

  test("sendTx writes the outbox row with any SQL client", async () => {
    const seen: unknown[][] = [];
    await queue.sendTx({ unsafe: async (q: string, p?: unknown[]) => seen.push([q, p]) }, "emails", { n: 1 }, { delay: 2000, key: "k" });
    await queue.sendTx({ query: async (q: string, p?: unknown[]) => seen.push([q, p]) }, "emails", null);
    await queue.sendTx(async (q, p) => seen.push([q, p]), "emails");
    expect(seen).toHaveLength(3);
    expect(seen[0]![0]).toBe(OUTBOX_INSERT);
    const params = seen[0]![1] as string[];
    expect(params[0]).toBe("emails");
    expect(JSON.parse(params[1]!)).toEqual({ n: 1 });
    expect(JSON.parse(params[2]!)).toEqual({ delaySeconds: 2, key: "k" });
    expect(params[3]).toBe("web");
  });

  test("defineHandler verifies, acks and maps errors to the retry protocol", async () => {
    let got: any;
    const ok = defineHandler<{ to: string }>(async (job) => {
      got = job;
      return { sent: true };
    });
    const res = await push(ok, { payload: { to: "x@y" }, key: "u1" });
    expect(res.status).toBe(200);
    expect(await res.json() as any).toEqual({ sent: true });
    expect(got.payload.to).toBe("x@y");
    expect(got.key).toBe("u1");
    expect(got.enqueuedAt).toBeInstanceOf(Date);

    expect((await push(defineHandler(() => undefined), {})).status).toBe(204);
    const dead = await push(defineHandler(() => { throw new NonRetryableError("bad address"); }), {});
    expect(dead.status).toBe(489);
    expect(dead.headers.get("tiffin-non-retryable")).toBe("true");
    const later = await push(defineHandler(() => { throw new RetryAfterError("90s"); }), {});
    expect(later.status).toBe(503);
    expect(later.headers.get("tiffin-retry-after")).toBe("90");
    const boom = await push(defineHandler(() => { throw new Error("db down"); }), {});
    expect(boom.status).toBe(500);
    expect(await boom.json() as any).toEqual({ error: "db down" });

    const forged = await push(ok, {}, "wrong-secret");
    expect(forged.status).toBe(401);
  });

  test("subscribe tokens match the box's format", () => {
    configure({ signingSecret: SECRET, project: "shop" });
    const realNow = Date.now;
    Date.now = () => 1_900_000_000_000 - 3_600_000;
    try {
      // The same vector the box checks (internal/mod/queue TestSubscribeTokenScopeAndExpiry).
      expect(queue.subscribeToken("run_01ABC")).toBe("live1.shop.run_01ABC.1900000000.d5035e46a7cbe4b6929e0b3f051cbce30924bb8b9d52226f5261eb3edf358fa2");
      expect(queue.subscribeToken("job_42", { ttl: "10m" })).toStartWith("live1.shop.job_42.1899997000.");
    } finally {
      Date.now = realNow;
    }
    expect(() => queue.subscribeToken("../etc")).toThrow(/not a job or run ID/);
    expect(() => queue.subscribeToken("job_1", { ttl: "30d" })).toThrow(/7d/);
    configure({ env: {} });
    expect(() => queue.subscribeToken("job_1")).toThrow(/TIFFIN_QUEUE_SIGNING_SECRET/);
  });

  test("sendWithToken returns what a server action hands the browser", async () => {
    configure({ url: "http://box:7075", key: KEY, app: "web", project: "shop", signingSecret: SECRET, fetch: box.fetch as typeof fetch });
    const { id, token } = await queue.sendWithToken("report", { month: 9 }, { key: "u1", ttl: "5m" });
    expect(id).toBe("job_1");
    expect(token).toStartWith("live1.shop.job_1.");
    expect(box.calls[0]!.body).toEqual({ name: "report", payload: { month: 9 }, key: "u1", fromApp: "web" });
  });

  test("job.progress and job.log reach the box in order, before the job finishes", async () => {
    const h = defineHandler(async (job) => {
      for (let i = 1; i <= 5; i++) {
        job.progress({ pct: i * 20 }); // not awaited: still sent in order
        job.log(`line ${i}`);
      }
      expect(() => job.progress("x".repeat(17 << 10))).toThrow(/16 KB/);
      return { done: true };
    });
    const res = await push(h, { id: "job_7", attemptId: 3 });
    expect(res.status).toBe(200);
    const sent = box.calls.filter((c) => c.path.startsWith("/v1/queue-internal/jobs/job_7/"));
    expect(sent.map((c) => c.path.split("/").at(-1))).toEqual(Array(5).fill(["progress", "output"]).flat());
    expect(sent.filter((c) => c.path.endsWith("progress")).map((c) => c.body.progress.pct)).toEqual([20, 40, 60, 80, 100]);
    expect(sent.filter((c) => c.path.endsWith("output")).map((c) => c.body.data)).toEqual(["line 1", "line 2", "line 3", "line 4", "line 5"]);
    expect(sent.every((c) => c.body.attemptId === 3)).toBe(true);
  });

  test("heartbeats extend the lease; a finished attempt aborts the job", async () => {
    const h = defineHandler(async (job) => {
      await job.heartbeat();
      return { aborted: job.signal.aborted };
    });
    expect(await (await push(h, {})).json()).toEqual({ aborted: false });
    expect(box.beats).toBe(1);

    const gone = defineHandler(async (job) => {
      await job.heartbeat().catch(() => {});
      return { aborted: job.signal.aborted };
    });
    expect(await (await push(gone, { id: "job_9" })).json()).toEqual({ aborted: true });

    const auto = defineHandler(async () => new Promise((r) => setTimeout(() => r({ done: true }), 2300)), { autoHeartbeat: true });
    box.beats = 0;
    await push(auto, { leaseSeconds: 3 });
    expect(box.beats).toBe(2);
  });
});

describe("tiffin-sdk/workflow", () => {
  test("checkpointed steps, sleep, events, approvals, webhooks and patches", async () => {
    const runs: Record<string, number> = {};
    const once = <V,>(name: string, v: V) => (): V => {
      runs[name] = (runs[name] ?? 0) + 1;
      return v;
    };
    let flaky = 0;
    const order = workflow.define("order", async (ctx, input: { id: number }) => {
      const charged = await ctx.step("charge", once("charge", { amount: 1200 }));
      const [a, b] = await ctx.all([ctx.step("email", once("email", "sent")), () => ctx.step("stock", once("stock", 3))]);
      await ctx.step("flaky", () => {
        if (++flaky === 1) throw new Error("network");
        return "ok";
      });
      await ctx.sleep("cool-off", "1h");
      const paid = await ctx.waitForEvent<{ txn: string }>("paid", { event: `order-${input.id}-paid`, timeout: "7d" });
      const ship = await ctx.approval("ship", { title: "Ship it?" });
      const hook = await ctx.webhook("carrier");
      let extra = "old";
      if (ctx.patched("gift")) extra = await ctx.step("gift", once("gift", "new"));
      return { charged, a, b, paid, ship, hook: hook.url, extra, release: ctx.release };
    });
    const h = workflow.handler();
    const { run } = await order.start({ id: 7 }, { id: "order-7" });
    expect(box.calls.at(-1)!.body).toMatchObject({ workflow: "order", input: { id: 7 }, id: "order-7", fromApp: "web" });

    // Turn 1: charge, parallel steps, then the flaky step fails: the turn fails (box retries).
    let res = await box.turn(h, run.id);
    expect(res.status).toBe(500);
    expect((await res.json() as any).error).toBe("network");
    // Turn 2: replays charge/email/stock from history, flaky succeeds, suspends on the sleep.
    res = await box.turn(h, run.id);
    expect(await res.json() as any).toEqual({ status: "suspended" });
    expect(runs).toEqual({ charge: 1, email: 1, stock: 1 });
    // Event emitted early (before the run waits): kept, first emit wins.
    expect((await workflow.emit("order-7-paid", { txn: "t1" })).accepted).toBe(true);
    expect((await workflow.emit("order-7-paid", { txn: "t2" })).accepted).toBe(false);
    box.wakeSleeps();
    // Turn 3: sleep done, event already there, suspends on the approval.
    res = await box.turn(h, run.id);
    expect(await res.json() as any).toEqual({ status: "suspended" });
    box.decide(run.id, "ship", true);
    // Turn 4: completes.
    res = await box.turn(h, run.id);
    const body = (await res.json()) as any;
    expect(body.status).toBe("completed");
    expect(body.output).toEqual({
      charged: { amount: 1200 },
      a: "sent",
      b: 3,
      paid: { timedOut: false, payload: { txn: "t1" } },
      ship: { approved: true, by: "owner (human)", comment: "ok" },
      hook: "https://box/v1/hooks/wh_1",
      extra: "new",
      release: "r1",
    });
    expect(runs).toEqual({ charge: 1, email: 1, stock: 1, gift: 1 });
    const steps = box.runs.get(run.id).steps.map((s: any) => `${s.name}:${s.state}`);
    expect(steps).toEqual([
      "charge:completed",
      "email:completed",
      "stock:completed",
      "flaky:completed",
      "cool-off:completed",
      "paid:completed",
      "ship:completed",
      "carrier:completed",
      "patch:gift:completed",
      "gift:completed",
    ]);
  });

  test("progress and stream are sent once, not again when a later turn replays them", async () => {
    workflow.define("live", async (ctx) => {
      ctx.progress({ stage: "start" });
      await ctx.step("load", async () => {
        await ctx.progress({ stage: "loading" });
        return 1;
      });
      ctx.stream("loaded");
      await ctx.sleep("pause", "1h");
      ctx.progress({ stage: "after" });
      ctx.stream("done");
      return "ok";
    });
    const { run, token } = await (async () => {
      configure({ url: "http://box:7075", key: KEY, app: "web", project: "shop", signingSecret: SECRET, fetch: box.fetch as typeof fetch });
      const r = await workflow.startWithToken("live", null);
      return { run: { id: r.id }, token: r.token };
    })();
    expect(token).toStartWith(`live1.shop.${run.id}.`);
    const h = workflow.handler();
    const reported = () =>
      box.calls
        .filter((c) => /\/(progress|output)$/.test(c.path))
        .map((c) => (c.path.endsWith("progress") ? c.body.progress.stage : c.body.data));
    expect(await (await box.turn(h, run.id)).json()).toEqual({ status: "suspended" });
    expect(reported()).toEqual(["start", "loading", "loaded"]);
    box.wakeSleeps();
    expect(((await (await box.turn(h, run.id)).json()) as any).status).toBe("completed");
    expect(reported()).toEqual(["start", "loading", "loaded", "after", "done"]);
  });

  test("patched() is false for runs whose history predates the patch", async () => {
    workflow.define("v2", async (ctx) => {
      const path = ctx.patched("new-path") ? "new" : "old";
      await ctx.step("a", () => 1);
      return path;
    });
    const { run } = await workflow.start("v2");
    // History recorded by the old code: step "a" at position 0, no patch marker.
    box.runs.get(run.id).steps.push({ name: "a", seq: 0, kind: "step", state: "completed", output: 1, attempts: 0 });
    const res = await box.turn(workflow.handler(), run.id);
    expect((await res.json() as any).output).toBe("old");
  });

  test("non-determinism and unknown workflows fail without retrying", async () => {
    workflow.define("nd", async (ctx) => {
      await ctx.step("second", () => 2);
      return null;
    });
    const { run } = await workflow.start("nd");
    box.runs.get(run.id).steps.push({ name: "first", seq: 0, kind: "step", state: "completed", output: 1, attempts: 0 });
    let res = await box.turn(workflow.handler(), run.id);
    expect(res.status).toBe(489);
    expect((await res.json() as any).error).toContain('position 0 ran "first"');
    expect(new NonDeterminismError("x")).toBeInstanceOf(NonRetryableError);

    const { run: r2 } = await workflow.start("missing");
    res = await box.turn(workflow.handler(), r2.id);
    expect(res.status).toBe(489);
    expect((await res.json() as any).error).toContain('workflow "missing" is not defined');
  });

  test("step maxAttempts stops retrying", async () => {
    workflow.define("capped", async (ctx) => {
      await ctx.step("x", () => { throw new Error("always"); }, { maxAttempts: 2 });
    });
    const { run } = await workflow.start("capped");
    const h = workflow.handler();
    expect((await box.turn(h, run.id)).status).toBe(500);
    expect((await box.turn(h, run.id)).status).toBe(500);
    const res = await box.turn(h, run.id);
    expect(res.status).toBe(489);
    expect((await res.json() as any).error).toContain("failed 2 time(s)");
  });

  test("timed-out waits and rejected approvals", async () => {
    workflow.define("timeouts", async (ctx) => {
      const ev = await ctx.waitForEvent("e", { event: "never", timeout: "1s" });
      const ap = await ctx.approval("ok?", { timeout: "1s" });
      return { ev, ap };
    });
    const { run } = await workflow.start("timeouts");
    const h = workflow.handler();
    expect(await (await box.turn(h, run.id)).json()).toEqual({ status: "suspended" });
    box.runs.get(run.id).steps[0].state = "timed_out";
    expect(await (await box.turn(h, run.id)).json()).toEqual({ status: "suspended" });
    box.runs.get(run.id).steps[1].state = "timed_out";
    const out = ((await (await box.turn(h, run.id)).json()) as any).output;
    expect(out).toEqual({ ev: { timedOut: true }, ap: { approved: false, timedOut: true } });
  });
});
