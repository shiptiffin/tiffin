// A stand-in for the outside world and for an app, for the Jobs specs
// (e2e/serve-jobs.sh starts it): web addresses that schedules and queues
// call, checked with tiffin-sdk/verify, a job handler that reports progress
// and output, and a workflow that steps, sleeps and renders pages. Every
// call it receives is listed at GET /_calls.
//
//   PORT=7395 TIFFIN_QUEUE_URL=… TIFFIN_QUEUE_KEY=… TIFFIN_QUEUE_SIGNING_SECRET=… bun e2e/jobs-worker.ts
import { defineHandler } from "../../../packages/sdk/src/queue";
import { verifyRequest } from "../../../packages/sdk/src/verify";
import { workflow } from "../../../packages/sdk/src/workflow";

declare const Bun: { serve(o: { port: number; hostname: string; fetch(req: Request): Response | Promise<Response> }): unknown };

const secret = process.env.TIFFIN_QUEUE_SIGNING_SECRET ?? "";
const calls: Array<{ path: string; signed: boolean; id?: string; cron?: string; at: string }> = [];
const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

// A report that takes a few seconds and says how far it got.
const report = defineHandler<{ pages?: number }>(async (job) => {
  const pages = job.payload?.pages ?? 6;
  for (let i = 1; i <= pages; i++) {
    await job.progress({ message: "Rendering pages", done: i, total: pages });
    await job.log(`rendered page ${i} of ${pages}`);
    await wait(900);
  }
  return { pages };
});

workflow.define("monthly-report", async (ctx, input: { month?: string }) => {
  const orders = await ctx.step("load-orders", async () => {
    await wait(1200);
    return { count: 48, month: input?.month ?? "September" };
  });
  await ctx.sleep("5 seconds", "5s");
  const pages = await ctx.step("render-pdf", async () => {
    for (let i = 1; i <= 6; i++) {
      await ctx.progress({ message: "Rendering pages", done: i, total: 6 });
      await ctx.stream(`page ${i} of 6`);
      await wait(900);
    }
    return { pages: 6 };
  });
  await ctx.step("email-owners", async () => {
    await wait(400);
    return { sent: 2 };
  });
  return { orders: orders.count, pages: pages.pages };
});
const turns = workflow.handler();

Bun.serve({
  port: Number(process.env.PORT ?? 7395),
  hostname: "127.0.0.1",
  async fetch(req) {
    const path = new URL(req.url).pathname;
    if (path === "/_calls") return Response.json(calls);
    if (path === "/_tiffin/workflows") return turns(req);
    if (path === "/hooks/report") return report(req);
    const call = await verifyRequest<unknown>(req.clone(), secret);
    calls.push({ path, signed: !!call, id: call?.id, cron: call?.cron, at: new Date().toISOString() });
    if (!call) return new Response("bad signature", { status: 401 });
    if (path === "/hooks/broken") return Response.json({ error: "the warehouse API is down" }, { status: 503 });
    return Response.json({ ok: true, received: call.payload ?? null });
  },
});
console.log("jobs worker ready");
