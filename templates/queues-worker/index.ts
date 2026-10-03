import { defineHandler, NonRetryableError, queue, workflow } from "./tiffin-sdk.gen.js";

// A queue worker on Tiffin. The box POSTs each job to a route here (signed;
// defineHandler checks the signature), retries failures with backoff, and
// moves jobs that keep failing (or throw NonRetryableError) to the
// dead-letter queue: `tiffin queue jobs list jobs --state dead`.

const pid = process.pid;
const running = new Map<string, number>();

// queue "work": payload { ms, fail?: "always" | "never" }.
const work = defineHandler<{ ms?: number; fail?: string }>(
  async (job) => {
    if (job.payload.fail === "always") throw new NonRetryableError("asked to fail");
    const key = job.key ?? "-";
    const now = (running.get(key) ?? 0) + 1;
    running.set(key, now);
    try {
      await Bun.sleep(job.payload.ms ?? 100);
    } finally {
      running.set(key, (running.get(key) ?? 1) - 1);
    }
    // The JSON returned here is stored as the job's output.
    return { pid, key, concurrentForKey: now, attempt: job.attempt, deploy: process.env.TIFFIN_DEPLOY ?? "local" };
  },
  { autoHeartbeat: true }, // long jobs keep their lease while they run
);

// Cron ticks from tiffin.config.ts (payload { cron, schedule, scheduledAt }).
const tick = defineHandler(async (job) => ({ tick: job.payload, pid }));

// A durable workflow: steps are checkpointed, the sleep survives restarts
// and redeploys, and each run stays on the release that started it.
workflow.define("nap", async (ctx, input: { sleep?: string }) => {
  const before = await ctx.step("before", () => ({ deploy: process.env.TIFFIN_DEPLOY ?? "local", at: new Date().toISOString() }));
  await ctx.sleep("nap", input?.sleep ?? "30s");
  const after = await ctx.step("after", () => ({ deploy: process.env.TIFFIN_DEPLOY ?? "local", at: new Date().toISOString() }));
  return { before, after, release: ctx.release };
});
const turns = workflow.handler();

const port = Number(process.env.PORT ?? 3000);
Bun.serve({
  port,
  async fetch(req) {
    const { pathname } = new URL(req.url);
    if (pathname === "/queues/work") return work(req);
    if (pathname === "/cron/tick") return tick(req);
    if (pathname === workflow.DEFAULT_PATH) return turns(req);
    if (pathname === "/healthz") return new Response("ok");
    // Enqueue from inside the app: POST /enqueue?n=10
    if (pathname === "/enqueue" && req.method === "POST") {
      const n = Number(new URL(req.url).searchParams.get("n") ?? 1);
      const jobs = [];
      for (let i = 0; i < n; i++) jobs.push(...(await queue.send("work", { ms: 200 }, { key: `k${i % 3}` })).jobs);
      return Response.json({ jobs });
    }
    return new Response("not found", { status: 404 });
  },
});
console.log(`queues-worker listening on :${port}`);
