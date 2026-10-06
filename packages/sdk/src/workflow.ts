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
import {
  boxCall,
  currentApp,
  errorResponse,
  heartbeater,
  NonRetryableError,
  readDelivery,
  reporter,
  subscribeToken,
  toMs,
  type Delivery,
  type Duration,
  type TokenOptions,
} from "./queue";

export { NonRetryableError, subscribeToken } from "./queue";

/** Where the box POSTs workflow turns by default. */
export const DEFAULT_PATH = "/_tiffin/workflows";

/** The code took a different path than this run's recorded history. */
export class NonDeterminismError extends NonRetryableError {
  override name = "NonDeterminismError";
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

export type WaitResult<T = unknown> = { timedOut: false; payload: T } | { timedOut: true };

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
  wait(opts?: { timeout?: Duration | undefined }): Promise<WaitResult<T>>;
}

export interface WorkflowContext {
  runId: string;
  workflow: string;
  /** The app release this run is pinned to. */
  release?: string | undefined;
  /** Runs fn once and checkpoints its JSON result. */
  step<T>(name: string, fn: () => T | Promise<T>, opts?: { maxAttempts?: number }): Promise<T>;
  /** Pauses the run (a single scheduled row; months are fine). */
  sleep(name: string, duration: Duration): Promise<void>;
  sleepUntil(name: string, until: Date | string): Promise<void>;
  /** Waits for a named event (workflow.emit). The first emit of a name wins; early events are kept. */
  waitForEvent<T = unknown>(name: string, opts: { event: string; timeout?: Duration | undefined }): Promise<WaitResult<T>>;
  /** Waits for a person (dashboard, CLI, MCP) to approve or reject. humanOnly (default true) refuses agent tokens. */
  approval(name: string, opts: { title?: string; description?: string; timeout?: Duration; humanOnly?: boolean }): Promise<ApprovalDecision>;
  /** A signed URL that resumes the run when called. */
  webhook<T = unknown>(name: string): Promise<Webhook<T>>;
  /** Runs branches in parallel (Promise.all that keeps every finished step even if one branch waits). */
  all<T extends readonly unknown[]>(branches: { [K in keyof T]: Promise<T[K]> | (() => Promise<T[K]>) }): Promise<T>;
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
  start(input: I, opts?: StartOptions): Promise<{ run: RunInfo; created: boolean }>;
  startWithToken(input: I, opts?: StartOptions & TokenOptions): Promise<{ id: string; token: string }>;
}

const registry = new Map<string, Workflow<any, any>>();

/** Defines a workflow in this app. Names: lowercase letters, digits, . _ - */
export function define<I = unknown, O = unknown>(name: string, fn: WorkflowFn<I, O>): Workflow<I, O> {
  if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)) throw new TypeError(`invalid workflow name ${JSON.stringify(name)}`);
  const wf: Workflow<I, O> = {
    name,
    fn,
    start: (input, opts) => start(name, input, opts),
    startWithToken: (input, opts) => startWithToken(name, input, opts),
  };
  registry.set(name, wf);
  return wf;
}

