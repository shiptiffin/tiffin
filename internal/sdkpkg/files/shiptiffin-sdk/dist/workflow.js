/**
 * `@shiptiffin/sdk/workflow`: durable workflows that run inside your app.
 *
 * ```ts
 * import { workflow } from "@shiptiffin/sdk/workflow";
 *
 * export const onboard = workflow.define("onboard", async (ctx, input: { userId: string }) => {
 *   const user = await ctx.step("load user", () => db.users.get(input.userId));
 *   await ctx.step("send welcome", () => sendWelcome(user));
 *   await ctx.sleep("wait a day", "1d");
 *   const paid = await ctx.waitForEvent("payment", { event: `paid-${user.id}`, timeout: "7d" });
 *   if (paid.timedOut) return { status: "unpaid" };
 *   const ok = await ctx.approval("ship", { title: `Ship order for ${user.email}?` });
 *   return { status: ok.approved ? "shipped" : "held" };
 * });
 *
 * // Mount the handler (one route serves every workflow of the app):
 * Bun.serve({ fetch: (req) => new URL(req.url).pathname === "/_tiffin/workflows" ? workflow.handler()(req) : app.fetch(req) });
 *
 * await onboard.start({ userId: "u_1" }, { id: "onboard-u_1" });   // idempotent by id
 * await workflow.emit("paid-u_1", { amount: 1200 });                // resumes the run
 * ```
 *
 * Live progress: `ctx.progress(value)` and `ctx.stream(chunk)` report to
 * browsers watching the run; `workflow.startWithToken()` (in a server
 * action) returns `{ id, token }` for `subscribeRun` in @shiptiffin/sdk/client.
 * Calls replayed from earlier turns are not sent again.
 *
 * How it runs: each "turn" the box POSTs the run and its finished steps to
 * the handler; the function runs from the top and finished steps return
 * their recorded result instead of running again. A step's result is saved
 * the moment it finishes, so a crash or redeploy loses nothing. Waits
 * (sleep, events, approvals, webhooks) end the turn; the box starts the next
 * turn when they resolve. A failing step fails the turn, which retries with
 * backoff (`maxAttempts` per step caps it).
 *
 * Rules for workflow code: everything with side effects or non-determinism
 * (I/O, Date.now(), Math.random()) goes inside `ctx.step`; step and wait
 * names are unique within a run and are called in the same order every
 * replay. Runs are pinned to the release that started them; to change code
 * that already-running runs may replay, guard it with `ctx.patched("id")`.
 */
import { boxCall, currentApp, errorResponse, heartbeater, NonRetryableError, readDelivery, reporter, subscribeToken, toMs, } from "./queue.js";
export { NonRetryableError, subscribeToken } from "./queue.js";
/** Where the box POSTs workflow turns by default. */
export const DEFAULT_PATH = "/_tiffin/workflows";
/**
 * The largest turn the handler reads by default: a turn carries the run's
 * whole history (every step's result, up to 1 MB each). Pass maxBytes to
 * handler() for runs that keep more.
 */
export const MAX_TURN_BYTES = 64 << 20;
/** The code took a different path than this run's recorded history. */
export class NonDeterminismError extends NonRetryableError {
    name = "NonDeterminismError";
}
const registry = new Map();
/** Defines a workflow in this app. Names: lowercase letters, digits, . _ - */
export function define(name, fn) {
    if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name))
        throw new TypeError(`invalid workflow name ${JSON.stringify(name)}`);
    const wf = {
        name,
        fn,
        start: (input, opts) => start(name, input, opts),
        startWithToken: (input, opts) => startWithToken(name, input, opts),
    };
    registry.set(name, wf);
    return wf;
}
/** Starts a run of a workflow (defined here or in another app of the project). */
export async function start(name, input, opts = {}) {
    return boxCall("POST", "/v1/queue-internal/workflows/start", {
        workflow: name,
        input: input === undefined ? null : input,
        id: opts.id,
        app: opts.app,
        path: opts.path,
        fromApp: currentApp(),
    });
}
/**
 * Starts a run and returns its ID with a token for the browser: what a
 * server action returns so the page can show the run's progress live.
 */
