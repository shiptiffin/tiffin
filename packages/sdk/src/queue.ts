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

export type Env = Record<string, string | undefined>;
export type Duration = number | string;

export interface SendOptions {
  /** Run after this long. */
  delay?: Duration;
  /** Run at this time (delay is added to it). */
  runAt?: Date | string;
  /** Limit key: the queue's keyConcurrency and rateLimit apply per key (e.g. a customer ID). */
  key?: string;
  /** FIFO group: jobs with the same groupKey run one at a time, in send order. */
  groupKey?: string;
  /** Idempotency key: sending it again within 24 hours enqueues nothing and returns the first job. */
  dedupe?: string;
  priority?: "high" | "normal" | "low";
  /** App whose route handles the job (queues only). Default: the queue's configured app, else this app. */
  app?: string;
  /** Path on that app. Default /queues/<name>. */
  path?: string;
  /** Attempts before the dead-letter queue (default: the queue's, 10). */
  maxAttempts?: number;
}

export interface SendResult {
  /** Job IDs: one, or one per subscriber of a topic. */
  jobs: string[];
  topic: boolean;
  deduplicated: boolean;
  runAt: string;
  message: string;
}

/** Throw from a handler (or a workflow step) to fail without retrying. */
export class NonRetryableError extends Error {
  override name = "NonRetryableError";
}

/** Throw from a handler to retry after a specific delay (counts as an attempt). */
export class RetryAfterError extends Error {
  override name = "RetryAfterError";
  readonly retryAfterMs: number;
  constructor(after: Duration, message = "retry later") {
    super(message);
    this.retryAfterMs = toMs(after);
  }
}

/** A box API error with the box's stable code and hint. */
export class QueueError extends Error {
  override name = "QueueError";
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly hint?: string,
  ) {
    super(hint ? `${message} (${hint})` : message);
  }
}

/** Parses "30s", "5m", "2h", "1d", "500ms" or a number of milliseconds. */
export function toMs(d: Duration): number {
  if (typeof d === "number") {
    if (!Number.isFinite(d) || d < 0) throw new TypeError(`invalid duration ${d}`);
    return d;
  }
  const m = /^\s*(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)\s*$/.exec(d);
  if (!m) throw new TypeError(`invalid duration ${JSON.stringify(d)}: use a number of milliseconds or e.g. "30s", "5m", "2h", "1d"`);
  const unit = { ms: 1, s: 1e3, m: 60e3, h: 3600e3, d: 86400e3 }[m[2] as "ms" | "s" | "m" | "h" | "d"];
  return Math.round(Number(m[1]) * unit);
}

// ---- the box client ----

export interface ClientOptions {
  /** Box URL for apps; default TIFFIN_QUEUE_URL. */
  url?: string;
  /** Project app key; default TIFFIN_QUEUE_KEY. */
  key?: string;
  /** This app's name; default TIFFIN_APP. */
  app?: string;
  /** Signing secret for incoming pushes; default TIFFIN_QUEUE_SIGNING_SECRET. */
  signingSecret?: string;
  fetch?: typeof fetch;
  env?: Env;
}

let overrides: ClientOptions = {};

/** Overrides the box connection (tests, scripts outside the box). */
export function configure(opts: ClientOptions): void {
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
export async function boxCall<T>(method: string, path: string, body?: unknown): Promise<T> {
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
  let data: any = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = { detail: text };
  }
  if (!res.ok) {
    throw new QueueError(res.status, data?.code ?? "internal", data?.detail ?? `HTTP ${res.status}`, data?.hint || undefined);
  }
  return data as T;
}

/** @internal */
export function currentApp(): string {
  return settings().app;
}

