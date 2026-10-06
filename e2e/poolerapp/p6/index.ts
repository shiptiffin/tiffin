// Prisma 6 (its Rust query engine) through the box's pooler, with and
// without the pgbouncer=true flag.
import { PrismaClient } from "@prisma/client";
import { type Client, routes } from "./lib";

const url = process.env.DATABASE_URL!;
const plain = new PrismaClient({ datasourceUrl: url });
const flagged = new PrismaClient({ datasourceUrl: url + "&pgbouncer=true" });

const run = (prisma: PrismaClient) => async (i: number) => {
  if (i % 2) {
    const [row] = await prisma.$queryRaw<{ n: number }[]>`select ${i}::int as n`;
    return row.n;
  }
  const [count, first] = await prisma.$transaction([prisma.note.count(), prisma.note.findFirst({ where: { id: { gt: -i } } })]);
  return count > 0 && first ? i : -1;
};

const clients: Client[] = [
  { name: "Prisma 6", query: run(plain) },
  { name: "Prisma 6 (pgbouncer=true)", query: run(flagged) },
];

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  async fetch(req) {
    const r = routes(clients, new URL(req.url).pathname);
    return r ?? new Response("ok");
  },
});