export async function startWithToken(name, input, opts = {}) {
    const { run } = await start(name, input, opts);
    return { id: run.id, token: subscribeToken(run.id, opts) };
}
/** Emits an event; every run waiting for it resumes. The first emit of a name wins. */
export async function emit(event, payload) {
    return boxCall("POST", "/v1/queue-internal/workflows/events", { name: event, payload: payload === undefined ? null : payload, fromApp: currentApp() });
}
/** Reads a run's state and steps. */
export async function get(runId) {
    return boxCall("GET", `/v1/queue-internal/workflows/runs/${encodeURIComponent(runId)}`);
}
class Suspend {
    reason;
    constructor(reason) {
        this.reason = reason;
    }
}
class Turn {
    run;
    fence;
    signal;
    seq = 0;
    byName = new Map();
    bySeq = new Map();
    inflight = new Set();
    /** Failed writes no workflow code awaits (patch markers): they fail the turn. */
    failures = [];
    maxRecordedSeq;
    report;
    constructor(run, fence, signal) {
        this.run = run;
        this.fence = fence;
        this.signal = signal;
        this.report = reporter(`/v1/queue-internal/workflows/runs/${run.id}`);
        let max = -1;
        for (const s of run.steps) {
            this.byName.set(s.name, s);
            if (s.kind !== "patch") {
                this.bySeq.set(s.seq, s);
                max = Math.max(max, s.seq);
            }
        }
        this.maxRecordedSeq = max;
    }
    /** True while the code is still before the last recorded step: an earlier turn already did this. */
    replaying() {
        return this.seq <= this.maxRecordedSeq;
    }
    track(p) {
        this.inflight.add(p);
        p.then(() => this.inflight.delete(p), () => this.inflight.delete(p));
        return p;
    }
    /** Claims the next position and checks it against history. */
    claim(name, kind) {
        if (!name || name.length > 200)
            throw new NonRetryableError("step names are 1-200 characters");
        const seq = this.seq++;
        const rec = this.byName.get(name);
        if (rec && rec.kind !== kind) {
            throw new NonDeterminismError(`"${name}" was a ${rec.kind} in this run's history but is now a ${kind}; guard code changes with ctx.patched()`);
        }
        const there = this.bySeq.get(seq);
        if (!rec && there && there.name !== name) {
            throw new NonDeterminismError(`position ${seq} ran "${there.name}" in this run's history but the code now runs "${name}" there; steps must run in the same order (use ctx.patched() for changes)`);
        }
        return { seq, rec };
    }
    async wait(body) {
        const rec = await boxCall("POST", `/v1/queue-internal/workflows/runs/${this.run.id}/waits`, { ...body, ...this.fence });
        this.byName.set(rec.name, rec);
        return rec;
    }
}
function ctxFor(t) {
    const run = t.run;
    const waitFor = async (name, kind, extra) => {
        const { seq, rec } = t.claim(name, kind);
        let r = rec;
        if (!r || r.state === "waiting") {
            if (r?.state === "waiting")
                throw new Suspend(`${kind} ${name}`);
            r = await t.wait({ name, seq, kind, ...extra });
        }
        if (r.state === "waiting")
            throw new Suspend(`${kind} ${name}`);
        return r;
    };
    const ctx = {
        runId: run.id,
        workflow: run.workflow,
        signal: t.signal,
        release: run.release,
        step(name, fn, opts = {}) {
            return t.track((async () => {
                const { seq, rec } = t.claim(name, "step");
                if (rec?.state === "completed")
                    return rec.output;
                const attempts = rec?.attempts ?? 0;
                if (opts.maxAttempts && attempts >= opts.maxAttempts) {
                    throw new NonRetryableError(`step "${name}" failed ${attempts} time(s); last error: ${rec?.error ?? "unknown"}`);
                }
                const started = new Date();
                try {
                    const value = await fn();
                    const output = value === undefined ? null : JSON.parse(JSON.stringify(value));
                    const stored = await boxCall("POST", `/v1/queue-internal/workflows/runs/${run.id}/steps`, {
                        name,
                        seq,
                        ok: true,
                        output,
                        startedAt: started.toISOString(),
                        durationMs: Date.now() - started.getTime(),
                        ...t.fence,
                    });
                    // The checkpoint wins: if another turn recorded this step first,
                    // carry on with its result, as every later replay will.
                    return stored?.state === "completed" ? (stored.output ?? null) : output;
                }
                catch (err) {
                    if (err instanceof Suspend)
                        throw err;
                    await boxCall("POST", `/v1/queue-internal/workflows/runs/${run.id}/steps`, {
                        name,
                        seq,
                        ok: false,
                        error: err instanceof Error ? `${err.name}: ${err.message}` : String(err),
                        startedAt: started.toISOString(),
                        durationMs: Date.now() - started.getTime(),
                        ...t.fence,
                    }).catch(() => { });
                    throw err;
                }
            })());
        },
        async sleep(name, duration) {
            // The wake time is fixed the first time; replays use the recorded one.
            const { seq, rec } = t.claim(name, "sleep");
            if (rec && rec.state !== "waiting")
                return;
            if (rec)
                throw new Suspend(`sleep ${name}`);
            const r = await t.wait({ name, seq, kind: "sleep", until: new Date(Date.now() + toMs(duration)).toISOString() });
            if (r.state === "waiting")
                throw new Suspend(`sleep ${name}`);
        },
        async sleepUntil(name, until) {
            const { seq, rec } = t.claim(name, "sleep");
            if (rec && rec.state !== "waiting")
                return;
            if (rec)
                throw new Suspend(`sleep ${name}`);
            const r = await t.wait({ name, seq, kind: "sleep", until: new Date(until).toISOString() });
            if (r.state === "waiting")
                throw new Suspend(`sleep ${name}`);
        },
        async waitForEvent(name, opts) {
            const r = await waitFor(name, "event", { event: opts.event, timeoutSeconds: opts.timeout ? Math.ceil(toMs(opts.timeout) / 1000) : undefined });
            if (r.state === "timed_out")
                return { timedOut: true };
            return { timedOut: false, payload: r.output };
        },
        async approval(name, opts) {
            const r = await waitFor(name, "approval", {
                title: opts.title ?? name,
                description: opts.description,
                humanOnly: opts.humanOnly ?? true,
                timeoutSeconds: opts.timeout ? Math.ceil(toMs(opts.timeout) / 1000) : undefined,
            });
            if (r.state === "timed_out")
                return { approved: false, timedOut: true };
            return r.output;
        },
        async webhook(name) {
            const { seq, rec } = t.claim(name, "webhook");
            const r = rec ?? (await t.wait({ name, seq, kind: "webhook" }));
            const hook = r.output;
            return {
                url: hook.url,
                wait: (opts = {}) => ctx.waitForEvent(`${name}:wait`, { event: hook.event, timeout: opts.timeout }),
            };
        },
        async all(branches) {
            const ps = branches.map((b) => t.track((async () => {
                // A branch that throws synchronously still lets the others finish.
                return await (typeof b === "function" ? b() : b);
            })()));
            const settled = await Promise.allSettled(ps);
            const failed = settled.flatMap((r) => (r.status === "rejected" ? [r.reason] : []));
            // A failure decides the turn before a wait does: suspending would hide
            // it until the wait resolves (and a NonRetryableError never fails the run).
            const errors = failed.filter((e) => !(e instanceof Suspend));
            if (errors.length)
                throw errors.find((e) => e instanceof NonRetryableError) ?? errors[0];
            if (failed.length)
                throw failed[0];
            return settled.map((r) => r.value);
        },
        patched(id) {
            const key = `patch:${id}`;
            if (t.byName.has(key))
                return true;
            // History reaching past this point was made by code without the patch.
            if (t.maxRecordedSeq >= t.seq)
                return false;
            const rec = { name: key, seq: t.seq, kind: "patch", state: "completed", attempts: 0 };
            t.byName.set(key, rec);
            t.track(t.wait({ name: key, seq: t.seq, kind: "patch" }).catch((err) => void t.failures.push(err)));
            return true;
        },
        progress(value) {
            return t.replaying() ? Promise.resolve() : t.report.progress(value);
        },
        stream(chunk) {
            return t.replaying() ? Promise.resolve() : t.report.output(chunk);
        },
    };
    return ctx;
}
/** @internal Runs one turn; returns the response body for the box. */
export async function runTurn(run, fence, signal = new AbortController().signal) {
    const wf = registry.get(run.workflow);
    if (!wf) {
        throw new NonRetryableError(`workflow "${run.workflow}" is not defined in this app (defined: ${[...registry.keys()].join(", ") || "none"})`);
    }
    const t = new Turn(run, fence, signal);
    try {
        const out = await wf.fn(ctxFor(t), run.input);
        while (t.inflight.size)
            await Promise.allSettled([...t.inflight]);
        if (t.failures.length)
            throw t.failures[0];
        return { status: "completed", output: out === undefined ? null : out };
    }
    catch (err) {
        // Let parallel branches finish and checkpoint before the turn ends.
        while (t.inflight.size)
            await Promise.allSettled([...t.inflight]);
        if (err instanceof Suspend) {
            if (t.failures.length)
                throw t.failures[0];
            return { status: "suspended" };
        }
        throw err;
    }
    finally {
        await t.report.flush(); // progress lands before the run moves on
    }
}
/**
 * The fetch handler the box pushes turns to. Mount it at /_tiffin/workflows
 * (or pass that path to start()).
 */
export function handler(opts = {}) {
    return async (req) => {
        const d = await readDelivery(req, opts.secret, opts.maxBytes ?? MAX_TURN_BYTES);
        if (d instanceof Response)
            return d;
        if (d.type !== "workflow" || !d.run)
            return Response.json({ error: "not a workflow turn" }, { status: 400 });
        const ctrl = new AbortController();
        const beat = heartbeater(d, ctrl);
        const timer = setInterval(() => beat().catch(() => { }), Math.max(1000, (d.leaseSeconds * 1000) / 3));
        try {
            return Response.json(await runTurn(d.run, { jobId: d.id, attemptId: d.attemptId }, ctrl.signal));
        }
        catch (err) {
            return errorResponse(err);
        }
        finally {
            clearInterval(timer);
        }
    };
}
export const workflow = { define, start, startWithToken, subscribeToken, emit, get, handler, DEFAULT_PATH };
