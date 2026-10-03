// A demo worker for the dashboard's seeded dev box (e2e/seed-box.sh): queues
// that succeed, crawl and fail, a topic, a cron, two durable workflows and a
// one-off job that backfills a day of analytics through the app's own key.
// Copied over templates/queues-worker/index.ts before `tiffin deploy`.
import { defineHandler, NonRetryableError, workflow } from "./tiffin-sdk.gen.js";

const sleep = (ms: number) => Bun.sleep(ms);
const jitter = (base: number) => base + Math.round(Math.random() * base);

// "emails": quick and reliable.
const emails = defineHandler<{ to: string; template: string }>(async (job) => {
  await sleep(jitter(40));
  return { sent: job.payload.template, to: job.payload.to };
});

// "thumbnails": slow image work, one at a time per key.
const thumbnails = defineHandler<{ key: string }>(
  async (job) => {
    await sleep(jitter(900));
    return { key: job.payload.key, sizes: [160, 480, 1200] };
  },
  { autoHeartbeat: true },
);

// "webhooks": a partner endpoint that is flaky; some deliveries can't succeed.
const webhooks = defineHandler<{ url: string; event: string; broken?: boolean }>(async (job) => {
  await sleep(jitter(120));
  if (job.payload.broken) throw new NonRetryableError(`410 Gone from ${job.payload.url}: the subscriber removed this endpoint`);
  if (job.attempt < 3 && Math.random() < 0.6) throw new Error(`502 Bad Gateway from ${job.payload.url}`);
  return { delivered: job.payload.event, status: 200 };
});

// Topic "order.placed" fans out to these two.
const orderEmail = defineHandler(async (job) => ({ receipt: (job.payload as { order: number }).order }));
const orderStock = defineHandler(async (job) => {
  await sleep(jitter(60));
  return { reserved: (job.payload as { items: number }).items };
});

const nightly = defineHandler(async (job) => ({ report: "daily-revenue", at: (job.payload as { scheduledAt?: string }).scheduledAt }));

// A day of analytics: page views and signups with real-looking visitors.
const backfill = defineHandler(async () => {
  const base = process.env.TIFFIN_ANALYTICS_URL;
  const key = process.env.TIFFIN_ANALYTICS_KEY;
  if (!base || !key) throw new NonRetryableError("analytics is not on for this project");
  const url = base.replace(/\/$/, "") + "/track";
  const probe = await fetch(url, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" }, body: JSON.stringify({ name: "pageview", url: "/" }) });
  if (probe.status !== 202) throw new NonRetryableError(`collector answered ${probe.status}: ${await probe.text()}`);
  const pages = ["/", "/", "/", "/shop", "/shop", "/shop/bowls", "/shop/tiffins", "/shop/tea", "/about", "/journal/brass-care", "/cart", "/checkout"];
  const refs = ["", "", "", "https://news.ycombinator.com/", "https://www.google.com/", "https://www.google.com/", "https://duckduckgo.com/", "https://t.co/x", "https://www.reddit.com/r/Cooking/", "https://bsky.app/"];
  const ips = ["81.2.69.160", "2.125.160.216", "89.160.20.112", "175.16.199.1", "216.160.83.56", "67.43.156.1", "202.196.224.1", "185.86.151.11", "149.101.100.1", "1.128.0.1", "5.145.149.1", "91.198.174.192", "103.21.244.1", "177.37.0.1"];
  const uas = [
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
    "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
  ];
  const pick = <T>(a: T[]) => a[Math.floor(Math.random() * a.length)];
  const post = (body: unknown) =>
    fetch(url, { method: "POST", headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" }, body: JSON.stringify(body) });
  // Visitors arrive through the day (quiet at night, busier in the evening), each
  // from their own address, and are sent oldest first so visits stay visits.
  const now = Date.now();
  const busy = (t: number) => {
    const h = new Date(t).getUTCHours();
    return 0.25 + 0.75 * Math.pow(Math.sin((Math.PI * (h - 4)) / 24), 2);
  };
  const starts: number[] = [];
  while (starts.length < 300) {
    const t = now - Math.random() * 23.5 * 3600_000;
    if (Math.random() < busy(t)) starts.push(t);
  }
  starts.sort((a, b) => a - b);
  let sent = 0;
  for (const [v, start] of starts.entries()) {
    let at = start;
    const ip = pick(ips).replace(/\.\d+$/, `.${1 + ((v * 37) % 250)}`);
    const ua = pick(uas);
    const referrer = pick(refs);
    // Most visits are one or two pages; a few people browse.
    const views = 1 + Math.floor(Math.pow(Math.random(), 2.2) * 5);
    for (let i = 0; i < views; i++) {
      await post({ name: "pageview", url: i === 0 ? pick(pages) : pick(pages.slice(3)), referrer: i === 0 ? referrer : "", ip, ua, at: new Date(at).toISOString() });
      sent++;
      at += 15_000 + Math.round(Math.random() * 75_000);
    }
    if (Math.random() < 0.06) await post({ name: "Signup", props: { plan: pick(["free", "free", "free", "pro"]) }, ip, ua, at: new Date(at).toISOString() });
    if (Math.random() < 0.04) await post({ name: "Checkout", props: { items: String(1 + Math.floor(Math.random() * 3)) }, ip, ua, at: new Date(at).toISOString() });
  }
  return { sent };
});

// Durable workflows.
workflow.define("fulfil-order", async (ctx, input: { order: number; total: number }) => {
  const charge = await ctx.step("charge card", async () => (await sleep(jitter(300)), { charged: input.total, ref: `ch_${input.order}` }));
  await ctx.step("reserve stock", async () => (await sleep(jitter(150)), { reserved: true }));
  const ok = await ctx.approval("ship it", {
    title: `Ship order #${input.order} (${(input.total / 100).toLocaleString("en-US", { style: "currency", currency: "USD" })})?`,
    description: "Big orders get a human look before they leave the warehouse.",
    timeout: "2d",
  });
  if (!ok.approved) {
    await ctx.step("refund", () => ({ refunded: charge.charged }));
    return { status: "refunded", by: ok.by };
  }
  await ctx.sleep("wait for the courier", "20s");
  await ctx.step("print label", () => ({ label: `LBL-${input.order}` }));
  return { status: "shipped", by: ok.by };
});

workflow.define("onboard", async (ctx, input: { email: string; failAt?: string }) => {
  await ctx.step("create account", () => ({ email: input.email }));
  await ctx.step("send welcome", () => ({ sent: true }));
  if (input.failAt === "crm") await ctx.step("sync to CRM", () => Promise.reject(new NonRetryableError("CRM answered 401: the API key was rotated")));
  const verified = await ctx.waitForEvent("email verified", { event: `verified-${input.email}`, timeout: "3d" });
  await ctx.step("unlock features", () => ({ verified: !verified.timedOut }));
  return { done: true };
});

const turns = workflow.handler();
const routes: Record<string, (req: Request) => Promise<Response>> = {
  "/queues/emails": emails,
  "/queues/thumbnails": thumbnails,
  "/queues/webhooks": webhooks,
  "/topics/order-email": orderEmail,
  "/topics/order-stock": orderStock,
  "/cron/nightly": nightly,
  "/queues/backfill": backfill,
  [workflow.DEFAULT_PATH]: turns,
};

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  fetch(req) {
    const { pathname } = new URL(req.url);
    if (pathname === "/healthz") return new Response("ok");
    const h = routes[pathname];
    return h ? h(req) : new Response("not found", { status: 404 });
  },
});
console.log("demo worker ready");
