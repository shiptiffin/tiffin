// Next.js runs on Bun on the box (`bun --bun next start`), so Bun's built-in
// Postgres client is there: no driver to install. It reads DATABASE_URL,
// which Tiffin sets. Locally, run `bun run dev` with DATABASE_URL set.
export function db() {
  const sql = globalThis.Bun?.sql;
  if (!sql) throw new Error("Postgres needs the Bun runtime: start Next.js with `bun --bun next ...`");
  return sql;
}

let ready;

// Creates the table once per process. Idempotent, so every instance and
// every deploy can run it.
export function migrate() {
  ready ??= db()`create table if not exists notes (
    id bigserial primary key,
    text text not null check (length(text) between 1 and 280),
    created_at timestamptz not null default now()
  )`.catch((err) => {
    ready = undefined;
    throw err;
  });
  return ready;
}
