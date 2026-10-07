// @bun
// Generated from @shiptiffin/sdk/queue and @shiptiffin/sdk/workflow by `bun run sync-sdk`; do not edit. Once @shiptiffin/sdk is on npm, import from @shiptiffin/sdk/queue and @shiptiffin/sdk/workflow instead.

// ../../packages/sdk/src/queue.ts
import { createHmac as createHmac2 } from "crypto";

// ../../packages/sdk/src/verify.ts
import { createHmac, timingSafeEqual } from "crypto";
function verifySignature(secret, header, body, toleranceSeconds = 300, now = Date.now()) {
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

// ../../packages/sdk/src/queue.ts
class NonRetryableError extends Error {
  name = "NonRetryableError";
}

class RetryAfterError extends Error {
  name = "RetryAfterError";
  retryAfterMs;
  constructor(after, message = "retry later") {
    super(message);
    this.retryAfterMs = toMs(after);
  }
}

class QueueError extends Error {
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
function toMs(d) {
  if (typeof d === "number") {
    if (!Number.isFinite(d) || d < 0)
      throw new TypeError(`invalid duration ${d}`);
    return d;
  }
  const m = /^\s*(\d+(?:\.\d+)?)\s*(ms|s|m|h|d)\s*$/.exec(d);
  if (!m)
    throw new TypeError(`invalid duration ${JSON.stringify(d)}: use a number of milliseconds or e.g. "30s", "5m", "2h", "1d"`);
  const unit = { ms: 1, s: 1000, m: 60000, h: 3600000, d: 86400000 }[m[2]];
  return Math.round(Number(m[1]) * unit);
}
var overrides = {};
function configure(opts) {
  overrides = { ...opts };
}
function settings() {
  const env = overrides.env ?? (typeof process !== "undefined" ? process.env : {});
  return {
    url: (overrides.url ?? env.TIFFIN_QUEUE_URL ?? "").replace(/\/$/, ""),
    key: overrides.key ?? env.TIFFIN_QUEUE_KEY ?? "",
    app: overrides.app ?? env.TIFFIN_APP ?? "",
    project: overrides.project ?? env.TIFFIN_PROJECT ?? "",
    secret: overrides.signingSecret ?? env.TIFFIN_QUEUE_SIGNING_SECRET ?? "",
    fetch: overrides.fetch ?? fetch
  };
}
async function boxCall(method, path, body) {
  const s = settings();
  if (!s.url || !s.key) {
    throw new QueueError(0, "precondition", "TIFFIN_QUEUE_URL and TIFFIN_QUEUE_KEY are not set", "run on a Tiffin box, or call configure({ url, key })");
  }
  const res = await s.fetch(s.url + path, {
    method,
    headers: { "content-type": "application/json", authorization: `Bearer ${s.key}` },
    body: body === undefined ? null : JSON.stringify(body)
  });
  const text = await res.text();
  let data = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = { detail: text };
  }
  if (!res.ok) {
    throw new QueueError(res.status, data?.code ?? "internal", data?.detail ?? `HTTP ${res.status}`, data?.hint || undefined);
  }
  return data;
}
function currentApp() {
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
    maxAttempts: opts.maxAttempts
  };
}
async function send(name, payload, opts = {}) {
  return boxCall("POST", "/v1/queue-internal/send", { ...sendBody(name, payload, opts), fromApp: currentApp() });
}
var OUTBOX_INSERT = "INSERT INTO tiffin_queue.outbox (name, payload, options, app) VALUES ($1, $2::jsonb, $3::jsonb, $4)";
async function sendTx(db, name, payload, opts = {}) {
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
async function flush() {
  await boxCall("POST", "/v1/queue-internal/outbox/kick", {});
}
function subscribeToken(id, opts = {}) {
  const s = settings();
  if (!s.secret || !s.project) {
    throw new QueueError(0, "precondition", "TIFFIN_QUEUE_SIGNING_SECRET and TIFFIN_PROJECT are not set", "run on a Tiffin box, or call configure({ signingSecret, project })");
  }
  if (!/^(job_\d+|run_[A-Za-z0-9]+)$/.test(id))
    throw new TypeError(`${JSON.stringify(id)} is not a job or run ID`);
  const ttl = toMs(opts.ttl ?? "1h");
  if (ttl < 1000 || ttl > 7 * 86400000)
    throw new TypeError("ttl must be between 1s and 7d");
  const exp = Math.floor((Date.now() + ttl) / 1000).toString();
  const sig = createHmac2("sha256", s.secret).update(`tiffin-live:${s.project}:${id}:${exp}`).digest("hex");
  return `live1.${s.project}.${id}.${exp}.${sig}`;
}
async function sendWithToken(name, payload, opts = {}) {
  const res = await send(name, payload, opts);
  const id = res.jobs[0];
  if (res.jobs.length !== 1 || !id) {
    throw new QueueError(0, "validation", `"${name}" is a topic: it made ${res.jobs.length} jobs`, "watch one queue's job, or mint a token per job with subscribeToken");
  }
  return { id, token: subscribeToken(id, opts) };
}
var queue = { send, sendTx, sendWithToken, subscribeToken, flush, configure };
function sign(secret, body, now = Date.now()) {
  const ts = Math.floor(now / 1000).toString();
  return `t=${ts},v1=${createHmac2("sha256", secret).update(`${ts}.${body}`).digest("hex")}`;
}
async function readDelivery(req, secret) {
  if (req.method !== "POST")
    return new Response("method not allowed", { status: 405 });
  const body = await req.text();
  const key = secret ?? settings().secret;
  if (!verifySignature(key, req.headers.get("tiffin-signature"), body)) {
    return Response.json({ error: "invalid or missing Tiffin-Signature" }, { status: 401 });
  }
  try {
    return JSON.parse(body);
  } catch {
    return Response.json({ error: "body is not JSON" }, { status: 400 });
  }
}
function errorResponse(err) {
  const message = err instanceof Error ? err.message : String(err);
  if (err instanceof NonRetryableError) {
    return Response.json({ error: message }, { status: 489, headers: { "tiffin-non-retryable": "true" } });
  }
  if (err instanceof RetryAfterError) {
    return Response.json({ error: message }, { status: 503, headers: { "tiffin-retry-after": String(Math.ceil(err.retryAfterMs / 1000)) } });
  }
  return Response.json({ error: message }, { status: 500 });
}
function heartbeater(d, ctrl) {
  return async () => {
    try {
      await boxCall("POST", `/v1/queue-internal/jobs/${d.id}/heartbeat`, { attemptId: d.attemptId });
    } catch (err) {
      if (err instanceof QueueError && err.status === 409)
        ctrl.abort(new Error("the box ended this attempt"));
      throw err;
    }
  };
}
function reporter(base, extra = {}) {
  let chain = Promise.resolve();
  const post = (kind, value) => {
    const json = JSON.stringify(value === undefined ? null : value);
    const limit = kind === "progress" ? 16 << 10 : 64 << 10;
    const size = new TextEncoder().encode(json).length;
    if (size > limit)
      throw new TypeError(`${kind === "progress" ? "progress" : "an output chunk"} is ${size} bytes; the limit is ${limit >> 10} KB`);
    const body = { ...extra, [kind === "progress" ? "progress" : "data"]: JSON.parse(json) };
    chain = chain.then(() => boxCall("POST", `${base}/${kind}`, body).then(() => {}, (err) => console.warn(`tiffin: ${kind} not recorded: ${err instanceof Error ? err.message : err}`)));
    return chain;
  };
  return { progress: (v) => post("progress", v), output: (v) => post("output", v), flush: () => chain };
}
function defineHandler(fn, opts = {}) {
  return async (req) => {
    const d = await readDelivery(req, opts.secret);
    if (d instanceof Response)
      return d;
    const ctrl = new AbortController;
    const beat = heartbeater(d, ctrl);
    const rep = reporter(`/v1/queue-internal/jobs/${d.id}`, { attemptId: d.attemptId });
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
      progress: rep.progress,
      log: rep.output,
      signal: ctrl.signal
    };
    const timer = opts.autoHeartbeat ? setInterval(() => beat().catch(() => {}), Math.max(1000, d.leaseSeconds * 1000 / 3)) : undefined;
    try {
      const out = await fn(job);
      return out === undefined ? new Response(null, { status: 204 }) : Response.json(out);
    } catch (err) {
      return errorResponse(err);
    } finally {
      if (timer)
        clearInterval(timer);
      await rep.flush();
    }
  };
}
// ../../packages/sdk/src/workflow.ts
var DEFAULT_PATH = "/_tiffin/workflows";

