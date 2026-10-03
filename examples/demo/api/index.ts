import { Hono } from "hono";
import { sql, redis } from "bun";

// Tiffin gives this app DATABASE_URL and REDIS_URL; Bun's built-in clients
// read them. Valkey keys must live under VALKEY_PREFIX.
const prefix = process.env.VALKEY_PREFIX ?? "";
const app = new Hono().basePath("/api");

await sql`create table if not exists entries (
  id bigserial primary key,
  name text not null check (length(name) between 1 and 60),
  message text not null check (length(message) between 1 and 280),
  created_at timestamptz not null default now()
)`;

app.get("/healthz", (c) => c.text("ok"));

app.get("/entries", async (c) => {
  const rows = await sql`select id, name, message, created_at from entries order by id desc limit 50`;
  const visits = await redis.incr(prefix + "visits");
  return c.json({ entries: rows, visits, greeting: process.env.GREETING ?? "Hello" });
});

app.post("/entries", async (c) => {
  const body = await c.req.json().catch(() => ({}));
  const name = String(body.name ?? "").trim().slice(0, 60);
  const message = String(body.message ?? "").trim().slice(0, 280);
  if (!name || !message) return c.json({ error: "name and message are required" }, 400);
  const [row] = await sql`insert into entries (name, message) values (${name}, ${message}) returning id, name, message, created_at`;
  console.log(`new entry #${row.id} from ${name}`);
  return c.json(row, 201);
});

export default { port: Number(process.env.PORT ?? 3000), fetch: app.fetch };
