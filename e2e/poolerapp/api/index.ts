// Every common Postgres client through the box's pooler (DATABASE_URL,
// transaction pooling), with prepared statements, plus LISTEN/NOTIFY over
// DIRECT_DATABASE_URL and a queue route.
import { PrismaPg } from "@prisma/adapter-pg";
import { SQL } from "bun";
import { gt, sql as dsql } from "drizzle-orm";
import { drizzle as drizzleNode } from "drizzle-orm/node-postgres";
import { pgTable, serial, text } from "drizzle-orm/pg-core";
import { drizzle as drizzleJs } from "drizzle-orm/postgres-js";
import pg from "pg";
import postgres from "postgres";
import { PrismaClient } from "./generated/prisma/client";
import { type Client, routes } from "./lib";

const url = process.env.DATABASE_URL!;
const max = Number(process.env.DATABASE_POOL_MAX ?? 10);

const pjs = postgres(url, { max }); // prepares every query (named statements)
const bsql = new SQL({ url, max }); // prepares too
// statement_timeout goes as a startup parameter: the pooler carries it.
const node = new pg.Pool({ connectionString: url, max, statement_timeout: 60_000 });
// node-postgres emits an idle connection's loss on the pool; without a
// listener the process exits (a pooler restart closes idle connections).
node.on("error", (e) => console.error("pg pool:", e.message));
const notes = pgTable("notes", { id: serial("id").primaryKey(), body: text("body") });
const dzJs = drizzleJs(pjs);
const dzNode = drizzleNode(node);
const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: url, max }) });

const dzJsPick = dzJs.select({ id: notes.id }).from(notes).where(gt(notes.id, dsql.placeholder("min"))).limit(1).prepare("dz_js_pick");
const dzNodePick = dzNode.select({ id: notes.id }).from(notes).where(gt(notes.id, dsql.placeholder("min"))).limit(1).prepare("dz_node_pick");

// Half the queries run inside a transaction, so statements prepared on one
// server connection meet others.
const clients: Client[] = [
  {
    name: "postgres.js",
    query: async (i) => {
      if (i % 2) return (await pjs`select ${i}::int as n from notes limit 1`)[0].n;
      return pjs.begin(async (tx) => (await tx`select ${i}::int as n`)[0].n);
    },
  },
  {
    name: "Bun.SQL",
    query: async (i) => {
      if (i % 2) return (await bsql`select ${i}::int as n from notes limit 1`)[0].n;
      return bsql.begin(async (tx) => (await tx`select ${i}::int as n`)[0].n);
    },
  },
  {
    name: "node-postgres",
    query: async (i) => {
      if (i % 2) return (await node.query({ name: "pick", text: "select $1::int as n from notes limit 1", values: [i] })).rows[0].n;
      const c = await node.connect();
      try {
        await c.query("begin");
        const n = (await c.query({ name: "pick_tx", text: "select $1::int as n", values: [i] })).rows[0].n;
        await c.query("commit");
        return n;
      } catch (e) {
        await c.query("rollback").catch(() => {});
        throw e;
      } finally {
        c.release();
      }
    },
  },
  {
    name: "Drizzle (postgres.js)",
    query: async (i) => {
      const [row] = await dzJsPick.execute({ min: -i });
      return row ? i : -1;
    },
  },
  {
    name: "Drizzle (node-postgres)",
    query: async (i) => {
      const [row] = await dzNodePick.execute({ min: -i });
      return row ? i : -1;
    },
  },
  {
    name: "Prisma 7 (adapter-pg)",
    query: async (i) => {
      if (i % 2) {
        const [row] = await prisma.$queryRaw<{ n: number }[]>`select ${i}::int as n`;
        return row.n;
      }
      const [count, first] = await prisma.$transaction([prisma.note.count(), prisma.note.findFirst({ where: { id: { gt: -i } } })]);
      return count > 0 && first ? i : -1;
    },
  },
];

// LISTEN needs a session of its own: DIRECT_DATABASE_URL. The listener
// reconnects by itself when Postgres restarts.
const direct = postgres(process.env.DIRECT_DATABASE_URL!, { max: 2 });
const heard: string[] = [];
await direct.listen("poolx_events", (payload) => {
  heard.push(payload);
});

async function listen(): Promise<Response> {
  const msg = `m${Date.now()}${Math.random()}`;
  const t0 = performance.now();
  await pjs.notify("poolx_events", msg); // NOTIFY works through the pooler; LISTEN does not
  while (!heard.includes(msg)) {
    if (performance.now() - t0 > 10_000) return Response.json({ received: false }, { status: 500 });
    await Bun.sleep(20);
  }
  return Response.json({ received: true, ms: Math.round(performance.now() - t0) });
}

async function info(): Promise<Response> {
  const [pooled] = await pjs`select current_setting('server_version') as version, inet_server_port() as port`;
  const [straight] = await direct`select inet_server_port() as port`;
  return Response.json({ version: pooled.version, pooledPort: process.env.PGPORT, directServerPort: straight.port, poolMax: max });
}

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch(req) {
    const { pathname } = new URL(req.url);
    const r = routes(clients, pathname);
    if (r) return r;
    if (pathname === "/listen") return listen();
    if (pathname === "/info") return info();
    if (pathname === "/queues/ping") return Response.json({ pong: true });
    return new Response("ok");
  },
});
