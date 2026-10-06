import { connection } from "next/server";

export async function GET() {
  await connection();
  return Response.json({ ok: true, runtime: typeof (globalThis as { Bun?: unknown }).Bun === "undefined" ? "node" : "bun" });
}
