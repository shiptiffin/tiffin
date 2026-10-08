import { Hono } from "hono";
import { logger } from "hono/logger";
import { migrate, sql } from "./db";

// An API: a small notes service in Hono on Bun, with Postgres on the same box
// (db.ts connects to it).

// Migrate on boot.
await migrate();

const app = new Hono();
app.use(logger());

app.get("/", (c) =>
  c.json({
    name: "notes",
    deploy: process.env.TIFFIN_DEPLOY ?? "local",
    endpoints: {
      "GET /notes": "list notes, newest first (?done=true|false to filter)",
      "POST /notes": 'create one: {"text": "..."}',
      "GET /notes/:id": "read one",
      "PATCH /notes/:id": 'change text and/or done: {"text": "...", "done": true}',
      "DELETE /notes/:id": "delete one",
    },
  }),
);

// The healthcheck in tiffin.config.ts: cheap and dependency-free.
app.get("/healthz", (c) => c.text("ok"));

const id = (raw: string) => (/^\d{1,9}$/.test(raw) ? raw : null);

app.get("/notes", async (c) => {
  const done = c.req.query("done");
  const rows =
    done === "true" || done === "false"
      ? await sql`select * from notes where done = ${done === "true"} order by id desc limit 200`
      : await sql`select * from notes order by id desc limit 200`;
  return c.json({ notes: rows });
});

app.post("/notes", async (c) => {
  const body = await c.req.json().catch(() => ({}));
  const text = typeof body.text === "string" ? body.text.trim() : "";
  if (!text || text.length > 2000) return c.json({ error: "text is required (1-2000 characters)" }, 400);
  const [note] = await sql`insert into notes (text) values (${text}) returning *`;
  return c.json(note, 201);
});

app.get("/notes/:id", async (c) => {
  const n = id(c.req.param("id"));
  const [note] = n ? await sql`select * from notes where id = ${n}` : [];
  return note ? c.json(note) : c.json({ error: "no such note" }, 404);
});

app.patch("/notes/:id", async (c) => {
  const n = id(c.req.param("id"));
  const body = await c.req.json().catch(() => ({}));
  const text = typeof body.text === "string" ? body.text.trim() : null;
  const done = typeof body.done === "boolean" ? body.done : null;
  if (text === null && done === null) return c.json({ error: "send text and/or done" }, 400);
  if (text !== null && (!text || text.length > 2000)) return c.json({ error: "text must be 1-2000 characters" }, 400);
  const [note] = n
    ? await sql`update notes set text = coalesce(${text}, text), done = coalesce(${done}, done), updated_at = now()
                where id = ${n} returning *`
    : [];
  return note ? c.json(note) : c.json({ error: "no such note" }, 404);
});

app.delete("/notes/:id", async (c) => {
  const n = id(c.req.param("id"));
  const [note] = n ? await sql`delete from notes where id = ${n} returning id` : [];
  return note ? c.body(null, 204) : c.json({ error: "no such note" }, 404);
});

app.onError((err, c) => {
  console.error(err);
  return c.json({ error: "internal error" }, 500);
});

const port = Number(process.env.PORT ?? 3000);
console.log(`notes listening on :${port}`);

export default { port, fetch: app.fetch };