/** Starts a run of a workflow (defined here or in another app of the project). */
export async function start(name: string, input?: unknown, opts: StartOptions = {}): Promise<{ run: RunInfo; created: boolean }> {
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
export async function startWithToken(name: string, input?: unknown, opts: StartOptions & TokenOptions = {}): Promise<{ id: string; token: string }> {
  const { run } = await start(name, input, opts);
  return { id: run.id, token: subscribeToken(run.id, opts) };
}

/** Emits an event; every run waiting for it resumes. The first emit of a name wins. */
export async function emit(event: string, payload?: unknown): Promise<{ accepted: boolean; woke: string[]; message: string }> {
  return boxCall("POST", "/v1/queue-internal/workflows/events", { name: event, payload: payload === undefined ? null : payload, fromApp: currentApp() });
}

/** Reads a run's state and steps. */
export async function get(runId: string): Promise<RunInfo> {
  return boxCall("GET", `/v1/queue-internal/workflows/runs/${encodeURIComponent(runId)}`);
}

class Suspend {
  constructor(readonly reason: string) {}
}

class Turn {
  seq = 0;
  readonly byName = new Map<string, StepRecord>();
  readonly bySeq = new Map<number, StepRecord>();
  readonly inflight = new Set<Promise<unknown>>();
  readonly maxRecordedSeq: number;
  readonly report: ReturnType<typeof reporter>;

  constructor(readonly run: TurnRun) {
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
  replaying(): boolean {
    return this.seq <= this.maxRecordedSeq;
  }

  track<T>(p: Promise<T>): Promise<T> {
    this.inflight.add(p);
    p.then(
      () => this.inflight.delete(p),
      () => this.inflight.delete(p),
    );
    return p;
  }

  /** Claims the next position and checks it against history. */
  claim(name: string, kind: StepRecord["kind"]): { seq: number; rec: StepRecord | undefined } {
    if (!name || name.length > 200) throw new NonRetryableError("step names are 1-200 characters");
    const seq = this.seq++;
    const rec = this.byName.get(name);
    if (rec && rec.kind !== kind) {
      throw new NonDeterminismError(`"${name}" was a ${rec.kind} in this run's history but is now a ${kind}; guard code changes with ctx.patched()`);
    }
    const there = this.bySeq.get(seq);
    if (!rec && there && there.name !== name) {
      throw new NonDeterminismError(
        `position ${seq} ran "${there.name}" in this run's history but the code now runs "${name}" there; steps must run in the same order (use ctx.patched() for changes)`,
      );
    }
    return { seq, rec };
  }

  async wait(body: Record<string, unknown>): Promise<StepRecord> {
    const rec = await boxCall<StepRecord>("POST", `/v1/queue-internal/workflows/runs/${this.run.id}/waits`, body);
    this.byName.set(rec.name, rec);
    return rec;
  }
}

function ctxFor(t: Turn): WorkflowContext {
  const run = t.run;
  const waitFor = async (name: string, kind: "event" | "approval", extra: Record<string, unknown>) => {
    const { seq, rec } = t.claim(name, kind);
    let r = rec;
    if (!r || r.state === "waiting") {
      if (r?.state === "waiting") throw new Suspend(`${kind} ${name}`);
      r = await t.wait({ name, seq, kind, ...extra });
    }
    if (r.state === "waiting") throw new Suspend(`${kind} ${name}`);
    return r;
  };
  const ctx: WorkflowContext = {
    runId: run.id,
    workflow: run.workflow,
    release: run.release,
    step(name, fn, opts = {}) {
      return t.track(
        (async () => {
          const { seq, rec } = t.claim(name, "step");
          if (rec?.state === "completed") return rec.output as any;
          const attempts = rec?.attempts ?? 0;
          if (opts.maxAttempts && attempts >= opts.maxAttempts) {
            throw new NonRetryableError(`step "${name}" failed ${attempts} time(s); last error: ${rec?.error ?? "unknown"}`);
          }
          const started = new Date();
          try {
            const value = await fn();
            const output = value === undefined ? null : JSON.parse(JSON.stringify(value));
            await boxCall("POST", `/v1/queue-internal/workflows/runs/${run.id}/steps`, {
              name,
              seq,
              ok: true,
              output,
              startedAt: started.toISOString(),
              durationMs: Date.now() - started.getTime(),
            });
            return output;
          } catch (err) {
            if (err instanceof Suspend) throw err;
            await boxCall("POST", `/v1/queue-internal/workflows/runs/${run.id}/steps`, {
              name,
              seq,
              ok: false,
              error: err instanceof Error ? `${err.name}: ${err.message}` : String(err),
              startedAt: started.toISOString(),
              durationMs: Date.now() - started.getTime(),
            }).catch(() => {});
            throw err;
          }
        })(),
      );
    },
    async sleep(name, duration) {
      // The wake time is fixed the first time; replays use the recorded one.
      const { seq, rec } = t.claim(name, "sleep");
      if (rec && rec.state !== "waiting") return;
      if (rec) throw new Suspend(`sleep ${name}`);
      const r = await t.wait({ name, seq, kind: "sleep", until: new Date(Date.now() + toMs(duration)).toISOString() });
      if (r.state === "waiting") throw new Suspend(`sleep ${name}`);
    },
    async sleepUntil(name, until) {
      const { seq, rec } = t.claim(name, "sleep");
      if (rec && rec.state !== "waiting") return;
      if (rec) throw new Suspend(`sleep ${name}`);
      const r = await t.wait({ name, seq, kind: "sleep", until: new Date(until).toISOString() });
      if (r.state === "waiting") throw new Suspend(`sleep ${name}`);
    },
    async waitForEvent<T>(name: string, opts: { event: string; timeout?: Duration | undefined }) {
      const r = await waitFor(name, "event", { event: opts.event, timeoutSeconds: opts.timeout ? Math.ceil(toMs(opts.timeout) / 1000) : undefined });
      if (r.state === "timed_out") return { timedOut: true } as const;
      return { timedOut: false, payload: r.output as T } as const;
    },
    async approval(name, opts) {
      const r = await waitFor(name, "approval", {
        title: opts.title ?? name,
        description: opts.description,
        humanOnly: opts.humanOnly ?? true,
        timeoutSeconds: opts.timeout ? Math.ceil(toMs(opts.timeout) / 1000) : undefined,
      });
      if (r.state === "timed_out") return { approved: false, timedOut: true };
      return r.output as ApprovalDecision;
    },
    async webhook<T>(name: string) {
      const { seq, rec } = t.claim(name, "webhook");
      const r = rec ?? (await t.wait({ name, seq, kind: "webhook" }));
      const hook = r.output as { url: string; event: string };
      return {
        url: hook.url,
        wait: (opts: { timeout?: Duration | undefined } = {}) => ctx.waitForEvent<T>(`${name}:wait`, { event: hook.event, timeout: opts.timeout }),
      } satisfies Webhook<T>;
    },
    async all(branches) {
      const ps = (branches as readonly unknown[]).map((b) => t.track(Promise.resolve(typeof b === "function" ? (b as () => unknown)() : b)));
      return (await Promise.all(ps)) as any;
    },
    patched(id) {
      const key = `patch:${id}`;
      if (t.byName.has(key)) return true;
      // History reaching past this point was made by code without the patch.
      if (t.maxRecordedSeq >= t.seq) return false;
      const rec: StepRecord = { name: key, seq: t.seq, kind: "patch", state: "completed", attempts: 0 };
      t.byName.set(key, rec);
      t.track(t.wait({ name: key, seq: t.seq, kind: "patch" }));
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
export async function runTurn(run: TurnRun): Promise<{ status: "completed"; output: unknown } | { status: "suspended" }> {
  const wf = registry.get(run.workflow);
  if (!wf) {
    throw new NonRetryableError(`workflow "${run.workflow}" is not defined in this app (defined: ${[...registry.keys()].join(", ") || "none"})`);
  }
  const t = new Turn(run);
  try {
    const out = await wf.fn(ctxFor(t), run.input);
    await Promise.allSettled([...t.inflight]);
    return { status: "completed", output: out === undefined ? null : out };
  } catch (err) {
    // Let parallel branches finish and checkpoint before the turn ends.
    while (t.inflight.size) await Promise.allSettled([...t.inflight]);
    if (err instanceof Suspend) return { status: "suspended" };
    throw err;
  } finally {
    await t.report.flush(); // progress lands before the run moves on
  }
}

/**
 * The fetch handler the box pushes turns to. Mount it at /_tiffin/workflows
 * (or pass that path to start()).
 */
export function handler(opts: { secret?: string } = {}): (req: Request) => Promise<Response> {
  return async (req) => {
    const d = await readDelivery(req, opts.secret);
    if (d instanceof Response) return d;
    if (d.type !== "workflow" || !d.run) return Response.json({ error: "not a workflow turn" }, { status: 400 });
    const ctrl = new AbortController();
    const beat = heartbeater(d as Delivery, ctrl);
    const timer = setInterval(() => beat().catch(() => {}), Math.max(1000, (d.leaseSeconds * 1000) / 3));
    try {
      return Response.json(await runTurn(d.run as TurnRun));
    } catch (err) {
      return errorResponse(err);
    } finally {
      clearInterval(timer);
    }
  };
}

export const workflow = { define, start, startWithToken, subscribeToken, emit, get, handler, DEFAULT_PATH };
