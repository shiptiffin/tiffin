// The steps that ran for a tag, in order.
export async function GET(req) {
  const tag = new URL(req.url).searchParams.get("tag");
  const sql = globalThis.Bun.sql;
  await sql`create table if not exists e2e_steps (tag text, step text, release text, at timestamptz default now())`;
  return Response.json(await sql`select step, release from e2e_steps where tag = ${tag} order by at`);
}