function sendBody(name: string, payload: unknown, opts: SendOptions) {
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
export async function send(name: string, payload?: unknown, opts: SendOptions = {}): Promise<SendResult> {
  return boxCall<SendResult>("POST", "/v1/queue-internal/send", { ...sendBody(name, payload, opts), fromApp: currentApp() });
}

/**
 * A database handle sendTx can write with: Bun.sql / postgres.js (`unsafe`),
 * node-postgres (`query`), or a function `(sql, params) => Promise`.
 */
export type SqlExecutor =
  | { unsafe(query: string, params?: unknown[]): unknown }
  | { query(query: string, params?: unknown[]): unknown }
  | ((query: string, params: unknown[]) => unknown);

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
export async function sendTx(db: SqlExecutor, name: string, payload?: unknown, opts: SendOptions = {}): Promise<void> {
  const b = sendBody(name, payload, opts);
  const options: Record<string, unknown> = { ...b };
  delete options.name;
  delete options.payload;
  const params = [name, JSON.stringify(b.payload), JSON.stringify(options), currentApp()];
  if (typeof db === "function") await db(OUTBOX_INSERT, params);
  else if ("unsafe" in db) await db.unsafe(OUTBOX_INSERT, params);
  else if ("query" in db) await db.query(OUTBOX_INSERT, params);
  else throw new TypeError("sendTx needs a SQL handle with unsafe() or query(), or a function");
}

/** Asks the box to move committed sendTx rows into the queue now. */
export async function flush(): Promise<void> {
  await boxCall("POST", "/v1/queue-internal/outbox/kick", {});
}

export const queue = { send, sendTx, flush, configure };

// ---- receiving ----

/** What every push carries (the box's delivery body). */
export interface Delivery {
  type: "job" | "cron" | "workflow";
  id: string;
  queue: string;
  topic?: string;
  subscription?: string;
  cron?: string;
  key?: string;
  groupKey?: string;
  attempt: number;
  maxAttempts: number;
  attemptId: number;
  leaseSeconds: number;
  enqueuedAt: string;
  payload: unknown;
  run?: unknown;
}

export interface Job<T = unknown> {
  id: string;
  queue: string;
  /** Set for topic messages. */
  topic?: string | undefined;
  subscription?: string | undefined;
  /** Set for cron ticks (payload is { cron, schedule, scheduledAt }). */
  cron?: string | undefined;
  key?: string | undefined;
  groupKey?: string | undefined;
  /** 1 for the first attempt. */
  attempt: number;
  maxAttempts: number;
  enqueuedAt: Date;
  payload: T;
  /** Extends the lease; long work should call it at least every leaseSeconds. */
  heartbeat(): Promise<void>;
  /** Aborted when the box ends the attempt (lease lost, cancelled). */
  signal: AbortSignal;
}

/** Verifies a Tiffin-Signature header over the raw body. */
export function verifySignature(secret: string, header: string | null, body: string, toleranceSeconds = 300, now = Date.now()): boolean {
  if (!secret || !header) return false;
  let ts = "";
  let sig = "";
  for (const part of header.split(",")) {
    const [k, v] = part.trim().split("=", 2);
    if (k === "t") ts = v ?? "";
    if (k === "v1") sig = v ?? "";
  }
  const t = Number(ts);
  if (!Number.isInteger(t) || !sig || Math.abs(now / 1000 - t) > toleranceSeconds) return false;
  const want = createHmac("sha256", secret).update(`${ts}.${body}`).digest("hex");
  return want.length === sig.length && timingSafeEqual(Buffer.from(want), Buffer.from(sig));
}

/** Signs a body the way the box does (tests, local tools). */
export function sign(secret: string, body: string, now = Date.now()): string {
  const ts = Math.floor(now / 1000).toString();
  return `t=${ts},v1=${createHmac("sha256", secret).update(`${ts}.${body}`).digest("hex")}`;
}

/** @internal Reads and verifies a push. Returns a Response to send back on failure. */
export async function readDelivery(req: Request, secret?: string): Promise<Delivery | Response> {
  if (req.method !== "POST") return new Response("method not allowed", { status: 405 });
  const body = await req.text();
  const key = secret ?? settings().secret;
  if (!verifySignature(key, req.headers.get("tiffin-signature"), body)) {
    return Response.json({ error: "invalid or missing Tiffin-Signature" }, { status: 401 });
  }
  try {
    return JSON.parse(body) as Delivery;
  } catch {
    return Response.json({ error: "body is not JSON" }, { status: 400 });
  }
}

/** @internal Maps an error to the box's retry protocol. */
export function errorResponse(err: unknown): Response {
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
export function heartbeater(d: Delivery, ctrl: AbortController) {
  return async () => {
    try {
      await boxCall("POST", `/v1/queue-internal/jobs/${d.id}/heartbeat`, { attemptId: d.attemptId });
    } catch (err) {
      if (err instanceof QueueError && err.status === 409) ctrl.abort(new Error("the box ended this attempt"));
      throw err;
    }
  };
}

export interface HandlerOptions {
  /** Heartbeat every leaseSeconds/3 while the handler runs. Default false. */
  autoHeartbeat?: boolean;
  /** Signing secret; default TIFFIN_QUEUE_SIGNING_SECRET. */
  secret?: string;
}

/**
 * Wraps a job function as a fetch handler `(Request) => Promise<Response>`
 * for the route the box pushes to. The function's return value (JSON) is
 * stored as the job's output.
 */
export function defineHandler<T = unknown>(fn: (job: Job<T>) => unknown, opts: HandlerOptions = {}): (req: Request) => Promise<Response> {
  return async (req) => {
    const d = await readDelivery(req, opts.secret);
    if (d instanceof Response) return d;
    const ctrl = new AbortController();
    const beat = heartbeater(d, ctrl);
    const job: Job<T> = {
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
      payload: d.payload as T,
      heartbeat: beat,
      signal: ctrl.signal,
    };
    const timer = opts.autoHeartbeat ? setInterval(() => beat().catch(() => {}), Math.max(1000, (d.leaseSeconds * 1000) / 3)) : undefined;
    try {
      const out = await fn(job);
      return out === undefined ? new Response(null, { status: 204 }) : Response.json(out);
    } catch (err) {
      return errorResponse(err);
    } finally {
      if (timer) clearInterval(timer);
    }
  };
}
