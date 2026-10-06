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
import { NonRetryableError, subscribeToken, type Duration, type TokenOptions } from "./queue.js";
export { NonRetryableError, subscribeToken } from "./queue.js";
/** Where the box POSTs workflow turns by default. */
export declare const DEFAULT_PATH = "/_tiffin/workflows";
/** The code took a different path than this run's recorded history. */
export declare class NonDeterminismError extends NonRetryableError {
    name: string;
}
export interface StepRecord {
    name: string;
    seq: number;
    kind: "step" | "sleep" | "event" | "approval" | "webhook" | "patch";
    state: "completed" | "failed" | "waiting" | "timed_out" | "cancelled";
    output?: unknown;
    error?: string;
    attempts: number;
    waitUntil?: string;
    event?: string;
}
interface TurnRun {
    id: string;
    workflow: string;
    input: unknown;
    release?: string;
    turn: number;
    createdAt: string;
    steps: StepRecord[];
}
export type WaitResult<T = unknown> = {
    timedOut: false;
    payload: T;
} | {
    timedOut: true;
};
export interface ApprovalDecision {
    approved: boolean;
    /** Who decided (token name and kind); absent on timeout. */
    by?: string;
    comment?: string;
    decidedAt?: string;
    timedOut?: boolean;
}
export interface Webhook<T = unknown> {
    /** POST anything to this URL (public, unguessable) to resume the run. */
    url: string;
    /** Waits for the call; the body (JSON, or { body, contentType }) is the payload. */
    wait(opts?: {
        timeout?: Duration | undefined;
    }): Promise<WaitResult<T>>;
}
export interface WorkflowContext {
    runId: string;
    workflow: string;
    /** The app release this run is pinned to. */
    release?: string | undefined;
    /** Runs fn once and checkpoints its JSON result. */
    step<T>(name: string, fn: () => T | Promise<T>, opts?: {
        maxAttempts?: number;
    }): Promise<T>;
    /** Pauses the run (a single scheduled row; months are fine). */
    sleep(name: string, duration: Duration): Promise<void>;
    sleepUntil(name: string, until: Date | string): Promise<void>;
    /** Waits for a named event (workflow.emit). The first emit of a name wins; early events are kept. */
    waitForEvent<T = unknown>(name: string, opts: {
        event: string;
        timeout?: Duration | undefined;
    }): Promise<WaitResult<T>>;
    /** Waits for a person (dashboard, CLI, MCP) to approve or reject. humanOnly (default true) refuses agent tokens. */
    approval(name: string, opts: {
        title?: string;
        description?: string;
        timeout?: Duration;
        humanOnly?: boolean;
    }): Promise<ApprovalDecision>;
    /** A signed URL that resumes the run when called. */
    webhook<T = unknown>(name: string): Promise<Webhook<T>>;
    /** Runs branches in parallel (Promise.all that keeps every finished step even if one branch waits). */
    all<T extends readonly unknown[]>(branches: {
        [K in keyof T]: Promise<T[K]> | (() => Promise<T[K]>);
    }): Promise<T>;
    /** True for runs that reach this point with the new code, false for runs whose history predates it. */
    patched(id: string): boolean;
    /** Reports progress (small JSON, at most 16 KB); browsers watching the run see the latest value. */
    progress(value: unknown): Promise<void>;
    /** Appends a chunk of output (JSON, at most 64 KB) that browsers watching the run receive in order. */
    stream(chunk: unknown): Promise<void>;
}
export type WorkflowFn<I, O> = (ctx: WorkflowContext, input: I) => Promise<O>;
export interface StartOptions {
    /** Idempotency key: starting again with the same id returns the same run. */
    id?: string;
    /** App that defines the workflow; default this app. */
    app?: string;
    /** Where that app mounts workflow.handler(); default /_tiffin/workflows. */
    path?: string;
}
export interface RunInfo {
    id: string;
    workflow: string;
    app: string;
    release?: string;
    state: "running" | "waiting" | "completed" | "failed" | "cancelled";
    waitingFor?: string;
    output?: unknown;
    error?: string;
    turns: number;
    createdAt: string;
    steps?: StepRecord[];
}
export interface Workflow<I, O> {
    name: string;
    fn: WorkflowFn<I, O>;
    start(input: I, opts?: StartOptions): Promise<{
        run: RunInfo;
        created: boolean;
    }>;
    startWithToken(input: I, opts?: StartOptions & TokenOptions): Promise<{
        id: string;
        token: string;
    }>;
}
/** Defines a workflow in this app. Names: lowercase letters, digits, . _ - */
export declare function define<I = unknown, O = unknown>(name: string, fn: WorkflowFn<I, O>): Workflow<I, O>;
/** Starts a run of a workflow (defined here or in another app of the project). */
export declare function start(name: string, input?: unknown, opts?: StartOptions): Promise<{
    run: RunInfo;
    created: boolean;
}>;
/**
 * Starts a run and returns its ID with a token for the browser: what a
 * server action returns so the page can show the run's progress live.
 */
export declare function startWithToken(name: string, input?: unknown, opts?: StartOptions & TokenOptions): Promise<{
    id: string;
    token: string;
}>;
/** Emits an event; every run waiting for it resumes. The first emit of a name wins. */
export declare function emit(event: string, payload?: unknown): Promise<{
    accepted: boolean;
    woke: string[];
    message: string;
}>;
/** Reads a run's state and steps. */
export declare function get(runId: string): Promise<RunInfo>;
/** @internal Runs one turn; returns the response body for the box. */
export declare function runTurn(run: TurnRun): Promise<{
    status: "completed";
    output: unknown;
} | {
    status: "suspended";
}>;
/**
 * The fetch handler the box pushes turns to. Mount it at /_tiffin/workflows
 * (or pass that path to start()).
 */
export declare function handler(opts?: {
    secret?: string;
}): (req: Request) => Promise<Response>;
export declare const workflow: {
    define: typeof define;
    start: typeof start;
    startWithToken: typeof startWithToken;
    subscribeToken: typeof subscribeToken;
    emit: typeof emit;
    get: typeof get;
    handler: typeof handler;
    DEFAULT_PATH: string;
};
