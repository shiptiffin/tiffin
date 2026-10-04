/**
 * `tiffin-sdk/queue`: background jobs pushed to your app.
 *
 * ```ts
 * import { queue, defineHandler, NonRetryableError } from "tiffin-sdk/queue";
 *
 * // Send (from any app of the project):
 * await queue.send("emails", { to: user.email }, { delay: "5m", key: user.id, dedupe: `welcome-${user.id}` });
 * await queue.send("order.created", order);            // a topic fans out to its subscribers
 *
 * // Receive: the box POSTs each job to /queues/<name> on the target app.
 * const emails = defineHandler<{ to: string }>(async (job) => {
 *   if (!job.payload.to.includes("@")) throw new NonRetryableError("bad address"); // → dead-letter queue
 *   await sendWelcome(job.payload.to);                    // a throw retries with backoff
 * });
 * Bun.serve({ fetch: (req) => new URL(req.url).pathname === "/queues/emails" ? emails(req) : app.fetch(req) });
 * ```
 *
 * Delivery: the box signs every push (Tiffin-Signature, HMAC-SHA256 with
 * TIFFIN_QUEUE_SIGNING_SECRET); `defineHandler` verifies it. A 2xx acks the
 * job. `NonRetryableError` answers 489 (straight to the dead-letter queue),
 * `RetryAfterError` asks for a specific delay, anything else retries with
 * exponential backoff until `maxAttempts`. Each attempt holds a lease
 * (default 60 s): answer within it or call `job.heartbeat()` (or pass
 * `autoHeartbeat: true`) for long work.
 *
 * Durations are milliseconds (numbers) or strings like "30s", "5m", "2h", "1d".
 */
import { createHmac, timingSafeEqual } from "node:crypto";
/** Throw from a handler (or a workflow step) to fail without retrying. */
export class NonRetryableError extends Error {
    name = "NonRetryableError";
}
/** Throw from a handler to retry after a specific delay (counts as an attempt). */
export class RetryAfterError extends Error {
    name = "RetryAfterError";
    retryAfterMs;
    constructor(after, message = "retry later") {
        super(message);
        this.retryAfterMs = toMs(after);
    }
}
/** A box API error with the box's stable code and hint. */
export class QueueError extends Error {
    status;
    code;
    hint;
    name = "QueueError";
    constructor(status, code, message, hint) {
        super(hint ? `${message} (${hint})` : message);
        this.status = status;
        this.code = code;
        this.hint = hint;
    }
}
/** Parses "30s", "5m", "2h", "1d", "500ms" or a number of milliseconds. */
export function toMs(d) {
    if (typeof d === "number") {
        if (!Number.isFinite(d) || d < 0)
            throw new TypeError(`invalid duration ${d}`);
        return d;
    }
    const m = /^\s*(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)\s*$/.exec(d);
    if (!m)
        throw new TypeError(`invalid duration ${JSON.stringify(d)}: use a number of milliseconds or e.g. "30s", "5m", "2h", "1d"`);
    const unit = { ms: 1, s: 1e3, m: 60e3, h: 3600e3, d: 86400e3 }[m[2]];
    return Math.round(Number(m[1]) * unit);
}
let overrides = {};
/** Overrides the box connection (tests, scripts outside the box). */
export function configure(opts) {
    overrides = { ...opts };
}
function settings() {
    const env = overrides.env ?? (typeof process !== "undefined" ? process.env : {});
    return {
        url: (overrides.url ?? env.TIFFIN_QUEUE_URL ?? "").replace(/\/$/, ""),
        key: overrides.key ?? env.TIFFIN_QUEUE_KEY ?? "",
        app: overrides.app ?? env.TIFFIN_APP ?? "",
        secret: overrides.signingSecret ?? env.TIFFIN_QUEUE_SIGNING_SECRET ?? "",
        fetch: overrides.fetch ?? fetch,
    };
}
/** @internal Calls an app-facing box endpoint. */
export async function boxCall(method, path, body) {
    const s = settings();
    if (!s.url || !s.key) {
        throw new QueueError(0, "precondition", "TIFFIN_QUEUE_URL and TIFFIN_QUEUE_KEY are not set", "run on a Tiffin box, or call configure({ url, key })");
    }
    const res = await s.fetch(s.url + path, {
        method,
        headers: { "content-type": "application/json", authorization: `Bearer ${s.key}` },
        body: body === undefined ? null : JSON.stringify(body),
    });
    const text = await res.text();
    let data = undefined;
    try {
        data = text ? JSON.parse(text) : undefined;
    }
    catch {
        data = { detail: text };
    }
    if (!res.ok) {
        throw new QueueError(res.status, data?.code ?? "internal", data?.detail ?? `HTTP ${res.status}`, data?.hint || undefined);
    }
    return data;
}
/** @internal */
export function currentApp() {
    return settings().app;
}
function sendBody(name, payload, opts) {
    return {
        name,
        payload: payload === undefined ? null : payload,
        delaySeconds: opts.delay === undefined ? undefined : Math.ceil(toMs(opts.delay) / 1000),
        runAt: opts.runAt === undefined ? undefined : new Date(opts.runAt).toISOString(),
        key: opts.key,
        groupKey: opts.groupKey,
        dedupe: opts.dedupe,
        priority: opts.priority,
        app: opts.app,
        path: opts.path,
        maxAttempts: opts.maxAttempts,
    };
}
/** Sends a job to a queue, or publishes to a topic (one job per subscriber). */
export async function send(name, payload, opts = {}) {
    return boxCall("POST", "/v1/queue-internal/send", { ...sendBody(name, payload, opts), fromApp: currentApp() });
}
/** The statement sendTx runs (the box creates tiffin.outbox in every project database). */
export const OUTBOX_INSERT = "INSERT INTO tiffin.outbox (name, payload, options, app) VALUES ($1, $2::jsonb, $3::jsonb, $4)";
/**
 * Enqueues inside your own Postgres transaction: the job exists if and only
 * if the transaction commits. Pass the transaction handle:
 *
 * ```ts
 * await sql.begin(async (tx) => {
 *   await tx`INSERT INTO orders ${tx(order)}`;
 *   await queue.sendTx(tx, "order.created", order);
 * });
 * ```
 *
 * The box moves committed rows into the queue within about a second
 * (`queue.flush()` asks it to look now).
 */
