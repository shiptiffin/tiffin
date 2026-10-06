import { connection } from "next/server";
import { sql } from "@/lib/db";

export async function GET() {
  await connection();
  const items = await sql()`select id, slug, name, price, stock from products order by id`;
  return Response.json({ items, instance: process.env.TIFFIN_INSTANCE ?? "local" });
}
