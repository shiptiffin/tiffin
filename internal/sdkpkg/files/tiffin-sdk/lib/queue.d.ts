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
export declare class NonRetryableError extends Error {
    name: string;
}
/** Throw from a handler to retry after a specific delay (counts as an attempt). */
export declare class RetryAfterError extends Error {
    name: string;
    readonly retryAfterMs: number;
    constructor(after: Duration, message?: string);
}
/** A box API error with the box's stable code and hint. */
export declare class QueueError extends Error {
    readonly status: number;
    readonly code: string;
    readonly hint?: string | undefined;
    name: string;
    constructor(status: number, code: string, message: string, hint?: string | undefined);
}
/** Parses "30s", "5m", "2h", "1d", "500ms" or a number of milliseconds. */
export declare function toMs(d: Duration): number;
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
/** Overrides the box connection (tests, scripts outside the box). */
export declare function configure(opts: ClientOptions): void;
/** @internal Calls an app-facing box endpoint. */
export declare function boxCall<T>(method: string, path: string, body?: unknown): Promise<T>;
/** @internal */
export declare function currentApp(): string;
/** Sends a job to a queue, or publishes to a topic (one job per subscriber). */
export declare function send(name: string, payload?: unknown, opts?: SendOptions): Promise<SendResult>;
/**
 * A database handle sendTx can write with: Bun.sql / postgres.js (`unsafe`),
 * node-postgres (`query`), or a function `(sql, params) => Promise`.
 */
export type SqlExecutor = {
    unsafe(query: string, params?: unknown[]): unknown;
} | {
    query(query: string, params?: unknown[]): unknown;
} | ((query: string, params: unknown[]) => unknown);
/** The statement sendTx runs (the box creates tiffin.outbox in every project database). */
export declare const OUTBOX_INSERT = "INSERT INTO tiffin.outbox (name, payload, options, app) VALUES ($1, $2::jsonb, $3::jsonb, $4)";
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
export declare function sendTx(db: SqlExecutor, name: string, payload?: unknown, opts?: SendOptions): Promise<void>;
/** Asks the box to move committed sendTx rows into the queue now. */
export declare function flush(): Promise<void>;
export declare const queue: {
    send: typeof send;
    sendTx: typeof sendTx;
    flush: typeof flush;
    configure: typeof configure;
};
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
export declare function verifySignature(secret: string, header: string | null, body: string, toleranceSeconds?: number, now?: number): boolean;
/** Signs a body the way the box does (tests, local tools). */
export declare function sign(secret: string, body: string, now?: number): string;
/** @internal Reads and verifies a push. Returns a Response to send back on failure. */
export declare function readDelivery(req: Request, secret?: string): Promise<Delivery | Response>;
/** @internal Maps an error to the box's retry protocol. */
export declare function errorResponse(err: unknown): Response;
/** @internal Heartbeats for one attempt; aborts the controller when the attempt is over. */
export declare function heartbeater(d: Delivery, ctrl: AbortController): () => Promise<void>;
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
export declare function defineHandler<T = unknown>(fn: (job: Job<T>) => unknown, opts?: HandlerOptions): (req: Request) => Promise<Response>;