export async function sendTx(db, name, payload, opts = {}) {
    const b = sendBody(name, payload, opts);
    const options = { ...b };
    delete options.name;
    delete options.payload;
    const params = [name, JSON.stringify(b.payload), JSON.stringify(options), currentApp()];
    if (typeof db === "function")
        await db(OUTBOX_INSERT, params);
    else if ("unsafe" in db)
        await db.unsafe(OUTBOX_INSERT, params);
    else if ("query" in db)
        await db.query(OUTBOX_INSERT, params);
    else
        throw new TypeError("sendTx needs a SQL handle with unsafe() or query(), or a function");
}
/** Asks the box to move committed sendTx rows into the queue now. */
export async function flush() {
    await boxCall("POST", "/v1/queue-internal/outbox/kick", {});
}
export const queue = { send, sendTx, flush, configure };
/** Verifies a Tiffin-Signature header over the raw body. */
export function verifySignature(secret, header, body, toleranceSeconds = 300, now = Date.now()) {
    if (!secret || !header)
        return false;
    let ts = "";
    let sig = "";
    for (const part of header.split(",")) {
        const [k, v] = part.trim().split("=", 2);
        if (k === "t")
            ts = v ?? "";
        if (k === "v1")
            sig = v ?? "";
    }
    const t = Number(ts);
    if (!Number.isInteger(t) || !sig || Math.abs(now / 1000 - t) > toleranceSeconds)
        return false;
    const want = createHmac("sha256", secret).update(`${ts}.${body}`).digest("hex");
    return want.length === sig.length && timingSafeEqual(Buffer.from(want), Buffer.from(sig));
}
/** Signs a body the way the box does (tests, local tools). */
export function sign(secret, body, now = Date.now()) {
    const ts = Math.floor(now / 1000).toString();
    return `t=${ts},v1=${createHmac("sha256", secret).update(`${ts}.${body}`).digest("hex")}`;
}
/** @internal Reads and verifies a push. Returns a Response to send back on failure. */
export async function readDelivery(req, secret) {
    if (req.method !== "POST")
        return new Response("method not allowed", { status: 405 });
    const body = await req.text();
    const key = secret ?? settings().secret;
    if (!verifySignature(key, req.headers.get("tiffin-signature"), body)) {
        return Response.json({ error: "invalid or missing Tiffin-Signature" }, { status: 401 });
    }
    try {
        return JSON.parse(body);
    }
    catch {
        return Response.json({ error: "body is not JSON" }, { status: 400 });
    }
}
/** @internal Maps an error to the box's retry protocol. */
export function errorResponse(err) {
    const message = err instanceof Error ? err.message : String(err);
    if (err instanceof NonRetryableError) {
        return Response.json({ error: message }, { status: 489, headers: { "tiffin-non-retryable": "true" } });
    }
    if (err instanceof RetryAfterError) {
        return Response.json({ error: message }, { status: 503, headers: { "tiffin-retry-after": String(Math.ceil(err.retryAfterMs / 1000)) } });
    }
    return Response.json({ error: message }, { status: 500 });
}
/** @internal Heartbeats for one attempt; aborts the controller when the attempt is over. */
export function heartbeater(d, ctrl) {
    return async () => {
        try {
            await boxCall("POST", `/v1/queue-internal/jobs/${d.id}/heartbeat`, { attemptId: d.attemptId });
        }
        catch (err) {
            if (err instanceof QueueError && err.status === 409)
                ctrl.abort(new Error("the box ended this attempt"));
            throw err;
        }
    };
}
/**
 * Wraps a job function as a fetch handler `(Request) => Promise<Response>`
 * for the route the box pushes to. The function's return value (JSON) is
 * stored as the job's output.
 */
export function defineHandler(fn, opts = {}) {
    return async (req) => {
        const d = await readDelivery(req, opts.secret);
        if (d instanceof Response)
            return d;
        const ctrl = new AbortController();
        const beat = heartbeater(d, ctrl);
        const job = {
            id: d.id,
            queue: d.queue,
            topic: d.topic,
            subscription: d.subscription,
            cron: d.cron,
            key: d.key,
            groupKey: d.groupKey,
            attempt: d.attempt,
            maxAttempts: d.maxAttempts,
            enqueuedAt: new Date(d.enqueuedAt),
            payload: d.payload,
            heartbeat: beat,
            signal: ctrl.signal,
        };
        const timer = opts.autoHeartbeat ? setInterval(() => beat().catch(() => { }), Math.max(1000, (d.leaseSeconds * 1000) / 3)) : undefined;
        try {
            const out = await fn(job);
            return out === undefined ? new Response(null, { status: 204 }) : Response.json(out);
        }
        catch (err) {
            return errorResponse(err);
        }
        finally {
            if (timer)
                clearInterval(timer);
        }
    };
}
