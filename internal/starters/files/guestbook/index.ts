import { Hono } from "hono";
import { logger } from "hono/logger";
import { serveStatic } from "hono/bun";
import { redis } from "bun";
import { migrate, sql } from "./db";

// A guestbook in one app: the page (public/), a JSON API, Postgres for the
// entries, Valkey for a visit counter and the box's cookieless analytics.
// Tiffin sets DATABASE_URL (db.ts connects to it), REDIS_URL and
// VALKEY_PREFIX (Bun's built-in `redis` reads the URL) and TIFFIN_ANALYTICS_*
// when analytics is on.

// Migrate on boot.
await migrate();

// Valkey keys must live under this project's prefix.
const prefix = process.env.VALKEY_PREFIX ?? "";

// The analytics tracker script, when analytics is on. Pageviews are counted
// at the edge either way; the script adds custom events (window.tiffin.track).
const tracker = process.env.TIFFIN_ANALYTICS_SCRIPT
  ? `<script defer src="${process.env.TIFFIN_ANALYTICS_SCRIPT}"></script>`
  : "";
const page = (await Bun.file(new URL("./public/index.html", import.meta.url)).text()).replace("<!-- analytics -->", tracker);

// Server-side events go straight to the box's collector.
function track(name: string, props: Record<string, unknown> = {}) {
  const url = process.env.TIFFIN_ANALYTICS_URL;
  const key = process.env.TIFFIN_ANALYTICS_KEY;
  if (!url || !key) return;
  fetch(url + "/track", {
    method: "POST",
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    body: JSON.stringify({ name, props }),
  }).catch(() => {});
}

const app = new Hono();
app.use(logger());

app.get("/", (c) => c.html(page));
app.get("/api/healthz", (c) => c.text("ok"));

app.get("/api/entries", async (c) => {
  const [entries, visits] = await Promise.all([
    sql`select id, name, message, created_at from entries order by id desc limit 50`,
    redis.incr(prefix + "visits"),
  ]);
  return c.json({ entries, visits, greeting: process.env.GREETING ?? "Welcome" });
});

app.post("/api/entries", async (c) => {
  const body = await c.req.json().catch(() => ({}));
  const name = String(body.name ?? "").trim().slice(0, 60);
  const message = String(body.message ?? "").trim().slice(0, 280);
  if (!name || !message) return c.json({ error: "name and message are required" }, 400);
  const [row] = await sql`insert into entries (name, message) values (${name}, ${message})
                          returning id, name, message, created_at`;
  console.log(`new entry #${row.id} from ${name}`);
  track("signed", { length: message.length });
  return c.json(row, 201);
});

app.use("/*", serveStatic({ root: new URL("./public", import.meta.url).pathname }));

const port = Number(process.env.PORT ?? 3000);
console.log(`guestbook listening on :${port}`);

export default { port, fetch: app.fetch };