class NonDeterminismError extends NonRetryableError {
  name = "NonDeterminismError";
}
var registry = new Map;
function define(name, fn) {
  if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name))
    throw new TypeError(`invalid workflow name ${JSON.stringify(name)}`);
  const wf = {
    name,
    fn,
    start: (input, opts) => start(name, input, opts),
    startWithToken: (input, opts) => startWithToken(name, input, opts)
  };
  registry.set(name, wf);
  return wf;
}
async function start(name, input, opts = {}) {
  return boxCall("POST", "/v1/queue-internal/workflows/start", {
    workflow: name,
    input: input === undefined ? null : input,
    id: opts.id,
    app: opts.app,
    path: opts.path,
    fromApp: currentApp()
  });
}
async function startWithToken(name, input, opts = {}) {
  const { run } = await start(name, input, opts);
  return { id: run.id, token: subscribeToken(run.id, opts) };
}
async function emit(event, payload) {
  return boxCall("POST", "/v1/queue-internal/workflows/events", { name: event, payload: payload === undefined ? null : payload, fromApp: currentApp() });
}
async function get(runId) {
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
  seq = 0;
  byName = new Map;
  bySeq = new Map;
  inflight = new Set;
  maxRecordedSeq;
  report;
  constructor(run) {
    this.run = run;
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
  replaying() {
    return this.seq <= this.maxRecordedSeq;
  }
  track(p) {
    this.inflight.add(p);
    p.then(() => this.inflight.delete(p), () => this.inflight.delete(p));
    return p;
  }
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
    const rec = await boxCall("POST", `/v1/queue-internal/workflows/runs/${this.run.id}/waits`, body);
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
        const started = new Date;
        try {
          const value = await fn();
          const output = value === undefined ? null : JSON.parse(JSON.stringify(value));
          await boxCall("POST", `/v1/queue-internal/workflows/runs/${run.id}/steps`, {
            name,
            seq,
            ok: true,
            output,
            startedAt: started.toISOString(),
            durationMs: Date.now() - started.getTime()
          });
          return output;
        } catch (err) {
          if (err instanceof Suspend)
            throw err;
          await boxCall("POST", `/v1/queue-internal/workflows/runs/${run.id}/steps`, {
            name,
            seq,
            ok: false,
            error: err instanceof Error ? `${err.name}: ${err.message}` : String(err),
            startedAt: started.toISOString(),
            durationMs: Date.now() - started.getTime()
          }).catch(() => {});
          throw err;
        }
      })());
    },
    async sleep(name, duration) {
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
        timeoutSeconds: opts.timeout ? Math.ceil(toMs(opts.timeout) / 1000) : undefined
      });
      if (r.state === "timed_out")
        return { approved: false, timedOut: true };
      return r.output;
    },
    async webhook(name) {
      const { seq, rec } = t.claim(name, "webhook");
      const r = rec ?? await t.wait({ name, seq, kind: "webhook" });
      const hook = r.output;
      return {
        url: hook.url,
        wait: (opts = {}) => ctx.waitForEvent(`${name}:wait`, { event: hook.event, timeout: opts.timeout })
      };
    },
    async all(branches) {
      const ps = branches.map((b) => t.track(Promise.resolve(typeof b === "function" ? b() : b)));
      return await Promise.all(ps);
    },
    patched(id) {
      const key = `patch:${id}`;
      if (t.byName.has(key))
        return true;
      if (t.maxRecordedSeq >= t.seq)
        return false;
      const rec = { name: key, seq: t.seq, kind: "patch", state: "completed", attempts: 0 };
      t.byName.set(key, rec);
      t.track(t.wait({ name: key, seq: t.seq, kind: "patch" }));
      return true;
    },
    progress(value) {
      return t.replaying() ? Promise.resolve() : t.report.progress(value);
    },
    stream(chunk) {
      return t.replaying() ? Promise.resolve() : t.report.output(chunk);
    }
  };
  return ctx;
}
async function runTurn(run) {
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
    while (t.inflight.size)
      await Promise.allSettled([...t.inflight]);
    if (err instanceof Suspend)
      return { status: "suspended" };
    throw err;
  } finally {
    await t.report.flush();
  }
}
function handler(opts = {}) {
  return async (req) => {
    const d = await readDelivery(req, opts.secret);
    if (d instanceof Response)
      return d;
    if (d.type !== "workflow" || !d.run)
      return Response.json({ error: "not a workflow turn" }, { status: 400 });
    const ctrl = new AbortController;
    const beat = heartbeater(d, ctrl);
    const timer = setInterval(() => beat().catch(() => {}), Math.max(1000, d.leaseSeconds * 1000 / 3));
    try {
      return Response.json(await runTurn(d.run));
    } catch (err) {
      return errorResponse(err);
    } finally {
      clearInterval(timer);
    }
  };
}
var workflow = { define, start, startWithToken, subscribeToken, emit, get, handler, DEFAULT_PATH };
export {
  workflow,
  verifySignature,
  toMs,
  subscribeToken,
  sign,
  sendWithToken,
  sendTx,
  send,
  reporter,
  readDelivery,
  queue,
  heartbeater,
  flush,
  errorResponse,
  defineHandler,
  currentApp,
  configure,
  boxCall,
  RetryAfterError,
  QueueError,
  OUTBOX_INSERT,
  NonRetryableError,
  NonDeterminismError
};
